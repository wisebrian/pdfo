package pdfo

import (
	"bytes"
	"compress/zlib"
	"errors"
	"math"
	"slices"
	"strconv"

	"github.com/wisebrian/pdfo/internal/content"
	"github.com/wisebrian/pdfo/internal/filter"
	"github.com/wisebrian/pdfo/internal/raster"
	"github.com/wisebrian/pdfo/internal/syntax"
	"github.com/wisebrian/pdfo/internal/writer"
)

// CompressOptions sets the target for images.
type CompressOptions struct {
	// DPI is the resolution images are reduced to. Only images drawn at
	// more than 1.5 × DPI are downsampled: below that the saving does not
	// pay for the blur (Ghostscript uses the same threshold). Zero keeps
	// every image's resolution.
	DPI float64
	// Quality is the JPEG quality, 1 to 100, of re-encoded images.
	Quality int
	// Write controls how the file is written.
	Write WriteOptions
}

// Presets for the common trade-offs; copy one and change a field to
// customise.
var (
	// Screen is for reading on a screen: about 100 dpi, quality 60.
	Screen = CompressOptions{DPI: 100, Quality: 60}
	// Ebook keeps images sharp on tablets and in zoom: 150 dpi, quality 70.
	Ebook = CompressOptions{DPI: 150, Quality: 70}
	// Print keeps what a printer can use: 300 dpi, quality 85.
	Print = CompressOptions{DPI: 300, Quality: 85}
)

func (o *CompressOptions) check() error {
	if !(o.DPI >= 0 && o.DPI <= 10000) {
		return &OptionError{"DPI", "must be between 0 and 10000"}
	}
	if o.Quality < 1 || o.Quality > 100 {
		return &OptionError{"Quality", "must be between 1 and 100"}
	}
	return o.Write.check()
}

// downsampleThreshold is how far above the target an image must be drawn
// before it is downsampled.
const downsampleThreshold = 1.5

// minGain is the least an image must shrink to be replaced: re-encoding
// loses quality, and a 1% saving is not worth any.
const minGain = 0.98

// CompressionReport says what Compress did, image by image.
type CompressionReport struct {
	// SizeBefore and SizeAfter are the sizes of the files, in bytes.
	SizeBefore, SizeAfter int64
	// Images lists every image the pages use, by first page.
	Images []ImageResult
}

// ImageResult is one image's line in the report.
type ImageResult struct {
	// Page is the first page that draws the image, numbered from 1; 0 for
	// an image no page's content draws in a way that can be followed.
	Page int
	// Width and Height are its size in pixels, before.
	Width, Height int
	// DPI is the lowest resolution it is drawn at; 0 when unknown.
	DPI float64
	// BytesBefore and BytesAfter are the sizes of its stream, as stored.
	BytesBefore, BytesAfter int64
	// Action is "downsampled", "re-encoded", "recompressed" (losslessly)
	// or "unchanged".
	Action string
	// Reason says why the image was not downsampled or re-encoded, such as
	// "CMYK", "JPEG 2000" or "not smaller".
	Reason string
}

// Compress rewrites d smaller. Images drawn at more than 1.5 × DPI are
// downsampled with an area-averaging filter; JPEG images and lossless
// images that end up smaller as JPEG are re-encoded at Quality. An image is
// replaced only when the result is at least 2% smaller. Identical streams
// are stored once, Flate streams are recompressed at the best level when
// that is smaller, uncompressed streams are compressed, objects nothing
// uses are dropped, and the file is written with object streams.
//
// Images left as they are: CMYK, indexed, 1-bit, 16-bit, Lab, Separation
// and DeviceN images; JPEG 2000, JBIG2 and CCITT streams; image masks and
// images with a colour-key or stencil mask; inline images. Each one's
// line in the report says why.
//
// Everything else in the document is kept, as for any action that keeps
// every page; private application data (PieceInfo) is removed.
func Compress(d *Document, opts *CompressOptions) (*Output, error) {
	o := Ebook
	if opts != nil {
		o = *opts
	}
	if err := o.check(); err != nil {
		return nil, err
	}
	sc := content.NewScanner(d.r, d.limits.MaxDepth)
	for i, p := range d.pages {
		sc.Page(i, p)
	}
	c := &compressor{
		opts: o, doc: d, uses: sc.Uses,
		alias: map[uint32]uint32{}, smasks: map[uint32]bool{},
	}
	c.prepare()
	a := &assembler{w: writer.New(), opts: o.Write, cmp: c}
	pages := make([]PageRef, d.NumPages())
	for i := range pages {
		pages[i] = PageRef{Doc: d, Index: i}
	}
	out, err := a.run(pages)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(c.results, func(x, y ImageResult) int {
		px, py := x.Page, y.Page
		if px == 0 {
			px = math.MaxInt
		}
		if py == 0 {
			py = math.MaxInt
		}
		return px - py
	})
	if c.results == nil {
		c.results = []ImageResult{}
	}
	out.Report.Compression = &CompressionReport{SizeBefore: d.r.Size(), SizeAfter: out.Size(), Images: c.results}
	return out, nil
}

type compressor struct {
	opts    CompressOptions
	doc     *Document
	uses    map[uint32]*content.Use
	alias   map[uint32]uint32 // duplicate stream to the one kept
	smasks  map[uint32]bool   // soft masks, which go with their image
	results []ImageResult
}

// prepare finds identical streams and soft masks. Streams are grouped by
// dictionary and length first, so only streams that could be equal are
// compared, byte for byte.
func (c *compressor) prepare() {
	r := c.doc.r
	type key struct {
		dict string
		n    int
	}
	groups := map[key][]uint32{}
	for _, num := range r.Nums() {
		st, ok := r.Object(num).(*syntax.Stream)
		if !ok {
			continue
		}
		if t, _ := st.Dict.Get("Subtype").(syntax.Name); t == "Image" {
			if sm, ok := st.Dict.Get("SMask").(syntax.Ref); ok {
				c.smasks[sm.Num] = true
			}
		}
		d := st.Dict.Clone()
		d.Delete("Length")
		k := key{string(syntax.Append(nil, d)), len(st.Data)}
		data, err := r.Raw(st)
		if err != nil {
			continue
		}
		matched := false
		for _, other := range groups[k] {
			os := r.Object(other).(*syntax.Stream)
			od, err := r.Raw(os)
			if err == nil && bytes.Equal(od, data) {
				c.alias[num] = other
				matched = true
				break
			}
		}
		if !matched && len(groups[k]) < 8 {
			groups[k] = append(groups[k], num)
		}
	}
	// A page may draw the duplicate; what is copied is the one kept.
	for dup, canon := range c.alias {
		u, ok := c.uses[dup]
		if !ok {
			continue
		}
		if cu, ok := c.uses[canon]; ok {
			cu.W, cu.H = math.Max(cu.W, u.W), math.Max(cu.H, u.H)
			cu.Page = min(cu.Page, u.Page)
		} else {
			c.uses[canon] = u
		}
	}
}

// stream copies one stream into the output, compressing what it can.
func (c *compressor) stream(a *assembler, j job, st *syntax.Stream) {
	if t, _ := st.Dict.Get("Subtype").(syntax.Name); t == "Image" && !c.smasks[j.num] {
		res := c.image(a, j, st)
		if res.replaced {
			c.results = append(c.results, res.ImageResult)
			return
		}
		res.BytesAfter = c.lossless(a, j, st)
		if res.BytesAfter < res.BytesBefore {
			res.Action = "recompressed"
		}
		c.results = append(c.results, res.ImageResult)
		return
	}
	c.lossless(a, j, st)
}

type imageResult struct {
	ImageResult
	replaced bool
}

// lossless writes a stream with its data compressed better when that is
// possible without changing what it decodes to, and returns the size
// written.
func (c *compressor) lossless(a *assembler, j job, st *syntax.Stream) int64 {
	r := j.src.r
	d := a.streamDict(j.src, st)
	if d.Get("PieceInfo") != nil {
		d.Delete("PieceInfo")
	}
	raw, err := r.Raw(st)
	if err != nil {
		a.w.PutStream(j.dst, d)
		return 0
	}
	fs, _ := r.Filters(st.Dict)
	t, _ := st.Dict.Get("Type").(syntax.Name)
	put := func(data []byte, filterOnly bool) int64 {
		if filterOnly {
			d.Set("Filter", syntax.Name("FlateDecode"))
			d.Delete("DecodeParms")
			d.Delete("DP")
		}
		a.w.PutStream(j.dst, d, data)
		return int64(len(data))
	}
	b := r.Budget()
	switch {
	case len(fs) == 1 && fs[0] == "FlateDecode" || len(fs) == 1 && fs[0] == "Fl":
		if inflated, err := filter.Inflate(raw, b); err == nil {
			if re := deflateBest(inflated); len(re) < len(raw) {
				// The data keeps its predictor; only the deflating changes.
				d.Set("Filter", syntax.Name("FlateDecode"))
				a.w.PutStream(j.dst, d, re)
				return int64(len(re))
			}
		}
	case len(fs) == 0 && t != "Metadata":
		// XMP metadata stays readable to tools that look for it in
		// clear; PDF/A requires it.
		if re := deflateBest(raw); len(re) < len(raw) {
			return put(re, true)
		}
	case len(fs) > 0 && onlyGeneral(fs):
		if decoded, err := r.Decode(st); err == nil {
			if re := deflateBest(decoded); len(re) < len(raw) {
				return put(re, true)
			}
		}
	}
	return put(raw, false)
}

// onlyGeneral reports whether every filter is one this package can decode
// and replace by Flate.
func onlyGeneral(fs []syntax.Name) bool {
	for _, f := range fs {
		switch f {
		case "FlateDecode", "Fl", "LZWDecode", "LZW", "ASCIIHexDecode", "AHx", "ASCII85Decode", "A85", "RunLengthDecode", "RL":
		default:
			return false
		}
	}
	return true
}

func deflateBest(data []byte) []byte {
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	zw.Write(data)
	zw.Close()
	return buf.Bytes()
}

// image downsamples and re-encodes an image when it can and when that makes
// it smaller, writing it and reporting replaced; otherwise it only fills in
// the report, and the caller copies the image losslessly.
func (c *compressor) image(a *assembler, j job, st *syntax.Stream) imageResult {
	r := j.src.r
	d := st.Dict
	w, _ := r.Resolve(d.Get("Width")).(syntax.Int)
	h, _ := r.Resolve(d.Get("Height")).(syntax.Int)
	res := imageResult{ImageResult: ImageResult{
		Width: int(w), Height: int(h), BytesBefore: int64(len(st.Data)), BytesAfter: int64(len(st.Data)),
		Action: "unchanged",
	}}
	u := c.uses[j.num]
	var effX, effY float64
	if u != nil {
		res.Page = u.Page + 1
		if u.W > 0 && u.H > 0 && w > 0 && h > 0 {
			effX, effY = float64(w)/(u.W/72), float64(h)/(u.H/72)
			res.DPI = math.Round(math.Min(effX, effY))
		}
	}
	skip := func(why string) imageResult {
		res.Reason = why
		return res
	}
	if m, _ := r.Resolve(d.Get("ImageMask")).(syntax.Bool); m {
		return skip("image mask")
	}
	if d.Get("Mask") != nil {
		return skip("colour-key or stencil mask")
	}
	fs, _ := r.Filters(d)
	isDCT := false
	for i, f := range fs {
		switch f {
		case "JPXDecode":
			return skip("JPEG 2000")
		case "JBIG2Decode":
			return skip("JBIG2")
		case "CCITTFaxDecode", "CCF":
			return skip("CCITT fax")
		case "DCTDecode", "DCT":
			if i != len(fs)-1 {
				return skip("unusual filters")
			}
			isDCT = true
		}
	}
	if bpc, _ := r.Resolve(d.Get("BitsPerComponent")).(syntax.Int); bpc != 8 {
		return skip(strconv.Itoa(int(bpc)) + "-bit samples")
	}
	comps, why := c.components(d.Get("ColorSpace"))
	if why != "" {
		return skip(why)
	}
	if d.Get("Decode") != nil {
		return skip("Decode array")
	}
	if w <= 0 || h <= 0 {
		return skip("damaged")
	}
	if int64(w)*int64(h) > c.doc.limits.MaxImagePixels {
		return skip("too large to decode")
	}

	var src raster.Source
	if isDCT {
		raw, err := r.Raw(st)
		if err == nil && len(fs) > 1 {
			raw, err = filter.Decode(raw, fs[:len(fs)-1], nil, r.Budget())
		}
		if err != nil {
			return skip("damaged")
		}
		js, err := raster.DecodeJPEG(raw, c.doc.limits.MaxImagePixels)
		switch {
		case errors.Is(err, raster.ErrCMYK):
			return skip("CMYK")
		case err != nil:
			return skip("damaged")
		}
		if js.Comps() != comps {
			return skip("damaged")
		}
		src = js
	} else {
		data, err := r.Decode(st)
		if err != nil {
			return skip("damaged")
		}
		img, err := raster.Samples(data, int(w), int(h), comps)
		if err != nil {
			return skip("damaged")
		}
		src = img
	}

	nw, nh := int(w), int(h)
	down := false
	if c.opts.DPI > 0 && effX > 0 && effY > 0 && math.Min(effX, effY) > c.opts.DPI*downsampleThreshold {
		nw = max(1, min(int(w), int(math.Round(float64(w)*c.opts.DPI/effX))))
		nh = max(1, min(int(h), int(math.Round(float64(h)*c.opts.DPI/effY))))
		down = nw < int(w) || nh < int(h)
	}
	if !down && !isDCT && u == nil {
		// A lossless image no page is seen to draw: without knowing its
		// size there is no case for making it lossy.
		return skip("not drawn by any page")
	}
	out := src
	if down {
		out = raster.Resize(src, nw, nh)
	}
	data, err := raster.EncodeJPEG(out, c.opts.Quality)
	if err != nil {
		return skip("could not be re-encoded")
	}
	filterName := syntax.Name("DCTDecode")
	if !isDCT && down {
		// Screenshots and drawings often stay smaller, and sharper,
		// without JPEG.
		if fl, err := raster.EncodeFlate(out); err == nil && len(fl) < len(data) {
			data, filterName = fl, "FlateDecode"
		}
	}
	if float64(len(data)) >= float64(len(st.Data))*minGain {
		return skip("not smaller")
	}

	nd := new(syntax.Dict)
	for i := range d.Len() {
		k, v := d.At(i)
		switch k {
		case "Length", "Filter", "DecodeParms", "DP", "Width", "Height", "SMask", "PieceInfo":
		default:
			nd.Set(k, a.copy(j.src, v))
		}
	}
	nd.Set("Width", syntax.Int(nw))
	nd.Set("Height", syntax.Int(nh))
	nd.Set("Filter", filterName)
	if filterName == "FlateDecode" {
		nd.Set("DecodeParms", filter.PredictorParms(comps, 8, nw))
	}
	if sm := d.Get("SMask"); sm != nil {
		if down {
			nd.Set("SMask", c.mask(a, j, sm, float64(nw)/float64(w), float64(nh)/float64(h)))
		} else {
			nd.Set("SMask", a.copy(j.src, sm))
		}
	}
	a.w.PutStream(j.dst, nd, data)
	res.replaced = true
	res.BytesAfter = int64(len(data))
	res.Action = "re-encoded"
	if down {
		res.Action = "downsampled"
	}
	return res
}

// mask scales a soft mask down with its image and stores it with Flate.
// A mask that cannot be decoded is kept as it is: a soft mask may differ in
// size from its image.
func (c *compressor) mask(a *assembler, j job, sm syntax.Object, sx, sy float64) syntax.Object {
	r := j.src.r
	st, ok := r.Resolve(sm).(*syntax.Stream)
	if !ok {
		return a.copy(j.src, sm)
	}
	w, _ := r.Resolve(st.Dict.Get("Width")).(syntax.Int)
	h, _ := r.Resolve(st.Dict.Get("Height")).(syntax.Int)
	bpc, _ := r.Resolve(st.Dict.Get("BitsPerComponent")).(syntax.Int)
	data, err := r.Decode(st)
	if err != nil || bpc != 8 || w <= 0 || h <= 0 || st.Dict.Get("Decode") != nil {
		return a.copy(j.src, sm)
	}
	img, err := raster.Samples(data, int(w), int(h), 1)
	if err != nil {
		return a.copy(j.src, sm)
	}
	nw := max(1, int(math.Round(float64(w)*sx)))
	nh := max(1, int(math.Round(float64(h)*sy)))
	enc, err := raster.EncodeFlate(raster.Resize(img, nw, nh))
	if err != nil {
		return a.copy(j.src, sm)
	}
	nd := new(syntax.Dict)
	for i := range st.Dict.Len() {
		k, v := st.Dict.At(i)
		switch k {
		case "Length", "Filter", "DecodeParms", "DP", "Width", "Height":
		default:
			nd.Set(k, a.copy(j.src, v))
		}
	}
	nd.Set("Width", syntax.Int(nw))
	nd.Set("Height", syntax.Int(nh))
	nd.Set("Filter", syntax.Name("FlateDecode"))
	nd.Set("DecodeParms", filter.PredictorParms(1, 8, nw))
	return a.w.AddStream(nd, enc)
}

// components returns the colour components of a colour space this package
// can resample, or why it cannot. Colour spaces with one or three
// components are kept as they are, profiles included: resampling changes
// pixels, not what their values mean.
func (c *compressor) components(cs syntax.Object) (int, string) {
	r := c.doc.r
	switch v := r.Resolve(cs).(type) {
	case syntax.Name:
		switch v {
		case "DeviceGray", "G":
			return 1, ""
		case "DeviceRGB", "RGB":
			return 3, ""
		case "DeviceCMYK", "CMYK":
			return 0, "CMYK"
		}
		return 0, "colour space " + string(v)
	case syntax.Array:
		if len(v) == 0 {
			return 0, "damaged"
		}
		family, _ := r.Resolve(v[0]).(syntax.Name)
		switch family {
		case "ICCBased":
			if len(v) < 2 {
				return 0, "damaged"
			}
			st, ok := r.Resolve(v[1]).(*syntax.Stream)
			if !ok {
				return 0, "damaged"
			}
			switch n, _ := r.Resolve(st.Dict.Get("N")).(syntax.Int); n {
			case 1, 3:
				return int(n), ""
			case 4:
				return 0, "CMYK"
			}
			return 0, "colour space with unusual components"
		case "CalRGB":
			return 3, ""
		case "CalGray":
			return 1, ""
		case "Indexed", "I":
			return 0, "indexed colours"
		case "Lab":
			return 0, "Lab colours"
		case "Separation":
			return 0, "spot colour"
		case "DeviceN":
			return 0, "DeviceN colours"
		}
		return 0, "colour space " + string(family)
	}
	return 0, "no colour space"
}

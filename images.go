package pdfo

import (
	"bytes"
	"compress/zlib"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"

	"github.com/wisebrian/pdfo/internal/filter"
	"github.com/wisebrian/pdfo/internal/imgfmt"
	"github.com/wisebrian/pdfo/internal/syntax"
	"github.com/wisebrian/pdfo/internal/writer"
)

// Fit says how an image fills the area inside the margins. Neither choice
// stretches: a distorted photo is never what anyone wants.
type Fit int

const (
	// Contain shows all of the image, centred, with blank bands where the
	// shapes differ.
	Contain Fit = iota
	// Cover fills the area, centred, and crops what overflows.
	Cover
)

// ImageOptions controls FromImages. The zero value makes each page the size
// of its image, with no margin.
type ImageOptions struct {
	// PageSize is the paper size. Zero sizes each page to its image at the
	// resolution the image declares (JFIF, EXIF or PNG pHYs), or 72 dpi when
	// it declares none.
	PageSize Size
	// Orientation turns PageSize; it does not apply when pages are sized to
	// their images.
	Orientation Orientation
	// Margin is the blank border on every side, in points.
	Margin float64
	// Fit says how the image fills the area inside the margins.
	Fit Fit
	// Limits bounds the work a hostile image can cause.
	Limits Limits
	// Write controls how the file is written.
	Write WriteOptions
}

func (o *ImageOptions) check() error {
	if s := o.PageSize; s != (Size{}) {
		ok := func(v float64) bool { return v >= minPage && v <= maxPage }
		if !ok(s.Width) || !ok(s.Height) {
			return &OptionError{"PageSize", "each side must be between 3 and 14400 points (1–5080 mm)"}
		}
	}
	if !(o.Margin >= 0) || math.IsInf(o.Margin, 0) {
		return &OptionError{"Margin", "must be zero or more"}
	}
	if o.Orientation < Auto || o.Orientation > Landscape {
		return &OptionError{"Orientation", "unknown orientation"}
	}
	if o.Fit != Contain && o.Fit != Cover {
		return &OptionError{"Fit", "unknown fit"}
	}
	if err := o.Limits.check(); err != nil {
		return err
	}
	return o.Write.check()
}

// FromImages makes a PDF with one page per image, in order. Formats are
// recognised by content, not by name.
//
// A JPEG is embedded without decoding: re-encoding would lose quality for
// nothing. Its EXIF orientation goes into the page's drawing matrix, because
// PDF viewers ignore EXIF and a phone photo would otherwise lie on its side.
// Its metadata is left out, since a PDF is often shared where a photo is not
// and EXIF can hold the place where the photo was taken. A PNG is decoded and
// stored losslessly; its alpha becomes a soft mask. Colour profiles are
// kept, so photos in wide-gamut colour (Display P3 on iPhones) do not look
// washed out.
//
// The Output refers to the JPEG data in images, which must not change until
// it has been written.
func FromImages(images [][]byte, opts *ImageOptions) (*Output, error) {
	var o ImageOptions
	if opts != nil {
		o = *opts
	}
	if err := o.check(); err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, &OptionError{"images", "no images given"}
	}
	b := &imageBuilder{
		w:        writer.New(),
		limits:   o.Limits.withDefaults(),
		profiles: map[string]syntax.Ref{},
	}
	catalog, pages := b.w.Alloc(), b.w.Alloc()
	var kids syntax.Array
	for i, data := range images {
		page := i + 1
		img, err := b.embed(data, page)
		if err != nil {
			return nil, &ImageError{Index: i, Err: err}
		}
		p, err := planPage(img, &o)
		if err != nil {
			return nil, err
		}
		if p.limited {
			b.warn.add("page-size-limited", "an image was too large for a page at its resolution and was scaled down to fit Acrobat's limit of 200 inches", page)
		}
		content := b.w.AddStream(new(syntax.Dict), p.content(img.orientation))
		xobj := new(syntax.Dict)
		xobj.Set("Im0", img.ref)
		res := new(syntax.Dict)
		res.Set("XObject", xobj)
		pd := new(syntax.Dict)
		pd.Set("Type", syntax.Name("Page"))
		pd.Set("Parent", pages)
		pd.Set("MediaBox", syntax.Array{syntax.Int(0), syntax.Int(0), syntax.Real(p.w), syntax.Real(p.h)})
		pd.Set("Resources", res)
		pd.Set("Contents", content)
		kids = append(kids, b.w.Add(pd))
	}
	pd := new(syntax.Dict)
	pd.Set("Type", syntax.Name("Pages"))
	pd.Set("Kids", kids)
	pd.Set("Count", syntax.Int(len(kids)))
	b.w.Put(pages, pd)
	cd := new(syntax.Dict)
	cd.Set("Type", syntax.Name("Catalog"))
	cd.Set("Pages", pages)
	b.w.Put(catalog, cd)
	var infoRef syntax.Ref
	if info := o.Write.info(true); info != nil {
		infoRef = b.w.Add(info)
	}
	f, err := b.w.Finish(writer.Options{
		Version:   "1.7",
		XRefTable: o.Write.XRefTable,
		Root:      catalog,
		Info:      infoRef,
	})
	if err != nil {
		return nil, err
	}
	return &Output{
		Report: Report{Pages: len(kids), Warnings: b.warn.result()},
		file:   f,
	}, nil
}

// imageBuilder turns image files into image XObjects of one output file.
type imageBuilder struct {
	w        *writer.Writer
	limits   Limits
	profiles map[string]syntax.Ref // colour profile streams, by content, so each is stored once
	warn     warnings
}

// embedded is an image XObject and what placing it needs.
type embedded struct {
	ref           syntax.Ref
	width, height int     // pixels, as stored
	orientation   int     // EXIF orientation, 1 to 8
	dpiX, dpiY    float64 // declared resolution, 0 when none
}

func (b *imageBuilder) embed(data []byte, page int) (*embedded, error) {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return b.jpeg(data, page)
	case imgfmt.IsPNG(data):
		return b.png(data, page)
	}
	return nil, sniffError(data)
}

// sniffError names the formats people most often try, so the message can
// say what to do instead of only "not an image".
func sniffError(b []byte) error {
	brand := ""
	if len(b) >= 12 && string(b[4:8]) == "ftyp" {
		brand = string(b[8:12])
	}
	name := ""
	switch {
	case bytes.HasPrefix(b, []byte("GIF8")):
		name = "GIF"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		name = "WebP"
	case brand == "heic" || brand == "heix" || brand == "hevc" || brand == "heim" || brand == "heis" || brand == "mif1" || brand == "msf1":
		name = "HEIC"
	case brand == "avif" || brand == "avis":
		name = "AVIF"
	case bytes.HasPrefix(b, []byte("II*\x00")) || bytes.HasPrefix(b, []byte("MM\x00*")):
		name = "TIFF"
	case bytes.HasPrefix(b, []byte("BM")):
		name = "BMP"
	case bytes.HasPrefix(b, []byte("%PDF")):
		return &FormatError{Offset: -1, Msg: "this is a PDF, not an image; merge PDFs instead"}
	}
	if name != "" {
		return imgfmt.Unsupported(name + " (convert it to JPEG or PNG first)")
	}
	return &FormatError{Offset: -1, Msg: "not a JPEG or PNG image"}
}

func (b *imageBuilder) jpeg(data []byte, page int) (*embedded, error) {
	j, err := imgfmt.ParseJPEG(data)
	if err != nil {
		return nil, imageErr(err)
	}
	d := imageDict(j.Width, j.Height, 8)
	d.Set("ColorSpace", b.colorSpace(j.Components, j.ICC, j.ICCProblem, page))
	if j.Components == 4 && j.Adobe {
		// Adobe's applications store CMYK JPEGs inverted; the inverted
		// Decode array is how a PDF says so.
		d.Set("Decode", syntax.Array{
			syntax.Int(1), syntax.Int(0), syntax.Int(1), syntax.Int(0),
			syntax.Int(1), syntax.Int(0), syntax.Int(1), syntax.Int(0),
		})
	}
	if def := defaultColorTransform(j.Components); j.ColorTransform != def {
		// DCTDecode assumes YCbCr for three components and no transform
		// otherwise; say so when the file differs.
		parms := new(syntax.Dict)
		parms.Set("ColorTransform", syntax.Int(j.ColorTransform))
		d.Set("DecodeParms", parms)
	}
	d.Set("Filter", syntax.Name("DCTDecode"))
	if j.Truncated {
		b.warn.add("jpeg-truncated", "a JPEG ends early; viewers show the part that is there", page)
	}
	ref := b.w.AddStream(d, j.Head, j.Body, j.Tail)
	return &embedded{ref, j.Width, j.Height, j.Orientation, j.DPIX, j.DPIY}, nil
}

// imageErr turns an imgfmt error into this package's: unsupported features
// pass through, anything else is damage.
func imageErr(err error) error {
	if errors.Is(err, errors.ErrUnsupported) {
		return err
	}
	return &FormatError{Offset: -1, Msg: err.Error()}
}

func imageDict(w, h, bpc int) *syntax.Dict {
	d := new(syntax.Dict)
	d.Set("Type", syntax.Name("XObject"))
	d.Set("Subtype", syntax.Name("Image"))
	d.Set("Width", syntax.Int(w))
	d.Set("Height", syntax.Int(h))
	d.Set("BitsPerComponent", syntax.Int(bpc))
	return d
}

var deviceSpaces = map[int]syntax.Name{1: "DeviceGray", 3: "DeviceRGB", 4: "DeviceCMYK"}

// colorSpace returns the colour space for an image with n components: its
// own profile when it has a usable one, the device space otherwise.
func (b *imageBuilder) colorSpace(n int, icc []byte, problem string, page int) syntax.Object {
	if problem != "" {
		b.warn.add("icc-profile-ignored", "an image's colour profile was unusable ("+problem+") and was left out; its colours may look slightly different", page)
	}
	if icc == nil {
		return deviceSpaces[n]
	}
	ref, ok := b.profiles[string(icc)]
	if !ok {
		d := new(syntax.Dict)
		d.Set("N", syntax.Int(n))
		d.Set("Alternate", deviceSpaces[n])
		d.Set("Filter", syntax.Name("FlateDecode"))
		ref = b.w.AddStream(d, deflate(icc))
		b.profiles[string(icc)] = ref
	}
	return syntax.Array{syntax.Name("ICCBased"), ref}
}

func deflate(data []byte) []byte {
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	zw.Write(data)
	zw.Close()
	return buf.Bytes()
}

// pngLevel is the zlib level for PNG samples. Level 9 searches much longer
// for a few percent less, which on a 48-megapixel image is seconds in a
// browser tab; level 6 is what PNG encoders themselves default to.
const pngLevel = zlib.DefaultCompression

// defaultColorTransform is the ColorTransform DCTDecode assumes when a
// stream does not give one (ISO 32000-2, table 13).
func defaultColorTransform(components int) int {
	if components == 3 {
		return 1
	}
	return 0
}

func (b *imageBuilder) png(data []byte, page int) (*embedded, error) {
	p, err := imgfmt.ParsePNG(data)
	if err != nil {
		return nil, imageErr(err)
	}
	// image/png allocates the whole image up front, from the header's
	// numbers alone, so the check must come before decoding.
	if int64(p.Width)*int64(p.Height) > b.limits.MaxImagePixels {
		return nil, &LimitError{Limit: "image size (pixels)", Value: b.limits.MaxImagePixels}
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, &FormatError{Offset: -1, Msg: err.Error()}
	}
	var (
		d       *syntax.Dict
		samples [][]byte
	)
	if pal, ok := img.(*image.Paletted); ok && p.ColorType == imgfmt.PNGPalette {
		d, samples, err = b.indexed(pal, p, page)
	} else {
		comps := 3
		if p.ColorType == imgfmt.PNGGray || p.ColorType == imgfmt.PNGGrayAlpha {
			comps = 1
		}
		d, samples, err = b.direct(img, comps, p, page)
	}
	if err != nil {
		return nil, err
	}
	return &embedded{
		ref:   b.w.AddStream(d, samples...),
		width: p.Width, height: p.Height,
		orientation: p.Orientation, dpiX: p.DPIX, dpiY: p.DPIY,
	}, nil
}

// direct stores a gray or RGB image as 8-bit samples, predicted and
// compressed, with its alpha, if it has any, as a soft mask.
func (b *imageBuilder) direct(img image.Image, comps int, p *imgfmt.PNG, page int) (*syntax.Dict, [][]byte, error) {
	src := newRowSource(img, comps)
	w, h := src.w, src.h
	var buf writer.Chunks
	rw, err := filter.NewRowWriter(&buf, pngLevel, w*comps, comps, true)
	if err != nil {
		return nil, nil, err
	}
	// Whether the image needs a soft mask is learnt on the way: a second
	// pass over the alpha happens only when some pixel is transparent.
	opaque := true
	for y := range h {
		c, a := src.row(y)
		if opaque && a != nil && !allOpaque(a) {
			opaque = false
		}
		if err := rw.WriteRow(c); err != nil {
			return nil, nil, err
		}
	}
	if err := rw.Close(); err != nil {
		return nil, nil, err
	}
	d := imageDict(w, h, 8)
	d.Set("ColorSpace", b.colorSpace(comps, p.ICC, p.ICCProblem, page))
	d.Set("Filter", syntax.Name("FlateDecode"))
	d.Set("DecodeParms", filter.PredictorParms(comps, 8, w))
	if !opaque {
		mask, err := b.softMask(w, h, func(y int) []byte { _, a := src.row(y); return a })
		if err != nil {
			return nil, nil, err
		}
		d.Set("SMask", mask)
	}
	return d, buf.Blocks(), nil
}

// indexed stores a palette image as it is, at its own bit depth: an
// Indexed colour space keeps it a quarter or less of the size RGB would be.
func (b *imageBuilder) indexed(m *image.Paletted, p *imgfmt.PNG, page int) (*syntax.Dict, [][]byte, error) {
	r := m.Bounds()
	w, h := r.Dx(), r.Dy()
	n := len(m.Palette)
	if n == 0 {
		return nil, nil, &FormatError{Offset: -1, Msg: "PNG palette is empty"}
	}
	bpc := p.BitDepth
	if n > 1<<bpc {
		bpc = 8
	}
	lookup := make([]byte, 0, 3*n)
	alpha := make([]byte, n)
	for i, c := range m.Palette {
		nc := color.NRGBAModel.Convert(c).(color.NRGBA)
		lookup = append(lookup, nc.R, nc.G, nc.B)
		alpha[i] = nc.A
	}
	index := func(x, y int) int {
		i := int(m.Pix[(y-r.Min.Y)*m.Stride+(x-r.Min.X)])
		if i >= n {
			i = 0 // image/png pads the palette, so this cannot happen
		}
		return i
	}
	opaque := true
	for y := r.Min.Y; y < r.Max.Y && opaque; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if alpha[index(x, y)] != 255 {
				opaque = false
				break
			}
		}
	}
	rowLen := (w*bpc + 7) / 8
	var buf writer.Chunks
	// PNG predictors do not help indices, which are labels, not levels.
	rw, err := filter.NewRowWriter(&buf, pngLevel, rowLen, 1, false)
	if err != nil {
		return nil, nil, err
	}
	row := make([]byte, rowLen)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		clear(row)
		for x := range w {
			bit := x * bpc
			row[bit/8] |= byte(index(r.Min.X+x, y)) << (8 - bpc - bit%8)
		}
		if err := rw.WriteRow(row); err != nil {
			return nil, nil, err
		}
	}
	if err := rw.Close(); err != nil {
		return nil, nil, err
	}
	d := imageDict(w, h, bpc)
	d.Set("ColorSpace", syntax.Array{
		syntax.Name("Indexed"),
		b.colorSpace(3, p.ICC, p.ICCProblem, page),
		syntax.Int(n - 1),
		syntax.String(lookup),
	})
	d.Set("Filter", syntax.Name("FlateDecode"))
	if !opaque {
		arow := make([]byte, w)
		mask, err := b.softMask(w, h, func(y int) []byte {
			for x := range w {
				arow[x] = alpha[index(r.Min.X+x, r.Min.Y+y)]
			}
			return arow
		})
		if err != nil {
			return nil, nil, err
		}
		d.Set("SMask", mask)
	}
	return d, buf.Blocks(), nil
}

// softMask stores alpha rows as an 8-bit gray image for /SMask.
func (b *imageBuilder) softMask(w, h int, alphaRow func(y int) []byte) (syntax.Ref, error) {
	var buf writer.Chunks
	rw, err := filter.NewRowWriter(&buf, pngLevel, w, 1, true)
	if err != nil {
		return syntax.Ref{}, err
	}
	for y := range h {
		if err := rw.WriteRow(alphaRow(y)); err != nil {
			return syntax.Ref{}, err
		}
	}
	if err := rw.Close(); err != nil {
		return syntax.Ref{}, err
	}
	d := imageDict(w, h, 8)
	d.Set("ColorSpace", syntax.Name("DeviceGray"))
	d.Set("Filter", syntax.Name("FlateDecode"))
	d.Set("DecodeParms", filter.PredictorParms(1, 8, w))
	return b.w.AddStream(d, buf.Blocks()...), nil
}

func allOpaque(a []byte) bool {
	for _, v := range a {
		if v != 255 {
			return false
		}
	}
	return true
}

// rowSource reads an image decoded by image/png one row at a time as 8-bit
// colour samples (gray or RGB) and alpha, without converting the whole
// image at once: a second full-size copy is what a large photo cannot
// afford in a browser tab.
type rowSource struct {
	img        image.Image
	comps      int
	w, h       int
	cbuf, abuf []byte
}

func newRowSource(img image.Image, comps int) *rowSource {
	r := img.Bounds()
	return &rowSource{
		img: img, comps: comps, w: r.Dx(), h: r.Dy(),
		cbuf: make([]byte, r.Dx()*comps), abuf: make([]byte, r.Dx()),
	}
}

// row returns row y (counted from the top, from 0). The alpha row is nil
// for image types that cannot hold transparency. The returned slices are
// reused by the next call.
func (s *rowSource) row(y int) (c, a []byte) {
	r := s.img.Bounds()
	y += r.Min.Y
	c, a = s.cbuf, s.abuf
	switch m := s.img.(type) {
	case *image.Gray:
		off := m.PixOffset(r.Min.X, y)
		if s.comps == 1 {
			return m.Pix[off : off+s.w], nil
		}
		for x := range s.w {
			v := m.Pix[off+x]
			c[3*x], c[3*x+1], c[3*x+2] = v, v, v
		}
		return c, nil
	case *image.Gray16:
		off := m.PixOffset(r.Min.X, y)
		for x := range s.w {
			v := to8(uint16(m.Pix[off+2*x])<<8 | uint16(m.Pix[off+2*x+1]))
			s.put(x, v, v, v)
		}
		return c, nil
	case *image.RGBA:
		// Premultiplied, but image/png uses this type only for opaque
		// images, whose samples need no division.
		off := m.PixOffset(r.Min.X, y)
		for x := range s.w {
			p := m.Pix[off+4*x:]
			if p[3] == 0xFF {
				s.put(x, p[0], p[1], p[2])
				a[x] = 0xFF
			} else {
				s.convert(x, m.At(r.Min.X+x, y))
			}
		}
		return c, a
	case *image.RGBA64:
		off := m.PixOffset(r.Min.X, y)
		for x := range s.w {
			p := m.Pix[off+8*x:]
			if be16(p[6:]) == 0xFFFF {
				s.put(x, to8(be16(p)), to8(be16(p[2:])), to8(be16(p[4:])))
				a[x] = 0xFF
			} else {
				s.convert(x, m.At(r.Min.X+x, y))
			}
		}
		return c, a
	case *image.NRGBA:
		off := m.PixOffset(r.Min.X, y)
		for x := range s.w {
			p := m.Pix[off+4*x:]
			s.put(x, p[0], p[1], p[2])
			a[x] = p[3]
		}
		return c, a
	case *image.NRGBA64:
		off := m.PixOffset(r.Min.X, y)
		for x := range s.w {
			p := m.Pix[off+8*x:]
			s.put(x, to8(be16(p)), to8(be16(p[2:])), to8(be16(p[4:])))
			a[x] = to8(be16(p[6:]))
		}
		return c, a
	}
	// Any other type goes through the colour model, pixel by pixel.
	for x := range s.w {
		s.convert(x, s.img.At(r.Min.X+x, y))
	}
	return c, a
}

// convert stores one pixel of any colour type, removing premultiplication
// exactly, as the colour model does.
func (s *rowSource) convert(x int, c color.Color) {
	v := color.NRGBA64Model.Convert(c).(color.NRGBA64)
	s.put(x, to8(v.R), to8(v.G), to8(v.B))
	s.abuf[x] = to8(v.A)
}

// put stores one pixel's colour, keeping the red sample as the gray level
// for a gray image, whose three samples are equal.
func (s *rowSource) put(x int, r, g, b byte) {
	if s.comps == 1 {
		s.cbuf[x] = r
		return
	}
	s.cbuf[3*x], s.cbuf[3*x+1], s.cbuf[3*x+2] = r, g, b
}

func be16(p []byte) uint16 { return uint16(p[0])<<8 | uint16(p[1]) }

// to8 reduces a 16-bit sample to 8 bits with rounding.
func to8(v uint16) byte { return byte((uint32(v)*255 + 32767) / 65535) }

// pagePlan is where an image goes on its page.
type pagePlan struct {
	w, h         float64 // the page
	x, y, dw, dh float64 // the image as displayed, after orientation
	clip         bool    // Cover: clip to the area inside the margins
	bx, by       float64 // that area
	bw, bh       float64
	limited      bool // the image was scaled down to fit Acrobat's page limit
}

// resolution returns the resolution to size an image by: the declared one
// when it is plausible, 72 dpi otherwise. Files declaring 1 dpi, or a
// million, exist, and taking them at their word makes absurd pages.
func resolution(x, y float64) (float64, float64) {
	ok := func(v float64) bool { return v >= 10 && v <= 10000 }
	if ok(x) && ok(y) {
		return x, y
	}
	return 72, 72
}

func planPage(img *embedded, o *ImageOptions) (pagePlan, error) {
	pw, ph := float64(img.width), float64(img.height)
	dx, dy := resolution(img.dpiX, img.dpiY)
	if img.orientation >= 5 { // a quarter turn swaps the sides
		pw, ph = ph, pw
		dx, dy = dy, dx
	}
	natW, natH := pw*72/dx, ph*72/dy
	m := o.Margin
	var p pagePlan
	if o.PageSize == (Size{}) {
		room := maxPage - 2*m
		if room <= 0 {
			return p, &OptionError{"Margin", "too large for a page"}
		}
		if natW > room || natH > room {
			k := min(room/natW, room/natH)
			natW, natH = natW*k, natH*k
			p.limited = true
		}
		p.w, p.h = max(natW+2*m, minPage), max(natH+2*m, minPage)
		p.dw, p.dh = natW, natH
		p.x, p.y = (p.w-natW)/2, (p.h-natH)/2
		return p, nil
	}
	w, h := o.PageSize.Width, o.PageSize.Height
	switch o.Orientation {
	case Portrait:
		if w > h {
			w, h = h, w
		}
	case Landscape:
		if h > w {
			w, h = h, w
		}
	case Auto:
		if natW != natH && w != h && (natW > natH) != (w > h) {
			w, h = h, w
		}
	}
	bw, bh := w-2*m, h-2*m
	if bw <= 0 || bh <= 0 {
		return p, &OptionError{"Margin", "leaves no room on the page"}
	}
	s := min(bw/natW, bh/natH)
	if o.Fit == Cover {
		s = max(bw/natW, bh/natH)
		p.clip = true
	}
	p.w, p.h = w, h
	p.bx, p.by, p.bw, p.bh = m, m, bw, bh
	p.dw, p.dh = natW*s, natH*s
	p.x, p.y = m+(bw-p.dw)/2, m+(bh-p.dh)/2
	return p, nil
}

// content returns the page's content stream: draw image Im0 into the planned
// rectangle, turned and mirrored as its EXIF orientation says.
func (p pagePlan) content(orientation int) []byte {
	b := []byte("q\n")
	if p.clip {
		b = appendNums(b, p.bx, p.by, p.bw, p.bh)
		b = append(b, " re W n\n"...)
	}
	b = appendNums(b, imageMatrix(orientation, p.x, p.y, p.dw, p.dh)...)
	return append(b, " cm\n/Im0 Do\nQ\n"...)
}

// imageMatrix maps the image's unit square, whose top row is the first row
// of stored pixels, onto the rectangle (x, y, w, h) so the image appears as
// EXIF orientation o says it should be displayed.
func imageMatrix(o int, x, y, w, h float64) []float64 {
	switch o {
	case 2: // mirrored left to right
		return []float64{-w, 0, 0, h, x + w, y}
	case 3: // turned 180°
		return []float64{-w, 0, 0, -h, x + w, y + h}
	case 4: // mirrored top to bottom
		return []float64{w, 0, 0, -h, x, y + h}
	case 5: // mirrored along the main diagonal
		return []float64{0, -h, -w, 0, x + w, y + h}
	case 6: // turned 90° clockwise
		return []float64{0, -h, w, 0, x, y + h}
	case 7: // mirrored along the other diagonal
		return []float64{0, h, w, 0, x, y}
	case 8: // turned 90° anticlockwise
		return []float64{0, h, -w, 0, x + w, y}
	}
	return []float64{w, 0, 0, h, x, y}
}

func appendNums(b []byte, nums ...float64) []byte {
	for i, v := range nums {
		if i > 0 {
			b = append(b, ' ')
		}
		b = syntax.AppendReal(b, v)
	}
	return b
}

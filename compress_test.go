package pdfo

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/wisebrian/pdfo/internal/imgfmt"
	"github.com/wisebrian/pdfo/internal/pdftest"
	"github.com/wisebrian/pdfo/internal/syntax"
)

// photo is a photograph-like test image: smooth gradients with a little
// noise, which JPEG compresses as it does real photos.
func photo(w, h int, seed uint64) *image.NRGBA {
	rng := rand.New(rand.NewPCG(seed, 99))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			n := rng.IntN(9) - 4
			c := func(v float64) uint8 { return uint8(max(0, min(255, int(v)+n))) }
			img.SetNRGBA(x, y, color.NRGBA{
				c(200 * (0.5 + 0.5*math.Sin(6*fx+2*fy))), c(180 * fy), c(220 * (1 - fx*fy)), 255,
			})
		}
	}
	return img
}

func compressed(t *testing.T, src []byte, opts *CompressOptions) (*Output, []byte, *Document) {
	t.Helper()
	out, err := Compress(openDoc(t, src, ""), opts)
	pdf, got := finish(t, out, err)
	if out.Report.Compression == nil || out.Report.Compression.SizeAfter != int64(len(pdf)) ||
		out.Report.Compression.SizeBefore != int64(len(src)) {
		t.Fatalf("report %+v", out.Report.Compression)
	}
	return out, pdf, got
}

func TestCompressDownsamplesPhotos(t *testing.T) {
	jpg := pdftest.JPEG(photo(2000, 1500, 1))
	src, err := FromImages([][]byte{jpg}, &ImageOptions{PageSize: A4})
	if err != nil {
		t.Fatal(err)
	}
	in := src.Bytes()
	out, pdf, _ := compressed(t, in, &Screen)
	rep := out.Report.Compression
	if len(rep.Images) != 1 {
		t.Fatalf("images %+v", rep.Images)
	}
	im := rep.Images[0]
	// On landscape A4 the 4:3 photo is limited by the height: drawn 794 pt
	// (11.0 in) wide, its 2000 pixels make 181 dpi.
	if im.Action != "downsampled" || im.Page != 1 || im.Width != 2000 || math.Abs(im.DPI-181) > 1 {
		t.Errorf("image %+v", im)
	}
	if rep.SizeAfter >= rep.SizeBefore/2 {
		t.Errorf("%d bytes became %d", rep.SizeBefore, rep.SizeAfter)
	}
	// Downsampled to 100 dpi: 2000 × 100/181.4 ≈ 1102 pixels.
	if !bytes.Contains(pdftest.QPDFJSON(t, pdf), []byte(`"/Width": 1102`)) {
		t.Error("not downsampled to 100 dpi")
	}
	// The page looks the same, give or take JPEG.
	a, b := pdftest.Render(t, in, 1, 36), pdftest.Render(t, pdf, 1, 36)
	if d := diff(a, b); d > 0.02 {
		t.Errorf("render differs in %.1f%% of pixels", 100*d)
	}
	// Print keeps it: 171 dpi is below 300 × 1.5, and a quality-85 re-encode
	// of a quality-95 JPEG is smaller, so it is only re-encoded.
	out, _, _ = compressed(t, in, &Print)
	if im := out.Report.Compression.Images[0]; im.Action != "re-encoded" {
		t.Errorf("print preset: %+v", im)
	}
	// With the original quality there is nothing to gain.
	out, _, _ = compressed(t, in, &CompressOptions{Quality: 100})
	if im := out.Report.Compression.Images[0]; im.Action != "unchanged" || im.Reason != "not smaller" {
		t.Errorf("quality 100: %+v", im)
	}
}

func TestCompressSoftMask(t *testing.T) {
	img := photo(1200, 900, 2)
	for y := range 900 {
		for x := range 1200 {
			c := img.NRGBAAt(x, y)
			c.A = uint8(255 * x / 1200)
			img.SetNRGBA(x, y, c)
		}
	}
	b := img.Bounds()
	rows := make([][]byte, b.Dy())
	for y := range rows {
		for x := range b.Dx() {
			c := img.NRGBAAt(x, y)
			rows[y] = append(rows[y], c.R, c.G, c.B, c.A)
		}
	}
	png := pdftest.EncodePNG(pdftest.PNG{Width: 1200, Height: 900, ColorType: imgfmt.PNGRGBA, BitDepth: 8, Rows: rows})
	src, err := FromImages([][]byte{png}, &ImageOptions{PageSize: A5})
	if err != nil {
		t.Fatal(err)
	}
	in := src.Bytes()
	out, pdf, got := compressed(t, in, &Screen)
	if im := out.Report.Compression.Images[0]; im.Action != "downsampled" {
		t.Fatalf("image %+v", im)
	}
	// The soft mask was scaled with its image.
	r := got.r
	res, _ := r.Resolve(got.pages[0].Resources).(*syntax.Dict)
	xo, _ := r.Resolve(res.Get("XObject")).(*syntax.Dict)
	_, im := xo.At(0)
	ist, _ := r.Resolve(im).(*syntax.Stream)
	mst, _ := r.Resolve(ist.Dict.Get("SMask")).(*syntax.Stream)
	if mst == nil || mst.Dict.Get("Width") != ist.Dict.Get("Width") || mst.Dict.Get("Height") != ist.Dict.Get("Height") {
		t.Errorf("mask %v, image %v", mst.Dict, ist.Dict)
	}
	a, b2 := pdftest.Render(t, in, 1, 36), pdftest.Render(t, pdf, 1, 36)
	if d := diff(a, b2); d > 0.03 {
		t.Errorf("render differs in %.1f%% of pixels", 100*d)
	}
}

func TestCompressSkips(t *testing.T) {
	src, err := FromImages([][]byte{testImage(t, "cmyk-gs.jpg"), pngCases()[len(pngCases())-4].encode()}, &ImageOptions{PageSize: A4})
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ := compressed(t, src.Bytes(), &Screen)
	reasons := map[string]bool{}
	for _, im := range out.Report.Compression.Images {
		reasons[im.Reason] = true
	}
	if !reasons["CMYK"] || !reasons["indexed colours"] {
		t.Errorf("images %+v", out.Report.Compression.Images)
	}
}

func TestCompressCorpus(t *testing.T) {
	for _, name := range []string{"basic.pdf", "lo-forms.pdf", "chrome.pdf", "basic-table.pdf"} {
		t.Run(name, func(t *testing.T) {
			src := corpusPDF(t, name)
			out, pdf, got := compressed(t, src, &Ebook)
			if out.Report.Compression.SizeAfter > out.Report.Compression.SizeBefore {
				t.Errorf("grew from %d to %d", out.Report.Compression.SizeBefore, out.Report.Compression.SizeAfter)
			}
			for p := 1; p <= got.NumPages(); p++ {
				sameRender(t, src, p, pdf, p)
			}
			if len(out.Report.Warnings) != 0 {
				t.Errorf("warnings %+v", out.Report.Warnings)
			}
			if again, _ := Compress(openDoc(t, src, ""), &Ebook); !bytes.Equal(again.Bytes(), pdf) {
				t.Error("not deterministic")
			}
		})
	}
}

func TestCompressDeduplicates(t *testing.T) {
	basic := corpusPDF(t, "basic.pdf")
	merged, err := Merge([]*Document{openDoc(t, basic, ""), openDoc(t, basic, "")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := merged.Bytes()
	out, pdf, got := compressed(t, in, &Ebook)
	if out.Report.Compression.SizeAfter >= out.Report.Compression.SizeBefore {
		t.Errorf("%d bytes became %d", out.Report.Compression.SizeBefore, out.Report.Compression.SizeAfter)
	}
	// The two copies' page contents are identical: stored once.
	for p := range 3 {
		if a, b := got.pages[p].Dict.Get("Contents"), got.pages[p+3].Dict.Get("Contents"); a != b {
			t.Errorf("page %d content %v, its copy's %v", p+1, a, b)
		}
	}
	for p := 1; p <= 6; p++ {
		sameRender(t, in, p, pdf, p)
	}
}

func TestCompressFollowsForms(t *testing.T) {
	// The page draws a form at half size; the form draws the image over
	// 400×300 of its space: the image appears 200×150 pt.
	jpg := pdftest.JPEG(photo(1000, 750, 3))
	j, err := imgfmt.ParseJPEG(jpg)
	if err != nil {
		t.Fatal(err)
	}
	_ = j
	doc := pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		pdftest.Page("/MediaBox[0 0 300 300]/Contents 4 0 R/Resources<</XObject<</F 5 0 R>>>>"),
		pdftest.Stream("", []byte("q 0.5 0 0 0.5 10 10 cm /F Do Q"), false),
		pdftest.Stream("/Type/XObject/Subtype/Form/BBox[0 0 400 300]/Resources<</XObject<</Im 6 0 R>>>>",
			[]byte("q 400 0 0 300 0 0 cm /Im Do Q"), false),
		"<</Type/XObject/Subtype/Image/Width 1000/Height 750/ColorSpace/DeviceRGB/BitsPerComponent 8/Filter/DCTDecode/Length " +
			itoa(len(jpg)) + ">>stream\n" + string(jpg) + "\nendstream",
	}}.Bytes()
	out, _, _ := compressed(t, doc, &Screen)
	im := out.Report.Compression.Images[0]
	// 1000 pixels over 200 pt (2.78 in) is 360 dpi.
	if math.Abs(im.DPI-360) > 1 || im.Action != "downsampled" {
		t.Errorf("image %+v", im)
	}
}

func itoa(i int) string { return string(syntax.Append(nil, syntax.Int(i))) }

func TestCompressOptions(t *testing.T) {
	d := openDoc(t, corpusPDF(t, "basic.pdf"), "")
	for _, o := range []CompressOptions{{DPI: -1, Quality: 50}, {DPI: 100, Quality: 0}, {DPI: 100, Quality: 101}} {
		var oe *OptionError
		if _, err := Compress(d, &o); !errors.As(err, &oe) {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

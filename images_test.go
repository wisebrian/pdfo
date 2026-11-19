package pdfo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wisebrian/pdfo/internal/imgfmt"
	"github.com/wisebrian/pdfo/internal/pdftest"
)

// build runs FromImages, checks what every output must satisfy, and returns
// the file.
func build(t *testing.T, images [][]byte, opts *ImageOptions) (*Output, []byte) {
	t.Helper()
	out, err := FromImages(images, opts)
	if err != nil {
		t.Fatal(err)
	}
	pdf := out.Bytes()
	if int64(len(pdf)) != out.Size() {
		t.Fatalf("Size() = %d, Bytes() has %d", out.Size(), len(pdf))
	}
	if again, _ := FromImages(images, opts); !bytes.Equal(again.Bytes(), pdf) {
		t.Fatal("output is not deterministic")
	}
	pdftest.Check(t, t.Name(), pdf)
	return out, pdf
}

func testImage(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "images", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// near reports whether two colours differ by at most tol in every channel.
func near(a, b color.NRGBA, tol int) bool {
	d := func(x, y uint8) bool { return int(x)-int(y) <= tol && int(y)-int(x) <= tol }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B)
}

// checkQuadrants renders page 1 and checks the centre of each quadrant.
func checkQuadrants(t *testing.T, pdf []byte, dpi int, want [4]color.NRGBA, tol int) {
	t.Helper()
	img := pdftest.Render(t, pdf, 1, dpi)
	r := img.Bounds()
	for q, c := range want {
		x := r.Min.X + r.Dx()/4 + (q%2)*r.Dx()/2
		y := r.Min.Y + r.Dy()/4 + (q/2)*r.Dy()/2
		if got := img.NRGBAAt(x, y); !near(got, c, tol) {
			t.Errorf("quadrant %d at (%d,%d): got %v, want %v", q, x, y, got, c)
		}
	}
}

func TestFromImagesEXIFOrientation(t *testing.T) {
	for o := 1; o <= 8; o++ {
		t.Run(strconv.Itoa(o), func(t *testing.T) {
			stored := pdftest.QuadrantImage(64, 48, o)
			jpg := pdftest.Splice(pdftest.JPEG(stored), pdftest.EXIF(o, o%2 == 0, 0))
			_, pdf := build(t, [][]byte{jpg}, &ImageOptions{Write: WriteOptions{XRefTable: true}})
			if !bytes.Contains(pdf, []byte("/MediaBox[0 0 64 48]")) {
				t.Errorf("page is not the displayed size 64×48")
			}
			checkQuadrants(t, pdf, 72, pdftest.Quadrants, 12)
		})
	}
}

func TestFromImagesPNGOrientation(t *testing.T) {
	stored := pdftest.QuadrantImage(32, 24, 6)
	b := stored.Bounds()
	rows := make([][]byte, b.Dy())
	for y := range rows {
		for x := range b.Dx() {
			c := stored.NRGBAAt(x, y)
			rows[y] = append(rows[y], c.R, c.G, c.B)
		}
	}
	exif := pdftest.EXIF(6, false, 0)
	png := pdftest.EncodePNG(pdftest.PNG{
		Width: b.Dx(), Height: b.Dy(), ColorType: imgfmt.PNGRGB, BitDepth: 8, Rows: rows,
		Chunks: [][2][]byte{{[]byte("eXIf"), exif[10:]}},
	})
	_, pdf := build(t, [][]byte{png}, nil)
	checkQuadrants(t, pdf, 144, pdftest.Quadrants, 0)
}

// pngCase is a PNG whose four quadrants have known samples, and the colours
// they must render as on a white page.
type pngCase struct {
	name      string
	colorType int
	depth     int
	samples   [4][]uint16 // per quadrant, in the PNG's sample order
	palette   []byte
	trns      []byte
	want      [4]color.NRGBA
}

func gray(v uint8) color.NRGBA { return color.NRGBA{v, v, v, 255} }

// over composites c at alpha a (0–255) over white, as a viewer does.
func over(c color.NRGBA, a uint8) color.NRGBA {
	f := func(v uint8) uint8 {
		return uint8(math.Round(float64(v)*float64(a)/255 + 255*(1-float64(a)/255)))
	}
	return color.NRGBA{f(c.R), f(c.G), f(c.B), 255}
}

func pngCases() []pngCase {
	q := pdftest.Quadrants
	rgb := func(c color.NRGBA, scale uint16) []uint16 {
		return []uint16{uint16(c.R) * scale, uint16(c.G) * scale, uint16(c.B) * scale}
	}
	alpha := [4]uint8{255, 128, 0, 64}
	levels := [4]uint8{0, 85, 170, 255}
	var cases []pngCase
	for _, d := range []int{1, 2, 4, 8, 16} {
		c := pngCase{name: "gray" + strconv.Itoa(d), colorType: imgfmt.PNGGray, depth: d}
		for i := range 4 {
			if d == 1 {
				c.samples[i] = []uint16{uint16(i % 2)}
				c.want[i] = gray(uint8(255 * (i % 2)))
				continue
			}
			max := uint32(1<<d - 1)
			c.samples[i] = []uint16{uint16(uint32(i) * max / 3)}
			c.want[i] = gray(levels[i])
		}
		cases = append(cases, c)
	}
	for _, d := range []int{8, 16} {
		scale := uint16(1)
		if d == 16 {
			scale = 257
		}
		c := pngCase{name: "rgb" + strconv.Itoa(d), colorType: imgfmt.PNGRGB, depth: d}
		ca := pngCase{name: "rgba" + strconv.Itoa(d), colorType: imgfmt.PNGRGBA, depth: d}
		ga := pngCase{name: "grayalpha" + strconv.Itoa(d), colorType: imgfmt.PNGGrayAlpha, depth: d}
		for i := range 4 {
			c.samples[i] = rgb(q[i], scale)
			c.want[i] = q[i]
			ca.samples[i] = append(rgb(q[i], scale), uint16(alpha[i])*scale)
			ca.want[i] = over(q[i], alpha[i])
			ga.samples[i] = []uint16{uint16(levels[i]) * scale, uint16(alpha[i]) * scale}
			ga.want[i] = over(gray(levels[i]), alpha[i])
		}
		cases = append(cases, c, ca, ga)
	}
	var pal []byte
	for _, c := range q {
		pal = append(pal, c.R, c.G, c.B)
	}
	for _, d := range []int{1, 2, 4, 8} {
		c := pngCase{name: "palette" + strconv.Itoa(d), colorType: imgfmt.PNGPalette, depth: d, palette: pal}
		for i := range 4 {
			idx := i
			if d == 1 {
				idx = i % 2
				c.palette = pal[:6]
			}
			c.samples[i] = []uint16{uint16(idx)}
			c.want[i] = q[idx]
		}
		cases = append(cases, c)
	}
	pt := pngCase{name: "palette-alpha", colorType: imgfmt.PNGPalette, depth: 8, palette: pal, trns: alpha[:]}
	for i := range 4 {
		pt.samples[i] = []uint16{uint16(i)}
		pt.want[i] = over(q[i], alpha[i])
	}
	// A tRNS chunk makes one exact colour or gray level transparent.
	gt := pngCase{name: "gray-trns", colorType: imgfmt.PNGGray, depth: 8, trns: []byte{0, 85}}
	ct := pngCase{name: "rgb-trns", colorType: imgfmt.PNGRGB, depth: 8,
		trns: []byte{0, q[1].R, 0, q[1].G, 0, q[1].B}}
	for i := range 4 {
		gt.samples[i] = []uint16{uint16(levels[i])}
		gt.want[i] = gray(levels[i])
		ct.samples[i] = rgb(q[i], 1)
		ct.want[i] = q[i]
	}
	gt.want[1] = gray(255)
	ct.want[1] = gray(255)
	return append(cases, pt, gt, ct)
}

// encode builds the 16×16 PNG of a case.
func (c pngCase) encode() []byte {
	const w, h = 16, 16
	rows := make([][]byte, h)
	for y := range h {
		row := make([]byte, (w*len(c.samples[0])*c.depth+7)/8)
		bit := 0
		for x := range w {
			q := x/8 + 2*(y/8)
			for _, v := range c.samples[q] {
				switch c.depth {
				case 16:
					binary.BigEndian.PutUint16(row[bit/8:], v)
				case 8:
					row[bit/8] = byte(v)
				default:
					row[bit/8] |= byte(v) << (8 - c.depth - bit%8)
				}
				bit += c.depth
			}
		}
		rows[y] = row
	}
	p := pdftest.PNG{Width: w, Height: h, ColorType: c.colorType, BitDepth: c.depth, Rows: rows}
	if c.palette != nil {
		p.Chunks = append(p.Chunks, [2][]byte{[]byte("PLTE"), c.palette})
	}
	if c.trns != nil {
		p.Chunks = append(p.Chunks, [2][]byte{[]byte("tRNS"), c.trns})
	}
	return pdftest.EncodePNG(p)
}

func TestFromImagesPNG(t *testing.T) {
	for _, c := range pngCases() {
		t.Run(c.name, func(t *testing.T) {
			_, pdf := build(t, [][]byte{c.encode()}, nil)
			hasMask := bytes.Contains(pdf, []byte("/SMask"))
			wantMask := c.colorType == imgfmt.PNGRGBA || c.colorType == imgfmt.PNGGrayAlpha || c.trns != nil
			if hasMask != wantMask {
				t.Errorf("soft mask: %v, want %v", hasMask, wantMask)
			}
			if c.colorType == imgfmt.PNGPalette && !bytes.Contains(pdf, []byte("/Indexed")) {
				t.Error("palette image not kept as Indexed")
			}
			if c.colorType == imgfmt.PNGPalette && !bytes.Contains(pdf, []byte("/BitsPerComponent "+strconv.Itoa(c.depth))) {
				t.Errorf("palette image not kept at %d bits", c.depth)
			}
			checkQuadrants(t, pdf, 144, c.want, 3)
		})
	}
}

func TestFromImagesCMYK(t *testing.T) {
	// Both files store CMYK inverted, as Adobe does. Without the inverted
	// Decode array, red would render cyan and white black.
	for _, name := range []string{"cmyk-gs.jpg", "cmyk-sips.jpg"} {
		t.Run(name, func(t *testing.T) {
			_, pdf := build(t, [][]byte{testImage(t, name)}, nil)
			if !bytes.Contains(pdf, []byte("/Decode[1 0 1 0 1 0 1 0]")) {
				t.Error("no inverted Decode array")
			}
			img := pdftest.Render(t, pdf, 1, 72)
			r := img.Bounds()
			at := func(q int) color.NRGBA {
				return img.NRGBAAt(r.Dx()/4+(q%2)*r.Dx()/2, r.Dy()/4+(q/2)*r.Dy()/2)
			}
			dominant := func(c color.NRGBA, ch int) bool {
				v := [3]int{int(c.R), int(c.G), int(c.B)}
				for i := range 3 {
					if i != ch && v[ch] < v[i]+60 {
						return false
					}
				}
				return true
			}
			for q, ch := range []int{0, 1, 2} {
				if c := at(q); !dominant(c, ch) {
					t.Errorf("quadrant %d renders as %v", q, c)
				}
			}
			if c := at(3); c.R < 200 || c.G < 200 || c.B < 200 {
				t.Errorf("white quadrant renders as %v", c)
			}
		})
	}
}

func TestFromImagesICC(t *testing.T) {
	rgbProfile := pdftest.ICCProfile("RGB ")
	jpg := pdftest.Splice(pdftest.JPEG(pdftest.QuadrantImage(64, 48, 1)), pdftest.ICCSegments(rgbProfile, 1000)...)
	gray := pdftest.EncodePNG(pdftest.PNG{
		Width: 1, Height: 1, ColorType: imgfmt.PNGGray, BitDepth: 8, Rows: [][]byte{{128}},
		Chunks: [][2][]byte{{[]byte("iCCP"), pdftest.ZlibChunk("gray\x00\x00", pdftest.ICCProfile("GRAY"))}},
	})
	// The same profile on two images is stored once.
	_, pdf := build(t, [][]byte{jpg, jpg, gray}, &ImageOptions{Write: WriteOptions{XRefTable: true}})
	if got := bytes.Count(pdf, []byte("/ICCBased")); got != 3 {
		t.Errorf("%d ICCBased colour spaces, want 3", got)
	}
	if got := bytes.Count(pdf, []byte("/Alternate/DeviceRGB")); got != 1 {
		t.Errorf("%d RGB profile streams, want 1", got)
	}
	if got := bytes.Count(pdf, []byte("/Alternate/DeviceGray")); got != 1 {
		t.Errorf("%d gray profile streams, want 1", got)
	}
	checkQuadrants(t, pdf, 72, pdftest.Quadrants, 14)
}

func TestFromImagesIgnoresBadICC(t *testing.T) {
	bad := pdftest.ICCProfile("GRAY") // a gray profile on a colour image
	jpg := pdftest.Splice(pdftest.JPEG(pdftest.QuadrantImage(64, 48, 1)), pdftest.ICCSegments(bad, 1000)...)
	out, pdf := build(t, [][]byte{jpg}, nil)
	if bytes.Contains(pdf, []byte("ICCBased")) {
		t.Error("unusable profile embedded")
	}
	w := out.Report.Warnings
	if len(w) != 1 || w[0].Code != "icc-profile-ignored" || len(w[0].Pages) != 1 || w[0].Pages[0] != 1 {
		t.Errorf("warnings: %+v", w)
	}
}

func TestFromImagesRemovesMetadata(t *testing.T) {
	gps := pdftest.Segment(0xE1, []byte("Exif\x00\x00II*\x00GPS 44.43N 26.10E"))
	jpg := pdftest.Splice(pdftest.JPEG(pdftest.QuadrantImage(64, 48, 1)), gps,
		pdftest.Segment(0xFE, []byte("secret comment")))
	jpg = append(jpg, "motion photo video"...)
	_, pdf := build(t, [][]byte{jpg}, &ImageOptions{Write: WriteOptions{XRefTable: true}})
	for _, leak := range []string{"Exif", "GPS", "secret comment", "motion photo"} {
		if bytes.Contains(pdf, []byte(leak)) {
			t.Errorf("%q leaked into the PDF", leak)
		}
	}
}

func TestFromImagesProgressiveAndRGB(t *testing.T) {
	_, pdf := build(t, [][]byte{testImage(t, "progressive.jpg"), testImage(t, "rgb.jpg"), testImage(t, "gray-cjpeg.jpg")},
		&ImageOptions{Write: WriteOptions{XRefTable: true}})
	if !bytes.Contains(pdf, []byte("/DecodeParms<</ColorTransform 0>>")) {
		t.Error("RGB JPEG lacks ColorTransform 0")
	}
	if got := bytes.Count(pdf, []byte("ColorTransform")); got != 1 {
		t.Errorf("ColorTransform written %d times, want once", got)
	}
	// rgb.jpg's quadrants must render in their colours, not as if they
	// were YCbCr.
	img := pdftest.Render(t, pdf, 2, 72)
	if c := img.NRGBAAt(16, 12); !near(c, pdftest.Quadrants[0], 20) {
		t.Errorf("RGB JPEG red quadrant renders as %v", c)
	}
}

func TestFromImagesInfo(t *testing.T) {
	jpg := testImage(t, "progressive.jpg")
	_, pdf := build(t, [][]byte{jpg}, &ImageOptions{Write: WriteOptions{
		XRefTable: true, Producer: "pdfo test", Timestamp: "2026-11-19T21:05:00+02:00",
	}})
	if !bytes.Contains(pdf, []byte("<</Producer(pdfo test)/CreationDate(D:20261119210500+02'00)>>")) {
		t.Errorf("information dictionary missing or wrong")
	}
	_, pdf = build(t, [][]byte{jpg}, &ImageOptions{Write: WriteOptions{XRefTable: true}})
	if bytes.Contains(pdf, []byte("/Info")) {
		t.Error("information dictionary written without anything in it")
	}
}

func TestPlanPage(t *testing.T) {
	photo := &embedded{width: 4000, height: 3000, orientation: 1}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	tests := []struct {
		name               string
		img                *embedded
		opts               ImageOptions
		w, h, x, y, dw, dh float64
		clip               bool
	}{
		{"fit at 72 dpi", photo, ImageOptions{}, 4000, 3000, 0, 0, 4000, 3000, false},
		{"fit at 300 dpi", &embedded{width: 2480, height: 3508, dpiX: 300, dpiY: 300, orientation: 1},
			ImageOptions{}, 595.2, 841.92, 0, 0, 595.2, 841.92, false},
		{"fit with margin", photo, ImageOptions{Margin: 10}, 4020, 3020, 10, 10, 4000, 3000, false},
		{"fit turned", &embedded{width: 3000, height: 4000, orientation: 6}, ImageOptions{}, 4000, 3000, 0, 0, 4000, 3000, false},
		{"fit implausible dpi", &embedded{width: 100, height: 50, dpiX: 1, dpiY: 1, orientation: 1}, ImageOptions{}, 100, 50, 0, 0, 100, 50, false},
		// A4 landscape is wider than 4:3, so the height limits the photo.
		{"A4 auto turns landscape", photo, ImageOptions{PageSize: A4}, A4.Height, A4.Width,
			(A4.Height - A4.Width/0.75) / 2, 0, A4.Width / 0.75, A4.Width, false},
		{"A4 portrait", photo, ImageOptions{PageSize: A4, Orientation: Portrait}, A4.Width, A4.Height,
			0, (A4.Height - A4.Width*0.75) / 2, A4.Width, A4.Width * 0.75, false},
		{"A4 portrait cover", photo, ImageOptions{PageSize: A4, Orientation: Portrait, Fit: Cover}, A4.Width, A4.Height,
			(A4.Width - A4.Height/0.75) / 2, 0, A4.Height / 0.75, A4.Height, true},
		{"letter landscape margin", &embedded{width: 100, height: 100, orientation: 1},
			ImageOptions{PageSize: Letter, Orientation: Landscape, Margin: 36}, 792, 612, 36 + (720-540)/2.0, 36, 540, 540, false},
	}
	for _, tt := range tests {
		p, err := planPage(tt.img, &tt.opts)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		got := []float64{p.w, p.h, p.x, p.y, p.dw, p.dh}
		want := []float64{tt.w, tt.h, tt.x, tt.y, tt.dw, tt.dh}
		for i := range got {
			if !near(got[i], want[i]) {
				t.Errorf("%s: got page %.3f×%.3f image (%.3f,%.3f) %.3f×%.3f, want %.3f×%.3f (%.3f,%.3f) %.3f×%.3f",
					tt.name, p.w, p.h, p.x, p.y, p.dw, p.dh, tt.w, tt.h, tt.x, tt.y, tt.dw, tt.dh)
				break
			}
		}
		if p.clip != tt.clip {
			t.Errorf("%s: clip = %v", tt.name, p.clip)
		}
	}
}

func TestPlanPageLimits(t *testing.T) {
	huge, err := planPage(&embedded{width: 30000, height: 15000, orientation: 1}, &ImageOptions{})
	if err != nil || huge.w != maxPage || huge.h != maxPage/2 || !huge.limited {
		t.Errorf("huge image: %+v, %v", huge, err)
	}
	tiny, err := planPage(&embedded{width: 1, height: 1, dpiX: 300, dpiY: 300, orientation: 1}, &ImageOptions{})
	if err != nil || tiny.w != minPage || tiny.h != minPage || tiny.dw != 0.24 {
		t.Errorf("tiny image: %+v, %v", tiny, err)
	}
	if _, err := planPage(&embedded{width: 10, height: 10, orientation: 1}, &ImageOptions{PageSize: A4, Margin: 300}); err == nil {
		t.Error("a margin wider than the page accepted")
	}
}

func TestFromImagesCoverClips(t *testing.T) {
	// A wide image covering a square page with a margin must not paint
	// over the margin.
	img := image.NewNRGBA(image.Rect(0, 0, 200, 50))
	for y := range 50 {
		for x := range 200 {
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
		}
	}
	png := pngOf(img)
	_, pdf := build(t, [][]byte{png}, &ImageOptions{PageSize: Size{100, 100}, Margin: 10, Fit: Cover})
	r := pdftest.Render(t, pdf, 1, 72)
	if c := r.NRGBAAt(5, 50); c != (color.NRGBA{255, 255, 255, 255}) {
		t.Errorf("margin painted: %v", c)
	}
	if c := r.NRGBAAt(50, 50); c.R > 10 {
		t.Errorf("image missing in the box: %v", c)
	}
}

func pngOf(img *image.NRGBA) []byte {
	b := img.Bounds()
	rows := make([][]byte, b.Dy())
	for y := range rows {
		for x := range b.Dx() {
			c := img.NRGBAAt(x, y)
			rows[y] = append(rows[y], c.R, c.G, c.B)
		}
	}
	return pdftest.EncodePNG(pdftest.PNG{Width: b.Dx(), Height: b.Dy(), ColorType: imgfmt.PNGRGB, BitDepth: 8, Rows: rows})
}

func TestFromImagesErrors(t *testing.T) {
	jpg := testImage(t, "progressive.jpg")
	bigPNG := pdftest.EncodePNG(pdftest.PNG{Width: 3000, Height: 3000, ColorType: imgfmt.PNGGray, BitDepth: 8,
		Rows: make([][]byte, 3000)})
	tests := []struct {
		name   string
		images [][]byte
		opts   *ImageOptions
		check  func(error) bool
	}{
		{"no images", nil, nil, isOption("images")},
		{"empty", [][]byte{jpg, {}}, nil, isImage(1, false)},
		{"gif", [][]byte{[]byte("GIF89a....")}, nil, isImage(0, true)},
		{"heic", [][]byte{[]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00")}, nil, isImage(0, true)},
		{"pdf", [][]byte{[]byte("%PDF-1.7\n")}, nil, isImage(0, false)},
		{"arithmetic", [][]byte{testImage(t, "arithmetic.jpg")}, nil, isImage(0, true)},
		{"damaged jpeg", [][]byte{jpg[:20]}, nil, isImage(0, false)},
		{"damaged png", [][]byte{pdftest.EncodePNG(pdftest.PNG{Width: 4, Height: 4, ColorType: 0, BitDepth: 8})}, nil, isImage(0, false)},
		{"pixel limit", [][]byte{bigPNG}, &ImageOptions{Limits: Limits{MaxImagePixels: 1 << 20}}, func(err error) bool {
			var le *LimitError
			return errors.As(err, &le)
		}},
		{"negative margin", [][]byte{jpg}, &ImageOptions{Margin: -1}, isOption("Margin")},
		{"NaN margin", [][]byte{jpg}, &ImageOptions{Margin: math.NaN()}, isOption("Margin")},
		{"page too small", [][]byte{jpg}, &ImageOptions{PageSize: Size{1, 100}}, isOption("PageSize")},
		{"page too large", [][]byte{jpg}, &ImageOptions{PageSize: Size{100, 20000}}, isOption("PageSize")},
		{"orientation", [][]byte{jpg}, &ImageOptions{Orientation: 7}, isOption("Orientation")},
		{"fit", [][]byte{jpg}, &ImageOptions{Fit: 2}, isOption("Fit")},
		{"timestamp", [][]byte{jpg}, &ImageOptions{Write: WriteOptions{Timestamp: "yesterday"}}, isOption("Timestamp")},
		{"limits", [][]byte{jpg}, &ImageOptions{Limits: Limits{MaxImagePixels: -1}}, isOption("Limits")},
	}
	for _, tt := range tests {
		_, err := FromImages(tt.images, tt.opts)
		if err == nil || !tt.check(err) {
			t.Errorf("%s: got %v (%T)", tt.name, err, err)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "pdfo: ") {
			t.Errorf("%s: message %q lacks the package prefix", tt.name, err)
		}
	}
}

func isOption(name string) func(error) bool {
	return func(err error) bool {
		var oe *OptionError
		return errors.As(err, &oe) && oe.Option == name
	}
}

func isImage(index int, unsupported bool) func(error) bool {
	return func(err error) bool {
		var ie *ImageError
		if !errors.As(err, &ie) || ie.Index != index {
			return false
		}
		if unsupported {
			return errors.Is(err, errors.ErrUnsupported)
		}
		var fe *FormatError
		return errors.As(err, &fe)
	}
}

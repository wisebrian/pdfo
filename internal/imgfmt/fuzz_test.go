package imgfmt

import (
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
)

func FuzzParseJPEG(f *testing.F) {
	pdftest.Seeds(f, "../..", "images/*.jpg")
	base := pdftest.JPEG(pdftest.QuadrantImage(16, 8, 1))
	f.Add(base)
	f.Add(pdftest.Splice(base, append([][]byte{pdftest.EXIF(6, true, 300)},
		pdftest.ICCSegments(pdftest.ICCProfile("RGB "), 200)...)...))
	f.Add(base[:len(base)/2])
	f.Fuzz(func(t *testing.T, data []byte) {
		pdftest.Bounded(t, len(data), func() {
			j, err := ParseJPEG(data)
			if err != nil {
				return
			}
			if j.Width <= 0 || j.Height <= 0 || j.Orientation < 1 || j.Orientation > 8 {
				t.Fatalf("accepted with %+v", j)
			}
			if n := len(j.Head) + len(j.Body) + len(j.Tail); n > len(data)+2 {
				t.Fatalf("cleaned file has %d bytes, more than the %d of the input", n, len(data))
			}
			if j.ICC != nil {
				if _, problem := CheckICC(j.ICC, j.Components); problem != "" {
					t.Fatalf("returned a profile that fails its own check: %s", problem)
				}
			}
		})
	})
}

func FuzzParsePNG(f *testing.F) {
	f.Add(pdftest.EncodePNG(pdftest.PNG{
		Width: 2, Height: 1, ColorType: PNGRGB, BitDepth: 8, Rows: [][]byte{{1, 2, 3, 4, 5, 6}},
		Chunks: [][2][]byte{
			{[]byte("pHYs"), {0, 0, 0x2e, 0x23, 0, 0, 0x2e, 0x23, 1}},
			{[]byte("iCCP"), pdftest.ZlibChunk("p\x00\x00", pdftest.ICCProfile("RGB "))},
			{[]byte("eXIf"), pdftest.EXIF(3, true, 0)[10:]},
		},
	}))
	f.Add(pdftest.EncodePNG(pdftest.PNG{Width: 1, Height: 1, ColorType: PNGPalette, BitDepth: 1, Rows: [][]byte{{0}},
		Chunks: [][2][]byte{{[]byte("PLTE"), {0, 0, 0}}, {[]byte("tRNS"), {0}}}}))
	f.Fuzz(func(t *testing.T, data []byte) {
		pdftest.Bounded(t, len(data), func() {
			p, err := ParsePNG(data)
			if err != nil {
				return
			}
			if p.Width <= 0 || p.Height <= 0 {
				t.Fatalf("accepted with %+v", p)
			}
			if p.ICC != nil {
				if _, problem := CheckICC(p.ICC, p.components()); problem != "" {
					t.Fatalf("returned a profile that fails its own check: %s", problem)
				}
			}
		})
	})
}

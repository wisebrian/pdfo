package pdfo

import (
	"bytes"
	"math"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
)

func FuzzParseSize(f *testing.F) {
	for _, s := range []string{"A4", "210x297mm", "8,5 x 11 in", "612×792pt", "1e3x2mm", "-1x-1cm", "x", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if sz, err := ParseSize(s); err == nil {
			if !(sz.Width > 0 && sz.Height > 0) || math.IsInf(sz.Width, 0) || math.IsInf(sz.Height, 0) {
				t.Fatalf("ParseSize(%q) = %v", s, sz)
			}
			_ = SizeName(sz)
		}
		if l, err := ParseLength(s); err == nil && math.IsNaN(l) {
			t.Fatalf("ParseLength(%q) = NaN", s)
		}
	})
}

// FuzzFromImages feeds arbitrary bytes as an image, with small limits so
// that each input is cheap; the output, when there is one, must be a
// complete file.
func FuzzFromImages(f *testing.F) {
	pdftest.Seeds(f, ".", "images/*.jpg")
	for _, c := range pngCases() {
		f.Add(c.encode())
	}
	f.Add(pdftest.Splice(pdftest.JPEG(pdftest.QuadrantImage(16, 8, 6)), pdftest.EXIF(6, false, 300)))
	opts := []*ImageOptions{
		{Limits: Limits{MaxImagePixels: 1 << 16}},
		{PageSize: A4, Margin: 20, Fit: Cover, Limits: Limits{MaxImagePixels: 1 << 16},
			Write: WriteOptions{XRefTable: true}},
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, o := range opts {
			pdftest.Bounded(t, len(data), func() {
				out, err := FromImages([][]byte{data}, o)
				if err != nil {
					return
				}
				pdf := out.Bytes()
				if int64(len(pdf)) != out.Size() || !bytes.HasPrefix(pdf, []byte("%PDF-1.7")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
					t.Fatalf("incomplete output: %d bytes, Size %d", len(pdf), out.Size())
				}
			})
		}
	})
}

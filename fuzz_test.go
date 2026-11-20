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

// FuzzCompress compresses arbitrary files with small limits; whatever
// opens must compress without panicking, and the result must open again.
func FuzzCompress(f *testing.F) {
	f.Add(pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		pdftest.Page("/MediaBox[0 0 100 100]/Contents 4 0 R/Resources<</XObject<</I 5 0 R>>>>"),
		pdftest.Stream("", []byte("q 100 0 0 100 0 0 cm /I Do Q"), false),
		pdftest.Stream("/Type/XObject/Subtype/Image/Width 4/Height 4/ColorSpace/DeviceGray/BitsPerComponent 8", make([]byte, 16), true),
	}}.Bytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		pdftest.Bounded(t, len(data), func() {
			d, err := Open(data, &OpenOptions{Limits: Limits{MaxStreamSize: 1 << 20, MaxDecoded: 8 << 20, MaxImagePixels: 1 << 16}})
			if err != nil {
				return
			}
			out, err := Compress(d, &Screen)
			if err != nil {
				return
			}
			if _, err := Open(out.Bytes(), nil); err != nil {
				t.Fatalf("compressed file does not open: %v", err)
			}
		})
	})
}

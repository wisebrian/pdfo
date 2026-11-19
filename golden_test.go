package pdfo

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// TestGolden compares FromImages output byte for byte with files made
// earlier. Output is deterministic, so any difference is a change in
// behaviour: if it is intended, run go test -run TestGolden -update and
// review the new files. A Go release that changes compress/flate changes
// the PNG cases too; that is expected.
func TestGolden(t *testing.T) {
	jpg := func(name string) []byte { return testImage(t, name) }
	var rgba, palette []byte
	for _, c := range pngCases() {
		switch c.name {
		case "rgba8":
			rgba = c.encode()
		case "palette-alpha":
			palette = c.encode()
		}
	}
	cases := []struct {
		name   string
		images [][]byte
		opts   *ImageOptions
	}{
		{"jpeg-fit", [][]byte{jpg("progressive.jpg")}, nil},
		{"jpeg-exif6-a4-margin", [][]byte{pdftest.Splice(jpg("progressive.jpg"), pdftest.EXIF(6, false, 0))},
			&ImageOptions{PageSize: A4, Margin: MM(10)}},
		{"cmyk-letter-landscape-cover", [][]byte{jpg("cmyk-gs.jpg"), jpg("cmyk-sips.jpg")},
			&ImageOptions{PageSize: Letter, Orientation: Landscape, Fit: Cover}},
		{"icc-a5", [][]byte{pdftest.Splice(jpg("progressive.jpg"), pdftest.ICCSegments(pdftest.ICCProfile("RGB "), 400)...)},
			&ImageOptions{PageSize: A5, Orientation: Portrait}},
		{"mixed-xref-table", [][]byte{jpg("rgb.jpg"), jpg("gray-cjpeg.jpg"), rgba, palette},
			&ImageOptions{Margin: 12, Write: WriteOptions{
				XRefTable: true, Producer: "pdfo golden test", Timestamp: "2026-11-19T21:05:00+02:00",
			}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, pdf := build(t, c.images, c.opts)
			path := filepath.Join("testdata", "golden", c.name+".pdf")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, pdf, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if !bytes.Equal(pdf, want) {
				t.Errorf("output differs from %s (%d bytes, want %d); run with -update if the change is intended", path, len(pdf), len(want))
			}
		})
	}
}

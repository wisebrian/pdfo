package imgfmt

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
)

func TestParseJPEG(t *testing.T) {
	base := pdftest.JPEG(pdftest.QuadrantImage(40, 30, 1))
	j, err := ParseJPEG(base)
	if err != nil {
		t.Fatal(err)
	}
	if j.Width != 40 || j.Height != 30 || j.Components != 3 || j.Progressive || j.Adobe || j.ColorTransform != 1 {
		t.Errorf("baseline JPEG read as %+v", j)
	}
	if j.Orientation != 1 || j.DPIX != 0 || j.ICC != nil || j.Truncated {
		t.Errorf("metadata read where there is none: %+v", j)
	}
	if got := join(j); !bytes.Equal(got, base) {
		t.Errorf("a file without metadata changed: %d bytes became %d", len(base), len(got))
	}

	gray := pdftest.JPEG(image.NewGray(image.Rect(0, 0, 8, 8)))
	if j, err := ParseJPEG(gray); err != nil || j.Components != 1 {
		t.Errorf("gray JPEG: %v, %+v", err, j)
	}
}

func TestParseJPEGMetadata(t *testing.T) {
	base := pdftest.JPEG(pdftest.QuadrantImage(40, 30, 6))
	profile := pdftest.ICCProfile("RGB ")
	segs := [][]byte{
		pdftest.EXIF(6, true, 300),
		pdftest.Segment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>")),
		pdftest.Segment(0xFE, []byte("a comment")),
		pdftest.Segment(0xED, []byte("Photoshop 3.0\x00")),
	}
	segs = append(segs, pdftest.ICCSegments(profile, 300)...)
	in := append(pdftest.Splice(base, segs...), "trailing video data"...)
	j, err := ParseJPEG(in)
	if err != nil {
		t.Fatal(err)
	}
	if j.Orientation != 6 {
		t.Errorf("orientation %d, want 6", j.Orientation)
	}
	if !bytes.Equal(j.ICC, profile) {
		t.Errorf("profile not reassembled (%q)", j.ICCProblem)
	}
	if !bytes.Equal(join(j), base) {
		t.Error("cleaned file differs from the file without metadata")
	}
	for _, leak := range []string{"Exif", "xmpmeta", "a comment", "Photoshop", "ICC_PROFILE", "trailing"} {
		if bytes.Contains(join(j), []byte(leak)) {
			t.Errorf("%s left in the cleaned file", leak)
		}
	}
	// Go's encoder writes no JFIF segment, so the resolution comes from
	// EXIF.
	if j.DPIX != 300 || j.DPIY != 300 {
		t.Errorf("resolution %v×%v, want 300 from EXIF", j.DPIX, j.DPIY)
	}
}

func TestParseJPEGEXIFByteOrders(t *testing.T) {
	base := pdftest.JPEG(pdftest.QuadrantImage(16, 8, 1))
	for o := 1; o <= 8; o++ {
		for _, be := range []bool{false, true} {
			j, err := ParseJPEG(pdftest.Splice(base, pdftest.EXIF(o, be, 0)))
			if err != nil || j.Orientation != o {
				t.Errorf("orientation %d (big endian %v): got %v, %v", o, be, j, err)
			}
		}
	}
	// Out-of-range orientations mean "unknown", i.e. upright.
	j, _ := ParseJPEG(pdftest.Splice(base, pdftest.EXIF(9, false, 0)))
	if j.Orientation != 1 {
		t.Errorf("orientation 9 read as %d", j.Orientation)
	}
}

func TestParseJPEGBadICC(t *testing.T) {
	base := pdftest.JPEG(pdftest.QuadrantImage(16, 8, 1))
	profile := pdftest.ICCProfile("RGB ")
	segs := pdftest.ICCSegments(profile, 200)
	tests := []struct {
		name string
		segs [][]byte
	}{
		{"missing chunk", segs[1:]},
		{"duplicate chunk", append([][]byte{segs[0]}, segs...)},
		{"gray profile on a colour image", pdftest.ICCSegments(pdftest.ICCProfile("GRAY"), 200)},
		{"not a profile", pdftest.ICCSegments(bytes.Repeat([]byte("x"), 400), 200)},
	}
	for _, tt := range tests {
		j, err := ParseJPEG(pdftest.Splice(base, tt.segs...))
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if j.ICC != nil || j.ICCProblem == "" {
			t.Errorf("%s: profile accepted", tt.name)
		}
	}
}

func TestParseJPEGTruncated(t *testing.T) {
	base := pdftest.JPEG(pdftest.QuadrantImage(40, 30, 1))
	cut := base[:len(base)-40]
	j, err := ParseJPEG(cut)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Truncated || !bytes.HasSuffix(join(j), []byte{0xFF, 0xD9}) {
		t.Error("truncated file not completed with an end-of-image marker")
	}
	if _, err := jpeg.Decode(bytes.NewReader(join(j))); err != nil {
		t.Logf("image/jpeg cannot decode the truncated scan either: %v", err)
	}
}

func TestParseJPEGRejects(t *testing.T) {
	base := pdftest.JPEG(pdftest.QuadrantImage(16, 8, 1))
	sof := bytes.Index(base, []byte{0xFF, 0xC0})
	withSOF := func(marker byte, precision byte) []byte {
		b := append([]byte(nil), base...)
		b[sof+1] = marker
		b[sof+4] = precision
		return b
	}
	tests := []struct {
		name        string
		in          []byte
		unsupported bool
	}{
		{"empty", nil, false},
		{"not a JPEG", []byte("GIF89a"), false},
		{"only SOI", []byte{0xFF, 0xD8}, false},
		{"lossless", withSOF(0xC3, 8), true},
		{"arithmetic", withSOF(0xC9, 8), true},
		{"hierarchical", withSOF(0xC5, 8), true},
		{"12-bit", withSOF(0xC1, 12), true},
		{"segment past the end", base[:sof+6], false},
		{"EOI before the image", []byte{0xFF, 0xD8, 0xFF, 0xD9}, false},
		{"scan without frame", []byte{0xFF, 0xD8, 0xFF, 0xDA, 0, 2, 0xFF, 0xD9}, false},
	}
	for _, tt := range tests {
		_, err := ParseJPEG(tt.in)
		if err == nil {
			t.Errorf("%s: accepted", tt.name)
			continue
		}
		if got := errors.Is(err, errors.ErrUnsupported); got != tt.unsupported {
			t.Errorf("%s: unsupported = %v (%v)", tt.name, got, err)
		}
	}
}

// TestParseJPEGFiles reads the JPEGs made by other programs; see
// testdata/README.md for how each was made.
func TestParseJPEGFiles(t *testing.T) {
	tests := []struct {
		file           string
		components     int
		progressive    bool
		adobe          bool
		colorTransform int
		err            bool
	}{
		{"cmyk-gs.jpg", 4, false, true, 0, false},
		{"cmyk-sips.jpg", 4, false, true, 0, false},
		{"progressive.jpg", 3, true, false, 1, false},
		{"rgb.jpg", 3, false, true, 0, false}, // RGB samples, said by the Adobe marker
		{"gray-cjpeg.jpg", 1, false, false, 0, false},
		{"arithmetic.jpg", 0, false, false, 0, true},
		{"12bit.jpg", 0, false, false, 0, true},
		{"lossless.jpg", 0, false, false, 0, true},
	}
	for _, tt := range tests {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "images", tt.file))
		if err != nil {
			t.Fatal(err)
		}
		j, err := ParseJPEG(data)
		if tt.err {
			if !errors.Is(err, errors.ErrUnsupported) {
				t.Errorf("%s: want unsupported, got %v", tt.file, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tt.file, err)
			continue
		}
		if j.Components != tt.components || j.Progressive != tt.progressive || j.Adobe != tt.adobe || j.ColorTransform != tt.colorTransform {
			t.Errorf("%s: read as %+v", tt.file, j)
		}
		// The cleaned file must decode to the same pixels.
		a, errA := jpeg.Decode(bytes.NewReader(data))
		b, errB := jpeg.Decode(bytes.NewReader(join(j)))
		if errA != nil || errB != nil {
			t.Errorf("%s: decode: %v, %v", tt.file, errA, errB)
			continue
		}
		if !samePixels(a, b) {
			t.Errorf("%s: cleaning changed the image", tt.file)
		}
	}
}

func join(j *JPEG) []byte {
	return append(append(append([]byte(nil), j.Head...), j.Body...), j.Tail...)
}

func samePixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if color.NRGBAModel.Convert(a.At(x, y)) != color.NRGBAModel.Convert(b.At(x, y)) {
				return false
			}
		}
	}
	return true
}

func TestParseJPEGFirstEXIFWins(t *testing.T) {
	// Editors that add EXIF leave the original after theirs; browsers and
	// exiftool read the first.
	base := pdftest.JPEG(pdftest.QuadrantImage(16, 8, 1))
	j, err := ParseJPEG(pdftest.Splice(base, pdftest.EXIF(6, false, 0), pdftest.EXIF(1, true, 0)))
	if err != nil || j.Orientation != 6 {
		t.Errorf("orientation %v, %v; want 6 from the first segment", j, err)
	}
}

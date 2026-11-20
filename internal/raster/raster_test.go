package raster

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/wisebrian/pdfo/internal/filter"
	"github.com/wisebrian/pdfo/internal/syntax"
)

func TestResizeAverages(t *testing.T) {
	// 4×2 gray, halved: each output pixel averages a 2×2 block.
	src := &Image{W: 4, H: 2, C: 1, Pix: []byte{0, 100, 200, 255, 50, 150, 0, 1}}
	out := Resize(src, 2, 1)
	if want := []byte{75, 114}; !bytes.Equal(out.Pix, want) {
		t.Errorf("got %v, want %v", out.Pix, want)
	}
	// 3 → 2: the middle pixel is shared half and half.
	src = &Image{W: 3, H: 1, C: 1, Pix: []byte{0, 90, 180}}
	if got := Resize(src, 2, 1).Pix; !bytes.Equal(got, []byte{30, 150}) {
		t.Errorf("3→2: %v", got)
	}
	// A flat image stays flat at any ratio.
	flat := &Image{W: 37, H: 23, C: 3, Pix: bytes.Repeat([]byte{10, 200, 77}, 37*23)}
	for _, s := range [][2]int{{5, 3}, {36, 22}, {1, 1}, {13, 7}} {
		out := Resize(flat, s[0], s[1])
		if !bytes.Equal(out.Pix, bytes.Repeat([]byte{10, 200, 77}, s[0]*s[1])) {
			t.Errorf("flat image changed at %v", s)
		}
	}
	// Never enlarges.
	if out := Resize(flat, 100, 100); out.W != 37 || out.H != 23 {
		t.Errorf("enlarged to %d×%d", out.W, out.H)
	}
}

func TestJPEGAndFlate(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := range 48 {
		for x := range 64 {
			img.Set(x, y, color.RGBA{byte(x * 4), byte(y * 5), 128, 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	src, err := DecodeJPEG(buf.Bytes(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	half := Resize(src, 32, 24)
	enc, err := EncodeJPEG(half, 80)
	if err != nil {
		t.Fatal(err)
	}
	back, err := jpeg.Decode(bytes.NewReader(enc))
	if err != nil || back.Bounds().Dx() != 32 {
		t.Fatalf("re-encoded JPEG: %v", err)
	}
	r, g, _, _ := back.At(16, 12).RGBA()
	if d := int(r>>8) - 130; d < -12 || d > 12 {
		t.Errorf("colour drifted: r=%d", r>>8)
	}
	_ = g
	fl, err := EncodeFlate(half)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := filter.Decode(fl, []syntax.Name{"FlateDecode"}, []*syntax.Dict{filter.PredictorParms(3, 8, 32)},
		&filter.Budget{PerStream: 1 << 20, Total: 1 << 20})
	if err != nil || !bytes.Equal(dec, half.Pix) {
		t.Error("Flate round trip failed")
	}
	if _, err := DecodeJPEG(buf.Bytes(), 100); err != ErrTooLarge {
		t.Errorf("pixel limit: %v", err)
	}
	if _, err := Samples([]byte{1, 2}, 2, 2, 1); err == nil {
		t.Error("short samples accepted")
	}
}

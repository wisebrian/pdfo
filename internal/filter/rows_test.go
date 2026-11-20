package filter

import (
	"bytes"
	"compress/zlib"
	"io"
	"math/rand/v2"
	"testing"
)

// unfilter undoes PNG prediction the way a reader does. It exists only to
// check RowWriter until the decoders of the next phase replace it.
func unfilter(t *testing.T, data []byte, rowLen, bpp int) []byte {
	t.Helper()
	var out []byte
	prev := make([]byte, rowLen)
	for len(data) > 0 {
		if len(data) < rowLen+1 {
			t.Fatalf("short row: %d bytes", len(data))
		}
		f, row := data[0], append([]byte(nil), data[1:rowLen+1]...)
		data = data[rowLen+1:]
		for i := range row {
			var a, c byte
			if i >= bpp {
				a, c = row[i-bpp], prev[i-bpp]
			}
			b := prev[i]
			switch f {
			case 0:
			case 1:
				row[i] += a
			case 2:
				row[i] += b
			case 3:
				row[i] += byte((int(a) + int(b)) / 2)
			case 4:
				row[i] += paethPredictor(a, b, c)
			default:
				t.Fatalf("filter type %d", f)
			}
		}
		out = append(out, row...)
		prev = row
	}
	return out
}

func TestRowWriterRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	const w, h, bpp = 37, 23, 3
	rowLen := w * bpp
	var img []byte
	for y := range h {
		for x := range rowLen {
			switch {
			case y < 5: // flat: favours Sub or Up
				img = append(img, 200)
			case y < 10: // horizontal gradient
				img = append(img, byte(x*3))
			case y < 15: // vertical gradient
				img = append(img, byte(y*11))
			default: // noise: favours None
				img = append(img, byte(rng.IntN(256)))
			}
		}
	}
	for _, predict := range []bool{false, true} {
		var buf bytes.Buffer
		rw, err := NewRowWriter(&buf, zlib.BestCompression, rowLen, bpp, predict)
		if err != nil {
			t.Fatal(err)
		}
		for y := range h {
			if err := rw.WriteRow(img[y*rowLen : (y+1)*rowLen]); err != nil {
				t.Fatal(err)
			}
		}
		if err := rw.Close(); err != nil {
			t.Fatal(err)
		}
		zr, err := zlib.NewReader(&buf)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		got := raw
		if predict {
			got = unfilter(t, raw, rowLen, bpp)
		}
		if !bytes.Equal(got, img) {
			t.Errorf("predict=%v: round trip changed the samples", predict)
		}
	}
}

func TestRowWriterChoosesFilters(t *testing.T) {
	// The same noisy row, one level brighter each time, is predicted well
	// only from the row above: every row after the first should use Up (2)
	// or Paeth (4), which picks the byte above here.
	const rowLen = 64
	rng := rand.New(rand.NewPCG(3, 4))
	pattern := make([]byte, rowLen)
	for i := range pattern {
		pattern[i] = byte(rng.IntN(256))
	}
	var buf bytes.Buffer
	rw, _ := NewRowWriter(&buf, zlib.NoCompression, rowLen, 1, true)
	for y := range 4 {
		row := make([]byte, rowLen)
		for i := range row {
			row[i] = pattern[i] + byte(y)
		}
		if err := rw.WriteRow(row); err != nil {
			t.Fatal(err)
		}
	}
	rw.Close()
	zr, _ := zlib.NewReader(&buf)
	raw, _ := io.ReadAll(zr)
	for y := 1; y < 4; y++ {
		if f := raw[y*(rowLen+1)]; f != 2 && f != 4 {
			t.Errorf("row %d used filter %d, want Up or Paeth", y, f)
		}
	}
}

func TestRowWriterRejectsBadRows(t *testing.T) {
	if _, err := NewRowWriter(io.Discard, 6, 0, 1, true); err == nil {
		t.Error("zero row length accepted")
	}
	rw, _ := NewRowWriter(io.Discard, 6, 4, 1, true)
	if err := rw.WriteRow(make([]byte, 3)); err == nil {
		t.Error("short row accepted")
	}
}

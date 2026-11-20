package filter

import (
	"bytes"
	"compress/lzw"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
	"github.com/wisebrian/pdfo/internal/syntax"
)

func budget() *Budget { return &Budget{PerStream: 1 << 30, Total: 1 << 30} }

func decode(t *testing.T, data []byte, f syntax.Name, p *syntax.Dict) []byte {
	t.Helper()
	out, err := Decode(data, []syntax.Name{f}, []*syntax.Dict{p}, budget())
	if err != nil {
		t.Fatalf("%s: %v", f, err)
	}
	return out
}

func zlibOf(b []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func TestFlate(t *testing.T) {
	text := bytes.Repeat([]byte("PDF stream data. "), 100)
	z := zlibOf(text)
	if got := decode(t, z, "FlateDecode", nil); !bytes.Equal(got, text) {
		t.Error("zlib data decoded wrongly")
	}
	// Raw deflate data without the zlib header, as some writers produce.
	if got := decode(t, z[2:], "Fl", nil); !bytes.Equal(got, text) {
		t.Error("raw deflate data decoded wrongly")
	}
	// A bad checksum is ignored.
	bad := append([]byte(nil), z...)
	bad[len(bad)-1] ^= 0xFF
	if got := decode(t, bad, "FlateDecode", nil); !bytes.Equal(got, text) {
		t.Error("bad checksum not tolerated")
	}
	// Truncated data gives what decoded, marked partial.
	out, err := Decode(z[:len(z)/2], []syntax.Name{"FlateDecode"}, nil, budget())
	var pe *PartialError
	if !errors.As(err, &pe) || len(out) == 0 || !bytes.HasPrefix(text, out) {
		t.Errorf("truncated data: %d bytes, %v", len(out), err)
	}
	if _, err := Decode([]byte("not compressed at all"), []syntax.Name{"FlateDecode"}, nil, budget()); err == nil {
		t.Error("garbage accepted")
	}
}

func TestBomb(t *testing.T) {
	bomb := zlibOf(make([]byte, 64<<20)) // 64 MiB of zeros in about 64 KiB
	b := &Budget{PerStream: 1 << 20, Total: 1 << 30}
	if _, err := Decode(bomb, []syntax.Name{"FlateDecode"}, nil, b); !errors.Is(err, ErrLimit) {
		t.Errorf("per-stream limit: %v", err)
	}
	b = &Budget{PerStream: 1 << 30, Total: 3 << 20}
	small := zlibOf(make([]byte, 2<<20))
	if _, err := Decode(small, []syntax.Name{"FlateDecode"}, nil, b); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(small, []syntax.Name{"FlateDecode"}, nil, b); !errors.Is(err, ErrLimit) {
		t.Errorf("total limit: %v", err)
	}
	if _, err := Decode(bytes.Repeat([]byte{0x81, 0}, 1000), []syntax.Name{"RunLengthDecode"}, nil, &Budget{PerStream: 1000, Total: 1 << 20}); !errors.Is(err, ErrLimit) {
		t.Errorf("run-length bomb: %v", err)
	}
}

func TestPNGPredictorRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for _, tc := range []struct{ colors, w, h int }{{1, 17, 9}, {3, 40, 20}, {4, 3, 3}} {
		rowLen := tc.colors * tc.w
		img := make([]byte, rowLen*tc.h)
		for i := range img {
			img[i] = byte(i/7) + byte(rng.IntN(4))
		}
		var buf bytes.Buffer
		rw, _ := NewRowWriter(&buf, zlib.DefaultCompression, rowLen, tc.colors, true)
		for y := range tc.h {
			rw.WriteRow(img[y*rowLen : (y+1)*rowLen])
		}
		rw.Close()
		got := decode(t, buf.Bytes(), "FlateDecode", PredictorParms(tc.colors, 8, tc.w))
		if !bytes.Equal(got, img) {
			t.Errorf("%+v: predictor round trip failed", tc)
		}
	}
}

func TestTIFFPredictor(t *testing.T) {
	p := new(syntax.Dict)
	p.Set("Predictor", syntax.Int(2))
	p.Set("Colors", syntax.Int(3))
	p.Set("Columns", syntax.Int(3))
	// Each sample is the difference from the same colour one pixel left.
	enc := []byte{10, 20, 30, 1, 2, 3, 1, 2, 3}
	if got := decode(t, zlibOf(enc), "FlateDecode", p); !bytes.Equal(got, []byte{10, 20, 30, 11, 22, 33, 12, 24, 36}) {
		t.Errorf("8-bit TIFF predictor: %v", got)
	}
	p.Set("Colors", syntax.Int(1))
	p.Set("Columns", syntax.Int(8))
	p.Set("BitsPerComponent", syntax.Int(1))
	// 1-bit samples 1,1,1,1,0,0,0,0 stored as differences 1,0,0,0,1,0,0,0.
	if got := decode(t, zlibOf([]byte{0b10001000}), "FlateDecode", p); !bytes.Equal(got, []byte{0b11110000}) {
		t.Errorf("1-bit TIFF predictor: %08b", got)
	}
	p.Set("BitsPerComponent", syntax.Int(16))
	p.Set("Columns", syntax.Int(2))
	if got := decode(t, zlibOf([]byte{0x01, 0x00, 0x00, 0x05}), "FlateDecode", p); !bytes.Equal(got, []byte{1, 0, 1, 5}) {
		t.Errorf("16-bit TIFF predictor: %v", got)
	}
}

func TestPredictorRejectsHugeRows(t *testing.T) {
	p := new(syntax.Dict)
	p.Set("Predictor", syntax.Int(12))
	p.Set("Columns", syntax.Int(1<<23))
	p.Set("Colors", syntax.Int(32))
	if _, err := Decode(zlibOf([]byte{2, 1, 2, 3}), []syntax.Name{"FlateDecode"}, []*syntax.Dict{p}, budget()); err == nil {
		t.Error("rows far longer than the data accepted")
	}
}

// lzwEncode is a straightforward LZW encoder for the tests, with or without
// early change. The decoder adds each table entry one code after the
// encoder does, so an encoder that widens its codes when its next entry
// reaches 2^width produces what a decoder with early change reads.
func lzwEncode(data []byte, early bool) []byte {
	var out []byte
	var acc uint64
	var n uint
	put := func(code, width int) {
		acc = acc<<uint(width) | uint64(code)
		n += uint(width)
		for n >= 8 {
			out = append(out, byte(acc>>(n-8)))
			n -= 8
		}
	}
	e := 1 // late change: widen one entry later
	if early {
		e = 0
	}
	dict := map[string]int{}
	next, width := 258, 9
	put(256, width)
	w := ""
	for _, c := range data {
		wc := w + string([]byte{c})
		if _, ok := dict[wc]; ok || len(wc) == 1 {
			w = wc
			continue
		}
		code := int(w[0])
		if len(w) > 1 {
			code = dict[w]
		}
		put(code, width)
		dict[wc] = next
		next++
		if next-e >= 1<<width && width < 12 {
			width++
		}
		if next == 4096 {
			put(256, width)
			dict = map[string]int{}
			next, width = 258, 9
		}
		w = string([]byte{c})
	}
	if w != "" {
		code := int(w[0])
		if len(w) > 1 {
			code = dict[w]
		}
		put(code, width)
		next++
		if next-e >= 1<<width && width < 12 {
			width++
		}
	}
	put(257, width)
	if n > 0 {
		out = append(out, byte(acc<<(8-n)))
	}
	return out
}

func TestLZW(t *testing.T) {
	// The example of ISO 32000-2, section 7.4.4.2.
	example := []byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}
	if got := decode(t, example, "LZWDecode", nil); string(got) != "-----A---B" {
		t.Errorf("specification example decoded as %q", got)
	}
	text := make([]byte, 20000)
	rng := rand.New(rand.NewPCG(7, 8))
	for i := range text {
		text[i] = "abcab "[rng.IntN(6)]
	}
	if got := decode(t, lzwEncode(text, true), "LZWDecode", nil); !bytes.Equal(got, text) {
		t.Error("early-change data decoded wrongly")
	}
	late := new(syntax.Dict)
	late.Set("EarlyChange", syntax.Int(0))
	if got := decode(t, lzwEncode(text, false), "LZWDecode", late); !bytes.Equal(got, text) {
		t.Error("late-change data decoded wrongly")
	}
	// compress/lzw writes the late-change variant, which EarlyChange 0
	// reads...
	var buf bytes.Buffer
	lw := lzw.NewWriter(&buf, lzw.MSB, 8)
	lw.Write(text)
	lw.Close()
	if got := decode(t, buf.Bytes(), "LZWDecode", late); !bytes.Equal(got, text) {
		t.Error("compress/lzw output not read with EarlyChange 0")
	}
	// ...but cannot read the early-change data PDF writers produce by
	// default, which is why this package has its own decoder.
	got, err := io.ReadAll(lzw.NewReader(bytes.NewReader(lzwEncode(text, true)), lzw.MSB, 8))
	if err == nil && bytes.Equal(got, text) {
		t.Error("compress/lzw read early-change data: the custom decoder may be unnecessary")
	}
}

func TestASCIIFilters(t *testing.T) {
	if got := decode(t, []byte("48 65\n6c6C 6F7>ignored"), "ASCIIHexDecode", nil); string(got) != "Hellop" {
		t.Errorf("ASCIIHex: %q", got)
	}
	data := []byte("Hello, PDF\x00\x00\x00\x00 world")
	enc := make([]byte, ascii85.MaxEncodedLen(len(data)))
	enc = enc[:ascii85.Encode(enc, data)]
	wrapped := append(append([]byte("<~"), bytes.ReplaceAll(enc, []byte("!!!!!"), []byte("z"))...), "~>"...)
	wrapped = append(wrapped[:10:10], append([]byte("\n \t"), wrapped[10:]...)...)
	if got := decode(t, wrapped, "ASCII85Decode", nil); !bytes.Equal(got, data) {
		t.Errorf("ASCII85: %q", got)
	}
	rl := []byte{2, 'a', 'b', 'c', 254, 'x', 128, 'z'}
	if got := decode(t, rl, "RunLengthDecode", nil); string(got) != "abcxxx" {
		t.Errorf("RunLength: %q", got)
	}
}

func TestDecodeChainAndUnknown(t *testing.T) {
	text := []byte("chained")
	hex := []byte("")
	for _, c := range zlibOf(text) {
		hex = append(hex, "0123456789abcdef"[c>>4], "0123456789abcdef"[c&15])
	}
	out, err := Decode(hex, []syntax.Name{"AHx", "Fl"}, nil, budget())
	if err != nil || string(out) != "chained" {
		t.Errorf("filter chain: %q, %v", out, err)
	}
	if _, err := Decode(nil, []syntax.Name{"DCTDecode"}, nil, budget()); !errors.Is(err, ErrImageFilter) {
		t.Errorf("DCT: %v", err)
	}
	if _, err := Decode(nil, []syntax.Name{"Fancy"}, nil, budget()); err == nil {
		t.Error("unknown filter accepted")
	}
}

func fuzzFilter(f *testing.F, name syntax.Name, seeds ...[]byte) {
	for _, s := range seeds {
		f.Add(s, int64(12), int64(3), int64(4))
	}
	f.Fuzz(func(t *testing.T, data []byte, pred, colors, columns int64) {
		p := new(syntax.Dict)
		p.Set("Predictor", syntax.Int(pred%16))
		p.Set("Colors", syntax.Int(colors%40))
		p.Set("Columns", syntax.Int(columns))
		p.Set("BitsPerComponent", syntax.Int([]int{1, 2, 4, 8, 16, 3}[(colors&0x7fff)%6]))
		pdftest.Bounded(t, len(data), func() {
			out, _ := Decode(data, []syntax.Name{name}, []*syntax.Dict{p}, &Budget{PerStream: 4 << 20, Total: 8 << 20})
			if len(out) > 4<<20 {
				t.Fatalf("output of %d bytes passes the limit", len(out))
			}
		})
	})
}

func FuzzFlate(f *testing.F) {
	fuzzFilter(f, "FlateDecode", zlibOf([]byte("hello hello hello")), zlibOf(make([]byte, 1000)))
}

func FuzzLZW(f *testing.F) {
	fuzzFilter(f, "LZWDecode", []byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}, lzwEncode([]byte("abababababab"), true))
}

func FuzzASCIIHex(f *testing.F) { fuzzFilter(f, "ASCIIHexDecode", []byte("4142 43>")) }

func FuzzASCII85(f *testing.F) {
	fuzzFilter(f, "ASCII85Decode", []byte("<~87cURD]i,\"Ebo80~>"), []byte("zz!!~>"))
}

func FuzzRunLength(f *testing.F) {
	fuzzFilter(f, "RunLengthDecode", []byte{2, 'a', 'b', 'c', 254, 'x', 128})
}

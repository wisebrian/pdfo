package imgfmt

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
)

func TestParsePNG(t *testing.T) {
	profile := pdftest.ICCProfile("RGB ")
	phys := binary.BigEndian.AppendUint32(nil, 11811) // 300 dpi, in pixels per metre
	phys = binary.BigEndian.AppendUint32(phys, 5906)  // 150 dpi
	phys = append(phys, 1)
	exifSeg := pdftest.EXIF(8, false, 0)
	in := pdftest.EncodePNG(pdftest.PNG{
		Width: 2, Height: 1, ColorType: PNGRGB, BitDepth: 8,
		Rows: [][]byte{{1, 2, 3, 4, 5, 6}},
		Chunks: [][2][]byte{
			{[]byte("pHYs"), phys},
			{[]byte("iCCP"), pdftest.ZlibChunk("test profile\x00\x00", profile)},
			{[]byte("eXIf"), exifSeg[4+6:]}, // the TIFF data, without the APP1 framing
			{[]byte("tRNS"), {0, 1, 0, 2, 0, 3}},
		},
	})
	p, err := ParsePNG(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 2 || p.Height != 1 || p.ColorType != PNGRGB || p.BitDepth != 8 || !p.Transparent {
		t.Errorf("header read as %+v", p)
	}
	if p.DPIX < 299.9 || p.DPIX > 300.1 || p.DPIY < 149.9 || p.DPIY > 150.1 {
		t.Errorf("resolution %v×%v, want 300×150", p.DPIX, p.DPIY)
	}
	if !bytes.Equal(p.ICC, profile) {
		t.Errorf("profile not read: %q", p.ICCProblem)
	}
	if p.Orientation != 8 {
		t.Errorf("orientation %d, want 8", p.Orientation)
	}
}

func TestParsePNGIgnoresDamagedAncillaryChunks(t *testing.T) {
	phys := append(binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 11811), 11811), 1)
	in := pdftest.EncodePNG(pdftest.PNG{
		Width: 1, Height: 1, ColorType: PNGGray, BitDepth: 8, Rows: [][]byte{{0}},
		Chunks: [][2][]byte{{[]byte("pHYs"), phys}},
	})
	i := bytes.Index(in, []byte("pHYs"))
	in[i+4] ^= 0xFF // breaks the checksum
	p, err := ParsePNG(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.DPIX != 0 {
		t.Errorf("damaged pHYs chunk was used")
	}
}

func TestParsePNGBadICC(t *testing.T) {
	gray := pdftest.ICCProfile("GRAY")
	tests := []struct{ name, data string }{
		{"wrong colour space", string(pdftest.ZlibChunk("p\x00\x00", gray))},
		{"bad compression method", "p\x00\x01junk"},
		{"no name", "\x00\x00junk"},
		{"not zlib", "p\x00\x00not zlib data"},
	}
	for _, tt := range tests {
		in := pdftest.EncodePNG(pdftest.PNG{
			Width: 1, Height: 1, ColorType: PNGRGB, BitDepth: 8, Rows: [][]byte{{0, 0, 0}},
			Chunks: [][2][]byte{{[]byte("iCCP"), []byte(tt.data)}},
		})
		p, err := ParsePNG(in)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if p.ICC != nil || p.ICCProblem == "" {
			t.Errorf("%s: profile accepted", tt.name)
		}
	}
}

func TestParsePNGRejects(t *testing.T) {
	good := pdftest.EncodePNG(pdftest.PNG{Width: 1, Height: 1, ColorType: PNGGray, BitDepth: 8, Rows: [][]byte{{0}}})
	header := func(w, h uint32, depth, ctype byte) []byte {
		b := append([]byte(nil), good...)
		binary.BigEndian.PutUint32(b[16:], w)
		binary.BigEndian.PutUint32(b[20:], h)
		b[24], b[25] = depth, ctype
		return b
	}
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"empty", nil, "not a PNG"},
		{"signature only", good[:8], "no header"},
		{"zero width", header(0, 1, 8, 0), "invalid"},
		{"huge width", header(1<<31, 1, 8, 0), "invalid"},
		{"bad depth", header(1, 1, 3, 0), "invalid"},
		{"16-bit palette", header(1, 1, 16, 3), "invalid"},
		{"unknown colour type", header(1, 1, 8, 5), "invalid"},
		{"header too long", append(append([]byte(nil), good[:8]...), "\x7f\xff\xff\xffIHDRxxxx"...), "longer than the file"},
	}
	for _, tt := range tests {
		_, err := ParsePNG(tt.in)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: got %v, want %q", tt.name, err, tt.want)
		}
	}
}

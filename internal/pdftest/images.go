package pdftest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"math"
)

// Quadrants is the test picture: red, green, blue and white quadrants, from
// top left to bottom right as displayed. It is asymmetric in both directions,
// so any wrong rotation or mirroring of it shows.
var Quadrants = [4]color.NRGBA{
	{R: 220, G: 30, B: 30, A: 255},
	{R: 30, G: 200, B: 40, A: 255},
	{R: 30, G: 40, B: 210, A: 255},
	{R: 250, G: 250, B: 250, A: 255},
}

// QuadrantImage returns the test picture at w×h pixels, stored so that
// displaying it with the given EXIF orientation shows the quadrants upright.
// For orientations 5 to 8 the stored image is h×w: a viewer rotates it a
// quarter turn.
func QuadrantImage(w, h, orientation int) *image.NRGBA {
	sw, sh := w, h
	if orientation >= 5 {
		sw, sh = h, w
	}
	img := image.NewNRGBA(image.Rect(0, 0, sw, sh))
	for y := range sh {
		for x := range sw {
			// Map the stored pixel to where it is displayed.
			dx, dy := Displayed(x, y, sw, sh, orientation)
			q := 0
			if dx >= w/2 {
				q++
			}
			if dy >= h/2 {
				q += 2
			}
			img.SetNRGBA(x, y, Quadrants[q])
		}
	}
	return img
}

// Displayed maps a stored pixel of an sw×sh image to its displayed position
// under an EXIF orientation (EXIF 2.3, tag 0x0112).
func Displayed(x, y, sw, sh, orientation int) (int, int) {
	switch orientation {
	case 2:
		return sw - 1 - x, y
	case 3:
		return sw - 1 - x, sh - 1 - y
	case 4:
		return x, sh - 1 - y
	case 5:
		return y, x
	case 6:
		return sh - 1 - y, x
	case 7:
		return sh - 1 - y, sw - 1 - x
	case 8:
		return y, sw - 1 - x
	}
	return x, y
}

// JPEG encodes img with image/jpeg at quality 95.
func JPEG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// Splice inserts segments into a JPEG right after its JFIF segment (or after
// the start-of-image marker when there is none).
func Splice(j []byte, segments ...[]byte) []byte {
	at := 2
	if len(j) > 6 && j[2] == 0xFF && j[3] == 0xE0 {
		at = 4 + int(j[4])<<8 | int(j[5])
	}
	out := append([]byte(nil), j[:at]...)
	for _, s := range segments {
		out = append(out, s...)
	}
	return append(out, j[at:]...)
}

// Segment builds a JPEG marker segment.
func Segment(marker byte, payload []byte) []byte {
	n := len(payload) + 2
	return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...)
}

// EXIF builds an APP1 EXIF segment with an orientation and, when dpi > 0, a
// resolution in dots per inch.
func EXIF(orientation int, bigEndian bool, dpi float64) []byte {
	var bo binary.AppendByteOrder = binary.LittleEndian
	t := []byte("II*\x00")
	if bigEndian {
		bo, t = binary.BigEndian, []byte("MM\x00*")
	}
	t = bo.AppendUint32(t, 8)
	type entry struct {
		tag, typ uint16
		val      uint32
	}
	entries := []entry{{0x0112, 3, uint32(orientation)}}
	var rational []byte
	if dpi > 0 {
		entries = append(entries, entry{0x011A, 5, 0}, entry{0x011B, 5, 0}, entry{0x0128, 3, 2})
	}
	dataAt := uint32(8 + 2 + 12*len(entries) + 4)
	t = bo.AppendUint16(t, uint16(len(entries)))
	for _, e := range entries {
		t = bo.AppendUint16(t, e.tag)
		t = bo.AppendUint16(t, e.typ)
		t = bo.AppendUint32(t, 1)
		switch e.typ {
		case 3:
			// A SHORT is left-justified in the four-byte field.
			t = bo.AppendUint16(t, uint16(e.val))
			t = append(t, 0, 0)
		case 5:
			t = bo.AppendUint32(t, dataAt+uint32(len(rational)))
			rational = bo.AppendUint32(rational, uint32(math.Round(dpi*100)))
			rational = bo.AppendUint32(rational, 100)
		}
	}
	t = append(t, 0, 0, 0, 0) // no next directory
	t = append(t, rational...)
	return Segment(0xE1, append([]byte("Exif\x00\x00"), t...))
}

// ICCSegments splits a profile into APP2 segments of at most chunk bytes.
func ICCSegments(profile []byte, chunk int) [][]byte {
	var parts [][]byte
	for len(profile) > 0 {
		n := min(chunk, len(profile))
		parts = append(parts, profile[:n])
		profile = profile[n:]
	}
	var segs [][]byte
	for i, p := range parts {
		payload := append([]byte("ICC_PROFILE\x00"), byte(i+1), byte(len(parts)))
		segs = append(segs, Segment(0xE2, append(payload, p...)))
	}
	return segs
}

// ICCProfile builds a minimal but complete ICC version 2 display profile, a
// matrix/TRC profile with sRGB's primaries for "RGB " and a gamma curve for
// "GRAY", that colour management engines such as Little CMS accept.
func ICCProfile(space string) []byte {
	s15 := func(b []byte, v float64) []byte {
		return binary.BigEndian.AppendUint32(b, uint32(int32(math.Round(v*65536))))
	}
	xyz := func(x, y, z float64) []byte {
		b := []byte("XYZ \x00\x00\x00\x00")
		return s15(s15(s15(b, x), y), z)
	}
	curve := []byte("curv\x00\x00\x00\x00\x00\x00\x00\x01\x02\x33\x00\x00") // gamma 2.2
	text := func(s string) []byte {
		b := []byte("desc\x00\x00\x00\x00")
		b = binary.BigEndian.AppendUint32(b, uint32(len(s)+1))
		b = append(b, s...)
		b = append(b, 0)
		b = append(b, make([]byte, 4+4+2+1+67)...) // no Unicode or ScriptCode text
		return b
	}
	copyright := append([]byte("text\x00\x00\x00\x00"), "No copyright, test data\x00"...)
	type tag struct {
		sig  string
		data []byte
	}
	tags := []tag{
		{"desc", text("pdfo test " + space)},
		{"wtpt", xyz(0.9642, 1.0, 0.8249)},
		{"cprt", copyright},
	}
	if space == "GRAY" {
		tags = append(tags, tag{"kTRC", curve})
	} else {
		tags = append(tags,
			tag{"rXYZ", xyz(0.4361, 0.2225, 0.0139)},
			tag{"gXYZ", xyz(0.3851, 0.7169, 0.0971)},
			tag{"bXYZ", xyz(0.1431, 0.0606, 0.7141)},
			tag{"rTRC", curve}, tag{"gTRC", curve}, tag{"bTRC", curve})
	}
	head := make([]byte, 128)
	copy(head[4:], "none")
	binary.BigEndian.PutUint32(head[8:], 0x02100000)
	copy(head[12:], "mntr")
	copy(head[16:], space)
	copy(head[20:], "XYZ ")
	copy(head[36:], "acsp")
	copy(head[68:], s15(nil, 0.9642)) // the D50 illuminant
	copy(head[72:], s15(nil, 1.0))
	copy(head[76:], s15(nil, 0.8249))
	table := binary.BigEndian.AppendUint32(nil, uint32(len(tags)))
	var data []byte
	off := 128 + 4 + 12*len(tags)
	for _, t := range tags {
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
		table = append(table, t.sig...)
		table = binary.BigEndian.AppendUint32(table, uint32(off+len(data)))
		table = binary.BigEndian.AppendUint32(table, uint32(len(t.data)))
		data = append(data, t.data...)
	}
	p := append(append(head, table...), data...)
	binary.BigEndian.PutUint32(p, uint32(len(p)))
	return p
}

// PNG options for EncodePNG.
type PNG struct {
	Width, Height int // pixels
	ColorType     int // 0, 2, 3, 4 or 6
	BitDepth      int // 1, 2, 4, 8 or 16, as the colour type allows
	// Rows holds each row's samples, packed as the PNG specification
	// packs them, without the filter byte.
	Rows [][]byte
	// Chunks are written between the header and the image data, in order:
	// each is a type followed by its data.
	Chunks [][2][]byte
}

// EncodePNG writes a PNG file exactly as described, including combinations
// image/png never writes, such as gray with alpha or 2-bit palettes.
func EncodePNG(p PNG) []byte {
	out := []byte("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
		start := len(out)
		out = append(out, typ...)
		out = append(out, data...)
		out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[start:]))
	}
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(p.Width))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(p.Height))
	ihdr = append(ihdr, byte(p.BitDepth), byte(p.ColorType), 0, 0, 0)
	chunk("IHDR", ihdr)
	for _, c := range p.Chunks {
		chunk(string(c[0]), c[1])
	}
	var raw bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&raw, zlib.BestCompression)
	for _, r := range p.Rows {
		zw.Write([]byte{0})
		zw.Write(r)
	}
	zw.Close()
	chunk("IDAT", raw.Bytes())
	chunk("IEND", nil)
	return out
}

// ZlibChunk compresses data, for iCCP chunks.
func ZlibChunk(prefix string, data []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(prefix)
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	zw.Write(data)
	zw.Close()
	return buf.Bytes()
}

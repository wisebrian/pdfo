package imgfmt

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// maxICC bounds a decompressed PNG colour profile. Real profiles are a few
// kilobytes, large CMYK ones a few megabytes; the bound stops a
// decompression bomb hidden in the iCCP chunk.
const maxICC = 16 << 20

// PNG colour types (PNG specification, table 11.1).
const (
	PNGGray      = 0
	PNGRGB       = 2
	PNGPalette   = 3
	PNGGrayAlpha = 4
	PNGRGBA      = 6
)

// PNG describes a PNG file. The pixels are left to image/png; this is what
// image/png does not report.
type PNG struct {
	// Width and Height are the stored size in pixels.
	Width, Height int
	// BitDepth is the bits per sample (per palette index for palette
	// images), as the header declares.
	BitDepth int
	// ColorType is one of PNGGray, PNGRGB, PNGPalette, PNGGrayAlpha and
	// PNGRGBA.
	ColorType int
	// Transparent is set when a tRNS chunk gives the image transparency
	// although it has no alpha channel.
	Transparent bool
	// DPIX and DPIY are the declared resolution, 0 when there is none.
	DPIX, DPIY float64
	// ICC is the embedded colour profile, nil when there is none or it is
	// unusable; ICCProblem then says what was wrong with it.
	ICC []byte
	// ICCProblem says why a profile that was present is unusable.
	ICCProblem string
	// Orientation is the orientation from an eXIf chunk, 1 when there is
	// none.
	Orientation int
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// IsPNG reports whether data starts with the PNG signature.
func IsPNG(data []byte) bool { return bytes.HasPrefix(data, pngSignature) }

// ParsePNG reads the chunks of a PNG file that describe it.
func ParsePNG(data []byte) (*PNG, error) {
	if !IsPNG(data) {
		return nil, errors.New("not a PNG file")
	}
	p := &PNG{Orientation: 1}
	pos := len(pngSignature)
	first := true
	for pos+12 <= len(data) {
		n := uint64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		if n > uint64(len(data)-pos-12) {
			if first {
				return nil, errors.New("PNG header chunk is longer than the file")
			}
			break // truncated: image/png will say so when it decodes
		}
		body := data[pos+8 : pos+8+int(n)]
		crc := binary.BigEndian.Uint32(data[pos+8+int(n):])
		pos += 12 + int(n)
		if first {
			if typ != "IHDR" {
				return nil, errors.New("PNG does not start with a header chunk")
			}
			if err := p.header(body); err != nil {
				return nil, err
			}
			first = false
			continue
		}
		// A chunk whose checksum fails is damaged; an ancillary one is
		// ignored, as PNG decoders do.
		ok := crc32.Update(crc32.ChecksumIEEE([]byte(typ)), crc32.IEEETable, body) == crc
		switch typ {
		case "tRNS":
			p.Transparent = true
		case "pHYs":
			if ok && len(body) == 9 && body[8] == 1 { // unit: the metre
				p.DPIX = float64(binary.BigEndian.Uint32(body)) * 0.0254
				p.DPIY = float64(binary.BigEndian.Uint32(body[4:])) * 0.0254
			}
		case "iCCP":
			if ok {
				p.ICC, p.ICCProblem = iccp(body, p.components())
			}
		case "eXIf":
			if ok {
				p.Orientation = parseEXIF(body).orientation
			}
		case "IEND":
			return p, nil
		}
	}
	if first {
		return nil, errors.New("PNG has no header chunk")
	}
	return p, nil
}

func (p *PNG) header(b []byte) error {
	if len(b) != 13 {
		return errors.New("PNG header chunk has the wrong length")
	}
	w, h := binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:])
	if w == 0 || h == 0 || w > 1<<31-1 || h > 1<<31-1 {
		return fmt.Errorf("PNG size %d×%d is invalid", w, h)
	}
	p.Width, p.Height = int(w), int(h)
	p.BitDepth, p.ColorType = int(b[8]), int(b[9])
	valid := map[int][]int{
		PNGGray:      {1, 2, 4, 8, 16},
		PNGRGB:       {8, 16},
		PNGPalette:   {1, 2, 4, 8},
		PNGGrayAlpha: {8, 16},
		PNGRGBA:      {8, 16},
	}[p.ColorType]
	for _, d := range valid {
		if d == p.BitDepth {
			return nil
		}
	}
	return fmt.Errorf("PNG colour type %d with bit depth %d is invalid", p.ColorType, p.BitDepth)
}

// components is the number of colour components, alpha excluded, a profile
// for this image describes.
func (p *PNG) components() int {
	if p.ColorType == PNGGray || p.ColorType == PNGGrayAlpha {
		return 1
	}
	return 3
}

// iccp decodes an iCCP chunk: a profile name, a zero byte, the compression
// method (always 0, zlib) and the compressed profile.
func iccp(b []byte, n int) ([]byte, string) {
	i := bytes.IndexByte(b, 0)
	if i < 1 || i > 79 || i+2 > len(b) || b[i+1] != 0 {
		return nil, "PNG colour profile chunk is malformed"
	}
	zr, err := zlib.NewReader(bytes.NewReader(b[i+2:]))
	if err != nil {
		return nil, "PNG colour profile does not decompress"
	}
	prof, err := io.ReadAll(io.LimitReader(zr, maxICC+1))
	if err != nil {
		return nil, "PNG colour profile does not decompress"
	}
	if len(prof) > maxICC {
		return nil, "PNG colour profile is too large"
	}
	return CheckICC(prof, n)
}

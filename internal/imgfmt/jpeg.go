// Package imgfmt reads JPEG and PNG headers: what a PDF needs to embed an
// image (size, colour, resolution, orientation, colour profile) without
// decoding its pixels.
//
// Every length and offset read from a file is checked against the bytes that
// back it, so a hostile file can produce an error but never a panic, a hang
// or an allocation larger than the file itself.
package imgfmt

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

// Unsupported returns the error for a valid file that cannot be embedded in a
// PDF, such as "arithmetic-coded JPEG". It matches errors.ErrUnsupported, so
// callers can tell it from damage.
func Unsupported(what string) error { return unsupportedError(what) }

type unsupportedError string

func (e unsupportedError) Error() string        { return string(e) + " is not supported" }
func (e unsupportedError) Is(target error) bool { return target == errors.ErrUnsupported }

// JPEG describes a JPEG file.
type JPEG struct {
	// Width and Height are the stored size in pixels, before orientation.
	Width, Height int
	// Components is 1 (gray), 3 (YCbCr or RGB) or 4 (CMYK or YCCK).
	Components int
	// Progressive is informational: PDF 1.3 and later display
	// progressive JPEGs as they are.
	Progressive bool
	// Adobe is set when the file has an Adobe APP14 marker. Adobe's
	// applications store CMYK inverted and mark it this way, so a
	// four-component image with the marker needs an inverted Decode array.
	Adobe bool
	// ColorTransform is the value of the DCTDecode parameter of that name
	// that describes the file: 1 when the decoder must convert YCbCr (or
	// YCCK) to RGB (or CMYK), 0 when the samples are stored as they are
	// meant. It follows the Adobe marker when there is one and, without
	// one, takes three components named R, G and B to be RGB, as libjpeg
	// does.
	ColorTransform int
	// Orientation is the EXIF orientation, 1 to 8; 1 when there is none.
	Orientation int
	// DPIX and DPIY are the declared resolution, 0 when there is none.
	DPIX, DPIY float64
	// ICC is the embedded colour profile, nil when there is none or it is
	// unusable; ICCProblem then says what was wrong with it.
	ICC []byte
	// ICCProblem says why a profile that was present is unusable.
	ICCProblem string
	// Truncated is set when the end-of-image marker is missing.
	Truncated bool
	// Head, Body and Tail together are the file without its metadata: EXIF
	// (which can hold the GPS position where a photo was taken), XMP,
	// comments, thumbnails and anything after the end of the image, such as
	// the video of a "motion photo". Head holds the segments a decoder needs,
	// copied; Body is a slice of the input from the first scan to the end of
	// the image; Tail is the end-of-image marker when the file lacks one.
	// The compressed image data itself is untouched.
	Head, Body, Tail []byte
}

// Marker codes, ISO/IEC 10918-1 table B.1.
const (
	mSOF0  = 0xC0 // baseline
	mSOF1  = 0xC1 // extended sequential, Huffman
	mSOF2  = 0xC2 // progressive, Huffman
	mRST0  = 0xD0
	mRST7  = 0xD7
	mSOI   = 0xD8
	mEOI   = 0xD9
	mSOS   = 0xDA
	mDHP   = 0xDE
	mEXP   = 0xDF
	mAPP0  = 0xE0
	mAPP1  = 0xE1
	mAPP2  = 0xE2
	mAPP14 = 0xEE
	mAPP15 = 0xEF
	mCOM   = 0xFE
	mTEM   = 0x01
)

// ParseJPEG reads the header of a JPEG file.
func ParseJPEG(data []byte) (*JPEG, error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != mSOI {
		return nil, errors.New("not a JPEG file")
	}
	j := &JPEG{Orientation: 1}
	head := []byte{0xFF, mSOI}
	var (
		icc            iccChunks
		sawFrame       bool
		sawEXIF        bool
		jfif           bool
		jfifX, jfifY   float64
		exifX, exifY   float64
		compIDs        []byte
		adobeTransform int
	)
	pos := 2
	for {
		// Bytes other than 0xFF where a marker should be are junk; libjpeg
		// skips them with a warning, and viewers display such files.
		for pos < len(data) && data[pos] != 0xFF {
			pos++
		}
		for pos < len(data) && data[pos] == 0xFF {
			pos++
		}
		if pos >= len(data) {
			return nil, errors.New("JPEG ends before its image data")
		}
		m := data[pos]
		pos++
		start := pos - 2
		switch {
		case m == 0x00 || m == mTEM || (m >= mRST0 && m <= mRST7):
			continue // no length; meaningless before the first scan
		case m == mSOI:
			return nil, errors.New("JPEG has two start-of-image markers")
		case m == mEOI:
			return nil, errors.New("JPEG has no image data")
		}
		if pos+2 > len(data) {
			return nil, errors.New("JPEG ends inside a segment")
		}
		n := int(data[pos])<<8 | int(data[pos+1])
		if n < 2 || pos+n > len(data) {
			return nil, fmt.Errorf("JPEG segment at offset %d is longer than the file", start)
		}
		payload := data[pos+2 : pos+n]
		seg := data[start : pos+n]
		pos += n

		keep := true
		switch m {
		case mSOF0, mSOF1, mSOF2:
			if sawFrame {
				return nil, errors.New("JPEG has two frame headers")
			}
			sawFrame = true
			if err := j.frame(payload); err != nil {
				return nil, err
			}
			j.Progressive = m == mSOF2
			for i := range j.Components {
				compIDs = append(compIDs, payload[6+3*i])
			}
		case 0xC3:
			return nil, Unsupported("lossless JPEG")
		case 0xC5, 0xC6, 0xC7, 0xCD, 0xCE, 0xCF, mDHP, mEXP:
			return nil, Unsupported("hierarchical JPEG")
		case 0xC9, 0xCA, 0xCB:
			// PDF viewers decode arithmetic coding unreliably, and image/jpeg
			// not at all.
			return nil, Unsupported("arithmetic-coded JPEG")
		case mAPP0:
			if p := payload; len(p) >= 12 && bytes.HasPrefix(p, []byte("JFIF\x00")) && !jfif {
				jfif = true
				x, y := float64(int(p[8])<<8|int(p[9])), float64(int(p[10])<<8|int(p[11]))
				switch p[7] {
				case 1:
					jfifX, jfifY = x, y
				case 2:
					jfifX, jfifY = x*2.54, y*2.54
				}
			} else {
				keep = false // JFXX thumbnails and the like
			}
		case mAPP1:
			// Only the first EXIF segment counts, as in browsers and
			// exiftool: editors that add their own leave the old one after.
			if !sawEXIF && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
				sawEXIF = true
				e := parseEXIF(payload[6:])
				j.Orientation = e.orientation
				exifX, exifY = e.dpiX, e.dpiY
			}
			keep = false
		case mAPP2:
			if bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00")) {
				icc.add(payload[12:])
			}
			keep = false
		case mAPP14:
			if bytes.HasPrefix(payload, []byte("Adobe")) && len(payload) >= 12 {
				j.Adobe = true
				adobeTransform = int(payload[11])
			}
		case mCOM:
			keep = false
		case mSOS:
			if !sawFrame {
				return nil, errors.New("JPEG scan before its frame header")
			}
			end, truncated := scanEnd(data, start)
			j.Body = data[start:end]
			if truncated {
				j.Truncated = true
				j.Tail = []byte{0xFF, mEOI}
			}
			j.Head = head
			j.ICC, j.ICCProblem = icc.profile(j.Components)
			switch {
			case jfifX > 0 && jfifY > 0:
				j.DPIX, j.DPIY = jfifX, jfifY
			case exifX > 0 && exifY > 0:
				j.DPIX, j.DPIY = exifX, exifY
			}
			switch {
			case j.Adobe:
				// 0 means no transform; 1 (YCbCr) and 2 (YCCK) both mean
				// the transform DCTDecode calls 1.
				j.ColorTransform = min(adobeTransform, 1)
			case j.Components == 3 && !jfif && string(compIDs) == "RGB":
				j.ColorTransform = 0
			case j.Components == 3:
				j.ColorTransform = 1
			}
			return j, nil
		default:
			// Tables (DHT, DQT, DRI, DAC) stay; APP3–APP13 and APP15 hold
			// metadata only.
			if m >= mAPP0 && m <= mAPP15 {
				keep = false
			}
		}
		if keep {
			head = append(head, seg...)
		}
	}
}

func (j *JPEG) frame(p []byte) error {
	if len(p) < 6 {
		return errors.New("JPEG frame header too short")
	}
	precision := int(p[0])
	j.Height = int(p[1])<<8 | int(p[2])
	j.Width = int(p[3])<<8 | int(p[4])
	j.Components = int(p[5])
	if len(p) < 6+3*j.Components {
		return errors.New("JPEG frame header too short")
	}
	if precision != 8 {
		// DCTDecode in PDF is defined for 8-bit samples only.
		return Unsupported(strconv.Itoa(precision) + "-bit JPEG")
	}
	if j.Height == 0 {
		return Unsupported("JPEG whose height is given after the image data (DNL)")
	}
	if j.Width == 0 {
		return errors.New("JPEG has zero width")
	}
	switch j.Components {
	case 1, 3, 4:
	default:
		return Unsupported("JPEG with " + strconv.Itoa(j.Components) + " colour components")
	}
	return nil
}

// scanEnd finds the end of the image that starts with the scan at start: the
// byte after its end-of-image marker, or the end of the data, reported as
// truncated, when there is none.
func scanEnd(data []byte, start int) (end int, truncated bool) {
	p := start
	for {
		if p+1 >= len(data) {
			return len(data), true
		}
		m := data[p+1]
		switch {
		case m == mEOI:
			return p + 2, false
		case m == mTEM || (m >= mRST0 && m <= mRST7) || m == 0xFF:
			p++
			if m != 0xFF {
				p++
			}
			continue
		}
		if p+4 > len(data) {
			return len(data), true
		}
		n := int(data[p+2])<<8 | int(data[p+3])
		if n < 2 || p+2+n > len(data) {
			return len(data), true
		}
		p += 2 + n
		if m != mSOS {
			// Between scans of a progressive image come tables (DHT, DQT,
			// DRI) and markers; find the next one.
			for p < len(data) && data[p] != 0xFF {
				p++
			}
			continue
		}
		// Entropy-coded data follows the scan header. In it, 0xFF is always
		// followed by 0x00 (a stuffed byte) or a restart marker; anything
		// else is the next marker.
		for {
			i := bytes.IndexByte(data[p:], 0xFF)
			if i < 0 {
				return len(data), true
			}
			q := p + i + 1
			for q < len(data) && data[q] == 0xFF {
				q++
			}
			if q >= len(data) {
				return len(data), true
			}
			if c := data[q]; c == 0x00 || (c >= mRST0 && c <= mRST7) {
				p = q + 1
				continue
			}
			p = q - 1
			break
		}
	}
}

// iccChunks collects a profile split across APP2 segments (ICC.1, annex B.4):
// each chunk carries its sequence number and the chunk count.
type iccChunks struct {
	parts   [][]byte
	problem string
}

func (c *iccChunks) add(p []byte) {
	if c.problem != "" {
		return
	}
	if len(p) < 2 {
		c.problem = "a colour profile segment is empty"
		return
	}
	seq, count := int(p[0]), int(p[1])
	switch {
	case count == 0 || seq == 0 || seq > count:
		c.problem = "a colour profile segment is misnumbered"
	case c.parts == nil:
		c.parts = make([][]byte, count)
	case len(c.parts) != count:
		c.problem = "colour profile segments disagree on their count"
	}
	if c.problem != "" {
		return
	}
	if c.parts[seq-1] != nil {
		c.problem = "a colour profile segment appears twice"
		return
	}
	c.parts[seq-1] = p[2:]
}

// profile returns the reassembled profile, checked for an image with n
// components, or the reason it cannot be used.
func (c *iccChunks) profile(n int) ([]byte, string) {
	if c.parts == nil && c.problem == "" {
		return nil, ""
	}
	if c.problem != "" {
		return nil, c.problem
	}
	var p []byte
	for _, part := range c.parts {
		if part == nil {
			return nil, "a colour profile segment is missing"
		}
		p = append(p, part...)
	}
	p, problem := CheckICC(p, n)
	if problem != "" {
		return nil, problem
	}
	return p, ""
}

package filter

import (
	"bytes"
	"compress/flate"
	"encoding/ascii85"
	"errors"
	"io"
	"strconv"

	"github.com/wisebrian/pdfo/internal/syntax"
)

// ErrLimit is returned when decoding would exceed a Budget: the defence
// against decompression bombs.
var ErrLimit = errors.New("decoded data exceeds the size limit")

// ErrImageFilter is returned for the filters that only images use (DCT,
// JPX, JBIG2, CCITT fax). Nothing here needs their output: streams that use
// them are copied as they are.
var ErrImageFilter = errors.New("image-only filter is not decoded")

// PartialError reports a stream that decoded only in part: damaged or
// truncated compressed data, which viewers display as far as it goes. The
// data decoded before the damage is returned with it.
type PartialError struct {
	Err error // what stopped decoding
}

func (e *PartialError) Error() string { return "stream decoded only in part: " + e.Err.Error() }

// Unwrap returns the cause.
func (e *PartialError) Unwrap() error { return e.Err }

// Budget bounds the bytes decoding may produce: PerStream for any one
// stream, Total across every stream decoded with the same Budget. Total
// shrinks as streams are decoded.
type Budget struct {
	PerStream int64 // the most one stream may decode to
	Total     int64 // what all streams may still decode to
}

// limit is the most one stream may decode to under b.
func (b *Budget) limit() int64 { return min(b.PerStream, b.Total) }

func (b *Budget) spend(n int) { b.Total -= int64(n) }

// limitWriter collects output and fails once it would pass max.
type limitWriter struct {
	buf bytes.Buffer
	max int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if int64(w.buf.Len())+int64(len(p)) > w.max {
		n := int(w.max - int64(w.buf.Len()))
		w.buf.Write(p[:n])
		return n, ErrLimit
	}
	return w.buf.Write(p)
}

// Decode applies the filters of a stream to data, in order. parms holds the
// decode parameters of each filter (nil entries for none). The result never
// exceeds the budget, which is charged for it.
func Decode(data []byte, filters []syntax.Name, parms []*syntax.Dict, b *Budget) ([]byte, error) {
	var partial error
	for i, f := range filters {
		var p *syntax.Dict
		if i < len(parms) {
			p = parms[i]
		}
		out, err := decodeOne(data, f, p, b)
		var pe *PartialError
		switch {
		case errors.As(err, &pe):
			partial = err
		case err != nil:
			return nil, err
		}
		data = out
	}
	return data, partial
}

func decodeOne(data []byte, f syntax.Name, p *syntax.Dict, b *Budget) ([]byte, error) {
	max := b.limit()
	if max < 0 {
		max = 0
	}
	var (
		out []byte
		err error
	)
	switch f {
	case "FlateDecode", "Fl":
		out, err = inflate(data, max)
	case "LZWDecode", "LZW":
		early := true
		if v, ok := p.Get("EarlyChange").(syntax.Int); ok && v == 0 {
			early = false
		}
		out, err = lzwDecode(data, early, max)
	case "ASCIIHexDecode", "AHx":
		out, err = asciiHex(data, max)
	case "ASCII85Decode", "A85":
		out, err = ascii85Decode(data, max)
	case "RunLengthDecode", "RL":
		out, err = runLength(data, max)
	case "DCTDecode", "DCT", "JPXDecode", "JBIG2Decode", "CCITTFaxDecode", "CCF":
		return nil, ErrImageFilter
	case "Crypt":
		return data, nil // decryption happens before decoding
	default:
		return nil, errors.New("unknown filter " + strconv.Quote(string(f)))
	}
	if err != nil && !isPartial(err) {
		return nil, err
	}
	partial := err
	if f == "FlateDecode" || f == "Fl" || f == "LZWDecode" || f == "LZW" {
		out, err = unpredict(out, p)
		if err != nil {
			return nil, err
		}
	}
	b.spend(len(out))
	return out, partial
}

func isPartial(err error) bool {
	var pe *PartialError
	return errors.As(err, &pe)
}

// inflate decompresses zlib data. It accepts what viewers accept: a
// missing or damaged zlib header (raw deflate data), a wrong checksum and
// data that stops early, returning what decoded before the damage.
func inflate(data []byte, max int64) ([]byte, error) {
	src := data
	if len(data) >= 2 && data[0]&0x0F == 8 && (uint16(data[0])<<8|uint16(data[1]))%31 == 0 {
		src = data[2:]
	}
	r := flate.NewReader(bytes.NewReader(src))
	w := &limitWriter{max: max}
	_, err := io.Copy(w, r)
	switch {
	case err == nil:
		return w.buf.Bytes(), nil
	case errors.Is(err, ErrLimit):
		return nil, ErrLimit
	case w.buf.Len() > 0:
		return w.buf.Bytes(), &PartialError{err}
	}
	return nil, err
}

// lzwDecode decodes LZW data as PDF defines it (section 7.4.4): codes of 9
// to 12 bits, most significant bit first, 256 to clear the table and 257 to
// end. With early change, the default, the code width grows one code
// sooner than in GIF; compress/lzw implements only the GIF behaviour, which
// is why this decoder exists.
func lzwDecode(data []byte, early bool, max int64) ([]byte, error) {
	const (
		clear  = 256
		eod    = 257
		maxLen = 4096
	)
	type entry struct {
		prefix int32 // index of the entry this one extends, or -1
		last   byte  // the byte this entry adds
		first  byte  // the first byte of the string
		length int32
	}
	table := make([]entry, maxLen)
	for i := range 256 {
		table[i] = entry{prefix: -1, last: byte(i), first: byte(i), length: 1}
	}
	var (
		out      []byte
		next     = 258
		width    = 9
		prev     = -1
		bits     uint32
		nbits    uint
		pos      int
		earlyInc = 0
		scratch  []byte
	)
	if early {
		earlyInc = 1
	}
	emit := func(code int) bool {
		n := int(table[code].length)
		if int64(len(out)+n) > max {
			return false
		}
		if cap(scratch) < n {
			scratch = make([]byte, n)
		}
		s := scratch[:n]
		for i, c := n-1, code; i >= 0; i-- {
			s[i] = table[c].last
			c = int(table[c].prefix)
		}
		out = append(out, s...)
		return true
	}
	for {
		for nbits < uint(width) {
			if pos >= len(data) {
				return out, nil // data ends without an end code: accepted
			}
			bits = bits<<8 | uint32(data[pos])
			pos++
			nbits += 8
		}
		code := int(bits>>(nbits-uint(width))) & (1<<width - 1)
		nbits -= uint(width)
		switch {
		case code == clear:
			next, width, prev = 258, 9, -1
			continue
		case code == eod:
			return out, nil
		case prev == -1:
			if code > 255 {
				return out, &PartialError{errors.New("LZW data starts with a code not in the table")}
			}
			if !emit(code) {
				return nil, ErrLimit
			}
			prev = code
			continue
		}
		var first byte
		switch {
		case code < next:
			first = table[code].first
		case code == next:
			first = table[prev].first
		default:
			return out, &PartialError{errors.New("LZW code beyond the table")}
		}
		if next < maxLen {
			table[next] = entry{prefix: int32(prev), last: first, first: table[prev].first, length: table[prev].length + 1}
			next++
		}
		if !emit(code) {
			return nil, ErrLimit
		}
		prev = code
		if next+earlyInc >= 1<<width && width < 12 {
			width++
		}
	}
}

// asciiHex decodes ASCIIHexDecode data, up to the > that ends it. White
// space is ignored, and so is anything else that is not a hex digit, as in
// pdf.js; an odd final digit is followed by an assumed 0.
func asciiHex(data []byte, max int64) ([]byte, error) {
	out := make([]byte, 0, min(int64(len(data)/2+1), max))
	var hi byte
	half := false
	for _, c := range data {
		if c == '>' {
			break
		}
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			continue
		}
		if half {
			if int64(len(out)) >= max {
				return nil, ErrLimit
			}
			out = append(out, hi<<4|v)
		} else {
			hi = v
		}
		half = !half
	}
	if half {
		if int64(len(out)) >= max {
			return nil, ErrLimit
		}
		out = append(out, hi<<4)
	}
	return out, nil
}

// ascii85Decode decodes ASCII85Decode data, up to the ~> that ends it.
func ascii85Decode(data []byte, max int64) ([]byte, error) {
	if bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n\f\x00"), []byte("<~")) {
		data = data[bytes.Index(data, []byte("<~"))+2:]
	}
	if i := bytes.IndexByte(data, '~'); i >= 0 {
		data = data[:i]
	}
	// encoding/ascii85 skips white space but not the other bytes PDF
	// writers leave in; keep only the alphabet.
	clean := make([]byte, 0, len(data))
	for _, c := range data {
		if (c >= '!' && c <= 'u') || c == 'z' {
			clean = append(clean, c)
		}
	}
	w := &limitWriter{max: max}
	_, err := io.Copy(w, ascii85.NewDecoder(bytes.NewReader(clean)))
	switch {
	case err == nil:
		return w.buf.Bytes(), nil
	case errors.Is(err, ErrLimit):
		return nil, ErrLimit
	case w.buf.Len() > 0:
		return w.buf.Bytes(), &PartialError{err}
	}
	return nil, err
}

// runLength decodes RunLengthDecode data (section 7.4.5).
func runLength(data []byte, max int64) ([]byte, error) {
	var out []byte
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		switch {
		case n == 128:
			return out, nil
		case n < 128:
			k := min(n+1, len(data)-i)
			if int64(len(out)+k) > max {
				return nil, ErrLimit
			}
			out = append(out, data[i:i+k]...)
			i += k
		default:
			if i >= len(data) {
				return out, nil
			}
			k := 257 - n
			if int64(len(out)+k) > max {
				return nil, ErrLimit
			}
			for range k {
				out = append(out, data[i])
			}
			i++
		}
	}
	return out, nil
}

// unpredict undoes the predictor named in a Flate or LZW filter's decode
// parameters (section 7.4.4.4): TIFF predictor 2 or the PNG predictors
// 10 to 15, whose rows each start with their filter type.
func unpredict(data []byte, p *syntax.Dict) ([]byte, error) {
	pred := intParm(p, "Predictor", 1)
	if pred < 2 {
		return data, nil
	}
	colors := intParm(p, "Colors", 1)
	bpc := intParm(p, "BitsPerComponent", 8)
	columns := intParm(p, "Columns", 1)
	switch bpc {
	case 1, 2, 4, 8, 16:
	default:
		return nil, errors.New("predictor: bad BitsPerComponent " + strconv.Itoa(bpc))
	}
	if colors < 1 || colors > 32 || columns < 1 {
		return nil, errors.New("predictor: bad Colors or Columns")
	}
	// A row longer than the data cannot be complete; refusing it bounds
	// the row buffers by the input.
	if bits := int64(colors) * int64(bpc) * int64(columns); bits > 8*int64(len(data))+8 {
		return nil, errors.New("predictor: rows longer than the data")
	}
	rowLen := (colors*bpc*columns + 7) / 8
	bpp := max((colors*bpc+7)/8, 1)
	if pred == 2 {
		return tiffPredictor(data, rowLen, colors, bpc), nil
	}
	rows := (len(data) + rowLen) / (rowLen + 1)
	out := make([]byte, rows*rowLen) // never more than the input: no reallocation
	zero := make([]byte, rowLen)
	for r := range rows {
		in := data[r*(rowLen+1):]
		f := in[0]
		row := out[r*rowLen : (r+1)*rowLen]
		copy(row, in[1:min(len(in), 1+rowLen)]) // a short last row is padded
		prev := zero
		if r > 0 {
			prev = out[(r-1)*rowLen : r*rowLen]
		}
		switch f {
		case 1:
			for j := bpp; j < rowLen; j++ {
				row[j] += row[j-bpp]
			}
		case 2:
			for j := range row {
				row[j] += prev[j]
			}
		case 3:
			for j := range row {
				var a byte
				if j >= bpp {
					a = row[j-bpp]
				}
				row[j] += byte((int(a) + int(prev[j])) / 2)
			}
		case 4:
			for j := range row {
				var a, c byte
				if j >= bpp {
					a, c = row[j-bpp], prev[j-bpp]
				}
				row[j] += paethPredictor(a, prev[j], c)
			}
		}
		// 0 is no prediction; pdf.js treats unknown types as none too.
	}
	return out, nil
}

// tiffPredictor undoes TIFF predictor 2: each sample is stored as the
// difference from the sample of the same colour to its left.
func tiffPredictor(data []byte, rowLen, colors, bpc int) []byte {
	out := append([]byte(nil), data...)
	for start := 0; start+rowLen <= len(out); start += rowLen {
		row := out[start : start+rowLen]
		switch bpc {
		case 8:
			for j := colors; j < rowLen; j++ {
				row[j] += row[j-colors]
			}
		case 16:
			for j := 2 * colors; j+1 < rowLen; j += 2 {
				v := uint16(row[j])<<8 | uint16(row[j+1])
				l := uint16(row[j-2*colors])<<8 | uint16(row[j-2*colors+1])
				v += l
				row[j], row[j+1] = byte(v>>8), byte(v)
			}
		default:
			// Samples smaller than a byte: work sample by sample.
			mask := 1<<bpc - 1
			n := rowLen * 8 / bpc
			get := func(k int) int {
				bit := k * bpc
				return int(row[bit/8]>>(8-bpc-bit%8)) & mask
			}
			set := func(k, v int) {
				bit := k * bpc
				shift := 8 - bpc - bit%8
				row[bit/8] = row[bit/8]&^byte(mask<<shift) | byte((v&mask)<<shift)
			}
			for k := colors; k < n; k++ {
				set(k, get(k)+get(k-colors))
			}
		}
	}
	return out
}

func intParm(p *syntax.Dict, key syntax.Name, def int) int {
	if v, ok := p.Get(key).(syntax.Int); ok && v >= 0 && v < 1<<24 {
		return int(v)
	}
	return def
}

// Inflate decompresses Flate data without undoing any predictor, for
// recompressing a stream whose decode parameters stay as they are.
func Inflate(data []byte, b *Budget) ([]byte, error) {
	out, err := inflate(data, max(b.limit(), 0))
	if err != nil {
		return nil, err
	}
	b.spend(len(out))
	return out, nil
}

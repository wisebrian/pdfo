package syntax

import (
	"math"
	"strconv"
)

// Append appends the canonical serialization of o to b. The same object
// always gives the same bytes: keys keep their order, reals have a fixed
// precision, and the form of each string follows from its content.
//
// Separators are written only where the grammar needs them ("/Type/Page",
// "[0 0 612 792]"), which keeps uncompressed dictionaries small.
func Append(b []byte, o Object) []byte {
	e := encoder{b: b}
	e.object(o)
	return e.b
}

// realDecimals is the precision reals are written with. Six decimal places
// put any coordinate within a millionth of a point, far below what a
// viewer resolves, while keeping matrices that hold scale factors such as
// 1/√2 accurate enough that a page scaled twice still lands on the pixel.
const realDecimals = 6

type encoder struct {
	b []byte
}

// sep adds a space when the previous token and the next one would
// otherwise run together: only two regular characters need one.
func (e *encoder) sep(next byte) {
	if n := len(e.b); n > 0 && isRegular(e.b[n-1]) && isRegular(next) {
		e.b = append(e.b, ' ')
	}
}

func (e *encoder) object(o Object) {
	switch v := o.(type) {
	case nil:
		e.sep('n')
		e.b = append(e.b, "null"...)
	case Bool:
		e.sep('t')
		if v {
			e.b = append(e.b, "true"...)
		} else {
			e.b = append(e.b, "false"...)
		}
	case Int:
		e.sep('0')
		e.b = strconv.AppendInt(e.b, int64(v), 10)
	case Real:
		e.sep('0')
		e.b = AppendReal(e.b, float64(v))
	case Name:
		e.b = AppendName(e.b, v)
	case String:
		e.b = appendString(e.b, v)
	case Array:
		e.b = append(e.b, '[')
		for _, x := range v {
			e.object(x)
		}
		e.b = append(e.b, ']')
	case *Dict:
		e.b = append(e.b, "<<"...)
		for i := range v.Len() {
			k, x := v.At(i)
			e.b = AppendName(e.b, k)
			e.object(x)
		}
		e.b = append(e.b, ">>"...)
	case Ref:
		e.sep('0')
		e.b = strconv.AppendUint(e.b, uint64(v.Num), 10)
		e.b = append(e.b, ' ')
		e.b = strconv.AppendUint(e.b, uint64(v.Gen), 10)
		e.b = append(e.b, " R"...)
	case *Stream:
		// A stream is only valid as an indirect object, and the writer
		// handles those itself. Writing null keeps a misuse from producing
		// a file that no reader can parse.
		e.object(nil)
	}
}

// AppendReal appends f with at most realDecimals decimal places and no
// exponent, which PDF does not allow. A value that is not finite cannot be
// represented at all and is written as 0, so it can never break a file.
func AppendReal(b []byte, f float64) []byte {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return append(b, '0')
	}
	var tmp [64]byte
	s := strconv.AppendFloat(tmp[:0], f, 'f', realDecimals, 64)
	// Trim the zeros the fixed precision leaves, then a bare point.
	n := len(s)
	for n > 0 && s[n-1] == '0' {
		n--
	}
	if n > 0 && s[n-1] == '.' {
		n--
	}
	s = s[:n]
	if string(s) == "-0" {
		s = s[1:]
	}
	return append(b, s...)
}

const hexDigits = "0123456789ABCDEF"

// AppendName appends n with its leading slash, escaping as #xx every byte
// that is not a regular printable character, as section 7.3.5 requires.
func AppendName(b []byte, n Name) []byte {
	b = append(b, '/')
	for i := 0; i < len(n); i++ {
		c := n[i]
		if c < '!' || c > '~' || c == '#' || isDelimiter(c) {
			b = append(b, '#', hexDigits[c>>4], hexDigits[c&15])
		} else {
			b = append(b, c)
		}
	}
	return b
}

// appendString writes printable ASCII as a literal string, which keeps text
// such as producer names readable, and everything else as hexadecimal,
// which survives any transfer that mangles line endings or high bytes.
func appendString(b []byte, s String) []byte {
	printable := true
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > '~' {
			printable = false
			break
		}
	}
	if !printable {
		b = append(b, '<')
		for i := 0; i < len(s); i++ {
			b = append(b, hexDigits[s[i]>>4], hexDigits[s[i]&15])
		}
		return append(b, '>')
	}
	b = append(b, '(')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '(', ')', '\\':
			b = append(b, '\\', c)
		default:
			b = append(b, c)
		}
	}
	return append(b, ')')
}

// isDelimiter reports whether c is one of the delimiter characters of
// section 7.2.3.
func isDelimiter(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// IsWhite reports whether c is one of the white-space characters of
// section 7.2.3.
func IsWhite(c byte) bool {
	switch c {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}

// isRegular reports whether c is a regular character: neither white space
// nor a delimiter.
func isRegular(c byte) bool { return !IsWhite(c) && !isDelimiter(c) }

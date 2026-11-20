package syntax

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// TextString encodes s as a PDF text string (section 7.9.2.2). Printable
// ASCII is stored as is, because PDFDocEncoding agrees with ASCII there and
// every reader decodes it the same way; anything else is stored as UTF-16BE
// with a byte-order mark, the one encoding that covers all of Unicode.
func TextString(s string) String {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > '~' {
			ascii = false
			break
		}
	}
	if ascii {
		return String(s)
	}
	b := make([]byte, 0, 2+2*len(s))
	b = append(b, 0xFE, 0xFF)
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if r1, r2 := utf16.EncodeRune(r); r1 != utf8.RuneError {
			b = append(b, byte(r1>>8), byte(r1), byte(r2>>8), byte(r2))
		} else {
			b = append(b, byte(r>>8), byte(r))
		}
	}
	return String(b)
}

// Date converts an RFC 3339 timestamp, such as "2026-11-19T21:05:00+02:00"
// or the "2026-11-19T19:05:00.000Z" that JavaScript's toISOString returns,
// to a PDF date string (section 7.9.4). Fractions of a second are dropped:
// PDF dates have none.
//
// It parses by hand rather than with the time package, which is the one
// way to be sure no code path here can read the clock.
func Date(s string) (String, error) {
	bad := func(why string) (String, error) {
		return "", errors.New("timestamp " + strconv.Quote(s) + ": " + why)
	}
	// YYYY-MM-DDTHH:MM:SS is 19 bytes, followed by an optional fraction and
	// then Z or ±HH:MM.
	if len(s) < 20 {
		return bad("want RFC 3339, e.g. 2026-11-19T21:05:00+02:00")
	}
	num := func(at, n int) (int, bool) {
		v := 0
		for i := at; i < at+n; i++ {
			c := s[i]
			if c < '0' || c > '9' {
				return 0, false
			}
			v = v*10 + int(c-'0')
		}
		return v, true
	}
	if s[4] != '-' || s[7] != '-' || (s[10] != 'T' && s[10] != 't' && s[10] != ' ') || s[13] != ':' || s[16] != ':' {
		return bad("want RFC 3339, e.g. 2026-11-19T21:05:00+02:00")
	}
	year, ok1 := num(0, 4)
	month, ok2 := num(5, 2)
	day, ok3 := num(8, 2)
	hour, ok4 := num(11, 2)
	minute, ok5 := num(14, 2)
	second, ok6 := num(17, 2)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
		return bad("want RFC 3339, e.g. 2026-11-19T21:05:00+02:00")
	}
	if month < 1 || month > 12 || day < 1 || day > daysIn(year, month) {
		return bad("no such date")
	}
	// RFC 3339 allows a leap second (60); PDF dates do not.
	if hour > 23 || minute > 59 || second > 59 {
		return bad("no such time")
	}
	rest := s[19:]
	if len(rest) > 0 && rest[0] == '.' {
		i := 1
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 1 {
			return bad("empty fraction of a second")
		}
		rest = rest[i:]
	}
	out := make([]byte, 0, 23)
	out = append(out, "D:"...)
	out = append(out, s[0:4]...)
	out = append(out, s[5:7]...)
	out = append(out, s[8:10]...)
	out = append(out, s[11:13]...)
	out = append(out, s[14:16]...)
	out = append(out, s[17:19]...)
	switch {
	case rest == "Z" || rest == "z":
		out = append(out, 'Z')
	case len(rest) == 6 && (rest[0] == '+' || rest[0] == '-') && rest[3] == ':':
		zh, okh := num(len(s)-5, 2)
		zm, okm := num(len(s)-2, 2)
		if !okh || !okm || zh > 23 || zm > 59 {
			return bad("bad time zone offset")
		}
		// ISO 32000-1 and -2 both write the offset as +HH'mm, without the
		// trailing apostrophe of the older Adobe reference.
		out = append(out, rest[0])
		out = append(out, rest[1:3]...)
		out = append(out, '\'')
		out = append(out, rest[4:6]...)
	default:
		return bad("missing time zone (Z or ±HH:MM)")
	}
	return String(out), nil
}

func daysIn(year, month int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// pdfDocEncoding maps the bytes of PDFDocEncoding that differ from Latin-1
// (ISO 32000-2, annex D.2).
var pdfDocEncoding = map[byte]rune{
	0x18: 0x02D8, 0x19: 0x02C7, 0x1A: 0x02C6, 0x1B: 0x02D9, 0x1C: 0x02DD, 0x1D: 0x02DB, 0x1E: 0x02DA, 0x1F: 0x02DC,
	0x80: 0x2022, 0x81: 0x2020, 0x82: 0x2021, 0x83: 0x2026, 0x84: 0x2014, 0x85: 0x2013, 0x86: 0x0192, 0x87: 0x2044,
	0x88: 0x2039, 0x89: 0x203A, 0x8A: 0x2212, 0x8B: 0x2030, 0x8C: 0x201E, 0x8D: 0x201C, 0x8E: 0x201D, 0x8F: 0x2018,
	0x90: 0x2019, 0x91: 0x201A, 0x92: 0x2122, 0x93: 0xFB01, 0x94: 0xFB02, 0x95: 0x0141, 0x96: 0x0152, 0x97: 0x0160,
	0x98: 0x0178, 0x99: 0x017D, 0x9A: 0x0131, 0x9B: 0x0142, 0x9C: 0x0153, 0x9D: 0x0161, 0x9E: 0x017E, 0x9F: 0xFFFD,
	0xA0: 0x20AC, 0xAD: 0xFFFD,
}

// Text decodes a text string (section 7.9.2.2) to UTF-8: UTF-16 with a
// byte-order mark (big-endian, as the specification says, or little-endian,
// as some writers produce), UTF-8 with a byte-order mark (PDF 2.0), or
// PDFDocEncoding.
func Text(s String) string {
	b := []byte(s)
	switch {
	case len(b) >= 2 && (b[0] == 0xFE && b[1] == 0xFF || b[0] == 0xFF && b[1] == 0xFE):
		be := b[0] == 0xFE
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			if be {
				u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
			} else {
				u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
			}
		}
		return string(utf16.Decode(u))
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return strings.ToValidUTF8(string(b[3:]), "�")
	}
	r := make([]rune, 0, len(b))
	for _, c := range b {
		if m, ok := pdfDocEncoding[c]; ok {
			r = append(r, m)
		} else {
			r = append(r, rune(c))
		}
	}
	return string(r)
}

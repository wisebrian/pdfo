package syntax

import (
	"errors"
	"strconv"
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

package jsapi

import (
	"errors"
	"math"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// This file reads and writes the JSON that crosses between JavaScript and Go.
// encoding/json would do it in a line, but in Go 1.27 it carries the whole
// json/v2 machinery: 1.8 MB of WebAssembly, a third of the module, for what
// is a handful of flat objects. The reader accepts what JSON.stringify
// produces; the writer produces what JSON.parse accepts.

// maxJSONDepth bounds nesting; the options objects are two levels deep.
const maxJSONDepth = 32

// parseJSON decodes one JSON value into map[string]any, []any, string,
// float64, bool or nil.
func parseJSON(data []byte) (any, error) {
	p := &jsonParser{s: data}
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i != len(p.s) {
		return nil, p.fail("unexpected data after the value")
	}
	return v, nil
}

type jsonParser struct {
	s []byte
	i int
}

func (p *jsonParser) fail(msg string) error {
	return errors.New("invalid JSON at byte " + strconv.Itoa(p.i) + ": " + msg)
}

func (p *jsonParser) space() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *jsonParser) value(depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, p.fail("nested too deeply")
	}
	p.space()
	if p.i >= len(p.s) {
		return nil, p.fail("unexpected end")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		p.i++
		m := map[string]any{}
		p.space()
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			return m, nil
		}
		for {
			p.space()
			if p.i >= len(p.s) || p.s[p.i] != '"' {
				return nil, p.fail("want a key")
			}
			k, err := p.str()
			if err != nil {
				return nil, err
			}
			p.space()
			if p.i >= len(p.s) || p.s[p.i] != ':' {
				return nil, p.fail("want ':'")
			}
			p.i++
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			m[k] = v
			p.space()
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
				continue
			}
			if p.i < len(p.s) && p.s[p.i] == '}' {
				p.i++
				return m, nil
			}
			return nil, p.fail("want ',' or '}'")
		}
	case c == '[':
		p.i++
		a := []any{}
		p.space()
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			return a, nil
		}
		for {
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
			p.space()
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
				continue
			}
			if p.i < len(p.s) && p.s[p.i] == ']' {
				p.i++
				return a, nil
			}
			return nil, p.fail("want ',' or ']'")
		}
	case c == '"':
		return p.str()
	case c == 't' || c == 'f' || c == 'n':
		for _, w := range []struct {
			lit string
			v   any
		}{{"true", true}, {"false", false}, {"null", nil}} {
			if len(p.s)-p.i >= len(w.lit) && string(p.s[p.i:p.i+len(w.lit)]) == w.lit {
				p.i += len(w.lit)
				return w.v, nil
			}
		}
		return nil, p.fail("unknown literal")
	case c == '-' || (c >= '0' && c <= '9'):
		start := p.i
		for p.i < len(p.s) && (p.s[p.i] == '-' || p.s[p.i] == '+' || p.s[p.i] == '.' ||
			p.s[p.i] == 'e' || p.s[p.i] == 'E' || (p.s[p.i] >= '0' && p.s[p.i] <= '9')) {
			p.i++
		}
		f, err := strconv.ParseFloat(string(p.s[start:p.i]), 64)
		if err != nil || math.IsInf(f, 0) {
			p.i = start
			return nil, p.fail("bad number")
		}
		return f, nil
	}
	return nil, p.fail("unexpected character")
}

// str reads a string, with p.i at its opening quote.
func (p *jsonParser) str() (string, error) {
	p.i++
	var b []byte
	for {
		if p.i >= len(p.s) {
			return "", p.fail("unterminated string")
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return string(b), nil
		case c < ' ':
			return "", p.fail("control character in string")
		case c != '\\':
			b = append(b, c)
			p.i++
			continue
		}
		p.i++
		if p.i >= len(p.s) {
			return "", p.fail("unterminated escape")
		}
		e := p.s[p.i]
		p.i++
		switch e {
		case '"', '\\', '/':
			b = append(b, e)
		case 'b':
			b = append(b, '\b')
		case 'f':
			b = append(b, '\f')
		case 'n':
			b = append(b, '\n')
		case 'r':
			b = append(b, '\r')
		case 't':
			b = append(b, '\t')
		case 'u':
			r, ok := p.hex4()
			if !ok {
				return "", p.fail("bad \\u escape")
			}
			if utf16.IsSurrogate(r) {
				// A high surrogate must be followed by \u and a low one.
				r2 := utf8.RuneError
				if p.i+1 < len(p.s) && p.s[p.i] == '\\' && p.s[p.i+1] == 'u' {
					p.i += 2
					if lo, ok := p.hex4(); ok {
						r2 = lo
					}
				}
				r = utf16.DecodeRune(r, r2)
			}
			b = utf8.AppendRune(b, r)
		default:
			return "", p.fail("bad escape")
		}
	}
}

func (p *jsonParser) hex4() (rune, bool) {
	if len(p.s)-p.i < 4 {
		return 0, false
	}
	v, err := strconv.ParseUint(string(p.s[p.i:p.i+4]), 16, 16)
	if err != nil {
		return 0, false
	}
	p.i += 4
	return rune(v), true
}

// appendJSONString appends s as a JSON string. Invalid UTF-8 becomes
// U+FFFD, which JSON.parse would otherwise reject or mangle.
func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b = append(b, '\\', byte(r))
		case r == '\n':
			b = append(b, '\\', 'n')
		case r == '\r':
			b = append(b, '\\', 'r')
		case r == '\t':
			b = append(b, '\\', 't')
		case r < ' ' || r == 0x2028 || r == 0x2029:
			// U+2028 and U+2029 are valid in JSON but end a line in older
			// JavaScript, so they are escaped as JSON.stringify does not.
			b = append(b, `\u`...)
			b = append(b, "0123456789abcdef"[r>>12&15], "0123456789abcdef"[r>>8&15],
				"0123456789abcdef"[r>>4&15], "0123456789abcdef"[r&15])
		default:
			b = utf8.AppendRune(b, r)
		}
	}
	return append(b, '"')
}

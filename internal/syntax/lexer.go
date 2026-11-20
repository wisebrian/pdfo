package syntax

import (
	"strconv"
)

// Kind is the kind of a token.
type Kind int

// Token kinds.
const (
	EOF        Kind = iota // end of input
	Integer                // an integer: Token.Int
	Number                 // a real number: Token.Real
	NameTok                // a name: Token.Value, #xx escapes decoded
	StringTok              // a literal or hexadecimal string: Token.Value
	ArrayOpen              // [
	ArrayClose             // ]
	DictOpen               // <<
	DictClose              // >>
	BraceOpen              // {, in PostScript calculator functions
	BraceClose             // }
	Keyword                // any other run of regular characters: Token.Value
	Bad                    // a delimiter where none belongs, such as a lone ")"
)

// Token is one lexical token.
type Token struct {
	Kind  Kind    // what the token is
	Pos   int     // offset of the token's first byte
	Int   int64   // the value of an Integer
	Real  float64 // the value of a Number
	Value string  // the text of a name, string or keyword
}

// Lexer splits PDF syntax into tokens (ISO 32000-2, section 7.2). It accepts
// what Acrobat and pdf.js accept: signs doubled ("--5"), numbers without
// digits ("-", "."), unbalanced or unterminated strings, invalid #
// escapes in names. It never fails; anything it cannot read becomes a Bad
// or Keyword token for the parser to judge.
type Lexer struct {
	data []byte
	pos  int
}

// NewLexer returns a Lexer reading data from offset pos.
func NewLexer(data []byte, pos int) *Lexer { return &Lexer{data: data, pos: pos} }

// Pos returns the offset of the next byte the lexer will read.
func (l *Lexer) Pos() int { return l.pos }

// SetPos moves the lexer to offset pos.
func (l *Lexer) SetPos(pos int) { l.pos = pos }

// Data returns the input.
func (l *Lexer) Data() []byte { return l.data }

// SkipSpace skips white space and comments.
func (l *Lexer) SkipSpace() {
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		if IsWhite(c) {
			l.pos++
			continue
		}
		if c == '%' {
			for l.pos < len(l.data) && l.data[l.pos] != '\n' && l.data[l.pos] != '\r' {
				l.pos++
			}
			continue
		}
		return
	}
}

// Next returns the next token.
func (l *Lexer) Next() Token {
	l.SkipSpace()
	start := l.pos
	if l.pos >= len(l.data) {
		return Token{Kind: EOF, Pos: start}
	}
	c := l.data[l.pos]
	switch c {
	case '[':
		l.pos++
		return Token{Kind: ArrayOpen, Pos: start}
	case ']':
		l.pos++
		return Token{Kind: ArrayClose, Pos: start}
	case '{':
		l.pos++
		return Token{Kind: BraceOpen, Pos: start}
	case '}':
		l.pos++
		return Token{Kind: BraceClose, Pos: start}
	case '/':
		l.pos++
		return Token{Kind: NameTok, Pos: start, Value: l.name()}
	case '(':
		l.pos++
		return Token{Kind: StringTok, Pos: start, Value: l.literal()}
	case '<':
		if l.pos+1 < len(l.data) && l.data[l.pos+1] == '<' {
			l.pos += 2
			return Token{Kind: DictOpen, Pos: start}
		}
		l.pos++
		return Token{Kind: StringTok, Pos: start, Value: l.hex()}
	case '>':
		if l.pos+1 < len(l.data) && l.data[l.pos+1] == '>' {
			l.pos += 2
			return Token{Kind: DictClose, Pos: start}
		}
		l.pos++
		return Token{Kind: Bad, Pos: start, Value: ">"}
	case ')':
		l.pos++
		return Token{Kind: Bad, Pos: start, Value: ")"}
	}
	if c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9') {
		return l.number()
	}
	for l.pos < len(l.data) && isRegular(l.data[l.pos]) {
		l.pos++
	}
	return Token{Kind: Keyword, Pos: start, Value: string(l.data[start:l.pos])}
}

// number reads a number. Like pdf.js, it takes extra signs as one, a sign
// or point without digits as 0, and stops at the first character that
// cannot continue the number.
func (l *Lexer) number() Token {
	start := l.pos
	neg := false
	for l.pos < len(l.data) && (l.data[l.pos] == '+' || l.data[l.pos] == '-') {
		neg = l.data[l.pos] == '-'
		l.pos++
	}
	digits := l.pos
	dot := false
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		if c >= '0' && c <= '9' {
			l.pos++
		} else if c == '.' && !dot {
			dot = true
			l.pos++
		} else {
			break
		}
	}
	s := string(l.data[digits:l.pos])
	if s == "" || s == "." {
		return Token{Kind: Integer, Pos: start}
	}
	if !dot {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			if neg {
				v = -v
			}
			return Token{Kind: Integer, Pos: start, Int: v}
		}
	}
	f, _ := strconv.ParseFloat(s, 64) // digits and one point: only overflow can fail, giving ±Inf
	if f > 3.4e38 {
		f = 3.4e38 // the largest real a PDF may hold
	}
	if neg {
		f = -f
	}
	return Token{Kind: Number, Pos: start, Real: f}
}

func (l *Lexer) name() string {
	var b []byte
	start := l.pos
	for l.pos < len(l.data) && isRegular(l.data[l.pos]) {
		c := l.data[l.pos]
		if c == '#' && l.pos+2 < len(l.data) {
			if h, ok := unhex(l.data[l.pos+1], l.data[l.pos+2]); ok {
				if b == nil {
					b = append([]byte(nil), l.data[start:l.pos]...)
				}
				b = append(b, h)
				l.pos += 3
				continue
			}
		}
		if b != nil {
			b = append(b, c)
		}
		l.pos++
	}
	if b == nil {
		return string(l.data[start:l.pos])
	}
	return string(b)
}

func unhex(a, b byte) (byte, bool) {
	x, ok1 := hexVal(a)
	y, ok2 := hexVal(b)
	return x<<4 | y, ok1 && ok2
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// literal reads a literal string after its opening parenthesis (section
// 7.3.4.2): balanced parentheses, escapes, and end-of-line markers, which
// all read as a single line feed. An unterminated string ends at the end
// of the input.
func (l *Lexer) literal() string {
	var b []byte
	depth := 1
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		l.pos++
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return string(b)
			}
		case '\r':
			if l.pos < len(l.data) && l.data[l.pos] == '\n' {
				l.pos++
			}
			c = '\n'
		case '\\':
			if l.pos >= len(l.data) {
				return string(b)
			}
			e := l.data[l.pos]
			l.pos++
			switch e {
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case '\r':
				if l.pos < len(l.data) && l.data[l.pos] == '\n' {
					l.pos++
				}
				continue // a line continuation
			case '\n':
				continue
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for k := 0; k < 2 && l.pos < len(l.data) && l.data[l.pos] >= '0' && l.data[l.pos] <= '7'; k++ {
						v = v*8 + int(l.data[l.pos]-'0')
						l.pos++
					}
					c = byte(v) // overflow is ignored, as the specification says
				} else {
					c = e // \( \) \\ and unknown escapes: the character itself
				}
			}
		}
		b = append(b, c)
	}
	return string(b)
}

// hex reads a hexadecimal string after its "<". White space is ignored, and
// so, as pdf.js does, is anything else that is not a hex digit; an odd
// final digit is followed by an assumed 0.
func (l *Lexer) hex() string {
	var b []byte
	var hi byte
	half := false
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		l.pos++
		if c == '>' {
			break
		}
		v, ok := hexVal(c)
		if !ok {
			continue
		}
		if half {
			b = append(b, hi<<4|v)
		} else {
			hi = v
		}
		half = !half
	}
	if half {
		b = append(b, hi<<4)
	}
	return string(b)
}

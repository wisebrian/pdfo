package syntax

import (
	"errors"
	"strconv"
)

// ErrDepth is returned when objects nest deeper than a Parser allows: real
// files nest a handful of levels, and unbounded nesting would let a small
// hostile file exhaust the stack.
var ErrDepth = errors.New("objects nested too deeply")

// Error is a syntax error at an offset in the input.
type Error struct {
	Pos int    // offset in the input
	Msg string // what was expected or found
}

func (e *Error) Error() string { return e.Msg + " at byte " + strconv.Itoa(e.Pos) }

// Parser reads objects (section 7.3) from a Lexer.
type Parser struct {
	lex      *Lexer
	maxDepth int
	buf      []Token // tokens read ahead, next first
}

// NewParser returns a Parser reading from lex, refusing objects nested more
// than maxDepth levels.
func NewParser(lex *Lexer, maxDepth int) *Parser {
	return &Parser{lex: lex, maxDepth: maxDepth}
}

// Lexer returns the parser's lexer. Its position is only meaningful when no
// token has been read ahead; Pos accounts for that.
func (p *Parser) Lexer() *Lexer { return p.lex }

// Pos returns the offset of the next token the parser will consume.
func (p *Parser) Pos() int {
	if len(p.buf) > 0 {
		return p.buf[0].Pos
	}
	p.lex.SkipSpace()
	return p.lex.Pos()
}

// Next returns the next token.
func (p *Parser) Next() Token {
	if len(p.buf) > 0 {
		t := p.buf[0]
		p.buf = p.buf[1:]
		return t
	}
	return p.lex.Next()
}

// Peek returns the token i places ahead (0 is the next) without consuming it.
func (p *Parser) Peek(i int) Token {
	for len(p.buf) <= i {
		p.buf = append(p.buf, p.lex.Next())
	}
	return p.buf[i]
}

// unread puts t back as the next token.
func (p *Parser) unread(t Token) {
	p.buf = append([]Token{t}, p.buf...)
}

// Object reads one object. A keyword other than true, false and null, such
// as "endobj" or "stream", is not an object: it is left unread and an error
// returned, so the caller can see it with Peek.
func (p *Parser) Object() (Object, error) {
	return p.object(0)
}

func (p *Parser) object(depth int) (Object, error) {
	if depth > p.maxDepth {
		return nil, ErrDepth
	}
	t := p.Next()
	switch t.Kind {
	case EOF:
		return nil, &Error{t.Pos, "unexpected end of data"}
	case Integer:
		// "N G R" is a reference: look two tokens ahead.
		if g := p.Peek(0); g.Kind == Integer {
			if r := p.Peek(1); r.Kind == Keyword && r.Value == "R" {
				p.Next()
				p.Next()
				if t.Int < 0 || t.Int > 1<<32-1 || g.Int < 0 || g.Int > 1<<16-1 {
					return nil, nil // an impossible reference is a null
				}
				return Ref{Num: uint32(t.Int), Gen: uint16(g.Int)}, nil
			}
		}
		return Int(t.Int), nil
	case Number:
		return Real(t.Real), nil
	case NameTok:
		return Name(t.Value), nil
	case StringTok:
		return String(t.Value), nil
	case ArrayOpen:
		var a Array
		for {
			n := p.Peek(0)
			switch n.Kind {
			case ArrayClose:
				p.Next()
				if a == nil {
					a = Array{}
				}
				return a, nil
			case EOF:
				return nil, &Error{n.Pos, "unterminated array"}
			case DictClose, Bad, BraceOpen, BraceClose:
				p.Next() // stray delimiter: skip it, as viewers do
				continue
			case Keyword:
				if n.Value != "true" && n.Value != "false" && n.Value != "null" {
					// A keyword means the array was never closed; end
					// it here and leave the keyword to the caller.
					return a, nil
				}
			}
			v, err := p.object(depth + 1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
	case DictOpen:
		d := new(Dict)
		for {
			n := p.Next()
			switch n.Kind {
			case DictClose:
				return d, nil
			case EOF:
				return nil, &Error{n.Pos, "unterminated dictionary"}
			case NameTok:
			case Keyword:
				if n.Value != "true" && n.Value != "false" && n.Value != "null" {
					// "stream" or "endobj" before ">>": the dictionary was
					// never closed. Keep what was read.
					p.unread(n)
					return d, nil
				}
				continue
			default:
				// A key that is not a name: skip it, as pdf.js does.
				if n.Kind == ArrayOpen || n.Kind == DictOpen {
					p.unread(n)
					if _, err := p.object(depth + 1); err != nil {
						return nil, err
					}
				}
				continue
			}
			key := Name(n.Value)
			v := p.Peek(0)
			if v.Kind == DictClose {
				continue // a key without a value means null
			}
			if v.Kind == Keyword && v.Value != "true" && v.Value != "false" && v.Value != "null" {
				p.Next()
				continue // an operator where a value belongs: skip both
			}
			val, err := p.object(depth + 1)
			if err != nil {
				return nil, err
			}
			if d.Get(key) == nil {
				// The first occurrence of a duplicated key wins, as in
				// Acrobat.
				d.Set(key, val)
			}
		}
	case Keyword:
		switch t.Value {
		case "true":
			return Bool(true), nil
		case "false":
			return Bool(false), nil
		case "null":
			return nil, nil
		}
		p.unread(t)
		return nil, &Error{t.Pos, "unexpected keyword " + strconv.Quote(t.Value)}
	}
	return nil, &Error{t.Pos, "unexpected " + strconv.Quote(t.Value)}
}

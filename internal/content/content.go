// Package content reads content streams (ISO 32000-2, section 8): it splits
// them into operators and follows the graphics state far enough to know
// where each image is drawn and how large.
package content

import (
	"bytes"
	"math"

	"github.com/wisebrian/pdfo/internal/reader"
	"github.com/wisebrian/pdfo/internal/syntax"
)

// maxArgs bounds the operands kept for one operator; no operator takes more
// than a handful, and a stream of operands without operator must not grow
// without bound.
const maxArgs = 64

// Ops calls fn for every operator in a content stream, with its operands.
// Inline images (BI … ID data EI) are skipped whole, their binary data
// never read as operators. Damaged syntax is skipped token by token, as
// viewers do. fn returns false to stop.
func Ops(data []byte, maxDepth int, fn func(op string, args []syntax.Object) bool) {
	p := syntax.NewParser(syntax.NewLexer(data, 0), maxDepth)
	var args []syntax.Object
	for {
		t := p.Peek(0)
		switch t.Kind {
		case syntax.EOF:
			return
		case syntax.Keyword:
			if t.Value == "true" || t.Value == "false" || t.Value == "null" {
				break
			}
			p.Next()
			if t.Value == "BI" {
				skipInlineImage(p)
				args = args[:0]
				continue
			}
			if !fn(t.Value, args) {
				return
			}
			args = args[:0]
			continue
		case syntax.Bad, syntax.ArrayClose, syntax.DictClose, syntax.BraceOpen, syntax.BraceClose:
			p.Next()
			continue
		}
		o, err := p.Object()
		if err != nil {
			p.Next()
			continue
		}
		if len(args) < maxArgs {
			args = append(args, o)
		}
	}
}

// skipInlineImage moves past an inline image: its dictionary up to ID, then
// its data up to an EI that stands alone, delimited by white space.
func skipInlineImage(p *syntax.Parser) {
	for {
		t := p.Peek(0)
		if t.Kind == syntax.EOF {
			return
		}
		if t.Kind == syntax.Keyword && t.Value == "ID" {
			p.Next()
			break
		}
		if _, err := p.Object(); err != nil {
			p.Next()
		}
	}
	l := p.Lexer()
	data := l.Data()
	pos := l.Pos() + 1 // one white-space byte follows ID
	for pos < len(data) {
		i := bytes.Index(data[pos:], []byte("EI"))
		if i < 0 {
			l.SetPos(len(data))
			return
		}
		at := pos + i
		before := at == 0 || syntax.IsWhite(data[at-1])
		after := at+2 >= len(data) || syntax.IsWhite(data[at+2])
		if before && after {
			l.SetPos(at + 2)
			return
		}
		pos = at + 2
	}
	l.SetPos(len(data))
}

// Matrix is an affine transformation [a b c d e f].
type Matrix [6]float64

// Identity is the identity matrix.
var Identity = Matrix{1, 0, 0, 1, 0, 0}

// Mul returns the transformation that applies m, then n.
func (m Matrix) Mul(n Matrix) Matrix {
	return Matrix{
		m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4], m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// Use is how an image is drawn: its largest drawn size, in points, along
// each of its own axes, and the first page that draws it.
type Use struct {
	W, H float64 // points, along the image's x and y axes
	Page int     // zero-based
}

// Scanner finds where images are drawn.
type Scanner struct {
	r        *reader.Reader
	maxDepth int
	// Uses maps image objects to how they are drawn.
	Uses map[uint32]*Use
	ops  int // operators read, against maxOps
}

// maxOps bounds the operators read across a document, so that pathological
// content (a form drawing itself through a long chain) costs bounded time.
const maxOps = 50_000_000

// maxFormDepth bounds nested form XObjects.
const maxFormDepth = 12

// NewScanner returns a Scanner for a document.
func NewScanner(r *reader.Reader, maxDepth int) *Scanner {
	return &Scanner{r: r, maxDepth: maxDepth, Uses: map[uint32]*Use{}}
}

// Page scans page index of the document: its content and the appearances
// of its annotations.
func (s *Scanner) Page(index int, p *reader.Page) {
	unit := p.UserUnit
	base := Matrix{unit, 0, 0, unit, 0, 0}
	s.stream(p.Dict.Get("Contents"), p.Resources, base, index, nil)
	annots, _ := s.r.Resolve(p.Dict.Get("Annots")).(syntax.Array)
	for _, a := range annots {
		ad, ok := s.r.Resolve(a).(*syntax.Dict)
		if !ok {
			continue
		}
		if f, _ := s.r.Resolve(ad.Get("F")).(syntax.Int); f&2 != 0 {
			continue // hidden
		}
		ap, _ := s.r.Resolve(ad.Get("AP")).(*syntax.Dict)
		form := ap.Get("N")
		if states, ok := s.r.Resolve(form).(*syntax.Dict); ok {
			state, _ := s.r.Resolve(ad.Get("AS")).(syntax.Name)
			form = states.Get(state)
		}
		fs, ok := s.r.Resolve(form).(*syntax.Stream)
		if !ok {
			continue
		}
		rect, ok1 := s.r.Rect(ad.Get("Rect"))
		bbox, ok2 := s.r.Rect(fs.Dict.Get("BBox"))
		if !ok1 || !ok2 {
			continue
		}
		fm := s.matrix(fs.Dict.Get("Matrix"))
		tb := box(fm, bbox)
		if tb.W() <= 0 || tb.H() <= 0 {
			continue
		}
		sx, sy := rect.W()/tb.W(), rect.H()/tb.H()
		a := Matrix{sx, 0, 0, sy, rect.X0 - tb.X0*sx, rect.Y0 - tb.Y0*sy}
		s.form(form, fs, fm.Mul(a).Mul(base), index, nil, 0)
	}
}

// stream runs a content stream (or an array of them, which concatenate)
// with the given resources and starting transformation.
func (s *Scanner) stream(c syntax.Object, res syntax.Object, ctm Matrix, page int, stack []uint32) {
	var data []byte
	switch v := s.r.Resolve(c).(type) {
	case *syntax.Stream:
		data, _ = s.r.Decode(v)
	case syntax.Array:
		for _, x := range v {
			if st, ok := s.r.Resolve(x).(*syntax.Stream); ok {
				d, _ := s.r.Decode(st)
				data = append(data, d...)
				data = append(data, '\n')
			}
		}
	}
	s.run(data, res, ctm, page, stack)
}

func (s *Scanner) run(data []byte, res syntax.Object, ctm Matrix, page int, stack []uint32) {
	rd, _ := s.r.Resolve(res).(*syntax.Dict)
	xobjects, _ := s.r.Resolve(rd.Get("XObject")).(*syntax.Dict)
	var saved []Matrix
	Ops(data, s.maxDepth, func(op string, args []syntax.Object) bool {
		s.ops++
		if s.ops > maxOps {
			return false
		}
		switch op {
		case "q":
			if len(saved) < 4096 {
				saved = append(saved, ctm)
			}
		case "Q":
			if n := len(saved); n > 0 {
				ctm = saved[n-1]
				saved = saved[:n-1]
			}
		case "cm":
			if len(args) == 6 {
				var m Matrix
				for i := range 6 {
					v, ok := num(args[i])
					if !ok {
						return true
					}
					m[i] = v
				}
				ctm = m.Mul(ctm)
			}
		case "Do":
			if len(args) < 1 {
				return true
			}
			name, _ := args[len(args)-1].(syntax.Name)
			ref := xobjects.Get(name)
			st, ok := s.r.Resolve(ref).(*syntax.Stream)
			if !ok {
				return true
			}
			switch sub, _ := st.Dict.Get("Subtype").(syntax.Name); sub {
			case "Image":
				if r, ok := ref.(syntax.Ref); ok {
					s.image(r.Num, ctm, page)
				}
			case "Form":
				s.form(ref, st, s.matrix(st.Dict.Get("Matrix")).Mul(ctm), page, stack, len(stack))
			}
		}
		return true
	})
}

// form runs a form XObject, unless it is already being run (a form that
// draws itself) or nesting is too deep.
func (s *Scanner) form(ref syntax.Object, st *syntax.Stream, ctm Matrix, page int, stack []uint32, depth int) {
	if depth >= maxFormDepth {
		return
	}
	if r, ok := ref.(syntax.Ref); ok {
		for _, n := range stack {
			if n == r.Num {
				return
			}
		}
		stack = append(stack, r.Num)
	}
	data, err := s.r.Decode(st)
	if err != nil && len(data) == 0 {
		return
	}
	res := st.Dict.Get("Resources")
	s.run(data, res, ctm, page, stack)
}

// image records that image num is drawn with transformation ctm: the unit
// square of the image maps to a parallelogram whose sides give its size.
func (s *Scanner) image(num uint32, ctm Matrix, page int) {
	w := math.Hypot(ctm[0], ctm[1])
	h := math.Hypot(ctm[2], ctm[3])
	if math.IsNaN(w+h) || math.IsInf(w+h, 0) {
		return
	}
	u, ok := s.Uses[num]
	if !ok {
		u = &Use{Page: page}
		s.Uses[num] = u
	}
	u.W = math.Max(u.W, w)
	u.H = math.Max(u.H, h)
}

func (s *Scanner) matrix(o syntax.Object) Matrix {
	a, ok := s.r.Resolve(o).(syntax.Array)
	if !ok || len(a) != 6 {
		return Identity
	}
	var m Matrix
	for i := range 6 {
		v, ok := num(s.r.Resolve(a[i]))
		if !ok {
			return Identity
		}
		m[i] = v
	}
	return m
}

func num(o syntax.Object) (float64, bool) {
	switch v := o.(type) {
	case syntax.Int:
		return float64(v), true
	case syntax.Real:
		return float64(v), true
	}
	return 0, false
}

func box(m Matrix, r reader.Rect) reader.Rect {
	pt := func(x, y float64) (float64, float64) { return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5] }
	x0, y0 := pt(r.X0, r.Y0)
	out := reader.Rect{X0: x0, Y0: y0, X1: x0, Y1: y0}
	for _, c := range [][2]float64{{r.X1, r.Y0}, {r.X0, r.Y1}, {r.X1, r.Y1}} {
		x, y := pt(c[0], c[1])
		out.X0, out.X1 = math.Min(out.X0, x), math.Max(out.X1, x)
		out.Y0, out.Y1 = math.Min(out.Y0, y), math.Max(out.Y1, y)
	}
	return out
}

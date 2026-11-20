package pdfo

import (
	"github.com/wisebrian/pdfo/internal/syntax"
)

// planAnnots allocates the copies of the annotations on pages that appear
// once, before any page is copied, so that whatever refers to an
// annotation (a popup, a form field, the structure tree) can map to it.
func (a *assembler) planAnnots() {
	for _, op := range a.out {
		if !op.unique {
			continue
		}
		s := op.src
		annots, _ := s.r.Resolve(op.page.Dict.Get("Annots")).(syntax.Array)
		for _, x := range annots {
			ref, ok := x.(syntax.Ref)
			if !ok {
				continue
			}
			ad, ok := s.r.Resolve(ref).(*syntax.Dict)
			if !ok || a.flattens(s, ad) {
				continue
			}
			if _, done := s.annots[ref.Num]; !done {
				s.annots[ref.Num] = a.w.Alloc()
			}
		}
	}
}

// flattens reports whether a widget annotation is drawn into the page
// rather than kept: signature fields always (a rewritten file cannot keep a
// valid signature, but the signature's appearance must stay visible), and
// every widget of a form that does not stay interactive.
func (a *assembler) flattens(s *source, ad *syntax.Dict) bool {
	if st, _ := ad.Get("Subtype").(syntax.Name); st != "Widget" {
		return false
	}
	if ft, _ := inheritedField(s.r, ad, "FT", s.doc.limits.MaxDepth).(syntax.Name); ft == "Sig" {
		return true
	}
	return !s.interactive
}

// copyAnnots copies a page's annotations and returns them, with the
// content that draws the widgets being flattened. res is the page's own
// copy of its resources, where flattened appearances are added.
func (a *assembler) copyAnnots(op *outPage, res *syntax.Dict) (syntax.Array, [][]byte) {
	s := op.src
	annots, _ := s.r.Resolve(op.page.Dict.Get("Annots")).(syntax.Array)
	// Pages that appear more than once get fresh copies of their
	// annotations each time, with references among them kept within the
	// page.
	local := map[uint32]syntax.Ref{}
	if !op.unique {
		for _, x := range annots {
			if ref, ok := x.(syntax.Ref); ok {
				if ad, ok := s.r.Resolve(ref).(*syntax.Dict); ok && !a.flattens(s, ad) {
					local[ref.Num] = a.w.Alloc()
				}
			}
		}
	}
	var out syntax.Array
	var extra [][]byte
	flat := 0
	for _, x := range annots {
		ad, ok := s.r.Resolve(x).(*syntax.Dict)
		if !ok {
			continue
		}
		if a.flattens(s, ad) {
			if c := a.flatten(op, ad, res, &flat); c != nil {
				extra = append(extra, c)
			}
			continue
		}
		var dst syntax.Ref
		if ref, ok := x.(syntax.Ref); ok {
			if op.unique {
				dst = s.annots[ref.Num]
			} else {
				dst = local[ref.Num]
			}
		}
		if dst.IsZero() {
			dst = a.w.Alloc()
		}
		a.w.Put(dst, a.annotDict(op, ad, local))
		out = append(out, dst)
	}
	return out, extra
}

// annotDict copies an annotation dictionary for the page op.
func (a *assembler) annotDict(op *outPage, ad *syntax.Dict, local map[uint32]syntax.Ref) *syntax.Dict {
	s := op.src
	mapAnnot := func(o syntax.Object) syntax.Object {
		ref, ok := o.(syntax.Ref)
		if !ok {
			return nil
		}
		if d, ok := local[ref.Num]; ok {
			return d
		}
		if op.unique {
			if d, ok := s.annots[ref.Num]; ok {
				return d
			}
		}
		return nil
	}
	sub, _ := ad.Get("Subtype").(syntax.Name)
	out := new(syntax.Dict)
	inert := false
	for i := range ad.Len() {
		k, v := ad.At(i)
		switch k {
		case "P":
			out.Set("P", op.ref)
		case "Popup", "IRT":
			out.Set(k, mapAnnot(v))
		case "Parent":
			if sub == "Widget" {
				out.Set(k, a.copy(s, v)) // the field, mapped through s.fields
			} else {
				out.Set(k, mapAnnot(v)) // a popup's markup annotation
			}
		case "Dest":
			if d := a.fixDest(s, v); d != nil {
				out.Set(k, d)
			} else {
				inert = true
			}
		case "A":
			if act := a.fixAction(s, v, 0); act != nil {
				out.Set(k, act)
			} else if isGoTo(s, v) {
				inert = true
			}
		case "AA":
			out.Set(k, a.fixActions(s, v))
		case "StructParent":
			if s.keepStruct {
				out.Set(k, v)
			}
		default:
			out.Set(k, a.copy(s, v))
		}
	}
	if inert {
		a.warn.add("links-inert", "links to pages that are not in the output no longer go anywhere", 0)
	}
	if op.resized {
		transformAnnot(out, op.m)
	}
	return out
}

func isGoTo(s *source, o syntax.Object) bool {
	d, _ := s.r.Resolve(o).(*syntax.Dict)
	t, _ := d.Get("S").(syntax.Name)
	return t == "GoTo"
}

// fixDest maps a destination (section 12.3.2) to the output: named
// destinations become explicit, pages map to their first copy, and
// coordinates follow a resized page. It returns nil when the page is not in
// the output.
func (a *assembler) fixDest(s *source, d syntax.Object) syntax.Object {
	return a.fixDestDepth(s, d, 0)
}

func (a *assembler) fixDestDepth(s *source, d syntax.Object, depth int) syntax.Object {
	if depth > 4 {
		return nil
	}
	switch v := s.r.Resolve(d).(type) {
	case syntax.Name:
		return a.fixDestDepth(s, s.namedDest(string(v)), depth+1)
	case syntax.String:
		return a.fixDestDepth(s, s.namedDest(string(v)), depth+1)
	case *syntax.Dict:
		return a.fixDestDepth(s, v.Get("D"), depth+1)
	case syntax.Array:
		if len(v) == 0 {
			return nil
		}
		var op *outPage
		switch p := v[0].(type) {
		case syntax.Ref:
			if i, ok := s.doc.byRef[p.Num]; ok {
				op = s.first[i]
			}
		case syntax.Int:
			// A page number where a reference belongs: some writers do it.
			if p >= 0 && int(p) < s.doc.NumPages() {
				op = s.first[int(p)]
			}
		}
		if op == nil {
			return nil
		}
		out := syntax.Array{op.ref}
		for _, x := range v[1:] {
			out = append(out, a.copy(s, s.r.Resolve(x)))
		}
		if op.resized {
			transformDest(out, op.m)
		}
		return out
	}
	return nil
}

// transformDest moves the coordinates of an explicit destination with its
// page's content.
func transformDest(d syntax.Array, m matrix) {
	if len(d) < 2 {
		return
	}
	kind, _ := d[1].(syntax.Name)
	x := func(i int) {
		if i < len(d) {
			if v, ok := numOf(d[i]); ok {
				d[i] = syntax.Real(m[0]*v + m[4])
			}
		}
	}
	y := func(i int) {
		if i < len(d) {
			if v, ok := numOf(d[i]); ok {
				d[i] = syntax.Real(m[3]*v + m[5])
			}
		}
	}
	switch kind {
	case "XYZ":
		x(2)
		y(3)
	case "FitH", "FitBH":
		y(2)
	case "FitV", "FitBV":
		x(2)
	case "FitR":
		x(2)
		y(3)
		x(4)
		y(5)
	}
}

func numOf(o syntax.Object) (float64, bool) {
	switch v := o.(type) {
	case syntax.Int:
		return float64(v), true
	case syntax.Real:
		return float64(v), true
	}
	return 0, false
}

// fixAction copies an action, fixing GoTo destinations and dropping what
// cannot be kept: GoTo actions to dropped pages, and JavaScript when the
// output combines documents. It returns nil when nothing is left.
func (a *assembler) fixAction(s *source, o syntax.Object, depth int) syntax.Object {
	if depth > 16 {
		return nil
	}
	act, ok := s.r.Resolve(o).(*syntax.Dict)
	if !ok {
		return nil
	}
	out := new(syntax.Dict)
	kind, _ := act.Get("S").(syntax.Name)
	switch kind {
	case "GoTo":
		d := a.fixDest(s, act.Get("D"))
		if d == nil {
			return nil
		}
		for i := range act.Len() {
			k, v := act.At(i)
			if k != "D" && k != "Next" {
				out.Set(k, a.copy(s, v))
			}
		}
		out.Set("D", d)
	case "JavaScript":
		if !s.keepJS {
			a.warn.add("javascript-dropped", "scripts were removed because they belong to one of several documents", 0)
			return nil
		}
		fallthrough
	default:
		for i := range act.Len() {
			k, v := act.At(i)
			if k != "Next" {
				out.Set(k, a.copy(s, v))
			}
		}
	}
	switch next := s.r.Resolve(act.Get("Next")).(type) {
	case *syntax.Dict:
		if n := a.fixAction(s, next, depth+1); n != nil {
			out.Set("Next", n)
		}
	case syntax.Array:
		var list syntax.Array
		for _, n := range next {
			if f := a.fixAction(s, n, depth+1); f != nil {
				list = append(list, f)
			}
		}
		if len(list) > 0 {
			out.Set("Next", list)
		}
	}
	return out
}

// fixActions copies an additional-actions dictionary, action by action.
func (a *assembler) fixActions(s *source, o syntax.Object) syntax.Object {
	aa, ok := s.r.Resolve(o).(*syntax.Dict)
	if !ok {
		return nil
	}
	out := new(syntax.Dict)
	for i := range aa.Len() {
		k, v := aa.At(i)
		out.Set(k, a.fixAction(s, v, 0))
	}
	if out.Len() == 0 {
		return nil
	}
	return out
}

// transformAnnot moves an annotation with its resized page: its rectangle
// and every other coordinate it holds, so links stay on their text and
// markup on what it marks.
func transformAnnot(d *syntax.Dict, m matrix) {
	if r, ok := rectOf(d.Get("Rect")); ok {
		d.Set("Rect", m.rect(r).Array())
	}
	points := func(a syntax.Array) syntax.Array {
		out := make(syntax.Array, len(a))
		for i := 0; i+1 < len(a); i += 2 {
			x, ok1 := numOf(a[i])
			y, ok2 := numOf(a[i+1])
			if !ok1 || !ok2 {
				out[i], out[i+1] = a[i], a[i+1]
				continue
			}
			px, py := m.point(x, y)
			out[i], out[i+1] = syntax.Real(px), syntax.Real(py)
		}
		if len(a)%2 == 1 {
			out[len(a)-1] = a[len(a)-1]
		}
		return out
	}
	for _, k := range []syntax.Name{"QuadPoints", "Vertices", "L", "CL"} {
		if a, ok := d.Get(k).(syntax.Array); ok {
			d.Set(k, points(a))
		}
	}
	if ink, ok := d.Get("InkList").(syntax.Array); ok {
		out := make(syntax.Array, len(ink))
		for i, x := range ink {
			if a, ok := x.(syntax.Array); ok {
				out[i] = points(a)
			} else {
				out[i] = x
			}
		}
		d.Set("InkList", out)
	}
	if rd, ok := d.Get("RD").(syntax.Array); ok {
		out := make(syntax.Array, len(rd))
		for i, x := range rd {
			if v, ok := numOf(x); ok {
				out[i] = syntax.Real(v * m[0])
			} else {
				out[i] = x
			}
		}
		d.Set("RD", out)
	}
}

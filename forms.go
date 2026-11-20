package pdfo

import (
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/wisebrian/pdfo/internal/reader"
	"github.com/wisebrian/pdfo/internal/syntax"
)

// planForms decides, for each source, whether its form stays interactive,
// and if so which fields are kept.
//
// A form stays a form when its document is the only one in the output with
// a form, and none of its pages appears twice. Otherwise field names could
// collide between documents, or one field would have two widgets meant to
// be different; its widgets are flattened instead: their appearances are
// drawn into the page, so filled-in values stay visible.
func (a *assembler) planForms() {
	withForm := 0
	for _, s := range a.srcs {
		if hasFields(s) {
			withForm++
		}
	}
	for _, s := range a.srcs {
		if !hasFields(s) {
			continue
		}
		unique := true
		for _, n := range s.count {
			if n > 1 {
				unique = false
			}
		}
		s.interactive = withForm == 1 && unique
		if !s.interactive {
			a.warn.add("form-flattened", "form fields were drawn into their pages and can no longer be filled in", 0)
			continue
		}
		kept := map[uint32]bool{}
		for _, op := range a.out {
			if op.src != s {
				continue
			}
			annots, _ := s.r.Resolve(op.page.Dict.Get("Annots")).(syntax.Array)
			for _, x := range annots {
				if ref, ok := x.(syntax.Ref); ok {
					kept[ref.Num] = true
				}
			}
		}
		form, _ := s.r.Resolve(s.r.Catalog().Get("AcroForm")).(*syntax.Dict)
		fields, _ := s.r.Resolve(form.Get("Fields")).(syntax.Array)
		seen := map[uint32]bool{}
		var keep func(o syntax.Object, depth int) bool
		keep = func(o syntax.Object, depth int) bool {
			ref, ok := o.(syntax.Ref)
			if !ok || depth > s.doc.limits.MaxDepth || seen[ref.Num] {
				return false
			}
			seen[ref.Num] = true
			f, ok := s.r.Resolve(ref).(*syntax.Dict)
			if !ok {
				return false
			}
			if ft, _ := inheritedField(s.r, f, "FT", s.doc.limits.MaxDepth).(syntax.Name); ft == "Sig" {
				return false // signatures are drawn into the page
			}
			if st, _ := f.Get("Subtype").(syntax.Name); st == "Widget" {
				return kept[ref.Num] // its copy is made with the page
			}
			any := false
			kids, _ := s.r.Resolve(f.Get("Kids")).(syntax.Array)
			for _, k := range kids {
				if keep(k, depth+1) {
					any = true
				}
			}
			if any {
				s.fields[ref.Num] = a.w.Alloc()
			}
			return any
		}
		for _, f := range fields {
			keep(f, 0)
		}
	}
}

func hasFields(s *source) bool {
	form, _ := s.r.Resolve(s.r.Catalog().Get("AcroForm")).(*syntax.Dict)
	fields, _ := s.r.Resolve(form.Get("Fields")).(syntax.Array)
	return len(fields) > 0
}

// form writes the kept fields of the interactive source and returns its
// AcroForm dictionary, or nil.
func (a *assembler) form() syntax.Object {
	for _, s := range a.srcs {
		if !s.interactive {
			continue
		}
		form, _ := s.r.Resolve(s.r.Catalog().Get("AcroForm")).(*syntax.Dict)
		mapped := func(o syntax.Object) syntax.Object {
			ref, ok := o.(syntax.Ref)
			if !ok {
				return nil
			}
			if d, ok := s.annots[ref.Num]; ok {
				return d
			}
			if d, ok := s.fields[ref.Num]; ok {
				return d
			}
			return nil
		}
		// Fields that are not widgets themselves, with their kids pruned,
		// in number order: copying allocates objects, and the order of
		// allocation is the numbering of the file.
		for _, num := range slices.Sorted(maps.Keys(s.fields)) {
			dst := s.fields[num]
			f, _ := s.r.Object(num).(*syntax.Dict)
			out := new(syntax.Dict)
			for i := range f.Len() {
				k, v := f.At(i)
				switch k {
				case "Kids":
					kids, _ := s.r.Resolve(v).(syntax.Array)
					var list syntax.Array
					for _, kid := range kids {
						if m := mapped(kid); m != nil {
							list = append(list, m)
						}
					}
					out.Set(k, list)
				case "Parent":
					out.Set(k, mapped(v))
				case "AA":
					out.Set(k, a.fixActions(s, v))
				default:
					out.Set(k, a.copy(s, v))
				}
			}
			a.w.Put(dst, out)
		}
		out := new(syntax.Dict)
		fields, _ := s.r.Resolve(form.Get("Fields")).(syntax.Array)
		var list syntax.Array
		for _, f := range fields {
			if m := mapped(f); m != nil {
				list = append(list, m)
			}
		}
		out.Set("Fields", list)
		for i := range form.Len() {
			k, v := form.At(i)
			switch k {
			case "Fields":
			case "XFA":
				a.warn.add("xfa-dropped", "the XFA version of the form was removed; the standard form fields remain", 0)
			case "SigFlags":
				// Signatures do not survive rewriting.
			case "CO":
				co, _ := s.r.Resolve(v).(syntax.Array)
				var keep syntax.Array
				for _, x := range co {
					if m := mapped(x); m != nil {
						keep = append(keep, m)
					}
				}
				if len(keep) > 0 {
					out.Set(k, keep)
				}
			default:
				out.Set(k, a.copy(s, v))
			}
		}
		return out
	}
	return nil
}

// flatten returns the content that draws a widget's appearance where the
// widget was, adding the appearance to res, the page's resources: the
// transformation of section 12.5.5, which maps the appearance's bounding
// box, as its own matrix moves it, onto the annotation's rectangle. It
// returns nil when there is nothing visible to draw.
func (a *assembler) flatten(op *outPage, ad *syntax.Dict, res *syntax.Dict, n *int) []byte {
	s := op.src
	r := s.r
	if ft, _ := inheritedField(r, ad, "FT", s.doc.limits.MaxDepth).(syntax.Name); ft == "Sig" {
		a.warn.add("signature-removed", "a digital signature was removed: a changed document can no longer carry it; its appearance stays on the page", 0)
	}
	flags, _ := r.Resolve(ad.Get("F")).(syntax.Int)
	if flags&2 != 0 || flags&32 != 0 { // Hidden, NoView
		return nil
	}
	ap, _ := r.Resolve(ad.Get("AP")).(*syntax.Dict)
	normal := ap.Get("N")
	var ref syntax.Ref
	on := false
	switch v := r.Resolve(normal).(type) {
	case *syntax.Stream:
		ref, _ = normal.(syntax.Ref)
	case *syntax.Dict:
		state := widgetState(r, ad, v, s.doc.limits.MaxDepth)
		on = state != "Off"
		ref, _ = v.Get(state).(syntax.Ref)
	}
	if !on {
		if v, _ := inheritedField(r, ad, "V", s.doc.limits.MaxDepth).(syntax.Name); v != "" && v != "Off" {
			on = true
		}
	}
	form, ok := r.Resolve(ref).(*syntax.Stream)
	if !ok || ref.IsZero() || emptyAppearance(r, form, s.doc.limits.MaxDepth) {
		// No appearance to draw, or an empty one in a form that left
		// drawing to the viewer: draw the value here.
		rect, ok := r.Rect(ad.Get("Rect"))
		if !ok {
			return nil
		}
		made := a.makeAppearance(s, ad, rect, on)
		if made == nil {
			if inheritedField(r, ad, "V", s.doc.limits.MaxDepth) != nil {
				a.warn.add("form-value-not-visible", "a form field has a value but no appearance to draw it with, so its value is not visible", 0)
			}
			return nil
		}
		return a.drawForm(res, n, made, rect, reader.Rect{X0: 0, Y0: 0, X1: rect.W(), Y1: rect.H()}, identity)
	}
	rect, ok1 := r.Rect(ad.Get("Rect"))
	bbox, ok2 := r.Rect(form.Dict.Get("BBox"))
	if !ok1 || !ok2 {
		return nil
	}
	fm := identity
	if ma, ok := r.Resolve(form.Dict.Get("Matrix")).(syntax.Array); ok && len(ma) == 6 {
		for i := range 6 {
			if v, ok := r.Number(ma[i]); ok {
				fm[i] = v
			}
		}
	}
	s.formize[ref.Num] = true
	return a.drawForm(res, n, a.ref(s, ref), rect, bbox, fm)
}

// drawForm adds a form XObject to res under a fresh name and returns the
// content that draws it with its bounding box, as its matrix moves it,
// fitted to rect.
func (a *assembler) drawForm(res *syntax.Dict, n *int, form syntax.Object, rect, bbox reader.Rect, fm matrix) []byte {
	tb := transformBox(fm, bbox)
	if tb.W() <= 0 || tb.H() <= 0 {
		return nil
	}
	sx, sy := rect.W()/tb.W(), rect.H()/tb.H()
	tx, ty := rect.X0-tb.X0*sx, rect.Y0-tb.Y0*sy
	if math.IsNaN(sx+sy+tx+ty) || math.IsInf(sx+sy+tx+ty, 0) {
		return nil
	}
	xo, _ := res.Get("XObject").(*syntax.Dict)
	if xo == nil {
		xo = new(syntax.Dict)
		res.Set("XObject", xo)
	}
	var name syntax.Name
	for {
		name = syntax.Name("PdfoField" + strconv.Itoa(*n))
		*n++
		if xo.Get(name) == nil {
			break
		}
	}
	xo.Set(name, form)
	b := []byte("q ")
	b = appendNums(b, sx, 0, 0, sy, tx, ty)
	b = append(b, " cm "...)
	b = syntax.AppendName(b, name)
	return append(b, " Do Q\n"...)
}

// transformBox returns the bounding box of a rectangle under any matrix.
func transformBox(m matrix, r reader.Rect) reader.Rect {
	xs := [4]float64{}
	ys := [4]float64{}
	xs[0], ys[0] = m.point(r.X0, r.Y0)
	xs[1], ys[1] = m.point(r.X1, r.Y0)
	xs[2], ys[2] = m.point(r.X0, r.Y1)
	xs[3], ys[3] = m.point(r.X1, r.Y1)
	out := reader.Rect{X0: xs[0], Y0: ys[0], X1: xs[0], Y1: ys[0]}
	for i := 1; i < 4; i++ {
		out.X0, out.X1 = math.Min(out.X0, xs[i]), math.Max(out.X1, xs[i])
		out.Y0, out.Y1 = math.Min(out.Y0, ys[i]), math.Max(out.Y1, ys[i])
	}
	return out
}

func rectOf(o syntax.Object) (reader.Rect, bool) {
	a, ok := o.(syntax.Array)
	if !ok || len(a) < 4 {
		return reader.Rect{}, false
	}
	var v [4]float64
	for i := range 4 {
		f, ok := numOf(a[i])
		if !ok {
			return reader.Rect{}, false
		}
		v[i] = f
	}
	return reader.Rect{X0: math.Min(v[0], v[2]), Y0: math.Min(v[1], v[3]), X1: math.Max(v[0], v[2]), Y1: math.Max(v[1], v[3])}, true
}

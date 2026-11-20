package reader

import (
	"math"

	"github.com/wisebrian/pdfo/internal/syntax"
)

// Rect is a rectangle normalised so that X0 ≤ X1 and Y0 ≤ Y1.
type Rect struct {
	X0, Y0, X1, Y1 float64 // lower left, then upper right
}

// W returns the width.
func (r Rect) W() float64 { return r.X1 - r.X0 }

// H returns the height.
func (r Rect) H() float64 { return r.Y1 - r.Y0 }

// Intersect returns the part of r inside s, or r if they do not overlap:
// a crop box outside the media box is ignored, as viewers ignore it.
func (r Rect) Intersect(s Rect) Rect {
	o := Rect{max(r.X0, s.X0), max(r.Y0, s.Y0), min(r.X1, s.X1), min(r.Y1, s.Y1)}
	if o.X1 <= o.X0 || o.Y1 <= o.Y0 {
		return s
	}
	return o
}

// Array returns the rectangle as a PDF array.
func (r Rect) Array() syntax.Array {
	return syntax.Array{num(r.X0), num(r.Y0), num(r.X1), num(r.Y1)}
}

func num(v float64) syntax.Object {
	if v == math.Trunc(v) && math.Abs(v) < 1<<31 {
		return syntax.Int(v)
	}
	return syntax.Real(v)
}

// letter is the media box viewers assume when a page has none.
var letter = Rect{0, 0, 612, 792}

// Page is a page with its inherited attributes resolved.
type Page struct {
	// Ref is the page object, zero for a page that is (invalidly) a direct
	// object.
	Ref  syntax.Ref
	Dict *syntax.Dict // the page dictionary
	// Resources, MediaBox, CropBox and Rotate may be inherited from the page
	// tree; these are the values that apply.
	Resources syntax.Object
	MediaBox  Rect    // letter size when missing, as viewers assume
	CropBox   Rect    // within MediaBox
	Rotate    int     // 0, 90, 180 or 270
	UserUnit  float64 // points per unit, 1 unless the page sets it
}

// Pages walks the page tree. Inherited attributes are resolved on the way
// down, so /Parent is never followed. A node seen twice (a /Kids loop, or a
// page listed twice) is skipped. When the tree yields no page at all, the
// pages are found by type instead, as pdf.js does for broken trees.
func (r *Reader) Pages() []*Page {
	r.treeNodes = map[uint32]bool{}
	var pages []*Page
	visited := map[uint32]bool{}
	type inherit struct {
		res      syntax.Object
		media    syntax.Object
		crop     syntax.Object
		rotate   syntax.Object
		userUnit syntax.Object
	}
	var walk func(node syntax.Object, in inherit, depth int)
	walk = func(node syntax.Object, in inherit, depth int) {
		if depth > r.limits.MaxDepth {
			r.warn("page tree nested too deeply")
			return
		}
		ref, isRef := node.(syntax.Ref)
		if isRef {
			if visited[ref.Num] {
				r.warn("page tree visits an object twice")
				return
			}
			visited[ref.Num] = true
		}
		d, ok := r.Resolve(node).(*syntax.Dict)
		if !ok {
			return
		}
		pick := func(k syntax.Name, def syntax.Object) syntax.Object {
			if v := d.Get(k); v != nil {
				return v
			}
			return def
		}
		in = inherit{
			res:      pick("Resources", in.res),
			media:    pick("MediaBox", in.media),
			crop:     pick("CropBox", in.crop),
			rotate:   pick("Rotate", in.rotate),
			userUnit: pick("UserUnit", in.userUnit),
		}
		t, _ := d.Get("Type").(syntax.Name)
		kids, hasKids := r.Resolve(d.Get("Kids")).(syntax.Array)
		if t == "Pages" || (t != "Page" && hasKids) {
			if isRef {
				r.treeNodes[ref.Num] = true
			}
			for _, k := range kids {
				walk(k, in, depth+1)
			}
			return
		}
		if !isRef {
			ref = syntax.Ref{}
		}
		pages = append(pages, r.page(ref, d, in.res, in.media, in.crop, in.rotate, in.userUnit))
	}
	cat := r.Catalog()
	walk(cat.Get("Pages"), inherit{}, 0)
	if len(pages) == 0 {
		r.warn("page tree has no pages; finding pages by type")
		for _, n := range r.Nums() {
			d, ok := r.Object(n).(*syntax.Dict)
			if !ok {
				continue
			}
			if t, _ := d.Get("Type").(syntax.Name); t == "Page" {
				pages = append(pages, r.page(syntax.Ref{Num: n}, d, d.Get("Resources"), d.Get("MediaBox"), d.Get("CropBox"), d.Get("Rotate"), d.Get("UserUnit")))
			}
		}
	}
	return pages
}

// IsTreeNode reports whether object num is an intermediate node of the
// page tree, as found by the last call to Pages.
func (r *Reader) IsTreeNode(num uint32) bool { return r.treeNodes[num] }

func (r *Reader) page(ref syntax.Ref, d *syntax.Dict, res, media, crop, rotate, unit syntax.Object) *Page {
	p := &Page{Ref: ref, Dict: d, Resources: res, UserUnit: 1}
	m, ok := r.rect(media)
	if !ok {
		m = letter
	}
	p.MediaBox = m
	p.CropBox = m
	if c, ok := r.rect(crop); ok {
		p.CropBox = c.Intersect(m)
	}
	if v, ok := number(r.Resolve(rotate)); ok && v == math.Trunc(v) && math.Abs(v) < 1e9 {
		p.Rotate = ((int(v)%360 + 360) % 360) / 90 * 90
	}
	if v, ok := number(r.Resolve(unit)); ok && v > 0 && v < 1e6 {
		p.UserUnit = v
	}
	return p
}

// Rect reads a rectangle, reporting false when o is not one or has no area.
func (r *Reader) rect(o syntax.Object) (Rect, bool) {
	a, ok := r.Resolve(o).(syntax.Array)
	if !ok || len(a) < 4 {
		return Rect{}, false
	}
	var v [4]float64
	for i := range 4 {
		f, ok := number(r.Resolve(a[i]))
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return Rect{}, false
		}
		v[i] = f
	}
	rc := Rect{min(v[0], v[2]), min(v[1], v[3]), max(v[0], v[2]), max(v[1], v[3])}
	if rc.W() <= 0 || rc.H() <= 0 {
		return Rect{}, false
	}
	return rc, true
}

// Rect reads a rectangle from the file, as for page boxes.
func (r *Reader) Rect(o syntax.Object) (Rect, bool) { return r.rect(o) }

// Number reads a number, resolving references.
func (r *Reader) Number(o syntax.Object) (float64, bool) { return number(r.Resolve(o)) }

func number(o syntax.Object) (float64, bool) {
	switch v := o.(type) {
	case syntax.Int:
		return float64(v), true
	case syntax.Real:
		return float64(v), true
	}
	return 0, false
}

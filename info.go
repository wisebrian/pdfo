package pdfo

import (
	"math"

	"github.com/wisebrian/pdfo/internal/reader"
	"github.com/wisebrian/pdfo/internal/syntax"
)

// Info is what Document.Info reports.
type Info struct {
	// Version is the PDF version; a catalog /Version later than the header
	// wins, as the specification says.
	Version string
	// Title and Producer come from the document information dictionary.
	Title, Producer string
	// Encrypted is set when the file was encrypted; Encryption then names
	// the method: "RC4 40-bit", "RC4 128-bit", "AES-128" or "AES-256".
	Encrypted bool
	// Encryption names the method, empty when not encrypted.
	Encryption string
	// Pages describes every page, in order.
	Pages []PageInfo

	// Bookmarks, FormFields, Attachments and JavaScript say whether the
	// document has them; actions that cannot keep them warn when they drop
	// them.
	Bookmarks, FormFields, Attachments, JavaScript bool
	// Signed is reported because any change invalidates a digital
	// signature; the UI should say so before the user acts.
	Signed bool

	// Images is the number of distinct image XObjects the pages use, and
	// ImageBytes their stored, compressed, size.
	Images int
	// ImageBytes is the stored size of those images, in bytes.
	ImageBytes int64

	// Repaired says the cross-reference data had to be rebuilt: the file
	// is damaged, even if every page was recovered.
	Repaired bool
	// Warnings lists problems found while reading.
	Warnings []Warning
}

// PageInfo describes one page as a viewer shows it.
type PageInfo struct {
	// Width and Height are the visible area (CropBox clipped to MediaBox,
	// times UserUnit) after Rotate, so a turned page reads as landscape.
	Width, Height float64 // points
	// WidthMM and HeightMM are the same in millimetres, for display.
	WidthMM, HeightMM float64
	// SizeName is the paper size the page matches, such as "A4", or "".
	SizeName string
	// Rotate is the page's rotation: 0, 90, 180 or 270, clockwise.
	Rotate int
}

// maxTreeItems bounds the entries read from a name tree or a field tree
// while gathering information, so a huge tree costs bounded time.
const maxTreeItems = 1 << 20

// Info describes the document for the UI to show before any action. It
// decodes no page content or image, so it stays cheap on large files.
func (d *Document) Info() *Info {
	r := d.r
	info := &Info{
		Version:  d.version(),
		Repaired: r.Repaired,
		Warnings: []Warning{},
	}
	if dict, ok := r.Resolve(r.Trailer.Get("Info")).(*syntax.Dict); ok {
		info.Title = text(r, dict.Get("Title"))
		info.Producer = text(r, dict.Get("Producer"))
	}
	if r.Crypt != nil {
		info.Encrypted = true
		info.Encryption = r.Crypt.Description
	}
	for _, p := range d.pages {
		w, h := visibleSize(p)
		info.Pages = append(info.Pages, PageInfo{
			Width: round2(w), Height: round2(h),
			WidthMM: round2(ToMM(w)), HeightMM: round2(ToMM(h)),
			SizeName: SizeName(Size{w, h}),
			Rotate:   p.Rotate,
		})
	}
	cat := r.Catalog()
	if o, ok := r.Resolve(cat.Get("Outlines")).(*syntax.Dict); ok && o.Get("First") != nil {
		info.Bookmarks = true
	}
	if form, ok := r.Resolve(cat.Get("AcroForm")).(*syntax.Dict); ok {
		fields, _ := r.Resolve(form.Get("Fields")).(syntax.Array)
		info.FormFields = len(fields) > 0 || form.Get("XFA") != nil
		if f, ok := r.Resolve(form.Get("SigFlags")).(syntax.Int); ok && f&1 != 0 {
			info.Signed = true
		}
		eachField(r, fields, d.limits.MaxDepth, func(f *syntax.Dict) {
			if ft, _ := inheritedField(r, f, "FT", d.limits.MaxDepth).(syntax.Name); ft == "Sig" && f.Get("V") != nil {
				info.Signed = true
			}
			if hasJS(r, f.Get("AA"), 0) {
				info.JavaScript = true
			}
		})
	}
	names, _ := r.Resolve(cat.Get("Names")).(*syntax.Dict)
	eachName(r, names.Get("EmbeddedFiles"), d.limits.MaxDepth, func(syntax.String, syntax.Object) bool {
		info.Attachments = true
		return false
	})
	eachName(r, names.Get("JavaScript"), d.limits.MaxDepth, func(syntax.String, syntax.Object) bool {
		info.JavaScript = true
		return false
	})
	if hasJS(r, cat.Get("OpenAction"), 0) || hasJS(r, cat.Get("AA"), 0) {
		info.JavaScript = true
	}
	seen := map[uint32]bool{}
	for _, p := range d.pages {
		if hasJS(r, p.Dict.Get("AA"), 0) {
			info.JavaScript = true
		}
		annots, _ := r.Resolve(p.Dict.Get("Annots")).(syntax.Array)
		for _, a := range annots {
			ad, ok := r.Resolve(a).(*syntax.Dict)
			if !ok {
				continue
			}
			if st, _ := ad.Get("Subtype").(syntax.Name); st == "FileAttachment" {
				info.Attachments = true
			}
			if hasJS(r, ad.Get("A"), 0) || hasJS(r, ad.Get("AA"), 0) {
				info.JavaScript = true
			}
		}
		countImages(r, p.Resources, seen, info, 0, d.limits.MaxDepth)
	}
	if r.Repaired {
		info.Warnings = append(info.Warnings, Warning{
			Code: "file-repaired", Message: "the file is damaged; its cross-reference data was rebuilt", Count: 1,
		})
	}
	if n := len(r.Warnings); n > 0 {
		info.Warnings = append(info.Warnings, Warning{Code: "file-problems", Message: r.Warnings[0], Count: n})
	}
	return info
}

// visibleSize returns the size a viewer shows: the crop box, in points
// (times UserUnit), turned by the page's rotation.
func visibleSize(p *reader.Page) (float64, float64) {
	w, h := p.CropBox.W()*p.UserUnit, p.CropBox.H()*p.UserUnit
	if p.Rotate%180 == 90 {
		w, h = h, w
	}
	return w, h
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func text(r *reader.Reader, o syntax.Object) string {
	s, _ := r.Resolve(o).(syntax.String)
	return syntax.Text(s)
}

// countImages adds the image XObjects in a resource dictionary, and in the
// form XObjects it uses, to the counts. seen keeps each image counted once
// and each form visited once.
func countImages(r *reader.Reader, res syntax.Object, seen map[uint32]bool, info *Info, depth, maxDepth int) {
	if depth > maxDepth {
		return
	}
	rd, _ := r.Resolve(res).(*syntax.Dict)
	xo, _ := r.Resolve(rd.Get("XObject")).(*syntax.Dict)
	for i := range xo.Len() {
		_, v := xo.At(i)
		ref, ok := v.(syntax.Ref)
		if !ok || seen[ref.Num] {
			continue
		}
		seen[ref.Num] = true
		s, ok := r.Resolve(ref).(*syntax.Stream)
		if !ok {
			continue
		}
		switch st, _ := s.Dict.Get("Subtype").(syntax.Name); st {
		case "Image":
			info.Images++
			info.ImageBytes += int64(len(s.Data))
		case "Form":
			countImages(r, s.Dict.Get("Resources"), seen, info, depth+1, maxDepth)
		}
	}
}

// hasJS reports whether an action, an additional-actions dictionary or
// an action chain holds JavaScript.
func hasJS(r *reader.Reader, o syntax.Object, depth int) bool {
	if depth > 16 {
		return false
	}
	switch v := r.Resolve(o).(type) {
	case *syntax.Dict:
		if s, _ := v.Get("S").(syntax.Name); s == "JavaScript" {
			return true
		}
		if v.Get("S") == nil { // an additional-actions dictionary
			for i := range v.Len() {
				_, a := v.At(i)
				if hasJS(r, a, depth+1) {
					return true
				}
			}
			return false
		}
		return hasJS(r, v.Get("Next"), depth+1)
	case syntax.Array:
		for _, a := range v {
			if hasJS(r, a, depth+1) {
				return true
			}
		}
	}
	return false
}

// eachName calls fn for the entries of a name tree (section 7.9.6), in
// order, until fn returns false. Loops and depth are bounded.
func eachName(r *reader.Reader, root syntax.Object, maxDepth int, fn func(syntax.String, syntax.Object) bool) {
	seen := map[uint32]bool{}
	count := 0
	var walk func(o syntax.Object, depth int) bool
	walk = func(o syntax.Object, depth int) bool {
		if depth > maxDepth {
			return true
		}
		if ref, ok := o.(syntax.Ref); ok {
			if seen[ref.Num] {
				return true
			}
			seen[ref.Num] = true
		}
		n, ok := r.Resolve(o).(*syntax.Dict)
		if !ok {
			return true
		}
		if names, ok := r.Resolve(n.Get("Names")).(syntax.Array); ok {
			for i := 0; i+1 < len(names); i += 2 {
				key, _ := r.Resolve(names[i]).(syntax.String)
				count++
				if count > maxTreeItems || !fn(key, names[i+1]) {
					return false
				}
			}
		}
		kids, _ := r.Resolve(n.Get("Kids")).(syntax.Array)
		for _, k := range kids {
			if !walk(k, depth+1) {
				return false
			}
		}
		return true
	}
	walk(root, 0)
}

// eachField calls fn for every field dictionary in a field tree.
func eachField(r *reader.Reader, fields syntax.Array, maxDepth int, fn func(*syntax.Dict)) {
	seen := map[uint32]bool{}
	count := 0
	var walk func(o syntax.Object, depth int)
	walk = func(o syntax.Object, depth int) {
		if depth > maxDepth || count > maxTreeItems {
			return
		}
		if ref, ok := o.(syntax.Ref); ok {
			if seen[ref.Num] {
				return
			}
			seen[ref.Num] = true
		}
		f, ok := r.Resolve(o).(*syntax.Dict)
		if !ok {
			return
		}
		count++
		fn(f)
		kids, _ := r.Resolve(f.Get("Kids")).(syntax.Array)
		for _, k := range kids {
			walk(k, depth+1)
		}
	}
	for _, f := range fields {
		walk(f, 0)
	}
}

// inheritedField returns a field attribute, looking up the /Parent chain as
// field attributes inherit (section 12.7.4.1), with loops bounded.
func inheritedField(r *reader.Reader, f *syntax.Dict, key syntax.Name, maxDepth int) syntax.Object {
	for range maxDepth {
		if f == nil {
			return nil
		}
		if v := f.Get(key); v != nil {
			return r.Resolve(v)
		}
		f, _ = r.Resolve(f.Get("Parent")).(*syntax.Dict)
	}
	return nil
}

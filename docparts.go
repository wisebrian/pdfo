package pdfo

import (
	"maps"
	"slices"
	"strconv"

	"github.com/wisebrian/pdfo/internal/syntax"
)

// catalog writes the document-level parts of the output and returns its
// catalog. What is kept follows the mode; see the package documentation.
func (a *assembler) catalog() *syntax.Dict {
	cat := new(syntax.Dict)
	cat.Set("Type", syntax.Name("Catalog"))
	cat.Set("Pages", a.pagesRef)
	first := a.srcs[0]
	fc := first.r.Catalog()

	if o := a.outlines(); o != nil {
		cat.Set("Outlines", o)
	}
	if f := a.form(); f != nil {
		cat.Set("AcroForm", f)
	}
	if names := a.names(); names.Len() > 0 {
		cat.Set("Names", names)
	}
	if oc := a.optionalContent(); oc != nil {
		cat.Set("OCProperties", oc)
	}

	// The opening action and document scripts belong to one document.
	if a.mode != modeMulti {
		if oa := fc.Get("OpenAction"); oa != nil {
			var fixed syntax.Object
			if _, isArray := first.r.Resolve(oa).(syntax.Array); isArray {
				fixed = a.fixDest(first, oa)
			} else {
				fixed = a.fixAction(first, oa, 0)
			}
			if fixed != nil {
				cat.Set("OpenAction", fixed)
			}
		}
		if aa := a.fixActions(first, fc.Get("AA")); aa != nil {
			cat.Set("AA", aa)
		}
	} else {
		for _, s := range a.srcs {
			c := s.r.Catalog()
			if hasJS(s.r, c.Get("OpenAction"), 0) || hasJS(s.r, c.Get("AA"), 0) {
				a.warn.add("javascript-dropped", "scripts were removed because they belong to one of several documents", 0)
			}
		}
	}

	// Structures tied to the exact page sequence survive only when it is
	// unchanged.
	for _, k := range []syntax.Name{"StructTreeRoot", "PageLabels", "Threads"} {
		if a.mode == modeSame {
			if v := fc.Get(k); v != nil {
				cat.Set(k, a.copy(first, v))
			}
			continue
		}
		present := false
		for _, s := range a.srcs {
			if s.r.Catalog().Get(k) != nil {
				present = true
			}
		}
		if !present {
			continue
		}
		switch k {
		case "StructTreeRoot":
			a.warn.add("tags-dropped", "the accessibility tags (logical structure) were removed", 0)
		case "PageLabels":
			a.warn.add("page-labels-dropped", "custom page numbering (page labels) was removed", 0)
		case "Threads":
			a.warn.add("articles-dropped", "article threads were removed", 0)
		}
	}
	if a.mode == modeSame {
		if v := fc.Get("MarkInfo"); v != nil {
			cat.Set("MarkInfo", a.copy(first, v))
		}
	}
	// Settings that describe the document as a whole come from the first.
	for _, k := range []syntax.Name{"Lang", "ViewerPreferences", "PageLayout", "PageMode", "Metadata", "OutputIntents", "URI"} {
		if v := fc.Get(k); v != nil {
			cat.Set(k, a.copy(first, v))
		}
	}
	if a.mode != modeMulti {
		if v := fc.Get("Collection"); v != nil {
			cat.Set("Collection", a.copy(first, v))
		}
	}
	// Bookmarks shown on opening need bookmarks.
	if pm, _ := cat.Get("PageMode").(syntax.Name); pm == "UseOutlines" && cat.Get("Outlines") == nil {
		cat.Delete("PageMode")
	}
	for _, s := range a.srcs {
		form, _ := s.r.Resolve(s.r.Catalog().Get("AcroForm")).(*syntax.Dict)
		if f, ok := s.r.Resolve(form.Get("SigFlags")).(syntax.Int); ok && f&1 != 0 {
			a.warn.add("signature-removed", "a digital signature was removed: a changed document can no longer carry it; its appearance stays on the page", 0)
		}
	}
	return cat
}

// info returns the output's information dictionary: the first source's,
// with Producer and the modification date from the options.
func (a *assembler) info() *syntax.Dict {
	first := a.srcs[0]
	d := new(syntax.Dict)
	if src, ok := first.r.Resolve(first.r.Trailer.Get("Info")).(*syntax.Dict); ok {
		for i := range src.Len() {
			k, v := src.At(i)
			// Values must be direct: the information dictionary of the
			// output is a new object.
			d.Set(k, a.copy(first, first.r.Resolve(v)))
		}
	}
	if a.opts.Producer != "" {
		d.Set("Producer", syntax.TextString(a.opts.Producer))
	}
	if a.opts.Timestamp != "" {
		date, _ := syntax.Date(a.opts.Timestamp)
		d.Set("ModDate", date)
	}
	if d.Len() == 0 {
		return nil
	}
	return d
}

// namedDest returns the destination a source names, from the catalog's
// Dests dictionary (PDF 1.1) or the Dests name tree.
func (s *source) namedDest(name string) syntax.Object {
	if s.named == nil {
		s.named = map[string]syntax.Object{}
		cat := s.r.Catalog()
		if d, ok := s.r.Resolve(cat.Get("Dests")).(*syntax.Dict); ok {
			for i := range min(d.Len(), maxTreeItems) {
				k, v := d.At(i)
				s.named[string(k)] = v
			}
		}
		names, _ := s.r.Resolve(cat.Get("Names")).(*syntax.Dict)
		eachName(s.r, names.Get("Dests"), s.doc.limits.MaxDepth, func(k syntax.String, v syntax.Object) bool {
			if _, ok := s.named[string(k)]; !ok {
				s.named[string(k)] = v
			}
			return true
		})
	}
	return s.named[name]
}

// names builds the output's name dictionary: attachments from every
// source, and, from a single source, its named destinations (those whose
// page is kept) and document scripts.
func (a *assembler) names() *syntax.Dict {
	out := new(syntax.Dict)
	// Attachments, renamed when two documents use the same name.
	var files []nameEntry
	used := map[string]bool{}
	for _, s := range a.srcs {
		names, _ := s.r.Resolve(s.r.Catalog().Get("Names")).(*syntax.Dict)
		eachName(s.r, names.Get("EmbeddedFiles"), s.doc.limits.MaxDepth, func(k syntax.String, v syntax.Object) bool {
			// Compare and rename as text: a key may be UTF-16, where
			// appending bytes would break it.
			base := syntax.Text(k)
			key := base
			for i := 2; used[key]; i++ {
				key = base + " (" + strconv.Itoa(i) + ")"
			}
			used[key] = true
			files = append(files, nameEntry{key, a.copy(s, v)})
			return true
		})
	}
	if t := nameTree(files); t != nil {
		out.Set("EmbeddedFiles", t)
	}
	if a.mode == modeMulti {
		for _, s := range a.srcs {
			names, _ := s.r.Resolve(s.r.Catalog().Get("Names")).(*syntax.Dict)
			if names.Get("JavaScript") != nil {
				a.warn.add("javascript-dropped", "scripts were removed because they belong to one of several documents", 0)
			}
		}
		return out
	}
	s := a.srcs[0]
	s.namedDest("") // load the table
	var dests []nameEntry
	for _, k := range slices.Sorted(maps.Keys(s.named)) {
		if d := a.fixDest(s, s.named[k]); d != nil {
			dests = append(dests, nameEntry{syntax.Text(syntax.String(k)), d})
		}
	}
	if t := nameTree(dests); t != nil {
		out.Set("Dests", t)
	}
	names, _ := s.r.Resolve(s.r.Catalog().Get("Names")).(*syntax.Dict)
	if js := names.Get("JavaScript"); js != nil {
		out.Set("JavaScript", a.copy(s, js))
	}
	return out
}

type nameEntry struct {
	key   string // as text, in UTF-8
	value syntax.Object
}

// nameTree builds a name tree with a single node. Keys must be sorted
// (section 7.9.6); they are sorted as text, as qpdf checks them, and
// written as text strings: ASCII as it is, anything else in UTF-16.
func nameTree(entries []nameEntry) syntax.Object {
	if len(entries) == 0 {
		return nil
	}
	slices.SortStableFunc(entries, func(x, y nameEntry) int {
		switch {
		case x.key < y.key:
			return -1
		case x.key > y.key:
			return 1
		}
		return 0
	})
	list := make(syntax.Array, 0, 2*len(entries))
	for _, e := range entries {
		list = append(list, syntax.TextString(e.key), e.value)
	}
	d := new(syntax.Dict)
	d.Set("Names", list)
	return d
}

// optionalContent combines the sources' optional content (layers). It
// must be kept: without it, content in layers that are off by default
// would appear.
func (a *assembler) optionalContent() syntax.Object {
	var parts []*syntax.Dict
	for _, s := range a.srcs {
		if oc, ok := s.r.Resolve(s.r.Catalog().Get("OCProperties")).(*syntax.Dict); ok {
			if d, ok := a.copy(s, oc).(*syntax.Dict); ok {
				parts = append(parts, d)
			}
		}
	}
	switch len(parts) {
	case 0:
		return nil
	case 1:
		return parts[0]
	}
	out := new(syntax.Dict)
	var ocgs syntax.Array
	def := new(syntax.Dict)
	for i, p := range parts {
		if g, ok := p.Get("OCGs").(syntax.Array); ok {
			ocgs = append(ocgs, g...)
		}
		d, _ := p.Get("D").(*syntax.Dict)
		for j := range d.Len() {
			k, v := d.At(j)
			switch k {
			case "ON", "OFF", "Order", "RBGroups", "Locked", "AS":
				prev, _ := def.Get(k).(syntax.Array)
				if arr, ok := v.(syntax.Array); ok {
					def.Set(k, append(prev, arr...))
				}
			default:
				if i == 0 {
					def.Set(k, v)
				}
			}
		}
	}
	out.Set("OCGs", ocgs)
	out.Set("D", def)
	return out
}

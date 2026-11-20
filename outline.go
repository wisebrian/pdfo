package pdfo

import (
	"github.com/wisebrian/pdfo/internal/syntax"
)

// outlineItem is a bookmark being rebuilt.
type outlineItem struct {
	src    *source
	d      *syntax.Dict
	dest   syntax.Object // the fixed destination, or nil
	action syntax.Object // the fixed action, or nil
	kids   []*outlineItem
	open   bool
}

// maxOutlineItems bounds the bookmarks read from one document.
const maxOutlineItems = 1 << 18

// outlineItems reads a source's bookmarks, keeping those that lead to a page
// in the output, those without a target (headings), and those whose target
// is gone but which still have kept children, as headings.
func (a *assembler) outlineItems(s *source) []*outlineItem {
	root, ok := s.r.Resolve(s.r.Catalog().Get("Outlines")).(*syntax.Dict)
	if !ok {
		return nil
	}
	seen := map[uint32]bool{}
	count := 0
	dropped := 0
	var walk func(first syntax.Object, depth int) []*outlineItem
	walk = func(first syntax.Object, depth int) []*outlineItem {
		var items []*outlineItem
		if depth > s.doc.limits.MaxDepth {
			return nil
		}
		for node := first; node != nil && count < maxOutlineItems; {
			ref, ok := node.(syntax.Ref)
			if !ok || seen[ref.Num] {
				break
			}
			seen[ref.Num] = true
			count++
			d, ok := s.r.Resolve(ref).(*syntax.Dict)
			if !ok {
				break
			}
			it := &outlineItem{src: s, d: d}
			hadTarget := d.Get("Dest") != nil || d.Get("A") != nil
			if d.Get("Dest") != nil {
				it.dest = a.fixDest(s, d.Get("Dest"))
			} else if d.Get("A") != nil {
				it.action = a.fixAction(s, d.Get("A"), 0)
			}
			it.kids = walk(d.Get("First"), depth+1)
			if c, ok := s.r.Resolve(d.Get("Count")).(syntax.Int); ok && c > 0 {
				it.open = true
			}
			if hadTarget && it.dest == nil && it.action == nil && len(it.kids) == 0 {
				dropped++
			} else {
				items = append(items, it)
			}
			node = d.Get("Next")
		}
		return items
	}
	items := walk(root.Get("First"), 0)
	if dropped > 0 {
		for range dropped {
			a.warn.add("bookmarks-dropped", "bookmarks to pages that are not in the output were removed", 0)
		}
	}
	return items
}

// outlines writes the bookmarks of the output and returns the outline
// dictionary, or nil when there are none. With several sources, each
// document's bookmarks follow the previous one's.
func (a *assembler) outlines() syntax.Object {
	var items []*outlineItem
	for _, s := range a.srcs {
		items = append(items, a.outlineItems(s)...)
	}
	if len(items) == 0 {
		return nil
	}
	root := a.w.Alloc()
	first, last, visible := a.writeItems(items, root)
	d := new(syntax.Dict)
	d.Set("Type", syntax.Name("Outlines"))
	d.Set("First", first)
	d.Set("Last", last)
	d.Set("Count", syntax.Int(visible))
	a.w.Put(root, d)
	return root
}

// writeItems writes a list of sibling items under parent and returns the
// first and last, and how many items are visible below parent.
func (a *assembler) writeItems(items []*outlineItem, parent syntax.Ref) (first, last syntax.Ref, visible int) {
	refs := make([]syntax.Ref, len(items))
	for i := range items {
		refs[i] = a.w.Alloc()
	}
	for i, it := range items {
		d := new(syntax.Dict)
		d.Set("Title", a.copy(it.src, it.src.r.Resolve(it.d.Get("Title"))))
		d.Set("Parent", parent)
		if i > 0 {
			d.Set("Prev", refs[i-1])
		}
		if i+1 < len(items) {
			d.Set("Next", refs[i+1])
		}
		if len(it.kids) > 0 {
			f, l, v := a.writeItems(it.kids, refs[i])
			d.Set("First", f)
			d.Set("Last", l)
			// Positive when open: the items visible below; negative when
			// closed: those that would be (section 12.3.3).
			if it.open {
				d.Set("Count", syntax.Int(v))
				visible += v
			} else {
				d.Set("Count", syntax.Int(-v))
			}
		}
		if it.dest != nil {
			d.Set("Dest", it.dest)
		} else if it.action != nil {
			d.Set("A", it.action)
		}
		for _, k := range []syntax.Name{"C", "F"} {
			if v := it.d.Get(k); v != nil {
				d.Set(k, a.copy(it.src, it.src.r.Resolve(v)))
			}
		}
		a.w.Put(refs[i], d)
		visible++
	}
	return refs[0], refs[len(refs)-1], visible
}

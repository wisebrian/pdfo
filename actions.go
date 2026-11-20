package pdfo

import (
	"strconv"
)

// Merge appends all pages of each document, in order.
func Merge(docs []*Document, opts *WriteOptions) (*Output, error) {
	var pages []PageRef
	for _, d := range docs {
		if d == nil {
			return nil, &OptionError{"documents", "a document is missing"}
		}
		for i := range d.NumPages() {
			pages = append(pages, PageRef{Doc: d, Index: i})
		}
	}
	return Assemble(pages, opts)
}

// Split makes one document per group of zero-based page indexes, as
// ParsePages returns them.
func Split(d *Document, groups [][]int, opts *WriteOptions) ([]*Output, error) {
	if len(groups) == 0 {
		return nil, &OptionError{"pages", "no pages given"}
	}
	outs := make([]*Output, 0, len(groups))
	for _, g := range groups {
		out, err := ExtractPages(d, g, opts)
		if err != nil {
			return nil, err
		}
		outs = append(outs, out)
	}
	return outs, nil
}

// SplitEvery makes one document per n pages; n = 1 gives one file per
// page.
func SplitEvery(d *Document, n int, opts *WriteOptions) ([]*Output, error) {
	if n < 1 {
		return nil, &OptionError{"every", "must be at least 1"}
	}
	var groups [][]int
	for start := 0; start < d.NumPages(); start += n {
		var g []int
		for i := start; i < min(start+n, d.NumPages()); i++ {
			g = append(g, i)
		}
		groups = append(groups, g)
	}
	return Split(d, groups, opts)
}

// ExtractPages keeps the listed pages, in the order listed. A page listed
// twice appears twice.
func ExtractPages(d *Document, indexes []int, opts *WriteOptions) (*Output, error) {
	if len(indexes) == 0 {
		return nil, &OptionError{"pages", "no pages given"}
	}
	pages := make([]PageRef, len(indexes))
	for i, x := range indexes {
		pages[i] = PageRef{Doc: d, Index: x}
	}
	return Assemble(pages, opts)
}

// DeletePages keeps every page except the listed ones.
func DeletePages(d *Document, indexes []int, opts *WriteOptions) (*Output, error) {
	drop, err := indexSet(d, indexes)
	if err != nil {
		return nil, err
	}
	var keep []int
	for i := range d.NumPages() {
		if !drop[i] {
			keep = append(keep, i)
		}
	}
	if len(keep) == 0 {
		return nil, &OptionError{"pages", "cannot delete every page"}
	}
	return ExtractPages(d, keep, opts)
}

// ReorderPages puts the pages in a new order. order must list every page
// exactly once; the check catches UI bugs that would silently lose a page.
func ReorderPages(d *Document, order []int, opts *WriteOptions) (*Output, error) {
	if len(order) != d.NumPages() {
		return nil, &OptionError{"order", "must list each of the " + strconv.Itoa(d.NumPages()) + " pages once"}
	}
	seen, err := indexSet(d, order)
	if err != nil {
		return nil, err
	}
	if len(seen) != d.NumPages() {
		return nil, &OptionError{"order", "lists a page more than once"}
	}
	return ExtractPages(d, order, opts)
}

// RotatePages turns the listed pages clockwise by degrees (±90, ±180,
// ±270), added to their current rotation.
func RotatePages(d *Document, indexes []int, degrees int, opts *WriteOptions) (*Output, error) {
	if degrees%90 != 0 {
		return nil, &OptionError{"degrees", "must be a multiple of 90"}
	}
	set, err := indexSet(d, indexes)
	if err != nil {
		return nil, err
	}
	pages := make([]PageRef, d.NumPages())
	for i := range pages {
		pages[i] = PageRef{Doc: d, Index: i}
		if set[i] {
			pages[i].Rotate = degrees
		}
	}
	return Assemble(pages, opts)
}

// ResizePages resizes the listed pages; see Resize.
func ResizePages(d *Document, indexes []int, r Resize, opts *WriteOptions) (*Output, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	set, err := indexSet(d, indexes)
	if err != nil {
		return nil, err
	}
	pages := make([]PageRef, d.NumPages())
	for i := range pages {
		pages[i] = PageRef{Doc: d, Index: i}
		if set[i] {
			rz := r
			pages[i].Resize = &rz
		}
	}
	return Assemble(pages, opts)
}

func indexSet(d *Document, indexes []int) (map[int]bool, error) {
	if d == nil {
		return nil, &OptionError{"document", "missing"}
	}
	set := map[int]bool{}
	for _, x := range indexes {
		if x < 0 || x >= d.NumPages() {
			return nil, &OptionError{"pages", "page " + strconv.Itoa(x+1) + " does not exist: the document has " +
				strconv.Itoa(d.NumPages()) + " pages"}
		}
		set[x] = true
	}
	return set, nil
}

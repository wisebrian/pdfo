package reader

import (
	"bytes"

	"github.com/wisebrian/pdfo/internal/syntax"
)

// rebuild reconstructs the cross-reference data by scanning the whole file
// for "N G obj", as Acrobat and pdf.js do when the table is missing or
// wrong. Later definitions of an object win, which is how incremental
// updates are read. Trailers are taken from "trailer" dictionaries and
// cross-reference streams; failing those, the catalog is found by type.
//
// The scan is linear: it looks for the "obj" keyword with bytes.Index and
// reads the two numbers before it backwards.
func (r *Reader) rebuild() {
	if r.repairing {
		return
	}
	r.repairing = true
	defer func() { r.repairing = false }()
	r.Repaired = true
	r.xref = map[uint32]entry{}
	r.cache = map[uint32]syntax.Object{}
	r.objstms = map[uint32]*objStm{}

	data := r.data
	var order []uint32 // object numbers in file order of their last definition
	for i := 0; ; {
		j := bytes.Index(data[i:], []byte("obj"))
		if j < 0 {
			break
		}
		k := i + j
		i = k + 3
		if k == 0 || !syntax.IsWhite(data[k-1]) || (k+3 < len(data) && !isEnd(data[k+3])) {
			continue // "endobj", "objstm" and the like
		}
		// Read back over "num gen ".
		p := k - 1
		for p >= 0 && syntax.IsWhite(data[p]) {
			p--
		}
		genEnd := p + 1
		for p >= 0 && data[p] >= '0' && data[p] <= '9' {
			p--
		}
		if p+1 == genEnd || genEnd-(p+1) > 5 {
			continue
		}
		q := p
		for q >= 0 && syntax.IsWhite(data[q]) {
			q--
		}
		if q == p {
			continue
		}
		numEnd := q + 1
		for q >= 0 && data[q] >= '0' && data[q] <= '9' {
			q--
		}
		if q+1 == numEnd || numEnd-(q+1) > 10 || (q >= 0 && !isEnd(data[q])) {
			continue
		}
		num, ok := atoi(data[q+1 : numEnd])
		if !ok || num > 1<<32-1 {
			continue
		}
		start := q + 1
		r.xref[uint32(num)] = entry{kind: 1, off: int64(start - r.base)}
		order = append(order, uint32(num))
	}

	// Trailer dictionaries, later ones winning.
	var trailers []*syntax.Dict
	for i := 0; ; {
		j := bytes.Index(data[i:], []byte("trailer"))
		if j < 0 {
			break
		}
		p := i + j + len("trailer")
		i = p
		if t, err := syntax.NewParser(syntax.NewLexer(data, p), r.limits.MaxDepth).Object(); err == nil {
			if d, ok := t.(*syntax.Dict); ok {
				trailers = append([]*syntax.Dict{d}, trailers...)
			}
		}
	}

	// Cross-reference streams give trailers, the catalog is found by type,
	// and object streams are noted for indexing.
	seen := map[uint32]bool{}
	var catalog syntax.Ref
	r.scanStms = nil
	for i := len(order) - 1; i >= 0; i-- {
		num := order[i]
		if seen[num] {
			continue
		}
		seen[num] = true
		p := r.pos(r.xref[num].off)
		if p < 0 {
			continue
		}
		_, o, err := r.parseIndirect(p, &num)
		if err != nil {
			continue
		}
		var d *syntax.Dict
		switch v := o.(type) {
		case *syntax.Dict:
			d = v
		case *syntax.Stream:
			d = v.Dict
		}
		switch t, _ := d.Get("Type").(syntax.Name); t {
		case "XRef":
			// No order between these and "trailer" dictionaries can be
			// known; the dictionaries win.
			trailers = append(trailers, d)
		case "ObjStm":
			r.scanStms = append([]uint32{num}, r.scanStms...)
		case "Catalog":
			if catalog.IsZero() && d.Get("Pages") != nil {
				catalog = syntax.Ref{Num: num}
			}
		}
	}
	r.cache = map[uint32]syntax.Object{}
	r.Trailer = mergeTrailers(trailers)
	r.indexObjStms()
	if !catalog.IsZero() {
		if root, ok := r.Trailer.Get("Root").(syntax.Ref); !ok || !r.isCatalog(root) {
			r.Trailer.Set("Root", catalog)
		}
	}
}

// indexObjStms adds the objects of the object streams found by rebuild for
// the numbers no top-level object uses. It runs again once an encrypted
// file's handler exists, since encrypted object streams cannot be read
// before.
func (r *Reader) indexObjStms() {
	r.objstms = map[uint32]*objStm{}
	for _, num := range r.scanStms {
		os := r.objStm(num)
		if os == nil {
			continue
		}
		for idx, n := range os.order {
			if _, ok := r.xref[n]; !ok {
				r.xref[n] = entry{kind: 2, off: int64(num), idx: idx}
			}
		}
	}
	r.cache = map[uint32]syntax.Object{}
}

func (r *Reader) isCatalog(ref syntax.Ref) bool {
	d, ok := r.Object(ref.Num).(*syntax.Dict)
	return ok && d.Get("Pages") != nil
}

// isEnd reports whether c can follow a token.
func isEnd(c byte) bool {
	return syntax.IsWhite(c) || c == '<' || c == '[' || c == '/' || c == '(' || c == '%' || c == '{'
}

func atoi(b []byte) (int64, bool) {
	var v int64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int64(c-'0')
	}
	return v, len(b) > 0
}

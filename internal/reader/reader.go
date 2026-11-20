// Package reader opens PDF files: the header, the cross-reference data in
// all its forms (tables, streams, hybrid files, incremental updates),
// object streams, encryption and the page tree. When the cross-reference
// data is missing or wrong it rebuilds it by scanning the file, as Acrobat
// and pdf.js do.
//
// Objects are read lazily, the first time they are needed, and stream data
// is never copied: a Stream's Data is a slice of the input.
//
// Every structure that can loop (references, /Prev chains, trees) is walked
// with a set of visited nodes and a depth limit, every count read from the
// file is checked against the bytes that back it, and decoding is bounded
// by a Budget, so no input can make Open hang, panic or allocate without
// bound.
package reader

import (
	"bytes"
	"errors"
	"strconv"

	"github.com/wisebrian/pdfo/internal/crypt"
	"github.com/wisebrian/pdfo/internal/filter"
	"github.com/wisebrian/pdfo/internal/syntax"
)

// Limits bounds the work a file can cause.
type Limits struct {
	MaxDepth      int   // nesting of objects and depth of trees
	MaxStreamSize int64 // decoded size of one stream
	MaxDecoded    int64 // decoded bytes of all streams
}

// Error kinds the caller maps to its own errors.
var (
	// ErrNotPDF means no PDF header was found.
	ErrNotPDF = errors.New("not a PDF file")
	// ErrNoPassword means the file needs a password and none was given.
	ErrNoPassword = errors.New("password required")
)

// FormatError reports damage that recovery could not repair.
type FormatError struct {
	Offset int64  // where, or -1
	Msg    string // what
}

func (e *FormatError) Error() string { return e.Msg }

// entry is one cross-reference entry.
type entry struct {
	kind byte  // 0 free, 1 at an offset, 2 in an object stream
	off  int64 // kind 1: offset of the object; kind 2: object stream number
	idx  int   // kind 2: index in the object stream
}

// Reader is an opened file.
type Reader struct {
	data []byte
	base int // offset of "%PDF-"; offsets in the file count from it

	// Version is the header version, such as "1.7".
	Version string
	// Trailer is the merged trailer dictionary.
	Trailer *syntax.Dict
	// Crypt decrypts the file; nil when it is not encrypted.
	Crypt *crypt.Handler
	// Repaired is set when the cross-reference data had to be rebuilt.
	Repaired bool
	// Warnings lists problems found and worked around.
	Warnings []string

	xref       map[uint32]entry
	cache      map[uint32]syntax.Object
	inProgress map[uint32]bool
	objstms    map[uint32]*objStm
	encNum     uint32 // the encryption dictionary, whose strings are not encrypted
	budget     *filter.Budget
	limits     Limits
	repairing  bool
	depth      int      // objects being loaded, each needed by the one before
	scanStms   []uint32 // object streams found by rebuild
	treeNodes  map[uint32]bool
}

// Open parses data, decrypting with password when the file is encrypted.
func Open(data []byte, password string, limits Limits) (*Reader, error) {
	r := &Reader{
		data:       data,
		xref:       map[uint32]entry{},
		cache:      map[uint32]syntax.Object{},
		inProgress: map[uint32]bool{},
		objstms:    map[uint32]*objStm{},
		limits:     limits,
		budget:     newBudget(limits),
	}
	// The header may follow some junk; Acrobat looks in the first 1024
	// bytes, pdf.js further. Offsets then count from the header.
	head := data[:min(len(data), 64<<10)]
	i := bytes.Index(head, []byte("%PDF-"))
	if i < 0 {
		return nil, ErrNotPDF
	}
	r.base = i
	v := i + 5
	for v < len(data) && v < i+10 && (data[v] == '.' || (data[v] >= '0' && data[v] <= '9')) {
		v++
	}
	r.Version = string(data[i+5 : v])
	if r.Version == "" {
		r.Version = "1.4"
	}

	if err := r.loadXRef(); err != nil {
		r.warn("cross-reference data unreadable (" + err.Error() + "); rebuilding it")
		r.rebuild()
	}
	if err := r.setupEncryption(password); err != nil {
		return nil, err
	}
	if !r.catalogOK() && !r.Repaired {
		r.warn("document catalog not found through the cross-reference data; rebuilding it")
		r.rebuild()
		if err := r.setupEncryption(password); err != nil {
			return nil, err
		}
	}
	if !r.catalogOK() {
		return nil, &FormatError{-1, "no document catalog with pages"}
	}
	return r, nil
}

func newBudget(l Limits) *filter.Budget {
	return &filter.Budget{PerStream: l.MaxStreamSize, Total: l.MaxDecoded}
}

func (r *Reader) warn(msg string) {
	if len(r.Warnings) < 100 {
		r.Warnings = append(r.Warnings, msg)
	}
}

// catalogOK reports whether the trailer leads to a catalog with a page tree.
func (r *Reader) catalogOK() bool {
	cat, _ := r.Resolve(r.Trailer.Get("Root")).(*syntax.Dict)
	_, ok := r.Resolve(cat.Get("Pages")).(*syntax.Dict)
	return ok
}

// Catalog returns the document catalog.
func (r *Reader) Catalog() *syntax.Dict {
	cat, _ := r.Resolve(r.Trailer.Get("Root")).(*syntax.Dict)
	return cat
}

func (r *Reader) setupEncryption(password string) error {
	r.Crypt = nil
	encObj := r.Trailer.Get("Encrypt")
	if encObj == nil {
		return nil
	}
	if ref, ok := encObj.(syntax.Ref); ok {
		r.encNum = ref.Num
		delete(r.cache, ref.Num) // read again without decryption
	}
	enc, ok := r.Resolve(encObj).(*syntax.Dict)
	if !ok {
		r.warn("encryption dictionary missing; reading the file as unencrypted")
		return nil
	}
	var id []byte
	if ids, ok := r.Resolve(r.Trailer.Get("ID")).(syntax.Array); ok && len(ids) > 0 {
		if s, ok := ids[0].(syntax.String); ok {
			id = []byte(s)
		}
	}
	h, err := crypt.New(enc, id, password)
	if errors.Is(err, crypt.ErrPassword) && password == "" {
		return ErrNoPassword
	}
	if err != nil {
		return err
	}
	r.Crypt = h
	// Objects read before the handler existed hold encrypted strings.
	for num := range r.cache {
		if num != r.encNum {
			delete(r.cache, num)
		}
	}
	r.objstms = map[uint32]*objStm{}
	if r.Repaired {
		r.indexObjStms()
	}
	return nil
}

// loadXRef reads the cross-reference sections from the last one back, the
// newest entries winning.
func (r *Reader) loadXRef() error {
	start, err := r.findStartXRef()
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	var trailers []*syntax.Dict
	off := start
	for {
		if seen[off] {
			r.warn("cross-reference sections form a loop")
			break
		}
		seen[off] = true
		trailer, err := r.readSection(off)
		if err != nil {
			if len(trailers) == 0 {
				return err
			}
			r.warn("older cross-reference section unreadable: " + err.Error())
			break
		}
		trailers = append(trailers, trailer)
		prev, ok := trailer.Get("Prev").(syntax.Int)
		if !ok {
			break
		}
		off = int64(prev)
	}
	r.Trailer = mergeTrailers(trailers)
	if len(r.xref) == 0 {
		return errors.New("no entries")
	}
	return nil
}

// mergeTrailers combines trailers newest first: an incremental update's
// trailer should repeat every key, but some writers leave out Info or ID.
func mergeTrailers(ts []*syntax.Dict) *syntax.Dict {
	out := new(syntax.Dict)
	for _, t := range ts {
		for i := range t.Len() {
			k, v := t.At(i)
			switch k {
			case "Prev", "XRefStm", "Type", "W", "Index", "Filter", "DecodeParms", "Length":
				continue
			}
			if out.Get(k) == nil {
				out.Set(k, v)
			}
		}
	}
	return out
}

func (r *Reader) findStartXRef() (int64, error) {
	key := []byte("startxref")
	tail := r.data[max(0, len(r.data)-4096):]
	i := bytes.LastIndex(tail, key)
	if i >= 0 {
		i += len(r.data) - len(tail)
	} else if i = bytes.LastIndex(r.data, key); i < 0 {
		return 0, errors.New("no startxref")
	}
	l := syntax.NewLexer(r.data, i+len(key))
	t := l.Next()
	if t.Kind != syntax.Integer || t.Int < 0 {
		return 0, errors.New("startxref without an offset")
	}
	return t.Int, nil
}

// pos converts a file offset to a position in data, or -1 when outside.
func (r *Reader) pos(off int64) int {
	p := off + int64(r.base)
	if p < 0 || p >= int64(len(r.data)) {
		return -1
	}
	return int(p)
}

// readSection reads the cross-reference section at off, adding the
// entries not already set by a newer section, and returns its trailer.
func (r *Reader) readSection(off int64) (*syntax.Dict, error) {
	p := r.pos(off)
	if p < 0 {
		return nil, errors.New("offset " + strconv.FormatInt(off, 10) + " is outside the file")
	}
	l := syntax.NewLexer(r.data, p)
	l.SkipSpace()
	if bytes.HasPrefix(r.data[l.Pos():], []byte("xref")) {
		return r.readTable(l.Pos() + 4)
	}
	d, section, err := r.readXRefStream(p)
	if err != nil {
		return nil, err
	}
	r.addEntries(section)
	return d, nil
}

// readTable reads a classic cross-reference table and the trailer after it.
func (r *Reader) readTable(p int) (*syntax.Dict, error) {
	ps := syntax.NewParser(syntax.NewLexer(r.data, p), r.limits.MaxDepth)
	section := map[uint32]entry{}
	for first := true; ; first = false {
		t := ps.Next()
		if t.Kind == syntax.Keyword && t.Value == "trailer" {
			break
		}
		if t.Kind != syntax.Integer {
			return nil, errors.New("bad cross-reference table")
		}
		c := ps.Next()
		if c.Kind != syntax.Integer || t.Int < 0 || c.Int < 0 || t.Int+c.Int > 1<<32 {
			return nil, errors.New("bad cross-reference subsection")
		}
		// Each entry takes at least six bytes ("0 0 n "); a count larger
		// than the rest of the file allows is a lie.
		if c.Int > int64(len(r.data)-ps.Pos())/6+1 {
			return nil, errors.New("cross-reference subsection longer than the file")
		}
		start := t.Int
		for i := int64(0); i < c.Int; i++ {
			o, g, k := ps.Next(), ps.Next(), ps.Next()
			if o.Kind != syntax.Integer || g.Kind != syntax.Integer || k.Kind != syntax.Keyword || (k.Value != "n" && k.Value != "f") {
				return nil, errors.New("bad cross-reference entry")
			}
			// Some writers number the first subsection from 1 although its
			// first entry is object 0, the head of the free list.
			if first && i == 0 && start == 1 && k.Value == "f" && g.Int == 65535 {
				start = 0
			}
			num := uint32(start + i)
			if k.Value == "f" {
				section[num] = entry{kind: 0}
			} else if o.Int > 0 {
				section[num] = entry{kind: 1, off: o.Int}
			}
		}
	}
	trailer, err := ps.Object()
	if err != nil {
		return nil, errors.New("bad trailer: " + err.Error())
	}
	td, ok := trailer.(*syntax.Dict)
	if !ok {
		return nil, errors.New("trailer is not a dictionary")
	}
	// A hybrid file lists in a cross-reference stream the objects only
	// readers of PDF 1.5 should see, marking them free in the table. Its
	// entries replace the table's free ones (section 7.5.8.4).
	if xs, ok := td.Get("XRefStm").(syntax.Int); ok {
		if p := r.pos(int64(xs)); p >= 0 {
			_, stm, err := r.readXRefStream(p)
			if err != nil {
				r.warn("hybrid cross-reference stream unreadable: " + err.Error())
			}
			for num, e := range stm {
				if t, ok := section[num]; !ok || t.kind == 0 {
					section[num] = e
				}
			}
		}
	}
	r.addEntries(section)
	return td, nil
}

func (r *Reader) addEntries(section map[uint32]entry) {
	for num, e := range section {
		if _, ok := r.xref[num]; !ok {
			r.xref[num] = e
		}
	}
}

// readXRefStream reads the cross-reference stream object at position p and
// returns its dictionary and entries.
func (r *Reader) readXRefStream(p int) (*syntax.Dict, map[uint32]entry, error) {
	_, obj, err := r.parseIndirect(p, nil)
	if err != nil {
		return nil, nil, err
	}
	s, ok := obj.(*syntax.Stream)
	if !ok {
		return nil, nil, errors.New("no cross-reference table or stream at the offset")
	}
	d := s.Dict
	if t, _ := d.Get("Type").(syntax.Name); t != "XRef" {
		return nil, nil, errors.New("stream at the offset is not a cross-reference stream")
	}
	data, err := r.decodeRaw(s, false)
	if err != nil && !isPartial(err) {
		return nil, nil, err
	}
	w, _ := d.Get("W").(syntax.Array)
	if len(w) < 3 {
		return nil, nil, errors.New("cross-reference stream lacks W")
	}
	var ws [3]int
	row := 0
	for i := range 3 {
		v, ok := w[i].(syntax.Int)
		if !ok || v < 0 || v > 8 {
			return nil, nil, errors.New("cross-reference stream has bad W")
		}
		ws[i] = int(v)
		row += int(v)
	}
	if row == 0 {
		return nil, nil, errors.New("cross-reference stream has zero-width entries")
	}
	size, _ := d.Get("Size").(syntax.Int)
	index, ok := d.Get("Index").(syntax.Array)
	if !ok {
		index = syntax.Array{syntax.Int(0), size}
	}
	field := func(b []byte) int64 {
		var v int64
		for _, c := range b {
			v = v<<8 | int64(c)
		}
		return v
	}
	section := map[uint32]entry{}
	pos := 0
	for i := 0; i+1 < len(index); i += 2 {
		start, ok1 := index[i].(syntax.Int)
		count, ok2 := index[i+1].(syntax.Int)
		if !ok1 || !ok2 || start < 0 || count < 0 {
			return nil, nil, errors.New("cross-reference stream has a bad Index")
		}
		for j := int64(0); j < int64(count); j++ {
			if pos+row > len(data) {
				break // fewer entries than declared: keep the ones there
			}
			e := data[pos : pos+row]
			pos += row
			typ := int64(1)
			if ws[0] > 0 {
				typ = field(e[:ws[0]])
			}
			f2 := field(e[ws[0] : ws[0]+ws[1]])
			f3 := field(e[ws[0]+ws[1]:])
			num := uint32(int64(start) + j)
			switch typ {
			case 0:
				section[num] = entry{kind: 0}
			case 1:
				section[num] = entry{kind: 1, off: f2}
			case 2:
				section[num] = entry{kind: 2, off: f2, idx: int(f3)}
			}
		}
	}
	return d, section, nil
}

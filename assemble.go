package pdfo

import (
	"math"
	"strconv"

	"github.com/wisebrian/pdfo/internal/reader"
	"github.com/wisebrian/pdfo/internal/syntax"
	"github.com/wisebrian/pdfo/internal/writer"
)

// PageRef is one page of an output: a page of a source document, plus
// changes that apply to the output only.
type PageRef struct {
	// Doc is the source document.
	Doc *Document
	// Index is the zero-based page index in Doc.
	Index int
	// Rotate is added, clockwise, to the page's own rotation; a multiple
	// of 90, negative values allowed.
	Rotate int
	// Resize, when not nil, puts the page's content on a page of another
	// size.
	Resize *Resize
}

// Resize puts a page's content on a page of another size. The content is
// scaled uniformly and centred, never stretched: a page of another shape
// gets blank bands rather than distorted text. The content is wrapped, not
// parsed, so resizing is as fast and lossless as copying.
type Resize struct {
	// To is the new size; zero means Scale.
	To Size
	// Orientation turns To: Auto matches the page's displayed orientation.
	Orientation Orientation
	// Scale, used when To is zero, multiplies the page size: 0.5 halves it.
	Scale float64
}

func (r *Resize) check() error {
	if r.To != (Size{}) {
		if r.Scale != 0 {
			return &OptionError{"Resize", "give either a size or a scale, not both"}
		}
		ok := func(v float64) bool { return v >= minPage && v <= maxPage }
		if !ok(r.To.Width) || !ok(r.To.Height) {
			return &OptionError{"Resize", "each side must be between 3 and 14400 points (1–5080 mm)"}
		}
	} else if !(r.Scale > 0 && r.Scale <= 100) {
		return &OptionError{"Resize", "the scale must be greater than 0 and at most 100"}
	}
	if r.Orientation < Auto || r.Orientation > Landscape {
		return &OptionError{"Resize", "unknown orientation"}
	}
	return nil
}

// Assembly modes: what the page list looks like decides what document-level
// structure survives (see the package documentation).
const (
	modeSame   = iota // every page of one document, once, in order
	modeSingle        // pages of one document
	modeMulti         // pages of several documents
)

// Assemble builds a new document from pages of one or more documents; every
// page action is a call to it. Only objects the pages reach are copied,
// renumbered, and an object shared by several pages (a font, an image) is
// copied once per source document. Streams are copied byte for byte
// (decrypted if needed), never re-encoded.
//
// What happens to bookmarks, links, forms, attachments and scripts depends
// on the page list, not on which function was called; the package
// documentation states the rules, and Report.Warnings lists what was
// dropped.
func Assemble(pages []PageRef, opts *WriteOptions) (*Output, error) {
	var o WriteOptions
	if opts != nil {
		o = *opts
	}
	if err := o.check(); err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, &OptionError{"pages", "no pages given"}
	}
	for i, p := range pages {
		if p.Doc == nil {
			return nil, &OptionError{"pages", "page " + strconv.Itoa(i+1) + " of the list has no document"}
		}
		if p.Index < 0 || p.Index >= p.Doc.NumPages() {
			return nil, &OptionError{"pages", "page " + strconv.Itoa(p.Index+1) + " does not exist: the document has " +
				strconv.Itoa(p.Doc.NumPages()) + " pages"}
		}
		if p.Rotate%90 != 0 {
			return nil, &OptionError{"Rotate", "must be a multiple of 90"}
		}
		if p.Resize != nil {
			if err := p.Resize.check(); err != nil {
				return nil, err
			}
		}
	}
	a := &assembler{w: writer.New(), opts: o}
	return a.run(pages)
}

type assembler struct {
	w        *writer.Writer
	opts     WriteOptions
	out      []*outPage
	srcs     []*source
	mode     int
	warn     warnings
	pagesRef syntax.Ref
	queue    []job
	cmp      *compressor // set by Compress
	fonts    map[string]syntax.Ref
}

// job is a source object waiting to be copied to dst.
type job struct {
	src *source
	num uint32
	dst syntax.Ref
}

// source is a document pages come from, with what has been copied from it.
type source struct {
	doc *Document
	r   *reader.Reader
	// objs maps source objects to their copies: each is copied once.
	objs map[uint32]syntax.Ref
	// first is the first output page made from each source page; links to a
	// page go there.
	first map[int]*outPage
	count map[int]int
	// annots maps annotations on pages that appear once to their copies,
	// so that what refers to them (popups, form fields, the structure
	// tree) can follow; annotNums holds every annotation of the source, so
	// that references to annotations not copied become null.
	annots    map[uint32]syntax.Ref
	annotNums map[uint32]bool
	// skip holds objects never copied through references: the catalog and
	// the encryption dictionary.
	skip map[uint32]bool
	// interactive says the source's form stays a form; fields maps its
	// kept fields to their copies.
	interactive bool
	fields      map[uint32]syntax.Ref
	keepJS      bool
	keepStruct  bool
	formize     map[uint32]bool // appearance streams drawn as form XObjects
	named       map[string]syntax.Object
}

// outPage is one page of the output.
type outPage struct {
	src     *source
	index   int
	page    *reader.Page
	ref     syntax.Ref
	rotate  int
	resize  *Resize
	m       matrix // maps the source page's space to the output page's
	box     reader.Rect
	resized bool
	unique  bool // the source page appears once in the output
}

func (a *assembler) run(refs []PageRef) (*Output, error) {
	bySrc := map[*Document]*source{}
	for _, pr := range refs {
		s, ok := bySrc[pr.Doc]
		if !ok {
			s = newSource(pr.Doc)
			bySrc[pr.Doc] = s
			a.srcs = append(a.srcs, s)
		}
		s.count[pr.Index]++
	}
	a.pagesRef = a.w.Alloc()
	catalog := a.w.Alloc()
	for _, pr := range refs {
		s := bySrc[pr.Doc]
		op := &outPage{
			src: s, index: pr.Index, page: pr.Doc.pages[pr.Index], ref: a.w.Alloc(),
			rotate: ((pr.Rotate % 360) + 360) % 360, resize: pr.Resize, m: identity,
			unique: s.count[pr.Index] == 1,
		}
		if s.first[pr.Index] == nil {
			s.first[pr.Index] = op
		}
		if pr.Resize != nil {
			op.m, op.box = planResize(op.page, op.rotate, pr.Resize)
			op.resized = true
		}
		a.out = append(a.out, op)
	}
	a.mode = modeMulti
	if len(a.srcs) == 1 {
		a.mode = modeSingle
		s := a.srcs[0]
		if len(refs) == s.doc.NumPages() {
			same := true
			for i, pr := range refs {
				if pr.Index != i {
					same = false
					break
				}
			}
			if same {
				a.mode = modeSame
			}
		}
	}
	a.planForms()
	for _, s := range a.srcs {
		s.keepJS = a.mode != modeMulti
		s.keepStruct = a.mode == modeSame
	}
	a.planAnnots()
	for _, op := range a.out {
		a.copyPage(op)
	}
	a.drain()
	cat := a.catalog()
	a.drain()
	a.w.Put(catalog, cat)
	kids := make(syntax.Array, len(a.out))
	for i, op := range a.out {
		kids[i] = op.ref
	}
	pd := new(syntax.Dict)
	pd.Set("Type", syntax.Name("Pages"))
	pd.Set("Kids", kids)
	pd.Set("Count", syntax.Int(len(kids)))
	a.w.Put(a.pagesRef, pd)

	var infoRef syntax.Ref
	if info := a.info(); info != nil {
		infoRef = a.w.Add(info)
	}
	a.drain()
	version := "1.7"
	encrypted := false
	for _, s := range a.srcs {
		if v := s.doc.version(); versionLess(version, v) && versionLess(v, "3") {
			version = v
		}
		if s.r.Crypt != nil {
			encrypted = true
		}
	}
	f, err := a.w.Finish(writer.Options{Version: version, XRefTable: a.opts.XRefTable, Root: catalog, Info: infoRef})
	if err != nil {
		return nil, err
	}
	return &Output{
		Report: Report{Pages: len(a.out), InputEncrypted: encrypted, Warnings: a.warn.result()},
		file:   f,
	}, nil
}

func newSource(d *Document) *source {
	s := &source{
		doc: d, r: d.r,
		objs: map[uint32]syntax.Ref{}, first: map[int]*outPage{}, count: map[int]int{},
		annots: map[uint32]syntax.Ref{}, annotNums: map[uint32]bool{}, skip: map[uint32]bool{},
		fields: map[uint32]syntax.Ref{}, formize: map[uint32]bool{},
	}
	if ref, ok := d.r.Trailer.Get("Root").(syntax.Ref); ok {
		s.skip[ref.Num] = true
	}
	if ref, ok := d.r.Trailer.Get("Encrypt").(syntax.Ref); ok {
		s.skip[ref.Num] = true
	}
	for _, p := range d.pages {
		annots, _ := d.r.Resolve(p.Dict.Get("Annots")).(syntax.Array)
		for _, x := range annots {
			if ref, ok := x.(syntax.Ref); ok {
				s.annotNums[ref.Num] = true
			}
		}
	}
	return s
}

// copy copies a source object into the output, mapping references.
func (a *assembler) copy(s *source, o syntax.Object) syntax.Object {
	switch v := o.(type) {
	case syntax.Ref:
		return a.ref(s, v)
	case syntax.Array:
		out := make(syntax.Array, len(v))
		for i, x := range v {
			out[i] = a.copy(s, x)
		}
		return out
	case *syntax.Dict:
		out := new(syntax.Dict)
		for i := range v.Len() {
			k, x := v.At(i)
			out.Set(k, a.copy(s, x))
		}
		return out
	case *syntax.Stream:
		return nil // streams are indirect; the reader never nests one
	}
	return o
}

// ref maps a reference: to the copy of a page, annotation or field when the
// output has one, to null when it has none (a link to a dropped page must
// not drag the page in), and otherwise to a copy of the object, made once.
func (a *assembler) ref(s *source, r syntax.Ref) syntax.Object {
	n := r.Num
	if d, ok := s.annots[n]; ok {
		return d
	}
	if d, ok := s.fields[n]; ok {
		return d
	}
	if i, ok := s.doc.byRef[n]; ok {
		if op := s.first[i]; op != nil {
			return op.ref
		}
		return nil
	}
	if s.skip[n] || s.annotNums[n] || s.r.IsTreeNode(n) {
		return nil
	}
	if a.cmp != nil {
		if canon, ok := a.cmp.alias[n]; ok {
			n = canon // an identical stream: copy one
		}
	}
	if d, ok := s.objs[n]; ok {
		return d
	}
	d := a.w.Alloc()
	s.objs[n] = d
	a.queue = append(a.queue, job{s, n, d})
	return d
}

// drain copies the objects waiting in the queue, and those they reach.
func (a *assembler) drain() {
	for i := 0; i < len(a.queue); i++ {
		j := a.queue[i]
		switch v := j.src.r.Object(j.num).(type) {
		case *syntax.Stream:
			if a.cmp != nil {
				a.cmp.stream(a, j, v)
				continue
			}
			d := a.streamDict(j.src, v)
			if j.src.formize[j.num] {
				d.Set("Type", syntax.Name("XObject"))
				d.Set("Subtype", syntax.Name("Form"))
				if d.Get("BBox") == nil {
					d.Set("BBox", syntax.Array{syntax.Int(0), syntax.Int(0), syntax.Int(1), syntax.Int(1)})
				}
			}
			body, err := j.src.r.Body(v)
			if err != nil {
				a.warn.add("stream-unreadable", "a stream could not be decrypted and was left empty", 0)
				a.w.PutStream(j.dst, d)
				continue
			}
			a.w.PutStreamBody(j.dst, d, body)
		default:
			a.w.Put(j.dst, a.copy(j.src, v))
		}
	}
	a.queue = a.queue[:0]
}

// streamDict copies a stream's dictionary for its stored data: without
// Length, which the writer sets, and without Crypt filters, since the data
// is written decrypted.
func (a *assembler) streamDict(s *source, st *syntax.Stream) *syntax.Dict {
	d := new(syntax.Dict)
	for i := range st.Dict.Len() {
		k, v := st.Dict.At(i)
		if k == "Length" {
			continue
		}
		d.Set(k, a.copy(s, v))
	}
	if s.r.Crypt == nil {
		return d
	}
	fs, ok := s.r.Resolve(st.Dict.Get("Filter")).(syntax.Array)
	if !ok {
		if f, _ := s.r.Resolve(st.Dict.Get("Filter")).(syntax.Name); f == "Crypt" {
			d.Delete("Filter")
			d.Delete("DecodeParms")
		}
		return d
	}
	ps, _ := s.r.Resolve(st.Dict.Get("DecodeParms")).(syntax.Array)
	var nf, np syntax.Array
	for i, f := range fs {
		if n, _ := s.r.Resolve(f).(syntax.Name); n == "Crypt" {
			continue
		}
		nf = append(nf, a.copy(s, f))
		if i < len(ps) {
			np = append(np, a.copy(s, ps[i]))
		} else if ps != nil {
			np = append(np, nil)
		}
	}
	d.Delete("Filter")
	d.Delete("DecodeParms")
	if len(nf) > 0 {
		d.Set("Filter", nf)
		if np != nil {
			d.Set("DecodeParms", np)
		}
	}
	return d
}

// pageKeys are the page dictionary entries copyPage writes itself.
var pageKeys = map[syntax.Name]bool{
	"Type": true, "Parent": true, "Resources": true, "MediaBox": true, "CropBox": true,
	"Rotate": true, "Contents": true, "Annots": true, "Thumb": true,
	"TrimBox": true, "BleedBox": true, "ArtBox": true,
}

func (a *assembler) copyPage(op *outPage) {
	s, p := op.src, op.page
	d := new(syntax.Dict)
	d.Set("Type", syntax.Name("Page"))
	d.Set("Parent", a.pagesRef)
	for i := range p.Dict.Len() {
		k, v := p.Dict.At(i)
		switch {
		case pageKeys[k]:
		case k == "StructParents" && !s.keepStruct:
		case k == "PieceInfo" && a.cmp != nil: // private data of the application that made the page
		case k == "B" && a.mode != modeSame: // article beads go with the threads
		default:
			d.Set(k, a.copy(s, v))
		}
	}
	// Inherited attributes are written on every page: the output's page
	// tree has a single level and inherits nothing.
	if op.resized {
		d.Set("MediaBox", op.box.Array())
		d.Set("CropBox", op.box.Array())
	} else {
		d.Set("MediaBox", p.MediaBox.Array())
		d.Set("CropBox", p.CropBox.Array())
	}
	for _, k := range []syntax.Name{"TrimBox", "BleedBox", "ArtBox"} {
		r, ok := s.r.Rect(p.Dict.Get(k))
		if !ok {
			continue
		}
		if op.resized {
			r = op.m.rect(r).Intersect(op.box)
		}
		d.Set(k, r.Array())
	}
	d.Set("Rotate", syntax.Int((p.Rotate+op.rotate)%360))

	res, _ := a.copy(s, s.r.Resolve(p.Resources)).(*syntax.Dict)
	if res == nil {
		res = new(syntax.Dict)
	}
	annots, extra := a.copyAnnots(op, res)
	d.Set("Resources", res)
	if len(annots) > 0 {
		d.Set("Annots", annots)
	}

	contents := a.copy(s, p.Dict.Get("Contents"))
	if op.resized || len(extra) > 0 {
		// The page's own content is wrapped in q … Q so whatever state it
		// leaves cannot affect what follows; the prefix also holds the
		// resize transformation and a clip to the old visible area, so
		// content outside it stays hidden in the new margins.
		prefix := []byte("q\n")
		if op.resized {
			prefix = appendNums(prefix, op.m[:]...)
			prefix = append(prefix, " cm "...)
			prefix = appendNums(prefix, p.CropBox.X0, p.CropBox.Y0, p.CropBox.W(), p.CropBox.H())
			prefix = append(prefix, " re W n\n"...)
		}
		list := syntax.Array{a.w.AddStream(new(syntax.Dict), prefix)}
		switch c := contents.(type) {
		case syntax.Array:
			list = append(list, c...)
		case nil:
		default:
			list = append(list, c)
		}
		suffix := []byte("\nQ\n")
		for _, e := range extra {
			if op.resized {
				// Flattened fields are drawn in the old page's space too.
				e = append(append(append([]byte("q "), appendNums(nil, op.m[:]...)...), " cm\n"...), append(e, "Q\n"...)...)
			}
			suffix = append(suffix, e...)
		}
		list = append(list, a.w.AddStream(new(syntax.Dict), suffix))
		contents = list
	}
	if contents != nil {
		d.Set("Contents", contents)
	}
	a.w.Put(op.ref, d)
}

// matrix is an affine transformation [a b c d e f].
type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

func (m matrix) point(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// rect transforms a rectangle; the matrices used here only scale and
// translate, so the result is again a rectangle.
func (m matrix) rect(r reader.Rect) reader.Rect {
	x0, y0 := m.point(r.X0, r.Y0)
	x1, y1 := m.point(r.X1, r.Y1)
	return reader.Rect{X0: math.Min(x0, x1), Y0: math.Min(y0, y1), X1: math.Max(x0, x1), Y1: math.Max(y0, y1)}
}

// planResize computes the transformation and new media box that put the
// visible area of p, scaled uniformly and centred, on a page of the size
// rz asks for. rotate is the extra rotation the page gets, which decides
// what "landscape" means for it.
func planResize(p *reader.Page, rotate int, rz *Resize) (matrix, reader.Rect) {
	crop := p.CropBox
	rot := (p.Rotate + rotate) % 360
	cw, ch := crop.W(), crop.H()
	dw, dh := cw, ch // as displayed
	if rot%180 == 90 {
		dw, dh = ch, cw
	}
	var s, W, H float64
	if rz.To != (Size{}) {
		W, H = rz.To.Width/p.UserUnit, rz.To.Height/p.UserUnit
		switch rz.Orientation {
		case Portrait:
			if W > H {
				W, H = H, W
			}
		case Landscape:
			if H > W {
				W, H = H, W
			}
		default:
			if dw != dh && W != H && (dw > dh) != (W > H) {
				W, H = H, W
			}
		}
		s = math.Min(W/dw, H/dh)
	} else {
		s = rz.Scale
		W, H = dw*s, dh*s
	}
	wu, hu := W, H // in the page's own, unrotated, space
	if rot%180 == 90 {
		wu, hu = H, W
	}
	tx := (wu-s*cw)/2 - s*crop.X0
	ty := (hu-s*ch)/2 - s*crop.Y0
	return matrix{s, 0, 0, s, tx, ty}, reader.Rect{X0: 0, Y0: 0, X1: wu, Y1: hu}
}

package reader

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strconv"

	"github.com/wisebrian/pdfo/internal/crypt"
	"github.com/wisebrian/pdfo/internal/filter"
	"github.com/wisebrian/pdfo/internal/syntax"
)

// maxRefChain bounds how many references Resolve follows in a row: an
// object whose value is a reference to another is legal, a long chain is
// not, and a loop must end.
const maxRefChain = 32

// maxLoadDepth bounds how many objects can be loading at once, each needed
// by the one before.
const maxLoadDepth = 64

// Resolve follows references until it reaches a direct object. A reference
// to a missing object, or a chain that loops, gives null, as the
// specification says of references to objects that do not exist.
func (r *Reader) Resolve(o syntax.Object) syntax.Object {
	for range maxRefChain {
		ref, ok := o.(syntax.Ref)
		if !ok {
			return o
		}
		o = r.Object(ref.Num)
	}
	return nil
}

// Object returns the value of object num, or nil.
func (r *Reader) Object(num uint32) syntax.Object {
	if o, ok := r.cache[num]; ok {
		return o
	}
	if r.inProgress[num] {
		// The object's own value depends on it, such as a stream whose
		// Length refers to the stream itself.
		r.warn("object " + strconv.Itoa(int(num)) + " refers to itself")
		return nil
	}
	// Loading one object can need another (an indirect Length, an object
	// stream); a hostile file can chain such needs without end.
	if r.depth >= maxLoadDepth {
		r.warn("objects depend on each other too deeply")
		return nil
	}
	r.depth++
	r.inProgress[num] = true
	o := r.load(num)
	delete(r.inProgress, num)
	r.depth--
	r.cache[num] = o
	return o
}

func (r *Reader) load(num uint32) syntax.Object {
	e, ok := r.xref[num]
	if !ok || e.kind == 0 {
		return nil
	}
	if e.kind == 2 {
		return r.fromObjStm(num, uint32(e.off), e.idx)
	}
	o, ok := r.loadAt(num, e.off)
	if ok {
		return o
	}
	// The offset does not hold the object. Files whose offsets count from
	// the start of the file rather than from the header, or that are wrong
	// altogether, are common; rebuild the table once and try again.
	if r.base > 0 {
		if o, ok := r.loadAt(num, e.off-int64(r.base)); ok {
			return o
		}
	}
	if !r.Repaired && !r.repairing {
		r.warn("object " + strconv.Itoa(int(num)) + " is not where the cross-reference data says; rebuilding it")
		r.rebuild()
		if e, ok := r.xref[num]; ok && e.kind == 1 {
			if o, ok := r.loadAt(num, e.off); ok {
				return o
			}
		} else if ok && e.kind == 2 {
			return r.fromObjStm(num, uint32(e.off), e.idx)
		}
	}
	return nil
}

// loadAt parses object num at offset off, reporting false if it is not
// there.
func (r *Reader) loadAt(num uint32, off int64) (syntax.Object, bool) {
	p := r.pos(off)
	if p < 0 {
		return nil, false
	}
	n, o, err := r.parseIndirect(p, &num)
	if err != nil || n != num {
		return nil, false
	}
	return r.decryptStrings(syntax.Ref{Num: num}, o), true
}

// parseIndirect parses "N G obj ... endobj" at position p. When want is not
// nil, the object number must match.
func (r *Reader) parseIndirect(p int, want *uint32) (uint32, syntax.Object, error) {
	ps := syntax.NewParser(syntax.NewLexer(r.data, p), r.limits.MaxDepth)
	n, g, k := ps.Next(), ps.Next(), ps.Next()
	if n.Kind != syntax.Integer || g.Kind != syntax.Integer || k.Kind != syntax.Keyword || k.Value != "obj" ||
		n.Int < 0 || n.Int > 1<<32-1 {
		return 0, nil, errors.New("no object header")
	}
	num := uint32(n.Int)
	if want != nil && num != *want {
		return num, nil, errors.New("object number does not match")
	}
	o, err := ps.Object()
	if err != nil {
		// "N G obj endobj": an empty object is null.
		if t := ps.Peek(0); t.Kind == syntax.Keyword && t.Value == "endobj" {
			return num, nil, nil
		}
		return num, nil, err
	}
	d, ok := o.(*syntax.Dict)
	if !ok {
		return num, o, nil
	}
	t := ps.Peek(0)
	if t.Kind != syntax.Keyword || t.Value != "stream" {
		return num, o, nil
	}
	data := r.streamData(d, t.Pos+len("stream"))
	return num, &syntax.Stream{Dict: d, Data: data, Ref: syntax.Ref{Num: num, Gen: uint16(g.Int)}}, nil
}

// streamData finds the data of a stream whose keyword ends at p: Length
// bytes when "endstream" follows them, otherwise up to the next
// "endstream", which is how viewers read streams whose Length is wrong.
func (r *Reader) streamData(d *syntax.Dict, p int) []byte {
	// The keyword is followed by CRLF or LF; a lone CR is tolerated.
	if p < len(r.data) && r.data[p] == '\r' {
		p++
	}
	if p < len(r.data) && r.data[p] == '\n' {
		p++
	}
	if length, ok := r.Resolve(d.Get("Length")).(syntax.Int); ok && length >= 0 && int64(p)+int64(length) <= int64(len(r.data)) {
		end := p + int(length)
		q := end
		for q < len(r.data) && q < end+4 && syntax.IsWhite(r.data[q]) {
			q++
		}
		if bytes.HasPrefix(r.data[q:], []byte("endstream")) {
			return r.data[p:end]
		}
	}
	i := bytes.Index(r.data[p:], []byte("endstream"))
	if i < 0 {
		// No end at all: the stream runs to the next object, or the end.
		if j := bytes.Index(r.data[p:], []byte("endobj")); j >= 0 {
			i = j
		} else {
			i = len(r.data) - p
		}
	}
	end := p + i
	// The end-of-line marker before "endstream" is not part of the data.
	if end > p && r.data[end-1] == '\n' {
		end--
	}
	if end > p && r.data[end-1] == '\r' {
		end--
	}
	return r.data[p:end]
}

// decryptStrings decrypts every string in an object read from the file at
// top level. Objects inside object streams are not encrypted one by one:
// their stream was.
func (r *Reader) decryptStrings(ref syntax.Ref, o syntax.Object) syntax.Object {
	if r.Crypt == nil || ref.Num == r.encNum {
		return o
	}
	var walk func(o syntax.Object, depth int) syntax.Object
	walk = func(o syntax.Object, depth int) syntax.Object {
		if depth > r.limits.MaxDepth {
			return o
		}
		switch v := o.(type) {
		case syntax.String:
			return r.Crypt.String(ref, v)
		case syntax.Array:
			for i := range v {
				v[i] = walk(v[i], depth+1)
			}
		case *syntax.Dict:
			for i := range v.Len() {
				k, x := v.At(i)
				v.Set(k, walk(x, depth+1))
			}
		case *syntax.Stream:
			walk(v.Dict, depth+1)
		}
		return o
	}
	return walk(o, 0)
}

// objStm is a decoded object stream.
type objStm struct {
	data    []byte
	offsets map[uint32]int // object number to position in data
	order   []uint32       // object numbers by index
}

func (r *Reader) objStm(num uint32) *objStm {
	if s, ok := r.objstms[num]; ok {
		return s
	}
	r.objstms[num] = nil // a stream that refers to itself finds nothing
	s, ok := r.Object(num).(*syntax.Stream)
	if !ok {
		return nil
	}
	data, err := r.Decode(s)
	if err != nil && !isPartial(err) {
		r.warn("object stream " + strconv.Itoa(int(num)) + ": " + err.Error())
		return nil
	}
	n, _ := s.Dict.Get("N").(syntax.Int)
	first, _ := s.Dict.Get("First").(syntax.Int)
	if n < 0 || first < 0 || int(first) > len(data) || int64(n) > int64(len(data))/2+1 {
		r.warn("object stream " + strconv.Itoa(int(num)) + " has a bad header")
		return nil
	}
	os := &objStm{data: data, offsets: map[uint32]int{}}
	l := syntax.NewLexer(data[:first], 0)
	for range n {
		a, b := l.Next(), l.Next()
		if a.Kind != syntax.Integer || b.Kind != syntax.Integer || a.Int < 0 || a.Int > 1<<32-1 || b.Int < 0 {
			break
		}
		p := int(first) + int(min(b.Int, int64(len(data))))
		if _, ok := os.offsets[uint32(a.Int)]; !ok {
			os.offsets[uint32(a.Int)] = p
		}
		os.order = append(os.order, uint32(a.Int))
	}
	r.objstms[num] = os
	return os
}

func (r *Reader) fromObjStm(num, stm uint32, idx int) syntax.Object {
	if stm == num {
		return nil
	}
	os := r.objStm(stm)
	if os == nil {
		return nil
	}
	p, ok := os.offsets[num]
	if !ok {
		return nil
	}
	_ = idx // the header gives the position; the index is only a hint
	o, err := syntax.NewParser(syntax.NewLexer(os.data, p), r.limits.MaxDepth).Object()
	if err != nil {
		return nil
	}
	return o
}

// Decode returns the decoded data of a stream: decrypted, then passed
// through its filters. Image-only filters (DCT, JPX, JBIG2, CCITT) give
// filter.ErrImageFilter, and damaged data a *filter.PartialError with what
// could be decoded.
func (r *Reader) Decode(s *syntax.Stream) ([]byte, error) {
	return r.decodeRaw(s, true)
}

func (r *Reader) decodeRaw(s *syntax.Stream, decrypt bool) ([]byte, error) {
	data := s.Data
	if decrypt && r.Crypt != nil {
		var err error
		if data, err = r.Crypt.Stream(s.Ref, r.Crypt.StreamMethod(s.Dict), data); err != nil {
			return nil, err
		}
	}
	filters, parms := r.Filters(s.Dict)
	return filter.Decode(data, filters, parms, r.budget)
}

// Filters returns a stream's filters and their parameters, without Crypt
// filters, which decryption has already applied.
func (r *Reader) Filters(d *syntax.Dict) ([]syntax.Name, []*syntax.Dict) {
	fo := r.Resolve(d.Get("Filter"))
	po := r.Resolve(d.Get("DecodeParms"))
	if po == nil {
		po = r.Resolve(d.Get("DP"))
	}
	var fs []syntax.Name
	var ps []*syntax.Dict
	switch f := fo.(type) {
	case syntax.Name:
		p, _ := po.(*syntax.Dict)
		if pa, ok := po.(syntax.Array); ok && len(pa) > 0 {
			p, _ = r.Resolve(pa[0]).(*syntax.Dict)
		}
		fs, ps = []syntax.Name{f}, []*syntax.Dict{p}
	case syntax.Array:
		pa, _ := po.(syntax.Array)
		for i, x := range f {
			name, _ := r.Resolve(x).(syntax.Name)
			var p *syntax.Dict
			if i < len(pa) {
				p, _ = r.Resolve(pa[i]).(*syntax.Dict)
			}
			fs = append(fs, name)
			ps = append(ps, p)
		}
	}
	for i := 0; i < len(fs); i++ {
		if fs[i] == "Crypt" {
			fs = append(fs[:i], fs[i+1:]...)
			ps = append(ps[:i], ps[i+1:]...)
			i--
		}
	}
	return fs, ps
}

// Body returns a stream's stored data ready to be copied into a new file:
// decrypted as it is written when the file is encrypted, the data itself
// otherwise.
func (r *Reader) Body(s *syntax.Stream) (crypt.Body, error) {
	if r.Crypt == nil {
		return plain(s.Data), nil
	}
	return r.Crypt.Body(s.Ref, r.Crypt.StreamMethod(s.Dict), s.Data)
}

func isPartial(err error) bool {
	var pe *filter.PartialError
	return errors.As(err, &pe)
}

// Nums returns the numbers of every object in use, in order.
func (r *Reader) Nums() []uint32 {
	nums := make([]uint32, 0, len(r.xref))
	for n, e := range r.xref {
		if e.kind != 0 {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)
	return nums
}

// plain is stream data copied as it is.
type plain []byte

func (p plain) Len() int64 { return int64(len(p)) }
func (p plain) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(p)
	return int64(n), err
}

// Raw returns a stream's stored data decrypted but still encoded by its
// filters, for re-encoding.
func (r *Reader) Raw(s *syntax.Stream) ([]byte, error) {
	if r.Crypt == nil {
		return s.Data, nil
	}
	return r.Crypt.Stream(s.Ref, r.Crypt.StreamMethod(s.Dict), s.Data)
}

// Size returns the length of the file.
func (r *Reader) Size() int64 { return int64(len(r.data)) }

// Budget returns the decoding budget the reader charges, so that work done
// on its streams elsewhere counts against the same limits.
func (r *Reader) Budget() *filter.Budget { return r.budget }

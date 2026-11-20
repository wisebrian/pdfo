// Package writer lays out PDF files. It numbers objects, groups the ones that
// may be compressed into object streams, builds the cross-reference data and
// derives the file identifier from the content.
//
// A finished File knows its exact size before any byte is written and writes
// stream data straight from where it lives, usually the caller's input,
// without first copying it into one large buffer. That is what lets a caller
// allocate the destination once, which matters in a browser tab whose
// WebAssembly memory can grow but never shrink.
package writer

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"errors"
	"io"
	"strconv"

	"github.com/wisebrian/pdfo/internal/syntax"
)

// objectsPerStream is how many objects go in one object stream: enough to
// compress well, few enough that a viewer looking up one object decodes
// little it does not need. qpdf uses the same number.
const objectsPerStream = 100

// inlineMax is the size below which stream data is copied next to the
// object's dictionary instead of being referenced where it lives: copying a
// few bytes is cheaper than another write call on the destination.
const inlineMax = 4096

// Writer collects the objects of one file. Its methods record misuse instead
// of panicking, and Finish reports it, so a bug can never crash a caller
// that is processing a user's file.
type Writer struct {
	objs []entry
	err  error
}

type entry struct {
	set    bool
	stream bool
	obj    syntax.Object // the value of a non-stream object
	dict   *syntax.Dict  // the dictionary of a stream
	data   [][]byte      // the data of a stream, in order
	body   Body          // or the data as a Body
}

// Body is stream data produced while the file is written, such as data
// decrypted on the way: it knows its length in advance, which the layout
// needs, and writes itself.
type Body interface {
	Len() int64
	WriteTo(w io.Writer) (int64, error)
}

// New returns an empty Writer.
func New() *Writer { return &Writer{} }

// Alloc reserves the next object number, so objects can refer to each other
// before their values exist. Numbers are given out in order, which is what
// makes the numbering, and so the file, deterministic.
func (w *Writer) Alloc() syntax.Ref {
	w.objs = append(w.objs, entry{})
	return syntax.Ref{Num: uint32(len(w.objs))}
}

// Put sets the value of an allocated object that is not a stream.
func (w *Writer) Put(r syntax.Ref, o syntax.Object) {
	e := w.entry(r)
	if e == nil {
		return
	}
	if _, ok := o.(*syntax.Stream); ok {
		w.fail("object " + strconv.Itoa(int(r.Num)) + ": a stream must be set with PutStream")
		return
	}
	*e = entry{set: true, obj: o}
}

// PutStream sets an allocated object to a stream whose dictionary is d and
// whose data is the concatenation of chunks. The chunks are referenced, not
// copied, so they must not change until the File has been written. d must
// already name the filters the data is encoded with; Length is set here.
func (w *Writer) PutStream(r syntax.Ref, d *syntax.Dict, chunks ...[]byte) {
	e := w.entry(r)
	if e == nil {
		return
	}
	*e = entry{set: true, stream: true, dict: d, data: chunks}
}

// PutStreamBody sets an allocated object to a stream whose data body
// writes when the file is written.
func (w *Writer) PutStreamBody(r syntax.Ref, d *syntax.Dict, body Body) {
	e := w.entry(r)
	if e == nil {
		return
	}
	*e = entry{set: true, stream: true, dict: d, body: body}
}

// Add allocates an object and sets its value.
func (w *Writer) Add(o syntax.Object) syntax.Ref {
	r := w.Alloc()
	w.Put(r, o)
	return r
}

// AddStream allocates an object and sets it to a stream; see PutStream.
func (w *Writer) AddStream(d *syntax.Dict, chunks ...[]byte) syntax.Ref {
	r := w.Alloc()
	w.PutStream(r, d, chunks...)
	return r
}

func (w *Writer) entry(r syntax.Ref) *entry {
	if r.Num == 0 || int(r.Num) > len(w.objs) || r.Gen != 0 {
		w.fail("object " + strconv.Itoa(int(r.Num)) + " was not allocated")
		return nil
	}
	return &w.objs[r.Num-1]
}

func (w *Writer) fail(msg string) {
	if w.err == nil {
		w.err = errors.New("writer: " + msg)
	}
}

// Options controls Finish.
type Options struct {
	// Version is the header version, such as "1.7".
	Version string
	// XRefTable writes a classic cross-reference table with every object at
	// top level, instead of object streams plus a cross-reference stream.
	XRefTable bool
	// Root is the document catalog. Info, when not zero, is the document
	// information dictionary.
	Root, Info syntax.Ref
}

// Finish lays out the file. The Writer must not be used afterwards.
func (w *Writer) Finish(opts Options) (*File, error) {
	if w.err != nil {
		return nil, w.err
	}
	for i, e := range w.objs {
		if !e.set {
			return nil, errors.New("writer: object " + strconv.Itoa(i+1) + " was allocated but never set")
		}
	}
	if w.entry(opts.Root) == nil || (!opts.Info.IsZero() && w.entry(opts.Info) == nil) {
		return nil, w.err
	}
	if opts.Version == "" {
		return nil, errors.New("writer: no version")
	}

	l := &layout{}
	// The comment's bytes are all above 127, which tells programs that
	// guess file types to treat the file as binary (section 7.5.2).
	l.inline([]byte("%PDF-" + opts.Version + "\n%\xE2\xE3\xCF\xD3\n"))
	if opts.XRefTable {
		return w.finishTable(l, opts)
	}
	return w.finishStream(l, opts)
}

// xref is one cross-reference entry: type 0 (free), 1 (at an offset) or 2
// (inside an object stream), with the two fields of section 7.5.8.3.
type xref struct {
	typ    byte
	f2, f3 int64
}

func (w *Writer) finishTable(l *layout, opts Options) (*File, error) {
	size := len(w.objs) + 1
	offsets := make([]int64, size)
	for i := range w.objs {
		offsets[i+1] = l.pos
		l.object(i+1, &w.objs[i])
	}
	start := l.pos
	if start > 9999999999 {
		return nil, errors.New("writer: file too large for a cross-reference table")
	}
	table := make([]byte, 0, 32+20*size)
	table = append(table, "xref\n0 "...)
	table = strconv.AppendInt(table, int64(size), 10)
	table = append(table, "\n0000000000 65535 f\r\n"...)
	for _, off := range offsets[1:] {
		table = appendPadded(table, off, 10)
		table = append(table, " 00000 n\r\n"...)
	}
	l.inline(table)

	trailer := new(syntax.Dict)
	trailer.Set("Size", syntax.Int(size))
	trailer.Set("Root", opts.Root)
	if !opts.Info.IsZero() {
		trailer.Set("Info", opts.Info)
	}
	trailer.Set("ID", idPlaceholder())
	final := []byte("trailer\n")
	final = syntax.Append(final, trailer)
	final = append(final, "\nstartxref\n"...)
	final = strconv.AppendInt(final, start, 10)
	final = append(final, "\n%%EOF\n"...)
	return l.file(final)
}

func (w *Writer) finishStream(l *layout, opts Options) (*File, error) {
	n := len(w.objs)
	var packed []int // object numbers that go in object streams, in order
	for i, e := range w.objs {
		if !e.stream {
			packed = append(packed, i+1)
		}
	}
	groups := (len(packed) + objectsPerStream - 1) / objectsPerStream
	xrefNum := n + groups + 1
	size := xrefNum + 1
	entries := make([]xref, size)
	entries[0] = xref{typ: 0, f2: 0, f3: 65535}

	// Streams cannot go in object streams (section 7.5.7): they stay at top
	// level, in number order.
	for i := range w.objs {
		if w.objs[i].stream {
			entries[i+1] = xref{typ: 1, f2: l.pos}
			l.object(i+1, &w.objs[i])
		}
	}
	for g := range groups {
		members := packed[g*objectsPerStream : min(len(packed), (g+1)*objectsPerStream)]
		num := n + 1 + g
		var head, body []byte
		for idx, m := range members {
			head = strconv.AppendInt(head, int64(m), 10)
			head = append(head, ' ')
			head = strconv.AppendInt(head, int64(len(body)), 10)
			head = append(head, ' ')
			body = syntax.Append(body, w.objs[m-1].obj)
			body = append(body, '\n')
			entries[m] = xref{typ: 2, f2: int64(num), f3: int64(idx)}
		}
		head[len(head)-1] = '\n'
		data, err := deflate(head, body)
		if err != nil {
			return nil, err
		}
		d := new(syntax.Dict)
		d.Set("Type", syntax.Name("ObjStm"))
		d.Set("N", syntax.Int(len(members)))
		d.Set("First", syntax.Int(len(head)))
		d.Set("Filter", syntax.Name("FlateDecode"))
		entries[num] = xref{typ: 1, f2: l.pos}
		l.object(num, &entry{set: true, stream: true, dict: d, data: [][]byte{data}})
	}

	start := l.pos
	entries[xrefNum] = xref{typ: 1, f2: start}
	var maxF2 int64
	for _, e := range entries {
		maxF2 = max(maxF2, e.f2)
	}
	w2 := 1
	for v := maxF2; v > 0xFF; v >>= 8 {
		w2++
	}
	// The third field is two bytes wide because object 0 records generation
	// 65535, as in a classic table; an index within an object stream never
	// needs more.
	cols := 1 + w2 + 2
	rows := make([]byte, 0, (cols+1)*size)
	prev := make([]byte, cols)
	row := make([]byte, cols)
	for _, e := range entries {
		row[0] = e.typ
		for i := range w2 {
			row[1+i] = byte(e.f2 >> (8 * (w2 - 1 - i)))
		}
		row[cols-2] = byte(e.f3 >> 8)
		row[cols-1] = byte(e.f3)
		// PNG Up prediction: most bytes repeat the row above, so the
		// differences are mostly zero and compress to almost nothing.
		rows = append(rows, 2)
		for i := range cols {
			rows = append(rows, row[i]-prev[i])
		}
		prev, row = row, prev
	}
	data, err := deflate(rows)
	if err != nil {
		return nil, err
	}
	d := new(syntax.Dict)
	d.Set("Type", syntax.Name("XRef"))
	d.Set("Size", syntax.Int(size))
	d.Set("W", syntax.Array{syntax.Int(1), syntax.Int(w2), syntax.Int(2)})
	d.Set("Root", opts.Root)
	if !opts.Info.IsZero() {
		d.Set("Info", opts.Info)
	}
	d.Set("ID", idPlaceholder())
	d.Set("Filter", syntax.Name("FlateDecode"))
	parms := new(syntax.Dict)
	parms.Set("Columns", syntax.Int(cols))
	parms.Set("Predictor", syntax.Int(12))
	d.Set("DecodeParms", parms)
	d.Set("Length", syntax.Int(len(data)))

	final := strconv.AppendInt(nil, int64(xrefNum), 10)
	final = append(final, " 0 obj\n"...)
	final = syntax.Append(final, d)
	final = append(final, "\nstream\n"...)
	final = append(final, data...)
	final = append(final, "\nendstream\nendobj\nstartxref\n"...)
	final = strconv.AppendInt(final, start, 10)
	final = append(final, "\n%%EOF\n"...)
	return l.file(final)
}

// idPlaceholder is the file identifier before its value is known: two
// 16-byte strings, which serialize as hexadecimal because zero bytes are not
// printable. File.WriteTo overwrites the digits with the hash.
func idPlaceholder() syntax.Array {
	zero := syntax.String(make([]byte, md5.Size))
	return syntax.Array{zero, zero}
}

func deflate(chunks ...[]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	for _, c := range chunks {
		if _, err := zw.Write(c); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func appendPadded(b []byte, v int64, width int) []byte {
	var tmp [20]byte
	s := strconv.AppendInt(tmp[:0], v, 10)
	for range width - len(s) {
		b = append(b, '0')
	}
	return append(b, s...)
}

// layout accumulates the bytes of a file as spans: small pieces copied into
// one arena, and large stream data referenced where it lives.
type layout struct {
	arena []byte
	spans []span
	pos   int64
}

type span struct {
	ext      []byte // data referenced in place; nil for an arena span
	body     Body   // or data written by a Body
	off, end int    // the arena range, when ext and body are nil
}

func (l *layout) inline(b []byte) {
	off := len(l.arena)
	l.arena = append(l.arena, b...)
	if n := len(l.spans); n > 0 && l.spans[n-1].ext == nil && l.spans[n-1].body == nil && l.spans[n-1].end == off {
		l.spans[n-1].end = len(l.arena)
	} else {
		l.spans = append(l.spans, span{off: off, end: len(l.arena)})
	}
	l.pos += int64(len(b))
}

func (l *layout) external(b []byte) {
	if len(b) < inlineMax {
		l.inline(b)
		return
	}
	l.spans = append(l.spans, span{ext: b})
	l.pos += int64(len(b))
}

// object writes one indirect object.
func (l *layout) object(num int, e *entry) {
	b := strconv.AppendInt(nil, int64(num), 10)
	b = append(b, " 0 obj\n"...)
	if !e.stream {
		b = syntax.Append(b, e.obj)
		b = append(b, "\nendobj\n"...)
		l.inline(b)
		return
	}
	var length int64
	for _, c := range e.data {
		length += int64(len(c))
	}
	if e.body != nil {
		length = e.body.Len()
	}
	d := e.dict.Clone()
	d.Set("Length", syntax.Int(length))
	b = syntax.Append(b, d)
	// The end-of-line after "stream" must be LF or CRLF, never CR alone, and
	// the one before "endstream" is not part of the data (section 7.3.8.1).
	b = append(b, "\nstream\n"...)
	l.inline(b)
	for _, c := range e.data {
		l.external(c)
	}
	if e.body != nil {
		l.spans = append(l.spans, span{body: e.body})
		l.pos += e.body.Len()
	}
	l.inline([]byte("\nendstream\nendobj\n"))
}

// file turns the layout into a File whose last part is final.
func (l *layout) file(final []byte) (*File, error) {
	f := &File{final: final, size: l.pos + int64(len(final))}
	for _, s := range l.spans {
		switch {
		case s.body != nil:
			f.parts = append(f.parts, part{body: s.body})
		case s.ext != nil:
			f.parts = append(f.parts, part{data: s.ext})
		default:
			f.parts = append(f.parts, part{data: l.arena[s.off:s.end]})
		}
	}
	// The identifier's two strings are the only hexadecimal strings in the
	// trailer, so the first two runs of 32 zero digits are theirs.
	ph := []byte("<" + string(bytes.Repeat([]byte("0"), 2*md5.Size)) + ">")
	at := 0
	for i := range f.idAt {
		j := bytes.Index(final[at:], ph)
		if j < 0 {
			return nil, errors.New("writer: identifier placeholder missing")
		}
		f.idAt[i] = at + j + 1
		at += j + len(ph)
	}
	return f, nil
}

// File is a laid-out PDF file, ready to be written.
type File struct {
	parts []part
	final []byte // the trailer, or the cross-reference stream, holding the identifier
	idAt  [2]int // where the identifier's two hexadecimal strings start in final
	size  int64
}

// Size returns the exact number of bytes WriteTo writes.
func (f *File) Size() int64 { return f.size }

// WriteTo writes the file to dst. The identifier is the MD5 of every byte
// before the trailer, computed as they are written, so it depends on the
// content alone; WriteTo can be called again and writes the same bytes.
func (f *File) WriteTo(dst io.Writer) (int64, error) {
	h := md5.New()
	both := io.MultiWriter(dst, h)
	var n int64
	for _, p := range f.parts {
		if p.body != nil {
			m, err := p.body.WriteTo(both)
			n += m
			if err != nil {
				return n, err
			}
			if m != p.body.Len() {
				return n, errors.New("writer: stream body wrote " + strconv.FormatInt(m, 10) + " bytes, not " + strconv.FormatInt(p.body.Len(), 10))
			}
			continue
		}
		h.Write(p.data)
		m, err := dst.Write(p.data)
		n += int64(m)
		if err != nil {
			return n, err
		}
	}
	var sum [md5.Size]byte
	h.Sum(sum[:0])
	final := append([]byte(nil), f.final...)
	for _, at := range f.idAt {
		for i, c := range sum {
			final[at+2*i] = hexDigit[c>>4]
			final[at+2*i+1] = hexDigit[c&15]
		}
	}
	m, err := dst.Write(final)
	n += int64(m)
	return n, err
}

const hexDigit = "0123456789ABCDEF"

// part is a piece of a File: bytes, or a Body.
type part struct {
	data []byte
	body Body
}

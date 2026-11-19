package writer

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
	"github.com/wisebrian/pdfo/internal/syntax"
)

// sample builds a small document with two pages, a shared content stream and
// an information dictionary.
func sample(t *testing.T, opts Options) *File {
	t.Helper()
	w := New()
	catalog := w.Alloc()
	pages := w.Alloc()
	content := w.AddStream(new(syntax.Dict), []byte("0 0 1 rg 100 100 200 200 re f\n"))
	var kids syntax.Array
	for range 2 {
		page := new(syntax.Dict)
		page.Set("Type", syntax.Name("Page"))
		page.Set("Parent", pages)
		page.Set("MediaBox", syntax.Array{syntax.Int(0), syntax.Int(0), syntax.Int(612), syntax.Int(792)})
		page.Set("Resources", new(syntax.Dict))
		page.Set("Contents", content)
		kids = append(kids, w.Add(page))
	}
	pd := new(syntax.Dict)
	pd.Set("Type", syntax.Name("Pages"))
	pd.Set("Kids", kids)
	pd.Set("Count", syntax.Int(len(kids)))
	w.Put(pages, pd)
	cd := new(syntax.Dict)
	cd.Set("Type", syntax.Name("Catalog"))
	cd.Set("Pages", pages)
	w.Put(catalog, cd)
	info := new(syntax.Dict)
	info.Set("Producer", syntax.String("writer test"))
	opts.Root = catalog
	opts.Info = w.Add(info)
	opts.Version = "1.7"
	f, err := w.Finish(opts)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func write(t *testing.T, f *File) []byte {
	t.Helper()
	var buf bytes.Buffer
	n, err := f.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != f.Size() || int64(buf.Len()) != f.Size() {
		t.Fatalf("Size() = %d, WriteTo wrote %d (returned %d)", f.Size(), buf.Len(), n)
	}
	return buf.Bytes()
}

func TestFinish(t *testing.T) {
	for _, mode := range []struct {
		name string
		opts Options
	}{
		{"xref-stream", Options{}},
		{"xref-table", Options{XRefTable: true}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			f := sample(t, mode.opts)
			pdf := write(t, f)
			if !bytes.HasPrefix(pdf, []byte("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")) {
				t.Errorf("bad header: %q", pdf[:16])
			}
			if !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
				t.Errorf("bad end: %q", pdf[len(pdf)-16:])
			}
			if again := write(t, f); !bytes.Equal(pdf, again) {
				t.Error("WriteTo is not repeatable")
			}
			if other := write(t, sample(t, mode.opts)); !bytes.Equal(pdf, other) {
				t.Error("output is not deterministic")
			}
			if bytes.Contains(pdf, []byte("<00000000000000000000000000000000>")) {
				t.Error("identifier placeholder left in the file")
			}
			pdftest.Check(t, mode.name, pdf)

			var doc struct {
				QPDF []map[string]any `json:"qpdf"`
			}
			if err := json.Unmarshal(pdftest.QPDFJSON(t, pdf), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.QPDF) != 2 {
				t.Fatalf("unexpected qpdf JSON: %d sections", len(doc.QPDF))
			}
			objs := doc.QPDF[1]
			trailer := objs["trailer"].(map[string]any)["value"].(map[string]any)
			id := trailer["/ID"].([]any)
			if len(id) != 2 || id[0] != id[1] || !strings.HasPrefix(id[0].(string), "b:") {
				t.Errorf("bad /ID: %v", id)
			}
		})
	}
}

func TestObjectStreams(t *testing.T) {
	// 250 small objects need three object streams; the streams must still
	// be found and parsed by an independent reader.
	w := New()
	catalog := w.Alloc()
	pages := w.Alloc()
	var extra syntax.Array
	for i := range 250 {
		extra = append(extra, w.Add(syntax.Int(i)))
	}
	pd := new(syntax.Dict)
	pd.Set("Type", syntax.Name("Pages"))
	pd.Set("Kids", syntax.Array{})
	pd.Set("Count", syntax.Int(0))
	w.Put(pages, pd)
	cd := new(syntax.Dict)
	cd.Set("Type", syntax.Name("Catalog"))
	cd.Set("Pages", pages)
	cd.Set("Extra", extra)
	w.Put(catalog, cd)
	f, err := w.Finish(Options{Version: "1.7", Root: catalog})
	if err != nil {
		t.Fatal(err)
	}
	pdf := write(t, f)
	if got := bytes.Count(pdf, []byte("/Type/ObjStm")); got != 3 {
		t.Errorf("%d object streams, want 3", got)
	}
	out := pdftest.QPDFJSON(t, pdf)
	if !bytes.Contains(out, []byte(`"obj:252 0 R": {`)) || !bytes.Contains(out, []byte(`"value": 249`)) {
		t.Errorf("qpdf did not find every object")
	}
}

func TestMisuse(t *testing.T) {
	w := New()
	r := w.Alloc()
	w.Put(syntax.Ref{Num: 7}, syntax.Int(1)) // never allocated
	w.Put(r, syntax.Int(1))
	if _, err := w.Finish(Options{Version: "1.7", Root: r}); err == nil {
		t.Error("Put of an unallocated object not reported")
	}

	w = New()
	r = w.Alloc()
	w.Alloc() // never set
	w.Put(r, syntax.Int(1))
	if _, err := w.Finish(Options{Version: "1.7", Root: r}); err == nil {
		t.Error("an object that was never set not reported")
	}

	w = New()
	r = w.Alloc()
	w.Put(r, &syntax.Stream{})
	if _, err := w.Finish(Options{Version: "1.7", Root: r}); err == nil {
		t.Error("a stream set with Put not reported")
	}
}

func TestLargeStreamDataIsNotCopied(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 2*inlineMax)
	w := New()
	catalog := w.Alloc()
	data := w.AddStream(new(syntax.Dict), big)
	cd := new(syntax.Dict)
	cd.Set("Type", syntax.Name("Catalog"))
	cd.Set("Data", data)
	w.Put(catalog, cd)
	f, err := w.Finish(Options{Version: "1.7", Root: catalog})
	if err != nil {
		t.Fatal(err)
	}
	shared := false
	for _, p := range f.parts {
		if len(p) > 0 && &p[0] == &big[0] {
			shared = true
		}
	}
	if !shared {
		t.Error("large stream data was copied instead of referenced")
	}
}

func TestChunks(t *testing.T) {
	var c Chunks
	var want []byte
	for i, n := range []int{0, 1, 100, 5000, chunkSize, 3*chunkSize + 17, 9} {
		p := bytes.Repeat([]byte{byte(i)}, n)
		if k, err := c.Write(p); k != n || err != nil {
			t.Fatalf("Write(%d) = %d, %v", n, k, err)
		}
		want = append(want, p...)
	}
	got := bytes.Join(c.Blocks(), nil)
	if !bytes.Equal(got, want) || c.Len() != len(want) {
		t.Fatalf("Chunks holds %d bytes, want %d", len(got), len(want))
	}
	for i, b := range c.Blocks() {
		if len(b) > chunkSize || (i < len(c.Blocks())-1 && len(b) != chunkSize) {
			t.Errorf("block %d has %d bytes", i, len(b))
		}
	}
}

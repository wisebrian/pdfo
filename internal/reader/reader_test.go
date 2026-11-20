package reader

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisebrian/pdfo/internal/crypt"
	"github.com/wisebrian/pdfo/internal/pdftest"
	"github.com/wisebrian/pdfo/internal/syntax"
)

var testLimits = Limits{MaxDepth: 100, MaxStreamSize: 64 << 20, MaxDecoded: 1 << 30}

func corpus(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pdf", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustOpen(t *testing.T, data []byte, pw string) *Reader {
	t.Helper()
	r, err := Open(data, pw, testLimits)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// content returns a page's decoded content.
func content(t *testing.T, r *Reader, p *Page) []byte {
	t.Helper()
	var out []byte
	c := r.Resolve(p.Dict.Get("Contents"))
	list, ok := c.(syntax.Array)
	if !ok {
		list = syntax.Array{p.Dict.Get("Contents")}
	}
	for _, x := range list {
		s, ok := r.Resolve(x).(*syntax.Stream)
		if !ok {
			continue
		}
		data, err := r.Decode(s)
		if err != nil {
			t.Fatalf("page content: %v", err)
		}
		out = append(out, data...)
	}
	return out
}

func TestCorpusBasic(t *testing.T) {
	want := []struct {
		w, h float64
		rot  int
	}{{595, 842, 0}, {612, 792, 90}, {595, 420, 0}}
	ref := mustOpen(t, corpus(t, "basic.pdf"), "")
	refPages := ref.Pages()
	files, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "pdf", "*.pdf"))
	for _, f := range files {
		name := filepath.Base(f)
		if !strings.HasPrefix(name, "basic") && !strings.HasPrefix(name, "enc-") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			pw := ""
			if strings.Contains(name, "-user") {
				pw = "user"
			}
			r := mustOpen(t, corpus(t, name), pw)
			if r.Repaired {
				t.Errorf("needed repair: %v", r.Warnings)
			}
			pages := r.Pages()
			if len(pages) != 3 {
				t.Fatalf("%d pages", len(pages))
			}
			for i, p := range pages {
				if p.MediaBox.W() != want[i].w || p.MediaBox.H() != want[i].h || p.Rotate != want[i].rot {
					t.Errorf("page %d: %v rotate %d", i+1, p.MediaBox, p.Rotate)
				}
				if !bytes.Equal(content(t, r, p), content(t, ref, refPages[i])) {
					t.Errorf("page %d content differs from basic.pdf", i+1)
				}
			}
			info, _ := r.Resolve(r.Trailer.Get("Info")).(*syntax.Dict)
			if title, _ := info.Get("Title").(syntax.String); syntax.Text(title) != "pdfo test document" {
				t.Errorf("title %q", syntax.Text(title))
			}
		})
	}
}

func TestEncryption(t *testing.T) {
	desc := map[string]string{
		"enc-r2-rc4-40.pdf":                     "RC4 40-bit",
		"enc-r3-rc4-128-user.pdf":               "RC4 128-bit",
		"enc-r4-aes-128.pdf":                    "AES-128",
		"enc-r5-aes-256-user.pdf":               "AES-256",
		"enc-r6-aes-256-user.pdf":               "AES-256",
		"enc-r6-aes-256-cleartext-metadata.pdf": "AES-256",
	}
	for name, d := range desc {
		data := corpus(t, name)
		user := strings.Contains(name, "-user")
		if user {
			if _, err := Open(data, "", testLimits); !errors.Is(err, ErrNoPassword) {
				t.Errorf("%s without password: %v", name, err)
			}
			if _, err := Open(data, "wrong", testLimits); !errors.Is(err, crypt.ErrPassword) {
				t.Errorf("%s with a wrong password: %v", name, err)
			}
		}
		for _, pw := range []string{"owner", map[bool]string{true: "user", false: ""}[user]} {
			r, err := Open(data, pw, testLimits)
			if err != nil {
				t.Errorf("%s with %q: %v", name, pw, err)
				continue
			}
			if r.Crypt == nil || r.Crypt.Description != d {
				t.Errorf("%s: %+v", name, r.Crypt)
			}
		}
	}
	// With EncryptMetadata false the XMP stream is stored in clear.
	r := mustOpen(t, corpus(t, "enc-r6-aes-256-cleartext-metadata.pdf"), "")
	meta, ok := r.Resolve(r.Catalog().Get("Metadata")).(*syntax.Stream)
	if !ok {
		t.Fatal("no metadata stream")
	}
	if xml, err := r.Decode(meta); err != nil || !bytes.Contains(xml, []byte("<?xpacket")) {
		t.Errorf("clear-text metadata: %q, %v", xml[:min(len(xml), 40)], err)
	}
}

func TestCorpusOthers(t *testing.T) {
	r := mustOpen(t, corpus(t, "lo-forms.pdf"), "")
	if n := len(r.Pages()); n != 2 {
		t.Errorf("LibreOffice file: %d pages", n)
	}
	r = mustOpen(t, corpus(t, "chrome.pdf"), "")
	if n := len(r.Pages()); n < 1 {
		t.Errorf("Chrome file: %d pages", n)
	}
}

// simple is a small valid document: catalog, page tree, one page with
// content.
func simple(pageExtra string) pdftest.Doc {
	return pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		pdftest.Page("/MediaBox[0 0 200 100]/Contents 4 0 R " + pageExtra),
		pdftest.Stream("", []byte("0 0 1 rg 10 10 50 50 re f"), true),
	}}
}

func TestInheritance(t *testing.T) {
	d := pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[5 0 R 3 0 R]/Count 3/MediaBox[0 0 300 400]/Rotate 90/Resources<</Font<<>>>>>>",
		"<</Type/Page/Parent 2 0 R/CropBox[10 10 5000 390]/Rotate -90>>",
		"<</Type/Page/Parent 5 0 R/MediaBox[-50 -50 50 50]/UserUnit 2>>",
		"<</Type/Pages/Parent 2 0 R/Kids[4 0 R]/Count 1/Rotate 450>>",
	}}
	r := mustOpen(t, d.Bytes(), "")
	pages := r.Pages()
	if len(pages) != 2 {
		t.Fatalf("%d pages", len(pages))
	}
	p := pages[0] // object 4, inheriting from 5 and 2
	if p.MediaBox != (Rect{-50, -50, 50, 50}) || p.Rotate != 90 || p.UserUnit != 2 || p.Resources == nil {
		t.Errorf("page 1: %+v", p)
	}
	p = pages[1]
	if p.MediaBox != (Rect{0, 0, 300, 400}) || p.CropBox != (Rect{10, 10, 300, 390}) || p.Rotate != 270 {
		t.Errorf("page 2: %+v", p)
	}
}

func TestRepair(t *testing.T) {
	good := simple("").Bytes()
	cases := map[string][]byte{
		// Every offset off by 7, as after a careless edit.
		"shifted":            bytes.Replace(good, []byte("%\xE2\xE3\xCF\xD3\n"), []byte("%\xE2\xE3\xCF\xD3\n% junk\n"), 1),
		"no xref":            simple("").Bytes()[:bytes.Index(good, []byte("xref"))],
		"junk before header": append([]byte("garbage line\r\n"), good...),
		"wrong startxref":    bytes.Replace(good, []byte("startxref\n"), []byte("startxref\n9"), 1),
		"truncated":          good[:len(good)-30],
	}
	for name, data := range cases {
		r, err := Open(data, "", testLimits)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		pages := r.Pages()
		if len(pages) != 1 || len(content(t, r, pages[0])) == 0 {
			t.Errorf("%s: %d pages", name, len(pages))
		}
	}
	if _, err := Open([]byte("not a pdf at all"), "", testLimits); !errors.Is(err, ErrNotPDF) {
		t.Errorf("not a PDF: %v", err)
	}
	if _, err := Open([]byte("%PDF-1.7\nnothing here"), "", testLimits); err == nil {
		t.Error("a file without objects opened")
	}
}

func TestLoops(t *testing.T) {
	cases := map[string]pdftest.Doc{
		"kids loop": {Objects: []string{
			"<</Type/Catalog/Pages 2 0 R>>",
			"<</Type/Pages/Kids[3 0 R 2 0 R]/Count 2>>",
			pdftest.Page("/MediaBox[0 0 100 100]"),
		}},
		"self parent": {Objects: []string{
			"<</Type/Catalog/Pages 2 0 R>>",
			"<</Type/Pages/Kids[3 0 R]/Count 1/Parent 2 0 R>>",
			"<</Type/Page/Parent 3 0 R/MediaBox[0 0 100 100]>>",
		}},
		"self length": {Objects: []string{
			"<</Type/Catalog/Pages 2 0 R>>",
			"<</Type/Pages/Kids[3 0 R]/Count 1>>",
			pdftest.Page("/MediaBox[0 0 100 100]/Contents 4 0 R"),
			"<</Length 4 0 R>>stream\nabc\nendstream",
		}},
		"reference chain": {Objects: []string{
			"<</Type/Catalog/Pages 2 0 R>>",
			"<</Type/Pages/Kids[3 0 R]/Count 1>>",
			pdftest.Page("/MediaBox 4 0 R"),
			"5 0 R",
			"4 0 R",
		}},
	}
	for name, d := range cases {
		r, err := Open(d.Bytes(), "", testLimits)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if n := len(r.Pages()); n != 1 {
			t.Errorf("%s: %d pages", name, n)
		}
	}
	// /Prev pointing at itself.
	data := simple("").Bytes()
	start := pdftest.LastStartXRef(data)
	data = bytes.Replace(data, []byte("trailer\n<<"), []byte("trailer\n<</Prev "+itoa(start)), 1)
	if r, err := Open(data, "", testLimits); err != nil || len(r.Pages()) != 1 {
		t.Errorf("Prev loop: %v", err)
	}
}

func itoa(i int) string { return string(syntax.Append(nil, syntax.Int(i))) }

func TestIncrementalUpdate(t *testing.T) {
	base := simple("").Bytes()
	upd := pdftest.Update(base, map[int]string{
		3: pdftest.Page("/MediaBox[0 0 400 300]/Contents 5 0 R"),
		5: pdftest.Stream("", []byte("1 0 0 rg 0 0 10 10 re f"), false),
	}, 6, "")
	r := mustOpen(t, upd, "")
	p := r.Pages()[0]
	if p.MediaBox.W() != 400 || string(content(t, r, p)) != "1 0 0 rg 0 0 10 10 re f" {
		t.Errorf("update not applied: %v %q", p.MediaBox, content(t, r, p))
	}
	if r.Repaired {
		t.Errorf("repaired: %v", r.Warnings)
	}
	// A deletion in the update hides the old object.
	del := bytes.Replace(upd, []byte("trailer\n<</Size 6"), []byte("trailer\n<</Size 6"), 1)
	_ = del
}

func TestObjectStreamAndXRefStream(t *testing.T) {
	// The catalog and page tree live in an object stream; a
	// cross-reference stream indexes everything.
	objs := []string{"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[3 0 R]/Count 1>>", pdftest.Page("/MediaBox[0 0 50 60]")}
	var head, body string
	for i, o := range objs {
		head += itoa(i+1) + " " + itoa(len(body)) + " "
		body += o + "\n"
	}
	stm := pdftest.Stream("/Type/ObjStm/N 3/First "+itoa(len(head)), []byte(head+body), true)
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	stmOff := b.Len()
	b.WriteString("4 0 obj\n" + stm + "\nendobj\n")
	xrefOff := b.Len()
	rows := []byte{0, 0, 0, 0xFF, 0xFF}
	for i := range 3 {
		rows = append(rows, 2, 0, 4, 0, byte(i))
	}
	rows = append(rows, 1, byte(stmOff>>8), byte(stmOff), 0, 0)
	rows = append(rows, 1, byte(xrefOff>>8), byte(xrefOff), 0, 0)
	b.WriteString("5 0 obj\n" + pdftest.Stream("/Type/XRef/Size 6/W[1 2 2]/Root 1 0 R", rows, true) + "\nendobj\n")
	b.WriteString("startxref\n" + itoa(xrefOff) + "\n%%EOF\n")
	r := mustOpen(t, b.Bytes(), "")
	if p := r.Pages(); len(p) != 1 || p[0].MediaBox.H() != 60 || r.Repaired {
		t.Errorf("pages %v, repaired %v %v", p, r.Repaired, r.Warnings)
	}
	// Broken offsets in the cross-reference stream: the scan finds the
	// object stream and indexes its objects.
	broken := bytes.Replace(b.Bytes(), []byte("4 0 obj"), []byte("  4 0 obj"), 1)
	r2, err := Open(broken, "", testLimits)
	if err != nil {
		t.Fatal(err)
	}
	if p := r2.Pages(); len(p) != 1 || p[0].MediaBox.H() != 60 {
		t.Errorf("rebuilt: %v %v", p, r2.Warnings)
	}
}

func TestHybrid(t *testing.T) {
	// A classic table that lists objects 1–3 and a cross-reference stream,
	// named by XRefStm, that adds object 4: the page's content.
	d := simple("")
	data := d.Bytes()
	cOff := bytes.Index(data, []byte("4 0 obj"))
	rows := []byte{1, byte(cOff >> 8), byte(cOff), 0, 0}
	start := pdftest.LastStartXRef(data)
	var b bytes.Buffer
	b.Write(data[:start])
	xs := b.Len()
	b.WriteString("6 0 obj\n" + pdftest.Stream("/Type/XRef/Size 7/W[1 2 2]/Index[4 1]", rows, false) + "\nendobj\n")
	table := string(data[start:])
	table = strings.Replace(table, "0000000000 65535 f\r\n", "0000000000 65535 f\r\n", 1)
	// Mark object 4 free in the table, as hybrid files do.
	lines := strings.Split(table, "\r\n")
	lines[4] = "0000000000 00001 f" // object 4
	table = strings.Join(lines, "\r\n")
	table = strings.Replace(table, "trailer\n<<", "trailer\n<</XRefStm "+itoa(xs)+" ", 1)
	off := b.Len()
	table = table[:strings.LastIndex(table, "startxref")] + "startxref\n" + itoa(off) + "\n%%EOF\n"
	b.WriteString(table)
	r := mustOpen(t, b.Bytes(), "")
	if r.Repaired || len(content(t, r, r.Pages()[0])) == 0 {
		t.Errorf("hybrid file: repaired %v, warnings %v", r.Repaired, r.Warnings)
	}
}

func TestFiltersInFile(t *testing.T) {
	d := simple("")
	d.Objects[3] = "<</Length 34/Filter[/ASCIIHexDecode]>>stream\n3020302031207267203020302031302072\nendstream"
	r := mustOpen(t, d.Bytes(), "")
	if got := string(content(t, r, r.Pages()[0])); got != "0 0 1 rg 0 0 10 r" {
		t.Errorf("ASCIIHex content: %q", got)
	}
}

func TestLimits(t *testing.T) {
	// An object stream that decompresses to 64 MiB of nothing.
	d := simple("")
	bomb := pdftest.Stream("/Type/ObjStm/N 1/First 4", append([]byte("1 0 "), make([]byte, 64<<20)...), true)
	d.Objects = append(d.Objects, bomb)
	small := Limits{MaxDepth: 50, MaxStreamSize: 1 << 20, MaxDecoded: 4 << 20}
	r, err := Open(bytes.Replace(d.Bytes(), []byte("/Pages 2 0 R"), []byte("/Pages 2 0 R/X 5 0 R"), 1), "", small)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := r.Object(5).(*syntax.Stream); ok {
		if _, err := r.Decode(s); err == nil {
			t.Error("a bomb decoded")
		}
	}
	deep := simple("/Deep " + strings.Repeat("[", 500) + strings.Repeat("]", 500))
	if r, err := Open(deep.Bytes(), "", small); err == nil {
		r.Pages() // must not panic
	}
}

func FuzzOpen(f *testing.F) {
	for _, name := range []string{"basic.pdf", "basic-table.pdf", "enc-r4-aes-128.pdf"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pdf", name))
		if err == nil {
			f.Add(b)
		}
	}
	f.Add(simple("").Bytes())
	f.Add(simple("").Bytes()[:200])
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := Limits{MaxDepth: 50, MaxStreamSize: 1 << 20, MaxDecoded: 8 << 20}
		pdftest.Bounded(t, len(data), func() {
			r, err := Open(data, "", lim)
			if err != nil {
				return
			}
			for _, p := range r.Pages() {
				r.Resolve(p.Dict.Get("Contents"))
				if s, ok := r.Resolve(p.Dict.Get("Contents")).(*syntax.Stream); ok {
					r.Decode(s)
				}
			}
		})
	})
}

func FuzzRebuild(f *testing.F) {
	f.Add(simple("").Bytes())
	f.Add([]byte("%PDF-1.4\n1 0 obj <</Type/Catalog/Pages 2 0 R>> endobj 2 0 obj <</Type/Pages/Kids[3 0 R]>> endobj 3 0 obj <</Type/Page>> endobj trailer <</Root 1 0 R>>"))
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := Limits{MaxDepth: 50, MaxStreamSize: 1 << 20, MaxDecoded: 8 << 20}
		data = append([]byte("%PDF-1.7\n"), data...)
		pdftest.Bounded(t, len(data), func() {
			r := &Reader{data: data, xref: map[uint32]entry{}, cache: map[uint32]syntax.Object{},
				inProgress: map[uint32]bool{}, objstms: map[uint32]*objStm{}, limits: lim}
			r.budget = newBudget(lim)
			r.rebuild()
			r.Pages()
		})
	})
}

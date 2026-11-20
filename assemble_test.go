package pdfo

import (
	"bytes"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
	"github.com/wisebrian/pdfo/internal/syntax"
)

func corpusPDF(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "pdf", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func openDoc(t *testing.T, data []byte, pw string) *Document {
	t.Helper()
	d, err := Open(data, &OpenOptions{Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// finish checks what every output must satisfy and returns its bytes,
// reopened.
func finish(t *testing.T, out *Output, err error) ([]byte, *Document) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	pdf := out.Bytes()
	if int64(len(pdf)) != out.Size() {
		t.Fatalf("Size %d, wrote %d", out.Size(), len(pdf))
	}
	pdftest.Check(t, t.Name(), pdf)
	return pdf, openDoc(t, pdf, "")
}

func hasWarning(out *Output, code string) bool {
	for _, w := range out.Report.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// sameRender compares a page of two files as mutool draws them.
func sameRender(t *testing.T, a []byte, pa int, b []byte, pb int) {
	t.Helper()
	ia := pdftest.Render(t, a, pa, 36)
	ib := pdftest.Render(t, b, pb, 36)
	if ia.Bounds() != ib.Bounds() {
		t.Errorf("page %d renders %v, output page %d renders %v", pa, ia.Bounds(), pb, ib.Bounds())
		return
	}
	if d := diff(ia, ib); d > 0.002 {
		t.Errorf("page %d and output page %d differ in %.2f%% of pixels", pa, pb, 100*d)
	}
}

// diff returns the fraction of pixels whose colour differs noticeably.
func diff(a, b *image.NRGBA) float64 {
	n := 0
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p, q := a.NRGBAAt(x, y), b.NRGBAAt(x, y)
			if !near(p, q, 24) {
				n++
			}
		}
	}
	return float64(n) / float64(r.Dx()*r.Dy())
}

func TestExtractKeepsPagesAndPrunesDocument(t *testing.T) {
	src := corpusPDF(t, "basic.pdf")
	d := openDoc(t, src, "")
	out, err := ExtractPages(d, []int{2, 0}, nil)
	pdf, got := finish(t, out, err)
	if got.NumPages() != 2 {
		t.Fatalf("%d pages", got.NumPages())
	}
	sameRender(t, src, 3, pdf, 1)
	sameRender(t, src, 1, pdf, 2)
	info := got.Info()
	if info.Pages[0].SizeName != "A5" || info.Pages[1].SizeName != "A4" || !info.Bookmarks || !info.Attachments {
		t.Errorf("info: %+v", info)
	}
	// Chapter 2's page is gone, so is its bookmark; the others remain.
	if !hasWarning(out, "bookmarks-dropped") {
		t.Error("no warning for the dropped bookmark")
	}
	titles := outlineTitles(t, got)
	if strings.Join(titles, "|") != "Chapter 1|Section 1.1|Chapter 3" {
		t.Errorf("bookmarks %q", titles)
	}
	// The link on page one, now output page 2, goes to page three, now
	// output page 1.
	if dest := firstLinkDest(t, got, 2); dest != got.pages[0].Ref {
		t.Errorf("link goes to %v, want %v", dest, got.pages[0].Ref)
	}
}

func outlineTitles(t *testing.T, d *Document) []string {
	t.Helper()
	var out []string
	r := d.r
	root, _ := r.Resolve(r.Catalog().Get("Outlines")).(*syntax.Dict)
	var walk func(o syntax.Object, depth int)
	walk = func(o syntax.Object, depth int) {
		for n := o; n != nil && depth < 10 && len(out) < 100; {
			it, ok := r.Resolve(n).(*syntax.Dict)
			if !ok {
				return
			}
			out = append(out, text(r, it.Get("Title")))
			walk(it.Get("First"), depth+1)
			n = it.Get("Next")
		}
	}
	walk(root.Get("First"), 0)
	return out
}

// firstLinkDest returns the page a link with a destination on page p
// (numbered from 1) points to.
func firstLinkDest(t *testing.T, d *Document, p int) syntax.Ref {
	t.Helper()
	r := d.r
	annots, _ := r.Resolve(d.pages[p-1].Dict.Get("Annots")).(syntax.Array)
	for _, a := range annots {
		ad, _ := r.Resolve(a).(*syntax.Dict)
		if st, _ := ad.Get("Subtype").(syntax.Name); st != "Link" {
			continue
		}
		if dest, ok := r.Resolve(ad.Get("Dest")).(syntax.Array); ok && len(dest) > 0 {
			ref, _ := dest[0].(syntax.Ref)
			return ref
		}
	}
	return syntax.Ref{}
}

func TestDeleteMakesLinksInert(t *testing.T) {
	src := corpusPDF(t, "basic.pdf")
	out, err := DeletePages(openDoc(t, src, ""), []int{2}, nil)
	pdf, got := finish(t, out, err)
	if got.NumPages() != 2 || !hasWarning(out, "links-inert") {
		t.Errorf("%d pages, warnings %+v", got.NumPages(), out.Report.Warnings)
	}
	if dest := firstLinkDest(t, got, 1); !dest.IsZero() {
		t.Errorf("link to the deleted page still goes to %v", dest)
	}
	sameRender(t, src, 1, pdf, 1)
	sameRender(t, src, 2, pdf, 2)
}

func TestRotate(t *testing.T) {
	src := corpusPDF(t, "basic.pdf")
	out, err := RotatePages(openDoc(t, src, ""), []int{0, 1}, -90, nil)
	_, got := finish(t, out, err)
	info := got.Info()
	if info.Pages[0].Rotate != 270 || info.Pages[1].Rotate != 0 || info.Pages[2].Rotate != 0 {
		t.Errorf("rotations %+v", info.Pages)
	}
	if info.Pages[0].Width != 841.89 && info.Pages[0].Width != 842 {
		t.Errorf("turned A4 is %v wide", info.Pages[0].Width)
	}
	// The whole document is there, so everything document-wide is kept.
	if len(out.Report.Warnings) != 0 {
		t.Errorf("warnings: %+v", out.Report.Warnings)
	}
	if len(outlineTitles(t, got)) != 4 || !got.Info().JavaScript {
		t.Error("bookmarks or script lost")
	}
}

func TestMergeCombinesDocuments(t *testing.T) {
	basic := corpusPDF(t, "basic.pdf")
	forms := corpusPDF(t, "lo-forms.pdf")
	a, b, c := openDoc(t, basic, ""), openDoc(t, forms, ""), openDoc(t, basic, "")
	out, err := Merge([]*Document{a, b, c}, nil)
	pdf, got := finish(t, out, err)
	if got.NumPages() != 8 {
		t.Fatalf("%d pages", got.NumPages())
	}
	for i, p := range []int{1, 2, 3} {
		sameRender(t, basic, p, pdf, i+1)
		sameRender(t, basic, p, pdf, i+6)
	}
	sameRender(t, forms, 1, pdf, 4)
	info := got.Info()
	if !info.FormFields || info.JavaScript {
		t.Errorf("form %v, script %v", info.FormFields, info.JavaScript)
	}
	if !hasWarning(out, "javascript-dropped") || !hasWarning(out, "tags-dropped") {
		t.Errorf("warnings %+v", out.Report.Warnings)
	}
	// basic.pdf has four bookmarks; LibreOffice made one.
	if n := len(outlineTitles(t, got)); n != 4+1+4 {
		t.Errorf("%d bookmarks", n)
	}
	// Both copies of basic.pdf bring an attachment of the same name.
	var names []string
	ns, _ := got.r.Resolve(got.r.Catalog().Get("Names")).(*syntax.Dict)
	eachName(got.r, ns.Get("EmbeddedFiles"), 10, func(k syntax.String, _ syntax.Object) bool {
		names = append(names, syntax.Text(k))
		return true
	})
	if strings.Join(names, "|") != "Original|pdfo-note.txt|pdfo-note.txt (2)" {
		t.Errorf("attachments %q", names)
	}
}

func TestFormsFlattenWhenCombined(t *testing.T) {
	forms := corpusPDF(t, "lo-forms.pdf")
	out, err := Merge([]*Document{openDoc(t, forms, ""), openDoc(t, forms, "")}, &WriteOptions{XRefTable: true})
	pdf, got := finish(t, out, err)
	if got.Info().FormFields || !hasWarning(out, "form-flattened") {
		t.Errorf("form kept: %+v", out.Report.Warnings)
	}
	if bytes.Contains(pdf, []byte("/Widget")) {
		t.Error("widgets left in a flattened form")
	}
	// Filled-in values stay visible: the pages look as before.
	sameRender(t, forms, 1, pdf, 1)
	sameRender(t, forms, 1, pdf, 3)
	// LibreOffice stores empty appearances and leaves drawing the values to
	// the viewer (NeedAppearances): flattening must draw them itself.
	if !bytes.Contains(pdf, []byte("(Ana Popescu) Tj")) {
		t.Error("the text field's value is not drawn")
	}
	// The check box's /AS names a state its appearances lack; its value
	// says it is on, so the "on" appearance must be drawn: dark pixels in
	// its rectangle, [57.5 688.9 65.6 697] on the A4 page.
	img := pdftest.Render(t, pdf, 1, 144)
	dark := 0
	x0, x1, y0, y1 := 57.5*2, 65.6*2, (842-697)*2.0, (842-688.9)*2
	for y := int(y0); y < int(y1); y++ {
		for x := int(x0); x < int(x1); x++ {
			if c := img.NRGBAAt(x, y); c.R < 100 {
				dark++
			}
		}
	}
	if dark < 10 {
		t.Errorf("the checked box is not drawn (%d dark pixels)", dark)
	}
}

func TestFormStaysInteractive(t *testing.T) {
	forms := corpusPDF(t, "lo-forms.pdf")
	out, err := ExtractPages(openDoc(t, forms, ""), []int{0}, nil)
	pdf, got := finish(t, out, err)
	if !got.Info().FormFields || hasWarning(out, "form-flattened") {
		t.Errorf("form lost: %+v", out.Report.Warnings)
	}
	form, _ := got.r.Resolve(got.r.Catalog().Get("AcroForm")).(*syntax.Dict)
	if fields, _ := got.r.Resolve(form.Get("Fields")).(syntax.Array); len(fields) != 2 {
		t.Errorf("%d fields", len(fields))
	}
	sameRender(t, forms, 1, pdf, 1)
}

func TestDuplicatePages(t *testing.T) {
	src := corpusPDF(t, "basic.pdf")
	out, err := ExtractPages(openDoc(t, src, ""), []int{0, 0}, nil)
	pdf, got := finish(t, out, err)
	a1, _ := got.r.Resolve(got.pages[0].Dict.Get("Annots")).(syntax.Array)
	a2, _ := got.r.Resolve(got.pages[1].Dict.Get("Annots")).(syntax.Array)
	if len(a1) == 0 || len(a1) != len(a2) || a1[0] == a2[0] {
		t.Errorf("annotations shared between copies of a page: %v %v", a1, a2)
	}
	sameRender(t, src, 1, pdf, 2)
}

func TestResize(t *testing.T) {
	src := corpusPDF(t, "basic.pdf")
	d := openDoc(t, src, "")
	out, err := ResizePages(d, []int{0, 1, 2}, Resize{To: A4}, nil)
	pdf, got := finish(t, out, err)
	for i, p := range got.Info().Pages {
		if p.SizeName != "A4" {
			t.Errorf("page %d is %v×%v", i+1, p.Width, p.Height)
		}
	}
	// A5 landscape becomes A4 landscape: scaled by √2 and centred, the
	// red square in its corner must be scaled and moved alike.
	img := pdftest.Render(t, pdf, 3, 72)
	s := 841.89 / 595.0
	if s2 := 595.28 / 420; s2 < s {
		s = s2
	}
	cx := (841.89-595*s)/2 + 40*s
	cy := (595.28-420*s)/2 + 40*s
	px, py := int(cx), img.Bounds().Dy()-int(cy)
	if c := img.NRGBAAt(px, py); c.R < 180 || c.G > 100 {
		t.Errorf("corner square not at (%d,%d): %v", px, py, c)
	}
	// Links move with the content.
	annots, _ := got.r.Resolve(got.pages[0].Dict.Get("Annots")).(syntax.Array)
	if len(annots) == 0 {
		t.Fatal("annotations lost")
	}
	if !bytes.Contains(pdf, []byte("re W n")) {
		t.Error("no clip to the old visible area")
	}

	half, err := ResizePages(d, []int{0}, Resize{Scale: 0.5}, nil)
	_, got = finish(t, half, err)
	if p := got.Info().Pages[0]; p.Width < 297 || p.Width > 298.5 {
		t.Errorf("half page is %v wide", p.Width)
	}
}

func TestEncryptedInputs(t *testing.T) {
	basic := corpusPDF(t, "basic.pdf")
	for name, pw := range map[string]string{
		"enc-r2-rc4-40.pdf": "", "enc-r3-rc4-128-user.pdf": "user", "enc-r4-aes-128.pdf": "",
		"enc-r5-aes-256-user.pdf": "user", "enc-r6-aes-256-user.pdf": "owner",
	} {
		t.Run(name, func(t *testing.T) {
			d := openDoc(t, corpusPDF(t, name), pw)
			if !d.Info().Encrypted {
				t.Error("not reported as encrypted")
			}
			out, err := ExtractPages(d, []int{0, 1, 2}, nil)
			pdf, got := finish(t, out, err)
			if !out.Report.InputEncrypted || got.Info().Encrypted {
				t.Error("encryption not reported or kept")
			}
			for p := 1; p <= 3; p++ {
				sameRender(t, basic, p, pdf, p)
			}
		})
	}
	data := corpusPDF(t, "enc-r6-aes-256-user.pdf")
	if _, err := Open(data, nil); !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("no password: %v", err)
	}
	if _, err := Open(data, &OpenOptions{Password: "nope"}); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong password: %v", err)
	}
}

func TestChromeNamedDests(t *testing.T) {
	src := corpusPDF(t, "chrome.pdf")
	d := openDoc(t, src, "")
	pages := make([]int, d.NumPages())
	for i := range pages {
		pages[i] = d.NumPages() - 1 - i
	}
	out, err := ReorderPages(d, pages, nil)
	pdf, got := finish(t, out, err)
	for i := range pages {
		sameRender(t, src, pages[i]+1, pdf, i+1)
	}
	if got.NumPages() != d.NumPages() {
		t.Errorf("%d pages", got.NumPages())
	}
}

func TestAssembleDeterministic(t *testing.T) {
	d := openDoc(t, corpusPDF(t, "lo-forms.pdf"), "")
	e := openDoc(t, corpusPDF(t, "basic.pdf"), "")
	pages := []PageRef{{Doc: e, Index: 1, Rotate: 90}, {Doc: d, Index: 0, Resize: &Resize{To: Letter}}, {Doc: e, Index: 1}}
	a, err := Assemble(pages, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Assemble(pages, nil)
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("Assemble is not deterministic")
	}
	pdftest.Check(t, "mixed", a.Bytes())
}

func TestAssembleErrors(t *testing.T) {
	d := openDoc(t, corpusPDF(t, "basic.pdf"), "")
	cases := map[string]func() error{
		"no pages":     func() error { _, err := Assemble(nil, nil); return err },
		"no document":  func() error { _, err := Assemble([]PageRef{{}}, nil); return err },
		"bad index":    func() error { _, err := ExtractPages(d, []int{3}, nil); return err },
		"bad rotation": func() error { _, err := RotatePages(d, []int{0}, 45, nil); return err },
		"delete all":   func() error { _, err := DeletePages(d, []int{0, 1, 2}, nil); return err },
		"bad order":    func() error { _, err := ReorderPages(d, []int{0, 0, 1}, nil); return err },
		"short order":  func() error { _, err := ReorderPages(d, []int{0, 1}, nil); return err },
		"bad scale":    func() error { _, err := ResizePages(d, []int{0}, Resize{Scale: -1}, nil); return err },
		"size and scale": func() error {
			_, err := ResizePages(d, []int{0}, Resize{To: A4, Scale: 2}, nil)
			return err
		},
		"split zero": func() error { _, err := SplitEvery(d, 0, nil); return err },
	}
	for name, f := range cases {
		var oe *OptionError
		if err := f(); !errors.As(err, &oe) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSplit(t *testing.T) {
	src := corpusPDF(t, "basic.pdf")
	d := openDoc(t, src, "")
	outs, err := SplitEvery(d, 1, nil)
	if err != nil || len(outs) != 3 {
		t.Fatalf("%d outputs, %v", len(outs), err)
	}
	for i, o := range outs {
		pdf, got := finish(t, o, nil)
		if got.NumPages() != 1 {
			t.Errorf("part %d has %d pages", i+1, got.NumPages())
		}
		sameRender(t, src, i+1, pdf, 1)
	}
	groups, _ := ParsePages("1-2, 3", 3)
	outs, err = Split(d, groups, nil)
	if err != nil || len(outs) != 2 {
		t.Fatalf("%d outputs, %v", len(outs), err)
	}
}

func FuzzAssemble(f *testing.F) {
	for _, name := range []string{"basic.pdf", "lo-forms.pdf"} {
		if b, err := os.ReadFile(filepath.Join("testdata", "pdf", name)); err == nil {
			f.Add(b)
		}
	}
	// Small files let the fuzzer mutate the structures that matter
	// without spending its time on fonts.
	f.Add(signed())
	f.Add(pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R/Outlines 6 0 R/AcroForm<</Fields[5 0 R]/NeedAppearances true>>/OCProperties<</OCGs[8 0 R]/D<</OFF[8 0 R]>>>>>>",
		"<</Type/Pages/Kids[3 0 R 9 0 R]/Count 2/MediaBox[0 0 200 200]>>",
		pdftest.Page("/Contents 4 0 R/Annots[5 0 R 10 0 R]/Rotate 90"),
		pdftest.Stream("", []byte("0 0 1 rg 0 0 50 50 re f"), false),
		"<</Type/Annot/Subtype/Widget/FT/Tx/T(t)/V(value)/DA(/Helv 0 Tf 0 g)/Rect[10 10 90 30]/P 3 0 R/AP<</N 11 0 R>>>>",
		"<</First 7 0 R/Last 7 0 R/Count 1>>",
		"<</Title(one)/Parent 6 0 R/Dest[9 0 R/XYZ 0 100 null]>>",
		"<</Type/OCG/Name(layer)>>",
		pdftest.Page("/CropBox[10 10 150 150]"),
		"<</Type/Annot/Subtype/Link/Rect[0 0 20 20]/A<</S/GoTo/D[9 0 R/Fit]>>>>",
		pdftest.Stream("/Type/XObject/Subtype/Form/BBox[0 0 80 20]", []byte("/Tx BMC EMC"), false),
	}}.Bytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		pdftest.Bounded(t, len(data), func() {
			d, err := Open(data, &OpenOptions{Limits: Limits{MaxStreamSize: 1 << 20, MaxDecoded: 8 << 20}})
			if err != nil {
				return
			}
			n := min(d.NumPages(), 4)
			pages := []PageRef{}
			for i := range n {
				pages = append(pages, PageRef{Doc: d, Index: i, Rotate: 90 * i})
			}
			pages = append(pages, PageRef{Doc: d, Index: 0, Resize: &Resize{To: A4}})
			out, err := Assemble(pages, nil)
			if err != nil {
				return
			}
			if int64(len(out.Bytes())) != out.Size() {
				t.Fatal("size mismatch")
			}
			d.Info()
		})
	})
}

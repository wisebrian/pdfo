package pdfo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
	"github.com/wisebrian/pdfo/internal/syntax"
)

func TestInfo(t *testing.T) {
	basic := openDoc(t, corpusPDF(t, "basic.pdf"), "").Info()
	if len(basic.Pages) != 3 || basic.Pages[0].SizeName != "A4" || basic.Pages[1].Rotate != 90 ||
		basic.Pages[1].Width != 792 || basic.Pages[2].SizeName != "A5" {
		t.Errorf("pages %+v", basic.Pages)
	}
	if !basic.Bookmarks || !basic.Attachments || !basic.JavaScript || basic.FormFields || basic.Signed ||
		basic.Title != "pdfo test document" || basic.Encrypted || basic.Repaired {
		t.Errorf("basic.pdf: %+v", basic)
	}
	forms := openDoc(t, corpusPDF(t, "lo-forms.pdf"), "").Info()
	if !forms.FormFields || !forms.Attachments || forms.Producer == "" {
		t.Errorf("lo-forms.pdf: %+v", forms)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "golden", "mixed-xref-table.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	img := openDoc(t, golden, "").Info()
	// Two JPEGs, two PNGs and the PNGs' two soft masks are not drawn
	// directly; the four images in the resources count.
	if img.Images != 4 || img.ImageBytes == 0 || img.Producer != "pdfo golden test" {
		t.Errorf("images: %d, %d bytes, producer %q", img.Images, img.ImageBytes, img.Producer)
	}
	enc := openDoc(t, corpusPDF(t, "enc-r4-aes-128.pdf"), "").Info()
	if !enc.Encrypted || enc.Encryption != "AES-128" {
		t.Errorf("encrypted: %+v", enc)
	}
}

func TestOpenErrors(t *testing.T) {
	var fe *FormatError
	if _, err := Open([]byte("hello"), nil); !errors.As(err, &fe) {
		t.Errorf("not a PDF: %v", err)
	}
	if _, err := Open(nil, &OpenOptions{Limits: Limits{MaxDepth: -1}}); err == nil {
		t.Error("negative limit accepted")
	}
}

// signed is a page with a digital signature whose widget draws a red box.
func signed() []byte {
	return pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R/AcroForm<</Fields[5 0 R]/SigFlags 3>>>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		pdftest.Page("/MediaBox[0 0 200 200]/Contents 4 0 R/Annots[5 0 R]/Resources<<>>"),
		pdftest.Stream("", []byte("0 0 1 rg 0 0 50 50 re f"), false),
		"<</Type/Annot/Subtype/Widget/FT/Sig/T(sig)/V 6 0 R/Rect[100 100 180 140]/F 4/P 3 0 R/AP<</N 7 0 R>>>>",
		"<</Type/Sig/Filter/Adobe.PPKLite/Contents<00>>>",
		pdftest.Stream("/Type/XObject/Subtype/Form/BBox[0 0 80 40]", []byte("1 0 0 rg 0 0 80 40 re f"), false),
	}}.Bytes()
}

func TestSignatureRemovedButVisible(t *testing.T) {
	src := signed()
	d := openDoc(t, src, "")
	if !d.Info().Signed {
		t.Error("signature not reported")
	}
	out, err := RotatePages(d, nil, 0, nil)
	pdf, got := finish(t, out, err)
	if !hasWarning(out, "signature-removed") || got.Info().Signed || got.Info().FormFields {
		t.Errorf("signature kept: %+v", out.Report.Warnings)
	}
	sameRender(t, src, 1, pdf, 1)
}

func TestHiddenLayerStaysHidden(t *testing.T) {
	src := pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R/OCProperties<</OCGs[5 0 R]/D<</OFF[5 0 R]/Order[5 0 R]>>>>>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		pdftest.Page("/MediaBox[0 0 200 200]/Contents 4 0 R/Resources<</Properties<</oc1 5 0 R>>>>"),
		pdftest.Stream("", []byte("/OC /oc1 BDC 1 0 0 rg 0 0 100 100 re f EMC 0 0 1 rg 120 120 50 50 re f"), false),
		"<</Type/OCG/Name(hidden layer)>>",
	}}.Bytes()
	basic := corpusPDF(t, "basic.pdf")
	out, err := Merge([]*Document{openDoc(t, src, ""), openDoc(t, basic, "")}, nil)
	pdf, _ := finish(t, out, err)
	sameRender(t, src, 1, pdf, 1)
	img := pdftest.Render(t, pdf, 1, 72)
	if c := img.NRGBAAt(50, 150); c.R != 255 || c.G != 255 {
		t.Errorf("hidden layer shows: %v", c)
	}
}

func TestResizeOddBoxes(t *testing.T) {
	// A media box away from the origin.
	off := pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		pdftest.Page("/MediaBox[-50 -50 50 50]/Contents 4 0 R"),
		pdftest.Stream("", []byte("1 0 0 rg -50 -50 20 20 re f"), false),
	}}.Bytes()
	out, err := ResizePages(openDoc(t, off, ""), []int{0}, Resize{To: Size{200, 200}}, nil)
	pdf, _ := finish(t, out, err)
	img := pdftest.Render(t, pdf, 1, 72)
	if c := img.NRGBAAt(20, 180); c.R < 200 || c.G > 50 {
		t.Errorf("square not in the corner: %v", c)
	}
	// A crop box hiding part of the content: the hidden part must stay
	// hidden in the bands the resize adds.
	crop := pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		pdftest.Page("/MediaBox[0 0 200 200]/CropBox[50 50 150 150]/Contents 4 0 R"),
		pdftest.Stream("", []byte("1 0 0 rg 0 0 200 200 re f 0 0 1 rg 60 60 20 20 re f"), false),
	}}.Bytes()
	out, err = ResizePages(openDoc(t, crop, ""), []int{0}, Resize{To: Size{300, 100}, Orientation: Landscape}, nil)
	pdf, got := finish(t, out, err)
	if p := got.Info().Pages[0]; p.Width != 300 || p.Height != 100 {
		t.Errorf("page %v×%v", p.Width, p.Height)
	}
	img = pdftest.Render(t, pdf, 1, 72)
	if c := img.NRGBAAt(20, 50); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("content outside the crop box shows in the band: %v", c)
	}
	if c := img.NRGBAAt(120, 70); c.B < 200 || c.R > 50 {
		t.Errorf("visible content moved: %v", c)
	}
}

func TestSameDocumentKeepsTags(t *testing.T) {
	src := corpusPDF(t, "lo-forms.pdf")
	out, err := RotatePages(openDoc(t, src, ""), nil, 0, nil)
	pdf, got := finish(t, out, err)
	if len(out.Report.Warnings) != 0 {
		t.Errorf("warnings: %+v", out.Report.Warnings)
	}
	cat := got.r.Catalog()
	if cat.Get("StructTreeRoot") == nil || cat.Get("MarkInfo") == nil || cat.Get("Lang") == nil {
		t.Error("tags lost")
	}
	if _, ok := got.r.Resolve(got.pages[0].Dict.Get("StructParents")).(syntax.Int); !ok {
		t.Error("StructParents lost")
	}
	sameRender(t, src, 1, pdf, 1)
	sameRender(t, src, 2, pdf, 2)
}

package content

import (
	"math"
	"strings"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
	"github.com/wisebrian/pdfo/internal/reader"
	"github.com/wisebrian/pdfo/internal/syntax"
)

var limits = reader.Limits{MaxDepth: 50, MaxStreamSize: 1 << 20, MaxDecoded: 1 << 24}

func TestOps(t *testing.T) {
	data := "q 1 0 0 1 5 5 cm BI /W 2 /H 1 /BPC 8 /CS /G ID \x00EI\xffx EI Q [(a) 2 (b)] TJ /P <</MCID 0>> BDC"
	var ops []string
	Ops([]byte(data), 50, func(op string, args []syntax.Object) bool {
		ops = append(ops, op+":"+string(syntax.Append(nil, syntax.Array(args))))
		return true
	})
	want := "q:[]|cm:[1 0 0 1 5 5]|Q:[]|TJ:[[(a)2(b)]]|BDC:[/P<</MCID 0>>]"
	if got := strings.Join(ops, "|"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestScannerSizes(t *testing.T) {
	doc := pdftest.Doc{Objects: []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		pdftest.Page("/MediaBox[0 0 300 300]/Contents 4 0 R/Resources<</XObject<</A 6 0 R/F 5 0 R>>>>/Annots[7 0 R]"),
		pdftest.Stream("", []byte("q 100 0 0 50 0 0 cm /A Do Q q 2 0 0 2 0 0 cm 0 1 -1 0 0 0 cm 10 0 0 20 0 0 cm /A Do Q /F Do"), false),
		// A form that draws the image and itself.
		pdftest.Stream("/Type/XObject/Subtype/Form/BBox[0 0 10 10]/Matrix[3 0 0 3 0 0]/Resources<</XObject<</I 6 0 R/Me 5 0 R>>>>",
			[]byte("q 5 0 0 5 0 0 cm /I Do Q /Me Do"), false),
		"<</Type/XObject/Subtype/Image/Width 10/Height 10/ColorSpace/DeviceGray/BitsPerComponent 8/Length 0>>stream\n\nendstream",
		"<</Type/Annot/Subtype/Stamp/Rect[0 0 80 40]/AP<</N 8 0 R>>>>",
		pdftest.Stream("/Type/XObject/Subtype/Form/BBox[0 0 20 10]/Resources<</XObject<</I 9 0 R>>>>", []byte("20 0 0 10 0 0 cm /I Do"), false),
		"<</Type/XObject/Subtype/Image/Width 4/Height 4/ColorSpace/DeviceGray/BitsPerComponent 8/Length 0>>stream\n\nendstream",
	}}.Bytes()
	r, err := reader.Open(doc, "", limits)
	if err != nil {
		t.Fatal(err)
	}
	s := NewScanner(r, 50)
	for i, p := range r.Pages() {
		s.Page(i, p)
	}
	u := s.Uses[6]
	// Drawn 100×50 directly, 20×40 turned (its axes swap: 40 along x, 20
	// along y) and 15×15 through the form, whose own drawing of itself is
	// cut short: the largest along each of the image's axes is 100 and 50.
	if u == nil || math.Abs(u.W-100) > 1e-9 || math.Abs(u.H-50) > 1e-9 {
		t.Errorf("image 6: %+v", u)
	}
	// The annotation's appearance stretches its 20×10 box over 80×40.
	if u := s.Uses[9]; u == nil || math.Abs(u.W-80) > 1e-9 || math.Abs(u.H-40) > 1e-9 {
		t.Errorf("image 9: %+v", u)
	}
}

func FuzzOps(f *testing.F) {
	f.Add([]byte("q 1 0 0 1 0 0 cm /Im0 Do Q BI /W 1 /H 1 ID x EI BT /F1 12 Tf (x) Tj ET"))
	f.Add([]byte("BI /W 4 ID \x00\x01 EI"))
	f.Fuzz(func(t *testing.T, data []byte) {
		pdftest.Bounded(t, len(data), func() {
			n := 0
			Ops(data, 30, func(op string, args []syntax.Object) bool {
				n++
				if len(args) > maxArgs {
					t.Fatalf("%d operands kept", len(args))
				}
				return true
			})
		})
	})
}

func FuzzScanner(f *testing.F) {
	f.Add([]byte("q 100 0 0 50 0 0 cm /A Do Q /F Do"), []byte("q 5 0 0 5 0 0 cm /I Do Q /Me Do"))
	f.Fuzz(func(t *testing.T, page, form []byte) {
		doc := pdftest.Doc{Objects: []string{
			"<</Type/Catalog/Pages 2 0 R>>",
			"<</Type/Pages/Kids[3 0 R]/Count 1>>",
			pdftest.Page("/MediaBox[0 0 300 300]/Contents 4 0 R/Resources<</XObject<</A 6 0 R/F 5 0 R>>>>"),
			pdftest.Stream("", page, false),
			pdftest.Stream("/Type/XObject/Subtype/Form/BBox[0 0 10 10]/Resources<</XObject<</I 6 0 R/Me 5 0 R>>>>", form, false),
			"<</Type/XObject/Subtype/Image/Width 10/Height 10/Length 0>>stream\n\nendstream",
		}}.Bytes()
		pdftest.Bounded(t, len(doc), func() {
			r, err := reader.Open(doc, "", limits)
			if err != nil {
				return
			}
			s := NewScanner(r, 30)
			for i, p := range r.Pages() {
				s.Page(i, p)
			}
		})
	})
}

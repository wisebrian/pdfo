package pdfo

import (
	"math"
	"strings"

	"github.com/wisebrian/pdfo/internal/content"
	"github.com/wisebrian/pdfo/internal/reader"
	"github.com/wisebrian/pdfo/internal/syntax"
)

// A form that sets NeedAppearances relies on viewers to draw its fields
// from their values, and many writers (LibreOffice among them) store empty
// appearances in such forms. Flattening one must then make the appearance
// itself, or the values would vanish. These appearances use the standard
// Helvetica, which needs no embedding but covers Latin text only; letters
// it lacks are written without their accents, or as "?".

// widgetState returns the appearance state of a check box or radio button:
// /AS when the appearance has it; otherwise the field's value when that is
// a state; otherwise, for a check box that is on, its one "on" state. Some
// writers set /AS to a name their appearances do not use.
func widgetState(r *reader.Reader, ad *syntax.Dict, states *syntax.Dict, maxDepth int) syntax.Name {
	if as, _ := r.Resolve(ad.Get("AS")).(syntax.Name); as != "" && states.Get(as) != nil {
		return as
	}
	v, _ := inheritedField(r, ad, "V", maxDepth).(syntax.Name)
	if v != "" && states.Get(v) != nil {
		return v
	}
	if v == "" || v == "Off" {
		return "Off"
	}
	ff, _ := inheritedField(r, ad, "Ff", maxDepth).(syntax.Int)
	if ff&(1<<15) != 0 {
		return "Off" // a radio button whose value names another button
	}
	var on []syntax.Name
	for i := range states.Len() {
		if k, _ := states.At(i); k != "Off" {
			on = append(on, k)
		}
	}
	if len(on) == 1 {
		return on[0]
	}
	return "Off"
}

// emptyAppearance reports whether an appearance stream paints nothing.
func emptyAppearance(r *reader.Reader, st *syntax.Stream, maxDepth int) bool {
	data, err := r.Decode(st)
	if err != nil {
		return false
	}
	empty := true
	content.Ops(data, maxDepth, func(op string, _ []syntax.Object) bool {
		switch op {
		case "Tj", "TJ", "'", "\"", "Do", "f", "F", "f*", "B", "B*", "b", "b*", "S", "s", "sh", "EI":
			empty = false
			return false
		}
		return true
	})
	return empty
}

// makeAppearance draws a widget from its field's value: text for text and
// choice fields, a check mark for a check box that is on. It returns nil
// when there is nothing to draw.
func (a *assembler) makeAppearance(s *source, ad *syntax.Dict, rect reader.Rect, on bool) syntax.Object {
	r := s.r
	depth := s.doc.limits.MaxDepth
	ft, _ := inheritedField(r, ad, "FT", depth).(syntax.Name)
	ff, _ := inheritedField(r, ad, "Ff", depth).(syntax.Int)
	w, h := rect.W(), rect.H()
	var body []byte
	mk, _ := r.Resolve(ad.Get("MK")).(*syntax.Dict)
	if bg := colorOp(r, mk.Get("BG"), false); bg != "" {
		body = append(body, bg...)
		body = appendNums(append(body, ' '), 0, 0, w, h)
		body = append(body, " re f\n"...)
	}
	if bc := colorOp(r, mk.Get("BC"), true); bc != "" {
		bw := 1.0
		if bs, ok := r.Resolve(ad.Get("BS")).(*syntax.Dict); ok {
			if v, ok := r.Number(bs.Get("W")); ok {
				bw = v
			}
		}
		if bw > 0 {
			body = append(body, bc...)
			body = appendNums(append(body, ' '), bw)
			body = append(body, " w "...)
			body = appendNums(body, bw/2, bw/2, w-bw, h-bw)
			body = append(body, " re S\n"...)
		}
	}
	font := ""
	switch {
	case ft == "Btn" && on:
		size := math.Min(w, h) * 0.8
		body = append(body, "q BT /ZaDb "...)
		body = appendNums(body, size)
		body = append(body, " Tf 0 g "...)
		// ✔ is "4" in ZapfDingbats, 0.846 em wide.
		body = appendNums(body, (w-0.846*size)/2, (h-0.7*size)/2)
		body = append(body, " Td (4) Tj ET Q\n"...)
		font = "ZaDb"
	case (ft == "Tx" || ft == "Ch") && ff&(1<<13) == 0: // not a password
		text := fieldText(r, inheritedField(r, ad, "V", depth))
		if text == "" {
			if len(body) == 0 {
				return nil
			}
			break
		}
		da := ""
		if s, ok := inheritedField(r, ad, "DA", depth).(syntax.String); ok {
			da = string(s)
		} else if form, ok := r.Resolve(r.Catalog().Get("AcroForm")).(*syntax.Dict); ok {
			if s, ok := r.Resolve(form.Get("DA")).(syntax.String); ok {
				da = string(s)
			}
		}
		size, color := parseDA(da)
		lines := []string{text}
		if ff&(1<<12) != 0 { // multiline
			lines = strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
		}
		widest := 0.0
		for _, l := range lines {
			widest = math.Max(widest, helveticaWidth(l))
		}
		if size <= 0 { // auto: as large as fits, up to 12 points
			size = math.Min(12, (h-4)/(1.15*float64(len(lines))))
			if widest > 0 {
				size = math.Min(size, (w-4)/(widest/1000))
			}
			size = math.Max(size, 4)
		}
		q, _ := inheritedField(r, ad, "Q", depth).(syntax.Int)
		body = append(body, "/Tx BMC q "...)
		body = appendNums(body, 1, 1, w-2, h-2)
		body = append(body, " re W n BT /Helv "...)
		body = appendNums(body, size)
		body = append(body, " Tf "...)
		body = append(body, color...)
		body = append(body, '\n')
		lineHeight := 1.15 * size
		y := (h - 0.718*size) / 2 // a single line, centred
		if len(lines) > 1 {
			y = h - 2 - size
		}
		px, py := 0.0, 0.0
		for _, l := range lines {
			tw := helveticaWidth(l) * size / 1000
			x := 2.0
			switch q {
			case 1:
				x = (w - tw) / 2
			case 2:
				x = w - 2 - tw
			}
			body = appendNums(body, x-px, y-py)
			body = append(body, " Td "...)
			body = append(syntax.Append(body, syntax.String(winAnsi(l))), " Tj\n"...)
			px, py = x, y
			y -= lineHeight
		}
		body = append(body, "ET Q EMC\n"...)
		font = "Helv"
	default:
		if len(body) == 0 {
			return nil
		}
	}
	d := new(syntax.Dict)
	d.Set("Type", syntax.Name("XObject"))
	d.Set("Subtype", syntax.Name("Form"))
	d.Set("BBox", syntax.Array{syntax.Int(0), syntax.Int(0), syntax.Real(w), syntax.Real(h)})
	if font != "" {
		fonts := new(syntax.Dict)
		fonts.Set(syntax.Name(font), a.standardFont(font))
		res := new(syntax.Dict)
		res.Set("Font", fonts)
		d.Set("Resources", res)
	}
	return a.w.AddStream(d, body)
}

// standardFont returns, made once per output, a standard 14 font: these
// need no embedding.
func (a *assembler) standardFont(name string) syntax.Ref {
	if a.fonts == nil {
		a.fonts = map[string]syntax.Ref{}
	}
	if r, ok := a.fonts[name]; ok {
		return r
	}
	d := new(syntax.Dict)
	d.Set("Type", syntax.Name("Font"))
	d.Set("Subtype", syntax.Name("Type1"))
	if name == "ZaDb" {
		d.Set("BaseFont", syntax.Name("ZapfDingbats"))
	} else {
		d.Set("BaseFont", syntax.Name("Helvetica"))
		d.Set("Encoding", syntax.Name("WinAnsiEncoding"))
	}
	r := a.w.Add(d)
	a.fonts[name] = r
	return r
}

// fieldText returns a field value as text: a string, or the first of an
// array (a list box with several selections).
func fieldText(r *reader.Reader, v syntax.Object) string {
	switch x := r.Resolve(v).(type) {
	case syntax.String:
		return syntax.Text(x)
	case syntax.Array:
		if len(x) > 0 {
			return fieldText(r, x[0])
		}
	case syntax.Name:
		return string(x)
	}
	return ""
}

// parseDA reads the font size and fill colour from a default appearance
// string, such as "0 0 1 rg /Helv 10 Tf".
func parseDA(da string) (size float64, color string) {
	color = "0 g"
	content.Ops([]byte(da), 8, func(op string, args []syntax.Object) bool {
		switch op {
		case "Tf":
			if len(args) == 2 {
				if v, ok := numOf(args[1]); ok {
					size = v
				}
			}
		case "g", "rg", "k":
			nums := make([]float64, 0, 4)
			for _, x := range args {
				if v, ok := numOf(x); ok {
					nums = append(nums, v)
				}
			}
			if want := map[string]int{"g": 1, "rg": 3, "k": 4}[op]; len(nums) == want {
				color = string(appendNums(nil, nums...)) + " " + op
			}
		}
		return true
	})
	return size, color
}

// colorOp turns an /MK colour array into a fill (or stroke) operator.
func colorOp(r *reader.Reader, o syntax.Object, stroke bool) string {
	arr, ok := r.Resolve(o).(syntax.Array)
	if !ok {
		return ""
	}
	nums := make([]float64, 0, 4)
	for _, x := range arr {
		if v, ok := r.Number(x); ok {
			nums = append(nums, v)
		}
	}
	op := map[int]string{1: "g", 3: "rg", 4: "k"}[len(nums)]
	if op == "" {
		return ""
	}
	if stroke {
		op = strings.ToUpper(op)
	}
	return string(appendNums(nil, nums...)) + " " + op
}

// helvetica holds the widths of Helvetica's printable ASCII characters, in
// thousandths of an em (from Adobe's font metrics).
var helvetica = [95]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

// helveticaWidth returns the width of s in Helvetica, in thousandths of an
// em; characters beyond ASCII count as an average letter.
func helveticaWidth(s string) float64 {
	w := 0
	for _, c := range string(winAnsi(s)) {
		if c >= 32 && c < 127 {
			w += helvetica[c-32]
		} else {
			w += 556
		}
	}
	return float64(w)
}

// winAnsiHigh maps the characters of WinAnsiEncoding's 0x80–0x9F range.
var winAnsiHigh = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88,
	'‰': 0x89, 'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E, '‘': 0x91, '’': 0x92, '“': 0x93,
	'”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97, '˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B,
	'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

// unaccent gives the base letter of accented letters WinAnsiEncoding lacks,
// such as those of Romanian, Polish, Czech or Turkish.
var unaccent = map[rune]rune{
	'ă': 'a', 'Ă': 'A', 'ș': 's', 'Ș': 'S', 'ş': 's', 'Ş': 'S', 'ț': 't', 'Ț': 'T', 'ţ': 't', 'Ţ': 'T',
	'ą': 'a', 'Ą': 'A', 'ć': 'c', 'Ć': 'C', 'ę': 'e', 'Ę': 'E', 'ł': 'l', 'Ł': 'L', 'ń': 'n', 'Ń': 'N',
	'ś': 's', 'Ś': 'S', 'ź': 'z', 'Ź': 'Z', 'ż': 'z', 'Ż': 'Z', 'č': 'c', 'Č': 'C', 'ď': 'd', 'Ď': 'D',
	'ě': 'e', 'Ě': 'E', 'ň': 'n', 'Ň': 'N', 'ř': 'r', 'Ř': 'R', 'ť': 't', 'Ť': 'T', 'ů': 'u', 'Ů': 'U',
	'ğ': 'g', 'Ğ': 'G', 'ı': 'i', 'İ': 'I', 'ő': 'o', 'Ő': 'O', 'ű': 'u', 'Ű': 'U',
}

// winAnsi encodes text for a standard font with WinAnsiEncoding.
func winAnsi(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, c := range s {
		if u, ok := unaccent[c]; ok {
			c = u
		}
		switch {
		case c >= 32 && c < 127, c >= 0xA0 && c <= 0xFF:
			out = append(out, byte(c))
		case winAnsiHigh[c] != 0:
			out = append(out, winAnsiHigh[c])
		default:
			out = append(out, '?')
		}
	}
	return out
}

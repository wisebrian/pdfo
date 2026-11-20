package jsapi

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/wisebrian/pdfo"
)

// InputError says which input of a call failed, such as the second PDF of a
// merge that needs a password.
type InputError struct {
	Index int   // the input's position, from 0
	Err   error // why it failed
}

func (e *InputError) Error() string { return e.Err.Error() }

// Unwrap returns the cause.
func (e *InputError) Unwrap() error { return e.Err }

// Request holds the options of a call on PDFs, decoded.
type Request struct {
	Passwords []string          // one per input PDF, in order
	Write     pdfo.WriteOptions // how to write the result
	Pages     string            // a selection as typed, such as "1-3, 5"
	List      []int             // or zero-based page indexes
	Every     int               // pages per part, for split
	Order     []int             // the new order, zero-based, for reorder
	Degree    int               // the rotation, for rotate
	Resize    *pdfo.Resize      // the new size, for resize
	Assemble  []AssemblePage    // the pages, for assemble
	// Compress holds compress's options: a preset, with dpi and quality
	// overriding it.
	Compress *pdfo.CompressOptions
}

// AssemblePage is one entry of assemble's pages list: page numbers from 1,
// documents by position in the input array.
type AssemblePage struct {
	Doc, Page, Rotate int          // document position, zero-based page, extra rotation
	Resize            *pdfo.Resize // nil keeps the size
}

// ParseRequest decodes the options object of a call on PDFs. fn names the
// call, which decides the options it takes.
func ParseRequest(fn string, data []byte) (*Request, error) {
	keys := append([]string{"password", "passwords"}, writeKeys...)
	switch fn {
	case "split":
		keys = append(keys, "pages", "every")
	case "extract", "delete":
		keys = append(keys, "pages")
	case "reorder":
		keys = append(keys, "order")
	case "rotate":
		keys = append(keys, "pages", "degrees")
	case "resize":
		keys = append(keys, "pages", "size", "orientation", "scale")
	case "assemble":
		keys = append(keys, "pages")
	case "compress":
		keys = append(keys, "preset", "dpi", "quality")
	}
	o, err := parseOptions(data, keys...)
	if err != nil {
		return nil, err
	}
	req := &Request{}
	if req.Write, err = o.write(); err != nil {
		return nil, err
	}
	if pw, err := o.str("password", "Password"); err != nil {
		return nil, err
	} else if pw != "" {
		req.Passwords = []string{pw}
	}
	if list, ok := o["passwords"].([]any); ok {
		req.Passwords = nil
		for _, x := range list {
			s, _ := x.(string)
			req.Passwords = append(req.Passwords, s)
		}
	} else if o["passwords"] != nil {
		return nil, &pdfo.OptionError{Option: "Passwords", Msg: "want an array of strings"}
	}
	switch v := o["pages"].(type) {
	case nil:
	case string:
		req.Pages = v
	case []any:
		if fn == "assemble" {
			for i, x := range v {
				p, err := assemblePage(x)
				if err != nil {
					return nil, &pdfo.OptionError{Option: "Pages", Msg: "entry " + strconv.Itoa(i+1) + ": " + err.Error()}
				}
				req.Assemble = append(req.Assemble, p)
			}
		} else if req.List, err = ints(v, "Pages"); err != nil {
			return nil, err
		}
	default:
		return nil, &pdfo.OptionError{Option: "Pages", Msg: "want a page selection such as \"1-3, 5\" or a list of page numbers"}
	}
	if v, ok := o["every"].(float64); ok {
		req.Every = int(v)
		if float64(req.Every) != v || v < 1 {
			return nil, &pdfo.OptionError{Option: "Every", Msg: "want a whole number of pages, at least 1"}
		}
	} else if o["every"] != nil {
		return nil, &pdfo.OptionError{Option: "Every", Msg: "want a number"}
	}
	if v, ok := o["order"].([]any); ok {
		if req.Order, err = ints(v, "Order"); err != nil {
			return nil, err
		}
	} else if o["order"] != nil {
		return nil, &pdfo.OptionError{Option: "Order", Msg: "want a list of page numbers"}
	}
	if v, ok := o["degrees"].(float64); ok {
		req.Degree = int(v)
		if float64(req.Degree) != v || req.Degree%90 != 0 {
			return nil, &pdfo.OptionError{Option: "Degrees", Msg: "want a multiple of 90"}
		}
	} else if o["degrees"] != nil {
		return nil, &pdfo.OptionError{Option: "Degrees", Msg: "want a number"}
	}
	if fn == "resize" {
		if req.Resize, err = resizeOf(o); err != nil {
			return nil, err
		}
	}
	if fn == "compress" {
		preset, err := o.str("preset", "Preset")
		if err != nil {
			return nil, err
		}
		var c pdfo.CompressOptions
		switch strings.ToLower(preset) {
		case "", "ebook":
			c = pdfo.Ebook
		case "screen":
			c = pdfo.Screen
		case "print":
			c = pdfo.Print
		default:
			return nil, &pdfo.OptionError{Option: "Preset", Msg: "want screen, ebook or print"}
		}
		if v, ok := o["dpi"].(float64); ok {
			c.DPI = v
		} else if o["dpi"] != nil {
			return nil, &pdfo.OptionError{Option: "DPI", Msg: "want a number"}
		}
		if v, ok := o["quality"].(float64); ok {
			if v != math.Trunc(v) {
				return nil, &pdfo.OptionError{Option: "Quality", Msg: "want a whole number from 1 to 100"}
			}
			c.Quality = int(v)
		} else if o["quality"] != nil {
			return nil, &pdfo.OptionError{Option: "Quality", Msg: "want a number"}
		}
		c.Write = req.Write
		req.Compress = &c
	}
	return req, nil
}

func ints(v []any, option string) ([]int, error) {
	out := make([]int, len(v))
	for i, x := range v {
		f, ok := x.(float64)
		if !ok || f != math.Trunc(f) || f < 1 || f > 1<<31 {
			return nil, &pdfo.OptionError{Option: option, Msg: "want page numbers, counted from 1"}
		}
		out[i] = int(f) - 1
	}
	return out, nil
}

func resizeOf(o options) (*pdfo.Resize, error) {
	r := &pdfo.Resize{}
	size, err := o.str("size", "Size")
	if err != nil {
		return nil, err
	}
	if size != "" {
		if r.To, err = pdfo.ParseSize(size); err != nil {
			return nil, &pdfo.OptionError{Option: "Size", Msg: strings.TrimPrefix(err.Error(), "pdfo: ")}
		}
	}
	if v, ok := o["scale"].(float64); ok {
		r.Scale = v
	} else if o["scale"] != nil {
		return nil, &pdfo.OptionError{Option: "Scale", Msg: "want a number"}
	}
	orient, err := o.str("orientation", "Orientation")
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(orient) {
	case "", "auto":
	case "portrait":
		r.Orientation = pdfo.Portrait
	case "landscape":
		r.Orientation = pdfo.Landscape
	default:
		return nil, &pdfo.OptionError{Option: "Orientation", Msg: "want auto, portrait or landscape"}
	}
	if size == "" && r.Scale == 0 {
		return nil, &pdfo.OptionError{Option: "Size", Msg: "give a size or a scale"}
	}
	return r, nil
}

func assemblePage(x any) (AssemblePage, error) {
	m, ok := x.(map[string]any)
	if !ok {
		return AssemblePage{}, errors.New("want an object such as {doc: 0, page: 1}")
	}
	o := options(m)
	for k := range m {
		switch k {
		case "doc", "page", "rotate", "size", "orientation", "scale":
		default:
			return AssemblePage{}, errors.New("unknown key " + strconv.Quote(k))
		}
	}
	whole := func(k string, def int) (int, error) {
		switch v := m[k].(type) {
		case nil:
			return def, nil
		case float64:
			if v == math.Trunc(v) && math.Abs(v) < 1<<31 {
				return int(v), nil
			}
		}
		return 0, errors.New(k + " must be a whole number")
	}
	var p AssemblePage
	var err error
	if p.Doc, err = whole("doc", 0); err != nil {
		return p, err
	}
	if p.Page, err = whole("page", 0); err != nil || p.Page < 1 {
		return p, errors.New("page must be a page number, counted from 1")
	}
	p.Page--
	if p.Rotate, err = whole("rotate", 0); err != nil {
		return p, err
	}
	if m["size"] != nil || m["scale"] != nil {
		if p.Resize, err = resizeOf(o); err != nil {
			return p, err
		}
	}
	return p, nil
}

// InfoJSON encodes Document.Info for JavaScript to JSON.parse. Field names
// follow the Go names in lower camel case.
func InfoJSON(in *pdfo.Info) string {
	b := []byte(`{"version":`)
	b = appendJSONString(b, in.Version)
	b = append(b, `,"title":`...)
	b = appendJSONString(b, in.Title)
	b = append(b, `,"producer":`...)
	b = appendJSONString(b, in.Producer)
	b = appendBool(b, "encrypted", in.Encrypted)
	b = append(b, `,"encryption":`...)
	b = appendJSONString(b, in.Encryption)
	b = append(b, `,"pages":[`...)
	for i, p := range in.Pages {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"width":`...)
		b = appendNumber(b, p.Width)
		b = append(b, `,"height":`...)
		b = appendNumber(b, p.Height)
		b = append(b, `,"widthMM":`...)
		b = appendNumber(b, p.WidthMM)
		b = append(b, `,"heightMM":`...)
		b = appendNumber(b, p.HeightMM)
		b = append(b, `,"sizeName":`...)
		b = appendJSONString(b, p.SizeName)
		b = append(b, `,"rotate":`...)
		b = strconv.AppendInt(b, int64(p.Rotate), 10)
		b = append(b, '}')
	}
	b = append(b, ']')
	b = appendBool(b, "bookmarks", in.Bookmarks)
	b = appendBool(b, "formFields", in.FormFields)
	b = appendBool(b, "attachments", in.Attachments)
	b = appendBool(b, "javaScript", in.JavaScript)
	b = appendBool(b, "signed", in.Signed)
	b = append(b, `,"images":`...)
	b = strconv.AppendInt(b, int64(in.Images), 10)
	b = append(b, `,"imageBytes":`...)
	b = strconv.AppendInt(b, in.ImageBytes, 10)
	b = appendBool(b, "repaired", in.Repaired)
	b = append(b, `,"warnings":`...)
	b = appendWarnings(b, in.Warnings)
	return string(append(b, '}'))
}

func appendBool(b []byte, key string, v bool) []byte {
	b = append(b, ',', '"')
	b = append(b, key...)
	b = append(b, '"', ':')
	return strconv.AppendBool(b, v)
}

// appendNumber appends f as a JSON number; JSON has no NaN or infinity.
func appendNumber(b []byte, f float64) []byte {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return append(b, "null"...)
	}
	return strconv.AppendFloat(b, f, 'g', -1, 64)
}

// PagesJSON encodes ParsePages groups with page numbers from 1.
func PagesJSON(groups [][]int) string {
	b := []byte{'['}
	for i, g := range groups {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '[')
		for j, p := range g {
			if j > 0 {
				b = append(b, ',')
			}
			b = strconv.AppendInt(b, int64(p+1), 10)
		}
		b = append(b, ']')
	}
	return string(append(b, ']'))
}

// SplitJSON encodes the reports of the parts of a split.
func SplitJSON(reports []pdfo.Report) string {
	b := []byte(`{"parts":[`)
	for i, r := range reports {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, ReportJSON(r)...)
	}
	return string(append(b, "]}"...))
}

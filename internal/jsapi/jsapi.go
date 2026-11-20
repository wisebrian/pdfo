// Package jsapi is the platform-independent half of the JavaScript API: it
// turns the options JavaScript passes into this module's types, and errors
// and reports into what JavaScript receives. Keeping syscall/js out of it
// lets it be tested without a browser; the wasm command is the thin rest.
package jsapi

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/wisebrian/pdfo"
)

// ArgError reports a JavaScript argument of the wrong kind, such as a string
// where a Uint8Array belongs: a bug in the calling page, not in the file.
type ArgError struct {
	Msg string // what was wrong with the argument
}

func (e *ArgError) Error() string { return "pdfo: " + e.Msg }

// Error is what a failed call returns to JavaScript, as an object.
type Error struct {
	// Message is the English description, for logs and as a fallback.
	Message string
	// Code is stable, for the page to choose what to show:
	// "bad-argument", "unsupported", "damaged", "limit" or "internal".
	Code string
	// Index is the position of the input that caused the error, or -1.
	Index int
	// Option is the name of the offending option, or "".
	Option string
	// Pos is, for a page selection, the byte offset of the bad item; -1
	// otherwise.
	Pos int
}

// Object returns the error as the JavaScript object a call returns:
// {error, code} plus index and option when they apply.
func (e Error) Object() map[string]any {
	m := map[string]any{"error": e.Message, "code": e.Code}
	if e.Index >= 0 {
		m["index"] = e.Index
	}
	if e.Option != "" {
		m["option"] = e.Option
	}
	if e.Pos >= 0 {
		m["pos"] = e.Pos
	}
	return m
}

// ErrorOf classifies err for JavaScript.
func ErrorOf(err error) Error {
	e := Error{Message: err.Error(), Code: "internal", Index: -1, Pos: -1}
	var (
		ae *ArgError
		oe *pdfo.OptionError
		le *pdfo.LimitError
		fe *pdfo.FormatError
		ie *pdfo.ImageError
		pe *pdfo.PageRangeError
		in *InputError
	)
	switch {
	case errors.Is(err, pdfo.ErrPasswordRequired):
		e.Code = "password-required"
	case errors.Is(err, pdfo.ErrWrongPassword):
		e.Code = "wrong-password"
	case errors.As(err, &pe):
		e.Code, e.Option, e.Pos = "bad-argument", "pages", pe.Pos
	case errors.As(err, &ae):
		e.Code = "bad-argument"
	case errors.As(err, &oe):
		e.Code, e.Option = "bad-argument", optionName(oe.Option)
	case errors.Is(err, errors.ErrUnsupported):
		e.Code = "unsupported"
	case errors.As(err, &le):
		e.Code = "limit"
	case errors.As(err, &fe):
		e.Code = "damaged"
	}
	if errors.As(err, &ie) {
		e.Index = ie.Index
	}
	if errors.As(err, &in) {
		e.Index = in.Index
	}
	return e
}

// Panic turns a recovered panic into an error for JavaScript. The page
// keeps working: the panic was caught before it could end the Go program.
func Panic(r any) Error {
	return Error{Message: fmt.Sprintf("pdfo: internal error: %v", r), Code: "internal", Index: -1, Pos: -1}
}

// optionName converts a Go option name to the JavaScript one ("PageSize"
// to "pageSize").
func optionName(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// options is a decoded options object.
type options map[string]any

// parseOptions decodes an options object, given as the JSON that
// JavaScript's JSON.stringify makes of it, and rejects keys the function
// does not take, so a misspelt option fails loudly instead of being ignored.
func parseOptions(data []byte, allowed ...string) (options, error) {
	if len(data) == 0 {
		return options{}, nil
	}
	v, err := parseJSON(data)
	if err != nil {
		return nil, &ArgError{Msg: "options: " + err.Error()}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, &ArgError{Msg: "options must be an object"}
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if !slices.Contains(allowed, k) {
			return nil, &ArgError{Msg: "options: unknown option " + strconv.Quote(k)}
		}
	}
	return m, nil
}

var writeKeys = []string{"xrefTable", "timestamp", "producer"}

func (o options) str(k, option string) (string, error) {
	switch v := o[k].(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	}
	return "", &pdfo.OptionError{Option: option, Msg: "want a string"}
}

func (o options) boolean(k, option string) (bool, error) {
	switch v := o[k].(type) {
	case nil:
		return false, nil
	case bool:
		return v, nil
	}
	return false, &pdfo.OptionError{Option: option, Msg: "want true or false"}
}

// length reads a number of points or a string with a unit.
func (o options) length(k, option string) (float64, error) {
	switch v := o[k].(type) {
	case nil:
		return 0, nil
	case float64:
		return v, nil
	case string:
		l, err := pdfo.ParseLength(v)
		if err != nil {
			return 0, &pdfo.OptionError{Option: option, Msg: strings.TrimPrefix(err.Error(), "pdfo: ")}
		}
		return l, nil
	}
	return 0, &pdfo.OptionError{Option: option, Msg: "want a number of points or a length such as \"10mm\""}
}

func (o options) write() (pdfo.WriteOptions, error) {
	var w pdfo.WriteOptions
	var err error
	if w.XRefTable, err = o.boolean("xrefTable", "XRefTable"); err != nil {
		return w, err
	}
	if w.Timestamp, err = o.str("timestamp", "Timestamp"); err != nil {
		return w, err
	}
	w.Producer, err = o.str("producer", "Producer")
	return w, err
}

// ParseImageOptions decodes the options object of imagesToPDF:
//
//	pageSize     a name ("A4") or a size with a unit ("210x297mm");
//	             empty or "fit" sizes each page to its image
//	orientation  "auto", "portrait" or "landscape"
//	margin       a number of points or a length with a unit ("10mm")
//	fit          "contain" or "cover"
//
// plus the write options every call takes: xrefTable, timestamp, producer.
func ParseImageOptions(data []byte) (*pdfo.ImageOptions, error) {
	o, err := parseOptions(data, append([]string{"pageSize", "orientation", "margin", "fit"}, writeKeys...)...)
	if err != nil {
		return nil, err
	}
	out := &pdfo.ImageOptions{}
	if out.Write, err = o.write(); err != nil {
		return nil, err
	}
	size, err := o.str("pageSize", "PageSize")
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(size) {
	case "", "fit":
	default:
		s, err := pdfo.ParseSize(size)
		if err != nil {
			return nil, &pdfo.OptionError{Option: "PageSize", Msg: strings.TrimPrefix(err.Error(), "pdfo: ")}
		}
		out.PageSize = s
	}
	orient, err := o.str("orientation", "Orientation")
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(orient) {
	case "", "auto":
		out.Orientation = pdfo.Auto
	case "portrait":
		out.Orientation = pdfo.Portrait
	case "landscape":
		out.Orientation = pdfo.Landscape
	default:
		return nil, &pdfo.OptionError{Option: "Orientation", Msg: "want auto, portrait or landscape"}
	}
	fit, err := o.str("fit", "Fit")
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(fit) {
	case "", "contain":
		out.Fit = pdfo.Contain
	case "cover":
		out.Fit = pdfo.Cover
	default:
		return nil, &pdfo.OptionError{Option: "Fit", Msg: "want contain or cover"}
	}
	if out.Margin, err = o.length("margin", "Margin"); err != nil {
		return nil, err
	}
	return out, nil
}

// ReportJSON encodes a report for JavaScript to JSON.parse:
//
//	{"pages": 3, "inputEncrypted": true, "warnings": [
//	  {"code": "...", "message": "...", "count": 2, "pages": [1, 3]}],
//	 "compression": {"sizeBefore": 1000, "sizeAfter": 500, "images": [
//	  {"page": 1, "width": 2000, "height": 1500, "dpi": 181,
//	   "bytesBefore": 800, "bytesAfter": 300, "action": "downsampled", "reason": ""}]}}
//
// inputEncrypted, count and pages appear only when they say something;
// compression only for compress.
func ReportJSON(r pdfo.Report) string {
	b := []byte(`{"pages":`)
	b = strconv.AppendInt(b, int64(r.Pages), 10)
	if r.InputEncrypted {
		b = append(b, `,"inputEncrypted":true`...)
	}
	b = append(b, `,"warnings":`...)
	b = appendWarnings(b, r.Warnings)
	if c := r.Compression; c != nil {
		b = append(b, `,"compression":{"sizeBefore":`...)
		b = strconv.AppendInt(b, c.SizeBefore, 10)
		b = append(b, `,"sizeAfter":`...)
		b = strconv.AppendInt(b, c.SizeAfter, 10)
		b = append(b, `,"images":[`...)
		for i, im := range c.Images {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, `{"page":`...)
			b = strconv.AppendInt(b, int64(im.Page), 10)
			b = append(b, `,"width":`...)
			b = strconv.AppendInt(b, int64(im.Width), 10)
			b = append(b, `,"height":`...)
			b = strconv.AppendInt(b, int64(im.Height), 10)
			b = append(b, `,"dpi":`...)
			b = appendNumber(b, im.DPI)
			b = append(b, `,"bytesBefore":`...)
			b = strconv.AppendInt(b, im.BytesBefore, 10)
			b = append(b, `,"bytesAfter":`...)
			b = strconv.AppendInt(b, im.BytesAfter, 10)
			b = append(b, `,"action":`...)
			b = appendJSONString(b, im.Action)
			b = append(b, `,"reason":`...)
			b = appendJSONString(b, im.Reason)
			b = append(b, '}')
		}
		b = append(b, "]}"...)
	}
	return string(append(b, '}'))
}

func appendWarnings(b []byte, ws []pdfo.Warning) []byte {
	b = append(b, '[')
	for i, w := range ws {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"code":`...)
		b = appendJSONString(b, w.Code)
		b = append(b, `,"message":`...)
		b = appendJSONString(b, w.Message)
		if w.Count > 0 {
			b = append(b, `,"count":`...)
			b = strconv.AppendInt(b, int64(w.Count), 10)
		}
		if len(w.Pages) > 0 {
			b = append(b, `,"pages":[`...)
			for j, p := range w.Pages {
				if j > 0 {
					b = append(b, ',')
				}
				b = strconv.AppendInt(b, int64(p), 10)
			}
			b = append(b, ']')
		}
		b = append(b, '}')
	}
	return append(b, ']')
}

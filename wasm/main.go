//go:build js && wasm

// Command wasm exposes pdfo to JavaScript when compiled to WebAssembly:
//
//	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o pdfo.wasm ./wasm
//
// Once the module runs, globalThis.pdfo holds the functions below. Each one
// is synchronous, meant to be called from a Web Worker, and never throws: on
// failure it returns {error, code} (plus index, option or pos when they
// apply) instead, because a panic escaping into JavaScript would end the Go
// program and every later call with it. Codes: "bad-argument",
// "password-required", "wrong-password", "unsupported", "damaged", "limit",
// "internal".
//
// Files go in and out as Uint8Array; reports come back as JSON strings for
// JSON.parse. Pages are numbered from 1, documents in an array from 0.
//
//	pdfo.imagesToPDF([img, …], {pageSize, orientation, margin, fit})  → {pdf, report}
//	pdfo.info(pdf, {password})                                        → {report}
//	pdfo.merge([pdf, …], {passwords})                                 → {pdf, report}
//	pdfo.split(pdf, {password, pages: "1-3, 4-"} or {password, every}) → {pdfs, report}
//	pdfo.extract(pdf, {password, pages})                              → {pdf, report}
//	pdfo.delete(pdf, {password, pages})                               → {pdf, report}
//	pdfo.reorder(pdf, {password, order: [3, 1, 2]})                   → {pdf, report}
//	pdfo.rotate(pdf, {password, pages, degrees})                      → {pdf, report}
//	pdfo.resize(pdf, {password, pages, size, orientation, scale})     → {pdf, report}
//	pdfo.assemble([pdf, …], {passwords, pages: [{doc, page, rotate, size, orientation, scale}]})
//	pdfo.compress(pdf, {password, preset: "screen"|"ebook"|"print", dpi, quality}) → {pdf, report}
//	pdfo.parsePages("1-3, 5", numPages)                               → {pages}
//
// Every call that writes a PDF also takes xrefTable, timestamp and producer.
// Where pages are optional (rotate, resize) they default to all.
package main

import (
	"bufio"
	"runtime"
	"runtime/debug"
	"syscall/js"

	"github.com/wisebrian/pdfo"
	"github.com/wisebrian/pdfo/internal/jsapi"
)

func main() {
	// WebAssembly memory grows but never shrinks, so the peak of a call is
	// what the page keeps. Collecting at 50% growth rather than 100% keeps
	// that peak nearer to what is actually live.
	debug.SetGCPercent(50)
	api := js.Global().Get("Object").New()
	api.Set("imagesToPDF", export(imagesToPDF))
	api.Set("info", export(info))
	api.Set("merge", export(merge))
	api.Set("split", export(split))
	api.Set("assemble", export(assemble))
	api.Set("parsePages", export(parsePages))
	for _, name := range []string{"extract", "delete", "reorder", "rotate", "resize", "compress"} {
		api.Set(name, export(onePDF(name)))
	}
	js.Global().Set("pdfo", api)
	select {} // keep the functions alive
}

// export wraps a function for JavaScript: errors and panics become
// {error, code} objects, and memory is collected before returning so the
// next call can reuse it instead of growing the heap.
func export(fn func(args []js.Value) (map[string]any, error)) js.Func {
	return js.FuncOf(func(this js.Value, args []js.Value) (result any) {
		defer func() {
			if r := recover(); r != nil {
				result = jsapi.Panic(r).Object()
			}
			runtime.GC()
		}()
		res, err := fn(args)
		if err != nil {
			return jsapi.ErrorOf(err).Object()
		}
		return res
	})
}

// imagesToPDF(images: Uint8Array[], options?: object) → {pdf, report}.
func imagesToPDF(args []js.Value) (map[string]any, error) {
	images, err := byteList(args, "imagesToPDF", "image")
	if err != nil {
		return nil, err
	}
	data, err := optionsJSON(args, 1, "imagesToPDF")
	if err != nil {
		return nil, err
	}
	opts, err := jsapi.ParseImageOptions(data)
	if err != nil {
		return nil, err
	}
	out, err := pdfo.FromImages(images, opts)
	if err != nil {
		return nil, err
	}
	return result(out)
}

// info(pdf: Uint8Array, options?: {password}) → {report}.
func info(args []js.Value) (map[string]any, error) {
	req, docs, err := openArgs(args, "info", false)
	if err != nil {
		return nil, err
	}
	_ = req
	return map[string]any{"report": jsapi.InfoJSON(docs[0].Info())}, nil
}

// merge(pdfs: Uint8Array[], options?) → {pdf, report}.
func merge(args []js.Value) (map[string]any, error) {
	req, docs, err := openArgs(args, "merge", true)
	if err != nil {
		return nil, err
	}
	out, err := pdfo.Merge(docs, &req.Write)
	if err != nil {
		return nil, err
	}
	return result(out)
}

// split(pdf, options) → {pdfs, report}.
func split(args []js.Value) (map[string]any, error) {
	req, docs, err := openArgs(args, "split", false)
	if err != nil {
		return nil, err
	}
	d := docs[0]
	var outs []*pdfo.Output
	switch {
	case req.Every > 0:
		outs, err = pdfo.SplitEvery(d, req.Every, &req.Write)
	case req.Pages != "":
		groups, perr := pdfo.ParsePages(req.Pages, d.NumPages())
		if perr != nil {
			return nil, perr
		}
		outs, err = pdfo.Split(d, groups, &req.Write)
	case req.List != nil:
		groups := make([][]int, len(req.List))
		for i, p := range req.List {
			groups[i] = []int{p}
		}
		outs, err = pdfo.Split(d, groups, &req.Write)
	default:
		return nil, &jsapi.ArgError{Msg: "split: give pages or every"}
	}
	if err != nil {
		return nil, err
	}
	pdfs := make([]any, len(outs))
	reports := make([]pdfo.Report, len(outs))
	for i, o := range outs {
		v, err := toJS(o)
		if err != nil {
			return nil, err
		}
		pdfs[i] = v
		reports[i] = o.Report
	}
	return map[string]any{"pdfs": pdfs, "report": jsapi.SplitJSON(reports)}, nil
}

// onePDF returns the function for a page action on one PDF.
func onePDF(name string) func(args []js.Value) (map[string]any, error) {
	return func(args []js.Value) (map[string]any, error) {
		req, docs, err := openArgs(args, name, false)
		if err != nil {
			return nil, err
		}
		d := docs[0]
		var out *pdfo.Output
		switch name {
		case "extract", "delete":
			pages, err := selection(d, req, false)
			if err != nil {
				return nil, err
			}
			if name == "extract" {
				out, err = pdfo.ExtractPages(d, pages, &req.Write)
			} else {
				out, err = pdfo.DeletePages(d, pages, &req.Write)
			}
			if err != nil {
				return nil, err
			}
		case "reorder":
			if req.Order == nil {
				return nil, &jsapi.ArgError{Msg: "reorder: give the order"}
			}
			if out, err = pdfo.ReorderPages(d, req.Order, &req.Write); err != nil {
				return nil, err
			}
		case "rotate":
			pages, err := selection(d, req, true)
			if err != nil {
				return nil, err
			}
			if out, err = pdfo.RotatePages(d, pages, req.Degree, &req.Write); err != nil {
				return nil, err
			}
		case "resize":
			pages, err := selection(d, req, true)
			if err != nil {
				return nil, err
			}
			if out, err = pdfo.ResizePages(d, pages, *req.Resize, &req.Write); err != nil {
				return nil, err
			}
		case "compress":
			if out, err = pdfo.Compress(d, req.Compress); err != nil {
				return nil, err
			}
		}
		return result(out)
	}
}

// assemble(pdfs: Uint8Array[], options) → {pdf, report}.
func assemble(args []js.Value) (map[string]any, error) {
	req, docs, err := openArgs(args, "assemble", true)
	if err != nil {
		return nil, err
	}
	pages := make([]pdfo.PageRef, len(req.Assemble))
	for i, p := range req.Assemble {
		if p.Doc < 0 || p.Doc >= len(docs) {
			return nil, &pdfo.OptionError{Option: "Pages", Msg: "a page names a document that was not given"}
		}
		pages[i] = pdfo.PageRef{Doc: docs[p.Doc], Index: p.Page, Rotate: p.Rotate, Resize: p.Resize}
	}
	out, err := pdfo.Assemble(pages, &req.Write)
	if err != nil {
		return nil, err
	}
	return result(out)
}

// parsePages(expr: string, numPages: number) → {pages}.
func parsePages(args []js.Value) (map[string]any, error) {
	if len(args) < 2 || args[0].Type() != js.TypeString || args[1].Type() != js.TypeNumber {
		return nil, &jsapi.ArgError{Msg: "parsePages: want a string and a page count"}
	}
	groups, err := pdfo.ParsePages(args[0].String(), args[1].Int())
	if err != nil {
		return nil, err
	}
	return map[string]any{"pages": jsapi.PagesJSON(groups)}, nil
}

// openArgs reads the PDF argument (one, or an array when many), the options
// and opens the documents.
func openArgs(args []js.Value, fn string, many bool) (*jsapi.Request, []*pdfo.Document, error) {
	var files [][]byte
	var err error
	if many {
		files, err = byteList(args, fn, "PDF")
	} else {
		if len(args) < 1 {
			return nil, nil, &jsapi.ArgError{Msg: fn + ": the first argument must be a Uint8Array"}
		}
		var b []byte
		b, err = bytesOf(args[0], fn+": the PDF")
		files = [][]byte{b}
	}
	if err != nil {
		return nil, nil, err
	}
	data, err := optionsJSON(args, 1, fn)
	if err != nil {
		return nil, nil, err
	}
	req, err := jsapi.ParseRequest(fn, data)
	if err != nil {
		return nil, nil, err
	}
	docs := make([]*pdfo.Document, len(files))
	for i, f := range files {
		pw := ""
		if i < len(req.Passwords) {
			pw = req.Passwords[i]
		}
		d, err := pdfo.Open(f, &pdfo.OpenOptions{Password: pw})
		if err != nil {
			if many {
				return nil, nil, &jsapi.InputError{Index: i, Err: err}
			}
			return nil, nil, err
		}
		docs[i] = d
	}
	return req, docs, nil
}

// selection returns the zero-based pages an action applies to: a selection
// string, a list, or, when def is set, every page.
func selection(d *pdfo.Document, req *jsapi.Request, def bool) ([]int, error) {
	switch {
	case req.Pages != "":
		groups, err := pdfo.ParsePages(req.Pages, d.NumPages())
		if err != nil {
			return nil, err
		}
		var pages []int
		for _, g := range groups {
			pages = append(pages, g...)
		}
		return pages, nil
	case req.List != nil:
		return req.List, nil
	case def:
		pages := make([]int, d.NumPages())
		for i := range pages {
			pages[i] = i
		}
		return pages, nil
	}
	return nil, &jsapi.ArgError{Msg: "give the pages"}
}

func result(out *pdfo.Output) (map[string]any, error) {
	pdf, err := toJS(out)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pdf": pdf, "report": jsapi.ReportJSON(out.Report)}, nil
}

// byteList copies the array of Uint8Array at args[0] into Go.
func byteList(args []js.Value, fn, what string) ([][]byte, error) {
	if len(args) < 1 || !isArray(args[0]) {
		return nil, &jsapi.ArgError{Msg: fn + ": the first argument must be an array of Uint8Array"}
	}
	list := args[0]
	out := make([][]byte, list.Length())
	for i := range out {
		b, err := bytesOf(list.Index(i), fn+": "+what+" "+itoa(i+1))
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func isArray(v js.Value) bool {
	return v.Type() == js.TypeObject && js.Global().Get("Array").Call("isArray", v).Bool()
}

// bytesOf copies a Uint8Array into Go memory: the one copy of each input
// the Go side holds.
func bytesOf(v js.Value, what string) ([]byte, error) {
	if v.Type() != js.TypeObject || !v.InstanceOf(js.Global().Get("Uint8Array")) {
		return nil, &jsapi.ArgError{Msg: what + " must be a Uint8Array"}
	}
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b, nil
}

// optionsJSON returns the options object at args[i] as JSON, or nil when
// there is none.
func optionsJSON(args []js.Value, i int, fn string) ([]byte, error) {
	if len(args) <= i || args[i].IsUndefined() || args[i].IsNull() {
		return nil, nil
	}
	if args[i].Type() != js.TypeObject || isArray(args[i]) {
		return nil, &jsapi.ArgError{Msg: fn + ": the options must be an object"}
	}
	return []byte(js.Global().Get("JSON").Call("stringify", args[i]).String()), nil
}

// toJS writes out into a new Uint8Array of exactly its size. The PDF is
// never assembled in Go memory: its parts, much of them slices of the
// inputs, are copied straight across in batches of up to 1 MiB.
func toJS(out *pdfo.Output) (js.Value, error) {
	dst := js.Global().Get("Uint8Array").New(out.Size())
	w := bufio.NewWriterSize(&jsWriter{dst: dst}, 1<<20)
	if _, err := out.WriteTo(w); err != nil {
		return js.Value{}, err
	}
	if err := w.Flush(); err != nil {
		return js.Value{}, err
	}
	return dst, nil
}

// jsWriter writes into a Uint8Array from the start.
type jsWriter struct {
	dst js.Value
	off int
}

func (w *jsWriter) Write(p []byte) (int, error) {
	n := js.CopyBytesToJS(w.dst.Call("subarray", w.off, w.off+len(p)), p)
	w.off += n
	return n, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

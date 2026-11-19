//go:build js && wasm

// Command wasm exposes pdfo to JavaScript when compiled to WebAssembly:
//
//	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o pdfo.wasm ./wasm
//
// Once the module runs, globalThis.pdfo holds the functions. Each one is
// synchronous, meant to be called from a Web Worker, and never throws: on
// failure it returns {error, code} (plus index or option when they apply)
// instead, because a panic escaping into JavaScript would end the Go program
// and every later call with it.
//
// Files go in and out as Uint8Array; reports come back as JSON strings for
// JSON.parse.
//
//	const {pdf, report, error} = pdfo.imagesToPDF([jpeg, png], {pageSize: "A4", margin: "10mm"})
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
	if len(args) < 1 || !isArray(args[0]) {
		return nil, &jsapi.ArgError{Msg: "imagesToPDF: the first argument must be an array of Uint8Array"}
	}
	list := args[0]
	images := make([][]byte, list.Length())
	for i := range images {
		b, err := bytesOf(list.Index(i), "imagesToPDF: image "+itoa(i+1))
		if err != nil {
			return nil, err
		}
		images[i] = b
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
	pdf, err := toJS(out)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pdf": pdf, "report": jsapi.ReportJSON(out.Report)}, nil
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

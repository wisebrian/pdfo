# pdfo

A PDF toolkit in Go, written using only the standard library, meant to run in
the browser as WebAssembly so the user's files never leave their machine.

- **Images to PDF**: JPEG and PNG, one per page; the page fitted to the image
  or to A4, Letter, A3, A5, Legal or any size; portrait, landscape or
  automatic; margins; contain or cover. JPEGs are embedded without
  re-encoding, with their EXIF orientation applied and their metadata (GPS
  position included) left out. Colour profiles are kept.
- **Page actions**: merge, split, extract, delete, reorder, rotate and resize
  pages, or any combination of pages from several documents. Bookmarks,
  links, forms, attachments, layers and tags are kept wherever that is
  possible, and every report says what could not be.
- **Compression**: images downsampled to a target resolution worked out from
  how large each one is drawn, re-encoded as JPEG when that is smaller;
  identical streams stored once; everything else recompressed losslessly.

It reads what real files are: cross-reference streams, object streams,
incremental updates, hybrid files, files whose cross-reference data is broken
or missing, and encrypted files (RC4 and AES, revisions 2 to 6). Hostile
files return errors; they cannot make it panic, hang or run out of memory.

## Library

```go
import "github.com/wisebrian/pdfo"

out, err := pdfo.FromImages([][]byte{jpeg, png}, &pdfo.ImageOptions{
	PageSize: pdfo.A4,
	Margin:   pdfo.MM(10),
})

doc, err := pdfo.Open(data, nil) // or &pdfo.OpenOptions{Password: "…"}
info := doc.Info()               // pages, sizes, bookmarks, forms, images…

pages, err := pdfo.ParsePages("1-3, 5, 8-", doc.NumPages())
out, err = pdfo.Split(doc, pages, nil)
out, err = pdfo.Merge([]*pdfo.Document{a, b}, nil)
out, err = pdfo.ResizePages(doc, []int{0, 1}, pdfo.Resize{To: pdfo.Letter}, nil)
out, err = pdfo.Compress(doc, &pdfo.Ebook)

pdf := out.Bytes()           // or out.WriteTo(w); out.Size() tells the length first
warnings := out.Report.Warnings
```

Output is deterministic: the same input and options give the same bytes.

## WebAssembly

```sh
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o pdfo.wasm ./wasm
```

The module sets `globalThis.pdfo`; [wasm/main.go](wasm/main.go) lists the
functions. They are synchronous, for a Web Worker, take and return
`Uint8Array`, return reports as JSON strings, and never throw:

```js
const {pdf, report, error, code} = pdfo.merge([a, b], {passwords: ["", "secret"]})
```

`wasm/smoketest/run.sh` runs every function in headless Chrome.

## Structure

```
pdfo/
├── doc.go                      overview, and the rules for what page actions keep
├── images.go                   FromImages
├── document.go, info.go        Open, Document, Info
├── assemble.go, actions.go     Assemble and the page actions built on it
├── annots.go, forms.go, outline.go, docparts.go
│                               annotations and links, forms, bookmarks, the rest
├── compress.go                 Compress
├── pages.go                    ParsePages
├── size.go, output.go, errors.go, limits.go
├── internal/syntax/            objects, lexer, parser, serialization
├── internal/reader/            cross-reference data, objects, repair, page tree
├── internal/writer/            numbering, object streams, cross-reference, /ID
├── internal/filter/            Flate, LZW, ASCIIHex, ASCII85, RunLength, predictors
├── internal/crypt/             the standard security handler
├── internal/content/           content streams and where images are drawn
├── internal/raster/            image samples, box filter, re-encoding
├── internal/imgfmt/            JPEG and PNG headers
├── internal/jsapi/             the JavaScript API's options and reports
├── internal/pdftest/           test helpers: qpdf, mutool, Ghostscript, test files
├── wasm/                       the WebAssembly module
└── testdata/                   test files and how each was made
```

## Tests

```sh
go test ./...
```

Every output the tests produce is checked with `qpdf --check`, `mutool info`
and Ghostscript, and page actions are compared, page by page, with the source
as mutool renders them: nothing visible may change. Golden files cover images
to PDF. Each parser, filter and action has a fuzz target. The checks that
need qpdf, mutool or Ghostscript are skipped when they are not installed.

## License

MIT, see [LICENSE](LICENSE).

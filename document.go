package pdfo

import (
	"errors"

	"github.com/wisebrian/pdfo/internal/crypt"
	"github.com/wisebrian/pdfo/internal/reader"
	"github.com/wisebrian/pdfo/internal/syntax"
)

var (
	// ErrPasswordRequired means the file needs a password to open. The UI
	// asks for one and calls Open again with OpenOptions.Password.
	ErrPasswordRequired = errors.New("pdfo: password required")

	// ErrWrongPassword is kept apart from ErrPasswordRequired so the UI can
	// say "wrong password" instead of asking again as if nothing had been
	// typed.
	ErrWrongPassword = errors.New("pdfo: wrong password")
)

// OpenOptions controls Open. The zero value opens unencrypted files and the
// many encrypted ones whose user password is empty.
type OpenOptions struct {
	// Password is tried as the user password, then as the owner password.
	Password string
	// Limits bounds the work a damaged or hostile file can cause.
	Limits Limits
}

// Document is an opened PDF. It reads objects lazily and shares the input's
// bytes instead of copying them, which keeps memory near one copy of the
// input; so the input must not change while the Document, or an Output made
// from it, is in use. A Document is not safe for concurrent use.
type Document struct {
	r      *reader.Reader
	pages  []*reader.Page
	byRef  map[uint32]int // page object number to page index
	limits Limits
}

// Open parses a PDF. When the cross-reference data is missing or its offsets
// are wrong, Open rebuilds it by scanning for objects, as Acrobat and pdf.js
// do; damage makes it fail only when no pages can be found at all.
func Open(data []byte, opts *OpenOptions) (*Document, error) {
	var o OpenOptions
	if opts != nil {
		o = *opts
	}
	if err := o.Limits.check(); err != nil {
		return nil, err
	}
	lim := o.Limits.withDefaults()
	r, err := reader.Open(data, o.Password, reader.Limits{
		MaxDepth: lim.MaxDepth, MaxStreamSize: lim.MaxStreamSize, MaxDecoded: lim.MaxDecoded,
	})
	switch {
	case errors.Is(err, reader.ErrNoPassword):
		return nil, ErrPasswordRequired
	case errors.Is(err, crypt.ErrPassword):
		return nil, ErrWrongPassword
	case errors.Is(err, reader.ErrNotPDF):
		return nil, &FormatError{Offset: -1, Msg: "not a PDF file"}
	case errors.Is(err, errors.ErrUnsupported):
		return nil, err
	case err != nil:
		var fe *reader.FormatError
		if errors.As(err, &fe) {
			return nil, &FormatError{Offset: fe.Offset, Msg: fe.Msg}
		}
		return nil, &FormatError{Offset: -1, Msg: err.Error()}
	}
	d := &Document{r: r, limits: lim, byRef: map[uint32]int{}}
	d.pages = r.Pages()
	if len(d.pages) == 0 {
		return nil, &FormatError{Offset: -1, Msg: "the document has no pages"}
	}
	for i, p := range d.pages {
		if _, ok := d.byRef[p.Ref.Num]; !ok && !p.Ref.IsZero() {
			d.byRef[p.Ref.Num] = i
		}
	}
	return d, nil
}

// NumPages returns the number of pages.
func (d *Document) NumPages() int { return len(d.pages) }

// version returns the PDF version the document declares: the header's, or
// the catalog's when that is later, as the specification says.
func (d *Document) version() string {
	v := d.r.Version
	if cv, ok := d.r.Catalog().Get("Version").(syntax.Name); ok && versionLess(v, string(cv)) {
		v = string(cv)
	}
	return v
}

// versionLess compares versions such as "1.4" and "2.0".
func versionLess(a, b string) bool {
	pa, pb := splitVersion(a), splitVersion(b)
	if pa[0] != pb[0] {
		return pa[0] < pb[0]
	}
	return pa[1] < pb[1]
}

func splitVersion(v string) [2]int {
	var out [2]int
	part := 0
	for _, c := range v {
		switch {
		case c == '.' && part == 0:
			part = 1
		case c >= '0' && c <= '9':
			out[part] = out[part]*10 + int(c-'0')
			if out[part] > 1000 {
				return out
			}
		}
	}
	return out
}

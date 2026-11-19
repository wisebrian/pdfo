package pdfo

import (
	"bytes"
	"io"
	"slices"
	"strings"

	"github.com/wisebrian/pdfo/internal/syntax"
	"github.com/wisebrian/pdfo/internal/writer"
)

// WriteOptions controls how output is written. The zero value suits almost
// every caller.
type WriteOptions struct {
	// XRefTable writes a classic cross-reference table with every object at
	// top level. The default, object streams plus a cross-reference stream,
	// is smaller and read by every viewer since PDF 1.5 (2003).
	XRefTable bool
	// Timestamp, in RFC 3339 form ("2026-11-19T21:05:00+02:00"), becomes the
	// CreationDate of a new document or the ModDate of a changed one. Empty
	// writes no date, so runs stay byte-identical. It is a string, not a
	// time.Time, so this package never imports time.
	Timestamp string
	// Producer, when set, is written as the document's Producer.
	Producer string
}

func (o WriteOptions) check() error {
	if o.Timestamp != "" {
		if _, err := syntax.Date(o.Timestamp); err != nil {
			return &OptionError{"Timestamp", err.Error()}
		}
	}
	return nil
}

// info builds the document information dictionary, or returns nil when
// there is nothing to put in it. created says whether the timestamp is a
// creation date or a modification date.
func (o WriteOptions) info(created bool) *syntax.Dict {
	if o.Producer == "" && o.Timestamp == "" {
		return nil
	}
	d := new(syntax.Dict)
	if o.Producer != "" {
		d.Set("Producer", syntax.TextString(o.Producer))
	}
	if o.Timestamp != "" {
		date, _ := syntax.Date(o.Timestamp) // checked by check
		if created {
			d.Set("CreationDate", date)
		} else {
			d.Set("ModDate", date)
		}
	}
	return d
}

// Output is a finished PDF, held as a plan of its bytes: most of a file is
// stream data copied unchanged from the inputs, and WriteTo copies it straight
// into the destination. In a browser tab that keeps a 200 MB job at three
// copies (the page's input, Go's copy of it, the result) instead of four.
//
// An Output refers to the inputs it was made from, which must not change
// until it has been written.
type Output struct {
	// Report lists what the operation could not keep.
	Report Report
	file   *writer.File
}

// Size returns the exact length WriteTo writes, so the destination can be
// allocated once.
func (o *Output) Size() int64 { return o.file.Size() }

// WriteTo writes the PDF. Calling it again writes the same bytes.
func (o *Output) WriteTo(w io.Writer) (int64, error) { return o.file.WriteTo(w) }

// Bytes returns the PDF in a new slice of exactly Size bytes.
func (o *Output) Bytes() []byte {
	buf := bytes.NewBuffer(make([]byte, 0, o.Size()))
	o.file.WriteTo(buf) // a bytes.Buffer never fails
	return buf.Bytes()
}

// Report says what an operation did that the user should know about.
type Report struct {
	// Pages is the number of pages in the output.
	Pages int
	// InputEncrypted is set when an input was encrypted; output never is.
	InputEncrypted bool
	// Warnings is never nil, so a UI can iterate it without a check.
	Warnings []Warning
}

// Warning is something an operation dropped, changed or could not keep.
type Warning struct {
	// Code is stable ("icc-profile-ignored", "bookmarks-dropped", …) so the
	// UI can translate it; Message is English, for logs and as a fallback.
	Code string
	// Message describes the warning for a reader, in English.
	Message string
	// Count is how many items the warning covers, when that means
	// something.
	Count int
	// Pages lists the output pages it concerns, numbered from 1.
	Pages []int
}

// warnings gathers warnings while an operation runs, merging those with the
// same code so a batch of a hundred photos gives one line, not a hundred.
type warnings struct {
	list []Warning
}

// add records a warning about the given page (from 1; 0 for none) and the
// item that caused it, which goes in the message the first time only.
func (ws *warnings) add(code, msg string, page int) {
	for i := range ws.list {
		w := &ws.list[i]
		if w.Code != code {
			continue
		}
		w.Count++
		if page > 0 && !slices.Contains(w.Pages, page) {
			w.Pages = append(w.Pages, page)
		}
		if !strings.HasSuffix(w.Message, " (and others)") && w.Message != msg {
			w.Message += " (and others)"
		}
		return
	}
	w := Warning{Code: code, Message: msg, Count: 1}
	if page > 0 {
		w.Pages = []int{page}
	}
	ws.list = append(ws.list, w)
}

// result returns the warnings, never nil, so a report always has a list
// to iterate.
func (ws *warnings) result() []Warning {
	if ws.list == nil {
		return []Warning{}
	}
	return ws.list
}

package jsapi

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/wisebrian/pdfo"
)

func TestParseImageOptions(t *testing.T) {
	o, err := ParseImageOptions([]byte(`{"pageSize":"A4","orientation":"landscape","margin":"10mm","fit":"cover",
		"xrefTable":true,"timestamp":"2026-11-19T21:05:00Z","producer":"site"}`))
	if err != nil {
		t.Fatal(err)
	}
	if o.PageSize != pdfo.A4 || o.Orientation != pdfo.Landscape || o.Fit != pdfo.Cover ||
		math.Abs(o.Margin-pdfo.MM(10)) > 1e-9 || !o.Write.XRefTable ||
		o.Write.Timestamp != "2026-11-19T21:05:00Z" || o.Write.Producer != "site" {
		t.Errorf("parsed as %+v", o)
	}
	for _, in := range []string{"", "{}", `{"pageSize":"fit","margin":12}`, `{"margin":null}`} {
		o, err := ParseImageOptions([]byte(in))
		if err != nil || o.PageSize != (pdfo.Size{}) || o.Orientation != pdfo.Auto || o.Fit != pdfo.Contain {
			t.Errorf("%q: %+v, %v", in, o, err)
		}
	}
}

func TestParseImageOptionsErrors(t *testing.T) {
	tests := []struct{ in, option string }{
		{`{"pageSize":"A9"}`, "pageSize"},
		{`{"orientation":"sideways"}`, "orientation"},
		{`{"fit":"stretch"}`, "fit"},
		{`{"margin":"10 parsecs"}`, "margin"},
		{`{"margin":true}`, "margin"},
		{`{"pageSise":"A4"}`, ""}, // misspelt: an argument error
		{`[1,2]`, ""},
	}
	for _, tt := range tests {
		_, err := ParseImageOptions([]byte(tt.in))
		if err == nil {
			t.Errorf("%s: accepted", tt.in)
			continue
		}
		e := ErrorOf(err)
		if e.Code != "bad-argument" || e.Option != tt.option {
			t.Errorf("%s: %+v", tt.in, e)
		}
	}
	_, err := ParseImageOptions([]byte(`{"pageSise":"A4"}`))
	if !strings.Contains(err.Error(), `unknown option "pageSise"`) {
		t.Errorf("message %q does not name the option", err)
	}
}

func TestErrorOf(t *testing.T) {
	tests := []struct {
		err   error
		code  string
		index int
	}{
		{&ArgError{"x"}, "bad-argument", -1},
		{&pdfo.ImageError{Index: 2, Err: &pdfo.FormatError{Offset: -1, Msg: "bad"}}, "damaged", 2},
		{&pdfo.ImageError{Index: 0, Err: fmt.Errorf("gif: %w", errors.ErrUnsupported)}, "unsupported", 0},
		{&pdfo.ImageError{Index: 1, Err: &pdfo.LimitError{Limit: "image size", Value: 1}}, "limit", 1},
		{errors.New("something else"), "internal", -1},
	}
	for _, tt := range tests {
		e := ErrorOf(tt.err)
		if e.Code != tt.code || e.Index != tt.index || e.Message != tt.err.Error() {
			t.Errorf("%v: %+v", tt.err, e)
		}
		obj := e.Object()
		if _, ok := obj["index"]; ok != (tt.index >= 0) {
			t.Errorf("%v: object %v", tt.err, obj)
		}
	}
	if p := Panic("boom"); p.Code != "internal" || !strings.Contains(p.Message, "boom") {
		t.Errorf("Panic: %+v", p)
	}
}

func TestReportJSON(t *testing.T) {
	if got := ReportJSON(pdfo.Report{Pages: 2}); got != `{"pages":2,"warnings":[]}` {
		t.Errorf("got %s", got)
	}
	r := pdfo.Report{Pages: 1, Warnings: []pdfo.Warning{{Code: "c", Message: "m", Count: 1, Pages: []int{1}}}}
	if got := ReportJSON(r); got != `{"pages":1,"warnings":[{"code":"c","message":"m","count":1,"pages":[1]}]}` {
		t.Errorf("got %s", got)
	}
}

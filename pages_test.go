package pdfo

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParsePages(t *testing.T) {
	good := []struct {
		in   string
		want [][]int
	}{
		{"1-3, 5, 8-", [][]int{{0, 1, 2}, {4}, {7, 8, 9}}},
		{"last", [][]int{{9}}},
		{"-3", [][]int{{0, 1, 2}}},
		{"even", [][]int{{1, 3, 5, 7, 9}}},
		{"ODD", [][]int{{0, 2, 4, 6, 8}}},
		{"all", [][]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}}},
		{"2–4; 9—last", [][]int{{1, 2, 3}, {8, 9}}},
		{" 7 ", [][]int{{6}}},
		{"3,3", [][]int{{2}, {2}}},
		{"1-3,", [][]int{{0, 1, 2}}}, // a trailing separator is forgiven
		{"first-2", [][]int{{0, 1}}},
	}
	for _, tt := range good {
		got, err := ParsePages(tt.in, 10)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParsePages(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	bad := []struct {
		in   string
		pos  int
		want string
	}{
		{"", 0, "no pages"},
		{"1, 12", 3, "page 12 does not exist: the document has 10 pages"},
		{"0", 0, "numbered from 1"},
		{"5-2", 0, "runs backwards; write 2-5"},
		{"1,,2", 2, "empty item"},
		{"abc", 0, `"abc" is not a page number`},
		{"-", 0, "needs a page"},
		{"99999999999999999999", 0, "does not exist"},
		{"1, 3-x", 3, `"x" is not a page number`},
	}
	for _, tt := range bad {
		_, err := ParsePages(tt.in, 10)
		var pe *PageRangeError
		if !errors.As(err, &pe) || pe.Pos != tt.pos || !strings.Contains(pe.Msg, tt.want) {
			t.Errorf("ParsePages(%q): %v (pos %v), want %q at %d", tt.in, err, pe, tt.want, tt.pos)
		}
	}
	if _, err := ParsePages("even", 1); err == nil {
		t.Error("even pages of a one-page document accepted")
	}
}

func FuzzParsePages(f *testing.F) {
	for _, s := range []string{"1-3, 5, 8-", "even", "last-1", "2–4;9", ",", "-"} {
		f.Add(s, 10)
	}
	f.Fuzz(func(t *testing.T, s string, n int) {
		if n < 1 || n > 100000 {
			return
		}
		groups, err := ParsePages(s, n)
		if err != nil {
			var pe *PageRangeError
			if !errors.As(err, &pe) || pe.Pos < 0 || pe.Pos > len(s) {
				t.Fatalf("bad error %v", err)
			}
			return
		}
		for _, g := range groups {
			for _, p := range g {
				if p < 0 || p >= n {
					t.Fatalf("page %d out of range", p)
				}
			}
		}
	})
}

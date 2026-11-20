package pdfo

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// PageRangeError explains why a page selection was rejected, such as "page
// 12 does not exist: the document has 10 pages". Pos points at the item so
// the UI can highlight it.
type PageRangeError struct {
	Expr string // the whole selection
	Pos  int    // byte offset of the offending item in Expr
	Msg  string // why, in English
}

func (e *PageRangeError) Error() string { return "pdfo: pages " + strconv.Quote(e.Expr) + ": " + e.Msg }

// ParsePages reads a page selection the way a user types it: "1-3, 5, 8-",
// "-3" (the first three), "even", "odd", "last", "all". Items are separated
// by commas or semicolons; a range's dash may be an en or em dash, which
// keyboards substitute as one types. Pages are numbered from 1 in the text
// and returned zero-based, one group per item, so Split makes one file per
// item and the other actions flatten the groups. Errors are
// *PageRangeError.
func ParsePages(expr string, numPages int) ([][]int, error) {
	fail := func(pos int, msg string) error { return &PageRangeError{Expr: expr, Pos: pos, Msg: msg} }
	if strings.TrimSpace(expr) == "" {
		return nil, fail(0, "no pages given")
	}
	var groups [][]int
	start := 0
	for start <= len(expr) {
		end := strings.IndexAny(expr[start:], ",;")
		if end < 0 {
			end = len(expr)
		} else {
			end += start
		}
		raw := expr[start:end]
		pos := start + len(raw) - len(strings.TrimLeft(raw, " \t"))
		item := strings.ToLower(strings.TrimSpace(raw))
		g, err := parseItem(item, numPages)
		if err != "" {
			return nil, fail(pos, err)
		}
		if g != nil {
			groups = append(groups, g)
		} else if end < len(expr) || len(groups) == 0 {
			return nil, fail(pos, "empty item")
		}
		start = end + 1
	}
	return groups, nil
}

// parseItem reads one item, returning a message when it is invalid. An
// empty item returns nil and no message.
func parseItem(item string, n int) ([]int, string) {
	switch item {
	case "":
		return nil, ""
	case "all":
		return seq(1, n), ""
	case "even", "odd":
		var g []int
		first := 2
		if item == "odd" {
			first = 1
		}
		for p := first; p <= n; p += 2 {
			g = append(g, p-1)
		}
		if g == nil {
			return nil, "the document has no " + item + " pages"
		}
		return g, ""
	}
	// Split a range at its dash, which may be typographic.
	dash := -1
	dashLen := 0
	for i, r := range item {
		if r == '-' || r == '–' || r == '—' || r == '‐' {
			dash, dashLen = i, utf8.RuneLen(r)
			break
		}
	}
	if dash < 0 {
		p, msg := page(item, n)
		if msg != "" {
			return nil, msg
		}
		return []int{p - 1}, ""
	}
	a, b := strings.TrimSpace(item[:dash]), strings.TrimSpace(item[dash+dashLen:])
	from, to := 1, n
	if a != "" {
		var msg string
		if from, msg = page(a, n); msg != "" {
			return nil, msg
		}
	}
	if b != "" {
		var msg string
		if to, msg = page(b, n); msg != "" {
			return nil, msg
		}
	}
	if a == "" && b == "" {
		return nil, "a range needs a page on at least one side of the dash"
	}
	if from > to {
		return nil, "range " + strconv.Itoa(from) + "-" + strconv.Itoa(to) + " runs backwards; write " +
			strconv.Itoa(to) + "-" + strconv.Itoa(from)
	}
	return seq(from, to), ""
}

// page reads a page number or "last".
func page(s string, n int) (int, string) {
	if s == "last" {
		return n, ""
	}
	if s == "first" {
		return 1, ""
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
			// Digits too many for an int.
			return 0, "page " + s + " does not exist: the document has " + strconv.Itoa(n) + " pages"
		}
		return 0, strconv.Quote(s) + " is not a page number"
	}
	switch {
	case v == 0:
		return 0, "pages are numbered from 1"
	case v < 0:
		return 0, strconv.Quote(s) + " is not a page number"
	case v > n:
		return 0, "page " + strconv.Itoa(v) + " does not exist: the document has " + strconv.Itoa(n) + " pages"
	}
	return v, ""
}

func seq(from, to int) []int {
	g := make([]int, 0, to-from+1)
	for p := from; p <= to; p++ {
		g = append(g, p-1)
	}
	return g
}

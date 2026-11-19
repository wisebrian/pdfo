package syntax

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func FuzzDate(f *testing.F) {
	for _, s := range []string{"2026-11-19T21:05:00+02:00", "2026-11-19T19:05:00.123Z", "2024-02-29T00:00:00-05:30", "x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := Date(s)
		if err != nil {
			return
		}
		// D:YYYYMMDDHHmmSS then Z or ±HH'mm.
		if !strings.HasPrefix(string(d), "D:") || (len(d) != 17 && len(d) != 22) {
			t.Fatalf("Date(%q) = %q", s, d)
		}
	})
}

func FuzzTextString(f *testing.F) {
	for _, s := range []string{"pdfo", "Înțeles", "😀", "\xff\xfe", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ts := string(TextString(s))
		if !utf8.ValidString(s) {
			return // replaced by U+FFFD; nothing to compare
		}
		if !strings.HasPrefix(ts, "\xfe\xff") {
			if ts != s {
				t.Fatalf("ASCII %q changed to %q", s, ts)
			}
			return
		}
		u := make([]uint16, 0, len(ts)/2)
		for i := 2; i+1 < len(ts); i += 2 {
			u = append(u, uint16(ts[i])<<8|uint16(ts[i+1]))
		}
		if got := string(utf16.Decode(u)); got != s {
			t.Fatalf("TextString(%q) decodes to %q", s, got)
		}
	})
}

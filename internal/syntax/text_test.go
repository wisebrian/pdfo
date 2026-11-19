package syntax

import "testing"

func TestTextString(t *testing.T) {
	tests := []struct{ in, want string }{
		{"pdfo", "pdfo"},
		{"", ""},
		{"Î", "\xfe\xff\x00\xce"},
		{"a\nb", "\xfe\xff\x00a\x00\n\x00b"},
		{"😀", "\xfe\xff\xd8\x3d\xde\x00"}, // outside the BMP: a surrogate pair
		{"\xff", "\xfe\xff\xff\xfd"},      // invalid UTF-8 becomes U+FFFD
	}
	for _, tt := range tests {
		if got := string(TextString(tt.in)); got != tt.want {
			t.Errorf("TextString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDate(t *testing.T) {
	good := []struct{ in, want string }{
		{"2026-11-19T21:05:00+02:00", "D:20261119210500+02'00"},
		{"2026-11-19T19:05:00.123Z", "D:20261119190500Z"},
		{"2026-11-19t19:05:00z", "D:20261119190500Z"},
		{"2024-02-29T00:00:00-05:30", "D:20240229000000-05'30"},
		{"2026-11-19 21:05:00+02:00", "D:20261119210500+02'00"},
	}
	for _, tt := range good {
		got, err := Date(tt.in)
		if err != nil || string(got) != tt.want {
			t.Errorf("Date(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	bad := []string{
		"", "2026-11-19", "2026-11-19T21:05:00", "2026-13-01T00:00:00Z",
		"2025-02-29T00:00:00Z", "2026-11-19T24:00:00Z", "2026-11-19T23:59:60Z",
		"2026-11-19T21:05:00+2:00", "2026-11-19T21:05:00+24:00", "2026-11-19T21:05:00.Z",
		"2026/11/19T21:05:00Z", "2026-11-19T21:05:00+02:00x", "２026-11-19T21:05:00Z",
	}
	for _, in := range bad {
		if got, err := Date(in); err == nil {
			t.Errorf("Date(%q) = %q, want an error", in, got)
		}
	}
}

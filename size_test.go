package pdfo

import (
	"math"
	"testing"
)

func TestUnits(t *testing.T) {
	if got := MM(25.4); got != 72 {
		t.Errorf("MM(25.4) = %v", got)
	}
	if got := Inches(8.5); got != 612 {
		t.Errorf("Inches(8.5) = %v", got)
	}
	if got := ToMM(MM(210)); math.Abs(got-210) > 1e-9 {
		t.Errorf("ToMM(MM(210)) = %v", got)
	}
}

func TestSizeName(t *testing.T) {
	tests := []struct {
		s    Size
		want string
	}{
		{A4, "A4"},
		{Size{595, 842}, "A4"},
		{Size{595.28, 841.89}, "A4"},
		{Size{842, 595}, "A4"}, // landscape
		{Letter, "Letter"},
		{Size{792, 612}, "Letter"},
		{Legal, "Legal"},
		{A3, "A3"},
		{A5, "A5"},
		{Size{600, 800}, ""},
		{Size{595 + MM(1.5), 842}, ""},
	}
	for _, tt := range tests {
		if got := SizeName(tt.s); got != tt.want {
			t.Errorf("SizeName(%v) = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestParseSize(t *testing.T) {
	near := func(a, b Size) bool {
		return math.Abs(a.Width-b.Width) < 1e-9 && math.Abs(a.Height-b.Height) < 1e-9
	}
	good := []struct {
		in   string
		want Size
	}{
		{"A4", A4},
		{" letter ", Letter},
		{"LEGAL", Legal},
		{"210x297mm", A4},
		{"210 x 297 mm", A4},
		{"210mm x 297mm", A4},
		{"210×297mm", A4},
		{"21 x 29,7 cm", A4},
		{"8.5x11in", Letter},
		{"8.5 x 11 inches", Letter},
		{`8.5x11"`, Letter},
		{"612X792pt", Letter},
		{"8.5in x 279.4mm", Letter},
	}
	for _, tt := range good {
		got, err := ParseSize(tt.in)
		if err != nil || !near(got, tt.want) {
			t.Errorf("ParseSize(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "A9", "210x297", "210x297furlongs", "0x297mm", "-210x297mm", "x297mm", "210xmm", "1e3x2mm", "NaNxNaNmm"} {
		if got, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %v, want an error", in, got)
		}
	}
}

func TestParseLength(t *testing.T) {
	for in, want := range map[string]float64{"10mm": MM(10), "0,5 in": 36, "12pt": 12, "1cm": MM(10)} {
		if got, err := ParseLength(in); err != nil || math.Abs(got-want) > 1e-9 {
			t.Errorf("ParseLength(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "10", "mm", "10 parsecs"} {
		if _, err := ParseLength(in); err == nil {
			t.Errorf("ParseLength(%q) accepted", in)
		}
	}
}

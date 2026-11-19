package syntax

import (
	"math"
	"testing"
)

func TestAppend(t *testing.T) {
	page := new(Dict)
	page.Set("Type", Name("Page"))
	page.Set("Parent", Ref{Num: 2})
	page.Set("MediaBox", Array{Int(0), Int(0), Real(595.2755905511812), Real(841.8897637795276)})
	page.Set("Resources", new(Dict))
	page.Set("Count", Int(3))

	tests := []struct {
		name string
		in   Object
		want string
	}{
		{"null", nil, "null"},
		{"bools", Array{Bool(true), Bool(false)}, "[true false]"},
		{"ints", Array{Int(0), Int(-17), Int(1 << 40)}, "[0 -17 1099511627776]"},
		{"reals", Array{Real(0.5), Real(-0.25), Real(3), Real(1e-9), Real(-1e-9), Real(2.0000004)}, "[0.5 -0.25 3 0 0 2]"},
		{"names", Array{Name("Type"), Name("A B"), Name("x#y"), Name("(paren)"), Name("é")}, "[/Type/A#20B/x#23y/#28paren#29/#C3#A9]"},
		{"literal", String("Hello (world) \\"), `(Hello \(world\) \\)`},
		{"hex", String("\x00\xff\n"), "<00FF0A>"},
		{"empty string", String(""), "()"},
		{"refs", Array{Ref{Num: 1}, Ref{Num: 12, Gen: 3}}, "[1 0 R 12 3 R]"},
		{"dict", page, "<</Type/Page/Parent 2 0 R/MediaBox[0 0 595.275591 841.889764]/Resources<<>>/Count 3>>"},
		{"nested", Array{Array{}, Array{Int(1), Array{Int(2)}}, new(Dict)}, "[[][1[2]]<<>>]"},
		{"stream inline", &Stream{Dict: new(Dict)}, "null"},
	}
	for _, tt := range tests {
		if got := string(Append(nil, tt.in)); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestAppendSeparatesAfterExistingBytes(t *testing.T) {
	// The writer appends objects after "N 0 obj\n"; a number that follows a
	// number already in the buffer must still be separated.
	b := []byte("12")
	if got := string(Append(b, Int(3))); got != "12 3" {
		t.Errorf("got %q", got)
	}
	b = []byte("obj\n")
	if got := string(Append(b, Int(3))); got != "obj\n3" {
		t.Errorf("got %q", got)
	}
}

func TestAppendRealNotFinite(t *testing.T) {
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := string(AppendReal(nil, f)); got != "0" {
			t.Errorf("AppendReal(%v) = %q, want 0", f, got)
		}
	}
	if got := string(AppendReal(nil, -0.0000001)); got != "0" {
		t.Errorf("negative zero written as %q", got)
	}
	if got := string(AppendReal(nil, 1e20)); got != "100000000000000000000" {
		t.Errorf("large real written as %q", got)
	}
}

func TestDict(t *testing.T) {
	d := new(Dict)
	d.Set("A", Int(1))
	d.Set("B", Int(2))
	d.Set("C", Int(3))
	d.Set("A", Int(10)) // keeps its position
	d.Set("B", nil)     // null deletes
	d.Delete("missing")
	if got := string(Append(nil, d)); got != "<</A 10/C 3>>" {
		t.Errorf("got %s", got)
	}
	c := d.Clone()
	c.Set("D", Int(4))
	if d.Len() != 2 || c.Len() != 3 {
		t.Errorf("Clone shares storage: %d, %d", d.Len(), c.Len())
	}
	var nilDict *Dict
	if nilDict.Get("A") != nil || nilDict.Len() != 0 {
		t.Error("nil Dict is not empty")
	}
}

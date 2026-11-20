package syntax

import (
	"errors"
	"strings"
	"testing"
)

func parse(t *testing.T, in string) Object {
	t.Helper()
	o, err := NewParser(NewLexer([]byte(in), 0), 100).Object()
	if err != nil {
		t.Fatalf("parse %q: %v", in, err)
	}
	return o
}

func TestParseObjects(t *testing.T) {
	tests := []struct{ in, want string }{
		{"null", "null"},
		{"true false", "true"},
		{"123", "123"},
		{"-17", "-17"},
		{"+5", "5"},
		{"--5", "-5"}, // pdf.js reads doubled signs as one
		{"4.", "4"},
		{"-.5", "-0.5"},
		{".", "0"},
		{"-", "0"},
		{"12345678901234567890", "12345678901234567168"}, // too big for int64: a real
		{"/Name", "/Name"},
		{"/A#20B", "/A#20B"},
		{"/A#2", "/A#232"}, // an invalid escape is kept as is
		{"/", "/"},
		{"(a(b)c)", `(a\(b\)c)`},
		{`(a\)b)`, `(a\)b)`},
		{`(\101\x\n)`, "<41780A>"},
		{"(a\r\nb)", "<610A62>"},
		{"(a\\\r\nb)", "(ab)"},
		{"(unterminated", "(unterminated)"},
		{"<48 65 6C6c 6>", "(Hell`)"},
		{"<zz41>", "(A)"},
		{"[1 2 R 3]", "[1 2 R 3]"},
		{"[1 0 R]", "[1 0 R]"},
		{"<</A 1/B[2 3]/C<</D(x)>>>>", "<</A 1/B[2 3]/C<</D(x)>>>>"},
		{"<</A 1/A 2>>", "<</A 1>>"},               // the first duplicate wins
		{"<</A/B>>", "<</A/B>>"},                   //
		{"<</A>>", "<<>>"},                         // a key without a value
		{"<</A 1 2/B 3>>", "<</A 1/B 3>>"},         // a stray number where a key belongs
		{"<</A 1 stream", "<</A 1>>"},              // missing >> before "stream"
		{"[1 2 endobj", "[1 2]"},                   // missing ]
		{"[1 ) 2 >> 3]", "[1 2 3]"},                // stray delimiters
		{"%comment\n 7", "7"},                      //
		{"-1 0 R", "null"},                         // not a valid reference
		{"[99999999999 0 R]", "[null]"},            // an impossible reference
		{"<</Length 5 0 R>>", "<</Length 5 0 R>>"}, //
	}
	for _, tt := range tests {
		if got := string(Append(nil, parse(t, tt.in))); got != tt.want {
			t.Errorf("%q parsed as %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "[1 2", "<</A 1", ">", ")", "endobj", "<< /A [ 1"} {
		if o, err := NewParser(NewLexer([]byte(in), 0), 100).Object(); err == nil {
			t.Errorf("%q parsed as %v without error", in, o)
		}
	}
	deep := strings.Repeat("[", 200) + strings.Repeat("]", 200)
	if _, err := NewParser(NewLexer([]byte(deep), 0), 100).Object(); !errors.Is(err, ErrDepth) {
		t.Errorf("deep nesting: %v", err)
	}
	if _, err := NewParser(NewLexer([]byte(strings.Repeat("<</A", 200)), 0), 100).Object(); !errors.Is(err, ErrDepth) {
		t.Errorf("deep dictionaries: %v", err)
	}
}

func TestParserKeywordLeftForCaller(t *testing.T) {
	p := NewParser(NewLexer([]byte("1 0 obj <</Length 3>> stream\nabc"), 0), 100)
	for _, want := range []Kind{Integer, Integer, Keyword} {
		if k := p.Next().Kind; k != want {
			t.Fatalf("token kind %v, want %v", k, want)
		}
	}
	if _, err := p.Object(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Object(); err == nil {
		t.Fatal("stream keyword read as an object")
	}
	if tok := p.Peek(0); tok.Kind != Keyword || tok.Value != "stream" {
		t.Errorf("next token %+v, want the stream keyword", tok)
	}
}

func TestBigDictionaryIsNotQuadratic(t *testing.T) {
	var b strings.Builder
	b.WriteString("<<")
	for i := range 200000 {
		b.WriteString("/K")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(itoa(i))
		b.WriteString(" 1")
	}
	b.WriteString(">>")
	d := parse(t, b.String()).(*Dict)
	if d.Len() != 200000 || d.Get("K0") != Int(1) {
		t.Errorf("got %d keys", d.Len())
	}
}

func itoa(i int) string {
	return strings.TrimSpace(string(AppendReal(nil, float64(i))))
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"<</Type/Page/MediaBox[0 0 612 792]/Contents 4 0 R>>",
		"[1 2 R (str\\)ing) <414243> /N#41me 3.14 -.5 true null]",
		"1 0 obj <</Length 10>> stream\nxxxxxxxxxx\nendstream endobj",
		"<< /A << /B [ ( unbalanced ( > ] >>",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p := NewParser(NewLexer(data, 0), 50)
		for range 1000 {
			before := p.Pos()
			o, err := p.Object()
			if err == nil {
				// Whatever parses must serialise and parse back the same.
				s := Append(nil, o)
				o2, err := NewParser(NewLexer(s, 0), 60).Object()
				if err != nil {
					t.Fatalf("serialised %q does not parse: %v", s, err)
				}
				if s2 := Append(nil, o2); string(s2) != string(s) {
					t.Fatalf("round trip changed %q to %q", s, s2)
				}
			} else {
				p.Next() // skip what could not be read
			}
			if p.Pos() <= before && p.Peek(0).Kind == EOF {
				break
			}
		}
	})
}

func FuzzLexer(f *testing.F) {
	f.Add([]byte("(a\\053\\\nb) <4142> /a#20b 1.5 -- . {}[]<<>> % comment\n kw"))
	f.Fuzz(func(t *testing.T, data []byte) {
		l := NewLexer(data, 0)
		for {
			pos := l.Pos()
			tok := l.Next()
			if tok.Kind == EOF {
				break
			}
			if l.Pos() <= pos {
				t.Fatalf("lexer did not advance at %d", pos)
			}
		}
	})
}

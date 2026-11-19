package jsapi

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// bs is a backslash. The JSON escapes below are built with it rather than
// written inside raw strings, so that no tool on the way can decode them
// before the compiler sees them.
const bs = string(rune(92))

func TestParseJSON(t *testing.T) {
	u := bs + "u"
	good := []struct {
		in   string
		want any
	}{
		{`{}`, map[string]any{}},
		{` {"a": 1, "b": [true, false, null], "c": {"d": "e"}} `, map[string]any{
			"a": 1.0, "b": []any{true, false, nil}, "c": map[string]any{"d": "e"},
		}},
		{`"` + bs + `"` + bs + bs + bs + `/` + bs + `b` + bs + `f` + bs + `n` + bs + `r` + bs + `t"`, "\"\\/\b\f\n\r\t"},
		{`"` + u + `00e9` + u + `d83d` + u + `de00"`, string([]rune{0xE9, 0x1F600})},
		{`-1.5e3`, -1500.0},
		{`[]`, []any{}},
		{`"` + u + `d83d"`, string(rune(0xFFFD))}, // a lone surrogate
	}
	for _, tt := range good {
		got, err := parseJSON([]byte(tt.in))
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseJSON(%s) = %#v, %v; want %#v", tt.in, got, err, tt.want)
		}
	}
	bad := []string{``, `{`, `{"a"}`, `{"a":1,}`, `[1,]`, `"abc`, "\"a\nb\"", `tru`, `nul`, `1e999`,
		`{} {}`, `"` + bs + `x"`, `"` + u + `12"`, `{1:2}`, strings.Repeat("[", 40) + "1" + strings.Repeat("]", 40)}
	for _, in := range bad {
		if got, err := parseJSON([]byte(in)); err == nil {
			t.Errorf("parseJSON(%s) = %#v, want an error", in, got)
		}
	}
}

func TestAppendJSONString(t *testing.T) {
	u := bs + "u"
	tests := []struct{ in, want string }{
		{"plain", `"plain"`},
		{`q"b` + bs, `"q` + bs + `"b` + bs + bs + `"`},
		{"a\nb\tc\x01", `"a` + bs + `nb` + bs + `tc` + u + `0001"`},
		{string(rune(0x2028)), `"` + u + `2028"`},
		{"\xff", `"` + string(rune(0xFFFD)) + `"`},
		{"Înțeles", `"Înțeles"`},
	}
	for _, tt := range tests {
		if got := string(appendJSONString(nil, tt.in)); got != tt.want {
			t.Errorf("appendJSONString(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func FuzzJSON(f *testing.F) {
	for _, s := range []string{`{"pageSize":"A4","margin":"10mm"}`, `[1,"x",{"a":null}]`, `"` + bs + `ud83d` + bs + `ude00"`, `{`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := parseJSON(data)
		if err != nil {
			return
		}
		// Strings that parse must survive a round trip through the writer.
		if s, ok := v.(string); ok && utf8.ValidString(s) {
			back, err := parseJSON(appendJSONString(nil, s))
			if err != nil || back != s {
				t.Fatalf("round trip of %q gave %q, %v", s, back, err)
			}
		}
		_, _ = ParseImageOptions(data)
	})
}

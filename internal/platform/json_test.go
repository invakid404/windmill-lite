package platform

import (
	"math"
	"strings"
	"testing"
)

// TestFormatDouble: doubles in the notation of the backend's JSON library
// (FX-42 rule 2), its plain range and its exponent form.
func TestFormatDouble(t *testing.T) {
	for _, c := range []struct {
		f    float64
		want string
	}{
		{1000, "1000.0"},
		{1500, "1500.0"},
		{1e21, "1e+21"},
		{123456789012345678901234567890, "1.2345678901234568e+29"},
		{0, "0.0"},
		{math.Copysign(0, -1), "-0.0"},
		{1, "1.0"},
		{1.5, "1.5"},
		{43210.1, "43210.1"},
		{5.444310685350916e+14, "544431068535091.6"},
		{-5942736479622170.0, "-5942736479622170.0"},
		{1e16, "1e+16"},
		{1e15, "1000000000000000.0"},
		{0.00001, "0.00001"},
		{0.000012, "0.000012"},
		{1e-6, "1e-6"},
		{5.960464477539062e-07, "5.960464477539062e-7"},
		{6.62607015e-34, "6.62607015e-34"},
		{3.439070283483335e+35, "3.439070283483335e+35"},
		{5e-324, "5e-324"},
		{1.7976931348623157e308, "1.7976931348623157e+308"},
		{0.1, "0.1"},
		{-0.5, "-0.5"},
	} {
		if got := formatDouble(c.f); got != c.want {
			t.Errorf("%v: %s, want %s", c.f, got, c.want)
		}
	}
}

// TestParseAndWrite: the library's parse and its compact output: key
// order kept, a repeated key in its first place with its last value,
// integers exact within 64 bits, minimal string escapes.
func TestParseAndWrite(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"b": 1, "a": 2, "b": 3}`, `{"b":3,"a":2}`},
		{`[18446744073709551615, 18446744073709551616, -1, 1.0, 1E2, 2e-2]`, `[18446744073709551615,1.8446744073709552e+19,-1,1.0,100.0,0.02]`},
		{` "\u00e9\/\"\\\b\f\n\r\t\u0001\u007f\ud83d\ude00" `, "\"\u00e9/\\\"\\\\\\b\\f\\n\\r\\t\\u0001\x7f\U0001f600\""},
		{`{"k":[true,false,null,{}],"e":[]}`, `{"k":[true,false,null,{}],"e":[]}`},
	} {
		n, err := parseJSON([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got := string(n.write(nil)); got != c.want {
			t.Errorf("%s: %s, want %s", c.in, got, c.want)
		}
	}
}

// TestParseErrors: the library's refusals, in its words and with the
// line and column it stood at (FX-51/c among them).
func TestParseErrors(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{``, "EOF while parsing a value at line 1 column 0"},
		{`x`, "expected value at line 1 column 1"},
		{`{"s": "\ud83d"}`, "unexpected end of hex escape at line 1 column 14"},
		{`"\ude00"`, "lone leading surrogate in hex escape at line 1 column 7"},
		{`"\ud83d\u0041"`, "lone leading surrogate in hex escape at line 1 column 13"},
		{`{"n": 1e400}`, "number out of range at line 1 column 11"},
		{`[1,]`, "trailing comma at line 1 column 4"},
		{`{"a":1,}`, "trailing comma at line 1 column 8"},
		{`[1 2]`, "expected `,` or `]` at line 1 column 4"},
		{`{"a" 1}`, "expected `:` at line 1 column 6"},
		{`{1:1}`, "key must be a string at line 1 column 2"},
		{`1 2`, "trailing characters at line 1 column 3"},
		{"\"a\nb\"", "control character (\\u0000-\\u001F) found while parsing a string at line 2 column 0"},
		{`"\x"`, "invalid escape at line 1 column 3"},
		{`01`, "invalid number at line 1 column 2"},
		{`tru`, "EOF while parsing a value at line 1 column 3"},
		{`[`, "EOF while parsing a list at line 1 column 1"},
		{"[ \n", "EOF while parsing a list at line 2 column 0"},
		{`[1,2`, "EOF while parsing a list at line 1 column 4"},
		{`[1,`, "EOF while parsing a value at line 1 column 3"},
		{`{"a":1} x`, "trailing characters at line 1 column 9"},
		{"{\n\"a\":1", "EOF while parsing an object at line 2 column 5"},
		// An object cut short: unclosed, a member short, and a key due
		// after a comma, which the library reports as a missing value.
		{`{`, "EOF while parsing an object at line 1 column 1"},
		{`{"a"`, "EOF while parsing an object at line 1 column 4"},
		{`{"a":`, "EOF while parsing a value at line 1 column 5"},
		{`{"a":1`, "EOF while parsing an object at line 1 column 6"},
		{`{"a":1,`, "EOF while parsing a value at line 1 column 7"},
		{`{"a":1, `, "EOF while parsing a value at line 1 column 8"},
		{`{"a":1,x`, "key must be a string at line 1 column 8"},
		{`{"a`, "EOF while parsing a string at line 1 column 3"},
		// The array opened at depth 128 fails.
		{strings.Repeat("[", 128) + "1" + strings.Repeat("]", 128), "recursion limit exceeded at line 1 column 128"},
		{`{"k": ` + strings.Repeat("[", 127) + strings.Repeat("]", 127) + `}`, "recursion limit exceeded at line 1 column 133"},
	} {
		_, err := parseJSON([]byte(c.in))
		if err == nil || err.Error() != c.want {
			t.Errorf("%q: %v, want %s", c.in, err, c.want)
		}
	}
	if _, err := parseJSON([]byte(strings.Repeat("[", 127) + strings.Repeat("]", 127))); err != nil {
		t.Errorf("127 levels: %v", err)
	}
}

package openflow

import (
	"errors"
	"strings"
	"testing"
)

func TestParseJSONRejectsMalformedInput(t *testing.T) {
	for _, in := range []string{
		``, ` `, `{`, `{"a":}`, `{"a" 1}`, `{a:1}`, `{"a":1,}`, `[1,]`, `[1 2]`,
		`01`, `-`, `1.`, `.5`, `1e`, `+1`, `NaN`, `Infinity`, `tru`, `nul`,
		`"abc`, `"\x"`, `"\u12"`, `"\ud800"`, `"\ud800A"`, `"\udc00"`,
		"\"a\tb\"", "\"\xff\"", "\xef\xbb\xbf{}", `{} {}`, `{}x`, `// c` + "\n{}",
	} {
		_, err := parseJSON([]byte(in))
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrSyntax {
			t.Errorf("parseJSON(%q) = %v, want a syntax error", in, err)
		}
	}
}

func TestParseJSONStrings(t *testing.T) {
	n, err := parseJSON([]byte(`"a\"\\\/\b\f\n\r\té😀z"`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "a\"\\/\b\f\n\r\té😀z"; n.str != want {
		t.Fatalf("got %q, want %q", n.str, want)
	}
}

// An input that ends early is reported in serde's words where the
// reference quotes them (expressions.md, JE-42's positions): "a list" or
// "an object" for an unclosed container, "a value" where a value or a key
// was due, and a trailing comma for an object closed after a comma.
// The position follows this parser's own convention.
func TestParseJSONEOFMessages(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`[`, "EOF while parsing a list"},
		{`[ `, "EOF while parsing a list"},
		{`[1,2`, "EOF while parsing a list"},
		{`[1,`, "EOF while parsing a value"},
		{`{`, "EOF while parsing an object"},
		{`{"a"`, "EOF while parsing an object"},
		{`{"a":`, "EOF while parsing a value"},
		{`{"a":1`, "EOF while parsing an object"},
		{`{"a":1,`, "EOF while parsing a value"},
		{`{"a":1, `, "EOF while parsing a value"},
		{`{"a":1,}`, "trailing comma"},
		{`{"a":1,x`, "key must be a string"},
	} {
		_, err := parseJSON([]byte(c.in))
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrSyntax || !strings.HasPrefix(e.Detail, c.want+" at line 1 column ") {
			t.Errorf("parseJSON(%q) = %v, want %s", c.in, err, c.want)
		}
	}
}

func TestParseJSONSyntaxErrorPosition(t *testing.T) {
	_, err := parseJSON([]byte("{\n  \"a\": ,\n}"))
	if err == nil || !strings.Contains(err.Error(), "at line 2 column 8") {
		t.Fatalf("got %v", err)
	}
}

func TestParseJSONDepthLimit(t *testing.T) {
	deep := strings.Repeat("[", maxDepth) + strings.Repeat("]", maxDepth)
	if _, err := parseJSON([]byte(deep)); err != nil {
		t.Fatalf("depth %d: %v", maxDepth, err)
	}
	deeper := "[" + deep + "]"
	if _, err := parseJSON([]byte(deeper)); err == nil || !strings.Contains(err.Error(), "recursion limit") {
		t.Fatalf("depth %d: %v", maxDepth+1, err)
	}
}

func TestParseJSONKeepsMembersAndRawText(t *testing.T) {
	n, err := parseJSON([]byte(`{"b": 1.0 , "a":[ 1e2 ,-0],"b":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, m := range n.members {
		keys = append(keys, m.key)
	}
	if strings.Join(keys, ",") != "b,a,b" {
		t.Fatalf("members %v, want b,a,b in input order", keys)
	}
	if got := string(n.members[0].val.raw); got != "1.0" {
		t.Errorf("raw = %q", got)
	}
	if got := string(n.members[1].val.raw); got != "[ 1e2 ,-0]" {
		t.Errorf("raw = %q", got)
	}
	if got := pointer(nil, n.members[1].val.elems[1]); got != "/a/1" {
		t.Errorf("pointer = %q", got)
	}
}

func TestPointerEscapesKeys(t *testing.T) {
	n, err := parseJSON([]byte(`{"a/b":{"c~d":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := pointer(nil, n.members[0].val.members[0].val); got != "/a~1b/c~0d" {
		t.Fatalf("pointer = %q", got)
	}
}

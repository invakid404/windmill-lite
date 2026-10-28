package yamlx_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/yamlx"
)

func mustParse(t *testing.T, src string) *yamlx.Node {
	t.Helper()
	n, err := yamlx.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return n
}

// Numbers keep their source text; their value is the CLI's double (SY-9).
func TestNumberTextAndValue(t *testing.T) {
	n := mustParse(t, "a: 017\nb: 0x1F\nc: 1e3\nd: .inf\ne: 9007199254740993\nf: '017'\n")
	for _, c := range []struct {
		key  string
		text string
		num  float64
	}{
		{"a", "017", 17},
		{"b", "0x1F", 31},
		{"c", "1e3", 1000},
		{"d", ".inf", math.Inf(1)},
		{"e", "9007199254740993", 9007199254740992},
	} {
		v := n.Get(c.key)
		if v == nil || v.Kind != yamlx.Number || v.Text != c.text {
			t.Fatalf("%s: got %+v, want number %q", c.key, v, c.text)
		}
		if f, ok := v.Float64(); !ok || f != c.num {
			t.Errorf("%s: Float64() = %v, %v; want %v", c.key, f, ok, c.num)
		}
	}
	if f := n.Get("f"); f.Kind != yamlx.String || f.Text != "017" {
		t.Errorf("quoted 017: got %v %q, want a string", f.Kind, f.Text)
	}
	if _, ok := n.Get("f").Float64(); ok {
		t.Error("Float64 of a string reports ok")
	}
}

func TestKindsAndPositions(t *testing.T) {
	n := mustParse(t, "a: [1, true, ~, x]\nb:\n  c: d\n")
	if n.Kind != yamlx.Mapping || n.Line != 1 || n.Column != 1 {
		t.Fatalf("root: got %v at %d:%d", n.Kind, n.Line, n.Column)
	}
	a := n.Get("a")
	if a.Kind != yamlx.Sequence || a.Line != 1 || a.Column != 4 || len(a.Items) != 4 {
		t.Fatalf("a: got %v at %d:%d with %d items", a.Kind, a.Line, a.Column, len(a.Items))
	}
	for i, want := range []yamlx.Kind{yamlx.Number, yamlx.Bool, yamlx.Null, yamlx.String} {
		if a.Items[i].Kind != want {
			t.Errorf("a[%d]: got %v, want %v", i, a.Items[i].Kind, want)
		}
	}
	if b := a.Items[1]; b.Text != "true" || b.Column != 8 {
		t.Errorf("a[1]: got %q at column %d", b.Text, b.Column)
	}
	if c := n.Get("b").Get("c"); c.Kind != yamlx.String || c.Text != "d" || c.Line != 3 || c.Column != 6 {
		t.Errorf("b.c: got %+v", c)
	}
	if n.Get("missing") != nil || a.Get("a") != nil {
		t.Error("Get of a missing key, or on a sequence, is not nil")
	}
	if got := yamlx.Mapping.String(); got != "mapping" {
		t.Errorf("Mapping.String() = %q", got)
	}
	empty := mustParse(t, "# nothing\n")
	if empty.Kind != yamlx.Null || empty.Line != 0 {
		t.Errorf("empty stream: got %v at line %d, want null at line 0", empty.Kind, empty.Line)
	}
}

// Pairs follow JavaScript property order, the order of the CLI's object.
func TestPairsOrder(t *testing.T) {
	n := mustParse(t, "b: 1\n2: x\na: 3\n1: y\n\"1\": z\n")
	var keys []string
	for _, p := range n.Pairs {
		keys = append(keys, p.Key)
	}
	if got := strings.Join(keys, ","); got != "1,2,b,a" {
		t.Errorf("keys: %s, want 1,2,b,a", got)
	}
	if v := n.Get("1"); v.Text != "z" {
		t.Errorf(`"1": got %q, want the later value z`, v.Text)
	}
}

// An alias stands for the anchored node itself.
func TestAliasSharesNode(t *testing.T) {
	n := mustParse(t, "a: &x {k: [1]}\nb: *x\n")
	if n.Get("a") != n.Get("b") {
		t.Error("alias does not share the anchored node")
	}
	if b := n.Get("b"); b.Line != 1 || b.Column != 4 {
		t.Errorf("alias position: %d:%d, want the anchor's 1:4", b.Line, b.Column)
	}
}

func TestAliasExpansionBound(t *testing.T) {
	// Each level repeats the previous one 16 times: 16^6 nodes.
	var b strings.Builder
	b.WriteString("l0: &l0 [x]\n")
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&b, "l%d: &l%d [*l%d%s]\n", i, i, i-1, strings.Repeat(fmt.Sprintf(", *l%d", i-1), 15))
	}
	_, err := yamlx.Parse([]byte(b.String()))
	var e *yamlx.Error
	if !errors.As(err, &e) || e.Kind != yamlx.Unsupported || !strings.Contains(e.Msg, "aliases add more than") {
		t.Fatalf("got %v, want the alias expansion refusal", err)
	}

	// Moderate reuse stays accepted, although the CLI refuses an anchor
	// used 100 times or more.
	b.Reset()
	b.WriteString("base: &b {k: v}\n")
	for i := range 1000 {
		fmt.Fprintf(&b, "k%d: *b\n", i)
	}
	if _, err := yamlx.Parse([]byte(b.String())); err != nil {
		t.Fatalf("1000 aliases of a small mapping: %v", err)
	}
}

// The CLI's writer starts a block scalar with a tab when the text's first
// line does; Parse reads every such scalar of a document.
func TestTabLedBlockScalars(t *testing.T) {
	var b strings.Builder
	for i := range 50 {
		fmt.Fprintf(&b, "k%d: |-\n  \tline %d\n  next\n", i, i)
	}
	n := mustParse(t, b.String())
	for _, k := range []string{"k0", "k49"} {
		want := "\tline " + strings.TrimPrefix(k, "k") + "\nnext"
		if v := n.Get(k); v == nil || v.Text != want {
			t.Errorf("%s: got %+v, want %q", k, v, want)
		}
	}
	// A tab the YAML 1.1 parser refuses elsewhere is not hidden.
	if _, err := yamlx.Parse([]byte("a: |\n  x\n \ty\n")); err == nil {
		t.Error("a tab inside a block scalar's indentation parses")
	}
}

func TestErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		src  string
		kind yamlx.ErrorKind
		line int
		col  int
		msg  string
	}{
		{"invalid UTF-8", "a: b\nc: \xff\n", yamlx.Unsupported, 2, 4, "not valid UTF-8"},
		{"UTF-16 byte order mark", "\xff\xfea\x00", yamlx.Unsupported, 1, 1, "not valid UTF-8"},
		{"duplicate key", "a: 1\nb: 2\na: 3\n", yamlx.Syntax, 3, 1, `duplicate mapping key "a"`},
		{"tag with a flow indicator", "[!, a]\n", yamlx.Unsupported, 1, 2, "flow indicator"},
		{"library error with a line", "a: 1\nb: c: d\n", yamlx.Syntax, 2, 0, "mapping values are not allowed"},
		{"library error without a line", "a: \"\\q\"\n", yamlx.Syntax, 0, 0, "found unknown escape character"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := yamlx.Parse([]byte(c.src))
			var e *yamlx.Error
			if !errors.As(err, &e) {
				t.Fatalf("got %v (%T), want a *yamlx.Error", err, err)
			}
			if e.Kind != c.kind || e.Line != c.line || e.Column != c.col || !strings.Contains(e.Msg, c.msg) {
				t.Errorf("got %#v, want %s at %d:%d containing %q", e, c.kind, c.line, c.col, c.msg)
			}
		})
	}
	e := &yamlx.Error{Kind: yamlx.Syntax, Line: 2, Column: 5, Msg: "m"}
	if got := e.Error(); got != "yaml_syntax: line 2 column 5: m" {
		t.Errorf("Error() = %q", got)
	}
	e.Column = 0
	if got := e.Error(); got != "yaml_syntax: line 2: m" {
		t.Errorf("Error() without a column = %q", got)
	}
}

// MarshalJSON gives the CLI's text; json.Marshal re-escapes it (HTML
// characters, U+2028), which changes bytes but not the value.
func TestMarshalJSON(t *testing.T) {
	n := mustParse(t, "a: '<&>'\nb: [1.0, -0, .nan]\n")
	got, err := n.MarshalJSON()
	if err != nil || string(got) != `{"a":"<&>","b":[1,0,null]}` {
		t.Fatalf("MarshalJSON = %s, %v", got, err)
	}
	wrapped, err := json.Marshal(struct {
		N *yamlx.Node `json:"n"`
	}{n})
	if err != nil {
		t.Fatal(err)
	}
	var back struct{ N map[string]any }
	if err := json.Unmarshal(wrapped, &back); err != nil || back.N["a"] != "<&>" {
		t.Fatalf("round trip through encoding/json: %s, %v", wrapped, err)
	}
	var nilNode *yamlx.Node
	if got := nilNode.AppendJSON([]byte("x=")); !bytes.Equal(got, []byte("x=null")) {
		t.Errorf("nil AppendJSON = %s", got)
	}
}

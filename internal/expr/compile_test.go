package expr

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestWrapperSingleLine pins the wrapper of JE-13 and JE-14: the opening
// text sits on the code's first line and the closing text on its last, so
// a trailing // comment on that line comments the closing text out, and a
// newline after it, as a predicate's YAML block keeps, rescues it.
func TestWrapperSingleLine(t *testing.T) {
	for _, tc := range []struct {
		site     SiteKind
		text     string
		mode     Mode
		lastLine string
	}{
		// spec: JE-14/a — the comment is followed by the closing text.
		{SiteInputTransform, "flow_input.n // the count", ModeExpression, "(async function() { return flow_input.n // the count; })()"},
		// spec: JE-14 — statement mode alike.
		{SiteInputTransform, "let y = 1; return y // c", ModeStatement, "(async function() { let y = 1; return y // c })()"},
		// spec: JE-14 — a multi-line text keeps its lines; the closing
		// text is on the last.
		{SiteInputTransform, "1 +\n2 // c", ModeExpression, "2 // c; })()"},
		// spec: JE-14, JE-24 — a predicate's trailing newline moves its
		// closing parenthesis, and the wrapper's, to the next line.
		{SiteSkipIf, "flow_input.n > 1 // c\n", ModeExpression, "); })()"},
	} {
		e := Compile(tc.site, tc.text)
		if e.Mode != tc.mode {
			t.Errorf("%q: mode %v, want %v", tc.text, e.Mode, tc.mode)
		}
		lines := strings.Split(e.Source, "\n")
		if got := lines[len(lines)-1]; got != tc.lastLine {
			t.Errorf("%q: the source's last line is %q, want %q", tc.text, got, tc.lastLine)
		}
		if !strings.HasPrefix(lines[0], wrapOpen) {
			t.Errorf("%q: the source's first line %q does not open the wrapper", tc.text, lines[0])
		}
	}
	for _, s := range []string{wrapOpen, wrapReturn, wrapCloseExpr, wrapCloseStatement} {
		if strings.Contains(s, "\n") {
			t.Errorf("wrapper text %q spans lines", s)
		}
	}
}

// TestPredicateWrapping checks JE-24 at every boolean site, and that no
// other site is wrapped.
func TestPredicateWrapping(t *testing.T) {
	for k := SiteInputTransform; k.Valid(); k++ {
		e := Compile(k, " x ")
		want := "x"
		if k.Predicate() {
			want = "Boolean( x )"
		}
		if e.Trimmed != want || e.Text != " x " || e.Site != k {
			t.Errorf("%v: Compile gave %+v", k, e)
		}
	}
}

// TestCompiledExprJSON checks that a compiled expression survives the IR's
// JSON form (ADR 0002).
func TestCompiledExprJSON(t *testing.T) {
	for _, text := range []string{"results.a.b", "flow_input[\"k\"]", "previous_result", "let x = 1; return x", ""} {
		e := Compile(SiteInputTransform, text)
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var back Expr
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		if !reflect.DeepEqual(&back, e) {
			t.Errorf("%q: round trip of %s gave %+v", text, data, back)
		}
	}
}

// FuzzCompile checks the invariants of the textual stages on any text: the
// trimmed text is the text trimmed (wrapped for a predicate), the source is
// the rewritten code in its mode's wrapper, removing the rewrites' awaits
// gives the trimmed text back, and the bound names are sorted and bindable.
func FuzzCompile(f *testing.F) {
	for _, s := range []string{"results.a\n+ 100", "variable(resource('a'))", "\"see results.a\";", "flow_input[\"a\"]", "results[\"a\"] + results[\"b\"]", "variable('x'", "return", "error"} {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, text string, predicate bool) {
		site := SiteInputTransform
		if predicate {
			site = SiteSkipIf
		}
		e := Compile(site, text)
		want := text
		if predicate {
			want = "Boolean(" + text + ")"
		}
		if e.Trimmed != strings.TrimSpace(want) {
			t.Fatalf("trimmed %q", e.Trimmed)
		}
		code, ok := unwrap(e.Mode, e.Source)
		if !ok {
			t.Fatalf("source %q is not in its wrapper", e.Source)
		}
		if mode(e.Trimmed) != e.Mode {
			t.Fatal("mode differs from the trimmed text's")
		}
		if !slices.IsSorted(e.Binds) {
			t.Fatalf("binds %q are not sorted", e.Binds)
		}
		for _, b := range e.Binds {
			if !slices.Contains(bindable, b) {
				t.Fatalf("binds %q", b)
			}
		}
		// Every rewrite inserts "(await " and a ")"; with the original
		// text holding no "(await ", dropping them gives the text back.
		if !strings.Contains(e.Trimmed, "(await ") && strings.Count(code, "(await ") > 0 {
			stripped := strings.ReplaceAll(code, "(await ", "")
			if len(stripped) != len(e.Trimmed)+strings.Count(code, "(await ") {
				t.Fatalf("code %q is not the trimmed text %q with awaits", code, e.Trimmed)
			}
		}
	})
}

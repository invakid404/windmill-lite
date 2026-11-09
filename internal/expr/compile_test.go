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
	for _, s := range []string{wrapOpen, wrapReturn, wrapCloseExpr, wrapCloseStatement, noPrefetch, prefetchCall, thenOpen, thenClose, serialise} {
		if strings.Contains(s, "\n") {
			t.Errorf("wrapper text %q spans lines", s)
		}
	}
}

// TestProgramLayout pins the program of JE-13 at CE 1.862.0 around the
// code: the prefetch, Promise.resolve() or __loadResults with the JSON
// array of the named steps' literals (JE-12), then .then(() => and the
// function, with nothing between them, so the code's first line starts at
// column n + 40 in expression mode and n + 33 in statement mode, n being
// the prefetch's length; the closing text and the serialisation follow the
// code's last line.
func TestProgramLayout(t *testing.T) {
	for _, tc := range []struct {
		text     string
		site     SiteKind
		results  bool
		prefetch string
		offset   int
	}{
		// spec: JE-13 — no step named: columns 57 and 50.
		{"flow_input.n", SiteInputTransform, true, "Promise.resolve()", 40},
		{"let y = 1; return y", SiteInputTransform, true, "Promise.resolve()", 33},
		// spec: JE-12, JE-13 — the examples of JE-13's first part.
		{"results.a", SiteInputTransform, true, `__loadResults(["\"a\""])`, 40},
		{"results['b'] + results.c", SiteInputTransform, true, `__loadResults(["\"c\"","'b'"])`, 40},
		// spec: JE-21 — the prefix of cf-19's recorded stack.
		{"results.missing.y[2].z", SiteInputTransform, true, `__loadResults(["\"missing\""])`, 40},
		// spec: JE-12 — serde_json's escapes in the array, and the literal
		// as written.
		{"results[\"\\x61\"] + results['\t']", SiteInputTransform, true, `__loadResults(["\"\\x61\"","'\t'"])`, 40},
		// spec: JE-12 — no results context, no prefetch.
		{"results.a", SiteSleep, false, "Promise.resolve()", 40},
		// spec: JE-24 — a predicate's code is its Boolean(...) wrapping.
		{"results.a > 1", SiteSkipIf, true, `__loadResults(["\"a\""])`, 40},
	} {
		e := Compile(tc.site, tc.text)
		src := programSource(e, tc.results)
		code, _ := unwrap(e.Mode, e.Source)
		first, _, _ := strings.Cut(code, "\n")
		if !strings.HasPrefix(src, tc.prefetch+thenOpen+"(async function() { ") {
			t.Errorf("%q: program %q does not start with %q", tc.text, src, tc.prefetch)
		}
		if col := strings.Index(src, first) + 1; col != len(tc.prefetch)+tc.offset {
			t.Errorf("%q: the code starts at column %d, want %d", tc.text, col, len(tc.prefetch)+tc.offset)
		}
		if !strings.HasSuffix(src, thenClose+serialise) || !strings.Contains(src, e.Source+thenClose+serialise) {
			t.Errorf("%q: program %q does not end with the source, ) and the serialisation", tc.text, src)
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
// the rewritten code in its mode's wrapper, removing the helper rewrite's
// awaits gives the trimmed text back, the bound names are sorted and
// bindable, and the prefetch's literals are sorted, without repeats, each a
// one-line string literal or a dot read's identifier that the text holds.
func FuzzCompile(f *testing.F) {
	for _, s := range []string{"results.a\n+ 100", "variable(resource('a'))", "\"see results.a\";", "flow_input[\"a\"]", "results[\"a\"] + results[\"b\"]", "variable('x'", "return", "error", "results?.['a\\'b'] + $results.c", "results[\"\\x61\"]results.b"} {
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
		if !slices.IsSorted(e.Prefetch) || len(slices.Compact(slices.Clone(e.Prefetch))) != len(e.Prefetch) {
			t.Fatalf("prefetch %q is not sorted without repeats", e.Prefetch)
		}
		for _, lit := range e.Prefetch {
			if len(lit) < 2 || lit[0] != '"' && lit[0] != '\'' || lit[len(lit)-1] != lit[0] || strings.ContainsAny(lit, "\n\r") {
				t.Fatalf("prefetch literal %q", lit)
			}
			if !strings.Contains(e.Trimmed, "results["+lit) && !strings.Contains(e.Trimmed, "results?.["+lit) && !strings.Contains(e.Trimmed, "results."+lit[1:len(lit)-1]) && !strings.Contains(e.Trimmed, "results?."+lit[1:len(lit)-1]) {
				t.Fatalf("prefetch literal %q is not read in %q", lit, e.Trimmed)
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

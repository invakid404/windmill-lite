package syncfolder

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

// testdata/minimatch.json holds glob cases observed on npm minimatch as a
// black box (its about field says which versions), and the patterns
// windmill-lite refuses.
type minimatchFile struct {
	About  []string `json:"about"`
	Groups []struct {
		Group    string `json:"group"`
		Patterns []struct {
			Pattern string   `json:"pattern"`
			Match   []string `json:"match"`
			NoMatch []string `json:"no_match"`
		} `json:"patterns"`
	} `json:"groups"`
	Refused []struct {
		Pattern string `json:"pattern"`
		Error   string `json:"error"`
	} `json:"refused"`
}

func loadMinimatch(t *testing.T) minimatchFile {
	t.Helper()
	var f minimatchFile
	readStrict(t, "testdata/minimatch.json", &f)
	return f
}

func TestMinimatchCases(t *testing.T) {
	f := loadMinimatch(t)
	n := 0
	for _, g := range f.Groups {
		for _, p := range g.Patterns {
			gl, err := compileGlob(p.Pattern)
			if err != nil {
				t.Errorf("%s: %q: %v", g.Group, p.Pattern, err)
				continue
			}
			for _, s := range p.Match {
				if !gl.match(s) {
					t.Errorf("%s: %q does not match %q", g.Group, p.Pattern, s)
				}
				n++
			}
			for _, s := range p.NoMatch {
				if gl.match(s) {
					t.Errorf("%s: %q matches %q", g.Group, p.Pattern, s)
				}
				n++
			}
		}
	}
	for _, r := range f.Refused {
		_, err := compileGlob(r.Pattern)
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrGlob || !strings.Contains(e.Msg, r.Error) {
			t.Errorf("%q: error %v, want unsupported_glob containing %q", r.Pattern, err, r.Error)
		}
	}
	t.Logf("%d observations, %d refusals", n, len(f.Refused))
}

// TestMinimatchCrossCheck re-derives every observation of
// testdata/minimatch.json from npm minimatch. It runs only when
// SYNCFOLDER_MINIMATCH names an unpacked minimatch package (with its
// dependencies installed) and node is on PATH:
//
//	npm install --ignore-scripts --prefix "$D" minimatch@10.2.6
//	SYNCFOLDER_MINIMATCH="$D/node_modules/minimatch" go test -run TestMinimatchCrossCheck ./internal/syncfolder
func TestMinimatchCrossCheck(t *testing.T) {
	dir := os.Getenv("SYNCFOLDER_MINIMATCH")
	if dir == "" {
		t.Skip("SYNCFOLDER_MINIMATCH is not set")
	}
	f := loadMinimatch(t)
	type obs struct {
		Pattern, Path string
		Want          bool
	}
	var all []obs
	for _, g := range f.Groups {
		for _, p := range g.Patterns {
			for _, s := range p.Match {
				all = append(all, obs{p.Pattern, s, true})
			}
			for _, s := range p.NoMatch {
				all = append(all, obs{p.Pattern, s, false})
			}
		}
	}
	in, _ := json.Marshal(all)
	const script = `const m = require(process.argv[1]); const mm = m.minimatch || m;
const cases = JSON.parse(require('fs').readFileSync(0, 'utf8'));
console.log(JSON.stringify(cases.map(c => mm(c.Path, c.Pattern))));`
	cmd := exec.Command("node", "-e", script, dir)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var got []bool
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(all) {
		t.Fatalf("node output %q: %v", out, err)
	}
	for i, o := range all {
		if got[i] != o.Want {
			t.Errorf("minimatch(%q, %q) = %v; the case file says %v", o.Path, o.Pattern, got[i], o.Want)
		}
	}
	t.Logf("%d observations re-derived", len(all))
}

func TestGlobNegationAndComments(t *testing.T) {
	for _, p := range []string{"f/**", "**/*.ts", "f/{a,b}/*", "f/[!a]?/x.ts"} {
		g, err := compileGlob(p)
		if err != nil {
			t.Fatal(err)
		}
		n, err := compileGlob("!" + p)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"f/a/x.ts", "u/x.ts", "f/bc/x.ts", "f/.a/x.ts"} {
			if g.match(s) == n.match(s) {
				t.Errorf("%q and its negation agree on %q", p, s)
			}
		}
	}
	c, err := compileGlob("#f/**")
	if err != nil || c.match("#f/a") || c.match("f/a") {
		t.Errorf("a comment pattern matched (%v)", err)
	}
}

func TestBraceExpansionBound(t *testing.T) {
	_, err := compileGlob(strings.Repeat("{a,b}", 13))
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrGlob || !strings.Contains(e.Msg, "brace expansions") {
		t.Fatalf("got %v, want the expansion bound", err)
	}
	if _, err := compileGlob("f/{1..100000}"); err == nil {
		t.Fatal("a sequence beyond the bound compiled")
	}
	if _, err := compileGlob(strings.Repeat("{a,b}", 12)); err != nil {
		t.Fatalf("4096 expansions: %v", err)
	}
}

// stepBound is the most steps matchSteps may take for one alternative:
// each (pattern segment, path segment) state is computed once; a segment
// match of t tokens against n code units takes at most (n+1)(n+t+1)
// steps, since every backtrack lets the last star take one more unit; and
// a trailing `**` checks each remaining segment once per state.
func stepBound(g *glob, p string) int {
	parts := strings.Split(p, "/")
	total := 0
	for _, alt := range g.alts {
		states := (len(alt.segs) + 1) * (len(parts) + 1)
		b := states + states*len(parts)
		for _, seg := range alt.segs {
			for _, part := range parts {
				n := len(utf16.Encode([]rune(part)))
				b += (n + 1) * (n + len(seg.toks) + 1)
			}
		}
		total += b
	}
	return total
}

// Matching is polynomial: patterns that make a backtracking matcher
// revisit suffixes exponentially stay within stepBound.
func TestGlobMatchingIsBounded(t *testing.T) {
	for _, c := range []struct{ pattern, path string }{
		// First, so that a matcher that revisits `**` states fails here on
		// the count, quickly, before the larger cases below.
		{strings.Repeat("**/", 5) + "z", strings.Repeat("a/", 40) + "b"},
		{"f/" + strings.Repeat("*a", 30) + "b.ts", "f/" + strings.Repeat("a", 30) + ".ts"},
		{"f/" + strings.Repeat("*a", 200) + "b", "f/" + strings.Repeat("a", 400)},
		{"f/" + strings.Repeat("?*", 50) + "x", "f/" + strings.Repeat("y", 300)},
		{strings.Repeat("**/a*/", 12) + "b.ts", strings.Repeat("a/", 40) + "x.ts"},
		{strings.Repeat("**/", 30) + "z", strings.Repeat("a/", 200) + "b"},
		{"**/" + strings.Repeat("*a*/**/", 10) + "c", strings.Repeat("ba/", 60) + "d"},
	} {
		g, err := compileGlob(c.pattern)
		if err != nil {
			t.Fatal(err)
		}
		hit, steps := g.matchSteps(c.path)
		if hit {
			t.Errorf("%.40q matches %.40q", c.pattern, c.path)
		}
		if bound := stepBound(g, c.path); steps > bound {
			t.Fatalf("%.40q against %.40q took %d steps, more than %d", c.pattern, c.path, steps, bound)
		}
	}
}

// Sequence ends and steps at the integer limits expand as minimatch does,
// or are refused, and never run past the expansion bound.
func TestSequenceLimits(t *testing.T) {
	for _, c := range []struct {
		body string
		want []string
	}{
		{"a..z..-9223372036854775808", []string{"a"}},
		{"a..z..9223372036854775807", []string{"a"}},
		{"z..a..-9223372036854775808", []string{"z"}},
		{"1..3..-9223372036854775808", []string{"1"}},
		{"3..1..9223372036854775807", []string{"3"}},
		{"1..5..99999999999999999999", []string{"1"}},
		{"9007199254740991..9007199254740992", []string{"9007199254740991", "9007199254740992"}},
		{"-9007199254740992..-9007199254740991", []string{"-9007199254740992", "-9007199254740991"}},
		{"9007199254740992..-9007199254740992..9007199254740992", []string{"9007199254740992", "0", "-9007199254740992"}},
		{"a..c..-2", []string{"a", "c"}},
		{"Z..a", []string{"Z", "[", "]", "^", "_", "`", "a"}},
	} {
		got, ok, err := sequence(c.body)
		if err != nil || !ok || !slices.Equal(got, c.want) {
			t.Errorf("{%s} = %q, %v, %v; want %q", c.body, got, ok, err, c.want)
		}
	}
	for _, body := range []string{
		"1..9007199254740993",
		"-9223372036854775808..0",
		"-9007199254740992..9007199254740992..9007199254740993",
		"1..4097",
		"1..18446744073709551615",
	} {
		if _, _, err := sequence(body); err == nil {
			t.Errorf("{%s} expanded; want a refusal", body)
		}
	}
	if got, _, err := sequence("1..4096"); err != nil || len(got) != 4096 {
		t.Errorf("{1..4096}: %d items, %v", len(got), err)
	}
}

// refMatch is a plain backtracking matcher with the same semantics as
// glob.match, for short inputs only.
func refMatch(g *glob, p string) bool {
	if g.comment {
		return false
	}
	parts := strings.Split(p, "/")
	hit := false
	for _, alt := range g.alts {
		if !alt.never && refSegs(alt.segs, parts) {
			hit = true
			break
		}
	}
	return hit != g.negate
}

func refSegs(segs []globSeg, parts []string) bool {
	if len(segs) == 0 {
		return len(parts) == 0
	}
	if !segs[0].globstar {
		if len(parts) == 0 {
			return false
		}
		if (parts[0] == "." || parts[0] == "..") && !allLiteral(segs[0].toks) ||
			strings.HasPrefix(parts[0], ".") && !leadingDotAllowed(segs[0].toks) {
			return false
		}
		return refToks(segs[0].toks, utf16.Encode([]rune(parts[0]))) && refSegs(segs[1:], parts[1:])
	}
	if len(segs) == 1 {
		return len(parts) > 0 && !slices.ContainsFunc(parts, func(s string) bool { return strings.HasPrefix(s, ".") })
	}
	for i := 0; i <= len(parts); i++ {
		if refSegs(segs[1:], parts[i:]) {
			return true
		}
		if i < len(parts) && strings.HasPrefix(parts[i], ".") {
			return false
		}
	}
	return false
}

func refToks(toks []globTok, s []uint16) bool {
	if len(toks) == 0 {
		return len(s) == 0
	}
	switch t := toks[0]; t.kind {
	case tokStar:
		for i := 0; i <= len(s); i++ {
			if refToks(toks[1:], s[i:]) {
				return true
			}
		}
		return false
	case tokAny:
		return len(s) > 0 && refToks(toks[1:], s[1:])
	case tokLit:
		return len(s) > 0 && s[0] == t.u && refToks(toks[1:], s[1:])
	default:
		return len(s) > 0 && t.class.matches(s[0]) && refToks(toks[1:], s[1:])
	}
}

// FuzzGlob checks that compiling and matching never panic, that a negated
// pattern matches exactly what the pattern does not, and that on short
// inputs the bounded matcher agrees with a plain backtracking one.
func FuzzGlob(f *testing.F) {
	for _, s := range []string{"f/**", "f/{a,{b,c}}/[!x]?*.ts", "f/{01..10..3}", "\\[a", "[]a]", "**/.a/**", "{a..z}", "*a*a*b", "**/a*/**/b", "f/[\U0001F600]?", "f/??"} {
		f.Add(s, "f/a/x.ts")
		f.Add(s, "f/\U0001F600/aab")
	}
	f.Fuzz(func(t *testing.T, pattern, p string) {
		g, err := compileGlob(pattern)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) || e.Kind != ErrGlob {
				t.Fatalf("%q: %v", pattern, err)
			}
			return
		}
		got := g.match(p)
		if len(pattern) <= 24 && len(p) <= 24 && len(g.alts) <= 16 {
			if want := refMatch(g, p); got != want {
				t.Fatalf("%q against %q: %v, the reference says %v", pattern, p, got, want)
			}
		}
		if strings.HasPrefix(pattern, "!") || strings.HasPrefix(pattern, "#") {
			return
		}
		n, err := compileGlob("!" + pattern)
		if err != nil {
			t.Fatalf("%q compiles but its negation does not: %v", pattern, err)
		}
		if n.match(p) == got {
			t.Fatalf("%q and its negation agree on %q", pattern, p)
		}
	})
}

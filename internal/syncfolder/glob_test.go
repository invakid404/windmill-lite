package syncfolder

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
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

// FuzzGlob checks that compiling and matching never panic, and that a
// negated pattern matches exactly what the pattern does not.
func FuzzGlob(f *testing.F) {
	for _, s := range []string{"f/**", "f/{a,{b,c}}/[!x]?*.ts", "f/{01..10..3}", "\\[a", "[]a]", "**/.a/**", "{a..z}"} {
		f.Add(s, "f/a/x.ts")
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

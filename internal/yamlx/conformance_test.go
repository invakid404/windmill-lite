package yamlx_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// The cases of testdata/sy9.json; its "about" field describes the format.
type caseFile struct {
	Finding string    `json:"finding"`
	About   []string  `json:"about"`
	Cases   []sy9Case `json:"cases"`
}

type sy9Case struct {
	Name       string     `json:"name"`
	Spec       string     `json:"spec"`
	Rule       string     `json:"rule"`
	Provenance string     `json:"provenance"`
	YAML       string     `json:"yaml"`
	JSON       *string    `json:"json"`
	Error      string     `json:"error"`
	CLI        *cliResult `json:"cli"`
}

// cliResult is what the CLI's parser returns for a policy case.
type cliResult struct {
	JSON  *string `json:"json"`
	Error string  `json:"error"`
}

func loadCases(t testing.TB) []sy9Case {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sy9.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f caseFile
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("testdata/sy9.json: %v", err)
	}
	if f.Finding != "SY-9" {
		t.Fatalf("testdata/sy9.json: finding %q, want SY-9", f.Finding)
	}
	names := map[string]bool{}
	for _, c := range f.Cases {
		switch {
		case names[c.Name]:
			t.Fatalf("case %q: duplicate name", c.Name)
		case c.Spec != "SY-9" && !strings.HasPrefix(c.Spec, "SY-9/"):
			t.Fatalf("case %q: spec %q is not SY-9 or one of its cases", c.Name, c.Spec)
		case (c.JSON == nil) == (c.Error == ""):
			t.Fatalf("case %q: needs exactly one of json and error", c.Name)
		case c.Provenance == "policy" && (c.Error == "" || c.CLI == nil):
			t.Fatalf("case %q: a policy case needs error and cli", c.Name)
		case c.Provenance == "source-derived" && c.CLI != nil:
			t.Fatalf("case %q: cli belongs to policy cases", c.Name)
		case c.Provenance != "policy" && c.Provenance != "source-derived":
			t.Fatalf("case %q: provenance %q", c.Name, c.Provenance)
		}
		names[c.Name] = true
	}
	return f.Cases
}

// spec: SY-9/a, SY-9/b, SY-9/c, and the SY-9 rules the other cases cite.
func TestSY9Cases(t *testing.T) {
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			got, err := yamlx.ToJSON([]byte(c.YAML))
			if c.JSON != nil {
				if err != nil {
					t.Fatalf("%s: %q: unexpected error: %v", c.Spec, c.YAML, err)
				}
				if string(got) != *c.JSON {
					t.Fatalf("%s: %q\n got %s\nwant %s", c.Spec, c.YAML, got, *c.JSON)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s: %q: got %s, want an error containing %q", c.Spec, c.YAML, got, c.Error)
			}
			var e *yamlx.Error
			if !errors.As(err, &e) {
				t.Fatalf("%s: error %v is a %T, not a *yamlx.Error", c.Spec, err, err)
			}
			if !strings.Contains(err.Error(), c.Error) {
				t.Fatalf("%s: %q\n got error %q\nwant one containing %q", c.Spec, c.YAML, err, c.Error)
			}
		})
	}
}

// The catalogue's SY-9 cases are the milestone's exit gate; this keeps
// them in the case file.
func TestSY9CatalogueCovered(t *testing.T) {
	got := map[string]int{}
	for _, c := range loadCases(t) {
		got[c.Spec]++
	}
	for spec, want := range map[string]int{"SY-9/a": 6, "SY-9/b": 1, "SY-9/c": 1} {
		if got[spec] != want {
			t.Errorf("%s: %d cases, want %d (the catalogue's rows)", spec, got[spec], want)
		}
	}
}

// crossCheckScript runs npm yaml as the CLI uses it: YAML.parse with the
// two scalar tags of SY-9, then JSON.stringify. It prints one JSON line per
// input. Arguments: the package directory and a JSON file of inputs.
const crossCheckScript = `
const YAML = require(process.argv[1]);
const inputs = JSON.parse(require('fs').readFileSync(process.argv[2], 'utf8'));
const customTags = [
  { tag: '!inline', resolve: (v) => '!inline ' + v },
  { tag: '!inline_fileset', resolve: (v) => '!inline_fileset ' + v },
];
process.emitWarning = () => {};
console.warn = () => {};
for (const src of inputs) {
  let r;
  try {
    r = { json: JSON.stringify(YAML.parse(src, { customTags })) };
  } catch (e) {
    r = { error: String(e.message) };
  }
  process.stdout.write(JSON.stringify(r) + '\n');
}
`

// TestSY9CrossCheck checks every expectation of the case file against the
// CLI's YAML library. It runs only when YAMLX_NPM_YAML names an unpacked
// npm yaml package (npm pack yaml@2.8.3) and node is on PATH.
func TestSY9CrossCheck(t *testing.T) {
	pkg := os.Getenv("YAMLX_NPM_YAML")
	if pkg == "" {
		t.Skip("set YAMLX_NPM_YAML to an unpacked npm yaml 2.8.3 package to cross-check the cases")
	}
	var meta struct{ Version string }
	data, err := os.ReadFile(filepath.Join(pkg, "package.json"))
	if err == nil {
		err = json.Unmarshal(data, &meta)
	}
	if err != nil || meta.Version != "2.8.3" {
		t.Fatalf("YAMLX_NPM_YAML=%s: want npm yaml 2.8.3, got version %q (%v)", pkg, meta.Version, err)
	}
	cases := loadCases(t)
	inputs := make([]string, len(cases))
	for i, c := range cases {
		inputs[i] = c.YAML
	}
	in := filepath.Join(t.TempDir(), "inputs.json")
	data, err = json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", "-e", crossCheckScript, pkg, in).Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	// Split on LF only: results may hold U+2028, a line break to some
	// splitters.
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("node printed %d results for %d cases", len(lines), len(cases))
	}
	for i, c := range cases {
		var r cliResult
		if err := json.Unmarshal([]byte(lines[i]), &r); err != nil {
			t.Fatalf("case %q: %v", c.Name, err)
		}
		want := cliResult{JSON: c.JSON}
		if c.CLI != nil {
			want = *c.CLI
		} else if c.Error != "" {
			want = cliResult{Error: "any"}
		}
		switch {
		case want.JSON != nil && (r.JSON == nil || *r.JSON != *want.JSON):
			t.Errorf("case %q: npm yaml gives %s, the case expects %s", c.Name, lines[i], *want.JSON)
		case want.Error != "" && r.Error == "":
			t.Errorf("case %q: npm yaml gives %s, the case expects an error", c.Name, lines[i])
		case want.Error != "" && want.Error != "any" && !strings.Contains(r.Error, want.Error):
			t.Errorf("case %q: npm yaml fails with %q, the case records %q", c.Name, r.Error, want.Error)
		}
	}
}

package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExprFile(t *testing.T, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ExprDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "je-x.yaml")
	if strings.Contains(body, "finding: JE-Y") {
		p = filepath.Join(dir, "je-x-misnamed.yaml")
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestExprCaseRefusesContext: a case that names a predecessor, a handler or
// a container, by a key or by its site, is a flow fixture (ADR 0003).
func TestExprCaseRefusesContext(t *testing.T) {
	for name, c := range map[string]string{
		"previous_result key": "previous_result: '{\"n\":1}'\n    expr: previous_result.n",
		"results key":         "results: {a: '1'}\n    expr: results.a",
		"failure_module key":  "failure_module: {}\n    expr: error.message",
		"iterator site":       "site: iterator\n    expr: '[1]'",
		"stop_after_if site":  "site: stop_after_if\n    expr: 'true'",
	} {
		t.Run(name, func(t *testing.T) {
			p := writeExprFile(t, "finding: JE-X\nscope: core\ncases:\n  - spec: JE-X/a\n    provenance: policy\n    title: t\n    "+c+"\n    expect: {value: '1'}\n")
			_, err := LoadExprFile(p)
			var ce *ExprCaseError
			if !errors.As(err, &ce) || ce.Kind != ErrContextualCase {
				t.Fatalf("got %v, want %s", err, ErrContextualCase)
			}
		})
	}
}

// TestExprCaseChecks refuses malformed cases.
func TestExprCaseChecks(t *testing.T) {
	head := "finding: JE-X\nscope: core\ncases:\n"
	for name, c := range map[string]string{
		"two expectations":        "  - {spec: JE-X/a, provenance: policy, title: t, expr: '1', expect: {value: '1', skipped: true}}\n",
		"no expectation":          "  - {spec: JE-X/a, provenance: policy, title: t, expr: '1', expect: {}}\n",
		"text without value":      "  - {spec: JE-X/a, provenance: policy, title: t, expr: '1', expect: {text: '1', error: {name: E}}}\n",
		"skipped on a transform":  "  - {spec: JE-X/a, provenance: policy, title: t, expr: '1', expect: {skipped: true}}\n",
		"static with expr":        "  - {spec: JE-X/a, provenance: policy, title: t, site: static, static: '1', expr: '1', expect: {value: '1'}}\n",
		"value not JSON":          "  - {spec: JE-X/a, provenance: policy, title: t, expr: '1', expect: {value: 'nope'}}\n",
		"finding without variant": "  - {spec: JE-X, provenance: policy, title: t, expr: '1', expect: {value: '1'}}\n",
		"input not an object":     "  - {spec: JE-X/a, provenance: policy, title: t, expr: '1', flow_input: '[1]', expect: {value: '1'}}\n",
		"case given twice":        "  - {spec: JE-X/a, provenance: policy, title: t, expr: '1', expect: {value: '1'}}\n  - {spec: JE-X/a, provenance: policy, title: u, expr: '2', expect: {value: '2'}}\n",
		"bad status":              "  - {spec: JE-X/a, provenance: policy, title: t, expr: '1', expect: {status: latched, value: '1'}}\n",
		"unknown key":             "  - {spec: JE-X/a, provenance: policy, title: t, expr: '1', expect: {value: '1'}, extra: 1}\n",
		"value on skip_if":        "  - {spec: JE-X/a, provenance: policy, title: t, site: skip_if, expr: 'true', expect: {value: 'null'}}\n",
		"error on skip_if":        "  - {spec: JE-X/a, provenance: policy, title: t, site: skip_if, expr: nope, expect: {error: {name: E}}}\n",
		"flow_error on transform": "  - {spec: JE-X/a, provenance: policy, title: t, expr: nope, expect: {flow_error: {name: E}}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadExprFile(writeExprFile(t, head+c)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// TestExprFileNamedAfterFinding: recordings are named after the file, so
// the file must be named after its finding.
func TestExprFileNamedAfterFinding(t *testing.T) {
	p := writeExprFile(t, "finding: JE-Y\nscope: core\ncases:\n  - {spec: JE-Y/a, provenance: policy, title: t, expr: '1', expect: {value: '1'}}\n")
	if _, err := LoadExprFile(p); err == nil || !strings.Contains(err.Error(), "je-y.yaml") {
		t.Fatalf("got %v", err)
	}
}

// TestLiftExprFile lifts a file into a generated fixture: one flow per
// case, the sink, runs named after the cases with their claims, and
// recordings under exprs/recorded/<file>/.
func TestLiftExprFile(t *testing.T) {
	p := writeExprFile(t, `finding: JE-X
scope: core
flow_input: '{"n":5}'
mask: [/result/error/message]
cases:
  - {spec: JE-X/a, provenance: policy, title: value, expr: flow_input.n, expect: {value: '5'}}
  - {spec: JE-X, variant: skip-case, provenance: policy, title: skip, site: skip_if, expr: 'true', expect: {skipped: true}}
  - {spec: JE-X/b, provenance: policy, title: static, site: static, static: '"${x}"', flow_input: '{}', expect: {error: {name: E, prefix: p}}}
`)
	f, err := LoadExprFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(p), RecordedDir, "je-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := `{"observation":{"status":"success","result":null},"provenance":{}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(p), RecordedDir, "je-x", "je-x-a.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	gen := filepath.Join(t.TempDir(), "gen")
	fx, err := LiftExprFile(f, gen)
	if err != nil {
		t.Fatal(err)
	}
	if !fx.Generated || fx.SyncTree() != filepath.Join(gen, SyncTreeDir) || len(fx.Compare.Mask) != 1 {
		t.Fatalf("fixture %+v", fx)
	}
	var names []string
	for _, r := range fx.Runs {
		names = append(names, r.Name+"="+r.Target+" "+string(r.ArgsOrEmpty()))
	}
	if got := strings.Join(names, "; "); got != `je-x-a=f/wl_expr/je_x_a {"n":5}; je-x-b=f/wl_expr/je_x_b {}; je-x-skip-case=f/wl_expr/je_x_skip_case {"n":5}` {
		t.Fatalf("runs %s", got)
	}
	if fx.Runs[0].Recorded == nil || fx.Runs[1].Recorded != nil {
		t.Fatal("recordings were not read from exprs/recorded/je-x/")
	}
	for _, name := range []string{"wmill.yaml", "f/wl_expr/sink.ts", "f/wl_expr/je_x_a.flow/flow.yaml", "f/wl_expr/je_x_skip_case.flow/flow.yaml"} {
		if _, err := os.Stat(filepath.Join(fx.SyncTree(), name)); err != nil {
			t.Errorf("tree lacks %s", name)
		}
	}
	data, err := os.ReadFile(filepath.Join(fx.SyncTree(), "f/wl_expr/je_x_skip_case.flow/flow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Value struct {
			Modules []struct {
				ID     string          `json:"id"`
				SkipIf json.RawMessage `json:"skip_if"`
				Value  struct {
					Path            string                     `json:"path"`
					InputTransforms map[string]json.RawMessage `json:"input_transforms"`
				} `json:"value"`
			} `json:"modules"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	m := doc.Value.Modules[0]
	compact := func(raw json.RawMessage) string {
		var b bytes.Buffer
		_ = json.Compact(&b, raw)
		return b.String()
	}
	if m.ID != ExprStep || m.Value.Path != ExprSink || compact(m.SkipIf) != `{"expr":"true"}` || compact(m.Value.InputTransforms[ExprInput]) != `{"type":"static","value":null}` {
		t.Fatalf("skip_if flow %s", data)
	}
	claims := map[string]string{}
	for _, r := range fx.Runs {
		b, _ := json.Marshal(r.Claims)
		claims[r.Name] = string(b)
	}
	for name, want := range map[string]string{
		"je-x-a":         `[{"at":"/status","equals":"success"},{"at":"/steps/m:b/args/x","equals":5}]`,
		"je-x-skip-case": `[{"at":"/status","equals":"success"},{"at":"/steps/m:b/skipped","equals":true}]`,
		"je-x-b":         `[{"at":"/status","equals":"failure"},{"at":"/steps/m:b/status","equals":"failure"},{"at":"/steps/m:b/result/error/name","equals":"E"},{"at":"/steps/m:b/result/error/message","prefix":"p"}]`,
	} {
		if claims[name] != want {
			t.Errorf("%s claims %s, want %s", name, claims[name], want)
		}
	}
	if _, err := LiftExprFile(f, gen); err == nil {
		t.Error("lifted into an existing directory")
	}
}

// TestFindExprFiles finds .yaml files in exprs directories once, however
// many roots reach them, and not deferred.yaml, files elsewhere, or a
// folder named exprs inside a fixture's sync tree.
func TestFindExprFiles(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"exprs/je-1.yaml", "exprs/deferred.yaml", "exprs/recorded/je-1/je-1-a.json", "flows/x/runs/a.yaml", "exprs/notes.md",
		"flows/y/fixture.yaml", "flows/y/windmill/f/exprs/x.script.yaml"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FindExprFiles(root, filepath.Join(root, "exprs"), filepath.Join(root, "exprs", "je-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.ToSlash(got[0]) != filepath.ToSlash(filepath.Join(root, "exprs/je-1.yaml")) {
		t.Fatalf("found %q", got)
	}
}

// TestCheckClaims checks each kind of claim on one observation, in its
// canonical encoding.
func TestCheckClaims(t *testing.T) {
	var o Observation
	if err := json.Unmarshal([]byte(`{"status":"failure","result":{"error":{"name":"E","message":"Error during x: boom"}},
		"steps":{"m:a":{"status":"success","args":{"x":{"b":1,"a":2}},"result":[1,2,3]},"m:l/i:0/m:s":{"status":"success","args":{"x":12345678901234567890},"result":null}}}`), &o); err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }
	n := func(i int) *int { return &i }
	for _, c := range []struct {
		claim Claim
		holds bool
	}{
		{Claim{At: "/status", Equals: json.RawMessage(`"failure"`)}, true},
		{Claim{At: "/steps/m:a/args/x", Equals: json.RawMessage(`{"a":2,"b":1}`)}, true},
		{Claim{At: "/steps/m:a/args/x", Equals: json.RawMessage(`{"a":2,"b":1}`), KeyOrder: "strict"}, false},
		{Claim{At: "/steps/m:a/args/x", Equals: json.RawMessage(`{"b":1,"a":2}`), KeyOrder: "strict"}, true},
		{Claim{At: "/steps/m:l~1i:0~1m:s/args/x", Equals: json.RawMessage(`12345678901234567890`)}, true},
		{Claim{At: "/steps/m:l~1i:0~1m:s/args/x", Equals: json.RawMessage(`12345678901234567000`)}, false},
		{Claim{At: "/result/error/message", Prefix: str("Error during"), Contains: []string{"boom"}}, true},
		{Claim{At: "/result/error/message", Prefix: str("boom")}, false},
		{Claim{At: "/result/error/message", NotContains: []string{"boom"}}, false},
		{Claim{At: "/result/error", Contains: []string{"boom"}}, false},
		{Claim{At: "/steps/m:a/result", Length: n(3)}, true},
		{Claim{At: "/steps/m:a/result", Length: n(2)}, false},
		{Claim{At: "/steps/m:a/skipped", Absent: true}, true},
		{Claim{At: "/steps/m:a/ran", Absent: true}, true},
		{Claim{At: "/steps/m:b", Absent: true}, true},
		{Claim{At: "/steps/m:a", Absent: true}, false},
		{Claim{At: "/steps/m:b/status", Equals: json.RawMessage(`"success"`)}, false},
	} {
		failed, err := CheckClaims(o, []Claim{c.claim})
		if err != nil {
			t.Fatalf("%+v: %v", c.claim, err)
		}
		if (len(failed) == 0) != c.holds {
			t.Errorf("%+v: holds %v, want %v (%q)", c.claim, len(failed) == 0, c.holds, failed)
		}
	}
	for _, bad := range []Claim{
		{At: "status"},
		{At: "/status"},
		{At: "/status", Equals: json.RawMessage(`1`), Absent: true},
		{At: "/status", Prefix: str("x"), KeyOrder: "strict"},
		{At: "/status", Equals: json.RawMessage(`{`)},
	} {
		if _, err := CheckClaims(o, []Claim{bad}); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}

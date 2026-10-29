package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const flowsDir = "../../conformance/flows"

// TestLoadFixtures loads every flow fixture of the suite: each must decode
// strictly, cite spec IDs and have runs.
func TestLoadFixtures(t *testing.T) {
	dirs, err := FindFixtures(flowsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no flow fixtures under conformance/flows")
	}
	for _, d := range dirs {
		f, err := LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range f.Runs {
			if r.Expect == nil && r.Recorded == nil {
				t.Errorf("%s run %s has neither expect nor a recording", d, r.Name)
			}
		}
	}
}

// TestSmokeFixture checks the OR-20 fixture as loaded.
func TestSmokeFixture(t *testing.T) {
	f, err := LoadFixture(filepath.Join(flowsDir, "oracle", "smoke"))
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != "oracle/smoke" || f.Scope != "core" || f.Oracle.NeedWorkers() != 1 || len(f.Runs) != 2 {
		t.Fatalf("fixture %+v", f)
	}
	fail, ok := f.Runs[0], f.Runs[0].Name == "fail"
	if !ok || fail.Target != "f/wl_smoke/two_step" || string(fail.Args) != `{"x":60}` || fail.IsScript() {
		t.Fatalf("run fail: %+v", fail)
	}
	b := fail.Expect.Steps[SitePath{}.Child(Module("b"))]
	if b.Status != StepFailure || string(b.Args) != `{"n":120,"tag":"hi"}` {
		t.Fatalf("expected step b: %+v", b)
	}
	if got := string(fail.Expect.Result); got != `{"error":{"name":"Error","message":"too big: 120","stack":"<stack>","step_id":"b"}}` {
		t.Fatalf("expected result %s", got)
	}
}

func TestYAMLToJSON(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a: 1\nz: 2.50\nb: [x, 'y', true, null, ~]\n", `{"a":1,"z":2.50,"b":["x","y",true,null,null]}`},
		{"{zz: 1, a: 2, mmm: 3}", `{"zz":1,"a":2,"mmm":3}`},
		{`{"k": "<stack>", "q": "a\"b"}`, `{"k":"<stack>","q":"a\"b"}`},
		{"content: '!inline b.ts'\n", `{"content":"!inline b.ts"}`},
		{"2: x\n", `{"2":"x"}`},
		{"", `null`},
		{"x: -0.5e3\n", `{"x":-0.5e3}`},
	} {
		got, err := YAMLToJSON([]byte(c.in))
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%q: got %s, want %s", c.in, got, c.want)
		}
	}
	for _, in := range []string{
		"a: 0x1F\n",
		"a: 1_000\n",
		"a: .inf\n",
		"a: &x 1\nb: *x\n",
		"a: 1\na: 2\n",
		"? [a]\n: 1\n",
		"base: &b {x: 1}\nc:\n  <<: *b\n",
	} {
		if got, err := YAMLToJSON([]byte(in)); err == nil {
			t.Errorf("%q accepted as %s", in, got)
		}
	}
}

func TestLoadFixtureRejects(t *testing.T) {
	write := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	good := map[string]string{
		"fixture.yaml":        "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\n",
		"windmill/wmill.yaml": "defaultTs: bun\n",
		"runs/r.yaml":         "target: f/x/y\nargs: {a: 1}\n",
	}
	if _, err := LoadFixture(write(t, good)); err != nil {
		t.Fatalf("good fixture: %v", err)
	}
	for _, d := range []string{"cli", "api"} {
		files := map[string]string{}
		for k, v := range good {
			files[k] = v
		}
		files["fixture.yaml"] = "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\noracle: {deploy: " + d + "}\n"
		f, err := LoadFixture(write(t, files))
		if err != nil {
			t.Fatalf("deploy %s: %v", d, err)
		}
		if f.Oracle.DeploysThroughAPI() != (d == "api") {
			t.Errorf("deploy %s: DeploysThroughAPI is %v", d, f.Oracle.DeploysThroughAPI())
		}
	}
	for name, edit := range map[string][2]string{
		"unknown fixture key": {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\ncolour: red\n"},
		"no spec":             {"fixture.yaml", "id: x\nscope: core\nprovenance: policy\n"},
		"bad scope":           {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: soon\nprovenance: policy\n"},
		"three workers":       {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\noracle: {workers: 3}\n"},
		"unknown deploy":      {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\noracle: {deploy: rest}\n"},
		"bad pointer":         {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\ncompare: {mask: [steps/a]}\n"},
		"subset and strict":   {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\ncompare: {mode: subset, key_order: strict}\n"},
		"args not an object":  {"runs/r.yaml", "target: f/x/y\nargs: [1]\n"},
		"no target":           {"runs/r.yaml", "args: {}\n"},
		"bad expect status":   {"runs/r.yaml", "target: f/x/y\nexpect: {status: fine, result: 1}\n"},
		"bad expect site":     {"runs/r.yaml", "target: f/x/y\nexpect: {status: success, result: 1, steps: {a: {status: success}}}\n"},
	} {
		files := map[string]string{}
		for k, v := range good {
			files[k] = v
		}
		files[edit[0]] = edit[1]
		if _, err := LoadFixture(write(t, files)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestFixtureOptionChecks loads fixtures that combine the E3e options
// (oracle.setup, oracle.pull, a run's submit) and requires each invalid
// combination to be refused with its reason.
func TestFixtureOptionChecks(t *testing.T) {
	const setup = "variables:\n  - path: f/x/v\n    value: v\n"
	for _, c := range []struct {
		name, oracle, setup, run, err string
	}{
		{name: "defaults", oracle: "{}", run: "target: f/x/y\n"},
		{name: "before push", oracle: "{setup: before_push}", setup: setup, run: "target: f/x/y\n"},
		{name: "each run", oracle: "{setup: each_run}", setup: setup, run: "target: f/x/y\n"},
		{name: "each run without variables", oracle: "{setup: each_run}", run: "target: f/x/y\n", err: "needs variables"},
		{name: "unknown policy", oracle: "{setup: sometimes}", setup: setup, run: "target: f/x/y\n", err: "is not after_push"},
		{name: "before push through the API", oracle: "{deploy: api, setup: before_push}", setup: setup, run: "target: f/x/y\n", err: "only the CLI's push"},
		{name: "pull", oracle: "{deploy: api, pull: true}", run: "target: f/x/y\n"},
		{name: "pull through the CLI", oracle: "{pull: true}", run: "target: f/x/y\n", err: "oracle.pull needs oracle.deploy api"},
		{name: "run and wait", oracle: "{}", run: "target: f/x/y\nsubmit: run_wait_result\n"},
		{name: "run and wait a script", oracle: "{}", run: "target: f/x/y\nkind: script\nsubmit: run_wait_result\n", err: "runs a flow alone"},
		{name: "run and wait in a group", oracle: "{}", run: "target: f/x/y\ngroup: g\nsubmit: run_wait_result\n", err: "runs a flow alone"},
		{name: "run and wait canceled", oracle: "{}", run: "target: f/x/y\ncancel_after_ms: 5\nsubmit: run_wait_result\n", err: "runs a flow alone"},
		{name: "unknown submit", oracle: "{}", run: "target: f/x/y\nsubmit: later\n", err: "is not run_wait_result"},
		{name: "not-found locations", oracle: "{}", run: "target: f/x/y\nnot_found_locs: [/result, /steps/m:e/result]\n"},
		{name: "a not-found location that is no pointer", oracle: "{}", run: "target: f/x/y\nnot_found_locs: [result]\n", err: "does not start with /"},
		{name: "copies", oracle: "{}", run: "target: f/x/y\ncopies: [{at: /result, of: \"m:a\"}, {at: /steps/s:failure/args/e, of: \"m:l/i:0/m:b\"}]\n"},
		{name: "a copy that is no pointer", oracle: "{}", run: "target: f/x/y\ncopies: [{at: result, of: \"m:a\"}]\n", err: "does not start with /"},
		{name: "a copy of no site", oracle: "{}", run: "target: f/x/y\ncopies: [{at: /result, of: \"\"}]\n", err: "is not a site path"},
		{name: "each run in a group", oracle: "{setup: each_run}", setup: setup, run: "target: f/x/y\ngroup: g\n", err: "cannot be in group"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range map[string]string{
				FixtureFile:           "id: x\nspec: [OR-15]\nscope: core\nprovenance: source-derived\noracle: " + c.oracle + "\n",
				"runs/r.yaml":         c.run,
				"windmill/wmill.yaml": "includes: [\"f/**\"]\n",
			} {
				p := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if c.setup != "" {
				if err := os.WriteFile(filepath.Join(dir, SetupFile), []byte(c.setup), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			f, err := LoadFixture(dir)
			switch {
			case c.err == "" && err != nil:
				t.Fatal(err)
			case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
				t.Fatalf("got %v, want an error with %q", err, c.err)
			case c.err == "" && c.run == "target: f/x/y\nsubmit: run_wait_result\n" && !f.Runs[0].RunsAndWaits():
				t.Fatal("the run does not run and wait")
			}
		})
	}
}

// A fixture's provided sites and providers (F2a): each named provider
// gives exactly one of a result, an error object and a decline, and
// providers come with provided sites; the sites are read as the host
// contract writes them, which wltest check validates.
func TestFixtureProviders(t *testing.T) {
	const site = "provided:\n  - {selector: {flow: f/x/y, module: a}, provider: p, mandatory: true}\n"
	for _, c := range []struct {
		name, extra, err string
	}{
		{name: "a result", extra: site + "providers:\n  p: {result: {ok: true}}\n"},
		{name: "a null result", extra: site + "providers:\n  p: {result: null}\n"},
		{name: "an error", extra: site + "providers:\n  p: {error: {name: E, message: m}}\n"},
		{name: "a decline", extra: site + "providers:\n  p: {decline: true}\n"},
		{name: "a static site alone", extra: "provided:\n  - {selector: {flow: f/x/y, module: a}, static: 1, mandatory: true}\n"},
		{name: "nothing", extra: site + "providers:\n  p: {}\n", err: "exactly one of result, error and decline"},
		{name: "two", extra: site + "providers:\n  p: {result: 1, decline: true}\n", err: "exactly one of result, error and decline"},
		{name: "an error that is no object", extra: site + "providers:\n  p: {error: boom}\n", err: "must be an object"},
		{name: "providers without sites", extra: "providers:\n  p: {decline: true}\n", err: "providers without provided sites"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range map[string]string{
				FixtureFile:           "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\n" + c.extra,
				"runs/r.yaml":         "target: f/x/y\n",
				"windmill/wmill.yaml": "includes: [\"f/**\"]\n",
			} {
				p := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			f, err := LoadFixture(dir)
			switch {
			case c.err == "" && err != nil:
				t.Fatal(err)
			case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
				t.Fatalf("got %v, want an error with %q", err, c.err)
			case c.err == "" && len(f.Provided) != 1:
				t.Fatalf("provided %s", f.Provided)
			}
		})
	}
}

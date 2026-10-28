package conformance

import (
	"os"
	"path/filepath"
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
	for name, edit := range map[string][2]string{
		"unknown fixture key": {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\ncolour: red\n"},
		"no spec":             {"fixture.yaml", "id: x\nscope: core\nprovenance: policy\n"},
		"bad scope":           {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: soon\nprovenance: policy\n"},
		"three workers":       {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\noracle: {workers: 3}\n"},
		"bad pointer":         {"fixture.yaml", "id: x\nspec: [OR-15]\nscope: core\nprovenance: policy\ncompare: {mask: [steps/a]}\n"},
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

package engine

import (
	"context"
	"os"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// envs records each invocation's environment by script, and succeeds.
type envs struct {
	mu  sync.Mutex
	got map[string]map[string]string
	bnd map[string]Boundary
}

func (e *envs) Execute(_ context.Context, inv Invocation) (StepOutcome, error) {
	e.mu.Lock()
	e.got[inv.Script], e.bnd[inv.Script] = inv.Env, inv.Boundary
	e.mu.Unlock()
	inv.Report.Settled(SettleCompleted)
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte("null"))}, nil
}

// envsEngine opens an engine on a folder whose three path scripts, a Bun
// one (f/t/plain), a //native one (f/t/headed, bunnative) and a nativets
// one (f/t/nativets), have the envs entries given, and whose flow f/t/main
// runs them in that order as steps a, b and c.
func envsEngine(t *testing.T, entries []string, exec StepExecutor) *Engine {
	t.Helper()
	jsOnce.Do(func() { jsEng, jsErr = newTestJS() })
	if jsErr != nil {
		t.Fatal(jsErr)
	}
	meta := "summary: ''\ndescription: ''\nkind: script\nenvs:\n"
	for _, e := range entries {
		meta += "  - '" + e + "'\n"
	}
	meta += "schema:\n  type: object\n  properties: {}\n  required: []\n"
	fsys := fstest.MapFS{
		"wmill.yaml":               {Data: []byte("defaultTs: bun\n")},
		"f/t/plain.ts":             {Data: []byte("export async function main() { return null }\n")},
		"f/t/plain.script.yaml":    {Data: []byte(meta)},
		"f/t/headed.ts":            {Data: []byte("//native\nexport async function main() { return null }\n")},
		"f/t/headed.script.yaml":   {Data: []byte(meta)},
		"f/t/nativets.fetch.ts":    {Data: []byte("export async function main() { return null }\n")},
		"f/t/nativets.script.yaml": {Data: []byte(meta)},
		"f/t/main.flow/flow.yaml": {Data: []byte(`{"summary":"t","value":` +
			flowOf(mod("a", "plain", "", `"continue_on_error":true`), mod("b", "headed", "", `"continue_on_error":true`), mod("c", "nativets", "", "")) + `}`)},
	}
	folder, err := syncfolder.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	prog, err := ir.Compile(folder, nil, ir.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Program: prog, Bundle: "sha256:test", Exec: exec, JS: jsEng, Workspace: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// A path script's own envs reach a Bun step's environment under every
// variable the worker sets later, each entry split at its first "=", an
// empty name kept, a later entry winning (WR-22, WA-2); a native step's
// environment has none of them (WR-45).
func TestScriptEnvs(t *testing.T) {
	exec := &envs{got: map[string]map[string]string{}, bnd: map[string]Boundary{}}
	e := envsEngine(t, []string{"A=1", "B=x=y", "A=2", "EMPTY=", "=nameless",
		"WM_WORKSPACE=mine", "WM_TOKEN=mine", "PATH=/mine", "BASE_URL=http://mine", "BASE_INTERNAL_URL=http://internal"}, exec)
	if tr := runIt(t, e, "{}", nil); tr.Status != conformance.RunSuccess {
		t.Fatalf("status %s, result %s", tr.Status, tr.Result)
	}
	bun := exec.got["f/t/plain"]
	if exec.bnd["f/t/plain"] != BoundaryBun {
		t.Fatalf("f/t/plain runs at %q", exec.bnd["f/t/plain"])
	}
	for k, want := range map[string]string{"A": "2", "B": "x=y", "EMPTY": "", "": "nameless", "BASE_INTERNAL_URL": "http://internal", "WM_WORKSPACE": "ws", "WM_FLOW_STEP_ID": "a"} {
		if got, ok := bun[k]; !ok || got != want {
			t.Errorf("a Bun step's %q is %q (%v), want %q", k, got, ok, want)
		}
	}
	for _, k := range []string{"PATH", "BASE_URL"} {
		if v, ok := bun[k]; ok {
			t.Errorf("a Bun step's environment has %q = %q", k, v)
		}
	}
	// The job identity sets WM_TOKEN (PF1), and the script's entry never
	// replaces it.
	if bun["WM_TOKEN"] == "mine" {
		t.Error("a Bun step's WM_TOKEN is the script's envs entry")
	}
	for _, script := range []string{"f/t/headed", "f/t/nativets"} {
		env := exec.got[script]
		if exec.bnd[script] != BoundaryNative {
			t.Fatalf("%s runs at %q", script, exec.bnd[script])
		}
		for _, k := range []string{"A", "B", "EMPTY", "", "BASE_INTERNAL_URL"} {
			if v, ok := env[k]; ok {
				t.Errorf("native step %s has the script's env %q = %q", script, k, v)
			}
		}
		if env["WM_WORKSPACE"] != "ws" {
			t.Errorf("native step %s's WM_WORKSPACE is %q", script, env["WM_WORKSPACE"])
		}
	}
}

// WR-22/c: an envs entry with no "=" fails a Bun job, and a bunnative one,
// whose runtime ignores the entries, before it runs, with the worker's
// execution error quoting the first such entry; a nativets job never reads
// them and runs. The same holds for a script run by path.
func TestScriptEnvsWithoutEquals(t *testing.T) {
	exec := &envs{got: map[string]map[string]string{}, bnd: map[string]Boundary{}}
	e := envsEngine(t, []string{"A=1", "NOEQ", "B"}, exec)
	tr := runIt(t, e, "{}", nil)
	wantStatus(t, tr, conformance.RunSuccess, "null")
	for _, id := range []string{"a", "b"} {
		rec := step(t, tr, id)
		if rec.Status != conformance.StepFailure || !rec.Ran || rec.ResultOrigin != OriginEngine {
			t.Fatalf("%s %+v", id, rec)
		}
		sameJSON(t, id+"'s result", rec.Result, `{"error":{"message":"execution error:\nBad request: Invalid env var: NOEQ. Must be in the form of KEY=VALUE","name":"ExecutionErr","step_id":"`+id+`"}}`)
	}
	if _, ran := exec.got["f/t/plain"]; ran {
		t.Error("the Bun script ran")
	}
	if _, ran := exec.got["f/t/headed"]; ran {
		t.Error("the bunnative script ran")
	}
	if _, ran := exec.got["f/t/nativets"]; !ran {
		t.Error("the nativets script did not run")
	}

	res, err := e.RunScript(context.Background(), ScriptRequest{Path: "f/t/plain", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	if _, ran := exec.got["f/t/plain"]; !res.Failed || ran {
		t.Fatalf("the script run %+v, ran %v", res, ran)
	}
	sameJSON(t, "the script run's result", res.Result, `{"error":{"message":"execution error:\nBad request: Invalid env var: NOEQ. Must be in the form of KEY=VALUE","name":"ExecutionErr"}}`)
}

// The worker copies its own NODE_PATH into a Bun job's environment after
// the script's envs, and only when it has one (WR-22/b): a script's
// NODE_PATH entry reaches the step when the engine has none, and is
// skipped when it has one, which the harness's base environment passes on.
func TestScriptEnvNodePath(t *testing.T) {
	s := &ir.Script{Metadata: &ir.Metadata{Envs: []string{"NODE_PATH=/mine", "BUN_INSTALL_CACHE_DIR=/mine"}}}
	t.Run("the engine has none", func(t *testing.T) {
		t.Setenv("NODE_PATH", "")
		os.Unsetenv("NODE_PATH")
		env := map[string]string{}
		addScriptEnvs(env, s, BoundaryBun)
		if got, ok := env["NODE_PATH"]; !ok || got != "/mine" {
			t.Fatalf("NODE_PATH is %q (%v), want the script's", got, ok)
		}
		if v, ok := env["BUN_INSTALL_CACHE_DIR"]; ok {
			t.Fatalf("BUN_INSTALL_CACHE_DIR is the script's %q; the worker always sets it", v)
		}
	})
	t.Run("the engine has one", func(t *testing.T) {
		t.Setenv("NODE_PATH", "/engine")
		env := map[string]string{}
		addScriptEnvs(env, s, BoundaryBun)
		if v, ok := env["NODE_PATH"]; ok {
			t.Fatalf("NODE_PATH is the script's %q, over the engine's", v)
		}
	})
}

package engine

import (
	"context"
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

// A path script's own envs reach a Bun step's environment under every
// variable the worker sets later, a later entry winning (WR-22, WA-2); a
// native step's environment has none of them (WR-45).
func TestScriptEnvs(t *testing.T) {
	jsOnce.Do(func() { jsEng, jsErr = newTestJS() })
	if jsErr != nil {
		t.Fatal(jsErr)
	}
	meta := "summary: ''\ndescription: ''\nkind: script\nenvs:\n" +
		"  - 'A=1'\n  - 'B=x=y'\n  - 'A=2'\n  - 'EMPTY='\n  - 'NOEQUALS'\n  - '=nameless'\n" +
		"  - 'WM_WORKSPACE=mine'\n  - 'WM_TOKEN=mine'\n  - 'PATH=/mine'\n  - 'BASE_URL=http://mine'\n  - 'BASE_INTERNAL_URL=http://internal'\n" +
		"schema:\n  type: object\n  properties: {}\n  required: []\n"
	fsys := fstest.MapFS{
		"wmill.yaml":               {Data: []byte("defaultTs: bun\n")},
		"f/t/plain.ts":             {Data: []byte("export async function main() { return null }\n")},
		"f/t/plain.script.yaml":    {Data: []byte(meta)},
		"f/t/headed.ts":            {Data: []byte("//native\nexport async function main() { return null }\n")},
		"f/t/headed.script.yaml":   {Data: []byte(meta)},
		"f/t/nativets.fetch.ts":    {Data: []byte("export async function main() { return null }\n")},
		"f/t/nativets.script.yaml": {Data: []byte(meta)},
		"f/t/main.flow/flow.yaml": {Data: []byte(`{"summary":"t","value":` +
			flowOf(mod("a", "plain", "", ""), mod("b", "headed", "", ""), mod("c", "nativets", "", "")) + `}`)},
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
	exec := &envs{got: map[string]map[string]string{}, bnd: map[string]Boundary{}}
	e, err := New(Config{Program: prog, Bundle: "sha256:test", Exec: exec, JS: jsEng, Workspace: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	if tr := runIt(t, e, "{}", nil); tr.Status != conformance.RunSuccess {
		t.Fatalf("status %s, result %s", tr.Status, tr.Result)
	}
	bun := exec.got["f/t/plain"]
	if exec.bnd["f/t/plain"] != BoundaryBun {
		t.Fatalf("f/t/plain runs at %q", exec.bnd["f/t/plain"])
	}
	for k, want := range map[string]string{"A": "2", "B": "x=y", "EMPTY": "", "BASE_INTERNAL_URL": "http://internal", "WM_WORKSPACE": "ws", "WM_FLOW_STEP_ID": "a"} {
		if got, ok := bun[k]; !ok || got != want {
			t.Errorf("a Bun step's %s is %q (%v), want %q", k, got, ok, want)
		}
	}
	for _, k := range []string{"NOEQUALS", "", "WM_TOKEN", "PATH", "BASE_URL"} {
		if v, ok := bun[k]; ok {
			t.Errorf("a Bun step's environment has %q = %q", k, v)
		}
	}
	for _, script := range []string{"f/t/headed", "f/t/nativets"} {
		env := exec.got[script]
		if exec.bnd[script] != BoundaryNative {
			t.Fatalf("%s runs at %q", script, exec.bnd[script])
		}
		for _, k := range []string{"A", "B", "EMPTY", "BASE_INTERNAL_URL"} {
			if v, ok := env[k]; ok {
				t.Errorf("native step %s has the script's env %s = %q", script, k, v)
			}
		}
		if env["WM_WORKSPACE"] != "ws" {
			t.Errorf("native step %s's WM_WORKSPACE is %q", script, env["WM_WORKSPACE"])
		}
	}
}

package wl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// gateStep is a Bun step that waits for the file dir/<gate> and returns
// its process id, writing dir/<started> first when started is set.
func gateStep(id, started, gate, envelope string) string {
	body := `import { existsSync, writeFileSync } from \"node:fs\";\nexport async function main(dir: string) {\n`
	if started != "" {
		body += `  writeFileSync(dir + \"/` + started + `\", String(process.pid));\n`
	}
	body += `  while (!existsSync(dir + \"/` + gate + `\")) await new Promise((r) => setTimeout(r, 5));\n  return process.pid;\n}`
	if envelope != "" {
		envelope = "," + envelope
	}
	return `{"id":"` + id + `","value":{"type":"rawscript","language":"bun","content":"` + body +
		`","input_transforms":{"dir":{"type":"javascript","expr":"flow_input.dir"}}}` + envelope + `}`
}

func waitFile(t *testing.T, path string) []byte {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("no file %s", path)
		}
	}
}

func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// An unpinned run's calls carry its own routing choice, so its late calls
// never go by the pin of a pinned run that took its id. The unpinned run
// ends at its deadline with its step abandoned, which keeps it unsettled
// and taints its process, which unrelated work keeps alive. A pinned run
// then takes the id; its first step times out and taints its process,
// and it waits in its second step there. The earlier run's abandoned step
// reads a resource that the host plugin provides, and the earlier run
// settles: neither call goes to the pinned run's tainted process.
func TestUnpinnedRunLateCallsAfterIDReuse(t *testing.T) {
	bun := bunOrSkip(t)
	dir := t.TempDir()
	logf := filepath.Join(dir, "plugin.log")
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	src := `import { appendFileSync } from "node:fs";
const log = (l: string) => appendFileSync(` + jsonString(logf) + `, l + "\n");
export default {
  onRunSettled: (t: { run: string; status: string }) => { log("settled " + t.run + " " + t.status + " " + process.pid); },
  resource: () => ({ type: "object", value: { pid: process.pid } }),
};
`
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	held := `import { existsSync, writeFileSync } from \"node:fs\";\nexport async function main(dir: string) {\n` +
		`  while (!existsSync(dir + \"/go\")) await new Promise((r) => setTimeout(r, 5));\n` +
		`  const r = await fetch(process.env.BASE_URL + \"/api/w/\" + process.env.WM_WORKSPACE + \"/resources/get_value_interpolated/u/t/r\", { headers: { Authorization: \"Bearer \" + process.env.WM_TOKEN } });\n` +
		`  writeFileSync(dir + \"/res\", JSON.stringify(await r.json()));\n` +
		`  while (!existsSync(dir + \"/end\")) await new Promise((r) => setTimeout(r, 5));\n  return 1;\n}`
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml": "defaultTs: bun\n",
		"f/t/held.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun","content":"` + held +
			`","input_transforms":{"dir":{"type":"javascript","expr":"flow_input.dir"}}}}]}}`,
		"f/t/busy.flow/flow.yaml": `{"summary":"t","value":{"modules":[` + gateStep("w", "busy", "release", "") + `]}}`,
		"f/t/succ.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
			gateStep("a", "", "release-a", `"timeout":{"type":"static","value":1},"continue_on_error":true`) + `,` +
			gateStep("b", "succ-b", "b-go", "") + `]}}`,
	})
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: plugin, Pool: PoolOptions{Max: 3}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	dirArgs := json.RawMessage(`{"dir":` + jsonString(dir) + `}`)
	busy := make(chan error, 1)
	go func() { _, err := rt.Run(context.Background(), "f/t/busy", dirArgs, RunOptions{}); busy <- err }()
	p1, _ := strconv.Atoi(string(waitFile(t, filepath.Join(dir, "busy"))))
	succ := make(chan *Result, 1)
	defer func() {
		touch(t, dir, "end", "b-go", "release-a", "release")
		<-succ
		<-busy
	}()
	const id = "6c1d4b8a-2f3e-4a5b-9c7d-0e1f2a3b4c5d"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	res, err := rt.Run(ctx, "f/t/held", dirArgs, RunOptions{ID: id})
	cancel()
	if err != nil || res.Status != StatusCanceled {
		t.Fatalf("the unpinned run: %+v, %v; want canceled at its deadline", res, err)
	}
	go func() {
		res, err := rt.Run(context.Background(), "f/t/succ", dirArgs, RunOptions{ID: id, Pin: true})
		if err != nil {
			t.Error(err)
		}
		succ <- res
	}()
	p2, _ := strconv.Atoi(string(waitFile(t, filepath.Join(dir, "succ-b"))))
	if p2 == p1 {
		t.Fatalf("the pinned run went to the tainted %d", p1)
	}
	touch(t, dir, "go")
	var r struct{ PID int }
	if err := json.Unmarshal(waitFile(t, filepath.Join(dir, "res")), &r); err != nil || r.PID == p2 || r.PID <= 0 {
		t.Fatalf("the unpinned run's late resource read went to %d (%v); the pinned run's tainted process is %d", r.PID, err, p2)
	}
	touch(t, dir, "end")
	settled := 0
	for deadline := time.Now().Add(30 * time.Second); settled == 0 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		b, _ := os.ReadFile(logf)
		for _, l := range strings.Split(string(b), "\n") {
			if rest, ok := strings.CutPrefix(l, "settled "+id+" canceled "); ok {
				settled, _ = strconv.Atoi(rest)
			}
		}
	}
	if settled <= 0 || settled == p2 {
		t.Fatalf("the unpinned run's onRunSettled went to %d; the pinned run's tainted process is %d", settled, p2)
	}
}

// A run's routing choice is its own runtime's: a context that a Go hook of
// a pinned run of one runtime passes to a run of another runtime with the
// same id does not route that run into the first runtime's process.
func TestPinOfAnotherRuntime(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml": "defaultTs: bun\n",
		"f/t/pid.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { return process.pid }","input_transforms":{}}}]}}`,
	})
	open := func(pool PoolOptions) *Runtime {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: pool})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rt.Close() })
		return rt
	}
	first, second := open(PoolOptions{PinRuns: true}), open(PoolOptions{})
	const id = "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	var inner *Result
	var innerErr error
	hooks := &Hooks{OnModuleEnd: func(ctx context.Context, _ RunHandle, site Site, _ StepRecord) error {
		inner, innerErr = second.Run(ctx, "f/t/pid", nil, RunOptions{ID: id})
		return nil
	}}
	res, err := first.Run(context.Background(), "f/t/pid", nil, RunOptions{ID: id, Hooks: hooks})
	if err != nil || res.Status != StatusSuccess || innerErr != nil || inner == nil || inner.Status != StatusSuccess {
		t.Fatalf("%+v, %v; inner %+v, %v", res, err, inner, innerErr)
	}
	pid, _ := strconv.Atoi(string(inner.Result))
	if !slices.ContainsFunc(second.pool.Stats(), func(s harness.ProcessStats) bool { return s.PID == pid }) {
		t.Fatalf("the second runtime's run ran in %d, not one of its own processes (the first runtime's ran in %s)", pid, res.Result)
	}
}

// A run that a step dispatched is in flight for Run's id check while it
// lives under PinRuns: a Run with its id is refused, so the child keeps
// the pin that holds its process when that process trips a recycle
// trigger between the child's steps (an RSS threshold of one byte, which
// the heartbeats during the child's 4 s sleep report), and its next step
// runs there.
func TestDispatchedRunKeepsItsPin(t *testing.T) {
	bun := bunOrSkip(t)
	dir := t.TempDir()
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml": "defaultTs: bun\n",
		"f/t/child.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
			`{"id":"a","value":{"type":"rawscript","language":"bun","content":"import { writeFileSync } from \"node:fs\";\nexport async function main(dir: string) { writeFileSync(dir + \"/child-a\", String(process.pid)); return process.pid }",` +
			`"input_transforms":{"dir":{"type":"javascript","expr":"flow_input.dir"}}}},` +
			`{"id":"b","sleep":{"type":"static","value":4},"value":{"type":"rawscript","language":"bun","content":"export async function main(prev: number) { return { a: prev, b: process.pid } }",` +
			`"input_transforms":{"prev":{"type":"javascript","expr":"previous_result"}}}}]}}`,
		"f/t/quick.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"q","value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { return 1 }","input_transforms":{}}}]}}`,
	})
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: PoolOptions{Max: 2, PinRuns: true, Recycle: RecycleTriggers{RSS: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ctx := context.Background()
	id, err := rt.dispatch.Dispatch(ctx, platform.Dispatch{Kind: platform.DispatchFlow, Target: "f/t/child", Args: []byte(`{"dir":` + jsonString(dir) + `}`)})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(string(waitFile(t, filepath.Join(dir, "child-a"))))
	if _, err := rt.Run(ctx, "f/t/quick", nil, RunOptions{ID: id}); !errors.Is(err, ErrRunInFlight) {
		t.Fatalf("a Run with a live dispatched run's id: %v; want ErrRunInFlight", err)
	}
	var r platform.JobResult
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var ok bool
		if r, ok, err = rt.dispatch.Result(ctx, id); err != nil || (ok && r.Completed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the child did not complete")
		}
	}
	var pids struct{ A, B int }
	if err != nil || !r.Success || json.Unmarshal(r.Result, &pids) != nil || pids.A != p || pids.B != p {
		t.Fatalf("the child: %+v %s, %v; want both steps in %d", r, r.Result, err, p)
	}
}

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
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/harness"
)

// A pinned run never moves to another process. When its process ends
// between two of its steps (the test kills it from the first step's
// OnModuleEnd hook, and waits until the pool has seen the exit), the next
// step latches the run with ErrPinnedProcessLost and never launches. When
// the process ends during the run's last step, that step fails as escaped,
// and the run fails as its error, with no latch.
func TestPinnedRunProcessLost(t *testing.T) {
	bun := bunOrSkip(t)
	pidStep := `{"type":"rawscript","language":"bun","content":"export async function main() { return process.pid }","input_transforms":{}}`
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml": "defaultTs: bun\n",
		"f/t/two.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":` + pidStep + `},` +
			`{"id":"b","value":` + pidStep + `}]}}`,
		"f/t/die.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { process.kill(process.pid, 'SIGKILL'); await new Promise(() => {}) }","input_transforms":{}}}]}}`,
	})
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	gone := func(pid int) bool {
		return !slices.ContainsFunc(rt.pool.Stats(), func(s harness.ProcessStats) bool { return s.PID == pid })
	}
	hooks := &Hooks{OnModuleEnd: func(ctx context.Context, run RunHandle, site Site, rec StepRecord) error {
		if site.Module != "a" {
			return nil
		}
		pid, err := strconv.Atoi(string(rec.Result.Producer()))
		if err != nil {
			return err
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			return err
		}
		for deadline := time.Now().Add(30 * time.Second); !gone(pid); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				return errors.New("the pool did not see the exit")
			}
		}
		return nil
	}}
	res, err := rt.Run(context.Background(), "f/t/two", json.RawMessage(`{}`), RunOptions{Pin: true, Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusLatched || !errors.Is(res.Latched, ErrPinnedProcessLost) {
		t.Fatalf("%s, latched %v; want latched with pinned_process_lost", res.Status, res.Latched)
	}
	for _, s := range res.Trace.Steps {
		if s.Site.String() == "m:b" && s.Ran {
			t.Fatalf("step b ran after its pinned process ended: %+v", s)
		}
	}
	// The same flow unpinned runs its second step in the replacement.
	if res, err := rt.Run(context.Background(), "f/t/two", json.RawMessage(`{}`), RunOptions{Hooks: hooks}); err != nil || res.Status != StatusSuccess {
		t.Fatalf("unpinned: %+v, %v", res, err)
	}
	res, err = rt.Run(context.Background(), "f/t/die", json.RawMessage(`{}`), RunOptions{Pin: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusFailure || res.Latched != nil {
		t.Fatalf("%s, latched %v; want the step's failure and no latch", res.Status, res.Latched)
	}
}

// dispatchScript dispatches the flow at path through the job API and
// returns its result once it has completed; a child that failed fails it.
const dispatchScript = `export async function main(path: string) {
  const base = process.env.BASE_URL + "/api/w/" + process.env.WM_WORKSPACE;
  const headers = { Authorization: "Bearer " + process.env.WM_TOKEN, "Content-Type": "application/json" };
  const r = await fetch(base + "/jobs/run/f/" + path, { method: "POST", headers, body: "{}" });
  if (r.status !== 201) throw new Error("dispatch: " + r.status + " " + (await r.text()));
  const id = await r.text();
  for (;;) {
    const j: any = await (await fetch(base + "/jobs_u/completed/get_result_maybe/" + id, { headers })).json();
    if (j.completed) {
      if (j.success !== true) throw new Error("the child failed: " + JSON.stringify(j.result));
      return j.result;
    }
    await new Promise((res) => setTimeout(res, 5));
  }
}
`

// A run that a step dispatches takes the runtime's settings. Under a
// runtime total of 1 MiB that another run holds whole, a dispatched
// child's reservation of one byte rejects. With PinRuns, a dispatched flow
// keeps to one process, though its first step makes that process lag in
// a pool of two.
func TestDispatchedRunsTakeRuntimeSettings(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml":      "defaultTs: bun\n",
		"f/t/hold.ts":     holdScript,
		"f/t/dispatch.ts": dispatchScript,
		"f/t/reserve1.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { try { await (globalThis as any).wl.reserve(1); return 'ok' } catch (e: any) { return e.name + ': ' + e.message } }","input_transforms":{}}}]}}`,
		"f/t/spun.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
			`{"id":"a","value":{"type":"rawscript","language":"bun","content":"export async function main() { const end = performance.now() + 1300; while (performance.now() < end) {} return process.pid }","input_transforms":{}}},` +
			`{"id":"b","value":{"type":"rawscript","language":"bun","content":"export async function main(prev: number) { return { a: prev, b: process.pid } }","input_transforms":{"prev":{"type":"javascript","expr":"previous_result"}}}}]}}`,
	})
	ctx := context.Background()

	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Limits: RuntimeLimits{Bytes: mib}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	dir := t.TempDir()
	held := make(chan error, 1)
	go func() {
		_, err := rt.Run(ctx, "f/t/hold", holdArgs(dir, nil, mib, "a", []string{"b"}, 0), RunOptions{Kind: KindScript})
		held <- err
	}()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "a")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the holding run did not reserve the total")
		}
	}
	res, err := rt.Run(ctx, "f/t/dispatch", json.RawMessage(`{"path":"f/t/reserve1"}`), RunOptions{Kind: KindScript})
	for _, f := range []string{"b", "b.done"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if err != nil || res.Status != StatusSuccess || !strings.Contains(string(res.Result), "the runtime's byte total of 1048576 has 0 left") {
		t.Fatalf("a dispatched child's reservation under a full total: %+v, %v", res, err)
	}

	pinned, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: PoolOptions{Max: 2, LagThreshold: 50 * time.Millisecond, PinRuns: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	res, err = pinned.Run(ctx, "f/t/dispatch", json.RawMessage(`{"path":"f/t/spun"}`), RunOptions{Kind: KindScript})
	var pids struct{ A, B int }
	if err != nil || res.Status != StatusSuccess || json.Unmarshal(res.Result, &pids) != nil {
		t.Fatalf("%+v, %v", res, err)
	}
	if pids.A <= 0 || pids.A != pids.B {
		t.Fatalf("the dispatched pinned flow's steps ran in %d and %d", pids.A, pids.B)
	}
}

// Each run owns its pin, also when a later run takes its id before it has
// settled. A pinned run ends at its deadline with its step abandoned and
// still running, which taints its process and keeps the run from settling;
// unrelated work in flight there keeps the process from draining. An
// unpinned run that takes the id then has its hook calls routed as its
// steps are, not to the earlier run's process. The earlier run's late
// calls still reach that process: its abandoned step's read of a resource
// that the host plugin provides, and its onRunSettled.
func TestPinnedRunIDReuse(t *testing.T) {
	bun := bunOrSkip(t)
	dir := t.TempDir()
	logf := filepath.Join(dir, "plugin.log")
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	src := `import { appendFileSync } from "node:fs";
const log = (l: string) => appendFileSync(` + jsonString(logf) + `, l + "\n");
export default {
  onModuleStart: ({ run, site }: { run: string; site: { module: string } }) => { log("start " + run + " " + site.module + " " + process.pid); },
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
		"f/t/pid.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"b","value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { return process.pid }","input_transforms":{}}}]}}`,
		"f/t/busy.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"w","value":{"type":"rawscript","language":"bun","content":"` +
			`import { existsSync, writeFileSync } from \"node:fs\";\nexport async function main(dir: string) {\n` +
			`  writeFileSync(dir + \"/busy\", \"\");\n  while (!existsSync(dir + \"/release\")) await new Promise((r) => setTimeout(r, 5));\n  return process.pid;\n}` +
			`","input_transforms":{"dir":{"type":"javascript","expr":"flow_input.dir"}}}}]}}`,
	})
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: plugin, Pool: PoolOptions{Max: 2}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	logLines := func() []string {
		b, _ := os.ReadFile(logf)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	pidOf := func(prefix string) int {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			for _, l := range logLines() {
				if rest, ok := strings.CutPrefix(l, prefix); ok {
					pid, err := strconv.Atoi(rest)
					if err != nil {
						t.Fatalf("%q: %v", l, err)
					}
					return pid
				}
			}
		}
		t.Fatalf("no plugin log line %q: %q", prefix, logLines())
		return 0
	}
	const id = "0b0e5b8e-6a3f-4c55-9d61-1b2c3d4e5f60"
	dirArgs := json.RawMessage(`{"dir":` + jsonString(dir) + `}`)
	busy := make(chan *Result, 1)
	go func() {
		res, err := rt.Run(context.Background(), "f/t/busy", dirArgs, RunOptions{})
		if err != nil {
			t.Error(err)
		}
		busy <- res
	}()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "busy")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the unrelated run's step did not start")
		}
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)
		<-busy
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	res, err := rt.Run(ctx, "f/t/held", dirArgs, RunOptions{ID: id, Pin: true})
	cancel()
	if err != nil || res.Status != StatusCanceled {
		t.Fatalf("the first run: %+v, %v; want canceled at its deadline", res, err)
	}
	p1 := pidOf("start " + id + " a ")
	for _, l := range logLines() {
		if f := strings.Fields(l); len(f) == 4 && f[0] == "start" && f[2] == "w" && f[3] != strconv.Itoa(p1) {
			t.Fatalf("the test needs the unrelated step in the pinned run's process %d: %q", p1, l)
		}
	}
	res, err = rt.Run(context.Background(), "f/t/pid", nil, RunOptions{ID: id})
	if err != nil || res.Status != StatusSuccess {
		t.Fatalf("the second run: %+v, %v", res, err)
	}
	step, _ := strconv.Atoi(string(res.Result))
	if hook := pidOf("start " + id + " b "); hook != step || step == p1 {
		t.Fatalf("the second run's hook ran in %d and its step in %d; the first run's process is %d", hook, step, p1)
	}
	pidOf("settled " + id + " success ")
	// The first run's abandoned step reads the resource.
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var b []byte
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if b, err = os.ReadFile(filepath.Join(dir, "res")); err == nil && len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the abandoned step read no resource")
		}
	}
	var r struct{ PID int }
	if json.Unmarshal(b, &r) != nil || r.PID != p1 {
		t.Fatalf("the first run's abandoned step read %s; want the resource from its process %d", b, p1)
	}
	// Another unpinned run with the id goes to the other process, and then
	// the first run settles: its onRunSettled still reaches its process.
	if res, err := rt.Run(context.Background(), "f/t/pid", nil, RunOptions{ID: id}); err != nil || string(res.Result) != strconv.Itoa(step) {
		t.Fatalf("a third run with the id: %+v, %v; want it in %d", res, err, step)
	}
	if err := os.WriteFile(filepath.Join(dir, "end"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := pidOf("settled " + id + " canceled "); got != p1 {
		t.Fatalf("the first run's onRunSettled ran in %d, want its process %d", got, p1)
	}
}

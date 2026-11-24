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
// returns its result once it has completed.
const dispatchScript = `export async function main(path: string) {
  const base = process.env.BASE_URL + "/api/w/" + process.env.WM_WORKSPACE;
  const headers = { Authorization: "Bearer " + process.env.WM_TOKEN, "Content-Type": "application/json" };
  const r = await fetch(base + "/jobs/run/f/" + path, { method: "POST", headers, body: "{}" });
  if (r.status !== 201) throw new Error("dispatch: " + r.status + " " + (await r.text()));
  const id = await r.text();
  for (;;) {
    const j: any = await (await fetch(base + "/jobs_u/completed/get_result_maybe/" + id, { headers })).json();
    if (j.completed) return j.result;
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
	if pids.A != pids.B {
		t.Fatalf("the dispatched pinned flow's steps ran in %d and %d", pids.A, pids.B)
	}
}

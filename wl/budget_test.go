package wl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// holdScript waits for the files in before, reserves first bytes, writes
// the file me, waits for the files in wait, then reserves then bytes. It
// returns, which settles it and returns its reservations, only once each
// step in wait has made its second attempt too (the files <me>.done).
// Each attempt is "ok", "none" for zero bytes, or the error it threw.
const holdScript = `import { existsSync, writeFileSync } from "node:fs";
export async function main(dir: string, before: string[], first: number, me: string, wait: string[], then: number) {
  const wl = (globalThis as any).wl;
  const attempt = async (n: number) => {
    if (n <= 0) return "none";
    try { await wl.reserve(n); return "ok" } catch (e: any) { return e.name + ": " + e.message }
  };
  const gate = async (files: string[]) => {
    for (const f of files) while (!existsSync(dir + "/" + f)) await new Promise((r) => setTimeout(r, 2));
  };
  await gate(before);
  const a = await attempt(first);
  writeFileSync(dir + "/" + me, "");
  await gate(wait);
  const b = await attempt(then);
  writeFileSync(dir + "/" + me + ".done", "");
  await gate(wait.map((w) => w + ".done"));
  return { first: a, then: b, pid: process.pid };
}
`

const kib, mib = 1024, 1 << 20

// holdStep is a flow step that runs f/t/hold with the static arguments
// given, and the others from the flow input.
func holdStep(id string, static map[string]any) string {
	var in []string
	for _, k := range []string{"dir", "before", "first", "me", "wait", "then"} {
		if v, ok := static[k]; ok {
			b, _ := json.Marshal(v)
			in = append(in, `"`+k+`":{"type":"static","value":`+string(b)+`}`)
		} else {
			in = append(in, `"`+k+`":{"type":"javascript","expr":"flow_input.`+k+`"}`)
		}
	}
	return `{"id":"` + id + `","value":{"type":"script","path":"f/t/hold","input_transforms":{` + strings.Join(in, ",") + `}}}`
}

type holdResult struct {
	First, Then string
	PID         int
}

func holdOf(t *testing.T, raw json.RawMessage) holdResult {
	t.Helper()
	var r holdResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return r
}

// holdArgs are f/t/hold's arguments.
func holdArgs(dir string, before []string, first int, me string, wait []string, then int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"dir": dir, "before": append([]string{}, before...), "first": first, "me": me, "wait": append([]string{}, wait...), "then": then})
	return b
}

// budgetRuntime opens a runtime of the budget tests' bundle.
func budgetRuntime(t *testing.T, opts Options) *Runtime {
	t.Helper()
	bun := bunOrSkip(t)
	branch := func(me, other string) string {
		return `{"summary":"","skip_failure":false,"modules":[` +
			holdStep(me, map[string]any{"before": []string{}, "first": 400 * kib, "me": me, "wait": []string{other}, "then": 400 * kib}) + `]}`
	}
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml":   "defaultTs: bun\n",
		"f/t/hold.ts":  holdScript,
		"f/t/whole.ts": `export async function main() { await (globalThis as any).wl.reserve(1048576); return "whole" }` + "\n",
		// f/t/hold, then a step that reserves a whole MiB.
		"f/t/then.flow/flow.yaml": `{"summary":"t","value":{"modules":[` + holdStep("a", nil) + `,` +
			`{"id":"b","value":{"type":"script","path":"f/t/whole","input_transforms":{}}}]}}`,
		// Two parallel branches of one run reserve 400 KiB each, then 400
		// KiB more once the other holds its own.
		"f/t/two.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"ba","value":{"type":"branchall","parallel":true,"branches":[` +
			branch("x", "y") + `,` + branch("y", "x") + `]}}]}}`,
	})
	opts.Bun, opts.HarnessRuntime = bun, runtimeDir
	rt, err := Open(out, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return rt
}

// runAll runs each of runs at once and returns their results in order.
func runAll(t *testing.T, rt *Runtime, runs ...func() (*Result, error)) []*Result {
	t.Helper()
	var wg sync.WaitGroup
	results := make([]*Result, len(runs))
	errs := make([]error, len(runs))
	for i, run := range runs {
		wg.Go(func() { results[i], errs[i] = run() })
	}
	wg.Wait()
	for i, res := range results {
		if errs[i] != nil || res.Status != StatusSuccess {
			t.Fatalf("run %d: %+v, %v", i, res, errs[i])
		}
	}
	return results
}

func holdRun(rt *Runtime, path string, args json.RawMessage, opts RunOptions) func() (*Result, error) {
	return func() (*Result, error) { return rt.Run(context.Background(), path, args, opts) }
}

// A run's byte budget: two runs, each with a budget of 1 MiB, hold 768
// KiB each at once, since each run has its own. A further 512 KiB rejects
// in the step with how much is left. Once the reserving step has settled,
// its reservations are back: the next step reserves the whole MiB. A run
// with no budget reserves anything.
func TestRunByteBudget(t *testing.T) {
	rt := budgetRuntime(t, Options{})
	dir := t.TempDir()
	budget := RunOptions{Limits: RunLimits{Bytes: mib}}
	results := runAll(t, rt,
		holdRun(rt, "f/t/then", holdArgs(dir, nil, 768*kib, "r0", []string{"r1"}, 512*kib), budget),
		holdRun(rt, "f/t/then", holdArgs(dir, nil, 768*kib, "r1", []string{"r0"}, 512*kib), budget),
	)
	for i, res := range results {
		steps := map[string]json.RawMessage{}
		for _, s := range res.Trace.Steps {
			steps[s.Site.String()] = json.RawMessage(s.Result.Producer())
		}
		h := holdOf(t, steps["m:a"])
		if h.First != "ok" || !strings.HasPrefix(h.Then, "RangeError: ") || !strings.Contains(h.Then, "byte budget of 1048576 has 262144 left") {
			t.Fatalf("run %d: %+v; want 768 KiB reserved and 512 KiB more refused", i, h)
		}
		if string(res.Result) != `"whole"` {
			t.Fatalf("run %d: the next step: %s; want the whole MiB reserved", i, res.Result)
		}
	}
	// A script job has a budget too; without one, reservations take
	// nothing.
	res := runAll(t, rt, holdRun(rt, "f/t/hold", holdArgs(t.TempDir(), nil, 768*kib, "s", nil, 512*kib), RunOptions{Kind: KindScript, Limits: RunLimits{Bytes: mib}}))[0]
	if h := holdOf(t, res.Result); h.First != "ok" || !strings.HasPrefix(h.Then, "RangeError: ") {
		t.Fatalf("a script job: %+v", h)
	}
	res = runAll(t, rt, holdRun(rt, "f/t/hold", holdArgs(t.TempDir(), nil, 768*kib, "s", nil, 64*mib), RunOptions{Kind: KindScript}))[0]
	if h := holdOf(t, res.Result); h.First != "ok" || h.Then != "ok" {
		t.Fatalf("a script job with no budget: %+v", h)
	}
}

// One unpinned run's parallel branches on two processes (Cap 1, Max 2)
// hold reservations at once: the run's account counts both, so a
// reservation past its budget rejects on either process.
func TestRunByteBudgetAcrossProcesses(t *testing.T) {
	rt := budgetRuntime(t, Options{Pool: PoolOptions{Max: 2, Cap: 1}})
	res := runAll(t, rt, holdRun(rt, "f/t/two", json.RawMessage(`{"dir":`+jsonString(t.TempDir())+`}`), RunOptions{Limits: RunLimits{Bytes: mib}}))[0]
	steps := map[string]json.RawMessage{}
	for _, s := range res.Trace.Steps {
		steps[s.Site.String()] = json.RawMessage(s.Result.Producer())
	}
	x, y := holdOf(t, steps["m:ba/b:0/m:x"]), holdOf(t, steps["m:ba/b:1/m:y"])
	if x.PID == y.PID {
		t.Fatalf("both branches ran in process %d", x.PID)
	}
	for _, h := range []holdResult{x, y} {
		if h.First != "ok" || !strings.HasPrefix(h.Then, "RangeError: ") || !strings.Contains(h.Then, "has 229376 left") {
			t.Fatalf("%+v; want 400 KiB reserved and 400 KiB more refused, with 224 KiB left", h)
		}
	}
}

// The runtime's total: three runs, each with a budget of 64 MiB, under a
// total of 64 MiB. Two hold 32 MiB each; the third's 1 MiB rejects while
// they hold theirs, so together they never pass the total. Once all have
// settled the total is whole again.
func TestRuntimeByteTotal(t *testing.T) {
	rt := budgetRuntime(t, Options{Limits: RuntimeLimits{Bytes: 64 * mib}})
	dir := t.TempDir()
	budget := RunOptions{Kind: KindScript, Limits: RunLimits{Bytes: 64 * mib}}
	results := runAll(t, rt,
		holdRun(rt, "f/t/hold", holdArgs(dir, nil, 32*mib, "r0", []string{"r2"}, 0), budget),
		holdRun(rt, "f/t/hold", holdArgs(dir, nil, 32*mib, "r1", []string{"r2"}, 0), budget),
		holdRun(rt, "f/t/hold", holdArgs(dir, []string{"r0", "r1"}, 1*mib, "r2", nil, 0), budget),
	)
	for i, res := range results[:2] {
		if h := holdOf(t, res.Result); h.First != "ok" {
			t.Fatalf("run %d: %+v", i, h)
		}
	}
	if h := holdOf(t, results[2].Result); !strings.HasPrefix(h.First, "RangeError: ") || !strings.Contains(h.First, "the runtime's byte total of 67108864 has 0 left") {
		t.Fatalf("the third run: %+v; want the total's refusal", h)
	}
	if used := rt.byteTotal.Used(); used != 0 {
		t.Fatalf("%d bytes still reserved once every run settled", used)
	}
	res := runAll(t, rt, holdRun(rt, "f/t/hold", holdArgs(t.TempDir(), nil, 64*mib, "s", nil, 0), budget))[0]
	if h := holdOf(t, res.Result); h.First != "ok" {
		t.Fatalf("the whole total after the runs: %+v", h)
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// A host plugin's around wrapper draws on its step's run budget too: what
// it reserved before the step ran counts against the step's reservations,
// and returns when the invocation settles.
func TestRunByteBudgetAround(t *testing.T) {
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	src := `export default { around: [{ selector: { script: "f/t/hold" }, fn: async (_c: unknown, next: () => Promise<unknown>) => { await (globalThis as any).wl.reserve(786432); return next() } }] };`
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := budgetRuntime(t, Options{Plugin: plugin})
	for range 2 {
		res := runAll(t, rt, holdRun(rt, "f/t/hold", holdArgs(t.TempDir(), nil, 512*kib, "s", nil, 256*kib), RunOptions{Kind: KindScript, Limits: RunLimits{Bytes: mib}}))[0]
		if h := holdOf(t, res.Result); !strings.Contains(h.First, "has 262144 left") || h.Then != "ok" {
			t.Fatalf("%+v; want the step's 512 KiB refused beside the wrapper's 768 KiB, and 256 KiB reserved", h)
		}
	}
}

package wl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// runDataFiles is a folder whose host plugin, f/rd/plugin.ts, declares
// around wrappers and no hook. The wrapper of the flow f/rd/flow's step a
// puts what it reads as wl.run.data into the host-provided module
// f/rd/ctx.ts, for its run, and the step returns that next to what it
// reads as wl.run.data itself. The script f/rd/gate, alone or as the
// step of f/rd/gated, waits for a GET of the URL it is given, under the
// wrappers of both.
var runDataFiles = map[string]string{
	"wmill.yaml":  "defaultTs: bun\n",
	"f/rd/ctx.ts": "export const seen = new Map<string, unknown>();\n",
	"f/rd/step.ts": `import { seen } from "./ctx.ts";
export async function main() {
  await new Promise((r) => setTimeout(r, 50));
  const own = (globalThis as any).wl.run.data;
  return { around: seen.get(process.env.WM_FLOW_JOB_ID ?? "") ?? null, step: own === undefined ? "undefined" : own };
}
`,
	"f/rd/gate.ts": `export async function main(gate: string) {
  await fetch(gate);
  return "opened";
}
`,
	"f/rd/plugin.ts": `import { seen } from "./ctx.ts";
const record = async (c: { run: string }, next: () => Promise<unknown>) => {
  seen.set(c.run, (globalThis as any).wl.run.data ?? "undefined");
  await next();
};
export default {
  around: [
    { selector: { flow: "f/rd/flow", module: "a" }, fn: record },
    { selector: { flow: "f/rd/gated", module: "a" }, fn: record },
    { selector: { script: "f/rd/gate" }, fn: record },
  ],
};
`,
	"f/rd/flow.flow/flow.yaml": `{"summary":"d","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/rd/step","input_transforms":{}}}]}}`,
	"f/rd/gated.flow/flow.yaml": `{"summary":"g","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/rd/gate",` +
		`"input_transforms":{"gate":{"type":"javascript","expr":"flow_input.gate"}}}}]}}`,
}

const runDataContract = "roots: [{flow: f/rd/flow}, {flow: f/rd/gated}, {script: f/rd/gate}]\nhost_modules: [f/rd/ctx.ts]\nplugin: f/rd/plugin.ts\n"

// TestRunDataAroundOnly runs, in a runtime with one fresh harness process,
// three concurrent runs of a flow whose plugin declares only around
// wrappers, so no host_call reaches the process before each run's first
// invoke: two runs with different data and one with none. Each wrapper
// reads its own run's data as wl.run.data, and the run without data reads
// undefined. The step, which runs inside the wrapper, reads undefined:
// run data is the plugin's.
func TestRunDataAroundOnly(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildContractBundle(t, bun, runDataFiles, runDataContract)
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: PoolOptions{Max: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	data := []string{`{"tenant":"t-0","n":[1,2]}`, `{"tenant":"t-1"}`, ""}
	want := []string{`{"tenant":"t-0","n":[1,2]}`, `{"tenant":"t-1"}`, `"undefined"`}
	type result struct {
		Around json.RawMessage
		Step   json.RawMessage
	}
	got := make([]result, len(data))
	errs := make([]error, len(data))
	var wg sync.WaitGroup
	for i := range data {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := rt.Run(context.Background(), "f/rd/flow", nil, RunOptions{Data: json.RawMessage(data[i])})
			if err == nil && res.Status != StatusSuccess {
				err = fmt.Errorf("status %s: %s", res.Status, res.Result)
			}
			if err == nil {
				err = json.Unmarshal(res.Result, &got[i])
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i := range data {
		switch {
		case errs[i] != nil:
			t.Errorf("run %d: %v", i, errs[i])
		case !sameJSON(got[i].Around, json.RawMessage(want[i])):
			t.Errorf("run %d: the around wrapper read %s as wl.run.data, want %s", i, got[i].Around, want[i])
		case string(got[i].Step) != `"undefined"`:
			t.Errorf("run %d: the step read %s as wl.run.data, want undefined", i, got[i].Step)
		}
	}
	if n := len(rt.pool.Stats()); n != 1 {
		t.Errorf("%d harness processes, want the one", n)
	}
}

// TestRunDataReleased runs a flow and a script job with data, each of
// whose step waits at a gate, in a runtime with one harness process. While
// the step waits, the process's health reports that it holds one run's
// data; once the run has settled, none.
func TestRunDataReleased(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildContractBundle(t, bun, runDataFiles, runDataContract)
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: PoolOptions{Max: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	// held is what the process last reported, with its heartbeat, every
	// second.
	held := func(t *testing.T) int {
		stats := rt.pool.Stats()
		if len(stats) != 1 {
			t.Fatalf("%d harness processes, want one", len(stats))
		}
		if stats[0].Health == nil {
			return -1
		}
		return stats[0].Health.RunsHeld
	}
	waitHeld := func(t *testing.T, want int, what string) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			n := held(t)
			if n == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the process holds %d runs' data, want %d", what, n, want)
			}
		}
	}
	for _, kind := range []string{KindFlow, KindScript} {
		t.Run(kind, func(t *testing.T) {
			open := make(chan struct{})
			var opened sync.Once
			openGate := func() { opened.Do(func() { close(open) }) }
			gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-open }))
			defer gate.Close()
			// Opened before the server closes, which waits for the step's
			// request, when the test fails first too.
			defer openGate()
			args, _ := json.Marshal(map[string]string{"gate": gate.URL})
			path := "f/rd/gated"
			if kind == KindScript {
				path = "f/rd/gate"
			}
			done := make(chan error, 1)
			go func() {
				res, err := rt.Run(context.Background(), path, args, RunOptions{Kind: kind, Data: json.RawMessage(`{"k":"` + kind + `"}`)})
				if err == nil && res.Status != StatusSuccess {
					err = fmt.Errorf("status %s: %s", res.Status, res.Result)
				}
				done <- err
			}()
			waitHeld(t, 1, "while the step waits")
			openGate()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			waitHeld(t, 0, "once the run has settled")
		})
	}
}

// TestRunDataNeedsPlugin: in a runtime whose processes load no host
// plugin, a run's data reaches no process. While the run's step waits at a
// gate, the process's health counts the invocation in flight and no run's
// data.
func TestRunDataNeedsPlugin(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildContractBundle(t, bun, runDataFiles, "roots: [{flow: f/rd/flow}, {flow: f/rd/gated}, {script: f/rd/gate}]\nhost_modules: [f/rd/ctx.ts]\n")
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: PoolOptions{Max: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	open := make(chan struct{})
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-open }))
	defer gate.Close()
	defer close(open)
	args, _ := json.Marshal(map[string]string{"gate": gate.URL})
	done := make(chan error, 1)
	go func() {
		_, err := rt.Run(context.Background(), "f/rd/gated", args, RunOptions{Data: json.RawMessage(`{"k":1}`)})
		done <- err
	}()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if stats := rt.pool.Stats(); len(stats) == 1 && stats[0].Health != nil && stats[0].Health.InFlight == 1 {
			if n := stats[0].Health.RunsHeld; n != 0 {
				t.Errorf("a process without a plugin holds %d runs' data", n)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no health report with the step in flight")
		}
	}
	open <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// runDataHookFiles is a folder whose host plugin answers the flow
// f/rh/flow's step a with an intercept hook, which returns what it reads
// as wl.run.data, whether the data can be changed, and what the plugin
// read when it loaded. The around wrapper of step b, which runs after a,
// puts what it reads into the host-provided module f/rh/ctx.ts, and the
// step returns that with a's result.
var runDataHookFiles = map[string]string{
	"wmill.yaml":   "defaultTs: bun\n",
	"f/rh/ctx.ts":  "export const seen = new Map<string, unknown>();\n",
	"f/rh/skip.ts": "export async function main() {\n  return \"ran\";\n}\n",
	"f/rh/read.ts": `import { seen } from "./ctx.ts";
export async function main(a: unknown) {
  return { a, around: seen.get(process.env.WM_FLOW_JOB_ID ?? "") ?? null };
}
`,
	"f/rh/plugin.ts": `import { seen } from "./ctx.ts";
const loaded = (globalThis as any).wl.run.data;
export default {
  intercept: [
    {
      selector: { flow: "f/rh/flow", module: "a" },
      fn: () => {
        const d = (globalThis as any).wl.run.data;
        let changed = "no data";
        if (d !== undefined) {
          try {
            d.k = "changed";
            changed = "changed";
          } catch {
            changed = "refused";
          }
        }
        return { result: { hook: d ?? "undefined", changed, loaded: loaded ?? "undefined" } };
      },
    },
  ],
  around: [
    {
      selector: { flow: "f/rh/flow", module: "b" },
      fn: async (c: { run: string }, next: () => Promise<unknown>) => {
        seen.set(c.run, (globalThis as any).wl.run.data ?? "undefined");
        await next();
      },
    },
  ],
  variable: () => ({ value: JSON.stringify((globalThis as any).wl.run.data ?? "undefined") }),
};
`,
	// A step that reads the variable u/rh/data, which only the plugin's
	// variable hook holds, through the platform facade.
	"f/rh/var.ts": "export async function main() {\n" +
		"  const r = await fetch(`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/variables/get_value/u/rh/data`, " +
		"{ headers: { Authorization: `Bearer ${process.env.WM_TOKEN}` } });\n" +
		"  return JSON.parse(await r.json());\n}\n",
	"f/rh/var_flow.flow/flow.yaml": `{"summary":"v","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/rh/var","input_transforms":{}}}]}}`,
	"f/rh/flow.flow/flow.yaml": `{"summary":"h","value":{"modules":[` +
		`{"id":"a","value":{"type":"script","path":"f/rh/skip","input_transforms":{}}},` +
		`{"id":"b","value":{"type":"script","path":"f/rh/read","input_transforms":{"a":{"type":"javascript","expr":"results.a"}}}}]}}`,
}

// TestRunDataHook runs, in a runtime with one harness process, a flow whose
// first step a plugin hook answers, so the run's first frame to the process
// is a host_call. The hook reads the run's data as wl.run.data and cannot
// change it; the around wrapper of the next step, whose invoke comes once
// the process holds the data, reads it too. A run without data reads
// undefined in both, and the plugin read undefined when it loaded. The
// plugin's variable hook, which a step's read through the platform facade
// reaches outside the run's own context, reads its run's data too.
func TestRunDataHook(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildContractBundle(t, bun, runDataHookFiles, "roots: [{flow: f/rh/flow}, {flow: f/rh/var_flow}]\nhost_modules: [f/rh/ctx.ts]\nplugin: f/rh/plugin.ts\n")
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: PoolOptions{Max: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	for _, c := range []struct {
		data, want string
	}{
		{`{"tenant":"t-7"}`, `{"a":{"hook":{"tenant":"t-7"},"changed":"refused","loaded":"undefined"},"around":{"tenant":"t-7"}}`},
		{"", `{"a":{"hook":"undefined","changed":"no data","loaded":"undefined"},"around":"undefined"}`},
	} {
		res, err := rt.Run(context.Background(), "f/rh/flow", nil, RunOptions{Data: json.RawMessage(c.data)})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusSuccess || !sameJSON(res.Result, json.RawMessage(c.want)) {
			t.Errorf("data %q: status %s, result %s, want %s", c.data, res.Status, res.Result, c.want)
		}
	}
	for _, c := range []struct{ data, want string }{{`{"tenant":"t-8"}`, `{"tenant":"t-8"}`}, {"", `"undefined"`}} {
		res, err := rt.Run(context.Background(), "f/rh/var_flow", nil, RunOptions{Data: json.RawMessage(c.data)})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusSuccess || !sameJSON(res.Result, json.RawMessage(c.want)) {
			t.Errorf("data %q: the variable hook gave %s %s, want %s", c.data, res.Status, res.Result, c.want)
		}
	}
}

// TestRunDataRefusesInvalid: data that is not JSON refuses the run before
// it starts, among it a value with whitespace that JSON does not allow
// around it, and JSON null with JSON's whitespace is no data.
func TestRunDataRefusesInvalid(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildContractBundle(t, bun, runDataFiles, runDataContract)
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	for _, bad := range []string{`{"k":`, "\u00a0{}", "{}\u00a0", "\u00a0", "\v{}"} {
		if _, err := rt.Run(context.Background(), "f/rd/flow", nil, RunOptions{Data: json.RawMessage(bad)}); err == nil {
			t.Errorf("a run whose data is %q started", bad)
		}
	}
	res, err := rt.Run(context.Background(), "f/rd/flow", nil, RunOptions{Data: json.RawMessage(" \t\r\nnull\n")})
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Around json.RawMessage }
	if err := json.Unmarshal(res.Result, &got); err != nil || string(got.Around) != `"undefined"` {
		t.Errorf("a run whose data is null: the wrapper read %s (%v), want undefined", got.Around, err)
	}
}

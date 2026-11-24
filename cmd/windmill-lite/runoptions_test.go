//go:build unix

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// These tests carry the run options of a request and of the configuration
// through Runner to the real engine, which launchPlugin starts: pinned
// runs, a run's limits and byte budget, the runtime's byte total, run
// data, the bound on live processes and the foreign Windmill origins
// (ADR 0009, the plugin command and the Runner service). Their bundle has
// a host plugin of its own, which the engine loads with the bundle.

// runOptionFiles is the synthetic folder of these tests. Its host plugin,
// f/opt/plugin.ts, declares around wrappers only. They share the
// host-provided module f/opt/ctx.ts with the steps of their process:
//   - around each call of f/opt/data, the wrapper records what it reads as
//     wl.run.data for its run, which the step returns;
//   - around each call of f/opt/first, f/opt/second and f/opt/report, the
//     three steps of the flow f/opt/pinned, each a script of its own, the
//     wrapper appends its process's id to its run's list, which
//     f/opt/report returns with its own process's id.
var runOptionFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/opt/ctx.ts": `// The host plugin's state, by run, which the steps of its process read.
export const seen = new Map<string, unknown>();
export const pids = new Map<string, number[]>();
`,
	"f/opt/plugin.ts": `import { pids, seen } from "./ctx.ts";
type Next = () => Promise<unknown>;
const data = async (c: { run: string }, next: Next) => {
  const data = (globalThis as any).wl.run.data;
  seen.set(c.run, data === undefined ? "undefined" : data);
  return next();
};
const pid = async (c: { run: string }, next: Next) => {
  pids.set(c.run, [...(pids.get(c.run) ?? []), process.pid]);
  return next();
};
export default {
  around: [
    { selector: { script: "f/opt/data" }, fn: data },
    { selector: { script: "f/opt/first" }, fn: pid },
    { selector: { script: "f/opt/second" }, fn: pid },
    { selector: { script: "f/opt/report" }, fn: pid },
  ],
};
`,
	"f/opt/data.ts": `// Returns what the host plugin's wrapper read as its run's data.
import { seen } from "./ctx.ts";
export async function main() {
  await Bun.sleep(50);
  const run = process.env.WM_FLOW_JOB_ID || process.env.WM_JOB_ID || "";
  return seen.has(run) ? seen.get(run) : "not wrapped";
}
`,
	"f/opt/first.ts": `// The first step of f/opt/pinned.
export async function main() {
  return "first";
}
`,
	"f/opt/second.ts": `// The second step of f/opt/pinned.
export async function main() {
  return "second";
}
`,
	"f/opt/report.ts": `// Returns the process ids that the host plugin recorded for its run, and
// its own.
import { pids } from "./ctx.ts";
export async function main() {
  return { pids: pids.get(process.env.WM_FLOW_JOB_ID ?? "") ?? [], pid: process.pid };
}
`,
	"f/opt/inflight.ts": `// Marks its entry in dir and counts the steps of dir inside at that
// moment, itself included: entered and not left. Then it waits, at most
// 60 s: with hold "all" until four steps have entered, and with hold
// "test" until the test writes <me>.go. It marks its exit before it
// returns the count, so that a step admitted after it sees it gone.
import { existsSync, readdirSync, writeFileSync } from "node:fs";
export async function main(dir: string, me: string, hold: string) {
  writeFileSync(dir + "/" + me + ".in", "");
  const names = readdirSync(dir);
  const seen = names.filter((n) => n.endsWith(".in") && !names.includes(n.slice(0, -3) + ".out")).length;
  const ready = hold === "all"
    ? () => readdirSync(dir).filter((n) => n.endsWith(".in")).length >= 4
    : () => existsSync(dir + "/" + me + ".go");
  const until = Date.now() + 60_000;
  while (!ready()) {
    if (Date.now() > until) throw new Error("not released");
    await Bun.sleep(5);
  }
  writeFileSync(dir + "/" + me + ".out", "");
  return seen;
}
`,
	"f/opt/big.ts": `// Returns a string of 100 characters.
export async function main() {
  return "x".repeat(100);
}
`,
	"f/opt/echo.ts": `// Returns its argument.
export async function main(x: unknown) {
  return x;
}
`,
	"f/opt/reserve.ts": `// Reserves bytes of its run's byte budget, which rejects when they do
// not fit, marks me in dir, then waits until each file of wait is in dir.
import { existsSync, writeFileSync } from "node:fs";
export async function main(dir: string, me: string, bytes: number, wait: string[]) {
  await (globalThis as any).wl.reserve(bytes);
  writeFileSync(dir + "/" + me, "");
  const until = Date.now() + 60_000;
  for (const f of wait) {
    while (!existsSync(dir + "/" + f)) {
      if (Date.now() > until) throw new Error("not released");
      await Bun.sleep(5);
    }
  }
  return "reserved";
}
`,
	"f/opt/link.ts": `// Dispatches a script at base as the stock clients do, with the
// invocation's own job ids as its parent_job and root_job, and returns
// those ids.
export async function main(base: string) {
  const e = process.env;
  const r = await fetch(base + "/api/w/" + e.WM_WORKSPACE + "/jobs/run/p/f/opt/echo?parent_job=" + e.WM_JOB_ID + "&root_job=" + e.WM_ROOT_FLOW_JOB_ID, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  });
  return { job: e.WM_JOB_ID, root: e.WM_ROOT_FLOW_JOB_ID, status: r.status };
}
`,
	"f/opt/data.flow/flow.yaml": `{"summary":"one step that returns its run's data","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/opt/data","input_transforms":{}}}]}}`,
	"f/opt/pinned.flow/flow.yaml": `{"summary":"three steps, each a script of its own","value":{"modules":[` +
		`{"id":"a","value":{"type":"script","path":"f/opt/first","input_transforms":{}}},` +
		`{"id":"b","value":{"type":"script","path":"f/opt/second","input_transforms":{}}},` +
		`{"id":"c","value":{"type":"script","path":"f/opt/report","input_transforms":{}}}]}}`,
	"f/opt/inflight.flow/flow.yaml": `{"summary":"four parallel branches","value":{"modules":[{"id":"p","value":{"type":"branchall","parallel":true,"branches":[` +
		inflightBranch("b0") + `,` + inflightBranch("b1") + `,` + inflightBranch("b2") + `,` + inflightBranch("b3") + `]}}]}}`,
	"f/opt/big.flow/flow.yaml": `{"summary":"two steps that return 100 characters each","value":{"modules":[` +
		`{"id":"a","value":{"type":"script","path":"f/opt/big","input_transforms":{}}},` +
		`{"id":"b","value":{"type":"script","path":"f/opt/big","input_transforms":{}}}]}}`,
	"f/opt/echo.flow/flow.yaml": `{"summary":"one step that echoes","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/opt/echo",` +
		`"input_transforms":{"x":{"type":"javascript","expr":"flow_input.x"}}}}]}}`,
	"f/opt/reserve.flow/flow.yaml": `{"summary":"one step that reserves bytes","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/opt/reserve",` +
		`"input_transforms":{"dir":{"type":"javascript","expr":"flow_input.dir"},"me":{"type":"javascript","expr":"flow_input.me"},` +
		`"bytes":{"type":"javascript","expr":"flow_input.bytes"},"wait":{"type":"javascript","expr":"flow_input.wait"}}}}]}}`,
	"f/opt/link.flow/flow.yaml": `{"summary":"one step that dispatches at a Windmill","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/opt/link",` +
		`"input_transforms":{"base":{"type":"javascript","expr":"flow_input.base"}}}}]}}`,
}

// inflightBranch is a branch of f/opt/inflight whose one step is
// f/opt/inflight, named me.
func inflightBranch(me string) string {
	return `{"summary":"","skip_failure":false,"modules":[{"id":"` + me + `","value":{"type":"script","path":"f/opt/inflight",` +
		`"input_transforms":{"dir":{"type":"javascript","expr":"flow_input.dir"},"me":{"type":"static","value":"` + me + `"},` +
		`"hold":{"type":"javascript","expr":"flow_input.hold"}}}}]}`
}

// inflightSteps are the names of f/opt/inflight's four branch steps.
var inflightSteps = []string{"b0", "b1", "b2", "b3"}

// releaseInflight lets the steps of a run of f/opt/inflight with hold
// "test" in dir leave, one at a time: it releases a step that is inside
// only while two are inside, or once all four have entered, and waits for
// that step to leave before it releases another. A third step that
// entered while two were inside would see three. Once ended is closed,
// when the run's call has returned, such as with a refusal, it returns at
// once and leaves the report to its caller. It fails the test if the
// steps do not get that far within a minute.
func releaseInflight(t *testing.T, dir string, ended <-chan struct{}) {
	t.Helper()
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	over := func() bool {
		select {
		case <-ended:
			return true
		default:
			return false
		}
	}
	deadline := time.Now().Add(time.Minute)
	for released := map[string]bool{}; len(released) < len(inflightSteps); {
		next := ""
		for next == "" {
			if over() {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the parallel branches did not reach two inside within a minute; released %v", released)
			}
			entered, inside := 0, []string{}
			for _, s := range inflightSteps {
				if has(s + ".in") {
					entered++
					if !has(s + ".out") {
						inside = append(inside, s)
					}
				}
			}
			if len(inside) >= 2 || (entered == len(inflightSteps) && len(inside) >= 1) {
				next = inside[0]
			} else {
				time.Sleep(5 * time.Millisecond)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, next+".go"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		released[next] = true
		for !has(next + ".out") {
			if over() {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the released branch %s did not leave within a minute", next)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

const runOptionContract = `roots:
  - flow: f/opt/data
  - script: f/opt/data
  - flow: f/opt/pinned
  - flow: f/opt/inflight
  - flow: f/opt/big
  - flow: f/opt/echo
  - flow: f/opt/reserve
  - flow: f/opt/link
host_modules: [f/opt/ctx.ts]
plugin: f/opt/plugin.ts
`

var runOptionBuild struct {
	once   sync.Once
	dir    string
	bundle string
	err    error
}

// runOptionBundle returns the pinned Bun and these tests' bundle, built
// once per test binary.
func runOptionBundle(t *testing.T) (bun, out string) {
	t.Helper()
	bun = absBun(t)
	runOptionBuild.once.Do(func() {
		if runOptionBuild.dir, runOptionBuild.err = os.MkdirTemp("", "wl-runopt-"); runOptionBuild.err != nil {
			return
		}
		runOptionBuild.bundle = filepath.Join(runOptionBuild.dir, "bundle")
		runOptionBuild.err = buildFolder(bun, runOptionFiles, runOptionContract, runOptionBuild.bundle)
	})
	if runOptionBuild.err != nil {
		t.Fatal(runOptionBuild.err)
	}
	return bun, runOptionBuild.bundle
}

// runAll runs each request at once and returns their results in order. It
// fails the test unless each run started.
func (e *pluginEngine) runAll(t *testing.T, reqs ...*plugin.RunRequest) []*plugin.RunResult {
	t.Helper()
	results := make([]*plugin.RunResult, len(reqs))
	errs := make([]error, len(reqs))
	var wg sync.WaitGroup
	for i, req := range reqs {
		wg.Go(func() { results[i], errs[i] = e.run(req) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("run %d (%s): %v\nthe engine's standard error:\n%s", i, reqs[i].GetPath(), err, e.stderr)
		}
	}
	return results
}

// TestRunnerPinnedState checks pinned runs (ADR 0009, process affinity and
// limits) in a pool whose module recycle threshold is one. f/opt/pinned's
// three steps are three scripts. The dispatch of a second distinct script
// to a process trips its trigger: that script still runs there, and the
// process then takes no new invocation of an unpinned run, which goes to
// the replacement, where the bundle's host plugin has no state for the run.
//
//   - With pin set on each request, and then with the configuration's
//     pool.pin_runs and no pin on the requests, two concurrent runs each
//     keep the process of their first step, whichever it is: the plugin's
//     list for each run holds the entries of all three steps, each the
//     reporting step's process.
//   - The control is one unpinned run, alone in its engine, so that its
//     own second step is what trips the trigger: its reporting step runs
//     in the replacement, whose list for the run holds only its own entry.
func TestRunnerPinnedState(t *testing.T) {
	bun, out := runOptionBundle(t)
	for _, c := range []struct {
		name    string
		pinRuns bool
		pin     bool
	}{
		{"pin on each request", false, true},
		{"pool.pin_runs", true, false},
		{"unpinned", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := pluginConfigOf(t, bun, out)
			pool := map[string]any{"max": 1, "recycle": map[string]any{"modules": 1}}
			if c.pinRuns {
				pool["pin_runs"] = true
			}
			cfg["pool"] = pool
			e := launchPlugin(t, cfg)
			req := func(id string) *plugin.RunRequest {
				return &plugin.RunRequest{Id: id, Path: "f/opt/pinned", Pin: c.pin}
			}
			pinned := c.pin || c.pinRuns
			reqs := []*plugin.RunRequest{req("unpinned")}
			if pinned {
				reqs = []*plugin.RunRequest{req("pinned-0"), req("pinned-1")}
			}
			for i, res := range e.runAll(t, reqs...) {
				var got struct {
					PIDs []int
					PID  int
				}
				if res.GetStatus() != "success" || json.Unmarshal(res.GetResult(), &got) != nil {
					t.Fatalf("run %d: %s %s", i, res.GetStatus(), res.GetResult())
				}
				whole := len(got.PIDs) == 3
				for _, pid := range got.PIDs {
					whole = whole && pid == got.PID
				}
				switch {
				case pinned && !whole:
					t.Errorf("run %d: the host plugin's state for the run is %v in process %d; want the three steps' entries there, the run's state whole",
						i, got.PIDs, got.PID)
				case !pinned && !slices.Equal(got.PIDs, []int{got.PID}):
					t.Errorf("run %d: the host plugin's state for the run is %v in process %d; want the reporting step's entry alone, in a process of its own",
						i, got.PIDs, got.PID)
				}
			}
		})
	}
}

// TestRunnerLimits checks a request's limits (ADR 0009, process affinity
// and limits) on the engine's one process. Each of four parallel branches
// counts the branches inside at its entry. With max_in_flight 2, whose
// branches wait for the test's release, none sees more than two, the
// second of the first two sees two, and the result's deviations name the
// cap. Without it, each waits until all four have entered, and the last
// sees four. Arguments larger than
// max_args_bytes refuse the run before it starts, with the RUN_NOT_STARTED
// detail, and arguments of that size run. Stored values past
// max_held_bytes latch the run with run_memory_limit. A reservation past
// the run's byte budget rejects in its step with a RangeError, which the
// failed run's result carries, and one within it is reserved. A negative
// limit refuses the run.
func TestRunnerLimits(t *testing.T) {
	bun, out := runOptionBundle(t)
	e := launchPlugin(t, pluginConfigOf(t, bun, out))

	inflight := func(limits *plugin.Limits, hold string) (int, []string) {
		t.Helper()
		dir := t.TempDir()
		args, err := json.Marshal(map[string]string{"dir": dir, "hold": hold})
		if err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			res *plugin.RunResult
			err error
		}
		done := make(chan outcome, 1)
		ended := make(chan struct{})
		go func() {
			res, err := e.run(&plugin.RunRequest{Path: "f/opt/inflight", Args: args, Limits: limits})
			done <- outcome{res, err}
			close(ended)
		}()
		if hold == "test" {
			releaseInflight(t, dir, ended)
		}
		o := <-done
		var counts []int
		if o.err != nil || o.res.GetStatus() != "success" || json.Unmarshal(o.res.GetResult(), &counts) != nil || len(counts) != 4 {
			t.Fatalf("the parallel branches: (%v, %v)\nthe engine's standard error:\n%s", o.res, o.err, e.stderr)
		}
		return slices.Max(counts), o.res.GetDeviations()
	}
	if most, deviations := inflight(&plugin.Limits{MaxInFlight: 2}, "test"); most != 2 || !slices.Contains(deviations, "D15") {
		t.Errorf("max_in_flight 2: at most %d branches inside at once, deviations %v; want 2, with D15", most, deviations)
	}
	if most, deviations := inflight(nil, "all"); most != 4 || slices.Contains(deviations, "D15") {
		t.Errorf("no limits: at most %d branches inside at once, deviations %v; want 4, without D15", most, deviations)
	}

	args := []byte(`{"x":"` + strings.Repeat("x", 100) + `"}`)
	_, err := e.run(&plugin.RunRequest{Path: "f/opt/echo", Args: args, Limits: &plugin.Limits{MaxArgsBytes: int64(len(args) - 1)}})
	checkRefused(t, "arguments larger than max_args_bytes", err, codes.InvalidArgument, "larger than its limit")
	if res := e.mustRun(t, &plugin.RunRequest{Path: "f/opt/echo", Args: args, Limits: &plugin.Limits{MaxArgsBytes: int64(len(args))}}); res.GetStatus() != "success" {
		t.Errorf("arguments of max_args_bytes: %s %s, want success", res.GetStatus(), res.GetResult())
	}

	res := e.mustRun(t, &plugin.RunRequest{Path: "f/opt/big", Limits: &plugin.Limits{MaxHeldBytes: 150}})
	if res.GetStatus() != "latched" || !strings.Contains(res.GetLatched(), "run_memory_limit") {
		t.Errorf("stored values past max_held_bytes: %s, latched %q; want latched with run_memory_limit", res.GetStatus(), res.GetLatched())
	}
	if res := e.mustRun(t, &plugin.RunRequest{Path: "f/opt/big", Limits: &plugin.Limits{MaxHeldBytes: 1000}}); res.GetStatus() != "success" {
		t.Errorf("stored values within max_held_bytes: %s, latched %q; want success", res.GetStatus(), res.GetLatched())
	}

	reserve := func(bytes int) []byte {
		b, err := json.Marshal(map[string]any{"dir": t.TempDir(), "me": "r", "bytes": bytes, "wait": []string{}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	res = e.mustRun(t, &plugin.RunRequest{Path: "f/opt/reserve", Args: reserve(2 << 20), Limits: &plugin.Limits{Bytes: 1 << 20}})
	if result := string(res.GetResult()); res.GetStatus() != "failure" || !strings.Contains(result, "RangeError") ||
		!strings.Contains(result, "the run's byte budget of 1048576 has 1048576 left") {
		t.Errorf("a reservation past the run's byte budget: %s %s; want failure with the step's RangeError", res.GetStatus(), result)
	}
	if res := e.mustRun(t, &plugin.RunRequest{Path: "f/opt/reserve", Args: reserve(1 << 19), Limits: &plugin.Limits{Bytes: 1 << 20}}); res.GetStatus() != "success" {
		t.Errorf("a reservation within the run's byte budget: %s %s, want success", res.GetStatus(), res.GetResult())
	}

	_, err = e.run(&plugin.RunRequest{Path: "f/opt/echo", Args: []byte(`{"x":1}`), Limits: &plugin.Limits{MaxInFlight: -1}})
	checkRefused(t, "a negative max_in_flight", err, codes.InvalidArgument, "not negative")
}

// TestRunnerBytesTotal checks the configuration's bytes_total, the
// runtime's byte total that every run's reservations share (ADR 0009, the
// plugin command): under a total of 64 MiB, two runs each have a byte
// budget of 48 MiB. While the first holds its 48 MiB in its step, the
// second's reservation of 48 MiB rejects in its step, though its own
// budget has room; once the first's step has settled, a third run
// reserves 48 MiB. A negative total refuses the start.
func TestRunnerBytesTotal(t *testing.T) {
	bun, out := runOptionBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	cfg["bytes_total"] = 64 << 20
	e := launchPlugin(t, cfg)
	dir := t.TempDir()
	budget := &plugin.Limits{Bytes: 48 << 20}
	reserve := func(me string, wait ...string) []byte {
		b, err := json.Marshal(map[string]any{"dir": dir, "me": me, "bytes": 48 << 20, "wait": append([]string{}, wait...)})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	type outcome struct {
		res *plugin.RunResult
		err error
	}
	first := make(chan outcome, 1)
	go func() {
		res, err := e.run(&plugin.RunRequest{Id: "holds", Path: "f/opt/reserve", Args: reserve("first", "release"), Limits: budget})
		first <- outcome{res, err}
	}()
	waitFile(t, dir, "first")
	res := e.mustRun(t, &plugin.RunRequest{Path: "f/opt/reserve", Args: reserve("second"), Limits: budget})
	if result := string(res.GetResult()); res.GetStatus() != "failure" || !strings.Contains(result, "RangeError") ||
		!strings.Contains(result, "the runtime's byte total of 67108864 has 16777216 left") {
		t.Errorf("the second run while the first holds 48 MiB: %s %s; want failure with the step's RangeError for the total", res.GetStatus(), result)
	}
	release(t, dir)
	if o := <-first; o.err != nil || o.res.GetStatus() != "success" {
		t.Fatalf("the first run: (%v, %v), want success\nthe engine's standard error:\n%s", o.res, o.err, e.stderr)
	}
	if res := e.mustRun(t, &plugin.RunRequest{Path: "f/opt/reserve", Args: reserve("third"), Limits: budget}); res.GetStatus() != "success" {
		t.Errorf("a run after the first's step settled: %s %s, want success", res.GetStatus(), res.GetResult())
	}

	cfg = pluginConfigOf(t, bun, out)
	cfg["bytes_total"] = -1
	if code, stderr := startRefused(t, cfg); code != 1 || !strings.Contains(stderr, "Limits.Bytes is -1") {
		t.Errorf("a negative bytes_total: exit %d, stderr %q; want 1, naming the total", code, stderr)
	}
}

// TestRunnerRunData checks a request's run_data (ADR 0009, the Runner
// service): in the engine's one process, three concurrent runs of a flow
// whose step the bundle's host plugin wraps, two with their own data and
// one with none, and a root script's run with data. Each wrapper reads its
// own run's data as wl.run.data, and the run without data reads undefined.
// Data that is not JSON refuses the run.
func TestRunnerRunData(t *testing.T) {
	bun, out := runOptionBundle(t)
	e := launchPlugin(t, pluginConfigOf(t, bun, out))
	reqs := []*plugin.RunRequest{
		{Path: "f/opt/data", RunData: []byte(`{"tenant":"t-0","n":[1,2]}`)},
		{Path: "f/opt/data", RunData: []byte(`{"tenant":"t-1"}`)},
		{Path: "f/opt/data"},
		{Kind: "script", Path: "f/opt/data", RunData: []byte(`"for the script"`)},
	}
	want := []string{`{"tenant":"t-0","n":[1,2]}`, `{"tenant":"t-1"}`, `"undefined"`, `"for the script"`}
	for i, res := range e.runAll(t, reqs...) {
		var got, w any
		if res.GetStatus() != "success" || json.Unmarshal(res.GetResult(), &got) != nil {
			t.Fatalf("run %d: %s %s", i, res.GetStatus(), res.GetResult())
		}
		if err := json.Unmarshal([]byte(want[i]), &w); err != nil {
			t.Fatal(err)
		}
		if !jsonValuesEqual(got, w) {
			t.Errorf("run %d (%s %s): the host plugin's wrapper read %s as wl.run.data, want %s", i, reqs[i].GetKind(), reqs[i].GetPath(), res.GetResult(), want[i])
		}
	}
	_, err := e.run(&plugin.RunRequest{Path: "f/opt/data", RunData: []byte(`{"tenant":`)})
	checkRefused(t, "run data that is not JSON", err, codes.InvalidArgument, "data is not JSON")
}

// TestRunnerMaxLive checks the configuration's pool.max_live, the bound on
// a pool's live processes (ADR 0009, the plugin command): a max_live of 1
// with a max of 2 refuses the start, naming the bound, as wl.Open refuses
// a positive bound below the maximum, and a max_live of 2 serves runs.
func TestRunnerMaxLive(t *testing.T) {
	bun, out := runOptionBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	cfg["pool"] = map[string]any{"max": 2, "max_live": 1}
	if code, stderr := startRefused(t, cfg); code != 1 || !strings.Contains(stderr, "MaxLive 1 is below its Max 2") {
		t.Errorf("pool.max_live below pool.max: exit %d, stderr %q; want 1, naming the bound", code, stderr)
	}
	cfg = pluginConfigOf(t, bun, out)
	cfg["pool"] = map[string]any{"max": 2, "max_live": 2}
	e := launchPlugin(t, cfg)
	if res := e.mustRun(t, &plugin.RunRequest{Path: "f/opt/echo", Args: []byte(`{"x":3}`)}); res.GetStatus() != "success" || string(res.GetResult()) != "3" {
		t.Errorf("a run with pool.max_live 2: %s %s, want success, 3", res.GetStatus(), res.GetResult())
	}
}

// TestRunnerForeignOrigins checks the configuration's
// foreign_windmill_origins (ADR 0009, the plugin command; ADR 0007, async
// dispatch): two loopback servers stand in for real Windmill instances,
// and the configuration lists one of them. A step's dispatch under
// /api/w/{workspace}/jobs/run/ to the listed one loses the parent_job and
// root_job that carry the step's own job ids, while the same dispatch to
// the other keeps them. An origin with the path /api refuses the start,
// naming its position.
func TestRunnerForeignOrigins(t *testing.T) {
	bun, out := runOptionBundle(t)
	type request struct {
		path  string
		query url.Values
	}
	stand := func() (*httptest.Server, func() []request) {
		var mu sync.Mutex
		var seen []request
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			mu.Lock()
			seen = append(seen, request{r.URL.Path, r.URL.Query()})
			mu.Unlock()
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, "00000000-0000-0000-0000-000000000001")
		}))
		t.Cleanup(s.Close)
		return s, func() []request {
			mu.Lock()
			defer mu.Unlock()
			return append([]request{}, seen...)
		}
	}
	listed, listedSeen := stand()
	other, otherSeen := stand()
	cfg := pluginConfigOf(t, bun, out)
	cfg["foreign_windmill_origins"] = []string{listed.URL}
	e := launchPlugin(t, cfg)

	for _, c := range []struct {
		name   string
		server *httptest.Server
		seen   func() []request
		links  bool
	}{
		{"the listed origin", listed, listedSeen, false},
		{"an origin not listed", other, otherSeen, true},
	} {
		args, err := json.Marshal(map[string]string{"base": c.server.URL})
		if err != nil {
			t.Fatal(err)
		}
		res := e.mustRun(t, &plugin.RunRequest{Path: "f/opt/link", Args: args})
		var step struct {
			Job, Root string
			Status    int
		}
		if res.GetStatus() != "success" || json.Unmarshal(res.GetResult(), &step) != nil || step.Job == "" || step.Root == "" || step.Status != http.StatusCreated {
			t.Fatalf("%s: %s %s", c.name, res.GetStatus(), res.GetResult())
		}
		seen := c.seen()
		if len(seen) != 1 || !strings.HasSuffix(seen[0].path, "/jobs/run/p/f/opt/echo") {
			t.Fatalf("%s: the server saw %+v, want the one dispatch", c.name, seen)
		}
		q := seen[0].query
		for name, id := range map[string]string{"parent_job": step.Job, "root_job": step.Root} {
			switch {
			case c.links && q.Get(name) != id:
				t.Errorf("%s: %s %q (present %t), want the step's %q", c.name, name, q.Get(name), q.Has(name), id)
			case !c.links && q.Has(name):
				t.Errorf("%s: %s %q is still there, want it removed", c.name, name, q.Get(name))
			}
		}
	}

	cfg = pluginConfigOf(t, bun, out)
	cfg["foreign_windmill_origins"] = []string{listed.URL, listed.URL + "/api"}
	if code, stderr := startRefused(t, cfg); code != 1 || !strings.Contains(stderr, "ForeignWindmillOrigins[1]") {
		t.Errorf("an origin with a path: exit %d, stderr %q; want 1, naming its position", code, stderr)
	}
}

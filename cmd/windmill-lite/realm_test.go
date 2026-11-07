package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/wl"
)

// These check what native-boundary invocations leave behind in a
// long-lived Bun process. Each native invocation runs in a fresh node:vm
// realm (harnesses/bun/runtime/native.ts). Under Bun 1.3.10, every script
// that node:vm compiles from a string stays reachable after a full
// collection, so a harness that compiles one per invocation grows with
// every invocation it serves, without bound. Bun 1.4.3 keeps none of them;
// these checks still hold the harness to compiling each script once, as
// it does on any Bun. The probe step f/native/probe
// reads, after a full collection, the count of JavaScriptCore Script
// objects (heapStats().objectTypeCounts.Script, from bun:jsc), the
// JavaScript heap and the resident set of the process it runs in: the
// host's in host mode, the harness's in subprocess mode, where the native
// steps run too.

// nativeSteps is how many native steps a run of f/native/steps makes.
const nativeSteps = 10

// nativeInput is f/native/steps' flow input; every step returns its
// argument, so the run's result is its p.
const nativeInput = `{"p":{"n":1,"s":"native"}}`

// nativeFolder is the synthetic sync folder of the native-realm checks.
// Its flows each run nativeSteps native steps that return their argument
// (nativeFlow):
//
//   - f/native/steps: a bunnative step and a nativets one in turn; the
//     third is a nativets step that imports the embedded client, which
//     each invocation loads into its own realm;
//   - f/native/varying: the same with f/native/evals, a nativets step that
//     returns its argument through a direct eval, in place of the
//     bunnative one.
func nativeFolder() map[string]string {
	files := map[string]string{
		"wmill.yaml": "defaultTs: bun\nincludes:\n  - f/**\n",
		"f/native/bunnative.ts": `//native
// Returns its argument: a bunnative step.
export async function main(p: unknown) {
  return p;
}
`,
		"f/native/nativets.fetch.ts": `// Returns its argument: a nativets step.
export async function main(p: unknown) {
  return p;
}
`,
		"f/native/client.fetch.ts": `// Returns its argument once the embedded client has loaded: a nativets
// step that imports it.
import * as wmill from "./windmill.ts";

export async function main(p: unknown) {
  return typeof wmill.getVariable === "function" ? p : null;
}
`,
		"f/native/evals.fetch.ts": `// Returns its argument through a direct eval, which can read any variable
// of the module's scope: a nativets step whose preamble declares every
// variable of the environment.
export async function main(p: unknown) {
  return eval("p");
}
`,
		"f/native/probe.ts": `// The native-realm probe: after a full collection, the count of
// JavaScriptCore Script objects, the JavaScript heap and the resident set,
// in bytes, of the process this Bun step runs in.
import { heapStats } from "bun:jsc";

export async function main() {
  for (let i = 0; i < 3; i++) Bun.gc(true);
  const s = heapStats();
  return { scripts: s.objectTypeCounts.Script ?? 0, heap: s.heapSize, rss: process.memoryUsage().rss };
}
`,
		"f/native/probe.flow/flow.yaml": "summary: The native-realm probe\nvalue:\n  modules:\n    - id: a\n      value: {type: script, path: f/native/probe, input_transforms: {}}\n",
	}
	files["f/native/steps.flow/flow.yaml"] = nativeFlow("Native steps that each return their argument", func(k int) string {
		switch {
		case k == 2:
			return "f/native/client"
		case k%2 == 1:
			return "f/native/nativets"
		}
		return "f/native/bunnative"
	})
	files["f/native/varying.flow/flow.yaml"] = nativeFlow("Nativets steps that each return their argument, some through eval", func(k int) string {
		switch {
		case k == 2:
			return "f/native/client"
		case k%2 == 1:
			return "f/native/nativets"
		}
		return "f/native/evals"
	})
	return files
}

// nativeFlow is a flow of nativeSteps steps, step k running the script
// path(k), that each return their argument: the first the flow input's p,
// the others previous_result.
func nativeFlow(summary string, path func(k int) string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "summary: %s\nvalue:\n  modules:\n", summary)
	for k := range nativeSteps {
		from := "previous_result"
		if k == 0 {
			from = "flow_input.p"
		}
		fmt.Fprintf(&b, "    - id: %c\n      value: {type: script, path: %s, input_transforms: {p: {type: javascript, expr: %s}}}\n", 'a'+k, path(k), from)
	}
	return b.String()
}

var (
	nativeOnce       sync.Once
	nativeBundlePath string
	nativeErr        error
)

// nativeBundle builds nativeFolder's bundle once per test binary, in the
// benchmarks' directory (benchSetup, which tb must have called).
func nativeBundle(tb testing.TB, bun string) string {
	tb.Helper()
	nativeOnce.Do(func() {
		folder := filepath.Join(benchDir, "nativefolder")
		for name, body := range nativeFolder() {
			p := filepath.Join(folder, filepath.FromSlash(name))
			if nativeErr = os.MkdirAll(filepath.Dir(p), 0o755); nativeErr != nil {
				return
			}
			if nativeErr = os.WriteFile(p, []byte(body), 0o644); nativeErr != nil {
				return
			}
		}
		repo, err := filepath.Abs("../..")
		if err != nil {
			nativeErr = err
			return
		}
		nativeBundlePath = filepath.Join(benchDir, "nativebundle")
		_, nativeErr = bundle.Build(context.Background(), bundle.Config{
			Folder:         folder,
			EngineVersion:  resolveVersion(),
			Bun:            bun,
			Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
			HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
			Out:            nativeBundlePath,
			Work:           filepath.Join(benchDir, "nativework"),
		})
	})
	if nativeErr != nil {
		tb.Fatal(nativeErr)
	}
	return nativeBundlePath
}

// realmSample is what f/native/probe read: the Script objects, the
// JavaScript heap and the resident set, in bytes, after a full collection.
type realmSample struct {
	Scripts int   `json:"scripts"`
	Heap    int64 `json:"heap"`
	RSS     int64 `json:"rss"`
}

func (s realmSample) String() string {
	return fmt.Sprintf("%d scripts, heap %.2f MiB, rss %.1f MiB", s.Scripts, float64(s.Heap)/(1<<20), float64(s.RSS)/(1<<20))
}

func realmProbe(tb testing.TB, rt *wl.Runtime) realmSample {
	tb.Helper()
	res, err := rt.Run(context.Background(), "f/native/probe", []byte(`{}`), wl.RunOptions{})
	if err != nil || res.Status != wl.StatusSuccess {
		tb.Fatalf("the probe: %+v, %v", res, err)
	}
	var s realmSample
	if err := json.Unmarshal(res.Result, &s); err != nil || s.Heap == 0 {
		tb.Fatalf("the probe's result %.200s: %v", res.Result, err)
	}
	return s
}

// runNative runs the flow at path, f/native/steps or f/native/varying, n
// times, one after another, with the run's hooks (nil for none), and
// checks that each returned its input.
func runNative(tb testing.TB, rt *wl.Runtime, path string, n int, hooks *wl.Hooks) {
	tb.Helper()
	var in struct {
		P json.RawMessage `json:"p"`
	}
	if err := json.Unmarshal([]byte(nativeInput), &in); err != nil {
		tb.Fatal(err)
	}
	for range n {
		res, err := rt.Run(context.Background(), path, []byte(nativeInput), wl.RunOptions{Hooks: hooks})
		if err != nil || res.Status != wl.StatusSuccess || !bytes.Equal(res.Result, in.P) {
			tb.Fatalf("%s: %+v, %v; want the result %s", path, res, err, in.P)
		}
	}
}

// varyingNames are hooks whose environment hook (H12) gives every
// invocation a variable that no invocation had before: WL_VARYING_1,
// WL_VARYING_2 and so on. A nativets module and the embedded client
// declare each variable of the environment that their text spells as a
// const of its name, and every variable when their text spells eval.
func varyingNames() *wl.Hooks {
	var seq atomic.Int64
	return &wl.Hooks{InvocationEnv: func(_ context.Context, _ wl.RunHandle, _ wl.Site, env map[string]string) error {
		env[fmt.Sprintf("WL_VARYING_%d", seq.Add(1))] = "v"
		return nil
	}}
}

// nativeVariants is how many preamble variants of one module the harness
// keeps compiled (MAX_VARIANTS, harnesses/bun/runtime/native.ts).
const nativeVariants = 16

// TestNativeInvocationsKeepNoScript runs N native-boundary invocations on
// one long-lived process, N = 200 and then 400, after runs that load and
// compile every module, and checks that the count of Script objects after
// a full collection does not grow with N: by fewer than 5 over the whole
// run. It runs in host mode, and in subprocess mode, which runs native
// steps with the same code, each with two kinds of environment:
//
//   - fixed names: f/native/steps, whose every invocation has the same
//     variable names, so one run compiles every module;
//   - varying names (varyingNames): f/native/varying, whose every
//     invocation adds a name no invocation had before. The nativets
//     steps, the step that imports the embedded client and the client
//     never spell those names, so they keep one variant each. The step
//     that evaluates code spells eval, so each of its invocations has a
//     preamble of its own: the first nativeVariants runs may keep at most
//     nativeVariants Script objects more, for its variants, and no run
//     after them may keep more.
func TestNativeInvocationsKeepNoScript(t *testing.T) {
	bun := benchSetup(t)
	bundlePath := nativeBundle(t, bun)
	for _, topology := range []string{"host", "subprocess"} {
		for _, names := range []string{"fixed", "varying"} {
			t.Run("topology="+topology+"/names="+names, func(t *testing.T) {
				rt := benchRuntimeOn(t, bun, topology, bundlePath, func(*wl.Options) {})
				path, hooks := "f/native/steps", (*wl.Hooks)(nil)
				if names == "varying" {
					path, hooks = "f/native/varying", varyingNames()
				}
				runNative(t, rt, path, 1, hooks)
				base := realmProbe(t, rt)
				t.Logf("after one run: %v", base)
				if names == "varying" {
					runNative(t, rt, path, nativeVariants, hooks)
					first := base
					base = realmProbe(t, rt)
					t.Logf("after %d more runs: %v", nativeVariants, base)
					if kept := base.Scripts - first.Scripts; kept > nativeVariants {
						t.Errorf("%s: the runs that fill the variants kept %d more Script objects; want at most %d", topology, kept, nativeVariants)
					}
				}
				done := 0
				for _, n := range []int{200, 400} {
					runNative(t, rt, path, (n-done)/nativeSteps, hooks)
					done = n
					s := realmProbe(t, rt)
					t.Logf("after %d more native invocations: %v", n, s)
					if grew := s.Scripts - base.Scripts; grew >= 5 {
						t.Errorf("%s, %s names: %d native invocations left %d more Script objects after a full collection; want fewer than 5", topology, names, n, grew)
					}
				}
			})
		}
	}
}

// TestNativeVariableNames checks, through the runtime, which environment
// variables a nativets step's preamble declares (WR-45, v1.862.0). A
// variable named process, a reserved word, or a name that is not an ASCII
// identifier gets no const, so f/native/steps returns its input. A
// variable named arguments gets one, which strict code refuses: it fails
// the load of f/native/varying's first step, f/native/evals, whose text
// spells eval, so that its preamble declares every variable. The run's
// environment hook (H12) also gives every invocation a name of its own, so
// each invocation of that step has a preamble of its own: the first
// nativeVariants compile, and the rest go through the realm's eval, all
// with the same error. Without arguments the run returns its input.
func TestNativeVariableNames(t *testing.T) {
	bun := benchSetup(t)
	bundlePath := nativeBundle(t, bun)
	const failed = "execution error:\nError: failed to load module: SyntaxError: Cannot declare a variable named arguments in strict mode."
	for _, topology := range []string{"host", "subprocess"} {
		t.Run("topology="+topology, func(t *testing.T) {
			rt := benchRuntimeOn(t, bun, topology, bundlePath, func(*wl.Options) {})
			var seq atomic.Int64
			hooks := func(extra map[string]string) *wl.Hooks {
				return &wl.Hooks{InvocationEnv: func(_ context.Context, _ wl.RunHandle, _ wl.Site, env map[string]string) error {
					env[fmt.Sprintf("WL_NAME_%d", seq.Add(1))] = "v"
					for k, v := range extra {
						env[k] = v
					}
					return nil
				}}
			}
			unbound := map[string]string{"process": "x", "while": "x", "WL-DASHED": "x", "WL_\u00c9": "x"}
			res, err := rt.Run(context.Background(), "f/native/steps", []byte(`{"p":1}`), wl.RunOptions{Hooks: hooks(unbound)})
			if err != nil || res.Status != wl.StatusSuccess || string(res.Result) != "1" {
				t.Fatalf("with names that get no const: %+v, %v; want the result 1", res, err)
			}
			for i := range nativeVariants + 4 {
				res, err := rt.Run(context.Background(), "f/native/varying", []byte(`{"p":1}`), wl.RunOptions{Hooks: hooks(map[string]string{"arguments": "x"})})
				if err != nil {
					t.Fatal(err)
				}
				var out struct {
					Error struct {
						Message string `json:"message"`
						StepID  string `json:"step_id"`
					} `json:"error"`
				}
				if res.Status == wl.StatusSuccess || json.Unmarshal(res.Result, &out) != nil || out.Error.Message != failed || out.Error.StepID != "a" {
					t.Fatalf("run %d with arguments: %s, %s; want step a's load to fail with %q", i, res.Status, res.Result, failed)
				}
			}
			res, err = rt.Run(context.Background(), "f/native/varying", []byte(`{"p":1}`), wl.RunOptions{Hooks: hooks(nil)})
			if err != nil || res.Status != wl.StatusSuccess || string(res.Result) != "1" {
				t.Fatalf("without arguments: %+v, %v; want the result 1", res, err)
			}
		})
	}
}

// nativeRuns is how many runs of f/native/steps BenchmarkNativeRealm makes
// on each topology, whatever b.N: WL_NATIVE_RUNS, 2,000 by default (20,000
// native invocations).
func nativeRuns(tb testing.TB) int {
	tb.Helper()
	v := os.Getenv("WL_NATIVE_RUNS")
	if v == "" {
		return 2000
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 4 {
		tb.Fatalf("WL_NATIVE_RUNS=%q: want a number of runs, at least 4", v)
	}
	return n
}

// BenchmarkNativeRealm is the long run of these checks: sequential runs of
// f/native/steps on one long-lived process, in host and subprocess mode.
// It reads f/native/probe after a first run and after each quarter of the
// runs (logged). It reports what each native invocation of the last three
// quarters leaves after a full collection, so that the allocators' growth
// while the process warms up does not count: Script objects
// (scripts/inv), JavaScript heap (heap-B/inv) and resident set
// (rss-B/inv). It also reports the last reading and the Script objects
// the whole run left (scripts-kept). Run it with -benchtime 1x: it makes
// nativeRuns runs.
func BenchmarkNativeRealm(b *testing.B) {
	bun := benchSetup(b)
	bundlePath := nativeBundle(b, bun)
	runs := nativeRuns(b)
	for _, topology := range []string{"host", "subprocess"} {
		b.Run("topology="+topology, func(b *testing.B) {
			rt := benchRuntimeOn(b, bun, topology, bundlePath, func(*wl.Options) {})
			runNative(b, rt, "f/native/steps", 1, nil)
			first := realmProbe(b, rt)
			b.Logf("%s, after one run: %v", topology, first)
			var quarter, last realmSample
			done := 0
			for q := 1; q <= 4; q++ {
				n := runs*q/4 - done
				runNative(b, rt, "f/native/steps", n, nil)
				done += n
				last = realmProbe(b, rt)
				if q == 1 {
					quarter = last
				}
				b.Logf("%s, after %d runs (%d native invocations): %v", topology, done, done*nativeSteps, last)
			}
			inv := float64((runs - runs/4) * nativeSteps)
			b.ReportMetric(float64(last.Scripts-quarter.Scripts)/inv, "scripts/inv")
			b.ReportMetric(float64(last.Heap-quarter.Heap)/inv, "heap-B/inv")
			b.ReportMetric(float64(last.RSS-quarter.RSS)/inv, "rss-B/inv")
			b.ReportMetric(float64(last.Scripts-first.Scripts), "scripts-kept")
			b.ReportMetric(float64(last.Heap)/(1<<20), "heap-MiB")
			b.ReportMetric(float64(last.RSS)/(1<<20), "rss-MiB")
			b.ReportMetric(float64(runs*nativeSteps), "invocations")
		})
	}
}

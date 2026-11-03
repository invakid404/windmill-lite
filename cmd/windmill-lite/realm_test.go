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
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/wl"
)

// These check what native-boundary invocations leave behind in a
// long-lived Bun process. Each native invocation runs in a fresh node:vm
// realm (harnesses/bun/runtime/native.ts). Under the pinned Bun 1.3.10,
// every script that node:vm compiles from a string stays reachable after a
// full collection, so a harness that compiles one per invocation grows with
// every invocation it serves, without bound. The probe step f/native/probe
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

// nativeFolder is the synthetic sync folder of the native-realm checks:
// f/native/steps, ten native steps that each return their argument (the
// first the flow input's p, the others previous_result). They alternate a
// bunnative step and a nativets one; the third is a nativets step that
// imports the embedded client, which each invocation loads into its own
// realm.
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
	var steps strings.Builder
	steps.WriteString("summary: Native steps that each return their argument\nvalue:\n  modules:\n")
	for k := range nativeSteps {
		path, from := "f/native/bunnative", "previous_result"
		switch {
		case k == 2:
			path = "f/native/client"
		case k%2 == 1:
			path = "f/native/nativets"
		}
		if k == 0 {
			from = "flow_input.p"
		}
		fmt.Fprintf(&steps, "    - id: %c\n      value: {type: script, path: %s, input_transforms: {p: {type: javascript, expr: %s}}}\n", 'a'+k, path, from)
	}
	files["f/native/steps.flow/flow.yaml"] = steps.String()
	return files
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

// runNative runs f/native/steps n times, one after another, and checks
// that each returned its input.
func runNative(tb testing.TB, rt *wl.Runtime, n int) {
	tb.Helper()
	var in struct {
		P json.RawMessage `json:"p"`
	}
	if err := json.Unmarshal([]byte(nativeInput), &in); err != nil {
		tb.Fatal(err)
	}
	for range n {
		res, err := rt.Run(context.Background(), "f/native/steps", []byte(nativeInput), wl.RunOptions{})
		if err != nil || res.Status != wl.StatusSuccess || !bytes.Equal(res.Result, in.P) {
			tb.Fatalf("f/native/steps: %+v, %v; want the result %s", res, err, in.P)
		}
	}
}

// TestNativeInvocationsKeepNoScript runs N native-boundary invocations on
// one long-lived process, N = 200 and then 400, after a run that loads
// and compiles every module once, and checks that the count of Script
// objects after a full collection does not grow with N: by fewer than 5
// over the whole run. In host mode, the topology the milestone names, and
// in subprocess mode, which runs native steps with the same code.
func TestNativeInvocationsKeepNoScript(t *testing.T) {
	bun := benchSetup(t)
	bundlePath := nativeBundle(t, bun)
	for _, topology := range []string{"host", "subprocess"} {
		t.Run("topology="+topology, func(t *testing.T) {
			rt := benchRuntimeOn(t, bun, topology, bundlePath, func(*wl.Options) {})
			runNative(t, rt, 1)
			base := realmProbe(t, rt)
			t.Logf("after one run: %v", base)
			done := 0
			for _, n := range []int{200, 400} {
				runNative(t, rt, (n-done)/nativeSteps)
				done = n
				s := realmProbe(t, rt)
				t.Logf("after %d more native invocations: %v", n, s)
				if grew := s.Scripts - base.Scripts; grew >= 5 {
					t.Errorf("%s: %d native invocations left %d more Script objects after a full collection; want fewer than 5", topology, n, grew)
				}
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
			runNative(b, rt, 1)
			first := realmProbe(b, rt)
			b.Logf("%s, after one run: %v", topology, first)
			var quarter, last realmSample
			done := 0
			for q := 1; q <= 4; q++ {
				n := runs*q/4 - done
				runNative(b, rt, n)
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

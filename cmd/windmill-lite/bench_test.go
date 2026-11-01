package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bench"
	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/wl"
)

// The end-to-end benchmarks of the step boundary (ADR 0005's benchmark
// plan) run synthetic flows on the real topologies, with the engine in
// this process, so that they read each run's trace and the engine's CPU
// time:
//
//   - host: a warm Bun 1.3.10 host in host mode, testdata/benchhost/host.ts
//     compiled with `bun build --compile`, which embeds the host SDK; the
//     SDK starts its engine as its child, as it always does, and that
//     child is this test binary as an engine proxy (benchProxy), which
//     hands the host's socket and child channel to the benchmark, as
//     `wltest check --mode host` does; the runtime is the one `windmill-lite
//     host` opens (wl.Options.Host), with the watchdog off, since the
//     engine is not the host's child here;
//   - subprocess: the warm Bun harness the engine spawns (harnesses/bun/
//     runtime), one process, as `windmill-lite run` uses it.
//
// The runs are submitted with Runtime.Run. One iteration of a benchmark
// is one run per worker, conc workers started together (bench.Workers), so
// even b.N = 1 puts conc runs in flight at once. Each benchmark reports,
// per step (report):
//
//   - ns/op, the wall time per step at its concurrency, and MB/s, the
//     payload bytes the steps moved both ways per second;
//   - the step's elapsed time, its module span in the trace, at p50, p95
//     and its mean;
//   - the stages of each step (stages), each the difference of two
//     readings of the same invocation: the trace's module and harness
//     spans, the engine side's clock (harness.SetTimesHook) and the
//     harness's own (harnesses/bun README, Benchmark timings); each
//     stage's mean and p50 over the steps, the means adding up to the
//     step's mean;
//   - the share of a run's module time spent at the boundary, its harness
//     spans less its steps' own code, per run at p50 and over all runs;
//     the same for the whole harness span; and the share of a run's
//     elapsed time in its modules;
//   - the engine's CPU time (this process's, getrusage) and the harness's
//     or host's (process.cpuUsage(), read by the probe step f/bench/probe
//     before and after), per step; for runs one at a time, also each run's
//     engine CPU and, for a shape, its host CPU, at p50 and p95;
//   - the host's or harness's peak resident set (host-peak-rss-mb) and,
//     on the plugin host, its provider hooks' calls per run
//     (host-calls/run);
//   - the concurrency reached (conc-peak, conc-mean: runs in flight) and
//     the one-minute load average when the runs started (load1).
//
// The benchmarks:
//
//   - BenchmarkStepBoundary: item 2 of ADR 0005's plan, the per-step
//     boundary cost for the payloads of bench.Synthetic from 26 B to
//     4 MiB, one run at a time and eight at once: f/bench/chain, eight
//     steps that each return their argument, the first taking the flow
//     input's member p and the others previous_result, both shortcuts that
//     run no expression (JE-6, JE-7).
//   - BenchmarkHostShapes: the shapes a host-mode embedder runs, in host
//     mode: 50 to 600 steps per run, arguments and results of 1 to 50 KiB,
//     0 to 3 log lines and 0 to 2 variable or resource reads through the
//     platform facade per step, and an expression that binds the flow
//     input in every step.
//
// They need the pinned Bun in WL_BUN and the build helper's dependencies,
// and run only with -bench; the command and the recorded baseline are in
// ADR 0005. WL_BENCH_TIMINGS=off leaves the stage readings out, which
// measures what taking them costs. TestBenchSplit runs the measurement
// once on each topology.

// benchProxyEnv puts the test binary in engine-proxy mode: the path of
// the benchmark's Unix socket.
const benchProxyEnv = "WL_BENCH_ENGINE_PROXY"

// benchProxy is the engine the benchmark host's SDK starts: it passes its
// file descriptors 3 (the host's socket) and 4 (the child channel) to the
// benchmark listening at sock, and waits until the benchmark closes the
// connection, keeping its copies open until then (on macOS, a socket
// closed while in flight to the receiver can end the connection first).
func benchProxy(sock string) int {
	c, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench engine proxy: %v\n", err)
		return 1
	}
	defer c.Close()
	fds := []int{3}
	var st syscall.Stat_t
	if syscall.Fstat(4, &st) == nil {
		fds = append(fds, 4)
	}
	if _, _, err := c.(*net.UnixConn).WriteMsgUnix([]byte{byte(len(fds))}, syscall.UnixRights(fds...), nil); err != nil {
		fmt.Fprintf(os.Stderr, "bench engine proxy: %v\n", err)
		return 1
	}
	_, _ = io.Copy(io.Discard, c)
	return 0
}

// benchChainSteps is the number of steps of f/bench/chain.
const benchChainSteps = 8

// benchShape is one host-mode shape: a flow of steps steps, step k
// returning a result of sizes[k mod len(sizes)] bytes, run conc at a
// time. A bare shape writes no log line, reads nothing and binds no flow
// input; the others do, step k writing k mod 4 log lines and making k mod
// 3 platform reads, alternately a variable and a resource.
//
// A shape with res set is the brief's example A instead, on a host whose
// plugin provides the resources (the topology host-plugin): step k takes
// two resources as $res: arguments, res/r<k mod 4 + 1> and the next, and
// three expressions: the previous result and an expression that binds the
// flow input, as the other shapes, and results.s<k-1>.n, the previous
// step's member. Its steps return small results; sizes is unused.
type benchShape struct {
	name  string
	steps int
	sizes []int
	bare  bool
	conc  int
	res   string
	// paths gives every step three member paths over JSON values instead
	// of the bare shape's nothing: flow_input.cfg.region,
	// previous_result.data.items[0].id and results.s<k-1>?.n.
	paths bool
	// plain runs the bare shape's steps with f/bench/plain, which returns
	// the same result and holds no I/O code, so that host mode leaves its
	// args.json out (ADR 0004 host mode item 7). f/bench/work can fetch, so
	// its invocations keep the file. Its runs report no host CPU of their
	// own; the probe's before and after give the total.
	plain bool
}

// plugin reports whether the shape runs on the plugin host.
func (s benchShape) plugin() bool { return s.res != "" }

var benchShapes = []benchShape{
	{name: "bare-s600-1KiB", steps: 600, sizes: []int{1 << 10}, bare: true, conc: 1},
	{name: "s50-1KiB", steps: 50, sizes: []int{1 << 10}, conc: 1},
	{name: "s50-50KiB", steps: 50, sizes: []int{50 << 10}, conc: 1},
	{name: "s200-mixed", steps: 200, sizes: benchMixed, conc: 1},
	{name: "s600-1KiB", steps: 600, sizes: []int{1 << 10}, conc: 1},
	{name: "s600-10KiB", steps: 600, sizes: []int{10 << 10}, conc: 1},
	{name: "s600-50KiB", steps: 600, sizes: []int{50 << 10}, conc: 1},
	{name: "s600-mixed", steps: 600, sizes: benchMixed, conc: 1},
	{name: "s200-mixed-conc8", steps: 200, sizes: benchMixed, conc: 8},
	{name: "plain-s600-1KiB", steps: 600, sizes: []int{1 << 10}, bare: true, plain: true, conc: 1},
	{name: "pathsA-s50", steps: 50, sizes: []int{1 << 10}, bare: true, paths: true, conc: 1},
	{name: "resA-s50", steps: 50, sizes: []int{1 << 10}, conc: 1, res: "f/bench"},
	{name: "resA-s50-1ms", steps: 50, sizes: []int{1 << 10}, conc: 1, res: "f/bench/slow"},
}

// benchMixed are the result sizes of a mixed shape's steps, in turn.
var benchMixed = []int{1 << 10, 5 << 10, 20 << 10, 50 << 10}

func (s benchShape) path() string {
	if s.plugin() {
		return "f/bench/" + strings.ReplaceAll(strings.ToLower(s.name), "-", "_")
	}
	if s.paths {
		return fmt.Sprintf("f/bench/paths_s%d_%s", s.steps, sizesName(s.sizes))
	}
	if s.plain {
		return fmt.Sprintf("f/bench/plain_s%d_%s", s.steps, sizesName(s.sizes))
	}
	if s.bare {
		return fmt.Sprintf("f/bench/bare_s%d_%s", s.steps, sizesName(s.sizes))
	}
	return fmt.Sprintf("f/bench/shape_s%d_%s", s.steps, sizesName(s.sizes))
}

func sizesName(sizes []int) string {
	if len(sizes) > 1 {
		return "mixed"
	}
	return strings.ToLower(bench.SizeLabel(sizes[0]))
}

// benchFolder is the synthetic sync folder of the benchmarks.
func benchFolder() map[string]string {
	files := map[string]string{
		"wmill.yaml": "defaultTs: bun\nincludes:\n  - f/**\n",
		"f/bench/echo.ts": `// Returns its argument: the step of the boundary benchmarks.
export async function main(p: unknown) {
  return p;
}
`,
		"f/bench/probe.ts": `// The benchmarks' probe: the CPU time of the process the step runs in, in
// microseconds (the host's in host mode, the harness's otherwise), and the
// harness's benchmark timings so far, which it drains. With timings, it
// sets their sink, so that the runtime records the next invocations; else
// it removes it.
const sink = Symbol.for("windmill-lite.timings");

export async function main(timings: boolean) {
  const g = globalThis as Record<symbol, unknown>;
  const got = Array.isArray(g[sink]) ? (g[sink] as unknown[]) : [];
  g[sink] = timings ? [] : undefined;
  const c = process.cpuUsage();
  // The peak resident set, in bytes: Bun gives it in bytes on macOS and
  // in kilobytes on Linux, as getrusage does.
  const peak = process.resourceUsage().maxRSS;
  // A plugin host's count of its provider hooks' calls, by run and path.
  const calls = g[Symbol.for("windmill-lite.bench.calls")] ?? {};
  return { user: c.user, system: c.system, timings: got, rss_peak: process.platform === "darwin" ? peak : peak * 1024, calls };
}
`,
		"f/bench/work.ts":              benchWork,
		"f/bench/plain.ts":             benchPlain,
		"f/bench/resstep.ts":           benchResStep,
		"f/bench/cachestep.ts":         benchCacheStep,
		"f/bench/probe.flow/flow.yaml": "summary: The benchmarks' probe\nvalue:\n  modules:\n    - id: a\n      value: {type: script, path: f/bench/probe, input_transforms: {timings: {type: javascript, expr: flow_input.timings}}}\n",
	}
	var chain strings.Builder
	chain.WriteString("summary: Steps that each return their argument\nvalue:\n  modules:\n")
	for k := range benchChainSteps {
		from := "previous_result"
		if k == 0 {
			from = "flow_input.p"
		}
		fmt.Fprintf(&chain, "    - id: %c\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: %s}}}\n", 'a'+k, from)
	}
	files["f/bench/chain.flow/flow.yaml"] = chain.String()
	var cache strings.Builder
	cache.WriteString("summary: Steps that read the same resources and variable\nvalue:\n  modules:\n")
	for k := range 3 {
		fmt.Fprintf(&cache, "    - id: c%d\n      value: {type: script, path: f/bench/cachestep, input_transforms: {counter: {type: static, value: '$res:f/bench/counter'}, job: {type: static, value: '$res:f/bench/jobres'}}}\n", k+1)
	}
	files["f/bench/cachecheck.flow/flow.yaml"] = cache.String()
	for _, s := range benchShapes {
		files[s.path()+".flow/flow.yaml"] = s.flow()
	}
	// WL_BENCH_ARGS_FILE=1 makes every script name args.json, so that the
	// build clears none and host mode writes every invocation's file: the
	// before side of the args file's measurement, on the same code.
	if os.Getenv("WL_BENCH_ARGS_FILE") == "1" {
		for name, body := range files {
			if strings.HasSuffix(name, ".ts") {
				files[name] = body + "\nexport const argsFile = \"args.json\";\n"
			}
		}
	}
	return files
}

// benchPlain is the plain shape's step: f/bench/work's result for no log
// lines and no reads, from code that holds no I/O at all, nor a property
// read by a built name, so that the build clears it (no_args_file).
const benchPlain = `// The step of the plain shape: work's result, with no I/O code.
const made = new Map<number, unknown>();

function data(size: number): unknown {
  let v = made.get(size);
  if (v === undefined) {
    const items: unknown[] = [];
    let len = 21;
    for (let i = 0; ; i++) {
      const rec = { id: i, name: "item " + (100000 + i), note: "line one\nline two", tags: "alpha beta".split(" "), price: (i % 1000) + 0.25, active: true, meta: { zone: "eu-1", score: 0.5, ref: null } };
      const n = JSON.stringify(rec).length;
      if (len + n + 1 > size) break;
      items.push(rec);
      len += n + 1;
    }
    v = { items, pad: "x".repeat(Math.max(0, size - len - 2)) };
    made.set(size, v);
  }
  return v;
}

export async function main(prev: unknown, n: number, size: number) {
  return { n, key: null, given: prev === undefined ? 0 : 1, data: data(size) };
}
`

// flow is the shape's flow: step k (from 1) takes the previous result
// (the flow input's seed for the first), its size, log lines and reads as
// static values, and, unless bare, a key from an expression that binds
// the flow input; the last step reports the host's CPU time with its
// result.
func (s benchShape) flow() string {
	if s.plugin() {
		return s.resFlow()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "summary: G1 shape %s\nvalue:\n  modules:\n", s.name)
	if s.plain {
		for k := 1; k <= s.steps; k++ {
			prev := "previous_result"
			if k == 1 {
				prev = "flow_input.seed"
			}
			fmt.Fprintf(&b, "    - id: s%d\n      value:\n        type: script\n        path: f/bench/plain\n        input_transforms:\n", k)
			fmt.Fprintf(&b, "          prev: {type: javascript, expr: %s}\n          n: {type: static, value: %d}\n          size: {type: static, value: %d}\n", prev, k, s.sizes[0])
		}
		return b.String()
	}
	for k := 1; k <= s.steps; k++ {
		prev := "previous_result"
		if k == 1 {
			prev = "flow_input.seed"
		}
		logs, reads := k%4, k%3
		if s.bare {
			logs, reads = 0, 0
		}
		fmt.Fprintf(&b, "    - id: s%d\n      value:\n        type: script\n        path: f/bench/work\n        input_transforms:\n", k)
		fmt.Fprintf(&b, "          prev: {type: javascript, expr: %s}\n", prev)
		fmt.Fprintf(&b, "          n: {type: static, value: %d}\n          size: {type: static, value: %d}\n", k, s.sizes[k%len(s.sizes)])
		fmt.Fprintf(&b, "          logs: {type: static, value: %d}\n          reads: {type: static, value: %d}\n          last: {type: static, value: %t}\n", logs, reads, k == s.steps)
		if !s.bare {
			fmt.Fprintf(&b, "          key: {type: javascript, expr: '`${flow_input.tenant}:${flow_input.region}:s%d`'}\n", k)
		}
		if s.paths {
			first, prevn := "{type: static, value: 0}", "{type: static, value: 0}"
			if k > 1 {
				first = "{type: javascript, expr: 'previous_result.data.items[0].id'}"
				prevn = fmt.Sprintf("{type: javascript, expr: 'results.s%d?.n'}", k-1)
			}
			fmt.Fprintf(&b, "          region: {type: javascript, expr: flow_input.cfg.region}\n          first: %s\n          prevn: %s\n", first, prevn)
		}
	}
	return b.String()
}

// resFlow is the flow of a shape with res: the brief's example A.
func (s benchShape) resFlow() string {
	var b strings.Builder
	fmt.Fprintf(&b, "summary: Resource shape %s\nvalue:\n  modules:\n", s.name)
	for k := 1; k <= s.steps; k++ {
		prev, m := "previous_result", fmt.Sprintf("{type: javascript, expr: results.s%d.n}", k-1)
		if k == 1 {
			prev, m = "flow_input.seed", "{type: static, value: 0}"
		}
		fmt.Fprintf(&b, "    - id: s%d\n      value:\n        type: script\n        path: f/bench/resstep\n        input_transforms:\n", k)
		fmt.Fprintf(&b, "          prev: {type: javascript, expr: %s}\n", prev)
		fmt.Fprintf(&b, "          a: {type: static, value: '$res:%s/r%d'}\n          b: {type: static, value: '$res:%s/r%d'}\n", s.res, k%4+1, s.res, (k+1)%4+1)
		fmt.Fprintf(&b, "          key: {type: javascript, expr: '`${flow_input.tenant}:s%d`'}\n", k)
		fmt.Fprintf(&b, "          m: %s\n          n: {type: static, value: %d}\n          last: {type: static, value: %t}\n", m, k, k == s.steps)
	}
	return b.String()
}

// benchResStep is the step of the resource shapes.
const benchResStep = `// The step of the resource shapes: it takes two resources as arguments.
export async function main(prev: unknown, a: { host: string; workspace?: string }, b: { host: string; workspace?: string }, key: string, m: number, n: number, last: boolean) {
  const out: Record<string, unknown> = { n, m, key, hosts: [a.host, b.host], workspace: a.workspace ?? b.workspace ?? null, given: prev === undefined ? 0 : 1 };
  if (last) {
    const c = process.cpuUsage();
    out.cpu = { user: c.user, system: c.system };
  }
  return out;
}
`

// benchCacheStep is the step of f/bench/cachecheck: it takes two
// resources as arguments, reads a resource and a variable through the job
// API, and returns what it saw.
const benchCacheStep = `// Reads resources and a variable as arguments and through the job API.
export async function main(counter: { seen: number }, job: { job: string }) {
  const base = process.env.BASE_URL + "/api/w/" + process.env.WM_WORKSPACE;
  const auth = { headers: { Authorization: "Bearer " + process.env.WM_TOKEN } };
  const r3 = await (await fetch(base + "/resources/get_value_interpolated/f/bench/r3", auth)).json();
  const v1 = await (await fetch(base + "/variables/get_value/f/bench/v1", auth)).json();
  return { seen: counter.seen, job: job.job, own_job: process.env.WM_JOB_ID, r3: r3.host, v1 };
}
`

// benchWork is the step of the shapes. Its result's data is a value of
// about size bytes shaped like bench.Synthetic's, made once per size
// and kept in the module's state (warm), so that the step's own work is
// small next to what the platform does around it.
const benchWork = `// The step of the host-mode shapes of milestone G1.
const made = new Map<number, unknown>();

function data(size: number): unknown {
  let v = made.get(size);
  if (v === undefined) {
    const items: string[] = [];
    let len = 21;
    for (let i = 0; ; i++) {
      const rec = JSON.stringify({ id: i, name: "item " + (100000 + i), note: "line one\nline two", tags: ["alpha", "beta"], price: (i % 1000) + 0.25, active: true, meta: { zone: "eu-1", score: 0.5, ref: null } });
      if (len + rec.length + 1 > size) break;
      items.push(rec);
      len += rec.length + 1;
    }
    v = JSON.parse('{"items":[' + items.join(",") + '],"pad":"' + "x".repeat(Math.max(0, size - len - 2)) + '"}');
    made.set(size, v);
  }
  return v;
}

export async function main(prev: unknown, n: number, size: number, logs: number, reads: number, last: boolean, key?: string) {
  for (let i = 0; i < logs; i++) console.log("step " + n + ": line " + (i + 1) + " of " + logs);
  const base = process.env.BASE_URL;
  const ws = process.env.WM_WORKSPACE;
  for (let i = 0; i < reads; i++) {
    const url = i % 2 === 0 ? base + "/api/w/" + ws + "/variables/get_value/f/bench/token" : base + "/api/w/" + ws + "/resources/get_value_interpolated/f/bench/config";
    const r = await fetch(url, { headers: { Authorization: "Bearer " + process.env.WM_TOKEN } });
    if (!r.ok) throw new Error("read " + url + ": " + r.status);
    await r.json();
  }
  const out: Record<string, unknown> = { n, key: key ?? null, given: prev === undefined ? 0 : 1, data: data(size) };
  if (last) {
    const c = process.cpuUsage();
    out.cpu = { user: c.user, system: c.system };
  }
  return out;
}
`

// The host's platform data the shapes read.
var (
	benchVariables = map[string]wl.Variable{"f/bench/token": {Value: "synthetic-token-0123456789abcdef"}}
	benchResources = map[string]wl.Resource{"f/bench/config": {Type: "c_bench_config", Value: json.RawMessage(`{"endpoint":"https://bench.example.invalid","retries":3,"region":"eu-1"}`)}}
)

// benchWorkdirs holds each host-mode benchmark runtime's working
// directory, the host's, for the tests that look into it.
var benchWorkdirs sync.Map

var (
	benchOnce   sync.Once
	benchDir    string
	benchBundle string
	benchHost   string
	benchErr    error
)

// benchSetup builds the benchmarks' bundle and compiles their host, once
// per test binary, and returns the pinned Bun. Without WL_BUN a benchmark
// fails and a test skips, unless WL_REQUIRE_BUN is set.
func benchSetup(b testing.TB) string {
	b.Helper()
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		// A benchmark run asks for figures: a skip, which go test reports
		// as a pass with no rows, would hide that it measured nothing.
		if _, benchmark := b.(*testing.B); benchmark || os.Getenv("WL_REQUIRE_BUN") != "" {
			b.Fatal("WL_BUN is not set: export it, naming the pinned Bun 1.3.10")
		}
		b.Skip("WL_BUN is not set; it names the pinned Bun 1.3.10")
	}
	benchOnce.Do(func() {
		if benchDir, benchErr = os.MkdirTemp("", "wl-bench-"); benchErr != nil {
			return
		}
		folder := filepath.Join(benchDir, "folder")
		for name, body := range benchFolder() {
			p := filepath.Join(folder, filepath.FromSlash(name))
			if benchErr = os.MkdirAll(filepath.Dir(p), 0o755); benchErr != nil {
				return
			}
			if benchErr = os.WriteFile(p, []byte(body), 0o644); benchErr != nil {
				return
			}
		}
		repo, err := filepath.Abs("../..")
		if err != nil {
			benchErr = err
			return
		}
		benchBundle = filepath.Join(benchDir, "bundle")
		if _, benchErr = bundle.Build(context.Background(), bundle.Config{
			Folder:         folder,
			EngineVersion:  resolveVersion(),
			Bun:            bun,
			Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
			HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
			Out:            benchBundle,
			Work:           filepath.Join(benchDir, "work"),
		}); benchErr != nil {
			return
		}
		benchHost = filepath.Join(benchDir, "benchhost")
		benchErr = compileHost(bun, "testdata/benchhost/host.ts", benchHost)
	})
	if benchErr != nil {
		b.Fatal(benchErr)
	}
	return bun
}

// benchRuntime opens the bundle on one topology, closed when b ends.
// WL_BENCH_READ_CACHE=on turns the read cache on (wl.Options.ReadCache).
func benchRuntime(b testing.TB, bun, topology string) *wl.Runtime {
	return benchRuntimeWith(b, bun, topology, func(o *wl.Options) { o.ReadCache = os.Getenv("WL_BENCH_READ_CACHE") == "on" })
}

// benchRuntimeWith is benchRuntime with the options edit sets.
func benchRuntimeWith(b testing.TB, bun, topology string, edit func(*wl.Options)) *wl.Runtime {
	b.Helper()
	// A short directory: Unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "wl-b-")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	diag, err := os.Create(filepath.Join(dir, "diag.log"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		diag.Close()
		if b.Failed() {
			log, _ := os.ReadFile(diag.Name())
			b.Logf("diagnostics:\n%s", log)
		}
	})
	opts := wl.Options{
		EngineVersion: resolveVersion(),
		Diagnostics:   diag,
		Variables:     benchVariables,
		Resources:     benchResources,
		DataDir:       filepath.Join(dir, "data"),
	}
	switch topology {
	case "subprocess":
		repo, err := filepath.Abs("../..")
		if err != nil {
			b.Fatal(err)
		}
		opts.Bun, opts.HarnessRuntime, opts.ModuleState = bun, filepath.Join(repo, "harnesses", "bun", "runtime"), wl.ModuleStateWarm
	case "host":
		opts.Host = benchHostConn(b, dir, diag)
	case "host-plugin":
		opts.Host = benchHostConn(b, dir, diag, "WL_BENCH_PLUGIN=1")
	default:
		b.Fatalf("topology %q", topology)
	}
	edit(&opts)
	rt, err := wl.Open(benchBundle, opts)
	if err != nil {
		b.Fatalf("wl.Open (%s): %v", topology, err)
	}
	if opts.Host != nil {
		benchWorkdirs.Store(rt, opts.Host.Workdir)
	}
	b.Cleanup(func() { rt.Close() })
	return rt
}

// benchHostConn starts the benchmark host with its working directory in
// dir and returns the host connection its engine proxy handed over.
func benchHostConn(b testing.TB, dir string, diag *os.File, env ...string) *wl.Host {
	b.Helper()
	workdir := filepath.Join(dir, "host")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		b.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		b.Fatal(err)
	}
	sock := filepath.Join(dir, "e.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	cmd := exec.Command(benchHost)
	cmd.Dir = workdir
	cmd.Env = append(append(os.Environ(), "WL_ENGINE="+self, "WL_BUNDLE="+benchBundle, benchProxyEnv+"="+sock), env...)
	cmd.Stdout, cmd.Stderr = diag, diag
	if err := cmd.Start(); err != nil {
		b.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	conns, err := acceptBenchProxy(ln, exited)
	if err != nil {
		cmd.Process.Kill()
		<-exited
		log, _ := os.ReadFile(diag.Name())
		b.Fatalf("the benchmark host's engine proxy: %v\n%s", err, log)
	}
	// Registered before the runtime's Close, so it runs after it: the host
	// exits once its engine connection ends.
	b.Cleanup(func() {
		for _, c := range conns {
			c.Close()
		}
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-exited
		}
	})
	h := &wl.Host{Conn: conns[1], Workdir: workdir, KillAfter: -1}
	if len(conns) > 2 {
		h.Children = conns[2]
	}
	return h
}

// acceptBenchProxy waits for the engine proxy and returns its control
// connection, then the host's socket and, when it passed one, the child
// channel.
func acceptBenchProxy(ln net.Listener, exited <-chan struct{}) ([]net.Conn, error) {
	type accepted struct {
		c   net.Conn
		err error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		ch <- accepted{c, err}
	}()
	var a accepted
	select {
	case a = <-ch:
	case <-exited:
		return nil, errors.New("the host exited before its engine started")
	case <-time.After(30 * time.Second):
		return nil, errors.New("no engine proxy within 30s")
	}
	if a.err != nil {
		return nil, a.err
	}
	uc := a.c.(*net.UnixConn)
	_ = uc.SetReadDeadline(time.Now().Add(30 * time.Second))
	buf, oob := make([]byte, 1), make([]byte, syscall.CmsgSpace(2*4))
	n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	_ = uc.SetReadDeadline(time.Time{})
	if err == nil && n != 1 {
		err = errors.New("the engine proxy sent nothing")
	}
	var fds []int
	if err == nil {
		var msgs []syscall.SocketControlMessage
		if msgs, err = syscall.ParseSocketControlMessage(oob[:oobn]); err == nil && len(msgs) == 1 {
			fds, err = syscall.ParseUnixRights(&msgs[0])
		}
	}
	if err == nil && (len(fds) == 0 || len(fds) != int(buf[0])) {
		err = fmt.Errorf("the engine proxy passed %d file descriptors, not %d", len(fds), buf[0])
	}
	if err != nil {
		uc.Close()
		return nil, err
	}
	conns := []net.Conn{uc}
	for _, fd := range fds {
		f := os.NewFile(uintptr(fd), "host")
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			for _, c := range conns {
				c.Close()
			}
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, nil
}

// engineCPU is this process's CPU time so far, the engine's in these
// benchmarks: user and system time together.
func engineCPU() time.Duration {
	user, sys := engineCPUParts()
	return user + sys
}

// engineCPUParts is this process's user and system CPU time so far.
func engineCPUParts() (user, sys time.Duration) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, 0
	}
	return time.Duration(ru.Utime.Nano()), time.Duration(ru.Stime.Nano())
}

// cpuOf reads the CPU time a step reported ({"user":µs,"system":µs}).
func cpuOf(raw json.RawMessage) (time.Duration, error) {
	var c struct{ User, System int64 }
	if err := json.Unmarshal(raw, &c); err != nil {
		return 0, err
	}
	return time.Duration(c.User+c.System) * time.Microsecond, nil
}

// loadAverage is the one-minute load average, or -1 when it cannot be
// read.
func loadAverage() float64 {
	var text string
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		text = string(b)
	} else if b, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
		text = strings.Trim(strings.TrimSpace(string(b)), "{} ")
	}
	if f := strings.Fields(text); len(f) > 0 {
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			return v
		}
	}
	return -1
}

// probed is what the probe step read in the harness's (or host's)
// process: its CPU time so far, the benchmark timings it drained, its peak
// resident set in bytes, and a plugin host's count of its provider hooks'
// calls by "<run> <path>".
type probed struct {
	cpu     time.Duration
	timings []bunTiming
	rssPeak int64
	calls   map[string]int
}

// probe runs the probe step, which leaves the timings' sink set when
// timings is true.
func probe(tb testing.TB, rt *wl.Runtime, timings bool) probed {
	tb.Helper()
	res, err := rt.Run(context.Background(), "f/bench/probe", fmt.Appendf(nil, `{"timings":%t}`, timings), wl.RunOptions{})
	if err != nil || res.Status != wl.StatusSuccess {
		tb.Fatalf("the probe: %+v, %v", res, err)
	}
	var out struct {
		Timings []bunTiming    `json:"timings"`
		RSSPeak int64          `json:"rss_peak"`
		Calls   map[string]int `json:"calls"`
	}
	d, err := cpuOf(res.Result)
	if err == nil {
		err = json.Unmarshal(res.Result, &out)
	}
	if err != nil {
		tb.Fatalf("the probe's result %.200s: %v", res.Result, err)
	}
	return probed{cpu: d, timings: out.Timings, rssPeak: out.RSSPeak, calls: out.Calls}
}

// bunTiming is one invocation's readings in the harness, in nanoseconds
// of Bun.nanoseconds() (harnesses/bun README, Benchmark timings).
type bunTiming struct {
	ID   string  `json:"id"`
	Rx   float64 `json:"rx"`
	Dec  float64 `json:"dec"`
	Call float64 `json:"call"`
	Ret  float64 `json:"ret"`
	Ser  float64 `json:"ser"`
	Sent float64 `json:"sent"`
}

// stages are the parts of a step, in the order an invocation goes
// through them. Each is a difference of two readings of the same
// invocation, so a step's stages add up to its module span.
var stages = []struct{ name, meaning string }{
	{"engine", "the engine outside its call of the executor: transforms, arguments and their storage, the trace (module span less harness span)"},
	{"executor", "the executor around the harness process: the runtime's recorder and the pool's routing (harness span less Process's own time)"},
	{"go-encode", "checks, the script's arguments and the invoke's payload"},
	{"args-file", "args.json written, its lock's wait included"},
	{"go-queue", "the invocation registered and its invoke queued, the process lock's wait included"},
	{"transport", "from the invoke queued to the result read, less the harness's time: the writer, the socket both ways and the wake-ups, unattributed"},
	{"bun-decode", "the invoke's last frame decoded in the harness"},
	{"bun-dispatch", "from decoded to the entrypoint called: context, metadata, module and binding, and any wait behind other invocations"},
	{"code", "the step's own code"},
	{"bun-serialise", "the result serialised (resultText)"},
	{"bun-send", "the result frame encoded and handed to the socket"},
	{"go-decode", "the result's last frame decoded in the engine"},
	{"go-handoff", "from decoded to the outcome back with the engine: settlement and the Reporter's calls"},
}

// stageCode is the index in stages of the step's own code.
const stageCode = 8

// stepSplit is one step's stages, by the index of stages.
type stepSplit struct {
	module, harness time.Duration
	stage           []time.Duration
}

// runRecord is what one run left for the split: its steps' spans and job
// ids, its expression time and its own elapsed time.
type runRecord struct {
	id      string
	steps   []benchStep
	expr    time.Duration
	elapsed time.Duration
}

type benchStep struct {
	job             string
	module, harness time.Duration
}

// recordRun reads a run's trace: each step's module and harness spans,
// matched by site, and its job id, the invocation's id.
func recordRun(tr *wl.Trace, want int, elapsed time.Duration) (runRecord, error) {
	spans := map[string]*benchStep{}
	var expr time.Duration
	for _, sp := range tr.Spans {
		switch sp.Kind {
		case engine.SpanModule, engine.SpanHarness:
			s := spans[sp.Site.String()]
			if s == nil {
				s = &benchStep{}
				spans[sp.Site.String()] = s
			}
			if sp.Kind == engine.SpanModule {
				s.module += sp.Duration
			} else {
				s.harness += sp.Duration
			}
		case engine.SpanExpression:
			expr += sp.Duration
		}
	}
	rec := runRecord{id: tr.RunID, expr: expr, elapsed: elapsed}
	for _, st := range tr.Steps {
		s := spans[st.Site.String()]
		if s == nil || st.Job == "" {
			continue
		}
		s.job = st.Job
		rec.steps = append(rec.steps, *s)
	}
	if len(rec.steps) != want || len(spans) != want {
		return rec, fmt.Errorf("%d steps with a job and %d sites with spans in the trace, %d wanted", len(rec.steps), len(spans), want)
	}
	return rec, nil
}

// split matches each step of the runs with the engine side's and the
// harness's readings of its invocation and returns its stages. Every step
// must have both, unless timed is false, when it returns none.
func split(runs []runRecord, goTimes map[string]harness.InvocationTimes, bunTimes []bunTiming, timed bool) ([]stepSplit, error) {
	if !timed {
		return nil, nil
	}
	bun := make(map[string]bunTiming, len(bunTimes))
	for _, t := range bunTimes {
		bun[t.ID] = t
	}
	ns := func(a, b float64) time.Duration { return time.Duration(b - a) }
	var out []stepSplit
	for _, r := range runs {
		for _, s := range r.steps {
			g, okG := goTimes[s.job]
			h, okB := bun[s.job]
			if !okG || !okB || h.Call == 0 || h.Ret == 0 || h.Ser == 0 {
				return nil, fmt.Errorf("invocation %s: engine readings %t, harness readings %+v", s.job, okG, h)
			}
			inHarness := ns(h.Rx, h.Sent)
			st := []time.Duration{
				s.module - s.harness,
				s.harness - g.Done.Sub(g.Start),
				g.Encoded.Sub(g.Start),
				g.ArgsWritten.Sub(g.Encoded),
				g.Queued.Sub(g.ArgsWritten),
				g.Received.Sub(g.Queued) - inHarness,
				ns(h.Rx, h.Dec),
				ns(h.Dec, h.Call),
				ns(h.Call, h.Ret),
				ns(h.Ret, h.Ser),
				ns(h.Ser, h.Sent),
				g.Decoded.Sub(g.Received),
				g.Done.Sub(g.Decoded),
			}
			out = append(out, stepSplit{module: s.module, harness: s.harness, stage: st})
		}
	}
	return out, nil
}

// benchCase is one benchmark's runs.
type benchCase struct {
	path  string
	args  json.RawMessage
	steps int
	conc  int
	warm  int
	check func(*wl.Result) error
	// hostCPU reads the host's CPU time that a run's result reports, for
	// each run's own figure when runs go one at a time; nil for none.
	hostCPU func(*wl.Result) (time.Duration, error)
	// bytes is the payload a step moves, both ways, for MB/s; 0 for none.
	bytes int
}

// benchResult is what measure measured.
type benchResult struct {
	conc      bench.Concurrency
	runs      []runRecord
	split     []stepSplit
	engineCPU time.Duration
	engineSys time.Duration
	hostCPU   time.Duration
	runEngine []time.Duration // per run and step, one run at a time only
	runHost   []time.Duration // the same
	load      float64
	// rssPeak is the host's or harness's peak resident set after the
	// runs, in bytes, and calls the plugin's provider calls the measured
	// runs made.
	rssPeak int64
	calls   int
}

// stepCount is the number of steps the measured runs ran.
func (r *benchResult) stepCount() int {
	n := 0
	for _, run := range r.runs {
		n += len(run.steps)
	}
	return n
}

// benchTimed reports whether the stage readings are on (WL_BENCH_TIMINGS
// is not off).
func benchTimed() bool { return os.Getenv("WL_BENCH_TIMINGS") != "off" }

// measure runs c on rt: c.warm runs first, then ops runs on each of c.conc
// workers started together, between two probes, with the stage readings
// on unless WL_BENCH_TIMINGS is off. timer, when not nil, is stopped
// outside the measured runs.
func measure(tb testing.TB, rt *wl.Runtime, c benchCase, ops int, timer interface {
	StartTimer()
	StopTimer()
}) benchResult {
	tb.Helper()
	ctx := context.Background()
	run := func() (*wl.Result, time.Duration, error) {
		t0 := time.Now()
		res, err := rt.Run(ctx, c.path, c.args, wl.RunOptions{})
		elapsed := time.Since(t0)
		if err == nil && res.Status != wl.StatusSuccess {
			err = fmt.Errorf("run %s: %s: %.300s", c.path, res.Status, res.Result)
		}
		if err == nil {
			err = c.check(res)
		}
		return res, elapsed, err
	}
	for range c.warm {
		if _, _, err := run(); err != nil {
			tb.Fatal(err)
		}
	}
	runtime.GC()
	timed := benchTimed()
	out := benchResult{load: loadAverage()}
	hostBefore := probe(tb, rt, timed).cpu
	hostLast := hostBefore
	var (
		mu      sync.Mutex
		goTimes = map[string]harness.InvocationTimes{}
	)
	if timed {
		harness.SetTimesHook(func(t harness.InvocationTimes) {
			mu.Lock()
			goTimes[t.ID] = t
			mu.Unlock()
		})
	}
	perRun := c.conc == 1
	userBefore, sysBefore := engineCPUParts()
	if timer != nil {
		timer.StartTimer()
	}
	conc, err := bench.Workers(c.conc, ops, func(int) error {
		e0 := engineCPU()
		res, elapsed, err := run()
		e1 := engineCPU()
		if err != nil {
			return err
		}
		rec, err := recordRun(res.Trace, c.steps, elapsed)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		out.runs = append(out.runs, rec)
		if perRun {
			out.runEngine = append(out.runEngine, (e1-e0)/time.Duration(c.steps))
			if c.hostCPU != nil {
				host, err := c.hostCPU(res)
				if err != nil {
					return err
				}
				out.runHost = append(out.runHost, (host-hostLast)/time.Duration(c.steps))
				hostLast = host
			}
		}
		return nil
	})
	if timer != nil {
		timer.StopTimer()
	}
	user, sys := engineCPUParts()
	harness.SetTimesHook(nil)
	if err != nil {
		tb.Fatal(err)
	}
	after := probe(tb, rt, false)
	out.conc = conc
	out.engineCPU, out.engineSys, out.hostCPU = user-userBefore+sys-sysBefore, sys-sysBefore, after.cpu-hostBefore
	out.rssPeak = after.rssPeak
	mu.Lock()
	defer mu.Unlock()
	measured := map[string]bool{}
	for _, run := range out.runs {
		measured[run.id] = true
	}
	for key, n := range after.calls {
		if run, _, _ := strings.Cut(key, " "); measured[run] {
			out.calls += n
		}
	}
	if out.split, err = split(out.runs, goTimes, after.timings, timed); err != nil {
		tb.Fatal(err)
	}
	return out
}

// shares returns, for each run, the share of its module time spent at the
// boundary (its steps' harness spans less their own code) and in the
// harness (its harness spans), and the share of its elapsed time in its
// modules; then the same over every run's steps together.
func (r *benchResult) shares() (boundary, harnessShare, modules []float64, pooledBoundary, pooledHarness float64) {
	var i int
	var allModule, allHarness, allStep time.Duration
	for _, run := range r.runs {
		var module, harness, step time.Duration
		for range run.steps {
			if r.split != nil {
				s := r.split[i]
				module, harness, step = module+s.module, harness+s.harness, step+s.stage[stageCode]
			}
			i++
		}
		if r.split == nil {
			for _, s := range run.steps {
				module, harness = module+s.module, harness+s.harness
			}
		}
		if module > 0 {
			boundary = append(boundary, float64(harness-step)/float64(module))
			harnessShare = append(harnessShare, float64(harness)/float64(module))
		}
		if run.elapsed > 0 {
			modules = append(modules, float64(module)/float64(run.elapsed))
		}
		allModule, allHarness, allStep = allModule+module, allHarness+harness, allStep+step
	}
	if allModule > 0 {
		pooledBoundary, pooledHarness = float64(allHarness-allStep)/float64(allModule), float64(allHarness)/float64(allModule)
	}
	return
}

// fractionAt returns the nearest-rank q-quantile of fs, with the
// convention of bench.Percentile.
func fractionAt(fs []float64, q float64) float64 {
	ds := make([]time.Duration, len(fs))
	for i, f := range fs {
		ds[i] = time.Duration(f * 1e9)
	}
	return float64(bench.Percentile(ds, q)) / 1e9
}

// report reports r's figures per step on b (the metrics listed at the top
// of this file).
func (r *benchResult) report(b *testing.B, c benchCase) {
	steps := float64(r.stepCount())
	var module, harnessSpan []time.Duration
	for _, run := range r.runs {
		for _, s := range run.steps {
			module, harnessSpan = append(module, s.module), append(harnessSpan, s.harness)
		}
	}
	b.ReportMetric(float64(r.conc.Wall)/steps, "ns/op")
	if c.bytes > 0 {
		b.ReportMetric(steps*float64(c.bytes)/r.conc.Wall.Seconds()/1e6, "MB/s")
	}
	b.ReportMetric(float64(bench.Percentile(module, 0.50)), "step-p50-ns")
	b.ReportMetric(float64(bench.Percentile(module, 0.95)), "step-p95-ns")
	b.ReportMetric(float64(bench.Mean(module)), "step-mean-ns")
	b.ReportMetric(float64(bench.Percentile(harnessSpan, 0.50)), "harness-p50-ns")
	for k, st := range stages {
		if r.split == nil {
			break
		}
		ds := make([]time.Duration, len(r.split))
		for i, s := range r.split {
			ds[i] = s.stage[k]
		}
		b.ReportMetric(float64(bench.Mean(ds)), st.name+"-mean-ns")
		b.ReportMetric(float64(bench.Percentile(ds, 0.50)), st.name+"-p50-ns")
	}
	boundary, harnessShare, modules, pooledBoundary, pooledHarness := r.shares()
	if r.split != nil {
		b.ReportMetric(fractionAt(boundary, 0.50), "boundary-share-p50")
		b.ReportMetric(pooledBoundary, "boundary-share")
	}
	b.ReportMetric(fractionAt(harnessShare, 0.50), "harness-share-p50")
	b.ReportMetric(pooledHarness, "harness-share")
	b.ReportMetric(fractionAt(modules, 0.50), "module-share-p50")
	var expr time.Duration
	for _, run := range r.runs {
		expr += run.expr
	}
	b.ReportMetric(float64(expr)/steps, "expr-ns/step")
	b.ReportMetric(float64(r.engineCPU)/steps, "engine-cpu-ns/step")
	b.ReportMetric(float64(r.engineSys)/steps, "engine-sys-ns/step")
	b.ReportMetric(float64(r.hostCPU)/steps, "host-cpu-ns/step")
	if len(r.runEngine) > 0 {
		b.ReportMetric(float64(bench.Percentile(r.runEngine, 0.50)), "engine-cpu-p50-ns/step")
		b.ReportMetric(float64(bench.Percentile(r.runEngine, 0.95)), "engine-cpu-p95-ns/step")
	}
	if len(r.runHost) > 0 {
		b.ReportMetric(float64(bench.Percentile(r.runHost, 0.50)), "host-cpu-p50-ns/step")
		b.ReportMetric(float64(bench.Percentile(r.runHost, 0.95)), "host-cpu-p95-ns/step")
	}
	if r.calls > 0 {
		b.ReportMetric(float64(r.calls)/float64(len(r.runs)), "host-calls/run")
	}
	b.ReportMetric(float64(r.rssPeak)/(1<<20), "host-peak-rss-mb")
	b.ReportMetric(float64(r.conc.Peak), "conc-peak")
	b.ReportMetric(r.conc.Mean, "conc-mean")
	b.ReportMetric(r.load, "load1")
}

// chainCase is f/bench/chain with the payload of size n, conc at a time.
func chainCase(n, conc int) benchCase {
	return benchCase{
		path: "f/bench/chain", args: fmt.Appendf(nil, `{"p":%s}`, bench.Synthetic(n)),
		steps: benchChainSteps, conc: conc, warm: 3, bytes: 2 * n,
		check: func(res *wl.Result) error {
			if len(res.Result) != n {
				return fmt.Errorf("the chain's result has %d bytes, not %d", len(res.Result), n)
			}
			return nil
		},
	}
}

func BenchmarkStepBoundary(b *testing.B) {
	bun := benchSetup(b)
	for _, topology := range []string{"host", "subprocess"} {
		b.Run("topology="+topology, func(b *testing.B) {
			rt := benchRuntime(b, bun, topology)
			for _, conc := range []int{1, 8} {
				for _, n := range bench.SyntheticSizes {
					b.Run(fmt.Sprintf("conc=%d/size=%s", conc, bench.SizeLabel(n)), func(b *testing.B) {
						c := chainCase(n, conc)
						b.StopTimer()
						r := measure(b, rt, c, b.N, b)
						r.report(b, c)
					})
				}
			}
		})
	}
}

// cleanupOn is a sub-benchmark whose cleanups run when its parent ends,
// for a runtime the parent's later sub-benchmarks share; its failures
// stay its own.
type cleanupOn struct {
	testing.TB
	parent testing.TB
}

func (c cleanupOn) Cleanup(f func()) { c.parent.Cleanup(f) }

// shapeArgs is a shape's flow input: a tenant, a region and a seed of
// its first step's size, and for a paths shape the object its first
// member path reads.
func shapeArgs(s benchShape) json.RawMessage {
	if s.paths {
		return fmt.Appendf(nil, `{"tenant":"tenant-a","region":"eu-1","cfg":{"region":"eu-1","tier":2},"seed":%s}`, bench.Synthetic(s.sizes[0]))
	}
	return fmt.Appendf(nil, `{"tenant":"tenant-a","region":"eu-1","seed":%s}`, bench.Synthetic(s.sizes[0]))
}

// shapeCase is the shape's runs.
func shapeCase(s benchShape) benchCase {
	if s.plain {
		return benchCase{
			path:  s.path(),
			args:  shapeArgs(s),
			steps: s.steps, conc: s.conc, warm: 1,
			check: func(res *wl.Result) error {
				var out struct {
					N int `json:"n"`
				}
				if err := json.Unmarshal(res.Result, &out); err != nil || out.N != s.steps {
					return fmt.Errorf("shape %s: the result is not its last step's: %.200s (%v)", s.name, res.Result, err)
				}
				return nil
			},
		}
	}
	return benchCase{
		path:  s.path(),
		args:  shapeArgs(s),
		steps: s.steps, conc: s.conc, warm: 1,
		check: func(res *wl.Result) error {
			var out struct {
				N   int             `json:"n"`
				CPU json.RawMessage `json:"cpu"`
			}
			if err := json.Unmarshal(res.Result, &out); err != nil || out.N != s.steps || out.CPU == nil {
				return fmt.Errorf("shape %s: the result is not its last step's: %.200s (%v)", s.name, res.Result, err)
			}
			return nil
		},
		hostCPU: func(res *wl.Result) (time.Duration, error) {
			var out struct{ CPU json.RawMessage }
			if err := json.Unmarshal(res.Result, &out); err != nil {
				return 0, err
			}
			return cpuOf(out.CPU)
		},
	}
}

func BenchmarkHostShapes(b *testing.B) {
	bun := benchSetup(b)
	// Each topology's runtime opens when its first shape runs, so a shape
	// run alone (-bench) has its host to itself.
	runtimes, parent := map[string]*wl.Runtime{}, b
	for _, s := range benchShapes {
		b.Run("shape="+s.name, func(b *testing.B) {
			topology := "host"
			if s.plugin() {
				topology = "host-plugin"
			}
			rt := runtimes[topology]
			if rt == nil {
				rt = benchRuntime(cleanupOn{TB: b, parent: parent}, bun, topology)
				runtimes[topology] = rt
			}
			c := shapeCase(s)
			b.StopTimer()
			r := measure(b, rt, c, b.N, b)
			r.report(b, c)
		})
	}
}

// TestBenchSplit runs the benchmarks' measurement once on each topology,
// with two runs at once, so that a broken split fails the ordinary tests:
// both runs were in flight together, every step was matched with both
// sides' readings of its invocation, its stages add up to its module span
// exactly, and the shares are fractions. It needs the pinned Bun.
func TestBenchSplit(t *testing.T) {
	if !benchTimed() {
		t.Skip("WL_BENCH_TIMINGS is off")
	}
	// A stage's metrics must not take the names of the step's own
	// (report), which ReportMetric would overwrite.
	if stages[stageCode].name != "code" {
		t.Fatalf("stage %d is %q, not the step's own code", stageCode, stages[stageCode].name)
	}
	for _, st := range stages {
		if st.name == "step" || st.name == "harness" {
			t.Fatalf("stage %q shadows the step's own metrics", st.name)
		}
	}
	bun := benchSetup(t)
	for _, topology := range []string{"host", "subprocess"} {
		t.Run(topology, func(t *testing.T) {
			rt := benchRuntime(t, bun, topology)
			c := chainCase(1<<10, 2)
			c.warm = 1
			r := measure(t, rt, c, 1, nil)
			if r.conc.Peak != 2 || len(r.runs) != 2 || len(r.split) != 2*benchChainSteps {
				t.Fatalf("peak %d, %d runs, %d steps split", r.conc.Peak, len(r.runs), len(r.split))
			}
			for _, s := range r.split {
				var sum time.Duration
				for k, d := range s.stage {
					// The transport is a difference of two processes' clocks
					// and may read a little below zero; the others cannot.
					if d < 0 && stages[k].name != "transport" {
						t.Fatalf("stage %s is %v", stages[k].name, d)
					}
					sum += d
				}
				if sum != s.module {
					t.Fatalf("the stages add up to %v, the module span is %v", sum, s.module)
				}
			}
			boundary, harnessShare, modules, pooled, _ := r.shares()
			for _, f := range slices.Concat(boundary, harnessShare, modules, []float64{pooled}) {
				if f <= 0 || f > 1 {
					t.Fatalf("a share of %v (boundary %v, harness %v, modules %v)", f, boundary, harnessShare, modules)
				}
			}
			if r.hostCPU <= 0 || r.engineCPU <= 0 {
				t.Fatalf("CPU: engine %v, host %v", r.engineCPU, r.hostCPU)
			}
		})
	}
}

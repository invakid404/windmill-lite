package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
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

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/wl"
)

// The end-to-end benchmarks of milestone G1 (ADR 0005's benchmark plan,
// ADR 0012 row G1) run synthetic flows on the real topologies, with the
// engine in this process, so that they read each run's trace and the
// engine's CPU time:
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
// The runs are submitted with Runtime.Run. Each benchmark reports, per
// step: the time per step at its concurrency (ns/op); the step's elapsed
// time, its module span in the trace, at p50 and p95; the harness span,
// the engine's Execute of the step, at p50 (the round trip to the harness
// and back, with the engine's invocation codec and args.json); the
// engine's handling outside it (module span minus harness span) at p50;
// the engine's CPU time (this process's, getrusage) and the harness's or
// host's (process.cpuUsage(), read by the probe flow f/bench/cpu before
// and after); and the one-minute load average when it started (load1),
// since another workload on the machine moves every figure.
//
//   - BenchmarkStepBoundary: item 2 of ADR 0005's plan, the per-step
//     boundary cost for the payloads of jsonval.Synthetic from 26 B to
//     4 MiB, one run at a time and eight at once: f/bench/chain, eight
//     steps that each return their argument, the first taking the flow
//     input's member p and the others previous_result, both shortcuts that
//     run no expression (JE-6, JE-7). internal/harness's benchmarks
//     measure the parts of the harness span (the transport, the harness's
//     parse and serialise, the engine's invocation codec, args.json).
//   - BenchmarkHostShapes: the shapes a host-mode embedder runs, in host
//     mode: 50 to 600 steps per run, arguments and results of 1 to 50 KiB,
//     0 to 3 log lines and 0 to 2 variable or resource reads through the
//     platform facade per step, and an expression that binds the flow
//     input in every step. Each run's engine and host CPU per step are
//     reported at p50 and p95 over the runs, besides their mean.
//
// They need the pinned Bun in WL_BUN and the build helper's dependencies,
// and run only with -bench; the command and the recorded baseline are in
// ADR 0005.

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
type benchShape struct {
	name  string
	steps int
	sizes []int
	bare  bool
	conc  int
}

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
}

// benchMixed are the result sizes of a mixed shape's steps, in turn.
var benchMixed = []int{1 << 10, 5 << 10, 20 << 10, 50 << 10}

func (s benchShape) path() string {
	if s.bare {
		return fmt.Sprintf("f/bench/bare_s%d_%s", s.steps, sizesName(s.sizes))
	}
	return fmt.Sprintf("f/bench/shape_s%d_%s", s.steps, sizesName(s.sizes))
}

func sizesName(sizes []int) string {
	if len(sizes) > 1 {
		return "mixed"
	}
	return strings.ToLower(jsonval.SizeLabel(sizes[0]))
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
		"f/bench/cpu.ts": `// The CPU time of the process the step runs in, in microseconds: the
// host's in host mode, the harness's otherwise.
export async function main() {
  const c = process.cpuUsage();
  return { user: c.user, system: c.system };
}
`,
		"f/bench/work.ts":            benchWork,
		"f/bench/cpu.flow/flow.yaml": "summary: The CPU probe\nvalue:\n  modules:\n    - id: a\n      value: {type: script, path: f/bench/cpu, input_transforms: {}}\n",
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
	for _, s := range benchShapes {
		files[s.path()+".flow/flow.yaml"] = s.flow()
	}
	maps.Copy(files, studyFolder())
	return files
}

// flow is the shape's flow: step k (from 1) takes the previous result
// (the flow input's seed for the first), its size, log lines and reads as
// static values, and, unless bare, a key from an expression that binds
// the flow input; the last step reports the host's CPU time with its
// result.
func (s benchShape) flow() string {
	var b strings.Builder
	fmt.Fprintf(&b, "summary: G1 shape %s\nvalue:\n  modules:\n", s.name)
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
	}
	return b.String()
}

// benchWork is the step of the shapes. Its result's data is a value of
// about size bytes shaped like jsonval.Synthetic's, made once per size
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

var (
	benchOnce   sync.Once
	benchDir    string
	benchBundle string
	benchHost   string
	benchErr    error
)

// benchSetup builds the benchmarks' bundle and compiles their host, once
// per test binary, and returns the pinned Bun.
func benchSetup(b *testing.B) string {
	b.Helper()
	bun := os.Getenv("WL_BUN")
	if bun == "" {
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
func benchRuntime(b *testing.B, bun, topology string) *wl.Runtime {
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
	default:
		b.Fatalf("topology %q", topology)
	}
	rt, err := wl.Open(benchBundle, opts)
	if err != nil {
		b.Fatalf("wl.Open (%s): %v", topology, err)
	}
	b.Cleanup(func() { rt.Close() })
	return rt
}

// benchHostConn starts the benchmark host with its working directory in
// dir and returns the host connection its engine proxy handed over.
func benchHostConn(b *testing.B, dir string, diag *os.File) *wl.Host {
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
	cmd.Env = append(os.Environ(), "WL_ENGINE="+self, "WL_BUNDLE="+benchBundle, benchProxyEnv+"="+sock)
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

// harnessCPU runs the CPU probe and returns the harness's (or host's) CPU
// time so far.
func harnessCPU(b *testing.B, rt *wl.Runtime) time.Duration {
	b.Helper()
	res, err := rt.Run(context.Background(), "f/bench/cpu", json.RawMessage(`{}`), wl.RunOptions{})
	if err != nil || res.Status != wl.StatusSuccess {
		b.Fatalf("the CPU probe: %+v, %v", res, err)
	}
	d, err := cpuOf(res.Result)
	if err != nil {
		b.Fatalf("the CPU probe's result %s: %v", res.Result, err)
	}
	return d
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

// stepTimes are one step's spans.
type stepTimes struct {
	module, harness time.Duration
}

// steps returns the spans of each step of a run's trace, by site, and the
// run's expression time.
func steps(tr *wl.Trace) (map[string]*stepTimes, time.Duration) {
	out := map[string]*stepTimes{}
	var expr time.Duration
	at := func(site string) *stepTimes {
		s := out[site]
		if s == nil {
			s = &stepTimes{}
			out[site] = s
		}
		return s
	}
	for _, sp := range tr.Spans {
		switch sp.Kind {
		case engine.SpanModule:
			at(sp.Site.String()).module += sp.Duration
		case engine.SpanHarness:
			at(sp.Site.String()).harness += sp.Duration
		case engine.SpanExpression:
			expr += sp.Duration
		}
	}
	return out, expr
}

// pct returns the q-quantile of ds in nanoseconds (nearest rank).
func pct(ds []time.Duration, q float64) float64 {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	return float64(s[min(len(s)-1, int(q*float64(len(s))))])
}

// benchOutcome is what a benchmark's runs measured.
type benchOutcome struct {
	mu        sync.Mutex
	module    []time.Duration
	harness   []time.Duration
	outside   []time.Duration
	expr      time.Duration
	steps     int
	runs      []time.Duration
	runEngine []time.Duration // per run, divided by its steps (sequential only)
	runHost   []time.Duration // the same
}

func (o *benchOutcome) add(tr *wl.Trace, want int) error {
	st, expr := steps(tr)
	if len(st) != want {
		return fmt.Errorf("%d steps in the trace, %d wanted", len(st), want)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, s := range st {
		o.module = append(o.module, s.module)
		o.harness = append(o.harness, s.harness)
		o.outside = append(o.outside, s.module-s.harness)
	}
	o.expr += expr
	o.steps += len(st)
	return nil
}

// report reports the outcome's figures per step. engineSys is the part of
// engineCPU that is system time.
func (o *benchOutcome) report(b *testing.B, wall, engineCPU, engineSys, hostCPU time.Duration, load float64) {
	steps := float64(o.steps)
	b.ReportMetric(float64(wall)/steps, "ns/op")
	b.ReportMetric(pct(o.module, 0.50), "step-p50-ns")
	b.ReportMetric(pct(o.module, 0.95), "step-p95-ns")
	b.ReportMetric(pct(o.harness, 0.50), "harness-p50-ns")
	b.ReportMetric(pct(o.outside, 0.50), "engine-side-p50-ns")
	b.ReportMetric(float64(engineCPU)/steps, "engine-cpu-ns/step")
	b.ReportMetric(float64(engineSys)/steps, "engine-sys-ns/step")
	b.ReportMetric(float64(hostCPU)/steps, "host-cpu-ns/step")
	if o.expr > 0 {
		b.ReportMetric(float64(o.expr)/steps, "expr-ns/step")
	}
	if len(o.runEngine) > 0 {
		b.ReportMetric(pct(o.runEngine, 0.50), "engine-cpu-p50-ns/step")
		b.ReportMetric(pct(o.runEngine, 0.95), "engine-cpu-p95-ns/step")
		b.ReportMetric(pct(o.runHost, 0.50), "host-cpu-p50-ns/step")
		b.ReportMetric(pct(o.runHost, 0.95), "host-cpu-p95-ns/step")
	}
	b.ReportMetric(load, "load1")
}

// benchRuns runs b.N runs of path with args, conc at a time, after a
// warm-up of warm runs, checking each with check, and reports the
// figures per step.
func benchRuns(b *testing.B, rt *wl.Runtime, path string, args json.RawMessage, steps, conc, warm int, check func(*wl.Result) error) {
	ctx := context.Background()
	run := func() (*wl.Result, error) {
		res, err := rt.Run(ctx, path, args, wl.RunOptions{})
		if err == nil && res.Status != wl.StatusSuccess {
			err = fmt.Errorf("run %s: %s: %s", path, res.Status, res.Result)
		}
		if err == nil {
			err = check(res)
		}
		return res, err
	}
	for range warm {
		if _, err := run(); err != nil {
			b.Fatal(err)
		}
	}
	runtime.GC()
	load := loadAverage()
	o := &benchOutcome{}
	hostBefore := harnessCPU(b, rt)
	userBefore, sysBefore := engineCPUParts()
	errs := make(chan error, conc)
	var wg sync.WaitGroup
	b.ResetTimer()
	start := time.Now()
	for k := range conc {
		n := b.N / conc
		if k < b.N%conc {
			n++
		}
		wg.Go(func() {
			for range n {
				t0 := time.Now()
				res, err := run()
				if err == nil {
					err = o.add(res.Trace, steps)
				}
				if err != nil {
					errs <- err
					return
				}
				o.mu.Lock()
				o.runs = append(o.runs, time.Since(t0))
				o.mu.Unlock()
			}
		})
	}
	wg.Wait()
	wall := time.Since(start)
	b.StopTimer()
	user, sys := engineCPUParts()
	hostSpent := harnessCPU(b, rt) - hostBefore
	close(errs)
	if err := <-errs; err != nil {
		b.Fatal(err)
	}
	o.report(b, wall, user-userBefore+sys-sysBefore, sys-sysBefore, hostSpent, load)
}

func BenchmarkStepBoundary(b *testing.B) {
	bun := benchSetup(b)
	for _, topology := range []string{"host", "subprocess"} {
		b.Run("topology="+topology, func(b *testing.B) {
			rt := benchRuntime(b, bun, topology)
			for _, conc := range []int{1, 8} {
				for _, n := range studyStepSizes(b) {
					args := fmt.Appendf(nil, `{"p":%s}`, jsonval.Synthetic(n))
					b.Run(fmt.Sprintf("conc=%d/size=%s", conc, jsonval.SizeLabel(n)), func(b *testing.B) {
						benchRuns(b, rt, "f/bench/chain", args, benchChainSteps, conc, 3, func(res *wl.Result) error {
							if len(res.Result) != n {
								return fmt.Errorf("the chain's result has %d bytes, not %d", len(res.Result), n)
							}
							return nil
						})
						b.SetBytes(int64(2 * n))
					})
				}
			}
		})
	}
}

func BenchmarkHostShapes(b *testing.B) {
	bun := benchSetup(b)
	rt := benchRuntime(b, bun, "host")
	for _, s := range benchShapes {
		args := fmt.Appendf(nil, `{"tenant":"tenant-a","region":"eu-1","seed":%s}`, jsonval.Synthetic(s.sizes[0]))
		b.Run("shape="+s.name, func(b *testing.B) {
			if s.conc > 1 {
				benchRuns(b, rt, s.path(), args, s.steps, s.conc, 1, checkShape(s))
				return
			}
			benchShapeRuns(b, rt, s, args)
		})
	}
}

// checkShape checks a shape's run: its result is its last step's.
func checkShape(s benchShape) func(*wl.Result) error {
	return func(res *wl.Result) error {
		var out struct {
			N   int             `json:"n"`
			CPU json.RawMessage `json:"cpu"`
		}
		if err := json.Unmarshal(res.Result, &out); err != nil || out.N != s.steps || out.CPU == nil {
			return fmt.Errorf("shape %s: the result is not its last step's: %.200s (%v)", s.name, res.Result, err)
		}
		return nil
	}
}

// benchShapeRuns runs b.N runs of a sequential shape, one at a time, and
// reports besides benchRuns's figures each run's engine and host CPU per
// step at p50 and p95: the engine's from getrusage around the run, the
// host's from the CPU time the run's last step reports, against the last
// run's (or the probe's, for the first).
func benchShapeRuns(b *testing.B, rt *wl.Runtime, s benchShape, args json.RawMessage) {
	ctx := context.Background()
	check := checkShape(s)
	run := func() (*wl.Result, time.Duration, error) {
		res, err := rt.Run(ctx, s.path(), args, wl.RunOptions{})
		if err == nil && res.Status != wl.StatusSuccess {
			err = fmt.Errorf("run %s: %s: %.300s", s.path(), res.Status, res.Result)
		}
		if err == nil {
			err = check(res)
		}
		var host time.Duration
		if err == nil {
			var out struct{ CPU json.RawMessage }
			_ = json.Unmarshal(res.Result, &out)
			host, err = cpuOf(out.CPU)
		}
		return res, host, err
	}
	if _, _, err := run(); err != nil {
		b.Fatal(err)
	}
	runtime.GC()
	load := loadAverage()
	o := &benchOutcome{}
	hostLast := harnessCPU(b, rt)
	hostBefore := hostLast
	userBefore, sysBefore := engineCPUParts()
	b.ResetTimer()
	start := time.Now()
	for range b.N {
		t0, e0 := time.Now(), engineCPU()
		res, host, err := run()
		e1 := engineCPU()
		if err == nil {
			err = o.add(res.Trace, s.steps)
		}
		if err != nil {
			b.Fatal(err)
		}
		o.runs = append(o.runs, time.Since(t0))
		o.runEngine = append(o.runEngine, (e1-e0)/time.Duration(s.steps))
		o.runHost = append(o.runHost, (host-hostLast)/time.Duration(s.steps))
		hostLast = host
	}
	wall := time.Since(start)
	b.StopTimer()
	user, sys := engineCPUParts()
	hostSpent := harnessCPU(b, rt) - hostBefore
	o.report(b, wall, user-userBefore+sys-sysBefore, sys-sysBefore, hostSpent, load)
}

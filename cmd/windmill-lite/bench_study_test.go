package main

// The JSON-library study's additions to G1's benchmarks (branch
// study/json-libraries, never merged):
//
//   - WL_BENCH_SIZES (labels such as 26B,1KiB,16MiB) replaces
//     BenchmarkStepBoundary's sizes;
//   - BenchmarkHostShapes gains shapes whose steps return 256 KiB to
//     16 MiB;
//   - BenchmarkStudyPassThrough is the workload brief's pass-through chain
//     (its pattern 1): an F2 array returned unchanged through 0 to 4 nested
//     sub-flows, then forwarded whole into three steps' arguments and
//     mapped to its ids by an expression;
//   - BenchmarkStudyExpressions is the brief's pattern 5: expressions that
//     read short fields out of an F1 result and an F5 flow input, at their
//     typical and worst sizes, each step's expression timed on its own.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bench"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonlib/shapes"
	"github.com/invakid404/windmill-lite/wl"
)

// studyStepSizes are BenchmarkStepBoundary's payload sizes.
func studyStepSizes(b *testing.B) []int {
	env := os.Getenv("WL_BENCH_SIZES")
	if env == "" {
		return bench.SyntheticSizes
	}
	var out []int
	for _, l := range strings.Split(env, ",") {
		n, ok := shapes.ParseSize(l)
		if !ok {
			b.Fatalf("WL_BENCH_SIZES: bad size %q", l)
		}
		out = append(out, n)
	}
	return out
}

func init() {
	benchShapes = append(benchShapes,
		benchShape{name: "s20-256KiB", steps: 20, sizes: []int{256 << 10}, conc: 1},
		benchShape{name: "s10-1MiB", steps: 10, sizes: []int{1 << 20}, conc: 1},
		benchShape{name: "s8-4MiB", steps: 8, sizes: []int{4 << 20}, conc: 1},
		benchShape{name: "s4-16MiB", steps: 4, sizes: []int{16 << 20}, conc: 1},
	)
}

// studyBig is the step that makes the brief's F1 and F2 values in the
// host, once per kind and size (kept in the module's state, warm).
const studyBig = `// The JSON-library study's large values: the workload brief's F1 (one
// wide object with a markup string and its plain-text form) and F2 (an
// array of wide records with one text field each), of about size bytes.
const made = new Map<string, unknown>();

const plain = "Hello Ana,\n\nThanks for the update on the quarterly figures — the café meeting moved to Thursday. " +
  "Please find the summary below and the “draft 3” attached.\n\nRegion  Revenue  Δ\nEU-1  1,204,000  +4.2%\n\n" +
  "> On Tue, Bob wrote:\n> Could you resend the numbers for Zürich and São Paulo?\n\n";
const markup = '<div class="msg" data-id="m1"><p style="margin:0 0 8px 0">Hello Ana,</p>\n' +
  "<p>Thanks for the update on the quarterly figures &mdash; the café meeting moved to Thursday. " +
  'Please find the summary below.</p>\n<table border="0" cellpadding="4"><tr><td>Region</td><td>Revenue</td></tr>' +
  "<tr><td>EU-1</td><td>1,204,000</td></tr></table>\n<blockquote>&gt; Could you resend the numbers?</blockquote></div>\n";

function text(chunk: string, n: number): string {
  return chunk.repeat(Math.ceil(n / chunk.length) + 1).slice(0, Math.max(0, n));
}

function record(i: number, t: string): Record<string, unknown> {
  return {
    id: "rec-" + i, external_id: "ext-" + (100000 + i), type: "conversation", title: "Follow-up on order " + (5000 + i),
    author: { id: "u-" + (i % 7), name: "Agent " + (i % 7), role: "agent" },
    channel: { id: "c-1", name: "support", kind: "email", region: "eu-1" },
    created_at: "2026-10-01T08:00:00Z", updated_at: "2026-10-02T09:30:00Z", score: 0.82, rank: i,
    is_read: true, is_flagged: false, parent_id: null,
    tags: [{ name: "billing" }, { name: "priority", weight: 2 }], participants: [{ id: "u-1" }, { id: "u-2", role: "cc" }],
    links: [{ href: "https://example.invalid/t/" + i }], labels: [{ k: "lang", v: "en" }],
    mentions: [{ id: "u-3" }, { id: "u-4" }, { id: "u-5" }], attachments_meta: [{ name: "a.pdf", size: "12 KB" }],
    status: "open", locale: "en", summary: "Customer asks about the invoice for order " + (5000 + i), source: "mail", text: t,
  };
}

function f2(size: number): unknown {
  const n = Math.max(1, Math.round(size / 22016));
  const per = Math.max(0, Math.floor(size / n) - 1100);
  return Array.from({ length: n }, (_, i) => record(i, text(plain, per)));
}

function f1(size: number): unknown {
  const headers: Record<string, string[]> = {};
  const keys = size < 262144 ? 20 : 40;
  for (let i = 0; i < keys; i++) {
    headers["x-header-" + i] = Array.from({ length: 1 + (i % 5) }, (_, j) => text("value; token=abc123; path=/a/b; domain=example.invalid ", 50 + ((i * 37 + j * 11) % 200)));
  }
  const attachments = Array.from({ length: 8 }, (_, i) => ({ id: "att-" + i, name: "report-" + i + ".pdf", type: "application/pdf", size: 10000 + i * 977, inline: false, cid: "part" + i + "@example.invalid" }));
  const rest = Math.max(0, size - (keys * 400 + 1800));
  return {
    id: "msg-000123", thread_id: "thr-000045", subject: "Re: quarterly report — draft 3", from: "Ana Example <ana@example.invalid>",
    to: ["ops@example.invalid", "finance@example.invalid"], cc: [], reply_to: "ana@example.invalid", date: "2026-10-30T09:15:00Z",
    folder: "inbox", language: "en", status: "new", category: "finance", priority: 2, size_hint: 123456, has_attachments: true,
    source: "mail", labels: ["finance", "q3"], summary: "Quarterly figures and the café meeting",
    headers, attachments, markup: text(markup, Math.floor(rest / 1.27)), text: text(plain, Math.floor(rest * 0.27 / 1.27)),
  };
}

export async function main(kind: string, size: number) {
  const k = kind + ":" + size;
  let v = made.get(k);
  if (v === undefined) {
    v = kind === "F1" ? f1(size) : f2(size);
    made.set(k, v);
  }
  return v;
}
`

// studySink takes a value as its argument and returns a small summary.
const studySink = `// Takes a value and returns its size, so that only the argument is large.
export async function main(p: unknown) {
  if (Array.isArray(p)) return { kind: "array", n: p.length };
  if (typeof p === "string") return { kind: "string", n: p.length };
  if (p === null || typeof p !== "object") return { kind: typeof p, n: 0 };
  return { kind: "object", n: Object.keys(p as object).length };
}
`

// studyListSteps are the steps the replicated list is copied into (the
// brief: 4 to 6).
const studyListSteps = 5

// studyHostPID is the benchmark host's process, for BenchmarkStudyMemory.
var studyHostPID int

// studyPassDepths are the pass-through chain's sub-flow levels.
const studyPassDepths = 4

// studyPassVariants are the pass-through chain's depth and fan-out pairs:
// every depth forwarding into 3 steps, and depth 4 into 1 and 5.
var studyPassVariants = [][2]int{{0, 3}, {1, 3}, {2, 3}, {3, 3}, {4, 3}, {4, 1}, {4, 5}}

// studyExprs are the expressions of f/bench/expr, by step: the brief's
// access patterns on the F1 result r and the F5 flow input.
var studyExprs = []struct{ id, expr string }{
	{"e0", `1 + 1`},
	{"e1", `results.r.subject`},
	{"e2", `results.r?.headers?.["x-header-1"]?.[0]`},
	{"e3", `results.r == null ? null : results.r.id`},
	{"e4", `flow_input.subject`},
	{"e5", `flow_input.sender?.email`},
	{"e6", `({ id: results.r.id, subject: results.r.subject, text: results.r.text, priority: flow_input.priority, sender: flow_input.sender })`},
	{"e7", `results.r.attachments.map((a) => a.id)`},
}

// studyFolder are the study's scripts and flows.
func studyFolder() map[string]string {
	files := map[string]string{
		"f/bench/big.ts":  studyBig,
		"f/bench/sink.ts": studySink,
	}
	sizeIn := "input_transforms: {size: {type: javascript, expr: flow_input.size}}"
	// pt_l0 makes the value and returns it through a step that returns its
	// input; pt_lk returns pt_l(k-1)'s result.
	files["f/bench/pt_l0.flow/flow.yaml"] = "summary: Makes an F2 value\nvalue:\n  modules:\n" +
		"    - id: m\n      value: {type: script, path: f/bench/big, input_transforms: {kind: {type: static, value: F2}, size: {type: javascript, expr: flow_input.size}}}\n" +
		"    - id: e\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: previous_result}}}\n"
	for k := 1; k < studyPassDepths; k++ {
		files[fmt.Sprintf("f/bench/pt_l%d.flow/flow.yaml", k)] = fmt.Sprintf("summary: Returns its sub-flow's result\nvalue:\n  modules:\n"+
			"    - id: x\n      value: {type: flow, path: f/bench/pt_l%d, %s}\n", k-1, sizeIn)
	}
	for _, v := range studyPassVariants {
		d, fan := v[0], v[1]
		var b strings.Builder
		fmt.Fprintf(&b, "summary: The pass-through chain at depth %d, forwarded into %d steps\nvalue:\n  modules:\n", d, fan)
		src := "x"
		if d == 0 {
			b.WriteString("    - id: m\n      value: {type: script, path: f/bench/big, input_transforms: {kind: {type: static, value: F2}, size: {type: javascript, expr: flow_input.size}}}\n")
			b.WriteString("    - id: x\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: previous_result}}}\n")
		} else {
			fmt.Fprintf(&b, "    - id: x\n      value: {type: flow, path: f/bench/pt_l%d, %s}\n", d-1, sizeIn)
		}
		for k := 1; k <= fan; k++ {
			fmt.Fprintf(&b, "    - id: s%d\n      value: {type: script, path: f/bench/sink, input_transforms: {p: {type: javascript, expr: results.%s}}}\n", k, src)
		}
		fmt.Fprintf(&b, "    - id: ids\n      value: {type: script, path: f/bench/sink, input_transforms: {p: {type: javascript, expr: 'results.%s.map((r) => r.id)'}}}\n", src)
		files[fmt.Sprintf("f/bench/pt_d%d_f%d.flow/flow.yaml", d, fan)] = b.String()
	}
	files["f/bench/chain2.flow/flow.yaml"] = "summary: Two steps that return their argument\nvalue:\n  modules:\n" +
		"    - id: a\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: flow_input.p}}}\n" +
		"    - id: b\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: previous_result}}}\n"
	var lst strings.Builder
	lst.WriteString("summary: A list of short strings copied into the arguments of five steps\nvalue:\n  modules:\n")
	for k := 1; k <= studyListSteps; k++ {
		fmt.Fprintf(&lst, "    - id: s%d\n      value: {type: script, path: f/bench/sink, input_transforms: {p: {type: javascript, expr: flow_input.list}}}\n", k)
	}
	files["f/bench/list.flow/flow.yaml"] = lst.String()
	var e strings.Builder
	e.WriteString("summary: Expressions on large values\nvalue:\n  modules:\n")
	e.WriteString("    - id: r\n      value: {type: script, path: f/bench/big, input_transforms: {kind: {type: static, value: F1}, size: {type: javascript, expr: flow_input.f1size}}}\n")
	for _, x := range studyExprs {
		fmt.Fprintf(&e, "    - id: %s\n      value: {type: script, path: f/bench/sink, input_transforms: {p: {type: javascript, expr: '%s'}}}\n", x.id, x.expr)
	}
	files["f/bench/expr.flow/flow.yaml"] = e.String()
	return files
}

// studyRuns runs b.N runs of path with args, one at a time, after warm
// runs, and reports per run: the wall time (ns/op), the engine's CPU and
// system time, the host's CPU, the expression time, the engine's bytes
// allocated, and each step's expression time when perSite is set.
func studyRuns(b *testing.B, rt *wl.Runtime, path string, args json.RawMessage, warm int, perSite bool) {
	ctx := context.Background()
	run := func() (*wl.Result, error) {
		res, err := rt.Run(ctx, path, args, wl.RunOptions{})
		if err == nil && res.Status != wl.StatusSuccess {
			err = fmt.Errorf("run %s: %s: %.300s", path, res.Status, res.Result)
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
	hostBefore, _ := probe(b, rt, false)
	userBefore, sysBefore := engineCPUParts()
	var ms0, ms1 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	var expr time.Duration
	site := map[string]time.Duration{}
	steps := 0
	var last *wl.Trace
	b.ResetTimer()
	start := time.Now()
	for range b.N {
		res, err := run()
		if err != nil {
			b.Fatal(err)
		}
		st, e := steps2(res.Trace, site)
		steps += st
		expr += e
		last = res.Trace
	}
	wall := time.Since(start)
	b.StopTimer()
	runtime.ReadMemStats(&ms1)
	user, sys := engineCPUParts()
	hostAfter, _ := probe(b, rt, false)
	host := hostAfter - hostBefore
	// The on_run_end payload of the last run's trace, as a plugin's
	// onRunEnd would be sent it: marshalled three times once the runs' CPU
	// and memory are read, the median reported, so that the runs' own
	// figures leave it out.
	var payload int
	var marshal []time.Duration
	for range 3 {
		t0 := time.Now()
		p, err := wl.StudyRunEndPayload(last)
		if err != nil {
			b.Fatal(err)
		}
		marshal = append(marshal, time.Since(t0))
		payload = len(p)
	}
	n := float64(b.N)
	b.ReportMetric(float64(wall)/n, "ns/op")
	b.ReportMetric(float64(user-userBefore+sys-sysBefore)/n, "engine-cpu-ns/run")
	b.ReportMetric(float64(sys-sysBefore)/n, "engine-sys-ns/run")
	b.ReportMetric(float64(host)/n, "host-cpu-ns/run")
	b.ReportMetric(float64(expr)/n, "expr-ns/run")
	b.ReportMetric(float64(ms1.TotalAlloc-ms0.TotalAlloc)/n, "engine-alloc-B/run")
	b.ReportMetric(float64(steps)/n, "steps/run")
	b.ReportMetric(float64(bench.Percentile(marshal, 0.5)), "runend-marshal-ns")
	b.ReportMetric(float64(payload), "runend-B")
	b.ReportMetric(load, "load1")
	if perSite {
		for _, x := range studyExprs {
			b.ReportMetric(float64(site["m:"+x.id])/n, "expr-"+x.id+"-ns")
		}
	}
}

// steps2 counts a trace's steps and its expression time, adding each
// top-level step's expression time to site.
func steps2(tr *wl.Trace, site map[string]time.Duration) (int, time.Duration) {
	var expr time.Duration
	mods := map[string]bool{}
	for _, sp := range tr.Spans {
		switch sp.Kind {
		case engine.SpanModule:
			mods[sp.Site.String()] = true
		case engine.SpanExpression:
			expr += sp.Duration
			site[sp.Site.String()] += sp.Duration
		}
	}
	return len(mods), expr
}

// BenchmarkStudyPassThrough is the pass-through chain at F2's p90 and
// worst sizes, from 0 to 4 sub-flow levels; the difference between depths
// is what one level costs.
func BenchmarkStudyPassThrough(b *testing.B) {
	bun := benchSetup(b)
	rt := benchRuntime(b, bun, "host")
	for _, size := range []int{64 << 10, 2150 << 10} {
		for _, v := range studyPassVariants {
			b.Run(fmt.Sprintf("size=%s/depth=%d/fan=%d", bench.SizeLabel(size), v[0], v[1]), func(b *testing.B) {
				studyRuns(b, rt, fmt.Sprintf("f/bench/pt_d%d_f%d", v[0], v[1]), fmt.Appendf(nil, `{"size":%d}`, size), 2, false)
			})
		}
	}
}

// BenchmarkStudyExpressions is f/bench/expr with the brief's F5 as the
// flow input and an F1 result, both at their typical and worst sizes.
func BenchmarkStudyExpressions(b *testing.B) {
	bun := benchSetup(b)
	rt := benchRuntime(b, bun, "host")
	f1, _ := shapes.BriefNamed("F1")
	f5, _ := shapes.BriefNamed("F5")
	for i, tier := range []string{"typical", "worst"} {
		input := f5.Tiers[i*2].Payload()
		args := append(fmt.Appendf(nil, `{"f1size":%d,`, f1.Tiers[i*2].Size), input[1:]...)
		b.Run("tier="+tier, func(b *testing.B) {
			studyRuns(b, rt, "f/bench/expr", args, 2, true)
		})
	}
}

// BenchmarkStudyReplicatedList is the brief's replicated list: its L
// family (512 short strings, 20 KiB, and 128 strings, 5 KiB) as the flow
// input, copied into the arguments of five steps.
func BenchmarkStudyReplicatedList(b *testing.B) {
	bun := benchSetup(b)
	rt := benchRuntime(b, bun, "host")
	l, _ := shapes.BriefNamed("L")
	for _, t := range l.Tiers {
		args := append(append([]byte(`{"list":`), t.Payload()...), '}')
		b.Run("tier="+t.Name, func(b *testing.B) {
			studyRuns(b, rt, "f/bench/list", args, 2, false)
		})
	}
}

// BenchmarkStudyMemory measures the peak resident memory of the engine
// (this process) and of the Bun host while one run runs, after a warm run
// on a small input, a garbage collection and a reset of both peaks (Linux:
// VmHWM, reset through /proc/<pid>/clear_refs). It reports each process's
// resident size before the run (base), its peak, and the rise between
// them, and the engine's live Go heap before the run. The sum of the two
// peaks bounds their combined peak from above, since the two need not fall
// at the same moment; a sampler that reads both resident sizes together
// every millisecond gives a bound from below (combined-sampled-MiB). The
// runs: f/bench/chain2 (two steps that return their argument) at 16, 32
// and 64 MiB; f/bench/chain (eight
// steps) at 16 MiB, whose trace keeps every step's values; and the
// pass-through chain at its deepest and widest with its on_run_end payload
// marshalled inside the window. The flow input is submitted from the
// engine's side, so the host does not hold it.
func BenchmarkStudyMemory(b *testing.B) {
	if runtime.GOOS != "linux" {
		b.Skip("peak resident memory needs /proc")
	}
	bun := benchSetup(b)
	// Each case opens its own runtime and Bun host, warms them on a small
	// input, and only then builds its own input, so that what the engine
	// keeps from earlier runs and the other cases' inputs stay out of its
	// base: Go's collector lets the heap grow in proportion to what is
	// live, so leftovers would inflate the peak too.
	type memCase struct {
		name, path string
		warm       []byte
		args       func() []byte
		runEnd     bool
	}
	synthetic := func(n int) func() []byte {
		return func() []byte { return fmt.Appendf(nil, `{"p":%s}`, bench.Synthetic(n)) }
	}
	var cases []memCase
	for _, n := range []int{16 << 20, 32 << 20, 64 << 20} {
		cases = append(cases, memCase{"chain2/size=" + bench.SizeLabel(n), "f/bench/chain2", []byte(`{"p":1}`), synthetic(n), false})
	}
	cases = append(cases,
		memCase{"chain8/size=16MiB", "f/bench/chain", []byte(`{"p":1}`), synthetic(16 << 20), false},
		memCase{"passthrough/size=2150KiB/depth=4/fan=5/runend", "f/bench/pt_d4_f5", []byte(`{"size":1024}`),
			func() []byte { return fmt.Appendf(nil, `{"size":%d}`, 2150<<10) }, true},
	)
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			rt := benchRuntime(b, bun, "host")
			host := studyHostPID
			run := func(args []byte) *wl.Result {
				res, err := rt.Run(context.Background(), c.path, args, wl.RunOptions{})
				if err != nil || res.Status != wl.StatusSuccess {
					b.Fatalf("run %s: %v %v", c.path, err, res)
				}
				return res
			}
			run(c.warm)
			args := c.args()
			var peakE, peakH, baseE, baseH, combined int64
			var live uint64
			for range b.N {
				runtime.GC()
				debug.FreeOSMemory()
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				live = ms.HeapAlloc
				if !resetPeak(0) || !resetPeak(host) {
					b.Skip("cannot reset the peak resident size")
				}
				baseE, baseH = procKiB(0, "VmRSS:"), procKiB(host, "VmRSS:")
				sampler := startCombined(host)
				res := run(args)
				if c.runEnd {
					if _, err := wl.StudyRunEndPayload(res.Trace); err != nil {
						b.Fatal(err)
					}
				}
				combined = sampler.finish()
				peakE, peakH = procKiB(0, "VmHWM:"), procKiB(host, "VmHWM:")
				runtime.KeepAlive(res)
			}
			runtime.KeepAlive(args)
			mib := func(kib int64) float64 { return float64(kib) / 1024 }
			b.ReportMetric(float64(live)/(1<<20), "engine-live-MiB")
			b.ReportMetric(mib(baseE), "engine-base-MiB")
			b.ReportMetric(mib(peakE), "engine-peak-MiB")
			b.ReportMetric(mib(peakE-baseE), "engine-rise-MiB")
			b.ReportMetric(mib(baseH), "host-base-MiB")
			b.ReportMetric(mib(peakH), "host-peak-MiB")
			b.ReportMetric(mib(peakH-baseH), "host-rise-MiB")
			b.ReportMetric(mib(peakE+peakH), "sum-peak-MiB")
			b.ReportMetric(mib(combined), "combined-sampled-MiB")
		})
	}
}

// TestStudyRetention asks whether a long-lived runtime keeps finished
// runs' values (JC1 §10.5). Measurement only: it asserts nothing, and runs
// only when WL_STUDY_RETENTION names a directory for its output. One
// runtime per variant, held as an embedder holds it, serves sequential
// runs of f/bench/chain2 (two steps that return their argument), each
// run's input built just before it and dropped after: 30 runs of a 16 MiB
// value, then 2,200 of a 64 KiB value, enough to pass a harness process's
// 4,096 recently settled invocations (DefaultRecentLate). After every 5th
// (every 50th) run it collects twice and records the Go heap, and on Linux
// the resident size; at the end it writes an inuse_space heap profile. The
// build tag study_recentlate2 is the control: a limit of 2. The variants: the subprocess topology an
// embedder gets by default, without a plugin and with a plugin whose
// onRunEnd drops the trace; and host mode without a plugin, the topology
// of the first memory benchmark, whose base grew with each run.
func TestStudyRetention(t *testing.T) {
	out := os.Getenv("WL_STUDY_RETENTION")
	if out == "" {
		t.Skip("WL_STUDY_RETENTION is not set; it names the directory for the probe's output")
	}
	runtime.MemProfileRate = 64 << 10
	bun := benchSetup(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	if err := os.WriteFile(plugin, []byte("export default {\n  onRunEnd(trace) { return []; },\n};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tsv, err := os.Create(filepath.Join(out, "retention.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer tsv.Close()
	fmt.Fprintln(tsv, "variant\tvalue\truns\theap_inuse_mib\theap_alloc_mib\theap_objects\tgoroutines\tresults_freed\ttraces_freed\truns_alive\tsettlements_alive\truns_started\trss_mib")
	subprocess := func(t *testing.T, plugin string) *wl.Runtime {
		rt, err := wl.Open(benchBundle, wl.Options{
			EngineVersion:  resolveVersion(),
			Diagnostics:    io.Discard,
			Variables:      benchVariables,
			Resources:      benchResources,
			DataDir:        filepath.Join(out, "data-"+filepath.Base(t.Name())),
			Bun:            bun,
			HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
			ModuleState:    wl.ModuleStateWarm,
			Plugin:         plugin,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rt.Close() })
		return rt
	}
	variants := []struct {
		name string
		open func(t *testing.T) *wl.Runtime
	}{
		{"subprocess", func(t *testing.T) *wl.Runtime { return subprocess(t, "") }},
		{"subprocess-onRunEnd", func(t *testing.T) *wl.Runtime { return subprocess(t, plugin) }},
		{"host", func(t *testing.T) *wl.Runtime { return benchRuntime(t, bun, "host") }},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			rt := v.open(t)
			// Finalizers count the runs' results and traces the collector
			// has freed: whether the runtime keeps the traces themselves.
			var resultsFreed, tracesFreed atomic.Int64
			record := func(value string, runs int) {
				runtime.GC()
				runtime.GC()
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				alive, settles, started := engine.StudyRunsAlive()
				// The resident size, on Linux only (-1 elsewhere).
				rss := float64(procKiB(0, "VmRSS:")) / 1024
				if rss < 0 {
					rss = -1
				}
				fmt.Fprintf(tsv, "%s\t%s\t%d\t%.1f\t%.1f\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.1f\n", v.name, value, runs, float64(ms.HeapInuse)/(1<<20), float64(ms.HeapAlloc)/(1<<20), ms.HeapObjects, runtime.NumGoroutine(), resultsFreed.Load(), tracesFreed.Load(), alive, settles, started, rss)
			}
			run := func(size int) {
				args := fmt.Appendf(nil, `{"p":%s}`, bench.Synthetic(size))
				res, err := rt.Run(context.Background(), "f/bench/chain2", args, wl.RunOptions{})
				if err != nil || res.Status != wl.StatusSuccess {
					t.Fatalf("run: %v %v", err, res)
				}
				runtime.SetFinalizer(res, func(*wl.Result) { resultsFreed.Add(1) })
				if res.Trace != nil {
					runtime.SetFinalizer(res.Trace, func(*wl.Trace) { tracesFreed.Add(1) })
				}
			}
			run(64)
			record("warm", 0)
			for _, c := range []struct {
				value       string
				size, runs  int
				recordEvery int
			}{{"16MiB", 16 << 20, 30, 5}, {"64KiB", 64 << 10, 2200, 50}} {
				for i := 1; i <= c.runs; i++ {
					run(c.size)
					if i%c.recordEvery == 0 {
						record(c.value, i)
					}
				}
			}
			runtime.GC()
			runtime.GC()
			f, err := os.Create(filepath.Join(out, "heap-"+v.name+".pb.gz"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
				t.Fatal(err)
			}
			g, err := os.Create(filepath.Join(out, "goroutines-"+v.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			if err := pprof.Lookup("goroutine").WriteTo(g, 1); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// combinedSampler reads the engine's and the host's resident sizes one
// after the other every millisecond and keeps the largest sum it sees: an
// approximate paired sample of their combined resident size, not a strict
// bound, since the two reads are not one observation and statm's resident
// counts are themselves approximate (proc_pid_statm(5)). It reads
// /proc/<pid>/statm into a fixed buffer, so a sample allocates nothing;
// the sampler's goroutine and reads are its own small overhead.
type combinedSampler struct {
	stop chan struct{}
	done chan int64
}

func startCombined(host int) *combinedSampler {
	s := &combinedSampler{stop: make(chan struct{}), done: make(chan int64, 1)}
	self, err := os.Open(procPath(0, "statm"))
	if err != nil {
		s.done <- -1
		return s
	}
	other, err := os.Open(procPath(host, "statm"))
	if err != nil {
		self.Close()
		s.done <- -1
		return s
	}
	go func() {
		defer self.Close()
		defer other.Close()
		var buf [128]byte
		page := int64(os.Getpagesize())
		resident := func(f *os.File) int64 {
			if _, err := f.Seek(0, 0); err != nil {
				return -1
			}
			n, _ := f.Read(buf[:])
			return statmResident(buf[:n])
		}
		var most int64
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			if e, h := resident(self), resident(other); e >= 0 && h >= 0 && (e+h)*page/1024 > most {
				most = (e + h) * page / 1024
			}
			select {
			case <-s.stop:
				s.done <- most
				return
			case <-tick.C:
			}
		}
	}()
	return s
}

// finish stops the sampler and returns the largest combined resident size
// it saw, in KiB (-1 when /proc could not be read).
func (s *combinedSampler) finish() int64 {
	close(s.stop)
	return <-s.done
}

// statmResident is the second field of /proc/<pid>/statm: the resident
// size in pages.
func statmResident(b []byte) int64 {
	i := 0
	for i < len(b) && b[i] != ' ' {
		i++
	}
	if i >= len(b) {
		return -1
	}
	var n int64
	digits := 0
	for _, c := range b[i+1:] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
		digits++
	}
	if digits == 0 {
		return -1
	}
	return n
}

// resetPeak resets a process's peak resident size (0: this process).
func resetPeak(pid int) bool {
	return os.WriteFile(procPath(pid, "clear_refs"), []byte("5"), 0) == nil
}

// procKiB reads a field of a process's /proc status in KiB (0: this
// process).
func procKiB(pid int, field string) int64 {
	b, err := os.ReadFile(procPath(pid, "status"))
	if err != nil {
		return -1
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, field); ok {
			n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return n
		}
	}
	return -1
}

func procPath(pid int, name string) string {
	if pid == 0 {
		return "/proc/self/" + name
	}
	return "/proc/" + strconv.Itoa(pid) + "/" + name
}

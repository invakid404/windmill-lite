package main

// The large-payload benchmarks (ADR 0005's benchmark plan,
// "Large values"): the size-dependent cost of the step path, from the
// JSON-library study's benchmarks and the workload brief's shapes, all
// synthetic.
//
//   - WL_BENCH_SIZES (labels such as 26B,1KiB,16MiB) replaces
//     BenchmarkStepBoundary's sizes, so that it reaches 16 MiB;
//   - BenchmarkHostShapes gains four shapes whose steps return 256 KiB to
//     16 MiB (largeShapes);
//   - BenchmarkPassThrough is the brief's pass-through chain: an array of
//     wide records (its F2 family) returned unchanged through 0 to 4 nested
//     sub-flows, then forwarded whole into 1 to 5 steps' arguments and
//     mapped to its ids by an expression; on the plugin host its onRunEnd
//     hook takes the whole trace;
//   - BenchmarkExpressions reads short fields out of a wide object with long
//     strings (the brief's F1 family) of 96 KiB and 2 MiB, each step's
//     expression timed on its own;
//   - BenchmarkRunPeaks is the peak resident memory of the engine and of the
//     Bun host over one run of 16 and 32 MiB (Linux only).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bench"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/wl"
)

// benchSizesEnv names BenchmarkStepBoundary's payload sizes, as labels
// separated by commas (26B, 1KiB, 16MiB); bench.SyntheticSizes without it.
const benchSizesEnv = "WL_BENCH_SIZES"

// stepSizes are BenchmarkStepBoundary's payload sizes.
func stepSizes(b *testing.B) []int {
	env := os.Getenv(benchSizesEnv)
	if env == "" {
		return bench.SyntheticSizes
	}
	var out []int
	for _, l := range strings.Split(env, ",") {
		n, ok := parseSize(strings.TrimSpace(l))
		if !ok {
			b.Fatalf("%s: bad size %q", benchSizesEnv, l)
		}
		out = append(out, n)
	}
	return out
}

// parseSize reads a size label as bench.SizeLabel writes it.
func parseSize(l string) (int, bool) {
	unit := 1
	switch {
	case strings.HasSuffix(l, "MiB"):
		l, unit = strings.TrimSuffix(l, "MiB"), 1<<20
	case strings.HasSuffix(l, "KiB"):
		l, unit = strings.TrimSuffix(l, "KiB"), 1<<10
	case strings.HasSuffix(l, "B"):
		l = strings.TrimSuffix(l, "B")
	default:
		return 0, false
	}
	n, err := strconv.Atoi(l)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * unit, true
}

// largeShapes are the host shapes whose steps return large results: each
// step evaluates the template that binds the flow input, whose seed is the
// first step's result, so the template reads two short fields out of a
// flow input as large as the results.
var largeShapes = []benchShape{
	{name: "s20-256KiB", steps: 20, sizes: []int{256 << 10}, conc: 1},
	{name: "s10-1MiB", steps: 10, sizes: []int{1 << 20}, conc: 1},
	{name: "s8-4MiB", steps: 8, sizes: []int{4 << 20}, conc: 1},
	{name: "s4-16MiB", steps: 4, sizes: []int{16 << 20}, conc: 1},
}

func init() { benchShapes = append(benchShapes, largeShapes...) }

// benchBig is the step that makes the brief's F1 and F2 values in the host,
// once per kind and size, kept in the module's state (a warm process).
const benchBig = `// The large values of the large-payload benchmarks, from the workload brief's
// families: F1, one wide object with a markup string and its plain-text
// form, and F2, an array of wide records with one text field each, of
// about size bytes. Synthetic text only.
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

// benchSink takes a value as its argument and returns a small summary, so
// that only its argument is large.
const benchSink = `// Takes a value and returns its size, so that only the argument is large.
export async function main(p: unknown) {
  if (Array.isArray(p)) return { kind: "array", n: p.length };
  if (typeof p === "string") return { kind: "string", n: p.length };
  if (p === null || typeof p !== "object") return { kind: typeof p, n: 0 };
  return { kind: "object", n: Object.keys(p as object).length };
}
`

// passDepths is the deepest sub-flow level of the pass-through chain.
const passDepths = 4

// passVariants are the pass-through chain's depth and fan-out pairs: every
// depth forwarding into 3 steps, and depth 4 into 1 and 5.
var passVariants = [][2]int{{0, 3}, {1, 3}, {2, 3}, {3, 3}, {4, 3}, {4, 1}, {4, 5}}

// passSizes are the pass-through chain's F2 sizes: the brief's p90 and its
// worst, about 2.1 MB.
var passSizes = []int{64 << 10, 2150 << 10}

// benchExprs are the expressions of f/bench/expr, by step, on the F1 result
// r (not the previous step, so each read is the results lookup's) and the
// flow input's F1 value v: the brief's access patterns (its pattern 5).
var benchExprs = []struct{ id, expr string }{
	{"e0", `1 + 1`},
	{"e1", `results.r.subject`},
	{"e2", `results.r?.headers?.["x-header-1"]?.[0]`},
	{"e3", `results.r == null ? null : results.r.id`},
	{"e4", `({ id: results.r.id, subject: results.r.subject, text: results.r.text })`},
	{"e5", `results.r.attachments.map((a) => a.id)`},
	{"e6", `flow_input.v == null ? null : flow_input.v.id`},
	{"e7", `({ id: flow_input.v.id, subject: flow_input.v.subject, text: flow_input.v.text })`},
}

// exprSizes are BenchmarkExpressions' F1 sizes: the brief's typical and
// its worst.
var exprSizes = []int{96 << 10, 2 << 20}

// largeFolder holds the large-payload benchmarks' scripts and flows.
func largeFolder() map[string]string {
	files := map[string]string{
		"f/bench/big.ts":  benchBig,
		"f/bench/sink.ts": benchSink,
	}
	sizeIn := "input_transforms: {size: {type: javascript, expr: flow_input.size}}"
	makeF2 := "{type: script, path: f/bench/big, input_transforms: {kind: {type: static, value: F2}, size: {type: javascript, expr: flow_input.size}}}"
	// pt_l0 makes the value and returns it through a step that returns its
	// argument; pt_lk returns pt_l(k-1)'s result.
	files["f/bench/pt_l0.flow/flow.yaml"] = "summary: Makes an F2 value\nvalue:\n  modules:\n" +
		"    - id: m\n      value: " + makeF2 + "\n" +
		"    - id: e\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: previous_result}}}\n"
	for k := 1; k < passDepths; k++ {
		files[fmt.Sprintf("f/bench/pt_l%d.flow/flow.yaml", k)] = fmt.Sprintf("summary: Returns its sub-flow's result\nvalue:\n  modules:\n"+
			"    - id: x\n      value: {type: flow, path: f/bench/pt_l%d, %s}\n", k-1, sizeIn)
	}
	for _, v := range passVariants {
		d, fan := v[0], v[1]
		var b strings.Builder
		fmt.Fprintf(&b, "summary: The pass-through chain at depth %d, forwarded into %d steps\nvalue:\n  modules:\n", d, fan)
		if d == 0 {
			b.WriteString("    - id: m\n      value: " + makeF2 + "\n")
			b.WriteString("    - id: x\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: previous_result}}}\n")
		} else {
			fmt.Fprintf(&b, "    - id: x\n      value: {type: flow, path: f/bench/pt_l%d, %s}\n", d-1, sizeIn)
		}
		for k := 1; k <= fan; k++ {
			fmt.Fprintf(&b, "    - id: s%d\n      value: {type: script, path: f/bench/sink, input_transforms: {p: {type: javascript, expr: results.x}}}\n", k)
		}
		b.WriteString("    - id: ids\n      value: {type: script, path: f/bench/sink, input_transforms: {p: {type: javascript, expr: 'results.x.map((r) => r.id)'}}}\n")
		files[fmt.Sprintf("f/bench/pt_d%d_f%d.flow/flow.yaml", d, fan)] = b.String()
	}
	files["f/bench/chain2.flow/flow.yaml"] = "summary: Two steps that return their argument\nvalue:\n  modules:\n" +
		"    - id: a\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: flow_input.p}}}\n" +
		"    - id: b\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: previous_result}}}\n"
	var e strings.Builder
	e.WriteString("summary: Expressions on large values\nvalue:\n  modules:\n")
	e.WriteString("    - id: r\n      value: {type: script, path: f/bench/big, input_transforms: {kind: {type: static, value: F1}, size: {type: javascript, expr: flow_input.size}}}\n")
	for _, x := range benchExprs {
		fmt.Fprintf(&e, "    - id: %s\n      value: {type: script, path: f/bench/sink, input_transforms: {p: {type: javascript, expr: '%s'}}}\n", x.id, x.expr)
	}
	files["f/bench/expr.flow/flow.yaml"] = e.String()
	return files
}

// runsResult is what runRuns measured, per run.
type runsResult struct {
	wall, engineCPU, engineSys, hostCPU, expr time.Duration
	alloc                                     uint64
	steps                                     int
	site                                      map[string]time.Duration
	load                                      float64
	n                                         int
}

// runRuns makes warm runs of path with args, then b.N runs, one at a time,
// and reports per run: the wall time (ns/op), the engine's CPU and system
// time, the host's CPU, the expression spans, the engine's bytes
// allocated and the steps.
func runRuns(b *testing.B, rt *wl.Runtime, path string, args json.RawMessage, warm int) runsResult {
	ctx := context.Background()
	run := func() (*wl.Result, error) {
		res, err := rt.Run(ctx, path, args, wl.RunOptions{})
		if err == nil && res.Status != wl.StatusSuccess {
			err = fmt.Errorf("run %s: %s: %.300s", path, res.Status, res.Result)
		}
		return res, err
	}
	b.StopTimer()
	for range warm {
		if _, err := run(); err != nil {
			b.Fatal(err)
		}
	}
	runtime.GC()
	r := runsResult{site: map[string]time.Duration{}, load: loadAverage(), n: b.N}
	hostBefore := probe(b, rt, false).cpu
	userBefore, sysBefore := engineCPUParts()
	var ms0, ms1 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	b.StartTimer()
	start := time.Now()
	for range b.N {
		res, err := run()
		if err != nil {
			b.Fatal(err)
		}
		st, e := traceSpans(res.Trace, r.site)
		r.steps += st
		r.expr += e
	}
	r.wall = time.Since(start)
	b.StopTimer()
	runtime.ReadMemStats(&ms1)
	user, sys := engineCPUParts()
	r.hostCPU = probe(b, rt, false).cpu - hostBefore
	r.engineCPU, r.engineSys = user-userBefore+sys-sysBefore, sys-sysBefore
	r.alloc = ms1.TotalAlloc - ms0.TotalAlloc
	return r
}

// report reports r's figures per run.
func (r runsResult) report(b *testing.B) {
	n := float64(r.n)
	b.ReportMetric(float64(r.wall)/n, "ns/op")
	b.ReportMetric(float64(r.engineCPU)/n, "engine-cpu-ns/run")
	b.ReportMetric(float64(r.engineSys)/n, "engine-sys-ns/run")
	b.ReportMetric(float64(r.hostCPU)/n, "host-cpu-ns/run")
	b.ReportMetric(float64(r.expr)/n, "expr-ns/run")
	b.ReportMetric(float64(r.alloc)/n, "engine-alloc-B/run")
	b.ReportMetric(float64(r.steps)/n, "steps/run")
	b.ReportMetric(r.load, "load1")
}

// traceSpans counts a trace's steps and its expression time, adding each
// top-level step's expression time to site, by the step's site.
func traceSpans(tr *wl.Trace, site map[string]time.Duration) (int, time.Duration) {
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

// BenchmarkPassThrough is the pass-through chain at F2's p90 and worst
// sizes, from 0 to 4 sub-flow levels and forwarded into 1 to 5 steps, on
// the host and on the plugin host, whose onRunEnd takes the whole trace.
// The difference between depths is what one level costs.
func BenchmarkPassThrough(b *testing.B) {
	bun := benchSetup(b)
	for _, topology := range []string{"host", "host-plugin"} {
		b.Run("topology="+topology, func(b *testing.B) {
			rt := benchRuntime(b, bun, topology)
			for _, size := range passSizes {
				for _, v := range passVariants {
					b.Run(fmt.Sprintf("size=%s/depth=%d/fan=%d", bench.SizeLabel(size), v[0], v[1]), func(b *testing.B) {
						r := runRuns(b, rt, fmt.Sprintf("f/bench/pt_d%d_f%d", v[0], v[1]), fmt.Appendf(nil, `{"size":%d}`, size), 2)
						r.report(b)
					})
				}
			}
		})
	}
}

// BenchmarkExpressions is f/bench/expr with an F1 result and the same F1
// value as the flow input's v, at the brief's typical and worst sizes. Each
// expression's time per run is reported as expr-<id>-ns.
func BenchmarkExpressions(b *testing.B) {
	bun := benchSetup(b)
	rt := benchRuntime(b, bun, "host")
	for _, size := range exprSizes {
		args := fmt.Appendf(nil, `{"size":%d,"v":%s}`, size, f1Value(size))
		b.Run("size="+bench.SizeLabel(size), func(b *testing.B) {
			r := runRuns(b, rt, "f/bench/expr", args, 2)
			r.report(b)
			for _, x := range benchExprs {
				b.ReportMetric(float64(r.site["m:"+x.id])/float64(r.n), "expr-"+x.id+"-ns")
			}
		})
	}
}

// f1Value is the brief's F1 value of about size bytes, as f/bench/big makes
// it: the same keys, the same text, in Go.
func f1Value(size int) []byte {
	const plain = "Hello Ana,\n\nThanks for the update on the quarterly figures — the café meeting moved to Thursday. " +
		"Please find the summary below and the “draft 3” attached.\n\nRegion  Revenue  Δ\nEU-1  1,204,000  +4.2%\n\n" +
		"> On Tue, Bob wrote:\n> Could you resend the numbers for Zürich and São Paulo?\n\n"
	const markup = `<div class="msg" data-id="m1"><p style="margin:0 0 8px 0">Hello Ana,</p>` + "\n" +
		"<p>Thanks for the update on the quarterly figures &mdash; the café meeting moved to Thursday. " +
		`Please find the summary below.</p>` + "\n" + `<table border="0" cellpadding="4"><tr><td>Region</td><td>Revenue</td></tr>` +
		"<tr><td>EU-1</td><td>1,204,000</td></tr></table>\n<blockquote>&gt; Could you resend the numbers?</blockquote></div>\n"
	text := func(chunk string, n int) string {
		// Lengths in UTF-16 code units, as the script's slice counts them,
		// do not matter here: the value only needs its shape and size.
		var s strings.Builder
		for s.Len() < n {
			s.WriteString(chunk)
		}
		return strings.ToValidUTF8(s.String()[:n], "")
	}
	keys := 20
	if size >= 256<<10 {
		keys = 40
	}
	headers := map[string][]string{}
	for i := range keys {
		var vs []string
		for j := range 1 + i%5 {
			vs = append(vs, text("value; token=abc123; path=/a/b; domain=example.invalid ", 50+(i*37+j*11)%200))
		}
		headers["x-header-"+strconv.Itoa(i)] = vs
	}
	type attachment struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Size   int    `json:"size"`
		Inline bool   `json:"inline"`
		CID    string `json:"cid"`
	}
	var atts []attachment
	for i := range 8 {
		atts = append(atts, attachment{"att-" + strconv.Itoa(i), "report-" + strconv.Itoa(i) + ".pdf", "application/pdf", 10000 + i*977, false, "part" + strconv.Itoa(i) + "@example.invalid"})
	}
	rest := max(0, size-(keys*400+1800))
	v := map[string]any{
		"id": "msg-000123", "thread_id": "thr-000045", "subject": "Re: quarterly report — draft 3", "from": "Ana Example <ana@example.invalid>",
		"to": []string{"ops@example.invalid", "finance@example.invalid"}, "cc": []string{}, "reply_to": "ana@example.invalid", "date": "2026-10-30T09:15:00Z",
		"folder": "inbox", "language": "en", "status": "new", "category": "finance", "priority": 2, "size_hint": 123456, "has_attachments": true,
		"source": "mail", "labels": []string{"finance", "q3"}, "summary": "Quarterly figures and the café meeting",
		"headers": headers, "attachments": atts, "markup": text(markup, int(float64(rest)/1.27)), "text": text(plain, int(float64(rest)*0.27/1.27)),
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return bytes.TrimSpace(out.Bytes())
}

// BenchmarkRunPeaks is the peak resident memory of the engine (this
// process) and of the Bun host while one run runs, on Linux (VmHWM, reset
// through /proc/<pid>/clear_refs), after a warm run on a small input, a
// collection and a reset of both peaks. It reports each process's resident
// size before the run (base), its peak, and the rise between them, the
// engine's live Go heap before the run, the sum of the two peaks, and the
// largest sum of the two resident sizes that a sampler read every
// millisecond, one after the other (combined-sampled-MiB): an approximate
// paired sample of what the two hold together, since the two peaks need
// not fall at the same moment. The runs: f/bench/chain2 (two steps that
// return their argument) at 16 and 32 MiB, and f/bench/chain (eight steps)
// at 16 MiB. Each case opens its own runtime and host, and builds its
// input only after the warm run, so that other cases' inputs and runs stay
// out of its base. The flow input is submitted from the engine's side, so
// the host does not hold it. Run it with -benchtime 1x.
func BenchmarkRunPeaks(b *testing.B) {
	if runtime.GOOS != "linux" {
		b.Skip("peak resident memory needs /proc")
	}
	bun := benchSetup(b)
	type peakCase struct {
		name, path string
		size       int
	}
	cases := []peakCase{
		{"chain2/size=16MiB", "f/bench/chain2", 16 << 20},
		{"chain2/size=32MiB", "f/bench/chain2", 32 << 20},
		{"chain8/size=16MiB", "f/bench/chain", 16 << 20},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			rt := benchRuntime(b, bun, "host")
			dir, _ := benchWorkdirs.Load(rt)
			pid, _ := benchHostPIDs.Load(dir)
			host := pid.(int)
			run := func(args []byte) *wl.Result {
				res, err := rt.Run(context.Background(), c.path, args, wl.RunOptions{})
				if err != nil || res.Status != wl.StatusSuccess {
					b.Fatalf("run %s: %v %v", c.path, err, res)
				}
				return res
			}
			run([]byte(`{"p":1}`))
			args := fmt.Appendf(nil, `{"p":%s}`, bench.Synthetic(c.size))
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

// combinedSampler reads the engine's and the host's resident sizes one
// after the other every millisecond and keeps the largest sum it sees: an
// approximate paired sample of their combined resident size, not a strict
// bound, since the two reads are not one observation and statm's resident
// counts are themselves approximate (proc_pid_statm(5)). A sample reads
// /proc/<pid>/statm into a fixed buffer and allocates nothing; the
// sampler's goroutine and reads are its own small overhead.
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
// process), or -1.
func procKiB(pid int, field string) int64 {
	b, err := os.ReadFile(procPath(pid, "status"))
	if err != nil {
		return -1
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, field); ok {
			n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				return -1
			}
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

// TestLargeBenchHelpers checks the large-payload benchmarks' pure helpers:
// the size labels WL_BENCH_SIZES reads, statm's resident field, and the F1
// value's size.
func TestLargeBenchHelpers(t *testing.T) {
	for l, want := range map[string]int{"26B": 26, "1KiB": 1 << 10, "256KiB": 256 << 10, "16MiB": 16 << 20, "2150KiB": 2150 << 10} {
		if n, ok := parseSize(l); !ok || n != want || bench.SizeLabel(n) != l {
			t.Errorf("parseSize(%q) = %d, %v; want %d", l, n, ok, want)
		}
	}
	for _, l := range []string{"", "1", "KiB", "0B", "-1KiB", "1GiB"} {
		if _, ok := parseSize(l); ok {
			t.Errorf("parseSize(%q) accepted", l)
		}
	}
	if got := statmResident([]byte("1234 567 89 0 0 0 0\n")); got != 567 {
		t.Errorf("statmResident = %d", got)
	}
	if got := statmResident([]byte("1234")); got != -1 {
		t.Errorf("statmResident without a second field = %d", got)
	}
	for _, size := range exprSizes {
		v := f1Value(size)
		if !json.Valid(v) || len(v) < size*9/10 || len(v) > size*11/10 {
			t.Errorf("f1Value(%d) is %d bytes (valid %v)", size, len(v), json.Valid(v))
		}
	}
}

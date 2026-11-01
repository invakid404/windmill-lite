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
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonlib/shapes"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/wl"
)

// studyStepSizes are BenchmarkStepBoundary's payload sizes.
func studyStepSizes(b *testing.B) []int {
	env := os.Getenv("WL_BENCH_SIZES")
	if env == "" {
		return jsonval.SyntheticSizes
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

// studyPassDepths are the pass-through chain's sub-flow levels.
const studyPassDepths = 4

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
	for d := 0; d <= studyPassDepths; d++ {
		var b strings.Builder
		fmt.Fprintf(&b, "summary: The pass-through chain at depth %d\nvalue:\n  modules:\n", d)
		src := "x"
		if d == 0 {
			b.WriteString("    - id: m\n      value: {type: script, path: f/bench/big, input_transforms: {kind: {type: static, value: F2}, size: {type: javascript, expr: flow_input.size}}}\n")
			b.WriteString("    - id: x\n      value: {type: script, path: f/bench/echo, input_transforms: {p: {type: javascript, expr: previous_result}}}\n")
		} else {
			fmt.Fprintf(&b, "    - id: x\n      value: {type: flow, path: f/bench/pt_l%d, %s}\n", d-1, sizeIn)
		}
		for _, s := range []string{"s1", "s2", "s3"} {
			fmt.Fprintf(&b, "    - id: %s\n      value: {type: script, path: f/bench/sink, input_transforms: {p: {type: javascript, expr: results.%s}}}\n", s, src)
		}
		fmt.Fprintf(&b, "    - id: ids\n      value: {type: script, path: f/bench/sink, input_transforms: {p: {type: javascript, expr: 'results.%s.map((r) => r.id)'}}}\n", src)
		files[fmt.Sprintf("f/bench/pt_d%d.flow/flow.yaml", d)] = b.String()
	}
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
	hostBefore := harnessCPU(b, rt)
	userBefore, sysBefore := engineCPUParts()
	var ms0, ms1 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	var expr time.Duration
	site := map[string]time.Duration{}
	steps := 0
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
	}
	wall := time.Since(start)
	b.StopTimer()
	runtime.ReadMemStats(&ms1)
	user, sys := engineCPUParts()
	host := harnessCPU(b, rt) - hostBefore
	n := float64(b.N)
	b.ReportMetric(float64(wall)/n, "ns/op")
	b.ReportMetric(float64(user-userBefore+sys-sysBefore)/n, "engine-cpu-ns/run")
	b.ReportMetric(float64(sys-sysBefore)/n, "engine-sys-ns/run")
	b.ReportMetric(float64(host)/n, "host-cpu-ns/run")
	b.ReportMetric(float64(expr)/n, "expr-ns/run")
	b.ReportMetric(float64(ms1.TotalAlloc-ms0.TotalAlloc)/n, "engine-alloc-B/run")
	b.ReportMetric(float64(steps)/n, "steps/run")
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
		for d := 0; d <= studyPassDepths; d++ {
			b.Run(fmt.Sprintf("size=%s/depth=%d", jsonval.SizeLabel(size), d), func(b *testing.B) {
				studyRuns(b, rt, fmt.Sprintf("f/bench/pt_d%d", d), fmt.Appendf(nil, `{"size":%d}`, size), 2, false)
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
		input := f5.Make(f5.Tiers[i*2].Size)
		args := append(fmt.Appendf(nil, `{"f1size":%d,`, f1.Tiers[i*2].Size), input[1:]...)
		b.Run("tier="+tier, func(b *testing.B) {
			studyRuns(b, rt, "f/bench/expr", args, 2, true)
		})
	}
}

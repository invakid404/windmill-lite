package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime/metrics"
	"runtime/pprof"
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
	"github.com/invakid404/windmill-lite/wl"
)

// The embedder shapes: synthetic workloads shaped like the requests a
// host-mode embedder runs, each run two ways and compared request by
// request, interleaved:
//
//   - windmill-lite: the workload as a flow on a warm Bun host in host mode
//     (the host-emb topology: the benchmark host with a plugin whose
//     resource hook reads a local API, and the per-run read cache on);
//   - in process: the same step functions composed by hand in one Bun
//     process (testdata/benchhost/inproc.ts), as plain function calls, with
//     the same store and API reads.
//
// Both sides call the same store, a separate process (embStore): the steps
// read and write it by fetch, and the plugin and the in-process side read
// the resources from it, once per (request, path). Each workload is a tree
// of nodes (embNode) that gives both the flow and the in-process program,
// so the two run the same steps in the same order with the same inputs.
//
// The workloads reproduce the counts a host-mode embedder's requests have:
// steps run (scripts and the flow control the engine runs itself: branches,
// identity steps, loops), host invocations, and expressions over results.<id>
// and flow_input, about half of which the pipeline answers itself and half
// QuickJS evaluates; a parallel loop over 1, 5 or 20 items; eight requests
// in flight; small values, and in A-typical items of about 65 KB passed
// through four levels of steps.
//
// BenchmarkEmbedder reports per request and per step run: both sides'
// elapsed time (p50, p95) and the gap, the engine's CPU, the host's CPU and
// the in-process side's, the expressions' time and the time each invocation
// spends inside the host. It needs the pinned Bun in WL_BUN.

// embNode is one module of a workload: a script step, a branchone whose
// predicates pick a branch, an identity step, or a for-loop.
type embNode struct {
	kind   string // "script", "branch", "identity", "loop"
	id     string
	script string // the script's name under f/emb/
	inputs []embInput
	// A branch's predicates, one per branch, and the default branch.
	preds    []string
	branches [][]embNode
	def      []embNode
	// A loop's iterator expression, whether it runs its iterations in
	// parallel, and its body.
	iter        string
	parallel    bool
	parallelism int
	items       int
	body        []embNode
}

// embInput is a script step's input: an expression, a static JSON value,
// or a resource ($res:<path>).
type embInput struct {
	name   string
	expr   string
	static string
	res    string
}

// embAPIs is how many API resources the steps read, in turn: each request
// reads each of them from the API once (the read cache on one side, a map
// on the other).
const embAPIs = 12

// embParams are every step script's parameters, in order: the in-process
// side calls main with them positionally, as the flow binds them by name.
var embParams = []string{"store", "api", "key", "tenant", "prev", "n", "flag", "opts", "x", "item", "work", "io", "k", "log"}

// embWorkload is one workload: a flow of nodes, run conc requests at once.
type embWorkload struct {
	name  string
	nodes []embNode
	conc  int
	// The target counts per request (for H, per batch of conc), which the
	// shape must reproduce.
	steps, invocations, expressions int
}

// embWork is the work a step does: units of burn (about a microsecond
// each) and store calls.
type embWork struct{ work, io int }

// embBuilder builds a workload's nodes, numbering its steps.
type embBuilder struct {
	n       int
	scripts []string // the top-level script steps' ids so far, for references
	kinds   []string // the step scripts to cycle through
	work    map[string]embWork
	last    string // the id of the last top-level script step
	// lowQ makes n and x shortcuts (results.<id>.n, results.<id>.items[0])
	// instead of QuickJS expressions.
	lowQ bool
	// after, when set, is called after each top-level script step of seq
	// with its place, and its nodes follow that step.
	after func(t int) []embNode
	// bigRef, when set, is a step with a large result that key and n read
	// (A-typical).
	bigRef string
}

// embMix is a workload's calibration: the step scripts it cycles through
// (get and put are native, calc a Bun step), the scale of their CPU work in
// percent, and whether its expressions lean on the shortcuts. The target
// workloads differ in how much host CPU an invocation adds (0.31 to
// 0.79 ms) and in how much of their expression time QuickJS takes; most
// of the first is the native steps' realm, so the share of native steps
// sets it.
type embMix struct {
	kinds []string
	scale int
	lowQ  bool
}

var (
	embMixA = embMix{kinds: []string{"get", "calc", "get", "put"}, scale: 100}
	embMixC = embMix{kinds: []string{"get", "calc", "calc"}, scale: 45}
	embMixG = embMix{kinds: []string{"get", "calc", "calc", "calc", "calc"}, scale: 30, lowQ: true}
	embMixH = embMix{kinds: []string{"get", "calc", "calc"}, scale: 42}
)

func newEmbBuilder(m embMix) *embBuilder {
	// WL_BENCH_EMB_WORK scales the CPU work further, in percent.
	scale := m.scale
	if v, err := strconv.Atoi(os.Getenv("WL_BENCH_EMB_WORK")); err == nil && v > 0 {
		scale = scale * v / 100
	}
	w := func(n int) int { return n * scale / 100 }
	return &embBuilder{
		kinds: m.kinds,
		lowQ:  m.lowQ,
		work: map[string]embWork{
			"get":  {work: w(500), io: 1},
			"put":  {work: w(400), io: 1},
			"calc": {work: w(7500), io: 0},
		},
	}
}

func (b *embBuilder) next(prefix string) string {
	b.n++
	return fmt.Sprintf("%s%d", prefix, b.n)
}

// ref returns the id of the top-level script step back steps ago, or "" when
// there is none.
func (b *embBuilder) ref(back int) string {
	if len(b.scripts) < back {
		return ""
	}
	return b.scripts[len(b.scripts)-back]
}

// script is a script step with nexpr of its inputs as expressions (3 to 7):
// in turn key (QuickJS: a template), tenant (flow_input.tenant, a
// shortcut), prev (results.<the last step>, a shortcut), n (QuickJS:
// arithmetic on an earlier result), flag (results.<id>.ok, a shortcut),
// opts (flow_input.opts, a shortcut) and x (QuickJS: an array joined). The
// inputs that are not expressions are static. Every step takes the store
// as a resource.
func (b *embBuilder) script(kind string, nexpr int, top bool) embNode {
	id := b.next("s")
	r1, r2 := b.ref(1), b.ref(2)
	if r2 == "" {
		r2 = r1
	}
	w := b.work[kind]
	in := []embInput{{name: "store", res: "f/emb/store"}, {name: "api", res: fmt.Sprintf("f/emb/api%d", b.n%embAPIs+1)}}
	add := func(name, expr, static string, i int) {
		if i < nexpr && expr != "" {
			in = append(in, embInput{name: name, expr: expr})
		} else {
			in = append(in, embInput{name: name, static: static})
		}
	}
	// The first step has no earlier result to read: its inputs read the
	// flow input instead.
	keyExpr, prevExpr, nExpr := "`${flow_input.tenant}:"+id+"`", "flow_input.opts.fields", "flow_input.opts.limit + 1"
	flagExpr, xExpr := "flow_input.mode === \"full\"", "[flow_input.region, flow_input.tenant].join(\"/\")"
	if r1 != "" {
		keyExpr = "`${flow_input.tenant}:${results." + r1 + ".id}`"
		prevExpr = "results." + r1
		nExpr = "results." + r2 + ".n + 1"
		flagExpr = "results." + r1 + ".ok"
		xExpr = "[flow_input.region, results." + r2 + ".id].join(\"/\")"
		if b.lowQ {
			nExpr, xExpr = "results."+r2+".n", "results."+r2+".items[0]"
		}
		if b.bigRef != "" {
			keyExpr = "`${flow_input.tenant}:${results." + b.bigRef + ".id}`"
			nExpr = "results." + b.bigRef + ".words + 1"
		}
	}
	// The order in which inputs become expressions as nexpr grows.
	add("key", keyExpr, `"`+id+`"`, 0)
	add("tenant", "flow_input.tenant", `"t-static"`, 1)
	add("prev", prevExpr, "null", 2)
	add("n", nExpr, "1", 3)
	add("flag", flagExpr, "true", 4)
	add("opts", "flow_input.opts", "null", 5)
	add("x", xExpr, "null", 6)
	in = append(in,
		embInput{name: "work", static: strconv.Itoa(w.work)},
		embInput{name: "io", static: strconv.Itoa(w.io)},
		embInput{name: "log", static: strconv.Itoa(b.n % 3 / 2)},
	)
	if top {
		b.scripts = append(b.scripts, id)
		b.last = id
	}
	return embNode{kind: "script", id: id, script: kind, inputs: in}
}

// countExprs is how many of a script step's inputs are expressions: those
// after the first step, which has nothing to refer to, have nexpr.
func (n embNode) countExprs() int {
	c := 0
	for _, in := range n.inputs {
		if in.expr != "" {
			c++
		}
	}
	return c
}

// branch is a branchone: two predicates (QuickJS), the first false and the
// second true, whose branch runs a script step then an identity step; the
// default runs an identity step. It runs 3 steps, 1 invocation and 2
// expressions besides its script's.
func (b *embBuilder) branch(kind string, nexpr int) embNode {
	id := b.next("b")
	r := b.ref(1)
	if r == "" {
		panic("a branch needs an earlier step")
	}
	mk := func() []embNode {
		return []embNode{b.script(kind, nexpr, false), {kind: "identity", id: b.next("i")}}
	}
	return embNode{
		kind:     "branch",
		id:       id,
		preds:    []string{"results." + r + ".n < 0", "flow_input.mode === \"full\""},
		branches: [][]embNode{mk(), mk()},
		def:      []embNode{{kind: "identity", id: b.next("i")}},
	}
}

func (b *embBuilder) identity() embNode { return embNode{kind: "identity", id: b.next("i")} }

// seqSpec is a sequential workload's make-up: inv invocations, of which br
// sit in branches, ids identity steps, and the expressions of each
// invocation's script step (e7, e6, e5 and e4 of them with 7, 6, 5 and 4).
type seqSpec struct {
	inv, br, ids   int
	e7, e6, e5, e4 int
}

// exprPlan returns the expressions of the k-th script step of a spec.
func (s seqSpec) exprPlan() []int {
	var p []int
	for _, c := range []struct{ n, e int }{{s.e7, 7}, {s.e6, 6}, {s.e5, 5}, {s.e4, 4}} {
		for range c.n {
			p = append(p, c.e)
		}
	}
	return p
}

// seq builds a sequential list: script steps, with branches and identity
// steps spread evenly among them, and a script step last.
func (b *embBuilder) seq(s seqSpec) []embNode {
	plan := s.exprPlan()
	if len(plan) != s.inv {
		panic(fmt.Sprintf("expression plan for %d steps, %d invocations", len(plan), s.inv))
	}
	var out []embNode
	tops := s.inv - s.br
	brEvery, idEvery := 0, 0
	if s.br > 0 {
		brEvery = max(1, tops/(s.br+1))
	}
	if s.ids > 0 {
		idEvery = max(1, tops/(s.ids+1))
	}
	pi, br, ids, k := 0, 0, 0, 0
	nextExpr := func() int {
		e := plan[pi%len(plan)]
		pi++
		return e
	}
	for t := 0; t < tops; t++ {
		kind := b.kinds[k%len(b.kinds)]
		k++
		out = append(out, b.script(kind, nextExpr(), true))
		if t == tops-1 {
			break
		}
		if b.after != nil {
			out = append(out, b.after(t)...)
		}
		if brEvery > 0 && br < s.br && (t+1)%brEvery == 0 {
			out = append(out, b.branch(b.kinds[k%len(b.kinds)], nextExpr()))
			k++
			br++
		}
		if idEvery > 0 && ids < s.ids && (t+1)%idEvery == 0 {
			out = append(out, b.identity())
			ids++
		}
	}
	// Whatever is left goes in before the last step.
	last := out[len(out)-1]
	out = out[:len(out)-1]
	for ; br < s.br; br++ {
		out = append(out, b.branch(b.kinds[k%len(b.kinds)], nextExpr()))
		k++
	}
	for ; ids < s.ids; ids++ {
		out = append(out, b.identity())
	}
	return append(out, last)
}

// embParallelism is the C loops' parallelism: the target figures (each
// invocation's time inside the host grows from 1.32 ms in A to 1.84 ms in
// C20) put about two iterations in flight at once.
const embParallelism = 2

// loop is a parallel for-loop over results.<src>.items, embParallelism
// iterations at once: each iteration runs body, built by mk.
func (b *embBuilder) loop(src string, items int, mk func() []embNode) embNode {
	id := b.next("l")
	saved := b.scripts
	b.scripts = nil
	body := mk()
	b.scripts = saved
	return embNode{kind: "loop", id: id, iter: "results." + src + ".items", parallel: true, parallelism: embParallelism, items: items, body: body}
}

// itemBody is the C workloads' loop body for one item: 16 script steps of 4
// expressions (on the item and the flow input), a branch and an identity
// step: 19 steps, 16 invocations and 66 expressions.
func (b *embBuilder) itemBody() []embNode {
	var out []embNode
	k := 0
	add := func() {
		n := b.script(b.kinds[k%len(b.kinds)], 4, true)
		k++
		// The first step reads the item, so that the iteration depends on it.
		if len(b.scripts) == 1 {
			for i, in := range n.inputs {
				switch in.name {
				case "key":
					n.inputs[i] = embInput{name: "key", expr: "`${flow_input.tenant}:${flow_input.iter.value.id}`"}
				case "prev":
					n.inputs[i] = embInput{name: "prev", expr: "flow_input.iter.value"}
				case "n":
					n.inputs[i] = embInput{name: "n", expr: "flow_input.iter.index + 1"}
				}
			}
		}
		out = append(out, n)
	}
	for range 8 {
		add()
	}
	out = append(out, b.branch("get", 4))
	for range 7 {
		add()
	}
	return append(out, b.identity())
}

// bigChain is A-typical's chain: a step reads an item of about 65 KB from
// the store, two steps take it and pass it on with what they extract, and a
// last one keeps about 5 KB of it. Each takes the previous result whole, and
// three QuickJS expressions read the item (its id, its text's length,
// whether its markup has a title).
func (b *embBuilder) bigChain() []embNode {
	var out []embNode
	prev := b.ref(1)
	for lvl, kind := range []string{"bigsrc", "pass", "pass", "keep"} {
		id := b.next("s")
		in := []embInput{{name: "store", res: "f/emb/store"}}
		if lvl == 0 {
			in = append(in,
				embInput{name: "key", expr: "`${flow_input.tenant}:${results." + prev + ".id}`"},
				embInput{name: "tenant", expr: "flow_input.tenant"},
				embInput{name: "n", expr: "results." + prev + ".n + 1"},
				embInput{name: "opts", expr: "flow_input.opts"},
			)
		} else {
			p := b.last
			in = append(in,
				embInput{name: "item", expr: "results." + p},
				embInput{name: "key", expr: "`${results." + p + ".id}:" + kind + "`"},
				embInput{name: "n", expr: "results." + p + ".text.length"},
				embInput{name: "tenant", expr: "results." + p + ".html.includes(\"<title>\") ? flow_input.tenant : \"none\""},
			)
		}
		in = append(in, embInput{name: "work", static: "150"}, embInput{name: "io", static: "0"}, embInput{name: "log", static: "0"})
		out = append(out, embNode{kind: "script", id: id, script: kind, inputs: in})
		if lvl == 2 {
			b.bigRef = id
		}
		b.last = id
	}
	// Later steps refer to the chain's last step, which keeps id and n,
	// and read the item through its last passing step (bigRef).
	b.scripts = append(b.scripts, b.last)
	return out
}

// counts returns what one request of nodes runs, as the trace counts it:
// steps, invocations and expressions. A branch runs its second branch, after
// both predicates; a loop runs its body once per item.
func countNodes(nodes []embNode) (steps, invs, exprs int) {
	for _, n := range nodes {
		switch n.kind {
		case "script":
			steps, invs, exprs = steps+1, invs+1, exprs+n.countExprs()
		case "identity":
			steps++
		case "branch":
			s, i, e := countNodes(n.branches[1])
			steps, invs, exprs = steps+1+s, invs+i, exprs+2+e
		case "loop":
			s, i, e := countNodes(n.body)
			steps, invs, exprs = steps+1+n.items*s, invs+n.items*i, exprs+1+n.items*e
		}
	}
	return
}

// counts is countNodes for a batch of the workload's conc requests.
func (w embWorkload) counts() (steps, invs, exprs int) {
	s, i, e := countNodes(w.nodes)
	return s * w.conc, i * w.conc, e * w.conc
}

// embWorkloads are the workloads, by their target's names. The counts are
// the target's per request; H's are per batch of eight (the target counts
// H's 627 steps for eight requests in flight; H-lit takes them per request).
func embWorkloads() []embWorkload {
	seqW := func(name string, m embMix, s seqSpec, steps, inv, exprs int) embWorkload {
		b := newEmbBuilder(m)
		return embWorkload{name: name, nodes: b.seq(s), conc: 1, steps: steps, invocations: inv, expressions: exprs}
	}
	c := func(items, steps, inv, exprs int) embWorkload {
		b := newEmbBuilder(embMixC)
		// The base: 48 invocations (12 in branches), 10 identity steps, the
		// loop after the 24th step, over the items of a step that returns
		// them.
		nodes := b.seq(seqSpec{inv: 24, br: 6, ids: 5, e7: 6, e6: 18})
		src := b.script("get", 6, true)
		setItems(&src, items)
		nodes = append(nodes, src)
		nodes = append(nodes, b.loop(src.id, items, b.itemBody))
		nodes = append(nodes, b.seq(seqSpec{inv: 23, br: 6, ids: 5, e7: 5, e6: 18})...)
		return embWorkload{name: fmt.Sprintf("C%d", items), nodes: nodes, conc: 1, steps: steps, invocations: inv, expressions: exprs}
	}
	h := func() embWorkload {
		b := newEmbBuilder(embMixH)
		return embWorkload{name: "H", nodes: b.seq(seqSpec{inv: 48, br: 12, ids: 6, e6: 21, e5: 27}), conc: 8, steps: 627, invocations: 386, expressions: 2280}
	}
	// H-lit is H under the other, literal reading of its target counts:
	// 627 / 386 / 2,280 per request, eight requests in flight, so eight
	// times H's work per batch. Only run when named.
	hlit := func() embWorkload {
		b := newEmbBuilder(embMixH)
		return embWorkload{name: "H-lit", nodes: b.seq(seqSpec{inv: 384, br: 96, ids: 48, e6: 168, e5: 216}), conc: 8, steps: 627 * 8, invocations: 386 * 8, expressions: 2280 * 8}
	}
	atyp := func() embWorkload {
		b := newEmbBuilder(embMixA)
		// 88 script steps and 12 branches, with a chain of four after every
		// fourth script step, 18 in all; the steps after a chain read its
		// item in two QuickJS expressions (bigRef).
		chains := 0
		b.after = func(t int) []embNode {
			if (t+1)%4 != 0 || chains == 18 {
				return nil
			}
			chains++
			return b.bigChain()
		}
		nodes := b.seq(seqSpec{inv: 100, br: 12, ids: 33, e6: 25, e5: 75})
		return embWorkload{name: "A-typical", nodes: nodes, conc: 1, steps: 229, invocations: 172, expressions: 837}
	}
	return []embWorkload{
		seqW("A", embMixA, seqSpec{inv: 60, br: 12, ids: 12, e6: 51, e5: 9}, 96, 60, 375),
		seqW("B", embMixA, seqSpec{inv: 105, br: 15, ids: 20, e5: 56, e4: 49}, 155, 105, 506),
		c(1, 102, 64, 390),
		c(5, 178, 128, 654),
		c(20, 463, 368, 1644),
		seqW("D1", embMixA, seqSpec{inv: 68, br: 14, ids: 14, e6: 46, e5: 22}, 110, 68, 414),
		seqW("D2", embMixA, seqSpec{inv: 84, br: 15, ids: 21, e6: 9, e5: 75}, 135, 84, 459),
		seqW("E", embMixA, seqSpec{inv: 64, br: 13, ids: 12, e6: 44, e5: 20}, 102, 64, 390),
		seqW("F", embMixA, seqSpec{inv: 33, br: 8, ids: 9, e7: 6, e6: 27}, 58, 33, 220),
		seqW("G", embMixG, seqSpec{inv: 26, br: 10, ids: 4, e7: 18, e6: 8}, 50, 26, 194),
		h(),
		atyp(),
		hlit(),
	}
}

// setItems makes a script step return k items, which a loop then iterates.
func setItems(n *embNode, k int) {
	n.inputs = append(n.inputs, embInput{name: "k", static: strconv.Itoa(k)})
}

// flowYAML is the workload's flow.
func (w embWorkload) flowYAML() string {
	var b strings.Builder
	fmt.Fprintf(&b, "summary: Embedder shape %s\nvalue:\n  modules:\n", w.name)
	writeEmbNodes(&b, w.nodes, "    ")
	return b.String()
}

func writeEmbNodes(b *strings.Builder, nodes []embNode, ind string) {
	q := func(s string) string { return strconv.Quote(s) }
	for _, n := range nodes {
		fmt.Fprintf(b, "%s- id: %s\n%s  value:\n", ind, n.id, ind)
		v := ind + "    "
		switch n.kind {
		case "script":
			fmt.Fprintf(b, "%stype: script\n%spath: f/emb/%s\n%sinput_transforms:\n", v, v, n.script, v)
			for _, in := range n.inputs {
				switch {
				case in.expr != "":
					fmt.Fprintf(b, "%s  %s: {type: javascript, expr: %s}\n", v, in.name, q(in.expr))
				case in.res != "":
					fmt.Fprintf(b, "%s  %s: {type: static, value: %s}\n", v, in.name, q("$res:"+in.res))
				default:
					fmt.Fprintf(b, "%s  %s: {type: static, value: %s}\n", v, in.name, in.static)
				}
			}
		case "identity":
			fmt.Fprintf(b, "%stype: identity\n", v)
		case "branch":
			fmt.Fprintf(b, "%stype: branchone\n%sbranches:\n", v, v)
			for i, p := range n.preds {
				fmt.Fprintf(b, "%s  - expr: %s\n%s    modules:\n", v, q(p), v)
				writeEmbNodes(b, n.branches[i], v+"      ")
			}
			fmt.Fprintf(b, "%sdefault:\n", v)
			writeEmbNodes(b, n.def, v+"  ")
		case "loop":
			fmt.Fprintf(b, "%stype: forloopflow\n%siterator: {type: javascript, expr: %s}\n%sparallel: %t\n%sparallelism: {type: static, value: %d}\n%sskip_failures: false\n%smodules:\n", v, v, q(n.iter), v, n.parallel, v, n.parallelism, v, v)
			writeEmbNodes(b, n.body, v+"  ")
		}
	}
}

// program is the workload's in-process program: an ES module whose default
// export runs one request, calling each step's main as a plain function.
// Its expressions are the flow's, as JavaScript in the same scope: results,
// flow_input (with iter inside a loop) and previous_result.
func (w embWorkload) program(folder string) string {
	var b strings.Builder
	used := map[string]bool{}
	var walk func([]embNode)
	walk = func(ns []embNode) {
		for _, n := range ns {
			if n.kind == "script" {
				used[n.script] = true
			}
			for _, br := range n.branches {
				walk(br)
			}
			walk(n.def)
			walk(n.body)
		}
	}
	walk(w.nodes)
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range used {
			if !yield(k) {
				return
			}
		}
	})
	b.WriteString("// The in-process form of embedder shape " + w.name + " (bench_embed_test.go).\n")
	for _, s := range names {
		fmt.Fprintf(&b, "import { main as m_%s } from %s;\n", s, strconv.Quote(filepath.Join(folder, "f", "emb", s+".ts")))
	}
	b.WriteString("export default async function (flow_input, rt) {\n  const R = new Map();\n  const res = (p) => rt.res(R, p);\n  const results = {};\n  let previous_result = null;\n")
	writeEmbProgram(&b, w.nodes, "  ")
	b.WriteString("  return previous_result;\n}\n")
	return b.String()
}

func writeEmbProgram(b *strings.Builder, nodes []embNode, ind string) {
	for _, n := range nodes {
		switch n.kind {
		case "script":
			args := map[string]string{}
			for _, in := range n.inputs {
				switch {
				case in.expr != "":
					args[in.name] = "(" + in.expr + ")"
				case in.res != "":
					args[in.name] = "(await res(" + strconv.Quote(in.res) + "))"
				default:
					args[in.name] = in.static
				}
			}
			// The flow binds the arguments one at a time before the step
			// runs; so does this, in parameter order.
			var list []string
			fmt.Fprintf(b, "%s{\n", ind)
			for i, p := range embParams {
				if a, ok := args[p]; ok {
					fmt.Fprintf(b, "%s  const a%d = %s;\n", ind, i, a)
					list = append(list, fmt.Sprintf("a%d", i))
				} else {
					list = append(list, "undefined")
				}
			}
			fmt.Fprintf(b, "%s  const r = await m_%s(%s);\n%s  results.%s = r;\n%s  previous_result = r;\n%s}\n", ind, n.script, strings.Join(list, ", "), ind, n.id, ind, ind)
		case "identity":
			fmt.Fprintf(b, "%sresults.%s = previous_result;\n", ind, n.id)
		case "branch":
			for i, p := range n.preds {
				kw := "} else if"
				if i == 0 {
					kw = "if"
				}
				fmt.Fprintf(b, "%s%s (%s) {\n", ind, kw, p)
				writeEmbProgram(b, n.branches[i], ind+"  ")
			}
			fmt.Fprintf(b, "%s} else {\n", ind)
			writeEmbProgram(b, n.def, ind+"  ")
			fmt.Fprintf(b, "%s}\n%sresults.%s = previous_result;\n", ind, ind, n.id)
		case "loop":
			fmt.Fprintf(b, "%s{\n%s  const items = %s;\n", ind, ind, n.iter)
			fmt.Fprintf(b, "%s  const out = await rt.parallel(items, %d, (value, index) => (async (flow_input, results, previous_result) => {\n", ind, n.parallelism)
			writeEmbProgram(b, n.body, ind+"    ")
			fmt.Fprintf(b, "%s    return previous_result;\n%s  })({ ...flow_input, iter: { value, index } }, Object.create(results), previous_result));\n", ind, ind)
			fmt.Fprintf(b, "%s  results.%s = out;\n%s  previous_result = out;\n%s}\n", ind, n.id, ind, ind)
		}
	}
}

// embBurn is the steps' CPU work, in units of about a microsecond: mostly
// built-ins (rows of data serialised, parsed back and searched, as steps
// that shape data do), and a tenth in a plain JavaScript loop, whose cost
// depends on the JIT's warmth, which a native step's fresh realm loses.
const embBurn = `function burn(work: number, seed: string): number {
  const rows: unknown[] = [];
  for (let i = 0; i < 16; i++) rows.push({ id: seed + "-" + i, name: "row " + i, state: i % 3 ? "active" : "idle", tags: ["a", "b"], v: i * 7, meta: { k: i, s: "abcdefghijklmnop" } });
  let h = 0;
  for (let r = Math.max(1, Math.round(work / 25)); r > 0; r--) {
    const t = JSON.stringify(rows);
    const back = JSON.parse(t) as unknown[];
    h = (h + back.length + t.length + (t.match(/"idle"/g)?.length ?? 0)) | 0;
  }
  const t = JSON.stringify(rows[0]);
  for (let i = 0, n = work / 10; i < n; i++) for (let j = 0; j < t.length; j += 4) h = (h * 31 + t.charCodeAt(j)) | 0;
  return h;
}
`

// embSig is the step scripts' signature (embParams).
const embSig = `store: { url: string; delay: number }, api: { region?: string } | null, key: string, tenant: string, prev: { id?: string } | null, n: number, flag: boolean, opts: unknown, x: unknown, item: any, work: number, io: number, k: number, log: number`

// embFolder is the embedder shapes' sync folder: the step scripts and the
// workloads' flows. Most steps are native (fetch only, a realm per
// invocation); calc is a Bun step.
func embFolder() map[string]string {
	files := map[string]string{
		"wmill.yaml": "defaultTs: bun\nincludes:\n  - f/**\n",
		"f/emb/get.ts": `//native
// Reads records from the store and works on them.
` + embBurn + `
export async function main(` + embSig + `) {
  let got = 0;
  for (let i = 0; i < io; i++) {
    const r = await fetch(store.url + "/kv/" + encodeURIComponent(tenant) + "/" + encodeURIComponent(key) + "?d=" + store.delay + "&k=" + (k || 3));
    const v = await r.json();
    got += v.items.length;
  }
  const h = burn(work, key);
  if (log) console.log("get " + key + ": " + got + " records");
  const items: { id: string; w: number }[] = [];
  for (let i = 0; i < (k || 3); i++) items.push({ id: tenant + "-" + n + "-" + i, w: (h >>> i) & 255 });
  return { id: tenant + "-" + n, n, ok: flag !== false, key, got, h, prev: prev?.id ?? null, items };
}
`,
		"f/emb/put.ts": `//native
// Writes a record to the store.
` + embBurn + `
export async function main(` + embSig + `) {
  for (let i = 0; i < io; i++) {
    const r = await fetch(store.url + "/kv/" + encodeURIComponent(tenant) + "/" + encodeURIComponent(key) + "?d=" + store.delay, { method: "PUT", body: JSON.stringify({ key, n, prev: prev?.id ?? null, opts }) });
    await r.json();
  }
  const h = burn(work, key);
  if (log) console.log("put " + key);
  return { id: tenant + "-" + n, n, ok: true, key, h, items: [{ id: tenant + "-" + n + "-0", w: h & 255 }] };
}
`,
		"f/emb/calc.ts": `// Works on its inputs in Bun.
` + embBurn + `
export async function main(` + embSig + `) {
  const h = burn(work, key + ":" + ((opts as { limit?: number } | null)?.limit ?? 0));
  if (log) console.log("calc " + key);
  return { id: tenant + "-" + n, n, ok: flag !== false, key, h, x: x ?? null, items: [{ id: tenant + "-" + n + "-0", w: h & 255 }] };
}
`,
		"f/emb/bigsrc.ts": `//native
// Reads an item of about 65 KB from the store.
` + embBurn + `
export async function main(` + embSig + `) {
  const r = await fetch(store.url + "/big/" + encodeURIComponent(key) + "?d=" + store.delay);
  const v = await r.json();
  burn(work, key);
  return { id: v.id, n, ok: true, html: v.html, text: v.text, meta: v.meta };
}
`,
		"f/emb/pass.ts": `//native
// Takes an item, extracts from it and passes it on.
` + embBurn + `
export async function main(` + embSig + `) {
  const title = /<title>([^<]*)<\/title>/.exec(item.html)?.[1] ?? "";
  const links = (item.html.match(/href="/g) ?? []).length;
  const words = item.text.split(/\s+/).length;
  burn(work, key);
  return { ...item, n, title, links, words };
}
`,
		"f/emb/keep.ts": `//native
// Keeps about 5 KB of an item.
` + embBurn + `
export async function main(` + embSig + `) {
  burn(work, key);
  return { id: item.id, n, ok: true, title: item.title, links: item.links, words: item.words, summary: item.text.slice(0, 5000), items: [{ id: item.id + "-0", w: n & 255 }] };
}
`,
	}
	for _, w := range embWorkloads() {
		files["f/emb/"+embFlowName(w.name)+".flow/flow.yaml"] = w.flowYAML()
	}
	// WL_BENCH_EMB_NATIVE=0 runs the native steps as Bun steps, which
	// separates what a realm per invocation costs.
	if os.Getenv("WL_BENCH_EMB_NATIVE") == "0" {
		for name, body := range files {
			files[name] = strings.TrimPrefix(body, "//native\n")
		}
	}
	return files
}

func embFlowName(name string) string {
	return "w_" + strings.ReplaceAll(strings.ToLower(name), "-", "_")
}

// embArgs is every workload's flow input.
var embArgs = json.RawMessage(`{"tenant":"tenant-a","region":"eu-1","mode":"full","opts":{"limit":20,"sort":"asc","fields":["id","name","state"],"retries":2}}`)

var (
	embOnce   sync.Once
	embDir    string
	embBundle string
	embErr    error
	// embStoreURL is the store's origin, once started.
	embStoreURL string
)

// embEnv are the store's settings: the delay of a store call and of an API
// (resource) read, in microseconds.
func embEnv() (kvDelay, apiDelay string) {
	kvDelay, apiDelay = os.Getenv("WL_BENCH_EMB_KV_US"), os.Getenv("WL_BENCH_EMB_API_US")
	if kvDelay == "" {
		kvDelay = "300"
	}
	if apiDelay == "" {
		apiDelay = "1200"
	}
	return
}

// embSetup builds the embedder shapes' bundle and starts the store, once
// per test binary.
func embSetup(b testing.TB, bun string) {
	b.Helper()
	embOnce.Do(func() {
		if embDir, embErr = os.MkdirTemp("", "wl-emb-"); embErr != nil {
			return
		}
		folder := filepath.Join(embDir, "folder")
		for name, body := range embFolder() {
			p := filepath.Join(folder, filepath.FromSlash(name))
			if embErr = os.MkdirAll(filepath.Dir(p), 0o755); embErr != nil {
				return
			}
			if embErr = os.WriteFile(p, []byte(body), 0o644); embErr != nil {
				return
			}
		}
		repo, err := filepath.Abs("../..")
		if err != nil {
			embErr = err
			return
		}
		embBundle = filepath.Join(embDir, "bundle")
		if _, embErr = bundle.Build(context.Background(), bundle.Config{
			Folder:         folder,
			EngineVersion:  resolveVersion(),
			Bun:            bun,
			Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
			HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
			Out:            embBundle,
			Work:           filepath.Join(embDir, "work"),
		}); embErr != nil {
			return
		}
		embStoreURL, embErr = startEmbStore()
	})
	if embErr != nil {
		b.Fatal(embErr)
	}
}

// embStoreEnv puts the test binary in store mode (benchStore).
const embStoreEnv = "WL_BENCH_EMB_STORE"

// startEmbStore starts the store, the test binary in store mode, and
// returns its origin. It runs until this process exits (its stdin closes).
func startEmbStore() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	kv, _ := embEnv()
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), embStoreEnv+"=1", "WL_BENCH_EMB_KV_US="+kv)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = in // kept open: the store ends when this process does
	embStoreStdin = in
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		cmd.Process.Kill()
		return "", fmt.Errorf("the store did not start: %v", err)
	}
	return "http://" + strings.TrimSpace(line), nil
}

var embStoreStdin io.WriteCloser

// benchStore is the store: an HTTP server on the loopback interface that
// answers after a delay (the d query parameter, in microseconds):
//
//   - GET /res/<path>: a resource, whose value holds the store's origin and
//     the delay of a store call (WL_BENCH_EMB_KV_US);
//   - GET /kv/...: k records (3 by default);
//   - PUT /kv/...: reads the body, answers ok;
//   - GET /big/<key>: an item of about 65 KB, ≈ 49 KB of markup and ≈ 15 KB
//     of text.
//
// It prints its address and serves until its standard input ends.
func benchStore() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	origin := "http://" + ln.Addr().String()
	kvDelay := os.Getenv("WL_BENCH_EMB_KV_US")
	big := embBigItem()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d, err := strconv.Atoi(r.URL.Query().Get("d")); err == nil && d > 0 {
			time.Sleep(time.Duration(d) * time.Microsecond)
		}
		w.Header().Set("content-type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/res/"):
			fmt.Fprintf(w, `{"type":"c_emb_store","value":{"url":%q,"delay":%s,"path":%q,"region":"eu-1"}}`, origin, kvDelay, strings.TrimPrefix(r.URL.Path, "/res/"))
		case strings.HasPrefix(r.URL.Path, "/kv/") && r.Method == http.MethodGet:
			k, _ := strconv.Atoi(r.URL.Query().Get("k"))
			if k <= 0 {
				k = 3
			}
			var sb strings.Builder
			sb.WriteString(`{"items":[`)
			for i := range k {
				if i > 0 {
					sb.WriteByte(',')
				}
				fmt.Fprintf(&sb, `{"id":"r-%d","name":"record %d","state":"active","v":%d}`, i, i, i*7)
			}
			sb.WriteString(`]}`)
			io.WriteString(w, sb.String())
		case strings.HasPrefix(r.URL.Path, "/kv/"):
			n, _ := io.Copy(io.Discard, r.Body)
			fmt.Fprintf(w, `{"ok":true,"n":%d}`, n)
		case strings.HasPrefix(r.URL.Path, "/big/"):
			fmt.Fprintf(w, `{"id":%q,%s}`, strings.TrimPrefix(r.URL.Path, "/big/"), big)
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(ln)
	fmt.Println(ln.Addr().String())
	io.Copy(io.Discard, os.Stdin)
	return 0
}

// embBigItem is the body of a /big item without its id: markup, text and
// metadata.
func embBigItem() string {
	var html, text strings.Builder
	html.WriteString("<html><head><title>Synthetic page</title></head><body>")
	for i := 0; html.Len() < 49<<10; i++ {
		fmt.Fprintf(&html, `<div class="row r%d"><a href="/item/%d">Item %d</a><p>Paragraph %d of the synthetic page, with some words in it.</p></div>`, i%9, i, i, i)
	}
	html.WriteString("</body></html>")
	for i := 0; text.Len() < 15<<10; i++ {
		fmt.Fprintf(&text, "Sentence %d of the synthetic text, which a step reads and keeps a part of. ", i)
	}
	h, _ := json.Marshal(html.String())
	t, _ := json.Marshal(text.String())
	return fmt.Sprintf(`"html":%s,"text":%s,"meta":{"lang":"en","size":%d}`, h, t, len(h)+len(t))
}

// inproc is the in-process side: one Bun process running
// testdata/benchhost/inproc.ts, driven over its standard input and output.
type inproc struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

type inprocReply struct {
	Elapsed []float64       `json:"elapsed"` // ns, per request
	CPU     float64         `json:"cpu"`     // µs, the batch's
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
}

func startInproc(b testing.TB, bun string) *inproc {
	b.Helper()
	_, apiDelay := embEnv()
	cmd := exec.Command(bun, "run", "testdata/benchhost/inproc.ts")
	cmd.Env = append(os.Environ(), "WL_BENCH_STORE_URL="+embStoreURL, "WL_BENCH_EMB_API_US="+apiDelay)
	if v := os.Getenv("WL_BENCH_INPROC_BUN_OPTIONS"); v != "" {
		cmd.Env = append(cmd.Env, "BUN_OPTIONS="+v)
	}
	// The steps' console output goes to its standard output, discarded; its
	// replies come on file descriptor 3.
	cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		b.Fatal(err)
	}
	out, w, err := os.Pipe()
	if err != nil {
		b.Fatal(err)
	}
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		b.Fatal(err)
	}
	w.Close()
	p := &inproc{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20)}
	b.Cleanup(func() {
		in.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	})
	return p
}

func (p *inproc) call(b testing.TB, msg any) inprocReply {
	b.Helper()
	line, _ := json.Marshal(msg)
	if _, err := p.in.Write(append(line, '\n')); err != nil {
		b.Fatalf("in-process side: %v", err)
	}
	text, err := p.out.ReadBytes('\n')
	if err != nil {
		b.Fatalf("in-process side: %v", err)
	}
	var r inprocReply
	if err := json.Unmarshal(text, &r); err != nil {
		b.Fatalf("in-process side: %v: %.300s", err, text)
	}
	if r.Error != "" {
		b.Fatalf("in-process side: %s", r.Error)
	}
	return r
}

// embRun is one windmill-lite request's record.
type embRun struct {
	elapsed time.Duration
	steps   int
	invs    int
	exprs   int
	expr    time.Duration
	harness []time.Duration
	result  json.RawMessage
}

// countTrace reads what the target counts from a run's trace: steps run (its
// records), host invocations (harness spans), expressions (expression
// spans) and their time, and each invocation's harness span.
func countTrace(tr *wl.Trace) embRun {
	var r embRun
	for _, s := range tr.Steps {
		if !s.Superseded {
			r.steps++
		}
	}
	for _, sp := range tr.Spans {
		switch sp.Kind {
		case engine.SpanHarness:
			r.invs++
			r.harness = append(r.harness, sp.Duration)
		case engine.SpanExpression:
			r.exprs++
			r.expr += sp.Duration
		}
	}
	return r
}

// embRequests is how many measured requests (batches, for H) each side runs.
func embRequests() int {
	if n, err := strconv.Atoi(os.Getenv("WL_BENCH_EMB_N")); err == nil && n > 0 {
		return n
	}
	return 50
}

// embWanted reports whether the workload is among WL_BENCH_EMB (names
// separated by commas), or every workload without it.
func embWanted(name string) bool {
	list := os.Getenv("WL_BENCH_EMB")
	if list == "" {
		return name != "H-lit"
	}
	return slices.Contains(strings.Split(list, ","), name)
}

// BenchmarkEmbedder runs each embedder shape on windmill-lite and in
// process, interleaved, embRequests() requests each (batches of conc), the
// side that goes first alternating, after warm requests on both. One
// iteration measures the whole set; run it with -benchtime 1x.
//
// WL_BENCH_EMB selects workloads; WL_BENCH_EMB_N the requests;
// WL_BENCH_EMB_KV_US and WL_BENCH_EMB_API_US the store's and the API's
// delays; WL_BENCH_EMB_SIDE=wl or inproc runs one side only (for profiles).
func BenchmarkEmbedder(b *testing.B) {
	bun := benchSetup(b)
	embSetup(b, bun)
	for _, w := range embWorkloads() {
		if !embWanted(w.name) {
			continue
		}
		b.Run("w="+w.name, func(b *testing.B) {
			b.StopTimer()
			for range b.N {
				measureEmb(b, bun, w)
			}
		})
	}
}

func measureEmb(b *testing.B, bun string, w embWorkload) {
	side := os.Getenv("WL_BENCH_EMB_SIDE")
	doWL, doIn := side != "inproc", side != "wl"
	var rt *wl.Runtime
	if doWL {
		rt = benchRuntimeOn(b, bun, "host-emb", embBundle, func(o *wl.Options) {
			o.ReadCache = os.Getenv("WL_BENCH_READ_CACHE") != "off"
			o.Host.SkipArgsFile = os.Getenv("WL_BENCH_ARGS_FILE") == "skip"
		})
		benchSignalProbes.Store(rt, true)
	}
	var ip *inproc
	if doIn {
		ip = startInproc(b, bun)
		dir := filepath.Join(embDir, "programs")
		os.MkdirAll(dir, 0o755)
		file := filepath.Join(dir, embFlowName(w.name)+".js")
		if err := os.WriteFile(file, []byte(w.program(filepath.Join(embDir, "folder"))), 0o644); err != nil {
			b.Fatal(err)
		}
		ip.call(b, map[string]any{"op": "load", "name": w.name, "file": file})
	}
	path := "f/emb/" + embFlowName(w.name)
	wlBatch := func() ([]embRun, error) {
		runs := make([]embRun, w.conc)
		_, err := bench.Workers(w.conc, 1, func(i int) error {
			t0 := time.Now()
			res, err := rt.Run(context.Background(), path, embArgs, wl.RunOptions{})
			el := time.Since(t0)
			if err == nil && res.Status != wl.StatusSuccess {
				err = fmt.Errorf("%s: %s: %.400s", path, res.Status, res.Result)
			}
			if err != nil {
				return err
			}
			r := countTrace(res.Trace)
			r.elapsed, r.result = el, res.Result
			runs[i] = r
			return nil
		})
		return runs, err
	}
	inBatch := func() inprocReply {
		return ip.call(b, map[string]any{"op": "run", "name": w.name, "conc": w.conc, "args": embArgs})
	}
	// Warm both sides, and check that they give the same result.
	for k := range 5 {
		var wr []embRun
		var ir inprocReply
		if doWL {
			var err error
			if wr, err = wlBatch(); err != nil {
				b.Fatal(err)
			}
		}
		if doIn {
			ir = inBatch()
		}
		if k == 0 && doWL && doIn {
			var a, c any
			json.Unmarshal(wr[0].result, &a)
			json.Unmarshal(ir.Result, &c)
			if !reflect.DeepEqual(a, c) {
				b.Fatalf("the two sides' results differ:\nwindmill-lite %.400s\nin process    %.400s", wr[0].result, ir.Result)
			}
		}
	}
	n := embRequests()
	var (
		wlRuns            []embRun
		wlElapsed, inEl   []time.Duration
		engCPU, inCPU     time.Duration
		batchSteps        []int
		batchExpr         []time.Duration
		hostBefore, after probed
	)
	load := loadAverage()
	if doWL {
		hostBefore = probe(b, rt, false)
	}
	// The host's and the in-process side's CPU by thread (Linux).
	hostPID, inPID := 0, 0
	if doWL {
		dir, _ := benchWorkdirs.Load(rt)
		if pid, ok := benchHostPIDs.Load(dir); ok {
			hostPID = pid.(int)
		}
	}
	if doIn {
		inPID = ip.cmd.Process.Pid
	}
	hostThr0, inThr0 := threadCPU(hostPID), threadCPU(inPID)
	gc0 := gcMetrics()
	// WL_BENCH_EMB_PPROF=FILE profiles this process's CPU over the measured
	// requests only, so that its shares apply to the CPU measured over the
	// same interval.
	if f := os.Getenv("WL_BENCH_EMB_PPROF"); f != "" {
		out, err := os.Create(f)
		if err != nil {
			b.Fatal(err)
		}
		if err := pprof.StartCPUProfile(out); err != nil {
			b.Fatal(err)
		}
		defer out.Close()
	}
	t0 := time.Now()
	for i := range n {
		for _, first := range []bool{i%2 == 0, i%2 == 1} {
			if first && doWL {
				e0 := engineCPU()
				runs, err := wlBatch()
				engCPU += engineCPU() - e0
				if err != nil {
					b.Fatal(err)
				}
				steps, expr := 0, time.Duration(0)
				for _, r := range runs {
					wlRuns = append(wlRuns, r)
					wlElapsed = append(wlElapsed, r.elapsed)
					steps += r.steps
					expr += r.expr
				}
				batchSteps = append(batchSteps, steps)
				batchExpr = append(batchExpr, expr)
			}
			if !first && doIn {
				r := inBatch()
				inCPU += time.Duration(r.CPU) * time.Microsecond
				for _, e := range r.Elapsed {
					inEl = append(inEl, time.Duration(e))
				}
			}
		}
	}
	if doWL {
		after = probe(b, rt, false)
	}
	gc1 := gcMetrics()
	t1 := time.Now()
	if os.Getenv("WL_BENCH_EMB_PPROF") != "" {
		pprof.StopCPUProfile()
	}
	// The measured interval in wall-clock time, which Bun's --cpu-prof
	// timestamps use, so that a host profile can be cut to it.
	fmt.Printf("emb-interval w=%s start-us=%d end-us=%d host-pid=%d inproc-pid=%d\n", w.name, t0.UnixMicro(), t1.UnixMicro(), hostPID, inPID)
	reportThreads := func(prefix string, d map[string]time.Duration, steps int) {
		for name, v := range d {
			if steps > 0 && v > 0 {
				b.ReportMetric(float64(v)/1e3/float64(steps), prefix+name+"-us/step")
			}
		}
	}
	ms := func(d time.Duration) float64 { return float64(d) / 1e6 }
	us := func(d time.Duration) float64 { return float64(d) / 1e3 }
	b.ReportMetric(load, "load1")
	b.ReportMetric(loadAverage(), "load1-end")
	if doIn {
		b.ReportMetric(ms(bench.Percentile(inEl, 0.50)), "inproc-p50-ms")
		b.ReportMetric(ms(bench.Percentile(inEl, 0.95)), "inproc-p95-ms")
	}
	if !doWL {
		return
	}
	var steps, invs, exprs int
	var harness []time.Duration
	for _, r := range wlRuns {
		steps += r.steps
		invs += r.invs
		exprs += r.exprs
		harness = append(harness, r.harness...)
	}
	perBatch := func(x int) float64 { return float64(x) / float64(n) }
	b.ReportMetric(perBatch(steps), "steps/req")
	b.ReportMetric(perBatch(steps)/float64(w.conc), "steps/run")
	b.ReportMetric(perBatch(invs), "inv/req")
	b.ReportMetric(perBatch(exprs), "expr/req")
	stepsPerBatch := float64(steps) / float64(n)
	p50, p95 := bench.Percentile(wlElapsed, 0.50), bench.Percentile(wlElapsed, 0.95)
	b.ReportMetric(ms(p50), "wl-p50-ms")
	b.ReportMetric(ms(p95), "wl-p95-ms")
	hostCPU := after.cpu - hostBefore.cpu
	b.ReportMetric(us(engCPU)/float64(steps), "engine-cpu-us/step")
	// The engine's collector over the measured requests (both sides ran
	// then; only windmill-lite's allocates in this process).
	b.ReportMetric((gc1[0]-gc0[0])/float64(steps), "gc/step")
	b.ReportMetric((gc1[1]-gc0[1])*1e6/float64(steps), "gc-cpu-us/step")
	if hostThr0 != nil {
		reportThreads("thr-host-", threadDelta(hostThr0, threadCPU(hostPID)), steps)
	}
	if inThr0 != nil {
		reportThreads("thr-inproc-", threadDelta(inThr0, threadCPU(inPID)), steps)
	}
	b.ReportMetric(us(hostCPU)/float64(steps), "host-cpu-us/step")
	b.ReportMetric(ms(engCPU+hostCPU)/float64(n), "wl-cpu-ms/req")
	b.ReportMetric(ms(bench.Percentile(batchExpr, 0.50)), "expr-ms/req")
	b.ReportMetric(us(bench.Mean(harness)), "host-us/inv")
	b.ReportMetric(us(bench.Percentile(harness, 0.50)), "host-p50-us/inv")
	if doIn {
		ip50, ip95 := bench.Percentile(inEl, 0.50), bench.Percentile(inEl, 0.95)
		b.ReportMetric(ms(p50-ip50), "gap-p50-ms")
		b.ReportMetric(ms(p95-ip95), "gap-p95-ms")
		b.ReportMetric(float64(p50)/float64(ip50), "ratio-p50")
		b.ReportMetric(us(p50-ip50)/stepsPerBatch, "gap-us/step")
		// The p50 gap is one request's (a run's); per step of that request
		// (for H, a batch holds eight).
		b.ReportMetric(us(p50-ip50)/(stepsPerBatch/float64(w.conc)), "gap-us/run-step")
		b.ReportMetric(us(inCPU)/float64(steps), "inproc-cpu-us/step")
		b.ReportMetric(us(hostCPU-inCPU)/float64(steps), "extra-host-cpu-us/step")
		b.ReportMetric(ms(inCPU)/float64(n), "inproc-cpu-ms/req")
	}
	_ = batchSteps
}

// TestEmbShapes checks that each embedder shape runs its target counts per
// request, as countNodes reads its nodes (BenchmarkEmbedder reports the
// trace's), and that its flow and program are well formed.
func TestEmbShapes(t *testing.T) {
	for _, w := range embWorkloads() {
		s, i, e := w.counts()
		t.Logf("%-9s steps %4d (%4d)  invocations %4d (%4d)  expressions %5d (%5d)", w.name, s, w.steps, i, w.invocations, e, w.expressions)
		if d := func(a, b int) float64 { return float64(a-b) / float64(b) }; abs(d(s, w.steps)) > 0.01 || abs(d(i, w.invocations)) > 0.01 || abs(d(e, w.expressions)) > 0.01 {
			t.Errorf("%s: steps %d, invocations %d, expressions %d; the target's are %d, %d, %d", w.name, s, i, e, w.steps, w.invocations, w.expressions)
		}
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// gcMetrics reads the runtime's count of finished GC cycles and its
// estimate of the CPU time the collector has spent, in seconds
// (runtime/metrics).
func gcMetrics() [2]float64 {
	ms := []metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}}
	metrics.Read(ms)
	var out [2]float64
	if ms[0].Value.Kind() == metrics.KindUint64 {
		out[0] = float64(ms[0].Value.Uint64())
	}
	if ms[1].Value.Kind() == metrics.KindFloat64 {
		out[1] = ms[1].Value.Float64()
	}
	return out
}

// BenchmarkArgsFileOps measures, directly, the CPU of writing a step's
// args file the way harness.Process.writeArgs does (create a temporary file
// in the working directory, write, close, rename over args.json): user and
// system CPU of this process per write, from getrusage, beside wall time. The directory is
// under TMPDIR, as the host's working directory is.
func BenchmarkArgsFileOps(b *testing.B) {
	for _, size := range []int{256, 1 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			dir := b.TempDir()
			text := []byte(`{"x":"` + strings.Repeat("a", size-8) + `"}`)
			var r0, r1 syscall.Rusage
			syscall.Getrusage(syscall.RUSAGE_SELF, &r0)
			b.ResetTimer()
			for range b.N {
				f, err := os.CreateTemp(dir, ".args-*.json")
				if err != nil {
					b.Fatal(err)
				}
				_, err = f.Write(text)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
				if err == nil {
					err = os.Rename(f.Name(), filepath.Join(dir, "args.json"))
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			syscall.Getrusage(syscall.RUSAGE_SELF, &r1)
			tv := func(t syscall.Timeval) float64 { return float64(t.Sec)*1e6 + float64(t.Usec) }
			b.ReportMetric((tv(r1.Utime)-tv(r0.Utime))/float64(b.N), "user-us/op")
			b.ReportMetric((tv(r1.Stime)-tv(r0.Stime))/float64(b.N), "sys-us/op")
		})
	}
}

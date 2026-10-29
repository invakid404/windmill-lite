package engine

import (
	"cmp"
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// A for loop (milestone N3a) runs as a container: it evaluates its iterator
// once, then runs its body once per element, sequentially or in parallel,
// and its result is the array of its iterations' results in index order
// (CF-2 to CF-13). A sequential loop whose body is one plain step collapses
// (CF-8): each iteration runs that step as a job of the loop's own flow.
// Every other iteration runs its body as a child flow (CF-1).

// errIterator marks an iterator or parallelism that fails the loop's start;
// its text is the chaining error's (CF-23).
var errIterator = errors.New("engine: the loop cannot start")

// forLoop runs module i of fr, a for loop, from its iterator to its
// completion; o is its outcome so far (its record's site). The loop's own
// stop_after_if is a break (CF-11), and its stop_after_all_iters_if a stop
// of the flow it is in (CF-13).
func (r *run) forLoop(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, o outcome) outcome {
	m := fr.modules[i]
	l := m.ForLoop
	site := fr.sites[i]
	// A loop with no body returns [] without iterating, whatever its
	// iterator (CF-4); reading: the iterator is then not evaluated either.
	var items [][]byte
	if len(l.Modules) > 0 {
		var text string
		var err error
		if items, text, err = r.iterator(ctx, fr, m, site, prev, prevID); err != nil {
			if ctx.Err() != nil {
				o.rec.Status, o.rec.Module, o.end = conformance.StepNotRun, conformance.ModuleNotRun, endInterrupted
				return o
			}
			return r.chaining(fr, i, o, text)
		}
	}
	if len(items) == 0 {
		// An empty container completes at once, with no child: its result
		// is [] and its stop_after_if a stop of the flow, not a break
		// (CF-29); its stop_after_all_iters_if is not evaluated (CF-V2).
		o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried = conformance.StepSuccess, conformance.ModuleSuccess, jsonEmpty, jsonEmpty
		o.carried = jsonEmpty
		r.recordJob(fr, i, jsonEmpty)
		return r.stop(ctx, fr, i, site, o)
	}
	if l.Parallel {
		return r.parallelLoop(ctx, fr, i, prev, prevID, items, o)
	}
	return r.sequentialLoop(ctx, fr, i, prev, items, o)
}

// iterator evaluates a for loop's iterator (CF-2, JE-30), once, with the
// scope of skip_if (JE-22), and returns its elements in their stored form:
// Windmill stores the evaluated array and reads each iteration's element
// from it. A failure returns the chaining error's text: the evaluator's
// error in its generic form, the array check's, the ai one's, or the
// database's when jsonb refuses the array (a NUL, FX-48; an unpaired
// surrogate or a number beyond numeric's range, FX-51), which fails the
// write before any iteration starts.
func (r *run) iterator(ctx context.Context, fr *frame, m *ir.Module, site conformance.SitePath, prev jsonval.Value, prevID string) ([][]byte, string, error) {
	it := m.ForLoop.Iterator
	var text []byte
	switch it.Type {
	case ir.Static:
		text = orNull(it.Value)
	case ir.Javascript:
		v, err := r.evaluate(ctx, fr, it.Expr, site, r.startScope(fr, prev, prevID))
		if err != nil {
			return nil, "Error: " + evalText(err), err
		}
		text = v.Producer()
	default:
		return nil, "AI input transform not supported for iterator", errIterator
	}
	if _, ok := elements(text); !ok {
		return nil, "Expected an array value in the iterator expression, found: " + serdeSequenceError(text), errIterator
	}
	stored, err := jsonval.FromProducer(text).Stored()
	if err != nil {
		return nil, databaseError(err).Error(), errIterator
	}
	items, _ := elements(stored)
	return items, "", nil
}

// iterations are a loop's results so far, in index order, with the
// pointers of the errors windmill-lite generated among them, which a
// comparison needs (ADR 0011, StepRecord.ResultNested).
type iterations struct {
	values []jsonval.Value
	nested []string
}

// add appends an iteration's result: a generated error at the iteration's
// index, and the generated errors inside its result below that index.
func (it *iterations) add(e flowEnd) {
	at := "/" + strconv.Itoa(len(it.values))
	it.values = append(it.values, e.result)
	if e.origin.Generated() {
		it.nested = append(it.nested, at)
	}
	for _, p := range e.nested {
		it.nested = append(it.nested, at+p)
	}
}

// sequentialLoop runs a sequential loop's iterations one after another
// (CF-5, CF-8, CF-11, CF-12). prev is the loop's previous_result, which a
// collapsed iteration chains (CF-8 item 5).
func (r *run) sequentialLoop(ctx context.Context, fr *frame, i int, prev jsonval.Value, items [][]byte, o outcome) outcome {
	m := fr.modules[i]
	l := m.ForLoop
	// What the run starts once an iteration has ended as e: the next
	// iteration after a success or a failure skip_failures tolerates, else
	// what follows the loop, which a failure that skip_failures does not
	// tolerate fails, and a stop breaks (CF-5, CF-12). The loop's own
	// stop_after_if, which may break it, is not known yet: the next
	// iteration counts.
	after := func(k int) func(flowEnd) bool {
		return func(e flowEnd) bool {
			failed := e.status == conformance.RunFailure
			if !e.stopped && !e.errorStop && (!failed || l.SkipFailures) && k < len(items)-1 {
				return true
			}
			return fr.afterModule(i, failed && !l.SkipFailures)
		}
	}
	// A collapsed iteration's previous_result is the step before the loop's
	// result, then the previous iteration's; a loop at the first module of
	// its flow gives every iteration the flow's input (JE-36 rules 1 and 5).
	iterPrev := prev
	if i == 0 {
		iterPrev = fr.input
	}
	var it iterations
	failed, ran := false, false
	var stopErr error
	r.progress(fr, i, nil)
	for k, item := range items {
		if st, ended := r.iterationEnded(ctx); ended {
			return r.loopInterrupted(o, &it, ran, st)
		}
		ran = true
		var e flowEnd
		if l.Collapse {
			c := r.collapsedFrame(fr, i, k, item, iterPrev)
			c.after = after(k)
			e = r.runList(ctx, c)
			if e.chain != "" {
				// The push of the iteration's job failed: a chaining error
				// of the loop's flow, at its pickup only for the first
				// iteration of a loop that is its first module.
				return r.chainingAt(fr, k == 0 && i == 0, o, e.chain)
			}
			if i > 0 {
				iterPrev = e.result
			}
		} else {
			c, text := r.childFrame(fr, i, k, item, false)
			if c == nil {
				// The push of the iteration's child flow failed.
				return r.chainingAt(fr, k == 0 && i == 0, o, text)
			}
			r.addChild(c)
			c.after = after(k)
			e = r.runList(ctx, c)
			// The child flow's completion refreshes the loop's flow's
			// ping (JE-2).
			r.ping(fr)
		}
		if ended(e.status) {
			return r.loopInterrupted(o, &it, ran, r.endedStatus(ctx, e.status))
		}
		it.add(e)
		r.progress(fr, i, it.values)
		if e.status == conformance.RunFailure {
			// A failed iteration stops the loop unless skip_failures; an
			// error stop breaks it either way, and the loop fails unless
			// skip_failures (CF-5, CF-12 item 4).
			if e.errorStop || !l.SkipFailures {
				failed = !l.SkipFailures
				break
			}
			continue
		}
		if e.stopped {
			// A clean stop in the body breaks the loop (CF-12 item 2).
			break
		}
		// The loop's own stop_after_if, after each successful iteration, is
		// a break; skip_if_stopped and error_message have no effect (CF-11).
		if s := m.StopAfterIf; s != nil {
			v, err := r.predicate(ctx, fr, s.Expr, fr.sites[i], r.iterStopScope(fr, s.Expr, e.result, it.values))
			if err != nil {
				if ctx.Err() != nil {
					return r.loopInterrupted(o, &it, ran, conformance.RunCanceled)
				}
				stopErr = err
				break
			}
			if v {
				break
			}
		}
	}
	return r.loopDone(ctx, fr, i, o, &it, failed, stopErr)
}

// parallelLoop runs a parallel loop's iterations at once, each a child
// flow with its own results scope (CF-7, CF-19): parallelism, evaluated
// per child, holds the children at and beyond it, and completions release
// them. The loop completes when every iteration has, with no fail-fast
// (Windmill's continue-all, ADR 0008), and its stop_after_if is not
// evaluated.
func (r *run) parallelLoop(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, items [][]byte, o outcome) outcome {
	m := fr.modules[i]
	l := m.ForLoop
	n := len(items)
	// parallelism is evaluated per child, as each is created (JE-V1); -1
	// is no limit.
	limits := make([]int, n)
	for k := range limits {
		limits[k] = -1
		if l.Parallelism == nil {
			continue
		}
		lim, text, err := r.parallelism(ctx, fr, m, fr.sites[i], prev, prevID)
		if err != nil {
			if ctx.Err() != nil {
				o.rec.Status, o.rec.Module, o.end = conformance.StepNotRun, conformance.ModuleNotRun, endInterrupted
				return o
			}
			return r.chaining(fr, i, o, text)
		}
		limits[k] = lim
	}
	// More than one child in a same_worker flow fails the loop's start
	// (CF-7); the flag is the root's, which child flows copy (CF-1).
	if r.f.SameWorker && m.Sleep == nil && n > 1 {
		return r.chaining(fr, i, o, "Bad request: Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker")
	}
	// Every child is created at once (CF-7): a child whose input cannot be
	// stored fails the loop's start, before any runs.
	children := make([]*frame, n)
	for k, item := range items {
		c, text := r.childFrame(fr, i, k, item, true)
		if c == nil {
			return r.chaining(fr, i, o, text)
		}
		children[k] = c
	}
	// An iteration's last module is followed by the iterations still held,
	// which the run would start, and by what follows the loop.
	var held atomic.Int32
	var failedChild atomic.Bool
	held.Store(int32(n))
	for _, c := range children {
		r.addChild(c)
		// An iteration's end is followed by the iterations still held,
		// which the run would start whatever it was (continue-all), and by
		// what follows the loop, which a failure skip_failures does not
		// tolerate fails (CF-7): this iteration's, or one that has already
		// ended; reading: iterations still running are taken to succeed.
		c.after = func(e flowEnd) bool {
			failed := e.status == conformance.RunFailure || failedChild.Load()
			return held.Load() > 0 || fr.afterModule(i, failed && !l.SkipFailures)
		}
	}
	// A child k is held when k is at or beyond its own parallelism N, and
	// becomes eligible once k - N + 1 iterations have completed (CF-7,
	// JE-V1). Held children wait by that count, in index order among
	// equals; the others start at once.
	type heldChild struct{ k, at int }
	var queue []heldChild
	for k, lim := range limits {
		if lim >= 0 && k >= lim {
			queue = append(queue, heldChild{k, k - lim + 1})
		}
	}
	slices.SortStableFunc(queue, func(a, b heldChild) int { return cmp.Compare(a.at, b.at) })
	ends := make([]flowEnd, n)
	started := make([]bool, n)
	done := make(chan int, n)
	nStarted, running, completed, q := 0, 0, 0, 0
	stopped := conformance.RunStatus("")
	launch := func(k int) bool {
		if st, ended := r.iterationEnded(ctx); ended {
			stopped = st
			return false
		}
		started[k] = true
		nStarted++
		running++
		held.Add(-1)
		go func() {
			ends[k] = r.runList(ctx, children[k])
			if ends[k].status == conformance.RunFailure {
				failedChild.Store(true)
			}
			done <- k
		}()
		return true
	}
	for k, lim := range limits {
		if (lim < 0 || k < lim) && !launch(k) {
			break
		}
	}
	release := func() {
		for stopped == "" && q < len(queue) && queue[q].at <= completed {
			if !launch(queue[q].k) {
				return
			}
			q++
		}
	}
	release()
	for running > 0 || (stopped == "" && nStarted < n) {
		if running == 0 {
			// Every remaining child is held and nothing runs to release
			// one (parallelism 0, CF-7): only the run's end stops the wait,
			// by a latch, a halt or the cancellation (the hold's 14-day
			// deadline is out of scope, Q-CF-10).
			select {
			case <-ctx.Done():
			case <-r.wake:
			}
			stopped = r.endedStatus(ctx, conformance.RunCanceled)
			continue
		}
		<-done
		running--
		completed++
		// The loop's goroutine refreshes its flow's ping as each child
		// flow completes (JE-2).
		r.ping(fr)
		release()
	}
	var it iterations
	failed := false
	for k, e := range ends {
		if !started[k] {
			continue
		}
		if ended(e.status) && stopped == "" {
			stopped = e.status
		}
		it.add(e)
		// A clean stop ends only its own iteration (CF-12 item 2); every
		// iteration that did not succeed fails the loop unless
		// skip_failures (CF-7).
		if e.status == conformance.RunFailure {
			failed = !l.SkipFailures
		}
	}
	if stopped != "" || nStarted < n {
		return r.loopInterrupted(o, &it, nStarted > 0, r.endedStatus(ctx, stopped))
	}
	return r.loopDone(ctx, fr, i, o, &it, failed, nil)
}

// loopDone completes an active loop: its result is the array of its
// iterations' results; a loop that failed fails with it (CF-5), and a
// loop whose stop_after_if threw fails as such a step does (FX-21). Then
// its stop_after_all_iters_if, evaluated once with that array, stops the
// flow the loop is in like an ordinary stop_after_if; an evaluation error
// is an error stop (CF-13, JE-27). Reading: on a loop that failed only an
// error stop changes anything, since with no failure module (N1b) a clean
// stop and the failure both end the flow with the array.
func (r *run) loopDone(ctx context.Context, fr *frame, i int, o outcome, it *iterations, failed bool, stopErr error) outcome {
	m := fr.modules[i]
	arr := arrayOf(it.values)
	r.recordJob(fr, i, arr)
	o.rec.Ran = true
	o.rec.Result, o.rec.Carried, o.carried = arr, arr, arr
	o.rec.ResultNested, o.rec.CarriedNested, o.nested = it.nested, it.nested, it.nested
	o.rec.Status, o.rec.Module = conformance.StepSuccess, conformance.ModuleSuccess
	if failed || stopErr != nil {
		o.rec.Status, o.rec.Module, o.end = conformance.StepFailure, conformance.ModuleFailure, endFailed
	}
	if stopErr != nil {
		return stopFailed(o, stopErr)
	}
	s := m.StopAfterAllItersIf
	if s == nil {
		return o
	}
	v, err := r.predicate(ctx, fr, s.Expr, fr.sites[i], r.stopScope(fr, s.Expr, arr))
	switch {
	case err != nil && ctx.Err() != nil:
		o.end = endInterrupted
		return o
	case err != nil:
		o = errorStop(o, "Error evaluating stop_after_all_iters_if expression `"+s.Expr.Text+"`: "+predicateText(err), s, arr)
	case !v || failed && (s.SkipIfStopped || s.ErrorMessage == nil):
		return o
	default:
		o = applyStop(s, o, arr)
	}
	if o.end.failed() {
		o.rec.Status = conformance.StepFailure
	}
	return o
}

// loopInterrupted is a loop that the latch, the cancellation or a halt
// stopped before it completed (status says which); its result is the
// array so far.
func (r *run) loopInterrupted(o outcome, it *iterations, ran bool, status conformance.RunStatus) outcome {
	o.rec.Ran = ran
	o.rec.Status, o.rec.Module, o.rec.Result, o.rec.ResultNested = conformance.StepCanceled, conformance.ModuleFailure, arrayOf(it.values), it.nested
	o.end = endInterrupted
	if status == conformance.RunHalted {
		o.end = endHalted
	}
	return o
}

// iterationEnded reports whether a host signal ends the run before an
// iteration starts, in ADR 0008's order: the latch, the cancellation (both
// Start's), then a halt that a module elsewhere in the run decided.
func (r *run) iterationEnded(ctx context.Context) (conformance.RunStatus, bool) {
	if st, end := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil}).Status(); end {
		return st, true
	}
	if r.halted.Load() {
		return conformance.RunHalted, true
	}
	return "", false
}

// endedStatus is the status a loop that host signals cut short ends the
// run with: the strongest signal by iterationEnded's order, whichever
// iteration met it first, or fallback.
func (r *run) endedStatus(ctx context.Context, fallback conformance.RunStatus) conformance.RunStatus {
	if st, end := r.iterationEnded(ctx); end {
		return st
	}
	return fallback
}

// ended reports whether a flow ended because of a host signal.
func ended(st conformance.RunStatus) bool {
	return st == conformance.RunLatched || st == conformance.RunCanceled || st == conformance.RunHalted
}

// progress records a sequential loop's job results so far, which a
// results read of the loop from its own flow, a collapsed iteration's,
// sees (JE-18 step 2, CF-19 route 5): a started loop module has a job
// list, empty at first, that grows with each iteration. Reading: the
// reference does not record such a read.
func (r *run) progress(fr *frame, i int, entries []jsonval.Value) {
	arr := arrayOf(entries)
	r.mu.Lock()
	fr.jobs[i] = arr
	r.mu.Unlock()
}

// iterStopScope is the scope of a sequential loop's stop_after_if after an
// iteration (CF-11, JE-26): the iteration's result, and all_iters, the
// results so far, when the text names it.
func (r *run) iterStopScope(fr *frame, x *expr.Expr, result jsonval.Value, entries []jsonval.Value) *expr.Scope {
	s := r.stopScope(fr, x, result)
	if strings.Contains(x.Trimmed, "all_iters") {
		s.Context = append(s.Context, expr.Named{Name: "all_iters", Value: arrayOf(entries)})
	}
	return s
}

// parallelism evaluates a parallel loop's parallelism for one child
// (JE-V1, CF-7): with the incoming P as result and previous_result, the
// containing flow's input unchanged, flow_env and results. Its value must
// parse as a u16; -1 is none, as a static null is (OF-V2; reading, as for
// a static timeout). A failure returns the chaining error's text.
func (r *run) parallelism(ctx context.Context, fr *frame, m *ir.Module, site conformance.SitePath, prev jsonval.Value, prevID string) (int, string, error) {
	t := m.ForLoop.Parallelism
	if t.Type == ir.Static {
		raw := orNull(t.Value)
		if string(raw) == "null" {
			return -1, "", nil
		}
		n, msg := parseU16(raw)
		if msg != "" {
			return 0, "Error parsing static value as u16: " + msg, errIterator
		}
		return n, "", nil
	}
	s := &expr.Scope{
		PreviousID: prevID,
		Context:    []expr.Named{{Name: "previous_result", Value: prev}, {Name: "result", Value: prev}},
		FlowInput:  fr.input,
		FlowEnv:    r.envOf(fr),
		Results:    r.results(fr),
		Helpers:    helpers{r.e.platform, r.rootID},
	}
	v, err := r.evaluate(ctx, fr, t.Expr, site, s)
	if err != nil {
		return 0, "Error during evaluation of expression `" + t.Expr.Text + "`:\n" + evalText(err), err
	}
	n, msg := parseU16(v.Producer())
	if msg != "" {
		return 0, "Error parsing result as u16: " + msg + ". Value was: " + string(v.Producer()), errIterator
	}
	return n, "", nil
}

// childFrame creates the child flow of iteration k of module i of fr, a
// for loop (CF-1): its input is the loop's flow input plus iter (CF-3),
// which it reads back from storage (JE-44); its first module's
// previous_result is that input (CF-9). A sequential iteration writes into
// its parent's results scope and stays in its innermost flow scope; a
// parallel one starts both (CF-19, WA-V1), and has an rng of its own. An
// input jsonb refuses fails the child's push (FX-48, FX-51): childFrame
// then returns nil and the chaining error's text. Reading: the iterator's
// own storage check rules that out, since the loop's input is stored too.
func (r *run) childFrame(fr *frame, i, k int, item []byte, parallel bool) (*frame, string) {
	m := fr.modules[i]
	stored, err := iterInput(fr.input, k, item).Stored()
	if err != nil {
		return nil, databaseError(err).Error()
	}
	c := newFrame(m.ForLoop.Modules, fr.sites[i].Child(conformance.Iteration(k)))
	c.site, c.child, c.id, c.pinged = fr.sites[i].Child(conformance.Iteration(k)), true, newUUID(), r.clock.Now()
	c.input = jsonval.FromProducer(flowInputText(stored, fr.rng))
	c.path, c.scope, c.nodes, c.rng = fr.path+"/loop-"+strconv.Itoa(k), fr.scope, fr.nodes, fr.rng
	if parallel {
		c.path, c.scope, c.nodes = fr.path+"/forloop-"+strconv.Itoa(k), c.id, newNodeScope(fr.nodes)
		c.rng = rand.New(rand.NewPCG(fr.rng.Uint64(), fr.rng.Uint64()))
	}
	return c, ""
}

// addChild records a child flow the run created in its trace.
func (r *run) addChild(c *frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Children = append(r.trace.Children, ChildFlow{Site: c.site, ID: c.id, Modules: c.ids()})
}

// collapsedFrame is iteration k of module i of fr, a collapsed for loop
// (CF-8): its body step runs as a job of fr, with the loop's flow input
// plus iter as flow_input, prev as previous_result and an empty previous
// id, and registers nothing under its own id.
func (r *run) collapsedFrame(fr *frame, i, k int, item []byte, prev jsonval.Value) *frame {
	m := fr.modules[i]
	c := newFrame(m.ForLoop.Modules, fr.sites[i].Child(conformance.Iteration(k)))
	c.collapsed, c.iter, c.parent = m, k, fr
	c.input, c.prev = iterInput(fr.input, k, item), prev
	c.path, c.id, c.scope, c.nodes, c.rng = fr.path, fr.id, fr.scope, fr.nodes, fr.rng
	return c
}

// iterInput is the flow input of iteration k (CF-3, JE-37): the loop's
// flow input plus iter = {"index": k, "value": item}. An iter already
// there moves to iter_parent, an iter_parent to iter_parent_parent, and so
// on.
func iterInput(parent jsonval.Value, k int, item []byte) jsonval.Value {
	ms, _ := members(parent.Producer())
	at := map[string]int{}
	for j, m := range ms {
		at[m.key] = j
	}
	depth := 0
	for name := "iter"; ; name += "_parent" {
		if _, ok := at[name]; !ok {
			break
		}
		depth++
	}
	for d := depth - 1; d >= 0; d-- {
		name := "iter" + strings.Repeat("_parent", d)
		j := at[name]
		ms[j].key, ms[j].rawKey = name+"_parent", jsonString(name+"_parent")
	}
	iter := `{"index":` + strconv.Itoa(k) + `,"value":` + string(item) + `}`
	ms = append(ms, rawMember{key: "iter", rawKey: []byte(`"iter"`), value: []byte(iter)})
	return jsonval.FromProducer(writeMembers(ms))
}

// arrayOf is a loop's result: the array of its iterations' results as the
// worker writes it after decoding each child's stored result (JE-44): no
// spaces, keys in jsonb's order, and numbers re-printed by the backend's
// JSON library (FX-42 rule 2).
func arrayOf(entries []jsonval.Value) jsonval.Value {
	var b strings.Builder
	b.WriteByte('[')
	for k, e := range entries {
		if k > 0 {
			b.WriteByte(',')
		}
		text := []byte("null")
		if !e.IsZero() {
			text = e.Producer()
			if s, err := e.Stored(); err == nil {
				text = s
			}
		}
		b.Write(reprintNumbers(text))
	}
	b.WriteByte(']')
	return jsonval.FromProducer([]byte(b.String()))
}

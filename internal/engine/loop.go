package engine

import (
	"cmp"
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// A for loop (milestone N3a) runs as a container: it evaluates its iterator
// once, then runs its body once per element, sequentially or in parallel,
// and its result is the array of its iterations' results in index order
// (CF-2 to CF-13). A while loop (N3b) runs its body sequentially until
// something ends it (CF-10). A sequential loop whose body is one plain step
// collapses (CF-8): each iteration runs that step as a job of the loop's
// own flow. Every other iteration runs its body as a child flow (CF-1).

// errIterator marks an iterator or parallelism that fails the loop's start;
// its text is the chaining error's (CF-23).
var errIterator = errors.New("engine: the loop cannot start")

// container runs module i of fr, a container that runs (CF-1): a for loop
// or a while loop, a branchone or a branchall. b is how the module started:
// b.due delays its first child, the sleep of the module before it (CF-21),
// and b.base, on a retry of a loop, is the input its children start from
// (CF-22 rule 3).
func (r *run) container(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, o outcome, b begin) outcome {
	switch fr.modules[i].Kind {
	case ir.KindForLoop:
		return r.forLoop(ctx, fr, i, prev, prevID, o, b)
	case ir.KindWhileLoop:
		return r.whileLoop(ctx, fr, i, prev, prevID, o, b)
	case ir.KindBranchOne:
		return r.branchOne(ctx, fr, i, prev, prevID, o, b)
	}
	return r.branchAll(ctx, fr, i, prev, prevID, o, b)
}

// forLoop runs module i of fr, a for loop, from its iterator to its
// completion; o is its outcome so far (its record's site). The loop's own
// stop_after_if is a break (CF-11), and its stop_after_all_iters_if a stop
// of the flow it is in (CF-13).
func (r *run) forLoop(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, o outcome, b begin) outcome {
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
		return r.emptyContainer(ctx, fr, i, o)
	}
	if l.Parallel {
		return r.parallelLoop(ctx, fr, i, prev, prevID, items, o, b)
	}
	return r.sequentialLoop(ctx, fr, i, prev, prevID, len(items), func(k int) []byte { return items[k] }, o, b)
}

// whileLoop runs module i of fr, a while loop (CF-10): its iterations run
// one after another, iteration k's element being k (CF-3), until its
// stop_after_if breaks it (CF-11), a stop in its body does (CF-12), an
// iteration fails that skip_failures does not tolerate (CF-5), or the run
// ends; nothing else ends it. A loop with no body returns [] at once, as
// an empty for loop does (CF-10, CF-29).
func (r *run) whileLoop(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, o outcome, b begin) outcome {
	if len(fr.modules[i].WhileLoop.Modules) == 0 {
		return r.emptyContainer(ctx, fr, i, o)
	}
	return r.sequentialLoop(ctx, fr, i, prev, prevID, -1, func(k int) []byte { return []byte(strconv.Itoa(k)) }, o, b)
}

// emptyContainer completes module i of fr, a container that creates no
// child (an empty iterator, an empty loop body, a branchall with no
// branches), at once: its result is [] and its stop_after_if a stop of the
// flow, not a break (CF-29); its stop_after_all_iters_if is not evaluated
// (CF-V2).
func (r *run) emptyContainer(ctx context.Context, fr *frame, i int, o outcome) outcome {
	o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried = conformance.StepSuccess, conformance.ModuleSuccess, jsonEmpty, jsonEmpty
	o.carried = jsonEmpty
	r.recordJob(fr, i, jsonEmpty)
	return r.stop(ctx, fr, i, fr.sites[i], o)
}

// loopBody is what a sequential loop runs, for a for loop or a while loop:
// its body, its skip_failures and whether it collapses (CF-8).
type loopBody struct {
	modules      []*ir.Module
	skipFailures bool
	collapse     bool
}

// bodyOf returns the body of m, a for loop or a while loop.
func bodyOf(m *ir.Module) loopBody {
	if l := m.WhileLoop; l != nil {
		return loopBody{modules: l.Modules, skipFailures: l.SkipFailures, collapse: l.Collapse}
	}
	l := m.ForLoop
	return loopBody{modules: l.Modules, skipFailures: l.SkipFailures, collapse: l.Collapse}
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
// comparison needs (ADR 0011, StepRecord.ResultNested), and the last child
// job that failed and the last that completed, which a retry of the loop
// starts from and records (CF-22).
type iterations struct {
	values       []jsonval.Value
	nested       []string
	failed, last *childJob
}

// childJob is a child job of a container as a retry of the container sees
// it (CF-22): a child flow (an iteration's or a branch's), or a collapsed
// iteration's body job; its site (the child flow's, or the body job's
// record), its stored input, and how it ended.
type childJob struct {
	site  conformance.SitePath
	flow  bool
	input jsonval.Value
	end   flowEnd
}

// completed notes a child job that completed: the last one, and the last
// one that failed.
func (it *iterations) completed(j *childJob) {
	it.last = j
	if j.end.status == conformance.RunFailure {
		it.failed = j
	}
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

// sequentialLoop runs the iterations of a sequential for loop or a while
// loop one after another (CF-5, CF-8, CF-10, CF-11, CF-12): n of them, or,
// for a while loop, n < 0, as many as run until something ends the loop;
// item gives iteration k's element. prev is the loop's previous_result,
// which a collapsed iteration chains (CF-8 item 5), and prevID its
// preceding id; b is how the loop started (container).
func (r *run) sequentialLoop(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, n int, item func(k int) []byte, o outcome, b begin) outcome {
	m := fr.modules[i]
	l := bodyOf(m)
	// A retry's re-entry pushes the loop's first child from the input of
	// its last failed child; the next iterations are pushed from the
	// loop's own flow input (CF-22 rule 3, recorded by
	// cf-22/collapsed_input).
	base := func(k int) jsonval.Value {
		if k == 0 && !b.base.IsZero() {
			return b.base
		}
		return fr.input
	}
	// A collapsed iteration's previous_result is the step before the loop's
	// result, then the previous iteration's; a loop at the first module of
	// its flow gives every iteration the flow's input (JE-36 rules 1 and 5).
	// The flow's last result, which the loop's skip_if sees before each
	// iteration after the first, follows the same rule, a child flow's
	// result standing for a collapsed job's (CF-21).
	iterPrev := prev
	if i == 0 {
		iterPrev = fr.input
	}
	var it iterations
	failed, ran := false, false
	var stopErr error
	r.progress(fr, i, nil)
	for k := 0; n < 0 || k < n; k++ {
		if st, ended := r.iterationEnded(ctx); ended {
			return r.loopInterrupted(o, &it, ran, st)
		}
		if k > 0 && m.SkipIf != nil {
			// A sequential loop's skip_if is evaluated again before every
			// iteration after the first (CF-21, Q-CF-6); a throw is a chaining
			// error of a later transition (FX-15).
			skip, err := r.predicate(ctx, fr, m.SkipIf, fr.sites[i], r.startScope(fr, iterPrev, prevID))
			switch {
			case err != nil && ctx.Err() != nil:
				return r.loopInterrupted(o, &it, ran, conformance.RunCanceled)
			case err != nil:
				return r.chainingAt(fr, false, o, predicateText(err))
			case skip:
				return r.loopSkipped(ctx, fr, i, o, iterPrev)
			}
		}
		ran = true
		var e flowEnd
		var job *childJob
		if l.collapse {
			c := r.collapsedFrame(fr, i, k, base(k), item(k), iterPrev, prev, prevID)
			if k == 0 {
				c.due = b.due
			}
			e = r.runList(ctx, c)
			if e.chain != "" {
				// The push of the iteration's job failed: a chaining error
				// of the loop's flow, at its pickup only for the first
				// iteration of a loop that is its first module. The job was
				// never pushed, so Windmill has no site for it.
				r.supersede(fr.sites[i].Child(conformance.Iteration(k)), conformance.SitePath{}, conformance.SitePath{})
				return r.chainingAt(fr, k == 0 && i == 0, o, e.chain)
			}
			job = &childJob{site: c.sites[0], input: e.args, end: e}
		} else {
			// The loop's timeout is evaluated for each child it pushes, and
			// a failure is a chaining error of its flow (CF-21); nothing
			// enforces it on a child flow (CF-27).
			if text, err := r.childTimeout(ctx, fr, i, prev, prevID); err != nil {
				if ctx.Err() != nil {
					return r.loopInterrupted(o, &it, ran, conformance.RunCanceled)
				}
				return r.chainingAt(fr, k == 0 && i == 0, o, text)
			}
			c, text := r.childFrame(fr, i, k, base(k), item(k), false)
			if c == nil {
				// The push of the iteration's child flow failed.
				return r.chainingAt(fr, k == 0 && i == 0, o, text)
			}
			if k == 0 {
				c.due = b.due
			}
			r.addChild(c)
			e = r.runList(ctx, c)
			job = &childJob{site: c.site, flow: true, input: c.input, end: e}
			// The child flow's completion refreshes the loop's flow's
			// ping (JE-2).
			r.ping(fr)
		}
		if ended(e.status) {
			st := r.endedStatus(ctx, e.status)
			if st == conformance.RunCanceled {
				it.add(canceledChild(ctx, job))
			}
			return r.loopInterrupted(o, &it, ran, st)
		}
		if i > 0 {
			iterPrev = e.result
		}
		it.add(e)
		it.completed(job)
		r.progress(fr, i, it.values)
		if e.status == conformance.RunFailure {
			// A failed iteration stops the loop unless skip_failures; an
			// error stop breaks it either way, and the loop fails unless
			// skip_failures (CF-5, CF-12 item 4).
			if e.errorStop || !l.skipFailures {
				failed = !l.skipFailures
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

// loopSkipped ends module i of fr, a sequential loop whose skip_if held
// before an iteration after the first, as a skipped identity job of p, the
// flow's last result (CF-21, Q-CF-6): the identity job replaces the loop's
// state, so its iterations so far are lost (recorded by cf-21/skip_mid),
// and the flow goes on with p.
func (r *run) loopSkipped(ctx context.Context, fr *frame, i int, o outcome, p jsonval.Value) outcome {
	r.supersede(fr.sites[i], conformance.SitePath{}, conformance.SitePath{})
	stored, err := storeMembers(objectOf(member{"previous_result", p.Producer()}))
	if err != nil {
		return r.chainingAt(fr, false, o, err.Error())
	}
	o.rec.RecordedArgs, o.rec.ResolvedArgs, o.rec.Start = stored, stored, r.clock.Now()
	return r.identityJob(ctx, fr, i, fr.sites[i], o, stored, true)
}

// childTimeout evaluates the timeout of module i of fr, a container, for a
// child flow it pushes (CF-21): with the container's P, preceding id and
// flow input, as the step's own timeout is (FX-24). Nothing enforces it on
// a child flow (CF-27); a failure returns the chaining error's text.
func (r *run) childTimeout(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string) (string, error) {
	m := fr.modules[i]
	if m.Timeout == nil {
		return "", nil
	}
	if _, err := r.moduleTimeout(ctx, fr, m, fr.sites[i], prev, prevID); err != nil {
		return err.Error(), err
	}
	return "", nil
}

// parallelLoop runs a parallel loop's iterations at once, each a child
// flow with its own results scope (CF-7, CF-19): parallelism, evaluated
// per child, holds the children at and beyond it, and completions release
// them. The loop completes when every iteration has, with no fail-fast
// (Windmill's continue-all, ADR 0008), and its stop_after_if is not
// evaluated.
func (r *run) parallelLoop(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, items [][]byte, o outcome, b begin) outcome {
	m := fr.modules[i]
	l := m.ForLoop
	n := len(items)
	// The loop's timeout, then its parallelism, are evaluated per child, as
	// each is created (CF-21, JE-V1); -1 is no limit. Nothing enforces the
	// timeout on a child flow (CF-27).
	limits := make([]int, n)
	for k := range limits {
		limits[k] = -1
		text, err := r.childTimeout(ctx, fr, i, prev, prevID)
		if err == nil && l.Parallelism != nil {
			limits[k], text, err = r.parallelism(ctx, fr, m, fr.sites[i], prev, prevID)
		}
		if err != nil {
			if ctx.Err() != nil {
				o.rec.Status, o.rec.Module, o.end = conformance.StepNotRun, conformance.ModuleNotRun, endInterrupted
				return o
			}
			return r.chaining(fr, i, o, text)
		}
	}
	// More than one child in a same_worker flow fails the loop's start
	// (CF-7); the flag is the root's, which child flows copy (CF-1).
	if r.f.SameWorker && m.Sleep == nil && n > 1 {
		return r.chaining(fr, i, o, "Bad request: Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker")
	}
	// Every child is created at once (CF-7), in one push: a child whose
	// input cannot be stored fails the loop's start, before any runs. On a
	// retry's re-entry that push starts every child from the input of the
	// last failed child (CF-22 rule 3); the sleep before the loop delays
	// every child it pushes (CF-21, recorded by cf-21/sleep_before_par).
	base := fr.input
	if !b.base.IsZero() {
		base = b.base
	}
	children := make([]*frame, n)
	for k, item := range items {
		c, text := r.childFrame(fr, i, k, base, item, true)
		if c == nil {
			return r.chaining(fr, i, o, text)
		}
		c.due = b.due
		children[k] = c
	}
	for _, c := range children {
		r.addChild(c)
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
	start := func(k int) {
		started[k] = true
		nStarted++
		running++
		go func() {
			ends[k] = r.runList(ctx, children[k])
			done <- k
		}()
	}
	launch := func(k int) bool {
		if st, ended := r.iterationEnded(ctx); ended {
			stopped = st
			return false
		}
		start(k)
		return true
	}
	// The children not held start together, after one check of the host
	// signals (CF-7), so none waits on another's outcome to start; each
	// held child is checked when it is released.
	if st, ended := r.iterationEnded(ctx); ended {
		stopped = st
	} else {
		for k, lim := range limits {
			if lim < 0 || k < lim {
				start(k)
			}
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
	var it iterations
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
		k := <-done
		running--
		completed++
		// The last child to fail, in the order the loop received their
		// completions, is the one a retry starts from (CF-22 rule 3).
		if !ended(ends[k].status) {
			it.completed(&childJob{site: children[k].site, flow: true, input: children[k].input, end: ends[k]})
		}
		// The loop's goroutine refreshes its flow's ping as each child
		// flow completes (JE-2).
		r.ping(fr)
		release()
	}
	failed := false
	for k, e := range ends {
		if started[k] && ended(e.status) && stopped == "" {
			stopped = e.status
		}
	}
	canceled := stopped != "" && r.endedStatus(ctx, stopped) == conformance.RunCanceled
	for k, e := range ends {
		switch {
		case canceled && (!started[k] || ended(e.status)):
			// The cancellation reaches every child, held ones included,
			// which completes as failed with the Canceled error without
			// running its body (CF-28, recorded by cf-28/held).
			it.add(canceledChild(ctx, nil))
			continue
		case !started[k]:
			continue
		}
		it.add(e)
		// A clean stop ends only its own iteration (CF-12 item 2); every
		// iteration that did not succeed fails the loop unless
		// skip_failures (CF-7).
		if e.status == conformance.RunFailure {
			failed = !l.SkipFailures
		}
	}
	if stopped != "" {
		return r.loopInterrupted(o, &it, nStarted > 0 || canceled, r.endedStatus(ctx, stopped))
	}
	return r.loopDone(ctx, fr, i, o, &it, failed, nil)
}

// canceledChild is the result a canceled child job leaves its container
// (CF-28): a child flow's is the Canceled error of its flow, and a
// collapsed body job's its own, the killed step's.
func canceledChild(ctx context.Context, j *childJob) flowEnd {
	if j != nil && !j.flow && !j.end.result.IsZero() {
		return flowEnd{status: conformance.RunFailure, result: j.end.result, origin: j.end.origin}
	}
	return flowEnd{status: conformance.RunFailure, result: canceledError(cancellationOf(ctx)), origin: OriginEngine}
}

// loopDone completes an active loop or branchall: its result is the array
// of its children's results; one that failed fails with it (CF-5, CF-15),
// and a loop whose stop_after_if threw fails as such a step does (FX-21).
// Then its stop_after_all_iters_if, evaluated once with that array, stops
// the flow it is in like an ordinary stop_after_if; an evaluation error is
// an error stop (CF-13, JE-27). On one that failed, a clean stop keeps the
// failure and still stops the flow, even under continue_on_error
// (endFailedStop, FX-2 completion rule 7.1).
func (r *run) loopDone(ctx context.Context, fr *frame, i int, o outcome, it *iterations, failed bool, stopErr error) outcome {
	m := fr.modules[i]
	arr := arrayOf(it.values)
	r.recordJob(fr, i, arr)
	o.rec.Ran = true
	o.rec.Result, o.rec.Carried, o.carried = arr, arr, arr
	o.rec.ResultNested, o.rec.CarriedNested, o.nested = it.nested, it.nested, it.nested
	o.rec.Status, o.rec.Module = conformance.StepSuccess, conformance.ModuleSuccess
	o.failedChild, o.lastChild = it.failed, it.last
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
	case !v:
		return o
	case failed && (s.SkipIfStopped || s.ErrorMessage == nil):
		o.end = endFailedStop
		return o
	default:
		o = applyStop(s, o, arr)
	}
	if o.end.failed() {
		o.rec.Status = conformance.StepFailure
	}
	return o
}

// loopInterrupted is a loop or a branchall that the latch, the
// cancellation or a halt stopped before it completed (status says which);
// its result is the array so far. A canceled one completes as its flow job
// does on Windmill once its children have reported, a failure whose array
// holds the Canceled error of each child the cancellation reached (CF-28,
// recorded by cf-28/held); the latch and a halt are windmill-lite's own,
// and leave it canceled.
func (r *run) loopInterrupted(o outcome, it *iterations, ran bool, status conformance.RunStatus) outcome {
	o.rec.Ran = ran
	o.rec.Status, o.rec.Module, o.rec.Result, o.rec.ResultNested = conformance.StepCanceled, conformance.ModuleFailure, arrayOf(it.values), it.nested
	o.end = endInterrupted
	switch status {
	case conformance.RunHalted:
		o.end = endHalted
	case conformance.RunCanceled:
		o.rec.Status = conformance.StepFailure
	}
	return o
}

// iterationEnded reports whether a host signal ends the run before an
// iteration or a branch starts, in ADR 0008's order: the latch, the
// cancellation (both Start's), then a halt that a module elsewhere in the
// run decided, or a pending one, which this start applies (pendingHalt).
func (r *run) iterationEnded(ctx context.Context) (conformance.RunStatus, bool) {
	if st, end := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil}).Status(); end {
		return st, true
	}
	if r.halted.Load() || r.pendingApplies() {
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
// for or while loop (CF-1): its input is base, the loop's flow input or, on
// a retry, the input of its last failed child (CF-22 rule 3), plus iter
// (CF-3), which it reads back from storage (JE-44); its first module's
// previous_result is that input (CF-9). A sequential or while iteration
// writes into its parent's results scope and stays in its innermost flow
// scope; a parallel one starts both (CF-19, WA-V1), and has an rng of its
// own. An input jsonb refuses fails the child's push (FX-48, FX-51):
// childFrame then returns nil and the chaining error's text. Reading: the
// iterator's own storage check rules that out, since the loop's input is
// stored too.
func (r *run) childFrame(fr *frame, i, k int, base jsonval.Value, item []byte, parallel bool) (*frame, string) {
	m := fr.modules[i]
	stored, err := iterInput(base, k, item).Stored()
	if err != nil {
		return nil, databaseError(err).Error()
	}
	c := newFrame(bodyOf(m).modules, fr.sites[i].Child(conformance.Iteration(k)))
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
	child := ChildFlow{Site: c.site, ID: c.id, Modules: c.ids(), Virtual: make([]bool, len(c.modules))}
	for i, m := range c.modules {
		child.Virtual[i] = m.Virtual
	}
	r.trace.Children = append(r.trace.Children, child)
}

// collapsedFrame is iteration k of module i of fr, a collapsed for or while
// loop (CF-8): its body step runs as a job of fr, with base plus iter as
// flow_input, prev as previous_result and an empty previous id, and
// registers nothing under its own id. base is the loop's flow input or, on
// a retry, its last failed body job's stored arguments (CF-22 rule 3);
// loopPrev and loopPrevID are the loop's own previous_result and preceding
// id, which its timeout sees (CF-21).
func (r *run) collapsedFrame(fr *frame, i, k int, base jsonval.Value, item []byte, prev, loopPrev jsonval.Value, loopPrevID string) *frame {
	m := fr.modules[i]
	c := newFrame(bodyOf(m).modules, fr.sites[i].Child(conformance.Iteration(k)))
	c.collapsed, c.iter, c.parent = m, k, fr
	c.input, c.prev = iterInput(base, k, item), prev
	c.loopPrev, c.loopPrevID = loopPrev, loopPrevID
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

package engine

import (
	"context"
	"math/rand/v2"
	"strconv"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Branches (milestone N3b): a branchone runs the first branch whose
// predicate holds, or its default (CF-14, JE-31), and a branchall runs every
// branch, sequentially or in parallel (CF-15). A branch that runs is a child
// flow (CF-1), even an empty branchall branch, whose result is then its
// input (CF-15). Its input is its flow's input, unchanged (JE-37), which is
// also its first module's previous_result (CF-9, JE-36), and it writes into
// its flow's results scope (CF-19). A stop in a branchone's branch or in a
// sequential branchall's propagates to the flow the branch is in; a
// parallel branchall ignores it (CF-12, FX-20).

// jsonObjectString is the JSON string "{}", Windmill's fallback for a
// previous result it does not have (OR-25, JE-36 rule 4).
var jsonObjectString = jsonval.FromProducer([]byte(`"{}"`))

// branchOne runs module i of fr, a branchone, from its predicates to its
// completion; o is its outcome so far. The chosen branch's result is the
// module's, and its stop_after_if, evaluated with it, is an ordinary stop of
// the flow (CF-14).
func (r *run) branchOne(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, o outcome) outcome {
	m := fr.modules[i]
	site := fr.sites[i]
	// The predicates are evaluated once, in order, each as Boolean(<expr>)
	// (an empty one never holds), with skip_if's scope; the first that holds
	// is chosen and later ones are not evaluated, and the default runs when
	// none holds (CF-14, CF-21, JE-31).
	seg, body, path := conformance.DefaultBranch(), m.BranchOne.Default, fr.path+"/branchone-0"
	chosen := &conformance.BranchChosen{Type: "default"}
	for j, br := range m.BranchOne.Branches {
		v, err := r.predicate(ctx, fr, br.Expr, site, r.startScope(fr, prev, prevID))
		if err != nil {
			if ctx.Err() != nil {
				o.rec.Status, o.rec.Module, o.end = conformance.StepNotRun, conformance.ModuleNotRun, endInterrupted
				return o
			}
			// A throw stops the scan and fails the module in place, an
			// ordinary step failure wherever the module is, never a chaining
			// error (CF-14, CF-23), with a nil job (OR-11).
			v := engineError("Anyhow", predicateText(err))
			o.rec.Status, o.rec.Module, o.rec.Carried, o.rec.CarriedOrigin = conformance.StepFailure, conformance.ModuleFailure, v, OriginEngine
			o.end, o.carried, o.origin = endFailed, v, OriginEngine
			r.recordNilJob(fr, i)
			return o
		}
		if v {
			n := j
			seg, body, path = conformance.Branch(j), br.Modules, fr.path+"/branchone-"+strconv.Itoa(j+1)
			chosen = &conformance.BranchChosen{Type: "branch", Branch: &n}
			break
		}
	}
	o.rec.BranchChosen = chosen
	if len(body) == 0 {
		// An empty chosen branch runs no child: the module succeeds at once
		// with a nil job and passes the previous_result handed to it
		// through, which at the first module of a flow is the JSON string
		// "{}" (CF-14, OR-25); reading: of any flow, a child flow's
		// included.
		res := prev
		if i == 0 {
			res = jsonObjectString
		}
		o.rec.Status, o.rec.Module, o.rec.Carried, o.carried = conformance.StepSuccess, conformance.ModuleSuccess, res, res
		r.recordNilJob(fr, i)
		return r.stop(ctx, fr, i, site, o)
	}
	if st, ended := r.iterationEnded(ctx); ended {
		return branchInterrupted(o, false, st)
	}
	c, text := r.branchFrame(fr, i, seg, body, path, false)
	if c == nil {
		return r.chaining(fr, i, o, text)
	}
	r.addChild(c)
	e := r.runList(ctx, c)
	// The branch's completion refreshes its flow's ping (JE-2).
	r.ping(fr)
	if ended(e.status) {
		return branchInterrupted(o, true, r.endedStatus(ctx, e.status))
	}
	r.recordJob(fr, i, e.result)
	o.rec.Ran = true
	o.rec.Result, o.rec.Carried, o.rec.ResultOrigin, o.rec.ResultNested, o.rec.CarriedNested = e.result, e.result, e.origin, e.nested, e.nested
	o.carried, o.origin, o.nested = e.result, e.origin, e.nested
	o.rec.Status, o.rec.Module = conformance.StepSuccess, conformance.ModuleSuccess
	switch o.end = branchOneEnd(e); o.end {
	case endFailed, endErrorStop:
		// A failed branch fails the module with the branch's result, and an
		// error stop in it is an error stop of the module, which its flow
		// forwards (CF-12 item 4, FX-20). A branch that ran its copy of the
		// failure module fails it with that module's result, its job
		// successful when it recovered (CF-16, CF-V1, FX-33); a failure
		// such a module handled stays handled through the forwarded stop
		// that a clean stop of a failed container in it raised.
		o.rec.Module, o.handled = conformance.ModuleFailure, e.handled
		if e.status == conformance.RunFailure {
			o.rec.Status = conformance.StepFailure
		}
		return o
	case endStopped, endStoppedSkipped:
		// A clean stop in the branch stops the flow right after the module,
		// with the same skipped flag (CF-12 item 2, FX-20); the module's own
		// stop_after_if is not evaluated.
		if o.end == endStoppedSkipped {
			o.rec.Status, o.rec.Skipped = conformance.StepSkipped, true
		}
		return o
	}
	return r.stop(ctx, fr, i, site, o)
}

// branchOneEnd is how a branchone ends when its branch's flow ended as e,
// before its own stop_after_if: a failure or an error stop of the branch is
// the module's (CF-12 item 4), and so is a clean stop (item 2); a branch
// that ran its copy of the failure module failed, even when it recovered
// (CF-16, CF-V1).
func branchOneEnd(e flowEnd) moduleEnd {
	switch {
	case e.status == conformance.RunFailure && e.errorStop:
		return endErrorStop
	case e.status == conformance.RunFailure, e.handled:
		return endFailed
	case e.stopped && e.status == conformance.RunSkipped:
		return endStoppedSkipped
	case e.stopped:
		return endStopped
	}
	return endSucceeded
}

// branchAll runs module i of fr, a branchall: every branch as a child flow,
// in parallel unless parallel is false (CF-6, CF-15). Its result is the
// array of its branches' results in branch order, and its stop_after_if is
// not evaluated; a branchall with no branches is an empty container, whose
// stop_after_if is (CF-15, CF-29).
func (r *run) branchAll(ctx context.Context, fr *frame, i int, o outcome) outcome {
	b := fr.modules[i].BranchAll
	switch {
	case len(b.Branches) == 0:
		return r.emptyContainer(ctx, fr, i, o)
	case b.Parallel:
		return r.parallelBranches(ctx, fr, i, o)
	}
	return r.sequentialBranches(ctx, fr, i, o)
}

// sequentialBranches runs a sequential branchall's branches in order
// (CF-15): after a failed branch the next starts only when that branch's
// skip_failure is true, otherwise the module fails with the results so far.
// A stop in a branch propagates (sequentialStop).
func (r *run) sequentialBranches(ctx context.Context, fr *frame, i int, o outcome) outcome {
	bs := fr.modules[i].BranchAll.Branches
	var it iterations
	failed, handled := false, false
	for k, br := range bs {
		if st, ended := r.iterationEnded(ctx); ended {
			return r.loopInterrupted(o, &it, k > 0, st)
		}
		c, text := r.branchFrame(fr, i, conformance.Branch(k), br.Modules, fr.path+"/branchall-"+strconv.Itoa(k), false)
		if c == nil {
			// The push of the branch's child flow failed: a chaining error of
			// the module's flow, at its pickup only for the first branch of a
			// module that is its first module.
			return r.chainingAt(fr, k == 0 && i == 0, o, text)
		}
		r.addChild(c)
		e := r.runList(ctx, c)
		// The branch's completion refreshes its flow's ping (JE-2).
		r.ping(fr)
		if ended(e.status) {
			return r.loopInterrupted(o, &it, true, r.endedStatus(ctx, e.status))
		}
		it.add(e)
		if e.stopped || e.errorStop {
			// A forwarded stop of a failure that a failure module handled
			// in the branch keeps it handled (CF-16 item 4).
			o = r.sequentialStop(fr, i, o, &it, sequentialBranchEnd(e, br.SkipFailure))
			o.handled = o.end == endErrorStop && e.handled
			return o
		}
		// A branch that ran its copy of the failure module failed, even when
		// it recovered (CF-16, CF-V1).
		if (e.status == conformance.RunFailure || e.handled) && !br.SkipFailure {
			failed, handled = true, e.handled
			break
		}
	}
	return handledBy(r.loopDone(ctx, fr, i, o, &it, failed, nil), handled)
}

// sequentialBranchEnd is how a sequential branchall ends when a stop ended
// one of its branches, of skip_failure skip, as e: a clean stop is the
// module's, with its skipped flag (CF-12 item 2); a stop of a failed branch,
// with no failure module (N3c), stops the flow with the branch's
// skip_failure deciding the status, success or failure (item 4).
func sequentialBranchEnd(e flowEnd, skip bool) moduleEnd {
	switch {
	case e.stopped && e.status == conformance.RunSkipped:
		return endStoppedSkipped
	case e.stopped, skip:
		return endStopped
	}
	return endErrorStop
}

// sequentialStop completes a sequential branchall that a stop in one of its
// branches stopped (CF-12 items 2 and 4, FX-20): no further branch runs,
// and the flow stops right after the module, whose result is the results
// so far, the stopped branch's included; end says how (sequentialBranchEnd).
// Its stop_after_all_iters_if is not evaluated (CF-13, JE-27).
func (r *run) sequentialStop(fr *frame, i int, o outcome, it *iterations, end moduleEnd) outcome {
	arr := arrayOf(it.values)
	r.recordJob(fr, i, arr)
	o.rec.Ran = true
	o.rec.Result, o.rec.Carried, o.carried = arr, arr, arr
	o.rec.ResultNested, o.rec.CarriedNested, o.nested = it.nested, it.nested, it.nested
	o.rec.Status, o.rec.Module, o.end = conformance.StepSuccess, conformance.ModuleSuccess, end
	switch end {
	case endStoppedSkipped:
		o.rec.Skipped = true
	case endErrorStop:
		o.rec.Status, o.rec.Module = conformance.StepFailure, conformance.ModuleFailure
	}
	return o
}

// parallelBranches runs a parallel branchall's branches at once, each a
// child flow on a goroutine of its own, with no limit, and completes when
// every branch has (CF-15). There is no fail-fast (Windmill's continue-all,
// decisions Q-18): the module fails with the full array when a branch whose
// skip_failure is false failed, and a stop ends only its own branch (CF-12).
func (r *run) parallelBranches(ctx context.Context, fr *frame, i int, o outcome) outcome {
	m := fr.modules[i]
	bs := m.BranchAll.Branches
	n := len(bs)
	// More than one branch in a same_worker flow fails the module's start
	// (CF-7); the flag is the root's, which child flows copy (CF-1).
	if fr.def.SameWorker && m.Sleep == nil && n > 1 {
		return r.chaining(fr, i, o, "Bad request: Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker")
	}
	// Every branch is created at once, each with an rng of its own drawn in
	// branch order, so a seed still fixes every order.
	children := make([]*frame, n)
	for k, br := range bs {
		c, text := r.branchFrame(fr, i, conformance.Branch(k), br.Modules, fr.path+"/branchall-"+strconv.Itoa(k), true)
		if c == nil {
			return r.chaining(fr, i, o, text)
		}
		children[k] = c
	}
	for _, c := range children {
		r.addChild(c)
	}
	ends := make([]flowEnd, n)
	started := 0
	stopped := conformance.RunStatus("")
	done := make(chan struct{}, n)
	// The branches start together, after one check of the host signals
	// (CF-15), so none waits on another's outcome to start.
	if st, ended := r.iterationEnded(ctx); ended {
		stopped = st
	} else {
		for k, c := range children {
			started++
			go func() {
				ends[k] = r.runList(ctx, c)
				done <- struct{}{}
			}()
		}
	}
	// This goroutine refreshes the flow's ping as each branch completes
	// (JE-2).
	for range started {
		<-done
		r.ping(fr)
	}
	var it iterations
	failed, handled := false, false
	for k, e := range ends[:started] {
		if ended(e.status) && stopped == "" {
			stopped = e.status
		}
		it.add(e)
		if e.status == conformance.RunFailure && !bs[k].SkipFailure {
			failed, handled = true, handled || e.handled
		}
	}
	if stopped != "" {
		return r.loopInterrupted(o, &it, started > 0, r.endedStatus(ctx, stopped))
	}
	return handledBy(r.loopDone(ctx, fr, i, o, &it, failed, nil), handled)
}

// branchFrame creates the child flow of a branch of module i of fr (CF-1):
// ms are its modules, seg its site's segment (b:<n> or b:default) and path
// its runnable path (CF-24). Its input is its flow's input, unchanged
// (JE-37), which it reads back from storage (JE-44), and its first module's
// previous_result (CF-9). It writes into its flow's results scope and stays
// in its innermost flow scope (CF-19, WA-V1); a parallel branch has an rng
// of its own. An input jsonb refuses fails the child's push, as childFrame
// says; reading: its flow's input was stored before, so none does.
func (r *run) branchFrame(fr *frame, i int, seg conformance.Segment, ms []*ir.Module, path string, parallel bool) (*frame, string) {
	stored, err := fr.input.Stored()
	if err != nil {
		return nil, databaseError(err).Error()
	}
	site := fr.sites[i].Child(seg)
	c := newFrame(ms, site)
	now := r.clock.Now()
	c.site, c.child, c.id, c.pinged, c.started = site, true, newUUID(), now, now
	c.def, c.failure, c.envSrc = fr.def, fr.failure, fr.envSrc
	c.input = jsonval.FromProducer(flowInputText(stored, fr.rng))
	c.path, c.scope, c.nodes, c.rng = path, fr.scope, fr.nodes, fr.rng
	if parallel {
		c.rng = rand.New(rand.NewPCG(fr.rng.Uint64(), fr.rng.Uint64()))
	}
	return c, ""
}

// branchInterrupted is a branchone that the latch, the cancellation or a
// halt stopped before its branch completed (status says which); ran says
// that the branch had started.
func branchInterrupted(o outcome, ran bool, status conformance.RunStatus) outcome {
	o.rec.Ran = ran
	o.rec.Status, o.rec.Module, o.end = conformance.StepCanceled, conformance.ModuleFailure, endInterrupted
	if status == conformance.RunHalted {
		o.end = endHalted
	}
	return o
}

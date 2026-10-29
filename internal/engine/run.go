package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// run is one run of a flow: its state and its trace. One goroutine drives
// it, and one more each running iteration of a parallel loop and each
// running branch of a parallel branchall; the Reporters of its
// invocations write late output into its trace from others, under mu.
type run struct {
	e     *Engine
	f     *ir.Flow
	id    string
	hooks Hooks
	exec  StepExecutor

	// rootID is the effective root of the run's jobs: the run itself,
	// unless the links of a dispatch tracked in its parent place it in
	// another run's tree (RunRequest.RootJob).
	rootID string
	// root is the frame of the flow's own modules, its failure module in
	// the slot after them; its input is the flow input, stored, with its
	// top-level keys shuffled (FX-42 item 1), and its envSrc the flow's
	// flow_env as stored, or zero (JE-38), which its child flows inherit
	// (CF-1). Each transition resolves a frame's map into it (resolveEnv)
	// with the flow job's reserved variables, flowJobEnv.
	root       *frame
	flowJobEnv map[string]string
	latch      Latch
	settle     *RunSettlement
	// halted is set once a module ended the run halted (H8), so that the
	// iterations of a parallel loop start nothing more; wake is closed, once,
	// when the run is latched or halted, which wakes a parallel loop whose
	// iterations are all held.
	halted   atomic.Bool
	wake     chan struct{}
	wakeOnce sync.Once
	// pending is a halt-after decided where a child flow ended, which
	// applies at the run's next start (pendingHalt); haltMu guards it, and
	// the first one decided is kept.
	haltMu  sync.Mutex
	pending *pendingHalt
	// wrote is set once a write-capable module has launched (H3); writeMu
	// holds the iterations of a parallel loop back while H3 decides.
	writeMu sync.Mutex
	wrote   bool
	// clock is the run's clock and jitter its retry-jitter source (H10);
	// cancel cancels the run's context with a cause (JE-2's monitor, the
	// pre-write deadline of H4).
	clock  Clock
	jitter Jitter
	cancel context.CancelCauseFunc
	// preWriteAt is the run's pre-write deadline (H4), zero for none, and
	// preWrite its state: armed until the first write-capable launch
	// disarms it or the deadline expires it, whichever comes first, the
	// actual time deciding and never the delivery of the deadline's timer.
	preWriteAt time.Time
	preWrite   atomic.Int32

	mu    sync.Mutex // guards trace and the frames' job results
	trace Trace
}

func newRun(e *Engine, f *ir.Flow, id string, hooks Hooks, exec StepExecutor, rng *rand.Rand, clock Clock, jitter Jitter, cancel context.CancelCauseFunc) *run {
	now := clock.Now()
	r := &run{e: e, f: f, id: id, rootID: id, hooks: hooks, exec: exec, wake: make(chan struct{}), clock: clock, jitter: jitter, cancel: cancel}
	r.root = newFrame(f.Modules, conformance.SitePath{})
	r.root.failure, r.root.def, r.root.started = f.FailureModule, f, now
	r.root.path, r.root.id, r.root.scope, r.root.nodes, r.root.rng, r.root.pinged = f.Path, id, id, newNodeScope(nil), rng, now
	r.trace = Trace{RunID: id, Bundle: e.bundle, ModuleState: e.state}
	r.settle = NewRunSettlement(func() { hooks.OnRunSettled(r.snapshot()) })
	return r
}

func (r *run) ID() string { return r.id }

// Latch latches err on the run (H5); a nil err latches nothing.
func (r *run) Latch(err error) {
	if r.latch.Set(err) {
		r.signal()
	}
}

// halt ends the run halted (H8), wherever its iterations are.
func (r *run) halt() {
	r.halted.Store(true)
	r.signal()
}

func (r *run) signal() { r.wakeOnce.Do(func() { close(r.wake) }) }

// site is the module at cursor i of fr as hooks see it. Container bodies
// belong to the enclosing flow, so Flow is the flow whose module lists
// hold the module (the run's, or a subflow's by path, ADR 0008), and
// Script is the leaf's static path, <flow path>/<module id> for an inline
// script.
func (r *run) site(fr *frame, i int) Site {
	m := fr.moduleAt(i)
	s := Site{Path: fr.sites[i], Flow: fr.def.Path, Module: m.ID}
	if m.Leaf != nil {
		if sc := r.e.prog.Scripts[m.Leaf.Script]; sc != nil {
			s.Script = sc.Path
		} else {
			s.Script = m.Leaf.Script
		}
	}
	return s
}

// snapshot copies the trace, so that what the run returns never changes
// while abandoned invocations report late output.
func (r *run) snapshot() *Trace {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.trace
	t.Steps = make([]StepRecord, len(r.trace.Steps))
	for i, st := range r.trace.Steps {
		st.Late = append([]Late(nil), st.Late...)
		st.Hooks = append([]string(nil), st.Hooks...)
		st.ResultNested = append([]string(nil), st.ResultNested...)
		st.CarriedNested = append([]string(nil), st.CarriedNested...)
		st.FailedRetries = slices.Clone(st.FailedRetries)
		t.Steps[i] = st
	}
	t.ResultNested = append([]string(nil), r.trace.ResultNested...)
	t.Children = make([]ChildFlow, len(r.trace.Children))
	for i, c := range r.trace.Children {
		c.Modules = append([]string(nil), c.Modules...)
		c.Virtual = append([]bool(nil), c.Virtual...)
		t.Children[i] = c
	}
	t.Warnings = append([]Warning(nil), r.trace.Warnings...)
	t.Deviations = append([]string(nil), r.trace.Deviations...)
	t.Spans = append([]Span(nil), r.trace.Spans...)
	t.PlatformCalls = append([]PlatformCall(nil), r.trace.PlatformCalls...)
	return &t
}

// end records the run's outcome. A latched run records its latched error
// and no result, and a halted run no result either. The final result is
// stored as jsonb (FX-36, FX-42 item 5); nested points to the generated
// errors inside it (Trace.ResultNested).
func (r *run) end(status conformance.RunStatus, result jsonval.Value, origin Origin, nested []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Status = status
	switch status {
	case conformance.RunLatched:
		r.trace.Latched = r.latch.Err()
		return
	case conformance.RunHalted:
		return
	}
	if result.IsZero() {
		return
	}
	r.trace.ResultOrigin, r.trace.ResultNested = origin, nested
	if stored, err := result.Stored(); err == nil {
		r.trace.Result = jsonval.FromProducer(stored)
	} else {
		r.trace.Result = result
	}
}

// endBy ends the run with the status of a host verdict: latched, halted, or
// canceled, whose result is the Canceled error of FX-38 whatever the flow
// was doing.
func (r *run) endBy(ctx context.Context, status conformance.RunStatus) {
	if status == conformance.RunCanceled {
		r.end(status, canceledError(cancellationOf(ctx)), OriginEngine, nil)
		return
	}
	r.end(status, jsonval.Value{}, OriginNone, nil)
}

func (r *run) span(site conformance.SitePath, kind SpanKind, start time.Time) {
	r.addSpan(Span{Site: site, Kind: kind, Start: start, Duration: r.since(start)})
}

func (r *run) addSpan(s Span) {
	r.mu.Lock()
	r.trace.Spans = append(r.trace.Spans, s)
	r.mu.Unlock()
}

// since is the time elapsed since start on the run's clock.
func (r *run) since(start time.Time) time.Duration { return r.clock.Now().Sub(start) }

// moduleEnd is what a module's start and completion leave the flow to do,
// before the host checks and Windmill's continue decision (FX-2
// completion step 7).
type moduleEnd uint8

const (
	// endSucceeded: the module is Success; the flow goes on unless it was
	// the last module.
	endSucceeded moduleEnd = iota
	// endFailed: the module is Failure (a failed job, an input-transform
	// error, a chaining error at the root's first pickup, a failed loop),
	// carrying its failure value (FX-36).
	endFailed
	// endErrorStop: an error stop (FX-19): the module is Failure, carrying
	// an EarlyStopError, while its job succeeded (FX-V2); a sequential
	// loop takes it as a break (CF-12).
	endErrorStop
	// endFailedStop: a clean stop of a module that is Failure: a failed
	// loop or branchall whose stop_after_all_iters_if stops cleanly
	// (CF-13). It carries the module's failure value, and a stop never
	// continues the flow (FX-2 completion rule 7.1).
	endFailedStop
	// endUnrecoverable: a throwing stop_after_if (FX-21): the module is
	// Failure, carrying the string error, and neither a retry nor
	// continue_on_error applies (FX-44).
	endUnrecoverable
	// endStopped and endStoppedSkipped: a clean stop (FX-19); the flow
	// ends with the module's result, as success or as skipped.
	endStopped
	endStoppedSkipped
	// endChained: a chaining error at a later transition (FX-15 context
	// 1), or at a child flow's pickup (context 3): the flow fails at once
	// with the error, carried.
	endChained
	// endInterrupted: the module did not complete because the run was
	// latched or canceled on its way; the completion check ends the run.
	endInterrupted
	// endHalted: a module in the module's container body ended the run
	// halted (H8); the completion check still ranks a latch or the
	// cancellation first.
	endHalted
)

// failed reports whether the module is Failure: what H7 and Windmill's
// continue decision treat as a failure.
func (e moduleEnd) failed() bool {
	return e == endFailed || e == endErrorStop || e == endFailedStop || e == endUnrecoverable
}

// outcome is how one module went.
type outcome struct {
	rec StepRecord
	end moduleEnd
	// carried is what the module carries on (FX-V2): the next module's
	// previous_result, or the flow's result; origin is who produced it
	// when it is an error.
	carried jsonval.Value
	origin  Origin
	// nested are the pointers, relative to carried, of the errors
	// windmill-lite generated inside it when it aggregates others: a loop's
	// array (StepRecord.CarriedNested).
	nested []string
	// chain is a chaining error's text, which a collapsed iteration hands
	// to its loop to route (FX-15).
	chain string
	// handled: the module, a container, failed because a child flow's
	// failure ended in a failure module (flowEnd.handled), which outer
	// levels do not run again for it (CF-16 item 4, FX-34): as a failure,
	// under a clean stop that keeps it (endFailedStop), or as the error
	// stop a branch's flow forwards for it.
	handled bool
	// failedChild and lastChild are, for a container, its last child job
	// that failed and its last child job, which a retry of the container
	// starts from and records as the failed attempt (CF-22).
	failedChild, lastChild *childJob
}

// pendingHalt is a halt-after (H8) decided at the module whose trace
// record is idx, when the module's flow then ended: whether the run starts
// something after that is for its containers' completions to say, their
// own stops included. It applies at the run's next start anywhere in the
// run: an iteration or a branch, or a module that is not the first of its
// flow, since a flow that has started (its iteration's or branch's start)
// runs its first module, as a parallel container's other children already
// started do. A run that ends with no such start, or that starts a failure
// module, keeps the status Windmill gives it, so a halt never hides how
// the run would end (ADR 0008).
type pendingHalt struct{ idx int }

// deferHalt records the halt-after decided at the module whose record is
// idx as pending, unless one already is.
func (r *run) deferHalt(idx int) {
	r.haltMu.Lock()
	defer r.haltMu.Unlock()
	if r.pending == nil {
		r.pending = &pendingHalt{idx: idx}
	}
}

// pendingApplies is the check of a start against a pending halt-after: when
// there is one, the run ends halted, with H8 recorded at the module that
// decided it unless another halt ended the run first, and it reports true.
func (r *run) pendingApplies() bool {
	r.haltMu.Lock()
	defer r.haltMu.Unlock()
	if r.pending == nil {
		return false
	}
	if !r.halted.Load() {
		r.mark(r.pending.idx, "H8")
		r.halt()
	}
	return true
}

// next is what Windmill's continue decision asks for after a module's
// completion (FX-2 completion steps 3, 7 and 8).
type next uint8

const (
	// nextModule: the cursor advances to the next module.
	nextModule next = iota
	// nextFailure: the failure module starts (FX-31).
	nextFailure
	// nextEnd: the flow completes.
	nextEnd
	// nextRetry: the module is re-entered (FX-2 start step 6), where its
	// retry is evaluated again: a retry due, or an error stop that a
	// failure module lets re-enter (FX-19).
	nextRetry
)

// decision is a continue decision: what starts next, or how the flow ends.
type decision struct {
	next next
	// direct is set when the failure module is entered at cursor len after
	// a tolerated error stop at the final module (FX-V4), where its
	// preceding id is empty; otherwise its preceding id is the failed
	// module's (FX-32).
	direct bool
	// status, result, origin and nested are the flow's ending, for nextEnd.
	status conformance.RunStatus
	result jsonval.Value
	origin Origin
	nested []string
}

// decide is Windmill's continue decision for module m of fr (FX-2
// completion steps 3, 7 and 8) when no retry is due (decideRetry decides
// that):
// continue_on_error (FX-30), the error stops (FX-19, FX-V4), the
// unrecoverable failure of a throwing stop_after_if (FX-21, FX-44) and the
// failure module (FX-31). last says that m is the flow's last module. A
// collapsed iteration's body takes its loop's continue_on_error, not its
// own: its failure fails the iteration (CF-8 item 5).
func (r *run) decide(fr *frame, m *ir.Module, o outcome, last bool) decision {
	// A container's failure that a child's copy of the failure module
	// handled does not run it again: the flow decides as if it had none
	// (CF-16 item 4, FX-34).
	hasFailure := fr.failure != nil && !o.handled
	ends := func(status conformance.RunStatus) decision {
		return decision{next: nextEnd, status: status, result: o.carried, origin: o.origin, nested: o.nested}
	}
	// A failure is tolerated by continue_on_error unless it is
	// unrecoverable (FX-30, FX-44); the cursor then advances.
	tolerated := m.ContinueOnError && o.end != endUnrecoverable && fr.collapsed == nil
	switch o.end {
	case endSucceeded:
		// Rule 3: continue unless this was the last module.
		if last {
			return ends(conformance.RunSuccess)
		}
		return decision{next: nextModule}
	case endStopped:
		return ends(conformance.RunSuccess)
	case endStoppedSkipped:
		return ends(conformance.RunSkipped)
	case endChained:
		return ends(conformance.RunFailure)
	case endErrorStop:
		// Rule 1: a stop continues only when it is an error stop and the
		// flow has a failure module. A tolerated one advances the cursor:
		// to the next module, or past the last to the failure module
		// (FX-V4); an untolerated one re-enters the module, which with no
		// retry due switches to the failure module (start step 6).
		switch {
		case hasFailure && tolerated && !last:
			return decision{next: nextModule}
		case hasFailure:
			return decision{next: nextFailure, direct: tolerated}
		case tolerated && last:
			// Step 8: a tolerated failure at the last module turns the
			// flow successful (FX-19/f).
			return ends(conformance.RunSuccess)
		}
		return ends(conformance.RunFailure)
	case endFailedStop:
		// Rule 1: a clean stop never continues, not even into the failure
		// module; step 8 turns a tolerated failure at the last module
		// successful.
		if tolerated && last {
			return ends(conformance.RunSuccess)
		}
		return ends(conformance.RunFailure)
	case endUnrecoverable:
		// Rule 4: the failure module, when there is one (FX-21).
		if hasFailure {
			return decision{next: nextFailure}
		}
		return ends(conformance.RunFailure)
	case endFailed:
		// Rule 5, then rules 7 and 8. A tolerated failure at the last
		// module ends the flow successfully with its failure value, and
		// the failure module does not run (FX-30).
		switch {
		case tolerated && last:
			return ends(conformance.RunSuccess)
		case tolerated:
			return decision{next: nextModule}
		case hasFailure:
			return decision{next: nextFailure}
		}
		return ends(conformance.RunFailure)
	}
	// Only a latch or the cancellation interrupts a module, and the
	// completion check ends the run for both before the decision.
	return decision{next: nextEnd, status: conformance.RunFailure, origin: OriginEngine,
		result: engineError("InternalErr", "windmill-lite: a module was interrupted with no latch or cancellation")}
}

// flowEnd is how a frame's flow ended: its status and result (a host
// verdict's status has none), whether a stop ended it (CF-12), errorStop
// for a failure that a stop ended, handled when its failure module ended
// it, and the generated errors inside the result.
type flowEnd struct {
	status    conformance.RunStatus
	result    jsonval.Value
	origin    Origin
	stopped   bool
	errorStop bool
	// handled: the flow ran its failure module, which decided its status:
	// success when it recovered (FX-33), or it failed with a container
	// whose failure such a module handled below it. The flow then reports
	// a failure to what contains it, a recovered one included, and does not
	// forward a stop; a parallel container judges it by its status instead
	// (CF-12 item 3, CF-16, CF-V1). Its containers' flows do not run their
	// failure modules again for it; a subflow step resets that (CF-17).
	handled bool
	nested  []string
	// chain is set when a collapsed iteration's push failed (outcome.chain).
	chain string
	// args are a collapsed iteration's body job's stored arguments, the
	// input of its job (CF-22 rule 3).
	args jsonval.Value
}

// run runs the flow and records its outcome.
func (r *run) run(ctx context.Context) {
	e := r.runList(ctx, r.root)
	if ended(e.status) {
		r.endBy(ctx, e.status)
		return
	}
	r.end(e.status, e.result, e.origin, e.nested)
}

// runList runs a frame's modules in order (FX-2): the start checks of the
// host (Start), the module's own start and completion, then the host's
// completion checks (Completion) and Windmill's continue decision, then
// haltAfter (AfterContinue). A failure the decision does not tolerate
// enters the failure module (failure).
func (r *run) runList(ctx context.Context, fr *frame) flowEnd {
	// FX-6, JE-36, CF-9: the first module's previous_result is the flow's
	// input; FX-3: a flow with no modules returns it.
	prev, prevID := fr.prev, ""
	if prev.IsZero() {
		prev = fr.input
	}
	// A child flow scheduled for later, by the sleep before its container
	// or a retry's delay (CF-21, FX-23, FX-26), is picked up then: none of
	// its modules starts before. A cancellation while it is queued fails
	// it without running its body (CF-28 rule 2). A collapsed iteration's
	// body job waits as any job does (begin.due).
	if !fr.due.IsZero() && fr.collapsed == nil {
		if err := r.wait(ctx, fr.site, fr.due); err != nil {
			return flowEnd{status: r.endedStatus(ctx, conformance.RunCanceled)}
		}
	}
	// continues is set when the start that follows is what the continue
	// decision asked for after an error stop that coincided with the
	// cancellation, which Completion let through (FX-38).
	continues := false
	for i, m := range fr.modules {
		site := r.site(fr, i)
		v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, ContinuesErrorStop: continues, HaltBefore: r.hooks.HaltBefore(site)})
		if st, end := v.Status(); end {
			if st == conformance.RunHalted {
				r.halt()
			}
			return flowEnd{status: st}
		}
		// A halt decided in another iteration of a parallel loop, or a
		// pending one, comes after the latch and the cancellation (ADR
		// 0008); a flow that has started runs its first module.
		if r.halted.Load() || i > 0 && r.pendingApplies() {
			return flowEnd{status: conformance.RunHalted}
		}
		// The root's first pickup is its first module's first start
		// (FX-15 context 2); the previous module's sleep applies to a
		// module's first start (FX-23).
		b := begin{first: i == 0}
		if i == 0 && fr.collapsed != nil {
			// The sleep before a collapsed loop, or its retry's delay,
			// schedules its first body job (CF-21).
			b.due = fr.due
		}
		if i > 0 && fr.modules[i-1].Sleep != nil {
			b.sleep, b.sleepSite = fr.modules[i-1].Sleep, fr.sites[i-1]
		}
		o, idx, st, ok := r.complete(ctx, fr, i, prev, prevID, b)
		if !ok {
			// A collapsed iteration's job keeps its result, the killed
			// step's when a cancellation ended it (CF-28).
			return flowEnd{status: st, result: o.carried, origin: o.origin}
		}
		if o.end == endHalted {
			return flowEnd{status: conformance.RunHalted}
		}
		last := i == len(fr.modules)-1
		var d decision
		for attempt := 0; ; {
			var end *flowEnd
			if d, end = r.decideRetry(ctx, fr, i, &o, idx, last); end != nil {
				return *end
			}
			if d.next != nextRetry {
				break
			}
			// A retry is no final outcome, so haltAfter waits
			// (AfterContinue); re-entry starts with the host's start
			// checks, where haltBefore applies to a first attempt only.
			continues = ctx.Err() != nil
			v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, ContinuesErrorStop: continues})
			if st, end := v.Status(); end {
				return flowEnd{status: st}
			}
			nb, p, due, end := r.reenter(ctx, fr, i, o, idx, attempt)
			if end != nil {
				return *end
			}
			if !due {
				d = r.decide(fr, m, o, last)
				break
			}
			attempt++
			if o, idx, st, ok = r.complete(ctx, fr, i, p, prevID, nb); !ok {
				return flowEnd{status: st}
			}
		}
		canceled := ctx.Err() != nil
		stop := o.end == endStopped || o.end == endStoppedSkipped || o.end == endErrorStop || o.end == endFailedStop
		end := flowEnd{status: d.status, result: d.result, origin: d.origin, nested: d.nested, chain: o.chain, args: o.rec.RecordedArgs,
			stopped:   stop && d.status != conformance.RunFailure && o.end != endErrorStop,
			errorStop: stop && d.status == conformance.RunFailure,
			handled:   o.handled && d.status == conformance.RunFailure}
		// haltAfter (H8): when the flow starts its next module, the run halts
		// now; when a child flow ends, the halt is pending until the run's
		// next start, after its containers' completions (pendingHalt); the
		// root's flow ends with nothing more to start.
		halt := r.hooks.HaltAfter(site)
		if AfterContinue(ContinueSignals{RunContinues: d.next != nextEnd, Final: true, ToFailureModule: d.next == nextFailure, Canceled: canceled, HaltAfter: halt}) != Proceed {
			if canceled {
				return flowEnd{status: conformance.RunCanceled}
			}
			r.mark(idx, "H8")
			r.halt()
			return flowEnd{status: conformance.RunHalted}
		}
		if halt && d.next == nextEnd && fr != r.root && !canceled {
			r.deferHalt(idx)
		}
		continues = canceled
		switch d.next {
		case nextEnd:
			return end
		case nextFailure:
			// Start step 6: previous_result stays the failing value; the
			// preceding id is the failed module's, or empty when the
			// cursor reached len (FX-V4).
			id := m.ID
			if d.direct {
				id = ""
			}
			return r.failure(ctx, fr, o.carried, id, continues)
		}
		prev, prevID = o.carried, m.ID
	}
	// Only a flow with no modules gets here: it completes with its input
	// (FX-3), through the completion's host checks, the latch and then the
	// cancel flag (FX-2 completion step 8, FX-38), as every completion does.
	if st, end := Completion(CompletionSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil}).Status(); end {
		return flowEnd{status: st}
	}
	return flowEnd{status: conformance.RunSuccess, result: prev}
}

// failure runs fr's failure module (FX-31) with the failing value as its
// previous_result (FX-32), and ends the flow with its result (FX-33): the
// flow succeeds only when the failure module's job succeeded and its result
// recovers; otherwise it fails with that result, or with the failure
// module's own failure value. The failure module has no stop and ignores
// its own continue_on_error (FX-19, FX-30), and a chaining error while it
// starts is a later transition's (FX-15 context 1). Its own retry applies
// to its failures (FX-32), and starting it resets the retry counter when
// it has one (FX-27).
func (r *run) failure(ctx context.Context, fr *frame, prev jsonval.Value, prevID string, continues bool) flowEnd {
	i := len(fr.modules)
	m := fr.failure
	v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, ContinuesErrorStop: continues, FailureModule: true, HaltBefore: r.hooks.HaltBefore(r.site(fr, i))})
	if st, end := v.Status(); end {
		return flowEnd{status: st}
	}
	if m.Retry != nil {
		fr.failCount = 0
	}
	o, idx, st, ok := r.complete(ctx, fr, i, prev, prevID, begin{})
	if !ok {
		return flowEnd{status: st}
	}
	for attempt := 0; o.end == endFailed && m.Retry != nil; {
		// FX-2 completion rule 7.6: a retry due re-enters the failure
		// module; a throwing retry_if makes the failure unrecoverable, and
		// the failure module, already running, does not start again.
		allowed, err := r.retryAllowed(ctx, fr, i, o.carried)
		if err != nil {
			if ctx.Err() != nil {
				return flowEnd{status: conformance.RunCanceled}
			}
			r.unrecoverable(&o, idx, retryIfText(err))
			break
		}
		if !allowed {
			break
		}
		v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, ContinuesErrorStop: continues, FailureModule: true})
		if st, end := v.Status(); end {
			return flowEnd{status: st}
		}
		nb, p, due, end := r.reenter(ctx, fr, i, o, idx, attempt)
		if end != nil {
			return *end
		}
		if !due {
			break
		}
		attempt++
		if o, idx, st, ok = r.complete(ctx, fr, i, p, prevID, nb); !ok {
			return flowEnd{status: st}
		}
	}
	switch {
	case o.end == endSucceeded && recovers(o.carried):
		return flowEnd{status: conformance.RunSuccess, result: o.carried, origin: o.origin, handled: true}
	case o.end == endSucceeded, o.end == endFailed, o.end == endChained, o.end == endUnrecoverable:
		return flowEnd{status: conformance.RunFailure, result: o.carried, origin: o.origin, handled: true}
	}
	return flowEnd{status: conformance.RunFailure, result: engineError("InternalErr", "windmill-lite: the failure module was interrupted with no latch or cancellation"), origin: OriginEngine}
}

// decideRetry is the continue decision with retries for the module at
// cursor i of fr, whose attempt o completed with record idx (FX-2
// completion steps 3 and 7): a failure, or an error stop under
// continue_on_error, evaluates the module's retry first, retry_if and then
// the attempts left (FX-26 to FX-28). A retry due re-enters the module,
// except after an error stop with no failure module, where the continue
// decision stops the flow first (FX-19); a throwing retry_if re-processes
// the completion as an unrecoverable failure (FX-28). An error stop that
// continue_on_error does not tolerate re-enters a module with a retry when
// the flow has a failure module, and re-entry evaluates the retry (FX-19).
// The flow's ending is returned when the run was canceled while the retry
// was evaluated.
func (r *run) decideRetry(ctx context.Context, fr *frame, i int, o *outcome, idx int, last bool) (decision, *flowEnd) {
	m := fr.moduleAt(i)
	hasFailure := fr.failure != nil
	if m.Retry != nil && (o.end == endFailed || o.end == endErrorStop && m.ContinueOnError) {
		allowed, err := r.retryAllowed(ctx, fr, i, o.carried)
		switch {
		case err != nil && ctx.Err() != nil:
			return decision{}, &flowEnd{status: conformance.RunCanceled}
		case err != nil:
			r.unrecoverable(o, idx, retryIfText(err))
		case allowed && o.end == endErrorStop && !hasFailure:
			// The cursor does not advance, and with no failure module the
			// stop ends the flow, failed (FX-19).
			return decision{next: nextEnd, status: conformance.RunFailure, result: o.carried, origin: o.origin, nested: o.nested}, nil
		case allowed:
			return decision{next: nextRetry}, nil
		}
	}
	if m.Retry != nil && o.end == endErrorStop && !m.ContinueOnError && hasFailure {
		return decision{next: nextRetry}, nil
	}
	return r.decide(fr, m, *o, last), nil
}

// retryIfText is the error of a retry_if that throws while the completion
// is decided (FX-28): the predicate's text converted once more into the
// generic error, so two "Error: " prefixes, where Windmill also writes two
// location suffixes (FX-50, recorded by fx-28/throw_no_handler) and
// windmill-lite none (D14).
func retryIfText(err error) string { return "Error: " + predicateText(err) }

// unrecoverable re-processes the completion of o, whose record is idx, as
// an unrecoverable failure whose error is text, a string (FX-28, as
// FX-21): no retry, no continue_on_error, and the failure module when
// there is one. The job's stored result stays as it was.
func (r *run) unrecoverable(o *outcome, idx int, text string) {
	o.end, o.origin, o.nested = endUnrecoverable, OriginEngine, nil
	o.carried = objectOf(member{"error", jsonString(text)})
	o.rec.Module, o.rec.Carried, o.rec.CarriedOrigin, o.rec.CarriedNested = conformance.ModuleFailure, o.carried, OriginEngine, nil
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := &r.trace.Steps[idx]
	rec.Module, rec.Carried, rec.CarriedOrigin, rec.CarriedNested = o.rec.Module, o.rec.Carried, o.rec.CarriedOrigin, nil
}

// retryAllowed reports whether module i of fr, whose attempt failed with
// the value failed, may be retried (FX-26 to FX-28): its retry_if,
// evaluated first, holds or is absent, and attempts are left by the
// flow's counter. An error is retry_if's failure.
func (r *run) retryAllowed(ctx context.Context, fr *frame, i int, failed jsonval.Value) (bool, error) {
	m := fr.moduleAt(i)
	if x := m.Retry.RetryIf; x != nil {
		ok, err := r.predicate(ctx, fr, x, fr.sites[i], r.retryScope(fr, x, failed))
		if err != nil || !ok {
			return false, err
		}
	}
	return attemptsLeft(m.Retry, fr.failCount), nil
}

// reenter is start step 6 for the module at cursor i of fr, re-entered
// after its attempt o (record idx, its attempt'th retry) failed (FX-2): its
// retry_if again, then the attempts left (FX-26 to FX-28). When a retry is
// due, the failed attempt's record moves to <site>/r:<k>, k its place in
// the flow's list of failed attempts (FX-27), the counter goes up, and it
// returns the next attempt's start, which reuses the failed attempt's
// recorded arguments (JE-V2) once the retry's delay has passed, and its
// previous_result (restored). due is false when no retry is due; the
// flow's ending is returned when re-entry ended it: a throwing retry_if is
// a chaining error of a later transition (FX-15 context 1, FX-28).
func (r *run) reenter(ctx context.Context, fr *frame, i int, o outcome, idx, attempt int) (begin, jsonval.Value, bool, *flowEnd) {
	m := fr.moduleAt(i)
	allowed, err := r.retryAllowed(ctx, fr, i, o.carried)
	switch {
	case err != nil && ctx.Err() != nil:
		return begin{}, jsonval.Value{}, false, &flowEnd{status: conformance.RunCanceled}
	case err != nil:
		return begin{}, jsonval.Value{}, false, &flowEnd{status: conformance.RunFailure, result: engineError("InternalError", predicateText(err)), origin: OriginEngine}
	case !allowed:
		return begin{}, jsonval.Value{}, false, nil
	}
	delay, _ := retryDelay(m.Retry, fr.failCount, r.jitter)
	site := fr.sites[i].Child(conformance.Attempt(len(fr.failed)))
	if r.attempt(fr, i, o, idx, site) {
		fr.failed = append(fr.failed, site)
	}
	fr.failCount++
	args := o.rec.RecordedArgs
	if args.IsZero() {
		// A failure with no job, a chaining error at the root's first
		// pickup (FX-15 context 2), recorded no arguments: the retry runs
		// with an empty map, as after a failed transform (JE-V2).
		args = jsonObj
	}
	b := begin{attempt: attempt + 1, reuse: &args, due: r.clock.Now().Add(delay)}
	if o.failedChild != nil && isLoop(m.Kind) {
		b.base = o.failedChild.input
	}
	return b, r.restored(fr, i, o.carried), true, nil
}

// attempt records the failed attempt o of the module at cursor i of fr,
// whose record is idx, at site, as Windmill's job tree keeps it for the
// walker (FX-27, OR-11), and reports whether there is such a site: the
// module's job, whose status lists it. An attempt's site is not a module:
// it has no module status and carries nothing on (StepRecord). A
// container's job is its last child job that failed, else its last child
// job (CF-22, recorded by the cf-22 fixture): a child flow's record, with
// its modules below the attempt's site, or a collapsed iteration's body
// job's; what else the attempt ran is superseded. A branchall has no job
// of its own, so its attempt has no site; a container that failed before
// any child, and a leaf, keep their own record there.
func (r *run) attempt(fr *frame, i int, o outcome, idx int, site conformance.SitePath) bool {
	m := fr.moduleAt(i)
	j := o.failedChild
	if j == nil {
		j = o.lastChild
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := &r.trace.Steps[idx]
	switch {
	case m.Kind == ir.KindBranchAll:
		rec.Superseded = true
		r.supersedeLocked(fr.sites[i], conformance.SitePath{}, conformance.SitePath{})
		return false
	case isContainer(m.Kind) && j != nil && !j.flow:
		rec.Superseded = true
		r.supersedeLocked(fr.sites[i], j.site, site)
		for k := range r.trace.Steps {
			if a := &r.trace.Steps[k]; a.Site == site && !a.Superseded {
				a.FailedRetries, a.Module, a.Carried, a.CarriedOrigin, a.CarriedNested = nil, "", jsonval.Value{}, OriginNone, nil
			}
		}
		return true
	case isContainer(m.Kind) && j != nil:
		r.supersedeLocked(fr.sites[i], j.site, site)
		*rec = StepRecord{Site: site, Attempt: rec.Attempt, Ran: true, Status: flowStepStatus(j.end.status), Start: rec.Start, Duration: rec.Duration,
			RecordedArgs: j.input, ResolvedArgs: j.input, Result: j.end.result, ResultOrigin: j.end.origin, ResultNested: j.end.nested, Late: rec.Late}
		return true
	}
	rec.Site, rec.FailedRetries, rec.Module, rec.Carried, rec.CarriedOrigin, rec.CarriedNested = site, nil, "", jsonval.Value{}, OriginNone, nil
	return true
}

// flowStepStatus is the status of a child flow's job that ended as st.
func flowStepStatus(st conformance.RunStatus) conformance.StepStatus {
	switch st {
	case conformance.RunSuccess:
		return conformance.StepSuccess
	case conformance.RunSkipped:
		return conformance.StepSkipped
	case conformance.RunFailure:
		return conformance.StepFailure
	}
	return conformance.StepCanceled
}

// supersede marks what the trace holds below prefix superseded
// (StepRecord.Superseded), except a retried module's attempts there; when
// keep is not empty, its records and child flow move below to instead,
// keep's own record to to itself.
func (r *run) supersede(prefix, keep, to conformance.SitePath) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.supersedeLocked(prefix, keep, to)
}

func (r *run) supersedeLocked(prefix, keep, to conformance.SitePath) {
	move := func(p conformance.SitePath) (conformance.SitePath, bool) {
		if keep.IsRoot() {
			return p, false
		}
		k := keep.String()
		switch t := p.String(); {
		case t == k:
			return to, true
		case strings.HasPrefix(t, k+"/"):
			if q, err := conformance.ParseSitePath(to.String() + t[len(k):]); err == nil {
				return q, true
			}
		}
		return p, false
	}
	below := func(p conformance.SitePath) bool {
		rest, ok := strings.CutPrefix(p.String(), prefix.String()+"/")
		// A retried module's earlier attempts stay where they are.
		return ok && !strings.HasPrefix(rest, "r:")
	}
	for k := range r.trace.Steps {
		rec := &r.trace.Steps[k]
		if rec.Superseded || !below(rec.Site) {
			continue
		}
		var moved bool
		if rec.Site, moved = move(rec.Site); !moved {
			rec.Superseded = true
			continue
		}
		for n, at := range rec.FailedRetries {
			rec.FailedRetries[n], _ = move(at)
		}
	}
	for k := range r.trace.Children {
		c := &r.trace.Children[k]
		if c.Superseded || !below(c.Site) {
			continue
		}
		var moved bool
		if c.Site, moved = move(c.Site); !moved {
			c.Superseded = true
		}
	}
}

// isLoop reports whether modules of kind k are loops, whose children's
// inputs a retry takes from a failed child (CF-22 rule 3).
func isLoop(k ir.Kind) bool { return k == ir.KindForLoop || k == ir.KindWhileLoop }

// restored is previous_result as a retry restores it (FX-6, JE-36 rule 3):
// at the first module, the flow's input (or its first module's
// previous_result); at a later one, the preceding module's stored result
// read back, in jsonb's printed form (JE-44), when that module's status is
// Success, and otherwise the JSON string "{}". The failure module keeps
// its own failed attempt's value, failed (FX-32).
func (r *run) restored(fr *frame, i int, failed jsonval.Value) jsonval.Value {
	switch {
	case fr.isFailure(i):
		return failed
	case i == 0 && !fr.prev.IsZero():
		return fr.prev
	case i == 0:
		return fr.input
	case !fr.succeeded[i-1]:
		return jsonval.FromProducer([]byte(`"{}"`))
	}
	r.mu.Lock()
	v := fr.jobs[i-1]
	r.mu.Unlock()
	if s, err := v.Stored(); err == nil {
		return jsonval.FromProducer(expr.DatabaseText(s))
	}
	return v
}

// begin is how one start phase of a module was entered (FX-2).
type begin struct {
	// first says that it is the flow's pickup, its first module's first
	// start, where a chaining error fails that module or the child flow
	// (FX-15 contexts 2 and 3).
	first bool
	// sleep is the previous module's sleep, at sleepSite, which a module's
	// first start applies (start step 5, FX-23); nil for none.
	sleep     *ir.Transform
	sleepSite conformance.SitePath
	// attempt counts the module's retries before this start; reuse is the
	// failed attempt's recorded arguments, which a retry runs with (start
	// step 8 rule 2, FX-27), and due the retry's scheduled time (FX-26), or
	// the time before which a flow's first job may not start, the sleep
	// before its container (CF-21). base is, for a retried loop, the stored
	// input of its last failed child job, from which its children's inputs
	// come (CF-22 rule 3).
	attempt int
	reuse   *jsonval.Value
	due     time.Time
	base    jsonval.Value
}

// complete runs one attempt of the module at cursor i of fr, started as b,
// from its slot in the trace to the host's completion checks (FX-2
// completion step 7's precondition): the module's own start and
// completion, OnModuleEnd (H6), then Completion, which may latch a fatal
// error name (H7). It returns the outcome and the attempt's record, and
// the status and false when the checks end the run.
func (r *run) complete(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, b begin) (outcome, int, conformance.RunStatus, bool) {
	site := r.site(fr, i)
	// The module's slot in the trace is reserved first, so late output of
	// its invocation always has a record to go to.
	r.mu.Lock()
	idx := len(r.trace.Steps)
	r.trace.Steps = append(r.trace.Steps, StepRecord{Site: site.Path})
	r.mu.Unlock()
	start := r.clock.Now()
	o := r.module(ctx, fr, i, prev, prevID, idx, b)
	if o.rec.Start.IsZero() {
		o.rec.Start, o.rec.Duration = start, r.since(start)
	}
	o.rec.Attempt = b.attempt
	// A module's Success or Failure status lists the flow's failed
	// attempts as its own (FX-27); a Success clears them, and the counter
	// (FX-2 completion step 5, FX-29).
	if o.rec.Module == conformance.ModuleSuccess || o.rec.Module == conformance.ModuleFailure {
		o.rec.FailedRetries = slices.Clone(fr.failed)
	}
	fr.succeeded[i] = o.rec.Module == conformance.ModuleSuccess && o.end != endInterrupted
	if fr.succeeded[i] {
		fr.failed, fr.failCount = nil, 0
	}
	r.span(site.Path, SpanModule, start)
	r.setRecord(idx, o.rec)
	if err := r.hooks.OnModuleEnd(ctx, r, site, o.rec); err != nil {
		r.Latch(HookLatchError(ctx, err))
	}
	fatal := o.end.failed() && r.hooks.FatalErrorName(errorName(o.carried))
	v := Completion(CompletionSignals{
		Latched:  r.latch.Err() != nil,
		Canceled: ctx.Err() != nil,
		// Windmill's continue decision checks a stop before the cancel
		// flag, so an error stop in a flow with a failure module goes on
		// under cancellation (FX-38).
		ErrorStopWithFailureModule: o.end == endErrorStop && fr.failure != nil,
		FatalName:                  fatal,
	})
	if v == EndLatched && r.latch.Err() == nil {
		// H7: the outcome's error latches the run, which bypasses what the
		// flow would have done with the failure.
		r.Latch(&FatalError{Site: site.Path, Name: errorName(o.carried), Value: o.carried})
		r.mark(idx, "H7")
		r.mu.Lock()
		r.trace.Deviations = append(r.trace.Deviations, "H7")
		r.mu.Unlock()
	}
	if st, end := v.Status(); end {
		return o, idx, st, false
	}
	return o, idx, "", true
}

func (r *run) setRecord(idx int, rec StepRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec.Late = append(r.trace.Steps[idx].Late, rec.Late...)
	r.trace.Steps[idx] = rec
}

// mark records that hook h changed what Windmill would have done at the
// module whose record is idx.
func (r *run) mark(idx int, h string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Steps[idx].Hooks = append(r.trace.Steps[idx].Hooks, h)
}

// module runs the start phase of the module at cursor i of fr (FX-2 start
// steps 5 and 7 to 10), started as b, and the first step of its
// completion phase, stop_after_if (FX-19), which the failure module does
// not have. prev is its previous_result and prevID its preceding id; idx
// is its record in the trace.
func (r *run) module(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, idx int, b begin) outcome {
	m := fr.moduleAt(i)
	site := r.site(fr, i)
	o := outcome{rec: StepRecord{Site: site.Path}}
	// H5: a hook that latches the run on the way to the launch stops the
	// module before it runs; the completion check ends the run latched.
	notLaunched := func() outcome {
		o.rec.Status, o.rec.Module, o.end = conformance.StepNotRun, conformance.ModuleNotRun, endInterrupted
		return o
	}
	fail := func(result jsonval.Value, origin Origin) outcome {
		o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried, o.rec.ResultOrigin = conformance.StepFailure, conformance.ModuleFailure, result, result, origin
		o.end, o.carried, o.origin = endFailed, result, origin
		return o
	}
	// A chaining error fails the module or the child flow at the flow's
	// pickup (FX-15 contexts 2 and 3); a retry's re-entry, like the
	// failure module's start, is a later transition (context 1). A
	// collapsed iteration's loop routes it (sequentialLoop).
	chaining := func(text string) outcome {
		return r.chainingAt(fr, b.first && fr.collapsed == nil, o, text)
	}
	if err := r.hooks.OnModuleStart(ctx, r, site); err != nil {
		r.Latch(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return notLaunched()
	}
	// The transition that starts the module resolves flow_env again (JE-38);
	// a collapsed iteration's job is pushed by its loop's transition, which
	// resolved it (sequentialLoop).
	if fr.collapsed == nil {
		r.resolveEnv(ctx, fr)
	}

	// Start step 5: the previous module's sleep sets when the module's job
	// may start; a retry's delay set it at re-entry (FX-23, FX-26). Its
	// failure is a chaining error (FX-15).
	due := b.due
	if b.sleep != nil {
		d, err := r.sleep(ctx, fr, b.sleep, b.sleepSite, prev)
		if err != nil {
			if ctx.Err() != nil {
				return notLaunched()
			}
			return chaining(err.Error())
		}
		due = r.clock.Now().Add(d)
	}

	// Start step 7: skip_if, with P as result and previous_result (FX-17,
	// JE-25). A throw, or a value that is not a boolean, is a chaining
	// error (FX-15).
	skipped := false
	if m.SkipIf != nil {
		v, err := r.predicate(ctx, fr, m.SkipIf, site.Path, r.startScope(fr, prev, prevID))
		if err != nil {
			if ctx.Err() != nil {
				return notLaunched()
			}
			return chaining(predicateText(err))
		}
		skipped = v
	}
	// A container that runs launches no job of its own: no arguments, no
	// H2 and no H1 (CF-1).
	if isContainer(m.Kind) && !skipped && (m.Mock == nil || !m.Mock.Enabled) {
		b.due = due
		return r.container(ctx, fr, i, prev, prevID, o, b)
	}

	// Start step 8: the arguments, by the first rule that applies (FX-2).
	// A mocked, skipped or identity module runs an identity job, which
	// returns its arguments' previous_result, or {} (FX-16). skip_if and
	// mock answer for the module they replace, which H2 and H1 never see;
	// an identity module launches its own identity job, which they do
	// (ADR 0008).
	var args jsonval.Value
	var preRun error // an input transform's failure (FX-14)
	identity, answered, reused := false, false, false
	switch {
	case m.Mock != nil && m.Mock.Enabled:
		// FX-18: the mock value, whatever the module's kind; skip_if was
		// still evaluated.
		identity, answered = true, true
		args = objectOf(member{"previous_result", orNull(m.Mock.ReturnValue)})
	case b.reuse != nil:
		// Rule 2: a retry runs with the failed attempt's recorded
		// arguments, its transforms not evaluated again and a pre-run
		// error not copied (FX-27, JE-V2); newly skipped, it is an
		// identity job of them (FX-17). They went through H2 already.
		identity, answered, reused = skipped || m.Kind == ir.KindIdentity, skipped, true
		args = *b.reuse
	case passDirect(fr, m):
		identity, answered = skipped || m.Kind == ir.KindIdentity, skipped
		args = fr.input
	case skipped || m.Kind == ir.KindIdentity:
		identity, answered = true, skipped
		args = objectOf(member{"previous_result", prev.Producer()})
	default:
		var err error
		if args, err = r.args(ctx, fr, i, inputsOf(m), site.Path, prev, prevID); err != nil {
			if ctx.Err() != nil {
				return notLaunched()
			}
			// FX-14, JE-23: the job still launches, with empty arguments,
			// and fails without running.
			preRun, args = err, jsonObj
		}
	}
	if identity {
		// FX-18: the identity job of a module with any skip_if is
		// classified skipped, whatever the predicate answered, so its
		// stop_after_if is not evaluated (FX-17). The failure module is
		// never flagged skipped, even when its skip_if replaced it with a
		// passthrough (FX-17, FX-32).
		skipped = m.SkipIf != nil && !fr.isFailure(i)
	}
	if !answered && !reused && preRun == nil {
		// H2 rewrites the arguments of a module that launches its own job;
		// the trace keeps both.
		rewritten, err := r.hooks.RewriteArgs(ctx, r, site, args)
		if err != nil {
			r.Latch(HookLatchError(ctx, err))
		} else if !isObject(rewritten) {
			r.Latch(errors.New("engine: H2 returned arguments that are not a JSON object"))
		}
		if r.latch.Err() != nil {
			return notLaunched()
		}
		if !bytes.Equal(rewritten.Producer(), args.Producer()) {
			o.rec.ArgsBeforeRewrite, o.rec.Hooks = args, append(o.rec.Hooks, "H2")
			args = rewritten
		}
	}
	// Start step 9: the module's own timeout (its loop's in a collapsed
	// iteration), which every module evaluates, though only a leaf's job
	// is timed (FX-24). Its failure is a chaining error (FX-15).
	secs, err := r.moduleTimeout(ctx, fr, m, site.Path, prev, prevID)
	if err != nil {
		if ctx.Err() != nil {
			return notLaunched()
		}
		return chaining(err.Error())
	}
	// The storage boundary: each argument becomes its stored text; one
	// that jsonb refuses, by a key or a value, fails the push, a chaining
	// error (ADR 0005 step 3, FX-48, FX-51).
	stored, err := storeMembers(args)
	if err != nil {
		return chaining(err.Error())
	}
	// The arguments are resolved when the job is picked up, in invoke
	// (JE-41); an identity job, or a launch a hook answers, resolves
	// nothing.
	o.rec.RecordedArgs, o.rec.ResolvedArgs = stored, stored

	// Start step 10: the job is pushed, scheduled for when a sleep or a
	// retry delay ends; it waits for that time on the run's clock (FX-23,
	// FX-26). A cancellation makes the queued job due at once, and it
	// fails without running with the Canceled error, a failure and not a
	// canceled job (FX-38, recorded by fx-23/canceled_sleep).
	if !due.IsZero() {
		if err := r.wait(ctx, site.Path, due); err != nil {
			reason, canceler := cancellationOf(ctx)
			v := canceledError(reason, canceler)
			o.rec.Ran, o.rec.Start = true, r.clock.Now()
			o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried, o.rec.ResultOrigin = conformance.StepFailure, conformance.ModuleFailure, v, v, OriginEngine
			o.end, o.carried, o.origin = endInterrupted, v, OriginEngine
			r.recordJob(fr, i, v)
			return o
		}
		if r.latch.Err() != nil {
			return notLaunched()
		}
	}
	o.rec.Start = r.clock.Now()
	if preRun != nil {
		o.rec.Ran = true
		out := fail(transformError(preRun, fr.collapsed != nil), OriginEngine)
		r.recordJob(fr, i, out.rec.Result)
		r.ended(fr, &out)
		return out
	}
	if answered {
		return r.identityJob(ctx, fr, i, site.Path, o, stored, skipped)
	}

	// The launch, where H1 may answer for the module.
	out, intercepted, err := r.hooks.Intercept(ctx, r, site, stored)
	if err != nil {
		r.Latch(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return notLaunched()
	}
	var job string // the id of the leaf's job, once it ran
	switch {
	case intercepted:
		o.rec.Hooks = append(o.rec.Hooks, "H1")
	case identity:
		return r.identityJob(ctx, fr, i, site.Path, o, stored, skipped)
	case m.Kind == ir.KindSubflow:
		return r.subflow(ctx, fr, i, site.Path, o, stored)
	default:
		entry, _, err := Entrypoint(stored)
		if err != nil {
			// WR-12: the job fails before any wrapper runs.
			o.rec.Ran = true
			out := fail(engineError("ExecutionErr", err.Error()), OriginEngine)
			r.recordJob(fr, i, out.rec.Result)
			r.ended(fr, &out)
			return out
		}
		// H3: the host may veto the run's first write-capable launch. H4:
		// the pre-write deadline applies until that launch, so one that
		// has passed stops it before the host is asked, and one that
		// passes while the host decides stops it after; otherwise the
		// launch disarms it.
		if r.hooks.WriteCapable(site) {
			r.writeMu.Lock()
			if !r.wrote && !r.preWriteDue() {
				r.wrote = true
				if err := r.hooks.BeforeFirstWrite(ctx, r, site); err != nil {
					r.Latch(HookLatchError(ctx, err))
				}
				if r.latch.Err() == nil {
					r.disarmPreWrite()
				}
			}
			r.writeMu.Unlock()
			if r.latch.Err() != nil || r.preWriteExpired() {
				return notLaunched()
			}
			r.mu.Lock()
			r.trace.MayHaveMutated = true
			r.mu.Unlock()
		}
		timeout, instanceWide := r.leafTimeout(fr, m, secs)
		o.rec.Ran = true
		var resolved jsonval.Value
		out, job, resolved, err = r.invoke(ctx, fr, i, site, entry, stored, timeout, idx)
		o.rec.Job = job
		if !resolved.IsZero() {
			// The script got the resolved arguments, however its job ended
			// (O3, JE-V3).
			o.rec.ResolvedArgs = resolved
		}
		var rf *resolveFailure
		switch {
		case errors.Is(err, errNotLaunched):
			o.rec.Ran = false
			return notLaunched()
		case errors.As(err, &rf):
			// JE-42 rule 4, WR-5: the job fails before its script runs, with
			// the worker's execution error.
			res := fail(workerError(fr.stepID(i), "execution error:\n"+rf.Error()), OriginEngine)
			r.recordJob(fr, i, res.rec.Result)
			r.ended(fr, &res)
			return res
		case errors.Is(err, CancelTimeout):
			o.rec.Abandoned = true
			// The worker's text for a step that ran out of time: WR-37's
			// for Bun, WR-44's for native.
			res := fail(TimeoutError(Boundary(m.Leaf.Boundary), fr.stepID(i), timeout, instanceWide), OriginEngine)
			r.recordJob(fr, i, res.rec.Result)
			r.ended(fr, &res)
			return res
		case err != nil && ctx.Err() != nil:
			// The run was canceled (H4): the step's job is canceled, with
			// the worker's text for a killed job (FX-38), and the
			// completion check ends the run.
			o.rec.Abandoned = true
			reason, canceler := cancellationOf(ctx)
			v := killedError(fr.stepID(i), reason, canceler)
			o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried, o.rec.ResultOrigin = conformance.StepCanceled, conformance.ModuleFailure, v, v, OriginEngine
			o.end, o.carried, o.origin = endInterrupted, v, OriginEngine
			r.recordJob(fr, i, v)
			r.ended(fr, &o)
			return o
		case err != nil:
			res := fail(engineError("InternalErr", err.Error()), OriginEngine)
			r.recordJob(fr, i, res.rec.Result)
			r.ended(fr, &res)
			return res
		}
	}
	r.ended(fr, &o)
	st := StoreOutcome(out, fr.stepID(i))
	if _, err := st.Value.Stored(); err != nil && job != "" {
		// FX-48 case 1, OR-27: the completion insert refuses a result, or
		// a failure value, that jsonb rejects, and the job fails with the
		// storage error, with no step_id, which the flow handles as any
		// failure. Windmill retries the insert for about 30 s first, a
		// delay windmill-lite does not reproduce (OR-27: out of scope).
		st = Stored{Failed: true, Value: engineError("InternalErr", "Internal: Could not add completed job "+job+": "+databaseError(err).Error()), Origin: OriginEngine}
	}
	r.recordJob(fr, i, st.Value)
	if st.Failed {
		if intercepted {
			st.Origin = OriginHost
		}
		return fail(st.Value, st.Origin)
	}
	o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried = conformance.StepSuccess, conformance.ModuleSuccess, st.Value, st.Value
	o.carried = st.Value
	if skipped {
		// An intercepted identity is classified as its identity job would
		// have been (FX-18).
		o.rec.Skipped = true
		return o
	}
	return r.stop(ctx, fr, i, site.Path, o)
}

// The states of a pre-write deadline (run.preWrite).
const (
	preWriteArmed int32 = iota
	preWriteDisarmed
	preWriteExpired
)

// expirePreWrite expires the run's pre-write deadline, unless the first
// write disarmed it first, and reports whether the deadline is expired;
// an expired one has cancelled the run (preWriteExpired).
func (r *run) expirePreWrite() bool {
	r.preWrite.CompareAndSwap(preWriteArmed, preWriteExpired)
	return r.preWriteExpired()
}

// preWriteExpired reports whether the run's pre-write deadline has
// expired, and if so cancels the run with ErrPreWriteDeadline (H4) before
// it returns. Whoever wins the expiry, the timer or the interpreter, may
// still be on its way to cancelling; cancelling again is idempotent, so
// nothing acts on an expired deadline before the run's context says so.
func (r *run) preWriteExpired() bool {
	if r.preWrite.Load() != preWriteExpired {
		return false
	}
	r.cancel(ErrPreWriteDeadline)
	return true
}

// preWriteDue reports whether the run's pre-write deadline stops a first
// write now: it has expired, or its time has come, which expires it.
func (r *run) preWriteDue() bool {
	if r.preWriteAt.IsZero() {
		return false
	}
	if time.Now().Before(r.preWriteAt) {
		return r.preWriteExpired()
	}
	return r.expirePreWrite()
}

// disarmPreWrite disarms the run's pre-write deadline at its first
// write-capable launch, once the host has approved it (H3, H4), and
// reports whether the write may launch: not when the deadline passed
// first, while the host decided included. A disarmed deadline never
// expires, so it cannot cancel the run.
func (r *run) disarmPreWrite() bool {
	if r.preWriteDue() {
		return false
	}
	r.preWrite.CompareAndSwap(preWriteArmed, preWriteDisarmed)
	return !r.preWriteExpired()
}

// ended records that the job of o, a job of fr's flow which started at
// o.rec.Start, has ended: its duration, and the end of the transition that
// pushed it, which starts the next one (JE-2's monitor, which a collapsed
// iteration's loop's flow keeps).
func (r *run) ended(fr *frame, o *outcome) {
	o.rec.Duration = r.since(o.rec.Start)
	r.ping(fr)
}

// ping refreshes the ping of fr's flow, a collapsed iteration's its
// loop's: one of its jobs, or one of its child flows, has completed, which
// ends the transition the monitor measures and starts the next (JE-2: a
// flow job's ping is refreshed when a child completes). Only the goroutine
// that runs the flow calls it.
func (r *run) ping(fr *frame) {
	own := fr.owner()
	own.spent, own.pinged = 0, r.clock.Now()
}

// passDirect reports whether module m of fr receives its flow's input as
// its arguments (OF-V4); a collapsed body takes its loop's flag instead of
// its own (CF-8 item 5), and its flow input is its iteration's.
func passDirect(fr *frame, m *ir.Module) bool {
	if fr.collapsed != nil {
		return fr.collapsed.PassFlowInputDirectly
	}
	return m.PassFlowInputDirectly
}

// identityJob runs the identity job (FX-16) of the module at cursor i of
// fr, which the worker runs at once, with no timer (FX-24): its result is
// its stored arguments' previous_result, or {}. A job classified skipped
// does not evaluate the module's stop_after_if (FX-17, FX-18).
func (r *run) identityJob(ctx context.Context, fr *frame, i int, site conformance.SitePath, o outcome, stored jsonval.Value, skipped bool) outcome {
	res := identityResult(stored)
	r.ended(fr, &o)
	o.rec.Ran, o.rec.Skipped = true, skipped
	o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried = conformance.StepSuccess, conformance.ModuleSuccess, res, res
	o.carried = res
	r.recordJob(fr, i, res)
	if skipped {
		return o
	}
	return r.stop(ctx, fr, i, site, o)
}

// stop is completion step 1 for a module whose job succeeded: its
// stop_after_if, with the module's own result as result and
// previous_result (FX-19, JE-26). o is the module's successful outcome.
// The failure module has no stop (FX-2 completion step 1).
func (r *run) stop(ctx context.Context, fr *frame, i int, site conformance.SitePath, o outcome) outcome {
	s := fr.moduleAt(i).StopAfterIf
	if s == nil || fr.isFailure(i) {
		return o
	}
	own := o.carried
	v, err := r.predicate(ctx, fr, s.Expr, site, r.stopScope(fr, s.Expr, own))
	switch {
	case err != nil && ctx.Err() != nil:
		o.end = endInterrupted
		return o
	case err != nil:
		// FX-21: the completion is redone as an unrecoverable failure whose
		// error is the predicate's text, a string; the job's stored result
		// stays the step's success (FX-V2).
		return stopFailed(o, err)
	case !v:
		return o
	}
	return applyStop(s, o, own)
}

// stopFailed is a module whose stop_after_if threw (FX-21, JE-26): it
// fails unrecoverably, carrying the predicate's text as a string.
func stopFailed(o outcome, err error) outcome {
	o.rec.Module, o.end = conformance.ModuleFailure, endUnrecoverable
	o.carried, o.origin, o.nested = objectOf(member{"error", jsonString(predicateText(err))}), OriginEngine, nil
	o.rec.Carried, o.rec.CarriedOrigin, o.rec.CarriedNested = o.carried, OriginEngine, nil
	return o
}

// applyStop applies a stop that fired (FX-19): a clean stop ends the flow
// with the module's result, as skipped with skip_if_stopped, which wins
// over error_message; an error stop fails the module, which carries an
// EarlyStopError while its job keeps its result (FX-V2). own is the result
// that error_include_result adds.
func applyStop(s *ir.Stop, o outcome, own jsonval.Value) outcome {
	switch {
	case s.SkipIfStopped:
		o.rec.Skipped, o.end = true, endStoppedSkipped
		return o
	case s.ErrorMessage == nil:
		o.end = endStopped
		return o
	}
	msg := *s.ErrorMessage
	if msg == "" {
		msg = "stop after if: " + s.Expr.Text
	}
	return errorStop(o, msg, s, own)
}

// errorStop fails the module with an EarlyStopError whose message is msg,
// with own as its result when s asks for it (FX-19, FX-V2). own is o's
// carried value: when it is included, the generated errors inside it keep
// their provenance below /error/result (ADR 0011); otherwise they go with
// it.
func errorStop(o outcome, msg string, s *ir.Stop, own jsonval.Value) outcome {
	e := []member{{"name", jsonString("EarlyStopError")}, {"message", jsonString(msg)}}
	var nested []string
	if s.ErrorIncludeResult != nil && *s.ErrorIncludeResult {
		e = append(e, member{"result", own.Producer()})
		for _, p := range o.nested {
			nested = append(nested, "/error/result"+p)
		}
	}
	o.carried, o.origin, o.nested = objectOf(member{"error", objectOf(e...).Producer()}), OriginEngine, nested
	o.rec.Module, o.rec.Carried, o.rec.CarriedOrigin, o.rec.CarriedNested, o.end = conformance.ModuleFailure, o.carried, OriginEngine, nested, endErrorStop
	return o
}

// chaining is a chaining error raised while the module at cursor i of fr
// starts (FX-15), whose display text is text (without Windmill's Rust
// location, D14). The start of a flow's first module is its pickup; the
// failure module's start is a later transition; the push of a collapsed
// iteration's job is routed again by its loop (sequentialLoop).
func (r *run) chaining(fr *frame, i int, o outcome, text string) outcome {
	return r.chainingAt(fr, i == 0 && fr.collapsed == nil, o, text)
}

// chainingAt routes a chaining error of fr's flow, raised at its pickup or
// not (FX-15, CF-23):
//
//   - at the root's pickup it is a failure of the module with no job, named
//     Anyhow (context 2), which continue_on_error and the failure module
//     then handle;
//   - at a child flow's pickup the child fails at once with that error, and
//     its first module never starts (context 3, CF-V3);
//   - at a later transition the flow fails at once with it, named
//     InternalError, and the module never starts (context 1).
func (r *run) chainingAt(fr *frame, pickup bool, o outcome, text string) outcome {
	o.chain = text
	switch {
	case pickup && !fr.child:
		v := engineError("Anyhow", "Error: "+text)
		o.rec.Status, o.rec.Module, o.rec.Carried, o.rec.CarriedOrigin = conformance.StepFailure, conformance.ModuleFailure, v, OriginEngine
		o.end, o.carried, o.origin = endFailed, v, OriginEngine
		return o
	case pickup:
		o.rec.Status, o.rec.Module = conformance.StepNotRun, conformance.ModuleNotRun
		o.end, o.carried, o.origin = endChained, engineError("Anyhow", "Error: "+text), OriginEngine
		return o
	}
	o.rec.Status, o.rec.Module = conformance.StepNotRun, conformance.ModuleNotRun
	o.end, o.carried, o.origin = endChained, engineError("InternalError", text), OriginEngine
	return o
}

// The names every start-phase site binds besides previous_result (FX-7,
// JE-22): a preceding suspend's resume, resumes and approvers, which are
// null, [] and [] without one.
var (
	jsonNull  = jsonval.FromProducer([]byte("null"))
	jsonEmpty = jsonval.FromProducer([]byte("[]"))
	jsonObj   = jsonval.FromProducer([]byte("{}"))
)

// startScope is the scope of skip_if and of a for loop's iterator (JE-22,
// JE-30): P as result and previous_result, the previous id, results with
// JE-9, flow_input and flow_env.
func (r *run) startScope(fr *frame, prev jsonval.Value, prevID string) *expr.Scope {
	return &expr.Scope{
		PreviousID: prevID,
		Context: []expr.Named{
			{Name: "previous_result", Value: prev}, {Name: "result", Value: prev},
			{Name: "resume", Value: jsonNull}, {Name: "resumes", Value: jsonEmpty}, {Name: "approvers", Value: jsonEmpty},
		},
		FlowInput: fr.input,
		FlowEnv:   r.envOf(fr),
		Results:   r.results(fr),
		Helpers:   helpers{r.e.platform, r.rootID},
	}
}

// transformScope is the scope of the input transforms of the module at
// cursor i of fr (FX-7, JE-22, JE-23): P as previous_result and no result,
// params always {}, the preceding id, results with JE-9, flow_input and
// flow_env. The failure module's also see started_at, fr's flow's start
// time as an RFC 3339 string, and flow_job_id, its job id: the run's, or a
// child flow's or a subflow's for a copy in a body or a subflow's own
// (FX-32, JE-35, CF-16 item 3).
func (r *run) transformScope(fr *frame, i int, prev jsonval.Value, prevID string) *expr.Scope {
	s := &expr.Scope{
		PreviousID: prevID,
		Context: []expr.Named{
			{Name: "previous_result", Value: prev}, {Name: "params", Value: jsonObj},
			{Name: "resume", Value: jsonNull}, {Name: "resumes", Value: jsonEmpty}, {Name: "approvers", Value: jsonEmpty},
		},
		FlowInput: fr.input,
		FlowEnv:   r.envOf(fr),
		Results:   r.results(fr),
		Helpers:   helpers{r.e.platform, r.rootID},
	}
	if fr.isFailure(i) {
		// The flow job's: the run's at the root, a body's or a subflow's
		// own when the failure module runs inside it (FX-32, FX-34).
		s.Context = append(s.Context,
			expr.Named{Name: "started_at", Value: jsonval.FromProducer(jsonString(rfc3339(fr.started)))},
			expr.Named{Name: "flow_job_id", Value: jsonval.FromProducer(jsonString(fr.id))})
	}
	return s
}

// stopScope is the scope of stop_after_if and stop_after_all_iters_if
// (FX-8, JE-22, JE-27): own as result and previous_result, in its producer
// text (FX-42 item 3); an empty previous id; results only when the text
// holds results., results[ or results?.; flow_env only when it holds
// flow_env, the flow's cached resolved map once one exists (JE-38).
func (r *run) stopScope(fr *frame, x *expr.Expr, own jsonval.Value) *expr.Scope {
	s := &expr.Scope{
		Context:   []expr.Named{{Name: "previous_result", Value: own}, {Name: "result", Value: own}},
		FlowInput: fr.input,
		Helpers:   helpers{r.e.platform, r.rootID},
	}
	t := x.Trimmed
	if strings.Contains(t, "results.") || strings.Contains(t, "results[") || strings.Contains(t, "results?.") {
		s.Results = r.results(fr)
	}
	if strings.Contains(t, "flow_env") {
		s.FlowEnv = r.cachedEnvOf(fr)
	}
	return s
}

// sleepScope is the scope of a sleep (FX-8, JE-22): the sleeping module's
// own result as result and previous_result, no other names, no results
// (results is not defined), flow_input, flow_env as the transition that
// starts the next module resolved it (JE-38), and the helpers (JE-20).
func (r *run) sleepScope(fr *frame, own jsonval.Value) *expr.Scope {
	return &expr.Scope{
		Context:   []expr.Named{{Name: "previous_result", Value: own}, {Name: "result", Value: own}},
		FlowInput: fr.input,
		FlowEnv:   r.envOf(fr),
		Helpers:   helpers{r.e.platform, r.rootID},
	}
}

// timeoutScope is the scope of a javascript timeout (FX-8, JE-22): P as
// result and previous_result, no other names, results with JE-9 and the
// preceding id, flow_input, flow_env as the module's transition resolved
// it (JE-38), and the helpers (JE-20).
func (r *run) timeoutScope(fr *frame, prev jsonval.Value, prevID string) *expr.Scope {
	return &expr.Scope{
		PreviousID: prevID,
		Context:    []expr.Named{{Name: "previous_result", Value: prev}, {Name: "result", Value: prev}},
		FlowInput:  fr.input,
		FlowEnv:    r.envOf(fr),
		Results:    r.results(fr),
		Helpers:    helpers{r.e.platform, r.rootID},
	}
}

// retryScope is the scope of retry_if (FX-8, JE-22, JE-32): the failed
// attempt's value as result and previous_result, whose error is error
// (JE-17), no other names and no results, and the helpers (JE-20);
// flow_env, as at every predicate evaluated after a module completes,
// only when the text names it, the flow's cached resolved map once one
// exists (JE-38), as stopScope binds it.
func (r *run) retryScope(fr *frame, x *expr.Expr, failed jsonval.Value) *expr.Scope {
	s := &expr.Scope{
		Context:   []expr.Named{{Name: "previous_result", Value: failed}, {Name: "result", Value: failed}},
		FlowInput: fr.input,
		Helpers:   helpers{r.e.platform, r.rootID},
	}
	if strings.Contains(x.Trimmed, "flow_env") {
		s.FlowEnv = r.cachedEnvOf(fr)
	}
	return s
}

// evaluate evaluates one expression of fr's flow, timed as a span, under
// the monitor of flows that hang between steps (JE-2): the time a
// transition spends evaluating counts against the engine's transition
// timeout, past which the run is canceled by "monitor", as Windmill's
// server cancels a flow whose transition outlives
// FLOW_ZOMBIE_TRANSITION_TIMEOUT. Only evaluations count, not hooks or
// waits, which Windmill does not have or runs as scheduled jobs; and the
// budget is wall time, where its timer runs, whatever the run's clock (a
// recording clock shared by several runs moves on with their waits).
func (r *run) evaluate(ctx context.Context, fr *frame, x *expr.Expr, site conformance.SitePath, s *expr.Scope) (jsonval.Value, error) {
	own := fr.owner()
	start, wall := r.clock.Now(), time.Now()
	var monitor *time.Timer
	if limit := r.e.transition; limit > 0 {
		hung := &Cancellation{Canceler: "monitor", Reason: fmt.Sprintf("Flow %s was cancelled because it was hanging in between 2 steps. Last ping: %s", own.id, rfc3339(own.pinged))}
		monitor = time.AfterFunc(max(limit-own.spent, 0), func() { r.cancel(hung) })
	}
	v, err := r.e.eval.Eval(ctx, x, s)
	if monitor != nil {
		monitor.Stop()
	}
	own.spent += time.Since(wall)
	r.span(site, SpanExpression, start)
	return v, err
}

// predicate evaluates a boolean site of fr's flow (JE-24). A value other
// than JSON true or false fails with Windmill's check.
func (r *run) predicate(ctx context.Context, fr *frame, x *expr.Expr, site conformance.SitePath, s *expr.Scope) (bool, error) {
	v, err := r.evaluate(ctx, fr, x, site, s)
	if err != nil {
		return false, err
	}
	switch t := string(bytes.TrimSpace(v.Producer())); t {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, &notBoolean{text: t}
	}
}

// args evaluates the input transforms of the module at cursor i of fr into
// its argument object (FX-7, JE-23): one at a time, in an order the
// frame's rng shuffles (a hash map's on Windmill, JE-V5), stopping at the
// first error; the object's top-level keys come in that order too (ADR
// 0005 step 2). A static value is copied verbatim (JE-40), an ai input
// left out.
func (r *run) args(ctx context.Context, fr *frame, i int, ins []ir.Input, site conformance.SitePath, prev jsonval.Value, prevID string) (jsonval.Value, error) {
	var ms []member
	var scope *expr.Scope
	for _, k := range fr.rng.Perm(len(ins)) {
		in := ins[k]
		switch in.Transform.Type {
		case ir.Static:
			ms = append(ms, member{in.Name, orNull(in.Transform.Value)})
		case ir.Javascript:
			if scope == nil {
				scope = r.transformScope(fr, i, prev, prevID)
			}
			v, err := r.evaluate(ctx, fr, in.Transform.Expr, site, scope)
			if err != nil {
				return jsonval.Value{}, &transformFailure{text: in.Transform.Expr.Text, err: err}
			}
			ms = append(ms, member{in.Name, v.Producer()})
		}
	}
	return objectOf(ms...), nil
}

// moduleTimeout evaluates the module's own timeout, start step 9 (FX-24,
// JE-29), its loop's in a collapsed iteration (CF-8, CF-27): its seconds,
// or nil when it has none or a static null. A static one is an i32
// (OF-V2); a javascript one sees P as result and previous_result, results
// with JE-9 and the preceding id (JE-22), and its value must deserialise
// as an i32. A negative timeout, a value that does not deserialise and a
// failed evaluation are chaining errors, whose texts the error carries
// (FX-15).
func (r *run) moduleTimeout(ctx context.Context, fr *frame, m *ir.Module, site conformance.SitePath, prev jsonval.Value, prevID string) (*int64, error) {
	t := m.Timeout
	if fr.collapsed != nil {
		t = fr.collapsed.Timeout
	}
	switch {
	case t == nil:
		return nil, nil
	case t.Type == ir.Static:
		return staticTimeout(t.Value)
	case t.Expr == nil:
		// A javascript timeout with no expression (OF-V2 admits a null
		// expr) asks for none.
		return nil, nil
	}
	scope := r.timeoutScope(fr, prev, prevID)
	if fr.collapsed != nil {
		// A collapsed loop's timeout is evaluated for each body job in the
		// loop's own scope, its flow input without iter (CF-21, recorded by
		// cf-21/timeout_scope_collapsed).
		scope = r.timeoutScope(fr.parent, fr.loopPrev, fr.loopPrevID)
	}
	v, err := r.evaluate(ctx, fr, t.Expr, site, scope)
	if err != nil {
		return nil, evaluationFailure(t.Expr, err)
	}
	n, err := timeoutResult(v.Producer())
	if err != nil {
		return nil, err
	}
	secs := int64(n)
	return &secs, nil
}

// evaluationFailure is the text Windmill's worker gives a failed
// evaluation of a typed site's expression (JE-28, JE-29): the expression
// as written, then the evaluator's text.
func evaluationFailure(x *expr.Expr, err error) error {
	return &evalFailure{text: "Error during evaluation of expression `" + x.Text + "`:\n" + evalText(err), err: err}
}

// evalFailure is a failed evaluation with the text a site reports it with.
type evalFailure struct {
	text string
	err  error
}

func (e *evalFailure) Error() string { return e.text }
func (e *evalFailure) Unwrap() error { return e.err }

// leafTimeout returns a leaf's timeout and whether it counts as
// instance-wide (FX-24, WR-37), given the module's own timeout in seconds
// (its loop's in a collapsed iteration), secs, or nil. The request is that
// timeout, else a path script's own timeout metadata; one strictly below
// the instance maximum is used as it is, and counts as job-specific, zero
// included, which leaves the step no time at all. With no request, or one
// at or above the maximum, the maximum applies (windmill-lite has no
// instance default job timeout below it).
func (r *run) leafTimeout(fr *frame, m *ir.Module, secs *int64) (time.Duration, bool) {
	if secs == nil && m.Kind == ir.KindScript {
		if s := r.e.prog.Scripts[m.Leaf.Script]; s != nil && s.Metadata != nil && s.Metadata.Timeout != nil {
			v := int64(*s.Metadata.Timeout)
			secs = &v
		}
	}
	if secs == nil {
		return r.e.cap, true
	}
	d := time.Duration(*secs) * time.Second
	if d >= r.e.cap {
		return r.e.cap, true
	}
	return d, false
}

// sleep reads the previous module's sleep at a module's first start,
// start step 5 (FX-23, JE-28): a static one's value, or a javascript one
// evaluated with that module's own result own as result and
// previous_result and no results (JE-22). A failed evaluation fails with
// "Expected a number value, had error instead: ExecutionErr(…)", the debug
// form of the evaluation's error, and so does a result that is not JSON
// (a replaced JSON.stringify, JE-15), which the evaluation itself fails
// to read as a JSON value: "Error parsing result as
// serde_json::value::Value: <serde_json's error>. Value was: <the text>"
// (recorded by fx-23/sleep_not_json). A value that is not a delay fails
// with sleepDelay's texts. All are chaining errors (FX-15).
func (r *run) sleep(ctx context.Context, fr *frame, t *ir.Transform, site conformance.SitePath, own jsonval.Value) (time.Duration, error) {
	text := []byte(orNull(t.Value))
	if t.Type == ir.Javascript {
		if t.Expr == nil {
			return sleepDelay([]byte("null"))
		}
		v, err := r.evaluate(ctx, fr, t.Expr, site, r.sleepScope(fr, own))
		if err != nil {
			return 0, &evalFailure{text: "Expected a number value, had error instead: ExecutionErr(" + rustDebug(evaluationFailure(t.Expr, err).Error()) + ")", err: err}
		}
		text = v.Producer()
		d, err := sleepDelay(text)
		var nj notJSON
		if errors.As(err, &nj) {
			return 0, &evalFailure{text: "Expected a number value, had error instead: ExecutionErr(" + rustDebug("Error parsing result as serde_json::value::Value: "+string(nj)+". Value was: "+string(text)) + ")", err: err}
		}
		return d, err
	}
	return sleepDelay(text)
}

// wait waits until due on the run's clock (H10), a sleep's or a retry's
// scheduled time (FX-23, FX-26), as a timer on the run's context, so a
// cancellation or a deadline cuts it short (ADR 0004). The requested wait
// is a span. It returns context.Cause(ctx) when ctx ended it.
func (r *run) wait(ctx context.Context, site conformance.SitePath, due time.Time) error {
	start := r.clock.Now()
	d := due.Sub(start)
	if d <= 0 {
		return nil
	}
	err := r.clock.Wait(ctx, d)
	span := Span{Site: site, Kind: SpanWait, Start: start, Duration: d}
	if err != nil {
		span.Duration = r.since(start)
	}
	r.addSpan(span)
	return err
}

// attemptsLeft reports whether FX-26's schedule has an attempt left for a
// module that has failed n times in the current retry sequence.
func attemptsLeft(r *ir.Retry, n uint32) bool {
	var constant uint32
	if r.Constant != nil {
		constant = r.Constant.Attempts
	}
	return n < constant || r.Exponential != nil && n-constant < r.Exponential.Attempts
}

// errNotLaunched: a hook latched the run before the step launched.
var errNotLaunched = errors.New("engine: the run latched before the step launched")

// invoke runs the module at cursor i of fr in its harness, with its
// timeout as a timer in the engine (ADR 0004 §Cancellation and deadlines
// item 2), and returns its outcome, its job's id, WM_JOB_ID, and the
// arguments its script received: args after the worker's resolution
// (JE-41, JE-42), which fails it with a *resolveFailure before it runs.
// The job identity follows the flow the job runs in (WA-6, WA-V1,
// ADR 0007): its job id, its innermost scope and its runnable path.
func (r *run) invoke(ctx context.Context, fr *frame, i int, site Site, entry string, args jsonval.Value, timeout time.Duration, idx int) (StepOutcome, string, jsonval.Value, error) {
	m := fr.moduleAt(i)
	in := fr.owner()
	inv := Invocation{
		ID:          newUUID(),
		Run:         r.id,
		Site:        site.Path,
		Script:      m.Leaf.Script,
		Entry:       entry,
		Args:        args,
		Boundary:    Boundary(m.Leaf.Boundary),
		ModuleState: r.e.state,
	}
	// Job identity (ADR 0007, WA-4, WA-6, WA-V1): the flow the job runs in
	// is its parent (a child flow, or the loop's flow for a collapsed
	// iteration), with that flow's innermost scope and runnable path, under
	// the run as its effective root.
	inv.Env = platform.Identity{
		JobID:         inv.ID,
		FlowJobID:     in.id,
		RootFlowJobID: in.scope,
		RootJobID:     r.rootID,
		FlowPath:      in.path,
		StepID:        fr.stepID(i),
		JobPath:       r.jobPath(fr, i),
		Token:         platform.NewToken(),
	}.Env(r.e.host)
	// The job's reserved variables, which its argument resolution reads
	// (WR-5, WA-V3): $<NAME> strings, and, with its stored root, $WM_*
	// strings in a resource an argument names; neither the script's own
	// envs nor H12's changes are among them.
	reserved := maps.Clone(inv.Env)
	addScriptEnvs(inv.Env, r.e.prog.Scripts[m.Leaf.Script], inv.Boundary)
	if err := r.hooks.InvocationEnv(ctx, r, site, inv.Env); err != nil {
		r.Latch(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return StepOutcome{}, "", jsonval.Value{}, errNotLaunched
	}
	// The worker resolves the arguments once it has picked the job up,
	// before the script runs (JE-41, JE-42, WR-5).
	resolved, err := r.e.platform.ResolveArgs(ctx, args, reserved, platform.APIEnv(reserved))
	if err != nil {
		return StepOutcome{}, inv.ID, jsonval.Value{}, &resolveFailure{err}
	}
	inv.Args = resolved
	inv.Report = r.settle.Track(&runReporter{run: r, idx: idx})
	stepCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if timeout <= 0 {
		cancel(CancelTimeout)
	} else {
		t := time.AfterFunc(timeout, func() { cancel(CancelTimeout) })
		defer t.Stop()
	}
	start := r.clock.Now()
	out, err := r.exec.Execute(stepCtx, inv)
	r.span(site.Path, SpanHarness, start)
	return out, inv.ID, resolved, err
}

// runReporter records what an invocation produces late in its module's
// record.
type runReporter struct {
	run *run
	idx int
}

func (p *runReporter) Late(l Late) {
	r := p.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Steps[p.idx].Late = append(r.trace.Steps[p.idx].Late, l)
}

func (p *runReporter) Warning(w Warning) {
	r := p.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Warnings = append(r.trace.Warnings, w)
}

func (p *runReporter) Settled(SettleKind) {}

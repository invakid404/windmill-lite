package engine

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
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
// it, and one more each running iteration of a parallel loop; the
// Reporters of its invocations write late output into its trace from
// others, under mu.
type run struct {
	e     *Engine
	f     *ir.Flow
	id    string
	hooks Hooks
	exec  StepExecutor
	// root is the frame of the flow's own modules, its failure module in
	// the slot after them; its input is the flow input, stored, with its
	// top-level keys shuffled (FX-42 item 1). env is the flow's flow_env, or
	// zero (JE-38), which its child flows inherit (CF-1).
	root   *frame
	env    jsonval.Value
	latch  Latch
	settle *RunSettlement
	// halted is set once a module ended the run halted (H8), so that the
	// iterations of a parallel loop start nothing more; wake is closed, once,
	// when the run is latched or halted, which wakes a parallel loop whose
	// iterations are all held.
	halted   atomic.Bool
	wake     chan struct{}
	wakeOnce sync.Once
	// wrote is set once a write-capable module has launched (H3); writeMu
	// holds the iterations of a parallel loop back while H3 decides.
	writeMu sync.Mutex
	wrote   bool
	// started is when the run started: the flow's start time, which a
	// failure module's input transforms see as started_at (JE-35).
	started time.Time

	mu    sync.Mutex // guards trace and the frames' job results
	trace Trace
}

func newRun(e *Engine, f *ir.Flow, id string, hooks Hooks, exec StepExecutor, rng *rand.Rand) *run {
	r := &run{e: e, f: f, id: id, hooks: hooks, exec: exec, wake: make(chan struct{}), started: time.Now()}
	r.root = newFrame(f.Modules, conformance.SitePath{})
	r.root.failure = f.FailureModule
	r.root.path, r.root.id, r.root.scope, r.root.nodes, r.root.rng = f.Path, id, id, newNodeScope(nil), rng
	r.trace = Trace{RunID: id, Bundle: e.bundle, ModuleState: ModuleStateWarm}
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
// belong to the enclosing flow, so Flow is the run's flow, and Script is
// the leaf's static path, <flow path>/<module id> for an inline script.
func (r *run) site(fr *frame, i int) Site {
	m := fr.moduleAt(i)
	s := Site{Path: fr.sites[i], Flow: r.f.Path, Module: m.ID}
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
		t.Steps[i] = st
	}
	t.ResultNested = append([]string(nil), r.trace.ResultNested...)
	t.Children = make([]ChildFlow, len(r.trace.Children))
	for i, c := range r.trace.Children {
		c.Modules = append([]string(nil), c.Modules...)
		t.Children[i] = c
	}
	t.Warnings = append([]Warning(nil), r.trace.Warnings...)
	t.Deviations = append([]string(nil), r.trace.Deviations...)
	t.Spans = append([]Span(nil), r.trace.Spans...)
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
	r.mu.Lock()
	r.trace.Spans = append(r.trace.Spans, Span{Site: site, Kind: kind, Start: start, Duration: time.Since(start)})
	r.mu.Unlock()
}

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
	// endHalted: a module in the module's loop body ended the run halted
	// (H8); the completion check still ranks a latch or the cancellation
	// first.
	endHalted
)

// failed reports whether the module is Failure: what H7 and Windmill's
// continue decision treat as a failure.
func (e moduleEnd) failed() bool {
	return e == endFailed || e == endErrorStop || e == endUnrecoverable
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
// completion steps 3, 7 and 8) in a flow with no retry (N2):
// continue_on_error (FX-30), the error stops (FX-19, FX-V4), the
// unrecoverable failure of a throwing stop_after_if (FX-21, FX-44) and the
// failure module (FX-31). last says that m is the flow's last module. A
// collapsed iteration's body takes its loop's continue_on_error, not its
// own: its failure fails the iteration (CF-8 item 5).
func (r *run) decide(fr *frame, m *ir.Module, o outcome, last bool) decision {
	hasFailure := fr.failure != nil
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
// for a failure that was an error stop, and the generated errors inside
// the result.
type flowEnd struct {
	status    conformance.RunStatus
	result    jsonval.Value
	origin    Origin
	stopped   bool
	errorStop bool
	nested    []string
	// chain is set when a collapsed iteration's push failed (outcome.chain).
	chain string
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
		// A halt decided in another iteration of a parallel loop comes
		// after the latch and the cancellation (ADR 0008).
		if r.halted.Load() {
			return flowEnd{status: conformance.RunHalted}
		}
		o, idx, st, ok := r.complete(ctx, fr, i, prev, prevID)
		if !ok {
			return flowEnd{status: st}
		}
		if o.end == endHalted {
			return flowEnd{status: conformance.RunHalted}
		}
		d := r.decide(fr, m, o, i == len(fr.modules)-1)
		canceled := ctx.Err() != nil
		end := flowEnd{status: d.status, result: d.result, origin: d.origin, nested: d.nested, chain: o.chain,
			stopped:   o.end == endStopped || o.end == endStoppedSkipped,
			errorStop: o.end == endErrorStop && d.status == conformance.RunFailure}
		// When the frame's flow ends, the run goes on when what contains the
		// frame would start something after that ending (frame.after): a
		// terminal failure or stop of a loop's body starts nothing more.
		runContinues := d.next != nextEnd || fr.after != nil && fr.after(end)
		if AfterContinue(ContinueSignals{RunContinues: runContinues, Final: true, ToFailureModule: d.next == nextFailure, Canceled: canceled, HaltAfter: r.hooks.HaltAfter(site)}) != Proceed {
			if canceled {
				return flowEnd{status: conformance.RunCanceled}
			}
			r.mark(idx, "H8")
			r.halt()
			return flowEnd{status: conformance.RunHalted}
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
// starts is a later transition's (FX-15 context 1).
func (r *run) failure(ctx context.Context, fr *frame, prev jsonval.Value, prevID string, continues bool) flowEnd {
	i := len(fr.modules)
	v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, ContinuesErrorStop: continues, FailureModule: true, HaltBefore: r.hooks.HaltBefore(r.site(fr, i))})
	if st, end := v.Status(); end {
		return flowEnd{status: st}
	}
	o, _, st, ok := r.complete(ctx, fr, i, prev, prevID)
	if !ok {
		return flowEnd{status: st}
	}
	switch {
	case o.end == endSucceeded && recovers(o.carried):
		return flowEnd{status: conformance.RunSuccess, result: o.carried, origin: o.origin}
	case o.end == endSucceeded, o.end == endFailed, o.end == endChained:
		return flowEnd{status: conformance.RunFailure, result: o.carried, origin: o.origin}
	}
	return flowEnd{status: conformance.RunFailure, result: engineError("InternalErr", "windmill-lite: the failure module was interrupted with no latch or cancellation"), origin: OriginEngine}
}

// complete runs the module at cursor i of fr, from its slot in the trace
// to the host's completion checks (FX-2 completion step 7's
// precondition): the module's own start and completion, OnModuleEnd (H6),
// then Completion, which may latch a fatal error name (H7). It returns the
// outcome and the module's record, and the status and false when the
// checks end the run.
func (r *run) complete(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string) (outcome, int, conformance.RunStatus, bool) {
	site := r.site(fr, i)
	// The module's slot in the trace is reserved first, so late output of
	// its invocation always has a record to go to.
	r.mu.Lock()
	idx := len(r.trace.Steps)
	r.trace.Steps = append(r.trace.Steps, StepRecord{Site: site.Path})
	r.mu.Unlock()
	start := time.Now()
	o := r.module(ctx, fr, i, prev, prevID, idx)
	o.rec.Start, o.rec.Duration = start, time.Since(start)
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
// steps 7 to 10) and the first step of its completion phase,
// stop_after_if (FX-19), which the failure module does not have. prev is
// its previous_result and prevID its preceding id; idx is its record in
// the trace.
func (r *run) module(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, idx int) outcome {
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
	if err := r.hooks.OnModuleStart(ctx, r, site); err != nil {
		r.Latch(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return notLaunched()
	}

	// Start step 7: skip_if, with P as result and previous_result (FX-17,
	// JE-25). A throw, or a value that is not a boolean, is a chaining
	// error (FX-15).
	skipped := false
	if m.SkipIf != nil {
		v, err := r.predicate(ctx, m.SkipIf, site.Path, r.startScope(fr, prev, prevID))
		if err != nil {
			if ctx.Err() != nil {
				return notLaunched()
			}
			return r.chaining(fr, i, o, predicateText(err))
		}
		skipped = v
	}
	// A for loop that runs is a container, which launches no job of its
	// own: no arguments, no H2 and no H1 (CF-1).
	if m.Kind == ir.KindForLoop && !skipped && (m.Mock == nil || !m.Mock.Enabled) {
		return r.forLoop(ctx, fr, i, prev, prevID, o)
	}

	// Start step 8: the arguments, by the first rule that applies (FX-2).
	// A mocked, skipped or identity module runs an identity job, which
	// returns its arguments' previous_result, or {} (FX-16). skip_if and
	// mock answer for the module they replace, which H2 and H1 never see;
	// an identity module launches its own identity job, which they do
	// (ADR 0008).
	var args jsonval.Value
	identity, answered := false, false
	switch {
	case m.Mock != nil && m.Mock.Enabled:
		// FX-18: the mock value, whatever the module's kind; skip_if was
		// still evaluated.
		identity, answered = true, true
		args = objectOf(member{"previous_result", orNull(m.Mock.ReturnValue)})
	case passDirect(fr, m):
		identity, answered = skipped || m.Kind == ir.KindIdentity, skipped
		args = fr.input
	case skipped || m.Kind == ir.KindIdentity:
		identity, answered = true, skipped
		args = objectOf(member{"previous_result", prev.Producer()})
	default:
		var err error
		args, err = r.args(ctx, fr, i, m.Leaf.Inputs, site.Path, prev, prevID)
		if err != nil {
			if ctx.Err() != nil {
				return notLaunched()
			}
			// FX-14, JE-23: the job still launches, with empty arguments,
			// and fails without running.
			o.rec.Ran = true
			o.rec.RecordedArgs, o.rec.ResolvedArgs = jsonObj, jsonObj
			out := fail(transformError(err, fr.collapsed != nil), OriginEngine)
			r.recordJob(fr, i, out.rec.Result)
			return out
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
	if !answered {
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
	// The storage boundary: each argument becomes its stored text; one
	// that jsonb refuses, by a key or a value, fails the push, a chaining
	// error (ADR 0005 step 3, FX-48, FX-51).
	stored, err := storeMembers(args)
	if err != nil {
		return r.chaining(fr, i, o, err.Error())
	}
	// The resolution of $var: and $res: (JE-41) is the platform providers'
	// (PF2); until then the resolved arguments are the recorded ones.
	o.rec.RecordedArgs, o.rec.ResolvedArgs = stored, stored
	if answered {
		return r.identityJob(ctx, fr, i, site.Path, o, stored, skipped)
	}

	// Start step 10: the launch, where H1 may answer for the module.
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
	default:
		entry, _, err := Entrypoint(stored)
		if err != nil {
			// WR-12: the job fails before any wrapper runs.
			o.rec.Ran = true
			out := fail(engineError("ExecutionErr", err.Error()), OriginEngine)
			r.recordJob(fr, i, out.rec.Result)
			return out
		}
		// H3: the host may veto the run's first write-capable launch.
		if r.hooks.WriteCapable(site) {
			r.writeMu.Lock()
			if !r.wrote {
				r.wrote = true
				if err := r.hooks.BeforeFirstWrite(ctx, r, site); err != nil {
					r.Latch(HookLatchError(ctx, err))
				}
			}
			r.writeMu.Unlock()
			if r.latch.Err() != nil {
				return notLaunched()
			}
			r.mu.Lock()
			r.trace.MayHaveMutated = true
			r.mu.Unlock()
		}
		// Start step 9: the timeout, of a leaf only (FX-24).
		timeout, instanceWide := r.timeout(fr, m)
		o.rec.Ran = true
		out, job, err = r.invoke(ctx, fr, i, site, entry, stored, timeout, idx)
		switch {
		case errors.Is(err, errNotLaunched):
			o.rec.Ran = false
			return notLaunched()
		case errors.Is(err, CancelTimeout):
			o.rec.Abandoned = true
			// The worker's text for a step that ran out of time: WR-37's
			// for Bun, WR-44's for native.
			res := fail(TimeoutError(Boundary(m.Leaf.Boundary), fr.stepID(i), timeout, instanceWide), OriginEngine)
			r.recordJob(fr, i, res.rec.Result)
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
			return o
		case err != nil:
			res := fail(engineError("InternalErr", err.Error()), OriginEngine)
			r.recordJob(fr, i, res.rec.Result)
			return res
		}
	}
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
	v, err := r.predicate(ctx, s.Expr, site, r.stopScope(fr, s.Expr, own))
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
		FlowEnv:   r.env,
		Results:   r.results(fr),
	}
}

// transformScope is the scope of the input transforms of the module at
// cursor i of fr (FX-7, JE-22, JE-23): P as previous_result and no result,
// params always {}, the preceding id, results with JE-9, flow_input and
// flow_env. The failure module's also see started_at, the flow's start
// time as an RFC 3339 string, and flow_job_id, the run's id (FX-32, JE-35).
func (r *run) transformScope(fr *frame, i int, prev jsonval.Value, prevID string) *expr.Scope {
	s := &expr.Scope{
		PreviousID: prevID,
		Context: []expr.Named{
			{Name: "previous_result", Value: prev}, {Name: "params", Value: jsonObj},
			{Name: "resume", Value: jsonNull}, {Name: "resumes", Value: jsonEmpty}, {Name: "approvers", Value: jsonEmpty},
		},
		FlowInput: fr.input,
		FlowEnv:   r.env,
		Results:   r.results(fr),
	}
	if fr.isFailure(i) {
		s.Context = append(s.Context,
			expr.Named{Name: "started_at", Value: jsonval.FromProducer(jsonString(rfc3339(r.started)))},
			expr.Named{Name: "flow_job_id", Value: jsonval.FromProducer(jsonString(r.id))})
	}
	return s
}

// stopScope is the scope of stop_after_if and stop_after_all_iters_if
// (FX-8, JE-22, JE-27): own as result and previous_result, in its producer
// text (FX-42 item 3); an empty previous id; results only when the text
// holds results., results[ or results?.; flow_env only when it holds
// flow_env.
func (r *run) stopScope(fr *frame, x *expr.Expr, own jsonval.Value) *expr.Scope {
	s := &expr.Scope{
		Context:   []expr.Named{{Name: "previous_result", Value: own}, {Name: "result", Value: own}},
		FlowInput: fr.input,
	}
	t := x.Trimmed
	if strings.Contains(t, "results.") || strings.Contains(t, "results[") || strings.Contains(t, "results?.") {
		s.Results = r.results(fr)
	}
	if strings.Contains(t, "flow_env") {
		s.FlowEnv = r.env
	}
	return s
}

// evaluate evaluates one expression, timed as a span.
func (r *run) evaluate(ctx context.Context, x *expr.Expr, site conformance.SitePath, s *expr.Scope) (jsonval.Value, error) {
	start := time.Now()
	v, err := r.e.eval.Eval(ctx, x, s)
	r.span(site, SpanExpression, start)
	return v, err
}

// predicate evaluates a boolean site (JE-24). A value other than JSON true
// or false fails with Windmill's check.
func (r *run) predicate(ctx context.Context, x *expr.Expr, site conformance.SitePath, s *expr.Scope) (bool, error) {
	v, err := r.evaluate(ctx, x, site, s)
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
			v, err := r.evaluate(ctx, in.Transform.Expr, site, scope)
			if err != nil {
				return jsonval.Value{}, &transformFailure{text: in.Transform.Expr.Text, err: err}
			}
			ms = append(ms, member{in.Name, v.Producer()})
		}
	}
	return objectOf(ms...), nil
}

// timeout returns a leaf's timeout and whether it counts as instance-wide
// (FX-24, WR-37). The request is the module's static timeout (its loop's
// in a collapsed iteration, CF-8, CF-27), else a path script's own timeout
// metadata; one strictly below the instance maximum is used as it is, and
// counts as job-specific, zero included, which leaves the step no time at
// all. With no request, or one at or above the maximum, the maximum applies
// (windmill-lite has no instance default job timeout below it).
func (r *run) timeout(fr *frame, m *ir.Module) (time.Duration, bool) {
	var secs *int64
	t := m.Timeout
	if fr.collapsed != nil {
		t = fr.collapsed.Timeout
	}
	if t != nil {
		secs, _ = staticTimeout(t.Value)
	}
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

// errNotLaunched: a hook latched the run before the step launched.
var errNotLaunched = errors.New("engine: the run latched before the step launched")

// invoke runs the module at cursor i of fr in its harness, with its
// timeout as a timer in the engine (ADR 0004 §Cancellation and deadlines
// item 2), and returns its outcome and its job's id, WM_JOB_ID. The job
// identity follows the flow the job runs in (WA-6, WA-V1, ADR 0007): its
// job id, its innermost scope and its runnable path.
func (r *run) invoke(ctx context.Context, fr *frame, i int, site Site, entry string, args jsonval.Value, timeout time.Duration, idx int) (StepOutcome, string, error) {
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
		ModuleState: ModuleStateWarm,
	}
	// Job identity (ADR 0007, WA-4, WA-6, WA-V1): the flow the job runs in
	// is its parent (a child flow, or the loop's flow for a collapsed
	// iteration), with that flow's innermost scope and runnable path, under
	// the run as its effective root.
	inv.Env = platform.Identity{
		JobID:         inv.ID,
		FlowJobID:     in.id,
		RootFlowJobID: in.scope,
		RootJobID:     r.id,
		FlowPath:      in.path,
		StepID:        fr.stepID(i),
		JobPath:       r.jobPath(fr, i),
		Token:         platform.NewToken(),
	}.Env(r.e.host)
	if err := r.hooks.InvocationEnv(ctx, r, site, inv.Env); err != nil {
		r.Latch(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return StepOutcome{}, "", errNotLaunched
	}
	inv.Report = r.settle.Track(&runReporter{run: r, idx: idx})
	stepCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if timeout <= 0 {
		cancel(CancelTimeout)
	} else {
		t := time.AfterFunc(timeout, func() { cancel(CancelTimeout) })
		defer t.Stop()
	}
	start := time.Now()
	out, err := r.exec.Execute(stepCtx, inv)
	r.span(site.Path, SpanHarness, start)
	return out, inv.ID, err
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

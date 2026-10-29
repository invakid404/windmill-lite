package engine

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// run is one run of a flow: its state and its trace. One goroutine drives
// it; the Reporters of its invocations write late output into its trace
// from others, under mu.
type run struct {
	e     *Engine
	f     *ir.Flow
	id    string
	hooks Hooks
	exec  StepExecutor
	rng   *rand.Rand
	// input is the flow input, stored, with its top-level keys shuffled
	// (FX-42 item 1); env is the flow's flow_env, or zero (JE-38).
	input jsonval.Value
	env   jsonval.Value
	// sites are the site paths of the flow's modules (ADR 0011): m:<id>,
	// or m:<id>~<k> for an id that occurs more than once (OF-2), then the
	// failure module's slot, s:failure, at index len(f.Modules), the
	// cursor Windmill gives it (FX-1).
	sites  []conformance.SitePath
	latch  Latch
	settle *RunSettlement
	// wrote is set once a write-capable module has launched (H3).
	wrote bool
	// started is when the run started: the flow's start time, which a
	// failure module's input transforms see as started_at (JE-35).
	started time.Time

	mu    sync.Mutex // guards trace and the job results below
	trace Trace
	// jobs holds, per module (the failure module last), the stored result
	// of its job when it has one (JE-18 step 2); nodes is the node-id map,
	// written as each module's job finishes, the last write winning
	// (JE-19).
	jobs  []jsonval.Value
	nodes map[string]jsonval.Value
}

func newRun(e *Engine, f *ir.Flow, id string, hooks Hooks, exec StepExecutor, rng *rand.Rand) *run {
	ids := make([]string, len(f.Modules))
	for i, m := range f.Modules {
		ids[i] = m.ID
	}
	r := &run{e: e, f: f, id: id, hooks: hooks, exec: exec, rng: rng, started: time.Now(), jobs: make([]jsonval.Value, len(f.Modules)+1), nodes: map[string]jsonval.Value{}}
	for _, seg := range conformance.ModuleSegments(ids) {
		r.sites = append(r.sites, conformance.SitePath{}.Child(seg))
	}
	r.sites = append(r.sites, conformance.SitePath{}.Child(conformance.FailureSlot()))
	r.trace = Trace{RunID: id, Bundle: e.bundle, ModuleState: ModuleStateWarm}
	r.settle = NewRunSettlement(func() { hooks.OnRunSettled(r.snapshot()) })
	return r
}

func (r *run) ID() string      { return r.id }
func (r *run) Latch(err error) { r.latch.Set(err) }

// moduleAt is the module at cursor i: a top-level module, or the failure
// module at len(f.Modules) (FX-1).
func (r *run) moduleAt(i int) *ir.Module {
	if i == len(r.f.Modules) {
		return r.f.FailureModule
	}
	return r.f.Modules[i]
}

// isFailure reports whether cursor i is the failure module's.
func (r *run) isFailure(i int) bool { return i == len(r.f.Modules) }

// site is the module at cursor i as hooks see it.
func (r *run) site(i int) Site {
	m := r.moduleAt(i)
	s := Site{Path: r.sites[i], Flow: r.f.Path, Module: m.ID}
	if m.Leaf != nil {
		s.Script = r.jobPath(m.Leaf)
	}
	return s
}

// jobPath is the Windmill path of a leaf's job (WR-29, FX-45): a path
// script's own, or <flow path>/<module id> for an inline step.
func (r *run) jobPath(l *ir.Leaf) string {
	if s := r.e.prog.Scripts[l.Script]; s != nil {
		return s.Path
	}
	return l.Script
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
		t.Steps[i] = st
	}
	t.Warnings = append([]Warning(nil), r.trace.Warnings...)
	t.Deviations = append([]string(nil), r.trace.Deviations...)
	t.Spans = append([]Span(nil), r.trace.Spans...)
	return &t
}

// end records the run's outcome. A latched run records its latched error
// and no result, and a halted run no result either. The final result is
// stored as jsonb (FX-36, FX-42 item 5).
func (r *run) end(status conformance.RunStatus, result jsonval.Value, origin Origin) {
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
	r.trace.ResultOrigin = origin
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
		r.end(status, canceledError(cancellationOf(ctx)), OriginEngine)
		return
	}
	r.end(status, jsonval.Value{}, OriginNone)
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
	// error, a chaining error at the root's first pickup), carrying its
	// failure value (FX-36).
	endFailed
	// endErrorStop: an error stop (FX-19): the module is Failure, carrying
	// an EarlyStopError, while its job succeeded (FX-V2).
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
	// 1): the flow fails at once with the InternalError, carried.
	endChained
	// endInterrupted: the module did not complete because the run was
	// latched or canceled on its way; the completion check ends the run.
	endInterrupted
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
	// status, result and origin are the flow's ending, for nextEnd.
	status conformance.RunStatus
	result jsonval.Value
	origin Origin
}

// decide is Windmill's continue decision for module m (FX-2 completion
// steps 3, 7 and 8) in a flow with no retry (N2): continue_on_error
// (FX-30), the error stops (FX-19, FX-V4), the unrecoverable failure of a
// throwing stop_after_if (FX-21, FX-44) and the failure module (FX-31).
// last says that m is the flow's last module.
func (r *run) decide(m *ir.Module, o outcome, last bool) decision {
	hasFailure := r.f.FailureModule != nil
	ends := func(status conformance.RunStatus) decision {
		return decision{next: nextEnd, status: status, result: o.carried, origin: o.origin}
	}
	// A failure is tolerated by continue_on_error unless it is
	// unrecoverable (FX-30, FX-44); the cursor then advances.
	tolerated := m.ContinueOnError && o.end != endUnrecoverable
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

// run runs the flow's modules in order (FX-2): the start checks of the
// host (Start), the module's own start and completion, then the host's
// completion checks (Completion) and Windmill's continue decision, then
// haltAfter (AfterContinue). A failure the decision does not tolerate
// enters the failure module (failure).
func (r *run) run(ctx context.Context) {
	// FX-6, JE-36: the first module's previous_result is the flow input;
	// FX-3: a flow with no modules returns it.
	prev, prevID := r.input, ""
	// continues is set when the start that follows is what the continue
	// decision asked for after an error stop that coincided with the
	// cancellation, which Completion let through (FX-38).
	continues := false
	for i, m := range r.f.Modules {
		site := r.site(i)
		v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, ContinuesErrorStop: continues, HaltBefore: r.hooks.HaltBefore(site)})
		if st, end := v.Status(); end {
			r.endBy(ctx, st)
			return
		}
		o, idx, ok := r.complete(ctx, i, prev, prevID)
		if !ok {
			return
		}
		d := r.decide(m, o, i == len(r.f.Modules)-1)
		canceled := ctx.Err() != nil
		if AfterContinue(ContinueSignals{RunContinues: d.next != nextEnd, Final: true, ToFailureModule: d.next == nextFailure, Canceled: canceled, HaltAfter: r.hooks.HaltAfter(site)}) != Proceed {
			if canceled {
				r.endBy(ctx, conformance.RunCanceled)
				return
			}
			r.mark(idx, "H8")
			r.endBy(ctx, conformance.RunHalted)
			return
		}
		continues = canceled
		switch d.next {
		case nextEnd:
			r.end(d.status, d.result, d.origin)
			return
		case nextFailure:
			// Start step 6: previous_result stays the failing value; the
			// preceding id is the failed module's, or empty when the
			// cursor reached len (FX-V4).
			id := m.ID
			if d.direct {
				id = ""
			}
			r.failure(ctx, o.carried, id, continues)
			return
		}
		prev, prevID = o.carried, m.ID
	}
	r.end(conformance.RunSuccess, prev, OriginNone)
}

// failure runs the failure module (FX-31) with the failing value as its
// previous_result (FX-32), and ends the flow with its result (FX-33): the
// flow succeeds only when the failure module's job succeeded and its result
// recovers; otherwise it fails with that result, or with the failure
// module's own failure value. The failure module has no stop and ignores
// its own continue_on_error (FX-19, FX-30), and a chaining error while it
// starts is a later transition's (FX-15 context 1).
func (r *run) failure(ctx context.Context, prev jsonval.Value, prevID string, continues bool) {
	i := len(r.f.Modules)
	v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, ContinuesErrorStop: continues, FailureModule: true, HaltBefore: r.hooks.HaltBefore(r.site(i))})
	if st, end := v.Status(); end {
		r.endBy(ctx, st)
		return
	}
	o, _, ok := r.complete(ctx, i, prev, prevID)
	if !ok {
		return
	}
	switch {
	case o.end == endSucceeded && recovers(o.carried):
		r.end(conformance.RunSuccess, o.carried, o.origin)
	case o.end == endSucceeded, o.end == endFailed, o.end == endChained:
		r.end(conformance.RunFailure, o.carried, o.origin)
	default:
		r.end(conformance.RunFailure, engineError("InternalErr", "windmill-lite: the failure module was interrupted with no latch or cancellation"), OriginEngine)
	}
}

// complete runs the module at cursor i, from its slot in the trace to the
// host's completion checks (FX-2 completion step 7's precondition): the
// module's own start and completion, OnModuleEnd (H6), then Completion,
// which may latch a fatal error name (H7). It returns the outcome and the
// module's record, and false when the checks ended the run.
func (r *run) complete(ctx context.Context, i int, prev jsonval.Value, prevID string) (outcome, int, bool) {
	m, site := r.moduleAt(i), r.site(i)
	// The module's slot in the trace is reserved first, so late output of
	// its invocation always has a record to go to.
	r.mu.Lock()
	idx := len(r.trace.Steps)
	r.trace.Steps = append(r.trace.Steps, StepRecord{Site: site.Path})
	r.mu.Unlock()
	start := time.Now()
	o := r.module(ctx, i, m, prev, prevID, idx)
	o.rec.Start, o.rec.Duration = start, time.Since(start)
	r.span(site.Path, SpanModule, start)
	r.setRecord(idx, o.rec)
	if err := r.hooks.OnModuleEnd(ctx, r, site, o.rec); err != nil {
		r.latch.Set(HookLatchError(ctx, err))
	}
	fatal := o.end.failed() && r.hooks.FatalErrorName(errorName(o.carried))
	v := Completion(CompletionSignals{
		Latched:  r.latch.Err() != nil,
		Canceled: ctx.Err() != nil,
		// Windmill's continue decision checks a stop before the cancel
		// flag, so an error stop in a flow with a failure module goes on
		// under cancellation (FX-38).
		ErrorStopWithFailureModule: o.end == endErrorStop && r.f.FailureModule != nil,
		FatalName:                  fatal,
	})
	if v == EndLatched && r.latch.Err() == nil {
		// H7: the outcome's error latches the run, which bypasses what the
		// flow would have done with the failure.
		r.latch.Set(&FatalError{Site: site.Path, Name: errorName(o.carried), Value: o.carried})
		r.mark(idx, "H7")
		r.mu.Lock()
		r.trace.Deviations = append(r.trace.Deviations, "H7")
		r.mu.Unlock()
	}
	if st, end := v.Status(); end {
		r.endBy(ctx, st)
		return o, idx, false
	}
	return o, idx, true
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

// module runs the start phase of the module at cursor i (FX-2 start steps
// 7 to 10) and the first step of its completion phase, stop_after_if
// (FX-19), which the failure module does not have. prev is its
// previous_result and prevID its preceding id; idx is its record in the
// trace.
func (r *run) module(ctx context.Context, i int, m *ir.Module, prev jsonval.Value, prevID string, idx int) outcome {
	site := r.site(i)
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
		r.latch.Set(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return notLaunched()
	}

	// Start step 7: skip_if, with P as result and previous_result (FX-17,
	// JE-25). A throw, or a value that is not a boolean, is a chaining
	// error (FX-15).
	skipped := false
	if m.SkipIf != nil {
		v, err := r.predicate(ctx, m.SkipIf, site.Path, r.startScope(prev, prevID))
		if err != nil {
			if ctx.Err() != nil {
				return notLaunched()
			}
			return r.chaining(i, o, predicateText(err))
		}
		skipped = v
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
	case m.PassFlowInputDirectly:
		identity, answered = skipped || m.Kind == ir.KindIdentity, skipped
		args = r.input
	case skipped || m.Kind == ir.KindIdentity:
		identity, answered = true, skipped
		args = objectOf(member{"previous_result", prev.Producer()})
	default:
		var err error
		args, err = r.args(ctx, i, m.Leaf.Inputs, site.Path, prev, prevID)
		if err != nil {
			if ctx.Err() != nil {
				return notLaunched()
			}
			// FX-14, JE-23: the job still launches, with empty arguments,
			// and fails without running.
			o.rec.Ran = true
			o.rec.RecordedArgs, o.rec.ResolvedArgs = jsonObj, jsonObj
			out := fail(transformError(err), OriginEngine)
			r.recordJob(i, out.rec.Result)
			return out
		}
	}
	if identity {
		// FX-18: the identity job of a module with any skip_if is
		// classified skipped, whatever the predicate answered, so its
		// stop_after_if is not evaluated (FX-17). The failure module is
		// never flagged skipped, even when its skip_if replaced it with a
		// passthrough (FX-17, FX-32).
		skipped = m.SkipIf != nil && !r.isFailure(i)
	}
	if !answered {
		// H2 rewrites the arguments of a module that launches its own job;
		// the trace keeps both.
		rewritten, err := r.hooks.RewriteArgs(ctx, r, site, args)
		if err != nil {
			r.latch.Set(HookLatchError(ctx, err))
		} else if !isObject(rewritten) {
			r.latch.Set(errors.New("engine: H2 returned arguments that are not a JSON object"))
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
		return r.chaining(i, o, err.Error())
	}
	// The resolution of $var: and $res: (JE-41) is the platform providers'
	// (PF2); until then the resolved arguments are the recorded ones.
	o.rec.RecordedArgs, o.rec.ResolvedArgs = stored, stored
	if answered {
		return r.identityJob(ctx, i, m, site.Path, o, stored, skipped)
	}

	// Start step 10: the launch, where H1 may answer for the module.
	out, intercepted, err := r.hooks.Intercept(ctx, r, site, stored)
	if err != nil {
		r.latch.Set(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return notLaunched()
	}
	var job string // the id of the leaf's job, once it ran
	switch {
	case intercepted:
		o.rec.Hooks = append(o.rec.Hooks, "H1")
	case identity:
		return r.identityJob(ctx, i, m, site.Path, o, stored, skipped)
	default:
		entry, _, err := Entrypoint(stored)
		if err != nil {
			// WR-12: the job fails before any wrapper runs.
			o.rec.Ran = true
			out := fail(engineError("ExecutionErr", err.Error()), OriginEngine)
			r.recordJob(i, out.rec.Result)
			return out
		}
		// H3: the host may veto the run's first write-capable launch.
		if r.hooks.WriteCapable(site) {
			if !r.wrote {
				r.wrote = true
				if err := r.hooks.BeforeFirstWrite(ctx, r, site); err != nil {
					r.latch.Set(HookLatchError(ctx, err))
				}
				if r.latch.Err() != nil {
					return notLaunched()
				}
			}
			r.mu.Lock()
			r.trace.MayHaveMutated = true
			r.mu.Unlock()
		}
		// Start step 9: the timeout, of a leaf only (FX-24).
		timeout, instanceWide := r.timeout(m)
		o.rec.Ran = true
		out, job, err = r.invoke(ctx, m, site, entry, stored, timeout, idx)
		switch {
		case errors.Is(err, errNotLaunched):
			o.rec.Ran = false
			return notLaunched()
		case errors.Is(err, CancelTimeout):
			o.rec.Abandoned = true
			limit := "job-specific duration limit"
			if instanceWide {
				limit = "instance-wide job duration limit"
			}
			// WR-37: the worker's text for a step that ran out of time.
			res := fail(workerError(m.ID, "execution error:\njob process terminated due to timeout after exceeding "+limit), OriginEngine)
			r.recordJob(i, res.rec.Result)
			return res
		case err != nil && ctx.Err() != nil:
			// The run was canceled (H4): the step's job is canceled, with
			// the worker's text for a killed job (FX-38), and the
			// completion check ends the run.
			o.rec.Abandoned = true
			reason, canceler := cancellationOf(ctx)
			v := killedError(m.ID, reason, canceler)
			o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried, o.rec.ResultOrigin = conformance.StepCanceled, conformance.ModuleFailure, v, v, OriginEngine
			o.end, o.carried, o.origin = endInterrupted, v, OriginEngine
			r.recordJob(i, v)
			return o
		case err != nil:
			res := fail(engineError("InternalErr", err.Error()), OriginEngine)
			r.recordJob(i, res.rec.Result)
			return res
		}
	}
	st := StoreOutcome(out, m.ID)
	if _, err := st.Value.Stored(); err != nil && job != "" {
		// FX-48 case 1, OR-27: the completion insert refuses a result, or
		// a failure value, that jsonb rejects, and the job fails with the
		// storage error, with no step_id, which the flow handles as any
		// failure. Windmill retries the insert for about 30 s first, a
		// delay windmill-lite does not reproduce (OR-27: out of scope).
		st = Stored{Failed: true, Value: engineError("InternalErr", "Internal: Could not add completed job "+job+": "+databaseError(err).Error()), Origin: OriginEngine}
	}
	r.recordJob(i, st.Value)
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
	return r.stop(ctx, m, site.Path, o)
}

// identityJob runs module i's identity job (FX-16), which the worker runs
// at once, with no timer (FX-24): its result is its stored arguments'
// previous_result, or {}. A job classified skipped does not evaluate the
// module's stop_after_if (FX-17, FX-18).
func (r *run) identityJob(ctx context.Context, i int, m *ir.Module, site conformance.SitePath, o outcome, stored jsonval.Value, skipped bool) outcome {
	res := identityResult(stored)
	o.rec.Ran, o.rec.Skipped = true, skipped
	o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried = conformance.StepSuccess, conformance.ModuleSuccess, res, res
	o.carried = res
	r.recordJob(i, res)
	if skipped {
		return o
	}
	return r.stop(ctx, m, site, o)
}

// stop is completion step 1 for a module whose job succeeded: its
// stop_after_if, with the module's own result as result and
// previous_result (FX-19, JE-26). o is the module's successful outcome.
// The failure module has no stop (FX-2 completion step 1).
func (r *run) stop(ctx context.Context, m *ir.Module, site conformance.SitePath, o outcome) outcome {
	s := m.StopAfterIf
	if s == nil || m == r.f.FailureModule {
		return o
	}
	own := o.carried
	v, err := r.predicate(ctx, s.Expr, site, r.stopScope(s.Expr, own))
	switch {
	case err != nil && ctx.Err() != nil:
		o.end = endInterrupted
		return o
	case err != nil:
		// FX-21: the completion is redone as an unrecoverable failure whose
		// error is the predicate's text, a string; the job's stored result
		// stays the step's success (FX-V2).
		o.rec.Module, o.end = conformance.ModuleFailure, endUnrecoverable
		o.carried, o.origin = objectOf(member{"error", jsonString(predicateText(err))}), OriginEngine
		o.rec.Carried, o.rec.CarriedOrigin = o.carried, OriginEngine
		return o
	case !v:
		return o
	case s.SkipIfStopped:
		// A clean stop that marks the module and the flow skipped;
		// skip_if_stopped wins over error_message.
		o.rec.Skipped, o.end = true, endStoppedSkipped
		return o
	case s.ErrorMessage == nil:
		o.end = endStopped
		return o
	}
	// An error stop: the module fails, carrying an EarlyStopError, while
	// its job stays successful (FX-V2).
	msg := *s.ErrorMessage
	if msg == "" {
		msg = "stop after if: " + s.Expr.Text
	}
	e := []member{{"name", jsonString("EarlyStopError")}, {"message", jsonString(msg)}}
	if s.ErrorIncludeResult != nil && *s.ErrorIncludeResult {
		e = append(e, member{"result", own.Producer()})
	}
	o.carried, o.origin = objectOf(member{"error", objectOf(e...).Producer()}), OriginEngine
	o.rec.Module, o.rec.Carried, o.rec.CarriedOrigin, o.end = conformance.ModuleFailure, o.carried, OriginEngine, endErrorStop
	return o
}

// chaining is a chaining error raised while the module at cursor i starts
// (FX-15), whose display text is text (without Windmill's Rust location,
// D14). At the root's first pickup, the first module's start, it is a
// failure of that module with no job, named Anyhow (context 2), which
// continue_on_error and the failure module then handle. At a later
// transition, the failure module's start included, the flow fails at once
// with it, named InternalError, and the module never starts (context 1).
func (r *run) chaining(i int, o outcome, text string) outcome {
	if i == 0 {
		v := engineError("Anyhow", "Error: "+text)
		o.rec.Status, o.rec.Module, o.rec.Carried, o.rec.CarriedOrigin = conformance.StepFailure, conformance.ModuleFailure, v, OriginEngine
		o.end, o.carried, o.origin = endFailed, v, OriginEngine
		return o
	}
	o.rec.Status, o.rec.Module = conformance.StepNotRun, conformance.ModuleNotRun
	o.end, o.carried, o.origin = endChained, engineError("InternalError", text), OriginEngine
	return o
}

// recordJob records the stored result of the job of the module at cursor
// i, which results lookups read (JE-18, JE-19).
func (r *run) recordJob(i int, v jsonval.Value) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[i] = v
	r.nodes[r.moduleAt(i).ID] = v
}

// Lookup is a results read on the engine path (JE-18 steps 2 and 3): the
// first module of the flow with that id, when it has a job, gives its
// job's result; otherwise the node-id map does (JE-19); anything else is
// null. Which of two modules with one id step 2 finds (OF-2) is a reading:
// the first in the flow. The value is read from storage, so the evaluator
// binds its stored text.
func (r *run) Lookup(_ context.Context, id string) (jsonval.Value, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, m := range r.f.Modules {
		if m.ID == id {
			if v := r.jobs[i]; !v.IsZero() {
				return v, nil
			}
			break
		}
	}
	return r.nodes[id], nil
}

// Stored is the result-by-node-id lookup of JE-9's direct path (JE-19):
// the job of the module with that id that finished last.
func (r *run) Stored(_ context.Context, id string) (jsonval.Value, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.nodes[id]
	return v, ok
}

// The names every start-phase site binds besides previous_result (FX-7,
// JE-22): a preceding suspend's resume, resumes and approvers, which are
// null, [] and [] without one.
var (
	jsonNull  = jsonval.FromProducer([]byte("null"))
	jsonEmpty = jsonval.FromProducer([]byte("[]"))
	jsonObj   = jsonval.FromProducer([]byte("{}"))
)

// startScope is the scope of skip_if (JE-22): P as result and
// previous_result, the previous id, results with JE-9, flow_input and
// flow_env.
func (r *run) startScope(prev jsonval.Value, prevID string) *expr.Scope {
	return &expr.Scope{
		PreviousID: prevID,
		Context: []expr.Named{
			{Name: "previous_result", Value: prev}, {Name: "result", Value: prev},
			{Name: "resume", Value: jsonNull}, {Name: "resumes", Value: jsonEmpty}, {Name: "approvers", Value: jsonEmpty},
		},
		FlowInput: r.input,
		FlowEnv:   r.env,
		Results:   r,
	}
}

// transformScope is the scope of the input transforms of the module at
// cursor i (FX-7, JE-22, JE-23): P as previous_result and no result, params
// always {}, the preceding id, results with JE-9, flow_input and flow_env.
// The failure module's also see started_at, the flow's start time as an
// RFC 3339 string, and flow_job_id, the run's id (FX-32, JE-35).
func (r *run) transformScope(i int, prev jsonval.Value, prevID string) *expr.Scope {
	s := &expr.Scope{
		PreviousID: prevID,
		Context: []expr.Named{
			{Name: "previous_result", Value: prev}, {Name: "params", Value: jsonObj},
			{Name: "resume", Value: jsonNull}, {Name: "resumes", Value: jsonEmpty}, {Name: "approvers", Value: jsonEmpty},
		},
		FlowInput: r.input,
		FlowEnv:   r.env,
		Results:   r,
	}
	if r.isFailure(i) {
		s.Context = append(s.Context,
			expr.Named{Name: "started_at", Value: jsonval.FromProducer(jsonString(rfc3339(r.started)))},
			expr.Named{Name: "flow_job_id", Value: jsonval.FromProducer(jsonString(r.id))})
	}
	return s
}

// stopScope is the scope of stop_after_if (FX-8, JE-22): the module's own
// result as result and previous_result, in its producer text (FX-42 item
// 3); an empty previous id; results only when the text holds results.,
// results[ or results?.; flow_env only when it holds flow_env.
func (r *run) stopScope(x *expr.Expr, own jsonval.Value) *expr.Scope {
	s := &expr.Scope{
		Context:   []expr.Named{{Name: "previous_result", Value: own}, {Name: "result", Value: own}},
		FlowInput: r.input,
	}
	t := x.Trimmed
	if strings.Contains(t, "results.") || strings.Contains(t, "results[") || strings.Contains(t, "results?.") {
		s.Results = r
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

// args evaluates the input transforms of the module at cursor i into its
// argument object (FX-7, JE-23): one at a time, in an order the run's seed
// shuffles (a hash map's on Windmill, JE-V5), stopping at the first error;
// the object's top-level keys come in that order too (ADR 0005 step 2). A
// static value is copied verbatim (JE-40), an ai input left out.
func (r *run) args(ctx context.Context, i int, ins []ir.Input, site conformance.SitePath, prev jsonval.Value, prevID string) (jsonval.Value, error) {
	var ms []member
	var scope *expr.Scope
	for _, k := range r.rng.Perm(len(ins)) {
		in := ins[k]
		switch in.Transform.Type {
		case ir.Static:
			ms = append(ms, member{in.Name, orNull(in.Transform.Value)})
		case ir.Javascript:
			if scope == nil {
				scope = r.transformScope(i, prev, prevID)
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
// (FX-24, WR-37). The request is the module's static timeout, else a path
// script's own timeout metadata; one strictly below the instance maximum
// is used as it is, and counts as job-specific, zero included, which
// leaves the step no time at all. With no request, or one at or above the
// maximum, the maximum applies (windmill-lite has no instance default job
// timeout below it).
func (r *run) timeout(m *ir.Module) (time.Duration, bool) {
	var secs *int64
	if m.Timeout != nil {
		secs, _ = staticTimeout(m.Timeout.Value)
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

// invoke runs the step in its harness, with its timeout as a timer in the
// engine (ADR 0004 §Cancellation and deadlines item 2), and returns its
// outcome and its job's id, WM_JOB_ID.
func (r *run) invoke(ctx context.Context, m *ir.Module, site Site, entry string, args jsonval.Value, timeout time.Duration, idx int) (StepOutcome, string, error) {
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
	inv.Env = map[string]string{
		"WM_WORKSPACE":        r.e.workspace,
		"WM_JOB_ID":           inv.ID,
		"WM_FLOW_JOB_ID":      r.id,
		"WM_ROOT_FLOW_JOB_ID": r.id,
		"WM_ROOT_JOB_ID":      r.id,
		"WM_FLOW_PATH":        r.f.Path,
		"WM_FLOW_STEP_ID":     m.ID,
		"WM_JOB_PATH":         site.Script,
	}
	if err := r.hooks.InvocationEnv(ctx, r, site, inv.Env); err != nil {
		r.latch.Set(HookLatchError(ctx, err))
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

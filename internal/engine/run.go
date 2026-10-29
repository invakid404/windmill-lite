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
	// root is the frame of the flow's own modules; its input is the flow
	// input, stored, with its top-level keys shuffled (FX-42 item 1). env
	// is the flow's flow_env, or zero (JE-38), which its child flows
	// inherit (CF-1).
	root   *frame
	env    jsonval.Value
	latch  Latch
	settle *RunSettlement
	// halted is set once a module's completion ended the run halted (H8),
	// so that the iterations of a parallel loop start nothing more.
	halted atomic.Bool
	// wrote is set once a write-capable module has launched (H3); writeMu
	// holds the iterations of a parallel loop back while H3 decides.
	writeMu sync.Mutex
	wrote   bool

	mu    sync.Mutex // guards trace and the frames' job results
	trace Trace
}

func newRun(e *Engine, f *ir.Flow, id string, hooks Hooks, exec StepExecutor, rng *rand.Rand) *run {
	r := &run{e: e, f: f, id: id, hooks: hooks, exec: exec}
	r.root = newFrame(f.Modules, conformance.SitePath{})
	r.root.path, r.root.id, r.root.scope, r.root.nodes, r.root.rng = f.Path, id, id, newNodeScope(nil), rng
	r.trace = Trace{RunID: id, Bundle: e.bundle, ModuleState: ModuleStateWarm}
	r.settle = NewRunSettlement(func() { hooks.OnRunSettled(r.snapshot()) })
	return r
}

func (r *run) ID() string      { return r.id }
func (r *run) Latch(err error) { r.latch.Set(err) }

// site is module i of fr as hooks see it. Container bodies belong to the
// enclosing flow, so Flow is the run's flow, and Script is the leaf's
// static path, <flow path>/<module id> for an inline script.
func (r *run) site(fr *frame, i int) Site {
	m := fr.modules[i]
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
		t.Steps[i] = st
	}
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
// and no result; a canceled or halted run no result either. The final
// result is stored as jsonb (FX-36, FX-42 item 5).
func (r *run) end(status conformance.RunStatus, result jsonval.Value, origin Origin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Status = status
	switch status {
	case conformance.RunLatched:
		r.trace.Latched = r.latch.Err()
		return
	case conformance.RunCanceled, conformance.RunHalted:
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
	// error, an error stop, a throwing stop_after_if, a chaining error at
	// the root's first pickup, a failed loop). With no failure module,
	// retry or continue_on_error to take it (N1b, N2), the flow fails with
	// its carried value (FX-36).
	endFailed
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
	// (H8).
	endHalted
)

// outcome is how one module went.
type outcome struct {
	rec StepRecord
	end moduleEnd
	// carried is what the module carries on (FX-V2): the next module's
	// previous_result, or the flow's result; origin is who produced it
	// when it is an error.
	carried jsonval.Value
	origin  Origin
	// errorStop marks an error stop (FX-19): a failure that also stops the
	// flow it is in, which a sequential loop takes as a break (CF-12).
	errorStop bool
	// chain is a chaining error's text, which a collapsed iteration hands
	// to its loop to route (FX-15).
	chain string
}

// flowEnd is how a frame's module list ended: the flow's status and
// result, whether a stop ended it (CF-12), and errorStop for a failure
// that was an error stop.
type flowEnd struct {
	status    conformance.RunStatus
	result    jsonval.Value
	origin    Origin
	stopped   bool
	errorStop bool
	// chain is set when a collapsed iteration's push failed (outcome.chain).
	chain string
}

// run runs the flow and records its outcome.
func (r *run) run(ctx context.Context) {
	e := r.runList(ctx, r.root)
	r.end(e.status, e.result, e.origin)
}

// runList runs a frame's modules in order (FX-2): the start checks of the
// host (Start), the module's own start and completion, then the host's
// completion checks (Completion) and Windmill's continue decision, then
// haltAfter (AfterContinue).
func (r *run) runList(ctx context.Context, fr *frame) flowEnd {
	// FX-6, JE-36, CF-9: the first module's previous_result is the flow's
	// input; FX-3: a flow with no modules returns it.
	prev, prevID := fr.prev, ""
	if prev.IsZero() {
		prev = fr.input
	}
	result, origin := fr.input, OriginNone
	for i, m := range fr.modules {
		site := r.site(fr, i)
		if r.halted.Load() {
			return flowEnd{status: conformance.RunHalted}
		}
		v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, HaltBefore: r.hooks.HaltBefore(site)})
		if st, end := v.Status(); end {
			return flowEnd{status: st}
		}
		// The module's slot in the trace is reserved first, so late output
		// of its invocation always has a record to go to.
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
			r.latch.Set(HookLatchError(ctx, err))
		}
		if o.end == endHalted {
			return flowEnd{status: conformance.RunHalted}
		}
		fatal := o.end == endFailed && r.hooks.FatalErrorName(errorName(o.carried))
		v = Completion(CompletionSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, FatalName: fatal})
		if v == EndLatched && r.latch.Err() == nil {
			// H7: the outcome's error latches the run, which bypasses what
			// the flow would have done with the failure.
			r.latch.Set(&FatalError{Site: site.Path, Name: errorName(o.carried), Value: o.carried})
			r.mark(idx, "H7")
			r.mu.Lock()
			r.trace.Deviations = append(r.trace.Deviations, "H7")
			r.mu.Unlock()
		}
		if st, end := v.Status(); end {
			return flowEnd{status: st}
		}
		// Windmill's continue decision (FX-2 completion step 7), for a flow
		// with no failure module and no tolerated failure (N1b) or retry
		// (N2).
		switch o.end {
		case endFailed, endChained:
			return flowEnd{status: conformance.RunFailure, result: o.carried, origin: o.origin, stopped: o.errorStop, errorStop: o.errorStop, chain: o.chain}
		case endStopped:
			return flowEnd{status: conformance.RunSuccess, result: o.carried, origin: o.origin, stopped: true}
		case endStoppedSkipped:
			return flowEnd{status: conformance.RunSkipped, result: o.carried, origin: o.origin, stopped: true}
		case endInterrupted:
			// Only a latch or the cancellation interrupts a module, and the
			// completion check above ends the run for both.
			return flowEnd{status: conformance.RunFailure, result: engineError("InternalErr", "windmill-lite: a module was interrupted with no latch or cancellation"), origin: OriginEngine}
		}
		continues := i < len(fr.modules)-1 || fr.tail
		if AfterContinue(ContinueSignals{RunContinues: continues, Final: true, HaltAfter: r.hooks.HaltAfter(site)}) == EndHalted {
			r.mark(idx, "H8")
			r.halted.Store(true)
			return flowEnd{status: conformance.RunHalted}
		}
		prev, prevID = o.carried, m.ID
		result, origin = o.carried, o.origin
	}
	return flowEnd{status: conformance.RunSuccess, result: result, origin: origin}
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

// module runs the start phase of module i of fr (FX-2 start steps 7 to
// 10) and the first step of its completion phase, stop_after_if (FX-19).
// prev is its previous_result and prevID the id of the module before it;
// idx is its record in the trace.
func (r *run) module(ctx context.Context, fr *frame, i int, prev jsonval.Value, prevID string, idx int) outcome {
	m := fr.modules[i]
	site := r.site(fr, i)
	o := outcome{rec: StepRecord{Site: site.Path}}
	// H5: a hook that latches the run on the way to the launch stops the
	// module before it runs; the completion check ends the run latched.
	notLaunched := func() outcome {
		o.rec.Status, o.rec.Module, o.end = conformance.StepNotRun, conformance.ModuleNotRun, endInterrupted
		return o
	}
	fail := func(result jsonval.Value, origin Origin) outcome {
		o.rec.Status, o.rec.Module, o.rec.Result, o.rec.ResultOrigin = conformance.StepFailure, conformance.ModuleFailure, result, origin
		o.rec.Carried, o.rec.CarriedOrigin = result, origin
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
		args, err = r.args(ctx, fr, m.Leaf.Inputs, site.Path, prev, prevID)
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
		// stop_after_if is not evaluated (FX-17).
		skipped = m.SkipIf != nil
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
		r.latch.Set(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return notLaunched()
	}
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
					r.latch.Set(HookLatchError(ctx, err))
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
		out, err = r.invoke(ctx, fr, i, site, entry, stored, timeout, idx)
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
			res := fail(workerError(fr.stepID(i), "execution error:\njob process terminated due to timeout after exceeding "+limit), OriginEngine)
			r.recordJob(fr, i, res.rec.Result)
			return res
		case err != nil && ctx.Err() != nil:
			// The run was canceled (H4); the completion check ends it.
			o.rec.Abandoned = true
			o.rec.Status, o.rec.Module, o.end = conformance.StepCanceled, conformance.ModuleFailure, endInterrupted
			return o
		case err != nil:
			res := fail(engineError("InternalErr", err.Error()), OriginEngine)
			r.recordJob(fr, i, res.rec.Result)
			return res
		}
	}
	st := StoreOutcome(out, fr.stepID(i))
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
	return r.stop(ctx, fr, m, site.Path, o)
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

// identityJob runs module i's identity job (FX-16), which the worker runs
// at once, with no timer (FX-24): its result is its stored arguments'
// previous_result, or {}. A job classified skipped does not evaluate the
// module's stop_after_if (FX-17, FX-18).
func (r *run) identityJob(ctx context.Context, fr *frame, i int, site conformance.SitePath, o outcome, stored jsonval.Value, skipped bool) outcome {
	res := identityResult(stored)
	o.rec.Ran, o.rec.Skipped = true, skipped
	o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried = conformance.StepSuccess, conformance.ModuleSuccess, res, res
	o.carried = res
	r.recordJob(fr, i, res)
	if skipped {
		return o
	}
	return r.stop(ctx, fr, fr.modules[i], site, o)
}

// stop is completion step 1 for a module whose job succeeded: its
// stop_after_if, with the module's own result as result and
// previous_result (FX-19, JE-26). o is the module's successful outcome.
func (r *run) stop(ctx context.Context, fr *frame, m *ir.Module, site conformance.SitePath, o outcome) outcome {
	s := m.StopAfterIf
	if s == nil {
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
	o.rec.Module, o.end = conformance.ModuleFailure, endFailed
	o.carried, o.origin = objectOf(member{"error", jsonString(predicateText(err))}), OriginEngine
	o.rec.Carried, o.rec.CarriedOrigin = o.carried, o.origin
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
// with own as its result when s asks for it (FX-19, FX-V2).
func errorStop(o outcome, msg string, s *ir.Stop, own jsonval.Value) outcome {
	e := []member{{"name", jsonString("EarlyStopError")}, {"message", jsonString(msg)}}
	if s.ErrorIncludeResult != nil && *s.ErrorIncludeResult {
		e = append(e, member{"result", own.Producer()})
	}
	o.carried, o.origin, o.errorStop = objectOf(member{"error", objectOf(e...).Producer()}), OriginEngine, true
	o.rec.Module, o.rec.Carried, o.rec.CarriedOrigin, o.end = conformance.ModuleFailure, o.carried, o.origin, endFailed
	return o
}

// chaining is a chaining error raised while module i of fr starts (FX-15),
// whose display text is text (without Windmill's Rust location, D14). The
// start of a flow's first module is its pickup; the push of a collapsed
// iteration's job is routed again by its loop (sequentialLoop).
func (r *run) chaining(fr *frame, i int, o outcome, text string) outcome {
	return r.chainingAt(fr, i == 0 && fr.collapsed == nil, o, text)
}

// chainingAt routes a chaining error of fr's flow, raised at its pickup or
// not (FX-15, CF-23):
//
//   - at the root's pickup it is a failure of the module, named Anyhow
//     (context 2);
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

// transformScope is the scope of a step's input transforms (FX-7, JE-22,
// JE-23): P as previous_result and no result, params always {}, the
// previous id, results with JE-9, flow_input and flow_env.
func (r *run) transformScope(fr *frame, prev jsonval.Value, prevID string) *expr.Scope {
	return &expr.Scope{
		PreviousID: prevID,
		Context: []expr.Named{
			{Name: "previous_result", Value: prev}, {Name: "params", Value: jsonObj},
			{Name: "resume", Value: jsonNull}, {Name: "resumes", Value: jsonEmpty}, {Name: "approvers", Value: jsonEmpty},
		},
		FlowInput: fr.input,
		FlowEnv:   r.env,
		Results:   r.results(fr),
	}
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

// args evaluates a step's input transforms into its argument object
// (FX-7, JE-23): one at a time, in an order the frame's rng shuffles (a
// hash map's on Windmill, JE-V5), stopping at the first error; the object's
// top-level keys come in that order too (ADR 0005 step 2). A static value
// is copied verbatim (JE-40), an ai input left out.
func (r *run) args(ctx context.Context, fr *frame, ins []ir.Input, site conformance.SitePath, prev jsonval.Value, prevID string) (jsonval.Value, error) {
	var ms []member
	var scope *expr.Scope
	for _, k := range fr.rng.Perm(len(ins)) {
		in := ins[k]
		switch in.Transform.Type {
		case ir.Static:
			ms = append(ms, member{in.Name, orNull(in.Transform.Value)})
		case ir.Javascript:
			if scope == nil {
				scope = r.transformScope(fr, prev, prevID)
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

// invoke runs module i of fr in its harness, with its timeout as a timer in
// the engine (ADR 0004 §Cancellation and deadlines item 2). The job
// identity follows the flow the job runs in (WA-6, WA-V1, ADR 0007): its
// job id, its innermost scope and its runnable path.
func (r *run) invoke(ctx context.Context, fr *frame, i int, site Site, entry string, args jsonval.Value, timeout time.Duration, idx int) (StepOutcome, error) {
	m := fr.modules[i]
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
	inv.Env = map[string]string{
		"WM_WORKSPACE":        r.e.workspace,
		"WM_JOB_ID":           inv.ID,
		"WM_FLOW_JOB_ID":      in.id,
		"WM_ROOT_FLOW_JOB_ID": in.scope,
		"WM_ROOT_JOB_ID":      r.id,
		"WM_FLOW_PATH":        in.path,
		"WM_FLOW_STEP_ID":     fr.stepID(i),
		"WM_JOB_PATH":         r.jobPath(fr, i),
	}
	if err := r.hooks.InvocationEnv(ctx, r, site, inv.Env); err != nil {
		r.latch.Set(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return StepOutcome{}, errNotLaunched
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
	return out, err
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

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
	"github.com/invakid404/windmill-lite/internal/platform"
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
	// or m:<id>~<k> for an id that occurs more than once (OF-2).
	sites  []conformance.SitePath
	latch  Latch
	settle *RunSettlement
	// wrote is set once a write-capable module has launched (H3).
	wrote bool

	mu    sync.Mutex // guards trace and the job results below
	trace Trace
	// jobs holds, per module, the stored result of its job when it has one
	// (JE-18 step 2); nodes is the node-id map, written as each module's job
	// finishes, the last write winning (JE-19).
	jobs  []jsonval.Value
	nodes map[string]jsonval.Value
}

func newRun(e *Engine, f *ir.Flow, id string, hooks Hooks, exec StepExecutor, rng *rand.Rand) *run {
	ids := make([]string, len(f.Modules))
	for i, m := range f.Modules {
		ids[i] = m.ID
	}
	r := &run{e: e, f: f, id: id, hooks: hooks, exec: exec, rng: rng, jobs: make([]jsonval.Value, len(f.Modules)), nodes: map[string]jsonval.Value{}}
	for _, seg := range conformance.ModuleSegments(ids) {
		r.sites = append(r.sites, conformance.SitePath{}.Child(seg))
	}
	r.trace = Trace{RunID: id, Bundle: e.bundle, ModuleState: ModuleStateWarm}
	r.settle = NewRunSettlement(func() { hooks.OnRunSettled(r.snapshot()) })
	return r
}

func (r *run) ID() string      { return r.id }
func (r *run) Latch(err error) { r.latch.Set(err) }

// site is the module at index i as hooks see it.
func (r *run) site(i int) Site {
	m := r.f.Modules[i]
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
	// the root's first pickup). With no failure module, retry or
	// continue_on_error to take it (N1b, N2), the flow fails with its
	// carried value (FX-36).
	endFailed
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

// run runs the flow's modules in order (FX-2): the start checks of the
// host (Start), the module's own start and completion, then the host's
// completion checks (Completion) and Windmill's continue decision, then
// haltAfter (AfterContinue).
func (r *run) run(ctx context.Context) {
	// FX-6, JE-36: the first module's previous_result is the flow input;
	// FX-3: a flow with no modules returns it.
	prev, prevID := r.input, ""
	result, origin := r.input, OriginNone
	for i, m := range r.f.Modules {
		site := r.site(i)
		v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil, HaltBefore: r.hooks.HaltBefore(site)})
		if st, end := v.Status(); end {
			r.end(st, jsonval.Value{}, OriginNone)
			return
		}
		// The module's slot in the trace is reserved first, so late output
		// of its invocation always has a record to go to.
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
			r.end(st, jsonval.Value{}, OriginNone)
			return
		}
		// Windmill's continue decision (FX-2 completion step 7), for a flow
		// with no failure module and no tolerated failure (N1b) or retry
		// (N2).
		switch o.end {
		case endFailed, endChained:
			r.end(conformance.RunFailure, o.carried, o.origin)
			return
		case endStopped:
			r.end(conformance.RunSuccess, o.carried, o.origin)
			return
		case endStoppedSkipped:
			r.end(conformance.RunSkipped, o.carried, o.origin)
			return
		case endInterrupted:
			// Only a latch or the cancellation interrupts a module, and the
			// completion check above ends the run for both.
			r.end(conformance.RunFailure, engineError("InternalErr", "windmill-lite: a module was interrupted with no latch or cancellation"), OriginEngine)
			return
		}
		last := i == len(r.f.Modules)-1
		if AfterContinue(ContinueSignals{RunContinues: !last, Final: true, HaltAfter: r.hooks.HaltAfter(site)}) == EndHalted {
			r.mark(idx, "H8")
			r.end(conformance.RunHalted, jsonval.Value{}, OriginNone)
			return
		}
		prev, prevID = o.carried, m.ID
		result, origin = o.carried, o.origin
	}
	r.end(conformance.RunSuccess, result, origin)
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

// module runs the start phase of the module at index i (FX-2 start steps
// 7 to 10) and the first step of its completion phase, stop_after_if
// (FX-19). prev is its previous_result and prevID the id of the module
// before it; idx is its record in the trace.
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
		args, err = r.args(ctx, m.Leaf.Inputs, site.Path, prev, prevID)
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
		out, err = r.invoke(ctx, m, site, entry, stored, timeout, idx)
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
			// The run was canceled (H4); the completion check ends it.
			o.rec.Abandoned = true
			o.rec.Status, o.rec.Module, o.end = conformance.StepCanceled, conformance.ModuleFailure, endInterrupted
			return o
		case err != nil:
			res := fail(engineError("InternalErr", err.Error()), OriginEngine)
			r.recordJob(i, res.rec.Result)
			return res
		}
	}
	st := StoreOutcome(out, m.ID)
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
func (r *run) stop(ctx context.Context, m *ir.Module, site conformance.SitePath, o outcome) outcome {
	s := m.StopAfterIf
	if s == nil {
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
		o.rec.Module, o.end = conformance.ModuleFailure, endFailed
		o.carried, o.origin = objectOf(member{"error", jsonString(predicateText(err))}), OriginEngine
		o.rec.Carried = o.carried
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
	o.rec.Module, o.rec.Carried, o.end = conformance.ModuleFailure, o.carried, endFailed
	return o
}

// chaining is a chaining error raised while module i starts (FX-15), whose
// display text is text (without Windmill's Rust location, D14). At the
// root's first pickup, the first module's start, it is a failure of that
// module, named Anyhow (context 2). At a later transition the flow fails
// at once with it, named InternalError, and the module never starts
// (context 1).
func (r *run) chaining(i int, o outcome, text string) outcome {
	if i == 0 {
		v := engineError("Anyhow", "Error: "+text)
		o.rec.Status, o.rec.Module, o.rec.Carried = conformance.StepFailure, conformance.ModuleFailure, v
		o.end, o.carried, o.origin = endFailed, v, OriginEngine
		return o
	}
	o.rec.Status, o.rec.Module = conformance.StepNotRun, conformance.ModuleNotRun
	o.end, o.carried, o.origin = endChained, engineError("InternalError", text), OriginEngine
	return o
}

// recordJob records the stored result of module i's job, which results
// lookups read (JE-18, JE-19).
func (r *run) recordJob(i int, v jsonval.Value) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[i] = v
	r.nodes[r.f.Modules[i].ID] = v
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

// transformScope is the scope of a step's input transforms (FX-7, JE-22,
// JE-23): P as previous_result and no result, params always {}, the
// previous id, results with JE-9, flow_input and flow_env.
func (r *run) transformScope(prev jsonval.Value, prevID string) *expr.Scope {
	return &expr.Scope{
		PreviousID: prevID,
		Context: []expr.Named{
			{Name: "previous_result", Value: prev}, {Name: "params", Value: jsonObj},
			{Name: "resume", Value: jsonNull}, {Name: "resumes", Value: jsonEmpty}, {Name: "approvers", Value: jsonEmpty},
		},
		FlowInput: r.input,
		FlowEnv:   r.env,
		Results:   r,
	}
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

// args evaluates a step's input transforms into its argument object
// (FX-7, JE-23): one at a time, in an order the run's seed shuffles (a hash
// map's on Windmill, JE-V5), stopping at the first error; the object's
// top-level keys come in that order too (ADR 0005 step 2). A static value
// is copied verbatim (JE-40), an ai input left out.
func (r *run) args(ctx context.Context, ins []ir.Input, site conformance.SitePath, prev jsonval.Value, prevID string) (jsonval.Value, error) {
	var ms []member
	var scope *expr.Scope
	for _, k := range r.rng.Perm(len(ins)) {
		in := ins[k]
		switch in.Transform.Type {
		case ir.Static:
			ms = append(ms, member{in.Name, orNull(in.Transform.Value)})
		case ir.Javascript:
			if scope == nil {
				scope = r.transformScope(prev, prevID)
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
// engine (ADR 0004 §Cancellation and deadlines item 2).
func (r *run) invoke(ctx context.Context, m *ir.Module, site Site, entry string, args jsonval.Value, timeout time.Duration, idx int) (StepOutcome, error) {
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
	// Job identity (ADR 0007, WA-4, WA-V1): a step directly in the flow
	// has the run as its parent, its innermost scope and its effective
	// root.
	inv.Env = platform.Identity{
		JobID:         inv.ID,
		FlowJobID:     r.id,
		RootFlowJobID: r.id,
		RootJobID:     r.id,
		FlowPath:      r.f.Path,
		StepID:        m.ID,
		JobPath:       site.Script,
		Token:         platform.NewToken(),
	}.Env(r.e.host)
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

package engine

import (
	"context"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// readFailed completes module i of fr, a loop or a branchall, whose
// children's results the backend cannot read back when it completes
// (aggregate's error err). Windmill's transition rolls back, so the module
// keeps no result and stays in progress: its record is canceled, in
// progress, with the children's results so far, as the oracle's walker
// reports a module left unfinished. Which flow then ends depends on where
// the chain of transitions started ([FX-42] rule 9):
//
//   - The children are the flow's own leaf jobs, the iterations of a
//     collapsed sequential for loop or while loop: the flow fails at once
//     with InternalErr and the message `SqlErr: error occurred while
//     decoding column 0: <err>`, without the location suffix Windmill adds,
//     and with no retry, continue_on_error, failure module or later step
//     (endUnreadable). A child flow that fails so does not tell its
//     parent, which waits (flowEnd.unreported).
//   - The children are child flows, of any other loop and of every
//     branchall: no job fails with the error, and fr's flow waits (hang).
//
// [FX-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-42
func (r *run) readFailed(ctx context.Context, fr *frame, i int, o outcome, it *iterations, err error) outcome {
	if m := fr.modules[i]; (m.ForLoop != nil || m.WhileLoop != nil) && bodyOf(m).collapse {
		o = inProgress(o, conformance.StepCanceled, arrayOf(it.values), OriginNone, it.nested)
		v := engineError("InternalErr", "SqlErr: error occurred while decoding column 0: "+err.Error())
		o.end, o.carried, o.origin, o.unreported = endUnreadable, v, OriginEngine, false
		return o
	}
	return r.unreportedChildren(ctx, fr, o, it)
}

// unreportedJob completes o, a branchone or a flow step of fr, whose child
// flow, its job, ended without telling fr's flow (e, flowEnd.unreported):
// the module stays in progress, its record has the child flow job's
// status and result, nothing is carried on, and fr's flow waits (hang).
func (r *run) unreportedJob(ctx context.Context, fr *frame, o outcome, e flowEnd) outcome {
	st := conformance.StepCanceled
	switch e.status {
	case conformance.RunSuccess:
		st = conformance.StepSuccess
	case conformance.RunFailure:
		st = conformance.StepFailure
	}
	o = inProgress(o, st, e.result, e.origin, e.nested)
	r.hang(ctx, fr)
	return o
}

// unreportedChildren completes o, a loop or a branchall of fr that cannot
// complete: one of its child flows ended without telling fr's flow
// (flowEnd.unreported), or the read of its children's results failed
// (readFailed). The module stays in progress with the children's results
// in it so far, nothing is carried on, and fr's flow waits (hang).
func (r *run) unreportedChildren(ctx context.Context, fr *frame, o outcome, it *iterations) outcome {
	o = inProgress(o, conformance.StepCanceled, arrayOf(it.values), OriginNone, it.nested)
	r.hang(ctx, fr)
	return o
}

// unreported reports whether one of a parallel container's child flows
// ended without telling its flow (flowEnd.unreported).
func unreported(ends []flowEnd) bool {
	for _, e := range ends {
		if e.unreported {
			return true
		}
	}
	return false
}

// inProgress makes o a container that keeps no final status: its record
// has status st and result v, of origin and nested, its module stays in
// progress and carries nothing, and the flow's completion check ends the
// run (endInterrupted), which its parents then wait for too
// (outcome.unreported).
func inProgress(o outcome, st conformance.StepStatus, v jsonval.Value, origin Origin, nested []string) outcome {
	o.rec.Ran = true
	o.rec.Status, o.rec.Module = st, conformance.ModuleInProgress
	o.rec.Result, o.rec.ResultOrigin, o.rec.ResultNested = v, origin, nested
	o.rec.Carried, o.rec.CarriedOrigin, o.rec.CarriedNested = jsonval.Value{}, OriginNone, nil
	o.end, o.carried, o.origin, o.nested = endInterrupted, jsonval.Value{}, OriginNone, nil
	o.unreported = true
	return o
}

// hang ends a run in which waiting, a flow, waits for a transition that
// never comes, as Windmill's zombie-flow monitor ends it: the run is
// canceled with canceler "monitor" and the first sentence of the
// monitor's reason, which names the waiting flow and its run page,
// `<BASE_URL>/run/<id>?workspace=<workspace>`, in the subflow form when
// the waiting flow is a flow step's. Windmill's monitor acts once the
// flow has made no progress for FLOW_ZOMBIE_TRANSITION_TIMEOUT (60 s by
// default); the engine cancels at once, so steps of the run still in
// flight are canceled with it, deviation D20, which the trace records.
// With the monitor disabled (Config.TransitionTimeout negative), nothing
// ends the wait but the run's own cancellation, so hang returns once the
// run's context ends. Only the first call acts ([FX-42] rule 9, [JE-2]).
//
// [FX-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-42
// [JE-2]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/expressions.md#je-2
func (r *run) hang(ctx context.Context, waiting *frame) {
	if r.e.transition <= 0 {
		<-ctx.Done()
		return
	}
	r.hangOnce.Do(func() {
		env := platform.Identity{}.Env(r.e.host)
		page := waiting.id + " (" + env["WM_BASE_URL"] + "/run/" + waiting.id + "?workspace=" + env["WM_WORKSPACE"] + ")"
		reason := "Flow " + page + " was cancelled because it was hanging in between 2 steps."
		if waiting.subflow {
			reason = "Flow was cancelled because subflow " + page + " was hanging in between 2 steps."
		}
		r.mu.Lock()
		r.trace.Deviations = append(r.trace.Deviations, "D20")
		r.mu.Unlock()
		r.cancel(&Cancellation{Canceler: "monitor", Reason: reason})
	})
}

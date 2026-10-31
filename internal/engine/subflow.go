package engine

import (
	"context"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Subflows by path (milestone N3c): a flow step runs the flow at its path,
// which the compiler put in the program, as a child flow of its own
// (CF-17). Its input is the step's evaluated arguments, as a script step's
// are, read back from storage (JE-44, JE-37); it is its own results scope,
// whose lookups still walk up to its caller's (CF-19, Q-CF-12 in ADR 0001
// §6), and its own innermost flow scope (WA-V1); it has its own failure
// module and its own flow_env, or its caller's when it defines none (CF-1,
// JE-38). Its steps see its job id and its path (WA-6). A stop inside it
// ends only the subflow (CF-12, FX-20).

// inputsOf returns the input transforms of m, a leaf or a flow step.
func inputsOf(m *ir.Module) []ir.Input {
	switch {
	case m.Subflow != nil:
		return m.Subflow.Inputs
	case m.Provided != nil:
		return m.Provided.Inputs
	}
	return m.Leaf.Inputs
}

// subflow runs module i of fr, a flow step whose stored arguments are
// stored, from the launch of its subflow to the step's completion; o is
// its outcome so far. The step's result is the subflow's, and a subflow
// that failed fails the step with it (CF-17): its failure value, a failed
// container's array, or its failure module's result, a recovered one
// included, since a subflow that ran its failure module reports a failure
// to its caller (CF-V1, FX-33); one that aborted at a later transition
// hands the step its last carried value, while lookups read the stored
// error (CF-23 route 3). The step's own stop_after_if is evaluated after a
// success, whatever stop the subflow raised.
func (r *run) subflow(ctx context.Context, fr *frame, i int, site conformance.SitePath, o outcome, stored jsonval.Value) outcome {
	m := fr.moduleAt(i)
	c := r.subflowFrame(fr, i, r.e.prog.Flows[m.Subflow.Path], stored)
	r.addChild(c)
	o.rec.Ran, o.rec.Job = true, c.id
	e := r.runList(ctx, c)
	if e.unreported {
		return r.unreportedJob(ctx, fr, o, e)
	}
	o.rec.Cached = e.cached
	// The subflow's completion ends its step's job, which refreshes fr's
	// ping (JE-2).
	r.ended(fr, &o)
	if ended(e.status) {
		return branchInterrupted(o, true, r.endedStatus(ctx, e.status))
	}
	// Lookups read the subflow job's stored result; the step takes the
	// value delivered with its completion, which differs after an abort
	// (CF-23 route 3).
	r.recordJob(fr, i, e.result)
	o.rec.Result, o.rec.ResultOrigin, o.rec.ResultNested = e.result, e.origin, e.nested
	o = carryOn(o, e)
	// The step's job status is the subflow job's own: success when its
	// failure module recovered, skipped when a clean stop with
	// skip_if_stopped ended it (OR-V2).
	switch e.status {
	case conformance.RunFailure:
		o.rec.Status = conformance.StepFailure
	case conformance.RunSkipped:
		o.rec.Status = conformance.StepSkipped
	default:
		o.rec.Status = conformance.StepSuccess
	}
	if e.status == conformance.RunFailure || e.handled {
		o.rec.Module, o.end = conformance.ModuleFailure, endFailed
		return o
	}
	o.rec.Module = conformance.ModuleSuccess
	return r.stop(ctx, fr, i, site, o)
}

// carryOn makes the value delivered with a single child flow's completion,
// e, what o, a branchone or a subflow step, carries on (flowEnd.carried):
// its result, or the child's last carried value after an abort, which
// the engine set apart from the job's result (CF-23 route 3).
func carryOn(o outcome, e flowEnd) outcome {
	c := e.carried()
	o.carried, o.origin, o.nested = c.value, c.origin, c.nested
	o.rec.Carried, o.rec.CarriedNested = c.value, c.nested
	if e.delivered != nil {
		o.rec.CarriedOrigin = c.origin
	}
	return o
}

// subflowFrame creates the child flow that module i of fr, a flow step,
// runs: the modules and the failure module of sub, the flow at its path,
// at the step's own site, with stored, the step's stored arguments, as its
// input, read back from storage (JE-44) and its first module's
// previous_result (CF-9). It is a results scope of its own below fr's
// (CF-19) and its own innermost flow scope (WA-V1), at sub's path (WA-6),
// and keeps flow_env of its own or else fr's (CF-17, JE-38), and its flow's
// cache_ttl, under which the result cache may answer its flow job (FX-40:
// the step's own cache_ttl is dropped). It runs on
// fr's goroutine, so it shares fr's rng.
func (r *run) subflowFrame(fr *frame, i int, sub *ir.Flow, stored jsonval.Value) *frame {
	now := r.clock.Now()
	c := newFrame(sub.Modules, fr.sites[i])
	c.site, c.child, c.subflow, c.id, c.pinged, c.started = fr.sites[i], true, true, newUUID(), now, now
	c.def, c.failure = sub, sub.FailureModule
	c.input = jsonval.FromProducer(flowInputText(stored.Producer(), fr.rng))
	c.path, c.nodes, c.rng = sub.Path, newNodeScope(fr.nodes), fr.rng
	c.scope, c.cacheTTL = c.id, flowTTL(sub)
	if c.envSrc = flowEnv(sub); c.envSrc.IsZero() {
		c.envSrc = fr.envSrc
	}
	return c
}

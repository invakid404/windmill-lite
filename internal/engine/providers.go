package engine

import (
	"context"

	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// This file is where a run reads the platform providers of ADR 0007
// (milestones PF2 and PF3): the expression helpers variable(), resource()
// and flow_user_state() (JE-20), flow_env's resolution on every transition
// (JE-38), and a leaf job's argument resolution before its script runs
// (JE-41, JE-42, WR-5), all through the engine's platform.Services.

// helpers answers the expression helpers of JE-20 for one run, whose
// effective root is root: the run, or for a run dispatched tracked in its
// parent the root its jobs share, whose flow user state the stock helpers
// address too. Every engine site has an authenticated client
// (only suspend's user_groups_required lacks one, and the engine does not
// run suspend).
type helpers struct {
	s    *platform.Services
	root string
}

// Variable is variable(path): the variable's value, a secret's plaintext,
// or the API's error text.
func (h helpers) Variable(ctx context.Context, path string) (string, error) {
	return h.s.VariableValue(ctx, path)
}

// Resource is resource(path): the resource's value interpolated as the
// client's read is (WA-12 contract (b), without a job id; which contract
// Windmill's helper uses is not in the reference).
func (h helpers) Resource(ctx context.Context, path string) (jsonval.Value, error) {
	b, err := h.s.InterpolatedResource(ctx, path, nil)
	if err != nil {
		return jsonval.Value{}, err
	}
	return jsonval.FromProducer(b), nil
}

// FlowUserState is flow_user_state(key): the value stored under key for
// the run's root flow job (WA-14), JSON null when there is none, as the
// API's read gives it.
func (h helpers) FlowUserState(ctx context.Context, key string) (jsonval.Value, error) {
	v, err := h.s.FlowUserState(ctx, h.root, key)
	if err != nil {
		return jsonval.Value{}, err
	}
	return jsonval.FromProducer(v), nil
}

// resolveEnv resolves flow_env for the transition that starts a module of
// fr (JE-38): its $var: and $res: references, as a leaf's arguments are
// (JE-41, JE-42), with the flow job's reserved variables. The map is fr's
// scope's (envSrc): a child flow inherits its flow's, and a subflow by path
// has its own unless it defines none (CF-1, CF-17); reading, its $WM_*
// strings are the top-level flow job's. A failure leaves the unresolved
// map, with a warning, and is not cached; a success becomes the flow's
// cached map, which the predicates after a child completes read
// (stopScope). The result is that of fr's flow, a collapsed iteration's its
// loop's (owner), which only the goroutine that drives it touches.
func (r *run) resolveEnv(ctx context.Context, fr *frame) {
	fr = fr.owner()
	if fr.envSrc.IsZero() {
		return
	}
	v, err := r.platform.ResolveArgs(ctx, fr.envSrc, r.flowJobEnv, platform.APIEnv(r.flowJobEnv))
	if err != nil {
		fr.env = fr.envSrc
		r.mu.Lock()
		r.trace.Warnings = append(r.trace.Warnings, Warning{Code: "flow_env_unresolved", Message: "flow_env is used unresolved: " + err.Error()})
		r.mu.Unlock()
		return
	}
	fr.env, fr.envCached = v, v
}

// envOf is flow_env as fr's expressions see it: as its current
// transition resolved it, or its scope's stored map before its first
// module.
func (r *run) envOf(fr *frame) jsonval.Value {
	own := fr.owner()
	if !own.env.IsZero() {
		return own.env
	}
	return own.envSrc
}

// cachedEnvOf is flow_env as the predicates evaluated after a module or a
// child completes see it (JE-38): the flow's cached resolved map once one
// exists, else envOf's.
func (r *run) cachedEnvOf(fr *frame) jsonval.Value {
	if own := fr.owner(); !own.envCached.IsZero() {
		return own.envCached
	}
	return r.envOf(fr)
}

// pickupFailure is what failed a leaf job once the worker picked it up,
// before its script runs: its envs entries (WR-22) or its argument
// resolution (JE-42). The job fails with the worker's execution error.
type pickupFailure struct{ err error }

func (e *pickupFailure) Error() string { return e.err.Error() }
func (e *pickupFailure) Unwrap() error { return e.err }

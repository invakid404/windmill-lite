// Package wlext is what package wl lends the engine's own adapters
// (internal/host) beyond its public API: the static interceptions that
// host mode's run frame carries, the refusal of the id of a run that a step
// dispatched, a run's trace in the JSON form that a host
// plugin's run-end hooks receive (ADR 0009, the Runner service), and the
// report of a warm pool process that fails to start (ADR 0009, health,
// readiness and drain). Only
// packages of this module can import it, so a host that imports wl sees
// none of it, and a run that does not use it runs as before.
package wlext

import (
	"context"
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/engine"
)

type interceptsKey struct{}

// WithStaticIntercepts returns ctx carrying a flow run's static
// interceptions (ADR 0008, the no-code form): by module id, the result
// that stands in for every module with that id, as host mode's run frame
// gives them in its intercept. wl's Runtime.Run reads them for the flow run
// that it starts with the returned context. An empty map carries none.
func WithStaticIntercepts(ctx context.Context, results map[string]json.RawMessage) context.Context {
	if len(results) == 0 {
		return ctx
	}
	return context.WithValue(ctx, interceptsKey{}, results)
}

// StaticIntercepts returns the static interceptions that ctx carries, or
// nil.
func StaticIntercepts(ctx context.Context) map[string]json.RawMessage {
	m, _ := ctx.Value(interceptsKey{}).(map[string]json.RawMessage)
	return m
}

type exclusiveKey struct{}

// WithExclusiveID returns ctx marking the run that wl's Runtime.Run starts
// with it as one whose id no run that a step dispatched may hold: Run
// refuses it with wl.ErrRunInFlight while such a run has the id, from
// before the dispatch returned the id, through a delayed start, until the
// run ends. A run without the mark keeps Run's own checks only.
func WithExclusiveID(ctx context.Context) context.Context {
	return context.WithValue(ctx, exclusiveKey{}, true)
}

// ExclusiveID reports whether ctx carries WithExclusiveID's mark.
func ExclusiveID(ctx context.Context) bool {
	v, _ := ctx.Value(exclusiveKey{}).(bool)
	return v
}

// TraceJSON returns a flow run's trace as JSON, at one of the levels of a
// host plugin's run-end hooks (ADR 0004, host plugins): "full", every field
// of the trace with its records, or "summary", the trace without its
// per-step parts. It is the object that such a hook receives as its
// payload's trace. Package wl sets it when it is initialized.
var TraceJSON func(t *engine.Trace, level string) ([]byte, error)

// OnPoolStartFailure makes rt, a *wl.Runtime that Open returned, call f
// with the error of each process that its warm pool fails to start from
// then on, until Close begins: a replacement, or a process that a step
// needs. The pool logs the failure first, and the step that needed the
// process still fails as before. f must not block for long; nil stops the
// reports. Package wl sets it when it is initialized.
var OnPoolStartFailure func(rt any, f func(error))

// Package wlext is what package wl lends the engine's own adapters
// (internal/host) beyond its public API: the static interceptions that
// host mode's run frame carries, and a run's trace in the JSON form that a
// host plugin's run-end hooks receive (ADR 0009, the Runner service). Only
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
// interceptions (ADR 0008 H1, the no-code form): by module id, the result
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

// TraceJSON returns a flow run's trace as JSON, at one of the levels of a
// host plugin's run-end hooks (ADR 0004, host plugins): "full", every field
// of the trace with its records, or "summary", the trace without its
// per-step parts. It is the object that such a hook receives as its
// payload's trace. Package wl sets it when it is initialized.
var TraceJSON func(t *engine.Trace, level string) ([]byte, error)

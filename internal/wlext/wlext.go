// Package wlext is what package wl lends the engine's own adapters
// (internal/host) beyond its public API: the static interceptions that
// host mode's run frame carries, the refusal of the id of a run that a step
// dispatched, a run's trace in the JSON form that a host
// plugin's run-end hooks receive (ADR 0009, the Runner service), and, for
// a drain (ADR 0009, health, readiness and drain), the report of a warm
// pool process that fails to start and the end of the runs that steps
// dispatched. Only
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
// then on: a replacement, or a process that a step needs. Close's start
// clears f: a report that had already taken f may still call it after
// that, and no report takes it later. The pool writes a diagnostics line
// for the failure first, except while an unsettled-run or a held-age
// recycle trigger is set: then that line is best effort, and can come
// after f's call, or never. The step that needed the process still fails as
// before. f must not block for long; nil stops the reports. Package wl
// sets it when it is initialized.
var OnPoolStartFailure func(rt any, f func(error))

// RunHooks are a run's hooks on a host's connection ([ADR 0009], hooks
// across the plugin boundary): a host plugin's declaration, as JSON, and
// the function that sends one call of them and returns its answer. The
// calls have a host plugin's host_call hook names and payloads, and a hook
// that the host failed returns a *wl.HostCallError.
//
// [ADR 0009]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0009-host-adapters.md#hooks-across-the-plugin-boundary
type RunHooks struct {
	Declaration []byte
	Call        func(ctx context.Context, hook, run, site string, payload []byte) ([]byte, error)
}

type runHooksKey struct{}

// WithRunHooks returns ctx carrying a flow run's hooks on a host's
// connection: wl's Runtime.Run makes them the hooks of the flow run that it
// starts with ctx, its own, ahead of the runtime's host plugin, and refuses
// the run when their declaration is not one or declares around.
func WithRunHooks(ctx context.Context, h RunHooks) context.Context {
	return context.WithValue(ctx, runHooksKey{}, h)
}

// RunHooksOf returns the run's hooks that ctx carries.
func RunHooksOf(ctx context.Context) (RunHooks, bool) {
	h, ok := ctx.Value(runHooksKey{}).(RunHooks)
	return h, ok
}

type settledKey struct{}

// WithSettled returns ctx making wl's Runtime.Run call f once the run that
// it starts with ctx has settled: its outcome decided, and every invocation
// and hook call that it started settled. That may be never, and is never
// for a run that does not start.
func WithSettled(ctx context.Context, f func()) context.Context {
	return context.WithValue(ctx, settledKey{}, f)
}

// Settled returns the function that ctx carries (WithSettled), or nil.
func Settled(ctx context.Context) func() {
	f, _ := ctx.Value(settledKey{}).(func())
	return f
}

// BrokerProviders makes opts, a *wl.Options, answer each named provider of
// names with the hooks of the run that reaches it (WithRunHooks); one whose
// hooks do not serve it latches there. Package wl sets it when it is
// initialized.
var BrokerProviders func(opts any, names []string)

// DispatchIdle returns a channel that is closed once no run that rt, a
// *wl.Runtime, dispatched for a step is in flight, through a delayed start
// too: at once if none is. A run that one in flight dispatches before it
// ends keeps it open. Package wl sets it when it is initialized.
var DispatchIdle func(rt any) <-chan struct{}

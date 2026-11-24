# internal/wlext

**Current state:** what [wl](../../wl/README.md) lends to the engine's own
adapters ([internal/host](../host/README.md)) and keeps out of its public
API. That is host mode's static interceptions of a flow run, the refusal of
the id of a run that a step dispatched, and a run's trace in the JSON form
of a host plugin's run-end hooks. It also reports a harness process that
the warm pool fails to start.

This README covers `internal/wlext`, the Go package
`github.com/invakid404/windmill-lite/internal/wlext`.

The `Runner` service serves the fields of host mode's run frame under the
same names ([host adapters](../../docs/design/0009-host-adapters.md#the-runner-service)).
Some of what it needs has no form in `wl`'s public API, and defining it a
second time in `internal/host` could let the two drift apart. Only packages of
this module can import an `internal` package, so a host that imports `wl`
sees none of it, and a run that does not use it runs as before.

| Name | What it does |
|---|---|
| `WithStaticIntercepts(ctx, results)` | Returns a context that carries a flow run's static interceptions: by module id, the result that stands in for every module with that id, in any flow of the run, as host mode's `intercept` gives them. `Runtime.Run` gives them to the flow run that it starts with that context |
| `StaticIntercepts(ctx)` | The interceptions that a context carries, or nil |
| `WithExclusiveID(ctx)`, `ExclusiveID(ctx)` | Mark a context, and read the mark. `Runtime.Run` refuses a run that it starts with a marked context, with `wl.ErrRunInFlight`, while a run that a step dispatched has its id: from before the dispatch returned the id, through a delayed start, until that run ends (`engine.Dispatcher.InFlight`). The check is opt-in. A run without the mark keeps `wl`'s own checks as they are, such as the pin that a dispatched run takes under `Pool.PinRuns` |
| `TraceJSON(trace, level)` | A flow run's trace as JSON at the level `full` or `summary`: the object that a host plugin's `onRunEnd` receives as its payload's `trace` at that level ([host plugins](../../wl/README.md#host-plugins)). Package `wl` sets it when it is initialized |
| `OnPoolStartFailure(rt, f)` | Makes a `*wl.Runtime` that `Open` returned call `f` with the error of each harness process that its warm pool fails to start from then on, a replacement or a process that a step needs. `Close` clears `f` first, though a report that had already taken `f` may still call it; nil stops the reports. The pool logs the failure first, and the step still fails as before. `windmill-lite plugin` drains on it; package `wl` sets it when it is initialized |
| `DispatchIdle(rt)` | A channel that is closed once no run that the runtime dispatched for a step is in flight, through a delayed start too, at once if none is (`engine.Dispatcher.Idle`). `windmill-lite plugin`'s drain waits for it. Package `wl` sets it when it is initialized |

## Files

| File | Contents |
|---|---|
| `wlext.go` | The package comment and the seven names |

## Tests

The package holds no logic of its own to test. The plugin tests of
[cmd/windmill-lite](../../cmd/windmill-lite/README.md#plugin-tests) check
each through the engine. `TestPluginFlowRequest` checks that an
interception's result stands in for its module, and that each trace level
gives its object. `TestPluginDispatchedID` checks that the id of a
dispatched run is refused while it waits for its start and while it runs,
and `TestPluginReplacementFailure` that a replacement that fails to start
makes the engine drain.

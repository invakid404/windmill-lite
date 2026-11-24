# internal/wlext

**Current state:** two things that [wl](../../wl/README.md) lends to the
engine's own adapters ([internal/host](../host/README.md)) and keeps out
of its public API: host mode's static interceptions of a flow run, and a
run's trace in the JSON form of a host plugin's run-end hooks. This README
covers `internal/wlext`, the Go package
`github.com/invakid404/windmill-lite/internal/wlext`.

The `Runner` service serves the fields of host mode's run frame under the
same names ([host adapters](../../docs/design/0009-host-adapters.md#the-runner-service)).
Two of them have no form in `wl`'s public API, and defining them a second
time in `internal/host` could let the two drift apart. Only packages of
this module can import an `internal` package, so a host that imports `wl`
sees neither, and a run that does not use them runs as before.

| Name | What it does |
|---|---|
| `WithStaticIntercepts(ctx, results)` | Returns a context that carries a flow run's static interceptions: by module id, the result that stands in for every module with that id, in any flow of the run, as host mode's `intercept` gives them. `Runtime.Run` gives them to the flow run that it starts with that context |
| `StaticIntercepts(ctx)` | The interceptions that a context carries, or nil |
| `TraceJSON(trace, level)` | A flow run's trace as JSON at the level `full` or `summary`: the object that a host plugin's `onRunEnd` receives as its payload's `trace` at that level ([host plugins](../../wl/README.md#host-plugins)). Package `wl` sets it when it is initialized |

## Files

| File | Contents |
|---|---|
| `wlext.go` | The package comment and the three names |

## Tests

The package holds no logic of its own to test.
`TestPluginFlowRequest` in [cmd/windmill-lite](../../cmd/windmill-lite/README.md#plugin-tests)
checks both through the engine: an interception's result stands in for its
module, and each trace level gives its object.

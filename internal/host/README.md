# internal/host

**Current state:** the `windmill_lite.v1.Runner` gRPC service over one
[wl](../../wl/README.md) runtime: `Run`, `Cancel` and `GetBuildInfo`.
`GetRun` answers `UNIMPLEMENTED`, and a request's `hooks`, `pin`, `limits`
and `run_data` are refused, until the engine serves the host's hooks on
go-plugin's broker and those run options. Health and readiness, and the
plain gRPC and HTTP adapters, are planned
([host adapters](../../docs/design/0009-host-adapters.md)). This README
covers `internal/host`, the Go package
`github.com/invakid404/windmill-lite/internal/host`.

`windmill-lite plugin` serves this service to a Go host that launches the
engine with [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin)
([cmd/windmill-lite](../../cmd/windmill-lite/README.md#plugin)). The
host's side is [wl/plugin](../../wl/plugin/README.md), whose types
`runner.proto` generates ([proto](../../proto/README.md)).

## Use

`host.New(host.Config{Runtime: rt, Labels: labels, ConfigDigest: digest})`
returns a `*Server`, a `plugin.RunnerServer` that `plugin.RunnerPlugin`
registers on a gRPC server. It never closes the runtime. `Config.Now`, nil
by default, is the clock that stamps a request's receipt; tests set it.

| Call | What it does |
|---|---|
| `GetBuildInfo` | The runtime's `BuildInfo`, with `protocol_version` 1, the configuration's labels and its digest |
| `Run` | Maps the request to `Runtime.Run`, and answers the run's `RunResult` or a refusal |
| `Cancel` | Cancels the run in flight with the id, with a `wl.Cancellation` of the request's reason and canceler. An id of no run in flight answers `NOT_FOUND` |

## How a request maps to a run

| Field | What the run gets |
|---|---|
| `id` | The run's job id; empty means a new UUID, which the server makes so that `Cancel` finds the run |
| `kind`, `path`, `args` | `RunOptions.Kind` (`flow` when empty, or `script`) and `Runtime.Run`'s path and arguments. The path of a flow is that of any flow of the bundle, a root or one that a flow calls by path, as `Runtime.Run` takes it |
| `deadline_ms` | The deadline of the run's context: the receipt of the request plus the budget. 0 or less has passed; above 9,223,372,036,854 ms it is the longest duration. Absent means none, and the call's own deadline still ends the call |
| `pre_write_deadline_ms` | `RunOptions.PreWriteDeadline`, counted from the receipt |
| `write_capable`, `halt_before`, `halt_after` | `Hooks.WriteCapable`, `HaltBefore` and `HaltAfter`: each exactly one JSON array of selector objects. JSON `null`, anything after the array, and a selector key that `wl.Selector` does not have refuse the run |
| `fatal_error_names` | `Hooks.FatalErrorNames` |
| `intercept` | Host mode's static interceptions: by module id, the result that stands in for every module with that id ([internal/wlext](../wlext/README.md)) |
| `clock` | `real` or empty: the real clock; `record`: a `wl.RecordClock` |
| `identity` | `RunOptions.Identity` |
| `resources`, `variables` | `RunOptions.Platform`: the run's own values by path |
| `trace` | The level of the result's trace: `none` or empty, `summary` or `full` |

- **A root script's run** takes `id`, `kind`, `path`, `args`,
  `deadline_ms`, `identity` and the trace level `none`. Any other field
  that the request sets refuses the run, naming the field.
- **Fields that the engine does not serve yet:** `hooks`, `pin`, `limits`
  and `run_data` refuse every run, naming the field.
- **Fields that the engine does not know,** which a newer host's request
  can hold, refuse the run, naming their field numbers. So do those of
  `identity`, and of each value of `resources` and `variables`, naming the
  field that holds them.
- **The id of a run that a step dispatched** refuses the run until that
  run ends, from before the dispatch returned the id, through
  `internal/wlext`'s mark.

## The result

| Field | Value |
|---|---|
| `id`, `status`, `result`, `cancel_cause` | `wl.Result`'s `ID`, `Status`, `Result` and `CancelCause` |
| `may_have_mutated` | For a flow run, `Result.MayHaveMutated`. For a root script's run, `Result.Started`: true once the script's invocation was sent, since its writes cannot be seen |
| `latched` | The latched error's text |
| `warnings` | A JSON array of objects with a `code` and a `message`; empty when the run has none |
| `deviations` | The deviations of the flow run's trace |
| `trace` | The flow run's trace at the requested level, in the JSON form that a host plugin's run-end hook receives at that level; empty at `none` |

## Refusals

A run that starts answers its `RunResult`, a failed, canceled or latched
one included. A run that does not start answers one of these gRPC codes,
with a `google.rpc.ErrorInfo` detail of the domain `windmill-lite` and the
reason `RUN_NOT_STARTED`, which `wl/plugin`'s `Client.Run` reports as
`ErrNotStarted`.

| Code | When |
|---|---|
| `ALREADY_EXISTS` | A run with the id is in flight: one of this server, or a run that a step dispatched and that has not ended (`wl.ErrRunInFlight`) |
| `NOT_FOUND` | No flow, or no root script, of the bundle has the path (`engine.ErrNoTarget`) |
| `UNAVAILABLE` | The runtime is closed (`wl.ErrClosed`) |
| `INVALID_ARGUMENT` | The request's own checks above, and every other error with which `Runtime.Run` refuses a run, such as arguments that are not a JSON object |

## Files

| File | Contents |
|---|---|
| `server.go` | The package comment, `Server`, the request and result mapping, and the refusals |

## Tests

The tests of this package run the real engine:
[cmd/windmill-lite](../../cmd/windmill-lite/README.md#plugin-tests)
launches its test binary as the engine through `wl/plugin` and calls this
service over go-plugin's connection. `TestPluginDeadlineFromReceipt` there
runs a `Server` in its own process, over a runtime of the test bundle, with
a `Config.Now` that stamps the receipt earlier. The package has no tests of
its own.

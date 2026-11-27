# internal/host

**Current state:** the `windmill_lite.v1.Runner` gRPC service over one
[wl](../../wl/README.md) runtime: `Run`, `Cancel`, `GetBuildInfo` and
`GetRun`; `HooksControl`'s `ReleaseHooks`; and a flow run's hooks, which the host answers on go-plugin's
broker. A `Gate`
answers gRPC health from the engine's state, Starting, Serving or
Draining, and refuses the calls while the engine starts, and new runs
while it drains. The plain gRPC and HTTP adapters
are planned ([host adapters](../../docs/design/0009-host-adapters.md)).

This README covers `internal/host`, the Go package
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

The rest of `Config` serves the runs' hooks
([A run's hooks on the broker](#a-runs-hooks-on-the-broker)).
`Config.Broker` returns go-plugin's broker of the connection; without one,
a request with `hooks` is refused. `Config.PluginTimeout` bounds the
release call, 60 s when zero, and `Config.MaxMessageBytes`, 512 MiB when
zero, each hook call's messages. `Config.TokenBound`, 65,536 when zero, is
the bound of gone tokens, and `Config.OnTokenBound` is called once at it.

| Call | What it does |
|---|---|
| `GetBuildInfo` | The runtime's `BuildInfo`, with `protocol_version` 1, the configuration's labels and its digest |
| `Run` | Maps the request to `Runtime.Run`, and answers the run's `RunResult` or a refusal |
| `Cancel` | Cancels the run in flight with the id, with a `wl.Cancellation` of the request's reason and canceler. An id of no run in flight answers `NOT_FOUND` |
| `GetRun` | Says whether the engine still holds the run of a hooks registration, by its id and token: `held` or `gone` |

`Gate.Hooks()` is the `windmill_lite.v1.HooksControl` service, which serves
beside `Runner`. Its one call, `ReleaseHooks`, closes the engine's
connection to a broker id that the host let go, at once, even while a run
uses it.

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
| `run_data` | `RunOptions.Data`: the run's data, which the host plugin of each process that the run uses reads as `wl.run.data`. Data that is not JSON refuses the run |
| `identity` | `RunOptions.Identity` |
| `resources`, `variables` | `RunOptions.Platform`: the run's own values by path |
| `pin` | `RunOptions.Pin`: the run keeps one harness process. A runtime without a `warm` pool refuses it |
| `limits` | `RunOptions.Limits`: `max_in_flight`, `max_args_bytes`, `max_held_bytes` and the byte budget `bytes`, each 0 for none. A negative limit refuses the run, and so does a count that an `int` cannot hold |
| `trace` | The level of the result's trace: `none` or empty, `summary` or `full` |
| `hooks` | The run's hooks on go-plugin's broker: the broker id of the host's `HostHooks` connection, the registration's token and a host plugin's declaration |

- **A root script's run** takes `id`, `kind`, `path`, `args`,
  `deadline_ms`, `identity`, `run_data`, `pin` and the trace level `none`.
  Any other field that the request sets refuses the run, naming the field.
- **Fields that the engine does not know,** which a newer host's request
  can hold, refuse the run, naming their field numbers. So do those of
  `identity`, of `limits`, of `hooks`, and of each value of `resources`
  and `variables`, naming the field that holds them.
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
| `answer` | `Result.Answer`: the JSON text of the run-end answer of the bundle's host plugin, as `wl` masks it, the text `null` for an answer of null that no mask changed, and empty when the run has none |

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
| `INVALID_ARGUMENT` | The request's own checks above, and every other error with which `Runtime.Run` refuses a run, such as arguments that are not a JSON object or are larger than `limits.max_args_bytes` (`wl.ErrRunArgsTooLarge`) |

## A run's hooks on the broker

A flow run's request can name hooks that the host answers on go-plugin's
broker ([hooks across the plugin boundary](../../docs/design/0009-host-adapters.md#hooks-across-the-plugin-boundary)).

- **Admission.** `hooks` needs a token. A token that a run of the engine
  holds refuses the run with `ALREADY_EXISTS`, and one that `GetRun`
  answered `gone` for with `INVALID_ARGUMENT`. Both carry the
  `RUN_NOT_STARTED` detail, and neither reaches the host.
- **The connection.** The engine dials each broker id once, with its
  message limit, and keeps the connection, so runs open no listener of
  their own. A dial waits on its own: a broker id that does not answer
  stalls neither the other ids nor the tokens.
- **Its end.** `ReleaseHooks`, on the engine's connection, says that the
  host let a connection go, so it arrives also while that one is down. The
  engine closes it at once, or as its dial ends: a call already sent on it
  fails, and a later call of a run that named it gets no answer. One that
  cannot connect is otherwise closed only once no held run uses it, and the
  host then serves a new broker id.
- **A dial ends ready.** A broker id's dial waits twice: for go-plugin to
  give the host's address, up to its 5 seconds, and then until the
  connection is ready, at most the plugin timeout. A connection that
  cannot connect refuses the run; the host stops its listener before it
  sends the release, so a release that overtakes an admission refuses it.
- **An admission ends with its run.** A Cancel or deadline while it waits
  refuses the run with `CANCELLED` or `DEADLINE_EXCEEDED`, and lets its
  token go. Other admissions keep the dial. A run whose deadline had passed
  when its request arrived waits for the dial and ends canceled.
- **The run's hooks.** `wl` builds the declaration as a host plugin of the
  run alone ([internal/wlext](../wlext/README.md)). Each of its calls goes
  to `HostHooks.Call` with a `host_call`'s hook name and payload. A
  `HookError` reply is the hook's failure, a `wl.HostCallError`.
- **No registration.** The host answers `unknown_run` when it holds no
  registration of the run. The engine takes that as no answer, which
  latches nothing: an interception or a provider declines, so a mandatory
  provided site still fails closed, and a rewrite keeps the arguments.
- **Release.** Once the run has settled, the engine sends the call
  `released` with the run's id and token, and then no longer holds the
  token. The engine holds a token from admission, before the runtime checks
  the run, so a lookup in between answers `held`. A run that the runtime
  then refuses gives the token up at once, and sends no release call.
- **`GetRun`** answers `held` while the engine holds the token for that run
  id, and `gone` otherwise. The engine keeps each `gone` token for the life
  of the process, and refuses a later run that carries it.
- **The bound.** At `Config.TokenBound` gone tokens, the engine calls
  `OnTokenBound` once. It then answers `UNAVAILABLE` for a token that it
  does not keep, so it never keeps more.

## Health and readiness

`windmill-lite plugin` completes go-plugin's handshake before its runtime
opens, and can drain, so the engine serves in three states, which a `Gate` holds
([health, readiness and drain](../../docs/design/0009-host-adapters.md#health-readiness-and-drain)).

| State | Health | `Run` | `GetBuildInfo`, `Cancel`, `GetRun`, `ReleaseHooks` |
|---|---|---|---|
| `Starting`: the runtime opens, with its warm start | `NOT_SERVING` | `UNAVAILABLE` with the `RUN_NOT_STARTED` detail | `UNAVAILABLE` |
| `Serving`: after `Gate.Serve` | `SERVING` | the `Server` | the `Server` |
| `Draining`, for good: after `Gate.Drain` | `NOT_SERVING` | `UNAVAILABLE` (`draining`) with the `RUN_NOT_STARTED` detail; the runs in flight go on | the `Server`, or `UNAVAILABLE` if the drain began while the engine started; `GetRun` `UNAVAILABLE` without the detail; `ReleaseHooks` the `Server` |

- **`NewGate()`** returns a `Gate` in `Starting`. `Gate.Serve(srv)` moves it
  to `Serving` once, with the `Server` of the opened runtime.
  `Gate.Drain()` moves it to `Draining` from either, for good.
- **`Gate.Idle()`** is a channel that closes once no `Run` call that went to
  the `Server` is in flight. **`Gate.Close()`** ends every `Watch`, each
  after it has sent the current status.
- **`Gate.Runner()`** is the `plugin.RunnerServer` that the command
  registers. It sends every call to the `Server` once the engine serves.
- **`Gate.ServerOptions()`** are the gRPC server options with the
  interceptors that answer `grpc.health.v1.Health` from the state, before
  the health service that go-plugin registers sees a call. `Check` and
  `List` answer for `""`, `plugin` and `windmill_lite.v1.Runner`. `Check`
  of another name answers `NOT_FOUND`, and `Watch` of one sends
  `SERVICE_UNKNOWN`; `Watch` sends the current status, then each change.
  Every other method of the service answers `UNIMPLEMENTED`.

## Files

| File | Contents |
|---|---|
| `server.go` | The package comment, `Server`, the request and result mapping, and the refusals |
| `health.go` | `State`, `Gate`: the engine's state, its gated `Runner` service and the health interceptors |
| `hooks.go` | The runs' hooks on go-plugin's broker: their admission and calls, the tokens, `GetRun`'s answers and the release call |

## Tests

The tests of this package run the real engine:
[cmd/windmill-lite](../../cmd/windmill-lite/README.md#plugin-tests)
launches its test binary as the engine through `wl/plugin` and calls this
service over go-plugin's connection. `TestPluginDeadlineFromReceipt` there
runs a `Server` in its own process, over a runtime of the test bundle, with
a `Config.Now` that stamps the receipt earlier.

The package's own tests, in `health_test.go`, run the `Gate`'s health
interceptor on a fake stream:
`TestGateWatchSendsEachChange` holds a `Watch`'s first send while the
`Gate` serves and then drains, and still gets `NOT_SERVING`, `SERVING`,
`NOT_SERVING`. `TestGateCloseEndsUnknownWatch` ends a `Watch` of another
name with the `Gate`'s `Close`. `TestGateDrainBeforeServe` checks that a
`Gate` that drained while the engine started keeps every call from the
`Runner` that `Serve` then offers.

The broker tests, in `broker_test.go` and `brokerrun_test.go`, run the
engine in this process with Bun 1.4.3. A `Server` over a `wl` runtime of
the plugin cases' bundle stands behind a `Gate`, on go-plugin's test
connection, which has a real broker on both sides. The host is the
`wl/plugin` `Client` that the connection dispenses.

| Test | What it shows |
|---|---|
| `TestBrokerCases` | The plugin cases (`conformance/plugins/hooks.yaml`) with each run's hooks on the broker, a case with a pool of its own on an engine with that pool. Each meets its expectations, and the host hears exactly the calls that the case's fixture and the run's trace give. The cases of `around`, of step code and of the TypeScript plugin's failure options are left out, each with its reason |
| `TestBrokerAroundRefused` | A declaration with `around` does not start its run, reaches no host, and leaves no registration |
| `TestBrokerHookTimeout` | A hook whose host holds its answer latches the run with the `host_call` timeout |
| `TestBrokerProviderMissing` | A provider set to broker answers from the run's hooks; a run whose hooks do not serve it, or that has none, latches there |
| `TestBrokerPhaseFailures` | A failing `onRunEnd` is a warning, a failing `onRunSettled` a diagnostic, a failing observer a latch |
| `TestBrokerOrderWithPlugin` | Where the run's hooks and a host plugin both intercept a step and answer a resource, the run's hooks answer. A provider that both serve refuses `wl.Open` |
| `TestBrokerLateCallback` | `Cancel` ends a run once its step has started, and the step, which runs on, holds the settlement: `Run` returns, then `onRunSettled` reaches the host and is served, then the registration goes |
| `TestBrokerIsolationNextRun` | The next run, with other hooks or none, never reaches a run's hooks: a run without hooks sends no hook call, and no call of the next run carries the first run's token |
| `TestBrokerNoListenerGrowth` | 1,000 runs share one broker id and one connection, and leave descriptors, goroutines and registrations as they were |
| `TestBrokerRegistrations` | The release call drops a provider-only run's registration without a lookup; a run without an id gets one; a refused run is dropped at once; a held id is refused and a released one reused |
| `TestBrokerLostMessages` | Each case on an engine of its own, its transports losing or delaying messages: a lost refusal, a lost release, a delayed call of an earlier run and a failed lookup. Then a request delayed past `gone`, and a plain run of an id that a lost release keeps registered |
| `TestBrokerTokenBound` | With the bound lowered to 8: nine runs whose releases are lost. Their Clients' first lookups are held until the engine has let the tokens go, through a transport that keeps their deadlines off the wire. Eight `gone` answers drain the engine, which keeps 8 tokens, and the Client drops their registrations; the ninth lookup answers `UNAVAILABLE` without `RUN_NOT_STARTED` and the Client keeps it; new runs are refused. The end of the generation drops it |
| `TestBrokerStaleCalls` | Calls that the host answers `unknown_run` latch nothing: a stale observer and interception leave the run to succeed, a stale provider at a mandatory site fails closed, and a stale first-write verdict lets the write go ahead. A registered run's handler that replies with that code fails its verdict, which latches the run before the write |
| `TestBrokerMessageLimits` | The host takes an `onRunEnd` whose trace is over 5 MiB; an engine whose limit is 1 MiB refuses an answer of 2 MiB |
| `TestBrokerDialStall` | While the test holds the dial of a silent broker id, `GetRun` answers and another run with hooks completes; let go, the dial fails and its run is refused |
| `TestBrokerClientClose` | With two `Client`s and a run of one held in flight, `Close` of that one makes the engine close its connection at once (`ReleaseHooks`); a run with hooks after `Close` is refused; the other `Client`'s runs are still served. The held run then succeeds without warnings, and its token goes |
| `TestBrokerFirstRunRefused` | A first run with hooks refused before the engine dials the host; after go-plugin has dropped that broker id's address, the next run on the same `Client` gets a new broker id and is served |
| `TestBrokerTransientFailure` | The test's dialer drops the connection and fails dials while the host's listener serves. A run in flight keeps the connection, which connects again; with no run in flight the engine closes it, and the next run on the same `Client` is served, also while a lost release's registration is not yet reconciled. `Close` during a failure, with a run held, closes the engine's side before the run ends |
| `TestBrokerCloseDuringVerdict` | `Close` while the host's handler of a first-write verdict still runs: the cut call fails, so the run latches and its write-capable step never runs |
| `TestBrokerReleaseRetried` | A release call lost before it reaches the engine, at `Close` with a run held: the `Client` sends it again, and the engine closes its connection while the run is held |
| `TestBrokerReleaseDuringDial` | A release that comes between the admission's dial and its end: the dial closes its connection as it ends, while the run is held |
| `TestBrokerReleaseBeforeAdmission` | A release that overtakes a run's admission: the test holds the request until `Close` has stopped the listener and the engine answered the release. The dial gets a real connection to that listener from the test, which cannot get ready; the run is refused on the dial, the connection is closed, and the engine holds no connection and no token |
| `TestBrokerReleaseSharedDial` | Two admissions share a dial that the host lets go while it is under way: the waiting one sees it let go as it wakes, so its interception gets no answer although the host still serves |
| `TestBrokerCancelDuringDial` | Two admissions share a dial that the test holds. The second is paused as it joins, Cancel of its run is answered, and then it goes on: it is refused with `CANCELLED` and lets its token go while the dial is held; the first is then served |

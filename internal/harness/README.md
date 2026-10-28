# internal/harness

**Current state:** milestone C1 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md), with what the
walking skeleton (W1) added. This README covers `internal/harness`. The
package is the engine side of the harness protocol of
[ADR 0004](../../docs/design/0004-harness-protocol.md): frames and part
streams, spawned harness processes with their handshake and supervision,
invocations through settlement, health, the watchdog, and pools. W1 added
the platform frames, with a pluggable handler, and host mode, where the
engine is the child of a Bun host that is also its harness (`AttachHost`).
Milestone C3a added the `warning` frame, which hands an unattributed
process-level error to every invocation in flight.
`Pool` and `Process` implement `engine.StepExecutor`; their users are the
walking skeleton's interpreter in
[internal/engine](../engine/README.md) and its `windmill-lite host` command
in [cmd/windmill-lite](../../cmd/windmill-lite/README.md).

Not here yet:

- `fresh` module state: `Spawn`, `NewPool` and `Execute` refuse it (C3b).
- The facade behind the platform frames: its router, policies and job
  identity (PF1). A process without a handler answers each call with a
  501.
- The `host_call` and `host_reply` frames, which are refused as
  `unknown_type` until F2c adds them.
- Host mode's completion (F3a): drain from the engine's side, the working
  directory checks on the engine's side, `onRunSettled` across the
  connection.
- Mapping an outcome to Windmill's stored value: `engine.StoreOutcome`
  does it from the `engine.StepOutcome` this package returns
  ([internal/engine](../engine/README.md)).

## Files

| File | Contents |
|---|---|
| `message.go` | The message types, `Encode` (canonical payload) and `Decode` |
| `envelope.go` | The canonical envelope writer and JSON string escaping |
| `frame.go` | The length prefix, the size limits, part splitting and reassembly |
| `errors.go` | `ProtocolError` and its codes, shared with the Bun codec |
| `conn.go` | One socket's writer and reader goroutines, and when the last frame arrived |
| `process.go` | `Spawn`, `Process`: the handshake, invocations, settlement, death, the watchdog, platform calls |
| `host.go` | `AttachHost`, `HostConn`: host mode on the engine's side, its runs, and its watchdog |
| `pool.go` | `Pool`: routing, lag, the cap, tainting and recycling, replacement, `Close` |
| `util.go` | The log tail, diagnostics lines, recent reporters, signal names |

## The wire

ADR 0004 §Transport and framing and its message table are the reference;
milestone C1 fixed the details they left open.

- **Frames.** A 4-byte big-endian length, then that many bytes of UTF-8
  JSON, at most `MaxFrame` (1 MiB).
- **Canonical envelopes.** `t` first, then the table's field order, strings
  escaped as `JSON.stringify` escapes them, integers in decimal. Raw values
  (`args`, `body`) are spliced in byte for byte and never re-encoded.
- **Decoding** follows the same rules as the Bun codec. Keys match exactly
  and in any order, and only the message type's own fields are read. A null
  field is absent, except a raw value. An integer is a number with an
  integral value within ±(2^53−1), however it is spelled.
- **Part streams.** A message over `MaxFrame` goes as `part` frames whose
  `chunk` is padded base64 of `PartChunk` (785,664) payload bytes, under a
  stream id of its own. The writer sends every queued whole frame before the
  next part frame and gives part streams turns, so a `cancel` or a `pong`
  waits behind at most one part frame.
- **Order per invocation.** A message about an invocation (`invoke`,
  `cancel`, `result`, `late`, `log`, `warning`) queued while a part stream about the
  same invocation is open waits for that stream's last part, so a `cancel`
  never overtakes the large `invoke` it cancels. Messages about one
  host-mode run (`run`, `run_done`, `run_cancel`) keep their order the same
  way, under a key of their own.
- **Limits.** `DefaultMaxMessage` is 512 MiB; `ProcessConfig.MaxMessage`
  lowers it and the welcome passes it on. It applies to whole frames and to
  reassembled messages. An invocation whose `invoke` would exceed it is not
  dispatched.
- **Protocol errors.** A malformed frame, a broken part stream, a frame in
  the wrong direction or one about an invocation in the wrong state is a
  `ProtocolError`. The `Process` then kills its harness; in host mode it
  ends the connection instead, and with it the engine.
- **Warnings** (C3a). A process-level error that no invocation's context
  raised reaches each invocation in flight as `warning`, with `id`, `code`
  (`unattributed_error`) and `message` (its kind, text and stack), ahead
  of the frame that settles it. The `Process` hands it to the
  invocation's `Reporter.Warning`; one for an invocation that is not in
  flight is dropped with a diagnostics line.
- **Platform calls** (W1). When `ProcessConfig.Facade` names the facade's
  origin, the welcome carries it as `facade`, and the harness turns every
  fetch to that origin into a `platform` frame: `inv` (the invocation whose
  context made it), `method`, `url`, `headers` (an object of strings,
  sorted) and an optional `body`, the body's bytes in padded base64.
  `ProcessConfig.Platform` answers each one on a goroutine of its own, and
  the reply goes back as `platform_reply` with `status` (a final HTTP
  status, 200 to 599), `headers` and an optional `body`. A handler that
  panics or answers another status gets a 502 in its place, so one bad
  answer never ends the harness.
- **Runs** (W1, host mode). A host submits `run` with `id`, `kind` (`flow`
  or `script`), `path`, `args` (raw) and an optional raw `intercept`
  object, module ids to the results that stand in for them (H1's static
  form). The engine answers `run_done` with `status` (a run status) and an
  optional raw `result`; `run_cancel` cancels a run. The run's `id` is its
  id in the engine, so every `invoke` of it carries it as `run`. These
  fields stand in for the JSON mapping of `runner.proto` until milestone F5
  defines it.

## Spawned processes

`Spawn` starts the harness with one end of a socketpair as fd 3 and stdin
on `/dev/null`. stdout and stderr are pipes the engine reads itself, so the
harness's exit is seen at once even when a child of it keeps them open.
They go to `ProcessConfig.Diagnostics` as lines prefixed
`harness pid <pid> stdout:` or `stderr:`. Each process gets
a private working directory (mode 0700) under `WorkRoot`, removed when it
exits. Its environment is `ProcessConfig.Env`, or `BaseEnv()`: the part of
WR-22's base set that needs no later milestone.

**The handshake.** The harness's `hello` must match `Kind`, `Runtime`
(`PinnedBun`, 1.3.10, for Bun), `Bundle` and the protocol version, and its
`pid` must be the process `Spawn` started. Otherwise the welcome carries a
`reject` (`proto_mismatch`, `harness_mismatch`, `runtime_mismatch`,
`bundle_mismatch`, `pid_mismatch`) and `Spawn` returns a `*HandshakeError`.
The pid check refuses a wrapper that forks the harness, which the
watchdog's signal would otherwise miss. An accepted harness gets the
module-state mode, `heartbeat_ms`, `max_message` and, when the engine serves
platform calls, `facade`. `Spawn` refuses a
heartbeat that is not shorter than `KillAfter`, and negative values.

## Invocations and settlement

`Execute` follows the settlement contract of `engine.StepExecutor`:

| What happens | `Execute` returns | Reporter |
|---|---|---|
| refused before dispatch (a bad entry name, the native boundary, fresh mode, an oversize or non-UTF-8 invoke, ctx already done) | the error | `Settled(not_started)` first |
| `result` with `settled` true | the outcome | `Settled(completed)` first |
| `result` with `settled` false (an intercepted `process.exit`, an attributed process-level error) | the outcome | later: the final `Late`, then `Settled(completed)`; or `Settled(process_exited)` |
| ctx ends first | `context.Cause(ctx)`, after sending `cancel` with `engine.CancelReasonOf(ctx)` | later: the final `Late` (`result` or `error`), then `Settled(completed)`; or `Settled(process_exited)` |
| the process dies | `escaped`, with the exit code, or `Signal` and `ExitCode` -1, and the tail | `Settled(process_exited)` first |
| host mode: the host's connection ends | `ErrHostLost` | `Settled(process_exited)` first |

Reporter calls for one invocation run in the order the contract fixes, one
at a time, on a goroutine of their own; `Settled(not_started)` runs on the
caller's before `Execute` returns. The connection's reader never runs a
Reporter, so a slow Reporter neither delays other invocations nor looks like
silence to the watchdog, and a Reporter may call `Execute`. A process stops
taking invocations as soon as its exit or the end of its stream is seen. `log` frames feed an
invocation's tail (the last `TailLength`, 600, characters) while it is in
flight and become late output afterwards. The last `RecentLate` (4,096)
settled invocations per process keep receiving late output of work they
left running (D9).

## Death and the watchdog

A harness that exits fails every in-flight invocation as escaped, with its
exit code or the signal that ended it (WR-4). The engine reads its stdout
and stderr for one more second (`exitReadGrace`), then closes its ends of
them, so a child that inherited them cannot keep the engine's descriptors
or goroutines; `Done` closes after that. The watchdog kills a process
from which no frame has arrived for `KillAfter` (`DefaultKillAfter`, 30 s;
`WatchdogOff` disables it), writes a diagnostics line naming the
invocations in flight, and the pool replaces the process. It measures
silence from received frames only, and the harness pings on its own every
heartbeat (one second, or a quarter of `KillAfter` when shorter), so the
engine's own blocked writes never make a live harness look silent. The
signal goes through the `os.Process` handle, which cannot reach a reused
pid: Go uses a pidfd on Linux and does not signal a reaped child elsewhere.

## Host mode

`AttachHost` serves host mode on the engine's side (ADR 0004 host mode,
milestone W1). The Bun host SDK starts the engine with its socket as file
descriptor 3 (`HostConn`); the host is the harness that every invocation
goes to.

- **Handshake.** As for a spawned harness, plus: the hello must offer the
  `host` capability (else `harness_mismatch`), and its `pid` must be the
  engine's parent process, `getppid()` (else `pid_mismatch`), because that
  is the process the watchdog would signal.
- **Runs.** Each `run` frame starts `HostConfig.Run` on a goroutine of its
  own, with a context that `run_cancel` ends with cause
  `engine.CancelCanceled`. What it returns goes back as `run_done`. Its
  invocations go to the same `Process`.
- **No taint, no recycle.** Abandoned invocations stay in the host until
  they settle, counted in its health; the engine never stops routing to
  the host (D2).
- **Losing the host.** When the connection ends, every invocation in flight
  fails with `ErrHostLost` and settles with the process
  (`process_exited`), every run's context ends with that cause, and `Done`
  closes: the engine has nothing left to serve (host mode item 6).
- **The watchdog** (host mode item 4, review item cl P4-3). It measures
  silence from received frames, as for spawned processes; the host pings
  every heartbeat on its own. After `KillAfter` of silence it writes a line
  to its diagnostics naming the runs and invocations in flight, and sends
  `SIGKILL` to the handshake's pid only if `getppid()` still equals it: once
  the host has exited the engine is reparented, and the pid may belong to
  another process. Then it ends the connection, which ends the engine. It
  reports no run outcome; their only receiver was the host.

## Pools

A `Pool` serves one `Key`: harness class, runtime pin, dependency set and
module-state mode.

- **Size.** `Min` and `Max` default to 1. `Start` spawns `Min` processes;
  otherwise the first `Execute` does.
- **Routing.** The process the run used last, then the least-loaded one. A
  process whose lag exceeds `LagThreshold` (250 ms by default) is used only
  when no other process can take the invocation and none can be started.
- **Cap.** `Cap` bounds invocations in flight per process (deviation D15);
  abandoned ones do not count. None by default. A finished invocation frees
  its slot before its Reporter hears of it, so a Reporter may run another
  step on the same pool.
- **Tainting.** An abandoned invocation taints its process: it gets no new
  invocations and does not count toward `Max`. Once nothing else is in
  flight in it, it is drained (killed after `RecycleTimeout`, 5 s) and
  replaced. If its abandoned invocations settle first, it returns to
  service when the pool is below `Max` without it, counting processes still
  starting; otherwise it is surplus and leaves once idle. A process that
  finishes starting when the pool has reached `Max` is surplus itself. The
  pool applies these rules before an abandoned invocation's Reporter hears
  `Settled(completed)`.
- **Replacement.** A process that dies is replaced up to `Min`; a failed
  start is logged and retried by the next `Execute`.
- **Close.** It refuses new invocations, waits for those in flight until
  its context ends, then drains every process, including one that finishes
  starting afterwards.

## Tests

The shared cases are in [conformance/protocol](../../conformance/README.md).

- `frames_test.go` runs the codec cases of `conformance/protocol/frames.json`
  and, with `-args -update`, rewrites their payloads from Go's encoder.
- `qwa4_test.go` is W1's Q-WA-4 env-view test
  ([Q-WA-4](../../docs/semantics/client-api.md#q-wa-4),
  [WA-31](../../docs/semantics/client-api.md#wa-31)): the stock
  `windmill-client@1.767.0`, by namespace and default import, in steps that
  are not bundled, bundled by `Bun.build` with Windmill's settings, and
  bundled with the define rewrite, plus an unbundled dependency that reads
  `Bun.env`. Sixteen concurrent invocations per variant must each read their
  own variables through `process.env`, `Bun.env`, `import.meta.env`, a copy
  of `process.env`, a variable they write, a process variable half of them
  delete, the dependency and the client's call, whose platform frame must
  be attributed to them; in a process without the variables and in one that
  has them. It needs `WL_WINDMILL_CLIENT`, a directory whose `node_modules`
  holds that client.
- `host_test.go` plays a host on a socketpair: the handshake's parent check,
  a host gone before its hello, runs and their invocations, `run_cancel`, a
  lost host, and the watchdog, which signals its parent and spares a pid
  that no longer is. It also checks the 502 that stands in for a bad
  platform answer.
- `exchange_test.go` plays the engine's side of every transcript in
  `conformance/protocol/exchanges/` against the real Bun harness. This is
  the C1 exit gate: protocol golden tests on Bun 1.3.10. The fixture
  bundle's scripts take named parameters, each with the `.meta.json`
  signature the build helper would record; C3a added the transcripts of
  an intercepted `process.exit` (`exit-caught`, `exit-zero`,
  `exit-from-timer`), an attributed error, the unattributed warning and a
  signature that did not parse.
- `bun_test.go` runs `Process` and `Pool` against the real Bun harness:
  handshake rejections and the pid check, outcomes, process death (through
  `process.reallyExit`, which the harness does not intercept), a
  `process.exit` that ends only its invocation, the attribution of a
  timer's error and the warning of an unattributed rejection, a detached
  rejection that ends nothing, cancel and late settlement, limits,
  the working directory, the watchdog case `watchdog/finite-cpu` in warm
  mode, and the pool rules above.
  `TestBunPoolTaintedProcessReturnsToService` holds its steps on the `gate`
  fixture step, which reports that it has started and ends when the test
  opens it, and turns lag routing off, so it does not depend on timing.
- `fake_test.go` re-runs the test binary as a fake harness for what Bun
  cannot be made to do on demand: a peer that stops reading, a mute peer,
  outcomes fixed before the call ends, late output, protocol violations, an
  exit while a child holds the socket, a slow start, a slow or re-entrant
  Reporter.
- `conn_test.go` checks the writer's ordering rules and the exchange
  matcher.

The Bun tests need the pinned Bun: set `WL_BUN` to its executable, and
`WL_WINDMILL_CLIENT` for the Q-WA-4 test. Without them they skip, unless
`WL_REQUIRE_BUN` is set, as CI sets it.

```sh
WL_BUN=/path/to/bun-1.3.10/bun WL_WINDMILL_CLIENT=/path/to/client go test -race ./internal/harness
```

# internal/harness

**Current state:** milestone C1 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md). This README
covers `internal/harness`. The package is the engine side of the harness
protocol of [ADR 0004](../../docs/design/0004-harness-protocol.md): frames
and part streams, spawned harness processes with their handshake and
supervision, invocations through settlement, health, the watchdog, and
pools. `Pool` and `Process` implement `engine.StepExecutor`. Nothing calls
them yet: the walking skeleton (W1) is the first user.

Not here yet:

- `fresh` module state: `Spawn`, `NewPool` and `Execute` refuse it (C3b).
- Host mode, where the harness is the host and submits runs (W1, F3a).
- The `platform`, `host_call` and `run` frames, which are refused as
  `unknown_type` until PF1, F2c and W1 add them.
- Mapping an outcome to Windmill's stored value (WR-20, WR-21, WR-V2), which
  C3a and N1a do from the `engine.StepOutcome` this package returns.
- Warnings for process-level errors no invocation owned (C3a).

## Files

| File | Contents |
|---|---|
| `message.go` | The message types, `Encode` (canonical payload) and `Decode` |
| `envelope.go` | The canonical envelope writer and JSON string escaping |
| `frame.go` | The length prefix, the size limits, part splitting and reassembly |
| `errors.go` | `ProtocolError` and its codes, shared with the Bun codec |
| `conn.go` | One socket's writer and reader goroutines, and when the last frame arrived |
| `process.go` | `Spawn`, `Process`: the handshake, invocations, settlement, death, the watchdog |
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
  `cancel`, `result`, `late`, `log`) queued while a part stream about the
  same invocation is open waits for that stream's last part, so a `cancel`
  never overtakes the large `invoke` it cancels.
- **Limits.** `DefaultMaxMessage` is 512 MiB; `ProcessConfig.MaxMessage`
  lowers it and the welcome passes it on. It applies to whole frames and to
  reassembled messages. An invocation whose `invoke` would exceed it is not
  dispatched.
- **Protocol errors.** A malformed frame, a broken part stream, a frame in
  the wrong direction or one about an invocation in the wrong state is a
  `ProtocolError`. The `Process` then kills its harness.

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
module-state mode, `heartbeat_ms` and `max_message`. `Spawn` refuses a
heartbeat that is not shorter than `KillAfter`, and negative values.

## Invocations and settlement

`Execute` follows the settlement contract of `engine.StepExecutor`:

| What happens | `Execute` returns | Reporter |
|---|---|---|
| refused before dispatch (a bad entry name, the native boundary, fresh mode, an oversize or non-UTF-8 invoke, ctx already done) | the error | `Settled(not_started)` first |
| `result` with `settled` true | the outcome | `Settled(completed)` first |
| `result` with `settled` false | the outcome | later: the final `Late`, then `Settled(completed)`; or `Settled(process_exited)` |
| ctx ends first | `context.Cause(ctx)`, after sending `cancel` with `engine.CancelReasonOf(ctx)` | later: the final `Late` (`result` or `error`), then `Settled(completed)`; or `Settled(process_exited)` |
| the process dies | `escaped`, with the exit code, or `Signal` and `ExitCode` -1, and the tail | `Settled(process_exited)` first |

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

## Pools

A `Pool` serves one `Key`: harness class, runtime pin, dependency set and
module-state mode.

- **Size.** `Min` and `Max` default to 1. `Start` spawns `Min` processes;
  otherwise the first `Execute` does.
- **Routing.** The process the run used last, then the least-loaded one. A
  process whose lag exceeds `LagThreshold` (250 ms by default) is used only
  when no other process can take the invocation and none can be started.
- **Cap.** `Cap` bounds invocations in flight per process (deviation D15);
  abandoned ones do not count. None by default. A finished or abandoned
  invocation frees its slot before its Reporter hears of it (an abandoned
  one before `Execute` returns), so a Reporter may run another step on the
  same pool.
- **Tainting.** An abandoned invocation taints its process: it gets no new
  invocations and does not count toward `Max`. Once nothing else is in
  flight in it, it is drained (killed after `RecycleTimeout`, 5 s) and
  replaced. If its abandoned invocations settle first, it returns to
  service when the pool is below `Max` without it, counting processes still
  starting; otherwise it is surplus and leaves once idle. A process that
  finishes starting when the pool has reached `Max` is surplus itself. The
  pool applies these rules before an abandoned invocation's Reporter hears
  `Settled(completed)`.
- **Replacement.** A process that dies leaves the pool, which starts its
  replacements up to `Min`, before the Reporters of its invocations hear
  `Settled(process_exited)`; a failed start is logged and retried by the
  next `Execute`.
- **Close.** It refuses new invocations, waits for those in flight until
  its context ends, then drains every process, including one that finishes
  starting afterwards.

## Tests

The shared cases are in [conformance/protocol](../../conformance/README.md).

- `frames_test.go` runs the codec cases of `conformance/protocol/frames.json`
  and, with `-args -update`, rewrites their payloads from Go's encoder.
- `exchange_test.go` plays the engine's side of every transcript in
  `conformance/protocol/exchanges/` against the real Bun harness. This is
  the C1 exit gate: protocol golden tests on Bun 1.3.10.
- `bun_test.go` runs `Process` and `Pool` against the real Bun harness:
  handshake rejections and the pid check, outcomes, process death, cancel
  and late settlement, limits, the working directory, the watchdog case
  `watchdog/finite-cpu` in warm mode, and the pool rules above. A step that
  must still run when the test acts is a `gate` fixture step, which reports
  that it has started and ends when the test opens it, or `spin-gate`,
  which blocks its process's event loop the same way. The cancel, taint,
  recycle and affinity tests hold their steps on gates and cancel only once
  a step runs; the lag test waits for the lag to show; and
  `TestBunPoolTaintClearsWhileTheReplacementStarts` also holds every start
  after the first on a gate file. The affinity and taint tests turn lag
  routing off, which is not under test there, so none of these depends on
  timing.
- `fake_test.go` re-runs the test binary as a fake harness for what Bun
  cannot be made to do on demand: a peer that stops reading, a mute peer,
  outcomes fixed before the call ends, late output, protocol violations, an
  exit while a child holds the socket or stdout, a slow start, a slow or
  re-entrant Reporter, and an abandoned call that settles the moment its
  cancel arrives. It checks that the pool hears of an abandonment and of a
  death before any Reporter does.
- `conn_test.go` checks the writer's ordering rules and the exchange
  matcher.

The Bun tests need the pinned Bun: set `WL_BUN` to its executable. Without
it they skip, unless `WL_REQUIRE_BUN` is set, as CI sets it.

```sh
WL_BUN=/path/to/bun-1.3.10/bun go test -race ./internal/harness
```

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
process-level error to every invocation in flight. Milestone C3b added
`fresh` mode and the `left_behind` warning, whose `Left` counts what an
invocation still had when it settled (the D9 fence). Milestone PF1 made
the platform handler two-phase, so the facade sees every platform frame in
order, and gave each process its facade origin as `BASE_URL`. Milestone
FU6 made a fresh process the leader of its job's process group
([WR-55](../../docs/semantics/languages.md#wr-55)) and completed the base
environment (WR-22/b).
`Pool` and `Process` implement `engine.StepExecutor`; their users are the
walking skeleton's interpreter in
[internal/engine](../engine/README.md) and its `windmill-lite host` command
in [cmd/windmill-lite](../../cmd/windmill-lite/README.md).

Not here yet:

- The facade behind the platform frames is
  [internal/platform/facade](../platform/facade/README.md) (PF1). A
  process without a handler answers each call with a 501.
- The `host_call` and `host_reply` frames, which are refused as
  `unknown_type` until F2c adds them.
- Host mode's completion (F3a): drain from the engine's side, the working
  directory checks on the engine's side, `onRunSettled` across the
  connection.
- Mapping an outcome to Windmill's stored value: `engine.StoreOutcome`
  does it from the `engine.StepOutcome` this package returns
  ([internal/engine](../engine/README.md)).

What this package does carry of the outcome's provenance: an `error`
result whose `origin` is `harness` becomes a `StepOutcome` whose `Origin`
is `engine.OriginHarness` (`process.go`, milestone FU5).

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
  never overtakes the large `invoke` it cancels. A `platform` frame counts
  as a message about the invocation its `inv` names (PF1), so a harness
  never sends a result ahead of a large platform call its invocation made
  before it. Messages about one host-mode run (`run`, `run_done`,
  `run_cancel`) keep their order the same way, under a key of their own.
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
  of the frame that settles it. What the native boundary handed a native
  invocation but does not track comes the same way, with code
  `native_untracked` (`WarningNativeUntracked`, D5 in ADR 0001) and
  `untracked`, its counts by kind (`Warning.Untracked`), just before the
  frame that settles that invocation. The `Process` hands it to the
  invocation's `Reporter.Warning`; one for an invocation that is not in
  flight is dropped with a diagnostics line.
- **Platform calls** (W1, PF1). `ProcessConfig.Facade` is the process's
  facade origin, which it gets as `BASE_URL` (below). A warm process's
  welcome carries it as `facade`, and the harness turns every fetch to that
  origin into a `platform` frame: `inv` (the invocation whose context made
  it), `method`, `url`, `headers` (an object of strings, sorted) and an
  optional `body`, the body's bytes in padded base64. A fresh process's
  welcome does not, so its calls go over loopback TCP to an origin that
  listens ([ADR 0007](../../docs/design/0007-platform-interface.md),
  transports). `ProcessConfig.Platform` is called on the reader, in frame
  order, and returns at once with the function that answers the request,
  which runs on a goroutine of its own; so the facade has seen every
  platform frame that arrived before an invocation's result when `Execute`
  returns it. The reply goes back as `platform_reply` with `status` (a
  final HTTP status, 200 to 599), `headers` and an optional `body`. A
  handler that panics in either part, gives no answer or answers another
  status gets a 502 in its place, so one bad answer never ends the
  harness.
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
WR-22's base set that needs no other milestone. When `ProcessConfig.Facade`
is set, `BASE_URL` is appended with its value, over any value of the same
name, as Windmill's worker sets it last
([WA-2](../../docs/semantics/client-api.md#wa-2)); `BASE_INTERNAL_URL` is
never set for Bun. The engine stands in for the worker
([WR-22](../../docs/semantics/languages.md#wr-22)/b):
`BUN_INSTALL_CACHE_DIR` is the engine's `WINDMILL_DIR` (`/tmp/windmill`
when unset or empty) followed by `/cache_nomount/bun`, and `NODE_PATH` is
the engine's, set only when the engine has one.

**Process groups** ([WR-55](../../docs/semantics/languages.md#wr-55)).
`Spawn` makes every process the leader of a process group of its own, and
killing it signals the whole group as well. A fresh process is its
invocation's job process: the step's children join its group unless they
start a session of their own, and its outcome waits for them (below). In a
warm or host process the harness gives each child an invocation starts
asynchronously a group of its own and announces it in a `child` frame,
written before the step's code goes on
([harnesses/bun](../../harnesses/bun/README.md)). The engine records the
groups per invocation (`invocation.children`) and kills them itself,
without the harness's event loop, which the step may block: when it
abandons the invocation (its timeout or cancellation, in `exec`), when the
process or the host's connection ends (`failInvocations`), and at once
for a group announced after that or after the invocation settled. It
forgets an invocation's groups at its settlement, which killed them in
the harness. A group id outside 2 to 2^31 − 1, which a negated kill target
would narrow to another target (`0`, `-1`, another pid), is refused by the
codec and again before any signal (`validGroupID`), and so is the
harness's own pid. The engine alone signals a child group (the harness
only reports it), and only while it is still the child's (`childGroup`):
each group is held with the members the engine last saw in it, pid and
start time (`groupMembersOf`: one pass over `/proc` on Linux,
`kern.proc.pgrp` on macOS), and with the token of its guardian, the
process the harness starts in the group so that its id stays reserved,
which the engine finds among the members by its arguments
(`processHasGuard`: `/proc/<pid>/cmdline` on Linux, `kern.procargs2` on
macOS, which gives another process's arguments but not its environment).
The token counts as the `sleep` operand, or, for a member other than the
group's leader, as a word of the prologue's script, which the guardian
carries from its fork until its exec (`hasGuardArg`); the leader carries
the script too until it execs the step's command, so it is never taken
for the guardian by it. It looks for the guardian again at each look
until it finds it, and again once the process it found has exited (the
prologue's subshell carries the script as well). A look that finds
neither the guardian nor a member seen before, in a guarded group, while
a member other than the leader runs and reads no arguments
(`processArgsUnread`: as in the middle of an exec, when
`/proc/<pid>/cmdline` reads empty), is `undecided`: the watcher keeps the
group as it was, and `end` looks again, `endRetry` apart, up to
`endTries` times (200 ms), before it gives the group up. A listing is not
taken at one instant (it lists the processes that exist as it starts and
reads each in turn), so the prologue can run its course during the one
`newChildGroup` takes, the leader gone before it is read and the guardian
forked after the listing started; an empty listing is taken again, and
the second shows the guardian.
`continues` takes the group for the original while the guardian is among
its members, or, without one, while a member seen before is still there;
otherwise the group is given up for good and never signalled. A group
that took the id after the original ended has no guardian and shares no
member, so no kill reaches it. `watchChildren` looks at each group every
50 ms in its first second and every 250 ms after: it renews the members,
drops the groups that no longer continue, and releases a group whose
guardian is its only member, killing the guardian and dropping the group.
Every kill of a group, the release's, a cancellation's, a settlement's or
the process's end's, goes through `childGroup.end`, which looks at the
members afresh under the group's lock and ends the group for good: a
group is killed once, by whichever path comes first, and no path signals
its id from a look that another path's kill made out of date.
The engine also kills an invocation's groups at its settlement
(`settleLocked`), when the harness reported them left behind. A group
without a guardian whose members all came and went between two looks is
given up and left running. The process operations are variables the
tests replace (`groupMembers`, `isGuard`, `argsUnread`, `signalGroup`), so no test
sends a real signal to a fake id.
When a warm process ends, `endGroup` kills what is left in its own group:
what the steps' synchronous child calls started, which gets no group of
its own.

**The handshake.** The harness's `hello` must match `Kind`, `Runtime`
(`PinnedBun`, 1.3.10, for Bun), `Bundle` and the protocol version, and its
`pid` must be the process `Spawn` started, and a process spawned in `fresh`
mode must offer the `fresh` capability. Otherwise the welcome carries a
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
| refused before dispatch (a bad entry name, the native boundary to a harness whose `hello` does not offer it or to a process with no facade origin, which a native step reads as `BASE_URL`, a module-state mode other than the process's, a second invocation of a fresh process, an oversize or non-UTF-8 invoke, ctx already done) | the error | `Settled(not_started)` first |
| `result` with `settled` true | the outcome | `Settled(completed)` first |
| `result` with `settled` false (an intercepted `process.exit`, an attributed process-level error) | the outcome | later: the final `Late`, then `Settled(completed)`; or `Settled(process_exited)` |
| ctx ends first | `context.Cause(ctx)`, after sending `cancel` with `engine.CancelReasonOf(ctx)` | later: the final `Late` (`result` or `error`), then `Settled(completed)`; or `Settled(process_exited)` |
| fresh mode: `result` with `settled` true | the outcome, once the process has exited and its group has emptied by itself (WR-55): a background child holds the job open; a kill or a failed connection before that fails the job as `escaped` by `SIGKILL`, settled `process_exited` | `Settled(completed)` first |
| warm or host mode: `child` | nothing; the group is recorded for the invocation, or killed at once when the invocation was abandoned or has settled or the process is ending (WR-55) | none |
| fresh mode: ctx ends first, before the result or while it waits for the group | `context.Cause(ctx)`, after killing the process and its group | later: `Settled(process_exited)`, nothing late |
| fresh mode: the process exits 0 before a result (`process.exit(0)`), over a connection that never failed | `empty` ([WR-V2](../../docs/semantics/languages.md#wr-v2)) | `Settled(process_exited)` first |
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

**The arguments file** ([WR-5](../../docs/semantics/languages.md#wr-5),
milestone PF2). Before it sends an invocation, a spawned process writes
the invocation's arguments (after the engine's `$var:` and `$res:`
resolution) to `args.json` (`ArgsFile`) in its working directory, as
Windmill's worker writes them into a job's directory: without the
worker's internal keys `_MODULES` and `_TEMP_SCRIPT_REFS`, every other
member, underscore keys included, kept as its raw text in its place
(`scriptArgs`, `args.go`), written beside the file and renamed over it,
so a script never reads it half written. Windmill's wrapper binds the
script's parameters from that file, so the invoke frame carries the same
text. A fresh process's file is its one invocation's; a warm
process's working directory is every invocation's (D10), so its file is
the latest invocation's. A host-mode connection has no working directory
of its own and writes none (F3a).

## Death and the watchdog

A harness that exits fails every in-flight invocation as escaped, with its
exit code or the signal that ended it (WR-4). The engine reads its stdout
and stderr for one more second (`exitReadGrace`), then closes its ends of
them, so a child that inherited them cannot keep the engine's descriptors
or goroutines; `Done` closes after that. The watchdog kills a process
from which no frame has arrived for `KillAfter` (`DefaultKillAfter`, 30 s;
`WatchdogOff` disables it), writes a diagnostics line naming the
invocations in flight, and the pool replaces the process. A `fresh`
process has no watchdog: it is its invocation, which only the step's own
timeout ends (case `watchdog/finite-cpu`). It measures
silence from received frames only, and the harness pings on its own every
heartbeat (one second, or a quarter of `KillAfter` when shorter), so the
engine's own blocked writes never make a live harness look silent. The
signal goes through the `os.Process` handle, which cannot reach a reused
pid: Go uses a pidfd on Linux and does not signal a reaped child elsewhere.

A fresh process's group is signalled as well, by its id. While a process is
left in the group, the id is given to no other process; `awaitGroup`,
which holds a fresh result until the group is empty (looking every 20 ms
with signal 0, and reaping the group's members when the engine is their
parent: PID 1, or a subreaper, each by its own pid with `reapMember`, the
process's own pid excepted), marks it gone once it sees it empty, and it
is not signalled again. The only window is the one poll between the group
emptying and that look. When the engine runs as PID 1 of a container
without reaping orphans otherwise, the reaping matters: a zombie stays in
its group.

The same holds for the child groups (`reap.go`; review round 5, finding
1). Every guardian is an orphan from its start, and so is what a member
leaves when it exits, so the engine, as PID 1 or a subreaper, is their
parent. `childGroup.end` hands the members it killed to the package's
reaper (`reapLater`), and so does `endGroup` for the harness's own group,
the harness itself excepted (its `exec.Cmd` waits for it); a goroutine,
which outlives the `Process`, reaps them as they exit, on the watcher's
schedule and at each of its looks (`reapPass`). Before each step it lists
the group again, and takes the listing only while a member it holds is
still in the group afterwards: the group was then never empty, so its id
was never reused, and a process forked as the kill landed is reaped too.
`reapAdopted` reaps, in a group the engine still holds, a member seen at
two looks that has exited, a descendant the engine adopted, so it does
not stay in the group until the group ends. Every wait is
`wait4(pid, WNOHANG)` for one member, made only while `readProc` shows the
process with the start time the engine saw and the engine as its parent:
another child's exit status is never taken, and `ECHILD` means the member
is not the engine's. A member whose parent is another process is kept
until that parent reaps it, or until the engine adopts it. `readProc` and
`reapPID` are variables the tests replace, as are the group operations.

## Host mode

`AttachHost` serves host mode on the engine's side (ADR 0004 host mode,
milestone W1). The Bun host SDK starts the engine with its socket as file
descriptor 3 (`HostConn`); the host is the harness that every invocation
goes to. It passes file descriptor 4 too (`HostChildren`,
`HostConfig.Children`): a channel of `child` frames only, which the host
writes synchronously and `readChildren` reads on a goroutine of its own,
so a step's child group reaches the engine even when the host's socket is
backed up and its event loop then blocks (WR-55).

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

A **fresh** pool (`engine.ModuleStateFresh`,
[ADR 0006](../../docs/design/0006-module-state.md)) starts one process for
each `Execute`, which serves that invocation in a working directory of its
own and exits once it has reported the outcome; one that outlives its
outcome by 5 s is killed. The outcome reaches the caller once no process is
left in the process's group (WR-55). When the engine stops waiting first,
the process and its group are killed and the invocation settles with it.
`Close` waits for the fresh processes until its context ends and kills the
rest; one that finishes starting after `Close` is killed before it runs
anything. None of the rules
below apply to it. The rest of this section is about warm pools.

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
  platform answer, from either part of the handler.
- `exchange_test.go` plays the engine's side of every transcript in
  `conformance/protocol/exchanges/` against the real Bun harness. This is
  the C1 exit gate: protocol golden tests on Bun 1.3.10. The fixture
  bundle's scripts take named parameters, each with the `.meta.json`
  signature the build helper would record; C3a added the transcripts of
  an intercepted `process.exit` (`exit-caught`, `exit-zero`,
  `exit-from-timer`), an attributed error, the unattributed warning and a
  signature that did not parse. C3b added the transcripts of `log`
  frames (ahead of the result; what a step prints never reaches the frame
  channel), of the `left_behind` warning (`left-behind`, and
  `platform-left-behind`, whose late reply is dropped), of a platform
  call refused after settlement, and of `fresh` mode (`welcome-fresh`,
  `fresh-one-invocation`, `fresh-exit`).
- `bun_test.go` runs `Process` and `Pool` against the real Bun harness:
  handshake rejections and the pid check, outcomes, process death (through
  `process.reallyExit`, which the harness does not intercept), a
  `process.exit` that ends only its invocation, the attribution of a
  timer's error and the warning of an unattributed rejection, one whose
  text exceeds a small `MaxMessage` (the warning is cut, and the process
  lives), work after the result reported as a late error that settles
  nothing (a timer's throw and its `process.exit`), a detached rejection
  that ends nothing, cancel and late settlement, limits,
  the working directory, the watchdog case `watchdog/finite-cpu` in warm
  and fresh mode, fresh pools (a process and a directory per invocation,
  a kill on cancel, results of 200 KB and 3 MB and a long log before an
  exit, all written before the process ends, an exit with the step's
  code, Close), a fresh `process.exit` after which no `catch` or
  `finally` block runs, with a short log and with one the socket cannot
  take at once (`TestBunFreshExitRunsNoMoreCode`, whose warm control runs
  both), output of a module's own work after its loading
  invocation settled, and the pool rules above. The late work of `TestBunWorkAfterTheResultIsLate` comes
  from continuations of a `Bun.sleep`, which the fence cannot stop, since
  it clears the timers a step leaves behind. A step that must still run when the test
  acts is a `gate` fixture step, which reports that it has started and ends
  when the test opens it, or `spin-gate`, which blocks its process's event
  loop the same way. The cancel, taint, recycle and affinity tests hold
  their steps on gates and cancel only once a step runs; the lag test waits
  for the lag to show; and `TestBunPoolTaintClearsWhileTheReplacementStarts`
  also holds every start after the first on a gate file. The affinity and
  taint tests turn lag routing off, which is not under test there, so none
  of these depends on timing.
- `children_test.go` runs the real Bun harness on the bundle of
  `testdata/children` (milestone FU6): the base environment's
  `BUN_INSTALL_CACHE_DIR` and `NODE_PATH`, as `BaseEnv` builds them and as
  a step sees them (WR-22/b), and process groups (WR-55). In fresh mode a
  child in the group holds the outcome until it exits, settled completed
  (WR-55/a), a detached child does not, and a timeout while the outcome
  waits kills the group and ends the step with the timeout (WR-55/b), and
  a kill that is not the invocation's own (`Kill`, as a pool's close does)
  fails the held job instead of releasing its result. In warm mode a
  normal return settles at once, its `child` reported and killed with its
  group, a `detached` child reported and left running, and a timeout's
  `cancel` kills the children of the abandoned call. A step that blocks
  its event loop after it started a child (`stall`) has the child killed
  by the engine on its timeout, though the harness cannot handle the
  `cancel`, and with the process when the watchdog kills it; a synchronous
  child call's child ends with the process too. A child that waits for
  all its children before it exits (`spawn-waitall`, this test binary run
  with `WL_WAIT_ALL`, looping on `wait4(-1)`) finishes in both modes, as
  outside the harness: a warm group's guardian is no child of it.
- `reap_linux_test.go` re-runs the test binary as a child subreaper
  (`prctl(PR_SET_CHILD_SUBREAPER)`, skipped where the kernel refuses it)
  and runs the real Bun harness there, so the test process adopts what
  the engine of PID 1 without an init would: repeated short spawns whose
  shell leaves a daemon (`spawn-daemon`), a descendant that exits while
  its group still runs (`spawn-shell`), a cancellation of a stalled step,
  the process's end with a child group or a synchronous child, and a
  fresh job's orphan. Each guardian and child the engine adopted is
  reaped, the descendant before its group ends, and no zombie of the
  test process is left. `childgroup_test.go` covers the reaping rules on
  the fake process table (`TestReapChildGroups`), and the reviewer's probe
  (`TestReviewOwnedGuardianReaped`): a real guardian, the test's own
  child, is reaped once the watcher releases its group.
- `fake_test.go` re-runs the test binary as a fake harness for what Bun
  cannot be made to do on demand: a peer that stops reading, a mute peer,
  outcomes fixed before the call ends, late output, protocol violations, an
  exit while a child holds the socket or stdout, a slow start, a slow or
  re-entrant Reporter, and an abandoned call that settles the moment its
  cancel arrives. It checks that the pool hears of an abandonment and of a
  death before any Reporter does.
- `conn_test.go` checks the writer's ordering rules and the exchange
  matcher.
- `args_test.go` checks `scriptArgs`: the two internal keys dropped, a key
  written with an escape included, every other member's raw text and
  place kept, and arguments with nothing to drop, or that are not an
  object, unchanged.
- The arguments file is checked end to end by the step case
  `conformance/steps/bun/wr-5.yaml` (two runs of one warm process, and
  fresh processes; the internal keys absent from the file and from the
  script's parameters) and by FX-42/h's run `args_file`
  ([cmd/wltest](../../cmd/wltest/README.md)).

The Bun tests need the pinned Bun: set `WL_BUN` to its executable, and
`WL_WINDMILL_CLIENT` for the Q-WA-4 test. Without them they skip, unless
`WL_REQUIRE_BUN` is set, as CI sets it.

```sh
WL_BUN=/path/to/bun-1.3.10/bun WL_WINDMILL_CLIENT=/path/to/client go test -race ./internal/harness
```

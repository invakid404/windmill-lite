# internal/harness

**Current state:** the package runs the engine's side of the
[harness protocol](../../docs/design/0004-harness-protocol.md): frames and
part streams, spawned harness processes with their handshake, invocations
through [settlement](../../docs/glossary.md#settle), process groups, the
watchdog, pools in [`warm`](../../docs/glossary.md#warm) and
[`fresh`](../../docs/glossary.md#fresh) mode,
[host mode](../../docs/glossary.md#host-mode), and
[host plugins](../../docs/glossary.md#host-plugin). Its host-mode
`run`, `run_done` and `run_cancel` frames carry the fields that they
share with `runner.proto`'s messages under the same names, with JSON
values embedded raw where the messages hold `bytes`
([host adapters](../../docs/design/0009-host-adapters.md#the-runner-service)).

This README covers `internal/harness` and its `testdata/`: `bench.ts`, the
Bun side of the microbenchmarks, and `children/`, the bundle of the
process-group tests.

The package is the engine's side of the
[harness protocol](../../docs/design/0004-harness-protocol.md): it starts or
attaches a [harness](../../docs/glossary.md#harness), sends it each
[invocation](../../docs/glossary.md#invocation), and reports how each one
ended and [settled](../../docs/glossary.md#settle). The design record holds
the message table; this README holds the rules that the record leaves to
the code.

- **Callers:** `Pool` and `Process` implement `engine.StepExecutor`
  ([internal/engine](../engine/README.md)). Their user is
  [wl](../../wl/README.md): its pool of spawned processes, and in host
  mode the host's `Process`, which
  `windmill-lite host` opens
  ([cmd/windmill-lite](../../cmd/windmill-lite/README.md#host)).
- **The other side:** the Bun harness runtime
  ([harnesses/bun](../../harnesses/bun/README.md)) speaks the same
  protocol, from the shared cases in
  [conformance/protocol](../../conformance/README.md).
- **Platform calls:** the
  [platform facade](../platform/facade/README.md) answers the `platform`
  frames that a process hands to its handler.
- **Outcomes:** this package returns an `engine.StepOutcome`;
  `engine.StoreOutcome` maps it to Windmill's stored value.

## Files

| File | What it holds |
|---|---|
| `message.go` | The message types, `Encode` (the canonical payload) and `Decode` |
| `envelope.go` | The canonical envelope writer and JSON string escaping |
| `frame.go` | The length prefix, the size limits, part splitting and reassembly |
| `errors.go` | `ProtocolError` and its codes, shared with the Bun codec |
| `conn.go` | One socket's writer and reader goroutines, and when the last frame arrived |
| `process.go` | `Spawn`, `Process`: the handshake, invocations, settlement, death, the watchdog, platform calls, child groups |
| `procgroup_linux.go`, `procgroup_darwin.go`, `procgroup_other.go` | Listing a process group's members and reading a process's arguments, per system |
| `reap.go` | Reaping the members that the engine adopted |
| `args.go` | `scriptArgs`: the text of `args.json` |
| `host.go` | `AttachHost`, `HostConn`, `HostChildren`, `CheckWorkdir`: host mode on the engine's side |
| `hostcall.go` | `Process.HostCall`, `Pool.HostCall`, `Pool.Plugin`, `HostCallError`, `ErrNoPlugin`: a harness's host plugin |
| `target_linux.go`, `target_other.go` | What host mode's watchdog signals: a pidfd of the host on Linux, kill(2) elsewhere |
| `pool.go` | `Pool`: routing, lag, the cap, tainting and recycling, recycle triggers, the bound on live processes, replacement, `Close` |
| `util.go` | The log tail, diagnostics lines and the kill-line budget, the recent settled invocations, the `serial` call queue, the stdout and stderr line writer, signal names |
| `timings.go` | `SetTimesHook`, `InvocationTimes`: each invocation's clock readings, for benchmarks |
| `doc.go` | The package comment |

## Public surface

### Processes and pools

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Spawn`, `ProcessConfig` | func, struct | Starts a harness process and shakes hands with it | `Pool`, `wltest` (`ProcessConfig` also through `PoolConfig.Process` in `wl`) |
| `Process` | struct | One harness process or one host connection. `Execute` runs an invocation; `HostCall` asks its plugin; `Drain`, `Kill`, `Done`, `Bye`, `ExitStatus`; `Ping`, `Health`, `Hello`, `Lag`, `Load`, `Silence`, `PID`, `Dir` | `Pool`, `wl`, `wltest` |
| `NewPool`, `PoolConfig`, `Pool` | func, struct, struct | A set of processes for one `Key`: `Execute`, `Start`, `Close`, `Stats`, `HostCall`, `Plugin`, and `PinRun`, `Unpinned`, `RunEnded`, `ForgetRun` for [pinned runs](#pinned-runs), with `Pin`, `WithPin` and `ErrPinLive`. `PoolConfig.CheckSettings` checks its size, cap, bound on live processes and recycle triggers as `NewPool` does | `wl` |
| `RecycleTriggers` | struct | `PoolConfig.Recycle`: the resident set and the count of distinct scripts that replace a warm process ([Warm pools](#warm-pools)) | `wl` |
| `Key` | struct | What a pool serves: harness class, runtime pin, dependency set, module-state mode | `wl` |
| `ProcessStats` | struct | One process's figures in `Pool.Stats` | tests |
| `Health` | struct | A harness's health, from its heartbeat `ping` or a `pong`; `RunsHeld` counts the runs whose data it holds | tests |
| `RunData`, `NewRunData`, `WithRunData`, `RunDataOf` | struct, funcs | One run's data, which the plugins of the processes it reaches read, carried in the run's context ([Run data](#run-data)); `Release` | `wl` |
| `ExitStatus` | struct | How a process ended: code or signal | tests |
| `BaseEnv` | func | The base environment of a spawned process | `ProcessConfig`'s default, `wltest` |
| `BunCommand` | func | The command line that starts the Bun harness runtime on a bundle, with `--umask` after `SetLaunchUmask` | `wl`, `wltest` |
| `SetLaunchUmask` | func | Makes every harness that `BunCommand` starts afterwards set that umask first ([harnesses/bun](../../harnesses/bun/README.md#start-a-spawned-harness)); without it, a harness keeps the umask that it inherits | `windmill-lite plugin` |
| `PinnedBun`, `ProtocolVersion` | const | `1.4.3`; `1` | `PinnedBun`: `wl`, `internal/bundle`, `wltest`. `ProtocolVersion`: the handshake, tests |
| `DefaultKillAfter`, `WatchdogOff`, `DefaultHandshakeTimeout`, `DefaultRecentLate` | const | 30 s; −1 (no watchdog); 10 s; 4,096 | `ProcessConfig`'s defaults; `windmill-lite host` (`DefaultKillAfter`), `wl`'s host mode (`WatchdogOff`) |
| `DefaultLagThreshold`, `DefaultRecycleTimeout`, `DefaultRecycleSamples` | const | 250 ms; 5 s; 2 | `PoolConfig` defaults |
| `ArgsFile` | const | `args.json` ([The arguments file](#the-arguments-file)) | `Process`, tests |
| `TailLength` | const | 600: the characters of an invocation's log kept for its tail | the package's own log tail; no other package reads it (`engine.TailLength` is the engine's) |
| `ErrPoolClosed` | var | `Execute` after `Close`, and a warm pool's `Start` | tests |
| `HandshakeError` | struct | The error of `Spawn` or `AttachHost` when the welcome rejects the hello | `wl` (host mode) |

### Host mode and host plugins

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `AttachHost`, `HostConfig` | func, struct | Serves a host on its connection, as its engine | `wl` (host mode) |
| `HostConn`, `HostChildren` | func | File descriptors 3 and 4 that the host passed | `windmill-lite host` |
| `CheckWorkdir` | func | The working-directory check of a host's hello; returns a `*Reject` | `AttachHost`, tests |
| `VersionsPair` | func | Whether a host SDK's version pairs with the engine's | `AttachHost` |
| `ErrHostLost` | var | The host's connection ended | tests |
| `HostCallError`, `HostErrorPlugin`, `HostErrorUnknownHook` | struct, const | A plugin's error reply: `plugin_error`, `unknown_hook` | `wl` (`HostErrorPlugin`) |
| `LateHostCallError` | struct | A host call whose context ended first; `Done` closes when its reply comes | `wl` |
| `ErrNoPlugin` | var | The harness loaded no plugin | tests |
| `RunRequest`, `RunDone`, `RunError`, `RunCancel`, `RunSettled` | structs | The host-mode run frames ([Host-mode run frames](#host-mode-run-frames)) | `wl` (the first three) |
| `RunReleased` | struct | A run whose data a process holds has settled ([Run data](#run-data)) | tests |
| `RunFlow`, `RunScript` | const | A run frame's kinds | tests |
| `ClockReal`, `ClockRecord` | const | A run frame's clocks: real waits, or recorded waits | `wl` (`ClockRecord`) |
| `RunDraining`, `RunRefused` | const | A `RunError`'s codes | `wl` |

### Messages and the codec

The package's own `Process` and connection use these types. Outside it,
`wl` checks a host's `Hello` and answers with a `Reject` (`RejectEngine`,
`RejectPlugin`), and re-exports the two working-directory codes of this
package's own host check. The
[platform facade](../platform/facade/README.md) serves `PlatformRequest`
with `PlatformReply` as a `PlatformHandler`, and `wltest check` reads
`WarningLeftBehind` and `WarningNativeUntracked`. The host-mode tests of
`wl` and `cmd/windmill-lite` play a host with `Encode` and `Decode`.

| Name | Kind | What it does |
|---|---|---|
| `Message`, `Encode`, `Decode` | interface, funcs | One protocol message; its canonical payload; a payload decoded |
| `Hello`, `Welcome`, `WelcomeConfig`, `Caps`, `HarnessInfo`, `Reject` | structs | The handshake |
| `Invoke`, `Result`, `Late`, `Cancel`, `Log`, `Warning`, `Child` | structs | An invocation's frames |
| `PlatformRequest`, `PlatformReply`, `PlatformHandler` | structs, func type | A `platform` frame, its `platform_reply`, and the handler that answers it |
| `HostCall`, `HostReply`, `HostError` | structs | A host plugin's call and reply |
| `ReserveRequest`, `ReserveReply` | structs | A step's `wl.reserve` and its answer, which a `Process` gives from the invocation's `engine.Budget`. `Invoke.Budget` says that the invocation has one; without it the runtime sends no `reserve` |
| `Ping`, `Pong`, `Part`, `Drain`, `Bye` | structs | Health, part streams, and the end of a connection |
| `TypeHello`, `TypeWelcome`, `TypeInvoke`, `TypeResult`, `TypeLate`, `TypeCancel`, `TypeLog`, `TypeWarning`, `TypeChild`, `TypePlatform`, `TypePlatformReply`, `TypeRun`, `TypeRunDone`, `TypeRunCancel`, `TypeRunSettled`, `TypeHostCall`, `TypeHostReply`, `TypeReserve`, `TypeReserveReply`, `TypeRunReleased`, `TypePing`, `TypePong`, `TypePart`, `TypeDrain`, `TypeBye` | const | The `t` field of each message type |
| `RejectProto`, `RejectHarness`, `RejectRuntime`, `RejectBundle`, `RejectPID`, `RejectWorkdirNotWritable`, `RejectWorkdirMismatch`, `RejectEngine`, `RejectPlugin`, `RejectVersion` | const | The reject codes ([The handshake](#the-handshake)) |
| `WarningUnattributed`, `WarningLeftBehind`, `WarningNativeUntracked` | const | The warning codes |
| `OutcomeOK`, `OutcomeError`, `OutcomeEscaped`, `OutcomeEmpty`; `LateResult`, `LateError`, `LateLog`; `OriginHarness` | const | A result's outcomes, a late frame's kinds, and the one origin a result names; `engine` spells the same values |
| `MaxFrame`, `DefaultMaxMessage`, `PartChunk` | const | 1 MiB; 512 MiB; 785,664 bytes |
| `ProtocolError`, `IsProtocolError` | struct, func | A protocol error and its code, `CodeFrameEmpty` to `CodeUnexpected` ([Protocol errors](#protocol-errors)) |

### Timings

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `SetTimesHook`, `InvocationTimes` | func, struct | Reports each invocation's clock readings ([The times hook](#the-times-hook)) | the benchmarks of `cmd/windmill-lite` |

## The wire

The [harness protocol](../../docs/design/0004-harness-protocol.md#transport-and-framing)
design record defines the transport and the messages. These are the rules
that it leaves to the code.

- **Frames.** A 4-byte big-endian length, then that many bytes of UTF-8
  JSON, at most `MaxFrame` (1 MiB).
- **Canonical envelopes.** `t` comes first, then the table's field order.
  Strings are escaped as `JSON.stringify` escapes them, and integers are
  in decimal. Raw values (`args`, `body`) are spliced in byte for byte and
  never encoded again.
- **Decoding** follows the same rules as the Bun codec. Keys match exactly
  and in any order, and only the message type's own fields are read. A
  null field is absent, except a raw value. An integer is a number with an
  integral value within ±(2^53−1), however it is spelled.
- **Part streams.** A message over `MaxFrame` goes as `part` frames, under
  a stream id of its own, each `chunk` padded base64. When this package
  splits a message, every part but the last carries `PartChunk` (785,664)
  payload bytes, and the last, with `last` true, the rest. A receiver does
  not require chunks of that size (`PartChunk` is no protocol constant): it
  checks each frame against `MaxFrame` and the message limit, and each
  stream's reassembled size against the message limit.
- **The writer's turns.** The writer sends every queued whole frame before
  the next part frame, and gives part streams turns. So a `cancel` or a
  `pong` waits behind at most one part frame.
- **A sender's own write.** `Execute` queues its `invoke`. Once the
  process's lock is released, it writes the frame itself when the writer is
  idle and nothing else is queued (`flush`). That spares a wake-up of the
  writer goroutine for each step.
- **That write does not block.** What the socket does not take at once
  goes back to the head of the queue, and the writer sends it before
  anything queued after it. So the bytes on the wire are the same.
- **Order per invocation.** A message about an invocation (`invoke`,
  `cancel`, `result`, `late`, `log`, `warning`) waits while a part stream
  about the same invocation is open, until that stream's last part. So a
  `cancel` never overtakes the large `invoke` that it cancels.
- **`platform` frames** count as messages about the invocation that their
  `inv` names. So a harness never sends a result ahead of a large platform
  call that its invocation made before it.
- **Order per host-mode run.** Messages about one run (`run`, `run_done`,
  `run_cancel`, `run_settled`) keep their order the same way, under a key
  of their own.
- **Barriers.** A barrier (`sendAfter`) waits behind every open stream
  whose key has a prefix, and behind what waits about those keys. A
  host-mode `bye` waits so behind every `run_done` queued before it, while
  unrelated small frames are not held.
- **Limits.** `DefaultMaxMessage` (512 MiB) applies to whole frames and
  to reassembled messages; `ProcessConfig.MaxMessage` lowers it, and the
  welcome passes it on. An invocation whose `invoke` would exceed it is not
  dispatched.
- **Protocol errors** ([below](#protocol-errors)). The `Process` kills its
  harness on one. In host mode it ends the connection instead, and with it
  the engine.

### Protocol errors

A `*ProtocolError` carries one of these codes and a message.
`IsProtocolError(err, code)` reports whether `err` wraps one with that
code; an empty `code` matches any code.

| Code (constant) | When |
|---|---|
| `frame_empty` (`CodeFrameEmpty`) | A frame of length 0 |
| `frame_too_large` (`CodeFrameTooLarge`) | A frame longer than `MaxFrame` |
| `truncated` (`CodeTruncated`) | The stream ended inside a frame |
| `invalid_utf8` (`CodeInvalidUTF8`) | A payload, or a string to encode, that is not UTF-8 |
| `invalid_json` (`CodeInvalidJSON`) | A payload that is not one JSON value |
| `not_object` (`CodeNotObject`) | A payload that is JSON but not an object |
| `missing_type` (`CodeMissingType`) | An object without a string `t` |
| `unknown_type` (`CodeUnknownType`) | A `t` that this side does not implement |
| `missing_field` (`CodeMissingField`) | A required field is absent, including a `welcome` with neither `config` nor `reject` and a `run_done` with neither `status` nor `error` |
| `bad_field` (`CodeBadField`) | A field of the wrong JSON type, or a value outside its enumeration or range (such as a `platform_reply` status outside 200 to 599, or a negative part `seq`). Also an invalid combination: a `welcome` with both `config` and `reject`, a `run_done` with both `status` and `error`, a `host_reply` with both `payload` and `error`, or a late `log` that settles |
| `part_sequence` (`CodePartSequence`) | A part stream that does not start at `seq` 0, a gap or a repeat, or a `seq` 0 for a stream already open |
| `part_in_part` (`CodePartInPart`) | A part stream that reassembles to another `part` frame |
| `bad_chunk` (`CodeBadChunk`) | A part's `chunk` that is empty or not padded standard base64; or the `body` of a `platform` or `platform_reply` frame that is not padded standard base64 (an empty body is allowed) |
| `message_too_large` (`CodeMessageTooLarge`) | A whole frame or a reassembled message over the maximum message size |
| `unexpected_message` (`CodeUnexpected`) | A message in the wrong direction, before the handshake, or about an invocation in the wrong state |

### Warnings

- **Unattributed errors.** A process-level error that no invocation's
  context raised reaches each invocation in flight as `warning`, ahead of
  the frame that settles it. It has `id`, `code` (`unattributed_error`) and
  `message` (its kind, text and stack).
- **Untracked native work.** What the native boundary handed a native
  invocation but does not track comes the same way, just before the frame
  that settles that invocation. Its code is `native_untracked`
  (`WarningNativeUntracked`), and `untracked` counts it by kind
  (`Warning.Untracked`). Native steps run in the Bun harness under a native
  boundary mode, a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **Delivery.** The `Process` hands a warning to the invocation's
  `Reporter.Warning`. One for an invocation that is not in flight is
  dropped, with a diagnostics line.

### Platform calls

- **The facade's origin.** `ProcessConfig.Facade` is the process's facade
  origin, which it gets as `BASE_URL`
  ([The base environment](#the-base-environment)).
- **`warm`.** A warm process's welcome carries the origin as `facade`. The
  harness turns every fetch to that origin into a `platform` frame: `inv`
  (the invocation whose context made it), `method`, `url`, `headers` (an
  object of strings, sorted) and an optional `body`, the body's bytes in
  padded base64.
- **`fresh`.** A fresh process's welcome does not carry it, so its calls go
  over loopback TCP to an origin that listens
  ([platform interface](../../docs/design/0007-platform-interface.md)).
- **Real Windmill instances.** `ProcessConfig.ForeignWindmillOrigins` and
  `HostConfig.ForeignWindmillOrigins` name their origins. The
  harness's fetch view drops the invocation's own job ids from a step's
  dispatch to them ([wl](../../wl/README.md#job-ids-on-a-real-windmill)).
  The package carries the list as it is given, and `wl` checks it.
- **The handler.** `ProcessConfig.Platform` is called on the reader, in
  frame order. It returns at once with the function that answers the
  request, which runs on a goroutine of its own. So the facade has seen
  every platform frame that arrived before an invocation's result when
  `Execute` returns it.
- **The reply** goes back as `platform_reply`, with `status` (a final HTTP
  status, 200 to 599), `headers` and an optional `body`.
- **A bad answer.** A handler that panics in either part, gives no answer,
  or answers another status gets a 502 in its place. So one bad answer
  never ends the harness.
- **No handler.** A process without a handler answers each call with a 501,
  `windmill-lite: this engine serves no platform calls`.

### Host-mode run frames

A host submits runs to the engine over its connection
([Host mode](#host-mode)). The
[message table](../../docs/design/0004-harness-protocol.md#messages) of
the harness protocol design record lists the fields of `run`, `run_done`,
`run_cancel` and `run_settled`. This package adds:

- **Go types.** `RunRequest` is a `run` frame and `RunDone` a `run_done`.
  A `run_done` for a run that did not start carries a `RunError` with
  `RunDraining` or `RunRefused` as its code, and no `status`.
- **Deadlines.** `RunRequest.DeadlineMS` and `PreWriteDeadlineMS` are
  `nil` when the frame has no such field. Any integer that the frame rules
  allow decodes, and both codecs write a present value again, 0 and
  negative ones included: a value of 0 or less is a deadline that has
  passed, which the engine's `Run` ends alone.
- **Cancellation.** `RunDone.CancelCause` is a canceled run's cause, which
  `run_done` writes last as `cancel_cause`. A `run_cancel` may carry
  `reason` and `canceler`, which the cancellation's error names. An empty
  `cancel_cause`, `reason` or `canceler` counts as absent, in both codecs.
- **`may_have_mutated`.** Both codecs write it in `run_done` only when it
  is true.
- **A run's identity.** `RunRequest.Identity` is the run frame's raw
  `identity`, which both codecs require to be an object; `wl` decodes its
  fields.
- **Ids.** The run's `id` is its id in the engine, so every `invoke` of
  the run carries it as `run`.
- **The hello.** A host's hello names its working directory as `workdir`,
  after `pid`.

## Spawned processes

- **Descriptors.** `Spawn` starts the harness with one end of a socketpair
  as file descriptor 3, and stdin on `/dev/null`.
- **Output.** stdout and stderr are pipes that the engine reads itself. So
  the harness's exit is seen at once, even when a child of it keeps them
  open. They go to `ProcessConfig.Diagnostics` as lines prefixed
  `harness pid <pid> stdout:` or `stderr:`.
- **Working directory.** Each process gets a private working directory
  (mode `0700`) under `WorkRoot`, removed when it exits.

### The base environment

A process's environment is `ProcessConfig.Env`, or `BaseEnv()`: the base
set that Windmill's worker gives a Bun job
([WR-22](../../docs/semantics/languages.md#wr-22)), as far as the engine
stands in for the worker.

| Variable | Value |
|---|---|
| `PATH` | The engine's |
| `HOME` | The engine's, or `/tmp` |
| `TZ` | The engine's, when it has one |
| `FORCE_COLOR`, `DO_NOT_TRACK` | `1` |
| `BUN_INSTALL_CACHE_DIR` | The engine's `WINDMILL_DIR` (`/tmp/windmill` when unset or empty), followed by `/cache_nomount/bun` |
| `BUN_RUNTIME_TRANSPILER_CACHE_PATH` | `0` |
| `NODE_PATH` | The engine's, set only when the engine has one |
| `BASE_URL` | `ProcessConfig.Facade`, when set. It is appended over any value of the same name, as Windmill's worker sets it last ([WA-2](../../docs/semantics/client-api.md#wa-2)) |
| `BASE_INTERNAL_URL` | Never set for Bun |

### Process groups

`Spawn` makes every process the leader of a process group of its own, and
killing it signals the whole group as well
([WR-55](../../docs/semantics/languages.md#wr-55)).

#### Which groups the engine kills

- **A fresh process** is its invocation's job process. The step's children
  join its group unless they start a session of their own, and its outcome
  waits for them ([Invocations and settlement](#invocations-and-settlement)).
- **A warm or host process.** The harness gives each child that an
  invocation starts asynchronously a group of its own. It announces the
  group in a `child` frame, written before the step's code goes on
  ([harnesses/bun](../../harnesses/bun/README.md)).
- **Recording.** The engine records the groups per invocation
  (`invocation.children`). It kills them itself, without the harness's
  event loop, which the step may block.
- **When it kills them:** when it abandons the invocation (its timeout or
  cancellation, in `exec`); when the process or the host's connection ends
  (`failInvocations`); and at once for a group announced after that, or
  after the invocation settled.
- **At settlement.** At an invocation's settlement (`settleLocked`), the
  engine kills every group that the invocation announced, before the
  Reporter hears `Settled`, and forgets them.
- **A warm process's own group.** When a warm process ends, `endGroup`
  kills what is left in the process's own group: what the steps'
  synchronous child calls started, which gets no group of its own.
- **Refused ids.** A group id outside 2 to 2^31 − 1 is refused by the codec
  and again before any signal (`validGroupID`), and so is the harness's own
  pid. A negated kill target would narrow such an id to another target
  (`0`, `-1`, another pid).

#### Holding a group: members and the guardian

The engine alone signals a child group; the harness only reports it. It
signals the group only while the group is still the child's
(`childGroup`).

- **Members.** Each group is held with the members that the engine last
  saw in it, by pid and start time (`groupMembersOf`: one pass over `/proc`
  on Linux, `kern.proc.pgrp` on macOS).
- **The guardian.** The harness starts a guardian process in the group, so
  that the group's id stays reserved. The engine holds the guardian's token
  too. It finds the guardian among the members by its arguments
  (`processHasGuard`: `/proc/<pid>/cmdline` on Linux, `kern.procargs2` on
  macOS, which gives another process's arguments but not its environment).
- **The token** counts as the `sleep` operand. For a member other than the
  group's leader, it also counts as a word of the prologue's script, which
  the guardian carries from its fork until its exec (`hasGuardArg`). The
  leader carries the script too until it execs the step's command, so it
  is never taken for the guardian by it.
- **Looking again.** The engine looks for the guardian again at each look
  until it finds it, and again once the process that it found has exited.
  The prologue's subshell carries the script as well.
- **Undecided looks.** A look in a guarded group can find neither the
  guardian nor a member seen before, while a member other than the leader
  runs and reads no arguments (`processArgsUnread`, as in the middle of an
  exec, when `/proc/<pid>/cmdline` reads empty). That look is `undecided`:
  the watcher keeps the group as it was. `end` looks again, `endRetry`
  apart, up to `endTries` times (200 ms), before it gives the group up.
- **Listings are not instant.** A listing lists the processes that exist
  as it starts, and reads each in turn. So the prologue can run its course
  during the listing that `newChildGroup` takes: the leader gone before it
  is read, and the guardian forked after the listing started. An empty
  listing is taken again, and the second shows the guardian.

#### Watching and ending a group

- **`continues`** takes the group for the original while the guardian is
  among its members, or, without one, while a member seen before is still
  there. Otherwise the group is given up for good, and never signalled.
- **A reused id.** A group that took the id after the original ended has no
  guardian and shares no member, so no kill reaches it.
- **`watchChildren`** looks at each group every 50 ms in its first second,
  and every 250 ms after. It renews the members, and drops the groups that
  no longer continue. It releases a group whose guardian is its only
  member: it kills the guardian and drops the group.
- **One kill per group.** Every kill of a group (the release's, a
  cancellation's, a settlement's or the process's end's) goes through
  `childGroup.end`. That looks at the members afresh under the group's
  lock, and ends the group for good. So a group is killed once, by
  whichever path comes first, and no path signals its id from a look that
  another path's kill made out of date.
- **A group left running.** A group without a guardian whose members all
  came and went between two looks is given up, and left running.
- **Tests.** The process operations are variables that the tests replace
  (`groupMembers`, `isGuard`, `argsUnread`, `signalGroup`), so no test
  sends a real signal to a fake id.

### The handshake

The harness's `hello` must match the process's `Kind`, `Runtime`
(`PinnedBun`, 1.4.3, for Bun), `Bundle` and the protocol version. Its
`pid` must be the process that `Spawn` started. A process spawned in
`fresh` mode must offer the `fresh` capability. Otherwise the welcome
carries a `reject`, and `Spawn` returns a `*HandshakeError`:

| Reject code | Constant | When |
|---|---|---|
| `proto_mismatch` | `RejectProto` | Another protocol version |
| `harness_mismatch` | `RejectHarness` | Another harness kind, or a missing capability (`fresh`, or `host` in host mode) |
| `runtime_mismatch` | `RejectRuntime` | Another runtime version |
| `bundle_mismatch` | `RejectBundle` | Another bundle |
| `pid_mismatch` | `RejectPID` | Another pid: a wrapper that forks the harness, which the watchdog's signal would miss |
| `plugin_mismatch` | `RejectPlugin` | Another plugin declaration ([Host plugins](#host-plugins)) |
| `version_mismatch`, `workdir_not_writable`, `workdir_mismatch`, `engine_refused` | `RejectVersion`, `RejectWorkdirNotWritable`, `RejectWorkdirMismatch`, `RejectEngine` | Host mode only ([Host mode](#host-mode)) |

An accepted harness gets the module-state mode, `heartbeat_ms`,
`max_message` and, for a warm or host-mode process with a facade origin,
`facade`. When `ForeignWindmillOrigins` names any, the welcome of every
module state also carries them as `foreign_windmill_origins`, an array of
strings ([Platform calls](#platform-calls)). Both codecs leave an empty
list out, and decode an empty or null one as none.
`Spawn` refuses a heartbeat that is not shorter than `KillAfter`, and
negative values.

## Invocations and settlement

`Execute` follows the settlement contract of `engine.StepExecutor`
([internal/engine](../engine/README.md#settlement)):

| What happens | `Execute` returns | Reporter |
|---|---|---|
| Refused before dispatch (below) | The error | `Settled(not_started)` first |
| `result` with `settled` true | The outcome | `Settled(completed)` first |
| `result` with `settled` false: an intercepted `process.exit`, or an attributed process-level error | The outcome | Later: the final `Late`, then `Settled(completed)`; or `Settled(process_exited)` |
| ctx ends first | `context.Cause(ctx)`, after sending `cancel` with `engine.CancelReasonOf(ctx)` | Later: the final `Late` (`result` or `error`), then `Settled(completed)`; or `Settled(process_exited)` |
| `fresh`: `result` with `settled` true | The outcome, once the process has exited and its group has emptied by itself. A background child holds the job open. A kill or a failed connection before that fails the job as `escaped` by `SIGKILL`, settled `process_exited` | `Settled(completed)` first |
| `warm` or host mode: `child` | Nothing. The group is recorded for the invocation, or killed at once when the invocation was abandoned or has settled, or the process is ending | None |
| `fresh`: ctx ends first, before the result or while the result waits for the group | `context.Cause(ctx)`, after killing the process and its group | Later: `Settled(process_exited)`, nothing late |
| `fresh`: the process exits 0 before a result (`process.exit(0)`), over a connection that never failed | `empty` ([WR-V2](../../docs/semantics/languages.md#wr-v2)) | `Settled(process_exited)` first |
| The process dies | `escaped`, with the exit code, or `Signal` and `ExitCode` −1, and the tail | `Settled(process_exited)` first |
| Host mode: the host's connection ends | `ErrHostLost`. The run's context has ended with that cause before the invocation fails, so the run reads it whichever wakes it first | `Settled(process_exited)` first |

An invocation is refused before dispatch for any of these:

- an empty id, no arguments, or an id already in flight;
- a bad entry name, or an unknown boundary;
- the native boundary, to a harness whose `hello` does not offer it, or to
  a process with no facade origin, which a native step reads as
  `BASE_URL`;
- a module-state mode other than the process's;
- a second invocation of a fresh process;
- an oversize or non-UTF-8 `invoke`;
- `around` wrappers, when the harness loaded no host plugin;
- a process that no longer serves, or an `args.json` that could not be
  written;
- a context that has already ended.

More rules:

- **The origin of an error.** An `error` result whose `origin` is
  `harness` becomes a `StepOutcome` whose `Origin` is
  `engine.OriginHarness` (`process.go`).
- **Reporter calls** for one invocation run in the order that the contract
  fixes, one at a time, on a goroutine of their own.
  `Settled(not_started)` runs on the caller's goroutine before `Execute`
  returns.
- **The reader never runs a Reporter.** So a slow Reporter neither delays
  other invocations nor looks like silence to the watchdog, and a Reporter
  may call `Execute`.
- **The end of a process.** A process stops taking invocations as soon as
  its exit, or the end of its stream, is seen.
- **The log tail.** `log` frames feed an invocation's tail (the last
  `TailLength`, 600, characters) while it is in flight, and become late
  output afterwards.
- **Late output of settled invocations.** The last `RecentLate` (4,096)
  settled invocations per process keep receiving late output of work that
  they left running. Work that a step leaves running in a shared process
  is cut off only as far as the
  [settle-time fence](../../docs/glossary.md#settle-time-fence) reaches, a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **Letting go.** A run's Reporters let go of the run once it has settled
  ([internal/engine](../engine/README.md#settlement)), and an invocation's
  call queue keeps no call that it has run, its outcome's delivery
  included. So the table of settled invocations keeps no finished run's
  values (`TestSerialKeepsNoCallItRan`).
- **The result's hand-off.** A result's calls may end with the outcome
  that `Execute` waits for while none of the invocation's calls is running.
  The reader then hands them to the goroutine waiting in `Execute`, which
  runs them in the same order, outside the process's lock, up to the
  outcome. Anything queued behind them goes on, on a goroutine of its own;
  the hand-off saves starting and waking a goroutine for each step.

### The arguments file

Before it sends an invocation, a spawned process writes the invocation's
arguments to `args.json` (`ArgsFile`) in its working directory
([WR-5](../../docs/semantics/languages.md#wr-5)). Windmill's worker writes
a job's arguments into its directory the same way, and its wrapper binds
the script's parameters from that file. So the `invoke` frame carries the
same text.

- **What it holds:** the arguments after the engine's `$var:` and `$res:`
  resolution, without the worker's internal keys `_MODULES` and
  `_TEMP_SCRIPT_REFS` (`scriptArgs`, `args.go`). Every other member,
  underscore keys included, keeps its raw text and its place.
- **Text that is not one whole object,** closed by its own brace with
  nothing after it, is kept as it is, for the harness to refuse. Dropping a
  key would leave the rest looking whole.
- **The walk.** `scriptArgs` walks the arguments (`jsonval.Members`) only
  when their text holds one of the two names or a `\u` escape, the only
  ways that a key can spell them. Otherwise nothing can be dropped.
- **Atomic writes.** The file is written beside its final name, and renamed
  over it, so a script never reads it half written.
- **Whose file.** A fresh process's file is its one invocation's. A warm
  process's working directory is every invocation's, so its file is the
  latest invocation's. One working directory per warm process is a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **Host mode.** A host-mode connection writes the file in the host's
  working directory, which the handshake checked (`HostConfig.Workdir`),
  and never removes that directory.
- **Leaving it out.** `HostConfig.SkipArgsFile` leaves every invocation's
  file out in host mode. `wl` sets it only for a bundle none of whose
  scripts can read the file (`no_args_file_reader`), when its host opted
  in.

## Death and the watchdog

A harness that exits fails every invocation in flight as `escaped`, with
its exit code or the signal that ended it
([WR-4](../../docs/semantics/languages.md#wr-4)).

### The end of a harness

- **The end of its stream** is EOF or `ECONNRESET` (`endOfStream`), and
  neither is a failed connection: the exit status says whether the process
  failed.
- **Why `ECONNRESET` is not a failure.** Linux ends a socket's reads with
  `ECONNRESET` when its peer closed it with data that it had not read. A
  fresh harness does that when it exits right after its result while a
  `pong` to its heartbeat is on its way. Taking `ECONNRESET` for a failure
  would discard the job's held result as killed by `SIGKILL`.
- **Reading after the exit.** A child of the harness may hold its socket
  open and keep writing to it. So once the harness has exited, the reader
  waits for more bytes at most `exitReadGrace` (1 s), in one read and in
  all (`conn.endReads`). Reading bytes already in the socket takes almost
  nothing from that second.
- **Waiting for the exit.** The Bun runtime ends its socket before it
  exits, so once the stream has ended the engine waits up to `endExitWait`
  (10 s) for the exit. A process still running then is killed alone,
  through its handle. When that kill is what ended it (`SIGKILL`), its
  invocations fail as escaped by `SIGKILL`, and its group is killed too.
- **After that kill**, a fresh job settles at most the same bound later.
  Its group is still waited for and reaped as before, until it is empty or
  the caller's context ends, as at `main`. A cancellation or a timeout
  before the settlement ends the wait too, as without the kill.
- **When the caller's context ends first**, the group is killed, as a
  cancellation kills it at `main`, and is no longer waited for. A caller
  that ends its context, even by releasing it once `Execute` has returned,
  so gives up the reaping of a later orphan.
- **A process that exited by itself** before that kill keeps its own exit
  status, and so does a fresh result held for its group. A `SIGKILL` sent
  by something else at that moment cannot be told from the engine's.
- **Why one grace, not a deadline at the exit.** The frames that the
  harness wrote before it exited are read however long the reader takes. A
  single deadline at the exit would cut off those still in the socket's
  buffer.
- **A kill after the exit,** such as a drain that timed out, ends the
  reader at once (`conn.stopReads`).
- **stdout and stderr.** After a harness exits, the engine reads its stdout
  and stderr for 1 s more (`exitReadGrace`), then closes its ends of them.
  So a child that inherited them cannot keep the engine's descriptors or
  goroutines. `Done` closes after that.

### Kill lines

Every kill but the watchdog's, which reports its own, is named in the
diagnostics (`killing the harness: <reason>`). The line is written after
the signal, on a goroutine of its own, because the diagnostics writer can
block: a full stderr pipe, or a host's logger. Neither a kill nor a fresh
invocation's cancellation, which kills its process, waits for it.

- Each process writes at most one kill line.
- At most 64 kill lines (`maxPendingKillLines`) wait for their writers at
  once, across every process in the program. So a writer that stays blocked
  holds at most 64 goroutines.
- Past that bound a kill's line is dropped. The next kill line that the
  bound admits ends with the count:
  `(<n> earlier kill lines were dropped while 64 waited for blocked diagnostics writers)`.

### The watchdog

The watchdog kills a process from which no frame has arrived for
`KillAfter` (`DefaultKillAfter`, 30 s; `WatchdogOff` disables it). It
writes a diagnostics line that names the invocations in flight, and the
pool replaces the process.

- **`fresh`.** A fresh process has no watchdog: it is its invocation,
  which only the step's own timeout ends (the case `watchdog/finite-cpu`).
- **Silence** is measured from received frames only. The harness pings on
  its own every heartbeat (1 s, or a quarter of `KillAfter` when shorter).
  So the engine's own blocked writes never make a live harness look
  silent.
- **The signal** goes through the `os.Process` handle, which cannot reach a
  reused pid: Go uses a pidfd on Linux, and does not signal a reaped child
  elsewhere.

### Reaping

When the engine runs as PID 1 of a container, without anything else that
reaps orphans, or as a subreaper, it reaps what it adopts. A zombie stays
in its group, so reaping keeps group ids from being held.

- **A fresh process's group** is signalled as well, by its id. While a
  process is left in the group, the id is given to no other process.
- **`awaitGroup`** holds a fresh result until the group is empty. It looks
  every 20 ms with signal 0, and reaps the group's members when the engine
  is their parent (PID 1, or a subreaper), each by its own pid with
  `reapMember`, the process's own pid excepted. It marks the group gone
  once it sees it empty, and the group is not signalled again. The only
  window is the one poll between the group emptying and that look.
- **Child groups** (`reap.go`). Every guardian is an orphan from its
  start, and so is what a member leaves when it exits. So the engine, as
  PID 1 or a subreaper, is their parent.
- **Who hands members over.** `childGroup.end` hands the members that it
  killed to the package's reaper (`reapLater`). `endGroup` does the same
  for the harness's own group, the harness itself excepted, since its
  `exec.Cmd` waits for it.
- **The reaper** is a goroutine that outlives the `Process`. It reaps the
  members as they exit, on the watcher's schedule and at each of its looks
  (`reapPass`).
- **Listing before each step.** Before each step it lists the group again,
  and takes the listing only while a member that it holds is still in the
  group afterwards. The group was then never empty, so its id was never
  reused, and a process forked while the kill was sent is reaped too.
- **`reapAdopted`** reaps, in a group that the engine still holds, a member
  seen at two looks that has exited: a descendant that the engine adopted.
  So that member does not stay in the group until the group ends.
- **Every wait** is `wait4(pid, WNOHANG)` for one member, made only while
  `readProc` shows the process with the start time that the engine saw and
  the engine as its parent. So another child's exit status is never taken,
  and `ECHILD` means that the member is not the engine's.
- **A member whose parent is another process** is kept until that parent
  reaps it, or until the engine adopts it.
- **Tests.** `readProc` and `reapPID` are variables that the tests replace,
  as are the group operations.

## Host mode

`AttachHost` serves host mode on the engine's side, as the harness
protocol's host mode defines it; the [wl](../../wl/README.md) runtime calls
it. The [Bun host SDK](../../docs/glossary.md#bun-host-sdk) starts the
engine and is the harness that every invocation goes to
([harnesses/bun](../../harnesses/bun/README.md#host-mode-and-the-host-sdk)).

- **File descriptor 3** (`HostConn`) is the host's socket.
- **File descriptor 4** (`HostChildren`, `HostConfig.Children`) is a channel
  of `child` frames only. The host writes it synchronously, and
  `readChildren` reads it on a goroutine of its own. So a step's child
  group reaches the engine even when the host's socket is backed up and its
  event loop then blocks ([WR-55](../../docs/semantics/languages.md#wr-55)).
- **`HostChildren` is called before `HostConn`.** With no file descriptor
  4, the socket's duplicate could take that number.

### The host's handshake

As for a spawned harness ([The handshake](#the-handshake)), plus:

- **The `host` capability.** The hello must offer it, else
  `harness_mismatch`.
- **The version pairing.** Its harness version, the host SDK's, must pair
  with the engine's (`VersionsPair`, on the shared cases of
  `conformance/protocol/host/versions.json`), else `version_mismatch`
  (`RejectVersion`; [host adapters](../../docs/design/0009-host-adapters.md)).
- **The parent.** With the watchdog on, its `pid` must be the engine's
  parent process, `getppid()`, else `pid_mismatch`, because that is the
  process that the watchdog would signal. With the watchdog off nothing is
  signalled, so the check does not apply: `wltest check --mode host` keeps
  the engine out of the host's process tree.
- **The working directory.** With `HostConfig.Workdir`, the directory that
  the host passed must be a directory that the engine can write, else
  `workdir_not_writable`. It must be the hello's `workdir` once both are
  resolved without symbolic links, else `workdir_mismatch`.
- **`CheckWorkdir`** makes that check, on the shared cases of
  `conformance/protocol/host/workdir.json`, which the host SDK runs too.
- **`Dir`.** The directory is the process's `Dir` from the moment the
  handshake publishes the serving state, before the welcome goes out,
  since the host may submit a run as soon as it reads it.
- **Refusing a host.** `HostConfig.Refuse` refuses the host whatever its
  hello says (`RejectEngine`, `engine_refused`), when the engine cannot
  open the bundle.

### Runs, drain and the end of the host

- **Runs.** Each `run` frame starts `HostConfig.Run` on a goroutine of its
  own, with a context that `run_cancel` ends with the cause
  `engine.CancelCanceled`, or an `*engine.Cancellation` of its `reason`
  and `canceler` when it has either. What it returns goes back as
  `run_done`, and `run_settled` follows when its `Settled` channel closes.
  Its invocations go to the same `Process`.
- **A host's invocations** carry `HostConfig.Facade` as `BASE_URL` in their
  `env`, since the host was not spawned with the base environment
  ([WA-2](../../docs/semantics/client-api.md#wa-2)). Their arguments go to
  `args.json` in the host's working directory.
- **Drain.** A host sends `drain`; from a spawned harness it is a protocol
  error. Every later `run` is answered with a `run_done` whose `error` is
  `draining`. Once no run is in flight, the engine sends `bye`, behind
  every `run_done` queued before it, whole, however large (`sendAfter`,
  prefix `run:`).
- **No taint, no recycle.** Abandoned invocations stay in the host until
  they settle, counted in its health. The engine never stops routing to
  the host: an abandoned invocation is not killed, a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **Losing the host.** When the connection ends, every invocation in flight
  fails with `ErrHostLost`, and settles with the process
  (`process_exited`). Every run's context ends with that cause, and `Done`
  closes: the engine has nothing left to serve.

### The host's watchdog

- **Silence** is measured from received frames, as for spawned processes;
  the host pings every heartbeat on its own.
- **After `KillAfter` of silence,** it writes a line to its diagnostics
  that names the runs in flight, and the invocations too while the host is
  still its parent. It sends `SIGKILL` to the
  handshake's pid only if `getppid()` still equals it: once the host has
  exited, the engine is reparented, and the pid may belong to another
  process.
- **The signal on Linux** goes through a pidfd of the host, opened at the
  handshake while the host was known to be the parent (`target_linux.go`).
  So the moment between the check and the signal cannot reach a process
  that took the pid. Elsewhere that moment remains.
- **Then** it ends the connection, which ends the engine. It reports no run
  outcome, since their only receiver was the host.

## Host plugins

A harness that loaded a [host plugin](../../docs/glossary.md#host-plugin)
says so in its hello. `Hello.Plugin` is the plugin's declaration: a raw
JSON object of the hooks that it answers and their selectors. The engine's
user, the [wl](../../wl/README.md) runtime, checks it and turns it into
hooks ([harnesses/bun](../../harnesses/bun/README.md#host-plugins)).

- **The handshake.** `ProcessConfig.ExpectPlugin` holds a process to a
  declaration, byte for byte, else `plugin_mismatch` (`RejectPlugin`). A
  `Pool` records the declaration of the first process that starts with
  one, and holds each spawn that begins after that to it.
- **In a pool with settings** ([Pools](#pools)), the first process that
  the pool admits sets the declaration, or its absence. Each later
  admission must carry the same, which also holds spawns that were under
  way: one that differs is killed, and its start fails. A failed start
  sets nothing.
- **Host mode.** `HostConfig.Check` is the engine's own check of the hello,
  before the welcome. The runtime refuses there a host whose declaration it
  cannot use.
- **Calls.** `Process.HostCall(ctx, hook, run, site, payload)` sends a
  `host_call` (`id` `hc<n>`, `hook`, `run`, `site`, raw `payload`), and
  waits for its `host_reply`. It returns the reply's raw `payload`, or its
  `error` as a `*HostCallError` (`plugin_error`, `unknown_hook`).
- **When ctx ends first,** it returns a `*LateHostCallError`, which unwraps
  to ctx's cause. Its `Done` closes once the hook's reply has come (and been
  dropped), or the harness has exited, so that the run's settlement can wait
  for the hook.
- **Other errors:** `ErrHostLost` (host mode) or an exit error when the
  connection ends; `ErrNoPlugin` for a harness without a plugin.
- **Calls that the engine stopped waiting for** are remembered until their
  reply comes, with no bound. A reply to no call, a second reply to a call,
  and a `host_call` from a harness are protocol errors.
- **`Pool.HostCall`** asks the run's process, or the least loaded one, and
  starts one when none serves. A `fresh` pool refuses. `Pool.Plugin` returns
  the declaration of the pool's first process, and starts it.
- **`around`.** An invocation's `engine.Invocation.Around` goes on its
  `invoke` as `around`. One with wrappers is not dispatched to a harness
  that loaded no plugin.

### Run data

`NewRunData` gives a run's data an id, unique in the engine. `wl` puts it
in the run's context with `WithRunData` before the run starts, and calls
`Release` once the run has settled. `Execute` and `HostCall` take it from
their context, so the frames of another run, one that reuses the run's id
included, never carry it. `WithRunData` with nil hides the data of a
context that a run without data inherits.

- **Which frames carry it.** An `invoke` to a process with a plugin, or a
  `host_call`, carries the data's `run_data_id`, and the data as
  `run_data` until one frame that carries it holds its place on the wire.
  A whole frame holds it once queued, and a part stream once its last part
  is taken (the connection's `placed` callback).
- **Why a later frame may carry it.** A small frame can overtake a large
  one, so a later frame of the run carries the data too while the first is
  still in parts.
- **Release.** `Release` sends `run_released` to each process that the
  data reached. A frame that carries the data and is placed after the
  release sends one of its own. Later frames of the run carry nothing, and
  the `RunData` lets go of the data and of the processes.

## Pools

A `Pool` serves one `Key`: a harness class, a runtime pin, a dependency set
and a module-state mode.

- **Settings.** A pool with settings is one whose `PoolConfig` sets any of
  `Min`, `Max`, `Cap`, `LagThreshold`, `RecycleTimeout`, `MaxLive` or
  `Recycle`. Only such a pool applies the admission checks below and
  serializes its diagnostics writer. A pool without settings behaves as it
  always has.
- **The diagnostics writer.** The processes of a pool with settings share
  `ProcessConfig.Diagnostics`, and the pool serializes their writes to it,
  so the writer need not be safe for concurrent use. It still calls the
  writer's `WriteString`, as `io.WriteString` does.

### Fresh pools

A fresh pool (`engine.ModuleStateFresh`,
[module state](../../docs/design/0006-module-state.md)) starts one process
for each `Execute`. That process serves the invocation in a working
directory of its own, and exits once it has reported the outcome.

- A process that outlives its outcome by 5 s is killed.
- The outcome reaches the caller once no process is left in the process's
  group ([WR-55](../../docs/semantics/languages.md#wr-55)).
- When the engine stops waiting first, the process and its group are
  killed, and the invocation settles with it.
- `Close` waits for the fresh processes until its context ends, and kills
  the rest. One that finishes starting after `Close` is killed before it
  runs anything.
- None of the rules below apply to a fresh pool.

### Warm pools

- **Size.** `Min` and `Max` default to 1. `Start` spawns `Min` processes
  and returns once `Min` live processes serve, or `ErrPoolClosed` after
  `Close`; otherwise the first `Execute` spawns them.
- **`Start`'s errors.** `Start` fails with the error of a start that it
  made, which includes the replacement of a member whose process exited
  before its admission. It also fails once more than `Min` members have
  exited that way.
- **Routing.** The process that the run used last, then the least-loaded
  one. A process whose lag exceeds `LagThreshold` (250 ms by default) is
  used only when no other process can take the invocation and none can be
  started.
- **The cap.** `Cap` bounds the invocations in flight per process;
  abandoned ones do not count, and neither do host calls, which a step can
  wait for. Host calls still keep a leaving process from draining. There is none by default; a host-set cap is
  a [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **Freeing a slot.** A finished or abandoned invocation frees its slot
  before its Reporter hears of it, an abandoned one before `Execute`
  returns. So a Reporter may run another step on the same pool.
- **Tainting.** An abandoned invocation taints its process: it gets no new
  invocations, and does not count toward `Max`. Once nothing else is in
  flight in it, it is drained (killed after `RecycleTimeout`, 5 s) and
  replaced.
- **Back to service.** If its abandoned invocations settle first, a
  tainted process returns to service when the pool is below `Max` without
  it, counting processes still starting. Otherwise it is surplus, and
  leaves once idle. A process that finishes starting when the pool has
  reached `Max` is surplus itself.
- **Order.** The pool applies these rules before an abandoned invocation's
  Reporter hears `Settled(completed)`.
- **Replacement.** A process that dies leaves the pool. The pool starts its
  replacements up to `Min` before the Reporters of its invocations hear
  `Settled(process_exited)`. A failed start is logged, retried by the
  next `Execute`, and passed to `PoolConfig.OnStartFailure` when it is set,
  which `windmill-lite plugin` uses to drain.
- **An early exit.** In a pool with settings, a process that exits before
  the pool admits it is a failed start, so it holds no place in the pool.
  A pool without settings admits it, as it always has, and `Start` removes
  it.
- **The RSS trigger.** `Recycle.RSS` trips a process whose health reports
  give an `rss` of at least that many bytes `Recycle.Samples` times in a
  row (2 by default). A health report is a heartbeat `ping` or a `pong`.
  Zero turns it off.
- **The module trigger.** `Recycle.Modules` trips a process once the
  invocations dispatched to it ran more distinct scripts than that; an
  invocation that was never sent does not count. Zero turns it off.
- **Past the module threshold,** the invocation of the one more script
  still runs there, and so do invocations picked for the process before
  that one was dispatched. Once the process starts to leave, no later
  invocation goes there. While another process leaves for a trigger, it
  keeps serving (one at a time, below).
- **Leaving for a trigger.** A process that trips stays tripped. It takes
  no new invocations and does not count toward `Max`. It drains once
  nothing that the engine waits for is in flight in it.
- **Its runs' host calls.** A host call of a run with an invocation in
  flight in a leaving process goes there when no serving process can take
  it, since the invocation may wait for the call. A run with nothing in
  flight there waits for a serving process.
- **Its replacement** starts at once, beside it, also in a pool that has
  grown past `Min`. One that `MaxLive` holds back starts once a process
  exits, unless the pool has reached `Max` by then. A failed start of it
  is tried again by the next `Execute` or host call.
- **One at a time.** One process leaves for a trigger at a time. Another
  that trips meanwhile keeps serving, and starts to leave once the exit of
  the first is observed.
- **The bound on live processes.** With `MaxLive` set, the pool starts no
  process while `MaxLive` are alive: members of every kind until their exit
  is observed, and spawns under way. Routing does not change, so an
  invocation waits only when no member can take it and the start that it
  needs is blocked. `NewPool` refuses `MaxLive` below `Max`.
- **Close.** It refuses new invocations, and waits for those in flight
  until its context ends. Then it drains every process, including one that
  finishes starting afterwards.

#### Pinned runs

`wl` calls `PinRun` before a pinned run starts, which returns the run's
`Pin`, then `RunEnded` with it at the run's end and `ForgetRun` once the
run has settled. A fresh pool returns no pin.

- **The pin.** The run's first invocation or host call is routed as any
  is, so it goes to a serving process, and the pin records that process.
  Every later one of the run goes there while it lives: tainted, surplus,
  lagging or leaving for a trigger. The cap still applies, so an
  invocation of the run waits for a slot there.
- **A first call that did not reach the process.** While no call of the
  run has reached the process, one that it refused, or that ended before
  it was sent, frees the pin, once no other is under way, to bind to the
  next pick. A host call that was sent has reached it, whatever ends it.
- **Concurrent first calls.** They go to the pinned process together, the
  cap aside. Only once that process is leaving or has exited does another
  call wait for the outcome of a first one under way, rather than report
  the process lost.
- **The hold.** A process with a live pin does not drain, whatever else
  would retire it. `RunEnded` ends the hold; until `ForgetRun`, the pin
  still routes the run's late host calls there while the process can
  answer, draining or not. Once it has exited or refused one, they are
  routed as any are.
- **One pin per run.** A call routes by the routing choice that its
  context carries (`WithPin`) when the choice is this pool's and of the
  call's run: a pin, or `Unpinned`, the ordinary pick. Otherwise it routes
  by the pin of the latest run admitted with its id.
- **Their calls.** `PinRun` replaces an ended run's pin for the id and
  refuses a live one's (`ErrPinLive`), and `ForgetRun` drops only its own
  pin.
- **A lost process.** Once the process has exited, the run's next
  invocation or host call fails with `engine.ErrPinnedProcessLost`, not
  dispatched, and the engine latches the run. After `RunEnded`, they are
  routed as any are.

## Tests

The tests that run the real Bun harness need the pinned Bun, and the
protocol's shared cases are in
[conformance/protocol](../../conformance/README.md). The codec tests, the
fake-harness tests (with their pool tests) and `TestPoolHostCall` need
nothing else. The warm pool rules are tested on Bun (`TestBunPool*`), and
the bound on live processes and the recycle triggers on the fake harness
(`pool_test.go`).

### Run the tests

1. Install Bun 1.4.3, the pinned Bun (`PinnedBun`).
2. For the env-view test, install the stock `windmill-client@1.862.0` in a
   directory, so that its `node_modules` holds it.
3. Export the variables of the table below.
4. Run the package's tests: `go test -race ./internal/harness`

```sh
WL_BUN=/path/to/bun-1.4.3/bun WL_WINDMILL_CLIENT=/path/to/client go test -race ./internal/harness
```

To rewrite the codec cases of `conformance/protocol/frames.json` from Go's
encoder, run `go test ./internal/harness -run TestFrameCases -args -update`.
Without `-update`, the test fails unless the file is exactly what
`-update` writes.

CI runs these tests in `Go (ubuntu-latest)` and
`Go (macos-latest, packages)`, with `go test -race -timeout 45m ./...`
and `WL_REQUIRE_BUN` set ([docs/ci.md](../../docs/ci.md#the-go-jobs)).

| Variable | Value | Tests that need it | When it is not set |
|---|---|---|---|
| `WL_BUN` | The path of a Bun 1.4.3 executable | Every test that runs the real Bun harness: `bun_test.go`, `exchange_test.go`, `children_test.go`, `rundata_test.go`, `reap_linux_test.go` (all but `TestFreshLandedKillReapsALaterOrphan`), `qwa4_test.go`, the `TestBunWarm*` tests of `childgroup_test.go`, `TestTimesHook`, `TestBenchEcho`; and `BenchmarkHarnessCodec` and `BenchmarkTransport`, which fail rather than skip | The test skips, or fails when `WL_REQUIRE_BUN` is set |
| `WL_WINDMILL_CLIENT` | A directory whose `node_modules` holds `windmill-client@1.862.0` | `qwa4_test.go` | The test skips, or fails when `WL_REQUIRE_BUN` is set |
| `WL_REQUIRE_BUN` | Any non-empty value | All of the above | Missing tools skip their tests |
| `WL_FAKE_HARNESS`, `WL_FAKE_CTL`, `WL_SUBREAPER`, `WL_WAIT_ALL`, `WL_DRIP_CHILD` | Set by the tests and their fixture steps (`spawn-waitall` sets `WL_WAIT_ALL`), when they re-run the test binary as a fake harness, a subreaper, a process that waits for all its children, or a child that keeps writing. `WL_FAKE_CTL` is a fake harness's control directory | `fake_test.go`, `pool_test.go`, `reap_linux_test.go`, `children_test.go` | Not for you to set |

### Test files

| File | What it checks | Needs |
|---|---|---|
| `frames_test.go` | The codec cases of `conformance/protocol/frames.json`; `-args -update` rewrites their payloads. An empty or null `foreign_windmill_origins` decodes as none (`TestWelcomeEmptyForeignOriginsAreNone`). | |
| `args_test.go` | `scriptArgs`: the two internal keys dropped, a key written with an escape included, every other member's raw text and place kept. Arguments with nothing to drop, or that are not an object, stay unchanged | |
| `conn_test.go` | The writer's ordering rules, the barrier behind open run streams (with a `pong` still overtaking them), and the exchange matcher. `TestFlushWritesTheFrameItself` and `TestFlushKeepsFramesWholeAndInOrder` (a sender's own write, under `-race`, on a 4 KiB socket buffer) | |
| `host_test.go` | A host on a socketpair: the handshake's parent check, a host gone before its hello, runs and their invocations, `run_cancel`, a lost host (also with a gap, `hostLostGap`), the watchdog, and the 502 for a bad platform answer | |
| `hostmode_test.go` | The shared working-directory cases, through `CheckWorkdir` and the handshake; a refused host; a host's `BASE_URL` and `args.json` (and `SkipArgsFile`); the drain; `run_settled` after `run_done`; a run sent the moment the welcome arrives. The version pairing's shared cases (`TestVersionPairingCases`). The plugin's calls, errors and refusals, and `TestPoolHostCall` | |
| `exchange_test.go` | The engine's side of every transcript in `conformance/protocol/exchanges/`, against the real Bun harness: the protocol's golden tests on Bun 1.4.3 | `WL_BUN` |
| `bun_test.go` | `Process` and `Pool` against the real Bun harness ([below](#what-the-bun-tests-check)) | `WL_BUN` |
| `children_test.go` | The bundle of `testdata/children`: the base environment as a step sees it, and process groups in `fresh` and `warm` mode ([below](#what-the-bun-tests-check)) | `WL_BUN` |
| `reap_linux_test.go` | Reaping, with the test binary as a child subreaper, on Linux ([below](#what-the-bun-tests-check)) | Linux; `WL_BUN` for all but `TestFreshLandedKillReapsALaterOrphan` |
| `childgroup_test.go` | Child groups on a fake process table: their identity, the guardian and its tokens (`hasGuardArg`), undecided looks, ending once, release, `groupMembersOf`; the reaping rules (`TestReapChildGroups`); a real guardian, the test's own child, reaped once the watcher releases its group (`TestReviewOwnedGuardianReaped`) | `WL_BUN` for `TestBunWarm*` |
| `fake_test.go` | A fake harness, for what Bun cannot be made to do on demand ([below](#what-the-fake-harness-tests-check)) | |
| `pool_test.go` | A warm pool's settings on the fake harness of mode `pool`, whose invocations and drains the tests hold; the pool's own events (spawns begun and failed, exits observed, drains begun, waits) give the order. The tests are listed [below](#what-the-pool-tests-check) | |
| `rundata_test.go` | Run data with the real harness. A `host_call` that overtakes a run's large first invoke carries the data (`TestRunDataOvertakingFrame`), and a release while that invoke is held reaches the process (`TestRunDataReleasedWhileQueued`). Each of two processes gets the data with its first frame and later frames carry the id alone (`TestRunDataTwoProcesses`); after the release a new process gets none (`TestRunDataAfterRelease`). Two runs of one id keep their own data (`TestRunDataSameRunID`). | `WL_BUN` |
| `qwa4_test.go` | That concurrent invocations each see their own `WM_*` environment variables ([Q-WA-4](../../docs/semantics/client-api.md#q-wa-4), [WA-31](../../docs/semantics/client-api.md#wa-31)) | `WL_BUN`, `WL_WINDMILL_CLIENT` |
| `retain_test.go` | `TestSerialKeepsNoCallItRan`: an invocation's call queue (`serial`) keeps no function that it has run, so what they closed over becomes unreachable | |
| `bench_test.go` | The microbenchmarks ([Microbenchmarks](#microbenchmarks)), `TestTimesHook` and `TestBenchEcho` | `WL_BUN` for the Bun parts |
| `helpers_test.go` | Shared helpers: `bunPath` (`WL_BUN`), the fixture bundle, `spawn`, a recording Reporter, `execute`. The gates are in `bun_test.go` | |

`qwa4_test.go` runs its steps with the stock `windmill-client@1.862.0`
unbundled, bundled with Windmill's settings, or bundled with the define
rewrite, plus an unbundled dependency that reads `Bun.env`. Sixteen
invocations per variant run at once, warm and fresh, with and without the
variables in the process. Each must read its own through every channel,
and a warm one's client call must be attributed to it.

`exchange_test.go`'s fixture bundle has scripts that take named
parameters, each with the `.meta.json` signature that the build helper
would record. Its transcripts include an intercepted `process.exit`
(`exit-caught`, `exit-zero`, `exit-from-timer`), an attributed error, the
unattributed warning, and a signature that did not parse. Others hold:

- `log` frames ahead of the result (what a step prints never reaches the
  frame channel);
- the `left_behind` warning (`left-behind`, and `platform-left-behind`,
  whose late reply is dropped), and a platform call refused after
  settlement;
- `fresh` mode (`welcome-fresh`, `fresh-one-invocation`, `fresh-exit`).

#### What the Bun tests check

- **`bun_test.go`:** handshake rejections and the pid check; outcomes;
  process death, through `process.reallyExit`, which the harness does not
  intercept; a `process.exit` that ends only its invocation.
- **Errors and warnings:** the attribution of a timer's error; the warning
  of an unattributed rejection, and one whose text exceeds a small
  `MaxMessage` (the warning is cut, and the process lives); a detached
  rejection that ends nothing.
- **Late work:** work after the result, reported as a late error that
  settles nothing (a timer's throw and its `process.exit`). The late work
  of `TestBunWorkAfterTheResultIsLate` comes from continuations of a
  `Bun.sleep`, which the fence cannot stop, since it clears only the timers
  that a step leaves behind. Output of a module's own work after its
  loading invocation settled.
- **Cancellation:** cancel and late settlement.
  `TestBunCancelDuringLargeInvoke` holds a 24 MiB invoke's delivery after
  its first part (`partWritten`, a hook that every connection's writer
  calls after a part frame), and cancels while the stream is open. It
  checks that the cancel waits behind the stream.
- **Limits, the working directory,** and the watchdog case
  `watchdog/finite-cpu` in warm and fresh mode.
- **Fresh pools:** a process and a directory per invocation; a kill on
  cancel; results of 200 KB and 3 MB and a long log before an exit, all
  written before the process ends; an exit with the step's code; `Close`.
- **A fresh `process.exit`** after which no `catch` or `finally` block
  runs, with a short log and with one that the socket cannot take at once
  (`TestBunFreshExitRunsNoMoreCode`, whose warm control runs both).
- **The pool rules** of [Warm pools](#warm-pools).
- **No timing dependence.** A step that must still run when the test acts
  is a `gate` fixture step, which reports that it has started and ends when
  the test opens it, or `spin-gate`, which blocks its process's event loop
  the same way. The cancel, taint, recycle and affinity tests hold their
  steps on gates, and cancel only once a step runs. The lag test waits for
  the lag to show.
- **Starts on a gate.** `TestBunPoolTaintClearsWhileTheReplacementStarts`
  also holds every start after the first on a gate file. The affinity test,
  `TestBunPoolTaintedProcessReturnsToService` and that test turn lag
  routing off, which is not under test there.
- **`children_test.go`, the environment:** `BUN_INSTALL_CACHE_DIR` and
  `NODE_PATH`, as `BaseEnv` builds them and as a step sees them
  ([WR-22/b](../../docs/semantics/conformance.md#wr-22)).
- **`children_test.go`, `fresh` groups:** a child in the group holds the
  outcome until it exits, settled completed
  ([WR-55/a](../../docs/semantics/conformance.md#wr-55)); a detached child
  does not. A timeout while the outcome waits kills the group, and ends the
  step with the timeout ([WR-55/b](../../docs/semantics/conformance.md#wr-55)). A kill that is not the invocation's own
  (`Kill`, as a pool's close does) fails the held job instead of releasing
  its result.
- **`children_test.go`, `warm` groups:** a normal return settles at once,
  its `child` reported and killed with its group; a `detached` child is
  reported and left running; a timeout's `cancel` kills the children of
  the abandoned call.
- **`children_test.go`, a blocked event loop:** a step that blocks its
  event loop after it started a child (`stall`) has the child killed by the
  engine on its timeout, though the harness cannot handle the `cancel`. The
  child is also killed with the process when the watchdog kills it. A
  synchronous child call's child ends with the process too.
- **`children_test.go`, `spawn-waitall`:** a child that waits for all its
  children before it exits (this test binary run with `WL_WAIT_ALL`,
  looping on `wait4(-1)`) finishes in both modes, as outside the harness:
  a warm group's guardian is no child of it. The test's `ended` helper
  reads a process's state through `procInfoOf`, not `ps`, which a slim
  Linux image lacks (`TestEnded` runs without a `PATH`).
- **`reap_linux_test.go`:** the test binary, re-run as a child subreaper
  (`prctl(PR_SET_CHILD_SUBREAPER)`, skipped where the kernel refuses it),
  adopts what the engine as PID 1 without an init would. The cases are
  repeated short spawns whose shell leaves a daemon (`spawn-daemon`), and a
  descendant that exits while its group still runs (`spawn-shell`). Others
  are a cancellation of a stalled step, the process's end with a child
  group or a synchronous child, and a fresh job's orphan.
- **`TestFreshLandedKillReapsALaterOrphan`** (its own subprocess, a child
  subreaper too): after the exit bound's kill, a member that another
  process leaves as an orphan once the job has settled is still reaped. It
  runs the fake harness, so it needs no Bun, and skips only where the
  kernel refuses `prctl`.
- **What `reap_linux_test.go` asserts:** each guardian and child that the
  engine adopted is reaped, the descendant before its group ends, and no
  zombie of the test process is left.

#### What the pool tests check

| Test | What it checks |
|---|---|
| `TestPoolMaxLive`, `TestPoolMaxLiveRouting` | `MaxLive` with a taint, and routing to a started process |
| `TestPoolLeavingWaitsForWork` | A leaver that waits for its work before it drains |
| `TestPoolRecycleOnRSS`, `TestPoolRecycleOnModules`, `TestPoolModulesCountDispatchedScripts` | The RSS trigger, from heartbeat pings and from pongs, and the module trigger |
| `TestPoolRecycleReplacesAboveMin` | The replacement of a tripped process in a pool grown past `Min` |
| `TestPoolCapLeavesHostCalls` | Host calls beside a full cap |
| `TestPoolEarlyExitNotAdmitted` | A process that exits before its admission, in a pool with settings |
| `TestPoolEarlyExitZeroSettings`, `TestPoolStartWaitsForALiveProcess` | Without settings: the early exit admitted as before, and `Start` waiting for a live process |
| `TestPoolStartSurfacesAFailedReplacement`, `TestPoolStartGivesUpOnRepeatedEarlyExits`, `TestPoolStartIgnoresOrdinaryExits` | `Start` failing with the failed replacement of an early exit and after more than `Min` early exits, but not for members that served and exited |
| `TestPoolPluginDeclarationsAgree`, `TestPoolPluginAbsenceIsTheBaseline`, `TestPoolFailedStartSetsNoDeclaration` | Two initial processes with different plugin declarations, a declaration after none, and a failed start that sets nothing |
| `TestPoolDiagnosticsSerialized` | Two processes that write to one writer that is not safe for concurrent use, at once: no race, every line whole, through `WriteString` |
| `TestPoolDiagnosticsZeroSettings` | Without settings: the writer's `WriteString` called as before, and one process's line not held behind another's |
| `TestPoolModulesTripBeforeTheNextPick` | No pick after the dispatch that crossed the module threshold goes to that process |
| `TestPoolOwedReplacementSurvivesAFailedStart` | A replacement whose first start fails is tried again |
| `TestPoolReportsAFailedStart` | A failed start reaches `OnStartFailure` with the error that `Start` returns |
| `TestPoolRecyclingProcessServesItsRunsHostCalls` | A host call of a run with an invocation in flight in a process leaving for a trigger, after the pool forgot the run's last process; a run with nothing in flight there waits |
| `TestPinnedRunStaysOnTaintedProcess`, `TestNoNewPinOnTaintedProcess` (`pin_test.go`) | A pinned run's later invocations on its tainted process, an unpinned run's on the replacement; no new pin on a tainted process, and new pins again once it returns to service |
| `TestPinHoldsUntilRunEnd`, `TestPinAfterTaintClears` | A tainted or surplus process that a live pin holds does not drain, and drains after `RunEnded` |
| `TestPinnedRunCapFull`, `TestPinnedRunHostCalls` | A pinned invocation waits for its process's cap while unpinned ones go elsewhere; a pinned run's host calls, `onRunSettled` after its end included, reach its process |
| `TestPinnedRunProcessLost` | An invocation in flight fails as escaped; the run's next invocation and host call fail with `pinned_process_lost`, not dispatched; after `RunEnded`, a late host call is routed |
| `TestPinIDReuse`, `TestPinRebindsAfterARefusedFirstCall` | A later run with the same id routed by its own pin, and the earlier run's late call by its pin. Each settlement forgets its own pin, and an unpinned successor is routed as any run. A pin binds again after a first step or host call refused before dispatch |
| `TestUnpinnedRunLateCalls`, `TestPinOfAnotherPool`, `TestPinRunRefusesALivePin` | An unpinned run's late call neither binds nor uses a later pinned run's pin; another pool's pin in a call's context is ignored; one live pin per id |
| `TestPinRebindsAfterACallThatWasNotSent`, `TestPinFirstCallsAcrossAnExit` | A first call that ended before it was sent frees the pin; a second call waits for a first one under way on a process whose exit the pool has observed, then runs elsewhere |
| `TestPoolAliveCount` | With pins, `Max: 1`, `MaxLive: 2` and drains held: a surplus process serving its pin drains only after the pin ends and its unrelated work, held past `RecycleTimeout`, completes. A new invocation waits through the held drain and starts the third process at the exit, and the live count never passes 2 |

#### What the fake-harness tests check

`fake_test.go` re-runs the test binary as a fake harness:

- a peer that stops reading, a mute peer, outcomes fixed before the call
  ends, late output, and protocol violations;
- an exit while a child holds the socket or stdout, a slow start, and a
  slow or re-entrant Reporter;
- an abandoned call that settles the moment its cancel arrives;
- a fresh process that exits right after its result with a `pong` still
  unread (`TestFreshExitWithAnUnreadPong`, which fails on Linux when
  `ECONNRESET` is taken for a failed connection);
- a fresh process that ends its stream and exits late
  (`TestFreshSlowExitKeepsItsResult`) or by its own signal
  (`TestFreshSignalledExitIsItsOwn`);
- one that never exits (`TestFreshNoExitIsEnded`), with a child in its
  group (`TestFreshNoExitWithAChildIsBounded`) or a zombie that its group
  keeps (`TestFreshNoExitWithAStrayZombieIsBounded`);
- an exit seen only after the bound's kill
  (`TestFreshExitSeenAfterTheBoundKeepsItsResult`), and when that kill
  counts as what ended it (`TestKillLanded`);
- after that kill, a cancellation that came before the settlement
  (`TestFreshCancellationBeforeTheSettlementEndsTheWait`), one after it
  (`TestFreshCallersContextEndEndsTheWait`) and the caller's deadline
  (`TestFreshSettledJobEndsByItsCallersDeadline`) each end the wait for a
  group that a zombie keeps;
- the end of the caller's context then kills a member that joined the
  group later (`TestFreshCallersContextEndKillsALateMember`);
- that a settled invocation, kept among the recent ones, holds nothing of
  its caller's context (`TestSettledInvocationKeepsNoCallerContext`);
- that a fresh dispatch asks the caller's context for its `Done` channel
  without holding the process's lock
  (`TestFreshDispatchAsksForDoneOutsideTheLock`);
- that the pool hears of an abandonment and of a death before any Reporter
  does;
- that a kill is named (`TestKillIsNamed`) without waiting for a blocked
  diagnostics writer, by `Kill` (`TestKillWithBlockedDiagnostics`) or by a
  fresh invocation's cancellation (mode `fresh-hang`,
  `TestFreshCancelWithBlockedDiagnostics`);
- that kill lines are bounded across processes
  (`TestKillLinesAreBoundedAcrossProcesses`), and that the output pipes
  close after the exit (`TestOutputPipesCloseAfterExit`).

| Test | What it checks |
|---|---|
| `TestFramesWrittenBeforeTheExitAreRead` | The reader is held on a fake fresh harness's first log frame (the hook `messageRead`) while the harness writes 20 more and exits. Once the grace after the exit has passed, the escaped outcome's tail holds every line, in order |
| `TestAChildWritingAfterTheExitDoesNotHoldTheReader` | In mode `fresh-drip-exit`, the fake harness exits and leaves a child, in a session of its own, that writes a log frame every 50 ms for 20 s. The invocation still ends soon after the exit (the test allows 8 s) |
| `TestADrainEndsTheReaderOfAnExitedProcess` | With the grace lengthened to an hour, a drain that times out ends the reader at once |
| `TestResultHandOffRacesCancellation` | 120 results race their contexts under `-race`. Every `Execute` returns, and each invocation's Reporter calls keep their order, do not overlap, and settle once |

Other checks of this package's behaviour run elsewhere. The step case
`conformance/steps/bun/wr-5.yaml` checks the arguments file end to end:
two runs of one warm process, and fresh processes, with the internal keys
absent from the file and from the script's parameters. So does
[FX-42/h](../../docs/semantics/conformance.md#fx-42)'s run `args_file`
([cmd/wltest](../../cmd/wltest/README.md)).

## Benchmarks

Two things here serve the step boundary benchmarks, whose recorded
baseline is in the
[JSON at step boundaries](../../docs/design/0005-json-step-boundaries.md#baseline)
design record: a hook that times every invocation, and microbenchmarks of
the parts of a round trip on their own.

### The times hook

`SetTimesHook(f)` makes every `Process` of the program report each
invocation whose outcome came in a `result` frame to `f`, as an
`InvocationTimes`. An invocation that a lost connection or a canceled
context ended is not reported.
These are readings of one monotonic clock, taken on the invocation's own
path:

| Reading | When |
|---|---|
| `Start` | `Process` began the invocation |
| `Encoded` | The invoke's payload was ready |
| `ArgsWritten` | `args.json` was written, or would have been: the reading is taken also when the process writes no arguments file (no working directory, or `HostConfig.SkipArgsFile`) |
| `Queued` | The invoke was queued on the connection |
| `Received` | The reader had read the result's last frame |
| `Decoded` | The reader had decoded the result |
| `Done` | The outcome had reached the invocation's own goroutine inside `Process`, just before `Execute` returns it. It is no settlement fence: for a result that does not settle at once, the final `Late` and `Settled` calls may run before or after it |

- The harness runtime records its own readings of the same invocation
  ([harnesses/bun](../../harnesses/bun/README.md#benchmark-timings)).
- The benchmarks of `cmd/windmill-lite` join both by the invocation's id,
  to split each step into stages
  ([docs/benchmarks.md](../../docs/benchmarks.md#stages)).
- While the hook is unset, a `Process` reads no clock for it: each
  invocation and each received frame cost one atomic load.
- `TestTimesHook` runs an invocation on the real Bun harness with the hook
  set, and checks that its readings are all taken and in order.

### Microbenchmarks

`bench_test.go` measures each part of a round trip on its own, as the mean
of repeated calls, on the payloads of [internal/bench](../bench/README.md),
from 26 B to 4 MiB. A step's argument is the payload, and its result is
the payload again. These figures are not a split of a real step, whose
stages the times hook measures.

| Benchmark | What it measures | Needs |
|---|---|---|
| `BenchmarkGoInvocation` | The engine's side of the codec: `scriptArgs` and the invoke's payload (`encode-invoke`), and the result's payload decoded (`decode-result`) | |
| `BenchmarkArgsFile` | The `args.json` file that each invocation writes into the harness's working directory, written back to back | |
| `BenchmarkHarnessCodec` | The harness's parse and serialise on the pinned Bun: the invoke decoded, the result serialised by the runtime's `resultText`, and the result's payload encoded | `WL_BUN` |
| `BenchmarkTransport` | The transport alone, one invocation at a time and eight at once, against an echo | `WL_BUN` |

- **`testdata/bench.ts`** is the Bun side of the last two. It imports the
  runtime's own `frame.ts` and `wrapper.ts`, so it runs the code that a
  step's invocation runs.
- **Its echo** reads frames and reassembles part streams as the runtime
  does. It answers each invoke, without parsing it, with a result whose
  payload it encoded once, split into part frames above 1 MiB.
- **`BenchmarkTransport`** covers both writers, part streams in both
  directions, both readers and the socketpair, and the engine's decode of
  the result. One iteration is one round trip per worker, the workers
  started together, so an iteration of the eight-worker case has eight in
  flight. It reports the concurrency reached.
- **`TestBenchEcho`** runs the echo with a whole frame and with part
  streams, two invocations at a time.
- **Without Bun.** The two benchmarks that need the pinned Bun fail
  without `WL_BUN` rather than skip. The benchmarks run only with
  `-bench`:

```sh
WL_BUN=/path/to/bun-1.4.3/bun go test ./internal/harness -run '^$' \
  -bench 'GoInvocation|ArgsFile|HarnessCodec|Transport' -benchtime 1s -count 3
```

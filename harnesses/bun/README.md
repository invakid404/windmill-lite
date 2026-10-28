# harnesses/bun

**Current state:** the runtime skeleton of milestone C1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)), with what the
walking skeleton (W1) added: the per-invocation context, the facade fetch,
the process-level error handlers, host mode, and a minimal host SDK. This
README covers `harnesses/bun`: `runtime/`, the harness side of the protocol
of [ADR 0004](../../docs/design/0004-harness-protocol.md); `host/`, the Bun
host SDK of [ADR 0009](../../docs/design/0009-host-adapters.md) in W1's
minimal form (F3a completes it); and `test/`, its developer checks. The
build helper (`build/`, C2a) does not exist yet. The harness runs on the
pinned Bun 1.3.10, the version of Windmill v1.767.0's images
([WR-3](../../docs/semantics/languages.md#wr-3)). It has no dependencies, no
`package.json` and no `node_modules`.

## Running

The engine starts it; `internal/harness` has `BunCommand` for that:

```sh
bun harnesses/bun/runtime/main.ts --bundle <bundle dir>
```

File descriptor 3 must be one end of a stream socketpair, which carries
every frame. stdout and stderr carry no frames, so what a step prints
cannot corrupt the protocol.

## What the runtime does

| File | Contents |
|---|---|
| `runtime/main.ts` | Reads the bundle's digest, connects to fd 3, starts the runtime |
| `runtime/runtime.ts` | The handshake, invocations, cancel, health, drain, platform calls, and host mode's runs |
| `runtime/context.ts` | The per-invocation context: the environment view, the facade fetch, the process-level error handlers |
| `runtime/frame.ts` | The codec and the send queue: canonical envelopes, the decoding rules and error codes of the Go side, part streams, and the order of messages about one invocation |

- **Handshake.** It sends `hello` with `Bun.version`, its pid, the SHA-256
  of the bundle's `manifest.json`, and the `fetch` capability (`host` too in
  host mode; `native` and `fresh` are false). It accepts only a warm
  welcome.
- **Heartbeat.** After the welcome it sends a `ping` every `heartbeat_ms`
  with its health: invocations in flight, abandoned ones, process-level
  errors (always 0 until C3a installs handlers), the event loop's lag (how
  late the heartbeat timer fired) and RSS. It answers the engine's pings
  with the same.
- **Invocations** run concurrently on the event loop, each inside its own
  `AsyncLocalStorage` context with its id, run, environment and abort
  signal. A script's module, `scripts/bun/<id>.mjs` in the bundle, is
  imported once per process on its first invocation, inside that
  invocation's context (warm module state).
- **Cancel** abandons the invocation. When its call ends, the outcome goes
  out as the `late` frame that settles it.
- **Drain** stops the heartbeat. Once everything queued has gone out, the
  runtime sends `bye`, its last frame, and exits when its socket has closed,
  whatever is still running.

## The per-invocation context

`runtime/context.ts` installs three things once, before any step module
loads ([WA-31](../../docs/semantics/client-api.md#wa-31)):

- **The environment view.** Each of `process.env`, `Bun.env` and
  `import.meta.env` answers from the current invocation's variables first.
  On Bun 1.3.10 the three start as one object, and assigning `process.env`
  leaves the other two on it, so the view has three parts:
  1. `process.env` becomes a Proxy over the process environment. It also
     keeps a step's writes and deletes in its own copy, so they never reach
     the process or other invocations, and lists its variables when the
     environment is copied or enumerated.
  2. The original object gets a Proxy as its prototype, which answers a
     read of a variable the process does not have from the current
     invocation. This reaches code that reads `Bun.env` or
     `import.meta.env` at run time, bundled or not, so it does not depend on
     the build's define rewrite (C2a).
  3. A variable that the process has and an invocation sets or deletes, or
     that the process writes while invocations are in flight, is turned
     into an accessor that answers from the current invocation first.

  Outside any invocation every name reads the process environment. W1's
  Q-WA-4 test in [internal/harness](../../internal/harness/README.md)
  measures every channel.
- **The facade fetch.** `globalThis.fetch` is wrapped: a request to the
  welcome's `facade` origin becomes a `platform` frame attributed to the
  invocation whose context made it, with its body's bytes, and its
  `platform_reply` becomes the `Response`. Any other request passes through
  unchanged. A facade request made outside every invocation is refused with
  a `TypeError`, and one whose signal has already aborted is not sent.
- **Process-level error handlers.** `uncaughtException` and
  `unhandledRejection` handlers that never exit
  ([ADR 0004](../../docs/design/0004-harness-protocol.md), the
  shared-process failure model, item 1). Each error is written to stderr and
  counted in health's `errors`. Attributing an error to the invocation whose
  context raised it is C3a's; until then every one counts as unattributed.

The fetch wrapper and the handlers are installed once per process and
answer to the runtime that installed them last, so a host that connects
again counts its errors and sends its facade calls on its current
connection.

## Host mode and the host SDK

`host/index.ts` is the SDK for a host that is itself a Bun program
(ADR 0009). `connect(options)`:

1. checks that `workdir` is writable and is already the process's working
   directory (`workdir_not_writable`, `workdir_mismatch`), and reads the
   bundle's manifest (`bundle`);
2. registers the host's instances of the host-provided modules
   (`hostModules`, by module path) in the registry
   `Symbol.for("windmill-lite.hostModules")`
   ([ADR 0010](../../docs/design/0010-packaging.md), D12), before any step
   loads;
3. starts the engine binary as `<engine> host --bundle <dir> --kill-after
   <ms>ms` with one end of a socketpair as its fd 3 (Node's extra `"pipe"`
   stdio, which Bun 1.3.10 makes a socket), or fails with `engine_start`, and
   runs the runtime in this process in host mode. The socket's backpressure
   reaches the runtime's send queue, so a small frame still waits behind at
   most one part frame.

It returns a `Host`:

- `run(path, args, {intercept, signal})` submits a run and resolves with
  its `status` and `result`; an aborted `signal` cancels it. The scope the caller has when it calls `run`
  is captured with `AsyncLocalStorage.snapshot()`, and every invocation of
  the run re-enters it (H11). `intercept` maps module ids to the results
  that stand in for them (H1's static form).
- `health()` reports invocations in flight and abandoned, the age of the
  oldest abandoned one, unattributed errors, lag, RSS and the runs in
  flight.
- `drain(timeoutMs)` refuses new runs (`draining`) and waits for those in
  flight up to the timeout; the host may then exit.
- `close()` ends the connection and resolves once the engine has exited;
  the host lives on.

When the connection to the engine ends other than by `close()`, the SDK
exits the host with code 1 unless `onEngineExit` is given: without its engine a host runs nothing, and
its supervisor restarts it. The engine's watchdog kills the host when its
event loop stops answering (`watchdog.killAfter`, default 30 s, 0 disables
it). A step reads a host scope only through a host-provided module; the
bundle's step imports it through a shim that takes each export from the
registry. Milestone C2a generates those shims; W1's test bundle has one
written by hand.

## The step runner

**The step runner is a skeleton.** It calls the export named by `entry`
with the argument object as its only parameter and serialises the value
with `JSON.stringify(value ?? null)`.

| The call | Outcome |
|---|---|
| returns | `ok` with that text |
| throws a value that is not `null` or `undefined` | `error` with `{message, name, stack}`, the fields the value has |
| the export is not a function | `error` with `<entry> function is missing` |
| the module fails to load or evaluate, or the call throws `null` or `undefined` | `escaped`, exit code 1 |
| the error object cannot be built or serialised (a BigInt or a cycle in it, a throwing getter) | `escaped`, exit code 1; the process lives on |
| the result is larger than `max_message` | `escaped`, with a tail that says so, or with none when that frame does not fit either; if no frame fits, the harness exits with code 5 |

Milestone C3a replaces this with Windmill's wrapper rules: binding by the
recorded signature, `Date` hydration, the WR-13 replacer, the WR-18 error
shape with `step_id` and `extra`, `process.exit` interception, and the
attribution of process-level errors. Until then a `process.exit` ends the
whole process, and the engine fails every invocation in it as escaped.
Per-invocation logs and child-process wrapping (C3b) are not there either:
the runtime sends no `log` frames.

Exit codes: 0 after `bye` or when the engine's end closes, 2 for a rejected
or unusable welcome, 3 for a protocol error by the engine, 4 when the
bundle cannot be read, 5 when no frame that ends an invocation fits
`max_message`. The invocation stays in flight until such a frame is queued,
so the engine always learns how it ended, by that frame or by the exit.

## Tests

`test/frames.test.ts` checks this codec against the golden frames that the
Go tests write, `conformance/protocol/frames.json`. `test/sender.test.ts`
checks the send queue against a socket that accepts a few bytes at a time:
priority, the order per invocation, partial writes and `whenFlushed`.
`test/context.test.ts` checks the environment view's rules in one process,
and `test/host.test.ts` the SDK's refusals: the working directory and an
engine that cannot start. All are
developer checks, run by CI's non-required `Bun harness checks` job. The
merge gate is the Go side: [internal/harness](../../internal/harness/README.md)
drives this runtime through every exchange in
`conformance/protocol/exchanges/` and runs the Q-WA-4 test, and
[cmd/windmill-lite](../../cmd/windmill-lite/README.md) runs host mode end to
end in a host compiled with `bun build --compile`.

```sh
cd harnesses/bun && bun test
```

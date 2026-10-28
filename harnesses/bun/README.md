# harnesses/bun

**Current state:** the runtime of milestone C1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)), with what the
walking skeleton (W1) added (the per-invocation context, the facade fetch,
the process-level error handlers, host mode, and a minimal host SDK) and
the step runtime core of milestone C3a: Windmill's wrapper rules, the
`process.exit` interception, the attribution of process-level errors, the
per-invocation registry and the process scope; and milestone C3b's
settle-time fence, per-invocation logs, child-process wrapping and `fresh`
mode. This
README covers `harnesses/bun`: `runtime/`, the harness side of the protocol
of [ADR 0004](../../docs/design/0004-harness-protocol.md); `host/`, the Bun
host SDK of [ADR 0009](../../docs/design/0009-host-adapters.md) in W1's
minimal form (F3a completes it); and `test/`, the developer checks. The
build helper of milestone C2a, which bundles scripts and parses their
signatures, has its own README: [build/](build/README.md). The harness runs
on the pinned Bun 1.3.10, the version of Windmill v1.767.0's images
([WR-3](../../docs/semantics/languages.md#wr-3)). The runtime and the host
SDK have no dependencies and no `package.json`; only the build helper has
one, its TypeScript parser.

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
| `runtime/main.ts` | Reads the bundle's digest, registers its host-provided modules, connects to fd 3, starts the runtime |
| `runtime/runtime.ts` | The handshake, the step runner, cancel, health, drain, platform calls, and host mode's runs |
| `runtime/wrapper.ts` | Windmill's wrapper rules: binding, `Date` hydration, the result and error serialisation |
| `runtime/context.ts` | The per-invocation context: the environment view, the fetch view, the timer fence, the log view, child-process wrapping, the process-level error handlers, the `process.exit` interception, the registry and the process scope |
| `runtime/frame.ts` | The codec and the send queue: canonical envelopes, the decoding rules and error codes of the Go side, part streams, and the order of messages about one invocation |

- **Handshake.** It sends `hello` with `Bun.version`, its pid, the SHA-256
  of the bundle's `manifest.json`, and the `fetch` and `fresh` capabilities
  (in host mode `host` instead of `fresh`, since host mode always runs
  warm; `native` is false). It accepts a warm welcome, or a fresh one
  outside host mode.
- **Heartbeat.** After the welcome it sends a `ping` every `heartbeat_ms`
  with its health: invocations in flight (those whose outcome is still
  open), abandoned ones, the process-level errors no invocation owned, the
  event loop's lag (how late the heartbeat timer fired) and RSS. It answers the engine's pings
  with the same.
- **Invocations** run concurrently on the event loop, each inside its own
  `AsyncLocalStorage` context with its id, run, environment, abort signal
  and resource registry. A script's module, `scripts/bun/<id>.mjs` in the
  bundle, and its metadata, `<id>.meta.json`, are read once per process on
  its first invocation, inside that invocation's context (warm module
  state).
- **Host-provided modules.** A harness the engine spawned registers the
  bundle's copy of each, `scripts/bun/provided/<id>.mjs`, in the order of
  the manifest's `host_modules`, before it greets the engine
  ([ADR 0010](../../docs/design/0010-packaging.md), D12); a copy that fails
  to load fails the start (exit code 4). `main.ts` makes the runtime
  first, so every process-wide hook below (the process scope included) is
  in place when the copies load. In host mode the host registers its own
  instances instead.
- **Cancel** abandons the invocation. When its call ends, the outcome goes
  out as the `late` frame that settles it.
- **Fresh mode** ([ADR 0006](../../docs/design/0006-module-state.md)): the
  process serves one invocation (a second `invoke` is a protocol error)
  and exits as soon as its result has been written to the socket, however
  large, as Windmill's wrapper exits after writing the result.
  `process.exit` and an uncaught error or unhandled rejection end the
  process at once, with its code or with 1: what is queued, the
  invocation's log included, is written straight to file descriptor 3
  (`Transport.writeSync`, which waits for a full socket), and the real
  `process.exit` follows, so no `catch` or `finally` block of the step
  runs (D9, D11). The engine kills it on timeout or cancellation.
- **Drain** stops the heartbeat. Once everything queued has gone out, the
  runtime sends `bye`, its last frame, and exits when its socket has closed,
  whatever is still running.

## The per-invocation context

`runtime/context.ts` installs these once, before any step module
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
  measures every channel. A step's first write of a variable the process
  has makes it an accessor too, so `Bun.env` reads the written value. A
  write through the original object itself (`Bun.env.X = …` in code the
  build does not rewrite) stays with the invocation: one of a variable the
  process lacks reaches the prototype's `set` trap, one of an accessor its
  setter. Two still reach the process, since an ordinary object cannot
  trap them: a delete through the original object, and a write of a
  process variable that no invocation has set yet. Enumerating `Bun.env`
  lists the process's variables; enumerating `process.env`, which the
  build's define rewrite puts in its place, lists the invocation's view.
- **The fetch view.** `globalThis.fetch` is wrapped. A request to the
  welcome's `facade` origin becomes a `platform` frame attributed to the
  invocation whose context made it, with its body's bytes, and its
  `platform_reply` becomes the `Response`. Any other request passes
  through, with a signal of the fence's added to its own. A facade request
  made outside every invocation, or after its invocation settled, is
  refused with a `TypeError`, and one whose signal has already aborted is
  not sent. One still waiting when its invocation settles is aborted, and
  the reply that comes later is dropped. Any other request stays
  registered until its response's body has been read to the end, canceled
  or failed: the reading methods (`text`, `json` and the rest) are the
  response's own, and `body` is one stream that passes the native body's
  chunks through, so the `Response` API is unchanged. A response with no
  body (a 204, 205 or 304, or the answer to `HEAD`) ends the request at
  once; one whose body the step never reads stays registered until
  settlement, which aborts it.
- **The timer fence.** `setTimeout`, `setInterval` and `setImmediate`, on
  `globalThis` and on `node:timers`, register each handle a step creates
  in its invocation's registry; the clear functions, and a timeout or
  immediate that fires, unregister it. A `refresh()` that re-arms a
  timeout that fired registers it again, with the invocation that made
  it, wherever it is called; once that invocation has settled, `refresh()`
  re-arms nothing. Bun's `util.promisify` form is kept. Handles created outside every invocation, in a process scope, or
  while a step module evaluates are not registered. `node:timers` is
  patched before any step module imports it. On Bun 1.3.10 the first named
  import of a builtin fixes its named bindings for every later importer:
  in host mode, a host whose own modules import `node:timers` or
  `node:child_process` by name before it connects leaves steps' named
  imports of them unwrapped (default imports and `require` always see the
  wrappers), which is why the host SDK requires `node:child_process`. The
  runtime's own timers use `realTimers`, the functions as Bun has them.
- **The log view.** Inside an invocation, `console`'s methods and
  `process.stdout.write` and `process.stderr.write` write to the
  invocation's log (`InvocationLog`). A console call is formatted by a
  Node `Console` of the invocation's own, without colours, so its
  counters, timers and groups are its own. Lines are merged per stream,
  empty ones dropped ([WR-23](../../docs/semantics/languages.md#wr-23)),
  and sent as `log` frames, batched until the code that wrote them has run
  on and always ahead of any other frame about the invocation; a line too
  long for a frame is cut, ending in `…`. The log keeps the last 600
  characters for the tail of an escaped outcome
  ([WR-20](../../docs/semantics/languages.md#wr-20)). Outside every
  invocation both write to the process's streams, and the runtime's
  diagnostics always go to its real stderr. `Bun.stdout`, a raw file
  descriptor and a child's inherited stdio are not attributed.
- **Child processes.** `child_process`'s `spawn`, `exec`, `execFile` and
  `fork`, their sync forms, and `Bun.spawn` and `Bun.spawnSync` give a
  child started inside an invocation without an `env` the invocation's
  view, a plain copy of `process.env`; on Bun 1.3.10 `spawnSync`,
  `execSync` and `Bun.spawn` would otherwise pass the process's own
  environment. `util.promisify(exec)` and `util.promisify(execFile)` keep
  their `{stdout, stderr}` form. A child still running is registered as
  `child`, and left running.
- **Process-level error handlers.** `uncaughtException` and
  `unhandledRejection` handlers that never exit
  ([ADR 0004](../../docs/design/0004-harness-protocol.md), the
  shared-process failure model, item 1). Each error is written to stderr
  and attributed through the context the handler runs in (item 2): on Bun
  1.3.10 a throw from a timer keeps its invocation's context, and an
  unhandled rejection or a microtask's throw has none.
  - In a **running** invocation's context, the error fixes its outcome:
    `escaped`, exit code 1
    ([Q-WR-2](../../docs/semantics/languages.md#q-wr-2)), the error's text
    as its tail, in a `result` frame with `settled` false; how the call
    then ends is the `late` frame that settles it.
  - In an invocation whose outcome is fixed, that was abandoned or that
    has settled, it is a `late` error that settles nothing (D9).
  - In **no** invocation's context it fails nothing (item 3): it is counted
    in health's `errors`, and every invocation in flight gets a `warning`
    frame with code `unattributed_error`, whose message is cut, ending in
    `…`, to fit `max_message` (`fitString`).

  The handlers never throw: a failure to report an error is written to the
  diagnostics, since a throw from one handler would reach the other, and
  one thrown from there ends the process.
- **`process.exit`** is replaced (item 6, D11). Inside an invocation the
  call fixes its outcome, `empty` for status 0 and `escaped` with the
  status otherwise, and throws a `ProcessExit` sentinel, so `finally`
  blocks run and a `catch` can swallow it; the call's end is reported late,
  and a sentinel that ends it gives a `late` error without a body. The
  status is Bun's (`exitStatus`): a safe integer (within ±(2^53 − 1)), or
  a string other than `""` that `Number` reads as one (`" "` is 0), modulo
  256; no code means `process.exitCode`, or 0. A code Bun refuses makes
  the call throw Bun's own `RangeError` or `TypeError` and fixes nothing:
  the code is assigned to `process.exitCode`, which Bun checks the same
  way and leaves unchanged when it refuses. `test/context.test.ts` checks
  `exitStatus` against a real Bun's `process.exit` for each of 38 codes. A call from an
  invocation whose outcome is already fixed, or that has settled, changes
  nothing and is reported late. Outside every invocation it is the real
  `process.exit`; the runtime and the host SDK exit through `exitProcess`,
  the real one, since their own code may run in an invocation's context.
  `process.reallyExit`, `process.abort` and signals still end the process.
- **The registry and the settle-time fence (D9).** Each context holds an
  `InvocationResources`: `track(kind, release)` registers something the
  invocation started, and returns the function that unregisters it once
  it ended by itself. When the runtime queues the frame that settles the
  invocation, it first sends a `warning` with code `left_behind` whose
  `left` counts what is still registered by kind (`pending()`), if
  anything is. Then it calls `settle()`, which runs every release still
  pending, once, and closes the invocation's log. So the invocation's
  pending timers are cleared, its requests aborted, its later log writes
  dropped and its later platform calls refused; a child is left running.
  Anything registered after settlement is released at once. The
  connection's end settles every invocation too. Nothing global is keyed
  by invocation, and the counts are the only leak report:
  `process._getActiveHandles()` lists nothing on Bun 1.3.10. The fence
  never touches sockets or shared objects, and cannot stop promise
  continuations already queued (`Bun.sleep`, `node:timers/promises`); what
  they do through the fenced channels is refused.
- **Module evaluation.** A step module evaluates inside its first
  invocation's context, so its top-level code reads that invocation's
  environment, writes to its log, and fails it by throwing. Its context's
  `work` is empty, though, so the timers, requests and children its
  evaluation starts are the module's (D1), never registered or fenced: a
  client a module makes at load keeps its maintenance timers (case
  `D9/module-evaluation`). Once the loading invocation has settled, what
  that work writes goes to the process's streams, and its platform calls
  are refused, as from a process scope.
- **The process scope.** `globalThis[Symbol.for("windmill-lite.processScope")].capture()`
  returns a runner for the caller's async context without the invocation's
  store: every other store (a host's tenant scope) stays. A pooled client
  or a host-provided module captures it at load and runs its factories
  through it: their maintenance timers then belong to no invocation, are
  never attributed to the step that happened to create the first client,
  and the fence never clears them; an error they throw is unattributed
  (cases `D12/pool-monitor` and `D12/pool-handshake-timeout`, with
  `D12/pool-handshake-without-scope` as their control).
  Loading outside every invocation is not enough: a function a module
  exports runs in its caller's context, so a factory a step calls must
  re-enter the captured scope (case `D12/pool-process-scope`, and
  `D12/pool-without-scope` as its control). The API is published when `runtime/context.ts`
  is evaluated: in a spawned harness before the bundle's host-provided
  copies load, and in host mode as soon as the host imports the host SDK,
  so a host imports the SDK before the modules of its own that capture the
  scope.

The fetch wrapper, the handlers and the `process.exit` interception are
installed once per process and answer to the runtime that installed them
last, so a host that connects again counts its errors and sends its facade
calls on its current connection.

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

`run` applies Windmill's Bun wrapper rules (`wrapper.ts`) in the wrapper's
order ([WR-2](../../docs/semantics/languages.md#wr-2)):

1. **The signature.** The entrypoint's signature is what the build
   recorded under its name in `<id>.meta.json`: for every export, and for
   every top-level function declaration, exported or not, as Windmill's
   parser reads them ([WR-7](../../docs/semantics/languages.md#wr-7))
   ([ADR 0002](../../docs/design/0002-ir-and-loader.md)). One that did not
   parse fails the step before its module loads, as Windmill's worker
   fails the job before its process starts, with
   [WR-37](../../docs/semantics/languages.md#wr-37)'s object, as an
   `error` outcome. Metadata that cannot be read is `escaped`.
2. **The module** loads; a failure to load or evaluate it is `escaped`
   ([WR-19](../../docs/semantics/languages.md#wr-19)), on every invocation
   of the script, and an intercepted `process.exit` during its evaluation
   ends every invocation of it as that exit did.
3. **The export** must be a function, or the step fails with `<entry>
   function is missing` in the wrapper-catch shape
   ([WR-7](../../docs/semantics/languages.md#wr-7),
   [WR-12](../../docs/semantics/languages.md#wr-12)). An exported function
   whose signature the bundle does not record is `escaped`: the bundle is
   broken.
4. **Binding** ([WR-6](../../docs/semantics/languages.md#wr-6)): with
   parameters, each one's value by name, read as destructuring reads it
   (a missing key gives `undefined`, `null` stays, extra keys are dropped,
   nothing is coerced), a `Date` parameter hydrated by the truthiness rule
   ([WR-8](../../docs/semantics/languages.md#wr-8)); with none, the values
   of the non-underscore keys when the arguments carry
   `_ENTRYPOINT_OVERRIDE` ([WR-V1](../../docs/semantics/languages.md#wr-v1)),
   and no arguments otherwise. The engine chose the export from the same
   argument (`engine.Entrypoint`).
5. **The call** is awaited, after the BigInt patch is installed, and the
   result serialised as `JSON.stringify(value ?? null)` with the
   `undefined` replacer ([WR-13](../../docs/semantics/languages.md#wr-13),
   [WR-14](../../docs/semantics/languages.md#wr-14)).
6. **A throw** from the export check, the binding, the call or the
   serialisation is printed to the invocation's stderr, its log, and
   shaped as
   [WR-18](../../docs/semantics/languages.md#wr-18)'s object: `message`,
   `name`, `stack`, `step_id` from `WM_FLOW_STEP_ID` as the step sees it,
   `extra` with the other own properties. When that object cannot be built
   or serialised (`throw null`, a cycle in `extra`) the failure is
   `escaped`, with the handler's error at the end of its log.

| The call | Outcome |
|---|---|
| returns | `ok` with the result's text |
| throws, or its signature did not parse | `error` with the object above |
| the module fails, `throw null`, an unserialisable error, an attributed process-level error | `escaped`, exit code 1; the error's text ends the log, whose last 600 characters are the tail |
| calls `process.exit(n)` | `escaped` with exit code `n` and the log's tail, or `empty` for 0; the call goes on (D11), or in fresh mode the process exits |
| the result is larger than `max_message` | `escaped`, with a tail that says so, or with none when that frame does not fit either; if no frame fits, the harness exits with code 5 |

The engine maps each outcome to the value Windmill stores
(`engine.StoreOutcome`, [internal/engine](../../internal/engine/README.md)),
which keeps the tail's text after the last `CODE EXECUTION ---` marker,
trimmed (WR-20).

Exit codes: 0 after `bye`, when the engine's end closes, or in fresh mode
after the result (otherwise the step's own), 2 for a rejected
or unusable welcome, 3 for a protocol error by the engine, 4 when the
bundle cannot be read, 5 when no frame that ends an invocation fits
`max_message`. The invocation stays in flight until such a frame is queued,
so the engine always learns how it ended, by that frame or by the exit.

## Tests

`test/frames.test.ts` checks this codec against the golden frames that the
Go tests write, `conformance/protocol/frames.json`. `test/wrapper.test.ts`
checks the wrapper's rules, and `test/fit.test.ts` how a warning's message
is cut to fit `max_message`. `test/sender.test.ts`
checks the send queue against a socket that accepts a few bytes at a time:
priority, the order per invocation, partial writes and `whenFlushed`.
`test/context.test.ts` checks the environment view's rules in one process
(a first write of a process variable included), the registry, the process
scope and the exit status; `test/fence.test.ts` the settle-time fence (the
timers, the fetch view and a refused platform call), the log view, the
`Bun.env` write trap and the environment of child processes;
and `test/host.test.ts` the SDK's refusals: the working directory and an
engine that cannot start. `test/build.test.ts` checks the build helper's
scans and parse. All are developer checks, run by CI's non-required
`Bun harness checks` job. The merge gate is the Go side:
[internal/harness](../../internal/harness/README.md) drives this runtime
through every exchange in `conformance/protocol/exchanges/` and runs the
Q-WA-4 test, [cmd/wltest](../../cmd/wltest/README.md) runs the step suite
of `conformance/steps` through it, [internal/bundle](../../internal/bundle/README.md) runs the
build helper, and [cmd/windmill-lite](../../cmd/windmill-lite/README.md)
runs host mode end to end in a host compiled with `bun build --compile`.

```sh
(cd harnesses/bun/build && bun install --frozen-lockfile)
cd harnesses/bun && bun test
```

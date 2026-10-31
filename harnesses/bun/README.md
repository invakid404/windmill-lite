# harnesses/bun

**Current state:** the runtime of milestone C1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)), with what the
walking skeleton (W1) added (the per-invocation context, the facade fetch,
the process-level error handlers, host mode, and a minimal host SDK) and
the step runtime core of milestone C3a: Windmill's wrapper rules, the
`process.exit` interception, the attribution of process-level errors, the
per-invocation registry and the process scope; milestone C3b's
settle-time fence, per-invocation logs, child-process wrapping and `fresh`
mode; the native boundary mode of milestone C3n, which runs `nativets`
and `//native` steps with the native runtime's rules (deviation D5); and
milestone PF1, which gave intercepted replies their reason phrase, kept a
result behind its own platform call, and fixed the borrowed reading
methods of the fetch view (below); and milestone FU6, which gave each
invocation's children a process group of their own
([WR-55](../../docs/semantics/languages.md#wr-55), below); milestone
F3a, which completed the host SDK; and milestone F2c, host plugins
answered over `host_call` (below).

This README covers `harnesses/bun`: `runtime/`, the harness side of the
protocol of [ADR 0004](../../docs/design/0004-harness-protocol.md);
`host/`, the Bun host SDK of
[ADR 0009](../../docs/design/0009-host-adapters.md); and `test/`, the
developer checks. The build helper, which bundles scripts and parses their
signatures, has its own README: [build/](build/README.md). The harness runs
on the pinned Bun 1.3.10, the version of Windmill v1.767.0's images
([WR-3](../../docs/semantics/languages.md#wr-3)).

The runtime and the host SDK have no dependencies and no `package.json`.
The build helper has one, for its TypeScript parser. The type check in
`test/typecheck` has one for TypeScript and Bun's type declarations, which
only developers and CI install ([Type check](#type-check)).

The runtime also runs a bundle's shared code (below,
[ADR 0013](../../docs/design/0013-shared-module-code.md)).

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
| `runtime/main.ts` | Reads the bundle's digest, extracts its codebase assets, registers its host-provided modules, loads the host plugin `--plugin` names, connects to fd 3, starts the runtime |
| `runtime/plugin.ts` | Host plugins (F2c): the `Plugin` type a host writes, its declaration, the answer to each `host_call`, `loadPlugin` |
| `runtime/runtime.ts` | The handshake, the step runner, cancel, health, drain, platform calls, and host mode's runs |
| `runtime/wrapper.ts` | Windmill's wrapper rules: binding, `Date` hydration, the result and error serialisation |
| `runtime/native.ts` | The native boundary mode: a realm per invocation with the native runtime's globals, the boundary between it and the web APIs, the preamble, imports and the embedded client, binding, the result and the error shapes |
| `runtime/context.ts` | The per-invocation context: the environment view, the fetch view, the timer fence, the log view, child-process wrapping, the process-level error handlers, the `process.exit` interception, the registry and the process scope |
| `runtime/frame.ts` | The codec and the send queue: canonical envelopes, the decoding rules and error codes of the Go side, the frame reader, whose buffer grows by doubling so that a frame read in small pieces is copied a bounded number of times, part streams, and the order of messages about one invocation |

- **Handshake.** It sends `hello` with `Bun.version`, its pid, the SHA-256
  of the bundle's `manifest.json`, and the `fetch`, `native` and `fresh`
  capabilities (in host mode `host` instead of `fresh`, since host mode
  always runs warm, and the working directory as `workdir`). Its harness
  version is the release it belongs to (`runtime/version.ts`): `dev` in
  the repository, which a release stamps with `--define WL_RELEASE`
  (F3b). It accepts a warm welcome, or a fresh one outside host mode; in
  host mode it refuses a welcome from an engine of another release with
  `version_mismatch` (`versionsPair`, ADR 0009). A welcome that refuses
  it is reported to `onReject` with its code and message.
- **Heartbeat.** After the welcome it sends a `ping` every `heartbeat_ms`
  with its health: invocations in flight (those whose outcome is still
  open), abandoned ones, the process-level errors no invocation owned, the
  event loop's lag (how late the heartbeat timer fired) and RSS. It answers the engine's pings
  with the same.
- **Invocations** run concurrently on the event loop, each inside its own
  `AsyncLocalStorage` context with its id, run, environment, abort signal
  and resource registry. A script's metadata, `scripts/bun/<id>.meta.json`
  in the bundle, and its module are read once per process on its first
  invocation, inside that invocation's context (warm module state). The
  module is `scripts/bun/<id>.mjs`, or, for a codebase script (metadata
  `bundler: "esbuild"`, milestone C2b), `<id>.cjs` when it is CommonJS,
  which Bun's CommonJS loader runs (D13). A codebase module loads from a
  copy in the working directory, `.windmill-lite-<16 hex digits of the
  id's SHA-256><ext>`, as Windmill places a codebase bundle as `main.js`
  in the job directory beside its assets
  ([WR-50](../../docs/semantics/languages.md#wr-50)): its `__dirname`,
  `__filename` and `import.meta`, and the relative paths its own `require`
  resolves at run time, are the working directory's (`moduleFile`).
- **Metadata.** The runtime keeps only the metadata fields it reads
  (`runtimeMeta`: `bundler`, `format`, `boundary`, `native`, `signatures`,
  `shared`), so a process does not hold every script's metafile.
- **Shared code** ([ADR 0013](../../docs/design/0013-shared-module-code.md);
  its differences from a per-script bundle are deviation [D18](../../docs/design/0001-equivalence-contract.md#5-named-deviations)). In warm
  mode and host mode, a script whose metadata names a closure
  (`shared.closure`) loads its shared-layout module, `<id>.shared.mjs` or
  `<id>.shared.cjs`. A codebase script loads it from the same
  working-directory copy. Fresh mode, and a script that keeps its
  per-script bundle (`shared.fallback`), load the per-script one.
- **The closure registry.** Before any module loads, the runtime installs
  a process-wide registry, `globalThis[Symbol.for("windmill-lite.closures")]`
  (`installClosures`). Its `instance(id, scope)` loads the closure factory
  `scripts/bun/closures/<id>.cjs` once per process, or the ES module
  `<id>.mjs` of an ES module codebase group, whose factory takes no
  `module` or `exports`.
- **An instance per step module.** Each call makes a fresh instance with
  the calling module's `module` and `exports` (a CommonJS module's; an ES
  module passes neither), `require`, file names and `import.meta`, which
  its dependency code sees as in the per-script bundle. The closure hands
  the instance's table back through
  `globalThis[Symbol.for("windmill-lite.closure-table")]`, which the
  registry clears at once.
- **What it costs.** Each step module has an instance of its own, so module
  state stays per script while the dependency code is compiled once. A
  dependency module initialises in an instance on its first reference.
  Loading a closure runs none of its modules.
- **Codebase assets.** A harness the engine spawned copies the bundle's
  `assets/` into its working directory, at their paths there, before any
  module loads (`extractAssets`; [ADR 0002](../../docs/design/0002-ir-and-loader.md),
  decision 3; D10); one that fails to extract them fails the start (exit
  code 4). In host mode the SDK extracts them into the host's working
  directory (F3a).
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
- **Host mode's runs** (F3a). `submit` sends a `run` and resolves with
  its `run_done`, or rejects with a `RunError` (`draining`, `refused`) for
  a run the engine did not start; its `onSettled` is called on
  `run_settled`, in the snapshot of the submission's async context and
  outside every invocation. `drainEngine` sends the host's `drain`, behind
  every `run` queued before it (`Sender.sendAfter`), and calls back on the
  engine's `bye`. `hostHealth` adds the age
  of the oldest abandoned invocation and the number of script modules
  loaded to the health pings carry, and `onHeartbeat` hears it at every
  heartbeat.
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
  `platform_reply` becomes the `Response`, with the reason phrase Go's
  `net/http` writes for its status as `statusText`, and no body for a
  `HEAD` request or a null-body status (204, 205, 304), so that a client
  sees the same response as from the facade's TCP transport (WA-30/a; an
  `ApiError` carries the reason phrase, WA-41/a). Only a warm or host-mode process's
  welcome names the facade; a fresh process's requests to it go over TCP
  like any other (ADR 0007). Any other request passes
  through, with a signal of the fence's added to its own. A facade request
  made outside every invocation, or after its invocation settled, is
  refused with a `TypeError`, and one whose signal has already aborted is
  not sent. One still waiting when its invocation settles is aborted, and
  the reply that comes later is dropped. Any other request stays
  registered until there is positive evidence that its body's transfer
  has ended (`observeTransfer`): a reading method (`text`, `json` and the
  rest) of the response, or of a clone made while the body was unused,
  that consumed the body and fulfilled, which it does only once the
  source has reached its end. Bun marks a body used as soon as a read
  starts, a reader is taken, or it is canceled, teed or piped, so a call
  on a used body is no evidence. Nothing else is: no stream, reader or
  pipe event, no rejection, no cancellation. A body read any other way,
  whose read fails, or that is never read, stays registered until
  settlement, which counts it and aborts whatever is still arriving: too
  high a count, never too low. The step gets Bun's own response: only its
  reading methods and `clone` are own properties, calling the native
  ones, and a reading method returns the native promise's continuation, so
  a rejection reaches the step unchanged, unhandled if the step leaves it
  so (case `D9/fetch-read-unhandled`). They act on the evidence only when
  called on that response: borrowed for another receiver
  (`res.text.call(other)`), a reading method calls the native one on it and
  leaves the request registered, and only the response's own clone is
  observed (review item R4-1 of C3b, fixed in PF1). Its body, stream,
  readers and pipes are untouched. A response with no body (a 204, 205 or 304, or the answer
  to `HEAD`) ends the request at once.
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
  their `{stdout, stderr}` form.

  **Process groups** ([WR-55](../../docs/semantics/languages.md#wr-55),
  milestone FU6). A Windmill job leads its own process group, which its
  children join unless they start a session of their own; a timeout or a
  cancellation kills the group, and a normal exit kills nothing. On Bun
  1.3.10 every `node:child_process` function starts its child through
  `Bun.spawn` or `Bun.spawnSync`, once, with `detached` set only from
  `spawn` and `fork`, where Bun honours a step's own (it ignores it for
  `exec`, `execFile` and the sync forms). So the wrappers of the two Bun
  functions alone register children (a child is counted once) and:
  - **in a warm or host process** (`setChildGroups(true)`, from the
    welcome), start each child an invocation starts detached, so it leads
    a group of its own that stands for the job's, and register it as
    `child`: alive while a process is left in the group, released by
    killing the group with `SIGKILL`, so what the child started is reached
    after it exited. Before the step's code goes on, the group is
    announced to the engine in a `child` frame (`setChildAnnouncer`, which
    the runtime sets): `Sender.flushFramesSync` writes it, after the frame
    in progress, straight to the socket's file descriptor, so it reaches
    the engine even if the step then blocks the event loop, and the engine
    kills the group on the invocation's timeout or cancellation and when
    the process ends (a host, whose transport has no synchronous write,
    writes it on its own channel instead, described under host mode). The
    engine alone signals the group, at the invocation's abandonment, its
    settlement and the process's end, and only while it is still the
    child's; the harness never signals a child's group, since it cannot
    tell the group from a later one given the same id. A `cancel` revokes
    `child` (`InvocationResources.revoke`), so those children, and any the
    call starts afterwards, are no longer counted. Each grouped child is
    started through `guardedCommand`'s `/bin/sh -c` prologue, whose
    subshell starts the group's guardian (`sleep 999999999.<token>`, with
    descriptors 0 to 9 closed) and exits before the shell execs the
    step's command, so `child.pid`, its streams and its exit status are
    the command's, and the guardian is no child of the command, which
    does not wait for it when it waits for all its children; the token
    goes in the `child` frame. A command that cannot be run as given, or an `argv0`
    other than the command, is spawned without a guardian, as Bun spawns
    it. A guarded child counts as `child` while its own process runs: its
    guardian keeps the group, so what it left there after it exited is
    killed with the group but not counted.
    A synchronous call (`Bun.spawnSync`, and so `spawnSync`, `execSync`
    and `execFileSync`) gets no group: its child's pid is known only once
    it has exited, too late to announce, so it stays in the harness's
    group, which the engine kills when the process ends; what it left
    running is not attributed or reported;
  - **in a fresh process**, which the engine spawned as the leader of the
    job's group, leave each child in that group, which the engine waits
    for or kills: `child` is registered, with a release that does nothing;
  - in either, register a child the step started detached (where Bun
    honours it) as `detached`, alive while its group has a process, never
    killed: it is outside the job's group on Windmill too.

  `Bun.$` starts its processes by neither Bun function and is not wrapped.
  windmill-lite has no log limit, WR-55's third kill trigger.
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
  `InvocationResources`: `track(kind, release, alive?)` registers something
  the invocation started, and returns the function that unregisters it
  once it ended by itself; `alive`, for a resource whose end no event
  reports (a child's process group), says whether it still runs, and
  `pending()` drops one that has ended. When the runtime queues the frame that settles the
  invocation, it first sends a `warning` with code `left_behind` whose
  `left` counts what is still registered by kind (`pending()`), if
  anything is. Then it calls `settle()`, which runs every release still
  pending, once, and closes the invocation's log. So the invocation's
  pending timers are cleared, its requests aborted, its later log writes
  dropped and its later platform calls refused, and in a warm process its
  children's groups are killed; a `detached` child is left running.
  Killing a `child` there is windmill-lite's reading: Windmill holds the
  job open until such a process exits (WR-55), which a warm process cannot
  do without letting the step's code run after it settles (an open
  question in the [design README](../../docs/design/README.md)); a fresh
  process does what Windmill does. Anything registered after settlement,
  or after `revoke(kind)` for its kind, is released at once. The
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
(ADR 0009). It imports `../runtime/`, so a host vendors `host/` and
`runtime/` together, from one commit, and checks the copy's provenance
([ADR 0009, Vendoring the Bun host SDK](../../docs/design/0009-host-adapters.md#vendoring-the-bun-host-sdk)).

Its `plugin` option is the host's plugin (below, Host
plugins), whose declaration goes in the hello and which the engine checks
there, refusing a host it cannot use with `plugin_mismatch`; the SDK
re-exports the plugin types. `connect(options)`:

1. checks `workdir` (`checkWorkdir`): it must be a directory this process
   can write, else `workdir_not_writable`, and already the process's
   working directory once both are resolved without symbolic links, else
   `workdir_mismatch` (ADR 0004, the shared-process failure model, item
   5), on the cases of `conformance/protocol/host/workdir.json`, which the
   engine runs too; and reads the bundle's manifest (`bundle`);
2. registers the host's instances of the host-provided modules
   (`hostModules`, by the keys the host contract gives them) in the
   registry `Symbol.for("windmill-lite.hostModules")`
   ([ADR 0010](../../docs/design/0010-packaging.md), D12), before any step
   loads, with a diagnostic for a key the manifest does not list and for a
   listed module the host left out (a step that imports it fails at load);
3. extracts the bundle's codebase assets into `workdir` (`extractAssets`,
   as a spawned harness extracts them into its own directory: WR-49,
   WR-50, D10), or fails with `assets`;
4. starts the engine binary as `<engine> host --bundle <dir> --workdir
   <workdir> --kill-after <ms>ms` and `engineFlags` after them (such as
   `-variables` and `-resources`), with one end of a socketpair as its fd 3
   (Node's extra `"pipe"` stdio, which Bun 1.3.10 makes a socket), or fails
   with `engine_start`, and runs the runtime in this process in host mode.
   Its hello names the working directory (`workdir`), which the engine
   checks again; an engine that refuses the host makes `connect` fail with
   the reject's code (`workdir_not_writable`, `workdir_mismatch`,
   `engine_refused`, ...). The socket's backpressure
   reaches the runtime's send queue, so a small frame still waits behind at
   most one part frame. A second socketpair, the engine's fd 4, carries only
   the `child` frames that announce the steps' child groups (WR-55): the
   runtime's `children` option writes each, synchronously, to the host's
   end of it (its descriptor from the socket's handle, `_handle.fd` on Bun
   1.3.10), waiting for a full channel. The socket may hold bytes it has
   not written yet, which a synchronous write to it would overtake, and a
   step can back it up and then block the event loop, so the
   announcement cannot wait in its queue. Without a descriptor the
   announcements fall back to the socket, with a diagnostic.

It returns a `Host`:

- `run(path, args, options)` submits a run of a flow, or with
  `kind: "script"` a script, with the options of
  [Run options](#run-options). It resolves with the run's `id`, `status`,
  `result`, `mayHaveMutated`, `warnings`, `deviations` (H7, H10) and, for
  a latched run, `latched` (the error it latched). It rejects with a
  `HostError` when the engine does not start the run: `draining`, or
  `refused`. The scope the caller has when it calls `run` is captured
  with `AsyncLocalStorage.snapshot()`, and every invocation of the run
  re-enters it (H11).
- `health()` reports invocations in flight and abandoned, the age of the
  oldest abandoned one, unattributed errors, lag, RSS, the script modules
  loaded (`modules`), the runs in flight, whether it drains, and the
  recycle trigger that tripped (`recycle`, or null).
- `drain(timeoutMs)` refuses new runs (`draining`), here and in the engine,
  to which it sends `drain` behind every run submitted before it, whole
  (the send queue's `sendAfter`), and waits up to the timeout for the
  engine's `bye`, which comes once the runs in flight have ended and their
  outcomes have gone out whole, and for every caller of `run` to have its
  outcome; the host may then exit (ADR 0004 host mode item 3).
- `close()` ends the connection and resolves once the engine has exited;
  the host lives on.

The connect option `recycle` sets recycle triggers, none by default: `rss`
trips when the resident set is at least that many bytes on `samples`
consecutive heartbeats (default 2), `modules` when more script modules
are loaded than that, since a module stays loaded for the life of the
process (ADR 0006), and `retired` (F3b) when the host's pooled-connection
layer counts more entries retired and not released than its `max`: entries
it superseded whose leases an abandoned invocation may still hold, which
only the process's exit frees (ADR 0004 host mode item 5). The runtime checks them at every heartbeat, outside
every invocation (its `onHeartbeat`); the first that trips is reported by
`health()` and, once, to `onRecycle`. The host decides what to do, usually
to drain and exit so that its supervisor starts a new generation; the SDK
never drains on its own.

When the connection to the engine ends other than by `close()`, the SDK
exits the host with code 1 unless `onEngineExit` is given: without its engine a host runs nothing, and
its supervisor restarts it. The engine's watchdog kills the host when its
event loop stops answering (`watchdog.killAfter`, default 30 s, 0 disables
it). A step reads a host scope only through a host-provided module; the
bundle's step imports it through the shim the build generates, which
takes each export from the registry.

### Run options

The run's own hooks and budgets go in its run frame, and come ahead of the
plugin's.

| Option | What it does |
|---|---|
| `kind` | `"flow"`, the default, or `"script"`: a root script, run as a script job |
| `intercept` | Maps module ids to the results that stand in for those modules (H1's static form) |
| `signal` | Cancels the run when it aborts |
| `onSettled` | Called once the run has settled, after its outcome (`run_settled`, ADR 0004 host mode item 5), in the scope the run was submitted in (H11) and outside every invocation. Every invocation of the run has settled then, which may be never, so resources that an abandoned step may still use are released here. |
| `writeCapable` | The run's write-capable selectors (H3): the plugin's `beforeFirstWrite` is asked before the first of them launches |
| `haltBefore`, `haltAfter` | The run's own halts (below) |
| `fatalErrorNames` | Error names that latch the run instead of failing a step (H7) |
| `deadlineMs`, `preWriteDeadlineMs` | The hard and the pre-write deadline, in milliseconds from the run's submission (H4) |
| `clock` | `"real"`, or `"record"`, which returns every sleep and retry delay at once (H10) |

`haltBefore` and `haltAfter` end that run alone with status `halted`
([H8](../../docs/design/0008-host-hooks.md#the-hooks)). The run halts
before the first attempt of a module that a selector matches, or once
such a module has its final outcome. Other runs of the flow, on the same
connection too, run past the module, while the plugin's halts apply to
every run of the host.

`run` rejects with `refused` in these cases:

- The engine has no such target, or the arguments are not an object.
- `writeCapable`, `haltBefore` or `haltAfter` is not a list of objects.
  The SDK checks this and sends no frame (`runSelectors`), because the
  engine would read such a frame as broken and end the connection.
- The engine finds a selector with an unknown field, a wrong form, or an
  ambiguous module id. The error names the selector, such as
  `the run's haltAfter[0]`.

## Host plugins

A host plugin (milestone F2c, ADR 0008 and ADR 0004 §Host plugins) is a
`Plugin` (`runtime/plugin.ts`): `intercept` (with a static value or `fn`),
`rewriteArgs`, `haltBefore`, `haltAfter`, `around`, `onModuleStart`,
`onModuleEnd`, `invocationEnv`, `onRunEnd`, `onRunSettled`, `providers`
(the named providers of mandatory host-provided sites), and `resource`
and `variable`, the provider bridge of H13 (raw values the engine's
router interpolates). Each list entry has a selector, the host
contract's form; the engine matches them.

- **Where it comes from.** In host mode, the host SDK's `connect` option
  `plugin`. In a `warm` harness the engine spawned, the default export (or
  the export `plugin`) of the module `--plugin` names, which `main.ts`
  loads once the runtime's process-wide hooks are in place and before the
  hello (`Runtime.usePlugin`; the engine names it with
  `wl.Options.Plugin`).
- **The declaration** (`declaration`) goes in the hello as `plugin`: the
  hooks the plugin answers, each list's selectors, static values. A plugin
  whose shape is wrong (an interception with both or neither of `static`
  and `fn`, a hook that is no function) is refused before the hello.
- **Answers.** The runtime answers a `host_call` with `dispatch`, outside
  every invocation, and in host mode inside the scope the host had when it
  submitted the run (H11); that scope is kept until the run settles, for
  `onRunSettled`. A hook's throw replies `plugin_error`, a hook or entry
  the plugin lacks `unknown_hook`, and a harness without a plugin replies
  `unknown_hook` to every call.
- **`around`.** When an `invoke` names around wrappers, the runtime runs
  the step inside them, outermost first, in the invocation's context
  (`runAround`): each calls `next()` once and gets the step's outcome
  (`{outcome, result}`), which stays the step's own. A wrapper that throws
  or never calls `next()` is reported in a `plugin_error` warning before
  the invocation's result, which latches the run; a step that never ran
  then fails with an error the harness generated. A wrapper's `next()` is
  closed once the wrapper returns or the invocation is no longer running:
  a first call then rejects and starts nothing, so a continuation the
  wrapper left behind never runs the step after its invocation settled.
- **Payloads.** A hook's `StepRecord` and `Trace` carry every field a Go
  hook gets (`plugin.ts`): the arguments before H2, as stored and as
  resolved, the carried value, timings, late output, child flows, spans,
  deviations and `PlatformCall`s.
- **F2b's hooks.** A plugin may declare `writeCapable` selectors,
  `beforeFirstWrite` (a throw vetoes the write) and `fatalErrorNames`.
- **Halts.** The plugin's `haltBefore` and `haltAfter` halt every run of
  the host at the modules they match. A host that halts one run gives the
  halts in that run's options instead ([Run options](#run-options)).
- **The step API** (`installStepAPI`, `context.ts`). `wl` on `globalThis`
  gives step code `wl.latch(error)`, sent as a `latch` warning ahead of
  the step's result, which latches the run, and `wl.signal()`, the
  invocation's abort signal, which aborts with the `cancel`'s reason. Both
  throw a `TypeError` outside an open invocation (a plugin's hook latches
  by throwing); a step cannot replace `wl`; a native step's realm has
  none.

`test/caseplugin.ts` (with a report file its `onRunSettled` writes, of
each run and whether one of its hooks still ran) is the plugin of the cases of
`conformance/plugins/hooks.yaml`, which the Go tests load in `warm`
processes and in a compiled host.

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
   `error` outcome whose `origin` is `harness`. Metadata that cannot be
   read is `escaped`.
2. **The module** loads; a failure to load or evaluate it is `escaped`
   ([WR-19](../../docs/semantics/languages.md#wr-19)), on every invocation
   of the script, and an intercepted `process.exit` during its evaluation
   ends every invocation of it as that exit did.
3. **The export** must be a function, or the step fails with `<entry>
   function is missing` in the wrapper-catch shape
   ([WR-7](../../docs/semantics/languages.md#wr-7),
   [WR-12](../../docs/semantics/languages.md#wr-12)). An exported function
   whose signature a Bun-built bundle does not record is `escaped`: the
   bundle is broken, since the build signs every export of its output. A
   codebase module's metadata cannot always sign every callable export
   (CommonJS source has no static export list), so one it does not sign
   gets an empty parameter list, as Windmill's parser gives a function it
   does not find in the script's content
   ([WR-6](../../docs/semantics/languages.md#wr-6),
   [WR-7](../../docs/semantics/languages.md#wr-7)).
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

## The native boundary mode

An invocation whose `boundary` is `native` (a `nativets` step, or a
Bun-family one whose header says `//native`) runs with the rules of
Windmill's in-worker native runtime
([WR-40](../../docs/semantics/languages.md#wr-40) to
[WR-45](../../docs/semantics/languages.md#wr-45)), in `native.ts`. What
this mode cannot reproduce is deviation D5 of
[ADR 0001](../../docs/design/0001-equivalence-contract.md).

- **A realm per invocation.** Each invocation gets a fresh `node:vm`
  context, as each native job gets a fresh isolate, so module state and
  globals never survive it in any mode
  ([WR-41](../../docs/semantics/languages.md#wr-41)). Its global object has
  the ECMAScript built-ins and the web APIs WR-45 lists, taken from this
  realm once fetch is wrapped and before any step loads (`fetch`, `URL`,
  `URLPattern`, `crypto`, the streams, `structuredClone`, `console` and the
  rest; Bun 1.3.10 has no `FileReader`), a `Deno` object holding only an
  empty `core`, and `addEventListener`, `removeEventListener` and
  `dispatchEvent` on `globalThis`. Nothing of Bun's: no `Bun`, `Buffer`,
  `require`, `module`, `process`, `setImmediate` or `ShadowRealm`.
- **A boundary that fails closed.** Every promise and object a web API
  hands the realm is **tracked** as the calling invocation's the moment the
  boundary hands it over, or handed over **untracked** and **counted**. No
  path an object can travel is listed: the boundary sits on the realm's
  only ways into the web APIs, so what it cannot tell apart is counted
  rather than missed.
  - *Where it sits.* Every class WR-45 lists is given to the realm as a
    realm-facing subclass, made once per process, which inherits from its
    parent's (`CustomEvent` from `Event`, `AbortSignal` from
    `EventTarget`; Bun's `File` shares `Blob`'s prototype, so the realm's
    `File` is chained to the realm's `Blob`). Its constructor is the only
    way the realm makes one, and each member it has, its own, inherited,
    symbol-named and static ones, calls the original and hands on what it
    returns or throws. The function globals (`atob`, `btoa`,
    `reportError`, `structuredClone`) do the same; `crypto`,
    `crypto.subtle` and `performance`, which every caller shares, are
    objects of their subclasses that forward to them. What the harness
    calls back is wrapped where the realm hands it over: a method's
    callback argument (`FormData.forEach`); the callbacks of a stream's
    underlying source, sink, transformer and queuing strategy, called on
    the dictionary that holds them; and every listener and `on…` handler
    of every event target, which runs in the async context that
    registered it (identity kept for `removeEventListener`, and the
    handler reads back what it was given). Each gets its `this` and
    arguments handed on.
  - *What it hands on, decided once per value.* A primitive, or the
    realm's own object given back (a signal's `reason`), as it is. A
    promise of the harness realm is **tracked in place**: it takes the
    prototype of a `Promise` subclass whose `then` records the invocation
    on every promise it derives (`catch`, `finally` and `await` go through
    it) and hands on the value or reason, so a rejection nobody handles is
    attributed to the invocation, while a rejection the web API's own code
    handles (Bun's stream iteration, a stream's `closed`) stays handled. An
    instance of a WR-45 class takes its subclass's prototype (a harness
    `DOMException` is the realm's). An array, `Map`, `Set` or plain object
    has its contents handed on, all through (`structuredClone`'s result, a
    message's `data`, a key pair, `{value, done}`), once. An iterator, sync
    or async, gets a forwarder whose results are handed on. Buffers and
    their views, dates, regular expressions, errors and boxed primitives
    are data and handed on as they are, as is the JSON `json()` parses.
  - *What it only counts.* Anything else of the harness realm, an object
    of a class WR-45 does not list (Bun's `performance.timing`, a
    `PerformanceTiming`) or a harness function, is handed over as it is and
    counted on the invocation; `runtime.ts` sends the count as a
    `native_untracked` warning, its `untracked` field counting by kind,
    just before the frame that settles the invocation, beside C3b's
    `left_behind` one. One
    handed out in no native invocation's context, or after its invocation
    settled, cannot be counted on it: it is a `native_untracked` warning to
    every invocation in flight, or a diagnostic line. A promise of a web
    API's own internals that is never handed over is not the invocation's:
    a rejection of it nobody handles is an unattributed error.

  A fetched response's `url`, `redirected` and `type` are kept for it and
  every clone of it. The harness realm's own classes and objects are never
  changed; a web API's promise, buffer or parsed JSON is the harness
  realm's, so not an `instanceof` the realm's `Promise`, `ArrayBuffer` or
  `Object`.
- **Timers and fetch are the invocation's.** `setTimeout` and
  `setInterval` return numeric ids; each pending handle and each fetch in
  flight is registered with the invocation and released when it settles,
  as the isolate is dropped. The realm registers them itself, on Bun's own
  timers (`realTimers`, not the fence's wrappers) and through the fetch
  view run with no work registry, so each is registered once. A fetch
  stays registered until its response's body has been read to its end, has
  failed or was cancelled, whatever reads it (a clone or a reader
  included), so a body still open when the invocation settles is aborted
  and counted in the `left_behind` warning, as a pending timer is. `fetch`
  is the fetch view, so a request to the facade is a platform call; it
  sends `User-Agent: windmill/beta`, or the text of a `//useragent` header,
  unless the request sets one.
- **The module** is the script's native form, `scripts/bun/<id>.native.js`
  ([build/](build/README.md)): one async arrow function, so its scope has
  no `arguments`, as a module's has none, and its module-level `this` is
  `undefined`, compiled in the realm
  once per process for each set of preamble names. Its **preamble**
  declares `process = { env }`, `BASE_URL` and `BASE_INTERNAL_URL` (both the
  facade's origin: the welcome's, or in a fresh process, which the welcome
  does not tell, the `BASE_URL` it was spawned with), and for nativets
  every job variable as a `const` of its
  name; the values are the invocation's job variables and nothing else. A
  top-level declaration of one of those names fails the load.
- **Imports** resolve against `file:///eval.ts`. For a nativets script,
  every spelling of `file:///windmill.ts` is the embedded client
  (`scripts/native/windmill.js`), loaded in the same realm with its own
  preamble. Anything else, and anything for a `//native` script, cannot be
  loaded, since the harness reads no file of the host it runs on. It fails
  as a missing file does on Windmill's Linux worker
  ([WR-40](../../docs/semantics/languages.md#wr-40)):
  - a static import fails the load with `Failed to load <url>: No such file
    or directory (os error 2)`;
  - a dynamic `import()` rejects with a `TypeError` that the script can
    catch. Its message is `Failed to load <url>`, then the line
    `  Caused by:`, then the same reason indented by four spaces.

  A named import that the module does not export fails the link.
- **The call.** nativets always calls `main`; `//native` calls the entry,
  so an override applies. Each parameter takes its argument parsed in the
  realm, `undefined` when missing, never hydrated; with no parameters, no
  argument is passed. The result is `JSON.stringify(value ?? null)` in the
  realm, with no replacer: `undefined` fields are left out, a value with no
  JSON text is `null`, and a `BigInt` fails with V8's message.
- **Errors.** A thrown value, from the module, the call or the
  serialisation, is `{message, stack, name}`: for an `Error` of the realm
  or of a web API, name and message decoded together, and the stack with
  the source line its first frame points at in front when that frame is in
  the script; three `null`s for anything else. A transpile error, a
  signature that did not parse, a `//proxy` header (an EE feature) and a
  load or link failure are the worker's `execution error:` object. Both
  are `error` outcomes; the second kind's `result` frame says `origin`
  `harness`, as a Bun step's unparsable signature does (WR-37), so that the
  engine records the message as generated (ADR 0004, FU5).
- **Process-level errors** of a native invocation are its own: by its
  async context (a timer), by the realm of the promise (a rejection nobody
  handles, which has no context) or directly (a throw from a microtask it
  queued). One fails a running invocation with its WR-43 object; after its
  outcome it is a late error.
- **A promise nothing can settle.** When the entrypoint's promise is
  pending and the invocation holds nothing that could still settle it,
  the invocation fails at once with the worker's `execution error:` object
  and the text `Promise resolution is still pending but the event loop has
  already resolved`, as on Windmill
  ([WR-43](../../docs/semantics/languages.md#wr-43)). The harness checks
  every 10 ms and needs two such checks in a row.
- **What holds an invocation until the work ends:** a pending timer or
  request, or a pending promise that a web API handed it.
- **What holds an invocation until its timeout**, since the harness cannot
  see when the work ends:
  - an event listener or `on…` handler;
  - a call of `Atomics.waitAsync` or of WebAssembly's asynchronous
    compilation;
  - anything handed to it untracked, or handed to a realm with no
    invocation to own it since the invocation began.

Exit codes: 0 after `bye`, when the engine's end closes, or in fresh mode
after the result (otherwise the step's own), 2 for a rejected
or unusable welcome, 3 for a protocol error by the engine, 4 when the
bundle cannot be read, 5 when no frame that ends an invocation fits
`max_message`. The invocation stays in flight until such a frame is queued,
so the engine always learns how it ended, by that frame or by the exit.

## Tests

`test/frames.test.ts` checks this codec against the golden frames that the
Go tests write, `conformance/protocol/frames.json`. `test/wrapper.test.ts`
checks the wrapper's rules, `test/native.test.ts` the native boundary
mode (the build's native form, the realm's globals, the fetch user agent
against a local endpoint, settlement, the error shapes, which errors are
marked generated, and the boundary: every WR-45 class realm-facing, what
each way of handing an object over gives, and what it counts as
untracked, and when a pending entrypoint fails as one that nothing can
settle), and
`test/fit.test.ts` how a warning's message
is cut to fit `max_message`, and `test/describe.test.ts` the text the
harness's own messages give an error: Bun 1.3.10 builds the stack of an
error that a garbage collection formats before anything read it without
the message, which `describe` puts back. `test/sender.test.ts`
checks the send queue against a socket that accepts a few bytes at a time:
priority, the order per invocation (a result held behind a large
platform call of its invocation included), partial writes,
`whenFlushed`, and the barrier a host's `drain` takes behind large runs
under backpressure, with a pong still overtaking them.
`test/context.test.ts` checks the environment view's rules in one process
(a first write of a process variable included), the registry, the process
scope, the exit status and the step API (`wl.latch`'s texts and refusals,
`wl.signal()`, a `wl` no step can replace); `test/fence.test.ts` the settle-time fence (the
timers and `refresh()`, the fetch view and the evidence that ends a
body's transfer, with a native-versus-fenced comparison of the
response's semantics, a reading method or clone borrowed for another
response, an intercepted reply without a body for `HEAD` or a null-body
status, and a refused platform call), the log view, the `Bun.env` write trap,
the environment of child processes, and their process groups (the
liveness check and `revoke`, a group that outlives its first process, the
sync forms and `exec`, killed at settlement or on `revoke`, a `detached`
child left running, and a fresh process's children left in its group);
`test/plugin.test.ts` host plugins: the declaration, each hook's answer
and the `unknown_hook` and `plugin_error` refusals, and the runtime
answering a `host_call` in the run's scope; and `test/host.test.ts` the
SDK: the shared working-directory cases of
`conformance/protocol/host/workdir.json` through `checkWorkdir`, the
shared version-pairing cases of `conformance/protocol/host/versions.json`
through `versionsPair` (F3b), the refusals before the engine starts, the assets extracted into the
working directory before it does, and a run's `onSettled` running in its
own submission scope, for concurrent runs, and never for a refused one;
it removes the temporary directories it makes, as `test/fence.test.ts`
does its own (F3b). `test/build.test.ts` checks the build helper's
scans and parse. All are developer checks, run by CI's non-required
`Bun harness checks` job. The merge gate is the Go side:
[internal/harness](../../internal/harness/README.md) drives this runtime
through every exchange in `conformance/protocol/exchanges/` and runs the
Q-WA-4 test, [cmd/wltest](../../cmd/wltest/README.md) runs the step suite
of `conformance/steps` through it, [internal/bundle](../../internal/bundle/README.md) runs the
build helper, and [cmd/windmill-lite](../../cmd/windmill-lite/README.md)
runs host mode end to end in a host compiled with `bun build --compile`.

`test/host.test.ts` also checks that a run frame carries the run's
write-capable selectors and halts as the host gave them.

```sh
(cd harnesses/bun/build && bun install --frozen-lockfile)
cd harnesses/bun && bun test
```

### Type check

`test/typecheck/check.sh` typechecks `host/` and `runtime/` as a host that
vendors them as source sees them
([ADR 0009](../../docs/design/0009-host-adapters.md)). It copies both
directories into a temporary package of type module. There it runs
TypeScript 6.0.3 against `@types/bun` 1.3.10, the type declarations of the
pinned Bun, both from the lockfile in `test/typecheck`.

- Its `tsconfig.json` resolves modules as Node.js does (`NodeNext`).
- It runs `tsc` four times: with and without the DOM library, each in
  strict and in non-strict mode, and exits 1 if any run reports an error.
- CI's non-required `Bun SDK types` job runs it.

The code uses only names that Bun's type declarations define, with or
without the DOM library. Where those declarations miss what Bun does,
the code names the gap in a comment and casts once: `RequestOf` (a
`Request` built from a URL) and the file-descriptor form of `Bun.connect`.

To run it:

1. Install its dependencies:
   `(cd harnesses/bun/test/typecheck && bun install --frozen-lockfile)`.
2. Run `harnesses/bun/test/typecheck/check.sh`.

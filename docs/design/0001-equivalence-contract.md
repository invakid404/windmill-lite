# 0001 · Equivalence contract

**Status:** proposed · **Topic:** what windmill-lite promises to match, what it
leaves out, and how unsettled findings are handled.

## Context

The [semantics reference](../semantics/README.md) describes Windmill
`v1.767.0` as 321 findings: 251 `core`, 58 `later`, 7 `out`, 5 `EE`.
Twenty-one of them are not settled (`DISPUTED`, `needs-evidence` or `open`), and 110
question entries track what would settle them. Several findings say outright
that an equivalence decision is needed: loop collapse
([FX-46](../semantics/execution.md#fx-46)), module state
([Q-WR-19](../semantics/languages.md#q-wr-19)), jsonb ordering at the step
boundary ([Q-WR-18](../semantics/languages.md#q-wr-18)), unsupported platform
calls ([Q-WA-11](../semantics/client-api.md#q-wa-11)).

"Windmill's semantics" also needs a reference point. The same flow behaves
differently as a preview, during the deploy race or after a failed lock
([OF-3](../semantics/openflow.md#of-3),
[OF-7](../semantics/openflow.md#of-7), [CF-8](../semantics/control-flow.md#cf-8)).

## Options considered

1. **Match everything Windmill does**, platform included (queues, job rows,
   suspend). This rebuilds Windmill and defeats the project.
2. **Match data semantics against one fixed reference configuration, list
   everything else as out of scope or as a named deviation.** Chosen.
3. **Best effort**, fixing differences as users report them. This is how a
   hand-written mirror drifts; rejected.

## Decision

### 1. The reference configuration

windmill-lite matches Windmill CE `v1.767.0` running a flow that was
**deployed successfully** (the lite form, with materialised defaults and
virtual steps), started **by path as a root job**, and observed through the
API and through platform effects. For sequential work the reference is one
worker; for parallel containers it is a Windmill with enough workers that
every eligible child runs at once ([CF-7](../semantics/control-flow.md#cf-7),
[OR-5](../semantics/oracle.md#or-5)), so the interleaving of their effects is
unspecified and never compared. A fixture with blocking dispatch (a step
that waits for a job it dispatched) assumes at least two workers, as
[OR-5](../semantics/oracle.md#or-5) requires of such fixtures; two suffice
for one level of blocking, and each fixture declares the worker capacity it
needs (`oracle: {workers: n}`). Concretely:

- container skip flags take their deployed defaults
  ([OF-7](../semantics/openflow.md#of-7), [CF-6](../semantics/control-flow.md#cf-6));
- a module list whose last module sleeps or suspends gets the `<id>-v`
  identity step ([OF-17](../semantics/openflow.md#of-17),
  [FX-4](../semantics/execution.md#fx-4));
- loops collapse as on a deployed `Flow` job reading the lite form, in which
  nested bodies are stored nodes, so the deployed rules apply at every depth
  ([FX-46](../semantics/execution.md#fx-46),
  [CF-V5](../semantics/control-flow.md#cf-v5)). windmill-lite has no preview
  mode and does not emulate the full-form selection that
  `DISABLE_FLOW_SCRIPT` can force, so neither half of the
  [CF-8](../semantics/control-flow.md#cf-8) dispute reaches it;
- the reference Bun is the one in the image, `1.3.10`
  ([WR-3](../semantics/languages.md#wr-3)).

### 2. What is matched exactly

Every `core` finding in the first release, and each `later` finding once the
milestone that covers it lands ([0012](0012-layout-and-milestones.md)). The
table groups findings by area; the ranges include `later` findings, and the
reference gives each finding's scope.



| Area | Matched | Main findings |
|---|---|---|
| Flow advance | the start and completion decision list, three outcomes per module, entering the failure module at cursor `len`; cursor-0 checks (`later`) | FX-1, FX-2, FX-V2, FX-V4; FX-22, FX-49 |
| Data in scope | `flow_input`, `previous_result`, `results.<id>` routes, `flow_env`, `iter`/`iter_parent` | FX-6, FX-7, FX-10, FX-11, CF-1, CF-3, CF-19, JE-22, JE-36, JE-37 |
| Expressions | QuickJS-ng semantics, shortcuts, textual rewrites, statement mode, serialisation, error texts, limits | JE-1 to JE-46 |
| Step controls | `skip_if`, `stop_after_if` (clean and error stops), `sleep`, `timeout`, `retry` (arithmetic and jitter distribution), `continue_on_error`, `mock` | FX-17 to FX-30, FX-44, FX-V1 |
| Containers | for and while loops (sequential, collapsed, parallel with `parallelism`), `branchone`, `branchall`, subflows by path, `identity`; result shapes, failure and stop propagation | CF-1 to CF-29, CF-V1 to CF-V4 |
| Error handling | `failure_module` (when, what it sees, recovery, innermost level), error shapes and names | FX-12 to FX-15, FX-31 to FX-34, FX-47 |
| Final result | result selection, cancellation | FX-3, FX-36 (executor part), FX-38 |
| JSON boundaries | producer text versus jsonb, unspecified outer order, NUL rejection | FX-42, FX-48, FX-V5, JE-16, JE-44, WR-10, OR-27 |
| Bun steps | process model as observed by one step, argument binding, `Date` hydration, result serialisation, error shapes, environment, imports and bundling | WR-1 to WR-32 (core parts), WR-37 to WR-39, WR-V1 to WR-V4, WR-V8 |
| Platform calls | the `wmill` client surface the core findings cover, error bodies, the three root notions | WA-1 to WA-16, WA-31, WA-40 to WA-42, WA-V1, WA-V3 |

**Observables.** A run is compared on:

1. the final status and result;
2. per module, its status, whether it ran, the stored result, the carried
   value and, for leaves, the arguments the script received;
3. error objects, field by field, with exact message text;
4. platform effects (variables, resources, state, flow user state, object
   store, dispatched jobs) and external effects that fixtures record.

**Normalised before comparison** ([OR-14](../semantics/oracle.md#or-14),
[OR-V5](../semantics/oracle.md#or-v5)):

- job ids, replaced by the typed site path of [0011](0011-differential-testing.md);
- `stack` strings, only with runtime-error provenance;
- the Rust location suffix ` @<file>.rs:<line>:<col>`, **only** in messages
  that Windmill itself generates, which windmill-lite cannot reproduce
  ([Q-FX-13](../semantics/execution.md#q-fx-13); deviation D14).
  windmill-lite records the provenance of every error it produces (generated
  by the engine, the facade or the harness, or thrown by user code), and
  its side is authoritative: the suffix is stripped from the oracle's value
  at a path only when windmill-lite's value at the same path is generated and
  the oracle's message matches a catalogue template that carries `<loc>`
  (the catalogue's notation for a location suffix). User messages are never
  stripped, so a user error that differs only in a Rust-looking suffix still
  fails the comparison. The oracle normaliser of
  [OR-14](../semantics/oracle.md#or-14) is unchanged for oracle-to-oracle
  comparisons. Every other part of a message is matched byte for byte;
- timestamps and durations.

**Unspecified on purpose.** The top-level key order of `flow_input` and of
every step's argument map is unspecified in Windmill
([FX-42](../semantics/execution.md#fx-42), [OR-23](../semantics/oracle.md#or-23)).
windmill-lite shuffles these orders with a per-run seed, so that nothing can
come to depend on an order Windmill does not guarantee. Comparisons treat
those keys as a set.

### 3. Out of scope

| Feature | What windmill-lite does | Findings |
|---|---|---|
| Durable `suspend`, approvals, resume | rejects the flow at load, unless every path to it passes through a mandatory host-provided call site ([0008](0008-host-hooks.md)) | FX-39 |
| Workflow-as-code scripts | rejects at load | WA-33, WR-32 |
| `aiagent` modules | rejects at load; an `ai` input transform on an ordinary step is left out of the arguments, as Windmill does | OF-9, FX-7, JE-47 |
| Queues, worker tags, `priority`, concurrency limits, debouncing, `dedicated_worker` | decoded and ignored | FX-41, OF-16 |
| Job rows, logs as a contract, the UI | not provided; logs are captured for error tails only | WR-20 |
| `delete_after_use`, `delete_after_secs` | no effect, as in CE's asynchronous flow worker, the one CE route windmill-lite reproduces; CE's synchronous script routes, which windmill-lite does not serve, blank a job at once for `delete_after_secs` 0 and only schedule a positive delay ([OR-21](../semantics/oracle.md#or-21)/c–d) | OR-21 |
| Schedules, triggers, apps | recognised by the loader and skipped | SY-12 |
| JSON-mode sync folders (`json: true`); and, until a later milestone, multi-file (`__mod`) scripts and file and fileset resources | rejected when the loader reads the item | SY-2, SY-10, SY-13 |
| EE behaviour (codebase bundles server-side, dedicated workers, OIDC) | out unless observable on CE; see [0002](0002-ir-and-loader.md) for codebase scripts | WR-30, WR-36 |

A load rejection is an error of kind `unsupported_feature` naming the
feature (`suspend`, `workflow_as_code`, `aiagent`), with the message
`unsupported: <feature>`; that satisfies both policy cases, FX-39/a and
WA-33/c.

### 4. Delegated

Steps or subflows a host chooses to run on a real Windmill
([0007](0007-platform-interface.md)) follow Windmill's own behaviour; the
contract covers how their outcome enters the local run, not what happens
remotely.

### 5. Named deviations

Each deviation has an id and a conformance case that shows the difference.
A deviation fixed at build time is listed in the bundle manifest; one that
depends on a runtime setting the host chooses (D15, and D2's watchdog) is
recorded in each run's trace.

| Id | Deviation | Why | Record |
|---|---|---|---|
| D1 | Module state survives between invocations in the default `warm` mode, and every script in a process shares one `globalThis` | per-step re-evaluation costs 0.6 to 2.1 ms per invocation for 0.3 to 1.3 MB bundles, before any dependency initialisation | [0006](0006-module-state.md) |
| D2 | A timed-out or canceled step is abandoned, not killed; its JavaScript may run on until it settles or the harness process is recycled. Its child processes are killed, each with its process group, as Windmill kills the job's group: the engine kills the groups the step announced itself, so a step that blocks the event loop cannot keep them ([WR-55](../semantics/languages.md#wr-55), milestone FU6). In host mode the engine neither recycles nor kills the host, except through the watchdog when the host's event loop stops answering; a step that blocks the event loop longer than the watchdog's threshold therefore ends the host and every run in it, even if it would have finished. In a `warm` process the engine spawned, the same watchdog kills the process and fails all its in-flight invocations; and while one step blocks a shared event loop, every co-resident invocation stalls and can reach its own `timeout`. `fresh` processes are exempt from the watchdog (case `watchdog/finite-cpu`) | one warm process serves many invocations; killing it would fail unrelated steps, and in host mode it is the host | [0004](0004-harness-protocol.md) |
| D3 | Job ids are windmill-lite UUIDs; job introspection answers from the run, not from a job table | there is no queue | [0007](0007-platform-interface.md) |
| D4 | The `cache_ttl` result cache is per process unless the host plugs in a shared store | Windmill's cache lives in its database | [0007](0007-platform-interface.md) |
| D5 | `nativets` and `//native` scripts run in the Bun harness under a native boundary mode, not in Windmill's V8 isolate: each invocation in a fresh JavaScriptCore realm with the native runtime's rules ([WR-40](../semantics/languages.md#wr-40) to [WR-45](../semantics/languages.md#wr-45)); what the realm cannot reproduce is listed below | one JavaScript harness, which host mode needs: a host's steps run in its own Bun process | [0004](0004-harness-protocol.md), [0002](0002-ir-and-loader.md), [0010](0010-packaging.md) |
| D6 | Module ids missing from a never-deployed folder are derived from their position, not random | reproducible bundle identity | [0002](0002-ir-and-loader.md) |
| D7 | `//nobundling` scripts are bundled at build time instead of resolving imports and installing packages at run time | a bundle must run offline; results are the same, but `import.meta`, file paths and stack text differ, as they already do between Windmill's two modes ([WR-27](../semantics/languages.md#wr-27)) | [0010](0010-packaging.md) |
| D8 | An expression attempt that hits the 20 s limit is interrupted; Windmill leaves the evaluation running on its thread | frees the CPU; the error text, the three attempts and the 5 s pauses are unchanged | §7, [JE-2](../semantics/expressions.md#je-2) |
| D9 | In a warm process, work a step leaves running after its result is cut off only as far as the settle-time fence reaches. When the invocation settles, the harness clears the `setTimeout`, `setInterval` and `setImmediate` handles it still has, aborts its requests through the `fetch` view, drops its later log writes, refuses its later platform calls, has the engine kill the process group each child process it started leads (the engine alone signals them, and only while each is still the child's, [0004](0004-harness-protocol.md) item 7), and reports all of that as left behind. What the fence cannot reach runs on: promise continuations already queued (whose effects through those channels are refused), sockets and clients the step opened itself, a child it started detached, what a synchronous child call left in the harness's own group (which the end of the process kills), and CPU work. Its late errors are recorded, not fatal. A process-level error that cannot be attributed to an invocation fails no step | Windmill cuts all of that work off at `process.exit` ([WR-2](../semantics/languages.md#wr-2)), and a detached rejection kills that step's process. In a shared process that would kill every co-resident step, and destroying a step's sockets would break the clients that module state keeps (D1). A child left in the job's process group holds a Windmill job open until it exits ([WR-55](../semantics/languages.md#wr-55)); a warm process cannot wait without letting the step's code run on, so it kills the child at settlement (an open question; `fresh` waits as Windmill does) | [0004](0004-harness-protocol.md) |
| D10 | A harness process has one working directory for all its invocations, not a fresh one per job, and every codebase entry's assets are visible there to every script; so one entry's `package.json` asset declaring `"type": "module"` also makes Bun import the default export of every CommonJS codebase module there as Node does (milestone C2b), and its `args.json` holds the arguments of its latest invocation, which a concurrent invocation's replaces (milestone PF2, [WR-5](../semantics/languages.md#wr-5)) | the working directory is process-global; Windmill gives each job its own, with only its entry's assets ([WR-1](../semantics/languages.md#wr-1)) | [0004](0004-harness-protocol.md) |
| D11 | An intercepted `process.exit` fixes the step's outcome at the call, but `finally` blocks run and a `catch` can swallow it and continue | the process must not exit; the call can only throw | [0004](0004-harness-protocol.md) |
| D12 | A host-provided module has one instance per process, shared by the host and every script that imports it | singletons that hold the host's scopes must be the host's own; Windmill inlines every import into each script's bundle ([WR-27](../semantics/languages.md#wr-27)) | [0010](0010-packaging.md) |
| D13 | Codebase scripts built as CommonJS run on Bun's CommonJS loader, not Node, and so does an ES module one whose header says `//nodejs`, which Windmill also runs on Node ([WR-50](../semantics/languages.md#wr-50)/d). Milestone C2b's cases (Node against Bun, `conformance/steps/bun/d13.yaml`) find the runtime visible (`globalThis.Bun`, `process.versions.bun`), an `_ENTRYPOINT_OVERRIDE` of `default` calling the module's default export on Bun where Node finds the whole exports object, and a `package.json` asset declaring `"type": "module"`, under which Node fails to load a CommonJS bundle that the harness runs; named exports, overrides, `__dirname`, run-time `require` of the bundle's files and assets, and the errors a script throws itself agree. Two differences are accepted as named exceptions of D13 rather than equivalence failures: **D-a**, the text of an error the JavaScript engine itself raises (a `TypeError` for reading a property of `undefined`, say) is V8's on Node and JavaScriptCore's on Bun, so a comparison that meets such a message normalises it or lists it, and none compares it across the two runtimes; **D-b**, the runtime names itself Bun wherever it identifies itself, for example in the metadata a client library sends a server when it connects, which changes no step's result | one JavaScript harness, and host mode needs the steps in the host's Bun process; Windmill runs CJS codebase bundles under Node ([WR-30](../semantics/languages.md#wr-30), [WR-50](../semantics/languages.md#wr-50)) | [0002](0002-ir-and-loader.md) |
| D14 | Messages windmill-lite generates in place of Windmill's server-side ones carry no Rust location suffix | windmill-lite has no Rust source; §2 strips the oracle's suffix only on generated messages | §2 |
| D15 | A host-set cap on in-flight invocations per process. There is none by default; with one set, concurrent children that wait on each other, or parents that wait on runs they dispatched locally, can wait forever where a Windmill with enough workers completes them | a host may need to bound concurrency; the reference has no such bound | [0004](0004-harness-protocol.md) |
| D16 | A transcendental `Math` function (`Math.sin`, `Math.tan`, `Math.log`, `Math.cosh` and the like) gives what the engine build's C library gives, which can differ in the last digit from the glibc of Windmill's image. Measured on recording batch E3a's 30 values: the static musl engine (`amd64` and `arm64`) gives all of them; the glibc of CI's Ubuntu 24.04 runner differs on `Math.cbrt(10)` and macOS's libm on 6, each by one unit in the last place. Other inputs can differ on any library but the image's own | QuickJS-ng calls the C library's libm, and the release engine links musl statically ([0003](0003-semantics-placement.md)); no libm that reproduces glibc's results is vendored | [JE-1](../semantics/expressions.md#je-1), [0003](0003-semantics-placement.md) |
| D17 | A recursion fits deeper in an expression than on Windmill: both engines stop at QuickJS-ng's 1 MiB stack ([JE-48](../semantics/expressions.md#je-48)), but the depth that fits depends on the compiled engine's frames. Recording batch E3a found that JE-48/a's depth of 1 000 overflows on the oracle (600 fits); windmill-lite's builds fit about 1 020 to 1 070 frames of such a function (1 019 on the static `amd64` engine, 1 070 on `arm64`, 1 035 on macOS) | depth is not portable between builds (JE-48); matching it would mean calibrating the stack limit to one compiler's frames | [0003](0003-semantics-placement.md) |

**D5, the native boundary mode** (milestone C3n). A native invocation runs
in a `node:vm` context of the harness process, made for it and released
when it settles, so its module state and globals never survive it, in any
mode ([WR-41](../semantics/languages.md#wr-41)): D1 does not apply to it.
Its global object holds the native runtime's web APIs and nothing of Bun's
([WR-45](../semantics/languages.md#wr-45)). The realm keeps the argument
binding, the result's serialisation (V8's BigInt message included), both
error shapes, the environment preamble and its redeclaration failures, the
timeout text, the failure of a job by a rejection nobody handles (of its
own promises or of those the web APIs hand it) or a throw from a timer,
a module scope with no `arguments` and an undefined `this`, and the end of
its timers and fetches (a response's body included) when it settles.

The web APIs reach the realm through a boundary that **fails closed**:
each promise and object they hand an invocation is tracked as its own
when the boundary hands it over, or handed over untracked and counted. No
path an object can travel is listed; the boundary sits on the realm's only
ways into the web APIs, so what it cannot tell apart is counted rather
than missed.

- *Where it sits.* The realm gets each class WR-45 lists as a subclass of
  the harness realm's, made once per process, which inherits from its
  parent's subclass as in V8 (`CustomEvent` from `Event`, `File` from
  `Blob`). Its constructor is the only way the realm makes one, and each
  member of the class, its own, inherited, symbol-named and static ones,
  hands on what it returns or throws. The function globals (`atob`,
  `btoa`, `reportError`, `structuredClone`) do the same, and `crypto`,
  `crypto.subtle` and `performance` are objects of their subclasses that
  forward to the shared ones. What the harness calls back is wrapped where
  the realm hands it over: a method's callback argument
  (`FormData.forEach`), the callbacks of a stream's underlying source,
  sink, transformer and queuing strategy, and every event listener and
  `on…` handler of every event target, which runs in the async context of
  the invocation that registered it. Each gets its `this` and arguments
  handed on.
- *Tracked.* A promise of the harness realm, as the invocation's, and each
  promise its `then` derives (`catch`, `finally` and `await` included),
  whose value or reason is handed on in turn, so a rejection nobody
  handles fails the invocation ([WR-43](../semantics/languages.md#wr-43)).
  An instance of a WR-45 class, which takes its subclass's prototype. The
  contents of an array, `Map`, `Set` or plain object, all through (a
  message event's `data`, `structuredClone`'s result, a key pair), and the
  results of an iterator, sync or async.
- *Handed on as data.* Buffers and their views, dates, regular
  expressions, errors other than `DOMException`, boxed primitives, the
  JSON `json()` parses, and the realm's own objects when a web API gives
  them back (a signal's `reason`): there is nothing in them to track.
- *Counted.* Anything else of the harness realm: an object of a class
  WR-45 does not list (Bun's `performance.timing`, a `PerformanceTiming`)
  or a harness function. It is handed over as it is and counted on the
  invocation, and the count reaches the engine as a `native_untracked`
  warning ahead of the frame that settles it
  ([0004](0004-harness-protocol.md)). One handed out in no invocation's
  context, or after its invocation settled, cannot be counted on it: it is
  a `native_untracked` warning to every invocation in flight, or a
  diagnostic line. A promise internal to a web API that is never handed
  over belongs to no invocation: a rejection of it nobody handles is an
  unattributed error (0004's failure model, item 3).

The realm cannot keep:

- JavaScriptCore's texts where they differ from V8's: a redeclared name, a
  cyclic result, a missing export, the cause of a load failure, the text
  and frames of a stack. The BigInt and stack-overflow messages are V8's;
- the stack limit, which is JavaScriptCore's; and a stack replaced by a
  value that is not a string leaves no source line in front of it
  (WR-43/i), since JavaScriptCore keeps a thrown error's frames only in
  its stack text;
- the 128 MiB heap cap per isolate (WR-44/c): the realms share the
  harness's heap; and the failure of a job whose promise can never settle
  (WR-43/e): with other invocations on the event loop, the harness cannot
  know that nothing is pending, so such a step runs until its timeout;
- `FileReader`, which Bun 1.3.10 lacks;
- separate web API objects: the realm gets the harness realm's classes
  through the subclasses above, made once per process, and the shared
  `crypto` and `performance` through forwarders; a script that changes one
  of them (a prototype, `crypto`) changes it for the other native
  invocations of the process, as D1 says of Bun's `globalThis`; and a web
  API's promise, buffer or parsed JSON is the harness realm's, so it is
  not an `instanceof` the realm's `Promise`, `ArrayBuffer` or `Object`;
- imports of files of the host: windmill-lite reads none, so only the
  embedded client resolves ([WR-40](../semantics/languages.md#wr-40)); the
  client is `windmill-client@1.326.0`, the version Windmill embeds, built
  from npm into the bundle;
- the drop of the isolate when an outcome is fixed early: the call goes on
  until it settles (D2, D9);
- result streaming ([WR-46](../semantics/languages.md#wr-46), `later`),
  which is not implemented.

`fresh` mode ([0006](0006-module-state.md)) removes D1, D2, D9, D10 and D11:
it runs one process per invocation, in its own working directory, and kills
it at the end, on timeout and on cancellation; the watchdog does not apply
to it.

### 5a. Restrictions

These are cases windmill-lite refuses rather than runs differently. Each
fails at build, at load or at the call, and has a case. All but the first
fail with the named error the table gives. The first has none: the kernel
refuses the connection before any windmill-lite code sees it, so the step
sees only the error its client library raises, and nothing is recorded.

| Restriction | Error | Record |
|---|---|---|
| In a warm or host-mode harness, step code reaches the platform only through `fetch`; other transports (`node:http`, raw sockets) find no listener | the step's client gets "connection refused"; nothing is recorded, because the refusal carries no invocation | [0007](0007-platform-interface.md) |
| A by-path target of a local dispatch (`runScript*`, `runFlow*`) must be a root of the bundle | the dispatching step fails with `UnsupportedPlatformCall`, `dispatch target not in bundle: <path>` | [0007](0007-platform-interface.md) |
| A host-provided module must be an ES module that does not export a binding it reassigns (`export let` that changes); a CommonJS module, whose names are known only when it runs, is refused | build error `host_module_live_binding`, or `host_module_commonjs` | [0010](0010-packaging.md) |
| A Bun script that Windmill's bundled mode cannot build, and so fails at every run: an import that resolves to no script of the folder, including one a `.ts` rewrite collision corrupted ([WR-28](../semantics/languages.md#wr-28), [WR-38](../semantics/languages.md#wr-38)). And scripts Windmill runs that the build refuses: an `//npm` script, which installs with npm; a bundle that emits a file besides its module (an asset through Bun's file loader); a source the build's TypeScript parser rejects (the deprecated `import … assert {}` form) | build error `bundle_failed`, `unsupported_feature` for `//npm`, or `parse_failed` | [0010](0010-packaging.md) |
| Two codebase scripts may not extract different assets to the same relative path, or to paths that would not form one tree | build error `asset_collision` | [0002](0002-ir-and-loader.md) |
| A codebase asset whose `to` is not a clean relative path (one that leaves the working directory), or whose source does not read; a codebase bundle with several output files (a `file` loader's output, a native add-on), whose upload layout is `later` ([WR-49](../semantics/languages.md#wr-49)); a codebase entry whose `format` is neither `cjs` nor `esm` | build error `invalid_asset`, `unsupported_feature`, or `bundle_failed` | [0002](0002-ir-and-loader.md), [0010](0010-packaging.md) |
| A hook selector that names a module id occurring more than once in one module list must give the occurrence; a mandatory provided-site selector must match exactly one site | load error `ambiguous_selector` | [0008](0008-host-hooks.md) |
| A codebase entry whose `customBundler` is set, since windmill-lite does not run its shell command as the bundler ([WR-48](../semantics/languages.md#wr-48)/a, `later`; a JavaScript-falsy value counts as unset); and a script with a `schema_validation` annotation, until the reference covers it | load error `unsupported_feature` | [0002](0002-ir-and-loader.md), [0009](0009-host-adapters.md) |
| Sync folders the CLI pushes but the loader does not read as the CLI does ([SY-1](../semantics/openflow.md#sy-1), [SY-7](../semantics/openflow.md#sy-7), [SY-13](../semantics/openflow.md#sy-13)): a script file with a dot before its script extension (`f/my.dir/x.ts`), which the CLI renames by cutting its path there; an `!inline` or script-lock reference that leaves the sync root, and an absolute script-lock reference; a script, inline or lock file that is not UTF-8; a `wmill.yaml` glob outside the syntax that minimatch 9 and 10 read alike (extglobs, POSIX classes, `.` and `..` segments, some escapes, braces that do not expand, sequences that JavaScript would round); a `wmill.yaml` value of the wrong type; a file whose name is not UTF-8, which is skipped with a warning | load error `dotted_path`, `path_escape`, `invalid_text`, `unsupported_glob` or `invalid_config` | [0002](0002-ir-and-loader.md) |
| YAML that the CLI's parser accepts but yamlx cannot read as the CLI does ([SY-9](../semantics/openflow.md#sy-9)): JavaScript-specific values (the tags `!!binary`, `!!timestamp`, `!!set`, `!!omap`, `!!pairs` and `!!merge`, collection keys), directives other than `%YAML 1.2`, a carriage return without a line feed (a line break only to a YAML 1.1 parser), anchor names that YAML 1.2 reads further, a `?` or `:` in a flow collection that the two parsers read as different indicators (`[?v]`, `[v:]`), aliases beyond an expansion bound, and some YAML 1.2 syntax the YAML 1.1 parser rejects (the `\/` escape) | load error `unsupported_yaml`, or the parser's `yaml_syntax` for its own rejections | [0002](0002-ir-and-loader.md) |

Anything not in this table and not out of scope is a bug when it differs.

### 6. Findings that are not settled

Every contested finding in this table can be recorded on the CE oracle,
which public CI may run, so none of them gets a permanent switch. Instead:

1. Recordings are made in **batches, one per consumer** (milestones E3a to
   E3f in [0012](0012-layout-and-milestones.md)): each contested rule's
   discriminating cases are recorded in the batch that gates the milestone
   implementing that rule, as the last column says.
2. Until a case is recorded, the implementation follows the default below,
   inside one function per contested rule, and the case keeps its `disputed`
   tag. The implementing milestone cannot merge before its batch has.
3. When the recording lands, the default becomes the recorded behaviour,
   the case becomes `oracle-observed`, and if the two differ, that one
   function changes in the same pull request.

If a finding later turns out not to be recordable on CE, it gets a build
switch through a change to this record, naming the reason.

| Finding | Readings | Default until recorded, and why | Settled by | Recorded in, gating |
|---|---|---|---|---|
| [FX-10](../semantics/execution.md#fx-10) / [FX-V6](../semantics/execution.md#fx-v6) | engine reads the preceding result from memory when (a) a literal preceding-id spelling appears, or (b) any binding trigger applies (`previous_result`, a spelling, or `error`) | **recorded: (b)**, the default, which agrees with the confirmed binding rule [JE-10](../semantics/expressions.md#je-10). In FX-V6/a's handler `results[flow_input.target]` read the stored `3` and `previous_result && results[flow_input.target]` the in-memory `EarlyStopError` (fixture `conformance/flows/execution/fx-v6`, oracle run 40685766017). Without a failure module, `previous_result && Object.keys(await results[k])` and `typeof error && …` read the preceding result from memory and a bare computed read reads storage (run `memory_or_storage` of [`execution/fx-10`](../../conformance/flows/execution/fx-10), oracle run 40685013818) | Q-FX-15, case FX-V6/a | E3c in a failure handler (recorded), gating N1b; E3b without one (recorded), gating N1a |
| [CF-19](../semantics/control-flow.md#cf-19) / [CF-V6](../semantics/control-flow.md#cf-v6) / FX-10 route 2 | direct `results` lookup when (a) the whole expression matches, or (b) the first qualifying line matches | **Recorded: (b).** CF-V6/a (`results.a`, a newline, `+ 100`, with `a` returning 7) gives 7 on the oracle, and its single-line control CF-V6/b gives 107; JE-9/g to JE-9/i agree. The default was (b), so nothing changes | Q-CF-16, cases CF-V6/a,b (oracle run 40687124625) | E3a, gating B2a and B2b |
| [FX-33](../semantics/execution.md#fx-33) | failure-module recovery from (a) an object only, or (b) an object or a one-element positional array | **recorded: (b)**, the default. A successful handler returning `[true]` made the flow succeed with result `[true]`; `[false]`, `[null]`, `[]`, `[true,false]` and `{"recover":"true"}` left it failed, and `{"recover":true,…}` recovered (fixture `conformance/flows/execution/fx-33`, oracle run 40685766017) | Q-FX-19, cases FX-33/c–g | E3c (recorded), gating N1b |
| [FX-34](../semantics/execution.md#fx-34) / [FX-32](../semantics/execution.md#fx-32) | a failure module copied into an inline body has (a) its id suffixed with the parent cursor and the iteration or branch, in its status and in `WM_FLOW_STEP_ID`, or (b) its own id | **recorded: (b)**. The reference states (a) as confirmed; the oracle contradicts it. In a loop at cursor 1 whose two iterations fail, both copies reported `WM_FLOW_STEP_ID` `failure`, and each body flow's status and stored flow name the copy `failure` (fixture `conformance/flows/execution/fx-34`, run `step_id`, oracle run 40689442792) | E3c's run `fx-34/step_id`; a correction requested from the semantics team | E3c (recorded), gating N1b |
| [CF-12](../semantics/control-flow.md#cf-12) | after a sequential loop breaks on an unhandled error stop with `skip_failures: false`, the flow (a) continues, or (b) fails without running the next step | **recorded: (b).** The loop fails with the array ending in the `EarlyStopError`, the flow fails with that array, and the next step does not run; with `skip_failures: true` the loop succeeds and the flow continues | Q-CF-14, cases CF-12/c and CF-12/f | E3d (`conformance/flows/control-flow/cf-12`, runs `c-fail` and `f`), gating N3a and N3b |
| [FX-42](../semantics/execution.md#fx-42) | `$jsonvar:` output keeps (a) its text, or (b) its insertion order with numbers re-serialised | (b). The readings differ only in number spelling, which a JavaScript consumer cannot see; the difference matters for Python (`later`). **Recorded (b)**, first in oracle run 40685013818 and committed from run 40686049913: the arguments file keeps the variable's key order and writes values re-serialised (`1e3` as `1000.0`, `1e21` as `1e+21`, an integer that fits in an unsigned 64-bit integer exact, escapes decoded, no spaces) (run `args_file` of [`execution/fx-42`](../../conformance/flows/execution/fx-42)) | Q-FX-16, case FX-42/h | E3b (recorded), gating PF2, which passes both runs: its resolver writes that spelling into the arguments file (`platform.ResolveArgs`, `args.json`) |
| [OF-3](../semantics/openflow.md#of-3) / [OF-V5](../semantics/openflow.md#of-v5) | one or two form selections | not applicable: both readings agree on the successful lite deployment, which is the reference configuration | Q-OF-11 | none needed |
| [CF-8](../semantics/control-flow.md#cf-8) | how far the raw-submission no-collapse rule reaches, and whether the stored form actually selected (lite or full) decides nested collapse | not applicable: windmill-lite never runs raw submissions and always reads the lite form (§1). The by-path halves of CF-8/a–c, which both readings agree on, are recorded (E3d), with the nested body's job kind `flownode` | Q-CF-15 | none needed |
| [FX-49](../semantics/execution.md#fx-49) (`later`) | a cursor-0 `skip_expr` that turns true mid-run (a) is left open, or (b) emits a stop | **recorded: (b)**, the default. A sequential loop at module 0 whose first iteration flips the expression to true stops after that iteration, and the flow completes normally with `[1]`, with a collapsed and with a two-step body; an expression that throws at the second start phase fails the flow with a chaining `InternalError`, the loop left canceled with its module in progress (fixture `conformance/flows/execution/fx-49`, oracle run 40794872025) | Q-FX-11, case FX-49/a | E3e (recorded), gating the `later` milestone (L1) that implements `skip_expr` |
| [FX-40](../semantics/execution.md#fx-40) | collapsed iterations (a) never, or (b) can share cache entries | **recorded: (b).** Two collapsed iterations with identical arguments share an entry for an inline rawscript body (with or without an explicit `path`) under the loop's `cache_ttl`, and for an unhashed by-path script with its own `cache_ttl`. The inline body runs as `flowscript` jobs whose paths end in `/loop-<index>` but which carry one script hash, so the key does not follow the path; the correction expected a rawscript without a path not to share. A body pinned by hash shares under the loop's `cache_ttl`, and a pinned script's own `cache_ttl` is dropped | Q-FX-17, case FX-40/d | E3d (`conformance/flows/execution/fx-40`, runs `d-raw`, `d-raw-path` and `d-script`; `conformance/flows/execution/fx-40-hash`, runs `d-hash` and `d-hash-script-ttl`), gating N4 |
| [JE-11](../semantics/expressions.md#je-11) (`later`), text that is not ASCII | the call rewrite counts its parenthesis's position in characters and applies it as a byte offset; where that offset falls inside a character Windmill's evaluator panics, and the job then (a) fails with some error, or (b) is lost or retried with the worker | **settled from the source (a), 2026-10-29:** the count starts at the call, and the runtime catches the panic, so the evaluation fails at once with the runtime's report, `task <n> panicked with message "assertion failed: self.is_char_boundary(idx)"`, routed like any evaluation error of its site. windmill-lite reproduces the early parenthesis and that text, its own task number in place of `<n>` (`expr.ErrRewritePanic`, milestone FU6; FU3 had failed it with its own text and counted from the start of the text) | Q-JE-13 (the source answers it; JE-11/b–e await a recording) | E3e did not record it, gating no milestone |
| [FX-36](../semantics/execution.md#fx-36) (HTTP part `later`) | array results decoded positionally as response metadata, or not | **recorded: positionally.** Run-and-wait answers `[201, null, null, null, 7]` with HTTP 201 and the body `7`: the array's elements fill the metadata's fields in order. FX-36/a–g hold as written, the bodies in jsonb's text output, so FX-36/b's members come in jsonb order (fixture `conformance/flows/execution/fx-36-http`, oracle runs 40794872025 and 40798097155) | Q-FX-12 | E3e (recorded), gating F7b |
| [WR-16](../semantics/languages.md#wr-16), [WR-28](../semantics/languages.md#wr-28) (`later` parts) | stream classification; dynamic-import rewrite reach | **recorded: the corrected readings.** A custom `replace` value goes through string `+` (WR-16/f gives `"7"`) and every printed line is classified on its own (WR-16/g gives `"ab"`); a static import's rewrite also rewrites a different dynamic specifier that contains it, which is then fetched from the workspace (WR-28/e). A script whose only import of a relative path is dynamic cannot be deployed: the dependency job takes the specifier for an npm package (WR-28/d, deferred). Fixtures `conformance/flows/languages/wr-16`, `wr-28` | Q-WR-15, Q-WR-16 | E3e (recorded), gating the `later` milestones (L1) for them |
| [OR-11](../semantics/oracle.md#or-11), [OR-14](../semantics/oracle.md#or-14), [OR-V3](../semantics/oracle.md#or-v3), [OR-V8](../semantics/oracle.md#or-v8) | identity encoding for compared steps | decided in [0011](0011-differential-testing.md): typed segments, an occurrence suffix for repeated module ids, the escape character escaped | Q-OR-17 | a design decision; K1's encoder tests |
| [OR-15](../semantics/oracle.md#or-15), [OR-V9](../semantics/oracle.md#or-v9) | whether the probe catalogue fills the fixture format | **recorded:** the catalogue is rebuilt as executable fixtures (`conformance/flows/oracle/`) and reproduces the recorded results with its bindings written out: `loop_seq_multi` gives `["a12","b12"]` with `flow_input.iter.value`, and its literal bare `iter.value` fails with `iter is not defined` (OR-V9/a, OR-V9/b); `branchone` and `loop_par` reproduce theirs with `flow_input.n` and `flow_input.iter.value` (OR-V9/c, OR-V9/d). Its recordings and raw captures retain the tree and the observations | Q-OR-16 | E3e (recorded) |

### 7. Policy questions the reference left to the design

| Question | Decision |
|---|---|
| [Q-CF-12](../semantics/control-flow.md#q-cf-12): a subflow can read its caller's `results.<id>` | Match it. The contract is exact semantics, and flows may rely on the leak without knowing. |
| [Q-CF-13](../semantics/control-flow.md#q-cf-13): absent container fields | Apply the deployed defaults. A `strict` build option rejects absent skip flags instead, for authors who want the folder to say what runs. |
| [Q-WR-17](../semantics/languages.md#q-wr-17): `.ts` rewrite collisions | Reproduce them: the rewrite is textual and cheap to copy, and a script that fails to build on Windmill must fail here too. |
| [Q-WR-18](../semantics/languages.md#q-wr-18): layer-specific jsonb order at the step boundary | Emulate it ([0005](0005-json-step-boundaries.md)). |
| [Q-WR-19](../semantics/languages.md#q-wr-19): module state | [0006](0006-module-state.md). |
| [Q-FX-13](../semantics/execution.md#q-fx-13): how much error text to match | All of it, except the Rust location suffix and stacks. |
| [Q-WA-9](../semantics/client-api.md#q-wa-9): secret masking in logs | Not in the first release: windmill-lite keeps no log store, and logs are not part of the contract. Hosts that forward logs own their masking. |
| [Q-WA-11](../semantics/client-api.md#q-wa-11): unsupported platform calls | Fail by default with `UnsupportedPlatformCall`; an `observe` mode keeps stock client behaviour for oracle comparison ([0007](0007-platform-interface.md)). |
| [JE-2](../semantics/expressions.md#je-2) (`later`): runaway expressions | Keep the message, the 20 s limit and the three attempts with 5 s pauses. windmill-lite also stops the engine instance when an attempt times out, which its design note calls a deviation to list: D8. It reproduces the zombie-flow monitor's cancel: a run whose transition spends `FLOW_ZOMBIE_TRANSITION_TIMEOUT` (60 s, the engine's `TransitionTimeout`) evaluating expressions is canceled by `monitor`, so the three attempts end the run canceled, as on the oracle (milestone N2). Hooks and waits do not count, since Windmill has no hooks and runs a wait as a scheduled job. |
| [FX-26](../semantics/execution.md#fx-26): retry arithmetic | Emulate the unsigned 16-bit wrap, saturate and cap steps exactly; the random source is injectable so tests can fix jitter. |

## Consequences

- The engine tracks three outcomes per module (stored result, status,
  carried value) because the contract compares all three
  ([FX-V2](../semantics/execution.md#fx-v2)).
- A flow using an out-of-scope feature fails at load, never midway through a
  run.
- Every deviation is visible in the bundle manifest, so a host can tell what
  it is running, and `fresh` mode turns five of them off (D1, D2, D9 to D11).
- Contested rules are settled by recording, early, so the engine is built
  against observed behaviour rather than kept with two readings.
- Milestone K1 corrected the §5a preamble. It had said every restriction
  fails "with a named error", but the `fetch`-only restriction raises no
  windmill-lite error at all, and two restrictions fail at build rather than
  at load. The preamble now says so; the table is unchanged.
- §5a gained the YAML restriction when milestone A2 implemented the dialect
  of [0002](0002-ir-and-loader.md): a few YAML constructs the CLI accepts
  cannot be read as the CLI reads them over a YAML 1.1 parser, so they fail
  at load instead of parsing differently.
- §5a gained the sync-folder restriction, and §3 the JSON-mode and `later`
  sync-folder rows, when milestone A4 implemented the loader of
  [0002](0002-ir-and-loader.md).
- §5a gained the bundling restriction when milestone C2a implemented the Bun
  build of [0010](0010-packaging.md): windmill-lite bundles before any run,
  so a script whose bundle Windmill's worker would fail to build (and fail
  the job with `exit code for "bun build"`) fails the build instead; an
  `//npm` script, a bundle with an extra output file and a source the
  build's parser rejects are refused there.
- Recording batch E3c recorded the two contested rules of §6 that it gates,
  Q-FX-15 (FX-10/FX-V6) and Q-FX-19 (FX-33). Both recordings agree with the
  stated default (b), so that default is now recorded behaviour. No engine
  function changes, because none exists yet: N1b implements both rules
  against these recordings. E3c's recordings also contradict one confirmed
  finding: a failure module copied into an inline body keeps its own id,
  in its status and in `WM_FLOW_STEP_ID`, where FX-34 and FX-32 say it is
  suffixed. §6 now lists it with the recorded behaviour, which the engine
  implements (N1b runs a top-level failure module with its own id; its
  copy inside a body comes with bodies, in N3c); the correction is asked
  of the semantics team
  ([README](README.md), request 5).
- Recording batch E3b settled Q-FX-16 and recorded the handler-free half of
  Q-FX-15 on the oracle. Both recordings agreed with the defaults in §6, so
  no default changed, and no implementation of either rule existed yet to
  change. The case tags in the reference stay with the semantics team,
  which the batch's pull request asks to mark the recorded cases
  `oracle-observed`.
- Recording batch E3d recorded CF-2 to CF-15 and FX-40 on the CE oracle
  (`conformance/flows/control-flow`, `conformance/flows/execution/fx-40`).
  It settled two rows of §6 on reading (b), the defaults they already had,
  so no engine function changes: CF-12 (Q-CF-14) and FX-40 (Q-FX-17, where
  the oracle also showed sharing for a rawscript without a path). The
  catalogue's provenance tags and the tracking questions are the semantics
  team's to update. The recordings confirm §1's deployed defaults from both
  sides. CF-6/a and CF-15/a are pushed through the API with the fields
  absent (`conformance/flows/control-flow/cf-6`, `cf-15`); the API answers
  with them filled once the dependency job has succeeded, and the flows run
  with the defaults. The published CLI's `generate-metadata` writes the same
  defaults (`skip_failures`, `parallel`, a branch's `skip_failure`,
  `parallel` and `expr`) into `flow.yaml`, so a folder that has been
  through it carries them explicitly. Each such recording's provenance
  holds the three values.
- Milestone C3b narrowed D9 with the settle-time fence (the anchor's
  decision of 2026-10-28). Before it, a warm process let every timer and
  request of a settled invocation run on. Now the harness revokes what it
  can attribute to the invocation (timers, `fetch`-view requests, log
  writes, platform calls) when the invocation settles. It reports what it
  revoked in a `left_behind` warning, so the conformance and differential
  suites flag a script that leaves work behind. The fence never touches
  sockets or shared objects: a pooled client makes its connections and
  maintenance timers in the process scope it captured, and a step
  module's top-level code is module state (D1), so neither is fenced
  ([0004](0004-harness-protocol.md), failure model item 4 and loading).
  Cases `D9/error-after-result` and `D11/exit-after-result` now expect, in
  `warm` too, that the timer never fires.
- Recording batch E3a settled the CF-19 / CF-V6 row of §6 on the oracle:
  the direct lookup is line-anchored, which confirms the default. Its cases are
  `conformance/flows/cf/cf-19` and the expression cases of
  `conformance/exprs`. The batch also recorded behaviour that differs from
  the reference's source-derived expectations without touching a contested
  rule (the JE-48 stack bracket, the final `sleep` of JE-28/c, the JE-35/b
  envelope, the JE-2/a timeout); those cases now expect what the oracle did,
  and the differences are asked of the semantics team ([README](README.md),
  request 7).
- §5 gained D16 and D17 when milestone B2b ran E3a's expression cases on
  the engine. Both come from the engine's build, not from a rule: QuickJS-ng
  takes transcendental `Math` results from the C library's libm, and the
  depth a recursion reaches before the 1 MiB stack limit from the compiled
  frames. Every other recorded expression case gives the oracle's value or
  text on windmill-lite's engine, error texts byte for byte with their
  stacks. Every bundle lists both deviations.
- Milestone PF1 gave the `fetch`-only restriction of §5a its cases: a warm
  step's `node:http` call to the facade gets "connection refused" and is
  recorded nowhere, while a fresh step's is answered over TCP
  (`conformance/steps/bun/wa-31.yaml`, whose `by_mode` names the
  restriction `5a`, and `TestAttribution` in
  [internal/platform/facade](../../internal/platform/facade/README.md)).
  The engine's port is held by a loopback connection, not a bound socket,
  because macOS drops connections to a bound socket that does not listen
  instead of refusing them ([0007](0007-platform-interface.md)).
- Milestone PF3 gave the dispatch-target restriction of §5a its cases: a
  step case's bundle has no root a step can dispatch, so a dispatch by
  path, by hash or of a flow, awaited or not, fails the step with
  `UnsupportedPlatformCall`, `dispatch target not in bundle: <path>`
  (`conformance/steps/bun/wa-15.yaml`, and `TestDispatchRefusedOnTheClient`
  on the stock client in
  [internal/platform/facade](../../internal/platform/facade/README.md)).
  The facade answers the dispatch with a 501 whose body is that text, and
  the 501 policy's error quotes it ([0007](0007-platform-interface.md)).
- Recording batch E3e recorded the contested `later` rows of §6 above, all
  on their defaults: FX-49 on reading (b), FX-36's array result decoded
  positionally (an HTTP adapter's concern, F7b), and the corrected
  readings of WR-16 and WR-28; no implementation of those rules exists yet
  to change. It rebuilt the probe catalogue (Q-OR-16). It also recorded
  what the build milestones asked: a flow input read from storage answers
  the JE-7 shortcut with jsonb's spaced text (`{"a": 2, "b": 1}`), the
  previous step after a loop answers JE-8 with compact text in jsonb key
  order, and a step's input transforms are evaluated in an order that
  varies from one evaluation to the next, whatever order the flow writes
  them in. The CE image runs CommonJS codebase bundles on Node v20.20.2,
  which deviation D13 replaces with Bun; the runs that report the runtime
  record Node. The catalogue's tags and tracking questions stay the
  semantics team's to update ([README](README.md), request 11, answered by
  the reference's revision of 2026-10-29).
- Recording batch E3f recorded native steps on a worker that serves the
  `nativets` tag, the configuration D5's native boundary mode stands in
  for: a CommonJS codebase script under `//native` fails at its first use
  of `module` (`ReferenceError`, `module is not defined`), as WR-50
  predicts, and an ES module one whose banner builds `require` with
  `createRequire` loads, its `require` a function since the browser-target
  bundle stubs `node:module`. windmill-lite's native mode gives both. Where
  windmill-lite's native mode differs from a recording (a promise that
  never settles fails at once on the oracle, WR-43/e, and waits for the
  step's timeout on windmill-lite), the batch lists the run for the next
  semantics revision and the run stays out of the flow suite; no deviation
  is added.

- Milestone N2 settled §7's JE-2 row: windmill-lite reproduces the
  zombie-flow monitor's cancel, which recording batch E3a found ends a
  runaway expression's flow at 60 s, before the third attempt's timeout
  text could surface. FX-26's arithmetic is emulated exactly, with the
  jitter source injectable as §7 asks (H10), and no deviation is added:
  a run on H10's recording clock, a host's choice, records `H10` among
  its trace's deviations, as H7 does.

## Spec IDs relied on

FX-1, FX-2, FX-4, FX-10, FX-26, FX-32, FX-33, FX-34, FX-36, FX-39, FX-40, FX-41, FX-42,
FX-46, FX-49, FX-V2, FX-V6, CF-6, CF-7, CF-8, CF-12, CF-19, CF-V5, CF-V6,
OF-3, OF-7, OF-17, OF-V5, JE-1, JE-2, JE-9, JE-10, JE-48, WR-1, WR-2, WR-3, WR-16,
WR-27, WR-28, WR-30, WR-32, WR-36, WA-33, OR-5, OR-11, OR-14, OR-15, OR-21,
OR-23, OR-V3, OR-V5, OR-V8, OR-V9, SY-1, SY-2, SY-7, SY-9, SY-10, SY-12,
SY-13, and the questions named in the tables.

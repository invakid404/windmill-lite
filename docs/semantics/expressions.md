# Expressions

This area describes how Windmill evaluates the JavaScript expressions embedded in flows: step input transforms, loop iterators and `parallelism`, predicates such as `skip_if`, `stop_after_if` and `retry_if`, `sleep` and `timeout` values, and the flow-level `skip_expr`. It covers the QuickJS-ng engine and its limits, the shortcuts that answer some expressions without the engine, the textual rewrites, the names each expression site can see, where each site's errors surface, `$var:`/`$res:` resolution of step arguments, and where values pass through Postgres `jsonb`. Step scheduling, retries and failure handling belong to [execution](execution.md), loops and branches to [control flow](control-flow.md), language runtimes and reserved variables to [languages](languages.md), and the flow schema to [OpenFlow](openflow.md).

**Pinned version:** Windmill `v1.767.0` (commit `6de4ec0f665ad856002495c4d86269105ec28ad7`).
Source pointers are `path:line @ v1.767.0` relative to the Windmill repository
root. Status, scope and confidence tags are defined in the [index](README.md#legend).

**ID prefixes in this file:** `JE-` (flow expression evaluation); `JE-Vn` findings were added during validation. Each `JE-Vn` finding is placed directly after the finding it extends. `JE-4` is withdrawn.

## Conventions

Terms used throughout:

- **P** is the value bound as `previous_result` (and as `result` at the sites that use it) at a given position in a flow. [JE-36](#je-36) defines it.
- **Previous id** is the id of the module immediately before the current one in the same flow. It is `""` at the first module of a flow or of a loop or branch body ([JE-8](#je-8)).
- **Results context** and **authenticated client** are two capabilities a site may give the evaluator: `results` lookups ([JE-18](#je-18)) and API-backed helpers ([JE-20](#je-20)). [JE-22](#je-22) lists which sites have them.
- **Shortcut** means one of the four fast paths [JE-6](#je-6) to [JE-9](#je-9), which answer an expression from stored JSON without running any JavaScript.
- **Collapsed loop** means a simple loop whose single-step body is evaluated in the loop's own context instead of in a body flow ([JE-36](#je-36), rule 5).
- **Predicate** means an expression at a boolean site (`skip_if`, `branchone`, `stop_after_if`, `stop_after_all_iters_if`, `retry_if`, flow `skip_expr`), wrapped as described in [JE-24](#je-24).

Conformance cases name an **observation point**, because the same value looks different before and after the database and before and after reference resolution ([JE-V3](#je-v3)):

| Point | What is observed |
|---|---|
| **O1** evaluator output | The JSON text the evaluator returns for a context and an expression. Only an implementation that exposes its evaluator can observe it directly. |
| **O2** recorded args | The step job's argument JSON as the job API returns it: `jsonb`-normalised ([JE-44](#je-44)) and *before* `$var:`/`$res:` resolution ([JE-41](#je-41)). This is the default point. |
| **O3** resolved args | The argument JSON after leaf resolution ([JE-41](#je-41)), before any language runtime touches it. |
| **O4** script observation | What the step's script sees or returns after its language runtime parsed the arguments. Bun rounds integers beyond 2^53. |
| **O5** flow and job outcome | The flow's final result, each module's status, and each child job's own row (result and success flag). |

O1 is not the only point where pre-database values matter: a later step can compute something from an in-memory value and record it, which is then visible at O2 and O5 ([JE-16](#je-16)). The fixture notation is in the [conformance notation](conformance.md#je-notation).

## Summary for the design

1. **One engine, one textual pipeline** ([JE-1](#je-1), [JE-5](#je-5), [JE-11](#je-11) to [JE-15](#je-15), [JE-24](#je-24)). Expressions that reach the engine run on QuickJS-ng (rquickjs 0.11.0) as strict-mode global scripts ([JE-50](#je-50)) in a fresh runtime per evaluation, with no `Intl`, `atob`, `URL` or `console` (oracle-observed) but with `queueMicrotask`, `performance` and `DOMException`, and with the C library's `Math` digits (glibc on the CE image). The engine's job queue is drained before the result is read, and a job that throws is ignored ([JE-51](#je-51)). Behaviour comes from a fixed pipeline: predicates are wrapped untrimmed in `Boolean(…)`; the text is trimmed; the shortcuts are tried in the order JE-6 → JE-7 → JE-8 → JE-9; the `variable`/`resource`/`flow_user_state` and `results` rewrites are applied; expression or statement mode is chosen; the value is serialised as `JSON.stringify(x ?? null)`. Shortcuts return before any engine starts, so the engine limits (20 s per attempt, up to 3 attempts, 128 MiB of heap, quickjs-ng's default 1 MiB of native stack; [JE-2](#je-2), [JE-3](#je-3), [JE-48](#je-48)) do not apply to them. On the pinned oracle a simple recursion of depth 600 fits in that stack and one of depth 1 000 overflows; frame sizes, and so the depth that fits, depend on the engine's build. A transition that keeps evaluating for 60 s can be canceled by the server's zombie-flow monitor before the timeout error surfaces. Context values nested deeper than 127 levels fail before the engine runs ([JE-49](#je-49)).
2. **Shortcut dispatch is semantics, not optimisation** ([JE-6](#je-6) to [JE-9](#je-9), [JE-V6](#je-v6)). JE-6 to JE-8 return stored JSON verbatim. JE-9 inspects only the first line that matches its pattern: it can discard the rest of the expression, turns misses into `null` where JavaScript would throw, and can hand a predicate a non-boolean. The exact spelling decides whether a read comes from memory, from the database or from JavaScript: `results.a.n` reads the database even when `a` is the previous step.
3. **`results` handling is textual and lazy** ([JE-10](#je-10), [JE-12](#je-12), [JE-18](#je-18)). Only names written in the text are bound. `results['a']` and computed keys stay Promises, and `results` inside a nested non-async function is a syntax error.
4. **Scopes differ per site** ([JE-22](#je-22), [JE-V1](#je-v1), [JE-33](#je-33), [JE-39](#je-39)). `sleep`, `retry_if` and `skip_expr` have no `results`; stop predicates get `results` only if their text names it; input transforms have no `result`, and `params` is always `{}`; `parallelism` sees the containing input unchanged, so an outer `iter` stays visible. No site binds `env`, and no `WM_*` name exists except `WM_SCHEDULED_FOR` in flow `skip_expr`.
5. **Lookup failures are not all `null`** ([JE-9](#je-9), [JE-18](#je-18), [JE-19](#je-19)). The JE-9 path lookup and the JE-18 fallback lookup turn misses and failures into `null`, but a fetch for a module of the current flow that already has a job throws on failure. Parallel iterations and subflows called by path start new scopes; a collapsed loop records only the loop id; a running restarted flow can read its original run.
6. **`previous_result` is positional** ([JE-36](#je-36)). The first module of every flow job, and any module at index 0 (collapsed loops included), sees the job input. Collapsing applies to deployed sequential for-loops and to while loops (previews included), never to parallel loops. The re-fetch fallback produces the *string* `"{}"`.
7. **Transforms run serially, once per step** ([JE-23](#je-23), [JE-V2](#je-v2), [JE-V5](#je-v5)). A step's transforms are evaluated one at a time, in unspecified order, each in its own runtime, stopping at the first error. Ordinary step retries reuse the recorded arguments without re-evaluating the transforms; collapsed loop bodies evaluate their transforms per iteration. Isolation does not make concurrent evaluation, a shared runtime or shared read caching behaviour-preserving.
8. **Argument references are resolved per leaf step and per attempt** ([JE-41](#je-41), [JE-42](#je-42), [JE-V3](#je-v3)). `$var:`/`$res:` markers are resolved when a script job builds its arguments, into a map separate from the recorded arguments. Expressions see unresolved strings, except in `flow_env` ([JE-38](#je-38)). Recorded arguments, resolved arguments, child-job results and parent-flow errors are distinct observation points.
9. **Errors take five routes** ([JE-35](#je-35), [JE-26](#je-26)): chaining after a child completed (`InternalError`, failure module not run); first pickup of a root flow (failure module runs); first pickup of a nested flow (the parent handles it); ordinary step failures; and an unrecoverable string-valued error from `stop_after_if`, while the child job stays successful. The message wrappers are part of the contract: at a first pickup an evaluation error is named `Anyhow` and carries two `Error: ` prefixes and two Rust location suffixes, after a completed child one of each. The location suffixes must be normalised before comparing. The flow-level `skip_expr` ends a flow `skipped`, and a final module's `sleep` is evaluated, because of the virtual step after it ([JE-28](#je-28), [JE-33](#je-33)).
10. **JSON boundaries depend on the read path** ([JE-16](#je-16), [JE-44](#je-44)). Reads from the database see `jsonb`: keys reordered, exponents expanded. The in-memory previous result keeps the producer's text, including `-0`, and its consequences are observable downstream. The text a shortcut returns follows the same split: a `flow_input` member and a previous result read back from storage come in `jsonb`'s spaced form, and a previous result after a loop or `branchall` is a compact array with re-printed numbers (JE-44's table). The top-level key order of `flow_input` and of script arguments comes from a hash map and is not stable.

Every finding below is source-confirmed at its stated scope and confidence, except [JE-50](#je-50) to [JE-52](#je-52), which are `drafted` (written from the pinned source, not yet independently checked). Recording batch E3a (design record 0012) recorded every case of this area on the CE oracle except JE-16/c, JE-19/e and JE-V5/a, and three cases only in part (JE-28/a, JE-36/d, JE-V1/a); the recordings confirmed the source-derived expectations except JE-2/a, JE-28/c, JE-33/a–b, JE-35/b and JE-48/a and c, whose findings were corrected on 2026-10-28. The questions still open are listed at the end.

## Design notes (not Windmill behaviour)

- **Design note (not Windmill behaviour):** the cheapest faithful design is to run the same textual pipeline (item 1 above) on the same engine family wherever expressions are evaluated; most quirks ([JE-5](#je-5), [JE-11](#je-11) to [JE-15](#je-15)) then come for free. Evaluating expressions in Bun's JavaScriptCore would diverge (for example, `Intl` exists in Bun).
- **Design note (not Windmill behaviour):** implement the [JE-9](#je-9) line matcher as specified, not a JavaScript parser, and keep every shortcut condition: they change values, not only speed.
- **Design note (not Windmill behaviour):** the design of JSON at step boundaries must choose, and test, which key order each read sees ([JE-44](#je-44)), and must not turn the hash-map order of top-level keys into a contract ([JE-16](#je-16)).
- Further design notes sit in [JE-2](#je-2) (timeouts), [JE-V5](#je-v5) (batching transforms) and [JE-43](#je-43) (a host secret layer).

## A. Engine, isolation and limits

<a id="je-1"></a>
### JE-1 · QuickJS-ng evaluates every expression that reaches the engine

**Scope:** core · **Confidence:** high (oracle-observed for the names and digits batch E3a recorded; source-traced for the names it did not record: `queueMicrotask`, `performance`, `DOMException`, `WeakRef`, `FinalizationRegistry`, `Iterator`, `structuredClone`, [Q-JE-2](#q-je-2); the image's C library: source-traced, [Q-JE-16](#q-je-16)) · **Status:** confirmed; drafted (how the image links the C library's math functions)

- **Rule:**
  - An expression that no shortcut ([JE-6](#je-6) to [JE-9](#je-9)) answers is evaluated by **QuickJS-ng**, embedded through the `rquickjs` crate at version `0.11.0`. That crate switched its base engine from Bellard's QuickJS to quickjs-ng in 0.9.0; its 0.11.0 changelog identifies quickjs-ng revision `be664ca1ccd86cbadf8df7348b8e15e77a32a1ba`.
  - No V8 or `deno_core` is involved. The `deno_core` runtime in `backend/windmill-runtime-nativets/` runs `nativets` scripts, not expressions ([JE-46](#je-46)).
  - Each evaluation that reaches the engine builds a **new runtime and a new full context** and discards both afterwards. Nothing survives from one evaluation to the next: no globals, no prototype patches, no caches. Each input transform is its own evaluation ([JE-23](#je-23)). The code runs as a strict-mode global script ([JE-50](#je-50)).
  - The context gets **no host APIs** beyond what the evaluator installs: the context values, `flow_input`/`flow_env`, the helpers and `error` ([JE-10](#je-10), [JE-11](#je-11), [JE-17](#je-17), [JE-20](#je-20)).
  - **Absent** (oracle-observed): `console`, `Intl`, `atob`, `btoa`, `TextEncoder`, `TextDecoder`, `URL`, `URLSearchParams`, `fetch`, `setTimeout`, `process` and `require` all have `typeof` `"undefined"`, so a `console.log(...)` call fails. Module imports are unavailable, since the code is a script, not a module. `structuredClone` and `navigator` are absent too (source-traced: the engine does not define them).
  - **Present:** the standard ECMAScript built-ins: `JSON`, `Math`, `Date`, `RegExp` (including lookbehind and named groups), `Map`/`Set`, `Proxy`, `Promise`, `BigInt`, the ES2023 array methods and `Object.groupBy` (oracle-observed). The full context also defines, beyond ECMAScript (source-traced): `queueMicrotask`, a global function that queues an engine job ([JE-51](#je-51)); `performance`, an object with `now()` (milliseconds since the context was created, so the clock restarts at every evaluation) and `timeOrigin`; `DOMException`; `WeakRef`; `FinalizationRegistry`; and `Iterator`.
  - **Transcendental `Math` functions come from the C library.** quickjs-ng computes `Math.sin`, `cos`, `tan`, `asin`, `acos`, `atan`, `atan2`, `exp`, `expm1`, `log`, `log1p`, `log2`, `log10`, `sinh`, `cosh`, `tanh`, `asinh`, `acosh`, `atanh`, `cbrt` and `pow` by calling the function of the same name in the libm of the binary it is linked into. The CE image is built on Debian trixie with the GNU C library, so Windmill's values are **glibc's**: the `windmill` binary is built for the GNU/Linux target, the engine's calls into the C library's math functions stay dynamic, and at run time they resolve against the GNU C library of the runtime image, `debian:trixie-slim` ([JE-48](#je-48) gives the build). Which glibc version that is cannot be read from the source, because the base image is named by tag; the pinned image settles it ([Q-JE-16](#q-je-16)). Whether the same glibc version gives the same digits on another machine was not traced: glibc can pick CPU-specific variants of these functions at run time. Under another libm the last digit can differ: of the values batch E3a recorded, macOS's libm differs on `tan(1)`, `tan(1e10)`, `asin(0.3)`, `acos(0.3)`, `cosh(10)` and `atanh(0.5)`, and an fdlibm-derived libm (V8's, musl's) gives `Math.log(3)` as `1.0986122886681096` where Windmill gives `1.0986122886681098`. The recorded digits are the `math-*` cases of `conformance/exprs/je-1.yaml` (oracle run 40687124625, amd64).
  - **Build feature.** The CE and EE images are built with the `quickjs` feature (the `oss_core` feature group). A build without it fails every expression that *reaches the engine* with ``JavaScript expression evaluation requires the `quickjs` feature...``; expressions a shortcut answers still succeed.
- **Evidence:** `backend/Cargo.lock:10294-10297` (rquickjs 0.11.0), `backend/Cargo.toml:614`; feature wiring `backend/Cargo.toml:126`, `:182-204`; fresh runtime and context `backend/windmill-jseval/src/lib.rs:435-437`, installed globals `:435-552`; fallback stub `:999-1018`; shortcut order `backend/windmill-worker/src/js_eval.rs:44-85`; missing APIs: the in-repository analysis `backend/QUICKJS_MIGRATION_ANALYSIS.md` (sections 2.3 and 7); engine provenance: the rquickjs changelog entry for 0.9.0 ("Switching to quickjs-ng from quickjs as base engine") and the [0.11.0 changelog](https://github.com/DelSkayn/rquickjs/blob/v0.11.0/CHANGELOG.md). The full context: rquickjs 0.11.0 `core/src/context/async.rs:173-183` creates it with quickjs-ng's `JS_NewContext`, which in the copy rquickjs-sys 0.11.0 vendors (`quickjs/quickjs.c:2379-2403`) adds the standard intrinsics plus `WeakRef`, `DOMException` and `performance` (`:57508-57521`); `queueMicrotask` is in the global function list (`:52226-52231`). `Math` through libm: `quickjs.c:45277-45297` (each wrapper calls the C library function of the same name), `:13516-13529` (`pow`), the function table `:45299-45338`; the base image `Dockerfile:1` (`debian:trixie-slim`), used for the runtime stage at `:134`; the GNU/Linux target built without a static C runtime (`backend/.cargo/config.toml:4-10` sets only the linker; `Dockerfile:120` passes no `--target`). The recorded census and digits: the `absent-host-apis`, `present-built-ins` and `math-*` cases of `conformance/exprs/je-1.yaml`.
- **Conformance:** [JE-1/a–e](conformance.md#je-1)
- **Revision 2026-10-28 (oracle, batch E3a; source re-check):** the absent and present lists were source-derived; batch E3a recorded them, and they hold. Added the names the full context defines beyond ECMAScript (`queueMicrotask`, `performance`, `DOMException`, `WeakRef`, `FinalizationRegistry`, `Iterator`), which the finding did not list, the strict mode of [JE-50](#je-50), and the rule that transcendental `Math` values are the C library's (glibc on the CE image), with the digits E3a recorded.
- **Revision 2026-10-28 (validation round 2, design request 9):** added how the image links those functions: dynamically, against the runtime image's glibc, whose version the source does not fix ([Q-JE-16](#q-je-16)). The reading behind deviation D16, that the digits are those of the engine build's C library, is confirmed.

<a id="je-2"></a>
### JE-2 · An evaluation has a 20 s wall-clock limit, tried up to 3 times

**Scope:** later · **Confidence:** high (the limit, the retries, the message: source-traced; the monitor cancel: oracle-observed; which monitor branch a hanging first transform takes: medium) · **Status:** confirmed

- **Rule:**
  - One evaluation attempt may run for **20 000 ms**. An attempt that does not finish in time fails with ``The expression evaluation `<expr>` took too long to execute (>20000ms)``, where `<expr>` is the trimmed text before any rewriting. For a predicate it is the `Boolean(...)`-wrapped text ([JE-5](#je-5), [JE-24](#je-24)).
  - On the worker path a timeout is retried: **up to 3 attempts in total, with a 5 s pause between attempts**, so about 70 s pass before the error surfaces. Only errors whose text contains `took too long` are retried; every other error surfaces at once.
  - No interrupt handler is installed. A CPU-bound loop keeps its thread busy after the timeout fires; the evaluator still reports the timeout error.
  - A promise that never settles (for example `new Promise(() => {})`) also ends in this timeout, and so does a chain of engine jobs that never empties ([JE-51](#je-51)).
  - The limit applies only to evaluations that reach the engine; a shortcut returns before any engine starts.
  - **The flow can be canceled before the timeout surfaces.** While the worker evaluates a flow's transition (the transforms and predicates of the next module), nothing refreshes the flow job's ping. The server's zombie-flow monitor checks running flow jobs (kinds `flow`, `flowpreview`, `flownode`, `singlestepflow`) whose last ping is older than `FLOW_ZOMBIE_TRANSITION_TIMEOUT` seconds (default **60**; read from the environment, any text accepted). It restarts such a flow when its first module is still waiting and the flow is not same-worker; otherwise it cancels it with canceler `monitor` and a reason that begins `Flow <id> (<base url>/run/<id>?workspace=<w>) was cancelled because it was hanging in between 2 steps. Last ping: …` and goes on with worker details. The monitor runs only in server mode (the oracle's standalone mode includes it) and can be disabled. Because 60 s is shorter than the three attempts' 70 s, an expression that times out on every attempt usually ends the flow as **canceled by the monitor**, not with the timeout error. On the oracle a root flow whose first module's transform loops forever was canceled after 60.1 s, before the third attempt ended. Which branch the monitor takes for a root flow hanging in its first transition, and why it canceled rather than restarted there, is not traced; the outcome also depends on timing (the monitor's period, the 60 s threshold against the 70 s of attempts).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:22`, `:303-328`; retry loop `backend/windmill-worker/src/js_eval.rs:81-107`. The monitor: the threshold `backend/src/monitor.rs:174-177`, the scan `:4867-4890`, the restart branch `:4895-4943`, the cancel branch and its reason `:4944-5143`, `cancel_zombie_flow_job` with canceler `monitor` `:5174-5199`, the call in server mode and the `DISABLE_ZOMBIE_JOBS_MONITORING` switch `:185`, `:3058-3069`; a flow job's ping is refreshed when one of its children completes (`backend/windmill-queue/src/jobs.rs:1247-1262`) and during large pushes (`backend/windmill-worker/src/worker_flow.rs:4123-4134`), not during an evaluation. Recorded: case JE-2/a of `conformance/exprs/je-2.yaml` (oracle run 40688787344).
- **Conformance:** [JE-2/a](conformance.md#je-2) (a slow fixture: allow at least 75 s).
- **Design note (not Windmill behaviour):** windmill-lite should keep the message. Interrupting a runaway evaluation, or dropping the retries, would be a deviation from Windmill; if adopted, it must be listed in the equivalence contract. windmill-lite has no zombie-flow monitor; whether it reproduces the monitor's cancel is a design decision.
- **Revision 2026-10-28 (oracle, batch E3a; source re-check):** JE-2/a expected the step to fail with the timeout message. On the oracle the flow was canceled by the zombie-flow monitor at 60 s, before the third attempt ended, so the message never surfaced. Added the monitor rule and the timing dependence; the attempt limit, the retries and the message are unchanged.

<a id="je-3"></a>
### JE-3 · An evaluation is capped at 128 MiB of heap

**Scope:** later · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - An evaluation's heap is capped at **128 MiB**. The `QUICKJS_MEMORY_LIMIT_MB` environment variable overrides the cap.
  - **How the variable is read.** Once per process, when the first flow evaluation needs the cap; a change takes effect only after a restart. The text must parse as an unsigned decimal integer: no surrounding whitespace, no fraction, no unit, an optional leading `+`. A missing variable, an unparsable value or `0` leaves the default. A value `N` gives `N × 1 048 576` bytes (so "MB" means MiB), saturating at the largest size the platform has instead of overflowing.
  - Running out of memory produces ``The expression evaluation exceeded the memory limit of <N> MB. Reduce the amount of data handled in the expression, or raise the cap via the QUICKJS_MEMORY_LIMIT_MB environment variable.``, where `<N>` is the cap in bytes divided by 1 048 576, rounded down: the configured value, or `128`.
  - QuickJS signals an out-of-memory condition either as an `InternalError` whose text is exactly `out of memory`, or as a bare `null`/`undefined` throw. The evaluator treats both as out of memory. A user's explicit `throw null` or `throw undefined` therefore *also* produces this message. A user's `throw new Error("out of memory")` stays an ordinary error.
  - Like [JE-2](#je-2), the cap applies only to evaluations that reach the engine. The variable governs flow evaluations only; the simple evaluator outside flows has a fixed 32 MiB cap ([JE-45](#je-45)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:331-364` (the default `:342`, the variable read once `:355-364`), `:366-421`; the cap passed to each evaluation `:292-293`, `:435-436`; tests `:2837-2945`. Recorded: case JE-3/a of `conformance/exprs/je-3.yaml` (oracle run 40687124625).
- **Conformance:** [JE-3/a](conformance.md#je-3)
- **Revision 2026-10-28 (source re-check):** the finding said only "a positive integer, in MB". Added how the variable is parsed (once per process; the default for missing, invalid or zero values; mebibytes; saturation), which `<N>` the message prints, and that it does not reach the simple evaluator. JE-3/a was recorded and holds.

<a id="je-48"></a>
### JE-48 · The stack limit is quickjs-ng's built-in 1 MiB, measured from where the embedding enters the engine

**Scope:** later · **Confidence:** high (the limit and the overflow behaviour: source-traced through Windmill, rquickjs 0.11.0 and quickjs-ng `be664ca`, and oracle-observed; why fewer than 1 000 frames fit on Windmill: medium; the CE image's engine build: source-traced, [Q-JE-16](#q-je-16)) · **Status:** confirmed; drafted (the CE image's engine build)

- **Rule:**
  - **No stack setting.** Windmill's evaluator creates each runtime with a heap cap only ([JE-3](#je-3)). It never sets a maximum stack size, and no environment variable changes one: `QUICKJS_MEMORY_LIMIT_MB` affects the heap only. The simple evaluator outside flows ([JE-45](#je-45)) does the same.
  - **The value.** The limit is therefore quickjs-ng's compiled-in default, `JS_DEFAULT_STACK_SIZE` = **1 MiB** (1 048 576 bytes). rquickjs 0.11.0 does not change it when it creates a runtime. The rquickjs documentation of its stack setter claims a default of 256 KiB; that comment is stale and does not describe the pinned engine.
  - **What it measures.** The limit is native (C) stack, not a count of JavaScript frames. The engine fails once the stack pointer has moved more than 1 MiB below a recorded top. With the `parallel` feature, which Windmill enables, rquickjs re-records that top every time the evaluation's future is polled, so each poll gets the full 1 MiB below the frame that polls. The engine jobs that poll runs ([JE-51](#je-51)) share that budget; the few Rust and engine frames between the poll and the first JavaScript call are charged to it too.
  - **The thread.** An evaluation runs on a Tokio blocking-pool thread. Windmill sets no thread stack size and its image sets no `RUST_MIN_STACK`, so the thread has Rust's default of 2 MiB. The engine's 1 MiB limit is therefore reached before the thread's own stack runs out.
  - **What happens on overflow.** The engine throws a `RangeError` with the message `Maximum call stack size exceeded`. It is an ordinary exception:
    - caught inside the expression, it is a value like any other;
    - uncaught, it fails the evaluation as [JE-21](#je-21) describes, so the text is `QuickJS evaluation error: Error: Maximum call stack size exceeded`, followed by the stack (its ten innermost frames, here all `at f (eval_script:…)`; see [JE-21](#je-21)), and each site wraps it ([JE-23](#je-23) to [JE-35](#je-35)).
    - An expression that holds a `;` outside quotes runs in statement mode ([JE-13](#je-13)): an uncaught overflow still fails it, but a value the expression computes, a caught overflow included, is discarded and the result is `null`.
  - **Where the engine checks:** at every JavaScript call (calls into built-ins included), while parsing deeply nested source, while parsing or stringifying deeply nested JSON (`JSON.parse`, `JSON.stringify`, the final serialisation of [JE-15](#je-15) included), and while walking proxy chains and flattening nested arrays.
  - **How many frames fit on Windmill** (oracle-observed, CE `v1.767.0` amd64). For the recursion `g(g, n) = n === 0 ? 0 : 1 + g(g, n - 1)` started from an arrow function, depths 10, 100, 300 and 600 fit, and a recursion of depth 1 000 of the same shape (`f = n => n === 0 ? 0 : 1 + f(n - 1)`) overflows. One such JavaScript call therefore takes more than about 1 KiB of C stack in Windmill's engine (1 MiB ÷ 1 000 ≈ 1 049 bytes), and at most about 1.7 KiB (1 MiB ÷ 600). The reason is the engine's build, not the limit (medium: the per-frame size was not measured): Windmill compiles quickjs-ng through rquickjs-sys's build script with the `cc` crate, under Cargo's release profile (optimisation level 3, which `cc` passes to the C compiler), with the build image's default C compiler, and **with assertions compiled in**, because the `disable-assertions` feature of rquickjs, which would define `NDEBUG`, is not enabled. The size of the interpreter's C frame for each JavaScript call follows from those choices.
  - **The CE image's engine build** (source-traced, so far as the source fixes it). The published CE image is built from the repository's `Dockerfile` with only the build argument `features=ce`, for `linux/amd64` and `linux/arm64`, so the default base images apply. The engine is compiled in the builder stage, on the image tag `rust:1.97-slim-trixie` (Debian trixie), for the host's GNU/Linux target (no `--target` and no target override), by the `cc` crate 1.3.0 with no `CC` or `CFLAGS` set, so with the compiler the base image calls `cc`. The source fixes these flags: optimisation level 3, `_GNU_SOURCE` defined, `NDEBUG` not defined, and `cc`'s defaults for a release build (position-independent code, one section per function and per datum). The C code becomes a static library inside the `windmill` binary; the binary is stripped of debug information. The source does **not** fix which compiler `cc` is or its version, since the base images are named by tag, not by digest, and both GCC and Clang are installed in the builder; only the pinned image can tell ([Q-JE-16](#q-je-16)).
  - **Depth depends on the build.** How many nested calls fit in 1 MiB depends on the size of the engine's C frames, and so on the compiler, its flags and the CPU architecture. The recordings above give Windmill's outcomes on the pinned oracle for these expressions: depths 10, 100, 300 and 600 fit, and the depth-1 000 expression overflows. They do not locate the exact limit.
- **Evidence:** no stack setting `backend/windmill-jseval/src/lib.rs:435-437`, `:972-973`; the blocking thread `:296-327`; the runtime builder, without a thread stack size, `backend/src/main.rs:221-225`; rquickjs features `backend/Cargo.toml:614`; rquickjs 0.11.0 [`runtime/raw.rs:194-199`](https://github.com/DelSkayn/rquickjs/blob/v0.11.0/core/src/runtime/raw.rs#L194) (re-recording the top, `parallel` only), [`:251-257`](https://github.com/DelSkayn/rquickjs/blob/v0.11.0/core/src/runtime/raw.rs#L251) (the opt-in setter and its stale comment), [`context/async/future.rs:91`](https://github.com/DelSkayn/rquickjs/blob/v0.11.0/core/src/context/async/future.rs#L91) (the top re-recorded on each poll); quickjs-ng [quickjs.h @ be664ca](https://github.com/quickjs-ng/quickjs/blob/be664ca1ccd86cbadf8df7348b8e15e77a32a1ba/quickjs.h) line 372 (the default) and [quickjs.c @ be664ca](https://github.com/quickjs-ng/quickjs/blob/be664ca1ccd86cbadf8df7348b8e15e77a32a1ba/quickjs.c) lines 1827-1832 (the check), 1888-1893 (the default applied when a runtime is created), 2589-2612 (limit = top minus size), 7351-7354 (the `RangeError`), 16425 (calls), 20938 (the parser), 21568 (the JSON tokenizer), 47165 and 47345 (the `JSON.parse` reviver and `JSON.stringify`). The quickjs-ng copy vendored in rquickjs-sys 0.11.0 differs from `be664ca` only by one added string-freeing helper and one promise-state constant, so its line numbers after line 4362 are 8 higher. The engine's build: rquickjs-sys 0.11.0 `build.rs:149-152` (`NDEBUG` only with `disable-assertions`), `:157-162` (the `cc` build); `disable-assertions` is off in rquickjs-core 0.11.0's `Cargo.toml:52-53` defaults and in Windmill's features `backend/Cargo.toml:614`; the release profile `backend/Cargo.toml:107-110` (no `opt-level`, so Cargo's default of 3); the build stage `Dockerfile:2`, `:24-26`, `:96-120` (no `CC` set, no `--target`), the strip `:125-127`, the binary copied into the runtime stage `:134`, `:142`, `:280`; the `cc` crate `backend/Cargo.lock:2292-2294`; no target override in `backend/.cargo/config.toml:1-24` (only linker settings); the published image's build `.github/workflows/docker-image.yml:86-100` (`features=ce`, both platforms). Recorded: cases JE-48/a–d and the `catchable` and `depth-bracket` variants of `conformance/exprs/je-48.yaml` (oracle runs 40687124625 and 40691336592).
- **Conformance:** [JE-48/a–d](conformance.md#je-48)
- **Design note (not Windmill behaviour):** pinning the same 1 MiB keeps the limit equal, but an engine compiled differently still overflows at a different depth. windmill-lite's own engine, built at `-O2` with `NDEBUG`, overflows the depth-1 000 recursion only at 1 037 to 1 089 frames depending on the platform, so it succeeds where Windmill's recorded run fails. Whether windmill-lite must match that outcome, or lists it as a deviation, is a compatibility decision for the equivalence contract ([design record 0001](../design/0001-equivalence-contract.md)); this reference does not settle it. A portable smoke test that holds for both builds could check that depth 600 fits and that depth 1 000 000 overflows; that is a proposal, not Windmill behaviour. Calibrating windmill-lite's frames to Windmill's would need the same compiler, at the same version, with the flags the source fixes; the source does not name the compiler ([Q-JE-16](#q-je-16)).
- **Revision 2026-10-28 (validation round 1):** the 2026-10-28 text added a "compatibility band" stating that a conforming engine may give either outcome between 600 and 1 000 000 and that no fixture may assert an overflow in that range. The source and recordings do not establish that, so it is removed: the rule keeps the 1 MiB limit, build-dependent frame sizes and the recorded outcomes (depths 10 to 600 fit, depth 1 000 overflows on the pinned oracle), and the smoke band moves to the design note as a proposal.
- **Revision 2026-10-28 (oracle, batch E3a):** the finding was `drafted`; the independent check of its source trace passed, and batch E3a recorded its cases. Two corrections. (1) The finding called 1 000 "far below any plausible limit", and JE-48/a expected a recursion of depth 1 000 to fit; on the oracle it overflows, while depths up to 600 fit. Added how many frames fit on Windmill and the medium-confidence reason (the engine's build). (2) The catalogue's JE-48 expressions hold a `;`, so they run in statement mode: JE-48/c's caught value is discarded and the result is `null`; a semicolon-free variant returns `["RangeError","Maximum call stack size exceeded"]`. The 1 MiB limit, its measurement and the overflow message are unchanged.
- **Revision 2026-10-28 (validation round 2, design request 9):** added what the source fixes about the CE image's engine build (base images, target, the `cc` crate and its flags, assertions on) and what it does not (the compiler and its version), with [Q-JE-16](#q-je-16) for the recording that settles it. The reading behind deviation D17, that the overflow depth follows the compiled engine's frames, is confirmed; a calibration is possible only once the compiler is known.

<a id="je-49"></a>
### JE-49 · Context values nested deeper than 127 levels fail before evaluation

**Scope:** later · **Confidence:** high (source-traced and oracle-observed; the position, the order and what reaches the binding: source-traced) · **Status:** confirmed; drafted (which text the position counts in, which value is reported, and what reaches the binding)

- **Rule:**
  - **Where the limit sits.** Before QuickJS runs, every bound context value is parsed from its JSON text into a Rust JSON tree and then converted ([JE-16](#je-16)). The parser is serde_json 1.0.151 without its `unbounded_depth` feature (no manifest in the workspace enables it). It allows at most **127 nested arrays or objects**: entering the 128th fails.
  - **Which values:** `previous_result` and `result`, `iter`, `params` and the other names of the transform context, each value of `flow_env`, and the whole `flow_input` object. For `flow_input` the outer object is the first level, so a key whose value is 127 levels deep already fails.
  - **What fails.** The evaluation fails with serde_json's text, `recursion limit exceeded at line <l> column <c>`, without the `QuickJS evaluation error: ` prefix, and each site wraps it as usual. No JavaScript runs.
  - **Which text the position counts in** (source-traced). For a transform-context value (`previous_result`, `result`, `iter`, …) and for each member of `flow_env`, the line and column count in that value's own JSON text as it is held: the producer's text for an in-memory previous result, the stored text otherwise. `flow_input` is different: the evaluator first writes the whole input object out again, compactly (`{"<key>":<value>,…}`, no spaces between the outer keys), with each member's value copied in as its held text (from the database that is `jsonb`'s output, `{"a": 1}` with spaces), and parses that. Its position therefore counts from the outer `{` and includes the keys before the deep member, in the outer object's hash-map order ([JE-16](#je-16)), so with several keys it can change from run to run.
  - **Which value is reported when several are too deep** (source-traced). The values are converted one at a time and the first failure ends the evaluation. The order is: the transform-context values the text names, in the transform context's hash-map order (not by name, and not stable between runs); then `previous_result` when only the `error` text or a previous-step spelling added it ([JE-10](#je-10)); then `flow_input`; then the members of `flow_env`, in its hash-map order.
  - **What reaches the binding** (source-traced 2026-10-29). Nothing before the evaluator limits the depth of a `flow_input` member or a `flow_env` member. The run API parses the request body as an object whose members are kept as raw JSON text, and serde_json reads a raw member without any depth budget; the deployment decodes `flow_env` the same way, as raw member texts; PostgreSQL's `jsonb` accepts far deeper nesting. So a run started with `{"k": <127 nested arrays>}` and a deployed flow with a `flow_env` member 128 levels deep both reach the binding and fail there, as this finding says.
  - **What does not fail.** A shortcut ([JE-6](#je-6) to [JE-9](#je-9)) returns stored JSON without parsing it, so `x = previous_result` passes a value of any depth through. Values built inside the expression have no such limit; only the stack limit ([JE-48](#je-48)) applies to them.
  - Depth counts containers: `[]` is depth 1 and `[[]]` depth 2.
- **Evidence:** context parsing `backend/windmill-jseval/src/lib.rs:455-470` (`flow_input` re-serialised and parsed; transform-context values parsed), `:473-482` (`flow_env`); the order of the names converted `:267-284` (the transform context's keys first, then `previous_result`, then `flow_input`), the hash maps `:19`, `:236-238`; conversion `:825-862`; serde_json features `backend/Cargo.toml:435`; serde_json 1.0.151 [`src/de.rs:63`](https://github.com/serde-rs/json/blob/v1.0.151/src/de.rs#L63) (the depth budget of 128), [`:1357-1387`](https://github.com/serde-rs/json/blob/v1.0.151/src/de.rs#L1357) (the check), [`src/error.rs:384`](https://github.com/serde-rs/json/blob/v1.0.151/src/error.rs#L384) (the message) and [`:405-415`](https://github.com/serde-rs/json/blob/v1.0.151/src/error.rs#L405) (the position suffix). Recorded: runs `je49-a`, `je49-b` and `je49-c` of `conformance/flows/je/sites` (oracle run 40687124625).
- **Conformance:** [JE-49/a–e](conformance.md#je-49)
- **Design note (not Windmill behaviour):** an engine that parses context JSON inside JavaScript (`JSON.parse`) has no 127-level limit, so it accepts values that Windmill rejects. Reproducing JE-49 needs an explicit depth check before binding.
- **Revision 2026-10-28 (oracle, batch E3a):** the finding was `drafted`, at medium confidence, because it was not traced that a 128-level step result reaches the binding unchanged. The independent check passed, and batch E3a recorded all three cases as stated: a Bun step's 128-level result reaches the binding and fails with `recursion limit exceeded at line 1 column 128`, without the `QuickJS evaluation error: ` prefix. No rule changed.
- **Revision 2026-10-28 (validation round 2, design request 9):** added which text the reported position counts in and which value is reported when several are too deep. Two readings are corrected. (1) "Counted in the compact text" holds only for the outer object of `flow_input`: its members' values keep their held text, which from the database has `jsonb`'s spaces, and a member of `flow_env` counts in its own text. (2) The order is not `flow_input`, then the context values by name, then `flow_env`: it is the named context values in hash-map order, then `previous_result` when added by the `error` or previous-step trigger, then `flow_input`, then `flow_env`'s members in hash-map order. No case is added: whether a run can be given a `flow_input` or `flow_env` deep enough to reach the binding is not traced ([Q-JE-15](#q-je-15)).
- **Revision 2026-10-29 (SC3, source re-check, Q-JE-15):** traced that both can. The run API parses the body into members held as raw text (`backend/windmill-api/src/args.rs:668-691`), and serde_json 1.0.151 reads a raw value by skipping it with an explicit stack and no depth budget (`src/de.rs:1102-1162`, `:1285-1293`); the deployed flow's `flow_env` is a map of raw member texts in the typed flow value (`backend/windmill-types/src/flows.rs:304`), decoded the same way at deploy and at run time. The new bullet "What reaches the binding" says so, and new cases JE-49/d and JE-49/e (drafted, source-traced) give the expected texts.

<a id="je-50"></a>
### JE-50 · Expressions run as strict-mode global scripts

**Scope:** core · **Confidence:** high (source-traced through Windmill and rquickjs 0.11.0; oracle pending, [Q-JE-12](#q-je-12)) · **Status:** drafted

- **Rule:**
  - The evaluator runs every piece of code it builds with rquickjs's default evaluation, which is a **global script** (not a module) with **strict mode forced**, under the file name `eval_script`. That covers the wrapped expression ([JE-13](#je-13)), the `error` set-up ([JE-17](#je-17)) and the helper set-ups ([JE-20](#je-20)). No `"use strict"` directive is needed or visible: the whole script, and every function it defines, is strict.
  - Observable consequences, all ordinary ECMAScript strict-mode rules:
    - `this` inside a plain function call is `undefined`, not the global object;
    - assigning to an undeclared name throws a `ReferenceError` (`<name> is not defined`) instead of creating a global;
    - `with`, legacy octal literals such as `010`, and `delete` of a plain name are syntax errors;
    - assigning to a read-only property, or adding one to a frozen object, throws a `TypeError`.
  - The simple evaluator outside flows ([JE-45](#je-45)) evaluates the same way.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:525`, `:544-552`, `:636`, `:662`, `:787`, `:800`, `:818` (each set-up evaluated with the default options), `:555-562` (the wrapped expression), `:988` (the simple evaluator); rquickjs 0.11.0 `core/src/context/ctx.rs:67-78` (the default options: global, strict), `:160-163` (`eval` uses them), `:180-201` (the file name `eval_script`); quickjs-ng's strict flag `quickjs/quickjs.c:34956` in rquickjs-sys 0.11.0.
- **Conformance:** [JE-50/a](conformance.md#je-50)

<a id="je-51"></a>
### JE-51 · The engine's job queue is drained before the result is read, and a throwing job is ignored

**Scope:** later · **Confidence:** high (source-traced through Windmill, rquickjs 0.11.0 and quickjs-ng; oracle pending, [Q-JE-12](#q-je-12)) · **Status:** drafted

- **Rule:**
  - The evaluation runs as a future that rquickjs polls. On each poll it first checks whether the expression's result promise has settled, and reads it if so. Otherwise it drives the pending host calls (the `variable`, `resource`, `flow_user_state` and `results` lookups, [JE-18](#je-18), [JE-20](#je-20)), then runs **every** queued engine job, in the order queued, including the jobs those jobs queue, until the queue is empty, and polls again.
  - **Consequences:**
    - Jobs the expression queues (promise reactions, `queueMicrotask` callbacks) run in first-in, first-out order, before the result is serialised whenever they were queued before the serialising reaction.
    - The result is read only after the queue empties. A job chain that never ends (for example a callback that queues itself again) therefore keeps the evaluation from finishing, even after the value has settled, and it ends in the timeout of [JE-2](#je-2).
    - A job whose function throws, as a `queueMicrotask` callback that throws does, is **dropped**: it neither fails the evaluation nor appears in any message, and the evaluation goes on. (A promise reaction never throws in this sense: an error inside it rejects the promise it belongs to.)
    - No unhandled-rejection tracking is installed: a rejected promise that is not the expression's result does not fail the evaluation.
- **Evidence:** the evaluation awaited inside rquickjs's `async_with!` `backend/windmill-jseval/src/lib.rs:451`, `:562-565`; rquickjs 0.11.0 `core/src/context/async/future.rs:107-150` (each poll: the result first, then the host-call scheduler, then every pending job until none is left; a job error is ignored), `core/src/value/promise.rs:186-215` (the result promise's future); quickjs-ng `JS_ExecutePendingJob` (`quickjs/quickjs.c:2041-2068` in rquickjs-sys 0.11.0: one job per call, an exception reported to the caller and not rethrown) and `queueMicrotask` (`:37737-37751`).
- **Conformance:** [JE-51/a](conformance.md#je-51)

<a id="je-52"></a>
### JE-52 · Unpaired surrogates: results carry them escaped; an error message holding one is dropped

**Scope:** later · **Confidence:** medium (source-traced through rquickjs 0.11.0 and quickjs-ng; oracle pending, [Q-JE-12](#q-je-12)) · **Status:** drafted

- **Rule:**
  - **Results.** A value leaves the engine only through the `JSON.stringify` of [JE-15](#je-15). quickjs-ng's `JSON.stringify` writes each unpaired surrogate code unit as a six-character escape with **lowercase** hex (`"\ud83d"`); a valid surrogate pair is written as the character itself. The evaluator's output text therefore never holds a raw lone surrogate. What happens when that text is then stored (as step arguments, through PostgreSQL `jsonb`) belongs to [execution](execution.md) ([FX-48](execution.md#fx-48)).
  - **Error messages.** When an uncaught error's `message` holds an unpaired surrogate, the engine encodes it for the host as a three-byte sequence that is not valid UTF-8; rquickjs then fails to read the message and treats it as absent. The evaluator's text is `QuickJS evaluation error: Error:` followed directly by a newline and the stack ([JE-21](#je-21)): the message is **dropped**, and nothing of it reaches the step error.
- **Evidence:** quickjs-ng's string quoting for JSON `quickjs/quickjs.c:13183-13248` in rquickjs-sys 0.11.0 (surrogates and control characters escaped as `\u` with four lowercase hex digits); the host encoding `:4238-4352` (unpaired surrogates kept as three-byte sequences); rquickjs 0.11.0 `core/src/value/string.rs:11-26` (a strict UTF-8 check on every string read), `core/src/value/exception.rs:73-78` (the message read, a failure giving none) and `:217-230` (the display without a message); the serialisation `backend/windmill-jseval/src/lib.rs:555-567`.
- **Conformance:** [JE-52/a](conformance.md#je-52)

<a id="je-4"></a>
### JE-4 · Reserved (unused)

**Status:** withdrawn

## B. The evaluation pipeline and its shortcuts

Every flow expression goes through one entry point. It **trims** the text ([JE-5](#je-5)) and then tries four shortcuts in a fixed order: [JE-6](#je-6) → [JE-7](#je-7) → [JE-8](#je-8) → [JE-9](#je-9). Only if all four miss does QuickJS run ([section C](#c-the-quickjs-path)). The shortcuts return **stored JSON text verbatim**, with no JavaScript round trip, so they can differ from the QuickJS path in number precision, key order and leniency. A predicate's text starts with `Boolean(`, so JE-6 to JE-8 never fire for predicates, but JE-9 can ([JE-24](#je-24)).

<a id="je-5"></a>
### JE-5 · Trimming, and what error messages quote

**Scope:** core (evaluation); later (message spelling) · **Confidence:** high (oracle-observed) · **Status:** confirmed; drafted (the 2026-10-28 additions for design request 8)

- **Rule:**
  - **What is trimmed.** The evaluator removes leading and trailing whitespace, newlines included, from the text it evaluates. The shortcuts and the statement-mode detection ([JE-13](#je-13)) see the trimmed text.
    - "Whitespace" is every character with the Unicode `White_Space` property, the set Rust's string trim removes: ASCII space, tab, `\n`, `\r`, vertical tab and form feed, and also U+0085, U+00A0, U+1680, U+2000 to U+200A, U+2028, U+2029, U+202F, U+205F and U+3000. A zero-width space (U+200B) and a byte order mark (U+FEFF) are not `White_Space` and stay.
  - **What messages quote.**
    - Site error messages (``Error during isolated evaluation of expression `…` ``, ``Error during evaluation of expression `…` `` and the predicate log lines) quote the expression **as written in the flow**, untrimmed.
    - The timeout message ([JE-2](#je-2)) quotes the trimmed text.
  - **Predicates are wrapped before trimming.** The text evaluated is `Boolean(` + the expression as written + `)`. Whitespace and newlines inside the wrapper survive; only the outside of the wrapper is trimmed ([JE-24](#je-24)).
- **Evidence:** `backend/windmill-worker/src/js_eval.rs:36` (the standard library's trim, defined by `White_Space`); `backend/windmill-worker/src/worker_flow.rs:2451-2452`, `:2691-2694`, `:2763-2765`; `backend/windmill-jseval/src/lib.rs:243`, `:324-327`, `:866` (statement detection trims again, a no-op).
- **Conformance:** [JE-5/a, JE-5/b](conformance.md#je-5)
- **Revision 2026-10-28 (source re-check, design request 8):** added which characters "whitespace" means. The reading windmill-lite's textual pipeline (B2a) took, Unicode `White_Space` as Rust's trim removes, is confirmed; nothing changes.

<a id="je-6"></a>
### JE-6 · An exact context name returns the stored value verbatim

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - If the trimmed text is exactly the name of a value in the evaluation context, that value's stored JSON is returned unchanged.
  - The possible names depend on the site ([JE-22](#je-22)): `previous_result`, `result`, `params`, `resume`, `resumes`, `approvers`, `all_iters`, `started_at`, `flow_job_id`.
  - `flow_input` and `flow_env` are passed separately, not as context entries, so this shortcut does not apply to them; [JE-7](#je-7) does.
  - "Unchanged" means the text the worker holds for that name. For `previous_result` that text depends on where the value came from ([JE-44](#je-44), the table of returned texts).
- **Evidence:** `backend/windmill-worker/src/js_eval.rs:44-46`.
- **Conformance:** [JE-6/a, JE-6/b](conformance.md#je-6)

<a id="je-7"></a>
### JE-7 · A top-level `flow_input` or `flow_env` key returns the stored value verbatim

**Scope:** core (plain keys); later (dotted-key quirk) · **Confidence:** high (oracle-observed; the empty key: source-traced) · **Status:** confirmed; drafted (the empty key, a 2026-10-28 addition for design request 8)

- **Rule:**
  - **Dot form.** If the text starts with `flow_input` followed by `.`, *everything after the first dot* is used as **one** key of the flow input. If the input has that key, its stored JSON is returned verbatim. The key may be empty: `flow_input.` looks up the key `""`, and returns its value when the input has a key that is the empty string (otherwise the text goes to the engine, where it is a `SyntaxError`).
  - **Bracket form.** For `flow_input["…"]`, the text must end with `"]` and the key between the quotes must be non-empty.
  - `flow_env.<…>` and `flow_env["…"]` behave the same way against the flow env ([JE-38](#je-38)).
  - **On a miss**, the text is evaluated on the QuickJS path with ordinary JavaScript semantics. Only a missing **direct** property of an existing object yields `null` ([JE-15](#je-15)); `flow_input.absent.child` throws a `TypeError` and the step fails.
  - **Quirk:** `flow_input.a.b` first looks up a top-level key literally named `a.b`. Only if there is none does JavaScript evaluate `flow_input.a.b`.
  - The returned text is exactly the stored text, so a number like `12345678901234567890` keeps its full precision. JavaScript arithmetic on it does not ([JE-16](#je-16)).
  - **Which text that is.** The flow input is the flow job's arguments as decoded from its `jsonb` column, member by member without re-encoding. A member is therefore returned as PostgreSQL prints the stored `jsonb`: an object or array value has `": "` after each key and `", "` between members and items, its keys are in `jsonb` order, and its numbers are in `numeric`'s spelling ([JE-44](#je-44)). `flow_input.o` with `o` submitted as `{"b":1,"a":[1,2]}` returns `{"a": [1, 2], "b": 1}`, not a compact text.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:51-55` (the prefixes), `:117-163` (`:136-138` the dot form, with no emptiness check; `:139-150` the bracket form, whose key must be non-empty), `backend/windmill-worker/src/js_eval.rs:48-52`, `:81-107`; the flow input's source `backend/windmill-worker/src/worker_flow.rs:3186-3194`, its type and query `backend/windmill-queue/src/jobs.rs:2754-2758`, `:3231-3241` (the arguments decoded as raw text per member from `v2_job.args`).
- **Conformance:** [JE-7/a–g](conformance.md#je-7)
- **Revision 2026-10-28 (source re-check, design request 8):** windmill-lite's textual pipeline (B2a) took two readings here. (1) The dot form takes an empty key: confirmed. (2) A member read from storage is returned as a compact stored text: **corrected**. An object or array member comes back in `jsonb`'s printed form, with a space after each `:` and `,`; scalars agree with the compact reading. B2b's tests that pin a compact text for an object or array member of `flow_input` must change. Not yet recorded ([Q-JE-13](#q-je-13)).
- **Revision 2026-10-29 (oracle, batch E3e):** reading (2)'s correction is recorded: `flow_input.o`, with `o` submitted as `{"b": 1, "a": 2}`, returns `{"a": 2, "b": 1}`, spaced and in `jsonb` order, both at the first module and after a step ([JE-44/d](conformance.md#je-44); `conformance/flows/je/shortcut-texts`, oracle run 40794872025). The empty-key reading (1) stays source-traced.

<a id="je-8"></a>
### JE-8 · The previous step's id returns `previous_result` verbatim

**Scope:** core (named previous id); later (empty-id spellings) · **Confidence:** high (oracle-observed; the re-printed numbers after a loop and the texts read back from storage: source-traced) · **Status:** confirmed; drafted (the 2026-10-28 additions for design request 8, except the compact text after a loop)

- **Rule:**
  - When the site has a results context and its context holds `previous_result`, four spellings return `previous_result` verbatim: `results.<P>`, `results?.<P>`, `results["<P>"]` and `results?.["<P>"]`, where `<P>` is the previous id.
  - The text must be **exactly** one of these four. A longer path such as `results.a.n` is not one of them and goes to [JE-9](#je-9) ([JE-V6](#je-v6)).
  - **Empty previous id.** The spellings are built even when the previous id is empty. At the first module of a flow or body the previous id is `""`, so the exact texts `results[""]` and `results?.[""]` return `previous_result`, which is that flow job's input object ([JE-36](#je-36)). So do `results.` and `results?.`, which are not valid JavaScript.
  - On the QuickJS path, the `results` proxy compares against the same empty id, so `results[""]` inside a larger expression also yields `previous_result` ([JE-18](#je-18)).
  - **"Verbatim" means the producer's text** (oracle-observed). The shortcut returns the previous step's in-memory result text, in the producer's key order and spelling. Where that text is stored as step arguments it is `jsonb`-normalised ([JE-44](#je-44)), but a site that quotes the value keeps it: when a `timeout` of `results.p` (with `p` the previous step, returning `{"b":1,"a":2}`) fails the integer parse, the message ends `Value was: {"b":1,"a":2}`, while the same expression reaching `p` through the [JE-9](#je-9) lookup (another step in between) quotes the stored text, `Value was: {"a": 2, "b": 1}` ([JE-29](#je-29)).
  - **"Verbatim" depends on where `previous_result` came from.** The producer's text holds only after an ordinary step whose job completed in the same transition. At other positions `previous_result` is a text the worker built, which the shortcut returns as it is ([JE-44](#je-44) gives the table): at the first module and in the preprocessor, a compact object of the flow arguments whose member values are `jsonb`'s printed text; after a loop or `branchall`, a compact array the worker rewrote from the stored child results; and when `previous_result` was read back from storage, `jsonb`'s printed text.
- **Evidence:** `backend/windmill-worker/src/js_eval.rs:54-72`; previous-id computation `backend/windmill-worker/src/worker_flow.rs:3658-3663`; proxy `backend/windmill-jseval/src/lib.rs:805-817`; where `previous_result` comes from `backend/windmill-worker/src/worker_flow.rs:3324-3346`, `:3776-3792`, `:2299-2326`, `:6234-6264`. Recorded: runs `je8-a`, `je8-b`, `je8-order-transform`, `je8-order-timeout` and `je8-order-timeout-db` of `conformance/flows/je/shortcuts`, cases JE-8/c and JE-8/d of `conformance/exprs/je-8.yaml` (oracle run 40687124625).
- **Conformance:** [JE-8/a–d](conformance.md#je-8)
- **Revision 2026-10-28 (oracle, batch E3a):** added the recorded key-order discriminator: a quoting site shows the producer's text through this shortcut and the `jsonb` text through JE-9. The rule is unchanged.
- **Revision 2026-10-28 (source re-check, design request 8):** added which text "verbatim" is at the positions where `previous_result` was not produced in memory. windmill-lite's textual pipeline (B2a) reads a value from storage as the compact stored text. That reading is **corrected** for the step after a loop or `branchall`, where numbers are re-printed ([JE-44](#je-44)), and for a `previous_result` read back from storage, which is spaced. B2b's tests that pin those texts must change. Not yet recorded ([Q-JE-13](#q-je-13)).
- **Revision 2026-10-29 (oracle, batch E3e):** the text right after a loop is recorded: `results.l`, with the loop's iteration returning `{"b":1,"a":2}`, returns `[{"a":2,"b":1}]`, compact and in `jsonb` key order ([JE-44/e](conformance.md#je-44); `conformance/flows/je/shortcut-texts` run `loop_prev`, oracle run 40794872025). The re-printed numbers and the texts read back from storage stay source-traced. After an identity job, `previous_result` is `jsonb`'s printed text ([JE-44](#je-44), chain N1a).

<a id="je-9"></a>
### JE-9 · Simple `results` paths are served by a lenient database lookup

**Scope:** core (shortcut and leniency); later (first-match `.length` decline) · **Confidence:** high (oracle-observed; per-path `#>` edge cases: medium) · **Status:** confirmed; drafted (the 2026-10-28 additions for design request 8)

- **Rule:**
  - **Where it applies.** At every site with both an authenticated client and a results context: step input transforms, the loop iterator, `parallelism`, `timeout`, and predicates (`skip_if` and `branchone` always; `stop_after_if` and `stop_after_all_iters_if` when their text mentions `results`). It does not apply to `sleep`, `retry_if` or flow `skip_expr` (no results context) or to `suspend.user_groups_required` (no client). See [JE-22](#je-22).
  - **What a qualifying line is.** A line qualifies when, by itself and starting at column 0, it has this form:
    1. `results` (optionally `results?`), then `.` and a step id made of `[A-Za-z0-9_]`;
    2. optionally one array index written `[<digits>]` immediately after the step id;
    3. optionally one or more `.segment` parts, each made of `[A-Za-z0-9_]`, so digit-only segments are allowed.

    Optional chaining after the step id (`results.a?.b`) does not qualify, and neither does an index anywhere other than right after the step id (`results.a.b[0]`).
  - **Line-by-line matching.** The pattern is matched against each line of the trimmed text. Only the **first** matching line is inspected:
    - if its last segment is `length`, the whole shortcut declines and the text goes to QuickJS; later lines are **not** searched for another match;
    - otherwise the shortcut fires on that line: **all other text is ignored**, and no JavaScript runs.
  - Indented lines never qualify. The first line is never indented, because the text is trimmed.
  - **Where a line ends.** Lines are split at `\n` only. A `\r` before the `\n` stays part of the line, and so does any trailing space or tab; since neither is a character the pattern allows, such a line does not qualify. Only the last line has its trailing whitespace removed, by the trim.
  - **The `length` decline reads the last `.segment` only.** A step whose id is `length` (`results.length`, or `results.length.x`) is looked up like any other id; only a path whose final `.segment` is `length` (`results.a.length`, `results.length.length`) declines.
  - In a predicate, the shortcut's value goes to the strict boolean check ([JE-24](#je-24)).
  - **How it resolves.** The step's stored result is looked up through the "result by node id" lookup ([JE-19](#je-19) gives the resolution order). The path is then applied the way Postgres's `#>` operator applies a path to `jsonb`:
    - a missing step, a lookup failure, a missing key, a type mismatch or a `null` intermediate all give **`null`**, never an error;
    - for a step whose stored result is a list of child jobs (a loop or `branchall` module), the first path element must be the numeric index, which selects that child's result; a non-numeric first element gives `null`;
    - the returned text is the database's `jsonb` text ([JE-44](#je-44)).
  - **This lookup is more lenient than JavaScript.** With `a` returning `{}`, `results.a.b.c` gives `null` here, where JavaScript would throw. `results.nope.x` gives `null` where JavaScript would throw a `TypeError`. `results.a.0` is not valid JavaScript but resolves here.
  - It reads the database even when the step is the previous one ([JE-V6](#je-v6)).
- **Evidence:** pattern and fallback `backend/windmill-jseval/src/lib.rs:73-75` (multi-line mode without CRLF mode, so `^` and `$` meet `\n` only), `:165-218` (first-match inspection `:184-197`; the decline tests the last segment of the path after the step id, `:170-178`); dispatch `backend/windmill-worker/src/js_eval.rs:74-79`; predicate wrapping `backend/windmill-worker/src/worker_flow.rs:2451-2466`; lookup `backend/windmill-queue/src/jobs.rs:4403-4461`, `:4711-4777`; endpoint `backend/windmill-common/src/client.rs:137-155`; test `backend/tests/worker.rs:5731-5778`.
- **Conformance:** [JE-9/a–l](conformance.md#je-9)
- **Revision 2026-10-28 (source re-check, design request 8):** added where a line ends and that a step id `length` does not decline. The readings windmill-lite's textual pipeline (B2a) took, lines ending at `\n` only and no decline for a step id `length`, are confirmed; nothing changes.
- **Note:** [CF-19](control-flow.md#cf-19) once drafted a whole-expression
  form of this shortcut. The oracle settled that dispute
  ([Q-CF-16](control-flow.md#q-cf-16), answered) for the line-by-line
  reading this finding gives, which [CF-V6](control-flow.md#cf-v6) and
  CF-19's route 4 now state.

## C. The QuickJS path

<a id="je-10"></a>
### JE-10 · Names are bound only if they appear in the expression text

**Scope:** core (binding rule, `flow_env` default); later (computed access, assignment) · **Confidence:** high (oracle-observed; assignment: source-traced) · **Status:** confirmed; drafted (which bindings can be assigned)

- **Rule:**
  - A context value ([JE-22](#je-22)) is defined only if its name appears **as a substring** of the trimmed text. Substring matching makes this a superset: `results` also binds `result`.
  - `flow_input` is bound only if the text contains `flow_input`. When the site has no flow input, it is bound to `null`.
  - `flow_env` is bound only if the text contains `flow_env`. It is then a plain object holding the resolved flow env ([JE-38](#je-38)), or `{}` when there is none.
  - `previous_result` is additionally bound when the text contains `error` ([JE-17](#je-17)) or one of the previous-step spellings of [JE-8](#je-8).
  - A name the text never mentions literally is unavailable: computed access such as `globalThis["fl"+"ow_input"]` gives `undefined`.
  - **Two kinds of binding.** `results` and `error` are lexical global bindings, not properties of `globalThis`. Context values, `flow_input`, `flow_env` and the helper functions *are* `globalThis` properties.
  - **Which can be assigned.** `results` is declared `const`: assigning to it (for example `results = 1`) throws a `TypeError` with the message `'results' is read-only`, so the evaluation fails with `QuickJS evaluation error: Error: 'results' is read-only`. `error` is declared with `let`, so the expression may assign to it. Context values, `flow_input`, `flow_env` and the helpers are ordinary writable global properties.
  - Values are copied in as fresh JavaScript values ([JE-16](#je-16)); mutating them affects nothing outside the evaluation.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:257-290`, `:455-492`, `:494-526` (`error` declared with `let`, `:497`), `:805-817` (`results` declared `const`, `:806`); the strict-mode assignment check on a read-only global binding, `quickjs/quickjs.c:10370-10401` and `:7337-7345` in rquickjs-sys 0.11.0.
- **Conformance:** [JE-10/a–e](conformance.md#je-10)
- **Revision 2026-10-28 (validation round 2, design request 9):** the finding did not say whether the two lexical bindings can be assigned. The reading that both are constants is half corrected: `results` is a constant, but `error` is a `let` binding and can be assigned. Added case JE-10/e (drafted, source-traced).

<a id="je-11"></a>
### JE-11 · Calls to `variable(`, `resource(` and `flow_user_state(` are rewritten textually

**Scope:** later · **Confidence:** high (oracle-observed; the count's start, the unclosed call and the panic's outcome: source-traced) · **Status:** confirmed; drafted (the 2026-10-28 additions for design request 8 and the 2026-10-29 additions for design request 10)

- **Rule:**
  - Before evaluation, every occurrence of the character sequences `variable(`, `resource(` and `flow_user_state(` is rewritten so that the call is awaited: `F(args)` becomes `(await F(args))`. The three names are rewritten in that order, each over the text the previous one produced.
  - **How the text is cut.** The text is split at every occurrence of `F(`. The part before the first occurrence is kept as it is. Each later part, the text from just after one `F(` up to the next `F(` or the end of the text, is rewritten on its own: it is prefixed with `(await F(` and gets one `)` inserted.
  - The inserted `)` goes where the call's own parentheses balance: `(` and `)` are counted from the start of the part, that is from just after the call's opening parenthesis. Parentheses inside string literals count too.
    - **When they never balance** within the part, the inserted `)` goes at the end of the part: before the next `F(` of the same name, or at the end of the text.
    - **The position is counted in characters but applied as a byte offset,** both for a call that closes and for one that never does. The count starts at the call, not at the start of the text, so only the characters between the call's opening parenthesis and the insertion point matter: text before the call, ASCII or not, shifts nothing (`"é" + variable("x")` is rewritten correctly). When that stretch is ASCII, the two agree and the rules above hold exactly. Otherwise the `)` lands early, by the number of extra UTF-8 bytes in the stretch. `variable("é")` becomes `(await variable("é)")`, with the parenthesis inside the string, which leaves a parenthesis open and fails as a `SyntaxError`.
    - **When the early offset falls inside a multi-byte character** (`variable("éé")`), the insertion panics. The rewrite runs on the blocking thread the evaluation is given, so the runtime catches the panic and the evaluation fails with an ordinary error whose text is the runtime's report of the panicked task, `task <n> panicked with message "assertion failed: self.is_char_boundary(idx)"`, in place of `QuickJS evaluation error: …`. `<n>` is the runtime's task number, which differs from run to run. The worker keeps running, the evaluation is not retried (only a timeout is, [JE-2](#je-2)), and the site handles the error as it handles any failed evaluation: an input transform fails its step before it runs ([JE-23](#je-23)), and a flow-level expression fails by its route with the prefixes and location suffixes of [FX-50](execution.md#fx-50) ([JE-35](#je-35)).
  - The rewrite is purely textual, not a JavaScript parse. It has no word boundary and no awareness of strings or comments:
    - `myvariable('x')` becomes `my(await variable('x'))`, which fails because `my` is not defined;
    - the text `"variable(x)"` inside a string literal is rewritten as well.
  - A rewritten call inside a non-`async` nested function is a syntax error (as in [JE-12](#je-12)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:81-115` (`:82-89`: the split at `F(` and the per-part rewrite; `:98-115`: the count starts at the part's first character, at level 1, and a character count is used as the insertion offset; the standard library's string insert takes a byte offset and asserts that it is on a character boundary), `:446-449` (the rewrite runs inside the evaluation), `:304-328` (the evaluation runs as a blocking task under a timeout; a panicked task comes back as the runtime's join error, which becomes the evaluation's error); `backend/windmill-worker/src/js_eval.rs:81-105` (only an error containing `took too long` is retried); the release profile sets no `panic = "abort"` (`backend/Cargo.toml:107-110`), so a panic unwinds; tokio 1.46.1 (`backend/Cargo.lock:13016-13019`) `src/runtime/task/error.rs:132-150` (the join error's text for a panic, the message in debug quotes, `:180-192`) and `src/runtime/task/id.rs:50-54` (the task number); tests `:1103-1116`.
- **Conformance:** [JE-11/a–e](conformance.md#je-11)
- **Revision 2026-10-28 (source re-check, design request 8):** added where an unbalanced call ends, and the character-versus-byte offset. The reading windmill-lite's textual pipeline (B2a) took, that an unbalanced call ends at the end of the text, is confirmed for ASCII text. For non-ASCII text before the insertion point it is extended, not reversed: the parenthesis lands early or the rewrite panics, as above. Not yet recorded ([Q-JE-13](#q-je-13)).
- **Revision 2026-10-29 (source re-check, design request 10):** three readings milestone FU3 left open are settled from the source. (1) **The count starts at the call**, just after its opening parenthesis, not at the start of the text: the text is cut at each `F(` and each part is counted on its own. FU3's reading, the start of the text, is **corrected**; the two differ only when non-ASCII text comes before the call, which then shifts nothing. (2) **A call that never closes** gets its `)` by the same character-count-as-byte-offset rule, at the end of its part: FU3's reading (yes) is confirmed, and the part ends at the next `F(` of the same name, not always at the end of the text. (3) **The panic** neither takes down the worker nor leaves the job to a monitor: the runtime catches it, and the evaluation fails at once with the text `task <n> panicked with message "assertion failed: self.is_char_boundary(idx)"`, routed like any evaluation error of its site. windmill-lite's reading, failing the evaluation with its own text, agrees on the outcome and differs only in the text. Added cases JE-11/b–e (source-derived, [Q-JE-13](#q-je-13)).

<a id="je-12"></a>
### JE-12 · `results.<id>` is rewritten textually into an awaited lookup

**Scope:** core (single-quoted and computed keys, bare `results`); later (other consequences) · **Confidence:** high (oracle-observed) · **Status:** confirmed; drafted (the 2026-10-28 additions for design request 8)

- **Rule:** after [JE-11](#je-11), every match of this pattern is wrapped as `(await <match>)`: the characters `results`, optionally followed by `?`, followed by either `.` and one or more `[A-Za-z0-9_]`, or by `["…"]` (double quotes only, matched lazily). Observable consequences:
  1. **No word boundary.** `flow_input.results.x` becomes `flow_input.(await results.x)`, a syntax error. `my_results.x` becomes `my_(await results.x)`.
  2. **Strings and comments are rewritten too.** The expression `"see results.a"` evaluates to the string `"see (await results.a)"`.
  3. **Single quotes and computed keys are not rewritten,** so `results['a']` and `results[k]` stay Promises:
     - used alone, the async wrapper adopts the Promise, so the value comes out right;
     - `results['a'].v` reads `.v` off a Promise and gives `null`;
     - `{y: results['a']}` gives `{"y":{}}`.
  4. **A nested non-async function breaks.** If `results.x` appears inside a non-`async` function or arrow, the inserted `await` is a compile-time `SyntaxError` and the whole expression fails; quickjs-ng reads `await` there as a plain name, so the diagnostic is `Unexpected identifier 'results'` (oracle-observed, JE-12/d). `results.x.map(a => a.y)` is fine, because `results` is read outside the callback; `arr.map(a => results.x[a])` fails.
  5. **Bare `results`** (not followed by `.` or `["`) is the lookup proxy itself. `JSON.stringify` of it is `{}`, so `x = results` gives `{}`.
  6. **A `["…"]` read never spans lines.** The lazy match inside the brackets does not cross a newline, so it ends at the first `"]` on the same line. `results["a` followed by a newline and `"]` is not rewritten, and neither is any other bracket read whose closing `"]` is on a later line.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:59-68` (`:66`: the bracket alternative's `.` is not in dot-matches-newline mode), `:92-95`, `:555-570`, `:805-817`; test `:1119-1132`.
- **Conformance:** [JE-12/a–e](conformance.md#je-12)
- **Revision 2026-10-28 (source re-check, design request 8):** added consequence 6. The reading windmill-lite's textual pipeline (B2a) took, that a `["…"]` read does not span lines, is confirmed; nothing changes.

<a id="je-13"></a>
### JE-13 · Expression mode or statement mode

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed; drafted (the 2026-10-28 additions for design request 8)

- **Rule:** after the rewrites, the code is placed in an `async` function body. **Statement mode** is chosen when any of these holds:
  - the text starts with `return ` or `return;`, or is exactly `return`. The space is a literal space: `return` followed by a newline or a tab matches none of these, so without a `;` such a text runs in expression mode as `return return…;` and fails as a `SyntaxError`;
  - the text starts with one of `const `, `let `, `var `, `if `, `if(`, `for `, `for(`, `while `, `while(`, `switch `, `switch(`, `try `, `try{`, `throw `, `function `, `class `, `async `, `await `;
  - the text contains a `;` outside single, double and backtick quotes. The scan reads one character at a time. A quote character opens a quote of its own kind, or closes the quote of its kind that is open, but only when no quote of another kind is open and the character just before it is not a backslash. So a quote of another kind inside a string is plain text. The backslash test looks at the raw previous character, so a quote after an escaped backslash (`"\\"`) still counts as escaped and the string stays open. The scan knows nothing about regex literals, comments or `${}` nesting.

  The wrapper is an `async function` expression called at once, not an async arrow, so the text sees that function's own (empty) `arguments` object.

  Then:
  - **Statement mode:** the text runs as the function body. The value is whatever it `return`s; with no `return` it is `undefined`, which becomes `null` ([JE-15](#je-15)).
  - **Expression mode:** the text is used as `return <text>;`. A leading `{` is therefore an **object literal**, not a block.
  - An empty text yields `null` ([JE-V4](#je-v4)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:554-559` (`:556`, `:558`: the `async function` wrapper), `:864-937` (`:872` the `return` forms; `:911-937` the quote scan, which tests the raw previous character at `:919`, `:922`, `:925`); tests `:1073-1100`, `:1466-1490`.
- **Conformance:** [JE-13/a–f](conformance.md#je-13)
- **Revision 2026-10-28 (source re-check, design request 8):** added the `return`-newline case, the exact quote rule and the wrapper's form. windmill-lite's textual pipeline (B2a) took three readings here, and all are confirmed: `return` followed by a newline does not select statement mode; a quote opens or closes its own kind unless a backslash precedes it, so another kind of quote inside a string is text; and the wrapper is a function, not an arrow. Nothing changes. The detail that an escaped backslash still escapes the next quote is new; it follows from the same rule.

<a id="je-14"></a>
### JE-14 · A trailing `//` comment breaks a single-line direct expression

**Scope:** later · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - In both modes the code is wrapped on a single line, with the wrapper's own closing text after the user's text. The wrapper is an `async function` expression ([JE-13](#je-13)). In a one-line expression, a trailing `//` line comment comments out that closing text, and the expression fails with a `SyntaxError`.
  - Direct (non-predicate) expressions are trimmed before wrapping ([JE-5](#je-5)), so a trailing newline does not help them.
  - A `//` comment followed by more lines is fine, and `/* */` comments are fine.
  - Predicates differ: they are wrapped before trimming, so a trailing newline after the comment rescues them ([JE-24](#je-24)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:555-559`.
- **Conformance:** [JE-14/a, JE-14/b](conformance.md#je-14)

<a id="je-15"></a>
### JE-15 · The returned value becomes JSON

**Scope:** core; later (a replaced `JSON.stringify`) · **Confidence:** high (oracle-observed; circular structures: medium; a replaced `JSON.stringify`: source-traced) · **Status:** confirmed; drafted (a replaced `JSON.stringify`)

- **Rule:**
  - The function's resolved value is serialised with `JSON.stringify(value ?? null)`, and the resulting text is the evaluation's result:
    - a top-level `undefined` becomes `null`;
    - a value that `JSON.stringify` turns into `undefined` (a function, a symbol) becomes `null`;
    - `NaN` and `±Infinity` become `null`;
    - a `Date` becomes its ISO string;
    - `Map`, `Set`, `RegExp` and nested Promises become `{}`;
    - `undefined` properties are dropped, and `undefined` array items become `null`.
  - A `BigInt` anywhere, or a circular structure, makes `JSON.stringify` throw, and the evaluation fails. The quickjs-ng text for the `BigInt` case is `BigInt are forbidden in JSON.stringify`.
  - A Promise returned at top level is awaited, because the `async` function adopts it.
  - The evaluator returns this JSON text as is (O1), with number text from QuickJS's `Number#toString`. It then goes into the job's recorded arguments (O2), may be resolved (O3, [JE-41](#je-41)) and is parsed by the script's runtime (O4).
  - **A replaced `JSON.stringify`.** The serialising call looks `JSON.stringify` up when it runs, so an expression that replaces it supplies the result text itself. That call is made inside a `.then` callback, and the evaluator awaits the promise the callback produces before it reads anything back. In order:
    1. **What the replacement returns is adopted like any promise reaction's value.** A promise or any other thenable it returns is followed, so `x = (JSON.stringify = () => Promise.resolve("7"), 1)` ends with the string `"7"`, exactly as a synchronous `() => "7"` does.
    2. **A rejection fails the evaluation.** A replacement that throws, or returns a promise that rejects (`() => Promise.reject(new Error("serializer failed"))`), fails the evaluation like any uncaught throw of that value ([JE-21](#je-21)).
    3. **The fulfilled value must convert to a host string.** It is read as a JavaScript string, with no coercion, and its bytes must be valid UTF-8. A value that is not a string (`() => 5`), and a string holding an unpaired surrogate (`() => "\ud800"`, which the real `JSON.stringify` would have escaped, [JE-52](#je-52)), both fail that conversion. The result text is then `null`, and the evaluation does not fail.
    4. **A string that converts is kept as the result text without any check** that it is JSON: `() => "7"` gives `7`, and `() => "not json"` gives the text `not json`.

    Text that is not JSON is passed on as if it were. What fails then depends on the site: a predicate's boolean check reports it (`Expected a boolean value, found: …`, [JE-24](#je-24)), a site that parses a number or an array reports a parse error, and in an input transform the step's arguments hold text that is not JSON when they are stored. **Storing them fails** (source-traced 2026-10-29): the job's argument object is written member by member with each member's text copied in unchanged, so `x = (JSON.stringify = () => "hello", 1)` gives the argument text `{"x":hello}`, which PostgreSQL's `jsonb` input refuses with `invalid input syntax for type json`. The step's job is never created: the push fails as a database error, which is [FX-48](execution.md#fx-48) case 2's route ([FX-51](execution.md#fx-51) case 2), routed by [FX-15](execution.md#fx-15) by phase: at the root's first pickup the module fails with `Anyhow` and `Error: SqlErr: error returned from database: invalid input syntax for type json<loc><loc>`; at a later transition the flow fails with the chaining `InternalError` `SqlErr: error returned from database: invalid input syntax for type json<loc>` ([FX-50](execution.md#fx-50)). Text that happens to be JSON (`() => "7"`) is stored like any other value.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:554-570` (the serialising call inside `.then`, `:556`, `:558`; the resulting promise awaited and a rejection mapped to an evaluation error, `:562-565`; a fulfilled value that does not convert read as `null`, `:567-568`; the text kept without a check, `:570`), `backend/windmill-common/src/utils.rs:1051-1053` (the unchecked conversion of text into a raw JSON value); rquickjs 0.11.0 `core/src/value/convert/from.rs:39-43` (a host string is read only from a JavaScript string, then converted), `core/src/value/string.rs:11-25` (the engine's bytes must be valid UTF-8); quickjs-ng [quickjs.c @ be664ca](https://github.com/quickjs-ng/quickjs/blob/be664ca1ccd86cbadf8df7348b8e15e77a32a1ba/quickjs.c) lines 4318-4338 (inside the string-to-bytes conversion that starts at line 4238: an unpaired surrogate is written as its own code point, which is not UTF-8), 50594-50634 (a reaction's return value is passed to the promise's resolver), 50806-50859 (a callable `then` is followed rather than fulfilling with the object), 47499 (the `BigInt` message); tests `:1314-1322`, `:1649-1694`, `:2725-2745`.
- **Conformance:** [JE-15/a–k](conformance.md#je-15)
- **Revision 2026-10-28 (validation round 2, design request 9):** added what happens when the expression replaces `JSON.stringify`. The reading that the evaluation then fails with its own text is corrected: Windmill keeps a returned string unchecked and reads any other return as `null`, and only a later site check, or the arguments' storage, can fail. Added case JE-15/j and question Q-JE-14 (drafted, source-traced).
- **Revision 2026-10-28 (validation round 3):** the replaced-`JSON.stringify` rule said every string a replacement returns passes through and every other return reads as `null`. That classified the replacement's immediate return value, but what counts is the value the awaited promise fulfils with, once it is converted to a host string. The rule now gives the four steps: a returned promise or thenable is adopted; a rejection fails the evaluation; a value that is not a string, or a string with an unpaired surrogate, reads as `null`; and only a string that converts is kept unchecked. JE-15/j gains three runs for those boundaries (drafted, source-traced; not recorded, [Q-JE-14](#q-je-14)).
- **Revision 2026-10-29 (SC3, source re-check, Q-JE-14):** the storage outcome of non-JSON argument text is traced. The argument object is serialised by writing each member's held text as it is (`backend/windmill-queue/src/jobs.rs:4903-4925`) and bound as the job row's `jsonb` arguments (`:6483`), with no check before; the step push that does this returns its database error to the flow's transition (`backend/windmill-worker/src/worker_flow.rs:4403-4436`), which routes it like FX-48 case 2. New case JE-15/k (drafted, source-traced).

<a id="je-16"></a>
### JE-16 · Context values arrive as converted JSON

**Scope:** core (key order, precision); later (negative zero, `__proto__` keys) · **Confidence:** high (oracle-observed; the evaluator-level negative-zero token: medium, not recordable; `__proto__` keys: source-traced) · **Status:** confirmed; drafted (`__proto__` keys)

- **Rule:**
  - Every context value is parsed from its JSON text and rebuilt as a JavaScript value. Strings, booleans, `null`, arrays and objects map one to one.
  - **`__proto__` keys** (source-traced). Objects are rebuilt by the host, key by key, with ordinary property assignment, not by `JSON.parse`. A key named `__proto__` therefore goes through the inherited `__proto__` setter instead of becoming an own property: when its value is an object, that object becomes the rebuilt object's **prototype**; when it is `null`, the prototype is `null`; when it is a string, number or boolean, the key is dropped. In each case the rebuilt object has no own `__proto__` key: from the context value `{"__proto__":{"x":1}}`, `Object.keys(v)` is `[]`, `v.x` is `1`, and `JSON.stringify(v)` is `{}`. (`JSON.parse` of the same text would make an own property.)
  - **Which values are rebuilt this way** (source-traced). The context values bound as globals (`previous_result`, `result`, `params`, `resume`, `resumes`, `approvers`, `all_iters`, `started_at`, `flow_job_id`), `flow_input`, and `flow_env` with each of its members. **Values fetched while the expression runs are not:** the host hands them over as JSON text, and the program turns that text into a value with `JSON.parse`, so a `__proto__` key in them is an own property. That covers:
    - a `results.<id>` read that fetches a stored result ([JE-18](#je-18) rules 2 and 3), for a single job and for the array of a loop's or `branchall`'s child results;
    - `resource(path)` and `flow_user_state(key)` ([JE-20](#je-20));
    - `variable(path)`, which returns a string and parses nothing.

    A `results.<id>` read of the **previous** step that the proxy serves from memory ([JE-18](#je-18) rule 1) returns the `previous_result` global itself, so it is the rebuilt value. The same stored object can therefore read differently: with `a` returning `{"__proto__":{"x":1}}` (an own key, as `JSON.parse` makes it), `results.a` directly after `a` has no own key and inherits `x`, while two steps later it has the own key `__proto__` and no `x`. The `JSON.parse` these reads use is looked up when it runs, so an expression that replaces `JSON.parse` before such a read gets its replacement's return value, much as [JE-15](#je-15) describes for `JSON.stringify`.
  - **Key order from raw JSON.** A value converted from raw JSON text keeps that text's key order. As for any JavaScript object, integer-like keys enumerate first, in ascending order.
  - **Unstable outer order.** The outer keys of `flow_input` have **no stable order**: the flow input is held in a hash map with randomised iteration before it is serialised into the context. The same holds for `previous_result` at module 0 (which is that input) and for the top-level argument object a script receives. Nested values keep their stored order.
  - **Numbers.** Integers that fit in 32 bits become integers, every other number a double. In JavaScript terms numbers behave like `JSON.parse`: integers beyond 2^53 lose precision.
  - **Negative zero.** The JSON token `-0` parses as floating-point negative zero and **stays `-0`** in JavaScript.
    - A producer's raw result travels in memory to the next flow transition ([JE-44](#je-44)). A `-0` or `-0.0` token in it reaches the next step's expressions as negative zero, and any consequence computed from it is recorded normally (visible at O2 and O5).
    - A `jsonb` round trip stores the number as `0` (or `0.0`), so a read through the database sees no negative zero.
    - Bun's `JSON.stringify(-0)` emits `0`. A fixture producer must emit a negative-zero token, as Python's `json` output `-0.0` does ([languages](languages.md)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:455-462`, `:825-862`; `serde_json` with `preserve_order` `backend/Cargo.toml:435`; `backend/windmill-worker/src/common.rs:133-142`; raw forwarding `backend/windmill-worker/src/result_processor.rs:1490-1531`, `backend/windmill-worker/src/worker_flow.rs:1477-1488`, `:2086-2099`; the pinned serde_json 1.0.151 (`backend/Cargo.lock:11151`), [`src/de.rs:509-523`](https://github.com/serde-rs/json/blob/v1.0.151/src/de.rs#L509); Python result serialisation `backend/windmill-worker/src/python_executor.rs:3287-3303`, `:984-987`, `:958-964`. The rebuild of objects by assignment: `backend/windmill-jseval/src/lib.rs:854-860`; rquickjs 0.11.0 `core/src/value/object.rs:53-68` (a set is an ordinary property assignment); quickjs-ng's `__proto__` accessor on `Object.prototype`, `quickjs/quickjs.c:39023` in rquickjs-sys 0.11.0, whose setter (`:38861-38872`) changes the prototype for an object or `null` and ignores any other value. Which values are rebuilt: the globals and `flow_input` `backend/windmill-jseval/src/lib.rs:455-470`, `flow_env` `:472-484`; the fetched values handed over as text and parsed in the program: stored results `:683-751` (the fetched value written out as text at `:745`) and `:772-781` (`JSON.parse` at `:776`), `flow_user_state` `:754-767`, `:783-785`, `resource` `:599-614`, `:627-633`, `variable` `:585-596`, `:619-625`; the previous step served from memory `:811-813`.
- **Conformance:** [JE-16/a–h](conformance.md#je-16)
- **Note:** no fixture may assert the order of `Object.keys(flow_input)` or of a script's top-level argument keys.
- **Revision 2026-10-28 (validation round 2, design request 9):** the finding did not say how a `__proto__` key is rebuilt. The reading that it becomes an own property, as `JSON.parse` makes it, is corrected: Windmill rebuilds objects by assignment, so the key sets the prototype (an object or `null`) or is dropped (any other value). Added case JE-16/g (drafted, source-traced).
- **Revision 2026-10-29 (source re-check, design request 10):** added which values are rebuilt by assignment. Milestone FU3's reading, that `results` reads and the helpers' values are not rebuilt but come from `JSON.parse`, is **confirmed** for every read that fetches a value, with one exception it did not state: a `results` read of the previous step served from memory is the rebuilt `previous_result`. Added case JE-16/h (drafted, source-traced).

<a id="je-17"></a>
### JE-17 · The `error` helper variable

**Scope:** core · **Confidence:** high (oracle-observed; the falsy fallbacks: source-traced) · **Status:** confirmed; drafted (the falsy fallbacks of the entry text)

- **Rule:** if the trimmed text contains the substring `error` anywhere (`flow_input.error_count` counts), a global `error` is defined before evaluation, derived from `previous_result` by this decision list:
  1. If `previous_result.error` is truthy, `error` is that value. This includes a string-valued error such as the one [JE-26](#je-26) produces.
  2. Otherwise, if `previous_result` is an array, collect the items that are objects with an own or inherited `error` key:
     - exactly one: `error` is that item's `.error`;
     - more than one: `error = {name: "MultipleErrors", message: <joined>, errors: previous_result}`. `<joined>` joins one entry per collected item with `; `. Each entry is `[<tag>] <text>`, where `<tag>` is the item's `error.step_id`, or else the item's position *within the collected list*, and `<text>` is `error.message`, or else `error.name`. Both "or else" tests are **falsy** tests, not nullish ones: a `step_id` of `""` or `0` gives the position, and a `message` of `""` gives the name. When both `message` and `name` are falsy, the text is that last value converted to a string (`[0] undefined` for an item whose `error` is `{}`);
     - none: `error = {name: "MultipleErrors", message: "Could not parse errors", errors: previous_result}`.
  3. Otherwise, if `previous_result` is truthy: `error = {name: "UnknownError", message: "Could not parse the error", error: previous_result}`.
  4. Otherwise: `error = {name: "UnknownError", message: "No error found"}`.

  The helper is meant for failure-module transforms and `retry_if`, but it is installed at every site whose text contains `error`.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:273-279`, `:494-526` (the entry text uses logical-or on the step id and on the message, `:506`).
- **Conformance:** [JE-17/a–d](conformance.md#je-17)
- **Revision 2026-10-28 (validation round 2, design request 9):** "or else" was not defined. It is a falsy test (`||`), not a nullish one, so the reading that an empty `step_id` or `message` is kept is corrected: an empty one falls back to the position or the name. Added case JE-17/d.

<a id="je-18"></a>
### JE-18 · How a `results.<id>` read resolves on the QuickJS path

**Scope:** core · **Confidence:** high (oracle-observed); high (repeated ids, source-traced) · **Status:** confirmed; drafted (repeated ids)

- **Rule:** `results` is a lazy proxy. Reading a string key `k` returns a Promise, which the rewrite ([JE-12](#je-12)) awaits; symbol keys give `undefined`. The decision list for `k`:
  1. **The previous step.** If `k` is the previous id ([JE-8](#je-8)) and `previous_result` is bound, return `previous_result` from memory, without any fetch. The textual binding of [JE-10](#je-10) always holds when the rewrite produced the read.
  2. **A module of the current flow with a job.** If `k` is a module of the *current* (sub)flow whose status already records a job (any started, finished or failed module):
     - a single job: fetch that job's completed result;
     - a list of child jobs (a loop or `branchall`): fetch every child's result concurrently and return them as an array in child order;
     - **if any fetch fails, the read throws** `Error("Failed to fetch result for step '<k>': <cause>")` (with `results for step` for lists);
     - **a repeated id** finds the **last** module in the flow's list whose status entry has a job; a later module of the id that has not started is skipped (see the 2026-10-29 revision).
  3. **Anything else:** use the API's result-by-node-id lookup ([JE-19](#je-19)). **Any failure there yields `null`.**
- **Evidence:** proxy `backend/windmill-jseval/src/lib.rs:669-823` (fetch errors versus fallback `:695-750`, `:773-782`); context `backend/windmill-worker/src/worker_flow.rs:6155-6171`; modules with a job `backend/windmill-types/src/flow_status.rs:401-404`, `:454-458`; endpoints `backend/windmill-common/src/client.rs:118-155`.
- **Conformance:** [JE-18/a–e](conformance.md#je-18)
- **Revision 2026-10-29 (source re-check, chain N1a):** which status entry
  step 2 finds when the flow repeats a module id, which deploy allows
  ([OF-2](openflow.md#of-2)). The step map is collected from the current
  flow's status entries in list order, keeping only entries with a job, and
  a later entry replaces an earlier one with the same id
  (`backend/windmill-worker/src/worker_flow.rs:6155-6171`). So the read
  finds the last module of that id that has started (in a sequential flow,
  the most recently reached one), not the first. The other lookups differ:
  step 1 answers from memory whichever module carries the previous id;
  step 3 and the line-anchored direct lookup ([JE-9](#je-9)) read
  [JE-19](#je-19)'s map, which holds the module of that id that completed
  most recently; the restart and completed-flow fallbacks stop at the first
  module of the id in list order. [OF-2](openflow.md#of-2) tabulates every
  lookup by id. New case JE-18/e. Milestone N1a read step 2 as the first
  entry.
- **Note:** when `previous_result` is bound for a computed read of the
  previous id (for example `results[flow_input.target]`) is settled in
  [FX-10](execution.md#fx-10) route 3 and [FX-V6](execution.md#fx-v6)
  ([Q-FX-15](execution.md#q-fx-15), answered): any binding trigger of
  [JE-10](#je-10) makes the read come from memory.

<a id="je-19"></a>
### JE-19 · Which `results.<id>` a scope can see

**Scope:** core (scope visibility); later (subflow-by-path, restart and completed-flow fallbacks) · **Confidence:** high (oracle-observed; the restarted-run fallback: high, source-traced 2026-10-29) · **Status:** confirmed; drafted (which reads of a restarted run reach the fallback)

- **Rule:** resolution through the API ([JE-9](#je-9) and step 3 of [JE-18](#je-18)) uses a per-scope map from node id to job. Each scope keeps its own map; every time a module finishes, its entry is written, overwriting the previous one.
  - **Scopes.** A top-level flow is its own scope. A subflow called by path (`flow` module) and every iteration of a parallel `forloopflow` each start a new scope. Sequential loops, while loops, `branchone` and `branchall` bodies share their enclosing scope.
  - **Lookup.** The current scope is searched first. If the id is absent and the scope has a parent job, the parent's scope is searched, and so on upwards.
  - **Restart fallback.** On a miss in the running scope chain, the lookup checks whether the running flow's root was **restarted** from an earlier run. If so, it searches that original, completed run top-down for a module with the id. This can happen **while the restarted run is still active**. If the root has no restart information, the miss ends there and gives `null`.
    - **Which reads reach it** (source-traced). A restarted run starts with a new status in which every module before the restart point is copied from the original run, `Success` with the original job, and the rest wait; its per-scope map starts **empty** and gains an entry only when a module completes in the new run. So a read that [JE-18](#je-18) step 2 answers (a module of the current flow) is served by the copied status entry and never needs the fallback, while the line-anchored direct lookup ([JE-9](#je-9)) and step 3 miss the map for every module that has not completed again and reach the original run. Both give the original module's result for a top-level module before the restart point; a module the copied status does not hold, such as a step inside a sequential loop's body, is found only through the fallback.
  - **Completed-flow fallback.** Only when there is no running status row at all (normally because the flow has completed: the status row is deleted together with the queue row) does the lookup search the given flow itself as a completed flow, top-down through its completed subflows. This is the normal lifecycle explanation, not a guarantee that any missing job exists in completed storage; the completed lookup has its normal not-found behaviour.
  - **Consequences:**

    | Reference | Value |
    |---|---|
    | `results.s` to a sibling earlier in the same body | That step's result (from [JE-18](#je-18) step 2) |
    | `results.o` from a loop or branch body to a step before the loop | `o`'s result |
    | `results.i` after a **sequential** loop with a non-collapsed body, to a step inside the body | The body step's result from the **last iteration that ran** |
    | `results.i` after a sequential loop with a **collapsed** single-step body ([JE-36](#je-36)) | `null`: only the loop id is recorded |
    | `results.i` after a **parallel** loop, to a step inside its body | `null` (the step lives in the iterations' own scopes) |
    | `results.i` from a caller, to a step inside a subflow called by path | `null` |
    | From inside a subflow called by path, to the **caller's** step `o` | `o`'s result (the lookup walks up to the parent), unless the subflow has its own `o` |
    | `results.L` to a loop or `branchall` module itself | An array of the iteration or branch results, in order |
    | `results.B` to a `branchone` module | The chosen branch's result |

  - Fixtures use distinct ids, so that no ancestor's step shadows the one under test.
- **Evidence:** scope roots `backend/windmill-worker/src/worker_flow.rs:4318-4328`; map writes `:1459-1474`; lookup walk `backend/windmill-queue/src/jobs.rs:4559-4611`, `:4613-4709`; restart and completed fallbacks `:4403-4461` (restart branch `:4423-4448`, missing-row branch `:4449-4458`); status-row lifecycle `backend/migrations/20250201124345_v2_job_status.up.sql:3`.
- **Conformance:** [JE-19/a–f](conformance.md#je-19)
- **Note:** which reads in a restarted run actually reach the restart fallback, rather than being served by module statuses copied into the restarted run ([JE-18](#je-18) step 2), was [Q-JE-8](#q-je-8); the source now answers it (the sub-bullet above). The subflow-reads-caller row is [Q-JE-10](#q-je-10).
- **Revision 2026-10-29 (SC3, source re-check, Q-JE-8):** added which reads of a restarted run reach the restart fallback. The restart builds the new status from the original one, copying the successful modules before the restart point with their jobs (`backend/windmill-queue/src/jobs.rs:5604-5646` and `:5989-6035`, the copy at `:7125-7166`); the per-scope map of completed modules is written only at completions in the running flow (`backend/windmill-worker/src/worker_flow.rs:1459-1474`) and nothing seeds it at a restart, so the direct lookup misses and falls back (`backend/windmill-queue/src/jobs.rs:4403-4461`), while the engine's step map reads the copied entries (`backend/windmill-worker/src/worker_flow.rs:6155-6171`). New case JE-19/f (later, drafted).

<a id="je-20"></a>
### JE-20 · The `variable()`, `resource()` and `flow_user_state()` helpers

**Scope:** core (`variable`, `resource`); later (`flow_user_state`) · **Confidence:** high (oracle-observed); high (source-traced: the read contract and error texts of `resource()`) · **Status:** confirmed; drafted (the read contract and error texts of `resource()`)

- **Rule:**
  - **`variable(path)`** resolves to the variable's value as a **string**, with secrets decrypted.
  - **`resource(path)`** resolves to the resource's JSON value, with its nested `$var:`/`$res:` references already interpolated.
    - **The read contract** (drafted). The helper reads the resource the way the clients' `getResource` does: the interpolated read **without a job id** and without the cache, contract (b) of [WA-40](client-api.md#wa-40), interpolated by the server as [WA-12](client-api.md#wa-12) says. It is not the leaf step's `$res:` resolution of [JE-42](#je-42) (contract (c)), which sends the step's job id. So:
      - `path` is put into the request as written. No `$res:` or `res://` prefix is stripped and the top-level path is not checked for a `/`: `resource("$res:u/a/b")` asks for a resource literally named `$res:u/a/b` and gets the not-found text below.
      - The value may be any JSON value, not only an object; a resource stored without a value gives `null`.
      - It is walked from depth 0: `$var:`, `$jsonvar:` and nested `$res:` strings are replaced wherever the walk reaches, the top-level value included (a resource whose whole value is the string `"$res:u/a/b"` yields `u/a/b`'s value). Arrays deeper than 2 or longer than 1000 are not walked.
      - **`$WM_*` strings stay literal,** because no job id is sent. The value then normally reaches the step's arguments without a marker, so the leaf step's resolution ([JE-42](#je-42) rule 1) does not touch them either, and the script sees `"$WM_JOB_ID"` as text (only when the same argument still holds a marker, for example inside an array too deep to walk, does rule 2's name lookup resolve them at O3). A static `"$res:<path>"` argument resolves the same strings to the step job's values.
      - The value comes back as JSON text that the program parses ([JE-16](#je-16)).
      - `CUSTOM_INSTANCE_DB/<db>` is the special path of [WA-12](client-api.md#wa-12), answered only for an instance super admin.
  - **`flow_user_state(key)`** resolves to the value stored under `key` for the root flow job.
  - **Failure:** each helper throws `Error(<API error text>)`.
    - `<API error text>` is the response body of the failed request, verbatim; the HTTP status is not shown. With the evaluator's frame, a missing resource reads `QuickJS evaluation error: Error: Not found: Resource <path> not found` followed by the line `    at resource (eval_script:15:50)` and a newline (drafted; the recorded `variable()` form, [JE-20/b](conformance.md#je-20), has `    at variable (eval_script:7:50)`: the helpers' prelude is one script, and `resource`'s `throw` sits eight lines below `variable`'s, at the same column).
    - The bodies a `resource()` call can carry are [WA-42](client-api.md#wa-42)'s: `Not found: Resource <path> not found` for the resource itself; `Not found: Variable <p> not found` for a nested `$var:` or `$jsonvar:`; the located `Not found: Resource not found at name <p> (resources.rs:846)` for a nested `$res:`; `Internal: Invalid resource path: <p> @<loc>` for a nested `$res:` with no `/`; `Internal: Failed to parse $jsonvar value as JSON: <message> @<loc>` ([JE-42](#je-42) gives the message's position rule); the depth message of [WA-12](client-api.md#wa-12); a secret that cannot be decrypted ([WA-11](client-api.md#wa-11)); and, for a resource the flow's user may not read, `Not authorized: Resource exists but you don't have access to it:` followed by the permissions and a debug dump of the caller, which no fixture can reproduce (compare by prefix).
  - **Availability:** the helpers need an authenticated client, and `flow_user_state` also needs a results context.
    - Without a client, `variable` and `resource` reject with `variable() is not available without an authenticated client` (and the same wording with `resource()`).
    - Without a results context, `flow_user_state` rejects with `flow_user_state() is not available in this context`. When there is no client either, it rejects with the `...without an authenticated client` wording instead.
  - Only the suspend site `user_groups_required` lacks a client ([JE-34](#je-34)). `sleep` and flow `skip_expr` have a client but no results context.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:541-552`, `:575-667`, `:753-803`; endpoints `backend/windmill-common/src/client.rs:87-116`, `:157-171`. The `resource()` contract: `backend/windmill-jseval/src/lib.rs:528-534` (the helpers exist only with a client), `:597-614` (the interpolated read with no job id; a failure is passed on as the response text behind an internal marker), `:616-634` (the prelude script: `variable`'s `throw` on its line 7, `resource`'s on its line 15, `JSON.parse` of the value); `backend/windmill-common/src/client.rs:95-116` (the request: the path inserted as given, `job_id` only when one is passed; any other status than 200 becomes the body text); `backend/windmill-store/src/resources.rs:653-682` (the route; the job id and the cache flag are optional query parameters), `:684-760` (the read).
- **Conformance:** [JE-20/a, JE-20/b, JE-20/c, JE-20/d](conformance.md#je-20)
- **Revision 2026-10-29 (SC3, source re-check, chain PF2):** which read contract `resource()` uses. Milestone PF2 read it as contract (b) without a job id; the source confirms that reading. Added the contract's consequences (literal `$WM_*`, any JSON value, the path used as written) and the error texts, with new cases JE-20/c and JE-20/d. Not yet recorded ([Q-JE-17](#q-je-17)).

<a id="je-21"></a>
### JE-21 · The evaluator's own error text

**Scope:** core (prefix structure); later (exact engine text) · **Confidence:** high (oracle-observed); the display of a thrown non-Error value: high (source-traced, [Q-JE-1](#q-je-1)) · **Status:** confirmed; drafted (the non-Error display)

- **Rule:** a failing evaluation produces `QuickJS evaluation error: <caught>`, where `<caught>` takes one of these forms:
  - **A thrown `Error` object of any subclass:** `Error: <message>`, followed, when the engine has a stack, by a newline and the stack text. The subclass name (`TypeError`, `ReferenceError`, `SyntaxError`) is **not** printed: it is always `Error:`. Engine stack frames name the script `eval_script`.
  - **A thrown non-Error value** (anything but an object of the engine's `Error` class): `Exception generated by quickjs: <form>`, followed by a newline. `<form>` is rquickjs's debug form of the value: its type name, then the payload in the host language's debug notation. It is **not** the value's string form:

    | Thrown | `<form>` |
    |---|---|
    | `"s"` (a string) | `String(Ok("s"))`: the payload is the result of reading the string, quoted and escaped in debug notation |
    | `1` (an integer that fits in 32 bits) | `Int(1)` |
    | `1.5`, `1e300`, `NaN` (other numbers) | `Float(1.5)`, `Float(1e300)`, `Float(NaN)` |
    | `true` | `Bool(true)` |
    | `10n` | `BigInt"BigInt"` |
    | `{}`, `[1]`, a function, a class, a Promise, a Proxy, a Symbol, or an object that only looks like an error (`{message: "m"}`) | `Object(0x…)`, `Array(0x…)`, `Function(0x…)`, `Constructor(0x…)`, `Promise(0x…)`, `Proxy(0x…)`, `Symbol(0x…)`: the engine's memory address of the value, which differs between runs, so the text is not reproducible |
  - **A `null` or `undefined` throw:** the out-of-memory message of [JE-3](#je-3).

  Engine messages on quickjs-ng revision `be664ca`: `<name> is not defined` for an unknown global, `cannot read property '<p>' of null` and `cannot read property '<p>' of undefined` for a property read on `null`/`undefined`, `BigInt are forbidden in JSON.stringify`, `not a function` for a call of a non-callable value, `Unexpected end of input`, `unexpected token in expression: '<token>'` and `Unexpected identifier '<name>'` for syntax errors. Each site then wraps this text ([JE-23](#je-23) to [JE-35](#je-35)).

  **The stack text** (oracle-observed). After the message comes a newline, then one line per frame, each `    at ` (four spaces) followed by the frame, and a final newline. At most the ten innermost frames are listed: quickjs-ng's `Error.stackTraceLimit` starts at 10 in every context, and nothing in the evaluator changes it (a deep recursion shows exactly ten lines). A runtime error inside the wrapped code shows the wrapper's frames, `at <anonymous> (eval_script:1:<col>)` for the `async` function and `at <eval> (eval_script:1:<col>)` for the script, with the user's own named functions and native built-ins in between (`at f (eval_script:1:68)`, `at stringify (native)`). A syntax error shows a single frame without a name, `at eval_script:<line>:<col>`. Line and column numbers refer to the script the evaluator builds ([JE-13](#je-13)), not to the expression as written. The display of a thrown non-Error value above is source-traced; no recording holds one yet ([Q-JE-1](#q-je-1)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:419`, `:939-942`; rquickjs 0.11.0 [`Exception` display](https://github.com/DelSkayn/rquickjs/blob/v0.11.0/core/src/value/exception.rs#L217) and [`CaughtError` display](https://github.com/DelSkayn/rquickjs/blob/v0.11.0/core/src/result.rs#L530); default script name `eval_script` in rquickjs `core/src/context/ctx.rs:190-195`; quickjs-ng [quickjs.c @ be664ca](https://github.com/quickjs-ng/quickjs/blob/be664ca1ccd86cbadf8df7348b8e15e77a32a1ba/quickjs.c) lines 7393, 7801-7803, 47499; the frame limit `quickjs/quickjs.c:2372`, `:7005-7030` in rquickjs-sys 0.11.0. Recorded stack texts: cases JE-21/a, JE-14/a and JE-15/h of `conformance/exprs/`, and runs `je9-l` and `je12-d` of `conformance/flows/je/shortcuts` (oracle run 40687124625). The non-Error display: rquickjs 0.11.0 `core/src/result.rs:547-561` (a caught value is an exception only when it is an object of the `Error` class, `core/src/value/exception.rs:50-56`; anything else is kept as a plain value), `:530-540` (a plain value printed as `Exception generated by quickjs: ` plus its debug form and a newline), `core/src/value.rs:83-123` (the debug form: the type's name, then the payload), `:531-548` (the type names); the evaluator keeps that text after `QuickJS evaluation error: ` and turns only `null` and `undefined` into the memory message, `backend/windmill-jseval/src/lib.rs:381-421`.
- **Conformance:** [JE-21/a, JE-21/b](conformance.md#je-21)
- **Revision 2026-10-28 (validation round 2, design request 9):** the non-Error form was given only as "the engine debug form of the value". It is now traced through rquickjs 0.11.0 and given per type. This corrects the reading that the text is the value's string form (`s` for `throw "s"`): Windmill writes `Exception generated by quickjs: String(Ok("s"))`. Objects, arrays, functions and symbols print a memory address and cannot be reproduced. Added case JE-21/b; Q-JE-1 is re-scoped to the recording.
- **Revision 2026-10-28 (oracle, batch E3a):** the stack text was open. Batch E3a recorded it for runtime and syntax errors, and the diagnostics for a non-callable value and for a rewritten `await` inside a non-async function (`Unexpected identifier 'results'`); added them. The prefix structure is unchanged.

## D. Expression sites and their scopes

<a id="je-22"></a>
### JE-22 · Scope table

**Scope:** core (all other rows); later (`parallelism`, `skip_expr` rows); out (`suspend.user_groups_required` row, AI-agent tool calls) · **Confidence:** high (oracle-observed; `sleep` and `stop_after_if` result identity: medium) · **Status:** confirmed

- **Rule:** these are the places where the flow engine evaluates JavaScript expressions during a flow run, with what each scope contains. In the table:
  - "both = X" means `result` and `previous_result` are both bound to X; P is defined in [JE-36](#je-36);
  - "Yes" under `results` means the lazy proxy of [JE-18](#je-18) and [JE-19](#je-19); "+ JE-9" means the [JE-9](#je-9) shortcut also applies;
  - `flow_input` is the current (sub)flow's input ([JE-37](#je-37)) and `flow_env` the resolved env ([JE-38](#je-38)) unless the row says otherwise; predicates evaluated after a module completes get `flow_env` only if their text contains `flow_env`;
  - `variable`/`resource` follow [JE-20](#je-20), and `error` follows [JE-17](#je-17) at every site.

  | Site (OpenFlow field) | When evaluated | `result` / `previous_result` | Other names | `results` | Boolean-wrapped | Site rule |
  |---|---|---|---|---|---|---|
  | Step input transform, `type: javascript` | When the step is about to be pushed (before any `sleep` delay elapses) | `previous_result` = P; **`result` is not bound** | `params` (always `{}`), `resume`, `resumes`, `approvers` | Yes (+ JE-9) | No | [JE-23](#je-23) |
  | Failure-module input transform | When the failure module starts | `previous_result` = the failed step's result | As above, plus `started_at` and `flow_job_id` when the flow has a start time | Yes; previous id = the failed module's id | No | [JE-35](#je-35) |
  | `forloopflow.iterator` | When the loop module starts (and again on restart inside the loop) | Both = P | `resume`, `resumes`, `approvers` | Yes (+ JE-9) | No | [JE-30](#je-30) |
  | `forloopflow.parallelism` (parallel loops only) | Once per child, as each child is enqueued | Both = the loop's incoming P | None (no `resume`, `resumes`, `approvers`, `params`); `flow_input` is the containing flow's input unchanged | Yes (+ JE-9) | No | [JE-V1](#je-v1) |
  | `branchone.branches[].expr` | When the `branchone` module starts, in order | Both = P | `resume`, `resumes`, `approvers` | Yes (+ JE-9) | Yes | [JE-31](#je-31) |
  | `skip_if.expr` | When the module is about to start | Both = P | `resume`, `resumes`, `approvers` | Yes (+ JE-9) | Yes | [JE-25](#je-25) |
  | `stop_after_if.expr` | After the module (or each sequential loop iteration) succeeds | Both = **this module's own result** (for a loop: the iteration's result) | `all_iters` (loops, only if the text contains `all_iters`) | **Only if the text contains `results.`, `results[` or `results?.`** (+ JE-9); previous id is empty | Yes | [JE-26](#je-26) |
  | `stop_after_all_iters_if.expr` | When a loop or `branchall` completes | Both = the **array of all iteration or branch results** | None | Same textual condition | Yes | [JE-27](#je-27) |
  | `sleep` (`type: javascript`) | When the **next** module is about to start | Both = **this module's own result** | None | **No**: `results` is not defined | No | [JE-28](#je-28) |
  | `timeout` (`type: javascript`) | When the step is pushed | Both = P | None | Yes (+ JE-9) | No | [JE-29](#je-29) |
  | `retry.retry_if.expr` | After a failed attempt | Both = the **failed attempt's result** (an `{"error":…}` object) | None | No | Yes | [JE-32](#je-32) |
  | Flow-level `skip_expr` | At step 0 of the flow | Both = the JSON **string** `"{}"` | `WM_SCHEDULED_FOR` (string) | No | Yes | [JE-33](#je-33) |
  | `suspend.user_groups_required` (`type: javascript`) | When the step after a suspend starts | Both = P | None | No; no client either ([JE-20](#je-20)) | No | [JE-34](#je-34) |

  - No site binds `env`, `process.env`, `WM_*` variables (other than `WM_SCHEDULED_FOR` in `skip_expr`) or a bare `iter`. The loop item is reachable only as `flow_input.iter` ([JE-39](#je-39)).
  - Each row takes the scope of its site rule: `retry_if` is core, `parallelism` and `skip_expr` are later, `suspend.user_groups_required` is out.
  - Under a nested restart that targets a `branchone` module, the originally chosen branch is reused and no predicate is evaluated ([JE-31](#je-31)).
  - **Outside the flow engine** (the boundary of this table):
    - **AI-agent tool calls** evaluate a tool module's input transforms in their own context: `previous_result` comes from the agent context, or is `null`; `flow_input` is the flow's inputs, and there is **no `flow_env`**; a client and a results context are present when available; static empty or `null` transforms are skipped, so the model fills them. This site is out of scope ([JE-47](#je-47)).
    - **Standalone-script `retry_if` and batch rerun** use the simple evaluator ([JE-45](#je-45)).
    - **Concurrency and debounce keys** use argument interpolation, not this evaluator. `debounce_key` is documented as EE, with `$workspace`/`$args[...]` placeholders rather than JavaScript; flow-node debouncing is feature-gated outside CE.
    - `cache_ttl` is a plain number, not an expression.
    - **Triggers:** no trigger filter calls these evaluators.
- **Evidence:** input transforms `backend/windmill-worker/src/worker_flow.rs:2712-2775`, `:3926-3961`; iterator `:5746-5823`, `:5825-5899`; `parallelism` `:4481-4518`; branchone `:5418-5485` (restart lock `:5419-5432`); skip_if `:3842-3870`; stop_after_if `:324-340`, `:662-743`; stop_after_all_iters_if `:342-409`, `:974-1016`, `:1204-1247`; sleep `:3666-3714`, `:2659-2708`; timeout `:4379-4399`; retry_if `:2390-2425`; skip_expr `:3280-3320`; user_groups_required `:3412-3450`; predicate context `:2427-2469`; AI tool calls `backend/windmill-worker/src/ai/tools.rs:337-368`; interpolated keys `backend/windmill-queue/src/jobs.rs:4185-4194`, `:6634-6660`; flow-node debouncing `backend/windmill-worker/src/worker_flow.rs:4453-4478`; types `backend/windmill-types/src/flows.rs:595-605`, `:807-825`; OpenFlow `openflow.openapi.yaml:92`, `:250-276`, `:295-314`, `:396-440`, `:816-818`, `:846-848`, `:856-890`; public docs <https://www.windmill.dev/docs/flows/early_stop> (its description of the step-level early stop, with `result`, `flow_input` and `results`, agrees with the source).
- **Conformance:** [JE-22/a–d](conformance.md#je-22) (one fixture per notable row; the `parallelism` cases are under [JE-V1](conformance.md#je-v1)).

<a id="je-v1"></a>
### JE-V1 · Parallel-loop `parallelism` is an expression site

**Scope:** later · **Confidence:** high (oracle-observed, except the concurrency cap of JE-V1/a; outcome of `parallelism: 0` after enqueue: unknown) · **Status:** confirmed

- **Rule:**
  - A parallel `forloopflow` can supply `parallelism` as an input transform (`static` or `javascript`). A plain number, the older form, is accepted as a static value.
  - **When:** evaluated separately for each child, as that child is enqueued.
  - **Scope:**
    - `result` and `previous_result` are the loop's incoming P;
    - `flow_input` is the **containing flow's input, unchanged**. The parallel loop adds **no new** child `iter` for this evaluation, but an `iter` already present in that input stays visible (the loop sits inside another loop body, or a caller passed one);
    - `flow_env`, an authenticated client and a results context are available, and [JE-9](#je-9) applies;
    - there is no `resume`, `resumes`, `approvers` or `params`.
  - **Value:** not wrapped as a boolean; ordinary shortcuts apply. It must parse as an unsigned 16-bit integer (0 to 65535). Errors read `Error parsing result as u16: …` and `Error parsing static value as u16: …`. Invalid or fractional values fail as flow-chaining failures ([JE-35](#je-35) path (a) or (b)).
  - **Zero** passes validation and is neither rejected nor raised to one. At enqueue time **every** child is held: each gets a positive suspension count and a suspend-until 14 days ahead. Children are normally released when another child completes; with `0` no child starts, so the ordinary completion-driven release never begins. What happens after that (suspension timeout, monitors, cancellation) is open ([Q-JE-6](#q-je-6)); this finding does not claim the loop stays stuck forever.
  - **Not evaluated** by a sequential loop, nor by a while loop, even though OpenFlow lists the field there too.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:4481-4518` (containing arguments `:4486-4497`, zero handling `:4501-4513`), child `iter` insertion `:4210-4223`, completion-driven release `:1097-1113`, typed evaluation `:2659-2708`; `backend/windmill-types/src/flows.rs:595-605`, `:807-825`; `openflow.openapi.yaml:816-818`, `:846-848`.
- **Conformance:** [JE-V1/a–e](conformance.md#je-v1)

<a id="je-23"></a>
### JE-23 · Step input transforms

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - Each key of `input_transforms` is evaluated **independently**, in an **unspecified order**. Each evaluation that reaches QuickJS gets its own fresh runtime. Transforms cannot see each other: `params` is always the empty object `{}`.
  - **The order is not fixed and may change from one pass to the next** (its variation oracle-observed). Each time a step is about to be pushed, and at each iteration of a collapsed sequential loop body ([JE-36](#je-36)), the module is decoded again from the stored flow, and its `input_transforms` land in a new hash map. The map's hasher is seeded per map, and nothing ties one pass's order to another's: two passes may take the keys in the same order or in different ones, between runs, between the iterations of one loop, and between two flows that are otherwise the same. The order does not follow the order in which the flow's YAML or JSON writes the keys, so neither the CLI's name order nor any other written order decides it.
  - **Dispatch is serial and stops at the first error** ([JE-V5](#je-v5)).
  - **`type: static`:** the stored JSON value is copied verbatim, with no templating ([JE-40](#je-40)).
  - **`type: ai`:** the key is omitted.
  - **`type: javascript`:** [JE-5](#je-5) to [JE-21](#je-21) apply. An expression yielding `undefined` gives a present key with value `null`, not an absent key.
  - Omitted payloads and unknown types: [JE-V4](#je-v4).
  - **On error,** the step is *still pushed*, with an empty argument map and a pre-run error. When it is picked up, it fails without running, with result ``{"error":{"name":"ExecutionErr","message":"Error during isolated evaluation of expression `<expr>`:\n<evaluator error>"}}``. This is an ordinary step failure: `retry`, `continue_on_error` and the failure module apply as for any failure ([execution](execution.md); surrounding error fields such as `step_id` are defined there).
    - When the body of a collapsed loop ([JE-36](#je-36)) fails a transform, the message is prefixed `could not transform input using an expr: `.
    - If several transforms would fail, the reported one is whichever is evaluated first in that pass's order; the others are never evaluated. Which one that is is not fixed, and may change from pass to pass (JE-23/e records it changing).
  - **Transforms are not evaluated at all** when:
    - the step is skipped by `skip_if`;
    - `mock.enabled` is true;
    - the module is `identity`;
    - the step is the preprocessor (it receives the flow input plus an entrypoint override);
    - the module sets `pass_flow_input_directly` (it receives the flow input as is);
    - the attempt is an ordinary retry, which reuses the recorded arguments ([JE-V2](#je-v2)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2712-2775` (`params` built from the still-empty map at `:2732`; the pass iterates the map at `:2746`), `:3874-3966` (the module decoded again at `:3918`), `:4152-4255`, `:4403-4436`, `:5682-5690` (a collapsed body's transforms taken from a module decoded again at each iteration); the module is decoded from its stored JSON on every call, `backend/windmill-types/src/flows.rs:634-637`, into the standard library's `HashMap` (`:1-4`, `:966`, `:980`, `:1021`, `:1041`, `:1054`), whose default hasher is seeded per instance ([`RandomState`](https://doc.rust-lang.org/std/collections/hash_map/struct.RandomState.html): hashers from two instances are unlikely to agree); pre-run error `backend/windmill-worker/src/worker.rs:3647-3649`; error to result `backend/windmill-worker/src/result_processor.rs:1910-1965`, `backend/windmill-common/src/worker.rs:2228-2233`. Recorded: the order varies, runs `order` of `conformance/flows/je/transform-order` and `conformance/flows/je/transform-order-api` (oracle run 40801169516).
- **Conformance:** [JE-23/a–e](conformance.md#je-23)
- **Revision 2026-10-29 (oracle, batch E3e):** "an unspecified order (a hash map)" is now oracle-observed and stated precisely. A step whose three transforms all fail, evaluated sixteen times as the iterations of one loop, did not report the same transform first every time, both when the CLI wrote the transforms in name order and when the flow was created through the API in another order. The source explains it: every pass decodes the module again into a freshly seeded hash map. This answers milestone E1's question whether the CLI's name sort fixes the order: it does not, and no written order does. Added case JE-23/e.
- **Revision 2026-10-29 (validation round 1):** the rule said the order "differs from one pass to the next", and JE-23/e required that sixteen passes not all report the same transform. Freshly seeded maps make the order unspecified and allow it to vary, but they guarantee neither a different order on the next pass nor any variation in a given number of passes. The rule now says the order is not fixed and may change; the recorded variation is kept as an observed sample, and JE-23/e's check is marked statistical.

<a id="je-v2"></a>
### JE-V2 · Ordinary retries reuse the recorded transformed arguments

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - When an ordinary step (a script or flow module) is retried, the new attempt's arguments are the failed attempt's **recorded** arguments (O2). Its input transforms are **not** re-evaluated, so `variable()`, `resource()` and `results` reads inside them are not repeated.
  - The restoration of P after a retry ([JE-36](#je-36) rule 3) happens before `skip_if`, iterator and `timeout` evaluation; it does not mean the transforms run again.
  - Marker strings in the recorded arguments are resolved again when the new job runs ([JE-41](#je-41)), so they can pick up changed values.
  - **After a failed transform,** the first attempt was pushed with an empty argument map plus the pre-run error ([JE-23](#je-23)). An ordinary retry reuses the empty map and runs the script without that error.
  - Collapsed loop bodies ([JE-36](#je-36)) evaluate the body's transforms while building each iteration's arguments; this rule is about ordinary steps only.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3758-3795`, `:3887-3904`, `:4168-4194`, `:4269-4283`, `:4403-4436`; `backend/windmill-worker/src/common.rs:80-89`.
- **Conformance:** [JE-V2/a–c](conformance.md#je-v2)

<a id="je-v4"></a>
### JE-V4 · Missing transform payloads have defined defaults

**Scope:** later · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - `{"type":"static"}` without `value` gives `null`.
  - `{"type":"javascript"}` without `expr` is the empty expression, which yields `null` ([JE-13](#je-13)).
  - An unknown `type` is a deserialisation error, not an expression-time exception: the flow fails to load, with `` got value: <type> for field `type`, expected value: `static` or `javascript` ``.
  - An argument with no transform entry at all is **absent** from the arguments.
- **Evidence:** `backend/windmill-types/src/flows.rs:732-790` (defaults `:780-786`); `backend/windmill-jseval/src/lib.rs:554-570`, `:864-870`.
- **Conformance:** [JE-V4/a–c](conformance.md#je-v4)

<a id="je-v5"></a>
### JE-V5 · Transform dispatch is serial and stops at the first error

**Scope:** core · **Confidence:** high (the varying order: oracle-observed) · **Status:** confirmed

- **Rule:**
  - Within one pass over a step's input transforms, transforms are taken in the order of a hash map seeded afresh for that pass, so the order is not fixed and may change from one pass to the next ([JE-23](#je-23)). Each evaluation is **awaited before the next one starts**.
  - The pass **stops at the first error**. Later transforms are never evaluated, so their helper reads (`variable()`, `resource()`, `flow_user_state()`, `results` fetches) never happen.
  - Isolated contexts mean transforms cannot see each other's values. They do **not** make external reads, time-dependent expressions, error selection or side effects order-independent.
  - A timed-out CPU-bound evaluation is not forcibly stopped ([JE-2](#je-2)); that concerns the evaluator's thread, not the dispatch order.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2743-2770`; helper reads `backend/windmill-jseval/src/lib.rs:584-614`; timeout task `:303-328`; a non-200 helper response's body becomes the error text `backend/windmill-common/src/client.rs:273-294`.
- **Conformance:** [JE-V5/a](conformance.md#je-v5); the varying order is recorded by [JE-23/e](conformance.md#je-23).
- **Revision 2026-10-29 (oracle, batch E3e):** the order of a pass was recorded varying from pass to pass in a sample of passes ([JE-23](#je-23), JE-23/e). The serial dispatch and the first-error stop are unchanged; JE-V5/a, which counts the reads, stays source-derived.
- **Design note (not Windmill behaviour):** nothing here shows that evaluating transforms concurrently, sharing a runtime, or de-duplicating reads (for example fetching `results.a` once for several transforms) preserves behaviour. Sending all of a step's transforms in one transport message is behaviour-preserving only if each is still evaluated one at a time, with the same context, its own engine instance and per-evaluation limits, the same reads, and the same first-error stop.

<a id="je-24"></a>
### JE-24 · Predicates are wrapped as `Boolean(<expr>)`

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Wrapping.** Every boolean site ([JE-22](#je-22)) wraps the predicate text **as written** (untrimmed) in `Boolean(` … `)`; only then is the whole trimmed ([JE-5](#je-5)) and sent through the same pipeline.
  - **Shortcuts.** [JE-6](#je-6), [JE-7](#je-7) and [JE-8](#je-8) never fire, because the text starts with `Boolean(`. [JE-9](#je-9) **can** fire: on any predicate line that by itself is a qualifying `results` path, at sites with a client and a results context. Its value then meets the boolean check.
  - **Strict boolean check.** Anything other than the JSON `true` or `false` fails with `Expected a boolean value, found: "<json>"`. This is reachable through statement mode (for example a `;` in a regex literal makes the wrapper a statement body with no `return`, giving `null`) and through JE-9.
  - **Trailing line comment.** If the predicate text ends with a newline after a `//` comment, as YAML block scalars do, the closing `)` lands on the next line and the predicate works. Without the trailing newline it is a syntax error. Direct expressions differ ([JE-14](#je-14)).
  - **No statements.** Statements and `return` are impossible: a `;` or a leading keyword gives a syntax error or statement mode, which yields `null`.
  - **Errors.** Evaluation errors become a generic error whose text is `Error: <evaluator error> @<rust location>` and whose name is `Anyhow`. Each site routes it differently ([JE-25](#je-25) to [JE-33](#je-33), [JE-35](#je-35)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2427-2469` (wrapping `:2451-2466`); `backend/windmill-worker/src/js_eval.rs:36`, `:74-79`; `backend/windmill-jseval/src/lib.rs:73-75`, `:184-218`, `:555-559`; error conversion `backend/windmill-common/src/error.rs:73-74`, `:138-151`.
- **Conformance:** [JE-24/a–d](conformance.md#je-24), plus the predicate cases [JE-9/j, JE-9/k](conformance.md#je-9).

<a id="je-25"></a>
### JE-25 · `skip_if`

**Scope:** core · **Confidence:** high (oracle-observed; error routing end to end: medium) · **Status:** confirmed

- **Rule:**
  - **Order:** evaluated after the previous module's `sleep` has been scheduled and after `retry_if`, and before the module's input transforms. Scope per [JE-22](#je-22).
  - **When true:** the module becomes an identity step ([execution](execution.md)) and its transforms are not evaluated.
  - **Error:** computing the next step fails, and the error is routed per [JE-35](#je-35): path (a) after a child completed, (b) at first pickup of a root flow, (c) at first pickup of a nested flow. The log line is ``Error evaluating skip_if of module '<id>' expression `<expr>`: …``, quoting the expression as written ([JE-5](#je-5)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3666-3870` (skip_if `:3842-3870`), `:3920-3925`; chaining failure `:2086-2125`; log line `:2471-2488`; parity test with `results` in `skip_if` `backend/tests/flow_engine_parity.rs:694-790`.
- **Conformance:** [JE-25/a](conformance.md#je-25)

<a id="je-26"></a>
### JE-26 · `stop_after_if`, and a throwing predicate fails the parent, not the child

**Scope:** core · **Confidence:** high (oracle-observed; paths; end to end: medium) · **Status:** confirmed

- **Rule:**
  - **When evaluated:** only after a module **succeeds**. It is not evaluated:
    - for a `branchall` module;
    - per iteration of a parallel loop;
    - for a step skipped by `skip_if`;
    - for the failure module or the preprocessor;
    - when a child already requested an early stop ([execution](execution.md), [control flow](control-flow.md)).
  - **Scope:** `result` and `previous_result` are the module's own result (for a sequential loop module: the iteration's result). `all_iters` is the array of iteration results so far, **including the iteration that just completed**, and is fetched only if the text contains `all_iters`: a sequential loop whose iterations return `10` then `20` sees `[10]` after the first and `[10,20]` after the second. `results` is available only under the textual condition of [JE-22](#je-22).
  - **Error path:**
    - **The child stays successful.** The child job is stored as successful, with its own result, *before* the parent evaluates `stop_after_if`. Nothing rewrites that row.
    - **The parent records the failure.** If the predicate throws, the parent's status update is redone as a failure. The error value is the predicate error **as a string**: `{"error":"Error: QuickJS evaluation error: … @<rust location>"}`, flagged unrecoverable.
    - The module is marked failed; there is **no retry**, and `continue_on_error` is **ignored**.
    - Without a failure module, the flow's final result is that object.
    - With a failure module, it runs, and its `previous_result` is that object. [JE-17](#je-17)'s `error` therefore holds the **string**, so a failure module must not assume `error.message`.
  - The behaviour of `skip_if_stopped`, `error_message` and `error_include_result` is described in [execution](execution.md).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:646-743`, `:224-250`, `:685-698`, `:1300-1325`, `:1738-1746`, `:3732-3742`, child appended at enqueue `:4591-4630`; child stored first `backend/windmill-worker/src/result_processor.rs:1490-1531`.
- **Conformance:** [JE-26/a–d](conformance.md#je-26)

<a id="je-27"></a>
### JE-27 · `stop_after_all_iters_if`

**Scope:** core · **Confidence:** high (oracle-observed; empty-body path: medium) · **Status:** confirmed

- **Rule:**
  - **When evaluated:** when a loop or `branchall` module completes with at least one child. For a parallel module that is when its last child finishes; for a sequential `branchall`, only if no branch stopped early.
  - An empty iterator or an empty branch list completes the module through a separate empty-body path, **without** evaluating `stop_after_all_iters_if`.
  - **Scope:** `result` and `previous_result` are the array of child results. `results` follows the textual condition of [JE-22](#je-22).
  - **Error:** the module stops early **with the error** ``Error evaluating stop_after_all_iters_if expression `<expr>`: <error text>``, and the module fails as an ordinary step failure ([JE-35](#je-35) path (d)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:342-409`, `:974-1016`, `:1204-1247`; empty-body path `:3998-4050`, `:5332-5333`, `:5539`.
- **Conformance:** [JE-27/a, JE-27/b](conformance.md#je-27)

<a id="je-28"></a>
### JE-28 · `sleep`

**Scope:** core · **Confidence:** high (oracle-observed; error routing: medium) · **Status:** confirmed

- **Rule:**
  - **When evaluated:** a module's `sleep` is evaluated when the **next** module is about to start. Its `result` is the sleeping module's own result. There is no `results` ([JE-22](#je-22)).
  - **The value delays when the next job runs, not evaluation:** the next module's `skip_if`, transforms and `timeout` are evaluated immediately, and only the job is scheduled later.
  - **A last module's `sleep` is evaluated too,** because a module with a `sleep` is never last in the list that runs: Windmill appends the virtual identity step `<id>-v` after a final module that has a `sleep` or `suspend` ([OF-17](openflow.md#of-17)), and the `sleep` is evaluated when that step starts. The `sleep` then delays the end of the flow (or body), and an invalid value fails the flow with a chaining error (route (a) of [JE-35](#je-35)): on the oracle, a flow `[a, b]` whose last module `b` has `sleep: {type: javascript, expr: "nope"}` ran `b`, then failed with `InternalError` ``Expected a number value, had error instead: ExecutionErr("Error during evaluation of expression `nope`:\nQuickJS evaluation error: Error: nope is not defined…")``. The same applies to the last module of a loop or branch body, whose stored body gets the virtual step as well. Only a list without the virtual step would skip the final `sleep`; which forms lack it is [OF-V5](openflow.md#of-v5)'s question, and no deployed run the oracle recorded lacked it.
  - **Static values** are parsed as JSON. **JavaScript values** go through [JE-5](#je-5) to [JE-21](#je-21). The result is then interpreted:
    - a non-negative integer is that many **seconds**;
    - a JSON number with a fraction or exponent is `value × 1000` **milliseconds**, truncated; a negative fraction becomes `0`. A static `2.0` therefore counts as a float (2000 ms), while an expression returning `2.0` serialises to `2`;
    - a negative integer fails with `Expected an integer, found: <n>`;
    - any other JSON fails with `Expected an integer, found: <debug form>`, for example `Expected an integer, found: Null` or `Expected an integer, found: String("5")`;
    - an evaluation error fails with ``Expected a number value, had error instead: ExecutionErr("Error during evaluation of expression `<expr>`:\n<evaluator error>")``. This is a debug form, so the newline appears escaped as `\n`.
  - **Failures** are flow-chaining failures, routed per [JE-35](#je-35).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3666-3714` (only the previous module's sleep is read, `:3666-3684`), `:2659-2708`; scheduling `:4403-4436`; the virtual step `backend/windmill-types/src/flows.rs:1252-1280`, added to every stored module list at deploy (`backend/windmill-worker/src/worker_lockfiles.rs:1706`) and to raw and inline bodies at run time (`backend/windmill-worker/src/worker_flow.rs:5056`, `backend/windmill-queue/src/jobs.rs:5605`). Recorded: runs `je28-a`, `je28-b` and `je28-c` of `conformance/flows/je/waits` (oracle run 40687124625).
- **Conformance:** [JE-28/a–c](conformance.md#je-28)
- **Revision 2026-10-28 (oracle, batch E3a):** the finding said the last module's `sleep` is never evaluated, because no next module starts, and JE-28/c expected no delay and no error. That ignored [OF-17](openflow.md#of-17)'s virtual identity step: on the oracle the final `sleep` was evaluated when that step started, and its invalid expression failed the flow. Corrected the rule and JE-28/c.

<a id="je-29"></a>
### JE-29 · `timeout`

**Scope:** core · **Confidence:** high (oracle-observed; error routing: medium) · **Status:** confirmed

- **Rule:**
  - The value must deserialise as a **32-bit signed integer** number of seconds.
  - **Error messages:**
    - a static value that fails: `Error parsing static value as i32: <serde error>`;
    - a JavaScript result that fails: `Error parsing result as i32: <serde error>. Value was: <json>`, where `<json>` is the evaluator's output text as it is, not re-serialised: a [JE-8](#je-8) shortcut quotes `previous_result`'s text as [JE-44](#je-44)'s table gives it (after an ordinary step the producer's text, `Value was: {"b":1,"a":2}`; right after a loop a compact array in `jsonb` key order, `Value was: [{"a":2,"b":1}]`), a [JE-7](#je-7) shortcut a stored flow input member's spaced `jsonb` text (`Value was: {"a": 2, "b": 1}`), a [JE-9](#je-9) lookup the stored `jsonb` text with its spacing and key order (`Value was: {"a": 2, "b": 1}`), and JavaScript its `JSON.stringify` text (`Value was: 1.5`) (oracle-observed);
    - a negative value: `Timeout value cannot be negative`;
    - an evaluation error: ``Error during evaluation of expression `<expr>`:\n<evaluator error>``.
  - **Scope:** per [JE-22](#je-22), including `results`.
  - **When evaluated:** at push time, after the transforms. Failures are flow-chaining failures, routed per [JE-35](#je-35): after a completed step the flow fails with `InternalError` and the message as above, without prefix or location suffix; at the first module, at the root flow's first pickup, it fails as `Anyhow` with the message `Error: ` plus the message as above plus one location suffix (oracle-observed).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:4379-4399`, `:2659-2708`. Recorded: run `je29-a` of `conformance/flows/je/waits`, runs `je8-order-timeout` and `je8-order-timeout-db` of `conformance/flows/je/shortcuts` (oracle run 40687124625); runs `fi_first`, `fi_later` and `loop_prev` of `conformance/flows/je/shortcut-texts` (oracle run 40794872025).
- **Conformance:** [JE-29/a](conformance.md#je-29)
- **Revision 2026-10-28 (oracle, batch E3a):** added that the `Value was:` part quotes the evaluator's text unchanged, which batch E3a recorded through both lookup routes, and the recorded chaining envelope. The rule is unchanged.
- **Revision 2026-10-29 (oracle, batch E3e):** batch E3e quoted three more texts through this site, a stored flow input member through [JE-7](#je-7) and the aggregate right after a loop through [JE-8](#je-8) ([JE-44/d](conformance.md#je-44), [JE-44/e](conformance.md#je-44)), and recorded the first-pickup envelope (`Anyhow`, `Error: …` with one location suffix). Added them; the rule is unchanged.

<a id="je-30"></a>
### JE-30 · The `forloopflow` iterator

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - The expression must yield a JSON **array**. Otherwise the module fails with `Expected an array value in the iterator expression, found: <serde error>`, for example `invalid type: map, expected a sequence at line 1 column 0`. On a restart inside a loop the iterator is recomputed, and the message is `Expected an array value, found: …`.
  - Each element is kept as its JSON text and becomes `flow_input.iter.value` ([JE-37](#je-37)).
  - An empty array gives an empty loop ([control flow](control-flow.md)).
  - A static iterator's elements are passed verbatim. `$var:`/`$res:` strings inside them are resolved only when a leaf step receives them ([JE-41](#je-41)).
  - An evaluation error propagates unwrapped, as `Error: QuickJS evaluation error: … @<rust location>`, and is a flow-chaining failure, routed per [JE-35](#je-35).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:5746-5899`; test `backend/tests/worker.rs:3375`.
- **Conformance:** [JE-30/a, JE-30/b](conformance.md#je-30)

<a id="je-31"></a>
### JE-31 · `branchone` predicates

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - Branches are evaluated **in order**. The first predicate that is `true` wins, and later predicates are **not evaluated**. If none is true, `default` runs.
  - A predicate error stops the scan. The module then fails as an **ordinary step failure** with result `{"error":{"message":"<error text>","name":"<error name>"}}`. For an evaluation error the name is `Anyhow` and the message is `Error: QuickJS evaluation error: … @<rust location>`. Ordinary failure handling then applies.
  - This describes a normal execution. Under a nested restart that targets the `branchone` module, the originally chosen branch is reused and no predicate is evaluated.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:5418-5485` (restart lock `:5419-5432`), `:4053-4076`.
- **Conformance:** [JE-31/a, JE-31/b](conformance.md#je-31)

<a id="je-32"></a>
### JE-32 · `retry_if` on a flow step

**Scope:** core · **Confidence:** medium (its cases oracle-observed) · **Status:** confirmed

- **Rule:**
  - **When evaluated:** after a failed attempt, before the next one is scheduled.
  - **Scope:** `result` and `previous_result` are the failed attempt's result; `error` is that result's `.error` ([JE-17](#je-17)). There is no `results`.
  - If the predicate is false, the step is not retried and normal failure handling continues.
  - If the expression throws, the error propagates: as a flow-chaining failure at step start ([JE-35](#je-35)), or as a re-reported failure on the `continue_on_error` path. Attempt counts and delays are covered in [execution](execution.md).
  - **Standalone scripts** with a retry policy evaluate `retry_if` with a separate, simpler evaluator, where an evaluation error means "retry anyway" ([JE-45](#je-45)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2390-2425`, `:3716-3729`, `:1305-1322`; standalone scripts `backend/windmill-queue/src/jobs.rs:1647-1690`; OpenFlow `openflow.openapi.yaml:250-258`, which mentions `result` and `error`.
- **Conformance:** [JE-32/a, JE-32/b](conformance.md#je-32)

<a id="je-33"></a>
### JE-33 · The flow-level `skip_expr`

**Scope:** later · **Confidence:** high (oracle-observed; `WM_SCHEDULED_FOR` format: medium, [Q-JE-7](#q-je-7)) · **Status:** confirmed

- **Rule:**
  - **When evaluated:** at step 0 of a flow whose value has `skip_expr` (root flows and flows called by path).
  - **Scope:** `flow_input`, `flow_env`, and `WM_SCHEDULED_FOR`, the job's scheduled time as a string of the form `YYYY-MM-DD HH:MM:SS[.ffffff] UTC`. `result` and `previous_result` are the JSON string `"{}"`. There is no `results`. `WM_SCHEDULED_FOR` is the only `WM_*` name any expression can read ([JE-39](#je-39)).
  - If the predicate is true, the flow stops early as a stop with `skip_if_stopped` would ([FX-19](execution.md#fx-19), [FX-22](execution.md#fx-22)): it completes with status **`skipped`** (its job counts as successful) and result `"stopped early"` (a string). No module runs.
  - An error is logged as `Error evaluating flow skip_expr expression …` and is a flow-chaining failure ([JE-35](#je-35) path (b) for a root flow).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3280-3320` (the extra binding `:3289-3293`; the stop passed on as an early stop that skips, `:3309-3318`), `:670-679` (that override taken as an early stop with the skip flag set), `:1048`, `:1278`, `:2010` (an early stop with the skip flag marks the flow skipped); `backend/windmill-common/src/variables.rs:273`; `openflow.openapi.yaml:92`; public docs <https://www.windmill.dev/docs/flows/early_stop#early-stop-for-flow>. Recorded: runs `je33-a` and `je33-b` of `conformance/flows/je/sites` (oracle run 40687124625).
- **Conformance:** [JE-33/a, JE-33/b](conformance.md#je-33)
- **Revision 2026-10-28 (oracle, batch E3a):** the finding said the flow completes successfully. On the oracle it ends with status `skipped` (result `"stopped early"`), because the stop is passed on with the skip flag set; [FX-22](execution.md#fx-22) already said so. Corrected the rule and JE-33/a–b.

<a id="je-34"></a>
### JE-34 · `suspend.user_groups_required`

**Scope:** out · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - When it is `type: javascript`, it is evaluated with `result`/`previous_result` = P, `flow_input` and `flow_env`, and **no client and no results context**. [JE-20](#je-20) gives the helper behaviour in that case.
  - The value must be an array of strings. Otherwise the error is `` Result returned by input transform invalid `<serde error>` ``.
  - Suspend and approval are out of scope.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3412-3450`.
- **Conformance:** none (out of scope).

<a id="je-35"></a>
### JE-35 · Failure-module transforms, and where expression errors land

**Scope:** core · **Confidence:** high (oracle-observed for routes (a) to (c) and their envelopes) · **Status:** confirmed

- **Rule:**
  - **Failure-module scope.** The failure module's transforms see:
    - `previous_result` = the failed module's result (normally `{"error":{…}}`; a string-valued error after [JE-26](#je-26));
    - previous id = the failed module's id, so `results.<failed id>` is the [JE-8](#je-8) shortcut;
    - `started_at` (the flow's start timestamp, as an RFC 3339 JSON string) and `flow_job_id` (the flow job's UUID string), both only when the flow has a start time;
    - `error`, per [JE-17](#je-17).
  - **Error routes.**
    - **(a) Chaining after a child completed.** The error happens while the flow computes its next module after one of its children finished. This covers `skip_if`, `sleep`, `timeout`, the iterator, `parallelism` and step-start `retry_if` on any module started that way.
      - The flow job is completed directly with `{"error":{"message":"<error text>","name":"InternalError"}}`.
      - The error text is the flow code's own (oracle-observed): a predicate or iterator evaluation error has one prefix and one suffix, `Error: QuickJS evaluation error: … @worker_flow.rs:<line>:<col>`; a typed error has neither, for example `Expected an integer, found: -1` or ``Error parsing result as i32: … Value was: 1.5``.
      - The flow's failure module is **not** run.
      - The log reads `Unexpected error during flow chaining:`.
    - **(b) First pickup of a root flow.** The error happens while starting the first module, when the root flow job is first handled; for example `skip_if` on module 0, flow `skip_expr`, or the iterator of a loop at module 0.
      - Ordinary job-error handling applies, with the error object `{"message":"<text>","name":"<error name>"}`.
      - The error crosses the worker's job dispatch, which turns it into a generic error: the name is **`Anyhow`** for every error on this route, and the message gains an `Error: ` prefix and a location suffix ` @worker.rs:<line>:<col>`. A predicate or iterator evaluation error already carries one `Error: ` prefix and one suffix from the flow code, so it reads `Error: Error: QuickJS evaluation error: … @worker_flow.rs:<line>:<col> @worker.rs:<line>:<col>`, with **two** prefixes and two suffixes. A typed error, such as the boolean check of a predicate or the `timeout` checks, gets one of each: `Error: Expected a boolean value, found: "null" @worker.rs:<line>:<col>` (oracle-observed; see also [FX-15](execution.md#fx-15)).
      - The failure is applied to the root's own status, so the root's **failure module runs** if present. Without one, the final result is `{"error":{…}}` with that object.
    - **(c) First pickup of a nested flow.** The same kind of error, raised in a loop or branch body or in a subflow called by path.
      - The nested job is completed with the error, with the same envelope as route (b): name `Anyhow`, two prefixes and two suffixes for an evaluation error (oracle-observed for a subflow called by path). Its **parent** is updated as if that child failed.
      - The nested flow's own failure module is **not** run.
      - The parent then applies its own retry, `continue_on_error` or failure module.
    - **(d) Step failures:** input transforms ([JE-23](#je-23)), `branchone` predicates ([JE-31](#je-31)) and `stop_after_all_iters_if` ([JE-27](#je-27)) follow ordinary failure semantics.
    - **(e) Unrecoverable string error:** `stop_after_if` ([JE-26](#je-26)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2086-2125`, `:1300-1340`, `:3732-3822`, `:3934-3955`; `backend/windmill-queue/src/jobs.rs:883-918`; `backend/windmill-worker/src/worker.rs:1706-1736`, `:3815-3841`; `backend/windmill-worker/src/result_processor.rs:1910-1978`. The envelopes: the generic error's display (`Error: ` before, ` @<location>` after) `backend/windmill-common/src/error.rs:73-74`, `:138-150`; a predicate's evaluation error becomes that error inside the flow code (`backend/windmill-worker/src/worker_flow.rs:2451-2460`, the boolean check `:2462-2467`); the flow handler returns an untyped error (`:2777-2793`), which the job dispatch turns into the generic error again (`backend/windmill-worker/src/worker.rs:3822-3840`). Recorded: runs `je35-a` to `je35-d` of `conformance/flows/je/errors`, cases JE-24/b and JE-24/d of `conformance/exprs/je-24.yaml` and runs `je28-b`, `je29-a`, `je30-b` and `je-v1-b` (oracle run 40687124625).
- **Conformance:** [JE-35/a–d](conformance.md#je-35)
- **Revision 2026-10-28 (oracle, batch E3a):** route (b) said a predicate's evaluation error reads `Error: QuickJS evaluation error: … @<rust location>`, and that typed errors are named `ExecutionErr`. On the oracle every route-(b) error is named `Anyhow` and gains a second `Error: ` prefix and a second location suffix from the job dispatch, so an evaluation error has two of each and a typed error one (JE-35/b, JE-24/b, JE-24/d); route (c) has the same envelope (JE-35/d). Added the recorded route-(a) texts. The routes and which failure module runs are unchanged and were recorded as stated.

<a id="je-36"></a>
### JE-36 · What `previous_result` is at each position

**Scope:** core · **Confidence:** high (oracle-observed; the preview while loop: pending, [Q-JE-5](#q-je-5)) · **Status:** confirmed

- **Rule:** P, the value bound as `previous_result` (and as `result` at the sites that use P), is:
  1. **At the first module of a flow job:** that job's **input object**. Flow jobs are the root, a non-collapsed loop-iteration body (sequential *or* parallel), a branch body, and a subflow called by path.
     - In an iteration body, that is the parent's `flow_input` plus `iter` ([JE-37](#je-37)).
     - In a branch body, it is the parent's `flow_input`, **not** the result of the step before the branch.
     - The rule applies whenever the module being started sits at **index 0** of its flow. That includes a collapsed loop at index 0, on **every** iteration.
  2. **Later modules:** the previous module's result, held in memory as raw text (normally the exact text the step produced). For a loop or `branchall` module it is the array of child results.
  3. **After a retry is scheduled:** P is restored to the preceding step's result (or the flow input at step 0), so `skip_if` and the iterator never see the failed attempt. Retried ordinary steps do not re-evaluate their transforms ([JE-V2](#je-v2)).
  4. **Fallback:** the JSON **string** `"{}"`, when the preceding step has no success status (for example it failed with `continue_on_error`) and P must be re-fetched (after a suspend, a restart or a retry).
  5. **Collapsing.** A loop's body is collapsed when it is a single plain script step with no `sleep`, `suspend`, cache, retry, stop or skip settings and no enabled mock, in a flow without a failure module:
     - **sequential `forloopflow`:** collapsed under these conditions, **except in preview runs**;
     - **`whileloopflow`:** collapsed under the same conditions, **including preview runs**;
     - **parallel `forloopflow`:** **never** collapsed; every iteration gets its own body flow (rule 1).

     Inside a collapsed loop, the body's transforms run in the loop's flow context with an empty previous id:
     - `flow_input` is the containing flow's input plus `iter`;
     - for a loop at index > 0, P is the preceding module's result on the first iteration and the previous iteration's result afterwards;
     - for a loop at index 0, P is the containing flow's input every time (rule 1).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3324-3345`, `:3766-3793`, `:4168-4194`, `:5239-5289`, `:5294-5297`, `:5352-5414`, `:5664-5700`, `:5729-5744`; subflow arguments `:3962-3966`; fallback `:6227-6260`.
- **Conformance:** [JE-36/a–f](conformance.md#je-36)

<a id="je-37"></a>
### JE-37 · What `flow_input` contains

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **A root flow:** its input arguments.
  - **A loop iteration's body:** the parent's `flow_input` plus the key `iter` = `{"index": <i32>, "value": <element>}`.
    - If the parent input already has `iter` (nested loops), the existing value moves to `iter_parent`, an existing `iter_parent` moves to `iter_parent_parent`, and so on.
    - In a `whileloopflow`, `iter` = `{"index": n, "value": n}`, where the value is the iteration number.
    - A collapsed iteration ([JE-36](#je-36)) has the same `flow_input`, but as an argument context of the loop, not as a child flow job.
  - **A branch body:** the parent's `flow_input`, unchanged.
  - **A subflow called by path:** that subflow's own inputs, as computed by the `flow` module's transforms.
  - `parallelism` sees the containing flow's input with no new `iter` ([JE-V1](#je-v1)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:4152-4255`, `:4981-4998`, `:5239-5286`; test `backend/windmill-jseval/src/lib.rs:2582-2634`.
- **Conformance:** [JE-37/a](conformance.md#je-37)

<a id="je-38"></a>
### JE-38 · What `flow_env` contains

**Scope:** later · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Source.** `flow_env` comes from the flow value's `flow_env` map. When the flow has none and the job has a parent, it is inherited from the nearest enclosing scope, one scope at a time. This covers loop and branch bodies, subflows called by path, and imported flows alike.
  - **Resolution.** Its values go through `$var:`/`$res:` resolution ([JE-41](#je-41), [JE-42](#je-42)). This happens again **each time the engine handles the flow job**, which is on every step transition.
  - **Cached for some predicates.** Predicates evaluated after a child completes (`stop_after_if`, `stop_after_all_iters_if`, and `retry_if` on that path) use a per-flow cached resolved map once one exists. They receive `flow_env` only if their text contains `flow_env`; otherwise it is absent, which behaves the same because of [JE-10](#je-10).
  - **No snapshot.** A variable changed during a run may therefore be seen by some sites and not by others.
  - **Failure.** If resolution fails, the unresolved map is used, with a warning, and it is not cached.
  - **In expressions,** `flow_env` is a plain object, `{}` when no env exists, and `flow_env.K` takes the [JE-7](#je-7) shortcut. It is the one place where expressions see resolved references ([JE-41](#je-41)). AI-agent tool calls have no `flow_env` ([JE-22](#je-22)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2795-2850`, `:502-553`, `:2504-2587`, `:2560-2650`; inheritance parity test `backend/tests/flow_engine_parity.rs:2664`.
- **Conformance:** [JE-38/a–d](conformance.md#je-38)

<a id="je-39"></a>
### JE-39 · Names that do not exist

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** these names are **not** defined in any flow expression, so referencing them throws a `ReferenceError`: `env`, `process`, `iter` (use `flow_input.iter`), `WM_WORKSPACE`, `WM_JOB_ID` and every other `WM_*` name except `WM_SCHEDULED_FOR` in flow `skip_expr` ([JE-33](#je-33)), `wmill`, `step_id`, `job_id`. `all_iters` exists only in `stop_after_if` ([JE-26](#je-26)).
- **Evidence:** the context maps of [JE-22](#je-22); installed scope `backend/windmill-jseval/src/lib.rs:455-552`; the only extra global `backend/windmill-worker/src/worker_flow.rs:3289-3293`.
- **Conformance:** [JE-39/a](conformance.md#je-39)

## E. Static values and `$` references

<a id="je-40"></a>
### JE-40 · Static values are copied verbatim, with no templating

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - A `type: static` transform value is copied verbatim. **The backend has no template interpolation of static strings:** `${…}` in a static string is literal text.
  - The UI's "template" editing of a string input stores the input as `type: javascript` with a backtick template-literal expression, such as `` `Hello ${flow_input.name}` ``. When it reloads, the UI shows an expression that starts with a backtick as a static template.
  - Templates are therefore ordinary JavaScript expressions ([JE-10](#je-10) to [JE-21](#je-21)), and they get the `results` rewrite inside `${}`.
  - A static string holding a `$var:`/`$res:` reference is recorded as written (O2) and resolved only at O3 ([JE-41](#je-41)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2745-2748`; UI behaviour `frontend/src/lib/components/InputTransformForm.svelte:186-198`, `:203-215`, `:273-275`; test `backend/windmill-jseval/src/lib.rs:1371-1388`.
- **Conformance:** [JE-40/a, JE-40/b](conformance.md#je-40)

<a id="je-41"></a>
### JE-41 · `$var:`, `$res:` and related strings are resolved per leaf step, at argument-build time

**Scope:** core (resolution point); later (`$jsonvar:`, `$encrypted:`) · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Where:** strings that reference variables and resources are **not** resolved by the expression evaluator or by flow transforms. They are resolved when a **leaf step job** (a script) builds its arguments, just before it runs.
  - **What:** every top-level argument of that job, wherever it came from: a static transform, an expression result (an expression returning `"$var:u/x/y"` is resolved too), or a `flow_input` value passed through.
  - **What expressions see:** the **unresolved** strings. For example, `flow_input.db` is the literal `"$res:f/x/db"`. The one exception is `flow_env` ([JE-38](#je-38)).
  - **Not resolved on the way:** a loop module's iterator values, branch predicates, and subflow inputs at the subflow boundary. Each is resolved later, in whichever leaf step finally receives it.
  - **Recorded arguments keep the markers.** Resolution writes a separate, per-attempt map or arguments file (O3) and never updates the job's stored arguments (O2) ([JE-V3](#je-v3)). A retry resolves the markers again ([JE-V2](#je-v2)).
  - **No `$secret:` prefix exists.** The recognised markers are `$var:`, `$jsonvar:`, `$res:` and `$encrypted:` ([JE-42](#je-42)).
- **Evidence:** `backend/windmill-worker/src/common.rs:80-89`, `:121-150`, `:162-210`; `backend/windmill-worker/src/worker_flow.rs:4269-4283`, `:4403-4436`; expression scope: the [JE-22](#je-22) sources.
- **Conformance:** [JE-41/a–c](conformance.md#je-41)

<a id="je-v3"></a>
### JE-V3 · Recorded arguments and resolved arguments are distinct observation points

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - The job API's arguments are the arguments stored **before** leaf interpolation. Resolution produces a separate map or arguments file and does not update the job's arguments row.
  - A fixture therefore cannot both observe "the received argument" through the job API and expect `$var:`/`$res:` replacement. Fixtures name their observation point: evaluator output (O1), recorded args (O2), resolved args (O3), script observation (O4), or flow and job outcome (O5); see [Conventions](#conventions).
  - Later observation points can still carry a computed consequence of a pre-database value ([JE-16](#je-16)), so a case at O2 to O5 that depends on a memory or database read says which read it uses.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:4269-4283`, `:4403-4436`; `backend/windmill-worker/src/common.rs:80-89`, `:121-150`, `:174-210`.
- **Conformance:** [JE-V3/a](conformance.md#je-v3)

<a id="je-42"></a>
### JE-42 · The resolution rules

**Scope:** core (`$var:`/`$res:` with nesting); later (other rules, depth guard) · **Confidence:** high (oracle-observed); high (source-traced: serde's positions, `$encrypted:`) · **Status:** confirmed; drafted (the position in serde's messages, the `$encrypted:` details, the `CUSTOM_INSTANCE_DB/` note)

- **Rule:**
  1. **Pre-check per argument.** A top-level argument is examined only if its JSON text contains `$var:`, `$jsonvar:`, `$res:` or `$encrypted:` *anywhere*. Arguments without any marker pass through untouched.
  2. **Walk.** The argument is parsed and walked. Only a string that **starts with** a marker is replaced; `"x $var:a"` stays as it is.
     - `$var:<path>`: the variable's value as a JSON string.
     - `$jsonvar:<path>`: the variable's value parsed as JSON. On failure: `Failed to parse $jsonvar value as JSON: …`.
       - **Position in serde's messages** (drafted). The parser's message ends `at line L column C`. `L` is 1 plus the number of line feeds before the point where the parser stopped. `C` counts **bytes**, not characters, from the start of that line up to and including the byte it stopped at; at the end of the text that is the last byte, and `C` is 0 when nothing precedes the end on that line (an empty variable gives `EOF while parsing a value at line 1 column 0`; a text ending in a line feed, `line 2 column 0`). A line feed the parser stops at counts as the start of the next line, at column 0: a raw line feed inside a string (the text `"a`, a line feed, `b"`) gives `control character (\u0000-\u001F) found while parsing a string at line 2 column 0`. A carriage return is an ordinary byte (the same string with CRLF stops at the CR, `line 1 column 3`). Checked against serde_json 1.0.151: `hello` gives `expected value at line 1 column 1`; `{"n": 1e400}` gives `number out of range at line 1 column 11` (the number's last digit) and `{"é": 1e400}` column 12, `é` being two bytes; `{"s": "\ud83d"}` gives `unexpected end of hex escape at line 1 column 14` (the byte after the escape); `{"a":1} x` gives `trailing characters at line 1 column 9`; `[1,2` gives `EOF while parsing a list at line 1 column 4`, `[` gives `EOF while parsing a list at line 1 column 1` and `[1,` gives `EOF while parsing a value at line 1 column 3` (the EOF message says `a list` or `an object` for an unclosed container, and `a value` where a value was due); and in a pretty-printed text whose third line is `  "b": 1e400`, `number out of range at line 3 column 12`. The same message and position appear wherever Windmill's backend parses JSON text with this library, for example in the server's interpolation ([WA-12](client-api.md#wa-12)) and in `$encrypted:` below.
     - `$res:<path>`: the resource's interpolated JSON value. The path must have at least 2 `/`-separated segments (`CUSTOM_INSTANCE_DB/…` excepted, an exception that never changes the outcome: see the revision of 2026-10-29); otherwise ``Argument `<name>` is an invalid resource path: <path>``.
     - `$encrypted:<blob>`: a decrypted value. This marker is internal.
       - **Details** (drafted). The blob is base64 of AES-256 ciphertext under a key built from the workspace key (with the instance's `SECRET_SALT` appended when it is set) **and the id of the step's root job**. Only a value Windmill encrypted for that same root job decrypts (an app's password input is one producer); a hand-written `$encrypted:` value, or a secret variable's stored ciphertext, does not. The decrypted text must be JSON and is substituted parsed. A failure fails the step with an internal error, stored in the execution envelope of [FX-51](execution.md#fx-51) rule 5 (`execution error:\n` then the text):
         - `Internal: Could not decrypt value. The value may have been encrypted with a different key. @<loc>` when the blob is base64 but not ciphertext under that key: another key, a decoded length that is not a whole, non-zero number of 16-byte blocks, or bad padding after decryption;
         - `Internal: <decoder message> @<loc>` when the blob is not canonical padded base64. The messages are `Invalid byte <b>, offset <i>.`, `Invalid padding`, `Invalid last symbol <b>, offset <i>.` and `Encoded text cannot have a 6-bit remainder.`, where `<b>` is the byte's decimal value and `<i>` its 0-based offset: `$encrypted:secret` gives `Invalid padding`, `$encrypted:not!base64` gives `Invalid byte 33, offset 3.`, while `$encrypted:abcd` decodes to 3 bytes and gives the first message;
         - `Internal: invalid utf-8 sequence of <n> bytes from index <i> @<loc>` (or `incomplete utf-8 byte sequence from index <i>`) when another key happens to leave valid padding and the result is not UTF-8;
         - `Internal: Failed to decrypt '$encrypted:' value: <serde message> @<loc>` when the decrypted text is not JSON.

         `<loc>` is one location suffix; at the pin it is `@variables.rs:265:45` for the first message, `@variables.rs:269:14` for the decoder and UTF-8 messages and `@common.rs:346:29` for the last. The decrypted text, and the string inside it, are registered for log masking. A worker that reaches the server over HTTP instead of the database cannot decrypt, and fails with `Not found: Http connection not supported`.
     - **Any other string starting with `$`,** within an argument that passed the pre-check: a lookup by name among the job's reserved variables (`WM_WORKSPACE`, `WM_JOB_ID` and others; [languages](languages.md) lists them). An unknown name is left as is. The same `$WM_JOB_ID` string in an argument *without* any marker is not replaced.
  3. **Depth limits.**
     - Arrays are walked only at nesting depth ≤ 2 (the top-level value is depth 0) and only when they have ≤ 1000 items. Deeper or larger arrays pass through unresolved.
     - **Any** value reached at depth 50 or more, of any type, fails the step; the check happens before the value's type is looked at. The message is ``Maximum resource/variable interpolation depth (50) exceeded for `<name>`; this usually indicates a circular `$res:` or `$var:` reference``. Since arrays beyond depth 2 are never walked, in practice the guard is reached only through nested objects.
  4. **Errors fail the step.** ``Variable <path> not found for `<name>`: <cause>`` and ``Resource <path> not found for `<name>`: <cause>`` are "not found" errors. `<name>` is the innermost object key, or the argument name at the top level.

  Resolved values are observed at O3.
- **Evidence:** `backend/windmill-worker/src/common.rs:162-210`, `:260-399` (depth guard `:273-280`); tests `:2275-2345`. Serde's positions: `serde_json` 1.0.151 (`backend/Cargo.lock:11151-11154`) `src/read.rs:421-430` (line and byte column of an index), `:573-581` (the position of the byte read last, or of the byte looked at). `$encrypted:`: `backend/windmill-worker/src/common.rs:320-358` (the key with the root job id, the parse, the masks, the HTTP refusal); `backend/windmill-common/src/variables.rs:150-161` (the key: workspace key, salt, suffix), `:263-271` (the decryption error texts); magic-crypt 3.1.13 (`backend/Cargo.lock:7284-7287`) `src/traits.rs:75-90` (base64 first, then decryption, then UTF-8), `src/errors.rs:50-61` (each cause's own message); base64 0.21.7 `src/engine/general_purpose/mod.rs:257-264`, `:332`, `:347` (canonical padding required), `src/decode.rs:30-41` (the messages); block-modes 0.8.1 `src/traits.rs:68-75` and block-padding 0.2.1 `src/lib.rs:167-182` (a length that is not a whole number of blocks, or bad padding, is the one decryption error). `CUSTOM_INSTANCE_DB/`: `backend/windmill-worker/src/common.rs:305`.
- **Conformance:** [JE-42/a–j](conformance.md#je-42)
- **Revision 2026-10-29 (SC3, source re-check, chain PF2):** milestone PF2 chose its own texts for `$encrypted:` and for ciphertext, and read serde's positions where FX-51/c showed none. Added the `$encrypted:` key, its failure texts and the position rule of serde's messages, with new cases JE-42/i and JE-42/j (not yet recorded, [Q-JE-17](#q-je-17)). Also: the `$res:` path check names `CUSTOM_INSTANCE_DB/` as an exception, but a path with that prefix always contains a `/`, so the exception changes nothing; what the server does with such a path is [WA-12](client-api.md#wa-12)'s.

<a id="je-43"></a>
### JE-43 · Secrets, and where a host secret layer could plug in

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Windmill has no `$secret:` marker. Secrets are variables marked secret, read through `$var:` or `variable()`.
  - Secret variables register their values for log masking when they are read; `$encrypted:` values register masks too. Masking is best effort for each log transport: it is not a guarantee that every secret is masked everywhere.
- **Evidence:** `backend/windmill-worker/src/common.rs:162-164`, `:320-355` (where `$encrypted:` registers log masks); `backend/windmill-store/src/variables.rs:1476-1482`, `:1539`.
- **Conformance:** none (the extension point below is not Windmill behaviour).
- **Design note (not Windmill behaviour):** a windmill-lite host secret resolver would need to cover the leaf-step argument resolution of [JE-41](#je-41) and [JE-42](#je-42) (including the pre-check), `variable()` and `resource()` inside expressions ([JE-20](#je-20)), and `flow_env` resolution ([JE-38](#je-38)). A new prefix would be a windmill-lite extension and must not change how the existing markers behave.

## F. JSON at the database boundary

<a id="je-44"></a>
### JE-44 · Which reads go through `jsonb`

**Scope:** core · **Confidence:** medium (its cases oracle-observed) · **Status:** confirmed; drafted (the 2026-10-28 additions for design request 8, except the rows batch E3e recorded; the identity row of 2026-10-29)

- **Rule:**
  - **What `jsonb` does.** Windmill stores job arguments and job results in Postgres `jsonb` columns, which normalise them:
    - **object keys are reordered**: shorter keys first, then bytewise ([Q-JE-9](#q-je-9));
    - duplicate keys collapse, and the last one wins;
    - whitespace is normalised;
    - numbers keep their exact value but **not their spelling**: exponents are expanded (`1e3` becomes `1000`), while plain decimals keep their digits, including trailing zeros (`1.50`).
  - **Reads that go through the database:**
    - `results.<id>` for steps other than the previous one ([JE-18](#je-18) rules 2 and 3, and [JE-9](#je-9));
    - **any** qualifying [JE-9](#je-9) path, even for the previous step ([JE-V6](#je-v6));
    - P after a loop or `branchall` module;
    - the re-fetch fallback for P ([JE-36](#je-36));
    - the nested values of `flow_input`, because arguments are loaded from the job row;
    - P after an identity job (an `identity` module, a step that `skip_if` skipped, a mocked step): the job's result is its own `previous_result` argument as read back from the job row, though it then reaches the next step in memory.
  - **Reads that do not:**
    - P after an ordinary step, which is raw text held in memory;
    - `results.<previous id>` through the whole-result spellings of [JE-8](#je-8) or rule 1 of [JE-18](#je-18), which return P.
  - **Top-level key order** of `flow_input` and of a script's arguments is unstable ([JE-16](#je-16)); fixtures must not depend on it.
  - As a result, `Object.keys(previous_result)` and `Object.keys(results.<same step>)` can differ in order.
  - **The text a shortcut returns.** [JE-6](#je-6) to [JE-9](#je-9) hand their value on as text, without the engine, so its exact spelling is observable wherever a site quotes it (the `Value was:` of [JE-29](#je-29)) and wherever it becomes a step's arguments. That text depends on how the value reached the worker:

    | Value | Text |
    |---|---|
    | P after an ordinary step whose job completed in this transition | the producer's text, as the job returned it ([JE-8](#je-8), oracle-observed) |
    | a member of `flow_input` ([JE-7](#je-7)) | PostgreSQL's printed form of the stored `jsonb`: `": "` after each key and `", "` between members and items, keys in `jsonb` order, numbers in `numeric`'s spelling (oracle-observed for an object member, at the first module and after one, JE-44/d) |
    | P at the first module of a flow or body, and in the preprocessor | a compact object the worker writes from the flow arguments: no spaces at the top level, top-level keys in an unstable order ([JE-16](#je-16)), each member value in `jsonb`'s printed form as in the row above |
    | P after a loop or `branchall` | a compact array the worker writes after decoding each child's stored result: no spaces at any level, object keys in `jsonb` order (the decoder keeps insertion order), and numbers re-printed by the backend's JSON library. An integer that fits in 64 bits stays exact; any other number becomes the nearest double in its shortest form, so a stored `1.50` reads `1.5` ([FX-42](execution.md#fx-42) rule 2 gives the spelling, decimal or exponent). The compact text and the `jsonb` key order are oracle-observed (JE-44/e); the re-printed numbers are source-traced |
    | P after an identity job: an `identity` module, a step that `skip_if` skipped, a mocked step ([JE-23](#je-23)) | `jsonb`'s printed form of the value passed on (P, or the mock's `return_value`). The worker pushes the identity job with the arguments `{"previous_result": <that text>}`, the job row stores them as `jsonb`, the job is pulled with its arguments decoded member by member from that row, as for a flow input, and its result is the `previous_result` member's text as decoded. That text then travels in memory, so the next step sees it, not the producer's text (source-traced) |
    | P read back from storage: the re-fetch fallback ([JE-36](#je-36)), after a suspend or a restart from a step, and when a retry re-evaluates its predicates | that one job's stored result in `jsonb`'s printed form |
    | a [JE-9](#je-9) lookup | `jsonb`'s printed form (oracle-observed, the discriminator of [JE-8](#je-8)) |
    | P after an empty chosen branch | the JSON string `"{}"` ([OR-25](oracle.md#or-25)) |

    A static or JavaScript transform that passes one of these values on stores it as the step's arguments, where `jsonb` normalises it again, so the difference shows only where a text is quoted or compared before storage.
- **Evidence:** column types `backend/migrations/20220123221903_first.up.sql:130`, `:161-162`, `backend/migrations/20250201124431_v2_job.up.sql:31`; in-memory passing of the last result `backend/windmill-worker/src/worker_flow.rs:1477-1488`, `:2086-2099`; `backend/windmill-jseval/src/lib.rs:805-817`; `backend/windmill-worker/src/js_eval.rs:54-72`; [PostgreSQL JSON types](https://www.postgresql.org/docs/current/datatype-json.html) ("jsonb does not preserve white space, does not preserve the order of object keys, and does not keep duplicate object keys").
- **Conformance:** [JE-44/a–f](conformance.md#je-44)
- **Evidence (the text a shortcut returns):** the flow input decoded per member from `v2_job.args` `backend/windmill-queue/src/jobs.rs:2754-2758`, `:3231-3241`, and used as is `backend/windmill-worker/src/worker_flow.rs:3186-3194`; P at the first module `:3324-3331`, `:3779-3783` (the arguments written out again); P after a loop or `branchall` `:2299-2326` (each child's result decoded as a JSON value and the array written out again), used at `:1477-1488`, `:371`, `:695`; P read back from storage `:3333-3346`, `:3769-3792`, `:6234-6264` (one job's result decoded as raw text); the backend's JSON library with `preserve_order` and without `arbitrary_precision` `backend/Cargo.toml:435`. PostgreSQL prints `jsonb` with a space after `:` and `,` in its text output, which is also what its binary form carries after the version byte (outside Windmill; the spaced text is oracle-observed through JE-9). P after an identity job: the arguments built `backend/windmill-worker/src/worker_flow.rs:3874-3889` (a mock), `:3918-3926` (P written into `{"previous_result": …}` and read back as raw member texts), the identity payload pushed `:5106-5141`; the job pulled with its arguments decoded per member from `v2_job.args` `backend/windmill-queue/src/jobs.rs:2754-2758`, `:3231-3241` (a job handed to the same worker is still pulled by id, `:7271-7276`); its result is that member's text `backend/windmill-worker/src/worker.rs:4032-4038`, forwarded raw to the next transition `backend/windmill-worker/src/result_processor.rs:1490-1531`. Recorded: runs `fi_first`, `fi_later` and `loop_prev` of `conformance/flows/je/shortcut-texts` (oracle run 40794872025).
- **Revision 2026-10-28 (source re-check, design request 8):** added the table of returned texts. windmill-lite's textual pipeline (B2a) reads every value from storage as the compact stored text. That reading is **corrected**: a `flow_input` member and a `previous_result` read back from storage are spaced, and a `previous_result` after a loop or `branchall` is compact but has its numbers re-printed. Only the producer's text and the compact top level at the first module agree with the reading as it stands. B2b's tests that pin these texts must change. Not yet recorded ([Q-JE-13](#q-je-13)): two runs settle the first and fourth rows, `timeout: flow_input.o` with `o` an object, and `timeout: results.l` right after a loop `l` whose iterations return `1.50`; their parse errors quote the text.
- **Revision 2026-10-29 (oracle, batch E3e):** the two runs request 8 asked for were recorded, and both agree with the 2026-10-28 table. `timeout: flow_input.o`, with `o` submitted as `{"b": 1, "a": 2}`, quotes `{"a": 2, "b": 1}`, spaced and in `jsonb` order, both at the first module and after a step; `timeout: results.l` right after a loop whose iteration returns `{"b":1,"a":2}` quotes `[{"a":2,"b":1}]`, compact and in `jsonb` order. The loop's iteration returned integers, so the re-printing of other numbers (`1.50` → `1.5`) is still source-traced. Added cases JE-44/d and JE-44/e (oracle-observed).
- **Revision 2026-10-29 (source re-check, chain N1a):** added the row for P after an identity job. Its text is `jsonb`'s printed form, because the identity job's result is its own argument read back from the job row. This confirms the reading milestone N1b took ("decoded member by member, as a flow input's are") and corrects milestone N1a's earlier reading, a compact text. Added case JE-44/f (drafted, source-traced).

<a id="je-v6"></a>
### JE-V6 · The exact spelling, not the module id, decides memory or database

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** whether an expression reads the in-memory previous result, the database path lookup or the JavaScript proxy depends on its **exact spelling**, tested in the dispatch order JE-6 → JE-7 → JE-8 → JE-9 → QuickJS:
  - **a whole-result spelling** of the previous step ([JE-8](#je-8)'s four forms) returns raw P from memory;
  - **a longer qualifying path,** such as `results.a.n` with `a` the previous step, is not a JE-8 form and goes to the [JE-9](#je-9) **database** lookup;
  - **the same path in parentheses** goes to QuickJS, where the proxy supplies P from memory ([JE-18](#je-18) rule 1) and converts it to JavaScript values ([JE-16](#je-16)).

  "Previous step" alone therefore does not decide the read path in [JE-44](#je-44). This is not a new lookup mechanism.
- **Evidence:** ordered dispatch `backend/windmill-worker/src/js_eval.rs:44-85` (whole-result shortcut `:54-72` before path lookup `:74-79`); path lookup `backend/windmill-jseval/src/lib.rs:184-218`; previous-value proxy `:805-817`.
- **Conformance:** [JE-V6/a, JE-V6/b](conformance.md#je-v6)

## G. Other evaluators and runtimes (boundaries)

<a id="je-45"></a>
### JE-45 · The simple evaluator outside flows

**Scope:** later (standalone-script `retry_if`); out (batch rerun) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - A second, simpler evaluation mode serves two callers: **standalone-script `retry_if`** (outside flows) and the **batch-rerun** argument remapping in the API.
  - It is synchronous: no rewrites, no `await`, no `results`, no helpers, no `error` variable, no shortcuts. It uses a 32 MiB heap cap with no override, and the 20 s limit with no retry.
  - It evaluates `JSON.stringify((<expr>) ?? null)` with the given globals. For `retry_if` those are `result`, `previous_result` and `flow_input` = the job arguments, under a `Boolean(...)` wrapper.
  - If `retry_if` throws there, the script is **retried anyway**.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:944-995` (timeout `:946-964`); `backend/windmill-queue/src/jobs.rs:1647-1690`; `backend/windmill-api/src/jobs.rs:4549-4560`.
- **Conformance:** none for flows.

<a id="je-46"></a>
### JE-46 · `windmill-runtime-nativets` is not the expression engine

**Scope:** out · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - `backend/windmill-runtime-nativets/` embeds `deno_core` (V8) to run **`nativets` scripts**: the in-worker "native" TypeScript runtime, with its own `windmill-client.js` and fetch support. Its behaviour belongs to [languages](languages.md) ([WR-40](languages.md#wr-40) to [WR-46](languages.md#wr-46)); its stack limit is separate from the expression engine's ([JE-48](#je-48)).
  - Flow expressions never run there. The only overlap is the worker file name `js_eval.rs`, which hosts both the expression entry point ([JE-6](#je-6) to [JE-9](#je-9)) and the `nativets` script entry point.
- **Evidence:** `backend/windmill-runtime-nativets/src/lib.rs:9-13`; `backend/windmill-worker/src/js_eval.rs:110-210`.
- **Conformance:** none.

<a id="je-47"></a>
### JE-47 · JavaScript transforms of AI-agent modules

**Scope:** out · **Confidence:** medium · **Status:** confirmed

- **Rule:**
  - AI-agent modules accept `type: javascript` for their provider and memory configuration, through the same JavaScript transform shape (`ProviderTransform`, `MemoryTransform` in OpenFlow), and are listed as a transform-bearing module kind for their ordinary input transforms.
  - AI-agent tool calls evaluate a tool module's input transforms in a separate context ([JE-22](#je-22), boundary).
  - This is a schema and boundary finding; it does not establish the execution semantics of provider or memory transforms. AI-agent execution is out of scope.
- **Evidence:** `openflow.openapi.yaml:499-512`, `:600-613`; `backend/windmill-worker/src/worker_flow.rs:3930-3933`; `backend/windmill-worker/src/ai/tools.rs:337-368`.
- **Conformance:** none.

## Open questions

None of these blocks a source-derived core rule; they confirm exact text or end-to-end behaviour on a real Windmill v1.767.0. Recording batch E3a answered several of them on 2026-10-28, and batch E3e with a source re-check answered parts of others on 2026-10-29; answered questions keep their numbers.

<a id="q-je-1"></a>
- **Q-JE-1** · affects [JE-12](#je-12), [JE-21](#je-21), [JE-9](#je-9) · **Re-scoped 2026-10-28.** Batch E3a recorded the stack text appended after `Error: <message>`, the syntax diagnostic for a rewritten `await` inside a non-async function (`Unexpected identifier 'results'`, JE-12/d) and the non-callable diagnostic of [JE-9/l](conformance.md#je-9) (`not a function`); [JE-21](#je-21) now states them. Still open: the display of a thrown non-Error value, the `Exception generated by quickjs: …` form. **Re-scoped again 2026-10-28 (validation round 2):** the source now settles its form per type ([JE-21](#je-21)): `String(Ok("s"))`, `Int(1)`, `Float(1.5)`, `Bool(true)`, `BigInt"BigInt"`, and for objects, arrays, functions and symbols a type name with a memory address that no recording can reproduce. What remains is to record the primitive forms. *Settled by:* an oracle run of [JE-21/b](conformance.md#je-21) as an expression case (recording batch E3e), with `throw "s"`, `throw 1`, `throw 1.5` and `throw true`; a `throw {}` recorded beside them should match only `Exception generated by quickjs: Object(0x`. *2026-10-29:* batch E3e did not record this (its expression fixtures recorded the shortcut texts and the transform order); still open. *2026-10-29 (SC3):* the source already gives every form; only the recording remains, requested from batch E3f.

<a id="q-je-2"></a>
- **Q-JE-2** · affects [JE-1](#je-1) · **Re-scoped 2026-10-28.** Batch E3a recorded `typeof` of `console`, `Intl`, `atob`, `btoa`, `TextEncoder`, `TextDecoder`, `URL`, `URLSearchParams`, `fetch`, `setTimeout`, `process` and `require`: all `"undefined"`, as [JE-1](#je-1) says (the `absent-host-apis` case of `conformance/exprs/je-1.yaml`). Still source-derived: `queueMicrotask`, `performance`, `DOMException`, `WeakRef`, `FinalizationRegistry` and `Iterator` present, `structuredClone` and `navigator` absent. *Settled by:* one oracle expression returning the `typeof` of each of these names. *2026-10-29:* batch E3e did not record this; still open. *2026-10-29 (SC3):* oracle only; requested from batch E3f.

<a id="q-je-3"></a>
- **Q-JE-3** · affects [JE-35](#je-35) · **Answered 2026-10-28.** Batch E3a recorded [JE-35/a–d](conformance.md#je-35) (`conformance/flows/je/errors`): the routes and which failure module runs hold as traced; the route-(b) envelope has two `Error: ` prefixes and two location suffixes, and [JE-35](#je-35) and JE-35/b were corrected.

<a id="q-je-4"></a>
- **Q-JE-4** · affects [JE-V2](#je-v2), [JE-23](#je-23) · **Answered 2026-10-28.** Yes: in the recording of [JE-V2/c](conformance.md#je-v2) (`conformance/flows/je/waits`, run `je-v2-c`) the first attempt failed with the transform error and the retry ran with `{}`.

<a id="q-je-5"></a>
- **Q-JE-5** · affects [JE-36](#je-36) · **Re-scoped 2026-10-28.** Batch E3a recorded [JE-36/a–f](conformance.md#je-36) on deployed flows, all as traced. Still open: the simple while loop of JE-36/d in a **preview** run. *Settled by:* running JE-36/d through the flow-preview API (recording batch E3e). *2026-10-29:* batch E3e deferred the preview half with no milestone: windmill-lite has no preview mode, so the run is outside its equivalence contract. The question stays open for the reference's completeness only. *2026-10-29 (SC3):* oracle only; listed for batch E3f as optional, for completeness.

<a id="q-je-6"></a>
- **Q-JE-6** · affects [JE-V1](#je-v1) · What eventually happens to a parallel loop with `parallelism: 0` after every child is held at enqueue (held for 14 days, cancelled, or released by a monitor)? The initial hold is source-settled; a permanent stall is not asserted. *Settled by:* tracing the suspension timeout and monitor paths ([control flow](control-flow.md)), or an oracle run that observes the loop.

<a id="q-je-7"></a>
- **Q-JE-7** · affects [JE-33](#je-33) · What is the exact fractional-second format of `WM_SCHEDULED_FOR`? *Settled by:* scheduled oracle runs at known timestamps, one with no fraction and some with controlled fractional values, whose `skip_expr` compares `WM_SCHEDULED_FOR` against complete candidate strings (only `skip_expr` can read the name), or the pinned chrono `Display` source. Predicates such as `WM_SCHEDULED_FOR.endsWith(' UTC')` and `WM_SCHEDULED_FOR.includes('.')` show the suffix and whether a fraction is present, but not the digit count.

<a id="q-je-8"></a>
- **Q-JE-8** · affects [JE-19](#je-19) · **Answered 2026-10-29 (SC3)** from the source, for the reference's completeness ([JE-19](#je-19), new case [JE-19/f](conformance.md#je-19)): both, by route. The restarted run's status holds the modules before the restart point, copied with their original jobs, so a read the engine answers ([JE-18](#je-18) step 2) is served by those entries; its map of completed modules starts empty, so the line-anchored direct lookup and step 3 reach the original-run fallback for any module not completed again. An oracle restart would only corroborate; batch E3e deferred it as outside the contract. Original question: Do reads in a restarted run reach the original-run fallback, or are they already served by module statuses copied into the restarted run? *Settled by:* an oracle restart of [JE-19/e](conformance.md#je-19) in which `b` fails only in the original run and succeeds on restart, so `c` is reached, plus inspection of the restarted run's flow status through the API. The value `17` alone does not distinguish the two paths: to exercise the fallback itself, read a node absent from the restarted status, and keep the two assertions separate. *2026-10-29:* batch E3e deferred [JE-19/e](conformance.md#je-19) with no milestone: windmill-lite has no restart, so the run is outside its equivalence contract, and the harness drives no restart. (Closed by the SC3 answer above.)

<a id="q-je-9"></a>
- **Q-JE-9** · affects [JE-44](#je-44) · Does the `jsonb` key ordering (shorter keys first, then bytewise) hold for every key set the fixtures use? *Settled by:* Postgres alone (`SELECT '<json>'::jsonb`) on the oracle's Postgres version; no Windmill is needed. *2026-10-29:* batch E3e's recordings agree for the key sets they use: `{"bbb", "a", "cc"}` reads back as `a`, `cc`, `bbb` (`conformance/flows/oracle/probes`, run `key_order`; `conformance/flows/oracle/key-order`, run `pre_keys`), and `{"b", "a"}` as `a`, `b` (`conformance/flows/je/shortcut-texts`). Still open as a general claim. *2026-10-29 (SC3):* the Windmill source cannot answer this: the order is PostgreSQL's, which Windmill never sorts itself. Requested from batch E3f as a database-only check (multi-byte UTF-8 keys, keys that differ only in length, integer-like keys).

<a id="q-je-10"></a>
- **Q-JE-10** · affects [JE-19](#je-19) · **Answered 2026-10-28.** Yes: in the recording of [JE-19/d](conformance.md#je-19) (`conformance/flows/je/scopes`, run `je19-d`) the subflow's step read the caller's result `7`.

<a id="q-je-11"></a>
- **Q-JE-11** · affects [JE-48](#je-48), [JE-49](#je-49) · **Answered 2026-10-28.** Batch E3a recorded [JE-48/a–d](conformance.md#je-48) and [JE-49/a–c](conformance.md#je-49). The overflow message is wrapped as JE-23 says, and a 128-level step result reaches the binding unchanged. Two corrections followed: a recursion of depth 1 000 already overflows on Windmill (depths up to 600 fit), and the catalogue's JE-48 expressions run in statement mode, so JE-48/c gives `null`. Whether another engine build must reproduce the depth-1 000 overflow is left to the equivalence contract ([JE-48](#je-48)'s design note).

<a id="q-je-12"></a>
- **Q-JE-12** · affects [JE-50](#je-50), [JE-51](#je-51), [JE-52](#je-52) · open (oracle) · Do strict mode, the job-queue rules and the unpaired-surrogate rules hold end to end? *Settled by:* recording [JE-50/a](conformance.md#je-50), [JE-51/a](conformance.md#je-51) and [JE-52/a](conformance.md#je-52) as expression cases on the oracle, together with an expression whose job chain never ends after its value settled (expected: the timeout of JE-2, or the monitor cancel), and the evaluator output (O1) of `"\ud83d"` from an evaluator-level check with rquickjs 0.11.0 outside Windmill. *2026-10-29:* batch E3e did not record these; still open. *2026-10-29 (SC3):* the source half is already in the findings; the recordings are requested from batch E3f.

<a id="q-je-13"></a>
- **Q-JE-13** · affects [JE-7](#je-7), [JE-8](#je-8), [JE-11](#je-11), [JE-44](#je-44) · open (oracle) · Do the texts the shortcuts return for values that reached the worker from storage match the source trace, and what does a non-ASCII argument do to the call rewrite? *Settled by:* oracle runs, as flow fixtures, that quote the text through the `timeout` site's parse error ([JE-29](#je-29)): `timeout: flow_input.o` with `o` an object of two keys and a nested array (expected: `jsonb`'s spaced text); `timeout: results.l` right after a loop `l` whose iterations return `{"b":1.50,"a":1}` (expected: a compact array, `1.5`, keys `a` before `b`); and transforms `variable("f/é")` and `variable("f/éé")` against existing variables (expected: a `SyntaxError` for the first and a worker panic for the second, whose job-level outcome the recording shows). **Re-scoped 2026-10-29.** Two parts are answered. (1) **The texts:** batch E3e recorded the two discriminators (`conformance/flows/je/shortcut-texts`, runs `fi_first`, `fi_later` and `loop_prev`, oracle run 40794872025), and both agree with [JE-44](#je-44)'s table: a stored flow input's object member is quoted spaced and in `jsonb` order, at the first module and after a step, and the loop's aggregate right after the loop compact and in `jsonb` order ([JE-44/d](conformance.md#je-44), [JE-44/e](conformance.md#je-44)). The loop's iteration returned integers, so the re-printed `1.50` was not exercised. (2) **The panic's outcome** is now traced in the source ([JE-11](#je-11)): the runtime catches the panic, and the evaluation fails at once with `task <n> panicked with message "assertion failed: self.is_char_boundary(idx)"`, routed like any evaluation error of its site; the worker is unaffected. Still open (oracle): a loop aggregate whose iterations return `{"b":1.50,"a":1}` (expected `[{"a":1,"b":1.5}]`), and [JE-11/c](conformance.md#je-11) and [JE-11/e](conformance.md#je-11) end to end. *2026-10-29 (SC3):* milestone N3a asked whether the re-printed aggregate writes 10²¹ as `1e+21` or `1e21`. The backend writes `1e+21` (re-checked against the pinned float formatter, [FX-42](execution.md#fx-42) rule 9), and that spelling is already recorded: [FX-42/h](conformance.md#fx-42)'s arguments file, written by the same JSON library, holds `"b":1e+21` (oracle run 40686049913). So the spelling needs no run of its own; the loop site is [FX-42/j](conformance.md#fx-42), whose recording is requested with the rest for batch E3f ([docs/design/README.md](../design/README.md)). A Bun iteration cannot return `1.50` (its `JSON.stringify` writes `1.5`), so the scale part of the aggregate run needs an iteration value that reaches storage without passing through a JavaScript or Python parse.

<a id="q-je-14"></a>
- **Q-JE-14** · affects [JE-15](#je-15) · open (oracle) · When an input transform replaces `JSON.stringify` so that the evaluator's result text is not JSON, what does the step do when its arguments are stored? The source shows the text kept unchecked; the storage outcome is not traced. *Settled by:* recording [JE-15/j](conformance.md#je-15) together with a second transform `x = (JSON.stringify = () => "hello", 1)` on a step, recording the step's and the flow's status and error. *2026-10-29:* batch E3e did not record this; still open. **Re-scoped 2026-10-29 (SC3).** The storage outcome is now traced ([JE-15](#je-15), new case [JE-15/k](conformance.md#je-15)): the arguments are written with the text unchanged, PostgreSQL's `jsonb` input refuses `{"x":hello}` with `invalid input syntax for type json`, and the step's push fails, routed like [FX-48](execution.md#fx-48) case 2 (`Anyhow` at the root's first pickup, the chaining `InternalError` at a later transition). What remains is the recording of JE-15/j and JE-15/k, requested from batch E3f.

<a id="q-je-15"></a>
- **Q-JE-15** · affects [JE-49](#je-49) · open (oracle) · Can a run reach the binding with a `flow_input` or a `flow_env` member deep enough to trip the 127-level limit, or does an earlier parse (the run request, the deployment) refuse it first; and does the reported position then count as [JE-49](#je-49) says? *Settled by:* an oracle run starting a one-step flow with input `{"k": <127 nested arrays>}` and a transform `x = flow_input.k.length` (expected, if the input is accepted: `recursion limit exceeded at line 1 column 132`), and a deployed flow with a `flow_env` member 128 levels deep read by `x = flow_env.K.length`. *2026-10-29:* batch E3e did not record this; still open. **Re-scoped 2026-10-29 (SC3).** The first half is traced: no earlier parse refuses either value, since the run API and the deployment keep such members as raw text read without a depth budget, so both reach the binding ([JE-49](#je-49), new cases [JE-49/d](conformance.md#je-49) and [JE-49/e](conformance.md#je-49)). What remains is the recording of those two cases, requested from batch E3f.

<a id="q-je-16"></a>
- **Q-JE-16** · affects [JE-1](#je-1), [JE-48](#je-48) · open (oracle) · Which C compiler, at which version, compiled the engine in the pinned CE image, and which glibc version does the runtime image ship? The source fixes the base image tags, the target, the `cc` crate's flags and that assertions are on ([JE-48](#je-48)), but not these. *Settled by:* an oracle run whose step prints `ldd --version` (the glibc version) and the compiler identifications in the `.comment` section of `/usr/src/app/windmill`, recorded on the pinned image digest (a Python step can read the ELF section without binutils; whether the build's `strip` kept that section is part of what the run shows). The engine's own compile flags beyond those the source fixes are only in the separate debug-information file of the build, not in the image. *2026-10-29:* batch E3e did not record this (its environment probe, `conformance/flows/oracle/probes` run `env`, reports the Bun version and the job's environment, not the compiler or the C library); still open. *2026-10-29 (SC3):* the Windmill source cannot answer this: it names the builder and runtime base images, not the compiler or C library versions inside them. Oracle only; requested from batch E3f.

<a id="q-je-17"></a>
- **Q-JE-17** · affects [JE-20](#je-20), [JE-42](#je-42) · open (oracle) · Added 2026-10-29 (SC3). Do the source-traced provider texts hold on the oracle: the `resource()` helper's read contract and error texts, the `$encrypted:` failures, and serde's positions in a `$jsonvar:` message? *Settled by:* recording [JE-20/c, JE-20/d](conformance.md#je-20) and [JE-42/i, JE-42/j](conformance.md#je-42) as flow fixtures in the style of E3a's `je/providers` (variables and resources created after the push, [OR-31](oracle.md#or-31)), on one worker. JE-42/i's positions need nothing beyond the variables; JE-20/d compares a `resource()` read with a static `$res:` argument of the same resource.

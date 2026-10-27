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

1. **One engine, one textual pipeline** ([JE-1](#je-1), [JE-5](#je-5), [JE-11](#je-11) to [JE-15](#je-15), [JE-24](#je-24)). Expressions that reach the engine run on QuickJS-ng (rquickjs 0.11.0) in a fresh runtime per evaluation, with no `Intl`, `atob`, `URL` or `console` (source-derived, [Q-JE-2](#q-je-2)). Behaviour comes from a fixed pipeline: predicates are wrapped untrimmed in `Boolean(…)`; the text is trimmed; the shortcuts are tried in the order JE-6 → JE-7 → JE-8 → JE-9; the `variable`/`resource`/`flow_user_state` and `results` rewrites are applied; expression or statement mode is chosen; the value is serialised as `JSON.stringify(x ?? null)`. Shortcuts return before any engine starts, so the engine limits (20 s per attempt, up to 3 attempts, 128 MiB; [JE-2](#je-2), [JE-3](#je-3)) do not apply to them.
2. **Shortcut dispatch is semantics, not optimisation** ([JE-6](#je-6) to [JE-9](#je-9), [JE-V6](#je-v6)). JE-6 to JE-8 return stored JSON verbatim. JE-9 inspects only the first line that matches its pattern: it can discard the rest of the expression, turns misses into `null` where JavaScript would throw, and can hand a predicate a non-boolean. The exact spelling decides whether a read comes from memory, from the database or from JavaScript: `results.a.n` reads the database even when `a` is the previous step.
3. **`results` handling is textual and lazy** ([JE-10](#je-10), [JE-12](#je-12), [JE-18](#je-18)). Only names written in the text are bound. `results['a']` and computed keys stay Promises, and `results` inside a nested non-async function is a syntax error.
4. **Scopes differ per site** ([JE-22](#je-22), [JE-V1](#je-v1), [JE-33](#je-33), [JE-39](#je-39)). `sleep`, `retry_if` and `skip_expr` have no `results`; stop predicates get `results` only if their text names it; input transforms have no `result`, and `params` is always `{}`; `parallelism` sees the containing input unchanged, so an outer `iter` stays visible. No site binds `env`, and no `WM_*` name exists except `WM_SCHEDULED_FOR` in flow `skip_expr`.
5. **Lookup failures are not all `null`** ([JE-9](#je-9), [JE-18](#je-18), [JE-19](#je-19)). The JE-9 path lookup and the JE-18 fallback lookup turn misses and failures into `null`, but a fetch for a module of the current flow that already has a job throws on failure. Parallel iterations and subflows called by path start new scopes; a collapsed loop records only the loop id; a running restarted flow can read its original run.
6. **`previous_result` is positional** ([JE-36](#je-36)). The first module of every flow job, and any module at index 0 (collapsed loops included), sees the job input. Collapsing applies to deployed sequential for-loops and to while loops (previews included), never to parallel loops. The re-fetch fallback produces the *string* `"{}"`.
7. **Transforms run serially, once per step** ([JE-23](#je-23), [JE-V2](#je-v2), [JE-V5](#je-v5)). A step's transforms are evaluated one at a time, in unspecified order, each in its own runtime, stopping at the first error. Ordinary step retries reuse the recorded arguments without re-evaluating the transforms; collapsed loop bodies evaluate their transforms per iteration. Isolation does not make concurrent evaluation, a shared runtime or shared read caching behaviour-preserving.
8. **Argument references are resolved per leaf step and per attempt** ([JE-41](#je-41), [JE-42](#je-42), [JE-V3](#je-v3)). `$var:`/`$res:` markers are resolved when a script job builds its arguments, into a map separate from the recorded arguments. Expressions see unresolved strings, except in `flow_env` ([JE-38](#je-38)). Recorded arguments, resolved arguments, child-job results and parent-flow errors are distinct observation points.
9. **Errors take five routes** ([JE-35](#je-35), [JE-26](#je-26)): chaining after a child completed (`InternalError`, failure module not run); first pickup of a root flow (failure module runs); first pickup of a nested flow (the parent handles it); ordinary step failures; and an unrecoverable string-valued error from `stop_after_if`, while the child job stays successful. The message wrappers are part of the contract; the Rust location suffix must be normalised before comparing.
10. **JSON boundaries depend on the read path** ([JE-16](#je-16), [JE-44](#je-44)). Reads from the database see `jsonb`: keys reordered, exponents expanded. The in-memory previous result keeps the producer's text, including `-0`, and its consequences are observable downstream. The top-level key order of `flow_input` and of script arguments comes from a hash map and is not stable.

Every finding below is source-confirmed at its stated scope and confidence. No case in this area has been recorded on the oracle yet; [Q-JE-1](#q-je-1) to [Q-JE-10](#q-je-10) are pending checks, not passed tests.

## Design notes (not Windmill behaviour)

- **Design note (not Windmill behaviour):** the cheapest faithful design is to run the same textual pipeline (item 1 above) on the same engine family wherever expressions are evaluated; most quirks ([JE-5](#je-5), [JE-11](#je-11) to [JE-15](#je-15)) then come for free. Evaluating expressions in Bun's JavaScriptCore would diverge (for example, `Intl` exists in Bun).
- **Design note (not Windmill behaviour):** implement the [JE-9](#je-9) line matcher as specified, not a JavaScript parser, and keep every shortcut condition: they change values, not only speed.
- **Design note (not Windmill behaviour):** the design of JSON at step boundaries must choose, and test, which key order each read sees ([JE-44](#je-44)), and must not turn the hash-map order of top-level keys into a contract ([JE-16](#je-16)).
- Further design notes sit in [JE-2](#je-2) (timeouts), [JE-V5](#je-v5) (batching transforms) and [JE-43](#je-43) (a host secret layer).

## A. Engine, isolation and limits

<a id="je-1"></a>
### JE-1 · QuickJS-ng evaluates every expression that reaches the engine

**Scope:** core · **Confidence:** high (missing-API list and `console`: medium, source-derived) · **Status:** confirmed

- **Rule:**
  - An expression that no shortcut ([JE-6](#je-6) to [JE-9](#je-9)) answers is evaluated by **QuickJS-ng**, embedded through the `rquickjs` crate at version `0.11.0`. That crate switched its base engine from Bellard's QuickJS to quickjs-ng in 0.9.0; its 0.11.0 changelog identifies quickjs-ng revision `be664ca1ccd86cbadf8df7348b8e15e77a32a1ba`.
  - No V8 or `deno_core` is involved. The `deno_core` runtime in `backend/windmill-runtime-nativets/` runs `nativets` scripts, not expressions ([JE-46](#je-46)).
  - Each evaluation that reaches the engine builds a **new runtime and a new full context** and discards both afterwards. Nothing survives from one evaluation to the next: no globals, no prototype patches, no caches. Each input transform is its own evaluation ([JE-23](#je-23)).
  - The context gets **no host APIs** beyond what the evaluator installs: the context values, `flow_input`/`flow_env`, the helpers and `error` ([JE-10](#je-10), [JE-11](#je-11), [JE-17](#je-17), [JE-20](#je-20)).
  - **Absent** (source-derived, [Q-JE-2](#q-je-2)): `Intl`, `atob`/`btoa`, `TextEncoder`/`TextDecoder`, `URL`/`URLSearchParams`, `fetch`, `setTimeout`, `process`, `require`, module imports, and `console`. `typeof console` is expected to be `"undefined"`, so a `console.log(...)` call fails.
  - **Present:** the standard ECMAScript built-ins: `JSON`, `Math`, `Date`, `RegExp` (including lookbehind and named groups), `Map`/`Set`, `Proxy`, `Promise`, `BigInt`, the ES2023 array methods and `Object.groupBy`.
  - **Build feature.** The CE and EE images are built with the `quickjs` feature (the `oss_core` feature group). A build without it fails every expression that *reaches the engine* with ``JavaScript expression evaluation requires the `quickjs` feature...``; expressions a shortcut answers still succeed.
- **Evidence:** `backend/Cargo.lock:10294-10297` (rquickjs 0.11.0), `backend/Cargo.toml:614`; feature wiring `backend/Cargo.toml:126`, `:182-204`; fresh runtime and context `backend/windmill-jseval/src/lib.rs:435-437`, installed globals `:435-552`; fallback stub `:999-1018`; shortcut order `backend/windmill-worker/src/js_eval.rs:44-85`; missing APIs: the in-repository analysis `backend/QUICKJS_MIGRATION_ANALYSIS.md` (sections 2.3 and 7); engine provenance: the rquickjs changelog entry for 0.9.0 ("Switching to quickjs-ng from quickjs as base engine") and the [0.11.0 changelog](https://github.com/DelSkayn/rquickjs/blob/v0.11.0/CHANGELOG.md).
- **Conformance:** [JE-1/a–e](conformance.md#je-1)

<a id="je-2"></a>
### JE-2 · An evaluation has a 20 s wall-clock limit, tried up to 3 times

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - One evaluation attempt may run for **20 000 ms**. An attempt that does not finish in time fails with ``The expression evaluation `<expr>` took too long to execute (>20000ms)``, where `<expr>` is the trimmed text before any rewriting. For a predicate it is the `Boolean(...)`-wrapped text ([JE-5](#je-5), [JE-24](#je-24)).
  - On the worker path a timeout is retried: **up to 3 attempts in total, with a 5 s pause between attempts**, so about 70 s pass before the error surfaces. Only errors whose text contains `took too long` are retried; every other error surfaces at once.
  - No interrupt handler is installed. A CPU-bound loop keeps its thread busy after the timeout fires, but the observable result is still the timeout error.
  - A promise that never settles (for example `new Promise(() => {})`) also ends in this timeout.
  - The limit applies only to evaluations that reach the engine; a shortcut returns before any engine starts.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:22`, `:303-328`; retry loop `backend/windmill-worker/src/js_eval.rs:81-107`.
- **Conformance:** [JE-2/a](conformance.md#je-2) (a slow fixture: allow at least 75 s).
- **Design note (not Windmill behaviour):** windmill-lite should keep the message. Interrupting a runaway evaluation, or dropping the retries, would be a deviation from Windmill; if adopted, it must be listed in the equivalence contract.

<a id="je-3"></a>
### JE-3 · An evaluation is capped at 128 MiB of heap

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - An evaluation's heap is capped at **128 MiB**. The `QUICKJS_MEMORY_LIMIT_MB` environment variable (a positive integer, in MB) overrides the cap.
  - Running out of memory produces ``The expression evaluation exceeded the memory limit of <N> MB. Reduce the amount of data handled in the expression, or raise the cap via the QUICKJS_MEMORY_LIMIT_MB environment variable.``
  - QuickJS signals an out-of-memory condition either as an `InternalError` whose text is exactly `out of memory`, or as a bare `null`/`undefined` throw. The evaluator treats both as out of memory. A user's explicit `throw null` or `throw undefined` therefore *also* produces this message. A user's `throw new Error("out of memory")` stays an ordinary error.
  - Like [JE-2](#je-2), the cap applies only to evaluations that reach the engine.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:331-364`, `:342-419`, `:366-421`; tests `:2837-2945`.
- **Conformance:** [JE-3/a](conformance.md#je-3)

<a id="je-4"></a>
### JE-4 · Reserved (unused)

**Status:** withdrawn

## B. The evaluation pipeline and its shortcuts

Every flow expression goes through one entry point. It **trims** the text ([JE-5](#je-5)) and then tries four shortcuts in a fixed order: [JE-6](#je-6) → [JE-7](#je-7) → [JE-8](#je-8) → [JE-9](#je-9). Only if all four miss does QuickJS run ([section C](#c-the-quickjs-path)). The shortcuts return **stored JSON text verbatim**, with no JavaScript round trip, so they can differ from the QuickJS path in number precision, key order and leniency. A predicate's text starts with `Boolean(`, so JE-6 to JE-8 never fire for predicates, but JE-9 can ([JE-24](#je-24)).

<a id="je-5"></a>
### JE-5 · Trimming, and what error messages quote

**Scope:** core (evaluation); later (message spelling) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **What is trimmed.** The evaluator removes leading and trailing whitespace, newlines included, from the text it evaluates. The shortcuts and the statement-mode detection ([JE-13](#je-13)) see the trimmed text.
  - **What messages quote.**
    - Site error messages (``Error during isolated evaluation of expression `…` ``, ``Error during evaluation of expression `…` `` and the predicate log lines) quote the expression **as written in the flow**, untrimmed.
    - The timeout message ([JE-2](#je-2)) quotes the trimmed text.
  - **Predicates are wrapped before trimming.** The text evaluated is `Boolean(` + the expression as written + `)`. Whitespace and newlines inside the wrapper survive; only the outside of the wrapper is trimmed ([JE-24](#je-24)).
- **Evidence:** `backend/windmill-worker/src/js_eval.rs:36`; `backend/windmill-worker/src/worker_flow.rs:2451-2452`, `:2691-2694`, `:2763-2765`; `backend/windmill-jseval/src/lib.rs:243`, `:324-327`.
- **Conformance:** [JE-5/a, JE-5/b](conformance.md#je-5)

<a id="je-6"></a>
### JE-6 · An exact context name returns the stored value verbatim

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - If the trimmed text is exactly the name of a value in the evaluation context, that value's stored JSON is returned unchanged.
  - The possible names depend on the site ([JE-22](#je-22)): `previous_result`, `result`, `params`, `resume`, `resumes`, `approvers`, `all_iters`, `started_at`, `flow_job_id`.
  - `flow_input` and `flow_env` are passed separately, not as context entries, so this shortcut does not apply to them; [JE-7](#je-7) does.
- **Evidence:** `backend/windmill-worker/src/js_eval.rs:44-46`.
- **Conformance:** [JE-6/a, JE-6/b](conformance.md#je-6)

<a id="je-7"></a>
### JE-7 · A top-level `flow_input` or `flow_env` key returns the stored value verbatim

**Scope:** core (plain keys); later (dotted-key quirk) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Dot form.** If the text starts with `flow_input` followed by `.`, *everything after the first dot* is used as **one** key of the flow input. If the input has that key, its stored JSON is returned verbatim.
  - **Bracket form.** For `flow_input["…"]`, the text must end with `"]` and the key between the quotes must be non-empty.
  - `flow_env.<…>` and `flow_env["…"]` behave the same way against the flow env ([JE-38](#je-38)).
  - **On a miss**, the text is evaluated on the QuickJS path with ordinary JavaScript semantics. Only a missing **direct** property of an existing object yields `null` ([JE-15](#je-15)); `flow_input.absent.child` throws a `TypeError` and the step fails.
  - **Quirk:** `flow_input.a.b` first looks up a top-level key literally named `a.b`. Only if there is none does JavaScript evaluate `flow_input.a.b`.
  - The returned text is exactly the stored text, so a number like `12345678901234567890` keeps its full precision. JavaScript arithmetic on it does not ([JE-16](#je-16)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:117-163`; `backend/windmill-worker/src/js_eval.rs:48-52`, `:81-107`.
- **Conformance:** [JE-7/a–g](conformance.md#je-7)

<a id="je-8"></a>
### JE-8 · The previous step's id returns `previous_result` verbatim

**Scope:** core (named previous id); later (empty-id spellings) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - When the site has a results context and its context holds `previous_result`, four spellings return `previous_result` verbatim: `results.<P>`, `results?.<P>`, `results["<P>"]` and `results?.["<P>"]`, where `<P>` is the previous id.
  - The text must be **exactly** one of these four. A longer path such as `results.a.n` is not one of them and goes to [JE-9](#je-9) ([JE-V6](#je-v6)).
  - **Empty previous id.** The spellings are built even when the previous id is empty. At the first module of a flow or body the previous id is `""`, so the exact texts `results[""]` and `results?.[""]` return `previous_result`, which is that flow job's input object ([JE-36](#je-36)). So do `results.` and `results?.`, which are not valid JavaScript.
  - On the QuickJS path, the `results` proxy compares against the same empty id, so `results[""]` inside a larger expression also yields `previous_result` ([JE-18](#je-18)).
- **Evidence:** `backend/windmill-worker/src/js_eval.rs:54-72`; previous-id computation `backend/windmill-worker/src/worker_flow.rs:3658-3663`; proxy `backend/windmill-jseval/src/lib.rs:805-817`.
- **Conformance:** [JE-8/a–d](conformance.md#je-8)

<a id="je-9"></a>
### JE-9 · Simple `results` paths are served by a lenient database lookup

**Scope:** core (shortcut and leniency); later (first-match `.length` decline) · **Confidence:** high (per-path `#>` edge cases: medium) · **Status:** confirmed

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
  - In a predicate, the shortcut's value goes to the strict boolean check ([JE-24](#je-24)).
  - **How it resolves.** The step's stored result is looked up through the "result by node id" lookup ([JE-19](#je-19) gives the resolution order). The path is then applied the way Postgres's `#>` operator applies a path to `jsonb`:
    - a missing step, a lookup failure, a missing key, a type mismatch or a `null` intermediate all give **`null`**, never an error;
    - for a step whose stored result is a list of child jobs (a loop or `branchall` module), the first path element must be the numeric index, which selects that child's result; a non-numeric first element gives `null`;
    - the returned text is the database's `jsonb` text ([JE-44](#je-44)).
  - **This lookup is more lenient than JavaScript.** With `a` returning `{}`, `results.a.b.c` gives `null` here, where JavaScript would throw. `results.nope.x` gives `null` where JavaScript would throw a `TypeError`. `results.a.0` is not valid JavaScript but resolves here.
  - It reads the database even when the step is the previous one ([JE-V6](#je-v6)).
- **Evidence:** pattern and fallback `backend/windmill-jseval/src/lib.rs:73-75`, `:165-218` (first-match inspection `:184-197`); dispatch `backend/windmill-worker/src/js_eval.rs:74-79`; predicate wrapping `backend/windmill-worker/src/worker_flow.rs:2451-2466`; lookup `backend/windmill-queue/src/jobs.rs:4403-4461`, `:4711-4777`; endpoint `backend/windmill-common/src/client.rs:137-155`; test `backend/tests/worker.rs:5731-5778`.
- **Conformance:** [JE-9/a–l](conformance.md#je-9)
- **Note:** [CF-19](control-flow.md#cf-19) drafted a whole-expression form
  of this shortcut. That dispute is tracked as
  [Q-CF-16](control-flow.md#q-cf-16); [CF-V6](control-flow.md#cf-v6) states
  the line-by-line reading that this finding gives.

## C. The QuickJS path

<a id="je-10"></a>
### JE-10 · Names are bound only if they appear in the expression text

**Scope:** core (binding rule, `flow_env` default); later (computed access) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - A context value ([JE-22](#je-22)) is defined only if its name appears **as a substring** of the trimmed text. Substring matching makes this a superset: `results` also binds `result`.
  - `flow_input` is bound only if the text contains `flow_input`. When the site has no flow input, it is bound to `null`.
  - `flow_env` is bound only if the text contains `flow_env`. It is then a plain object holding the resolved flow env ([JE-38](#je-38)), or `{}` when there is none.
  - `previous_result` is additionally bound when the text contains `error` ([JE-17](#je-17)) or one of the previous-step spellings of [JE-8](#je-8).
  - A name the text never mentions literally is unavailable: computed access such as `globalThis["fl"+"ow_input"]` gives `undefined`.
  - **Two kinds of binding.** `results` and `error` are lexical global bindings, not properties of `globalThis`. Context values, `flow_input`, `flow_env` and the helper functions *are* `globalThis` properties.
  - Values are copied in as fresh JavaScript values ([JE-16](#je-16)); mutating them affects nothing outside the evaluation.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:257-290`, `:455-492`, `:494-526`, `:805-817`.
- **Conformance:** [JE-10/a–d](conformance.md#je-10)

<a id="je-11"></a>
### JE-11 · Calls to `variable(`, `resource(` and `flow_user_state(` are rewritten textually

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Before evaluation, every occurrence of the character sequences `variable(`, `resource(` and `flow_user_state(` is rewritten so that the call is awaited: `F(args)` becomes `(await F(args))`.
  - The closing parenthesis is found by counting `(` and `)` from the call onwards. Parentheses inside string literals count too.
  - The rewrite is purely textual, not a JavaScript parse. It has no word boundary and no awareness of strings or comments:
    - `myvariable('x')` becomes `my(await variable('x'))`, which fails because `my` is not defined;
    - the text `"variable(x)"` inside a string literal is rewritten as well.
  - A rewritten call inside a non-`async` nested function is a syntax error (as in [JE-12](#je-12)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:81-115`, `:446-449`; tests `:1103-1116`.
- **Conformance:** [JE-11/a](conformance.md#je-11)

<a id="je-12"></a>
### JE-12 · `results.<id>` is rewritten textually into an awaited lookup

**Scope:** core (single-quoted and computed keys, bare `results`); later (other consequences) · **Confidence:** high (exact nested-function diagnostic: medium, [Q-JE-1](#q-je-1)) · **Status:** confirmed

- **Rule:** after [JE-11](#je-11), every match of this pattern is wrapped as `(await <match>)`: the characters `results`, optionally followed by `?`, followed by either `.` and one or more `[A-Za-z0-9_]`, or by `["…"]` (double quotes only, matched lazily). Observable consequences:
  1. **No word boundary.** `flow_input.results.x` becomes `flow_input.(await results.x)`, a syntax error. `my_results.x` becomes `my_(await results.x)`.
  2. **Strings and comments are rewritten too.** The expression `"see results.a"` evaluates to the string `"see (await results.a)"`.
  3. **Single quotes and computed keys are not rewritten,** so `results['a']` and `results[k]` stay Promises:
     - used alone, the async wrapper adopts the Promise, so the value comes out right;
     - `results['a'].v` reads `.v` off a Promise and gives `null`;
     - `{y: results['a']}` gives `{"y":{}}`.
  4. **A nested non-async function breaks.** If `results.x` appears inside a non-`async` function or arrow, the inserted `await` is a compile-time `SyntaxError` and the whole expression fails. `results.x.map(a => a.y)` is fine, because `results` is read outside the callback; `arr.map(a => results.x[a])` fails.
  5. **Bare `results`** (not followed by `.` or `["`) is the lookup proxy itself. `JSON.stringify` of it is `{}`, so `x = results` gives `{}`.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:59-68`, `:92-95`, `:555-570`, `:805-817`; test `:1119-1132`.
- **Conformance:** [JE-12/a–e](conformance.md#je-12)

<a id="je-13"></a>
### JE-13 · Expression mode or statement mode

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** after the rewrites, the code is placed in an `async` function body. **Statement mode** is chosen when any of these holds:
  - the text starts with `return ` or `return;`, or is exactly `return`;
  - the text starts with one of `const `, `let `, `var `, `if `, `if(`, `for `, `for(`, `while `, `while(`, `switch `, `switch(`, `try `, `try{`, `throw `, `function `, `class `, `async `, `await `;
  - the text contains a `;` outside single, double and backtick quotes. The scan toggles a quote state on each unescaped quote character; it knows nothing about regex literals, comments or `${}` nesting.

  Then:
  - **Statement mode:** the text runs as the function body. The value is whatever it `return`s; with no `return` it is `undefined`, which becomes `null` ([JE-15](#je-15)).
  - **Expression mode:** the text is used as `return <text>;`. A leading `{` is therefore an **object literal**, not a block.
  - An empty text yields `null` ([JE-V4](#je-v4)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:554-559`, `:864-937`; tests `:1073-1100`, `:1466-1490`.
- **Conformance:** [JE-13/a–f](conformance.md#je-13)

<a id="je-14"></a>
### JE-14 · A trailing `//` comment breaks a single-line direct expression

**Scope:** later · **Confidence:** medium (source-read, not run) · **Status:** confirmed

- **Rule:**
  - In both modes the code is wrapped on a single line, with the wrapper's own closing text after the user's text. In a one-line expression, a trailing `//` line comment comments out that closing text, and the expression fails with a `SyntaxError`.
  - Direct (non-predicate) expressions are trimmed before wrapping ([JE-5](#je-5)), so a trailing newline does not help them.
  - A `//` comment followed by more lines is fine, and `/* */` comments are fine.
  - Predicates differ: they are wrapped before trimming, so a trailing newline after the comment rescues them ([JE-24](#je-24)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:555-559`.
- **Conformance:** [JE-14/a, JE-14/b](conformance.md#je-14)

<a id="je-15"></a>
### JE-15 · The returned value becomes JSON

**Scope:** core · **Confidence:** high (`BigInt` and circular structures: medium) · **Status:** confirmed

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
- **Evidence:** `backend/windmill-jseval/src/lib.rs:554-570`; tests `:1314-1322`, `:1649-1694`, `:2725-2745`; quickjs-ng message: [quickjs.c @ be664ca](https://github.com/quickjs-ng/quickjs/blob/be664ca1ccd86cbadf8df7348b8e15e77a32a1ba/quickjs.c) line 47499.
- **Conformance:** [JE-15/a–i](conformance.md#je-15)

<a id="je-16"></a>
### JE-16 · Context values arrive as converted JSON

**Scope:** core (key order, precision); later (negative zero) · **Confidence:** high (Python negative-zero token: medium) · **Status:** confirmed

- **Rule:**
  - Every context value is parsed from its JSON text and rebuilt as a JavaScript value. Strings, booleans, `null`, arrays and objects map one to one.
  - **Key order from raw JSON.** A value converted from raw JSON text keeps that text's key order. As for any JavaScript object, integer-like keys enumerate first, in ascending order.
  - **Unstable outer order.** The outer keys of `flow_input` have **no stable order**: the flow input is held in a hash map with randomised iteration before it is serialised into the context. The same holds for `previous_result` at module 0 (which is that input) and for the top-level argument object a script receives. Nested values keep their stored order.
  - **Numbers.** Integers that fit in 32 bits become integers, every other number a double. In JavaScript terms numbers behave like `JSON.parse`: integers beyond 2^53 lose precision.
  - **Negative zero.** The JSON token `-0` parses as floating-point negative zero and **stays `-0`** in JavaScript.
    - A producer's raw result travels in memory to the next flow transition ([JE-44](#je-44)). A `-0` or `-0.0` token in it reaches the next step's expressions as negative zero, and any consequence computed from it is recorded normally (visible at O2 and O5).
    - A `jsonb` round trip stores the number as `0` (or `0.0`), so a read through the database sees no negative zero.
    - Bun's `JSON.stringify(-0)` emits `0`. A fixture producer must emit a negative-zero token, as Python's `json` output `-0.0` does ([languages](languages.md)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:455-462`, `:825-862`; `serde_json` with `preserve_order` `backend/Cargo.toml:435`; `backend/windmill-worker/src/common.rs:133-142`; raw forwarding `backend/windmill-worker/src/result_processor.rs:1490-1531`, `backend/windmill-worker/src/worker_flow.rs:1477-1488`, `:2086-2099`; the pinned serde_json 1.0.151 (`backend/Cargo.lock:11151`), [`src/de.rs:509-523`](https://github.com/serde-rs/json/blob/v1.0.151/src/de.rs#L509); Python result serialisation `backend/windmill-worker/src/python_executor.rs:3287-3303`, `:984-987`, `:958-964`.
- **Conformance:** [JE-16/a–f](conformance.md#je-16)
- **Note:** no fixture may assert the order of `Object.keys(flow_input)` or of a script's top-level argument keys.

<a id="je-17"></a>
### JE-17 · The `error` helper variable

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** if the trimmed text contains the substring `error` anywhere (`flow_input.error_count` counts), a global `error` is defined before evaluation, derived from `previous_result` by this decision list:
  1. If `previous_result.error` is truthy, `error` is that value. This includes a string-valued error such as the one [JE-26](#je-26) produces.
  2. Otherwise, if `previous_result` is an array, collect the items that are objects with an own or inherited `error` key:
     - exactly one: `error` is that item's `.error`;
     - more than one: `error = {name: "MultipleErrors", message: <joined>, errors: previous_result}`. `<joined>` joins one entry per collected item with `; `. Each entry is `[<tag>] <text>`, where `<tag>` is the item's `error.step_id`, or else the item's position *within the collected list*, and `<text>` is `error.message`, or else `error.name`;
     - none: `error = {name: "MultipleErrors", message: "Could not parse errors", errors: previous_result}`.
  3. Otherwise, if `previous_result` is truthy: `error = {name: "UnknownError", message: "Could not parse the error", error: previous_result}`.
  4. Otherwise: `error = {name: "UnknownError", message: "No error found"}`.

  The helper is meant for failure-module transforms and `retry_if`, but it is installed at every site whose text contains `error`.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:273-279`, `:494-526`.
- **Conformance:** [JE-17/a–c](conformance.md#je-17)

<a id="je-18"></a>
### JE-18 · How a `results.<id>` read resolves on the QuickJS path

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** `results` is a lazy proxy. Reading a string key `k` returns a Promise, which the rewrite ([JE-12](#je-12)) awaits; symbol keys give `undefined`. The decision list for `k`:
  1. **The previous step.** If `k` is the previous id ([JE-8](#je-8)) and `previous_result` is bound, return `previous_result` from memory, without any fetch. The textual binding of [JE-10](#je-10) always holds when the rewrite produced the read.
  2. **A module of the current flow with a job.** If `k` is a module of the *current* (sub)flow whose status already records a job (any started, finished or failed module):
     - a single job: fetch that job's completed result;
     - a list of child jobs (a loop or `branchall`): fetch every child's result concurrently and return them as an array in child order;
     - **if any fetch fails, the read throws** `Error("Failed to fetch result for step '<k>': <cause>")` (with `results for step` for lists).
  3. **Anything else:** use the API's result-by-node-id lookup ([JE-19](#je-19)). **Any failure there yields `null`.**
- **Evidence:** proxy `backend/windmill-jseval/src/lib.rs:669-823` (fetch errors versus fallback `:695-750`, `:773-782`); context `backend/windmill-worker/src/worker_flow.rs:6155-6171`; modules with a job `backend/windmill-types/src/flow_status.rs:401-404`, `:454-458`; endpoints `backend/windmill-common/src/client.rs:118-155`.
- **Conformance:** [JE-18/a–d](conformance.md#je-18)
- **Note:** when `previous_result` is bound for a computed read of the
  previous id (for example `results[flow_input.target]`) is disputed in
  [FX-10](execution.md#fx-10) and tracked as
  [Q-FX-15](execution.md#q-fx-15); see [JE-10](#je-10) for the binding rule.

<a id="je-19"></a>
### JE-19 · Which `results.<id>` a scope can see

**Scope:** core (scope visibility); later (subflow-by-path, restart and completed-flow fallbacks) · **Confidence:** medium (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** resolution through the API ([JE-9](#je-9) and step 3 of [JE-18](#je-18)) uses a per-scope map from node id to job. Each scope keeps its own map; every time a module finishes, its entry is written, overwriting the previous one.
  - **Scopes.** A top-level flow is its own scope. A subflow called by path (`flow` module) and every iteration of a parallel `forloopflow` each start a new scope. Sequential loops, while loops, `branchone` and `branchall` bodies share their enclosing scope.
  - **Lookup.** The current scope is searched first. If the id is absent and the scope has a parent job, the parent's scope is searched, and so on upwards.
  - **Restart fallback.** On a miss in the running scope chain, the lookup checks whether the running flow's root was **restarted** from an earlier run. If so, it searches that original, completed run top-down for a module with the id. This can happen **while the restarted run is still active**. If the root has no restart information, the miss ends there and gives `null`.
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
- **Conformance:** [JE-19/a–e](conformance.md#je-19)
- **Note:** which reads in a restarted run actually reach the restart fallback, rather than being served by module statuses copied into the restarted run ([JE-18](#je-18) step 2), is [Q-JE-8](#q-je-8). The subflow-reads-caller row is [Q-JE-10](#q-je-10).

<a id="je-20"></a>
### JE-20 · The `variable()`, `resource()` and `flow_user_state()` helpers

**Scope:** core (`variable`, `resource`); later (`flow_user_state`) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **`variable(path)`** resolves to the variable's value as a **string**, with secrets decrypted.
  - **`resource(path)`** resolves to the resource's JSON value, with its nested `$var:`/`$res:` references already interpolated.
  - **`flow_user_state(key)`** resolves to the value stored under `key` for the root flow job.
  - **Failure:** each helper throws `Error(<API error text>)`.
  - **Availability:** the helpers need an authenticated client, and `flow_user_state` also needs a results context.
    - Without a client, `variable` and `resource` reject with `variable() is not available without an authenticated client` (and the same wording with `resource()`).
    - Without a results context, `flow_user_state` rejects with `flow_user_state() is not available in this context`. When there is no client either, it rejects with the `...without an authenticated client` wording instead.
  - Only the suspend site `user_groups_required` lacks a client ([JE-34](#je-34)). `sleep` and flow `skip_expr` have a client but no results context.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:541-552`, `:575-667`, `:753-803`; endpoints `backend/windmill-common/src/client.rs:87-116`, `:157-171`.
- **Conformance:** [JE-20/a, JE-20/b](conformance.md#je-20)

<a id="je-21"></a>
### JE-21 · The evaluator's own error text

**Scope:** core (prefix structure); later (exact engine text) · **Confidence:** medium (structure confirmed from dependency source; stack text and non-Error display pending) · **Status:** confirmed

- **Rule:** a failing evaluation produces `QuickJS evaluation error: <caught>`, where `<caught>` takes one of these forms:
  - **A thrown `Error` object of any subclass:** `Error: <message>`, followed, when the engine has a stack, by a newline and the stack text. The subclass name (`TypeError`, `ReferenceError`, `SyntaxError`) is **not** printed: it is always `Error:`. Engine stack frames name the script `eval_script`.
  - **A thrown non-Error value:** `Exception generated by quickjs: <engine debug form of the value>`, followed by a newline.
  - **A `null` or `undefined` throw:** the out-of-memory message of [JE-3](#je-3).

  Engine messages on quickjs-ng revision `be664ca`: `<name> is not defined` for an unknown global, `cannot read property '<p>' of null` and `cannot read property '<p>' of undefined` for a property read on `null`/`undefined`, `BigInt are forbidden in JSON.stringify`. The exact stack text and the debug display of a thrown non-Error value are open ([Q-JE-1](#q-je-1)). Each site then wraps this text ([JE-23](#je-23) to [JE-35](#je-35)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:419`, `:939-942`; rquickjs 0.11.0 [`Exception` display](https://github.com/DelSkayn/rquickjs/blob/v0.11.0/core/src/value/exception.rs#L217) and [`CaughtError` display](https://github.com/DelSkayn/rquickjs/blob/v0.11.0/core/src/result.rs#L530); default script name `eval_script` in rquickjs `core/src/context/ctx.rs:190-195`; quickjs-ng [quickjs.c @ be664ca](https://github.com/quickjs-ng/quickjs/blob/be664ca1ccd86cbadf8df7348b8e15e77a32a1ba/quickjs.c) lines 7393, 7801-7803, 47499.
- **Conformance:** [JE-21/a](conformance.md#je-21)

## D. Expression sites and their scopes

<a id="je-22"></a>
### JE-22 · Scope table

**Scope:** core (all other rows); later (`parallelism`, `skip_expr` rows); out (`suspend.user_groups_required` row, AI-agent tool calls) · **Confidence:** high (`sleep` and `stop_after_if` result identity: medium) · **Status:** confirmed

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

**Scope:** later · **Confidence:** high (outcome of `parallelism: 0` after enqueue: unknown) · **Status:** confirmed

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

**Scope:** core · **Confidence:** high (retry after a failed transform: medium) · **Status:** confirmed

- **Rule:**
  - Each key of `input_transforms` is evaluated **independently**, in an **unspecified order** (a hash map). Each evaluation that reaches QuickJS gets its own fresh runtime. Transforms cannot see each other: `params` is always the empty object `{}`.
  - **Dispatch is serial and stops at the first error** ([JE-V5](#je-v5)).
  - **`type: static`:** the stored JSON value is copied verbatim, with no templating ([JE-40](#je-40)).
  - **`type: ai`:** the key is omitted.
  - **`type: javascript`:** [JE-5](#je-5) to [JE-21](#je-21) apply. An expression yielding `undefined` gives a present key with value `null`, not an absent key.
  - Omitted payloads and unknown types: [JE-V4](#je-v4).
  - **On error,** the step is *still pushed*, with an empty argument map and a pre-run error. When it is picked up, it fails without running, with result ``{"error":{"name":"ExecutionErr","message":"Error during isolated evaluation of expression `<expr>`:\n<evaluator error>"}}``. This is an ordinary step failure: `retry`, `continue_on_error` and the failure module apply as for any failure ([execution](execution.md); surrounding error fields such as `step_id` are defined there).
    - When the body of a collapsed loop ([JE-36](#je-36)) fails a transform, the message is prefixed `could not transform input using an expr: `.
    - If several transforms would fail, the reported one is whichever is evaluated first in the unspecified order; the others are never evaluated.
  - **Transforms are not evaluated at all** when:
    - the step is skipped by `skip_if`;
    - `mock.enabled` is true;
    - the module is `identity`;
    - the step is the preprocessor (it receives the flow input plus an entrypoint override);
    - the module sets `pass_flow_input_directly` (it receives the flow input as is);
    - the attempt is an ordinary retry, which reuses the recorded arguments ([JE-V2](#je-v2)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2712-2775` (`params` built from the still-empty map at `:2732`), `:3874-3966`, `:4152-4255`, `:4403-4436`; pre-run error `backend/windmill-worker/src/worker.rs:3647-3649`; error to result `backend/windmill-worker/src/result_processor.rs:1910-1965`, `backend/windmill-common/src/worker.rs:2228-2233`.
- **Conformance:** [JE-23/a–d](conformance.md#je-23)

<a id="je-v2"></a>
### JE-V2 · Ordinary retries reuse the recorded transformed arguments

**Scope:** core · **Confidence:** high (retry after a failed transform: medium, [Q-JE-4](#q-je-4)) · **Status:** confirmed

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

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - `{"type":"static"}` without `value` gives `null`.
  - `{"type":"javascript"}` without `expr` is the empty expression, which yields `null` ([JE-13](#je-13)).
  - An unknown `type` is a deserialisation error, not an expression-time exception: the flow fails to load, with `` got value: <type> for field `type`, expected value: `static` or `javascript` ``.
  - An argument with no transform entry at all is **absent** from the arguments.
- **Evidence:** `backend/windmill-types/src/flows.rs:732-790` (defaults `:780-786`); `backend/windmill-jseval/src/lib.rs:554-570`, `:864-870`.
- **Conformance:** [JE-V4/a–c](conformance.md#je-v4)

<a id="je-v5"></a>
### JE-V5 · Transform dispatch is serial and stops at the first error

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Within one pass over a step's input transforms, transforms are taken in unspecified hash-map order. Each evaluation is **awaited before the next one starts**.
  - The pass **stops at the first error**. Later transforms are never evaluated, so their helper reads (`variable()`, `resource()`, `flow_user_state()`, `results` fetches) never happen.
  - Isolated contexts mean transforms cannot see each other's values. They do **not** make external reads, time-dependent expressions, error selection or side effects order-independent.
  - A timed-out CPU-bound evaluation is not forcibly stopped ([JE-2](#je-2)); that concerns the evaluator's thread, not the dispatch order.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2743-2770`; helper reads `backend/windmill-jseval/src/lib.rs:584-614`; timeout task `:303-328`; a non-200 helper response's body becomes the error text `backend/windmill-common/src/client.rs:273-294`.
- **Conformance:** [JE-V5/a](conformance.md#je-v5)
- **Design note (not Windmill behaviour):** nothing here shows that evaluating transforms concurrently, sharing a runtime, or de-duplicating reads (for example fetching `results.a` once for several transforms) preserves behaviour. Sending all of a step's transforms in one transport message is behaviour-preserving only if each is still evaluated one at a time, with the same context, its own engine instance and per-evaluation limits, the same reads, and the same first-error stop.

<a id="je-24"></a>
### JE-24 · Predicates are wrapped as `Boolean(<expr>)`

**Scope:** core · **Confidence:** high · **Status:** confirmed

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

**Scope:** core · **Confidence:** high (error routing end to end: medium) · **Status:** confirmed

- **Rule:**
  - **Order:** evaluated after the previous module's `sleep` has been scheduled and after `retry_if`, and before the module's input transforms. Scope per [JE-22](#je-22).
  - **When true:** the module becomes an identity step ([execution](execution.md)) and its transforms are not evaluated.
  - **Error:** computing the next step fails, and the error is routed per [JE-35](#je-35): path (a) after a child completed, (b) at first pickup of a root flow, (c) at first pickup of a nested flow. The log line is ``Error evaluating skip_if of module '<id>' expression `<expr>`: …``, quoting the expression as written ([JE-5](#je-5)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3666-3870` (skip_if `:3842-3870`), `:3920-3925`; chaining failure `:2086-2125`; log line `:2471-2488`; parity test with `results` in `skip_if` `backend/tests/flow_engine_parity.rs:694-790`.
- **Conformance:** [JE-25/a](conformance.md#je-25)

<a id="je-26"></a>
### JE-26 · `stop_after_if`, and a throwing predicate fails the parent, not the child

**Scope:** core · **Confidence:** high (paths; end to end: medium) · **Status:** confirmed

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

**Scope:** core · **Confidence:** high (empty-body path: medium) · **Status:** confirmed

- **Rule:**
  - **When evaluated:** when a loop or `branchall` module completes with at least one child. For a parallel module that is when its last child finishes; for a sequential `branchall`, only if no branch stopped early.
  - An empty iterator or an empty branch list completes the module through a separate empty-body path, **without** evaluating `stop_after_all_iters_if`.
  - **Scope:** `result` and `previous_result` are the array of child results. `results` follows the textual condition of [JE-22](#je-22).
  - **Error:** the module stops early **with the error** ``Error evaluating stop_after_all_iters_if expression `<expr>`: <error text>``, and the module fails as an ordinary step failure ([JE-35](#je-35) path (d)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:342-409`, `:974-1016`, `:1204-1247`; empty-body path `:3998-4050`, `:5332-5333`, `:5539`.
- **Conformance:** [JE-27/a, JE-27/b](conformance.md#je-27)

<a id="je-28"></a>
### JE-28 · `sleep`

**Scope:** core · **Confidence:** high (error routing: medium) · **Status:** confirmed

- **Rule:**
  - **When evaluated:** a module's `sleep` is evaluated when the **next** module is about to start. Its `result` is the sleeping module's own result. There is no `results` ([JE-22](#je-22)).
  - **The value delays when the next job runs, not evaluation:** the next module's `skip_if`, transforms and `timeout` are evaluated immediately, and only the job is scheduled later.
  - **The last module's `sleep` is never evaluated,** because no next module starts. This also applies to the last module of a loop or branch body.
  - **Static values** are parsed as JSON. **JavaScript values** go through [JE-5](#je-5) to [JE-21](#je-21). The result is then interpreted:
    - a non-negative integer is that many **seconds**;
    - a JSON number with a fraction or exponent is `value × 1000` **milliseconds**, truncated; a negative fraction becomes `0`. A static `2.0` therefore counts as a float (2000 ms), while an expression returning `2.0` serialises to `2`;
    - a negative integer fails with `Expected an integer, found: <n>`;
    - any other JSON fails with `Expected an integer, found: <debug form>`, for example `Expected an integer, found: Null` or `Expected an integer, found: String("5")`;
    - an evaluation error fails with ``Expected a number value, had error instead: ExecutionErr("Error during evaluation of expression `<expr>`:\n<evaluator error>")``. This is a debug form, so the newline appears escaped as `\n`.
  - **Failures** are flow-chaining failures, routed per [JE-35](#je-35).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3666-3714` (only the previous module's sleep is read, `:3666-3684`), `:2659-2708`; scheduling `:4403-4436`.
- **Conformance:** [JE-28/a–c](conformance.md#je-28)

<a id="je-29"></a>
### JE-29 · `timeout`

**Scope:** core · **Confidence:** high (error routing: medium) · **Status:** confirmed

- **Rule:**
  - The value must deserialise as a **32-bit signed integer** number of seconds.
  - **Error messages:**
    - a static value that fails: `Error parsing static value as i32: <serde error>`;
    - a JavaScript result that fails: `Error parsing result as i32: <serde error>. Value was: <json>`;
    - a negative value: `Timeout value cannot be negative`;
    - an evaluation error: ``Error during evaluation of expression `<expr>`:\n<evaluator error>``.
  - **Scope:** per [JE-22](#je-22), including `results`.
  - **When evaluated:** at push time, after the transforms. Failures are flow-chaining failures, routed per [JE-35](#je-35).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:4379-4399`, `:2659-2708`.
- **Conformance:** [JE-29/a](conformance.md#je-29)

<a id="je-30"></a>
### JE-30 · The `forloopflow` iterator

**Scope:** core · **Confidence:** high · **Status:** confirmed

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

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Branches are evaluated **in order**. The first predicate that is `true` wins, and later predicates are **not evaluated**. If none is true, `default` runs.
  - A predicate error stops the scan. The module then fails as an **ordinary step failure** with result `{"error":{"message":"<error text>","name":"<error name>"}}`. For an evaluation error the name is `Anyhow` and the message is `Error: QuickJS evaluation error: … @<rust location>`. Ordinary failure handling then applies.
  - This describes a normal execution. Under a nested restart that targets the `branchone` module, the originally chosen branch is reused and no predicate is evaluated.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:5418-5485` (restart lock `:5419-5432`), `:4053-4076`.
- **Conformance:** [JE-31/a, JE-31/b](conformance.md#je-31)

<a id="je-32"></a>
### JE-32 · `retry_if` on a flow step

**Scope:** core · **Confidence:** medium · **Status:** confirmed

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

**Scope:** later · **Confidence:** high (`WM_SCHEDULED_FOR` format: medium, [Q-JE-7](#q-je-7)) · **Status:** confirmed

- **Rule:**
  - **When evaluated:** at step 0 of a flow whose value has `skip_expr` (root flows and flows called by path).
  - **Scope:** `flow_input`, `flow_env`, and `WM_SCHEDULED_FOR`, the job's scheduled time as a string of the form `YYYY-MM-DD HH:MM:SS[.ffffff] UTC`. `result` and `previous_result` are the JSON string `"{}"`. There is no `results`. `WM_SCHEDULED_FOR` is the only `WM_*` name any expression can read ([JE-39](#je-39)).
  - If the predicate is true, the flow completes **successfully** with result `"stopped early"` (a string), as an early stop.
  - An error is logged as `Error evaluating flow skip_expr expression …` and is a flow-chaining failure ([JE-35](#je-35) path (b) for a root flow).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3280-3320` (the extra binding `:3289-3293`); `backend/windmill-common/src/variables.rs:273`; `openflow.openapi.yaml:92`; public docs <https://www.windmill.dev/docs/flows/early_stop#early-stop-for-flow>.
- **Conformance:** [JE-33/a, JE-33/b](conformance.md#je-33)

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

**Scope:** core · **Confidence:** high (paths (a), (b) and the envelopes; path (c): medium; end to end pending, [Q-JE-3](#q-je-3)) · **Status:** confirmed

- **Rule:**
  - **Failure-module scope.** The failure module's transforms see:
    - `previous_result` = the failed module's result (normally `{"error":{…}}`; a string-valued error after [JE-26](#je-26));
    - previous id = the failed module's id, so `results.<failed id>` is the [JE-8](#je-8) shortcut;
    - `started_at` (the flow's start timestamp, as an RFC 3339 JSON string) and `flow_job_id` (the flow job's UUID string), both only when the flow has a start time;
    - `error`, per [JE-17](#je-17).
  - **Error routes.**
    - **(a) Chaining after a child completed.** The error happens while the flow computes its next module after one of its children finished. This covers `skip_if`, `sleep`, `timeout`, the iterator, `parallelism` and step-start `retry_if` on any module started that way.
      - The flow job is completed directly with `{"error":{"message":"<error text>","name":"InternalError"}}`.
      - The flow's failure module is **not** run.
      - The log reads `Unexpected error during flow chaining:`.
    - **(b) First pickup of a root flow.** The error happens while starting the first module, when the root flow job is first handled; for example `skip_if` on module 0, flow `skip_expr`, or the iterator of a loop at module 0.
      - Ordinary job-error handling applies, with the error object `{"message":"<text>","name":"<error name>"}`.
      - The name is `Anyhow` for predicate and iterator evaluation errors, whose message is `Error: QuickJS evaluation error: … @<rust location>`; it is `ExecutionErr` for typed errors such as the `sleep` and `timeout` checks.
      - The failure is applied to the root's own status, so the root's **failure module runs** if present. Without one, the final result is `{"error":{…}}` with that object.
    - **(c) First pickup of a nested flow.** The same kind of error, raised in a loop or branch body or in a subflow called by path.
      - The nested job is completed with the error, and its **parent** is updated as if that child failed.
      - The nested flow's own failure module is **not** run.
      - The parent then applies its own retry, `continue_on_error` or failure module.
    - **(d) Step failures:** input transforms ([JE-23](#je-23)), `branchone` predicates ([JE-31](#je-31)) and `stop_after_all_iters_if` ([JE-27](#je-27)) follow ordinary failure semantics.
    - **(e) Unrecoverable string error:** `stop_after_if` ([JE-26](#je-26)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2086-2125`, `:1300-1340`, `:3732-3822`, `:3934-3955`; `backend/windmill-queue/src/jobs.rs:883-918`; `backend/windmill-worker/src/worker.rs:1706-1736`, `:3815-3841`; `backend/windmill-worker/src/result_processor.rs:1910-1978`.
- **Conformance:** [JE-35/a–d](conformance.md#je-35)

<a id="je-36"></a>
### JE-36 · What `previous_result` is at each position

**Scope:** core · **Confidence:** high (source-traced; oracle pending, [Q-JE-5](#q-je-5)) · **Status:** confirmed

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

**Scope:** core · **Confidence:** high · **Status:** confirmed

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

**Scope:** later · **Confidence:** high · **Status:** confirmed

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

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** these names are **not** defined in any flow expression, so referencing them throws a `ReferenceError`: `env`, `process`, `iter` (use `flow_input.iter`), `WM_WORKSPACE`, `WM_JOB_ID` and every other `WM_*` name except `WM_SCHEDULED_FOR` in flow `skip_expr` ([JE-33](#je-33)), `wmill`, `step_id`, `job_id`. `all_iters` exists only in `stop_after_if` ([JE-26](#je-26)).
- **Evidence:** the context maps of [JE-22](#je-22); installed scope `backend/windmill-jseval/src/lib.rs:455-552`; the only extra global `backend/windmill-worker/src/worker_flow.rs:3289-3293`.
- **Conformance:** [JE-39/a](conformance.md#je-39)

## E. Static values and `$` references

<a id="je-40"></a>
### JE-40 · Static values are copied verbatim, with no templating

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - A `type: static` transform value is copied verbatim. **The backend has no template interpolation of static strings:** `${…}` in a static string is literal text.
  - The UI's "template" editing of a string input stores the input as `type: javascript` with a backtick template-literal expression, such as `` `Hello ${flow_input.name}` ``. When it reloads, the UI shows an expression that starts with a backtick as a static template.
  - Templates are therefore ordinary JavaScript expressions ([JE-10](#je-10) to [JE-21](#je-21)), and they get the `results` rewrite inside `${}`.
  - A static string holding a `$var:`/`$res:` reference is recorded as written (O2) and resolved only at O3 ([JE-41](#je-41)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2745-2748`; UI behaviour `frontend/src/lib/components/InputTransformForm.svelte:186-198`, `:203-215`, `:273-275`; test `backend/windmill-jseval/src/lib.rs:1371-1388`.
- **Conformance:** [JE-40/a, JE-40/b](conformance.md#je-40)

<a id="je-41"></a>
### JE-41 · `$var:`, `$res:` and related strings are resolved per leaf step, at argument-build time

**Scope:** core (resolution point); later (`$jsonvar:`, `$encrypted:`) · **Confidence:** high · **Status:** confirmed

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

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - The job API's arguments are the arguments stored **before** leaf interpolation. Resolution produces a separate map or arguments file and does not update the job's arguments row.
  - A fixture therefore cannot both observe "the received argument" through the job API and expect `$var:`/`$res:` replacement. Fixtures name their observation point: evaluator output (O1), recorded args (O2), resolved args (O3), script observation (O4), or flow and job outcome (O5); see [Conventions](#conventions).
  - Later observation points can still carry a computed consequence of a pre-database value ([JE-16](#je-16)), so a case at O2 to O5 that depends on a memory or database read says which read it uses.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:4269-4283`, `:4403-4436`; `backend/windmill-worker/src/common.rs:80-89`, `:121-150`, `:174-210`.
- **Conformance:** [JE-V3/a](conformance.md#je-v3)

<a id="je-42"></a>
### JE-42 · The resolution rules

**Scope:** core (`$var:`/`$res:` with nesting); later (other rules, depth guard) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  1. **Pre-check per argument.** A top-level argument is examined only if its JSON text contains `$var:`, `$jsonvar:`, `$res:` or `$encrypted:` *anywhere*. Arguments without any marker pass through untouched.
  2. **Walk.** The argument is parsed and walked. Only a string that **starts with** a marker is replaced; `"x $var:a"` stays as it is.
     - `$var:<path>`: the variable's value as a JSON string.
     - `$jsonvar:<path>`: the variable's value parsed as JSON. On failure: `Failed to parse $jsonvar value as JSON: …`.
     - `$res:<path>`: the resource's interpolated JSON value. The path must have at least 2 `/`-separated segments (`CUSTOM_INSTANCE_DB/…` excepted); otherwise ``Argument `<name>` is an invalid resource path: <path>``.
     - `$encrypted:<blob>`: a decrypted value. This marker is internal.
     - **Any other string starting with `$`,** within an argument that passed the pre-check: a lookup by name among the job's reserved variables (`WM_WORKSPACE`, `WM_JOB_ID` and others; [languages](languages.md) lists them). An unknown name is left as is. The same `$WM_JOB_ID` string in an argument *without* any marker is not replaced.
  3. **Depth limits.**
     - Arrays are walked only at nesting depth ≤ 2 (the top-level value is depth 0) and only when they have ≤ 1000 items. Deeper or larger arrays pass through unresolved.
     - **Any** value reached at depth 50 or more, of any type, fails the step; the check happens before the value's type is looked at. The message is ``Maximum resource/variable interpolation depth (50) exceeded for `<name>`; this usually indicates a circular `$res:` or `$var:` reference``. Since arrays beyond depth 2 are never walked, in practice the guard is reached only through nested objects.
  4. **Errors fail the step.** ``Variable <path> not found for `<name>`: <cause>`` and ``Resource <path> not found for `<name>`: <cause>`` are "not found" errors. `<name>` is the innermost object key, or the argument name at the top level.

  Resolved values are observed at O3.
- **Evidence:** `backend/windmill-worker/src/common.rs:162-210`, `:260-399` (depth guard `:273-280`); tests `:2275-2345`.
- **Conformance:** [JE-42/a–h](conformance.md#je-42)

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

**Scope:** core · **Confidence:** medium · **Status:** confirmed

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
    - the nested values of `flow_input`, because arguments are loaded from the job row.
  - **Reads that do not:**
    - P after an ordinary step, which is raw text held in memory;
    - `results.<previous id>` through the whole-result spellings of [JE-8](#je-8) or rule 1 of [JE-18](#je-18), which return P.
  - **Top-level key order** of `flow_input` and of a script's arguments is unstable ([JE-16](#je-16)); fixtures must not depend on it.
  - As a result, `Object.keys(previous_result)` and `Object.keys(results.<same step>)` can differ in order.
- **Evidence:** column types `backend/migrations/20220123221903_first.up.sql:130`, `:161-162`, `backend/migrations/20250201124431_v2_job.up.sql:31`; in-memory passing of the last result `backend/windmill-worker/src/worker_flow.rs:1477-1488`, `:2086-2099`; `backend/windmill-jseval/src/lib.rs:805-817`; `backend/windmill-worker/src/js_eval.rs:54-72`; [PostgreSQL JSON types](https://www.postgresql.org/docs/current/datatype-json.html) ("jsonb does not preserve white space, does not preserve the order of object keys, and does not keep duplicate object keys").
- **Conformance:** [JE-44/a–c](conformance.md#je-44)

<a id="je-v6"></a>
### JE-V6 · The exact spelling, not the module id, decides memory or database

**Scope:** core · **Confidence:** high · **Status:** confirmed

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
  - `backend/windmill-runtime-nativets/` embeds `deno_core` (V8) to run **`nativets` scripts**: the in-worker "native" TypeScript runtime, with its own `windmill-client.js` and fetch support. Its behaviour belongs to [languages](languages.md).
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

None of these blocks a source-derived core rule; they confirm exact text or end-to-end behaviour on a real Windmill v1.767.0.

<a id="q-je-1"></a>
- **Q-JE-1** · affects [JE-12](#je-12), [JE-21](#je-21), [JE-9](#je-9) · What is the exact stack text appended after `Error: <message>`, the syntax diagnostic for a rewritten `await` inside a non-async function, the display of a thrown non-Error value (`throw "s"`, `throw {}`), and the non-callable diagnostic of case [JE-9/l](conformance.md#je-9)? *Settled by:* running those fixtures on the oracle and recording the step error messages; an equivalent offline check evaluates the same wrapped code with rquickjs 0.11.0 on quickjs-ng `be664ca` (both MIT-licensed), outside Windmill.

<a id="q-je-2"></a>
- **Q-JE-2** · affects [JE-1](#je-1) · Which host APIs really exist: `typeof` of `console`, `Intl`, `atob`, `btoa`, `TextEncoder`, `URL`, `setTimeout`, `structuredClone`, `queueMicrotask`? The source-derived expectations are not yet evidence. *Settled by:* the same engine run as Q-JE-1, or one oracle fixture with a transform per name.

<a id="q-je-3"></a>
- **Q-JE-3** · affects [JE-35](#je-35) · Do the end-to-end error envelopes and failure-module behaviour on routes (a), (b) and (c) match the source? *Settled by:* an oracle run of [JE-35/a–d](conformance.md#je-35), recording the parent's final result, each child row, and whether each failure module ran, as separate assertions.

<a id="q-je-4"></a>
- **Q-JE-4** · affects [JE-V2](#je-v2), [JE-23](#je-23) · After an input-transform failure, does a retry run the script with an empty argument map? *Settled by:* an oracle run of [JE-V2/c](conformance.md#je-v2), recording both attempts' recorded arguments (O2) and outcomes (O5).

<a id="q-je-5"></a>
- **Q-JE-5** · affects [JE-36](#je-36) · Are the `previous_result` values for collapsed and non-collapsed loops as traced, including a simple while loop in a **preview** run? *Settled by:* an oracle run of [JE-36/a–e](conformance.md#je-36), with the while-loop case run once through the flow-preview API and once as a deployed flow.

<a id="q-je-6"></a>
- **Q-JE-6** · affects [JE-V1](#je-v1) · What eventually happens to a parallel loop with `parallelism: 0` after every child is held at enqueue (held for 14 days, cancelled, or released by a monitor)? The initial hold is source-settled; a permanent stall is not asserted. *Settled by:* tracing the suspension timeout and monitor paths ([control flow](control-flow.md)), or an oracle run that observes the loop.

<a id="q-je-7"></a>
- **Q-JE-7** · affects [JE-33](#je-33) · What is the exact fractional-second format of `WM_SCHEDULED_FOR`? *Settled by:* scheduled oracle runs at known timestamps, one with no fraction and some with controlled fractional values, whose `skip_expr` compares `WM_SCHEDULED_FOR` against complete candidate strings (only `skip_expr` can read the name), or the pinned chrono `Display` source. Predicates such as `WM_SCHEDULED_FOR.endsWith(' UTC')` and `WM_SCHEDULED_FOR.includes('.')` show the suffix and whether a fraction is present, but not the digit count.

<a id="q-je-8"></a>
- **Q-JE-8** · affects [JE-19](#je-19) · Do reads in a restarted run reach the original-run fallback, or are they already served by module statuses copied into the restarted run? *Settled by:* an oracle restart of [JE-19/e](conformance.md#je-19) in which `b` fails only in the original run and succeeds on restart, so `c` is reached, plus inspection of the restarted run's flow status through the API. The value `17` alone does not distinguish the two paths: to exercise the fallback itself, read a node absent from the restarted status, and keep the two assertions separate.

<a id="q-je-9"></a>
- **Q-JE-9** · affects [JE-44](#je-44) · Does the `jsonb` key ordering (shorter keys first, then bytewise) hold for every key set the fixtures use? *Settled by:* Postgres alone (`SELECT '<json>'::jsonb`) on the oracle's Postgres version; no Windmill is needed.

<a id="q-je-10"></a>
- **Q-JE-10** · affects [JE-19](#je-19) · Does a subflow called by path that reads its caller's step id behave as traced (it reads the caller's result)? *Settled by:* the oracle run of [JE-19/d](conformance.md#je-19), using distinct ids.

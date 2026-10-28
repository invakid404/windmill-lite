# Languages and script wrappers

This area covers how one script step runs: from the args map the worker hands it, through the language wrapper, to the JSON value (or error) that comes back. Bun, the default TypeScript runtime, is covered in depth and tagged `core`, together with codebase bundles ([WR-47](#wr-47) to [WR-50](#wr-50)) and the in-worker native runtime that runs `nativets` and `bunnative` steps ([section H](#h-the-native-runtime-nativets-and-bunnative)); Deno, Python, Go and Bash are covered at summary level and tagged `later`. Neighbouring areas take over at the edges: [execution](execution.md) and [control flow](control-flow.md) for what a flow does with a step's result or error, [expressions](expressions.md) for expression evaluation, [client API](client-api.md) for the `wmill` client surface, [OpenFlow](openflow.md) for the flow model and sync-folder loader, and [oracle](oracle.md) for the oracle harness.

**Pinned version:** Windmill `v1.767.0` (commit `6de4ec0f665ad856002495c4d86269105ec28ad7`).
Source pointers are `path:line @ v1.767.0` relative to the Windmill repository root. Status, scope and confidence tags are defined in the [index](README.md#legend).

**ID prefixes in this file:** `WR-` script-wrapper findings; `WR-Vn` findings were added during validation; `Q-WR-` open questions; `PD-` proposed design decisions (only in [Design notes](#design-notes-not-windmill-behaviour)).

**Terms used in this file.**

- **Wrapper:** the small program Windmill generates for each job. It loads the user's script, calls its entrypoint and writes the result. For Bun it is `wrapper.mjs`.
- **Entrypoint:** the exported function the wrapper calls: `main` by default, or the name given by an entrypoint override (WR-12).
- **Job directory:** the per-job working directory. The worker writes `args.json` (the inputs) into it, plus an empty `result.json` that the wrapper fills.
- **Args map:** the job's named inputs, a JSON object.
- **Oracle:** a real Windmill CE `v1.767.0` instance used to record black-box behaviour ([oracle](oracle.md)). Every expectation in this area is source-derived; none has been recorded on the oracle yet ([Q-WR-20](#q-wr-20)).
- In error messages, `\n` is the JSON escape for a newline. `…` marks free text that a test should not match exactly.

## Summary for the design

1. **One fresh process per step.** Every Bun step runs in a new process with a new working directory. The module is evaluated before args are read. Pending work is cut off at `process.exit`, right after the result is written ([WR-1](#wr-1), [WR-2](#wr-2)). No in-process state survives between jobs, but disk caches do. How windmill-lite's warm harness reproduces this is a design decision ([Q-WR-19](#q-wr-19)).
2. **Args bind by name, from an AST parse of the TypeScript signature.** A missing key gives `undefined`, `null` stays `null`, extra keys are dropped and nothing is coerced ([WR-6](#wr-6), [WR-7](#wr-7)). With an entrypoint override and no parsed parameters, the values of all non-underscore keys are passed: integer-like keys first, ascending, then the rest in **unspecified** order ([WR-V1](#wr-v1), [WR-12](#wr-12)). Flow preprocessors take that path. An arrow-function `main` without an override gets no arguments. The last `function main` declaration wins (overloads and `declare` count), a `this` parameter breaks the generated wrapper, and a decorator anywhere fails the parse ([WR-51](#wr-51)).
3. **Only `Date` parameters are hydrated.** The test is the parser's resolved type plus a truthiness check on the value. Local aliases, parentheses and two-member nullable unions count; arrays, three-member unions and imported aliases do not ([WR-8](#wr-8), [WR-V3](#wr-v3)). A qualified name counts by its last segment (`globalThis.Date`), and aliases are matched by their snake-case names, the later of two colliding ones winning ([WR-51](#wr-51)).
4. **Key order at the step boundary depends on the layer** ([WR-10](#wr-10), [WR-V8](#wr-v8)):
   - the top-level args object is unordered;
   - unresolved nested values arrive in Postgres `jsonb` order. This is established from source, including the database decoder, but not yet observed ([Q-WR-4](#q-wr-4));
   - `$jsonvar:` and `$encrypted:` substitutions keep their own source order;
   - a stored `$res:` value keeps `jsonb` order;
   - a script preprocessor's output reaches `main` in memory;
   - JavaScript enumerates integer-like keys first.

   No single sort reproduces all of this. What an *expression* sees is a separate question ([expressions](expressions.md)).
5. **Results are serialised as `JSON.stringify(value ?? null)`, with an `undefined`→`null` replacer and a BigInt→string patch.** So `undefined` fields are kept as `null` ([WR-13](#wr-13), [WR-14](#wr-14), [WR-9](#wr-9)). Exit 0 is only the *initial* success classification. Completion can still fail the job, through `wm_failure`, an empty result or the size limits ([WR-4](#wr-4), [WR-17](#wr-17), [WR-V2](#wr-v2), [WR-15](#wr-15)).
6. **Error shapes.** There are four:
   - **Wrapper catch:** `{message, name, stack, step_id?, extra?}` ([WR-18](#wr-18)).
   - **Escaped failure:** a log-tail `ExecutionErr` with `exit_code`. The exit code Bun produces is pending ([WR-19](#wr-19), [WR-20](#wr-20), [Q-WR-2](#q-wr-2)).
   - **Execution-handler error:** `execution error:\n…` ([WR-37](#wr-37)).
   - **Empty result:** `InternalErr` without `step_id` ([WR-V2](#wr-v2)).

   All of them are stored as `{"error": …}`, except values whose top-level `error.name` is `ManualFailure` ([WR-21](#wr-21), [WR-V5](#wr-v5)).
7. **Timeouts are immediate SIGKILLs.** The "job-specific" timeout wording appears only when the requested timeout is strictly below the instance cap ([WR-37](#wr-37)).
8. **Imports resolve by path to the latest deployed script.** Relative, `/f/` and `/u/` imports go to the latest deployed version, served by the `raw_unpinned` endpoint both at run time and during lock generation ([WR-28](#wr-28), [WR-V9](#wr-v9)). Inline flow scripts use the flow's path as their import base. A module's explicit `path` is dropped when the flow is deployed, so it sets the base only for the deploy's dependency job and for raw runs such as previews ([WR-29](#wr-29)). Pin removal and the `.ts` rewrite are whole-text replacements: they also reach dynamic-import literals, strings and comments, and they can collide ([WR-V4](#wr-v4), [WR-26](#wr-26), [WR-38](#wr-38)). Their specifier lists come from a TSX parse (which rejects angle-bracket type assertions) and from Bun's `scanImports` (which rejects a hashbang line), and `..` in a relative import is resolved over the whole request URL ([WR-52](#wr-52)). WR-28 is disputed only in its later dynamic-import consequence ([Q-WR-16](#q-wr-16)).
9. **Some loader decisions are textual, others use an AST.** Annotation parsing, workflow-as-code detection and import rewriting are textual scans. Signature parsing uses an AST. An equivalent loader must reproduce each one as it is ([WR-39](#wr-39), [WR-31](#wr-31), [WR-32](#wr-32)).
10. **Pins and environment.**
    - The v1.767.0 images ship Bun `1.3.10`. That is the image recipe; the running oracle's version must still be recorded ([WR-3](#wr-3)).
    - Bun jobs see `BASE_URL` (not `BASE_INTERNAL_URL`), `WM_TOKEN` and the `WM_*` reserved variables ([WR-22](#wr-22)).
    - `npm:` specifiers stay outside core until [Q-WR-8](#q-wr-8) is settled.
    - Details for the later languages are in [WR-33](#wr-33) to [WR-35](#wr-35), [WR-V6](#wr-v6) and [WR-V7](#wr-v7).
    - Bundled mode fixes `process.env.NODE_ENV` as `"development"` at build time ([WR-53](#wr-53)), and the `schema_validation` line is read by its own exact-line rule, not as a [WR-31](#wr-31) annotation ([WR-54](#wr-54)).
    - Of the 63 findings, 57 are confirmed and 4 are `drafted` ([WR-51](#wr-51) to [WR-54](#wr-54), written from the pinned source and not yet independently checked). Parts of WR-15, WR-29, WR-32 and WR-48 revised on 2026-10-28 are drafted too; their tag lines say which. WR-16 and WR-28 are disputed only in their `later` portions.
11. **Native steps run in an in-worker V8 isolate with their own boundary rules** ([WR-40](#wr-40) to [WR-46](#wr-46)). `nativets` steps, and Bun steps whose header says `//native`, get a fresh isolate per job, no process and no files. Compared with Bun:
    - arguments bind by name with `JSON.parse` and no `Date` hydration ([WR-41](#wr-41));
    - the result is `JSON.stringify(value ?? null)` with **no replacer**, so `undefined` fields are **omitted** and a `BigInt` fails the job ([WR-42](#wr-42));
    - a thrown `Error` is stored as exactly `{message, stack, name}`, with no `step_id` and no `extra`, and any other thrown value gives three `null`s ([WR-43](#wr-43));
    - the timeout text is `Query timeout after (>Ns)` ([WR-44](#wr-44));
    - `process` is a module-level `{env}` object, and the global surface is a fixed list of web APIs ([WR-45](#wr-45)).
12. **Codebase bundles** ([WR-47](#wr-47) to [WR-50](#wr-50)). The pinned CLI bundles with esbuild `0.28.0`, `format` defaulting to `cjs`, and passes `external`, `inject`, `define` and `banner` through unchanged. Assets travel in a tar that the worker unpacks into the job directory. CJS bundles run on the image's Node 20 and ESM bundles on Bun, unless the source's header says otherwise: `//native` wins over both, and `//nodejs` sends ESM to Node. The codebase id depends only on the files under `relative_path` and the entry's settings. `wmill sync push` uses it to decide which scripts to push, but `wmill script push` with generated metadata uploads anyway, and even a deploy the server treats as a no-op rewrites the stored bundle under the same hash. CE in standalone mode, the oracle's topology, deploys and runs codebase scripts.

## A. Bun: process model and lifecycle

<a id="wr-1"></a>
### WR-1 · One fresh OS process per script job, in a fresh job directory

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Per job:** the worker creates a new directory `<worker_dir>/<job_id>` and spawns one new `bun run` process with that directory as its working directory. The process exits when the job finishes.
  - **In-process state never carries over:** module-level state, globals, timers and in-memory caches are gone for the next job.
  - **Disk state does persist across jobs:** the dependency cache (`BUN_INSTALL_CACHE_DIR`), the bundle cache ([WR-27](#wr-27)) and a `same_worker` flow's `./shared` files ([WR-24](#wr-24)).
  - **Cleanup:** the job directory is deleted after the job unless `KEEP_JOB_DIR` is set, or the job is a `same_worker` **flow job**. In that case the flow job's directory is kept, not each step's.
  - **The only long-lived Bun process** is the EE dedicated-worker mode ([WR-36](#wr-36)).
- **Evidence:** `backend/windmill-worker/src/worker.rs:1923-1929` (`create_job_dir`), `:3040` (called per job), `:3254-3256` (cleanup); `backend/windmill-worker/src/bun_executor.rs:2337-2416` (spawn), `:2368-2436`, `:2420-2436` (`handle_child` waits for exit) (all @ v1.767.0).
- **Conformance:** [WR-1/a, WR-1/b](conformance.md#wr-1)
- **Design note (not Windmill behaviour):** windmill-lite's warm process diverges from this on purpose. The default mode, and a "fresh module state" conformance mode that reproduces this rule, are an open design decision ([Q-WR-19](#q-wr-19)).

<a id="wr-2"></a>
### WR-2 · The module is evaluated before args are read, and the entrypoint runs exactly once

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - The generated `wrapper.mjs` imports the user module statically. The module's top-level code, and that of everything it imports, therefore runs **before** `args.json` is read.
  - The wrapper then runs these steps in order:
    1. read `args.json` and parse it with `JSON.parse`;
    2. hydrate `Date` parameters ([WR-8](#wr-8));
    3. run the preprocessor if it applies ([WR-11](#wr-11));
    4. pass the args to the entrypoint ([WR-6](#wr-6)) and await it;
    5. write `result.json` ([WR-13](#wr-13));
    6. call `process.exit(0)`.
  - Work started by the entrypoint can still run while the entrypoint is being awaited and while the result is being written. It is cut off at `process.exit`. After that, unawaited promises, pending timers and open handles never complete, and a `beforeExit` hook cannot do async work.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1907-1970` (non-workflow wrapper template), `:1927-1947` (the run sequence), `:1937-1946` (awaited write, then exit) (all @ v1.767.0).
- **Conformance:** [WR-2/a, WR-2/b](conformance.md#wr-2)
- **Note:** the timer fixture WR-2/a assumes that the call and the result write finish within the timer's 2-second delay. The exit rule itself does not depend on that.

<a id="wr-3"></a>
### WR-3 · Runtime binary and version pin

**Scope:** core · **Confidence:** high (image recipe; the running oracle binary not inspected) · **Status:** confirmed

- **Rule:**
  - At v1.767.0 the official images copy Bun **`1.3.10`** to `/usr/bin/bun`. The `BUN_PATH` env var overrides the path.
  - Other runtimes in the same image:
    - `//nodejs` scripts run on the image's distribution `nodejs` (`/usr/bin/node`, overridable with `NODE_BIN_PATH`);
    - Deno is `2.2.1`;
    - Go is `1.26.0`.
  - The `windmill-client` package that matches this release is `1.767.0`.
  - These are facts about the image recipe. The version a running oracle actually reports has to be recorded separately: the oracle harness records `Bun.version` in its fixture metadata ([oracle](oracle.md)).
- **Evidence:** `Dockerfile:285` (`oven/bun:1.3.10`), `Dockerfile:283` (`denoland/deno:2.2.1`), `Dockerfile:141` (`GO_VERSION=1.26.0`), `docker/DockerfileSlim:73`; `backend/windmill-worker/src/worker.rs:607-610`; `typescript-client/package.json:4` (all @ v1.767.0).
- **Conformance:** [WR-3/a](conformance.md#wr-3)
- **Design note (not Windmill behaviour):** windmill-lite should pin the same Bun version and report it in its build info.

<a id="wr-4"></a>
### WR-4 · Process exit status and how the job outcome is first decided

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Before the process starts, the worker writes `args.json` and pre-creates an **empty** `result.json`. "Empty" therefore means the wrapper never reached its write.
  - **Exit status 0:** the job is *initially* classified as a success, and its result is the **raw text** of `result.json`, neither parsed nor validated at this point ([WR-15](#wr-15)). Completion can still fail the job: through `wm_failure` ([WR-17](#wr-17)), an empty result ([WR-V2](#wr-v2)) or the size limits ([WR-15](#wr-15)).
  - **Non-zero exit status:** the job fails.
    - If `result.json` is non-empty, its text **is** the error value (normally the wrapper's error object, [WR-18](#wr-18)).
    - If it is empty, a log-tail error is synthesised ([WR-20](#wr-20)).
  - **Killed by a signal:** the job fails with the message `process terminated by signal: …`.
- **Evidence:** `backend/windmill-worker/src/common.rs:122-153` (`create_args_and_out_file`), `:417-436`; `backend/windmill-worker/src/handle_child.rs:998-1020` (`process_status`); `backend/windmill-worker/src/result_processor.rs:586-625` (all @ v1.767.0).
- **Conformance:** covered by [WR-18](conformance.md#wr-18), [WR-20](conformance.md#wr-20) and [WR-V2](conformance.md#wr-v2).

## B. Bun: the argument path

<a id="wr-5"></a>
### WR-5 · `args.json` is the job's args map after `$var:`/`$res:` resolution, with internal keys removed

**Scope:** core (`$res:`, `$var:`, `$jsonvar:`); later (`$encrypted:`, `$NAME` substitution) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **What is written:** before spawning the process, the worker writes `args.json` as one JSON object, the job's args map.
    - The keys `_MODULES` and `_TEMP_SCRIPT_REFS` are removed.
    - Other underscore keys stay, for example `_ENTRYPOINT_OVERRIDE` on preprocessor steps.
    - A job with no args gets `{}`.
  - **Which args are resolved:** an arg is resolved only if its raw JSON text contains `$var:`, `$jsonvar:`, `$res:` or `$encrypted:` anywhere. Any other arg is copied through as raw JSON text, unchanged (see [WR-10](#wr-10) for key order).
  - **How a qualifying arg is walked:** every string value inside it is examined.
    - The walk recurses into objects at any depth. Entering recursion depth 50 is an **error**, not a silent stop: ``Maximum resource/variable interpolation depth (50) exceeded for `<name>`; this usually indicates a circular `$res:` or `$var:` reference``.
    - It recurses into arrays only at nesting depth ≤ 2, and only when the array has ≤ 1000 items. Deeper or larger arrays are left alone.
  - **What each string becomes:**

    | String value | Becomes |
    |---|---|
    | `"$var:<path>"` | the variable's value, as a JSON string |
    | `"$jsonvar:<path>"` | the variable's value, parsed as JSON |
    | `"$res:<path>"` | the resource value, fetched in interpolated form, so the API resolves nested `$var:`/`$res:` inside it. The path must have at least 2 `/` segments, as `u/a/pg` does, unless it starts with `CUSTOM_INSTANCE_DB/`. |
    | `"$encrypted:<ct>"` | decrypted, then parsed as JSON. The key is built from the workspace **and the root job id**. The plaintext is registered for log masking. |
    | any other string starting with `$`, e.g. `"$NAME"` | if `NAME` is a reserved variable ([WR-22](#wr-22)), that variable's value; otherwise unchanged |

  - Matching looks at the value only. The declared parameter type plays no part.
  - Resolution happens after the job is pulled and before `args.json` is written. That timing matters for key order ([WR-V8](#wr-v8)).
- **Evidence:** `backend/windmill-worker/src/common.rs:122-153` (writes `args.json`), `:163-172` (trigger pattern), `:174-210` (`transform_json`), `:265-399` (`transform_json_value`), `:325-337` (encryption context) (all @ v1.767.0). Docs: <https://www.windmill.dev/docs/core_concepts/resources_and_types>.
- **Conformance:** [WR-5/a–e](conformance.md#wr-5)
- **Note:** resolved values come through the platform interface ([client API](client-api.md)). Where `$res:` strings come from in static transforms is covered in [expressions](expressions.md).

<a id="wr-6"></a>
### WR-6 · How args bind to the entrypoint

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** let *P* be the parsed parameter list of the entrypoint (`main`, or the override name, [WR-12](#wr-12)). The worker gets *P* from the TypeScript signature parser, which parses the module as an AST ([WR-39](#wr-39)). Binding then takes one of three branches.
  1. ***P* is non-empty** (with or without an override): named binding.
     - The parsed args object is destructured by the names in *P*, and the values are passed positionally, in declaration order.
     - A missing key gives `undefined`, so a JavaScript default parameter value applies.
     - An explicit `null` stays `null`, and the default does **not** apply.
     - Keys that match no parameter are dropped silently.
     - Nothing is coerced: `"5"` stays a string even for a `number` parameter. The only exception is `Date` ([WR-8](#wr-8)).
     - The worker never injects schema defaults.
  2. ***P* is empty and an override is present** ([WR-V1](#wr-v1)): the values of every key that does not start with `_` are passed, in the parsed args object's own-key order:
     - integer-like keys first, ascending;
     - then the other keys in `args.json` text order, which is **unspecified**, because the outer map is a hash map ([WR-10](#wr-10)).
  3. ***P* is empty and there is no override** (an arrow-function `main`, or a zero-parameter `main`): the entrypoint is called with **no arguments**.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1709-1722` (parse), `:1754-1765` (name list and spread selection), `:1913-1937` (destructure and call); `backend/parsers/windmill-parser-ts/src/lib.rs:304-464` (`parse_deno_signature`); `backend/windmill-queue/src/jobs.rs:2753-2757`; `backend/windmill-worker/src/common.rs:128-146` (all @ v1.767.0).
- **Conformance:** [WR-6/a–f](conformance.md#wr-6)

<a id="wr-7"></a>
### WR-7 · Which declarations count as the entrypoint, and the parser's edge cases

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** the entrypoint is named `main` unless an entrypoint override is set ([WR-12](#wr-12)).
  - **What the parser recognises** (it walks the module's top-level declarations):
    - a `function main(...)` declaration, exported or not;
    - `const main = workflow(async (...) => …)`, the workflow-as-code form, which is `out` of scope ([WR-32](#wr-32)).
  - **What runs:** the wrapper calls the module's **exported** `main`. If that export is missing or is not a function, the job fails with `main function is missing` (name `Error`). This also happens for a non-exported `function main` and for an `export default function`.
  - **Edge cases:**
    - **Arrow-function `main`:** `export const main = async (x) => …` is **not** recognised by the parser, so its parameter list is empty. The function is still exported and still called, but with **no arguments**, so every parameter is `undefined`. With an override, [WR-V1](#wr-v1) applies instead. The parser classifies an arrow-only script as auto-kind `lib`. What that means for the deployed schema belongs to [OpenFlow](openflow.md).
    - **Destructured object parameter:** `main({a, b}: T)` is recorded under the synthetic name `anon1` (then `anon2`, and so on, in order). Its value comes from the args key `anon1`; there is no special mapping.
    - **Rest parameter:** `(...xs)` and other unsupported patterns make parsing fail with `parameter syntax unsupported: …`. The job errors before it runs.
    - **Defaults:** the parser skips default values. JavaScript applies them at run time.
- **Evidence:** `backend/parsers/windmill-parser-ts/src/lib.rs:304-424` (AST signature), `:378-424` (entrypoint detection: function declarations matched by name, variable declarations only through `workflow(...)`), `:428-438` (`lib` auto-kind), `:528-636` (`parse_param`; `anon{n}` at `:565-569`, `:614-626`); `backend/windmill-worker/src/bun_executor.rs:1933-1935` (missing-function error) (all @ v1.767.0).
- **Conformance:** [WR-7/a–d](conformance.md#wr-7)
- **Note:** which of several `main` declarations counts, `this` parameters, the numbering of destructured parameters and the syntax the parse accepts are in [WR-51](#wr-51).

<a id="wr-8"></a>
### WR-8 · Which parameter annotations are hydrated as `Date`

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Hydrated:** the wrapper hydrates a parameter exactly when the parser resolves its annotation to the parser type `Datetime`. That happens for:
    - the type reference `Date`, matched by name before any local symbol;
    - a parenthesised type that resolves to `Date`;
    - a **local** top-level `type` alias whose right-hand side resolves to `Date`, recursively ([WR-V3](#wr-v3)). Aliases are looked up by their snake_case name, and their position in the file does not matter;
    - a **two-member** union of one of the above with `undefined` or `null`;
    - an optional parameter `d?: Date`.
  - **Not hydrated** (the value arrives as parsed JSON):
    - unions of three or more members, such as `Date | null | undefined`, which resolve to an unknown type;
    - arrays (`Date[]`), tuple elements and object members, because hydration is not recursive;
    - interfaces;
    - aliases imported from another module.
  - **The runtime test:** a **truthy** value becomes `new Date(value)`; a falsy value becomes `undefined`.
    - So `""`, `0`, `null` and `false` all become `undefined`, and a JavaScript default then applies.
    - A number is read as epoch milliseconds.
    - An unparsable string becomes an Invalid Date, with no error.
    - A missing input stays `undefined`; no `Date` is manufactured for it.
  - **Nothing else is hydrated:**
    - `Base64` (parser type `Bytes`) stays a string.
    - `Uint8Array`, `Buffer` and other class names become resource types in the parser, and the value passes through as plain JSON.
    - `S3Object` and other resource types pass through as plain JSON objects.
    - Numbers, booleans and the `Email`, `Sql` and `DynSelect_*` types are not coerced.
  - When a script preprocessor applies, hydration uses the **preprocessor's** parameters, not `main`'s ([WR-11](#wr-11)).
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1738-1752`; `backend/parsers/windmill-parser-ts/src/lib.rs:378-405` (symbol table), `:773-809` (alias resolution), `:925-927` (parentheses), `:942-1006` (union handling), `:1033-1056` (`Date`→`Datetime`, `Base64`→`Bytes`, other type references → resource type), `:1067-1090` (literal-union fallback), `:1235-1243` (all @ v1.767.0).
- **Conformance:** [WR-8/a–f](conformance.md#wr-8)
- **Note:** a qualified type counts by its last segment (`globalThis.Date` is hydrated), and alias names are compared in snake case, the later of two colliding declarations winning ([WR-51](#wr-51)).

<a id="wr-9"></a>
### WR-9 · The JSON boundary into a step: args are JSON text parsed with `JSON.parse`

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Every Bun step receives its inputs as JSON text and parses it with plain `JSON.parse`, with no reviver.
  - **Consequences for values from earlier steps:**
    - they arrive as plain JSON data;
    - a `Date` returned upstream arrives as an ISO string, unless this step's parameter is typed `Date` ([WR-8](#wr-8));
    - a `Map` arrives as `{}`;
    - an upstream `undefined` field arrives as `null` ([WR-14](#wr-14));
    - integers beyond 2^53 lose precision;
    - object identity and aliasing are cut.
  - This describes a single step boundary. For key order inside the arriving values, see [WR-10](#wr-10); there is no universal `jsonb` sort.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1913` (args read and parsed), `:1944`; `backend/windmill-worker/src/common.rs:132-135` (`args.json` written from the args map) (all @ v1.767.0).
- **Conformance:** [WR-9/a](conformance.md#wr-9)
- **Design note (not Windmill behaviour):** this is the reference behaviour for a normalising walk at step boundaries, used in place of a real serialise-and-parse round trip. Such a walk must implement exactly [WR-13](#wr-13) and [WR-14](#wr-14), plus standard `toJSON` handling.

<a id="wr-10"></a>
### WR-10 · Key order at the step-argument boundary, by layer

**Scope:** core · **Confidence:** high (source-traced, including the database decoder; not oracle-observed) · **Status:** confirmed

- **Rule:** on the normal SQL worker path, the order in which a Bun step sees object keys depends on the layer.

  | Layer | What happens | Key order the step sees |
  |---|---|---|
  | 1. Top-level args object | The worker decodes the stored `jsonb` args into a hash map from arg name to raw JSON fragment, then re-serialises that map to write `args.json`. | **Unspecified**; it can vary between runs. Named binding cannot observe it ([WR-6](#wr-6) branch 1); the override fallback can (branch 2). |
  | 2. Nested values of an arg that needed no resolution ([WR-5](#wr-5)) | The fragment is the text exactly as pulled. Every pushed job's args are stored in `jsonb` first, so this covers static inputs, evaluated transforms and previous results alike. | Postgres `jsonb` output order: shorter keys first, keys of equal length in byte order. Duplicate keys collapse to the last value; numbers use `jsonb` numeric spelling (so spellings such as `1e2` are normalised). |
  | 3. Args that needed resolution (they contain `$var:`, `$jsonvar:`, `$res:` or `$encrypted:`) | The fragment is parsed into an order-preserving JSON value (`serde_json` is built with `preserve_order`), transformed and re-serialised. None of the substituted values passes through `jsonb` before `args.json` is written. | Untouched parts keep their pulled `jsonb` order. A `$jsonvar:` value keeps the variable string's own order: a variable holding `{"bb":2,"a":1}` yields `bb` before `a`. `$encrypted:` plaintext likewise keeps its own order. An unchanged stored resource fetched by `$res:` keeps `jsonb` order, while a `$jsonvar:` object substituted inside that resource keeps its variable string's order. |
  | 4. Script preprocessor output ([WR-11](#wr-11)) | Handed to `main` **in memory**. Persisting the preprocessed args later does not affect this call. | JavaScript insertion order of the object the preprocessor built. |
  | 5. In JavaScript, after `JSON.parse` | Own-key enumeration always lists integer-like keys first, in ascending order, wherever they appear in the text. | Text order applies only to the remaining keys. |

  - **No single sort:** no universal "sort keys in `jsonb` order" step reproduces Windmill. The layer decides.
  - **Precision:** numbers lose precision only later, in `JSON.parse` ([WR-9](#wr-9)).
- **Evidence:**
  - Storage and pull: `backend/migrations/20250201124431_v2_job.up.sql:31` (`args JSONB`); `backend/windmill-common/src/worker.rs:615-640` (the pull query selects `j.args`); `backend/windmill-queue/src/jobs.rs:2753-2757`, `:3240` (the args decode into a map of raw fragments); `backend/Cargo.toml:435` (`serde_json` with `preserve_order` and `raw_value`).
  - Writer and resolver: `backend/windmill-worker/src/common.rs:128-146`, `:174-210`, `:268-399` (`$jsonvar:` at `:293-301`), `:303-337`.
  - Wrapper: `backend/windmill-worker/src/bun_executor.rs:1779-1794`, `:1913`, `:1931-1937`.
  - Resources: `backend/migrations/20220123221903_first.up.sql:298-301` (the resource value is `jsonb`); `backend/windmill-store/src/resources.rs:659-681`, `:684-767` (the interpolation endpoint returns the value as JSON), `:820-829` (`$jsonvar:` parses the variable text), `:969-983` (values replaced in place, keys not rebuilt); `backend/windmill-common/src/client.rs:95-112` (all @ v1.767.0).
  - Outside Windmill (supplemental): `backend/Cargo.lock` pins sqlx-postgres 0.8.6 and serde_json 1.0.151. The [SQLx 0.8.6 JSON decoder](https://github.com/launchbadge/sqlx/blob/v0.8.6/sqlx-postgres/src/types/json.rs) strips the `jsonb` version byte and deserialises the remaining text directly, without building and re-serialising a generic value. serde_json 1.0.151 `src/raw.rs:385-413`, `:490-509` captures raw fragments as text. The PostgreSQL 16 [`jsonb` output](https://github.com/postgres/postgres/blob/REL_16_STABLE/src/backend/utils/adt/jsonb.c) and [key comparator](https://github.com/postgres/postgres/blob/REL_16_STABLE/src/backend/utils/adt/jsonb_util.c) order keys by byte length, then bytes. [PostgreSQL JSON types documentation](https://www.postgresql.org/docs/16/datatype-json.html). The oracle's PostgreSQL version was not inspected.
- **Conformance:** [WR-10/a–e](conformance.md#wr-10)
- **Note:** what key order an *expression* inside a transform sees for `results.x` (the in-memory previous result or the fetched results) is a separate question, owned by [execution](execution.md) and [expressions](expressions.md) ([Q-WR-4](#q-wr-4)).
- **Design note (not Windmill behaviour):** whether windmill-lite emulates layer 2's `jsonb` ordering or declares a documented deviation is [PD-2](#pd-2) ([Q-WR-18](#q-wr-18)).

<a id="wr-11"></a>
### WR-11 · Script-level `preprocessor` (trigger preprocessing)

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **When it runs:** a Bun script's own exported `preprocessor` runs only when both hold:
    - the job is marked as needing preprocessing (`preprocessed == false`, which is set when a trigger requests it);
    - the job is not the flow step `preprocessor` ([WR-12](#wr-12)).
  - **What the wrapper does:**
    1. parses the `preprocessor` signature and passes args to it by name;
    2. replaces the args object with the preprocessor's awaited return value;
    3. writes that value back to `args.json`, using the `undefined`→`null` replacer ([WR-14](#wr-14));
    4. calls `main` with the **in-memory** returned object. There is no JSON round trip between `preprocessor` and `main`, so a `Date` the preprocessor returns arrives in `main` as a real `Date`;
    5. after exit, the worker re-reads `args.json` and stores it as the job's new args.
  - **Date hydration:** only the **preprocessor's** `Date` parameters are hydrated; `main`'s are **not**.
  - **Missing preprocessor:** the job fails with `preprocessor function is missing`.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1519-1520` (condition), `:1724-1736` (parse), `:1738-1740` (dates from preprocessor args), `:1779-1794` (preprocessor block), `:1931-1937`, `:2440-2455` (args re-read) (all @ v1.767.0). Docs: <https://www.windmill.dev/docs/core_concepts/preprocessors>.
- **Conformance:** [WR-11/a](conformance.md#wr-11)

<a id="wr-12"></a>
### WR-12 · Entrypoint override, and the flow `preprocessor_module`

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **The override:** the job arg `_ENTRYPOINT_OVERRIDE` selects a named export to call instead of `main`. The parser and the wrapper both use that name.
    - It must match `^[A-Za-z_][A-Za-z0-9_]*$`. Otherwise the job fails with `Invalid entrypoint override …` before any wrapper runs.
  - **The flow preprocessor step:** a flow's `preprocessor_module` runs as a step with runnable path `<flow_path>/preprocessor` and step id `preprocessor` (`WM_FLOW_STEP_ID` = `preprocessor`). Its args are the **whole flow input** plus `_ENTRYPOINT_OVERRIDE: "preprocessor"`. Binding follows [WR-6](#wr-6):
    - a declared `function preprocessor(a, b)` binds `a` and `b` by name;
    - a `preprocessor` whose parsed parameter list is empty (an arrow export, or a zero-parameter declaration) receives the values of every non-underscore flow-input key: integer-like keys ascending, then the other keys in unspecified order ([WR-V1](#wr-v1)).
  - **Missing export:** `preprocessor function is missing`, or `<name> function is missing` for another override.
  - What happens to the preprocessor's result (it becomes the flow input seen by later steps) is covered in [execution](execution.md).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3906-3912` (preprocessor step args), `:5076-5086` (`get_path`); `backend/windmill-worker/src/worker.rs:4815-4827` (validation); `backend/windmill-queue/src/jobs.rs:6453` (override taken from args); `backend/windmill-worker/src/bun_executor.rs:1754-1765`, `:1757`, `:1933-1936` (all @ v1.767.0).
- **Conformance:** [WR-12/a–c](conformance.md#wr-12)

<a id="wr-51"></a>
### WR-51 · Signature-parser details: which declaration counts, `this`, qualified types, aliases and the accepted syntax

**Scope:** core · **Confidence:** high (the declaration walk, parameter naming, type-name lookup, snake case); medium (the decorator failure and the `this` failure end to end; Bun's message is from the release binary run outside Windmill) · **Status:** drafted

- **Rule:** these details complete [WR-7](#wr-7) and [WR-8](#wr-8).
  - **The parse.** The signature parser reads the module as TypeScript without JSX, at the parser's default settings. A fatal syntax error fails the parse, and the job fails before it runs with the [WR-37](#wr-37) shape: `error.message` starts `execution error:\nError: Error while parsing code, it is invalid TypeScript:`. What the parse accepts:
    - auto-accessors (`accessor x = 1` in a class), and import attributes in both spellings, `with { … }` and `assert { … }`, when they follow the specifier on the same line;
    - **not decorators.** Decorator syntax is off in this configuration, so a module with `@dec` before a class or member fails the parse, although Bun itself would run it. The pin-removal parse of [WR-52](#wr-52) rejects them too.
  - **Which declaration gives the parameters.** The parser walks the top-level items in order. Every function declaration named like the entrypoint, exported or not, with a body or without one, replaces the parameter list taken so far, so the **last** one wins. TypeScript overload signatures and `declare function` both count. In the usual overload pattern the implementation comes last, so its signature decides both binding ([WR-6](#wr-6)) and `Date` hydration ([WR-8](#wr-8)).
    - `export default function main` is not such a declaration ([WR-7](#wr-7)).
    - The workflow-as-code variable form of [WA-33](client-api.md#wa-33) (`const main = workflow(async (…) => …)`) is taken only while no function declaration of the name has been seen. A `function main`, an overload or a `declare` included, that comes **before** it keeps the script from being flagged as workflow-as-code; one that comes after replaces the parameters and leaves the flag set. The variable declaration counts whether or not it is exported.
  - **Parameter patterns.**
    - A plain identifier gives its own name. In TypeScript the parser takes a **`this` parameter** as an ordinary identifier, so `main(this: T, a: number)` lists `this` and `a`. The Bun wrapper then destructures the args object by those names ([WR-6](#wr-6)), and JavaScript does not allow `this` there: the generated `wrapper.mjs` fails to parse, Bun exits with code 1 before `main` runs, and the job fails with the [WR-20](#wr-20) log-tail error of program `bun run`. Bun `1.3.10`'s text is `Cannot use abbreviated destructuring syntax for keyword 'this'.`
    - Object patterns, bare or with a default, are named `anon1`, `anon2`, …, counted among object patterns only: `main(a, {b}, {c} = {}, d)` lists `a`, `anon1`, `anon2` and `d`.
    - Array patterns, bare or with a default, and rest parameters fail the parse with `parameter syntax unsupported: …` ([WR-7](#wr-7)).
  - **Type names are matched by their last segment.** A qualified type such as `globalThis.Date` or `Ns.Date` is `Date` and is hydrated; `Ns.Base64` is `Base64`. Any other name is looked up among the module's top-level type aliases and interfaces, exported or not.
  - **Alias lookup goes through snake case.** Declared alias and interface names and referenced type names are all converted to snake case before they are compared:
    - words split at `_`, between a lower-case letter and an upper-case one, between a digit and an upper-case letter, and before the last capital of a run of capitals that is followed by a lower-case letter; never between a letter and a digit, or a digit and a lower-case letter. Words are lower-cased and joined with `_`;
    - for example `S3Object` gives `s3_object`, `S3object` gives `s3object`, `HTTPServer` gives `http_server`, `AaBC` gives `aa_bc`, and `typeNameHere` gives `type_name_here`;
    - so two declarations whose names convert alike collide, and the **later** one replaces the earlier: after `type DateLike = Date` and then `type Date_Like = string`, a parameter typed `DateLike` is a string and is not hydrated. A later interface replaces an alias the same way, unless the interface's own name is already in snake case.
  - **Aliases that refer to each other** (`type A = B; type B = A`) resolve to an object type with no properties, named after the alias looked up first; never to `Date`.
- **Evidence:** `backend/parsers/windmill-parser-ts/src/lib.rs:304-337` (the parse at default TypeScript settings and the fatal-error message), `:343-424` (the walk: aliases and interfaces keyed by snake case at `:382-399`, each function declaration of the name replacing the list at `:400-408`, the variable form only while no list was found at `:409-423`), `:528-636` (`parse_param`: identifiers, `anon{n}` counted only for object patterns, everything else refused), `:782-870` (alias resolution, with a placeholder that ends cycles), `:1011-1015` (the last segment of a qualified name), `:1033-1056` (`Date`, `Base64`, then the snake-case lookup at `:1048`); `backend/parsers/windmill-parser/src/lib.rs:159-164` (snake case: the default boundaries without the letter–digit and digit–lower ones); `backend/windmill-worker/src/bun_executor.rs:1754`, `:1915-1917` (the wrapper destructures by the parsed names) (all @ v1.767.0). Pinned dependencies (crates.io packages whose checksums match `backend/Cargo.lock`): swc_ecma_lexer 23.0.2, which swc_ecma_parser 24.0.3 uses, `src/common/syntax.rs:191-216` (TypeScript turns on auto-accessors and import attributes always, decorators only when asked), `src/common/parser/class_and_fn.rs:88-94` (decorators are not parsed when off), `:285-313` (a function without a body is still a function), `src/common/parser/ident.rs:194-195` (`this` as an identifier), `src/common/parser/module_item.rs:76-79` (`assert` or `with`); convert_case 0.6.0 `src/segmentation.rs:198-204`, `:245-248`, `:296-317` (the boundaries). Bun's message: the official `1.3.10` release binary, run on a wrapper of that shape.
- **Conformance:** [WR-51/a–d](conformance.md#wr-51)
- **Note:** the CLI generates a script's schema with a WebAssembly build of this parser (`windmill-parser-wasm-ts`, `cli/src/utils/metadata.ts:905-908`), so *(medium)* a `this` parameter is expected to appear there as a property named `this` too.

<a id="wr-v1"></a>
### WR-V1 · An override with zero parsed parameters passes the non-internal argument values

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **When it applies:** in the ordinary Bun wrapper, when an entrypoint override is present **and** its parsed parameter list is empty. Only those two conditions select it; there is no check that the job is a workflow-as-code child.
  - **What is passed:** the values of all arg keys that do not begin with `_`.
  - **Which entrypoints this reaches:** an arrow export (which the parser does not recognise) and an ordinary zero-parameter function.
  - **Order:** JavaScript own-key enumeration of the parsed args object. Integer-like keys come first, ascending. The other keys follow in `args.json` text order, which is unspecified ([WR-10](#wr-10) layer 1).
  - **Without an override,** an empty parsed signature still means zero arguments.
  - **Date hydration** still depends on the parsed signature.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1754-1765`, `:1931-1937` (all @ v1.767.0).
- **Conformance:** [WR-V1/a, WR-V1/b](conformance.md#wr-v1)
- **Note:** this fallback is part of [WR-6](#wr-6) (branch 2) and [WR-12](#wr-12). Flow preprocessing also uses the override, which is why it is `core`.

<a id="wr-v3"></a>
### WR-V3 · Local type aliases and parentheses take part in `Date` hydration

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - The wrapper chooses which parameters to hydrate from the parser's **resolved** parameter type.
  - The parser resolves locally declared type aliases recursively and unwraps parentheses. So an alias to `Date` is hydrated as `Date`, including a nullable two-member union that resolves to `Date`.
  - The wrapper does not hydrate recursively: a `Date` inside an array or an object member is not hydrated.
  - This parser does not resolve aliases imported from another module.
- **Evidence:** `backend/parsers/windmill-parser-ts/src/lib.rs:378-405`, `:773-809`, `:925-968`, `:1033-1056`; `backend/windmill-worker/src/bun_executor.rs:1738-1752` (all @ v1.767.0).
- **Conformance:** [WR-V3/a–c](conformance.md#wr-v3)
- **Note:** part of [WR-8](#wr-8).

<a id="wr-v8"></a>
### WR-V8 · Argument ordering depends on the layer and on post-pull transformations

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** on the normal SQL worker path:
  - **Top level:** stored job args are read into an outer hash map whose values are raw JSON fragments. That map is serialised again to create `args.json`, so the order of non-integer top-level keys is unspecified, even when the database supplied ordered JSON text.
  - **Unresolved nested values** keep their raw text from the pull.
  - **Args that need interpolation** go through the value resolver and are serialised after the pull. For example, `$jsonvar:` parses the variable's string contents into JSON. This build's `serde_json` preserves insertion order, and the new object does not pass through `jsonb` before `args.json` is written.
  - **Script preprocessing** is another exception: `main` receives the preprocessor's in-memory object, not the copy stored later.
  - **In JavaScript,** after `JSON.parse`, integer-index keys are enumerated first, wherever they appear in the text.
  - There is no universal rule to sort every object into `jsonb` order before each call.
- **Evidence:** `backend/windmill-queue/src/jobs.rs:2753-2757`, `:3240`; `backend/windmill-common/src/worker.rs:615-640`; `backend/windmill-worker/src/common.rs:128-146`, `:174-210`, `:294-302`; `backend/Cargo.toml:435` (`preserve_order`); `backend/windmill-worker/src/bun_executor.rs:1779-1794`, `:1913`, `:1931-1937` (all @ v1.767.0).
- **Conformance:** [WR-V8/a–d](conformance.md#wr-v8)
- **Note:** part of [WR-10](#wr-10). The separate ordering visible inside expressions still needs to be settled with [execution](execution.md) and [expressions](expressions.md).

## C. Bun: the result path

<a id="wr-13"></a>
### WR-13 · The result is `JSON.stringify(value ?? null, replacer)`, written to `result.json`

**Scope:** core · **Confidence:** high (source-traced; the top-level function case is pending [Q-WR-12](#q-wr-12)) · **Status:** confirmed

- **Rule:**
  - **How it is written:** the awaited return value first goes through `?? null` at the top level, then through `JSON.stringify` with a replacer that maps every `undefined` it sees to `null`. The text is written to `./result.json` in the working directory.
  - **Top-level values:** `undefined` and `null` both become `null`. `false`, `0` and `""` are kept.
  - **BigInt:** before calling the entrypoint, the wrapper installs a `BigInt.prototype.toJSON` that returns the decimal string. Every BigInt anywhere in the result therefore serialises as a string.
  - **Everything else follows standard `JSON.stringify` semantics:**
    - `toJSON` is called first, so Dates become ISO strings and an Invalid Date becomes `null`;
    - `Map` and `Set` become `{}`;
    - typed arrays become objects keyed by index;
    - a Node `Buffer` becomes `{"type":"Buffer","data":[…]}`;
    - class instances contribute only their own enumerable properties; getters on the prototype are dropped;
    - `NaN` and `±Infinity` become `null`, and `-0` becomes `0`;
    - functions and symbols are dropped from objects and become `null` in arrays;
    - a cyclic structure throws `TypeError`, which becomes the job error ([WR-18](#wr-18));
    - an object whose `toJSON` returns `undefined` serialises as `null`.
  - **A top-level function or symbol** survives `?? null`. `JSON.stringify` then produces no text, and handing that non-string to the file write is expected to fail inside the wrapper. The job then takes the [WR-18](#wr-18) path with a `TypeError` whose message depends on the runtime. The actual Bun `1.3.10` outcome is [Q-WR-12](#q-wr-12).
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1923-1925` (BigInt), `:1944-1946` (stringify and write); `backend/tests/bun_jobs.rs:93-205` (BigInt result `9007199254740993n` → `"9007199254740993"`), `:245-325` (`null`/`undefined` → `null`) (all @ v1.767.0).
- **Conformance:** [WR-13/a–d](conformance.md#wr-13)

<a id="wr-14"></a>
### WR-14 · Object fields holding `undefined` become `null`, not omitted

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Plain `JSON.stringify` omits such fields; Windmill does not. Because the replacer maps `undefined` to `null`, any object property whose value is `undefined` (or whose `toJSON()` returns `undefined`) is **kept, with value `null`**.
  - Downstream steps therefore see the key with `null`, not a missing key.
  - The preprocessor's write-back to `args.json` uses the same replacer ([WR-11](#wr-11)).
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1944`, `:1789` (all @ v1.767.0).
- **Conformance:** [WR-14/a](conformance.md#wr-14)

<a id="wr-15"></a>
### WR-15 · `result.json` is taken as raw text; size limits

**Scope:** core (raw handoff; empty case in WR-V2); later (size limits) · **Confidence:** high; medium (the check on a flow's own result) · **Status:** confirmed; drafted (the limit's reading and the stored failure, added 2026-10-28)

- **Rule:**
  - **Raw handoff:** on exit 0, the worker passes `result.json`'s text on without parsing it. An empty file fails at completion with `InternalErr` ([WR-V2](#wr-v2)).
  - **Size checks at completion:** the completion path computes `size_MiB = floor(bytes / 1048576)` over the result text it holds, before anything is stored.
    - When `size_MiB > 2` (that is, ≥ 3 MiB), a nested check runs first: if `size_MiB > MAX_RESULT_SIZE_MB` (default 500, so ≥ 501 MiB), the job fails with `ResultTooLarge` (`Result of job <id> is too large: <n>MB > MAX_RESULT_SIZE_MB=<m>MB. …`). This failure returns *before* the large-result warning, so a rejected result never logs that warning.
    - Otherwise, a result with `size_MiB > 2` is accepted and the large-result warning is logged. On the multi-tenant cloud this case is a `ResultTooLarge` failure instead.
    - So 2 MiB plus one byte triggers nothing, and 3 MiB triggers the warning.
  - **The limit.** `MAX_RESULT_SIZE_MB` is the worker's environment variable of that name, read once per process; a missing or unparsable value means 500. The limit is tested only inside the `size_MiB > 2` branch, so a limit below 3 acts as 3: with `MAX_RESULT_SIZE_MB=1`, a 2.5 MiB result is accepted without a warning and a 3 MiB result fails.
  - **The stored failure.** Completion hands a `ResultTooLarge` to the generic job-error handler, as for [WR-V2](#wr-v2). The job's stored value, which a parent flow also receives as the step's failure, is `{"error": {"message": M, "name": "ResultTooLarge"}}`, where `M` is `Result of job <id> is too large: <n>MB > MAX_RESULT_SIZE_MB=<m>MB.\nUse external storages such as the Windmill Object Storage to store large results: https://www.windmill.dev/docs/core_concepts/object_storage_in_windmill`. It has no `step_id`, `stack` or `exit_code`, and no `execution error:` prefix. The result itself is never stored, and the commit is not retried.
  - *(medium)* The same check runs when a flow job completes, so a flow whose own final result passes the limit fails the same way.
  - **On the cloud only,** the file reader also rejects any result strictly larger than 2 097 152 bytes.
- **Evidence:** `backend/windmill-worker/src/common.rs:417-436` (raw read; cloud-only limit); `backend/windmill-worker/src/worker.rs:1244`; `backend/windmill-queue/src/jobs.rs:922` (the variable and its default), `:950-955` (empty check), `:1006-1012` (no commit retry on `ResultTooLarge`), `:1075-1077` (the check runs first in the commit), `:1472-1500`, `:1477-1500` (size gate, nesting and message texts); `backend/windmill-common/src/error.rs:43-44`, `:103` (the name, and a display that is the message alone); `backend/windmill-worker/src/result_processor.rs:719-735` (a completion error goes to the job-error handler), `:1910-1926` (`handle_job_error`); `backend/windmill-common/src/worker.rs:2228-2233` (`error_to_value`: `message` and `name` only); `backend/windmill-worker/src/worker_flow.rs:2024`, `:2040` (a flow's completion goes through the same commit) (all @ v1.767.0).
- **Conformance:** [WR-15/a–c](conformance.md#wr-15)
- **Revision 2026-10-28 (source re-check):** added how `MAX_RESULT_SIZE_MB` is read (a limit below 3 MiB acts as 3) and the exact stored failure, which the rule named only by its error name and the start of its message. The check itself is unchanged. [FX-43](execution.md#fx-43) states the same limits from the flow side.

<a id="wr-16"></a>
### WR-16 · Result streaming: async-iterable results and `WM_STREAM: ` log lines

**Scope:** later · **Confidence:** high (source-traced) · **Status:** DISPUTED

The dispute covers only two clauses of the producing side, marked *(disputed)* below. Method lookup, the failure cases, extraction and merging are not contested.

- **Rule (as drafted):**
  - **Producing the stream:** if the awaited result is async-iterable, the wrapper does three things for each chunk, in order:
    1. **Lookup:** it reads the chunk's `replace` property by ordinary property lookup, own or inherited. For a string that is the built-in `String.prototype.replace`.
    2. **Call:** it calls that function with a global-newline pattern and the two-character replacement `\n`.
    3. **Concatenate:** it joins `WM_STREAM: ` and the returned value with JavaScript string `+`, and prints the result to stdout.
  - **Failures:** any exception in these steps takes the [WR-18](#wr-18) path. Typical exceptions:
    - `replace` is missing or not callable (numbers, `null`, plain objects) → `TypeError`;
    - `replace` throws → its exception;
    - `replace` returns something that cannot be converted to a string, such as a Symbol → `TypeError`.
  - *(disputed)* Other return values are converted the usual JavaScript way: an object becomes its `toString()`, e.g. `[object Object]`.
  - *(disputed)* If the printed text still contains raw newlines (possible only with a custom `replace`), only its first line carries the prefix, and the rest goes to the logs.
  - After iteration, the result is `null`. Any script can also print `WM_STREAM: ` lines itself.
  - **Extraction:** the log reader takes every stdout or stderr line that starts with `WM_STREAM: ` and has non-empty content. For each such line it:
    - turns each two-character `\n` back into a newline. This is lossy: a literal backslash-n in a chunk is decoded too;
    - removes the line from the logs;
    - appends it to the stream buffer.
  - **Merging into the final result** (only when the stream is non-empty):
    - an object result gets an extra field `wm_stream` holding the whole concatenated stream;
    - a `null` result is replaced by the stream string;
    - any other result is left unchanged.
- **Correction (validation):** two points.
  - String `+` first performs JavaScript primitive conversion, which may use `Symbol.toPrimitive` or `valueOf`. So not every object becomes its `toString()`. Example: a custom `replace` returns an object whose `valueOf` gives `7` and whose `toString` gives `x` → stream text `7`.
  - The worker classifies **every** output line on its own, checking it for the stream prefix and the other protocol prefixes. Lines after a raw newline do not all go to the logs. Example: a custom `replace` returns `a`, a raw LF, then `WM_STREAM: b` → the accumulated stream is `ab`, assuming the normal log budget and no masking.
  - Evidence: `backend/windmill-worker/src/bun_executor.rs:1938-1943`; `backend/windmill-worker/src/handle_child.rs:430-476`; `backend/windmill-common/src/result_stream.rs:4-15` (all @ v1.767.0).
- **Open:** [Q-WR-15](#q-wr-15)
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1919-1921`, `:1938-1943`; `backend/windmill-common/src/result_stream.rs:4-15`; `backend/windmill-worker/src/handle_child.rs:463-471`, `:998-1006`; `backend/windmill-worker/src/common.rs:438-463` (all @ v1.767.0).
- **Conformance:** [WR-16/a–g](conformance.md#wr-16)

<a id="wr-17"></a>
### WR-17 · Result markers `wm_failure` and `wm_labels`

**Scope:** later (`wm_failure`); out (`wm_labels`) · **Confidence:** high (retagging); medium (how a flow treats the pre-shaped failure) · **Status:** confirmed

- **Rule:**
  - **`wm_failure`:** after a *successful* run, if the result is a JSON object whose top-level `wm_failure` is a **string**, the run is retagged as failed.
    - The worker adds a top-level `error: {"name":"ManualFailure","message":<wm_failure>}` to the object and keeps all sibling fields.
    - That object is stored as the failure value **as-is**, without the usual `{"error": …}` wrapping ([WR-21](#wr-21), [WR-V5](#wr-v5)).
  - **`wm_labels`:** an array of strings that only labels the job row. The result is unchanged.
  - **A bad marker disables both:** if either marker has the wrong type (e.g. `wm_failure: true`), metadata parsing fails as a whole, and neither marker takes effect.
  - **Null markers:** `wm_failure: null` and `wm_labels: null` count as absent, not as malformed.
- **Evidence:** `backend/windmill-worker/src/result_processor.rs:104-129`, `:1575-1599`; `backend/windmill-queue/src/jobs.rs:639-673`, `:642-648`, `:698-720` (metadata parse with a default on failure), `:836-880` (all @ v1.767.0).
- **Conformance:** [WR-17/a, WR-17/b](conformance.md#wr-17)
- **Note:** `wm_failure` is `later` because it changes flow control. How a flow then handles the failure is covered in [execution](execution.md).

<a id="wr-v2"></a>
### WR-V2 · An empty successful result fails completion with `InternalErr`

**Scope:** core · **Confidence:** high (source-traced; not oracle-observed) · **Status:** confirmed

- **Rule:**
  - **When it happens:** on the normal SQL completion path, a script exits 0 but leaves `result.json` empty. That happens when the script calls `process.exit(0)` itself, or changes directory before the wrapper writes.
  - **What happens:** the run is first reported as a success. Completion then rejects the empty raw value (`Result of job is invalid json (empty)`) and hands it to the generic job-error handler.
  - **The stored failure,** which is also the value sent to the parent flow, holds an `error` object with `name: "InternalErr"` and the internal-error display message.
  - **Fields this handler does not add:** `step_id`, `stack`, `exit_code` and the `execution error:` prefix. That holds for a flow step too.
- **Evidence:** `backend/windmill-queue/src/jobs.rs:950-955`; `backend/windmill-worker/src/result_processor.rs:673-742`, `:1487-1510`, `:1881-1907`, `:1910-1967`; `backend/windmill-common/src/worker.rs:2228-2233`; `backend/windmill-common/src/error.rs:61-62`, `:105-108`, `:233-239` (all @ v1.767.0).
- **Conformance:** [WR-V2/a](conformance.md#wr-v2)

## D. Bun: the error path

<a id="wr-18"></a>
### WR-18 · The error object the wrapper writes when the entrypoint, the preprocessor or serialisation throws

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** any exception that escapes the wrapper's main routine is handled as follows. That covers the entrypoint, the preprocessor, Date hydration, the stream loop, and serialising and writing the result.
  - The exception is printed to stderr, so it appears in the logs.
  - An object is built:
    - `message` = `e.message`, `name` = `e.name`, `stack` = `e.stack`;
    - `step_id` = the `WM_FLOW_STEP_ID` env var, added only when it is non-empty;
    - `extra` = every **own** property of `e` (as listed by `Object.getOwnPropertyNames`) except `line`, `name`, `stack`, `column`, `message`, `sourceURL`, `originalLine` and `originalColumn`. It is added only when it has at least one key.
  - The object is serialised with plain `JSON.stringify`: there is no `undefined` replacer, but the BigInt patch still applies. So fields whose value is `undefined` are **omitted**.
  - The text goes to `result.json` and the process exits with code 1. The worker uses that text verbatim as the error value ([WR-4](#wr-4)), then wraps it ([WR-21](#wr-21)).
  - **Consequences:**
    - The `name` of an `Error` subclass is `Error`, unless the class sets `name`.
    - Properties the user attaches (`code`, `cause`, `errors` on `AggregateError`) appear under `extra`.
    - Values inside `extra` serialise by ordinary JSON rules. A nested Error with no enumerable own properties gives `{}`; enumerable own properties survive.
    - Throwing a **non-Error** value gives odd shapes. A string gives an `extra` of indexed characters plus `length`, and no `message`. A number gives only `step_id`.
    - If serialising `extra` throws (a cycle, for example), the handler itself fails, `result.json` stays empty, and [WR-19](#wr-19) applies.
  - Which own properties Bun-native errors carry is [Q-WR-7](#q-wr-7).
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1948-1969`; `backend/tests/bun_jobs.rs:326-367` (a runtime error's `message` contains the thrown text) (all @ v1.767.0).
- **Conformance:** [WR-18/a–h](conformance.md#wr-18)

<a id="wr-19"></a>
### WR-19 · Failures that escape the wrapper's error shaping

**Scope:** core (shape); later (exit-code value) · **Confidence:** high (escape cases and classification); the exit status is unknown until observed · **Status:** confirmed

- **Rule:**
  - **Which failures escape:** in three situations the wrapper's error handler never produces output, so `result.json` stays empty:
    - (a) the thrown value is `null` or `undefined`, so reading `.message` inside the handler throws;
    - (b) an exception during module evaluation (top-level code, or a failing import), which happens before the handler is in place;
    - (c) an exception inside the handler itself, such as an `extra` value that cannot be serialised.
  - **How Windmill classifies the result:**
    - a non-zero exit becomes the [WR-20](#wr-20) log-tail object, with the observed `exit_code`;
    - exit 0 becomes the [WR-V2](#wr-v2) `InternalErr` object.
  - **What the source does not settle:** which exit status Bun `1.3.10` uses in these cases, and whether a detached (unawaited) promise rejection kills the process before the result is written ([Q-WR-2](#q-wr-2)). The expected `exit_code` `1` in the fixture is a prediction, not an observation.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1909-1913`, `:1948-1969`; `backend/windmill-worker/src/result_processor.rs:589-614`; plus the [WR-V2](#wr-v2) evidence (all @ v1.767.0).
- **Conformance:** [WR-19/a](conformance.md#wr-19)

<a id="wr-20"></a>
### WR-20 · Log-tail error synthesised on a non-zero exit with an empty `result.json`

**Scope:** core (shape); later (exact tail text); out (agent-worker fallback) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Taking the log tail:** the worker takes the last 600 characters of the job's latest log chunk, then keeps only the text after the last `CODE EXECUTION ---` marker.
  - **The error object it builds:**
    - `message`: `exit code for "<program>": <code>, last log lines:\n<tail>`, with the tail trimmed and ANSI escapes removed;
    - `name`: `ExecutionErr`;
    - `step_id`: added when the job is a flow step;
    - `exit_code`: the exit code.
  - **`<program>`** is the child process that failed:
    - `bun run` for the script;
    - `bun build` for bundling or lockfile generation;
    - `bun install` for dependency installation.
  - **Which workers:** this fallback belongs to the SQL-connected worker. An HTTP-connected (agent) worker returns the bare string `"See logs for more details"` instead; that path is `out`.
- **Evidence:** `backend/windmill-worker/src/result_processor.rs:589-614`, `:611-613` (agent-worker fallback), `:2020-2045` (`SerializedError`, `extract_error_value`); `backend/windmill-worker/src/bun_executor.rs:448-478`, `:761-792`, `:2429`; `backend/tests/bun_jobs.rs:445-500` (an unclosed bracket fails at the build step, with a message containing `syntax error` and `Unexpected end of file`) (all @ v1.767.0).
- **Conformance:** [WR-20/a](conformance.md#wr-20)

<a id="wr-21"></a>
### WR-21 · How a failed script's error value is stored and passed on

**Scope:** core (wrapping); later (the `ManualFailure` bypass) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Pre-shaped failures are checked first.** On the failure path, if the raw error value parses as a JSON object whose top-level `error` is an object with `name` = `ManualFailure`, it is stored and passed on **as-is**, with its sibling fields kept ([WR-V5](#wr-v5)).
    - The object does not need a `wm_failure` field.
    - It does not matter whether the worker injected the marker.
  - **Everything else is wrapped** as `{"error": <value>}`.
    - That one object is both the job's stored result and the value handed to the flow state machine.
    - `<value>` is the [WR-18](#wr-18), [WR-20](#wr-20) or [WR-37](#wr-37) object.
    - Text that is not JSON becomes `{"error":{"message":"Non serializable error: <text>"}}`.
  - **How a Bun script reaches the bypass:** through `wm_failure` ([WR-17](#wr-17)), or by writing such an object to `result.json` itself and exiting non-zero.
- **Evidence:** `backend/windmill-queue/src/jobs.rs:627` (`WrappedError`), `:651-673`, `:659-673`, `:883-900`; `backend/windmill-worker/src/result_processor.rs:1575-1613`, `:1583-1613`; `backend/tests/bun_jobs.rs:362-366` (`result["error"]["message"]`) (all @ v1.767.0).
- **Conformance:** [WR-21/a, WR-21/b](conformance.md#wr-21)
- **Note:** what `continue_on_error` exposes to later steps is covered in [execution](execution.md).

<a id="wr-37"></a>
### WR-37 · Execution-handler failures (`execution error:`) and timeouts

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **What this covers:** errors returned by the **execution handler**: arg resolution ([WR-5](#wr-5)), signature parsing ([WR-7](#wr-7)), timeout, cancellation, and I/O while running the child.
    - Each becomes `{"message":"execution error:\n<error display>","name":"ExecutionErr","step_id":…}`, which is then wrapped as in [WR-21](#wr-21).
    - Errors raised later, at **completion** (an empty or oversize result), are shaped differently ([WR-V2](#wr-v2), [WR-15](#wr-15)).
  - **Error displays:**
    - **Timeout:** `job process terminated due to timeout after exceeding job-specific duration limit`, or `job process terminated due to timeout after exceeding instance-wide job duration limit`.
    - **Cancellation:** `job process terminated due to cancelled[ by <user>][ (reason: <r>)]`.
    - **Internal errors** carry an ` @<source location>` suffix.
  - **Which timeout applies:**
    - A requested timeout gets the "job-specific" wording only when it is **strictly less than** the instance maximum (`TIMEOUT`). The default maximum is 604 800 s self-hosted. On the cloud the base default is 900 s, and in a premium cloud workspace the effective maximum is six times the instance maximum.
    - A request equal to or above the maximum is ignored. The instance maximum applies, the log gets `WARNING: Custom job timeout of <n> seconds was greater than the maximum timeout. …`, and the wording is "instance-wide".
    - With no request, the instance default or maximum applies.
  - **Flow-module timeouts** are input transforms. The flow engine evaluates them and passes the result when it pushes the job. A negative value fails with `Timeout value cannot be negative`.
  - **Kill sequence:**
    - SIGINT is sent first only if `MAX_WAIT_FOR_SIGINT` > 0, and the default is 0.
    - The Bun run does not ask for SIGTERM (`MAX_WAIT_FOR_SIGTERM` = 5 does not come into play).
    - So on Unix the process is **SIGKILLed immediately** on timeout or cancellation, with no chance to clean up.
  - How cancelling the containing flow behaves is covered in [execution](execution.md).
- **Evidence:** `backend/windmill-worker/src/result_processor.rs:616-621`; `backend/windmill-worker/src/handle_child.rs:166-197` (kill-reason texts), `:199-216` (timeout and cancel race), `:254-306` (signals), `:334-348`; `backend/windmill-worker/src/worker.rs:664-672` (`MAX_WAIT_FOR_SIGINT`, `MAX_WAIT_FOR_SIGTERM`); `backend/windmill-worker/src/bun_executor.rs:2427-2431` (SIGTERM flag off); `backend/windmill-worker/src/common.rs:1056-1105` (`resolve_job_timeout`; premium multiplier at `:1064-1082`); `backend/windmill-common/src/worker.rs:210-211`, `:295-298`; `backend/windmill-worker/src/worker_flow.rs:4379-4428` (module timeout evaluated and pushed) (all @ v1.767.0).
- **Conformance:** [WR-37/a](conformance.md#wr-37)
- **Note:** sits in this section because it completes the error path. Deadlines therefore preempt user code rather than asking it to stop.

<a id="wr-v5"></a>
### WR-V5 · Pre-shaped failure detection is a structural marker check

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - The failure-completion path skips the usual outer wrapping whenever its raw error result parses as an object with `error.name` equal to `ManualFailure`.
  - It requires no `wm_failure` member, and it does not track whether the worker injected the marker.
  - Other sibling fields are kept.
  - Exceptions caught by the wrapper are still shaped by [WR-18](#wr-18) before this check.
- **Evidence:** `backend/windmill-queue/src/jobs.rs:659-673`; `backend/windmill-worker/src/result_processor.rs:1583-1613` (all @ v1.767.0).
- **Conformance:** [WR-V5/a](conformance.md#wr-v5)
- **Note:** part of [WR-21](#wr-21). It concerns manual result-file behaviour, but it also marks the exact edge of the core wrapper failure path.

## E. Bun: environment and filesystem

<a id="wr-22"></a>
### WR-22 · Environment variables a Bun job sees

**Scope:** core (`WM_WORKSPACE`, `WM_TOKEN`, `WM_JOB_ID`, `WM_FLOW_JOB_ID`, `WM_ROOT_FLOW_JOB_ID`, `WM_FLOW_STEP_ID`, `WM_FLOW_PATH`, `WM_JOB_PATH`, `BASE_URL`); later (the rest); out (`WM_WORKER_GROUP`) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Assembly order:** the process environment is **cleared**, then filled in this order, later sources overriding earlier ones:
    1. the script's own `envs` (`KEY=VALUE` entries from the script metadata; ignored on the cloud) plus the worker's proxy variables;
    2. the reserved variables, then any workspace custom env vars and worker-group `env_vars` (worker-group entries can override reserved names);
    3. the proxy and OTel context variables;
    4. the common Bun variables: `PATH`, `HOME` (the worker's, default `/tmp`), `TZ` (the worker's), `FORCE_COLOR=1`, `DO_NOT_TRACK=1`, `BUN_INSTALL_CACHE_DIR`, `BUN_RUNTIME_TRANSPILER_CACHE_PATH=0` and `BASE_URL`, plus optionally `NODE_PATH` and `BUN_INSTALL_MINIMUM_RELEASE_AGE`. `BASE_URL` is the **internal** base URL, with `localhost` rewritten to `127.0.0.1`.
  - **`WM_PIPELINE=true`** is added for pipeline members.
  - **`BASE_INTERNAL_URL`:** Windmill does **not** inject it automatically for Bun, unlike for Deno, Python and Bash. The TypeScript client falls back to `BASE_URL`. The script's `envs`, a workspace env var or worker-group `env_vars` can still supply it.
  - **Reserved variables** (empty string when not applicable):

    | Variable | Value |
    |---|---|
    | `WM_WORKSPACE` | the workspace id |
    | `WM_EMAIL`, `WM_USERNAME`, `WM_PERMISSIONED_AS` | as named |
    | `WM_TOKEN` | job-scoped bearer token |
    | `WM_BASE_URL` | the public URL |
    | `WM_JOB_ID` | the job id |
    | `WM_JOB_PATH` | the runnable path; for an inline flow step `<flow_path>/<step_id>` ([WR-29](#wr-29)) |
    | `WM_FLOW_JOB_ID` | the **parent** job id |
    | `WM_ROOT_FLOW_JOB_ID` | the innermost root flow job |
    | `WM_ROOT_JOB_ID` | the root job |
    | `WM_FLOW_PATH` | the parent's runnable path |
    | `WM_FLOW_STEP_ID` | the module id, or `preprocessor` |
    | `WM_SCHEDULE_PATH` | schedule path |
    | `WM_SCHEDULED_FOR` | RFC 3339 with seconds and `Z` |
    | `WM_STATE_PATH` | in a flow `<flow_path>/<step_id>_<trigger>`; for a plain script `<script_path>/<trigger>`. `<trigger>` is the username when scheduled and `user` otherwise. |
    | `WM_OBJECT_PATH` | contains a millisecond timestamp, so it is non-deterministic |
    | `WM_WORKER_GROUP`, `WM_RUNNABLE_ID` (the script hash), `WM_END_USER_EMAIL`, `WM_TESTED_RUNNABLE` | as named |

  - Custom workspace env vars whose names collide with the reserved `WM_*` pattern are dropped.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:2383-2402` (assembly order), `:3402-3445` (`get_common_bun_proc_envs`); `backend/windmill-worker/src/worker.rs:4149-4172` (`build_envs`), `:5176-5180` (`WM_PIPELINE`), `:1024-1048` (proxy envs); `backend/windmill-worker/src/common.rs:508-571` (`get_reserved_variables`, `build_envs_map`); `backend/windmill-common/src/variables.rs:281-529` (all @ v1.767.0). Docs: <https://www.windmill.dev/docs/core_concepts/variables_and_secrets#contextual-variables>.
- **Conformance:** [WR-22/a](conformance.md#wr-22)
- **Design note (not Windmill behaviour):** whatever `wmill` shim windmill-lite injects ([client API](client-api.md)) must read the same names: `BASE_URL`, `WM_TOKEN` and the `WM_*` variables.

<a id="wr-23"></a>
### WR-23 · Filesystem view

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **The contract:**
    - the working directory is a private, writable, per-job directory;
    - stdin is `/dev/null`;
    - stdout and stderr are merged line by line into the job logs, and empty lines are dropped.
  - **Which files are present depends on the mode** ([WR-27](#wr-27)):
    - a *bundle-cache hit* has `main.js` (a symlink), `wrapper.mjs`, `args.json`, `result.json` and `package.json` (`{"type":"module"}`);
    - a *bundle-cache miss* also keeps the install artifacts and build outputs, and removes `main.ts`;
    - *unbundled* mode has `main.ts`, `loader.bun.js`, the install artifacts and the wrapper files.
  - **Install artifacts** (`bun.lock`, `node_modules`) appear only when dependencies are actually installed. A lock whose lock part is `<empty>` creates neither ([WR-25](#wr-25)).
  - A relative write to `result.json` by user code is overwritten when the wrapper writes its result.
  - The file inventory is **not** part of the contract.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1503-1513`, `:1554-1618`, `:2072-2116`, `:2359-2402`; `backend/windmill-worker/src/handle_child.rs:434-436`, `:961-983` (all @ v1.767.0).
- **Conformance:** [WR-23/a](conformance.md#wr-23)

<a id="wr-24"></a>
### WR-24 · `same_worker` flows share `./shared`

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - When a flow has `same_worker: true`, each step's job directory gets a `shared` entry. It is a symlink to `<worker_dir>/<flow_job_id>/shared`, so files one step writes can be read by the next.
  - The flow's directory is not deleted between steps.
  - The symlink points at the **immediate parent** job's `shared` directory, so nested subflows do not share one global namespace.
- **Evidence:** `backend/windmill-worker/src/worker.rs:3040-3082`, `:3254`, `:5150-5167` (all @ v1.767.0).
- **Conformance:** [WR-24/a](conformance.md#wr-24)

## F. Bun: build, dependencies and imports

<a id="wr-25"></a>
### WR-25 · The lockfile format stored with a deployed Bun script

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Format:** a Bun script's lock (the `.script.lock` content) is the `package.json` text, then a separator line, then the lock.
    - With a text lock, the separator is `//bun.lock` (between newlines), and the lock is `bun.lock` text.
    - With a binary lock, the separator is `//bun.lockb`, and the lock is base64 of `bun.lockb`.
    - CRLF variants of both separators are accepted.
  - **`<empty>`:** a lock part exactly equal to `<empty>` means "no dependencies", and installation is skipped.
  - **`//npm` scripts:** the lock is only `package.json`; no separator is required.
  - **Missing separator** in any other script is an error: `Invalid requirements, expected to find //bun.lock split pattern in reqs. Found: |…|`.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:76-81`, `:354-378`, `:1392-1418`, `:1580-1618` (all @ v1.767.0).
- **Conformance:** [WR-25/a](conformance.md#wr-25)
- **Note:** the sync-folder loader reads this format from `.script.lock` ([OpenFlow](openflow.md)).

<a id="wr-26"></a>
### WR-26 · Dependency inference and pinned-import removal

**Scope:** core (whole-text pin removal); later (dependency inference); `npm:` specifiers outside core until Q-WR-8 · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Inference.** This runs when there is no lock, i.e. for previews and for lock generation by `wmill`:
    1. **Bundle the script:** Bun.build runs on it, with every bare import external and with the relative-import plugin ([WR-28](#wr-28)). The plugin fetches imported workspace scripts from `raw_unpinned` ([WR-V9](#wr-v9)).
    2. **Scan the output:** every import specifier is matched as `name[@version]`.
       - `name` is `@scope/pkg` or `pkg`; any sub-path is ignored.
       - Node built-ins are skipped.
       - A specifier without a version contributes `latest`.
    3. **Pick versions:** when one name appears with several versions, the lowest is chosen. The comparison is made after stripping the range operators `^ ~ < <= >= >`, and the choice is logged.
    4. **Write and install:** the result is written as `package.json` `{"dependencies":{…}}`, then `bun install --save-text-lockfile` runs.
       - `--no-cache` is added when `BUN_NO_CACHE` is set.
       - `//npm` scripts use `npm install` instead.
    - If the workspace defines workspace dependencies, that `package.json` is used instead of inference.
  - **Pin removal is a whole-text replace.**
    - The rewriter finds every import specifier of the form `pkg@version` or `@scope/pkg@version`.
    - Taking them longest first, it replaces **every occurrence of that exact text anywhere in the source** with the unpinned name. That includes strings and comments, not just the import ([WR-V4](#wr-v4)).
    - Package subpaths are kept: `pkg@1.2.3/sub` becomes `pkg/sub`, and `@s/p@1/x` becomes `@s/p/x`.
  - **Where pin removal runs:** on the script itself before it runs, and on every imported workspace script that `raw_unpinned` serves.
  - **`npm:` specifiers:**
    - The scanner records an `npm:`-prefixed specifier as the package name `npm:<pkg>`, and pin removal leaves the prefix alone.
    - Whether Bun `1.3.10` then installs or rejects it has not been observed ([Q-WR-8](#q-wr-8)). These specifiers are outside the core compatibility boundary until it has.
- **Evidence:** `backend/windmill-worker/loader_builder.bun.js:38-101` (dependency capture and version reduction; `npm:` handling at `:38-52`); `backend/parsers/windmill-parser-ts/src/lib.rs:676-695` (`remove_pinned_imports`); `backend/windmill-api-scripts/src/scripts.rs:2922-2926`; `backend/windmill-worker/src/bun_executor.rs:380-552` (`gen_bun_lockfile`), `:643-679`, `:643-810` (`install_bun_lockfile`) (all @ v1.767.0).
- **Conformance:** [WR-26/a, WR-26/b](conformance.md#wr-26)
- **Note:** which import forms feed pin removal (type-only imports and re-exports do; `import(…)` and `require(…)` do not), and the TSX parse that rejects angle-bracket type assertions, are in [WR-52](#wr-52).
- **Design note (not Windmill behaviour):** windmill-lite consumes existing locks from the sync folder and leaves lock generation to `wmill`, which is why inference is `later`. Pin removal stays `core` because a sync-folder script may contain `pkg@ver` specifiers.

<a id="wr-27"></a>
### WR-27 · Bundled and unbundled modes share the wrapper rules, not every observable

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Mode selection:**
    - bundled when a lock exists and bundling is not disabled; this is the default for deployed scripts;
    - unbundled with `//nobundling` ([WR-31](#wr-31)), `DISABLE_BUNDLING=true`, or when there is no lock.
  - **Bundled mode:**
    - **Build:** the worker runs Bun.build on `main.ts` with target `bun`, ESM output, syntax-only minification (identifiers and whitespace kept), and only `electron` external. Every npm dependency and every relative import is **inlined**.
    - **Cache key:** the bundle is cached under a hash of the script content, the lock, the `(path, latest deployed hash)` of every script in the transitive relative-import closure (capped at 256), and the workspace registry settings.
    - **Cache hit:** `main.js` is symlinked in, `package.json` is `{"type":"module"}`, nothing is installed, and the command is `bun run wrapper.mjs`.
    - **Cache miss:** the worker installs, bundles and saves, rewrites the wrapper to import `./main.js`, then runs the same way.
  - **Unbundled mode:**
    - The worker installs dependencies into the job directory.
    - It runs `bun run --preserve-symlinks -i --prefer-offline -r ./loader.bun.js wrapper.mjs`. `-i` lets Bun auto-install missing packages, and the preloaded plugin resolves relative imports at run time over HTTP.
  - **What both modes share:** the args, Date-hydration, result-serialisation and error rules ([WR-5](#wr-5) to [WR-21](#wr-21)).
  - **What can differ between modes:**
    - module paths and `import.meta`;
    - `__filename` and directory layout;
    - stack text;
    - the file inventory ([WR-23](#wr-23));
    - module-identity effects of inlining.
  - **What is promised:** equal results only for scripts whose output depends on none of those.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:900-945` (bundle build options), `:1347-1390` (cache key), `:1468-1501` (cache lookup), `:1993-1997` (`build_cache`), `:2072-2116` (build, save, rewrite wrapper to `./main.js`), `:2368-2382` (commands); `backend/windmill-common/src/worker.rs:401` (`DISABLE_BUNDLING`) (all @ v1.767.0).
- **Conformance:** [WR-27/a](conformance.md#wr-27)
- **Note:** `process.env.NODE_ENV` is one more observable that differs between the modes: bundled mode fixes it as `"development"` ([WR-53](#wr-53)).
- **Design note (not Windmill behaviour):** windmill-lite's own bundling should copy the bundled mode, because that is what deployed scripts actually use.

<a id="wr-28"></a>
### WR-28 · Which imports are rewritten and fetched from the workspace

**Scope:** core (static `.`, `/f/`, `/u/` imports); later (dynamic-import interaction and other forms) · **Confidence:** high (source rules); medium (which import kinds the Bun `1.3.10` scanner reports) · **Status:** DISPUTED

The dispute covers only the dynamic-import consequence, marked *(disputed)* below. The static-import core is confirmed.

- **Rule (as drafted):**
  1. **Rewrite trigger.** An import triggers a rewrite only when all of these hold:
     - Bun's import scanner reports it as a static import statement. A probe on a newer, non-pinned Bun release reported `export … from` as one; Bun `1.3.10` is unverified ([Q-WR-13](#q-wr-13)).
     - Its specifier starts with `.`, `/u/` or `/f/`.
     - Its specifier does not end in `.ts`.

     Dynamic `import()` calls never trigger a rewrite. The rewrite applies to the entry `main.ts` and to every workspace module that is fetched.
  2. **What a rewrite touches:** it appends `.ts` to **every occurrence of that specifier text in the whole source**. That includes the same literal inside a dynamic `import()`, other strings and comments ([WR-V4](#wr-v4); collisions are covered in [WR-38](#wr-38)).
  3. **Interception:** the resolver plugin intercepts only specifiers that end in `.ts`. It skips the entry `main.ts`, files prefixed `_wm_`, the wrapper, and any import that comes from `node_modules`. Each intercepted specifier is resolved like this:
     - **Local file:** if the specifier is relative and a file exists at that path in the job directory (a multi-file script module), that file is used.
     - **Otherwise, fetch from the workspace:**
       - a relative specifier resolves to `<importer_path>/../<spec>`, i.e. against the importer's own path treated as a file;
       - an absolute specifier resolves to `<spec>` without its leading `/`;
       - dot segments are normalised and `.ts` is ensured.
     - **Which version:** the **latest deployed** version of the target, from the `raw_unpinned` endpoint. This is not the version that existed when the importer was deployed. A new deploy of an imported script invalidates the importer's bundle cache ([WR-27](#wr-27)).
     - **Transitive imports** resolve relative to each imported script's own path.
  4. **Consequences:**
     - An extensionless `/other/x` is neither rewritten nor intercepted. Bun then resolves it as a host filesystem path, which in practice fails the build. `/other/x.ts` *is* fetched, as workspace path `other/x`.
     - A relative non-TypeScript import such as `"./data.json"` is rewritten to `./data.json.ts` and fetched under that name, which in practice fails.
     - *(disputed)* An extensionless dynamic `import("./x")` is intercepted only if a static import of `./x` in the same file triggered the rewrite, which also turns the dynamic literal into `./x.ts`. Otherwise it is not intercepted, and Bun resolves it itself (in practice, against the job directory).
- **Correction (validation):**
  - An identical static import is **sufficient but not necessary** for a dynamic-import literal to be rewritten.
  - A qualifying static specifier can match a substring inside a *different* dynamic specifier. For example, a reported static `./helper` rewrites the dynamic literal `./nested/./helper` to `./nested/./helper.ts`, with no static import of `./nested/./helper` at all.
  - Interception then depends on the resulting specifier and on the loader's filter and exclusions.
  - Evidence: `backend/windmill-worker/loader.bun.js:23-54`, `:72-86` (@ v1.767.0).
- **Open:** [Q-WR-16](#q-wr-16) (the correction); [Q-WR-13](#q-wr-13) (now re-scoped: the scanner's behaviour on the pinned Bun is in [WR-52](#wr-52), which also covers how `..` segments of a relative import are resolved).
- **Evidence:** `backend/windmill-worker/loader.bun.js:23-47` (filters), `:34-54` (rewrite), `:72-75` (fetched-module rewrite), `:78-113` (resolve and fetch); `backend/windmill-api-scripts/src/scripts.rs:2857-2865` (latest non-archived row); `backend/windmill-dep-map/src/lib.rs:43-67` (`parse_ts_relative_imports`, the same normalisation for dependency tracking); `backend/windmill-worker/src/bun_executor.rs:836-845`; `backend/tests/bun_jobs.rs:687-995` (relative, nested and transitive import tests) (all @ v1.767.0).
- **Conformance:** [WR-28/a–e](conformance.md#wr-28)
- **Design note (not Windmill behaviour):** windmill-lite resolves these imports from the local sync folder.

<a id="wr-29"></a>
### WR-29 · The import base for an inline flow script is the flow's path

**Scope:** core · **Confidence:** high (source-traced; not oracle-observed) · **Status:** confirmed (the default base); drafted (the explicit-`path` rule, revised 2026-10-28)

- **Rule:**
  - **The import base** is the step's runnable path, with nested-body segments collapsed. Inside `branchone-N`, `branchall-N`, `forloop-N` or `loop-N` bodies, the base is first collapsed to `<flow_path>/flow`.
  - **Default case:** an inline (`rawscript`) step has runnable path `<flow_path>/<step_id>`, and the preprocessor step has `<flow_path>/preprocessor`. Both at top level and inside branches and loops:
    - `./x` resolves to `<flow_path>/x`;
    - `../x` resolves to `<dir of flow_path>/x`.
  - **An explicit `path` on an inline module** plays three different parts:
    1. **In a deployed flow** (stored in the lite form, [OF-3](openflow.md#of-3), the reference configuration), the field is **dropped**: storing the flow turns each inline script into a `flowscript` reference that keeps no `path`. The step runs as a job with the default runnable path, so its run-time import base is the default one above, whatever `path` said.
    2. **In the flow's dependency job** (lock generation and the bundle built at deploy), the base is `<path>/flow`, read as a file, so `./x` resolves to `<path>/x`. Without an explicit `path` this is `<flow_path>/flow`, which agrees with the default.
    3. **When the flow runs its raw module values** (a preview, or a flow that kept its raw value because its first lock failed, [OF-3](openflow.md#of-3)), the explicit path is the job's runnable path and the import base, as written at any depth and collapsed the same way. `path: f/elsewhere/entry` then makes `./x` resolve to `f/elsewhere/x`.

    So with `path: f/elsewhere/entry` in flow `f/a/fl`, `./x` is locked as `f/elsewhere/entry/x` but runs as `f/a/fl/x` once deployed. The bundle cache key, built from the step's run-time path ([WR-27](#wr-27)), then differs from the one built at deploy, and the step is bundled again at run time from `f/a/fl/x`. If `f/elsewhere/entry/x` does not exist, the lock fails instead, the flow keeps its raw value, and the run resolves `f/elsewhere/x`.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:5076-5086` (`get_path`), `:5199` (raw module values: the explicit path, or `get_path`), `:5217-5224` (a stored `flowscript` always gets `get_path`), `:5374`, `:5511`, `:5553`, `:5670` (nested path names); `backend/windmill-worker/src/worker_lockfiles.rs:1612-1650` (an inline script becomes a `flowscript` without its `path`), `:1321-1324`, `:1378-1381` (the dependency job's base `<path or flow path>/flow`); `backend/windmill-types/src/flows.rs:1038-1052` (`flowscript` has no `path`); `backend/windmill-worker/src/common.rs:1799-1813` (`use_flow_root_path`); `backend/windmill-worker/src/bun_executor.rs:415-419`, `:840-843`, `:1474-1481` (the run-time cache key from the job's runnable path) (all @ v1.767.0).
- **Conformance:** [WR-29/a, WR-29/b](conformance.md#wr-29)
- **Revision 2026-10-28 (source re-check):** the exception said that an explicit `path` is the import base, so that `path: f/elsewhere/entry` makes `./x` resolve to `f/elsewhere/x`. That holds only where raw module values run. A deployed flow stores its inline scripts without the field, so at run time the base is the default one; the dependency job uses `<path>/flow`, which resolves `./x` one level deeper again. WR-29/b now expects the deployed outcome.

<a id="wr-30"></a>
### WR-30 · Codebase bundles (`wmill.yaml` codebases)

**Scope:** EE (normal remote fetch) · **Confidence:** high (worker side); medium (CLI side) · **Status:** confirmed

- **Rule:**
  - **The worker does not build these.** A script deployed under a codebase is bundled by the CLI and uploaded at deploy time.
  - **Runtime lookup,** in order:
    1. the worker's local cache file for the bundle id;
    2. in standalone mode with no object store, the standalone bundle directory. If the bundle is absent there, the job fails with `(standalone bundle test mode) could not find codebase at <path>`;
    3. otherwise the enterprise object store. Without that build, the job fails with `codebase is an EE feature`.
  - **How it runs:**
    - CJS bundles run under **Node** (`node --preserve-symlinks wrapper.mjs`), not Bun.
    - ESM bundles run under Bun.
    - The wrapper imports `./main.js`. Args, results and errors behave as in [WR-6](#wr-6) to [WR-21](#wr-21).
    - These are the defaults. A `//native` header in the source takes precedence over both, and `//nodejs` sends an ESM bundle to Node ([WR-50](#wr-50) gives the order).
  - **How the CLI builds the bundle:**
    - by default with esbuild: platform `node`, packages inlined, `format` `cjs` (target `node20.15.1`) or `esm`, plus configurable `external`, `inject`, `define`, `loader` and `banner`;
    - or with an arbitrary `customBundler` command;
    - several output files produce a tar.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1061-1129` (`pull_codebase`; lookup order at `:1077-1108`), `:1574-1578`, `:1650-1652` (CJS forces Node), `:2338-2366`; `cli/src/commands/script/script.ts:325-375` (bundling options) (all @ v1.767.0).
- **Correction (semantics request 2):** the standalone bundle directory is not only a test fixture. In standalone mode with no object store, the server itself writes every uploaded codebase bundle there, so codebase scripts deploy and run on CE in that mode, which is the CE oracle's topology ([WR-49](#wr-49), [WR-50](#wr-50)). `codebase is an EE feature` is the outcome on the other CE modes. The CLI's settings, the upload format and the run-time details are [WR-47](#wr-47) to [WR-50](#wr-50).
- **Conformance:** the cases of [WR-47](conformance.md#wr-47) to [WR-50](conformance.md#wr-50), on a CE standalone oracle.
- **Design note (not Windmill behaviour):** windmill-lite may support codebase folders natively by bundling them locally. That is a design choice to record. The bundles it builds can now be compared with the oracle's ([WR-50](#wr-50)).

<a id="wr-47"></a>
### WR-47 · How the pinned CLI bundles a codebase script

**Scope:** core · **Confidence:** high (CLI source-traced; not oracle-observed, [Q-WR-24](#q-wr-24)) · **Status:** confirmed

- **Rule:**
  - **Bundler and version.** esbuild **`0.28.0`**, pinned exactly in the CLI's manifest and in the dependency list of the published CLI package.
    - The CLI uses the native esbuild binary.
    - If that binary does not start (for example its version disagrees with the host package), the CLI falls back to `esbuild-wasm` at the host package's version. It downloads that package from the npm registry, or takes a configured path or URL, and caches it.
    - The fallback is meant to produce the same output.
  - **Format.** An absent `format` means **`cjs`**. Only an explicit `format: esm` produces ESM.
  - **Build options.** The script file is the single entry point, and the output stays in memory:

    | Option | Value |
    |---|---|
    | `bundle` | `true` |
    | `platform` | `node` |
    | `packages` | `bundle`: npm packages are inlined |
    | `format` | the entry's `format`, default `cjs` |
    | `target` | `node20.15.1` for `cjs`, `esnext` for `esm` |
    | `outdir` | `/`; output files are named relative to it |
    | `external`, `inject`, `define`, `banner` | the entry's values, passed through unchanged; absent means esbuild's default |
    | `loader` | the entry's map, which **replaces** the default `{".node": "file"}` entirely |
    | minification, source maps, code splitting, metafile | not set, so esbuild's defaults (off) apply |

  - **What passing through means.**
    - `inject` is a list of file paths. esbuild resolves them against the CLI's working directory and imports their exports wherever the bundle uses those names as free identifiers. The CLI does nothing else with the list.
    - `define` values are JavaScript expressions written as strings, so a string constant needs its own inner quotes. esbuild substitutes them.
    - `banner` is esbuild's per-output-type map, for example `{js: "…"}`.
  - **`relative_path` does not limit what is bundled.** esbuild follows imports wherever they lead. `relative_path` only feeds the codebase id ([WR-49](#wr-49)).
  - **Which CLI paths build this way:** `wmill sync push`, `wmill script push` and `wmill script preview`. The flow-preview reader for local path scripts builds differently, and is `out` for windmill-lite:
    - with esbuild it always builds ESM with target `esnext`, whatever `format` says, and it refuses entries that have assets or several output files;
    - with a `customBundler` it returns the command's output before either refusal, so an entry with assets is accepted and its assets are simply not delivered.
- **Evidence:** `cli/package.json:23`; `cli/build-npm.ts:75`; `cli/src/utils/esbuild_loader.ts:26-28`, `:55-79`, `:98-125`; `cli/src/commands/script/script.ts:315-364` (deploy), `:1563-1593` (preview); `cli/src/core/conf.ts:114-131` (the entry's fields); `cli/src/utils/local_path_scripts.ts:31-81` (flow-preview reader; the custom-bundler return at `:35-45` comes before the refusals at `:69-78`) (all @ v1.767.0). esbuild's [API documentation](https://esbuild.github.io/api/) for the meaning of each option.
- **Conformance:** [WR-47/a–f](conformance.md#wr-47)
- **Design note (not Windmill behaviour):** a bundler that reproduces these options through esbuild's Go API at `v0.28.0` should produce the same bundle. Whether it does is the parity check of [Q-WR-24](#q-wr-24).

<a id="wr-48"></a>
### WR-48 · `customBundler`

**Scope:** later · **Confidence:** high (push path); medium (shell details) · **Status:** confirmed; drafted (what counts as set, added 2026-10-28)

- **Rule:**
  - **What counts as set.** The CLI tests the entry's `customBundler` for JavaScript truthiness, with no type check on `wmill.yaml`. An absent key, `null`, `""`, `false` and `0` all mean "not set", and esbuild builds the bundle as in [WR-47](#wr-47). Any truthy value counts. A value that is not a string is turned into text before the script path is appended, so `customBundler: true` runs the shell's `true`, which prints nothing, and the uploaded bundle is empty.
  - When an entry sets `customBundler`, esbuild is not used. The CLI runs the command line `<customBundler> <script path>` through the system shell, in its own working directory. The path is appended after one space, unquoted. The command's standard output, decoded as text, is the bundle.
  - None of `format`, `external`, `inject`, `define`, `loader` or `banner` is applied to the build. `format` still decides how the bundle runs, after the header annotations ([WR-50](#wr-50)): absent means CJS under Node, so the command's output must match it.
  - A non-zero exit, or more than 50 MiB of output, aborts the push of that script.
  - The output is always one file, so there is never a multi-file tar. `assets` are still added ([WR-49](#wr-49)).
  - The flow-preview reader of [WR-47](#wr-47) runs the command differently: through a login shell, with the path passed as a quoted positional argument.
- **Evidence:** `cli/src/commands/script/script.ts:325-330` (the truthiness test and the command built by concatenation), `:395-416`; `cli/src/core/conf.ts:114-131` (the field is declared as a string but never checked); `cli/src/utils/local_path_scripts.ts:35-45` (all @ v1.767.0).
- **Conformance:** [WR-48/a, WR-48/b](conformance.md#wr-48)
- **Revision 2026-10-28 (source re-check):** the rule said only "when an entry sets `customBundler`". It now says which values count as set: JavaScript-truthy ones, so `false` and `0` leave esbuild in charge, as an absent key does.

<a id="wr-49"></a>
### WR-49 · What the CLI uploads, the codebase id, and where the server keeps the bundle

**Scope:** core (single file and assets); later (several output files; push, version and replacement decisions) · **Confidence:** high (CLI and server source-traced); medium (the multi-output naming edge) · **Status:** confirmed

- **Rule:**
  - **One output and no `assets`:** the upload is the bundle text itself.
  - **One output with `assets`:** the upload is a tar.
    - Its first entry is `main.js`, the bundle.
    - Then comes one entry per `{from, to}`, in order: the bytes of the file `from`, read relative to the CLI's working directory at push time, stored under the entry name `to`.
  - **Several output files** (esbuild emitted more than the entry, for example a `.node` add-on or a file through the `file` loader): the upload is a tar.
    - Its first entry is `main.js`: the output named `<script file name up to its first dot>.js`.
    - Then comes every other output, under its path relative to `/`.
    - **`assets` are not added in this case.**
    - For an entry whose file name has a second dot (such as `x.bun.ts`), no output has the expected name, so `main.js` is empty (medium, [Q-WR-25](#q-wr-25)).
  - **The id the CLI sends.** The script's `codebase` field is a digest, plus `.esm` when `format` is `esm` and `.tar` when the upload is a tar.
    - **What the digest covers:** the bytes of every regular file found by walking `relative_path` recursively (relative to the CLI's working directory), visited in name order within each directory, together with the entry's settings as written in `wmill.yaml`. Any change to a setting changes it, and so does a change to the order of its keys, since the settings are serialised as text.
    - **What it does not cover:**
      - entry names: renaming a file or directory leaves the digest unchanged as long as the visiting order of contents stays the same, while a rename that reorders files of different contents changes it;
      - symbolic links, to files or directories, and whatever they point at;
      - anything outside `relative_path`: imported modules, `inject` files and asset `from` files that live elsewhere.
    - Every file under `relative_path` counts, whether or not the script uses it. The digest is computed once per CLI run and shared by every script of the entry, so editing any file there gives every script of the entry a new id.
  - **From a local change to the bundle that runs: four separate decisions.** None of them compares bundle bytes.
    1. **Whether `wmill sync push` selects the script.**
       - Sync push compares the local tree with the deployed workspace's tree. Before comparing, it writes the current digest into each codebase script's local metadata, as its `codebase` field, with an empty `lock`, which is also what the server stores for codebase scripts.
       - The script is therefore selected when its source or metadata differ from the deployed ones, or when the id differs from the deployed id.
       - A script that sync push does not select gets no request at all, and its deployed bundle stays as it was.
       - *(medium)* A multi-output bundle without assets is stored with a `.tar` suffix that this local id lacks, so such a script is selected on every sync push.
    2. **Whether the CLI sends the request.**
       - `wmill script push`, and the push of a script that sync push selected, both end in the same check. The CLI stops, reporting the script up to date, only if the source `content`, the metadata fields and the id all equal the deployed script's.
       - Its `priority` comparison reads the deployed value as a boolean but the local value as written. Metadata without a `priority` key (the CLI's generated default) therefore never matches, and the request is sent every time.
    3. **Whether the server makes a new version.**
       - The CLI always asks for the server's no-op check. The server reuses the parent's hash, making no new version, only if every field it compares is identical to the parent's: the content, the lock, the metadata, the id, and the optional settings as stored.
       - A few fields default when omitted, so leaving them out still matches: `is_template` is read as `false`, `kind` as `script` and `envs` as an empty list, and an absent `lock` or `codebase` equals an empty one.
       - Every other optional field is compared as sent against the value stored. Where the server stores a default for an omitted field, the omission therefore does not match it. An omitted `ws_error_handler_muted`, for example, is not the stored `false`, so default metadata usually yields a new version.
    4. **Whether the stored bundle is replaced.**
       - The uploaded file is written under the hash the server returned, whichever way decision 3 went. A no-op deploy therefore keeps the hash but replaces the bytes.
       - In standalone mode the file is overwritten in the standalone directory, which the worker reads for every job (on CE that path keeps no worker-side cache), so the next job runs the new bytes.
       - *(medium, EE)* A worker cache file already holding that hash would keep serving the old bytes.
  - **Consequences for an unchanged script:**
    - A change the digest sees (under `relative_path`, or in the settings), pushed with `wmill sync push`, selects the script and deploys a new version with a fresh bundle, even when the bundle comes out identical.
    - A change it does not see (an asset or import outside `relative_path`, a rename, a symlink target), pushed with `wmill sync push` from a tree that was otherwise in sync, selects nothing. No request is sent, and the old bundle keeps running.
    - The same change pushed with `wmill script push` and generated metadata does reach the server, and the next run uses the new bundle, whether or not the hash changed.
  - **What the server keeps.** The push is one multipart request to `create_snapshot`.
    - The script JSON comes first, with its source `content` ([SY-8](openflow.md#sy-8)), then the bundle.
    - The server creates the script version, or reuses the parent's hash on a no-op (decision 3 above), and stores the bundle under `script_bundle/<workspace>/<that hash>`, plus `.esm` and `.tar` copied from the sent id.
    - Only those two suffixes matter later: the worker rebuilds the id from the stored script's hash ([WR-50](#wr-50)).
  - **Where it is stored.**
    - On EE, in the object store.
    - On CE in standalone mode with no object store, under the standalone bundle directory, `$HOME/.windmill/standalone_bundle/`.
    - In any other CE mode the upload fails with `codebase is an EE feature`, and no script version is created.
- **Evidence:** `cli/src/commands/script/script.ts:365-418` (tar or text), `:461-463` (id), `:802-834` (multipart upload); `cli/src/utils/codebase.ts:19-45` (digest inputs, memoisation and suffixes); `cli/src/utils/utils.ts:108-121` (the directory walk: file contents in name order, directories recursed, symlinks skipped, names not hashed); sync selection `cli/src/commands/sync/sync.ts:414-460` (the current digest and an empty lock written into local metadata), `:3840-3862` (push compares local with remote, codebase changes not ignored), `:2324-2356`, `:2359-2398` (metadata comparison and codebase-id changes); `backend/windmill-api-scripts/src/scripts.rs:1139-1140` (codebase scripts stored with an empty lock); the CLI's check `cli/src/commands/script/script.ts:130-181` (`wmill script push` reaches the same function), `:515-565` (the up-to-date check; `priority` at `:542`), `:568-584`; `cli/bootstrap/script_bootstrap.ts:17-29` (generated metadata has no `priority` or `ws_error_handler_muted`); `cli/src/commands/script/script.ts:770`, `:817` (the CLI always requests the no-op check); the server `backend/windmill-api-scripts/src/scripts.rs:708-845` (fields compared: `kind`, `envs` and `is_template` with their defaults at `:784-786`, `:792`, `:809`; `lock` and `codebase` with absent read as empty at `:772`, `:813-814`, `:849-851`; the other optional fields literally), `:1565` (`ws_error_handler_muted` stored as `false` when omitted), `:1085-1092` (a no-op returns the parent's hash), `:502-576` (`create_snapshot`; suffixes `:521-541`, the file written under the returned hash `:543-559`); `backend/windmill-object-store/src/lib.rs:983-985` (bundle key), `:991-1020` (standalone directory, EE object store, CE refusal); `backend/windmill-common/src/worker.rs:409` (`ROOT_STANDALONE_BUNDLE_DIR`), `:760-765` (the write replaces the file); `backend/windmill-worker/src/bun_executor.rs:1066-1084`, `:1087-1103`, `:1131-1147` (a worker cache file wins; otherwise the standalone file is copied in for each job) (all @ v1.767.0).
- **Conformance:** [WR-49/a–g](conformance.md#wr-49)
- **Revision 2026-10-28 (source re-check):** decision 3 said that an omitted optional field never equals its stored default. That holds for most fields, `ws_error_handler_muted` among them, but `is_template`, `kind` and `envs` default when omitted, and an absent `lock` or `codebase` equals an empty one. The independent check of this finding raised the point; the rule and the evidence now say so, and WR-49/g notes the `is_template` variant.

<a id="wr-50"></a>
### WR-50 · How a codebase script runs: lookup, extraction and runtime

**Scope:** core (lookup, extraction, runtime selection); later (the Node patch version and its module-format detection, a CJS bundle under `//native`) · **Confidence:** high (source-traced); medium (the CJS-under-`//native` failure; Node's detection rule, from its release notes); the Node version is unknown until observed ([Q-WR-24](#q-wr-24)) · **Status:** confirmed

- **Rule:**
  - **Lookup** follows [WR-30](#wr-30). The id is rebuilt from the script row: `<script hash>`, plus `.esm` if the stored `codebase` value contains `.esm`, plus `.tar` if it ends in `.tar`.
  - **CE standalone runs codebase scripts.** In standalone mode with no object store, the bundle the server wrote ([WR-49](#wr-49)) is found in the standalone bundle directory. This is the topology of the CE oracle ([OR-4](oracle.md#or-4)), so codebase scripts deploy and run there. The `codebase is an EE feature` failure of [WR-30](#wr-30) applies to the other CE modes.
  - **Placement in the job directory:**
    - The worker first writes a placeholder `package.json` containing `{}`.
    - A plain bundle becomes `main.js`: copied from the standalone directory, or symlinked from the worker's cache.
    - A tar is unpacked into the job directory itself. `main.js` and every asset land at their entry names relative to the job directory, which is the process's working directory, so a script reads the asset `to: "data/a.txt"` as `./data/a.txt`. A tar entry named `package.json` replaces the placeholder.
    - Each job gets its own copy ([WR-1](#wr-1)). Nothing is installed: there is no lock, and `node_modules` exists only if the tar contains it.
  - **Which runtime runs the bundle.** The format is not the only input. The worker keeps the header annotations of the script's source ([WR-31](#wr-31)), which the push sends along with the bundle ([SY-8](openflow.md#sy-8)), and turns CJS into the Node flag. It then decides in this order:
    1. **`//native` in the source:** the bundle runs in the native runtime of [WR-40](#wr-40) to [WR-46](#wr-46), whatever the format. The worker builds nothing, and the server stores such a script as `bunnative`, tagged `nativets`.
       - An ESM bundle loads there as an ordinary module.
       - *(medium)* A CJS bundle is evaluated as an ES module too, so it fails on its first use of `module`, `exports` or `require`.
    2. **CJS (no `.esm`), or `//nodejs` in the source:** `node --preserve-symlinks wrapper.mjs`, with the Node binary at `NODE_BIN_PATH` (default `/usr/bin/node`).
       - The wrapper is the ordinary one of [WR-6](#wr-6) to [WR-21](#wr-21), importing `./main.js`. The environment is the Bun one of [WR-22](#wr-22).
       - Node chooses the module format of `main.js` from the nearest `package.json`. The `{}` placeholder has no `type` field, which leaves the format **ambiguous**, and Node's own version then decides:
         - before Node 20.19.0, an ambiguous `.js` file loads as CommonJS, so an ESM bundle fails to load;
         - from Node 20.19.0, module syntax detection is on by default. An ambiguous file that does not parse as CommonJS because of ES module syntax is retried as an ES module, so an ESM bundle loads.
         - A CJS bundle loads as CommonJS either way, and the wrapper reaches its exports through Node's CommonJS interop.
       - The worker passes Node no flag that changes detection, and the image does not pin the Node 20 patch, so for an ESM bundle forced onto Node by `//nodejs` the outcome without a `type` depends on the installed patch ([Q-WR-24](#q-wr-24)).
       - A `package.json` asset declaring `"type": "module"` makes the ESM interpretation explicit, on every Node version.
    3. **Otherwise (ESM):** `bun run wrapper.mjs`, on Bun `1.3.10` ([WR-3](#wr-3)).
  - **Node version.** The v1.767.0 image installs Node from NodeSource's `20.x` line when the image is built, so the major version is 20 and the patch is whatever was current then. The source does not pin it. The CLI's `node20.15.1` target ([WR-47](#wr-47)) only sets the output's syntax level.
- **Evidence:** `backend/windmill-common/src/cache.rs:654-682` (id from the hash); `backend/windmill-types/src/scripts.rs:315-319`; `backend/windmill-api-scripts/src/scripts.rs:1181-1190` (a codebase source with `//native` is stored as `bunnative`); `backend/windmill-worker/src/worker.rs:5381-5407` (the codebase is passed to the shared Bun executor); `backend/windmill-worker/src/bun_executor.rs:1442` (annotations from the source), `:1061-1129` (lookup), `:1131-1153` (copy, symlink or unpack), `:1505-1513` (placeholder), `:1574-1578`, `:1650-1652` (CJS → Node), `:1767-1771` (`./main.js`), `:2133-2245` (the native branch, taken before any process starts), `:2338-2366` (Node, then Bun); `backend/windmill-worker/src/global_cache.rs:127-148` (unpack); `backend/windmill-worker/src/worker.rs:609` (`NODE_BIN_PATH`); `backend/windmill-common/src/utils.rs:106-163` (standalone is the default mode); `Dockerfile:248-249` (NodeSource 20.x, patch not pinned) (all @ v1.767.0). Node's [v20.19.0 release notes](https://nodejs.org/en/blog/release/v20.19.0#module-syntax-detection-is-now-enabled-by-default) for module syntax detection becoming the default.
- **Conformance:** [WR-50/a–e](conformance.md#wr-50)
- **Note:** the standalone path copies a plain bundle into `main.js` instead of symlinking it, so `__filename` and symlink-sensitive code see a regular file there.

<a id="wr-31"></a>
### WR-31 · Leading-comment annotations for TypeScript scripts

**Scope:** core (parsing); later (`nodejs` and `native` runtimes); out (`sandbox`, `npm`) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Where annotations are read:** only from the leading block of comment lines.
    - Blank lines are skipped.
    - The first line that does not start with `//` ends the block. Leading whitespace before `//` is **not** trimmed, so an indented `  //nobundling` ends the block and is not an annotation.
    - The text after `//` is trimmed, so `// nobundling` and `//nobundling` are the same.
    - Unknown keys are ignored.
  - **Recognised flags:**

    | Flag | Effect |
    |---|---|
    | `npm` | install with npm |
    | `nodejs` | run under Node, with a Node-target bundle of the wrapper |
    | `native` | run in an embedded V8 isolate with fetch-only APIs, not Bun; needs the `deno_core` build feature. The runtime's rules are [WR-40](#wr-40) to [WR-46](#wr-46) (`core`) |
    | `nobundling` | unbundled mode ([WR-27](#wr-27)) |
    | `sandbox` | requires nsjail; fails with `Script has //sandbox annotation but nsjail is not available on this worker. …` when nsjail is absent |

- **Evidence:** `backend/windmill-common/src/worker.rs:968-974`; `backend/windmill-macros/src/lib.rs:48-84` (generated parse); `backend/windmill-worker/src/bun_executor.rs:1460-1466`, `:2133-2245` (native), `:2117-2131` and `:2338-2366` (nodejs) (all @ v1.767.0).
- **Conformance:** [WR-31/a, WR-31/b](conformance.md#wr-31)
- **Note:** `schema_validation` is not one of these flags. It is read by a separate exact-line test that stops at a blank line ([WR-54](#wr-54)).

<a id="wr-32"></a>
### WR-32 · How the workflow-as-code wrapper is selected

**Scope:** core (detection predicate); out (workflow-as-code execution) · **Confidence:** high (detection); medium (misselection outcome and the alternate wrapper's behaviour) · **Status:** confirmed; drafted (the line rules, revised 2026-10-28)

- **Rule:**
  - **What the alternate wrapper does.** Workflow-as-code (WAC v2) scripts wrap their entrypoint in `workflow(async (...) => …)` from `windmill-client`. They run under a separate wrapper that:
    - loads a checkpoint and replays completed steps;
    - returns `{type:"complete"|"dispatch"|"inline_checkpoint"|"approval"|"sleep", …}` to the worker;
    - dispatches steps as child jobs.

    Its error shape matches [WR-18](#wr-18).
  - **Selection is a text heuristic.** The WAC wrapper replaces the normal one when there is **no entrypoint override** and a line-based scan of the source finds both of these. The scan skips comment lines, but not block comments or strings.
    - an import of `windmill-client`: a single-line `import`/`from` line mentioning it, or a multi-line `import {` … `} from "windmill-client"` block;
    - the substring `workflow`, either in that import or on any line that contains both `export` and `workflow(`.
  - **The line rules.** Every line is trimmed at both ends before any test, so indentation never matters. For each trimmed line, in order:
    1. a line starting with `//` is skipped, inside an import block too;
    2. a line that contains `windmill-client` and **starts with** `import` or `from` (a plain text prefix, so `fromage` counts) is a client import. It counts `workflow` if the substring is on that line, and it ends any open block;
    3. otherwise, a line that starts with `import`, contains `{` and contains no `}` opens a block. The block counts `workflow` if the substring appears on any of its lines, this one included;
    4. otherwise, inside a block: a line containing `windmill-client` makes the block a client import, which counts `workflow` if the block did; and **the first line containing `}` ends the block**, whether or not it names the client.
    5. Independently of 1 to 4, any non-comment line containing both `export` and `workflow(` counts `workflow`.

    So a block whose closing `}` stands on a line of its own, followed by a separate `from "windmill-client"` line, is a client import by rule 2 but loses the block's `workflow`. A type-only import of the client counts like any other.
  - **Misselection:** a plain script that only *imports* a name containing `workflow` from `windmill-client` is selected too. It then fails at wrapper top level with `No workflow() entrypoint found. Wrap your main function with workflow().`. That failure escapes the error handler, so it is shaped by [WR-19](#wr-19).
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1522`, `:1522-1551`, `:1802-1905`, `:1821-1833`, `:2061-2069` (WAC bundle re-exports), `:2459-2462`; `backend/windmill-worker/src/wac_executor.rs:218-270` (line rules: trimming `:227`, comment skip `:228-230`, client line `:232-240`, block start `:242-245`, block body and end `:247-262`, the `export` test `:264-266`); `backend/parsers/windmill-parser-ts/src/lib.rs:368-376`, `:428-438` (all @ v1.767.0).
- **Conformance:** [WR-32/a](conformance.md#wr-32)
- **Revision 2026-10-28 (source re-check):** the scan was described as skipping "lines starting with `//`" and finding a multi-line `import {` … `} from "windmill-client"` block. The line rules now say that lines are trimmed first, that a line starting with `from` counts, and that a block ends at its first line containing `}`. The predicate is unchanged.
- **Design note (not Windmill behaviour):** the loader must flag scripts that match this predicate as unsupported, rather than run them as plain scripts.

<a id="wr-38"></a>
### WR-38 · Possible `.ts`-rewrite collisions, depending on the scanner's output

**Scope:** later · **Confidence:** high (the conditional source operation); the pinned Bun scanner behaviour is unknown · **Status:** confirmed

- **Rule** (a conditional description of the source operation):
  - **What the loader does:** it processes the scanner's import list in order. For each qualifying entry ([WR-28](#wr-28)), it replaces every occurrence of that specifier text with the text plus `.ts`.
  - **Overlapping specifiers** (one is a prefix of the other, e.g. `./util` and `./util_db`): the rewrite for the shorter one alters the longer one too. The corrupted text depends on the list order:
    - shorter first: the longer becomes `./util.ts_db`;
    - longer first: it becomes `./util_db.ts`, then `./util.ts_db.ts`.
  - **The same extensionless specifier reported twice** (two statements import it): the second pass turns `./x.ts` into `./x.ts.ts`. If the scanner deduplicates, this case does not arise.
  - **When this actually fails:** a corrupted specifier breaks the build only if nothing exists at the corrupted path. A test fixture must make sure nothing does. The failure is then a [WR-20](#wr-20) error with program `bun build`.
  - **The workaround:** writing the `.ts` extension explicitly skips the rewrite.
- **Evidence:** `backend/windmill-worker/loader.bun.js:34-54` (per-import whole-text replace, no deduplication) (@ v1.767.0). A probe on a newer, non-pinned Bun release reported repeated import statements as separate entries. That supports the rule but does not guarantee Bun `1.3.10`'s behaviour ([Q-WR-13](#q-wr-13)).
- **Conformance:** [WR-38/a, WR-38/b](conformance.md#wr-38)
- **Design note (not Windmill behaviour):** whether windmill-lite reproduces or deliberately fixes this behaviour is [PD-1](#pd-1) ([Q-WR-17](#q-wr-17)).

<a id="wr-39"></a>
### WR-39 · Annotation, WAC-detection and import-rewrite scans are textual; signature parsing uses an AST

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Three textual scans** can be influenced by comments or string literals:
    - annotations ([WR-31](#wr-31)): only the leading block of `//` lines counts;
    - WAC detection ([WR-32](#wr-32)): a line scan that skips `//` lines but not block comments or strings;
    - pin removal and the `.ts` rewrite ([WR-26](#wr-26), [WR-28](#wr-28)): the specifier list comes from a parser, but it is applied by whole-text replacement.
  - **Signature parsing is not one of them:** entrypoint detection and the parameter list ([WR-7](#wr-7), [WR-8](#wr-8)) come from parsing the module as TypeScript and walking its top-level declarations. Comments and strings do not affect them.
- **Evidence:** `backend/windmill-macros/src/lib.rs:48-84`; `backend/windmill-worker/src/wac_executor.rs:221-270`; `backend/parsers/windmill-parser-ts/src/lib.rs:304-424` (AST signature), `:676-695`; `backend/windmill-worker/loader.bun.js:34-54` (all @ v1.767.0).
- **Conformance:** [WR-39/a–d](conformance.md#wr-39)
- **Design note (not Windmill behaviour):** a loader that claims equivalence must apply the three textual predicates exactly as written, and must parse signatures with an AST-equivalent parser.

<a id="wr-52"></a>
### WR-52 · What the pin-removal parse and the `.ts`-rewrite scanner see, and where a relative import is fetched

**Scope:** core · **Confidence:** high (the parse settings, the collected forms, the scanner call, the URL); medium (the TSX failure and the hashbang failure end to end; Bun's scanner output is from the `1.3.10` release binary run outside Windmill) · **Status:** drafted

- **Rule:** these details complete [WR-26](#wr-26), [WR-28](#wr-28) and [WR-39](#wr-39).
  - **Pin removal's specifier list** comes from its own parse of the source: TypeScript **with JSX enabled** (TSX), early errors off. It collects the text between the quotes of:
    - every import declaration, side-effect `import "…"` and **type-only imports included**;
    - `export … from` and `export * from`.

    It does **not** collect dynamic `import(…)`, `require(…)`, `import x = require(…)`, or `import(…)` in a type position. A pinned specifier that appears only in those forms is not unpinned, and its text elsewhere is left as it is.
  - **The TSX setting fails some TypeScript.** An angle-bracket type assertion (`<number>x`) is read as the start of a JSX element, so a module that contains one fails this parse. Pin removal runs before every Bun build and run of the script, in the deploy-time bundle and when `raw_unpinned` serves the module to an importer, so such a script can neither be bundled nor run: the run fails before the script starts, with `execution error:\nError: Error while parsing code, it is invalid TypeScript: …` ([WR-37](#wr-37)). `x as number` is unaffected. Other forms that TSX reads differently are [Q-WR-27](#q-wr-27).
  - **The `.ts` rewrite** runs Bun's `Bun.Transpiler` with the `ts` loader and its **`scanImports`** method, not `scan`, on the entry `main.ts` and on every fetched workspace module (which is then loaded as `tsx`). Only entries of kind `import-statement` can trigger a rewrite. On Bun `1.3.10`:
    - `export … from`, `export * from` and side-effect imports are reported as `import-statement`;
    - entries keep source order and are not deduplicated, so a specifier imported by two statements is listed twice ([WR-38](#wr-38));
    - type-only imports are left out, including an `import { type T } from …` whose specifiers are all type-only;
    - `import(…)` is reported as `dynamic-import` and `require(…)` as `require-call`, so neither triggers;
    - **a hashbang first line (`#!…`) makes `scanImports` throw** (`Unexpected #!…`), where `scan` accepts it. The loader's load hook fails with it, so every Bun build of a script that starts with a hashbang fails, lock generation and the deploy-time bundle included, with the [WR-20](#wr-20) error of program `bun build`.
  - **Where a relative import is fetched.** The loader builds the request as the URL `<base>/api/w/<workspace>/scripts/raw_unpinned/p/<importer path>/../<specifier>.ts`, and dot segments are then resolved over the **whole URL path**, as for any URL. They are not clamped at the workspace root: `..` segments that climb above the importer's top-level folder eat into `raw_unpinned/p/`, the request goes to another route, and the build fails with `Failed to find relative import at <url>`. From `f/a/s`, `./x` fetches `f/a/x`, but `../../../../f/a/x` reaches `/api/w/<workspace>/scripts/f/a/x.ts`. The dependency tracker, which resolves the same import for the bundle cache key ([WR-27](#wr-27)) and the dependency map, does clamp at the root, so it records `f/a/x` for both.
- **Evidence:** `backend/parsers/windmill-parser-ts/src/lib.rs:31-122` (the collector: import declarations, `export *` and `export … from`, type-only entries kept when not skipping; types not visited), `:130-165` (the TSX parse with early errors off), `:217-238` (the tracker's root-clamped normalisation), `:676-695` (`remove_pinned_imports` reads that list); `backend/windmill-worker/src/bun_executor.rs:1187`, `:1657`, `:1677`, `:1680` (pin removal before each build or run), `:1915-1917`; `backend/windmill-api-scripts/src/scripts.rs:2922-2926` (`raw_unpinned`); `backend/windmill-worker/loader.bun.js:30-50` (`Bun.Transpiler` with the `ts` loader, `scanImports`, kind `import-statement`), `:53-77` (the entry, then fetched modules, which load as `tsx`; `Failed to find relative import at …` at `:68`), `:79-121` (the URL built with `/../` at `:109`); `backend/windmill-dep-map/src/lib.rs:43-67` (the same clamped normalisation) (all @ v1.767.0). swc_ecma_lexer 23.0.2 `src/common/parser/expr.rs:1462-1475` (angle-bracket assertions are parsed only without JSX) and `:1651-1676` (with JSX, `<` starts an element). Bun `1.3.10`'s `scanImports` and `scan` outputs: the official release binary on the same inputs.
- **Conformance:** [WR-52/a–d](conformance.md#wr-52)
- **Note:** the scanner observations answer the scanner half of [Q-WR-13](#q-wr-13).

<a id="wr-53"></a>
### WR-53 · Bundled mode fixes `process.env.NODE_ENV` as `"development"`

**Scope:** core · **Confidence:** high (the build environment, from source); high (Bun's inlining, observed on the `1.3.10` release binary outside Windmill) · **Status:** drafted

- **Rule:**
  - Every Bun.build the worker runs (the deploy-time bundle, a bundle rebuilt at run time, lock generation, and the `node` and `browser` targets of `//nodejs` and `//native`) runs in a child process whose environment is cleared and refilled with only the common Bun variables of [WR-22](#wr-22): `PATH`, `HOME`, `TZ`, `FORCE_COLOR`, `DO_NOT_TRACK`, the two Bun cache variables, `BASE_URL`, and optionally `NODE_PATH` and `BUN_INSTALL_MINIMUM_RELEASE_AGE`. No `NODE_ENV`, no script `envs`, no workspace or worker-group variables. The build options set no `define`.
  - Bun.build replaces `process.env.NODE_ENV` with a constant taken from its own environment, `"development"` when the variable is unset. So in **bundled mode** ([WR-27](#wr-27)) a script reads `"development"`, whatever its job environment holds at run time. Other `process.env` reads stay run-time reads.
  - In **unbundled mode** (`//nobundling`, or no lock) the read happens at run time and sees the job environment of [WR-22](#wr-22): `undefined`, unless the script's `envs`, a workspace env var or a worker-group `env_vars` entry sets `NODE_ENV`.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:436-441` (lock generation), `:962-968`, `:1013-1019` (the bundle builds), each clearing the environment and adding `get_common_bun_proc_envs`; `:3402-3445` (those variables); `:900-937` (the build options: entry, target, plugin, `electron` external, minification; no `define`) (all @ v1.767.0). Bun `1.3.10`: a build of `process.env.NODE_ENV` with no `NODE_ENV` in the environment emits `"development"` for the `bun`, `browser` and `node` targets, and `bun run` without it reads `undefined`.
- **Conformance:** [WR-53/a](conformance.md#wr-53)

<a id="wr-54"></a>
### WR-54 · The `schema_validation` line, and what it switches on

**Scope:** later · **Confidence:** high (the line test); medium (which jobs validate and the error shape) · **Status:** drafted

- **Rule:**
  - **It is not a [WR-31](#wr-31) annotation.** The annotation parser has no `schema_validation` flag; a separate test reads it, with its own rules:
    - it walks the source's lines from the first and stops at the first line that does not begin with the language's comment marker: `//` for TypeScript (`bun`, `bunnative`, `nativets`, `deno`), `#` for Python and Bash, `--` for the SQL languages, `//!` for Rust. Nothing is trimmed before that test, so an indented line, a **blank line** or code ends the walk; WR-31, by contrast, skips blank lines;
    - it answers yes when one of the lines it walks, with trailing whitespace removed, is exactly the marker, **one space** and `schema_validation`: `// schema_validation` for TypeScript.

    So `//schema_validation`, `//  schema_validation`, `// schema_validation=true`, `// Schema_validation` and the exact line after a blank first line all leave validation off.
  - **What it switches on.** Before the script runs, the worker validates the job's arguments:
    - a deployed script stores the test's answer at deploy and is validated against its stored schema;
    - every other job, a deployed flow's inline step and previews included, runs the test on its own code at run time. The schema is the one the job carries; an inline flow step carries none, so the validator is built from the parsed signature ([WR-7](#wr-7)), with every parameter that has no default and is not optional required.
  - **A failed validation** fails the job before the script starts, as an execution-handler error ([WR-37](#wr-37)): a missing required argument gives `execution error:\nProblem with arguments: Argument <name> is required`. The type rules and their other messages are [Q-WR-26](#q-wr-26).
- **Evidence:** `backend/windmill-common/src/schema.rs:333-346` (the line test), `:479-482` (`schema_validation` with the language's marker), `:490-509` (required arguments first, then the rules); `backend/windmill-types/src/scripts.rs:133-143` (comment markers); `backend/windmill-api-scripts/src/scripts.rs:1192` (the test at deploy, stored with the script); `backend/windmill-common/src/cache.rs:684-693` (the stored flag gives a validator from the stored schema); `backend/windmill-worker/src/worker.rs:4227-4310` (`try_validate_schema`: the stored validator, otherwise the test on the job's code with the job's schema or the signature), `:4605-4613` (before the run); `backend/windmill-worker/src/schema.rs:85-97` (required parameters); `backend/windmill-common/src/error.rs:85-86` (`Problem with arguments: …`) (all @ v1.767.0).
- **Conformance:** [WR-54/a](conformance.md#wr-54)

<a id="wr-v4"></a>
### WR-V4 · Import normalisation can change ordinary source string values

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Pinned-package rewriting first discovers import strings, then replaces each matching string throughout the whole source, including matching text outside import syntax.
  - The Bun relative loader appends `.ts` the same way, by whole-text replacement, for the static relative imports it discovers.
  - So a data string or a comment containing the same text can change.
  - `raw_unpinned` applies package-pin removal to fetched imported source too.
  - Reproducing only an edit of the AST import specifier is therefore not fully equivalent.
- **Evidence:** `backend/parsers/windmill-parser-ts/src/lib.rs:676-695`; `backend/windmill-worker/loader.bun.js:34-54`, `:72-75`; `backend/windmill-worker/src/bun_executor.rs:1690`; `backend/windmill-api-scripts/src/scripts.rs:2922-2926` (all @ v1.767.0).
- **Conformance:** [WR-V4/a, WR-V4/b](conformance.md#wr-v4)
- **Note:** this expands [WR-26](#wr-26) and [WR-28](#wr-28); it also reaches dynamic-import literals.

<a id="wr-v9"></a>
### WR-V9 · Bun lock generation uses the loader's literal `raw_unpinned` endpoint

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - At this pin, the embedded Bun loader contains literal `scripts/raw_unpinned/p/` URLs.
  - Lock generation tries to replace a `RAW_GET_ENDPOINT` placeholder with `raw`, but the loader has no such placeholder, so the replacement does nothing.
  - So both Bun lock generation (which uses this loader) and ordinary Bun relative loading request `raw_unpinned`. Deno's separately generated import map uses `raw` ([WR-33](#wr-33)).
  - The difference between the two API endpoints is package-pin removal, not the choice of a historical script version.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:409-423`, `:836-845`; `backend/windmill-worker/loader.bun.js:108-111`; `backend/windmill-worker/src/deno_executor.rs:563-604`; `backend/windmill-api-scripts/src/scripts.rs:2651-2674`, `:2857-2865`, `:2922-2926` (all @ v1.767.0).
- **Conformance:** [WR-V9/a](conformance.md#wr-v9)
- **Note:** ordinary `core` runtime loading already uses `raw_unpinned` ([WR-28](#wr-28)). Only the Linux loader was checked; the Windows loader is `out`.

## G. Other languages (summary level)

<a id="wr-33"></a>
### WR-33 · Deno

**Scope:** later · **Confidence:** high (binding and endpoints); medium (other wrapper details) · **Status:** confirmed

- **Rule:**
  - **Same as Bun:**
    - args are matched by name and passed positionally;
    - `Date` hydration uses the same truthiness rule;
    - the same `BigInt` patch, and the same result `JSON.stringify` with the `undefined`→`null` replacer;
    - the same async-iterable stream rule;
    - preprocessor support.
  - **Different from Bun:**
    - **Error object:** `{message, name, stack}` plus `step_id`, **without `extra`**.
    - **No [WR-V1](#wr-v1) fallback:** Deno always binds by name, so an override with an empty parsed list gets **no** arguments.
    - **Imports:** an import map points `/`, `./` and `../` at the API's `raw` endpoint. Both `raw` and `raw_unpinned` serve the latest deployed, non-archived version; only `raw_unpinned` strips package pins. Bun's runtime loader and Bun lock generation both use `raw_unpinned` ([WR-V9](#wr-v9)).
    - **Command:** `deno run --no-check --import-map … --reload=<base>`.
    - **Permissions:** `-A` by default; sandbox mode gets a restricted list without `--allow-run`; `DENO_FLAGS` overrides both.
    - **Lock:** passed as `--lock=lock.json --frozen=false`, unless `DISABLE_DENO_LOCK` is set.
    - **`npm:` specifiers** are native.
    - **Environment:** includes `BASE_INTERNAL_URL` and `DENO_AUTH_TOKENS`.
- **Evidence:** `backend/windmill-worker/src/deno_executor.rs:91`, `:261-381` (wrapper; error object at `:370-378`), `:307-353` (spread), `:446-511` (command), `:563-604` (import map); `backend/windmill-worker/loader.bun.js:109-110`; `backend/windmill-worker/src/bun_executor.rs:409-423`, `:836-845`; `backend/windmill-api-scripts/src/scripts.rs:2651-2674`, `:2857-2865`, `:2922-2926` (all @ v1.767.0).
- **Conformance:** [WR-33/a](conformance.md#wr-33); see also [WR-V9/a](conformance.md#wr-v9).

<a id="wr-34"></a>
### WR-34 · Python

**Scope:** later · **Confidence:** high (the `**kwargs`, hydration and preprocessor rules); medium (the rest of the summary) · **Status:** confirmed

- **Rule:**
  - **Wrapper and import:**
    - A generated `wrapper.py` runs as `python -u -m wrapper`, in a fresh process per job.
    - The script is written under a dotted package path derived from its workspace path: digits and keywords get a `_` prefix, `-` becomes `_`, and the last segment is lowercased.
    - It is imported as `inner_script`.
  - **Args** (from `args.json`, read with non-strict JSON):
    - only the signature's parameters are passed, so extra keys are dropped;
    - a missing parameter without a default is passed `None`;
    - for a parameter **with** a default, a missing **or `null`** value removes the key, so the Python default applies (unlike Bun);
    - values equal to the sentinel `"<function call>"` are removed;
    - **`**kwargs` mains** are the exception: they get the **entire** args object as keyword arguments. A supplied `null` is then kept, and a missing required parameter raises `TypeError` inside the handler.
  - **Hydration by annotation:**
    - `bytes` ← base64-decode;
    - `datetime` ← `fromisoformat`;
    - `date` ← `fromisoformat`, then the fallbacks `%d-%m-%Y`, `%m/%d/%Y`, `%d/%m/%Y` and `%Y/%m/%d`, otherwise the raw string is kept;
    - nothing else: dataclasses, pydantic models, TypedDicts and resources stay plain dicts.
  - **Hydration runs before the error handler is set up.** Invalid base64 or datetime text therefore raises outside the `BaseException` handler. The traceback goes to stderr, `result.json` stays empty, and the job gets the [WR-20](#wr-20) log-tail error, not the `{message, name, stack}` object.
  - **Result serialisation:**
    - Before the dump: pandas and polars DataFrames become row lists; top-level `bytes`, or top-level `bytes` values in a dict, become base64.
    - The dump uses compact separators and `default=str`. So `datetime` becomes its `str()`, with a **space** rather than a `T`, and custom objects become their `str()`.
    - Afterwards, bare `NaN`, `Infinity` and `-Infinity` become `null`, and `\u0000` escapes become the text `null` surrounded by single spaces. The `#skip_result_postprocessing` annotation skips this step.
  - **Streams:** any non-container iterable result is streamed as `WM_STREAM: ` lines. A numpy array or a pandas Series takes this path. Chunks must be `str`.
  - **Neither `main` nor the preprocessor is awaited** by the normal wrapper. An async `main`'s coroutine is therefore serialised via `str()`. Runtime-specific coroutine text should be normalised. This does not carry over to the separate workflow-as-code wrapper.
  - **Errors:**
    - `except BaseException` writes `{message: str(e), name: <class name>, stack: <formatted traceback minus the first frame>, extra?: e.__dict__ when non-empty, step_id?}`, then exits 1.
    - So `sys.exit(0)` inside `main` is an error.
  - **Preprocessor** ([WR-V6](#wr-v6)):
    - it runs under the same condition as Bun's ([WR-11](#wr-11)) and is suppressed on the flow preprocessor step;
    - hydration uses the preprocessor's signature;
    - it is called synchronously, and its result is written to `args.json`;
    - `main` receives the returned object in memory, and `main`'s own annotations get no second hydration;
    - the worker re-reads `args.json` afterwards;
    - entrypoint overrides select the named callable.
  - **Relative imports:** `from f.x.y import …` and `from .x import …` are served by a `loader.py` meta-path finder. It fetches `…/scripts/raw/p/<path>.py` into `./tmp/…`.
  - **Dependencies:** there is one `uv pip install --no-deps --target` cache directory per pinned requirement, all joined on `PYTHONPATH`.
  - **Environment:** includes `BASE_INTERNAL_URL`.
- **Evidence:** `backend/windmill-worker/src/python_executor.rs:660-688` (module path), `:820-837`, `:937-1002` (wrapper; the transforms come before the handler at `:940-966`), `:966-990` (call, stream, write), `:974-983`, `:1211-1228`, `:1640-1807` (spread and hydration; `:1789-1807`), `:1662-1664`, `:1710-1816`, `:1883`, `:2198-2207`, `:2416` (deps), `:3288-3313` (`res_to_json`); `backend/windmill-worker/loader.py:24-91`; `backend/parsers/windmill-parser-py/src/lib.rs:297`, `:581-623` (all @ v1.767.0).
- **Conformance:** [WR-34/a–d](conformance.md#wr-34)

<a id="wr-35"></a>
### WR-35 · Go and Bash

**Scope:** later · **Confidence:** high (Bash); medium (Go) · **Status:** confirmed

- **Rule (Go):**
  - **Build:** the user file becomes package `inner`. A generated runner decodes `args.json` into a struct with one field per parameter, using the original parameter name as the JSON tag.
  - **Arg mapping:** missing keys give Go zero values, extra keys are ignored, and there are no defaults.
  - **Unsupported parameter types:** a parameter whose type the parser cannot render (selectors such as `time.Time`, pointers) gets an `interface{}` request field. Passing that field to the concretely typed parameter **fails to compile**, so the job fails at build. There is no untyped hydration.
  - **Return type:** `main` returns `(T, error)`, where `T` is any type assignable to `interface{}`, e.g. `(string, error)`. The generated runner widens it.
  - **Result:** `json.Marshal` of the value, so `nil` becomes `null`.
  - **Errors:** any returned error is printed and the program exits 1. That gives the [WR-20](#wr-20) log-tail error with program `go run`. A panic exits 2.
  - **Locks** ([WR-V7](#wr-v7)): the lock is split at `//go.sum` plus a newline into `go.mod` and `go.sum`.
    - With both parts present, setup runs `go mod download`; with only the `go.mod` part, `go mod tidy`.
  - **Caching:** binaries are cached by a hash of content plus lock. A cache hit skips dependency preparation, but the cached executable still runs as a fresh child per job.
  - **No preprocessor.**
- **Rule (Bash):**
  - **Parameter declarations** are matched **anywhere in the file**, on whole lines of the form `name="$N"`, `name="${N}"` or `name="${N:-default}"`.
    - N counts from 1, stops at the first gap, and is at most 19.
    - Each value is passed as a positional argument: JSON strings unquoted, other values as compact JSON, missing values as `""`.
  - **Result,** first match wins:
    1. a non-empty `./result.json`, as raw JSON;
    2. a non-empty `./result.out`, as a JSON string;
    3. the **last line** of the merged stdout and stderr, trimmed, as a JSON string;
    4. the string `No result.out, result2.out or result.json found`.

    `result.json`, `result.out` and `result2.out` are all pre-created empty. So a silent success yields `""`, and the step-4 message appears only if `result2.out` is missing.
  - **Failure:**
    - `main.sh` runs with `set -e`; `pipefail` is set only in the outer wrapper.
    - A failing command gives a non-zero exit, and so the [WR-20](#wr-20) log-tail error with program `bash run`, unless `result.json` is non-empty.
    - A failing command inside a user pipeline (`false | true`) does not fail the job by itself.
  - **Process:** SIGTERM is used on kill.
  - **Environment:** includes `BASE_INTERNAL_URL`.
  - **No preprocessor.**
- **Evidence:** `backend/windmill-worker/src/go_executor.rs:78`, `:105`, `:111-137`, `:149-229`, `:183`, `:390-420`, `:474-486`, `:608-626`, `:679-687`, `:705-721`; `backend/parsers/windmill-parser-go/src/lib.rs:100-171`; `backend/windmill-worker/src/bash_executor.rs:151-202`, `:208-238`, `:224-228`, `:349`, `:372-399`; `backend/parsers/windmill-parser-bash/src/lib.rs:107-150`; `backend/windmill-worker/src/common.rs:73-79` (`raw_to_string`) (all @ v1.767.0).
- **Conformance:** [WR-35/a–e](conformance.md#wr-35)

<a id="wr-36"></a>
### WR-36 · Dedicated workers and runner groups (long-lived processes)

**Scope:** EE · **Confidence:** high (source-read; EE, so not observable on CE) · **Status:** confirmed

- **Rule:**
  - **What they are:** with the proprietary `private` build feature, a script can be pinned to a long-lived process.
    - It reads stdin commands `execd:<json>`, `execd_preprocess:<json>`, `exec:<path>:<json>`, `exec_preprocess:<path>:<json>` and `end`.
    - It replies with stdout lines `wm_res[success]:<json>`, `wm_res[error]:<json>` or `wm_res[preprocessed_args]:<json>`.
  - **How it behaves:**
    - module state persists across jobs;
    - Dates are hydrated as in [WR-8](#wr-8), and results use the same replacer;
    - a per-call error object is `{message, name, stack, line: <raw args json>}`, with **no** `step_id` and **no** `extra`;
    - protocol-level failures (`Script not found: …`, a malformed command, a missing preprocessor) emit only `{message, name}`.
  - **Why it matters:** it is Windmill's own precedent for a warm-process protocol. But it is EE, and its semantics differ from the default path ([WR-1](#wr-1)).
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:88-351` (`generate_multi_script_wrapper`, compiled only with the `private` feature or in tests), `:3734-3960` (`start_worker`); `backend/windmill-worker/src/dedicated_worker_oss.rs` (all @ v1.767.0).
- **Conformance:** none (EE; not observable on CE).

<a id="wr-v6"></a>
### WR-V6 · Python preprocessing uses first-signature hydration and an in-memory handoff

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - The ordinary Python wrapper uses the same needs-preprocessing condition as Bun, and skips script preprocessing on the flow preprocessor step.
  - It hydrates inputs using the first function's signature.
  - It calls the preprocessor synchronously, serialises the result to `args.json`, and then binds the in-memory returned object for `main`. `main`'s annotations do not cause a second hydration pass.
  - After the process succeeds, the worker re-reads the args file as an object.
  - Entrypoint overrides select the named callable.
  - Neither the ordinary `main` nor this preprocessor is awaited automatically.
- **Evidence:** `backend/windmill-worker/src/python_executor.rs:820-837`, `:966-975`, `:1211-1228`, `:1662-1664`, `:1710-1721`, `:1789-1816` (all @ v1.767.0).
- **Conformance:** [WR-V6/a](conformance.md#wr-v6)
- **Note:** part of [WR-34](#wr-34).

<a id="wr-v7"></a>
### WR-V7 · Go locks contain module metadata and checksums

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Splitting:** a resolved Go lock is split at the literal `//go.sum` followed by a newline. The text before it is written as `go.mod`, the text after it as `go.sum`.
  - **Dependency preparation:** with both parts, it runs `go mod download`. A lock with only module text runs `go mod tidy`.
  - **Caching:** an executable cache hit bypasses dependency preparation. The cached executable still runs as a fresh child.
- **Evidence:** `backend/windmill-worker/src/go_executor.rs:78`, `:111-137`, `:390-420`, `:474-486`, `:608-626`, `:679-687` (all @ v1.767.0).
- **Conformance:** [WR-V7/a–c](conformance.md#wr-v7)
- **Note:** part of [WR-35](#wr-35).

## H. The native runtime: `nativets` and `bunnative`

This section answers semantics request 1: the boundary rules of the native runtime. It covers which steps run there ([WR-40](#wr-40)), arguments ([WR-41](#wr-41)), results ([WR-42](#wr-42)), errors ([WR-43](#wr-43)), limits ([WR-44](#wr-44)), the environment and global surface ([WR-45](#wr-45)), and streaming ([WR-46](#wr-46)). Evidence outside Windmill comes from the crates its lock pins: deno_core `0.352.1` (MIT; source pointers are to its crates.io package, `path_in_vcs = core`, commit `9487a912`) and the V8 crate `137.3.0`. None of it has been recorded on the oracle yet ([Q-WR-21](#q-wr-21)).

<a id="wr-40"></a>
### WR-40 · Which steps run in the native runtime, and how each kind is prepared

**Scope:** core (selection, preparation, the client import); later (`//native` with `//nobundling`); out (builds without `deno_core`, dedicated native workers) · **Confidence:** high (source-traced; oracle pending, [Q-WR-21](#q-wr-21)) · **Status:** confirmed

- **Rule:**
  - **Two languages, one runtime.** `nativets` and `bunnative` steps both run inside the worker process, in an embedded V8 isolate, not in a Bun child process. There is no dependency install at run time, no `args.json`, no `result.json` and no wrapper file.
  - **What makes a step `bunnative` is the `//native` header** ([WR-31](#wr-31), [SY-V3](openflow.md#sy-v3)), not the stored language.
    - At deploy, at lock generation and at preview, a `bun` or `bunnative` script is relabelled to follow its header: stored as `bunnative` with `//native`, as `bun` without it.
    - At run time the Bun executor decides from the header alone. A rawscript step that says `language: bunnative` without the header runs as an ordinary Bun step; one that says `language: bun` with the header runs in the native runtime.
  - **Who picks the job up.** A `bunnative` job carries the `nativets` tag; only its dependency job is tagged `bun`. A worker's default tags include `nativets`, so one default worker runs both kinds.
  - **How `nativets` code is prepared.**
    - The source is transpiled from TypeScript to JavaScript (types stripped, nothing bundled) and loaded as the ES module `file:///eval.ts`.
    - **How imports resolve.** Each specifier is resolved as a URL against `file:///eval.ts`, then loaded from the module map or from the worker's own filesystem. There is no allowlist and no other source:
      - The embedded client is registered beforehand under the URL `file:///windmill.ts`, so every spelling that resolves to that URL reaches it: `./windmill.ts`, `/windmill.ts` and `file:///windmill.ts`.
      - Any other `file:` URL is read from the worker's filesystem: `./helper.ts` and `/f/x/y.ts` mean the files `/helper.ts` and `/f/x/y.ts` on the worker host. Such a file loads if it exists and parses as JavaScript; it is not transpiled, and a `.json` file needs a JSON import attribute. Workspace scripts are never fetched, so a workspace script at the matching logical path does not help.
      - A bare specifier, or a URL with another scheme (`npm:`, `https:`), cannot be loaded.
    - **When a failed import surfaces.** A static import that cannot be loaded fails the module load, before `main` runs: the worker-side `failed to load module` error of [WR-43](#wr-43). A dynamic `import()` that fails rejects while `main` runs, like any other rejection: `main` can catch it, and uncaught it gives the three-field exception error of [WR-43](#wr-43).
    - The embedded client is a prebuilt bundle of an older TypeScript client. Its generated `OpenAPI.VERSION` is `1.326.0`, not `1.767.0`, so its function surface is older than the [client API](client-api.md) catalogue.
  - **How `bunnative` code is prepared.**
    - An ordinary `//native` script is always bundled, even without a lock: Bun.build with target **`browser`**, ESM output, and the minification settings, `electron` external and import plugin of [WR-27](#wr-27). Relative and `/f/` imports therefore follow [WR-28](#wr-28), and npm packages from the lock are inlined.
    - **Two exceptions:**
      - *(later)* if the header also has `//nodejs`, the bundle is built for the **`node`** target instead: the build checks `nodejs` before `native`, on the run path and in deploy-time prebundling alike. The bundle still runs in the native runtime ([WR-50](#wr-50) gives the run-time precedence);
      - a codebase script with `//native` is not bundled by the worker at all: its uploaded codebase bundle runs in the native runtime ([WR-50](#wr-50)).
    - The bundle is loaded as the module `file:///eval.ts`, and the bundle cache of [WR-27](#wr-27) applies.
    - The client is whatever `windmill-client` version the lock inlines; `windmill.ts` is not loaded.
  - **The build feature.** Native steps need Windmill's `deno_core` feature. The CE and EE images have it: their feature sets include `all_languages`. Without it a `nativets` step *succeeds* with the string `"require deno_core"`, and a `//native` step fails with an internal error. That case is `out`.
  - *(later, medium)* `//native` together with `//nobundling` skips the bundle, so the runtime finds no bundle file and the step fails with an `execution error:` I/O error.
- **Evidence:** `backend/windmill-worker/src/worker.rs:5094-5126` (`nativets` dispatch), `:3536-3574` (`do_nativets`), `:5381-5398` (`bun` and `bunnative` share the Bun executor); `backend/windmill-worker/src/bun_executor.rs:1442` (header parse), `:1199-1207` (prebundle target: `nodejs`, then `native`, then Bun), `:1993-2012` (the same order on the run path), `:900-945` (targets), `:2133-2245` (native execution reads the bundle); `backend/windmill-api-scripts/src/scripts.rs:1181-1190` (deploy relabelling); `backend/windmill-worker/src/worker_lockfiles.rs:1402-1408`, `:1763-1770`; `backend/windmill-queue/src/jobs.rs:5498-5505` (preview), `:6224-6231` (tag); `backend/windmill-common/src/worker.rs:248-276` (default tags); `backend/windmill-worker/src/js_eval.rs:110-140` (transpile, and the stubs without the feature); `backend/windmill-runtime-nativets/src/lib.rs:384-401` (transpile), `:591` (filesystem module loader, no transpiler for loaded files), `:639-668` (client and script modules registered by URL), `:876-894` (a static-load failure), `:1027` (the dynamic import of the script, run inside the execution); deno_core 0.352.1 `modules/loaders.rs:445-512` (URL resolution, then a file read; no other source), `module_specifier.rs:11-25` (bare specifiers are rejected), `modules/map.rs:1643-1644` (a failed dynamic import rejects its promise with a JavaScript error); `backend/windmill-runtime-nativets/src/windmill-client.js:123` (`VERSION: "1.326.0"`); `backend/Cargo.toml:177`, `:182-200` (features), `:511` (deno_core); `.github/workflows/docker-image.yml:95` (`features=ce`); `frontend/src/lib/script_helpers.ts:85-86`, `:97-103` (the editor's templates import `./windmill.ts` and `windmill-client`) (all @ v1.767.0).
- **Conformance:** [WR-40/a–f](conformance.md#wr-40)
- **Note:** the header test and the relabelling apply only to `bun` and `bunnative`. The worker dispatches a `nativets` step on its stored language alone, so a `nativets` script runs in the native runtime with or without a `//native` line. It is never handed to Bun.build: it is transpiled and loaded as described above, never bundled for any target, and its imports follow the URL rule above, not [WR-28](#wr-28). Of its header, only the fetch annotations of [WR-45](#wr-45) are read (`backend/windmill-worker/src/worker.rs:5094-5126`; `backend/windmill-api-scripts/src/scripts.rs:1181-1190` relabels only `bun` and `bunnative` @ v1.767.0).

<a id="wr-41"></a>
### WR-41 · A fresh isolate per job; how arguments bind

**Scope:** core (fresh state, binding); later (entrypoint overrides and preprocessors) · **Confidence:** high (state, binding); medium (the `nativets` override and preprocessor consequences) · **Status:** confirmed

- **Rule:**
  - **Fresh state.** Every native job creates a new isolate and drops it when the job ends. Module state, globals and timers never survive between jobs, as in [WR-1](#wr-1). Work still pending when the entrypoint's promise settles is abandoned: the isolate is dropped right after the result is read.
  - **Which names bind.** The parameter list comes from the same TypeScript signature parser as Bun ([WR-7](#wr-7)). It runs on the original source, not on the `bunnative` bundle.
  - **How each parameter gets its value.** For each parsed parameter, in order, the worker looks up the argument of the same name in the job's args map, after `$var:`/`$res:` resolution ([WR-5](#wr-5)):
    - present: its JSON text is parsed with `JSON.parse`, so a JSON `null` stays `null`;
    - absent: the slot is `undefined`, so a parameter default applies;
    - extra keys are ignored;
    - nothing is hydrated. A `Date`-annotated parameter receives the ISO string, unlike Bun's [WR-8](#wr-8).
  - **Entrypoint.**
    - `nativets` always calls `main`. An entrypoint override is ignored, so a `nativets` flow `preprocessor_module` runs `main` on the flow input, and a script-level preprocessor never runs.
    - `bunnative` honours an override ([WR-12](#wr-12)). It runs a script-level preprocessor in two passes: the preprocessor's result, a JSON object, becomes the args map for `main`.
    - With an override and zero parsed parameters, no arguments are passed. Bun's fallback of passing every non-underscore value ([WR-V1](#wr-v1)) does not exist here.
    - The call goes through the module namespace, so a missing export fails with a `TypeError` ([WR-43](#wr-43)).
- **Evidence:** `backend/windmill-runtime-nativets/src/lib.rs:735-748` (names and values), `:770-787` (a new isolate per job), `:849-850` (the isolate dropped after the result), `:1023-1027` (missing is `undefined`, present is parsed); `backend/windmill-worker/src/worker.rs:3547-3570` (`nativets` passes no override); `backend/windmill-worker/src/bun_executor.rs:2146-2239` (`bunnative` passes the override and runs the preprocessor in two passes) (all @ v1.767.0).
- **Conformance:** [WR-41/a–d](conformance.md#wr-41)

<a id="wr-42"></a>
### WR-42 · The result is `JSON.stringify(value ?? null)` with no replacer

**Scope:** core · **Confidence:** high (serialisation); medium (the V8 message text, and the completion checks applying unchanged) · **Status:** confirmed

- **Rule:**
  - The awaited return value `v` is serialised inside the isolate as `JSON.stringify(v ?? null)`, with **no replacer and no BigInt patch** (compare [WR-13](#wr-13)). The resulting text is taken as the result as is.
  - **Consequences:**
    - an object property holding `undefined` is **omitted**, where Bun keeps it as `null` ([WR-14](#wr-14));
    - an `undefined` array item becomes `null`, as in Bun;
    - a top-level `undefined` or `null` gives `null`;
    - a top-level function or symbol makes `JSON.stringify` return `undefined`, and the result is `null` (Bun's outcome is [Q-WR-12](#q-wr-12));
    - a `BigInt` anywhere fails the job with a `TypeError` in the [WR-43](#wr-43) shape. V8's text is `Do not know how to serialize a BigInt`; Bun would write the value as a string;
    - `toJSON` is honoured, so a `Date` becomes its ISO string. `NaN` and `±Infinity` become `null`, `Map` and `Set` become `{}`, and a cyclic value fails with a `TypeError`.
  - The text is never empty, so [WR-V2](#wr-v2) does not arise.
  - The completion-side rules still apply: the size checks of [WR-15](#wr-15) (but not its cloud-only reader limit) and the `wm_failure`/`wm_labels` markers of [WR-17](#wr-17).
  - An async-iterable result is a stream ([WR-46](#wr-46)).
- **Evidence:** `backend/windmill-runtime-nativets/src/lib.rs:1026-1034` (stringify), `:670-681` (an `undefined` outcome read back as `null`); `backend/windmill-worker/src/js_eval.rs:193-209` (the text passed on as is); completion as in [WR-15](#wr-15) and [WR-17](#wr-17) (all @ v1.767.0).
- **Conformance:** [WR-42/a–d](conformance.md#wr-42)
- **Design note (not Windmill behaviour):** a harness that runs native steps must serialise without the Bun replacer, or `undefined` fields come back as `null`.

<a id="wr-43"></a>
### WR-43 · The native error shapes

**Scope:** core (shapes); later (the texts of the rarer failures) · **Confidence:** high (shapes, from Windmill and deno_core 0.352.1); medium (detached rejections and timer throws; V8 and deno_core message texts) · **Status:** confirmed

- **Rule:** a failed native job stores `{"error": E}` ([WR-21](#wr-21)). `E` takes one of two forms.
  1. **A JavaScript exception reached the end of the run.** It came from the entrypoint, from evaluating the module, from a failed dynamic `import()` that the script did not catch ([WR-40](#wr-40)), or from serialising the result.
     - `E` has exactly the keys `message`, `stack` and `name`: `{"message": M, "stack": S, "name": N}`. There is **no `step_id`**, even inside a flow, and **no `extra`**: own properties such as `code` or `cause` are dropped (compare [WR-18](#wr-18)).
     - **For an `Error` instance** (any object whose prototype chain reaches `Error.prototype`), the `name` and `message` properties, own or inherited, are read **together** as two optional strings:
       - when both are strings (or `String` objects), `N` and `M` are those strings. So `new Error()` gives `M` = `""`, and a subclass that does not set `name` gives `Error`;
       - a property that is `null` or `undefined` gives `null` for that field alone;
       - when either holds anything else (a number, an object), the pair fails to decode and **both** `N` and `M` are `null`, however valid the other one was.
     - **`S` is the error's `stack` property, with one line in front of it** when the first stack frame is in the script's own module: the text of the source line that frame points at, and a newline.
       - `stack` is read on its own, so the name and message rule above does not affect it. It must be a string: any other value is dropped, which leaves at most the source-line prefix.
       - `S` is `null` when both parts are empty.
       - The line comes from the module text as loaded: the transpiled `nativets` code or the `bunnative` bundle, with the environment preamble of [WR-45](#wr-45) in front.
     - **For any other thrown value** (a string, a number, a plain object even with `message` and `name` fields, `null`, `undefined`), all three fields are `null`.
     - *(medium)* A rejection that nobody handles while the entrypoint is still pending, or an exception thrown from a timer callback, fails the job the same way with that value, even if the entrypoint would have succeeded.
  2. **Any other failure is a worker-side error:** `E` = `{"message": "execution error:\n<detail>", "name": "ExecutionErr", "step_id"?}` ([WR-37](#wr-37)), with these details:

     | Failure | `<detail>` |
     |---|---|
     | a `nativets` source that does not parse as TypeScript, caught when it is transpiled, before anything loads | `Error: <parser diagnostic>`, then a Rust location suffix ` @<file>.rs:<line>:<col>` to strip |
     | the module fails to load: a name the preamble already declares ([WR-45](#wr-45)), a static import that cannot be resolved or read ([WR-40](#wr-40)), any other error the engine finds while compiling or linking | `Error: failed to load module: <cause>`, then the location suffix |
     | the timeout ([WR-44](#wr-44)) | `Query timeout after (>Ns)` |
     | the heap cap ([WR-44](#wr-44)) | `Memory limit reached, killing isolate` |
     | the entrypoint's promise never settles and nothing else (no timer, no request) is pending | `Promise resolution is still pending but the event loop has already resolved` |
     | a `//proxy` header on CE ([WR-45](#wr-45)) | `Proxy is an EE feature` |

     A `bunnative` syntax error fails earlier, while bundling, with the [WR-20](#wr-20) error of program `bun build`.
- **Evidence:** `backend/windmill-worker/src/worker.rs:3557-3560` (the transpile runs before the runtime is called); `backend/windmill-runtime-nativets/src/lib.rs:384-401` (transpile errors), `:876-940` (load failure, exception shaping, the source line), `:1045-1058` (exception versus other failure), `:845-848` (heap), `:752-755` (proxy); `backend/windmill-worker/src/result_processor.rs:621-627` (a raw error value kept as is; other failures as `execution error:`), `:2020-2028`; `backend/windmill-common/src/error.rs:71-74`, `:146-151` (`Error: … @<location>`); `backend/windmill-worker/src/handle_child.rs:733-738` (timeout); deno_core 0.352.1 `error.rs:736-930` (fields read only from `Error` instances; other values give none), `:649-655` and `:763-769` (`name` and `message` decoded together, defaulting to none on failure), `:799-803` (`stack` kept only when it is a string), `:975-1000` (the prototype test); serde_v8 0.261.1 `de.rs:197-220` (a string or `String` object is required; `null` and `undefined` give none); `:76-78` (pending resolution), `runtime/jsruntime.rs:1921-1944`, `:2029` (event-loop exceptions and unhandled rejections end the run) (all @ v1.767.0 unless named).
- **Conformance:** [WR-43/a–i](conformance.md#wr-43)

<a id="wr-44"></a>
### WR-44 · Timeout, heap and stack limits

**Scope:** core (timeout); later (heap, stack) · **Confidence:** high (timeout; the ignored stack flag and V8's default, from the pinned V8 source); medium (when the heap check takes effect; that the prebuilt V8 library uses the default build configuration) · **Status:** confirmed

- **Rule:**
  - **Timeout duration.** The instance maximum comes from the worker's `TIMEOUT` environment variable, 604 800 s (7 days) by default on a self-hosted instance. The duration then depends on whether the job has a timeout of its own (a flow step's `timeout`, [FX-24](execution.md#fx-24)):
    - **a timeout strictly below the maximum:** that timeout;
    - **a timeout at or above the maximum:** the maximum;
    - **no timeout:** the instance's default job timeout (the `job_default_timeout` setting) when it is set and strictly below the maximum, otherwise the maximum.
  - **Timeout outcome.**
    - The message is always `Query timeout after (>Ns)`, where `N` is the duration in whole seconds, rounded down. There is no "job-specific" wording, whichever branch chose the duration.
    - The isolate is terminated; no process is killed.
  - **Heap.** The isolate's heap is capped at 128 MiB. Reaching the cap produces the `Memory limit reached, killing isolate` failure ([WR-43](#wr-43)). The check takes effect only when the script next waits for a timer or I/O, because V8 raises the limit temporarily so the synchronous part can continue. A script that never waits is [Q-WR-23](#q-wr-23).
  - **Stack.**
    - Windmill passes `--stack-size=1024` to V8, but as the **first** element of the flag list, the position V8's command-line parser treats as the program name and skips. The setting therefore has **no effect**. Only the second flag (`--no-harmony-import-assertions`) applies, and the check for unrecognised flags skips the first position as well, so nothing reports the loss.
    - The limit is thus V8's built-in default for the pinned V8 13.7.152.14: **984 KiB** on the 64-bit targets the images ship (`linux/amd64`, `linux/arm64`). V8 uses 864 KiB on 32-bit ARM and x86 and 960 KiB in AddressSanitizer builds. No other flag in deno_core or Windmill sets it.
    - Overflow throws `RangeError` with the message `Maximum call stack size exceeded`. It is an ordinary exception: caught, it is a value; uncaught, it gives the [WR-43](#wr-43) form 1 error with `name` `RangeError`.
    - The overflow depth depends on V8's frame sizes, so no fixture may assert an exact depth or byte limit.
    - This limit is unrelated to the expression engine's ([JE-48](expressions.md#je-48)).
- **Evidence:** `backend/windmill-worker/src/handle_child.rs:723-738`; `backend/windmill-worker/src/common.rs:1056-1114` (duration, including the no-timeout branch `:1095-1111`); `backend/windmill-worker/src/worker.rs:663` (`JOB_DEFAULT_TIMEOUT`); `backend/windmill-common/src/global_settings.rs:31` (`job_default_timeout`); `backend/windmill-common/src/worker.rs:211`, `:295-298` (the `TIMEOUT` environment variable and its default); `backend/windmill-worker/src/worker.rs:677` (the maximum as a duration); `backend/src/monitor.rs:2506-2513` (the default job timeout is loaded from the instance setting); `backend/windmill-runtime-nativets/src/lib.rs:353-359` (the flag list and the skipped first position), `:587-589` (heap cap), `:607-615` (the near-limit callback reports and raises the limit), `:845-848` (the failure is taken when the run next yields), `:196-205` (termination); deno_core 0.352.1 `flags.rs:3-8` (the first element is ignored), `runtime/setup.rs:26-72` (deno_core's own flags, none of them a stack size); V8 at the commit pinned by the `v8` crate 137.3.0 ([`denoland/v8@f68bbb6c`](https://github.com/denoland/v8/tree/f68bbb6cda689a14d019a6a60cc93963724e7c35)): `include/v8-version.h:11-14`, `src/flags/flags.cc:601-614` (parsing starts at the second element), `src/flags/flag-definitions.h:2645-2646` (`stack_size` defaults to `V8_DEFAULT_STACK_SIZE_KB`), `src/common/globals.h:170-192` (984, 864 and 960 KiB) (all @ v1.767.0 unless named).
- **Conformance:** [WR-44/a–d](conformance.md#wr-44)
- **Design note (not Windmill behaviour):** the `1024` in Windmill's flag list is not a limit Windmill applies, so it is not a compatibility requirement. A harness on another engine (Bun's JavaScriptCore) overflows at its own depth either way, and only the overflow's error shape is portable.

<a id="wr-45"></a>
### WR-45 · What the native runtime offers: environment, globals and `fetch`

**Scope:** core (environment names, the global surface); later (`fetch` user agent, proxy and certificates) · **Confidence:** high (environment, installed globals); medium (globals that come from V8 and deno_core defaults: `Intl`, `queueMicrotask`, `Deno`; the custom-env consequence) · **Status:** confirmed

- **Rule:**
  - **Environment.** There is no real process. The worker puts a short preamble in front of the script's module text. It declares a module-scope `const process = { env: … }` and fills `process.env` with:
    - `BASE_URL` and `BASE_INTERNAL_URL`, both the worker's internal base URL as configured (no `localhost` rewrite, unlike [WR-22](#wr-22));
    - the reserved `WM_*` variables of [WR-22](#wr-22), plus the workspace's custom env vars and the worker group's `env_vars`;
    - nothing else: no `PATH`, `HOME` or `TZ`, no script `envs`, no proxy or OTel variables, no `WM_PIPELINE`.

    Values are written as single-quoted string literals with backslash, quote, LF and CR escaped, so each value arrives as the same string.
  - **Module-scope names.** The preamble is part of the script's own module, so its declarations are bindings of that module.
    - Both kinds declare `process`, `BASE_URL` and `BASE_INTERNAL_URL`.
    - `nativets` also declares every variable above as a `const` of the same name. A `nativets` script can read `WM_WORKSPACE` as a bare identifier; in `bunnative` that is a `ReferenceError`.
    - A script that declares one of these names at its top level fails to load with a redeclaration `SyntaxError` ([WR-43](#wr-43)).
    - *(medium)* The same load failure hits every `nativets` script of a workspace that has a custom env var whose name is not a valid JavaScript identifier.
    - The embedded `nativets` client gets the same preamble. It reads `BASE_INTERNAL_URL`, then `BASE_URL`, and `WM_TOKEN` from its own `process.env`.
  - **Installed globals.** Each is the implementation from deno_web, deno_url, deno_fetch, deno_crypto or deno_console:

    | Group | Names |
    |---|---|
    | network | `fetch`, `Request`, `Response`, `Headers`, `FormData` |
    | URLs | `URL`, `URLSearchParams`, `URLPattern` |
    | encoding | `atob`, `btoa`, `TextEncoder`, `TextDecoder`, `TextEncoderStream`, `TextDecoderStream` |
    | files | `Blob`, `File`, `FileReader` |
    | timers | `setTimeout`, `clearTimeout`, `setInterval`, `clearInterval`, `queueMicrotask` |
    | crypto | `crypto`, `Crypto`, `CryptoKey`, `SubtleCrypto` |
    | events | `Event`, `EventTarget`, `CustomEvent`, `MessageEvent`, `CloseEvent`, `ErrorEvent`, `reportError`, `AbortController`, `AbortSignal`, `DOMException` |
    | streams | `ReadableStream`, `ReadableStreamDefaultReader`, `ReadableStreamBYOBReader`, `ReadableStreamDefaultController`, `ReadableByteStreamController`, `ReadableStreamBYOBRequest`, `WritableStream`, `WritableStreamDefaultWriter`, `WritableStreamDefaultController`, `TransformStream`, `TransformStreamDefaultController`, `ByteLengthQueuingStrategy`, `CountQueuingStrategy`, `CompressionStream`, `DecompressionStream` |
    | messaging and timing | `MessageChannel`, `MessagePort`, `structuredClone`, `performance`, `Performance`, `PerformanceEntry`, `PerformanceMark`, `PerformanceMeasure` |
    | logging | `console`; each call becomes one job-log message |

    There are also V8's ECMAScript built-ins, including `Intl` (deno_core builds in the ICU data by default) and `WebAssembly`, and a `Deno` object that holds only deno_core's internal `core`. `globalThis` is an `EventTarget`.
  - **Absent:** `Bun`, `Buffer`, `require`, `module`, `exports`, `__dirname`, `__filename`, `setImmediate`, `WebSocket`, `navigator`, `EventSource` and `ImageData` (their modules load, but they are not exposed), the Deno CLI's `Deno.*` APIs, Node built-in modules, and every filesystem API. `process` is only the `{env}` object above.
  - **`fetch`** reaches any host: no permission check applies. The default `User-Agent` is `windmill/beta`.
    - Header annotations are read from the leading run of lines that start with `//`. Unlike [WR-31](#wr-31), a blank line ends the run.
    - `//useragent <text>` sets the user agent.
    - `//proxy http(s)://[user:pass@]host[:port]` routes requests through a proxy; on CE the step fails with `Proxy is an EE feature`.
    - TLS trusts the default web roots, plus any roots named by the worker's `DENO_CERT`, `SSL_CERT_FILE` and `NODE_EXTRA_CA_CERTS`, and the system store when `DENO_TLS_CA_STORE` lists `system`.
- **Evidence:** `backend/windmill-worker/src/worker.rs:5108-5120` (the `nativets` preamble with `const` names); `backend/windmill-worker/src/bun_executor.rs:3447-3462` (the `bunnative` preamble), `:2144` (it is used); `backend/windmill-worker/src/common.rs:507-559` (reserved, workspace and worker-group variables); `backend/windmill-runtime-nativets/src/lib.rs:876-890` (the preamble prepended to the client and script modules), `:403-441` (annotations), `:557-582` (user agent, proxy and extensions), `:84-173` (permissions allow everything), `:251-331` (certificates), `:752-755` (proxy on CE); `backend/windmill-runtime-nativets/src/runtime.js:33-122` (installed globals), `:126-146` (`globalThis` as an `EventTarget`); `backend/windmill-runtime-nativets/src/windmill-client.js:9861-9877` (the client's environment reads); deno_core 0.352.1 `01_core.js:833` (`queueMicrotask`), `Cargo.toml:30-33` (ICU data by default) (all @ v1.767.0 unless named).
- **Conformance:** [WR-45/a–g](conformance.md#wr-45)
- **Design note (not Windmill behaviour):** the global list is what a load-time check of native scripts can compare free identifiers against.

<a id="wr-46"></a>
### WR-46 · Native result streaming

**Scope:** later · **Confidence:** medium (source-traced; how a non-JSON stream result is stored is pending) · **Status:** confirmed

- **Rule:**
  - **Producing the stream.** If the awaited value is async-iterable, each chunk is logged as `WM_STREAM: ` followed by the chunk, with every LF escaped as the two characters `\n`. A chunk must be a string.
  - **A non-string chunk, or an error from the iterator, ends the stream silently.** The job does not fail, and the remaining chunks are lost. Bun fails with a `TypeError` instead ([WR-16](#wr-16)).
  - The entrypoint's result is then `null`.
  - **Extraction.** Each `console` message that starts with `WM_STREAM: ` is taken out of the logs, its `\n` pairs are decoded, and it is appended to the stream. The check applies to the whole message, not to each of its lines.
  - **Merging.** If the result is `null` and the stream is non-empty, the result becomes the **raw concatenated stream text**, not a JSON string as in Bun, and it is not checked to be JSON. An object result gets no `wm_stream` field.
    - The chunks `"1"` and `"2"` give the number `12`.
    - The chunks `"\"a"` and `"b\""` give the JSON string `"ab"`.
    - How Windmill stores a stream whose text is not JSON is [Q-WR-22](#q-wr-22).
- **Evidence:** `backend/windmill-runtime-nativets/src/lib.rs:992-1019` (chunks, and the catch that ends the stream), `:1029-1031`, `:796-843` (extraction), `:851-866` (merging); `backend/windmill-common/src/result_stream.rs:6-15`; for Bun's merge, `backend/windmill-worker/src/common.rs:438-462` (all @ v1.767.0).
- **Conformance:** [WR-46/a, WR-46/b](conformance.md#wr-46)

## Design notes (not Windmill behaviour)

These are proposals for windmill-lite's equivalence contract, not facts about Windmill. Pending design choices do not by themselves waive any `core` equivalence requirement above.

<a id="pd-1"></a>
- **PD-1 ([WR-38](#wr-38)):** reproduce Windmill's `.ts`-rewrite collision failures, or fix them and document the deviation. *Settled by:* a decision recorded in the loader's design record ([Q-WR-17](#q-wr-17)).

<a id="pd-2"></a>
- **PD-2 ([WR-10](#wr-10), layer 2):** emulate `jsonb` key ordering for unresolved nested arg values, or document the deviation. *Settled by:* a decision recorded in the JSON-boundary design record, informed by the [Q-WR-4](#q-wr-4) oracle result ([Q-WR-18](#q-wr-18)). A proposed deviation is not an already accepted exception to the core contract.

<a id="pd-module-state"></a>
- **Fresh module state ([WR-1](#wr-1)):** a warm harness breaks module-state and late-timer behaviour by design. The default mode needs an explicit decision, and the conformance suite needs a "fresh module state" mode that reproduces WR-1 ([Q-WR-19](#q-wr-19)).

## Open questions

<a id="q-wr-1"></a>
- **Q-WR-1** · affects [WR-15](#wr-15), [WR-V2](#wr-v2) · answered: see [WR-V2](#wr-v2).

<a id="q-wr-2"></a>
- **Q-WR-2** · affects [WR-19](#wr-19), [WR-20](#wr-20) · open (oracle; the exact value is `later`) · What exit status and stderr does Bun `1.3.10` produce for `throw null`, for a top-level module throw, and for an unawaited rejection while the entrypoint is pending? Does that rejection end the process before the result write? The `exit_code` `1` expected in WR-19/a is a prediction. *Settled by:* running three synthetic scripts on the v1.767.0 oracle image and recording `exit_code`, `error.message` and success or failure for each.

<a id="q-wr-3"></a>
- **Q-WR-3** · affects [WR-29](#wr-29) · answered: see [WR-29](#wr-29). It is settled from source, including the explicit-path exception. An optional oracle confirmation (flow `f/a/fl` with inline imports of `./x` and `../x`) is tracked in [Q-WR-20](#q-wr-20).

<a id="q-wr-4"></a>
- **Q-WR-4** · affects [WR-10](#wr-10), [WR-V8](#wr-v8) · open · This question has two sides.
  - **Wrapper side:** source establishes that a static nested object input `{"bb":1,"a":2}` reaches a Bun step as keys `["a","bb"]` (WR-10 layer 2), including through the database driver's decoder. It has not been observed on the oracle.
  - **Expression side:** what key order a transform expression sees for `results.x` (the in-memory previous result or the fetched results) belongs to [execution](execution.md) and [expressions](expressions.md). Do not infer it from the wrapper rule.

  *Settled by:* an oracle flow with one static-input step and one `results.prev` step, each returning `Object.keys` (WR-10/a), plus the expression-side trace in those areas.

<a id="q-wr-5"></a>
- **Q-WR-5** · affects [WR-6](#wr-6), [WR-7](#wr-7) · answered: see [WR-7](#wr-7). This covers the wrapper and parser; the deployed-schema consequences of an arrow `main` belong to [OpenFlow](openflow.md).

<a id="q-wr-6"></a>
- **Q-WR-6** · affects [WR-37](#wr-37) · answered: see [WR-37](#wr-37). This covers CE and self-hosted timeouts; cancellation of the containing flow belongs to [execution](execution.md).

<a id="q-wr-7"></a>
- **Q-WR-7** · affects [WR-18](#wr-18) · open, `later` · Which own properties do Bun-native errors carry under Bun `1.3.10`, e.g. a `fetch` failure or a `SystemError` with `code`/`errno`/`syscall`? Do they include own `line`, `column` or `sourceURL`? No universal list of native extras is assumed. *Settled by:* an oracle script that lets `fetch("http://127.0.0.1:1")` fail uncaught, recording `error.extra`.

<a id="q-wr-8"></a>
- **Q-WR-8** · affects [WR-26](#wr-26) · open · Does Bun `1.3.10` support `npm:`-prefixed specifiers in Bun scripts, through lock generation and the run, or reject them? They stay outside the core boundary until this is settled. *Settled by:* an oracle deploy and run of a script importing `npm:<synthetic or small public package>`, recording whether lock generation and the run succeed.

<a id="q-wr-9"></a>
- **Q-WR-9** · affects [WR-16](#wr-16) · answered: see [WR-16](#wr-16). A numeric chunk fails with `TypeError`.

<a id="q-wr-10"></a>
- **Q-WR-10** · affects [WR-18](#wr-18), [WR-27](#wr-27) · open, `later` · What exact `error.stack` text does each of the bundled and unbundled modes produce, so that the conformance normaliser can strip it reliably? *Settled by:* an oracle run of a throwing script in bundled and `//nobundling` modes, recording `error.stack`.

<a id="q-wr-11"></a>
- **Q-WR-11** · affects [WR-34](#wr-34) · answered: see [WR-34](#wr-34). The ordinary Python wrapper does not await an async `main`.

<a id="q-wr-12"></a>
- **Q-WR-12** · affects [WR-13](#wr-13) · open, `later` · What does Bun `1.3.10` do when the result file write receives `undefined`, as with a top-level function or symbol result? Does the job fail, and with which `error.name` and `error.message`, or is something written? *Settled by:* an oracle run of `return () => 1` (WR-13/d).

<a id="q-wr-13"></a>
- **Q-WR-13** · affects [WR-38](#wr-38), [WR-28](#wr-28) · **Re-scoped 2026-10-28.** The scanner half is answered in [WR-52](#wr-52): the loader calls `scanImports` with the `ts` loader, and on the Bun `1.3.10` release binary that method keeps source order, does not deduplicate, reports `export … from` and `export * from` as `import-statement`, leaves type-only imports out, and throws on a hashbang line. What remains is whether WR-38's fixtures fail on the oracle as predicted. *Settled by:* WR-38/a and WR-38/b on the oracle, plus a variant importing `./x` twice.

<a id="q-wr-14"></a>
- **Q-WR-14** · affects [WR-10](#wr-10), [WR-5](#wr-5) · answered: see [WR-10](#wr-10) (layer 3). From source, on the ordinary stored-resource path, an unchanged stored resource keeps `jsonb` order (WR-10/e). This is not oracle-observed, and it does not cover every special resource path.

<a id="q-wr-15"></a>
- **Q-WR-15** · affects [WR-16](#wr-16) · open, `later` · This tracks the validation correction to two clauses of WR-16:
  - string `+` performs JavaScript primitive conversion (`Symbol.toPrimitive` or `valueOf`), not always `toString()`;
  - every output line is classified on its own for the stream prefix, so lines after a raw newline can still join the stream.

  *Settled by:* adopting the corrected rules as stated in WR-16. WR-16/f and WR-16/g discriminate between the two versions; an oracle run can corroborate them but is not needed to identify the error.

<a id="q-wr-16"></a>
- **Q-WR-16** · affects [WR-28](#wr-28) · open, `later` · This tracks the validation correction to WR-28's dynamic-import consequence. An identical static import is sufficient but not necessary for a dynamic-import literal to be rewritten, because a qualifying static specifier can match a substring of a different dynamic specifier. Interception then depends on the resulting specifier. *Settled by:* adopting the whole-text rule, and running WR-28/e as an integration fixture on the pinned image (two workspace helpers, no local-file shortcut). Scanner details remain [Q-WR-13](#q-wr-13).

<a id="q-wr-17"></a>
- **Q-WR-17** · affects [WR-38](#wr-38) · open (design decision, `later`) · Should windmill-lite reproduce the conditional rewrite-collision behaviour, or explicitly exclude it from the equivalence contract ([PD-1](#pd-1))? *Settled by:* a loader design record that states the choice and its conformance scope, informed by [Q-WR-13](#q-wr-13).

<a id="q-wr-18"></a>
- **Q-WR-18** · affects [WR-10](#wr-10), [WR-V8](#wr-v8) · open (design decision, `core`) · How will windmill-lite preserve the layer-specific `jsonb` ordering at the step boundary, or will it explicitly declare a deviation ([PD-2](#pd-2))? *Settled by:* a JSON-boundary design record stating the implementation and equivalence choice, with the WR-10 and WR-V8 fixtures. The [Q-WR-4](#q-wr-4) oracle confirmation is useful, but the source rule no longer depends on it.

<a id="q-wr-19"></a>
- **Q-WR-19** · affects [WR-1](#wr-1) · open (design decision) · How does windmill-lite's warm harness expose the fresh-module-state contract, and which mode is the default ([design notes](#pd-module-state))? *Settled by:* a module-state design record, including the WR-1 repeated-invocation fixtures and any explicitly documented deviation. Windmill's fresh-process behaviour itself is confirmed.

<a id="q-wr-20"></a>
- **Q-WR-20** · affects [WR-3](#wr-3), [WR-29](#wr-29), [WR-V2](#wr-v2), [WR-V9](#wr-v9) and every other fixture in this area · open (oracle) · No fixture in this area has been recorded on the v1.767.0 CE oracle. Beyond the checks in Q-WR-2, Q-WR-4, Q-WR-7, Q-WR-8, Q-WR-10, Q-WR-12, Q-WR-13, Q-WR-15 and Q-WR-16, these expectations explicitly await a recording:
  - the running Bun version (WR-3/a), to be recorded in fixture metadata by the [oracle](oracle.md) harness;
  - the inline-flow import base (WR-29/a, WR-29/b), the optional confirmation from Q-WR-3;
  - the empty-result `InternalErr` (WR-V2/a);
  - the `raw_unpinned` request during lock generation (WR-V9/a).

  All other cases in this area are also source-derived. *Settled by:* recording these fixtures on the oracle and updating their provenance.

<a id="q-wr-21"></a>
- **Q-WR-21** · affects [WR-40](#wr-40) to [WR-46](#wr-46) · open (oracle) · No native case has been recorded. Beyond the shapes, these texts and behaviours are source-derived only:
  - the exact `stack` text, and the V8 and deno_core messages: `Do not know how to serialize a BigInt`, the redeclaration `SyntaxError`, the `<cause>` of a load failure;
  - the detached-rejection outcome (WR-43/d);
  - the present and absent globals (WR-45/d, WR-45/e);
  - whether a `bunnative` bundle built for the `browser` target polyfills Node built-ins such as `Buffer` or `node:` imports, or fails on them;
  - the error that a failed dynamic import rejects with (WR-40/e, WR-40/f): its `name` and the rest of its message after the resolved URL;
  - the native stack limit: 984 KiB is V8's default, and source shows that Windmill's `--stack-size=1024` never reaches V8 (WR-44). A recording can confirm that overflow happens (WR-44/b) but cannot measure the byte limit;
  - the CJS codebase bundle under `//native` (WR-50), expected to fail on its first use of `module`, `exports` or `require`.

  *Settled by:* recording WR-40/a to WR-46/b on the v1.767.0 CE oracle (its default worker serves the `nativets` tag), plus WR-50/c and a CJS variant of it.

<a id="q-wr-22"></a>
- **Q-WR-22** · affects [WR-46](#wr-46) · open (oracle, `later`) · How is a native stream result stored when its concatenated text is not JSON (chunks `"a"` and `"b"`)? Does completion fail, and with which error, or is the text stored some other way? *Settled by:* an oracle run of that script, recording the job's result, success flag and stream rows.

<a id="q-wr-23"></a>
- **Q-WR-23** · affects [WR-44](#wr-44) · open (oracle, `later`) · What happens when a native script passes the 128 MiB heap cap without ever waiting for a timer or I/O? Does the job fail, succeed, or take the worker down? *Settled by:* an oracle run of a synchronous allocation loop, recording the job outcome and whether the worker survives.

<a id="q-wr-24"></a>
- **Q-WR-24** · affects [WR-47](#wr-47), [WR-50](#wr-50) · open (oracle) · Which Node `20.x` patch does the v1.767.0 image run, and so does an ESM bundle sent to Node by `//nodejs` load without a `package.json` asset declaring its type (module syntax detection is the default from 20.19.0)? Does windmill-lite's bundle for each WR-47 fixture equal the one the pinned CLI uploads? *Settled by:* WR-50/b and WR-50/e on the oracle, and pushing the WR-47 fixtures with the pinned `wmill` to the CE standalone oracle, then reading the stored bundles from its standalone bundle directory.

<a id="q-wr-25"></a>
- **Q-WR-25** · affects [WR-49](#wr-49) · open (`later`) · Two packaging edges are not settled:
  - is `main.js` empty, as traced, for a multi-output entry whose file name has two dots?
  - how does the worker's tar unpacking treat an asset `to` path that is absolute or contains `..`?

  *Settled by:* a source trace of the Rust `tar` crate's unpack rules at the version the pinned lock uses, and an oracle deploy of both cases.

<a id="q-wr-26"></a>
- **Q-WR-26** · affects [WR-54](#wr-54) · open (`later`) · Which rules does argument validation apply beyond required arguments, for each parsed type (strings with enums or patterns, numbers, integers, booleans, arrays, objects, resource types, `null` values), and with which messages? WR-54 covers only how the `schema_validation` line is read, which jobs validate, and the missing-argument message. *Settled by:* a source trace of the validator's rules, then oracle runs of one failing argument per type.

<a id="q-wr-27"></a>
- **Q-WR-27** · affects [WR-52](#wr-52) · open · Besides angle-bracket type assertions, which TypeScript forms fail the TSX-mode pin-removal parse? A generic arrow function written `<T>(x: T) => x` is the likeliest; `<T,>(x: T) => x` and `<T extends unknown>` should parse. *Settled by:* a trace of swc_ecma_lexer 23.0.2's TSX arrow handling, and oracle previews of WR-52/a, WR-51/d and the arrow variants, recording success or the error text.

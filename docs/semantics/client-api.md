# The wmill client API and the platform interface

This area covers what a Windmill script sees of the platform: the environment
variables a job receives, the `wmill` client libraries (TypeScript
`windmill-client` and Python `wmill`), the HTTP endpoint behind each client
function and how those endpoints answer and fail, and what a local platform
interface must reproduce, including delegating steps to a real Windmill. Flow
structure and the loader are in [openflow.md](openflow.md); job execution,
suspend/resume and early return in [execution.md](execution.md); loop and branch
bodies in [control-flow.md](control-flow.md); static `$res:` and argument
transforms in [expressions.md](expressions.md); language runtimes, result streams
and error shapes in [languages.md](languages.md); the oracle in
[oracle.md](oracle.md).

**Pinned version:** Windmill `v1.767.0` (commit `6de4ec0f665ad856002495c4d86269105ec28ad7`).
Source pointers are `path:line @ v1.767.0` relative to the Windmill repository
root. Status, scope and confidence tags are defined in the [index](README.md#legend).

**ID prefixes in this file:** `WA-` (the wmill client API and the platform
interface); `WA-Vn` findings were added during validation.

**Conventions in this file:**

- **Licences.** `typescript-client/`, `python-client/`, `go-client/` and
  `backend/windmill-api/openapi.yaml` are Apache-2.0 (`typescript-client/package.json`
  says "Apache 2.0", `jsr.json` and `python-client/wmill/pyproject.toml` say
  "Apache-2.0"). Their files and exported identifiers are named freely here.
  `backend/**` is AGPLv3 with proprietary `private`/`enterprise`-gated parts;
  its behaviour is described in prose with pointers only.
- **The S3 job-helper server is not public.**
  `backend/windmill-api/src/job_helpers_oss.rs:1-111` is a stub that re-exports
  `job_helpers_ee` under the `private` feature, and a pure OSS build registers
  no job-helper routes (`:46-49`). Its server behaviour is knowable only from the
  oracle.
- **TS** means `windmill-client@1.767.0` (npm; its version equals the Windmill
  version). **Python** means the `wmill` package
  (`python-client/wmill/wmill/client.py`).
- **Routes** are written relative to the client's `OpenAPI.BASE`, which is
  `<base>/api`; `{ws}` is the workspace id (`WM_WORKSPACE`). In examples,
  `…/` abbreviates `/api/w/{ws}/`.
- **Facade** (a windmill-lite design term, used only in design notes and test
  set-ups): an HTTP server that answers the client's platform calls in place of
  Windmill. A *recording facade* is a test server that returns chosen responses
  and records the requests; cases that use one test client behaviour only.
- Fixtures use synthetic paths (`u/x/…`, `u/t/…`, `f/t/…`); job ids are
  normalised (`<flow>`, `F`, `S`).

## Summary for the design

1. **The client is HTTP over about 25 endpoints, configured from env.** TS
   fixes `OpenAPI.BASE` and `OpenAPI.TOKEN` when the module is first evaluated,
   from `BASE_INTERNAL_URL ?? BASE_URL` and `WM_TOKEN`; only the legacy `task()`
   dispatch reads `WM_TOKEN` live. Every other `WM_*` is read at call time. A Bun
   job gets only `BASE_URL` from the worker; Python jobs get `BASE_INTERNAL_URL`
   (and `BASE_URL` in some branches), and the Python client never reads
   `BASE_URL`. ([WA-1](#wa-1), [WA-2](#wa-2), [WA-3](#wa-3))
2. **There are five import surfaces:** TS named exports, the TS default object
   (which has no `OpenAPI`), the generated TS root (`OpenAPI`, `ApiError`,
   services), Python module functions, and Python `Windmill` instance members.
   Runtime keys must be tested separately from type-only names. The generated
   root's `ApiError`/global-`fetch` contract holds only for an artifact generated
   with the fetch transport ([Q-WA-6](#q-wa-6)). ([WA-8](#wa-8), [WA-9](#wa-9),
   [WA-V4](#wa-v4), [WA-V8](#wa-v8))
3. **Per-invocation identity is a core requirement.** `process.env` and the
   import-time `OpenAPI.TOKEN` are process-global, so concurrent in-process steps
   cannot get their own `WM_*` view or be told apart by token. A candidate
   mechanism works on Bun 1.4.2 and must pass a gate on the pinned Bun 1.3.10
   ([Q-WA-4](#q-wa-4)). Windmill's own dedicated worker (EE) drops per-job env.
   ([WA-31](#wa-31), [WA-7](#wa-7))
4. **Keep three root notions apart.** The **stored `root_job`** is often null by
   normalisation. The **effective root** drives `getRootJobId`, the process
   `WM_ROOT_JOB_ID` and the flow-user-state key. The **innermost scope** drives
   `WM_ROOT_FLOW_JOB_ID` and resets only when a `flow` module is invoked or for a
   parallel-loop body. Body sub-flows follow the full "simple" predicate and live
   at `loop-i`, `forloop-i`, `branchone-k` and `branchall-i`. Flow user state is a
   per-root-run map with `null` for missing keys, gone once the root completes
   (a later write gets 404, a read `null`); TS swallows its errors
   (including a failed root lookup) unless `errorIfNotPossible`; the Python
   setter ignores HTTP failures, but the Python getter parses non-404 bodies and
   may raise, and a failed Python root lookup raises. ([WA-V1](#wa-v1),
   [WA-6](#wa-6), [WA-14](#wa-14))
5. **Interpolation has three contexts:** raw reads; client interpolated reads
   (no job id, so `$WM_*` stays literal); worker argument resolution (with a job
   id, and a fresh API depth counter for a nested `$res:`). The depth guard is
   `>= 50` on every node; arrays are walked only at depth ≤ 2 with at most 1000
   items; `$var:` and `$jsonvar:` substitutions are terminal. Inside API resource
   interpolation `$WM_*` uses the job's **stored** fields, so `$WM_ROOT_JOB_ID`
   can be `""`; direct worker resolution of `$<name>` and the process env use the
   worker's **computed** values. ([WA-12](#wa-12), [WA-V3](#wa-v3),
   [WA-40](#wa-40))
6. **Error bodies are part of the contract and differ by language.** The API
   status table is exhaustive: `AIError` is 400, `MetricNotFound` and
   `SerdeJson` are 500, and a missing nested `$res:` gives the located not-found
   form. TS rewraps resource and variable errors; Python wraps HTTP errors as
   `<url>: <status>, <text>`, except at the S3 upload and delete boundaries.
   ([WA-42](#wa-42), [WA-10](#wa-10), [WA-11](#wa-11), [WA-15](#wa-15),
   [WA-3](#wa-3), [WA-V9](#wa-v9), [WA-V11](#wa-v11))
7. **An HTTP 501 alone does not fail a step with the stock clients:** async
   dispatch returns the body as a job id, and the flow-state and Python read
   helpers swallow it. Under a `text/plain`, non-JSON 501, generated services
   reject with an `ApiError` whose `message` is `Not Implemented` and whose
   `statusText` is whatever the response carries. How such a step ends is a
   windmill-lite policy decision ([Q-WA-11](#q-wa-11)). ([WA-41](#wa-41),
   [WA-V5](#wa-v5), [WA-V8](#wa-v8))
8. **Object store:** the S3 client contract is verified from the Apache source;
   CE availability (10 GiB quota) comes only from the public docs; the pinned CE
   server behaviour is unobserved, and the CE image builds it from closed-source
   modules, so only the oracle can settle it ([Q-WA-3](#q-wa-3)). ([WA-16](#wa-16))
9. **Hybrid delegation.** `run_wait_result` answers 200/422, waits 600 s by
   default and cancels the job when the wait ends without a result; for a flow
   with `early_return` it can answer while the flow keeps running. A preview
   does not snapshot transitive code. Inlining a by-path step keeps its state
   path but changes `WM_JOB_PATH`, and its runnable id depends on the step
   representation. Previews need generic `jobs:run`; deployed paths need
   per-path scopes. Delegated steps read the real workspace. ([WA-50](#wa-50),
   [WA-V6](#wa-v6), [WA-V7](#wa-v7), [WA-V10](#wa-v10), [WA-51](#wa-51))
10. **Prior art:** windmill-ts (Unlicense) offers a local sync-folder source
    seam, JSON-Schema quirk fixes for typed methods, and a resource-resolver
    hook. ([WA-60](#wa-60))

## A. How the client is configured

<a id="wa-1"></a>
### WA-1 · The TS client is a thin HTTP layer configured from env vars; token and base URL are captured at import

**Scope:** core (client configuration); later (mock-file retry) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - `windmill-client` consists of (a) a module generated from
    `backend/windmill-api/openapi.yaml` by `@hey-api/openapi-ts@0.43.0` (the
    `*Service` classes and a mutable `OpenAPI` config object; root exports in
    [WA-V8](#wa-v8)) and (b) hand-written helpers in `client.ts`.
  - **Import-time capture.** The build patches the generated `OpenAPI` object
    so that, when the module is first evaluated:
    - `OpenAPI.BASE` is the first of `BASE_INTERNAL_URL`, `BASE_URL` or the
      default `http://localhost:8000` that is set, followed by `/api`;
    - `OpenAPI.TOKEN` is `WM_TOKEN`;
    - `WITH_CREDENTIALS` is `true`.
  - Changing `process.env.WM_TOKEN` or `BASE_*` afterwards has no effect on any
    request that uses `OpenAPI.TOKEN`/`OpenAPI.BASE` (the generated services and
    the raw-fetch helpers) unless `setClient()` is called. The exception is the
    legacy HTTP dispatch in TS `task()`, which reads `WM_TOKEN` at call time
    ([WA-32](#wa-32)).
  - `setClient(token?, baseUrl?)` re-applies the same defaults, except that a
    missing token becomes the literal `"no_token"`.
  - **Call-time reads.** Every other helper reads its env var when called:
    `WM_WORKSPACE` (default `"no_workspace"`), `WM_JOB_ID`, `WM_FLOW_JOB_ID`,
    `WM_ROOT_FLOW_JOB_ID`, `WM_FLOW_STEP_ID`, `WM_STATE_PATH_NEW`/`WM_STATE_PATH`,
    `WM_BASE_URL` (public URL, default `"http://localhost:3000"`),
    `WM_MOCKED_API_FILE`, `WM_PIPELINE`, `WM_WAC_INLINE_FAST_PATH`. `getEnv`
    reads `process.env` (or `window.process.env` in a browser).
  - **Mock file.** `WM_MOCKED_API_FILE` is read on every call until a mock
    object has been loaded; an unset variable is not cached. Once loaded, the
    mock is cached for the module's lifetime ([WA-17](#wa-17)).
- **Evidence:** `typescript-client/build.sh:7` (generator version), `:8-33`
  (`OpenAPI` patch), `:43` (named-export list), `:137-223` (default export);
  `typescript-client/client.ts:61-74` (`setClient`), `:76-78`, `:80-87`,
  `:93-95`, `:1457-1497` (mock-file loading), `:1875-1887` (live `WM_TOKEN` read
  in `task()`).
- **Conformance:** [WA-1/a, WA-1/b](conformance.md#wa-1)

<a id="wa-2"></a>
### WA-2 · Which base-URL variables a job sees

**Scope:** core (Bun); later (Python) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Bun (non-native).** The worker clears the environment, then sets, in
    order: the job's `envs`, the reserved variables ([WA-4](#wa-4)), the proxy
    envs, and the common Bun envs; later sets override earlier ones. The common
    Bun envs are `PATH`, `HOME`, `TZ`, `FORCE_COLOR=1`, `DO_NOT_TRACK=1`,
    `BUN_INSTALL_CACHE_DIR`, `BUN_RUNTIME_TRANSPILER_CACHE_PATH=0`,
    `BASE_URL=<base internal URL with "localhost" replaced by "127.0.0.1">`, and
    optionally `NODE_PATH` and `BUN_INSTALL_MINIMUM_RELEASE_AGE`.
    - `BASE_URL` is applied last, so it overrides any configured value of the
      same name.
    - The worker never sets `BASE_INTERNAL_URL`. That name can still arrive
      through the job's `envs`, workspace custom env vars or worker-config
      `env_vars`, and then it survives. Without such an override the TS client
      resolves its base from `BASE_URL`.
  - **Python.** The nsjail branch sets both `BASE_INTERNAL_URL` and `BASE_URL`;
    the ordinary (non-nsjail) branch sets only `BASE_INTERNAL_URL`; the shared
    helper for other Python process kinds sets both. The Python client resolves
    its base as `base_url` argument → `BASE_INTERNAL_URL` → `WM_BASE_URL` and
    never reads `BASE_URL`, so in every branch it uses `BASE_INTERNAL_URL`.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:2340-2400` (env
  clearing and order), `:3401-3445` (`get_common_bun_proc_envs`);
  `backend/windmill-worker/src/python_executor.rs:1118-1134` (nsjail),
  `:1149-1172` (non-nsjail), `:3446-3449`;
  `backend/windmill-common/src/worker.rs:2200-2206` (`WHITELIST_ENVS`);
  `python-client/wmill/wmill/client.py:46-52`.
- **Conformance:** [WA-2/a, WA-2/b](conformance.md#wa-2)
- **Design note (not Windmill behaviour):** set both `BASE_URL` and
  `BASE_INTERNAL_URL` to the facade origin for every language; that covers every
  Windmill branch, and both clients resolve to it.

<a id="wa-3"></a>
### WA-3 · Python client: one global client, created lazily from env

**Scope:** later (Python) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Module-level `wmill.*` functions share one global `Windmill()` instance,
    created on the first call (`init_global_client`). At construction it
    captures `base_url`, `token` (`WM_TOKEN`), `workspace` (`WM_WORKSPACE`,
    asserted non-empty), `WM_JOB_PATH`, and the mocked-API file.
  - It uses `httpx.Client` with a 900 s timeout and
    `Authorization: Bearer <token>`.
  - HTTP errors from the client's `get`/`post` methods are raised as
    `Exception("<full request url>: <status>, <response text>")`.
  - Per-job values (`WM_JOB_ID`, `WM_FLOW_JOB_ID`, `WM_ROOT_FLOW_JOB_ID`,
    `WM_STATE_PATH*`, `WM_FLOW_STEP_ID`) are read from `os.environ` at call time.
  - The timeout and the error wrapping cover `get`/`post` only. Raw S3 upload
    and delete are separate requests with their own error boundaries
    ([WA-V9](#wa-v9), [WA-V11](#wa-v11)).
- **Evidence:** `python-client/wmill/wmill/client.py:27`, `:34-67`, `:92-147`,
  `:1406-1414` (`init_global_client`).
- **Conformance:** [WA-3/a](conformance.md#wa-3); S3 boundaries in
  [WA-V9](conformance.md#wa-v9) and [WA-V11](conformance.md#wa-v11).

<a id="wa-v9"></a>
### WA-V9 · Python S3 upload distinguishes transport/JSON errors from response-shape errors

**Scope:** later (Python) · **Confidence:** high · **Status:** confirmed

- **Rule:** `write_s3_file` handles errors in three stages:
  1. **Preflight, before any request.** It parses the S3 object, which can
     raise the `ValueError`s of [WA-16](#wa-16), then checks the content type:
     anything other than `bytes` or `BufferedReader` raises
     `Exception("Type of file_content not supported")`. These errors propagate
     unwrapped.
  2. **Request and JSON decode, inside one catch.** The request is sent with
     `Content-Type: application/octet-stream` and no timeout. Any failure here
     (transport error, non-JSON body) raises
     `Exception("Could not write file to S3")`, chained to the cause.
  3. **After the catch.** No HTTP status check is made (no `raise_for_status`).
     The parsed body is indexed for `file_key` outside the catch, so a body
     without `file_key` raises an unwrapped `KeyError`, and a non-2xx response
     whose JSON body has `file_key` is accepted as success.

  This differs from the ordinary `Windmill.post` ([WA-3](#wa-3)) and from TS.
- **Evidence:** `python-client/wmill/wmill/client.py:946-1011` (preflight
  `:970-979`, request and JSON catch `:992-1010`, `file_key` indexing `:1011`).
- **Conformance:** [WA-V9/a, WA-V9/b, WA-V9/c](conformance.md#wa-v9)
- **Note:** these are client boundaries tested against a recording facade; they
  say nothing about the real server's error bodies.

<a id="wa-v11"></a>
### WA-V11 · Python S3 delete error boundaries, including an unwrapped preflight

**Scope:** later (Python) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Preflight, before any request.** `delete_s3_object` parses the S3 object
    and builds the query outside its request catch. An invalid S3 URI string
    raises its original `ValueError`; a mapping without `s3` raises an
    unwrapped `KeyError` while building the query. Neither makes an HTTP call.
  - **Request block.** It calls `raise_for_status`: HTTP errors raise
    `Exception("<url>: <status>, <text>")`, and any other failure inside the
    block raises `Exception("Could not delete file from S3")`.
- **Evidence:** `python-client/wmill/wmill/client.py:1013-1045` (`:1028-1045`),
  `:2214-2238`.
- **Conformance:** [WA-V11/a, WA-V11/b, WA-V11/c](conformance.md#wa-v11)

## B. What a job sees in its environment

<a id="wa-4"></a>
### WA-4 · Reserved ("contextual") variables injected into every job

**Scope:** core (the eleven variables listed in the Note); later (the rest) · **Confidence:** high (names and formulas; which job fields are populated is refined by WA-V1 and WA-V10); high (the token's workspace check, source-traced) · **Status:** confirmed; drafted (the token's workspace check)

- **Rule:** every job's process env contains the variables below. "Parent" is
  the job's `parent_job`: the enclosing flow, or a loop-iteration or branch
  sub-flow ([WA-6](#wa-6)).

  | Name | Value |
  |---|---|
  | `WM_WORKSPACE` | workspace id |
  | `WM_TOKEN` | per-job ephemeral bearer token with the run's permissions |
  | `WM_EMAIL` | `permissioned_as_email` |
  | `WM_USERNAME` | `created_by` |
  | `WM_BASE_URL` | the instance's public `BASE_URL` setting |
  | `WM_JOB_ID` | this job's UUID |
  | `WM_SCHEDULED_FOR` | `scheduled_for` as RFC 3339 with whole seconds and `Z` (e.g. `2026-01-02T03:04:05Z`), else `""` |
  | `WM_JOB_PATH` | the job's runnable path, else `""` |
  | `WM_FLOW_JOB_ID` | parent job UUID, else `""` |
  | `WM_ROOT_FLOW_JOB_ID` | `flow_innermost_root_job` (the innermost flow scope, [WA-V1](#wa-v1)), else `""` |
  | `WM_ROOT_JOB_ID` | the effective root: first non-null of `root_job`, `flow_innermost_root_job`, `parent_job`, own id (always non-empty) |
  | `WM_FLOW_PATH` | parent job's `runnable_path`, else `""` |
  | `WM_SCHEDULE_PATH` | schedule path if schedule-triggered, else `""` |
  | `WM_PERMISSIONED_AS` | `u/<user>` or `g/<group>` |
  | `WM_STATE_PATH` | see [WA-5](#wa-5) |
  | `WM_FLOW_STEP_ID` | flow module id (`a`, `b`, …), else `""` |
  | `WM_OBJECT_PATH` | in a flow: `<flow_path with / replaced by _>/<schedule_path with / replaced by _, or "manual">/<step_id>/<epoch_ms>_<job_id>`; otherwise `<script_path with / replaced by _>/<schedule-or-manual>/<epoch_ms>_<job_id>` |
  | `WM_WORKER_GROUP` | worker group name |
  | `WM_RUNNABLE_ID` | the job's runnable id (script hash), else `""`; depends on the flow-step representation ([WA-V10](#wa-v10)) |
  | `WM_END_USER_EMAIL` | app end user's address, else `""` |
  | `WM_TESTED_RUNNABLE` | `{kind}/{path}` for CI-test-triggered jobs, else `""` |

  - These are followed by the workspace's **custom env vars** (cached for
    15 minutes). After that, the worker-config `env_vars` are inserted and
    **override** any same-named entry.
  - `WM_STATE_PATH_NEW` is read by every client but **never set** by the
    v1.767.0 backend (no assignment was found in the worker or common source).
  - `WM_PIPELINE=true` is added only for data-pipeline runs.
  - The `WM_ROOT_JOB_ID` rule above is the worker's process-env rule (the
    computed effective root). API resource interpolation of `$WM_ROOT_JOB_ID`
    uses the stored field instead ([WA-V3](#wa-v3)).
  - **`WM_TOKEN` is bound to the job's workspace.** It is `jwt_` followed by
    a token signed with the instance's internal secret, minted when the job
    is pulled; it names the job's workspace and the job id, and carries the
    permissioned-as identity. The API takes the workspace of a call from the
    URL (`/api/w/<ws>/…`):
    - **Authenticated routes** accept the token only when `<ws>` is the
      job's workspace. For any other `<ws>`, existing or not, the call is
      answered 401 `Not authorized: Unauthorized` (`text/plain`) before the
      handler runs, so before any path, query or body rejection. Among the
      routes the stock clients use, these are variables, resources (and so
      state, [WA-13](#wa-13)), flow user state, dispatch (`jobs/run/…`) and
      the object-store helpers (`job_helpers/…`).
    - **The unauthenticated job routes** (`/api/w/<ws>/jobs_u/…`:
      `get_root_job_id`, `completed/get_result`,
      `completed/get_result_maybe`, `get`) check no token. A token that is
      not valid for `<ws>` is ignored, so the caller is anonymous, and `<ws>`
      only scopes the job lookup ([WA-V2](#wa-v2), [WA-19](#wa-19)).

    The stock clients build every path from `WM_WORKSPACE`, so a script
    reaches another workspace only by passing it explicitly.
- **Evidence:** `backend/windmill-common/src/variables.rs:273`, `:280-480`,
  `:491-522` (custom env cache); `backend/windmill-worker/src/common.rs:508-571`
  (worker-side assembly, `build_envs_map` override order), `:535-558`
  (runnable id passed into env construction), `:1834-1839` (root-job rule);
  `backend/windmill-worker/src/worker.rs:5165-5180` (`WM_PIPELINE`). The
  token's workspace check: `backend/windmill-queue/src/jobs.rs:3182-3210`
  (minted per job for its workspace); `backend/windmill-common/src/auth.rs:235-243`,
  `:542-576` (the workspace claim, the `jwt_` prefix);
  `backend/windmill-api-auth/src/auth.rs:182-195` (a claim for another
  workspace is refused), `:672-690`, `:756-761` (the workspace read from the
  URL), `:829` (the 401); `backend/windmill-api/src/lib.rs:547-549`,
  `:595`, `:716-717` (the authenticated group and its layers), `:827-830`
  (the unauthenticated job routes outside it).
- **Conformance:** [WA-4/a, WA-4/b](conformance.md#wa-4)
- **Note:** the core variables are `WM_WORKSPACE`, `WM_JOB_ID`,
  `WM_FLOW_JOB_ID`, `WM_ROOT_FLOW_JOB_ID`, `WM_ROOT_JOB_ID`, `WM_FLOW_PATH`,
  `WM_FLOW_STEP_ID`, `WM_JOB_PATH`, `WM_BASE_URL`, `WM_TOKEN` and
  `WM_STATE_PATH`.
- **Revision 2026-10-29 (SC3, source re-check, chain PF1):** added the
  workspace binding of `WM_TOKEN`. Milestone PF1 found that the reference did
  not say whether a call's `{workspace}` is checked against the caller; the
  source checks it against the token's own workspace on every authenticated
  route (401 `Not authorized: Unauthorized`) and not at all on the
  unauthenticated job routes.

<a id="wa-5"></a>
### WA-5 · State path formula

**Scope:** core (the value; the state store is WA-13) · **Confidence:** high · **Status:** confirmed

- **Rule:** let `trigger` be the job's `created_by` username when the job has a
  schedule path, otherwise the literal `user`. Then:
  - **Flow step:** `WM_STATE_PATH = <flow_path>/<step_id or "nostep">_<trigger>`,
    where `flow_path` is the parent's runnable path ([WA-6](#wa-6)).
  - **Standalone script with a path:** `<script_path>/<trigger>`. A script path
    ending in `/` gets `state` appended first.
  - **Otherwise:** `u/<username>/tmp_state`.

  The clients read `WM_STATE_PATH_NEW ?? WM_STATE_PATH`. If neither is set, TS
  throws `Error("State path not set")` and Python raises
  `Exception("State path not found")`.
- **Evidence:** `backend/windmill-common/src/variables.rs:300-321`;
  `typescript-client/client.ts:558-564`; `python-client/wmill/wmill/client.py:1188-1199`.
- **Conformance:** [WA-5/a, WA-5/b, WA-5/c](conformance.md#wa-5)
- **Note:** a transformation preserves a step's state path only if it keeps the
  parent flow path, the step id **and** the trigger ([WA-50](#wa-50)).

<a id="wa-6"></a>
### WA-6 · When loop and branch bodies run as sub-flows, and the runnable paths that drive `WM_FLOW_PATH`, `WM_JOB_PATH` and `WM_STATE_PATH`

**Scope:** core (top-level steps); later (exact parity inside bodies) · **Confidence:** high (source-traced; per-variable values await oracle corroboration, Q-WA-13) · **Status:** confirmed

- **Rule:**
  - A step job's runnable path is `<parent runnable path>/<module id>` for
    inline modules, and `<parent runnable path>/preprocessor` for the
    preprocessor. A `script` module keeps the referenced script's path. An
    unreduced `rawscript` with an explicit `path` uses that path, and a reduced
    `flowscript` uses its inner path ([WA-V10](#wa-v10)).
  - Whether a body runs as its own sub-flow job depends on the module:
    - **Sequential `forloopflow`/`whileloopflow`:** the body runs directly under
      the flow **only if it is "simple"**: exactly one module, itself simple
      (`script`, `rawscript` or `flowscript`), with no `sleep`, `suspend`,
      `cache_ttl`, `retry`, `stop_after_if`, `stop_after_all_iters_if` or
      `skip_if`, no enabled `mock`, **and no `failure_module` on the flow**.
      Otherwise each iteration is a sub-flow at `<flow>/loop-<index>`.
    - **Parallel `forloopflow`:** **always** one sub-flow per iteration, at
      `<flow>/forloop-<i>`, even for one-module bodies.
    - **`branchone`:** the chosen branch runs as a sub-flow at
      `<flow>/branchone-0` for the default branch and at
      `<flow>/branchone-<i+1>` for `branches[i]`. An empty chosen branch creates
      no job.
    - **`branchall`:** each branch runs as a sub-flow at `<flow>/branchall-<i>`.
  - Worker env assembly looks up the immediate parent's runnable path. Steps
    inside a sub-flow therefore see `WM_FLOW_PATH` equal to the sub-flow's path,
    `WM_FLOW_JOB_ID` equal to the sub-flow job, a `WM_STATE_PATH` under that
    prefix ([WA-5](#wa-5)), and the innermost-scope rule of [WA-V1](#wa-v1).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:5038-5074`
  (`payload_from_modules`), `:5076-5086` (`get_path`), `:5352-5394` (parallel
  loop), `:5491-5513` (branchone index), `:5546-5553` and `:5617-5630`
  (branchall), `:5669-5712` (sequential loop), `:5729-5746`
  (`is_simple_modules`); `backend/windmill-worker/src/common.rs:513-526`
  (parent runnable-path lookup).
- **Conformance:** [WA-6/a, WA-6/b, WA-6/c, WA-6/d](conformance.md#wa-6)
- **Note:** loop and branch semantics are in [control-flow.md](control-flow.md).
  Answers [Q-WA-1](#q-wa-1).

<a id="wa-v1"></a>
### WA-V1 · Stored root fields, effective root identity and innermost flow scope are distinct

**Scope:** core · **Confidence:** high (enqueue arguments, storage normalisation, env and API lookup traced) · **Status:** confirmed

- **Rule:** for ordinary OpenFlow execution:
  - **Effective root.** A flow-created child's effective root is the effective
    root of its parent flow.
  - **Innermost scope.** A child of flow F gets an unset innermost root (it
    starts a new innermost scope) when its module is a `flow` module or a
    parallel `forloopflow` body. Otherwise it inherits F's innermost root, or F
    itself. `branchone`, `branchall` and sequential-loop bodies therefore keep
    F's innermost scope.
  - **Stored root.** The queue drops a stored `root_job` that equals the
    innermost root, the parent or the job itself. A top-level flow step
    normally has a **null stored `root_job`**, while `getRootJobId()` and its
    process `WM_ROOT_JOB_ID` still identify the outer flow.
  - **Consequence.** Nested steps can have different `WM_ROOT_FLOW_JOB_ID`
    values while sharing the same effective root, and so the same flow user
    state ([WA-14](#wa-14)).
  - This rule covers flow-created children. It must not be generalised to jobs
    dispatched separately by a client ([WA-15](#wa-15)) without tracing their
    submitted ancestry.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:4318-4330`,
  `:4419-4421`; `backend/windmill-queue/src/jobs.rs:6373-6382`;
  `backend/windmill-worker/src/common.rs:1834-1839`;
  `backend/windmill-api/src/jobs.rs:498-507`.
- **Conformance:** [WA-V1/a](conformance.md#wa-v1)
- **Note:** answers [Q-WA-5](#q-wa-5) and, with [WA-6](#wa-6),
  [Q-WA-1](#q-wa-1).

<a id="wa-v10"></a>
### WA-V10 · Runnable identity depends on the flow-step representation

**Scope:** later (hybrid delegation fidelity) · **Confidence:** high (payload, queue storage and env construction traced) · **Status:** confirmed

- **Rule:** with no custom env overrides:
  - an ordinary deployed workspace-script step has `WM_RUNNABLE_ID` equal to
    the string form of its selected script hash;
  - an unreduced `rawscript` step has an empty runnable id;
  - an inline script reduced to a `flowscript` has the string form of the
    flow-node id's `ScriptHash`. It is not the identity of a deployed script
    merely because the source text is the same. Flow reduction creates this
    representation, and deployed-flow loading prefers an available "lite"
    version under its version and configuration gates, falling back to the
    original version.

  There is therefore no universal "inline means empty" rule, and no promise
  that the two forms' strings always differ. A `rawscript`'s explicit `path`,
  when supplied, overrides the ordinary `<flow path>/<step id>` job path; a
  reduced `flowscript` uses its inner path.
- **Evidence:** `backend/windmill-common/src/jobs.rs:80-165` (workspace path to
  deployed hash, separate hub branch); `backend/windmill-worker/src/worker_flow.rs:5928-5980`,
  `:5996-6012`, `:6065-6130` (script, rawscript and flowscript payloads);
  `backend/windmill-worker/src/worker_lockfiles.rs:748-777`, `:1612-1650` (lite
  representation); `backend/windmill-queue/src/jobs.rs:5369-5417`,
  `:5477-5530`, `:5912-5931` (ids and representation selection);
  `backend/windmill-common/src/cache.rs:889-910` (flow retrieval);
  `backend/windmill-worker/src/common.rs:535-558` and
  `backend/windmill-common/src/variables.rs:464-468` (runnable id to env).
- **Conformance:** [WA-V10/a, WA-V10/b](conformance.md#wa-v10)
- **Note:** answers [Q-WA-12](#q-wa-12).

<a id="wa-7"></a>
### WA-7 · Windmill's own warm processes do not provide per-job env

**Scope:** EE (dedicated worker); the precedent informs core design · **Confidence:** high (read) · **Status:** confirmed

- **Rule:**
  - Windmill's dedicated-worker mode (a warm Bun process per script) computes
    the reserved variables **once at startup**: job id `"NOT_AVAILABLE"`,
    e-mail `dedicated_worker@windmill.dev`, username and permissioned-as
    `dedicated_worker`, empty flow fields (so the state path is
    `<script_path>/user`), and one token for all jobs.
  - The native (V8) runtime instead builds a per-job JS `process.env` object
    from the reserved variables rather than using the OS environment.
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:3766-3790`
  (dedicated worker, behind the `private` feature, so EE), `:3448-3462`
  (`build_nativets_env_code`).
- **Conformance:** none (design precedent).
- **Note:** the native construction is a precedent for supplying env as a JS
  object per invocation; it does not prove that isolation works in one shared
  Bun process ([WA-31](#wa-31)).
- **Design note (not Windmill behaviour):** windmill-lite must not copy the
  dedicated worker's loss of per-job env ([WA-31](#wa-31)).

## C. The client surface: what each call does on the wire

<a id="wa-8"></a>
### WA-8 · Catalogue of client functions → endpoints → platform service

**Scope:** core (TS runtime and type surfaces); later (Python); out (workflow-as-code helpers); per-row scopes in the table · **Confidence:** high (surface 3 conditional on the fetch transport, Q-WA-6) · **Status:** confirmed

- **Rule (surfaces).** A shim or conformance test must enumerate five surfaces
  separately ([WA-V4](#wa-v4)):
  1. **TS named exports from `client.ts`** (the list the build appends):
     - Helpers: `setClient`, `getVariable`, `setVariable`, `getResource`,
       `setResource`, `getResumeUrls`, `setState`, `setProgress`,
       `getProgress`, `getState`, `getIdToken`.
     - S3: `denoS3LightClientSettings`, `loadS3FileStream`, `loadS3File`,
       `writeS3File`, `deleteS3File`, `signS3Objects`, `signS3Object`,
       `getPresignedS3PublicUrls`, `getPresignedS3PublicUrl`.
     - Workflow-as-code: `task`, `taskScript`, `taskFlow`, `workflow`, `step`,
       `sleep`, `parallel`, `waitForApproval`, `WorkflowCtx`, `_workflowCtx`,
       `setWorkflowCtx`, `StepSuspend`.
     - Dispatch: `runScript`, `runScriptAsync`, `runScriptByPath`,
       `runScriptByHash`, `runScriptByPathAsync`, `runScriptByHashAsync`,
       `runFlow`, `runFlowAsync`, `waitJob`, `getRootJobId`.
     - Other: `setFlowUserState`, `getFlowUserState`, `usernameToEmail`,
       `requestInteractiveSlackApproval`, `requestInteractiveTeamsApproval`,
       `appendToResultStream`, `streamResult`, `datatable`, `ducklake`,
       `upsertPartition`, `appendPartition`, `commitKafkaOffsets`.
     - **Type-only** (not runtime keys): `Base64`, `Sql`, `TaskOptions`,
       `S3Object`, `S3ObjectRecord`, `S3ObjectURI`, `SqlStatement`,
       `SqlTemplateFunction`, `DatatableSqlTemplateFunction`,
       `DucklakeMaterializeOptions`, `DenoS3LightClientSettings`.
  2. **The TS default-export object:** every non-type name in (1), plus:
     - extra helpers: `SHARED_FOLDER`, `getWorkspace`, `getStatePath`,
       `getInternalState`, `setInternalState`, `getResumeEndpoints`,
       `getResult`, `getResultMaybe`, `resolveDefaultResource`,
       `databaseUrlFromResource`, `base64ToUint8Array`, `uint8ArrayToBase64`,
       `parseS3Object`;
     - 14 services: `AdminService`, `AuditService`, `FlowService`,
       `GranularAclService`, `GroupService`, `JobService`, `ResourceService`,
       `VariableService`, `ScriptService`, `ScheduleService`, `SettingsService`,
       `UserService`, `WorkspaceService`, `TeamsService`.

     It has **no `OpenAPI` member** and none of the other generated services.
  3. **TS root exports of the generated module** (the generated `index.ts` the
     build appends to): `ApiError`, `OpenAPI`, `CancelablePromise`,
     `CancelError`, every generated service and the generated types, plus the
     type-only `OpenAPIConfig` ([WA-V8](#wa-v8)). This holds only for an
     artifact generated with the fetch transport: `build.sh` does not force it
     and the generator auto-detects ([Q-WA-6](#q-wa-6)). Under
     `import * as wmill`, names that exist only on the default object (such as
     `getResultMaybe` and `getWorkspace`) are `undefined` on the namespace,
     while `wmill.OpenAPI` and every service exist.
  4. **Python module-level names.** `wmill/__init__.py` star-imports `client`
     and `s3_types`, and neither defines `__all__`, so every public
     module-level name is exported:
     - Context and dispatch: `get_workspace`, `get_root_job_id`, `get_version`
       (deprecated), `run_script_async`, `run_flow_async`, `run_script_sync`
       (by hash), `run_script_by_path_async`, `run_script_by_hash_async`,
       `run_script_by_path_sync`, `run_script`, `run_script_by_path`,
       `run_script_by_hash`, `run_inline_script_preview`.
     - Jobs: `get_id_token`, `get_job_status`, `get_job`, `get_result`,
       `cancel_job`, `cancel_running`.
     - S3: `duckdb_connection_settings`, `polars_connection_settings`,
       `boto3_connection_settings`, `load_s3_file`, `load_s3_file_reader`,
       `write_s3_file`, `delete_s3_object`, `sign_s3_objects`,
       `sign_s3_object`, `get_presigned_s3_public_urls`,
       `get_presigned_s3_public_url`.
     - Data and state: `whoami`, `get_state`, `get_resource`, `set_resource`,
       `list_resources`, `set_state`, `set_progress`, `get_progress`,
       `set_shared_state_pickle`, `get_shared_state_pickle`,
       `set_shared_state`, `get_shared_state`, `get_variable`, `set_variable`,
       `get_flow_user_state`, `set_flow_user_state`, `get_state_path`,
       `get_resume_urls`.
     - Other: `request_interactive_slack_approval`, `send_teams_message`,
       `username_to_email`, `datatable`, `ducklake`, `parse_resource_syntax`,
       `parse_s3_object`, `parse_variable_syntax`, `append_to_result_stream`,
       `stream_result`, `task`, `task_script`, `task_flow`, `workflow`,
       `commit_kafka_offsets`, the decorators `init_global_client` and
       `deprecate`, `infer_sql_type`, `parse_sql_client_name`.
     - Workflow-as-code coroutine functions: `step(name, fn)`,
       `sleep(seconds)`, `wait_for_approval(…)`,
       `parallel(items, fn, *, concurrency=None)`.
     - Classes: `Windmill`, `DataTableClient`, `DucklakeClient`, `SqlQuery`,
       `TaskError`, `WorkflowCtx`, `S3Object`, `S3FsClientKwargs`, `S3FsArgs`,
       `StorageOptions`, `PolarsConnectionSettings`, `Boto3ConnectionSettings`,
       `DuckDbConnectionSettings`.
     - Values: `JobStatus` and `logger`.

     Modules that leak through the star import (`os`, `httpx`, `json`, …) are
     not API.
  5. **Python `Windmill` instance members with no same-named module function:**
     `wait_job`, `create_token`, `version` (property), `state` (property with a
     setter), `user` (property), `get_mocked_api`, `worker_has_internal_server`,
     `get_client`, `get`, `post`. The `get_*_connection_settings` methods are
     exposed at module level under the shorter names in (4).
- **Rule (wire behaviour).** Routes are relative to `OpenAPI.BASE`
  (`<base>/api`). The service column names the platform service of
  [WA-40](#wa-40).

  | TS | Python | HTTP | Service | Scope |
  |---|---|---|---|---|
  | `getResource(path?, undefinedIfEmpty?)` | `get_resource(path, none_if_undefined=False, interpolated=True)` | `GET /w/{ws}/resources/get_value_interpolated/{path}`; Python with `interpolated=False`: `GET /w/{ws}/resources/get_value/{path}` | resources | core |
  | `setResource(value, path?, initTypeIfNotExist?)` | `set_resource(path, value, resource_type="any")` | TS: `GET /w/{ws}/resources/exists/{path}`, then `POST /w/{ws}/resources/update_value/{path}` `{value}` or `POST /w/{ws}/resources/create` `{path,value,resource_type}`. Python: probes `GET /w/{ws}/resources/get/{path}`; 404 → create | resources | later |
  | — | `list_resources(resource_type?, page?, per_page?)` | `GET /w/{ws}/resources/list` with those query keys | resources | later |
  | `getVariable(path)` | `get_variable(path)` | `GET /w/{ws}/variables/get_value/{path}` | variables/secrets | core |
  | `setVariable(path, value, isSecretIfNotExist?, descriptionIfNotExist?)` | `set_variable(path, value, is_secret=False)` | TS: `GET /w/{ws}/variables/exists/{path}`, then `POST /w/{ws}/variables/update/{path}` `{value}` or `POST /w/{ws}/variables/create` `{path,value,is_secret,description}`. Python: probes `GET /w/{ws}/variables/get/{path}` | variables | later |
  | `getState(path?)`, `setState(state, path?)`; deprecated `getInternalState`, `setInternalState` | `get_state`, `set_state`, `Windmill.state` | the resource calls above at the state path, type `state` | state | later |
  | `getFlowUserState(key, errorIfNotPossible?)`, `setFlowUserState(key, value, errorIfNotPossible?)` | `get_flow_user_state`, `set_flow_user_state` | `GET /w/{ws}/jobs_u/get_root_job_id/{WM_JOB_ID}`, then `GET`/`POST /w/{ws}/jobs/flow/user_states/{root}/{key}` | flow-run KV | core |
  | `getRootJobId(jobId?)` | `get_root_job_id` | `GET /w/{ws}/jobs_u/get_root_job_id/{id}` | job tree | core |
  | `runScriptByPathAsync(path, args, scheduledInSeconds, tag)`, `runScriptByHashAsync`; deprecated `runScriptAsync` | `run_script_by_path_async`, `run_script_by_hash_async`, `run_script_async` | `POST /w/{ws}/jobs/run/p/{path}` or `POST /w/{ws}/jobs/run/h/{hash}`; query `parent_job`, `root_job`, `tag`, `scheduled_in_secs`; body = args JSON | dispatch | core (interface) |
  | `runScriptByPath(path, args, verbose, tag)`, `runScriptByHash`; deprecated `runScript` | `run_script_by_path`, `run_script_by_hash`, `run_script_sync` (by hash), `run_script` | the async variant, then `waitJob`/`wait_job` | dispatch | core (interface) |
  | — | `run_script_by_path_sync(path, …)` | the deprecated instance `run_script(path=…)`: async dispatch, then `wait_job` | dispatch | later |
  | `runFlow(path, args, verbose, tag)`, `runFlowAsync(path, args, scheduledInSeconds, doNotTrackInParent=true, tag)` | `run_flow_async` | `POST /w/{ws}/jobs/run/f/{path}` | dispatch | core (interface) |
  | `waitJob(id, verbose)`, `getResultMaybe(id)`, `getResult(id)` | `Windmill.wait_job` (instance only), `get_result` | poll `GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}`; `GET /w/{ws}/jobs_u/completed/get_result/{id}` | dispatch | core (interface) |
  | — | `get_job(id)`, `get_job_status(id)` | `GET /w/{ws}/jobs_u/get/{id}`. Status is `"COMPLETED"` if `type` lower-cased is `completedjob`, `"RUNNING"` if `running`, otherwise `"WAITING"` | job tree | later |
  | — | `cancel_job(id, reason?)`, `cancel_running()` | `POST /w/{ws}/jobs_u/queue/cancel/{id}` with `{reason}` (default reason `cancelled via cancel_job method`). `cancel_running` first does `GET /w/{ws}/jobs/list?running=true&script_path_exact=<WM_JOB_PATH>`, then cancels every match except `WM_JOB_ID` with reason ``killed by `cancel_running` method`` | dispatch | out |
  | — | `run_inline_script_preview(content, language, args?)` | `POST /w/{ws}/jobs/run_inline/preview` when the base is localhost/127.0.0.1, else `POST /w/{ws}/jobs/run_wait_result/preview`; body `{content, language, args}` | — | out |
  | `loadS3File`, `loadS3FileStream` | `load_s3_file`, `load_s3_file_reader` | `GET /w/{ws}/job_helpers/download_s3_file?file_key&s3_resource_path&storage` (raw fetch) | object store | core |
  | `writeS3File` | `write_s3_file` | `POST /w/{ws}/job_helpers/upload_s3_file?file_key&s3_resource_path&storage&content_type&content_disposition`, raw body | object store | core |
  | `deleteS3File` | `delete_s3_object` | `DELETE /w/{ws}/job_helpers/delete_s3_file?file_key&storage` (Python also `s3_resource_path`) | object store | later |
  | `denoS3LightClientSettings` | `boto3_connection_settings`, `duckdb_connection_settings`, `polars_connection_settings` | `POST /w/{ws}/job_helpers/v2/s3_resource_info`, `/w/{ws}/job_helpers/v2/duckdb_connection_settings`, `/w/{ws}/job_helpers/v2/polars_connection_settings` | object store (credentials) | later |
  | `signS3Objects`, `signS3Object`, `getPresignedS3PublicUrls`, `getPresignedS3PublicUrl` | `sign_s3_objects`, `sign_s3_object`, `get_presigned_s3_public_urls`, `get_presigned_s3_public_url` | `POST /w/{ws}/apps/sign_s3_objects` `{s3_objects:[…]}`; URLs built locally. Python `sign_s3_objects` parses each object, `sign_s3_object` sends its object unparsed | object store | later |
  | `getResumeUrls(approver?, flowLevel?)`; deprecated `getResumeEndpoints` | `get_resume_urls` | `GET /w/{ws}/jobs/resume_urls/{WM_JOB_ID}/{nonce}?approver&flow_level` | resume | core (call shape); out (resuming) |
  | `setProgress(percent, jobId?)`, `getProgress(jobId?)` | `set_progress`, `get_progress` | `POST /w/{ws}/job_metrics/set_progress/{id}` `{percent, flow_job_id}`; `GET /w/{ws}/job_metrics/get_progress/{id}`; TS `setProgress(…, jobId)` first does `GET /w/{ws}/jobs_u/get/{jobId}` | progress | later |
  | `appendToResultStream(text)`, `streamResult(asyncIterable)` | `append_to_result_stream`, `stream_result` | **no HTTP**: prints a `WM_STREAM: ` stdout line | result stream ([languages.md](languages.md)) | later |
  | `usernameToEmail(username)` | `username_to_email`, `whoami()`, `Windmill.user` | `GET /w/{ws}/users/username_to_email/{u}`; `GET /users/whoami` | identity | later |
  | — | `get_version()` (deprecated), `Windmill.version` | `GET /version` (text) | — | later |
  | — | `Windmill.create_token(duration=1 day)` | `POST /users/tokens/create` with `{label: "refresh <unix time>", expiration}`; `expiration` is the **local** wall-clock time of now + duration, formatted with a trailing `Z` | — | out |
  | `getIdToken(audience, expiresIn?)` | `get_id_token` | `POST /w/{ws}/oidc/token/{audience}?expires_in` | OIDC | EE |
  | `requestInteractiveSlackApproval`, `requestInteractiveTeamsApproval` | `request_interactive_slack_approval`, `send_teams_message` | `GET /w/{ws}/jobs/slack_approval/{id}`, `GET /w/{ws}/jobs/teams_approval/{id}`, `POST /teams/activities` | approval | out |
  | `datatable(name)`, `ducklake(name)`, `upsertPartition`, `appendPartition` | `datatable`, `ducklake` | `POST /w/{ws}/jobs/run_inline/preview` if `OpenAPI.BASE` is `http(s)://localhost` or `127.0.0.1`, else `POST /w/{ws}/jobs/run_wait_result/preview`; `POST /w/{ws}/assets/record_materialization` | SQL | out |
  | `commitKafkaOffsets` | `commit_kafka_offsets(trigger_path, topic, partition, offset)` | `POST /w/{ws}/kafka_triggers/commit_offsets/{path}` | triggers | out |
  | `workflow`, `task`, `taskScript`, `taskFlow`, `step`, `sleep`, `parallel`, `waitForApproval`, `WorkflowCtx`, `StepSuspend`, `setWorkflowCtx` | `workflow`, `task`, `task_script`, `task_flow`, `step`, `sleep`, `wait_for_approval`, `parallel` | workflow-as-code ([WA-33](#wa-33), [WA-32](#wa-32)) | — | out |
  | `setClient(token?, baseUrl?)` | — | none; sets `OpenAPI.BASE`, `TOKEN` and `WITH_CREDENTIALS` ([WA-1](#wa-1)) | — | core |
  | `getWorkspace`, `getStatePath`, `resolveDefaultResource`, `databaseUrlFromResource`, `base64ToUint8Array`, `uint8ArrayToBase64`, `parseS3Object`, `SHARED_FOLDER = "/shared"` | `get_workspace`, `get_state_path`, `parse_resource_syntax`, `parse_variable_syntax`, `parse_s3_object`, `set_shared_state[_pickle]`, `get_shared_state[_pickle]` (files under `/shared/`) | local only (`databaseUrlFromResource` calls `getResource`) | — | core (pure helpers) |
- **Evidence:** `typescript-client/build.sh:7`, `:41-43`, `:47-223`;
  `typescript-client/client.ts:103-2038`; `typescript-client/sqlUtils.ts:162-220`,
  `:487-566`; `python-client/wmill/wmill/__init__.py:1-2`;
  `python-client/wmill/wmill/client.py:34-1402` (instance methods),
  `:1406-1432`, `:1436-2262`, `:2265-2603`, `:2617-3220`, `:3044`, `:3060`,
  `:3072`, `:3101`; `python-client/wmill/wmill/s3_types.py:1-74`; operation
  paths in `backend/windmill-api/openapi.yaml` (e.g.
  `getResourceValueInterpolated` `:7494`, `getVariableValue` `:6095`,
  `getRootJobId` `:13927`, `getCompletedJobResultMaybe` `:14293`,
  `setFlowUserState`/`getFlowUserState` `:15013`, `getResumeUrls` `:14662`,
  `fileUpload` `:21401`, `deleteS3File` `:21348`, `s3ResourceInfo` `:20848`,
  `signS3Objects` `:12044`, `setJobProgress` `:21637`, `getOidcToken` `:5903`,
  `usernameToEmail` `:5717`); generator `@hey-api/openapi-ts@0.43.0` ([WA-V8](#wa-v8)).
- **Conformance:** [WA-8/a, WA-8/b, WA-8/c, WA-8/d](conformance.md#wa-8)
- **Note:** which client versions real sync folders pin, and how their surfaces
  differ, is [Q-WA-8](#q-wa-8).

<a id="wa-9"></a>
### WA-9 · Scripts use the generated services and `OpenAPI` directly

**Scope:** core (import forms; a coherent `OpenAPI.BASE`/`TOKEN`); later (job introspection fidelity, WA-19) · **Confidence:** high (surface; the claim about how scripts use it is a usage observation, not a source fact) · **Status:** confirmed

- **Rule:**
  - Scripts can call the generated `*Service` classes directly, for example
    `JobService.getJob`, `JobService.runFlowByPath`, `JobService.runFlowPreview`,
    `JobService.listQueue`, `JobService.cancelQueuedJob`,
    `ResourceService.getResource`, `ResourceService.listResource`,
    `VariableService.getVariableValue` and `UserService.whoami`. They can also
    read `wmill.OpenAPI.BASE`, `wmill.OpenAPI.TOKEN` and `wmill.OpenAPI.HEADERS`
    to make raw `fetch` calls. Any of the roughly 600 operations in
    `openapi.yaml` is reachable from a step.
  - The hand-written helpers themselves import the services and make raw
    `fetch` calls with `OpenAPI.BASE`/`OpenAPI.TOKEN`.
  - **Import form.** `OpenAPI` is not on the default-export object.
    `wmill.OpenAPI` works only through a namespace import
    (`import * as wmill`) or a named `{ OpenAPI }` import from the generated
    root ([WA-V8](#wa-v8)); it never works through
    `import wmill from "windmill-client"`.
- **Evidence:** `typescript-client/build.sh:105-136` (services in the default
  export), `:137-223`; `typescript-client/client.ts:1-13` (services imported by
  helpers), `:432-442`, `:936-944` (raw `fetch` with `OpenAPI.BASE`/`TOKEN`);
  prior art: windmill-ts generated code also uses `wmill.OpenAPI.BASE`/`TOKEN`
  for raw fetches (`src/generator/scripts.ts:45-53` @ windmill-ts
  `af2b1e325320`).
- **Conformance:** [WA-9/a, WA-9/b](conformance.md#wa-9)
- **Design note (not Windmill behaviour):** a shim that covers only the
  hand-written helpers is not faithful. windmill-lite needs either (a) an
  HTTP-level facade that answers the endpoints it supports and fails the rest
  loudly, or (b) a shim that re-exports the full generated surface; (a) is
  cheaper and language-neutral ([WA-30](#wa-30)). For [WA-9/a](conformance.md#wa-9),
  windmill-lite must return the same value or fail with the unsupported-call
  error ([WA-41](#wa-41)), never hang or return garbage.

<a id="wa-v4"></a>
### WA-V4 · TS default and named export surfaces are not interchangeable

**Scope:** core (shim and export compatibility) · **Confidence:** high (explicit build export lists) · **Status:** confirmed

- **Rule:** the build appends an explicit named helper-export list and
  separately constructs a default object. Helpers such as `getWorkspace`,
  `getStatePath`, `getInternalState`, `getResult`, `getResultMaybe` and
  `parseS3Object` are on the default object but absent from the named list. The
  default object has the 14 enumerated service classes and no `OpenAPI`
  property. A shim must preserve the import forms the published package
  actually exposes; root named exports cannot be inferred from the `export`
  declarations in `client.ts` alone, and generated root exports must be checked
  separately from the hand-written default object.
- **Evidence:** `typescript-client/build.sh:43`, `:105-136`, `:137-225`;
  `typescript-client/package.json:20-34`.
- **Conformance:** [WA-V4/a](conformance.md#wa-v4)
- **Note:** the full inventory is in [WA-8](#wa-8).

<a id="wa-v8"></a>
### WA-V8 · Generated core exports, error objects and transport selection

**Scope:** core (generated-client compatibility; the artifact's transport is a packaging gate) · **Confidence:** high (generator behaviour; the transport of a given artifact depends on its generation configuration) · **Status:** confirmed

- **Rule:** for `@hey-api/openapi-ts@0.43.0`, the generator pinned by the build:
  - **Root exports.** With core output enabled, the generated root exports
    `ApiError`, `OpenAPI`, the type-only `OpenAPIConfig`, and, for a
    non-Angular client, `CancelablePromise` and `CancelError`; it also
    re-exports the generated services and types. The ordinary defaults enable
    core, services and types.
  - **Fetch transport.** Requests call the runtime's global `fetch`. A response
    body is read as text when the response `Content-Type` contains `text/`.
  - **Error object.** A generated HTTP error has `name = "ApiError"`, plus
    `url`, numeric `status`, `statusText`, `body`, `request`, and the normal
    `Error` message and stack. Known statuses use a default message table,
    which the operation's error map can override; for 501 the default message
    is `Not Implemented`, independent of the response body. `statusText` is the
    response's own status text.
  - **Transport choice is conditional.** Without an explicit client option the
    generator auto-detects client dependencies (including global npm
    dependencies) and falls back to fetch. The TS package declares no
    alternative client dependency, but `build.sh` does not force
    `--client fetch`, so an arbitrary generation environment is not proof of
    fetch output ([Q-WA-6](#q-wa-6)).
- **Evidence:** `typescript-client/build.sh:7`, `typescript-client/package.json`;
  the exact [generator 0.43.0 archive](https://registry.npmjs.org/@hey-api/openapi-ts/-/openapi-ts-0.43.0.tgz)
  (SHA-1 `8dfaf95866fcd115ae26a64f86d78d65ba1b4bbd`, matching registry
  metadata), `package/dist/node/index.cjs`: configuration normalisation,
  root-index construction, the `ApiError`, fetch request/response and
  error-code templates. This is generator-source inspection, not an execution
  of the Windmill build or an inspection of the published client artifact.
- **Conformance:** [WA-V8/a, WA-V8/b](conformance.md#wa-v8)

## D. Semantics per area

<a id="wa-10"></a>
### WA-10 · `getResource` / `get_resource`: path syntax, mocked API, error wrapping

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **TS path handling:** the path is taken from `$res:<p>` (prefix stripped),
    else `res://<p>` (prefix stripped), else used as given; if absent, the state
    path is used.
  - **TS mocked API:** if a mocked API is active ([WA-17](#wa-17)) and
    `resources[path]` is **truthy**, that value is returned. Otherwise it logs
    `MockedAPI present, but resource not found at <path>, falling back to real API`
    and calls the API.
  - **TS API call:** `get_value_interpolated` is called **without `job_id` and
    without `allow_cache`**. On any error: if `undefinedIfEmpty` is set and the
    HTTP status is 404, it returns `undefined`. Otherwise it throws
    `Error("Resource not found at <path> or not visible to you: <e.body>")`,
    even for non-404 errors.
  - **Python:** the same prefixes; mocked lookup by key presence (falsy values
    are returned); `interpolated=True` by default. On **any** exception,
    `none_if_undefined=True` yields `None`; otherwise the error is logged and
    re-raised as in [WA-3](#wa-3). A mock miss with `none_if_undefined=True`
    returns `None` without HTTP ([WA-17](#wa-17)).
  - **Neither client** strips `$var:` from variable paths; only `var://` is
    recognised ([WA-11](#wa-11)).
- **Evidence:** `typescript-client/client.ts:103-135`, `:1505-1512`;
  `python-client/wmill/wmill/client.py:628-673`, `:2204-2211`.
- **Conformance:** [WA-10/a](conformance.md#wa-10)

<a id="wa-11"></a>
### WA-11 · Variables: `getVariable` / `setVariable`

**Scope:** core (read); later (write, masking, audit) · **Confidence:** high; high (source-traced: secrets that cannot be decrypted) · **Status:** confirmed; drafted (secrets that cannot be decrypted, the `already_encrypted` check)

- **Rule:**
  - **Reads.**
    - The path accepts the `var://` prefix (stripped). `$var:` is not
      stripped, so it is requested literally and gets a 404.
    - A successful read returns the (decrypted) string value.
    - A missing variable gives 404 with body `Not found: Variable <p> not found`.
    - A variable that exists but is not visible gives 401 with a body starting
      `Not authorized: Variable exists but you don't have access to it:`.
    - TS rewraps every error as
      `Error("Variable not found at <p> or not visible to you: <body>")`.
    - **A secret whose stored value cannot be decrypted** (drafted). On CE
      the only secret backend is the database: a read of a non-empty secret
      decrypts the stored base64 ciphertext with the workspace key (and the
      instance's `SECRET_SALT`, when set). A failure fails the read, on
      `get_value` and on `get` with decryption alike, with HTTP 500 and the
      body
      `Internal: Error decrypting variable <p>: Internal: <cause> @<loc> @<loc>`
      (`@variables.rs:<line>:<col> @resolver.rs:262:17` at the pin). `<cause>`
      is one of the texts of [JE-42](expressions.md#je-42)'s `$encrypted:`:
      normally `Could not decrypt value. The value may have been encrypted with a different key.`
      (ciphertext under another key, or a length that is not a whole number
      of 16-byte blocks), the base64 decoder's message for a value that is
      not base64, or rarely a UTF-8 message when another key happens to leave
      valid padding. More rarely still, ciphertext under another key decrypts
      to valid padding **and** valid UTF-8, and the read then succeeds with
      that text: nothing else checks the plaintext (magic-crypt 3.1.13
      `src/traits.rs:75-90` decrypts, then converts to UTF-8). So no stored
      ciphertext fails under every key; the shape check admits only whole
      16-byte blocks. The audit event is written first. An empty stored value
      is not decrypted and reads as `""`. Every reader sees this body:
      the clients rewrap it as above, the expression helper `variable()`
      throws it ([JE-20](expressions.md#je-20)), and a leaf step's `$var:`
      fails with ``Not found: Variable <p> not found for `<name>`: <body>``
      ([JE-42](expressions.md#je-42)).
  - **Side effects of reading a secret.**
    - An audit event `variables.decrypt_secret` is written.
    - The plaintext is registered for log masking along the store's local code
      path. That store code can run in components other than the API process;
      whether the masks reach the logs of separate worker processes is not
      established ([Q-WA-9](#q-wa-9)).
    - An expired OAuth-linked secret is refreshed first, **but only if the
      backend is built with the `oauth2` feature**. Without it the read fails
      with an internal error whose message is `Require oauth2 feature`.
  - **Writes.**
    - TS `setVariable` creates a missing variable with
      `is_secret = isSecretIfNotExist ?? false` and
      `description = descriptionIfNotExist ?? ""`. For an existing variable it
      updates only `value`; secrecy is not changed.
    - Python `set_variable(path, value, is_secret=False)` probes
      `GET /w/{ws}/variables/get/{path}`. A 404 leads to a create with
      `description: ""`; any other answer leads to an update of the value.
    - **How undecryptable ciphertext gets stored** (drafted). A create or
      update of a secret with the query flag `already_encrypted=true` stores
      the value verbatim, which is how the CLI pushes the secrets of a
      pulled folder unless it is told they are plain
      ([SY-11](openflow.md#sy-11)). The server checks only the shape: the
      value must be standard base64 decoding to a non-zero multiple of 16
      bytes (an external backend's `$vault:`, `$aws_sm:` or `$azure_kv:`
      marker is also let through). Ciphertext pulled from a workspace with
      another key passes, and every later read fails as above. A value of
      any other shape is refused with HTTP 400
      `Bad request: Variable <p> was sent as already-encrypted (already_encrypted=true) but its value is not valid workspace-encrypted ciphertext. To push a plaintext secret, send it without already_encrypted (CLI: use --plain-secrets) so it gets encrypted.`
- **Evidence:** `typescript-client/client.ts:743-802`;
  `python-client/wmill/wmill/client.py:573-626`, `:2241-2245`;
  `backend/windmill-store/src/variables.rs:447-499` (permission explanation),
  `:1458-1550` (oauth2 gate `:1510-1531`; masking registration `:1476-1483`,
  `:1541-1543`); `backend/windmill-common/src/error.rs:24-43`, `:250-282`.
  Secrets that cannot be decrypted: `backend/windmill-store/src/variables.rs:1505-1533`
  (`get_value`: audit, then decryption of a non-empty value), `:377-418` (`get`);
  `backend/windmill-common/src/secret_backend/resolver.rs:79-86` (CE has only
  the database backend), `:249-276` (the decryption and its outer text);
  `backend/windmill-common/src/variables.rs:115-148` (the workspace key and
  salt), `:263-271` (the inner texts). The shape check:
  `backend/windmill-store/src/variables.rs:539-566`, `:616-622` (create),
  `:1119-1124` (update); `cli/src/commands/variable/variable.ts:127-198`
  (`alreadyEncrypted` unless plain secrets).
- **Conformance:** [WA-11/a, WA-11/b, WA-11/c, WA-11/d](conformance.md#wa-11)
- **Revision 2026-10-29 (SC3, source re-check, chain PF2):** milestone PF2
  fails ciphertext and `$encrypted:` with its own text. Added Windmill's
  texts for a secret that cannot be decrypted and the server's shape check
  on already-encrypted values, with new cases WA-11/c and WA-11/d (not yet
  recorded, [Q-WA-13](#q-wa-13)).
- **Revision 2026-10-29 (SC3 validation round 1, finding 6):** the rule
  and WA-11/c allowed only a padding or a UTF-8 failure for ciphertext
  under another key. A key that leaves valid padding and valid UTF-8 makes
  the read succeed, so WA-11/c is now an observation with three allowed
  outcomes, and its recording selects a ciphertext that fails.

<a id="wa-12"></a>
### WA-12 · Resource values are interpolated on the server

**Scope:** core · **Confidence:** high (traced) · **Status:** confirmed; drafted (an unusable `job_id`, `CUSTOM_INSTANCE_DB/` paths, the `$jsonvar:` body)

- **Rule:** `get_value_interpolated` loads the stored value (a NULL value gives
  JSON `null`) and transforms it recursively.
  - **Strings.**
    - `$var:<p>` becomes that variable's value as a string ([WA-11](#wa-11)
      rules; a missing variable fails the whole call with its 404).
    - `$jsonvar:<p>` becomes the variable's value **parsed as JSON**. Invalid
      JSON gives an internal error `Failed to parse $jsonvar value as JSON: …`.
      Its body is `Internal: Failed to parse $jsonvar value as JSON: <serde message> @<loc>`
      (HTTP 500; `@resources.rs:827:17` at the pin), where the serde message
      ends `at line L column C`, counted in bytes as
      [JE-42](expressions.md#je-42) says (drafted).
    - `$res:<p>` becomes that resource's value, interpolated again. Splitting
      `<p>` on `/` must give at least 2 components (the path must contain at
      least one `/`); otherwise the call fails with an internal error whose
      message is `Invalid resource path: <p>`. A missing nested resource gives
      the located 404 form ([WA-42](#wa-42)).
    - **Terminal substitutions:** the value substituted for `$var:`, and the
      parsed value substituted for `$jsonvar:`, are never interpolated again.
    - `$WM_*` strings (e.g. `$WM_TOKEN`, `$WM_JOB_ID`) are resolved from the
      reserved variables of the job given in the `job_id` query parameter,
      using that job's **stored** fields ([WA-V3](#wa-v3)), **only when
      `job_id` is given**; otherwise they are left unchanged. An unknown
      `$WM_X` is left unchanged. Any other `$…` string (custom envs, `$5.00`)
      is left unchanged.
    - **An unusable `job_id`** (drafted). The job is looked up only when the
      walk meets a `$WM_*` string, at each such string. A value without one
      is returned as if no `job_id` had been sent, whatever it names. When
      it is looked up, the job must belong to this workspace **and still be
      queued or running**: the lookup joins the queue table, whose row is
      deleted when the job completes. An unknown id, a job of another
      workspace and a completed job all fail the whole read with HTTP 404
      `Not found: Job not found at name <job_id> (resources.rs:899)`. A
      `job_id` that is not a UUID never reaches the handler: the web
      framework refuses the query string with HTTP 400 ([WA-42](#wa-42)).
      Windmill's own callers send only the running leaf job's id
      ([WA-V3](#wa-v3)); `getResource` sends none. The `$WM_TOKEN` it
      resolves is the caller's own token.
  - **Depth accounting.**
    - The top-level value is transformed at depth 0.
    - Each value inside an object or array is transformed at its container's
      depth + 1.
    - The value loaded for a `$res:` reference is transformed at the referring
      string's depth + 1.
    - Before inspecting any value, scalars included, the walker fails if
      `depth >= 50`; the deepest allowed node is at depth 49. The error is
      `` Maximum resource/variable interpolation depth (50) exceeded; this usually indicates a circular `$res:` or `$var:` reference ``.
    - Objects consume depth exactly as arrays do.
  - **Arrays.** An array at depth ≤ 2 with at most 1000 items has its items
    transformed. Any other array is returned unchanged and its items are never
    visited, so nothing inside it can trip the depth check; the array itself is
    still subject to that check.
  - **Caching.** Values without `$WM_*` may be cached per caller identity when
    `allow_cache=true`. The clients never set it.
  - **Error bodies.** Depth and invalid-path failures are HTTP 500 with body
    `Internal: <message> @<file>:<line>:<column>`; compare by prefix.
  - **`CUSTOM_INSTANCE_DB/<db>`** (drafted). A path of this route that
    starts with `CUSTOM_INSTANCE_DB/` is not looked up as a resource. The
    caller must be an instance super admin, else HTTP 401
    `Not authorized: This endpoint requires the caller to be a super admin`.
    A super admin gets the connection settings of Windmill's own database,
    parsed from its database URL, with `dbname` replaced by `<db>`: an
    object with the members `host`, `user`, `password`, `port`, `sslmode`,
    `dbname`, `root_certificate_pem`, `accept_invalid_certs`,
    `use_iam_auth` and `region` in that order, the last four `null` (`user`,
    `password`, `port` and `sslmode` are `null` when the URL has none).
    Nothing is interpolated, cached or checked against `job_id`. This is CE
    behaviour, with no feature gate. Only this route has the special case:
    the raw read (`get_value`) and a nested `$res:CUSTOM_INSTANCE_DB/<db>`
    inside a value look the path up as an ordinary resource and find none.
    So it is reached by `getResource`, by the expression helper `resource()`
    ([JE-20](expressions.md#je-20)) and by a leaf step's top-level `$res:`
    argument ([JE-42](expressions.md#je-42)), each as the job's user.
  - Because the TS and Python clients never pass `job_id`, **`getResource`
    returns `$WM_*` placeholders literally**. The worker's own argument
    resolution (static `$res:` transforms, [expressions.md](expressions.md))
    does pass `job_id` ([WA-V3](#wa-v3)).
- **Evidence:** `backend/windmill-store/src/resources.rs:653-760`, `:768`,
  `:797-861` (guard `:806-810`, `$var:`/`$jsonvar:` `:812-829`, `$res:`
  `:830-861`), `:944-985` (arrays and objects);
  `backend/windmill-common/src/error.rs:59-62`, `:234-239` (located internal
  error); worker side `backend/windmill-worker/src/common.rs:164`, `:174-210`,
  `:262-396` (passes `job.id` at `:311-314`). An unusable `job_id`:
  `backend/windmill-store/src/resources.rs:653-657` (an optional UUID in
  the query), `:870-899` (the lookup only for a `$WM_*` string, joined with
  the queue, then the located not-found); `backend/windmill-common/src/utils.rs:338-352`;
  `backend/windmill-queue/src/jobs.rs:1244`, `:4779-4792` (completion
  deletes the queue row). `CUSTOM_INSTANCE_DB/`:
  `backend/windmill-store/src/resources.rs:692-700` (before any lookup),
  `:564-591` (the raw read has no such case), `:830-846` (nor the nested
  `$res:`); `backend/windmill-api-auth/src/lib.rs:196-206`;
  `backend/windmill-common/src/lib.rs:724-741`, `:1026-1068` (the members,
  all serialised). The `$jsonvar:` body: `backend/windmill-store/src/resources.rs:826-828`.
- **Conformance:** [WA-12/a, WA-12/b, WA-12/c, WA-12/d, WA-12/e, WA-12/f, WA-12/g, WA-12/h](conformance.md#wa-12)
- **Revision 2026-10-29 (SC3, source re-check, chain PF2):** milestone PF2
  answers 501 for a `job_id` naming an unknown job and reads
  `CUSTOM_INSTANCE_DB/` paths as plain resources, the reference being
  silent on both. Added what Windmill does (a 404 only when a `$WM_*`
  string is met, and the super-admin-only database settings) and the
  `$jsonvar:` body, with new cases WA-12/g and WA-12/h (not yet recorded,
  [Q-WA-13](#q-wa-13)). Also corrected the error bodies' location from
  `@<file>:<line>` to `@<file>:<line>:<column>`, as every location suffix
  is written ([FX-50](execution.md#fx-50)).
- **Design note (not Windmill behaviour):** windmill-lite has to own this
  interpolation (in Go); a shared engine must keep the contexts of
  [WA-V3](#wa-v3) and [WA-40](#wa-40) apart.

<a id="wa-v3"></a>
### WA-V3 · Shared interpolation needs separate resource and argument contexts

**Scope:** core · **Confidence:** high (both callers and transformations traced) · **Status:** confirmed

- **Rule:**
  - Client resource reads start API interpolation at depth 0 with no job id.
  - Worker `$res:` argument resolution calls the same API with a job id,
    starting a **new** API depth counter; it does not continue the worker's own
    counter.
  - API `$WM_*` interpolation passes the job's **stored** `root_job` into
    reserved-variable construction (empty string when absent), whereas process
    env construction computes the effective root ([WA-V1](#wa-v1)). The same
    contextual name can therefore differ between resource interpolation and the
    process env.
  - Worker argument interpolation additionally handles `$encrypted:` and any
    string `$<name>` that matches a reserved or custom env variable, using the
    worker's computed values. It runs for a top-level argument only when that
    argument contains a `$var:`/`$jsonvar:`/`$res:`/`$encrypted:` marker. API
    resource interpolation recognises only its listed resource and variable
    prefixes and `$WM_*`.
  - `$var:` output and parsed `$jsonvar:` output are terminal in both walkers,
    even when the replacement contains another interpolation-looking string.
- **Evidence:** `backend/windmill-store/src/resources.rs:736-744`, `:816-834`,
  `:878-935` (stored `root_job` at `:913-935`);
  `backend/windmill-worker/src/common.rs:164-210` (top-level marker gate),
  `:262-396`, `:539-555`; `backend/windmill-common/src/variables.rs:412-422`;
  root normalisation `backend/windmill-queue/src/jobs.rs:6373-6382`.
- **Conformance:** [WA-V3/a, WA-V3/b](conformance.md#wa-v3)
- **Note:** the worker side is specified in [expressions.md](expressions.md).

<a id="wa-13"></a>
### WA-13 · State (`getState`/`setState`) is a resource of type `state` at the state path

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **TS `getState(path?)`** is `getResource(path ?? statePath, undefinedIfEmpty=true)`:
    it returns `undefined` when state was never written (404 only); other
    errors throw.
  - **TS `setState(v, path?)`** is `setResource(v, path ?? statePath, "state")`,
    which checks existence, then updates the value or creates a resource with
    `resource_type: "state"`.
  - **Python `get_state`** returns `None` on any error from the resource read
    itself. **Python `set_state`** probes with `GET /w/{ws}/resources/get/{path}`;
    a 404 leads to a create with type `state`, anything else to an update.
  - **No state path.** When neither `WM_STATE_PATH_NEW` nor `WM_STATE_PATH` is
    set, Python `get_state()` and `set_state()` raise
    `Exception("State path not found")` before any resource call; TS throws
    `Error("State path not set")`.
  - **Interpolation on read.** State is read through the **interpolating**
    endpoint, so a state value containing `"$var:…"`/`"$res:…"` strings is
    resolved on read. This is upstream behaviour; an opt-out would be a
    deliberate compatibility deviation.
  - **TS `setResource` without a type** on a missing path throws
    `Resource at path <p> does not exist and no type was provided to initialize it`.
    Python's module-level `set_resource` defaults the type to `"any"` and
    creates the resource.
- **Evidence:** `typescript-client/client.ts:558-564`, `:572-618`, `:726-736`;
  `python-client/wmill/wmill/client.py:675-712`, `:743-761`, `:1188-1199`,
  `:1882-1887`.
- **Conformance:** [WA-13/a, WA-13/b](conformance.md#wa-13)
- **Note:** answers [Q-WA-7](#q-wa-7).

<a id="wa-14"></a>
### WA-14 · Flow user state: a key/value map on the root flow job, live while it runs

**Scope:** core (TS); later (Python; access after the run) · **Confidence:** high (handlers traced); high for access after the run and for the request rejections (source-traced) · **Status:** confirmed; drafted (access after the run, request rejections)

- **Rule:**
  - **Keying.** Both clients first resolve `root = getRootJobId(WM_JOB_ID)`,
    the server's effective-root rule (first non-null of `root_job`,
    `flow_innermost_root_job`, `parent_job`, own id; [WA-V1](#wa-v1)), then
    call the user-state endpoint on `root`.
  - **GET** reads `user_states.<key>` from the root's flow status (or
    workflow-as-code status). It returns JSON `null` if the key is absent, if
    the root has no flow status, or if the root job is **no longer in the
    queue** (completed).
  - **POST** with any JSON body (TS turns `undefined` into `null`) sets
    `user_states.<key>` only on jobs of kind `flow`, `flowpreview`, `flownode`
    or `singlestepflow`; otherwise it returns 404
    `Not found: Flow job not found`. On success the response text is
    `Flow job state updated`. The POST needs the root's flow-status row, which
    exists only while the root job is queued or running: completing a job
    deletes that row together with the queue row, so a write to a completed
    root fails with the same 404 (**After the run**, below).
  - **TS errors** (including a failed root lookup) are swallowed unless
    `errorIfNotPossible` is set; they are logged with
    `console.error("Error setting flow user state at <key>: <body>")` (**the
    same "setting" wording for the getter**), and the getter resolves
    `undefined`. With `errorIfNotPossible` it throws an `Error` with that
    message.
  - **Python.** `set_flow_user_state` does not check the response: it prints
    `Job <root> does not exist or is not a flow` on 404 and **silently ignores
    every other HTTP failure**. `get_flow_user_state` returns `None` on 404 and
    otherwise parses the body as JSON, so a non-JSON error body raises a decode
    error. A failed Python root lookup raises.
  - **Sharing.** Values set by one step are visible to later steps, to
    parallel branches and iterations, and to sub-flow steps whose root resolves
    to the same job (including steps inside a called sub-flow, [WA-V1](#wa-v1)).
  - **After the run.** Upstream does not stop a script from addressing a
    finished run's flow state: generated services can pass any job id. From
    the moment the root's completion is committed, GET returns `null` and
    POST returns 404 `Not found: Flow job not found`; there is no grace
    period. The map then survives only inside the completed job's stored
    `flow_status`, which neither endpoint reads.
  - **Request rejections** (raw HTTP; the stock clients always send a UUID
    from the root lookup and a JSON body). Each comes from the web framework
    with `Content-Type: text/plain; charset=utf-8` ([WA-42](#wa-42)):
    - an id that is not a UUID, on GET or POST: 400
      ``Invalid URL: Cannot parse `job_id` with value `<id>`: UUID parsing failed: <detail>``
      (for `abc`, `<detail>` is `invalid length: found 3`);
    - a POST body that is not JSON, sent with a JSON content type: 400
      `Failed to parse the request body as JSON: <parser message>` (for the
      body `hello`, `expected value at line 1 column 1`); with no JSON content
      type at all: 415 ``Expected request with `Content-Type: application/json` ``.

    The id is checked before the body, and both after authentication: a
    token of another workspace gets 401 first ([WA-4](#wa-4)).
- **Evidence:** `typescript-client/client.ts:142-149`, `:672-720`;
  `python-client/wmill/wmill/client.py:505-515`, `:807-829`;
  `backend/windmill-api/src/jobs.rs:336-340`, `:489-507`, `:4214-4262`;
  `backend/windmill-worker/src/common.rs:1834-1839`. After the run:
  `backend/migrations/20250201124345_v2_job_status.up.sql:2-3` (the status
  row's key references the queue row, with a cascading delete),
  `backend/migrations/20250201124409_v2_job_queue_compatiblity_view.up.sql:2`
  (the queue table renamed, the constraint kept);
  `backend/windmill-queue/src/jobs.rs:1092-1117` (the completed row copies
  `flow_status`), `:1244`, `:4779-4792` (the queue row deleted in the same
  transaction). Request rejections: `backend/windmill-api/src/jobs.rs:4214-4218`,
  `:4236-4241` (path, then body extractors); the pinned axum 0.8.9
  (`src/extract/path/mod.rs:357-401`, `:420-451`, `src/extract/path/de.rs:375-391`,
  `src/json.rs:106-113`, `:172-202`, `src/extract/rejection.rs:10-40`),
  serde_path_to_error 0.1.20 (`src/lib.rs:119-126`) and uuid 1.24.0
  (`src/external/serde_support.rs:133-135`, `src/error.rs:61-206`), each
  checked against `backend/Cargo.lock`.
- **Conformance:** [WA-14/a, WA-14/b, WA-14/c, WA-14/d](conformance.md#wa-14)
- **Design note (not Windmill behaviour):** discarding the map when the run
  ends is a windmill-lite lifetime policy. It is faithful for every access made
  through the stock helpers during the run, and for an explicit-id write after
  it, which Windmill also answers with 404; an explicit-id GET after the run
  gets `null` from Windmill.
- **Revision 2026-10-29 (SC3, source re-check, chain PF3):** answers
  [Q-WA-10](#q-wa-10). The first edition allowed that a POST to a completed
  root might still succeed while its status row lived on. It cannot: the
  status table's key references the queue row with a cascading delete (the
  constraint was created while the table was still called `queue`, and the
  rename kept it), and completion copies `flow_status` into the completed row
  and deletes the queue row in one transaction. So a write after the run gets
  404 `Not found: Flow job not found` at once, as windmill-lite's
  discard-at-end policy already answers, and a read gets `null`. Also added:
  Windmill's status and text for an id that is not a UUID and a body that is
  not JSON, where windmill-lite answers 400 with texts of its own (the status
  agrees; the texts are the framework's, above).

<a id="wa-15"></a>
### WA-15 · Dispatch: `run*Async`, `run*`, `waitJob`

**Scope:** core (interface); later (local execution vs delegation, WA-50; Python result helpers) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **TS script async.** Args are POSTed as JSON to `/w/{ws}/jobs/run/p/{path}`
    (or `/w/{ws}/jobs/run/h/{hash}`). The query gets `parent_job=<WM_JOB_ID>`
    whenever `WM_JOB_ID` is defined, `root_job=<WM_ROOT_FLOW_JOB_ID>` when it is
    non-empty, and `tag` and `scheduled_in_secs` when they are truthy. The
    **response text** is returned as the job id **without checking the HTTP
    status**, so on an error the body text is returned as if it were an id
    ([WA-V5](#wa-v5)).
  - **`runFlowAsync`** adds parent and root **only if
    `doNotTrackInParent === false`** (the default is `true`).
  - **Synchronous `run*`** calls the async variant, then `waitJob`. TS
    `runFlow` calls `runFlowAsync(path, args, null, false, tag)`, so it **does**
    send `parent_job`/`root_job`; the synchronous `runScriptByPath`/
    `runScriptByHash` also send them whenever the env vars are set.
  - **`waitJob(id)`** polls `get_result_maybe` every 500 ms with no timeout. On
    `completed && success` it returns `result`. On `completed && !success` it
    throws an `Error` whose message is `Job <id> was not successful: ` followed
    by the JSON serialisation of `result.error`. Polling responses are in
    [WA-V2](#wa-v2); on an unknown but valid UUID, stock TS `waitJob` polls
    forever.
  - **Python** makes the same requests.
    - `wait_job` (an instance method) adds an optional `timeout`: on expiry it
      POSTs a cancel with reason `reached timeout` and raises
      `TimeoutError("reached timeout")`. By default it registers an `atexit`
      cancel with reason `parent script cancelled`.
    - Failures raise `Exception("Job <id> was not successful: <str(error)>")`:
      a **Python dict repr, not JSON**.
    - **Null results.** These **module-level** functions default
      `assert_result_is_not_none=True`: `run_script`, `run_script_by_path`,
      `run_script_by_hash`, `run_script_sync`, `run_script_by_path_sync`. These
      **instance** methods default it to `False`: `Windmill.run_script`,
      `run_script_by_path`, `run_script_by_hash`, `wait_job`. With `True`, a
      successful `null` result raises `Exception("Result was none")`.
    - **`get_result`** (module and instance) defaults
      `assert_result_is_not_none=True` but tests the response **text** for
      `None`; the text is a string for any ordinary HTTP response, so this
      assertion never fires and a JSON `null` result is returned as `None`. HTTP
      and network errors still raise through the client's `get` wrapping
      ([WA-3](#wa-3)).
  - **Deprecated** `runScript`/`runScriptAsync` log a deprecation warning and
    reject passing both path and hash with `path and hash_ are mutually exclusive`.
- **Evidence:** `typescript-client/client.ts:154-330` (`:170-192`, `:254-268`),
  `:377-539`; `python-client/wmill/wmill/client.py:105-125`, `:165-438`
  (`:257-339`, `:363-438`), `:550-571`, `:1465-1645` (`:1527-1560`,
  `:1614-1645`), `:1683-1697`, `:2088-2155`;
  `backend/windmill-api/src/jobs.rs:9197-9300`, `:9305-9328`.
- **Conformance:** [WA-15/a, WA-15/b, WA-15/c, WA-15/d, WA-15/e](conformance.md#wa-15)

<a id="wa-v2"></a>
### WA-V2 · Unfinished and unknown-job polling responses carry explicit nulls

**Scope:** core (dispatch interface) · **Confidence:** high (response struct and all response branches read); high (ids that are not UUIDs, anonymous reads; source-traced) · **Status:** confirmed; drafted (ids that are not UUIDs, anonymous reads)

- **Rule:** `GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}`:
  - **Default query.** A job without a completed row returns all four fields:
    `started: null`, `success: null`, `completed: false`, `result: null`. This
    includes a syntactically valid UUID with no job row.
  - **`get_started=true`.** `started` is the queued job's running flag, or
    `false` when there is none; the other unfinished fields are unchanged.
  - **Completed jobs** return `started: true`, a Boolean `success`, and their
    `result`.
  - Null-valued fields are serialised, not omitted. Stock TS `waitJob` on an
    unknown valid UUID can therefore poll indefinitely.
  - **An id that is not a UUID** is refused by the web framework before the
    handler: 400, `Content-Type: text/plain; charset=utf-8`, body
    ``Invalid URL: Cannot parse `id` with value `<id>`: UUID parsing failed: <detail>``
    ([WA-42](#wa-42)). Every `jobs_u` route keyed by `{id}` answers the same
    (`completed/get_result`, `get_root_job_id`, `get`).
  - **No authentication.** The route belongs to the unauthenticated job
    group ([WA-4](#wa-4)): a missing token, or one not valid for the path's
    workspace, makes the caller anonymous instead of failing. The path's
    workspace only scopes the lookup, so a job of another workspace reads as
    unknown (the null response above). A completed job of this workspace,
    read anonymously, answers 400
    `Bad request: As a non logged in user, you can only see jobs ran by anonymous users`
    unless it was created by `anonymous`; the same check guards the
    `get_started=true` branch for a job that exists but has not completed.
- **Evidence:** `backend/windmill-api/src/jobs.rs:9197-9203`, `:9263-9328`;
  `typescript-client/client.ts:276-309`. Ids and anonymous reads:
  `backend/windmill-api/src/jobs.rs:405-409` (the route, in the
  unauthenticated group), `:9210-9218` (an optional caller, then the path
  extractor), `:9241-9256`, `:9285-9303` (the anonymous rule); the pinned
  axum 0.8.9 path rejection ([WA-14](#wa-14) lists the crate lines).
- **Conformance:** [WA-V2/a, WA-V2/b, WA-V2/c, WA-V2/d](conformance.md#wa-v2)
- **Note:** answers [Q-WA-2](#q-wa-2).
- **Revision 2026-10-29 (SC3, source re-check, chains PF1 and PF3):** added
  the answer to an id that is not a UUID (windmill-lite answers 400 with a
  text of its own; Windmill's status agrees and its text is the framework's,
  above) and the anonymous reading of a token from another workspace.

<a id="wa-16"></a>
### WA-16 · Object store (S3) client contract

**Scope:** core (client contract); later (auto keys, storages, presigning, credential helpers); EE (advanced S3 permissions) · **Confidence:** high (client contract); high (why the public source cannot settle CE's server side, source-traced); medium (public-docs CE statements); low (CE server behaviour, Q-WA-3) · **Status:** confirmed; drafted (where CE's handlers come from)

- **Rule:**
  - **`S3Object`** is either a record `{s3, storage?, presigned?}` or a URI.
    `s3://<storage>/<key>` parses to `{storage, s3: key}`; `s3:///<key>` gives
    the default storage (`storage` undefined). Any other string throws:
    - an `s3://` string with an empty key:
      `Invalid s3 object URI "<v>": expected s3://<storage>/<key> with a non-empty key (s3:///<key> for the default storage)`;
    - otherwise:
      `Invalid s3 object "<v>": expected an s3://<storage>/<key> URI (e.g. "s3:///<v>" for key "<v>" in the default storage) or { s3: <key> }`.

    Python raises `ValueError` with equivalent wording.
  - **TS `loadS3File(obj, s3ResourcePath?, workspace?)`** GETs
    `download_s3_file?file_key=<s3>[&s3_resource_path][&storage]` and returns a
    `Uint8Array` of the body. A non-2xx response throws
    `Error("Failed to load S3 file: <status> <statusText> - <body>")`.
    `loadS3FileStream` returns the `Blob`.
  - **TS `writeS3File(obj|undefined, content: string|Blob, s3ResourcePath?, contentType?, contentDisposition?, workspace?)`**
    turns a string into a `text/plain` Blob. It POSTs the raw body to
    `upload_s3_file` with `file_key` (omitted when there is no object, so the
    server **generates a key**), `s3_resource_path`, `storage`, `content_type`
    and `content_disposition`. It returns `{s3: <response file_key>, storage?}`.
  - **Python `write_s3_file`** accepts only `bytes` or `BufferedReader` and
    sends `Content-Type: application/octet-stream`; its error boundaries are in
    [WA-V9](#wa-v9).
  - **Presigned URLs** are built locally as
    `<baseUrl ?? WM_BASE_URL ?? http://localhost:3000>/api/w/<ws>/s3_proxy/<storage ?? "_default_">/<s3>?<presigned>`.
    `getPresignedS3PublicUrls` first calls `POST /w/{ws}/apps/sign_s3_objects`
    for every object that lacks `presigned`, and only then builds the URLs.
  - **CE availability (public docs, not version-pinned).** The
    [object-storage page](https://www.windmill.dev/docs/core_concepts/object_storage_in_windmill)
    states that:
    - workspace object storage works on Community Edition, with a **10 GiB
      total quota per workspace** and no per-file size limit;
    - writes beyond the quota are rejected, while reads and deletes are never
      blocked;
    - the bucket browser only works for buckets with at most 20 files;
    - advanced S3 permissions are EE-only;
    - since client 1.748.0, a bare string object such as `"path/to/file"` is
      rejected, and an auto-generated key is requested by omitting the object.

    The page also has an ambiguous sentence describing "the integration of
    Windmill with S3" as EE-exclusive. Neither the docs nor the source specify
    the auto-key format or the error when no workspace storage is configured.
  - **Server.** The source is not public and a pure OSS build registers no
    job-helper routes. **No CE server behaviour is verified**: the explicit-key
    round trip is an oracle acceptance case ([Q-WA-3](#q-wa-3)).
    - The CE image is not a pure OSS build. Its release workflow first
      copies the closed-source modules into the tree, then builds with the
      `ce` feature set, which includes `private` (use the closed-source
      modules) and `parquet` (the object-store support). So the CE image's
      `job_helpers` routes, the no-storage answer, the auto-generated key
      format, the CE quota and the answer for a missing object
      (`loadS3File` of a key that does not exist) all come from code that is
      not in the public repository; the public stubs (an empty router, and
      helpers failing with
      `Not implemented in Windmill's Open Source repository`) are never
      compiled into it. Only an oracle run can settle these
      ([Q-WA-3](#q-wa-3)).
    - The routes are nested inside the authenticated workspace group in
      public code, so a token for another workspace gets 401 there too
      ([WA-4](#wa-4)).
    - A non-2xx answer to `loadS3File` throws, whatever the server's text:
      `Failed to load S3 file: <status> <statusText> - <body>` (above).
- **Evidence:** `typescript-client/s3Types.ts:1-65` (`:53-65`);
  `typescript-client/client.ts:841-1102` (`:918-1102`, signing `:1066-1081`);
  `python-client/wmill/wmill/client.py:840-1143` (`:946-1045`), `:2214-2238`;
  `backend/windmill-api/src/job_helpers_oss.rs:1-111` (`:46-49`);
  `backend/windmill-api/openapi.yaml:21348-21430`; public docs page above.
  Where CE's handlers come from: `.github/workflows/docker-image.yml:72-74`
  (the closed-source modules copied in), `:88-95` (built with `features=ce`);
  `Dockerfile:97`, `:120`; `backend/Cargo.toml:114`, `:182-189`, `:198-199`
  (`ce` includes `private` and, through `oss_core`, `parquet`);
  `backend/windmill-api/src/job_helpers_oss.rs:1-3` (a `private` build uses
  the closed module), `backend/windmill-api/src/lib.rs:443-453`, `:595` (the
  routes nested in the authenticated group).
- **Conformance:** [WA-16/a, WA-16/b, WA-16/c, WA-16/d, WA-16/e](conformance.md#wa-16)
- **Design note (not Windmill behaviour):** windmill-lite's local blob store is
  a host extension that implements the verified client contract, keyed by
  `(storage or "_default_", key)` ([WA-40](#wa-40)). It claims no CE parity for
  what that contract leaves open: keys, quotas, storage selection, errors.
- **Revision 2026-10-29 (SC3, source re-check, chain PF3):** re-checked
  whether the public source can settle the CE answers milestone PF3 needed
  (no storage, a missing object). It cannot: the CE image is built with the
  closed-source modules (`ce` includes `private`). windmill-lite's 404 for a
  missing object is its own choice until [Q-WA-3](#q-wa-3)'s recording.

<a id="wa-17"></a>
### WA-17 · `WM_MOCKED_API_FILE`: an upstream local-mode hook for resources and variables

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - If `WM_MOCKED_API_FILE` is set, the clients load a JSON file
    `{variables:{path:string}, resources:{path:any}}`; missing maps default to
    `{}`. In TS, an unreadable or invalid file gives an empty mock with a
    warning. TS reads the variable on each call until a mock is loaded, then
    caches it for the module's lifetime ([WA-1](#wa-1)); Python keeps it per
    `Windmill` instance.
  - **Hits.** `getVariable` and `getResource` serve from it (TS only for
    **truthy** values; Python by key presence).
  - **Writes** through `setVariable`, `setResource` and `setState` update the
    **in-memory** copy only (never the file) and make no HTTP call.
  - **Misses differ by language.**
    - **TS:** a miss (absent or falsy value) logs
      `MockedAPI present, but resource not found at <path>, falling back to real API`
      (or the variable equivalent) and calls the API. `getState()` therefore
      hits the API at the state path when the mock lacks it.
    - **Python:** `get_resource(path, none_if_undefined=True)` on a miss logs
      and returns `None` **without any HTTP call**, which covers `get_state()`.
      With `none_if_undefined=False` (the default) it falls back to the API.
      `get_variable` misses always fall back.
  - In TS, `getState` reads through the mock (via `getResource`).
    `getFlowUserState`, S3 and dispatch are **not** mocked.
- **Evidence:** `typescript-client/client.ts:108-117`, `:578-582`, `:745-754`,
  `:779-783`, `:1457-1503`; `python-client/wmill/wmill/client.py:74-90`,
  `:583-591`, `:603-605`, `:645-659`, `:689-691`, `:752-761`.
- **Conformance:** [WA-17/a, WA-17/b](conformance.md#wa-17)
- **Design note (not Windmill behaviour):** useful for tests, but windmill-lite
  should not rely on it for core behaviour, because of the TS truthiness quirk
  and the missing flow-state and S3 support.

<a id="wa-18"></a>
### WA-18 · Resume URLs (the step asks for them; resuming is a separate suspend feature)

**Scope:** core (call shape); out (resuming) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Client call:**
    `GET /w/{ws}/jobs/resume_urls/<job id>/<nonce>?approver=<a>&flow_level=<bool>`,
    where `<job id>` is `WM_JOB_ID`, or the placeholder `NO_JOB_ID` or `NO_ID`
    (depending on the client) when it is unset. TS's nonce is an integer in
    [0, 2^32−1); Python's is in [0, 10^9].
  - **Response** `{approvalPage, resume, cancel}`:
    - `resume` = `<BASE_URL>/api/w/<ws>/jobs_u/resume/<target>/<nonce>/<sig>`
      and `cancel` = `<BASE_URL>/api/w/<ws>/jobs_u/cancel/<target>/<nonce>/<sig>`,
      each followed by `?approver=<urlencoded>` when an approver is given.
    - `<target>` is the job itself; with `flow_level=true` it is the job itself
      if its kind is `flow` or `flowpreview`, otherwise its `parent_job`.
    - `<sig>` is the lowercase hex HMAC-SHA256, under the workspace key, of
      (target UUID bytes ‖ nonce as big-endian u32 ‖ approver bytes if any).
    - `approvalPage` = `<BASE_URL>/approve/<ws>/<flow job id>?token=<approval token>`.
      Its target uses the same lookup and falls back to the job itself if the
      lookup fails.
  - A scoped token needs `jobs:run:flows:<flow path>`.
  - Getting the URLs has no effect by itself. What happens when they are hit,
    and when a step has `suspend`, belongs to [execution.md](execution.md)
    (scope `out`).
- **Evidence:** `typescript-client/client.ts:1111-1139`;
  `python-client/wmill/wmill/client.py:1255-1275`;
  `backend/windmill-api/src/jobs.rs:340-344`, `:4264-4359` (`:4311-4352`),
  `:4405-4436`.
- **Conformance:** [WA-18/a, WA-18/b](conformance.md#wa-18)
- **Design note (not Windmill behaviour):** with no resume provider,
  windmill-lite answers the resume-URL call as unsupported ([WA-41](#wa-41));
  with a host provider it returns the provider's `{approvalPage, resume, cancel}`
  unchanged.

<a id="wa-19"></a>
### WA-19 · Job introspection from scripts (`getJob`, `getRootJobId`, `getResult*`, `listQueue`, cancel)

**Scope:** core (`getRootJobId`); later (`getJob`, `getResult`); out (queue listing, cancelling foreign jobs) · **Confidence:** high (surface); high (the root lookup's and `getResult`'s error answers, source-traced); low (what callers rely on) · **Status:** confirmed; drafted (the root lookup's and `getResult`'s error answers)

- **Rule:** scripts can read their own job tree. `JobService.getJob({id})`
  returns a `Job` (`oneOf` `QueuedJob`/`CompletedJob` with discriminator `type`,
  plus fields such as `parent_job`, `args`, `flow_status`, `running`,
  `job_kind`, `script_path`). Scripts can also list queues and cancel jobs.
  `getRootJobId` follows the effective-root rule ([WA-14](#wa-14),
  [WA-V1](#wa-v1)). TS `setProgress(…, jobId)` uses `getJob`.
  - **The root lookup.** TS `getRootJobId(id?)` (default `WM_JOB_ID`; with
    neither it throws `Job ID not set` before any request) and Python
    `get_root_job_id` call `GET /w/{ws}/jobs_u/get_root_job_id/{id}`, an
    unauthenticated route ([WA-4](#wa-4)). It looks the id up in the path's
    workspace and answers 200 with the root id as a JSON string. An id with
    no job row there (unknown, or a job of another workspace) is not a 404:
    the database's no-row error surfaces as HTTP 400, `text/plain`,
    `SqlErr: no rows returned by a query that expected to return at least one row @jobs.rs:<line>:<col>`
    ([WA-42](#wa-42)'s `SqlErr` row; the location is the converting call
    site and moves between builds, [FX-50](execution.md#fx-50)). An id that
    is not a UUID gets the framework's 400 of [WA-V2](#wa-v2). Stock TS
    rejects with an `ApiError` of status 400; Python raises
    `Exception("<url>: 400, <body>")`.
  - **`getResult(id)`** (TS) and Python `get_result` call
    `GET /w/{ws}/jobs_u/completed/get_result/{id}`, which reads completed
    jobs only. A job still queued or running, and an unknown id alike,
    answer 404
    `Not found: Completed Job not found at name <id> (jobs.rs:9074)`; the
    parenthesised location is part of the text ([FX-50](execution.md#fx-50)).
    A completed job read anonymously gets the 400 of [WA-V2](#wa-v2).
    `getResultMaybe` is the polling route of [WA-V2](#wa-v2).
- **Evidence:** `backend/windmill-api/openapi.yaml:13896` (`getJob`), `:24558`
  (`QueuedJob`), `:24674` (`CompletedJob`), `:25110` (`Job`), `:23497`
  (`FlowStatus`); `backend/windmill-api/src/jobs.rs:498-507`;
  `typescript-client/client.ts:632-640`. Error answers:
  `backend/windmill-api/src/jobs.rs:389`, `:405` (both routes in the
  unauthenticated group), `:489-507` (the root lookup expects one row),
  `:9026-9074` (`getResult` reads completed rows; the not-found at `:9074`),
  `:9123-9140` (the anonymous rule); `backend/windmill-common/src/error.rs:153-158`
  (a database error becomes `SqlErr` with its call site), `:250-262` (`SqlErr`
  is 400); `backend/windmill-common/src/utils.rs:338-351` (the not-found
  text); sqlx-core 0.8.6 `src/error.rs:62-64` (the no-row text, checked
  against `backend/Cargo.lock`); `typescript-client/client.ts:142-149`,
  `:317-320`; `python-client/wmill/wmill/client.py:505-515`, `:550-571`.
- **Conformance:** [WA-19/a, WA-19/b, WA-19/c, WA-19/d](conformance.md#wa-19)
- **Design note (not Windmill behaviour):** windmill-lite has no job rows, so
  it needs a per-endpoint policy: synthesise a minimal record from the run's
  in-memory tree, delegate to a real Windmill, or fail loudly. `getRootJobId`
  and `get_result_maybe` for jobs windmill-lite dispatched itself are cheap to
  synthesise faithfully; a full `flow_status` is not. Under the synthesise
  policy, `JobService.getJob` on the flow id would return at least
  `{"type":"QueuedJob","id":"<flow>","running":true}`. A minimal record is a
  design choice, not the full upstream schema.
- **Revision 2026-10-29 (SC3, source re-check, chains PF1 and PF3):** added
  Windmill's answers where windmill-lite chose its own: the root lookup of an
  unknown id is a 400 `SqlErr` text, not a 404 and not windmill-lite's 501;
  `getResult` on a job that has not completed is the 404 not-found text above,
  the status windmill-lite already gives.

<a id="wa-20"></a>
### WA-20 · Progress, and other small helpers with exact client-side behaviour

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **`appendToResultStream(t)`** prints the single stdout line `WM_STREAM: `
    followed by `t` with every newline replaced by the two characters `\n`.
    `streamResult` does this for each chunk. Python does the same with `print`.
    Stream handling is in [languages.md](languages.md).
  - **`setProgress(p, jobId?)`.**
    - TS sends `{percent: floor(p), flow_job_id}`, where `flow_job_id` is
      `WM_FLOW_JOB_ID`, or undefined if that is `""`. If `jobId` is given, TS
      first GETs the job and uses its `parent_job` as `flow_job_id`.
    - Python sends the value as given (no floor). It fetches the job (to use
      its `parent_job` as `flow_job_id`) whenever a job id is available, passed
      in or taken from `WM_JOB_ID`.
  - **Progress is not monotonic.** Each set overwrites the stored integer. GET
    returns the latest value clamped to 0–99. The copy written into the parent
    flow's status is also clamped, with no comparison against the previous
    value.
  - **`getProgress`** returns a number or `null`.
  - **`getRootJobId()`** with `WM_JOB_ID` unset throws `Error("Job ID not set")`
    in TS.
  - **`resolveDefaultResource(x)`:** if `x` is a string starting `$res:`, it
    returns `getResource(rest, true)`; otherwise `x`.
  - **`databaseUrlFromResource(p)`** returns
    `postgresql://<user>:<password>@<host>:<port>/<dbname>?sslmode=<sslmode>`
    from the resource, with no escaping.
  - **`uint8ArrayToBase64`/`base64ToUint8Array`** use standard padded base64.
- **Evidence:** `typescript-client/client.ts:142-149`, `:232-244`, `:546-552`,
  `:626-664`, `:809-812`, `:1164-1224`; `python-client/wmill/wmill/client.py:763-805`,
  `:2248-2262`; `backend/windmill-common/src/job_metrics.rs:161-170`;
  `backend/windmill-api-jobs/src/job_metrics.rs:175-204` (set), `:244-261`
  (get).
- **Conformance:** [WA-20/a, WA-20/b, WA-20/c, WA-20/d](conformance.md#wa-20)
- **Design note (not Windmill behaviour):** the minimal local behaviour is to
  store the last value and clamp on read.

## E. Injecting a shim, and per-invocation context

<a id="wa-30"></a>
### WA-30 · How the client reaches a job, and the options for serving it without Windmill

**Scope:** core (Bun); later (Python and other languages) · **Confidence:** medium (inferred design; mechanisms read) · **Status:** confirmed

- **Rule:**
  - `windmill-client` reaches a Bun job as an ordinary lockfile dependency: the
    script's `.script.lock` is split into `package.json` and `bun.lock`, and the
    worker imports `windmill-client` as a package without injecting a version.
    Whatever version the lockfile pins is what runs.
  - The Bun worker runs `bun run … -r ./loader.bun.js wrapper.mjs`; the preload
    is a plugin (`onResolve`/`onLoad`) that installs Windmill's relative-import
    resolver.
  - TS `workerHasInternalServer()` is true when the base is a `127.0.0.1` (or
    `localhost`) URL, which changes `datatable`/`ducklake` routing
    ([WA-8](#wa-8)).
- **Evidence:** `backend/windmill-worker/loader.bun.js:1-120`;
  `backend/windmill-worker/src/bun_executor.rs:2370-2399`
  (`bun run … -r ./loader.bun.js wrapper.mjs`), `:353-404` (lockfile split),
  `:1766-1776` (`windmill-client` imported as a package);
  `typescript-client/client.ts:52-54`.
- **Conformance:** [WA-30/a](conformance.md#wa-30), a windmill-lite
  acceptance check (not Windmill behaviour).
- **Design note (not Windmill behaviour):** three ways to serve the client
  surface without a real Windmill:
  1. **Loopback HTTP facade, stock client unmodified.** Point `BASE_URL` and
     `BASE_INTERNAL_URL` (both, per [WA-2](#wa-2)) at `http://127.0.0.1:<port>`
     before the client module is first evaluated, and implement the endpoint
     subset of [WA-8](#wa-8) in Go. Language-neutral (Bun, Python, Deno, Go,
     Bash with curl), single-sourced, and it exercises the exact pinned client
     with its quirks, at one loopback round trip per call.
  2. **In-process `fetch` interception (Bun fast path).** Keep the stock client
     but replace `globalThis.fetch` in the harness so that requests to the
     facade origin go over the existing harness channel to the same Go router,
     without TCP. Both the generated services (when generated with the fetch
     transport, [WA-V8](#wa-v8)) and the raw-fetch helpers go through `fetch`.
  3. **Module alias.** Resolve the bare specifier `windmill-client` to a shim
     through a Bun plugin `onResolve` (at runtime via a `-r` preload, as Windmill
     installs `loader.bun.js`, or as a `Bun.build` plugin when bundling). The
     shim must then reproduce the full export surface of the version pinned in
     the script's lockfile ([WA-8](#wa-8)), a large maintenance surface. The
     Python equivalent is a `wmill` package earlier on `sys.path`, or
     `sys.modules["wmill"]` pre-seeded by the harness.

  The recommendation is options 1 and 2 with one Go router, and option 3 only
  for narrowly overridden helpers. Version drift between pinned clients is a
  real concern ([Q-WA-8](#q-wa-8)). Acceptance check: the same fixture flow,
  run once through the facade with the stock `windmill-client@1.767.0` and once
  through the in-process interceptor, yields byte-identical normalised step
  results and identical recorded platform calls (method, path, query, body).

<a id="wa-31"></a>
### WA-31 · Per-invocation identity: process-global env and token require per-step isolation

**Scope:** core · **Confidence:** high (requirement); medium (candidate mechanism, Bun 1.4.2); low (pinned Bun 1.3.10, Q-WA-4) · **Status:** confirmed

- **Rule (established from source):**
  - The client reads `WM_*` from `process.env` at call time ([WA-1](#wa-1)),
    `OpenAPI.TOKEN` is captured once at import, and user code reads
    `process.env.WM_*` directly ([WA-4](#wa-4)). All three are process-global.
  - Consequently, when several steps run concurrently in one process (parallel
    `forloopflow`, `branchall`), each step must still see its own `WM_*` values,
    through the client's call-time reads and through direct reads, and every
    platform call must be attributable to the step that made it. Process-wide
    writes to `process.env` and the import-time `OpenAPI.TOKEN` cannot provide
    either.
- **Evidence:** `typescript-client/client.ts:413-421`, `:435-441`, `:628`,
  `:1306-1316`, `:1746` (call-time env and `OpenAPI.TOKEN` reads);
  `typescript-client/build.sh:21-32` (import-time capture); `Dockerfile:285`
  (the image ships Bun 1.3.10); `backend/windmill-worker/src/bun_executor.rs:3448-3462`.
- **Conformance:** [WA-31/a, WA-31/b](conformance.md#wa-31)
- **Design note (not Windmill behaviour):** candidate mechanism, not yet
  validated on the pinned Bun 1.3.10 ([Q-WA-4](#q-wa-4)). Run each step inside
  an `AsyncLocalStorage` context and:
  1. replace `process.env` with a view that answers from the current context
     first (assigning to `process.env` is permitted);
  2. intercept `globalThis.fetch` and identify the caller from the async
     context, **not** from the token, which stays at its import-time value;
  3. rewrite `Bun.env.*` and `import.meta.env.*` reads to `process.env` at
     bundle time with `Bun.build`'s `define`.

  Observed in a synthetic experiment on Bun 1.4.2 (16 concurrent invocations
  with random delays): the `process.env` view isolated 16/16; attributing
  fetches through the async context worked 16/16, both for a client loaded from
  source and for the same client inlined by `Bun.build` (the harness cannot
  reach a bundled client's config object, but fetch interception still works);
  `Bun.env` and `import.meta.env` reads were isolated 0/16 without the rewrite
  and 16/16 with it; `Bun.env === import.meta.env`, and `Bun.env` does not
  follow a replaced `process.env`; assigning to `Bun.env` threw
  `Attempted to assign to readonly property.`, and `Object.defineProperty` on it
  threw as well. Experiment design, for rerunning on 1.3.10: a synthetic client
  module captures `BASE_URL`/`WM_TOKEN` into a module-local object at import
  and reads `WM_JOB_ID`/`WM_WORKSPACE` per call; an entry function awaits a
  random delay, then returns `process.env.WM_JOB_ID`, `Bun.env.WM_JOB_ID` and
  the result of a client `fetch`; the harness installs the view and the fetch
  interceptor and runs 16 entries in parallel under distinct contexts, from
  source, from a `Bun.build` bundle, and from a bundle with the `define`
  rewrite. Gate: the mechanism must pass [WA-31/a, WA-31/b](conformance.md#wa-31)
  on the pinned Bun before anything relies on it. Code in `node_modules` that
  windmill-lite does not bundle and that reads `Bun.env` directly is not
  covered by the bundle-time rewrite. Making `OpenAPI.TOKEN` an accessor is
  unnecessary: fetch-time attribution covers the raw-fetch helpers, bundled
  copies and the generated services (provided they call `fetch`,
  [Q-WA-6](#q-wa-6)). Windmill's native per-job `process.env` object
  ([WA-7](#wa-7)) is a precedent, not proof.

<a id="wa-32"></a>
### WA-32 · TS `task()` switches to HTTP dispatch when `WM_FLOW_JOB_ID` is empty

**Scope:** core (env invariant); out (workflow-as-code) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **TS.** `task(fn)` outside a workflow context runs `fn` directly **unless**
    `WM_JOB_ID` is set and `WM_FLOW_JOB_ID` is empty. In that case it POSTs to
    `/w/{ws}/jobs/run/workflow_as_code/{WM_JOB_ID}/{name}` and waits, reading
    `WM_TOKEN` at call time ([WA-1](#wa-1)). TS `step(name, fn)` and `sleep(s)`
    outside a workflow just run `fn()` or wait locally.
  - **Python.** The `@task` wrapper checks the `_workflow_ctx` `ContextVar`
    first. Only when it is unset does HTTP dispatch apply: whenever `WM_JOB_ID`
    is set and `MAIN_OVERRIDE` does not equal the function's name, **even inside
    flow steps**. The `workflow` decorator does not set the context; it only
    marks the function. The context is set by `_run_workflow_async`, the runner
    the worker invokes, for the duration of the workflow body, and reset
    afterwards.
- **Evidence:** `typescript-client/client.ts:1792-1807`, `:1844-1898`;
  `python-client/wmill/wmill/client.py:2912-2948`, `:3031-3041`, `:3126-3190`
  (set at `:3128`, reset at `:3188`).
- **Conformance:** [WA-32/a, WA-32/b, WA-32/c](conformance.md#wa-32)
- **Design note (not Windmill behaviour):** windmill-lite must give every flow
  step a non-empty `WM_FLOW_JOB_ID`, or TS `task()` silently switches to HTTP
  dispatch.

<a id="wa-33"></a>
### WA-33 · Workflow-as-code is a separate execution model

**Scope:** out · **Confidence:** medium (execution model read, not traced); high (detection paths) · **Status:** confirmed; drafted (the parsed path's declaration rules, revised 2026-10-28)

- **Rule:**
  - `workflow()`/`task()`/`step()`/`waitForApproval()` with a `WorkflowCtx`
    implement checkpoint and replay. A task throws `StepSuspend` with dispatch
    information, the worker re-runs the script with `completed_steps` and
    `_executing_key`, and inline steps checkpoint over
    `POST /w/{ws}/jobs/wac/inline_checkpoint/{job}`. The Bun worker injects a
    `WorkflowCtx` re-export into scripts it detects as workflow-as-code (WAC).
  - **Detection.** Windmill's TS parser marks a script as WAC in two ways:
    1. **Parsed path** (takes priority). The parser walks the top-level
       items in source order. A variable declaration named like the
       entrypoint (default `main`), exported or not, whose initialiser is a
       call to the bare identifier `workflow` with an `async` arrow or
       `async` function as first argument (possibly parenthesised), sets the
       WAC flag, provided no earlier item has supplied the entrypoint's
       parameters (an earlier `function main` declaration does; a later one
       replaces the parameters but leaves the flag set). An
       `export default workflow(…)` expression of the same shape sets the
       flag the same way. This needs no `task(` call and no
       `windmill-client` substring; [WR-51](languages.md#wr-51) gives the
       parser's walk in full.
    2. **Substring fallback**, used only when no entrypoint parameters were
       found at all: if the source text contains `workflow(`, `task(` and
       `windmill-client`, the kind is WAC, otherwise `lib`. This is a plain
       substring test, not a parsed call.
- **Evidence:** `typescript-client/client.ts:1514-1991` (`:1516-1790`,
  `:1965-1991`); `backend/windmill-worker/src/bun_executor.rs:1190`, `:2068`;
  `backend/windmill-worker/src/wac_executor.rs:18-81`, `:219-260`;
  `backend/parsers/windmill-parser-ts/src/lib.rs:367-376` (the default-export
  form), `:378-423` (exported and plain top-level declarations; the variable
  form only while no parameters are known), `:428-440`, `:476-530`
  (`extract_workflow_params`).
- **Revision 2026-10-28 (source re-check):** the parsed path said the
  variable had to be exported and that any function declaration took
  priority over it; the parser accepts an unexported declaration too, only
  an earlier `function main` blocks the variable form, and the
  default-export form was missing.
- **Conformance:** [WA-33/a, WA-33/b](conformance.md#wa-33); the policy
  check [WA-33/c](conformance.md#wa-33)
- **Design note (not Windmill behaviour):** rejecting WAC scripts is
  windmill-lite policy, not upstream behaviour. Imports alone do not prove a
  program uses WAC; to match upstream classification the loader should mirror
  both detection paths, not the substring test alone. Acceptance check: the
  loader rejects a detected WAC script with error kind
  `unsupported_feature`/`workflow_as_code`.

## F. The platform interface (minimal faithful local behaviour)

<a id="wa-40"></a>
### WA-40 · Platform services, and the three resource-read contracts they must keep apart

**Scope:** core (resources, variable reads, flow user state, root id, dispatch interface, explicit-key object store, resume via host provider); later (the rest) · **Confidence:** medium (design synthesis; the resource-read contracts are source-traced) · **Status:** confirmed

- **Rule (upstream contracts the design must respect):**
  - Upstream has three distinct resource-read contracts:
    - **(a) Raw read** (`get_value`): no interpolation.
    - **(b) Client interpolated read** (`get_value_interpolated` with no job
      id): `$WM_*` stays literal and the depth counter starts at 0
      ([WA-12](#wa-12)). The expression helper `resource()` reads this way
      too ([JE-20](expressions.md#je-20); revision 2026-10-29, SC3).
    - **(c) Worker argument resolution** ([expressions.md](expressions.md)):
      handles `$var:`, `$jsonvar:`, `$res:`, `$encrypted:` and any string
      `$<name>` that matches a reserved or custom env variable; runs for a
      top-level argument only when it contains a
      `$var:`/`$jsonvar:`/`$res:`/`$encrypted:` marker; a nested `$res:` calls
      contract (b) **with** the job id, which starts a fresh depth counter;
      `$WM_*` inside resources then uses the job's stored fields, so
      `$WM_ROOT_JOB_ID` can be `""` where the process env shows the effective
      root ([WA-V3](#wa-v3)).
  - The platform surface the clients reach groups into the services of the
    [WA-8](#wa-8) table: resources, variables/secrets, state, flow user state,
    job tree, dispatch, object store, resume, identity, progress, and services
    with no local counterpart (OIDC, Slack/Teams approval, Kafka,
    datatable/ducklake, workflow-as-code, queue listing and cancelling foreign
    jobs).
- **Evidence:** derived from [WA-8](#wa-8) to [WA-20](#wa-20), [WA-V1](#wa-v1),
  [WA-V3](#wa-v3); `backend/windmill-worker/src/common.rs:164-210`, `:262-396`;
  `backend/windmill-store/src/resources.rs:913-935`.
- **Conformance:** windmill-lite acceptance checks
  [WA-40/a, WA-40/b](conformance.md#wa-40) (not Windmill behaviour); the
  upstream contracts are exercised by [WA-V3/a](conformance.md#wa-v3) and the
  service fixtures of WA-10 to WA-18.
- **Design note (not Windmill behaviour):** each service is a Go interface
  with a default local implementation and a host override, arranged as a chain
  of resolvers like the windmill-ts `ResourceResolver` ([WA-60](#wa-60)).
  Proposed minimal local behaviour:
  - **Resources** (raw get, exists, create, update value): values from the
    folder's `*.resource.yaml` plus host overrides; one walker parameterised by
    contract (a), (b) or (c); errors and statuses per [WA-10](#wa-10),
    [WA-12](#wa-12), [WA-42](#wa-42).
  - **Variables and secrets** (get value, exists, create, update): plain values
    from the folder's `*.variable.yaml`; secrets from a pluggable, host-provided
    secret resolver; secret reads register the value for log masking (later,
    [Q-WA-9](#q-wa-9)).
  - **State:** backed by Resources with type `state`, so interpolation on read
    is preserved; persistence is host-chosen (memory by default).
  - **Flow user state:** a map on the root run object for the run's lifetime;
    GET of a missing key gives `null`; a set outside a flow run gives 404
    `Not found: Flow job not found`; the map is discarded when the run ends
    ([WA-14](#wa-14)).
  - **Job tree:** synthetic UUIDs per run, step and iteration, keeping stored
    root, effective root and innermost scope apart ([WA-V1](#wa-v1));
    `get_result_maybe` is answered for jobs windmill-lite dispatched.
  - **Dispatch:** run the target from the loaded folder (by path), or delegate
    ([WA-50](#wa-50)); return a UUID as `text/plain`; `waitJob` semantics as in
    [WA-15](#wa-15).
  - **Object store:** a host-provided blob store keyed by
    `(storage or "_default_", key)`, a local directory by default, implementing
    only the verified client contract ([WA-16](#wa-16)).
  - **Resume:** a host provider; unsupported by default ([WA-18](#wa-18)).
  - **Identity** (`WM_EMAIL`, `WM_USERNAME`, `WM_PERMISSIONED_AS`, whoami,
    `usernameToEmail`): host-configured constants.
  - **Progress:** last value wins, clamped on read ([WA-20](#wa-20)); a no-op
    store by default.
  - **Unsupported** (OIDC, Slack/Teams, Kafka, datatable/ducklake, WAC, queue
    listing and cancelling foreign jobs): answered as in [WA-41](#wa-41); an
    HTTP error response alone does not fail the step.

  Acceptance checks: [WA-V3/a](conformance.md#wa-v3) passes under windmill-lite
  (the argument resolves to `{"root":""}`, `getResource` returns
  `{"root":"$WM_ROOT_JOB_ID"}`, the process env shows F), and each service gets
  one shared fixture run on both the oracle and windmill-lite, compared after
  normalisation.

<a id="wa-41"></a>
### WA-41 · Unsupported platform calls: what the stock clients do with an error response

**Scope:** core · **Confidence:** high (stock-client behaviour, conditional on the fetch transport, Q-WA-6); medium (the failure policy, a proposal) · **Status:** confirmed

- **Rule:**
  - **404 means "absent" to several helpers.** TS `getResource(…, true)` and
    `getState()` turn a 404 (and only a 404) into `undefined`; Python
    `get_resource(…, none_if_undefined=True)` and `get_state()` turn any error
    into `None`.
  - **No status by itself makes every helper fail.** [WA-V5](#wa-v5) lists the
    helpers that resolve or swallow on an error response.
  - **For a `text/plain`, non-JSON 501 body**, the remaining helpers (those
    going through the generated services, the TS helpers that rethrow, and
    Python `get`/`post`) throw or raise:
    - generated-service calls reject with an `ApiError` whose `status` is 501,
      `message` is `Not Implemented` (the generator's default status text,
      [WA-V8](#wa-v8)), `body` is the response text, and `statusText` is the
      response's own status text;
    - TS helpers that rewrap embed the body, e.g.
      `Resource not found at <p> or not visible to you: <body>`;
    - Python `get`/`post` raise `Exception("<url>: 501, <text>")`;
    - Python upload raises `Exception("Could not write file to S3")`
      ([WA-V9](#wa-v9)).

    This is not a statement about arbitrary 501 bodies: with a JSON body,
    Python upload can return a result or raise `KeyError` ([WA-V9](#wa-v9)).
- **Evidence:** `typescript-client/client.ts:126-133` (404 is special),
  `:432-442`, `:530-539`, `:672-720`, `:734-736`, `:759-761`;
  `python-client/wmill/wmill/client.py:669-673`, `:807-829`; [WA-V8](#wa-v8),
  [WA-V9](#wa-v9).
- **Conformance:** [WA-41/a](conformance.md#wa-41) and the policy check
  [WA-41/b](conformance.md#wa-41); stock-client reactions in
  [WA-V5/a, WA-V5/b, WA-V5/c](conformance.md#wa-v5).
- **Design note (not Windmill behaviour):**
  - **Facade endpoint contract.** An unimplemented route answers HTTP 501,
    `text/plain`, body `Not implemented in windmill-lite: <METHOD> <route template>`.
    Never 404, which the reads above would turn into "absent" and so mask the
    gap.
  - **How the step ends** (a proposal, [Q-WA-11](#q-wa-11)). Every 501 is
    recorded against the calling step, using the attribution of
    [WA-31](#wa-31). When the step settles, if any unsupported call was
    recorded, windmill-lite fails the step, replacing any success result, with
    `{"name":"UnsupportedPlatformCall","message":"Not implemented in windmill-lite: <METHOD> <route template>","calls":[…]}`;
    `message` names the first recorded call and `calls` lists all of them. This
    deliberately departs from stock behaviour. Hosts configure it as `fail`
    (default) or `observe` (stock-client behaviour, for oracle comparison).
  - Acceptance checks: in `fail` mode, the steps of
    [WA-V5/a–c](conformance.md#wa-v5) end with the `UnsupportedPlatformCall`
    error even though the script returned normally; `getIdToken("aud")` throws
    with `status === 501`; `getState()` returns `undefined` only for a genuinely
    missing state.

<a id="wa-v5"></a>
### WA-V5 · An unsupported HTTP response does not imply a rejected helper call

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** endpoint behaviour and client-observable behaviour are distinct. On
  an HTTP 501 with text body B:
  - TS `runScriptByPathAsync`, `runScriptByHashAsync` and `runFlowAsync`
    resolve to B, as if it were a job id;
  - TS `setFlowUserState`/`getFlowUserState` without `errorIfNotPossible` log
    and resolve `undefined`;
  - Python `get_resource(…, none_if_undefined=True)` and `get_state()` return
    `None`, and Python `set_flow_user_state` ignores non-404 HTTP failures after
    the root lookup succeeds.

  A policy that guarantees an unsupported operation fails the step needs a
  separately specified mechanism ([WA-41](#wa-41)); with the stock clients
  unchanged, returning 501 alone cannot provide it.
- **Evidence:** `typescript-client/client.ts:432-442`, `:530-539`, `:672-720`;
  `python-client/wmill/wmill/client.py:669-673`, `:807-829`.
- **Conformance:** [WA-V5/a, WA-V5/b, WA-V5/c](conformance.md#wa-v5)

<a id="wa-42"></a>
### WA-42 · Error status codes and bodies

**Scope:** core · **Confidence:** high · **Status:** confirmed; drafted (request-extraction and authentication rejections)

- **Rule:** every variant of the API `Error` type is rendered as `text/plain`
  with its display string as the body. The full mapping:

  | Status | Variant: body |
  |---|---|
  | 404 | `NotFound`: `Not found: <m>` |
  | 401 | `NotAuthorized`: `Not authorized: <m>` |
  | 403 | `PermissionDenied`: `Permission denied: <m>`; `RequireAdmin`: `Require Admin privileges for <m>` |
  | 400 | `BadRequest`: `Bad request: <m>`; `QuotaExceeded`: `Quota exceeded: <m>`; `SqlErr`: `SqlErr: <e> @<location>`; `JsonErr`: rendered from its `error`/`details` payload; `AIError`: the raw message |
  | 502 | `BadGateway`: `Bad gateway: <m>` |
  | the error's own status | `Generic(status, m)`: `<m>` |
  | 500 | every other variant: `BadConfig` (`Bad config: <m>`), `ConnectingToDatabase` (`Connecting to database: <m>`), `MetricNotFound` (`Metric not found: <m>`, a 500 despite its wording), `ExecutionErr` (raw, e.g. `timeout after 600s`), `ResultTooLarge` (raw), `IoErr`/`Utf8Err`/`UuidErr`/`SerdeJson`/`HexErr` (`<Kind>: <e> @<location>`), `InternalErr` (`Internal: <m>`), `InternalErrLoc` (`Internal: <m> @<location>`), `InternalErrAt` (`Internal: <location>: <m>`), `DatabaseMigration` (`Migrating database: <e>`), `ExitStatus` (`Non-zero exit status for <x>: <code>`), `ExecutionRawError` (`ExecutionRawError: <json>`), `Anyhow` (`Error: <e> @<location>`), `AlreadyCompleted` (raw), `WacSuspended` (`WAC job suspended: <m>`), `FindPythonError` (`Find python error: <m>`), `ArgumentErr` (`Problem with arguments: <m>`), `WorkersAreBehind`, `MigrationNeeded`, `FeatureUnavailable` (their fixed sentences) |

  - Failures while extracting the request (a malformed path, query or JSON
    body) come from the web framework (axum 0.8.9), not from this table, and
    their bodies differ: `Content-Type: text/plain; charset=utf-8`, and no
    `Error` display prefix. The ones a client call can meet:
    - **a path segment that is not a UUID**, where the route takes one: 400
      ``Invalid URL: Cannot parse `<param>` with value `<value>`: UUID parsing failed: <detail>``,
      where `<param>` is the route's parameter name (`id`, `job_id`, …),
      `<value>` the percent-decoded segment and `<detail>` the UUID parser's
      reason: `invalid length: found <n>` when every character is a hex
      digit, ``invalid character: found `<c>` at <i>`` at the first character
      that is neither a hex digit nor `-`, `invalid group count: expected 5, found <n>`
      or `invalid group length in group <g>: expected <e>, found <n>` for a
      hyphenated form;
    - **a JSON body**, where the route takes one: without a JSON content type
      (`application/json`, or any `application/*+json`), 415
      ``Expected request with `Content-Type: application/json` ``; a body
      that does not parse, an empty one or one with trailing data, 400
      `Failed to parse the request body as JSON: <parser message>`; a body
      that parses but does not fit the expected type, 422
      `Failed to deserialize the JSON body into the target type: <message>`.
      The parser message is serde_json's (`expected value at line 1 column 1`
      for `hello`, `EOF while parsing a value at line 1 column 0` for an
      empty body), preceded by `<path>: ` when the error lies inside a
      member or element (for example `a: ` for `{"a": x}`).
  - **Authentication** runs before the handler, so before these. An
    authenticated route called with no token at all answers 401 with the
    bare body `Unauthorized` (`text/plain; charset=utf-8`); with a token
    that is not valid, or not valid for the path's workspace, 401
    `Not authorized: Unauthorized` (`text/plain`, [WA-4](#wa-4)).
  - **Not-found bodies a script can see:**
    - direct resource path missing: `Not found: Resource <p> not found`, from
      the permission-explanation path;
    - nested `$res:` missing during interpolation (authenticated caller
      included): the located form
      `Not found: Resource not found at name <nested p> (resources.rs:<line>)`;
    - variable missing, directly or via a nested `$var:`:
      `Not found: Variable <p> not found`.
- **Evidence:** `backend/windmill-common/src/error.rs:24-99` (variants and
  display formats), `:250-282` (status mapping);
  `backend/windmill-common/src/utils.rs:338-351`;
  `backend/windmill-store/src/resources.rs:593-631`, `:728-735`, `:843-853`.
  Request extraction: `backend/Cargo.lock` (axum 0.8.9, axum-core 0.5.6,
  serde_path_to_error 0.1.20, uuid 1.24.0, serde_json 1.0.151); in those
  crates, axum `src/extract/path/mod.rs:357-401`, `:420-451`,
  `src/extract/path/de.rs:375-391`, `src/json.rs:106-113`, `:138-155`,
  `:172-202`, `src/extract/rejection.rs:10-40`, axum-core
  `src/macros.rs:136-141`, serde_path_to_error `src/lib.rs:119-126`, uuid
  `src/external/serde_support.rs:133-135`, `src/error.rs:61-206`.
  Authentication: `backend/windmill-api-auth/src/auth.rs:592-619` (no
  token), `:720-829` (token checked); `backend/windmill-api/src/lib.rs:716-717`
  (the token-presence layer wraps, so runs before, the authentication layer).
- **Conformance:** [WA-42/a, WA-42/b, WA-42/c](conformance.md#wa-42); the
  framework rejections in [WA-14/d](conformance.md#wa-14) and
  [WA-V2/c](conformance.md#wa-v2), the 401 in [WA-4/b](conformance.md#wa-4)
- **Design note (not Windmill behaviour):** a facade answering in Windmill's
  place reproduces these statuses and bodies.
- **Revision 2026-10-29 (SC3, source re-check, chains PF1 and PF3):** the
  request-extraction rejections, which the first edition only said differ,
  are now spelled out from the pinned framework crates, and the 401 of a
  token for another workspace is added.

## G. Hybrid: delegating chosen steps to a real Windmill

<a id="wa-50"></a>
### WA-50 · Delegation endpoints and their semantics

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:** paths here are given from the instance root (with `/api`), as a
  delegating caller sends them.
  - **Deployed code, synchronous.**
    `POST /api/w/{ws}/jobs/run_wait_result/p/{path}` (script) or
    `/api/w/{ws}/jobs/run_wait_result/f/{path}` (flow), body = args.
    - On success: 200 with the result JSON.
    - On job failure: **422** with the stored failed-job result (an object with
      an `error` field; shape per [execution.md](execution.md) and
      [languages.md](languages.md)).
    - It waits up to the instance setting `timeout_wait_result` (env
      `TIMEOUT_WAIT_RESULT`, **default 600 s**), then returns 500 with body
      `timeout after <n>s`.
    - Whenever the wait ends without a result (timeout **or the HTTP client
      disconnecting**), the server **cancels the job** with reason
      `http connection broke`.
    - Results that set `windmill_status_code`/`windmill_content_type`/
      `windmill_headers` are turned into custom HTTP responses.
    - For a flow with `early_return`, the answer can come before the flow
      completes ([WA-V7](#wa-v7)).
  - **Deployed code, asynchronous.** `POST /api/w/{ws}/jobs/run/p|h|f/…` returns
    a `text/plain` UUID; then poll
    `GET /api/w/{ws}/jobs_u/completed/get_result_maybe/{id}` ([WA-15](#wa-15),
    [WA-V2](#wa-v2)).
  - **Local code.** `POST /api/w/{ws}/jobs/run/preview` or
    `/api/w/{ws}/jobs/run_wait_result/preview` with
    `{content, language, lock, path, args, modules?, temp_script_refs?}`; for
    sub-flows `POST /api/w/{ws}/jobs/run/preview_flow` or
    `/api/w/{ws}/jobs/run_wait_result/preview_flow` with
    `{value: FlowValue, path, args}`. A preview does not by itself snapshot
    transitive code ([WA-V6](#wa-v6)).
  - **Job tree.** A job submitted without `parent_job`/`root_job` is a
    detached root: its `WM_FLOW_JOB_ID`/`WM_ROOT_FLOW_JOB_ID` differ from a
    fully remote run, and flow user state ([WA-14](#wa-14)) and `getRootJobId`
    key on the delegated job.
  - **What a change of representation changes.** Rewriting a script-by-path
    flow step as an inline (`rawscript`) step keeps `WM_STATE_PATH`, which is
    built from the parent flow path, the step id and the trigger
    ([WA-5](#wa-5)). It changes `WM_JOB_PATH`: the script's path for a by-path
    step, `<flow path>/<step id>` for an inline step without an explicit
    `path`. `WM_RUNNABLE_ID` follows [WA-V10](#wa-v10). Only transformations
    that alter the parent flow path or the step id change the state path, for
    example moving the step into a different wrapper flow, renaming steps, or
    delegating it as a standalone script job (which gets
    `<script path>/<trigger>`).
  - **Scopes.** Deployed-path runs need path-scoped permissions; previews need
    generic `jobs:run` ([WA-51](#wa-51)).
- **Evidence:** `backend/windmill-api/src/jobs.rs:175-200`, `:250-266`;
  `backend/windmill-api-jobs/src/execution.rs:186-220` (cancel-on-drop guard),
  `:224-226`, `:263-372` (poll loop, timeout, early return), `:374-400`
  (200/422), `:813-848`; `backend/windmill-api/openapi.yaml:9909`
  (`runWaitResultScriptByPath`), `:12781` (`runScriptPreview`), `:12890`
  (`runScriptPreviewAndWaitResult`), `:13064` (`runFlowPreview`), `:13103`
  (`runFlowPreviewAndWaitResult`), `:25965-26005` (`Preview`), `:29658-29680`
  (`FlowPreview`); `backend/windmill-common/src/variables.rs:300-321`;
  `backend/windmill-worker/src/worker_flow.rs:5076-5086`.
- **Conformance:** [WA-50/a, WA-50/b](conformance.md#wa-50); see also
  [WA-V6](conformance.md#wa-v6), [WA-V7](conformance.md#wa-v7),
  [WA-V10](conformance.md#wa-v10).
- **Design note (not Windmill behaviour):** use the asynchronous pair for
  anything that can exceed the wait timeout or must survive client restarts.
  Pass `parent_job`/`root_job` only if a real anchor job exists. windmill-lite
  surfaces a delegated 422 as that step's failure with the same error object as
  a local failure.

<a id="wa-v6"></a>
### WA-V6 · Preview delegation needs a transitive code snapshot

**Scope:** later (hybrid execution) · **Confidence:** high (preview and import boundary) · **Status:** confirmed

- **Rule:**
  - **Script previews** carry the entrypoint `content` and `lock`, plus
    `modules` (relative-path modules) and `temp_script_refs` (relative imports
    resolved from temp storage). Any relative import these do not cover is
    fetched by the worker's Bun loader from the **deployed** workspace scripts.
  - **Flow previews:** `script`-by-path and `flow`-by-path modules run the
    **deployed** targets; supplying the outer `FlowValue` does not snapshot
    them.
- **Evidence:** `backend/windmill-api/openapi.yaml:25965-26005`;
  `backend/windmill-worker/loader.bun.js:82-120`;
  `backend/windmill-worker/src/worker_flow.rs:4285-4296`.
- **Conformance:** [WA-V6/a](conformance.md#wa-v6)
- **Design note (not Windmill behaviour):** running the exact local code
  remotely requires supplying every transitive dependency (inline scripts in the
  flow value, plus `modules`/`temp_script_refs` for relative imports) or
  refusing delegation with an explicit error. Rewriting a by-path target as an
  inline module changes `WM_JOB_PATH` and possibly `WM_RUNNABLE_ID`
  ([WA-50](#wa-50), [WA-V10](#wa-v10)), so it is not a neutral substitution.

<a id="wa-v7"></a>
### WA-V7 · Flow wait-result can finish before the remote flow

**Scope:** later (hybrid route choice) · **Confidence:** high (handler and guard traced) · **Status:** confirmed

- **Rule:** flow `run_wait_result` passes the flow's `early_return` node to the
  waiter, which can return that node's result before a completed-flow row
  exists, while the flow keeps running: a 200, or a 422 if the node failed and
  the flow has no failure module. If the early node failed and a failure module
  exists, the early result is ignored and the handler waits for the completed
  flow. Returning an early result marks the wait guard done, so closing the
  request does not cancel the rest of the flow. Wait-result and async
  dispatch-then-poll are therefore interchangeable only for flows without
  `early_return`.
- **Evidence:** `backend/windmill-api-jobs/src/execution.rs:263-372`
  (`:293-318`), `:813-848`.
- **Conformance:** [WA-V7/a, WA-V7/b](conformance.md#wa-v7)
- **Note:** the underlying early-return rule is in [execution.md](execution.md).

<a id="wa-51"></a>
### WA-51 · Platform calls made by delegated steps, and token scopes

**Scope:** later · **Confidence:** high (scope checks); medium (hybrid modes, a design) · **Status:** confirmed

- **Rule:**
  - A delegated step runs on real Windmill, so its
    `getResource`/`getVariable`/S3/state calls hit the **real workspace**, not
    windmill-lite's platform.
  - **Scopes.** Running a deployed path checks a path-scoped permission,
    `jobs:run:flows:<path>` or `jobs:run:scripts:<path>`; the wait-result
    handlers check the same per-path scopes. Every preview handler (script
    preview, inline script preview, bundle script preview, flow preview) checks
    generic `jobs:run`.
  - The Apache-2.0 Go client `github.com/windmill-labs/windmill-go-client` is
    generated from `openapi.yaml` with `oapi-codegen`, plus thin helpers that
    read `BASE_INTERNAL_URL`, `WM_WORKSPACE` and `WM_TOKEN`.
- **Evidence:** `backend/windmill-api/src/jobs.rs:5834` (flows), `:5869`
  (scripts), `:6470`/`:6489` (`run_preview_script`), `:6570`/`:6585`
  (`run_inline_preview_script`), `:6822`/`:6837` (`run_bundle_preview_script`),
  `:7503`/`:7518` (`run_preview_flow_job`);
  `backend/windmill-api-jobs/src/execution.rs:874` (flows), `:916` (scripts);
  `go-client/windmill.go:16-190` (`:20-35`), `go-client/build.sh:1-27`
  (`:14-19`), `go-client/LICENSE`.
- **Conformance:** [WA-51/a](conformance.md#wa-51); the policy check
  [WA-51/b](conformance.md#wa-51)
- **Design note (not Windmill behaviour):** two coherent hybrid modes:
  - **(a) Remote-backed platform.** The facade proxies resources, variables,
    S3 and state to the real Windmill, adding a configured token and
    workspace, so local and delegated steps see the same data. Flow user state
    and the job tree stay local, because they are per run.
  - **(b) Local platform**, with delegation restricted to steps that touch no
    platform data.

  Delegation needs a token with `jobs:run` scope (path-scoped tokens for
  deployed paths) and a workspace id. The Go client can be used as a
  dependency, or windmill-lite can generate its own from `openapi.yaml`.
  Acceptance check for mode (a): a local step's `getVariable("u/x/v")` and a
  delegated step's `getVariable("u/x/v")` return the same value, and the facade
  records one proxied `GET …/variables/get_value/u/x/v`.

## H. Prior art: windmill-ts

<a id="wa-60"></a>
### WA-60 · What windmill-lite can reuse from windmill-ts

**Scope:** core (loader overlap, owned by openflow.md); later (typed adapters) · **Confidence:** high (read) · **Status:** confirmed

- **Rule:** [windmill-ts](https://github.com/invakid404/windmill-ts)
  (`master` @ `af2b1e325320`, Unlicense, the same licence as windmill-lite, so
  its code may be copied freely) offers five reusable ideas:
  1. **A source seam.** `GenerationSource` has a remote implementation and a
     **local sync-folder** implementation that discovers `*.script.yaml|json`,
     `*.resource.yaml|json`, root-level `*.resource-type.*`, `*.flow.json` and
     `*.flow`/`__flow` directories. It applies the root `wmill.yaml` include,
     exclude and skip filters, expands file-resource `!inline <path>` with
     path-escape checks, and merges resource-type schemas in the order Hub
     cache, committed catalog, local files, so local definitions win over the
     catalog, which wins over the Hub.
  2. **Typed methods.** Per-path argument schemas are converted from Windmill
     JSON Schema into typed argument checks, with these fixes for Windmill
     quirks:
     - drop `default: null`;
     - drop a `default` not in its `enum`;
     - delete `enum: null` on strings;
     - `contentEncoding: base64` means a base64 or `data:` URL string;
     - an object with no properties allows additional properties;
     - `format: dynselect-*` restores `originalType`;
     - `format: resource-<type>` means a resource reference or a value of that
       type;
     - S3 objects are detected when the type name with its **first** `_`
       removed, lower-cased, equals `s3object`; the schema is
       `{s3, storage?, filename?}`.
  3. **A resolver hook.** `ResourceResolver(ctx)` is consulted first. `ctx` has
     `path`, `resourceType`, `options`, `hasEmbeddedResource`,
     `resolveEmbedded()`, `fetchResource()` and `resolveDefault()`. It returns
     `{value}`, or `undefined` to fall through to embedded values and then
     `wmill.getResource`.
  4. **Detached vs tracked dispatch.** A `detached` option controls whether
     `parent_job`/`root_job` are sent ([WA-15](#wa-15)).
  5. **Embedded resource values** snapshotted at generation time, which serve as
     a "static resources" mode.

  windmill-ts's generated client is **not** an offline runtime: it calls
  `windmill-client` (and uses `wmill.OpenAPI.BASE`/`TOKEN` for raw fetches,
  [WA-9](#wa-9)).
- **Evidence:** windmill-ts @ `af2b1e325320`: `src/source/types.ts`,
  `src/source/local/*.ts` (`resourceTypes.ts` for schema precedence),
  `src/generator/common.ts:89-263`, `src/generator/scripts.ts:8-79`,
  `src/generator/resources.ts:150-300`; README section "Resource Resolver Hook".
- **Conformance:** none (prior art).
- **Note:** these are reusable prior-art choices, not proof of Windmill schema
  or runtime semantics.
- **Design note (not Windmill behaviour):** the source seam maps onto the
  loader ([openflow.md](openflow.md)); port it to Go or call it at build time.
  The schema fixes are the argument-schema rules a typed host adapter needs, and
  windmill-ts's tests can serve as fixtures for a port. The resolver hook is a
  template for windmill-lite's platform resolver chain ([WA-40](#wa-40)). Under
  windmill-lite, windmill-ts's calls are served by the facade or interceptor
  like any other client call ([WA-30](#wa-30)).

## Open questions

<a id="q-wa-1"></a>
- **Q-WA-1** · affects [WA-4](#wa-4), [WA-6](#wa-6) · answered: see
  [WA-6](#wa-6) and [WA-V1](#wa-v1) (from source). Oracle corroboration of
  WA-6/a–d is tracked in [Q-WA-13](#q-wa-13).
<a id="q-wa-2"></a>
- **Q-WA-2** · affects [WA-15](#wa-15) · answered: see [WA-V2](#wa-v2).
  Corroboration in [Q-WA-13](#q-wa-13).
<a id="q-wa-3"></a>
- **Q-WA-3** · affects [WA-16](#wa-16), [WA-40](#wa-40) · What is the CE server
  behaviour of the S3 job helpers at v1.767.0: the explicit-key round trip, the
  format of auto-generated keys, the status and body when no workspace storage
  is configured, and the response at the quota boundary? *Settled by:* a CE
  `1.767.0` oracle with workspace storage pointed at a local S3-compatible
  store: run `writeS3File({s3:"t/a.txt"},"hi")` plus `loadS3File`,
  `writeS3File(undefined,"x")`, the same upload with no storage configured, and
  a quota-boundary upload; record status, body and returned `file_key`
  (cases [WA-16/c, WA-16/d](conformance.md#wa-16)). **Re-scoped 2026-10-29
  (SC3).** Re-checked for milestone PF3: the public source cannot answer any
  part of this, because the CE image is built with the closed-source modules
  ([WA-16](#wa-16), Server), so this stays oracle-only. Added to it: the
  status and body of `loadS3File` for a key that does not exist (the stock
  TS client then throws `Failed to load S3 file: <status> <statusText> - <body>`),
  which windmill-lite answers with a 404 of its own meanwhile. *Settled by:*
  the runs above plus `loadS3File({s3:"t/none.txt"})` after the explicit-key
  write, on the same storage-configured session, recording the caught
  error's message ([WA-16/e](conformance.md#wa-16)).
<a id="q-wa-4"></a>
- **Q-WA-4** · affects [WA-31](#wa-31) · Does the per-invocation isolation
  mechanism hold on the pinned Bun 1.3.10 with the real client? (build gate)
  *Settled by:* rerunning the [WA-31](#wa-31) experiment on Bun 1.3.10 with the
  stock pinned `windmill-client`, using both namespace and default imports,
  bundled and unbundled, plus one unbundled `node_modules` dependency that reads
  `Bun.env`; 16/16 isolation is required for every channel.
<a id="q-wa-5"></a>
- **Q-WA-5** · affects [WA-14](#wa-14) · answered: see [WA-V1](#wa-v1).
  Corroboration in [Q-WA-13](#q-wa-13).
<a id="q-wa-6"></a>
- **Q-WA-6** · affects [WA-8](#wa-8), [WA-9](#wa-9), [WA-V8](#wa-v8),
  [WA-41](#wa-41), [WA-31](#wa-31) · Which transport was the client artifact the
  harness actually uses generated with, and what are its exact root exports?
  (packaging gate) *Settled by:* generating with
  `@hey-api/openapi-ts@0.43.0 --client fetch` explicitly, or inspecting the
  published `windmill-client@<pinned>` `dist` for global-`fetch` request code
  and `ApiError`/`OpenAPI` root exports; then running
  [WA-8/b, WA-8/c](conformance.md#wa-8).
<a id="q-wa-7"></a>
- **Q-WA-7** · affects [WA-13](#wa-13) · answered: interpolation of state on
  read is upstream behaviour; see [WA-13](#wa-13). Corroboration in
  [Q-WA-13](#q-wa-13).
<a id="q-wa-8"></a>
- **Q-WA-8** · affects [WA-8](#wa-8), [WA-30](#wa-30) · Which
  `windmill-client`/`wmill` versions do real sync folders pin, and how do their
  export surfaces and routes differ from 1.767.0? (inventory) *Settled by:*
  scanning the `.script.lock` files of representative sync folders for the
  resolved versions, then diffing each version's export lists (`build.sh` named
  and default lists; Python public names) and helper routes against
  [WA-8](#wa-8).
<a id="q-wa-9"></a>
- **Q-WA-9** · affects [WA-11](#wa-11) · (design decision) Should secret-read log
  masking be in scope, and if so, does it reach worker logs? *Settled by:* a
  scope decision first; if in scope, an oracle run in which a step reads a
  secret variable and prints it, checking whether the stored job log shows it
  masked.
<a id="q-wa-10"></a>
- **Q-WA-10** · affects [WA-14](#wa-14) · **Answered 2026-10-29 (SC3)**
  from the source: see [WA-14](#wa-14) (After the run). The status row is
  deleted, by a cascading key, in the same transaction that completes the
  root, so from that moment a POST answers 404 `Not found: Flow job not found`
  and a GET answers `null`; there is no grace period. Oracle corroboration
  of [WA-14/c](conformance.md#wa-14) is tracked in [Q-WA-13](#q-wa-13).
  Original question: after a root flow completes, does a flow-user-state
  POST to its id still succeed, and for how long? *Settled by:* running a
  flow to completion, then `POST …/jobs/flow/user_states/{root}/{k}` and
  `GET` of the same, recording statuses over time, and tracing the
  `v2_job_status` cleanup.
<a id="q-wa-11"></a>
- **Q-WA-11** · affects [WA-41](#wa-41) · (design decision) Should windmill-lite
  adopt the fail-by-default `UnsupportedPlatformCall` policy? *Settled by:* a
  design decision recorded in the platform-interface design record.
<a id="q-wa-12"></a>
- **Q-WA-12** · affects [WA-50](#wa-50), [WA-V10](#wa-v10) · answered: see
  [WA-V10](#wa-v10) (from source). Oracle comparison of the step
  representations is tracked in [Q-WA-13](#q-wa-13).
<a id="q-wa-13"></a>
- **Q-WA-13** · affects the findings listed here · No case in this area has
  been recorded on the v1.767.0 CE oracle. Do the source-derived expectations
  that exercise server behaviour hold there? Named as oracle runs or oracle
  corroboration: [WA-2](#wa-2), [WA-6](#wa-6) (Q-WA-1), [WA-9](#wa-9),
  [WA-13](#wa-13) (Q-WA-7), [WA-18](#wa-18), [WA-51](#wa-51), [WA-V1](#wa-v1)
  (Q-WA-5), [WA-V2](#wa-v2) (Q-WA-2), [WA-V10](#wa-v10) (Q-WA-12). Other
  server-side expectations: [WA-3](#wa-3), [WA-4](#wa-4), [WA-5](#wa-5),
  [WA-10](#wa-10), [WA-11](#wa-11), [WA-12](#wa-12), [WA-14](#wa-14),
  [WA-15](#wa-15), [WA-19](#wa-19), [WA-20](#wa-20), [WA-33](#wa-33),
  [WA-42](#wa-42), [WA-50](#wa-50), [WA-V3](#wa-v3), [WA-V6](#wa-v6),
  [WA-V7](#wa-v7). *Settled by:* recording each case on the
  oracle ([oracle.md](oracle.md)) in the configuration it states; cases that
  run against a recording facade test the client only and need no oracle.
  **Re-scoped 2026-10-29 (SC3, chain PF2):** the source re-check added four
  server cases to this list: [WA-11/c, WA-11/d](conformance.md#wa-11) (a
  secret that cannot be decrypted; the `already_encrypted` shape check) and
  [WA-12/g, WA-12/h](conformance.md#wa-12) (an unusable `job_id`;
  `CUSTOM_INSTANCE_DB/`). Each is recordable from a Bun step that calls the
  API with its own token; WA-11/c's ciphertext is accepted whatever the
  oracle's workspace key, but whether its read fails depends on that key
  (validation round 1): almost always with the padding cause, rarely with a
  UTF-8 cause, and more rarely the read succeeds. The recording therefore
  selects a ciphertext whose read fails with the padding cause and records
  which one it kept ([WA-11/c](conformance.md#wa-11)).

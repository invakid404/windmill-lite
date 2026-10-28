# Loops, branches and subflows

This area covers what changes inside and around flow containers:
`forloopflow`, `whileloopflow`, `branchone`, `branchall`, by-path subflows
(`type: flow`) and `identity`. It describes body scopes, iteration and branch
results, failure and early-stop propagation, `results.<id>` visibility across
container boundaries, and the stored-representation details that change any
of these. Plain-step semantics (input transforms, `retry`,
`continue_on_error`, `failure_module`, flow result selection, error objects,
cancellation) are in [execution.md](execution.md). The expression engine is
in [expressions.md](expressions.md), and per-language error and result shapes
are in [languages.md](languages.md).

**Pinned version:** Windmill `v1.767.0` (commit `6de4ec0f665ad856002495c4d86269105ec28ad7`).
Source pointers are `path:line @ v1.767.0` relative to the Windmill repository
root. Status, scope and confidence tags are defined in the [index](README.md#legend).

**ID prefixes in this file:** `CF-` control-flow findings; `CF-Vn` findings were added during validation.

**Conventions.**

- `worker_flow.rs` abbreviates `backend/windmill-worker/src/worker_flow.rs`.
- A **container** is a module of type `forloopflow`, `whileloopflow`,
  `branchone` or `branchall`. Its **body** is the module list of an
  iteration or a branch. A **child flow** is the nested flow job a body runs
  as (CF-1).
- A body is stored either **inline** (the module list inside the container
  module) or as a **stored node** (a separately stored flow, used by the
  deployed **lite** version that a flow's dependency job builds; CF-V4).
- Flow jobs have a **job kind**: `Flow` (a by-path run of a deployed flow, or
  a by-path subflow), `FlowNode` (a stored-node body) or `FlowPreview` (any
  raw flow submission, including every inline body; CF-V5). Fixtures write
  them in lower case (`flow`, `flownode`, `flowpreview`).
- A **collapsed** loop (the "fast path") runs a simple one-module body
  directly as a script job instead of a child flow (CF-8).
- Fixture shorthand (`ret(E)`, `fail(M)`, `ERR(M)`) and the run-mode rules
  for conformance cases are defined under
  [Notation](conformance.md#cf-notation).
- **Recordings.** Recording batch E3d (design record 0012) recorded the
  cases of CF-2 to CF-15 on the v1.767.0 CE oracle, by path after a
  successful dependency job (fixtures
  [`conformance/flows/control-flow`](../../conformance/flows/control-flow);
  oracle runs 40685057858, 40686532046 and 40692334073). Batch E3a recorded
  the CF-19 and CF-V6 cases (fixture
  [`conformance/flows/cf/cf-19`](../../conformance/flows/cf/cf-19), oracle
  run 40687124625). Every recorded case held, and the recordings settled
  CF-12 and CF-19. The recorded job kinds agree with this file: a non-simple
  body runs as a `flownode` child, a simple body that is not collapsed (a
  parallel loop's) stays inline and runs as a `flowpreview` child, and a
  collapsed iteration is a script job directly under the loop. The other
  expectations are source-derived; [Q-CF-1](#q-cf-1) and
  [Q-CF-17](#q-cf-17) track what remains.

## Summary for the design

1. **A container body is a nested flow scope.** Its `flow_input` is the
   enclosing flow's input plus `iter` for loops; an existing `iter` key moves
   to `iter_parent`, recursively. Its step 0 gets that input as
   `previous_result`. It has its own step statuses, a copy of the enclosing
   failure handler, and the nearest ancestor's `flow_env`
   ([CF-1](#cf-1), [CF-3](#cf-3), [CF-9](#cf-9)).
2. **The simple-loop fast path is observable and depends on job kind.**
   Collapsing changes the `previous_result` chain, which module's fields
   apply, step ids, result registration and timeout enforcement. A forloop
   collapses only when the flow job executing it is not a `FlowPreview` job;
   while loops have no job-kind condition, but only simple bodies ever
   collapse. The gate is local to the executing flow: a by-path subflow is
   always a `Flow` job, so a raw submission does not stop collapsing inside
   a subflow it calls. Collapse disqualifiers are tested by presence
   (`retry: {}` and `cache_ttl: 0` disqualify). The stored form actually
   selected (lite or full) matters, because a successful dependency job
   alone does not determine it ([CF-8](#cf-8), [CF-V5](#cf-v5),
   [CF-26](#cf-26), [CF-27](#cf-27)).
3. **Container results are arrays in index or branch order**, never in
   completion order. Entries are child results: error objects, handler
   outputs, nested arrays. A failed container's result, and so an uncaught
   failed flow's final result, is that array, not an `{"error": …}` object
   ([CF-4](#cf-4), [CF-5](#cf-5), [CF-15](#cf-15)).
4. **Failure and recovery differ by mode.** Sequential containers stop at the
   first non-skippable failure and count a handler-recovered child as failed.
   Parallel containers run every child, then judge persisted child status, so
   recovered children pass. `parallelism` releases eligibility to start in
   index order ([CF-5](#cf-5), [CF-7](#cf-7), [CF-V1](#cf-v1)).
5. **Stopping and failing are separate.** A loop module's `stop_after_if` is a
   break, except on an empty container, where it stops the flow. A clean body
   stop breaks a sequential loop, is ignored by parallel containers,
   propagates through `branchone` and sequential `branchall`, and is blocked
   at a subflow boundary. An error stop is consumed by the copied handler when
   the flow has one. Without a handler, a skippable sequential-branchall
   branch yields a stopped but successful flow. `stop_after_all_iters_if`
   runs only for active containers. A sequential loop that breaks on an
   unhandled error stop with `skip_failures: false` fails, and the enclosing
   flow fails with the loop's array without running its next step: a break
   clears the stop, not the failure (recorded on the oracle)
   ([CF-11](#cf-11), [CF-12](#cf-12), [CF-13](#cf-13), [CF-29](#cf-29),
   [CF-V2](#cf-v2)).
6. **`results.<id>` is resolved by ordered routes:** whole-expression context
   name, exact input/env property, previous-step shortcut, a narrow direct
   lookup in a scoped last-write-wins map (`null` on a miss), then the JS
   engine (own-step fetch that can throw, then the map). New scopes start
   only at subflows and parallel iterations. The route decides between `null`
   and an error. Direct lookup is line-anchored: the first qualifying line
   of a multiline expression answers by itself and the rest of the text is
   ignored (recorded on the oracle; [CF-19](#cf-19), [CF-V6](#cf-v6),
   [JE-9](expressions.md#je-9)).
7. **Absent defaults and stored representation are compatibility inputs.**
   Absent skip flags mean `false` before a successful dependency job and the
   typed defaults after it; a pulled folder, and a folder after the CLI's
   `generate-metadata`, then shows them explicitly. The lite form changes
   handler ids, inline script paths and job kinds; a by-path `script` keeps
   its own path. It reduces only the module lists: the failure and
   preprocessor modules stay as written, get no virtual step, and their
   `sleep` is never applied ([CF-6](#cf-6), [CF-V4](#cf-v4),
   [CF-24](#cf-24), [CF-30](#cf-30)).
8. **Errors while starting a container take one of three routes:** a root
   flow at pickup records a step failure and handles it normally; a child
   flow at pickup fails and bypasses its own handler; later chaining aborts
   the flow with `InternalError`, bypassing its handler. `branchone`
   predicate errors are always step failures. The pickup and chaining forms
   are recorded on the oracle ([CF-23](#cf-23), [CF-V3](#cf-v3),
   [CF-14](#cf-14)).
9. **Timing and enforcement.** The iterator and `branchone` predicates are
   evaluated once; `timeout` and `parallelism` per child. A container
   `timeout` is enforced only on collapsed script children on the ordinary
   worker path. Cancellation marks the whole subtree; held and queued
   children never run body code; the root ends with a `Canceled` error; no
   ordering against running children is guaranteed ([CF-2](#cf-2),
   [CF-21](#cf-21), [CF-27](#cf-27), [CF-28](#cf-28)).
10. **Every child boundary is a JSON round trip**, so siblings get
    independent copies ([CF-20](#cf-20)).

## A. Execution model of containers

<a id="cf-1"></a>
### CF-1 · Every container body runs as a nested child flow scope

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Which bodies.** A `forloopflow` iteration, a `whileloopflow` iteration,
    the chosen `branchone` branch and each `branchall` branch run their body
    as a **child flow**. The exception is a collapsed loop body
    ([CF-8](#cf-8)), whose eligibility depends on the job kind of the flow
    executing the loop ([CF-V5](#cf-v5)).
  - **Same for inline and stored-node bodies:**
    1. The child's flow input is the enclosing flow's input object, plus
       `iter` for loops ([CF-3](#cf-3)).
    2. The child has its own step list and statuses; its step 0 receives that
       input as `previous_result` ([CF-9](#cf-9)).
    3. The child carries a copy of the enclosing flow's `failure_module` and
       the enclosing `same_worker` flag.
    4. The child has no preprocessor, `early_return`, flow-level skip
       expression, or concurrency or cache settings of its own.
    5. The child sees the `flow_env` of the nearest ancestor flow that defines
       one. A by-path subflow defines its own scope and inherits only when it
       has none ([CF-17](#cf-17)).
    6. The child's result is selected like any flow's (see
       [execution.md](execution.md)).
  - **Inline bodies only** ([CF-V4](#cf-v4)):
    - the copied handler's id gets a suffix: `-<step>-<index>` for loop
      iterations and `branchall` branches, `-<step>` for `branchone`, where
      `<step>` is the container's position in its flow;
    - `preserve_step_tags` is inherited.
  - **Stored-node bodies:** the handler keeps its original id, and
    `preserve_step_tags` is false. Every non-empty body under the `modules`
    list of a deployed flow that has a failure module is a stored node, so
    there the copy reads `failure` (recorded, [FX-34](execution.md#fx-34));
    a container in the failure-module or preprocessor slot is not reduced at
    deploy and keeps inline bodies.
  - The container step's own result is assembled from the child results
    ([CF-4](#cf-4), [CF-13](#cf-13), [CF-14](#cf-14)).
- **Evidence:** `worker_flow.rs:5038-5074` (inline construction; the
  stored-node early return comes first), `:3964` (a container's args are the
  enclosing flow's args), `:4158-4222` (`iter` added per iteration),
  `:3322-3330`, `:2795-2808`, `:2606-2650` (`flow_env` inheritance);
  `backend/windmill-worker/src/worker_lockfiles.rs:1554-1591` (node built from
  the modules, unsuffixed handler, `same_worker` only);
  `backend/windmill-queue/src/jobs.rs:5420-5440`;
  `backend/windmill-types/src/flows.rs:631`.
- **Conformance:** [CF-1/a](conformance.md#cf-1)

<a id="cf-2"></a>
### CF-2 · `forloopflow` iterator evaluation

**Scope:** core (message); later (error name per route) · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - The `iterator` is evaluated **once**, when the loop step starts, and the
    evaluated array is stored. It is not re-evaluated per iteration. A
    `retry` of the loop step re-evaluates it ([CF-22](#cf-22)).
  - `type: static` uses the literal value. `type: javascript` is evaluated
    with this scope:
    - `flow_input`: the enclosing flow's input;
    - `result` and `previous_result`: both the previous step's result, or the
      flow input when the loop is step 0 of its flow;
    - `results.<id>`, where `results.<previous step id>` short-circuits to
      `previous_result`;
    - `resume`, `resumes`, `approvers`;
    - `flow_env`.
  - `type: ai` fails with the message
    `AI input transform not supported for iterator`.
  - The value must be a JSON **array**. Otherwise the array check raises the
    internal `ExecutionErr` variant with the message
    `Expected an array value in the iterator expression, found: <deserializer message>`.
    For an object the tail is
    `invalid type: map, expected a sequence at line 1 column 0` (Windmill's
    own test `backend/tests/worker.rs:3342` asserts this text).
  - The error a flow observes is wrapped according to where it is raised
    ([CF-23](#cf-23)): a root flow at pickup gets name `Anyhow` with the
    message `Error: <text above> @<file>.rs:<line>:<col>`; later chaining
    gives name `InternalError` with the plain message. Both forms are
    recorded (oracle: a loop at step 0 gave the suffix `@worker.rs:3822:13`,
    and the same loop after a first step gave the plain text). The location
    suffix depends on the build; compare the message with it masked.
  - An empty array completes the step at once with result `[]`; no child is
    created.
  - **Restart.** Continuing a loop that was restarted mid-iteration reuses
    the stored iterator array; it is re-evaluated only when that stored array
    is empty. Restart is out of scope ([CF-26](#cf-26)).
- **Evidence:** `worker_flow.rs:5747-5905` (`next_forloop_status`; `:5778`,
  `:5804` message, `:5839-5881` restart reuse), `:3998-4052` (empty →
  module `Success` with no child jobs), `:3214` (empty-loop result `[]`);
  `backend/windmill-common/src/error.rs:145-150`;
  `backend/windmill-common/src/worker.rs:2228-2232`; tests
  `backend/tests/worker.rs:3342` (exact message), `:3284`, `:3381` (empty
  iterator → `[]`; a later step summing `results.<loop>` gets `0`). The
  `Error: … @<location>` form is the display of the `Anyhow` variant,
  `backend/windmill-common/src/error.rs:73-74`, `:138-150`.
- **Conformance:** [CF-2/a, CF-2/b](conformance.md#cf-2)
- **Revision 2026-10-28 (oracle, batch E3d):** the message forms of both
  routes are now stated exactly, as recorded (fixture
  `conformance/flows/control-flow/cf-2`, runs `b-step0` and `b-later`);
  before, the finding said only that the pickup message contains the text.
  The rule is unchanged.

<a id="cf-3"></a>
### CF-3 · The `iter` object and nesting (`iter_parent`)

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - Each loop iteration's child flow input is the enclosing flow input plus
    a key `iter` = `{"index": <0-based int>, "value": <element>}`.
  - For `whileloopflow`, `value` equals `index`: 0, 1, 2, …
  - If the incoming input already has an `iter` key, that key is first moved
    to `iter_parent`; an existing `iter_parent` moves to
    `iter_parent_parent`, and so on recursively. This happens with an outer
    loop's `iter`, or with a user input literally named `iter`.
  - In a doubly nested loop, `flow_input.iter` is the inner iteration and
    `flow_input.iter_parent` the outer one.
  - `iter` is reachable only as `flow_input.iter`. It is not a free variable
    in expressions, and the iterator and predicate contexts bind no `iter` of
    their own.
- **Evidence:** `worker_flow.rs:4981-4998` (`insert_iter_arg`), `:4158-4170`,
  `:4210-4222` (sequential and parallel insertion), `:5263-5279` (while:
  `value` = index), `:3009-3012` (field order `index`, `value`),
  `:2427-2472`, `:5778-5799` (no `iter` binding in iterator or predicate
  contexts); `frontend/src/lib/components/flows/previousResults.ts:96-120`
  (the UI models the same renaming); `openflow.openapi.yaml:794-826`
  (Apache-2.0: `flow_input.iter.value` / `.index`).
- **Conformance:** [CF-3/a](conformance.md#cf-3)

<a id="cf-4"></a>
### CF-4 · Loop result shape and order

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - A loop step's result is a JSON array with one entry per iteration that
    ran, in **iteration index order**. This holds for parallel loops too,
    whatever order the iterations finish in.
  - Each entry is that iteration's result: the body's flow result, or the
    single body step's result when the loop collapsed ([CF-8](#cf-8)).
  - A **failed** iteration's entry is its error result object
    (`{"error": {...}}`). The public OpenAPI description says failed
    iterations "return null"; the source keeps the error object, and so does
    the oracle (CF-4/b).
  - An entry is not always an `{"error": …}` object: when the copied failure
    handler ran in a failed iteration, the entry is the handler's output (any
    JSON, [CF-16](#cf-16)); when the body's last step is itself a failed
    container, the entry is that container's array ([CF-5](#cf-5)).
  - A `whileloopflow` result includes the iteration that triggered the break.
  - A loop with an empty `modules` list returns `[]` without iterating, even
    for a non-empty iterator.
  - The next step's `previous_result` is this array, and `results.<loop id>`
    resolves to it.
- **Evidence:** `worker_flow.rs:1476-1488` (container result = results of all
  child jobs), `:2299-2327` (`retrieve_flow_jobs_results`, ordered by the
  child-job list pushed in index order at `:4597-4660`), `:5352-5414`,
  `:5700-5720` (empty body → `EmptyInnerFlows`), `:6234-6262`; tests
  `backend/tests/worker.rs:48` (failed entry `result[2].error.message`),
  `:108` (parallel), `:3425`; `openflow.openapi.yaml:807-809` (the "null"
  claim the source contradicts).
- **Conformance:** [CF-4/a, CF-4/b](conformance.md#cf-4)

<a id="cf-5"></a>
### CF-5 · Sequential loop failure, `skip_failures`, and what the flow sees

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** For a sequential (`parallel: false`) `forloopflow` or a
  `whileloopflow`:
  - If an iteration fails and the loop has `skip_failures: true`, the next
    iteration starts. The loop step ends **successfully** after the last
    iteration, even if that one failed.
  - If `skip_failures` is false, no further iteration starts. The loop step
    **fails** with the array of results so far; its last entry is the failing
    iteration's error and its length is the failed index + 1.
  - From there the loop step is an ordinary failed step for the rules in
    [execution.md](execution.md): its own `retry` ([CF-22](#cf-22)),
    `continue_on_error`, and the flow's `failure_module` ([CF-16](#cf-16)).
    The step's result is the array.
  - If nothing catches the failure, the **flow's final result is that
    array**; it is not wrapped in `{"error": ...}`.
  - With `continue_on_error: true` on the loop module, the flow continues and
    the next step's `previous_result` is the array, error entries included.
  - This covers ordinary iteration completion. Errors raised while a child is
    starting follow [CF-V3](#cf-v3).
- **Evidence:** `worker_flow.rs:1138-1166` (next iteration if
  `success || skip_loop_failures`), `:1267-1345` (completion: success with
  `skip_loop_failures`, otherwise `Failure` with all child jobs),
  `:1476-1488`, `:1729-1764` (continue/stop decision, `continue_on_error`),
  `:2008-2028` (failed flow stored with the array result), `:571-585`
  (`skip_failures` read from the module JSON); test
  `backend/tests/worker.rs:48` (257-element sequential loop,
  `skip_failures: false`: the result is an array, entry 2 is the error).
- **Conformance:** [CF-5/a, CF-5/b](conformance.md#cf-5)
- **Note:** the recorded loops have a one-module body and run by path, so
  they collapse ([CF-8](#cf-8)): their error entries carry `step_id` equal to
  the loop's id, where the same body in a parallel loop ([CF-4/b](conformance.md#cf-4))
  carries the body step's id.

<a id="cf-6"></a>
### CF-6 · Absent container fields: defaults depend on the stored state

**Scope:** core · **Confidence:** high (the materialized values and the behaviour after a successful dependency job oracle-observed; point 6 oracle-observed; the pull output of point 5 and the un-normalized behaviour traced in source, not run) · **Status:** confirmed

- **Rule:**
  1. **Runtime fallback.** At run time `skip_failures` (forloop, whileloop)
     and a branch's `skip_failure` are read from the stored module JSON; when
     absent they count as **false**. An absent `branchall.parallel` counts as
     **true**; an absent `forloopflow.parallel` counts as **false**.
  2. **Normalization by the dependency job.** Creating or updating a flow
     through the API (which `wmill sync push` uses) enqueues an
     **asynchronous** flow dependency job. When that job *succeeds*, it
     re-serializes every container module through the typed model and writes
     the result back into the flow and its latest version. The materialized
     values are:
     - `forloopflow`: `skip_failures: true`, `parallel: false`;
     - `whileloopflow`: `skip_failures: false`;
     - `branchall`: `parallel: true`;
     - each branch: `skip_failure: true`, `parallel: true`, `expr: ""`.

     The same job builds the lite version ([CF-V4](#cf-v4)).
  3. **When normalization does not happen.** The stored JSON stays as pushed
     when any inline script fails to lock (the job errors before writing),
     when the dependency job is cancelled, or when a newer version landed
     meanwhile (the update is skipped for the obsolete version). A run
     started before the job finishes uses the un-normalized version: the
     lite lookup falls back to the full version.
  4. **Consequence.** An absent `forloopflow.skip_failures` or branch
     `skip_failure` behaves as **false** before successful normalization, or
     when it never happens, and as **true** after it.
  5. **CLI pull.** The workspace export behind `wmill sync pull` writes the
     latest version's stored value, stripping only top-level metadata keys.
     The CLI turns it into `flow.yaml`, extracting only inline script
     contents. After a successful dependency job, a pulled `flow.yaml`
     therefore shows the materialized fields explicitly; a folder pulled
     from a normalized workspace never has them absent.
  6. **CLI `generate-metadata`.** The pinned CLI's `wmill generate-metadata`
     (and its deprecated per-flow predecessors) materializes the same
     defaults **locally**. For each flow folder whose inline scripts are
     stale, it inlines their files, sends the flow value to
     `POST /api/w/{w}/jobs/run/flow_dependencies_async`, waits for that
     job, and rewrites `flow.yaml` from the job's `updated_flow_value`, with
     inline scripts extracted back into `!inline` files. That value is the
     typed re-serialization of point 2, so the written `flow.yaml` carries
     `skip_failures`, `parallel`, `skip_failure` and `expr` explicitly. The
     job runs with `skip_flow_update`, so nothing on the server changes. A
     folder that passed through `generate-metadata` therefore never has
     these fields absent either; to send them absent, the flow has to reach
     the API without it.
  - `parallelism` may be a bare number or an input transform. The
    required-ness of `skip_failures` in `openflow.openapi.yaml` is not
    enforced at run time.
  - Which stored form a run actually uses (lite or full) is a separate
    compatibility input ([CF-24](#cf-24), [Q-CF-15](#q-cf-15)).
- **Evidence:** `backend/windmill-api-flows/src/flows.rs:710-745`,
  `:1315-1350` (dependency job enqueued);
  `backend/windmill-worker/src/worker_lockfiles.rs:610-638` (error before
  write), `:673-687` (cancellation), `:711-730` (obsolete version skipped),
  `:733-745` (write-back), `:1044-1220` (typed re-serialization);
  `backend/windmill-types/src/flows.rs:1126-1160`, `:838-850`,
  `:1080-1081` (bare-number `parallelism`); `worker_flow.rs:571-585`,
  `:949-955`, `:2329-2346` (runtime reads with `false` fallback);
  `backend/windmill-queue/src/jobs.rs:5915-5930` (lite → full fallback);
  `backend/windmill-api/src/workspaces_export.rs:830-847`, `:347-380`;
  `cli/src/commands/sync/sync.ts:946-1024`; test
  `backend/tests/worker.rs:3677` (raw flow: a branch without `skip_failure`
  is not skipped). Point 6:
  `cli/src/commands/flow/flow_metadata.ts:307-347` (the value sent, then
  `flow.yaml` rewritten from the answer), `:435-513` (the request, and the
  answer's `updated_flow_value`);
  `cli/src/commands/generate-metadata/generate-metadata.ts:470-480`;
  `backend/windmill-api/src/jobs.rs:7151`, `:7163` (`skip_flow_update`
  set, a raw flow-dependencies payload);
  `backend/windmill-worker/src/worker_lockfiles.rs:662-669`, `:843-848`
  (the typed value returned).
- **Conformance:** [CF-6/a, CF-6/b, CF-6/c](conformance.md#cf-6)
- **Design note (not Windmill behaviour):** treat "absent" as a compatibility
  input and record which state a fixture represents. The proposed default for
  a sync folder is the steady state of a successful deploy (the materialized
  defaults); a strict mode could reject absent fields instead. The decision
  is [Q-CF-13](#q-cf-13).
- **Revision 2026-10-28 (oracle, batch E3d):** point 6 is new. Before, the
  finding said only that `sync pull` writes the materialized fields; the
  batch's API-deployed fixtures record, beside each run, the flow as
  submitted (fields absent), as the API answers it after the dependency job
  (fields filled) and as `generate-metadata` writes it (the same fields
  filled), and the source shows why the CLI command writes them.

<a id="cf-7"></a>
### CF-7 · Parallel `forloopflow`: spawning, `parallelism`, completion

**Scope:** core (`same_worker` error: later) · **Confidence:** high (aggregation, order, `same_worker` condition and error; release order under `parallelism: 1`; all oracle-observed); medium (start order among several eligible iterations) · **Status:** confirmed

- **Rule:** With `parallel: true`:
  - **Spawning.** All iterations are created at once as child flows; the loop
    never collapses. Each child gets `iter = {index: i, value: element_i}`.
    Each parallel iteration is its own results scope ([CF-19](#cf-19)).
  - **`parallelism` N.** Evaluated per child with the step scope
    (`flow_input`, `result`/`previous_result` = the previous step's result,
    `results`); it must parse as an unsigned 16-bit integer. Iterations with
    index `i ≥ N` are created *held* and become **eligible to start** once
    `i − N + 1` iterations have completed. Eligibility is released in index
    order; the actual start order among several eligible jobs is up to the
    scheduler. Without `parallelism`, all iterations run concurrently.
    - With `N` larger than the iterator length, nothing is held.
    - With `N = 0`, every iteration is held and no completion releases any.
      The hold carries a 14-day deadline, so "never starts" is too strong;
      behaviour at the deadline is out of scope ([Q-CF-10](#q-cf-10)).
  - **Completion.** The loop step completes only when every iteration has
    completed. There is no fail-fast: all iterations run even after one
    fails.
  - **Success decision.** It uses the children's **persisted** completion
    status, so a child that recovered through its handler
    (`recover: true`) counts as success ([CF-V1](#cf-v1)). The step succeeds
    if `skip_failures` is true or every child counts as success; otherwise
    it fails with the full array as its result.
  - **Stop conditions.** The loop module's own `stop_after_if` is not
    evaluated. A body-level early stop ends only that iteration
    ([CF-12](#cf-12)). `stop_after_all_iters_if` is evaluated once all
    iterations are done ([CF-13](#cf-13)). Empty containers follow
    [CF-V2](#cf-v2).
  - **`same_worker`.** The error
    `Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker`
    is raised only when more than one child job would be pushed **and**
    same-worker continuation is active: the flow has `same_worker`, the
    container has no `sleep`/`suspend`, and the run is not a recovery from a
    crashed worker. A one-element parallel loop or a one-branch parallel
    `branchall` in a `same_worker` flow does not error.
    - It is raised as a `BadRequest`, whose text is `Bad request: <text>`,
      while the children are being prepared, so it takes the container-start
      routes of [CF-23](#cf-23). A loop at step 0 of a root flow fails at
      pickup with name `Anyhow` and message
      `Error: Bad request: Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker @<file>.rs:<line>:<col>`
      (recorded with the suffix `@worker.rs:3822:13`); no child is
      created.
  - **Observed timing (not a rule).** On the one-worker oracle an iteration
    released by a completion started about 4 to 5 s after that completion,
    while the first `N` started at once. The one worker ran the body
    scripts one at a time but not the child flows: with `parallelism: 2`
    the child flows overlapped, with `parallelism: 1` each started after
    the previous one completed.
- **Evidence:** `worker_flow.rs:5352-5414` (all payloads at once),
  `:4484-4515` (held jobs and release counter), `:1097-1115` (release when a
  sibling completes), `:765-916` (per-child completion), `:917-1080`,
  `:1018-1035` (final aggregation, persisted-status query), `:675-682` (no
  `stop_after_if` for parallel loops), `:4100-4110`, `:4573`, `:4793-4798`
  (`same_worker` condition; the error raised as `BadRequest` at `:4793-4798`);
  `backend/windmill-common/src/error.rs:55-56` (`Bad request: ` text),
  `:73-74` (the pickup form); `backend/windmill-types/src/flows.rs:1080-1081`;
  `frontend/src/lib/components/flows/content/FlowModuleEarlyStop.svelte:72`
  (the UI hides "break" for parallel loops).
- **Conformance:** [CF-7/a to CF-7/d](conformance.md#cf-7)
- **Revision 2026-10-28 (oracle, batch E3d):** the `same_worker` error's
  recorded form (`Bad request: ` inside the pickup envelope) and the
  observed release timing are added; the release order under
  `parallelism: 1` is now recorded, so its confidence rises. Before, the
  finding gave the error text alone. What one worker cannot show is
  [Q-CF-18](#q-cf-18).

<a id="cf-8"></a>
### CF-8 · The simple-loop-body fast path; eligibility follows the executing job's kind

**Scope:** core · **Confidence:** high (by-path top-level, nested and step-0 cases oracle-observed; the raw-submission reach and the stored-form condition source-traced) · **Status:** confirmed

- **Rule:**
  1. **Body shape.** Exactly one module of type `script`, `rawscript` or
     `flowscript`; that module has no `sleep`, `suspend`, `cache_ttl`,
     `retry`, `stop_after_if`, `stop_after_all_iters_if` or `skip_if`, and no
     enabled `mock`; the flow being executed has no `failure_module`.
     - **Presence decides, not value.** Each of these fields disqualifies
       when the key is present with any non-null value: `retry: {}`,
       `cache_ttl: 0`, `skip_if: {expr: "false"}`, `sleep: {type: static,
       value: 0}` and a `stop_after_if` that can never fire all prevent
       collapsing. A field written as `null` counts as absent. `mock`
       disqualifies only with `enabled: true`. Other envelope fields
       (`timeout`, `continue_on_error`, `priority`, `summary`, …) never
       disqualify.
     - The deployed form keeps these fields as written: the dependency job
       decodes and re-encodes each module through the typed model, which
       keeps a present empty `retry` as `{}`. The dependency job uses the
       same test to decide which bodies become stored nodes: a body that
       passes it stays inline, one that fails it is stored as a node, and a
       loop whose body is a node never collapses.
  2. **For a `forloopflow`:** it must also be sequential, and the job
     executing the loop must not be of kind `FlowPreview`. Job kinds:
     - a by-path run of a deployed flow, and a by-path subflow child, are
       `Flow`;
     - a container body stored as a node (lite form, [CF-V4](#cf-v4)) is
       `FlowNode`;
     - an inline container body is `FlowPreview`, because every inline body
       is submitted as a raw flow;
     - any raw flow submission (editor preview or API raw run) is
       `FlowPreview`.
  3. **A `whileloopflow`** has no job-kind condition.
  4. **Consequences:**
     - a top-level forloop of a by-path run collapses, whether or not the
       dependency job has finished;
     - a forloop nested inside another container body collapses only when
       that body runs as a stored node (`FlowNode`). That is the case for a
       deployed flow run from its lite version, which a successful
       dependency job builds and which runs read unless `DISABLE_FLOW_SCRIPT`
       is set or the lite version is missing. Otherwise the enclosing body
       is an inline `FlowPreview` child and the nested loop does not
       collapse;
     - the gate is local to the flow executing the loop. In a raw
       submission (`FlowPreview`) and in its inline bodies no forloop
       collapses, but a by-path subflow it calls is always a `Flow` job,
       whatever the caller's kind ([CF-17](#cf-17)), so an eligible forloop
       in that subflow collapses;
     - the failure and preprocessor modules are not reduced by the lite
       version, so a body of a container-valued failure or preprocessor
       module that becomes a child flow runs inline, as a `FlowPreview`
       child, and a forloop nested in it never collapses. The collapse
       check still precedes child creation: a slot that is itself a
       sequential forloop (or a while loop) with a simple body collapses in
       the job executing it when the flow has no `failure_module`, and a
       failure module that is a loop never collapses ([CF-30](#cf-30));
     - while loops collapse at any depth in every mode, subject to the body
       shape: multi-step while bodies never collapse.
  5. **Effects of collapsing:**
     - the body's input transforms are evaluated in the enclosing flow's
       context, with `flow_input` = enclosing input plus `iter`;
     - `previous_result`: iteration 0 gets the result of the step before the
       loop; iteration k > 0 gets **iteration k−1's result**; a loop at step
       0 of its flow gives every iteration the enclosing flow input *without*
       `iter`. `results.<step before the loop>` is not short-circuited to
       `previous_result`. This differs from [CF-9](#cf-9);
     - the body module's `continue_on_error`, `timeout`, `priority`,
       `delete_after_use`/`delete_after_secs`, `pass_flow_input_directly`
       and `apply_preprocessor` are ignored in favour of the loop module's.
       A failing body with `continue_on_error: true` fails the iteration;
     - the body job's flow step id is the loop's id, and so is any `step_id`
       a language wrapper writes into errors ([languages.md](languages.md));
     - nothing is registered under the body module's id. A
       `results.<body id>` lookup falls through to the general rules
       ([CF-19](#cf-19)): `null` unless another visible step or ancestor
       scope has that id;
     - the loop's `timeout` is enforced on each body job ([CF-27](#cf-27)).
- **Evidence:** `worker_flow.rs:5729-5745` (`is_simple_modules`; each
  disqualifier tested as an absent optional field),
  `:5298-5300` (forloop: sequential and not `FlowPreview`), `:5247-5249`
  (while: no job-kind gate), `:5657-5697` (collapsed payload; transforms
  taken from the body), `:4158-4205` (per-iteration evaluation with an empty
  previous-step id and the last result), `:3322-3345` (last-result rule;
  step 0 → flow args), `:1147-1166` (an iteration hands its own result
  forward), `:4379-4400`, `:4429` (timeout and step id from the loop module),
  `:5908-5983` (`payload_from_simple_module`), `:5038-5074` (inline body →
  raw flow payload; stored node → node payload);
  `backend/windmill-queue/src/jobs.rs:5604-5667` (raw flow → `FlowPreview`),
  `:5420-5440` (node → `FlowNode`), `:5912-5980` (by path → `Flow`);
  `backend/windmill-worker/src/worker_lockfiles.rs:1554-1591`, `:1571`
  (simple bodies stay inline; non-simple, non-empty bodies become nodes,
  by the same test), `:748-758` (the lite version reduces only the module
  list); `backend/windmill-types/src/flows.rs:543-588` (the disqualifiers
  are optional fields), `:433-442` (an empty `retry` decodes to a present
  default and re-encodes as `{}`), `:662-665`. Raw-submission reach:
  `worker_flow.rs:5138-5154`, `:6020-6049` (a `flow` step always builds a
  by-path payload); `backend/windmill-queue/src/jobs.rs:5658`,
  `:5915-5930` (`DISABLE_FLOW_SCRIPT` and the lite-to-full fallback),
  `:5965-5978` (that payload is a `Flow` job).
- **Conformance:** [CF-8/a to CF-8/d](conformance.md#cf-8)
- **Design note (not Windmill behaviour):** windmill-lite must reproduce both
  paths or record a deliberate divergence; the IR has to mark collapsible
  bodies at load time and take the job kind (stored representation) as an
  input.
- **Revision 2026-10-28 (oracle, batch E3d; source re-check):** the finding
  was `DISPUTED` between its draft, which said that in a raw submission no
  forloop collapses at any depth and that a successful dependency job makes
  a nested body a stored node, and the validation's correction, which
  limits the no-collapse rule to the `FlowPreview` job and its inline
  bodies and makes the nested case depend on the version actually read.
  The correction is now the rule. The source settles it: a `flow` step
  always enqueues a `Flow` job, and the forloop gate tests only the kind of
  the job executing the loop. The by-path halves of CF-8/a to CF-8/c, on
  which both sides agreed, are recorded: `[101,103,106]`,
  `[[101,103,106]]` with the outer body a `flownode` child, and the flow
  input three times. Raw submissions are outside the reference
  configuration (design record 0001), so their halves are not recorded.
  Also new: the presence rule for disqualifiers and the bodies of the
  failure and preprocessor modules (questions asked by windmill-lite's IR
  compiler).
- **Revision 2026-10-28 (validation round 1):** the slot bullet of point 4
  said that every body of a container-valued failure or preprocessor module
  runs as a `FlowPreview` child and that no forloop there collapses. It now
  limits that to bodies that become child flows and says that a slot which
  is itself an eligible loop collapses, because the collapse check precedes
  child creation ([CF-30](#cf-30)).

<a id="cf-9"></a>
### CF-9 · `previous_result` at the first step of a non-collapsed loop body or branch

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - In a child flow ([CF-1](#cf-1)), the first step's `previous_result` is
    the **child flow's input**: the enclosing `flow_input` object, plus
    `iter` for loops. It is *not* the result of the step before the
    container. The previous-step shortcut for `results.<id>` is off for that
    first step (the previous-step id is empty).
  - Later body steps follow the ordinary rule: the previous body step's
    result.
  - The editor's autocomplete suggests the step before the container
    instead; the UI hint disagrees with the runtime.
  - To reach the step before the container, a body uses `results.<id>`,
    which works ([CF-19](#cf-19)).
- **Evidence:** `worker_flow.rs:3322-3345` (first step of any flow → its own
  args), `:3964`, `:4152-4222` (a container's child receives the enclosing
  args plus `iter`), `:3659-3664` (previous-step id empty at step 0);
  `frontend/src/lib/components/flows/previousResults.ts:200-235` (UI
  suggestion).
- **Conformance:** [CF-9/a, CF-9/b](conformance.md#cf-9)

<a id="cf-10"></a>
### CF-10 · `whileloopflow`

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - Iterations run one after another, with `iter = {index: n, value: n}`.
  - **Termination.** There is no condition field. The loop ends only through:
    1. the loop module's `stop_after_if` turning truthy, evaluated after each
       **successful** iteration ([CF-11](#cf-11));
    2. an early stop raised inside the body ([CF-12](#cf-12));
    3. a failed iteration when `skip_failures` is false (the typed default
       for while loops is false; [CF-5](#cf-5), [CF-6](#cf-6));
    4. cancellation ([CF-28](#cf-28)).

    Without any of these it never ends.
  - **Ignored fields.** `parallel` and `parallelism` appear in the OpenAPI
    description but are not part of the runtime model and are ignored.
  - **Result.** The array of all iteration results, including the iteration
    that broke the loop. An empty body returns `[]` at once.
  - **Collapsing.** A while loop has no job-kind condition, so an eligible
    simple body collapses in every job kind, including raw submissions and
    inline children ([CF-8](#cf-8)). This does not remove the body-shape
    requirements.
- **Evidence:** `worker_flow.rs:5247-5292` (no `FlowPreview` exclusion at
  `:5247-5249`), `:1138-1166` (continue until stopped), `:1195-1201`;
  `backend/windmill-types/src/flows.rs:998-1006`, `:1139-1146` (no
  `parallel` field; `skip_failures` default false);
  `openflow.openapi.yaml:827-855` (lists `parallel`); test
  `backend/tests/worker.rs:5468` (a failure in a while body propagates with
  `skip_failures: false`).
- **Conformance:** [CF-10/a](conformance.md#cf-10)

<a id="cf-11"></a>
### CF-11 · A loop module's `stop_after_if` means "break"

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** On an active **sequential** `forloopflow` or `whileloopflow`
  module, `stop_after_if` is evaluated after each **successful** iteration.
  - **Scope:** `result` and `previous_result` = that iteration's result;
    `flow_input` = the flow containing the loop; `results`; `flow_env`.
    `all_iters` is also bound, but only when the expression text contains
    `all_iters`; it holds the results of all iterations so far, including the
    current one.
  - **Effect when truthy:** no further iteration starts; the loop step
    **succeeds** with the results so far; the flow **continues**.
    `skip_if_stopped` and `error_message` on a loop module's `stop_after_if`
    have **no effect**: the stop is converted into a plain break.
  - It is never evaluated after a failed iteration, and never for parallel
    loops.
  - An empty container is the exception: there `stop_after_if` stops the
    enclosing flow ([CF-29](#cf-29), [CF-V2](#cf-v2)).
- **Evidence:** `worker_flow.rs:675-730` (evaluation; `all_iters` at `:694`),
  `:2427-2472`, `:1147` (no continue when stopped), `:1195-1201` (a stop
  inside a loop is reset to a break and does not bubble);
  `frontend/src/lib/components/flows/content/FlowModuleEarlyStop.svelte:72-82`
  (UI: "Break loop (evaluated after each iteration)"; the error-message
  option is hidden for loops).
- **Conformance:** [CF-11/a](conformance.md#cf-11)

<a id="cf-12"></a>
### CF-12 · Early stop raised inside a container body

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  1. **Clean vs error stop.** A body step's `stop_after_if` that fires is a
     **clean stop** when `skip_if_stopped` is true (which takes precedence
     over `error_message`) or no `error_message` is set. Otherwise it is an
     **error stop**: the step is marked failed with
     `{"error": {"name": "EarlyStopError", "message": <msg>}}`. An empty
     `error_message` gives the message `stop after if: <expr>`. With
     `error_include_result`, the step's own result is added as
     `error.result`.
  2. **Clean stop.** The child flow stops after that step; its result is that
     step's result, flagged skipped if `skip_if_stopped`. The container
     reacts:
     - **sequential forloop/whileloop:** breaks; the loop succeeds with the
       results so far, including this iteration, and the flow continues;
     - **parallel forloop / parallel branchall:** ignores the stop; only that
       child ended early;
     - **branchone / sequential branchall:** propagates; the enclosing flow
       stops right after the container, with the container's result, and the
       same rule applies one level up;
     - **subflow (`flow` step):** blocks the stop; the parent evaluates only
       the `flow` step's own `stop_after_if`.
  3. **Error stop, flow with a `failure_module`.** The child, which carries a
     copy of the handler, runs it. The handler's completion does **not**
     forward the stop. The container sees an ordinary failed child whose
     result is the handler's output, and its own `skip_failures` /
     `skip_failure` rules apply; a sequential loop with
     `skip_failures: true` therefore **continues** instead of breaking.
  4. **Error stop, flow without a handler.** The child completes as failed
     and forwards the stop flag, without a new error message. The container
     reacts:
     - **sequential forloop/whileloop:** breaks. Its result is the array so
       far, ending with the `EarlyStopError` entry. Breaking clears the stop
       flag, not the failure. With `skip_failures: true` the loop step
       succeeds and the flow continues with the next step. With
       `skip_failures: false` the loop step fails and is an ordinary failed
       step: its own `retry` and `continue_on_error` apply, and without them
       the enclosing flow fails with the loop's array as its result and its
       next step does not run ([CF-5](#cf-5));
     - **sequential branchall:** no further branch runs and the enclosing
       flow **stops** right after the branchall. The branchall's status, and
       so the stopped flow's status, follows the stopped branch's
       `skip_failure` (resolved per [CF-6](#cf-6)): `true` → success,
       `false` → failure. The result is the array of branch results so far.
       The parent receives the stop flag with that status and applies the
       same rules one level up;
     - **branchone:** the step **fails** (branch skip flags do not apply)
       with the child's `EarlyStopError` result; the enclosing flow stops as
       failed and forwards the stop;
     - **parallel forloop / parallel branchall:** the stop is ignored; the
       child counts as an ordinary failed child under the container's skip
       rules ([CF-7](#cf-7), [CF-V1](#cf-v1));
     - **subflow (`flow` step):** the stop is blocked; the step fails like
       any failed subflow.

     Stopping and failing are independent: a propagated stop does not imply
     a failed flow.
  5. **What the stopped step looks like.** A body step stopped with an
     error keeps its own job result and status: its job succeeded, its
     module is marked failed, and it carries the `EarlyStopError`, which is
     what its child flow returns ([FX-V2](execution.md#fx-v2)).
- **Evidence:** `worker_flow.rs:304-322` (skip precedence, default message,
  result inclusion), `:599-626` (subflow stop override ignored), `:670-682`
  (child stop applied unless parallel or subflow; forwarded without a
  message; none on handler completion), `:1145-1147`, `:1173-1192` (no next
  branch while stopped), `:1195-1201` (loop: reset to break), `:1267-1345`,
  `:1273-1284` (container success from skip flags), `:1350-1365`,
  `:1734-1736` (a stopped flow does not continue; an error stop continues
  into the handler), `:1734-1757` (a failed loop is an ordinary failed
  step), `:2006-2047`, `:2135-2150` (stop flag handed to the parent),
  `:2329-2346` (per-branch `skip_failure`); tests
  `backend/tests/worker.rs:645`, `:700` (stop inside a sequential branchall:
  flow result `[-123]`, later step not run);
  `frontend/src/lib/components/flows/content/FlowModuleEarlyStop.svelte:36-82`.
- **Conformance:** [CF-12/a to CF-12/f](conformance.md#cf-12)
- **Revision 2026-10-28 (oracle, batch E3d):** the finding was `DISPUTED`.
  Its draft said that after a sequential loop breaks on an unhandled error
  stop the flow continues after the loop either way; the validation's
  correction said a break clears the stop, not the failure. The oracle
  recorded the correction (fixture `conformance/flows/control-flow/cf-12`,
  runs `c-fail` and `f`: the loop and the flow fail with
  `[{"error":{"name":"EarlyStopError","message":"halt"}}]` and the next step
  is not run; runs `c-skip` and `f-skip`: with `skip_failures: true` the loop
  succeeds and the next step runs). The draft's sentence is withdrawn; every
  other point, on which both sides agreed, is recorded as stated. Point 5
  is new: it states what the recordings show of the stopped step.

<a id="cf-13"></a>
### CF-13 · `stop_after_all_iters_if` (loops and branchall)

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** Evaluated **once**, after an **active** `forloopflow`,
  `whileloopflow` or `branchall` has finished all its children, sequential or
  parallel. An active container is one that created at least one child.
  Sequential loops count whether they ended normally, by break or by
  failure; a sequential branchall counts unless it was stopped from inside.
  - **Scope:** `result` and `previous_result` = the array of all child
    results; `flow_input`; `results`; `flow_env`.
  - **Effect when truthy:** it acts like an ordinary step's `stop_after_if`
    on the **enclosing flow**, not a break. `skip_if_stopped` gives a clean
    stop with the skipped flag; `error_message` produces `EarlyStopError`,
    with the array embedded as `error.result` when `error_include_result` is
    set.
  - **Evaluation error:** it becomes an error stop of the enclosing flow
    (never a clean one) with the message
    ``Error evaluating stop_after_all_iters_if expression `<expr>`: <cause>``.
    The container step fails; its own job result stays the array of child
    results, and it carries
    ``{"error": {"name": "EarlyStopError", "message": "Error evaluating stop_after_all_iters_if expression `<expr>`: <cause>"}}``
    ([FX-V2](execution.md#fx-v2)). Without a handler that error object is
    the flow's final result. `<cause>` is the engine error in the
    `Anyhow` display: for a syntax error,
    `Error: QuickJS evaluation error: Error: <engine message>`, a newline,
    the engine frame lines, then ` @<file>.rs:<line>:<col>`. Recorded:
    ``Error evaluating stop_after_all_iters_if expression `bad!!!syntax`: Error: QuickJS evaluation error: Error: Unexpected token '!'`` followed by
    `\n    at eval_script:1:7\n @worker_flow.rs:2451:11`.
  - **Not evaluated** for empty containers (empty iterator, empty loop body,
    zero-branch branchall; [CF-V2](#cf-v2)) or for `branchone`.
- **Evidence:** `worker_flow.rs:342-407`
  (`evaluate_stop_after_all_iters_if`), `:968-1016` (parallel), `:1203-1239`
  (sequential; not after a branchall stopped from inside), `:304-322`,
  `:1350-1365` (`EarlyStopError` object), `:3998-4052`, `:3197-3221`,
  `:571-595` (empty containers), `:1006-1015`, `:1236-1243` (an evaluation
  error sets an error stop with the message), `:2451` (the evaluation whose
  error is converted at that location); tests
  `backend/tests/worker.rs:5550`, `:5607` (bad expression: message contains
  `stop_after_all_iters_if`), `backend/tests/flow_engine_parity.rs:3136`,
  `:3241`.
- **Conformance:** [CF-13/a to CF-13/c](conformance.md#cf-13)
- **Revision 2026-10-28 (oracle, batch E3d):** the evaluation-error bullet
  said only that the step fails with the `EarlyStopError` object; it now
  says where that object sits (carried by the failed step, the flow's
  result) and gives the recorded `<cause>` form (fixture
  `conformance/flows/control-flow/cf-13`, run `b`).

<a id="cf-14"></a>
### CF-14 · `branchone`

**Scope:** core (empty branch at step 0: later) · **Confidence:** high (order, scope, result, error shape, empty-branch quirk; oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Predicate order.** Branch `expr`s are evaluated **in array order**,
    each as `Boolean(<expr>)`, so JS truthiness applies. The first truthy
    branch is chosen and later predicates are **not evaluated**. If none is
    truthy, `default` runs. An empty or absent `expr` is never truthy.
  - **Predicate scope:** `flow_input` = the enclosing flow's;
    `result` and `previous_result` = the previous step's result, or the flow
    input at step 0; `results.<id>`, where the previous step id
    short-circuits; `resume`, `resumes`, `approvers`; `flow_env`.
  - **Predicate errors.** If a predicate throws, evaluation stops and the
    branchone **step fails** in place with
    `{"error": {"name": "Anyhow", "message": "Error: <engine message> @<source-file>:<line>:<col>"}}`,
    where `<engine message>` is `QuickJS evaluation error: Error: <message>`
    followed by the engine's frame lines. Recorded at step 1:
    `Error: QuickJS evaluation error: Error: nope is not defined`, two frame
    lines, then ` @worker_flow.rs:2451:11` (batch E3a, fixture
    `conformance/flows/je/branches`, run `je31-a`). The suffix depends on
    the build; compare with it masked. Ordinary failure handling applies
    (`continue_on_error`, `retry`, `failure_module`, an enclosing loop's
    `skip_failures`).
  - **Result.** The chosen branch runs as a child flow; the branchone result
    is that child's result (the branch's last module). A `stop_after_if` on
    the branchone module is evaluated with that result.
  - **Empty chosen branch:** no child runs, and the result passes through the
    `previous_result` handed to the step. When the branchone is the first
    step of its flow and no prior result was supplied, the pass-through is
    the JSON **string** `"{}"` (recorded; [Q-CF-4](#q-cf-4)). Later `results.<id>`
    lookups of an empty-branch branchone depend on the lookup route
    ([CF-19](#cf-19)).
  - Branch path indexes: `default` is index 0 and branch i is index i+1
    ([CF-24](#cf-24)).
- **Evidence:** `worker_flow.rs:5418-5530` (predicate loop, `StepFailure`,
  branch index), `:2427-2472` (`Boolean(...)` wrapping; results by id),
  `:4053-4080` (step failure routed through normal completion),
  `:3197-3221`, `:2957-3003` (empty branch: module already `Success`, result
  = last result or the `"{}"` string);
  `backend/windmill-types/src/flows.rs:841` (`expr` default `""`); tests
  `backend/tests/worker.rs:3513`, `:3551`, `:3759`,
  `backend/tests/flow_engine_parity.rs:252`.
- **Conformance:** [CF-14/a to CF-14/c](conformance.md#cf-14)
- **Note:** a multiline predicate can be answered by its first qualifying
  line through direct lookup ([CF-V6](#cf-v6)).
- **Revision 2026-10-28 (oracle, batches E3d and E3a):** the predicate
  error's message form is now stated as recorded, and the step-0
  empty-branch quirk is recorded (CF-14/c gives the JSON string `"{}"`);
  both were medium confidence before. The rule is unchanged.

<a id="cf-15"></a>
### CF-15 · `branchall`

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - Branch `expr`s are **ignored**.
  - **Parallel** (the default; an absent `parallel` counts as true,
    [CF-6](#cf-6)): every branch starts at once as its own child flow, with
    no concurrency cap. The step completes when all finish; the result is an
    array in **branch order**.
  - **Parallel failure:** the step fails if any branch whose `skip_failure`
    is not true failed; the result is then the full array, failed entries
    being error objects. The check reads persisted child status, so a branch
    that recovered through its handler counts as success even with
    `skip_failure: false` ([CF-V1](#cf-v1)).
  - **Sequential (`parallel: false`):** branches run in order. After a branch
    fails, the next starts only if *that* branch has `skip_failure: true`;
    otherwise the step fails with the array of results so far and the
    remaining branches never run. Sequential branchall uses the propagated
    child failure, so a recovered branch counts as failed.
  - **`stop_after_if` on a branchall module** is not evaluated for a
    branchall that created children; use `stop_after_all_iters_if`
    ([CF-13](#cf-13)). A branchall with **zero branches** is not active, so
    its `stop_after_if` *is* evaluated with `result = []` and can stop the
    enclosing flow ([CF-V2](#cf-v2), [CF-29](#cf-29)).
  - A body-level early stop follows [CF-12](#cf-12).
  - **Empty branch** (`modules: []`): a child flow with no steps still runs;
    its result is its own input, the enclosing `flow_input` object
    (recorded; [Q-CF-4](#q-cf-4)).
  - **Stored status.** A completed branchall's entry in the flow status has
    no `branchall` field (the field that tracks the running branch), so a
    reader of a completed status takes the branch structure from the flow
    definition; see [OR-11](oracle.md#or-11) for how the oracle harness
    walks it.
  - **No branches:** result `[]`.
- **Evidence:** `worker_flow.rs:5532-5655` (parallel: all payloads;
  sequential: next branch; empty branches still produce a child),
  `:3197-3205` (a flow with no modules → result = its args), `:748-757`,
  `:1173-1192` (sequential skip rule), `:940-966`, `:940-1035` (parallel:
  skipped branches excluded; persisted-status check), `:682` (no
  `stop_after_if` for branchall), `:3998-4052`;
  `backend/windmill-types/src/flow_status.rs:233-300` (only the in-progress
  status entry has the `branchall` and `iterator` fields); tests
  `backend/tests/worker.rs:3589`, `:3629` (`[[1,2],[1,3]]`), `:3677`,
  `:3818` (nested branchall shape), `:591` (`[[42]]`).
- **Conformance:** [CF-15/a to CF-15/f](conformance.md#cf-15)
- **Revision 2026-10-28 (oracle, batch E3d):** every case is recorded as
  stated, CF-15/a and CF-15/b with `parallel` and the skip flags absent as
  pushed (deployed through the API, so the defaults of [CF-6](#cf-6)
  applied). The stored-status bullet is new: the recordings showed it.

<a id="cf-16"></a>
### CF-16 · `failure_module` inside containers

**Scope:** core · **Confidence:** high (routing, recovery split); high (oracle-observed: the stored-node handler id); medium (the inline handler id; oracle pending) · **Status:** confirmed

- **Rule:**
  1. **Where the handler runs.** Each child flow carries a copy of the flow's
     handler ([CF-1](#cf-1)). A failure inside a child runs it **inside that
     child**, once for each child flow whose failure reaches the handler.
     Because a flow with a `failure_module` never collapses a loop
     ([CF-8](#cf-8)), every loop body in such a flow is a child flow.
  2. **When it does not run:**
     - for a body step whose failure is absorbed by its own
       `continue_on_error`;
     - for attempts still being retried (it runs only after the step's
       retries are exhausted; see [execution.md](execution.md));
     - for an error escaping a child flow's startup, which completes the
       child as failed without running its handler ([CF-V3](#cf-v3)).
  3. **What it sees.** `flow_input` = the child's input, including `iter`;
     `previous_result` and `error` = the failed step's error. Its output
     becomes that child's result entry.
  4. **No second run.** Outer levels do not run the handler again for the
     same failure, except across a by-path subflow boundary, where the
     caller's handler runs too ([CF-17](#cf-17)).
  5. **How the container counts it** ([CF-V1](#cf-v1)):
     - sequential loops, sequential branchall and branchone: a child that
       ended in its handler counts as **failed**, even if the handler
       returned `{"recover": true}`;
     - parallel loops and parallel branchall: recovered children count as
       **succeeded**.
  6. **Handler id.** An inline child's handler step id is suffixed
     (`<id>-<step>-<index>` for loops and branchall, `<id>-<step>` for
     branchone). A stored-node child, the normal deployed form, keeps `<id>`
     ([CF-V4](#cf-v4)); on the oracle a deployed loop's copies ran as
     `failure` ([FX-34](execution.md#fx-34)).
  7. **`error` after a failed container.** A failure module that runs after
     a failed container sees an `error` binding derived from the array: a
     single error entry yields that entry's `error`; several yield
     `{"name": "MultipleErrors", "message": "[<step_id|index>] <msg>; …", "errors": <array>}`
     (see [expressions.md](expressions.md)).
- **Evidence:** `worker_flow.rs:5038-5074` (handler copied into children),
  `:940-1035`, `:1018-1035`, `:1138-1192`, `:1734-1757` (a failed step with
  a handler continues into it), `:2006-2047`, `:2135-2150` (the child reports
  `success && !is_failure_step`), `:1082-1092` (parallel: no second run at
  the container); `backend/windmill-worker/src/result_processor.rs:1939-1978`;
  `backend/windmill-worker/src/worker_lockfiles.rs:1554-1591`;
  `backend/windmill-jseval/src/lib.rs:495-524` (`error` binding over arrays).
- **Conformance:** [CF-16/a to CF-16/d](conformance.md#cf-16)
- **Note:** handler retry and unrecoverable-error exceptions belong to
  [execution.md](execution.md).
- **Revision 2026-10-28 (oracle, batch E3c):** point 6 was right; execution's
  FX-34 had said every copy is suffixed and is now corrected to match. The
  stored-node id was recorded by fixture `conformance/flows/execution/fx-34`
  (run `step_id`, oracle run 40689442792), as was point 1's once-per-failing
  iteration run (run `loop`). The CF-16 cases themselves are not recorded.

<a id="cf-17"></a>
### CF-17 · Subflows (`type: flow`, by path)

**Scope:** core (scheduler overrides: out) · **Confidence:** high (inputs, result, scope); medium (`recover` across the boundary) · **Status:** confirmed

- **Rule:** A `flow` step:
  - **Resolution.** Resolves `path` to the **latest deployed version** when
    the step starts and runs it as a child job of kind `Flow`. Its inputs are
    evaluated from `input_transforms` exactly like a script step, and
    `pass_flow_input_directly` is honoured.
  - **Independence.** The subflow runs as an independent flow with its own
    `flow_input` (the evaluated inputs), its own `failure_module`, its own
    `flow_env` (or the parent's when it defines none), its own results scope
    ([CF-19](#cf-19)), and its own flow-level settings (`same_worker`,
    cache, priority).
  - Its `preprocessor_module` is **not applied**.
  - **Result.** The step's result is the subflow's final result, including a
    result produced by the subflow's own `early_return`.
  - **Failure.** If the subflow fails, the step **fails** with the subflow's
    final result as its error result: an `{"error": …}` object, a failed
    container's array ([CF-5](#cf-5)), or the subflow's own failure-module
    output. The parent's `continue_on_error`, `retry` and `failure_module`
    then apply; the parent's handler runs even if the subflow's handler
    already ran.
  - **`recover`.** A subflow whose handler returned `recover: true` is still
    a failed step for the parent ([CF-V1](#cf-v1)).
  - **Early stop.** A subflow's early stop does not stop the parent
    ([CF-12](#cf-12)).
  - **Start failures.** An unknown path fails the step at start
    ([CF-23](#cf-23)); an error at pickup of the subflow's first step
    follows [CF-V3](#cf-v3).
  - **Scheduling.** The subflow job's scheduler fields (tag, priority) can be
    overridden by the calling step and flow; that is scheduling, scoped
    `out`.
- **Evidence:** `worker_flow.rs:5138-5154`, `:6020-6049` (`flow_to_payload`:
  latest version, owner and tag of the subflow), `:3931-3964`, `:4318-4330`
  (new results scope), `:2795-2808` (own or inherited env), `:498-500`
  (handler flag reset for `flow` steps), `:599-626`, `:2006`, `:2135-2150`,
  `:4140-4150` (caller scheduling overrides);
  `backend/windmill-queue/src/jobs.rs:5912-5980` (preprocessor dropped,
  `:5937`); tests `backend/tests/flow_engine_parity.rs:2584`, `:2664`,
  `:2771` (subflow `flow_env` own and inherited).
- **Conformance:** [CF-17/a](conformance.md#cf-17)
- **Note:** the subflow job is a `Flow` job whatever the caller's kind, so
  an eligible forloop inside it collapses even when the caller is a raw
  submission ([CF-8](#cf-8)).

<a id="cf-18"></a>
### CF-18 · `identity`, skipped containers, mocked containers

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **`identity`** returns `previous_result` unchanged; at step 0 that is the
    flow input. The `flow` boolean in the OpenAPI `Identity` schema is
    ignored.
  - **Skipped container.** A container whose `skip_if` is truthy runs as an
    identity: no iteration, no predicate, no branch. Its result is
    `previous_result`, not `[]`.
  - **Mocked container.** A container with an enabled `mock` returns
    `mock.return_value`, or `null` when it is absent, without running
    anything.
  - A container's `skip_if` is evaluated *before* the mock check, so a
    throwing `skip_if` surfaces even on a mocked container (the `skip_if`
    error path is in [execution.md](execution.md)).
- **Evidence:** `backend/windmill-worker/src/worker.rs:4032-4038` (identity
  result = the `previous_result` arg, or `{}` when absent);
  `worker_flow.rs:3919-3930` (identity args), `:5106-5137` (mock and skip
  short-circuit before the container logic), `:3842-3866` (skip evaluated
  first), `:3874-3890` (mock args); test `backend/tests/worker.rs:307`.
- **Conformance:** [CF-18/a](conformance.md#cf-18)

<a id="cf-19"></a>
### CF-19 · How `results.<id>` resolves across container boundaries

**Scope:** core (routes, syntax, scopes, outer visibility, container ids, `null` vs error); later (last-iteration, caller-leak and stale-iteration cases; nil-job fetch-error text) · **Confidence:** high (routes, line anchoring, scopes, the empty-branchone cases and the nil-job fetch-error text: oracle-observed); medium (leak and stale-iteration cases) · **Status:** confirmed

- **Rule:**
  - **Routes.** An expression is resolved by the first route that applies:
    1. **Whole-expression context name.** The whole expression is exactly a
       context name such as `previous_result` and resolves to that value
       ([expressions.md](expressions.md)).
    2. **Exact flow input or env property.** The whole expression is exactly
       `flow_input.<k>`, `flow_input["<k>"]` or the `flow_env` equivalent,
       and resolves directly.
    3. **Previous-step shortcut.** The whole expression is exactly
       `results.<p>`, `results?.<p>`, `results["<p>"]` or
       `results?.["<p>"]`, where `<p>` is the previous step's id; it resolves
       to `previous_result`. The previous-step id is empty at step 0 of a
       child flow and inside collapsed loop bodies.
    4. **Direct lookup, line-anchored.** The (trimmed) expression text is
       searched for a **line** that is exactly `results` (or `results?`),
       then `.` and a step id of `[A-Za-z0-9_]`, then **at most one decimal
       index `[n]` directly after the id**, then zero or more `.segment`s of
       `[A-Za-z0-9_]`. A line is a stretch between line feeds or the ends of
       the text; a line ending in a carriage return does not qualify. Only
       the **first** qualifying line counts:
       - if its last segment is `length`, direct lookup is skipped and the
         whole expression goes to route 5, even when a later line would
         qualify;
       - otherwise the value is read from the scoped leaf-result map with
         that line's path and returned, and **the rest of the expression
         text is ignored**; a miss or any lookup failure gives JSON `null`.

       Examples:
       - single lines that qualify: `results.X`, `results.X.y.z`,
         `results.X[2]`, `results.X[2].y`;
       - single lines that do not: `results.X.y[2].z`, `results.X?.y`,
         `results["X"].y`, `results.X.length`, `results.X + 1`;
       - with `a` returning `7`, the two-line expression `results.a`, a
         newline, `+ 100` gives `7`; the one-line `results.a + 100` goes to
         the engine and gives `107`.
    5. **JS engine.** Everything else is evaluated by the engine. Each
       `results.X` inside resolves lazily: to `previous_result` if X is the
       previous step's id; else to the stored result of X if X is one of the
       evaluating flow's own steps with a job (all child results for loops
       and branchall), throwing `Failed to fetch result for step 'X': <cause>`
       if that fetch fails; else to the leaf-result map, with a miss giving
       `null`. Ordinary JS semantics follow: reading a property of `null`
       throws, and the input transform fails under the transform-error
       contract in [expressions.md](expressions.md).
  - **Leaf-result map and scopes.** Scopes are the top-level flow, each
    by-path subflow child, and each **parallel** forloop iteration. All other
    child flows (sequential and while iterations, branch flows) write into
    their parent's scope. When any step completes, its scope's map entry for
    that step id is overwritten with its job(s). A lookup checks the current
    scope, then the parent job's scope, and so on to the top.
  - **Consequences:**
    - **Outer steps from inside.** A step outside a container is visible
      from inside it at any depth, including parallel iterations and
      subflows. A subflow can read its caller's steps when it has no step of
      that id (a leak; [Q-CF-12](#q-cf-12)).
    - **Branch steps after the container:** visible (chosen branch only;
      otherwise `null`).
    - **Sequential non-collapsed loop or while-loop steps after the loop:**
      visible, holding the last iteration's value.
    - **Steps inside a parallel iteration, a collapsed body or a subflow,
      seen from outside:** `null`, unless another visible step or ancestor
      has that id. A missing registration falls through; it does not mask a
      same-named visible step.
    - **Containers.** `results.<container id>` is its result: an array for
      loops and branchall (`results.<loop>[i]` selects iteration i), the
      chosen branch's result for branchone.
    - **Earlier iterations.** Inside a sequential loop, a body step that has
      not run yet in this iteration can resolve to the previous iteration's
      value.
    - **Unknown id:** `null`.
    - **Missing step:** `results.missing[2].z` gives `null` (route 4), while
      `results.missing.y[2].z` makes the transform fail (route 5).
    - **Empty-branch branchone `br` whose predecessor returned `42`:**
      directly after it, `results.br` gives `42` (route 3) and
      `[results.br]` gives `[42]` (route 5, via `previous_result`). At any
      later step, `results.br` gives `null` (route 4, nil job) while
      `[results.br]` throws a fetch error (route 5, own step with a nil job).
      The nil job is the all-zero job id, and the fetch fails with the
      server's not-found text, so the transform error reads
      `Failed to fetch result for step 'br': Not found: Completed Job not found at name 00000000-0000-0000-0000-000000000000 (<file>.rs:<line>)`
      inside the transform-error envelope of
      [expressions.md](expressions.md) (recorded with `(jobs.rs:9074)`, a
      source location of the server that depends on the build).
- **Evidence:** `backend/windmill-worker/src/js_eval.rs:36-85` (trimming,
  then routes 1 to 4 in order); `backend/windmill-jseval/src/lib.rs:73-76`
  (direct-lookup syntax, multi-line anchors), `:180-217` (first match only,
  direct lookup, `null` on miss), `:168-178`, `:189-194` (`length`
  bypass), `:670-760`, `:697-758` (own-step fetch throws; fallback `null`
  at `:741-750`), `:806-816` (engine previous-id shortcut);
  `backend/windmill-common/src/utils.rs:338-349` (the not-found text with
  the caller's location), `backend/windmill-common/src/error.rs:31-32`
  (`Not found: `), `backend/windmill-api/src/jobs.rs:9074` (the completed-job
  lookup that fails);
  `worker_flow.rs:6155-6172` (own-step context), `:1458-1474` (map write
  keyed by step id into the scope root), `:4318-4330` (scope roots for
  `flow` and parallel forloop children), `:3197-3221`, `:3659-3664`;
  `backend/windmill-queue/src/jobs.rs:4403-4455`, `:4559-4605` (lookup
  walking parent scopes), `:4708-4775`, `:4760-4775` (nil or absent single
  job → `null`); tests `backend/tests/worker.rs:591` (outer step from two
  levels of branchall), `backend/tests/flow_engine_parity.rs:1860`, `:2005`
  (unknown id → `null`).
- **Conformance:** [CF-19/a to CF-19/c](conformance.md#cf-19); the line
  anchoring is exercised by [CF-V6/a and CF-V6/b](conformance.md#cf-v6).
- **Design note (not Windmill behaviour):** the in-harness segment executor
  needs this scoped map, or an equivalent, to reproduce `null` versus error
  inside and after containers.
- **Revision 2026-10-28 (oracle, batch E3a):** the finding was `DISPUTED`.
  Its draft anchored route 4 to the whole expression ("and nothing else");
  the validation's correction ([CF-V6](#cf-v6)) said the matcher is
  line-anchored. The oracle recorded the correction: CF-V6/a's two-line
  expression gave `7` and its one-line control CF-V6/b gave `107` (fixture
  `conformance/flows/cf/cf-19`, oracle run 40687124625), which agrees with
  [JE-9](expressions.md#je-9). Route 4 now states the line-anchored rule.
  CF-19/a to CF-19/c are recorded as stated, and the nil-job fetch-error
  text, which was open ([Q-CF-5](#q-cf-5)), is added.

<a id="cf-20"></a>
### CF-20 · Values crossing container boundaries are JSON copies

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** Every child (iteration, branch, subflow, body step) receives its
  inputs as persisted JSON, and every container reads its children's results
  back from persisted JSON. So no aliasing exists between sibling branches or
  iterations; mutating an input inside one branch never affects another; and
  non-JSON values do not survive (normalization details are in
  [execution.md](execution.md) and [languages.md](languages.md)).
- **Evidence:** `worker_flow.rs:4268-4280` (each child's args serialized
  before the push), `:2299-2327` (results re-read from storage); test
  `backend/tests/worker.rs:3629` (two parallel branches each append to
  `results.a` = `[1]` and get `[1,2]` and `[1,3]`).
- **Conformance:** [CF-20/a](conformance.md#cf-20)
- **Design note (not Windmill behaviour):** equivalence needs a cheap
  normalising JSON copy at body entry and at result collection.

<a id="cf-21"></a>
### CF-21 · Evaluation timing of container-level fields

**Scope:** later · **Confidence:** high (once/per-child split); low (mid-loop `skip_if`) · **Status:** confirmed

- **Rule:**
  - **Once, at container start:** the `iterator`, the `branchone`
    predicates, and the `sleep` of the *preceding* step (the delay applies
    before the first child only).
  - **Per child pushed:** `timeout`, evaluated in step scope and assigned to
    each child job (iteration flow, collapsed body job, branch flow or
    subflow); `parallelism`, re-evaluated per child. Where the assigned
    `timeout` is actually enforced is [CF-27](#cf-27).
  - **`skip_if` on a sequential loop module is re-evaluated before every
    iteration.** For iterations after the first, `previous_result` is the
    prior iteration's result. If it turns truthy mid-loop, the loop step ends
    as a skipped identity whose result is that `previous_result`, and the
    partial array is lost ([Q-CF-6](#q-cf-6)).
- **Evidence:** `worker_flow.rs:3666-3715` (sleep only while the step waits
  to start), `:4379-4400` (timeout per pushed child), `:4484-4499`
  (parallelism per child), `:3842-3866` (`skip_if` evaluated on every
  entry, including continuing iterations), `:5136` (identity/next step
  replaces the active loop state).
- **Conformance:** [CF-21/a](conformance.md#cf-21)
- **Note:** run the fixture by path with the loop at top level (root kind
  `flow`), where the loop collapses ([CF-8](#cf-8)).

<a id="cf-22"></a>
### CF-22 · `retry` on a container module

**Scope:** later · **Confidence:** medium (re-run from scratch, restoration); low (`iter_parent` artefact; oracle pending) · **Status:** confirmed

- **Rule:**
  1. **Re-run.** A failed container step with `retry` re-runs from scratch
     after the backoff: the iterator or predicates are re-evaluated and all
     children re-run.
  2. **`previous_result` on retry** is restored to the preceding step's
     stored result, or the flow input when the container is step 0. If the
     preceding step did not succeed (for example it failed under
     `continue_on_error`), the restored value is the JSON **string** `"{}"`.
  3. **Child inputs on retry** come from the **last failed child job's**
     stored input, not from a fresh computation.
     - **Non-collapsed body:** that stored input is the old child's flow
       input, which already contains `iter`, so the re-run children see the
       failed child's `iter` moved to `flow_input.iter_parent`.
     - **Collapsed body:** the stored input is the body script's transformed
       arguments, so no `iter_parent` artefact arises from the loop input.
       (A body's transformed arguments can still contain an `iter` key the
       user put there deliberately.)
- **Evidence:** `worker_flow.rs:3720-3790`, `:3723-3790` (retry: reset,
  restoration; `"{}"` fallback at `:3768-3784`), `:3737`, `:3891-3908`
  (input from the last failed job), `:4158-4170` (`iter` insertion renames
  the existing `iter`), `:6234-6262` (only successful preceding steps are
  retrieved).
- **Conformance:** [CF-22/a](conformance.md#cf-22)

<a id="cf-23"></a>
### CF-23 · Errors raised while starting a container

**Scope:** core (that the container fails; the message text); later (route-dependent error name and handler bypass) · **Confidence:** high (routing; the route 1 and route 3 forms, oracle-observed); low (the parent's recorded value on route 3) · **Status:** confirmed

- **Rule:** Errors raised while preparing a container's children are an
  iterator that is not an array or throws ([CF-2](#cf-2)), a `parallelism`
  that is not a u16, an unknown subflow path, and `parallel` with active
  `same_worker` ([CF-7](#cf-7)). They take one of three routes, depending on
  where they are raised:
  1. **Root flow at pickup.** A flow job with no parent being started by a
     worker: its first step, or the step after an approval/suspend resume.
     The error is recorded as a failure of the current step,
     `{"error": {"name": "Anyhow", "message": "Error: <message> @<source-file>:<line>:<col>"}}`,
     and that flow's ordinary step-failure handling applies
     (`continue_on_error`, `retry`, `failure_module`).
  2. **Child flow at pickup** (iteration, branch and by-path subflow jobs).
     The child job is completed as failed with that error object, and its
     **own** retry/continue/handler machinery is bypassed. Its parent then
     receives an ordinary failed child ([CF-V3](#cf-v3)).
  3. **During chaining.** The container is started right after an earlier
     step of the same flow completed. The flow job **aborts**: it completes
     as failed with `{"error": {"name": "InternalError", "message": "<message>"}}`,
     and that flow's `continue_on_error`, `retry` and `failure_module` do not
     run. If it is a child, its parent sees a failed child; for loops and
     branchall the entry is the stored error. For a branchone or subflow
     step, the parent may record the last successful inner result instead of
     the error ([Q-CF-3](#q-cf-3)). The abort persists `InternalError`, but
     the notification to the parent still carries the prior result value, so
     the stored child result and the value delivered to the parent must not
     be conflated.

  `branchone` predicate errors are the exception: they always become a
  failure of the branchone step ([CF-14](#cf-14)).
- **Evidence:** `backend/windmill-worker/src/worker.rs:3822-3840`;
  `backend/windmill-worker/src/result_processor.rs:1910-1978` (root:
  failed-step update on itself; child: complete, then notify the parent);
  `worker_flow.rs:2086-2121` (error while chaining → `InternalError`),
  `:2135-2150`, `:4053-4080` (`StepFailure` path for predicates);
  `backend/windmill-common/src/error.rs:145-150`;
  `backend/windmill-common/src/worker.rs:2228-2232`; test
  `backend/tests/worker.rs:5468` (an inner iterator error inside a while body
  fails the outer flow).
- **Conformance:** [CF-23/a to CF-23/c](conformance.md#cf-23)
- **Note:** the oracle recorded both forms (batch E3d,
  [CF-2/b](conformance.md#cf-2) and [CF-7/d](conformance.md#cf-7)). Route 1
  gave name `Anyhow` and `Error: <message> @worker.rs:3822:13`, where
  `<message>` is the raised error's own text (`Bad request: …` for the
  `same_worker` error); route 3 gave `InternalError` with the plain message.
  The route 1 wrapper adds one `Error: ` prefix and one suffix to whatever
  error escapes. An error that is itself an `Anyhow` value, such as a failed
  expression evaluation, therefore shows two prefixes and two suffixes at
  pickup ([JE-35](expressions.md#je-35)). The suffixes depend on the build;
  compare with them masked.

<a id="cf-24"></a>
### CF-24 · Child job paths and step ids

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:** These values reach scripts as job-path and step-id environment
  values ([languages.md](languages.md)).
  1. **Child flow paths**, from the enclosing flow path `P`:
     - sequential or while iteration `i`: `P/loop-<i>`;
     - parallel iteration `i`: `P/forloop-<i>`;
     - `branchone`: `P/branchone-0` for the default branch,
       `P/branchone-<i+1>` for branch i;
     - `branchall` branch i: `P/branchall-<i>`.

     These hold for inline and stored-node children alike.
  2. **Job path of a step that runs a script** depends on the module type,
     for collapsed loop bodies and ordinary steps alike:
     - `script` (by path or hash): **the referenced script's own path**; no
       synthesized path is used;
     - `rawscript` with an explicit `path`: that path;
     - `rawscript` without a path, and every `flowscript`: a synthesized
       path. A collapsed loop body gets `P/loop-<i>`; an ordinary step gets
       `<its flow's path>/<module id>`, e.g. `P/loop-0/b`.

     The lite form stores inline scripts as `flowscript` without a `path`,
     so an explicit `rawscript` path survives only where the selected stored
     form still has `rawscript` modules: raw runs, runs before
     normalization, and full-version runs (a full version can be selected
     even after normalization when lite use is disabled). Record the stored
     form actually used rather than inferring it from dependency success.
  3. **Step ids.** A child flow job's flow step id is the container's id; a
     collapsed body job's step id is the loop's id.
- **Evidence:** `worker_flow.rs:5908-5983` (collapsed body: script lookup
  `:5928-5937`, explicit rawscript path `:5949-5963`, flowscript
  `:5965-5976`), `:5188-5225` (ordinary steps; `:5199`, `:5217-5225`),
  `:5076-5086` (`get_path`), `:5374`, `:5511`, `:5553`, `:5626`, `:5670`
  (child paths), `:4429` (step id);
  `backend/windmill-worker/src/worker_lockfiles.rs:1612-1643` (lite
  conversion drops `path`); `backend/windmill-queue/src/jobs.rs:5915-5930`.
- **Conformance:** [CF-24/a to CF-24/e](conformance.md#cf-24)

<a id="cf-25"></a>
### CF-25 · `squash`

**Scope:** EE · **Confidence:** high (CE behaviour); low (EE semantics) · **Status:** confirmed

- **Rule:** `squash: true` on a sequential loop asks Windmill to run all
  iterations on one dedicated runner, an Enterprise feature. In the CE build,
  starting such a loop (non-empty, sequential, actually starting runners)
  fails with `Squashing flow loops is not available in OSS`. Empty loops start
  no runners, and parallel loops do not take the squash start path. Whether
  squash changes data semantics in EE (for example module state shared
  across iterations) is unknown ([Q-CF-11](#q-cf-11)).
- **Evidence:** `worker_flow.rs:5265`, `:5336`, `:4080-4095`, `:4800-4830`;
  `backend/windmill-worker/src/dedicated_worker_oss.rs:5-26`; public docs
  <https://www.windmill.dev/docs/flows/flow_loops> ("Squash … all iterations
  run on the same worker", "can't be combined with parallel").
- **Conformance:** [CF-25/a, CF-25/b](conformance.md#cf-25)

<a id="cf-26"></a>
### CF-26 · Representation and restart details

**Scope:** core (virtual identity, handler id, script path, job kind); out (restart, storage, worker tags) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  1. **Behaviour-neutral details:** storing iterator arrays out of line; the
     per-child success/duration timeline (`flow_jobs_success`,
     `flow_jobs_duration`); the virtual identity step `<id>-v` added when a
     body's last step has `sleep`/`suspend`, whose result equals the
     previous result. `flowscript` is the stored form of an inline script in
     the lite version (see [openflow.md](openflow.md)).
  2. **Not behaviour-neutral: stored flow nodes** (`modules_node`,
     `default_node`). In the deployed lite form, non-simple, non-empty bodies
     are stored as flow nodes. This changes:
     - the copied handler's step id ([CF-V4](#cf-v4));
     - `preserve_step_tags` inheritance (scheduling, `out`);
     - the explicit `path` of inline scripts ([CF-24](#cf-24));
     - the **job kind** of the body ([CF-V5](#cf-v5)): a nested forloop with
       a simple body collapses inside a stored node (`FlowNode`) and not
       inside an inline child (`FlowPreview`), so for nested loops the stored
       form changes everything collapsing changes ([CF-8](#cf-8)): the
       `previous_result` chain, ignored body fields, step ids, result
       registration and timeout enforcement. This is core.

     Input and result scoping are unchanged.
  3. **Restart-from-step** (`restarted_from`, `branch_or_iteration_n`, branch
     locking, reuse of the stored iterator) changes data behaviour: it
     selects iterations and locks the originally chosen branch. It is out of
     scope by **scope decision** (windmill-lite runs flows; it does not
     resume Windmill runs), not because restart has no semantics.
- **Evidence:** `worker_flow.rs:5009-5036`, `:5418-5434`, `:5839-5881`,
  `:4613-4635`, `:5038-5050`; `backend/windmill-types/src/flows.rs:1252-1280`;
  `backend/windmill-types/src/flow_status.rs:62-80`;
  `backend/windmill-worker/src/worker_lockfiles.rs:1554-1591`, `:1612-1643`;
  `backend/windmill-queue/src/jobs.rs:5604-5667`, `:5420-5440`.
- **Conformance:** [CF-26/a](conformance.md#cf-26); stored-node effects are
  covered by [CF-V4](conformance.md#cf-v4), [CF-24](conformance.md#cf-24)
  and [CF-8/b](conformance.md#cf-8).

<a id="cf-27"></a>
### CF-27 · Container `timeout` enforcement

**Scope:** later · **Confidence:** medium (ordinary worker path); low (other execution modes) · **Status:** confirmed

- **Rule** (ordinary, non-dedicated worker path):
  1. **Where a job timeout is enforced:** by the process supervisor of script
     executors, and by the native/SQL/AI executors' own timers.
  2. **Flow-kind jobs are not timed.** Jobs of kind `Flow`, `FlowNode` and
     `FlowPreview` go straight to the flow engine, which has no wall-clock
     timer. A flow job's own `timeout` is not handed down to its steps; each
     step evaluates its own `timeout`.
  3. **Effect of a container module's `timeout`:** it bounds **each collapsed
     body job** separately when the fast path applies ([CF-8](#cf-8)). It has
     no effect on child flows: non-collapsed iterations, branches, subflow
     jobs. Whether a loop collapses depends on job kind ([CF-V5](#cf-v5)).
  4. **Error shape.** A timed-out Bun body job fails with
     `{"error":{"name":"ExecutionErr","message":"execution error:\njob process terminated due to timeout after exceeding job-specific duration limit","step_id":"<loop id>"}}`
     when the custom timeout is strictly below the instance maximum.
     Otherwise the instance maximum applies, a warning is logged, and the
     message ends with `instance-wide job duration limit`.
  5. **Not covered:** EE dedicated runners, `squash`, and agent workers
     ([Q-CF-8](#q-cf-8)).
- **Evidence:** `backend/windmill-worker/src/worker.rs:3815-3840` (flow kinds
  go to the flow engine); `backend/windmill-worker/src/handle_child.rs:165-232`
  (timer race and kill reasons; `:172-180`, `:199-229`), `:335-347`
  (`job process terminated due to …`), `:200`, `:724`, `:803`;
  `backend/windmill-worker/src/common.rs:1056-1123` (job-specific vs instance
  cap); `backend/windmill-worker/src/result_processor.rs:587-627`
  (`execution error:` prefix, `ExecutionErr`, `step_id`);
  `backend/windmill-worker/src/bun_executor.rs:2418-2435`; native executors
  `backend/windmill-worker/src/graphql_executor.rs:84`,
  `backend/windmill-worker/src/bigquery_executor.rs:372`,
  `backend/windmill-worker/src/snowflake_executor.rs:750`,
  `backend/windmill-worker/src/ai_executor.rs:981`;
  `worker_flow.rs:4379-4400`, `:4429`; `backend/src/monitor.rs` (only the
  default-timeout setting and zombie handling).
- **Conformance:** [CF-27/a to CF-27/d](conformance.md#cf-27)
- **Note:** the "no enforcement on flow jobs" conclusion rests on a search of
  timeout consumers on the ordinary path; it is not extended to other
  execution modes. The positive-control fixtures need inner steps whose
  effective default or instance deadlines exceed three seconds. The timeout
  findings [FX-24](execution.md#fx-24) and [FX-25](execution.md#fx-25)
  should be read with the same job-kind distinction.

<a id="cf-28"></a>
### CF-28 · Cancellation inside containers

**Scope:** later · **Confidence:** high (marking, pickup and root result); low (timing, deliberately unspecified) · **Status:** confirmed

- **Rule:**
  1. **Normal cancel of a running flow.** The flow and every queued or
     running descendant, found through parent links at any depth up to 500
     levels, get cancellation markers. Holds created by `parallelism` are
     cleared and those jobs are scheduled immediately.
  2. **Queued or held descendants.** When picked up, they complete at once
     as failed with the cancellation error, **without running body code**.
     Their parents then see failed children.
  3. **Running script descendants.** Their worker notices cancellation by
     polling and kills the process. If the process exits first, its normal
     result, success included, is reported. No ordering between cancel and
     completion is guaranteed.
  4. **Flows in the tree** (active flow completion):
     - a cancelled flow completes when its outstanding children have
       reported; for a parallel container, when the last one does;
     - it starts no further steps, iterations or branches, with one
       exception: an error stop for which the flow has a failure handler is
       checked *before* the cancel guard, so the flow can still continue into
       its handler;
     - on this ordinary route, the cancelled flow's result is
       `{"error":{"name":"Canceled","message":"Job canceled: <reason> by <canceler>","reason":"<reason>","canceler":"<canceler>"}}`;
       absent values are `no reason given` (reason) and `unknown`
       (canceler).
  5. **Direct completion.** A **force cancel** (which first climbs to the
     topmost queued parent) and a root job that is not yet running are
     completed directly with a similar `Canceled` object (default reason
     `unexplicited reasons`), not through the flow's aggregation. The
     outstanding-child wait of point 4 does not apply to these routes or to
     cancelled-at-pickup jobs.
- **Evidence:** `backend/windmill-queue/src/jobs.rs:205-250`, `:212-247`,
  `:296-309` (direct completion for force cancel or a not-running root),
  `:264-394`, `:330-394` (descendants marked, holds cleared), `:883-918`,
  `:6896-6903` (cancellation result), `:1095-1129` (completion uses the
  worker's cancellation metadata);
  `backend/windmill-worker/src/worker.rs:3644-3646` (cancelled job at
  pickup); `backend/windmill-worker/src/handle_child.rs:205-215`,
  `:850-956` (process exit vs cancellation polling);
  `worker_flow.rs:765-1115`, `:1734-1738`, `:1972-1988`.
- **Conformance:** [CF-28/a](conformance.md#cf-28)

<a id="cf-29"></a>
### CF-29 · An empty container's own `stop_after_if` stops the flow

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** For an empty iterator, an empty loop body, or a zero-branch
  branchall, the container's `stop_after_if` is evaluated with
  `result = previous_result = []`. When truthy it behaves like any step's
  `stop_after_if`, **not** as a loop break:
  - the enclosing flow stops after the container, with result `[]`;
  - `skip_if_stopped` gives a skipped flag;
  - `error_message` makes the flow fail with `EarlyStopError`, including
    `error.result = []` when `error_include_result` is set.

  `all_iters` is not bound (it is supplied only for an in-progress module),
  so referencing it throws. Skip-over-error precedence and handler behaviour
  are those of ordinary stop handling.
- **Evidence:** `worker_flow.rs:3998-4052`, `:3197-3221`, `:571-595`,
  `:675-735`, `:692-702`, `:1195-1209`, `:1350-1365`.
- **Conformance:** [CF-29/a](conformance.md#cf-29)
- **Note:** the zero-branch case is recorded on the oracle as
  [CF-15/d](conformance.md#cf-15): the flow stops after the branchall with
  result `[]` and the next step does not run.

<a id="cf-30"></a>
### CF-30 · The failure and preprocessor modules are slots, not list members

**Scope:** core (failure module); later (preprocessor module) · **Confidence:** high (the lite version, virtual steps and `sleep`, source-traced); medium (container-valued slots: read, not traced end to end) · **Status:** drafted

- **Rule:** a flow's `failure_module` and `preprocessor_module` are single
  modules kept beside the module list, and every transformation that works
  on module lists leaves them out:
  1. **Lite version.** The dependency job builds the lite version by
     reducing the module list only, at the top level and, recursively, in
     container bodies: inline scripts there become stored `flowscript`
     entries and non-simple bodies become stored nodes. The failure and
     preprocessor modules stay as written; an inline script keeps its
     content and lock. So an inline failure or preprocessor script runs as
     an inline-script job (kind `preview`, as the oracle shows;
     [OR-28](oracle.md#or-28)), not as a `flowscript` job. The copy of the
     failure module that a stored body node holds is also kept as written
     ([CF-V4](#cf-v4)).
  2. **No virtual step.** The trailing `<id>-v` identity of
     [OF-17](openflow.md#of-17) is added to module lists only; a `sleep` or
     `suspend` on the failure or preprocessor module never gets one.
  3. **`sleep` is never applied.** A step's delay comes from the `sleep` of
     the module just before it in the same list. The failure module is not
     in the list, and step 0 has no predecessor in it, so:
     - the `sleep` of the failure module and that of the preprocessor
       module are never evaluated: an expression there that would throw
       causes no error;
     - the `sleep` of a step that fails into the failure module is not
       applied before the handler either (it is applied when the next
       list step starts, for example under `continue_on_error`).
  4. **Container-valued slots.** If either module is a container, its
     bodies are never stored as nodes. The runtime collapse check
     ([CF-8](#cf-8)) still comes first, so:
     - **a slot that is itself a loop** is judged like any loop of the job
       executing it. A preprocessor that is a sequential forloop runs in
       the root flow job (`Flow` for a by-path run), so with a simple body
       (one inline script, no disqualifying field) and **no flow
       `failure_module`** it collapses: each iteration is dispatched as a
       script job directly, and since the body was not reduced, an inline
       body script runs as a `preview` job, not a `flowscript` one. A while
       loop slot with a simple body collapses the same way. A flow
       `failure_module` disqualifies every body, so a failure module that
       is itself a loop never collapses (the flow has one: itself);
     - **bodies that do become child flows** (a non-simple body, a
       branch's body, or any body when the flow has a failure module) are
       raw flows and run as `FlowPreview` children, so a forloop nested in
       one never collapses; a while loop nested in one still does when its
       own body is simple and the flow has no failure module.
- **Evidence:** `backend/windmill-worker/src/worker_lockfiles.rs:748-758`
  (the lite version reduces `modules` only), `:1594-1706` (the reduction
  walks module lists; virtual steps added at `:1706`), `:1553-1591` (a body
  node holds the failure module as given), `:934-998` (locking visits the
  failure and preprocessor modules without reducing them);
  `backend/windmill-types/src/flows.rs:1252-1280` (virtual step for a
  list), `:312-330` (the slots are looked up apart from the list);
  `backend/windmill-worker/src/worker_flow.rs:3658-3681` (the delay is the
  previous list module's `sleep`; none for the slots or step 0), `:5997`
  (an inline script step's payload), `:5052-5061` (an inline body is a raw
  flow), `:5298-5300` (forloop gate: job kind, `parallel`, simple body),
  `:5247-5249` (while gate: simple body only), `:5729-5745` (the simple-body
  test; the flow's `failure_module` disqualifies, nothing tests for a
  slot), `:5669-5698` (a collapsed iteration is dispatched as a script job
  before any child flow is built), `:5940-5958` (an inline script body
  becomes an inline-script payload); `backend/windmill-queue/src/jobs.rs:5477-5522`
  (inline-script jobs are `Preview`), `:5604-5667` (raw flows are
  `FlowPreview`).
- **Conformance:** [CF-30/a to CF-30/c](conformance.md#cf-30)
- **Note:** this answers questions from windmill-lite's IR compiler.
  [FX-23](execution.md#fx-23) and [JE-28](expressions.md#je-28) describe
  `sleep` on list modules.
- **Revision 2026-10-28 (validation round 1):** point 4 said that a
  container-valued slot's bodies always run as `FlowPreview` children, so
  that no forloop in a slot collapses. That confused the absence of a
  stored node with the absence of collapsing: the collapse check runs
  before any child is created and has no exclusion for the slots, so a
  slot that is itself an eligible loop collapses. The rule now separates
  the slot loop from loops nested in its child flows, and
  [CF-30/c](conformance.md#cf-30) checks the direct case.

## B. Findings added during validation

<a id="cf-v1"></a>
### CF-V1 · Recovery success differs between sequential propagation and parallel aggregation

**Scope:** core · **Confidence:** high (both paths traced; oracle pending) · **Status:** confirmed

- **Rule:** A successful failure handler returning `{"recover":true}` makes
  its own flow's persisted completion successful. The immediate notification
  to its parent still reports failure when that child finished in its
  failure step (`success && !is_failure_step`). A sequential loop,
  sequential branchall, branchone or by-path subflow step uses that
  propagated failure. A parallel loop or branchall makes its final success
  decision from the persisted completion status of its non-skipped children
  instead, so recovered children pass that check even when the relevant
  skip-failure setting is false. Its per-child success timeline can still
  record false.
- **Evidence:** `worker_flow.rs:2006-2047` (persisted completion),
  `:2135-2150`, `:2144` (parent notification), `:940-1035`, `:1018-1035`
  (parallel filtering and persisted-status query), `:1138-1192` (sequential
  decision).
- **Conformance:** [CF-V1/a to CF-V1/c](conformance.md#cf-v1)

<a id="cf-v2"></a>
### CF-V2 · Optimized empty containers bypass the all-iterations stop predicate

**Scope:** core · **Confidence:** high (the empty-iterator case oracle-observed) · **Status:** confirmed

- **Rule:** An empty iterator, an empty loop body, or a branchall with zero
  branches completes without child jobs. Its stored module status is already
  `Success` when completion processing runs, so it is classified neither as
  an active loop nor as an active branchall. Therefore
  `stop_after_all_iters_if` is not evaluated. An ordinary `stop_after_if` on
  that container instead runs with `result = []` and can stop the enclosing
  flow; the active-loop break reset and the active-branchall suppression do
  not apply ([CF-29](#cf-29)). With an `error_message`, the empty container
  stops the flow with `EarlyStopError`. An empty `branchall` *branch* is
  different: it still creates a child flow and returns that child's input
  ([CF-15](#cf-15)).
- **Evidence:** `worker_flow.rs:3998-4052`, `:3197-3221`, `:571-595`,
  `:675-730`, `:1195-1209`, `:5532-5560`.
- **Conformance:** [CF-V2/a to CF-V2/c](conformance.md#cf-v2)

<a id="cf-v3"></a>
### CF-V3 · Child-flow startup errors bypass that child's failure module

**Scope:** core (nested error and handler behaviour); later (location-suffix fidelity) · **Confidence:** high (routing traced; the location suffix is build-dependent) · **Status:** confirmed

- **Rule:** When an exception escapes the initial execution of a flow job,
  the error handling depends on whether the job has a parent. A root flow
  gets a failed-step update on itself ([CF-23](#cf-23) route 1). A child flow
  is first completed with the error; then its parent receives the failed
  child update. The child's own first-module retry, continue and
  failure-module machinery is bypassed. This differs both from a branch
  predicate error, which is deliberately converted to a step failure
  ([CF-14](#cf-14)), and from an error during later chaining
  ([CF-23](#cf-23) route 3). The parent may handle or skip the failed child
  normally.
- **Evidence:** `backend/windmill-worker/src/worker.rs:3822-3840`;
  `backend/windmill-worker/src/result_processor.rs:1910-1978`, `:1939-1978`;
  `worker_flow.rs:4053-4080`, `:2086-2121`; error names and messages are
  serialized at `backend/windmill-common/src/worker.rs:2228-2232`; anyhow
  conversion at `backend/windmill-common/src/error.rs:145-150`.
- **Conformance:** [CF-V3/a, CF-V3/b](conformance.md#cf-v3)

<a id="cf-v4"></a>
### CF-V4 · Stored child nodes do not receive runtime handler-id suffixes

**Scope:** core (observable handler step id); out (worker-tag scheduling) · **Confidence:** high (oracle-observed for the stored-node id) · **Status:** confirmed

- **Rule:** Inline child-flow creation copies and suffixes the parent's
  handler id and preserves its step-tag setting ([CF-1](#cf-1)). A stored
  `modules_node` / `default_node` selects a prebuilt child flow before those
  changes. The dependency job keeps empty and simple bodies inline and turns
  the others into nodes holding the copied failure module with its original
  id; it does not copy `preserve_step_tags` into that child flow value,
  leaving it false. The child input and result scope are unchanged, but
  handler step ids and worker-tag selection can differ. Deployed runs use the
  lite version by default and fall back to the full version only when the
  lite one is missing, so a deployed flow's iteration handler normally runs
  with the original id. Not all stored-representation details are
  behaviour-neutral ([CF-26](#cf-26)).
- **Evidence:** `worker_flow.rs:5038-5074`;
  `backend/windmill-worker/src/worker_lockfiles.rs:1554-1591`;
  `backend/windmill-queue/src/jobs.rs:5420-5440`, `:5915-5930`;
  `backend/windmill-types/src/flows.rs:277-285`.
- **Conformance:** [CF-V4/a, CF-V4/b](conformance.md#cf-v4)
- **Revision 2026-10-28 (oracle, batch E3c):** the stored-node half was
  recorded: in a deployed flow whose loop at cursor 1 failed in both
  iterations, each copy reported `WM_FLOW_STEP_ID` `failure`, unsuffixed
  (fixture `conformance/flows/execution/fx-34`, run `step_id`, oracle run
  40689442792). This contradicted [FX-34](execution.md#fx-34), which is now
  corrected to this finding. The inline half (CF-V4/b) is not recorded.

<a id="cf-v5"></a>
### CF-V5 · For-loop fast-path eligibility follows runtime job kind

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** A `forloopflow`'s simple-body collapse requires that the **flow
  currently executing the loop** is not a `FlowPreview` job. A raw flow
  payload is queued as `FlowPreview` even when it was submitted through an
  API or test helper rather than an editor. Runtime-generated inline child
  bodies are also raw flow payloads and thus `FlowPreview`. Stored
  `FlowNode` bodies and deployed by-path `Flow` jobs do not have that
  exclusion; a by-path subflow establishes its own kind, so this rule never
  requires descendants reached through a deployed subflow to stay
  `FlowPreview`. The while-loop path does not test job kind. As a result, a
  nested forloop in an inline child does not collapse while the equivalent
  nested loop in a stored-node child does, even though its body has the same
  single script and no disqualifying fields. The difference affects the
  first-step `previous_result`, body fields, step ids, lookup registration
  and timeout enforcement ([CF-8](#cf-8), [CF-27](#cf-27)).
- **Evidence:** `backend/windmill-queue/src/jobs.rs:5604-5667` (raw flow →
  `FlowPreview`, kind at `:5658`), `:5420-5440` (`FlowNode` job),
  `:5965-5978` (deployed `Flow` job); `worker_flow.rs:5038-5074` (inline
  body payload versus node), `:5298-5300` (forloop exclusion), `:5247-5249`
  (while: no exclusion); `backend/windmill-worker/src/worker_lockfiles.rs:1571-1591`
  (simple bodies stay inline within their stored or deployed flow).
- **Conformance:** [CF-V5/a, CF-V5/b](conformance.md#cf-v5)

<a id="cf-v6"></a>
### CF-V6 · Direct result lookup can ignore surrounding multiline expression text

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** after the whole-expression context-name and previous-step
  shortcuts have been considered, the direct result matcher searches with
  multiline line anchors. A qualifying standalone line can select direct
  lookup even when other lines contain more expression text; the stored
  result is returned without evaluating the surrounding JS. The
  index/property grammar and the terminal-`length` exclusion of
  [CF-19](#cf-19) route 4 still apply, but the exclusion is checked on the
  first match only; there is no search for a later acceptable line. This
  affects every evaluation site that supplies the results context,
  including multiline `branchone` predicates ([CF-14](#cf-14)).
- **Evidence:** `backend/windmill-jseval/src/lib.rs:73-76` (multiline
  matcher), `:180-217` (first capture, `length` bypass, direct return);
  `backend/windmill-worker/src/js_eval.rs:36-79` (trim and earlier
  shortcuts); `worker_flow.rs:2451-2466` (predicate wrapping and boolean
  result check).
- **Conformance:** [CF-V6/a, CF-V6/b](conformance.md#cf-v6)
- **Note:** [JE-9](expressions.md#je-9) describes the same multiline
  matching; [CF-19](#cf-19) route 4 now states it.
- **Revision 2026-10-28 (oracle, batch E3a):** the finding was `open`, the
  correction side of the CF-19 dispute ([Q-CF-16](#q-cf-16)), against the
  competing claim that route 4 applies only when the whole expression has
  the direct-lookup form. The oracle recorded this finding's reading:
  CF-V6/a gave `7` and CF-V6/b `107` (fixture `conformance/flows/cf/cf-19`,
  oracle run 40687124625). The competing claim is withdrawn.

## Design notes (not Windmill behaviour)

These are proposals made while studying the behaviour. They bind nothing.

- **Body scopes.** The IR needs an explicit scope node per container body
  ([CF-1](#cf-1), [CF-3](#cf-3), [CF-9](#cf-9)).
- **Fast path.** Reproduce both the collapsed and the child-flow paths, or
  record a deliberate divergence; mark collapsible bodies at load time and
  take job kind (stored representation) as an input ([CF-8](#cf-8),
  [CF-V5](#cf-v5)).
- **Absent fields.** See the design note under [CF-6](#cf-6) and
  [Q-CF-13](#q-cf-13).
- **Result lookup.** A scoped leaf-result map, or an equivalent, is needed
  in the segment executor ([CF-19](#cf-19)). Whether to reproduce the
  subflow caller-results leak or return `null` is [Q-CF-12](#q-cf-12); a
  deliberate `null` is a documented compatibility divergence, not source
  parity.
- **JSON copies.** Normalise values at body entry and result collection
  ([CF-20](#cf-20)).

## Open questions

<a id="q-cf-1"></a>
- **Q-CF-1** · affects [CF-5](#cf-5), [CF-6](#cf-6), [CF-8](#cf-8),
  [CF-9](#cf-9), [CF-12](#cf-12), [CF-13](#cf-13), [CF-19](#cf-19),
  [CF-29](#cf-29), [CF-V1](#cf-v1), [CF-V2](#cf-v2), [CF-V4](#cf-v4) ·
  **Re-scoped 2026-10-28.** Batches E3d and E3a recorded CF-5/a and CF-5/b,
  CF-6/a, the by-path halves of CF-8/a to CF-8/c, CF-9/a and CF-9/b,
  CF-12/b, CF-12/c and CF-12/e, CF-13/a to CF-13/c, CF-V2/a, and CF-19/b
  and CF-19/c. CF-6/b waits for a pull read-back (E3e); CF-6/c and the raw
  halves of CF-8 are outside the reference configuration (design record
  0001) and are not recorded. Still to record: CF-V1/a to CF-V1/c,
  CF-V2/b, CF-V2/c and CF-29/a; CF-V4's cases are tracked under
  [CF-V4](#cf-v4). As first written, the oracle matrix of source-traced
  core rules had not been run, with the cases:
  CF-8/a to CF-8/c; CF-9/a and CF-9/b; CF-12/b, CF-12/c and CF-12/e;
  CF-19/b and CF-19/c; CF-V1/a to CF-V1/c; CF-V2/a to CF-V2/c and CF-29/a;
  CF-V4/a and CF-V4/b (handler ids in lite versus inline form); CF-6/a to
  CF-6/c (before and after normalization); CF-5/a and CF-5/b; CF-13/a to
  CF-13/c. *Settled by:* running them on a v1.767.0 CE oracle (setup in
  [oracle.md](oracle.md)), by path after a successful dependency job unless
  the case says otherwise, recording dependency state, stored form and job
  kinds.
<a id="q-cf-2"></a>
- **Q-CF-2** · affects [CF-6](#cf-6) · How long the window between push and
  dependency-job success lasts. The specification needs only the two
  states, which are source-settled. *Settled by:* measuring push-to-success
  time on the oracle.
<a id="q-cf-3"></a>
- **Q-CF-3** · affects [CF-23](#cf-23) · **Re-scoped 2026-10-28.** Part (1)
  is answered: the oracle recorded route 1 as `Anyhow` with
  `Error: <message> @worker.rs:3822:13` and route 3 as `InternalError` with
  the plain message ([CF-2/b](conformance.md#cf-2),
  [CF-7/d](conformance.md#cf-7)); the suffix stays build-dependent and is
  masked in comparisons. Part (2) remains. As first written: (1) The exact
  `Anyhow` message and location suffix of route 1 are build-dependent;
  fixtures match the message substring only. (2) Whether a branchone or subflow parent records the last
  inner result rather than the error on route 3. *Settled by:* an oracle run
  of CF-23/a for (1); for (2), an oracle run of a `branchone` at step 1
  whose chosen branch is `[ret(1), forloop with an object iterator]`,
  inspecting the branchone step's stored result separately from the value
  delivered to the parent and the final result.
<a id="q-cf-4"></a>
- **Q-CF-4** · affects [CF-14](#cf-14), [CF-15](#cf-15) · **Answered
  2026-10-28.** Both predictions hold on the oracle: an empty chosen
  `branchone` branch at step 0 gives the JSON string `"{}"`
  ([CF-14/c](conformance.md#cf-14)), and an empty `branchall` branch gives
  the enclosing flow input ([CF-15/f](conformance.md#cf-15)); batch E3d. As
  first asked: empty-body quirks, an empty `branchone` branch at step 0
  predicted to give `"{}"` and an empty `branchall` branch the enclosing
  flow input, settled by oracle runs of CF-14/c and CF-15/f.
<a id="q-cf-5"></a>
- **Q-CF-5** · affects [CF-19](#cf-19) · **Answered 2026-10-28.** The
  recorded text is
  `Failed to fetch result for step 'br': Not found: Completed Job not found at name 00000000-0000-0000-0000-000000000000 (jobs.rs:9074)`,
  the last part a build-dependent server location ([CF-19](#cf-19),
  [CF-19/c](conformance.md#cf-19); batch E3a). As first asked: the exact
  fetch-error text for a nil job, settled by an oracle run of CF-19/c,
  step `e`.
<a id="q-cf-6"></a>
- **Q-CF-6** · affects [CF-21](#cf-21) · Mid-loop re-evaluation of a loop
  module's `skip_if`. *Settled by:* an oracle run of CF-21/a, by path.
<a id="q-cf-7"></a>
- **Q-CF-7** · affects [CF-22](#cf-22) · The `iter_parent` artefact on
  container retry. *Settled by:* an oracle run of CF-22/a.
<a id="q-cf-8"></a>
- **Q-CF-8** · affects [CF-27](#cf-27) · Timeout enforcement outside the
  ordinary worker path (EE dedicated runners, `squash`, agent workers).
  *Settled by:* an EE oracle, only if windmill-lite ever needs those modes;
  out of scope otherwise.
<a id="q-cf-9"></a>
- **Q-CF-9** · affects [CF-28](#cf-28) · Kill latency and the final
  statuses of running children after a cancel are deliberately unspecified.
  *Settled by:* measuring on the oracle, only if the design needs a bound.
<a id="q-cf-10"></a>
- **Q-CF-10** · affects [CF-7](#cf-7) · Behaviour of `parallelism: 0` at its
  14-day hold deadline. Out of scope. *Settled by:* a scheduler trace or
  oracle run, if ever needed.
<a id="q-cf-11"></a>
- **Q-CF-11** · affects [CF-25](#cf-25) · EE `squash` data semantics
  (shared module state across iterations?). *Settled by:* an EE oracle run
  comparing a squashed and an unsquashed loop whose body keeps module-level
  state.
<a id="q-cf-12"></a>
- **Q-CF-12** · affects [CF-19](#cf-19) · (design decision) A subflow can
  read its caller's `results.<id>`. The source behaviour is settled; whether
  windmill-lite matches it or deliberately returns `null` is an architect
  decision, to be recorded as a divergence if taken.
<a id="q-cf-13"></a>
- **Q-CF-13** · affects [CF-6](#cf-6) · (design decision) Loader policy for
  absent container fields. The two stored states are source-settled; which
  default to adopt is an architect decision.
<a id="q-cf-14"></a>
- **Q-CF-14** · affects [CF-12](#cf-12) · **Answered 2026-10-28.** The
  correction holds: the loop fails and the enclosing flow fails with the
  loop's array without running its next step; with `skip_failures: true`
  the loop succeeds and the flow continues ([CF-12/c](conformance.md#cf-12),
  [CF-12/f](conformance.md#cf-12); batch E3d). As first asked (DISPUTED):
  after a sequential loop breaks on a body error stop with no handler and
  `skip_failures: false`, does the enclosing flow continue (as drafted) or
  fail without running its next step (correction)? Settled by an oracle run
  of CF-12/f.
<a id="q-cf-15"></a>
- **Q-CF-15** · affects [CF-8](#cf-8), [CF-V5](#cf-v5), [CF-17](#cf-17) ·
  **Answered 2026-10-28.** The correction holds, by a source re-check: a
  `flow` step always enqueues a `Flow` job, and the forloop gate tests only
  the kind of the job executing the loop, so a by-path subflow called from
  a raw root collapses an eligible loop. The nested case follows the
  version actually read, which is the lite one unless `DISABLE_FLOW_SCRIPT`
  is set or the lite version is missing ([CF-8](#cf-8)). The by-path halves
  of CF-8/a to CF-8/c are recorded (batch E3d); CF-8/d and the raw halves
  are outside the reference configuration and are not recorded. As first
  asked (DISPUTED): does a raw submission prevent forloop collapsing at
  every depth (as drafted), or only in `FlowPreview` jobs and their inline
  descendants (correction)? And the nested-loop rule depends on the stored
  form actually selected. Settled by an oracle run of CF-8/d, recording
  job kinds and stored form.
<a id="q-cf-16"></a>
- **Q-CF-16** · affects [CF-19](#cf-19), [CF-V6](#cf-v6),
  [CF-14](#cf-14), [FX-10](execution.md#fx-10) (route 2) · **Answered
  2026-10-28.** One line of a multiline expression can match, and the rest
  is ignored: CF-V6/a gave `7` and CF-V6/b `107` on the oracle (batch E3a);
  [CF-19](#cf-19), [CF-V6](#cf-v6) and [JE-9](expressions.md#je-9) agree.
  As first asked (DISPUTED): does direct result lookup require the whole
  expression to match (as drafted in CF-19 and in FX-10's route 2), or can
  one line of a multiline expression match and the rest be ignored (CF-V6)?
  Settled by an oracle run of CF-V6/a and CF-V6/b, and reconciliation with
  JE-9.
<a id="q-cf-17"></a>
- **Q-CF-17** · affects [CF-1](#cf-1), [CF-2](#cf-2), [CF-3](#cf-3),
  [CF-4](#cf-4), [CF-7](#cf-7), [CF-10](#cf-10), [CF-11](#cf-11),
  [CF-12](#cf-12), [CF-14](#cf-14), [CF-15](#cf-15), [CF-16](#cf-16),
  [CF-17](#cf-17), [CF-18](#cf-18), [CF-19](#cf-19), [CF-20](#cf-20),
  [CF-23](#cf-23), [CF-24](#cf-24), [CF-25](#cf-25), [CF-26](#cf-26),
  [CF-27](#cf-27), [CF-28](#cf-28), [CF-V3](#cf-v3), [CF-V5](#cf-v5),
  [CF-30](#cf-30) ·
  **Re-scoped 2026-10-28.** Batches E3d and E3a recorded CF-2/a and
  CF-2/b, CF-3/a, CF-4/a and CF-4/b, CF-7/a to CF-7/d, CF-10/a, CF-11/a,
  CF-12/a and CF-12/d, CF-14/a and CF-14/b, CF-15/a to CF-15/e and
  CF-19/a. Still to record: CF-1/a; CF-16/a and CF-16/b (tracked under
  [CF-16](#cf-16)); CF-17/a; CF-18/a; CF-20/a; CF-23/b; CF-24/a to
  CF-24/e; CF-25/a; CF-26/a; CF-27/a to CF-27/d; CF-28/a; CF-V3/a and
  CF-V3/b; CF-V5/a and CF-V5/b (CF-V5/a's flow is CF-8/a's, recorded by
  path); CF-30/a and CF-30/b. As first written, the remaining
  source-derived cases had not been recorded on the oracle either: CF-1/a; CF-2/a and CF-2/b; CF-3/a; CF-4/a and CF-4/b; CF-7/a to
  CF-7/d; CF-10/a; CF-11/a; CF-12/a and CF-12/d; CF-14/a and CF-14/b;
  CF-15/a to CF-15/e; CF-16/a and CF-16/b; CF-17/a; CF-18/a; CF-19/a;
  CF-20/a; CF-23/b; CF-24/a to CF-24/e; CF-25/a; CF-26/a; CF-27/a to
  CF-27/d; CF-28/a; CF-V3/a and CF-V3/b; CF-V5/a and CF-V5/b. *Settled by:*
  running them on the oracle under the same recording rules as
  [Q-CF-1](#q-cf-1).
<a id="q-cf-18"></a>
- **Q-CF-18** · affects [CF-7](#cf-7) · With one oracle worker, the
  recordings separate release from worker serialisation for the child
  flows: with `parallelism: 2` they overlapped, with `parallelism: 1` each
  started after the previous one completed, in index order
  ([CF-7/a, CF-7/b](conformance.md#cf-7)). They cannot show what happens
  when several iterations are eligible at once: whether their body scripts
  then run concurrently, and in which order they start (CF-7 leaves the
  start order to the scheduler). *Settled by:* a two-worker oracle session
  running CF-7/a with `parallelism: 2` and recording the body jobs'
  start and end times, which needs a change to the oracle workflow (it runs
  one worker, [OR-4](oracle.md#or-4)). Not needed for the first release
  unless the design promises an order.

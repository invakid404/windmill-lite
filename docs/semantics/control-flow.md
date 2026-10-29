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
  collapsed iteration is a script job directly under the loop. Milestones
  N3c, NI and N5 have since recorded, in the same directory, the cases of
  CF-16 to CF-18, CF-20 to CF-29 and CF-V1 to CF-V5 that the
  reference configuration allows (oracle runs 40858228112, 40852609901,
  40857510216, 40915682847 and 40926159329); each case names its run. The
  other expectations are source-derived; [Q-CF-17](#q-cf-17) tracks what
  remains.

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
   `sleep` is never applied. A container in the failure slot is advanced
   by bookkeeping meant for list modules: a sequential loop or `branchall`
   there ends the flow after its first child, an empty one makes the failed
   step succeed, and a parallel one never completes on its own
   ([CF-6](#cf-6), [CF-V4](#cf-v4), [CF-24](#cf-24), [CF-30](#cf-30),
   [CF-31](#cf-31)).
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
- **Revision 2026-10-29 (oracle, batch E3e):** the parallel half is now
  recorded with completions out of index order. On the oracle's two-worker
  session a parallel loop over `[0, 1]` whose iteration 0 sleeps 1.5 s
  finished iteration 1 first and still gave `[0, 1]` (fixture
  `conformance/flows/oracle/workers2`, run `loop_slow`, oracle run
  40792040448; [Q-OR-7](oracle.md#q-or-7)). The rule is unchanged.

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

**Scope:** core · **Confidence:** high (the materialized values and the behaviour after a successful dependency job oracle-observed; points 5 and 6 oracle-observed; the un-normalized behaviour traced in source, not run) · **Status:** confirmed

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
- **Revision 2026-10-29 (oracle, batch E3e):** point 5 is recorded
  (CF-6/b, fixture `conformance/flows/control-flow/pull`, oracle run
  40794872025): a forloop pushed through the API without `skip_failures`
  and read back by `wmill sync pull` after its dependency job carries
  `skip_failures: true` and `parallel: false` in the pulled `flow.yaml`,
  while a non-container module keeps an unknown field as submitted
  ([SY-6](openflow.md#sy-6)). The rule is unchanged.

<a id="cf-7"></a>
### CF-7 · Parallel `forloopflow`: spawning, `parallelism`, completion

**Scope:** core (`same_worker` error: later) · **Confidence:** high (aggregation, order, `same_worker` condition and error; release order under `parallelism: 1`; all oracle-observed); medium (start order among several eligible iterations); high (`type: ai` and the release delay, source-traced); high (a failed parallel loop under `continue_on_error`, source-traced) · **Status:** confirmed; drafted (`type: ai`, the release delay, `continue_on_error`)

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
    - **`parallelism: {type: ai}`** decodes (the slot accepts the `ai`
      transform type, [OF-V2](openflow.md#of-v2)) and evaluates to `0`,
      the value an `ai` transform gives wherever a number is expected. So it
      behaves exactly as `N = 0`: every iteration is held, none is released,
      and the loop waits on the 14-day hold. CE has no other route for it:
      nothing refuses it at deploy or at run time, and no AI runtime fills
      it in a flow run. (`iterator: {type: ai}` is refused instead,
      [CF-2](#cf-2).)
  - **Completion.** The loop step completes only when every iteration has
    completed. There is no fail-fast: all iterations run even after one
    fails.
  - **Success decision.** It uses the children's **persisted** completion
    status, so a child that recovered through its handler
    (`recover: true`) counts as success ([CF-V1](#cf-v1)). The step succeeds
    if `skip_failures` is true or every child counts as success; otherwise
    it fails with the full array as its result.
  - **A failed parallel loop under `continue_on_error`** (drafted; the same
    for a parallel `branchall`, [CF-15](#cf-15)). The parallel completion
    path has no tolerance step: it does not advance the cursor, and the
    continue decision, which then still sees the loop as a step before the
    last, continues into it. A `retry` that is due runs first and re-runs
    the loop ([CF-22](#cf-22)). Otherwise the flow re-enters the failed loop,
    which cannot start from a `Failure` status, and the flow is aborted by
    [CF-23](#cf-23) route 3: name `InternalError`, message
    `Bad request: Unrecognized module status for ForloopFlow Failure { … }`
    (`BranchAll` for a branchall), the status printed in Rust debug
    notation, which holds job ids. The next step never runs, at the last
    position the flow does not succeed with the array, and the flow's
    failure module does not run. So `continue_on_error` never tolerates a
    failed parallel container; `skip_failures` (or `skip_failure` per
    branch) is the only way through.
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
  - **Why a released iteration starts late.** A held iteration is queued
    as a suspended job. Each completion releases exactly one, the iteration
    `N + k − 1` at the `k`-th completion, by clearing its hold. A worker
    looks for released suspended jobs only on some pulls: when its previous
    such look found one, at random in proportion to how many of its last 30
    pulls found one, or once 5 s have passed since its last look. On an
    otherwise idle worker that gives the observed delay of up to about 5 s.
    Among several released iterations, that look takes the highest priority
    first, then the oldest queue row; all iterations of a loop are queued
    in one transaction and share their creation time, so the source does
    not fix which of several eligible iterations starts first
    ([Q-CF-18](#q-cf-18)).
- **Evidence:** `worker_flow.rs:5352-5414` (all payloads at once),
  `:4484-4515` (held jobs and release counter), `:1097-1115` (release when a
  sibling completes), `:765-916` (per-child completion), `:917-1080`,
  `:1018-1035` (final aggregation, persisted-status query), `:675-682` (no
  `stop_after_if` for parallel loops), `:4100-4110`, `:4573`, `:4793-4798`;
  `type: ai`: `backend/windmill-types/src/flows.rs:780-794`, `:805-830`
  (decoded in the `parallelism` slot), `worker_flow.rs:2706` (an `ai`
  transform evaluates to the type's default, `0`), `:4484-4515` (held
  when the index is at least that value); the release delay:
  `worker_flow.rs:775-846` (the completion counter), `:1097-1115` (one
  release per completion), `backend/windmill-common/src/worker.rs:648-657`
  (the suspended-job pull and its order),
  `backend/windmill-worker/src/worker.rs:2668-2690`, `:2750-2760` (when a
  worker tries it), `backend/windmill-queue/src/jobs.rs:3998-4030`,
  `backend/migrations/20220123221903_first.up.sql:124` (a queue row's
  creation time defaults to the transaction's time)
  (`same_worker` condition; the error raised as `BadRequest` at `:4793-4798`);
  `backend/windmill-common/src/error.rs:55-56` (`Bad request: ` text),
  `:73-74` (the pickup form); `backend/windmill-types/src/flows.rs:1080-1081`;
  `frontend/src/lib/components/flows/content/FlowModuleEarlyStop.svelte:72`
  (the UI hides "break" for parallel loops);
  `continue_on_error`: `worker_flow.rs:1037-1092` (the parallel completion
  advances only on success), `:1369-1397` (so the step is not the last),
  `:1729-1758` (the continue decision), `:3723-3832` (no retry due and
  `continue_on_error` set: neither a retry nor the failure module),
  `:5900-5903` (`Unrecognized module status for ForloopFlow …`),
  `:5603-5605` (the `branchall` counterpart), `:2099-2126` (the chaining
  abort),
  `backend/windmill-common/src/error.rs:55-56`.
- **Conformance:** [CF-7/a to CF-7/f](conformance.md#cf-7)
- **Revision 2026-10-28 (oracle, batch E3d):** the `same_worker` error's
  recorded form (`Bad request: ` inside the pickup envelope) and the
  observed release timing are added; the release order under
  `parallelism: 1` is now recorded, so its confidence rises. Before, the
  finding gave the error text alone. What one worker cannot show is
  [Q-CF-18](#q-cf-18).
- **Revision 2026-10-29 (oracle, batch E3e):** on the oracle's two-worker
  session the body scripts of an unbounded parallel loop over `[0, 1]`
  overlapped: both started within 2 ms, and iteration 0's 1.5 s body
  completed after iteration 1's, while the loop's result stayed `[0, 1]`
  (fixture `conformance/flows/oracle/workers2`, run `loop_slow`, oracle run
  40792040448). So the one-at-a-time body scripts of the observed timing
  above come from the single worker, not from the loop. Release under
  `parallelism` with several eligible iterations is still unrecorded
  ([Q-CF-18](#q-cf-18)).
- **Revision 2026-10-29 (SC3, source re-check, chain N3a):** milestone N3a
  refuses an `ai` parallelism and asked whether CE has a route for it. It
  has one, and it is useless: the value evaluates to `0`, so the loop holds
  every iteration as with `parallelism: 0`. Added, with the source's reason
  for the observed release delay and why the start order among several
  eligible iterations is not fixed. New source-derived case CF-7/e.
- **Revision 2026-10-29 (SC4, source re-check):** added what a failed
  parallel loop or parallel `branchall` does under `continue_on_error`: the
  flow aborts with an `InternalError` instead of continuing. New
  source-derived case CF-7/f.

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

**Scope:** core · **Confidence:** high (oracle-observed); high (source-traced: the container's settings on a forwarded stop) · **Status:** confirmed; drafted (the container's settings on a forwarded stop, added 2026-10-29 by SC4)

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
       `skip_failures: false` the loop step fails: its own `retry` applies,
       but its `continue_on_error` does **not**, at any position (see
       below), so without a retry the enclosing flow fails with the loop's
       array as its result and its next step does not run
       ([CF-5](#cf-5));
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

     **The container's own settings on that completion** (drafted). The
     completion that carries a forwarded stop turns the container's
     `continue_on_error` off, wherever the container stands in its list, so
     it never tolerates the failure. A loop, which clears the stop when it
     breaks, still evaluates its `retry`; a `branchone` or sequential
     `branchall`, which keep the stop, end the flow before any `retry` is
     considered.
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
  step), `:670-677` (a forwarded stop sets `continue_on_error` to false for
  that completion), `:1302-1327`, `:1729-1733` (so the cursor does not
  advance and the last-step tolerance does not apply), `:2006-2047`, `:2135-2150` (stop flag handed to the parent),
  `:2329-2346` (per-branch `skip_failure`); tests
  `backend/tests/worker.rs:645`, `:700` (stop inside a sequential branchall:
  flow result `[-123]`, later step not run);
  `frontend/src/lib/components/flows/content/FlowModuleEarlyStop.svelte:36-82`.
- **Conformance:** [CF-12/a to CF-12/g](conformance.md#cf-12)
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
- **Revision 2026-10-29 (SC4, source re-check, milestone N3c):** point 4
  said that after a sequential loop breaks on a forwarded error stop, its
  own `retry` and `continue_on_error` apply. Only the `retry` does: the
  completion that carries the forwarded stop sets the container's
  `continue_on_error` to false, so it is not tolerated at a middle or at
  the last position, and `branchone` and sequential `branchall` ignore
  their `retry` too. None of the recorded runs had `continue_on_error` on
  the loop (CF-12/c and CF-12/f use `skip_failures`). New source-derived
  case CF-12/g. Point 3's handled case is extended in [CF-16](#cf-16) point
  8.

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
- **Revision 2026-10-29 (oracle, batch E3e):** branch order is now
  recorded under real parallelism. On the oracle's two-worker session a
  parallel `branchall` whose branch 0 sleeps 1.5 s completed branch 1 (and,
  with three branches, the empty middle branch and branch 2) first, and the
  result stayed in branch order: `[0, 1]`, and `[0, {}, 2]` with the empty
  branch keeping index 1 and giving the enclosing flow input `{}` (fixture
  `conformance/flows/oracle/workers2`, runs `par_slow` and `par_slow_empty`,
  oracle run 40792040448; [Q-OR-7](oracle.md#q-or-7),
  [Q-OR-15](oracle.md#q-or-15)). The rule is unchanged.

<a id="cf-16"></a>
### CF-16 · `failure_module` inside containers

**Scope:** core · **Confidence:** high (routing, recovery split); high (oracle-observed: the stored-node handler id, points 1 and 4, the recovery split); medium (the inline handler id; not recorded, outside the reference configuration); high (source-traced: point 8) · **Status:** confirmed; drafted (point 8, added 2026-10-29 by SC4)

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
  8. **What the enclosing flow does with a handled failure** (drafted). A
     child flow whose failure ended in its copy of the handler reports an
     ordinary failure to its container, marked as handled; an error stop's
     stop flag is not forwarded ([CF-12](#cf-12) point 3). When the
     container step then fails, it is an ordinary failed step except for the
     handler:
     - its own `retry` is evaluated first and re-runs it
       ([CF-22](#cf-22));
     - for a **sequential** container (a sequential loop, a while loop, a
       `branchone`, a sequential `branchall`), its `continue_on_error`
       tolerates the failure at **any** position: at a middle step the flow
       continues with the container's result as `previous_result`; at the
       last step the flow succeeds with that result
       ([FX-30](execution.md#fx-30)). A failed **parallel** loop or
       parallel `branchall` is not tolerated: it completes through the
       parallel path, which leaves the cursor on the container, so
       `continue_on_error` re-enters it and the flow aborts with
       [CF-7](#cf-7)'s `InternalError`, handled mark or not
       ([CF-7/f](conformance.md#cf-7), its handler variant);
     - without either, the flow fails with the container's result, does not
       run its own handler again (point 4), and reports the failure as
       handled to its own parent, so no outer level runs it either.

     The mark covers only that completion: a later step of the same flow
     that fails runs the flow's handler normally. A parallel loop or
     parallel `branchall` that fails is always marked handled when the
     flow has a handler, since each failed child ran its copy; the mark
     only keeps the flow's own handler out, and adds no tolerance (above). A `flow` step
     by path clears the mark, so the caller's handler runs
     ([CF-17](#cf-17)). A container as the handler itself is
     [CF-31](#cf-31).
- **Evidence:** `worker_flow.rs:5038-5074` (handler copied into children),
  `:940-1035`, `:1018-1035`, `:1138-1192`, `:1734-1757` (a failed step with
  a handler continues into it), `:2006-2047`, `:2135-2150` (the child reports
  `success && !is_failure_step`), `:1082-1092` (parallel: no second run at
  the container); `backend/windmill-worker/src/result_processor.rs:1939-1978`;
  `backend/windmill-worker/src/worker_lockfiles.rs:1554-1591`;
  `backend/windmill-jseval/src/lib.rs:495-524` (`error` binding over arrays);
  point 8: `worker_flow.rs:163-185` (each completion starts unmarked unless
  a child reports the mark), `:498-500` (a `flow` step clears it),
  `:1087-1092` (a failed parallel container is marked), `:1302-1327` (the
  cursor advances under `continue_on_error` unless a retry is due),
  `:1729-1758` (retry, then tolerance, then the handler check that the mark
  blocks), `:2140-2153` (a child that ended in its handler reports the
  mark).
- **Conformance:** [CF-16/a to CF-16/f](conformance.md#cf-16)
- **Note:** handler retry and unrecoverable-error exceptions belong to
  [execution.md](execution.md).
- **Revision 2026-10-28 (oracle, batch E3c):** point 6 was right; execution's
  FX-34 had said every copy is suffixed and is now corrected to match. The
  stored-node id was recorded by fixture `conformance/flows/execution/fx-34`
  (run `step_id`, oracle run 40689442792), as was point 1's once-per-failing
  iteration run (run `loop`). The CF-16 cases themselves are not recorded.
- **Revision 2026-10-29 (SC4, source re-check, milestone N3c):** added
  point 8, answering N3c's open question 4. N3c treats a handled failure
  that reaches a flow with a handler as a forwarded error stop, which its
  `continue_on_error` tolerates only at the last module. In Windmill it is
  not a stop: it is an ordinary failure marked as handled, so the
  container's `retry` and `continue_on_error` apply as for any failed step,
  at a middle step as at the last (for a sequential container; corrected in
  validation round 1, below), and only the flow's handler is skipped.
  A forwarded error stop proper (a child without a handler) is
  [CF-12](#cf-12) point 4, corrected the same day. New source-derived cases
  CF-16/e and CF-16/f.
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** CF-16/a and
  CF-16/b were recorded as stated (fixture
  `conformance/flows/control-flow/cf-16`, runs `a` and `b`, oracle run
  40858228112): one handler job in each failing iteration's child flow,
  none at the root. CF-16/c and the stored-node half of CF-16/d follow from
  the recorded CF-V1/a to CF-V1/c and CF-V4/a; the inline half (CF-V4/b)
  needs an inline child payload and is not recorded.
- **Revision 2026-10-29 (SC4 validation round 1, finding 3):** point 8
  said that the container's `continue_on_error` tolerates a handled
  failure at any position, for every container. That holds only for
  sequential containers. A failed parallel loop or parallel `branchall`
  completes through the parallel path, which never advances the cursor
  past the container (`worker_flow.rs:1037-1061`, `:1087-1093`,
  `:1369-1397`), so `continue_on_error` re-enters it and the flow aborts
  as [CF-7](#cf-7) says, even when every failed child ran its handler
  ([CF-7/f](conformance.md#cf-7)); the handled mark then only keeps the
  flow's own handler out.

<a id="cf-17"></a>
### CF-17 · Subflows (`type: flow`, by path)

**Scope:** core (scheduler overrides: out) · **Confidence:** high (oracle-observed: inputs, result, scope; a `retry` on the step); medium (`recover` across the boundary); medium (source-traced: the version each attempt runs, the subflow's own handler on each attempt, nesting depth) · **Status:** confirmed; drafted (the version each retry runs, the subflow's own handler on each attempt, nesting depth; added 2026-10-29 by SC4)

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
  - **`retry` on the step** (recorded by milestone N5,
    [CF-17/b to CF-17/i](conformance.md#cf-17)). The caller's `retry`
    applies to a failed `flow` step as to any other step
    ([FX-26](execution.md#fx-26), [FX-27](execution.md#fx-27)):
    1. **Each attempt is its own subflow job.** A failed attempt's `Flow`
       job stays as it ended, with its own status and results. When the
       retry is scheduled, that job's id is appended to the caller's
       flow-wide retry list, which the step's status entry copies as
       `failed_retries`. The retry then queues a new `Flow` job of the same
       path at once, due after the retry's delay ([FX-26](execution.md#fx-26)),
       with the failed attempt's stored arguments (the subflow's `flow_input` of that attempt): the step's
       input transforms are not evaluated again. The retry goes through the
       step's start phase again, so the path is resolved again to its
       latest deployed version when the retry is queued, not when the
       delayed job starts (drafted).
    2. **The last attempt is the step.** The step's status entry names the
       last attempt's job, so `results.<id>`, the next step's
       `previous_result` and, at the last step, the flow's result are the
       last attempt's. Earlier attempts appear only in `failed_retries`.
       When the attempts run out, the step fails with the last attempt's
       final result, and the caller's `continue_on_error` and
       `failure_module` apply to it as above.
    3. **Retry state belongs to each flow job.** The subflow's own steps
       keep their retries in the subflow's own status: a failed attempt has
       spent its steps' retries, and the next attempt starts them afresh.
    4. **Inside a body,** the step and its retries belong to the child flow
       that runs the body, so each loop iteration retries its own subflow
       step (recorded in a sequential and a parallel loop).
    5. **Later modules list the attempts again**
       ([FX-27](execution.md#fx-27), [FX-29](execution.md#fx-29)): the
       caller's retry list is cleared only when a module succeeds, so after
       the last attempt fails, the failure module's status, or under
       `continue_on_error` the next module's status, lists the same failed
       attempts in its own `failed_retries`.
    6. **A throwing input transform** ([FX-14](execution.md#fx-14)): the
       step's `Flow` job is still queued, with empty arguments and the
       transform's error as its pre-run error. It fails when picked up,
       with that `ExecutionErr`, before any of its modules starts, so all
       of them stay waiting. The retry re-queues the subflow with that
       attempt's stored arguments, `{}`, and the subflow then runs with an
       empty `flow_input`.
    7. **The subflow's own failure module** (drafted) runs inside each
       failed attempt that reaches it; the caller still sees a failed step
       and still retries it, whatever the handler returned: the handler
       flag is reset at a `flow` step, and a subflow that ended in its
       handler reports failure even after `recover: true`.

    **What the oracle's walker shows** ([OR-11](oracle.md#or-11)). The
    harness fetches the job of each `failed_retries[k]` and records it at
    the typed site `<step>/r:<k>` (design record 0011); since that job is a
    flow, it walks the job's own status, so a failed attempt's modules
    appear below it (`m:a/r:0/m:x`), and a retried step inside that attempt
    nests further (`m:a/r:0/m:x/r:0`). The site path and the nesting are
    the harness's notation: Windmill keeps only a list of job ids on the
    step's status entry, each a complete flow job. The same job appears
    again at the sites of the later modules that list it (`m:b/r:0`,
    `s:failure/r:0`).
  - **Nesting depth** (drafted). Nothing bounds how deeply subflows by path
    nest. Deployment records each `flow` step's path as a dependency
    without looking for cycles, and the step queues a new `Flow` job
    whenever it starts, so a flow that calls itself by path, directly or
    through other flows, deploys and runs, and only its own logic ends the
    recursion ([CF-17/j](conformance.md#cf-17)). A cancellation marks
    queued and running descendants only down to 500 levels
    ([CF-28](#cf-28)): the jobs of a deeper recursion are not marked by it.
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
  `:2771` (subflow `flow_env` own and inherited). The retry:
  `worker_flow.rs:1701-1722`, `:1751` (a failed step is retried while
  attempts remain), `:3723-3737` (the retry evaluated at the next push),
  `:3752-3766` (the failed job appended to the flow's retry list and
  copied to the step's `failed_retries`), `:3793-3795`, `:3891-3905` (the
  retry's arguments are the last failed job's stored arguments),
  `:1287-1300`, `:1334-1345` (the completed entry: `job` and the copied
  list), `:2135-2153` (a finished subflow reports to its caller under its
  own job id), `:5142-5157`, `:6020-6049` (each start of the step resolves
  the path and queues a `Flow` job), `:1491-1503` (the list cleared after
  a successful module), `:498-500` and `:2144` (the handler flag reset at
  a `flow` step; a subflow ending in its handler reports failure);
  `backend/windmill-types/src/flow_status.rs:33`, `:48-51` (each flow
  job's status holds its own retry state). The throwing transform:
  `worker_flow.rs:4267-4281` (empty arguments and a pre-run error),
  `:4404-4429` (the job is queued with them);
  `backend/windmill-worker/src/worker.rs:3647-3649` (a job with a pre-run
  error fails at pickup, before its kind is looked at). Nesting depth:
  `backend/windmill-worker/src/worker_lockfiles.rs:1238-1247` (the
  dependency row, no cycle check); `backend/windmill-queue/src/jobs.rs:327-344`
  (the cancel walk stops at depth 500). The retry's behaviour is recorded
  by fixture
  [`control-flow/subflow-retry`](../../conformance/flows/control-flow/subflow-retry)
  (oracle runs 40915682847 and 40926159329).
- **Conformance:** [CF-17/a to CF-17/j](conformance.md#cf-17)
- **Note:** the subflow job is a `Flow` job whatever the caller's kind, so
  an eligible forloop inside it collapses even when the caller is a raw
  submission ([CF-8](#cf-8)). Inside a subflow by path,
  `WM_ROOT_FLOW_JOB_ID` names the subflow's own job, so each retry attempt
  has a different one, while `WM_ROOT_JOB_ID` stays the submitted root
  ([WA-V1](client-api.md#wa-v1), [OR-16](oracle.md#or-16)); N5's recordings
  show both.
- **Revision 2026-10-29 (SC4, recordings of milestone N5):** the finding
  said only that the caller's `retry` applies to a failed subflow. Milestone
  N5 recorded a retry on a `flow` step on the CE oracle (fixture
  `control-flow/subflow-retry`, runs `once`, `exhausted`,
  `exhausted_continue`, `exhausted_failure`, `nested`, `loop_seq`,
  `loop_par`, `delay` in oracle run 40915682847 and `pre_run` in oracle run
  40926159329). The rule now says where a failed attempt goes (its own
  `Flow` job, listed in the step's `failed_retries`), that the last attempt
  is the step's value, that each flow job keeps its own retry state, that
  later modules list the attempts again, and what a throwing transform
  does on a `flow` step; new cases CF-17/b to CF-17/i record them. From the
  source alone (drafted): each retry resolves the path again, the
  subflow's own handler runs in each failed attempt without stopping the
  retry, and nothing bounds the nesting of subflows by path, which answers
  milestone N3c's question about a self-calling subflow (new case
  CF-17/j).
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** CF-17/a was
  recorded as stated (fixture `conformance/flows/control-flow/cf-17`, run
  `a`, oracle run 40858228112): the subflow job's input was its step's
  arguments, its result the step's, and the caller's `results.s` was
  undefined.

<a id="cf-18"></a>
### CF-18 · `identity`, skipped containers, mocked containers

**Scope:** core · **Confidence:** high; high (oracle-observed: a skipped container) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** CF-18/a was
  recorded as stated (fixture `conformance/flows/control-flow/cf-18`, run
  `a`, oracle run 40858228112): the skipped loop ran as one identity job
  whose argument was `{"previous_result": 42}`, with no iteration.

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

**Scope:** core · **Confidence:** high (oracle-observed for sibling branches) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** CF-20/a was
  recorded as stated (fixture `conformance/flows/control-flow/cf-20`, run
  `a`, oracle run 40858228112).

<a id="cf-21"></a>
### CF-21 · Evaluation timing of container-level fields

**Scope:** later · **Confidence:** high (once/per-child split); high (oracle-observed: the preceding step's `sleep`, the `timeout`'s scope, mid-loop `skip_if` on collapsed, child-flow and while loops, a parallel loop's single `skip_if`); medium (the order of `timeout` and `parallelism`, and a parallel `branchall`'s delayed children, source-traced) · **Status:** confirmed; drafted (the order of `timeout` and `parallelism`, and that a parallel `branchall`'s children all take the preceding step's delay; added 2026-10-29 by SC4)

- **Rule:**
  - **Once, at container start:** the `iterator`, the `branchone`
    predicates, and the `sleep` of the *preceding* step. That delay is set
    on every child queued in the container's start phase and on no later
    one: for a sequential loop (collapsed or not), a while loop, a
    `branchone` and a sequential `branchall`, which queue one child at a
    time, it delays only the first child; a parallel loop and a parallel
    `branchall` queue all their children in that phase, so it delays every
    child, held ones included.
  - **Per child pushed:** `timeout`, evaluated in the scope of the
    container step itself and assigned to each child job (iteration flow,
    collapsed body job, branch flow or subflow); its `flow_input` is the
    arguments of the flow job running the container, not those of the
    child being created, so it never sees the iteration being created, also
    when it is evaluated again for each body job of a collapsed loop. Those
    arguments hold an `iter` only when the enclosing flow's own input has
    one: inside the body of an outer loop that runs as a child flow, the
    outer iteration's `iter`; at the top level, an `iter` member the caller
    passed. `parallelism` is re-evaluated
    per child. Where the assigned `timeout` is actually enforced is
    [CF-27](#cf-27).
  - **Order within a child** (drafted). For each child, `timeout` is
    evaluated before the child is queued and a parallel loop's
    `parallelism` after it, so for the first child the `timeout` comes
    first. All children of one start phase are queued in one transaction,
    committed only after the last, so either expression throwing ends the
    start phase with no child queued, whichever child it threw for
    ([CF-23](#cf-23)).
  - **`skip_if` on a sequential loop module is re-evaluated before every
    iteration.** For iterations after the first, `previous_result` is the
    prior iteration's result: for a body that runs as child flows, that
    child flow's final result. If it turns truthy mid-loop, the
    loop step ends as a skipped identity whose result is that
    `previous_result`, and the partial array is lost ([Q-CF-6](#q-cf-6)).
- **Evidence:** `worker_flow.rs:3666-3715` (sleep only while the step waits
  to start), `:4114-4122`, `:4417` (every payload of one start phase is
  queued with that one delay), `:4379-4400` (timeout per pushed child,
  evaluated over the flow job's own arguments at `:4379-4389`), `:4484-4499`
  (parallelism per child), `:3842-3866` (`skip_if` evaluated on every
  entry, including continuing iterations), `:5136` (identity/next step
  replaces the active loop state); the order: `:4120` (one transaction for
  the children), `:4379-4400` before the push at `:4404-4429`, the
  parallelism after it at `:4481-4499`, `:4569`, `:4800` (the commit after
  the last child); the next `previous_result`: `:1134-1166` (a completed
  iteration that continues the loop leaves the module in progress),
  `:1478-1489`, `:2086-2091` (its result is what the next push receives).
- **Conformance:** [CF-21/a to CF-21/f](conformance.md#cf-21)
- **Note:** run the fixture by path with the loop at top level (root kind
  `flow`), where the loop collapses ([CF-8](#cf-8)).
- **Revision 2026-10-29 (SC4, source re-check, milestone NI):** milestone
  NI read, without a recording, that a container's `timeout` is evaluated
  before its `parallelism` and that a mid-loop `skip_if` over child flows
  sees the previous child's result. Both hold in the source: added the
  order within a child (and that a throwing one queues no child) and the
  child flow's final result as the next `previous_result`.
- **Revision 2026-10-29 (SC4, recordings of milestone NI):** milestone NI's
  fixtures `conformance/flows/control-flow/cf-21` (oracle runs 40852609901
  and 40857510216) and `conformance/flows/control-flow/waits` (run
  `sleep_before_branch`, oracle run 40857510216) recorded CF-21/a as stated
  and the new cases CF-21/b to CF-21/e. One statement is corrected: the
  rule said that the preceding step's `sleep` applies before the first
  child only. That holds for containers that queue one child at a time; a
  parallel loop queues all its children in its start phase with the same
  delay, and the oracle started both iterations' jobs at least 2 s after
  the preceding step ended (run `sleep_before_par`). The `timeout`'s scope,
  which the rule called "step scope", is now spelt out: an expression
  reading `flow_input.iter` throws, for a collapsed loop as for child
  flows, and the flow aborts with the chaining `InternalError` before any
  iteration job exists (runs `timeout_scope_collapsed`,
  `timeout_scope_flows`). The mid-loop `skip_if` over child flows, drafted
  above, is recorded (run `skip_mid_flows`), as are a while loop's (run
  `skip_mid_while`) and a parallel loop's single evaluation at its start
  (run `skip_parallel`). [Q-CF-6](#q-cf-6) is answered.
- **Revision 2026-10-29 (SC4 validation round 1, finding 6):** the rule
  said that the `timeout`'s `flow_input` holds no `iter`. It is the
  arguments of the flow job running the container
  (`worker_flow.rs:4379-4389`), and the new iteration's `iter` is inserted
  only into the child's arguments (`:4158-4170`), so the timeout never
  sees the iteration being created; but it does see an `iter` the
  enclosing flow's input already has, an outer iteration's or one the
  caller passed. The two recorded failures hold for their top-level
  inputs. New source-derived case CF-21/f.

<a id="cf-22"></a>
### CF-22 · `retry` on a container module

**Scope:** later · **Confidence:** high (oracle-observed: the re-run, the restored `previous_result`, the `iter_parent` artefact, a collapsed loop's first push, a retried while loop, `branchone` and parallel `branchall`); medium (source-traced: the attempt and retry base of a parallel container; later iterations of a non-collapsed loop) · **Status:** confirmed; drafted (parallel containers, and the inputs of the children after the first at re-entry; added 2026-10-29 by SC4)

- **Rule:**
  1. **Re-run.** A failed container step with `retry` re-runs from scratch
     after the backoff: the iterator or predicates are re-evaluated and all
     children re-run. Recorded for a sequential for loop, a while loop, a
     `branchone` and a parallel `branchall` (every branch runs again, the
     one that succeeded before included).
  2. **`previous_result` on retry** is restored to the preceding step's
     stored result, or the flow input when the container is step 0. If the
     preceding step did not succeed (for example it failed under
     `continue_on_error`), the restored value is the JSON **string** `"{}"`.
  3. **Child inputs on retry.** The re-entry's **first** child is pushed
     from the **last failed child job's** stored input, not from the
     flow's input. Only that push: the children a sequential container
     pushes afterwards (the next iterations, the next branches) start from
     the flow's input as usual (recorded for a collapsed loop; the same
     code path for a non-collapsed one, drafted).
     - **Non-collapsed body:** that stored input is the old child's flow
       input, which already contains `iter`, so the first re-run child sees
       the failed child's `iter` moved to `flow_input.iter_parent`.
     - **Collapsed body:** the stored input is the body script's transformed
       arguments, and the body's input transforms are evaluated again over
       it (plus the new `iter`) as `flow_input`. So the first re-run
       iteration's transforms see the failed job's arguments, not the
       flow's input: a transform reading a flow input the body's arguments
       do not carry gets `undefined`. No `iter_parent` artefact arises from
       the loop input. (A body's transformed arguments can still contain an
       `iter` key the user put there deliberately.)
     - **`branchone`:** the chosen branch's child flow was pushed with the
       flow's input, so the re-run branch gets that input again.
  4. **The failed attempt** is the job whose completion failed the
     container, and the step's entry lists it in `failed_retries`: a
     sequential loop's failed iteration (its child flow, or a collapsed
     body's script job, whose error carries the loop's `step_id`), a
     `branchone`'s branch child flow. The oracle's walker records it at
     `<step>/r:<k>` with a child flow's modules below it ([CF-17](#cf-17)
     explains the notation).
  5. **Parallel containers** (drafted). A parallel `forloopflow` or
     `branchall` completes when its last child reports, and its `Failure`
     entry names that child's job: the last child to **complete**, whatever
     its own outcome, which under real parallelism is not always the last
     to fail and can be a child that succeeded. The retry takes that job as
     the failed attempt and its stored input as the base for **every**
     child the re-entry pushes: each parallel iteration gets that input
     with its `iter` moved to `iter_parent` and its own `iter` added; each
     branch of a parallel `branchall` gets it unchanged, and since every
     branch was pushed with the flow's input, that is the flow's input
     again, so the base cannot be seen there. The module lists no failed
     attempt once its retry has started: a parallel container's completed
     entry always has an empty `failed_retries`
     ([FX-27](execution.md#fx-27)). The attempt stays only in the flow-wide
     list: if the retried container fails for good, the failure module's
     entry lists it; if the container succeeds, it is listed nowhere.
- **Evidence:** `worker_flow.rs:3720-3790`, `:3723-3790` (retry: reset,
  restoration; `"{}"` fallback at `:3768-3784`), `:3737`, `:3891-3908`
  (input from the last failed job), `:4158-4170` (`iter` insertion renames
  the existing `iter`), `:6234-6262` (only successful preceding steps are
  retrieved). The first push only: `:3737-3795` (the base is taken only
  while the module's entry is a `Failure`), `:3964` (otherwise a container
  starts from the flow's input), `:4158-4205` (a sequential iteration's
  args, then a collapsed body's transforms over them). The attempt:
  `:1334-1345` (the sequential entry names the completing child and copies
  the list), `:1037-1061` (a parallel container's entry names the child
  whose completion finished it, with an empty list), `:766-773`,
  `:917` (it finishes when the last child reports), `:4152-4157` (each
  branch of a `branchall` gets the base as is), `:4206-4262` (each parallel
  iteration gets the base plus its own `iter`).
- **Conformance:** [CF-22/a to CF-22/h](conformance.md#cf-22)
- **Revision 2026-10-29 (SC4, recordings of milestones NI and N5, source
  re-check):** milestone NI recorded five retried containers (fixture
  `control-flow/cf-22`, runs `iter_parent`, `collapsed_input`, `exhausted`,
  `branchall` in oracle run 40857510216 and `prev_fallback` in oracle run
  40852609901) and milestone N5 two more (fixture
  `control-flow/subflow-retry`, runs `while_retry` and `branchone_retry`,
  oracle run 40915682847). CF-22/a is recorded as stated
  ([Q-CF-7](#q-cf-7)), and rule 2 too. Rule 3 said that the re-run children
  take the failed child's input; the collapsed run showed that only the
  re-entry's first push does, and that a collapsed body's transforms read
  the failed job's arguments, so rule 3 is corrected. Rule 4 and the
  parallel rule 5 are new; rule 5 comes from the source (milestone NI
  had read the base and the attempt as the last child to fail, with no
  recording to tell the two readings apart). New cases CF-22/b to
  CF-22/h.

<a id="cf-23"></a>
### CF-23 · Errors raised while starting a container

**Scope:** core (that the container fails; the message text); later (route-dependent error name and handler bypass) · **Confidence:** high (routing; the route 1 and route 3 forms, oracle-observed); high (the parent's recorded value on route 3, source-traced) · **Status:** confirmed; drafted (the parent's value on route 3)

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
     run. If it is a child, its parent sees a failed child, but what it
     gets as that child's value depends on the parent module:
     - **a loop or `branchall`** reads its children's results from storage
       when it aggregates, so the aborted child's entry is the stored
       `{"error": {"name": "InternalError", "message": "<message>"}}`;
     - **a `branchone` or a subflow step** (`type: flow`) has a single child
       and takes the value delivered with the notification, which is the
       aborted flow's **last carried value**: the result of the inner step
       that completed just before the failing start, not the error. The
       module's status entry becomes `Failure` naming the child job, and
       every in-memory use of the step's value receives the carried value
       as `previous_result` (and, in `retry_if`, also as `result`): the
       parent's `retry_if`, its `continue_on_error` (the next step's
       `previous_result`), its failure module's `previous_result`
       ([FX-32](execution.md#fx-32), [FX-8](execution.md#fx-8)), and, when
       the parent flow ends on this failure, the parent's own stored
       result, which is then that value with a failure status and no
       `error` member. An `error` named in those expressions is **not** the
       carried value: it is derived from `previous_result` by
       [JE-17](expressions.md#je-17)'s decision list, so a carried `1` gives
       `{"name": "UnknownError", "message": "Could not parse the error", "error": 1}`,
       a falsy carried value `{"name": "UnknownError", "message": "No error found"}`,
       and a carried object with a truthy `error` member that member
       ([CF-23/e, CF-23/f](conformance.md#cf-23)). Lookups that read storage see the error instead:
       `results.<branchone id>` from a later step ([JE-18](expressions.md#je-18)
       step 2) and the direct lookup ([JE-19](expressions.md#je-19)) fetch
       the child job's stored `InternalError` object
       ([CF-23/d](conformance.md#cf-23)). This is the same split as
       [FX-V2](execution.md#fx-v2): the stored child result and the module's
       effective value differ.

  `branchone` predicate errors are the exception: they always become a
  failure of the branchone step ([CF-14](#cf-14)).
- **Evidence:** `backend/windmill-worker/src/worker.rs:3822-3840`;
  `backend/windmill-worker/src/result_processor.rs:1910-1978` (root:
  failed-step update on itself; child: complete, then notify the parent);
  `worker_flow.rs:2086-2121` (error while chaining → `InternalError`),
  `:2132-2154` (the parent is notified as failed with the aborted flow's
  carried value), `:1478-1488` (a module with a list of child jobs
  re-reads their stored results; any other takes the delivered value),
  `:4703-4708` (a `branchone` records no list of child jobs),
  `:2039-2056` (a failed flow stores that value as its result), `:1459-1474` and `backend/windmill-types/src/flow_status.rs:454-458`
  (lookups map the module to the child job, whose stored result is read),
  `:2135-2150`, `:4053-4080` (`StepFailure` path for predicates);
  `backend/windmill-common/src/error.rs:145-150`;
  `backend/windmill-common/src/worker.rs:2228-2232`; test
  `backend/tests/worker.rs:5468` (an inner iterator error inside a while body
  fails the outer flow).
- **Conformance:** [CF-23/a to CF-23/f](conformance.md#cf-23)
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
- **Revision 2026-10-29 (SC3, source re-check, chain N3b, Q-CF-3 part 2):**
  route 3's parent value is now traced: a `branchone` or subflow parent
  takes the aborted child's last carried value, not the stored error,
  while a loop or `branchall` re-reads storage. Evidence:
  `backend/windmill-worker/src/worker_flow.rs:2086-2154` (the chaining
  abort stores the `InternalError` and notifies the parent with the
  carried result and a failure), `:1478-1488`, `:2039-2056` (aggregation
  reads storage), `:1459-1474` (the scoped map), `:4703-4708`;
  `backend/windmill-types/src/flow_status.rs:454-458`. New case CF-23/d.
  Milestone N3b's provisional reading, the stored `InternalError`, is the
  storage view only.
- **Revision 2026-10-29 (SC3 validation round 1, finding 4):** the
  revision above listed the failure module's `error` among the values that
  equal the carried value. `error` is derived from `previous_result` by
  [JE-17](expressions.md#je-17)'s rules
  (`backend/windmill-jseval/src/lib.rs:494-526`), as the recordings of
  JE-17/b and JE-17/c show for other carried values; the carried value is
  `previous_result` (and `result` in `retry_if`). New source-derived cases
  CF-23/e (a failure module reading both) and CF-23/f (a `retry_if` reading
  both).
- **Revision 2026-10-29 (SC4, recordings of milestones NI and N3c):**
  CF-23/a and CF-23/b were recorded as stated by milestone NI (fixture
  `conformance/flows/control-flow/cf-23`, runs `root_pickup` and
  `chaining`, oracle run 40852609901), and CF-23/c, which is CF-V3/a, by
  milestone N3c. CF-23/d to CF-23/f stay unrecorded.

<a id="cf-24"></a>
### CF-24 · Child job paths and step ids

**Scope:** later · **Confidence:** high; high (oracle-observed: the deployed paths, CF-24/a, /b, /d, /e) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone NI):** CF-24/a,
  CF-24/b, CF-24/d and CF-24/e were recorded as stated (fixture
  `conformance/flows/control-flow/cf-24`, runs `x`, `x_custom`, `d` and `e`,
  oracle run 40852609901). CF-24/c needs a raw flow payload, outside the
  reference configuration, and is not recorded.

<a id="cf-25"></a>
### CF-25 · `squash`

**Scope:** EE · **Confidence:** high (when the runners are requested; the stub's text in a build without `private`); high (the six recorded CE outcomes, oracle-observed); low (what the CE image's runner does across iterations, [Q-CF-19](#q-cf-19)); low (EE semantics) · **Status:** confirmed; drafted (the conditions, the stub's error text, and the job kinds that request runners)

- **Rule:** `squash: true` on a sequential loop asks Windmill to run all
  iterations on one dedicated runner. The worker decides from public code
  **when** it requests the runners (below); what the request does comes from
  a module that depends on the build:
  - **A build without the `private` feature** (the pure OSS build,
    `features=oss`) has only a stub, and starting such a loop fails with
    `Squashing flow loops is not available in OSS` (the full text below).
  - **The published CE image** is built with `features=ce`, which includes
    `private` ([OR-3](oracle.md#or-3)): its release build copies in the
    closed-source runner module, and the stub is not compiled. The public
    source cannot say what that module does. The oracle recorded six
    squashed loops on the CE image ([CF-25/d](conformance.md#cf-25)): a
    sequential `forloopflow` at step 0 and after a step, a
    `whileloopflow`, a loop whose one-step body collapses, a parallel loop
    and an empty loop. Each completed with the result it gives without
    `squash`, none failed with the stub's text, and the flow job's stored
    value kept `squash: true` (seen in the raw capture). Their iteration jobs have the kinds the
    deployed body gives ([CF-26](#cf-26), [CF-V5](#cf-v5)): a `flownode`
    child per iteration for the two-step bodies (step 0, after a step, the
    while loop), the step itself as a `script` child for the collapsed
    body, a `flowpreview` child per iteration for the parallel loop's
    one-step body, which stays inline, and none for the empty loop.
  - **What those recordings do not show.** They do not show that the CE
    image runs a squashed loop as an ordinary loop, nor that the flag has
    no effect there. Each recorded sequential loop that starts the runners
    ran a single iteration (the three for loops iterate over `[1]`, the
    while loop stops after its first), the parallel loop never starts them,
    the empty loop runs no iteration, and the body only echoes its input.
    Job counts and kinds cannot tell the paths apart either: the squash
    path queues the ordinary iteration job before it starts the runners,
    and a worker hands such a job to a runner without changing its kind.
    Whether the CE image's runner reuses one runtime across iterations is
    [Q-CF-19](#q-cf-19).

  Empty loops start no runners, and parallel loops do not take the squash
  start path, in every build. Whether squash changes data semantics in EE
  (for example module state shared across iterations) is unknown
  ([Q-CF-11](#q-cf-11)).

  **When the runners are requested, exactly.** The attempt is made when the loop's first
  iteration is started: a `forloopflow` with `parallel` false whose
  iterator is not empty, or a `whileloopflow` (always sequential), in both
  cases with a body that is not empty, collapsed or not ([CF-8](#cf-8)).
  The squash flag is ignored, and the loop runs normally, when the loop is
  parallel, when its iterator or body is empty, and when the loop module
  itself has a `sleep` or a `suspend`: those settings keep the worker from
  continuing in place, and the runners are only started on that path. Nothing
  refuses `squash` at deploy time. The request is made **after** the
  first iteration's job has been queued and committed; where the request
  fails, that job is not withdrawn on this path (what becomes of it is not
  traced).

  **The error text where the stub is compiled** (a build without
  `private`; not the CE image). The stub's located internal error is wrapped with
  the context `failed to spawn flow module runners for job: <id>`, where
  `<id>` is the UUID of the flow job that runs the loop, and converted
  into an `Anyhow` error ([FX-50](execution.md#fx-50)). It then takes the
  container-start routes of [CF-23](#cf-23):
  - at a root flow's pickup (route 1, a failure of the loop module that
    the flow's retry, `continue_on_error` and failure module handle):
    name `Anyhow`, message
    `Error: Error: failed to spawn flow module runners for job: <id>: Internal: Squashing flow loops is not available in OSS<loc><loc><loc>`;
  - at a child flow's pickup (route 2): the same name and message on the
    child;
  - during chaining (route 3, the flow aborts): name `InternalError`,
    message
    `Error: failed to spawn flow module runners for job: <id>: Internal: Squashing flow loops is not available in OSS<loc><loc>`.

  The innermost `<loc>` is the stub's own location
  (` @dedicated_worker_oss.rs:<line>:<col>`). Because the context text
  stands between the leading `Error: ` prefixes and the `Internal: ` layer,
  [FX-50](execution.md#fx-50)'s count sees one fewer leading prefix than
  there are suffixes, so its masking leaves that innermost suffix in place;
  a comparison must write it as a literal or mask it explicitly, and must
  normalise `<id>`.
- **Evidence:** `worker_flow.rs:5265`, `:5336`, `:4080-4095`, `:4800-4830`;
  `backend/windmill-worker/src/dedicated_worker_oss.rs:5-26`; public docs
  <https://www.windmill.dev/docs/flows/flow_loops> ("Squash … all iterations
  run on the same worker", "can't be combined with parallel"). The
  conditions: `worker_flow.rs:5247-5289` (a while loop asks at its first
  iteration), `:5294-5300`, `:5326-5348` (a sequential for loop asks at
  index 0; a parallel one never does), `:5671-5697`, `:5700-5727` (both
  collapsed and child-flow bodies carry the request; an empty body
  returns before it), `:4081-4096` (runners only on the continue-in-place
  path, which a `sleep` or `suspend` on the module turns off), `:4800`
  (the queued jobs are committed first), `:4803-4824` (the call, its
  context text and the conversion; the call names the stub module, which
  re-exports the closed-source module when `private` is set,
  `dedicated_worker_oss.rs:1-3`, and `backend/windmill-worker/src/lib.rs:36-38`
  compiles `dedicated_worker_ee` only with `private`; that file is not in
  the public tree); the features: `backend/Cargo.toml:182-199` (`ce`
  includes `ce_core`, which includes `private`),
  `.github/workflows/docker-image.yml:72-74`, `:93-95` (closed-source
  modules copied in, then built with `features=ce`); the text: `dedicated_worker_oss.rs:23-25`
  (a located internal error), `backend/windmill-common/src/error.rs:61-62`,
  `:73-74`, `:146-150`, `:233-239`; `worker_flow.rs:2960-2977` (the start
  phase's error leaves the flow handler as a generic error),
  `backend/windmill-worker/src/worker.rs:3822-3840` (pickup conversion),
  `worker_flow.rs:2103-2104` (chaining stores the display)
- **Conformance:** [CF-25/a–CF-25/d](conformance.md#cf-25)
- **Revision 2026-10-29 (SC3, source re-check, chain N3a):** milestone N3a
  refuses `squash` and asked whether CE has any route for it. It has none
  that runs squashed: CE fails the loop at its first iteration, or ignores
  the flag where the runners are never started (parallel, empty, or a loop
  module with `sleep` or `suspend`). Added the exact conditions, the full
  error text per route and the masking caveat. New source-derived case
  CF-25/c.
- **Revision 2026-10-29 (SC3, source re-check of the CE build, chain N3a):** the earlier
  text, and the revision just above, said that "CE" fails a squashed loop
  with the stub's message. That holds only for a build without the
  `private` feature. The published CE image is built with `private`, so the
  stub is replaced by the closed-source runner module, and the public
  source cannot say what the CE image does with a sequential squashed loop.
  What stays source-derived and holds on CE: when the runners are requested,
  and that the flag has no effect on a parallel loop, an empty loop, or a
  loop module with `sleep` or `suspend` (CF-25/c). CF-25/a is narrowed to
  the build without `private`, and the CE image's outcome is the new
  [Q-CF-19](#q-cf-19). Answer to N3a's question: CE has no route that
  *refuses* `squash` at deploy time; the source shows CE ignoring it where
  no runners start, and only a recording can show what a sequential
  squashed loop does on the CE image.
- **Revision 2026-10-29 (SC3 validation round 1, recording by milestone
  NI):** that recording exists: milestone NI's fixture `conformance/flows/control-flow/cf-25`, runs `step0`, `later`, `while`, `collapsed`, `empty` (oracle run 40857510216) and `parallel` (oracle run 40852609901) ran every
  squashed loop on the CE image as an ordinary loop, with ordinary results
  and job kinds; the stored flow kept `squash: true`. The rule now states
  it, new case CF-25/d records it, and [Q-CF-19](#q-cf-19) is answered.
  CF-25/a still describes only a build without `private`.
- **Revision 2026-10-29 (SC3 validation round 2, findings 2 and 3):**
  the revision just above read more into NI's recordings than they
  show. They establish six results and the absence of the stub's error,
  not that the CE image runs a squashed loop as an ordinary loop: every
  recorded sequential loop that starts the runners has one iteration,
  and the squash path queues the ordinary iteration job before starting
  the runners (`backend/windmill-worker/src/worker_flow.rs:4793-4839`),
  which a worker then hands to a runner without changing its kind
  (`backend/windmill-worker/src/worker.rs:2879-2905`). The rule now says
  only what the runs show, and [Q-CF-19](#q-cf-19) is re-scoped to
  runtime reuse. The job kinds are now stated per run: the parallel
  loop's one-step body stays inline at deploy, so its iterations are
  `flowpreview` children, not `flownode`
  (`backend/windmill-worker/src/worker_lockfiles.rs:1571-1591`;
  `worker_flow.rs:5038-5073`, `:5364-5380`; the collapse is for
  sequential loops only, `:5298-5300`).
- **Revision 2026-10-29 (SC4, source re-check, milestone NI):** NI asked
  whether CE v1.767.0 ever rejects a squashed loop, and in which job kind.
  As far as the public source goes: **no job kind is singled out.** The
  runner request is made by whichever flow job runs the loop (a root `Flow`
  job by path, a `FlowPreview` preview or inline body, a `FlowNode` stored
  body, a by-path subflow's `Flow` job, or the failure slot,
  [CF-31](#cf-31)), under the conditions above and nothing else; the only
  other places that read `squash` are the decoder and the dependency job,
  which copies the field into the locked flow; nothing at deploy or enqueue
  refuses it. A build without `private` therefore fails every such loop at
  its first iteration, in any job kind, by the routes above. Whether the CE
  image ever rejects one cannot be read from the public source: the
  runner's module is closed source. Windmill's own runner tests are
  compiled only with both `private` and `enterprise`, so they describe an
  EE build, not the CE image: there each step of a squashed loop runs in a
  dedicated process that writes a log line starting `dedicated worker
  <language>:` into the iteration jobs' logs, and a step in a language the
  runners do not support (a Bash step) falls back to ordinary execution for
  each iteration. The six recorded runs (CF-25/d) show no rejection.
  [Q-CF-19](#q-cf-19) stays open, with that log line added to what its
  recording observes. Evidence: `worker_flow.rs:4081-4096`, `:4803-4824`
  (the request, with no job-kind test), `:5247-5265`, `:5294-5336`;
  `backend/windmill-worker/src/worker_lockfiles.rs:1055`, `:1087`,
  `:1126-1154` (the field copied while locking);
  `backend/windmill-types/src/flows.rs:996-1005`, `:1096-1145` (decoded);
  `backend/windmill-worker/src/worker.rs:2879-2905` (a job is handed to a
  runner by its step id, whatever its kind); `backend/tests/dedicated_workers.rs:1`
  (the tests' features), `:28-71`, `:299-341`, `:711-758`
  (the log line, a squashed Bun loop, the Bash fallback);
  `backend/tests/fixtures/dedicated_flows.sql:163-172`.

<a id="cf-26"></a>
### CF-26 · Representation and restart details

**Scope:** core (virtual identity, handler id, script path, job kind); out (restart, storage, worker tags) · **Confidence:** high; high (oracle-observed: a body's virtual identity step) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone NI):** CF-26/a was
  recorded as stated (fixture `conformance/flows/control-flow/cf-26`, run
  `a`, oracle run 40852609901).

<a id="cf-27"></a>
### CF-27 · Container `timeout` enforcement

**Scope:** later · **Confidence:** high (oracle-observed: points 2 to 4 on the ordinary worker path); medium (the rest of the ordinary worker path); low (other execution modes) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone NI):** CF-27/a to
  CF-27/c were recorded as stated (fixture
  `conformance/flows/control-flow/cf-27`, runs `a`, `b` and `c`, oracle run
  40852609901), as were [FX-24/e](conformance.md#fx-24) and the deployed
  half of [FX-24/d](conformance.md#fx-24) (runs `fx24_e`, `fx24_d`): a
  collapsed body job is cut at the loop's `timeout` with the loop's id as
  `step_id`, and iterations that run as child flows, sequential or
  parallel, are not timed. CF-27/d needs a raw submission and is not
  recorded. Where the container's `timeout` is evaluated, and in which
  scope, is [CF-21](#cf-21).

<a id="cf-28"></a>
### CF-28 · Cancellation inside containers

**Scope:** later · **Confidence:** high (marking, pickup and root result; oracle-observed: held iterations and the container's entry); low (timing, deliberately unspecified) · **Status:** confirmed; drafted (steps waiting to start, added 2026-10-29 by SC4)

- **Rule:**
  1. **Normal cancel of a running flow.** The flow and every queued or
     running descendant, found through parent links at any depth up to 500
     levels, get cancellation markers. Holds created by `parallelism` are
     cleared and those jobs are scheduled immediately.
  2. **Queued or held descendants.** When picked up, they complete at once
     as failed with the cancellation error, **without running body code**.
     Their parents then see failed children. This includes a step whose job
     waits on a `sleep`, a retry delay or a `parallelism` hold, and a
     subflow job that has not started: none of its modules runs
     ([FX-38](execution.md#fx-38)).
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
       (canceler);
     - a container whose children were cancelled fails like any container
       with failed children: its entry is `Failure` and its result is the
       children's results read back, so a loop whose child flows were all
       cancelled holds one such `Canceled` object per iteration, the
       running iteration's included (its child flow completes `Canceled`
       whatever its killed step reported), and a held iteration's entry is
       the `Canceled` value it failed with at pickup.
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
  `worker_flow.rs:765-1115`, `:1734-1738`, `:1972-1988`, `:1478-1489` (a
  container's result is its children's results read back).
- **Conformance:** [CF-28/a](conformance.md#cf-28)
- **Revision 2026-10-29 (SC4, recordings of milestone NI):** CF-28/a was
  recorded (fixture `conformance/flows/control-flow/cf-28`, run `held`,
  oracle run 40852609901): canceled 8 s after submission, the held
  iterations ran no body code, the root ended `Canceled`, and the loop's
  entry was `Failure` with three `Canceled` entries, iteration 0's
  included, although its body job had ended with the killed process's
  `ExecutionErr`. Point 4 now states the container's entry, and CF-28/a's
  expectation names it.

<a id="cf-29"></a>
### CF-29 · An empty container's own `stop_after_if` stops the flow

**Scope:** core · **Confidence:** high (oracle-observed: the error stop of an empty loop) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** CF-29/a was
  recorded as stated (fixture `conformance/flows/control-flow/cf-29`, run
  `a`, oracle run 40858228112): the flow failed with
  `{"error":{"name":"EarlyStopError","message":"empty"}}`, the loop's own
  module ended `failure` with result `[]`, and `n` did not run.
  [Q-CF-1](#q-cf-1) is answered.

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
  `sleep` on list modules. What a container in the failure slot does at run
  time, beyond which of its loops collapse, is [CF-31](#cf-31).
- **Revision 2026-10-28 (validation round 1):** point 4 said that a
  container-valued slot's bodies always run as `FlowPreview` children, so
  that no forloop in a slot collapses. That confused the absence of a
  stored node with the absence of collapsing: the collapse check runs
  before any child is created and has no exclusion for the slots, so a
  slot that is itself an eligible loop collapses. The rule now separates
  the slot loop from loops nested in its child flows, and
  [CF-30/c](conformance.md#cf-30) checks the direct case.

<a id="cf-31"></a>
### CF-31 · A container as the `failure_module`

**Scope:** later · **Confidence:** high (the status bookkeeping and its effect on sequential loops, sequential `branchall` and `branchone`; source-traced); medium (empty bodies, a throwing `branchone` predicate, nesting through the copied handler, a retry on the slot: read, not traced end to end); low (how the parallel case finally ends, which depends on the server's monitor) · **Status:** drafted

- **Rule:** nothing refuses a container in the failure slot. The flow
  decoder checks only `retry` settings and workspace paths inside the
  slot, the dependency
  job locks the slot's bodies like any module's ([CF-30](#cf-30) point 1),
  and OpenFlow's description of the slot ("only supports script/rawscript
  types") is not enforced. At run time a `forloopflow`, `whileloopflow`,
  `branchone` or `branchall` in the slot starts like any failure module, but
  the bookkeeping that advances it was written for list modules, which gives
  the following behaviour. An `identity` or a `flow` step by path in the slot
  behaves like a script failure module: its result is the handler's result.
  1. **Start and scope.** It starts when a script failure module would
     ([FX-31](execution.md#fx-31)). Its iterator, its `branchone`
     predicates and its own `skip_if` see what a script failure module's
     input transforms see ([FX-32](execution.md#fx-32)): `previous_result`
     and `result` are the triggering value, `error` is derived from it,
     `flow_input` is the flow input, and `results` treats the failed module
     as the preceding step. Its bodies run as child flows built from the raw
     module values: never collapsed and never stored nodes, since the flow
     has a failure module (point 4 of [CF-30](#cf-30)), so each child is a
     `flowpreview` job and an inline script in it a `preview` job. A child's
     `flow_input` is the flow input (plus `iter` for a loop) and its first
     step's `previous_result` is that input, not the error ([CF-9](#cf-9));
     a body reaches the error through `iter.value` when the iterator carries
     it, or through `results.<failed id>` ([CF-19](#cf-19)). Each child
     carries a copy of the flow's handler, which is this same container
     ([CF-16](#cf-16)).
  2. **Its state is looked up by list position.** The slot's state is kept
     in the flow status's `failure_module` entry while the cursor points
     past the module list. But the code that advances a loop or a
     `branchall` after a child completes, and the decision to continue, read
     the cursor as a list position past the last module. So:
     - **A sequential `forloopflow` with more than one item, a
       `whileloopflow`, or a sequential `branchall` with more than one
       branch** runs its first child only. When that child succeeds, the flow
       starts no further child and completes at once: its result is that
       child's result (not an array), the `recover` decode of
       [FX-33](execution.md#fx-33) is applied to that value, and the
       `failure_module` entry stays `InProgress`. A while loop in the slot
       therefore always ends after its first successful iteration; its
       `stop_after_if` is not evaluated there. When the first child fails,
       the slot fails with the one-element array `[<child result>]`, and the
       flow fails with that array (unless the slot's `retry` applies, point
       6).
     - **A loop over exactly one item, or a sequential `branchall` with one
       branch,** completes normally with the one-element array, so the flow
       recovers only when that child returned exactly `true` (`[true]`
       decodes as the recovery record, [FX-33](execution.md#fx-33)).
     - **A `branchone`** behaves like a script failure module: the chosen
       branch's child-flow result is the handler's result and is decoded for
       `recover`.
     - **A parallel `forloopflow` or parallel `branchall`** never records a
       child's completion: the status update reads the counter from the
       module list, finds no entry, and fails with
       `requiring an index in InProgress for flow <id> at step <n>` (`<n>`
       the module count); the update is retried once with that error and
       fails the same way. The flow stays running until the server's monitor
       finds its last ping older than `FLOW_ZOMBIE_TRANSITION_TIMEOUT` (60 s
       by default) and cancels it, so it ends with a `Canceled` error whose
       canceler is `monitor` (low: not traced end to end).
  3. **A body that starts no child.** An empty iterator, a `branchall` with
     no branches, or a `branchone` whose chosen body is empty records a
     `Success` entry carrying the slot's id (normally `failure`) **in the
     failed module's place** in the module list, where the cursor still
     points. The flow then treats the failed module as having succeeded, with
     `[]` as its value for a loop or `branchall` and the triggering value for
     a `branchone`: if it was the last module, the flow **succeeds** with
     that value and no `recover` decode; otherwise the next module runs with
     it as `previous_result`. When the slot is entered with the cursor
     already past the list (a tolerated error stop at the last module,
     [FX-V4](execution.md#fx-v4)), the entry is appended after the module
     list instead, the slot is started again, and the flow never completes
     (medium).
  4. **Errors while starting it.** An iterator that throws or is not an
     array aborts the flow by [CF-23](#cf-23) route 3 (`InternalError`,
     without running anything). A `branchone` predicate that throws is
     recorded as a new failure of the **failed module**, with the
     predicate's `{"error": …}` as its value, and the flow re-enters the
     failure module with that value as `previous_result`; a predicate that
     throws on it again repeats this without end and creates no job
     (medium).
  5. **Failures inside its children.** A body step that fails inside a child
     runs that child's copy of the handler, the same container, inside the
     child ([CF-16](#cf-16) point 1), under the same rules; a body that
     always fails nests a new level each time, with no depth limit, until
     the flow is canceled (medium). A clean stop inside a body breaks a loop
     slot, whose result is then the array so far, and ends the flow as
     stopped in a `branchone` slot ([CF-12](#cf-12)).
  6. **Settings on the slot.** Its `continue_on_error` is ignored, as for
     any failure module ([FX-30](execution.md#fx-30)). Its `retry` applies
     to the failed slot like a script failure module's
     ([FX-32](execution.md#fx-32)): the container starts again, its
     iterator is evaluated again with `previous_result` set to the slot's
     own failed value (the array), and the first child it pushes receives
     the last failed child's stored input, so its `iter` moves to
     `iter_parent` ([CF-22](#cf-22) rule 3) (medium).
  7. **What the rest of the flow sees.** The flow cache stores the final
     value when the last completion succeeded, before the `recover` check
     ([FX-40](execution.md#fx-40)): a slot that stopped after a successful
     first child caches that child's result. A child flow that ended in its
     container handler (points 2 and 5) reports to its container a failure
     its handler ended, so the enclosing flow does not run its handler again
     ([CF-16](#cf-16) point 4, point 8); after point 3's route the child
     instead succeeds, and its container sees a successful child.

  The preprocessor slot keeps its own status the same way; how a container
  preprocessor completes is not traced here ([CF-30](#cf-30) point 4 covers
  only which of its loops collapse).
- **Evidence:** the slot: `backend/windmill-types/src/flows.rs:222-262`
  (the decoder walks both slots as module trees, checking only `retry`
  settings and workspace paths), `:268-272`;
  `backend/windmill-worker/src/worker_lockfiles.rs:934-963` (locked like a
  module); `openflow.openapi.yaml:54-56` (the unenforced description). Start
  and scope: `worker_flow.rs:3796-3829` (the failure module replaces the
  failed one), `:3325-3327` (triggering value), `:3964` (a container's
  args are the flow input), `:4152-4170` (`iter` added), `:5038-5074` (the
  child carries the flow's handler), `:5248-5249`, `:5298-5300`,
  `:5729-5745` (no collapse with a failure module), `:5450-5481` (predicates),
  `:5770-5803` (iterator). The position lookup: `:492-496` (no current
  module for the slot), `:560` (the slot's status), `:565-582` (loop flags;
  `skip_failures` and `parallelism` come from the absent current module),
  `:596-597`, `:678-679`, `:1133-1189` (sequential arms: no new status, no
  advance), `:1195-1201`, `:1369-1397` (the cursor stays at the module
  count, read as past the last module), `:1399-1430` (the slot's status
  write), `:1478-1489`, `:1734-1758` (no continuation),
  `:1807-1811` (the log line), `:1992-2006` (cache store, then `recover`),
  `:2140-2153` (what the parent hears), `:4723-4742` (the slot's status at
  start); `:299-301`, `:409-412` (the recovery decode). Parallel:
  `:774-821` (counter read from the module list; the text at `:821`),
  `:846-906`, `:163-240` (the update retried once with the error);
  `backend/windmill-worker/src/result_processor.rs:1520-1541`,
  `:691-735`, `:1910-2000` (the error is logged, nothing completes the
  flow); `backend/src/monitor.rs:172-177` (60 s default), `:4867-4890`,
  `:5142-5170` (the monitor cancels it). Empty bodies: `worker_flow.rs:3998-4042`
  (the `Success` entry is written at the cursor, the failed module's index),
  `:3196-3221` (a `Success` entry ends the push with `[]` or the carried
  value), `backend/windmill-worker/src/result_processor.rs:463-488` (that
  update); `backend/windmill-types/src/flows.rs:381-389` (a cursor past the
  list is the failure step). Predicate errors: `worker_flow.rs:4053-4077`,
  `:5479-5482`. Retry on the slot: `:3723-3796` (no restore of
  `previous_result` for the failure step, args from the last failed job),
  `:3891-3905`.
- **Conformance:** [CF-31/a to CF-31/e](conformance.md#cf-31)
- **Note:** milestone N3c refuses a container in the slot until the
  reference records it; this finding states Windmill's behaviour. Windmill's
  outcome depends on the container kind and on whether the body starts a
  child, so a refusal remains a defensible windmill-lite policy if recorded
  as a deviation. Recording requested for batch E3f
  ([docs/design/README.md](../design/README.md)).
- **Revision 2026-10-29 (SC4, source re-check, milestone N3c):** new
  finding, answering N3c's open question 3. It extends [CF-30](#cf-30),
  which described only which loops in a container slot collapse.

## B. Findings added during validation

<a id="cf-v1"></a>
### CF-V1 · Recovery success differs between sequential propagation and parallel aggregation

**Scope:** core · **Confidence:** high (both paths traced; oracle-observed) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** recorded as
  stated, for loops and `branchall` (fixture
  `conformance/flows/control-flow/cf-v1`, runs `a`, `b`, `c-seq` and
  `c-par`, oracle run 40858228112): the sequential containers failed with
  `[{"recover":true}]` after their first child, the parallel ones succeeded
  with both children's `{"recover":true}`. [Q-CF-1](#q-cf-1) is answered.

<a id="cf-v2"></a>
### CF-V2 · Optimized empty containers bypass the all-iterations stop predicate

**Scope:** core · **Confidence:** high (oracle-observed: the empty iterator and the zero-branch `branchall`, both predicates) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** the rest of
  the cases recorded as stated (fixture `conformance/flows/control-flow/cf-v2`,
  runs `a`, `b`, `c-all` and `c-stop`, oracle run 40858228112): an empty loop
  and a zero-branch `branchall` skip `stop_after_all_iters_if`, and their own
  `stop_after_if` stops the flow with `[]`. [Q-CF-1](#q-cf-1) is answered.

<a id="cf-v3"></a>
### CF-V3 · Child-flow startup errors bypass that child's failure module

**Scope:** core (nested error and handler behaviour); later (location-suffix fidelity) · **Confidence:** high (routing traced; oracle-observed: both cases; the location suffix is build-dependent) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** both cases
  were recorded (fixture `conformance/flows/control-flow/cf-v3`, runs `a`
  and `b`, oracle run 40858228112): called by path, the child failed at its
  first pickup with
  `{"error":{"name":"Anyhow","message":"Error: Expected an array value in the iterator expression, found: invalid type: map, expected a sequence at line 1 column 0 @worker.rs:3822:13"}}`
  without running its handler, and the caller's handler ran; run directly,
  its own handler ran and the flow failed with `"child-handled"`. CF-V3/b's
  expectation, which said the handler "may" run, now gives that outcome.

<a id="cf-v4"></a>
### CF-V4 · Stored child nodes do not receive runtime handler-id suffixes

**Scope:** core (observable handler step id); out (worker-tag scheduling) · **Confidence:** high (oracle-observed for the stored-node id, CF-V4/a included) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** CF-V4/a was
  recorded with its own setup (fixture
  `conformance/flows/control-flow/cf-v4`, run `a`, oracle run 40858228112):
  the copy of handler `h` in a stored body node at position zero kept the
  id `h`. CF-V4/b needs an inline child payload, outside the reference
  configuration, and stays unrecorded.

<a id="cf-v5"></a>
### CF-V5 · For-loop fast-path eligibility follows runtime job kind

**Scope:** core · **Confidence:** high; high (oracle-observed: the by-path collapse) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone N3c):** CF-V5/a was
  recorded as stated (fixture `conformance/flows/control-flow/cf-v5`, run
  `a`, oracle run 40858228112): each iteration ran as a script job of the
  flow, with the previous iteration's result as its `previous_result`.
  CF-V5/b, a raw-flow preview, is outside the reference configuration and
  stays unrecorded.

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
  kinds. *2026-10-29:* batch E3e recorded CF-6/b with a pull read-back
  (fixture `conformance/flows/control-flow/pull`, oracle run 40794872025).
  It recorded no other case of this list (its probe fixture cites `OR-`
  cases only), so CF-V1/a to CF-V1/c, CF-V2/b, CF-V2/c and CF-29/a remain;
  still open. *2026-10-29 (SC3):* no part of these is left for the source;
  their recording is requested for batch E3f
  ([docs/design/README.md](../design/README.md)).
  **Answered 2026-10-29 (SC4).** Milestone N3c had already recorded every
  remaining case, each as the catalogue expects (oracle run 40858228112):
  CF-V1/a to CF-V1/c (fixture `conformance/flows/control-flow/cf-v1`, runs
  `a`, `b`, `c-seq` and `c-par`), CF-V2/b and CF-V2/c
  (`conformance/flows/control-flow/cf-v2`, runs `b`, `c-all` and `c-stop`)
  and CF-29/a (`conformance/flows/control-flow/cf-29`, run `a`); the cases
  are now `oracle-observed`. No E3f recording is needed for them. CF-6/c
  and the raw halves of CF-8 stay outside the reference configuration, and
  CF-V4's cases are tracked under [CF-V4](#cf-v4). Status: closed.
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
  **Answered 2026-10-29 (SC3) for part (2), from the source.** A
  `branchone` or subflow parent takes the aborted child's last carried
  value, not the error: its module becomes `Failure` naming the child job,
  every in-memory use of the step's value (the parent's `retry_if`,
  `continue_on_error`, failure module, and its own final result when it
  ends on this failure) receives the carried value as `previous_result`,
  with any `error` derived from it by [JE-17](expressions.md#je-17)'s rules
  rather than equal to it (corrected in validation round 1), and only
  storage lookups of the step see the child's stored `InternalError`; a loop or
  `branchall` parent re-reads its children from storage, so its entry is
  the error ([CF-23](#cf-23) route 3, new
  [CF-23/d](conformance.md#cf-23)). Milestone N3b's provisional reading,
  the stored `InternalError`, differs. A confirming recording of CF-23/d
  is requested for batch E3f ([docs/design/README.md](../design/README.md)).
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
  **Answered 2026-10-29 (SC4)** by milestone NI's recordings (fixture
  `conformance/flows/control-flow/cf-21`, runs `skip_mid`, `skip_mid_flows`,
  `skip_mid_while` and `skip_parallel`, oracle run 40852609901): a
  sequential loop re-evaluates its `skip_if` before each iteration with the
  previous iteration's result, collapsed, as child flows and as a while
  loop, and ends as a skipped identity with that result; a parallel loop
  evaluates it once ([CF-21/a to CF-21/c](conformance.md#cf-21)). Status:
  closed.
<a id="q-cf-7"></a>
- **Q-CF-7** · affects [CF-22](#cf-22) · **Answered 2026-10-29 (SC4).**
  The artefact is real: milestone NI's run `iter_parent` of fixture
  `conformance/flows/control-flow/cf-22` (oracle run 40857510216) gave
  `[{"index":0,"value":1}]`, as [CF-22/a](conformance.md#cf-22) predicted.
  Its other runs and milestone N5's `while_retry` and `branchone_retry`
  also showed that only the re-entry's first push takes the failed job's
  input, which corrects CF-22's rule 3 ([CF-22/b to CF-22/g](conformance.md#cf-22)).
  What a retried **parallel** container re-enters from is traced in the
  source (rule 5) but not recorded; [CF-22/h](conformance.md#cf-22) is
  requested for batch E3f ([docs/design/README.md](../design/README.md)).
  As first asked: the `iter_parent` artefact on container retry, settled
  by an oracle run of CF-22/a.
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
  [Q-CF-1](#q-cf-1). *2026-10-29 (SC4):* milestone N5 recorded a retry
  on a `flow` step, now
  [CF-17/b to CF-17/i](conformance.md#cf-17) (fixture
  `conformance/flows/control-flow/subflow-retry`, oracle runs 40915682847
  and 40926159329); the new source-derived CF-17/j (a flow calling itself
  by path) joins the list above. Still open.
  *2026-10-29 (SC4, milestone N3c):* the new source-derived cases
  [CF-31/a to CF-31/e](conformance.md#cf-31) (a container as the failure
  module), [CF-16/e, CF-16/f](conformance.md#cf-16) (a handled failure
  under the container's `continue_on_error` and `retry`),
  [CF-12/g](conformance.md#cf-12) (a forwarded error stop and the loop's
  `continue_on_error`) and [CF-7/f](conformance.md#cf-7) (a failed parallel
  loop under `continue_on_error`) join the list; their recording is
  requested for batch E3f ([docs/design/README.md](../design/README.md)).
  *2026-10-29 (SC4, recordings of milestones N3c and NI):* the milestones'
  committed fixtures under `conformance/flows/control-flow/` recorded, as
  the catalogue states them, CF-16/a and CF-16/b (`cf-16`), CF-17/a
  (`cf-17`), CF-18/a (`cf-18`), CF-20/a (`cf-20`), CF-21/a (`cf-21`),
  CF-23/a and CF-23/b (`cf-23`), CF-24/a, CF-24/b, CF-24/d and CF-24/e
  (`cf-24`), CF-26/a (`cf-26`), CF-27/a to CF-27/c (`cf-27`), CF-28/a
  (`cf-28`), CF-V3/a and CF-V3/b (`cf-v3`), CF-V4/a (`cf-v4`) and CF-V5/a
  (`cf-v5`) (oracle runs 40858228112, 40852609901 and 40857510216); CF-16/c,
  the stored-node half of CF-16/d and CF-23/c follow from the cases they
  point to, and the new CF-21/b to CF-21/e are recorded too. CF-24/c,
  CF-27/d, CF-V4/b and CF-V5/b need a raw or inline submission, and CF-25/a
  a build without the `private` feature: all are outside the reference
  configuration and will not be recorded. Still to record: CF-1/a (its
  deployed half), CF-30/a and CF-30/b, and the source-derived cases this
  revision added (CF-7/f, CF-12/g, CF-16/e, CF-16/f, CF-17/j, CF-22/h,
  CF-31/a to CF-31/e). Still open.
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
  **Re-scoped 2026-10-29.** Batch E3e
  added a second oracle session with two workers (fixtures declaring
  `oracle: {workers: 2}`, [OR-5](oracle.md#or-5)), so no workflow change is
  needed any more. It answered the concurrency half for an **unbounded**
  parallel loop: the body scripts of a two-iteration loop overlapped and
  the slow iteration 0 completed last, with the result in index order
  (fixture `conformance/flows/oracle/workers2`, run `loop_slow`, oracle run
  40792040448; [CF-7](#cf-7)). It did not record CF-7/a or CF-7/b on two
  workers, so what remains is the `parallelism` case: with `parallelism: 2`
  over four iterations, whether the released iterations' bodies run
  concurrently and in which order several eligible iterations start.
  *Settled by:* CF-7/a and CF-7/b as a fixture of the two-worker session,
  reading the body jobs' start and end times from the provenance. Still
  open.
  *2026-10-29 (SC3), source half:* each completion releases exactly one
  held iteration, in index order; a released iteration is picked up only
  when a worker next looks for released suspended jobs (at most about 5 s
  later on an idle worker), and that look orders by priority and then by
  the queue row's creation time, which all iterations of the loop share
  because they are queued in one transaction ([CF-7](#cf-7)). So the source
  fixes the release order but not the start order among several released
  iterations, and two released iterations can run at once on two workers.
  Only a recording can show the observed pattern.

<a id="q-cf-19"></a>
- **Q-CF-19** · affects [CF-25](#cf-25) · open (oracle) · What does the
  published CE image do with a sequential loop that has `squash: true`? The
  public source fixes when the runners are requested and what a build
  without the `private` feature does (it fails with the stub's
  `Squashing flow loops is not available in OSS`), but the CE image is
  built with `private` and compiles the closed-source runner module
  instead ([CF-25](#cf-25), [OR-3](oracle.md#or-3)). It may run the loop
  squashed, run it as an ordinary sequential loop, or fail with a text of
  its own. *Settled by:* an oracle run of a one-step flow whose step is a
  sequential `forloopflow` over `[1, 2]` with `squash: true` and a body
  `bun(v): return v` (`v = "flow_input.iter.value"`), and the same with a
  `whileloopflow` whose body stops after two iterations, recording the
  flow's result or error and the jobs created (a squashed loop runs its
  iterations on one runner, so the number and kind of iteration jobs tell
  the paths apart). Added 2026-10-29 (SC3). **Answered 2026-10-29 (SC3
  validation round 1)** by milestone NI's recordings (fixture `conformance/flows/control-flow/cf-25`, runs `step0`, `later`, `while`, `collapsed`, `empty` (oracle run 40857510216) and `parallel` (oracle run 40852609901)): the CE
  image runs every squashed loop as an ordinary loop, with the results and
  child job kinds of an unsquashed one, and raises no error
  ([CF-25](#cf-25), [CF-25/d](conformance.md#cf-25)).
  **Re-scoped 2026-10-29 (SC3 validation round 2).** The answer above
  claimed more than the recordings show. They settle that the CE image
  does not fail these six squashed loops with the stub's text and that
  each gives the result of its unsquashed form. They do not show whether
  the closed-source runner runs the iterations, or whether it reuses one
  runtime across them: each recorded sequential loop that starts the
  runners ran a single iteration, and the body only echoes its input.
  The discriminator proposed above does not work: the squash path
  queues the ordinary iteration job before starting the runners, and a
  worker hands that job to a runner without changing its kind, so the
  number and kind of iteration jobs are the same on both paths
  ([CF-25](#cf-25)). What remains: does the CE image run the iterations
  of a sequential squashed loop in one reused runtime? *Settled by:*
  oracle runs, on the default worker tags, of a sequential
  `forloopflow` over `[1, 2, 3]` and of a `whileloopflow` with
  `stop_after_if: {expr: "result.v >= 2"}` (three iterations), each
  with `squash: true`, once with a one-step body (which collapses) and
  once with a two-step body that runs the same script twice, next to the
  same four flows without `squash`. The script is a Bun script that
  keeps a counter at module scope and returns `{"pid": <its process
  id>, "n": <the counter after adding 1>, "v": <its input>}`, with `v =
  "flow_input.iter.index"` (the while loop's iteration index; the for
  loop's `flow_input.iter.value`). Observe each iteration's `pid` and
  `n`: in the unsquashed controls every iteration should report `n = 1`
  and its own `pid`; in a squashed loop, an `n` above 1 or a `pid`
  repeated across iterations means a reused runtime.
  *2026-10-29 (SC4):* the public source fixes no job kind for the runner
  request, so nothing is gained by varying the run mode ([CF-25](#cf-25),
  revision of this date). Two more observations sharpen the recording:
  each iteration job's logs (an EE build's runner writes a line starting
  `dedicated worker bun:` there), and a control whose body is a Bash step
  `echo $v` (EE runners do not serve Bash, so its iterations would run
  ordinarily even when the Bun ones are squashed). Still open.
  *2026-10-29 (SC4 validation round 1):* [WR-55](languages.md#wr-55)
  points here for what the CE image's runners do with a child an
  iteration leaves behind: add a squashed one-step loop over `[1, 2]`
  whose body starts a background child sleeping 5 s and returns at once,
  next to the same loop without `squash`, and record each iteration job's
  start and completion times (unsquashed, each iteration should complete
  only after its child, WR-55/a).

# Flow execution

This area describes how Windmill runs a flow: the run state and the decision
list that advances it, the data each step and expression can see, and what
happens when a step succeeds, fails, is retried, stopped early, skipped, timed
out or canceled. It also records where values cross JSON storage boundaries
between steps. Loops and branches are specified in
[control-flow.md](control-flow.md), the expression evaluator in
[expressions.md](expressions.md), language wrappers in
[languages.md](languages.md), platform variable and resource resolution in
[client-api.md](client-api.md), and the oracle harness in
[oracle.md](oracle.md).

**Pinned version:** Windmill `v1.767.0` (commit `6de4ec0f665ad856002495c4d86269105ec28ad7`).
Source pointers are `path:line @ v1.767.0` relative to the Windmill repository
root. Status, scope and confidence tags are defined in the [index](README.md#legend).

**ID prefixes in this file:** `FX-` flow execution; `FX-Vn` findings were
added during validation.

No expectation in this area has been recorded on the oracle yet. Every
conformance case is source-derived, and the open questions say which
recording settles each one ([Q-FX-18](#q-fx-18) covers the cases no other
question names).

## Terms

- **Step:** a top-level entry of `value.modules`. A **module** is any step,
  including the `failure_module` and the `preprocessor_module`. A **job** is a
  unit that Windmill queues.
- **Modules and jobs.** A module run is normally at least one job. The
  mappings differ for three kinds of module:
  - an **empty container** (an empty loop iterator, or an empty or missing
    branch body) launches no job and is recorded `Success` with a nil job id;
  - a **collapsed single-step loop** ([FX-46](#fx-46)) launches one script job
    per iteration, directly under the enclosing flow, with no child flow job;
  - a **parallel container** launches one job per iteration or branch.

  Identity, skipped and mocked modules each launch one trivial identity job.
- **Inline body:** the modules inside a `branchone`/`branchall` branch or a
  loop body. Windmill runs an inline body as a child *raw flow* job, unless
  the loop collapses. A **subflow by path** is a `type: flow` module that
  refers to another deployed flow.
- **Wrapped error:** the stored result `{"error": E}` of a failed script job
  or pre-run failure ([FX-12](#fx-12)). "Error object" means `E`. Flow jobs
  store their final value unwrapped.
- **Failure module** (also **handler**): the flow's `failure_module`.
- **Cursor:** the index of the module being run; `len` is the number of
  regular modules. The **preceding id** is the id of the module whose result
  is `previous_result`.
- **Start phase / completion phase:** the two halves of the decision list in
  [FX-2](#fx-2).
- **Clean stop / error-stop:** a `stop_after_if` that fires without, or with,
  an `error_message` ([FX-19](#fx-19)).
- **Tolerated failure:** a failure that `continue_on_error`, a loop's
  `skip_failures` or a branch's `skip_failure` lets the flow continue past.
- **Chaining error:** an error raised while starting a module that is not a
  failure of that module ([FX-15](#fx-15)).
- **Unrecoverable failure:** a failure that skips retry and tolerance
  ([FX-44](#fx-44)).

## Summary for the design

1. **One decision list ([FX-2](#fx-2)).** Starting a module and completing a
   module are separate phases. Args precedence at start is mock, then retry
   reuse, then preprocessor, then direct input, then identity or skip, then
   input transforms. The continue decision at completion is stop (an
   error-stop continues only towards a failure module), then cancel, then
   success, then unrecoverable, then tolerated, then retry, then failure
   module, then stop. Error-stops share the failure machinery: they can be
   retried, advance under `continue_on_error`, end a flow successfully at the
   last module, or enter the failure module at cursor `len`
   ([FX-19](#fx-19), [FX-V4](#fx-v4)).
2. **Three outcomes per module can disagree:** the stored job result, the
   module status and the flow outcome. A successful script can be a `Failure`
   module after an error-stop, and storage-path lookups still see its stored
   success value ([FX-V2](#fx-v2), [FX-10](#fx-10)). The IR and the oracle
   harness must track all three.
3. **Error shapes.** Script and pre-run failures are `{"error": E}`; Bun omits
   undefined keys and may lack `name`/`stack` for non-Error throws
   ([FX-12](#fx-12), [FX-47](#fx-47)). Flow jobs store their final value
   unwrapped. Literal error names to keep apart: `EarlyStopError`,
   `Canceled`, `InternalError` (later-transition chaining), `Anyhow` (pickup
   chaining), `InternalErr` (storage failure, [FX-48](#fx-48)),
   `ExecutionErr` (executor failures and timeouts), `ManualFailure`
   ([FX-13](#fx-13)).
4. **Error contexts have different policies.** Input-transform and
   `branchone` predicate errors are step failures ([FX-14](#fx-14)). Other
   start-phase errors are chaining errors, routed three ways: later
   transition, root pickup, child pickup ([FX-15](#fx-15)). A throwing
   `stop_after_if` or completion-time `retry_if` is an unrecoverable string
   error ([FX-21](#fx-21), [FX-28](#fx-28)); a throwing
   `stop_after_all_iters_if` is an `EarlyStopError` ([FX-V1](#fx-v1)).
5. **JSON boundaries ([FX-42](#fx-42), [FX-V5](#fx-v5), [FX-48](#fx-48)).**
   Outer argument-map key order is unspecified. Stored nested values are
   jsonb-normalised; values introduced by worker-side reference resolution
   are not. In-memory handoffs keep the producer's text. Two points are
   disputed and must not be built on yet: whether `$jsonvar:` output keeps
   its spelling or only its key order ([Q-FX-16](#q-fx-16)), and which
   engine expressions read the preceding result from memory rather than
   storage ([FX-V6](#fx-v6), [Q-FX-15](#q-fx-15)).
6. **Timeouts by execution path ([FX-24](#fx-24), [FX-25](#fx-25)).** Process
   and in-process executors are timed, with different messages. Identity
   jobs and child flow jobs are untimed. In a collapsed loop the loop
   module's timeout applies to each iteration.
7. **Loop collapse changes semantics ([FX-46](#fx-46)).** It changes which
   `timeout`, `continue_on_error`, `step_id` and cache TTL apply, and previews
   never collapse for-loops. An explicit equivalence decision is needed.
   Whether collapsed iterations can share cache entries is disputed
   ([FX-40](#fx-40), [Q-FX-17](#q-fx-17)).
8. **Retry arithmetic** is unsigned 16-bit with wrap, saturate and cap steps;
   jitter applies to the exponential phase only ([FX-26](#fx-26)). Retries
   reuse the failed attempt's args ([FX-27](#fx-27)), and the retry counter
   carries over after a tolerated failure ([FX-29](#fx-29)).
9. **Cursor-0 checks run again** on every start phase at cursor 0, so a
   non-deterministic `skip_expr` can change its answer mid-run. The mid-run
   effect is disputed ([FX-49](#fx-49), [Q-FX-11](#q-fx-11)). Evaluating
   `skip_expr` once would be a documented deviation, not Windmill behaviour.
10. **Waits, cancellation and scope.** Sleeps, retry delays and timeouts are
    real, cancellable waits. Cancellation skips the failure module and ends
    the flow as `Canceled` ([FX-38](#fx-38)). Suspend, priority, concurrency,
    tags, deletion and squash need no runtime support ([FX-39](#fx-39),
    [FX-41](#fx-41)). `mock`, `early_return`, the preprocessor (never run in
    subflows by path), `skip_expr`, `flow_env`, caching, the shared directory
    and the HTTP run-and-wait adapter ([FX-36](#fx-36), [FX-V3](#fx-v3)) are
    `later`.

## A. Run model and lifecycle

<a id="fx-1"></a>
### FX-1 · Flow run state: a cursor plus one status per module

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** A flow run keeps:
  - a step cursor `step` (an integer): `-1` while the preprocessor runs,
    `0 … len-1` for regular modules, `len` while the failure module runs;
  - one status per top-level module, in definition order;
  - one status for the failure module (its id defaults to `"failure"` when
    the flow has none);
  - an optional status for the preprocessor;
  - a retry counter `retry = {fail_count, failed_jobs}`.

  Module status kinds are `WaitingForPriorSteps`, `WaitingForEvents`
  (suspended), `WaitingForExecutor` (job pushed), `InProgress` (loops and
  branches), `Success` (with `skipped: bool`) and `Failure`. `Success` and
  `Failure` carry the module's job id; loops and branches also carry their
  child jobs (`flow_jobs`).

  Initially every module is `WaitingForPriorSteps`. The cursor starts at `-1`
  when the flow has a preprocessor that will run ([FX-35](#fx-35)), else at
  `0`. How module runs map to jobs is given under [Terms](#terms). Per-step
  status and `skipped` are observable through the flow-status API.
- **Evidence:**
  - `backend/windmill-types/src/flow_status.rs:19-44`, `:218-299`, `:503-545`
  - `backend/windmill-types/src/flows.rs:373-389`
  - Job mappings: `backend/windmill-worker/src/worker_flow.rs:3998-4052`
    (empty container, nil job id at `:3998-4034`), `:5657-5744`, `:5352-5415`
  - Docs: https://www.windmill.dev/docs/flows/architecture
- **Conformance:** [FX-1/a](conformance.md#fx-1)
- **Design note (not Windmill behaviour):** windmill-lite need not persist
  this state, but because per-step status and `skipped` are observable, the
  oracle harness should compare them.

<a id="fx-2"></a>
### FX-2 · How a flow advances (decision list)

**Scope:** core · **Confidence:** high (source-traced) · **Status:** confirmed

- **Rule:** The flow alternates between a start phase and a completion phase.

  **Start phase** for the module at cursor `i`. It is entered when the flow
  job is picked up (first start, re-queue after the preprocessor, resume after
  a suspend) or when a completion asked to continue. Checks run in this order:
  1. **No modules, or already done.** If the flow has no modules and is not in
     its preprocessor, complete with the input ([FX-3](#fx-3)). If module `i`
     is already `Success` (an empty container just finished), report that
     value as module `i`'s completion.
  2. **Cursor-0 checks**, on every start phase at cursor 0 ([FX-49](#fx-49)):
     for a root flow started by a schedule with `no_flow_overlap`, the overlap
     check; then, for every flow job whose definition has `skip_expr` (roots
     and subflows by path; inline children never have one), `skip_expr`
     ([FX-22](#fx-22)).
  3. **Compute `previous_result`** ([FX-6](#fx-6)).
  4. **Suspend:** if module `i-1` armed a suspend, wait for events
     ([FX-39](#fx-39)).
  5. **Sleep:** only if module `i` is waiting (not on re-entry after a
     failure), apply module `i-1`'s `sleep` ([FX-23](#fx-23)).
  6. **Re-entry after a failure.** If module `i` is `Failure` and the failure
     is not unrecoverable, evaluate retry (`retry_if`, then attempts left;
     [FX-26](#fx-26) to [FX-28](#fx-28)).
     - Retry due: set the module back to waiting, schedule it after the
       delay, restore `previous_result` (not for the failure module,
       [FX-32](#fx-32)), and mark the failed attempt's args for reuse.
     - Otherwise, if the module is not `continue_on_error`, or the failure is
       unrecoverable: switch to the failure module. The cursor becomes `len`
       and `previous_result` stays the failing value.
  7. **`skip_if`** of the current module; that is the failure module's own
     `skip_if` when step 6 switched to it ([FX-17](#fx-17)).
  8. **Args.** The first matching rule wins ([FX-16](#fx-16),
     [FX-17](#fx-17)):
     1. `mock`;
     2. a retry reuses the failed attempt's args;
     3. the preprocessor gets the input plus `_ENTRYPOINT_OVERRIDE`;
     4. `pass_flow_input_directly`;
     5. identity or skipped module: `{previous_result}`;
     6. input transforms; a throw becomes a pre-run error ([FX-14](#fx-14)).

     Containers build their children's inputs themselves
     ([control-flow.md](control-flow.md)).
  9. **Timeout:** evaluate `timeout` ([FX-24](#fx-24)).
  10. **Launch:** mock or skipped module → an identity job; otherwise the job
      or jobs for the module type.

  An error raised in start steps 2–10 is a chaining error ([FX-15](#fx-15)),
  **except** two captured failures, which become a failure of the module
  being started: an input-transform error in step 8 ([FX-14](#fx-14)) and a
  `branchone` predicate error in step 10 ([control-flow.md](control-flow.md)).
  Loop `iterator` and `parallelism` errors in step 10 *are* chaining errors.

  **Completion phase** for module `m` at cursor `i`, when a job finished with
  success flag `s` and result `r`:
  1. **Stop signal.**
     - If an inline child reported a stop, and `m` is neither a `flow` module
       nor a parallel container, stop with the child's skip flag
       ([FX-20](#fx-20)).
     - Else, if `m` is the failure module or the preprocessor, there is no
       stop, and `continue_on_error` is ignored.
     - Else, if `s` holds and `m` is not a branchall, not a parallel loop and
       not a skipped identity, evaluate `stop_after_if` ([FX-19](#fx-19)); a
       throw follows [FX-21](#fx-21).
  2. **Containers.** A parallel container waits for all children, then
     evaluates `stop_after_all_iters_if` (a throw follows [FX-V1](#fx-v1)). A
     sequential loop or branchall continues with its next child unless it
     stopped or failed; a stop inside a sequential loop breaks the loop and is
     consumed there. A finished sequential loop or branchall evaluates
     `stop_after_all_iters_if`.
  3. **Status.** The module becomes `Success` if (`s` holds, or a container
     failure-skip applies) and this is not an error-stop; otherwise
     `Failure`. The cursor advances on `Success`. On `Failure` it advances
     only if the module has `continue_on_error`, the failure is not
     unrecoverable, and no retry is due. This applies to an error-stop too
     ([FX-19](#fx-19), [FX-30](#fx-30)).
  4. **Result value.** An error-stop replaces the carried value with the
     `EarlyStopError` object.
  5. **Retry state** is cleared when the module is `Success`.
  6. **Preprocessor.** A successful preprocessor rewrites the input and
     re-queues the flow; the phase ends there ([FX-35](#fx-35)).
  7. **Continue?** The first matching rule decides:
     1. a stop → continue only if it is an error-stop **and** a failure module
        exists;
     2. the flow is canceled → stop;
     3. `s` holds → continue unless this was the last module;
     4. unrecoverable → continue only if a failure module exists, is not
        already running, and none already ran for this failure;
     5. tolerated (`continue_on_error`, loop `skip_failures`, branch
        `skip_failure`) → continue unless last;
     6. a retry is due → continue (re-entry, start step 6);
     7. a failure module exists, is not running, and none already ran →
        continue (start step 6 switches to it);
     8. otherwise → stop.
  8. **Completing.**
     - A tolerated failure at the last module turns the flow successful,
       including an error-stop with `continue_on_error`. This adjustment does
       **not** cancel a continuation already requested in step 7; when the
       failure module then runs, its outcome decides the flow
       ([FX-33](#fx-33)).
     - The completion checks the cancel flag first ([FX-38](#fx-38));
       otherwise the result follows [FX-36](#fx-36).
     - The completed flow job reports to its parent, if it has one
       ([FX-5](#fx-5)).

  **Entering the failure module at cursor `len`.** Completion can advance the
  cursor to `len` and still request continuation. That happens only when an
  error-stop at the final regular module is tolerated by `continue_on_error`,
  no retry is due, and a failure module exists ([FX-V4](#fx-v4)). The next
  start phase then begins directly at the failure module: its status is
  waiting, so no retry is evaluated and no sleep applies; `previous_result` is
  the carried `EarlyStopError`; and the preceding id is empty, so
  `results.<stopped module>` reads storage ([FX-10](#fx-10)).
- **Evidence:**
  - Start phase: `backend/windmill-worker/src/worker_flow.rs:3196-3322`,
    `:3324-3346`, `:3355-3359`, `:3659-3664`, `:3666-3832`, `:3842-3971`,
    `:4053-4077`, `:4267-4281`, `:4379-4401`, `:5106-5138`, `:5418-5489`,
    `:5747-5807`
  - Completion phase: `worker_flow.rs:599-746`, `:765-1348`, `:1350-1367`,
    `:1369-1397`, `:1491-1503`, `:1510-1694`, `:1724-1764`, `:1972-2059`,
    `:2131-2161`
  - `backend/windmill-types/src/flows.rs:380-389`
- **Conformance:** none of its own; every case in this area is an instance,
  in particular [FX-14](conformance.md#fx-14), [FX-15](conformance.md#fx-15),
  [FX-17](conformance.md#fx-17), [FX-19](conformance.md#fx-19),
  [FX-30](conformance.md#fx-30), [FX-49](conformance.md#fx-49) and
  [FX-V4](conformance.md#fx-v4).

<a id="fx-3"></a>
### FX-3 · A flow with no modules returns its input

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** A flow whose `modules` is empty, and that has no preprocessor to
  run, completes successfully with the flow input object as its result. The
  result's outer key order before storage is unspecified ([FX-42](#fx-42));
  the stored final result is jsonb-normalised. An empty inline body is not
  run as a child job: the container records it as an empty result
  ([Terms](#terms), [control-flow.md](control-flow.md)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3196-3222`;
  completion at `worker_flow.rs:595-597`, `:1395-1397`.
- **Conformance:** [FX-3/a](conformance.md#fx-3)

<a id="fx-4"></a>
### FX-4 · Trailing virtual identity step when the last module sleeps or suspends

**Scope:** core · **Confidence:** high (call sites read); medium (that every deployed-flow path goes through the lock step) · **Status:** confirmed

- **Rule:** If the last module of a module list has `sleep` or `suspend`,
  Windmill appends a synthetic `identity` module with id `<last id>-v` and
  summary `Virtual module needed for suspend/sleep when last module`. This
  applies to top-level modules (added at deploy and at preview time) and to
  every inline body. A sleep on the last step therefore delays flow
  completion, and the flow result is the sleeping step's result passed
  through. The extra status entry is visible in flow status.
- **Evidence:**
  - `backend/windmill-types/src/flows.rs:1252-1280`
  - Call sites: `backend/windmill-queue/src/jobs.rs:5605` (raw and preview
    flows); `backend/windmill-worker/src/worker_lockfiles.rs:1706` (deployed
    flows, at lock/deploy time); `backend/windmill-worker/src/worker_flow.rs:5056`
    (inline bodies)
- **Conformance:** [FX-4/a](conformance.md#fx-4)

<a id="fx-5"></a>
### FX-5 · Inline bodies versus subflows by path

**Scope:** core (inline bodies, loop collapse); later (subflows by path) · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:**
  - **Inline body.** A branch body, or a loop body that does not collapse,
    runs as a child flow. That child flow gets:
    - the body's modules plus the virtual step of [FX-4](#fx-4);
    - the parent's failure module, copied with a numeric suffix:
      `-<parent cursor>`, `-<parent cursor>-<iteration>` or
      `-<parent cursor>-<branch index>`;
    - the parent's `same_worker` and `preserve_step_tags`.

    It gets nothing else: no `flow_env`, `early_return`, `skip_expr`,
    `cache_ttl` or preprocessor. `flow_env` is inherited at run time instead
    ([FX-11](#fx-11)).
  - **Collapsed loops.** A sequential loop whose body is a single eligible
    script step gets no child flow; each iteration is a script job of the
    enclosing flow, and several settings come from the loop module
    ([FX-46](#fx-46)).
  - **Subflow by path.** A `type: flow` module runs the latest deployed
    version of the flow at that path, as a full flow with its own settings and
    its own failure module. It is launched with preprocessing disabled, so its
    `preprocessor_module` never runs.
  - **What crosses the parent/child boundary:**
    - **Result:** the child's final result ([FX-36](#fx-36)) becomes the
      module's result.
    - **Success:** the child's success flag. A child that ran its failure
      module always reports failure to the parent, even if it recovered
      ([FX-33](#fx-33)).
    - **Stop signals** follow [FX-20](#fx-20): sequential loops consume them,
      parallel containers and `flow` modules ignore them, branch bodies
      propagate them.
    - **Failure handling:** after an inline child ran its copied failure
      module, the parent does not run its own for that failure. A `flow`
      module resets this, so both the subflow's and the parent's failure
      modules can run.
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:5038-5074` (child flow built
    from an inline body), `:5657-5745` (collapse), `:6020-6049` (by-path flow,
    latest deployed version; preprocessing disabled at `:6034-6040`)
  - `worker_flow.rs:498-500` (reset for `flow` modules), `:599-618`,
    `:670-677`, `:1085-1091` (parallel containers), `:1195-1201`, `:2136-2156`
    (result, success and stop propagation)
- **Conformance:** [FX-5/a](conformance.md#fx-5); also
  [FX-20](conformance.md#fx-20), [FX-33](conformance.md#fx-33),
  [FX-34](conformance.md#fx-34), [FX-46](conformance.md#fx-46).

<a id="fx-46"></a>
### FX-46 · Collapsed single-step loops change which settings apply

**Scope:** core (later for cache) · **Confidence:** high (collapse conditions, settings source); medium (`continue_on_error` consequence) · **Status:** confirmed

- **Rule:** A loop is **collapsed** when it is
  - a `forloopflow` with `parallel: false` in a **non-preview** flow, or
  - any `whileloopflow`, preview included,

  and all of the following hold:
  - the body is exactly one `script`, `rawscript` or `flowscript` module;
  - that module has none of `sleep`, `suspend`, `cache_ttl`, `retry`,
    `stop_after_if`, `stop_after_all_iters_if`, `skip_if` or an enabled
    `mock`;
  - the enclosing flow has no failure module.

  A collapsed loop runs each iteration as a script job directly under the
  enclosing flow, with no child flow. The body module's input transforms are
  evaluated with `iter` added ([control-flow.md](control-flow.md)).
  Consequences:
  - **Timeout:** the loop module's `timeout` applies to each iteration; the
    body module's `timeout` is ignored ([FX-24](#fx-24)).
  - **`continue_on_error` on the body is ignored:** an iteration failure is a
    loop failure, handled by the loop's `skip_failures` and the loop module's
    `continue_on_error`.
  - **Job attributes from the loop module:** `step_id` / `WM_FLOW_STEP_ID`,
    priority and `delete_after_use` ([FX-45](#fx-45)).
  - **Cache TTL** follows the per-payload matrix of [FX-40](#fx-40): a
    by-path script body keeps its script's own TTL; other bodies take the
    loop module's.

  **Preview divergence:** a preview never collapses a for-loop, so the same
  flow can behave differently when previewed and when deployed.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:5247-5249`,
  `:5294-5300`, `:5657-5697`, `:5729-5745`, `:5908-5983`, `:4142-4148`,
  `:4379-4401`, `:4429`; cache matrix as in [FX-40](#fx-40).
- **Conformance:** [FX-46/a](conformance.md#fx-46); see also
  [FX-24/d](conformance.md#fx-24) and [FX-40/c](conformance.md#fx-40).
- **Design note (not Windmill behaviour):** windmill-lite must pick one of
  the deployed and preview behaviours (deployed is the one the oracle
  exercises) and record the choice in its equivalence contract.

<a id="fx-49"></a>
### FX-49 · Cursor-0 checks run on every start phase at cursor 0

**Scope:** later · **Confidence:** medium (re-evaluation traced); low (mid-run effect) · **Status:** DISPUTED

- **Rule (as drafted):** The cursor-0 checks (the flow-level `skip_expr` and,
  for scheduled root flows with `no_flow_overlap`, the overlap check) run on
  every start phase at cursor 0, not once per flow. Besides the first start
  they run when:
  - module 0 is re-entered for a retry;
  - module 0 failed and the flow switches to the failure module (the check
    runs before the switch);
  - each iteration or branch of a sequential container that is module 0 is
    launched.

  A pure, deterministic `skip_expr` that was false at the start stays false,
  so nothing is observable. A non-deterministic one (reading variables, time
  or `WM_SCHEDULED_FOR`) can change its answer mid-run; a throwing one
  becomes a chaining error at that later transition ([FX-15](#fx-15),
  context 1). The effect of a mid-run true result was left unsettled: the
  source suggests it acts like a stop arriving at module 0, which would break
  a sequential loop at module 0. The drafted fixture predicted three
  evaluations, with the outcome open.
- **Correction (validation):** Repeated cursor-0 evaluation is confirmed, but
  the fixture does not justify three evaluations. With `v` bound explicitly
  to `flow_input.iter.value`, the first iteration sets the variable to
  `yes`, the next start phase sees `true` and emits the stop signal. The
  source then predicts **two** evaluations, loop result `[1]`, and an
  ordinary successful completion of that one-module flow. Evidence:
  `backend/windmill-worker/src/worker_flow.rs:3311-3322`, `:670-677`,
  `:1134-1149`, `:1195-1201`, `:1478-1489`;
  `backend/windmill-worker/src/result_processor.rs:450-480`.
- **Open:** [Q-FX-11](#q-fx-11).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3224-3322` (the
  gate sits before the retry and failure-module switch at `:3720-3832`),
  `:1134-1191`, `:1392-1397`, `:2958-2980`.
- **Conformance:** [FX-49/a](conformance.md#fx-49)
- **Design note (not Windmill behaviour):** windmill-lite could evaluate
  `skip_expr` once per run. That would be a deviation from Windmill and must
  be documented as one, unless the oracle shows the re-evaluation matters for
  supported flows.

## B. Data available to steps and expressions

<a id="fx-6"></a>
### FX-6 · What `previous_result` is

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:**
  - **First top-level step:** the flow input object, after any preprocessor
    rewrite. Its outer key order is unspecified ([FX-42](#fx-42)).
  - **First step of an inline body:** the inline child's own input, which
    the container builds ([control-flow.md](control-flow.md)).
  - **Later steps:** the result of the module just before it, whatever it
    was: a success value; a skipped module's passthrough ([FX-17](#fx-17));
    a mock value ([FX-18](#fx-18)); the wrapped error of a failed module that
    `continue_on_error` tolerated ([FX-30](#fx-30)); an `EarlyStopError`
    after a tolerated error-stop ([FX-19](#fx-19)); for a loop or branchall,
    the array of child results.
  - **Previous result not held in memory** (after a suspend or a restart, or
    when it is re-derived for a retry): it is re-read from the stored result
    of the preceding module, but only if that module's status is `Success`.
    Otherwise (for example it failed with `continue_on_error`)
    `previous_result` becomes the **JSON string `"{}"`**, a two-character
    string, not an empty object. This is reachable through an ordinary retry
    of a module that follows a tolerated failure.
  - **Failure module:** the value that triggered it: a wrapped error, an
    `EarlyStopError`, or a loop or branch result array ([FX-32](#fx-32)). A
    retry of the failure module keeps the failure module's own failed-attempt
    value instead.
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:3324-3346`
  - `worker_flow.rs:3778-3792` (retry restores the preceding result; not for
    the failure module)
  - `worker_flow.rs:6234-6265` (re-read only from a `Success` predecessor)
- **Conformance:** [FX-6/a, FX-6/b](conformance.md#fx-6)

<a id="fx-7"></a>
### FX-7 · Step input transforms

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** A step's args object is built from its `input_transforms` map,
  one entry per argument name.
  - **`static`:** the `value` is passed verbatim. It is not templated;
    `$var:`/`$res:` strings are resolved later by the worker
    ([FX-42](#fx-42), [FX-V5](#fx-v5), [expressions.md](expressions.md),
    [client-api.md](client-api.md)).
  - **`javascript`:** the `expr` is evaluated ([expressions.md](expressions.md)).
    Its value is serialised with JavaScript `JSON.stringify`, with
    `undefined` turned into `null`. Some fast paths pass the raw value
    through instead ([FX-42](#fx-42)).
  - **`ai`:** the entry is left out of the args.

  Each transform is evaluated independently, in no defined order, and none
  can see the others' values.

  **Names visible to a JavaScript transform:**
  - `flow_input`, `previous_result`, `results` (lazy, [FX-10](#fx-10)) and
    `flow_env` ([FX-11](#fx-11));
  - `params`, which is always `{}` (it is captured before any argument is
    computed);
  - `resume`, `resumes`, `approvers` (from a preceding suspend, else `null`,
    `[]`, `[]`);
  - the helpers `variable(path)`, `resource(path)` and
    `flow_user_state(key)`;
  - `error`, when the text contains `error` ([FX-8](#fx-8));
  - inside the failure module only: `started_at` (the flow's start
    timestamp) and `flow_job_id`.

  **`result` is not defined** in step input transforms, unlike in
  predicates. A bare `result` evaluates as an unbound identifier, so the step
  fails with the pre-run error of [FX-14](#fx-14) carrying an engine
  `ReferenceError`; the exact engine text is not asserted.

  **Errors:** a transform that throws makes that step fail ([FX-14](#fx-14)).
  **Missing args:** an argument with no transform entry is absent from the
  args, and the script's own default applies ([languages.md](languages.md)).
  **Override:** `pass_flow_input_directly: true` replaces the whole mechanism;
  the args become the flow input object.
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:2712-2774` (transform map,
    environment, `params` captured empty; `result` absent at `:2728-2744`)
  - `worker_flow.rs:3906-3963` (selection order), `:3939-3947`
    (failure-module extras)
  - `backend/windmill-jseval/src/lib.rs:456-469` (only supplied context names
    are installed), `:555-559` (`JSON.stringify(x ?? null)`)
- **Conformance:** [FX-7/a–FX-7/d](conformance.md#fx-7)
- **Note:** after the args are stored, the worker resolves platform
  references in them before the script runs; see [FX-V5](#fx-v5).

<a id="fx-8"></a>
### FX-8 · Scope of each flow-level expression

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** For each expression: when it is evaluated, what `result` and
  `previous_result` are bound to, and the other names it can see.
  `flow_input`, `flow_env` and the `variable`/`resource` helpers are
  available in all of them unless the table says otherwise.

  | Expression | Evaluated | `result` and `previous_result` | `results` | Also visible |
  |---|---|---|---|---|
  | Step `skip_if.expr` | before the step starts | the preceding module's result (step 0: the flow input) | available; the preceding step is served from memory | `resume`, `resumes`, `approvers` |
  | Step `timeout` (javascript) | when the step's job is pushed | the preceding module's result | available | |
  | `sleep` on step `i` (javascript) | before step `i+1` starts | step `i`'s own result | **not available**: undefined, so referencing it throws | |
  | Flow `skip_expr` | before step 0 of a flow | the string `"{}"` | not available | `WM_SCHEDULED_FOR`, as a string |
  | Step `stop_after_if.expr` | after the step **succeeds** | both are **this step's own result** | only if the text contains `results.`, `results[` or `results?.`; then every step, the preceding one included, is read from storage | `all_iters` when the module is a loop or branchall and the text contains `all_iters`; `flow_env` only if the text contains `flow_env` |
  | `stop_after_all_iters_if.expr` | after a loop or branchall finishes | the array of child results | same textual rule as `stop_after_if` | |
  | `retry.retry_if.expr` | after the step **fails** | the failed step's wrapped error | not available | |

  Shared behaviour:
  - Boolean predicates are coerced with JavaScript truthiness ([FX-9](#fx-9)).
  - In every expression whose text contains the substring `error`, a variable
    `error` is pre-bound to `previous_result.error`. If that is absent, it is
    derived from an array of errors as `MultipleErrors`, or set to
    `UnknownError` (details in [expressions.md](expressions.md)).
  - A `branchall` module never evaluates its own `stop_after_if`; use
    `stop_after_all_iters_if` there.
  - A throwing `stop_after_all_iters_if` follows [FX-V1](#fx-v1), not
    [FX-21](#fx-21).
  - `retry_if` may be evaluated twice per failure ([FX-28](#fx-28)).
  - Completion-phase predicates use a cached `flow_env` resolution
    ([FX-11](#fx-11)).
  - Branch predicates, the loop iterator and parallelism are specified in
    [control-flow.md](control-flow.md) and [expressions.md](expressions.md).
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:3842-3872` (`skip_if`),
    `:4379-4401` (`timeout`), `:3666-3718` and `:2659-2708` (`sleep`: no id
    context), `:664-746` and `:324-340` (`stop_after_if`: `results` gated
    textually; empty preceding id), `:342-407` (all-iters), `:2390-2425`
    (`retry_if`: no id context), `:3280-3321` (`skip_expr`), `:2427-2469`
    (shared boolean helper binds `result` and `previous_result` to the same
    value)
  - `backend/windmill-jseval/src/lib.rs:273-279`, `:494-526` (`error`
    binding)
- **Conformance:** [FX-8/a, FX-8/b](conformance.md#fx-8)

<a id="fx-9"></a>
### FX-9 · Boolean predicates use JavaScript truthiness

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** `skip_if`, `stop_after_if`, `stop_after_all_iters_if`,
  `retry_if`, the flow `skip_expr` and branch predicates are evaluated as
  `Boolean(<expr>)`, so any JavaScript value is accepted: `0`, `""`, `null`,
  `undefined`, `NaN` and `false` mean false; `"false"`, `[]` and `{}` mean
  true. If the expression throws, the predicate errors; [FX-15](#fx-15),
  [FX-21](#fx-21), [FX-V1](#fx-v1) and [control-flow.md](control-flow.md)
  give the consequences.

  The engine is QuickJS with a time limit per evaluation. A time-limited
  evaluation runs **at most three times in total** (the first try plus two
  retries, 5 s apart), and only "took too long" errors are retried
  ([expressions.md](expressions.md)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2451-2468`;
  `backend/windmill-worker/src/js_eval.rs:81-107`;
  `backend/windmill-jseval/src/lib.rs:304-328`
- **Conformance:** [FX-9/a, FX-9/b](conformance.md#fx-9)

<a id="fx-10"></a>
### FX-10 · `results.<id>` resolution routes

**Scope:** core · **Confidence:** high (routes source-traced); binding condition disputed · **Status:** DISPUTED

- **Rule (as drafted):** An expression that mentions `results` is answered by
  the first route that applies.
  1. **Exact preceding id.** The whole expression is exactly `results.<p>`,
     `results?.<p>`, `results["<p>"]` or `results?.["<p>"]`, where `p` is the
     preceding module's id. It returns the in-memory `previous_result`.
  2. **Simple path, answered from storage.** The whole expression has the
     form `results.<id>` or `results?.<id>`, optionally followed by one
     `[<digits>]`, optionally followed by `.<key>` segments (identifier
     characters only), and does not end in `.length`. It is answered by a
     storage query for that step's stored result, **including when `<id>` is
     the preceding step**. So `results.<p>.x` reads the stored job result of
     `p`, which can differ from `previous_result` ([FX-V2](#fx-v2)). A missing
     step or path gives `null`.
  3. **Anything else goes to the engine.** `results` is a lazy proxy. The
     preceding id returns the in-memory `previous_result`; other ids fetch
     from storage; loops and branchall return arrays of child results; a step
     with no job yet, or an unknown id, gives `null` (no error is raised).
     The drafted amendment adds: route 3 gives the in-memory value **only**
     when the expression's text contains one of the four preceding-id
     spellings, because that is what binds `previous_result` into the
     engine; computed indexing such as `results[id]` goes to storage even for
     the preceding step.

  Inside `stop_after_if` the preceding id is empty, so routes 1 and 3 never
  use memory. In an ordinarily entered failure module, the preceding id is
  the failed module's id ([FX-32](#fx-32)); in one entered at cursor `len`
  it is empty ([FX-V4](#fx-v4)).
- **Correction (validation):** The "only" and the unconditional
  computed-index storage rule are false when the expression binds
  `previous_result` for another reason. The engine installs
  `previous_result` when its name appears anywhere in the expression, when
  one of the four literal preceding-id spellings appears, or when the
  expression contains `error`; the proxy returns the in-memory value
  whenever the requested id equals the preceding id and that variable is
  defined. Computed indexing therefore does not always read storage. Evidence:
  `backend/windmill-jseval/src/lib.rs:253-279`, `:804-815`. The competing rule
  is stated in full as [FX-V6](#fx-v6).
- **Open:** [Q-FX-15](#q-fx-15).
- **Evidence:**
  - `backend/windmill-worker/src/js_eval.rs:44-79`
  - `backend/windmill-jseval/src/lib.rs:65-76`, `:165-218`, `:260-279`,
    `:670-823`
  - `backend/windmill-worker/src/worker_flow.rs:324-340`, `:3805-3813`,
    `:6155-6172`
- **Conformance:** [FX-10/a, FX-10/b](conformance.md#fx-10); the contested
  expectation is [FX-V6/a](conformance.md#fx-v6).
- **Note:** routes 1 and 2 and the proxy's basic behaviour are not contested;
  the dispute concerns only when route 3 uses memory. Coordinate with
  [expressions.md](expressions.md).

<a id="fx-v6"></a>
### FX-V6 · Dynamic preceding-result lookup depends on all context-binding triggers

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** open

- **Rule:** For an engine expression (route 3 of [FX-10](#fx-10)), the
  results proxy returns the in-memory preceding result only if the requested
  id equals the configured preceding id and `previous_result` is defined in
  the engine. That variable is installed when:
  - the name `previous_result` appears in the expression;
  - one of the four literal preceding-result spellings appears; or
  - the expression contains `error`.

  Computed indexing therefore does not always read storage. If no binding
  trigger applies, the lookup falls back to storage. The exact-expression and
  simple storage-path fast paths still take precedence before the engine
  runs.
- **Evidence:** `backend/windmill-jseval/src/lib.rs:253-279`, `:456-469`,
  `:804-815`; `backend/windmill-worker/src/js_eval.rs:44-79`.
- **Conformance:** [FX-V6/a](conformance.md#fx-v6)
- **Note:** this is the competing claim in the [FX-10](#fx-10) dispute. Until
  [Q-FX-15](#q-fx-15) is settled, neither it nor the FX-10 amendment is
  settled behaviour. Coordinate with [expressions.md](expressions.md).

<a id="fx-11"></a>
### FX-11 · `flow_env`

**Scope:** later · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** A flow's `flow_env` is a map from name to JSON value, exposed to
  expressions as the object `flow_env`; a missing key is `undefined`.
  - **Source.** The flow's own `flow_env`, or else the nearest ancestor
    flow's. An inline child flow has none of its own and inherits; a subflow
    by path uses its own if it defines one, otherwise it inherits.
  - **Resolution.** Every time a flow job runs its start phase
    ([FX-2](#fx-2)), `flow_env` is resolved again, replacing `$var:` and
    `$res:` references. **If resolution fails, the unresolved literal map is
    used and no error is raised**; that result is not cached.
  - **Completion-phase predicates** (`stop_after_if`,
    `stop_after_all_iters_if`, `retry_if`) use a per-run cached resolution
    when available, and resolve only if the expression text contains
    `flow_env`.

  So a variable that changes during a run can be seen with different values
  by different steps, or by a step and a predicate.
- **Evidence:** `backend/windmill-types/src/flows.rs:303-304`;
  `backend/windmill-worker/src/worker_flow.rs:2796-2851`, `:2507-2651`
  (nearest ancestor at `:2603-2651`; fallback at `:2580-2594`, `:2829-2832`),
  `:502-553`, `:84-109`; `backend/windmill-jseval/src/lib.rs:472-485`
- **Conformance:** [FX-11/a, FX-11/b](conformance.md#fx-11)

## C. Step outcomes and errors

<a id="fx-12"></a>
### FX-12 · Failure values of script and worker jobs

**Scope:** core (Bun, flow-level); later (Deno, Python) · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** This rule covers **script jobs and pre-run failures** only. A
  failed **flow** job (root, inline child or subflow) stores its final value
  as-is ([FX-36](#fx-36)): a failure module's string, an array, a
  `ManualFailure` object ([FX-13](#fx-13)) or anything else.

  For script and worker failures the stored result is `{"error": E}`, where
  `E` is:
  - **Bun wrapper (thrown value `e`).** `E` starts with the keys `message`,
    `name` and `stack` taken from `e`; a key whose value is `undefined` is
    **omitted**. Then `step_id` is added when `WM_FLOW_STEP_ID` is set, and
    `extra` collects every own property name of `e` except `line`, `name`,
    `stack`, `column`, `message`, `sourceURL`, `originalLine` and
    `originalColumn`, when there are any. Non-Error thrown values:
    [FX-47](#fx-47).
  - **Deno wrapper:** `message`, `name`, `stack` and optional `step_id`;
    **no `extra`**.
  - **Python wrapper:** `message` = `str(e)`; `name` = the exception class
    name; `stack` = the formatted traceback without its first frame, joined
    with newlines; `extra` = the exception's attribute dict when non-empty;
    optional `step_id`. Values that cannot be serialised are converted to
    strings.
  - **Non-zero exit without an error file:** `{"message": "exit code for
    \"<program>\": <code>, last log lines:\n<logs>", "name": "ExecutionErr",
    "step_id"?, "exit_code": <code>}`.
  - **Worker-side failure** (timeout, kill, I/O): `{"message": "execution
    error:\n<detail>", "name": "ExecutionErr", "step_id"?}`.
  - **Pre-run and pickup failures:** `{"message": <text>, "name": <error
    kind>}`, with no `step_id`. `<error kind>` is a Rust error kind name such
    as `ExecutionErr`, `Anyhow` or `InternalErr`. If the error already carries
    a JSON value, that value is `E` (for example `Canceled`,
    [FX-38](#fx-38)).
  - **An error file that is not valid JSON:** `{"message": "Non serializable
    error: <raw>"}`.
- **Evidence:**
  - `backend/windmill-worker/src/bun_executor.rs:1948-1970`
  - `backend/windmill-worker/src/deno_executor.rs:370-377`
  - `backend/windmill-worker/src/python_executor.rs:920-932`, `:988-1000`
  - `backend/windmill-worker/src/result_processor.rs:587-628`, `:1584-1613`,
    `:1910-1965`, `:2020-2044`
  - `backend/windmill-queue/src/jobs.rs:626-629`, `:883-918`
  - `backend/windmill-worker/src/worker_flow.rs:2040-2049`
  - `backend/windmill-common/src/error.rs:23-151`
  - Docs: https://www.windmill.dev/docs/flows/error_handling
- **Conformance:** [FX-12/a–FX-12/d](conformance.md#fx-12)
- **Note:** language detail is in [languages.md](languages.md).

<a id="fx-13"></a>
### FX-13 · A successful result with `wm_failure` is turned into a failure

**Scope:** later · **Confidence:** high (insertion read); medium (downstream path) · **Status:** confirmed

- **Rule:** A step that returns successfully is treated as **failed** if its
  result is an object with a string field `wm_failure`. The result is kept
  unwrapped, and a top-level `"error": {"name": "ManualFailure", "message":
  <wm_failure>}` is inserted alongside the other fields. Downstream it
  behaves like any failure (retry, `continue_on_error`, failure module).
- **Evidence:** `backend/windmill-worker/src/result_processor.rs:107-129`,
  `:1584-1597`; `backend/windmill-queue/src/jobs.rs:652-673`
- **Conformance:** [FX-13/a](conformance.md#fx-13)

<a id="fx-14"></a>
### FX-14 · An input-transform error fails that step; a retry runs with empty args

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** If evaluating a step's input transforms throws, the step's job is
  still launched, with **empty args** and a pre-run error. It fails without
  running the script, with
  `` {"error": {"message": "Error during isolated evaluation of expression `<expr>`:\n<engine error>", "name": "ExecutionErr"}} ``
  and no `step_id`. A transform error inside a simple loop
  iteration has the prefix `could not transform input using an expr: `
  ([control-flow.md](control-flow.md)).

  This is an ordinary step failure, so `continue_on_error` and the failure
  module apply. **A retry does not re-evaluate the transforms and does not
  copy the pre-run error:** it re-launches the step with the failed attempt's
  args, the empty map, and **the script runs** with no arguments. Whether
  that run fails depends on the script: missing parameters fall back to
  their defaults or fail ([languages.md](languages.md)).
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:2746-2771`, `:3891-3905`,
    `:4267-4281`, `:4404-4429`
  - `backend/windmill-queue/src/jobs.rs:6436`, `:6488`
  - `backend/windmill-worker/src/worker.rs:3647-3649`
- **Conformance:** [FX-14/a, FX-14/b](conformance.md#fx-14)

<a id="fx-15"></a>
### FX-15 · Chaining errors: three contexts

**Scope:** core · **Confidence:** high (contexts 1 and 3); medium (context 2 re-entry outcome) · **Status:** confirmed

- **Rule:** These errors raised during the start phase ([FX-2](#fx-2), start
  steps 2–10) are chaining errors:
  - an expression that throws: `skip_if`, flow `skip_expr`, `sleep`,
    `timeout`, the loop `iterator` or `parallelism`, a suspend's
    `user_groups_required`, or a `retry_if` evaluated at re-entry;
  - a negative `timeout`;
  - a `sleep` value that is not a number;
  - a failure to push the next job, for example args containing NUL
    ([FX-48](#fx-48)).

  Input-transform errors ([FX-14](#fx-14)) and `branchone` predicate errors
  ([control-flow.md](control-flow.md)) are step failures instead. What
  happens to a chaining error depends on how the start phase was entered:
  1. **Later transition** (after a completion in the same flow):
     - the flow completes **failed** at once, with `{"error": {"message":
       <text>, "name": "InternalError"}}`;
     - no retry, no `continue_on_error`, no failure module;
     - the log says `Unexpected error during flow chaining`;
     - a parent, if any, is told the flow failed, but receives the flow's
       **previous** carried value, not the `InternalError`.

     `<text>` is the error's display text; for a throwing JavaScript
     expression it starts with `Error: ` and ends with an internal source
     location.
  2. **Pickup of a root flow** (no parent): first start, re-queue after the
     preprocessor, or resume after a suspend.
     - The error is a **failure of the module at the cursor**, with value
       `{"error": {"message": "Error: <text> @<location>", "name":
       "Anyhow"}}`.
     - Retry, `continue_on_error` and the failure module apply.
     - Only expressions on the path actually revisited are evaluated again:
       a retry re-runs the module's own `skip_if` and `timeout`; a switch to
       the failure module evaluates the failure module's `skip_if` and
       `timeout`, not the failed module's; the cursor-0 checks run on every
       start phase at cursor 0 ([FX-49](#fx-49)). A retry that hits the same
       throwing expression therefore ends in context 1.
  3. **Pickup of a child flow** (an inline child or a subflow by path):
     - the child job completes **failed** at once, with `{"error":
       {"message": "Error: <text> @<location>", "name": "Anyhow"}}`;
     - **its own modules, retry and failure module are bypassed**;
     - the parent handles it as a failed module: the parent's retry,
       tolerance and failure module apply, and a `flow` module resets the
       failure-module bookkeeping ([FX-5](#fx-5)).

  The name is `Anyhow` at pickup because the start phase returns a generic
  error type at the flow-job boundary, which the worker wraps into its
  `Anyhow` error kind, displayed as `Error: <inner> @<location>`.

  Messages:
  - `Timeout value cannot be negative`
  - `Expected an integer, found: <value>` (sleep given a negative integer or
    a non-number; `<value>` is a debug rendering such as `String("5")`)
  - `Expected a number value, had error instead: …` (the sleep expression
    threw)
  - predicate engine errors render as `Error: <engine message> @<source
    location>`.
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:2085-2156` (InternalError
    completion; parent receives the previous value), `:2777-2793` (generic
    error type), `:3280-3321`, `:3694-3713`, `:3723-3736`, `:3869`,
    `:4392-4396`
  - `backend/windmill-worker/src/worker.rs:3815-3840`, `:3180-3212`,
    `:1706-1737`
  - `backend/windmill-worker/src/result_processor.rs:1910-1967`
  - `backend/windmill-common/src/error.rs:73-75`, `:109`, `:145-151`
- **Conformance:** [FX-15/a–FX-15/c](conformance.md#fx-15)
- **Design note (not Windmill behaviour):** windmill-lite should match the
  error `name` and the message prefix, not the location suffix.

<a id="fx-16"></a>
### FX-16 · `identity` module

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** An identity job returns its args' `previous_result` field, or
  `{}` if that field is missing. Which args it gets depends on the args rule
  that won ([FX-2](#fx-2), start step 8):
  - normally `{previous_result: <preceding result>}`, so the identity passes
    the preceding result through;
  - with `pass_flow_input_directly: true`, the args are the flow input, so it
    returns `flow_input.previous_result`, or `{}`;
  - on a retry, the reused args' `previous_result`, or `{}`;
  - with `mock`, the mock value ([FX-18](#fx-18)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3874-3927`,
  `:5141`; `backend/windmill-worker/src/worker.rs:4032-4038`
- **Conformance:** [FX-16/a, FX-16/b](conformance.md#fx-16)

<a id="fx-17"></a>
### FX-17 · `skip_if`

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** The predicate (scope in [FX-8](#fx-8)) is evaluated before the
  module starts, for any module type, and again when a retry re-enters it.
  When it is true:
  - the module does not run; an identity job runs instead;
  - the module's status is `Success{skipped: true}`;
  - its `stop_after_if` is not evaluated;
  - a `suspend` on it is not armed, so the next step does not wait;
  - its `sleep`, if any, still delays the next step (sleep is evaluated when
    the next step starts).

  The flow continues and is *not* marked skipped. If the skipped module is
  the last one, the flow result is its passthrough value.

  **The passthrough value follows the args precedence** ([FX-2](#fx-2),
  start step 8; [FX-16](#fx-16)):
  - normally `{previous_result}`, so the preceding result;
  - `mock` → the mock value ([FX-18](#fx-18));
  - a retry → the failed attempt's args, so a module that becomes skipped
    only on retry returns those args' `previous_result` field, typically
    `{}`;
  - `pass_flow_input_directly` → as in [FX-16](#fx-16).

  The failure module's own `skip_if` is evaluated when it starts. If true,
  the failure module is replaced by a passthrough, whose identity args still
  follow the args precedence: normally the failing value, but a retry of the
  failure module reuses its failed args ([FX-32](#fx-32)).

  A throwing predicate is a chaining error ([FX-15](#fx-15)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3842-3927`,
  `:5136-5138`, `:645-684` (no `stop_after_if` on identity at `:684`),
  `:1278-1283` (skipped flag), `:6197-6231` (no suspend wait)
- **Conformance:** [FX-17/a, FX-17/b](conformance.md#fx-17)

<a id="fx-18"></a>
### FX-18 · `mock`

**Scope:** later · **Confidence:** high (main rule); medium (`skip_if` interplay) · **Status:** confirmed

- **Rule:** If `mock.enabled` is true, the module (of any type, loops and
  branches included) does not run; an identity job returns
  `mock.return_value`, or `null` if it is absent. `mock.enabled: false` is
  ignored.

  Mock takes precedence over every other args rule and over `skip_if`: the
  skip predicate is still evaluated, and its errors still surface.
  `stop_after_if` *is* evaluated on the mock value, unless the module also
  has a `skip_if`: an identity with any configured `skip_if` is classified
  as skipped, even if the predicate returned false, and stop is then not
  evaluated.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3874-3890`,
  `:5106-5138`, `:645-662`, `:1278-1283`;
  `backend/windmill-types/src/flows.rs:536-541`; Docs:
  https://www.windmill.dev/docs/flows/step_mocking
- **Conformance:** [FX-18/a](conformance.md#fx-18)

<a id="fx-19"></a>
### FX-19 · `stop_after_if` on a step, with its interactions

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** `stop_after_if` is evaluated only after the module's job
  **succeeded**. It is not evaluated on a failed job, on a skipped identity,
  on a `branchall`, per iteration of a parallel loop (on non-parallel loops
  it acts per iteration; [FX-20](#fx-20)), or on the failure module or the
  preprocessor (those also ignore their own `continue_on_error`).

  If it is true, the flow stops early; `skip_if_stopped` wins over
  `error_message`. The flow log says `Flow job stopped early because of a
  stop early predicate returning true`. Three modes:
  - **`skip_if_stopped: true`** (clean stop): the module is
    `Success{skipped: true}`; the flow completes **successfully** with status
    **`skipped`** and the stopping module's result; later modules never run
    (they stay `WaitingForPriorSteps`).
  - **`skip_if_stopped: false`, no `error_message`** (clean stop): the same,
    except the flow status is `success` and the module is not marked skipped.
  - **`skip_if_stopped: false`, `error_message` present** (error-stop): the
    module is `Failure`, and the carried value is `{"error": {"name":
    "EarlyStopError", "message": <msg>}}`. `<msg>` is `error_message`, or
    `stop after if: <expr>` when `error_message` is the empty string. With
    `error_include_result: true` the error object also gets `"result":
    <stopping module's result>`; for loops that is the array of child
    results.

  **An error-stop behaves like a failure of the module whose result is the
  `EarlyStopError`:**
  - **No failure module, no `continue_on_error`:** the flow fails with the
    `EarlyStopError`. A configured `retry` is **not** used, because the
    continue decision stops first.
  - **With a failure module:** the continue decision re-enters the module,
    and re-entry evaluates retry first. A step with attempts left is re-run
    (and may stop again) before the failure module; once attempts run out,
    the failure module runs and sees the `EarlyStopError`.
  - **`continue_on_error: true`, no retry due:** the cursor advances.
    - Another regular module follows: with a failure module, that next module
      runs (its `previous_result` is the `EarlyStopError`) and the failure
      module is not reached for this stop; without one, the flow fails with
      the `EarlyStopError` and the later modules never run.
    - The stopping module is the final regular module: with a failure module,
      the cursor reaches `len` and **the failure module runs**
      ([FX-V4](#fx-v4)); the flow ends with its result, failed unless it
      succeeds and returns `recover: true` ([FX-33](#fx-33)). Without one,
      the flow **succeeds** with the `EarlyStopError` as its result.
  - **`continue_on_error: true` and a retry due:** the cursor does not
    advance. With a failure module the retry happens; without one the flow
    fails.
- **Evidence:**
  - `backend/windmill-types/src/flows.rs:415-426`
  - `backend/windmill-worker/src/worker_flow.rs:304-322`, `:664-746`,
    `:678-679`, `:1272-1347`, `:1350-1367`, `:1369-1397`, `:1724-1764`,
    `:1807-1808`, `:1996-2059`, `:3167-3182`, `:3656`, `:3723-3832`
  - Docs: https://www.windmill.dev/docs/flows/early_stop
- **Conformance:** [FX-19/a–FX-19/i](conformance.md#fx-19)
- **Note:** the docs say the predicate can be evaluated "at the end of, or
  before, a step"; in the source the before-step check is `skip_if`, and
  `stop_after_if` runs after success only. The docs also call "skipped" just
  a label; in the source it is the flow job's completed status. A stored
  successful script result is not rewritten by an error-stop
  ([FX-V2](#fx-v2)).

<a id="fx-20"></a>
### FX-20 · How a stop-early propagates through containers

**Scope:** core · **Confidence:** high (read); medium (exact branch results) · **Status:** confirmed

- **Rule:**
  - **Non-parallel loop body:** a stop that fires there (from the loop
    module's `stop_after_if` evaluated per iteration, or propagated up from
    the body) **breaks only that loop**. The loop completes with the results
    gathered so far, and the outer flow continues. A loop module's own
    `stop_after_if` with `error_message`, firing on an iteration, is cleared
    together with the loop break, so the loop completes normally. That
    differs from a loop whose child *failed* carrying an `EarlyStopError`
    from inside an inline body: that is a failed child, handled by
    `skip_failures` and the loop failure rules
    ([control-flow.md](control-flow.md)).
  - **`branchone` body or sequential `branchall` branch:** a stop in the
    inline body propagates up, so the enclosing flow stops as well, with the
    same `skipped` flag. The error-message flavour reaches the parent as a
    plain failure.
  - **Parallel loops and parallel branchall:** a stop from a child is
    ignored.
  - **Subflow by path:** a stop inside it ends the subflow only
    (successfully, or failed for the error flavour). It never stops the
    parent through the stop channel.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:599-618`,
  `:670-679`, `:1195-1201`, `:2147-2152`
- **Conformance:** [FX-20/a](conformance.md#fx-20)
- **Note:** exact branch result shapes are in [control-flow.md](control-flow.md).

<a id="fx-21"></a>
### FX-21 · A `stop_after_if` that throws fails the step unrecoverably

**Scope:** core · **Confidence:** high (read); medium (exact string) · **Status:** confirmed

- **Rule:** If evaluating `stop_after_if` on a successful step throws,
  Windmill re-processes the completion as a failure:
  - the result is `{"error": "<error text>"}`: the error value is a
    **string**, not an object. It is the display string of the evaluation
    error; for an ordinary QuickJS exception it includes `Error: QuickJS
    evaluation error: … @<location>`;
  - the failure is unrecoverable: **no retry and no `continue_on_error`**;
  - the failure module runs if present;
  - the flow's root log gets
    `` Error evaluating stop_after_if of module '<id>' expression `<expr>`: … ``.

  This changes the module and flow outcome, not the already stored
  successful script result ([FX-V2](#fx-v2)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:223-251` (string
  wrap at `:235-241`), `:715-727`, `:1302-1327`, `:1743-1747`;
  `backend/windmill-common/src/error.rs:74-75`;
  `backend/windmill-jseval/src/lib.rs:419`
- **Conformance:** [FX-21/a](conformance.md#fx-21)

<a id="fx-22"></a>
### FX-22 · Flow-level `skip_expr`

**Scope:** later · **Confidence:** high (main rule); low (by-path first-module quirk) · **Status:** confirmed

- **Rule:** At cursor 0 of **every** flow job whose definition has a
  `skip_expr` (roots and subflows by path; inline children never have one),
  the predicate is evaluated (scope in [FX-8](#fx-8)). If it is true, the flow
  completes successfully with status `skipped` and the **string result
  `"stopped early"`**; no step runs. The check runs again on every start
  phase at cursor 0 ([FX-49](#fx-49)).

  Separately, for a root flow started by a schedule with `no_flow_overlap`:
  if another run of the same schedule is running, the result is the string
  `not allowed to overlap with <ids>, scheduling next iteration`, also
  skipped. Only this overlap check is root-only.

  Quirk: if module 0 is a `type: flow` module, the skip override is ignored,
  and the flow runs on from step 1 with module 0 recorded as done.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3224-3322`
  (overlap block `:3224-3280`), `:599-618`, `:670-679`
- **Conformance:** [FX-22/a](conformance.md#fx-22)
- **Note:** a skipped flow is reported as a failure by the synchronous HTTP
  endpoint ([FX-V3](#fx-v3)).

<a id="fx-23"></a>
### FX-23 · `sleep`

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** `sleep` on module `i` is an input transform (static or
  javascript; scope in [FX-8](#fx-8)). It is evaluated when module `i+1` is
  about to start, and module `i+1`'s job is scheduled for now plus the delay.
  With [FX-4](#fx-4), a sleep on the last module delays completion.

  **The value:**
  - a non-negative JSON integer means that many **seconds**;
  - a JSON float means seconds with millisecond resolution (the fraction is
    truncated to whole milliseconds); a **negative float clamps to 0**;
  - a negative integer, or any non-number (numeric strings and `null`
    included), is a chaining error ([FX-15](#fx-15)).

  **When it does not apply:** sleep is applied only when the next module is
  waiting. When a module is re-entered for a retry, no sleep is applied again;
  the retry delay replaces the scheduled time. After a stop-early nothing
  runs, so there is no sleep.

  Sleep still applies after a skipped module and after a tolerated failure.
  A pending sleep is cut short when the flow is canceled ([FX-38](#fx-38)):
  Windmill runs sleeps as a delayed queue entry, not as an in-process wait. A
  module with `sleep` is not pinned under `same_worker` ([FX-41](#fx-41)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3666-3718`,
  `:3753`, `:6187-6194`; `backend/windmill-types/src/flows.rs:561`; Docs:
  https://www.windmill.dev/docs/flows/sleep
- **Conformance:** [FX-23/a–FX-23/c](conformance.md#fx-23)

<a id="fx-24"></a>
### FX-24 · Per-step `timeout`: value, cap and enforcement by execution path

**Scope:** core (process, identity, child-flow and collapsed-loop paths); later (in-process executors) · **Confidence:** high (paths 1, 3, 4, 5; path 4 rests on an exhaustive search for readers); medium (path 2) · **Status:** confirmed

- **Rule:**
  - **Value.** `timeout` on a module is an input transform (a bare number is
    accepted as static), evaluated at push time (scope in [FX-8](#fx-8)) as a
    whole number of **seconds**. A negative value is a chaining error
    ([FX-15](#fx-15)): `InternalError` "Timeout value cannot be negative" at a
    later transition, an `Anyhow` failure of the module at root pickup.
  - **Precedence.** The effective timeout is the first that exists: the
    module's `timeout`; for a `script` module by path or hash, the script's
    own `timeout` metadata; the instance default job timeout; the instance
    maximum. `rawscript` steps have no script-level timeout.
  - **Cap.** A custom value strictly below the maximum is used as-is and
    counts as "job-specific". A custom value **equal to or above** the
    maximum is replaced by the maximum, with a logged warning, and counts as
    instance-wide. With no custom value, the instance default applies if it
    is strictly below the maximum, else the maximum; both count as
    instance-wide. The maximum is the instance's `TIMEOUT` (default 7 days
    self-hosted, 900 s on Windmill Cloud), ×6 for cloud premium workspaces.
  - **Enforcement depends on how the job executes:**
    1. **Process executors** (scripts run as a child process, such as Bun,
       Deno, Python, Go, Bash): a process timer kills the process, with the
       [FX-25](#fx-25) error and its "job-specific"/"instance-wide" wording.
    2. **In-process executors** (the native TypeScript runtime and the
       database, GraphQL, AI and similar executors; which language modes use
       them is decided in [languages.md](languages.md)): a timer around the
       evaluation. Expiry gives `ExecutionErr` with the text `Query timeout
       after (><whole seconds>s)`, where the seconds are the limit in
       milliseconds divided by 1000, rounded down. The worker wraps it as
       `{"message": "execution error:\nQuery timeout after (><n>s)", "name":
       "ExecutionErr", "step_id"?}`. The same cap rule applies. Cancellation
       on this path reads `Job cancelled by <user> (reason: <reason>)`
       instead of the process-kill text.
    3. **Identity jobs** (identity modules and skipped or mocked modules) run
       immediately in the worker **with no timer**. A stored timeout, even
       `0`, has no effect; a negative value still fails at launch.
    4. **Child flow jobs** (non-collapsed containers, subflows by path) have
       **no wall-clock deadline**. The timeout is evaluated and stored on the
       child flow job, but nothing consumes it: flow jobs bypass every
       executor timer, and no other reader was found in the worker, monitor
       or queue.
    5. **Collapsed loops** ([FX-46](#fx-46)): the loop module's timeout is
       evaluated again for every iteration and applies to each iteration's
       script job through path 1 or 2. The body module's own timeout is
       ignored. For a `script` body by path, the script's own timeout applies
       only when the loop module has none.
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:4379-4401`, `:5657-5697`,
    `:5908-5983`, `:6140-6144`
  - `backend/windmill-worker/src/common.rs:1056-1114` (premium multiplier at
    `:1063-1082`; cap equality at `:1084-1097`)
  - `backend/windmill-worker/src/handle_child.rs:172-192`, `:199-229`
    (process timer), `:723-747` (in-process timer and messages)
  - `backend/windmill-worker/src/js_eval.rs:143-210`
  - `backend/windmill-worker/src/worker.rs:3536-3576` (native TypeScript
    passes the job timeout), `:3564`, `:3815-3845` (flows bypass executors),
    `:4032-4038` (identity)
  - `backend/windmill-worker/src/result_processor.rs:622-627`
  - `backend/windmill-types/src/flows.rs:566-571`, `:805-830`
  - `backend/windmill-common/src/worker.rs:210-211`, `:295-298`
  - In-process timer users: `ai_executor.rs`, `bigquery_executor.rs`,
    `duckdb_executor.rs`, `graphql_executor.rs`, `mssql_executor.rs`,
    `mysql_executor.rs`, `oracledb_executor.rs`, `pg_executor.rs`,
    `snowflake_executor.rs`, `js_eval.rs`, `bash_executor.rs` (one mode),
    all under `backend/windmill-worker/src/`
  - `backend/src/monitor.rs`: searched; only zombie/ping timeouts and
    setting reloads
  - Docs: https://www.windmill.dev/docs/flows/custom_timeout
- **Conformance:** [FX-24/a–FX-24/g](conformance.md#fx-24)

<a id="fx-25"></a>
### FX-25 · Process timeout: enforcement and error

**Scope:** core · **Confidence:** high (message); medium (that the canceled step status leaves the flow uncanceled) · **Status:** confirmed

- **Rule:** When a step's **process** outlives its timeout, the worker:
  1. marks the job canceled with reason `duration > <secs>` and canceler
     `timeout`;
  2. interrupts the process, then terminates it, then kills it;
  3. fails the step.

  The error object is `{"message": "execution error:\njob process terminated
  due to timeout after exceeding job-specific duration limit", "name":
  "ExecutionErr", "step_id": <id>}`. "job-specific duration limit" appears
  only when the custom timeout is strictly below the maximum; a custom value
  at or above the maximum, or the instance default, gives "instance-wide job
  duration limit" ([FX-24](#fx-24)). The step job's completed status is
  `canceled`, but **the flow is not canceled**: this is an ordinary step
  failure, so retry, `continue_on_error` and the failure module apply (the
  flow checks only its own cancel flag).

  This shape is for process timeouts only; in-process and identity paths
  follow [FX-24](#fx-24).
- **Evidence:** `backend/windmill-worker/src/handle_child.rs:172-192`,
  `:199-229`, `:340-346`; `backend/windmill-worker/src/result_processor.rs:622-627`,
  `:1625-1649`; `backend/windmill-worker/src/worker_flow.rs:1736`;
  `backend/windmill-worker/src/common.rs:1056-1114`
- **Conformance:** [FX-25/a](conformance.md#fx-25)

<a id="fx-26"></a>
### FX-26 · `retry`: the delay schedule and its exact arithmetic

**Scope:** core (formula); the out-of-range domain is a design decision · **Confidence:** high (formula); medium (wrapping, from the build profile and language defaults) · **Status:** confirmed

- **Rule:** `retry = {constant: {attempts, seconds}, exponential: {attempts,
  multiplier, seconds, random_factor}, retry_if?}`. Missing parts default to
  zero attempts; `multiplier` defaults to 1.

  Let `n` be the number of failures of this module so far in the current
  retry sequence (`0` before the first retry; see [FX-29](#fx-29)).
  - If `n < constant.attempts`: the delay is `constant.seconds`.
  - Else, if `n − constant.attempts < exponential.attempts`: the exponential
    delay below. **The exponent counts all failures, constant phase
    included.**
  - Otherwise: no retry.

  Total retries after the first run = `constant.attempts +
  exponential.attempts`.

  Exponential delay, in the released build's unsigned 16-bit arithmetic
  (overflow wraps, because release builds do not check integer overflow):
  1. `pow = seconds^(n+1)`, **saturating** at 65535;
  2. `delay = (multiplier × pow) mod 65536`;
  3. **jitter**, only in the exponential phase and only when
     `random_factor > 0`: pick an integer `p` uniformly in
     `[0, min(random_factor, 100))`; `delta = ((delay × p) mod 65536) div
     100`; with probability ½, `delay = min(delay + delta, 65535)`, otherwise
     `delay = max(delay − delta, 0)`.

  Constant-phase delays are never jittered. The scheduled delay is
  `min(delay, 21600)` seconds (6 hours).

  **Deploy validation:** a flow with exponential `attempts > 0` and
  `seconds == 0` is rejected with `Module '<id>': Exponential backoff base
  (seconds) must be greater than 0. A base of 0 would cause immediate
  retries.`
- **Evidence:**
  - `backend/windmill-types/src/flows.rs:199-207`, `:433-510` (formula at
    `:444-470`, `:489-510`; jitter at `:448-462`)
  - `backend/windmill-types/src/flow_status.rs:12-13`
  - `backend/windmill-worker/src/worker_flow.rs:2390-2425`
  - `backend/Cargo.toml:107-110` (the release profile sets no overflow
    checks)
  - Docs: https://www.windmill.dev/docs/flows/retries
- **Conformance:** [FX-26/a–FX-26/e](conformance.md#fx-26)
- **Design note (not Windmill behaviour):** which part of this numeric domain
  windmill-lite supports is a design decision; any deviation must be stated
  in the equivalence contract and tested at the boundary.

<a id="fx-27"></a>
### FX-27 · `retry`: what is retried and how state evolves

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** When a module fails, a retry is scheduled if its `retry` (or,
  while the failure module runs, the failure module's `retry`) still has
  attempts left by [FX-26](#fx-26), and `retry_if`, if present, is true
  ([FX-28](#fx-28)).

  **What the retry does:**
  - the **same module** re-runs after the delay;
  - a script or flow module re-runs with **the same args as the failed
    attempt**; input transforms are not re-evaluated, and a pre-run error is
    not copied ([FX-14](#fx-14));
  - a loop or branch module restarts from scratch (the iterator and
    predicates are re-evaluated);
  - before the re-run, `previous_result` is restored to the preceding
    module's result ([FX-6](#fx-6)), except for the failure module
    ([FX-32](#fx-32)), and `skip_if` is re-evaluated. A module newly skipped
    on retry returns the reused args' `previous_result` field
    ([FX-17](#fx-17)).

  **State:**
  - `fail_count` goes up by one per retry, and failed attempt job ids pile up
    in `failed_retries`;
  - when the module finally succeeds, the retry state is cleared and its
    `Success` status lists `failed_retries`;
  - retry is evaluated before `continue_on_error`: a tolerated step is first
    retried to exhaustion;
  - retry is never applied to unrecoverable failures ([FX-21](#fx-21),
    [FX-44](#fx-44));
  - starting a failure module that has its own retry config resets the
    counter to 0.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3720-3832`,
  `:3891-3905`, `:1306-1327`, `:1491-1503`, `:1701-1722`, `:1751`,
  `:3815-3827`
- **Conformance:** [FX-27/a](conformance.md#fx-27)

<a id="fx-28"></a>
### FX-28 · `retry_if`

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** `retry.retry_if.expr` is a boolean predicate (scope in
  [FX-8](#fx-8): `result` and `previous_result` are the failed step's wrapped
  error; no `results`). It is evaluated **before** the attempts check. If it
  is false, no retry happens and the failure proceeds (`continue_on_error`,
  then the failure module, else the flow fails).

  It can be evaluated twice per failure: once in the completion phase (the
  continue decision or the tolerance check) and again on re-entry. Do not
  assume one evaluation per attempt. A throw has a different effect at each
  point:
  - **during completion:** the update is re-processed as an unrecoverable
    failure with a **string** error, as in [FX-21](#fx-21): no retry, no
    tolerance, and the failure module runs if there is one;
  - **on re-entry:** a chaining error ([FX-15](#fx-15)).
- **Evidence:** `backend/windmill-types/src/flows.rs:428-442`;
  `backend/windmill-worker/src/worker_flow.rs:2390-2425`, `:1306-1327`,
  `:1701-1751`, `:223-251`, `:3723-3736`
- **Conformance:** [FX-28/a–FX-28/c](conformance.md#fx-28)

<a id="fx-29"></a>
### FX-29 · Quirk: the retry counter carries over after a tolerated failure

**Scope:** core · **Confidence:** medium (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** The retry counter is flow-wide and is cleared only when a module
  **succeeds**. If module `k` exhausts its retries and is then tolerated by
  `continue_on_error`, the counter is not reset. A later module that fails
  starts with `n` already equal to `k`'s failure count, so it may get fewer
  retries (or none), and its exponential delays are larger. A successful
  module in between clears the counter.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:1491-1503`
  (cleared only on `Success`), `:1306-1327`, `:2390-2425`, `:3723-3769`
- **Conformance:** [FX-29/a, FX-29/b](conformance.md#fx-29)
- **Design note (not Windmill behaviour):** windmill-lite must reproduce this
  to be exact.

<a id="fx-30"></a>
### FX-30 · `continue_on_error`

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** When a module with `continue_on_error: true` fails (after
  retries are exhausted, [FX-27](#fx-27)):
  - its status is `Failure`, but the cursor still advances;
  - the next module's `previous_result` (and `results.<id>`) is the failed
    module's wrapped error;
  - at the last module, the flow completes **successfully**, with that
    wrapped error as its result.

  It also applies to error-stops ([FX-19](#fx-19)): the cursor advances, and a
  last-step error-stop without a failure module ends successfully.

  It does not apply to unrecoverable failures ([FX-44](#fx-44)); to the
  failure module and the preprocessor, whose own flag is ignored; or to
  collapsed loop bodies, where the body step's flag is ignored and the loop
  module's flag counts ([FX-46](#fx-46)).

  **The failure module does not run for a tolerated failure**, with exactly
  two exceptions, both error-stops:
  1. an error-stop with `continue_on_error` where a retry was due, so the
     cursor did not advance;
  2. an error-stop with `continue_on_error`, no retry due, at the **final**
     regular module, with a failure module ([FX-V4](#fx-v4)).

  An ordinary tolerated failure of the last script still ends the flow
  successfully with the wrapped error, and no failure module runs; the
  error-stop continue rule is the one that checks for a failure module first.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:678-679`, `:742`,
  `:1302-1346`, `:1724-1764`, `:5657-5697`
- **Conformance:** [FX-30/a](conformance.md#fx-30); also
  [FX-19/e–FX-19/g, FX-19/i](conformance.md#fx-19).

<a id="fx-31"></a>
### FX-31 · `failure_module`: when it runs

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** The failure module runs at most once per flow run (repeated only
  through its own `retry`). It runs when the continue decision ([FX-2](#fx-2),
  completion step 7, rules 4 and 7) asks to continue for a failure and the
  next start phase finds no retry due, or when a tolerated final-module
  error-stop advances the cursor to `len` ([FX-V4](#fx-v4)).

  **Triggers:**
  - untolerated step and container failures;
  - error-stops with no retry left ([FX-19](#fx-19));
  - a tolerated error-stop at the final regular module ([FX-V4](#fx-v4));
  - preprocessor failures ([FX-35](#fx-35));
  - suspend timeout or disapproval ([FX-39](#fx-39));
  - unrecoverable failures ([FX-44](#fx-44));
  - root-pickup chaining errors ([FX-15](#fx-15), context 2).

  **It does not run:**
  - on tolerated failures, except the error-stop cases of [FX-30](#fx-30);
  - on cancel ([FX-38](#fx-38));
  - on later-transition chaining errors ([FX-15](#fx-15), context 1);
  - on child-pickup chaining errors, where the child's own failure module is
    bypassed and the parent's may run;
  - after an inline child already ran its copy for the same failure
    ([FX-34](#fx-34));
  - for a clean stop;
  - again after it failed itself, except through its own `retry`;
  - when its own `skip_if` is true; it is then replaced by a passthrough
    ([FX-17](#fx-17)).

  **While it runs** the cursor is `len`, and its status is the flow's
  `failure_module` entry, with `parent_module` set to the id of the module
  that failed. When it is entered directly at cursor `len` ([FX-V4](#fx-v4)),
  `parent_module` records the failure module's own id.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:1399-1428`,
  `:1734-1760`, `:2085-2129`, `:3723-3832`, `:4725-4745`;
  `backend/windmill-worker/src/result_processor.rs:1945-1967`; Docs:
  https://www.windmill.dev/docs/flows/flow_error_handler
- **Conformance:** [FX-31/a](conformance.md#fx-31); also
  [FX-15](conformance.md#fx-15), [FX-19/d, FX-19/e, FX-19/i](conformance.md#fx-19).

<a id="fx-32"></a>
### FX-32 · `failure_module`: what it sees

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** The failure module is a normal module (usually a script); its
  input transforms have the usual scope ([FX-7](#fx-7)). On its initial run:
  - `previous_result` is the triggering value: the failed module's wrapped
    error `{"error": E}`, an `EarlyStopError` object, or for loops and
    branches the array of child results;
  - `error` is bound to `previous_result.error`, or derived as in
    [FX-8](#fx-8);
  - the failed module counts as the preceding step: an exact
    `results.<failed id>` expression ([FX-10](#fx-10), route 1) returns the
    in-memory triggering value, while a **simple subpath** such as
    `results.<failed id>.x` (route 2) reads the failed module's **stored job
    result**; for an error-stop that is the script's original successful
    value ([FX-V2](#fx-v2)). Which engine expressions use memory is disputed
    ([FX-10](#fx-10), [FX-V6](#fx-v6));
  - other `results.<id>` read storage;
  - `flow_input` is the flow input;
  - `started_at` (the flow's start timestamp) and `flow_job_id` (a UUID
    string) are available when the flow has started;
  - its `WM_FLOW_STEP_ID` is its own id, suffixed inside inline bodies
    ([FX-34](#fx-34)).

  When it is entered directly at cursor `len` ([FX-V4](#fx-v4)), the
  preceding id is empty, so `results.<stopped module>` reads storage.

  **Retry of the failure module itself:** the failed attempt's args are
  reused; `previous_result` stays the failure module's own failed-attempt
  value, because the restore step of [FX-27](#fx-27) skips the failure
  module; its `skip_if`, if any, sees that value.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2737-2743`,
  `:3325-3327`, `:3778-3792`, `:3805-3829`, `:3891-3905`, `:3936-3947`;
  `backend/windmill-jseval/src/lib.rs:180-218`, `:494-526`
- **Conformance:** [FX-32/a, FX-32/b](conformance.md#fx-32); also
  [FX-V2/a](conformance.md#fx-v2).

<a id="fx-33"></a>
### FX-33 · `failure_module`: the flow's final result and `recover`

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** After the failure module ran, the flow completes with the
  **failure module's result as its result**, stored as-is with no wrapping.
  The flow succeeds only if the failure module's job **succeeded** and its
  result is an object whose `recover` field is the JSON boolean `true`;
  otherwise it is failed. A string `"true"` or a non-object does not count.
  If the failure module itself fails, the flow fails with the failure
  module's wrapped error.

  **Seen from a parent**, a child flow (inline or by path) that ran its
  failure module always reports **failure**, even with `recover: true`. The
  parent receives the failure module's result as the module result. The
  child's own job is still marked successful when it recovered.

  The flow log says `Flow job completed with error, and error handler was
  triggered. It completed with <success|error>, and with recover: <bool>`.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:409-412`,
  `:1809-1810`, `:2006-2059`, `:2143-2153`
- **Conformance:** [FX-33/a, FX-33/b](conformance.md#fx-33)

<a id="fx-34"></a>
### FX-34 · `failure_module` inside inline bodies runs at the innermost level

**Scope:** core · **Confidence:** high (read); medium (per-iteration consequence) · **Status:** confirmed

- **Rule:** Inline bodies carry a copy of the flow's failure module
  ([FX-5](#fx-5)). When a step inside a branch or loop body fails and is not
  tolerated there, **the copied failure module runs inside that body's child
  flow**. It sees the inner failing step as `previous_result`, and its id is
  suffixed with the numeric parent cursor and iteration or branch index (for
  example `failure-2`, or `failure-2-0` for loop iteration 0 at step 2), never
  with the container's id.

  The child then reports failure to the parent, with the failure module's
  result. The parent does not run the failure module a second time and
  fails, unless it tolerates the container's failure. Only a subflow by path
  resets this; there, the parent's own failure module also runs.

  **Consequence:** with a `forloopflow` that has `skip_failures: true`, the
  failure module runs **once per failing iteration** while the loop
  continues, and its result becomes that iteration's entry in the loop
  result array. Copying a failure module also prevents the loop from
  collapsing ([FX-46](#fx-46)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:498-500`,
  `:1085-1091`, `:1753-1757`, `:2153`, `:5058-5061`, `:5367-5376`,
  `:5700-5711`, `:5733-5744`
- **Conformance:** [FX-34/a](conformance.md#fx-34)

<a id="fx-35"></a>
### FX-35 · `preprocessor_module`

**Scope:** later · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:**
  - **When it runs:** only when a root flow is started with preprocessing
    applied: by a trigger (webhook, HTTP route, …) or by the run API without
    `skip_preprocessor=true`, and only for a deployed flow that has one.
    Preview and raw flows drop it. A `type: flow` module launches its subflow
    with preprocessing disabled ([FX-5](#fx-5)). (This describes the
    flow-module launch paths; it makes no claim about other API callers that
    set parent metadata.)
  - **How it runs:** first, at cursor `-1`, as a job whose args are the
    flow's original args plus `"_ENTRYPOINT_OVERRIDE": "preprocessor"`. The
    script's exported `preprocessor` function is called instead of `main`.
    The trigger payload arrives in the arg `event` (or the legacy
    `wm_trigger`); see [languages.md](languages.md) and
    [client-api.md](client-api.md).
  - **On success** its result **replaces the flow's input**: an object
    becomes the new input; `null` becomes `{}`; any other value becomes
    `{"value": <result>}`. The flow is re-queued and starts at step 0 with
    the new input, which is `flow_input` and step 0's `previous_result`.
  - **On failure** the normal failure path applies: its own `retry`, then the
    failure module. Its `stop_after_if` and `continue_on_error` are ignored
    ([FX-19](#fx-19)).
  - Inline bodies never have a preprocessor. Script-level preprocessors
    (`apply_preprocessor` on a `script` step) are in
    [languages.md](languages.md).
- **Evidence:**
  - `backend/windmill-types/src/flow_status.rs:503-533`
  - `backend/windmill-queue/src/jobs.rs:5649-5651`, `:5936-5938`
  - `backend/windmill-api-jobs/src/execution.rs:746-747`
  - `backend/windmill-worker/src/worker_flow.rs:678`, `:1510-1694` (input
    replacement at `:1644-1694`), `:3906-3912`, `:6035-6040`
  - `backend/windmill-types/src/jobs.rs:595`
  - Docs: https://www.windmill.dev/docs/core_concepts/preprocessors
- **Conformance:** [FX-35/a](conformance.md#fx-35); also
  [FX-5/a](conformance.md#fx-5).

<a id="fx-36"></a>
### FX-36 · The flow's final result and the HTTP response

**Scope:** core (executor result selection; HTTP for oracle interpretation); later (HTTP adapter) · **Confidence:** high (object metadata decoding); non-object decoding unsettled · **Status:** needs-evidence

- **Rule (as drafted):**
  - **Executor result.** When the flow stops ([FX-2](#fx-2)) its result is:
    - normal completion: the result of the last module that completed (a
      script result; a passthrough from identity, skip or mock; for loops and
      branchall the array of child results in order,
      [control-flow.md](control-flow.md));
    - stop-early: the stopping module's result, or the `EarlyStopError`
      object ([FX-19](#fx-19));
    - failure without a failure module: the failed module's result exactly as
      it reached the flow, typically the wrapped error `{"error": E}`; for a
      failed loop or branchall, the array of child results;
    - failure module ran: its result ([FX-33](#fx-33));
    - canceled: the `Canceled` error ([FX-38](#fx-38));
    - chaining error: the `InternalError` ([FX-15](#fx-15));
    - empty flow: the input ([FX-3](#fx-3)).

    The stored value is the result; a flow job never wraps it. It is stored
    as jsonb ([FX-42](#fx-42)). The plain "get result" endpoint always
    returns 200.
  - **Synchronous run-and-wait response.** Success means stored status
    `success` only, so a `skipped` flow counts as failed ([FX-V3](#fx-v3)).
    The stored result is first decoded as an object with these optional,
    typed fields:
    - `windmill_status_code` (alias `wm_status_code`): an unsigned 16-bit
      integer;
    - `windmill_content_type` (alias `wm_content_type`): a string;
    - `windmill_headers` (alias `wm_headers`): a map of string to string;
    - `windmill_content_transfer_encoding` (alias
      `wm_content_transfer_encoding`): a string;
    - `result`: any JSON value.

    Then:
    - **Decoding fails** (the result is not a suitable object, or a field has
      the wrong type, for example a string status code or a non-string header
      value): the ordinary response, **200** if the flow succeeded, else
      **422**, with the **whole** stored result as the body.
    - **Decoding succeeds but every metadata field is absent or `null`:** the
      ordinary response, as above.
    - **At least one metadata field is non-null:** the status is the decoded
      code (one that is not a valid HTTP status gives the error `Invalid
      status code`); with no code, 422 when failed, else 200 when `result`
      decoded to a non-null value, else 204. Headers are set from
      `windmill_headers` (an invalid name or value is an error). The body,
      with a content type, is the nested `result` as text (a JSON string is
      unquoted; `base64` transfer encoding decodes it and requires a string);
      without one, the nested `result` as JSON; with no non-null `result`,
      empty.

    This applies on success **and** on failure.
- **Correction (validation):** the object cases above (typed fields, `null`
  values, invalid types) are confirmed. Generalising "decoding fails" to
  every non-object result is not settled: the typed decoder might accept a
  JSON array positionally (for example `[201, null, null, null, 7]`). Treat
  the non-object, array case as provisional.
- **Open:** [Q-FX-12](#q-fx-12).
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:1478-1489`, `:1972-2059`
  - `backend/windmill-api-jobs/src/execution.rs:251-261` (typed fields),
    `:323-347`, `:374-496` (null handling at `:384-389`; decode fallback at
    `:485-496`), `:499-517`
- **Conformance:** [FX-36/a–FX-36/g](conformance.md#fx-36); also
  [FX-V3/a](conformance.md#fx-v3).

<a id="fx-37"></a>
### FX-37 · `early_return`

**Scope:** later · **Confidence:** high (poll loop); medium (lookup details) · **Status:** confirmed

- **Rule:** `early_return` is a **step id**, not an expression, and it only
  affects the synchronous run-and-wait API. As soon as that step has a
  result, the API returns it (with that step's success status, where the
  step lookup accepts both `success` and `skipped`) while the flow **keeps
  running** in the background; it is not canceled. If that step failed and
  the flow has a failure module, the early value is discarded and the API
  waits for the final flow result. No expression is evaluated, nothing extra
  is stored, and the flow's own final result is unaffected.
- **Evidence:** `backend/windmill-types/src/flows.rs:296-297`;
  `backend/windmill-api-jobs/src/execution.rs:186-208`, `:290-322`,
  `:350-368`; `backend/windmill-queue/src/jobs.rs:4463-4540`; Docs:
  https://www.windmill.dev/docs/flows/early_return
- **Conformance:** [FX-37/a](conformance.md#fx-37)

<a id="fx-38"></a>
### FX-38 · Cancellation

**Scope:** core (host-initiated cancel with the same result shape) · **Confidence:** high (steady state); low (race) · **Status:** confirmed

- **Rule:** Canceling a flow sets its cancel flag and the flag of every
  queued descendant job, up to depth 500. Queued children are made due
  immediately, which cuts pending sleeps and suspends short. A queued child
  fails without running, with the `Canceled` error value. A running step's
  process is killed when the worker's ping loop notices the flag; the killed
  script's own error is `{"message": "execution error:\njob process
  terminated due to cancelled by <user> (reason: <reason>)", "name":
  "ExecutionErr", "step_id": <id>}` (the in-process path reads differently,
  [FX-24](#fx-24)).

  When the flow next processes a completion and sees its own cancel flag, it
  stops **without retry, without `continue_on_error` and without running the
  failure module**. It completes with status **`canceled`**, log `Flow job
  canceled`, and result `{"error": {"message": "Job canceled: <reason> by
  <canceler>", "name": "Canceled", "reason": <reason>, "canceler":
  <canceler>}}`, whatever the killed step reported. The defaults are `no
  reason given` and `unknown` (`unexplicited reasons` when a non-running
  top-level job is canceled directly).

  **Ordering.** The continue decision checks a stop before the cancel flag;
  the completion checks the cancel flag first. So a canceled flow that stops
  cleanly completes as `Canceled`, not skipped. When an error-stop and a
  cancel coincide and a failure module exists, the continue decision requests
  the failure module. A job pushed after the cancel cascade is not flagged
  and may run; the flow then ends `Canceled` on the next completion. **No
  ordering is promised for that race.**
- **Evidence:**
  - `backend/windmill-queue/src/jobs.rs:201-262`, `:264-395`, `:6896-6903`
  - `backend/windmill-worker/src/worker.rs:3644-3646`
  - `backend/windmill-worker/src/handle_child.rs:182-192`, `:340-346`
  - `backend/windmill-worker/src/result_processor.rs:622-627`
  - `backend/windmill-worker/src/worker_flow.rs:1734-1736`, `:1805-1806`,
    `:1972-1988`
- **Conformance:** [FX-38/a](conformance.md#fx-38); the race has no fixture.

<a id="fx-39"></a>
### FX-39 · `suspend` / approval: behaviour when a flow meets it

**Scope:** out · **Confidence:** high (source-traced) · **Status:** confirmed

- **Rule:** `suspend` on module `i` with `required_events > 0` makes module
  `i+1` wait. The flow parks until enough resume events arrive, or until
  `suspend.timeout` seconds pass (default **30 minutes**).
  - **Enough events:** module `i+1` runs, and its transforms see `resume`
    (the last resume payload), `resumes` (all payloads, oldest first) and
    `approvers` (their usernames, or `"anonymous"`).
  - **Timeout or disapproval:** with `continue_on_disapprove_timeout` set,
    the flow continues and `resume` is `{"error": {"message": "Timed out
    waiting to be resumed", "name": "SuspendedTimedOut"}}` (or `"Disapproved
    by <user>"` / `"SuspendedDisapproved"`). Otherwise the module fails with
    that value as an unrecoverable failure: no retry, no
    `continue_on_error`, but the failure module runs ([FX-44](#fx-44)).
  - A `suspend` on a module that `skip_if` skipped is ignored.
  - A suspend on the last module gets the virtual step of [FX-4](#fx-4).
  - An error in the suspend's `user_groups_required` expression is a
    chaining error ([FX-15](#fx-15)). A module with `suspend` is not pinned
    under `same_worker` ([FX-41](#fx-41)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3348-3654`,
  `:6197-6231`; `backend/windmill-worker/src/result_processor.rs:450-480`
  (completion by signal passes the unrecoverable flag);
  `backend/windmill-types/src/flows.rs:512-534`; Docs:
  https://www.windmill.dev/docs/flows/flow_approval
- **Conformance:** none (the only case given is a windmill-lite policy check;
  see the design note).
- **Design note (not Windmill behaviour):** windmill-lite must refuse flows
  that use `suspend` at load time, or fail them with a clear
  unsupported-feature error (for example `unsupported: suspend`). It must not
  silently ignore the suspend.

<a id="fx-40"></a>
### FX-40 · `cache_ttl` (step and flow)

**Scope:** later · **Confidence:** high (TTL matrix, failed-flow caching); medium (S3-aware hashing) · **Status:** DISPUTED

- **Rule (as drafted):**
  - **Lookup.** Before any job with a `cache_ttl` (seconds) runs, including
    a whole flow job with a flow-level `cache_ttl`, the worker looks up a
    cache entry. The key is a SHA-256 of the job kind; the script hash, or
    the path plus the raw code or flow; and the args with keys sorted.
    S3-aware argument hashing includes the object's ETag and can leave out
    its path (`cache_ignore_s3_path`).
  - **Hit.** The job completes successfully at once with the cached result,
    logging `Job skipped because args & path found in cache and not
    expired`. For a flow, no step runs.
  - **Store.** A result is stored with expiry now plus the TTL when the job
    succeeded. **Failed flows can be cached:** a flow stores its final result
    when the last processed job succeeded, and that check comes **before**
    the `recover` check. So a flow whose failure module ran successfully
    without `recover: true` fails but caches the failure module's result, and
    a later identical run completes **successfully** from the cache with that
    value.
  - **Which TTL applies to a step:**

    | Step kind | TTL source |
    |---|---|
    | `rawscript` / `flowscript` | the module's `cache_ttl` |
    | `script` pinned by hash | the module's only (the script's is dropped) |
    | `script` by path | the script's own only (the module's is dropped) |
    | `flow` by path | the subflow's flow-level `cache_ttl` (the module's is dropped) |
    | containers | none (inline children carry none) |

    A module with `cache_ttl` prevents single-step loop collapse
    ([FX-46](#fx-46)).
  - **Collapsed loops.** The body module never carries `cache_ttl` itself.
    Each iteration's script job takes its TTL from the same rule, with the
    loop module standing in for "the module": `rawscript`, `flowscript` and
    hash-pinned `script` bodies use the loop module's `cache_ttl`; an
    unhashed by-path `script` body uses the script's own metadata TTL and
    ignores the loop module's. Iterations within one run do not share
    entries: each collapsed iteration's job path ends in `/loop-<index>`,
    and an unhashed job's cache key includes its path.
- **Correction (validation):** the TTL matrix and failed-flow caching are
  correct, but "iterations within one run do not share entries" is too broad.
  The `/loop-<index>` path is only a fallback for a rawscript without its
  own path, and a rawscript may supply an explicit common path; a cache key
  built from a runnable id or hash does not include the path. Two sequential
  collapsed iterations with a pinned script hash, an enabled loop TTL and
  identical transformed args should therefore share an entry. Evidence:
  `backend/windmill-worker/src/worker_flow.rs:5940-5958`;
  `backend/windmill-worker/src/common.rs:1544-1575`.
- **Open:** [Q-FX-17](#q-fx-17).
- **Evidence:**
  - `backend/windmill-worker/src/worker.rs:3753-3814`
  - `backend/windmill-worker/src/common.rs:1497-1575` (S3 hashing at
    `:1507-1541`), `:1706-1774`
  - `backend/windmill-worker/src/worker_flow.rs:1992-2006`, `:5038-5074`,
    `:5199-5246`, `:5670`, `:5908-6153` (by hash at `:6120`), `:6003`
  - `backend/windmill-common/src/jobs.rs:80-160` (by path)
  - Docs: https://www.windmill.dev/docs/flows/cache
- **Conformance:** [FX-40/a–FX-40/d](conformance.md#fx-40)

<a id="fx-41"></a>
### FX-41 · `priority`, `same_worker`, deletion, squash and scheduling settings

**Scope:** later (`same_worker` shared directory); out (priority, deletion, concurrency, debouncing, worker tags); EE (squash) · **Confidence:** high (pinning exclusions, linking for all jobs); medium (behaviour across workers and platforms) · **Status:** confirmed

- **Rule:**
  - **`priority`** (flow or module; the module wins) only orders the queue
    (EE). It has no semantic effect.
  - **`delete_after_use`** wipes the stored args and result (and the logs,
    at flow level) after the root flow completes; `delete_after_secs`
    schedules that wipe for later. Neither changes any value seen during the
    run.
  - **`squash`** on loops reuses a warm runtime across iterations. It is
    EE-only; OSS rejects it with `Squashing flow loops is not available in
    OSS`.
  - **Concurrency limits, debouncing, worker tags and `preserve_step_tags`**
    are scheduling only.
  - **`same_worker: true` pinning.** The next job is pinned to the current
    worker, **except** when the module being launched has `sleep` or
    `suspend`, or after an unrecoverable failure.
  - **Shared directory.** It is prepared for every job a worker picks up,
    flow jobs included, before execution starts:
    - the root flow job, with no parent, gets a real `shared` directory in
      its job directory;
    - a pinned job with a parent gets `shared` as a link to its immediate
      parent's `shared`; that holds for an inline child flow job too. Since
      the parent's `shared` is itself a link, **all pinned jobs of one root
      run resolve to the root's directory**: inline-body steps see top-level
      files, and the other way round;
    - an unpinned job (a module with `sleep` or `suspend`, a failure module
      after an unrecoverable failure, or a job that runs on another worker)
      gets a fresh empty directory, which breaks the chain for that job and
      its descendants;
    - on Windows, a failed link falls back to a fresh directory.

    Each step still runs in its own process.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:1858-1970`,
  `:2131-2134`, `:4093-4109`, `:4142-4148`, `:4440-4449`, `:4519-4567`;
  `backend/windmill-worker/src/worker.rs:3040-3093`;
  `backend/windmill-worker/src/dedicated_worker_oss.rs:6-25`; Docs:
  https://www.windmill.dev/docs/flows/priority,
  https://www.windmill.dev/docs/flows/lifetime
- **Conformance:** [FX-41/a–FX-41/c](conformance.md#fx-41)

<a id="fx-42"></a>
### FX-42 · Where JSON serialisation happens between steps

**Scope:** core (boundary ordering, in-memory handoff, `flow_input` shortcut); later (number rewriting for non-JavaScript consumers); resolver behaviour per the expressions and client-API areas · **Confidence:** high (map types, handoff, `$jsonvar:` ordering); medium (jsonb key order and number rewriting, which are PostgreSQL behaviour; `$res:` ordering) · **Status:** DISPUTED

- **Rule (as drafted):** Values cross step boundaries as JSON text, and most
  of it passes through PostgreSQL `jsonb`: flow and step args
  (`v2_job.args`), results (`v2_job_completed.result`), flow definitions
  including static transform values (`flow_version.value`) and flow status.
  jsonb does not keep the text: it orders object keys shorter first, then by
  bytes; keeps only the last of duplicate keys; rewrites numbers in exponent
  form as plain decimals; and rejects `\u0000` in strings and keys.
  1. **Outer argument maps have an unspecified key order.** The flow input
     map and every step's args map are held as hash maps with randomised
     iteration order and re-serialised from them. So the top-level key order
     of step 0's `previous_result`, the engine's `flow_input` object, the
     `args.json` given to a script, and the empty-flow result before storage
     is **unspecified**. Named-parameter calls cannot observe it;
     `Object.keys(flow_input)` in a transform can.
  2. **Stored nested values** in a job's args have been through jsonb
     (normalised key order, duplicates collapsed, exponents rewritten).
     **Then, after pickup and before the arguments file is written**, the
     worker resolves argument values that contain platform references
     (`$var:`, `$jsonvar:`, `$res:` and similar). Only values that contain a
     reference are re-parsed (with insertion order kept); their
     non-reference parts keep their stored jsonb order, and untouched
     arguments pass through as stored. Resolved pieces are inserted as their
     source provides them: `$jsonvar:` parses the variable's text, **so its
     key order and spelling survive** ([FX-V5](#fx-v5)); `$var:` yields a
     string; `$res:` yields the resource value as the platform returns it. No
     further jsonb normalisation is applied to resolved values.
  3. **In-memory handoff.** The language wrapper serialises a step's result
     (Bun: `JSON.stringify`, with `undefined` turned into `null`;
     [languages.md](languages.md)). That text is kept in memory as
     `stop_after_if`'s `result`, the next module's `previous_result`, and the
     candidate flow result; it is also stored as jsonb. `previous_result`,
     route-1 and route-3 `results.<prev>` ([FX-10](#fx-10)) and
     `stop_after_if`'s `result` keep the **producer's text**; route-2 paths
     and other ids come from storage, as do loop and branchall aggregates,
     preprocessor output and a re-derived `previous_result` ([FX-6](#fx-6)).
  4. **Expression engine.** JavaScript transforms and predicates (QuickJS)
     parse their inputs into JavaScript values, so integers beyond 2⁵³ lose
     precision; the output is `JSON.stringify(v ?? null)`. Fast paths skip
     the engine and pass raw JSON through untouched: an expression that is
     exactly a context name (for example `previous_result`); an exact
     `flow_input.<key>`, `flow_input["<key>"]` or `flow_env.<key>`;
     `results.<preceding id>` in its four spellings; and simple
     `results.<id>` paths, answered from storage.
  5. **The final flow result** is stored whole, so the API returns it fully
     jsonb-normalised, outer keys included.
  6. **Numbers.** JavaScript consumers see the same double either way;
     Python consumers can tell the difference. JavaScript writes `1e+21` for
     10²¹, and jsonb turns it into `1000000000000000000000`, which Python
     parses as an `int` rather than a `float`; `1.5e3` becomes `1500`.
  7. **The `flow_input` shortcut** treats the whole suffix as one key: with
     input `{"a.b": 7, "a": {"b": 9}}`, `flow_input.a.b` gives `7`, but
     `flow_input.a.b + 0` gives `9` ([expressions.md](expressions.md)).
  8. **Strings containing NUL** cannot be stored ([FX-48](#fx-48)).
- **Correction (validation):** the ordering distinctions and the
  no-second-jsonb-pass rule are correct, but raw spelling is not preserved.
  `$jsonvar:` parses the variable's text into a typed JSON value, and the
  resolved argument is serialised again, so insertion order survives while
  lexical details (number notation, escaped strings, whitespace) are not
  guaranteed. Insertion order and lexical preservation are different
  guarantees, and adding a jsonb pass would be a different normalisation,
  not a fix. Evidence: `backend/windmill-worker/src/common.rs:194-208`,
  `:293-303`; `backend/Cargo.toml:435`.
- **Open:** [Q-FX-16](#q-fx-16) (spelling); [Q-FX-7](#q-fx-7) (key order and
  number types on the oracle).
- **Evidence:**
  - Column types: `backend/migrations/20220123221903_first.up.sql:141`,
    `:161-162`; `backend/migrations/20250201124431_v2_job.up.sql:31`;
    `backend/migrations/20240630102146_flow_versioning.up.sql:8`
  - Hash maps: `backend/windmill-queue/src/jobs.rs:11`, `:2757` (job args map
    type); `backend/windmill-worker/src/worker_flow.rs:3181-3194`,
    `:3196-3205`, `:3328-3331`
  - Resolution before the arguments file: `backend/windmill-worker/src/common.rs:121-149`,
    `:174-208`, `:285-303`
  - In-memory result passing: `backend/windmill-worker/src/result_processor.rs:1488-1542`,
    `:1599-1649`; `worker_flow.rs:2086-2099`, `:2299-2327`, `:2671-2705`,
    `:3335-3336`, `:6234-6265`
  - `backend/windmill-worker/src/js_eval.rs:44-79`
  - `backend/windmill-jseval/src/lib.rs:117-163`, `:456-469`, `:555-559`,
    `:826-840`
  - The backend's JSON library keeps insertion order (`backend/Cargo.toml:435`,
    feature `preserve_order`; no `arbitrary_precision`).
  - PostgreSQL: https://www.postgresql.org/docs/current/datatype-json.html,
    https://www.postgresql.org/docs/current/datatype-numeric.html
- **Conformance:** [FX-42/a–FX-42/h](conformance.md#fx-42); also
  [FX-V2/a](conformance.md#fx-v2), [FX-V5/a](conformance.md#fx-v5).
- **Design note (not Windmill behaviour):** an earlier proposal that a
  key-sorting walk is enough for parity is withdrawn. Exact parity needs
  nested jsonb key order and duplicate collapse, number rewriting where
  non-JavaScript consumers exist, a NUL policy, and an unspecified outer-map
  order: windmill-lite must not specify a deterministic order there.

<a id="fx-43"></a>
### FX-43 · Result size limits

**Scope:** out · **Confidence:** high (source-traced) · **Status:** confirmed

- **Rule:** Size is measured as `floor(bytes / 1024 / 1024)` in whole MiB,
  using integer division twice.
  - **When it is > 2** (that is, at least 3 MiB): self-hosted Windmill logs a
    warning; Windmill Cloud rejects the result.
  - **When it is > `MAX_RESULT_SIZE_MB`** (default 500, so at least 501 MiB):
    the result is rejected with `ResultTooLarge` (`Result of job <id> is too
    large: <n>MB > MAX_RESULT_SIZE_MB=<m>MB. …`) and not retried.

  Nothing is truncated.
- **Evidence:** `backend/windmill-queue/src/jobs.rs:922`, `:1006-1012`,
  `:1474-1500`
- **Conformance:** none.

<a id="fx-44"></a>
### FX-44 · Unrecoverable failures

**Scope:** core (status-update paths); out (zombie jobs) · **Confidence:** high (source-traced) · **Status:** confirmed

- **Rule:** A failure is unrecoverable in these cases:
  - an error while processing a completion: a throwing `stop_after_if`
    ([FX-21](#fx-21)) or a throwing `retry_if` during completion
    ([FX-28](#fx-28));
  - a completion delivered by internal signal (suspend timeout or
    disapproval, [FX-39](#fx-39));
  - a zombie job that was a `same_worker` job.

  Other zombie handling is ordinary and recoverable. Unrecoverable failures
  skip retry and tolerance and go to the failure module, which is then not
  pinned to the worker ([FX-41](#fx-41)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:223-251`,
  `:1302-1327`, `:1729-1747`, `:3720-3745`, `:4093-4109`;
  `backend/windmill-worker/src/result_processor.rs:465-480`;
  `backend/src/monitor.rs:4683-4690`
- **Conformance:** none of its own; see [FX-21/a](conformance.md#fx-21) and
  [FX-28/c](conformance.md#fx-28).

<a id="fx-45"></a>
### FX-45 · Step identity in the environment

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** Each step's job carries the module id as its flow step id.
  Wrappers expose it as `WM_FLOW_STEP_ID`, and it ends up in error objects as
  `step_id` ([FX-12](#fx-12)).
  - For a failure module it is that module's id (suffixed inside inline
    bodies, [FX-34](#fx-34)).
  - For the preprocessor it is the preprocessor module's id.
  - For the virtual step it is `<id>-v` ([FX-4](#fx-4)).
  - In a collapsed loop ([FX-46](#fx-46)) each iteration job carries the
    **loop module's** id, so `WM_FLOW_STEP_ID` and `step_id` are the loop's
    id, not the body step's.

  The job's path is `<flow path>/<module id>` for inline scripts with no
  explicit `path`; preprocessors use `<flow path>/preprocessor`, and
  iterations of a collapsed loop use `<flow path>/loop-<n>`. Other `WM_*`
  variables are in [languages.md](languages.md).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:4429`,
  `:5076-5086`, `:5199`, `:5657-5697`, `:5670`;
  `backend/windmill-worker/src/bun_executor.rs:1887-1890`, `:1952-1955`
- **Conformance:** [FX-45/a](conformance.md#fx-45)

<a id="fx-47"></a>
### FX-47 · Thrown non-Error values in Bun

**Scope:** core (strings and objects); later (`null`/`undefined`) · **Confidence:** medium (plain object, string); low (`null`/`undefined`) · **Status:** confirmed

- **Rule:** The Bun wrapper builds the error from whatever was thrown
  ([FX-12](#fx-12)).
  - **A plain object** contributes `message`, `name` and `stack` only if it
    has them; its other own properties go to `extra`.
  - **A thrown string** has no `message`, `name` or `stack`. Its own
    property names are its character indices and `length`, so they land in
    `extra`: `throw "ab"` at step `a` gives `{"error": {"step_id": "a",
    "extra": {"0": "a", "1": "b", "length": 2}}}`.
  - **`throw null` or `throw undefined`** makes the wrapper's own property
    access fail before it writes the error file. The process exits without
    an error file, so the non-zero-exit shape applies (`ExecutionErr`, with
    `exit_code` and the last log lines).
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1948-1970`;
  `backend/windmill-worker/src/result_processor.rs:587-614`, `:2029-2044`
- **Conformance:** [FX-47/a, FX-47/b](conformance.md#fx-47)

<a id="fx-48"></a>
### FX-48 · NUL characters at storage boundaries

**Scope:** core · **Confidence:** high (case 1 error mapping and retry policy); medium (case 2 routing); low (case 3) · **Status:** confirmed

- **Rule:** jsonb rejects the escaped NUL character in strings and keys.
  Windmill's behaviour depends on where the NUL appears:
  1. **In a step's result** (success value or error object): the completion
     insert fails and is retried up to **10 times at 3-second intervals**.
     The job is then handled as failed with `{"error": {"message": "Internal:
     Could not add completed job <job id>: <database error> @<location>",
     "name": "InternalErr"}}`, with **no `step_id`** (assuming the
     subsequent error-record insertion succeeds). The parent flow treats this
     as an ordinary step failure: retry, tolerance and the failure module
     apply. `InternalErr` here is a different literal from the chaining
     `InternalError` of [FX-15](#fx-15).
  2. **In args generated during a start phase** (for example a transform
     returning `String.fromCharCode(0)`): pushing the child job fails. That is
     a chaining error routed by [FX-15](#fx-15): an `Anyhow` failure of the
     module at root pickup; a failed child at child-flow pickup;
     `InternalError` at a later transition. The run request itself was valid;
     nothing is refused at submission.
  3. **In the incoming flow input:** the request to start the run fails when
     the flow job is stored, so there is no flow result.
- **Evidence:**
  - `backend/windmill-queue/src/jobs.rs:995-1017` (retry policy),
    `:1094-1133` (explicit internal-error mapping at `:1127-1133`)
  - `backend/windmill-common/src/error.rs:59-62`, `:105-110`, `:234-239`
  - `backend/windmill-worker/src/result_processor.rs:700-740`, `:1910-1967`
  - `backend/windmill-worker/src/worker_flow.rs:2085-2129`, `:4404-4438`
  - PostgreSQL: https://www.postgresql.org/docs/current/datatype-json.html
- **Conformance:** [FX-48/a–FX-48/c](conformance.md#fx-48)
- **Design note (not Windmill behaviour):** windmill-lite's equivalence
  contract must say whether it reproduces cases 1–3 or deviates; the
  Windmill behaviour above is the reference.

<a id="fx-v1"></a>
### FX-V1 · A throwing `stop_after_all_iters_if` uses the EarlyStopError path

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** An exception while evaluating the aggregate stop predicate of a
  loop or branchall is caught locally, after the container's results have
  been retrieved. It requests an error-stop with an `EarlyStopError` object;
  it does **not** use the unrecoverable string-error path of
  [FX-21](#fx-21). The same conversion exists for sequential and parallel
  containers.
  - The message begins `Error evaluating stop_after_all_iters_if expression `
    and includes the expression and the formatted underlying error.
  - The throw path does not set `error_include_result`, so the error object
    has no `result` field, and the aggregate results fetched before the
    throw are discarded in favour of the error object.
  - Parallel containers also log the error.

  Retry and failure-module behaviour then follow the error-stop rules of
  [FX-19](#fx-19).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:342-407`,
  `:974-1016`, `:1203-1246`, `:1350-1367`
- **Conformance:** [FX-V1/a, FX-V1/b](conformance.md#fx-v1)

<a id="fx-v2"></a>
### FX-V2 · Stored child-job result and effective module outcome can differ

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** A successful script result is stored before flow-level stop
  predicates run. A later `stop_after_if` error-stop, or a throwing stop
  predicate, changes the module and flow outcome without rewriting that
  script job's stored result. So a module may be `Failure` while its job is
  successful, and a failure module can carry an error in `previous_result`
  while a storage-based lookup of that step (route 2 of [FX-10](#fx-10))
  still returns the script's original result. The oracle must capture job
  outcome and module status separately.
- **Evidence:** `backend/windmill-worker/src/result_processor.rs:1488-1542`;
  `backend/windmill-worker/src/worker_flow.rs:664-746`, `:1272-1367`,
  `:3805-3813`; `backend/windmill-worker/src/js_eval.rs:54-79`;
  `backend/windmill-jseval/src/lib.rs:180-218`
- **Conformance:** [FX-V2/a](conformance.md#fx-v2)

<a id="fx-v3"></a>
### FX-V3 · Synchronous HTTP completion treats a stored `skipped` status as failure

**Scope:** core (oracle interpretation); later (compatible synchronous HTTP adapter) · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** The synchronous run-and-wait query computes success from stored
  status `success` only. A completed `skipped` flow therefore reaches the
  result-response function with success false, and, without response
  metadata, the response is HTTP **422**, even though the executor stopped
  cleanly with status `skipped`. The early-return step lookup
  ([FX-37](#fx-37)) accepts both `success` and `skipped`. This is an adapter
  and result-capture distinction; it does not make the executor's clean stop
  a failure.
- **Evidence:** `backend/windmill-api-jobs/src/execution.rs:323-347`,
  `:374-412`, `:499-517`; `backend/windmill-queue/src/jobs.rs:4503-4510`;
  `backend/windmill-worker/src/worker_flow.rs:2006-2033`
- **Conformance:** [FX-V3/a](conformance.md#fx-v3)

<a id="fx-v4"></a>
### FX-V4 · A tolerated error-stop at the final module enters the failure module

**Scope:** core · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** When an error-stop occurs with `continue_on_error: true` and no
  retry due, the cursor advances. If another regular module exists, the flow
  continues to it. If the stopping module was the final regular module, the
  advanced cursor equals the module count and selects the failure module
  instead: the error-stop continue rule still requests continuation because a
  failure module exists, and the last-step success adjustment does not
  suppress that ([FX-2](#fx-2)). The failure module's outcome and `recover`
  then decide the final result as usual ([FX-33](#fx-33)).

  In a failure module entered this way, the preceding id is empty: `results.a`
  (for stopped module `a`) reads `a`'s stored job result, the script's
  success value, while `previous_result` and `error` give the
  `EarlyStopError`. The failure module's flow-status entry records its own id
  as `parent_module`.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:1306-1346`,
  `:1369-1385`, `:1724-1764`, `:3165-3181`, `:3655-3664`, `:4725-4745`;
  `backend/windmill-types/src/flows.rs:312-327`, `:381-389`
- **Conformance:** [FX-V4/a](conformance.md#fx-v4); also
  [FX-19/e, FX-19/f, FX-19/i](conformance.md#fx-19).

<a id="fx-v5"></a>
### FX-V5 · Argument resolution can introduce objects after the jsonb boundary

**Scope:** core (boundary ordering, when references are supported); resolver per the expressions and client-API areas · **Confidence:** high (source-traced; oracle pending) · **Status:** confirmed

- **Rule:** Queued args pass through jsonb before execution, but worker-side
  variable, resource and decryption resolution can replace values after that
  round trip, and objects it introduces are not normalised by jsonb before
  the wrapper receives them. In particular `$jsonvar:` reads a variable's
  text and parses it as JSON in the worker, and its object insertion order can
  survive into the arguments file. Only argument values that contain a
  reference are re-parsed and resolved; untouched arguments keep their stored
  raw value. Keep the stored args, the resolved args and language hydration
  as distinct stages, and do not apply an extra jsonb normalisation to
  resolved arguments.
- **Evidence:** `backend/windmill-worker/src/common.rs:121-208`, `:285-303`;
  `backend/Cargo.toml:435` (insertion-order JSON maps);
  `backend/windmill-worker/src/bun_executor.rs:1913` (args-file parse)
- **Conformance:** [FX-V5/a](conformance.md#fx-v5)
- **Note:** this preserves insertion order only; it does not promise that
  numeric or string spelling is preserved, which is disputed under
  [FX-42](#fx-42) ([Q-FX-16](#q-fx-16)). Coordinate the variable fixture with
  [expressions.md](expressions.md), [languages.md](languages.md) and
  [client-api.md](client-api.md).

## Open questions

All oracle recordings below are runs of the [oracle](oracle.md) harness
against Windmill CE `v1.767.0`.

<a id="q-fx-1"></a>
- **Q-FX-1** · affects [FX-24](#fx-24) · Is a `timeout` on a non-collapsed
  container (sequential or parallel loop, branch, `flow` module) ever
  enforced? The source shows no consumer of a child flow job's timeout.
  *Settled by:* oracle runs of [FX-24/d and FX-24/e](conformance.md#fx-24), a
  branch with `timeout: 1` around a 3 s body, and a `flow` module with
  `timeout: 1` around a 3 s subflow; the source predicts no timeout in each.

<a id="q-fx-2"></a>
- **Q-FX-2** · affects [FX-24](#fx-24) · `timeout: 0` on process and
  in-process executors counts as job-specific. Does the step fail at once,
  or can an instant step finish first? *Settled by:* an oracle run of a Bun
  step `return 1` with `timeout: 0`; do not assume a deterministic ordering
  for the race.

<a id="q-fx-3"></a>
- **Q-FX-3** · affects [FX-19](#fx-19), [FX-30](#fx-30), [FX-31](#fx-31),
  [FX-V4](#fx-v4) · The error-stop, retry, tolerance and failure-module
  matrix is fully traced in source but not recorded. *Settled by:* oracle
  runs of [FX-19/d–FX-19/i](conformance.md#fx-19) and
  [FX-V4/a](conformance.md#fx-v4).

<a id="q-fx-4"></a>
- **Q-FX-4** · affects [FX-15](#fx-15), [FX-24](#fx-24) · Pickup-path
  errors: confirm the `Anyhow` name and message prefix, the failure-module
  run for a root flow, and the bypassed failure module for a child flow.
  *Settled by:* oracle runs of [FX-15/b, FX-15/c](conformance.md#fx-15) and
  [FX-24/b](conformance.md#fx-24).

<a id="q-fx-5"></a>
- **Q-FX-5** · affects [FX-46](#fx-46), [FX-40](#fx-40) · Collapsed loops:
  confirm the deployed versus preview divergence for a body with
  `continue_on_error`, and the per-payload TTL source. *Settled by:*
  deploying and previewing the same flow on the oracle
  ([FX-46/a](conformance.md#fx-46), [FX-40/c](conformance.md#fx-40),
  [FX-24/d](conformance.md#fx-24)).

<a id="q-fx-6"></a>
- **Q-FX-6** · affects [FX-40](#fx-40) · Is a non-recovered failure-module
  result cached and then served as a success? *Settled by:* two oracle runs
  within the TTL ([FX-40/b](conformance.md#fx-40)).

<a id="q-fx-7"></a>
- **Q-FX-7** · affects [FX-42](#fx-42), [FX-V5](#fx-v5) · jsonb
  normalisation: the nested key order a Bun step sees; `$jsonvar:` order
  preservation; Python number types after exponent rewriting (`1e21` becoming
  an `int`); how PostgreSQL's numeric rewriting handles scale (for example
  `1.50e3`). *Settled by:* oracle runs of [FX-42/a–FX-42/g](conformance.md#fx-42)
  and [FX-V5/a](conformance.md#fx-v5).

<a id="q-fx-8"></a>
- **Q-FX-8** · affects [FX-48](#fx-48) · NUL: the exact database detail and
  the runtime of the 10 × 3 s insert retries (case 1); the push-failure
  message (case 2); the HTTP status when the incoming input contains NUL
  (case 3). *Settled by:* oracle runs of
  [FX-48/a–FX-48/c](conformance.md#fx-48) plus an incoming-input probe.

<a id="q-fx-9"></a>
- **Q-FX-9** · affects [FX-47](#fx-47) · Does a thrown `null` or `undefined`
  in Bun give the exit-code shape, and what program name appears in the
  message? *Settled by:* an oracle run of [FX-47/b](conformance.md#fx-47).

<a id="q-fx-10"></a>
- **Q-FX-10** · affects [FX-41](#fx-41) · Shared directory: does the nested
  link chain give `"x"`, and does an unpinned step (`sleep`) break the chain?
  *Settled by:* oracle runs of [FX-41/a–FX-41/c](conformance.md#fx-41) on a
  Linux worker.

<a id="q-fx-11"></a>
- **Q-FX-11** · affects [FX-49](#fx-49), [FX-22](#fx-22) · What happens when
  a cursor-0 `skip_expr` re-evaluation flips to true, or throws, during a
  sequential loop at module 0? As drafted, the fixture was predicted to
  evaluate the expression three times with the outcome open; with `v` bound
  to `flow_input.iter.value`, the source predicts two evaluations, loop result
  `[1]` and a successful completion (trace in [FX-49](#fx-49)). Evaluating
  `skip_expr` once is a proposed windmill-lite deviation, not the Windmill
  rule. *Settled by:* an oracle run of [FX-49/a](conformance.md#fx-49) plus a
  throwing variant.

<a id="q-fx-12"></a>
- **Q-FX-12** · affects [FX-36](#fx-36) · Does the typed response-metadata
  decoder accept a JSON array positionally (for example `[201, null, null,
  null, 7]`)? *Settled by:* checking the decoder's behaviour for arrays, or an
  oracle run-and-wait with such a result. Later scope. Status: open
  (needs-evidence finding).

<a id="q-fx-13"></a>
- **Q-FX-13** · affects [FX-15](#fx-15), [FX-21](#fx-21), [FX-28](#fx-28),
  [FX-V1](#fx-v1), [FX-48](#fx-48) · Exact engine, database and runtime
  error suffixes. The structural rules do not depend on them. *Settled by:*
  capturing them on the oracle only where windmill-lite wants to match more
  than `name` plus the documented prefix.

<a id="q-fx-14"></a>
- **Q-FX-14** · affects [FX-29](#fx-29), [FX-17](#fx-17), [FX-32](#fx-32) ·
  The carried-over retry counter and the retry-skip and failure-module
  retry-skip fixtures are fully traced in source but not recorded.
  *Settled by:* oracle runs of [FX-29/a, FX-29/b](conformance.md#fx-29),
  [FX-17/b](conformance.md#fx-17) and [FX-32/b](conformance.md#fx-32).

<a id="q-fx-15"></a>
- **Q-FX-15** · affects [FX-10](#fx-10), [FX-V6](#fx-v6) · **Core blocker.**
  When does an engine `results` lookup of the preceding id read memory
  rather than storage? As drafted, only the four literal preceding-id
  spellings bind `previous_result`, and computed indexing always reads
  storage. The competing source reading: `previous_result` is also bound
  when its name occurs anywhere in the expression or when the expression
  contains `error` (`backend/windmill-jseval/src/lib.rs:253-279`), and the
  proxy uses memory whenever the requested id equals the preceding id and
  that variable is defined (`:804-815`). *Settled by:* the paired fixture
  [FX-V6/a](conformance.md#fx-v6) (`results[flow_input.target]` versus
  `previous_result && results[flow_input.target]` in an ordinarily entered
  failure module after an error-stop; the source reading predicts stored `3`
  versus the carried `EarlyStopError`), cross-checked with
  [expressions.md](expressions.md) and confirmed on the oracle. Status: open
  (disputed finding).

<a id="q-fx-16"></a>
- **Q-FX-16** · affects [FX-42](#fx-42), [FX-V5](#fx-v5) · Does `$jsonvar:`
  preserve only object insertion order, or also raw JSON spelling? As
  drafted, its "key order and spelling survive"; the competing reading is
  that the text is parsed into a typed value and the resolved argument is
  serialised again (`backend/windmill-worker/src/common.rs:194-208`,
  `:293-303`; `backend/Cargo.toml:435`), so number notation, escaped strings
  and whitespace are not preserved. *Settled by:* resolver text containing
  `1e3`, `1e21` and an integer beyond the exact integer range, inspecting the
  resolved arguments file or a non-JavaScript consumer
  ([FX-42/h](conformance.md#fx-42)); confirm numeric details with
  [expressions.md](expressions.md) and [languages.md](languages.md). Do not
  add a jsonb pass to compensate. Core boundary wording; the non-JavaScript
  numeric cases are later.

<a id="q-fx-17"></a>
- **Q-FX-17** · affects [FX-40](#fx-40), [FX-46](#fx-46) · Can collapsed
  iterations of one run share cache entries? As drafted they never do; the
  competing reading is that the `/loop-<index>` path is only a fallback for a
  rawscript without its own path, and a runnable-id or hash cache key does
  not include the path (`backend/windmill-worker/src/worker_flow.rs:5940-5958`;
  `backend/windmill-worker/src/common.rs:1544-1575`). *Settled by:*
  [FX-40/d](conformance.md#fx-40): two sequential collapsed iterations with a
  pinned script hash, an enabled loop TTL and identical transformed args,
  contrasted with a rawscript with no explicit path, inspecting cache hits
  and execution counts rather than random-number inequality. Later scope.

<a id="q-fx-18"></a>
- **Q-FX-18** · affects [FX-1](#fx-1), [FX-3](#fx-3), [FX-4](#fx-4),
  [FX-5](#fx-5), [FX-6](#fx-6), [FX-7](#fx-7), [FX-8](#fx-8), [FX-9](#fx-9),
  [FX-10](#fx-10), [FX-11](#fx-11), [FX-12](#fx-12), [FX-13](#fx-13), [FX-14](#fx-14),
  [FX-15](#fx-15), [FX-16](#fx-16), [FX-17](#fx-17), [FX-18](#fx-18),
  [FX-19](#fx-19), [FX-20](#fx-20), [FX-21](#fx-21), [FX-22](#fx-22),
  [FX-23](#fx-23), [FX-24](#fx-24), [FX-25](#fx-25), [FX-26](#fx-26),
  [FX-27](#fx-27), [FX-28](#fx-28), [FX-30](#fx-30), [FX-31](#fx-31),
  [FX-32](#fx-32), [FX-33](#fx-33), [FX-34](#fx-34), [FX-35](#fx-35),
  [FX-36](#fx-36), [FX-37](#fx-37), [FX-38](#fx-38), [FX-40](#fx-40),
  [FX-45](#fx-45), [FX-47](#fx-47), [FX-V1](#fx-v1), [FX-V2](#fx-v2), [FX-V3](#fx-v3) · The
  remaining conformance cases of these findings are source-derived and have
  not been recorded on the oracle: every case not named in Q-FX-1 to
  Q-FX-17. This includes the by-path first-module `skip_expr` quirk of
  [FX-22](#fx-22), the engine text for a bare `result` in a transform
  ([FX-7/d](conformance.md#fx-7)), and the virtual step on deployed flows
  ([FX-4/a](conformance.md#fx-4)). *Settled by:* recording each case on the
  oracle and updating its provenance.

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

Recording batches E3b, E3c, E3d and E3e (design record 0012) recorded many
of this area's cases on the oracle; those cases are tagged `oracle-observed` and
name their recording in [conformance.md](conformance.md#execution). The other
cases are source-derived, and the open questions say which recording settles
each one ([Q-FX-18](#q-fx-18) covers the cases no other question names).

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
   ([FX-13](#fx-13)). Messages of the `Anyhow` kind gain an `Error: ` prefix
   and a Rust location suffix per conversion, so a predicate error at a
   pickup carries two of each; typed errors such as the sleep messages carry
   none ([FX-50](#fx-50)). A failure module's copy inside a deployed flow's
   body keeps the id `failure` ([FX-34](#fx-34)).
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
   are not. In-memory handoffs keep the producer's text. `$jsonvar:` output
   keeps the variable's key order but not its spelling: numbers and strings
   are written out again (recorded, [Q-FX-16](#q-fx-16)). Which engine
   expressions read the preceding result from memory rather than storage is
   [FX-V6](#fx-v6)'s rule ([Q-FX-15](#q-fx-15)). Besides NUL, unpaired
   surrogate escapes and out-of-range numbers fail at the jsonb boundary
   ([FX-51](#fx-51)).
6. **Timeouts by execution path ([FX-24](#fx-24), [FX-25](#fx-25)).** Process
   and in-process executors are timed, with different messages. Identity
   jobs and child flow jobs are untimed. In a collapsed loop the loop
   module's timeout applies to each iteration.
7. **Loop collapse changes semantics ([FX-46](#fx-46)).** It changes which
   `timeout`, `continue_on_error`, `step_id` and cache TTL apply, and previews
   never collapse for-loops. An explicit equivalence decision is needed.
   On a deployed flow, collapsed iterations of one run with identical
   arguments share cache entries, because every body job has a runnable id
   that replaces the path in the key (recorded, [FX-40](#fx-40),
   [Q-FX-17](#q-fx-17)).
8. **Retry arithmetic** is unsigned 16-bit with wrap, saturate and cap steps;
   jitter applies to the exponential phase only ([FX-26](#fx-26)). Retries
   reuse the failed attempt's args ([FX-27](#fx-27)), and the retry counter
   carries over after a tolerated failure ([FX-29](#fx-29)).
9. **Cursor-0 checks run again** on every start phase at cursor 0, so a
   non-deterministic `skip_expr` can change its answer mid-run: a true
   answer between two iterations of a sequential loop at module 0 ends the
   loop with the results so far and the flow completes normally; a throw
   there is a chaining error (recorded, [FX-49](#fx-49)). Evaluating
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

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** A flow whose `modules` is empty, and that has no preprocessor to
  run, completes successfully with the flow input object as its result. The
  result's outer key order before storage is unspecified ([FX-42](#fx-42));
  the stored final result is jsonb-normalised. An empty inline body is not
  run as a child job: the container records it as an empty result
  ([Terms](#terms), [control-flow.md](control-flow.md)).

  **Its flow status is not empty.** The completion is recorded like the
  completion of a module at cursor 0, and with no regular modules cursor 0
  is the failure module's position. So the stored status ends with `step: 1`
  and one `modules` entry: `{"id": "failure", "type": "Success", "job":
  <nil UUID>, "skipped": false}`, holding the failure module's id even when
  the flow has no failure module. The `failure_module` entry stays
  `WaitingForPriorSteps`. The flow does not count as having run a failure
  module (a zero-length flow is never treated as being at its failure step),
  so it succeeds.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3196-3222`;
  completion at `worker_flow.rs:595-597`, `:1395-1397`; the cursor-0 step
  maps to the failure position when there are no modules
  (`backend/windmill-types/src/flows.rs:381-389`; used at
  `worker_flow.rs:554-564`), the success entry takes that status's id and a
  nil job (`worker_flow.rs:1274-1300`; the completion message carries the nil
  job, `backend/windmill-worker/src/result_processor.rs:463-476`), and it is
  written at index `step` of `modules` (`worker_flow.rs:1444-1458`).
- **Conformance:** [FX-3/a](conformance.md#fx-3)
- **Revision 2026-10-28 (oracle, batch E3c):** the rule held as written
  (fixture `conformance/flows/execution/fx-36`, run `empty`, oracle run
  40685766017). Added the stored status of an empty flow, which the finding
  did not state: one `Success` entry with id `failure` and a nil job.

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
    - a copy of the parent's failure module. A deployed flow stores each
      such body under its `modules` list as a flow node whose copy keeps the
      module's own id (normally `failure`); a body built at run time from
      raw modules (a raw or preview submission, a flow without its stored
      lite form, or a container in the failure-module or preprocessor slot,
      which deploy does not reduce) gets a numeric suffix: `-<parent cursor>`,
      `-<parent cursor>-<iteration>` or `-<parent cursor>-<branch index>`
      ([FX-34](#fx-34), [CF-V4](control-flow.md#cf-v4));
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
    from an inline body; a stored node is returned at `:5052-5054`, before
    the suffix at `:5058-5061`), `:5657-5745` (collapse), `:6020-6049` (by-path flow,
    latest deployed version; preprocessing disabled at `:6034-6040`)
  - `backend/windmill-worker/src/worker_lockfiles.rs:1553-1591` (at deploy,
    bodies become flow nodes holding the unsuffixed failure-module copy)
  - `worker_flow.rs:498-500` (reset for `flow` modules), `:599-618`,
    `:670-677`, `:1085-1091` (parallel containers), `:1195-1201`, `:2136-2156`
    (result, success and stop propagation)
- **Conformance:** [FX-5/a](conformance.md#fx-5); also
  [FX-20](conformance.md#fx-20), [FX-33](conformance.md#fx-33),
  [FX-34](conformance.md#fx-34), [FX-46](conformance.md#fx-46).
- **Revision 2026-10-28 (oracle, batch E3c):** the failure-module bullet said
  every inline body's copy gets a numeric suffix. On a deployed flow the copy
  keeps its own id, as [CF-V4](control-flow.md#cf-v4) already stated; the
  suffix belongs to bodies built from raw modules only. Recorded by fixture
  `conformance/flows/execution/fx-34`, run `step_id`, oracle run
  40689442792.

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
    `mock`. Presence decides, not value: `retry: {}` or `cache_ttl: 0`
    prevents collapsing, a field written as `null` counts as absent, and
    `mock` counts only with `enabled: true`
    ([CF-8](control-flow.md#cf-8));
  - the enclosing flow has no failure module.

  "Non-preview" refers to the job executing the loop: an inline container
  body that becomes a child flow, including such a body of a
  container-valued failure or preprocessor module, runs as a preview child,
  so a for-loop nested there never collapses. A preprocessor that is itself
  an eligible loop is judged in the root job and can collapse
  ([CF-8](control-flow.md#cf-8), [CF-30](control-flow.md#cf-30)).

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
- **Revision 2026-10-28 (source re-check):** the presence rule for the
  disqualifying fields and the reach of "non-preview" (inline bodies,
  including those of the failure and preprocessor modules) are spelled
  out; before, the finding listed the fields without saying how they are
  tested. The collapse rule itself is unchanged; collapsed by-path loops
  are recorded on the oracle in batch E3d ([CF-8](control-flow.md#cf-8)).
- **Revision 2026-10-28 (validation round 1):** the "non-preview" paragraph
  said that any body of a container-valued failure or preprocessor module
  runs as a preview child; it now says so only of bodies that become child
  flows, and that a preprocessor which is itself an eligible loop can
  collapse ([CF-30](control-flow.md#cf-30)).

<a id="fx-49"></a>
### FX-49 · Cursor-0 checks run on every start phase at cursor 0

**Scope:** later · **Confidence:** high (oracle-observed: a mid-run true and a mid-run throw in a sequential loop at module 0); medium (the retry and failure-module re-entries, traced) · **Status:** confirmed

- **Rule:** The cursor-0 checks (the flow-level `skip_expr` and, for
  scheduled root flows with `no_flow_overlap`, the overlap check) run on
  every start phase at cursor 0, not once per flow. Besides the first start
  they run when:
  - module 0 is re-entered for a retry;
  - module 0 failed and the flow switches to the failure module (the check
    runs before the switch);
  - each iteration or branch of a sequential container that is module 0 is
    launched, after the first.

  A pure, deterministic `skip_expr` that was false at the start stays false,
  so nothing is observable. A non-deterministic one (reading variables, time
  or `WM_SCHEDULED_FOR`) can change its answer mid-run:
  - **A mid-run true** is a stop signal arriving at module 0. In a
    sequential loop at module 0 it only breaks the loop, which completes
    with the results of the iterations already run; the flow then goes on
    as after any completed module and, when the loop is its last module,
    completes **successfully** with that array. It is not the `skipped`
    outcome of a true `skip_expr` at the first start ([FX-22](#fx-22)).
    With a variable flipped by the first iteration, the expression is
    evaluated twice (at the start and at the second iteration's launch) and
    the loop result is `[1]`.
  - **A mid-run throw** is a chaining error at that later transition
    ([FX-15](#fx-15), context 1): the flow fails at once with
    `{"error": {"name": "InternalError", "message": "Error: <evaluator text><loc>"}}`
    ([FX-50](#fx-50)), and the loop's status entry is left in progress with
    the results so far.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3224-3322` (the
  gate sits before the retry and failure-module switch at `:3720-3832`; a
  true `skip_expr` completes the update with a stop-early override at
  `:3310-3319`), `:670-677` (the override taken as a stop),
  `:1134-1191`, `:1195-1201` (inside a sequential loop the stop only breaks
  the loop), `:1392-1397`, `:1478-1489`, `:2958-2980`;
  `backend/windmill-worker/src/result_processor.rs:450-480`.
- **Conformance:** [FX-49/a, FX-49/b](conformance.md#fx-49)
- **Design note (not Windmill behaviour):** windmill-lite could evaluate
  `skip_expr` once per run. That would be a deviation from Windmill and must
  be documented as one, unless the oracle shows the re-evaluation matters for
  supported flows.
- **Revision 2026-10-29 (oracle, batch E3e):** the finding was `DISPUTED`
  ([Q-FX-11](#q-fx-11)). As drafted, FX-49/a's fixture evaluated the
  expression three times, once per start phase, with the outcome open; the
  validation's correction predicted two evaluations, loop result `[1]` and
  an ordinary successful completion. Runs `flip` (a one-step body) and
  `flip_body` (a two-step body, so each iteration is a child flow) of
  [`execution/fx-49`](../../conformance/flows/execution/fx-49) (oracle run
  40794872025) record the correction: the flow succeeds with `[1]` after one
  iteration. Run `throws` records the throwing variant: the flow fails with
  the chaining `InternalError` and the loop is left in progress with `[1]`
  (FX-49/b). The rule above folds the correction in.

## B. Data available to steps and expressions

<a id="fx-6"></a>
### FX-6 · What `previous_result` is

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

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
- **Revision 2026-10-28 (oracle, batch E3b):** recorded as stated. Beyond
  FX-6/a and FX-6/b, run `rederived_on_retry` of
  [`execution/fx-6`](../../conformance/flows/execution/fx-6) shows the
  re-derived JSON string `"{}"`: a step after a tolerated failure sees the
  wrapped error on its first attempt, and on its retry its `skip_if` sees
  `"{}"` (truthy), so it is skipped and returns `{}` (oracle run
  40685013818).

<a id="fx-7"></a>
### FX-7 · Step input transforms

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

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
  fails with the pre-run error of [FX-14](#fx-14). The engine throws a
  `ReferenceError`, but the error text never names the class: it reads
  `QuickJS evaluation error: Error: result is not defined`, followed by a
  newline and the engine's stack. Every engine exception is rendered with
  the fixed word `Error:` and its message, whatever its class
  ([JE-21](expressions.md#je-21)).

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
    are installed), `:555-559` (`JSON.stringify(x ?? null)`), `:387-421`
    (the `QuickJS evaluation error: ` prefix)
  - rquickjs-core `0.11.0` (pinned in `backend/Cargo.lock:10304-10307`):
    `src/value/exception.rs:217-230` (an exception displays as the word
    `Error:`, its `message`, then a newline and its `stack`; `name` is not
    read)
- **Conformance:** [FX-7/a–FX-7/d](conformance.md#fx-7)
- **Note:** after the args are stored, the worker resolves platform
  references in them before the script runs; see [FX-V5](#fx-v5).
- **Revision 2026-10-28 (oracle, batch E3b):** the finding said the pre-run
  error carries an engine `ReferenceError` and left the engine text open.
  Run `result_unbound` of
  [`execution/fx-7`](../../conformance/flows/execution/fx-7) (oracle run
  40686049913) records the message
  ``Error during isolated evaluation of expression `result`:\nQuickJS evaluation error: Error: result is not defined\n    at <anonymous> (eval_script:1:1)\n    at <eval> (eval_script:1:35)\n``,
  name `ExecutionErr`, empty args and no `step_id`. The class name is absent
  because the embedding's display of a caught exception writes a fixed
  `Error:` (evidence above), which [JE-21](expressions.md#je-21) already
  states. The rest of the finding is recorded as stated (FX-7/a–c).

<a id="fx-8"></a>
### FX-8 · Scope of each flow-level expression

**Scope:** core · **Confidence:** high (oracle-observed for the `stop_after_if` and `sleep` rows; source-traced for the others) · **Status:** confirmed

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
  - `worker_flow.rs:3684-3712` (a throwing `sleep` expression becomes the
    `ExecutionErr` `Expected a number value, had error instead: <debug form
    of the evaluation error>`)
- **Conformance:** [FX-8/a, FX-8/b](conformance.md#fx-8)
- **Revision 2026-10-28 (oracle, batch E3b):** both cases recorded as
  stated ([`execution/fx-8`](../../conformance/flows/execution/fx-8), oracle
  runs 40685013818 and 40686049913). FX-8/b's case now holds the exact text:
  the failure is an `InternalError` whose message is
  `Expected a number value, had error instead: ExecutionErr("…")`, the debug
  form of the evaluation error that says `results is not defined`, with no
  `Error: ` prefix and no location suffix. That is [FX-15](#fx-15)'s
  later-transition context carrying a typed error rather than an `Anyhow`
  one.

<a id="fx-9"></a>
### FX-9 · Boolean predicates use JavaScript truthiness

**Scope:** core · **Confidence:** high (oracle-observed for `skip_if`) · **Status:** confirmed

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
- **Revision 2026-10-28 (oracle, batch E3b):** recorded as stated for
  `skip_if` with every value the rule lists: `'false'`, `[]` and `({})`
  skip; `0`, `''`, `null`, `undefined`, `NaN` and `false` run (run `values`
  of [`execution/fx-9`](../../conformance/flows/execution/fx-9), oracle run
  40685013818). The other predicate sites share the wrapper but were not
  recorded here.

<a id="fx-10"></a>
### FX-10 · `results.<id>` resolution routes

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** An expression that mentions `results` is answered by the first
  route that applies. The text is trimmed first.
  1. **Exact preceding id.** The whole expression is exactly `results.<p>`,
     `results?.<p>`, `results["<p>"]` or `results?.["<p>"]`, where `p` is the
     preceding module's id. It returns the in-memory `previous_result`.
  2. **Simple path, answered from storage.** The **first line** of the
     expression that has, from its start to its end, the form
     `results.<id>` or `results?.<id>`, optionally followed by one
     `[<digits>]`, optionally followed by `.<key>` segments (identifier
     characters only), decides ([JE-9](expressions.md#je-9)). If that line's
     last `.<key>` is `length`, the whole shortcut declines and the text goes
     to route 3; later lines are not searched. A step whose id is `length`
     is looked up like any other. Otherwise that line is answered by a
     storage query for that step's stored result, and the rest of the text
     is ignored, without running JavaScript. This holds **also when `<id>` is
     the preceding step**: `results.<p>.x` reads the stored job result of
     `p`, which can differ from `previous_result` ([FX-V2](#fx-v2)). A
     missing step or path gives `null`. So `results.a`, a newline, then
     `+ 100` gives `a`'s stored value, while the single-line
     `results.a + 100` goes to the engine ([CF-V6](control-flow.md#cf-v6)).
  3. **Anything else goes to the engine.** `results` is a lazy proxy: other
     ids fetch from storage; loops and branchall return arrays of child
     results; a step with no job yet, or an unknown id, gives `null` (no
     error is raised). The preceding id returns the **in-memory**
     `previous_result` whenever that variable is bound in the engine, and
     otherwise falls back to storage. It is bound when the expression text
     contains the name `previous_result`, one of the four preceding-id
     spellings of route 1, or the substring `error` anywhere (these are
     plain substring tests on the text). So computed indexing such as
     `results[flow_input.t]` with `t` equal to the preceding id reads storage
     by itself, and memory once the text also names `previous_result` or
     contains `error` ([FX-V6](#fx-v6)).

  Inside `stop_after_if` the preceding id is empty (a results context exists
  only when the text mentions `results`), so an ordinary id reads storage
  there; but the empty-id spelling `results[""]` binds `previous_result`,
  which in that predicate is the step's own result, and the proxy returns
  it from memory ([JE-8](expressions.md#je-8)). In an ordinarily entered failure module, the preceding id is
  the failed module's id ([FX-32](#fx-32)); in one entered at cursor `len`
  it is empty ([FX-V4](#fx-v4)).
- **Evidence:**
  - `backend/windmill-worker/src/js_eval.rs:36-79` (trim, routes 1 and 2)
  - `backend/windmill-jseval/src/lib.rs:65-76` (route 2's pattern is
    multi-line: it matches whole lines), `:165-218` (the first match is
    inspected and a final `length` declines at `:184-197`; the decline reads
    the path after the step id, `:170-178`), `:256-279` (the binding
    triggers), `:670-823` (the proxy; memory for the preceding id at
    `:804-815`)
  - `backend/windmill-worker/src/worker_flow.rs:324-340` (a stop predicate's
    results context has the empty preceding id), `:2427-2451` (its
    `previous_result` is the step's own result), `:3805-3813`, `:6155-6172`
- **Conformance:** [FX-10/a–FX-10/c](conformance.md#fx-10),
  [FX-V6/a](conformance.md#fx-v6) (route 3's binding),
  [CF-V6/a, CF-V6/b](conformance.md#cf-v6) (route 2 is line-anchored),
  [JE-9/l](conformance.md#je-9) (the first match declines on `length`).
- **Note:** coordinate with [expressions.md](expressions.md) (JE-8 to JE-10,
  JE-18), which states the same routes from the evaluator's side.
- **Revision 2026-10-28 (oracle, batches E3a, E3b and E3c):** the finding was
  `DISPUTED` on two points, and the oracle settled both for the correction
  that the reference's default already followed.
  - Route 3 ([Q-FX-15](#q-fx-15)): the drafted amendment said only the four
    literal spellings bind the in-memory value and computed indexing always
    reads storage. The oracle shows the binding triggers of
    [FX-V6](#fx-v6): in a failure module after an error-stop,
    `results[flow_input.target]` read the stored `3` and
    `previous_result && results[flow_input.target]` the in-memory
    `EarlyStopError` (fixture `conformance/flows/execution/fx-v6`, run
    `lookup`, oracle run 40685766017); without a failure module, computed
    reads whose text names `previous_result` or contains `error` read the
    producer's key order from memory and a bare computed read jsonb's order
    from storage (fixture `conformance/flows/execution/fx-10`, run
    `memory_or_storage`, oracle run 40685013818).
  - Route 2 ([Q-CF-16](control-flow.md#q-cf-16)): it was drafted as a
    whole-expression match. The oracle shows the line-anchored reading:
    `results.a`, a newline, `+ 100` gave `7` and the single line gave `107`
    (fixture `conformance/flows/cf/cf-19`, oracle run 40687124625).

  The superseded readings are kept only in this note.
- **Revision 2026-10-28 (validation round 1):** route 2 said the first line
  that matches and does not end in `.length` answers; the source inspects
  only the first syntactic match and declines the whole shortcut when its
  last segment is `length` (as JE-9 states; recorded by run `je9-l` of
  `conformance/flows/je/shortcuts`, oracle run 40687124625, where
  `results.a.length`, a newline, `results.a` went to QuickJS and failed).
  The note on `stop_after_if` said memory was never used there; the
  empty-id spelling `results[""]` reads the step's own result from memory
  (source-traced, case FX-10/c).
- **Revision 2026-10-29 (oracle, batch E3e):** the empty-id read in
  `stop_after_if`, the one part still `drafted`, was recorded as stated:
  `results[""].k === 1` stops the flow cleanly with `a`'s `{"k": 1}`, and `b`
  never runs (fixture
  [`execution/fx-10-c`](../../conformance/flows/execution/fx-10-c), run
  `own`, oracle run 40794872025). The status is now `confirmed` throughout.

<a id="fx-v6"></a>
### FX-V6 · Dynamic preceding-result lookup depends on all context-binding triggers

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

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
- **Conformance:** [FX-V6/a](conformance.md#fx-v6); the handler-free
  discriminator is run `memory_or_storage` of
  [`execution/fx-10`](../../conformance/flows/execution/fx-10).
- **Note:** [FX-10](#fx-10) now states this rule as its route 3.
  Coordinate with [expressions.md](expressions.md).
- **Revision 2026-10-28 (oracle, batches E3b and E3c):** this was the
  competing claim (`open`) of the FX-10 dispute, [Q-FX-15](#q-fx-15). The
  oracle chose it in both halves: in a failure module (FX-V6/a, fixture
  `conformance/flows/execution/fx-v6`, oracle run 40685766017) and without
  one (fixture `conformance/flows/execution/fx-10`, run
  `memory_or_storage`, oracle run 40685013818). The rule is unchanged; its
  status is now `confirmed`.

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

**Scope:** core (Bun, flow-level); later (Deno, Python) · **Confidence:** high (oracle-observed for the Bun and Deno wrappers and the unwrapped flow result; source-traced for the rest) · **Status:** confirmed

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
- **Revision 2026-10-28 (oracle, batch E3c):** the Bun shapes (an Error with
  an own property; a thrown plain object without `name` and `stack`), the
  Deno shape without `extra`, and the unwrapped result of a failed flow whose
  failure module ran were recorded as stated (fixtures
  `conformance/flows/execution/fx-12` and `fx-12-deno`, oracle run
  40685766017). Pickup-failure texts are in [FX-15](#fx-15) and
  [FX-50](#fx-50). Nothing in the rule changed.
- **Revision 2026-10-29 (oracle, batch E3e):** FX-12/a's HTTP half was
  recorded: run-and-wait answers the failed flow with HTTP 422 and the
  stored error as the body, in `jsonb`'s printed order
  (`{"error": {"name": "Error", "extra": {"code": 7}, "stack": …, "message": "boom", "step_id": "a"}}`,
  [FX-36](#fx-36); fixture
  [`execution/fx-36-http`](../../conformance/flows/execution/fx-36-http), run
  `extra`, oracle run 40794872025).

<a id="fx-13"></a>
### FX-13 · A successful result with `wm_failure` is turned into a failure

**Scope:** later · **Confidence:** high (insertion read; oracle-observed for a step at the end of a flow); medium (downstream path); high (identity jobs, source-traced) · **Status:** confirmed; drafted (identity jobs)

- **Rule:** A step that returns successfully is treated as **failed** if its
  result is an object with a string field `wm_failure`. The result is kept
  unwrapped, and a top-level `"error": {"name": "ManualFailure", "message":
  <wm_failure>}` is inserted alongside the other fields. Downstream it
  behaves like any failure (retry, `continue_on_error`, failure module).

  **Identity jobs are checked too.** The check runs on every job the worker
  completes successfully, whatever its kind, before anything else reads the
  result, so it also applies to the identity job that replaces a module
  ([FX-16](#fx-16)): a mocked module whose `mock.return_value` is such an
  object ([FX-18](#fx-18)), an `identity` module or a skipped module
  ([FX-17](#fx-17)) whose passthrough `previous_result` is one, and an
  identity that reads it from the flow input through
  `pass_flow_input_directly`. The step then fails exactly as a script would,
  with the `error` member added. One consequence: a `wm_failure` step
  tolerated by `continue_on_error` ([FX-30](#fx-30)) hands its retagged
  object to the next step as `previous_result`, so an `identity` or skipped
  step right after it fails again with the same object (the `error` member
  is written again with the same value). A value that is not an object, or
  whose `wm_failure` is not a string, passes through
  ([WR-17](languages.md#wr-17)).
- **Evidence:** `backend/windmill-worker/src/result_processor.rs:107-129`,
  `:1584-1597`; `backend/windmill-queue/src/jobs.rs:652-673`. Identity
  jobs: `backend/windmill-worker/src/worker.rs:4032-4038` (the identity
  result is the args' `previous_result`), `:4129-4145` and
  `backend/windmill-worker/src/result_processor.rs:546-585` (every
  successful result goes to the completion channel),
  `result_processor.rs:376-410` (that channel's one consumer runs the
  marker check of `:117-130` first); `backend/windmill-worker/src/worker_flow.rs:5106-5118`
  (a mock pushes an identity job), `:3874-3890` (its args carry the mock
  value), `:5136-5141` (skipped and identity modules), `:3919-3927`
  (passthrough args); `result_processor.rs:1584-1631` (a failed step hands
  its retagged result to the flow as is)
- **Conformance:** [FX-13/a–FX-13/c](conformance.md#fx-13)
- **Revision 2026-10-28 (oracle, batch E3c):** FX-13/a was recorded as
  stated: the step and the flow fail with `{"x": 1, "error": {"name":
  "ManualFailure", "message": "nope"}, "wm_failure": "nope"}` (fixture
  `conformance/flows/execution/fx-13`, oracle run 40685766017). The
  downstream paths (retry, tolerance) were not recorded.
- **Revision 2026-10-29 (SC3, source re-check, chain N1b):** milestone N1b
  asked whether a mock value or an identity step's value carrying the
  marker fails the step. It does: the marker is checked after every
  successful job, identity jobs included, and nothing exempts them. The
  rule now says so, with the consequence for a tolerated `wm_failure`
  step followed by an identity. New source-derived cases FX-13/b and
  FX-13/c.
- **Revision 2026-10-29 (SC3 validation round 1, cross-reference):** a
  pre-shaped manual failure is stored as its own JSON text, so it is
  measured against `MAX_RESULT_SIZE_MB` like a successful result; an
  oversized one ends as `ResultTooLarge`, not as the manual-failure object
  ([OR-7](oracle.md#or-7), [OR-7/c](conformance.md#or-7)).

<a id="fx-14"></a>
### FX-14 · An input-transform error fails that step; a retry runs with empty args

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** If evaluating a step's input transforms throws, the step's job is
  still launched, with **empty args** and a pre-run error. It fails without
  running the script, with
  `` {"error": {"message": "Error during isolated evaluation of expression `<expr>`:\n<engine error>", "name": "ExecutionErr"}} ``
  and no `step_id`. The message is built directly as an `ExecutionErr`, so it
  has no `Error: ` prefix and no location suffix ([FX-50](#fx-50)).
  `<engine error>` is the evaluator's full error text: `QuickJS evaluation
  error: ` followed by the engine's rendering of the thrown value and its
  stack frames ([expressions.md](expressions.md#je-21)). Recorded for
  `flow_input.a.b.c` with input `{}`:
  `` Error during isolated evaluation of expression `flow_input.a.b.c`:\nQuickJS evaluation error: Error: cannot read property 'b' of undefined\n    at <anonymous> (eval_script:1:1)\n    at <eval> (eval_script:1:45)\n ``
  (the frame columns depend on the expression and its wrapper; the text
  ends in a newline). A transform error inside a simple loop
  iteration has the prefix `could not transform input using an expr: `
  ([control-flow.md](control-flow.md)).

  On a `flow` step the launched job is the subflow's `Flow` job, with the
  same empty args and pre-run error. It fails with the same `ExecutionErr`
  when picked up, before any of its modules starts, so its modules stay
  waiting ([CF-17](control-flow.md#cf-17)).

  This is an ordinary step failure, so `continue_on_error` and the failure
  module apply. **A retry does not re-evaluate the transforms and does not
  copy the pre-run error:** it re-launches the step with the failed attempt's
  args, the empty map, and **the script runs** with no arguments. Whether
  that run fails depends on the script: missing parameters fall back to
  their defaults or fail ([languages.md](languages.md)). A retried `flow`
  step re-launches the subflow with that empty map, so the subflow runs
  with an empty `flow_input`.
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:2746-2771`, `:3891-3905`,
    `:4267-4281`, `:4404-4429`
  - `backend/windmill-queue/src/jobs.rs:6436`, `:6488`
  - `backend/windmill-worker/src/worker.rs:3647-3649`
  - the message: `worker_flow.rs:2761-2767`; the evaluator's prefix
    `backend/windmill-jseval/src/lib.rs:419`, `:941`
  - a `flow` step: the pre-run error is checked at pickup before the job's
    kind is looked at (`worker.rs:3647-3649`), so a `Flow` job carrying one
    never reaches the flow engine; the retry's args are the failed job's
    stored args (`worker_flow.rs:3793-3795`, `:3891-3905`)
- **Conformance:** [FX-14/a to FX-14/c](conformance.md#fx-14)
- **Revision 2026-10-28 (oracle, batch E3c):** recorded as stated, with and
  without a failure module and on retry (fixture
  `conformance/flows/execution/fx-14`, oracle run 40685766017). Added the
  recorded engine text after the prefix, which the finding left open
  ([Q-FX-13](#q-fx-13)).
- **Revision 2026-10-29 (SC4, recordings of milestone N5):** added the
  `flow` step. Milestone N5 recorded a retried `flow` step whose transform
  throws (fixture `control-flow/subflow-retry`, run `pre_run`, oracle run
  40926159329): the failed attempt is the subflow's `Flow` job with args
  `{}` and the transform's `ExecutionErr`, none of its modules ran, and the
  retry ran the subflow with `{}`. New case FX-14/c.

<a id="fx-15"></a>
### FX-15 · Chaining errors: three contexts

**Scope:** core · **Confidence:** high (oracle-observed: the three contexts and their texts); medium (context 2 re-entry outcome) · **Status:** confirmed

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
       **previous** carried value, not the `InternalError`; a `branchone`
       or subflow parent keeps that value as the step's value, while a loop
       or `branchall` re-reads the stored error
       ([CF-23](control-flow.md#cf-23) route 3).

     `<text>` is the error's display text ([FX-50](#fx-50)). For a boolean
     predicate that throws (`skip_if`, the flow `skip_expr`, a `retry_if` at
     re-entry) it is `Error: <evaluator text><loc>`, one location suffix.
     For an error the start phase builds itself (a negative timeout, the
     sleep messages below) it is the bare message, with no `Error: ` prefix
     and no suffix.
  2. **Pickup of a root flow** (no parent): first start, re-queue after the
     preprocessor, or resume after a suspend.
     - The error is a **failure of the module at the cursor**, with value
       `{"error": {"message": "Error: <text><loc>", "name": "Anyhow"}}`,
       where `<text>` is the display text of context 1. The module's status
       entry is a `Failure` with no job.
     - Retry, `continue_on_error` and the failure module apply.
     - Only expressions on the path actually revisited are evaluated again:
       a retry re-runs the module's own `skip_if` and `timeout`; a switch to
       the failure module evaluates the failure module's `skip_if` and
       `timeout`, not the failed module's; the cursor-0 checks run on every
       start phase at cursor 0 ([FX-49](#fx-49)). A retry that hits the same
       throwing expression therefore ends in context 1.
  3. **Pickup of a child flow** (an inline child or a subflow by path):
     - the child job completes **failed** at once, with `{"error":
       {"message": "Error: <text><loc>", "name": "Anyhow"}}`, `<text>` as
       in context 2;
     - **its own modules, retry and failure module are bypassed**;
     - the parent handles it as a failed module: the parent's retry,
       tolerance and failure module apply, and a `flow` module resets the
       failure-module bookkeeping ([FX-5](#fx-5)).

  The name is `Anyhow` at pickup because the start phase returns a generic
  error type at the flow-job boundary, which the worker wraps into its
  `Anyhow` error kind, displayed as `Error: <inner><loc>` ([FX-50](#fx-50)).
  So at a pickup a throwing predicate reads
  `Error: Error: <evaluator text><loc><loc>` (two prefixes, two suffixes)
  and a negative timeout reads `Error: Timeout value cannot be negative<loc>`
  (one of each; `<loc>` includes its leading space).

  Messages:
  - `Timeout value cannot be negative`
  - `Expected an integer, found: <value>` (sleep given a negative integer,
    shown as the number, or a non-number, shown as a debug rendering such as
    `String("5")`)
  - `Expected a number value, had error instead: <debug>` (the sleep
    expression threw). `<debug>` is the debug rendering of the evaluation
    error, its quotes and newlines escaped, for example
    `` ExecutionErr("Error during evaluation of expression `results.a`:\nQuickJS evaluation error: Error: results is not defined\n    at <anonymous> (eval_script:1:1)\n    at <eval> (eval_script:1:46)\n") ``,
    where `\n` is two characters and the inner prefix says `evaluation`,
    not `isolated evaluation`.
  - predicate engine errors: `<evaluator text>` is `QuickJS evaluation
    error: <engine rendering>` ([expressions.md](expressions.md#je-21)).

  Recorded examples (`skip_if: {expr: "null.x"}` unless stated; `<loc>`
  includes its leading space, and the engine text ends in a newline):
  - context 1: `Error: QuickJS evaluation error: Error: cannot read property 'x' of null\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:44)\n<loc>`,
    name `InternalError`;
  - context 3: the same text with a second `Error: ` in front and a second
    `<loc>` at the end, name `Anyhow`;
  - context 2, a timeout expression giving `-1`:
    `Error: Timeout value cannot be negative<loc>`, name `Anyhow`;
  - context 1, a `sleep` reading `results`: `Expected a number value, had
    error instead: ExecutionErr(…)`, name `InternalError`, no prefix and no
    suffix.
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:2085-2156` (InternalError
    completion; parent receives the previous value), `:2777-2793` (generic
    error type), `:3280-3321`, `:3694-3713`, `:3723-3736`, `:3869`,
    `:4392-4396`
  - `backend/windmill-worker/src/worker.rs:3815-3840`, `:3180-3212`,
    `:1706-1737`
  - `backend/windmill-worker/src/result_processor.rs:1910-1967`
  - `backend/windmill-common/src/error.rs:73-75`, `:109`, `:145-151`
  - conversions: `worker_flow.rs:2451` (predicates), `worker.rs:3822`
    (a picked-up flow job); built messages `worker_flow.rs:3694-3713`
    (sleep), `:4392-4396` (timeout)
- **Conformance:** [FX-15/a–FX-15/c](conformance.md#fx-15); also
  [FX-8/b](conformance.md#fx-8) (the sleep message)
- **Design note (not Windmill behaviour):** windmill-lite should match the
  error `name` and the message prefix, not the location suffix.
- **Revision 2026-10-28 (oracle, batches E3b and E3c):** the three contexts
  were recorded as stated (fixture `conformance/flows/execution/fx-15`,
  oracle run 40685766017). The finding said every throwing expression's
  text starts with `Error: ` and ends in a location; that holds for
  predicates only, and a sleep error reads bare (fixture
  `conformance/flows/execution/fx-8`, run `sleep_no_results`, oracle run
  40686049913). Added the doubled prefix and suffix at a pickup and the
  recorded texts ([Q-FX-13](#q-fx-13)).

<a id="fx-16"></a>
### FX-16 · `identity` module

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

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
- **Revision 2026-10-28 (oracle, batch E3b):** recorded as stated: with
  direct input the identity's args are the flow input and it returns `{}`;
  normally its args are `{"previous_result": …}` and it returns that value
  ([`execution/fx-16`](../../conformance/flows/execution/fx-16), oracle run
  40684097626). The mock row is recorded under [FX-18](#fx-18), the retry
  row under [FX-6](#fx-6).

<a id="fx-17"></a>
### FX-17 · `skip_if`

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

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
  failure module reuses its failed args ([FX-32](#fx-32)). **That
  passthrough is not marked skipped:** its status is `Success` with
  `skipped: false`, because the skipped flag is computed only for a regular
  module at a cursor below `len`, never for the failure module (or the
  preprocessor).

  A throwing predicate is a chaining error ([FX-15](#fx-15)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3842-3927`,
  `:5136-5138`, `:645-684` (no `stop_after_if` on identity at `:684`),
  `:1278-1283` (skipped flag), `:6197-6231` (no suspend wait); the flag
  reads the current module, which exists only for a regular cursor
  (`:493-496`, `:650-655`)
- **Conformance:** [FX-17/a, FX-17/b](conformance.md#fx-17); the failure
  module's unflagged passthrough is [FX-32/b](conformance.md#fx-32).
- **Revision 2026-10-28 (oracle, batches E3b and E3c):** recorded as stated
  (fixture `conformance/flows/execution/fx-17`, oracle run 40685013818,
  also a skipped last step whose `stop_after_if` is not evaluated). Added
  that a skipped failure module is not flagged `skipped`, which the finding
  did not say; recorded by fixture `conformance/flows/execution/fx-32`, run
  `handler_retry`, oracle run 40685766017.

<a id="fx-18"></a>
### FX-18 · `mock`

**Scope:** later · **Confidence:** high (oracle-observed, the main rule and the `skip_if` interplay) · **Status:** confirmed

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
- **Revision 2026-10-28 (oracle, batch E3b):** recorded as stated, the
  interplay included, so its confidence is no longer medium
  ([`execution/fx-18`](../../conformance/flows/execution/fx-18), oracle run
  40685013818). Run `mocked`: the identity job's args are
  `{"previous_result": <mock value>}` and it returns the mock value. Run
  `mock_stop`: without a `skip_if`, a clean stop with `skip_if_stopped`
  fires on the mock value, the module is `Success{skipped: true}` and the
  flow ends `skipped` with that value. Run `mock_skip_false`: with a
  `skip_if` that is false, the module is still marked skipped, its
  `stop_after_if` is not evaluated and the next step runs.

<a id="fx-19"></a>
### FX-19 · `stop_after_if` on a step, with its interactions

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

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
- **Revision 2026-10-28 (oracle, batch E3b):** the three modes and the whole
  error-stop matrix are recorded as stated: FX-19/a to FX-19/i, plus
  `skip_if_stopped` winning over `error_message` (run `skipped_wins`) and
  the clean stop without the skipped flag (run `clean_success`), in
  [`execution/fx-19`](../../conformance/flows/execution/fx-19) (oracle runs
  40685013818 and 40686049913). In every error-stop run the stopped
  script's stored job stays successful with its own result while its module
  status is `Failure` ([FX-V2](#fx-v2)). One detail the rule did not state:
  when the failure module runs after a retried error-stop (FX-19/d), its
  `Success` status lists the stopped module's failed attempt in
  `failed_retries`, because that list is flow-wide ([FX-27](#fx-27)).

<a id="fx-20"></a>
### FX-20 · How a stop-early propagates through containers

**Scope:** core · **Confidence:** high (oracle-observed for `branchone`); high (read, other containers); medium (exact branch results) · **Status:** confirmed

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
- **Revision 2026-10-28 (oracle, batch E3b):** the `branchone` bullet is
  recorded as stated
  ([`execution/fx-20`](../../conformance/flows/execution/fx-20), oracle runs
  40685013818 and 40686049913). A clean stop with `skip_if_stopped` in the
  default body ends the parent flow `skipped` with the body step's result,
  and the `branchone` module's own status is `Success{skipped: true}` with
  that result (run `branch_stop`). An error-stop in the body fails the body,
  and the parent handles the `branchone` as an ordinary failed module: with
  no tolerance and no failure module the flow fails with the body's
  `EarlyStopError` and the next step never runs (run
  `branch_error_stop`). Loops, parallel containers and subflows are
  recorded under [control-flow.md](control-flow.md).

<a id="fx-21"></a>
### FX-21 · A `stop_after_if` that throws fails the step unrecoverably

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** If evaluating `stop_after_if` on a successful step throws,
  Windmill re-processes the completion as a failure:
  - the result is `{"error": "<error text>"}`: the error value is a
    **string**, not an object. It is the display string of the evaluation
    error. For an ordinary QuickJS exception that is `Error: `, then
    `QuickJS evaluation error: Error: <engine message>`, a newline and the
    engine's stack (which ends in a newline), then a space and Windmill's
    Rust location `@worker_flow.rs:<line>:<col>`. The engine class name
    never appears ([JE-21](expressions.md#je-21)); the outer `Error: ` and
    the location come from the backend's wrapping of an untyped error;
  - the failure is unrecoverable: **no retry and no `continue_on_error`**;
  - the failure module runs if present;
  - the flow's root log gets
    `` Error evaluating stop_after_if of module '<id>' expression `<expr>`: … ``.

  This changes the module and flow outcome, not the already stored
  successful script result ([FX-V2](#fx-v2)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:223-251` (string
  wrap at `:235-241`), `:715-727`, `:1302-1327`, `:1743-1747`, `:2451`
  (the predicate evaluation whose `?` records the location);
  `backend/windmill-common/src/error.rs:73-74` (an untyped error displays as
  `Error: <error> @<location>`), `:138-151` (the location is the caller's
  file name, line and column);
  `backend/windmill-jseval/src/lib.rs:419`
- **Conformance:** [FX-21/a](conformance.md#fx-21)
- **Revision 2026-10-28 (oracle, batch E3b):** the finding gave the string
  only in outline (medium confidence). Run `throwing_stop` of
  [`execution/fx-21`](../../conformance/flows/execution/fx-21) (oracle run
  40686049913) records
  `"Error: QuickJS evaluation error: Error: cannot read property 'x' of null\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:44)\n @worker_flow.rs:2451:11"`
  for `stop_after_if: null.x`: the engine's `TypeError` shows as `Error:`,
  and the string ends in the Rust location. FX-21/a now writes that
  location as `<loc>`, so a comparison can strip it. Run
  `throwing_stop_handler` (oracle run 40685013818) records the third bullet:
  `continue_on_error` does not apply, the failure module runs and the flow
  fails with its result.

<a id="fx-22"></a>
### FX-22 · Flow-level `skip_expr`

**Scope:** later · **Confidence:** high (main rule: oracle-observed for a root flow); low (by-path first-module quirk) · **Status:** confirmed

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
  (overlap block `:3224-3280`), `:599-618`, `:670-679`. Recorded: runs
  `je33-a` and `je33-b` of `conformance/flows/je/sites` (oracle run
  40687124625): status `skipped`, result `"stopped early"`, module 0 not
  run.
- **Conformance:** [FX-22/a](conformance.md#fx-22)
- **Note:** a skipped flow is reported as a failure by the synchronous HTTP
  endpoint ([FX-V3](#fx-v3)). [JE-33](expressions.md#je-33) is the
  expression side of the same rule.
- **Revision 2026-10-28 (oracle, batch E3a):** recorded as stated, through
  JE-33's runs; [JE-33](expressions.md#je-33), which said the flow
  completes successfully, was corrected to this finding's `skipped`.

<a id="fx-23"></a>
### FX-23 · `sleep`

**Scope:** core · **Confidence:** high (oracle-observed: FX-23/a–c) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestones N2, NI and E1):** the three cases were recorded as stated by milestone N2's fixture
  `conformance/flows/execution/fx-23` (runs `js_own_result`,
  `static_fraction` and `string_value`, oracle run 40818457347), which
  [Q-FX-18](#q-fx-18) still listed as unrecorded.
- **Revision 2026-10-29 (SC4, recordings of milestone NI):** milestone NI's
  fixture `conformance/flows/control-flow/waits` (oracle run 40852609901)
  recorded the rule inside containers, with no case of its own: a body
  step's `sleep` delays the next step of the iteration's child flow (run
  `body_sleep`), and a loop module's own `sleep`, evaluated with the loop's
  array as `result`, delays the module after the loop (run
  `loop_sleep_after`). The `sleep` of the step before a container is
  [CF-21](control-flow.md#cf-21)'s.

<a id="fx-24"></a>
### FX-24 · Per-step `timeout`: value, cap and enforcement by execution path

**Scope:** core (process, identity, child-flow and collapsed-loop paths); later (in-process executors) · **Confidence:** high (paths 1, 3, 4, 5; path 4 rests on an exhaustive search for readers; oracle-observed: FX-24/a, /c, /e, /f and the deployed half of /d); medium (path 2) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestones N2, NI and E1):** FX-24/a, FX-24/c and FX-24/f were recorded as stated
  by milestone N2 (`conformance/flows/execution/fx-24`, runs
  `process_timeout`, `negative_later` and `identity_untimed`, oracle run
  40818457347; FX-24/a also by milestone E1's
  `conformance/flows/languages/bun-results` run `timeout`, oracle run
  40771900586), and FX-24/e and the deployed half of FX-24/d by milestone NI
  (`conformance/flows/control-flow/cf-27`, runs `fx24_e` and `fx24_d`, oracle
  run 40852609901): a parallel loop's 3 s iteration completed under the
  loop's `timeout: 1`, and a collapsed body was cut at about 1 s with the
  loop's id as `step_id`. [Q-FX-18](#q-fx-18) still listed them as
  unrecorded; FX-24/b is [Q-FX-4](#q-fx-4)'s and FX-24/g is `later`.
- **Revision 2026-10-29 (SC4, recordings of milestone NI):** the same
  fixture family recorded the negative and javascript timeouts inside
  containers, with no case of their own (`conformance/flows/control-flow/waits`,
  oracle run 40852609901): a body step's javascript `timeout` times its job
  in the iteration's child flow (run `body_timeout_js`); a negative
  `timeout` on a child flow's second module aborts that child with the
  `InternalError` `Timeout value cannot be negative`, which the loop takes
  as its failed iteration's entry (run `body_timeout_negative_later`), and
  on its first module fails the child at its pickup with
  `Error: Timeout value cannot be negative<loc>`, name `Anyhow` (run
  `body_timeout_negative_pickup`); a loop module's negative `timeout` is a
  chaining `InternalError` at a later transition (run
  `loop_timeout_negative_later`) and, for a collapsed loop at step 0, an
  `Anyhow` failure of the loop step at the root's pickup (run
  `loop_timeout_negative_root`), the route FX-24/b states for a script
  step ([FX-15](#fx-15) contexts 1 to 3).

<a id="fx-25"></a>
### FX-25 · Process timeout: enforcement and error

**Scope:** core · **Confidence:** high (message, oracle-observed); medium (that the canceled step status leaves the flow uncanceled) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestones N2, NI and E1):** FX-25/a was recorded as stated by milestone N2
  (`conformance/flows/execution/fx-25`, run `tolerated`, oracle run
  40818457347); run `retried` of the same fixture adds a timed-out step
  retried with the same args, which timed out again.

<a id="fx-26"></a>
### FX-26 · `retry`: the delay schedule and its exact arithmetic

**Scope:** core (formula); the out-of-range domain is a design decision · **Confidence:** high (formula); medium (wrapping, from the build profile and language defaults) · **Status:** confirmed; drafted (the jitter draws and where a delay is visible, added 2026-10-29 by SC3)

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
  `min(delay, 21600)` seconds (6 hours). `random_factor` is a signed 8-bit
  number; zero or a negative value means no jitter.

  **Where the randomness comes from.** The two draws of step 3, `p` and the
  sign, are independent draws from the worker thread's random generator
  (the thread-local generator of `rand` 0.9.0, seeded from the operating
  system): `p` is uniform over its range and the sign is a fair coin. The
  delay that counts is drawn when the failed module is re-entered for its
  retry ([FX-27](#fx-27)); the completion phase also computes a delay, only
  to decide whether a retry exists, and throws it away. So for given
  `seconds`, `multiplier`, `random_factor` and `n` the distribution of the
  delay is fixed by these rules ([FX-26/f](conformance.md#fx-26)), but no
  single value can be predicted, seeded or replayed.

  **Where a delay is visible.** The retry's job is queued at once, at
  re-entry, with `scheduled_for` set to that moment plus the delay (the
  same field that [FX-23](#fx-23)'s sleep sets). While it waits, the queued
  job's `scheduled_for` shows the delay: in the job returned by
  `GET /api/w/{w}/jobs_u/get/{id}`, or as milliseconds since the epoch from
  `GET /api/w/{w}/jobs/queue/scheduled_for/{id}`, where `{id}` is the job
  the retried module's status entry names. A recording can read it without
  waiting the delay out, then cancel the flow.

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
  - Randomness: `backend/windmill-types/src/flows.rs:453-462` (two calls to
    the thread-local generator), `:499-504` (`random_factor` is an `i8`);
    `backend/Cargo.toml:455` (`rand` pinned at `0.9.0`); the delay used is
    the one computed at re-entry, `backend/windmill-worker/src/worker_flow.rs:3723-3754`,
    while the completion phase's calls at `:1306-1325` and `:1701-1722`
    keep only whether a retry exists. Docs:
    https://docs.rs/rand/0.9.0/rand/fn.rng.html
  - Visibility: `worker_flow.rs:3753` (`scheduled_for` of the retry),
    `:6187-6194` (now plus the delay), `:4417` (passed to the push);
    `backend/windmill-queue/src/jobs.rs:6471`, `:6500` (stored in the
    queue row); `backend/windmill-api/src/jobs.rs:290`, `:740-755` (the
    `scheduled_for` endpoint), `:390` (`jobs_u/get`);
    `backend/windmill-types/src/jobs.rs:185` (a queued job's
    `scheduled_for`)
  - Docs: https://www.windmill.dev/docs/flows/retries
- **Conformance:** [FX-26/a–FX-26/f](conformance.md#fx-26)
- **Design note (not Windmill behaviour):** which part of this numeric domain
  windmill-lite supports is a design decision; any deviation must be stated
  in the equivalence contract and tested at the boundary.
- **Revision 2026-10-29 (SC3, source re-check, chain N2):** milestone N2
  could not record the jitter distribution or FX-26/c–e, the latter for
  lack of a `scheduled_for`. Both are settled from the source as far as a
  source can: the jitter rule above was already exact, and the finding now
  says where its two draws come from, which draw counts, and that the
  distribution is fixed while single values are not (new source-derived
  case FX-26/f gives one distribution); and that the retry job is queued
  with its `scheduled_for` from the start of the delay, which is where a
  recording reads FX-26/c–e ([Q-FX-18](#q-fx-18)).
- **Revision 2026-10-29 (SC4, recordings of milestones N2, NI and E1):** FX-26/a and FX-26/b were recorded as stated by
  milestone N2 (`conformance/flows/execution/fx-26`, runs `phases` and
  `multiplier`, oracle run 40818457347): delays of at least 1, 1, 8 and 16 s,
  and one of at least 6 s. FX-26/c to FX-26/f stay unrecorded.

<a id="fx-27"></a>
### FX-27 · `retry`: what is retried and how state evolves

**Scope:** core · **Confidence:** high (source-traced; oracle pending); high (oracle-observed: a retried script or `flow` step reuses the failed attempt's args; each child flow keeps its own retry state) · **Status:** confirmed; drafted (the second `skip_if` evaluation, added 2026-10-29 by SC3; a parallel container's empty list, added 2026-10-29 by SC4)

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
  - **The second `skip_if` evaluation.** Re-entry runs the module's whole
    start phase again, so its `skip_if` is evaluated once before the first
    attempt and once more before each retry (with its `timeout`). An
    evaluation has no effect of its own: the second one shows only through
    its value. It is observable when the predicate sees something different
    the second time, such as the restored predecessor read back from
    storage in `jsonb` key order instead of the in-memory order of the first
    attempt, which batch E3e recorded ([OR-24/c](conformance.md#or-24)), or
    a predicate that turns true on the retry and skips the module. A
    throwing predicate also leaves a line in the root job's log at each
    evaluation.

  **State:**
  - `fail_count` goes up by one per retry, and failed attempt job ids pile up
    in `failed_retries`;
  - the retry state is **flow-wide**, not per module. Each time a module
    completes, its `Success` or `Failure` status copies the whole current
    list into its own `failed_retries`. So a module that completes while the
    list still holds an earlier module's attempts lists those attempts as
    its own. That happens after a tolerated failure ([FX-29](#fx-29)) and
    when the failure module runs after a module that was retried and then
    failed or error-stopped: the failure module's status repeats the failed
    attempts, beside `parent_module` naming the failed module;
  - **except a parallel container** (drafted): a parallel `forloopflow` or
    `branchall` completes through its own path, which always writes an
    empty `failed_retries`. A retried parallel container therefore lists
    none of its own failed attempts once its retry has started, and one
    that completes while the list holds an earlier module's attempts does
    not list them either, although its success still clears the
    flow-wide list (which attempt a retried parallel container records,
    and from which child it re-enters, is [CF-22](control-flow.md#cf-22)'s);
  - when a module succeeds, the retry state is cleared after its status is
    written, so the next module starts with an empty list;
  - retry is evaluated before `continue_on_error`: a tolerated step is first
    retried to exhaustion;
  - retry is never applied to unrecoverable failures ([FX-21](#fx-21),
    [FX-44](#fx-44));
  - starting a failure module that has its own retry config resets the
    counter to 0.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3720-3832`,
  `:3891-3905`, `:1306-1327`, `:1491-1503`, `:1701-1722`, `:1751`,
  `:3815-3827`; `:1295` and `:1341` (a completing module's status takes the
  flow-wide list), `:3752-3766` (each retry appends to the flow-wide list
  and copies it to the retried module's status); `:3842-3870` (`skip_if`
  evaluated on every entry, an error logged to the root job at
  `:3857-3867`); `:766-773`, `:1037-1061` (a parallel container's
  completed entry with an empty `failed_retries`), `:4763-4779` (re-entry
  replaces the module's entry with an `InProgress` one, which has no list)
- **Conformance:** [FX-27/a](conformance.md#fx-27)
- **Revision 2026-10-28 (oracle, batch E3b):** the finding said only that a
  module's `Success` status lists its `failed_retries` once it succeeds. Run
  `retry_then_handler` of
  [`execution/fx-19`](../../conformance/flows/execution/fx-19) (oracle run
  40686049913) shows the list is the flow's: step `a` error-stops, is
  retried once and stops again; its `Failure` status lists its first
  attempt, and the failure module's `Success` status lists the same attempt
  in its own `failed_retries`, with `parent_module: "a"`. The State bullets
  now say so. FX-27/a itself is not recorded yet.
- **Revision 2026-10-29 (SC3, source re-check, chain N2):** milestone N2
  could not record FX-27/a's double `skip_if` evaluation. The source
  settles when it happens (on every entry into the module's start phase)
  and that it is visible only through a different value, which is how
  [OR-24/c](conformance.md#or-24) recorded the second evaluation. FX-27/a's
  expectation now says how to observe it; the args reuse is still
  unrecorded ([Q-FX-18](#q-fx-18)).
- **Revision 2026-10-29 (SC4, source re-check and recordings of
  milestones N5 and NI):** a retried `flow` step re-ran with the failed
  attempt's stored args, `{}` after a throwing transform (milestone N5,
  fixture `control-flow/subflow-retry`, run `pre_run`, oracle run
  40926159329; [FX-14/c](conformance.md#fx-14)), and a retried collapsed
  loop pushed its first body job from the failed job's stored args
  (milestone NI, [CF-22/c](conformance.md#cf-22)). FX-27/a's own flow,
  a script step, was recorded as stated by milestone N2
  (`conformance/flows/execution/fx-27`, run `reuses_args`, oracle run
  40818457347): the retry's args equalled the first attempt's, with one
  failed retry listed. Milestone NI's `conformance/flows/control-flow/waits`
  (runs `body_retry_seq`, `body_retry_par` and `branch_retry`, oracle run
  40852609901) recorded a body step retried inside each iteration's or
  branch's child flow with the same args, each child flow listing only its
  own failed attempt. From the source (drafted): the State bullet about
  copying the flow-wide list did not hold for a parallel container, whose
  completed entry always has an empty list; milestone NI's retried
  parallel `branchall` showed no listed attempt
  ([CF-22/e](conformance.md#cf-22)).

<a id="fx-28"></a>
### FX-28 · `retry_if`

**Scope:** core · **Confidence:** high (oracle-observed: FX-28/a–c) · **Status:** confirmed

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
    tolerance, and the failure module runs if there is one. The string is
    FX-21's wrapped once more: the retry decision that evaluates the
    predicate converts its error a second time, so the string starts
    `Error: Error: QuickJS evaluation error: ` and ends in two location
    suffixes, the evaluation's and the retry decision's
    (`@worker_flow.rs:2451:11 @worker_flow.rs:1710:32` at the pin);
  - **on re-entry:** a chaining error ([FX-15](#fx-15)).
- **Evidence:** `backend/windmill-types/src/flows.rs:428-442`;
  `backend/windmill-worker/src/worker_flow.rs:2390-2425`, `:1306-1327`,
  `:1701-1751`, `:223-251`, `:3723-3736`; the second wrap: `:2397` (the
  retry evaluation returns an untyped error, so the predicate's located
  error is wrapped), `:1710-1718` (converted back into a located error in
  the completion's retry decision), `backend/windmill-common/src/error.rs:73-74`
- **Conformance:** [FX-28/a–FX-28/c](conformance.md#fx-28)
- **Revision 2026-10-29 (SC4, recordings of milestone N2):** the three
  cases were recorded (`conformance/flows/execution/fx-28`, runs
  `false_prevents`, `throw_handler` and `throw_no_handler`, oracle runs
  40818457347 and 40819992740). FX-28/a held as stated. FX-28/b and FX-28/c
  showed the throwing predicate's string with two leading `Error: ` and two
  location suffixes, where the rule said only "as in FX-21", whose string
  has one of each; milestone N2 noted the difference in its fixture. The
  rule now gives the second wrap and its source; the cases carry a
  correction.

<a id="fx-29"></a>
### FX-29 · Quirk: the retry counter carries over after a tolerated failure

**Scope:** core · **Confidence:** high (oracle-observed: the counter and the carried-over list) · **Status:** confirmed; drafted (a parallel container lists nothing, added 2026-10-29 by SC4)

- **Rule:** The retry counter is flow-wide and is cleared only when a module
  **succeeds**. If module `k` exhausts its retries and is then tolerated by
  `continue_on_error`, the counter is not reset. A later module that fails
  starts with `n` already equal to `k`'s failure count, so it may get fewer
  retries (or none), and its exponential delays are larger. A successful
  module in between clears the counter. The list of failed attempt ids is
  carried over with the counter: the next module to complete, whether it
  succeeds or fails, lists `k`'s failed attempts in its own
  `failed_retries` ([FX-27](#fx-27)). The exception is a parallel loop or
  `branchall`, whose completed entry lists nothing (drafted): if it is the
  next module and succeeds, `k`'s attempts end up listed nowhere, since
  the success clears the list.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:1491-1503`
  (cleared only on `Success`), `:1306-1327`, `:2390-2425`, `:3723-3769`,
  `:1295`, `:1341`
- **Conformance:** [FX-29/a to FX-29/c](conformance.md#fx-29)
- **Design note (not Windmill behaviour):** windmill-lite must reproduce this
  to be exact.
- **Revision 2026-10-28 (oracle, batch E3b):** added the carried-over
  `failed_retries` list. The same flow-wide state is recorded in run
  `retry_then_handler` of
  [`execution/fx-19`](../../conformance/flows/execution/fx-19) (oracle run
  40686049913), where the failure module repeats a failed module's attempt
  ([FX-27](#fx-27)); the tolerated-failure cases FX-29/a and FX-29/b are not
  recorded yet.
- **Revision 2026-10-29 (SC4, recordings of milestone N5):** the carried-over
  list is now recorded after a tolerated failure. In run
  `exhausted_continue` of fixture `control-flow/subflow-retry` (oracle run
  40915682847) a retried `flow` step ran out of attempts under
  `continue_on_error`, and the next step's `Success` entry listed the
  step's failed first attempt (new case FX-29/c). From the source
  (drafted): a parallel container lists nothing ([FX-27](#fx-27)).
- **Revision 2026-10-29 (SC4, recordings of milestone N2):** the revision
  just above said that the counter itself was unrecorded. Milestone N2
  had recorded it: fixture `execution/fx-29` (oracle run 40818457347), run
  `carries_over`, where `b` ran once after `a` was tolerated and `b`'s
  `Failure` entry listed `a`'s two failed attempts, and run
  `reset_by_success`, where `b` ran three times after `s`'s `Success`
  entry listed `a`'s two attempts. FX-29/a and FX-29/b are now
  `oracle-observed`, as stated.

<a id="fx-30"></a>
### FX-30 · `continue_on_error`

**Scope:** core · **Confidence:** high (oracle-observed); high (source-traced: a due retry at the last module, the exclusions added 2026-10-29) · **Status:** confirmed; drafted (a due retry at the last module, and the parallel-container, forwarded-stop and handled-failure sentences, added 2026-10-29 by SC4)

- **Rule:** When a module with `continue_on_error: true` fails (after
  retries are exhausted, [FX-27](#fx-27)):
  - its status is `Failure`, but the cursor still advances;
  - the next module's `previous_result` (and `results.<id>`) is the failed
    module's wrapped error;
  - at the last module, the flow completes **successfully**, with that
    wrapped error as its result.

  **A due retry comes first, at the last module too.** While a retry is
  still due the cursor does not advance, so the module is not yet treated as
  the last one: the continue decision re-enters it and the retry runs. Only
  the attempt after which no retry is due advances the cursor, and only then
  does a last module end the flow successfully. A module with
  `continue_on_error` and `retry: {constant: {attempts: 2}}` that always
  fails therefore runs three times, as at any other position.

  It also applies to error-stops ([FX-19](#fx-19)): the cursor advances, and a
  last-step error-stop without a failure module ends successfully.

  It does not apply to unrecoverable failures ([FX-44](#fx-44)); to the
  failure module and the preprocessor, whose own flag is ignored; to
  collapsed loop bodies, where the body step's flag is ignored and the loop
  module's flag counts ([FX-46](#fx-46)); to a failed parallel loop or
  parallel `branchall`, which aborts the flow with an `InternalError`
  instead ([CF-7](control-flow.md#cf-7), drafted); or to a container failure
  that carries a stop forwarded from a child flow
  ([CF-12](control-flow.md#cf-12) point 4, drafted). A failure that a child
  flow's copy of the failure module handled is tolerated like any other
  ([CF-16](control-flow.md#cf-16) point 8).

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
  `:1302-1346`, `:1724-1764`, `:5657-5697`; a due retry at the last module:
  `:1306-1327` (the cursor advances only when no retry is due),
  `:1369-1397` (the last-module test reads the advanced cursor),
  `:1729-1745` (continue while the module is not the last; the last-module
  success needs the last-module test), `:3723-3760` (the retry runs on
  re-entry)
- **Conformance:** [FX-30/a, FX-30/b](conformance.md#fx-30); also
  [FX-19/e–FX-19/g, FX-19/i](conformance.md#fx-19).
- **Revision 2026-10-28 (oracle, batch E3c):** recorded as stated: a
  tolerated middle step's `Failure` status with the next step seeing its
  wrapped error as `previous_result` and `results.a`, and a tolerated last
  step ending the flow successfully without the failure module (fixture
  `conformance/flows/execution/fx-30`, oracle run 40685766017).
- **Revision 2026-10-29 (SC4, source re-check):** a reading circulated
  that at the last module `continue_on_error` ends the flow at once even
  when a retry is due. It does not: the last-module test reads the cursor
  after the advance, and the cursor does not advance while a retry is due,
  so the retry runs first, as the rule's "after retries are exhausted"
  says. Stated explicitly, with new source-derived case FX-30/b; no
  recording has `continue_on_error` and a due retry on a last module that
  fails ordinarily (`je/errors`' runs `je26-b` and `je26-c` combine them
  with a throwing `stop_after_if`, which is unrecoverable,
  [FX-21](#fx-21)). Added the exclusions for a failed parallel container
  and a forwarded stop, and the handled case.

<a id="fx-31"></a>
### FX-31 · `failure_module`: when it runs

**Scope:** core · **Confidence:** high (oracle-observed for an untolerated failure, cancel, the chaining contexts and inline bodies; source-traced for the other triggers) · **Status:** confirmed

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
- **Revision 2026-10-28 (oracle, batch E3c):** recorded as stated for an
  untolerated failure (fixture `conformance/flows/execution/fx-31`, oracle
  run 40685766017), a cancel ([FX-38](#fx-38)), the three chaining contexts
  ([FX-15](#fx-15)) and an inline body's copy ([FX-34](#fx-34)). No rule
  changed.

<a id="fx-32"></a>
### FX-32 · `failure_module`: what it sees

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

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
    value ([FX-V2](#fx-v2)). An engine expression uses memory when it binds
    `previous_result` ([FX-10](#fx-10) route 3, [FX-V6](#fx-v6));
  - other `results.<id>` read storage;
  - `flow_input` is the flow input;
  - `started_at` (the flow's start timestamp) and `flow_job_id` (a UUID
    string) are available when the flow has started;
  - its `WM_FLOW_STEP_ID` is its own id, normally `failure`, also for the
    copy that runs inside an inline body of a deployed flow; only a body
    built from raw modules suffixes it ([FX-34](#fx-34)).

  When it is entered directly at cursor `len` ([FX-V4](#fx-v4)), the
  preceding id is empty, so `results.<stopped module>` reads storage.

  **Retry of the failure module itself:** the failed attempt's args are
  reused; `previous_result` stays the failure module's own failed-attempt
  value, because the restore step of [FX-27](#fx-27) skips the failure
  module; its `skip_if`, if any, sees that value. A retry that `skip_if`
  skips runs a passthrough identity job of the reused args, and the
  failure module's status is `Success` with `skipped: false`: the skipped
  flag is never set on the failure module ([FX-17](#fx-17)). The failed
  attempts are listed in its `failed_retries`.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2737-2743`,
  `:3325-3327`, `:3778-3792`, `:3805-3829`, `:3891-3905`, `:3936-3947`;
  `backend/windmill-jseval/src/lib.rs:180-218`, `:494-526`; the step id
  inside bodies: `worker_flow.rs:5052-5061`,
  `backend/windmill-worker/src/worker_lockfiles.rs:1553-1591`; the skipped
  flag: `worker_flow.rs:493-496`, `:650-655`, `:1274-1300`
- **Conformance:** [FX-32/a, FX-32/b](conformance.md#fx-32); also
  [FX-V2/a](conformance.md#fx-v2).
- **Revision 2026-10-28 (oracle, batch E3c):** the step-id bullet said the
  id is suffixed inside inline bodies; on a deployed flow it is not (fixture
  `conformance/flows/execution/fx-34`, run `step_id`, oracle run
  40689442792; see [FX-34](#fx-34)). Added that a skipped retry of the
  failure module is not flagged `skipped` (fixture
  `conformance/flows/execution/fx-32`, run `handler_retry`, oracle run
  40685766017). The rest was recorded as stated (run `sees`).

<a id="fx-33"></a>
### FX-33 · `failure_module`: the flow's final result and `recover`

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** After the failure module ran, the flow completes with
  the **failure module's result as its result**, stored as-is with no
  wrapping. The flow succeeds only if the failure module's job **succeeded**
  and its result decodes as the one-field recovery record with `recover`
  equal to the JSON boolean `true`; otherwise it is failed. The decoder
  accepts an object with a boolean `recover` field, and also a JSON array
  holding that field by position, so `[true]` recovers. A decode error, an
  absent `recover` or `null` counts as not recovered. If the failure module
  itself fails, the flow fails with the failure module's wrapped error.

  | Successful failure-module result | Flow recovers? |
  |---|---|
  | `{"recover": true, …}` | yes, result as returned |
  | `[true]` | yes, result `[true]` |
  | `{"recover": "true"}`, `[false]`, `[null]`, `[]`, `[true,false]` | no |

  **Seen from a parent**, a child flow (inline or by path) that ran its
  failure module always reports **failure**, even with `recover: true`. The
  parent receives the failure module's result as the module result. The
  child's own job is still marked successful when it recovered.

  The flow log says `Flow job completed with error, and error handler was
  triggered. It completed with <success|error>, and with recover: <bool>`.
- **Decoder detail:** the record decoder is serde's derived struct decoding
  at the pinned `serde` 1.0.229 and `serde_json` 1.0.151
  (`backend/Cargo.lock:11067`, `:11151`), which accepts a JSON array holding
  the field positionally. A decoder probe with exactly those versions gave:

  | Failure-module result | Decoded `recover` | Flow recovers? |
  |---|---|---|
  | `[true]` | `true` | yes |
  | `[false]` | `false` | no |
  | `[null]` | absent | no |
  | `[]` | decode error | no |
  | `[true,false]` | decode error | no |
  | `{"recover":true}` | `true` | yes |
  | `{"recover":"true"}` | decode error | no |

  The oracle gave exactly these outcomes.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:298-301`,
  `:409-412`, `:1809-1810`, `:2006-2059`, `:2140-2153`;
  `backend/Cargo.lock:11067`, `:11151`.
- **Conformance:** [FX-33/a–g](conformance.md#fx-33)
- **Revision 2026-10-28 (oracle, batch E3c):** the finding was `DISPUTED`
  ([Q-FX-19](#q-fx-19)). As drafted, only an object with a boolean
  `recover: true` recovered and any non-object failed; the correction (from
  a review of this reference) said the record decoder also takes a
  one-element array. The oracle chose the correction: `[true]` made the
  flow succeed with result `[true]`, the other arrays and the string
  `"true"` left it failed, and the object control recovered (fixture
  `conformance/flows/execution/fx-33`, oracle run 40685766017, which also
  recorded the parent view: a recovered child reports failure to its
  parent). The object-only reading is superseded.

<a id="fx-34"></a>
### FX-34 · `failure_module` inside inline bodies runs at the innermost level

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** Inline bodies carry a copy of the flow's failure module
  ([FX-5](#fx-5)). When a step inside a branch or loop body fails and is not
  tolerated there, **the copied failure module runs inside that body's child
  flow**. It sees the inner failing step as `previous_result`.

  **Its id depends on how the body is stored.** At deploy, the dependency job
  stores every non-empty body under the flow's `modules` list of a flow that
  has a failure module as a flow node, holding a copy of the failure module
  with its **own id** (normally `failure`). Only that list is reduced: a
  container placed in the failure-module or preprocessor slot keeps its raw
  bodies, which run as raw flows and so get the suffix below
  (source-traced). A stored body runs from its node, so the copy's
  status entry, its `WM_FLOW_STEP_ID` and the `step_id` of its errors all
  read `failure`, in every iteration and branch. Only a body built at run
  time from raw modules (a raw or preview submission, or a flow without its
  stored lite form, [CF-V4](control-flow.md#cf-v4)) suffixes the copy with
  the numeric parent cursor and iteration or branch index: `failure-2` for a
  `branchone` at step 2, `failure-2-0` for loop iteration 0 at step 2; never
  the container's id.

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
  `:5700-5711`, `:5733-5744`; the stored node is returned before the suffix
  is added at `:5052-5054`; `backend/windmill-worker/src/worker_lockfiles.rs:1553-1591`
  (bodies stored as nodes with the unsuffixed copy), `:1593-1700` (every
  body of the flow gets the same copy), `:748-758` (only the `modules` list
  is reduced, not the failure-module or preprocessor slot)
- **Conformance:** [FX-34/a](conformance.md#fx-34)
- **Revision 2026-10-28 (oracle, batch E3c):** the finding said the copy's
  id is always suffixed (`failure-2-0`), and FX-34/a expected `failure-0-0`.
  On the oracle, a loop at cursor 1 whose two iterations failed ran both
  copies as `failure`: their `WM_FLOW_STEP_ID`, each body flow's status and
  the stored flow node all name the copy `failure` (fixture
  `conformance/flows/execution/fx-34`, run `step_id`, oracle run
  40689442792). The source explains it: deployed bodies are flow nodes,
  which bypass the suffix ([CF-V4](control-flow.md#cf-v4) already said so).
  The once-per-failing-iteration rule was recorded as stated (run `loop`,
  oracle run 40685766017).
- **Revision 2026-10-28 (validation round 1):** "every deployed body is a
  flow node" is narrowed to bodies under the `modules` list; a container in
  the failure-module or preprocessor slot is not reduced at deploy, so its
  bodies run raw and suffix the copy (source-traced). The recorded
  unsuffixed id holds for ordinary stored bodies.

<a id="fx-35"></a>
### FX-35 · `preprocessor_module`

**Scope:** later · **Confidence:** high (oracle-observed for a run-API start and a non-object result; source-traced for the rest) · **Status:** confirmed

- **Rule:**
  - **When it runs:** only when a root flow is started with preprocessing
    applied: by a trigger (webhook, HTTP route, …) or by the run API without
    `skip_preprocessor=true`, and only for a deployed flow that has one. The
    public preprocessor page says it is only called when the runnable is
    started by a trigger and does not list the run API; the source and the
    oracle show that a run-API start runs it too.
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
    The new input also replaces the flow job's stored args. The successful
    preprocessor job's own stored args are then overwritten with the marker
    `{"reason": "PREPROCESSOR_ARGS_ARE_DISCARDED"}` and it is flagged
    `preprocessed`, so reading that job's args back never shows what it
    received. The overwrite applies to any successful job whose flow step id
    is `preprocessor`; a failed preprocessor keeps its args.
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
  - `backend/windmill-worker/src/result_processor.rs:1454-1470` (the
    stored-args marker)
  - Docs: https://www.windmill.dev/docs/core_concepts/preprocessors
    (disagrees on run-API starts, see above)
- **Conformance:** [FX-35/a](conformance.md#fx-35); also
  [FX-5/a](conformance.md#fx-5).
- **Revision 2026-10-28 (oracle, batch E3c):** recorded as stated: a deployed
  flow started through the run API ran its preprocessor first, and its `5`
  became the input `{"value": 5}` (fixture
  `conformance/flows/execution/fx-35`, run `pre`, oracle run 40685766017).
  Added the stored-args marker and the disagreement with the public page,
  which the finding did not state. The preprocessor job is of kind
  `preview` ([OR-28](oracle.md#or-28)).

<a id="fx-36"></a>
### FX-36 · The flow's final result and the HTTP response

**Scope:** core (executor result selection; HTTP for oracle interpretation); later (HTTP adapter) · **Confidence:** high (oracle-observed: executor result selection, the run-and-wait response and the positional array decoding); high (source-traced: the error responses, an array of another length, an alias given twice) · **Status:** confirmed

- **Rule:**
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
    The handler reads the stored result back from `jsonb` as raw text, so
    every JSON it sends is PostgreSQL's printed form of the stored value:
    a space after each `:` and `,`, and object keys in `jsonb` order,
    shorter first, then by bytes ([FX-42](#fx-42)), whatever order the
    producer wrote. That text is first decoded into a record of these
    optional, typed fields, in this order:
    1. `windmill_status_code` (alias `wm_status_code`): an unsigned 16-bit
       integer;
    2. `windmill_content_type` (alias `wm_content_type`): a string;
    3. `windmill_headers` (alias `wm_headers`): a map of string to string;
    4. `windmill_content_transfer_encoding` (alias
       `wm_content_transfer_encoding`): a string;
    5. `result`: any JSON value.

    The decoder takes either an object, whose unknown keys are ignored, or
    an **array of exactly five elements read by position** in the order
    above, each `null` or of its field's type: `[201, null, null, null, 7]`
    decodes as status code 201 with `result` 7. An array of any other
    length, an element of the wrong type, a scalar, `null`, or an object
    that gives a field and its alias both (a duplicate field) fails to
    decode. Then:
    - **Decoding fails** (for example a string status code or a non-string
      header value): the ordinary response, **200** if the flow succeeded,
      else **422**, with the **whole** stored result as a JSON body
      (`application/json`).
    - **Decoding succeeds but every metadata field is absent or `null`:** the
      ordinary response, as above.
    - **At least one metadata field is non-null:**
      - **Status:** the decoded code, sent as it is, a `3xx` included (one
        that is not a valid HTTP status gives the error
        `Invalid status code`); with no code, 422 when failed, else 200 when
        `result` decoded to a non-null value, else 204.
      - **Headers:** set from `windmill_headers` (an invalid name or value is
        an error).
      - **Body with a content type:** the `Content-Type` header is the given
        text verbatim, parameters such as `; charset=iso-8859-1` included,
        and the body is the nested `result` as text: a JSON string is
        unquoted, any other value is its JSON text. With
        `windmill_content_transfer_encoding: "base64"`, the string is
        decoded and the body is the decoded bytes, which need not be UTF-8;
        a non-string `result` or invalid base64 is an error, and so is any
        other transfer encoding, or a transfer encoding without a content
        type.
      - **Body without a content type:** the nested `result` as JSON
        (`application/json`), so a string stays quoted; with no non-null
        `result`, no body and no content type.

    This applies on success **and** on failure.
- **Evidence:**
  - `backend/windmill-worker/src/worker_flow.rs:1478-1489`, `:1972-2059`
  - `backend/windmill-api-jobs/src/execution.rs:250-261` (the typed record
    and its field order), `:322-347` (the stored result read back as raw
    `jsonb` text), `:374-496` (null handling at `:384-397`; status code
    `:400-413`; headers `:415-426`; content type, unquoting and base64
    `:429-474`; a transfer encoding without a content type `:475-480`; no
    content type `:481-485`; decode fallback `:487-495`), `:499-517`
  - Positional decoding: the record's derived decoder also accepts a
    sequence, field by field in declaration order (`serde_derive` `1.0.229`,
    `backend/Cargo.lock:11129-11132`: `src/de.rs:451-470`, `:836`, a missing
    element is an invalid length), and `serde_json` `1.0.151` hands an array
    to it and refuses leftover elements (`src/de.rs:1820-1846`,
    `:1072-1086`). The same mechanism lets a one-element array recover a
    flow ([FX-33](#fx-33)).
- **Conformance:** [FX-36/a–FX-36/m](conformance.md#fx-36); also
  [FX-V3/a](conformance.md#fx-v3), [FX-12/a](conformance.md#fx-12) and
  [FX-37/a](conformance.md#fx-37) over HTTP.
- **Revision 2026-10-28 (oracle, batch E3c):** the executor rule was recorded
  as stated: an empty flow returns its input, normal completion the last
  module's result, a clean stop the stopping module's result, an error-stop
  without a failure module the `EarlyStopError`, a failed `branchall` the
  array of its branches' results, a ran failure module its result, a cancel
  the `Canceled` error and a chaining error the `InternalError` (fixture
  `conformance/flows/execution/fx-36`, oracle run 40685766017, and the
  fixtures of FX-12, FX-15, FX-31, FX-33 and FX-38). The HTTP part
  (FX-36/a–g) and [Q-FX-12](#q-fx-12) wait for batch E3e, which records
  run-and-wait; the status stays `needs-evidence` for that part only.
- **Revision 2026-10-29 (oracle, batch E3e):** the finding was
  `needs-evidence` for non-object results: as drafted, every non-object
  result failed to decode and was sent whole; the validation suspected the
  typed decoder might read an array by position ([Q-FX-12](#q-fx-12)). Run
  `array` of
  [`execution/fx-36-http`](../../conformance/flows/execution/fx-36-http)
  (oracle run 40798097155) settles it for the positional reading:
  `[201, null, null, null, 7]` is answered with HTTP 201 and the body `7`.
  The same fixture recorded FX-36/a and FX-36/c–g as stated, a content type
  unquoting a string result and kept with its charset parameter, base64
  decoding to bytes that are not UTF-8 (`/wD+` gives `ff 00 fe`), and a 302
  answered as it is (runs `content_type`, `charset`, `base64`, `binary`,
  `redirect`; oracle runs 40794872025 and 40807688503). It also showed that
  a JSON body is the stored value's `jsonb` text, spaced and in `jsonb` key
  order, so FX-36/b's body reads `name`, `stack`, `message`, `step_id`; the
  rule now says why. The status is `confirmed`; the exact array length and
  the alias clash are from the source and the decoder ([FX-36/i](conformance.md#fx-36)).

<a id="fx-37"></a>
### FX-37 · `early_return`

**Scope:** later · **Confidence:** high (oracle-observed: poll loop); medium (lookup details) · **Status:** confirmed

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
- **Revision 2026-10-28 (oracle, batch E3c):** the executor part was recorded
  as stated: with `early_return` the flow still completes with its last
  module's result (fixture `conformance/flows/execution/fx-37`, run `early`,
  oracle run 40685766017). The early response itself goes through
  run-and-wait, which batch E3e records.
- **Revision 2026-10-29 (oracle, batch E3e):** the early response was
  recorded as stated: with `early_return: "a"`, run-and-wait answered
  HTTP 200 with the body `1` about 164 ms after the submission, while the
  flow ran on through `b`'s 2 s sleep and completed with `2` after about
  2.4 s (fixture
  [`execution/fx-36-http`](../../conformance/flows/execution/fx-36-http), run
  `early`, oracle run 40794872025). The confidence of the poll loop is now
  oracle-observed.

<a id="fx-38"></a>
### FX-38 · Cancellation

**Scope:** core (host-initiated cancel with the same result shape) · **Confidence:** high (steady state, oracle-observed); low (race); high (source-traced: a step waiting to start) · **Status:** confirmed; drafted (a step waiting to start, added 2026-10-29 by SC4)

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

  **A step waiting to start** (drafted). A step is pushed as soon as the
  module before it completes, even when it must wait: the `sleep` of the
  module before it and a retry delay queue its job with a later
  `scheduled_for` ([FX-23](#fx-23), [FX-26](#fx-26)), and a `parallelism`
  hold queues held iterations ([CF-7](control-flow.md#cf-7)). So when the
  flow is canceled during the wait, the cascade finds that job, flags it and
  makes it due at once. At pickup it completes without running: its stored
  status is `failure` (not `canceled`: that path records no canceler) and
  its result is the `Canceled` value of the cascade's reason and canceler.
  For a `flow` step the subflow's `Flow` job therefore exists and ends that
  way with its own flow status still at step 0: none of its modules starts
  and it creates no job. The caller's module records `Failure` naming that
  job, and the flow ends `Canceled` as above. The flow's own flag is read
  before the next step is pushed, so a cancel that lands before the module
  before it completes means the step is never pushed; one that lands
  between the cascade's search and the push's commit is the race above.
- **Evidence:**
  - `backend/windmill-queue/src/jobs.rs:201-262`, `:264-395`, `:6896-6903`
  - `backend/windmill-worker/src/worker.rs:3644-3646`
  - `backend/windmill-worker/src/handle_child.rs:182-192`, `:340-346`
  - `backend/windmill-worker/src/result_processor.rs:622-627`
  - `backend/windmill-worker/src/worker_flow.rs:1734-1736`, `:1805-1806`,
    `:1972-1988`
  - a step waiting to start: `worker_flow.rs:3667-3720` (a `sleep` sets the
    pushed job's `scheduled_for`), `:3753` (a retry delay), `:4403-4417`
    (the job is pushed with it), `:1505-1507`, `:1736` (the flow's flag is
    read before a push); `backend/windmill-queue/src/jobs.rs:250-258`,
    `:330-347`, `:366-381` (flagged, made due, held iterations released);
    `backend/windmill-worker/src/worker.rs:3644-3646` (completed at pickup
    before any flow or script code), `:1706-1735` (that path passes no
    canceler); `backend/windmill-worker/src/result_processor.rs:1910-1972`
    (the job's completion and the caller's update);
    `backend/windmill-queue/src/jobs.rs:1094-1122` (the stored status is
    `canceled` only with a canceler), `:6896-6903` (the value)
- **Conformance:** [FX-38/a, FX-38/b](conformance.md#fx-38); the race has no fixture.
- **Revision 2026-10-28 (oracle, batch E3c):** the steady state was recorded
  as stated: a flow canceled while its step slept ended `canceled` with the
  `Canceled` result, its failure module did not run, and the killed step's
  own result was the `ExecutionErr` text above with status `canceled`
  (fixture `conformance/flows/execution/fx-38`, run `cancel`, oracle run
  40685766017). The ordering rules and the race were not recorded.
- **Revision 2026-10-29 (SC4, source re-check, milestone N5):** N5 noted
  that no case records whether Windmill launches a flow job for a step
  canceled before its launch. It does: the job was already pushed during
  the wait, and the cancel makes it fail at pickup without running. Added
  as "A step waiting to start", with new source-derived case FX-38/b;
  [CF-28](control-flow.md#cf-28) point 2 cross-references it.

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
- **Conformance:** [FX-39/a](conformance.md#fx-39), a windmill-lite policy
  check (not Windmill behaviour; see the design note).
- **Design note (not Windmill behaviour):** windmill-lite must refuse flows
  that use `suspend` at load time, or fail them with a clear
  unsupported-feature error (for example `unsupported: suspend`). It must not
  silently ignore the suspend.

<a id="fx-40"></a>
### FX-40 · `cache_ttl` (step and flow)

**Scope:** later (the entry store and its permissions: out) · **Confidence:** high (oracle-observed: hits, failed-flow caching, the TTL matrix's loop rows, sharing between collapsed iterations); high (source-traced: where the lookup sits, what a hit skips, the store conditions); medium (S3-aware hashing, whose storage client is closed source on CE; raw-submission bodies; who can read an entry) · **Status:** confirmed; drafted (where the lookup sits, argument equality, what a hit skips and what still applies, the store conditions beyond FX-40/b, S3-aware hashing, where entries live)

- **Rule:**
  - **Lookup.** Before any job with a `cache_ttl` (seconds) runs, including
    a whole flow job with a flow-level `cache_ttl`, the worker looks up a
    cache entry. The key is a SHA-256 over, in order: the job kind; then
    either the job's runnable id, when it has one (a script hash; for a
    `flowscript` job the id of the stored flow node holding its code; for a
    deployed flow, run by path or as a subflow, its flow version id, so each
    new deployment starts with no entries), or else the job's path followed
    by its raw code or raw flow; then each argument, in sorted key order, as
    its name and its raw JSON text. **The path enters the key only for a job
    without a runnable id.** S3-aware hashing can add an object's ETag and
    leave out its path (below).
  - **Argument equality.** The argument text in the key is the job's stored
    arguments as the queue hands them back: the `jsonb` text of each
    top-level value ([FX-42](#fx-42)). Two calls share an entry when their
    arguments are equal as `jsonb` values: member order and whitespace do
    not matter, number spelling does where `jsonb` keeps it (`1` and `1.0`
    are different keys; `1e3` and `1000` are the same). The key covers the
    arguments **as stored, before** the worker resolves `$var:`,
    `$jsonvar:`, `$res:` or `$encrypted:` strings
    ([WR-5](languages.md#wr-5)): the literal reference string is hashed,
    never the value it names.
  - **Where the lookup sits.** At pickup, after the cancellation check (a
    job canceled before it starts completes as canceled, never from the
    cache; [FX-38](#fx-38)) and after a pre-run error (a step whose input
    transforms threw fails as [FX-14](#fx-14) says, never from the cache),
    and before anything else: before argument resolution, and for a flow
    before any step is evaluated. Jobs without `cache_ttl` skip it.
    Identity jobs never carry one, so a mocked module ([FX-18](#fx-18)), a
    skipped one ([FX-17](#fx-17)), an `identity` step and the virtual
    `<id>-v` step neither read nor fill the cache, whatever `cache_ttl` the
    module has. Previews are not exempt: a flow preview with a flow-level
    `cache_ttl`, and a preview's inline steps with one, look up and store
    like any job, keyed by path and code.
  - **Hit.** The queued job itself completes at once, successfully, with
    the cached result. It keeps its own job id; its status is `success`
    (never `skipped`, even when the run that stored the value had stopped
    with `skip_if_stopped`); its log is `Job skipped because args & path
    found in cache and not expired`. For a flow, no step runs and its flow
    status does not advance. Nothing is resolved or executed: `$var:`,
    `$res:` and `$encrypted:` arguments are not read (no variable read, no
    secret audit event), so a hit returns the value computed with whatever
    the variable held when the entry was stored. The completion then takes
    the ordinary success path: [FX-13](#fx-13)'s marker check applies to the
    cached value, and in a flow the step's completion phase applies as for
    any successful job ([FX-2](#fx-2)): `stop_after_if` is evaluated on the
    cached result, and retry and `continue_on_error` have nothing to act on.
    A run-and-wait call of a flow with `early_return` ([FX-37](#fx-37)) that
    the cache serves answers with the cached final result, because the early
    step never ran (medium).
  - **Store.** A failed job never stores.
    - **Script-kind jobs** store their result under the key computed at
      pickup when they succeed after [FX-13](#fx-13)'s check (a `wm_failure`
      result is a failure, so it is not stored). The entry is written before
      the job is recorded as complete, so the next collapsed iteration
      already sees it ([FX-40/d](conformance.md#fx-40)).
    - **Flow jobs** decide once, when the flow completes through the
      completion of a module: its last module, a stop, or its failure
      module. The flow stores its final result when that completion counts
      as a success **before** the `recover` rule ([FX-33](#fx-33)) is
      applied. A flow that ends canceled ([FX-38](#fx-38)), or that a
      chaining or start error ends without a failure module
      ([FX-15](#fx-15)), stores nothing.

      | How the flow ends | Stored | Stored value |
      |---|---|---|
      | Its last module succeeds, including a skipped step's identity, the virtual `<id>-v` step, and a flow with no modules ([FX-3](#fx-3)) | yes | the final result |
      | A clean stop ([FX-19](#fx-19)), with or without `skip_if_stopped` | yes | the stopping module's result |
      | A failure tolerated at the last module: its `continue_on_error`, a loop's `skip_failures`, a sequential `branchall` branch's `skip_failure` ([FX-30](#fx-30)) | yes | the wrapped error, or the container's array |
      | An error stop tolerated by `continue_on_error` at the last module, with no failure module | yes | `{"error": {"name": "EarlyStopError", …}}` |
      | Any other error stop without a failure module | no | |
      | The last module fails, untolerated, with no failure module | no | |
      | The failure module ran and succeeded, with or without `recover: true` | yes | the failure module's result |
      | The failure module failed | no | |

      So a flow that failed only because its successful failure module did
      not return `recover: true` is stored, and a later identical run
      completes **successfully** with that value
      ([FX-40/b](conformance.md#fx-40)).
    - **A container as the last module.** The decision uses the container's
      own outcome, as the flow counts it ([control-flow.md](control-flow.md#cf-5),
      [CF-V1](control-flow.md#cf-v1)), and the stored value is the
      container's array; which child job finished last does not matter. A
      parallel loop in which one iteration failed, without `skip_failures`,
      is not stored, even when the iteration that finished last succeeded.
    - **The flow's key at store time** is computed again at completion,
      over the flow job's arguments as they are then. Those are the run's
      input, except when a preprocessor ran ([FX-35](#fx-35)): its output
      replaced the arguments, and the flow job, queued again after it, looks
      the cache up a second time with them (medium).
    - **Expiry.** An entry expires at its store time in whole Unix seconds
      plus the TTL, and it is valid while the current second is earlier. A
      `cache_ttl` of `0` stores an entry that has already expired, so it
      never hits.
    - **A failed store** is logged by the worker, not in the job's logs, and
      changes nothing for the job; the worker's in-memory copy (below) is
      still written.
  - **S3-aware hashing** (medium; CE builds include it). For each argument
    whose whole value decodes as Windmill's S3 object record (a JSON object
    with a string `s3` member and optional `storage`, `filename` and
    `presigned` members; other members are ignored; values nested inside an
    argument are not examined), the worker tries to get the object's ETag:
    1. it reads the workspace's storage setting: the secondary storage that
       `storage` names, or else the primary storage;
    2. a filesystem storage is opened directly. For the S3, Azure Blob,
       Google Cloud Storage and identity-federated kinds, the storage
       resource is read through the API with the job's token (interpolated)
       and turned into a client by a helper that the public source only
       stubs: the CE image compiles a closed-source module for it
       ([OR-3](oracle.md#or-3)). The instance's bucket and account
       restrictions are checked next;
    3. it asks the store for the metadata of the object at the `s3` key and
       takes its ETag.

    When an ETag is found, it is hashed after the argument's name, and the
    argument's text follows unless `cache_ignore_s3_path` is true. So the
    same path with new content misses, and with the flag two paths whose
    objects have the same ETag share an entry. **Any failure along the way
    gives no ETag: the argument is hashed as plain JSON, and
    `cache_ignore_s3_path` then has no effect.** That covers no storage
    configured, an unreadable setting or resource, a refused bucket, a
    missing object and an object without an ETag. A workspace without object
    storage therefore hashes S3 objects exactly like any other argument.
    `cache_ignore_s3_path` is a boolean beside `cache_ttl`: on a flow (for
    the flow job), on a flow module (for its inline, `flowscript` and
    hash-pinned script jobs), and in a script's settings (for a by-path
    script job, which drops the module's flag as it drops the module's
    TTL). A flow's ETags are fetched again at completion for its store key.
  - **Where entries live** (`out`). An entry is a workspace resource of
    type `cache` at `g/results/<the key's 64 hex digits>`, holding
    `{"expire": <Unix seconds>, "value": <result>}`; a later store under the
    same key overwrites it. Each worker process also keeps up to 1000
    entries in memory, keyed by that path, and checks them first (expiry
    included). A database read goes through the API with the job's own
    token, so row-level permissions apply: a `g/results/…` row is visible to
    admins and to members of a group named `results`, and for any other job
    owner only the worker's in-memory copy can hit (medium). The server's
    periodic cleanup deletes expired entries, and a workspace export leaves
    them out.
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
    ignores the loop module's.
  - **Collapsed iterations of one run share entries** when their arguments
    are identical and a TTL applies. Each iteration's job path ends in
    `/loop-<index>` (or is the rawscript's explicit `path`), but on a
    deployed flow every body job has a runnable id, so the path is not in
    the key:
    - an inline `rawscript` body, with or without an explicit `path`, is
      stored at deployment as a flow node and runs as `flowscript` jobs that
      all carry that node's id;
    - a by-path `script` body runs as jobs of the path's deployed hash, and
      a hash-pinned body as jobs of its hash.

    Only a collapsed body that runs from raw module values, as `rawscript`
    jobs without a runnable id, is keyed by path and code; there iterations
    share an entry only when the rawscript has an explicit common `path`
    (medium, not recorded). Previews never collapse for-loops
    ([FX-46](#fx-46)); which other raw values collapse is
    [CF-8](control-flow.md#cf-8)'s question.
- **Evidence:**
  - `backend/windmill-worker/src/worker.rs:3753-3814`
  - `backend/windmill-worker/src/common.rs:1497-1575` (S3 hashing at
    `:1507-1541`; the key at `:1544-1575`), `:1706-1774`
  - `backend/windmill-worker/src/worker_flow.rs:1992-2006`, `:5038-5074`,
    `:5199-5246`, `:5670` (the `/loop-<index>` path), `:5908-6153` (by hash
    at `:6120`; a rawscript's explicit path or the fallback at `:5949`;
    `flowscript` payloads at `:5959-5980`), `:6003`
  - `backend/windmill-queue/src/jobs.rs:5401-5419` (a `flowscript` job's
    runnable id is its flow node)
  - `backend/windmill-worker/src/worker_lockfiles.rs:1594-1650` (deployment
    turns inline rawscripts into `flowscript` nodes, also inside collapsed
    bodies)
  - `backend/windmill-common/src/jobs.rs:80-160` (by path: the latest
    deployed hash)
  - Docs: https://www.windmill.dev/docs/flows/cache
  - Where the lookup sits and the hit: `backend/windmill-worker/src/worker.rs:3644-3649`
    (cancellation and pre-run error first), `:3753-3810` (key, lookup and
    hit completion; the log at `:3776`);
    `backend/windmill-worker/src/result_processor.rs:117-131` (the marker
    check, which every completion passes through, `:394`),
    `backend/windmill-api-jobs/src/execution.rs:290-322` (`early_return`
    falls back to the completed result); `worker_flow.rs:5106-5137` (mocked
    and skipped modules become identity jobs);
    `backend/windmill-queue/src/jobs.rs:6090-6092` (identity jobs carry no
    cache settings), `:5654-5661` (a flow preview's settings), `:5932-5933`,
    `:5967-5973` (a deployed flow job's runnable id is its version id)
  - Arguments: `common.rs:1497-1542` (hashing; each top-level value's stored
    text); the queue returns `jsonb` text ([FX-42](#fx-42))
  - The store: `result_processor.rs:1442-1446` (script jobs, on success);
    `worker_flow.rs:1972-2006` (canceled flows skip it; the store comes before
    the `recover` check at `:2006`), `:1272-1347`, `:1350-1367` (error stops),
    `:1027-1060`, `:1478-1489` (a container's outcome and array), `:1724-1764`
    (a tolerated failure at the last module counts as a success),
    `:1660-1673` (a preprocessor's output replaces the flow's arguments);
    `common.rs:1689-1741` (entry shape, the in-memory copy of 1000, expiry),
    `:1743-1774` (the store; a failure is only logged, `:1769`; the memory
    copy, `:1773`)
  - S3-aware hashing: `common.rs:1511-1539`, `:1578-1687` (the storage
    setting, the resource read, the restrictions);
    `backend/windmill-types/src/s3.rs:264-273` (the S3 object record);
    `backend/windmill-object-store/src/lib.rs:58-59`,
    `backend/windmill-object-store/src/job_s3_helpers_oss.rs:1-3` (the client
    helper is the closed-source module when `private` is set),
    `backend/windmill-object-store/src/lib.rs:1039-1056` (the ETag from a
    metadata request); `backend/Cargo.toml:182-189`, `:199` (`parquet` and
    `private` are in the CE build); `backend/windmill-common/src/jobs.rs:115-160`
    and `worker_flow.rs:6119-6121` (which flag a script job takes)
  - Where entries live: `backend/windmill-store/src/resources.rs:564-591`
    (read with the caller's permissions);
    `backend/migrations/20240216100535_improve_policies.up.sql:137-139`
    (group rows); `backend/src/monitor.rs:1313-1326` (cleanup);
    `backend/windmill-api/src/workspaces_export.rs:794` (export)
- **Conformance:** [FX-40/a–FX-40/l](conformance.md#fx-40)
- **Revision 2026-10-28 (oracle, batch E3d):** the finding was `DISPUTED`
  ([Q-FX-17](#q-fx-17)). As drafted, iterations within one run never shared
  entries, because each collapsed iteration's path ends in `/loop-<index>`;
  the validation's correction let a hash-pinned body share but not a
  rawscript without an explicit path. The oracle settles it for sharing in
  every deployed variant, which neither text predicted for a rawscript
  without a path: runs `d-raw`, `d-raw-path` and `d-script` of
  [`execution/fx-40`](../../conformance/flows/execution/fx-40) (oracle run
  40686532046) and `d-hash` of
  [`execution/fx-40-hash`](../../conformance/flows/execution/fx-40-hash)
  (oracle run 40692334073). The raw captures show the rawscript body's jobs
  as `flowscript` jobs with paths ending in `/loop-0` and `/loop-1` and one
  script hash. The key rule above explains all four. Run
  `d-hash-script-ttl` records the matrix row for a hash-pinned script: its
  own `cache_ttl` is dropped, so with no loop TTL nothing is cached. FX-40/a
  to FX-40/c are recorded as stated (oracle runs 40685057858 and
  40686532046), FX-40/b answering [Q-FX-6](#q-fx-6).
- **Revision 2026-10-29 (SC4, source re-check, milestone N4):** milestone N4
  read the store rule literally (store when the job that ended the flow
  succeeded) and asked about the cases no recording covers. The rule now
  states them. Two of N4's readings differ from Windmill: a plain error
  stop is **not** stored (only an error stop tolerated at the last module
  without a failure module is, because that flow succeeds), and a failure
  tolerated at the last module **is** stored, since that flow succeeds too.
  A failed failure module stores nothing, as N4 read it. Also added: where
  the lookup sits at pickup (after cancellation and a pre-run error, before
  argument resolution), that identity jobs (mock, `skip_if`) never touch
  the cache, what a hit skips (no `$var:`/`$res:` resolution) and what
  still applies (`stop_after_if`, FX-13's marker check), argument equality
  under `jsonb`, the version id in a flow's key, the expiry arithmetic, a
  failed store, where entries live, and S3-aware hashing in full: with no
  workspace object storage it hashes S3 objects as plain JSON and
  `cache_ignore_s3_path` has no effect. New source-derived cases FX-40/e to
  FX-40/l, tracked by the new [Q-FX-21](#q-fx-21).

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
    EE-only; a build without the `private` feature rejects it with
    `Squashing flow loops is not available in OSS`. The published CE image
    is built with `private` and does not use that stub: the oracle recorded
    six squashed loops on it completing with the results of their
    unsquashed forms, without that error ([CF-25](control-flow.md#cf-25),
    [CF-25/d](conformance.md#cf-25)). Whether it reuses a runtime across
    iterations is not recorded ([Q-CF-19](control-flow.md#q-cf-19)).
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
- **Revision 2026-10-29 (SC3, source re-check of the CE build, chain N3a):** "OSS rejects
  it" is narrowed to a build without the `private` feature; the CE image
  compiles the closed-source runner module instead
  ([CF-25](control-flow.md#cf-25)). On `delete_after_use`: on CE the
  flow-level wipe described above does not happen, because the completion
  cleanup is `enterprise`-gated; the only CE effect is on the synchronous
  script routes, and a deployed flow keeps the key only in the deploy race
  ([OR-21](oracle.md#or-21)).
- **Revision 2026-10-29 (SC3 validation rounds 1 and 2):** the `squash`
  item first took milestone NI's recordings to mean that the CE image
  runs a squashed loop as an ordinary loop. It now says only what those
  six runs show, their results and no stub error, and leaves runtime
  reuse open ([CF-25](control-flow.md#cf-25),
  [Q-CF-19](control-flow.md#q-cf-19)).

<a id="fx-42"></a>
### FX-42 · Where JSON serialisation happens between steps

**Scope:** core (boundary ordering, in-memory handoff, `flow_input` shortcut); later (number rewriting for non-JavaScript consumers); resolver behaviour per the expressions and client-API areas · **Confidence:** high (map types, source-traced); high (oracle-observed: the in-memory handoff's key order, a static object's jsonb order, `$jsonvar:` key order and spelling); medium (jsonb number rewriting, which is PostgreSQL behaviour; `$res:` ordering); high (source-traced: rule 9's number notation and its tie rule); high (source-traced: a child result the read cannot decode, root flow); medium (the same in a child flow, whose parent the zombie-flow monitor ends; not traced end to end) · **Status:** confirmed; drafted (rule 9, the number notation, its tie rule, and a child result the read cannot decode)

- **Rule:** Values cross step boundaries as JSON text, and most
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
     source provides them, and each resolved argument is then written out
     again by the backend's JSON library: `$jsonvar:` parses the variable's
     text into a JSON value, **so its key order survives but not its
     spelling** ([FX-V5](#fx-v5)). Numbers come back in that library's
     notation (rule 9): an integer that fits a signed or unsigned 64-bit
     integer stays exact, and every
     other number becomes a double (`1e3` → `1000.0`, `1.50e3` → `1500.0`,
     `1e21` → `1e+21`, `123456789012345678901234567890` →
     `1.2345678901234568e+29`). Strings are re-escaped minimally
     (`"A\/"` → `"A/"`), and whitespace is dropped. `$var:` yields a string;
     `$res:` yields the resource value as the platform returns it. No
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
  8. **Strings containing NUL** cannot be stored ([FX-48](#fx-48)), nor can
     unpaired surrogate escapes or numbers outside PostgreSQL `numeric`'s
     range ([FX-51](#fx-51)).
  9. **The backend's number notation.** Where the backend parses JSON into
     a value and writes it out again, numbers take this form. The sites are
     rule 2's resolved references, and the results of a loop's or
     `branchall`'s children, which are read from storage into values and
     written out as the container's result text (its `previous_result`,
     [expressions.md](expressions.md#je-44)).
     - **Negative zero.** The integer token `-0` is not an integer: it is
       parsed as the double negative zero and written `-0.0`, while `0`
       stays `0`.
     - **Integers.** Any other number written without a fraction or an
       exponent that fits an unsigned 64-bit integer (non-negative) or a
       signed one (negative) is kept exactly and written as an integer.
     - **Doubles.** Every remaining number is parsed as the nearest double;
       the parse is correctly rounded, so a double written in its shortest
       form comes back with the same digits. A number too small for a double
       becomes zero with its sign (`1e-400` is `0.0`, `-1e-400` is `-0.0`).
       A number too large for one is not accepted: the parse fails with
       `number out of range at line <l> column <c>`, so a `$jsonvar:`
       reference fails its step ([FX-51](#fx-51) rule 5) instead of
       producing a value.
     - **Digits.** A double is written with the shortest digits that parse
       back to it. Zero is `0.0`, negative zero `-0.0`. When two digit
       strings of that shortest length are equally near the double, the
       one ending in an even digit is written. Such ties exist:
       `562949953421312.25` (2⁴⁹ + 0.25, a double whose neighbours are 0.125
       away) lies exactly halfway between `562949953421312.2` and
       `562949953421312.3`, both of which parse back to it, and is written
       `562949953421312.2`; `562949953421312.75` is written
       `562949953421312.8`. Go's shortest formatting and JavaScript's
       number-to-string make the same choice, so the digits agree with
       both; only the notation differs.
     - **Notation.** Let E be the exponent of the first significant digit
       (the double is d.ddd × 10^E). If −5 ≤ E ≤ 15 the double is written
       in plain decimal: when every digit is before the point, the digits,
       zeros up to the point and `.0` (`1000.0`, `1000000000000000.0`);
       otherwise with the point among the digits (`12.5`) or, for E < 0,
       `0.` and −E−1 zeros before them (`0.00001`, `0.00001234`). Otherwise
       it is written as the first digit, a point and the other digits when
       there are any, `e`, the sign `+` or `-`, and E without leading zeros
       (`1e-6`, `1.5e-7`, `1e+16`, `1.7976931348623157e+308`, `5e-324`).

     The exponent form matches JavaScript's (`1e+21`, `1e-7`), but the
     ranges differ: JavaScript writes plain decimal for 10⁻⁶ ≤ |x| < 10²¹
     (E from −6 to 20), the backend only for E from −5 to 15. JavaScript
     also writes no `.0`, and it would round a 64-bit integer the backend
     keeps exact.

     | Text the backend reads | Written |
     |---|---|
     | `1000`, `18446744073709551615`, `-9223372036854775808` | the same (64-bit integers) |
     | `18446744073709551616`, `-9223372036854775809` | `1.8446744073709552e+19`, `-9.223372036854776e+18` |
     | `1e3`, `1000.0` | `1000.0` |
     | `1.50`, `0.1`, `0.0`, `-0.0` | `1.5`, `0.1`, `0.0`, `-0.0` |
     | `0`, `-0` | `0`, `-0.0` |
     | `1e-400`, `-1e-400` | `0.0`, `-0.0` |
     | `1e400`, `-1e400`, `123e999` | not accepted: `number out of range at line 1 column 5` (6, 7) |
     | `0.00001` (or `1e-5`) | `0.00001` |
     | `0.000001` (or `1e-6`), `0.000001234` | `1e-6`, `1.234e-6` |
     | `0.0000001` (or `1e-7`), `0.00000015` | `1e-7`, `1.5e-7` |
     | `1e15`, `123456789012345.6` | `1000000000000000.0`, `123456789012345.6` |
     | `1e16`, `1e17` | `1e+16`, `1e+17` |
     | `100000000000000000000` (10²⁰), `1000000000000000000000` (10²¹) | `1e+20`, `1e+21` |
     | `123456789012345680000`, `123456789012345678901234567890` | `1.2345678901234568e+20`, `1.2345678901234568e+29` |
     | `5e-324`, `1.7976931348623157e308` | `5e-324`, `1.7976931348623157e+308` |

     The text read matters. A `$jsonvar:` variable's text is parsed as the
     user wrote it, since a variable is stored as text, so `-0` there
     reaches the arguments file as `-0.0`. For a loop's children the text
     read is PostgreSQL's printed `jsonb`, which writes every number in
     plain decimal and keeps the scale (rule 6; `1e-7` is printed
     `0.0000001`, `1.50` stays `1.50`), and has no negative zero: a child's
     `-0.0` was already stored as `0.0` ([JE-16](expressions.md#je-16)), so
     the container's result text holds `0.0`.
     Integers a JavaScript step returns stay as written up to
     the 64-bit limits; beyond them (10²⁰, 2⁶⁴) they turn into exponent
     form. A number JavaScript wrote as `1e+21` or `1.5e-7` comes back the
     same; `0.000001` comes back as `1e-6`.

     **A child result the read cannot decode** (drafted). The read of a
     container's children covers `forloopflow` (sequential or parallel,
     collapsed or not), `whileloopflow` and `branchall` (sequential or
     parallel), not `branchone`, whose result travels in memory. It
     decodes each child's stored `jsonb` into a value, and two stored
     values cannot be decoded: a number whose double would be infinite (a
     `1e400`, which `jsonb` keeps, [FX-51](#fx-51), and prints as `1`
     followed by 400 zeros) and nesting deeper than the library's limit
     (an array or object opened at depth 128). A JavaScript step cannot
     return the number, but an identity child can carry either from a flow
     input, and a Bun child can return the nesting. The read runs when the
     container ends; a `stop_after_all_iters_if`, or a loop's
     `stop_after_if` that mentions `all_iters`, reads the children earlier,
     and the flow then ends the same way, at the latest when the container
     ends. When the read fails:
     1. The container step gets no result and no final status: the
        transition that would record them is rolled back, so the module's
        status stays in progress. The worker tries the same transition once
        more as an unrecoverable failure of the child, and that read fails
        the same way. The child that completed keeps its own stored result.
     2. The flow job that runs the container is then completed **failed,
        directly**, with
        `{"error": {"name": "InternalErr", "message": "SqlErr: error occurred while decoding column 0: <library text> @<loc>"}}`.
        `<library text>` is `number out of range at line <l> column <c>`
        or `recursion limit exceeded at line <l> column <c>`, positioned in
        PostgreSQL's printed form of that child's stored result (spaces
        after `:` and `,`, keys in `jsonb` order): for the number, the
        column of its last digit; for the nesting, the column of the
        bracket that opens depth 128. `<loc>` is the backend's location
        (`worker_flow.rs:<line>:<col>`). The name is `InternalErr`, not
        the chaining `InternalError` of [FX-15](#fx-15); the message has no
        `Error: ` prefix and one location suffix. The flow's log gains
        `Unexpected error during flow job error handling:` and the same
        text.
     3. No retry, no `continue_on_error`, no failure module and no later
        step runs.
     4. **When the container runs in a child flow** (a loop body, a
        branch, a subflow), that child flow is completed failed the same
        way, but this route skips the transition that would tell its
        parent, so the parent keeps waiting. Windmill's zombie-flow monitor
        then cancels a running flow that has made no progress for
        `FLOW_ZOMBIE_TRANSITION_TIMEOUT` seconds (60 by default), with a
        force cancel ([CF-28](control-flow.md#cf-28) point 5) whose reason
        starts `Flow <id> (<url>) was cancelled because it was hanging in between 2 steps.`
        (`Flow was cancelled because subflow <id> (<url>) was hanging …`
        when the waiting flow is itself a flow step). This part is medium:
        read, not traced end to end.
- **Open:** [Q-FX-7](#q-fx-7) (the cases not yet recorded: nested key order
  of a stored producer value, numbers for non-JavaScript consumers, scale).
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
    feature `preserve_order`; no `arbitrary_precision`), so it parses
    numbers into 64-bit integers or doubles. It is `serde_json` `1.0.151`
    (`backend/Cargo.lock:11151-11154`), which writes doubles through `zmij`
    `1.0.23` (`:17353-17356`). `$jsonvar:` parsing and re-serialisation:
    `backend/windmill-worker/src/common.rs:194-208`, `:293-303`.
  - Rule 9: `serde_json` `1.0.151` writes a double with
    `zmij::Buffer::format_finite` (`src/ser.rs:1716-1723`); `zmij` `1.0.23`
    picks the shortest digits and uses plain decimal for a leading-digit
    exponent from −5 to 15 (`src/lib.rs:320`, `:1622-1647`), else the
    exponent form with its sign and no padding (`:1649-1702`, the
    exponent strings at `:583-623`); zero is written at `:1538-1546`
    (the sign is written first, at `:1518-1521`). `serde_json`'s parser
    turns the integer token `-0` into a double (`src/de.rs:509-526`),
    rejects a double whose value is infinite with `NumberOutOfRange`
    (`src/de.rs:624-635`; text `number out of range`, `src/error.rs:366`),
    and lets one that is too small round to a signed zero; a `$jsonvar:`
    parse failure becomes the step's error at
    `backend/windmill-worker/src/common.rs:298-300`. Checked against those
    crates, as built with `float_roundtrip`.
    The parse is correctly rounded because the CE build (`features=ce`,
    which includes `deno_core` through `all_languages`,
    `backend/Cargo.toml:132`, `:177`, `:198-199`) links `deno_core`
    `0.352.1` (`backend/Cargo.lock:3733-3735`),
    whose dependency on `serde_json` turns on `float_roundtrip`; Cargo's
    feature resolver (`resolver = "2"`, `backend/Cargo.toml:8`) applies it
    to the whole binary. Without it, about one double in ten would come
    back one digit off. A loop's or `branchall`'s child results are read
    into values and written out at
    `backend/windmill-worker/src/worker_flow.rs:2299-2326`. Checked with
    the pinned crates: every table row, and two million random doubles
    printed shortest, read back and written again unchanged.
  - PostgreSQL's plain-decimal printing of exponent input is checked
    against the oracle's PostgreSQL image by `conformance/jsonb/je-44.yaml`.
  - Rule 9's tie rule: `zmij` `1.0.23` `src/lib.rs:1477-1503` (the scale
    leaves at most one shorter candidate in the rounding interval; the
    extra digit is the nearest one, an exact half rounding to the even
    digit: `2.5` to `2` at `:1495-1497`, `7.5` to `8` through the bias of
    `:823`). A probe of the pinned crate wrote the even digit for each of
    50,000 such ties above 2⁴⁹, and its digits matched Go's
    `strconv.FormatFloat(v, 'e', -1, 64)` for 2.66 million doubles (600
    consecutive and 600 random doubles of every binade, and 200,000
    consecutive doubles above 2⁴⁹). JavaScript's rule: ECMA-262,
    Number::toString ("If there are two such possible values of n, choose
    the one that is even").
  - A child result the read cannot decode: the read
    `backend/windmill-worker/src/worker_flow.rs:2299-2324` decodes the
    `jsonb` column as a nullable JSON value (`backend/.sqlx/query-b1c96c527c4b263b5155d689eb88894ea93f0eaba37874f828a733062af17640.json`:
    `Jsonb`, nullable); it runs at the container's end `:1480-1488`, for
    `stop_after_all_iters_if` `:369-372` (whose error becomes an error
    stop, `:1004-1016`, `:1234-1245`), and for a loop `stop_after_if`
    naming `all_iters` `:690-696`; `branchone` keeps no child list
    `:4703-4717`. The decode: `sqlx-postgres` `0.8.6` `src/types/json.rs:84-97`
    (the version byte, then the library's parse of the text),
    `sqlx-core` `0.8.6` `src/row.rs:151-162` and `src/error.rs:78-85`
    (`error occurred while decoding column <index>: <source>`, the index
    being the column's position, `sqlx-macros-core` `0.8.6`
    `src/query/output.rs:147`); `serde_json` `1.0.151` `src/de.rs:63`,
    `:1372-1386` (the depth limit, reported at the opening bracket),
    `:677-716` (an integer beyond a double), `:1102-1150` (a raw value is
    skipped without either check, which is why the result could be
    stored and passed on). The wrapper: `backend/windmill-common/src/error.rs:51-52`,
    `:153-158` (`SqlErr: <error> @<location>`). The retried transition
    `worker_flow.rs:197-251`; the error reaches
    `backend/windmill-worker/src/result_processor.rs:1520-1543`,
    `:1622-1648`, `:720-736`, and `handle_job_error` `:1910-2016`, which
    tries the transition again and then completes the flow job with name
    `InternalErr` (`:1980-2005`); `backend/windmill-queue/src/jobs.rs:883-914`,
    `:1094-1153` (a completed child is not overwritten),
    `:1239-1258` (a completion only refreshes the parent's progress
    time). The monitor: `backend/src/monitor.rs:174-177`, `:4867-4893`,
    `:5118-5136`, `:5174-5197`. Checked with the pinned crates: the
    stored `{"n": 1e400, "iter": {"index": 0, "value": 1}}`, printed
    with 400 zeros, fails at column 407; an object holding 127 nested
    arrays fails at column 133.
  - PostgreSQL: https://www.postgresql.org/docs/current/datatype-json.html,
    https://www.postgresql.org/docs/current/datatype-numeric.html
- **Conformance:** [FX-42/a–FX-42/n](conformance.md#fx-42); also
  [FX-V2/a](conformance.md#fx-v2), [FX-V5/a](conformance.md#fx-v5).
- **Design note (not Windmill behaviour):** an earlier proposal that a
  key-sorting walk is enough for parity is withdrawn. Exact parity needs
  nested jsonb key order and duplicate collapse, number rewriting where
  non-JavaScript consumers exist, a NUL policy, and an unspecified outer-map
  order: windmill-lite must not specify a deterministic order there.
- **Note (what is observable for `$jsonvar:`):** three stages differ.
  (1) The **stored args** of the step job keep the reference string
  unresolved (`{"v": "$jsonvar:f/x/numbers"}`). (2) The **arguments file**
  the worker writes ([languages.md](languages.md#wr-5)) holds the resolved
  value in rule 2's spelling. (3) A Bun script parses that file, so it sees
  the key order and the parsed doubles (`12345678901234567891` arrives as
  `12345678901234567000`). Only (3) reaches a Bun step's result; (2)'s
  spelling matters to languages whose parser tells `1000.0` from `1000`,
  such as Python (`later`). A runner without an arguments file can still be
  compared on (1) and (3).
- **Revision 2026-10-28 (oracle, batch E3b):** the finding was `DISPUTED`
  ([Q-FX-16](#q-fx-16)): as drafted, `$jsonvar:` kept key order **and**
  spelling; the validation's correction kept only the order. Run
  `args_file` of [`execution/fx-42`](../../conformance/flows/execution/fx-42)
  settles it for the correction: for the variable text
  `{"zz": 1e3, "b": 1e21, "aaa": 12345678901234567891, "big": 123456789012345678901234567890, "n": 1.50e3, "s": "A\/"}`
  the arguments file holds
  `{"v":{"zz":1000.0,"b":1e+21,"aaa":12345678901234567891,"big":1.2345678901234568e+29,"n":1500.0,"s":"A/"}}`
  (first seen in oracle run 40685013818, committed from 40686049913); rule 2
  now gives that spelling. Run `javascript` (oracle run 40690630818) and run
  `memory_or_storage` of
  [`execution/fx-10`](../../conformance/flows/execution/fx-10) (oracle run
  40685013818) record FX-42/c, FX-42/g and [FX-V5/a](#fx-v5) as stated.
- **Revision 2026-10-29 (source re-check, design request 10):** rule 2 gave
  examples of the backend's number notation but not the rule: where plain
  decimal turns into exponent form, how small numbers are written, and
  which sites show it. New rule 9 gives it, from the pinned JSON library
  and its float formatter, with a table checked against those crates. The
  examples rule 2 already gave, which the oracle recorded, follow from it
  and stay. New source-derived cases [FX-42/i](conformance.md#fx-42) (the
  `$jsonvar:` site) and [FX-42/j](conformance.md#fx-42) (a loop's result
  text). Rule 9 is source-traced; no recording yet tells a correctly
  rounded parse from the library's default one, because the recorded
  values come back the same either way.
- **Revision 2026-10-29 (validation round 1):** rule 9 said that a number
  without a fraction or exponent is an integer when it fits 64 bits, and
  that every other number becomes the nearest double. Two exceptions were
  missing: the token `-0` is parsed as negative zero and written `-0.0`,
  and a number whose double would be infinite (`1e400`) is rejected, so a
  `$jsonvar:` reference to it fails the step. Rule 9 now states both, and
  that `-0` survives only where the text is parsed as written (a variable)
  and not after a `jsonb` round trip. New source-derived cases
  [FX-42/k](conformance.md#fx-42) and [FX-42/l](conformance.md#fx-42).
- **Revision 2026-10-29 (SC3, source re-check, chain N3a):** milestone N3a
  asked whether the backend writes 10²¹ as `1e+21` or `1e21`. It writes
  `1e+21`: the float formatter's exponent strings always carry the sign,
  `+` included, with no zero padding (`zmij` `1.0.23`, `src/lib.rs:583-623`,
  written at `:1656-1702`; a probe of the pinned crate printed `1e+21`,
  `1e+20`, `1e+16`, `1000000000000000.0`, `1e-6`, `0.00001`, `1.5e-7`,
  `1.2345678901234568e+29`, `5e-324` and `1.7976931348623157e+308`). The
  spelling is also already recorded: in [FX-42/h](conformance.md#fx-42) the
  backend wrote the arguments file with `"b":1e+21` (run `args_file`,
  oracle run 40686049913), and the loop site of
  [FX-42/j](conformance.md#fx-42) uses the same writer. Rule 9 is unchanged;
  FX-42/j itself stays unrecorded ([Q-FX-7](#q-fx-7)).
- **Revision 2026-10-29 (SC4, source re-check, design request 13 and
  milestone FU6):** rule 9 said that the read of a loop's or `branchall`'s
  children fails on a number beyond a double, but not what Windmill then
  does. Added it: the flow job running the container completes failed at
  once with name `InternalErr` and the message
  `SqlErr: error occurred while decoding column 0: <library text> @<loc>`,
  without its failure module, and a child flow's parent is left to the
  zombie-flow monitor. Milestone FU6 fails such a flow with a chaining
  `InternalError` whose message is the library's text alone; Windmill's
  name, wrapper and outcome differ. Nesting past the library's limit
  fails the same way. Also added the tie rule for the shortest digits
  (FU6 asked how two equally near candidates are broken): the even last
  digit, as in Go and JavaScript. New source-derived cases
  [FX-42/m](conformance.md#fx-42) and [FX-42/n](conformance.md#fx-42).

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
- **Conformance:** none here; [WR-15/c](conformance.md#wr-15) covers the
  failure.
- **Note:** the limit's reading (a `MAX_RESULT_SIZE_MB` below 3 acts as 3)
  and the exact stored failure, `{"error": {"message": …, "name":
  "ResultTooLarge"}}` with no `step_id`, are in
  [WR-15](languages.md#wr-15).

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

**Scope:** core · **Confidence:** high (oracle-observed: `WM_FLOW_STEP_ID`) · **Status:** confirmed

- **Rule:** Each step's job carries the module id as its flow step id.
  Wrappers expose it as `WM_FLOW_STEP_ID`, and it ends up in error objects as
  `step_id` ([FX-12](#fx-12)).
  - For a failure module it is that module's id, also for the copy inside an
    inline body of a deployed flow; only a body built from raw modules
    suffixes it ([FX-34](#fx-34); corrected 2026-10-28 from the oracle
    recording of batch E3c).
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
- **Revision 2026-10-29 (SC4, recordings of milestone E1):** FX-45/a was
  recorded as stated: two steps read their own module ids from
  `WM_FLOW_STEP_ID` (`conformance/flows/client-api/job-env`, run `envs`,
  oracle run 40771900586).

<a id="fx-47"></a>
### FX-47 · Thrown non-Error values in Bun

**Scope:** core (strings and objects); later (`null`/`undefined`) · **Confidence:** high (string, oracle-observed); medium (plain object); low (`null`/`undefined`) · **Status:** confirmed

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
- **Revision 2026-10-29 (SC4, recordings of milestone E1):** FX-47/a was
  recorded as stated: a thrown `"ab"` gave
  `{"error":{"step_id":"a","extra":{"0":"a","1":"b","length":2}}}`
  (`conformance/flows/languages/bun-results`, run `thrown_string`, oracle run
  40771900586). FX-47/b stays unrecorded ([Q-FX-9](#q-fx-9)).

<a id="fx-48"></a>
### FX-48 · NUL characters at storage boundaries

**Scope:** core · **Confidence:** high (oracle-observed: case 1 for a Bun result); medium (case 2 routing); medium (case 3, source-traced) · **Status:** confirmed; drafted (case 3's response)

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
     the flow job is stored, so there is no flow result and no job. The run
     API keeps each argument's text raw and decodes only the keys, so a NUL
     in a value or a key reaches the insert of the job row, whose `jsonb`
     `args` PostgreSQL refuses. The database error is returned as is: HTTP
     **400**, `Content-Type: text/plain`, body
     `SqlErr: error returned from database: unsupported Unicode escape sequence<loc>`,
     where `<loc>` is the one location suffix of the insert in `jobs.rs`
     ([FX-50](#fx-50)). The same holds for the run-and-wait endpoints, which
     store the job the same way.
- **Evidence:**
  - `backend/windmill-queue/src/jobs.rs:995-1017` (retry policy),
    `:1094-1133` (explicit internal-error mapping at `:1127-1133`)
  - Case 3: `backend/windmill-api/src/args.rs:683-690` (a JSON object body
    is decoded into keys with raw values), `backend/windmill-api/src/jobs.rs:4818-4849`
    and `backend/windmill-api-jobs/src/execution.rs:959-994` (the run
    request pushes the job), `backend/windmill-queue/src/jobs.rs:6417-6521`
    (the job row insert, `args` bound as `jsonb`; the first statement that
    sends the args to the database), `backend/windmill-common/src/error.rs:51-52`,
    `:152-156` (a database error keeps its kind and gains a location),
    `:250-281` (that kind answers 400 with its display as plain text)
  - `backend/windmill-common/src/error.rs:59-62`, `:105-110`, `:234-239`
  - `backend/windmill-worker/src/result_processor.rs:700-740`, `:1910-1967`
  - `backend/windmill-worker/src/worker_flow.rs:2085-2129`, `:4404-4438`
  - PostgreSQL: https://www.postgresql.org/docs/current/datatype-json.html
- **Conformance:** [FX-48/a–FX-48/d](conformance.md#fx-48)
- **Note:** the other text jsonb input rejects, an unpaired surrogate escape
  or a number outside `numeric`'s range, takes the same routes; see
  [FX-51](#fx-51). Case 1 is recorded for a top-level string by
  [OR-27](oracle.md#or-27).
- **Design note (not Windmill behaviour):** windmill-lite's equivalence
  contract must say whether it reproduces cases 1–3 or deviates; the
  Windmill behaviour above is the reference.
- **Revision 2026-10-29 (oracle, batch E3e):** case 1 was recorded for a Bun
  step returning a NUL in a top-level string, in an object key and in a
  nested value: each fails with
  `Internal: Could not add completed job <job>: error returned from database: unsupported Unicode escape sequence<loc>`,
  name `InternalErr`, no `step_id`, after the step job ran for about 30 s,
  the ten 3-second insert retries (fixture
  [`oracle/nul`](../../conformance/flows/oracle/nul), runs `nul`, `nul_key`
  and `nul_nested`, oracle run 40794872025; [OR-27](oracle.md#or-27)). The
  same fixture's Python run does not fail ([OR-27](oracle.md#or-27) says
  why), so case 1 holds for results that reach storage with the escape.
- **Revision 2026-10-29 (SC3, source re-check, Q-FX-8):** case 3 said only
  that the request fails. Traced now: the run API stores the job with the
  argument text unchecked, and the database error answers HTTP 400 with its
  display as the plain-text body. New source-derived case FX-48/d. The
  confidence of case 3 rises from low to medium (traced, not recorded).

<a id="fx-50"></a>
### FX-50 · How error messages gain an `Error: ` prefix and location suffixes

**Scope:** core · **Confidence:** high (oracle-observed: the recorded shapes); high (source-traced: the layer list, the masking rule and derived values) · **Status:** confirmed; drafted (the layer list, the masking rule and derived values)

- **Rule:** Windmill's internal error kind `Anyhow` ([FX-12](#fx-12))
  displays as `Error: <inner text> @<location>`. `<inner text>` is the
  wrapped error's text with its whole cause chain; `<location>` is
  `<file>:<line>:<col>` of the place where a generic error was converted
  into that kind, with only the last segment of the file path
  (`worker_flow.rs:2451:11`).
  - Each conversion adds one layer. A text that already reads
    `Error: … @<location>` and is converted again gains a second `Error: `
    in front and a second suffix at the end, the outermost last.
  - Other kinds keep their own display: an `ExecutionErr` is its bare text
    (no prefix, no suffix), a `BadRequest` reads `Bad request: <text>`, and
    so on ([FX-12](#fx-12)); such an error converted into `Anyhow` reads
    `Error: <that display> @<location>`.

  In flows the layers come from two places: the helper that evaluates every
  boolean predicate converts the evaluator's error (`@worker_flow.rs:2451:11`),
  and the worker converts any error of a flow job it has just picked up
  (`@worker.rs:3822:13`). That gives these recorded shapes:

  | Error | Later transition ([FX-15](#fx-15) context 1, `InternalError`) | Pickup (contexts 2 and 3, `Anyhow`) |
  |---|---|---|
  | a throwing predicate | `Error: <evaluator text><loc>` | `Error: Error: <evaluator text><loc><loc>` |
  | a message the start phase builds (negative timeout, sleep) | the bare message | `Error: <message><loc>` |
  | an input transform ([FX-14](#fx-14), a step failure) | not a chaining error: `Error during isolated evaluation …`, no suffix | the same |

  `<loc>` is one suffix **including its leading space**: ` @<file>.rs:<line>:<col>`,
  or the parenthesised form ` (<file>.rs:<line>)` that some not-found
  messages end in (for example ` (jobs.rs:9074)`,
  [CF-19](control-flow.md#cf-19)); two suffixes are `<loc><loc>`. The location literal is the
  source position at the pin and moves between Windmill versions;
  [0001](../design/0001-equivalence-contract.md) §7 matches error text
  except location suffixes, and the catalogue writes each as `<loc>`
  ([conformance.md](conformance.md#execution-notation)). Other areas meet
  the same shapes, for example a predicate error at a root flow's first
  pickup ([JE-35](expressions.md#je-35)).
- **Which displays end in a suffix.** A suffix belongs to one error layer:
  the display of one internal error kind, which is a prefix, the text of
  what it wraps, and for some kinds one ` @<file>.rs:<line>:<col>` suffix.
  - **With a suffix:** `Anyhow` (`Error: `); the located internal error that
    most internal failures are built as (`Internal: `); and the wrapped
    library errors `SqlErr: `, `IoErr: `, `Utf8Err: `, `UuidErr: `,
    `SerdeJson: ` and `HexErr: `. Each location is its converting or
    building call site, last path segment.
  - **Without one:** `ExecutionErr` (bare), `Bad request: `, `Not found: `,
    an `Internal: ` error built without a location, and the other kinds.
  - The parenthesised form ` (<file>.rs:<line>)` is not a layer: it ends
    the text of a not-found error from the shared lookup helper
    (`<kind> not found at name <name> (<file>.rs:<line>)`), wherever that
    text is quoted, also inside a longer message such as a transform error
    quoting a failed fetch ([CF-19](control-flow.md#cf-19)/c).

  A layer wraps the whole display of the error it converts, so layers nest
  like brackets: the prefixes read outermost first and the suffixes
  innermost first. The executor's envelope `execution error:\n<display>`
  ([FX-51](#fx-51) rule 5) is a lead-in, not a layer; the display after it
  follows the same rule.
- **Masking the generated suffixes.** For a message that is such a display
  (the chaining errors of [FX-15](#fx-15), [FX-21](#fx-21)'s string, a
  storage failure such as [FX-48](#fx-48)'s, and the text after an
  `execution error:\n` lead-in):
  1. Count the layer prefixes at the very start of the message, one after
     another: `Error: `, `Internal: `, `SqlErr: `, `IoErr: `, `Utf8Err: `,
     `UuidErr: `, `SerdeJson: `, `HexErr: `. Call the count k. (`Error
     parsing …` and `Error during …` are not prefixes.)
  2. Mask trailing ` @<file>.rs:<line>:<col>` suffixes from the end, at
     most k of them, each as `<loc>`.
  3. Mask a parenthesised ` (<file>.rs:<line>)` only where the expected
     text writes `<loc>` for it: a case whose step reads a result the
     lookup helper cannot find, as [CF-19](control-flow.md#cf-19)/c does.
     Never mask it by searching the observed text
     ([OR-14](oracle.md#or-14) compares the same way).

  For trailing suffixes, user text cannot imitate a layer at these sites.
  The innermost text starts with fixed text (`QuickJS evaluation error: `,
  `Expected …`, `Timeout value cannot be negative`, `Error parsing result
  as …`, `Bad request: …`, a database error), so it cannot add to k. It
  ends in fixed text, in the engine's stack or debug form followed by a
  newline, or in a JSON text (`… Value was: <result text>`), none of which
  ends like a suffix. The one exception is a result text from a replaced
  `JSON.stringify`, which can be any string ([JE-15](expressions.md#je-15)).

  The parenthesised form is different: no textual rule tells a generated
  one from user text. The helper writes it as plain text, with no marker,
  and an expression's failed fetch reaches the message as an ordinary
  exception whose text is that string, so it renders exactly as a
  `throw new Error("…")` with the same text would. A user can therefore
  write `Not found: Completed Job not found at name x (user.rs:123)` in
  their own error, and a search-based mask would hide a change of `123` to
  `456` in the user's message ([FX-50/d](conformance.md#fx-50)). Which
  parenthesised forms Windmill generated is a property of the flow (which
  lookups fail), so the case's expected text must say so.

  The recorded chaining errors have as many `Error: ` prefixes as suffixes,
  but that is not general. A start-phase error from another located kind
  breaks it: a failure to push the next job ([FX-48](#fx-48) case 2) is a
  database error, which reads `SqlErr: error returned from database: <detail><loc>`
  at a later transition (no `Error: `, one suffix) and
  `Error: SqlErr: error returned from database: <detail><loc><loc>` at a
  pickup (one `Error: `, two suffixes). An internal error built without a
  location reads `Error: Internal: <text><loc>` at a pickup (two prefixes,
  one suffix). Counting only `Error: ` prefixes leaves the database
  suffix unmasked; counting every layer prefix and masking at most k covers
  all of these.
- **Derived values keep the suffixes.** A suffix is ordinary characters of
  the stored message: Windmill never strips it, marks it, or keeps the
  message in any other form. Whatever reads the error gets the whole text:
  a failure module through `error.message` ([FX-32](#fx-32)), an
  expression through `results.<id>.error.message`, a script given the
  error as an argument. Every value derived from it therefore carries the
  suffix as characters. A copy of the message ends in the suffix
  ([FX-15/b](conformance.md#fx-15) records a handler returning
  `{n: e.name, m: e.message}` with `m` = `Error: Timeout value cannot be negative<loc>`);
  a concatenation or a JSON text that embeds the message keeps it in the
  middle; a substring keeps whatever part of it was taken; and a value
  computed from the message, such as its length or a hash, depends on the
  location's digits, which change between Windmill builds. Windmill makes
  no distinction between these and user data. Only a value that holds the
  whole message unchanged, at a known place, can be compared with the
  suffixes masked by the rule above.
- **Evidence:** `backend/windmill-common/src/error.rs:74` (the `Anyhow`
  display), `:109` (its name), `:138-150` (the location is the converting
  call site, last path segment); `:45-54`, `:61`, `:65`, `:234-239` (the
  other located kinds; `internal_err` builds the located internal error),
  `:59-60` (the unlocated one); `backend/windmill-common/src/utils.rs:338-351`
  (the not-found helper's parenthesised location, written as plain text);
  `backend/windmill-jseval/src/lib.rs:703-708`, `:743-747` (a failed
  fetch's text, handed to the program behind a private marker),
  `:770-778` (the prelude throws it as `new Error(<text>)`), `:387-419`
  (any exception is rendered as `QuickJS evaluation error: ` plus its
  display);
  `backend/windmill-worker/src/worker_flow.rs:2104` (a later transition
  stores only the outermost display), `:4404-4438` and
  `backend/windmill-queue/src/jobs.rs:6417-6521` (pushing the next job; a
  database error keeps its kind); `backend/windmill-worker/src/result_processor.rs:622-627`
  (the `execution error:\n` lead-in); `backend/windmill-worker/src/worker_flow.rs:2427-2470`
  (the predicate helper; conversion at `:2451`; callers `:385`, `:703`,
  `:2403`, `:3281`, `:3844`, `:5441`), `:2085-2105` (a later transition stores
  the display text); `backend/windmill-worker/src/worker.rs:3815-3840`
  (conversion at `:3822`); built messages `worker_flow.rs:3694-3713`,
  `:4392-4396`, `:2761-2767`.
- **Conformance:** [FX-50/a–FX-50/d](conformance.md#fx-50);
  [FX-15/a–FX-15/c](conformance.md#fx-15),
  [FX-14/a](conformance.md#fx-14), [FX-8/b](conformance.md#fx-8).
- **Note:** added 2026-10-28 from the recordings of batches E3b and E3c
  (fixtures `conformance/flows/execution/fx-15`, oracle run 40685766017,
  and `fx-8`, oracle run 40686049913), which answer [Q-FX-13](#q-fx-13).
- **Revision 2026-10-28 (validation round 1):** the `<loc>` definition now
  names the parenthesised not-found form too, and the whole catalogue uses
  one convention: `<loc>` owns its leading space, so no expected text puts
  a space before it and two suffixes read `<loc><loc>`, matching the one
  space the recordings hold before each suffix.
- **Revision 2026-10-29 (source re-check, chain N1a):** a build milestone
  read the masking rule as "n leading `Error: ` prefixes, exactly n trailing
  suffixes, masked only when all n are present". That holds for every
  recorded chaining error, but not for a start-phase error of another
  located kind, such as the database error of a failed push. The finding
  now lists which displays carry a suffix, how layers nest, and the
  masking rule (count every layer prefix, mask at most that many suffixes);
  case [FX-50/a](conformance.md#fx-50) states the push failure's text. The
  layer list and the masking rule are source-traced; the push failure's
  text is not recorded ([Q-FX-8](#q-fx-8)).
- **Revision 2026-10-29 (validation round 1):** masking step 3 said to mask
  every ` (<file>.rs:<line>)` that follows the lookup helper's not-found
  text, wherever it stands. That would also hide user text: a failed
  fetch reaches the message as an ordinary thrown `Error`, so a user's own
  `throw` can write the same phrase, and nothing in the text tells the two
  apart. Step 3 now masks a parenthesised form only where the case's
  expected text writes `<loc>` for it, as [OR-14](oracle.md#or-14) already
  compares; the new negative case [FX-50/d](conformance.md#fx-50) keeps a
  forged phrase literal. The trailing-suffix count (steps 1 and 2) is
  unchanged.
- **Revision 2026-10-29 (SC3, source re-check, chain N1b):** milestone N1b
  noted that values derived from a generated error, such as a failure
  module copying `error.message`, carry Windmill's location suffixes. They
  do, as plain text, and nothing in Windmill removes or marks them; the new
  "Derived values" part says which derived values keep a maskable suffix
  and which depend on the build. How windmill-lite compares such values is
  a design question ([0011](../design/0011-differential-testing.md)); this
  finding states Windmill's side only.

<a id="fx-51"></a>
### FX-51 · Unpaired surrogate escapes and out-of-range numbers at storage boundaries

**Scope:** core (JavaScript results and transform args); later (`$jsonvar:`, raw input, other languages) · **Confidence:** medium (source-traced; oracle pending, raw input included) · **Status:** drafted

- **Rule:** PostgreSQL `jsonb` input rejects two kinds of JSON text besides
  the NUL escape of [FX-48](#fx-48): a UTF-16 surrogate escape without its
  pair (`"\ud83d"`, `"\ude00"`), with the message
  `invalid input syntax for type json`; and a number outside `numeric`'s
  range (more than 131072 digits before the decimal point or more than 16383
  after it), with `value overflows numeric format`. Windmill checks for
  neither before storing, so they follow FX-48's routes:
  1. **In a step's result.** A JavaScript string can hold an unpaired
     surrogate (for example `String.fromCharCode(0xd83d)`, or a `slice`
     through a pair). Bun's `JSON.stringify` writes it as a `\uXXXX` escape
     ([WR-13](languages.md#wr-13)), and the worker takes `result.json` as raw
     text without parsing it, so the escape reaches the completion insert.
     That insert fails and is retried 10 times at 3-second intervals, and the
     job is then failed with
     `{"error":{"name":"InternalErr","message":"Internal: Could not add completed job <job>: error returned from database: invalid input syntax for type json @<location>"}}`
     and no `step_id`. This is FX-48 case 1 with PostgreSQL's message for
     this input. A key, a nested value, or a thrown error's message or stack
     holding the surrogate fail the same way.
  2. **In args generated during a start phase.** A JavaScript transform's
     value is also written by `JSON.stringify` (quickjs-ng escapes an
     unpaired surrogate as `\uXXXX`) and kept as raw text, so pushing the
     child job fails: FX-48 case 2's chaining error, routed by
     [FX-15](#fx-15).
  3. **In the incoming flow input:** the run API keeps each argument's raw
     text, so the request fails when the flow job is stored, as in FX-48
     case 3: HTTP 400, `Content-Type: text/plain`, body
     `SqlErr: error returned from database: invalid input syntax for type json<loc>`,
     and no job. Keys are different: the run API decodes them as strings,
     and that decode validates escapes, so an unpaired surrogate in a key
     fails the request before anything is stored, with HTTP 400 and the
     body `Bad request: invalid json: <parser message>` (for example
     `unexpected end of hex escape at line 1 column 10` for
     `{"k\ud83d": 1}`).
  4. **Numbers.** A JavaScript producer cannot write a number outside
     `numeric`'s range: `JSON.stringify` writes only finite doubles (`NaN`
     and `±Infinity` become `null`), and every finite double fits. Only text
     written by hand can carry one: the incoming input of case 3, or a
     static value in a flow definition, which is stored as jsonb when the
     flow is deployed. In the incoming input the run API skips over the
     number without computing it (skipping checks only its syntax), so
     `{"x": 1e131072}` reaches the insert and the request fails with HTTP
     400 and the body
     `SqlErr: error returned from database: value overflows numeric format<loc>`.
  5. **`$jsonvar:` fails earlier, and differently.** The worker parses the
     variable's text into a typed value before writing the arguments file
     ([FX-42](#fx-42) rule 2), and that parser validates. An unpaired
     surrogate escape fails it with `unexpected end of hex escape at line L
     column C` (a high surrogate not followed by another escape) or
     `lone leading surrogate in hex escape at line L column C` (a low
     surrogate, or a high one followed by a non-low escape); a number beyond
     the double range fails with `number out of range at line L column C`.
     `L` counts lines from 1 and `C` counts bytes from the start of that
     line up to the byte where the parser stopped ([JE-42](expressions.md#je-42)
     gives the rule): `{"s": "\ud83d"}` gives column 14 (the byte after
     the escape) and `{"n": 1e400}` column 11 (the last digit).
     The resolver raises the internal error
     `Internal: Failed to parse $jsonvar value as JSON: <that message> @<location>`
     while the worker writes the arguments file, so the script never starts.
     That error does not reach storage as `InternalErr`: like every other
     executor failure it is converted into the execution envelope, so the
     step's stored result is
     `{"error": {"name": "ExecutionErr", "message": "execution error:\nInternal: Failed to parse $jsonvar value as JSON: <that message> @<location>", "step_id": "<step id>"}}`.
     This differs from case 1's `InternalErr`, which is raised after the job
     ran, by the completion insert.
     A large number within the double range is accepted and rewritten
     (FX-42).
- **Evidence:**
  - `backend/windmill-worker/src/common.rs:429-435` (`result.json` taken as
    raw text), `:194-208` and `:293-303` (`$jsonvar:` parsed into a typed
    value)
  - `backend/windmill-queue/src/jobs.rs:709-711` (the result-metadata
    decode skips other values without validating them), `:995-1017`,
    `:1094-1133` (insert, retries and error mapping, as FX-48),
    `:4886-4889` (run args kept as raw text per key)
  - `backend/windmill-worker/src/bun_executor.rs:1944-1945` (the result is
    `JSON.stringify` output), `:1977-1981` (a resolver error while writing
    the arguments file ends the job before the script starts)
  - `backend/windmill-worker/src/common.rs:122-136` (resolution before the
    arguments file), `:299` (the `$jsonvar:` parse error)
  - `backend/windmill-worker/src/result_processor.rs:621-627` (an executor
    error becomes `ExecutionErr` with the `execution error:\n` prefix and the
    step id), `:2021-2028` (its fields; `exit_code` omitted when absent),
    `:1599-1612` and `backend/windmill-queue/src/jobs.rs:883-895` (the
    completion wraps it in `error`)
  - `backend/windmill-jseval/src/lib.rs:553-570` (engine output kept as raw
    text)
  - The incoming input (rules 3 and 4): `backend/windmill-api/src/args.rs:683-690`
    (keys decoded as strings, values kept raw, a decode failure answered
    as `Bad request: invalid json: …`); `serde_json` `1.0.151`
    `src/de.rs:1217-1283` (skipping a number scans its digits only) and
    `src/read.rs:900-970` (decoding a string validates its escapes); the
    insert and the 400 answer as [FX-48](#fx-48) case 3
  - quickjs-ng `be664ca` ([JE-1](expressions.md#je-1)):
    `quickjs.c:13175-13235` (JSON quoting writes every surrogate that is not
    part of a pair as `\uXXXX`)
  - `serde_json` `1.0.151` (`backend/Cargo.lock:11151-11154`):
    `src/read.rs:900-970` (string parsing validates surrogate pairs),
    `:1025-1048` (skipping a value, as raw values do, does not),
    `src/error.rs:366`, `:378-383` (the messages)
  - PostgreSQL: https://www.postgresql.org/docs/current/datatype-json.html,
    https://www.postgresql.org/docs/current/datatype-numeric.html. Both
    PostgreSQL messages are checked against the oracle's PostgreSQL image by
    this repository's stored-form level (`conformance/jsonb/fx-48.yaml`,
    `conformance/jsonb/je-44.yaml`).
- **Conformance:** [FX-51/a–FX-51/e](conformance.md#fx-51)
- **Open:** [Q-FX-20](#q-fx-20).
- **Note:** this answers semantics request 4 of the design. Every route is
  FX-48's, which [OR-27](oracle.md#or-27) recorded for a NUL in a top-level
  string; only the database message differs.
- **Revision 2026-10-28 (validation round 1):** rule 5 said the job fails
  with the resolver's `InternalErr` message and called the stored shape
  untraced. Traced now: the resolver error is converted into the
  `ExecutionErr` execution envelope with the `execution error:\n` prefix and
  the step id, then wrapped in `error` at completion. The E3a recordings
  `je42-h` and `je42-e` of
  [`je/providers`](../../conformance/flows/je/providers) (oracle run
  40687124625) show the same envelope around other resolver failures, one of
  them an `Internal: … @common.rs:278:20` error. Rule 5 and FX-51/c now
  expect it; the surrogate input itself is still unrecorded.
- **Revision 2026-10-29 (SC3, source re-check, chain PF2):** milestone PF2
  read serde's line and column where FX-51/c gave only `C`. Rule 5 now
  says how they are counted (bytes, up to the byte where the parser
  stopped; checked against serde_json 1.0.151) and FX-51/c gives the two
  columns, 14 and 11. More positions are in [JE-42/i](conformance.md#je-42).
- **Revision 2026-10-29 (SC3, source re-check, Q-FX-20):** rule 3 was low
  confidence and left the HTTP answer to a recording. Traced now: a value
  holding an unpaired surrogate or an out-of-range number reaches the job
  row insert and gets HTTP 400 with the database error as a plain-text
  body, while a surrogate in a key is refused by the request decode with
  `Bad request: invalid json: …`. FX-51/d now states the response, and
  new source-derived case FX-51/e covers a surrogate in a value and in a
  key; the database texts are still PostgreSQL's, unrecorded through
  Windmill ([Q-FX-20](#q-fx-20)).

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

**Scope:** core (oracle interpretation); later (compatible synchronous HTTP adapter) · **Confidence:** high (oracle-observed) · **Status:** confirmed

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
- **Revision 2026-10-29 (oracle, batch E3e):** recorded as stated: a flow
  stopped by `stop_after_if` with `skip_if_stopped` completes `skipped` with
  result `3`, and run-and-wait answers HTTP 422 with the body `3` (fixture
  [`execution/fx-36-http`](../../conformance/flows/execution/fx-36-http), run
  `skipped`, oracle run 40794872025).

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

**Scope:** core (boundary ordering, when references are supported); resolver per the expressions and client-API areas · **Confidence:** high (oracle-observed) · **Status:** confirmed

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
- **Note:** this preserves insertion order only; numeric and string
  spelling is not preserved ([FX-42](#fx-42) rule 2, settled by the oracle).
  Coordinate the variable fixture with [expressions.md](expressions.md),
  [languages.md](languages.md) and [client-api.md](client-api.md).
- **Revision 2026-10-28 (oracle, batch E3b):** FX-V5/a is recorded as
  stated: a `$jsonvar:` object reaches a Bun script in the variable's key
  order (`zz, b, aaa, big, n, s`), while a static object argument
  `{aaa: 1, zz: 2}` arrives in jsonb order (`zz, aaa`) (run `javascript` of
  [`execution/fx-42`](../../conformance/flows/execution/fx-42), oracle run
  40690630818). The note no longer calls the spelling disputed.

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
  **Re-scoped 2026-10-29 (SC4).** The loop half is recorded: milestone NI's
  fixture `conformance/flows/control-flow/cf-27` (oracle run 40852609901)
  ran a parallel loop's 3 s iteration to completion under `timeout: 1`
  (run `fx24_e`, [FX-24/e](conformance.md#fx-24)), the iterations of a
  sequential loop with a two-step body likewise (run `c`,
  [CF-27/c](conformance.md#cf-27)), and cut a collapsed body job at the
  loop's 1 s (run `fx24_d`, the deployed half of FX-24/d). What remains is
  the branch and the `flow` module: a `branchone` or `branchall` with
  `timeout: 1` around a 3 s body, and a `flow` step with `timeout: 1`
  calling a 3 s subflow; the source predicts no timeout in either. Still
  open.

<a id="q-fx-2"></a>
- **Q-FX-2** · affects [FX-24](#fx-24) · `timeout: 0` on process and
  in-process executors counts as job-specific. Does the step fail at once,
  or can an instant step finish first? *Settled by:* an oracle run of a Bun
  step `return 1` with `timeout: 0`; do not assume a deterministic ordering
  for the race.

<a id="q-fx-3"></a>
- **Q-FX-3** · affects [FX-19](#fx-19), [FX-30](#fx-30), [FX-31](#fx-31),
  [FX-V4](#fx-v4) · **Re-scoped 2026-10-28.** The matrix itself is recorded:
  FX-19/d to FX-19/i hold on the oracle (batch E3b,
  [`execution/fx-19`](../../conformance/flows/execution/fx-19)), including a
  tolerated error-stop at the final module entering the failure module once.
  What remains is what that failure module sees: `previous_result` the
  `EarlyStopError` and `results.<stopped id>` the stored success value.
  *Settled by:* an oracle run of [FX-V4/a](conformance.md#fx-v4).
  *2026-10-29 (SC4):* the new source-derived case
  [FX-30/b](conformance.md#fx-30) (a due retry before the last-module
  tolerance) joins it; both are requested for batch E3f
  ([docs/design/README.md](../design/README.md)). Still open.

<a id="q-fx-4"></a>
- **Q-FX-4** · affects [FX-15](#fx-15), [FX-24](#fx-24) ·
  **Re-scoped 2026-10-28.** The FX-15 half is answered: batch E3c recorded
  the `Anyhow` name, the doubled prefix and suffixes, the failure-module run
  for a root flow and the bypassed failure module for a child flow
  ([FX-15/b, FX-15/c](conformance.md#fx-15), [FX-50](#fx-50)). What remains
  is FX-24/b's pickup path. Original question: pickup-path
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
- **Q-FX-6** · affects [FX-40](#fx-40) · **Answered 2026-10-28.** Yes: a
  flow whose failure module ran successfully without `recover` fails, caches
  the failure module's result, and an identical call within the TTL
  completes successfully with it and runs no step (run `b` of
  [`execution/fx-40`](../../conformance/flows/execution/fx-40), oracle run
  40685057858; [FX-40/b](conformance.md#fx-40)). The question was: is a
  non-recovered failure-module result cached and then served as a success?

<a id="q-fx-7"></a>
- **Q-FX-7** · affects [FX-42](#fx-42), [FX-V5](#fx-v5) · **Re-scoped
  2026-10-28.** Recorded in batch E3b: `$jsonvar:` order preservation
  (FX-V5/a), a static object's jsonb order (FX-42/g) and the in-memory
  handoff's producer order (FX-42/c). Still to record: the nested key order a
  Bun step sees for a stored producer value (FX-42/a), an engine-built
  object stored as an argument (FX-42/b), the `flow_input` shortcut's
  whole-suffix key (FX-42/e), Python number types after exponent rewriting
  (`1e21` becoming an `int`, FX-42/f), and how PostgreSQL's numeric
  rewriting handles scale (for example `1.50e3`) in a stored value.
  *Settled by:* oracle runs of FX-42/a, FX-42/b, FX-42/e and FX-42/f
  ([conformance](conformance.md#fx-42)) and a stored `1.50e3`.
  *2026-10-29:* batch E3e recorded none of these. Rule 9's number notation
  (design request 10) adds two source-derived cases to record, FX-42/i and
  FX-42/j. Adding a value such as `0.028735999999999998` to FX-42/j's
  array would also show that the parse is correctly rounded: the library's
  default parse would bring it back as `0.028736`. Still open.
  *2026-10-29 (SC3):* nothing here is left for the Windmill source: the
  expectations are traced, and the scale of a stored `1.50e3` is
  PostgreSQL's behaviour. The backend's `1e+21` spelling, which milestone
  N3a asked about, is already recorded by [FX-42/h](conformance.md#fx-42)
  ([FX-42](#fx-42), revision of 2026-10-29). The runs above are requested
  for batch E3f ([docs/design/README.md](../design/README.md)).

<a id="q-fx-8"></a>
- **Q-FX-8** · affects [FX-48](#fx-48), [FX-50](#fx-50) · **Re-scoped
  2026-10-29.** Case 1 is recorded: batch E3e's `conformance/flows/oracle/nul`
  runs `nul`, `nul_key` and `nul_nested` (oracle run 40794872025) give the
  database detail `error returned from database: unsupported Unicode escape sequence`
  and a step job of about 30 s ([FX-48/a](conformance.md#fx-48)). What
  remains is the push-failure message of case 2, for which
  [FX-50/a](conformance.md#fx-50) now states the source's prediction, and
  the HTTP status when the incoming input contains NUL (case 3). Original
  question: NUL: the exact database detail and
  the runtime of the 10 × 3 s insert retries (case 1); the push-failure
  message (case 2); the HTTP status when the incoming input contains NUL
  (case 3). *Settled by:* oracle runs of
  [FX-48/b, FX-48/c](conformance.md#fx-48) and
  [FX-50/a](conformance.md#fx-50), plus an incoming-input probe.
  **Re-scoped 2026-10-29 (SC3).** Case 3 is answered from the source: the
  run request is refused with HTTP 400, `Content-Type: text/plain` and the
  body `SqlErr: error returned from database: unsupported Unicode escape sequence<loc>`,
  and no job is created ([FX-48](#fx-48) case 3, new
  [FX-48/d](conformance.md#fx-48)). What remains for the oracle is the
  push-failure message of case 2 ([FX-48/b](conformance.md#fx-48),
  [FX-48/c](conformance.md#fx-48), [FX-50/a](conformance.md#fx-50)) and,
  optionally, FX-48/d itself; requested for batch E3f
  ([docs/design/README.md](../design/README.md)).

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
- **Q-FX-11** · affects [FX-49](#fx-49), [FX-22](#fx-22) · **Answered
  2026-10-29.** The correction holds: a `skip_expr` that turns true between
  two iterations of a sequential loop at module 0 ends the loop after its
  first iteration, and the flow completes successfully with `[1]`, for a
  one-step and a two-step body; a throwing one fails the flow with the
  chaining `InternalError` and leaves the loop in progress (batch E3e,
  fixture `conformance/flows/execution/fx-49`, runs `flip`, `flip_body` and
  `throws`, oracle run 40794872025; [FX-49/a, FX-49/b](conformance.md#fx-49)).
  FX-49 is now `confirmed`. Original question: what happens when
  a cursor-0 `skip_expr` re-evaluation flips to true, or throws, during a
  sequential loop at module 0? As drafted, the fixture was predicted to
  evaluate the expression three times with the outcome open; with `v` bound
  to `flow_input.iter.value`, the source predicts two evaluations, loop result
  `[1]` and a successful completion (trace in [FX-49](#fx-49)). Evaluating
  `skip_expr` once is a proposed windmill-lite deviation, not the Windmill
  rule. *Settled by:* an oracle run of [FX-49/a](conformance.md#fx-49) plus a
  throwing variant. Status: closed.

<a id="q-fx-12"></a>
- **Q-FX-12** · affects [FX-36](#fx-36) · **Answered 2026-10-29.** Yes:
  run-and-wait answers `[201, null, null, null, 7]` with HTTP 201 and the
  body `7` (batch E3e, fixture `conformance/flows/execution/fx-36-http`, run
  `array`, oracle run 40798097155; [FX-36/h](conformance.md#fx-36)). The
  record's derived decoder takes an array of exactly five elements by
  position; any other length fails to decode ([FX-36/i](conformance.md#fx-36),
  source and decoder). The same batch recorded FX-36/a–g and showed the
  body is the stored `jsonb` text, which corrects FX-36/b's member order.
  FX-36 is now `confirmed`. Original question: does the typed
  response-metadata decoder accept a JSON array positionally (for example
  `[201, null, null, null, 7]`)? *Settled by:* checking the decoder's
  behaviour for arrays, or an oracle run-and-wait with such a result. Later
  scope. Status: closed.

<a id="q-fx-13"></a>
- **Q-FX-13** · affects [FX-15](#fx-15), [FX-21](#fx-21), [FX-28](#fx-28),
  [FX-V1](#fx-v1), [FX-48](#fx-48) · **Answered 2026-10-28** for the
  chaining and transform texts: [0001](../design/0001-equivalence-contract.md)
  §7 decided to match all error text except location suffixes and stacks,
  and batches E3b and E3c recorded the texts. How the `Error: ` prefixes
  and location suffixes arise is [FX-50](#fx-50); the recorded texts are in
  [FX-14](#fx-14) and [FX-15](#fx-15). The database text of
  [FX-48](#fx-48) stays with [Q-FX-8](#q-fx-8). Original question:
  exact engine, database and runtime
  error suffixes. The structural rules do not depend on them. *Settled by:*
  capturing them on the oracle only where windmill-lite wants to match more
  than `name` plus the documented prefix.

<a id="q-fx-14"></a>
- **Q-FX-14** · affects [FX-29](#fx-29), [FX-17](#fx-17), [FX-32](#fx-32) ·
  **Re-scoped 2026-10-28.** The retry-skip fixtures are recorded as
  predicted: [FX-17/b](conformance.md#fx-17) by batch E3b and
  [FX-32/b](conformance.md#fx-32) by batch E3c, which also showed that the
  skipped failure module is not flagged `skipped`. What remains is
  [FX-29/a, FX-29/b](conformance.md#fx-29). Original question:
  the carried-over retry counter and the retry-skip and failure-module
  retry-skip fixtures are fully traced in source but not recorded.
  *Settled by:* oracle runs of [FX-29/a, FX-29/b](conformance.md#fx-29),
  [FX-17/b](conformance.md#fx-17) and [FX-32/b](conformance.md#fx-32).
  *2026-10-29 (SC4):* the carried-over list of failed attempts is recorded
  after a tolerated failure: milestone N5's run `exhausted_continue`
  (fixture `conformance/flows/control-flow/subflow-retry`, oracle run
  40915682847) is the new [FX-29/c](conformance.md#fx-29).
  **Answered 2026-10-29 (SC4).** FX-29/a and FX-29/b were recorded by
  milestone N2 (fixture `conformance/flows/execution/fx-29`, runs
  `carries_over` and `reset_by_success`, oracle run 40818457347), as
  predicted, so every case this question listed is recorded
  ([FX-29](#fx-29)). Status: closed.

<a id="q-fx-15"></a>
- **Q-FX-15** · affects [FX-10](#fx-10), [FX-V6](#fx-v6) · **Answered
  2026-10-28.** The competing source reading holds: `previous_result` is
  bound when its name, a preceding-id spelling or `error` occurs in the
  text, and then the preceding id reads memory. Batch E3c recorded it in a
  failure module ([FX-V6/a](conformance.md#fx-v6), fixture
  `conformance/flows/execution/fx-v6`, oracle run 40685766017) and batch E3b
  without one (fixture `conformance/flows/execution/fx-10`, run
  `memory_or_storage`, oracle run 40685013818). FX-10 and FX-V6 are now
  `confirmed`. Original question (was a core blocker):
  when does an engine `results` lookup of the preceding id read memory
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
  [expressions.md](expressions.md) and confirmed on the oracle. Status:
  closed.

<a id="q-fx-16"></a>
- **Q-FX-16** · affects [FX-42](#fx-42), [FX-V5](#fx-v5) · **Answered
  2026-10-28.** Only the insertion order: the resolved value is written out
  again, so `1e3` becomes `1000.0`, `1e21` becomes `1e+21` and escapes and
  whitespace are rewritten (run `args_file` of
  [`execution/fx-42`](../../conformance/flows/execution/fx-42), oracle run
  40686049913; [FX-42](#fx-42) rule 2, [FX-42/h](conformance.md#fx-42)).
  The question was: does `$jsonvar:`
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
- **Q-FX-17** · affects [FX-40](#fx-40), [FX-46](#fx-46) · **Answered
  2026-10-28.** Yes, on a deployed flow in every variant recorded: an inline
  rawscript body with or without an explicit `path`, an unhashed by-path
  script with its own TTL, and a hash-pinned script under the loop's TTL.
  Every such body job has a runnable id, which the cache key uses instead of
  the path (batch E3d: runs `d-raw`, `d-raw-path`, `d-script` of
  [`execution/fx-40`](../../conformance/flows/execution/fx-40), oracle run
  40686532046, and `d-hash` of
  [`execution/fx-40-hash`](../../conformance/flows/execution/fx-40-hash),
  oracle run 40692334073; [FX-40](#fx-40)). The question was: can collapsed
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
- **Q-FX-18** · affects [FX-1](#fx-1), [FX-4](#fx-4), [FX-5](#fx-5),
  [FX-11](#fx-11), [FX-23](#fx-23), [FX-24](#fx-24), [FX-25](#fx-25),
  [FX-26](#fx-26), [FX-27](#fx-27), [FX-28](#fx-28), [FX-45](#fx-45),
  [FX-47](#fx-47), [FX-V1](#fx-v1), [FX-V2](#fx-v2) ·
  **Re-scoped 2026-10-28.** Recording batches E3b and E3c recorded the cases
  of FX-3, FX-6 to FX-10, FX-12 to FX-22, FX-30 to FX-35, FX-37, FX-38 and
  FX-40 that this entry used to list, among them FX-7/d's engine text and
  FX-22's `skip_expr` outcome; those cases now name their recordings. What
  remains is every case of the findings above that is still
  `source-derived` and not named in another question: FX-1/a, FX-4/a (the
  virtual step on deployed flows), FX-5/a, FX-11/a,b, FX-23/a–c, FX-24/a and
  FX-24/c–g, FX-25/a, FX-26/a–e, FX-27/a, FX-28/a–c, FX-45/a, FX-47/a,b,
  FX-V1/a,b and FX-V2/a. *Settled by:* recording each case on the
  oracle and updating its provenance. *2026-10-29:* batch E3e recorded
  FX-V3/a over run-and-wait (fixture
  `conformance/flows/execution/fx-36-http`, run `skipped`, oracle run
  40794872025), so it left this list; the other cases above are still
  unrecorded.
  **Re-scoped 2026-10-29 (SC3).** Milestone N2 found three of these
  unrecordable as the harness stands; the source now says how to record
  each. FX-26/c–e: the retry job is queued at once with its
  `scheduled_for`, so a recording reads the delay from the queued job and
  then cancels the flow, without waiting ([FX-26](#fx-26)). The jitter
  distribution ([FX-26/f](conformance.md#fx-26)) is fixed by the source,
  but single values come from an unseeded generator, so a recording can
  only sample delays and check they fall in its support. FX-27/a's second
  `skip_if` evaluation is visible only through a value that differs
  between the evaluations; [OR-24/c](conformance.md#or-24) already
  recorded one, so what FX-27/a still needs is its args-reuse half
  ([FX-27](#fx-27)). The recordings are requested for batch E3f
  ([docs/design/README.md](../design/README.md)).
  **Re-scoped 2026-10-29 (SC4).** Much of this list was recorded by the
  engine milestones and never folded in; the cases are now
  `oracle-observed`, each with its recording: FX-23/a–c, FX-24/a, /c, /f,
  FX-25/a, FX-26/a, /b, FX-27/a (its args-reuse half, which is all it still
  needed) and FX-28/a–c by milestone N2 (fixtures
  `conformance/flows/execution/fx-23` to `fx-28`, oracle runs 40818457347
  and 40819992740); FX-24/e and the deployed half of FX-24/d by milestone NI
  (`conformance/flows/control-flow/cf-27`, oracle run 40852609901); FX-45/a
  and FX-47/a by milestone E1 (`conformance/flows/client-api/job-env` and
  `conformance/flows/languages/bun-results`, oracle run 40771900586). They
  agree with the catalogue, except that FX-28/b and FX-28/c recorded a
  string with two `Error: ` prefixes and two location suffixes, which
  [FX-28](#fx-28) now explains. Still to record: FX-1/a, FX-4/a (its `a-v`
  status entry is [Q-OR-9](oracle.md#q-or-9)'s), FX-5/a, FX-11/a, FX-11/b,
  FX-24/g, FX-26/c–f, FX-47/b, FX-V1/a, FX-V1/b and FX-V2/a; the preview
  half of FX-24/d is outside the reference configuration. Still open.
  *2026-10-29 (SC4, milestone N5):* the new source-derived case
  [FX-38/b](conformance.md#fx-38) ([FX-38](#fx-38): a step canceled while
  it waits to start) joins this list; requested for batch E3f.
<a id="q-fx-19"></a>
- **Q-FX-19** · affects [FX-33](#fx-33) · **Answered 2026-10-28.** The
  correction holds: a successful failure module returning `[true]` makes
  the flow succeed with result `[true]`; `[false]`, `[null]`, `[]`,
  `[true,false]` and `{"recover":"true"}` leave it failed (batch E3c,
  fixture `conformance/flows/execution/fx-33`, oracle run 40685766017).
  FX-33 is now `confirmed`. Original question (raised by the review of this
  reference, finding SR-1, not by the module study): does a
  successful failure module recover the flow only when its result is an
  object with boolean `recover: true` (as drafted), or whenever the result
  decodes as the one-field recovery record, which serde also accepts as a
  one-element array, so that `[true]` recovers and the flow succeeds with
  result `[true]` (correction)? Source pointers:
  `backend/windmill-worker/src/worker_flow.rs:298-301` (the recovery record),
  `:409-412` (decode of the raw result; any error counts as not recovered),
  `:2006-2059` (flow success flag and preserved result), `:2140-2153`
  (parent notification, unchanged); `backend/Cargo.lock:11067` (`serde`
  1.0.229), `:11151` (`serde_json` 1.0.151). *Settled by:* oracle runs of
  [FX-33/c–g](conformance.md#fx-33) (`[true]`, `[false]`, `[null]`, `[]`,
  `[true,false]`) next to the object controls FX-33/a–b. All seven were
  recorded and agree with the decoder probe.

<a id="q-fx-20"></a>
- **Q-FX-20** · affects [FX-51](#fx-51), [FX-48](#fx-48) · Unpaired
  surrogate escapes and out-of-range numbers at the jsonb boundary are
  source-traced only. Record: a Bun step returning an unpaired high and an
  unpaired low surrogate (the `InternalErr` text, PostgreSQL's
  `invalid input syntax for type json`, and the insert retries' duration); a
  transform producing one at root pickup and at a later transition; a
  `$jsonvar:` variable holding one, and one holding `1e400`; and a run
  request whose input holds `1e131072` (the HTTP status and body). Batch E3e
  (design record 0012) is the natural home. *Settled by:* oracle runs of
  [FX-51/a–FX-51/d](conformance.md#fx-51). *2026-10-29:* batch E3e did not
  record this; its storage runs were the NUL ones of
  [OR-27](oracle.md#or-27), which confirm FX-48's route for a key and a
  nested value, not PostgreSQL's surrogate or numeric messages. Still open.
  **Re-scoped 2026-10-29 (SC3).** The run request's answer is traced: a
  value holding `1e131072` or an unpaired surrogate reaches the job row
  insert and gets HTTP 400 with the database error as a plain-text body
  (`SqlErr: error returned from database: value overflows numeric format<loc>`,
  or `… invalid input syntax for type json<loc>`), while a surrogate in a
  key is refused earlier with `Bad request: invalid json: …`
  ([FX-51](#fx-51) rules 3 and 4, [FX-51/d](conformance.md#fx-51)). What
  remains is to record PostgreSQL's messages through Windmill and the
  insert retries' duration (FX-51/a–d); requested for batch E3f.

<a id="q-fx-21"></a>
- **Q-FX-21** · affects [FX-40](#fx-40) · open (oracle, `later`) · The
  cache rules milestone N4 asked about are traced in the source but not
  recorded: the store conditions (a failure tolerated at the last module
  is stored, an error stop is not, a container at the last module stores by
  its own outcome, a hit of a stopped run is not `skipped`), argument
  equality under `jsonb`, a hit that does not resolve `$var:`, S3-aware
  hashing without workspace object storage, and `stop_after_if` on a hit.
  With workspace object storage, S3-aware hashing also goes through a
  storage client that is closed source on CE, so only a recording can show
  it. Added 2026-10-29 (SC4). *Settled by:* oracle runs of
  [FX-40/e to FX-40/l](conformance.md#fx-40) in the reference session,
  checking each run's flow and step results, the step jobs created and the
  cache log line; and, in a session with workspace object storage on a
  local S3-compatible store, a step with `cache_ttl` and
  `cache_ignore_s3_path` whose argument is an S3 object, recording a hit,
  a miss after the object's content changes, and a hit for a second key
  holding a copy of the same content (requested for batch E3f).

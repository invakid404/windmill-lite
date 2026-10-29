# internal/engine

**Current state:** the contracts of milestone K1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)), the
mapping of a Bun step's outcome to Windmill's stored value with the
entrypoint's selection (milestone C3a), the engine core of milestone
N1a, which runs a compiled program's flows sequentially, with the errors
of milestone N1b (`continue_on_error`, the failure module and recovery,
the error shapes, chaining errors, the final result and the cancellation
outcome), the for loops of milestone N3a (sequential, collapsed and
parallel, with `parallelism`), the while loops, `branchone` and
`branchall` of milestone N3b, the subflows by path and the failure
modules in bodies of milestone N3c, the platform providers a run reads
(milestone PF2): a leaf's argument resolution, `flow_env`'s resolution and
the expression helpers `variable()` and `resource()`, the waits and
time of milestone N2: `sleep`, the step timeout and its messages, retries
with their arithmetic, jitter and argument reuse, deadlines and the clock,
(milestone PF3) each run's place in the job registry, the helper
`flow_user_state()`, a root script's run as a script job and the default
dispatcher, and the integration of milestone NI: N2's waits and retries,
the cancellation and failures inside containers, and the evaluation timing
of container-level fields. This README
covers `internal/engine` and its `testdata/`. The package holds the seams
that the flow interpreter is built against (the step executor, the hook
interface with no-op defaults, the precedence of host signals, the run
trace) and the interpreter itself. `skeleton.go` is what remains of the
walking skeleton (W1): the loader of W1's hand-written bundle, which runs
its flows on the engine core until F3a moves host mode onto `wl`. The
harness that executes steps is [internal/harness](../harness/README.md).

## Contracts

| File | Contents | Built on by |
|---|---|---|
| `executor.go` | `StepExecutor`, `Invocation`, `StepOutcome`, `CancelReason`, `Reporter` and `SettleKind`: one leaf run over the harness protocol ([ADR 0004](../../docs/design/0004-harness-protocol.md)), and its settlement | C1 implements it; W1, C3a, N1a call it; PF1's facade wraps it |
| `outcome.go` | `StoreOutcome`, `Stored`, `FailureValue`, `ManualFailure`, `LogTail`; `Entrypoint` and `ErrInvalidEntrypoint` (below); `TimeoutError`, a step's timeout text by boundary (C3n) | C3a, N1b; the step-case runner (`wltest check`) and the engine call them |
| `settle.go` | `RunSettlement`: counts a run's unsettled invocations and says once when the run has settled | N1a calls `Hooks.OnRunSettled` from it |
| `hooks.go` | `Hooks` (H1–H3, H6–H9, H12 of [ADR 0008](../../docs/design/0008-host-hooks.md)), `NoHooks`, `HookLatchError`, `Site`, `Run`, `Latch` (H5), `Clock` and `RealClock` (H10); `StaticIntercepts`, H1's no-code form | N1a calls them; F2a, F2b and F2c expose them to hosts |
| `waits.go` | H10's `Jitter`, `SeededJitter` and `RecordClock`; the values of `sleep` and `timeout` and the retry schedule (below) | N2 calls them; F2b exposes the clock and the jitter to hosts |
| `precedence.go` | `Start`, `Completion`, `AfterContinue` and their `Verdict` | N1a's decision list |
| `trace.go` | `Trace`, `StepRecord`, `ChildFlow`, `Origin`, `Late`, `Warning`, `Span` (H9; run spans of [ADR 0011](../../docs/design/0011-differential-testing.md)). The trace a run returns is a snapshot nothing changes later; late output goes to the snapshot `OnRunSettled` receives | N1a fills it; wl returns it |
| `engine.go`, `run.go`, `values.go` | `Engine`, `Config`, `New`, `RunRequest`, `Cancellation`, `ErrUnsupported`, `ErrPreWriteDeadline`, `FatalError`: the engine core (below) | the flow runner of `wltest check`; `Skeleton`; wl (F1) |
| `providers.go` | What a run reads of the platform (PF2, below): the expression helpers, `flow_env`'s resolution per transition, a resolution failure | the engine core |
| `dispatch.go` | `RunScript`, `ScriptRequest`, `ScriptResult`: a root script's run as a script job; `Dispatcher`, `NewDispatcher`: the default dispatcher of [ADR 0007](../../docs/design/0007-platform-interface.md) (PF3, below) | a host's `platform.Services`; `wltest check` |
| `env.go` | `addScriptEnvs`: a path script's own `envs` in a Bun step's environment, under every variable the worker sets later; none for a native step ([WR-22](../../docs/semantics/languages.md#wr-22), [WR-45](../../docs/semantics/languages.md#wr-45); FU5) | `run.go`, at each launch |
| `frame.go`, `loop.go`, `branch.go`, `subflow.go`, `serde.go` | the flows a run executes (the root, its containers' child flows and its subflows by path), their results scopes, for and while loops, branches and subflows (below); the texts of Windmill's JSON library | N3a, N3b, N3c |
| `skeleton.go` | `Skeleton`, `NewSkeleton`, `SkeletonRun`: W1's bundle loader over the engine (below) | W1's `windmill-lite host`, until F3a |

Site identities are `conformance.SitePath` values. Run statuses are
`conformance.RunStatus` values, including the windmill-lite-only `latched`
and `halted`. Traces, hooks and observations therefore share one vocabulary.

## Hooks

- **Errors latch.** A non-nil error from any hook latches the run (H5),
  unless it is the run's own cancellation: `HookLatchError` drops an error
  that matches the ended context's error or cause, so a hook giving up on a
  canceled run cannot turn the cancellation into a latch. A hook that wants
  to fail a module answers `Intercept` with an error outcome instead.
- **First latch wins.** `Latch` (H5, reached through `Run.Latch`) keeps the
  first error latched and drops later ones, and is safe for concurrent use.
- **Concurrent calls.** The iterations of a parallel loop and the
  branches of a parallel `branchall` run on goroutines of their own, so
  one run's hooks are called concurrently: a `Hooks` implementation must
  be safe for concurrent use.
- **Not hooks in this interface:**
  - H4 (cancellation and budgets) reaches the engine as the run's context,
    whose deadline is the hard one, and `RunRequest.PreWriteDeadline`
    (N2, below);
  - H10 (clock and randomness) reaches it as `RunRequest.Clock` and
    `RunRequest.Jitter` (N2, below);
  - H11 (host scopes) lives in the Bun harness.
- **Left for later milestones:** H13 (per-run platform providers) arrives
  with the platform milestones.

## Precedence

The three functions are pure. `precedence_test.go` states them case by
case and checks the rules below over every combination of signals.

1. **`Start`**, before a module, iteration, branch or retry attempt starts:
   the latch, then cancellation and deadlines, then `haltBefore` (at a
   module's first attempt, never for a failure module). Then Windmill's own
   start checks run, with interception at launch.
2. **`Completion`**, when a module completes: the latch, then cancellation,
   then fatal error names (H7). Then Windmill's continue decision runs.
3. **`AfterContinue`**, after that decision: `haltAfter` ends the run halted
   once the module has its final outcome and the run would start something
   other than a failure module. A container's completion is final only after
   its last iteration or branch, and a module's only when no retry is due;
   `ContinueSignals.Final` says so, and its zero value never halts. A
   completion that ends the run anyway keeps Windmill's status.

Cancellation has one exception
([FX-38](../../docs/semantics/execution.md#fx-38)): an error stop that
coincides with the cancellation, in a flow with a failure module, still goes
to Windmill's continue decision, and what that decision asks for still
starts: a retry when one is due, the next module under `continue_on_error`,
or else the failure module
([FX-19](../../docs/semantics/execution.md#fx-19)). The run then ends
canceled at its next completion. A halt never turns a canceled run into a
halted one: a halt that falls on such a start stops it, and the run ends
canceled.

## Step outcomes

A `StepOutcome` mirrors the harness's `result` frame (`ok`, `error`,
`escaped`, `empty`) before the engine maps it to Windmill's stored value.

- **Same type for hooks.** A host plugin that answers for a module (H1)
  returns the same type, so its result cannot be told apart from a step's.
- **Abandonment.** When the invocation's context ends, `Execute` returns at
  once with the context's cause. The engine names the cause with a
  `CancelReason` (`timeout`, `canceled`, `run_ended`). Anything the
  abandoned invocation produces later reaches the trace through `Reporter`
  as `Late` (deviations D2 and D9). In `fresh` mode the harness process is
  killed instead, so the invocation settles with it and nothing comes late.

## Stored values and the entrypoint

`StoreOutcome(out, stepID)` is the outcome table of
[ADR 0004](../../docs/design/0004-harness-protocol.md): what Windmill
stores for a Bun step, which is also what the flow sees of a failed one.

| Outcome | Stored | Origin |
|---|---|---|
| `ok` | the result's producer text; an object whose top-level `wm_failure` is a string fails, kept as it is with `"error": {"name": "ManualFailure", "message": <wm_failure>}` put in, in place of an `error` member or else last (`ManualFailure`, [WR-17](../../docs/semantics/languages.md#wr-17), [FX-13](../../docs/semantics/execution.md#fx-13)); a `null` marker marks nothing, and a `wm_failure` that is not a string or a `wm_labels` that is not `null` or an array of strings (a `null` element included) disables both | — or user |
| `error` | `{"error": E}` with the wrapper's object `E` (it carries `step_id` itself, [WR-18](../../docs/semantics/languages.md#wr-18)), unless it is a pre-shaped `ManualFailure`, kept as it is ([WR-21](../../docs/semantics/languages.md#wr-21), [WR-V5](../../docs/semantics/languages.md#wr-v5)); text that is not JSON is `Non serializable error: <text>` | user, or the outcome's `Origin` when set: `facade` for the facade's `UnsupportedPlatformCall` ([ADR 0007](../../docs/design/0007-platform-interface.md), the 501 policy), `harness` for the errors the harness generates in place of the worker's ([WR-37](../../docs/semantics/languages.md#wr-37)'s signature error; a native job's transpile and load failures and `//proxy` refusal, [WR-43](../../docs/semantics/languages.md#wr-43)), which the `result` frame names |
| `escaped`, by a signal | `execution error:\nprocess terminated by signal: <signal>`, `ExecutionErr`, `step_id` ([WR-4](../../docs/semantics/languages.md#wr-4), [WR-37](../../docs/semantics/languages.md#wr-37)) | harness |
| `escaped`, by an exit | `exit code for "bun run": <code>, last log lines:\n<tail>`, `ExecutionErr`, `step_id`, `exit_code` ([WR-20](../../docs/semantics/languages.md#wr-20)) | harness |
| `empty` | `Internal: Result of job is invalid json (empty)`, `InternalErr`, no `step_id` ([WR-V2](../../docs/semantics/languages.md#wr-v2)), and no Rust location (D14) | harness |

`step_id` is added only for a flow step (`stepID` not empty). The tail is
`LogTail`'s: the last 600 characters, what follows the last `CODE EXECUTION
---` marker, without ANSI escapes, trimmed. Windmill's size limits on a
result ([WR-15](../../docs/semantics/languages.md#wr-15), `later`) are not
applied here.

`Entrypoint(args)` is the export an invocation calls
([WR-12](../../docs/semantics/languages.md#wr-12)): the value of
`_ENTRYPOINT_OVERRIDE` when the argument object carries it, as the flow
preprocessor step's always does, and `main` otherwise. An override that is
not a string matching `^[A-Za-z_][A-Za-z0-9_]*$` is refused with an error
wrapping `ErrInvalidEntrypoint`, whose text begins `Invalid entrypoint
override`, before any wrapper runs; the engine fails the step with it as a
pre-run error (below). The harness selects WR-V1's fallback from the same
argument.

`outcome_test.go` checks every row, the bypass's edges, the
`wm_failure` marker's, the tail and the entrypoint's rules.

## Settlement

An invocation settles when its entrypoint call can run no more code, and
`Reporter.Settled` says so exactly once per `Execute` call:

| `SettleKind` | When | Relative to `Execute` returning |
|---|---|---|
| `completed` | the call returned or threw | before, normally; later for an abandoned call, or one whose outcome was fixed before it ended (an intercepted `process.exit`, D11, or an attributed process-level error that failed it as `escaped`) |
| `not_started` | the invocation never reached a harness | before |
| `process_exited` | the harness process ended before the call did | before when the invocation was in flight; later when it had been abandoned or its outcome was fixed early |

- **Warnings.** `Warning` hands over a process-level error no invocation
  owned (code `unattributed_error`), what the invocation still had when
  it settled (code `left_behind`, its `Left` counting the timers, requests
  and child processes by kind), which the settle-time fence then revoked
  (D9), or what the native boundary handed a native invocation but does
  not track (code `native_untracked`, its `Untracked` counting by kind,
  D5). None changes the outcome.
- **Order.** One invocation's `Reporter` is never called from two goroutines
  at once. Its calls come in this order: `Late` and `Warning`; at most one
  `Late` with `Final` set, which says how a late-ending call ended;
  `Settled`; then only non-final `Late`, which is output of work the call
  left running (D9).
- **Not settlement.** Abandonment is not settlement, and neither is a late
  error without `Final`.
- **Resources.** `RunSettlement` counts each tracked invocation until its
  first `Settled` and drops duplicates. Once `End` has been called and
  nothing is unsettled, it calls its callback once; that is where the engine
  calls `OnRunSettled` and releases what step code can reach (ADR 0004 host
  mode item 5).
- **Tests.** `settle_test.go` drives a fake executor through completion,
  abandonment with a late end, a non-final late error, a process exit, a
  never-dispatched invocation, and an `escaped` outcome fixed while the call
  is still pending, which settles only when the call ends or its process
  exits.

## The engine core

`New(Config)` opens a compiled program ([internal/ir](../ir/README.md)): it
checks that every flow stays within what the engine runs, and compiles
every expression's program once on the configured QuickJS-ng engine
through B2b's `expr.Pipeline` ([ADR 0003](../../docs/design/0003-semantics-placement.md)).
The compiler has already run the expressions' textual stages
(`expr.Compile`), so nothing is recomputed per run. `Run(ctx,
RunRequest)` runs one flow to its end and returns its trace; an error
means the run could not start (an unknown flow, arguments that are not a
JSON object or that jsonb refuses, no executor). An `Engine` is safe for
concurrent use.

**What it runs (N1a, N1b, N3a, N3b, N3c, N2, NI).** A flow's modules in sequence:
`script` and `rawscript` Bun steps, `identity`, input transforms,
`skip_if`, `mock`, `pass_flow_input_directly`, `stop_after_if`, `sleep`,
a static or javascript `timeout`, `retry` and `retry_if`,
`continue_on_error`, a failure module of those kinds, `flow_env`,
`early_return` (which changes only the synchronous HTTP response,
[FX-37](../../docs/semantics/execution.md#fx-37)), a flow's own `timeout`,
which nothing enforces: a flow job bypasses every executor timer
([FX-24](../../docs/semantics/execution.md#fx-24), path 4), for and while
loops and `branchall` with `stop_after_all_iters_if`, `branchone`, and
subflows by path, in a flow with a failure module too, whose bodies each
carry a copy of it (below), with N2's waits and retries in, on and before
containers (NI, below).
`stop_after_all_iters_if` is evaluated on an active loop or
`branchall` only, so on any other module it is accepted and never
evaluated ([CF-13](../../docs/semantics/control-flow.md#cf-13),
[FX-2](../../docs/semantics/execution.md#fx-2)). `New` refuses everything
else with `ErrUnsupported`, naming the milestone that implements it, for
a top-level module, a container body's and the failure module alike:
a container as the failure module, whose
bodies run raw with a suffixed copy of it
([CF-30](../../docs/semantics/control-flow.md#cf-30), source-traced
only); a retry on a subflow step, whose attempts' subflows would go
below the attempt's site, which no case of NI covers (`later`);
`cache_ttl`, a path script's own included (N4); host-provided
sites (F2a); a step of another harness than Bun's, whose `bun` and
`native` boundaries it runs (L1); an `ai`
parallelism, whose error the reference does not give; a preprocessor,
`skip_expr` and an `ai` sleep or timeout (`later`).

**One module** follows [FX-2](../../docs/semantics/execution.md#fx-2):

1. **Host start checks** (`Start`): the latch, cancellation, `haltBefore`
   (at a module's first attempt only). Then `OnModuleStart` (H6), for
   every attempt.
2. **The previous module's `sleep`**, at the module's first attempt (N2,
   below), which sets when its job may start.
3. **`skip_if`** ([FX-17](../../docs/semantics/execution.md#fx-17),
   [JE-25](../../docs/semantics/expressions.md#je-25)), a predicate whose
   value must be JSON `true` or `false`
   ([JE-24](../../docs/semantics/expressions.md#je-24)).
4. **Arguments**, by the first rule that applies: `mock` (`{previous_result:
   <value or null>}`, [FX-18](../../docs/semantics/execution.md#fx-18)),
   a retry's reused arguments (N2, below), `pass_flow_input_directly` (the
   flow input), an identity or skipped module (`{previous_result: P}`),
   else the input transforms
   ([FX-7](../../docs/semantics/execution.md#fx-7)), whose failure still
   launches the job, with `{}`, to fail without running. A module that
   launches its own job, a leaf or an identity module, has its arguments
   go through H2; a module that `skip_if` or `mock` answered, a retry and
   a failed transform do not
   ([ADR 0008](../../docs/design/0008-host-hooks.md)). Then the module's
   own `timeout` is evaluated, for every module (N2, below), and every
   module's arguments go through the storage boundary.
5. **Launch**, once the scheduled time of a sleep or a retry delay has
   passed (N2, below). A mocked or skipped module is replaced by an identity job,
   which no hook sees. An `identity` module launches its own identity job,
   which H1 may answer for like any launch. An identity job's result is
   its stored arguments' `previous_result`, or `{}`
   ([FX-16](../../docs/semantics/execution.md#fx-16)), and it is never
   timed ([FX-24](../../docs/semantics/execution.md#fx-24)). A leaf is
   offered to H1 (`Intercept`), then its entrypoint is chosen
   (`Entrypoint`), H3 asks `BeforeFirstWrite` once per run before the
   first write-capable launch, H12 edits the environment, and the step
   runs through the `StepExecutor` with its timeout as a timer in the
   engine. The timeout is the module's own, static or evaluated, else a
   path script's own timeout metadata; a request strictly below the
   instance maximum is job-specific, zero included (no time at all); none,
   or one at or above the maximum, gives the maximum, instance-wide
   (FX-24, FX-25, WR-37). `StoreOutcome` maps the outcome, H1's included;
   a leaf's result or failure value that jsonb refuses fails its job
   instead (FX-48 case 1, below).
   The environment H12 edits is the step's job identity
   ([internal/platform](../platform/README.md), milestone PF1): a fresh
   `WM_JOB_ID` (the invocation's id), the run as `WM_FLOW_JOB_ID`,
   `WM_ROOT_FLOW_JOB_ID` and `WM_ROOT_JOB_ID`, the flow's path, the module
   id, the job path, `WM_STATE_PATH`, a fresh `WM_TOKEN`, and the host
   configuration of `Config.Host` and `Config.Workspace`. A step in a loop
   body takes its child flow's job id, innermost scope and path, or its
   collapsed loop's step id and `<flow path>/loop-<i>` job path (For loops,
   below). A Bun step of a path script also gets the script's own `envs`
   entries (`KEY=VALUE`) under them: a name the worker sets later (a
   reserved variable, or a common Bun variable such as `PATH` or
   `BASE_URL`, and `NODE_PATH` when the engine's environment has one,
   which the harness then copies, WR-22/b) or one already set is skipped,
   and a later entry wins
   ([WR-22](../../docs/semantics/languages.md#wr-22),
   [WA-2](../../docs/semantics/client-api.md#wa-2); `env.go`). A native
   step gets no `envs` ([WR-45](../../docs/semantics/languages.md#wr-45)),
   milestone FU5.
6. **`stop_after_if`** after a success, except on the failure module, and
   on an identity job classified skipped: any identity job of a module
   with a `skip_if`, whatever the predicate answered, and whether it ran or
   H1 answered for it ([FX-17](../../docs/semantics/execution.md#fx-17),
   [FX-18](../../docs/semantics/execution.md#fx-18),
   [FX-19](../../docs/semantics/execution.md#fx-19)). A clean stop ends
   the flow with the module's result, `skipped` when `skip_if_stopped` (the
   module is then skipped too) and `success` otherwise; an error stop fails
   the module, which carries `{"error": {"name": "EarlyStopError",
   "message": <error_message, or "stop after if: <expr>" when empty>,
   "result"?}}` while its job keeps its success
   ([FX-V2](../../docs/semantics/execution.md#fx-v2)). A throwing
   predicate fails the module unrecoverably
   ([FX-21](../../docs/semantics/execution.md#fx-21)).
7. **`OnModuleEnd`** (H6), then the host completion checks (`Completion`):
   the latch, cancellation (except for an error stop in a flow with a
   failure module, below), a fatal error name (H7, which latches a
   `FatalError` and records `H7` in the trace's deviations), then
   Windmill's continue decision (below), with its retries. Last,
   `AfterContinue` applies `haltAfter` when the flow would go on, never on
   the way to the failure module, and never while a retry is due; when the
   module ends a container's child flow, the halt is pending until the
   container has completed (below).

**The continue decision** (N1b; [FX-2](../../docs/semantics/execution.md#fx-2)
completion steps 3, 7 and 8, when no retry is due; the retries are N2's,
below).
`continue_on_error` tolerates a failure unless it is unrecoverable
([FX-44](../../docs/semantics/execution.md#fx-44)):

| The module | Then |
|---|---|
| succeeded | the next module, or the flow succeeds with its result at the last |
| stopped cleanly | the flow ends with its result, `success` or `skipped` |
| a chaining error at a later transition | the flow fails at once with the `InternalError`; the failure module does not run ([FX-15](../../docs/semantics/execution.md#fx-15) context 1) |
| failed (a failed job, a transform error, a chaining error at the root's first pickup), tolerated | the next module, whose `previous_result` and `results.<id>` are the failure value; at the last the flow succeeds with it, and the failure module does not run ([FX-30](../../docs/semantics/execution.md#fx-30)) |
| failed, not tolerated | the failure module, else the flow fails with the failure value ([FX-31](../../docs/semantics/execution.md#fx-31), [FX-36](../../docs/semantics/execution.md#fx-36)) |
| an error stop, with a failure module | tolerated: the next module, or past the last one the failure module, entered at cursor `len` with an empty preceding id ([FX-V4](../../docs/semantics/execution.md#fx-v4)); not tolerated: the failure module ([FX-19](../../docs/semantics/execution.md#fx-19)) |
| an error stop, no failure module | tolerated at the last module: the flow succeeds with the `EarlyStopError`; otherwise it fails with it |
| a failed loop or `branchall` whose `stop_after_all_iters_if` stops cleanly (N3b) | a stop never continues, not even into the failure module (rule 7.1): tolerated at the last module, the flow succeeds with the array; otherwise it fails with it ([CF-13](../../docs/semantics/control-flow.md#cf-13)) |
| a throwing `stop_after_if` | the failure module, else the flow fails with the string error; `continue_on_error` never applies ([FX-21](../../docs/semantics/execution.md#fx-21)) |

**The failure module** sits at the slot `s:failure`, the cursor `len`
([FX-1](../../docs/semantics/execution.md#fx-1)). It starts like any
module, through the host's start checks (where `haltBefore` never applies
to it), its own `skip_if`, arguments and launch, with these differences
([FX-32](../../docs/semantics/execution.md#fx-32),
[JE-35](../../docs/semantics/expressions.md#je-35)):

- its `previous_result` is the failing value, and its preceding id the
  failed module's (empty when it is entered at cursor `len`), so
  `results.<failed id>` reads the failing value from memory and a simple
  path reads the failed job's stored result
  ([FX-10](../../docs/semantics/execution.md#fx-10)); `error` is the
  evaluator's ([JE-17](../../docs/semantics/expressions.md#je-17));
- its input transforms also see `started_at`, the start time of the flow
  it runs in (the run's, or a child flow's or a subflow's for a copy in a
  body or a subflow's own, N3c) as an RFC 3339 string with 0, 3, 6 or 9
  fraction digits, and `flow_job_id`, that flow's job id; its
  `WM_FLOW_STEP_ID` is its own id;
- a `skip_if` that holds replaces it with a passthrough identity job that
  is never flagged skipped ([FX-17](../../docs/semantics/execution.md#fx-17));
  it has no stop, and its own `continue_on_error` is ignored;
- a chaining error while it starts is a later transition's (context 1).

The flow then ends with the failure module's result, stored as it is: it
succeeds only when the failure module's job succeeded and its result
decodes as the recovery record with `recover` true, an object's boolean
member or a one-element array by position; a decode error, an absent
`recover` or `null` does not recover, and a failure module that fails
fails the flow with its own failure value
([FX-33](../../docs/semantics/execution.md#fx-33)).

**Cancellation** ([FX-38](../../docs/semantics/execution.md#fx-38), H4).
A run whose context ends finishes canceled, without retry,
`continue_on_error` or the failure module, with the result
`{"error": {"message": "Job canceled: <reason> by <canceler>", "name":
"Canceled", "reason", "canceler"}}`, whatever its modules reported. The
reason and the canceler are the context's cause when it is a
`*Cancellation`, else Windmill's defaults, `no reason given` and
`unknown`. A step in flight is abandoned and its job is canceled with the
worker's text `execution error:\njob process terminated due to cancelled
by <canceler> (reason: <reason>)`, `ExecutionErr`, `step_id`. Windmill's
continue decision checks a stop before the cancel flag: a clean stop
under cancellation ends canceled, not skipped, while an error stop in a
flow with a failure module still starts what the decision asks for (the
next module or the failure module), and the run ends canceled at its next
completion.

The flow's result is what the decision ends it with, stored as jsonb
(FX-36); a flow with no modules returns its input
([FX-3](../../docs/semantics/execution.md#fx-3)), through the same
completion checks, so it ends canceled when its context has ended. A
latched or halted run has no result.

**The argument pipeline** ([ADR 0005](../../docs/design/0005-json-step-boundaries.md)):

- The flow input is stored (jsonb) before anything reads it
  ([JE-44](../../docs/semantics/expressions.md#je-44)); `flow_input` and
  the first module's `previous_result` are the text JE-44's table gives
  the worker's copy: a compact object, each member in jsonb's printed form
  (a space after each `:` and `,`, `expr.DatabaseText`), so JE-7's
  shortcut answers a member in that form.
- The top-level order of the flow input and of every argument map is
  unspecified on Windmill ([FX-42](../../docs/semantics/execution.md#fx-42)
  item 1), so a per-run seed shuffles it (ADR 0001 §2). The same seed
  orders the input transforms' evaluation: one at a time, stopping at the
  first error ([JE-V5](../../docs/semantics/expressions.md#je-v5)).
  `RunRequest.Seed` fixes it; zero draws one.
- A static value is copied verbatim, an `ai` input left out, a javascript
  one evaluated ([JE-23](../../docs/semantics/expressions.md#je-23)).
- Each argument, its key and its value, becomes its stored text (step 3
  of the ADR's pipeline), in the shuffled order, the last of two keys that
  store alike winning, as in jsonb; a key or a value jsonb refuses fails
  the push ([FX-48](../../docs/semantics/execution.md#fx-48),
  [FX-51](../../docs/semantics/execution.md#fx-51)). The stored arguments
  are the recorded ones (O2); the resolved ones (O3) are the script's,
  which the job's pickup resolves (below), and are the recorded ones for an
  identity job or a launch a hook answers.

**Expression scopes** ([JE-22](../../docs/semantics/expressions.md#je-22),
[FX-8](../../docs/semantics/execution.md#fx-8)), P being the previous
module's carried value (its producer text) or the flow input at the first
module:

| Site | `previous_result`, `result` | Other names | Previous id | `results` |
|---|---|---|---|---|
| input transform | P, unbound | `params` `{}`, `resume` `null`, `resumes` `[]`, `approvers` `[]` | the previous module's | yes |
| `skip_if` | P, P | `resume`, `resumes`, `approvers` | the previous module's | yes |
| `stop_after_if` | the module's own result, twice | — | empty | only when the text holds `results.`, `results[` or `results?.` |
| `sleep` (N2) | the sleeping module's own result, twice | — | empty | no: `results` is not defined |
| `timeout` (N2) | P, P | — | the previous module's | yes |
| `retry_if` (N2) | the failed attempt's value, twice, and `error` its error | — | empty | no |

Every site has `flow_input`; `flow_env` is the flow's, as the transition
that started the module resolved it
([JE-38](../../docs/semantics/expressions.md#je-38)), at `stop_after_if`
and `retry_if` only when the text names it, and there the flow's cached
resolved map once one exists. Every site has the helpers of
[JE-20](../../docs/semantics/expressions.md#je-20) (below).

**The results context.** The run answers the evaluator's lookups
([JE-18](../../docs/semantics/expressions.md#je-18),
[JE-19](../../docs/semantics/expressions.md#je-19)) from the jobs it has
run, per flow (a frame, `frame.go`: the root, or a loop iteration's or a
branch's child flow): `Lookup` (the engine path) gives the job result of
the last module of the current flow with that id that has a job (a
loop's or a `branchall`'s is the array of its children's results, a
`branchone`'s its branch's), so a later module of a repeated id that has
not started is skipped
([JE-18/e](../../docs/semantics/conformance.md#je-18),
[OF-2](../../docs/semantics/openflow.md#of-2)), else the node-id map's;
`Stored` (JE-9's direct path) gives the node-id map's, where the module
that finished last wins. A `branchone` that ran no branch has a nil job:
`Lookup` fails to fetch it with the server's not-found text, ``Failed to
fetch result for step '<id>': Not found: Completed Job not found at name
00000000-0000-0000-0000-000000000000``, without its source location (D14),
and the map holds it as `null`
([CF-19](../../docs/semantics/control-flow.md#cf-19) route 5). The map has
scopes (CF-19): the root's, and one per parallel iteration; a sequential
or while iteration and a branch write into their parent's, and a lookup
walks from a scope to the root. Both are job results in storage: after an error stop that is
the step's success value, not the `EarlyStopError`
([FX-10](../../docs/semantics/execution.md#fx-10)). An unknown id is
`null`. The previous module's own reads from memory are the evaluator's
(JE-8, JE-18 step 1, FX-V6).

**Failure values** the engine generates (origin `engine`):

| When | Value | Finding |
|---|---|---|
| an input transform fails | `{"error": {"message": "Error during isolated evaluation of expression `<text as written>`:\n<evaluator text>", "name": "ExecutionErr"}}`; the job launched with `{}` and did not run | FX-14, JE-23 |
| `skip_if`, or an argument jsonb refuses, at the first module (the root's first pickup) | the module fails, not run: `{"error": {"message": "Error: <text>", "name": "Anyhow"}}` | FX-15 context 2, JE-35 (b) |
| the same at a later module | the flow fails at once, the module not run: `{"error": {"message": <text>, "name": "InternalError"}}` | FX-15 context 1, JE-35 (a) |
| `stop_after_if` fails | the module fails unrecoverably with the string `{"error": <text>}` | FX-21, JE-26 |
| a for loop's iterator throws, is not an array, or is `ai` | a chaining error, routed as above: `Error: <evaluator text>`, ``Expected an array value in the iterator expression, found: <serde's message>``, or `AI input transform not supported for iterator` | CF-2, CF-23, JE-30 |
| jsonb refuses the iterator's array (a NUL, an unpaired surrogate, a number beyond numeric's range), or a child flow's input | a chaining error, routed as above, before any iteration runs: `error returned from database: <PostgreSQL's message>` | CF-2, FX-48, FX-51 |
| a parallel loop's `parallelism` throws or is not a u16 | a chaining error: ``Error during evaluation of expression `<text>`:
<evaluator text>``, `Error parsing result as u16: <serde's message>. Value was: <value>` or `Error parsing static value as u16: <serde's message>` | JE-V1 |
| a parallel loop of two or more iterations in a `same_worker` flow | a chaining error: `Bad request: Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker` | CF-7 |
| a chaining error at a child flow's first module | the child fails at once with the `Anyhow` form, its module not started; the loop sees a failed iteration | FX-15 context 3, CF-V3 |
| a `branchone` predicate throws | the module fails in place, an ordinary step failure wherever it is, never a chaining error: `{"error": {"message": "Error: <evaluator text>", "name": "Anyhow"}}`, with a nil job | CF-14, CF-23, JE-31 |
| a parallel `branchall` of two or more branches in a `same_worker` flow | the `same_worker` chaining error of a parallel loop | CF-7 |
| a collapsed body's input transform fails | as an input transform's, the message prefixed `could not transform input using an expr: ` | JE-23, JE-39 |
| `stop_after_all_iters_if` fails | an error stop of the flow: ``{"error": {"name": "EarlyStopError", "message": "Error evaluating stop_after_all_iters_if expression `<text>`: <predicate text>"}}``, carried by the loop, whose result stays its array | CF-13, JE-27 |
| an invalid `_ENTRYPOINT_OVERRIDE` | `{"error": {"message": "Invalid entrypoint override: <value>", "name": "ExecutionErr"}}`, before the step runs | WR-12 |
| the step's timeout | `TimeoutError`'s value for the step's boundary: for Bun, `execution error:\njob process terminated due to timeout after exceeding <job-specific or instance-wide> duration limit`; for native (C3n), `execution error:\nQuery timeout after (>Ns)`, whichever limit applied; `ExecutionErr`, `step_id` | WR-37, WR-44 |
| a job's result or failure value that jsonb refuses | the job fails: `{"error": {"message": "Internal: Could not add completed job <job id>: error returned from database: <PostgreSQL's message>", "name": "InternalErr"}}`, no `step_id`, at once where Windmill retries the insert for about 30 s first | FX-48 case 1, OR-27 |
| the run is canceled | the run's `Canceled` error, and the killed step's `ExecutionErr` (above); a job still waiting for a sleep or a retry delay fails without running, with the run's `Canceled` error | FX-38 |
| a sleep's value is not a delay (N2) | a chaining error (always a later transition): `Expected an integer, found: <n>` for a negative integer, `Expected an integer, found: <debug form>` (`String("1")`, `Null`, `Array [Number(1)]`) for anything else, `Expected a number value, had error instead: ExecutionErr("Error during evaluation of expression `<text>`:\n<evaluator text>")`, a debug form with its newline escaped, for a failed evaluation | FX-23, JE-28, FX-15 |
| a timeout's value fails (N2) | a chaining error: `Timeout value cannot be negative`; `Error parsing result as i32: <serde_json's error, with its position>. Value was: <the evaluator's text as it is>`; `Error during evaluation of expression `<text>`:\n<evaluator text>` | FX-24, JE-29, FX-15 |
| `retry_if` throws while the completion is decided (N2) | the module fails unrecoverably with the string `{"error": "Error: <text>"}`, as for `stop_after_if` but converted once more, so two `Error: ` prefixes (and on Windmill two location suffixes) | FX-28, FX-21, FX-50 |
| `retry_if` throws at re-entry (N2) | a chaining error of a later transition: the flow fails at once with `{"error": {"message": <text>, "name": "InternalError"}}` | FX-28, FX-15 |
| a transition outlives the monitor (N2) | the run is canceled by `monitor` (below) | JE-2 |

A predicate's `<text>` is `Error: <evaluator text>` for a failed
evaluation, or `Expected a boolean value, found: "<value's JSON>"`
([JE-24](../../docs/semantics/expressions.md#je-24)); an argument's is
`error returned from database: <PostgreSQL's message>`
([FX-48](../../docs/semantics/execution.md#fx-48)). serde's message is
what Windmill's JSON library reports for the value (`serde.go`), such as
``invalid type: map, expected a sequence at line 1 column 0`` or
``invalid type: floating point `1.5`, expected u16 at line 1 column 3``.
Windmill ends the generic ones with Rust source locations, which
windmill-lite does not write (D14); a comparison strips them on the
oracle's side ([internal/conformance](../conformance/README.md#location-suffixes)).
The trace records who produced a module's carried value apart from its
result (`CarriedOrigin`), since an error stop's `EarlyStopError` is the
engine's over a step's own result; and, for a loop's array, where inside it
the errors the engine generated are (`ResultNested`, `CarriedNested`,
`Trace.ResultNested`: JSON pointers such as `/0` or `/1/0` for a nested
loop's), through every level of nesting up to the run's result, and below
`/error/result` when an error stop includes the array, so that a
comparison strips their locations and a user's error beside them keeps
its own; a `branchall`'s array does the same, and a `branchone` carries
its branch's result with its origin and pointers.

**Readings where the reference is silent**, each pinned by a test:

- An identity job reads its arguments from storage, decoded member by
  member as a flow input's are, so its result, the value a skipped, mocked
  or identity module carries on, is its `previous_result` in jsonb's
  printed form, as JE-44's table gives a flow input's member (N1b; N1a
  read it as the compact stored text).
- An H1 intercept runs no job (`Ran` false), and H3 asks only before a
  leaf's launch that H1 declined: an intercepted module or an identity job
  writes nothing.
- An argument that jsonb refuses fails the push with PostgreSQL's message
  ([Q-FX-8](../../docs/semantics/execution.md#q-fx-8) leaves the text open).
- An invalid entrypoint override fails as a pre-run error, with no
  `step_id`.
- The `wm_failure` marker and the storage failure of FX-48 case 1 apply to
  a leaf's outcome, never to an identity job's result; an H1 intercept,
  which runs no job, is checked for `wm_failure` but not for storage.
- A canceled step's killed text and the run's `Canceled` error take the
  same reason and canceler, Windmill's defaults included.
- N2's readings are listed with its section, below.
- For loops (N3a): an empty body does not evaluate the iterator; a
  collapsed iteration's previous id is empty, and a read of its own loop
  sees the iterations so far, `[]` at first; on a loop that failed, a
  clean stop of `stop_after_all_iters_if` keeps the failure and the array
  and still stops the flow, and an error stop replaces the carried value
  (N3b, below); a loop whose `stop_after_if` throws fails as a step's does
  (FX-21), with no `stop_after_all_iters_if` after it; a static null
  `parallelism` is no limit, as a static null timeout is none; a collapsed
  body takes its loop's `pass_flow_input_directly` instead of its own
  (CF-8 item 5) and then receives its iteration's flow input; the
  text of P after a loop is the compact array with every number written
  again in the backend's notation
  ([JE-44](../../docs/semantics/expressions.md#je-44),
  [FX-42](../../docs/semantics/execution.md#fx-42) rule 9: `1e+21`,
  `1e-6`, `1000.0`, `-0.0`), by `platform.Reprint`, the writer the
  platform's argument resolution uses (`aggregate`); a child result the
  backend's library cannot read back (a number beyond a double, nesting
  deeper than 128, which only an identity child can carry) fails that
  read at the container's completion, as Windmill's fails: the flow fails
  at once with a chaining error named `InternalError` (FX-15 context 1),
  whose message is the library's error text, a reading, since the
  reference does not give Windmill's (design README, request 13), and the
  container fails with no result, for a loop, a while loop and a
  `branchall` alike; the sites that show the iterations so far without
  reading them back (a running loop's job list, `all_iters`, an
  interrupted loop's result) keep such a result as stored; an iterator or `parallelism` whose
  text is not JSON (a replaced `JSON.stringify`,
  [JE-15](../../docs/semantics/expressions.md#je-15)) fails with serde_json's
  syntax error for an empty text, a character no value starts with, a
  broken `true`, `false` or `null` (``expected ident at line 1 column 2``),
  a leading zero (``invalid number at line 1 column 2``) or more than white
  space after a value (``trailing characters at line 1 column 2``), which
  serde_json checks after the value's type and range, and serde_json's
  other syntax errors are not reproduced; whatever is not one JSON value
  of the expected kind is refused.
- While loops and branches (N3b): an empty chosen `branchone` branch at
  the first module of a child flow passes the JSON string `"{}"` on, as
  at the root's (CF-14 says "the first step of its flow"); a `branchone`
  whose predicate threw has a nil job, as an empty branch's
  ([OR-11](../../docs/semantics/oracle.md#or-11)), so results reads of it
  behave alike; a stop that a branch propagates is not followed by the
  `branchone`'s own `stop_after_if`; a branch that fails at a later
  transition of its child flow (FX-15 context 1) gives its `branchone` the
  stored error, as a loop's entry is, where
  [Q-CF-3](../../docs/semantics/control-flow.md#q-cf-3) part 2 leaves open
  whether Windmill hands it the last successful inner result; an error
  stop a `branchone` or a sequential `branchall` propagates is an error
  stop of the module, so `continue_on_error` ends the flow there unless
  the module is the last
  ([FX-2](../../docs/semantics/execution.md#fx-2) completion rule 7.1,
  [FX-19](../../docs/semantics/execution.md#fx-19)); a clean stop with
  `skip_if_stopped` in a sequential `branchall` flags the `branchall`
  skipped, as FX-20's "the same skipped flag" says of a `branchone`; a
  clean `stop_after_all_iters_if` stop of a failed loop or `branchall`
  leaves the module failed, with no skipped flag (`skip_if_stopped`
  included), carrying its array, and its child flow, if any, forwards it
  as the stop of a failure.

**For loops (N3a, `loop.go`).** A loop module that runs is a container: no
arguments, no H1 or H2, a record whose status is the loop's own and whose
result is the array of its iterations' results
([CF-4](../../docs/semantics/control-flow.md#cf-4)).

1. **Start.** `skip_if` and `mock` answer for it as for any module. The
   iterator is evaluated once, with `skip_if`'s scope; its elements are
   kept in their stored form, since Windmill stores the evaluated array
   ([CF-2](../../docs/semantics/control-flow.md#cf-2),
   [JE-30](../../docs/semantics/expressions.md#je-30)): an array jsonb
   refuses fails the loop's start before any iteration runs
   ([FX-48](../../docs/semantics/execution.md#fx-48),
   [FX-51](../../docs/semantics/execution.md#fx-51)). An empty array, or
   an empty body, completes the loop at once with `[]` and no job; its
   `stop_after_if` is then an ordinary stop of the flow with `result` `[]`
   and no `all_iters`, and its `stop_after_all_iters_if` is not evaluated
   ([CF-29](../../docs/semantics/control-flow.md#cf-29),
   [CF-V2](../../docs/semantics/control-flow.md#cf-v2)). With no job, its
   record's status is its module's, as the oracle's walker reads a
   container with no child: an error stop fails both (CF-29/a, N3c).
2. **Iterations.** Each iteration's flow input is the loop's flow input
   plus `iter` = `{"index": i, "value": element}`, an `iter` already there
   moving to `iter_parent` and so on up
   ([CF-3](../../docs/semantics/control-flow.md#cf-3)). The IR says whether
   a loop collapses ([CF-8](../../docs/semantics/control-flow.md#cf-8)):
   - a **collapsed** iteration runs its one body step as a job of the
     loop's flow: its previous_result is the step before the loop's
     result, then the previous iteration's (the flow's input every time
     for a loop at the first module, [JE-36](../../docs/semantics/expressions.md#je-36));
     its previous id is empty; it takes the loop's `timeout`, and its
     loop's `continue_on_error` rather than its own, so a failed body
     fails the iteration (CF-8 item 5); its step id,
     and its errors' `step_id`, are the loop's; its job path is
     `<flow path>/loop-<i>` for an inline step; nothing is registered under
     its own id;
   - any other iteration runs its body as a **child flow**
     ([CF-1](../../docs/semantics/control-flow.md#cf-1)), whose input is read
     back from storage and is its first step's previous_result
     ([CF-9](../../docs/semantics/control-flow.md#cf-9)). Its steps see
     the child's job id as `WM_FLOW_JOB_ID` and its path,
     `<flow path>/loop-<i>` or `<flow path>/forloop-<i>`, as `WM_FLOW_PATH`
     ([WA-6](../../docs/semantics/client-api.md#wa-6)); a parallel
     iteration is also their innermost scope (`WM_ROOT_FLOW_JOB_ID`,
     [WA-V1](../../docs/semantics/client-api.md#wa-v1)). The trace lists
     each child flow (`Trace.Children`) with its modules, so that a module
     it never reached is `not_run`, and its id. A chaining error at a
     child's first module fails the child at its pickup (FX-15 context 3).
3. **Sequential** ([CF-5](../../docs/semantics/control-flow.md#cf-5),
   [CF-11](../../docs/semantics/control-flow.md#cf-11),
   [CF-12](../../docs/semantics/control-flow.md#cf-12)): a failed iteration
   stops the loop, which fails with the array so far, unless
   `skip_failures`; a clean stop in the body breaks it, and so does an
   error stop, which fails it unless `skip_failures`; after each successful
   iteration the loop's `stop_after_if`, with `all_iters` bound when its
   text names it, breaks it, `skip_if_stopped` and `error_message` without
   effect.
4. **Parallel** ([CF-7](../../docs/semantics/control-flow.md#cf-7),
   [JE-V1](../../docs/semantics/expressions.md#je-v1)): every iteration is a
   child flow on a goroutine of its own, each its own results scope.
   `parallelism`, evaluated per child with the loop's P and its flow's
   input, holds a child k at or beyond its own N until k − N + 1
   iterations have completed; the children not held start at once, so one
   never waits behind a held one, and held ones start in the order of that
   count, by index among equals; with N = 0 none starts and only the run's
   end stops the wait. There is no fail-fast: every iteration runs
   (Windmill's continue-all, decisions Q-18), and the loop fails with the
   full array unless every iteration succeeded or `skip_failures`. A stop
   ends only its iteration, and the loop's `stop_after_if` is not
   evaluated. Each parallel iteration seeds its own shuffles from its
   loop's flow, in index order, so a seed still fixes every order.
5. **Completion** ([CF-13](../../docs/semantics/control-flow.md#cf-13),
   [JE-27](../../docs/semantics/expressions.md#je-27)): an active loop's
   `stop_after_all_iters_if` is evaluated once with the array; when it
   fires it acts as an ordinary `stop_after_if` of the flow the loop is
   in.

The host checks run before every iteration too (`Start`): a latch, the
cancellation or a halt starts none, and a parallel loop waits for the
iterations it started. A halt decided in one parallel iteration stops the
others at their next module, and wakes a loop whose iterations are all
held; the run's status ranks the latch, then the cancellation, then the
halt (ADR 0008), whichever iteration met its signal first. A body
module's halt-after that ends its child flow is pending for the whole run
(N3b): the loop completes the iteration, its `stop_after_if` included,
and the halt applies at the run's next start anywhere, the next
iteration, an iteration a parallel loop still holds, the next module of
another iteration already running, or, once the loop has completed (its
`stop_after_all_iters_if` included) and every enclosing container too,
the next module; a run that ends first keeps the status Windmill gives
it, and the pending halt is dropped, with no `H8`. So a halt never hides
a failure or a stop that ends the run. A step already running finishes,
and a flow that has started (an iteration or a branch launched) runs its
first module, so a parallel loop's iterations already started are not
starts. A loop so interrupted records the array so far; a canceled one
completes as a failure (NI, below).

**While loops (N3b, `loop.go`).** A while loop is a sequential loop with
no iterator ([CF-10](../../docs/semantics/control-flow.md#cf-10)): its
iterations run one after another, iteration k's `iter` being
`{"index": k, "value": k}` (CF-3), until its `stop_after_if` breaks it
after a successful iteration (CF-11, `all_iters` bound as above), a stop
in its body does (CF-12), an iteration fails that `skip_failures` does
not tolerate (CF-5), or the run ends (CF-28); nothing else ends it. It
shares the sequential for loop's code: a one-step body collapses in every
job kind (CF-8 item 3), each iteration otherwise runs as a child flow at
`<flow path>/loop-<i>` (CF-24), and its result keeps the iteration that
broke it. An empty body returns `[]` at once, an empty container
(CF-29, CF-V2); `stop_after_all_iters_if` is evaluated as for a for loop
(CF-13). Its `parallel` fields are dropped by the IR (OF-8).

**Branches (N3b, `branch.go`).** A branch that runs is a child flow
([CF-1](../../docs/semantics/control-flow.md#cf-1)) whose input is its
flow's input, unchanged and read back from storage
([JE-37](../../docs/semantics/expressions.md#je-37)), which is its first
module's previous_result ([CF-9](../../docs/semantics/control-flow.md#cf-9),
[JE-36](../../docs/semantics/expressions.md#je-36)); it writes into its
flow's results scope and stays in its innermost flow scope (CF-19,
WA-V1), at `<flow path>/branchone-<n>` (`-0` the default, `-<i+1>` branch
i) or `<flow path>/branchall-<i>`
([CF-24](../../docs/semantics/control-flow.md#cf-24)). The trace lists it
(`Trace.Children`) under `b:<n>` or `b:default`.

1. **`branchone`** ([CF-14](../../docs/semantics/control-flow.md#cf-14),
   [JE-31](../../docs/semantics/expressions.md#je-31)): its predicates are
   evaluated once, in order, each as `Boolean(<expr>)` (so an empty one
   never holds), with `skip_if`'s scope; the first that holds is chosen,
   later ones are not evaluated, and the default runs when none holds.
   The record names it (`BranchChosen`). A throw fails the module in
   place (Failure values, above). The chosen branch's result is the
   module's, and the module's `stop_after_if` is then an ordinary stop of
   the flow. An empty chosen branch runs no child: the module succeeds
   with a nil job, passing on the previous_result handed to it, or the
   JSON string `"{}"` at the first module
   ([OR-25](../../docs/semantics/oracle.md#or-25)).
2. **`branchall`** ([CF-15](../../docs/semantics/control-flow.md#cf-15)):
   every branch runs, an empty one included, whose result is then its
   input; its result is the array of the branches' results in branch
   order, and its own `stop_after_if` is not evaluated. A parallel one
   (the deployed default, [CF-6](../../docs/semantics/control-flow.md#cf-6))
   starts every branch at once on goroutines of their own, with no limit
   and no fail-fast (continue-all, decisions Q-18), and fails with the
   full array when a branch whose `skip_failure` is false failed; each
   parallel branch draws its own rng from its flow's, in branch order. A
   sequential one goes past a failed branch only when that branch's
   `skip_failure` is true, and otherwise fails with the results so far. A
   `branchall` with no branches is an empty container (CF-29).
   `stop_after_all_iters_if` is evaluated once, with the array, as for a
   loop (CF-13, [JE-27](../../docs/semantics/expressions.md#je-27)),
   unless a sequential one was stopped from inside.
3. **Stops** ([CF-12](../../docs/semantics/control-flow.md#cf-12),
   [FX-20](../../docs/semantics/execution.md#fx-20)): a clean stop in a
   `branchone`'s branch or a sequential `branchall`'s stops the flow
   right after the module, with the same skipped flag, the `branchall`'s
   result being its results so far; an error stop there, with no failure
   module, is an error stop of a `branchone`, and of a sequential
   `branchall` whose stopped branch's `skip_failure` is false, while one
   whose `skip_failure` is true stops the flow as a success; either way
   the flow the module is in forwards it one level up, as a stop of a
   sequential loop's body breaks the loop. A parallel `branchall` ignores
   a stop: it ends only its branch, and an error stop counts as a failed
   branch.

The host checks run before every branch, as before every iteration, and
a body module's halt-after that ends a branch is pending as in a loop:
it applies at the next branch of a sequential `branchall`, at the next
module of another branch of a parallel `branchall`, or at what the run
starts once the container has completed, its `stop_after_if` or
`stop_after_all_iters_if` included.

**Subflows by path (N3c, `subflow.go`).** A `type: flow` step runs the
flow at its path, which the compiler put in the program, as a child flow
of its own ([CF-17](../../docs/semantics/control-flow.md#cf-17),
[FX-5](../../docs/semantics/execution.md#fx-5)), at the step's own site;
the trace lists it (`Trace.Children`) there, and the step's record holds
the child's id as its job.

- **Input.** The step's evaluated arguments, stored and read back as a
  script step's are ([JE-44](../../docs/semantics/expressions.md#je-44),
  [JE-37](../../docs/semantics/expressions.md#je-37)), with their
  top-level keys shuffled (FX-42); they are its first module's
  previous_result (CF-9).
- **Scopes.** It is a results scope of its own, whose lookups of an id it
  has no step of still walk up to its caller's
  ([CF-19](../../docs/semantics/control-flow.md#cf-19), Q-CF-12 matched,
  [ADR 0001](../../docs/design/0001-equivalence-contract.md) §6), and its
  own innermost flow scope: its steps see its job id as `WM_FLOW_JOB_ID`
  and `WM_ROOT_FLOW_JOB_ID` and its path as `WM_FLOW_PATH`
  ([WA-6](../../docs/semantics/client-api.md#wa-6),
  [WA-V1](../../docs/semantics/client-api.md#wa-v1)); a body inside it
  hangs below it as below any flow.
- **Settings.** Its own failure module, `same_worker` and `flow_env`, or
  its caller's `flow_env` when it defines none
  ([CF-1](../../docs/semantics/control-flow.md#cf-1),
  [JE-38](../../docs/semantics/expressions.md#je-38)); `flow_env`'s `$WM_*`
  references resolve with the run's flow job's variables (reading). Each
  frame carries its flow (`frame.def`), its stored `flow_env`
  (`frame.envSrc`) and its start (`frame.started`, a failure module's
  `started_at`), so a subflow's settings apply inside it.
- **Outcome.** The step's result is the subflow's. Its job's status is the
  subflow job's: success when its failure module recovered, skipped when
  a clean stop with `skip_if_stopped` ended it. A subflow that failed, or
  that ran its failure module, fails the step, a recovered one included
  ([CF-V1](../../docs/semantics/control-flow.md#cf-v1),
  [FX-33](../../docs/semantics/execution.md#fx-33)), and the caller's
  `continue_on_error` and failure module then apply: the boundary resets
  the no-second-run rule below, so both failure modules can run
  (CF-16 item 4). A stop inside it ends only the subflow
  ([CF-12](../../docs/semantics/control-flow.md#cf-12),
  [FX-20](../../docs/semantics/execution.md#fx-20)); the step's own
  `stop_after_if` is evaluated after a success. A chaining error at the
  subflow's first module fails it at its pickup, bypassing its failure
  module ([CF-V3](../../docs/semantics/control-flow.md#cf-v3), FX-15
  context 3).
- **Refused.** A retry with attempts or `retry_if` on a subflow step,
  whose attempts' subflows belong under the attempt's site
  ([ADR 0011](../../docs/design/0011-differential-testing.md)): N3c left
  it to NI, whose cases do not cover it, so it is `later`.

**Failure modules in bodies (N3c).** In a flow with a failure module,
every loop iteration and branch runs as a child flow carrying a copy of
it ([CF-1](../../docs/semantics/control-flow.md#cf-1),
[CF-8](../../docs/semantics/control-flow.md#cf-8): such a loop never
collapses), which runs inside that child, at `<child>/s:failure` with the
module's own id, as a deployed flow's stored node keeps it
([CF-16](../../docs/semantics/control-flow.md#cf-16),
[CF-V4](../../docs/semantics/control-flow.md#cf-v4),
[FX-34](../../docs/semantics/execution.md#fx-34)). It sees the child's
input, `iter` included, and the child's job as `flow_job_id`; its result
is the child's entry. The trace's `ChildFlow.Failure` says the child has
the slot, so that a copy that never ran is `not_run`.

- **No second run.** A child flow whose failure ended in its failure
  module reports that it was handled (`flowEnd.handled`); a container that
  fails by such a child carries it (`outcome.handled`), as a failure,
  under a clean `stop_after_all_iters_if` that keeps the failure
  (`endFailedStop`), or as the error stop a `branchone` or a sequential
  `branchall` forwards for it, and the flow it is in, and any flow above,
  decides as if it had no failure module for that failure (CF-16 item 4):
  the failure ends the flow, its `continue_on_error` still tolerating it.
  An error stop of the container's own predicate is a failure of its own,
  which the failure module handles; a subflow step resets the flag.
- **Counting** ([CF-V1](../../docs/semantics/control-flow.md#cf-v1)): a
  sequential loop, a sequential `branchall` and a `branchone` count a
  child that ran its failure module as failed, a recovered one included;
  a parallel loop and a parallel `branchall` judge each child's status,
  so a recovered child succeeds.
- **Stops.** An error stop in a body runs the child's copy, whose
  completion does not forward the stop (CF-12 item 3): the container sees
  an ordinary failed child.
- **Refused.** A container as the failure module, whose bodies run raw
  with a suffixed copy ([CF-30](../../docs/semantics/control-flow.md#cf-30),
  source-traced only).

**The platform providers (PF2, `providers.go`).** `Config.Platform`, a
`*platform.Services` ([internal/platform](../platform/README.md#services)),
holds the resources, variables and secrets a run reads; nil means none,
so every read is a not-found. A host gives the facade the same services.

- **A leaf's arguments** are resolved when its job is picked up, after its
  job identity, its script's `envs` and H12, before the step runs
  ([JE-41](../../docs/semantics/expressions.md#je-41),
  [JE-42](../../docs/semantics/expressions.md#je-42),
  [WR-5](../../docs/semantics/languages.md#wr-5)): worker argument
  resolution of the stored arguments, with the job's reserved variables
  (its job identity, without the script's `envs` or H12's changes), which
  `$<NAME>` strings read, and a `$res:` reading the resource with the
  job's stored fields (`platform.APIEnv`,
  [WA-V3](../../docs/semantics/client-api.md#wa-v3)). The step gets the
  resolved arguments, which the trace records as `ResolvedArgs` however
  its job then ends (a timeout, a cancellation or an executor's error
  included), and the job keeps the stored ones (`RecordedArgs`,
  [JE-V3](../../docs/semantics/expressions.md#je-v3)). Expressions see the
  markers, whatever produced them. A failure fails the job before it runs,
  with the worker's execution error, origin `engine`:
  `{"error":{"message":"execution error:\n<the failure's display>","name":"ExecutionErr","step_id":…}}`,
  which the flow handles as any failure.
- **`flow_env`** is resolved the same way, with the flow job's reserved
  variables, each time a module starts, into the frame that runs it
  (JE-38): a child flow inherits its flow's stored map
  (`frame.envSrc`) and resolves it as its own transitions come, a
  subflow by path keeps its own when it defines one (CF-17, N3c), and a
  parallel iteration's frame is its own, so no two goroutines share one.
  Reading: a child flow's or a subflow's `$WM_*` strings are the
  top-level flow job's. A failure leaves the map as stored, with the
  warning `flow_env_unresolved` in the trace, and is not cached; a success
  becomes the frame's cached map.
- **`variable(path)` and `resource(path)`** answer at every site, which
  all have an authenticated client: the variable's value (a secret's
  plaintext), and the resource read as the stock client reads it, without
  a job id (contract (b); which contract Windmill's helper uses is not in
  the reference). A failure throws the API's error text.
  `flow_user_state(key)` (PF3) reads the run's flow user state as the
  API's read gives it: the value stored at key for the run's root, JSON
  `null` for a missing key ([WA-14](../../docs/semantics/client-api.md#wa-14)).

**The job registry and dispatch (PF3, `engine.go`, `dispatch.go`).**

- **Each run is a flow job** of the job registry
  (`platform.Services.JobRegistry`, D3) from its start to its end, its own
  effective root, so that its steps' root lookups, reads with a job id and
  flow user state find it; the facade registers the steps' jobs. Its user
  state is discarded when the run ends
  ([internal/platform](../platform/README.md#the-job-registry-and-flow-user-state)).
- **`RunScript`** runs a root script of the program as a script job: its
  arguments stored and resolved as a step's are, its entrypoint and its
  own `envs` as a step's, its timeout its own or the instance's (WR-37),
  and its outcome stored by `StoreOutcome` without a step id. Its job
  identity is a script job's at its path (WA-4, WA-5), its own root;
  given a dispatch's `parent_job` and `root_job` it is the one the oracle
  recorded for a script that `runScriptByPath` dispatched
  (`conformance/flows/client-api/dispatch`, run `identity`): its parent
  (`WM_FLOW_JOB_ID`) the dispatching job, its innermost scope
  (`WM_ROOT_FLOW_JOB_ID`) the `root_job`, its effective root the first of
  `root_job`, `parent_job` and itself that is set, its flow path the
  dispatching job's runnable path, so its state path is
  `<that path>/nostep_user`, and no step id.
- **A dispatched flow** is a `Run` whose `RunRequest` carries the
  dispatch's links: as the oracle recorded for a flow that `runFlow`
  dispatched, its steps' effective root is then the first of `root_job`,
  `parent_job` and the run that is set, and the `root_job`, when set, is
  their innermost scope, so they share the dispatching run's flow user
  state; without links (`runFlowAsync`) the run is its own root, as any
  other. The run's own job is registered in its effective root's tree.
- **`Dispatcher`** is the default dispatcher of ADR 0007
  (`platform.Dispatcher`): it runs a dispatched target from the engine's
  program, a script root through `RunScript` for `jobs/run/p` and a flow
  root through `Run` for `jobs/run/f`, as a separate run of the same
  runtime that starts at once (no default cap delays it, D15) and is
  detached from the call and from the dispatching run; it ends on its own
  or when the dispatcher closes (`Close`), which cancels what still runs.
  Any other target is not in the bundle (`platform.NotInBundleError`,
  ADR 0001 §5a), and arguments that are not a JSON object jsonb could
  store are a 400. A run's links (its `parent_job`, `root_job` and the
  parent job's runnable path, from the job registry) are taken when the
  dispatch is accepted, so a run that starts after its parent has ended,
  after `scheduled_in_secs` or at once, keeps them. It answers the result
  polls for its runs; the record of
  a completed run is kept while the dispatching step's root lives in the
  registry, and swept after. A host sets it as the services' `Dispatcher`
  before any run starts, over an executor that is the facade's, so that
  the dispatched runs' calls are attributed.
- **A script root is checked when the engine opens**, as every flow is
  (ADR 0001 §3's rule): a Bun script in either boundary, without its own
  `cache_ttl` (N4).

**Tests.** `engine_test.go` compiles synthetic sync folders with the IR
compiler and runs them on the real QuickJS-ng engine, with steps answered
in Go: transforms and the results routes, the shuffled orders, identity,
`skip_if` and `mock` (a table of their rules), identity jobs with H1 and
H2 and the skipped classification, `stop_after_if` (each stop, a throw, a
non-boolean, the textual `results` condition), the chaining errors, the
argument failures and the keys at the storage boundary, the timeouts'
precedence, repeated ids, every hook seam and its precedence, the
refusals, and concurrent runs under `-race`. `errors_test.go` (N1b)
covers `continue_on_error`, the failure module (its scope, a failing,
skipped or chaining one, no stop), the recovery table, the error-stop
matrix with and without tolerance and a failure module, the unrecoverable
stop, chaining errors with a failure module, the storage failures, the
cancellation outcome and its orderings (an empty flow's included), the
hooks with a failure module, and the identity job's text. `loop_test.go`
(N3a) does the same for for loops: child flows and their scopes and job
identity, collapsing and its previous_result chain, the loop's timeout
and failure texts, `skip_failures`, every break and stop of the flow, the
empty loop, the iterator's errors at both routes, nested `iter`, a
parallel loop's index order, `parallelism`'s release order with a gated
executor (N = 2, 1 and 0, and a different N per child), continue-all, its
start errors, an iterator jsonb refuses, chaining errors in a child flow,
the hooks inside loops, the host signals across parallel iterations,
halt-after in nested loops and after each kind of iteration outcome, the
provenance of generated errors in nested arrays and in an included
result, concurrent runs, and serde's texts. `while_test.go` (N3b) covers
while loops: collapsed and child-flow iterations with their `iter`,
previous_result chain, job identity and break, every way one ends, a loop
that only the cancellation ends, and halt-after. `branch_test.go` (N3b)
covers branches: the predicates' order, scope and throw, the chosen
branch's child flow, scope and job identity, the empty branch and its nil
job's results reads, every stop through a `branchone` and a sequential
`branchall` and one level up, parallel and sequential `branchall` with
their skip rules, empty branches and no branches, its
`stop_after_all_iters_if`, the `same_worker` error, the hooks, halt-after,
a latch and a cancellation in branches, provenance and concurrent runs;
also a clean `stop_after_all_iters_if` stop of a failed `branchall`, while
loop or for loop under `continue_on_error`, a halt-after that waits for
its containers' own stops, for loops' included, and one that stops the
other children of a parallel container at their next module.
`subflow_test.go` (N3c) covers subflows by path: the child flow, its
input, results scope and job identity, a body inside one under a
caller's loop, the caller's results and `flow_env` it reads, every
outcome (a failure, a tolerated one, both failure modules, a recovered
one, each kind of stop), a pickup error, the hooks, `same_worker`, the
executor check through a subflow, and concurrent runs. `bodies_test.go`
(N3c) covers failure modules in bodies: the copy per failed iteration,
its scope and id, no second run with or without `skip_failures` and
through a clean stop of a failed inner loop under a loop, a `branchone`
and a sequential `branchall`, the recovery count of each container, and
an error stop in a body.
`env_test.go` (FU5, `TestScriptEnvs`) checks a path script's `envs` in a
Bun step's environment, a later entry winning and every name the worker
sets later skipped, and none in a native step's, `nativets` or
`//native`. `dispatch_test.go` (PF3) covers the dispatcher: a script
root and a flow root run as separate runs, a failure's value, the
arguments resolved and stored, the script job's identity on its own and
with a dispatch's links (as the oracle recorded it), the refusals of a
target not in the bundle or of the wrong kind and of a hash, arguments
that are not an object, a run detached from its call that reports
started and is canceled by `Close`, a scheduled start, the links of a run
that starts after its parent has ended (a script and a flow, delayed and
at once), a script job's own timeout, the records following their root, a
script root refused at open, and a run's flow user state read by
`flow_user_state()` and gone with the run. `providers_test.go` (PF2)
covers the resolved arguments (O2
and O3, whatever produced them, `$WM_*` from the reserved variables in an
argument and from the stored fields in a resource, a secret), O3 after a
timeout, a cancellation and an executor's error, a resolution failure
(whose O3 stays its O2) and the run's settlement after it, `flow_env`
resolved per transition and its unresolved fallback with its warnings,
the helpers and a helper's failure, and an engine with no platform.
`waits_test.go` and `retry_test.go` (N2) are listed with N2's section,
below. The flow fixtures run the engine on the real Bun harness
([cmd/wltest](../../cmd/wltest/README.md#flow-fixtures), the exit gate of
milestones N1a, N1b, N3a, N3b, N3c, PF2, N2, PF3 and NI).

## Waits and time

Milestone N2 ([FX-23](../../docs/semantics/execution.md#fx-23) to
[FX-29](../../docs/semantics/execution.md#fx-29),
[JE-28](../../docs/semantics/expressions.md#je-28),
[JE-29](../../docs/semantics/expressions.md#je-29),
[JE-32](../../docs/semantics/expressions.md#je-32),
[JE-V2](../../docs/semantics/expressions.md#je-v2),
[JE-2](../../docs/semantics/expressions.md#je-2); H4 and H10 of
[ADR 0008](../../docs/design/0008-host-hooks.md)).

**Sleep** (start step 5). A module's `sleep` is read when the next module
starts its first attempt, never on a retry and never before the failure
module: a static value as written, a javascript one evaluated with the
sleeping module's own result. `sleepDelay` reads it as Windmill does: a
JSON integer that fits in 64 unsigned bits is seconds; any other number
(a fraction, an exponent, negative zero, a larger integer) is milliseconds,
value × 1000 truncated, a negative one 0. The next module's `skip_if`,
arguments and timeout are evaluated at once; only its job waits, until
the scheduled time, so a skipped, mocked or identity module waits too. A
final module's sleep is applied by the virtual `<id>-v` identity the
compiler appends ([OF-17](../../docs/semantics/openflow.md#of-17)).

**Timeout** (start step 9). Every module evaluates its own `timeout`,
identity and skipped modules included, though only a leaf's job is timed
(FX-24): static, an i32 or null (OF-V2); javascript, whose value must
deserialise as an i32 (`timeoutResult` and `parseI32` write serde_json's
texts). Its failures are chaining errors, as are a sleep's.

**Retries.** A module with a `retry` is retried, by the decision list of
FX-2, the same in the failure module with its own `retry`:

- `decideRetry` is the completion phase: a failure, or an error stop under
  `continue_on_error`, evaluates `retry_if` and then the attempts left; a
  throw makes the failure unrecoverable (FX-28). A retry due re-enters the
  module, except after an error stop with no failure module, which ends
  the flow (FX-19). An error stop that the module does not tolerate, in a
  flow with a failure module, re-enters a module with a `retry` without
  evaluating it.
- `reenter` is start step 6: `retry_if` again (a throw is now a chaining
  error of a later transition), then the attempts; a retry due moves the
  failed attempt's record to `<site>/r:<k>`, raises the counter, and
  starts the next attempt with the failed attempt's recorded arguments
  (JE-V2: transforms are not evaluated again, a pre-run error is not
  copied, H2 does not see them again), after the delay. Its
  `previous_result` is restored (FX-6, JE-36): the flow input at the first
  module; the preceding module's stored result in jsonb's printed form
  when that module's status is Success; otherwise the JSON string `"{}"`;
  the failure module keeps its own failed attempt's value (FX-32). With no
  retry due, the continue decision proceeds as if none had been.
- `retryDelay` is FX-26's schedule in unsigned 16-bit arithmetic: the
  constant phase, then the exponential phase, seconds^(n+1) saturating at
  65535, times the multiplier wrapping modulo 65536, jittered when
  `random_factor` is positive, all capped at 21600 s.
- The retry state is the flow's (FX-27, FX-29), kept on its frame: a
  counter and the list of failed attempts. Every module's Success or Failure status lists the
  list (`StepRecord.FailedRetries`), so after a tolerated failure the next
  module lists an earlier module's attempts, and an attempt's site counts
  its place in that list; a Success clears both. Starting a failure module
  with its own `retry` resets the counter.
- A chaining error at the root's first pickup fails the first module
  (FX-15 context 2) and can be retried; a retry's re-entry is a later
  transition, so the same failure then fails the flow.

**Waiting** happens on the run's clock (`RunRequest.Clock`, H10), a timer
on the run's context, so a cancellation or a deadline cuts it short; the
queued job then fails without running, with the run's `Canceled` error
(FX-38): a failed job, not a canceled one, as the oracle recorded. Each
requested wait is a `SpanWait` span. `RecordClock` records
waits instead of sleeping them and runs ahead by them; a run whose clock
is not `RealClock` records `H10` among its trace's deviations. The jitter
draws come from `RunRequest.Jitter`, or from a source seeded with the
run's seed, so a fixed seed fixes them.

**Deadlines.** The run's context carries the hard deadline (H4).
`RunRequest.PreWriteDeadline` ends the run canceled, with
`ErrPreWriteDeadline` as its context's cause and Windmill's default
reason and canceler, when it passes before the first write-capable
module launched (H3): a deadline already passed cancels the run before
it starts; one that passes before the first write, or while the host's
`BeforeFirstWrite` decides on it, stops that write. The actual time
decides, under the write gate, never the delivery of the deadline's
timer: an atomic state, armed, disarmed or expired, lets one of the
first write and the timer win, and whoever reads it expired cancels the
run (idempotently) before acting on it, so the run never stops a write
before its context says why. After the first write it no longer
applies. A flow's
own `timeout` is accepted and not enforced (FX-24, path 4).

**The monitor of flows that hang between steps** (JE-2). Windmill's
server cancels a flow job whose transition, the work between two jobs,
outlives `FLOW_ZOMBIE_TRANSITION_TIMEOUT` (60 s). The engine counts, per
flow (a frame; a collapsed iteration's is its loop's flow), the wall time
each transition spends evaluating expressions, whatever the run's clock,
against
`Config.TransitionTimeout` (zero means 60 s, negative disables it), and
past it cancels the run with canceler `monitor` and a reason that begins
as Windmill's (`Flow <id> was cancelled because it was hanging in between
2 steps. Last ping: <time>`, without the URL and worker details). A job's
completion, and a child flow's (a loop iteration's or a branch's),
refreshes the flow's
ping and starts its next transition, as a child's completion refreshes a
Windmill flow job's ping. Hooks
and waits do not count: Windmill has no hooks, and runs a sleep or a
retry delay as a scheduled job. So a runaway expression, whose three
20 s attempts and two 5 s pauses take 70 s, ends the run canceled at
60 s, as on the oracle (JE-2/a).

**The trace.** A failed attempt's record moves to `<site>/r:<k>`; it has
no module status and carries nothing on. Each record's `Attempt` is the
module's attempt it is of, `Job` its leaf job's id, and `Start` and
`Duration` time its job, from its launch after any wait to its outcome,
which is what `wltest check` reads a run's `delays` from.

**Readings where the reference is silent**, each pinned by a test or a
fixture recorded on the oracle:

- A retry draws its jitter once, at re-entry; the completion phase checks
  only `retry_if` and the attempts left.
- A failure with no job, the root's first-pickup chaining error, is
  retried with an empty argument map, as a failed transform's retry is.
- Re-entry that finds no retry due after the completion found one (a
  `retry_if` that changed its answer) proceeds with the continue decision
  as if no retry had been due.
- The failure module's counter reset leaves the list of failed attempts.
- A sleep's or timeout's value that is not JSON (a replaced
  `JSON.stringify`, JE-15) fails with serde_json's syntax error, through
  the helpers N3a's iterator and `parallelism` use (`serde.go`): a broken
  or truncated literal, a character no value starts with, a leading zero,
  then more than white space after the value, at serde_json's positions
  (lines included); its other syntax errors read `expected value` where
  the decoder stopped. An i32 is checked for type and range before what
  follows it. A sleep's evaluation itself fails to read such a text, so
  its error is `Expected a number value, had error instead:
  ExecutionErr("Error parsing result as serde_json::value::Value: <serde
  error>. Value was: <text>")`. The oracle recorded these
  (`fx-23/sleep_not_json`, `fx-24/js_not_json`, `fx-24/js_leading_zero`).
- serde_json names a float of the wrong type in `rustFloat`'s spelling
  (`floating point `1e+21``, recorded by `fx-24/js_exponent`), and a
  negative zero is a float (`-0.0`), which `serdeUnexpected`, shared with
  N3a's iterator and `parallelism` errors, now reads as one; a `Value`'s
  debug form writes its floats so too.
- A sleep beyond about 292 years waits that long, where Windmill's
  scheduled time would overflow.
- serde_json's `Value` debug form writes an object's members in insertion
  order (the backend's `preserve_order`, JE-44).

**Tests.** `waits_test.go` checks the values of `sleep` and `timeout`
(every case of JE-28's and JE-29's texts, with serde_json's positions),
FX-26's schedule (FX-26/a to /e, the wrap in the jitter's product, the
saturating sum, the factor's bounds), the seeded jitter, a `Value`'s
debug form and the recording clock. `retry_test.go` runs flows: argument reuse
and the attempt records, the flow-wide counter and list (FX-29/a and /b,
and an attempt whose site counts an earlier module's), FX-26/a to /e and
the jitter through the engine on the recording clock, `retry_if` (false,
true, its scope, a throw with and without a failure module, a throw at
re-entry), the error stops with a retry, the failure module's retry and
its counter reset, the restored `previous_result` from storage and the
`"{}"` fallback, sleeps (each value, after a skip and a tolerated
failure, the final module's, not on a retry, not before the failure
module, its chaining errors) and a canceled sleep, the timeouts' values
and contexts (every module evaluates its own, the root's Anyhow and its
retry, a failed transform with a negative timeout, the evaluated value
timing the leaf), the monitor (a runaway expression, one transition at a
time, two evaluations in one transition, disabled) and the pre-write
deadline (one already passed at the start, one that passes while the
host approves the first write, its states decided by the actual time,
and a timer that wins the expiry but has not cancelled yet), that the monitor's budget ignores a recording clock other runs
move on, that child flows refresh their loop's ping, and sleep and
timeout values that are not JSON.

## Containers and waits

Milestone NI integrates N1b's errors and N2's waits and retries with N3a's
and N3b's containers ([CF-21](../../docs/semantics/control-flow.md#cf-21)
to [CF-28](../../docs/semantics/control-flow.md#cf-28)). The fixtures
`conformance/flows/control-flow/cf-21` to `cf-28` and `waits`, recorded on
the oracle by NI (`internal/oracle/testdata/batches/ni.json`), settle what
the reference left to them.

- **Inside a body.** A child flow (a loop iteration's or a branch's) runs
  N2's sleeps, timeouts and retries as any flow does, with a retry state
  and a transition monitor of its own (FX-27), so the iterations of a
  parallel loop retry independently. A body whose last module has a
  sleep ends in the virtual `<id>-v` identity, which the trace marks
  (`ChildFlow.Virtual`, CF-26). A body module with a sleep or a retry
  never collapses (CF-8), so a collapsed iteration has neither.
- **The sleep before a container** (CF-21) delays its first push of
  children: the first child of a sequential loop or `branchall`, the
  chosen branch of a `branchone`, every child of a parallel loop or
  `branchall`, all pushed at once (recorded by
  `cf-21/sleep_before_par`); later children do not wait. Such a child
  flow is scheduled (`frame.due`): it is picked up once the delay has
  passed, before any of its modules starts, so a first module that
  completes without a job, an empty container, does not use the delay
  up, and a cancellation while it is queued fails it without running its
  body (CF-28); a collapsed iteration's body job waits as any job does. A
  container's retry delay schedules its first push the same way. A
  container's own sleep delays the module after it, evaluated with the
  container's result, as any module's.
- **A container's timeout** is evaluated for each child it pushes, in the
  container's own scope: its previous_result, its preceding id and its
  flow's input, with no `iter`, a collapsed loop's included (recorded by
  `cf-21/timeout_scope_flows` and `timeout_scope_collapsed`). Its failure
  is a chaining error of the container's flow (FX-15), at its pickup for
  the first child of a container that is its first module. It bounds each
  collapsed body job and no child flow (CF-27).
- **A container's retry** (CF-22) runs it again from scratch after the
  backoff: its iterator or predicates again and every child. Its
  `previous_result` is restored as a module's is (FX-6). The re-entry's
  first push of a retried loop (the first child of a sequential loop,
  every child of a parallel one) starts from the stored input of the loop's
  last failed child job, so a child flow sees the failed child's `iter` as
  `iter_parent`, and a collapsed body job transforms the failed job's
  arguments; later iterations start from the loop's own flow input
  (recorded by `cf-22/iter_parent` and `collapsed_input`). The re-entry is
  a later transition, so a chaining error there (its iterator, timeout,
  `parallelism` or a push) fails the flow at once (FX-15 context 1), as a
  leaf's retry does. The failed
  attempt is recorded as Windmill's job tree keeps it: the module's job,
  its last child job that failed, else its last child job, at
  `<site>/r:<k>`, a child flow's modules below it; a `branchall`, which
  has no job of its own, leaves no attempt site, and its attempts still
  count. What else the failed attempt ran is superseded
  (`StepRecord.Superseded`, `ChildFlow.Superseded`), since the walker no
  longer reaches it.
- **`flow_env`** is resolved again (JE-38) at each transition of a
  sequential container's flow that pushes its next child, after the
  previous one completed, so the next push's `skip_if`, timeout and a
  collapsed body job's arguments see what the previous child changed. A
  collapsed iteration's job belongs to its loop's flow, whose resolution
  it uses (`frame.owner`); the predicates evaluated after a child
  completes keep reading the flow's cached map.
- **A sequential loop's `skip_if`** (for or while) is evaluated again
  before every iteration after the first, with the flow's last result as
  `previous_result` (the previous iteration's, or the flow's input for a
  loop at its first module) and the loop's preceding id. When it holds the
  loop ends as a skipped identity job of that result, and its iterations
  so far are superseded (recorded by `cf-21/skip_mid`, `skip_mid_flows`
  and `skip_mid_while`); a throw is a chaining error of a later
  transition. A parallel loop evaluates it once (`cf-21/skip_parallel`).
- **Cancellation** (CF-28) reaches every child: a canceled container
  completes as a failure whose array holds, for each child the
  cancellation reached, held ones included, the `Canceled` error (a
  collapsed body job's own killed result), and the children that had
  completed keep theirs (recorded by `cf-28/held`); a canceled
  `branchone` fails with its branch's `Canceled` error. The run then ends
  canceled.
- **`squash`** (CF-25) has no effect: on the Community Edition a squashed
  loop runs as an ordinary loop, collapsed or not, sequential or parallel
  (recorded by the `cf-25` fixture, which contradicts CF-25/a's predicted
  error).
- **A collapsed iteration whose push fails** (its timeout, its arguments'
  storage) has no site, since its job was never pushed; its record is
  superseded.

**Readings where the reference is silent:**

- A parallel loop's failed attempt, and the input its retry starts from,
  are those of the last child to fail in the order the loop received
  their completions (no recording discriminates).
- A container's timeout is evaluated for each child before its
  `parallelism`, in the order the reference lists them (pinned by
  `TestContainerTimeoutPerChild`).
- The flow's last result that a mid-loop `skip_if` sees for a loop of
  child flows is the previous child flow's result, as for a collapsed
  loop (recorded by `cf-21/skip_mid_flows`).

**Tests.** `containers_test.go` covers the sleep before every kind of
container on the recording clock (a child flow whose first module is an
empty loop, and a cancellation while the child is queued, included),
`flow_env` resolved again before each sequential push, a container's
timeout per child and its routes, a container's retry (the re-run, the attempt record, `iter_parent`,
a `branchall`, spent attempts), the mid-loop `skip_if` (collapsed, child
flows, while, parallel, a throw), `squash`, and the waits and retries in a
parallel loop's child flows with the virtual identity; `while_test.go`
and `branch_test.go` cover a canceled container's failure.

## The walking skeleton

`Skeleton` (milestone W1) is now a loader: it reads a bundle directory
that holds each flow as `flows/<flow path>.flow.json`, the flow's OpenFlow
document as JSON, turns each into the IR, and runs it on an `Engine`. Its
scripts are named by their job paths (`scripts/bun/<flow path>/<id>.mjs`
for an inline step, or `.native.js`), which it records as their inventory
ids. It accepts W1's subset (script steps by path and inline Bun-family
steps, `bun`, `bunnative` and `nativets`; static, javascript and ai
transforms; a static step timeout) and refuses anything else with
`ErrSkeletonUnsupported`, at load. Each step gets its boundary (C3n): an
inline step's follows the compiler's rule (`ir.DeployedLanguage`,
`ir.BoundaryOf`), native for a `nativets` step with or without a
`//native` header ([WR-40](../../docs/semantics/languages.md#wr-40)'s
note) and for a `bun` or `bunnative` step whose header says `//native`;
a path script's is what its metadata in the bundle records
(`SkeletonConfig.ScriptsDir`, by default the bundle directory), Bun when
it has none. `windmill-lite host` serves it until F3a moves host mode onto
`wl` and its bundles.

`testdata/w1/` is the synthetic bundle of W1's tests: the flows, and step
modules written by hand, each with the `.meta.json` signature the build
helper would record (the harness binds arguments by it), among them
`scope_shim.mjs`, which reads the host-provided module `f/w1/scopes.ts`
through a hand-written shim, `scope_copy.mjs`, which bundles its own
copy of it, and `stall.mjs` (flow `f/w1/stall`), which backs up a host's
socket with a large log, starts a child and blocks the event loop, for
the host-mode test of WR-55's child groups in `cmd/windmill-lite`
(milestone FU6).

`skeleton_test.go` checks the loader and the interpreter through it with a
fake executor and the real QuickJS-ng engine: transforms (JE-6's verbatim
answer included), the step timeout, H1, failure values, cancellation and
settlement, and latches. `skeleton_bun_test.go` runs it on the real Bun
harness and reports W1's first per-step latency figure; it needs `WL_BUN`,
like the harness tests.

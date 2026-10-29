# internal/engine

**Current state:** the contracts of milestone K1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)), the
mapping of a Bun step's outcome to Windmill's stored value with the
entrypoint's selection (milestone C3a), and the engine core of milestone
N1a, which runs a compiled program's flows sequentially, with the errors
of milestone N1b: `continue_on_error`, the failure module and recovery,
the error shapes, chaining errors, the final result and the cancellation
outcome, and the waits and time of milestone N2: `sleep`, the step
timeout and its messages, retries with their arithmetic, jitter and
argument reuse, deadlines and the clock. This README
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
| `trace.go` | `Trace`, `StepRecord`, `Origin`, `Late`, `Warning`, `Span` (H9; run spans of [ADR 0011](../../docs/design/0011-differential-testing.md)). The trace a run returns is a snapshot nothing changes later; late output goes to the snapshot `OnRunSettled` receives. A record's `ResultOrigin` and `CarriedOrigin` say who produced its result and, when the engine set it apart, its carried value | N1a fills it; wl returns it |
| `engine.go`, `run.go`, `values.go` | `Engine`, `Config`, `New`, `RunRequest`, `Cancellation`, `ErrUnsupported`, `ErrPreWriteDeadline`, `FatalError`: the engine core (below) | the flow runner of `wltest check`; `Skeleton`; wl (F1) |
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
| `error` | `{"error": E}` with the wrapper's object `E` (it carries `step_id` itself, [WR-18](../../docs/semantics/languages.md#wr-18)), unless it is a pre-shaped `ManualFailure`, kept as it is ([WR-21](../../docs/semantics/languages.md#wr-21), [WR-V5](../../docs/semantics/languages.md#wr-v5)); text that is not JSON is `Non serializable error: <text>` | user, or the outcome's `Origin` when set: `facade` for the facade's `UnsupportedPlatformCall` ([ADR 0007](../../docs/design/0007-platform-interface.md), the 501 policy) |
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

**What it runs (N1a, N1b, N2).** A flow's top-level modules in sequence:
`script` and `rawscript` Bun steps, `identity`, input transforms,
`skip_if`, `mock`, `pass_flow_input_directly`, `stop_after_if`, `sleep`,
a static or javascript `timeout`, `retry` and `retry_if`,
`continue_on_error`, a failure module of those kinds, `flow_env`,
`early_return` (which changes only the synchronous HTTP response,
[FX-37](../../docs/semantics/execution.md#fx-37)) and a flow's own
`timeout`, which nothing enforces: a flow job bypasses every executor
timer ([FX-24](../../docs/semantics/execution.md#fx-24), path 4). `New`
refuses everything else with `ErrUnsupported`, naming the milestone that
implements it, for a top-level module and the failure module alike:
loops and branches (N3a, N3b); subflows by path (N3c); `cache_ttl` (N4);
host-provided sites (F2a); a step of another harness than Bun's, whose
`bun` and `native` boundaries it runs (L1); a preprocessor, `skip_expr`
and an `ai` sleep or timeout (`later`).

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
   configuration of `Config.Host` and `Config.Workspace`.
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
   the way to the failure module, and never while a retry is due.

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
- its input transforms also see `started_at`, the run's start time as an
  RFC 3339 string with 0, 3, 6 or 9 fraction digits, and `flow_job_id`, the
  run's id; its `WM_FLOW_STEP_ID` is its own id;
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
  are the recorded and the resolved ones, since `$var:` and `$res:`
  resolution is PF2's.

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

Every site has `flow_input`; `flow_env` is the flow's own, in its stored
form ([JE-38](../../docs/semantics/expressions.md#je-38)), at
`stop_after_if` and `retry_if` only when the text names it. No site has helpers yet:
`variable()`, `resource()` and `flow_user_state()` reject with
[JE-20](../../docs/semantics/expressions.md#je-20)'s "not available"
texts until the platform providers (PF2).

**The results context.** The run answers the evaluator's lookups
([JE-18](../../docs/semantics/expressions.md#je-18),
[JE-19](../../docs/semantics/expressions.md#je-19)) from the jobs it has
run: `Lookup` (the engine path) gives the job result of the first module
with that id that has a job, else the node-id map's; `Stored` (JE-9's
direct path) gives the node-id map's, where the module that finished
last wins. Both are job results in storage: after an error stop that is
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
([FX-48](../../docs/semantics/execution.md#fx-48)). Windmill ends the
generic ones with Rust source locations, which windmill-lite does not
write (D14).

**Readings where the reference is silent**, each pinned by a test:

- An identity job reads its arguments from storage, decoded member by
  member as a flow input's are, so its result, the value a skipped, mocked
  or identity module carries on, is its `previous_result` in jsonb's
  printed form, as JE-44's table gives a flow input's member (N1b; N1a
  read it as the compact stored text).
- A module that occurs twice in one list (OF-2): `Lookup` finds the first
  in the flow. The reference does not say which status entry JE-18 step 2
  finds; FX-1's statuses are kept in definition order.
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
hooks with a failure module, and the identity job's text.
`waits_test.go` and `retry_test.go` (N2) are listed with N2's section,
below. The flow fixtures run the engine on the real Bun harness
([cmd/wltest](../../cmd/wltest/README.md#flow-fixtures), milestone N1a's
exit gate).

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
- The retry state is flow-wide (FX-27, FX-29): a counter and the list of
  failed attempts. Every module's Success or Failure status lists the
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
module launched (H3), also while the host's `BeforeFirstWrite` decides,
and that module then does not launch; after that launch it no longer
applies. A flow's
own `timeout` is accepted and not enforced (FX-24, path 4).

**The monitor of flows that hang between steps** (JE-2). Windmill's
server cancels a flow job whose transition, the work between two jobs,
outlives `FLOW_ZOMBIE_TRANSITION_TIMEOUT` (60 s). The engine counts the
wall time each transition spends evaluating expressions, whatever the
run's clock, against
`Config.TransitionTimeout` (zero means 60 s, negative disables it), and
past it cancels the run with canceler `monitor` and a reason that begins
as Windmill's (`Flow <id> was cancelled because it was hanging in between
2 steps. Last ping: <time>`, without the URL and worker details). Hooks
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
- A sleep's or timeout's value that is not JSON (B2b's readback can return
  such text) fails with serde_json's message for the commonest cases.
- A sleep beyond about 292 years waits that long, where Windmill's
  scheduled time would overflow.
- serde_json's `Value` debug form writes an object's members in insertion
  order (the backend's `preserve_order`, JE-44).

**Tests.** `waits_test.go` checks the values of `sleep` and `timeout`
(every case of JE-28's and JE-29's texts, with serde_json's positions),
FX-26's schedule (FX-26/a to /e, the wrap in the jitter's product, the
saturating sum, the factor's bounds), the seeded jitter, the ryu float
form and the recording clock. `retry_test.go` runs flows: argument reuse
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
deadline (one that passes while the host approves the first write
included), and that the monitor's budget ignores a recording clock other
runs move on.

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
through a hand-written shim, and `scope_copy.mjs`, which bundles its own
copy of it.

`skeleton_test.go` checks the loader and the interpreter through it with a
fake executor and the real QuickJS-ng engine: transforms (JE-6's verbatim
answer included), the step timeout, H1, failure values, cancellation and
settlement, and latches. `skeleton_bun_test.go` runs it on the real Bun
harness and reports W1's first per-step latency figure; it needs `WL_BUN`,
like the harness tests.

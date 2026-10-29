# internal/engine

**Current state:** the contracts of milestone K1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)), the
mapping of a Bun step's outcome to Windmill's stored value with the
entrypoint's selection (milestone C3a), the engine core of milestone
N1a, which runs a compiled program's flows sequentially, with the errors
of milestone N1b (`continue_on_error`, the failure module and recovery,
the error shapes, chaining errors, the final result and the cancellation
outcome), and the for loops of milestone N3a: sequential, collapsed and
parallel, with `parallelism`. This README
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
| `precedence.go` | `Start`, `Completion`, `AfterContinue` and their `Verdict` | N1a's decision list |
| `trace.go` | `Trace`, `StepRecord`, `ChildFlow`, `Origin`, `Late`, `Warning`, `Span` (H9; run spans of [ADR 0011](../../docs/design/0011-differential-testing.md)). The trace a run returns is a snapshot nothing changes later; late output goes to the snapshot `OnRunSettled` receives | N1a fills it; wl returns it |
| `engine.go`, `run.go`, `values.go` | `Engine`, `Config`, `New`, `RunRequest`, `Cancellation`, `ErrUnsupported`, `FatalError`: the engine core (below) | the flow runner of `wltest check`; `Skeleton`; wl (F1) |
| `frame.go`, `loop.go`, `serde.go` | the flows a run executes (the root and its loops' child flows), their results scopes, and for loops (below); the texts of Windmill's JSON library | N3a; N3b and N3c run their containers on frames |
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
- **Concurrent calls.** The iterations of a parallel loop run on
  goroutines of their own, so one run's hooks are called concurrently: a
  `Hooks` implementation must be safe for concurrent use.
- **Not hooks in this interface:**
  - H4 (cancellation and budgets) reaches the engine as the run's context
    and its deadlines;
  - H11 (host scopes) lives in the Bun harness.
- **Left for later milestones:**
  - H13 (per-run platform providers) arrives with the platform milestones;
  - the retry-jitter source of H10 arrives with the retry arithmetic (N2).

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
| `error` | `{"error": E}` with the wrapper's object `E` (it carries `step_id` itself, [WR-18](../../docs/semantics/languages.md#wr-18)), unless it is a pre-shaped `ManualFailure`, kept as it is ([WR-21](../../docs/semantics/languages.md#wr-21), [WR-V5](../../docs/semantics/languages.md#wr-v5)); text that is not JSON is `Non serializable error: <text>` | user, or the outcome's `Origin` when set: `facade` for the facade's `UnsupportedPlatformCall` ([ADR 0007](../../docs/design/0007-platform-interface.md), the 501 policy), `harness` for the errors the harness generates in place of the worker's ([WR-37](../../docs/semantics/languages.md#wr-37)'s signature error, a native job's `execution error:` of [WR-43](../../docs/semantics/languages.md#wr-43)), which the `result` frame names |
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

**What it runs (N1a, N1b, N3a).** A flow's modules in sequence: `script`
and `rawscript` Bun steps, `identity`, input transforms, `skip_if`,
`mock`, `pass_flow_input_directly`, `stop_after_if`, a static `timeout` or
none, `continue_on_error`, a failure module of those kinds, `flow_env` and
`early_return` (which changes only the synchronous HTTP response,
[FX-37](../../docs/semantics/execution.md#fx-37)), and for loops with
`stop_after_all_iters_if` (below). `New` refuses everything else with
`ErrUnsupported`, naming the milestone that implements it, for a
top-level module, a loop body's and the failure module alike: `sleep`, a
javascript or negative `timeout`, a retry with attempts or `retry_if`, a
flow timeout (N2); while loops and branches (N3b); a for loop in a flow
with a failure module, whose bodies carry a copy of it, and subflows by
path (N3c); `cache_ttl`, a path script's own included (N4);
host-provided sites (F2a); a step of another harness than Bun's, whose
`bun` and `native` boundaries it runs (L1); a squashed loop (EE,
[CF-25](../../docs/semantics/control-flow.md#cf-25)); an `ai`
parallelism, whose error the reference does not give; a preprocessor and
`skip_expr` (`later`).

**One module** follows [FX-2](../../docs/semantics/execution.md#fx-2):

1. **Host start checks** (`Start`): the latch, cancellation, `haltBefore`.
   Then `OnModuleStart` (H6).
2. **`skip_if`** ([FX-17](../../docs/semantics/execution.md#fx-17),
   [JE-25](../../docs/semantics/expressions.md#je-25)), a predicate whose
   value must be JSON `true` or `false`
   ([JE-24](../../docs/semantics/expressions.md#je-24)).
3. **Arguments**, by the first rule that applies: `mock` (`{previous_result:
   <value or null>}`, [FX-18](../../docs/semantics/execution.md#fx-18)),
   `pass_flow_input_directly` (the flow input), an identity or skipped
   module (`{previous_result: P}`), else the input transforms
   ([FX-7](../../docs/semantics/execution.md#fx-7)). A module that
   launches its own job, a leaf or an identity module, has its arguments
   go through H2; a module that `skip_if` or `mock` answered does not
   ([ADR 0008](../../docs/design/0008-host-hooks.md)). Every module's
   arguments go through the storage boundary.
4. **Launch.** A mocked or skipped module is replaced by an identity job,
   which no hook sees. An `identity` module launches its own identity job,
   which H1 may answer for like any launch. An identity job's result is
   its stored arguments' `previous_result`, or `{}`
   ([FX-16](../../docs/semantics/execution.md#fx-16)), and it is never
   timed ([FX-24](../../docs/semantics/execution.md#fx-24)). A leaf is
   offered to H1 (`Intercept`), then its entrypoint is chosen
   (`Entrypoint`), H3 asks `BeforeFirstWrite` once per run before the
   first write-capable launch, H12 edits the environment, and the step
   runs through the `StepExecutor` with its timeout as a timer in the
   engine. The timeout is the module's static one, else a path script's
   own timeout metadata; a request strictly below the instance maximum is
   job-specific, zero included (no time at all); none, or one at or above
   the maximum, gives the maximum, instance-wide (FX-24, FX-25, WR-37).
   `StoreOutcome` maps the outcome, H1's included; a leaf's result or
   failure value that jsonb refuses fails its job instead (FX-48 case 1,
   below).
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
   `BASE_URL`) or one already set is skipped, and a later entry wins
   ([WR-22](../../docs/semantics/languages.md#wr-22),
   [WA-2](../../docs/semantics/client-api.md#wa-2); `env.go`). A native
   step gets no `envs` ([WR-45](../../docs/semantics/languages.md#wr-45)),
   milestone FU5.
5. **`stop_after_if`** after a success, except on the failure module, and
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
6. **`OnModuleEnd`** (H6), then the host completion checks (`Completion`):
   the latch, cancellation (except for an error stop in a flow with a
   failure module, below), a fatal error name (H7, which latches a
   `FatalError` and records `H7` in the trace's deviations), then
   Windmill's continue decision (below). Last, `AfterContinue` applies
   `haltAfter` when the flow would go on, never on the way to the failure
   module.

**The continue decision** (N1b; [FX-2](../../docs/semantics/execution.md#fx-2)
completion steps 3, 7 and 8, in a flow with no retry, which is N2's).
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

Every site has `flow_input`; `flow_env` is the flow's own, in its stored
form ([JE-38](../../docs/semantics/expressions.md#je-38)), at
`stop_after_if` only when the text names it. No site has helpers yet:
`variable()`, `resource()` and `flow_user_state()` reject with
[JE-20](../../docs/semantics/expressions.md#je-20)'s "not available"
texts until the platform providers (PF2).

**The results context.** The run answers the evaluator's lookups
([JE-18](../../docs/semantics/expressions.md#je-18),
[JE-19](../../docs/semantics/expressions.md#je-19)) from the jobs it has
run, per flow (a frame, `frame.go`: the root, or a loop iteration's child
flow): `Lookup` (the engine path) gives the job result of the first module
of the current flow with that id that has a job (a loop's is the array of
its iterations' results), else the node-id map's; `Stored` (JE-9's direct
path) gives the node-id map's, where the module that finished last wins.
The map has scopes ([CF-19](../../docs/semantics/control-flow.md#cf-19)):
the root's, and one per parallel iteration; a sequential iteration writes
into its parent's, and a lookup walks from a scope to the root. Both are job results in storage: after an error stop that is
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
| a collapsed body's input transform fails | as an input transform's, the message prefixed `could not transform input using an expr: ` | JE-23, JE-39 |
| `stop_after_all_iters_if` fails | an error stop of the flow: ``{"error": {"name": "EarlyStopError", "message": "Error evaluating stop_after_all_iters_if expression `<text>`: <predicate text>"}}``, carried by the loop, whose result stays its array | CF-13, JE-27 |
| an invalid `_ENTRYPOINT_OVERRIDE` | `{"error": {"message": "Invalid entrypoint override: <value>", "name": "ExecutionErr"}}`, before the step runs | WR-12 |
| the step's timeout | `TimeoutError`'s value for the step's boundary: for Bun, `execution error:\njob process terminated due to timeout after exceeding <job-specific or instance-wide> duration limit`; for native (C3n), `execution error:\nQuery timeout after (>Ns)`, whichever limit applied; `ExecutionErr`, `step_id` | WR-37, WR-44 |
| a job's result or failure value that jsonb refuses | the job fails: `{"error": {"message": "Internal: Could not add completed job <job id>: error returned from database: <PostgreSQL's message>", "name": "InternalErr"}}`, no `step_id`, at once where Windmill retries the insert for about 30 s first | FX-48 case 1, OR-27 |
| the run is canceled | the run's `Canceled` error, and the killed step's `ExecutionErr` (above) | FX-38 |

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
its own.

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
- For loops (N3a): an empty body does not evaluate the iterator; a
  collapsed iteration's previous id is empty, and a read of its own loop
  sees the iterations so far, `[]` at first; on a loop that failed only an
  error stop of `stop_after_all_iters_if` changes its outcome (with no
  failure module, a clean stop and the failure both end the flow with the
  array); a loop whose `stop_after_if` throws fails as a step's does
  (FX-21), with no `stop_after_all_iters_if` after it; a static null
  `parallelism` is no limit, as a static null timeout is none; a collapsed
  body takes its loop's `pass_flow_input_directly` instead of its own
  (CF-8 item 5) and then receives its iteration's flow input; a loop's
  `skip_if` is evaluated once, not before
  every iteration ([CF-21](../../docs/semantics/control-flow.md#cf-21) is
  `later`, [Q-CF-6](../../docs/semantics/control-flow.md#q-cf-6)); the
  text of P after a loop is the compact array with numbers re-printed
  ([JE-44](../../docs/semantics/expressions.md#je-44)), written with
  FX-42's exponent spellings (`1e+21`); an iterator or `parallelism` whose
  text is not JSON (a replaced `JSON.stringify`,
  [JE-15](../../docs/semantics/expressions.md#je-15)) fails with serde_json's
  syntax error for an empty text, a character no value starts with, a
  broken `true`, `false` or `null` (``expected ident at line 1 column 2``),
  a leading zero (``invalid number at line 1 column 2``) or more than white
  space after a value (``trailing characters at line 1 column 2``), which
  serde_json checks after the value's type and range, and serde_json's
  other syntax errors are not reproduced; whatever is not one JSON value
  of the expected kind is refused.

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
   [CF-V2](../../docs/semantics/control-flow.md#cf-v2)).
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
module's halt-after asks the loops that contain it, at every level and
given how its flow ended, whether the run would start more: a further
iteration after a success or a failure `skip_failures` tolerates, an
iteration a parallel loop still holds, or what follows a loop, which a
stop breaks to and a failure `skip_failures` does not tolerate fails,
unless the loop's `continue_on_error` takes it. So a halt never hides a
failure or a stop that ends the run; reading: a parallel iteration still
running is taken to succeed. A loop so interrupted records the
array so far.

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
result, concurrent runs, and serde's texts. The flow fixtures run the
engine on the real Bun harness
([cmd/wltest](../../cmd/wltest/README.md#flow-fixtures), the exit gate of
milestones N1a, N1b and N3a).

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

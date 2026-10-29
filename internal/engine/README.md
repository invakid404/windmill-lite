# internal/engine

**Current state:** the contracts of milestone K1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)), the
mapping of a Bun step's outcome to Windmill's stored value with the
entrypoint's selection (milestone C3a), and the engine core of milestone
N1a, which runs a compiled program's flows sequentially. This README
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
| `outcome.go` | `StoreOutcome`, `Stored`, `FailureValue`, `LogTail`; `Entrypoint` and `ErrInvalidEntrypoint` (below) | C3a; the step-case runner (`wltest check`) and the engine call them |
| `settle.go` | `RunSettlement`: counts a run's unsettled invocations and says once when the run has settled | N1a calls `Hooks.OnRunSettled` from it |
| `hooks.go` | `Hooks` (H1–H3, H6–H9, H12 of [ADR 0008](../../docs/design/0008-host-hooks.md)), `NoHooks`, `HookLatchError`, `Site`, `Run`, `Latch` (H5), `Clock` and `RealClock` (H10); `StaticIntercepts`, H1's no-code form | N1a calls them; F2a, F2b and F2c expose them to hosts |
| `precedence.go` | `Start`, `Completion`, `AfterContinue` and their `Verdict` | N1a's decision list |
| `trace.go` | `Trace`, `StepRecord`, `Origin`, `Late`, `Warning`, `Span` (H9; run spans of [ADR 0011](../../docs/design/0011-differential-testing.md)). The trace a run returns is a snapshot nothing changes later; late output goes to the snapshot `OnRunSettled` receives | N1a fills it; wl returns it |
| `engine.go`, `run.go`, `values.go` | `Engine`, `Config`, `New`, `RunRequest`, `ErrUnsupported`, `FatalError`: the engine core (below) | the flow runner of `wltest check`; `Skeleton`; wl (F1) |
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
| `ok` | the result's producer text | — |
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

`outcome_test.go` checks every row, the bypass's edges, the tail and the
entrypoint's rules.

## Settlement

An invocation settles when its entrypoint call can run no more code, and
`Reporter.Settled` says so exactly once per `Execute` call:

| `SettleKind` | When | Relative to `Execute` returning |
|---|---|---|
| `completed` | the call returned or threw | before, normally; later for an abandoned call, or one whose outcome was fixed before it ended (an intercepted `process.exit`, D11, or an attributed process-level error that failed it as `escaped`) |
| `not_started` | the invocation never reached a harness | before |
| `process_exited` | the harness process ended before the call did | before when the invocation was in flight; later when it had been abandoned or its outcome was fixed early |

- **Warnings.** `Warning` hands over a process-level error no invocation
  owned (code `unattributed_error`), or what the invocation still had when
  it settled (code `left_behind`, its `Left` counting the timers, requests
  and child processes by kind), which the settle-time fence then revoked
  (D9). Neither changes the outcome.
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

**What it runs (N1a).** A flow's top-level modules in sequence: `script`
and `rawscript` Bun steps, `identity`, input transforms, `skip_if`,
`mock`, `pass_flow_input_directly`, `stop_after_if`, a static `timeout`
or none, `flow_env` and `early_return` (which changes only the synchronous HTTP
response, [FX-37](../../docs/semantics/execution.md#fx-37)). `New` refuses
everything else with `ErrUnsupported`, naming the milestone that
implements it: a failure module and `continue_on_error` (N1b); `sleep`, a
javascript or negative `timeout`, a retry with attempts or `retry_if`, a
flow timeout (N2); loops and branches (N3a, N3b); subflows by path (N3c);
`cache_ttl` (N4); host-provided sites (F2a); a step outside the Bun
boundary (C3n); a preprocessor and `skip_expr` (`later`).

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
   `StoreOutcome` maps the outcome, H1's included. The environment H12
   edits is the step's job identity
   ([internal/platform](../platform/README.md), milestone PF1): a fresh
   `WM_JOB_ID` (the invocation's id), the run as `WM_FLOW_JOB_ID`,
   `WM_ROOT_FLOW_JOB_ID` and `WM_ROOT_JOB_ID`, the flow's path, the module
   id, the job path, `WM_STATE_PATH`, a fresh `WM_TOKEN`, and the host
   configuration of `Config.Host` and `Config.Workspace`.
5. **`stop_after_if`** after a success, except on an identity job
   classified skipped: any identity job of a module with a `skip_if`,
   whatever the predicate answered, and whether it ran or H1 answered for
   it ([FX-17](../../docs/semantics/execution.md#fx-17),
   [FX-18](../../docs/semantics/execution.md#fx-18),
   [FX-19](../../docs/semantics/execution.md#fx-19)). A clean stop ends
   the flow with the module's result, `skipped` when `skip_if_stopped` (the
   module is then skipped too) and `success` otherwise; an error stop fails
   the module, which carries `{"error": {"name": "EarlyStopError",
   "message": <error_message, or "stop after if: <expr>" when empty>,
   "result"?}}` while its job keeps its success
   ([FX-V2](../../docs/semantics/execution.md#fx-v2)).
6. **`OnModuleEnd`** (H6), then the host completion checks (`Completion`):
   the latch, cancellation, a fatal error name (H7, which latches a
   `FatalError` and records `H7` in the trace's deviations), then
   Windmill's continue decision. With no failure module, retry or
   tolerance yet, a failed module fails the flow with the value it
   carries ([FX-36](../../docs/semantics/execution.md#fx-36)). Last,
   `AfterContinue` applies `haltAfter` when the flow would go on.

The flow's result is the last module's carried value, stored as jsonb; a
flow with no modules returns its input
([FX-3](../../docs/semantics/execution.md#fx-3)). A latched, halted or
canceled run has no result (the cancellation's `Canceled` value is N1b's).

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
| the step's timeout | `execution error:\njob process terminated due to timeout after exceeding <job-specific or instance-wide> duration limit`, `ExecutionErr`, `step_id` | WR-37 |

A predicate's `<text>` is `Error: <evaluator text>` for a failed
evaluation, or `Expected a boolean value, found: "<value's JSON>"`
([JE-24](../../docs/semantics/expressions.md#je-24)); an argument's is
`error returned from database: <PostgreSQL's message>`
([FX-48](../../docs/semantics/execution.md#fx-48)). Windmill ends the
generic ones with Rust source locations, which windmill-lite does not
write (D14).

**Readings where the reference is silent**, each pinned by a test:

- An identity job reads its arguments from storage, so its result, the
  value a skipped, mocked or identity module carries on, is in stored form,
  compact: JE-44's table of texts does not list an identity job's result.
- A module that occurs twice in one list (OF-2): `Lookup` finds the first
  in the flow.
- An H1 intercept runs no job (`Ran` false), and H3 asks only before a
  leaf's launch that H1 declined: an intercepted module or an identity job
  writes nothing.
- An argument that jsonb refuses fails the push with PostgreSQL's message
  ([Q-FX-8](../../docs/semantics/execution.md#q-fx-8) leaves the text open).
- An invalid entrypoint override fails as a pre-run error, with no
  `step_id`.

**Tests.** `engine_test.go` compiles synthetic sync folders with the IR
compiler and runs them on the real QuickJS-ng engine, with steps answered
in Go: transforms and the results routes, the shuffled orders, identity,
`skip_if` and `mock` (a table of their rules), identity jobs with H1 and
H2 and the skipped classification, `stop_after_if` (each stop, a throw, a
non-boolean, the textual `results` condition), the chaining errors, the
argument failures and the keys at the storage boundary, the timeouts'
precedence, repeated ids, every hook seam and its precedence, the
refusals, and concurrent runs under `-race`. The flow
fixtures run the engine on the real Bun harness
([cmd/wltest](../../cmd/wltest/README.md#flow-fixtures), milestone N1a's
exit gate).

## The walking skeleton

`Skeleton` (milestone W1) is now a loader: it reads a bundle directory
that holds each flow as `flows/<flow path>.flow.json`, the flow's OpenFlow
document as JSON, turns each into the IR, and runs it on an `Engine`. Its
scripts are named by their job paths (`scripts/bun/<flow path>/<id>.mjs`
for an inline step), which it records as their inventory ids. It accepts
W1's subset (script steps by path and inline Bun steps, static, javascript
and ai transforms, a static step timeout) and refuses anything else with
`ErrSkeletonUnsupported`, at load. `windmill-lite host` serves it until
F3a moves host mode onto `wl` and its bundles.

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

# internal/engine

**Current state:** the engine runs the flows and root scripts of a compiled
[program](../../docs/glossary.md#program): steps, loops, branches, subflows by path, failure modules,
retries, sleeps, timeouts, cancellation, the result cache, the platform
reads and dispatch. It does not run a preprocessor, a flow's `skip_expr`,
an `ai` sleep or timeout, a container as the failure module, or a step in
a harness other than Bun's. `New` refuses such a flow before any run
starts ([What New refuses](#what-new-refuses)).

This README covers `internal/engine`, which has no `testdata/`.

The package is windmill-lite's [engine](../../docs/glossary.md#engine): the
Go interpreter of [flows](../../docs/glossary.md#flow). It walks a flow's
[steps](../../docs/glossary.md#step) by Windmill's decision list, evaluates
their [expressions](../../docs/glossary.md#expression), and sends each
script step to a [harness](../../docs/glossary.md#harness) as an
[invocation](../../docs/glossary.md#invocation).

- **Callers:** [wl](../../wl/README.md), which opens a
  [bundle](../../docs/glossary.md#bundle) and gives the engine its harness
  and [hooks](../../docs/glossary.md#hook), and `wltest check`
  ([cmd/wltest](../../cmd/wltest/README.md)), which runs the flow fixtures.
  [internal/harness](../harness/README.md) implements `StepExecutor`, and the
  [platform facade](../platform/facade/README.md) wraps it.
- **What it calls:** [internal/ir](../ir/README.md) (the program),
  [internal/expr](../expr/README.md) (expressions),
  [internal/jsonval](../jsonval/README.md) (JSON values and their stored
  text), [internal/platform](../platform/README.md) (resources, variables,
  the job registry, the result cache),
  [internal/openflow](../openflow/README.md) (script languages) and
  [internal/conformance](../conformance/README.md) (site paths and run
  statuses).
- **Design records:** the
  [harness protocol](../../docs/design/0004-harness-protocol.md),
  [JSON at step boundaries](../../docs/design/0005-json-step-boundaries.md),
  [module state](../../docs/design/0006-module-state.md), the
  [platform interface](../../docs/design/0007-platform-interface.md),
  [host hooks](../../docs/design/0008-host-hooks.md),
  [where semantics live](../../docs/design/0003-semantics-placement.md)
  and the [equivalence contract](../../docs/design/0001-equivalence-contract.md),
  whose [named deviations](../../docs/design/0001-equivalence-contract.md#5-named-deviations)
  list each deliberate difference from Windmill that this README names.
- **The rules it follows** are Windmill's, as the
  [semantics reference](../../docs/glossary.md#semantics-reference) states
  them in numbered [findings](../../docs/glossary.md#finding). This README
  says what the engine does in plain words and links each finding.

Site identities are `conformance.SitePath` values, and run statuses are
`conformance.RunStatus` values, with the windmill-lite statuses `latched`
and `halted`. Traces, hooks and the comparator's observations therefore
share one vocabulary.

## Files

| File | What it holds |
|---|---|
| `engine.go` | `Engine`, `Config`, `New`, `Run`, `ProvideFunc`, `ErrProviderDeclined`: opening a program, the checks of `New`, each run's registration |
| `run.go` | A run: the module cursor, the decision list, the failure module, the completion of a flow |
| `values.go` | The argument pipeline and the stored values of a module |
| `frame.go` | A flow's frame: its results scope, its records, its retry state, its job identity |
| `loop.go`, `branch.go`, `subflow.go` | For and while loops, branches, subflows by path |
| `executor.go` | `StepExecutor`, `Invocation`, `StepOutcome`, `Reporter`, `CancelReason`, `SettleKind` |
| `budget.go` | `Budget`, `ByteTotal`, `ErrBudgetSettled`: a run's byte budget and the runtime's total, the account that `wl.reserve` draws on |
| `outcome.go` | `StoreOutcome`, `Stored`, `FailureValue`, `ManualFailure`, `LogTail`, `Entrypoint`, `TimeoutError` |
| `settle.go` | `RunSettlement`, `HoldSettlement` |
| `hooks.go` | `Hooks`, `NoHooks`, `HookLatchError`, `Site`, `Run`, `Latch`, `Clock`, `RealClock`, `StaticIntercepts` |
| `precedence.go` | `Start`, `Completion`, `AfterContinue` and their `Verdict` |
| `waits.go` | `Jitter`, `SeededJitter`, `RecordClock`; the values of `sleep` and `timeout`; the retry schedule |
| `trace.go` | `Trace`, `StepRecord`, `ChildFlow`, `Span`, `PlatformCall`, `Warning`, `Late` |
| `provided.go` | A host-provided site's launch (`ProvideFunc` and `ErrProviderDeclined` are in `engine.go`) |
| `providers.go` | What a run reads from the platform: argument resolution, `flow_env`, the expression helpers |
| `flowenv.go` | What input transforms store when their text reads `flow_env` |
| `env.go` | A path script's own `envs` in a Bun step's environment |
| `dispatch.go` | `RunScript`, `ScriptRequest`, `ScriptResult`, `Dispatcher`, `NewDispatcher` |
| `jobrecord.go` | The job records that the job reads of a live run answer |
| `cache.go` | The `cache_ttl` result cache |
| `readback.go` | Containers whose children's results cannot be read back, and the end of a flow left waiting |
| `serde.go` | The texts of Windmill's JSON library for wrong values |
| `doc.go` | The package comment |

## Public surface

### Open the engine and run a flow

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `New` | func | Opens a program: checks every flow, compiles every expression once. Returns an error wrapping `ErrUnsupported` for a feature it does not run. Other errors are plain: a missing program or expression engine, an unknown module state, shared code outside warm mode, a malformed module, a root script missing from the inventory, or an uncompiled expression (an `ir.json` of version 1) | `wl`, `wltest check` |
| `Config` | struct | The program, the bundle's digest, the default `StepExecutor`, the expression engine, the host's job environment, `InstanceTimeout`, `TransitionTimeout`, `Platform`, `ModuleState`, `SharedCode`, `Provide`, `QuickJSOnly` | `wl`, `wltest check` |
| `Engine` | struct | An opened program. Safe for concurrent use | `wl` holds one per bundle |
| `Engine.Run` | method | Runs one flow to its end and returns its `*Trace`. An error means the run could not start | `wl` (and through it `wltest check`) |
| `RunRequest` | struct | One run: `ID`, `Path`, `Args`, `Hooks`, `Exec`, `ParentJob`, `RootJob`, `ParentPath`, `Seed`, `Clock`, `Jitter`, `Platform`, `PreWriteDeadline`, `Around`, `Started`, `Deviations`, `Limits` | `wl` |
| `RunLimits` | struct | A run's own limits ([Limits per run](#limits-per-run)): `MaxInFlight`, `MaxHeldBytes`, `Bytes`, `ByteTotal` | `wl` |
| `ErrPinnedProcessLost`, `ErrRunMemoryLimit` | var | The latches `pinned_process_lost`, which an executor returns for a pinned run whose process has ended, and `run_memory_limit` | `wl`, `internal/harness` (`ErrPinnedProcessLost`) |
| `Cancellation` | struct | The cause that a host cancels a run with: `Reason` and `Canceler` | `wl`, `internal/harness` |
| `CancelCauseOf`, `CauseDeadline`, `CausePreWriteDeadline`, `CauseCanceled`, `CauseClosing`, `CauseFlowTimeout` | func, consts | The cause of a canceled run ([The cause](#the-cause)) | `wl` |
| `Closing` | var | The `*Cancellation` with which a runtime that closes cancels its runs, whose cause is `CauseClosing` | `wl` |
| `ModuleState` | string type | `ModuleStateWarm` (the default) or `ModuleStateFresh` | `wl`, `internal/harness`, `internal/bundle`, `cmd/windmill-lite`, `wltest check` |
| `Boundary` | string type | A step's boundary: `BoundaryBun` or `BoundaryNative` | `internal/harness`, `wltest check` |
| `ErrUnsupported` | var | Wrapped by `New`'s refusal of a feature that the engine does not run ([What New refuses](#what-new-refuses)); `New`'s other errors do not wrap it | `wl`, `wltest check` |
| `ErrPreWriteDeadline` | var | The context's cause when the pre-write deadline passes | `wl` |
| `FatalError` | struct | The latch of a failure whose error name the host listed as fatal | `wl` |

### The step executor

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `StepExecutor` | interface | `Execute(ctx, Invocation)`: runs one leaf step in a harness | implemented by `internal/harness` (`Pool`, `Process`); wrapped by the facade's `Executor` |
| `Invocation` | struct | One leaf run: ids, site, attempt, script, entry, arguments, environment, boundary, module state, `Report`, `Platform`, `Around`, `Budget` | built by the engine |
| `Budget`, `ByteTotal`, `NewByteTotal`, `ErrBudgetSettled` | interface, struct, func, var | An invocation's draw on its run's byte budget (`Reserve`), the runtime's total that runs share, and `Reserve`'s error once the invocation has settled | `internal/harness` (`Budget`), `wl` (`ByteTotal`) |
| `StepOutcome` | struct | How the invocation ended: `Kind`, `Body`, `Tail`, `ExitCode`, `Signal`, `Origin` | returned by the executor and by `Hooks.Intercept` |
| `OutcomeKind` | string type | `OutcomeOK`, `OutcomeError`, `OutcomeEscaped`, `OutcomeEmpty` | set by `internal/harness`, the facade's `Executor`, `wl` (an `Intercept` outcome) and `wltest check` (its Node runs); read by the engine |
| `Origin` | string type | Who produced an error value: `OriginNone`, `OriginUser`, `OriginEngine`, `OriginHarness`, `OriginFacade`, `OriginHost`; `Origin.Generated` | the comparator |
| `CancelReason` | string type | Why an invocation is abandoned: `CancelTimeout`, `CancelCanceled`, `CancelRunEnded`; `CancelReasonOf(ctx)` | `internal/harness` |
| `Reporter` | interface | Receives `Late`, `Warning` and `Settled` for one invocation | called by the executor |
| `NopReporter` | struct | A `Reporter` that discards everything | `internal/harness`, the facade, tests |
| `Late`, `LateKind` | struct, string type | Output after the outcome was fixed or the invocation was abandoned: `LateResult`, `LateError`, `LateLog` | the executor |
| `Warning` | struct | A warning for the trace, such as `left_behind` or `unattributed_error` | the executor, `Hooks.OnRunEnd` |
| `SettleKind` | string type | `SettleCompleted`, `SettleNotStarted`, `SettleProcessExited` | the executor |
| `WarningLatch`, `WarningPluginError` | const | The warning codes of a step's `wl.latch` and of a host plugin's failure in an around wrapper | `LatchOf` |
| `StepLatch`, `PluginError` | struct | The latch errors of those two warnings | `LatchOf` |
| `LatchOf` | func | Maps a warning to the error it latches, or nil | the engine |

### Hooks and the clock

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Hooks` | interface | `Intercept`, `RewriteArgs`, `WriteCapable`, `BeforeFirstWrite`, `FatalErrorName`, `HaltBefore`, `HaltAfter`, `OnModuleStart`, `OnModuleEnd`, `InvocationEnv`, `OnRunEnd`, `OnRunSettled` | implemented by `wl`'s adapter over its `Hooks` callbacks |
| `NoHooks` | struct | The no-op default of every hook; embed it to implement a subset | `wl` uses `NoHooks{}` as its default hooks |
| `StaticIntercepts` | struct | A fixed result per module id, the no-code form of `Intercept` | `wl`'s host mode |
| `Site` | struct | A call site as hooks see it: `Path`, `Flow`, `Module`, `Script` | hooks |
| `Run` | interface | The handle a hook has on its run: `ID`, `Latch` | hooks |
| `Latch` | struct | A run's latched error; the first `Set` wins | the engine |
| `HookLatchError` | func | Drops a hook's error that is only the run's own cancellation | `wl` |
| `ProvideFunc`, `ErrProviderDeclined` | func type, var | `Config.Provide` and its refusal | `wl` |
| `Clock`, `RealClock`, `RecordClock` | interface, structs | The run's clock: real waits, or recorded waits that return at once (`RecordClock.Waits`) | `wl`, tests |
| `Jitter`, `SeededJitter` | interface, func | The retry jitter's random draws | `wl`, tests |

### Precedence functions

The engine's run loop (`run.go`, and `loop.go` for an iteration's start) calls these three functions; no other package does, except the package's example. [Precedence](#precedence) states their rules.

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Start`, `StartSignals` | func, struct | The host checks before a module, iteration, branch or retry attempt starts | the engine |
| `Completion`, `CompletionSignals` | func, struct | The host checks when a module completes | the engine |
| `AfterContinue`, `ContinueSignals` | func, struct | The halt check after Windmill's continue decision | the engine |
| `Verdict` | uint8 type | `Proceed` or the status that ends the run; `Verdict.Status`, `Verdict.String` | the engine |

### Outcomes and stored values

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `StoreOutcome`, `Stored` | func, struct | Maps a `StepOutcome` to what Windmill stores for a Bun step | the engine, `wltest check`'s step cases |
| `FailureValue` | func | The value a flow sees of a failed step's stored error | the engine |
| `ManualFailure` | func | Applies the `wm_failure` marker to a result | the engine |
| `LogTail`, `TailLength` | func, const | The last 600 characters of a log after the last marker | `StoreOutcome` |
| `Entrypoint`, `EntrypointOverride`, `ErrInvalidEntrypoint` | func, const, var | The export an invocation calls | the engine, `wltest check` |
| `TimeoutError` | func | The failure value of a step's timeout, by boundary | the engine, `wltest check` |

### Settlement types

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `RunSettlement`, `NewRunSettlement` | struct, func | Counts a run's unsettled invocations and holds, and says once when the run has settled: `Track`, `End`, `Hold`, `Open` | the engine, `wl` |
| `HoldSettlement` | func | Holds the settlement that a run's context carries | `wl`'s host plugin support |

### The trace

The engine fills the trace; `wl` returns it to its host and adds the platform calls, and `wltest check` compares its records and child flows with a fixture's expectations.

| Name | Kind | What it does | Who reads it |
|---|---|---|---|
| `Trace` | struct | What a run returns: its status, result, the cause of a cancellation, records, child flows, spans, warnings, deviations, platform calls; `Trace.Step` finds a record by site | `wl`, `wltest check` |
| `StepRecord` | struct | One site: a module, or a failed attempt at `<module>/r:<k>`. Its status, its arguments (recorded and resolved), its result and carried value with their origins, its job, attempt, timings and hooks | `wl`'s hooks, `wltest check` |
| `ChildFlow` | struct | A child flow the run created: its site, id, modules, failure slot | `wltest check`, `wl`'s plugin trace payload |
| `Span`, `SpanKind` | struct, string type | A timed interval: `SpanModule`, `SpanQueue`, `SpanHarness`, `SpanSerialise`, `SpanExpression`, `SpanPlatform`, `SpanWait` | `wl`'s plugin trace payload, the benchmarks of `cmd/windmill-lite` |
| `PlatformCall` | struct | One platform call of a step, as the facade recorded it | `wl` fills it |

### Script jobs and dispatch

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Engine.RunScript`, `ScriptRequest`, `ScriptResult` | method, structs | Runs a root script as a script job. `ScriptResult` has `Failed`, `Result`, `Canceled` and `Latched` | `wl`, the dispatcher |
| `Dispatcher`, `NewDispatcher` | struct, func | The default dispatcher: runs a dispatched root as a separate run; `Dispatch`, `Result`, `Close`, and `SetRuns`, which gives each run a host part (`DispatchRun`), prepared before `Dispatch` returns the run's id. Once `Close` has begun, a dispatch refused at its admission calls none of it; one admitted before is prepared, its context possibly canceled, then refused with its end and settlement calls made. `Close` waits for those within the context that it is given | a host's `platform.Services` |

## Hooks

The engine calls a run's hooks at the points that each `Hooks` method
names, in the order of [Precedence](#precedence). The
[host hooks](../../docs/design/0008-host-hooks.md#the-hooks) design record
defines them. A host gives them as Go callbacks with
[selectors](../../docs/glossary.md#selector) through
[wl](../../wl/README.md#hooks), whose adapter implements this interface
over them.

- **An error latches.** A non-nil error from any hook
  [latches](../../docs/glossary.md#latch) the run, unless it is the run's
  own cancellation. `HookLatchError` drops an error that matches the ended
  context's error or cause. So a hook that gives up on a canceled run
  cannot turn the cancellation into a latch.
- **A hook does not fail a module with an error.** A hook that wants to
  fail a module answers `Intercept` with an error outcome.
- **The first latch wins.** `Latch`, which a hook reaches through
  `Run.Latch`, keeps the first error and drops later ones. It is safe for
  concurrent use.
- **Calls are concurrent.** The iterations of a parallel loop and the
  branches of a parallel `branchall` run on goroutines of their own. So
  the engine calls one run's hooks concurrently, and a `Hooks`
  implementation must be safe for concurrent use.

Some host seams are not `Hooks` methods:

| Seam | How it reaches the engine |
|---|---|
| Cancellation and the hard deadline | The run's context |
| The pre-write deadline | `RunRequest.PreWriteDeadline` ([Deadlines](#deadlines)) |
| The clock and the retry jitter | `RunRequest.Clock` and `RunRequest.Jitter` ([Waiting](#waiting)) |
| Platform providers for one run | `RunRequest.Platform` ([Per-run platform providers](#per-run-platform-providers)) |
| Host scopes | The Bun harness ([host mode](../../harnesses/bun/README.md#host-mode-and-the-host-sdk)) |
| Deviations that a host setting turns on, such as the host's read cache | `RunRequest.Deviations`, which the trace lists among its own |

## Precedence

`Start`, `Completion` and `AfterContinue` are pure functions.
`precedence_test.go` states them case by case, and checks the rules below
over every combination of signals.

1. **`Start`** runs before a module, an iteration, a branch or a retry
   attempt starts. It checks the latch, then cancellation and the
   deadlines, then `HaltBefore`. `HaltBefore` applies only at a module's
   first attempt, and never to a failure module. Then Windmill's own start
   checks run, with `Intercept` at the launch.
2. **`Completion`** runs when a module completes. It checks the latch,
   then cancellation, then a fatal error name (`FatalErrorName`). Then
   Windmill's continue decision runs.
3. **`AfterContinue`** runs after that decision. `HaltAfter` ends the run
   `halted` once the module has its final outcome and the run would start
   something other than a failure module. `ContinueSignals.Final` says
   whether the outcome is final, and its zero value never halts.
   - A container's completion is final only after its last iteration or
     branch.
   - A module's completion is final only when no retry is due.
   - A completion that ends the run anyway keeps Windmill's status.

Cancellation has one exception
([FX-38](../../docs/semantics/execution.md#fx-38)). In a flow with a
failure module, an error stop that coincides with the cancellation still
goes to Windmill's continue decision. What that decision asks for still
starts: a retry when one is due, the next module under
`continue_on_error`, or else the failure module
([FX-19](../../docs/semantics/execution.md#fx-19)). The run then ends
canceled at its next completion.

A halt never turns a canceled run into a halted one. A halt that falls on
such a start stops it, and the run ends canceled.

## Step outcomes

A `StepOutcome` mirrors the harness's `result` frame (`ok`, `error`,
`escaped`, `empty`), before the engine maps it to Windmill's stored value.

- **One type for steps and hooks.** An `Intercept` that answers for a
  module returns the same type, so its result cannot be told apart from a
  step's.
- **Abandonment.** When the invocation's context ends, `Execute` returns at
  once with the context's cause. The engine names the cause with a
  `CancelReason`: `timeout`, `canceled` or `run_ended`.
- **Late output.** An abandoned invocation is not killed in a shared
  process: its JavaScript may run on. What it produces later reaches the
  trace through `Reporter` as `Late`. Both are
  [named deviations](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **`fresh`.** In [`fresh`](../../docs/glossary.md#fresh) the harness
  process is killed instead, so the invocation settles with it and nothing
  comes late.

## Stored values and the entrypoint

`StoreOutcome(out, stepID)` gives what Windmill stores for a Bun step,
which is also what the flow sees of a failed step. The
[harness protocol](../../docs/design/0004-harness-protocol.md) design
record holds the outcome table that it implements.

| Outcome | Stored | Origin |
|---|---|---|
| `ok` | The result's producer text, unless the `wm_failure` marker applies (below) | none, or user |
| `error` | `{"error": E}`, with the wrapper's object `E`, which carries `step_id` itself ([WR-18](../../docs/semantics/languages.md#wr-18)). A pre-shaped `ManualFailure` is kept as it is ([WR-21](../../docs/semantics/languages.md#wr-21), [WR-V5](../../docs/semantics/languages.md#wr-v5)). Text that is not JSON becomes `Non serializable error: <text>` | user, or the outcome's `Origin` when set (below) |
| `escaped`, by a signal | `execution error:\nprocess terminated by signal: <signal>`, `ExecutionErr`, `step_id` ([WR-4](../../docs/semantics/languages.md#wr-4), [WR-37](../../docs/semantics/languages.md#wr-37)) | harness |
| `escaped`, by an exit | `exit code for "bun run": <code>, last log lines:\n<tail>`, `ExecutionErr`, `step_id`, `exit_code` ([WR-20](../../docs/semantics/languages.md#wr-20)) | harness |
| `empty` | `Internal: Result of job is invalid json (empty)`, `InternalErr`, no `step_id` ([WR-V2](../../docs/semantics/languages.md#wr-v2)), and no Rust location suffix | harness |

- **The `wm_failure` marker.** An `ok` result that is an object with a
  string `wm_failure` at its top level fails
  ([WR-17](../../docs/semantics/languages.md#wr-17),
  [FX-13](../../docs/semantics/execution.md#fx-13)). It is kept as it is,
  with `"error": {"name": "ManualFailure", "message": <wm_failure>}` put
  in, in place of an `error` member or else last (`ManualFailure`).
- **What disables the marker.** A `null` marker marks nothing. A
  `wm_failure` that is not a string, or a `wm_labels` that is not `null` or
  an array of strings, disables both. A `null` element in `wm_labels`
  disables them too.
- **The origin of an `error` outcome.** The outcome's `Origin` is `facade`
  for the facade's `UnsupportedPlatformCall` (the 501 policy of the
  [platform interface](../../docs/design/0007-platform-interface.md)). It
  is `harness` for the errors that the harness generates in place of the
  worker's, which the `result` frame names: the signature error of
  [WR-37](../../docs/semantics/languages.md#wr-37), and a native job's
  transpile and load failures and its `//proxy` refusal
  ([WR-43](../../docs/semantics/languages.md#wr-43)).
- **`step_id`** is added only for a flow step (`stepID` not empty).
- **The tail** is `LogTail`'s: the last 600 characters (`TailLength`) of
  what follows the last `CODE EXECUTION ---` marker, without ANSI escapes,
  trimmed.
- **Size limits.** Windmill's size limits on a result
  ([WR-15](../../docs/semantics/languages.md#wr-15)) are not applied.

`Entrypoint(args)` is the export that an invocation calls
([WR-12](../../docs/semantics/languages.md#wr-12)):

- It is the value of `_ENTRYPOINT_OVERRIDE` (`EntrypointOverride`) when the
  argument object carries it, as the flow preprocessor step's always does,
  and `main` otherwise.
- An override that is not a string that matches
  `^[A-Za-z_][A-Za-z0-9_]*$` is refused with an error that wraps
  `ErrInvalidEntrypoint`. Its text is the one that Windmill's worker
  writes after `Bad request: `, and no wrapper runs. The engine fails the
  step with it as the worker's execution error
  ([Failure values](#failure-values)).
- The same argument selects the harness's fallback for binding arguments
  ([WR-V1](../../docs/semantics/languages.md#wr-v1)): when the arguments
  carry `_ENTRYPOINT_OVERRIDE` and the export has no parameters, the
  harness passes the values of the keys that do not start with `_`.
- `Entrypoint` walks the argument object in place for the last member of
  that key (`jsonval.Members`). A step's stored arguments spell a letter
  or an underscore in a key only as itself, so on them `storedEntrypoint`
  walks only when the key's quoted text is there.
- `ManualFailure` walks only an object, and only when its text holds
  `wm_failure` or a `\u` escape, the only ways that a key can spell it.

`outcome_test.go` checks every row, the edges of both walks, the
`wm_failure` marker, the tail and the entrypoint's rules.

## Settlement

An invocation [settles](../../docs/glossary.md#settle) when its entrypoint
call can run no more code. `Reporter.Settled` says so exactly once per
`Execute` call:

| `SettleKind` | When | Relative to `Execute` returning |
|---|---|---|
| `completed` | The call returned or threw | Before, normally. Later for an abandoned call, or for one whose outcome was fixed before it ended: an intercepted `process.exit`, or an attributed process-level error that failed it as `escaped` |
| `not_started` | The invocation never reached a harness | Before |
| `process_exited` | The harness process ended before the call did | Before when the invocation was in flight. Later when it had been abandoned or its outcome was fixed early |

- **Warnings.** `Warning` hands over one of these three things, none of
  which changes the outcome:
  - a process-level error that no invocation owned (code
    `unattributed_error`);
  - what the invocation still had when it settled, which the
    [settle-time fence](../../docs/glossary.md#settle-time-fence) then
    revoked (code `left_behind`; its `Left` counts the timers, requests and
    child processes by kind);
  - what the native boundary handed a native invocation but does not track
    (code `native_untracked`; its `Untracked` counts by kind).
- **Latching warnings.** A warning with code `latch` (`WarningLatch`, a
  step's `wl.latch`) or `plugin_error` (`WarningPluginError`, a host
  plugin's failure in an around wrapper) latches the run (`LatchOf`).
- **Order.** The executor never calls one invocation's `Reporter` from two
  goroutines at once. Its calls come in this order:
  1. `Late` and `Warning`;
  2. at most one `Late` with `Final` set, which says how a late-ending call
     ended;
  3. `Settled`;
  4. then only non-final `Late`: output of work that the call left
     running.
- **Not settlement.** Abandonment is not settlement, and neither is a
  late error without `Final`.
- **Counting.** `RunSettlement` counts each tracked invocation until its
  first `Settled`, and drops duplicates. Once `End` has been called and
  nothing is unsettled, it calls its callback once. There the engine calls
  `OnRunSettled`, in which the host releases what step code can reach, as
  the harness protocol's host mode requires.
- **Holds.** `Hold` counts work that is not an invocation: a host
  plugin's hook, from its call until it has finished. The hook may outlive
  the engine's wait for it, even after `End`. Once the run has settled,
  `Hold` holds nothing and says so, and the work must not start.
- **The run's context.** A flow run's context carries its settlement, so
  whatever serves the run holds it with `HoldSettlement(ctx)`. A context of
  no run, the settlement callback's own, holds nothing, so the callback
  never waits on itself. `RunRequest.Started` hands that context and the
  run's handle to what serves the run outside its hooks, such as a
  plugin's per-run providers.
- **Letting go of a settled run.** Once the run has settled, nothing reads
  its trace again, because `OnRunSettled` has had it. So the Reporters that
  `Track` handed out drop the run, and the settlement drops its callback.
  So an executor that keeps settled invocations for their late output
  keeps no run's values reachable, and late output that still comes goes
  nowhere.

## How a run walks a flow

- **`New(Config)`** opens a compiled program
  ([internal/ir](../ir/README.md)). It checks that every flow stays within
  what the engine runs, and compiles every expression's program once on
  the configured QuickJS-ng engine (`expr.Pipeline`). The compiler has
  already run the expressions' textual stages (`expr.Compile`), so nothing
  is computed again per run.
- **`Run(ctx, RunRequest)`** runs one flow to its end and returns its
  trace. An error means that the run could not start: an unknown flow,
  arguments that are not a JSON object or that jsonb refuses, or no step
  executor.
- **Concurrency.** An `Engine` is safe for concurrent use. Its runs share
  the program, the expression pipeline, the step executor (unless
  `RunRequest.Exec` gives a run its own), the platform services (unless
  `RunRequest.Platform` does) and the engine's result cache
  ([The result cache](#the-result-cache)). Each run has its own trace,
  latch, job map and scheduling state, and the clock of its
  `RunRequest.Clock` or `RealClock`: two runs given one `*RecordClock`
  share its waits.

### What the engine runs

- A flow's modules in sequence: `script` and `rawscript` Bun steps,
  `identity`, input transforms, `skip_if`, `mock`,
  `pass_flow_input_directly`, `stop_after_if`, `sleep`, a static or
  javascript `timeout`, `retry` and `retry_if`, and `continue_on_error`.
- A failure module of those kinds.
- `flow_env`, and `early_return`, which changes only the synchronous HTTP
  response ([FX-37](../../docs/semantics/execution.md#fx-37)).
- A flow's own `timeout`: its wall-clock limit
  ([FX-52](../../docs/semantics/execution.md#fx-52),
  [The flow's own timeout](#the-flows-own-timeout)).
- For and while loops, `branchone`, `branchall`, and subflows by path, in
  a flow with a failure module too ([Failure modules in bodies](#failure-modules-in-bodies)).
- `stop_after_all_iters_if` on a loop or a `branchall`. On any other module
  it is accepted and never evaluated
  ([CF-13](../../docs/semantics/control-flow.md#cf-13),
  [FX-2](../../docs/semantics/execution.md#fx-2)).
- Sleeps, timeouts and retries in, on and before containers
  ([Containers and waits](#containers-and-waits)).
- `cache_ttl` on steps, loops and flows, a path script's own included
  ([The result cache](#the-result-cache)).

### What New refuses

`New` refuses each of these with an error that wraps `ErrUnsupported` and
names what it refuses. It refuses it in a top-level module, in a
container's body and in the failure module alike, so a run never stops
midway on it.

| Refused | Why |
|---|---|
| A container as the failure module | Windmill accepts it, with outcomes that windmill-lite does not reproduce ([A container as the failure module](#a-container-as-the-failure-module)) |
| A step in a harness other than Bun's | The engine runs Bun steps, in the `bun` and the `native` boundaries |
| A preprocessor module, a flow's `skip_expr` | Not supported |
| An `ai` sleep or timeout | Not supported |

### The expression pipeline

The pipeline's Go evaluator of templates, arithmetic, comparisons and
predicates (`jsExpr`) is on unless `Config.QuickJSOnly` is set. It reads
`results.<id>` through the run's frame results. Those declare that their
lookup reads the run's memory under the run's lock and never waits
(`expr.MemoryResults`), so the expression engine also answers that lookup
on its own thread ([internal/expr](../expr/README.md)).

`TestQuickJSOnlyReachesThePipeline` runs a flow whose template, arithmetic
and `skip_if` the evaluator answers. With the evaluator on, nothing reaches
QuickJS-ng; with `QuickJSOnly`, all three do, and the run's result is the
same.

## One module

One module follows the start steps of
[FX-2](../../docs/semantics/execution.md#fx-2):

1. **The host's start checks** (`Start`): the latch, cancellation, then
   `HaltBefore`, at a module's first attempt only. Then `OnModuleStart`,
   for every attempt.
2. **The previous module's `sleep`**, at the module's first attempt
   ([Sleep](#sleep)). It sets when the module's job may start.
3. **`skip_if`** ([FX-17](../../docs/semantics/execution.md#fx-17),
   [JE-25](../../docs/semantics/expressions.md#je-25)): a predicate whose
   value must be JSON `true` or `false`
   ([JE-24](../../docs/semantics/expressions.md#je-24)).
4. **The arguments** ([The arguments of a module](#the-arguments-of-a-module)),
   then the module's own `timeout` ([Timeout](#timeout)). Every module's
   arguments then go through the storage boundary.
5. **The launch** ([The launch](#the-launch)), once the scheduled time of
   a sleep or a retry delay has passed.
6. **`stop_after_if`**, after a success
   ([stop_after_if](#stop_after_if)).
7. **`OnModuleEnd`**, then the host's completion checks (`Completion`):
   the latch, cancellation, then a fatal error name. A fatal error name
   latches a `FatalError`, and the trace lists the fatal-error-name
   deviation. Then Windmill's continue decision runs
   ([The continue decision](#the-continue-decision)), with its retries.
   Last, `AfterContinue` applies `HaltAfter` ([Precedence](#precedence)).

`AfterContinue` applies `HaltAfter` only when the flow would go on: never
on the way to the failure module, and never while a retry is due. When the
module ends a container's child flow, the halt waits until the container
has completed ([Host checks inside containers](#host-checks-inside-containers)).

### The arguments of a module

The first rule that applies gives the arguments:

1. `mock`: `{previous_result: <value or null>}`
   ([FX-18](../../docs/semantics/execution.md#fx-18)).
2. A retry: the failed attempt's arguments ([Retries](#retries)).
3. `pass_flow_input_directly`: the flow input.
4. An identity or a skipped module: `{previous_result: P}`, where P is the
   previous module's carried value.
5. Otherwise, the input transforms
   ([FX-7](../../docs/semantics/execution.md#fx-7)). When one fails, the
   job still launches, with `{}`, and fails without running.

`RewriteArgs` sees the arguments of a module that launches its own job: a
leaf, a flow step or an identity module. It does not see a module that `skip_if` or
`mock` answered, a retry, or a failed transform.

### The launch

- **A mocked or skipped module** is replaced by an identity job, which no
  hook sees.
- **An `identity` module** launches its own identity job, which
  `Intercept` may answer like any launch.
- **An identity job's result** is its stored arguments' `previous_result`,
  or `{}` ([FX-16](../../docs/semantics/execution.md#fx-16)). It is never
  timed ([FX-24](../../docs/semantics/execution.md#fx-24)).
- **The `wm_failure` marker on an identity job.** Like a script's result,
  an identity job's result fails when it marks itself failed with a string
  `wm_failure`. It is kept with its `ManualFailure` error put in
  ([FX-13](../../docs/semantics/execution.md#fx-13)). This applies to a
  mock value and to an identity's or a skipped module's passthrough, so a
  step after a tolerated `wm_failure` fails again.
- **A module that fails so** is `Failure`, which carries no skipped flag
  ([OR-11](../../docs/semantics/oracle.md#or-11)).
- **A host-provided site's provider** answers its launch in place of
  `Intercept` ([Host-provided sites](#host-provided-sites)).

A leaf's launch then goes through these steps:

1. `Intercept` is offered the launch.
2. `Entrypoint` chooses the export.
3. `BeforeFirstWrite` is asked once per run, before the first
   write-capable launch (`WriteCapable`). It is also asked before a
   write-capable subflow call's child flow starts. Its approval is the
   commit point: it sets `MayHaveMutated` and disarms the pre-write
   deadline, whatever then stops the launch.
4. `InvocationEnv` edits the environment ([The job environment](#the-job-environment)).
5. The step runs through the `StepExecutor`, with its timeout as a timer
   in the engine.
6. `StoreOutcome` maps the outcome, an `Intercept`'s included.

**The step's timeout** is the module's own positive timeout, static or
evaluated, else a path script's own positive timeout metadata. A timeout
strictly below the instance maximum (`Config.InstanceTimeout`) is a custom
timeout. None, or one at or above the maximum, gives the instance maximum
([FX-24](../../docs/semantics/execution.md#fx-24),
[FX-25](../../docs/semantics/execution.md#fx-25),
[WR-37](../../docs/semantics/languages.md#wr-37)).

**NUL escapes.** The completion stores a leaf's result or failure value
without its NUL escapes, which every reader of storage sees. The flow
carries the producer's text on in memory, so the next module's
`previous_result` keeps them
([FX-48](../../docs/semantics/execution.md#fx-48) case 1). A value that
jsonb still refuses fails its job instead
([FX-51](../../docs/semantics/execution.md#fx-51) case 1,
[Failure values](#failure-values)).

### The job environment

The engine gives each step its job identity, from which
[internal/platform](../platform/README.md) builds the job environment;
`InvocationEnv` may then edit it. The engine fills these values:

| Variable | Value |
|---|---|
| `WM_JOB_ID` | A fresh id: the invocation's |
| `WM_FLOW_JOB_ID`, `WM_ROOT_FLOW_JOB_ID`, `WM_ROOT_JOB_ID` | The run's id; a dispatched run's two root ids follow its links ([A dispatched flow](#a-dispatched-flow)) |
| `WM_FLOW_PATH`, `WM_FLOW_STEP_ID`, `WM_JOB_PATH`, `WM_STATE_PATH` | The flow's path, the module id, the job path, the state path |
| `WM_TOKEN` | A fresh token |
| The host configuration | `Config.Host` and `Config.Workspace` |

A step in a loop body takes its child flow's job id, innermost scope and
path. A collapsed iteration's step takes its loop's step id and the job
path `<flow path>/loop-<i>` ([For loops](#for-loops)).

A Bun step of a path script also gets the script's own `envs` entries,
under the job identity ([WR-22](../../docs/semantics/languages.md#wr-22),
[WA-2](../../docs/semantics/client-api.md#wa-2); `env.go`):

- An entry is `KEY=VALUE`, split at the first `=`. An empty name is kept.
- A later entry wins.
- A name that the worker sets later is skipped: a reserved variable, a
  common Bun variable such as `PATH` or `BASE_URL`, and `NODE_PATH` when
  the engine's environment has one, which the harness then copies. A name
  already set is skipped too.
- A native step gets no `envs`
  ([WR-45](../../docs/semantics/languages.md#wr-45)).
- An entry with no `=` fails a Bun or a bunnative (`//native`) job once
  it is picked up, before `InvocationEnv`, its argument resolution and its
  script. A `nativets` job never reads its entries.

The failure value of an entry with no `=` is the worker's execution error:

```json
{"error": {"message": "execution error:\nBad request: Invalid env var: <entry>. Must be in the form of KEY=VALUE", "name": "ExecutionErr", "step_id": "<module id>"}}
```

### stop_after_if

`stop_after_if` is evaluated after a success. It is not evaluated on the
failure module. It is not evaluated on an identity job classified
skipped: any identity job of a module with a `skip_if`, whatever the
predicate answered, and whether it ran or `Intercept` answered for it
([FX-17](../../docs/semantics/execution.md#fx-17),
[FX-18](../../docs/semantics/execution.md#fx-18),
[FX-19](../../docs/semantics/execution.md#fx-19)).

- **A clean stop** ends the flow with the module's result: `skipped` when
  `skip_if_stopped` is set (the module is then skipped too), and `success`
  otherwise.
- **An error stop** fails the module, while its job keeps its success
  ([FX-V2](../../docs/semantics/execution.md#fx-v2)). The module carries
  `{"error": {"name": "EarlyStopError", "message": <msg>, "result"?}}`,
  where `<msg>` is `error_message`, or `stop after if: <expr>` when that is
  empty.
- **A predicate that throws** fails the module unrecoverably
  ([FX-21](../../docs/semantics/execution.md#fx-21)).

## The continue decision

The continue decision applies when no retry is due
([FX-2](../../docs/semantics/execution.md#fx-2) completion steps 3, 7 and
8; [Retries](#retries)). `continue_on_error` tolerates a failure unless it
is unrecoverable ([FX-44](../../docs/semantics/execution.md#fx-44)).

| The module | Then |
|---|---|
| Succeeded | The next module; at the last module the flow succeeds with its result |
| Stopped cleanly | The flow ends with its result, `success` or `skipped` |
| A chaining error at a later transition | The flow fails at once with the `InternalError`; the failure module does not run ([FX-15](../../docs/semantics/execution.md#fx-15) context 1) |
| Failed, tolerated: a failed job, a transform error, or a chaining error at the root's first pickup | The next module, whose `previous_result` and `results.<id>` are the failure value. At the last module the flow succeeds with it, and the failure module does not run ([FX-30](../../docs/semantics/execution.md#fx-30)) |
| Failed, not tolerated | The failure module, else the flow fails with the failure value ([FX-31](../../docs/semantics/execution.md#fx-31), [FX-36](../../docs/semantics/execution.md#fx-36)) |
| An error stop, with a failure module | Tolerated: the next module, or past the last one the failure module, entered at cursor `len` with an empty preceding id ([FX-V4](../../docs/semantics/execution.md#fx-v4)). Not tolerated: the failure module ([FX-19](../../docs/semantics/execution.md#fx-19)) |
| An error stop, no failure module | Tolerated at the last module: the flow succeeds with the `EarlyStopError`. Otherwise the flow fails with it |
| A failed loop or `branchall` whose `stop_after_all_iters_if` stops cleanly | A stop never continues, not even into the failure module. Tolerated at the last module, the flow succeeds with the array, unless the container is parallel; otherwise it fails with it ([CF-13](../../docs/semantics/control-flow.md#cf-13)) |
| A parallel loop or parallel `branchall` that failed | Never tolerated. Under `continue_on_error` the flow aborts (below); without it, the failure module runs, else the flow fails with the array |
| A sequential loop, `branchone` or sequential `branchall` that failed on an error stop that a child flow forwarded | Its `continue_on_error` does not apply to that completion, at any position: the failure is not tolerated, so the failure module runs (unless a child's copy handled it), else the flow fails. A loop's retry still applies ([CF-12](../../docs/semantics/control-flow.md#cf-12) item 4) |
| A `stop_after_if` that throws | The failure module, else the flow fails with the string error; `continue_on_error` never applies ([FX-21](../../docs/semantics/execution.md#fx-21)) |

**A failed parallel container.** Its completion leaves the cursor on it.
Under `continue_on_error` the decision enters it again, which cannot
start from a failure ([CF-7](../../docs/semantics/control-flow.md#cf-7)):

- The flow aborts with the `InternalError`
  `Bad request: Unrecognized module status for ForloopFlow Failure { .. }`,
  or `BranchAll` in place of `ForloopFlow` for a `branchall`. Windmill
  prints the status where windmill-lite writes `..`.
- Neither the next module nor the failure module runs.
- A parent takes the container's array as the aborted flow's last carried
  value.
- A retry that is due runs first.

## The failure module

The failure module sits at the slot `s:failure`, the cursor `len`
([FX-1](../../docs/semantics/execution.md#fx-1)). It starts like any
module: through the host's start checks, where `HaltBefore` never applies
to it, then its own `skip_if`, its arguments and its launch. These are the
differences ([FX-32](../../docs/semantics/execution.md#fx-32),
[JE-35](../../docs/semantics/expressions.md#je-35)):

- **Its `previous_result`** is the failing value. Its preceding id is the
  failed module's, or empty when it is entered at cursor `len`. So
  `results.<failed id>` reads the failing value from memory, and a simple
  path reads the failed job's stored result
  ([FX-10](../../docs/semantics/execution.md#fx-10)). `error` is the
  evaluator's ([JE-17](../../docs/semantics/expressions.md#je-17)).
- **`started_at`** in its input transforms is the start time of the flow
  that it runs in: the run's, or a child flow's or a subflow's for a copy
  in a body or a subflow's own. It is an RFC 3339 string with 0, 3, 6 or 9
  fraction digits.
- **`flow_job_id`** in its input transforms is that flow's job id. Its
  `WM_FLOW_STEP_ID` is its own id.
- **A `skip_if` that holds** replaces it with a passthrough identity job
  that is never flagged skipped
  ([FX-17](../../docs/semantics/execution.md#fx-17)).
- **It has no stop**, and its own `continue_on_error` is ignored.
- **A chaining error while it starts** is a later transition's
  ([FX-15](../../docs/semantics/execution.md#fx-15) context 1).

### Recovery

The flow ends with the failure module's result, stored as it is
([FX-33](../../docs/semantics/execution.md#fx-33)):

- The flow succeeds only when the failure module's job succeeded and its
  result decodes as the recovery record with `recover` true: an object's
  boolean member, or a one-element array by position.
- A decode error, an absent `recover` or `null` does not recover.
- A failure module that fails fails the flow with its own failure value.

### The recovered mark

A flow whose failure module recovered reports its failure to what
contains it, marked as recovered
([FX-33](../../docs/semantics/execution.md#fx-33),
[CF-V1](../../docs/semantics/control-flow.md#cf-v1)):

- A sequential loop, a sequential `branchall`, a `branchone` or a flow
  step that this failure fails keeps the mark.
- A parallel loop or a parallel `branchall`, a stop of the failed
  container (clean or an error), and any new failure drop the mark.
- A child flow that a failure with the mark ends, when nothing retried,
  tolerated or handled it, fails and passes the mark on to its parent.
- A root flow that it ends that way is stored `success`, with the result
  that it would have failed with.
- A caller's own failure module still runs across a flow step, and decides
  as usual.

## Limits per run

`RunRequest.Limits` bounds one run, by the
[pooling rules](../../docs/design/0004-harness-protocol.md#pooling-processes-the-engine-spawns).
A zero field sets no limit. A run that a step dispatches has none of its
parent's.

- **`MaxInFlight`.** The run's invocations in flight at once: a step past
  it waits, as a job in the queue, after its launch and before its
  arguments resolve and its timeout starts, and launches once one of the
  run's invocations returns. A run that latches meanwhile launches it no
  more, and the trace lists `D15`.
- **`MaxHeldBytes`.** The bytes of the stored values that the run holds:
  its arguments and each job's stored result, a cached root's included,
  counted at their stored size as they are stored, and not returned while
  the run lasts. Once they pass it, the run latches `ErrRunMemoryLimit`.
- **A script job's** (`ScriptRequest.MaxHeldBytes`) counts its stored
  arguments, which latch it before it runs when they pass it alone, and
  its result, however the job ends.
- **`Bytes` and `ByteTotal`.** The run's byte budget and the runtime's
  total: with either, each invocation gets a `Budget`, whose `Reserve` a
  step's `wl.reserve` reaches through the executor. A reservation must fit
  both, and returns when the invocation settles, before its Reporter hears
  `Settled`. A script job has the same fields in `ScriptRequest`.

## Cancellation

A run whose context ends finishes canceled, without a retry,
`continue_on_error` or the failure module, whatever its modules reported
([FX-38](../../docs/semantics/execution.md#fx-38)).

- **The run's result** is
  `{"error": {"message": "Job canceled: <reason> by <canceler>", "name": "Canceled", "reason", "canceler"}}`.
- **The reason and the canceler** are the context's cause when it is a
  `*Cancellation`. Otherwise they are Windmill's defaults, `no reason
  given` and `unknown`.
- **A step in flight** is abandoned, and its job is canceled with the
  worker's text
  `execution error:\njob process terminated due to cancelled by <canceler> (reason: <reason>)`,
  `ExecutionErr`, `step_id`.
- **A stop under cancellation.** Windmill's continue decision checks a
  stop before the cancel flag. So a clean stop under cancellation ends
  canceled, not skipped. An error stop in a flow with a failure module
  still starts what the decision asks for (the next module or the failure
  module), and the run ends canceled at its next completion
  ([Precedence](#precedence)).

### The cause

A canceled run's `Trace.CancelCause` says what canceled it. The status
stays `canceled`, and the result stays the `Canceled` error above.
`CancelCauseOf` reads the context's cause, in this order:

| Cause | When |
|---|---|
| `pre_write_deadline` | The cause is `ErrPreWriteDeadline` ([Deadlines](#deadlines)). |
| `closing` | The cause is `Closing`, with which a runtime that closes cancels its runs. |
| `deadline` | A deadline of the context passed. |
| `canceled` | Anything else: a `*Cancellation`, the monitor's included, or a cause that is not one. |

A root flow that its own wall-clock limit ended has the cause
`flow_timeout`, while its context still runs
([The flow's own timeout](#the-flows-own-timeout)).

### The flow's result

- The flow's result is what the decision ends it with, stored as jsonb
  ([FX-36](../../docs/semantics/execution.md#fx-36)).
- A flow with no modules returns its input
  ([FX-3](../../docs/semantics/execution.md#fx-3)), through the same
  completion checks, so it ends canceled when its context has ended.
- A latched or halted run has no result.

## The argument pipeline

The [JSON at step boundaries](../../docs/design/0005-json-step-boundaries.md)
design record defines the pipeline. The engine applies it so:

- **The flow input** is stored (jsonb) before anything reads it
  ([JE-44](../../docs/semantics/expressions.md#je-44)). `flow_input` and
  the first module's `previous_result` are the text that [JE-44](../../docs/semantics/expressions.md#je-44)'s table
  gives the worker's copy. That is a compact object, each member in
  jsonb's printed form, with a space after each `:` and `,`
  (`expr.DatabaseText`). So the member shortcut of the expression
  pipeline answers a member in that form.
- **Shuffled orders.** The top-level order of the flow input and of every
  argument map is unspecified on Windmill
  ([FX-42](../../docs/semantics/execution.md#fx-42) item 1). So a per-run
  seed shuffles it, as the
  [equivalence contract](../../docs/design/0001-equivalence-contract.md#2-what-is-matched-exactly)
  allows. `RunRequest.Seed` fixes the seed; zero draws one.
- **The order of the input transforms.** The same seed orders their
  evaluation: one at a time, stopping at the first error
  ([JE-V5](../../docs/semantics/expressions.md#je-v5)).
- **Each input transform:** a static value is copied verbatim, an `ai`
  input is left out, and a javascript one is evaluated
  ([JE-23](../../docs/semantics/expressions.md#je-23)).
- **The stored text.** Each argument, its key and its value, becomes its
  stored text, in the shuffled order. Of two keys that store alike, the
  last wins, as in jsonb.
- **NUL escapes in arguments.** The push first removes every NUL escape
  from the keys and the values (`argsWithoutNUL`). A name given twice keeps
  its last value. Of two distinct names that the removal makes equal, it
  keeps the one that held no NUL, else the smallest original name
  ([FX-48](../../docs/semantics/execution.md#fx-48) cases 2 and 3).
- **What fails the push.** Text that is not exactly one object only loses
  its NULs, so a malformed request stays refused. A key or a value that
  jsonb still refuses fails the push
  ([FX-51](../../docs/semantics/execution.md#fx-51)).
- **Recorded and resolved arguments.** The stored arguments are the
  recorded ones. The resolved ones are the script's, which the job's
  pickup resolves ([Argument resolution](#argument-resolution)). For an
  identity job, or a launch that a hook answers, they are the recorded
  ones.
- **Held values.** A member whose text is exactly the producer text of the
  value that it was written from takes that value's stored text
  (`storeMembers`). A forwarded result already has one, so its bytes are
  not canonicalised again.
- **Rewritten arguments.** Arguments that `RewriteArgs` rewrote are
  canonicalised from their bytes. The stored text depends on the bytes
  alone, so the stored arguments are the same either way.

## Expression scopes

Each expression site has these names
([JE-22](../../docs/semantics/expressions.md#je-22),
[FX-8](../../docs/semantics/execution.md#fx-8)). P is the previous
module's carried value (its producer text), or the flow input at the first
module.

| Site | `previous_result`, `result` | Other names | Previous id | `results` |
|---|---|---|---|---|
| Input transform | P, unbound | `params` `{}`, `resume` `null`, `resumes` `[]`, `approvers` `[]` | The previous module's | Yes |
| `skip_if` | P, P | `resume`, `resumes`, `approvers` | The previous module's | Yes |
| `stop_after_if` | The module's own result, twice | None | Empty | Only when the text holds `results.`, `results[` or `results?.` |
| `sleep` | The sleeping module's own result, twice | None | Empty | No: `results` is not defined |
| `timeout` | P, P | None | The previous module's | Yes |
| `retry_if` | The failed attempt's value, twice, and `error` its error | None | Empty | No |

- Every site has `flow_input`.
- `flow_env` is the flow's, as the transition that started the module
  resolved it ([JE-38](../../docs/semantics/expressions.md#je-38)). At
  `stop_after_if` and `retry_if` it is bound only when the text names it,
  and there it is the flow's cached resolved map once one exists.
- Every site has the helpers of
  [JE-20](../../docs/semantics/expressions.md#je-20)
  ([The expression helpers](#the-expression-helpers)).

## The results context

The run answers the evaluator's lookups from the jobs that it has run
([JE-18](../../docs/semantics/expressions.md#je-18),
[JE-19](../../docs/semantics/expressions.md#je-19)). It keeps them per
flow, in a frame (`frame.go`): the root, a loop iteration's or a
branch's child flow, or a flow step's subflow.

- **`Lookup`, the engine path,** gives the job result of the last module
  of the current flow with that id that has a job. A loop's or a
  `branchall`'s job result is the array of its children's results, and a
  `branchone`'s is its branch's. So a later module of a repeated id that
  has not started is skipped
  ([JE-18/e](../../docs/semantics/conformance.md#je-18),
  [OF-2](../../docs/semantics/openflow.md#of-2)). Otherwise `Lookup` gives
  the node-id map's value.
- **`Stored`, the direct path of
  [JE-9](../../docs/semantics/expressions.md#je-9),** gives the node-id
  map's value, where the module that finished last wins.
- **A `branchone` that ran no branch** has a nil job. `Lookup` fails to
  fetch it with the server's not-found text, without its source location:
  ``Failed to fetch result for step '<id>': Not found: Completed Job not found at name 00000000-0000-0000-0000-000000000000``.
  The map holds it as `null`
  ([CF-19](../../docs/semantics/control-flow.md#cf-19) route 5).
- **Scopes.** The map has scopes
  ([CF-19](../../docs/semantics/control-flow.md#cf-19)): the root's, one
  per parallel iteration, and one per flow step's subflow. A sequential or while iteration and a branch
  write into their parent's scope. A lookup walks from a scope to the
  root.
- **Both routes read job results in storage.** After an error stop, that
  is the step's success value, not the `EarlyStopError`
  ([FX-10](../../docs/semantics/execution.md#fx-10)).
- **An unknown id** is `null`.
- **The previous module's own reads from memory** are the evaluator's
  ([internal/expr](../expr/README.md)).

## Failure values

The engine generates these failure values, with origin `engine`. A
chaining error is routed by the continue decision: at the root's first
pickup it fails the module, and at a later transition it fails the flow
([FX-15](../../docs/semantics/execution.md#fx-15)).

### Steps

| When | Value | Findings |
|---|---|---|
| An input transform fails | ``{"error": {"message": "Error during isolated evaluation of expression `<text as written>`:\n<evaluator text>", "name": "ExecutionErr"}}``. The job launched with `{}` and did not run | [FX-14](../../docs/semantics/execution.md#fx-14), [JE-23](../../docs/semantics/expressions.md#je-23) |
| `skip_if` fails, or jsonb refuses an argument, at the first module (the root's first pickup) | The module fails, not run: `{"error": {"message": "Error: <text>", "name": "Anyhow"}}` | [FX-15](../../docs/semantics/execution.md#fx-15) context 2, [JE-35](../../docs/semantics/expressions.md#je-35) (b) |
| The same at a later module | The flow fails at once, the module not run: `{"error": {"message": <text>, "name": "InternalError"}}` | [FX-15](../../docs/semantics/execution.md#fx-15) context 1, [JE-35](../../docs/semantics/expressions.md#je-35) (a) |
| `stop_after_if` fails | The module fails unrecoverably with the string `{"error": <text>}` | [FX-21](../../docs/semantics/execution.md#fx-21), [JE-26](../../docs/semantics/expressions.md#je-26) |
| An invalid `_ENTRYPOINT_OVERRIDE` | `{"error": {"message": "execution error:\nBad request: Invalid entrypoint override <value>: must match ^[A-Za-z_][A-Za-z0-9_]*$ (letters, digits and underscores, not starting with a digit)", "name": "ExecutionErr", "step_id": <id>}}`, at the job's pickup, before the step runs | [WR-12](../../docs/semantics/languages.md#wr-12) |
| The step's timeout, Bun | `TimeoutError`'s value, `ExecutionErr` with `step_id`: ``execution error:\njob process terminated due to timeout: `bun run` exceeded <limit>``, the limit named as the custom timeout or the instance maximum. The job is stored canceled | [FX-25](../../docs/semantics/execution.md#fx-25), [WR-37](../../docs/semantics/languages.md#wr-37) |
| The step's timeout, native | `execution error:\nQuery timeout after (>Ns)`, whichever limit applied; an ordinary failed job | [WR-44](../../docs/semantics/languages.md#wr-44) |
| A job's result or failure value that jsonb refuses once its NUL escapes are removed (an unpaired surrogate, a number beyond numeric's range) | The job fails: `{"error": {"message": "Internal: Could not add completed job <job id>: error returned from database: <PostgreSQL's message>", "name": "InternalErr"}}`, no `step_id`. Windmill retries the insert for about 30 s first; windmill-lite fails at once | [FX-51](../../docs/semantics/execution.md#fx-51) case 1, [OR-27](../../docs/semantics/oracle.md#or-27) |
| The run is canceled | The run's `Canceled` error, and the killed step's `ExecutionErr` ([Cancellation](#cancellation)). A job still waiting for a sleep or a retry delay completes without running, stored canceled, with the run's `Canceled` error | [FX-38](../../docs/semantics/execution.md#fx-38) |

### Loops and branches

| When | Value | Findings |
|---|---|---|
| A for loop's iterator throws, is not an array, or is `ai` | A chaining error: `Error: <evaluator text>`, ``Expected an array value in the iterator expression, found: <serde's message>``, or `AI input transform not supported for iterator` | [CF-2](../../docs/semantics/control-flow.md#cf-2), [CF-23](../../docs/semantics/control-flow.md#cf-23), [JE-30](../../docs/semantics/expressions.md#je-30) |
| jsonb refuses the iterator's array, or a child flow's input: a NUL (which the flow status that holds it keeps), an unpaired surrogate, a number beyond numeric's range | A chaining error before any iteration runs: `SqlErr: error returned from database: <PostgreSQL's message>` | [CF-2](../../docs/semantics/control-flow.md#cf-2), [FX-48](../../docs/semantics/execution.md#fx-48), [FX-51](../../docs/semantics/execution.md#fx-51), [FX-50](../../docs/semantics/execution.md#fx-50) |
| A parallel loop's `parallelism` throws or is not a u16 | A chaining error: ``Error during evaluation of expression `<text>`:\n<evaluator text>``, `Error parsing result as u16: <serde's message>. Value was: <value>`, or `Error parsing static value as u16: <serde's message>` | [JE-V1](../../docs/semantics/expressions.md#je-v1) |
| A parallel loop of two or more iterations, or a parallel `branchall` of two or more branches, with no `sleep` of its own, in a `same_worker` flow | A chaining error: `Bad request: Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker` | [CF-7](../../docs/semantics/control-flow.md#cf-7) |
| A chaining error at a child flow's first module | The child fails at once with the `Anyhow` form, its module not started; the loop sees a failed iteration | [FX-15](../../docs/semantics/execution.md#fx-15) context 3, [CF-V3](../../docs/semantics/control-flow.md#cf-v3) |
| A chaining error at a later module of a child flow | The child's flow job stores the `InternalError` form, which a loop's or a `branchall`'s array and every `results.<id>` lookup read. A `branchone` or a flow step carries the child's last carried value instead (below) | [CF-23](../../docs/semantics/control-flow.md#cf-23) route 3 |
| A `branchone` predicate throws | The module fails in place, an ordinary step failure wherever it is, never a chaining error: `{"error": {"message": "Error: <evaluator text>", "name": "Anyhow"}}`, with a nil job | [CF-14](../../docs/semantics/control-flow.md#cf-14), [CF-23](../../docs/semantics/control-flow.md#cf-23), [JE-31](../../docs/semantics/expressions.md#je-31) |
| A collapsed body's input transform fails | As an input transform's, the message prefixed `could not transform input using an expr: ` | [JE-23](../../docs/semantics/expressions.md#je-23), [JE-39](../../docs/semantics/expressions.md#je-39) |
| `stop_after_all_iters_if` fails | An error stop of the flow: ``{"error": {"name": "EarlyStopError", "message": "Error evaluating stop_after_all_iters_if expression `<text>`: <predicate text>"}}``, carried by the loop, whose result stays its array | [CF-13](../../docs/semantics/control-flow.md#cf-13), [JE-27](../../docs/semantics/expressions.md#je-27) |

The last carried value of a child flow that aborts at a later module is
the module's before the start that failed (`flowEnd.delivered`). A
`branchone` or a flow step carries it as its own carried value, so these
read it:

- its `retry_if`'s `result` and `previous_result`;
- the next module's `previous_result` under `continue_on_error`;
- its flow's failure module's `previous_result`, whose `error`
  [JE-17](../../docs/semantics/expressions.md#je-17) derives from it;
- when its flow ends on it, that flow's failed result.

### Waits and retries

| When | Value | Findings |
|---|---|---|
| A sleep's value is not a delay | A chaining error, always of a later transition: `Expected an integer, found: <n>` for a negative integer; `Expected an integer, found: <debug form>` (`String("1")`, `Null`, `Array [Number(1)]`) for anything else; for a failed evaluation, ``Expected a number value, had error instead: ExecutionErr("Error during evaluation of expression `<text>`:\n<evaluator text>")``, a debug form with its newline escaped | [FX-23](../../docs/semantics/execution.md#fx-23), [JE-28](../../docs/semantics/expressions.md#je-28), [FX-15](../../docs/semantics/execution.md#fx-15) |
| A timeout's value fails | A chaining error: `Error parsing result as i32: <serde_json's error, with its position>. Value was: <the evaluator's text as it is>`, or ``Error during evaluation of expression `<text>`:\n<evaluator text>``. A value of 0 or less is no error, but unset | [FX-24](../../docs/semantics/execution.md#fx-24), [JE-29](../../docs/semantics/expressions.md#je-29), [FX-15](../../docs/semantics/execution.md#fx-15) |
| `retry_if` throws while the completion is decided | The module fails unrecoverably with the string `{"error": "Error: <text>"}`. When the evaluation threw, it is converted once more than `stop_after_if`'s, so it has two `Error: ` prefixes (and on Windmill two location suffixes). A value that is not a boolean gives one: `Error: Expected a boolean value, found: "<value>"` | [FX-28](../../docs/semantics/execution.md#fx-28), [FX-21](../../docs/semantics/execution.md#fx-21), [FX-50](../../docs/semantics/execution.md#fx-50) |
| `retry_if` throws at re-entry | A chaining error of a later transition: the flow fails at once with `{"error": {"message": <text>, "name": "InternalError"}}` | [FX-28](../../docs/semantics/execution.md#fx-28), [FX-15](../../docs/semantics/execution.md#fx-15) |
| A transition outlives the monitor | The run is canceled by `monitor` ([The monitor of flows that hang between steps](#the-monitor-of-flows-that-hang-between-steps)) | [JE-2](../../docs/semantics/expressions.md#je-2) |

### The texts inside failure values

- **A predicate's `<text>`** is `Error: <evaluator text>` for a failed
  evaluation, or `Expected a boolean value, found: "<value's JSON>"`
  ([JE-24](../../docs/semantics/expressions.md#je-24)).
- **An argument's `<text>`** is the push's database error,
  `SqlErr: error returned from database: <PostgreSQL's message>`
  ([FX-51](../../docs/semantics/execution.md#fx-51) case 2,
  [FX-50/a](../../docs/semantics/conformance.md#fx-50)). It is the same
  when the arguments are not JSON because a replaced `JSON.stringify`
  wrote them: `invalid input syntax for type json`
  ([JE-15/k](../../docs/semantics/conformance.md#je-15)). `RewriteArgs`'s
  not-an-object check applies only to arguments that a hook changed.
- **A result's completion insert** quotes the bare `error returned from
  database: …` after its own `Internal: Could not add completed job <job
  id>: ` ([FX-51](../../docs/semantics/execution.md#fx-51) case 1).
- **serde's message** is what Windmill's JSON library reports for the value
  (`serde.go`), such as ``invalid type: map, expected a sequence at line 1 column 0``
  or ``invalid type: floating point `1.5`, expected u16 at line 1 column 3``.
- **Location suffixes.** Windmill ends the generic messages with Rust
  source locations, which windmill-lite does not write, a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
  A comparison strips them on the oracle's side
  ([internal/conformance](../conformance/README.md#location-suffixes)).

### Who produced a value

The trace records who produced each value, so that a comparison strips
locations only from the errors that windmill-lite generated:

- **`CarriedOrigin`** records who produced a module's carried value, apart
  from its result: an error stop's `EarlyStopError` is the engine's, over
  a step's own result.
- **`ResultNested`, `CarriedNested` and `Trace.ResultNested`** record, for
  a loop's array, where inside it the engine's errors are: JSON pointers
  such as `/0`, or `/1/0` for a nested loop's. They go through every level
  of nesting up to the run's result.
- An error stop that includes the array records them below
  `/error/result`. So a comparison strips their locations, and a user's
  error beside them keeps its own.
- A `branchall`'s array does the same, and a `branchone` carries its
  branch's result with its origin and pointers.

## For loops

A loop module that runs is a container: it has no arguments, no
`Intercept` and no `RewriteArgs`. Its record's status is the loop's own,
and its result is the array of its iterations' results
([CF-4](../../docs/semantics/control-flow.md#cf-4)). `loop.go` holds the
code.

1. **Start.** `skip_if` and `mock` answer for the loop as for any module.
   The iterator is evaluated once, with `skip_if`'s scope, unless the
   body is empty.
   - Its elements are kept in their stored form, since Windmill stores the
     evaluated array ([CF-2](../../docs/semantics/control-flow.md#cf-2),
     [JE-30](../../docs/semantics/expressions.md#je-30)). An array that
     jsonb refuses fails the loop's start before any iteration runs
     ([FX-48](../../docs/semantics/execution.md#fx-48),
     [FX-51](../../docs/semantics/execution.md#fx-51)).
   - An empty array, or an empty body, completes the loop at once with
     `[]` and no job. Its `stop_after_if` is then an ordinary stop of the
     flow, with `result` `[]` and no `all_iters`, and its
     `stop_after_all_iters_if` is not evaluated
     ([CF-29](../../docs/semantics/control-flow.md#cf-29),
     [CF-V2](../../docs/semantics/control-flow.md#cf-v2)).
   - With no job, its record's status is its module's, as the oracle's
     walker reads a container with no child. An error stop fails both
     ([CF-29/a](../../docs/semantics/conformance.md#cf-29)).
2. **Iterations.** Each iteration's flow input is the loop's flow input
   plus `iter` = `{"index": i, "value": element}`. An `iter` already there
   moves to `iter_parent`, and so on up
   ([CF-3](../../docs/semantics/control-flow.md#cf-3)). The IR says whether
   a loop collapses ([CF-8](../../docs/semantics/control-flow.md#cf-8)).
3. **Sequential loops** ([CF-5](../../docs/semantics/control-flow.md#cf-5),
   [CF-11](../../docs/semantics/control-flow.md#cf-11),
   [CF-12](../../docs/semantics/control-flow.md#cf-12)):
   - A failed iteration stops the loop, which fails with the array so far,
     unless `skip_failures` is set.
   - A clean stop in the body breaks the loop. An error stop breaks it too,
     and fails it unless `skip_failures` is set.
   - After each successful iteration, the loop's `stop_after_if` may break
     it. `all_iters` is bound when its text names it.
     `skip_if_stopped` and `error_message` have no effect there.
4. **Parallel loops** ([CF-7](../../docs/semantics/control-flow.md#cf-7),
   [JE-V1](../../docs/semantics/expressions.md#je-v1)): every iteration is
   a child flow on a goroutine of its own, and each is its own results
   scope ([`parallelism`](#parallelism)).
   - There is no fail-fast: every iteration runs, as Windmill's
     continue-all does. The loop fails with the full array unless every
     iteration succeeded or `skip_failures` is set.
   - A stop ends only its iteration, and the loop's `stop_after_if` is not
     evaluated.
   - Each parallel iteration seeds its own shuffles from its loop's flow,
     in index order, so a seed still fixes every order.
5. **Completion** ([CF-13](../../docs/semantics/control-flow.md#cf-13),
   [JE-27](../../docs/semantics/expressions.md#je-27)): the loop's
   `stop_after_all_iters_if` is evaluated once, with the array. When it
   fires, it acts as an ordinary `stop_after_if` of the flow that the loop
   is in.

### Collapsed iterations and child flows

A **collapsed** iteration runs its one body step as a job of the loop's
flow:

- Its `previous_result` is the result of the step before the loop, then
  the previous iteration's. For a loop at the first module it is the
  flow's input every time
  ([JE-36](../../docs/semantics/expressions.md#je-36)).
- Its previous id is empty.
- It takes the loop's `timeout`, and its loop's `continue_on_error` rather
  than its own, so a failed body fails the iteration ([CF-8](../../docs/semantics/control-flow.md#cf-8) item 5).
- Its step id, and its errors' `step_id`, are the loop's.
- Its job path is `<flow path>/loop-<i>` for an inline step.
- Nothing is registered under its own id.

Any other iteration runs its body as a **child flow**
([CF-1](../../docs/semantics/control-flow.md#cf-1)):

- Its input is read back from storage, and is its first step's
  `previous_result` ([CF-9](../../docs/semantics/control-flow.md#cf-9)).
- Its steps see the child's job id as `WM_FLOW_JOB_ID`, and its path,
  `<flow path>/loop-<i>` or `<flow path>/forloop-<i>`, as `WM_FLOW_PATH`
  ([WA-6](../../docs/semantics/client-api.md#wa-6)).
- A parallel iteration is also their innermost scope
  (`WM_ROOT_FLOW_JOB_ID`, [WA-V1](../../docs/semantics/client-api.md#wa-v1)).
- The trace lists each child flow (`Trace.Children`) with its id and its
  modules, so that a module that it never reached is `not_run`.
- A chaining error at a child's first module fails the child at its
  pickup ([FX-15](../../docs/semantics/execution.md#fx-15) context 3).

### parallelism

`parallelism` is evaluated per child, with the loop's P and its flow's
input. It holds a child k at or beyond its own N until k − N + 1
iterations have completed.

- The children that are not held start at once, so one never waits
  behind a held one.
- Held children start in the order of that count, by index among equals.
- With N = 0 none starts, and only the run's end stops the wait.
- An `ai` parallelism evaluates to 0, and holds every iteration the same
  way ([CF-7/e](../../docs/semantics/conformance.md#cf-7)). The sync
  folder's decoder takes it in the slot
  ([internal/openflow](../openflow/README.md)).

### Host checks inside containers

The host's checks (`Start`) run before every iteration and every branch
too. A latch, the cancellation or a halt starts none, and a parallel
container waits for the children that it started.

- **Across parallel children.** A halt decided in one parallel iteration
  stops the others at their next module, and wakes a loop whose iterations
  are all held.
- **The run's status** ranks the latch, then the cancellation, then the
  halt, whichever iteration met its signal first.
- **A pending halt.** A body module's `HaltAfter` that ends its child flow
  is pending for the whole run. The container completes the child, its
  `stop_after_if` included. The halt applies at the run's next start
  anywhere (below).
- **A run that ends first** keeps the status that Windmill gives it, and
  the pending halt is dropped. So a halt never hides a failure or a stop
  that ends the run.
- **What still runs.** A step already running finishes. A flow that has
  started (an iteration or a branch launched) runs its first module, so a
  parallel loop's iterations already started are not starts.
- **An interrupted container.** A loop so interrupted records the array
  so far. A canceled one completes as a failure
  ([Containers and waits](#containers-and-waits)).

The run's next start, where a pending halt applies, is one of these:

- the next iteration, or an iteration that a parallel loop still holds;
- the next branch of a sequential `branchall`;
- the next module of another iteration or branch that is already running;
- once the container has completed, its `stop_after_if` or
  `stop_after_all_iters_if` included, and every enclosing container too,
  the next module.

## While loops

A while loop is a sequential loop with no iterator
([CF-10](../../docs/semantics/control-flow.md#cf-10)). It shares the
sequential for loop's code (`loop.go`).

- **Iterations** run one after another. Iteration k's `iter` is
  `{"index": k, "value": k}` ([CF-3](../../docs/semantics/control-flow.md#cf-3)).
- **What ends it,** and nothing else:
  - its `stop_after_if` after a successful iteration
    ([CF-11](../../docs/semantics/control-flow.md#cf-11)), with
    `all_iters` bound as for a for loop;
  - a stop in its body ([CF-12](../../docs/semantics/control-flow.md#cf-12));
  - an iteration that fails, which `skip_failures` does not tolerate
    ([CF-5](../../docs/semantics/control-flow.md#cf-5));
  - the run's end ([CF-28](../../docs/semantics/control-flow.md#cf-28)).
- **Collapsing.** A body of one plain step collapses, in every job kind,
  as the IR decides ([CF-8](../../docs/semantics/control-flow.md#cf-8)
  item 3). Otherwise each iteration runs as a child flow at
  `<flow path>/loop-<i>` ([CF-24](../../docs/semantics/control-flow.md#cf-24)).
- **Its result** keeps the iteration that broke it.
- **An empty body** returns `[]` at once, as an empty container does
  ([CF-29](../../docs/semantics/control-flow.md#cf-29),
  [CF-V2](../../docs/semantics/control-flow.md#cf-v2)).
- **`stop_after_all_iters_if`** is evaluated as for a for loop
  ([CF-13](../../docs/semantics/control-flow.md#cf-13)).
- **Its `parallel` fields** are dropped by the IR
  ([OF-8](../../docs/semantics/openflow.md#of-8)).

## Branches

A branch that runs is a child flow
([CF-1](../../docs/semantics/control-flow.md#cf-1)); `branch.go` holds the
code.

- **Its input** is its flow's input, unchanged and read back from storage
  ([JE-37](../../docs/semantics/expressions.md#je-37)). That is its first
  module's `previous_result`
  ([CF-9](../../docs/semantics/control-flow.md#cf-9),
  [JE-36](../../docs/semantics/expressions.md#je-36)).
- **Its scopes.** It writes into its flow's results scope, and stays in
  its innermost flow scope ([CF-19](../../docs/semantics/control-flow.md#cf-19),
  [WA-V1](../../docs/semantics/client-api.md#wa-v1)).
- **Its path** is `<flow path>/branchone-<n>` (`-0` for the default,
  `-<i+1>` for branch i) or `<flow path>/branchall-<i>`
  ([CF-24](../../docs/semantics/control-flow.md#cf-24)).
- **The trace** lists it (`Trace.Children`) under `b:<n>` or `b:default`.

1. **`branchone`** ([CF-14](../../docs/semantics/control-flow.md#cf-14),
   [JE-31](../../docs/semantics/expressions.md#je-31)):
   - Its predicates are evaluated once, in order, each as
     `Boolean(<expr>)`, with `skip_if`'s scope. So an empty predicate
     never holds.
   - The first that holds is chosen, and later ones are not evaluated. The
     default runs when none holds. The record names the choice
     (`BranchChosen`).
   - A throw fails the module in place ([Failure values](#loops-and-branches)).
   - The chosen branch's result is the module's, and the module's
     `stop_after_if` is then an ordinary stop of the flow.
   - An empty chosen branch runs no child. The module succeeds with a nil
     job, and passes on the `previous_result` handed to it, or the JSON
     string `"{}"` at the first module
     ([OR-25](../../docs/semantics/oracle.md#or-25)).
2. **`branchall`** ([CF-15](../../docs/semantics/control-flow.md#cf-15)):
   - Every branch runs, an empty one included, whose result is then its
     input. The result is the array of the branches' results, in branch
     order. Its own `stop_after_if` is not evaluated.
   - A parallel one, the deployed default
     ([CF-6](../../docs/semantics/control-flow.md#cf-6)), starts every
     branch at once on goroutines of their own, with no limit and no
     fail-fast. It fails with the full array when a branch whose
     `skip_failure` is false failed. Each parallel branch draws its own
     random source from its flow's, in branch order.
   - A sequential one goes past a failed branch only when that branch's
     `skip_failure` is true, and otherwise fails with the results so far.
   - A `branchall` with no branches is an empty container ([CF-29](../../docs/semantics/control-flow.md#cf-29)).
   - `stop_after_all_iters_if` is evaluated once, with the array, as for a
     loop ([CF-13](../../docs/semantics/control-flow.md#cf-13),
     [JE-27](../../docs/semantics/expressions.md#je-27)), unless a
     sequential one was stopped from inside.
3. **Stops** ([CF-12](../../docs/semantics/control-flow.md#cf-12),
   [FX-20](../../docs/semantics/execution.md#fx-20)):
   - A clean stop in a `branchone`'s branch, or in a sequential
     `branchall`'s, stops the flow right after the module, with the same
     skipped flag. The `branchall`'s result is its results so far.
   - An error stop there, with no failure module, is an error stop of a
     `branchone`. It is one of a sequential `branchall` whose stopped
     branch's `skip_failure` is false. When that branch's `skip_failure`
     is true, the stop ends the flow as a success.
   - Either way, the flow that the module is in forwards the stop one level
     up, as a stop in a sequential loop's body breaks the loop.
   - A parallel `branchall` ignores a stop: the stop ends only its branch,
     and an error stop counts as a failed branch.

## Subflows by path

A `type: flow` step runs the flow at its path, which the compiler put in
the program, as a child flow of its own, at the step's own site
([CF-17](../../docs/semantics/control-flow.md#cf-17),
[FX-5](../../docs/semantics/execution.md#fx-5)). The trace lists it
(`Trace.Children`) there, and the step's record holds the child's id as
its job. `subflow.go` holds the code.

- **Input.** The step's evaluated arguments, stored and read back as a
  script step's are ([JE-44](../../docs/semantics/expressions.md#je-44),
  [JE-37](../../docs/semantics/expressions.md#je-37)), with their
  top-level keys shuffled ([FX-42](../../docs/semantics/execution.md#fx-42)).
  They are its first module's `previous_result`
  ([CF-9](../../docs/semantics/control-flow.md#cf-9)).
- **Results scope.** It is a results scope of its own. A lookup of an id
  that it has no step of still walks up to its caller's
  ([CF-19](../../docs/semantics/control-flow.md#cf-19)). Windmill's
  subflow reads its caller's results so, and the
  [equivalence contract](../../docs/design/0001-equivalence-contract.md#7-policy-questions-the-reference-left-to-the-design)
  decides to match it ([Q-CF-12](../../docs/semantics/control-flow.md#q-cf-12)).
- **Flow scope.** It is its own innermost flow scope: its steps see its
  job id as `WM_FLOW_JOB_ID` and `WM_ROOT_FLOW_JOB_ID`, and its path as
  `WM_FLOW_PATH` ([WA-6](../../docs/semantics/client-api.md#wa-6),
  [WA-V1](../../docs/semantics/client-api.md#wa-v1)). A body inside it
  hangs below it, as below any flow.
- **Settings.** It has its own failure module, `same_worker` and
  `flow_env`, or its caller's `flow_env` when it defines none
  ([CF-1](../../docs/semantics/control-flow.md#cf-1),
  [JE-38](../../docs/semantics/expressions.md#je-38)). Each frame carries
  its flow (`frame.def`), its stored `flow_env` (`frame.envSrc`) and its
  start (`frame.started`, a failure module's `started_at`), so a
  subflow's settings apply inside it.
- **Outcome.** The step's result is the subflow's. Its job's status is the
  subflow job's: success when its failure module recovered, and skipped
  when a clean stop with `skip_if_stopped` ended it.
- **A failed subflow.** A subflow that failed, or that ran its failure
  module, fails the step, a recovered one included
  ([CF-V1](../../docs/semantics/control-flow.md#cf-v1),
  [FX-33](../../docs/semantics/execution.md#fx-33)). The caller's
  `continue_on_error` and failure module then apply. The subflow boundary
  resets the no-second-run rule, so both failure modules can run
  ([CF-16](../../docs/semantics/control-flow.md#cf-16) item 4).
- **Stops.** A stop inside it ends only the subflow
  ([CF-12](../../docs/semantics/control-flow.md#cf-12),
  [FX-20](../../docs/semantics/execution.md#fx-20)). The step's own
  `stop_after_if` is evaluated after a success.
- **A chaining error at its first module** fails the subflow at its
  pickup, and bypasses its failure module
  ([CF-V3](../../docs/semantics/control-flow.md#cf-v3), [FX-15](../../docs/semantics/execution.md#fx-15) context 3).

### A retried subflow

A flow step with `retry` runs its subflow again when it fails, as any step
is retried ([FX-26](../../docs/semantics/execution.md#fx-26),
[FX-27](../../docs/semantics/execution.md#fx-27)):

- The retry is a new child flow, after the retry's delay, whose input is
  the failed attempt's recorded arguments.
- The failed attempt's job is its subflow's. It moves to the step's
  `r:<k>`, with the subflow's records and child flows below it, its own
  retried modules' attempts included. The oracle's walker meets them there
  (`attempt`, recorded by `conformance/flows/control-flow/subflow-retry`).
- The step's status lists the attempts. A failure module, or the step
  after a tolerated failure, lists them again
  ([FX-29](../../docs/semantics/execution.md#fx-29)). The step's result
  is its last subflow's.
- A flow step whose input transform throws still launches its subflow's
  job, which fails before any module of it runs
  ([FX-14](../../docs/semantics/execution.md#fx-14)). The trace lists that
  child flow with nothing run, so its modules are `not_run`, at the step's
  site or, retried, at its `r:<k>`. The retry runs the subflow with an
  empty input.

## Failure modules in bodies

In a flow with a failure module, every loop iteration and branch runs as
a child flow that carries a copy of it
([CF-1](../../docs/semantics/control-flow.md#cf-1)). So such a loop never
collapses ([CF-8](../../docs/semantics/control-flow.md#cf-8)).

- **The copy** runs inside that child, at `<child>/s:failure` with the
  module's own id, as a deployed flow's stored node keeps it
  ([CF-16](../../docs/semantics/control-flow.md#cf-16),
  [CF-V4](../../docs/semantics/control-flow.md#cf-v4),
  [FX-34](../../docs/semantics/execution.md#fx-34)).
- **Its scope.** It sees the child's input, `iter` included, and the
  child's job as `flow_job_id`. Its result is the child's entry.
- **The trace.** `ChildFlow.Failure` says that the child has the slot, so
  that a copy that never ran is `not_run`.
- **No second run.** A child flow whose failure ended in its failure
  module reports that it was handled (`flowEnd.handled`), and a container
  that fails by such a child carries that (`outcome.handled`). The flow
  that it is in, and any flow above, then decides as if it had no failure
  module for that failure ([CF-16](../../docs/semantics/control-flow.md#cf-16) item 4). So the failure ends the flow; a
  sequential container's `continue_on_error` still tolerates it at any
  position.
- **How a container carries a handled failure:** as a failure, under a
  clean `stop_after_all_iters_if` that keeps the failure
  (`endFailedStop`), or as the error stop that a `branchone` or a
  sequential `branchall` forwards for it.
- **Not handled.** An error stop of the container's own predicate is a
  failure of its own, which the failure module handles. A subflow step
  resets the flag.
- **Parallel containers.** A failed parallel loop or parallel `branchall`
  is always marked handled when the flow has a failure module, since each
  failed child ran its copy. Its `continue_on_error` aborts the flow
  ([The continue decision](#the-continue-decision), [CF-16](../../docs/semantics/control-flow.md#cf-16) item 8).
- **Counting** ([CF-V1](../../docs/semantics/control-flow.md#cf-v1)). A
  sequential loop, a sequential `branchall` and a `branchone` count a
  child that ran its failure module as failed, a recovered one included.
  A parallel loop and a parallel `branchall` judge each child's status, so
  a recovered child succeeds.
- **Stops.** An error stop in a body runs the child's copy, whose
  completion does not forward the stop ([CF-12](../../docs/semantics/control-flow.md#cf-12) item 3). The container sees
  an ordinary failed child.

### A container as the failure module

`New` refuses a flow whose failure module is a container, with
`ErrUnsupported` and a message that names
[CF-31](../../docs/semantics/control-flow.md#cf-31)
(`TestEngineRefusesAContainerAsTheFailureModule`). Windmill accepts such a
flow, but it advances the container with bookkeeping written for list
positions. So the outcome depends on the kind of container and on its
body:

- a sequential loop or `branchall` with more than one child runs its first
  child only, and the flow ends with that child's result;
- a parallel loop or parallel `branchall` hangs until the server's monitor
  cancels the flow after 60 s;
- a body that starts no child, entered past the module list, or a body
  that always fails, can keep the flow from ever completing.

To reproduce these outcomes, windmill-lite would have to emulate the
server's status table and its monitor. So it refuses the flow before it
runs.

## What a run reads from the platform

`Config.Platform`, a `*platform.Services`
([internal/platform](../platform/README.md#services)), holds the
resources, variables and secrets that a run reads. nil means none, so
every read is a not-found. A host gives the facade the same services.
`providers.go` holds the code.

### Argument resolution

A leaf's arguments are resolved when its job is picked up: after its job
identity, its script's `envs` and `InvocationEnv`, before the step runs
([JE-41](../../docs/semantics/expressions.md#je-41),
[JE-42](../../docs/semantics/expressions.md#je-42),
[WR-5](../../docs/semantics/languages.md#wr-5)).

- **What resolves them:** the worker's argument resolution of the stored
  arguments, with the job's reserved variables. Those are its job
  identity, without the script's `envs` or `InvocationEnv`'s changes.
- **`$<NAME>` strings** read the reserved variables. A `$res:` string reads
  the resource, with the job's stored fields (`platform.APIEnv`,
  [WA-V3](../../docs/semantics/client-api.md#wa-v3)).
- **The trace.** The step gets the resolved arguments, which the trace
  records as `ResolvedArgs` however its job then ends: a timeout, a
  cancellation or an executor's error included. The job keeps the stored
  ones (`RecordedArgs`, [JE-V3](../../docs/semantics/expressions.md#je-v3)).
- **Expressions see the markers,** whatever produced them.
- **A failure** fails the job before it runs, with the worker's execution
  error, origin `engine`:
  `{"error":{"message":"execution error:\n<the failure's display>","name":"ExecutionErr","step_id":…}}`.
  The flow handles it as any failure.

### flow_env

`flow_env` is resolved the same way, with the flow job's reserved
variables, each time a module starts, into the frame that runs it
([JE-38](../../docs/semantics/expressions.md#je-38)).

- A child flow inherits its flow's stored map (`frame.envSrc`), and
  resolves it as its own transitions come.
- A subflow by path keeps its own map when it defines one
  ([CF-17](../../docs/semantics/control-flow.md#cf-17)).
- A parallel iteration's frame is its own, so no two goroutines share one.
- A child flow's or a subflow's `$WM_*` strings read the top-level flow
  job's variables.
- A failure leaves the map as stored, with the warning
  `flow_env_unresolved` in the trace, and is not cached. A success becomes
  the frame's cached map.

### What input transforms store from flow_env

A transform whose text contains `flow_env` stores the following, at a
step, a failure module or a collapsed loop body (`flowenv.go`,
[JE-38](../../docs/semantics/expressions.md#je-38)):

- **An exact read** of a key that the env defines (`flow_env.K` or
  `flow_env["K"]`) stores the entry as written in the env. So a reference
  stays unresolved, and the leaf resolves it. Into a subflow called by
  path (a `flow` module), only a secret entry is kept so.
- **Any other text** stores its value from the resolved env. When the text
  reads a secret entry by name, the value is sealed under the run's root
  job (`platform.SealForRoot`), and the leaf, or a subflow's step, opens
  it.
- **A read by name** is `flow_env.K` or `flow_env?.K` with no letter,
  digit, `_` or `$` around it, or `flow_env["K"]` in any of the three
  quotes. A computed key, or a use of the whole object, is not one.
- **A secret entry** is a literal `$encrypted:` string, or a `$var:` or
  `$jsonvar:` reference to a secret variable. The engine looks the entries
  up once per module start. An entry whose lookup fails counts as secret,
  so a failure never stores a secret in clear.
- **At a host-provided site** nothing is sealed. Its provider runs outside
  the engine and could not open the value, so it gets the value in clear.
  An `Intercept` at a real step gets the stored arguments, sealed values
  included.

### The expression helpers

The helpers answer at every site, since every site has an authenticated
client ([JE-20](../../docs/semantics/expressions.md#je-20)):

| Helper | Answer | On a failure |
|---|---|---|
| `variable(path)` | The variable's value; a secret's plaintext | Throws the API's error text |
| `resource(path)` | The resource, read as the stock client reads it, without a job id | Throws the API's error text |
| `flow_user_state(key)` | The value stored at `key` for the run's root, as the API's read gives it; JSON `null` for a missing key ([WA-14](../../docs/semantics/client-api.md#wa-14)) | |

Which of the client's reads Windmill's `resource()` helper uses is not in
the reference; the engine uses the read without a job id.

### Per-run platform providers

A run given its own services (`RunRequest.Platform`) reads its resources,
variables, secrets and value resolvers from them:

- its argument resolution, `flow_env` and expression helpers read them;
- its invocations carry them to the facade (`Invocation.Platform`), which
  answers the steps' resource and variable reads from them;
- they share the engine's job registry, flow user state, object store,
  dispatcher and result cache (`platform.Services.WithProviders`);
- the engine registers runs and child flows with its own services, and
  the runs that the dispatcher starts read its own.

## Host-provided sites

A `Provided` module is a mandatory host-provided call site of the
[host contract](../../docs/glossary.md#host-contract), whose target the
compiler left opaque
([IR and loader](../../docs/design/0002-ir-and-loader.md#host-contract-and-mandatory-host-provided-call-sites)).
It runs as a leaf whose job its [provider](../../docs/glossary.md#provider)
answers ([host hooks](../../docs/design/0008-host-hooks.md#mandatory-host-provided-call-sites);
`provided.go`).

- **As a leaf:** its input transforms, `skip_if`, `mock`, `RewriteArgs`,
  timeout and the storage boundary apply as for a leaf.
- **Its launch** gives a static site's value, verbatim, or the outcome of
  `Config.Provide` for the named provider, with the site's stored
  arguments, once per attempt.
- **Its completion** is a job's: `stop_after_if`, retry,
  `continue_on_error`, and the results routes apply.
- **Latches.** A provider that declines, or a named site with no
  `Provide`, latches the run with `ErrProviderDeclined`
  (`provider_declined`). A provider's error latches the run with that
  error.
- **Its record** has `Ran` set, no job, and `provided` among its hooks.
  The failures that it produces have origin `host`.
- **Its call-site identity** is the step's that it replaces. Hooks see a
  provided script step's path, or an inline step's
  `<flow path>/<module id>`, as its `Site.Script`, so script selectors name
  it.

## Dispatch and the job registry

`engine.go` and `dispatch.go` hold the code. The
[platform interface](../../docs/design/0007-platform-interface.md) design
record defines the job registry and the dispatcher.

- **Each run is a flow job** of the job registry
  (`platform.Services.JobRegistry`) from its start to its end, and its own
  effective root. So its steps' root lookups, reads with a job id and flow
  user state find it. The facade registers the steps' jobs. The run's
  user state is discarded when the run ends
  ([internal/platform](../platform/README.md#the-job-registry-and-flow-user-state)).
- **Job ids are windmill-lite's,** and job reads answer from the run, not
  from a job table, a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **Each child flow is a flow job** of the run's tree while it runs
  (`runList`): a loop iteration's, a branch's and a subflow's. It has its
  runnable path (`<path>/loop-<k>` for a sequential or while iteration,
  `<path>/forloop-<k>` for a parallel one, `<path>/branchone-<n>`,
  `<path>/branchall-<k>`, or the subflow's path), whether or not one of
  its steps has registered.
- **A parallel loop** registers all its children at its start, held ones
  included, until the child or the loop ends. A collapsed iteration runs as
  a job of its loop's flow, and registers nothing.
- **A dispatch from a child flow** whose `parent_job` names one runs with
  the child's path as `WM_FLOW_PATH`
  ([WA-15](../../docs/semantics/client-api.md#wa-15)).
- **A script root is checked when the engine opens,** as every flow is: a
  Bun script in either boundary. Its own `cache_ttl` caches its script job
  ([The result cache](#the-result-cache)).

### RunScript

`RunScript` runs a root script of the program as a script job:

- its arguments are stored and resolved as a step's are;
- its entrypoint and its own `envs` are chosen as a step's are;
- its timeout is its own or the instance's
  ([WR-37](../../docs/semantics/languages.md#wr-37));
- `StoreOutcome` stores its outcome, without a step id.

Its job identity is a script job's at its path, its own root
([WA-4](../../docs/semantics/client-api.md#wa-4),
[WA-5](../../docs/semantics/client-api.md#wa-5)). Given a dispatch's
`parent_job` and `root_job`, it is the identity that the oracle recorded
for a script that `runScriptByPath` dispatched
(`conformance/flows/client-api/dispatch`, run `identity`):

| Variable or field | Value |
|---|---|
| Parent (`WM_FLOW_JOB_ID`) | The dispatching job |
| Innermost scope (`WM_ROOT_FLOW_JOB_ID`) | The `root_job` |
| Effective root | The first of `root_job`, `parent_job` and itself that is set |
| Flow path | The dispatching job's runnable path, so the state path is `<that path>/nostep_user` |
| Step id | None |

### A dispatched flow

A dispatched flow is a `Run` whose `RunRequest` carries the dispatch's
links. The oracle recorded this for a flow that `runFlow` dispatched:

- its steps' effective root is the first of `root_job`, `parent_job` and
  the run that is set;
- the `root_job`, when set, is their innermost scope, so they share the
  dispatching run's flow user state;
- without links (`runFlowAsync`), the run is its own root, as any other;
- the run's own job is registered in its effective root's tree.

### The Dispatcher

`Dispatcher` is the default dispatcher of the platform interface
(`platform.Dispatcher`). It runs a dispatched target from the engine's
program: a script root through `RunScript` for `jobs/run/p`, and a flow
root through `Run` for `jobs/run/f`.

- **A separate run.** A dispatched run is a separate run of the same
  runtime that starts at once: no default cap delays it. It is detached
  from the call and from the dispatching run. It ends on its own, or when
  the dispatcher closes (`Close`), which cancels what still runs.
- **Links.** A run's links (its `parent_job`, `root_job` and the parent
  job's runnable path, from the job registry) are taken when the dispatch
  is accepted. So a run that starts after its parent has ended, after
  `scheduled_in_secs` or at once, keeps them.
- **Result polls.** It answers the result polls for its runs. The record
  of a completed run is kept while the dispatching step's root lives in the
  registry, and swept after.
- **Set-up.** A host sets it as the services' `Dispatcher` before any run
  starts, over an executor that is the facade's, so that the dispatched
  runs' calls are attributed.

`Dispatch` refuses these requests, and creates no job
([FX-48](../../docs/semantics/execution.md#fx-48),
[FX-51](../../docs/semantics/execution.md#fx-51) cases 3 and 4):

| Request | Answer |
|---|---|
| A target that is not a root of the bundle | `platform.NotInBundleError`, a [restriction](../../docs/design/0001-equivalence-contract.md#5a-restrictions) of the equivalence contract |
| Arguments that are not a JSON object | 400 |
| A surrogate escape without its pair in a top-level key, which the API decodes | 400, `Bad request: invalid json: <message> at line <l> column <c>`, in Windmill's JSON library's words |
| An unpaired surrogate in a value, or a number beyond numeric's range, which reach the job row's insert | 400, `SqlErr: error returned from database: <PostgreSQL's message>` |

Each 400 answers with the error's display as plain text ([FX-51/d](../../docs/semantics/conformance.md#fx-51) and /e).
A NUL is removed before the insert, in a key or a value, so such a request
is accepted, and the run starts with the arguments without it ([FX-48/d](../../docs/semantics/conformance.md#fx-48)).

## The job records

A run renders the record that `GET jobs_u/get/{id}` answers for each job
of its tree while it lives, and the arguments that
`GET jobs_u/get_args/{id}` answers
([WA-19](../../docs/semantics/client-api.md#wa-19)). It also answers the
result polls of those jobs (`JobPoll`): the record's state, and a
completed job's whole stored result, where the record replaces a result
that is too large. The facade reaches them through the job registry.
`jobrecord.go` holds the code.

### References, not copies

- A run registers its flow job and each child flow with itself as the
  view (`platform.Job.View`). It indexes every job that it starts by id
  (`run.jobs`): a flow job by its frame, and a leaf by its frame, its
  module and its record in the trace.
- The leaf's record names its job and its stored arguments from its launch
  on.
- A child flow records its end, and its place among the ends of the run's
  child flows; the run records its own.
- A parallel loop registers all its children at its start, the ones that
  its parallelism holds included.
- Nothing is rendered until a step asks.

### Per attempt

Each frame holds, per module:

- the record of its current attempt (`frame.recs`), and that attempt's
  child flows or collapsed iterations by slot (`frame.kids`), both set
  when the attempt starts;
- the records of its own modules' attempts (`frame.owned`), where the
  attempts that its modules list as failed retries are;
- the child flow of a failed container attempt that a retry kept
  (`frame.attempts`).

A retry runs at the same sites as the attempt that it replaces. So the
record of a superseded attempt's flow job reads its own modules and jobs
from its frame, never the retry's.

### The record

The record is the oracle's, with the fields that windmill-lite serves.
Each argument's value and the result are written as jsonb's text output.

| Field | Value |
|---|---|
| `type`, `id` | The job's kind and id |
| `parent_job` | The flow job of the frame that the job runs in |
| `root_job` | The innermost scope: a frame's scope, absent when the frame starts its own |
| `running`, `flow_step_id` | While it runs |
| `canceled` | Always (`false`: a canceled job is not rendered) |
| `success`, `result`, `resolved` (`false`) | Once it has completed |
| `args`, `is_flow_step` | Always |
| `script_path`, `language` | A script job's; an identity job has neither, and a flow job has no `language` |
| The workspace and the user | The host's |
| `flow_status` | A flow job's (below) |

`flow_status` comes from the frame:

- each module's current record: none gives `WaitingForPriorSteps`, no
  status yet gives `InProgress`, then its module status;
- its job: a subflow step's comes from its child flow while it runs;
- `skipped` on a success, and the failed attempts;
- a loop's or a `branchall`'s children with their success, and a
  `branchone`'s chosen branch;
- while a container runs, its `iterator` (`run.iterLen`; a parallel loop's
  index counts its completed children), `parallel`, `while_loop` or
  `branchall`;
- `step`, which follows the module cursor.

A container's job is its last child. A running parallel container's job
is the flow job that it runs in, the frame's own. A completed parallel
container's job is the child that completed last. These have the nil job:
a container that completed with no child, one whose start failed, and a
`branchone` whose predicate threw.

The status of a child flow whose body the deploy stored as a node also
holds `no_inherited_flow_env`, `true` (`frame.storedNode`,
`ir.StoredNode`). That is a loop iteration's or a branch's child flow
whose body is not empty and not [CF-8](../../docs/semantics/control-flow.md#cf-8)'s simple body. The Community Edition
1.862.0 recordings show `no_inherited_flow_env`, `resolved` and the nil
job, which the reference does not describe yet.

### Jobs pushed for later

A job scheduled after the previous step's sleep or a retry's delay is
pushed with its id before the wait, as Windmill pushes it.

- **What is pushed:**
  - a step's job (`run.jobScheduled`);
  - the identity job of a skipped, mocked or identity module, a
    container's included;
  - a job that its input transform or its entrypoint fails at its pickup;
  - a flow step's subflow (`run.subflowScheduled`), which the step then
    runs, or fails when its input transform failed.
- **Indexing.** Each is indexed and registered at once. A child flow waits
  at its pickup (`frame.scheduled`).
- **Its id.** Whatever completes a pushed job takes its id
  (`run.takeScheduled`): its invocation, the result cache's answer at its
  pickup (`run.cachedJob`), or a completion with no invocation
  (`run.noInvocationJob`).
- **While it waits,** the module's entry reads `WaitingForExecutor`, and
  the job's record `running: false`.

### Other rules

- **Large values.** An argument map or a result whose stored column takes
  90000 bytes or more is replaced as Windmill replaces it (`tooBig`).
  `jsonval.StoredBound` rules most values out at once, and
  `jsonval.ToastedSize` computes the others' `pg_column_size`.
- **Jobs with no invocation.** An identity job (a skipped or mocked flow
  step's included), and a script job that failed before its invocation,
  get an id when their module completes (`StepRecord.NoInvocationJob`).
  That is the one that they were pushed with if they waited. It is
  registered with the run's tree, since Windmill's status names one for
  them.
- **A subflow that fails before any of its modules runs** (an input
  transform's failure, a cancellation while it was scheduled) is created
  and registered as one that ran (`run.unlaunchedSubflow`).
- **Script runs.** `RunScript` registers its job with a view of its own
  (`scriptView`), which renders it running, then completed while its root
  lives. A dispatch that tracked it in its parent keeps it in another
  run's tree.
- **A canceled script job.** A Bun job that its process timeout killed, or
  one that its run's cancellation killed, is stored canceled
  (`ScriptResult.Canceled`, [FX-25](../../docs/semantics/execution.md#fx-25),
  [FX-38](../../docs/semantics/execution.md#fx-38)). Its record is then
  not rendered, as a canceled step's is not, while its result is still a
  failure and its arguments are read. A native job's timeout leaves an
  ordinary failed job.

These are not rendered: each is a `platform.NotServed`, which the facade
answers 501.

- A job of a run that has ended and is its own root, even while a
  registration of it lingers. A job read of an ended run answering 501 is
  a [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- A canceled job, and a flow job that ended other than success or
  failure: canceled, skipped, latched or halted.
- A flow with a preprocessor module.
- A flow job while a host-provided step of it waits for the previous
  step's sleep (`frame.waiting`), since a provided site has no Windmill
  job to record.

## The result cache

Before a job with a `cache_ttl` runs, the engine looks it up in the result
cache of its platform services (`platform.ResultCache`,
[FX-40](../../docs/semantics/execution.md#fx-40)). An engine with no
services keeps one of its own, per engine (`New` makes it), a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
`cache.go` holds the code.

**Which TTL applies:**

| Job | Its `cache_ttl` |
|---|---|
| An inline step | Its module's |
| A collapsed iteration | Its loop's |
| A path script | Its own; its module's is dropped |
| A subflow by path | Its flow-level `cache_ttl`; its step's is dropped |
| The root flow | Its own |
| A script root run as a script job | Its own |
| An identity, a container, a body's child flow | None |

- **A TTL of 0.** Every job that has a `cache_ttl` looks the cache up, 0
  included. The lookup tests only the stored entry's own expiry, so a job
  with 0 hits an entry that a job with a positive TTL stored under the same
  key. Only a positive TTL stores, since an entry stored with 0 has already
  expired (`jobTTL`).
- **The key** is a SHA-256 over the bundle's digest, the job's kind
  (`flowscript`, `script` or `flow`), its runnable, and each argument in
  sorted key order as its name and stored text. The runnable is an inline
  step's inventory id (which the collapsed iterations of its loop share), a
  path script's path, or a flow's path.
- **What the key leaves out.** No path that differs between jobs enters
  it, so the iterations of one loop share an entry, as [FX-40/d](../../docs/semantics/conformance.md#fx-40) recorded.
  The bundle's digest keeps entries from crossing bundles in a shared
  store.
- **Expiry** is in whole seconds: an entry expires at its store time,
  rounded down to the second, plus the TTL. It is valid while the current
  time, rounded down the same way, is earlier.
- **A store's failure** is a miss, and a `result_cache_unavailable`
  warning in the trace. It never fails the run.

### S3 object arguments

An S3 object argument is one whose whole value is an S3 object record: a
string `s3` member, and optional `storage`, `filename` and `presigned`
strings.

- **With the object in the store.** When the services' object store holds
  the object, the argument is hashed with the object's ETag after its
  name, then its text, unless the job's `cache_ignore_s3_path` is set. The
  ETag is the SHA-256 of the object's content. So new content under the
  same key misses, and with the flag two keys that hold the same content
  share an entry.
- **Without it.** With no object store, no such object or a failed read,
  the argument is hashed as its text, and the flag has no effect. That is
  how a Windmill workspace without object storage behaves.
- **The flag** comes from the module, a collapsed iteration's loop or the
  flow, as the TTL does. A path script's metadata in a sync folder
  carries none.

### A hit

A hit completes the job successfully at once, with the cached result. It
is looked up once a leaf's job is picked up (after `Intercept`, which
answers at the launch, before a job exists), or when a flow job starts.

- **A leaf's hit.** Its record has a job id of its own, `Cached` set, its
  arguments as stored, and no invocation. Its `stop_after_if` applies as
  after any success.
- **A subflow's hit** lists its child flow with no module run.
- **A root run's hit.** Its trace is `Cached`, with no step.
- **A hit is still a job,** with the identity that it would have run with,
  which the job registry knows while its root lives. A leaf's or a script
  root's hit is registered as the facade's executor registers a job that
  ran.
- **A flow job's hit** is its start and its completion at once, so the
  host's checks of both apply around the lookup
  ([FX-38](../../docs/semantics/execution.md#fx-38)). A latch, the
  cancellation (a passed pre-write deadline's included) or a halt there,
  before the lookup or during it, ends the flow as it would any other,
  never with the cached result.
- **A canceled script root's hit** is killed, as its job would be.

### A store

- **A leaf's store** follows a job that succeeded: its result, until now
  plus the TTL on the run's clock. An `Intercept`'s outcome is never
  stored.
- **Every entry is a stored result,** without the NUL escapes that a
  flow's result can still carry in memory from its last module
  ([FX-48](../../docs/semantics/execution.md#fx-48)). So a hit answers the
  stored text.
- **A flow job's store** follows the store table of
  [FX-40](../../docs/semantics/execution.md#fx-40). The flow job stores its
  final result when its completion counts as a success before the recover
  check (`flowEnd.stores`), under its key computed again at the
  completion. That is when:
  - its last module succeeded;
  - a clean stop ended it, its hit then being a success, not `skipped`;
  - its last module's failure or error stop was tolerated;
  - its failure module succeeded, so a failed flow caches that module's
    result, which the next call returns as a success.
- **What stores nothing:** an error stop or a failure that ends the flow,
  a failed failure module, a chaining error and a cancellation.
- **Order.** A flow job's store comes before its completion, so a latch or
  the cancellation that arrives during it still ends the flow. A leaf's
  lookup and store happen inside its module, before the host's completion
  checks.

## Module state and shared code

- **`Config.ModuleState`** is the
  [module state](../../docs/glossary.md#module-state) that every
  invocation runs in ([module state](../../docs/design/0006-module-state.md)):
  [`warm`](../../docs/glossary.md#warm) by default, or `fresh`. The trace
  records it. The flow suite of `wltest` (`TestFlowSuite`) runs the result
  cache's fixtures in both.
- **`Config.SharedCode`** says that the bundle's steps run their
  shared-layout modules ([shared module code](../../docs/design/0013-shared-module-code.md)).
  It is valid in `warm` only: `New` refuses it in another mode.
- **The trace** records shared code (`Trace.SharedCode`), and lists its
  differences from Windmill, a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations),
  first among the run's deviations.

## Waits and time

The engine runs Windmill's sleeps, timeouts and retries
([FX-23](../../docs/semantics/execution.md#fx-23) to
[FX-29](../../docs/semantics/execution.md#fx-29),
[JE-28](../../docs/semantics/expressions.md#je-28),
[JE-29](../../docs/semantics/expressions.md#je-29),
[JE-32](../../docs/semantics/expressions.md#je-32),
[JE-V2](../../docs/semantics/expressions.md#je-v2)), and the monitor of
flows that hang between steps ([JE-2](../../docs/semantics/expressions.md#je-2)).
The host controls the clock, the jitter and the deadlines
([host hooks](../../docs/design/0008-host-hooks.md#the-hooks)).
`waits.go` holds the values and the schedule.

### Sleep

- **When it is read.** A module's `sleep` is read when the next module
  starts its first attempt: never on a retry, and never before the failure
  module.
- **Its value.** A static value is taken as written. A javascript one is
  evaluated with the sleeping module's own result.
- **Its unit.** `sleepDelay` reads the value as Windmill does. A JSON
  integer that fits in 64 unsigned bits is seconds, and a negative integer
  fails ([Failure values](#waits-and-retries)). Any other number (a
  fraction, an exponent, negative zero, a larger integer) is milliseconds,
  value × 1000 truncated, and 0 when negative.
- **What waits.** The next module's `skip_if`, arguments and timeout are
  evaluated at once. Only its job waits, until the scheduled time, so a
  skipped, mocked or identity module waits too.
- **A final module's sleep** is applied by the virtual `<id>-v` identity
  that the compiler appends
  ([OF-17](../../docs/semantics/openflow.md#of-17)).

### Timeout

- **Every module evaluates its own `timeout`,** identity and skipped
  modules included, though only a leaf's job is timed
  ([FX-24](../../docs/semantics/execution.md#fx-24)).
- **A static timeout** is an i32 or null
  ([OF-V2](../../docs/semantics/openflow.md#of-v2)). A javascript one's
  value must deserialise as an i32 (`timeoutResult` and `parseI32` write
  serde_json's texts).
- **Its failures** are chaining errors, as a sleep's are
  ([Failure values](#waits-and-retries)).
- **Unset.** A value of 0 or less is unset, as is a path script's own
  timeout of 0 or less. The step then runs as if neither were set.
- **On a flow step,** a positive timeout is its subflow's wall-clock limit
  ([The flow's own timeout](#the-flows-own-timeout)).
- **On a container,** it bounds only its collapsed body jobs
  ([Containers and waits](#containers-and-waits)).

### Retries

A module with a `retry` is retried, by the decision list of
[FX-2](../../docs/semantics/execution.md#fx-2). The failure module retries
the same way with its own `retry`.

- **`decideRetry` is the completion phase.** A failure, or an error stop
  under `continue_on_error`, evaluates `retry_if`, then the attempts left.
  A throw makes the failure unrecoverable
  ([FX-28](../../docs/semantics/execution.md#fx-28)).
- **Where a due retry goes.** A retry that is due enters the module again.
  After an error stop with no failure module, it ends the flow instead
  ([FX-19](../../docs/semantics/execution.md#fx-19)). An error stop that the
  module does not tolerate, in a flow with a failure module, enters a
  module with a `retry` again without evaluating it.
- **`reenter` is the start step that runs a retry.** It evaluates
  `retry_if` again, where a throw is now a chaining error of a later
  transition, then the attempts. With no retry due, the continue decision
  proceeds as if none had been.
- **What a retry does.** It moves the failed attempt's record to
  `<site>/r:<k>`, raises the counter, and starts the next attempt after
  the delay, with the failed attempt's recorded arguments
  ([JE-V2](../../docs/semantics/expressions.md#je-v2)). The transforms are
  not evaluated again, a pre-run error is not copied, and `RewriteArgs`
  does not see the arguments again.
- **A retry's `previous_result`** is restored
  ([FX-6](../../docs/semantics/execution.md#fx-6),
  [JE-36](../../docs/semantics/expressions.md#je-36)): the flow input at
  the first module; the preceding module's stored result, in jsonb's
  printed form, when that module's status is Success; otherwise the JSON
  string `"{}"`. The failure module keeps its own failed attempt's value
  ([FX-32](../../docs/semantics/execution.md#fx-32)).
- **`retryDelay`** is the schedule of
  [FX-26](../../docs/semantics/execution.md#fx-26), in unsigned 16-bit
  arithmetic: the constant phase, then the exponential phase. That phase is
  seconds^(n+1), saturating at 65535, times the multiplier, wrapping
  modulo 65536. It is jittered when `random_factor` is positive. Every
  delay is capped at 21600 s.
- **The retry state is the flow's**
  ([FX-27](../../docs/semantics/execution.md#fx-27),
  [FX-29](../../docs/semantics/execution.md#fx-29)), kept on its frame: a
  counter and the list of failed attempts. Every module's Success or
  Failure status lists the list (`StepRecord.FailedRetries`). So after a
  tolerated failure, the next module lists an earlier module's attempts,
  and an attempt's site counts its place in that list. A Success clears
  both.
- **A failure module with its own `retry`** resets the counter when it
  starts.
- **A chaining error at the root's first pickup** fails the first module
  ([FX-15](../../docs/semantics/execution.md#fx-15) context 2), which can
  be retried. A retry's re-entry is a later transition, so the same
  failure then fails the flow.

### Waiting

- **The run's clock.** Waiting happens on the run's clock
  (`RunRequest.Clock`), as a timer on the run's context. So a cancellation
  or a deadline cuts it short.
- **A cut-short wait.** The queued job then completes without running,
  with the run's `Canceled` error, stored canceled, as the oracle recorded
  ([FX-38](../../docs/semantics/execution.md#fx-38)).
- **A flow step's queued job** is its subflow's flow job. When it fails
  without running, the trace lists the subflow with its modules, none of
  which started, as Windmill's run shows them never started.
- **Spans.** Each requested wait is a `SpanWait` span.
- **`RecordClock`** records waits instead of sleeping them, and runs
  ahead by them. A run whose clock is not `RealClock` lists the recorded
  clock among its trace's deviations.
- **The jitter** draws come from `RunRequest.Jitter`, or from a source
  seeded with the run's seed, so a fixed seed fixes them.

### Deadlines

- **The hard deadline** is the run's context's.
- **The pre-write deadline.** `RunRequest.PreWriteDeadline` ends the run
  canceled when it passes before the first write-capable module launched.
  The context's cause is then `ErrPreWriteDeadline`, with Windmill's
  default reason and canceler, and the run's cause `pre_write_deadline`.
- **When it acts.** A deadline already passed cancels the run before it
  starts. One that passes before the first write, or while the host's
  `BeforeFirstWrite` decides on it, stops that write. After the first
  write it no longer applies.
- **The actual time decides,** under the write gate, never the delivery of
  the deadline's timer. An atomic state (armed, disarmed or expired) lets
  one of the first write and the timer win. Whoever reads it expired
  cancels the run, idempotently, before acting on it. So the run never
  stops a write before its context says why.

### The flow's own timeout

A flow job carries a wall-clock limit
([FX-52](../../docs/semantics/execution.md#fx-52)): its calling flow
step's positive `timeout`, else its flow's own positive `value.timeout`.

- **Who has one.** A root run and each subflow run under theirs, counted
  from their start. A container body's child flow carries none. The
  metadata `timeout` beside `value` enforces nothing.
- **At the limit,** `runList` cancels the flow's context with canceler
  `timeout` and reason `the flow exceeded its <n>s timeout`. Its jobs end
  as a cancellation ends them: a running step is killed, and a queued one
  is canceled at its pickup.
- **When.** Windmill's server cancels on its monitor's first pass after the
  limit, every 10 s by default. windmill-lite cancels at the limit, which
  the reference leaves unbounded.
- **Its failure module** runs once, with
  `{"error": {"name": "ExecutionErr", "message": "flow terminated due to timeout: <reason>"}}`
  as its `previous_result`, and the module that the flow was at as its
  preceding id. The flow ends as `timedOut` says. The flow job completes
  canceled with the canceled value, whatever that module returned.
- **A root run** ends there, with the cause `flow_timeout`.
- **A subflow** is a failed step to its caller, whose retry,
  `continue_on_error` and failure module apply. The step's job is canceled
  with the canceled value, which lookups read. It carries the timeout
  error or the failure module's result (`flowEnd.delivered`).
- **A subflow that its caller's cancellation reached,** an ancestor's
  timeout included, ends as any canceled child, without its failure
  module.

### The monitor of flows that hang between steps

Windmill's server cancels a flow job whose transition, the work between
two jobs, outlives `FLOW_ZOMBIE_TRANSITION_TIMEOUT` (60 s)
([JE-2](../../docs/semantics/expressions.md#je-2)). The engine does the
same.

- **What it counts.** Per flow (a frame; a collapsed iteration's is its
  loop's flow), the wall time that each transition spends evaluating
  expressions, whatever the run's clock.
- **The limit** is `Config.TransitionTimeout`. Zero means 60 s, and a
  negative value disables the monitor.
- **Past the limit** it cancels the run with canceler `monitor`, and a
  reason that begins as Windmill's: `Flow <id> was cancelled because it was
  hanging in between 2 steps. Last ping: <time>`. The URL and the worker
  details are left out.
- **What refreshes the ping.** A job's completion, and a child flow's (a
  loop iteration's or a branch's), refreshes the flow's ping and starts
  its next transition. A child's completion refreshes a Windmill flow
  job's ping the same way.
- **What does not count:** hooks and waits. Windmill has no hooks, and runs
  a sleep or a retry delay as a scheduled job.
- **An example.** A runaway expression at a later transition, whose three
  20 s attempts and two 5 s pauses take 70 s, ends the run canceled at
  60 s.
- **Flows that have not started.** The monitor cancels only a flow that
  has started, or one that runs on the same worker. A flow has started
  once one of its module attempts, jobs or child flows has completed.
  Windmill puts a flow that has not started back in the queue instead,
  which does not stop the evaluation, and the engine leaves it alone.
- **So at a flow's first module,** a runaway expression runs its three
  attempts, and the module fails with the timeout error after about 70 s,
  as on the oracle ([JE-2/a](../../docs/semantics/conformance.md#je-2), [JE-2/b](../../docs/semantics/conformance.md#je-2)). What a requeued flow does when a
  worker picks it up again is not modelled.

### Unreadable child results

A loop or a `branchall` reads its children's stored results back when it
completes ([FX-42](../../docs/semantics/execution.md#fx-42) rule 9;
`readback.go`). That read fails for a result that the backend's JSON
library cannot read back:

- a number beyond a double's range, which only an identity child can
  carry;
- an array or an object opened at depth 128.

The column in the error counts in the text that PostgreSQL prints for the
stored result, with a space after each `:` and `,`. The container then
keeps no result and stays in progress. Its record is canceled, in
progress, with the children's results so far. What ends depends on the
children:

| The children | What happens |
|---|---|
| The flow's own leaf jobs: the iterations of a collapsed sequential for loop or while loop | The flow fails at once with `{"error": {"name": "InternalErr", "message": "SqlErr: error occurred while decoding column 0: <library text>"}}`, without Windmill's location suffix. No retry, `continue_on_error`, failure module or later step runs (`endUnreadable`). When the flow is a child flow, its parent waits, as in the next row |
| Child flows: any other loop, and every `branchall` | No job fails with the error, and the flow waits |

- **A child flow that ends without telling its parent** is no completion
  of the container that runs it. A parallel loop or a parallel `branchall`
  does not count it, so it releases no held iteration and refreshes no
  ping, and the container's flow waits.
- **A waiting flow is ended** as Windmill's zombie-flow monitor ends it.
  The run is canceled with canceler `monitor`. The reason names the
  waiting flow and its run page,
  `Flow <id> (<WM_BASE_URL>/run/<id>?workspace=<workspace>) was cancelled because it was hanging in between 2 steps.`,
  or for a subflow
  `Flow was cancelled because subflow <id> (<page>) was hanging in between 2 steps.`
- **At once, not after 60 s.** Windmill's monitor acts only after
  `FLOW_ZOMBIE_TRANSITION_TIMEOUT` (60 s by default). The engine cancels as
  soon as a flow starts to wait. So the other children of a parallel
  container, which Windmill would let run until then, are canceled, and
  their records stay: a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **With the monitor disabled** (`Config.TransitionTimeout` negative), the
  run waits until its context ends. A parallel container's other children
  still complete, and their completions release held iterations.

The `all_iters` that a loop's `stop_after_if` reads after each iteration
holds the children's results as stored, with no read back, where
Windmill's read there would already fail. What the predicate then sees of
a result nested deeper than the read back allows, or of a number beyond a
double's range, is what QuickJS-ng makes of that text; no test pins it.
The read at the loop's completion fails either way, as this section
describes.

### The attempt records in the trace

- A failed attempt's record moves to `<site>/r:<k>`. It has no module
  status, and carries nothing on.
- Each record's `Attempt` is the module's attempt that it is of.
- `Job` is its job's id: a script step's leaf job, a subflow step's child
  flow job, or the job that a result-cache hit completed without running.
- `Start` and `Duration` time its job, from its launch after any wait to
  its outcome. `wltest check` reads a run's `delays` from them.

## Containers and waits

Errors, waits and retries apply inside, on and before loops and branches
([CF-21](../../docs/semantics/control-flow.md#cf-21) to
[CF-28](../../docs/semantics/control-flow.md#cf-28)). The flow fixtures
`conformance/flows/control-flow/cf-21` to `cf-28` and `waits`, recorded on
the oracle, settle what the reference leaves open.

- **Inside a body.** A child flow (a loop iteration's or a branch's) runs
  sleeps, timeouts and retries as any flow does, with a retry state and a
  transition monitor of its own
  ([FX-27](../../docs/semantics/execution.md#fx-27)). So the iterations of
  a parallel loop retry independently.
- **A body whose last module has a sleep** ends in the virtual `<id>-v`
  identity, which the trace marks (`ChildFlow.Virtual`,
  [CF-26](../../docs/semantics/control-flow.md#cf-26)). A body module with
  a sleep or a retry never collapses
  ([CF-8](../../docs/semantics/control-flow.md#cf-8)), so a collapsed
  iteration has neither.
- **The sleep before a container**
  ([CF-21](../../docs/semantics/control-flow.md#cf-21)) delays its first
  push of children, and later children do not wait. The first push is the
  first child of a sequential loop or `branchall`, or the chosen branch of
  a `branchone`. For a parallel loop or `branchall` it is every child, all
  pushed at once (recorded by `cf-21/sleep_before_par`).
- **A scheduled child flow** (`frame.due`) is picked up once the delay has
  passed, before any of its modules starts. So a first module that
  completes without a job, an empty container, does not use the delay up.
  A cancellation while it is queued cancels it before its body runs
  ([CF-28](../../docs/semantics/control-flow.md#cf-28)). A collapsed
  iteration's body job waits as any job does.
- **A container's retry delay** schedules its first push the same way. A
  container's own sleep delays the module after it, evaluated with the
  container's result, as any module's.
- **A container's timeout** is evaluated for each child that it pushes, in
  the container's own scope: its `previous_result`, its preceding id and
  its flow's input, with no `iter`, a collapsed loop's included (recorded
  by `cf-21/timeout_scope_flows` and `timeout_scope_collapsed`). Its
  failure is a chaining error of the container's flow
  ([FX-15](../../docs/semantics/execution.md#fx-15)). For the first child
  of a container that is its flow's first module, that error comes at the
  pickup. The timeout bounds each collapsed body job and no child flow
  ([CF-27](../../docs/semantics/control-flow.md#cf-27)).
- **A container's retry** ([CF-22](../../docs/semantics/control-flow.md#cf-22))
  runs it again from scratch after the backoff: its iterator or
  predicates again, and every child. Its `previous_result` is restored as
  a module's is ([FX-6](../../docs/semantics/execution.md#fx-6)). The
  re-entry is a later transition, so a chaining error there (its iterator,
  timeout, `parallelism` or a push) fails the flow at once, as a leaf's
  retry does.
- **The input of a retried loop's first push** is a child job's stored
  input. So a child flow sees that child's `iter` as `iter_parent`, and a
  collapsed body job transforms that job's arguments:
  - a sequential loop's first child starts from its last failed child
    job's input, and later iterations from the loop's own flow input
    (recorded by `cf-22/iter_parent` and `collapsed_input`);
  - every child of a parallel loop starts from the input of its last child
    job to complete, whatever that child's outcome, since the completion
    that finished the loop names it ([CF-22](../../docs/semantics/control-flow.md#cf-22) rule 5, recorded by
    `cf-22-retried/retried`).
- **A container's failed attempt** is recorded as Windmill's job tree keeps
  it: the module's job, its last child job that failed, else its last child
  job, at `<site>/r:<k>`, with a child flow's modules below it.
  - A `branchall`, which has no job of its own, leaves no attempt site.
    Neither does a parallel loop, whose completed entry never lists a
    failed attempt ([FX-27](../../docs/semantics/execution.md#fx-27)). Their attempts still count.
  - What else the failed attempt ran is superseded
    (`StepRecord.Superseded`, `ChildFlow.Superseded`), since the walker no
    longer reaches it.
  - A parallel container's record never lists the flow's failed attempts.
- **`flow_env`** is resolved again
  ([JE-38](../../docs/semantics/expressions.md#je-38)) at each transition
  of a sequential container's flow that pushes its next child, after the
  previous one completed. So the next push's timeout, and a collapsed body
  job's arguments, see what the previous child changed. A collapsed
  iteration's job belongs to its loop's flow, whose resolution it uses
  (`frame.owner`). The predicates evaluated after a child completes keep
  reading the flow's cached map.
- **A loop's `skip_if`** is evaluated once, when the module is entered
  while it still waits to start, as any module's is
  ([FX-17](../../docs/semantics/execution.md#fx-17)). An iteration's result
  never skips a sequential, collapsed or while loop part-way: every
  iteration runs, and the loop returns its whole array (recorded by
  `cf-21/skip_mid`, `skip_mid_flows` and `skip_mid_while`, and for a
  parallel loop by `cf-21/skip_parallel`). A retry that enters the loop
  again evaluates it again.
- **Cancellation** ([CF-28](../../docs/semantics/control-flow.md#cf-28))
  reaches every child, and the run then ends canceled. A canceled
  container completes as a failure. Its array holds the `Canceled` error
  for each child that the cancellation reached, held ones included (a
  collapsed body job's own killed result). The children that had
  completed keep theirs (recorded by `cf-28/held`).
- **A canceled `branchone`** fails with its branch's `Canceled` error. Its
  job, the branch's flow job, is canceled, queued or running.
- **`squash`** ([CF-25](../../docs/semantics/control-flow.md#cf-25)) is
  accepted and ignored. windmill-lite runs a squashed loop unsquashed,
  collapsed or not, sequential or parallel. That gives the values that the
  Community Edition image recorded (the `cf-25` fixture). The stub's error
  in the reference describes a build without the `private` feature.
- **A collapsed iteration whose push fails** (its timeout, or its
  arguments' storage) has no site, since its job was never pushed. Its
  record is superseded.

### A squashed loop's runtime

The Community Edition image runs each body step of a squashed sequential
loop in one runtime for every iteration. There, module-scope state carries
over, and `WM_JOB_ID` reads `NOT_AVAILABLE`
([CF-25](../../docs/semantics/control-flow.md#cf-25), recorded by the
`cf-25-runtime` fixture). windmill-lite does not reproduce that runtime:

- In `warm` and host mode, a script's module state already carries over
  between all its invocations, squashed or not. That is a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- In `fresh` mode, every invocation has a process of its own.
- `WM_JOB_ID` stays the invocation's own job id, which the platform's job
  registry keys each running invocation by.

## Decisions where the reference is silent

The reference leaves these questions open. Each row states the engine's
choice, and the test or the oracle recording that pins it, or says where
only the code holds it.

### Steps and errors

| Question | The engine's choice | Why | Pinned by |
|---|---|---|---|
| What text does an identity job carry on? | It reads its arguments from storage, decoded member by member as a flow input's are. So its result, the value that a skipped, mocked or identity module carries on, is its `previous_result` in jsonb's printed form | The same text that [JE-44](../../docs/semantics/expressions.md#je-44)'s table gives a flow input's member | `TestEngineIdentityResultText` |
| Does an intercepted module run a job or write? | An `Intercept` runs no job (`Ran` false). `BeforeFirstWrite` is asked only before a leaf's launch that `Intercept` declined, or before a subflow call's child flow. An intercepted module or an identity job writes nothing | No job runs, so nothing writes | No test of `Ran` for an intercept (`run.go`); `TestEngineFirstWriteOnSubflowCalls` for the subflow call |
| What does a refused argument say? | The push fails with PostgreSQL's message in the database error's display | [FX-50/a](../../docs/semantics/conformance.md#fx-50) predicts it from the source; [Q-FX-8](../../docs/semantics/execution.md#q-fx-8) leaves the recording open | `TestEngineArgumentFailures` |
| How does an invalid entrypoint override fail? | As the worker's pre-run error at the job's pickup, before any wrapper runs: `ExecutionErr`, with `step_id` in a flow and none in a script job | Windmill's worker checks the override before it runs the job | `TestEngineArgumentFailures`, `TestEntrypoint` |
| Does the storage failure of [FX-51](../../docs/semantics/execution.md#fx-51) case 1 apply to identity jobs? | It applies to a leaf's outcome, never to an identity job's result, on which the `wm_failure` marker is checked ([FX-13](../../docs/semantics/execution.md#fx-13)). An `Intercept`'s outcome, which runs no job, is checked for `wm_failure` but not for storage | Only a job's completion inserts a result | `TestEngineStorageFailures` (a leaf's result, a refused push); the identity and `Intercept` exemption is in `run.go` only |
| Which reason and canceler does a canceled step's text name? | The same as the run's `Canceled` error, Windmill's defaults included | One cancellation | `TestEngineCancellation` |

### Loops and branches

| Question | The engine's choice | Why | Pinned by |
|---|---|---|---|
| Is an empty body's iterator evaluated? | No | An implementation choice: with no body there is no iteration to feed; the reference records no reason | `TestLoopStopsOfTheFlow` |
| What is a collapsed iteration's previous id, and what does a read of its own loop see? | Empty; the iterations so far, `[]` at first | Windmill evaluates a collapsed iteration with an empty previous-step id ([CF-8](../../docs/semantics/control-flow.md#cf-8)'s evidence). What a read of its own loop sees is the engine's reading; the reference records none | `TestLoopCollapsed` (the reads of its own loop); the empty id in `run.go` only |
| What does a clean `stop_after_all_iters_if` stop do on a loop that failed? | It keeps the failure and the array, and still stops the flow. An error stop replaces the carried value | A clean stop changes where the flow goes, not what the module carries; an error stop carries its own error | `TestContainerCleanStopAfterFailure` |
| What does a loop whose `stop_after_if` throws do? | It fails as a step's does, with no `stop_after_all_iters_if` after it | [FX-21](../../docs/semantics/execution.md#fx-21) states it for a step; the engine applies the same rule to a loop | `TestLoopStopsOfTheFlow` |
| Is a static null `parallelism` a limit? | No | A static null timeout is no timeout, so a static null limit is read the same way | `TestLoopParallelStartErrors` |
| Whose `pass_flow_input_directly` does a collapsed body take? | Its loop's, not its own; it then receives its iteration's flow input | [CF-8](../../docs/semantics/control-flow.md#cf-8) item 5: a collapsed body runs as its loop's job | `TestLoopCollapsed` |
| What is the text of P after a loop? | The compact array, with every number written again in the backend's notation (`1e+21`, `1e-6`, `1000.0`, `-0.0`) by `platform.Reprint` (`aggregate`) | The backend builds the array itself ([JE-44](../../docs/semantics/expressions.md#je-44), [FX-42](../../docs/semantics/execution.md#fx-42) rule 9); `platform.Reprint` is the writer that the platform's argument resolution already uses | `TestSerdeTexts` (its `arrayOf` cases) |
| What does a child result that the backend cannot read back do? | It leaves the container in progress ([Unreadable child results](#unreadable-child-results)). The sites that show the iterations so far without reading them back keep such a result as stored: a running loop's job list, `all_iters`, an interrupted loop's result | Only a read back can fail; a site that shows the stored text has nothing to refuse | `readback_test.go` |
| Which syntax errors does an iterator or a `parallelism` that is not JSON give (a replaced `JSON.stringify`, [JE-15](../../docs/semantics/expressions.md#je-15))? | serde_json's error for an empty text, a character that no value starts with, a broken `true`, `false` or `null` (``expected ident at line 1 column 2``), a leading zero (``invalid number at line 1 column 2``), or more than white space after a value (``trailing characters at line 1 column 2``). serde_json checks these after the value's type and range. Its other syntax errors are not reproduced; whatever is not one JSON value of the expected kind is refused | A reading: the errors reproduced are those of a text that does not start with a JSON value, a leading zero, and trailing text; the reference records no reason | `TestSerdeTexts` |
| What does an empty chosen `branchone` branch at the first module of a child flow pass on? | The JSON string `"{}"`, as at the root's first module | [CF-14](../../docs/semantics/control-flow.md#cf-14) says "the first step of its flow", which a child flow's first module also is | `TestBranchOneEmptyBranch` (at the root's first module); the child flow's case in `branch.go` only |
| What job does a `branchone` whose predicate threw have? | A nil job, as an empty branch has ([OR-11](../../docs/semantics/oracle.md#or-11)) | No branch ran, so results reads of it behave as for an empty branch | `TestBranchOnePredicateErrors` |
| Is a stop that a branch propagates followed by the `branchone`'s own `stop_after_if`? | No | An implementation choice: the propagated stop already ends the module; the reference records no reason | `TestBranchOneStops` |
| What last carried value does a child flow deliver when it aborts on a chaining error before a sequential container's child after the first (its timeout or the push)? | The flow's last result: for a loop, the previous child's result, or the flow's input for a loop at its flow's first module; for a sequential `branchall`, the previous branch's result | The aborted child never produced a result, so the last one that exists is carried, as [CF-23](../../docs/semantics/control-flow.md#cf-23) route 3 does for a collapsed loop | `TestBranchOneAbortedMidLoop` |
| What does a child flow whose failure module's start fails deliver? | The failing value | [CF-23](../../docs/semantics/control-flow.md#cf-23) route 3 names a module's start only; the engine treats the failure module's start as one | No test of a child flow's own failure module (`run.go`); `TestBranchOneAbortedFailureModule` covers the root's failure module getting an aborted branch's value |
| Is an error stop that a `branchone` or a sequential `branchall` propagates an error stop of the module? | Yes, and the module's `continue_on_error` does not tolerate it, wherever the module stands, the last included | [CF-12](../../docs/semantics/control-flow.md#cf-12) item 4: the completion that carries a forwarded stop turns the container's `continue_on_error` off, wherever it stands. A step's own error stop differs: at the last module `continue_on_error` tolerates it ([FX-2](../../docs/semantics/execution.md#fx-2) rule 8, [FX-19](../../docs/semantics/execution.md#fx-19)) | `TestContainerForwardedStopNotTolerated` |
| Does a clean stop with `skip_if_stopped` in a sequential `branchall` flag the `branchall` skipped? | Yes | [FX-20](../../docs/semantics/execution.md#fx-20)'s "the same skipped flag" says so of a `branchone` | `TestBranchAllSequential` |
| What does a clean `stop_after_all_iters_if` stop of a failed loop or `branchall` leave? | The module failed, with no skipped flag (`skip_if_stopped` included), carrying its array. Its child flow, if any, forwards it as the stop of a failure | The clean stop keeps the failure, as for a loop above, so the skipped flag of a clean stop does not apply | `TestContainerCleanStopAfterFailure` |

### Waits and retries

| Question | The engine's choice | Why | Pinned by |
|---|---|---|---|
| When does a retry draw its jitter? | Once, at re-entry. The completion phase checks only `retry_if` and the attempts left | An implementation choice; the reference records no reason | `TestEngineRetryJitter` (the draws' values); when they are drawn is in `run.go` only |
| What arguments does a retried failure with no job get (the root's first-pickup chaining error)? | An empty argument map | A failed transform's retry gets one too: no arguments were ever stored | `TestEngineTimeoutValues` (the retry of the root's chaining error); the empty map is in `run.go` only |
| What if re-entry finds no retry due after the completion found one (a `retry_if` that changed its answer)? | The continue decision proceeds as if no retry had been due | An implementation choice; the reference records no reason | No test (`run.go`, `reenter`) |
| Does the failure module's counter reset clear the list of failed attempts? | No | An implementation choice; the reference records no reason | `TestEngineFailureModuleRetry` |
| Which errors does a sleep's or a timeout's value that is not JSON give (a replaced `JSON.stringify`, [JE-15](../../docs/semantics/expressions.md#je-15))? | serde_json's syntax error, through the helpers that the iterator and `parallelism` use (`serde.go`). It covers a broken or truncated literal, a character that no value starts with, a leading zero, then more than white space after the value, at serde_json's positions, lines included. Its other syntax errors read `expected value` where the decoder stopped. An i32 is checked for type and range before what follows it | The oracle recorded these texts; one set of helpers keeps the iterator's and the waits' errors alike | The oracle's recordings `fx-23/sleep_not_json`, `fx-24/js_not_json`, `fx-24/js_leading_zero`; `TestEngineTimeValuesNotJSON` |
| What does a sleep's evaluation that cannot read such a text say? | ``Expected a number value, had error instead: ExecutionErr("Error parsing result as serde_json::value::Value: <serde error>. Value was: <text>")`` | The oracle recorded it | `fx-23/sleep_not_json`; `TestEngineTimeValuesNotJSON` |
| How does serde_json name a float of the wrong type? | In `rustFloat`'s spelling (``floating point `1e+21` ``). A negative zero is a float (`-0.0`), which `serdeUnexpected` reads as one, for the iterator's and `parallelism`'s errors too. A `Value`'s debug form writes its floats so too | The oracle recorded the exponent case; the negative zero follows serde_json's own number types | `fx-24/js_exponent`; `TestTimeoutResult`, `TestRustValueDebug` |
| What does a sleep beyond about 292 years do? | It waits `math.MaxInt64` nanoseconds (about 292 years), however long the sleep asks for: the delay saturates (`maxDelay` in `waits.go`) | Windmill's scheduled time would overflow; a saturated wait is the nearest defined behaviour | `TestSleepDelay` |
| In which order does serde_json's `Value` debug form write an object's members? | Insertion order | The backend builds serde_json with `preserve_order` ([JE-44](../../docs/semantics/expressions.md#je-44)) | `TestSleepDelay` |

### Containers

| Question | The engine's choice | Why | Pinned by |
|---|---|---|---|
| Is a container's timeout evaluated before or after its `parallelism`, for each child? | Before | The reference lists them in that order | `TestContainerTimeoutPerChild` |
| What last result does an abort before a later iteration of a loop of child flows carry? | The previous child flow's result | [CF-23](../../docs/semantics/control-flow.md#cf-23) route 3 says so for a collapsed loop | `TestBranchOneAbortedMidLoop` |

## Tests

The tests compile synthetic [sync folders](../../docs/glossary.md#sync-folder)
with the IR compiler, and run them on the real QuickJS-ng engine, with
steps answered in Go by fake executors. They need no Bun and no
environment variable. `internal/expr` builds QuickJS-ng with cgo, so they
need a C compiler.

### Run the tests

1. Run the package's tests, as CI's Go jobs do:
   `go test -race ./internal/engine`
2. Run one test by name, for example:
   `go test -race -run '^TestEngineRetryIf$' ./internal/engine`

CI runs them in the jobs `Go (ubuntu-latest)` and
`Go (macos-latest, packages)`, with `go test -race -timeout 45m ./...`;
the macOS part adds `-skip '^TestFlowSuite$'`
([docs/ci.md](../../docs/ci.md#the-go-jobs)). The flow fixtures run the
engine on the real Bun harness in `wltest check`
([cmd/wltest](../../cmd/wltest/README.md#flow-fixtures)).

### Test files

| File | What it checks |
|---|---|
| `engine_test.go` | Transforms, the results routes and the shuffled orders. Identity, `skip_if` and `mock` (a table of their rules), with `Intercept`, `RewriteArgs` and the skipped classification; the `wm_failure` marker on an identity job's result (`TestEngineIdentityManualFailure`). `stop_after_if`, the chaining errors, the argument failures, arguments that are not JSON (`TestEngineNonJSONArguments`) and the keys at the storage boundary. The timeouts' precedence (`TestEngineTimeouts`), repeated ids, every hook seam, the refusals of `New`, and concurrent runs under `-race` |
| `errors_test.go` | `continue_on_error`, the failure module (its scope; a failing, skipped or chaining one; no stop) and the recovery table. The error-stop matrix with and without tolerance and a failure module, and the unrecoverable stop. Chaining errors with a failure module, the storage failures, and the hooks with a failure module. The cancellation outcome and its orderings, an empty flow's included, and the identity job's text |
| `loop_test.go` | For loops: child flows and their job identity; collapsing; the loop's timeout and failure texts; `skip_failures`. Every break and stop, the empty loop, the iterator's errors, and nested `iter`. A parallel loop's order, `parallelism`'s release order (N = 2, 1 and 0, an `ai` one, a different N per child), continue-all and start errors. Chaining errors in a child flow, hooks, host signals, `HaltAfter`, the origin of nested errors, concurrent runs and serde's texts |
| `while_test.go` | While loops: collapsed and child-flow iterations with their `iter`, `previous_result` chain, job identity and break. Every way that one ends, a loop that only the cancellation ends, `HaltAfter`, and a canceled container's failure |
| `branch_test.go` | Branches: the predicates, the chosen branch's child flow and job identity, and the empty branch. Every stop through a `branchone` and a sequential `branchall`. Parallel and sequential `branchall`, `stop_after_all_iters_if`, `same_worker`, hooks, signals, the origin of errors and concurrent runs. Aborts (`TestBranchOneAborted*`), a clean stop of a failed container (`TestContainerCleanStopAfterFailure`) and pending halts (`TestHaltAfterWaitsForContainerStops`, `TestPendingHaltStopsSiblingNextModule`) |
| `subflow_test.go` | Subflows by path: the child flow, its input, results scope and job identity; a body inside one under a caller's loop; the caller's results and `flow_env` that it reads. Every outcome: a failure, a tolerated one, both failure modules, a recovered one, each kind of stop. A pickup error, an abort at a later transition (`TestSubflowAborted`), hooks, `same_worker`, the executor check and concurrent runs |
| `subflow_retry_test.go` | A retried flow step: the failed attempt's child flow and modules at `r:<k>`, the retry's arguments and each attempt's flow id. Running out of retries (plain, under `continue_on_error`, with a failure module). A child's own retried step, and a retried flow step and loop inside a retried subflow, in sequential and parallel loops. An input transform that throws |
| `bodies_test.go` | Failure modules in bodies: the copy per failed iteration, its scope and id. No second run, with or without `skip_failures`, and through a clean stop of a failed inner loop under a loop, a `branchone` and a sequential `branchall`. The recovery count of each container, and an error stop in a body |
| `container_outcomes_test.go` | A retried parallel loop's base and attempt record; `continue_on_error` after a failed parallel container or a forwarded error stop; the refusal of a container as the failure module |
| `containers_test.go` | The sleep before every kind of container on the recording clock, with a child flow whose first module is an empty loop and a cancellation while the child is queued. `flow_env` resolved again before each sequential push, and a container's timeout per child and its routes. A container's retry: the re-run, the attempt record, `iter_parent`, a `branchall`, spent attempts. A loop's `skip_if` evaluated once, `squash`, and waits and retries in a parallel loop's child flows with the virtual identity |
| `waits_test.go` | The values of `sleep` and `timeout`: every case of [JE-28](../../docs/semantics/expressions.md#je-28)'s and [JE-29](../../docs/semantics/expressions.md#je-29)'s texts, with serde_json's positions. The schedule of [FX-26](../../docs/semantics/execution.md#fx-26): [FX-26/a](../../docs/semantics/conformance.md#fx-26) to /e, the wrap in the jitter's product, the saturating sum, the factor's bounds. The seeded jitter, a `Value`'s debug form, and the recording clock |
| `retry_test.go` | Argument reuse, the attempt records, and the flow-wide counter and list ([FX-29/a](../../docs/semantics/conformance.md#fx-29) and /b). The schedule and jitter; `retry_if`, a throw included; error stops with a retry; the failure module's retry; the restored `previous_result`. Sleeps (each value, the final module's, not on a retry, not before the failure module, chaining errors), a canceled sleep, and timeout values (`TestEngineTimeoutValues`). The monitor, the pre-write deadline, `BeforeFirstWrite` on subflow calls, and time values that are not JSON |
| `flowtimeout_test.go` | The flow's own timeout: a root's and a subflow's; a step's timeout that replaces a subflow's; the failure module; a tolerated or retried subflow; the unset values; an ancestor's timeout |
| `readback_test.go` | Unreadable child results: the flow left waiting and canceled by `monitor`, for each kind of container of child flows. A collapsed body's failure at once, with its column in PostgreSQL's printed text, also in a `branchone`'s branch and in a subflow. A parallel child (`TestReadBackInAParallelChild`), and the monitor disabled (`TestReadBackInAParallelChildWithoutMonitor`, `TestReadBackWithoutMonitor`) |
| `cache_test.go` | A step's hit, expiry, a zero TTL, equal arguments, S3 object arguments and a collapsed loop. Whose `cache_ttl` applies ([FX-40](../../docs/semantics/execution.md#fx-40)): a path script's own, never its module's, and none for an identity or a subflow step. A flow job's store, the root flow's cache, a host store per bundle and a failing one. `Intercept` bypassing the cache, the module state, the host's checks and a hit's job |
| `dispatch_test.go` | The dispatcher: a script root and a flow root as separate runs, a failure's value, the arguments resolved and stored. The script job's identity, alone and with a dispatch's links; the refusals (`TestDispatchRefusesUnstorableArguments`). `Close`, a scheduled start, links that outlive the parent, a script job's own timeout, the records following their root, `flow_user_state()`, and a script root's own `cache_ttl` (`TestRunScriptCache`) |
| `providers_test.go` | The resolved arguments: `$WM_*` from the reserved variables and from a resource's stored fields, and a secret. The resolved arguments after a timeout, a cancellation and an executor's error; a resolution failure and the run's settlement. `flow_env` per transition, and its unresolved fallback. The helpers, a helper's failure, and an engine with no platform |
| `provided_test.go` | Host-provided sites: a named and a static provider, the provider's arguments, the site's `stop_after_if`, its retry, a declining provider, a provider's error, no `Provide`. Child flows in the job registry with their paths, and a dispatch from inside an iteration (`TestChildFlowsAreJobs`). A flow job while its provided site waits (`TestFlowWithAWaitingProvidedSiteIsNotServed`) |
| `flowenv_test.go` | What input transforms store from `flow_env`, with the sealed values unmasked (`TestFlowEnvStoredArguments`): exact reads in both spellings, computed reads, a `$res:` entry, a chained reference, a computed key and the whole object. A subflow's input, a collapsed body, the failure module, a host-provided site and a failed secrecy lookup. A secret entry under the empty key (`TestFlowEnvEmptyKey`), and the texts that read an entry by name (`TestReadsByName`) |
| `env_test.go` | A path script's `envs` in a Bun step's environment, and none in a native step's (`TestScriptEnvs`); an entry with no `=` (`TestScriptEnvsWithoutEquals`); `NODE_PATH` (`TestScriptEnvNodePath`) |
| `jobrecord_test.go` | The job records, with an executor that registers each invocation as the facade does (below) |
| `jobpoll_test.go` | The result polls' states (`TestJobPollStates`): a running job, a job pushed for later while a held clock keeps its retry waiting, and that job once completed. Whole results (`TestJobPollWholeResults`, on a configured registry): a step's, a child flow's and a dispatched script's large result, which the record replaces, and the placeholder's text as a result |
| `settle_test.go` | Settlement with a fake executor: completion, abandonment with a late end, a non-final late error, a process exit, a never-dispatched invocation. An `escaped` outcome fixed while the call is pending. Holds before and after `End`, released once, and in a run's context |
| `retain_test.go` | `TestSettledRunLetsGoOfItsValues`: a settled run's step results become unreachable (weak pointers), while a run that ended with an invocation still open gets late output and keeps its values until it settles |
| `cancel_test.go` | `TestSubflowCanceledWhileWaiting`: a flow step canceled while it waits fails at pickup and lists its subflow's modules, none started |
| `precedence_test.go` | `Start`, `Completion` and `AfterContinue`, case by case and over every combination of signals |
| `limits_test.go` | The limits per run on the gated executor: `MaxInFlight` over a parallel branchall, with `D15` (`TestRunMaxInFlight`), and `MaxHeldBytes` from a step's result and from the arguments (`TestRunHeldBytes`), at the stored size (`TestRunHeldBytesStoredSize`). A script job's arguments latch it before it runs, and cached results count (`TestRunHeldBytesBeforeAndWithoutExecution`). A reservation returns at its invocation's settlement, under the runtime's total too (`TestByteBudgetReturnsAtSettlement`), and no sum overflows (`TestByteBudgetBoundaries`). Dispatched runs without an executor fail, their host calls made once (`TestDispatcherRunsWithoutAnExecutor`) |
| `outcome_test.go` | Every row of `StoreOutcome`, `ManualFailure`, `FailureValue`, `LogTail`, `Entrypoint` and its walks, `TimeoutError` |
| `hooks_test.go` | `NoHooks`, `HookLatchError`, the latch under concurrency, `RealClock.Wait` |
| `storage_test.go` | `storeMembers`' held values; `argsWithoutNUL` |
| `executor_test.go` | `CancelReasonOf`, `Trace.Step`, `Origin.Generated` |
| `quickjsonly_test.go` | `TestQuickJSOnlyReachesThePipeline` ([The expression pipeline](#the-expression-pipeline)) |
| `example_test.go` | `ExampleNoHooks`: a host that halts before one site |

The tests of `jobrecord_test.go`:

| Test | What it checks |
|---|---|
| `TestJobRecordsDuringARun` | The records that a step reads of its own running job, of the run's flow job with its status, and of the completed step before it, byte for byte, and its arguments read |
| `TestEndedRunReleasesItsJobRecords` | A run that has ended leaves its step results unreachable (weak pointers), its jobs known to the root lookup and no longer read |
| `TestJobRecordsOfARetriedSubflow` | Both flow jobs of a subflow that failed and then succeeded on its retry, each with its own step's job |
| `TestJobRecordOfAnUnlaunchedSubflow` | A subflow whose input transform failed |
| `TestJobRecordsOfSkippedAndMockedSubflows` | The identity jobs of a skipped and a mocked flow step |
| `TestJobRecordWhileAModuleWaitsForItsRetry` | A flow job while a module waits for its retry delay: its entry waits for its executor and names the retry's job, read queued and not running, which the retry then runs as |
| `TestJobRecordsOfNodesAndParallelContainers` | `no_inherited_flow_env` on stored nodes, and the jobs of parallel containers |
| `TestJobsPushedForLaterKeepTheirIds` | From a sibling branch, it reads seven jobs that wait for a sleep, each queued and not running. They are a skipped step's, a cached step's, a step's whose input transform fails, a step's whose entrypoint fails, a skipped loop's, a mocked `branchone`'s, and a failing flow step's subflow. Each completes with the id that it waited with |
| `TestJobRecordLargeValues` | 100,000 times `x` is read whole, while 100,000 characters that do not compress, as a result and in arguments, are replaced, and the arguments read serves them |
| `TestReplacedFromNinetyThousandBytes` | The replacement at 89,999, 90,000 and 90,001 bytes stored, with jsonval's boundary texts |

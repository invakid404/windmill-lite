# internal/engine

**Current state:** the contracts of milestone K1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)) and the
walking skeleton's interpreter (W1). This README covers `internal/engine`
and its `testdata/`. The package holds the seams that the flow interpreter
is built against: the step executor, the hook interface with no-op
defaults, the precedence of host signals, and the run trace.
`skeleton.go` is scaffolding that runs one sequential flow on those seams,
so that W1 could exercise the process model on the pinned Bun; milestone
N1a replaces it with the engine core. The harness that executes steps is
[internal/harness](../harness/README.md).

## Contracts

| File | Contents | Built on by |
|---|---|---|
| `executor.go` | `StepExecutor`, `Invocation`, `StepOutcome`, `CancelReason`, `Reporter` and `SettleKind`: one leaf run over the harness protocol ([ADR 0004](../../docs/design/0004-harness-protocol.md)), and its settlement | C1 implements it; W1, C3a, N1a call it |
| `settle.go` | `RunSettlement`: counts a run's unsettled invocations and says once when the run has settled | N1a calls `Hooks.OnRunSettled` from it |
| `hooks.go` | `Hooks` (H1–H3, H6–H9, H12 of [ADR 0008](../../docs/design/0008-host-hooks.md)), `NoHooks`, `HookLatchError`, `Site`, `Run`, `Latch` (H5), `Clock` and `RealClock` (H10) | N1a calls them; F2a, F2b and F2c expose them to hosts |
| `precedence.go` | `Start`, `Completion`, `AfterContinue` and their `Verdict` | N1a's decision list |
| `trace.go` | `Trace`, `StepRecord`, `Origin`, `Late`, `Warning`, `Span` (H9; run spans of [ADR 0011](../../docs/design/0011-differential-testing.md)). The trace a run returns is a snapshot nothing changes later; late output goes to the snapshot `OnRunSettled` receives | N1a fills it; wl returns it |
| `skeleton.go` | `Skeleton`, `NewSkeleton`, `SkeletonRun`, `StaticIntercepts`: the walking skeleton's interpreter (below) | W1's `windmill-lite host`; N1a replaces it |

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
  as `Late` (deviations D2 and D9).

## Settlement

An invocation settles when its entrypoint call can run no more code, and
`Reporter.Settled` says so exactly once per `Execute` call:

| `SettleKind` | When | Relative to `Execute` returning |
|---|---|---|
| `completed` | the call returned or threw | before, normally; later for an abandoned call, or one whose outcome was fixed before it ended (an intercepted `process.exit`, D11, or an attributed process-level error that failed it as `escaped`) |
| `not_started` | the invocation never reached a harness | before |
| `process_exited` | the harness process ended before the call did | before when the invocation was in flight; later when it had been abandoned or its outcome was fixed early |

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

## The walking skeleton

`Skeleton` (milestone W1) runs one flow at a time per run, sequentially,
from a bundle directory that holds each flow as
`flows/<flow path>.flow.json`, the flow's OpenFlow document as JSON. It is
scaffolding: its review bar was that it retires W1's gates, and N1a, B2b,
C3a and F4 replace its parts.

- **What it runs.** Script steps by path and inline Bun steps (whose module
  is the bundle's `scripts/bun/<flow path>/<id>.mjs`), with `static` and
  `javascript` input transforms, a static step `timeout`, and H1
  interception at launch. `NewSkeleton` refuses any other module kind or
  feature with `ErrSkeletonUnsupported`, at load.
- **Transforms.** Static values are copied verbatim
  ([JE-40](../../docs/semantics/expressions.md#je-40)). A javascript
  transform is a stand-in for B2b's pipeline: the text trimmed, in
  expression or statement mode
  ([JE-13](../../docs/semantics/expressions.md#je-13)), with `flow_input`,
  `previous_result` and `params` bound only when the text names them
  ([JE-10](../../docs/semantics/expressions.md#je-10)), serialised as
  `JSON.stringify(value ?? null)`
  ([JE-15](../../docs/semantics/expressions.md#je-15)), and evaluated in
  QuickJS-ng. `params` is `{}` and `previous_result` is the flow input at
  the first step ([JE-23](../../docs/semantics/expressions.md#je-23),
  [FX-6](../../docs/semantics/execution.md#fx-6)). The shortcuts and
  rewrites of JE-6 to JE-12 are left out.
- **Steps.** Each runs through the `StepExecutor` with the core `WM_*`
  variables, with the run's id as `WM_FLOW_JOB_ID` and the invocation's as
  `WM_JOB_ID`. The timeout is a timer in the engine; when it fires the step
  is abandoned and fails with
  [WR-37](../../docs/semantics/languages.md#wr-37)'s text.
- **Failure.** The flow stops at the first failed step, whose result is the
  flow's ([FX-36](../../docs/semantics/execution.md#fx-36)). The failure
  values follow [FX-12](../../docs/semantics/execution.md#fx-12) and
  [FX-14](../../docs/semantics/execution.md#fx-14); C3a and N1a own the
  final mapping. The final result is stored as jsonb.
- **Host signals.** `Start` and `Completion` decide the latch and
  cancellation at every module; a canceled run ends with no result, and the
  run settles (`OnRunSettled`) once its abandoned invocations have.
  `StaticIntercepts` is H1's no-code form, a fixed result per module id.

`testdata/w1/` is the synthetic bundle of W1's tests: the flows, and step
modules written by hand, among them `scope_shim.mjs`, which reads the
host-provided module `f/w1/scopes.ts` through a hand-written shim, and
`scope_copy.mjs`, which bundles its own copy of it.

`skeleton_test.go` checks the interpreter with a fake executor and the real
QuickJS-ng engine. `skeleton_bun_test.go` runs it on the real Bun harness
and reports W1's first per-step latency figure; it needs `WL_BUN`, like the
harness tests.

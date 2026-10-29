# wl

**Current state:** milestones F1, F2a and F2b of
[ADR 0012](../docs/design/0012-layout-and-milestones.md): the core of
[ADR 0009](../docs/design/0009-host-adapters.md), and the host hooks of
[ADR 0008](../docs/design/0008-host-hooks.md) as Go callbacks with the
bundle's mandatory host-provided sites (F2a), and its budgets and latches
(F2b: write-capable steps and the first-write signal, deadlines, the
latch, fatal error names, the clock and the jitter). This README covers `wl`,
the public Go library API and the only exported engine package
(`github.com/invakid404/windmill-lite/wl`). A host opens a built bundle
([ADR 0010](../docs/design/0010-packaging.md)) with `Open`, runs its flows
and root scripts with `Run`, reads what it opened with `BuildInfo`, and
stops it with `Close`. Every adapter of ADR 0009 is a transport over these
calls: the command line's `windmill-lite run`
([cmd/windmill-lite](../cmd/windmill-lite/README.md)) is the first, and
`wltest check` ([cmd/wltest](../cmd/wltest/README.md)) runs the flow suite
through it. Host-mode
transport (F3a), host plugins in TypeScript (F2c) and the go-plugin
adapter (F5, `wl/plugin`) come later, as fields of `Options` and
`RunOptions` and as runtimes over the same calls, so a host written
against this API keeps compiling.

## Files

| File | Contents |
|---|---|
| `wl.go` | `Options`, `Resource`, `Variable`, `ResultCache`, `Runtime`, `Open`, `BuildInfo`, `Close`, `Version`, `ErrUnsupported`, `ErrEngineVersion`, `ErrClosed` |
| `run.go` | `RunOptions`, `RunPlatform`, `Result`, `Run`; `Clock`, `RealClock`, `RecordClock`, `Jitter`, `SeededJitter`, `ErrPreWriteDeadline` (H4, H10); the trace types (`Trace`, `PlatformCall`, `Warning`, `Cancellation`, aliases of the engine's); the executor that collects each run's platform calls |
| `hooks.go` | `Hooks`, `Interceptor`, `ArgRewriter`, `Provider`, `Call`, `Outcome`, `Selector`, `Site`, `SitePath`, `StepRecord`, `RunHandle`, `FatalError`, `ErrMissingProvider`, `ErrProviderDeclined`, `ErrAmbiguousSelector`, `ErrSelector`: the hooks and providers as Go callbacks, selector matching, and their adapter to the engine's hook seams |

## Open

`Open(path, Options)` opens a bundle directory or `.wlb` archive:

1. **Paths.** The bundle, the data directory and the harness runtime are
   made absolute, since harness processes run in directories of their own.
   The data directory is `Options.DataDir`, or a temporary one that `Close`
   removes.
2. **Verification.** A directory is verified in place
   (`bundle.Verify`); an archive is verified and extracted
   (`bundle.Extract`) into a directory of its own, `<data>/bundle-*`, which
   `Close` removes, so a data directory the host keeps opens again,
   however the last open ended. `Options.Digest` pins the bundle digest.
3. **Engine version.** The manifest's engine version must be the runtime's:
   `Options.EngineVersion`, or `Version()`, the version linked in with
   `-ldflags "-X github.com/invakid404/windmill-lite/wl.version=<v>"`, else
   the module's version from the build information, else `(devel)`.
   Otherwise `Open` fails with `ErrEngineVersion`. `ir.json` must be of the
   IR version this engine reads.
4. **Module state.** The manifest's, or `Options.ModuleState` (`warm` or
   `fresh`, [ADR 0006](../docs/design/0006-module-state.md)). The engine
   hands it to every invocation and records it in every trace.
5. **Platform services** ([ADR 0007](../docs/design/0007-platform-interface.md)),
   which the runtime owns: one store with the bundle's `data/` and, at the
   same paths, the host's `Options.Resources` and `Options.Variables`,
   which replace them (a bundle never holds a secret, so secrets come only
   from the host; a `Variable` with `Ciphertext` is Windmill ciphertext
   windmill-lite never decrypts, JE-43); an object store under
   `<data>/objects`; the job registry and flow user state; the result
   cache of `cache_ttl` (N4, FX-40), `Options.Results` (a `ResultCache`,
   which a host can share between runtimes) or one in memory for the
   runtime's runs; the facade
   (`internal/platform/facade`) that serves them to the steps; and the
   default dispatcher over the bundle's program, which runs a root a step
   dispatches as a run of its own (PF3). A step that updates a variable
   through the job API (WA-11) updates the runtime's store, so a later run
   reads the new value.
6. **Harness.** A bundle with steps needs `Options.HarnessRuntime`, the
   Bun harness runtime directory (`harnesses/bun/runtime`) whose tree
   digest the manifest records (drift guard 2); `Open` refuses any other.
   Harness processes start lazily, on the first step: one pool of warm
   processes, or one process per step in `fresh` mode, on `Options.Bun`
   (`bun` on the `PATH` by default), whose handshake checks the harness's
   kind, the pinned Bun 1.3.10 and the bundle digest. A warm process reaches
   the facade through its intercepted `fetch`, a fresh one over loopback
   TCP. Their working directories are under `<data>/work`.
7. **Engine.** The program's expressions are compiled once, on a
   QuickJS-ng engine of `Options.ExpressionSlots` slots
   ([ADR 0003](../docs/design/0003-semantics-placement.md)), and a flow the
   engine does not run yet fails `Open` with `ErrUnsupported`, naming the
   milestone that runs it.
8. **Providers.** Every provider the bundle's host contract names for a
   mandatory host-provided site
   ([ADR 0002](../docs/design/0002-ir-and-loader.md)) must be in
   `Options.Providers`, or `Open` fails with `ErrMissingProvider`
   (`missing_provider`), so no run reaches an unprovided site. A static
   site needs none.

## Run

`Run(ctx, path, args, RunOptions)` runs the flow at `path`, or with
`Kind: KindScript` the root script, as a script job (WA-4), with `args`, a
JSON object (empty means `{}`). `RunOptions.ID` is the run's job id (a new
UUID by default) and `Seed` fixes the shuffles of the orders Windmill leaves
unspecified and the retry jitter. `Hooks` are the flow run's host hooks
and `Platform` its own platform providers (below). An error means the run
could not start: no such target, arguments that are not an object or
cannot be stored, or a selector of `Hooks` that is not well formed
(`ErrSelector`) or is ambiguous (`ErrAmbiguousSelector`).

The context bounds the run: when it ends, the run ends canceled, with the
reason and canceler of a `*Cancellation` cause
(`context.WithCancelCause`), else Windmill's defaults (FX-38). `Close`
cancels it too, with the reason `the runtime is closing` by
`windmill-lite`; once `Close` has begun, `Run` refuses a run with
`ErrClosed`.

`Result` holds the job id, the status (`success`, `failure`, `canceled`,
`skipped`), the final result in its stored form (FX-36), `MayHaveMutated`
(ADR 0008 H3), the warnings, and for a flow its `Trace`: per site what
happened and why, with provenance and timings, and `PlatformCalls`, every
platform call the run's steps made as the facade recorded it, with the
step's site and job, the route it matched and the answer's status, in the
order the facade answered them. The runtime collects them per run: its
executor maps each invocation to its run and site, and the facade hands
over each call as soon as its answer is decided (`Config.Answered`), before
the step sees the answer. An invocation's run and site stay known until
its run ends, not only until the facade releases the invocation, since a
call the step did not await can be answered after the step settled. So a
run's trace has every call its steps were answered before it ended, those
of a step that settled without awaiting one and of a step that the run's
cancellation abandoned included; a call answered after the run ended, and
the calls of the runs the dispatcher starts, are not in it.

`Run` is safe for concurrent use: runs of one runtime share its harness
pool, its services and its expression engine. A run latched by a hook, a
provider or the engine (H5) has status `latched` and its error in
`Result.Latched`; a halted one (H8) has status `halted`.

## Hooks

`RunOptions.Hooks` gives a run the host hooks of ADR 0008 as Go
callbacks, each optional, over the engine's hook seams, with ADR 0008's
precedence. A hook's error latches the run with it (H5), and so does
`RunHandle.Latch`; a hook never fails a module by returning an error,
which an `Outcome` with `Failed` does. The callbacks of one run can be
called from several goroutines at once (a parallel loop's iterations).

| Hook | Field | What it does |
|---|---|---|
| H1 | `Intercept` | answers for a matching site's job when its module would launch, after `skip_if`, `mock`, its arguments and its timeout are decided: `Static`, a fixed result, or `Func`, which returns an `Outcome` or declines; the first matching interceptor answers, and the completion phase applies to its outcome. The record lists `H1` |
| H2 | `RewriteArgs` | rewrites the arguments of a matching site whose module launches its own job, every matching rewriter in order; the record keeps both and lists `H2` |
| H6 | `OnModuleStart`, `OnModuleEnd` | observe every module, nested ones and iterations included, the end with the site's record |
| H8 | `HaltBefore`, `HaltAfter` | end the run halted before a matching module's first attempt, or once it has its final outcome and the run would start something other than a failure module |
| H9 | `OnRunEnd`, `OnRunSettled` | the run's end, whose warnings join the run's, and its settlement, once its abandoned invocations have settled; both traces carry the run's platform calls as of its end, as the returned trace does |
| H12 | `InvocationEnv` | sets or deletes variables of each invocation's job environment before it runs |
| H3 | `WriteCapable`, `BeforeFirstWrite` | marks the sites the host deems write-capable, a leaf or a subflow call (asked about before its child flow starts, so a veto stops every step below it); before the first of them launches a job in a run, `BeforeFirstWrite` is asked once, and its error vetoes the write and latches the run; parallel launches wait for that one verdict, and a veto stops them all. The approval is the commit point: from it on the run reports `MayHaveMutated`, a failed run too, and the pre-write deadline no longer applies, even when what prepares the launch after it (`InvocationEnv`, the arguments' resolution) stops the job. An interception, a provided site, a mock or a skipped module launches no job, so it is never a write |
| H7 | `FatalErrorNames` | error names that bypass retry, `continue_on_error`, `skip_failures` and `skip_failure`: a module that fails with one latches the run with a `*FatalError`, and the trace lists `H7` among its deviations |

**Selectors** (`Selector`, the host contract's selector type) name call
sites: `{Flow, Module}` names the module with that id in any module list
of the flow (container bodies belong to their enclosing flow), and
`{Script}` every call of a script, a script step by its path and an inline
step by `<flow path>/<module id>`, a host-provided site that replaces such
a step included. A module selector may add `Occurrence`,
required for an id repeated in one module list (otherwise
`ErrAmbiguousSelector` before the run starts), and `Iterations` or
`Branches`, which match within those iterations or branches of the
innermost loop or branching module around the site.

**Providers.** A mandatory host-provided site (ADR 0002, ADR 0008) runs
with its own envelope, its transforms, `skip_if`, `mock`, `stop_after_if`,
retries and `continue_on_error`, and at its launch the site's provider
answers in place of the job its target would have run: a static
contract value, or `Options.Providers[name]`, which gets a `Call` with the
site's stored arguments and returns an `Outcome`, once per attempt. A
mandatory provider cannot decline: declining latches the run with
`ErrProviderDeclined` (`provider_declined`), and the run fails closed. The
site's record has `Ran` set, no job, and `provided` among its hooks.

**Per-run providers** (H13). `RunOptions.Platform` gives a run its own
resources and variables, over the runtime's at the same paths, and its
own resolver (`Resolve`, the host secret layer of ADR 0007), which a host
chooses from its run data, for example per tenant. The run's argument
resolution, `flow_env`, expression helpers and its steps' API reads answer
from them; a step's variable update changes the run's own variable, or the
runtime's where the run has none. The job registry, flow user state,
object store and result cache stay the runtime's, and the runs a step
dispatches read the runtime's providers.

## Budgets and latches

Every one of them is off unless a host sets it (F2b).

- **Cancellation and deadlines (H4).** The run's context is its
  cancellation source and its hard deadline; `RunOptions.PreWriteDeadline`
  is a second deadline that applies only until the run's first write is
  approved (H3), after which it no longer counts. A passed
  deadline ends the run canceled, as a cancellation does, with Windmill's
  default reason and canceler in the Canceled error; a cancellation with
  a `*Cancellation` cause names its own. Both are checked at every module
  boundary, iteration, sleep and retry delay.
- **The latch (H5).** A hook's error, `RunHandle.Latch`, a veto (H3), a
  fatal error name (H7) and a declining provider each latch an error on
  the run: it starts nothing more and ends `latched` with the first error
  latched (`Result.Latched`), which never becomes a step's result.
- **The clock and the jitter (H10).** `RunOptions.Clock` is the clock every
  sleep and retry delay waits on: the real one by default, or a
  `*RecordClock`, which records each wait and returns at once (quick
  waits), and makes the trace list `H10` among its deviations.
  `RunOptions.Jitter` is the retry-jitter source; `SeededJitter` fixes it,
  as `Seed` does.
- **Precedence** (ADR 0008). At a module's start: the latch, then
  cancellation and deadlines, then halts. At its completion: the latch,
  then cancellation, then fatal error names, then Windmill's own continue
  decision; one exception lets an error stop that coincides with the
  cancellation, in a flow with a failure module, still start what
  Windmill's continue decision asks for. The latch-precedence cases of
  `conformance/hooks` hold this order against retry, `continue_on_error`,
  `skip_failures` and the failure module.

## BuildInfo and Close

`BuildInfo()` reports the bundle digest, the engine version, the Bun and
esbuild versions, the harness runtime digests, the module state in effect,
the host contract, the deviations in effect and whether the bundle was
built on another Bun (`nonconformant_runtime`). The deviations are the
manifest's, with those of the module state in effect in place of the
manifest's mode's when `Options.ModuleState` overrode it (D1, D2, D9, D10
and D11 are warm mode's, ADR 0006; `bundle.ModeDeviations`, the rule the
build lists them by).

`Close()` stops admitting runs, cancels the runs in flight and the
dispatcher's and waits for them, then drains the harness pool, closes the
facade and the expression engine, and removes the directory an archive was
extracted into and a data directory `Open` made. All of it is bounded by
10 s; a run still in flight then makes `Close` return an error. It is
idempotent and safe to call while runs start.

## Tests

`wl_test.go` opens bundles written by hand (identity and mocked steps, so
no harness runs): `Open` and `Run` with `BuildInfo` and `Close`, an
archive extracted into a given data directory, the host's variables
replacing the bundle's, the refusals (another engine version, a pinned
digest, an unknown module state, a changed file, another IR version, an
unknown target or kind, arguments that are not an object), and a canceled
run; `Close` ending a run in its 30 s sleep, canceled with the runtime's
reason, and refusing later runs (`ErrClosed`); runs racing `Close`, each
run or refused; an archive opened twice with one data directory, and
again after an open that failed once it was extracted; and `BuildInfo`'s
deviations under an override both ways; and an unawaited call answered
after its step's invocation was released, which still goes to the run in
flight, with nothing of the invocation kept after the run ends.
`hookcases_test.go` runs `conformance/hooks`, the latch-precedence cases,
through the public hooks (F2b). `hooks_test.go` covers the hooks without Bun: H1 by function and static,
H2 with the trace's arguments before and after, H6's observers, H9's
warnings and settlement, H8 before and after, a hook's error and a latch,
an interception narrowed to one iteration, the selector checks
(`ErrSelector`, `ErrAmbiguousSelector`, an occurrence), the run-time half
of ADR 0002's provided-site policy case (the provider's value from the
site's arguments, with the site's `stop_after_if`; `missing_provider`;
`provider_declined`; a provider's error), and H13's variables and
resolver; and a script selector on a provided path script and inline
script, whose H2 reaches the provider and whose halts stop before and
after it, while an optional H1 does not apply. `bun_test.go` builds a real
bundle with `bundle.Build` and runs it on
the pinned Bun in `warm` and `fresh` mode: a step reads a host's variable
and updates it through the job API, both calls are in the trace with the
step's site and job, the next run reads the update, the script runs as a
script job too, and a bundle with steps does not open without its harness
runtime or with another; two runtimes that share a host's result
cache share a `cache_ttl` step's result, which the first stores once; and
a run canceled while its step waits, after the step read and updated the
variable, keeps both calls in its trace, in `warm` and `fresh`; and, in
both, an `InvocationEnv` hook's variable in the step's environment (H12)
and a run's own variable read and updated through the job API (H13), the
runtime's left as it was; and the same platform calls in the returned
trace and in the traces `OnRunEnd` and `OnRunSettled` get, for a run
canceled while its step waits too; and the first write (H3) and the
pre-write deadline (H4): a veto latches and launches nothing, an approval
is asked once for two write-capable steps and makes the run report
`MayHaveMutated`, a veto stops parallel write-capable branches, and the
pre-write deadline ends a run with no write but not one past its first
write; a write-capable subflow call asked about before its child flow,
vetoed and approved, and of two calls of one subflow only the marked one;
and an H12 veto after the approval, which still counts as written. It needs
`WL_BUN` and the build helper's dependencies, and skips without them unless
`WL_REQUIRE_BUN` is set, as CI sets it.

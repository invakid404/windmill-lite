# wl

**Current state:** milestone F1 of
[ADR 0012](../docs/design/0012-layout-and-milestones.md): the core of
[ADR 0009](../docs/design/0009-host-adapters.md). This README covers `wl`,
the public Go library API and the only exported engine package
(`github.com/invakid404/windmill-lite/wl`). A host opens a built bundle
([ADR 0010](../docs/design/0010-packaging.md)) with `Open`, runs its flows
and root scripts with `Run`, reads what it opened with `BuildInfo`, and
stops it with `Close`. Every adapter of ADR 0009 is a transport over these
calls: the command line's `windmill-lite run`
([cmd/windmill-lite](../cmd/windmill-lite/README.md)) is the first, and
`wltest check` ([cmd/wltest](../cmd/wltest/README.md)) runs the flow suite
through it. Hooks as Go callbacks and per-run services (F2a), budgets and
latches (F2b), host-mode transport (F3a) and the go-plugin adapter
(F5, `wl/plugin`) come later, as fields of `Options` and `RunOptions` and
as runtimes over the same calls, so a host written against this API keeps
compiling.

## Files

| File | Contents |
|---|---|
| `wl.go` | `Options`, `Resource`, `Variable`, `ResultCache`, `Runtime`, `Open`, `BuildInfo`, `Close`, `Version`, `ErrUnsupported`, `ErrEngineVersion`, `ErrClosed` |
| `run.go` | `RunOptions`, `Result`, `Run`; the trace types (`Trace`, `PlatformCall`, `Warning`, `Cancellation`, aliases of the engine's); the executor that collects each run's platform calls |

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

## Run

`Run(ctx, path, args, RunOptions)` runs the flow at `path`, or with
`Kind: KindScript` the root script, as a script job (WA-4), with `args`, a
JSON object (empty means `{}`). `RunOptions.ID` is the run's job id (a new
UUID by default) and `Seed` fixes the shuffles of the orders Windmill leaves
unspecified and the retry jitter. `Hooks` are a flow run's host hooks
([ADR 0008](../docs/design/0008-host-hooks.md)): the engine's hook
interface, which only this module's own tools implement (`wltest check`'s
effect sink, stub replay and run groups) until milestone F2a gives hosts
Go callbacks for them. An error means the run could not start:
no such target, or arguments that are not an object or cannot be stored.

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
pool, its services and its expression engine.

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
`bun_test.go` builds a real
bundle with `bundle.Build` and runs it on
the pinned Bun in `warm` and `fresh` mode: a step reads a host's variable
and updates it through the job API, both calls are in the trace with the
step's site and job, the next run reads the update, the script runs as a
script job too, and a bundle with steps does not open without its harness
runtime or with another; two runtimes that share a host's result
cache share a `cache_ttl` step's result, which the first stores once; and
a run canceled while its step waits, after the step read and updated the
variable, keeps both calls in its trace, in `warm` and `fresh`. It needs
`WL_BUN` and the build helper's dependencies, and skips without them unless
`WL_REQUIRE_BUN` is set, as CI sets it.

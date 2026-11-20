# wl

**Current state:** `wl` opens a bundle and runs its flows and root
scripts, with host hooks, providers, budgets, host mode and host plugins.
Only Bun steps run, and the go-plugin, gRPC and HTTP adapters of the
[host adapters ADR](../docs/design/0009-host-adapters.md) do not exist yet.
This README covers `wl/`, the Go package
`github.com/invakid404/windmill-lite/wl`.

`wl` is the only exported Go package of the windmill-lite
[engine](../docs/glossary.md#engine). A Go program, the
[host](../docs/glossary.md#host), opens a
[bundle](../docs/glossary.md#bundle) with `Open`, runs its
[flows](../docs/glossary.md#flow) and [root](../docs/glossary.md#root)
[scripts](../docs/glossary.md#script) with `Run`, and stops the runtime
with `Close`. The runtime runs each [step](../docs/glossary.md#step)'s
script in [Bun](../docs/glossary.md#bun)
[harness](../docs/glossary.md#harness) processes that it starts.

The `windmill-lite run` and `windmill-lite host` commands
([cmd/windmill-lite](../cmd/windmill-lite/README.md)) and `wltest check`
([cmd/wltest](../cmd/wltest/README.md)) use `wl` too.

## What you need

| Requirement | Why |
|---|---|
| Go 1.27.3 or later, and a C compiler | `wl` embeds the [expression](../docs/glossary.md#expression) engine, QuickJS-ng, through cgo. |
| Bun 1.4.3 | The harness processes run on it, and their handshake refuses any other Bun. |
| A bundle that the engine of your `wl` version built | `Open` refuses a bundle that another engine version built, with `ErrEngineVersion`. |
| The [harness runtime](../docs/glossary.md#harness-runtime) of the same version | `Open` checks its digest against the bundle's manifest. It is the directory `harnesses/bun/runtime` of a checkout of the release's tag. |

The engine version of your program is `wl.Version()`. It is the version
of the module `github.com/invakid404/windmill-lite` in the program's build
information, such as `v0.4.1` when your `go.mod` requires that version.
Build the bundle with the engine of the same version: the release's
`windmill-lite` binary, or the command that
`go install github.com/invakid404/windmill-lite/cmd/windmill-lite@v0.4.1`
installs.

## A minimal example

1. Build a bundle with that engine and the
   [build helper](../docs/glossary.md#build-helper) of a checkout of the
   release's tag ([Build a bundle](../harnesses/README.md#build-a-bundle)).
   This example uses the synthetic [fixture](../docs/glossary.md#fixture) of
   [Quick start](../README.md#quick-start), built to `fixture.wlb`.
2. Add the module to your program:
   `go get github.com/invakid404/windmill-lite@v0.4.1`.
3. Open the bundle, run its flow `f/app/main`, and print the outcome:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/invakid404/windmill-lite/wl"
)

func main() {
	rt, err := wl.Open("fixture.wlb", wl.Options{
		Bun:            "/path/to/bun-1.4.3/bun",
		HarnessRuntime: "windmill-lite/harnesses/bun/runtime",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer rt.Close()

	args := json.RawMessage(`{"name": "lite"}`)
	res, err := rt.Run(context.Background(), "f/app/main", args, wl.RunOptions{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Status, string(res.Result))
}
```

What the code does:

- `wl.Open` verifies the bundle, checks the engine version and the harness
  runtime, and returns a `*wl.Runtime`. `HarnessRuntime` is the path of
  the harness runtime in the checkout of the release's tag.
- `rt.Run` runs the flow with its arguments, a JSON object, and returns a
  `*wl.Result`. An error means that the [run](../docs/glossary.md#run)
  could not start.
- `res.Status` is `success` when the flow succeeded, and `res.Result` is
  the flow's result as JSON.
- `rt.Close` cancels the runs in flight and removes the runtime's
  temporary files.

The same code is the example function in
[example_test.go](example_test.go). `go test` compiles it, so a change to
`wl` that breaks the example fails CI. It does not run it, because it
needs a bundle.

## Open

`Open(path string, opts Options) (*Runtime, error)` opens a bundle
directory or a `.wlb` archive. It returns an error when one of these
checks fails:

1. Every file of the bundle matches the bundle's manifest, and the
   bundle's digest is `opts.Digest` when that is set. `Open` first
   extracts an archive into the data directory.
2. This engine version built the bundle (`ErrEngineVersion`), and its
   compiled program is of the version that this engine reads.
3. For a bundle with steps, `opts.HarnessRuntime` is the harness runtime
   that the bundle was built for.
4. `opts.Providers` has every [provider](../docs/glossary.md#provider)
   that the [host contract](../docs/glossary.md#host-contract) names
   (`ErrMissingProvider`). A [host plugin](../docs/glossary.md#host-plugin)'s
   providers count too.
5. The engine runs every feature that the bundle's flows use
   (`ErrUnsupported`).

Harness processes start on the first step. Only a host plugin makes
`Open` start one earlier, to read the plugin's declaration.

The engine version is `opts.EngineVersion`, or else `Version()`.
`Version()` takes the first of these: the version linked in with
`-ldflags "-X github.com/invakid404/windmill-lite/wl.version=<v>"`, the
module's version in the program's build information, or `(devel)`.

### Options

The zero value of each field is its default.

| Field | What it sets | Default | When to set it |
|---|---|---|---|
| `Bun` | The Bun executable of the harness processes. It must be Bun 1.4.3. | `bun` on the `PATH` | When `bun` on the `PATH` is not Bun 1.4.3 |
| `HarnessRuntime` | The harness runtime directory that the bundle was built for | None | For every bundle with steps |
| `DataDir` | The directory that the runtime writes into: an extracted archive, the object store (`objects/`) and the harness processes' working directories | A temporary directory, which `Close` removes | To keep the object store after `Close` |
| `ModuleState` | The [module state](../docs/glossary.md#module-state): `ModuleStateWarm` or `ModuleStateFresh` | The bundle's | To run the bundle in the other module state |
| `Digest` | The digest that the bundle must have | None | To open only one exact bundle |
| `EngineVersion` | The runtime's engine version, which must be the version that built the bundle | `Version()` | When the program's build information does not carry the engine version, such as a build that links its own version in |
| `Resources`, `Variables` | The host's resources and variables. They replace the bundle's at the same paths. | The bundle's `data/` | To give secrets, which a bundle never holds, and values that differ by deployment |
| `Results` | The store of the results of steps that have a `cache_ttl` (a `ResultCache`) | One in memory for each runtime | To share cached results between runtimes |
| `ResourceStore` | The store of the resources that steps write, such as the state of the stock client's `setState` ([The resource store](#the-resource-store)) | A new memory store for each runtime | To share them between runtimes, or to keep them in a store of the host's |
| `Diagnostics` | Where the harness output that no step owns, and the engine's diagnostics about it, go | `os.Stderr` | To capture them |
| `ExpressionSlots` | The number of evaluation slots of the expression engine | `min(GOMAXPROCS, 8)` | To change how many expressions can evaluate at once |
| `QuickJSOnly` | Turns off the engine's Go evaluator of templates, arithmetic, comparisons and predicates over JSON values. Every expression that the shortcuts and the member paths do not answer then runs in QuickJS-ng. Nothing observable changes. | Off | To compare the two evaluators, or to rule the Go evaluator out when a result looks wrong |
| `Providers` | The named providers of the bundle's [provided sites](../docs/glossary.md#provided-site) | None | When the host contract names providers ([Providers](#providers)) |
| `Plugin` | The path of a [host plugin](../docs/glossary.md#host-plugin) module, which every `warm` harness process loads | None | To give [hooks](../docs/glossary.md#hook) in TypeScript ([Host plugins](#host-plugins)) |
| `PluginTimeout` | How long each call to the host plugin waits for its reply | `DefaultPluginTimeout`, 60 s | To change that bound |
| `ReadCache` | Gives each flow run a read cache: the run reads each of the host's and the bundle's resources and variables once, and answers its later reads from that read. A provider's error is not kept, so the next read asks again, and an update of a variable through the run drops its cached value ([the read cache](../internal/platform/README.md#the-read-cache)). A state that steps wrote to the `ResourceStore` is read again each time, so a run sees its latest value. | Off | When a run reads the same values often and need not see a change during the run |
| `Host` | Opens the runtime in [host mode](#host-mode) | Nil | Only for host mode |

A `Variable` holds its `Value`, and `Secret` marks a secret. With
`Ciphertext`, the value is [Windmill](../docs/glossary.md#windmill)
ciphertext, which windmill-lite never
decrypts: a step that reads it fails as Windmill fails a secret that it
cannot decrypt. A step can update a variable through Windmill's
[job](../docs/glossary.md#job) API,
and later runs of the runtime read the new value.

`ReadCache` is a [deviation](../docs/glossary.md#deviation): Windmill reads
a resource or variable again for each step and each call. The
[equivalence contract](../docs/design/0001-equivalence-contract.md#5-named-deviations)
names it, and the [trace](../docs/glossary.md#trace) of a run with it
lists `D21`.

### The resource store

Steps write resources of type `state` through the job API, as the stock
client's `setState` does, and the runtime keeps them in its
`ResourceStore`. Every read finds the host's and the bundle's resources
first, then the store's entries, without a cache. A step cannot change or
delete a resource that the host or the bundle provides: that call answers
501.

- With `ResourceStore` left nil, each runtime makes a memory store, which
  `Close` releases with its entries. A read that reaches the store after
  the release fails as an outage of the store; one that got its entry
  before returns it.
- `NewMemoryResourceStore` returns a memory store that the host keeps:
  its entries live as long as the host holds it, and only the runtimes
  given it see them. The trace of each flow run on a memory store lists
  `D24`, a
  [deviation](../docs/design/0001-equivalence-contract.md#5-named-deviations):
  Windmill keeps them in its database. A root script's run has no trace.
- The package [`wl/redisstore`](redisstore/README.md) is a store on
  Redis, which runtimes in several processes share and which lasts as long
  as Redis keeps its data; its runs list no `D24`. The guide
  [Choosing a resource store](../docs/resource-stores.md) compares the two
  stores and says how to set Redis up.
- A host's own store implements `ResourceStore`. Its contract is the doc
  comment in
  [internal/platform/resourcestore.go](../internal/platform/resourcestore.go),
  which `wl.ResourceStore` aliases. The package
  [`wl/storetest`](storetest/README.md) holds the contract's tests, and
  `storetest.Run` runs them on a store.
- When a store fails, the step's call answers 500, `Internal: windmill-lite:
  the resource store failed: <err>`, and the run's warnings also get one
  with the code `resource_store_failed`. So does a read of the run's own, such as a
  `$res:` argument, whose step then fails with the same error. A missing state is never what a failure
  reads as.

## Run

`Run(ctx context.Context, path string, args json.RawMessage, opts RunOptions) (*Result, error)`
runs the flow at `path`, or with `Kind: KindScript` the root script, with
`args`, a JSON object. Empty `args` mean `{}`. `Run` is safe for
concurrent use: the runs of a runtime share its harness processes, its
platform services and its expression engine.

`Run` returns an error only when the run could not start:

- the runtime is closing (`ErrClosed`);
- a run of the runtime with the same `opts.ID` is in flight
  (`ErrRunInFlight`); the id is free again once that run returns;
- the bundle has no such flow or root script;
- `args` is not a JSON object, or cannot be stored;
- a [selector](../docs/glossary.md#selector) of `opts.Hooks` is not well
  formed (`ErrSelector`) or is
  ambiguous (`ErrAmbiguousSelector`).

The context bounds the run. When it ends, the run ends with status
`canceled`, and Windmill's default reason and canceler. A context
canceled with a `*wl.Cancellation` cause (`context.WithCancelCause`) names
its own reason and canceler.

### RunOptions

| Field | What it sets | Default |
|---|---|---|
| `Kind` | `KindFlow` or `KindScript`. A root script runs as a script job. | `KindFlow` |
| `ID` | The run's job id. Steps read it as `WM_FLOW_JOB_ID`, a script as `WM_JOB_ID`. | A new UUID |
| `Seed` | Fixes the run's shuffles of the orders that Windmill leaves unspecified, and its retry jitter | A random seed |
| `Hooks` | The run's [hooks](#hooks) | None |
| `Platform` | The run's own resources, variables and secret resolver ([Per-run platform data](#per-run-platform-data)) | The runtime's |
| `PreWriteDeadline` | A deadline that applies until the run's first write is approved ([Budgets](#budgets)) | None |
| `Clock` | The clock of every sleep and retry delay: `wl.RealClock`, or a `*wl.RecordClock` that returns each wait at once | The real clock |
| `Jitter` | The source of the retry jitter. `SeededJitter(seed)` fixes it. | One seeded with `Seed` |

A script run uses only `Kind` and `ID`. The other fields apply to flow
runs.

### Result

| Field | What it holds |
|---|---|
| `ID` | The run's job id |
| `Status` | How the run ended (table below) |
| `Result` | The final result as Windmill stores it: the flow's result, or the script's result or failure value |
| `MayHaveMutated` | `true` once the run's first write was approved ([Budgets](#budgets)), in a failed run too. The approval counts even when what prepares the launch after it, such as `InvocationEnv`, stops the job. |
| `Trace` | For a flow, the [trace](../docs/glossary.md#trace): for each call site what happened and why, with timings, and the platform calls that the steps made before the run ended. Each call is a `PlatformCall`: the step's site; the request's method, path and query, without its headers or body; the route that it matched, empty when none did; and the answer's status. `nil` for a script. |
| `Warnings` | The run's warnings, such as an error that no step owns, or the work that a step left behind when it settled. Each is a `Warning` with a code and a message, and no warning changes the run's status. |
| `Latched` | For a `latched` run, the error that was latched |

| Status | Constant | Meaning |
|---|---|---|
| `success` | `StatusSuccess` | The flow or script succeeded. |
| `failure` | `StatusFailure` | The flow or script failed. `Result` holds the error value. |
| `canceled` | `StatusCanceled` | The run's context ended, or a deadline passed, or `Close` canceled it. |
| `skipped` | `StatusSkipped` | A step's early stop with `skip_if_stopped` ended the flow. |
| `latched` | `StatusLatched` | A hook, a provider, a step or the engine [latched](../docs/glossary.md#latch) an error. `Latched` holds it. |
| `halted` | `StatusHalted` | A halt hook ended the run. |

## Hooks

`RunOptions.Hooks` gives a run its [hooks](../docs/glossary.md#hook) as Go
callbacks. The [host hooks ADR](../docs/design/0008-host-hooks.md) gives
their full rules and the reasons for them.

- Every field is optional.
- A hook that returns an error [latches](../docs/glossary.md#latch) the
  run with it. A hook can also call `RunHandle.Latch`.
- A hook makes a step fail only through an `Outcome` with `Failed` set,
  never by returning an error.
- The engine can call the hooks of one run from several goroutines at
  once, for the iterations of a parallel loop.

| Field | When the engine calls it | What it can do |
|---|---|---|
| `Intercept` | When a matching site would launch its job, after `skip_if`, `mock`, its arguments and its timeout are decided | Answer for the job. Each `Interceptor` holds a `Selector`, and either a `Static` result or a `Func` that returns the `Outcome` or declines. Only the first matching interceptor is asked: when it declines, the real job runs, and no later interceptor is tried. |
| `RewriteArgs` | When a matching site launches its own job | Return the arguments, a JSON object, that the step runs with. Each `ArgRewriter` holds a `Selector` and the `Func` that returns them. Every matching rewriter runs, in order, on what the one before returned, and an error stops the chain and latches the run. When the arguments change, the trace keeps them as they were before and after. |
| `OnModuleStart`, `OnModuleEnd` | At the start and end of every module, nested ones and iterations included | Observe. `OnModuleEnd` gets the site's `StepRecord`. |
| `HaltBefore`, `HaltAfter` | Before a matching module's first attempt, or once it has its final outcome and the run would start something other than a failure module | End the run with status `halted` |
| `InvocationEnv` | Before each [invocation](../docs/glossary.md#invocation) runs | Set or delete variables of the invocation's job environment |
| `OnRunEnd` | Once the run's outcome is decided | Add warnings to the run. It cannot change the status, and must not change the trace. |
| `OnRunSettled` | Once every invocation that the run abandoned has settled, which may be never | Release the resources that step code can reach |
| `WriteCapable`, `BeforeFirstWrite` | Before the first write-capable site launches a job | Veto the write ([Budgets](#budgets)) |
| `FatalErrorNames` | When a module fails | Latch the run instead of retrying or continuing ([Budgets](#budgets)) |

A step's record (`StepRecord.Hooks`) and the trace's deviations name the
hooks that acted. They use the IDs of the
[host hooks ADR](../docs/design/0008-host-hooks.md#the-hooks): `H1` for an
interception, `H2` for a rewrite, `H7` for a fatal error name, `H8` for a
halt, `H10` for the record clock, and `provided` for a provided site.

### Selectors

A [selector](../docs/glossary.md#selector) (`wl.Selector`, the host
contract's type) names the call sites that a hook applies to.

- `{Flow, Module}` names the module with that id in any module list of the
  flow. The modules inside a loop or a branch belong to their enclosing
  flow.
- `{Script}` names every call of a script: a script step by its path, and
  an inline script by `<flow path>/<module id>`. A provided site that
  replaces such a step matches too.
- `Occurrence` picks one module when its id is repeated in one module
  list. Without it, `Run` fails with `ErrAmbiguousSelector`.
- `Iterations` or `Branches` limit a module selector to those iterations
  or branches of the innermost loop or branch around the site.

A hook gets the call site that it acts on as a `Site`. A `Site` holds the
site's typed path, a `SitePath` such as `m:a/i:2/m:b`
([site paths](../internal/conformance/README.md#site-paths)); the flow
whose module list holds the module; the module's id; and, for a leaf, its
script's path.

### Providers

A [provided site](../docs/glossary.md#provided-site) runs with its own
input transforms, `skip_if`, `mock`, `stop_after_if`, retries and
`continue_on_error`. When it launches, its provider answers in place of
the job that its target would have run.

- The host contract gives each provided site a static value or a provider
  name. `Options.Providers[name]` is a `wl.Provider`: it gets a `Call` with
  the site's stored arguments, and returns an `Outcome`, once for each
  attempt.
- A provider cannot decline. Returning `false` latches the run with
  `ErrProviderDeclined`, and the run fails closed.
- The site's record has `Ran` set, no job, and `provided` among its hooks.
- An argument computed from a secret `flow_env` entry is stored sealed, as
  an `$encrypted:` value that only the engine opens. An interceptor's or a
  rewriter's `Call` holds it sealed; a provider gets it in clear.

### Per-run platform data

`RunOptions.Platform` gives one flow run its own platform data, which a
host can choose from the run's data, for example per tenant.

- `Resources` and `Variables` replace the runtime's at the same paths, for
  this run only. A step's variable update changes the run's own variable,
  or the runtime's where the run has none.
- `Resolve`, when set, is the run's secret layer. Each resource value and
  each secret's plaintext passes through it after interpolation. A raw
  resource read does not.
- The run's argument resolution, `flow_env`, expression helpers and its
  steps' job-API reads answer from this data.
- The job registry, flow user state, object store and result cache stay
  the runtime's. The runs that a step dispatches read the runtime's data.

## Budgets

A budget limits a run. Every budget is off unless the host sets it,
except the host plugin's timeout, which is 60 s by default. The
engine checks the context and the deadlines at every module boundary,
iteration, sleep and retry delay.

| Setting | What it limits | What happens when it trips |
|---|---|---|
| The run's context (`ctx`) | The whole run: its cancellation and its hard deadline | The run ends `canceled`. |
| `RunOptions.PreWriteDeadline` | The time before the run's first write is approved | The run ends `canceled`, and its context's cause is `ErrPreWriteDeadline`. After the approval, the deadline no longer applies. |
| `Hooks.WriteCapable` and `Hooks.BeforeFirstWrite` | The first write: the first job of a write-capable site, or a write-capable subflow call | An error from `BeforeFirstWrite` vetoes the write and latches the run. Parallel launches wait for that one verdict. After an approval, `Result.MayHaveMutated` is `true`, also in a failed run. |
| `Hooks.FatalErrorNames` | The error names that skip retries, `continue_on_error`, `skip_failures` and `skip_failure` | The run latches with a `*FatalError`, and the trace lists `H7`. |
| `RunOptions.Clock` set to a `*wl.RecordClock` | The time of sleeps and retry delays | Each wait is recorded and returns at once, and the trace lists `H10`. |
| `Options.PluginTimeout` | Each call to the host plugin | The run latches with `ErrHostCallTimeout`, unless its context ended first. A late `onRunEnd` adds a `plugin_error` warning instead, and a late `onRunSettled` goes to the diagnostics. |

The host decides which sites are write-capable, and the first-write gate
covers only those. An interception, a provided site, a mock or a skipped
module launches no job, so it is never a write.

**Order.** At a module's start, the engine checks the latch, then
cancellation and deadlines, then halts. At its completion, it checks the
latch, then cancellation, then fatal error names, then Windmill's own
decision to continue. The
[host hooks ADR](../docs/design/0008-host-hooks.md#the-hooks) gives the one
exception, for an error stop that coincides with a cancellation.

## Errors

Test a sentinel error with `errors.Is`, and a typed error with
`errors.As`. Other errors of `Open` name the file or option at fault, such
as a missing `HarnessRuntime` or one of another digest.

| Error | Where | What it means | What to do |
|---|---|---|---|
| `ErrEngineVersion` | `Open` | Another engine version built the bundle. | Build the bundle with the engine of your `wl` version ([What you need](#what-you-need)). |
| `ErrUnsupported` | `Open` | A flow uses a feature that the engine does not run yet. The error names it. | Change the flow, or run it on Windmill. |
| `ErrMissingProvider` (`missing_provider`) | `Open` | The host contract names a provider that neither `Options.Providers` nor the host plugin has. | Register the provider. |
| `ErrClosed` | `Run` | `Close` has begun. | Open a new runtime. |
| `ErrRunInFlight` | `Run` | A run of the runtime with the same `RunOptions.ID` is in flight. Nothing ran. | Use another id, or wait for that run to return. |
| `ErrSelector` | `Run` | A selector does not name exactly one of a module and a script, or a script selector has `Occurrence`, `Iterations` or `Branches`. | Fix the selector. |
| `ErrAmbiguousSelector` (`ambiguous_selector`) | `Run` | A module selector names an id that a module list repeats. | Add `Occurrence`. |
| `ErrProviderDeclined` (`provider_declined`) | `Result.Latched` | A provider declined. | Make the provider answer every call. |
| `*FatalError` | `Result.Latched` | A module failed with one of `Hooks.FatalErrorNames`. It holds the site, the error's name and the failure value. | Handle the failure in the host. |
| `*HostCallError` | `Result.Latched` | A host plugin's hook threw or gave a wrong answer (code `plugin_error`), or the plugin lacks the hook (`unknown_hook`). | Fix the plugin. |
| `ErrHostCallTimeout` (`host_call_timeout`) | `Result.Latched` | A host plugin's hook did not reply within `Options.PluginTimeout`. | Make the hook faster, or raise the timeout. |
| `ErrPreWriteDeadline` | The cause of the run's context, which hooks see with `context.Cause` | The pre-write deadline passed before a first write was approved, so no write-capable site launched a job. It wraps `context.DeadlineExceeded`. | Retry only if the steps that ran are safe to repeat: steps that the host did not mark write-capable may have had effects. |
| `*HandshakeError` | `Open`, in host mode | The runtime refused the host, or the host's connection ended first. `Code` is a reject code. | See the codes below. |

A step that calls `wl.latch(error)` latches its run too, and so does a
host plugin's `around` wrapper that fails. `Result.Latched` then holds
that error.

| Reject code | Constant | Meaning |
|---|---|---|
| `workdir_not_writable` | `RejectWorkdirNotWritable` | `Host.Workdir` is not a directory that the runtime can write. |
| `workdir_mismatch` | `RejectWorkdirMismatch` | `Host.Workdir` is not the host's working directory. |
| `engine_refused` | `RejectEngine` | The runtime cannot serve the bundle at all, for example because another engine version built it or it does not verify. The message gives the reason. |

## Inspect and close a runtime

`BuildInfo()` reports what the runtime opened:

| Field | What it holds |
|---|---|
| `BundleDigest` | The digest of the bundle's manifest |
| `EngineVersion` | The runtime's engine version, which built the bundle |
| `BunVersion`, `EsbuildVersion` | The Bun that built the scripts, and the esbuild of the codebase scripts, if any |
| `HarnessRuntimes` | The digests of the harness runtimes that the bundle was built for, by harness class |
| `ModuleState` | The module state in effect |
| `SharedCode` | Whether the steps run the bundle's shared code: the bundle has it, and the steps run `warm` ([shared module code ADR](../docs/design/0013-shared-module-code.md)). A run's trace records it too (`Trace.SharedCode`). |
| `Contract` | The host contract that the bundle was built with, as JSON |
| `Deviations` | The deviations in effect: the manifest's, with those of the module state in effect |
| `NonconformantRuntime` | The bundle was built on a Bun other than 1.4.3. |

- `PlatformOrigin()` is the origin at which the steps reach the runtime's
  [platform facade](../docs/glossary.md#platform-facade). A step reads it
  as `BASE_URL`, and a native step also as `BASE_INTERNAL_URL`. A `warm`
  or host-mode process reaches it through its
  intercepted `fetch`, and a `fresh` process over loopback TCP.
- `Done()` is closed when host mode ends, and is `nil` outside host mode.
- `Close()` stops admitting runs, cancels the runs in flight with the
  reason `the runtime is closing` by `windmill-lite`, and waits for them.
  Then it stops the harness processes, the facade and the expression
  engine, and removes the runtime's temporary directories. It takes at
  most 10 s, and returns an error if a run is still in flight then.
  `Close` is idempotent and safe to call while runs start.

## Host mode

In [host mode](../docs/glossary.md#host-mode), the steps run in the
process of the host that started the engine, and that host submits the
runs. `windmill-lite host` is such an engine, and the
[Bun host SDK](../harnesses/README.md) starts it. Use `Options.Host` only
to write another such engine.

- The bundle must be a directory, which the host loads too.
- Steps run `warm`: `ModuleState` `fresh` and `Options.Plugin` fail
  `Open`. `HarnessRuntime` and `Bun` are not used.
- `Open` completes the handshake with the host before it returns. When
  `Open` fails, it refuses the host with the reason.
- `Host.Refuse` refuses the host for an engine that fails before `Open`.

| `Host` field | What it sets |
|---|---|
| `Conn` | The engine's end of the socket to the host: file descriptor 3 of `windmill-lite host` |
| `Children` | The channel on which the host announces its steps' child process groups, file descriptor 4. `nil` means that they come on `Conn`. |
| `Workdir` | The working directory that the host passed. It must be a directory that the runtime can write, and already the host's working directory. Required. |
| `SkipArgsFile` | The host's statement that its own code never reads `args.json` in `Workdir`. For a bundle none of whose scripts can read the file, the runtime then writes none. |
| `KillAfter` | The time without a frame from the host after which the runtime kills the host with `SIGKILL`. Zero means 30 s, and a negative value turns the watchdog off. |

The [Bun host SDK](../harnesses/bun/README.md#host-mode-and-the-host-sdk)
describes host mode from the host's side, and the
[harness protocol ADR](../docs/design/0004-harness-protocol.md#host-mode)
gives the protocol.

## Host plugins

A [host plugin](../docs/glossary.md#host-plugin) gives hooks and providers
in TypeScript. The
[Bun harness README](../harnesses/bun/README.md#host-plugins) describes how
to write one.

- `Options.Plugin` names the plugin's module. Every `warm` harness process
  loads it, and `Open` starts the first process to read the plugin's
  declaration. A `fresh` runtime refuses a plugin. In host mode, the
  plugin is the host's.
- The runtime checks the declaration's selectors as it checks a Go host's.
  Each provider that the host contract names must be in
  `Options.Providers` or in the plugin, not in both. Otherwise `Open`
  fails, or in host mode the host is refused with `plugin_mismatch`.
- A run's own `RunOptions.Hooks` come first, then the plugin's. This holds
  for a plugin's write-capable selectors, `beforeFirstWrite` and fatal
  error names too.
- A plugin's `around` wrappers run a step inside them, in the step's
  invocation; the Bun harness README describes them.
- A plugin's `resource` and `variable` answer after the run's own
  platform data and before the runtime's.
- A failed `onRunEnd` adds a `plugin_error` warning, and a failed
  `onRunSettled` goes to the diagnostics. Every other failed hook latches
  the run, unless the run's context ended first.
- `onRunEnd` is called for a run whose context ended too, canceled or past
  a deadline. Its call is bounded by the plugin timeout alone.
- A Go hook always gets the whole trace. A plugin's run-end hooks get the
  trace in the form that its declaration asks for.

## Garbage collection

A Go program that embeds `wl` runs the engine in its own process, so the
engine's garbage collection follows the program's setting (`GOGC`,
`debug.SetGCPercent`). A host-mode engine follows the `GOGC` of the host
that started it. At Go's default, 100, the engine collects when its heap
reaches twice what is live; at 400, five times.

`GOGC=400` against the default, measured on the embedder shapes in host
mode ([docs/benchmarks.md](../docs/benchmarks.md#the-embedder-shapes)),
as the range over the twelve workloads of each workload's median change
over paired rounds. Linux ran
[one job per architecture](https://github.com/invakid404/windmill-lite/actions/runs/41756851459)
with 5 rounds; macOS ran 3 rounds.

| Runner | p50 | Engine CPU per step | Collector CPU per step |
|---|---|---|---|
| linux/amd64 (AMD EPYC 9V45) | 0 to −15 % | −6 to −28 % | −61 to −100 % |
| linux/arm64 (Neoverse-N2) | +3 to −3 % | −1 to −14 % | −58 to −100 % |
| macOS (Apple M4) | +5 to −11 % | −5 to −25 % | −59 to −77 % |

At −100 %, no collection ran during the measured requests. The default
suits most hosts. Raise `GOGC` where the engine's CPU matters more than its
memory.

## Work on wl

This section is for contributors who change `wl`.

### Files

| File | Contents |
|---|---|
| `wl.go` | `Options`, `Resource`, `Variable`, `ResultCache`, `ResourceStore`, `MemoryResourceStore`, `NewMemoryResourceStore`, `Runtime`, `Open`, `BuildInfo`, `PlatformOrigin`, `Close`, `Version`, and `ErrUnsupported`, `ErrEngineVersion`, `ErrClosed` |
| `run.go` | `RunOptions`, `RunPlatform`, `Result`, `Run`; the clock and jitter types; `ErrPreWriteDeadline`, `ErrRunInFlight`; the trace types, which alias the engine's; the executor that collects each run's platform calls |
| `hooks.go` | `Hooks` and the types of their callbacks, `Selector`, `Site`, `FatalError` and the selector and provider errors; selector matching, and the adapter to the engine's hook interface |
| `host.go` | `Host`, `Runtime.Done`, `Host.Refuse`, `HandshakeError` and the reject codes: host mode, its handshake, and the runs that it serves |
| `plugin.go` | `DefaultPluginTimeout`, `ErrHostCallTimeout`, `HostCallError`: host plugins, their declaration as `Hooks` and `Providers`, their resources and variables, `around`, and the merge of a run's hooks with the plugin's |
| `payload.go` | The encoding of the payloads that the runtime sends to a host plugin |
| `example_test.go` | The README's example, compiled by `go test` |
| `storetest/` | The contract suite of `ResourceStore` ([its README](storetest/README.md)) |
| `redisstore/` | The resource store on Redis ([its README](redisstore/README.md)) |

### How Open works

- `Open` makes every path absolute, because harness processes run in
  directories of their own.
- It verifies a directory in place (`bundle.Verify`). It verifies and
  extracts an archive (`bundle.Extract`) into a new `<data>/bundle-*`
  directory, which `Close` removes. So a data directory that the host
  keeps opens again, however the last open ended.
- The runtime owns its platform services
  ([internal/platform](../internal/platform/README.md)). They are one
  store with the bundle's `data/` and the host's resources and variables,
  an object store in `<data>/objects`, the job registry and flow user
  state, the result cache, the facade, and the default dispatcher. The
  dispatcher runs a root that a step dispatches as a run of its own.
- Harness processes start on the first step: a pool of `warm` processes,
  or one process for each step in `fresh` mode, with working directories
  in `<data>/work`. Their handshake checks the harness's kind, Bun 1.4.3
  and the bundle's digest.
- A `warm` process reaches the facade through its intercepted `fetch`, and
  a `fresh` one over loopback TCP.
- `Open` compiles the program's expressions once.
  `Options.QuickJSOnly` turns off the expression pipeline's Go evaluator,
  `jsExpr` ([internal/expr](../internal/expr/README.md)).

### Platform calls in the trace

The runtime's executor maps each invocation to its run and site. The
facade hands over each platform call as soon as its answer is decided
(`Config.Answered`), before the step sees the answer.

- An invocation's run and site stay known until its run ends, because a
  call that the step did not await can be answered after the step
  settled.
- So a run's trace has every call that was answered before the run ended,
  also from a step that settled without awaiting it, or that the run's
  cancellation abandoned.
- A call answered after the run ended is not in the trace, and neither
  are the calls of the runs that the dispatcher starts.

### Host mode inside

`host.go` serves host mode over the
[harness protocol](../docs/design/0004-harness-protocol.md#host-mode),
with the engine's side of the protocol in
[internal/harness](../internal/harness/README.md).

- **Handshake.** The host must greet the runtime as a Bun harness that
  offers the host capability, with Bun 1.4.3 and the bundle's digest,
  from the engine's parent process. Its hello's working directory must be
  `Host.Workdir`.
- **Refusals.** When `Open` fails before the handshake, `refuseHost`
  refuses the host with `engine_refused` and the reason.
- **Runs.** Each run frame becomes a `Run` of its kind and path, with its
  id as the job id and its `intercept` entries as static interceptions.
  The run's invocations go back to the host, which runs them in the scope
  that it captured when it submitted the run.
- **Outcomes.** `run_done` carries the status, the stored result,
  `may_have_mutated`, the warnings and the trace's deviations.
  `run_settled` follows once the run has settled. A run that `Run`
  refuses goes back as `refused`, or `draining` once `Close` has begun.
- **A run's own hooks.** `hostBudgets` turns a run frame's write-capable
  selectors, halts and fatal error names into the run's own hooks, ahead
  of the plugin's, with its pre-write deadline, record clock and hard
  deadline. `checkSelectorIn` checks their selectors as a plugin's are
  checked, and a selector that fails refuses the run, naming it, such as
  `the run's haltAfter[0]`.
- **Drain and end.** A host's `drain` is answered on the harness side:
  later runs are refused `draining`, and `bye` follows once no run is in
  flight. When the host's connection ends, `Done` closes, and the runs in
  flight are lost (`harness_lost`).

### Host plugins inside

- **Hooks.** `plugin.go` turns the declaration into `Hooks` and
  `Providers`, and `mergeHooks` puts the run's own hooks first. A static
  interception and the halts stay in Go. Every other callback sends a
  `host_call` with its hook, run, site and payload, and decodes the reply.
- **Payloads.** `marshalPayload` writes a payload as `json.Marshal` does,
  map keys sorted, but splices the stored texts of arguments, results,
  carried values and late bodies in as they are. So its escapes can differ
  from `encoding/json`'s inside a stored text (`<`, `>`, `&`, U+2028,
  U+2029), and the plugin parses the same values. `TestPayloadSplice`
  holds this.
- **`around`.** `aroundAt` puts the indexes of the plugin's `around`
  wrappers whose selectors match a leaf's site on its invocation, and on a
  script run's one job too. The harness runs the step inside them.
- **Records and traces.** `recordJSON` and `traceJSON` carry every field
  of the Go `StepRecord` and `Trace`, in snake_case.
- **Settlement.** Every `host_call` of a run holds the run's settlement
  (`doHeld`, `engine.HoldSettlement`) until its reply, or until its late
  reply or the harness's exit when the runtime stopped waiting first. So
  `OnRunSettled`, `run_settled` and the release of the run's resources come
  after every hook of the run. A hook of a settled run is not called
  (`errRunSettled`).
- **Resources and variables.** The plugin's `resource` and `variable`
  reach their run through a `runBinding` that `engine.RunRequest.Started`
  fills, because the facade serves job-API calls outside the run's
  context. A failure or a timeout latches the run, unless the call ended
  with its context or the run's outcome was decided.

### Tests

Run `go test -race ./wl`. The tests that need Bun need `WL_BUN` and the
build helper's dependencies, and skip without them unless
`WL_REQUIRE_BUN` is set, as CI sets it
([Run the tests locally](../docs/ci.md#run-the-tests-locally)).

| File | What it covers | Needs Bun |
|---|---|---|
| `wl_test.go` | `Open`, `Run`, `BuildInfo` and `Close` on hand-written bundles that need no harness. It covers archives, data directories, the host's variables, every refusal of `Open` and `Run`, cancellation, `Close` during runs, and a late platform call that still reaches its run. | No |
| `hooks_test.go` | Every hook without Bun: interceptions, rewrites, observers, halts, run-end hooks, latches, selector checks, providers and their errors, per-run variables and the resolver | No |
| `hookcases_test.go` | The latch-precedence cases of `conformance/hooks`, through the public hooks | No |
| `host_test.go` | Host mode on a socketpair: the welcome, `run_done` and `run_settled`, refused runs, drain, `Close`, every refusal in the handshake, and per-run halts (`TestHostRunHalts`) | No |
| `plugin_test.go` | Host plugins without Bun: refusals, the declaration's checks, providers, `host_call`s, timeouts, `mergeHooks`, settlement (`TestPluginCallsHoldSettlement`) and the run-end trace detail (`TestPluginTraceDetail`) | No |
| `payload_test.go` | The payload encoding against `json.Marshal` (`TestPayloadSplice`) | No |
| `settled_test.go` | The run-settlement hooks | No |
| `bun_test.go` | A real bundle in `warm` and `fresh`: platform calls in the trace, cached results shared between runtimes, cancellation, `InvocationEnv`, per-run variables, the first write and the pre-write deadline, the step API (`TestStepAPI`), plugin trace parity and `around` | Yes |
| `outside_test.go` | A bundle whose host contract lists two modules outside the [sync folder](../docs/glossary.md#sync-folder), without a host | Yes |
| `plugincases_test.go` | The cases of `conformance/plugins/hooks.yaml`, through the TypeScript case plugin and through Go callbacks built from the same plugins | Yes |
| `store_test.go` | `Options.ResourceStore` on real steps (`TestResourceStoreOnBun`): two runtimes that share a memory store, one with its own, a failing store's warning, and `D24` in the trace; a run id in flight refused (`TestRunIDInFlight`). Without Bun: `Close` releasing its own store while readers use it (`TestCloseReleasesItsOwnStore`), and a late answer kept from a later run of the same id (`TestLateAnswerAcrossIDReuse`) | Yes, but for the last two |
| `example_test.go` | Compiles the README's example | No |

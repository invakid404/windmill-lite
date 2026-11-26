# Glossary

These are the terms of windmill-lite's documentation, for its own concepts
and for those of [Windmill](#windmill). Use each term only in the meaning
given here, and do not use the words on its **Not** line for it
([writing standard](writing.md#words)). Ordinary uses of the same words,
qualified so that they cannot be confused, are fine: a CI job, the
repository root, a Go module. The
[writing standard](writing.md#assumed-knowledge) lists the general
knowledge that the documentation assumes.

## Terms

### ack

[Memoria](#memoria)'s record that a person or agent reviewed a README
against the files in its scope. `memoria ack` writes it to `memoria.lock`
([docs/memoria.md](memoria.md#the-per-session-flow)).

### ADR

An architecture decision record: a numbered file in `docs/design/` that
records one decision, with its context, the alternatives and the
consequences ([docs/design](design/README.md)).

**Not:** design record.

### build helper

The Bun program that `windmill-lite build` runs to bundle a sync folder's
Bun scripts and to read their signatures: `harnesses/bun/build/main.ts`
([harnesses/bun/build](../harnesses/bun/build/README.md)). Neither a
GitHub Release nor the npm package ships it, so a build takes it from a
checkout of the engine's version.

### build team

The contributors who design, review or implement windmill-lite. They never
read Windmill's AGPL or proprietary source
([AGENTS.md](../AGENTS.md#build-team)).

### Bun

The JavaScript runtime on which Windmill runs ordinary TypeScript scripts.
windmill-lite pins Bun 1.4.3 for its builds, harnesses and hosts. The image
of Windmill's [pinned version](#pinned-version) ships Bun 1.4.4, a
difference that the
[equivalence contract](design/0001-equivalence-contract.md#5-named-deviations)
names.

### Bun host SDK

The TypeScript library with which a Bun program becomes a [host](#host):
the entry `windmill-lite/bun` of the npm package `windmill-lite`
([harnesses](../harnesses/README.md)). Its `connect()` starts the engine as
a child process, in [host mode](#host-mode). Its version is the version of
the engine that it works with.

### bundle

What `windmill-lite build` writes from a [sync folder](#sync-folder) and a
[host contract](#host-contract): the compiled [program](#program), the
bundled [scripts](#script), the sync folder's data, and a manifest that
records the hash of every file. A bundle is a directory, or the same tree
as one `.wlb` archive ([packaging ADR](design/0010-packaging.md#the-bundle)).

### conformance case

Test data that states an input and the output that windmill-lite must
produce, and names the [findings](#finding) that it checks. One file can
hold several cases, and one case several runs
([conformance/](../conformance/README.md)).

**Not:** test case.

### deviation

A deliberate difference from Windmill's behaviour, which the
[equivalence contract](design/0001-equivalence-contract.md#5-named-deviations)
lists with its reason and its conformance case. A bundle's manifest and a
run's [trace](#trace) list the deviations in effect.

### engine

The Go interpreter of [flows](#flow), in
[internal/engine](../internal/engine/README.md). It runs the flows of a
compiled [program](#program), and sends each [step](#step)'s
[script](#script) to a [harness](#harness). The `windmill-lite` command
contains it, so the `windmill-lite` binaries of a release are also called
the engine.

**Not:** runtime.

### expression

A JavaScript expression in a [flow](#flow), such as a step's input
transform, which the [engine](#engine) evaluates
([internal/expr](../internal/expr/README.md)).

### finding

A numbered rule of Windmill's behaviour in the
[semantics reference](#semantics-reference). The prefix of its ID names its
area ([finding prefixes](#finding-prefixes)).

**Not:** spec item.

### fixture

A synthetic input written for a test: a sync folder, a flow or a script.
Never copy a fixture from a real Windmill workspace
([AGENTS.md](../AGENTS.md#fixtures-and-public-hygiene)).

### flow

A Windmill workflow: [steps](#step) in sequence, with loops and branches.
It can have a failure module: a step that runs when a failed step is
neither retried nor allowed to continue. Its format is OpenFlow, whose
schema windmill-lite vendors
([internal/openflow](../internal/openflow/README.md)).

**Not:** workflow.

### `fresh`

The [module state](#module-state) that runs each
[invocation](#invocation) in a harness process of its own, as Windmill runs
each ordinary Bun [job](#job) in a new process
([module state ADR](design/0006-module-state.md#decision)).

### generation

One process of the [engine](#engine), or of a host in
[host mode](#host-mode), that a supervisor runs until it replaces it with
the next one. `wl/plugin`'s supervisor admits each generation before it
sends it calls
([host adapters ADR](design/0009-host-adapters.md#the-supervisor)).

### harness

The process that runs step [scripts](#script) for the [engine](#engine),
such as the Bun harness in [harnesses/bun](../harnesses/bun/README.md). The
engine and the harness talk over the harness protocol
([harness protocol ADR](design/0004-harness-protocol.md)). In
[host mode](#host-mode), the host's own process is the harness.

**Not:** worker.

### harness runtime

The TypeScript code that a Bun harness runs: the directory
`harnesses/bun/runtime` ([harnesses/bun](../harnesses/bun/README.md)). A
bundle's manifest records the digest of the harness runtime that the
bundle was built for, and the engine starts harness processes for the
bundle only from that runtime. The [Bun host SDK](#bun-host-sdk) includes it.

### hook

A callback with which a [host](#host) observes or changes a [run](#run):
for example, it answers for a step, rewrites a step's arguments, observes
each step, halts the run, or [latches](#latch) an error on it
([host hooks ADR](design/0008-host-hooks.md#the-hooks)). Some hooks apply
only at the call sites that their [selectors](#selector) name; others
observe every step, or the run as a whole. A Go host gives hooks as Go
callbacks ([wl](../wl/README.md#hooks)), and a [host plugin](#host-plugin)
gives them in TypeScript.

### host

A program that embeds windmill-lite and runs the flows of a
[bundle](#bundle): a Go program through the [`wl`](#wl) package, or a Bun
program through the [Bun host SDK](#bun-host-sdk)
([host adapters ADR](design/0009-host-adapters.md)).

**Not:** embedder.

### host contract

The second input of the build, next to the [sync folder](#sync-folder): a
YAML or JSON file that lists the [roots](#root), the
[provided sites](#provided-site) and the
[host-provided modules](#host-provided-module)
([IR and loader ADR](design/0002-ir-and-loader.md#host-contract-and-mandatory-host-provided-call-sites)).
Without a host contract, every flow and script of the folder is a root.

### host mode

The mode in which a Bun [host](#host) runs the [steps](#step) in its own
process, and starts `windmill-lite host` as its engine
([harnesses/bun](../harnesses/bun/README.md#host-mode-and-the-host-sdk)).
The host submits its runs to the engine, and the engine sends the host
each step to run.

### host plugin

TypeScript [hooks](#hook) and [providers](#provider) that a Bun harness
runs for the engine, which calls them over the harness protocol
([harnesses/bun](../harnesses/bun/README.md#host-plugins)). In host mode,
the plugin is the `plugin` option of the host's `connect()`. Otherwise,
`wl`'s `Options.Plugin` names its module, which every `warm` harness
process loads. A bundle may also carry its own, which the host contract's
`plugin` field names and both load when neither option gives a plugin.

### host-provided module

A module, listed in the [host contract](#host-contract), of which the
[host](#host) and every [script](#script) in a process share one instance.
A step can read a scope that the host entered, such as an
`AsyncLocalStorage` store, only through such a module. The build resolves
each import of it to a [shim](#shim)
([packaging ADR](design/0010-packaging.md#host-provided-modules)).

### invocation

One call of one [step](#step)'s [script](#script) in a [harness](#harness)
process, from its arguments to its outcome. A [run](#run) of a flow has an
invocation each time that it runs a script step
([harness protocol ADR](design/0004-harness-protocol.md)).

**Not:** execution, job.

### job

Windmill's unit of queued work: Windmill runs each step as a job.
windmill-lite has no job queue, but it gives each step the job identity that
scripts read, such as `WM_JOB_ID`
([internal/platform](../internal/platform/README.md)).

### latch

To latch an error on a [run](#run) is to stop the run with it: the run
starts nothing more, and ends with status `latched` and the first error
latched ([host hooks ADR](design/0008-host-hooks.md#the-hooks)). A
[hook](#hook), a [provider](#provider), a step or the engine can latch an
error. The error never becomes a step's result.

### late shim

A variant of a [shim](#shim) that reads the registry each time one of its
values is read, and each time an `import()` resolves to it. A host-provided
module's bundled copy imports another module through it when it imports
that module only lazily and registers first. An import or a read before
the other module registers fails, and one after it works
([packaging ADR](design/0010-packaging.md#host-provided-modules)).

### Memoria

The tool that ties each README to the files in its scope, and fails CI while
a README has a change that nobody reviewed ([docs/memoria.md](memoria.md)).

### module state

Whether the values that a script's top-level code creates survive between
its [invocations](#invocation): [`warm`](#warm), the default, or
[`fresh`](#fresh) ([module state ADR](design/0006-module-state.md)). The
build records it in the bundle, and a host can override it when it opens
the bundle.

### oracle

An unmodified image of Windmill Community Edition at the
[pinned version](#pinned-version), which public CI runs to record the
behaviour that windmill-lite must match
([differential testing ADR](design/0011-differential-testing.md)).

### pinned run

A [run](#run) that keeps one `warm` harness process: every invocation and
host-plugin call of the run goes to the process that served its first, so
a [host plugin](#host-plugin) can keep state for the run there. It is an
option of the warm pool, for a runtime or for one run
([harness protocol ADR](design/0004-harness-protocol.md#pooling-processes-the-engine-spawns)).

**Not:** sticky run.

### pinned version

Windmill v1.862.0: the release that the
[semantics reference](#semantics-reference) describes and the
[oracle](#oracle) runs ([docs/semantics](semantics/README.md#pinned-version)).
A finding not yet re-checked at it ends its tag line with
`verified at v1.767.0 only`, after the previous pin.

### platform facade

The Go router that answers the platform calls of a step's stock
`windmill-client`: resources, variables, job reads and the other routes
that windmill-lite supports
([internal/platform/facade](../internal/platform/facade/README.md)). Every
other route of Windmill's API answers 501.

### process scope

A runner for the caller's async context without its
[invocation](#invocation): work started through it belongs to no
invocation, so the [settle-time fence](#settle-time-fence) never clears it
([harnesses/bun](../harnesses/bun/README.md#the-per-invocation-context)).
A pooled client or a [host-provided module](#host-provided-module) captures
it when it loads, with
`globalThis[Symbol.for("windmill-lite.processScope")].capture()`, and runs
its factories through it.

### program

The output of the compiler, which turns a [sync folder](#sync-folder) and a
[host contract](#host-contract) into the validated form that the
[engine](#engine) runs ([internal/ir](../internal/ir/README.md)).

### provided site

A `script`, `flow` or `rawscript` [step](#step) that the
[host contract](#host-contract) names. The [host](#host) replaces its target
with a static value, or with a [provider](#provider) that answers for the
step at run time
([IR and loader ADR](design/0002-ir-and-loader.md#host-contract-and-mandatory-host-provided-call-sites)).

### provider

Host code that answers for a [provided site](#provided-site) at run time,
in place of the job that the site's target would have run
([host hooks ADR](design/0008-host-hooks.md#mandatory-host-provided-call-sites)).
The host contract names each provider. A Go host registers it in `wl`'s
`Options.Providers`, and a [host plugin](#host-plugin) in its `providers`.

### recording

What the [oracle](#oracle) observed for one fixture or case, committed with
its provenance: the oracle's image and configuration, and the CI run that
recorded it ([internal/oracle](../internal/oracle/README.md)).

### resource store

Where a runtime keeps the resources that steps write through the job API,
such as the [state](#state) of the stock client's `setState`. Every read finds them
after the host's and the bundle's resources. A Go host sets it with `wl`'s
`Options.ResourceStore`; the default keeps them in memory, a
[deviation](#deviation) ([wl](../wl/README.md#the-resource-store)), and
the Redis store keeps them in Redis
([Choosing a resource store](resource-stores.md)).

### root

A flow or script that the [host contract](#host-contract) lists: one that
the host runs, or one that a step dispatches by path
([IR and loader ADR](design/0002-ir-and-loader.md#host-contract-and-mandatory-host-provided-call-sites)).

**Not:** entrypoint.

### run

The work of running a flow or a [root](#root) script once, from its
arguments to its result. `wl`'s `Run` returns its status, its result, and
for a flow its [trace](#trace) ([wl](../wl/README.md)).

**Not:** job.

### run-end answer

The answer, any JSON value, that a [host plugin](#host-plugin)'s `onRunEnd`
gives a flow [run](#run), however the run ended. The run's result carries
it: `wl`'s `Result.Answer`, the `Runner` service's `RunResult.answer` and
host mode's `run_done`
([harnesses/bun](../harnesses/bun/README.md#the-run-end-answer)).

**Not:** result, outcome.

### script

Code in one file that windmill-lite runs: a script by path in the
[sync folder](#sync-folder), or an inline script in a [flow](#flow)
([internal/syncfolder](../internal/syncfolder/README.md)).

### selector

A rule that names call sites: a module of a flow by its id, or every call
of a script by its path
([host hooks ADR](design/0008-host-hooks.md#selectors)). [Hooks](#hook)
and the [host contract](#host-contract) use selectors. A module selector
needs an occurrence when its id is repeated in one module list; otherwise
it is refused as `ambiguous_selector`.

### semantics reference

[docs/semantics/](semantics/README.md): Windmill's behaviour at the
[pinned version](#pinned-version), written as numbered
[findings](#finding). "The reference" is its short form.

**Not:** spec.

### semantics team

The contributors who write and validate the
[semantics reference](#semantics-reference). They can read Windmill's AGPL
source at the pinned version ([AGENTS.md](../AGENTS.md#semantics-team)).

**Not:** spec team.

### settle

An [invocation](#invocation) settles when no code of its entrypoint call can
run any more: the call ended, its process ended, or it never started. In
[`warm`](#warm) and [host mode](#host-mode), the
[settle-time fence](#settle-time-fence) then acts on the work that the
invocation left behind.

### settle-time fence

What the harness does to an [invocation](#invocation)'s remaining work when
the invocation [settles](#settle) in a shared process: it clears the
invocation's timers, aborts its `fetch` requests, and drops its later logs
and platform calls
([harnesses/bun](../harnesses/bun/README.md#the-per-invocation-context)).
Sockets that the script opened and CPU work run on, a difference from
Windmill that the
[equivalence contract](design/0001-equivalence-contract.md#5-named-deviations)
names.

### shim

The module that each bundler resolves imports of a
[host-provided module](#host-provided-module) to. It takes every export of
the module, by name, from the process-wide registry that the [host](#host)
or the [harness](#harness) fills, so every script shares one instance
([packaging ADR](design/0010-packaging.md#host-provided-modules)).

### state

A resource of type `state` that a step keeps between runs through the job
API, as the stock client's `setState` and `getState` do. A runtime keeps it
in its [resource store](#resource-store)
([Choosing a resource store](resource-stores.md)).

**Not:** module state, flow user state.

### step

One entry in a [flow](#flow), such as a script, a subflow (a flow that the
step runs), a loop or a branch. OpenFlow calls it a module: say "step",
except in Windmill's own names such as "failure module"
([internal/openflow](../internal/openflow/README.md)).

**Not:** module.

### sync folder

The folder that `wmill sync`, the sync command of Windmill's command-line
tool, writes, with scripts, flows, resources and variables. It is the first
input of the build ([internal/syncfolder](../internal/syncfolder/README.md)).

### trace

The record of what the steps of a flow [run](#run) did: for each call site,
its status, its arguments, its result and its timings, and the platform
calls that the steps made ([wl](../wl/README.md#run)). `wl`'s `Run`
returns it, and the run-end [hooks](#hook) get it.

### `warm`

The default [module state](#module-state), in which one harness process
runs many [invocations](#invocation), and the values that a script's
top-level code creates survive between its invocations
([module state ADR](design/0006-module-state.md#decision)). Native steps,
`nativets` and `//native` scripts, are the exception: each of their
invocations runs in a realm of its own, as Windmill gives each native job a
new isolate.

### Windmill

The workflow platform whose flows and scripts windmill-lite runs, at the
[pinned version](#pinned-version) ([windmill.dev](https://www.windmill.dev/docs)).
Its Community Edition (CE) is in scope; behaviour that only its Enterprise
Edition (EE) shows is not ([AGENTS.md](../AGENTS.md#both-teams)).

### `wl`

The public Go package that opens a [bundle](#bundle) and runs its flows and
root scripts: `github.com/invakid404/windmill-lite/wl`
([wl](../wl/README.md)).

## Finding prefixes

| Prefix | Area | File |
|---|---|---|
| `OF`, `PA`, `SY` | OpenFlow decoding and sync folders | [openflow.md](semantics/openflow.md) |
| `FX` | Flow execution | [execution.md](semantics/execution.md) |
| `CF` | Control flow: loops and branches | [control-flow.md](semantics/control-flow.md) |
| `JE` | Expressions | [expressions.md](semantics/expressions.md) |
| `WR` | How a script runs, by language | [languages.md](semantics/languages.md) |
| `WA` | The client library that a script calls | [client-api.md](semantics/client-api.md) |
| `OR` | The oracle | [oracle.md](semantics/oracle.md) |
| `LIC` | Licensing | [licensing.md](semantics/licensing.md) |

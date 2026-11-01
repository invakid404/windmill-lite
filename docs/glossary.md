# Glossary

These are the terms that windmill-lite's documentation uses for its own
concepts and for those of [Windmill](#windmill). When you write about one of these concepts,
use its term in the meaning given here, and do not use the words on its
**Not** line for it. The [writing standard](writing.md#words) gives the
reason.

Ordinary uses of the same words, qualified so that they cannot be confused,
are fine: a CI job, the repository root, a Go module. The
[writing standard](writing.md#assumed-knowledge) lists the general
technical knowledge that the documentation assumes.

## Terms

### ack

Memoria's record that a person or agent reviewed a README against the files
in its scope. `memoria ack` writes it to `memoria.lock`
([Memoria](#memoria), [docs/memoria.md](memoria.md#the-per-session-flow)).

### ADR

An architecture decision record: a numbered file in `docs/design/` that
records one decision, its context, the alternatives and the consequences
([docs/design](design/README.md)).

**Not:** design record.

### build team

The contributors who design, review or implement windmill-lite. They never
read Windmill's AGPL or proprietary source
([AGENTS.md](../AGENTS.md#build-team)).

### Bun

The JavaScript runtime on which Windmill runs ordinary TypeScript scripts.
windmill-lite pins Bun 1.3.10, the version that the
[pinned version](#pinned-version) of Windmill ships
([WR-3](semantics/languages.md#wr-3)).

### bundle

What `windmill-lite build` writes from a [sync folder](#sync-folder) and a
[host contract](#host-contract): the compiled [program](#program), the
bundled [scripts](#script), the sync folder's data, and a manifest that
records the hash of every file. A `.wlb` file is its archive form
([ADR 0010](design/0010-packaging.md)).

### conformance case

Test data that states an input and the output that windmill-lite must
produce, and names the [findings](#finding) that it checks. One file can
hold several cases, and one case several runs
([conformance/](../conformance/README.md)).

**Not:** test case.

### deviation

A deliberate difference from Windmill's behaviour, listed with its reason
and its conformance case in
[ADR 0001](design/0001-equivalence-contract.md#5-named-deviations).

### engine

The Go interpreter of [flows](#flow), in
[internal/engine](../internal/engine/README.md). It runs the flows of a
compiled [program](#program), and sends each
[step](#step)'s [script](#script) to a [harness](#harness).

**Not:** runtime.

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

A Windmill workflow: [steps](#step) in sequence, with loops, branches, and an
optional failure module, a step that runs when a failed step is neither
retried nor allowed to continue ([FX-2](semantics/execution.md#fx-2)). Its
format is OpenFlow, whose schema windmill-lite vendors
([internal/openflow](../internal/openflow/README.md)).

**Not:** workflow.

### expression

A JavaScript expression in a [flow](#flow), such as a step's input
transform, which the [engine](#engine) evaluates
([internal/expr](../internal/expr/README.md)).

### `fresh`

The mode that runs each [invocation](#invocation) in a harness process of
its own, as Windmill runs each ordinary Bun [job](#job) in a new process
([WR-1](semantics/languages.md#wr-1), [ADR 0006](design/0006-module-state.md#decision)).

### harness

The process that runs step [scripts](#script) for the [engine](#engine),
such as the Bun harness in [harnesses/bun](../harnesses/bun/README.md). The
engine and the harness talk over the harness protocol
([ADR 0004](design/0004-harness-protocol.md)).

**Not:** worker.

### host

A program that embeds windmill-lite and runs the flows of a
[bundle](#bundle), through the [`wl`](#wl) package or through the host
software development kit (SDK), a TypeScript library for Bun hosts ([ADR 0009](design/0009-host-adapters.md)).

**Not:** embedder.

### host contract

The second input of the build, next to the [sync folder](#sync-folder). It
lists the [roots](#root), the [provided sites](#provided-site), and the
host-provided modules, of which the host and every script share one
instance ([ADR 0002](design/0002-ir-and-loader.md#host-contract-and-mandatory-host-provided-call-sites)).

### host mode

The mode in which a Bun [host](#host) runs the [steps](#step) in its own
process, and starts `windmill-lite host` as its engine
([ADR 0009](design/0009-host-adapters.md)).

### invocation

One call of one [step](#step)'s [script](#script) in a [harness](#harness)
process, from its arguments to its outcome. A [run](#run) of a flow has an
invocation each time that it runs a script step
([ADR 0004](design/0004-harness-protocol.md)).

**Not:** execution, job.

### job

Windmill's unit of queued work: Windmill runs each step as a job.
windmill-lite has no job queue, but it gives each step the job identity that
scripts read, such as `WM_JOB_ID` ([internal/platform](../internal/platform/README.md)).

### late shim

A variant of a [shim](#shim) that reads the registry each time one of its
values is read, and each time an `import()` resolves to it. A host-provided
module's bundled copy imports another module through it when it imports
that module only lazily and registers first. An import or a read before
the other module registers fails, and one after it works
([ADR 0010](design/0010-packaging.md#host-provided-modules)).

### Memoria

The tool that ties each README to the files in its scope, and fails CI while
a README has a change that nobody reviewed ([docs/memoria.md](memoria.md)).

### oracle

An unmodified image of Windmill Community Edition at the
[pinned version](#pinned-version), which public CI runs to record the
behaviour that windmill-lite must match
([ADR 0011](design/0011-differential-testing.md)).

### pinned version

Windmill v1.767.0: the release that the
[semantics reference](#semantics-reference) describes and the
[oracle](#oracle) runs ([docs/semantics](semantics/README.md#pinned-version)).

### program

The output of the compiler, which turns a [sync folder](#sync-folder) and a
[host contract](#host-contract) into the validated form that the
[engine](#engine) runs ([internal/ir](../internal/ir/README.md)).

### provided site

A `script`, `flow` or `rawscript` [step](#step) that the
[host contract](#host-contract) names. The [host](#host) replaces its target
with a static value, or with a provider: code that it registers at run time
to answer for the step
([ADR 0002](design/0002-ir-and-loader.md#host-contract-and-mandatory-host-provided-call-sites)).

### recording

What the [oracle](#oracle) observed for one fixture or case, committed with
its provenance: the oracle's image and configuration, and the CI run that
recorded it ([internal/oracle](../internal/oracle/README.md)).

### root

A flow or script that the [host contract](#host-contract) lists: one that
the host runs, or one that a step dispatches by path
([ADR 0002](design/0002-ir-and-loader.md#host-contract-and-mandatory-host-provided-call-sites)).

**Not:** entrypoint.

### run

The work of running a flow or a [root](#root) script once, from its
arguments to its result. `wl`'s `Run` returns its status, its result, and
its trace, the record of what its steps did ([wl](../wl/README.md)).

**Not:** job.

### script

Code in one file that windmill-lite runs: a script by path in the
[sync folder](#sync-folder), or an inline script in a [flow](#flow)
([internal/syncfolder](../internal/syncfolder/README.md)).

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
[`warm`](#warm) and [host mode](#host-mode), the settle-time fence then
clears its timers, aborts its `fetch` requests, and drops its later logs and
platform calls, but sockets that the script opened and CPU work run on
([D9](design/0001-equivalence-contract.md#5-named-deviations),
[ADR 0004](design/0004-harness-protocol.md)).

### shim

The module that each bundler resolves imports of a host-provided module
to. It takes every export of the module, by name, from the process-wide
registry that the [host](#host) or the [harness](#harness) fills, so every
script shares one instance
([ADR 0010](design/0010-packaging.md#host-provided-modules)).

### step

One entry in a [flow](#flow), such as a script, a subflow (a flow that the
step runs), a loop or a branch. OpenFlow calls it a module: say "step",
except in Windmill's own names such as "failure module"
([internal/openflow](../internal/openflow/README.md)).

**Not:** module.

### sync folder

The folder that `wmill sync`, the sync command of Windmill's command-line
tool, writes, with scripts, flows, resources and variables. It is the first input of the build
([internal/syncfolder](../internal/syncfolder/README.md)).

### `warm`

The default mode, in which one harness process runs many
[invocations](#invocation), and the values that a script's top-level code
creates survive between its invocations. Native steps, `nativets` and
`//native` scripts, are the exception: each of their invocations runs in a
realm of its own, as Windmill gives each native job a new isolate
([WR-41](semantics/languages.md#wr-41), [ADR 0006](design/0006-module-state.md#decision)).

### Windmill

The workflow platform whose flows and scripts windmill-lite runs, at the
[pinned version](#pinned-version) ([windmill.dev](https://www.windmill.dev/docs)).
Its Community Edition (CE) is in scope; behaviour that only its Enterprise
Edition (EE) shows is not ([AGENTS.md](../AGENTS.md#both-teams)).

### `wl`

The public Go package that opens a [bundle](#bundle) and runs its flows and
root scripts ([wl](../wl/README.md)).

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

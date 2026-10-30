# Glossary

These are the terms that windmill-lite's documentation uses, each with one
meaning. Use each term only in the meaning given here, and do not use the
words in the "Not" column for it. The [writing standard](writing.md#words)
gives the reason.

## Terms

| Term | Meaning | Not |
|---|---|---|
| ack | Memoria's record that a person or agent reviewed a README against its current inputs. `memoria ack` writes it to `memoria.lock` ([docs/memoria.md](memoria.md)). | |
| ADR | An architecture decision record: a numbered file in [docs/design/](design/README.md) that records one decision. | design record, decision record |
| build team | The contributors who design, review or implement windmill-lite. They never read Windmill's AGPL or proprietary source ([AGENTS.md](../AGENTS.md#clean-room)). | |
| bundle | What `windmill-lite build` writes: the compiled program, the bundled scripts, the sync folder's data, and a manifest with the digest of every file. A `.wlb` file is its archive form ([ADR 0010](design/0010-packaging.md)). | |
| conformance case | A data file that states one input and its expected output, and cites the findings that it checks. When Go and a harness both need a rule, both run the same case. Most cases are in [conformance/](../conformance/README.md). | test case |
| deviation | A deliberate difference from Windmill's behaviour, listed and numbered D1, D2 and so on in [ADR 0001](design/0001-equivalence-contract.md). | |
| engine | The Go flow interpreter in [internal/engine](../internal/engine/README.md). It runs the flows of a compiled program and sends each step's script to a harness. | runtime |
| finding | A numbered rule of Windmill's behaviour in the semantics reference, such as `WR-2`. Its prefix names its area ([Finding prefixes](#finding-prefixes)). | spec item |
| fixture | A synthetic input written for a test: a sync folder, a flow or a script. Never copy a fixture from a real Windmill workspace ([AGENTS.md](../AGENTS.md#fixtures-and-public-hygiene)). | |
| flow | A Windmill workflow, defined in OpenFlow: steps in sequence, with loops, branches and a failure module. | workflow |
| `fresh` | The mode that runs each invocation in a harness process of its own, as Windmill does ([ADR 0006](design/0006-module-state.md)). | |
| harness | The process that runs step scripts for the engine, such as the Bun harness in [harnesses/bun](../harnesses/bun/README.md). The engine and the harness talk over the harness protocol ([ADR 0004](design/0004-harness-protocol.md)). | worker |
| host | A program that embeds windmill-lite and runs the flows of a bundle, through the [wl](../wl/README.md) package or the Bun host SDK ([ADR 0009](design/0009-host-adapters.md)). | embedder |
| host contract | The second input of the build, next to the sync folder. It lists the roots, the provided sites and the host-provided modules ([ADR 0002](design/0002-ir-and-loader.md)). | |
| host mode | The mode in which a Bun host runs the steps in its own process, and starts `windmill-lite host` as its engine ([ADR 0009](design/0009-host-adapters.md)). | |
| invocation | One call of one step's script in a harness process, from its arguments to its result. A run of a flow has an invocation each time that it runs a script step. | execution, job |
| job | Windmill's unit of queued work: Windmill runs each step as a job. windmill-lite has no job queue. It gives each step the job identity that scripts read, such as `WM_JOB_ID`. Use "job" only for Windmill's jobs and for this identity. | |
| oracle | An unmodified Windmill Community Edition image at the pinned version, which public CI runs to record the behaviour that windmill-lite must match ([ADR 0011](design/0011-differential-testing.md)). | |
| pinned version | Windmill `v1.767.0`: the release that the semantics reference describes and the oracle runs. | |
| program | The output of the compiler: a sync folder and a host contract, compiled and validated into the form that the engine runs ([internal/ir](../internal/ir/README.md)). | |
| provided site | A `script`, `flow` or `rawscript` step that the host contract names. The host replaces its target with a static value or with a provider that it registers ([ADR 0002](design/0002-ir-and-loader.md)). | |
| recording | What the oracle observed for one fixture or case, committed with its provenance. | |
| root | A flow or script that the host contract lists: one that the host runs, or one that a step dispatches by path. | entrypoint |
| run | The work of running a flow or a root script once, from its arguments to its result. `wl`'s `Run` returns its status, result and trace. | job |
| script | Code in one file that windmill-lite runs: a script by path in the sync folder, or an inline script in a flow. | |
| semantics reference | [docs/semantics/](semantics/README.md): Windmill's behaviour at the pinned version, written as numbered findings. "The reference" is its short form. | spec |
| semantics team | The contributors who write and validate the semantics reference. They can read Windmill's AGPL source at the pinned version ([AGENTS.md](../AGENTS.md#clean-room)). | spec team |
| settle | An invocation settles when its script returns a result or throws an error. In `warm` and host mode, the settle-time fence then revokes the timers, requests and output that the invocation left running ([ADR 0001](design/0001-equivalence-contract.md), D9). | |
| step | One entry in a flow, such as a script, a subflow, a loop or a branch. OpenFlow calls it a module. Say "step", except in Windmill's own names such as "failure module". | module |
| sync folder | The folder that `wmill sync` writes, with scripts, flows, resources and variables. It is the first input of the build ([internal/syncfolder](../internal/syncfolder/README.md)). | |
| `warm` | The default mode. One harness process runs many invocations, and the module state of each script survives between its invocations ([ADR 0006](design/0006-module-state.md)). | |
| `wl` | The public Go package that opens a bundle and runs its flows and root scripts ([wl](../wl/README.md)). | |

## Finding prefixes

| Prefix | Area | File |
|---|---|---|
| `OF`, `PA`, `SY` | OpenFlow decoding and sync folders | [openflow.md](semantics/openflow.md) |
| `FX` | Flow execution | [execution.md](semantics/execution.md) |
| `CF` | Control flow: loops and branches | [control-flow.md](semantics/control-flow.md) |
| `JE` | Expressions | [expressions.md](semantics/expressions.md) |
| `WR` | How a script runs, by language | [languages.md](semantics/languages.md) |
| `WA` | The `wmill` client API | [client-api.md](semantics/client-api.md) |
| `OR` | The oracle | [oracle.md](semantics/oracle.md) |
| `LIC` | Licensing | [licensing.md](semantics/licensing.md) |

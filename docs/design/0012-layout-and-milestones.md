# Go package layout and implementation milestones

**Status:** Accepted · **Topic:** where code goes, and the sequence of
pull-request-sized milestones to the first release, with what can run in
parallel.

The public Go packages are `wl/`, `wl/storetest`, `wl/redisstore` and
`wl/plugin`; the commands live under `cmd/`, the other Go packages
under `internal/`, and the harnesses and shared conformance cases under
`harnesses/` and `conformance/`. The work is split into milestones, each one
reviewed pull request, ordered by the interfaces and exit gates that each
one needs.

**Not built yet:** the packages of the milestones that have no code yet:
`internal/gen` (typed clients), `internal/pack` (the single executable and
the image) and `internal/platform/remote` (hybrid dispatch). In
`internal/host`, which serves the go-plugin adapter's `Runner` service,
the plain gRPC and HTTP adapters.

## Context

[AGENTS.md](../../AGENTS.md) holds back new packages until the design decides
the layout. The design records from the
[equivalence contract](0001-equivalence-contract.md) to
[differential testing](0011-differential-testing.md) fix the components;
this record places them and orders the work. Each milestone is one reviewed pull request: one
component or one slice of one, its README (the documentation gate requires
one per module directory), its tests, and the conformance cases it can
already pass.

## Decision

### Layout

```text
cmd/windmill-lite/          the CLI: build, run, gen, pack, image, verify, selfcheck, serve
cmd/wltest/                 conformance and oracle runner: check, record, diff, stub
wl/                         the public Go library API (0009); the only exported engine package
wl/plugin/                  go-plugin client and server helpers for hosts (0009)
wl/storetest/               the contract suite of wl.ResourceStore, which every resource store runs (0007)
wl/redisstore/              the resource store on Redis, over go-redis (0007)
internal/jsonval/           JSON text values, jsonb canonicaliser, NUL checks (0005)
internal/yamlx/             YAML 1.2 core scalar resolution over a node parser (0002)
internal/openflow/          OpenFlow types, effective decoder, vendored schema, coverage test (0002)
internal/syncfolder/        folder walk, wmill.yaml, filters, paths, inline scripts, locks, data files (0002)
internal/ir/                IR types, compiler, validation (0002)
internal/expr/              expression pipeline: shortcuts, rewrites, binding, serialisation (0003)
internal/expr/quickjs/      vendored quickjs-ng source, the native (cgo) backend, the pool (0003)
internal/engine/            the flow interpreter: run state, decision list, containers, waits, hooks (0003, 0008)
internal/harness/           the Go side of the protocol: frames, processes, pools (0004)
internal/platform/          job identity, service interfaces, local defaults, interpolation walker (0007)
internal/platform/facade/   the HTTP router behind the client and the fetch interception (0007)
internal/platform/remote/   hybrid dispatch to a real Windmill; waits for a human decision (0007)
internal/bundle/            bundle format, build orchestration, manifest, identity, verify (0010)
internal/pack/              single executable and OCI image (0010)
internal/host/              the Runner service over wl, and the plain gRPC and HTTP adapters (0009)
internal/wlext/             what wl lends internal/host beyond its public API (0009)
internal/gen/               typed client generation (0009)
internal/conformance/       fixture loading, observation, comparison, identity encoding (0003, 0011)
internal/oracle/            the oracle harness (0011)
internal/bench/             what the benchmarks share: payloads, percentiles, concurrent workers (0005)
internal/racebuild/         whether the binary was built with -race, and the factor for race-sensitive time limits
harnesses/                  the npm package windmill-lite (0009): package.json, one subpath per runtime
                            and the Bun SDK's test entry; npm/ (its build and packaging test)
harnesses/bun/              TypeScript: runtime/ (step runner), build/ (bundling, signatures),
                            host/ (Bun host SDK and its test entry), test/ (developer checks against golden frames)
proto/                      generate.sh, which generates runner.proto's Go code into wl/plugin (0009)
proto/windmill_lite/v1/     runner.proto (0009)
conformance/                flows/, steps/, exprs/, openflow/, jsonb/, syncfolder/, ir/, protocol/, build/: the shared cases (0003)
```

Everything except `wl/`, `wl/plugin/`, `wl/storetest/` and `wl/redisstore/` is internal, so the engine can change
without breaking hosts. The Bun host SDK is published to npm as the package
`windmill-lite`, whose root is `harnesses/`
([host adapters](0009-host-adapters.md#the-npm-package)); a host can also vendor it
from this repository.

### Milestones

**Terms.** A milestone **can start** once the interfaces it codes against
are frozen: the contracts of K1, or the merged interface of a milestone it
depends on. It **can merge** only when every milestone in its "Needs merged"
column has merged and its exit gate passes. The **wave** of a milestone is
one more than the highest wave among the milestones it needs merged (K1 is
wave 0); it is the earliest wave in which it can merge, and milestones in one
wave proceed in parallel. Coding can begin a wave earlier against frozen
interfaces. A milestone's "Needs merged" lists everything its exit gate
needs, not only what its code imports, so an exit gate never depends on a
later milestone. **External gates** are inputs from outside the repository's
code: S1 is a request to the semantics team listed in the [README](README.md),
and H-b is the human decision on hybrid dispatch. **MVP** marks what the first
host integration needs. Engine milestones are numbered N and platform
milestones PF, so that no milestone id looks like a deviation id of
[0001](0001-equivalence-contract.md).

| Id | Milestone | Needs merged | Exit gate (beyond its own tests and cases) | Wave | MVP |
|---|---|---|---|---|---|
| K1 | **Contracts**: the `jsonval` value type, the expression IR and evaluator interface, the step-executor interface, the internal hook interface with no-op defaults, precedence and the `latched`/`halted` statuses, run-trace and observation types, and the site-path encoder | — | the encoder's negative tests ([0011](0011-differential-testing.md)) | 0 | yes |
| A1 | `internal/jsonval`: producer and stored texts, canonicaliser, NUL checks, the test implementation | K1 | FX-42, JE-44 and WR-10 cases | 1 | yes |
| A2 | `internal/yamlx`: the YAML dialect of SY-9 | — | SY-9 fixtures | 1 | yes |
| A3 | `internal/openflow`: types, effective decoder, error prefixes and server checks, the vendored schema with its notice, the coverage test | — | OF cases | 1 | yes |
| B1a | `internal/expr/quickjs`: vendored quickjs-ng `be664ca1` with notices, the native backend, the pool, limits, bytecode cache, interrupts, the pinned stack size | K1 | the JE-1 to JE-3 limits at the engine level (missing globals, the 20 s attempt, the 128 MiB cap; their message texts are B2b's); an interrupt test; static Linux binaries built with `zig cc`; the C builds under `-race` on both CI runners; the native pool under load measured | 1 | yes |
| C1 | `internal/harness` and `harnesses/bun/runtime` skeleton: frames (with `part`), handshake, supervision, pools, an echo step, golden frames | K1 | protocol golden tests on Bun 1.3.10 | 1 | yes |
| E2 | `internal/oracle` and `wltest record`: compose with pinned digests, bootstrap, CLI push, deployment gate, capture, walker, normaliser; the oracle workflow; image lint; manifest guard | K1 | one recorded smoke fixture in CI | 1 | yes |
| W1 | **Walking skeleton** (scaffolding that N1a, B2b, C3a and F3a replace; its review bar is that it retires the gates, not that its code survives): one sequential flow, one Bun step, a static and a JavaScript transform, a step timeout, the real protocol, host mode through a minimal SDK with abandonment counted in health and the engine watchdog, one H1 intercept, one H11 read through one host-provided module whose shim is written by hand | K1, A3, B1a, C1 | on Bun 1.3.10: the Q-WA-4 env-view test 16/16; the host-provided-module ALS test 16/16 in a compiled host; one detached rejection does not end the host; the never-settling-step test and the watchdog test of [0004](0004-harness-protocol.md); a first per-step latency figure | 2 | yes |
| E3a | Recording batch **JE**: the core JE catalogue cases and the contested CF-19/CF-V6 cases, as context-free expression cases or flow fixtures | E2 | recordings committed with provenance; [0001](0001-equivalence-contract.md) §6 updated | 2 | yes |
| E3b | Recording batch **FX core**: FX-6 to FX-10 and FX-16 to FX-21, with the contested FX-10 route cases that need no handler and FX-42/h | E2 | as E3a | 2 | yes |
| E3c | Recording batch **FX errors**: FX-12 to FX-15 and FX-30 to FX-38 (the executor parts only of FX-36 and FX-37; their run-and-wait HTTP halves are E3e's), with the contested FX-33 and FX-V6/a cases | E2 | as E3a | 2 | yes |
| E3d | Recording batch **CF**: CF-2 to CF-15, with the contested CF-12 and FX-40 cases | E2 | as E3a | 2 | yes |
| A4 | `internal/syncfolder`: SY-1 to SY-15, SY-V1 to SY-V4 | A2, A3 | SY cases | 2 | yes |
| C2a | `harnesses/bun/build` and the shared build transforms: bundling per WR-26 to WR-29, the export list and signatures, annotations, workflow-as-code detection, the export scan, the shim generator and the define table (one implementation for both bundlers, [0010](0010-packaging.md)) | C1 | parser and build golden tests for WR-6 to WR-8, WR-31 and WR-32; the `host_module_live_binding` refusal; generated shim and define golden tests | 2 | yes |
| E4a | `wltest stub` and the replay service, on the oracle | E2 | the stub acceptance cases of [0011](0011-differential-testing.md), on the oracle | 2 | yes |
| A5 | `internal/ir`: normalisation to the deployed form, the IR, collapse, harness classes, the host contract and provided sites, duplicate-id occurrences, validation and policy errors | K1, A3, A4 | IR golden tests; the compile-time half of the provided-site policy case of [0002](0002-ir-and-loader.md) | 3 | yes |
| B2a | `internal/expr` textual pipeline: trimming, shortcuts, rewrites, statement mode, binding analysis, against a fake engine | K1, A1, E3a | JE-5 to JE-14 cases as recorded in E3a, checked at unit level with each case's recorded context (preceding id, producer and stored texts) supplied directly; the same fixtures run end to end in N1a's gate | 3 | yes |
| C3a | Bun step runtime core: entrypoints and binding, hydration, the Bun boundary, outcome mapping, exit interception, process-level handlers and attribution, the working directory; the Go step-case runner and comparator | C1, C2a, W1 | the step suite; the alternate-entrypoint and arrow-export cases | 3 | yes |
| B2b | `internal/expr` on the real engine: serialisation, error texts, the expression runner | B1a, B2a | the expression suite against the E3a recordings, at unit level as in B2a; the budget gate of [0003](0003-semantics-placement.md) | 4 | yes |
| C2b | codebase bundling through esbuild's Go API, using the shared shims and define table; assets; the CLI parity check | C2a, C3a, W1 | D13 cases (Node against Bun); an esbuild CJS codebase script that imports a host-provided module reads the host's store 16/16 on Bun 1.3.10 in W1's host-mode skeleton; env-rewrite cases for CJS and ESM output; the parity check or its documented fallback | 4 | yes |
| C3b | per-invocation env, log and `fetch` views, child-process wrapping, `fresh` and `warm` modes | C3a | the step suite in `fresh` and `warm`; the D9 to D11 cases in both modes; case `watchdog/finite-cpu` of [0004](0004-harness-protocol.md) in `fresh` and `warm` | 4 | yes |
| C3n | native boundary mode | C3a, **S1** | native cases from the reference | 4 | yes, for hosts with native steps |
| E1 | `wltest check` for flows: the flow runner, first fixtures per area, run on W1's skeleton engine | K1, C3a, W1 | the first fixtures pass in `warm`, the only mode before C3b | 4 | yes |
| F4 | `internal/bundle`, `windmill-lite build` and `verify`: format, manifest, digest, reproducibility | A5, B1a, C2a | two builds of one fixture give one digest; the static Linux engine starts on an empty base | 4 | yes |
| N1a | engine core, sequential: transforms, the `results` routes, `skip_if`, stops, identity, mock, the hook seams of K1 | A5, B2b, E1, E3b | the E3b cases that [`internal/oracle/testdata/batches/e3b.json`](../../internal/oracle/testdata/batches/e3b.json) gates on N1a (the others need `continue_on_error`, a failure module, an error shape or chaining error, a retry or a sleep, a branch or a provider, and gate the milestone that implements it, as E3a's do); the sequential, context-free subset of E3a end to end: flow cases whose flows use only plain steps, transforms, `skip_if`, `stop_after_if` and identity, with no failure module, container, wait, retry or platform provider. E3a's other flow cases gate the milestones below that implement their features | 5 | yes |
| E3e | the remaining recordings, among them the run-and-wait HTTP halves of FX-36 and FX-37, the contested `later` cases and the probe-catalogue reconstruction (Q-OR-16) | E3a, E3b, E3c, E3d, E1 | — | 5 | yes |
| E3f | recording batch **E3f**: an oracle session whose worker runs native steps, with native fixtures, and the recordings that semantics revisions SC3 and SC4 requested ([README](README.md), Recording requests for batch E3f) | E3e, E4b, N5 | every E3f run that windmill-lite already matches, in the flow suite | 10 | yes |
| N1b | engine errors: `continue_on_error`, failure module and recovery, error shapes, chaining errors, final result, cancellation outcome | N1a, E3c | the E3c cases whose flows need nothing a later milestone implements (the others gate N3c and NI, below); E3a's failure-module and error-route cases (JE-17/a, JE-35/a–c) and its root-pickup expression cases (JE-24/b, JE-24/d); the E3b cases gated on N1b (FX-6/b, FX-7/d, FX-10/a, FX-19/e–g and /i, FX-21/a and its failure-module variant) | 6 | yes |
| N2 | engine waits and time: `sleep`, `timeout` and its messages, retry arithmetic, jitter and argument reuse, deadlines, the clock | N1a | FX-23 to FX-29 cases, but FX-24/d–e (a loop's `timeout`, NI's) and FX-24/g (`later`); E3a's wait, timeout and retry cases (JE-2/a, JE-22/a–b, JE-28/a–c, JE-29/a, JE-32/a–b, JE-V2/c); E3b's FX-19/h | 6 | yes |
| N3a | for loops: sequential, collapsed, parallel, `parallelism` | N1a, E3d | the E3d for-loop cases; E3a's for-loop cases (JE-9/e–f, JE-19/a and /c, JE-26/d, JE-27/a–b on loops, JE-30/a–b, JE-36/a–c and /e, JE-37/a, JE-38/c, JE-39/a, JE-V1/a–e) | 6 | yes |
| N3b | while loops, `branchone`, `branchall` | N1a, E3d | the E3d while and branch cases; E3a's while and branch cases (JE-24/a, JE-27/a–b on `branchall`, JE-31/a–b, JE-36/d and /f); E3b's FX-20/a and its error-stop variant | 6 | yes |
| G1 | in-repository benchmarks of [0004](0004-harness-protocol.md) and [0005](0005-json-step-boundaries.md), run spans | A1, C1, N1a | a recorded baseline | 6 | yes |
| PF1 | facade router, interception, attribution and the transport restriction, the 501 policy, job identity | C3b, N1a | the attribution test of [0007](0007-platform-interface.md) | 6 | yes |
| N3c | subflows by path, `results` across scopes, stop propagation, failure modules in bodies | N1b, N3a, N3b | CF-16 to CF-20, CF-29 and CF-V cases; E3a's cross-scope cases (JE-19/b, JE-38/d) and its child-pickup case (JE-35/d, a subflow by path); the E3c runs that need a subflow by path or a body's failure module (FX-15/c, FX-33's parent view, FX-34/a) | 7 | yes |
| NI | integration slice of N1b, N2 and N3: retries, sleeps, cancellation and failure inside containers | N1b, N2, N3a, N3b | CF-21 to CF-28 cases; FX-24/d–e (a loop's `timeout`); the E3b cases that need both N1b and N2 (FX-6's retry variant, FX-8/b, FX-19/d); E3a's unrecoverable-stop cases on a module with a `retry` (JE-26/b–c); the E3c runs that need a retry, a javascript `timeout` or a container (FX-14/b, FX-15/b, FX-32/b, FX-36's failed `branchall`); PF3's WA-6/b run of a retry in a loop's body (`client-api/flow-path/retry`) | 7 | yes |
| PF2 | resources, variables, secrets, the interpolation walker | PF1 | WA-10 to WA-12 and WA-40 to WA-42 cases; the E3b FX-42/h case; E3a's provider cases (JE-11/a, JE-20/a–b, JE-38/b, JE-41/a–c, JE-42/a and /e, JE-V3/a) | 7 | yes |
| PF3 | flow user state, object store, dispatcher, job registry | PF1 | WA-5, WA-6, WA-14 and WA-16 cases; the dispatch-target restriction case | 7 | yes |
| E4b | the stub acceptance cases on windmill-lite; follow-up E4c, without a row of its own: the copies that `wltest stub`, `check` and `record` make of a sync tree carry what its codebase entries name outside it | E4a, E1, N3c, NI | the cases match the oracle's | 8 | yes |
| N4 | the `cache_ttl` result cache with the host store interface | N3c | the E3d FX-40 cases | 8 | yes |
| F1 | `wl`: `Open`, `Run`, `BuildInfo`; `windmill-lite run` | F4, N3c, NI, PF2, PF3 | the flow suite through `wl` in `fresh` and `warm`, including E3a cases that need features of two milestones neither of which depends on the other (JE-V2/a and /b: retry, and a variable read or argument resolution), and E3b's FX-17/b (retry and a variable) | 8 | yes |
| N5 | retry on a subflow step, in the engine and in stub mode | NI, E4b | the `control-flow/subflow-retry` cases, recorded on the oracle, in `fresh` and `warm`; the `stub/subflow_retry` case on the oracle and on windmill-lite | 9 | yes |
| F2a | hooks as Go callbacks: H1, H2, H6, H8, H9, H12, H13, and mandatory provided sites | F1 | hook cases; the run-time half of the provided-site policy case of [0002](0002-ir-and-loader.md) | 9 | yes |
| F2b | budgets and latches: H3, H4, H5, H7, H10 | F1 | latch-precedence cases | 9 | yes |
| F3a | host-mode transport: `run` frames, host-provided-module registration, scope capture, health, drain, the watchdog option, the working-directory checks | F1, C2b, C3b | `workdir_not_writable` and `workdir_mismatch` cases; a codebase script reads its extracted asset in host mode | 9 | yes |
| F3h | host-provided modules outside the sync folder (a file of a codebase entry's `relative_path`), the codebase build's time with host-provided modules listed, and the load effects a shimmed bundle keeps | F4, C2b | an outside module builds, registers and is shared; listing a module leaves a codebase build's time as it was; shimmed and plain bundles run the same load effects ([0010](0010-packaging.md)) | 9 | yes |
| F5 | go-plugin adapter, `runner.proto`, `wl/plugin` | F1 | — | 9 | no |
| F7b | HTTP adapter | F1, E3e | the E3e FX-36 and FX-37 HTTP cases | 9 | no |
| F7c | `windmill-lite gen go` | F1 | — | 9 | no |
| F7d | `windmill-lite gen ts` | F1 | — | 9 | no |
| F6 | `pack --single` and `image` | F1, F3a | the image runs on its base: empty for the static engine; for the compiled Bun harness, the smallest base that provides the C library it links, recorded in the manifest | 10 | no |
| F2c | host plugins over `host_call`: TypeScript hooks, `around`, the provider bridge | F2a, F3a | the `conformance/plugins` cases in `warm` (a plugin the runtime names) and in host mode (the host SDK's plugin): every F2a hook (H1, H2, H6, H8, H9, H12, H13) and a mandatory provided site answered by a TypeScript plugin over `host_call`, giving what the same cases give through Go callbacks; an `around` wrapper around a step; a plugin's error, a provider's decline and error, and a `host_call` timeout latching the run; `host_call` refused where it must be (a `fresh` runtime, an undeclared hook, a reply to no call, a declaration the engine cannot use); F2b's H3 and H7 answered by the plugin, and its budgets and latches carried to the run (H3, H4, H7, H10: `RunOptions` in `warm`, the `run` frame in host mode); step code's `wl.latch` latching the run and `wl.signal` aborting at a deadline | 10 | yes |
| F7a | plain gRPC adapter | F5 | — | 10 | no |
| F3b | **host-mode integration gate** | F2b, F2c, F3a, E1 | on Bun 1.3.10, with a public test supervisor that only restarts on exit: the flow suite in `warm` and in host mode; the never-settling-step, watchdog, `watchdog/finite-cpu` (host mode) and resource-release tests of [0004](0004-harness-protocol.md); the H11 policy case | 11 | yes |
| F8a | **the shared module-code record** ([0013](0013-shared-module-code.md)): each option for memory that grows with the step modules a process loads, measured on a public synthetic reproduction and on a closure shaped like a host's; a design milestone | F3b, F3h | the record, with each option measured on both fixtures, in host mode or, where the runtime cannot load its layout yet, in a loader model; the synthetic reproduction as a host-mode test in three layouts (per-script, one shared instance, the shared-code prototype); the embedder-shaped closure as a test behind `WL_MEMORY_PROBE`; a Codex review of the record | 12 | yes |
| F8b | **shared code** ([0013](0013-shared-module-code.md)): one closure per build group, an instance per step module in `warm` and host mode, the manifest and trace fields, D18; the runtime keeps only the metadata fields it reads | F8a | 0013's D18 step and build cases in `warm`, host mode and `fresh`; the flow suite in `warm` and host mode from bundles built with shared code; 0013's memory gate (the embedder-shaped closure at most 650 MiB peak with 34 modules and eight concurrent runs over 1,000 runs, at most 16 MiB per further module; the synthetic reproduction at most 1.5 MiB per further module) | 13 | yes |
| R1 | `internal/platform/remote` (hybrid dispatch) | PF3, **H-b** | — | — | no |

**Further harnesses** (Python, then Deno, Go, Bash) and the remaining
`later` findings are a programme, not a milestone. It is planned through
its own records and split into milestones then.

**Critical path to the first host integration.** The table's "Needs merged"
column gives the order, with its joins: read the path from the table. The
path ends at the host-mode integration gate and shared code, since the first
host's memory budget needs shared code
([shared module code](0013-shared-module-code.md)).

- The walking skeleton retires the process-model and pinned-Bun risks two
  waves after the contracts, before the engine milestones build on them.
- A host whose flows use native steps also waits for the semantics team's
  request on native steps and for the native boundary mode.
- Nothing else waits on an external gate, except hybrid dispatch, which
  waits for the human decision.

**Deferred out of the first edition:** a wazero (no-cgo) expression backend
([where semantics live](0003-semantics-placement.md)), and the further
harnesses above.

## Consequences

- The riskiest integration (host mode, the shared process, the pinned Bun)
  is exercised early by the walking skeleton and gated again by the
  host-mode integration gate, instead of surfacing last.
- Every component lands with its README and its conformance cases, so the
  documentation gate and the suite grow together.
- The hybrid adapter is the only milestone held by a human decision; native
  steps are the only ones held by a semantics request.
- **Conformance levels.** Each level of
  [where semantics live](0003-semantics-placement.md) has a directory of its
  own under `conformance/`, such as `openflow/` for the OpenFlow decode
  cases, `jsonb/` for the stored-form cases, `syncfolder/` for the
  sync-folder load cases and `ir/` for the compile cases.
- **Recording batches and gates.** A recording batch's manifest,
  `internal/oracle/testdata/batches/<batch>.json`, can name in `gates` the
  milestone whose exit gate runs each of its runs: the one that implements
  the last feature the run's flow needs.
  - A case that needs two milestones, neither of which depends on the
    other, gates the first milestone that needs both.
  - A milestone's exit gate includes the runs that a manifest assigns to
    it.
- **The flow suite's list.** `cmd/wltest/testdata/flowsuite.json` lists the
  runs that the flow suite requires, each with the milestone that requires
  it, and the milestones whose manifest gates it also requires
  ([the flow suite](../../cmd/wltest/README.md#the-flow-suite)). Do not
  remove a required run: no test stops it, and it then stays required only
  while a manifest gate requires it. The file keys a run of a context-free
  expression case `<file>/<case>`.
  - A run that the engine cannot run yet is reported as unsupported and
    never compared.
  - The `diff` verb of [differential testing](0011-differential-testing.md)
    is in no milestone's row.
- **Job identity** is in `internal/platform`, beside the service
  interfaces, because the engine and the facade share it. The facade's
  route table is generated from the Apache-2.0 schema vendored in
  `internal/oracle/openapi`, so the schema is vendored once.
- **The platform services.** The resource, variable and secret services,
  their local default store (`platform.Store`, filled from a bundle's
  `data/` by `platform.LoadData`) and the interpolation walker are in
  `internal/platform`, and their routes in the facade. The engine takes the
  same `platform.Services` as the facade (`engine.Config.Platform`).
- **The default dispatcher** is in `internal/engine` (`engine.Dispatcher`,
  with `Engine.RunScript` for a root script's run as a script job), not in
  `internal/platform` beside the dispatcher's interface. It runs the
  engine's program, and the engine imports `internal/platform`. The job
  registry, flow user state and the object store are in `internal/platform`,
  with their routes in the facade.
- **The result cache.** The engine runs `cache_ttl` on the result cache of
  the [platform interface](0007-platform-interface.md),
  `platform.ResultCache`: a host's store, or the services' in-memory
  default (`internal/engine/cache.go`).
- **The build.** The shared build transforms (the define table, its support
  module and the shim generator) are in `internal/bundle`, beside the
  bundle format, and the Bun build helper is in `harnesses/bun/build`.
  Codebase bundling (`BuildCodebase`, esbuild through its Go API) is in
  `internal/bundle` too, beside the Bun build.
- **Codebase step cases.** A step case can name a `codebase`. When
  `wltest check` has a Node executable (`--node` or `WL_NODE`), it also runs
  each CommonJS codebase case on Node, except one at the native boundary:
  that is how the cases of
  [D13](0001-equivalence-contract.md#5-named-deviations) compare Node
  against Bun.
- **The CLI parity check** runs the pinned CLI in a workflow of its own,
  `parity.yml`, which branch protection does not require. Its fallback runs
  in the Go jobs.
- **Native step cases.** The step files `conformance/steps/bun/wr-40.yaml`
  to `wr-45.yaml` hold the native cases of the reference. Each catalogue
  case of the native findings, [WR-40](../semantics/languages.md#wr-40) to
  [WR-46](../semantics/languages.md#wr-46), is a step case, run as `nativets` and as
  `//native` where the catalogue says both kinds, or is listed in the
  level's `deferred.yaml` with its reason.
  - A step case gives its `language` and its `timeout` for that.
  - The native build cases are `conformance/build/d5.json`.
- **The engine runs a compiled program** (`engine.New`, `Engine.Run` on an
  `ir.Program`). The compiler runs every expression's textual stages, and
  the engine compiles their bytecode when it opens the program.
- **One code path for every transport.** `windmill-lite run` and
  `wltest check` are both transports over `wl` (`wl.Open`, `Runtime.Run`),
  so the flow suite runs the code path that a host runs. `wltest check`
  gives its runs the effect sink and run groups through the same
  `wl.Hooks`, so the runner takes no path that a host does not have.
- **Benchmarks.** The benchmarks of the
  [harness protocol](0004-harness-protocol.md) and of
  [JSON at step boundaries](0005-json-step-boundaries.md) are Go benchmarks
  beside the code they measure, in `internal/jsonval`, `internal/harness`
  and `cmd/windmill-lite`, with their Bun sides in those packages'
  `testdata`.
  - What they share (payloads, percentiles, concurrent workers) is in
    `internal/bench`, a package that only test files use.
  - Their baseline is in
    [JSON at step boundaries](0005-json-step-boundaries.md#baseline), which
    records why they run locally, never in CI. Its large-value figures come
    from GitHub's hosted runners, through the `Benchmarks` workflow, which
    is started by hand and gates nothing.
- **Two host-plugin follow-ups stay open.** An abort signal for plugin
  hooks needs a `host_call` cancel frame. A seed in the host's `run` frame
  matters to replay.
- **Where the details live.** Each part's behaviour is described beside its
  code:
  - [internal/engine](../../internal/engine/README.md): loops, branches and
    subflows as child flows (`Trace.Children`), the error routes, waits and
    retries, the transition monitor, the result cache (`cache_ttl`), and
    `squash`, which the engine accepts and ignores;
  - [internal/conformance](../../internal/conformance/README.md) and
    [cmd/wltest](../../cmd/wltest/README.md): the location rule
    (`StripLocations`), declared `copies` and `not_found_locs`, the flow
    suite in each mode, and a fixture's `roots`, `delays` and
    `oracle.session`;
  - [conformance](../../conformance/README.md): the fixtures, the recording
    batches' manifests and the deferred cases;
  - [internal/platform](../../internal/platform/README.md),
    [wl](../../wl/README.md), [harnesses/bun](../../harnesses/bun/README.md)
    and [cmd/windmill-lite](../../cmd/windmill-lite/README.md): the services
    and dispatch, the library's hooks and options, host mode with its
    plugins and recycle triggers, and the commands and memory tests;
  - the records on hosts: [host hooks](0008-host-hooks.md) (host plugins)
    and [host adapters](0009-host-adapters.md) (host mode as a runtime of
    `wl`);
  - the records on the build and the oracle: [packaging](0010-packaging.md)
    (`selfcheck`, files outside the sync folder, a listed file's shim),
    [differential testing](0011-differential-testing.md) (the run-and-wait
    submission, stub mode's retries, job ids the walk cannot map) and
    [shared module code](0013-shared-module-code.md) (the memory
    measurements).

## Spec IDs relied on

The milestone rows cite the finding families each one encodes; the records
they implement list their own.

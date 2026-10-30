# 0012 · Go package layout and implementation milestones

**Status:** proposed · **Topic:** where code goes, and the sequence of
pull-request-sized milestones to the first release, with what can run in
parallel.

## Context

[AGENTS.md](../../AGENTS.md) holds back new packages until the design decides
the layout. Records [0001](0001-equivalence-contract.md) to
[0011](0011-differential-testing.md) fix the components; this record places
them and orders the work. Each milestone is one reviewed pull request: one
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
internal/host/              gRPC and HTTP adapters (0009)
internal/gen/               typed client generation (0009)
internal/conformance/       fixture loading, observation, comparison, identity encoding (0003, 0011)
internal/oracle/            the oracle harness (0011)
harnesses/bun/              TypeScript: runtime/ (step runner), build/ (bundling, signatures),
                            host/ (Bun host SDK), test/ (developer checks against golden frames)
proto/windmill_lite/v1/     runner.proto (0009)
conformance/                flows/, steps/, exprs/, openflow/, jsonb/, syncfolder/, ir/, protocol/, build/: the shared cases (0003)
```

Everything except `wl/` and `wl/plugin/` is internal, so the engine can change
without breaking hosts. The Bun harness and its host SDK are consumed from
this repository; nothing is published to a package registry.

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
| F8a | **the shared module-code record** ([0013](0013-shared-module-code.md)): each option for memory that grows with the step modules a process loads, measured on a public synthetic reproduction and on a closure shaped like a host's; a design milestone | F3b, F3h | the record, with each option measured in host mode, in a loader model where the runtime cannot load its layout yet, or by a labelled proxy; the synthetic reproduction as a host-mode test in three layouts (per-script, one shared instance, the shared-code prototype); the embedder-shaped closure as a test behind `WL_MEMORY_PROBE`; a Codex review of the record | 12 | yes |
| F8b | **shared code** ([0013](0013-shared-module-code.md)): one closure per build group, an instance per step module in `warm` and host mode, the manifest and trace fields, D18; the runtime keeps only the metadata fields it reads | F8a | 0013's D18 step and build cases in `warm`, host mode and `fresh`; the flow suite in `warm` and host mode from bundles built with shared code; 0013's memory gate (the embedder-shaped closure at most 650 MiB peak with 34 modules and eight concurrent runs over 1,000 runs, at most 16 MiB per further module; the synthetic reproduction at most 1.5 MiB per further module) | 13 | yes |
| R1 | `internal/platform/remote` (hybrid dispatch) | PF3, **H-b** | — | — | no |

**L1, further harnesses** (Python, then Deno, Go, Bash) and the remaining
`later` findings, is a placeholder for a programme, not a milestone. It is
planned through its own records and split into milestones then.

**Critical path to the first host integration:** K1 and A2, A3 → A4 → A5;
K1, A1 and E2 → E3a → B2a, with B1a → B2b; C1 → C2a and W1 → C3a → E1; all of
them, with E3b, into N1a → N1b (with E3c), N3a and N3b (with E3d) → N3c and
NI → F1 → F2a, and F3a (with C2b) → F2c → F3b → F8a → F8b, since the first
host's memory budget needs shared code ([0013](0013-shared-module-code.md)). W1 retires the process-model and
pinned-Bun risks in wave 2, before the engine milestones build on them. A
host whose flows use native steps also waits for S1 and C3n; nothing else
waits on an external gate except R1.

**Deferred out of the first edition:** a wazero (no-cgo) expression backend
([0003](0003-semantics-placement.md)), and L1 below.

## Consequences

- K1 is the first pull request after this design; six milestones (A1 to A3,
  B1a, C1 and E2) can merge in wave 1 behind it.
- The riskiest integration (host mode, the shared process, the pinned Bun)
  is exercised by W1 in wave 2 and gated again by F3b, instead of surfacing
  last.
- Every component lands with its README and its conformance cases, so the
  documentation gate and the suite grow together.
- The hybrid adapter is the only milestone held by a human decision; native
  steps are the only ones held by a semantics request.
- `conformance/openflow/` holds the OpenFlow decode cases, a level that
  [0003](0003-semantics-placement.md) added with milestone A3, and
  `conformance/jsonb/` the stored-form cases, a level added with milestone A1.
- `conformance/syncfolder/` holds the sync-folder load cases, a level that
  [0003](0003-semantics-placement.md) added with milestone A4.
- E3c's row names the executor part of FX-37 as well as FX-36's.
  `early_return` changes only the synchronous run-and-wait response, which
  the oracle harness never calls ([OR-10](../semantics/oracle.md#or-10)). So
  E3c records that the flow's own final result is unaffected, and FX-37/a's
  early response joins FX-36's HTTP cases in E3e, gating F7b.
- `conformance/ir/` holds the compile cases, a level that
  [0003](0003-semantics-placement.md) added with milestone A5; they are the
  exit gate's IR cases, with the golden programs in `internal/ir/testdata/`.
- A recording batch's manifest, `internal/oracle/testdata/batches/<batch>.json`,
  can name in `gates` the milestone whose exit gate runs each of its runs:
  the one that implements the last feature the run's flow needs. Milestone
  E3b added gates because its FX core cases reach beyond N1a: about half of them need
  `continue_on_error`, a failure module, an error shape or chaining error, a
  retry or a sleep, a branch or a variable. A case that needs two milestones
  neither of which depends on the other gates the first milestone that needs
  both: NI for N1b with N2, and F1 for N2 with PF2, where E3a's JE-V2/b is.
  A milestone's exit gate includes the runs a manifest assigns to it.
- Milestone PF1 put job identity, which the engine and the facade share,
  in `internal/platform` beside the service interfaces PF2 and PF3 add;
  the facade's route table is generated from the Apache-2.0 schema that
  E2 vendored in `internal/oracle/openapi`, so the schema is vendored once.
- `conformance/protocol/` holds the harness protocol's golden frames and
  exchange transcripts, a level that [0003](0003-semantics-placement.md)
  added with milestone C1; C1's exit gate is the exchanges passing against
  the Bun harness on Bun 1.3.10.
- `conformance/build/` holds the Bun build cases, a level that
  [0003](0003-semantics-placement.md) added with milestone C2a. C2a placed
  the shared build transforms (the define table, its support module and the shim
  generator) in `internal/bundle`, which F4 extends with the bundle format,
  and the Bun build helper in `harnesses/bun/build`.
- `conformance/steps/` holds the step cases, the level of
  [0003](0003-semantics-placement.md) that milestone C3a gave its format
  and its runner, `wltest check`; C3a runs them in `warm`, and C3b adds
  `fresh`.
- C2b placed codebase bundling in `internal/bundle` (`BuildCodebase`,
  esbuild through its Go API) beside the Bun build, and gave the step
  cases a `codebase` field and a per-run `node` check: `wltest check` runs
  each CommonJS codebase bundle on Node as well, which is how its D13
  cases compare Node against Bun. Its CLI parity check runs the pinned CLI
  in a workflow of its own, `parity.yml`, which branch protection does not
  require; the fallback runs in the Go jobs.
- C3n's exit gate, the native cases of the reference, is the step files
  `conformance/steps/bun/wr-40.yaml` to `wr-45.yaml`: each catalogue case of
  WR-40 to WR-46 is a step case, run as `nativets` and as `//native` where
  the catalogue says both kinds, or is listed in the level's `deferred.yaml`
  with its reason. A step case gives its `language` and its `timeout` for
  that; the native build cases are `conformance/build/d5.json`.
- F4 added `windmill-lite selfcheck`, which starts the expression engine and
  runs a few probes, to the CLI: it is how its exit gate starts the static
  Linux engine on an empty base ([0010](0010-packaging.md), drift guard 5).
- E1 made `wltest check` run the flow fixtures on windmill-lite, in `warm`,
  on W1's walking skeleton, which runs sequential flows of Bun steps only.
  Its first fixtures per area are the runs that skeleton can run: the E3b
  and E3c runs of `execution` and the E3a runs of `je/sites` that already
  pass, and new fixtures of two areas that had none, `languages`
  (`flows/languages/bun-results`, `flows/languages/codebase`) and
  `client-api` (`flows/client-api/job-env`), recorded on the oracle.
  `control-flow` has none until N3a and N3b, since the skeleton runs no
  container. `cmd/wltest/testdata/flowsuite.json` lists the runs the flow
  suite requires, each with the milestone that first made it pass, and
  names the milestones whose batch gates it also requires; an engine
  milestone adds itself there, and a required run stays required. A run the
  engine cannot run yet is reported as unsupported and never compared.
  `flows/languages/codebase` is the codebase-script fixture that C2b drops
  its esbuild bundles into: E1 built codebase scripts with the Bun build
  helper as ordinary Bun scripts, and since C2b `wltest check` builds them
  with esbuild (`bundle.BuildCodebase`), the CommonJS one running on Bun's
  CommonJS loader (D13). The `diff`
  verb of [0011](0011-differential-testing.md) is in no milestone's row,
  and E1 left it out.
- N1a's engine core runs a compiled program (`engine.New`, `Engine.Run`
  on an `ir.Program`); the compiler runs every expression's textual stages,
  and the engine compiles their bytecode when it opens the program. W1's
  skeleton became a loader of its own test layout over that engine, which
  `windmill-lite host` serves until F3a. The engine refuses at open, with
  the milestone's name, what later milestones implement. The "sequential,
  context-free subset of E3a" in N1a's row is read as both of E3a's kinds
  of case: its flow fixtures whose flows use only the features the row
  lists, and its context-free expression cases end to end, which
  `wltest check` runs as the one-step flows `wltest record` lifts them
  into and compares with their recordings (B2a's row asks for the same
  fixtures "end to end in N1a's gate"); `flowsuite.json` keys such a run
  `<file>/<case>`. Nine of the 69 expression cases are not required: the
  four transcendental `Math` cases of JE-1 can differ in their last digit
  with the C library (D16), on either CI runner (pow-roots on the Linux
  runner's glibc, hyperbolic and trig on macOS), and JE-48/a by D17, as at
  unit level, where B2b holds them; JE-2/a recorded
  the oracle's own cancellation of a hanging flow; JE-11/a reads a
  variable (PF2); and JE-24/b and /d fail the root at its first pickup,
  whose recorded messages carry Rust locations that ADR 0011's location
  rule does not strip yet, which is N1b's, with the chaining errors and
  JE-35/b. Until PF2, the flow runner reported a run that read the
  platform as unsupported; since PF2 it serves every fixture's resources
  and variables, and reports as unsupported only a run whose flow calls
  `flow_user_state()`, which needs PF3.
- N3a runs for loops on frames: the engine runs the root flow and each
  non-collapsed iteration's child flow with one interpreter, each with
  its own results scope where CF-19 starts one, and a collapsed iteration
  as a job of its loop's flow. "The E3d for-loop cases" in its row are
  E3d's runs whose flows hold a for loop and no while loop, branch,
  failure module or subflow: those of `cf-2` to `cf-9`, `cf-11`, `cf-13`
  and runs `a`, `c-fail`, `c-skip`, `f` and `f-skip` of `cf-12`; `cf-12`'s
  `b` needs a failure module in a body (N3c), and `fx-40`'s loop runs need
  caching (N4, as ADR 0001 §6 says). Their recorded messages end in Rust
  locations at the pickup and chaining routes, which N1b's location rule
  strips, and in `stop_after_all_iters_if`'s error stop, for which N3a
  added a template to the rule of [0011](0011-differential-testing.md);
  the trace points to the generated errors inside a loop's array
  (`ResultNested`), at any depth, so that the rule reaches them and a
  user's error beside them keeps its difference. Of E3a's cross-scope
  cases the flow suite also requires JE-19/b, a parallel iteration's
  scope. The trace lists the child flows a run created
  (`Trace.Children`), from which `wltest check` writes a body module that
  never got a job as `not_run` and names a child flow's job id by its
  iteration's site, as the walker does. With N1b merged first, the engine
  refuses a for loop in a flow with a failure module, since every body
  then runs as a child flow that carries a copy of it, which is N3c's;
  it also refuses a path script's own `cache_ttl` (N4), which a loop body
  made reachable.
- PF2 put the resource, variable and secret services, their local
  default store (`platform.Store`, filled from a bundle's `data/` by
  `platform.LoadData`) and the interpolation walker in
  `internal/platform`, as the layout says, and their routes in the facade.
  The engine takes the same `platform.Services` as the facade
  (`engine.Config.Platform`): it resolves a leaf's arguments when the job
  is picked up, `flow_env` on every transition, into the frame that starts
  the module, so parallel iterations share none, and the expression
  helpers. FX-42/h's run `args_file` reads the arguments file, so the
  harness writes `args.json` into each step's working directory
  (WR-5, core), the latest invocation's in a warm process (D10). "The
  WA-10 to WA-12 and WA-40 to WA-42 cases" in PF2's row are read as each
  catalogue case of those findings at the level that can run it: the step
  cases (`conformance/steps/bun/wa-*.yaml`, through the real harness in
  `warm` and `fresh`), the walker's unit cases
  (`internal/platform/testdata/interpolation.json`), the stock client
  (`TestProvidersOnTheClient`) and, for WA-40/a and /b, the flow fixture
  `client-api/providers`, recorded on the oracle, whose runs the flow suite
  requires; WA-11/b is a write (`later`), deferred with the reason.

- PF3 put the job registry, flow user state, the object store and the
  dispatcher's interface in `internal/platform`, as the layout says, their
  routes in the facade, and the default dispatcher in `internal/engine`
  (`engine.Dispatcher`, with `engine.RunScript` for a root script's run as
  a script job), since it runs the engine's program and the engine
  imports `internal/platform`. A flow fixture may list `roots` in its
  `fixture.yaml`, the bundle's other roots (the targets its steps
  dispatch, which a host contract lists), which `wltest check` compiles as
  the default dispatcher's program. "The WA-5, WA-6, WA-14 and WA-16
  cases" in PF3's row are read as PF2's are, each catalogue case at the
  level that can run it: WA-5/a and /c as step cases (PF1's `wa-5.yaml`)
  and, inside bodies, in the flow fixture `client-api/flow-path`, WA-5/b
  deferred while schedules are out of scope; WA-6/a, /c and /d as that
  fixture's runs `seq`, `par`, `branch` and `branch_default`, whose
  recordings the flow suite requires (/d since N3b's branches, which
  merged first); WA-14/a as step cases (`wa-14.yaml`), on the stock client
  (`TestFlowUserStateOnTheClient`) and in the flow fixture
  `client-api/user-state`, recorded on the oracle, whose runs the flow
  suite requires, WA-14/c by internal/platform's `TestFlowUserState`, and
  WA-14/b (Python) deferred; WA-16/a and /c as step cases (`wa-16.yaml`)
  and on the stock client (`TestObjectsOnTheClient`, which checks WA-16/b,
  the client's own refusal, too), WA-16/d (a CE observation) deferred.
  WA-6/b needs a retry inside a container's body, which N2 left to NI;
  PF3 does not need NI merged, and the job identity it observes is PF1's,
  so by the rule above it gates NI, the milestone that implements the
  last feature it needs: the oracle recorded it with the fixture, and NI
  adds it to `flowsuite.json`. "The dispatch-target restriction
  case" is `wa-15.yaml`, whose cases a step case's bundle, which has no
  root to dispatch, refuses, and `TestDispatchRefusedOnTheClient` on the
  stock client. The flow fixture `client-api/dispatch`, on the oracle's
  second session (two workers, OR-5), records what the reference leaves
  out of a dispatched job: its identity, which the engine reproduces.

- N4 runs `cache_ttl` on the result cache of
  [0007](0007-platform-interface.md): `platform.ResultCache`, a host's
  store or the services' in-memory default (D4), in which the engine
  looks a job up before it runs and stores its result when it succeeds
  (`internal/engine/cache.go`). The TTL follows FX-40's matrix: an inline
  step's module, a collapsed iteration's loop, a path script's own, a
  subflow's or the root's flow-level `cache_ttl`, and a script root's own
  for its script job. The key leaves out any path that differs between
  jobs, so a collapsed loop's iterations share an entry (FX-40/d).
  "The E3d FX-40 cases" in its row are read as `execution/fx-40`'s seven
  runs (FX-40/a to /d), which pass in `warm` (the flow suite) and in
  `fresh` (`TestCacheFixturesFreshAndWarm`). For that, `wltest check` now
  runs flow fixtures in `fresh` too, on a pool of one process per
  invocation, with the engine's new `Config.ModuleState`; the flow suite
  through `wl` in both modes stays F1's. `execution/fx-40-hash`'s two
  runs stay unsupported: the compiler refuses a hash-pinned script, which
  a folder cannot supply, so the IR has no hash-pinned row of the matrix
  to run. A flow job stores its result when the job that ended it
  succeeded, which is FX-40/b's rule; for the cases no recording settles,
  its text decides: a flow ended by an error stop stores, since that
  step's job succeeded, and one that a tolerated failure at its last
  module ends successfully does not, since that job failed. S3-aware
  argument hashing needs an object's ETag, which the object store does
  not keep, so `cache_ignore_s3_path` is refused (`later`).

- N3c runs subflows by path as child flows at their step's site
  (`internal/engine/subflow.go`), each its own results scope (whose
  lookups still walk up, Q-CF-12 matched), its own innermost flow scope
  and path, with its own failure module and `flow_env`; each frame now
  carries its flow, its stored `flow_env` and its start, which replace
  the run-wide ones. Every loop iteration and branch of a flow with a
  failure module runs a copy of it, and a container whose child ran its
  copy no longer runs the flow's own failure module again (CF-16 item 4),
  through a clean stop that keeps the failure or the stop a branch
  forwards for it too, except across a subflow boundary. "CF-16 to CF-20, CF-29 and CF-V
  cases" in its row are read as CF-19's cases, recorded by E3a and E3d
  (`cf/cf-19`), plus the cases of CF-16 to CF-18, CF-20, CF-29 and CF-V1
  to CF-V5. N3c records the latter with its own fixtures under
  `conformance/flows/control-flow` (cf-16 to cf-v5), whose manifest is
  `internal/oracle/testdata/batches/n3c.json`. CF-V6's cases are CF-19's
  and already required since N1a. CF-V4/b and CF-V5/b need an inline or
  raw-preview submission, which windmill-lite never runs, so they are
  deferred to `none` (and CF-16/d's inline half with them). CF-19/c passes
  through a new run field, `not_found_locs`: it is FX-50 rule 3, the
  case declaring where the parenthesised not-found location is generated.
  A run can declare the values that copy, through user code, a site's
  generated error (`copies`, ADR 0011 amended), which makes JE-35/d and
  FX-15/b pass; so FX-15/b, which N2 left to NI, is required under N3c. E3a's JE-19/b
  has been required since N3a; its run `je19-d`, a subflow's read of its
  caller's results, passes too. The E3c runs of the row (FX-15/c, FX-33's
  parent view, FX-34/a's `loop` and `step_id`) and `cf-12`'s `b` are
  required under N3c. An empty container's error stop now fails its
  record's status, as the walker reads a container with no child
  (CF-29/a). A container as the failure module (CF-30, source-traced only)
  and a retry on a subflow step (whose attempts' subflows NI places under
  the attempt's site) are refused; N5 runs the latter. The N2 and N3b
  merge resolution (2dfbae10) was reviewed with this milestone.

- N3b runs while loops on N3a's sequential loop, with no iterator, and
  `branchone` and `branchall` on frames: each branch that runs is a child
  flow of its own, which the trace lists under `b:<n>` or `b:default`.
  "The E3d while and branch cases" in its row are E3d's runs whose flows
  hold a while loop or a branch and no failure module or subflow: `cf-10`'s
  `a`, `cf-12`'s `d`, `e-fail` and `e-skip`, `cf-14`'s and `cf-15`'s runs;
  `cf-12`'s `b` needs a failure module in a body (N3c). The flow suite
  also requires the runs N3b first made pass: CF-9/b, CF-19/a, the two
  `branchall` runs of `oracle/workers2`, and FX-36's failed `branchall`,
  which NI's row names but which needs nothing of N2 (a sequential
  `branchall` whose second branch fails). FX-20's error-stop run gained
  the carried `EarlyStopError` in its expect, which its recording cannot
  show. With N1b merged, the engine refuses any container, not only a for
  loop, in a flow with a failure module (N3c). A `branchone` that ran no
  branch has a nil job, whose engine read fails with the server's
  not-found text; CF-19/c's recording has that text end in the server's
  source location in the middle of a transform error, which ADR 0011's
  location rule does not reach, so that run stays N3c's. N2 merged first,
  so its refusal of waits and retries for NI covers every container, not
  only for loops: a sleep, a retry or a javascript or negative timeout on
  a while loop, a `branchone` or a `branchall`, in any container's body,
  or as the sleep before any container; and a branch's child flow keeps
  its own transition monitor, whose completion refreshes its flow's
  ping, as a loop iteration's does.

- N1b gave the engine core Windmill's continue decision for a flow
  without retries: `continue_on_error`, the error stops, the
  unrecoverable stop and the failure module at `s:failure` with its
  recovery, chaining errors routed by context, the storage failure of a
  job's result (FX-48 case 1), `wm_failure` (FX-13, WR-17) and the
  cancellation outcome, whose reason and canceler a host passes as the
  context's cause (`engine.Cancellation`). The location rule of ADR 0011
  (`conformance.StripLocations`) now strips the `<loc>` suffixes of
  FX-50's templates, one per `Error: ` prefix, of the chaining errors and
  of FX-21's string, where windmill-lite generated the value; a trace
  records the origin of a carried value the engine sets apart from the
  job's result, so that rule reaches it too. "The E3c cases" in N1b's row
  is read as N1a's row reads E3b's: the E3c runs whose flows need nothing
  a later milestone implements, which `flowsuite.json` lists under N1b.
  E3c has no `gates`, because two of its runs need features no milestone
  of the first edition implements, so the rows say where its other runs
  go: FX-12's Deno variant (L1) and FX-35's preprocessor run (`later`)
  gate no milestone; the runs that need a subflow by path or a body's
  failure module gate N3c, which implements both; those that need a retry
  or a javascript `timeout` gate NI, the first milestone that needs both
  E3c and N2, and so does FX-36's failed `branchall`, a failure inside a
  container. By the same rule, E3a's JE-35/d
  (a subflow by path) moved from N1b's row to N3c's, and JE-26/b–c, whose
  flows configure a `retry` that the unrecoverable stop never uses, moved
  to NI's, as E3b gates its FX-19/h (an error stop with an unused `retry`)
  on N2. The root-pickup expression cases JE-24/b and JE-24/d, which N1a
  left to N1b's location rule, are N1b's. The failure module runs with its
  own id at the top level; its copy inside an inline body (FX-34) comes
  with bodies, in N3c.

- E3e, the last recording batch, holds what the earlier batches left to it
  and the contested `later` rows of [0001](0001-equivalence-contract.md)
  §6: FX-36's HTTP part with FX-37/a's early response, for which the
  harness gained a run-and-wait submission and gates F7b; FX-49, WR-16 and
  WR-28, whose implementing milestones belong to L1; the probe catalogue
  rebuilt as executable fixtures (Q-OR-16); codebase scripts in CommonJS
  and ESM; FX-10/c, which the reference added after E3b; and the pull
  read-back of CF-6/b and SY-6/c. Its manifest,
  [`internal/oracle/testdata/batches/e3e.json`](../../internal/oracle/testdata/batches/e3e.json),
  has no `gates`: only F7b needs E3e merged. Cases outside the equivalence
  contract (a restart, a preview, the CLI's push-and-version decisions for
  codebase scripts) and a concurrency count that needs three workers are
  deferred to `none`, with the reason. The oracle workflow runs a second
  session with two workers, for the fixtures that need them and for the
  smoke fixture under another `LISTEN_NEW_EVENTS_INTERVAL_SEC`.

- N2 gave the engine Windmill's waits and time: `sleep` (FX-23,
  JE-28), the step timeout evaluated for every module with the texts of
  its failures (FX-24, FX-25, JE-29), `retry` and `retry_if` with
  FX-26's 16-bit delay arithmetic, the flow-wide retry state of FX-27 and
  FX-29 and the reuse of the failed attempt's arguments (JE-V2), H10's
  clock (`RunRequest.Clock`, with a recording clock) and jitter source
  (`RunRequest.Jitter`, else the run's seed), H4's pre-write deadline, and
  the monitor of flows that hang between steps (JE-2), which cancels a
  run whose transition spends 60 s evaluating, as Windmill's server
  cancels it. A failed attempt's record is the site `<module>/r:<k>`, and
  a module's record lists the flow-wide failed attempts, which the
  walker meets again under every module whose status lists them; `wltest
  check` now checks a run's `delays` on the trace's job times and runs
  JE-2/a, with a run timeout of 150 s. "FX-23 to FX-29 cases" in N2's
  row is read as the catalogue cases N2's features decide, recorded on
  the oracle by N2's own fixtures `conformance/flows/execution/fx-23` to
  `fx-29`, whose manifest is `internal/oracle/testdata/batches/n2.json`:
  FX-24/d–e need a loop, so they gate NI; FX-24/g is `later`; the delays
  of FX-26/c–e, hours long, are checked at unit level on the recording
  clock, since the walker does not capture a queued job's scheduled time.
  FX-25/a, FX-28/b–c and FX-29/a–b also need N1b's features, which merged
  first, so N2's gate runs them. JE-V2/a moved from N2's row to F1's,
  beside JE-V2/b: its transform reads a variable and its script writes one
  through the job API, which need PF1 and PF2; `fx-27/reuses_args`
  records the rule it shows, that a retry does not evaluate its
  transforms again. The E3b and E3c runs that NI's row gates and N2 can
  run because N1b merged first (FX-6's retry variant, FX-8/b, FX-14/b,
  FX-19/d, FX-32/b) and E3a's JE-26/b–c are required by `flowsuite.json`
  under N2; FX-15/b still differs only by the `<loc>` suffix its failure
  module copies from a generated error, which ADR 0011's location rule
  does not reach, and stays NI's. N3a merged first, so the engine
  refuses N2's waits and retries in a for loop's body, on a loop, and as
  the sleep that delays a loop's start, naming NI, which integrates them
  with containers (a loop module's retry restarts it, a collapsed loop
  evaluates its timeout per iteration); each flow, the root and every
  child flow, keeps its own retry state and transition monitor, so
  parallel iterations share none.

- NI replaced the engine's refusal of N2's waits and retries in, on and
  before containers with their behaviour, recorded on the oracle by NI's
  own fixtures `conformance/flows/control-flow/cf-21` to `cf-28` and
  `waits`, whose manifest is `internal/oracle/testdata/batches/ni.json`.
  "CF-21 to CF-28 cases" in its row is read as N2's row reads FX-23 to
  FX-29: the catalogue cases of those findings, written as runs with an
  `expect` from the reference before recording, and corrected with a
  `note` where the oracle disagreed. Four go to `none` (CF-24/c and CF-27/d
  are raw submissions, CF-25/b is EE) or to N3c (CF-23/c is CF-V3/a).
  CF-23/a and /b put a failure module beside a container, which the engine
  refused until N3c; N3c merged first, so NI requires them. The recordings settled what the reference left
  open, among them that a container's timeout is evaluated for each child
  in the container's own scope, a collapsed loop's too; that a retried
  loop's re-entry starts only its first push from its last failed child's
  input; that the sleep before a parallel container delays every child of
  its push; and that a canceled container completes as a failure of
  `Canceled` entries. The oracle contradicted CF-25/a, which holds only
  for a build without the `private` feature: the CE image completed every
  recorded squashed loop with its unsquashed result (CF-25/d), and the
  manifest lists the contradiction. The recordings do not show whether CE
  reuses one runtime across iterations (Q-CF-19), so the engine accepts
  `squash` and ignores it, running the loop unsquashed. FX-15/b, which N2
  left to NI, passed first under N3c, which merged first, through its
  declared `copies`. PF3 merged first
  with WA-6/b's `client-api/flow-path/retry`, a retry in a loop's body,
  which NI's engine runs, so NI requires it. N3c left a retry on a subflow
  step refused for NI; no case of NI's row covers it, so it stayed refused,
  as `later`, until N5. The E3b runs that `e3b.json`
  gates on NI (FX-6's retry variant, FX-8/b, FX-19/d) keep that gate, by
  the rule that a case needing two milestones gates the first that needs
  both; `flowsuite.json` already requires them under N2.

- F1 opens a built bundle with `wl.Open` and runs it with `Runtime.Run`;
  `windmill-lite run` and `wltest check` are both transports over these
  calls, so the flow suite runs the code path a host runs. `wltest check`
  builds each fixture with `bundle.Build`, whose contract's roots are the
  targets the compiler and the engine accept, and opens the bundle once
  per mode; the whole suite passes the same runs in `warm` and in `fresh`.
  JE-V2/a and /b and FX-17/b each have a step write a variable through the
  job API, a route that was `later` until F1 ([WA-11](../semantics/client-api.md#wa-11)'s
  `updateVariable`); the runtime owns its variable store, the bundle's
  `data/` with the host's variables over it, so a later run reads the
  write. ADR 0009 puts the module-state override in `runOptions`; F1
  takes it in `Options`, once per runtime, because the harness pool and
  the engine hold one mode, and a run's override can be added later as a
  run option without breaking a host. N4 merged first, and `Options.Results`
  passes its result cache store, in memory per runtime by default. The
  trace carries the platform calls each step made (`Trace.PlatformCalls`),
  which the runtime collects from the facade's records. PF3's job registry still registers no loop
  iteration, branch or subflow, so a dispatch from inside one gets no
  parent path; no case of F1's row needs it, and it stays open. A retry
  on a subflow step stayed refused until N5.

- N5 is a milestone added after the first plan. A host flow on the MVP
  path retries a flow step that runs a flow by path, which the engine
  refused (N3c, NI). CF-17 says the caller's `retry` applies to a failed
  subflow, but no catalogue case records it. So N5 wrote the fixture
  `conformance/flows/control-flow/subflow-retry` and recorded it on the
  oracle without an expectation first, letting the recordings decide.
  The runs are: a subflow step that fails once; one that runs out of
  retries, plain, under `continue_on_error` and with a failure module; a
  2 s delay; the step in a sequential and in a parallel loop; and above a
  child that retries its own step. Every recording puts a failed
  attempt's subflow job at the step's `r:<k>`, with that subflow's modules
  below it (its own retried modules' attempts below those). A failure
  module, or the step after a tolerated failure, lists the step's failed
  attempts again with that subtree (FX-29), and a later step's `results`
  read the last attempt's result. A run whose step's input transform
  throws (FX-14) showed that the step's subflow job still launches and
  fails with none of its modules run, and that the retry runs the
  subflow with an empty input; the engine now lists that never-run child
  flow, retried or not.
  The engine moves a failed attempt's child flow to that site, as NI
  moves a retried container's last failed child. The same fixture
  records a retry on a while loop and on a branchone, which NI's engine
  already ran and which no case had recorded; N5 requires them.
  Stub mode accepts a retry on a flow step. A failed container attempt
  is walked as its last failed child alone, under `r:<k>` without the
  child's `i:` or `b:` segment, so none of its calls matches the site the
  replay service resolved; a retry on a loop or branch step stays
  refused there. The replay service resolves a subflow's step at its
  site without the caller's attempt and counts its jobs across the
  attempts, which `conformance.CheckReplay` holds to the walk's order
  among the jobs that execute a script, so that a skipped step's
  identity job takes no place (ADR 0011). `stub/subflow_retry` records
  both, with a step skipped on the first attempt in its run `skipped`. No catalogue case gates
  N5, so it has no recording-batch manifest; `flowsuite.json` requires
  its runs.

- F2a exposes the engine's hook seams to Go hosts as `wl.Hooks` and runs
  the host contract's mandatory provided sites with `wl.Options.Providers`.
  `wltest check` gives its runs the effect sink and run groups through the
  same `wl.Hooks`, so the runner takes no path a host does not have.
  ADR 0008 does not say which loop or branching module a selector's
  `iterations` or `branches` narrow to; F2a reads them as the innermost
  one around the site. A provided site's record says it ran, with
  `provided` among its hooks and no job, since the host's value stands for
  the job its target would have run, while an H1-intercepted site's record
  keeps saying it did not. Per-run providers (H13) are a run's own
  resources, variables and resolver over the runtime's, and its steps'
  API reads answer from them; a variable chain now updates the service a
  read answers from, so a step's update in such a run changes the run's
  own variable. The runs a step dispatches still read the runtime's
  providers. Fixtures declare a host contract's provided sites and named
  providers (`provided`, `providers`), and `host/provided` holds the
  run-time half of ADR 0002's policy case as expects, since the oracle
  would run the real targets. F1's open gap is closed: the engine registers
  every loop iteration, branch and subflow as a flow job of the run's tree
  while it runs, so a dispatch whose `parent_job` names one gets its path.

- F2b exposes H3, H4, H5, H7 and H10 through `wl`: write-capable selectors
  and `BeforeFirstWrite`, and `FatalErrorNames`, on `wl.Hooks`; the
  pre-write deadline, the clock (`RecordClock` for quick waits) and the
  jitter on `wl.RunOptions`; the latch through `RunHandle.Latch` and every
  hook's error. All of them are off by default. Its exit gate's
  latch-precedence cases are data files of a new conformance level,
  `conformance/hooks` ([0003](0003-semantics-placement.md)), run through
  `wl`'s public hooks on flows of identity modules; H3, which only a job
  that runs a step reaches, is checked by `wl`'s tests on the Bun harness.
  H3 applies to a write-capable subflow call as to a leaf's launch, asked
  before its child flow starts, and its approval is the commit point: the
  run reports `MayHaveMutated` and the pre-write deadline stops applying
  from it on, even when the launch it approved is then stopped by H12 or
  by its arguments' resolution, a conservative reading of H4's "until H3
  fires". The cases read FX-19's error stop as a `stop_after_if` with an
  `error_message`, which is the only completion the cancellation's
  exception lets through; a plain failure under cancellation never starts
  the failure module. A passed deadline, hard or pre-write, names
  Windmill's default reason and canceler, since a deadline has neither.

- F3a makes host mode a runtime of `wl`: `Options.Host` opens a bundle
  directory in host mode, completes the handshake with the host, and
  serves its `run` frames with `Runtime.Run`, so `windmill-lite host`, the
  engine the Bun host SDK starts, runs real bundles on the code path every
  other adapter runs. It replaces the walking skeleton's loader in host
  mode; the loader stays in `internal/engine` for its own tests until a
  cleanup removes it. The working-directory checks run on both sides, the
  SDK's and the engine's, on the shared cases of
  `conformance/protocol/host`; those cases, with the engine's own refusal
  through `windmill-lite host` and a codebase script that reads its asset
  in a compiled host, are the exit gate. ADR 0004 records the protocol
  F3a added (`workdir`, `run_settled`, the host's `drain`, `run_done`'s
  new fields) and ADR 0009 the engine path's pairing. The recycle
  triggers of host mode (resident memory, modules loaded) are reported by
  the SDK's `health()` and left to the host; a retired-entry trigger waits
  for the pooled-connection layer it would count.

- F2c gives TypeScript hosts F2a's seams over `host_call`: a host plugin
  declares its hooks and their selectors in its harness's hello, and the
  runtime turns the declaration into the same `wl.Hooks` and providers a
  Go host passes, whose callbacks send `host_call` frames, so selectors
  match in Go only and both kinds of host take one path through the
  engine. ADR 0012 gave the row no exit gate; F2c defined the one above,
  whose cases (`conformance/plugins`, a level ADR 0003 records) run three
  ways with one expected outcome. In `warm` the plugin is a module the
  runtime names at `Open` (`wl.Options.Plugin`), which every harness
  process loads; bundling plugins into a bundle's `plugins/` (ADR 0010)
  waits for a milestone that needs it. `around` runs in the harness,
  around the invocations whose sites its selectors match, which the
  engine names on the `invoke`. A plugin's platform calls outside an
  invocation, and cacheable provider answers (ADR 0007), are not in F2c.
  F2b's settings reach TypeScript hosts here too: a plugin declares
  write-capable selectors, `beforeFirstWrite` and fatal error names; a
  host-mode `run` frame carries the run's budgets and latches; and step
  code calls `wl.latch` and `wl.signal`, added to the gate because F2b
  left them to F2c. F3a's two follow-ups, the SDK–engine version pairing and the
  retired-entry recycle trigger, are not `host_call` matters and go to
  F3b.

- F3b is the host-mode integration gate. The flow suite runs in host mode
  through `wltest check --mode host`: each fixture's runs go to a Bun host
  that embeds the SDK and runs their steps in its own process, over the
  host-mode transport. The checker keeps the engine (`wl.Options.Host`) in
  its own process, so that it reads each run's trace and gives it the
  hooks it gives warm and fresh runs; the SDK's spawned engine is the
  checker's binary, which hands the host's sockets over and waits, and the
  watchdog is off there. The watchdog, `watchdog/finite-cpu`,
  never-settling-step and resource-release tests, and the H11 policy case,
  run the real topology, a compiled host spawning `windmill-lite host`,
  under a supervisor that only restarts it on exit. F3b also closed F3a's
  two follow-ups: the SDK–engine version pairing
  ([0009](0009-host-adapters.md)) and the `retired` recycle trigger, which
  counts through the host's pooled-connection layer since windmill-lite
  has none yet. F2c's two open questions are not the gate's: an abort
  signal for plugin hooks needs a `host_call` cancel frame, and a seed in
  the host's run frame matters to replay, neither of which the gate's
  cases use, so both stay follow-ups.

- F3h came from an embedder building its real folder. A host contract can
  list a file of a codebase entry's `relative_path` outside the sync root,
  which esbuild builds as `outside/<n>/...` ([0010](0010-packaging.md)).
  The esbuild plugin now loads a listed file's shim in place of the file
  instead of resolving every import a second time, which had made a
  codebase build 16 to 60 times slower with any module listed. The lost
  load effects were Bun 1.3.10's: its bundler drops a module that a
  `sideEffects: false` package re-exports when a plugin's resolve filter
  matches the package's import, which hit a listed module's copy, so the
  Bun helper's filter now matches only the specifiers it resolves.

- F8a is a design milestone, added after F3b when an embedder measured that
  `warm` does not fit its memory budget: a compiled host serving 53
  CommonJS codebase scripts that bundle one large dependency closure each
  peaked at about 2.3 GiB with 34 modules loaded, against 650 MiB. Its
  record, [0013](0013-shared-module-code.md), weighs shared chunks
  ([0006](0006-module-state.md)'s option 4), host-provided packages, one
  instance per package and shared code with an instance per script, and
  chooses the last (D18), which keeps each script's module state its own.
  Its tests are the record's evidence: `TestModuleMemory` in
  [cmd/windmill-lite](../../cmd/windmill-lite/README.md) runs the public
  synthetic reproduction in host mode in three layouts, and
  `TestModuleMemoryClosure` the closure shaped like the embedder's, behind
  `WL_MEMORY_PROBE`; F8b replaces their shared-code prototype with the real
  layout.

## Spec IDs relied on

The milestone rows cite the finding families each one encodes; the records
they implement list their own.

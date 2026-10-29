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
internal/platform/          service interfaces, local defaults, interpolation walker (0007)
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
| N1b | engine errors: `continue_on_error`, failure module and recovery, error shapes, chaining errors, final result, cancellation outcome | N1a, E3c | the E3c cases; E3a's failure-module and error-route cases (JE-17/a, JE-26/b–c, JE-35/a–d); the E3b cases gated on N1b (FX-6/b, FX-7/d, FX-10/a, FX-19/e–g and /i, FX-21/a and its failure-module variant) | 6 | yes |
| N2 | engine waits and time: `sleep`, `timeout` and its messages, retry arithmetic, jitter and argument reuse, deadlines, the clock | N1a | FX-23 to FX-29 cases; E3a's wait, timeout and retry cases (JE-2/a, JE-22/a–b, JE-28/a–c, JE-29/a, JE-32/a–b, JE-V2/a and /c); E3b's FX-19/h | 6 | yes |
| N3a | for loops: sequential, collapsed, parallel, `parallelism` | N1a, E3d | the E3d for-loop cases; E3a's for-loop cases (JE-9/e–f, JE-19/a and /c, JE-26/d, JE-27/a–b on loops, JE-30/a–b, JE-36/a–c and /e, JE-37/a, JE-38/c, JE-39/a, JE-V1/a–e) | 6 | yes |
| N3b | while loops, `branchone`, `branchall` | N1a, E3d | the E3d while and branch cases; E3a's while and branch cases (JE-24/a, JE-27/a–b on `branchall`, JE-31/a–b, JE-36/d and /f); E3b's FX-20/a and its error-stop variant | 6 | yes |
| G1 | in-repository benchmarks of [0004](0004-harness-protocol.md) and [0005](0005-json-step-boundaries.md), run spans | A1, C1, N1a | a recorded baseline | 6 | yes |
| PF1 | facade router, interception, attribution and the transport restriction, the 501 policy, job identity | C3b, N1a | the attribution test of [0007](0007-platform-interface.md) | 6 | yes |
| N3c | subflows by path, `results` across scopes, stop propagation, failure modules in bodies | N1b, N3a, N3b | CF-16 to CF-20, CF-29 and CF-V cases; E3a's cross-scope cases (JE-19/b, JE-38/d) | 7 | yes |
| NI | integration slice of N1b, N2 and N3: retries, sleeps, cancellation and failure inside containers | N1b, N2, N3a, N3b | CF-21 to CF-28 cases; the E3b cases that need both N1b and N2 (FX-6's retry variant, FX-8/b, FX-19/d) | 7 | yes |
| PF2 | resources, variables, secrets, the interpolation walker | PF1 | WA-10 to WA-12 and WA-40 to WA-42 cases; the E3b FX-42/h case; E3a's provider cases (JE-11/a, JE-20/a–b, JE-38/b, JE-41/a–c, JE-42/a and /e, JE-V3/a) | 7 | yes |
| PF3 | flow user state, object store, dispatcher, job registry | PF1 | WA-5, WA-6, WA-14 and WA-16 cases; the dispatch-target restriction case | 7 | yes |
| E4b | the stub acceptance cases on windmill-lite | E4a, E1, N3c, NI | the cases match the oracle's | 8 | yes |
| N4 | the `cache_ttl` result cache with the host store interface | N3c | the E3d FX-40 cases | 8 | yes |
| F1 | `wl`: `Open`, `Run`, `BuildInfo`; `windmill-lite run` | F4, N3c, NI, PF2, PF3 | the flow suite through `wl` in `fresh` and `warm`, including E3a cases that need features of two milestones neither of which depends on the other (JE-V2/b: retry and argument resolution), and E3b's FX-17/b (retry and a variable) | 8 | yes |
| F2a | hooks as Go callbacks: H1, H2, H6, H8, H9, H12, H13, and mandatory provided sites | F1 | hook cases; the run-time half of the provided-site policy case of [0002](0002-ir-and-loader.md) | 9 | yes |
| F2b | budgets and latches: H3, H4, H5, H7, H10 | F1 | latch-precedence cases | 9 | yes |
| F3a | host-mode transport: `run` frames, host-provided-module registration, scope capture, health, drain, the watchdog option, the working-directory checks | F1, C2b, C3b | `workdir_not_writable` and `workdir_mismatch` cases; a codebase script reads its extracted asset in host mode | 9 | yes |
| F5 | go-plugin adapter, `runner.proto`, `wl/plugin` | F1 | — | 9 | no |
| F7b | HTTP adapter | F1, E3e | the E3e FX-36 and FX-37 HTTP cases | 9 | no |
| F7c | `windmill-lite gen go` | F1 | — | 9 | no |
| F7d | `windmill-lite gen ts` | F1 | — | 9 | no |
| F6 | `pack --single` and `image` | F1, F3a | the image runs on its base: empty for the static engine; for the compiled Bun harness, the smallest base that provides the C library it links, recorded in the manifest | 10 | no |
| F2c | host plugins over `host_call`: TypeScript hooks, `around`, the provider bridge | F2a, F3a | — | 10 | yes |
| F7a | plain gRPC adapter | F5 | — | 10 | no |
| F3b | **host-mode integration gate** | F2b, F2c, F3a, E1 | on Bun 1.3.10, with a public test supervisor that only restarts on exit: the flow suite in `warm` and in host mode; the never-settling-step, watchdog, `watchdog/finite-cpu` (host mode) and resource-release tests of [0004](0004-harness-protocol.md); the H11 policy case | 11 | yes |
| R1 | `internal/platform/remote` (hybrid dispatch) | PF3, **H-b** | — | — | no |

**L1, further harnesses** (Python, then Deno, Go, Bash) and the remaining
`later` findings, is a placeholder for a programme, not a milestone. It is
planned through its own records and split into milestones then.

**Critical path to the first host integration:** K1 and A2, A3 → A4 → A5;
K1, A1 and E2 → E3a → B2a, with B1a → B2b; C1 → C2a and W1 → C3a → E1; all of
them, with E3b, into N1a → N1b (with E3c), N3a and N3b (with E3d) → N3c and
NI → F1 → F2a, and F3a (with C2b) → F2c → F3b. W1 retires the process-model and
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

## Spec IDs relied on

The milestone rows cite the finding families each one encodes; the records
they implement list their own.

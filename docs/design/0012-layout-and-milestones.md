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
cmd/windmill-lite/          the CLI: build, run, gen, pack, image, verify, serve
cmd/wltest/                 conformance and oracle runner: check, record, diff, stub
wl/                         the public Go library API (0009); the only exported engine package
wl/plugin/                  go-plugin client and server helpers for hosts (0009)
internal/jsonval/           JSON text values, jsonb canonicaliser, NUL checks (0005)
internal/yamlx/             YAML 1.2 core scalar resolution over a node parser (0002)
internal/openflow/          OpenFlow types, effective decoder, vendored schema, coverage test (0002)
internal/syncfolder/        folder walk, wmill.yaml, filters, paths, inline scripts, locks, data files (0002)
internal/ir/                IR types, compiler, validation (0002)
internal/expr/              expression pipeline: shortcuts, rewrites, binding, serialisation (0003)
internal/expr/quickjs/      vendored quickjs-ng source, the native (cgo) and wazero backends, the pool (0003)
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
conformance/                flows/, steps/, exprs/: the shared cases (0003)
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
interfaces. **External gates** are inputs from outside the repository's code:
S1 is a request to the semantics team listed in the [README](README.md), and
H-b is the human decision on hybrid dispatch. **MVP** marks what the first
host integration needs. Engine milestones are numbered N and platform
milestones PF, so that no milestone id looks like a deviation id of
[0001](0001-equivalence-contract.md).

| Id | Milestone | Needs merged | Exit gate (beyond its own tests and cases) | Wave | MVP |
|---|---|---|---|---|---|
| K1 | **Contracts**: the `jsonval` value type, the expression IR and evaluator interface, the step-executor interface, the internal hook interface with no-op defaults, precedence and the `latched`/`halted` statuses, run-trace and observation types, and the site-path encoder | — | the encoder's negative tests ([0011](0011-differential-testing.md)) | 0 | yes |
| A1 | `internal/jsonval`: producer and stored texts, canonicaliser, NUL checks, the test implementation | K1 | FX-42, JE-44 and WR-10 cases | 1 | yes |
| A2 | `internal/yamlx`: the YAML dialect of SY-9 | — | SY-9 fixtures | 1 | yes |
| A3 | `internal/openflow`: types, effective decoder, error prefixes and server checks, the vendored schema with its notice, the coverage test | — | OF cases | 1 | yes |
| B1a | `internal/expr/quickjs`: vendored quickjs-ng `be664ca1` with notices, the native backend, the pool, limits, bytecode cache, interrupts, memory recycling | K1 | JE-1 to JE-3 cases; an interrupt test; a recycling test | 1 | yes |
| C1 | `internal/harness` and `harnesses/bun/runtime` skeleton: frames (with `part`), handshake, supervision, pools, an echo step, golden frames | K1 | protocol golden tests on Bun 1.3.10 | 1 | yes |
| E2 | `internal/oracle` and `wltest record`: compose with pinned digests, bootstrap, CLI push, deployment gate, capture, walker, normaliser; the oracle workflow; image lint; manifest guard | K1 | one recorded smoke fixture in CI | 1 | yes |
| W1 | **Walking skeleton**: one sequential flow, one Bun step, a static and a JavaScript transform, the real protocol, host mode through a minimal SDK, one H1 intercept, one H11 read through one host-provided module whose shim is written by hand (C2a later generates them) | K1, A3, B1a, C1 | on Bun 1.3.10: the Q-WA-4 env-view test 16/16; the host-provided-module ALS test 16/16 in a compiled host; one detached rejection does not end the host; the host-mode never-settling-step and CPU-bound-step tests of [0004](0004-harness-protocol.md); a first per-step latency figure | 2 | yes |
| E3a | Record the contested cases and the core FX, CF and JE catalogue cases on the oracle; update [0001](0001-equivalence-contract.md) §6 | E2 | recordings committed with provenance | 2 | yes |
| A4 | `internal/syncfolder`: SY-1 to SY-15, SY-V1 to SY-V4 | A2, A3 | SY cases | 2 | yes |
| A5 | `internal/ir`: normalisation to the deployed form, the IR, collapse, harness classes, the host contract and provided sites, duplicate-id occurrences, validation and policy errors | K1, A3, A4 | the provided-site policy case of [0002](0002-ir-and-loader.md) | 3 | yes |
| B2a | `internal/expr` textual pipeline: trimming, shortcuts, rewrites, statement mode, binding analysis, against a fake engine | K1, A1 | JE-5 to JE-14 cases | 2 | yes |
| C2a | `harnesses/bun/build`: bundling per WR-26 to WR-29, the export list and signatures, annotations, workflow-as-code detection, the `Bun.env` rewrite, host-provided-module shims | C1 | WR-6 to WR-8, WR-31, WR-32 cases; the `host_module_live_binding` refusal | 2 | yes |
| B2b | `internal/expr` on the real engine: serialisation, error texts, the expression runner | B1a, B2a | the expression suite; the budget gate of [0003](0003-semantics-placement.md) | 3 | yes |
| B1b | the wazero backend and the reproducible WASM build | B1a | the expression suite byte-identical on both backends | 2 | no |
| C2b | codebase bundling through esbuild's Go API, assets, the CLI parity check | C2a | D13 cases (Node against Bun); the parity check or its documented fallback | 3 | yes |
| C3a | Bun step runtime core: entrypoints and binding, hydration, the Bun boundary, outcome mapping, exit interception, process-level handlers and attribution, the working directory | C1, C2a, W1 | step cases; the alternate-entrypoint and arrow-export cases | 3 | yes |
| C3b | per-invocation env, log and `fetch` views, child-process wrapping, `fresh` and `warm` modes | C3a | the step suite in `fresh` and `warm`; the D9 to D11 cases in both modes | 4 | yes |
| C3n | native boundary mode | C3a, **S1** | native cases from the reference | 4 | yes, for hosts with native steps |
| N1a | engine core, sequential: transforms, the `results` routes, `skip_if`, stops, identity, mock, the hook seams of K1 | K1, A5, B2b, E3a | the recorded FX-6 to FX-10 and FX-16 to FX-21 cases | 4 | yes |
| F4 | `internal/bundle`, `windmill-lite build` and `verify`: format, manifest, digest, reproducibility | A5, C2a | two builds of one fixture give one digest | 4 | yes |
| N1b | engine errors: `continue_on_error`, failure module and recovery, error shapes, chaining errors, final result, cancellation outcome | N1a, E3a | the recorded FX-12 to FX-15 and FX-30 to FX-38 cases | 5 | yes |
| N2 | engine waits and time: `sleep`, `timeout` and its messages, retry arithmetic, jitter and argument reuse, deadlines, the clock | N1a | FX-23 to FX-29 cases | 5 | yes |
| N3a | for loops: sequential, collapsed, parallel, `parallelism` | N1a, E3a | the recorded CF-2 to CF-8 cases | 5 | yes |
| N3b | while loops, `branchone`, `branchall` | N1a, E3a | the recorded CF-10 to CF-15 cases | 5 | yes |
| E1 | `wltest check` for flows in `fresh` and `warm`, first fixtures per area | C3b, N1a | the first fixtures pass in both modes | 5 | yes |
| G1 | in-repository benchmarks of [0004](0004-harness-protocol.md) and [0005](0005-json-step-boundaries.md), run spans | A1, C1, N1a | a recorded baseline | 5 | yes |
| N3c | subflows by path, `results` across scopes, stop propagation, failure modules in bodies | N1b, N3a, N3b | CF-16 to CF-20, CF-29 and CF-V cases | 6 | yes |
| NI | integration slice of N1b, N2 and N3: retries, sleeps, cancellation and failure inside containers | N1b, N2, N3a, N3b | CF-21 to CF-28 cases | 6 | yes |
| PF1 | facade router, interception, attribution and the transport restriction, the 501 policy, job identity | C3b, N1a | the attribution test of [0007](0007-platform-interface.md) | 5 | yes |
| E3b | the remaining recordings and the probe-catalogue reconstruction (Q-OR-16) | E3a, E1 | — | 6 | yes |
| E4 | `wltest stub` and the replay service | E1, E2 | the stub acceptance cases of [0011](0011-differential-testing.md) | 6 | yes |
| PF2 | resources, variables, secrets, the interpolation walker | PF1 | WA-10 to WA-12, WA-40 to WA-42 cases | 6 | yes |
| PF3 | flow user state, object store, dispatcher, job registry | PF1 | WA-5, WA-6, WA-14, WA-16 cases | 6 | yes |
| N4 | the `cache_ttl` result cache with the host store interface | N3c | FX-40 cases | 7 | yes |
| F1 | `wl`: `Open`, `Run`, `BuildInfo`; `windmill-lite run` | F4, N3c, NI, PF2, PF3 | the flow suite through `wl` in `fresh` and `warm` | 7 | yes |
| F2a | hooks as Go callbacks: H1, H2, H6, H8, H9, H12, H13, and mandatory provided sites | F1 | hook cases; the `missing_provider` and `provider_declined` cases | 8 | yes |
| F2b | budgets and latches: H3, H4, H5, H7, H10 | F1 | latch-precedence cases | 8 | yes |
| F3a | host-mode transport: `run` frames, host-provided-module registration, scope capture, health | F1, C3b | — | 8 | yes |
| F5 | go-plugin adapter, `runner.proto`, `wl/plugin` | F1 | — | 8 | no |
| F6 | `pack --single` and `image` | F1, F3a | — | 9 | no |
| F2c | host plugins over `host_call`: TypeScript hooks, `around`, the provider bridge | F2a, F3a | — | 9 | yes |
| F7a | plain gRPC adapter | F5 | — | 9 | no |
| F7b | HTTP adapter | F1 | — | 8 | no |
| F7c | `windmill-lite gen go` | F1 | — | 8 | no |
| F7d | `windmill-lite gen ts` | F1 | — | 8 | no |
| F3b | **host-mode integration gate** | F2b, F2c, F3a, E1 | on Bun 1.3.10: the flow suite in `warm` and in host mode; the host-mode recovery tests; the H11 policy case | 10 | yes |
| R1 | `internal/platform/remote` (hybrid dispatch) | PF3, **H-b** | — | — | no |

**L1, further harnesses** (Python, then Deno, Go, Bash) and the remaining
`later` findings, is a placeholder for a programme, not a milestone. It is
planned through its own records and split into milestones then.

**Critical path to the first host integration:** A2 and A3 → A4 → A5, and
B1a with B2a → B2b; both, with E2 → E3a supplying the recorded expectations,
into N1a → N1b, N3a, N3b → N3c and NI → F1 → F2a and F3a → F2c → F3b. Alongside
run C1 → C2a → C3a → C3b and W1, which retires the process-model and
pinned-Bun risks in wave 2, before the engine milestones build on them. A
host whose flows use native steps also waits for S1 and C3n; nothing else
waits on an external gate except R1.

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

## Spec IDs relied on

The milestone rows cite the finding families each one encodes; the records
they implement list their own.

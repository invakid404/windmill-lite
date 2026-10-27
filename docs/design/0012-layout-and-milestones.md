# 0012 · Go package layout and implementation milestones

**Status:** proposed · **Topic:** where code goes, and the sequence of
pull-request-sized milestones to the first release, with what can run in
parallel.

## Context

[AGENTS.md](../../AGENTS.md) holds back new packages until the design decides
the layout. Records [0001](0001-equivalence-contract.md) to
[0011](0011-differential-testing.md) fix the components; this record places
them and orders the work. Each milestone is one reviewed pull request: one
component, its README (the documentation gate requires one per module
directory), its tests, and the conformance cases it can already pass.

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
internal/expr/quickjs/      QuickJS-ng WASM build and the wazero engine with its context pool (0003)
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
                            host/ (Bun host SDK), test/ (step-case runner)
proto/windmill_lite/v1/     runner.proto (0009)
conformance/                flows/, steps/, exprs/: the shared cases (0003)
```

Everything except `wl/` and `wl/plugin/` is internal, so the engine can change
without breaking hosts. The Bun harness and its host SDK are consumed from
this repository; nothing is published to a package registry.

### Milestones

"Wave" is the earliest point a milestone can start; milestones in the same
wave run in parallel. **MVP** marks what the first host integration needs.

| Id | Milestone | Depends on | Wave | MVP |
|---|---|---|---|---|
| A1 | `internal/jsonval`: producer and stored texts, canonicaliser, NUL, the test implementation; cases from FX-42, JE-44, WR-10 | — | 1 | yes |
| A2 | `internal/yamlx`: YAML dialect of SY-9 with fixtures | — | 1 | yes |
| A3 | `internal/openflow`: types, effective decoder, error prefixes and server checks, vendored schema with its notice, coverage test; OF cases | — | 1 | yes |
| B1 | `internal/expr/quickjs`: reproducible WASM build of quickjs-ng `be664ca1` (wasi-sdk, notices, digest check in CI), wazero engine, fresh-context pool, limits; JE-1 to JE-3 cases | — | 1 | yes |
| C1 | `internal/harness` and `harnesses/bun/runtime` skeleton: frames, handshake, supervision, pools, an echo step; golden frames shared by Go and Bun tests | — | 1 | yes |
| E2 | `internal/oracle` and `cmd/wltest record`: compose with pinned digests, bootstrap, CLI push, deployment gate, capture, walker, identity encoder, normaliser; label-triggered workflow; image lint; manifest guard | — | 1 | yes |
| A4 | `internal/syncfolder`: SY-1 to SY-15 and SY-V1 to SY-V4 | A2, A3 | 2 | yes |
| A5 | `internal/ir`: normalisation to the deployed form, IR, collapse, languages and segments, validation and policy errors, readings and deviations | A3 (A4 for folder tests) | 2 | yes |
| B2 | `internal/expr`: trimming, shortcuts with readings, rewrites, statement mode, binding, serialisation, error texts; the `exprs/` runner | A1, B1 | 2 | yes |
| C2 | `harnesses/bun/build`: bundling per WR-26 to WR-29 and SY-8, signature and annotation parsing, workflow-as-code detection, native API check | C1 | 2 | yes |
| C3 | `harnesses/bun/runtime`: binding, hydration, both boundary modes, error outcomes, exit interception, per-invocation env, logs and fetch (the Q-WA-4 gate on the pinned Bun), module-state modes; the `steps/` runner in Bun and in Go | C1, C2 | 3 | yes |
| D1 | `internal/engine` core: sequential flows, transforms, results routes, `skip_if`, stops, identity, mock, `continue_on_error`, failure module, error shapes, chaining errors, final result, run trace with site paths | A5, B2 (C3 or a fake executor) | 3 | yes |
| F4 | `internal/bundle` and `windmill-lite build`: bundle format, build orchestration, manifest, digest, reproducibility check | A5, C2 | 3 | yes |
| D2 | engine waits and time: `sleep`, `timeout` and its messages, `retry` arithmetic, jitter and argument reuse, cancellation, the injectable clock | D1 | 4 | yes |
| D3 | engine containers: for loops (sequential, collapsed, parallel, `parallelism`), while loops, `branchone`, `branchall`, subflows by path, results scoping, stop propagation, failure modules in bodies | D1 | 4 | yes |
| D4 | `internal/platform` and `facade`: services, interpolation walker, router, interception, attribution, the 501 policy, flow user state, object store, local dispatcher, job identity | C3, D1 | 4 | yes |
| E1 | `cmd/wltest check` for flows, and first fixtures per area | C3, D1 | 4 | yes |
| G1 | in-repository benchmarks of 0003 to 0005, and run spans | A1, B1, C1 (D1 for end to end) | 4 | yes |
| D5 | `cache_ttl` result cache (step and flow), per process, with the host store interface | D3 | 5 | yes |
| F1 | `wl`: `Open`, `Run`, `BuildInfo`; `windmill-lite run` | F4, D2, D3, D4 | 5 | yes |
| E3 | rebuild the probe catalogue as fixtures, record the core fixtures, settle the contested readings, flip defaults where recordings say so | E1, E2 | 5 | yes |
| F2 | host hooks H1 to H13, including host plugins over `host_call` | F1 | 6 | yes |
| F3 | Bun host SDK (host mode): `run` frames, scope capture (gate on the pinned Bun), in-process plugins | F1, C3 | 6 | yes |
| F5 | go-plugin adapter, `runner.proto`, `wl/plugin` | F1 | 6 | no |
| F6 | `windmill-lite pack --single`, `image`, `verify` | F4, F1 | 6 | yes |
| F7 | plain gRPC and HTTP adapters, `windmill-lite gen` | F5 | 7 | no |
| L1 | further harnesses (Python, then Deno, Go, Bash) and the `later` findings of the reference | D4 | 5 | no |
| R1 | `internal/platform/remote` (hybrid dispatch) | D4 and the human decision on Q-OF-7(b) | — | no |

**Critical path to the first host integration:** A3 → A5 and B1 → B2, both
into D1, then D3 → F1 → F2 and F3, with C1 → C2 → C3 alongside. Wave 1 has six
independent milestones; the later waves have between one and five each.

go-plugin (F5) is the first adapter for Go hosts, as planned, but it is not
on the critical path of a Bun host, which uses host mode (F3).

## Consequences

- Six milestones can start the day this design merges.
- Every component lands with its README and its conformance cases, so the
  documentation gate and the suite grow together.
- The hybrid adapter is the only milestone held by a human decision.

## Spec IDs relied on

The milestone rows cite the finding families each one encodes; the records
they implement list their own.

# windmill-lite design

**Current state:** first edition, proposed. Milestone A3 implements the
OpenFlow decoder of [0002](0002-ir-and-loader.md) in
[internal/openflow](../../internal/openflow/README.md), milestone A2 its YAML
dialect in [internal/yamlx](../../internal/yamlx/README.md), milestone A1 the
jsonb canonicaliser of [0005](0005-json-step-boundaries.md) in
[internal/jsonval](../../internal/jsonval/README.md), milestone A4 its
sync-folder loader in
[internal/syncfolder](../../internal/syncfolder/README.md), milestone A5 its
compiler in [internal/ir](../../internal/ir/README.md), milestone K1
the contracts of [0012](0012-layout-and-milestones.md) (the shared types and
interfaces under `internal/`), milestone B1a the QuickJS-ng engine of
[0003](0003-semantics-placement.md) in
[internal/expr/quickjs](../../internal/expr/quickjs/README.md), milestone
B2a the textual stages and shortcuts of its expression pipeline in
[internal/expr](../../internal/expr/README.md), milestone B2b that
pipeline's program, serialisation and error texts on the engine, held to
recording batch E3a's expression cases and to the budget gate, milestone
C1 the harness protocol of [0004](0004-harness-protocol.md) in
[internal/harness](../../internal/harness/README.md) with a skeleton Bun
harness, milestone C3a its Bun step runtime core (Windmill's wrapper rules,
the `process.exit` interception, the attribution of process-level errors)
in [harnesses/bun](../../harnesses/bun/README.md) and the step level of
[0003](0003-semantics-placement.md)'s conformance suite, run by
[cmd/wltest](../../cmd/wltest/README.md), milestone C3b the per-invocation
log, `fetch` and child-process views, the settle-time fence that narrows D9
of [0001](0001-equivalence-contract.md), and `fresh` mode of
[0006](0006-module-state.md), milestone C3n the native boundary mode of
D5 in [0001](0001-equivalence-contract.md) (each `nativets` and `//native`
step in a realm of its own, behind a boundary to the web APIs that fails
closed), milestone C2a the Bun side of the build of
[0010](0010-packaging.md) in [internal/bundle](../../internal/bundle/README.md)
and [harnesses/bun/build](../../harnesses/bun/build/README.md), milestone
E2 the oracle harness of
[0011](0011-differential-testing.md) in
[internal/oracle](../../internal/oracle/README.md), on which recording batches
E3c, E3b and E3d recorded the flow-execution error cases, its core and the
control-flow cases and settled contested rules of
[0001](0001-equivalence-contract.md) §6, recording batch E3a the JE
expression cases and CF-19 and CF-V6, which settled another, recording
batch E3e the remaining recordings (FX-36's HTTP part, the contested
`later` rows, the probe catalogue rebuilt as fixtures, codebase scripts),
and milestone E4a its stub mode. Milestone F4 implements the bundle of
[0010](0010-packaging.md) (the build, the manifest and the bundle digest,
`verify`) in [internal/bundle](../../internal/bundle/README.md) and
[cmd/windmill-lite](../../cmd/windmill-lite/README.md), and put the
expression engine's heap on mimalloc. Milestone W1 is the walking skeleton:
one sequential flow at a time, in a spawned harness and in host mode,
scaffolding that tested the process model on the pinned Bun before the
engine milestones build on it. Milestone E1 runs the flow level of
[0003](0003-semantics-placement.md)'s conformance suite: `wltest check`
([cmd/wltest](../../cmd/wltest/README.md)) compares each flow fixture's run
with its expectation or the oracle's recording, in `warm`. Milestone N1a
is the engine core in [internal/engine](../../internal/engine/README.md),
which replaced the skeleton's interpreter: it runs a compiled program's
flows in sequence, with input transforms, the `results` routes, `skip_if`,
`mock`, identity steps, `stop_after_if` and the hook seams of
[0008](0008-host-hooks.md), and `wltest check` runs the flow fixtures and
the expression cases on it. Milestone N1b adds the engine's errors:
`continue_on_error`, the failure module and recovery, chaining errors, the
error shapes, the final result and the cancellation outcome, with the
location rule of [0011](0011-differential-testing.md) extended to the
chaining errors' suffixes. Milestone N3a adds for loops to it:
sequential, collapsed and parallel, with `parallelism`, each
non-collapsed iteration a child flow of its own. Milestone N3b adds while
loops, which run as N3a's sequential loops do, and `branchone` and
`branchall`, each branch that runs a child flow of its own. Milestone N3c
adds subflows by path, each a child flow with its own results scope,
failure module and `flow_env`, and the failure module's copy in every
container body, with the location rule of
[0011](0011-differential-testing.md) extended to the copies of a
generated error and the not-found location a run declares. Milestone N4
adds the `cache_ttl` result cache: a store behind the result cache
service of [0007](0007-platform-interface.md), which a host may replace
and whose default is in memory, keyed and timed by the rules of FX-40.
Milestone PF1 is the
platform facade of [0007](0007-platform-interface.md) in
[internal/platform/facade](../../internal/platform/facade/README.md): the
router behind the stock client, attribution over the intercepted `fetch`
and loopback TCP, the 501 policy, and job identity in
[internal/platform](../../internal/platform/README.md). Milestone PF2 adds
its resource, variable and secret services, with their local defaults, the
host secret layer and the one interpolation walker for the three
resource-read contracts, in
[internal/platform](../../internal/platform/README.md); the facade serves
the reads, and the engine resolves a leaf's arguments, `flow_env` and the
expression helpers through the same services. Milestone N2 adds
the engine's waits and time: `sleep`, the step timeout and its messages,
retries with their arithmetic, jitter and argument reuse, deadlines (the
monitor of flows that hang between steps, the pre-write deadline of
[0008](0008-host-hooks.md)'s H4) and the clock of H10, with its fixtures
`fx-23` to `fx-29` recorded on the oracle. Milestone PF3 adds the job
registry, flow user state, the object store and the dispatcher's
interface there, their routes in the facade, and in the engine each run's
registration, `flow_user_state()`, a root script's run as a script job
and the default dispatcher, which runs a bundle's roots as separate runs
and refuses any other target (the restriction of
[0001](0001-equivalence-contract.md) §5a). Milestone NI integrates N2's
waits and retries, the cancellation and failures with the containers of
N3a and N3b, with its fixtures `cf-21` to `cf-28` and `waits` recorded on
the oracle, and observations of a container's failed attempts as the
walker of [0011](0011-differential-testing.md) meets them. Milestone E4b
runs the stub mode of [0011](0011-differential-testing.md) on
windmill-lite. `wltest check` serves the oracle's effect sink and replay
service to every run that needs them and holds the stub calls against the
run's trace with the same check the oracle applies to its walk. The stub
acceptance cases, together with a case on codebase scripts, match the
oracle's recordings.
This README covers `docs/design/`: this index and the decision records it
links. The records decide the architecture, the equivalence contract with
Windmill `v1.767.0`, the Go package layout and the implementation
milestones. They build on the
[semantics reference](../semantics/README.md) and cite its finding IDs.

The records are written by the build team under the clean-room rule of
[AGENTS.md](../../AGENTS.md): from the semantics reference, Windmill's public
documentation, the Apache-2.0 parts of Windmill, and observed behaviour.
Where a decision depends on a number, the number was measured with a
throwaway prototype outside this repository, and the record states the
hardware, versions and method. Milestone G1 turns those prototypes into
in-repository benchmarks. The one exception so far is the native pool
under load, measured by milestone B1a's own load test.

## The architecture in one paragraph

A Go **engine** loads a **bundle** built from a `wmill sync` folder and is the
only flow interpreter: it runs Windmill's decision list, containers, retries,
waits, failure modules and cancellation, and evaluates every expression in
QuickJS-ng (the engine Windmill uses), embedded natively.
Steps run in warm **language harnesses** (Bun first), which are step runners:
they bind arguments, hydrate, call the entrypoint, and serialise results and
errors with the language's rules, one framed message per step over a
socketpair. Step code reaches the platform through the stock `windmill-client`,
which talks to a Go facade, intercepted in-process for Bun. Hosts drive the
engine through a Go library and thin adapters (go-plugin, a Bun host mode,
gRPC, HTTP, a CLI) and change runs through a fixed set of hooks.

```mermaid
flowchart LR
  host[host adapter] --> engine[Go engine: flows and expressions]
  engine -->|one frame per step| harness[Bun harness: step runner]
  harness -->|platform calls| facade[Go platform facade]
```

## Decision records

| Record | Decision |
|---|---|
| [0001 Equivalence contract](0001-equivalence-contract.md) | Match every `core` finding for a successfully deployed flow run by path as a root on CE; reject out-of-scope features at load; seventeen named deviations (D1–D17), five of which `fresh` mode removes, and ten restrictions; contested findings recorded on the oracle early, with a stated default until then and no permanent switches; Rust location suffixes stripped only from generated messages. |
| [0002 IR, loader and validation](0002-ir-and-loader.md) | The CLI's YAML dialect over a YAML 1.1 parser, with named refusals; a sync-folder loader that reads each item on demand; hand-written OpenFlow types with Windmill's effective decoder and a schema-coverage test; normalise once to the deployed form; a typed IR with precomputed collapse, compiled expressions and a harness class per leaf; signatures of every export; a host contract whose mandatory provided sites make their targets opaque; codebase scripts bundled with esbuild and run on Bun (D13). |
| [0003 Where semantics live](0003-semantics-placement.md) | One interpreter and one expression pipeline, in Go; QuickJS-ng `be664ca1` embedded natively (a wazero backend deferred), built static with `zig cc` and a stack pinned at 1 MiB on threads the engine creates, its heap on mimalloc; a fresh runtime per evaluation from a bounded pool, bytecode cached, values bound as host strings; a budget gate before the engine merges; harnesses are step runners; no segments; a conformance suite of flow, step and expression cases plus OpenFlow decode cases, jsonb stored-form cases, sync-folder load cases, compile cases, harness protocol cases and Bun build cases, compared once in Go, in `fresh`, `warm` and host mode, with contextual expression cases written as flow fixtures. |
| [0004 Harness protocol](0004-harness-protocol.md) | Length-prefixed JSON frames on a socketpair, with canonical envelopes and large messages in base64 part streams; validated entrypoint names; a shared-process failure model (handlers that never exit, attribution through the invocation's async context without guessing, a `warning` frame for errors no invocation owned, `process.exit` intercepted per invocation, a settle-time fence that clears an invocation's timers, aborts its requests and drops its later output when it settles and reports what it left behind, D9–D11); `fetch` as the only platform transport in warm processes (other transports refused, unrecorded); no in-flight cap unless a host sets one (D15); host mode never tainted, drained by the host, and killed by the engine's watchdog when its event loop stops answering. |
| [0005 JSON at step boundaries](0005-json-step-boundaries.md) | Go holds every value as producer text plus a lazily computed jsonb text and never decodes what it passes through; a routing table says which consumer sees which; one streaming canonicaliser checked against PostgreSQL; host-plugin results serialised by the steps' own function; the normalising walk dropped, because it was not equivalent. |
| [0006 Module state](0006-module-state.md) | Two modes: `fresh` (a process per invocation, Windmill-equivalent) and `warm` (default: one module instance per script for the process lifetime, `globalThis` shared), plus host-provided modules as the one shared exception; `shared` left out of the first edition. |
| [0007 Platform interface](0007-platform-interface.md) | Go service interfaces with local defaults and host replacements; one interpolation walker with three modes and a host secret layer; a facade reached by intercepted `fetch` (warm) or per-invocation tokens (`fresh`), never by a shared token; unsupported routes fail with `UnsupportedPlatformCall`; hybrid dispatch built behind interfaces, its remote adapter held for a licence decision. |
| [0008 Host hooks](0008-host-hooks.md) | Thirteen hooks with typed selectors, as Go callbacks or TypeScript host plugins; mandatory host-provided call sites that fail closed; scopes reach steps through host-provided modules; seams defined in the first engine milestone. |
| [0009 Host adapters](0009-host-adapters.md) | The `wl` Go library is the only engine API; adapters in order: go-plugin, a Bun host SDK, plain gRPC, HTTP, CLI; typed client methods generated from Windmill schemas over a generic wire, bound to the bundle digest. |
| [0010 Packaging](0010-packaging.md) | A bundle with a manifest of digests, tied to one exact engine version; a Bun build helper on the pinned Bun `1.3.10`; host-provided modules through a registry (D12), with one shim generator and one define table and support module for both bundlers; a bundle digest in every handshake, trace and build-info call; a single executable; an OCI image written without Docker; a `verify` command. |
| [0011 Differential testing](0011-differential-testing.md) | The oracle harness as the reference designs it, in public CI only; a typed, injective site-path encoding that also separates repeated module ids; `record`, `check` and `diff` in three configurations; only context-free cases lifted into one-step flows; a stub mode on the oracle and then on windmill-lite; benchmarks, a bounded-pool benchmark and run spans. |
| [0012 Layout and milestones](0012-layout-and-milestones.md) | `wl/` as the only public package and components under `internal/`; 51 milestones, each one pull request, whose "needs merged" covers everything their exit gates need; a contracts milestone first, a walking skeleton on the pinned Bun in wave 2, oracle recordings in batches, each before the milestone it gates, with a manifest that can name each run's gate. |

## Measurements behind the decisions

Apple M4, macOS, Go 1.27.3, Bun 1.4.2 unless a row names 1.3.10 (details in
each record):

| Question | Result | Used in |
|---|---|---|
| Go ↔ warm Bun round trip, one step | 12.5 µs p50 for 26 B, 18.4 µs for 3 KB, about 250 µs for 100 KB | 0003, 0004 |
| First QuickJS-ng evaluation on a never-used runtime (setup excluded), by bound input | wazero: 53 µs at 50 B, 79 µs at 4 KB, 427 µs at 64 KB; native: 87 µs, 91 µs, 162 µs | 0003 |
| Fresh runtime setup | 221 µs in wazero, 84 µs native | 0003 |
| Bounded pool (wazero prototype), 8 slots, 8 callers with pauses, 4 KB | 120 µs p50, 260 µs p95 | 0003 |
| Native pool (milestone B1a), 8 slots, 8 callers with pauses, by bound value | 4 KB: 95 µs p50, 152 µs p95; 64 KB: 350 µs, 539 µs; 256 KB: 1.32 ms, 2.10 ms | 0003 |
| The budget gate through the pipeline (milestone B2b), a predicate over a results value read from the database, 8 slots, 8 callers with pauses | 4 KB: 121 µs p50, 223 µs p95, 437 µs p99; 64 KB: 313 µs, 449 µs, 573 µs; 256 KB: 1.02 ms, 1.65 ms, 1.84 ms; all inside the gate | 0003 |
| Static musl engine's pool (milestone F4), 8 callers back to back, CPU per evaluation, musl's `malloc` against mimalloc | `linux/arm64`: 2.27 ms and 189 µs at 4 KB, 12.9 ms and 1.46 ms at 256 KB; `linux/amd64`: 1.74 ms and 340 µs, 8.45 ms and 2.82 ms; glibc's `malloc`: 261 µs, 1.69 ms, 433 µs, 3.04 ms | 0003 |
| Normalising walk versus native JSON round trip in Bun | faster, but not equivalent (top-level functions and symbols, own `__proto__` keys) | 0005 |
| Fresh module state per invocation in one process | 0.6–2.1 ms for 0.3–1.3 MB bundles; re-importing leaks module records | 0006 |
| Step code reading a host scope through a separately bundled copy of the module, on Bun 1.3.10 | `undefined`; through a host-provided module, 16 of 16 isolated, compiled host included | 0010 |
| One detached promise rejection in a shared Bun process, 1.3.10 and 1.4.2 | ends the process unless handlers are installed | 0004 |
| A host spinning forever, watched by a Go child that kills it after 1 s of missed heartbeats | killed 1.04 s after its last heartbeat; a restart-on-exit supervisor serves the next call | 0004 |
| esbuild CJS and ESM bundles resolving a host-provided module to the generated shim, Bun 1.3.10 and 1.4.2 | 16 of 16 isolated; `import.meta.env` rewritten in ESM only | 0010 |
| The Q-WA-4 env-view test (milestone W1), Bun 1.3.10, the stock `windmill-client@1.767.0`: 16 concurrent invocations per variant, steps unbundled, bundled by `Bun.build` and bundled with the define rewrite, by namespace and default import | 16 of 16 on every channel (`process.env`, `Bun.env`, `import.meta.env`, a copy of `process.env`, a written variable, a deleted process variable, an unbundled dependency's `Bun.env`, the client's call), with and without the variables in the process environment; `Bun.env` and `import.meta.env` are isolated at run time, without the define rewrite | 0004, 0010 |
| H11 through a host-provided module with a hand-written shim, in a host compiled with `bun build --compile`, Bun 1.3.10 (milestone W1) | 16 of 16 concurrent runs read their own scope; a separately bundled copy reads nothing | 0008, 0010 |
| H11 through an esbuild CommonJS codebase script whose import of the host-provided module resolves to the generated shim, in a host compiled with `bun build --compile`, Bun 1.3.10 (milestone C2b) | 16 of 16 concurrent runs read their own scope, on Bun's CommonJS loader | 0002, 0010 |
| The same esbuild CommonJS codebase bundle on Node 20 and on the Bun harness, with the harness's wrapper rules on both (milestone C2b, D13) | agree on named exports and overrides, `__dirname`, run-time `require` of assets and of the bundle's own file, thrown errors and assets; differ on the runtime's identity, an override naming `default`, and a `package.json` asset declaring `"type": "module"` (Node fails to load the bundle) | 0001, 0002 |
| The pinned `windmill-cli@1.767.0`'s codebase uploads against esbuild 0.28.0 through its Go API with the settings of WR-47 (milestone C2b's parity check) | the same bytes for seven fixtures (each WR-47 setting, an inlined package, an asset in a tar); a text upload's line breaks are CRLF, the form-data encoding's | 0002 |
| `process.exit(code)` on Bun 1.3.10 (milestone C3a) | a safe integer, or a string other than `""` that `Number` reads as one (`" "` is 0), exits with it modulo 256 (`258` exits 2, `-1` and `"-1"` 255, `9007199254740991` 255); `2^53`, `"9007199254740993"`, `1.5`, `NaN`, `"x"`, `""`, `true`, `{}` and `[5]` throw a `RangeError` or `TypeError` before exiting and leave `process.exitCode` as it was | 0004 |
| A step in `fresh` mode (milestone C3b): spawn, handshake, invocation and result of a one-line echo step, 50 sequential runs after 10 warm-up runs, Bun 1.3.10, Apple M4, engine built without the race detector | 21.5 ms p50, 23.4 ms p95, against 48 µs p50 and 131 µs p95 for the same step in a warm process | 0004, 0006 |
| Which child-process calls see a replaced `process.env` on Bun 1.3.10 (milestone C3b) | `child_process.spawn`, `exec` and `Bun.$` read it at the call; `spawnSync`, `execSync` and `Bun.spawn`/`Bun.spawnSync` pass the process's own environment, so the harness gives every call made in an invocation an explicit one | 0004 |
| Patching `node:timers` and `node:child_process` on Bun 1.3.10 (milestone C3b) | assignments to the CommonJS object before a module's first import of the builtin reach its named ESM imports; `module.syncBuiltinESMExports()` does not update a namespace imported earlier | 0004 |
| Which child-process calls honour `detached` on Bun 1.3.10 (milestone FU6) | `spawn`, `fork`, `Bun.spawn` and `Bun.spawnSync` start a detached child in a new session and group; `exec`, `execFile`, `spawnSync`, `execSync` and `execFileSync` ignore it, leaving the child in the caller's group. Every `node:child_process` function starts its child through the global `Bun.spawn` or `Bun.spawnSync`, once, passing `detached` on only from `spawn` and `fork`; `Bun.$` uses neither; `process.kill(-pgid, …)` signals a group | 0004 |
| What the engine can read of another process of its user (milestone FU6) | on macOS `kern.procargs2` gives its executable's path and arguments but not its environment, and `ps -E` shows none either; Linux's `/proc/<pid>/cmdline` and `environ` give both. So a child group's guardian carries its token in its arguments, `sleep 999999999.<token>`, which macOS `sleep` accepts (it refuses `2147483647.<digits>`) | 0004 |
| Whose child a group's guardian is (milestone FU6) | `exec` keeps the shell's pid and its children, so a guardian the shell forks before it execs the step's command is a child of the command, and a command that waits for all its children (`wait()`, `waitpid(-1)`) waits for it until the step times out; a guardian a subshell starts in the background is reparented when the subshell exits, which the shell waits for before the `exec`, so the command has no extra child. Measured under macOS `/bin/sh`, `dash` and `bash` | 0004 |
| Who reaps a child group's guardian (milestone FU6) | its parent once the prologue's subshell exits: PID 1 or the nearest subreaper. Measured in a Linux container with the engine's test process as a subreaper (under `--init`) and as PID 1 (without): the engine is that parent, and each guardian, and each orphan a member leaves, stays a zombie until the engine waits for it; so the engine reaps them, by pid | 0004 |
| `process._getActiveHandles()` on Bun 1.3.10 with a pending timeout and interval (milestone C3a) | an empty list, so leaks are counted in the harness's own per-invocation registry | 0004 |
| The host-mode watchdog (milestone W1), `killAfter` 1 s, a step that spins forever | the host was killed 1.00 s after the step's run was sent; the restarted host served the next run | 0004 |
| Per-step latency of the walking skeleton (milestone W1), a one-step flow with one javascript transform, 300 sequential runs on Bun 1.3.10, engine built without the race detector | spawned warm harness: 56 µs p50, 120 µs p95 per run (invoke to result 31 µs p50); host mode in a compiled host, timed around `run`: 78 µs p50, 126 µs p95 | 0003, 0004 |

## Unsettled findings

Twenty-one findings of the reference were not settled when the design was
written; the reference's revision of 2026-10-28 settled nine of them, the
revision of 2026-10-29 six more, and
[six remain](../semantics/README.md#not-settled). The design does not pick
sides silently: [0001](0001-equivalence-contract.md) §6 lists each contested
rule, its readings, the default and why, and the recording that settles it.
Recording batches E3a to E3e record them on the oracle (E3e holds the
contested `later` cases), each before the milestone that implements its
rules merges; until then the implementation
follows the stated default in one function per rule, and no build switch
keeps both readings. E3c has recorded two of them, FX-10/FX-V6 (Q-FX-15)
and FX-33 (Q-FX-19), both for the stated default. It also found one
confirmed finding contradicted, the id of a failure module copied into an
inline body (FX-34, FX-32). E3b recorded Q-FX-16 (FX-42) and the half of
Q-FX-15 that needs no failure module, also for the stated defaults. Batch
E3d recorded CF-12 and FX-40 as reading (b), the default each already had
(request 6 below). E3a has recorded the CF-19 / CF-V6 dispute
([Q-CF-16](../semantics/control-flow.md#q-cf-16)): the direct lookup is
line-anchored, the stated default (request 7 below). 0001 §6 gives the
results, and the reference now records them (requests 5 to 7 below). E3e
recorded the contested `later` rows, FX-49 (Q-FX-11), FX-36's array result
(Q-FX-12) and WR-16 and WR-28 (Q-WR-15, Q-WR-16), each for its stated
default, and rebuilt the probe catalogue (Q-OR-16); the reference records
those since its revision of 2026-10-29 (request 11 below).

## Requests to the semantics team

Questions reach the semantics team as requests for the reference to cover a
behaviour ([AGENTS.md](../../AGENTS.md)). The design depended on these, and
all but the last two are answered: 1 to 3 by the semantics requests S1 to S3
(pull request #8), 4 to 9 by the reference's revision of 2026-10-28 (SC1,
pull request #36), 10 and 11, and request 1's last question, by its
revision of 2026-10-29 (SC2, pull request #55). Requests 12 and 13 are
open. Each
item names the findings that answer it. Where an answer differs from a
reading a milestone took, that milestone's code follows the answer in a
follow-up; the sync-folder loader and the import base already do (FU4),
and `internal/expr`, the engine, the comparison rule and the Bun harness
follow SC2's answers (FU6).

1. **Native boundary rules** for `bunnative` and `nativets` steps: how a
   result is serialised (in particular object properties holding
   `undefined`), the error object's fields, the timeout text, and which
   globals and APIs the native runtime offers. The reference covers the
   native runtime only in outline ([WR-31](../semantics/languages.md#wr-31))
   and puts it in `later` scope, while the language values are `core` for
   loading ([OF-13](../semantics/openflow.md#of-13)). Until it is covered, the
   harness's native mode ([0004](0004-harness-protocol.md)) has no public
   conformance cases, and milestone C3n cannot merge (gate S1).
   **Answered** by [WR-40](../semantics/languages.md#wr-40) to
   [WR-46](../semantics/languages.md#wr-46): which steps run natively and
   how each is prepared (WR-40), a fresh isolate and argument binding
   (WR-41), the result as `JSON.stringify(value ?? null)` with no replacer,
   so `undefined` properties are dropped (WR-42), the error shapes (WR-43),
   the timeout text and the heap and stack limits (WR-44), the environment,
   globals and `fetch` (WR-45), and streaming (WR-46). Gate S1 is met.
   Milestone C3n implements them as the Bun harness's native mode
   ([0001](0001-equivalence-contract.md) D5). One expectation cannot hold as
   written: a `//native` bundle is built with Bun's syntax minification
   ([WR-27](../semantics/languages.md#wr-27)), which writes
   `throw new Error("x")` as `throw Error("x")`, so WR-43/c's source line for
   that kind lacks `new`; please confirm the catalogue's reading.
   **Answered:** [WR-43](../semantics/languages.md#wr-43) now says that a
   `bunnative` stack line is the minified bundle's, and WR-43/c gives one
   expectation per kind, `throw Error("x")` for `bunnative`, as
   `conformance/steps/bun/wr-43.yaml` already expected.
2. **Codebase bundling details**: the default `format` of a codebase entry,
   the esbuild version the pinned CLI uses, how `inject` and `customBundler`
   are handled, where `assets` land at run time, and the Node version that
   runs CJS bundles. [WR-30](../semantics/languages.md#wr-30) gives the
   outline. **Answered** by [WR-47](../semantics/languages.md#wr-47) to
   [WR-50](../semantics/languages.md#wr-50): esbuild `0.28.0`, `cjs` by
   default, `inject` and the other settings passed through, `customBundler`
   run through the shell with its output as the bundle (WR-48), assets
   beside `main.js` in the job directory, Node 20 with its patch unpinned
   (Q-WR-24). Milestone C2b builds to those settings and checks its
   bundles against the pinned CLI's uploads
   ([0002](0002-ir-and-loader.md), decision 4).
3. **The expression engine's stack limit**: which maximum stack size
   Windmill's QuickJS-ng embedding uses, since stack-overflow depth is
   observable. **Answered** by [JE-48](../semantics/expressions.md#je-48):
   QuickJS-ng's built-in default of 1 MiB, which windmill-lite pins and
   records in the manifest ([0003](0003-semantics-placement.md)).
   Recording batch E3a found the reference's bracket wrong on the oracle: a
   recursion of depth 1 000 already overflows (JE-48/a), so far fewer frames
   fit than the reference implies; request 7 asks why. JE-48 now keeps the
   recorded depths and gives the reason, the engine's C build; how many
   frames fit depends on that build (deviation D17 of
   [0001](0001-equivalence-contract.md),
   [Q-JE-16](../semantics/expressions.md#q-je-16)).
4. **What `jsonb` rejects besides NUL**: PostgreSQL's `jsonb` input also
   rejects a UTF-16 surrogate escape without its pair (`"\ud83d"`) and a
   number outside `numeric`'s range (more than 131072 digits before the
   point or 16383 after it, or an exponent beyond 2³⁰−1 in magnitude). Does Windmill fail a step whose result or
   arguments hold one, as [FX-48](../semantics/execution.md#fx-48) and
   [OR-27](../semantics/oracle.md#or-27) say it does for NUL, and with which
   message? The reference covers only NUL. Until it is answered, `jsonval`
   rejects both as PostgreSQL does, and the eager NUL flag of
   [0005](0005-json-step-boundaries.md) covers NUL alone.
   **Answered** by [FX-51](../semantics/execution.md#fx-51): an unpaired
   surrogate leaves Bun and the engine as a `\uXXXX` escape, reaches
   `jsonb` and fails with `invalid input syntax for type json` (a
   `$jsonvar:` value fails earlier); no JavaScript producer can emit a
   number outside `numeric`'s range.
   [Q-FX-20](../semantics/execution.md#q-fx-20) asks batch E3e to record
   it.
5. **Fold in recording batch E3c.** E3c recorded the catalogue cases of
   FX-12 to FX-15, FX-30 to FX-38 (FX-36's executor part), FX-V6/a and
   FX-10/a on the CE oracle (`conformance/flows/execution`, oracle run
   40685766017; FX-34/a's step-id run, run 40689442792). Every catalogue
   expectation it recorded held but one. A failure module copied into an
   inline body keeps its own id, `failure`, in its status and in
   `WM_FLOW_STEP_ID`; FX-34 (`failure-0-0`) and FX-32 say it is suffixed
   with the parent cursor and the iteration. Please correct both findings;
   [0001](0001-equivalence-contract.md) §6 lists the recorded behaviour.
   The reference still tags the recorded cases `source-derived` or
   `disputed`. Two disputes are now settled for their corrections: Q-FX-15
   (FX-V6) and Q-FX-19 (FX-33, SR-1). The recordings also show behaviour
   the reference does not state:
   - CE refuses a third workspace besides `admins`, so a fresh workspace per
     fixture ([OR-6](../semantics/oracle.md#or-6)) means deleting each one
     after its runs;
   - an empty flow's status holds one `Success` entry with id `failure` and
     a nil job ([FX-3](../semantics/execution.md#fx-3));
   - a completed `branchall` entry has no `branchall` field. Failure-module
     and preprocessor jobs are of kind `preview`, branch bodies
     `flowpreview` and loop bodies `flownode`
     ([OR-11](../semantics/oracle.md#or-11), [OR-28](../semantics/oracle.md#or-28));
   - a failure module whose retry its `skip_if` skips runs an identity job
     that is not marked skipped ([FX-17](../semantics/execution.md#fx-17),
     [FX-32](../semantics/execution.md#fx-32));
   - a preprocessor runs on a run-API start, and its job stores the args
     `{"reason":"PREPROCESSOR_ARGS_ARE_DISCARDED"}`
     ([FX-35](../semantics/execution.md#fx-35));
   - the exact error texts ([Q-FX-13](../semantics/execution.md#q-fx-13)).
     An input-transform error continues `QuickJS evaluation error: Error: …`
     with the engine's frames ([FX-14](../semantics/execution.md#fx-14)).
     Chaining errors end in Rust location suffixes, and at a child flow's
     pickup the message starts `Error: Error: ` and carries two suffixes
     ([FX-15](../semantics/execution.md#fx-15)).

   **Answered.** [FX-34](../semantics/execution.md#fx-34) and
   [FX-32](../semantics/execution.md#fx-32) are corrected (a deployed body's
   copied failure module keeps the id `failure`), with FX-5, FX-45,
   [CF-16](../semantics/control-flow.md#cf-16) and CF-V4; the recorded cases
   are `oracle-observed`; Q-FX-15 and Q-FX-19 are closed for FX-V6 and
   FX-33. The other observations are now in [OR-6](../semantics/oracle.md#or-6)
   (the workspace cap), [FX-3](../semantics/execution.md#fx-3) (an empty
   flow's status), [OR-11](../semantics/oracle.md#or-11) and
   [OR-28](../semantics/oracle.md#or-28) (job kinds), FX-17 (the skipped
   flag), [FX-35](../semantics/execution.md#fx-35) (the preprocessor's
   arguments), and [FX-50](../semantics/execution.md#fx-50), which answers
   Q-FX-13: how a message gains its `Error: ` prefix and location
   suffixes.
6. **Batch E3d's recordings**
   ([conformance/flows/control-flow](../../conformance/flows/control-flow),
   [conformance/flows/execution/fx-40](../../conformance/flows/execution/fx-40)):
   the CF-2 to CF-15 and FX-40 cases they record can become
   `oracle-observed`, and they answer
   [Q-CF-14](../semantics/control-flow.md#q-cf-14) (reading (b)),
   [Q-CF-4](../semantics/control-flow.md#q-cf-4) (both predictions hold),
   [Q-FX-6](../semantics/execution.md#q-fx-6) (yes) and
   [Q-FX-17](../semantics/execution.md#q-fx-17). Three observations go
   beyond the reference's text. In FX-40/d, a deployed flow's inline
   rawscript body without an explicit `path` also shares an entry between
   collapsed iterations; it runs as `flowscript` jobs with one script hash,
   though their paths end in `/loop-<index>`, and the correction expected
   no sharing there (a body pinned by hash shares too, as both readings
   expect). The pinned CLI's `generate-metadata` writes container defaults
   into `flow.yaml`, as [CF-6](../semantics/control-flow.md#cf-6) says
   `sync pull` does; CF-6/a's recording holds its output beside the value
   the API answers after the dependency job. CF-7/d fails at pickup, with
   name `Anyhow` and the message
   `Error: Bad request: Cannot continue on same worker …` plus a location.
   The design waits on none of this.
   **Answered.** The CF-2 to CF-15 and FX-40 cases are `oracle-observed`;
   Q-CF-14 ([CF-12](../semantics/control-flow.md#cf-12), reading (b)),
   Q-CF-4, Q-FX-6 and Q-FX-17 are closed;
   [FX-40](../semantics/execution.md#fx-40) is confirmed, with the shared
   entry of collapsed inline `flowscript` nodes;
   [CF-6](../semantics/control-flow.md#cf-6) gains `generate-metadata`'s
   container defaults; and [CF-7](../semantics/control-flow.md#cf-7) and
   CF-23 state CF-7/d's error.
7. **Fold in recording batch E3a.** E3a recorded the JE catalogue cases
   (`conformance/exprs`, `conformance/flows/je`) and CF-19 and CF-V6
   (`conformance/flows/cf/cf-19`; oracle runs 40687124625, 40688787344 and
   40691336592). The dispute Q-CF-16 is settled for CF-V6: a qualifying line
   of a multiline expression answers by itself. These recordings differ
   from the reference's source-derived expectations; the cases now expect
   what the oracle did and say so. The reference should say which rule
   produces each, and where a reading is not settled:
   - JE-48/a, above: the stack depth that fits. The catalogue's JE-48 cases
     also hold a `;`, so they run in statement mode
     ([JE-13](../semantics/expressions.md#je-13)) and JE-48/c gives `null`;
     the batch adds semicolon-free variants;
   - JE-28/c: the last module's `sleep` is evaluated, because the deployed
     flow ends in the `-v` identity of [OF-17](../semantics/openflow.md#of-17),
     so an invalid final `sleep` fails the flow;
   - JE-35/b: at the first pickup of a root flow, a predicate error is
     `Error: Error: QuickJS evaluation error: …` with two location suffixes;
   - JE-2/a: a root flow's first transform that times out three times is
     canceled after 60 s by the monitor of flows hanging between two steps,
     before the timeout error surfaces;
   - JE-33/a and b: an early stop by `skip_expr` leaves the flow `skipped`,
     where JE-33 says it completes successfully.
   **Answered.** Q-CF-16 is closed: [CF-19](../semantics/control-flow.md#cf-19)
   and CF-V6 are confirmed. [JE-48](../semantics/expressions.md#je-48) keeps
   the recorded depths (request 3);
   [JE-28](../semantics/expressions.md#je-28) and
   [OF-17](../semantics/openflow.md#of-17) evaluate the final `sleep`
   through the `-v` identity; [JE-35](../semantics/expressions.md#je-35)
   follows FX-50's prefixes and suffixes;
   [JE-2](../semantics/expressions.md#je-2) names the 60 s monitor of flows
   hanging between two steps; and [JE-33](../semantics/expressions.md#je-33)
   and [FX-22](../semantics/execution.md#fx-22) leave a flow stopped by
   `skip_expr` `skipped`.
8. **The textual pipeline's unstated details** (milestone B2a). The
   reference leaves these open. Each is decided by the reading named here.
   All but the last are pinned by a source-derived case in
   `internal/expr/testdata/pipeline` (the wrapper by `TestWrapperSingleLine`),
   so a different answer changes one function:
   - [JE-5](../semantics/expressions.md#je-5): which characters trimming
     removes. Read as Unicode White_Space, as Rust's `str::trim` removes.
   - [JE-7](../semantics/expressions.md#je-7): whether the dot form takes an
     empty key (`flow_input.`). Read as yes; only the bracket form needs a
     non-empty key.
   - [JE-9](../semantics/expressions.md#je-9): whether a step id `length`
     (`results.length`) declines like a last segment `length`. Read as no.
   - [JE-11](../semantics/expressions.md#je-11): where a call whose
     parentheses never balance ends. Read as the end of the text.
   - [JE-13](../semantics/expressions.md#je-13): whether `return` followed by
     a newline selects statement mode (read as no, the text lists a space
     or a semicolon), and how the quote scan reads a quote: as opening a
     quote of its kind or closing the one it is in, unless a backslash
     precedes it, so another kind of quote inside a string is text.
   - JE-13 and [JE-14](../semantics/expressions.md#je-14): whether the
     wrapper is an `async function` or an async arrow, which `arguments`
     tells apart. Read as a function. Milestone B2b found it settled by
     E3a's recorded stacks, which place the code at column 28 in
     expression mode and 21 in statement mode: the opening is
     `(async function() { return ` or `(async function() { `.
   - JE-9: where a line ends. Read as at `\n` only, so a line with a
     trailing `\r` or space does not qualify.
   - JE-12: whether a `["…"]` read spans lines. Read as no, as a regular
     expression's `.` does not match a newline.
   - JE-6 to JE-8 and [JE-44](../semantics/expressions.md#je-44): which text
     the shortcuts return for a value read from the database (a member of
     `flow_input`, `previous_result` after a loop or `branchall`): the
     compact stored text or jsonb's spaced output. JE-9's is spaced
     ([0005](0005-json-step-boundaries.md)). The windmill-lite engine
     supplies these texts; the stored text is used until this is answered.
     Two oracle runs would settle it: `timeout: flow_input.o` with `o` an
     object, and `timeout: results.l` right after a loop `l`, whose parse
     errors quote the text.

   **Answered.** JE-5, JE-7, JE-9, JE-12, [JE-13](../semantics/expressions.md#je-13)
   and JE-14 confirm the readings, the function wrapper included;
   [JE-11](../semantics/expressions.md#je-11) confirms the end of the text
   for ASCII and states what non-ASCII text does. JE-6 to
   [JE-8](../semantics/expressions.md#je-8) and
   [JE-44](../semantics/expressions.md#je-44) are corrected: a value read
   from the database is not uniformly the compact stored text. A
   `flow_input` member is `jsonb`'s spaced text, and a `previous_result`
   after a loop or `branchall` is compact text printed again.
   [Q-JE-13](../semantics/expressions.md#q-je-13) asks for the two oracle
   runs. Recording batch E3e made them
   (`conformance/flows/je/shortcut-texts`), and they agree with the
   correction: `flow_input.o` quotes `{"a": 2, "b": 1}`, at the first
   module and after one, and `results.l` right after a loop quotes
   `[{"a":2,"b":1}]` (request 11). Milestone FU3 follows the answers: the
   pipeline cases restate the E3a cases with the texts the worker holds,
   `expr.Scope` and [0005](0005-json-step-boundaries.md)'s routing table
   give those texts, and JE-11's rewrite places its parenthesis at a
   character count applied as a byte offset, failing the evaluation where
   Windmill's evaluator panics ([0001](0001-equivalence-contract.md) §6).
9. **The expression program's unstated details** (milestone B2b). Each is
   decided by the reading named here and pinned by a test in
   `internal/expr`; [internal/expr](../../internal/expr/README.md) says
   where:
   - [JE-21](../semantics/expressions.md#je-21) ([Q-JE-1](../semantics/expressions.md#q-je-1)):
     the text of a thrown value that is not an `Error`. The reference gives
     the engine's debug form; windmill-lite writes the value's string form.
     An oracle run of `throw "s"`, `throw 1` and `throw {}` would settle
     the primitives; an object's form may not be reproducible.
   - [JE-17](../semantics/expressions.md#je-17): whether "or else" in an
     entry's tag (`error.step_id`, else the position) and text
     (`error.message`, else `error.name`) means a nullish or a falsy test.
     Read as nullish.
   - [JE-49](../semantics/expressions.md#je-49): the position quoted for
     `flow_input` and for a member of `flow_env`, which Windmill
     re-serialises (read as counted in the compact text), and which value
     is reported when several are too deep (read as `flow_input`, then the
     context values by name, then `flow_env`'s members by key).
   - [JE-10](../semantics/expressions.md#je-10): whether the lexical
     `results` and `error` are constants or can be assigned. Read as
     constants.
   - [JE-16](../semantics/expressions.md#je-16): whether a `__proto__` key
     of a context value becomes an own property (as `JSON.parse` makes it)
     or the object's prototype. Read as an own property.
   - [JE-15](../semantics/expressions.md#je-15): what Windmill does when
     the user's code replaces `JSON.stringify` so that the value's text is
     not JSON. windmill-lite fails the evaluation with its own text.
   - [JE-48](../semantics/expressions.md#je-48) and
     [JE-1](../semantics/expressions.md#je-1): the overflow depth and the
     transcendental `Math` digits depend on the engine's build, deviations
     D16 and D17 of [0001](0001-equivalence-contract.md). Which C library
     and compiler flags the CE image's engine was built with would say
     whether a calibration could retire them.

   **Answered.** [JE-21](../semantics/expressions.md#je-21) is corrected: a
   thrown value that is not an `Error` reads
   `Exception generated by quickjs: <debug form>`, and Q-JE-1 now asks to
   record JE-21/b. [JE-17](../semantics/expressions.md#je-17)'s "or else" is
   a falsy test. [JE-49](../semantics/expressions.md#je-49) is partly
   corrected: only `flow_input`'s outer object is compacted, and the named
   context values are checked before it
   ([Q-JE-15](../semantics/expressions.md#q-je-15)).
   [JE-10](../semantics/expressions.md#je-10): `results` is a constant and
   `error` can be assigned. [JE-16](../semantics/expressions.md#je-16): a
   `__proto__` key goes through the setter.
   [JE-15](../semantics/expressions.md#je-15): a replaced `JSON.stringify`'s
   string passes through unchecked
   ([Q-JE-14](../semantics/expressions.md#q-je-14)). JE-48 and JE-1 state
   what the source fixes about the CE image's build; D16 and D17 stand
   until [Q-JE-16](../semantics/expressions.md#q-je-16) is recorded.
   Milestone FU3 follows the answers in `internal/expr`
   ([internal/expr](../../internal/expr/README.md) says where), and
   implements [JE-50](../semantics/expressions.md#je-50) (strict mode),
   [JE-51](../semantics/expressions.md#je-51) (the job queue) and
   [JE-52](../semantics/expressions.md#je-52) (unpaired surrogates), which
   the same revision added.
10. **The readings FU3 left open** (milestone FU3). Each was decided by
    the reading named here and pinned by a test in `internal/expr`:
    - [JE-11](../semantics/expressions.md#je-11): whether the character
      count starts at the start of the text or at the call (read as the
      start of the text, as "ASCII up to the insertion point" says), and
      whether it also places the `)` of a call that never closes (read as
      yes). Q-JE-13's runs, calls at the start of the text, cannot tell.
      Where Windmill panics, windmill-lite fails the evaluation with its
      own text; what the panic does to Windmill's job is Q-JE-13's.
    - [JE-16](../semantics/expressions.md#je-16): whether the values of
      `results` reads and of the helpers are rebuilt by assignment like the
      context values. Read as no, as `JSON.parse` gives them.
    - [FX-42](../semantics/execution.md#fx-42) rule 2 and
      [JE-44](../semantics/expressions.md#je-44): the notation in which the
      backend's JSON library writes a double, which a `previous_result`
      after a loop or `branchall` shows. FX-42 gives examples (`1000.0`,
      `1e+21`, `1.2345678901234568e+29`) but not where the notation turns
      from decimal to exponent, nor how small numbers are written. The loop
      milestones need it.

    **Answered.** [JE-11](../semantics/expressions.md#je-11) is
    corrected: the count starts at the call, since the text is cut at each
    call text and each part is counted on its own, so text before a call
    moves nothing; a call that never closes gets its `)` by the same rule
    at the end of its part, as read; and the runtime catches the panic, so
    the evaluation fails at once with
    `task <n> panicked with message "assertion failed: self.is_char_boundary(idx)"`,
    routed like any evaluation error (JE-11/b–e).
    [JE-16](../semantics/expressions.md#je-16) confirms that fetched
    values are parsed with `JSON.parse`, looked up when the read runs, and
    adds that the previous step's read, served from memory, is the rebuilt
    `previous_result` itself (JE-16/h).
    [FX-42](../semantics/execution.md#fx-42) rule 9 gives the notation:
    shortest digits, plain decimal for a leading-digit exponent from −5 to
    15, `-0` read as `-0.0`, a number too small for a double read as zero
    with its sign and one too large refused (FX-42/i–l). Milestone FU6
    follows the answers: `internal/expr` counts from the call, fails a
    panicked rewrite with the runtime's text and answers the previous
    step's read from memory with the global; the engine writes a loop's
    result with the platform's writer of rule 9 (`platform.Reprint`), the
    one the argument resolution uses.

11. **Fold in recording batch E3e.** E3e recorded on the oracle what its
    manifest, `internal/oracle/testdata/batches/e3e.json`, lists, with
    fixtures under `conformance/flows` (the batch is in
    [internal/oracle](../../internal/oracle/README.md#recording-batches)).
    Please mark the recorded cases `oracle-observed` and close what they
    settle:
    - [Q-FX-11](../semantics/execution.md#q-fx-11): FX-49/a gives the
      correction's outcome, loop result `[1]` and a normal completion, with
      a collapsed and a two-step body; the throwing variant fails the flow
      with a chaining `InternalError` and leaves the loop canceled, its
      module in progress.
    - [Q-FX-12](../semantics/execution.md#q-fx-12): run-and-wait decodes an
      array result positionally (`[201, null, null, null, 7]` gives HTTP
      201 with the body `7`). FX-36/b's body is jsonb's text output, its
      members in jsonb order (`name`, `stack`, `message`, `step_id`), not
      the order the case writes them in; FX-36/a, /c–/g and FX-V3/a hold as
      written, and `early_return` answers while the flow runs on (FX-37/a).
    - [Q-WR-15](../semantics/languages.md#q-wr-15) and
      [Q-WR-16](../semantics/languages.md#q-wr-16): the corrected readings
      (WR-16/f `"7"`, WR-16/g `"ab"`; WR-28/e fetches the rewritten dynamic
      specifier). WR-28/d's script cannot even be deployed: its dependency
      job takes the dynamic `./helper` for an npm package named `.`, so the
      failure comes at deploy time, not when the import runs.
    - [Q-WR-24](../semantics/languages.md#q-wr-24): the image runs
      CommonJS bundles on Node v20.20.2, so an ESM bundle sent to Node
      without a declared type loads (WR-50/b, WR-50/e). A `//native`
      codebase script's job never completed on CE standalone, canceled or
      not (WR-50/c), which the reference may want to say. WR-49/b gives
      `[true,false]` as stated.
    - [Q-OR-16](../semantics/oracle.md#q-or-16): the catalogue rebuilt as
      `conformance/flows/oracle/probes` reproduces every recorded result
      with its bindings written out, and the bare `iter.value` fails with
      `iter is not defined` (OR-V9/b).
    - [Q-OR-6](../semantics/oracle.md#q-or-6): a NUL in an object key or a
      nested value of a Bun result fails like OR-27/a; a Python result's
      NUL is stored as the text ` null ` and does not fail.
    - [Q-OR-7](../semantics/oracle.md#q-or-7) and
      [Q-OR-15](../semantics/oracle.md#q-or-15): on two workers a slow
      index-0 body completes after the next one, while results and an empty
      branch's index stay in index order.
    - [Q-OR-11](../semantics/oracle.md#q-or-11) (a) and
      [Q-OR-12](../semantics/oracle.md#q-or-12): a retry's `skip_if` sees
      the predecessor in jsonb order (OR-24/c), and integer-index keys come
      first on both sides (OR-24/b). For (c), an object the *flow's*
      preprocessor built is read back in jsonb order (`a, cc, bbb`), not
      the preprocessor's; a script's own preprocessor, which OR-24/e names,
      runs only for a trigger and was not recorded.
    - [Q-OR-18](../semantics/oracle.md#q-or-18): OR-5/b completes on two
      workers, OR-7/a's knob leaves the smoke observations unchanged (the
      workflow's second session checks it every time), a subflow step's
      `delete_after_use` blanks nothing on CE (OR-21/a), and a variable
      seeded before a push answers 404 after it (OR-31/b, checked by every
      session).
    - [JE-23](../semantics/expressions.md#je-23) and
      [JE-V5](../semantics/expressions.md#je-v5): which of a step's
      transforms is evaluated first varies from one evaluation to the next,
      whether the flow writes them in the CLI's name order or otherwise
      (`conformance/flows/je/transform-order`, `transform-order-api`). This
      answers milestone E1's question about the CLI's sorting: the order in
      `flow.yaml` does not decide it.
    - [Q-JE-13](../semantics/expressions.md#q-je-13): request 8's two runs,
      as above, which agree with the corrected JE-6 to JE-8.
    Cases E3e could not record are deferred in its manifest with the reason:
    a restarted run (JE-19/e) and a preview (JE-36/d) are outside the
    contract, a cap of two needs three workers (JE-V1/a), and the CLI's
    push-and-version decisions for codebase scripts (WR-49/c–g) are a
    deploy pipeline windmill-lite does not reproduce.
    **Answered.** The reference's revision of 2026-10-29 marks the
    recorded cases `oracle-observed` and closes
    [Q-FX-11](../semantics/execution.md#q-fx-11),
    [Q-FX-12](../semantics/execution.md#q-fx-12),
    [Q-WR-15](../semantics/languages.md#q-wr-15),
    [Q-WR-16](../semantics/languages.md#q-wr-16),
    [Q-WR-24](../semantics/languages.md#q-wr-24),
    [Q-OR-6](../semantics/oracle.md#q-or-6) (except Deno),
    [Q-OR-7](../semantics/oracle.md#q-or-7),
    [Q-OR-11](../semantics/oracle.md#q-or-11) (a) and (b),
    [Q-OR-12](../semantics/oracle.md#q-or-12),
    [Q-OR-15](../semantics/oracle.md#q-or-15),
    [Q-OR-16](../semantics/oracle.md#q-or-16) and
    [Q-OR-18](../semantics/oracle.md#q-or-18); FX-36, FX-49, WR-16 and
    WR-28 are `confirmed`, [JE-23](../semantics/expressions.md#je-23) and
    JE-V5 state that the transform order is not fixed, and Q-JE-13's two
    runs agree with the corrected JE-6 to JE-8. The reference's other
    changes in that revision for the build team are in request 10 above and
    in [FX-50](../semantics/execution.md#fx-50) (which suffixes a
    comparison masks, followed by `conformance.StripLocations`, FU6),
    [JE-18](../semantics/expressions.md#je-18) (a repeated id's step 2
    reads the last module with a job, followed by the engine, FU6),
    [WR-22](../semantics/languages.md#wr-22) (`BUN_INSTALL_CACHE_DIR` and
    `NODE_PATH`) and [WR-55](../semantics/languages.md#wr-55) (a job's
    process group), followed by the Bun harness (FU6, request 12 below).

12. **A child process left behind in a warm process**
    ([WR-55](../semantics/languages.md#wr-55), milestone FU6), open.
    Windmill starts each job's process as the leader of its own process
    group; a timeout or a cancellation kills the whole group, and when the
    job's process exits by itself nothing is killed, but the job completes
    only once every child left in its group has exited, since the worker
    is PID 1 of its container and adopts them; a child that left the group
    (a detached spawn) is neither waited for nor killed. A `fresh` process
    is its invocation's job process, and FU6 reproduces all of that
    ([0004](0004-harness-protocol.md), failure model item 8). A `warm` or
    host process serves many invocations and cannot wait for one
    invocation's children without letting its other code run on after the
    result, and it cannot exit. FU6 gives each child an invocation starts
    a process group of its own, which stands for the job's, kills those
    groups on a timeout or cancellation, as Windmill does, and at
    settlement after a normal return reports a group still holding a
    process as `left_behind` (`child`) and kills it, the fence's release,
    so that nothing of the step runs after it settles (D9). A child the
    step itself starts detached is left running in every mode, as on
    Windmill, and reported as `detached`. Where Windmill would hold the job open until the
    child exits, the warm step completes at once and the child's work is
    cut off. The engine owns the children's groups (the harness announces
    each in a `child` frame, a host on a channel of its own), so a step
    that blocks the event loop, or backs up a host's socket first, cannot
    keep them past its timeout or the process's end. The engine alone
    signals them: each group starts with a guardian that keeps its id
    reserved, and the engine signals a group only while the guardian, or
    for an unguarded group a member it saw before, is still in it, so no
    kill reaches a group that took the id after the child's ended. A synchronous child
    call gets no group of its own (its pid comes too late to announce): it
    stays in the harness's group, which the end of the process kills, and
    what it leaves running is not attributed. Should a warm process hold
    the invocation's result until its children's groups are empty, as
    Windmill holds the job, with the timer, `fetch`, log and platform
    fences applied at the return? The reference
    does not settle what a shared process should do, and
    [Q-WR-28](../semantics/languages.md#q-wr-28) asks the oracle to confirm
    WR-55 itself. Until it is answered, a script that leaves a child in
    its group is flagged by the `left_behind` warning in both modes, and
    one that must let its children finish runs `fresh`.

13. **A container child result the backend cannot read back**
    ([FX-42](../semantics/execution.md#fx-42) rule 9,
    [JE-44](../semantics/expressions.md#je-44); milestone FU6), open. A
    loop's or `branchall`'s result is its children's stored results read
    back into the backend's JSON values. A number beyond a double's range,
    or nesting past the library's recursion limit, is refused by that
    read, and an identity child can carry either from a flow input that
    `jsonb` holds as it was written. FX-42 says the parse fails; it does
    not say what Windmill then does at the container's completion, nor
    with which error. windmill-lite fails the flow at once with a chaining
    error named `InternalError` (FX-15 context 1) whose message is the
    library's text, `number out of range at line <l> column <c>` or
    `recursion limit exceeded at line <l> column <c>`, the position in the
    child's stored text, and the container fails with no result. Which
    error, wrapper and position does Windmill give?

## Changing the design

1. A record is changed by a pull request that edits it in place and says why
   in its Consequences; a reversed decision gets a new record that names the
   one it replaces.
2. New records take the next number.
3. Records cite finding IDs and conformance case IDs, never Windmill source.

## Decisions only the human can make

1. **Provision the `MEMORIA_AUTO_ACK_TOKEN` repository secret.** Until then,
   the Renovate auto-acknowledgement workflow stays installed but inactive,
   and Renovate pull requests need a manual Memoria acknowledgement
   ([docs/memoria.md](../memoria.md)).
2. **Packages labelled Apache only in their own metadata**
   ([Q-OF-7](../semantics/openflow.md#q-of-7)(a)): may the build team read,
   depend on or copy `windmill-yaml-validator`, `cli/windmill-utils-internal`,
   `rust-client` and the published `windmill-cli`? The design does not need
   them: the loader is written from the `SY-` findings, and the manifest guard
   rejects these packages until the question is answered. Running the `wmill`
   CLI as a black box for the oracle is unaffected.
3. **Hybrid dispatch under the CE terms**
   ([Q-OF-7](../semantics/openflow.md#q-of-7)(b)): does a client that delegates
   steps to a user's own Windmill count as "wrapping" it? Milestone R1 (the
   remote adapter) waits for this answer; nothing else does.

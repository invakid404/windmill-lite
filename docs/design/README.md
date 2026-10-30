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
oracle's recordings. Milestone F1 is the core of
[0009](0009-host-adapters.md), the public [wl](../../wl/README.md)
library: `Open` verifies a bundle and owns its platform services, `Run`
runs a flow or root script in `warm` or `fresh` and returns its trace with
the platform calls its steps made, and `windmill-lite run` is its command
line; `wltest check` runs the flow suite through it in both modes.
Milestone F2a exposes the hook seams of [0008](0008-host-hooks.md) to
hosts through `wl` as Go callbacks with selectors (H1, H2, H6, H8, H9,
H12, H13) and runs the mandatory host-provided sites of
[0002](0002-ir-and-loader.md)'s host contract with the host's providers.
Milestone F2b adds the budgets and latches of [0008](0008-host-hooks.md)
(H3, H4, H5, H7, H10) to `wl`, and the hooks level of
[0003](0003-semantics-placement.md)'s conformance suite, whose
latch-precedence cases hold their precedence.
Milestone F2c gives TypeScript hosts the same seams: a host plugin
declares its hooks in its harness's hello and answers them over
`host_call` ([0004](0004-harness-protocol.md)), with `around` wrappers
and the provider bridge of [0007](0007-platform-interface.md); a
host-mode run carries F2b's budgets and latches in its `run` frame; and
step code reaches its run's latch and signal through `wl.latch` and
`wl.signal`. The plugins level of [0003](0003-semantics-placement.md)
runs its cases three ways with one outcome.
Milestone N5, added to [0012](0012-layout-and-milestones.md) after the
first plan, retries a flow step that runs a flow by path: the oracle
recorded where a failed attempt's subflow sits, below the step's `r:<k>`,
and the engine and stub mode follow the recordings.
Milestone E4b's follow-up E4c lays out the copies that `wltest stub`,
`wltest check` and `wltest record` make of a sync tree so that the paths
outside the tree that its codebase entries name resolve from the copy as
from the original, with `wmill.yaml` unchanged.
Milestone F3h, added to [0012](0012-layout-and-milestones.md) after an
embedder built its real folder, lets a host contract list a file of a
codebase entry's `relative_path` outside the sync root, keeps a codebase
build's time when host-provided modules are listed, and keeps the load
effects that Bun 1.3.10 dropped from a listed module's copy.
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
| [0004 Harness protocol](0004-harness-protocol.md) | Length-prefixed JSON frames on a socketpair, with canonical envelopes and large messages in base64 part streams; validated entrypoint names; a shared-process failure model (handlers that never exit, attribution through the invocation's async context without guessing, a `warning` frame for errors no invocation owned, `process.exit` intercepted per invocation, a settle-time fence that clears an invocation's timers, aborts its requests and drops its later output when it settles and reports what it left behind, D9–D11); `fetch` as the only platform transport in warm processes (other transports refused, unrecorded); no in-flight cap unless a host sets one (D15); host mode never tainted, drained by the host, its working directory checked by the SDK and the engine, its runs' settlement reported to it, and killed by the engine's watchdog when its event loop stops answering. |
| [0005 JSON at step boundaries](0005-json-step-boundaries.md) | Go holds every value as producer text plus a lazily computed jsonb text and never decodes what it passes through; a routing table says which consumer sees which; one streaming canonicaliser checked against PostgreSQL; host-plugin results serialised by the steps' own function; the normalising walk dropped, because it was not equivalent. |
| [0006 Module state](0006-module-state.md) | Two modes: `fresh` (a process per invocation, Windmill-equivalent) and `warm` (default: one module instance per script for the process lifetime, `globalThis` shared), plus host-provided modules as the one shared exception; `shared` left out of the first edition. |
| [0007 Platform interface](0007-platform-interface.md) | Go service interfaces with local defaults and host replacements; one interpolation walker with three modes and a host secret layer; a facade reached by intercepted `fetch` (warm) or per-invocation tokens (`fresh`), never by a shared token; unsupported routes fail with `UnsupportedPlatformCall`; hybrid dispatch built behind interfaces, its remote adapter held for a licence decision. |
| [0008 Host hooks](0008-host-hooks.md) | Thirteen hooks with typed selectors, as Go callbacks or TypeScript host plugins (declared up front, called over `host_call`, with `around` wrappers; a plugin's error or timeout latches the run); step code's `wl.latch` and `wl.signal`; mandatory host-provided call sites that fail closed; scopes reach steps through host-provided modules; seams defined in the first engine milestone. |
| [0009 Host adapters](0009-host-adapters.md) | The `wl` Go library is the only engine API; adapters in order: go-plugin, a Bun host SDK, plain gRPC, HTTP, CLI; typed client methods generated from Windmill schemas over a generic wire, bound to the bundle digest. |
| [0010 Packaging](0010-packaging.md) | A bundle with a manifest of digests, tied to one exact engine version; a Bun build helper on the pinned Bun `1.3.10`; host-provided modules through a registry (D12), with one shim generator and one define table and support module for both bundlers; a bundle digest in every handshake, trace and build-info call; a single executable; an OCI image written without Docker; a `verify` command. |
| [0011 Differential testing](0011-differential-testing.md) | The oracle harness as the reference designs it, in public CI only; a typed, injective site-path encoding that also separates repeated module ids; `record`, `check` and `diff` in three configurations; only context-free cases lifted into one-step flows; a stub mode on the oracle and then on windmill-lite; benchmarks, a bounded-pool benchmark and run spans. |
| [0012 Layout and milestones](0012-layout-and-milestones.md) | `wl/` as the only public package and components under `internal/`; 53 milestones, each one pull request, whose "needs merged" covers everything their exit gates need; a contracts milestone first, a walking skeleton on the pinned Bun in wave 2, oracle recordings in batches, each before the milestone it gates, with a manifest that can name each run's gate. |

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
| A codebase build of six scripts importing a package of 400 modules, without and with one host-provided module listed, esbuild 0.28.0, Apple M4 (milestone F3h) | 99 ms and 6.1 s when the plugin resolved each import a second time; 99 ms and 99 ms when it loads the listed file's shim in place of the file | 0010 |
| `Bun.build` on Bun 1.3.10 with a plugin, a package marked `sideEffects: false` whose entry re-exports `export { x } from "./x.js"` (milestone F3h) | the re-exported module is dropped, and the bundle references a binding it never defines, whenever a resolve filter matches the import of the package, even with a callback that resolves nothing; kept when no filter matches it, and with `export *` or an import and export; `ignoreDCEAnnotations` does not change it | 0010 |
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
written; the reference's revision of 2026-10-28 settled nine of them, its
revisions of 2026-10-29 eight more (six by batch E3e, and OF-3 and OF-V5
from the source in SC3), and
[four remain](../semantics/README.md#not-settled). The design does not pick
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
default, and rebuilt the probe catalogue (Q-OR-16); the reference has
recorded them since its revision of 2026-10-29 (request 11 below): FX-36,
FX-49, WR-16, WR-28, OR-15 and OR-V9 are confirmed from E3e.

## Requests to the semantics team

Questions reach the semantics team as requests for the reference to cover a
behaviour ([AGENTS.md](../../AGENTS.md)). The design depended on these, and
all are answered: 1 to 3 by the semantics requests S1 to S3 (pull
request #8), 4 to 9 by the reference's revision of 2026-10-28 (SC1, pull
request #36), 10 and 11 (FU3's readings and the fold-in of batch E3e),
and request 1's last question, by its revision of 2026-10-29 (SC2, pull
request #55); 12 and 13 (FU6's questions) by its revision SC4 of the same
day, 12 for the ordinary execution path only. Each item names the findings
that answer it. Where an answer
differs from a reading a milestone took, that milestone's code follows the
answer in a follow-up; the sync-folder loader and the import base already
do (FU4), and `internal/expr`, the engine, the comparison rule and the Bun
harness follow SC2's answers (FU6).

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
10. **The readings FU3 left open** (milestone FU3), answered by the
    reference's revision of 2026-10-29 (SC2) in JE-11, JE-16 and FX-42.
    Each was decided by the reading named here and pinned by a test in
    `internal/expr`:
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
    ([WR-55](../semantics/languages.md#wr-55), milestone FU6), answered for
    the ordinary execution path by the reference's revision SC4
    (2026-10-29) in WR-55.
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

    **Answered** for the ordinary execution path.
    [WR-55](../semantics/languages.md#wr-55)'s drafted part, "Long-lived
    processes", and case [WR-55/c](../semantics/conformance.md#wr-55) say
    that an ordinary script job on CE, one that no `squash` runner serves,
    gets a process of its own: Windmill's dedicated workers, which keep one
    process warm across jobs, exist only in its enterprise build (`EE`),
    and the CE image never starts one. WR-55/c expects a script that two
    steps of a flow without a loop call, the flow run twice, to report four
    processes and a counter of 1 every time. The CE image does have a
    second route that keeps a process across jobs: the closed-source
    runners that `squash` starts for a sequential loop
    ([CF-25](../semantics/control-flow.md#cf-25)). Whether they reuse one
    process across a loop's iterations, when they mark an iteration's job
    complete and what they do with a child an iteration leaves behind are
    open ([Q-CF-19](../semantics/control-flow.md#q-cf-19)), and a CE
    recording can test them (WR-55; the section below requests it). So for
    ordinary jobs Windmill's behaviour is the per-job rule: a job completes
    once its process has exited and its process group is empty, and a
    timeout or a cancellation kills the group, which `fresh` mode
    reproduces. What a `warm` or host process does with the children an
    invocation leaves behind is, for that path, windmill-lite's own policy,
    for the design to decide; the question above is therefore the
    design's, not the reference's. For a squashed loop on CE, Windmill's
    behaviour is not known yet.
    [Q-WR-28](../semantics/languages.md#q-wr-28) still asks the oracle to
    confirm the per-job rule, and the section below requests WR-55/a–c
    from batch E3f.

13. **A container child result the backend cannot read back**
    ([FX-42](../semantics/execution.md#fx-42) rule 9,
    [JE-44](../semantics/expressions.md#je-44); milestone FU6), answered by
    the reference's revision SC4 (2026-10-29) in FX-42 and JE-44, corrected
    in its validation round 1. A loop's or `branchall`'s result is its
    children's stored results read back into the backend's JSON values. A
    number beyond a double's range, or nesting past the library's recursion
    limit, is refused by that read, and an identity child can carry either
    from a flow input that `jsonb` holds as it was written. FX-42 says the
    parse fails; it does not say what Windmill then does at the container's
    completion, nor with which error. windmill-lite fails the flow at once
    with a chaining error named `InternalError` (FX-15 context 1) whose
    message is the library's text,
    `number out of range at line <l> column <c>` or
    `recursion limit exceeded at line <l> column <c>`, the position in the
    child's stored text, and the container fails with no result. Which
    error, wrapper and position does Windmill give?

    **Answered.** [FX-42](../semantics/execution.md#fx-42) rule 9's drafted
    part, "A child result the read cannot decode", as corrected in the
    reference's validation round 1 of SC4, the loop and `branchall` row of
    [JE-44](../semantics/expressions.md#je-44)'s table and cases
    [FX-42/m to FX-42/o](../semantics/conformance.md#fx-42) give Windmill's
    outcome, which depends on where the chain of transitions started. The
    read covers the children of a `forloopflow`, a `whileloopflow` and a
    `branchall`, not a `branchone`'s, whose result travels in memory. It
    refuses a number beyond a double and nesting past the library's depth
    limit, positioned in PostgreSQL's printed form of the child's stored
    result (spaces after `:` and `,`, keys in `jsonb` order), and the
    container's status entry stays in progress either way.
    - **The children are the flow's own leaf jobs** (a sequential
      `forloopflow` or a `whileloopflow` whose body collapses, FX-42/o):
      that flow fails at once with
      `{"error": {"name": "InternalErr", "message": "SqlErr: error occurred while decoding column 0: <library text> @<loc>"}}`,
      the library text being `number out of range at line <l> column <c>`
      or `recursion limit exceeded at line <l> column <c>`. The name is
      `InternalErr`, not the chaining `InternalError` of FX-15, and the
      message has no `Error: ` prefix. No retry, `continue_on_error`,
      failure module or later step runs.
    - **The children are child flows** (any loop whose body does not
      collapse, a parallel loop, a `branchall`; FX-42/m and FX-42/n):
      Windmill's error handler targets the child flow that has already
      completed, so no job is completed with the error. The flow running
      the container stays running until the zombie-flow monitor cancels it
      after about 60 s (medium).
    - A child flow that the first route fails leaves its parent waiting the
      same way.

    Both routes are source-derived and still need the recordings the
    section below requests from batch E3f. windmill-lite's immediate
    chaining `InternalError` with the library's bare text, positioned in
    the child's stored text, differs in both routes: in the name, the
    wrapper and the position where Windmill fails the flow, and in the
    outcome where Windmill leaves it waiting. Aligning it is a follow-up for
    the engine.

## Recording requests for batch E3f

**From the semantics team** (revisions SC3 and SC4 of 2026-10-29); these
are requests, not design decisions. The reference's revisions answered from
the pinned source what the source settles, and SC4 folded in the
recordings that milestones N2, N3c, NI and N5 had already committed, so
the requests those recordings answered are gone from this list. What only a running Windmill can
show is listed here for a future recording batch E3f, grouped by the
oracle session each needs. Every expectation below is the reference's
source-derived prediction; the named case or question holds the exact
text. Compare location suffixes as `<loc>` and mask job ids
([OR-14](../semantics/oracle.md#or-14), [FX-50](../semantics/execution.md#fx-50)).
Nothing here may commit the oracle's database credentials unmasked.

*Recorded by batch E3f* ([ADR 0012](0012-layout-and-milestones.md)): each
request below is a fixture of the batch, recorded in the session it names,
or is deferred with its reason in the batch's manifest,
[`internal/oracle/testdata/batches/e3f.json`](../../internal/oracle/testdata/batches/e3f.json),
which also lists every claim the oracle contradicted. Requests the next
semantics revision makes go in a new section.

**The reference session** (one worker, default worker tags):

- **Retry delays and jitter**
  ([FX-26/c–f](../semantics/conformance.md#fx-26),
  [Q-FX-18](../semantics/execution.md#q-fx-18)). One Bun step that always
  throws, once per `retry`: FX-26/c `exponential: {attempts: 1, multiplier:
  250, seconds: 300}`, FX-26/d `{attempts: 1, multiplier: 1, seconds:
  30000}`, FX-26/e `{attempts: 2, multiplier: 1, seconds: 300}`. Do not
  wait the delays out: once the step's status names the retry job, read
  that job's `scheduled_for` through the jobs API, subtract the failed
  attempt's completion time, then cancel the flow. Expected 9464 s,
  21600 s, and 21600 s for FX-26/e's second delay. FX-26/f: `{attempts: 1,
  multiplier: 1, seconds: 10, random_factor: 50}` about 20 times; every
  delay a whole number of seconds from 6 to 14 (only the range is fixed,
  not the values).
- **`wm_failure` on identity jobs**
  ([FX-13/b, FX-13/c](../semantics/conformance.md#fx-13)). (b) `[a: bun:
  return 1 (mock: {enabled: true, return_value: {wm_failure: "m", x: 1}}),
  b: bun: return 2]`; (c) `[a: bun: return {wm_failure: "nope", x: 1}
  (continue_on_error: true), b: identity, c: bun: return 3]`, and a variant
  where `b` is a step with `skip_if: "true"`. Observe each step's status
  and stored result and the flow's result. Expected: in (b) `a` fails; in
  (c) and its variant `b` fails and `c` never runs.
- **A branchone aborted at a later transition**
  ([CF-23/d](../semantics/conformance.md#cf-23),
  [Q-CF-3](../semantics/control-flow.md#q-cf-3)). (i) A one-step flow `b:
  branchone(branches: [], default: [r: bun: return 1, l: forloopflow
  (iterator: {type: javascript, expr: "({o: 1})"}, modules: [x: bun:
  return 1])])`; (ii) the same `b` with `continue_on_error: true`, then `c:
  bun(p): return p` (`p = "previous_result"`) and `d: bun(q): return q`
  (`q = "results.b"`). Observe `b`'s status entry and job, the branch
  job's stored result, each step's result and the flow's result.
  Expected: the branch job stores an `InternalError`; (i) fails with
  result `1`; in (ii) `c` returns `1` and `d` the error object. Also
  [CF-23/e, CF-23/f](../semantics/conformance.md#cf-23): (i) with a
  failure module returning `[previous_result, error]` (expected `[1,
  {"name":"UnknownError","message":"Could not parse the error","error":1}]`),
  and (i) with one `retry` object holding both `constant: {attempts: 1,
  seconds: 0}` and a `retry_if` that is true only for the carried value
  (expected a second branch job), next to the same with `retry_if` false
  (expected one branch job).
- **`ai` parallelism and squash on the CE image**
  ([CF-7/e, CF-25/c](../semantics/conformance.md#cf-7),
  [Q-CF-19](../semantics/control-flow.md#q-cf-19)). Milestone NI recorded
  six squashed loops on the CE image (`control-flow/cf-25`,
  [CF-25/d](../semantics/conformance.md#cf-25)): each gave the result of
  its unsquashed form and none failed. Those runs do not show whether the
  CE image reuses one runtime across a squashed loop's iterations: each
  sequential loop that starts the runners ran a single iteration, and job
  counts and kinds are the same either way. Still to record: Q-CF-19, a
  sequential `forloopflow` over `[1, 2, 3]` and a `whileloopflow` whose
  `stop_after_if` (`result.v >= 2`) ends it after its third iteration,
  each with `squash: true`, once with a one-step Bun body and once with a
  two-step body running the same script twice, next to the same four
  flows without `squash`, on the default worker tags; the script keeps a
  counter at module scope and returns its process id, the counter after
  adding 1, and its input `v` (the iteration's value or index). Observe
  each iteration's process id and counter: a counter above 1 or a process
  id repeated across iterations means a reused runtime. Also read each
  iteration job's logs for a line starting `dedicated worker bun:` (what
  Windmill's own runner tests look for in a build with the `enterprise`
  feature), and add a control whose body is a Bash step `echo $v` with
  `squash: true`, which those runners do not serve. The run mode need not
  vary: nothing in the source ties the runner request to a job kind
  ([CF-25](../semantics/control-flow.md#cf-25)). For what the runners do
  with a child an iteration leaves behind
  ([WR-55](../semantics/languages.md#wr-55)): a squashed one-step loop over
  `[1, 2]` whose body starts a background child sleeping 5 s and returns
  at once, next to the same loop without `squash`; record each iteration
  job's start and completion times (unsquashed, each iteration should
  complete only after its child, WR-55/a). Also CF-25/c's two flows (the flag
  on a loop module with `sleep: {type: static, value: 0}` followed by an
  identity step, and on a parallel loop as the first step; both expected
  to give `[1, 2]`), and CF-7/e: a parallel loop over `[1, 2]` with
  `parallelism: {type: ai}`; wait about 30 s, check that no iteration
  started, then cancel.
- **Number notation** ([Q-FX-7](../semantics/execution.md#q-fx-7),
  [Q-JE-13](../semantics/expressions.md#q-je-13)). FX-42/a, /b, /e, /f, /i,
  /j and a stored `1.50e3`, as Q-FX-7 describes; a loop aggregate whose
  iterations return `{"b":1.50,"a":1}` (expected `[{"a":1,"b":1.5}]`; a
  Bun or Python iteration cannot produce `1.50`, so the value must reach
  storage without a JavaScript or Python parse); JE-11/c and JE-11/e end
  to end. For the stored `1.50e3`: start a deployed one-step flow with the
  run body `{"n": 1.50e3, "m": 1.50, "k": 1.50e1}` and read the job's
  stored args through the jobs API; expected `{"k": 15.0, "m": 1.50, "n":
  1500}`, since PostgreSQL's numeric input keeps the fraction digits
  minus the exponent as the display scale, never below 0 (a prediction
  from PostgreSQL, not from the Windmill source).
- **Push failures and the jsonb boundary**
  ([FX-48/b, FX-48/c](../semantics/conformance.md#fx-48),
  [FX-50/a](../semantics/conformance.md#fx-50),
  [FX-51/a–c](../semantics/conformance.md#fx-51),
  [Q-FX-8](../semantics/execution.md#q-fx-8),
  [Q-FX-20](../semantics/execution.md#q-fx-20)). `[a: bun(s): return s]`
  with `s = "String.fromCharCode(0)"`, and the same transform on a second
  step after `x: bun: return 1` (expected: one `Error: ` prefix and one
  suffix fewer at the later transition than at pickup). A Bun step
  returning an unpaired high and an unpaired low surrogate (the
  `InternalErr` text and the roughly 30 s of insert retries); the same
  value produced by a transform at root pickup and at a later transition;
  `$jsonvar:` variables holding `{"s": "\ud83d"}` and `{"n": 1e400}`.
- **A transform whose text is not JSON**
  ([JE-15/j, JE-15/k](../semantics/conformance.md#je-15),
  [Q-JE-14](../semantics/expressions.md#q-je-14)). JE-15/j's runs, plus `x
  = (JSON.stringify = () => "hello", 1)` on step `b` of `[a, b]` and on a
  one-step flow. Expected after a step: an `InternalError` naming
  `invalid input syntax for type json`; on the first step an `Anyhow`
  error with one `Error: ` prefix and two suffixes; no job for `b`.
- **Deep context values** ([JE-49/d, JE-49/e](../semantics/conformance.md#je-49),
  [Q-JE-15](../semantics/expressions.md#q-je-15)). A run whose input is
  `{"k": <127 nested arrays>}` with `x = flow_input.k.length` (expected: the
  job is created and fails with `recursion limit exceeded at line 1 column
  132`); a deployed flow whose `flow_env` member `K` is 128 levels deep, read
  by `x = flow_env.K.length` (expected: the deploy succeeds; the step fails
  at column 128).
- **`resource()` and the provider texts**
  ([JE-20/c, JE-20/d, JE-42/i, JE-42/j](../semantics/conformance.md#je-20),
  [Q-JE-17](../semantics/expressions.md#q-je-17)). Resources and variables
  created after the push, as E3a's `je/providers` does. (a) A transform
  `resource("u/test/none")`, then `resource("u/test/r1")` with `r1 =
  {"k":"$var:u/test/missing"}`, then `resource("$res:u/test/r1")`; record
  each message. (b) `x = resource("u/test/r")`, `y =
  resource("u/test/alias")`, `z = resource("u/test/n")` and a static `w =
  "$res:u/test/r"`, with `r = {"p":"$var:u/test/v","job":"$WM_JOB_ID"}`,
  `alias` the string `"$res:u/test/r"` and `n = 42`; record the result,
  job ids masked (`x.job` should stay literal, `w.job` become the step's
  id). (c) A static `$jsonvar:` argument for variables holding `hello`, the
  empty text, `{"é": 1e400}`, and the three lines `{`, `  "b": 1e400`, `}`
  (the serde positions). (d) Static `$encrypted:abcd`, `$encrypted:secret`
  and `$encrypted:not!base64`.
- **Secrets, `job_id` and `CUSTOM_INSTANCE_DB/` on the variable and
  resource routes** ([WA-11/c, WA-11/d, WA-12/g, WA-12/h](../semantics/conformance.md#wa-11),
  [Q-WA-13](../semantics/client-api.md#q-wa-13)). Create a secret with
  `already_encrypted=true` and the value `AAAAAAAAAAAAAAAAAAAAAA==` (if
  its read does not fail with WA-11/c's padding cause under the oracle's
  key, try other 16-byte values and record which one was kept); repeat
  the create with `hunter2`, `dGVzdA==` and `$vault:u/x/p` and record each
  status and body; read the secret with `getVariable`, as a static
  `$var:` argument, and read `u/x/p` (compare the cause against WA-11/c's
  set of texts). With resources `u/x/plain = {"a":1}` and `u/x/ctx =
  {"j":"$WM_JOB_ID"}`, call the interpolated read of both with `job_id`
  set to a random UUID, a completed step's id, the caller's own id and
  `nope`. As the admin (a super admin), read `CUSTOM_INSTANCE_DB/demo`
  through `getResource` and the raw route, masking every connection field;
  the non-admin half needs a second user, deferred if the harness has none.
- **The facade's error answers**
  ([WA-14/c, WA-14/d, WA-19/b–d, WA-V2/c](../semantics/conformance.md#wa-14),
  [Q-WA-13](../semantics/client-api.md#q-wa-13)). A Bun step that calls
  the API with `fetch` and its own token and returns `[status, body]` for:
  the root lookup of an unknown UUID and of `abc` (WA-19/b, /c);
  `get_result_maybe` of `abc` (WA-V2/c); the flow user state of `abc`, and
  WA-14/d's two non-JSON POSTs against the step's own root; `getResult` of
  a child script started 5 s earlier and still running (WA-19/d). A second
  flow stores a user-state key; after it completes the harness POSTs and
  GETs that key by root id (WA-14/c: expected 404 `Not found: Flow job not
  found`, then `null`). WA-4/b and WA-V2/d need a second workspace; record
  them only if the bootstrap can create one.
- **Envs, handler errors and validation**
  ([WR-22/b, WR-22/c, WR-37/b](../semantics/conformance.md#wr-22),
  [WR-54/a–e](../semantics/conformance.md#wr-54),
  [Q-WR-20](../semantics/languages.md#q-wr-20),
  [Q-WR-26](../semantics/languages.md#q-wr-26)). WR-22/b's cache directory
  and `NODE_PATH`, for a Bun script and a CJS codebase script; WR-22/c by
  path: `envs: ["A=b=c","B="]` (expected `["b=c",""]`) and `envs:
  ["NOEQ"]` (expected: deploys, then fails with `Bad request: Invalid env
  var: NOEQ. …`). WR-37/b: a preview of `main(...xs)`, expected to fail
  with ``Error: parameter syntax unsupported: `...xs` `` and one location
  suffix. Inline steps that start with the line `// schema_validation`,
  given a wrong type, `null` for a required and for an optional
  parameter, a string outside a literal union, and a missing or `null`
  array element and object property; record each error. WR-54/f and
  WR-54/g: a script created through the scripts API with a stored schema
  whose property is `{"type":["array","null"],"items":…}` (expected: the
  load fails for every input) and the same union as `anyOf` (expected to
  build and validate).
- **The rest of Q-WR-20** ([Q-WR-20](../semantics/languages.md#q-wr-20)):
  the inline-flow import base (WR-29/a, /b), the empty-result `InternalErr`
  (WR-V2/a), the `raw_unpinned` request during lock generation (WR-V9/a,
  from the server's request log), and a single-file codebase script that
  reports whether its own `main.js` contains CRLF (WR-49/h).
- **TSX parse** ([WR-51/d, WR-52/a, WR-52/e](../semantics/conformance.md#wr-52),
  [Q-WR-27](../semantics/languages.md#q-wr-27)). Previews of a decorator
  script and of an angle-bracket assertion script (each expected to fail
  with `Error while parsing code, it is invalid TypeScript: …` and one
  location suffix), and of the generic-arrow script (expected `[1,2,3]`).
- **Leftover processes and process reuse** ([WR-55/a–c](../semantics/conformance.md#wr-55),
  [Q-WR-28](../semantics/languages.md#q-wr-28)). A step that starts a
  background child sleeping 5 s and returns at once, and a detached
  variant; the same child under a 1 s step timeout with a 10 s body.
  Record each job's start and completion times and outcome. WR-55/c: a
  deployed Bun script `f/demo/counter` that keeps a module-scope counter
  and returns `{"pid": process.pid, "n": <the counter after adding 1>}`,
  called by two steps of one flow, the flow run twice by path. Expected:
  `n` is `1` in all four results and no `pid` repeats (the CE image starts
  no dedicated worker).
- **The trailing virtual step** ([OR-V7/c](../semantics/conformance.md#or-v7),
  [OF-17/a](../semantics/conformance.md#of-17),
  [Q-OR-9](../semantics/oracle.md#q-or-9)). Deploy `[a]`, `a` a Bun step
  returning `7` with `sleep: {type: static, value: 1}`, run it by path;
  record `flows/get` (expected: no `a-v`) and the raw `flow_status`
  (expected: `a` and `a-v` both `Success`, `a-v` an identity job about 1 s
  after `a`, result `7`).
- **`delete_after_use` that survives deploy**
  ([OR-21/b, OR-21/c](../semantics/conformance.md#or-21)). Create `f/demo/dau`
  (`main(x)` returns `{secret: x}`) through the scripts API with
  `"delete_after_use": true` (the CLI never sends it). (b) A flow with one
  step by path to it and `x = 7`: expected unchanged args and result on CE.
  (c) The run-and-wait script route with `{"x":7}`: expected the response
  `{"secret":7}`, then the stored job with args `{}`, result `{}` and logs
  `##DELETED##` (the script has no `delete_after_secs`). OR-21/d: the same
  script created with `"delete_after_secs": 3600`; expected: nothing is
  blanked on CE, now or later.
- **A script's own preprocessor** ([OR-24/e](../semantics/conformance.md#or-24),
  [Q-OR-11](../semantics/oracle.md#q-or-11)). No trigger is needed: a Bun
  script with `preprocessor(event)` returning `{x: {bbb: 1, a: 2, cc: 3}}`
  and `main(x)` returning `Object.keys(x)`, run through the run-and-wait
  script route (expected `["bbb","a","cc"]`), plus the stored args. In a
  flow ([WR-11/b, WR-11/c](../semantics/conformance.md#wr-11)): a step by
  path without a hash and with `apply_preprocessor: true` (expected: the
  preprocessor does not run), and the same step pinned to the script's
  hash (expected: it runs).
- **Incoming input refused at storage**
  ([FX-48/d](../semantics/conformance.md#fx-48),
  [FX-51/d, FX-51/e](../semantics/conformance.md#fx-51)). Run requests to
  a deployed one-step flow with the bodies `{"s": "x\u0000y"}`,
  `{"k\u0000": 1}`, `{"x": 1e131072}`, `{"k": "\ud83d"}` and `{"k\ud83d":
  1}`. Record status, content type and body, and that no job was created.
  Expected 400 `text/plain` for all; the first four start `SqlErr: error
  returned from database: `, the last is `Bad request: invalid json:
  unexpected end of hex escape at line 1 column 10`.
- **CLI warnings on push** ([SY-5/d, SY-19/e](../semantics/conformance.md#sy-5),
  [Q-OF-13](../semantics/openflow.md#q-of-13)). Capture the CLI's stdout and
  stderr during the pushes Q-OF-13 already asks for: a script with no lock
  file (expected `Error reading inline path: f/a/x.script.lock, Error:
  ENOENT: no such file or directory, open 'f/a/x.script.lock'`) and a flow
  inline lock starting with the bytes `FF FE` (expected `Lock file
  a.inline_script.lock not found, treating as empty`, and the lock `""`).

- **The result cache** ([FX-40/e–FX-40/m](../semantics/conformance.md#fx-40),
  [Q-FX-21](../semantics/execution.md#q-fx-21)). Each case runs its flow
  twice (or as listed) within the TTL, in one fixture run, as FX-40/a and
  FX-40/b do. Observe each call's flow status and result, which step jobs
  were created, and the log line `Job skipped because args & path found in
  cache and not expired` on the hit jobs. Expected: FX-40/e (a failure
  tolerated at the last module) is a hit on the second call; FX-40/f (an
  error stop) runs `a` both times, and with a failure module returning
  `"H"` the second call succeeds with `"H"` from the cache; FX-40/g (a
  `skip_if_stopped` stop) completes `skipped` first and `success` on the
  hit; FX-40/h (a parallel loop with `skip_failures: false` and one
  failing iteration at the last module) runs both times, and with
  `skip_failures: true` hits; FX-40/i
  (inputs `{"o": {"b": 1, "a": 2}}` then `{"o": {"a": 2, "b": 1}}`) hits,
  and `{"n": 1}` then `{"n": 1.0}` misses; FX-40/j (a static
  `"$var:u/test/v"` input, the variable changed between the calls) returns
  the first value; FX-40/k (an S3 object input with
  `cache_ignore_s3_path: true` and no workspace storage) hits for the same
  key and misses for another key; FX-40/l (`stop_after_if` on a cached
  step) stops after `a` both times, the second time from the cache;
  FX-40/m (two steps pinned to the same script hash with equal arguments,
  `cache_ttl: 60` then `cache_ttl: 0`, on one worker): the second step
  hits the first's entry, while a flow whose two such steps both have
  `cache_ttl: 0` runs both.
- **A container as the failure module** ([CF-31/a–e](../semantics/conformance.md#cf-31),
  [Q-CF-17](../semantics/control-flow.md#q-cf-17)). Deployed flows, by
  path, each with `[a: bun: throw new Error("x")]` and a `failure_module`
  with id `failure`: (a) a sequential `forloopflow` over `[1, 2]` whose
  body is `[h: bun(x): return x * 10]` with `x = "flow_input.iter.value"`
  (expected: status failure, result `10`, one `flowpreview` child for the
  slot, the raw `flow_status.failure_module` still `in_progress`), and the
  same body returning `{recover: true, i: x}` (expected success,
  `{"recover":true,"i":1}`); (b) the iterator `[1]` (expected failure
  `[10]`) and a body returning `true` (expected success `[true]`); (c) a
  `branchone` slot with one branch `{expr: "error.message == 'x'",
  modules: [h1: return "matched"]}` and default `[h2: return "default"]`
  (expected failure `"matched"`), and with the step throwing `y` (expected
  `"default"`); (d) a slot over `[]` with a second step `b: bun(p): return
  p` (`p = "previous_result"`) after `a` (expected success, result `[]`,
  and the raw status's entry at `a`'s position a `Success` with id
  `failure`), and the one-step flow (expected success `[]`); (e) a slot
  with `parallel: true` over `[1, 2]`: record that both children complete
  and that the flow has not completed 30 s later, then let it run to about
  90 s and record whether the server cancels it (canceler `monitor`);
  cancel it if not. Record every job's kind, status and result.
- **A container's own settings on handled failures and forwarded stops**
  ([CF-16/e, CF-16/f](../semantics/conformance.md#cf-16),
  [CF-12/g](../semantics/conformance.md#cf-12),
  [CF-7/f](../semantics/conformance.md#cf-7),
  [FX-30/b](../semantics/conformance.md#fx-30),
  [Q-FX-3](../semantics/execution.md#q-fx-3)). (1) A flow with a
  `failure_module` returning `"H"` and `[l: forloopflow over [1],
  skip_failures: false, body [x: bun: throw new Error("e")],
  continue_on_error: true, n: bun(p): return p]` (expected success
  `["H"]`, no handler job at the root), the same without `n` (expected
  success `["H"]`), and with `n` throwing (expected failure `"H"`, one root
  handler job). (2) The same `l` alone with `retry: {constant: {attempts:
  1, seconds: 0}}` and no `continue_on_error` (expected two iteration child
  flows, failure `["H"]`, no root handler job). (3) CF-12/f's flow with
  `continue_on_error: true` on the loop (expected failure with
  `[{"error":{"name":"EarlyStopError","message":"halt"}}]`, `n` not run),
  the loop alone (same), and with `retry` instead (expected two child flows
  for iteration 0, same failure). (4) `[l: forloopflow over [1, 2],
  parallel: true, skip_failures: false, body [x: bun: throw new
  Error("e")], continue_on_error:
  true, n: bun: return "after"]` (expected an `InternalError` whose message
  starts `Bad request: Unrecognized module status for ForloopFlow Failure
  {`, `n` not run), `l` alone (same), and with a `failure_module` returning
  `"H"` (same error; the root handler does not run). (5) `[a: bun: throw
  new Error("x"), continue_on_error: true, retry: {constant: {attempts: 2,
  seconds: 0}}]` (expected three `a` jobs, success with the wrapped error,
  and two failed attempts listed).
- **A step canceled while it waits to start** ([FX-38/b](../semantics/conformance.md#fx-38)).
  Deploy `f/demo/child` = `[c: bun: return 2]` and `[a: bun: return 1,
  sleep: {type: static, value: 30}, b: flow f/demo/child]`; run by path
  and cancel 3 s after `a` completes. Expected: the flow ends `canceled`
  with the `Canceled` result well before 30 s; `b`'s job exists with kind
  `flow` and completed `failure` with the `Canceled` value, with no child
  job (`c` never runs); `b`'s module is `Failure`. Variant with `b` a Bun
  step (its `script` job ends the same way without running). Optional
  variant: `b: flow f/demo/failing` (a child that throws) with `retry:
  {constant: {attempts: 1, seconds: 30}}`, canceled 3 s after the first
  subflow failed: expected a second subflow job that fails `Canceled` at
  pickup with no child.
- **A retried parallel container** ([CF-22/h](../semantics/conformance.md#cf-22),
  [CF-22](../semantics/control-flow.md#cf-22) rule 5). `[l:
  forloopflow(iterator: [10, 20], parallel: true, parallelism: 1,
  skip_failures: false, modules: [b: bun(i, p)], retry: {constant:
  {attempts: 1, seconds: 0}})]`, deployed and run by path,
  with `i = flow_input.iter.index` and `p = flow_input.iter_parent ??
  null`. On its first run in the outer run (a `/tmp` file keyed by
  `WM_ROOT_JOB_ID` and `i`) `b` throws for `i = 0` and returns `p` for `i
  = 1`; on later runs it returns `p`. `parallelism: 1` holds iteration 1
  until iteration 0 has completed, so the successful iteration completes
  last in the first attempt. Observe the result, `l`'s status entry
  (`job`, `failed_retries`), each iteration child's args and the first
  attempt's completion order.
  Expected `[{"index":1,"value":20}, {"index":1,"value":20}]` (the base is
  iteration 1's child, which completed last although it succeeded) and no
  failed attempt listed on `l`; a last-to-fail reading predicts
  `{"index":0,"value":10}`. Variant: `b` always throws for `i = 0` and the
  flow has a failure module `ret(1)`: expected `l` fails for good, lists
  nothing, and the failure module's entry lists the first attempt's
  iteration-1 child flow (a successful job).
- **A flow that calls itself by path** ([CF-17/j](../semantics/conformance.md#cf-17)).
  Deploy `f/t/rec` as the case says and run it with `{"n": 3}`. Expected:
  the deploy succeeds and the run returns `0` through four nested `Flow`
  jobs of `f/t/rec`.
- **A container's throwing `timeout` and `parallelism`**
  ([CF-21](../semantics/control-flow.md#cf-21)). `[a: ret(1), l:
  forloopflow(iterator: [1, 2], parallel: true, timeout: {type:
  javascript, expr: "null.t"}, parallelism: {type: javascript, expr:
  "null.p"}, modules: [b: ret(1)])]`. Expected: the flow fails with
  [CF-23](../semantics/control-flow.md#cf-23)'s chaining error whose
  message contains ``Error during evaluation of expression `null.t` ``
  (the timeout's, not the parallelism's), and no iteration job exists;
  the same with only `parallelism` throwing: no iteration job either.
  Also [CF-21/f](../semantics/conformance.md#cf-21): CF-21/e's collapsed
  flow run with the input `{"iter": {"index": 3}}` (expected success with
  `[1]`, the timeout `13`), and the nested flow of CF-21/f, whose inner
  loop's timeout reads the outer iteration's `iter` (expected success with
  `[2]`).
- **A container child result the backend cannot decode**
  ([FX-42/m–FX-42/o](../semantics/conformance.md#fx-42)). Allow every run
  of this bullet at least 150 s, and record job ids masked, each job's
  status, result and log, and `l`'s raw status entry. (1) FX-42/o, the
  collapsed route: deploy `[l: forloopflow(iterator: {type: javascript,
  expr: "[1]"}, modules: [x: rawscript bun: let v = 1; for (let i = 0; i
  < 128; i++) v = [v]; return v;]), b: bun: return 1]` with no failure
  module and run it by path. Expected: the flow fails at once with
  `{"error": {"name": "InternalErr", "message": "SqlErr: error occurred
  while decoding column 0: recursion limit exceeded at line 1 column 128
  @<loc>"}}`, `b` never runs, `l`'s entry stays in progress; with 127
  wrappings, success. (2) The same loop as the `default` branch of a
  `branchone` at step 0: expected the branch's child flow fails as in (1)
  and the root stays running until it ends `canceled` about 60 s or more
  later, reason starting `Flow <id> (` and containing `was hanging in
  between 2 steps` (URLs masked). (3) FX-42/m and FX-42/n, the child-flow
  route: deploy `[l: forloopflow(iterator: {type: javascript, expr:
  "[1]"}, modules: [x: identity]), b: bun: return 1]` with a failure
  module returning `"H"`; run it by path with the input `{"n": 1e400}`
  sent as raw JSON text (the run API keeps it; `jsonb` stores it), and
  with `{"k": <127 nested arrays>}` next to `{"k": <126 nested arrays>}`
  (expected success). Expected for the first two: the iteration's child
  flow succeeds, no job carries the decode error, neither `b` nor the
  failure module runs, and the root stays running until the monitor
  cancels it as in (2). Repeat (3) with `parallel: true` and with a
  `branchall` whose one branch is `[x: identity]`.
- **The answers to variable writes** ([WA-11/e](../semantics/conformance.md#wa-11)).
  With a token of the workspace (harness side, or a Bun step using `fetch`
  and its own token, as in the facade's error-answers request): create
  `u/x/w`, create it again, update its value, update `u/x/none`; record
  each status, content type and body. Expected 201 `variable u/x/w
  created`, 400 `Bad request: Variable u/x/w already exists`, 200 `variable
  u/x/w updated (npath: "u/x/w")`, 404 `Not found: Variable not found at
  name u/x/none <loc>`. Also a Bun step returning `await
  setVariable("u/x/w", "c")` (expected `null`) and a later read (expected
  `"c"`).
- **A job chain that never ends** ([Q-JE-12](../semantics/expressions.md#q-je-12)).
  A one-step flow whose module-0 transform is `x =
  (queueMicrotask(function f() { queueMicrotask(f) }), 1)`. Expected: the
  evaluation never finishes ([JE-51](../semantics/expressions.md#je-51)),
  and the flow ends canceled by the zombie-flow monitor after about 60 s,
  as [JE-2/a](../semantics/conformance.md#je-2) recorded for a CPU loop at
  the same site, not with JE-2's timeout text. Allow at least 75 s.
- **Colliding step identities** ([Q-OR-17](../semantics/oracle.md#q-or-17)).
  Deploy through the flows API, not the CLI, whose file names derive from
  module ids: (i) `[failure: bun: throw new Error("x")]` with a failure
  module `recover` returning `1`; (ii) `[preprocessor: bun: return 1]` with
  a preprocessor module `pre` returning `{}`; (iii) `[a/b: bun: return 1,
  a%2Fb: bun: return 2]`. Record the raw `flow_status` and the walker's
  site paths; each pair must stay two distinct records and placeholders.
  Expected: all three deploy (the API checks no module id), and the slot's
  entry sits in `failure_module` or `preprocessor_module`, apart from
  `modules`.
- **Timeouts on branches and subflow steps** ([Q-FX-1](../semantics/execution.md#q-fx-1),
  [CF-27](../semantics/control-flow.md#cf-27)). `[b: branchone(branches:
  [], default: [x: bun: await sleep(3000); return 3000]) (timeout: 1)]`,
  the same as a two-branch parallel `branchall` with that body in each
  branch, and `[c: flow f/demo/slow (timeout: 1)]` where `f/demo/slow` =
  `[x: bun: await sleep(3000); return 3000]`. Expected: all three succeed
  after about 3 s with `3000`, `[3000, 3000]` and `3000`, with no timeout
  error: a branch flow and a subflow job are flow jobs, which nothing
  times.

**The two-worker session:**

- **Released iterations** ([Q-CF-18](../semantics/control-flow.md#q-cf-18)).
  CF-7/a and CF-7/b with `parallelism: 2` over four iterations whose
  bodies sleep different times; record each body job's start and end.
  Predicted: one release per completion, in index order, a pickup delay of
  up to about 5 s, and no fixed start order among iterations released
  together.

**Sessions with a changed configuration** (only the fixtures that declare
them run there):

- **Native steps** ([Q-WR-21](../semantics/languages.md#q-wr-21);
  [WR-40](../semantics/languages.md#wr-40) to
  [WR-46](../semantics/languages.md#wr-46),
  [WR-50/c, WR-50/f](../semantics/conformance.md#wr-50)). Add only
  `WORKER_TAGS=nativets` to the standalone container (a `default`-group
  worker adds it to its group's tags; `FORCE_WORKER_TAGS` would replace
  them). First check that a one-step `nativets` flow returning `1`
  completes. Then record WR-40/a to WR-46/b, WR-50/c (`format: esm`, first
  line `//native`; expected `["undefined","undefined"]`) and WR-50/f (the
  same with the default `cjs` format; expected a `ReferenceError` with
  `module is not defined`), with full results and error objects including
  `stack`. Also [WR-40/h](../semantics/conformance.md#wr-40): a
  `//native` step importing `node:buffer` and `node:fs` and returning
  `[Buffer.from("hi").toString("base64"), typeof readFileSync, typeof
  __dirname]` (expected `["aGk=","undefined","string"]`), and a variant
  using a global `Buffer` without an import (expected a `ReferenceError`,
  `Buffer is not defined`); [WR-40/i](../semantics/conformance.md#wr-40):
  `//native` steps importing `spawn` from `node:child_process` (expected
  `"undefined"`), from `child_process` and `test` from `node:test` (both
  expected to fail with `bun build`'s exit-code error whose log tail holds
  `Browser build cannot import Node.js builtin:`); and record WR-40/f's `name` and `message`
  (expected `TypeError` and `Failed to load file:///missing.ts\n  Caused
  by:\n    No such file or directory (os error 2)`) and WR-40/c's whole
  message (expected `execution error:\nError: failed to load module:
  Failed to load file:///helper.ts: No such file or directory (os error
  2)<loc>`).
- **The result-size limit** ([OR-7/b](../semantics/conformance.md#or-7),
  [Q-OR-14](../semantics/oracle.md#q-or-14) is answered from the source;
  this confirms it). `MAX_RESULT_SIZE_MB=3` and a Bun step returning about
  4.5 MiB. Expected a `ResultTooLarge` error with `… 4MB >
  MAX_RESULT_SIZE_MB=3MB.`, no `step_id`, and no 30 s retry window. Also
  [OR-7/c](../semantics/conformance.md#or-7): the same payload with
  `wm_failure: "m"` added, expected to end as `ResultTooLarge` too, not as
  a `ManualFailure`.
- **`DISABLE_FLOW_SCRIPT=true`**, optional
  ([OF-V5/b](../semantics/conformance.md#of-v5),
  [Q-OF-11](../semantics/openflow.md#q-of-11) is answered from the
  source). The trailing-sleep flow above; expected status ids `["a"]` at
  enqueue, with the worker still reading the lite form.
- **Workspace object storage** ([WA-16/c–e](../semantics/conformance.md#wa-16),
  [Q-WA-3](../semantics/client-api.md#q-wa-3)). The CE image's object-store
  handlers are closed source, so only a recording can say what they do.
  Session A points workspace storage at a local S3-compatible store
  started beside the oracle; a Bun step returns, each call in its own
  `try`: `writeS3File({s3:"t/a.txt"},"hi")` then `loadS3File` of it as
  text (expected `[{"s3":"t/a.txt"},"hi"]`), `writeS3File(undefined,"x")`
  (the generated key's form), and `loadS3File({s3:"t/none.txt"})` (the
  status and body inside `Failed to load S3 file: …`). Session B has no
  storage and runs the first call only (WA-16/d).
- **S3-aware cache hashing** ([FX-40](../semantics/execution.md#fx-40),
  [FX-40/k](../semantics/conformance.md#fx-40),
  [Q-FX-21](../semantics/execution.md#q-fx-21)), optional, `later`, in
  session A. A step `a: bun(f): return Math.random()` with `cache_ttl: 60`
  and `f = "flow_input.f"`: write `t/c.txt` with `"one"`, run with `{"f":
  {"s3": "t/c.txt"}}` twice (expected: a hit), overwrite `t/c.txt` with
  `"two"` and run again (expected: a miss, since the ETag changed); then
  with `cache_ignore_s3_path: true`, copy `t/c.txt` to `t/d.txt`, run with
  `t/c.txt` and then with `t/d.txt` (expected: a hit, when the store gives
  both copies one ETag), and run with `{"f": {"s3": "t/none.txt"}}` twice
  (expected: a hit keyed by the plain JSON, since a missing object gives
  no ETag).

**Outside the oracle, or outside the equivalence contract** (optional):

- **PostgreSQL alone** ([Q-JE-9](../semantics/expressions.md#q-je-9)):
  `SELECT '<json>'::jsonb::text` on the oracle's PostgreSQL with keys of
  mixed length, multi-byte UTF-8 keys and integer-like keys.
- **The image's toolchain** ([Q-JE-16](../semantics/expressions.md#q-je-16)):
  a Python step printing `ldd --version` and the `.comment` section of the
  server binary.
- **Preview and restart:** JE-36/d as a preview
  ([Q-JE-5](../semantics/expressions.md#q-je-5)); a restart for JE-19/e and
  JE-19/f ([Q-JE-8](../semantics/expressions.md#q-je-8) is answered from the
  source; expected `{"x":17,"y":17,"z":5}`).

**Still open and oracle-only, recorded as their questions describe:**
[Q-JE-1](../semantics/expressions.md#q-je-1) (JE-21/b's thrown values),
[Q-JE-2](../semantics/expressions.md#q-je-2) (the `typeof` list),
[Q-JE-12](../semantics/expressions.md#q-je-12) (JE-50/a, JE-51/a, JE-52/a),
[Q-FX-18](../semantics/execution.md#q-fx-18)'s remaining cases (FX-1/a,
FX-4/a, FX-5/a, FX-11/a, FX-11/b, FX-24/g, FX-47/b, FX-V1/a, FX-V1/b and
FX-V2/a; FX-26/c–f have their own request above),
[Q-CF-17](../semantics/control-flow.md#q-cf-17)'s remaining cases,
[Q-OF-1](../semantics/openflow.md#q-of-1)'s fixtures and
[Q-OF-13](../semantics/openflow.md#q-of-13)'s sync trees. Q-CF-1 is
answered by recordings milestone N3c had already made, and Q-OR-17's
colliding pairs have their own request above.

**From the build team** (milestone FU8, N5's follow-up). N5 left a
canceled flow step that is scheduled but not launched unrecorded; it is
now [FX-38/b](../semantics/conformance.md#fx-38)'s subflow variant,
requested above ("A step canceled while it waits to start"). Its Bun
variant is recorded already (`conformance/flows/execution/fx-23`, run
`canceled_sleep`). windmill-lite treats both alike: the cancellation makes
the queued job due, and it fails without running, with the `Canceled`
error, before a flow step's subflow starts (internal/engine, FX-38).

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

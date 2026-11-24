# windmill-lite design

**Current state:** thirteen design records, each `Accepted`: the code
implements their decisions. Eleven of them name the parts that have no code
yet in a **Not built yet:** line, such as the go-plugin, gRPC and HTTP
adapters, the single executable and the OCI image. One request to the
semantics team is open, and the
[recording requests](#recording-requests) list the observations of Windmill
that are still to record.

This README covers `docs/design/`: this index and the decision records it
links. The records decide the architecture, the equivalence contract with
Windmill `v1.862.0`, the Go package layout and the implementation
milestones. They build on the
[semantics reference](../semantics/README.md) and cite its
[finding](../glossary.md#finding) IDs.

## How the records are written

- Each record is an [ADR](../glossary.md#adr): one decision, with its
  context, the options considered and its consequences. Its status line
  says `Accepted` when the code implements the decision.
- The [build team](../glossary.md#build-team) writes the records under the
  clean-room rule of [AGENTS.md](../../AGENTS.md#clean-room): from the
  semantics reference, Windmill's public documentation, the Apache-2.0 parts
  of Windmill, and observed behaviour.
- Where a decision depends on a number, the number was measured with a
  throwaway prototype outside this repository, and the record states the
  hardware, versions and method.
- Two kinds of number come from the repository instead: the native pool
  under load, from the expression engine's own load test, and the step
  boundary, whose benchmarks have their baseline in
  [JSON at step boundaries](0005-json-step-boundaries.md#baseline).

## The architecture in brief

A Go **engine** loads a **bundle** built from a `wmill sync` folder and is the
only flow interpreter. It runs Windmill's decision list, containers, retries,
waits, failure modules and cancellation. It evaluates every expression in
QuickJS-ng (the engine Windmill uses), embedded natively.

Steps run in warm **language harnesses** ([Bun](../glossary.md#bun) first),
which are step runners. They bind arguments, hydrate, call the entrypoint, and
serialise results and errors with the language's rules, one framed message per
step over a socketpair. Step code reaches the platform through the stock
`windmill-client`, which talks to a Go facade, intercepted in-process for Bun.

Hosts drive the engine through a Go library and thin adapters (go-plugin, a
Bun [host mode](../glossary.md#host-mode), gRPC, HTTP, a CLI) and change runs
through a fixed set of hooks.

```mermaid
flowchart LR
  host[host adapter] --> engine[Go engine: flows and expressions]
  engine -->|one frame per step| harness[Bun harness: step runner]
  harness -->|platform calls| facade[Go platform facade]
```

The Bun host mode and the CLI are built; the go-plugin, gRPC and HTTP
adapters are planned ([host adapters](0009-host-adapters.md)).

## Decision records

| Record | Decision |
|---|---|
| [Equivalence contract](0001-equivalence-contract.md) | Match every `core` finding for a successfully deployed flow run by path as a root on CE, and reject out-of-scope features at load. Twenty-four named deviations (D1–D24), six of which `fresh` mode removes, and eleven restrictions. Contested findings are recorded on the oracle early, with a stated default until then and no permanent switches. Rust location suffixes are stripped only from generated messages. |
| [IR, loader and validation](0002-ir-and-loader.md) | The CLI's YAML dialect over a YAML 1.1 parser, with named refusals; a sync-folder loader that reads each item on demand; hand-written OpenFlow types with Windmill's effective decoder and a schema-coverage test; normalise once to the deployed form; a typed IR with precomputed collapse, compiled expressions and a harness class per leaf; signatures of every export; a host contract whose mandatory provided sites make their targets opaque; codebase scripts bundled with esbuild and run on Bun (D13). |
| [Where semantics live](0003-semantics-placement.md) | One flow interpreter and one expression pipeline, in Go; harnesses are step runners, with no segments. Expressions run on QuickJS-ng `be664ca1`, embedded natively and built static: a fresh runtime per evaluation from a bounded pool, its heap on mimalloc and destroyed whole. Member paths, templates, arithmetic, comparisons and predicates over JSON values are answered in Go with the program's exact answer. One conformance suite of flow, step, expression and format cases runs in every mode. |
| [Harness protocol](0004-harness-protocol.md) | Length-prefixed JSON frames on a socketpair, with canonical envelopes and base64 part streams for large messages. A shared-process failure model attributes errors per invocation, intercepts `process.exit` and fences an invocation when it settles; host mode is never tainted, and a watchdog kills a host that stops answering. A deadline ends only its run, which names its cause. A bundle may carry a host plugin; planned: pinned runs, recycle triggers and limits per run in the warm pool, and run data. |
| [JSON at step boundaries](0005-json-step-boundaries.md) | Go holds every value as producer text plus a lazily computed jsonb text and never decodes what it passes through; a routing table says which consumer sees which; one streaming canonicaliser checked against PostgreSQL; host-plugin results serialised by the steps' own function; the normalising walk dropped, because it was not equivalent. |
| [Module state](0006-module-state.md) | Two modes: `fresh` (a process per invocation, Windmill-equivalent) and `warm` (default: one module instance per script for the process lifetime, `globalThis` shared), plus host-provided modules as the one shared exception; `shared` left out of the first edition, and shared code added by [shared module code](0013-shared-module-code.md). |
| [Platform interface](0007-platform-interface.md) | Go service interfaces with local defaults and host replacements, and one interpolation walker with three modes and a host secret layer. Steps reach a facade by intercepted `fetch` (warm) or per-invocation tokens (`fresh`); unsupported routes fail with `UnsupportedPlatformCall`. Planned: hybrid dispatch to a real Windmill behind interfaces (its remote adapter held for a licence decision), result polls for the jobs that the job read answers for, identity settings; suspending flows take a declared stand-in. |
| [Host hooks](0008-host-hooks.md) | Thirteen hooks with typed selectors, as Go callbacks or TypeScript host plugins, which are declared up front, called over `host_call` and may wrap a step with `around`; a hook's error or timeout before the run's outcome latches the run. Step code has `wl.latch` and `wl.signal`, mandatory host-provided call sites fail closed, and scopes reach steps through host-provided modules. Planned: Go callbacks over go-plugin's broker, beside the settings that a run carries. |
| [Host adapters](0009-host-adapters.md) | The `wl` Go library is the only engine API, and the adapters are go-plugin, a Bun host SDK, plain gRPC, HTTP and a CLI. The Bun host SDK and the CLI are built; a Bun host installs the SDK from npm or vendors it. Planned: the engine binary as a go-plugin plugin serving `windmill_lite.v1.Runner`, `wl/plugin` to launch, pin and supervise it, three routes for hooks, and typed clients generated from Windmill schemas. |
| [Packaging](0010-packaging.md) | A bundle with a manifest of digests, tied to one exact engine version; a Bun build helper on the pinned Bun `1.4.3`; host-provided modules through a registry (D12), registered in the order of their load-time imports, with one shim generator and one define table and support module for both bundlers; a bundle digest in every handshake, trace and build-info call; a single executable; an OCI image written without Docker; a `verify` command. |
| [Differential testing](0011-differential-testing.md) | The oracle harness as the reference designs it, in public CI only; a typed, injective site-path encoding that also separates repeated module ids; `record`, `check` and `diff` in three configurations; only context-free cases lifted into one-step flows; a stub mode on the oracle and then on windmill-lite; benchmarks, a bounded-pool benchmark and run spans. |
| [Layout and milestones](0012-layout-and-milestones.md) | `wl/` and its packages `wl/plugin`, `wl/storetest` and `wl/redisstore` as the only public Go packages, components under `internal/`, and the npm package `windmill-lite` rooted at `harnesses/`. 56 milestones, each one pull request, whose "needs merged" covers everything their exit gates need. A contracts milestone comes first, then a walking skeleton on the pinned Bun, and oracle recordings in batches, each before the milestone it gates, with a manifest that can name each run's gate. |
| [Shared module code](0013-shared-module-code.md) | A bundle built with shared code compiles each build group's dependency modules once per process and gives each step module its own instance of them (D18), when one closure would make a `warm` or host-mode process outgrow its memory. Module state stays per script, so shared chunks, host-provided packages and one instance per package, which share it, are rejected. `fresh` runs the CLI's per-script bundles. The target is a peak of 768 MiB with 34 modules loaded. |

## Measurements behind the decisions

Apple M4, macOS, Go 1.27.3, Bun 1.4.2 unless a row names another version
(details in each record). windmill-lite pins Bun 1.4.3. A row on another
Bun was not measured again on 1.4.3, unless a row of its own measures the
same on 1.4.3:

| Question | Result | Used in |
|---|---|---|
| Go ↔ warm Bun round trip, one step | 12.5 µs p50 for 26 B, 18.4 µs for 3 KB, about 250 µs for 100 KB | [Where semantics live](0003-semantics-placement.md), [harness protocol](0004-harness-protocol.md) |
| First QuickJS-ng evaluation on a never-used runtime (setup excluded), by bound input | wazero: 53 µs at 50 B, 79 µs at 4 KB, 427 µs at 64 KB; native: 87 µs, 91 µs, 162 µs | [Where semantics live](0003-semantics-placement.md) |
| Fresh runtime setup | 221 µs in wazero, 84 µs native | [Where semantics live](0003-semantics-placement.md) |
| Bounded pool (wazero prototype), 8 slots, 8 callers with pauses, 4 KB | 120 µs p50, 260 µs p95 | [Where semantics live](0003-semantics-placement.md) |
| Native pool (`internal/expr/quickjs`), 8 slots, 8 callers with pauses, by bound value | 4 KB: 95 µs p50, 152 µs p95; 64 KB: 350 µs, 539 µs; 256 KB: 1.32 ms, 2.10 ms | [Where semantics live](0003-semantics-placement.md) |
| The budget gate through the expression pipeline (`internal/expr`), a predicate over a results value read from the database, 8 slots, 8 callers with pauses | 4 KB: 121 µs p50, 223 µs p95, 437 µs p99; 64 KB: 313 µs, 449 µs, 573 µs; 256 KB: 1.02 ms, 1.65 ms, 1.84 ms; all inside the gate | [Where semantics live](0003-semantics-placement.md) |
| Static musl engine's pool, 8 callers back to back, CPU per evaluation, musl's `malloc` against mimalloc | `linux/arm64`: 2.27 ms and 189 µs at 4 KB, 12.9 ms and 1.46 ms at 256 KB; `linux/amd64`: 1.74 ms and 340 µs, 8.45 ms and 2.82 ms; glibc's `malloc`: 261 µs, 1.69 ms, 433 µs, 3.04 ms | [Where semantics live](0003-semantics-placement.md) |
| The repository's step boundary benchmarks on Bun 1.3.10: one step that returns its argument, one run at a time, host mode | 142 µs p50 at 26 B, a mean of 150 µs of which writing `args.json` takes 114 µs; 935 µs at 32 KiB; 121 ms at 4 MiB, each configuration in a process of its own. Transport alone: 14 µs at 26 B, 123 µs at 32 KiB | [Harness protocol](0004-harness-protocol.md), [JSON at step boundaries](0005-json-step-boundaries.md) |
| The embedder shapes in host mode, twelve workloads (linux/amd64 AMD EPYC 7763 and linux/arm64 Neoverse-N2, 5 rounds of 20 requests per side): expressions answered in Go, heaps destroyed whole and inline memory lookups, against without | Engine CPU per step −38 to −66 % (amd64) and −33 to −61 % (arm64), A-typical's the least; p50 −7 to −13 % and −3 to −13 % | [Where semantics live](0003-semantics-placement.md) |
| The same, with the host's working directory on tmpfs against the runner's disk | p50 −3 to −12 % (amd64) and +1 to −7 % (arm64); engine CPU per step −11 to −28 % and −8 to −25 % | [Harness protocol](0004-harness-protocol.md) |
| The jsonb canonicaliser against a decode, sort and encode in Go | 240 to 361 MB/s against 11 to 20 MB/s, from 26 B to 4 MiB | [JSON at step boundaries](0005-json-step-boundaries.md) |
| Normalising walk versus native JSON round trip in Bun | faster, but not equivalent (top-level functions and symbols, own `__proto__` keys) | [JSON at step boundaries](0005-json-step-boundaries.md) |
| Fresh module state per invocation in one process | 0.6–2.1 ms for 0.3–1.3 MB bundles; re-importing leaks module records | [Module state](0006-module-state.md) |
| Step code reading a host scope through a separately bundled copy of the module, on Bun 1.3.10 | `undefined`; through a host-provided module, 16 of 16 isolated, compiled host included | [Packaging](0010-packaging.md) |
| One detached promise rejection in a shared Bun process, 1.3.10 and 1.4.2 | ends the process unless handlers are installed | [Harness protocol](0004-harness-protocol.md) |
| A host spinning forever, watched by a Go child that kills it after 1 s of missed heartbeats | killed 1.04 s after its last heartbeat; a restart-on-exit supervisor serves the next call | [Harness protocol](0004-harness-protocol.md) |
| esbuild CJS and ESM bundles resolving a host-provided module to the generated shim, Bun 1.3.10 and 1.4.2 | 16 of 16 isolated; `import.meta.env` rewritten in ESM only | [Packaging](0010-packaging.md) |
| A codebase build of six scripts importing a package of 400 modules, without and with one host-provided module listed, esbuild 0.28.0, Apple M4 | 99 ms and 6.1 s when the plugin resolved each import a second time; 99 ms and 99 ms when it loads the listed file's shim in place of the file | [Packaging](0010-packaging.md) |
| `Bun.build` on Bun 1.3.10 with a plugin, a package marked `sideEffects: false` whose entry re-exports `export { x } from "./x.js"` | The re-exported module is dropped, and the bundle references a binding it never defines, whenever a resolve filter matches the import of the package, even with a callback that resolves nothing. It is kept when no filter matches it, and with `export *` or an import and export. `ignoreDCEAnnotations` does not change it | [Packaging](0010-packaging.md) |
| The env-view test of [Q-WA-4](../semantics/client-api.md#q-wa-4), whether each invocation sees its own environment, Bun 1.3.10, the stock `windmill-client@1.767.0`: 16 concurrent invocations per variant, steps unbundled, bundled by `Bun.build` and bundled with the define rewrite, by namespace and default import | 16 of 16 on every channel (`process.env`, `Bun.env`, `import.meta.env`, a copy of `process.env`, a written variable, a deleted process variable, an unbundled dependency's `Bun.env`, the client's call), with and without the variables in the process environment. `Bun.env` and `import.meta.env` are isolated at run time, without the define rewrite | [Harness protocol](0004-harness-protocol.md), [packaging](0010-packaging.md) |
| A host-provided module's shim, imported lazily by `import()` in a bundle built by `Bun.build` on Bun 1.3.10, evaluated before the module is registered and again after | the first import throws the not-registered error; the second gets the shim's exports undefined, because the bundle initialises a module once. A late shim, an ES module that re-exports from a CommonJS module's registry getters, throws on the first read and returns the registered value on the second | [Packaging](0010-packaging.md) |
| The same on Bun 1.4.3, and `import()` expressions whose value goes unused, or only has exports read, in a bundle built by `Bun.build` | both imports of the plain shim throw the not-registered error: a module whose initialisation threw throws that error again on every later import. Bun gives the unused `import()` an empty object in place of the namespace (`(init(), {})`), and the one whose value only has exports read one too, each read going to the export's binding, so nothing reads the namespace's `then` | [Packaging](0010-packaging.md) |
| The host scope of [H11](0008-host-hooks.md#the-hooks), read through a host-provided module with a hand-written shim, in a host compiled with `bun build --compile`, Bun 1.3.10 | 16 of 16 concurrent runs read their own scope; a separately bundled copy reads nothing | [Host hooks](0008-host-hooks.md), [packaging](0010-packaging.md) |
| The host scope of [H11](0008-host-hooks.md#the-hooks), read through an esbuild CommonJS codebase script whose import of the host-provided module resolves to the generated shim, in a host compiled with `bun build --compile`, Bun 1.3.10 | 16 of 16 concurrent runs read their own scope, on Bun's CommonJS loader | [IR, loader and validation](0002-ir-and-loader.md), [packaging](0010-packaging.md) |
| The same esbuild CommonJS codebase bundle on Node 20 and on the Bun harness, with the harness's wrapper rules on both ([D13](0001-equivalence-contract.md#5-named-deviations)) | They agree on named exports and overrides, `__dirname`, run-time `require` of assets and of the bundle's own file, thrown errors and assets. They differ on the runtime's identity, an override naming `default`, and a `package.json` asset declaring `"type": "module"` (Node fails to load the bundle) | [Equivalence contract](0001-equivalence-contract.md), [IR, loader and validation](0002-ir-and-loader.md) |
| The pinned `windmill-cli@1.767.0`'s codebase uploads against esbuild 0.28.0 through its Go API with the settings of [WR-47](../semantics/languages.md#wr-47), the codebase build options (the build's parity check) | the same bytes for seven fixtures (each WR-47 setting, an inlined package, an asset in a tar); a text upload's line breaks are CRLF, the form-data encoding's | [IR, loader and validation](0002-ir-and-loader.md) |
| The same check with the pinned `windmill-cli@1.862.0` against esbuild 0.28.2 | the same bytes for the same seven fixtures, a text upload's line breaks still CRLF; the CLI makes the same three requests: the user, the deployed script and one `create_snapshot` | [IR, loader and validation](0002-ir-and-loader.md) |
| A plain shim that throws while it loads, imported by `import()` before and after its module is registered, and a module without exports that throws, required twice, in esbuild bundles (CommonJS and ESM output) run on Bun 1.4.3 | esbuild 0.28.2: every import throws the first error again, even after registration; 0.28.0: the later imports got undefined exports, and the module without exports seemed to load | [Packaging](0010-packaging.md) |
| `process.exit(code)` on Bun 1.3.10 | A safe integer, or a string other than `""` that `Number` reads as one (`" "` is 0), exits with it modulo 256 (`258` exits 2, `-1` and `"-1"` 255, `9007199254740991` 255). The values `2^53`, `"9007199254740993"`, `1.5`, `NaN`, `"x"`, `""`, `true`, `{}` and `[5]` throw a `RangeError` or `TypeError` before exiting and leave `process.exitCode` as it was | [Harness protocol](0004-harness-protocol.md) |
| A step in `fresh` mode: spawn, handshake, invocation and result of a one-line echo step, 50 sequential runs after 10 warm-up runs, Bun 1.3.10, Apple M4, engine built without the race detector | 21.5 ms p50, 23.4 ms p95, against 48 µs p50 and 131 µs p95 for the same step in a warm process | [Harness protocol](0004-harness-protocol.md), [module state](0006-module-state.md) |
| Which child-process calls see a replaced `process.env` on Bun 1.3.10 | `child_process.spawn`, `exec` and `Bun.$` read it at the call; `spawnSync`, `execSync` and `Bun.spawn`/`Bun.spawnSync` pass the process's own environment, so the harness gives every call made in an invocation an explicit one | [Harness protocol](0004-harness-protocol.md) |
| Patching `node:timers` and `node:child_process` on Bun 1.3.10 | assignments to the CommonJS object before a module's first import of the builtin reach its named ESM imports; `module.syncBuiltinESMExports()` does not update a namespace imported earlier | [Harness protocol](0004-harness-protocol.md) |
| Which child-process calls honour `detached` on Bun 1.3.10 | `spawn`, `fork`, `Bun.spawn` and `Bun.spawnSync` start a detached child in a new session and group; `exec`, `execFile`, `spawnSync`, `execSync` and `execFileSync` ignore it, leaving the child in the caller's group. Every `node:child_process` function starts its child through the global `Bun.spawn` or `Bun.spawnSync`, once, passing `detached` on only from `spawn` and `fork`; `Bun.$` uses neither; `process.kill(-pgid, …)` signals a group | [Harness protocol](0004-harness-protocol.md) |
| What the engine can read of another process of its user | on macOS `kern.procargs2` gives its executable's path and arguments but not its environment, and `ps -E` shows none either; Linux's `/proc/<pid>/cmdline` and `environ` give both. So a child group's guardian carries its token in its arguments, `sleep 999999999.<token>`, which macOS `sleep` accepts (it refuses `2147483647.<digits>`) | [Harness protocol](0004-harness-protocol.md) |
| Whose child a group's guardian is, under macOS `/bin/sh`, `dash` and `bash` | `exec` keeps the shell's pid and its children. So a guardian the shell forks before it execs the step's command is a child of the command, and a command that waits for all its children (`wait()`, `waitpid(-1)`) waits for it until the step times out. A guardian a subshell starts in the background is reparented when the subshell exits, which the shell waits for before the `exec`, so the command has no extra child | [Harness protocol](0004-harness-protocol.md) |
| Who reaps a child group's guardian | Its parent once the prologue's subshell exits: PID 1 or the nearest subreaper. Measured in a Linux container with the engine's test process as a subreaper (under `--init`) and as PID 1 (without), the engine is that parent. Each guardian, and each orphan a member leaves, stays a zombie until the engine waits for it; so the engine reaps them, by pid | [Harness protocol](0004-harness-protocol.md) |
| `process._getActiveHandles()` on Bun 1.3.10 with a pending timeout and interval | an empty list, so leaks are counted in the harness's own per-invocation registry | [Harness protocol](0004-harness-protocol.md) |
| `process._getActiveHandles()` on Bun 1.4.3, with a pending timeout, interval and immediate | an empty list, as on 1.3.10 | [Harness protocol](0004-harness-protocol.md) |
| The timer functions Bun 1.4.3's networking modules call through the globals the fence wraps: `node:http` requests answered, refused and to `localhost`, a keep-alive agent, request and socket timeouts, and a `node:http` server | `node:net` arms a socket's idle `setTimeout` (5 s on the agent's kept-alive sockets) and a `setTimeout` between connection attempts, and closes a destroyed socket's handle in a `setImmediate`; the server times its sockets and tracks its connections with a `setInterval`. Bun 1.3.10 called none of them. The callers are `node:net`'s `setTimeout`, `closeSocketHandle` and `internalConnectMultiple` and `node:_http_server`'s `setTimeout` and `setupConnectionsTracking` | [Harness protocol](0004-harness-protocol.md) |
| Which function Bun 1.4.3 reports as the caller of a timer that a step's callback starts, when a networking module calls that callback | A step's `lookup` callback that `node:net` calls, or a listener that `node:events` calls, shows that module's function as the caller of a timer it returns: JavaScriptCore's proper tail calls drop its frame | [Harness protocol](0004-harness-protocol.md) |
| The host-mode watchdog, `killAfter` 1 s, a step that spins forever | the host was killed 1.00 s after the step's run was sent; the restarted host served the next run | [Harness protocol](0004-harness-protocol.md) |
| Per-step latency of the walking skeleton, a one-step flow with one javascript transform, 300 sequential runs on Bun 1.3.10, engine built without the race detector | spawned warm harness: 56 µs p50, 120 µs p95 per run (invoke to result 31 µs p50); host mode in a compiled host, timed around `run`: 78 µs p50, 126 µs p95 | [Where semantics live](0003-semantics-placement.md), [harness protocol](0004-harness-protocol.md) |
| A compiled Bun 1.3.10 host in host mode, resident set after a full collection. The synthetic reproduction: six Bun scripts importing one 4.36 MB module of 40,000 strings. A closure shaped like an embedder's (34 CommonJS codebase scripts of 6.89 MB importing one module over mongoose, the stock client, js-yaml, Babel and a generated client package) | Per script: 6.3 to 6.7 MiB and 53 MiB per further module, 1,911 MiB with eight concurrent runs, a 2,015 to 2,042 MiB peak after 1,000 runs. One shared instance: 0.1 and 0.2 MiB per module, a 185 to 194 MiB peak. Shared code with an instance per script (prototype): 0.2 and 11 MiB per module, a 519 to 530 MiB peak | [Shared module code](0013-shared-module-code.md) |
| Shared chunks, one instance per package, and packages as host-provided modules (registry shims) in a loader model, Bun 1.3.10: memory | 120 to 129 MiB, 132 MiB and 127 MiB for the closure's 34 modules, against 1,497 MiB per script. Chunks of the synthetic reproduction cost 0.5 to 0.6 MiB per further module, against 6.8 to 7.8 MiB | [Shared module code](0013-shared-module-code.md) |
| The same three layouts: their semantics | Chunks and one instance per package make the second of two scripts that register a Mongoose model of one name fail, as any shared instance does. Chunks also run a CommonJS script as a strict ES module, where an assignment to an undeclared variable throws | [Shared module code](0013-shared-module-code.md) |
| The shared layout in host mode, a compiled Bun 1.3.10 host, resident set after a full collection | Over several runs, the synthetic reproduction costs 0.17 to 0.27 MiB per further module. The embedder-shaped closure holds 439 to 450 MiB with 34 modules loaded and 453 to 462 MiB with eight concurrent runs, and peaks at 492 to 497 MiB after 1,000 runs, at 9.9 to 10.3 MiB per further module. A closure in Bun's own CommonJS form loads its first module at the per-script bundle's cost; one that Bun transpiled cost about 21 MiB more | [Shared module code](0013-shared-module-code.md) |
| The shared layout in host mode on Linux, compiled Bun 1.3.10 and 1.4.3 hosts in one job, each test in a container limited to 1,536 MiB without swap: linux/amd64 and linux/arm64, medians of five rounds | Bun 1.3.10: 9.1 and 9.2 MiB per further module, and peaks of 506.7 and 484.4 MiB after 1,000 runs. Bun 1.4.3: 13.2 and 10.6 MiB per further module, and peaks of 712.8 and 585.3 MiB. On Bun 1.4.3 the host starts 16 to 21 MiB smaller, and per script the closure costs 4 to 5 MiB less per further module | [Shared module code](0013-shared-module-code.md) |
| The peak resident set that `getrusage` reports on Linux for a process that Go's `os/exec` starts | It keeps the peak of what the process was before its `execve`, its parent's: a memory-test host of 38.5 MiB reported 616 to 682 MiB at connect. `VmHWM` in `/proc/self/status` is the process's own | [Shared module code](0013-shared-module-code.md) |
| `process.resourceUsage().maxRSS` on macOS, Bun 1.3.10 and 1.4.3 | In bytes on Bun 1.3.10, and in kilobytes on Bun 1.4.3, as Node reports it on every platform | [Shared module code](0013-shared-module-code.md) |
| Bun 1.4.3's limit and JavaScriptCore's RAM size, the same closure on Linux | Without a container Bun 1.4.3 takes the runner's 16 GiB as its limit, as on a host that mounts a cgroup v1 hierarchy too, and its peaks overlap those in the container. Forcing the RAM size to 16 GiB rather than 1.5 GiB raises the arm64 peak from 574.1 to 662.2 MiB, and moves the amd64 one within the spread | [Shared module code](0013-shared-module-code.md) |
| `BenchmarkRunPeaks` on Bun 1.4.3 against 1.3.10 in one job, in a container limited to 1,536 MiB, linux/amd64 and linux/arm64 | The host's peak is 15 to 29 % lower on Bun 1.4.3. The engine's moves by at most 2.4 %, which `benchstat` does not find significant | [JSON at step boundaries](0005-json-step-boundaries.md) |
| The modules Bun 1.3.10 has built in: `node:module`'s `builtinModules`, `Bun.resolveSync` of each listed name alone and after `node:`, and imports of them from two directories, one whose `node_modules` holds packages named `ws`, `undici`, `fs` and `bun` | Node's modules and their subpaths, and `bun`, `bun:ffi`, `bun:jsc`, `bun:sqlite`, `bun:test`, `bun:wrap`, `ws` and `undici`; `ws` and `undici` load Bun's own copy even when `node_modules` holds a package of that name. Every listed name resolves alone to a built-in module, not to the package | [Shared module code](0013-shared-module-code.md) |
| The same names after `node:`, Bun 1.3.10 | Node's names resolve, and Bun's own throw "No such built-in module": an `import()` of `node:ws`, `node:bun`, `node:bun:sqlite` or `node:undici` rejects with that message from CommonJS, ES module and TypeScript files, and a `require` of `node:ws` throws it. `import.meta.resolve` returns every `node:` spelling unchanged, and `isBuiltin` accepts names the list does not hold, such as `node:test`, `bun:main` and `node:ws`. `import("node:nope")` fails with the same message from both directories | [Shared module code](0013-shared-module-code.md) |
| The modules Bun 1.4.3 has built in, by the same probe | As on 1.3.10, 144 built-in specifiers, with two changes: `builtinModules` no longer lists `bun:wrap`, which still resolves and loads, and lists `node:sqlite` with its prefix, the only spelling that resolves (`sqlite` is a package specifier, and `node:node:sqlite` is rejected). `node:test` and `node:quic` resolve but are not listed. `ws`, `undici`, `fs` and `bun` still load Bun's own module when `node_modules` holds a package of that name, and `node:` before Bun's own names is still rejected | [Shared module code](0013-shared-module-code.md) |
| The shared layout with lazily imported built-in modules, a compiled Bun 1.3.10 host in host mode: the embedder-shaped closure whose shared module imports `node:async_hooks` by `import()` and whose generated package imports `node:fs/promises` by `import()` | One run: 451 MiB with 34 modules loaded, 464 MiB with eight concurrent runs, a 502 MiB peak after 1,000 runs, and 10.3 MiB per further module. If those `import()` expressions caused a fallback, all 34 scripts would keep their per-script bundles: 36.6 MiB per further module and a 1,340 MiB peak | [Shared module code](0013-shared-module-code.md) |
| `process.env`, `Bun.env` and `import.meta.env` on Bun 1.4.3 (black-box probe) | One object at start. `process.env` can be assigned; `Bun.env` and the global `Bun` cannot be redefined. The object takes only configurable, writable, enumerable data properties, a write of a key it does not hold walks to its prototype, and it is not the C environment | [Harness protocol](0004-harness-protocol.md), [packaging](0010-packaging.md) |
| The Q-WA-4 env-view test on Bun 1.4.3, warm and fresh, with and without the variables in the process | 16 of 16 on every channel; fresh runs make no client call | [Harness protocol](0004-harness-protocol.md), [packaging](0010-packaging.md) |
| `AsyncLocalStorage.getStore()` at the top level of a module that `import()` loads inside `run()`, Bun 1.4.3 against 1.3.10 | `undefined` on 1.4.3, for ESM and CommonJS modules and their static imports; the caller's store on 1.3.10 | [Harness protocol](0004-harness-protocol.md) |
| The same through `require()` on Bun 1.4.3, then `import()` of the file, against a loading-invocation variable set around `import()` | `require()` keeps the store at the top level, after a top-level `await` and in the work the module starts. With a top-level `await` it throws a `TypeError` once the evaluation started, and `import()` waits for it. `import()` then gives the same namespace without a second evaluation, and loading takes as long. The variable is seen by host code, process-scope timers and microtasks during the load, and misses timers the module starts | [Harness protocol](0004-harness-protocol.md) |
| Patching `node:timers` and `node:child_process` on Bun 1.4.3, by the probe of 1.3.10 | As on 1.3.10: assignments to the CommonJS object before a module's first import of the builtin reach its named, namespace and default imports, with or without `module.syncBuiltinESMExports()`. A module that imported it by name earlier keeps Bun's functions, and so does every later importer; the default import and `require` see the assignments. A timeout's `refresh()` re-arms it once it fired, not once it was cleared | [Harness protocol](0004-harness-protocol.md) |
| Which child-process calls see a replaced `process.env` on Bun 1.4.3 | `child_process.spawn`, `exec`, `execFile`, their sync forms and `Bun.$` read it at the call. `Bun.spawn` and `Bun.spawnSync` pass the environment the process started with: neither the replacement nor what was written to the original object. So the harness still gives every call made in an invocation an explicit one | [Harness protocol](0004-harness-protocol.md) |
| Which child-process calls honour `detached` on Bun 1.4.3 | `spawn`, `fork`, `spawnSync`, `execSync`, `execFileSync`, `Bun.spawn` and `Bun.spawnSync` start a detached child in a new session and group; `exec` and `execFile` ignore it. Every `node:child_process` function still starts its child through the global `Bun.spawn` or `Bun.spawnSync`, once, and now passes `detached` on from the sync forms too | [Harness protocol](0004-harness-protocol.md) |
| The async context of the process-level error handlers on Bun 1.4.3, for a throw from a callback started inside `run()`, against 1.3.10 | A timer's, `process.nextTick`'s, `exec`'s, a child's `exit` listener's, a socket's `error` listener's and `zlib`'s reach `uncaughtException` with the caller's store, and so now do those of `node:fs` and `dns.lookup`, which 1.3.10 raised as unhandled rejections without one. `queueMicrotask`'s and `crypto`'s have no store. Rejections, `fs.promises`' and `fetch`'s included, reach `unhandledRejection` without one | [Harness protocol](0004-harness-protocol.md) |
| `process.exit(code)` on Bun 1.4.3 | The same codes exit with the same statuses, and the same are refused with the same error names and codes, as on 1.3.10. Only the range text changed: `It must be >= -9007199254740991 && <= 9007199254740991. Received 9_007_199_254_740_992`, where 1.3.10 wrote `and` and plain digits | [Harness protocol](0004-harness-protocol.md) |
| `Bun.build` with a plugin whose resolve filter matches the import of a package marked `sideEffects: false` that re-exports `export { x } from "./x.js"`, its callback resolving nothing, Bun 1.4.3 against 1.3.10. Also the tree of [WR-27](../semantics/languages.md#wr-27)/c: a transitive bare import of a package named `leaf.ts`, under a filter for every specifier that ends in `.ts` | Bun 1.4.3 keeps the re-exported module in both, and the bundle returns its value. Bun 1.3.10 drops it, and reading the binding throws a `ReferenceError` (`v is not defined` in WR-27/c's tree) | [Equivalence contract](0001-equivalence-contract.md), [packaging](0010-packaging.md) |
| `vm.SourceTextModule` with a top-level `await`, linked and evaluated in a new `node:vm` context, Bun 1.4.3 against 1.3.10 | Bun 1.4.3 evaluates it, and the process goes on. Bun 1.3.10 ends the process with exit status 133 | [Packaging](0010-packaging.md) |
| The C stack of one JavaScript call of a simple recursion, by compiler, and the calls that fit an unscaled 1 MiB: `linux/amd64` on GitHub's runners, and Apple clang on the Apple M4 | GCC 13.3: 784 B, 1 335 calls; GCC 14: 1 632 B, 640; GCC 15.2: 1 728 B, 604. clang 18 and 21: 1 024 to 1 040 B, 1 006 to 1 021; `zig cc` 0.15.2, static musl: 1 008 B, 1 038; Apple clang 21, `darwin/arm64`: 992 B, 1 054 (CI runs 43131238909 and 43132472348). The `Expression engine` CI job prints the current cost on every run | [Where semantics live](0003-semantics-placement.md) |
| What grows the frame of the interpreter's `JS_CallInternal` with GCC 14 and 15, `linux/amd64` (`-fstack-usage`, each pass that `-O2` adds to `-O1` turned off alone) | `-finline-small-functions`: 1 584 to 1 680 B with it, 688 to 704 B without, where GCC 12 and 13 give 736 B. Every other pass, the hardening flags, `-march` and `-std` move it by 112 B at most | [Where semantics live](0003-semantics-placement.md) |
| The total time of five programs (a recursion, a loop, JSON, arrays and strings) with the inlining of small functions turned off, `linux/amd64` | GCC's `-fno-inline-small-functions` for the whole package: 4 to 5 % longer with GCC 15, 13 to 14 % with GCC 13. The same as a `#pragma GCC optimize` in `qjs_quickjs.c` only: 2 to 5 % and 8 %, and per program from 21 % faster to 19 % slower. clang's `-fno-inline-functions`: 2.2 to 2.6 times longer (CI runs 43135571202 and 43148947865) | [Where semantics live](0003-semantics-placement.md) |
| How deep a recursion and nesting fit under the scaled default stack limit, by build: a recursion's calls, then the levels of `JSON.parse`, `JSON.stringify` and parenthesised source text | GCC 13.3, `linux/amd64` (limit 1 070 422 B): 1 362, 13 339, 4 170, 995. GCC 15.2, `linux/amd64` (2 359 296 B): 1 362, 29 426, 7 355, 1 937. `zig cc`, static `linux/amd64` (1 376 256 B): 1 362, 8 578, 5 046, 1 129; static `linux/arm64` (1 310 720 B): 1 362, 10 211, 5 836, 984 (CI run 43148947834) | [Where semantics live](0003-semantics-placement.md) |

## Contested findings

The design does not pick sides silently.
[§6 of the equivalence contract](0001-equivalence-contract.md#6-findings-that-are-not-settled)
lists each rule that the reference left contested and its readings, with
the default and the recording that settles it where the readings differ. No
finding of the reference is unsettled now
([findings that are not settled](../semantics/README.md#not-settled)).

- Where the readings agree in the reference configuration, no default is
  needed.
- Until a recording settles a rule, the implementation follows the default
  in one function per rule, and no build switch keeps both readings.
- A finding that proves not recordable on CE gets a build switch only
  through a change to that record.

## Requests to the semantics team

The build team asks the [semantics team](../glossary.md#semantics-team) to
cover a behaviour through a numbered request here, never with a source
excerpt ([AGENTS.md](../../AGENTS.md#clean-room)). Code, tests and the
reference cite these requests by number. Request 14 is open; each answered
request names the findings that answer it. Where an answer differs from a
reading that the code had taken, the code follows the answer.

1. **Native boundary rules** for `bunnative` and `nativets` steps.
   Answered by [WR-40](../semantics/languages.md#wr-40) to
   [WR-46](../semantics/languages.md#wr-46): which steps run natively, the
   fresh isolate, the result's serialisation, the error shapes, the timeout
   text and limits, the globals and `fetch`, and streaming.
   [WR-43](../semantics/languages.md#wr-43) gives a `bunnative` stack line
   from the minified bundle, as `throw Error("x")`.
2. **Codebase bundling details**: the default `format`, the esbuild version,
   `inject` and `customBundler`, where `assets` land at run time, and the
   Node version that runs CommonJS bundles. Answered by
   [WR-47](../semantics/languages.md#wr-47) to
   [WR-50](../semantics/languages.md#wr-50).
3. **The expression engine's stack limit.** Answered by
   [JE-48](../semantics/expressions.md#je-48): QuickJS-ng's built-in 1 MiB,
   which windmill-lite configures and scales to its build's frames.
4. **What `jsonb` rejects besides NUL**: an unpaired UTF-16 surrogate escape,
   and a number outside `numeric`'s range. Answered by
   [FX-51](../semantics/execution.md#fx-51): an unpaired surrogate in a step
   result or a transform's value reaches `jsonb` and fails with
   `invalid input syntax for type json`. A `$jsonvar:` value fails earlier,
   in the worker's parser, and no JavaScript producer can emit such a
   number.
5. **The [oracle](../glossary.md#oracle)'s recordings of flow-execution
   cases.** Answered:
   [FX-34](../semantics/execution.md#fx-34) and
   [FX-32](../semantics/execution.md#fx-32) are corrected, so a deployed
   body's copied failure module keeps the id `failure`. The other
   observations are in [OR-6](../semantics/oracle.md#or-6),
   [FX-3](../semantics/execution.md#fx-3),
   [OR-11](../semantics/oracle.md#or-11),
   [OR-28](../semantics/oracle.md#or-28),
   [FX-17](../semantics/execution.md#fx-17),
   [FX-35](../semantics/execution.md#fx-35) and
   [FX-50](../semantics/execution.md#fx-50).
6. **The oracle's recordings of control-flow cases and of the result
   cache.** Answered: the cases they recorded are `oracle-observed`.
   - [CF-12](../semantics/control-flow.md#cf-12): after an unhandled error
     stop in a sequential loop with `skip_failures: false`, the loop fails,
     and the enclosing flow fails with the loop's array without running its
     next step. With `skip_failures: true` the loop succeeds and the flow
     continues.
   - [FX-40](../semantics/execution.md#fx-40): the iterations of one
     collapsed loop share a cache entry when caching applies to the body and
     their transformed arguments are equal. An inline body shares too, since
     its jobs carry one flow-node id.
   - [CF-6](../semantics/control-flow.md#cf-6): the container defaults that
     `generate-metadata` writes.
   - [CF-7](../semantics/control-flow.md#cf-7): in a `same_worker` flow, a
     parallel loop with several children fails with an error, and one with a
     single child succeeds.
7. **The oracle's recordings of expression cases, CF-19 and CF-V6.**
   Answered: [CF-19](../semantics/control-flow.md#cf-19) and
   [CF-V6](../semantics/control-flow.md#cf-v6) are confirmed, so a qualifying
   line of a multiline expression answers by itself.
   [JE-48](../semantics/expressions.md#je-48),
   [JE-28](../semantics/expressions.md#je-28),
   [JE-35](../semantics/expressions.md#je-35),
   [JE-2](../semantics/expressions.md#je-2) and
   [JE-33](../semantics/expressions.md#je-33) state what the oracle
   recorded.
8. **The textual pipeline's unstated details**: which characters trimming
   removes, empty and `length` keys, where an unbalanced call ends,
   statement mode and the quote scan, the wrapper, line ends, and the texts
   that the shortcuts return for stored values. Answered:
   [JE-5](../semantics/expressions.md#je-5),
   [JE-7](../semantics/expressions.md#je-7),
   [JE-9](../semantics/expressions.md#je-9),
   [JE-12](../semantics/expressions.md#je-12),
   [JE-13](../semantics/expressions.md#je-13) and
   [JE-14](../semantics/expressions.md#je-14) confirm windmill-lite's
   readings, and [JE-11](../semantics/expressions.md#je-11) the end of the
   text for ASCII.
   - [JE-6](../semantics/expressions.md#je-6) to
     [JE-8](../semantics/expressions.md#je-8) and
     [JE-44](../semantics/expressions.md#je-44) are corrected: a
     `flow_input` member is `jsonb`'s spaced text, and a `previous_result`
     after a loop or `branchall` is compact text printed again.
9. **The expression program's unstated details**: a thrown value that is
   not an `Error`, "or else" in an entry, and the positions of deep values.
   Also whether `results` and `error` can be assigned, `__proto__` keys, a
   replaced `JSON.stringify`, and the build-dependent overflow depth and
   `Math` digits. Answered by [JE-21](../semantics/expressions.md#je-21),
   [JE-17](../semantics/expressions.md#je-17),
   [JE-49](../semantics/expressions.md#je-49),
   [JE-10](../semantics/expressions.md#je-10),
   [JE-16](../semantics/expressions.md#je-16),
   [JE-15](../semantics/expressions.md#je-15),
   [JE-48](../semantics/expressions.md#je-48) and
   [JE-1](../semantics/expressions.md#je-1).
   - The deviations for the `Math` digits and the overflow depth,
     [D16 and D17](0001-equivalence-contract.md#5-named-deviations), stand
     until [Q-JE-16](../semantics/expressions.md#q-je-16) records which glibc
     version the image ships and which compiler built its engine.
10. **The readings that the answers to requests 8 and 9 left open**: where
    the call rewrite's character count starts, whether fetched values are
    rebuilt by assignment, and the notation in which the backend writes a
    double. Answered by [JE-11](../semantics/expressions.md#je-11) (the
    count starts at the call), [JE-16](../semantics/expressions.md#je-16)
    (fetched values are parsed with `JSON.parse`) and
    [FX-42](../semantics/execution.md#fx-42) rule 9 (shortest digits,
    plain decimal for an exponent from −5 to 15).
11. **The oracle's recordings of the remaining cases**, which the manifest
    `internal/oracle/testdata/batches/e3e.json` lists. Answered: the
    recorded cases are `oracle-observed`, and
    [FX-36](../semantics/execution.md#fx-36),
    [FX-49](../semantics/execution.md#fx-49),
    [WR-16](../semantics/languages.md#wr-16) and
    [WR-28](../semantics/languages.md#wr-28) are confirmed, except WR-28's
    drafted rule for a dynamic import with no rewrite trigger.
    [JE-23](../semantics/expressions.md#je-23) states that the order in
    which a step's transforms run is not fixed.
12. **A child process left behind in a warm process.** Answered for the
    ordinary execution path by [WR-55](../semantics/languages.md#wr-55): a
    job completes once its process has exited and its process group is
    empty, and a timeout or a cancellation kills the group. For the runners
    of a squashed loop, [Q-CF-19](../semantics/control-flow.md#q-cf-19) is
    open. The request also asked what a [`warm`](../glossary.md#warm) process should do, which is
    the design's own question ([open design questions](#open-design-questions)).
13. **A container child result the backend cannot read back.** Answered by
    [FX-42](../semantics/execution.md#fx-42) rule 9,
    [JE-44](../semantics/expressions.md#je-44) and the cases
    [FX-42/m to FX-42/o](../semantics/conformance.md#fx-42): the outcome
    depends on where the chain of transitions started. The engine follows
    both routes, but cancels a waiting flow at once
    ([D20](0001-equivalence-contract.md#5-named-deviations)).
14. **The depths in JE-48's design note.** Open: replace the note's numbers
    with a link to [D17](0001-equivalence-contract.md#5-named-deviations),
    or drop them.
    - The note of [JE-48](../semantics/expressions.md#je-48) says that
      windmill-lite's engine overflows the recursion of depth 1 000 only at
      1 037 to 1 089 frames.
    - The engine now scales its stack limit to its build's frames
      ([where semantics live](0003-semantics-placement.md)), so about 1 362
      such calls fit on every build measured; an unscaled build with GCC 15
      fitted 604.

## Open design questions

- **A child that a `warm` or [host-mode](../glossary.md#host-mode)
  [invocation](../glossary.md#invocation) leaves in its process
  group.** Windmill holds a job open until every child left in its process
  group exits ([WR-55](../semantics/languages.md#wr-55)). A `warm` or host
  process serves many invocations, so at
  [settlement](../glossary.md#settle) windmill-lite reports
  the child as `left_behind` (`child`) and kills its process group.
  - Neither the child nor what it left in that group runs on after the
    step settles. Other work the step leaves running is cut off only as far
    as the settle-time fence reaches
    ([D9](0001-equivalence-contract.md#5-named-deviations)).
  - The question: should a warm process instead hold the invocation's
    result until its children's groups are empty, with the timer, `fetch`,
    log and platform fences applied at the return?
  - Until it is decided, a script that must let its children finish runs
    `fresh`, which does what Windmill does
    ([harness protocol](0004-harness-protocol.md#the-shared-process-failure-model)).
- **A halt that ends only a subflow.** A halt ends the whole run
  ([H8](0008-host-hooks.md#the-hooks)), so a flow that wraps a
  subflow cannot halt it at a module and go on to shape what it read. A
  host maps the halted run's trace instead.
  - The question: should a hook end a by-path subflow early, with the
    results so far, and let its parent continue?
- **A first-write signal from step code.** The first-write signal
  ([H3](0008-host-hooks.md#the-hooks)) is asked before the first write-capable step launches, and
  the run counts as written from that approval. A step that reads its
  sources before its first write is counted as written from its start.
  - The question: should step code report its own first write, for
    example through the step API (`wl`), with the approval at launch as the
    fallback?
- **A host provider's answers across runs.** The read cache serves one run
  ([D21](0001-equivalence-contract.md#5-named-deviations)), so a host that
  reads its values from an API pays a read per path in every run.
  - The question: should a cache of a provider's answers, with a time limit
    and keyed by a scope that the run names, serve later runs too? A value
    changed at its source would then reach runs only after the limit: a
    deviation of its own.

## Recording requests

These requests ask for observations that the reference cannot settle from
the source. Most need a running Windmill, the [oracle](../glossary.md#oracle);
a few optional checks need only PostgreSQL or the expression evaluator. They
are requests, not design decisions, and all but two come from the semantics
team. The build team made the other two: one that the oracle has recorded,
and the result polls for a job that a flow created, which is open.

Each oracle request becomes a [fixture](../glossary.md#fixture) recorded in
the session it names, or is deferred with its reason in a recording batch's
manifest under `internal/oracle/testdata/batches/`
([internal/oracle](../../internal/oracle/README.md#recording-batches)).

- Every expectation below is the reference's prediction; the named case or
  question holds the exact text.
- Compare location suffixes as `<loc>` and mask job ids
  ([OR-14](../semantics/oracle.md#or-14),
  [FX-50](../semantics/execution.md#fx-50)).
- Nothing here may commit the oracle's database credentials unmasked.

The requests name these oracle sessions:

| Session | Configuration |
|---|---|
| The reference session | One worker with the default worker tags. Requests use it unless they name another. |
| The two-worker session | Two workers, so that a driver step can wait for a job it starts ([OR-5/b](../semantics/conformance.md#or-5)). |
| `native` | The standalone container's worker also has `WORKER_TAGS=nativets`, so that it serves native steps ([Q-WR-21](../semantics/languages.md#q-wr-21)). |
| Object storage | Workspace storage pointed at a local S3-compatible store started beside the oracle. The oracle's harness starts no such store yet. |

### Open requests

#### Loops, branches and slots

- **What a squashed loop's runner does**
  ([Q-CF-19](../semantics/control-flow.md#q-cf-19),
  [CF-25/e](../semantics/conformance.md#cf-25),
  [WR-55](../semantics/languages.md#wr-55)). The oracle's recordings show
  that the runner keeps one runtime per body step. Their reading step looked
  the iteration jobs up by `WM_JOB_ID`, which reads `NOT_AVAILABLE` in a
  squashed runtime, so it read no logs or times.
  - Repeat `control-flow/cf-25-runtime`'s `for_one_squash`,
    `for_two_squash` and the `child_plain`/`child_squash` pair, deployed
    and run by path. The body script also returns `pid` (`process.pid`)
    and `env`: the values of `WM_JOB_ID`, `WM_FLOW_JOB_ID`,
    `WM_ROOT_FLOW_JOB_ID`, `WM_FLOW_STEP_ID`, `WM_USERNAME` and `WM_EMAIL`.
  - The last step finds the iteration jobs through the loop's status:
    `GET /api/w/<ws>/jobs_u/get/<WM_FLOW_JOB_ID of the last step>`,
    `flow_status.modules[0].flow_jobs`. For the two-step body these are
    `flownode` child flows: read each one the same way for its steps' job
    ids. Then read `/jobs_u/get_logs/<id>` and
    `/jobs_u/completed/get/<id>` for each job.
  - The step returns only stable values: the number of distinct `pid`s per
    step, and the `env` values with any UUID replaced by `"<uuid>"`. It
    also returns whether any log line starts `dedicated worker bun:`, and,
    for the child pair, whether each iteration job's completion came at
    least 4 s after its start.
  - Predicted: one `pid` per body step across the three iterations.
    `WM_JOB_ID` is `"NOT_AVAILABLE"`; `WM_FLOW_JOB_ID`,
    `WM_ROOT_FLOW_JOB_ID` and `WM_FLOW_STEP_ID` are `""`; `WM_USERNAME` is
    `"dedicated_worker"` and `WM_EMAIL` is `"dedicated_worker@windmill.dev"`.
  - These values come from the public start of a dedicated runtime; the CE
    image's runner module is closed source. Unsquashed, every iteration
    waits for its child ([WR-55/a](../semantics/conformance.md#wr-55)).
    There is no prediction for the log line, or for whether a squashed
    iteration waits for its child.
- **An empty loop in the failure slot**
  ([CF-31/d](../semantics/conformance.md#cf-31),
  [Q-CF-17](../semantics/control-flow.md#q-cf-17)). The oracle's walker
  refuses the run, so read it through the API, in the two-worker session.
  - A Bun driver step runs CF-31/d's two flows by path:
    `[a: fail("x"), b: ret(x) x = previous_result]` and `[a: fail("x")]`.
    Each has as its failure module a `forloopflow` `failure` over `[]`
    with the body `[h: ret(1)]`.
  - The driver waits for each and returns the job's `success` and
    `result`. From `GET /api/w/<ws>/jobs_u/get/<id>` it also returns each
    `flow_status.modules` entry's `id` and `type`, and
    `flow_status.failure_module.type`.
  - Predicted: both succeed with `[]`. The entries are
    `[{"id": "failure", "type": "Success"}, {"id": "b", "type": "Success"}]`
    and `[{"id": "failure", "type": "Success"}]`, and `b`'s result is `[]`.
- **A parallel loop stopped by its own predicate** (optional;
  [CF-7](../semantics/control-flow.md#cf-7)'s drafted
  `stop_after_all_iters_if` sentence;
  [CF-13](../semantics/control-flow.md#cf-13)).
  - Deploy and run by path:
    `[l: forloopflow(iterator: [1, 2], parallel: true, skip_failures: true,
    stop_after_all_iters_if: {expr: "null.x"}, modules: [x: bun: return 1])
    (continue_on_error: true), n: ret("after")]`, then `l` alone without
    `continue_on_error`.
  - Predicted for both: status failure, result `{"error": {"name":
    "EarlyStopError", "message": <starts "Error
    evaluating stop_after_all_iters_if expression `null.x`: ">}}`, `n` not
    run, and no `InternalError`.
- **A preprocessor that is not a script, by the other routes** (optional;
  [CF-30/b, CF-30/c](../semantics/conformance.md#cf-30)). Extend
  `control-flow/cf-1-30`'s driver.
  - Send the same run request for CF-30/c's flow, whose preprocessor is a
    sequential `forloopflow`. Expected: the same 500
    `Internal: Unsupported preprocessor module<loc>`.
  - Send both flows' requests with `?skip_preprocessor=true`. Expected:
    201, then each flow succeeds with `{}`, which the driver reads with
    `GET /jobs_u/completed/get_result_maybe/<id>` in the two-worker session.

#### Flow execution

- **What the failure module sees after a tolerated error stop at the last
  module** ([FX-V4/a](../semantics/conformance.md#fx-v4), [Q-FX-3](../semantics/execution.md#q-fx-3)). `[a: bun:
  return 3 (continue_on_error: true, stop_after_if: {expr: "true",
  skip_if_stopped: false, error_message: "E"})]`, no retry, with a failure
  module `bun(p, r): return {p, r}`, `p = "previous_result"`,
  `r = "results.a"`. Expected: the handler runs once and the flow fails
  with `{"p": {"error": {"name": "EarlyStopError", "message": "E"}}, "r":
  3}` (the handler is entered through the advanced cursor, so it has no
  preceding id and `results.a` reads the stored `3`). Control: the same
  flow without `continue_on_error`, where `r` is the `EarlyStopError`
  (as FX-V2/a recorded).
- **Preview against deployed**
  ([FX-46/a](../semantics/conformance.md#fx-46),
  [FX-24/d](../semantics/conformance.md#fx-24),
  [FX-40/c](../semantics/conformance.md#fx-40),
  [Q-FX-5](../semantics/execution.md#q-fx-5)), in the two-worker session.
  A driver step waits for the jobs it starts, as the `client-api/driven`
  run `previews` does. It runs each flow by path and its value through
  `POST /api/w/{w}/jobs/run/preview_flow`, waiting for each.
  - FX-46/a's flow: expected deployed failure
    `[{"error": {…, "step_id": "l"}}]`, preview success
    `[{"error": {…, "step_id": "x"}}]`.
  - FX-24/d's flow: expected preview success after about 3 s (the loop's
    `timeout: 1` is not enforced on the uncollapsed body).
  - FX-40/c's rawscript flow previewed twice: expected two different values
    (a preview does not collapse the loop, so the loop's `cache_ttl` does
    not reach the body job).
- **The shared directory** ([FX-41/a–c](../semantics/conformance.md#fx-41),
  [Q-FX-10](../semantics/execution.md#q-fx-10)), flows with `same_worker: true` on the one
  Linux worker: (a) `[a: bun: write ./shared/f "x", b: bun: read
  ./shared/f]`, expected `"x"`; (b) `[a: as (a), br: branchone(default: [c:
  bun: read ./shared/f])]`, expected `"x"`; (c) (a) with `sleep: {type:
  static, value: 0}` on `a`, expected `b` to fail with the runtime's
  file-not-found error (record its text).
- **S3-aware cache hashing** ([FX-40/k](../semantics/conformance.md#fx-40),
  [Q-FX-21](../semantics/execution.md#q-fx-21)), `later`, optional, in the
  object storage session. A step `a: bun(f): return Math.random()` has
  `cache_ttl: 60` and `f = "flow_input.f"`.
  1. Write `t/c.txt` with `"one"`, and run with `{"f": {"s3": "t/c.txt"}}`
     twice. Expected: a hit.
  2. Overwrite `t/c.txt` with `"two"` and run again. Expected: a miss,
     since the ETag changed.
  3. With `cache_ignore_s3_path: true`, copy `t/c.txt` to `t/d.txt`, run
     with `t/c.txt` and then with `t/d.txt`. Expected: a hit, when the
     store gives both copies one ETag.
  4. Run with `{"f": {"s3": "t/none.txt"}}` twice. Expected: a hit keyed by
     the plain JSON, since a missing object gives no ETag.
- **Cases no open question names** (optional;
  [FX-36/i](../semantics/conformance.md#fx-36),
  [FX-42/d, FX-42/k, FX-42/l](../semantics/conformance.md#fx-42),
  [FX-50/b–d](../semantics/conformance.md#fx-50)).
  - FX-42/k and FX-42/l: a variable with the text
    `[-0, 0, -1e-400, 1e-400]` (and one with `1e400`) passed as
    `v = "$jsonvar:<path>"` to a Bun step returning its arguments file's
    text. Expected `{"v":[-0.0,0,-0.0,0.0]}`, and, for `1e400`, the step
    failing with ``execution error:\nInternal: Failed to parse $jsonvar value as JSON: number out of range at line 1 column 5<loc>``.
  - FX-42/d: a transform `Object.keys(flow_input).sort()` with input
    `{"b": 1, "a": 2}`, expected `["a", "b"]`.
  - FX-50/c and FX-50/d: the two throwing `skip_if` flows as the cases give
    them, expected the `InternalError` texts there (the user's
    ` @worker.rs:1:2` and ` (user.rs:123)` kept, one generated `<loc>`).
    FX-50/b's messages are already in recorded texts and need no run.
  - FX-36/i: a deployed flow run through run-and-wait whose result is each
    of the four values the case lists, expected HTTP 200 with the whole
    result as the body.
- **Rule 9's rounding**, optional ([FX-42](../semantics/execution.md#fx-42) rule 9).
  FX-42/j's flow with `0.028735999999999998` appended to the body's array;
  expected the loop's text to print it as `0.028735999999999998`, not
  `0.028736` (the parse is correctly rounded).

#### Languages

- **The module-level `apply_preprocessor`**
  ([WR-11/d](../semantics/conformance.md#wr-11),
  [WR-11](../semantics/languages.md#wr-11)). The oracle's
  `oracle/preprocessor` fixture wrote the flag inside the step's `value`,
  where it is ignored.
  - Deploy two flows over the WR-11/b script through the flows API, not
    the CLI, so the module keeps the field as written. Each has one
    `script` module carrying `apply_preprocessor: true` beside `id` and
    `value`.
  - In one, the value has `hash` set to the script's deployed hash (as
    `pinned` substitutes it); the other has no `hash`.
  - Observe the step's result and stored args. Expected: pinned `"pre"`
    (stored args `{"x":"pre"}`), by path `"orig"`. Also read the deployed
    flow back (`flows/get`) to check the module still has the field.
- **A run after a failed deploy**
  ([WR-40/i](../semantics/conformance.md#wr-40),
  [WR-54/f](../semantics/conformance.md#wr-54)). The harness stops a
  fixture whose dependency job fails, so use a driver step that creates,
  waits and runs through the API. The driver needs a second worker
  ([OR-5/b](../semantics/conformance.md#or-5)).
  - (a) WR-40/i `d` and `e`, in a two-worker variant of the `native`
    session (both workers with `WORKER_TAGS=nativets`). Create each
    one-step flow with `flows/create`: `//native` with
    `import { spawn } from "child_process"`, and
    `import { test } from "node:test"`.
    - Wait for its dependency job and return that job's result. Expected:
      failed, its error containing
      `Browser build cannot import Node.js builtin: "child_process"`, or
      `"node:test"`.
    - Then run the flow by path with run-and-wait and return its result.
      Expected: the step fails with `ExecutionErr`, a message starting
      `exit code for "bun build": 1, last log lines:` and containing the
      same build error and `Failed to build node bundle`.
  - (b) WR-54/f, in a two-worker reference session. Create the script with
    `scripts/create` exactly as the case gives it, at a path with no
    earlier version, and keep the hash it answers with. Wait for its
    dependency job. Expected: failed with
    `Error creating schema validator: ` and the load error.
    1. Run it by path with `{"xs":["a"]}`, and return the HTTP status and
       body, and the jobs at the path. Expected: 404,
       ``Not found: script not found at name <path> (lib.rs:1587)``, and no
       job besides the dependency job.
    2. Run the failed version by hash, `POST /api/w/{w}/jobs/run/h/{hash}`
       with the same body, and wait for that job. Expected: it fails before
       the script runs, its message containing
       ``Couldn't create schema validator for script requiring schema validation: Error: Problem making rule for xs: Error: Array type should have field `items` ``
       (compare with location suffixes masked).
    3. Control: at a second path, create a valid version (the same script
       without `// schema_validation`) and wait for its lock. Create the
       failing version over it, wait for its failed dependency job, and run
       by path. Expected: the valid version's result, `["a"]`.
- **The instance's default job timeout**
  ([WR-44/d](../semantics/conformance.md#wr-44)), in a `native` session of
  its own, since the setting applies to every job. A worker reloads
  `job_default_timeout` only when it polls the setting-change
  notifications, every `LISTEN_NEW_EVENTS_INTERVAL_SEC` (1 s on the oracle).
  Once per repeat:
  1. Set it to `1` with `POST /api/settings/global/job_default_timeout`
     (`{"value": 1}`).
  2. Poll with a `nativets` probe step that has no `timeout` and waits 3 s,
     until it fails with `Query timeout after (>1s)` (at most about 30 s).
  3. Run WR-44/d in both kinds.
  4. Reset the setting (`{"value": null}`), and poll the probe until it
     succeeds before the session ends.

  Expected:
  `{"error":{"message":"execution error:\nQuery timeout after (>1s)","name":"ExecutionErr","step_id":"a"}}`
  in both kinds.
- **A native stream that is not JSON** ([WR-46/c](../semantics/conformance.md#wr-46), [Q-WR-22](../semantics/languages.md#q-wr-22)), `native` session: a `nativets` step `export async function* main() { yield "a"; yield "b"; }`. Observe the step's status and error; expect it to take about 30 s (the completion insert's ten retries). Expected: `{"error":{"name":"InternalErr","message":"Internal: Could not add completed job <job id>: error returned from database: invalid input syntax for type json @<loc>"}}`, no `step_id`.
- **`require` through esbuild's helper**
  ([WR-50/h](../semantics/conformance.md#wr-50)), `native` session. Push a
  codebase entry with the pinned CLI exactly like the one that
  `languages/native-codebase` runs as `esmreq_native` (format `esm`, the
  `createRequire` banner). Its `//native` script returns
  `[e.name, e.message]` from a `try` around `require("node:path").sep`.
  Expected: `["Error","Dynamic require of \"node:path\" is not supported"]`.
- **A re-exported module of a `sideEffects: false` package**
  ([WR-27/b](../semantics/conformance.md#wr-27)), optional, needs the npm
  registry.
  - Deploy a Bun script with a lock that imports a named export, which a
    published package with `"sideEffects": false` re-exports from another
    of its modules (`export { v } from "./v.js"`). The re-exported module
    has a load-time effect that the export shows, and no specifier on the
    way may end in `.ts`.
  - Pick the package by reading its published files first, and pin its
    version.
  - Expected: the export's value. No import on that path matches the
    import plugin's `.ts` filter, so the module is kept.
- **The same behind a transitive bare import ending in `.ts`**
  ([WR-27/c](../semantics/conformance.md#wr-27)), optional, in the
  reference session, and in `native` for the `//native` variant.
  - Build three synthetic packages, exactly as WR-27/c gives them: `outer`
    re-exports `v` from `leaf.ts`, which re-exports it from `./value.js`.
    That module adds 1 to `globalThis.loaded` and exports `const v = 42`;
    both packages declare `"sideEffects": false`.
  - Also build the variant, `outer` re-exporting from `deep/index.ts`, and
    the controls: the package named `leaf`, and `leaf`'s re-export from
    `./value.ts`.
  - The packages must reach the worker's `bun install`. Serve each as a
    tarball from the harness's sink, and name it by URL in the dependencies
    that the lock step installs: the workspace-dependencies route of
    [WR-26](../semantics/languages.md#wr-26). Or use published packages of
    exactly that shape, pinned.
  - Deploy one Bun script per tree,
    `import { v } from "outer"; export function main() { return [v, globalThis.loaded ?? 0]; }`,
    wait for its lock, and run it by path. Repeat the first tree as
    `//nodejs` and as `//native` (native session).
  - Also read the script's lock to check that the tarballs were installed.
  - Expected, from a probe on Bun 1.3.10 outside Windmill (the image's Bun at
    `v1.767.0`): the two `.ts` trees fail with
    `{"name": "ReferenceError", "message": "v is not defined", …}` in every
    mode, and both controls return `[42, 1]`.
  - Bun 1.4.3 keeps the re-exported module (the measurements above), and CE
    `v1.862.0` is not checked yet
    ([D19](0001-equivalence-contract.md#5-named-deviations)). The run on the
    current image must show which of the two holds there.
- **The lock-generation endpoint, by its effect**
  ([WR-V9/a](../semantics/conformance.md#wr-v9),
  [WR-V9](../semantics/languages.md#wr-v9)), optional, needs the npm
  registry. The oracle's harness cannot see the request itself.
  - Deploy `f/x/helper`, whose source imports a small published package
    pinned to an old version (`import x from "<package>@<old version>"`,
    not the latest). Then deploy `f/x/main`, which imports `./helper` and
    nothing else.
  - Read `f/x/main`'s lock with `GET /api/w/{w}/scripts/get/p/f/x/main`.
  - Expected on CE `v1.862.0`: its `package.json` part lists the old
    version, because lock generation fetches the helper through `raw`, which
    keeps the pin ([WR-V9/a](../semantics/conformance.md#wr-v9)). Through
    `raw_unpinned`, as on CE `v1.767.0`, it would list
    `"<package>": "latest"`.
- **The preview half of the import-base case** ([WR-29/b](../semantics/conformance.md#wr-29)): preview the case's flow value with `POST /jobs/run/preview_flow` from a driver step (two workers). Expected: `"elsewhere"`.

#### OpenFlow, the sync folder, expressions and the oracle

- **An unknown pin, deployed through the API**
  ([OF-14/f](../semantics/conformance.md#of-14)). It needs no push of a
  tree: deploy a script `f/wl_pin/s` (`export function main() { return 1 }`)
  and a flow `[a: {type: script, path: f/wl_pin/s, hash:
  "0123456789abcdef"}]` with `oracle.deploy api`, wait for the flow's
  dependency job, and run by path.
  - Observe the dependency job's success, the flow's status and error, and
    `a`'s module entry.
  - Predicted: the dependency job succeeds; the run fails before `a` has a
    job, with an error whose message contains
    `script not found at name 81985529216486895`.
- **`$jsonvar:` texts cut short in an object**
  ([JE-42/k](../semantics/conformance.md#je-42)), in the style of
  `je/je-20-providers`' `i` runs: variables holding `{"a":1,`, `{"a":1` and
  `{"a":1,}`, each the static `x = "$jsonvar:<path>"` of a Bun step `b`
  after `a: bun: return 1`, one run each. Predicted: `b` fails before it
  runs with `execution error:\nInternal: Failed to parse $jsonvar value as
  JSON: <m> @common.rs:299:17` and `step_id` `b`, where `<m>` is `EOF while
  parsing a value at line 1 column 7`, `EOF while parsing an object at
  line 1 column 6` and `trailing comma at line 1 column 8`.
- **A deep `flow_env` member, in a session of its own**
  ([JE-49/e](../semantics/conformance.md#je-49),
  [Q-JE-15](../semantics/expressions.md#q-je-15)). Deploying it crashed the
  CE server in oracle run 40990751153 and ended that session. The source
  read does not show why: the flows API and the flow decoder keep the
  member as raw text.
  - Run it last in a session whose job also saves the Windmill container's
    log (`docker compose logs`) as an artifact, whatever the outcome.
  - Create, through the flows API, three flows `[a: bun(x): return x]`
    with `x = flow_env.K.length` and `flow_env: {K: nest(n)}`, for `n` =
    100, 127 and 128 in that order. `nest(n)` is `n` nested arrays around
    `[]`.
  - After each create, poll `GET /api/version` until it answers or 60 s
    pass, wait for the dependency job, and run the flow by path.
  - Observe, for each `n`: the create's status and body, the dependency
    job's outcome, whether the server still answers, and the run's result.
    Also observe the log's last lines before any restart: a Rust panic or a
    stack-overflow abort names the thread.
  - Predicted from the source alone: every create and dependency job
    succeeds. `n` = 100 and 127 succeed with `1`, since the member's own
    text is within the 127-level limit. `n` = 128 fails `a` before it runs
    with `recursion limit exceeded at line 1 column 128` and no
    `QuickJS evaluation error` ([JE-49](../semantics/expressions.md#je-49)).
  - The point of the run is where, and from which `n`, the crash happens.
- **Pushing sync trees as runs**
  ([Q-OF-13](../semantics/openflow.md#q-of-13); the 17 sync-folder cases
  listed below). These cases wait
  for one reason: the harness pushes a fixture's tree once, before its
  runs, and fails the fixture when that push fails.
  - They need a harness session in which **a run is a push**. The pinned
    `windmill-cli@1.862.0` (Node) runs `wmill sync push --yes` from the
    tree's root into a fresh, empty workspace, one per run.
  - The run records the exit status, standard output and standard error,
    with the workspace, URL and token masked. It also records the files
    that the push wrote into the tree.
  - It reads back every item the case names (`scripts/get`, `flows/get`,
    `resources/get`, `variables/get`), and lists the workspace's scripts and
    flows, so that an item that was not deployed shows as absent.
  - Per case, the tree is the case's Given and the prediction its Expect:
    - [SY-4/b](../semantics/conformance.md#sy-4): the files written back (`x.script.yaml`, an empty
      `x.script.lock`) and the deployed schema (`n`, `number`, required);
    - [SY-5/c](../semantics/conformance.md#sy-5): the deployed lock, the literal ` !inline f/a/x.script.lock`;
    - [SY-5/d](../semantics/conformance.md#sy-5): standard output's `Error reading inline path: …, Error: ENOENT:
      no such file or directory, open 'f/a/x.script.lock'` and the lock `""`;
    - [SY-7/f](../semantics/conformance.md#sy-7): the module content, the text of `a.`;
    - [SY-8/c](../semantics/conformance.md#sy-8): the codebase the script was bundled from (`../shared`): give
      the two codebases scripts that differ, and read the deployed bundle
      back;
    - [SY-10/b](../semantics/conformance.md#sy-10) and [SY-11/b](../semantics/conformance.md#sy-11): the push's failure (`reading 'content'`; the
      server's ``missing field `value` ``) and the item absent;
    - [SY-13/b](../semantics/conformance.md#sy-13): no `wmill.yaml`; the warning `No wmill.yaml found. Use 'wmill
      init' to bootstrap it.` and the three items deployed;
    - [SY-16/a](../semantics/conformance.md#sy-16): one push per row of the table, recording which item deploys;
    - [SY-17/a–c](../semantics/conformance.md#sy-17) and [SY-18/a](../semantics/conformance.md#sy-18): exactly which items deploy for each `includes`;
    - [SY-18/b](../semantics/conformance.md#sy-18): the failure `The item f/a/helper.js has an unrecognized type
      ending script`;
    - [SY-19/a](../semantics/conformance.md#sy-19): the deployed content's first characters
      (`export`, no U+FEFF);
    - [SY-19/b](../semantics/conformance.md#sy-19): the failure `… is encoded as UTF-16 LE, which is
      not supported. Please convert it to UTF-8.`;
    - [SY-19/e](../semantics/conformance.md#sy-19): standard error's
      `Lock file a.inline_script.lock not found, treating as empty` and the
      lock `""`.

Not requested: JE-19/e and JE-19/f, since the CE image has no restart at
all (the documented route answers 500 before the CE build's stub, which
itself only answers that restarting is enterprise-only;
[JE-19](../semantics/expressions.md#je-19)).

#### The client API

Each request here is made by a Bun driver step with `fetch` and its own
`WM_TOKEN`, which records each answer's status, `Content-Type` and body.

- **A token outside its workspace**
  ([WA-4/b](../semantics/conformance.md#wa-4),
  [WA-V2/d](../semantics/conformance.md#wa-v2),
  [Q-WA-13](../semantics/client-api.md#q-wa-13)). Recordable now, with
  `<none>` a workspace id that names no workspace (for example `wl-none`)
  and `<ws>` the fixture's workspace.
  - The calls:
    1. `GET /api/w/<none>/variables/get_value/<a variable of the fixture>`;
    2. `POST /api/w/<none>/jobs/flow/user_states/<WM_ROOT_FLOW_JOB_ID>/k`
       with the JSON body `1`;
    3. `GET /api/w/<none>/jobs_u/get_root_job_id/<WM_JOB_ID>`;
    4. `GET /api/w/<none>/jobs_u/completed/get_result_maybe/<id of a completed job of <ws>>`;
    5. `GET /api/w/<ws>/jobs_u/get_root_job_id/<WM_JOB_ID>`.
  - Expected for the first two: 401 `text/plain`
    `Not authorized: Unauthorized`. A job token is refused on another
    workspace's authenticated routes, existing or not.
  - Expected for the third: 400 `text/plain`
    `SqlErr: no rows returned by a query that expected to return at least one row @jobs.rs:<line>:<col>`.
    For the fourth: 200
    `{"started":null,"success":null,"completed":false,"result":null}`, since
    the caller is anonymous and the job is not in `<none>`. For the fifth:
    200 with the root id as a JSON string.
  - Once the harness can create a second workspace `other` with the admin's
    session, repeat the four calls against `other`, expecting the same.
  - Then deploy into `other` a one-step Bun script, and run it there with
    the id `J` of a completed job that the admin ran in `<ws>`. With its own
    token it calls `GET /api/w/<ws>/jobs_u/completed/get_result_maybe/J`.
    Expected: 400 `text/plain`
    `Bad request: As a non logged in user, you can only see jobs ran by anonymous users`.
- **A second user** (the other half of
  [WA-12/h](../semantics/conformance.md#wa-12)). The harness adds to the
  workspace a user who is not an instance super admin. As that user, it
  runs a Bun step returning
  `await getResource("CUSTOM_INSTANCE_DB/demo").catch(e => e.message)` and
  the answer of
  `GET /api/w/<ws>/resources/get_value_interpolated/CUSTOM_INSTANCE_DB/demo`.
  Expected, as WA-12/h gives it on CE `v1.862.0` for both users: the read is
  made with the job's token, so it gets HTTP 401 and the client throws
  `Resource not found at CUSTOM_INSTANCE_DB/demo or not visible to you: Not authorized: CUSTOM_INSTANCE_DB cannot be resolved from a job token ($WM_TOKEN)`.
- **Object storage**
  ([WA-16/c–e](../semantics/conformance.md#wa-16),
  [Q-WA-3](../semantics/client-api.md#q-wa-3)). The CE image's
  object-store handlers are closed source, so only a recording can say what
  they do.
  - Session A is the object storage session. A Bun step returns, each call
    in its own `try`: `writeS3File({s3:"t/a.txt"},"hi")` then `loadS3File`
    of it as text (expected `[{"s3":"t/a.txt"},"hi"]`), and
    `writeS3File(undefined,"x")` (the generated key's form). It also
    returns `loadS3File({s3:"t/none.txt"})`'s message: the status and body
    inside `Failed to load S3 file: …`.
  - Also in session A, an upload at the storage quota's boundary: record its
    status, its body and the returned `file_key`
    ([Q-WA-3](../semantics/client-api.md#q-wa-3)).
  - Session B has no storage and runs the first call only
    ([WA-16/d](../semantics/conformance.md#wa-16)). The step returns the
    caught `ApiError`'s `status`, `statusText` and `body`. The recording of
    WA-16/d kept only the message, `Internal Server Error`, which fixes the
    status at 500 but not CE's text.
- **The result polls for a job that a flow created**
  ([WA-V2](../semantics/client-api.md#wa-v2),
  [WA-19](../semantics/client-api.md#wa-19)), in the two-worker session, so
  that the driver runs while a sibling still runs. The build team asks for
  it: the result polls are to answer for every job of a live run
  ([platform interface](0007-platform-interface.md#services)).
  - Deploy `f/x/fails`, `[x: bun: throw new Error("e")]`, and run by path
    `[a: bun: return {"k": [1, 2.50]}, c: flow f/x/fails
    (continue_on_error: true), p: branchall(parallel: true, branches:
    [[w: the sibling], [d: the driver]])]`.
  - The sibling `w` sets the flow user state `started` to `true`
    ([WA-14](../semantics/client-api.md#wa-14)), then polls `release` every
    100 ms until it is `true` or 60 s have passed, and returns 1.
  - The driver polls `started` until it is `true`, at most 60 s, then reads
    the root flow's job (its `WM_ROOT_JOB_ID`) and `p`'s first child flow
    through `GET /api/w/<ws>/jobs_u/get/<id>`, to find the jobs of `a`, `c`
    and `w` in `flow_status.modules`. Once it has the answers below, it
    sets `release` to `true`.
  - For each of the three jobs it calls
    `GET /api/w/<ws>/jobs_u/completed/get_result_maybe/<id>`, the same with
    `?get_started=true`, and `GET /api/w/<ws>/jobs_u/completed/get_result/<id>`.
  - Predicted from the rules of the polls and the job read: `a` completed,
    with `success` `true` and its result, and `c`'s flow job completed,
    with `success` `false` and the subflow's error as its result. The
    running `w` answers the unfinished nulls, `started` `true` when asked,
    and `get_result`'s 404
    `Not found: Completed Job not found at name <id><loc>`.
  - Not predicted: the spelling of a result in the body, compact or with
    the spaces of `jsonb`'s text output, which the job read uses. Every
    answer above stays a prediction until the recording, which decides
    what the planned option answers.

#### Optional checks outside the equivalence contract

- **PostgreSQL alone** ([Q-JE-9](../semantics/expressions.md#q-je-9)),
  which needs no Windmill: `SELECT '<json>'::jsonb::text` on the oracle's PostgreSQL with keys of
  mixed length, multi-byte UTF-8 keys and integer-like keys.
- **The image's toolchain** ([Q-JE-16](../semantics/expressions.md#q-je-16)),
  on the oracle: a Python step printing `ldd --version` and the `.comment` section of the
  server binary.
- **The evaluator alone** ([Q-JE-12](../semantics/expressions.md#q-je-12)):
  rquickjs 0.11.0's output for `"\ud83d"`, which needs no Windmill.

### Recorded requests

The oracle has recorded these requests. Each case names its fixture and
oracle run in its **Recorded** line, and a case that the oracle contradicted
says so in its `Settled:` line. Where part of a request is still open, the
last column names the open request that asks for it.

| Request | Cases recorded | Still open |
|---|---|---|
| Retry delays and jitter | [FX-26/c–f](../semantics/conformance.md#fx-26) | |
| `wm_failure` on identity jobs | [FX-13/b, FX-13/c](../semantics/conformance.md#fx-13) | |
| A `branchone` aborted at a later transition | [CF-23/d–f](../semantics/conformance.md#cf-23) | |
| `ai` parallelism and squash on the CE image | [CF-7/e](../semantics/conformance.md#cf-7), [CF-25/c–e](../semantics/conformance.md#cf-25) | What a squashed loop's runner does |
| Number notation | [FX-42/a, /b, /e, /f, /i, /j](../semantics/conformance.md#fx-42), [JE-11/c, JE-11/e](../semantics/conformance.md#je-11), the stored `1.50e3`, and the loop aggregate of `{"b":1.50,"a":1}` (`execution/fx-42-notation` run `aggregate`, [Q-JE-13](../semantics/expressions.md#q-je-13)) | Rule 9's rounding |
| Push failures and the `jsonb` boundary | [FX-48/b, FX-48/c](../semantics/conformance.md#fx-48), [FX-50/a](../semantics/conformance.md#fx-50), [FX-51/a–c](../semantics/conformance.md#fx-51) | |
| A transform whose text is not JSON | [JE-15/j, JE-15/k](../semantics/conformance.md#je-15) | |
| Deep context values | [JE-49/d](../semantics/conformance.md#je-49) | A deep `flow_env` member |
| `resource()` and the provider texts | [JE-20/c, JE-20/d](../semantics/conformance.md#je-20), [JE-42/i, JE-42/j](../semantics/conformance.md#je-42) | `$jsonvar:` texts cut short in an object |
| Secrets, `job_id` and `CUSTOM_INSTANCE_DB/` on the variable and resource routes | [WA-11/c, WA-11/d](../semantics/conformance.md#wa-11), [WA-12/g](../semantics/conformance.md#wa-12), and WA-12/h's super admin | A second user |
| The facade's error answers | [WA-14/c, WA-14/d](../semantics/conformance.md#wa-14), [WA-19/b–d](../semantics/conformance.md#wa-19), [WA-V2/c](../semantics/conformance.md#wa-v2) | A token outside its workspace |
| Envs, handler errors and validation | [WR-22/b, WR-22/c](../semantics/conformance.md#wr-22), [WR-37/b](../semantics/conformance.md#wr-37), [WR-54/a–e, WR-54/g](../semantics/conformance.md#wr-54) | A run after a failed deploy |
| The inline-flow import base, the empty-result error and CRLF in a codebase bundle | [WR-29/a](../semantics/conformance.md#wr-29), WR-29/b's deployed half, [WR-V2/a](../semantics/conformance.md#wr-v2), [WR-49/h](../semantics/conformance.md#wr-49) | WR-29/b's preview half; the lock-generation endpoint |
| TSX parse | [WR-51/d](../semantics/conformance.md#wr-51), [WR-52/a, WR-52/e](../semantics/conformance.md#wr-52) | |
| Leftover processes and process reuse | [WR-55/a–c](../semantics/conformance.md#wr-55) | |
| The trailing virtual step | [OR-V7/c](../semantics/conformance.md#or-v7), [OF-17/a](../semantics/conformance.md#of-17) | |
| `delete_after_use` that survives deploy | [OR-21/b–d](../semantics/conformance.md#or-21) | |
| A script's own preprocessor | [OR-24/e](../semantics/conformance.md#or-24), [WR-11/b, WR-11/c](../semantics/conformance.md#wr-11) | The module-level `apply_preprocessor` |
| Incoming input refused at storage | [FX-48/d](../semantics/conformance.md#fx-48), [FX-51/d, FX-51/e](../semantics/conformance.md#fx-51) | |
| The result cache | [FX-40/e–m](../semantics/conformance.md#fx-40) | S3-aware cache hashing. Not recorded: the log line `Job skipped because args & path found in cache and not expired` on the hit jobs, since the oracle's walker records results and statuses, not logs |
| A container as the failure module | [CF-31/a–c, CF-31/e](../semantics/conformance.md#cf-31) | An empty loop in the failure slot |
| A container's own settings on handled failures and forwarded stops | [CF-16/e, CF-16/f](../semantics/conformance.md#cf-16), [CF-12/g](../semantics/conformance.md#cf-12), [CF-7/f](../semantics/conformance.md#cf-7), [FX-30/b](../semantics/conformance.md#fx-30) | |
| A step canceled while it waits to start, also as a subflow step | [FX-38/b](../semantics/conformance.md#fx-38) | |
| A retried parallel container | [CF-22/h](../semantics/conformance.md#cf-22) | |
| A flow that calls itself by path | [CF-17/j](../semantics/conformance.md#cf-17) | |
| A container's throwing `timeout` and `parallelism` | [CF-21/e, CF-21/f](../semantics/conformance.md#cf-21) | |
| A container child result the backend cannot decode | [FX-42/m–o](../semantics/conformance.md#fx-42) | |
| The answers to variable writes | [WA-11/e](../semantics/conformance.md#wa-11) | |
| A job chain that never ends | [JE-2/b](../semantics/conformance.md#je-2) | |
| Colliding step identities | [OR-V8/a–c](../semantics/conformance.md#or-v8) | |
| Timeouts on branches and subflow steps | `control-flow/cf-27-flow-timeouts` ([Q-FX-1](../semantics/execution.md#q-fx-1)); on CE `v1.862.0` a subflow step's timeout is enforced ([FX-52/f](../semantics/conformance.md#fx-52)) | |
| Released iterations, on two workers | [CF-7/a, CF-7/b](../semantics/conformance.md#cf-7) | |
| Native steps | [WR-40/a–i](../semantics/conformance.md#wr-40) (WR-40/i's step `c`), [WR-41](../semantics/conformance.md#wr-41) to [WR-46/b](../semantics/conformance.md#wr-46) except [WR-44/d](../semantics/conformance.md#wr-44), [WR-50/c, WR-50/f](../semantics/conformance.md#wr-50) | A run after a failed deploy; the instance's default job timeout; a native stream that is not JSON |
| The result-size limit | [OR-7/b, OR-7/c](../semantics/conformance.md#or-7) | |
| `DISABLE_FLOW_SCRIPT=true` | [OF-V5/b](../semantics/conformance.md#of-v5) | |
| A preview of a collapsed while loop | [JE-36/d](../semantics/conformance.md#je-36) | |
| `timeout: 0` | `execution/fx-24` run `zero` ([Q-FX-2](../semantics/execution.md#q-fx-2)): on CE `v1.862.0` a timeout of 0 is unset, so there is no race | |
| A negative timeout at root pickup | [FX-24/b](../semantics/conformance.md#fx-24): on CE `v1.862.0` the timeout is unset, and the flow succeeds | |
| The questions' own recordings | [JE-21/b](../semantics/conformance.md#je-21) ([Q-JE-1](../semantics/expressions.md#q-je-1)); the `typeof` list (`je/je-open` run `typeof`, [Q-JE-2](../semantics/expressions.md#q-je-2)); [JE-50/a](../semantics/conformance.md#je-50), [JE-51/a](../semantics/conformance.md#je-51), [JE-52/a](../semantics/conformance.md#je-52) ([Q-JE-12](../semantics/expressions.md#q-je-12)); [Q-FX-18](../semantics/execution.md#q-fx-18)'s cases; [Q-OF-1](../semantics/openflow.md#q-of-1)'s fixtures | The evaluator alone |

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
   steps to a user's own Windmill count as "wrapping" it? The remote adapter
   of hybrid dispatch waits for this answer; nothing else does.

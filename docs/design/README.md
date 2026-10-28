# windmill-lite design

**Current state:** first edition, proposed. Milestone A3 implements the
OpenFlow decoder of [0002](0002-ir-and-loader.md) in
[internal/openflow](../../internal/openflow/README.md), milestone A2 its YAML
dialect in [internal/yamlx](../../internal/yamlx/README.md), milestone A1 the
jsonb canonicaliser of [0005](0005-json-step-boundaries.md) in
[internal/jsonval](../../internal/jsonval/README.md), and milestone K1
the contracts of [0012](0012-layout-and-milestones.md) (the shared types and
interfaces under `internal/`); nothing runs flows yet. This README covers
`docs/design/`: this index and the decision records it links. The records
decide the architecture, the equivalence contract with Windmill `v1.767.0`,
the Go package layout and the implementation milestones. They build on the
[semantics reference](../semantics/README.md) and cite its finding IDs.

The records are written by the build team under the clean-room rule of
[AGENTS.md](../../AGENTS.md): from the semantics reference, Windmill's public
documentation, the Apache-2.0 parts of Windmill, and observed behaviour.
Where a decision depends on a number, the number was measured with a
throwaway prototype outside this repository, and the record states the
hardware, versions and method. Milestone G1 turns those prototypes into
in-repository benchmarks.

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
| [0001 Equivalence contract](0001-equivalence-contract.md) | Match every `core` finding for a successfully deployed flow run by path as a root on CE; reject out-of-scope features at load; fifteen named deviations (D1–D15), five of which `fresh` mode removes, and seven restrictions; contested findings recorded on the oracle early, with a stated default until then and no permanent switches; Rust location suffixes stripped only from generated messages. |
| [0002 IR, loader and validation](0002-ir-and-loader.md) | The CLI's YAML dialect over a YAML 1.1 parser, with named refusals; hand-written OpenFlow types with Windmill's effective decoder and a schema-coverage test; normalise once to the deployed form; a typed IR with precomputed collapse, compiled expressions and a harness class per leaf; signatures of every export; a host contract whose mandatory provided sites make their targets opaque; codebase scripts bundled with esbuild and run on Bun (D13). |
| [0003 Where semantics live](0003-semantics-placement.md) | One interpreter and one expression pipeline, in Go; QuickJS-ng `be664ca1` embedded natively (a wazero backend deferred), built static with `zig cc` and a pinned stack size; a fresh runtime per evaluation from a bounded pool, bytecode cached, values bound as host strings; a budget gate before the engine merges; harnesses are step runners; no segments; a conformance suite of flow, step and expression cases plus OpenFlow decode cases and jsonb stored-form cases, compared once in Go, in `fresh`, `warm` and host mode, with contextual expression cases written as flow fixtures. |
| [0004 Harness protocol](0004-harness-protocol.md) | Length-prefixed JSON frames on a socketpair, large messages in parts; validated entrypoint names; a shared-process failure model (handlers that never exit, attribution without guessing, D9–D11); `fetch` as the only platform transport in warm processes (other transports refused, unrecorded); no in-flight cap unless a host sets one (D15); host mode never tainted, drained by the host, and killed by the engine's watchdog when its event loop stops answering. |
| [0005 JSON at step boundaries](0005-json-step-boundaries.md) | Go holds every value as producer text plus a lazily computed jsonb text and never decodes what it passes through; a routing table says which consumer sees which; one streaming canonicaliser checked against PostgreSQL; host-plugin results serialised by the steps' own function; the normalising walk dropped, because it was not equivalent. |
| [0006 Module state](0006-module-state.md) | Two modes: `fresh` (a process per invocation, Windmill-equivalent) and `warm` (default: one module instance per script for the process lifetime, `globalThis` shared), plus host-provided modules as the one shared exception; `shared` left out of the first edition. |
| [0007 Platform interface](0007-platform-interface.md) | Go service interfaces with local defaults and host replacements; one interpolation walker with three modes and a host secret layer; a facade reached by intercepted `fetch` (warm) or per-invocation tokens (`fresh`), never by a shared token; unsupported routes fail with `UnsupportedPlatformCall`; hybrid dispatch built behind interfaces, its remote adapter held for a licence decision. |
| [0008 Host hooks](0008-host-hooks.md) | Thirteen hooks with typed selectors, as Go callbacks or TypeScript host plugins; mandatory host-provided call sites that fail closed; scopes reach steps through host-provided modules; seams defined in the first engine milestone. |
| [0009 Host adapters](0009-host-adapters.md) | The `wl` Go library is the only engine API; adapters in order: go-plugin, a Bun host SDK, plain gRPC, HTTP, CLI; typed client methods generated from Windmill schemas over a generic wire, bound to the bundle digest. |
| [0010 Packaging](0010-packaging.md) | A bundle with a manifest of digests, tied to one exact engine version; a Bun build helper on the pinned Bun `1.3.10`; host-provided modules through a registry (D12), with one shim generator and one define table for both bundlers; a bundle digest in every handshake, trace and build-info call; a single executable; an OCI image written without Docker; a `verify` command. |
| [0011 Differential testing](0011-differential-testing.md) | The oracle harness as the reference designs it, in public CI only; a typed, injective site-path encoding that also separates repeated module ids; `record`, `check` and `diff` in three configurations; only context-free cases lifted into one-step flows; a stub mode on the oracle and then on windmill-lite; benchmarks, a bounded-pool benchmark and run spans. |
| [0012 Layout and milestones](0012-layout-and-milestones.md) | `wl/` as the only public package and components under `internal/`; 51 milestones, each one pull request, whose "needs merged" covers everything their exit gates need; a contracts milestone first, a walking skeleton on the pinned Bun in wave 2, oracle recordings in batches, each before the milestone it gates. |

## Measurements behind the decisions

Apple M4, macOS, Go 1.27.3, Bun 1.4.2 unless a row names 1.3.10 (details in
each record):

| Question | Result | Used in |
|---|---|---|
| Go ↔ warm Bun round trip, one step | 12.5 µs p50 for 26 B, 18.4 µs for 3 KB, about 250 µs for 100 KB | 0003, 0004 |
| First QuickJS-ng evaluation on a never-used runtime (setup excluded), by bound input | wazero: 53 µs at 50 B, 79 µs at 4 KB, 427 µs at 64 KB; native: 87 µs, 91 µs, 162 µs | 0003 |
| Fresh runtime setup | 221 µs in wazero, 84 µs native | 0003 |
| Bounded pool (wazero prototype), 8 slots, 8 callers with pauses, 4 KB | 120 µs p50, 260 µs p95 | 0003 |
| Normalising walk versus native JSON round trip in Bun | faster, but not equivalent (top-level functions and symbols, own `__proto__` keys) | 0005 |
| Fresh module state per invocation in one process | 0.6–2.1 ms for 0.3–1.3 MB bundles; re-importing leaks module records | 0006 |
| Step code reading a host scope through a separately bundled copy of the module, on Bun 1.3.10 | `undefined`; through a host-provided module, 16 of 16 isolated, compiled host included | 0010 |
| One detached promise rejection in a shared Bun process, 1.3.10 and 1.4.2 | ends the process unless handlers are installed | 0004 |
| A host spinning forever, watched by a Go child that kills it after 1 s of missed heartbeats | killed 1.04 s after its last heartbeat; a restart-on-exit supervisor serves the next call | 0004 |
| esbuild CJS and ESM bundles resolving a host-provided module to the generated shim, Bun 1.3.10 and 1.4.2 | 16 of 16 isolated; `import.meta.env` rewritten in ESM only | 0010 |

## Unsettled findings

Twenty-one findings of the reference are not settled. The design does not pick
sides silently: [0001](0001-equivalence-contract.md) §6 lists each contested
rule, its readings, the default and why, and the recording that settles it.
Recording batches E3a to E3e record them on the oracle (E3e holds the
contested `later` cases), each before the milestone that implements its
rules merges; until then the implementation
follows the stated default in one function per rule, and no build switch
keeps both readings.

## Requests to the semantics team

Questions reach the semantics team as requests for the reference to cover a
behaviour ([AGENTS.md](../../AGENTS.md)). The design depends on these:

1. **Native boundary rules** for `bunnative` and `nativets` steps: how a
   result is serialised (in particular object properties holding
   `undefined`), the error object's fields, the timeout text, and which
   globals and APIs the native runtime offers. The reference covers the
   native runtime only in outline ([WR-31](../semantics/languages.md#wr-31))
   and puts it in `later` scope, while the language values are `core` for
   loading ([OF-13](../semantics/openflow.md#of-13)). Until it is covered, the
   harness's native mode ([0004](0004-harness-protocol.md)) has no public
   conformance cases, and milestone C3n cannot merge (gate S1).
2. **Codebase bundling details**: the default `format` of a codebase entry,
   the esbuild version the pinned CLI uses, how `inject` and `customBundler`
   are handled, where `assets` land at run time, and the Node version that
   runs CJS bundles. [WR-30](../semantics/languages.md#wr-30) gives the
   outline. Until it is answered, milestone C2b uses the CLI's documented
   defaults and records the esbuild version in the manifest; an answer
   refines those settings but does not hold C2b.
3. **The expression engine's stack limit**: which maximum stack size
   Windmill's QuickJS-ng embedding uses, since stack-overflow depth is
   observable. Until it is answered, windmill-lite pins QuickJS-ng's built-in
   default of 1 MiB and records it in the manifest
   ([0003](0003-semantics-placement.md)).
4. **What `jsonb` rejects besides NUL**: PostgreSQL's `jsonb` input also
   rejects a UTF-16 surrogate escape without its pair (`"\ud83d"`) and a
   number outside `numeric`'s range (more than 131072 digits before the
   point or 16383 after it, or an exponent beyond 2³⁰−1 in magnitude). Does Windmill fail a step whose result or
   arguments hold one, as [FX-48](../semantics/execution.md#fx-48) and
   [OR-27](../semantics/oracle.md#or-27) say it does for NUL, and with which
   message? The reference covers only NUL. Until it is answered, `jsonval`
   rejects both as PostgreSQL does, and the eager NUL flag of
   [0005](0005-json-step-boundaries.md) covers NUL alone.

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

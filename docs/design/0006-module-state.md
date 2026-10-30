# 0006 · Module state

**Status:** proposed · **Topic:** whether a script's module state survives
between invocations in a warm harness, the default, and the opt-out.

## Context

Windmill runs every Bun step in a new process with a new working directory.
Top-level code runs before the arguments are read, and nothing in memory
survives to the next job: module-level variables, `globalThis`, timers and
caches all start fresh ([WR-1](../semantics/languages.md#wr-1),
[WR-2](../semantics/languages.md#wr-2)). Cases WR-1/a and WR-1/b expect a
module-level counter and a `globalThis` counter to read `1` on each of two
runs. The reference leaves the warm harness's behaviour to the design
([Q-WR-19](../semantics/languages.md#q-wr-19)).

Scripts written for Windmill assume a fresh process. Keeping state is
usually harmless and often useful (a client created at module level is
reused), but it can leak: a cache keyed without the tenant, a module-level
array that grows, a value read from the environment once at load.

## Measurements

Bun 1.4.2, Apple M4, one warm process, synthetic step bundles (function
declarations, classes and tables at top level; prototype outside the
repository). Per invocation:

| Bundle | Keep module | Re-import with a cache-busting URL | Re-run a compiled `node:vm` script |
|---|---|---|---|
| 13 KB | 0.6 µs | 162 µs, +24 MiB per 200 runs | 18 µs |
| 326 KB | 0.1 µs | 2.8 ms, +388 MiB per 200 runs | 0.57 ms |
| 1.3 MB | 0.2 µs | 12.8 ms, +1022 MiB per 200 runs | 2.1 ms |

Re-importing keeps every module record alive, so it is unusable. Re-running
a compiled script reuses bytecode but still re-executes all top-level code,
and real dependencies do more at load (connection pools, schema compilation)
than the synthetic tables. Neither resets `globalThis`. Starting a new Bun
process took 46–54 ms to the first message.

## Options considered

1. **Fresh per invocation, in-process** (re-run the module each time). Not
   faithful (`globalThis` survives) and too slow for bundles with real
   dependencies.
2. **Fresh per invocation, one process per invocation.** Exactly Windmill's
   model, at about 50 ms per step.
3. **Warm per script**: each script's bundle is evaluated once per harness
   process and kept; scripts stay isolated from each other as in Windmill,
   because each bundle carries its own copy of its dependencies.
4. **Shared**: all scripts of a harness are built together with shared
   chunks, so dependencies are evaluated once for the whole process. Least
   memory; scripts share module state with each other, which Windmill never
   does.
5. **Warm per script plus a short list of host-provided modules** shared
   with the host. Chosen, with option 2 as the opt-out.
6. **Fresh per run.** Saves little, since each step of a run is usually a
   different script.

## Decision

Two modes, chosen by the host, recorded in the bundle manifest and in each
run's trace:

| Mode | Module identity | Lifetime | Use |
|---|---|---|---|
| `fresh` | per invocation | one harness process per invocation, ended afterwards | Windmill-equivalent; the conformance suite; kill semantics on timeout and cancellation; scripts a host lists by path |
| `warm` (default) | per script, as in Windmill | the harness process | production (deviation D1) |

- **Default `warm`.** Module-scoped state stays per script, as in Windmill,
  because each script's bundle carries its own copy of its imports
  ([WR-27](../semantics/languages.md#wr-27)); only its lifetime changes.
  `globalThis` is shared: one process serves every script, so all scripts in
  it see one global object, which case WR-1/b exposes. That is the smallest
  deviation that keeps per-step cost in microseconds.
- **Host-provided modules** are the one deliberate exception to per-script
  identity. A host lists module paths whose single instance the host and
  every script must share (for example a module that holds an
  `AsyncLocalStorage` the host enters); the build replaces their imports
  with references to that one instance ([0010](0010-packaging.md), D12).
  Everything else stays per script.
- **Opt-out.** A host can run the whole bundle `fresh`, or list script paths
  (glob patterns) that always run `fresh`. `fresh` also removes D2, D9, D10
  and D11 ([0001](0001-equivalence-contract.md) §5). It is not available in
  host mode, where steps must run in the host's process: **host mode always
  runs `warm`**.
- **`shared` is not in the first edition.** A mode with one build and shared
  chunks for all scripts was weighed: it saves memory but changes bundling
  away from WR-27, shares state between scripts that Windmill keeps apart,
  and doubles the configurations to test. Its main use case, a host whose
  scripts must share a few singletons with it, is covered by host-provided
  modules. It can come back through its own record, with a memory
  measurement that shows `warm` does not fit. A host's measurement did,
  and [0013](0013-shared-module-code.md) is that record: it rejects shared
  chunks, which would share module state between scripts and turn
  CommonJS codebase scripts into ES modules, and adds **shared code** to
  `warm` instead, a build setting under which a process compiles the
  dependency modules of a group of scripts once and each step module makes
  its own instance of them (D18).
- **Conformance.** Flow cases run in `fresh`, in `warm` and in host mode;
  step cases run in `fresh` and `warm` ([0003](0003-semantics-placement.md)).
  Cases WR-1/a and WR-1/b expect `1` and `1` in `fresh`, and `1` then `2` in
  `warm`: the deviation is tested, not assumed.
- **Native steps are fresh in every mode.** Each invocation of a `nativets`
  or `//native` step runs in a realm of its own, as Windmill gives each
  native job a new isolate ([WR-41](../semantics/languages.md#wr-41),
  D5), so neither D1 nor the shared `globalThis` applies to them. On Bun
  1.3.10 a realm with the module's compiled code reused costs about
  0.06 ms per invocation (milestone C3n).
- **`globalThis`** survives in `warm`, across invocations and across scripts.
  Code that keeps resources there on purpose, such as a connection cache
  registered on a global object, keeps working, and that is the point of a
  warm harness.

## Consequences

- A host turning on `warm` takes responsibility for module-level state in its
  scripts. The run trace records the mode, so an incident can be traced to
  it.
- Memory grows with the number of distinct scripts in `warm`, because each
  carries its dependencies. A host measures it on its own bundle; a result
  that does not fit is the trigger for a `shared` record, not a silent
  change of mode. Milestone F8a wrote that record,
  [0013](0013-shared-module-code.md), after a host measured about 59 MiB
  per loaded module against a 650 MiB budget. With shared code, what a
  further module costs is the state its dependencies make when they load,
  not their code; module identity stays per script, as this record
  decided.
- First-invocation latency includes module evaluation; eager loading
  ([0004](0004-harness-protocol.md)) moves it to process start.
- Milestone C3b implemented `fresh` mode. A fresh process offers the
  `fresh` capability and serves one invocation in a working directory of
  its own. It exits once its result has gone out, as Windmill's wrapper
  does after writing it, and the engine kills it if the invocation times
  out or is canceled. The step suite runs in both modes
  (`wltest check --mode fresh`). It also decided that a step module's
  top-level code runs in its loading invocation's context, but that the
  timers and connections it starts are module state, which the settle-time
  fence of D9 leaves alone ([0004](0004-harness-protocol.md), loading).

## Spec IDs relied on

WR-1, WR-2, WR-27, WR-41, Q-WR-19, and deviations D1, D2, D5, D9 to D12
and D18 of 0001.

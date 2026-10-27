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
5. **Fresh per run.** Saves little, since each step of a run is usually a
   different script.

## Decision

Three modes, chosen by the host, recorded in the bundle manifest and in each
run's trace:

| Mode | Module identity | Lifetime | Use |
|---|---|---|---|
| `fresh` | per invocation | one harness process per invocation, killed afterwards | Windmill-equivalent; the conformance suite; scripts a host lists by path |
| `warm` (default) | per script, as in Windmill | the harness process | production default (deviation D1) |
| `shared` | per harness build | the harness process | memory-constrained hosts whose scripts were written for one shared process |

- **Default `warm`.** Module-scoped state stays per script, as in Windmill,
  and only its lifetime changes. `globalThis` is the exception: one process
  serves every script, so all scripts in it share one global object, which
  case WR-1/b exposes. That is the smallest deviation that keeps per-step
  cost in microseconds.
- **Opt-out.** A host can run the whole bundle `fresh`, or list script paths
  (glob patterns) that always run `fresh`. `fresh` also gives back kill
  semantics on timeout and cancellation ([0004](0004-harness-protocol.md), D2).
- **Opt-in `shared`.** It is a separate build output ([0010](0010-packaging.md))
  because sharing chunks changes bundling. It suits a host that already ran
  its scripts in one process.
- **Conformance.** The suite runs every flow and step case in `fresh`, and
  the step cases also in `warm`, where WR-1/a and WR-1/b are expected to read
  `1` then `2`: the deviation is tested, not assumed.
- **`globalThis`** survives in `warm` and `shared`, across invocations and
  across scripts (deviation D1). Code that keeps resources
  there on purpose, such as a connection cache registered on a global object,
  keeps working, and that is the point of a warm harness.

## Consequences

- A host turning on `warm` takes responsibility for module-level state in its
  scripts. The run trace records the mode, so an incident can be traced to
  it.
- Production memory grows with the number of distinct scripts in `warm`
  (each carries its dependencies); `shared` is the remedy.
- First-invocation latency includes module evaluation; eager loading
  ([0004](0004-harness-protocol.md)) moves it to process start.

## Spec IDs relied on

WR-1, WR-2, WR-27, Q-WR-19.

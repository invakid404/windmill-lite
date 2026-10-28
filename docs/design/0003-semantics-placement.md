# 0003 · Where semantics live, and the conformance-suite format

**Status:** proposed · **Topic:** which process interprets flows and
evaluates expressions, how semantics stay single-sourced between Go and the
language harnesses, how the expression engine is run, and the format of the
shared conformance suite.

## Context

Steps run inside a warm language harness (Bun first). Transforms and
predicates are JavaScript. A natural design runs a whole same-language
**segment** inside the harness, so consecutive steps need no round trip to
Go; but Go is the primary language, and the flow semantics (the decision list
of [FX-2](../semantics/execution.md#fx-2), the container rules of
`control-flow.md`, retries, failure modules) are intricate. A hand-written
TypeScript mirror of exactly these rules is what drifts today; windmill-lite
exists to remove that drift.

Two facts from the reference constrain every option:

1. **Expressions run on QuickJS-ng**, not on V8 or JavaScriptCore: rquickjs
   `0.11.0` on quickjs-ng revision `be664ca1`, a fresh runtime and context per
   evaluation, no `Intl`, `atob`, `URL`, `console` or timers, and
   QuickJS-ng's own error texts ([JE-1](../semantics/expressions.md#je-1),
   [JE-15](../semantics/expressions.md#je-15), [JE-21](../semantics/expressions.md#je-21)).
   Bun's own engine would give different messages and globals, so exact
   semantics need QuickJS-ng wherever expressions are evaluated.
2. **Values cross every boundary as JSON text**, and which text (the
   producer's, or the jsonb-normalised stored form) depends on the read route
   ([FX-42](../semantics/execution.md#fx-42), [JE-44](../semantics/expressions.md#je-44)).
   QuickJS-ng has its own heap, so feeding it any value means converting from
   JSON text whichever process hosts it ([JE-16](../semantics/expressions.md#je-16)).
   The cost of an evaluation therefore grows with the size of what it binds:
   a transform that mentions `flow_input` binds the whole input object
   ([JE-10](../semantics/expressions.md#je-10)).

## Measurements

Throwaway prototypes, kept outside this repository; milestone G1 re-creates
them as in-repository benchmarks. Apple M4 (10 cores), macOS, Go 1.27.3,
Bun 1.4.2 unless a row says 1.3.10. The QuickJS-ng builds used are not
revision `be664ca1`; the numbers describe the engine and its hosting, not the
pinned revision. The expression measured is an invented synthetic transform,
`results.fetch_order.archived ? null : flow_input.order.ref`, binding
`flow_input` and one result; inputs are synthetic order objects of about
1 KB per line item.

**Go ↔ warm Bun round trip**, length-prefixed JSON frames, the Bun side
parsing the request and serialising a result with the Windmill replacer
([WR-13](../semantics/languages.md#wr-13)):

| Payload | Unix socket p50 / p99 | stdio p50 |
|---|---|---|
| 26 B | 12.5 µs / 28 µs | 27 µs |
| 3 KB object | 18.4 µs / 60 µs | 33 µs |
| 100 KB list | 257 µs | 155 µs |
| 1 MiB string | 2.5 ms | 1.5 ms |

Eight concurrent callers on one socket reach 418 000 tiny calls per second.
Large payloads are dominated by JSON encoding and decoding, which Windmill's
semantics require at every boundary anyway.

**One QuickJS-ng evaluation in Go**, by bound `flow_input` size (p50):

| Bound input | wazero, JSON spliced into the source | wazero, JSON passed as a host string and parsed once | wazero, first evaluation on a never-used runtime | cgo native, reused | cgo native, first evaluation on a never-used runtime |
|---|---|---|---|---|---|
| 50 B | 45 µs | 44 µs | 53 µs | 85 µs | 87 µs |
| 4 KB | 123 µs | 69 µs | 79 µs | 89 µs | 91 µs |
| 16 KB | 374 µs | 151 µs | — | — | — |
| 64 KB | 1.31 ms | 431 µs | 427 µs | 159 µs | 162 µs |
| 256 KB | 5.03 ms | 1.54 ms | — | 376 µs | 380 µs |

wazero 1.9.0 through fastschema/qjs 0.0.6; cgo through buke/quickjs-go 0.7.7
(quickjs-ng 0.15.1), whose own promise-await loop sets the ~85 µs floor.
Creating a fresh runtime and context costs 221 µs p50 in wazero (inside one
WASM instance; a new instance is 1.4 ms) and 84 µs natively. Running bytecode
compiled once instead of the wrapper's source, on a fresh wazero runtime with
a 4 KB input, costs 46 µs instead of 77 µs. For comparison, QuickJS-ng in
Bun's WASM costs 110 µs fresh and 24 µs reused (small input), and
JavaScriptCore's own `new Function` 0.3 µs (not QuickJS).

**A bounded pool in Go** (wazero, 8 WASM instances, each holding one
prepared fresh runtime; an evaluation takes a prepared instance, and the
instance frees and prepares the next runtime on its own goroutine; latency
includes waiting for a prepared instance):

| Load, 4 KB input unless stated | p50 | p95 | p99 | CPU per evaluation |
|---|---|---|---|---|
| 8 callers, 0.5 ms between evaluations | 120 µs | 260 µs | 583 µs | 443 µs |
| 8 callers, back to back | 547 µs | 1.08 ms | 2.49 ms | 538 µs |
| 16 callers, back to back | 976 µs | 1.54 ms | 3.76 ms | 507 µs |
| burst of 64 at once | 1.96 ms | 3.60 ms | 3.93 ms | 514 µs |
| 8 callers, 64 KB | 946 µs | 1.77 ms | 4.22 ms | 1.1 ms |
| 8 callers, 256 KB | 2.31 ms | 6.19 ms | 63.7 ms | 3.0 ms |

After the 256 KB runs the eight instances held 570 MiB of WASM linear
memory, which never shrinks. With 4 instances, latencies under load roughly
doubled.

## Options considered

**(a) Go lowers the IR into a segment program that a deliberately dumb VM in
each harness executes.** Go keeps the semantics in the compiler; each
harness carries a VM. But the Go side still needs the same VM to run the
parts between segments, so the VM exists twice. And the saving is small:
expressions still need QuickJS-ng, which inside Bun costs 24–54 µs per
evaluation on a reused context and 110–140 µs fresh, against a 12.5–18 µs
round trip to Go. For a bundle with one language, (a) is a TypeScript
interpreter, which is (b) under another name.

**(b) A full interpreter in each language, kept equal by a shared suite.**
This is the hand-written mirror again, multiplied by the number of languages.
Rejected.

**(c) The Go core interprets every flow and evaluates every expression
through an embedded QuickJS-ng; harnesses only run steps.** One
implementation of the decision list and of the expression pipeline. Cost: a
round trip per step and Go-side QuickJS.

**(d) Hybrids**, for example (c) with straight-line batches pushed to the
harness. Any batch whose steps have expressions needs QuickJS-ng in the
harness, which is (a) with its duplication.

## Decision

**(c).**

1. **One flow interpreter, in Go** (`engine`). It owns the run state, the
   decision list, containers, retries, sleeps, timeouts, failure modules,
   cancellation and host hooks.
2. **One expression pipeline, in Go** (`expr`). The textual stages
   (trimming, the shortcuts of [JE-6](../semantics/expressions.md#je-6) to
   [JE-9](../semantics/expressions.md#je-9), the rewrites of
   [JE-11](../semantics/expressions.md#je-11) and
   [JE-12](../semantics/expressions.md#je-12), statement-mode detection,
   binding analysis) run at bundle open where they depend only on the text,
   and at run time where they depend on the preceding id.
3. **The engine is QuickJS-ng at revision `be664ca1`** (MIT), vendored as C
   source and embedded **natively through cgo**, behind a Go interface. Native
   wins because it scales about four times better with the bound input
   (376 µs against 1.54 ms at 256 KB) and creates a runtime in 84 µs rather
   than 221 µs. A **wazero backend is not in the first edition**: every
   target this design ships (`linux/amd64`, `linux/arm64`, `darwin/arm64`)
   builds native, and no consumer needs a build without cgo. The interface
   keeps the option; a wazero backend would come with its own record and a
   named consumer. **Release engineering:**
   - **Toolchain.** `zig cc` cross-compiles the vendored C for both Linux
     targets against musl, so the Linux engine is one static binary that
     runs on an empty base image; macOS builds use the system clang.
   - **CI.** Both CI runners build the C, including under `-race`.
   - **Exit checks.** B1a checks that the Linux binaries are static, and F4
     that they start on an empty base.
   - **Stack.** QuickJS-ng's maximum stack size is pinned to its built-in
     default of 1 MiB and recorded in the manifest, because stack-overflow
     depth is observable; semantics request 3 (README) asks which value
     Windmill's embedding uses, and the pin follows the answer.
4. **Every evaluation gets a fresh runtime and context**, as in Windmill
   ([JE-1](../semantics/expressions.md#je-1)). Reusing a runtime with a fresh
   context was weighed and rejected: leftover garbage would count against
   the 128 MiB limit of the next evaluation ([JE-3](../semantics/expressions.md#je-3)).
5. **Binding.** Context values reach the engine as host strings that the
   wrapper parses once; they are never spliced into source text (that costs
   three times as much at 64 KB). Each expression's wrapper is compiled to
   bytecode once, when the bundle opens, and each evaluation runs that
   bytecode on its fresh runtime (46 µs instead of 77 µs at 4 KB). A wrapper
   that fails to compile is recorded and its error is raised at evaluation
   time, with the engine's text, as Windmill reports it.
6. **The pool.**
   - **Size.** N evaluation slots, default `min(GOMAXPROCS, 8)`, host
     settable. A slot is one prepared fresh runtime on its own locked OS
     thread.
   - **Replenishment.** After an evaluation the slot frees its runtime and
     prepares the next one on its own thread, off the caller's path.
   - **Drained pool.** A caller that finds no prepared slot waits in FIFO
     order for the next one. No slot is ever created on the critical path,
     and the wait is a span in the run trace.
   - **Memory.** Each runtime has Windmill's 128 MiB heap limit, which the
     host can override with `QUICKJS_MEMORY_LIMIT_MB` as on Windmill
     ([JE-3](../semantics/expressions.md#je-3)), and freeing
     a runtime returns its memory to the allocator, so there is no
     instance-level growth to manage (the wazero prototype held 570 MiB of
     linear memory after 256 KB runs, which is part of why it is deferred).
   - **Interrupts (D8).** An attempt that reaches 20 s is interrupted through
     QuickJS-ng's interrupt handler, and the slot prepares a new runtime.
7. **Harnesses are step runners.** Given a script, arguments and a
   per-invocation environment, a harness binds arguments, hydrates, calls the
   entrypoint, serialises the result or error with its language's rules, and
   routes platform calls back to Go ([0004](0004-harness-protocol.md),
   [0007](0007-platform-interface.md)). Language rules that only make sense
   in one language live in that harness and only there: TypeScript signature
   parsing, `Date` hydration, `JSON.stringify` replacers, error extraction
   ([WR-6](../semantics/languages.md#wr-6) to
   [WR-21](../semantics/languages.md#wr-21)).
8. **No segments.** The charter's segments (runs of same-language steps
   chained in one process) were weighed. With the interpreter in Go nothing
   would execute them: placement is per leaf, by the pool key, and host
   scopes are per run. The IR and the protocol therefore carry no segment
   concept ([0002](0002-ir-and-loader.md)). Adding a harness-side executor
   later would need a new record and a measured reason.

### What the numbers do and do not show

The microbenchmarks fix the decision's shape; they do not prove the latency
of a real run.

- Per step, the protocol costs 12.5–18 µs for small values and grows with
  size at the rate JSON encoding sets.
- Per engine-evaluated expression, the cost on an unloaded pool is roughly
  the first-evaluation column above, and it is dominated by the bound size.
  Under sustained load, replenishment competes for CPU: about 0.5 ms of CPU
  per evaluation in wazero at 4 KB.
- Any per-run figure built from these rows is a **hypothesis** until the
  end-to-end budget below holds on the pinned versions.

**Budget gate (milestone B2b exit criterion).** An engine-evaluated predicate
over a `results` value of 4 KB, 64 KB and 256 KB, on the default (native)
backend with the default pool, on the benchmark machine. The load is eight
concurrent runs, each issuing its next evaluation 0.5 ms after the previous
one returns, which is the shape of runs that interleave evaluations with
step I/O (the paced row of the pool table above). Back-to-back load is
measured and reported beside it but not gated: it saturates the CPU by
design, and the wazero numbers above show it at about four times the paced
latency. The native pool has not been measured under load yet; B1a measures
it, and B2b may not merge on the wazero figures:

| Bound value | p95 | p99 |
|---|---|---|
| 4 KB | ≤ 0.5 ms | ≤ 1.5 ms |
| 64 KB | ≤ 2 ms | ≤ 6 ms |
| 256 KB | ≤ 6 ms | ≤ 20 ms |

Latency includes the wait for a prepared slot. If the gate fails, B2b does
not merge. The remedies, in order: pool size,
memory recycling thresholds, and binding only the paths an expression reads
when a static check proves the expression cannot observe the rest. That last
remedy needs its own record.

### The conformance-suite format

Every Windmill rule is single-sourced in one implementation, so the suite's
job is to pin each implementation to the reference and to keep the Go and
harness sides of each boundary in agreement. Cases live under `conformance/`
as hand-written YAML and JSON, at three levels:

| Level | Directory | A case is | Compared by |
|---|---|---|---|
| flow | `conformance/flows/<area>/<case>/` | a sync tree, setup, runs and expected observations, in the fixture format of [OR-15](../semantics/oracle.md#or-15) | Go `wltest check`, in each configuration below; the oracle recorder `wltest record` |
| step | `conformance/steps/<lang>/<case>.yaml` | one script, its arguments, environment and boundary mode, the expected **stored** result or error (Windmill-observable terms), and the expected platform calls | Go `wltest check`, driving the real harness through the protocol; the oracle via a generated one-step flow |
| expr | `conformance/exprs/<case>.yaml` | a **context-free** expression: an input transform or predicate of a flow's first step, with `flow_input` and `flow_env` as its only bound values (producer text; the stored form is derived with `jsonval`), expected JSON value or error | Go `expr` tests; the oracle via a generated one-step flow |

**Contextual expression cases are flow fixtures.** A one-step flow cannot
reproduce an expression whose meaning depends on what ran before it: a
named predecessor, stored versus carried results, a retry, a container or a
failure handler. Such cases are written as ordinary flow fixtures, with the
producer steps, containers and handlers the context needs, and the
expression at its real site; they are never lifted automatically. The two
acceptance examples are JE-8 (a step `p` returns `{"b":1,"a":2}` and the next
step's transform `results.p` must return `previous_result` verbatim, in the
producer's key order rather than the stored jsonb order, so the two routes
can be told apart) and FX-V6/a (a
step stores a result, an error stop fails it, and the failure handler's two
transforms distinguish the stored value from the carried `EarlyStopError`).
An expression case that names a predecessor, a handler or a container fails
validation with `contextual_case_needs_flow_fixture`.

- **One comparator.** Every expectation is compared once, in Go. The
  harness's own tests (`bun test`) are fast developer checks against golden
  frames that the Go run writes; they never gate a merge, so the outcome
  mapping, placeholders and normalisation exist only in Go.
- **Configurations.** Flow cases run in three: `fresh`, `warm`, and **host
  mode** through a small public Bun test host built on the host SDK
  ([0009](0009-host-adapters.md)). Cases that demonstrate a deviation
  ([0001](0001-equivalence-contract.md) §5) carry the expected value per
  configuration; every other case must pass in all three. Passing the flow
  suite in `warm` and in host mode is an exit criterion of milestone F3b.
- **Provenance.** Every case names the catalogue case it encodes
  (`spec: JE-9/c`), its scope, and its provenance: `source-derived` until
  recorded, then `oracle-observed`; `disputed` while its finding is not
  settled; or `policy`. Contested cases follow
  [0001](0001-equivalence-contract.md) §6: they are recorded early, and the
  engine is built against the recording, so no case is ever checked against
  two readings.
- **Notation.** Expected values use the catalogue's notation (`<stack>`,
  `<any>`, `<job:PATH>`) with the identity encoding of
  [0011](0011-differential-testing.md).

## Consequences

- The decision list, the container rules and the expression pipeline each
  exist once, in Go.
- A Python, Deno or Go harness later needs only step-runner logic; it gets
  every flow feature for free.
- Each step costs a round trip; [0005](0005-json-step-boundaries.md) keeps the
  JSON work to what the semantics require.
- Expression cost scales with what an expression binds. The design measures
  it, gates it in B2b, and uses the faster backend; it does not
  claim a per-run figure before that gate.
- Every build needs a C toolchain (`zig cc` for Linux releases); builds
  with `CGO_ENABLED=0` are not supported in the first edition.
- Host code that must run in the harness (interceptors that call step code,
  scoped context) reaches it through host plugins ([0008](0008-host-hooks.md)),
  not through a harness-side interpreter.

## Spec IDs relied on

FX-2, FX-42, JE-1, JE-3, JE-5 to JE-16, JE-21, JE-22, JE-44, WR-6 to WR-21,
OR-15, and the conformance catalogue's notation and provenance rules.

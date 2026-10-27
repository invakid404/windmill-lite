# 0003 · Where semantics live, and the conformance-suite format

**Status:** proposed · **Topic:** which process interprets flows and
evaluates expressions, how semantics stay single-sourced between Go and the
language harnesses, and the format of the shared conformance suite.

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

## Measurements

Throwaway prototypes, kept outside this repository; milestone G1 re-creates
them as in-repository benchmarks. Apple M4 (10 cores), macOS, Go 1.27.3,
Bun 1.4.2. The QuickJS-ng builds used are not revision `be664ca1`; the
numbers are indicative of the engine, not of the pinned revision.

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

**One QuickJS-ng evaluation** of a rewritten input transform
(`results.a.ingestion ? undefined : flow_input.data.id`, binding `flow_input`
and one result from JSON text, a lazy `results` proxy, `JSON.stringify`):

| Engine host | Fresh runtime + context | Reused context |
|---|---|---|
| Go, wazero 1.9.0 (fastschema/qjs 0.0.6) | 250 µs (46 µs eval + 204 µs runtime setup); 1.4 ms with a new WASM instance | 46 µs (118 µs with a 4 KB input) |
| Go, cgo, quickjs-ng 0.15.1 (buke/quickjs-go 0.7.7) | 188 µs (279 µs with 4 KB) | 76 µs (163 µs) |
| Bun, WASM (quickjs-emscripten 0.32.0, ng variant) | 110 µs (140 µs) | 24 µs (54 µs) |
| Bun, JavaScriptCore `new Function` (not QuickJS) | — | 0.3 µs |

## Options considered

**(a) Go lowers the IR into a segment program that a deliberately dumb VM in
each harness executes.** Go keeps the semantics in the compiler; each
harness carries a VM. But the Go side still needs the same VM to run the
parts between segments, so the VM exists twice. And the saving is small:
expressions still need QuickJS-ng, which inside Bun costs 24–54 µs per
evaluation on a reused context and 110–140 µs fresh, against a 12.5–18 µs
round trip to Go. A step typically has one to three expressions. Moving the
interpreter into the harness saves one round trip per step and doubles the
code that must agree.

**(b) A full interpreter in each language, kept equal by a shared suite.**
This is the hand-written mirror again, multiplied by the number of languages.
Rejected.

**(c) The Go core interprets every flow and evaluates every expression
through an embedded QuickJS-ng; harnesses only run steps.** One
implementation of the decision list and of the expression pipeline. Cost: a
round trip per step (12.5–18 µs for typical payloads) and Go-side QuickJS.

**(d) Hybrids**, for example (c) with straight-line batches pushed to the
harness. Any batch whose steps have expressions needs QuickJS-ng in the
harness, which is (a) with its duplication.

## Decision

**(c), keeping segments as a placement and scope unit.**

1. **One flow interpreter, in Go** (`engine`). It owns the run state, the
   decision list, containers, retries, sleeps, timeouts, failure modules,
   cancellation and host hooks.
2. **One expression pipeline, in Go** (`expr`). The textual stages
   (trimming, the shortcuts of [JE-6](../semantics/expressions.md#je-6) to
   [JE-9](../semantics/expressions.md#je-9), the rewrites of
   [JE-11](../semantics/expressions.md#je-11) and
   [JE-12](../semantics/expressions.md#je-12), statement-mode detection,
   binding analysis) run at bundle build time where they depend only on the
   text, and at run time where they depend on the preceding id. Evaluation
   uses **QuickJS-ng at revision `be664ca1`** (MIT), compiled once to WASM by
   a reproducible build script and run in **wazero** (pure Go, no cgo). Every
   evaluation gets a fresh runtime and context, as in Windmill; a pool
   prepares fresh contexts ahead of time on idle cores, so the critical path
   pays only the evaluation (about 46 µs here) and not the 204 µs setup.
   Native QuickJS-ng through cgo was measured too: its fresh runtime is
   cheaper (188 µs against 250 µs), but evaluation on a prepared context is
   slower through the binding used (76 µs against 46 µs), so it gains nothing
   on the critical path and would cost a C toolchain in every build. The
   engine sits behind an interface, so a native backend can still be added if
   the latency benchmark ever demands it.
3. **Harnesses are step runners.** Given a script, arguments and a
   per-invocation environment, a harness binds arguments, hydrates, calls the
   entrypoint, serialises the result or error with its language's rules, and
   routes platform calls back to Go ([0004](0004-harness-protocol.md),
   [0007](0007-platform-interface.md)). Language rules that only make sense
   in one language live in that harness and only there: TypeScript signature
   parsing, `Date` hydration, `JSON.stringify` replacers, error extraction
   ([WR-6](../semantics/languages.md#wr-6) to
   [WR-21](../semantics/languages.md#wr-21)).
4. **Segments** (maximal same-class subtrees, [0002](0002-ir-and-loader.md))
   decide which warm process runs a step and which host scope a run enters.
   They are not executed inside the harness. The protocol reserves a
   capability flag, `segments`, so a harness-side executor could be added
   later without a protocol break; adding one requires a new decision record
   and a measured reason.

Per step, the price of this decision is one round trip plus the Go-side
handling of its JSON. For the measured payloads that is 12.5–18 µs for small
values and grows with size at the rate JSON encoding sets. The benchmark plan
in [0011](0011-differential-testing.md) watches it.

### The conformance-suite format

Every Windmill rule is single-sourced in one implementation, so the suite's
job is to pin each implementation to the reference and to keep the Go and
harness sides of each boundary in agreement. Cases live under `conformance/`
as YAML and JSON files that no code generates, at three levels:

| Level | Directory | A case is | Run by |
|---|---|---|---|
| flow | `conformance/flows/<area>/<case>/` | a sync tree, setup, runs and expected observations, in the fixture format of [OR-15](../semantics/oracle.md#or-15) | Go `wltest check` (engine plus real harness); the oracle recorder `wltest record` |
| step | `conformance/steps/<lang>/<case>.yaml` | one script, its arguments and environment, the boundary mode, the expected result or error envelope, and expected platform calls | the harness's own test runner (`bun test`) **and** Go through the protocol; the oracle via a one-step flow |
| expr | `conformance/exprs/<case>.yaml` | site kind, text, bindings (JSON texts for `flow_input`, `previous_result`, results by id with their stored and in-memory forms), expected JSON value or error | Go `expr` tests; the oracle via a generated one-step flow |

Running each step case from both the harness's test runner and from Go keeps
the two sides of the protocol honest: a harness that passes its own tests but
behaves differently when Go drives it fails the Go run.

Every case names the catalogue case it encodes (`spec: JE-9/c`), its scope,
its provenance (`source-derived` until recorded, then `oracle-observed`;
`disputed` while its finding is not settled; or `policy`), and, for a
contested rule, the reading it expects ([0001](0001-equivalence-contract.md) §6).
The catalogue says not to encode either side of a dispute as a passing test
until its question closes. The suite departs from that on purpose: it checks
each contested case under both readings, and each must fail under the other
one, to prove that the switch implements both as drafted. Such a case keeps
its `disputed` tag and never counts as evidence of Windmill's behaviour; only
an oracle recording does. Expected values use the notation of the catalogue (`<stack>`, `<any>`,
`<job:PATH>`) with the identity encoding of
[0011](0011-differential-testing.md).

## Consequences

- The decision list, the container rules and the expression pipeline each
  exist once, in Go.
- A Python, Deno or Go harness later needs only step-runner logic; it gets
  every flow feature for free.
- Each step costs a round trip; [0005](0005-json-step-boundaries.md) keeps the
  JSON work to what the semantics require.
- Go-side QuickJS evaluation costs CPU for the pre-warmed contexts (about
  204 µs per evaluation, off the critical path).
- Host code that must run in the harness (interceptors that call step code,
  scoped context) reaches it through host plugins ([0008](0008-host-hooks.md)),
  not through a harness-side interpreter.

## Spec IDs relied on

FX-2, FX-42, JE-1, JE-5 to JE-16, JE-21, JE-22, JE-44, WR-6 to WR-21, OR-15,
and the conformance catalogue's notation and provenance rules.

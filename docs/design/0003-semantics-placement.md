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
   - **Member paths are answered in Go (PS1).** A text that only reads a
     member path of a JSON value, such as `previous_result.items[0]?.id`,
     gets the program's own answer without the engine. That is
     `JSON.parse`, JavaScript's property reads and optional chaining, then
     `JSON.stringify`.
   - Wherever JavaScript would do more than read the value's own data, the
     text goes to the engine. That covers an inherited property, a
     primitive's member, a `TypeError`, and a value that needs JE-16's
     rebuild.
   - The engine declares that it evaluates such paths exactly
     (`ExactMemberPaths`). A differential test against QuickJS-ng over
     generated values and paths holds the two to each other. Such a path
     takes about 0.6 µs instead of 26 to 48 µs on a runtime.
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
     depth is observable. Semantics request 3 (README) is answered by
     [JE-48](../semantics/expressions.md#je-48): Windmill's embedding never
     sets a stack size, so it runs on that default, measured from where each
     call enters the engine. Evaluations run on threads that the engine
     creates with stacks well above the limit (at least 8 MiB), because a
     thread the Go runtime creates gets the C library's default stack, which
     is 128 KiB on musl and would end before the engine's limit.
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
latency. B1a measured the native pool under these loads (the table after
the gate, below); B2b may not merge on the wazero figures, nor on B1a's,
which bind values without the pipeline's wrapper:

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

**The native pool under load (milestone B1a).** `TestPoolUnderLoad` in
`internal/expr/quickjs` evaluates a stand-in predicate: it binds an invented
order object of the row's size as one results value and a small flow input,
both as host strings; parses both; and serialises
`Boolean(results.fetch_order.archived ? null : flow_input.order.ref)`. There
is no pipeline wrapper and no shortcut. QuickJS-ng `be664ca1` built with the
system clang at `-O2`, the default pool (8 slots), Apple M4 (4 performance
and 6 efficiency cores), macOS 26.5, Go 1.27.3, 3 s per row. The machine was
shared with other work, at a load average of about 2.9 when the run started.
CPU per evaluation is the process's CPU time divided by the evaluations, so
it includes freeing each runtime and preparing the next. A burst's latency
counts from when the burst is issued.

| Load | Bound value | p50 | p95 | p99 | CPU per evaluation |
|---|---|---|---|---|---|
| 1 caller, 2 ms between evaluations | 4 KB | 89 µs | 150 µs | 191 µs | 397 µs |
| 8 callers, 0.5 ms between evaluations | 4 KB | 95 µs | 152 µs | 193 µs | 221 µs |
| 8 callers, back to back | 4 KB | 190 µs | 264 µs | 336 µs | 201 µs |
| 16 callers, back to back | 4 KB | 381 µs | 438 µs | 588 µs | 199 µs |
| burst of 64 at once | 4 KB | 1.44 ms | 4.55 ms | 5.56 ms | 311 µs |
| 1 caller, 2 ms between evaluations | 64 KB | 257 µs | 328 µs | 569 µs | 406 µs |
| 8 callers, 0.5 ms between evaluations | 64 KB | 350 µs | 539 µs | 582 µs | 470 µs |
| 8 callers, back to back | 64 KB | 495 µs | 848 µs | 1.04 ms | 568 µs |
| 16 callers, back to back | 64 KB | 1.12 ms | 1.54 ms | 1.91 ms | 566 µs |
| burst of 64 at once | 64 KB | 4.01 ms | 5.54 ms | 9.72 ms | 606 µs |
| 1 caller, 2 ms between evaluations | 256 KB | 939 µs | 991 µs | 1.55 ms | 1.10 ms |
| 8 callers, 0.5 ms between evaluations | 256 KB | 1.32 ms | 2.10 ms | 2.31 ms | 1.61 ms |
| 8 callers, back to back | 256 KB | 1.45 ms | 2.46 ms | 2.87 ms | 1.68 ms |
| 16 callers, back to back | 256 KB | 3.33 ms | 4.40 ms | 5.13 ms | 1.68 ms |
| burst of 64 at once | 256 KB | 7.89 ms | 13.6 ms | 15.1 ms | 1.68 ms |

The paced rows sit inside the gate's bounds at every size, but they are not
the gate: B2b measures through the pipeline. On a busier machine the tails
grew several times over. A run at a load average of about 20 before it
started gave a paced 4 KB p99 of 1.98 ms and a paced 256 KB p99 of 59.5 ms,
so the gate needs a quiet machine. The unloaded rows are not a lower bound:
an idle caller on this machine often runs at a lower clock. CI reports the
same table on each run from its shared Linux and macOS runners, where the
default pool follows the runner's CPU count (3 to 5 slots so far); those
figures show the trend, not the gate.

**The budget gate, met (milestone B2b).** `TestBudgetGate` in
`internal/expr` evaluates the predicate
`results.fetch_order.archived ? null : flow_input.order.ref` at a `skip_if`
site through the whole pipeline: the shortcut check, the depth check, the
three scripts and the program on a fresh runtime, the results lookup a
database read of a synthetic order of the row's size (the previous step
is another), and the serialisation. Default (native) backend and pool
(8 slots), QuickJS-ng `be664ca1` built with the system clang at `-O2`, Apple
M4, macOS 26.5.1, Go 1.27.3, 3 s per row, at a load average of 3.4 to 4.7
from other work. The gated rows are the paced ones:

| Load | Results value | p50 | p95 | p99 | CPU per evaluation | Gate |
|---|---|---|---|---|---|---|
| 8 callers, 0.5 ms between evaluations | 4 KB | 121 µs | 223 µs | 437 µs | 222 µs | p95 ≤ 0.5 ms, p99 ≤ 1.5 ms: met |
| 8 callers, back to back | 4 KB | 176 µs | 290 µs | 457 µs | 176 µs | reported |
| 8 callers, 0.5 ms between evaluations | 64 KB | 313 µs | 449 µs | 573 µs | 370 µs | p95 ≤ 2 ms, p99 ≤ 6 ms: met |
| 8 callers, back to back | 64 KB | 414 µs | 643 µs | 769 µs | 445 µs | reported |
| 8 callers, 0.5 ms between evaluations | 256 KB | 1.02 ms | 1.65 ms | 1.84 ms | 1.18 ms | p95 ≤ 6 ms, p99 ≤ 20 ms: met |
| 8 callers, back to back | 256 KB | 1.13 ms | 1.86 ms | 2.13 ms | 1.31 ms | reported |

With `fetch_order` as the previous step, the text also binds the value as
`previous_result` and `result` (JE-10), so it is parsed three times; that
paced row gives a p95 of 221 µs, 1.24 ms and 5.19 ms at the three sizes,
inside the gate too. CI reports the same table on each run, from its
shared runners and from the static musl engine on `amd64` and `arm64`
(review item cl R3-2); those figures show the trend, not the gate.

### The conformance-suite format

Every Windmill rule is single-sourced in one implementation, so the suite's
job is to pin each implementation to the reference and to keep the Go and
harness sides of each boundary in agreement. Cases live under `conformance/`
as hand-written YAML and JSON, at three run levels, four levels that
observe no run (decode, jsonb, load and compile), one protocol level (whose
golden payloads the Go run writes), one build level and one hooks level:

| Level | Directory | A case is | Compared by |
|---|---|---|---|
| flow | `conformance/flows/<area>/<case>/` | a sync tree, setup, runs and expected observations, in the fixture format of [OR-15](../semantics/oracle.md#or-15) | Go `wltest check`, in each configuration below; the oracle recorder `wltest record` |
| step | `conformance/steps/<lang>/<case>.yaml` | one script, its arguments, environment and boundary mode, the expected **stored** result or error (Windmill-observable terms), and the expected platform calls | Go `wltest check`, driving the real harness through the protocol; the oracle via a generated one-step flow |
| expr | `conformance/exprs/<case>.yaml` | a **context-free** expression: an input transform or predicate of a flow's first step, with `flow_input` and `flow_env` as its only bound values (producer text; the stored form is derived with `jsonval`), expected JSON value or error | Go `expr` tests; the oracle via a generated one-step flow |
| decode | `conformance/openflow/<finding>.json` | an OpenFlow flow value as JSON and the effective decoder's decision: the rejection's message substrings, or parts of the accepted value's decoded form ([OF-2](../semantics/openflow.md#of-2)) | Go `internal/openflow` tests; on the oracle, a create-flow request once [Q-OF-2](../semantics/openflow.md#q-of-2) is recorded |
| jsonb | `conformance/jsonb/<finding>.yaml` | a producer text and its stored text or the rejection jsonb gives it ([FX-42](../semantics/execution.md#fx-42), [JE-44](../semantics/expressions.md#je-44), [FX-48](../semantics/execution.md#fx-48)); for a catalogue case, also the form its consumer reads and the top-level key order expected there | Go `internal/jsonval` tests; PostgreSQL in the oracle's pinned image ([0005](0005-json-step-boundaries.md)) |
| load | `conformance/syncfolder/<finding>.json` | a sync folder as a map of files and what the loader makes of it: whether it opens, the items it lists or filters out, and parts of the records it reads or their errors ([SY-1](../semantics/openflow.md#sy-1) to [SY-15](../semantics/openflow.md#sy-15)) | Go `internal/syncfolder` tests |
| compile | `conformance/ir/<finding>.json` | a sync folder, a host contract and build options, and what the compiler makes of them ([0002](0002-ir-and-loader.md)): its errors, or parts of the program, addressed by flow, by a step's site and by inventory id; for a flow case, the part a deploy decides (defaults, the native switch, virtual steps, collapse) | Go `internal/ir` tests |
| protocol | `conformance/protocol/frames.json`, `conformance/protocol/exchanges/<case>.json` | the harness protocol of [0004](0004-harness-protocol.md), which is windmill-lite's own (provenance `policy`): golden frames of the codec (a message and its canonical payload, a part split, a byte stream and the protocol error it raises), and exchange transcripts (the frames the engine sends and those the harness must answer with) over a synthetic fixture bundle | Go `internal/harness` tests, the exchanges against the real harness on the pinned runtime; each harness's own developer tests run the codec cases |
| plugins | `conformance/plugins/hooks.yaml` | host-plugin cases (milestone F2c, provenance `policy`): a synthetic flow, a declarative host plugin that answers its hooks and provided sites, and the run's expected status, result, latched error and the plugin's log | Go `wl` tests, through a TypeScript plugin over `host_call` in `warm` and through Go callbacks; Go `cmd/windmill-lite` tests, through the host SDK's plugin in host mode |
| build | `conformance/build/<finding>.json` | a sync folder and Bun scripts, and what the Bun build records for each (the signature of every export, annotations, the bundler target, workflow-as-code markers, the workspace scripts inlined) or refuses; optionally a run of the built modules' exports, with `process.env` replaced and host-provided modules registered ([WR-6](../semantics/languages.md#wr-6) to [WR-8](../semantics/languages.md#wr-8), [WR-26](../semantics/languages.md#wr-26) to [WR-32](../semantics/languages.md#wr-32), [WR-38](../semantics/languages.md#wr-38), [WR-39](../semantics/languages.md#wr-39), [WA-31](../semantics/client-api.md#wa-31), D12) | Go `internal/bundle` tests, running the Bun build helper and the modules on the pinned Bun; the helper's own developer tests run its scans |
| hooks | `conformance/hooks/<topic>.yaml` | a synthetic flow, the host's signals of [0008](0008-host-hooks.md) (H1 outcomes, latches and cancellations at a module's start or end, fatal error names, halts, the clock, a pre-write deadline), and what the run must show: its status, its latch, the sites that ran or did not, its result and its deviations; windmill-lite's own rules (provenance `policy`) | Go `wl` tests, through the public API |

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

A recorded argument cannot tell JE-8's two routes apart: the job API stores
every argument as jsonb, which reorders keys, so `{"b":1,"a":2}` comes back
as `{"a":2,"b":1}` whichever route produced it. The example is therefore
checked twice. At unit level, a flow fixture's run restates the
expression with its context (the previous id, the previous result's
producer text, the stored texts a database read returns) and the exact
evaluator output, and a test ties that context and output to the
recording through their stored forms. On the oracle, the same read sits at
the `timeout` site, a site with a results context whose parse error quotes
the evaluated text, so the producer's key order is observable in the run's
error, with the database route (a step in between) as its control.

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
- Milestone B1a measured the native pool under load and recorded it under
  the budget gate, and moved evaluations onto threads the engine creates,
  because the stack limit of JE-48 needs more stack than musl gives the Go
  runtime's threads.
- Milestone F4 measured the pool in the static musl engine: musl's `malloc`
  makes the slot threads wait on one lock, and with 8 concurrent callers an
  evaluation cost 3 to 10 times the CPU it costs with glibc. The engine's
  heap therefore allocates with mimalloc (MIT, vendored with its notice
  beside QuickJS-ng), which makes the static musl engine as fast as a glibc
  build under load; the C library's `malloc` is untouched elsewhere
  (`internal/expr/quickjs` README, Allocator). F4 also runs the expression
  suite from the static binaries and starts the static engine on an empty
  base ([0010](0010-packaging.md)).
- The decode level was added with milestone A3. A flow value's accept or
  reject decision has no run to observe, so it fits none of the three run
  levels, and the decoder is Go only, so no harness reads these cases.
- The jsonb level was added with milestone A1, for the same reasons: a
  value's stored text is observed without a run, and the canonicaliser is Go
  only. Its catalogue cases are flow cases whose texts it checks; each names
  the milestone that checks its route.
- The load level was added with milestone A4, for the same reasons: reading
  a sync folder has no run to observe, and the loader is Go only. A load
  case may also cite only its finding, for a case that extends the
  catalogue.
- The compile level was added with milestone A5, for the same reasons:
  compiling a folder has no run to observe, and the compiler is Go only.
  Most of its catalogue cases are flow cases whose expectation depends on
  what a deploy applies; a compile case checks that part and names the
  milestone that checks the run, as a stored-form case does.
- The expression level took its format with recording batch E3a: one
  `conformance/exprs/<finding>.yaml` per finding, each case's expected value
  at the recorded argument (point O2 of the catalogue's notation), with the
  evaluator's own text (O1) where it matters, and its recordings under
  `conformance/exprs/recorded/`. Flow fixtures restate their expressions as
  unit cases with their recorded context, for B2a and B2b, and the JE-8
  example has the oracle discriminator above (review item cx R16).
- Milestone B2a implemented the textual pipeline (`expr.Compile`) and the
  evaluator's dispatch (`expr.Pipeline`), and drew its line with B2b in the
  engine source. An expression's `Source` is its rewritten text in a
  single-line async function that its mode selects, called at once; B2b
  writes the program around it, which turns the bound host strings into
  values, gives the user's code `results`, `error` and the helpers, and
  serialises the value (JE-15). The evaluator binds a host string per
  context value the text names, and the results lookup and the helpers as
  host functions that answer JSON text; JE-18's previous-step read is
  answered from memory, by the results prelude since FU6 (below). Its cases are a pipeline level inside the
  package, `internal/expr/testdata/pipeline/<finding>.json`: each restates
  an E3a case, whose context it rebuilds from the recording's case, or
  gives its own, and says what `Compile` makes of the text and which route
  answers on a fake engine. B2b runs the same restatements on the real
  engine.
- Milestone B2b wrote the program around an expression's source and runs
  the pipeline on the native engine. An evaluation runs global scripts
  before the program, the engine's preludes (`Input.Preludes`): one parses
  the bound host strings in place and defines the helpers, one declares
  the lexical `results` and one the lexical `error`, which only a script
  of their own can declare (JE-10). The program is the source followed by
  the serialisation of JE-15. Their text is observable in the stack frames
  that error messages carry (JE-21), and E3a's recordings fix it: the
  wrapper is `(async function() { …` (B2a's provisional reading, an async
  function, with its exact opening), and the helpers and the results
  lookup throw from the positions the oracle shows. Every expression E3a
  recorded gives the oracle's value or text at unit level, its error texts
  byte for byte, except where the engine's build differs: the last digit
  of some transcendental `Math` results on a C library other than the
  image's glibc,
  and the recursion depth that fits the 1 MiB stack, deviations D16 and
  D17 of [0001](0001-equivalence-contract.md). The budget gate above is
  met.
- Milestone FU3 followed the semantics team's answers to B2a's and B2b's
  readings (requests 8 and 9 of the [README](README.md)). Two changed the
  engine contract: every script is a strict-mode global script (JE-50),
  and the engine runs every queued job before it reads the completion
  value and drops a job that throws (JE-51), as rquickjs's poll does. The
  rest are in the program and the pipeline: an Error's message and stack
  are read as rquickjs reads them, and another thrown value shows
  rquickjs's debug form, from the kind and payload the engine reports
  (JE-21, JE-52); a replaced `JSON.stringify`'s string is the result text
  unchecked, so the evaluator's output need not be JSON (JE-15); context
  values are rebuilt by assignment (JE-16); `error` is a `let` binding
  (JE-10) with falsy fallbacks (JE-17); the depth check follows JE-49's
  order and positions; and the call rewrite places its parenthesis at a
  character count applied as a byte offset (JE-11), failing the evaluation
  where Windmill's evaluator panics. The texts the engine supplies for
  values read from storage follow [0005](0005-json-step-boundaries.md)'s
  corrected routing table.
- Milestone FU6 followed the answers to FU3's open readings (request 10 of
  the [README](README.md)), all in the program and the pipeline: JE-11's
  count starts at each call, each helper's pass cutting the text at its
  call text, and a panicked rewrite fails with the runtime's report of the
  panicked task; the previous step's `results` read is the rebuilt
  `previous_result` itself, which the results prelude returns from memory
  when the evaluator names the previous id in a host string, while a
  fetched read and the `resource` and `flow_user_state` helpers parse
  with `JSON.parse` as it is when they run (JE-16, JE-18). The positions
  E3a's stacks show are kept.
- The protocol level was added with milestone C1. The frame codec is a rule
  that Go and every harness implement, so [AGENTS.md](../../AGENTS.md) asks
  for one shared case set that both sides run rather than two codecs that
  happen to agree; it is not Windmill behaviour, so it fits none of the
  other levels. The golden frames are the ones "the Go run writes" (above):
  `go test ./internal/harness -run TestFrameCases -args -update` rewrites
  them, and the Bun harness's `bun test` checks its codec against them.
- The build level was added with milestone C2a. The signature parse,
  annotations, workflow-as-code detection and import rewrites are rules of
  the build, which runs before any step; the catalogue cases that cover them
  are step cases, so each build case checks a case's build half and names
  where its run is checked (the step suite, C3a), and a case can run the
  built modules directly to show that the transforms behave as Windmill's.
  The parse lives in the Bun build helper, but Go is the one comparator.
- The hooks level was added with milestone F2b. The precedence of host
  signals (the latch, cancellation and deadlines, fatal error names, halts)
  against Windmill's continue decision is windmill-lite's own rule
  ([0008](0008-host-hooks.md)), with no oracle to record, so it fits none of
  the run levels; its cases run through `wl`'s public hooks, as a host
  would give them, on flows of identity modules whose jobs H1 answers, so
  no harness runs. What needs a real job (H3's first write) is checked by
  `wl`'s tests on the Bun harness instead.
- The step level took its format with milestone C3a: one
  `conformance/steps/<lang>/<finding>.yaml` per finding (or per deviation
  without one, such as `d11.yaml`), as the expression level has, each case
  one script and the runs one harness process serves in order, so module
  state across runs is observable. A run's check (the stored value as an
  observation, claims, late reports, settlement, warnings) holds in every
  configuration; a case of a deviation gives one check per configuration
  instead (`by_mode`, `warm` and `fresh`). `deferred.yaml` lists the
  catalogue cases of those findings that are not step cases. `wltest
  check` builds each file's scripts with the Bun build helper, maps every
  outcome to Windmill's stored value with the engine's mapping, and
  compares in Go. Expected platform calls arrive with the facade (PF1).

## Spec IDs relied on

FX-2, FX-42, JE-1 to JE-3, JE-5 to JE-18, JE-20 to JE-22, JE-44, JE-48, JE-49, WR-6 to WR-21,
OR-15, SY-1 to SY-15, and the conformance catalogue's notation and
provenance rules.

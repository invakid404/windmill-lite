# 0005 · JSON at step boundaries

**Status:** proposed · **Topic:** reproducing Windmill's serialise, store and
hydrate behaviour exactly while keeping the cost to what the semantics
require; why the normalising walk was dropped; and the benchmark plan.

## Context

In Windmill a value changes form as it moves:

- a Bun step's result is `JSON.stringify(v ?? null)` with an `undefined` →
  `null` replacer and a `BigInt` patch ([WR-13](../semantics/languages.md#wr-13),
  [WR-14](../semantics/languages.md#wr-14)); a native step's is
  `JSON.stringify(v ?? null)` with no replacer, so `undefined` fields are
  left out ([WR-42](../semantics/languages.md#wr-42),
  [0001](0001-equivalence-contract.md) D5);
- that **producer text** is kept in memory as `previous_result` and as
  `stop_after_if`'s `result`, and is also **stored** in PostgreSQL `jsonb`,
  which orders keys (shorter first, then bytewise), keeps the last duplicate,
  rewrites exponent numbers and rejects `\u0000`
  ([FX-42](../semantics/execution.md#fx-42), [FX-48](../semantics/execution.md#fx-48));
- the read **route** decides which form a consumer sees
  ([FX-10](../semantics/execution.md#fx-10), [JE-44](../semantics/expressions.md#je-44),
  [OR-13](../semantics/oracle.md#or-13), [OR-24](../semantics/oracle.md#or-24));
- a step's arguments are stored, then resolved for `$var:`/`$res:` markers
  with insertion order kept, then parsed by the wrapper
  ([WR-10](../semantics/languages.md#wr-10), [WR-V8](../semantics/languages.md#wr-v8),
  [FX-V5](../semantics/execution.md#fx-v5));
- the top-level order of argument maps and of `flow_input` is unspecified
  ([FX-42](../semantics/execution.md#fx-42)).

No single sort reproduces this; the reference withdrew that idea. The
question is where windmill-lite keeps which form, and how cheaply.

## Options considered

1. **Keep live JavaScript values in the harness and copy them with a
   normalising walk** that equals a JSON round trip without building the
   string. It would be the cheap path if steps were chained inside the
   harness. With
   the interpreter in Go ([0003](0003-semantics-placement.md)), Go must hold
   the text anyway: the producer text for memory routes, the stored text for
   storage routes, and text to feed QuickJS-ng.
2. **Hold every value in Go as JSON text, in two forms, and never decode what
   is only passed through.** Chosen.
3. **Decode into Go maps.** Loses number spelling and key order, and costs
   allocations; rejected.

## Measurements

A prototype walk in Bun 1.4.2 on Apple M4 (outside the repository) was two
to three times faster than a native round trip (`JSON.parse(JSON.stringify(v))`)
on objects of 26 B to 185 KB (for example 2.0 µs against 5.3 µs for a 3.8 KB
object), and shared strings (0.08 µs against 485 µs for a 1 MiB string);
jsonb key ordering roughly doubled either cost. It was also **not
equivalent**. On Bun 1.3.10 and 1.4.2 alike:

| Input | Native round trip | Walk |
|---|---|---|
| a top-level function | throws `SyntaxError` (no text to parse) | `null` |
| a top-level symbol | throws `SyntaxError` | `null` |
| a parsed object with an own `__proto__` key | keeps the key | loses it |

and a differential mode that ran both evaluated getters and `toJSON` twice,
so a stateful `toJSON` changed the value between the two runs. Across the
process boundary, a 3 KB step round trip costs 18.4 µs and a 100 KB one about
250 µs ([0003](0003-semantics-placement.md)).

### Baseline

The benchmarks of the plan below (§Benchmark plan) are in the repository,
and this is their first record. They ran on an Apple M4 (10 cores) with
16 GiB, macOS 26.5.1, Go 1.27.3 and Bun 1.3.10. Each figure is the median
of three runs (`-count 3`), taken while no other build or benchmark ran.

The one-minute load average was 2.0 to 4.5 from the machine's own
services. A first pass that overlapped another host-mode workload read
within 3 % of these medians up to 1 MiB, and 21 % higher at 4 MiB in host
mode. The commands, from the repository root, with
`WL_BUN` naming Bun 1.3.10:

```sh
go test ./internal/jsonval -run '^$' -bench BenchmarkStored -benchtime 1s -count 3
go test ./internal/harness -run '^$' -bench 'GoInvocation|ArgsFile|HarnessCodec|Transport' -benchtime 1s -count 3
go test ./cmd/windmill-lite -run '^$' -bench BenchmarkStepBoundary -benchtime 3s -count 3
go test ./cmd/windmill-lite -run '^$' -bench BenchmarkHostShapes -benchtime 20x -count 3
```

The payloads are compact JSON objects of exactly their size, shaped like
step results (`jsonval.Synthetic`). The canonicaliser against the test
implementation's decode, sort and encode, per call:

| Payload | Canonicaliser | Decode, sort and encode |
|---|---|---|
| 26 B | 0.1 µs (247 MB/s) | 1.8 µs (14 MB/s) |
| 1 KiB | 3.2 µs (322 MB/s) | 49.7 µs (21 MB/s) |
| 4 KiB | 12.1 µs (339 MB/s) | 255 µs (16 MB/s) |
| 32 KiB | 92.9 µs (353 MB/s) | 3.0 ms (11 MB/s) |
| 256 KiB | 735 µs (356 MB/s) | 24 ms (11 MB/s) |
| 1 MiB | 2.9 ms (360 MB/s) | 97 ms (11 MB/s) |
| 4 MiB | 12 ms (363 MB/s) | 383 ms (11 MB/s) |

The step boundary end to end: a flow of eight steps that each return
their argument, the payload, through shortcuts that run no expression.
Elapsed is a step's module span in the trace. CPU is per step: the
engine's from `getrusage`, the host's or harness's from
`process.cpuUsage()`.

| Payload | Host mode: elapsed p50 / p95 | Engine CPU | Host CPU | Spawned harness: elapsed p50 / p95 | Engine CPU | Harness CPU |
|---|---|---|---|---|---|---|
| 26 B | 145 µs / 169 µs | 155 µs | 16.5 µs | 147 µs / 174 µs | 158 µs | 15.2 µs |
| 1 KiB | 178 µs / 207 µs | 198 µs | 24.4 µs | 180 µs / 210 µs | 204 µs | 23.5 µs |
| 4 KiB | 266 µs / 298 µs | 277 µs | 47.8 µs | 272 µs / 315 µs | 290 µs | 46.9 µs |
| 32 KiB | 980 µs / 1.2 ms | 877 µs | 257 µs | 996 µs / 1.2 ms | 890 µs | 255 µs |
| 256 KiB | 6.5 ms / 7.2 ms | 5.5 ms | 1.9 ms | 7.0 ms / 49 ms | 6.6 ms | 2.1 ms |
| 1 MiB | 31 ms / 33 ms | 24 ms | 10.0 ms | 31 ms / 200 ms | 31 ms | 10 ms |
| 4 MiB | 124 ms / 139 ms | 98 ms | 43 ms | 142 ms / 681 ms | 120 ms | 42 ms |

With eight runs at once, the time per step is the wall time divided by the
steps of all eight:

| Payload | Host mode: time per step | Elapsed p50 / p95 | Engine CPU | Spawned harness: time per step | Elapsed p50 / p95 | Engine CPU |
|---|---|---|---|---|---|---|
| 26 B | 122 µs | 926 µs / 1.1 ms | 164 µs | 128 µs | 952 µs / 1.2 ms | 163 µs |
| 1 KiB | 123 µs | 948 µs / 1.1 ms | 200 µs | 129 µs | 968 µs / 1.2 ms | 206 µs |
| 4 KiB | 131 µs | 989 µs / 1.4 ms | 287 µs | 143 µs | 1.0 ms / 1.5 ms | 294 µs |
| 32 KiB | 331 µs | 2.4 ms / 3.7 ms | 938 µs | 311 µs | 2.3 ms / 3.2 ms | 944 µs |
| 256 KiB | 2.2 ms | 17 ms / 20 ms | 5.8 ms | 2.4 ms | 18 ms / 23 ms | 6.0 ms |
| 1 MiB | 14 ms | 103 ms / 135 ms | 27 ms | 14 ms | 105 ms / 144 ms | 28 ms |
| 4 MiB | 160 ms | 471 ms / 1912 ms | 120 ms | 138 ms | 390 ms / 972 ms | 132 ms |

The parts of a step in host mode, one run at a time. The engine's part
outside the harness call is from the trace. The other parts are measured
on their own, and the remainder is the step less all of them: the harness
runtime's own work for an invocation. Above 256 KiB, the isolated
`args.json` writes and transport exceed the step's own share, so the
parts overlap and no remainder is given.

| Payload | Step | Engine, outside the harness call | Invoke encode and result decode | `args.json` | Transport | Harness parse and serialise | Remainder |
|---|---|---|---|---|---|---|---|
| 26 B | 145 µs | 6.6 µs | 2.9 µs | 112 µs | 12.6 µs | 2.4 µs | 8.8 µs |
| 1 KiB | 178 µs | 23.3 µs | 8.4 µs | 113 µs | 13.8 µs | 8.4 µs | 11.5 µs |
| 4 KiB | 266 µs | 63.2 µs | 25.4 µs | 116 µs | 16.9 µs | 26.8 µs | 17 µs |
| 32 KiB | 980 µs | 412 µs | 162 µs | 134 µs | 61.6 µs | 186 µs | 25.1 µs |
| 256 KiB | 6.5 ms | 3.1 ms | 1.3 ms | 228 µs | 454 µs | 1.5 ms | – |
| 1 MiB | 31 ms | 13 ms | 4.9 ms | 4.2 ms | 6.2 ms | 5.8 ms | – |
| 4 MiB | 124 ms | 51 ms | 20 ms | 21 ms | 25 ms | 23 ms | – |

Transport is the round trip of an invoke and its result between the
engine's connection and the runtime's frame layer, less the result's
decode, which the third column counts. The shapes a host-mode embedder
runs, in host mode, one run at a time unless noted:

| Steps | Results | Logs, reads, expression | Elapsed p50 / p95 | Engine CPU p50 / p95 | Host CPU p50 / p95 | Expression |
|---|---|---|---|---|---|---|
| 600 | 1 KiB | none | 191 µs / 229 µs | 206 µs / 223 µs | 27.6 µs / 38.6 µs | 0.7 µs |
| 50 | 1 KiB | all | 313 µs / 421 µs | 366 µs / 714 µs | 63.2 µs / 153 µs | 44.2 µs |
| 50 | 50 KiB | all | 2.1 ms / 2.5 ms | 2.0 ms / 2.4 ms | 459 µs / 617 µs | 537 µs |
| 200 | 1 to 50 KiB | all | 947 µs / 1.3 ms | 775 µs / 898 µs | 213 µs / 252 µs | 50.5 µs |
| 600 | 1 KiB | all | 304 µs / 384 µs | 413 µs / 425 µs | 65.7 µs / 86.8 µs | 44.5 µs |
| 600 | 10 KiB | all | 665 µs / 773 µs | 710 µs / 764 µs | 143 µs / 171 µs | 134 µs |
| 600 | 50 KiB | all | 2.1 ms / 2.4 ms | 2.0 ms / 2.1 ms | 453 µs / 476 µs | 549 µs |
| 600 | 1 to 50 KiB | all | 947 µs / 1.3 ms | 789 µs / 841 µs | 207 µs / 234 µs | 48.4 µs |
| 200, eight at once | 1 to 50 KiB | all | 1.8 ms / 3.8 ms | 883 µs (mean) | 243 µs (mean) | 76.5 µs |

In the shapes, a step writes up to 3 log lines and makes up to 2 variable
or resource reads through the platform facade. Every step evaluates an
expression that binds the flow input, which holds a seed of the first
step's result size. CPU is per step at p50 and p95 over 20 runs, and the
expression column is the mean expression time per step.

What the baseline shows, without changing anything:

- A step's fixed cost is about 145 µs on both topologies, and writing
  `args.json` takes 112 µs of it. At 26 B, the engine's CPU per step is
  155 µs, of which 120 µs is system time.
- The engine spends more CPU per step than the host at every size: 155 µs
  against 17 µs at 26 B, and 98 ms against 43 ms at 4 MiB.
- Eight runs at once gain little below 32 KiB: 122 µs of wall time per
  step at 26 B, against 152 µs one at a time. Each process writes the
  `args.json` of one invocation at a time, under a lock.
- From 32 KiB, the engine's part outside the harness call is 40 to 50 % of
  a step. An expression that binds a 50 KiB flow input costs about 540 µs
  per step.
- The spawned harness has slow outliers from 256 KiB (p95 49 ms against
  7.2 ms in host mode) that host mode does not have.

## Decision

### Two texts per value, held in Go

The engine's value type (`jsonval`) holds:

- the **producer text**, byte for byte as the harness or QuickJS-ng wrote it;
- the **stored text**, the jsonb-canonical form, computed lazily the first
  time a storage route reads the value;
- a NUL flag, computed eagerly, because every result is stored in Windmill
  and a NUL fails the step there ([OR-27](../semantics/oracle.md#or-27)).

Which form each consumer gets:

| Consumer | Form | Findings |
|---|---|---|
| `previous_result` after an ordinary step whose job completed in the same transition; `stop_after_if`'s `result`; `results.<preceding id>` in its four exact spellings | producer | FX-42 item 3, JE-8 |
| an engine read of the preceding id when `previous_result` is bound | producer, under the default reading | FX-V6, [0001](0001-equivalence-contract.md) §6 |
| simple `results` paths (the direct lookup) | stored, the path applied, answered in jsonb's printed form (a space after each `:` and `,`) | JE-9, JE-44 |
| other ids read through the engine | stored | FX-42, JE-18 |
| `previous_result` after a loop or `branchall` | re-printed: the children's stored results decoded and the array written out again compactly, object keys in jsonb order, an integer that fits 64 bits exact and any other number the nearest double, in the backend JSON library's notation (FX-42 rule 2: a stored `1.50` reads `1.5`) | FX-6, JE-44 |
| `previous_result` at the first module of a flow or body and in the preprocessor; `flow_input` in shortcuts and in the engine | the flow arguments' text: a compact object, top-level order shuffled per run, each member in jsonb's printed form | JE-7, JE-37, JE-44, FX-42 item 1, OR-23 |
| a re-derived `previous_result` when the predecessor succeeded (the re-fetch fallback, after a suspend or a restart from a step, a retry's predicates) | that job's stored result in jsonb's printed form | FX-6, JE-36, JE-44 |
| a re-derived `previous_result` when the predecessor did not succeed; the result of an empty chosen `branchone` | the constant two-character JSON string `"{}"` | FX-6, OR-25 |
| `flow_env` values (`later`) | resolved as [JE-38](../semantics/expressions.md#je-38) says, top-level order as stored | JE-7, JE-38 |
| nested argument values a script receives | stored, then resolution | WR-10, WR-V8 |
| the final flow result returned to the host | stored | FX-36, FX-42 item 5 |

A value a shortcut answers ([JE-6](../semantics/expressions.md#je-6) to
[JE-9](../semantics/expressions.md#je-9)) keeps its form as text, since the
shortcut does not parse it; every other consumer parses it, where the
printed and re-printed forms read as the stored one but for key order and
the precision of numbers that are not 64-bit integers.

### The argument pipeline

1. Each transform yields JSON text: a static value's raw text, a
   shortcut's verbatim text, or QuickJS-ng's `JSON.stringify(x ?? null)`
   output ([JE-15](../semantics/expressions.md#je-15)).
2. The engine assembles the argument object with its top-level keys in an
   order shuffled by the run's seed ([0001](0001-equivalence-contract.md) §2).
3. **Storage boundary:** each argument becomes its stored text. A NUL here
   is a push failure, which is a chaining error routed as
   [FX-15](../semantics/execution.md#fx-15) says
   ([FX-48](../semantics/execution.md#fx-48) case 2): a module failure named
   `Anyhow` at root pickup, a failed child at child pickup, or `InternalError`
   at a later transition. That routing is source-derived at medium
   confidence; the exact message text
   is open ([Q-FX-8](../semantics/execution.md#q-fx-8)) and is pinned by a
   recording.
4. **Resolution:** only arguments whose stored text contains `$var:`,
   `$jsonvar:`, `$res:` or `$encrypted:` are walked, with the depth and
   array rules of [WA-12](../semantics/client-api.md#wa-12) and
   [WA-V3](../semantics/client-api.md#wa-v3). Substituted values enter in
   their source order and are not normalised again; `$jsonvar:` follows the
   default reading of [0001](0001-equivalence-contract.md) §6.
5. The **recorded** arguments (after step 3) and the **resolved** arguments
   (after step 4) are both kept in the run trace: they are distinct
   observation points ([JE-V3](../semantics/expressions.md#je-v3)).
6. The harness parses the text once, with no reviver, as the wrapper does
   ([WR-9](../semantics/languages.md#wr-9)). JavaScript then enumerates
   integer-like keys first, as it would on Windmill.

### The canonicaliser

`jsonval` turns producer text into stored text by streaming over the bytes,
without building a tree of Go values:

- object keys ordered by UTF-8 byte length, then bytewise; the last of
  duplicate keys kept;
- numbers in PostgreSQL `numeric` output form: exponents expanded
  (`1e+21` → `1000000000000000000000`, `1.5e3` → `1500`), `-0` → `0`,
  other digits kept; the scale of an exponent number such as `1.50e3` is
  pinned by an oracle-checked fixture ([Q-FX-7](../semantics/execution.md#q-fx-7));
  a number outside `numeric`'s range rejected, as PostgreSQL rejects it;
- strings re-escaped to `jsonb`'s own output escaping; `\u0000` in a string
  or key rejected, and so is a surrogate escape without its pair, as
  PostgreSQL rejects both;
- whitespace removed. jsonb's own text output puts spaces after `:` and `,`;
  no consumer can observe that after parsing, so the compact form is used and
  comparisons are on parsed values. The expression shortcuts
  ([JE-6](../semantics/expressions.md#je-6) to
  [JE-9](../semantics/expressions.md#je-9)) answer texts unparsed, and a
  site can quote their answer (Consequences).

It is checked three ways: catalogue cases ([FX-42](../semantics/execution.md#fx-42)
a–h, [JE-44](../semantics/expressions.md#je-44), [WR-10](../semantics/languages.md#wr-10)),
an independent Go implementation used only in tests (decode, sort, re-encode),
and PostgreSQL itself: a CI job casts every canonicaliser fixture to `jsonb`
and back in the oracle's PostgreSQL image, pinned by the same digest
([Q-JE-9](../semantics/expressions.md#q-je-9)).

### Results and errors in the harness

The harness serialises each result once with the boundary mode's rules
(Bun: replacer plus `BigInt.prototype.toJSON`; native: no replacer, in the
invocation's realm, [WR-42](../semantics/languages.md#wr-42), D5) and errors
with [WR-18](../semantics/languages.md#wr-18) (plain `JSON.stringify`, no
replacer), or for native with [WR-43](../semantics/languages.md#wr-43). The producer text is that string; Go does not reparse it except
where a route or the trace needs a field. A value that the serialiser
cannot turn into text (a top-level function or symbol) fails the step as an
`error` outcome with the WR-18 shape and name `TypeError`, which is what the
reference expects from the wrapper's result write
([WR-13](../semantics/languages.md#wr-13) case d). The message is pinned when
[Q-WR-12](../semantics/languages.md#q-wr-12) is recorded; until then that
case compares the name only.

**Host-plugin results** ([0008](0008-host-hooks.md)) that stand in for a
module's result are serialised the same way, with the module's boundary mode,
by the same function. A plugin result is therefore indistinguishable from a
step result by construction, not by a second implementation.

### No normalising walk in the first edition

The walk is dropped: every value crosses a frame as text anyway, the walk's
only remaining uses were host-plugin results (above) and a speculative
"value residency" optimisation, and it was not equivalent to the round trip
it was meant to replace. Keeping it would also have meant a second jsonb
canonicaliser in TypeScript beside `jsonval`, against the single-source rule.
If benchmark G1 shows that large values crossing the boundary dominate a
real workload, residency gets its own decision record, with a shared fixture
set run by both canonicalisers and an equivalence contract written for the
walk's result and errors.

### Benchmark plan (milestone G1)

1. Go: the canonicaliser versus decode, sort and encode, and against
   PostgreSQL in oracle CI for correctness only.
2. End to end: the per-step boundary cost (engine → harness → engine) for
   payloads from 26 B to 4 MiB, sequential and with 8 concurrent
   invocations, split into transport, harness parse and serialise, and Go
   handling.
3. The share of a run's time spent at boundaries, from the run spans of
   [0011](0011-differential-testing.md), on the conformance flows and on any
   host workload a host provides.

Results are recorded with the runtime versions, the hardware and the
machine's load (§Baseline). The benchmarks run locally, on demand, and
never in CI. A comparison runs both sides on one machine in one sitting,
and `benchstat` compares their outputs. A shared CI runner's timings vary
by more than the 20 % a regression gate would catch, and a scheduled job
that fails on that noise would need a person to look at it.

## Consequences

- Exactness comes from where the text is produced (the harness and
  QuickJS-ng) and from one canonicaliser, not from re-implementing jsonb in
  every language, and not from a JavaScript copy that only approximates a
  round trip.
- Go never decodes a value it only passes through.
- Key-order provenance that the oracle has not yet confirmed
  ([Q-WR-4](../semantics/languages.md#q-wr-4), [Q-OR-11](../semantics/oracle.md#q-or-11),
  [Q-OR-12](../semantics/oracle.md#q-or-12)) is encoded as cases; a recording
  that disagrees changes one routing table entry.
- Milestone A1 changed two points. The PostgreSQL check runs in the ordinary
  CI workflow rather than only in oracle CI, because it needs PostgreSQL but
  no Windmill. And the canonicaliser rejects two texts besides NUL that
  PostgreSQL's `jsonb` input rejects, an unpaired surrogate escape and a
  number outside `numeric`'s range. The reference covers only NUL, so
  whether Windmill fails a step on them as it does on NUL is request 4 of the
  [README](README.md); until then the eager flag covers NUL alone.
- Milestone B2a found that the spaces of jsonb's text output can show.
  The [JE-9](../semantics/expressions.md#je-9) shortcut answers the
  database's text unparsed, and the `timeout` site's parse error quotes it:
  recording batch E3a recorded `Value was: {"a": 2, "b": 1}` for a
  `results.p` read through the database, against `Value was: {"b":1,"a":2}`
  for the same read of the previous step from memory
  (`conformance/flows/je/shortcuts`, runs `je8-order-timeout-db` and
  `je8-order-timeout`). The expression pipeline therefore applies the path
  to the stored text and answers in jsonb's text output, a space after each
  `:` and `,` outside strings; the stored text stays compact. The other
  shortcuts answer the texts they are given as they are: JE-6 and JE-8
  `previous_result`, JE-7 a member of the flow input. Some of those values
  are read from the database too
  ([JE-44](../semantics/expressions.md#je-44)): the flow input's members,
  and `previous_result` after a loop or `branchall` or a re-fetch. Whether
  Windmill's text there is jsonb's spaced output was request 8 of the
  [README](README.md).
- The semantics team answered request 8 from the source, and milestone FU3
  changed the routing table to the answer: a flow input's member and a
  `previous_result` read back from storage are in jsonb's printed form, the
  flow input's top level and the first module's `previous_result` are
  compact, and `previous_result` after a loop or `branchall` is compact with
  its numbers re-printed. `expr.Scope` gives the engine these texts, which
  the shortcuts answer verbatim. They are not recorded yet
  ([Q-JE-13](../semantics/expressions.md#q-je-13)). The re-printed form
  needs the backend library's number notation, which FX-42 gives by
  example (`1000.0`, `1e+21`, `1.2345678901234568e+29`); where it changes
  from decimal to exponent notation was asked of the semantics team
  (request 10), and FX-42 rule 9 now gives it: plain decimal for a
  leading-digit exponent from −5 to 15, the shortest digits, `-0` as
  `-0.0`. Since milestone FU6 the engine writes a loop's result with the
  platform's writer of that rule (`platform.Reprint`), the one the
  argument resolution uses.

- The benchmark plan is in the repository, with its baseline in
  §Measurements. The benchmarks are Go benchmarks beside the
  code they measure: `internal/jsonval`, `internal/harness` with a Bun
  script that imports the runtime's frame codec, and `cmd/windmill-lite`
  for whole runs. `windmill-lite run` and `host` take `-cpuprofile <file>`
  for the engine.
- Putting them there changed three points of the plan. They run locally, not
  nightly in CI, for the reason in §Benchmark plan. Item 3's share of a
  run spent at boundaries is the harness span against the module span, on
  flows shaped like a host-mode embedder's. The conformance flows have too
  few steps to measure more than start-up.
- The third point is how item 2 splits a step. The trace declares queue,
  serialise and platform spans that the engine does not fill, because the
  step executor does not report its timings. So the parts inside the
  harness call are measured on their own, with the same payloads, and the
  engine's own part comes from the module and harness spans.

## Spec IDs relied on

FX-6, FX-10, FX-36, FX-42, FX-48, FX-V5, FX-V6, CF-20, JE-7 to JE-9, JE-15,
JE-16, JE-18, JE-36, JE-37, JE-44, JE-V3, WR-9, WR-10, WR-13, WR-14, WR-18, WR-V8, Q-WR-12,
WA-12, WA-V3, OR-13, OR-23, OR-24, OR-25, OR-27, Q-FX-7, Q-FX-8, Q-JE-9, Q-JE-13, Q-WR-4,
Q-OR-11, Q-OR-12.

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
of three runs (`-count 3`) of its benchmark, taken while no other build or
benchmark ran.

A run that overlapped another build or benchmark on the machine was
repeated, and the boundary benchmarks ran in four parts so that each could
be repeated alone. The one-minute load average was 2 to 6.5, from the
machine's own services and the benchmarks themselves. The commands, from
the repository root, with `WL_BUN` naming Bun 1.3.10:

```sh
go test ./internal/jsonval -run '^$' -bench BenchmarkStored -benchtime 1s -count 3
go test ./internal/harness -run '^$' -bench 'GoInvocation|ArgsFile|HarnessCodec|Transport' -benchtime 1s -count 3
go test ./cmd/windmill-lite -run '^$' -bench BenchmarkStepBoundary -benchtime 3s -count 3
go test ./cmd/windmill-lite -run '^$' -bench BenchmarkHostShapes -benchtime 20x -count 3
```

Percentiles are nearest-rank
([internal/bench](../../internal/bench/README.md#percentiles)). The
payloads are compact JSON objects of exactly their size, shaped like step
results. The canonicaliser against the test implementation's decode, sort
and encode, per call:

| Payload | Canonicaliser | Decode, sort and encode |
|---|---|---|
| 26 B | 0.1 µs (247 MB/s) | 1.8 µs (14 MB/s) |
| 1 KiB | 3.2 µs (322 MB/s) | 50 µs (21 MB/s) |
| 4 KiB | 12 µs (339 MB/s) | 255 µs (16 MB/s) |
| 32 KiB | 93 µs (353 MB/s) | 3.0 ms (11 MB/s) |
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
| 26 B | 147 µs / 173 µs | 157 µs | 19 µs | 151 µs / 180 µs | 165 µs | 19 µs |
| 1 KiB | 181 µs / 213 µs | 202 µs | 26 µs | 186 µs / 215 µs | 206 µs | 26 µs |
| 4 KiB | 270 µs / 305 µs | 283 µs | 50 µs | 276 µs / 310 µs | 292 µs | 49 µs |
| 32 KiB | 986 µs / 1.1 ms | 889 µs | 263 µs | 999 µs / 1.1 ms | 892 µs | 262 µs |
| 256 KiB | 6.6 ms / 7.1 ms | 5.4 ms | 1.9 ms | 6.7 ms / 7.3 ms | 5.6 ms | 1.9 ms |
| 1 MiB | 31 ms / 33 ms | 24 ms | 10.0 ms | 31 ms / 34 ms | 25 ms | 9.5 ms |
| 4 MiB | 126 ms / 139 ms | 98 ms | 43 ms | 124 ms / 128 ms | 99 ms | 40 ms |

With eight runs at once, started together, the time per step is the wall
time over the steps of all eight. Every row had eight runs in flight at
its peak, and 7.8 to 8 on average:

| Payload | Host mode: time per step | Elapsed p50 / p95 | Engine CPU | Host CPU | Spawned harness: time per step | Elapsed p50 / p95 | Engine CPU | Harness CPU |
|---|---|---|---|---|---|---|---|---|
| 26 B | 121 µs | 927 µs / 1.1 ms | 164 µs | 19 µs | 123 µs | 942 µs / 1.1 ms | 166 µs | 18 µs |
| 1 KiB | 126 µs | 956 µs / 1.1 ms | 208 µs | 26 µs | 130 µs | 977 µs / 1.2 ms | 209 µs | 26 µs |
| 4 KiB | 134 µs | 999 µs / 1.4 ms | 294 µs | 49 µs | 152 µs | 1.0 ms / 1.6 ms | 297 µs | 49 µs |
| 32 KiB | 330 µs | 2.5 ms / 3.3 ms | 972 µs | 289 µs | 468 µs | 2.4 ms / 11 ms | 978 µs | 275 µs |
| 256 KiB | 2.2 ms | 17 ms / 19 ms | 5.8 ms | 2.0 ms | 2.5 ms | 19 ms / 24 ms | 6.2 ms | 2.1 ms |
| 1 MiB | 14 ms | 108 ms / 145 ms | 27 ms | 11 ms | 16 ms | 122 ms / 188 ms | 30 ms | 10 ms |
| 4 MiB | 78 ms | 570 ms / 803 ms | 122 ms | 47 ms | 105 ms | 657 ms / 1252 ms | 123 ms | 42 ms |

#### Where a step's time goes

Each step is split into stages, each the difference of two readings of
that step's own invocation: the trace's module and harness spans, and the
clocks of both processes ([cmd/windmill-lite](../../cmd/windmill-lite/README.md#stages)).
A step's stages add up to its module span, so their means add up to its
mean. Taking the readings moves a step's p50 by under 2 %.

The tables group the stages:

- **Go handling**: the engine, its executor, the invoke's encoding,
  `args.json`, queueing, the result's decoding and its hand-off.
- **Transport**: from the invoke queued to the result read, less the
  harness's time: the writer, the socket both ways and the wake-ups, not
  attributed further. With eight runs at once it includes the wait behind
  the other invocations' frames.
- **Harness parse and serialise**: the invoke decoded, the result
  serialised, and its frame encoded and handed to the socket.
- **Harness dispatch**: the runtime's work from the decoded invoke to the
  entrypoint's call.
- **Boundary share**: per run, its harness spans less its steps' own code,
  over its module spans, at p50 over the runs.

Host mode, one run at a time, means:

| Payload | Step | Go handling | Transport | Harness parse and serialise | Harness dispatch | Step's code | Boundary share p50 |
|---|---|---|---|---|---|---|---|
| 26 B | 152 µs | 130 µs | 12 µs | 7.4 µs | 2.2 µs | 0.1 µs | 95 % |
| 1 KiB | 190 µs | 159 µs | 13 µs | 15 µs | 2.2 µs | 0.1 µs | 87 % |
| 4 KiB | 278 µs | 224 µs | 16 µs | 36 µs | 2.2 µs | 0.1 µs | 76 % |
| 32 KiB | 1.0 ms | 739 µs | 76 µs | 200 µs | 2.8 µs | 0.2 µs | 58 % |
| 256 KiB | 6.6 ms | 4.8 ms | 400 µs | 1.5 ms | 7.0 µs | 0.5 µs | 52 % |
| 1 MiB | 31 ms | 19 ms | 5.7 ms | 6.4 ms | 24 µs | 1.9 µs | 59 % |
| 4 MiB | 128 ms | 76 ms | 27 ms | 25 ms | 25 µs | 2.6 µs | 59 % |

Host mode, eight runs at once, means:

| Payload | Step | Go handling | Transport | Harness parse and serialise | Harness dispatch | Step's code | Boundary share p50 |
|---|---|---|---|---|---|---|---|
| 26 B | 966 µs | 938 µs | 18 µs | 7.9 µs | 2.5 µs | 0.2 µs | 99 % |
| 1 KiB | 1.0 ms | 961 µs | 22 µs | 16 µs | 2.5 µs | 0.3 µs | 98 % |
| 4 KiB | 1.1 ms | 997 µs | 32 µs | 37 µs | 2.4 µs | 0.3 µs | 94 % |
| 32 KiB | 2.6 ms | 848 µs | 1.5 ms | 226 µs | 3.1 µs | 0.2 µs | 82 % |
| 256 KiB | 17 ms | 4.9 ms | 11 ms | 1.6 ms | 5.2 µs | 0.3 µs | 81 % |
| 1 MiB | 111 ms | 22 ms | 82 ms | 6.6 ms | 22 µs | 1.5 µs | 87 % |
| 4 MiB | 569 ms | 105 ms | 437 ms | 28 ms | 32 µs | 2.6 µs | 88 % |

The spawned harness, one run at a time, means:

| Payload | Step | Go handling | Transport | Harness parse and serialise | Harness dispatch | Step's code | Boundary share p50 |
|---|---|---|---|---|---|---|---|
| 26 B | 160 µs | 139 µs | 11 µs | 7.2 µs | 2.2 µs | 0.1 µs | 95 % |
| 1 KiB | 194 µs | 164 µs | 13 µs | 15 µs | 2.1 µs | 0.1 µs | 87 % |
| 4 KiB | 286 µs | 232 µs | 16 µs | 36 µs | 2.1 µs | 0.1 µs | 76 % |
| 32 KiB | 1.0 ms | 747 µs | 76 µs | 202 µs | 2.6 µs | 0.2 µs | 58 % |
| 256 KiB | 6.8 ms | 4.8 ms | 475 µs | 1.5 ms | 7.2 µs | 0.6 µs | 52 % |
| 1 MiB | 32 ms | 20 ms | 5.8 ms | 6.5 ms | 27 µs | 1.8 µs | 59 % |
| 4 MiB | 125 ms | 75 ms | 25 ms | 25 ms | 25 µs | 2.4 µs | 58 % |

The spawned harness, eight runs at once, means:

| Payload | Step | Go handling | Transport | Harness parse and serialise | Harness dispatch | Step's code | Boundary share p50 |
|---|---|---|---|---|---|---|---|
| 26 B | 982 µs | 952 µs | 20 µs | 7.7 µs | 2.3 µs | 0.2 µs | 99 % |
| 1 KiB | 1.0 ms | 996 µs | 21 µs | 16 µs | 2.5 µs | 0.3 µs | 98 % |
| 4 KiB | 1.2 ms | 1.1 ms | 41 µs | 37 µs | 2.6 µs | 0.5 µs | 94 % |
| 32 KiB | 3.8 ms | 2.1 ms | 1.4 ms | 226 µs | 2.9 µs | 0.2 µs | 83 % |
| 256 KiB | 20 ms | 5.6 ms | 12 ms | 1.7 ms | 6.1 µs | 0.5 µs | 82 % |
| 1 MiB | 121 ms | 25 ms | 89 ms | 6.8 ms | 25 µs | 1.8 µs | 87 % |
| 4 MiB | 806 ms | 284 ms | 494 ms | 28 ms | 30 µs | 3.0 µs | 90 % |

Every stage in host mode, one run at a time, as its mean and, in
brackets, its p50:

| Stage | 26 B | 1 KiB | 4 KiB | 32 KiB | 256 KiB | 1 MiB | 4 MiB |
|---|---|---|---|---|---|---|---|
| `engine` | 7.2 µs (6.8 µs) | 24 µs (23 µs) | 64 µs (63 µs) | 416 µs (411 µs) | 3.2 ms (3.1 ms) | 13 ms (12 ms) | 52 ms (51 ms) |
| `executor` | 1.2 µs (1.1 µs) | 1.4 µs (1.2 µs) | 1.4 µs (1.2 µs) | 1.7 µs (1.5 µs) | 2.9 µs (2.4 µs) | 6.9 µs (6.1 µs) | 11 µs (11 µs) |
| `go-encode` | 3.5 µs (3.2 µs) | 8.0 µs (7.6 µs) | 17 µs (16 µs) | 104 µs (103 µs) | 789 µs (779 µs) | 3.1 ms (3.1 ms) | 13 ms (13 ms) |
| `args-file` | 113 µs (110 µs) | 117 µs (112 µs) | 125 µs (121 µs) | 143 µs (132 µs) | 240 µs (209 µs) | 518 µs (459 µs) | 1.5 ms (1.3 ms) |
| `go-queue` | 1.4 µs (1.2 µs) | 1.5 µs (1.4 µs) | 1.7 µs (1.5 µs) | 3.9 µs (3.1 µs) | 18 µs (17 µs) | 4.3 µs (3.8 µs) | 8.1 µs (7.8 µs) |
| `transport` | 12 µs (10 µs) | 13 µs (11 µs) | 16 µs (12 µs) | 76 µs (61 µs) | 400 µs (342 µs) | 5.7 ms (5.6 ms) | 27 ms (26 ms) |
| `bun-decode` | 3.5 µs (3.0 µs) | 6.6 µs (6.0 µs) | 15 µs (15 µs) | 84 µs (82 µs) | 618 µs (613 µs) | 2.6 ms (2.6 ms) | 10 ms (10 ms) |
| `bun-dispatch` | 2.2 µs (1.9 µs) | 2.2 µs (1.9 µs) | 2.2 µs (1.9 µs) | 2.8 µs (2.1 µs) | 7.0 µs (5.5 µs) | 24 µs (24 µs) | 25 µs (24 µs) |
| `code` | 0.1 µs (0.1 µs) | 0.1 µs (0.1 µs) | 0.1 µs (0.1 µs) | 0.2 µs (0.1 µs) | 0.5 µs (0.4 µs) | 1.9 µs (1.9 µs) | 2.6 µs (2.6 µs) |
| `bun-serialise` | 0.5 µs (0.5 µs) | 4.9 µs (4.5 µs) | 17 µs (16 µs) | 109 µs (109 µs) | 838 µs (836 µs) | 3.5 ms (3.4 ms) | 14 ms (14 ms) |
| `bun-send` | 3.3 µs (3.0 µs) | 3.7 µs (3.3 µs) | 4.1 µs (3.6 µs) | 7.6 µs (6.7 µs) | 32 µs (29 µs) | 358 µs (355 µs) | 803 µs (684 µs) |
| `go-decode` | 2.1 µs (1.9 µs) | 4.6 µs (4.5 µs) | 12 µs (12 µs) | 67 µs (67 µs) | 500 µs (499 µs) | 2.8 ms (2.8 ms) | 8.9 ms (8.9 ms) |
| `go-handoff` | 1.6 µs (1.4 µs) | 2.4 µs (2.3 µs) | 2.6 µs (2.5 µs) | 2.8 µs (2.6 µs) | 4.3 µs (3.8 µs) | 10 µs (9.8 µs) | 17 µs (16 µs) |
| the step's module span | 152 µs (147 µs) | 189 µs (181 µs) | 279 µs (270 µs) | 1.0 ms (986 µs) | 6.6 ms (6.6 ms) | 31 ms (31 ms) | 128 ms (126 ms) |

#### Shapes

The shapes a host-mode embedder runs, in host mode, one run at a time
unless noted. A step writes up to 3 log lines and makes up to 2 variable
or resource reads through the platform facade. Every step evaluates an
expression that binds the flow input, which holds a seed of the first
step's result size.

CPU is per step, at p50 and p95 over 20 runs. The boundary share is as
above, and the expression column is the mean expression time per step.

| Steps | Results | Logs, reads, expression | Elapsed p50 / p95 | Engine CPU p50 / p95 | Host CPU p50 / p95 | Boundary share p50 | Expression |
|---|---|---|---|---|---|---|---|
| 600 | 1 KiB | none | 199 µs / 253 µs | 215 µs / 243 µs | 30 µs / 49 µs | 85 % | 0.8 µs |
| 50 | 1 KiB | all | 315 µs / 413 µs | 366 µs / 711 µs | 60 µs / 141 µs | 56 % | 44 µs |
| 50 | 50 KiB | all | 2.2 ms / 2.6 ms | 2.0 ms / 2.4 ms | 470 µs / 534 µs | 38 % | 569 µs |
| 200 | 1 to 50 KiB | all | 963 µs / 1.3 ms | 778 µs / 911 µs | 221 µs / 245 µs | 52 % | 50 µs |
| 600 | 1 KiB | all | 311 µs / 396 µs | 427 µs / 438 µs | 67 µs / 78 µs | 57 % | 45 µs |
| 600 | 10 KiB | all | 682 µs / 810 µs | 729 µs / 763 µs | 148 µs / 158 µs | 47 % | 142 µs |
| 600 | 50 KiB | all | 2.2 ms / 2.7 ms | 2.0 ms / 2.1 ms | 465 µs / 498 µs | 38 % | 579 µs |
| 600 | 1 to 50 KiB | all | 948 µs / 1.3 ms | 793 µs / 827 µs | 213 µs / 222 µs | 51 % | 51 µs |
| 200, eight at once | 1 to 50 KiB | all | 2.1 ms / 4.2 ms | 930 µs (mean) | 256 µs (mean) | 56 % | 99 µs |

#### What the baseline shows

These are facts of the measurements; nothing was changed to obtain them.

- At 26 B a step takes 152 µs on average in host mode. Go handling takes
  130 µs of it, and writing `args.json` alone 113 µs. The engine's CPU per
  step is 157 µs, against 19 µs for the host.
- Go handling is the largest part of a step at every size, one run at a
  time. At 4 MiB it takes 76 ms of 128 ms, against 25 ms for the
  harness's parse and serialise and 27 ms for the transport.
- With eight runs at once, Go handling dominates below 32 KiB, and the
  transport from 32 KiB. Each process writes the `args.json` of one
  invocation at a time, under a lock.
- The boundary takes 95 % of a run's module time at 26 B, and 52 to 59 %
  from 256 KiB, where the engine's own part grows. In the embedder-shaped
  flows it takes 38 to 57 %, and 85 % with no logs, reads or expressions.
- An expression that binds a 50 KiB flow input costs about 570 µs per
  step, against 44 µs for a 1 KiB input.

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
  §Measurements. The benchmarks are Go benchmarks beside the code they
  measure: `internal/jsonval`, `internal/harness` with a Bun script that
  imports the runtime's frame codec, and `cmd/windmill-lite` for whole
  runs, sharing `internal/bench`. `windmill-lite run` and `host` take
  `-cpuprofile <file>` for the engine.
- Putting them there changed three points of the plan. They run locally, not
  nightly in CI, for the reason in §Benchmark plan. Item 3's share of a
  run spent at boundaries is the harness span against the module span, on
  flows shaped like a host-mode embedder's. The conformance flows have too
  few steps to measure more than start-up.
- The third point is how item 2 splits a step. The trace declares queue,
  serialise and platform spans that the engine does not fill, because the
  step executor reports no timings to the engine. The split comes from
  readings of each invocation's clock on both sides instead, which
  `harness.SetTimesHook` and the runtime's benchmark timings take only
  while a benchmark turns them on.

## Spec IDs relied on

FX-6, FX-10, FX-36, FX-42, FX-48, FX-V5, FX-V6, CF-20, JE-7 to JE-9, JE-15,
JE-16, JE-18, JE-36, JE-37, JE-44, JE-V3, WR-9, WR-10, WR-13, WR-14, WR-18, WR-V8, Q-WR-12,
WA-12, WA-V3, OR-13, OR-23, OR-24, OR-25, OR-27, Q-FX-7, Q-FX-8, Q-JE-9, Q-JE-13, Q-WR-4,
Q-OR-11, Q-OR-12.

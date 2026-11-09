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
  rewrites exponent numbers and rejects `\u0000`, which Windmill removes
  from a result and from a job's arguments before it stores them
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

The benchmarks of the plan below (§Benchmark plan) are in the repository.
This record replaces their first one. It was re-taken on the code of the
per-step cost work in host mode, as merged, with each shape and each
boundary configuration in a test process of its own.

They ran on an Apple M4 (10 cores) with 16 GiB, macOS 26.5.1, Go 1.27.3
and Bun 1.3.10. Each figure is the median of three runs (`-count 3`) of
its benchmark, taken while no other build, benchmark or heavy indexing
ran.

**Why it was re-taken.** In one process, a benchmark runs on the heap the
benchmarks before it left. The engine used to keep every finished run
reachable, so that heap grew with every run and the Go collector ran
rarely. The first record's figures leaned on memory that a long-lived
engine under a memory budget cannot spend.

The engine now releases a finished run's values once the run has
settled, and the collector reclaims them. In a shared process the
engine's CPU then depends on which benchmarks ran first; in a process of
its own each figure is its own
([cmd/windmill-lite](../../cmd/windmill-lite/README.md#one-process-per-shape)).

A run that overlapped another build, benchmark or Spotlight indexing at
more than a third of a core was repeated: the first take of the
end-to-end parts overlapped heavy indexing and was discarded. The
one-minute load average was 1.4 to 4.6, from the machine's own services
and the benchmarks themselves.

The commands below produced the baseline, from the repository root. They
need the build helper's dependencies
([harnesses/bun/build](../../harnesses/bun/build/README.md#running)) and
`WL_BUN` exported, naming Bun 1.3.10; a benchmark fails without it.
`WL_BENCH_FRESH=1` runs each shape and configuration in a process of its
own, and `-timeout` covers them all. The last command measures what the
stage readings cost:

```sh
export WL_BUN=/path/to/bun-1.3.10/bun
(cd harnesses/bun/build && "$WL_BUN" install --frozen-lockfile)
go test ./internal/jsonval -run '^$' -bench BenchmarkStored -benchtime 1s -count 3
go test ./internal/harness -run '^$' -bench 'GoInvocation|ArgsFile|HarnessCodec|Transport' -benchtime 1s -count 3
export WL_BENCH_FRESH=1
go test ./cmd/windmill-lite -run '^$' -bench BenchmarkStepBoundary -benchtime 3s -count 3 -timeout 60m
go test ./cmd/windmill-lite -run '^$' -bench BenchmarkHostShapes -benchtime 20x -count 3 -timeout 60m
WL_BENCH_TIMINGS=off go test ./cmd/windmill-lite -run '^$' \
  -bench 'BenchmarkStepBoundary/topology=host/conc=1/size=(26B|32KiB|4MiB)$' -benchtime 3s -count 3
```

Percentiles are nearest-rank
([internal/bench](../../internal/bench/README.md#percentiles)). The
payloads are compact JSON objects of exactly their size, shaped like step
results. The canonicaliser against the test implementation's decode, sort
and encode, per call:

| Payload | Canonicaliser | Decode, sort and encode |
|---|---|---|
| 26 B | 0.1 µs (240 MB/s) | 1.7 µs (15 MB/s) |
| 1 KiB | 3.2 µs (318 MB/s) | 50 µs (20 MB/s) |
| 4 KiB | 12 µs (335 MB/s) | 262 µs (16 MB/s) |
| 32 KiB | 95 µs (345 MB/s) | 3.0 ms (11 MB/s) |
| 256 KiB | 746 µs (351 MB/s) | 24 ms (11 MB/s) |
| 1 MiB | 2.9 ms (356 MB/s) | 97 ms (11 MB/s) |
| 4 MiB | 12 ms (361 MB/s) | 380 ms (11 MB/s) |

The step boundary end to end: a flow of eight steps that each return
their argument, the payload, through shortcuts that run no expression.
Elapsed is a step's module span in the trace. CPU is per step: the
engine's from `getrusage`, the host's or harness's from
`process.cpuUsage()`.

| Payload | Host mode: elapsed p50 / p95 | Engine CPU | Host CPU | Spawned harness: elapsed p50 / p95 | Engine CPU | Harness CPU |
|---|---|---|---|---|---|---|
| 26 B | 142 µs / 174 µs | 152 µs | 25 µs | 149 µs / 173 µs | 158 µs | 23 µs |
| 1 KiB | 177 µs / 215 µs | 203 µs | 34 µs | 181 µs / 212 µs | 207 µs | 32 µs |
| 4 KiB | 272 µs / 323 µs | 324 µs | 61 µs | 277 µs / 325 µs | 337 µs | 58 µs |
| 32 KiB | 935 µs / 1.4 ms | 1.2 ms | 274 µs | 980 µs / 1.4 ms | 1.2 ms | 273 µs |
| 256 KiB | 6.6 ms / 7.2 ms | 7.3 ms | 2.0 ms | 6.8 ms / 7.3 ms | 7.6 ms | 1.9 ms |
| 1 MiB | 30 ms / 33 ms | 29 ms | 10 ms | 30 ms / 32 ms | 29 ms | 9.3 ms |
| 4 MiB | 121 ms / 130 ms | 101 ms | 43 ms | 119 ms / 129 ms | 103 ms | 39 ms |

With eight runs at once, started together, the time per step is the wall
time over the steps of all eight. Every row had eight runs in flight at
its peak, and 7.8 to 8 on average:

| Payload | Host mode: time per step | Elapsed p50 / p95 | Engine CPU | Host CPU | Spawned harness: time per step | Elapsed p50 / p95 | Engine CPU | Harness CPU |
|---|---|---|---|---|---|---|---|---|
| 26 B | 122 µs | 931 µs / 1.1 ms | 165 µs | 23 µs | 126 µs | 950 µs / 1.2 ms | 170 µs | 21 µs |
| 1 KiB | 127 µs | 959 µs / 1.2 ms | 214 µs | 32 µs | 130 µs | 981 µs / 1.2 ms | 216 µs | 30 µs |
| 4 KiB | 143 µs | 1.0 ms / 1.6 ms | 344 µs | 57 µs | 148 µs | 1.1 ms / 1.7 ms | 350 µs | 55 µs |
| 32 KiB | 359 µs | 2.6 ms / 4.0 ms | 1.4 ms | 308 µs | 345 µs | 2.5 ms / 3.8 ms | 1.4 ms | 298 µs |
| 256 KiB | 2.3 ms | 18 ms / 21 ms | 7.0 ms | 2.1 ms | 2.4 ms | 19 ms / 22 ms | 7.2 ms | 2.1 ms |
| 1 MiB | 13 ms | 102 ms / 126 ms | 27 ms | 11 ms | 13 ms | 101 ms / 125 ms | 28 ms | 9.6 ms |
| 4 MiB | 56 ms | 432 ms / 580 ms | 104 ms | 43 ms | 56 ms | 437 ms / 571 ms | 107 ms | 38 ms |

#### Where a step's time goes

Each step is split into stages, each the difference of two readings of
that step's own invocation: the trace's module and harness spans, and the
clocks of both processes ([cmd/windmill-lite](../../cmd/windmill-lite/README.md#stages)).
A step's stages add up to its module span, so their means add up to its
mean.

Turning the readings off made no step measurably faster where that was
measured, in host mode, one run at a time. With them off, in separate
runs, the p50 was 144 µs, 1.0 ms and 125 ms at 26 B, 32 KiB and 4 MiB;
with them on, 142 µs, 935 µs and 121 ms.

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
| 26 B | 150 µs | 129 µs | 11 µs | 7.4 µs | 2.5 µs | 0.1 µs | 95 % |
| 1 KiB | 187 µs | 158 µs | 12 µs | 15 µs | 2.6 µs | 0.1 µs | 87 % |
| 4 KiB | 285 µs | 230 µs | 15 µs | 38 µs | 2.9 µs | 0.1 µs | 76 % |
| 32 KiB | 991 µs | 714 µs | 78 µs | 195 µs | 3.0 µs | 0.1 µs | 58 % |
| 256 KiB | 6.6 ms | 4.7 ms | 430 µs | 1.5 ms | 10 µs | 0.5 µs | 51 % |
| 1 MiB | 31 ms | 19 ms | 5.6 ms | 6.3 ms | 36 µs | 1.8 µs | 59 % |
| 4 MiB | 123 ms | 73 ms | 25 ms | 25 ms | 46 µs | 2.0 µs | 60 % |

Host mode, eight runs at once, means:

| Payload | Step | Go handling | Transport | Harness parse and serialise | Harness dispatch | Step's code | Boundary share p50 |
|---|---|---|---|---|---|---|---|
| 26 B | 973 µs | 945 µs | 18 µs | 7.6 µs | 2.6 µs | 0.2 µs | 99 % |
| 1 KiB | 1.0 ms | 969 µs | 23 µs | 16 µs | 2.8 µs | 0.3 µs | 98 % |
| 4 KiB | 1.1 ms | 1.1 ms | 29 µs | 38 µs | 2.8 µs | 0.3 µs | 94 % |
| 32 KiB | 2.9 ms | 1.1 ms | 1.5 ms | 233 µs | 4.1 µs | 0.3 µs | 81 % |
| 256 KiB | 18 ms | 5.2 ms | 11 ms | 1.6 ms | 6.4 µs | 0.3 µs | 81 % |
| 1 MiB | 103 ms | 20 ms | 77 ms | 6.4 ms | 19 µs | 0.8 µs | 87 % |
| 4 MiB | 429 ms | 77 ms | 328 ms | 25 ms | 53 µs | 1.9 µs | 88 % |

The spawned harness, one run at a time, means:

| Payload | Step | Go handling | Transport | Harness parse and serialise | Harness dispatch | Step's code | Boundary share p50 |
|---|---|---|---|---|---|---|---|
| 26 B | 153 µs | 133 µs | 10 µs | 7.2 µs | 2.3 µs | 0.1 µs | 95 % |
| 1 KiB | 188 µs | 160 µs | 11 µs | 14 µs | 2.4 µs | 0.1 µs | 87 % |
| 4 KiB | 291 µs | 237 µs | 15 µs | 37 µs | 2.5 µs | 0.1 µs | 77 % |
| 32 KiB | 1.0 ms | 743 µs | 84 µs | 202 µs | 3.0 µs | 0.1 µs | 58 % |
| 256 KiB | 6.8 ms | 4.8 ms | 491 µs | 1.5 ms | 9.7 µs | 0.4 µs | 52 % |
| 1 MiB | 31 ms | 19 ms | 5.6 ms | 6.3 ms | 38 µs | 1.5 µs | 59 % |
| 4 MiB | 121 ms | 72 ms | 24 ms | 25 ms | 45 µs | 1.7 µs | 59 % |

The spawned harness, eight runs at once, means:

| Payload | Step | Go handling | Transport | Harness parse and serialise | Harness dispatch | Step's code | Boundary share p50 |
|---|---|---|---|---|---|---|---|
| 26 B | 994 µs | 965 µs | 19 µs | 7.4 µs | 2.5 µs | 0.2 µs | 99 % |
| 1 KiB | 1.0 ms | 992 µs | 21 µs | 16 µs | 2.6 µs | 0.3 µs | 98 % |
| 4 KiB | 1.2 ms | 1.1 ms | 36 µs | 38 µs | 2.7 µs | 0.4 µs | 94 % |
| 32 KiB | 2.7 ms | 939 µs | 1.5 ms | 235 µs | 3.9 µs | 0.3 µs | 81 % |
| 256 KiB | 19 ms | 5.1 ms | 12 ms | 1.6 ms | 6.8 µs | 0.3 µs | 82 % |
| 1 MiB | 102 ms | 20 ms | 76 ms | 6.4 ms | 19 µs | 0.8 µs | 87 % |
| 4 MiB | 426 ms | 77 ms | 324 ms | 25 ms | 53 µs | 1.9 µs | 87 % |

Every stage in host mode, one run at a time, as its mean and, in
brackets, its p50:

| Stage | 26 B | 1 KiB | 4 KiB | 32 KiB | 256 KiB | 1 MiB | 4 MiB |
|---|---|---|---|---|---|---|---|
| `engine` | 6.8 µs (6.3 µs) | 24 µs (23 µs) | 67 µs (64 µs) | 405 µs (386 µs) | 3.2 ms (3.1 ms) | 12 ms (12 ms) | 49 ms (49 ms) |
| `executor` | 1.2 µs (1.0 µs) | 1.3 µs (1.2 µs) | 1.4 µs (1.1 µs) | 1.6 µs (1.2 µs) | 2.9 µs (2.1 µs) | 5.3 µs (5.0 µs) | 8.4 µs (7.5 µs) |
| `go-encode` | 3.3 µs (3.0 µs) | 8.0 µs (7.5 µs) | 17 µs (16 µs) | 101 µs (95 µs) | 803 µs (758 µs) | 3.0 ms (2.9 ms) | 12 ms (12 ms) |
| `args-file` | 114 µs (108 µs) | 117 µs (111 µs) | 129 µs (123 µs) | 133 µs (122 µs) | 211 µs (198 µs) | 722 µs (423 µs) | 3.1 ms (2.0 ms) |
| `go-queue` | 0.5 µs (0.4 µs) | 0.5 µs (0.4 µs) | 0.8 µs (0.6 µs) | 2.4 µs (1.7 µs) | 14 µs (10 µs) | 3.0 µs (2.8 µs) | 4.9 µs (4.6 µs) |
| `transport` | 11 µs (9.1 µs) | 12 µs (9.5 µs) | 15 µs (11 µs) | 78 µs (62 µs) | 430 µs (347 µs) | 5.6 ms (5.5 ms) | 25 ms (24 ms) |
| `bun-decode` | 3.1 µs (2.6 µs) | 6.0 µs (5.4 µs) | 15 µs (14 µs) | 83 µs (81 µs) | 599 µs (593 µs) | 2.5 ms (2.5 ms) | 10 ms (10 ms) |
| `bun-dispatch` | 2.5 µs (2.2 µs) | 2.6 µs (2.2 µs) | 2.9 µs (2.3 µs) | 3.0 µs (2.5 µs) | 10 µs (8.6 µs) | 36 µs (35 µs) | 46 µs (45 µs) |
| `code` | 0.1 µs (0.1 µs) | 0.1 µs (0.1 µs) | 0.1 µs (0.1 µs) | 0.1 µs (0.1 µs) | 0.5 µs (0.3 µs) | 1.8 µs (1.3 µs) | 2.0 µs (1.6 µs) |
| `bun-serialise` | 0.6 µs (0.5 µs) | 4.8 µs (4.2 µs) | 18 µs (18 µs) | 104 µs (102 µs) | 813 µs (809 µs) | 3.4 ms (3.4 ms) | 14 ms (14 ms) |
| `bun-send` | 3.6 µs (3.3 µs) | 3.9 µs (3.5 µs) | 4.7 µs (4.2 µs) | 8.1 µs (7.2 µs) | 43 µs (36 µs) | 366 µs (364 µs) | 899 µs (804 µs) |
| `go-decode` | 2.1 µs (1.8 µs) | 4.6 µs (4.5 µs) | 12 µs (12 µs) | 68 µs (67 µs) | 486 µs (479 µs) | 2.7 ms (2.6 ms) | 8.6 ms (8.5 ms) |
| `go-handoff` | 1.4 µs (1.2 µs) | 2.2 µs (2.0 µs) | 2.3 µs (2.0 µs) | 2.4 µs (2.0 µs) | 4.1 µs (3.4 µs) | 7.7 µs (6.8 µs) | 10 µs (10 µs) |
| the step's module span | 150 µs (142 µs) | 188 µs (177 µs) | 285 µs (272 µs) | 987 µs (935 µs) | 6.6 ms (6.6 ms) | 31 ms (30 ms) | 123 ms (121 ms) |

#### Shapes

The shapes a host-mode embedder runs, in host mode, one run at a time
unless noted. A step writes up to 3 log lines and makes up to 2 variable
or resource reads through the platform facade. Every step evaluates an
expression that binds the flow input, which holds a seed of the first
step's result size.

The last four rows are the shapes added with the per-step cost work
([cmd/windmill-lite](../../cmd/windmill-lite/README.md#benchmarks)). The
plain shape's bundle has no reader of `args.json`, so its host leaves the
file out; that host reports no stages.

CPU is per step, at p50 and p95 over 20 runs. The boundary share is as
above, and the expression column is the mean expression time per step.

| Steps | Results | Logs, reads, expression | Elapsed p50 / p95 | Engine CPU p50 / p95 | Host CPU p50 / p95 | Boundary share p50 | Expression |
|---|---|---|---|---|---|---|---|
| 600 | 1 KiB | none | 194 µs / 228 µs | 219 µs / 241 µs | 34 µs / 50 µs | 83 % | 0.8 µs |
| 50 | 1 KiB | all | 321 µs / 430 µs | 446 µs / 478 µs | 93 µs / 221 µs | 56 % | 44 µs |
| 50 | 50 KiB | all | 2.1 ms / 2.6 ms | 2.4 ms / 2.5 ms | 500 µs / 618 µs | 38 % | 546 µs |
| 200 | 1 to 50 KiB | all | 941 µs / 1.3 ms | 994 µs / 1.1 ms | 247 µs / 288 µs | 51 % | 49 µs |
| 600 | 1 KiB | all | 313 µs / 397 µs | 439 µs / 465 µs | 90 µs / 133 µs | 57 % | 44 µs |
| 600 | 10 KiB | all | 682 µs / 1.7 ms | 844 µs / 1.2 ms | 177 µs / 288 µs | 47 % | 147 µs |
| 600 | 50 KiB | all | 2.2 ms / 3.9 ms | 2.2 ms / 2.3 ms | 493 µs / 545 µs | 39 % | 558 µs |
| 600 | 1 to 50 KiB | all | 972 µs / 2.2 ms | 965 µs / 1.0 ms | 240 µs / 286 µs | 62 % | 53 µs |
| 200, eight at once | 1 to 50 KiB | all | 2.3 ms / 8.8 ms | 1.1 ms (mean) | 274 µs (mean) | 63 % | 91 µs |
| 600 | 1 KiB | none, no reader of args.json, skipArgsFile | 71 µs / 87 µs | 100 µs / 102 µs | – / – | – | 0.7 µs |
| 50 | 1 KiB | three member paths | 224 µs / 261 µs | 262 µs / 293 µs | 50 µs / 68 µs | 76 % | 21 µs |
| 50 | 1 KiB | two resources as arguments, logs, reads | 267 µs / 321 µs | 370 µs / 448 µs | 62 µs / 125 µs | 58 % | 43 µs |
| 50 | 1 KiB | the same, each read 1 ms | 3.2 ms / 4.3 ms | 1.2 ms / 2.0 ms | 293 µs / 604 µs | 15 % | 149 µs |

The p95 of the 600-step shapes with logs and reads, and of the
eight-at-once shape, carry file-system stalls on this machine at the
time. In the counts with the longest tails, the `args-file` stage's mean
was 0.7 to 1.2 ms against a p50 of about 130 µs. Their p50s and CPU
figures are steady across counts.

#### Large values

The large-value benchmarks of the plan (items 4 to 7 below) were
recorded on GitHub's hosted runners, through the workflow `Benchmarks`:
Go 1.27.3, Bun 1.3.10, `ubuntu-24.04` and `ubuntu-24.04-arm`, 4 vCPUs
each. Each figure is the median of 10 rounds, the builds rotating within
one job.

Each job draws its own CPU. On amd64 the step boundary ran on an AMD EPYC
9V74 and the rest on an EPYC 7763; on arm64 every job ran on a
Neoverse-N2. The command, from a checkout:

```sh
gh workflow run bench.yml -f builds="main=<base> head=<head>" \
  -f groups="boundary shapes passthrough expressions peaks small" -f rounds=10
```

Time is per step for the boundary and the shapes, per run for the
pass-through chain and per evaluation for the expressions. CPU is per
step or per run, as the time is. The pass-through chain forwards a 2.1 MB
array of records; on the plugin host its `onRunEnd` takes the whole
trace.

| | amd64 | arm64 |
|---|---|---|
| step boundary, host mode, 256 KiB: step / engine CPU / host CPU | 6.93ms / 4.37ms / 3.56ms | 7.88ms / 4.73ms / 4.71ms |
| step boundary, host mode, 1 MiB: step / engine CPU / host CPU | 30.89ms / 20.62ms / 16.37ms | 36.69ms / 21.31ms / 21.02ms |
| step boundary, host mode, 4 MiB: step / engine CPU / host CPU | 117.73ms / 71.80ms / 77.22ms | 138.36ms / 75.15ms / 93.90ms |
| step boundary, host mode, 16 MiB: step / engine CPU / host CPU | 492.87ms / 275.23ms / 328.67ms | 547.11ms / 286.96ms / 376.92ms |
| host shape s20-256KiB: step / engine CPU / host CPU | 16.26ms / 14.39ms / 6.45ms | 15.06ms / 11.71ms / 6.89ms |
| host shape s10-1MiB: step / engine CPU / host CPU | 69.51ms / 60.97ms / 25.49ms | 61.90ms / 48.02ms / 25.38ms |
| host shape s8-4MiB: step / engine CPU / host CPU | 273.47ms / 235.03ms / 136.08ms | 239.24ms / 183.80ms / 120.45ms |
| host shape s4-16MiB: step / engine CPU / host CPU | 1.193s / 973.07ms / 598.47ms | 1.031s / 784.86ms / 501.11ms |
| pass-through 2.1 MB, depth 4, fan-out 3, host: run / engine CPU / bytes allocated | 198.77ms / 171.27ms / 73.2MiB | 164.92ms / 128.79ms / 73.2MiB |
| pass-through 2.1 MB, depth 4, fan-out 3, host-plugin: run / engine CPU / bytes allocated | 527.26ms / 299.42ms / 170.7MiB | 433.33ms / 232.43ms / 170.7MiB |
| 2 MiB result: short read / null test / object of three fields | 12.30ms / 12.34ms / 16.66ms | 10.20ms / 10.11ms / 12.64ms |
| whole run, two steps, 16 MiB: engine / host / paired sample, MiB | 264.9 / 277.2 / 531.6 | 260.4 / 280.1 / 529 |
| whole run, two steps, 32 MiB: engine / host / paired sample, MiB | 497.3 / 464.9 / 887 | 494.2 / 483.2 / 951.6 |
| whole run, eight steps, 16 MiB: engine / host / paired sample, MiB | 612 / 297 / 875 | 604.6 / 295.1 / 870.4 |

The peaks of the last three rows were measured again on Bun 1.4.3, against
Bun 1.3.10 in the same job:

- Each test process ran in a container limited to 1,536 MiB without swap
  (the workflow's `memory_limit`).
- Each figure is the median of 5 rounds.
- The amd64 job ran on an AMD EPYC 9V45 and the arm64 job on a
  Neoverse-N2
  ([run](https://github.com/invakid404/windmill-lite/actions/runs/42606138796)).

| Whole run: engine / host / paired sample, MiB | amd64, Bun 1.3.10 | amd64, Bun 1.4.3 | arm64, Bun 1.3.10 | arm64, Bun 1.4.3 |
|---|---|---|---|---|
| two steps, 16 MiB | 276.1 / 290.9 / 555.9 | 272.3 / 238.3 / 512.7 | 276.5 / 273.1 / 532.7 | 270.0 / 231.2 / 499.0 |
| two steps, 32 MiB | 508.0 / 482.4 / 945.6 | 508.8 / 358.7 / 827.2 | 494.0 / 468.1 / 966.1 | 505.7 / 334.1 / 768.1 |
| eight steps, 16 MiB | 623.3 / 363.8 / 940.6 | 614.5 / 298.0 / 851.7 | 620.1 / 284.3 / 882.3 | 616.5 / 240.7 / 822.2 |

On Bun 1.4.3 the host's peak is 15 to 29 % lower. The engine's moves by
at most 2.4 %, which `benchstat` does not find significant.

A long-lived engine keeps about 2 KiB per finished run on every
topology (`BenchmarkRetainedHeap`,
[cmd/windmill-lite](../../cmd/windmill-lite/README.md#benchmarks)).

#### What the baseline shows

These are facts of the measurements; nothing was changed to obtain them.

- At 26 B a step takes 150 µs on average in host mode. Go handling takes
  129 µs of it, and writing `args.json` alone 114 µs. The engine's CPU per
  step is 152 µs, against 25 µs for the host.
- Go handling is the largest part of a step at every size, one run at a
  time. At 4 MiB it takes 73 ms of 123 ms, against 25 ms for the
  harness's parse and serialise and 25 ms for the transport.
- With eight runs at once, Go handling dominates below 32 KiB, and the
  transport from 32 KiB. Each process writes the `args.json` of one
  invocation at a time, under a lock.
- The boundary takes 95 % of a run's module time at 26 B, and 51 to 60 %
  from 256 KiB, where the engine's own part grows. In the embedder-shaped
  flows it takes 38 to 63 %, and 83 % with no logs, reads or expressions.
- An expression that binds a 50 KiB flow input costs about 550 µs per
  step, against 44 µs for a 1 KiB input.
- Against the first record, elapsed p50s and the host's CPU are about the
  same. The engine's CPU is higher from 4 KiB, for example 1.2 ms against
  889 µs per step at 32 KiB, one run at a time in host mode: the collector
  now reclaims the finished runs the engine used to keep.

## Decision

### Two texts per value, held in Go

The engine's value type (`jsonval`) holds:

- the **producer text**, byte for byte as the harness or QuickJS-ng wrote it;
- the **stored text**, the jsonb-canonical form, computed lazily the first
  time a storage route reads the value;
- a NUL flag, computed eagerly, because every result is stored in Windmill,
  which removes a NUL from the stored copy, and only from it
  ([OR-27](../semantics/oracle.md#or-27); until Windmill 1.768.0 a NUL
  failed the step).

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
3. **Storage boundary:** each argument becomes its stored text, every NUL
   escape removed first ([FX-48](../semantics/execution.md#fx-48) case 2,
   since Windmill 1.826.0). A text jsonb still refuses (an unpaired
   surrogate, a number beyond `numeric`'s range) is a push failure, which is
   a chaining error routed as [FX-15](../semantics/execution.md#fx-15) says
   ([FX-51](../semantics/execution.md#fx-51) case 2): a module failure
   named `Anyhow` at root pickup, a failed child at child pickup, or
   `InternalError` at a later transition, with the texts a recording pins
   ([FX-50/a](../semantics/conformance.md#fx-50)).
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

**Large values (amendment).** The plan gains the size-dependent cost of
the step path. It measures values below Windmill's result-size limit,
which windmill-lite applies as Windmill does
([OR-7](../semantics/oracle.md#or-7),
[WR-15](../semantics/languages.md#wr-15)). The values are synthetic,
shaped like a real workload's large ones: wide objects with long strings,
and arrays of wide records, tens of KB to several MiB
([cmd/windmill-lite](../../cmd/windmill-lite/README.md#benchmarks)):

4. the step boundary at 256 KiB, 1, 4 and 16 MiB (`WL_BENCH_SIZES`), and
   host shapes whose steps return 256 KiB to 16 MiB;
5. a pass-through chain: a 2.1 MB array of records returned unchanged
   through 0 to 4 nested sub-flows, then forwarded whole into 1 to 5
   steps' arguments. It runs with and without a plugin that takes the
   run-end trace, and reports the engine's CPU and bytes allocated per
   run;
6. expressions that read short fields out of a 2 MiB result: a short read,
   a null test and an object of three fields, each timed on its own;
7. the peak resident memory of the engine and of the Bun host over one run
   of 16 and 32 MiB, each process's own and an approximate paired sample of
   both together.

Their figures, unlike item 2's, come from GitHub's hosted runners
(linux/amd64 and linux/arm64), through the workflow `Benchmarks`. It is
started by hand and gates nothing. It rotates the builds it compares on
one runner, round after round, and compares them with `benchstat` (at
least ten rounds, p < 0.05).

At these sizes a change moves a step by tens of percent, far above the
runners' noise. The figures then hold on both architectures an embedder
deploys to, which one laptop cannot show.

Its `small` group runs item 2's payloads of 1 KiB and below the same
way, each in a process of its own. It checks that a change does not slow
them beyond the runners' noise; item 2's recorded baseline stays the
local one above.

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
- The retarget to Windmill 1.862.0 changed the NUL. Windmill removes every
  NUL escape before it stores a result (1.768.0) or a job's arguments
  (1.826.0), so a NUL fails nothing there
  ([FX-48](../semantics/execution.md#fx-48),
  [OR-27](../semantics/oracle.md#or-27)). The canonicaliser still rejects
  it, as `jsonb` input does; the engine removes it from the stored copy
  first (`jsonval.WithoutNUL`), at those two boundaries only, and the
  producer text keeps it, so a memory route still reads it (FX-48/e). A
  for loop's iterator array, which Windmill stores in the flow's status,
  neither a result nor arguments, still fails on a NUL: whether that write
  removes it too is a question for the reference. An unpaired surrogate and
  an out-of-range number fail as before
  ([FX-51](../semantics/execution.md#fx-51)).
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

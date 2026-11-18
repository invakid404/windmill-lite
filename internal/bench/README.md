# internal/bench

**Current state:** working. It holds what the repository's benchmarks
share, and only test files use it. This README covers `internal/bench`;
the benchmarks themselves are in [internal/jsonval](../jsonval/README.md),
[internal/harness](../harness/README.md#benchmarks) and
[cmd/windmill-lite](../../cmd/windmill-lite/README.md#benchmarks).

The step boundary is where the engine hands a
[step](../../docs/glossary.md#step)'s arguments to the
[harness](../../docs/glossary.md#harness) and reads its result back. This
package gives every benchmark of that boundary the same three things: the
synthetic payloads that they measure, the percentile rule that they report,
and a runner that starts concurrent workers together. The benchmarks follow
the plan of the [ADR](../../docs/glossary.md#adr)
[JSON at step boundaries](../../docs/design/0005-json-step-boundaries.md),
which also holds their recorded
[baseline](../../docs/design/0005-json-step-boundaries.md#baseline).
[docs/benchmarks.md](../../docs/benchmarks.md) says how to run them and
compare two builds.

## Files

| File | Contents |
|---|---|
| `synthetic.go` | `Synthetic`, `SyntheticSizes`, `SyntheticMin` and `SizeLabel`: the payloads that every benchmark of the step boundary measures |
| `stats.go` | `Percentile` and `Mean` |
| `workers.go` | `Workers` and `Concurrency`: operations on workers that start together |

## Public surface

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Synthetic` | func | Returns a compact JSON object of exactly the given size, shaped like a step's result; below 64 bytes, the 26-byte object of `SyntheticMin` ([Payloads](#payloads)) | the benchmarks and tests of `internal/jsonval`, `internal/harness` and `cmd/windmill-lite` |
| `SyntheticSizes` | var | The sizes that the boundary benchmarks measure: 26 B, 1 KiB, 4 KiB, 32 KiB, 256 KiB, 1 MiB and 4 MiB | the same |
| `SyntheticMin` | const | 26: the size of the smallest payload, `{"id":1,"name":"step-one"}` | the benchmarks of `internal/harness` |
| `SizeLabel` | func | Names a payload size in benchmark names: `26B`, `4KiB`, `1MiB` | the benchmarks and tests of `internal/jsonval`, `internal/harness` and `cmd/windmill-lite` |
| `Percentile` | func | The nearest-rank percentile of a list of durations ([Percentiles](#percentiles)) | the benchmarks of `internal/harness` and `cmd/windmill-lite` |
| `Mean` | func | The arithmetic mean of a list of durations, or 0 for none | the benchmarks of `cmd/windmill-lite` |
| `Workers` | func | Runs concurrent workers that start together ([Concurrent workers](#concurrent-workers)) | the benchmarks of `internal/harness` and `cmd/windmill-lite` |
| `Concurrency` | struct | What a `Workers` call reached: `Wall`, `Peak`, `Mean`, `Ops` | the callers of `Workers` |

## Payloads

`Synthetic(size)` returns a compact JSON object of exactly `size` bytes for
a size of 64 bytes or more, shaped like a step's result:
`{"items":[...],"pad":"..."}`.

- **The records.** It holds as many records as fit. Each holds strings (one
  with an escaped newline), an integer, a decimal, a boolean, a list, a
  nested object and null. A padding string then brings the text to `size`.
- **Small sizes.** Below 64 bytes, it returns the 26-byte object of
  `SyntheticMin`, whatever the size asked: `Synthetic(32)` has 26 bytes.
- **Stable text.** The text is valid UTF-8 and holds no NUL. The same size
  always gives the same text, so the benchmarks of the canonicaliser, the
  frame codecs, the transport and whole runs measure the same bytes.

`SyntheticSizes` runs from a small value, through values that fit one
frame, to values that travel in part frames (above 1 MiB).

## Percentiles

`Percentile(ds, q)` uses the nearest-rank method: the ⌈q·n⌉-th smallest of
n observations, and the smallest for q = 0.

- The p50 of 20 observations is the 10th, and the p95 the 19th. The p50 of 2
  is the 1st.
- It does not reorder its input, and it returns 0 for no observations.
- `Mean` is the arithmetic mean. Unlike a percentile, a mean adds up across
  the stages of a step.

## Concurrent workers

`Workers(conc, ops, op)` runs `conc` workers that each call `op` `ops`
times, with the worker's index.

- **They start together.** Every worker waits at a barrier until all are
  ready, and none calls `op` before then. After the barrier each worker runs
  on its own, so a short operation can end before another worker's first
  one starts: `conc` operations in flight is the aim, not a guarantee.
- **Equal shares.** Every worker does the same number of operations, unless
  one stops at an error.
- **What it returns.** `Wall` is the time from the barrier's release to the
  last worker's end, and `Ops` each successful operation's duration, by
  worker. `Peak` is the most operations that were in flight at once, as
  observed. `Mean` is the successful operations' total duration over the
  wall time.
- **Errors.** A worker stops at its first error, and that operation's
  duration is not in `Ops` or `Mean`. `Workers` returns the first error that
  any worker met, once every worker has stopped.

## Tests

The tests are in `bench_test.go`, and need only Go: no cgo, Bun or
variable. Run them with `go test -count=1 ./internal/bench`. CI's `Test`
step runs them with the rest of the module
([docs/ci.md](../../docs/ci.md#the-go-jobs)).

| Test (`bench_test.go`) | What it checks |
|---|---|
| `TestPercentile` | `Percentile` at 1, 2, 3, 7, 20 and 100 observations, where q·n is an integer and where it is not |
| `TestWorkers` | `Workers` with one, two and three operations per worker, whose operations overlap on purpose (each sleeps 20 ms): its peak, mean and operation counts, and its error |
| `TestSynthetic` | Each payload's size, that it is one JSON object, and that it is stable; `SizeLabel`'s names |

[internal/jsonval](../jsonval/README.md) checks that its canonicaliser and
its test implementation agree on every payload.

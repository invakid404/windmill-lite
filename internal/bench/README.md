# internal/bench

**Current state:** implemented: what the repository's benchmarks share.
Only test files use it. This README covers `internal/bench`; the
benchmarks themselves are in [internal/jsonval](../jsonval/README.md),
[internal/harness](../harness/README.md#benchmarks) and
[cmd/windmill-lite](../../cmd/windmill-lite/README.md#benchmarks), with
their recorded baseline in
[ADR 0005](../../docs/design/0005-json-step-boundaries.md#baseline).

## Files

| File | Contents |
|---|---|
| `synthetic.go` | `Synthetic`, `SyntheticSizes`, `SyntheticMin` and `SizeLabel`: the payloads every benchmark of the step boundary measures |
| `stats.go` | `Percentile` and `Mean` |
| `workers.go` | `Workers` and `Concurrency`: operations on workers that start together |

## Payloads

`Synthetic(size)` returns a compact JSON object of exactly `size` bytes,
shaped like a step's result. It holds as many records as fit, each with
strings (one with an escaped newline), numbers, booleans, a list, a nested
object and null, then a padding string. Below 64 bytes it returns the
26-byte object `{"id":1,"name":"step-one"}`.

`SyntheticSizes` lists the sizes the boundary benchmarks measure, from
26 B to 4 MiB, and `SizeLabel` names them (`26B`, `4KiB`, `1MiB`). The
same size always gives the same text, so the benchmarks of the
canonicaliser, the frame codecs, the transport and whole runs measure the
same bytes.

## Percentiles

`Percentile(ds, q)` uses the nearest-rank method: the ⌈q·n⌉-th smallest
of n observations, and the smallest for q = 0. So the p50 of 20
observations is the 10th and the p95 the 19th; the p50 of 2 is the 1st.
It does not reorder its input. `Mean` is the arithmetic mean, which, unlike
a percentile, adds up across the stages of a step.

## Concurrent workers

`Workers(conc, ops, op)` runs `conc` workers that each call `op` `ops`
times. They start together: no worker calls `op` before every worker is
ready, so one operation each already puts `conc` operations in flight at
once. Every worker does the same number of operations, so the end has no
tail of fewer workers, beyond operations of unequal length.

It returns the wall time, each operation's duration, and the concurrency
reached: `Peak`, the most operations in flight at once, and `Mean`, the
operations' total duration over the wall time. A worker stops at its first
error, which `Workers` returns once every worker has stopped.

## Tests

`bench_test.go` checks `Percentile` at 1, 2, 3, 7, 20 and 100
observations, where q·n is an integer and where it is not; `Workers` at
one and three operations per worker, its peak, mean and operation counts,
and its error; and each payload's size, that it is one JSON object, and
that it is stable. [internal/jsonval](../jsonval/README.md) checks that
its canonicaliser and its test implementation agree on every payload.

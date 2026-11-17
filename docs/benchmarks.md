# Benchmarks

This guide says how to run the end-to-end benchmarks of the
`windmill-lite` command, what each one measures, and how to compare two
builds of the [engine](glossary.md#engine). It is for contributors who
measure a change.

## What the benchmarks measure

The benchmarks in [cmd/windmill-lite](../cmd/windmill-lite/README.md#benchmarks)
measure the step boundary end to end: the cost of handing a
[step](glossary.md#step) to the [harness](glossary.md#harness) and its
result back. Every benchmark but `BenchmarkArgsFileOps` runs synthetic
[flows](glossary.md#flow) with the engine in the benchmark's own process,
so that it reads each [run](glossary.md#run)'s
[trace](glossary.md#trace) and the engine's CPU time.
`BenchmarkArgsFileOps` measures file writes alone, in Go.

The recorded baseline, with its machine, commands and load, is in the
[JSON at step boundaries ADR](design/0005-json-step-boundaries.md#baseline).
[internal/bench](../internal/bench/README.md) holds the payloads, the
percentiles and the concurrent workers that the benchmarks share.

## Run a benchmark

Every benchmark but `BenchmarkArgsFileOps` needs [Bun](glossary.md#bun)
1.4.3 and the dependencies of the
[build helper](glossary.md#build-helper). Such a benchmark fails without
`WL_BUN`, rather than skip and report a pass with no figures.

1. Point `WL_BUN` at the Bun 1.4.3 executable:
   `export WL_BUN=/path/to/bun-1.4.3/bun`.
2. Install the build helper's dependencies:
   `(cd harnesses/bun/build && "$WL_BUN" install --frozen-lockfile)`.
3. Run the benchmarks with `-bench`, from the root of the checkout:

   ```sh
   go test ./cmd/windmill-lite -run '^$' \
     -bench 'BenchmarkStepBoundary|BenchmarkHostShapes' -benchtime 3s -count 3
   ```

4. To compare two outputs, run
   [`benchstat`](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat) on
   them: `benchstat old.txt new.txt`.

This runs every shape in one process. To compare revisions or to record a
baseline, run each shape in a process of its own
([One process per shape](#one-process-per-shape)). The baseline's exact
commands are in the
[JSON at step boundaries ADR](design/0005-json-step-boundaries.md#baseline).

## The benchmarks

| Benchmark | What it runs |
|---|---|
| `BenchmarkStepBoundary` | `f/bench/chain`, eight steps that each return their argument, on both topologies, one run at a time and eight at once. The payloads are those of [internal/bench](../internal/bench/README.md), from 26 B to 4 MiB. The first step takes the flow input's `p`, and the others `previous_result`, which run no expression. |
| `BenchmarkHostShapes` | Flows shaped like a host-mode embedder's, in [host mode](glossary.md#host-mode): 50 to 600 steps, and results of 1 to 50 KiB. Except in the bare shape, each step writes up to 3 log lines, makes up to 2 variable or resource reads, and evaluates an [expression](glossary.md#expression) that binds the flow input. One shape runs eight at once. |
| `BenchmarkHostShapes`, the `plain` shape | The bare shape's 600 steps of 1 KiB, run by `f/bench/plain`, which returns the same result and holds no I/O code, on the host-plain topology. Its bundle has no reader of `args.json`, so the file is left out. The bare shape's `f/bench/work` can fetch, so its bundle is a reader and keeps the file. |
| `BenchmarkHostShapes`, the `pathsA` shape | 50 bare steps that each read three member paths over JSON values: `flow_input.cfg.region`, `previous_result.data.items[0].id` and `results.s<k>?.n` |
| `BenchmarkHostShapes`, the `resA` shapes | On the plugin host, 50 steps that each take two of 4 resources as `$res:` arguments, the previous result, an expression that binds the flow input, and the previous step's member `results.s<k>.n` |
| `BenchmarkHostShapes`, the large shapes | 20 steps of 256 KiB, 10 of 1 MiB, 8 of 4 MiB and 4 of 16 MiB (`s20-256KiB` to `s4-16MiB`). Each step works as in the 1 KiB shapes. Its template reads two short fields of a flow input whose `seed` is as large as the results. |
| `BenchmarkRetainedHeap` | Sequential runs of `f/bench/pair`, two steps that each return a payload of 64 KiB or 16 MiB, on one long-lived runtime: in host mode, in subprocess mode and on the plugin host. It reads the engine's Go heap after two collections, before and after the runs, and reports what each finished run leaves reachable (`heap-mib/run`). Run it with `-benchtime 1x`. |
| `BenchmarkPassThrough` | A large value passed on unchanged (`bench_large_test.go`): an array of wide records of 64 KiB and about 2.1 MB. A step returns it through 0 to 4 nested subflows, and the flow forwards it into 1, 3 or 5 steps. It runs on the host and on the plugin host, whose `onRunEnd` takes the whole trace. |
| `BenchmarkExpressions` | A step returns a wide object with a long markup string and its plain-text form, of 96 KiB and 2 MiB. Eight steps each evaluate one expression on it, or on the same value as the flow input's `v`. Each expression's time is reported per run (`expr-e<k>-ns`). |
| `BenchmarkNativeRealm` | Sequential runs of `f/native/steps` on one host and on one warm harness, read by `f/native/probe` after a first run and after each quarter of the runs. It reports what each native invocation of the last three quarters leaves after a full collection: `Script` objects (`scripts/inv`), JavaScript heap (`heap-B/inv`) and resident set (`rss-B/inv`). Run it with `-benchtime 1x`. |
| `BenchmarkEmbedder` | Twelve workloads shaped like a host-mode embedder's requests (`bench_embed_test.go`), on windmill-lite in host mode and as an in-process baseline ([The embedder shapes](#the-embedder-shapes)). Run it with `-benchtime 1x`. |
| `BenchmarkArgsFileOps` | The write sequence of a step's `args.json` alone, at 256 B, 1 KiB and 64 KiB: a temporary file in a directory under `TMPDIR`, written, closed and renamed over `args.json`. It reports user and system CPU per write, from `getrusage`. |
| `BenchmarkRunPeaks` | Linux only, with `-benchtime 1x`: the peak resident memory of the engine and of the host over one run. The cases are two steps that return their argument at 16 and 32 MiB, and eight at 16 MiB, each on its own runtime and host after a warm run. It also reports the largest sum of the two resident sizes, read together every millisecond (`combined-sampled-MiB`). |

More detail of three of them:

- `BenchmarkRetainedHeap` keeps its own input alive through both heap
  readings.
- `BenchmarkPassThrough` maps the forwarded value to its ids, and reports
  per run the time, the engine's CPU and bytes allocated, and the host's
  CPU.
- `BenchmarkExpressions` evaluates a short read, an optional-chained read,
  a null test, an object of three fields and a map. `BenchmarkNativeRealm`
  also reports the last reading and the `Script` objects that the whole run
  left (`scripts-kept`).

What one iteration of each benchmark is:

| Benchmark | One iteration |
|---|---|
| `BenchmarkStepBoundary`, `BenchmarkHostShapes` | One run per worker, with the workers started together. So even one iteration has every run of its concurrency in flight at once. |
| `BenchmarkPassThrough`, `BenchmarkExpressions` | One run, after two warm runs |
| `BenchmarkRunPeaks` | One run of the case |
| `BenchmarkEmbedder` | The whole measurement of a workload: the warm requests and the measured requests on both sides |
| `BenchmarkRetainedHeap`, `BenchmarkNativeRealm` | A fixed number of runs (200 of 64 KiB or 8 of 16 MiB; `WL_NATIVE_RUNS`), whatever the iteration count: run them with `-benchtime 1x` |
| `BenchmarkArgsFileOps` | One write of `args.json` |

## Topologies

| Topology | What runs the steps |
|---|---|
| `host` | `cmd/windmill-lite/testdata/benchhost/host.ts`, compiled with `bun build --compile`: a warm Bun [host](glossary.md#host) that embeds the SDK and does nothing else. The SDK starts its engine as its child: the test binary as an engine proxy, which hands the host's socket and child channel to the benchmark. The runtime is the one that `host` opens, with the watchdog off. |
| `subprocess` | The warm Bun harness that the engine spawns, one process, as `run` uses it |
| `host-plugin` | The same host with a [host plugin](glossary.md#host-plugin) (`WL_BENCH_PLUGIN=1`), as a host that keeps its resources elsewhere runs. Its `resource` and `variable` hooks answer synthetic resources, those under `f/bench/slow/` after 1 ms, and count their calls by run and path. Its `onRunEnd` hook takes every run's trace. |
| `host-plain` | The same host on the plain shape's bundle, which has no reader of `args.json`. The host opted in to leaving the file out (`wl.Host.SkipArgsFile`), unless `WL_BENCH_ARGS_FILE=1`. |
| `host-emb` | The same host with a plugin whose `resource` hook reads the embedder shapes' resources (`f/emb/*`) from their store's API (`WL_BENCH_STORE_URL`) |

## Variables

| Variable | Effect |
|---|---|
| `WL_BUN` | The Bun 1.4.3 executable. Required. |
| `WL_BENCH_SIZES` | Other payload sizes for `BenchmarkStepBoundary`, as labels separated by commas, such as `26B,1KiB,16MiB`. Without it, the sizes are those of [internal/bench](../internal/bench/README.md), up to 4 MiB. |
| `WL_BENCH_FRESH=1` | Runs each shape and configuration in a process of its own ([One process per shape](#one-process-per-shape)) |
| `WL_BENCH_TIMINGS=off` | Leaves the stage readings out, which measures what taking them costs |
| `WL_BENCH_TRACE` | Makes the plugin's `onRunEnd` hook declare a `summary` or `none` trace in place of the whole trace |
| `WL_BENCH_ARGS_FILE=1` | Keeps `args.json` on the host-plain topology, which measures its cost on the same code |
| `WL_NATIVE_RUNS` | The runs of `BenchmarkNativeRealm`; 2,000 by default |
| `WL_BENCH_HOST_BUN_OPTIONS` | Reaches the compiled host as `BUN_OPTIONS`, for example for its `--cpu-prof` |
| `WL_BENCH_HOST_ENV` | `NAME=value` pairs, separated by commas, added to the host's environment only |

[The embedder shapes](#the-embedder-shapes) has the variables of
`BenchmarkEmbedder`.

## Stages

`BenchmarkStepBoundary` and `BenchmarkHostShapes` split each step into
stages, each the difference of two readings of the same
[invocation](glossary.md#invocation). The readings are the trace's
module and harness spans, the engine side's clock
([internal/harness](../internal/harness/README.md#the-times-hook)) and the
harness's own
([harnesses/bun](../harnesses/bun/README.md#benchmark-timings)). They are
joined by the invocation's id, the step's job id. A step's stages add up
to its module span exactly, so their means add up to the step's mean.

| Stage | What it holds |
|---|---|
| `engine` | The engine outside its call of the executor: transforms, arguments and their storage, the trace |
| `executor` | The executor around the harness process: the runtime's recorder and the pool's routing |
| `go-encode` | Checks, the script's arguments and the invoke's payload |
| `args-file` | `args.json` written, the wait for its lock included |
| `go-queue` | The invocation registered and its invoke queued |
| `transport` | From the invoke queued to the result read, less the harness's time: the writer, the socket both ways and the wake-ups. It is not attributed further. |
| `bun-decode` | The invoke's last frame decoded in the harness |
| `bun-dispatch` | From decoded to the entrypoint called: context, module, binding, and any wait behind other invocations |
| `code` | The step's own code |
| `bun-serialise` | The result serialised |
| `bun-send` | The result frame encoded and handed to the socket |
| `go-decode` | The result's last frame decoded in the engine |
| `go-handoff` | From decoded to the outcome back with the engine: settlement and the `Reporter`'s calls |

## Metrics

`BenchmarkStepBoundary` and `BenchmarkHostShapes` report these figures
per step:

| Metric | Meaning |
|---|---|
| `ns/op`, `MB/s` | Wall time per step at the benchmark's concurrency, and the payload that the steps moved, both ways, per second |
| `step-p50-ns`, `step-p95-ns`, `step-mean-ns` | The step's module span |
| `<stage>-mean-ns`, `<stage>-p50-ns` | Each stage, over every step |
| `boundary-share-p50`, `boundary-share` | The share of a run's module time spent at the boundary: its harness spans less its steps' own code, per run at p50, and over every run together |
| `harness-share-p50`, `harness-share` | The same for the whole harness span |
| `module-share-p50` | The share of a run's elapsed time spent in its modules |
| `engine-cpu-ns/step`, `engine-sys-ns/step` | The engine process's CPU time, and its system part, from `getrusage` |
| `host-cpu-ns/step` | The host's or harness's CPU time, from `process.cpuUsage()` |
| `engine-cpu-p50-ns/step` and the like | Per run, for runs one at a time; a shape's host CPU too |
| `expr-ns/step` | The expression spans |
| `host-calls/run` | On the plugin host, the calls of its `resource` and `variable` hooks per run |
| `host-peak-rss-mb` | The host's or harness's peak resident set after the runs, in MiB |
| `conc-peak`, `conc-mean` | The most runs in flight at once, and the mean |
| `load1` | The one-minute load average when the runs started |

- Percentiles are nearest-rank
  ([internal/bench](../internal/bench/README.md#percentiles)).
- The probe step `f/bench/probe` reads the host's or harness's CPU time
  before and after the runs, its peak resident set and the plugin's call
  counts. It also sets and drains the harness's timings.
- A shape's last step reports the host's CPU time with its result too, so
  each run has its own figure.
- The plain shape's bundle has no probe step, which would read the process
  and so make the bundle a reader. Its host writes its CPU time and peak
  resident set to `bench-probe.json` on `SIGUSR1` instead, and the shape
  reports no stages.

The other benchmarks report figures of their own:

| Benchmark | Figures |
|---|---|
| `BenchmarkPassThrough`, `BenchmarkExpressions` | Per run: `ns/op`, `engine-cpu-ns/run`, `engine-sys-ns/run`, `host-cpu-ns/run`, `expr-ns/run`, `steps/run` and `load1`; `BenchmarkExpressions` also `expr-e<k>-ns` |
| `BenchmarkRetainedHeap` | `heap-mib/run`, `heap-mib` and `runs` |
| `BenchmarkNativeRealm` | `scripts/inv`, `heap-B/inv`, `rss-B/inv`, `scripts-kept`, `heap-MiB`, `rss-MiB` and `invocations` |
| `BenchmarkRunPeaks` | The base, peak and rise of the engine and the host (`engine-peak-MiB` and the like), `engine-live-MiB`, `sum-peak-MiB` and `combined-sampled-MiB` |
| `BenchmarkArgsFileOps` | `user-us/op` and `sys-us/op` per write, beside `ns/op` |
| `BenchmarkEmbedder` | Its own figures ([The embedder shapes](#the-embedder-shapes)) |

## The embedder shapes

`BenchmarkEmbedder` runs each workload two ways, and compares them request
by request. The side that goes first alternates, after five warm requests
on both.

- **windmill-lite:** the workload as a flow, on the `host-emb` topology,
  with the per-run read cache on. The read cache keeps the answer of a
  run's first read of each resource and variable, and answers the run's
  later reads from it. It keeps no error, and drops a variable that a step
  updates ([the read cache](../internal/platform/README.md#the-read-cache)).
  It is a named [deviation](glossary.md#deviation) of the
  [equivalence contract](design/0001-equivalence-contract.md#5-named-deviations).
- **In process:** the same step functions called as plain functions,
  composed by hand in one Bun process
  (`cmd/windmill-lite/testdata/benchhost/inproc.ts`). The same expressions
  are evaluated as JavaScript in the same scope, and each resource is read
  once per request.

Both sides call one store, a separate process (the test binary with
`WL_BENCH_EMB_STORE`). The steps read and write it by `fetch`, after
`WL_BENCH_EMB_KV_US` (300 µs by default). The resources come from its API
after `WL_BENCH_EMB_API_US` (1,200 µs).

A workload is one tree of nodes (script steps, `branchone`, identity
steps, a for-loop) that gives both the flow and the in-process program. So
both run the same steps in the same order with the same inputs. The step
scripts are synthetic: `get` and `put` are native (`//native`) and call
the store, and `calc` is a Bun step that computes. Their CPU work is
calibrated per workload.

| Workload | Per request: steps / invocations / expressions | Shape |
|---|---|---|
| `A`, `B`, `D1`, `D2`, `E`, `F` | 96 / 60 / 375, 155 / 105 / 506, 110 / 68 / 414, 135 / 84 / 459, 102 / 64 / 390, 58 / 33 / 220 | Sequential, with branches and identity steps |
| `C1`, `C5`, `C20` | 102 / 64 / 390, 178 / 128 / 654, 463 / 368 / 1,644 | A parallel loop (parallelism 2) over 1, 5 or 20 items |
| `G` | 50 / 26 / 194 | Short and branch-heavy, its expressions mostly the pipeline's shortcuts |
| `H` | 624 / 384 / 2,280 per batch: eight requests in flight of 78 / 48 / 285 | Concurrent requests |
| `A-typical` | 229 / 172 / 837 | `A` with items of about 65 KB passed through four levels of steps |
| `H-lit` | Eight requests in flight of 624 / 384 / 2,280 each | `H`'s counts read per request; run only when named |

These variables change a run:

| Variable | Effect |
|---|---|
| `WL_BENCH_EMB` | The workloads, names separated by commas (default: the twelve) |
| `WL_BENCH_EMB_N` | The measured requests per side (default 50) |
| `WL_BENCH_EMB_SIDE` | `wl` or `inproc`: one side only |
| `WL_BENCH_EMB_WORK` | The steps' CPU work, in percent of the calibration |
| `WL_BENCH_EMB_NATIVE=0` | Runs the native steps as Bun steps |
| `WL_BENCH_READ_CACHE=off` | Turns the read cache off |
| `WL_BENCH_ARGS_FILE=skip` | Leaves `args.json` out where the bundle allows |
| `WL_BENCH_EMB_PPROF` | A file for a CPU profile of the engine over the measured requests only |
| `WL_BENCH_INPROC_BUN_OPTIONS` | The in-process side's `BUN_OPTIONS` |

Its figures, per request (for `H`, per batch) and per step run:

| Metric | Meaning |
|---|---|
| `wl-p50-ms`, `wl-p95-ms`, `inproc-p50-ms`, `inproc-p95-ms` | Each side's elapsed time per request |
| `gap-p50-ms`, `gap-p95-ms`, `ratio-p50` | windmill-lite against the in-process baseline |
| `gap-us/run-step`, `gap-us/step` | The p50 gap per step of one request, and per step of a batch (`H`) |
| `engine-cpu-us/step`, `host-cpu-us/step`, `inproc-cpu-us/step`, `extra-host-cpu-us/step` | CPU per step: the engine's (this process, `getrusage` around the requests), the host's, the in-process side's, and the host's over the in-process side's |
| `wl-cpu-ms/req`, `inproc-cpu-ms/req` | CPU per request: the engine and the host together, and the in-process side |
| `gc/step`, `gc-cpu-us/step` | The engine's collections and their CPU over the measured requests (`runtime/metrics`) |
| `expr-ms/req` | The expression spans |
| `host-us/inv`, `host-p50-us/inv` | Each invocation's harness span: the time a step spends inside the host |
| `thr-host-<thread>-us/step`, `thr-inproc-<thread>-us/step` | CPU per step by thread name: on Linux from `/proc`, on macOS through `cmd/windmill-lite/testdata/benchhost/threadcpu_darwin.c` |
| `steps/req`, `steps/run`, `inv/req`, `expr/req` | The counts, read from the traces |
| `load1`, `load1-end` | The one-minute load average before and after |

## Comparing builds on GitHub's runners

The workflow `Benchmarks` ([bench.yml](../.github/workflows/bench.yml))
compares builds of the engine on GitHub's hosted runners, linux/amd64 and
linux/arm64. It runs only when started by hand, and gates nothing.

| Input | Value | Default |
|---|---|---|
| `builds` | The builds to compare, in order: `name=ref` pairs separated by spaces | `main=origin/main` |
| `groups` | The groups to run (table below), separated by spaces | `boundary shapes passthrough expressions peaks small` |
| `rounds` | The rounds per job | `10` |
| `small_builds` | The builds of the `small` group, when they are not `builds` | Empty: `builds` |
| `memory_limit` | A size that `docker run --memory` takes, such as `1536m` | Empty: each command runs on the runner |

- **A build's own environment:** a build can add environment to its runs,
  to compare a runtime setting on the same code in the same job:
  `name=ref:VAR=value`, with more `:VAR=value` fields as needed. Git
  refuses `:` in ref names. Examples: `tmpfs=<sha>:TMPDIR=/dev/shm`,
  `gc=<sha>:GOGC=400`.
- **Bun:** each build runs on the Bun that its own tree pins (`PinnedBun`
  in `internal/harness`). The workflow downloads it from Bun's releases and
  checks it against the release's `SHASUMS256.txt`. So a build from before
  a Bun upgrade runs on its own Bun, in the same job as one after it.
- **Memory limit:** with `memory_limit` set, each command runs in a
  container of its own, with that cgroup v2 memory limit and no swap. Its
  image is Ubuntu 24.04, the runners' release, pinned by digest, so the
  binaries built on the runner run in it.
- **Container figures:** after each command in a container, the output
  gives the container's peak (`memory.peak`, page cache included) and its
  OOM kills.

To compare your branch with `main`:

1. Push your branch. The workflow checks out every branch, so a build
   names it as `origin/<branch>`.
2. Start the workflow with the builds and the groups:

   ```sh
   gh workflow run bench.yml -f builds="main=origin/main head=origin/my-branch" -f groups="boundary small" -f rounds=10
   ```

3. When it ends, download its artifacts: the outputs, the comparisons and
   the machine of each job.

For the memory tests, set a memory limit:

```sh
gh workflow run bench.yml -f builds="main=origin/main head=origin/my-branch" -f groups=memory -f rounds=5 -f memory_limit=1536m
```

Each job runs one group on one architecture:

1. It builds every ref's test binary in a worktree of its own.
2. It runs the rounds. Each round runs every build once, in a rotating
   order.
3. A command that fails, or a test run that skips a test, fails the job
   once every round has run. A test run times out after 30 minutes.
4. In the `memory` group, the closure's budgets are judged on the median
   of each build's rounds instead: the job lists each round over a budget,
   and fails only when a median is over its budget.
5. It compares each build with the one before it, and the last with the
   first, with `benchstat`.

| Group | What it runs |
|---|---|
| `boundary` | `BenchmarkStepBoundary` in host mode, one run at a time, 256 KiB to 16 MiB, `-benchtime 3s` |
| `shapes` | The large shapes of `BenchmarkHostShapes`, `-benchtime 5x` |
| `passthrough` | `BenchmarkPassThrough`, `-benchtime 10x` |
| `expressions` | `BenchmarkExpressions`, `-benchtime 10x` |
| `peaks` | `BenchmarkRunPeaks`, `-benchtime 1x` |
| `retained` | `BenchmarkRetainedHeap`, `-benchtime 1x` |
| `native` | `BenchmarkNativeRealm`, `-benchtime 1x`, with `WL_NATIVE_RUNS=8000` (80,000 native invocations), so that the process warms up within the first quarter, which the figures per invocation leave out |
| `small` | In a process each (`WL_BENCH_FRESH=1`): `BenchmarkStepBoundary` at 26 B and 1 KiB on both topologies and both concurrencies, `-benchtime 3s`, and the shapes of 1 KiB and below and `s200-mixed-conc8`, `-benchtime 20x` |
| `mid` | In a process each (`WL_BENCH_FRESH=1`): the sequential shapes between 1 KiB and 256 KiB, `s50-50KiB`, `s200-mixed`, `s600-10KiB`, `s600-50KiB` and `s600-mixed`, `-benchtime 10x` |
| `embedder` | `BenchmarkEmbedder`, all twelve workloads, `-benchtime 1x`, 20 measured requests per side per round (`WL_BENCH_EMB_N=20`) |
| `memory` | The tests of `memory_test.go`, with `-v` and `WL_MEMORY_PROBE=1`: `TestModuleMemory`, then the shared-instance and shared-code layouts of `TestModuleMemoryClosure`. The job's summary gives each build's figures. |
| `closure-per-script` | The per-script layout of `TestModuleMemoryClosure`, whose host grows to about 2 GiB. It needs a `memory_limit` above that, or none. |

## One process per shape

With `WL_BENCH_FRESH=1`, each shape of `BenchmarkHostShapes` and each
configuration of `BenchmarkStepBoundary` runs in a test process of its
own (`cmd/windmill-lite/benchfresh_test.go`). The process that `go test`
starts sets nothing up. For each of them, it runs the test binary again,
for that benchmark alone, and reports the child's figures as its own.

Use this mode to compare revisions and to record a baseline. In one
process, a shape runs on the heap that the shapes before it left, so how
often the Go collector runs depends on which shapes ran first. The
engine's CPU and the slowest steps move with it. A change to what the
engine keeps in memory then moves the figures of every later shape, not
only its own.

One process is quicker, and is enough for a quick look, or for a single
shape or configuration, which `-bench` already runs alone. `go test`'s
`-timeout` covers the whole process, children included, so a full run
needs more than its default 10 minutes:

```sh
WL_BENCH_FRESH=1 WL_BUN=/path/to/bun-1.4.3/bun go test ./cmd/windmill-lite -run '^$' \
  -bench 'BenchmarkStepBoundary|BenchmarkHostShapes' -benchtime 10x -count 3 -timeout 60m
```

What each child gets:

- the test flags that this process was given, such as `-benchtime`,
  `-timeout`, `-benchmem`, `-short` and `-v`;
- `-cpu` at the GOMAXPROCS of each `-cpu` value, so `-cpu 1,4` runs each
  benchmark in a child per value;
- for a profile or trace flag, a file of its own: the name, then the
  benchmark's, its `-cpu` and the child's number, such as
  `cpu.out.BenchmarkHostShapes_shape-s600-1KiB.cpu4.3`.

The flags that this process acts on itself stay its own: `-run`, `-bench`,
`-count`, `-list`, `-skip`, `-shuffle`, fuzzing and coverage. `-count`
starts a child for each count. With more than one `-cpu` value, each
benchmark starts one more child, whose figures go unused: the testing
package calls a benchmark once before it sets GOMAXPROCS to the first
value.

The output keeps its form, so `benchstat` reads it as before. The
iteration count printed is the parent's: with `-benchtime 10x` it is 10,
as the child's, and with a duration it is 1. `BenchmarkRetainedHeap` has
no such mode: it measures one long-lived runtime on purpose.

## Tests that keep the benchmarks working

These tests run with the ordinary tests, so a broken benchmark fails CI
without a benchmark run.

| Test | What it checks |
|---|---|
| `TestBenchSplit` | Runs the measurement once on each topology, with two runs at once. Both runs overlap, every step is matched with its readings, and its stages add up to its module span. It skips without `WL_BUN`. |
| `TestBenchFresh` | One boundary configuration, run with `WL_BENCH_FRESH=1`, ran in a child process and reports the usual figures. |
| `TestEmbShapes` | Each embedder shape's steps, invocations and expressions per request are within 1% of its target counts. `BenchmarkEmbedder` itself checks that both sides return the same first result. |
| `TestLargeBenchHelpers` | The large-payload benchmarks' helpers: the size labels that `WL_BENCH_SIZES` reads, the resident size read from `/proc`, and a value's size |

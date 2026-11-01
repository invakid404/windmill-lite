# cmd/windmill-lite

**Current state:** milestones F4, F1 and F3a of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md). This README
covers the `windmill-lite` command in this directory and its `testdata/`.
The command prints its version, builds a bundle from a Windmill sync
folder, verifies a bundle, runs a bundle's flow or script through the
[wl](../../wl/README.md) library (F1), checks that its expression engine
starts, and serves host mode on a built bundle through `wl` (F3a). The commands that serve a bundle (`serve`), pack it
(`pack`, `image`) or generate clients (`gen`) do not exist yet.

`build -shared-code` builds the bundle with the shared code of
[ADR 0013](../../docs/design/0013-shared-module-code.md).

## Usage

```sh
go run ./cmd/windmill-lite            # windmill-lite (devel)
go run ./cmd/windmill-lite version    # same; --version, -version and -v also work
windmill-lite build <folder> -o <bundle> [flags]
windmill-lite verify <bundle> [-manifest <release manifest>] [-digest <digest>]
windmill-lite run <path> --bundle <bundle> [--args <json|@file>] [-cpuprofile <file>] [flags]
windmill-lite selfcheck
windmill-lite host --bundle <dir> --workdir <dir> [--kill-after <duration>] [-cpuprofile <file>] [flags]
```

Flags may come before or after the positional argument. A usage error exits
with status 2; any other failure with status 1.

### build

`windmill-lite build <folder> -o <bundle>` builds the bundle of
[ADR 0010](../../docs/design/0010-packaging.md) with
[internal/bundle](../../internal/bundle/README.md): it reads the sync folder
from the root above `<folder>` (the directory of `wmill.yaml`), compiles it,
bundles its Bun scripts on the pinned Bun 1.3.10, and writes the bundle. It
prints the bundle digest on standard output, and warnings on standard
error.

| Flag | Meaning |
|---|---|
| `-o` | the bundle to write: a directory that must not exist or be empty, or a file ending in `.wlb` that must not exist, written as an archive |
| `-contract` | the host contract file (YAML or JSON); without one every flow and script of the folder is a root |
| `-bun` | the Bun executable; `$WL_BUN`, or `bun` on the `PATH` |
| `-helper` | the Bun build helper, `harnesses/bun/build/main.ts` with its dependencies installed; `$WL_BUILD_HELPER`. It is required |
| `-harness-runtime` | the Bun harness runtime directory whose digest the manifest records; `$WL_HARNESS_RUNTIME`, or `runtime/` beside the helper's directory |
| `-bun-mismatch` | build on another Bun and mark the bundle `nonconformant_runtime` |
| `-module-state` | `warm` (default) or `fresh` ([ADR 0006](../../docs/design/0006-module-state.md)) |
| `-shared-code` | build with shared code ([ADR 0013](../../docs/design/0013-shared-module-code.md)). Warm and host-mode processes then compile each build group's dependency modules once and give each step module its own instance of them; the differences are deviation [D18](../../docs/design/0001-equivalence-contract.md#5-named-deviations). Fresh runs and CLI parity keep the per-script bundles |
| `-strict` | refuse absent for-loop `skip_failures` and branchall `skip_failure` instead of defaulting them |
| `-work` | a scratch directory for dependency installs; a temporary one by default |
| `-verbose` | copy the build helper's output to standard error |

From a checkout, with the helper's dependencies installed
(`bun install --frozen-lockfile` in `harnesses/bun/build`):

```sh
WL_BUILD_HELPER=$PWD/harnesses/bun/build/main.ts \
  windmill-lite build internal/bundle/testdata/fixture/windmill \
  -contract internal/bundle/testdata/fixture/contract.yaml -o /tmp/fixture.wlb
```

The manifest records the engine's version (below). An engine without one
records `(devel)` and says so on standard error.

### verify

`windmill-lite verify <bundle>` recomputes the digest of every file of a
bundle directory or `.wlb` archive and checks them against the bundle's own
manifest, or against a release manifest given with `-manifest`, whose bytes
the bundle's manifest must then equal. `-digest` pins the bundle digest. It
prints the bundle digest and exits with status 0 when everything matches;
otherwise it lists every difference and exits with status 1. A bundle that
a different engine version built still verifies, with a note that this
engine would not open it.

### run

`windmill-lite run <path> --bundle <bundle>` is the command-line adapter of
[ADR 0009](../../docs/design/0009-host-adapters.md): it opens the bundle
with `wl.Open`, runs the flow (or, with `-kind script`, the root script)
at `<path>` with `Runtime.Run`, and prints the outcome as JSON on standard
output: `id`, `status`, `result` (the final result in its stored form),
`may_have_mutated`, `warnings`, and with `-trace` a summary of the trace
(`module_state`, each step's site, attempt, job, status and result, and
each platform call's site, job, route and status), which is for debugging,
not the trace's wire form. It exits with status 0 when the run succeeded or
was skipped, and 1 when it failed or was canceled, or could not start;
SIGINT and SIGTERM cancel the run.

| Flag | Meaning |
|---|---|
| `--bundle` | the bundle: a directory, or a `.wlb` archive, which is extracted into a directory of its own under the data directory and removed afterwards. It is required |
| `--args` | the arguments: a JSON object, or `@file` holding one; `{}` by default |
| `-kind` | `flow` (default) or `script` |
| `-id` | the run's job id; a new UUID by default |
| `-bun` | the Bun executable, which must be Bun 1.3.10; `$WL_BUN`, or `bun` on the `PATH` |
| `-harness-runtime` | the Bun harness runtime directory the bundle was built for, whose digest must be the manifest's; `$WL_HARNESS_RUNTIME`. A bundle with steps needs it |
| `-module-state` | `warm` or `fresh`; the bundle's by default |
| `-data` | the data directory the runtime writes into; a temporary one, removed afterwards, by default |
| `-digest` | the bundle digest the bundle must have |
| `-variables` | a JSON file of the host's variables, keyed by path, as in a bundle's `data/variables.json` (`value`, `is_secret`, `encrypted`); secrets go here, since a bundle never holds one |
| `-resources` | a JSON file of the host's resources, keyed by path, as in a bundle's `data/resources.json` (`resource_type`, `value`) |
| `-trace` | print the trace summary with the outcome |
| `-cpuprofile` | write a CPU profile of the engine to this file until the run ends ([Profiling](#profiling)) |

The engine version (below) must be the one that built the bundle. With the
fixture built as above:

```sh
windmill-lite run f/app/main --bundle /tmp/fixture.wlb \
  --args '{"name": "lite"}' -harness-runtime harnesses/bun/runtime
```

### selfcheck

`windmill-lite selfcheck` starts the expression engine (QuickJS-ng, through
[internal/expr/quickjs](../../internal/expr/quickjs/README.md)) and
evaluates three probes whose results do not depend on the C library: the
language core, a bound host string, and a recursion 800 calls deep, which
needs the engine's own thread stacks. It prints one line and exits with
status 0, or names the failing probe and exits with status 1. CI runs it
from the static Linux binary in an image built `FROM scratch`
([ADR 0010](../../docs/design/0010-packaging.md), drift guard 5).

### host

`windmill-lite host --bundle <dir> --workdir <dir>` is the engine side of
host mode ([ADR 0004](../../docs/design/0004-harness-protocol.md),
[ADR 0009](../../docs/design/0009-host-adapters.md)). It is not run by
hand: a host such as the Bun host SDK
([harnesses/bun](../../harnesses/bun/README.md)) starts it as its child,
with its socket as file descriptor 3 and, as file descriptor 4, the
channel on which the host announces its steps' child process groups
(`child` frames, WR-55), which the engine reads on its own
(`harness.HostChildren`, taken before the socket, so that a host that
passes no file descriptor 4 still works). It opens the bundle directory,
which the host loads too, with `wl.Open` in host mode
(`wl.Options.Host`): the handshake checks that the host greeting it is
its parent process and that `--workdir`, the directory the host passed,
is a directory it can write and the working directory the host's hello
names (else it refuses the host with `workdir_not_writable` or
`workdir_mismatch`), and a bundle that does not open, or files that do
not read, refuse the host with `engine_refused` and the reason. It then
serves the host's runs, whose steps run in the host, until the host's
connection ends, SIGTERM or SIGINT ([wl](../../wl/README.md), host mode;
[internal/harness](../../internal/harness/README.md)), and exits 0.

| Flag | Meaning |
|---|---|
| `--bundle` | the bundle directory; required |
| `--workdir` | the working directory the host passed; required |
| `--kill-after` | the watchdog's threshold of silence from the host (default 30 s; `0` disables it) |
| `-data` | the data directory the runtime writes into; a temporary one, removed afterwards, by default |
| `-digest` | the bundle digest the bundle must have |
| `-variables`, `-resources` | the host's variables and resources, as for `run` |
| `-plugin-timeout` | how long a `host_call` to the host's plugin waits for its reply (default 1m; F2c) |
| `-read-cache` | give each run a read cache, deviation D21 of [ADR 0001](../../docs/design/0001-equivalence-contract.md#5-named-deviations): each resource and variable is read once per run (`wl.Options.ReadCache`, the host SDK's `readCache`) |
| `-cpuprofile` | write a CPU profile of the engine to this file until the host's connection ends ([Profiling](#profiling)) |

### Profiling

`-cpuprofile <file>` on `run` and `host` writes a CPU profile of the
engine process in Go's pprof format, which `go tool pprof` reads. It
covers the engine only: the Bun harness or the host is another process.
`run` writes the profile when the run ends. `host` writes it when the
host's connection ends or the engine gets SIGTERM or SIGINT.

A Bun host passes the flag through the SDK's `engineFlags`:

```ts
await connect({ engine, bundle, workdir, engineFlags: ["-cpuprofile", "/tmp/engine.pprof"] });
```

```sh
go tool pprof -top /tmp/engine.pprof
```

A profile file that cannot be created fails the command before it opens
the bundle; `host` then refuses the host with the reason.

## Version

The printed version, which `build` records as the engine version and `run`
requires of a bundle, is, in order of preference:

1. the value linked in with `-ldflags "-X main.version=<version>"`;
2. the module version from the binary's build information: the version of
   `go install github.com/invakid404/windmill-lite/cmd/windmill-lite@<version>`,
   or the pseudo-version `go build` stamps from the checkout's commit, with
   `+dirty` when the tree has changes;
3. `(devel)`, for `go run` and test binaries.

## Tests

`main_test.go` covers the version output, the link-time override, the usage
errors, `selfcheck`, and `build` and `verify` end to end: the fixture of
internal/bundle built as a directory and as an archive gives one digest,
which `verify` confirms against the release manifest, and a changed file
fails verification. The build test needs the pinned Bun in `WL_BUN` and
skips without it, unless `WL_REQUIRE_BUN` is set, as CI sets it.

`runcmd_test.go` covers `run`: its usage errors, and arguments, a bundle
or a CPU profile file that do not open, which exit with status 1 and print
nothing on standard output.

With the pinned Bun, `runcmd_test.go` builds a synthetic folder as an
archive whose flow reads a host's secret through argument resolution, and
runs it in `warm` and `fresh` mode. A successful run exits 0 with its
result, trace summary and CPU profile; a failing one exits 1 with its
error; a run without the bundle's harness runtime does not start.

`host_test.go` runs host mode end to end: the host-mode half of W1's exit
gate, the host-mode gate of milestone C2b, F3a's exit gate, F3h's
host-mode case, and F3b's host-mode cases (the never-settling-step,
watchdog, `watchdog/finite-cpu` and resource-release tests and the H11
policy case, on a compiled host under a supervisor that only restarts it
on exit; the flow-suite half of F3b's gate is `cmd/wltest`'s).

`testdata/hostfolder/` is a synthetic sync folder with its host contract
(`contract.yaml`), which the test builds once with `bundle.Build`. Its
host-provided modules are:

- `f/host/scopes.ts`, which holds the tenant scope;
- `f/host/pool.ts`, a pool of leases that the host releases at a run's
  `onSettled`;
- `../hostlib/tenant.ts`, outside the sync root: `testdata/hostlib/` is
  the `relative_path` of a codebases entry whose script
  `f/hostout/read.ts` imports the module from there;
- `f/host/lazy/a.ts` and `f/host/lazy/b.ts`: `a.ts` imports `b.ts` while it
  loads, and `b.ts` imports `a.ts` back only from a function.

`testdata/host/host.ts` is a Bun host that embeds the SDK, registers its
instances of these modules, connects the engine on the bundle, and serves
its callers over HTTP on a Unix socket. It submits each run inside the
caller's tenant scope, in `scopes.ts` and in `tenant.ts`.

The test compiles the host with `bun build --compile`, and its engine is
the test binary itself (`TestMain` runs the command when `WL_TEST_ENGINE`
is set). A supervisor that only restarts the host when it exits runs it.
The tests:

| Test | What it shows |
|---|---|
| `TestHostScopesThroughAHostProvidedModule` | 16 concurrent runs, each in its own tenant scope, read their scope through the host-provided module, by the shim the build generated, 16 of 16 (H11); a separately bundled copy of the module reads nothing |
| `TestHostScopesThroughACodebaseScript` | C2b's gate: `f/hostcb/cb_scope.ts`, the CommonJS codebase script of the folder, its import of `f/host/scopes.ts` resolved to the module's generated shim, reads the host's scope in 16 of 16 concurrent runs on Bun's CommonJS loader (D12, D13), and reads `Bun.env` through the env rewrite |
| `TestHostScopesThroughAnOutsideModule` | F3h's host-mode case: `f/hostout/read.ts`, a codebase script, reads the host's scope through `../hostlib/tenant.ts`, a host-provided module outside the sync folder, in 16 of 16 concurrent runs |
| `TestHostCodebaseAsset` | F3a's gate: the codebase script `f/hostcb/cb_asset.ts` reads its asset, which the SDK extracted into the host's working directory, by a relative path and beside its module, and reads the `args.json` the engine wrote there |
| `TestHostWorkdirRefusedBySDK` | F3a's gate: the SDK refuses to connect with `workdir_mismatch` for another directory than the host's own, and `workdir_not_writable` for a read-only one |
| `TestHostWorkdirRefusedByEngine` | F3a's gate, from the engine's side: the test plays the host, starts `windmill-lite host` with a socketpair as file descriptor 3 (and none as 4) and greets it; the engine welcomes a host in its own working directory, refuses one whose hello names another (`workdir_mismatch`) or a read-only one (`workdir_not_writable`), and refuses with `engine_refused` when the bundle does not open; a host whose plugin declaration has a malformed selector is refused with `plugin_mismatch`, and one with a usable declaration welcomed (F2c) |
| `TestHostDrain` | `drain` refuses a new run (`draining`), waits for the run in flight, which completes, and the host lives on |
| `TestHostDrainAfterALargeOutcome` | a drain that starts while a run with a 3 MiB outcome is in flight (`f/host/big`) ends only after that run's caller has its whole outcome |
| `TestHostRunSettled` | a step that its timeout abandons runs on: the run's outcome comes at about 1 s and its `onSettled` at about 1.5 s, in the run's own tenant scope; a run that abandons nothing settles with its outcome |
| `TestHostScriptRun` | a root script runs as a script job; a run of no target is refused |
| `TestHostLazyImportCycle` | two host-provided modules whose cycle closes only through an `import()` inside a function build, and the root script `f/host/lazy_cycle.ts` reads both through their shims: it gets `["ab", "ab"]` from the host's instances, and each module loaded once in the process |
| `TestHostFacade` | a step reads a variable of the bundle through the job API at the `BASE_URL` its invocation carries |
| `TestHostRecycleModules` | the modules recycle trigger trips once a second module loads, in `health()` and `onRecycle` |
| `TestHostRecycleRetired` | F3b: the retired-entry recycle trigger trips once the host's count of pooled entries retired and not released passes its maximum, in `health()` and `onRecycle`; the host lives on |
| `TestHostPluginCases` | The cases of `conformance/plugins/hooks.yaml` in host mode, answered over `host_call` by the host's case plugin (from `WL_PLUGIN_CASES`). Each run carries its own hooks and budgets in its run frame (`caseRunBody`) and gives its case's `expect`; a case's later runs share the connection, and every hook runs in the run's tenant scope (H11). The plugin's report file (`WL_PLUGIN_REPORT`) shows every run settling after its hooks. A step that waits for `wl.signal()` alone settles once the frame's deadline aborts it. |
| `TestHostSurvivesADetachedRejection` | one detached rejection does not end the host; it is counted in health |
| `TestHostNeverSettlingStep` | ADR 0004's never-settling-step test: after one run's step times out, the other run's next step completes, a new run succeeds, and health reports one abandoned invocation; the host is never restarted |
| `TestHostChildUnderBackpressure` | a step backs up the host's socket, starts a child and blocks the event loop; its timeout still kills the child, announced on the child channel (WR-55) |
| `TestHostWatchdog` | ADR 0004's watchdog test: a step that spins forever, with a second run active; the engine kills the host within `killAfter` plus one second, naming both runs, the second run's caller sees the exit, and a run on the restarted host succeeds |
| `TestHostWatchdogFiniteCPU` | F3b: case `watchdog/finite-cpu` in host mode: a step computes synchronously for 2 s with `killAfter` 1 s and a step timeout of 10 s; the engine kills the host after about 1 s, a second run's caller sees the exit, and a run on the restarted host succeeds; with the watchdog off the step completes on the first generation |
| `TestHostResourceRelease` | F3b: ADR 0004's resource-release test: a step holds a lease of the host's pool (the host-provided module `f/host/pool.ts`) and uses it after its timeout abandons it; the host releases the run's leases at its `onSettled`, which comes only after the step's last use |
| `TestHostVersionPairing` | F3b: the SDK–engine version pairing (ADR 0009): a host whose SDK is stamped with another release (`bun run --define WL_RELEASE`) is refused with `version_mismatch` by the engine (stamped as `-X main.version` would, `WL_TEST_ENGINE_VERSION`), the SDK refuses a welcome from an engine of another release that does not check (`fakeEngine`), and the engine's own release is welcomed and runs |
| `TestHostStaticIntercept` | H1's static form through the run frame |
| `TestHostStepLatency` | reports the per-step latency of host mode |
| `TestHostReadCacheOption` | the SDK's `readCache` reaches the engine: the run lists deviation D21, and without the option it does not |
| `TestHostCPUProfile` | `-cpuprofile`, passed among the SDK's engine flags, writes a whole CPU profile of the engine once the host exits, and none before |

They need the pinned Bun in `WL_BUN` too, and the build helper's
dependencies.

`TestHostRunHalts` runs a second Bun host, `testdata/halthost/host.ts`,
with `bun run`, on a bundle of `testdata/haltfolder`. On one connection,
the host runs the two-step flow `f/repro/prefix` with `haltAfter` its
first module, then without, and counts the modules that end in each run
with its plugin's `onModuleEnd`. The first run ends `halted` after one
module, and the second succeeds after two. Runs with a malformed halt, or
with a halt option that is not a list, are refused, and the next run
succeeds.

`memory_test.go` measures what warm step modules cost a host process: the
evidence of milestone F8a's record,
[ADR 0013](../../docs/design/0013-shared-module-code.md).
`testdata/memhost/host.ts` is a Bun host that embeds the SDK, registers the
host-provided modules `WL_HOST_MODULES` names, connects the engine, and
answers over HTTP on a Unix socket with its resident set after a full
garbage collection and its peak (`/rss`), and with the outcome of runs it
submits, concurrently and repeatedly (`/run`). The tests compile it with
`bun build --compile` and run it under `host_test.go`'s supervisor, on
bundles that `bundle.Build` builds from sync folders the tests generate,
each in three layouts:

- **per-script**: each step's bundle inlines its own copy of the shared
  module, as the build does today;
- **shared-instance**: the shared module is host-provided (D12), so the
  host's one instance serves every step;
- **shared-code**: ADR 0013's decision as the build makes it
  (`Config.SharedCode`): one closure per build group,
  compiled once per process, and an instance of it per step module. A
  build that leaves any script on its per-script bundle
  (`shared_code_fallback`) fails the test.

| Test | What it shows |
|---|---|
| `TestModuleMemory` | ADR 0013's public synthetic reproduction: six Bun scripts that each import one generated module of 40,000 distinct strings, a one-step flow and a six-step flow. Per script, each further module costs at least 3 MiB; with one shared instance or with shared code, at most 1.5 MiB. Each step counts its calls in the module's state: per script and with shared code every step counts its own, as on Windmill, and with one shared instance they all count on one |
| `TestModuleMemoryClosure` | Only with `WL_MEMORY_PROBE` set: the closure shaped like an embedder's, 34 CommonJS codebase scripts of about 6.9 MB each that import one module outside the sync folder, over mongoose, `windmill-client`, js-yaml, Babel's parser and types (pinned by `testdata/memory/closure.package.json` and `closure.bun.lock`, which it installs, so it needs the network once) and a generated package shaped like an API client. It reports the resident set with one module, with all 34, with eight concurrent runs and after a soak of 1,000 runs; with shared code, the soak's peak must stay within the embedder's budget of 650 MiB |

In `TestModuleMemoryClosure`, the shared module imports `node:async_hooks`
by `import()`, and the generated package imports `node:fs/promises` by
`import()`, as published packages do. Both are modules the runtime has
built in, so every script keeps its shared layout.

With shared code, both tests assert the memory gate of [ADR 0013](../../docs/design/0013-shared-module-code.md).
`TestModuleMemory` allows at most 1.5 MiB per further module.
`TestModuleMemoryClosure` allows at most 16 MiB per further module, and a
peak of at most 650 MiB after its soak.

## Benchmarks

`bench_test.go` holds the end-to-end benchmarks of the step boundary.
Each runs synthetic flows with the engine in the benchmark's own process,
so that it reads every run's trace and the engine's CPU time. The
recorded baseline, with its machine, command and load, is in
[ADR 0005](../../docs/design/0005-json-step-boundaries.md#baseline).

The benchmarks run on two topologies:

- **host**: `testdata/benchhost/host.ts`, compiled with `bun build
  --compile`, is a warm Bun host that embeds the SDK and does nothing else.
  The SDK starts its engine as its child, which is the test binary as an
  engine proxy: it hands the host's socket and child channel to the
  benchmark. The runtime is the one `host` opens, with the watchdog off.
- **subprocess**: the warm Bun harness that the engine spawns, one process,
  as `run` uses it.
- **host-plugin**: the same host with a plugin
  (`WL_BENCH_PLUGIN=1`), as a host that keeps its resources elsewhere
  runs. Its `resource` and `variable` hooks answer synthetic resources,
  those under `f/bench/slow/` after 1 ms, and count their calls by run and
  path; its `onRunEnd` hook takes every run's trace. `WL_BENCH_TRACE`
  makes the hook declare a `summary` or `none` instead.

| Benchmark | What it runs |
|---|---|
| `BenchmarkStepBoundary` | `f/bench/chain`, eight steps that each return their argument, on both topologies, one run at a time and eight at once. The payloads are those of [internal/bench](../../internal/bench/README.md), from 26 B to 4 MiB. The first step takes the flow input's `p` and the others `previous_result`, which run no expression |
| `BenchmarkHostShapes` | Flows shaped like a host-mode embedder's, in host mode: 50 to 600 steps and results of 1 to 50 KiB. Except in the bare shape, each step writes up to 3 log lines, makes up to 2 variable or resource reads, and evaluates an expression that binds the flow input. One shape runs eight at once |
| `BenchmarkHostShapes`, the `pathsA` shape | 50 bare steps that each read three member paths over JSON values: `flow_input.cfg.region`, `previous_result.data.items[0].id` and `results.s<k>?.n` |
| `BenchmarkHostShapes`, the `resA` shapes | On the plugin host, 50 steps that each take two of 4 resources as `$res:` arguments, the previous result, an expression that binds the flow input, and the previous step's member `results.s<k>.n` |

One iteration is one run per worker, the workers started together, so
even one iteration has every run of its concurrency in flight at once.

### Stages

Each step is split into stages, each the difference of two readings of the
same invocation. The readings are the trace's module and harness spans,
the engine side's clock
([internal/harness](../../internal/harness/README.md#the-times-hook)) and
the harness's own
([harnesses/bun](../../harnesses/bun/README.md#benchmark-timings)). They are
joined by the invocation's id, the step's job id. A step's stages add up
to its module span exactly, so their means add up to the step's mean.

| Stage | What it holds |
|---|---|
| `engine` | the engine outside its call of the executor: transforms, arguments and their storage, the trace |
| `executor` | the executor around the harness process: the runtime's recorder and the pool's routing |
| `go-encode` | checks, the script's arguments and the invoke's payload |
| `args-file` | `args.json` written, the wait for its lock included |
| `go-queue` | the invocation registered and its invoke queued |
| `transport` | from the invoke queued to the result read, less the harness's time: the writer, the socket both ways and the wake-ups. It is not attributed further |
| `bun-decode` | the invoke's last frame decoded in the harness |
| `bun-dispatch` | from decoded to the entrypoint called: context, module, binding, and any wait behind other invocations |
| `code` | the step's own code |
| `bun-serialise` | the result serialised |
| `bun-send` | the result frame encoded and handed to the socket |
| `go-decode` | the result's last frame decoded in the engine |
| `go-handoff` | from decoded to the outcome back with the engine: settlement and the `Reporter`'s calls |

### Metrics

Each benchmark reports these figures per step:

| Metric | Meaning |
|---|---|
| `ns/op`, `MB/s` | wall time per step at the benchmark's concurrency, and the payload the steps moved, both ways, per second |
| `step-p50-ns`, `step-p95-ns`, `step-mean-ns` | the step's module span |
| `<stage>-mean-ns`, `<stage>-p50-ns` | each stage, over every step |
| `boundary-share-p50`, `boundary-share` | the share of a run's module time spent at the boundary: its harness spans less its steps' own code, per run at p50, and over every run together |
| `harness-share-p50`, `harness-share` | the same for the whole harness span |
| `module-share-p50` | the share of a run's elapsed time spent in its modules |
| `engine-cpu-ns/step`, `engine-sys-ns/step` | the engine process's CPU time, and its system part, from `getrusage` |
| `host-cpu-ns/step` | the host's or harness's CPU time, from `process.cpuUsage()` |
| `engine-cpu-p50-ns/step` and the like | per run, for runs one at a time; a shape's host CPU too |
| `expr-ns/step` | the expression spans |
| `host-calls/run` | on the plugin host, the calls of its `resource` and `variable` hooks per run |
| `host-peak-rss-mb` | the host's or harness's peak resident set after the runs, in MiB |
| `conc-peak`, `conc-mean` | the most runs in flight at once, and the mean |
| `load1` | the one-minute load average when the runs started |

Percentiles are nearest-rank ([internal/bench](../../internal/bench/README.md#percentiles)).
The probe step `f/bench/probe` reads the host's or harness's CPU time
before and after the runs, its peak resident set and the plugin's call
counts, and sets and drains the harness's timings. A
shape's last step reports the host's CPU time with its result too, so each
run has its own figure.

`WL_BENCH_TIMINGS=off` leaves the stage readings out, which measures what
taking them costs. `TestBenchSplit` runs the measurement once on each
topology with two runs at once, so that a broken split fails the ordinary
tests. It checks that both runs overlap, that every step is matched with
its readings, and that its stages add up to its module span.

They need the build helper's dependencies and the pinned Bun in `WL_BUN`,
and run only with `-bench`. A benchmark fails without `WL_BUN`, rather
than skip and report a pass with no figures, while `TestBenchSplit`
skips. The baseline's exact commands, each part of
`BenchmarkStepBoundary` in a test process of its own, are in
[ADR 0005](../../docs/design/0005-json-step-boundaries.md#baseline). One
run of both, in one process:

```sh
WL_BUN=/path/to/bun-1.3.10/bun go test ./cmd/windmill-lite -run '^$' \
  -bench 'BenchmarkStepBoundary|BenchmarkHostShapes' -benchtime 3s -count 3
```

`benchstat` (`golang.org/x/perf/cmd/benchstat`) compares two such outputs.

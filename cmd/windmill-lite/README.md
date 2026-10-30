# cmd/windmill-lite

**Current state:** milestones F4, F1 and F3a of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md). This README
covers the `windmill-lite` command in this directory and its `testdata/`.
The command prints its version, builds a bundle from a Windmill sync
folder, verifies a bundle, runs a bundle's flow or script through the
[wl](../../wl/README.md) library (F1), checks that its expression engine
starts, and serves host mode on a built bundle through `wl` (F3a). The commands that serve a bundle (`serve`), pack it
(`pack`, `image`) or generate clients (`gen`) do not exist yet.

## Usage

```sh
go run ./cmd/windmill-lite            # windmill-lite (devel)
go run ./cmd/windmill-lite version    # same; --version, -version and -v also work
windmill-lite build <folder> -o <bundle> [flags]
windmill-lite verify <bundle> [-manifest <release manifest>] [-digest <digest>]
windmill-lite run <path> --bundle <bundle> [--args <json|@file>] [flags]
windmill-lite selfcheck
windmill-lite host --bundle <dir> --workdir <dir> [--kill-after <duration>] [flags]
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

`runcmd_test.go` covers `run`: its usage errors, and arguments or a bundle
that do not open, which exit with status 1 and print nothing on standard
output; and, with the pinned Bun, a synthetic folder built as an archive
whose flow reads a host's secret through argument resolution, run in
`warm` and `fresh` mode: a successful run exits 0 with its result and
trace summary, a failing one exits 1 with its error, and a run without the
harness runtime the bundle was built for does not start.

`host_test.go` runs host mode end to end: the host-mode half of W1's exit
gate, the host-mode gate of milestone C2b, and F3a's exit gate.
`testdata/hostfolder/` is a synthetic sync folder with its host contract
(`contract.yaml`), which the test builds once with `bundle.Build`, and
whose host-provided module is `f/host/scopes.ts`.
`testdata/host/host.ts` is a Bun host that embeds the SDK, registers its
instance of that module, connects the engine on the bundle, and serves its
callers over HTTP on a Unix socket. The test compiles it with
`bun build --compile`, and its engine is the test binary itself
(`TestMain` runs the command when `WL_TEST_ENGINE` is set). A supervisor
that only restarts the host when it exits runs it. The tests:

| Test | What it shows |
|---|---|
| `TestHostScopesThroughAHostProvidedModule` | 16 concurrent runs, each in its own tenant scope, read their scope through the host-provided module, by the shim the build generated, 16 of 16 (H11); a separately bundled copy of the module reads nothing |
| `TestHostScopesThroughACodebaseScript` | C2b's gate: `f/hostcb/cb_scope.ts`, the CommonJS codebase script of the folder, its import of `f/host/scopes.ts` resolved to the module's generated shim, reads the host's scope in 16 of 16 concurrent runs on Bun's CommonJS loader (D12, D13), and reads `Bun.env` through the env rewrite |
| `TestHostCodebaseAsset` | F3a's gate: the codebase script `f/hostcb/cb_asset.ts` reads its asset, which the SDK extracted into the host's working directory, by a relative path and beside its module, and reads the `args.json` the engine wrote there |
| `TestHostWorkdirRefusedBySDK` | F3a's gate: the SDK refuses to connect with `workdir_mismatch` for another directory than the host's own, and `workdir_not_writable` for a read-only one |
| `TestHostWorkdirRefusedByEngine` | F3a's gate, from the engine's side: the test plays the host, starts `windmill-lite host` with a socketpair as file descriptor 3 (and none as 4) and greets it; the engine welcomes a host in its own working directory, refuses one whose hello names another (`workdir_mismatch`) or a read-only one (`workdir_not_writable`), and refuses with `engine_refused` when the bundle does not open; a host whose plugin declaration has a malformed selector is refused with `plugin_mismatch`, and one with a usable declaration welcomed (F2c) |
| `TestHostDrain` | `drain` refuses a new run (`draining`), waits for the run in flight, which completes, and the host lives on |
| `TestHostDrainAfterALargeOutcome` | a drain that starts while a run with a 3 MiB outcome is in flight (`f/host/big`) ends only after that run's caller has its whole outcome |
| `TestHostRunSettled` | a step that its timeout abandons runs on: the run's outcome comes at about 1 s and its `onSettled` at about 1.5 s, in the run's own tenant scope; a run that abandons nothing settles with its outcome |
| `TestHostScriptRun` | a root script runs as a script job; a run of no target is refused |
| `TestHostFacade` | a step reads a variable of the bundle through the job API at the `BASE_URL` its invocation carries |
| `TestHostRecycleModules` | the modules recycle trigger trips once a second module loads, in `health()` and `onRecycle` |
| `TestHostPluginCases` | F2c's gate in host mode: the cases of `conformance/plugins/hooks.yaml` on a bundle built from them, answered by the host's TypeScript plugin (the case plugin, from `WL_PLUGIN_CASES`) over `host_call`, each with its run's budgets and latches in the run frame (`caseRunBody`) and its expected status, `may_have_mutated`, result, latched error, log, warnings and deviations; every hook in the run's own tenant scope (H11); the plugin's `onRunSettled`; and a step that waits for `wl.signal()` alone settling once the run frame's deadline has aborted it |
| `TestHostSurvivesADetachedRejection` | one detached rejection does not end the host; it is counted in health |
| `TestHostNeverSettlingStep` | ADR 0004's never-settling-step test: after one run's step times out, the other run's next step completes, a new run succeeds, and health reports one abandoned invocation; the host is never restarted |
| `TestHostChildUnderBackpressure` | a step backs up the host's socket, starts a child and blocks the event loop; its timeout still kills the child, announced on the child channel (WR-55) |
| `TestHostWatchdog` | ADR 0004's watchdog test: a step that spins forever, with a second run active; the engine kills the host within `killAfter` plus one second, naming both runs, the second run's caller sees the exit, and a run on the restarted host succeeds |
| `TestHostStaticIntercept` | H1's static form through the run frame |
| `TestHostStepLatency` | reports the per-step latency of host mode |

They need the pinned Bun in `WL_BUN` too, and the build helper's
dependencies.

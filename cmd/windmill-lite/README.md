# cmd/windmill-lite

**Current state:** milestone F4 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md), with the
walking skeleton's host command (milestone W1). This README covers the
`windmill-lite` command in this directory and its `testdata/`. The command
prints its version, builds a bundle from a Windmill sync folder, verifies a
bundle, checks that its expression engine starts, and serves host mode on
the walking skeleton's layout, whose flows the engine core runs
(milestone N1a). The commands that run a bundle (`run`, `serve`), pack
it (`pack`, `image`) or generate clients (`gen`) do not exist yet.

## Usage

```sh
go run ./cmd/windmill-lite            # windmill-lite (devel)
go run ./cmd/windmill-lite version    # same; --version, -version and -v also work
windmill-lite build <folder> -o <bundle> [flags]
windmill-lite verify <bundle> [-manifest <release manifest>] [-digest <digest>]
windmill-lite selfcheck
windmill-lite host --bundle <dir> [--kill-after <duration>]
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

`windmill-lite host --bundle <dir> [--kill-after <duration>]` is the engine
side of host mode ([ADR 0004](../../docs/design/0004-harness-protocol.md),
[ADR 0009](../../docs/design/0009-host-adapters.md)). It is not run by
hand: the Bun host SDK
([harnesses/bun](../../harnesses/bun/README.md)) starts it as its child,
with its socket as file descriptor 3. It loads the flows of the walking
skeleton's test layout (`flows/<path>.flow.json`, not a built bundle) with
`engine.Skeleton`, which runs them on the engine core
([internal/engine](../../internal/engine/README.md)), checks that the host
greeting it is its parent process, and serves the host's runs, whose steps
run in the host, until the host's connection ends
([internal/harness](../../internal/harness/README.md), host mode).
`--kill-after` is the watchdog's threshold of silence from the host
(default 30 s; `0` disables it). Milestone F3a replaces this command's
loader with host mode on the `wl` library and its bundles.

## Version

The printed version, which `build` records as the engine version, is, in
order of preference:

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

`host_test.go` runs host mode end to end: the host-mode half of W1's exit
gate, and the host-mode gate of milestone C2b. `testdata/host/host.ts` is a Bun host that embeds the SDK, registers
its instance of the host-provided module `testdata/host/scopes.ts` as
`f/w1/scopes.ts`, and serves its callers over HTTP on a Unix socket. The
test compiles it with `bun build --compile`, and its engine is the test
binary itself (`TestMain` runs the command when `WL_TEST_ENGINE` is set),
on the walking skeleton's layout `internal/engine/testdata/w1`. A supervisor
that only restarts the host when it exits runs it. The tests:

| Test | What it shows |
|---|---|
| `TestHostScopesThroughAHostProvidedModule` | 16 concurrent runs, each in its own tenant scope, read their scope through the host-provided module 16 of 16 (H11); a separately bundled copy of the module reads nothing |
| `TestHostScopesThroughACodebaseScript` | C2b's gate: `testdata/codebase/cb_scope.ts`, built by `bundle.BuildCodebase` as the CommonJS codebase script of a synthetic folder, its import of `f/w1/scopes.ts` resolved to the module's generated shim, and added with a one-step flow to a copy of the W1 layout, reads the host's scope in 16 of 16 concurrent runs on Bun's CommonJS loader (D12, D13), and reads `Bun.env` through the env rewrite |
| `TestHostSurvivesADetachedRejection` | one detached rejection does not end the host; it is counted in health |
| `TestHostNeverSettlingStep` | ADR 0004's never-settling-step test: after one run's step times out, the other run's next step completes, a new run succeeds, and health reports one abandoned invocation; the host is never restarted |
| `TestHostWatchdog` | ADR 0004's watchdog test: a step that spins forever, with a second run active; the engine kills the host within `killAfter` plus one second, naming both runs, the second run's caller sees the exit, and a run on the restarted host succeeds |
| `TestHostStaticIntercept` | H1's static form through the run frame |
| `TestHostStepLatency` | reports the per-step latency of host mode |

They need the pinned Bun in `WL_BUN` too.

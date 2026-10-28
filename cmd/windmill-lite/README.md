# cmd/windmill-lite

**Current state:** prints its version, and serves host mode with the
walking skeleton (milestone W1 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md)). This README
covers the `windmill-lite` command in this directory and its `testdata/`.
The commands that build a Windmill folder and run its flows (`build`,
`run`, and the others of ADR 0012) do not exist yet.

## Usage

```sh
go run ./cmd/windmill-lite            # windmill-lite (devel)
go run ./cmd/windmill-lite version    # same; --version, -version and -v also work
```

`windmill-lite host --bundle <dir> [--kill-after <duration>]` is the engine
side of host mode ([ADR 0004](../../docs/design/0004-harness-protocol.md),
[ADR 0009](../../docs/design/0009-host-adapters.md)). It is not run by
hand: the Bun host SDK
([harnesses/bun](../../harnesses/bun/README.md)) starts it as its child,
with its socket as file descriptor 3. It loads the bundle's flows into the
walking skeleton's interpreter
([internal/engine](../../internal/engine/README.md)), checks that the host
greeting it is its parent process, and serves the host's runs, whose steps
run in the host, until the host's connection ends
([internal/harness](../../internal/harness/README.md), host mode).
`--kill-after` is the watchdog's threshold of silence from the host
(default 30 s; `0` disables it). Milestone F3a replaces this command's
skeleton with host mode on the `wl` library.

Any other argument prints the usage to standard error and exits with
status 2.

## Version

The printed version is, in order of preference:

1. the value linked in with `-ldflags "-X main.version=<version>"`;
2. the module version from the binary's build information, for example when
   it is built with `go install github.com/invakid404/windmill-lite/cmd/windmill-lite@<version>`;
3. `(devel)`.

## Tests

`main_test.go` covers the version output, the link-time override and the
usage error.

`host_test.go` runs host mode end to end, the host-mode half of W1's exit
gate. `testdata/host/host.ts` is a Bun host that embeds the SDK, registers
its instance of the host-provided module `testdata/host/scopes.ts` as
`f/w1/scopes.ts`, and serves its callers over HTTP on a Unix socket. The
test compiles it with `bun build --compile`, and its engine is the test
binary itself (`TestMain` runs the command when `WL_TEST_ENGINE` is set),
on the bundle `internal/engine/testdata/w1`. A supervisor that only
restarts the host when it exits runs it. The tests:

| Test | What it shows |
|---|---|
| `TestHostScopesThroughAHostProvidedModule` | 16 concurrent runs, each in its own tenant scope, read their scope through the host-provided module 16 of 16 (H11); a separately bundled copy of the module reads nothing |
| `TestHostSurvivesADetachedRejection` | one detached rejection does not end the host; it is counted in health |
| `TestHostNeverSettlingStep` | ADR 0004's never-settling-step test: after one run's step times out, the other run's next step completes, a new run succeeds, and health reports one abandoned invocation; the host is never restarted |
| `TestHostWatchdog` | ADR 0004's watchdog test: a step that spins forever, with a second run active; the engine kills the host within `killAfter` plus one second, naming both runs, the second run's caller sees the exit, and a run on the restarted host succeeds |
| `TestHostStaticIntercept` | H1's static form through the run frame |
| `TestHostStepLatency` | reports the per-step latency of host mode |

They need the pinned Bun: set `WL_BUN` to its executable. Without it they
skip, unless `WL_REQUIRE_BUN` is set, as CI sets it.

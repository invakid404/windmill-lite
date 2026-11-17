# Continuous integration

This guide says which GitHub Actions workflows check windmill-lite, when
each runs, what each job checks, and how to read a failure. It is for
contributors. The workflows are in [.github/workflows](../.github/workflows).

The guide uses project terms in their [glossary](glossary.md) meaning:
[Bun](glossary.md#bun), the [bundle](glossary.md#bundle), the
[engine](glossary.md#engine), an [expression](glossary.md#expression), the
[harness](glossary.md#harness), [host mode](glossary.md#host-mode), the
[oracle](glossary.md#oracle) and a [fixture](glossary.md#fixture).

## Required checks

Branch protection on `main` requires these five checks. A pull request
needs all five to pass before it merges.

| Check | Workflow | What it checks |
|---|---|---|
| `Go (ubuntu-latest)` | [CI](../.github/workflows/ci.yml) | Formatting, `go.mod`, `go vet`, the full test suite and the other steps of [The Go jobs](#the-go-jobs), on Linux |
| `Go (macos-latest)` | [CI](../.github/workflows/ci.yml) | That every macOS part of the Go job passed ([The macOS parts](#the-macos-parts)) |
| `Expression engine (ubuntu-24.04)` | [CI](../.github/workflows/ci.yml) | The expression engine, built with the C compiler of that image ([The expression engine jobs](#the-expression-engine-jobs)) |
| `Expression engine (ubuntu-26.04)` | [CI](../.github/workflows/ci.yml) | The same, on `ubuntu-26.04` |
| `memoria` | [Memoria documentation](../.github/workflows/memoria.yml) | That every README is acknowledged against its current scope ([docs/memoria.md](memoria.md)) |

Every other check reports, and does not block a merge.

## Workflows

| Workflow | File | When it runs |
|---|---|---|
| CI | [ci.yml](../.github/workflows/ci.yml) | Each pull request, and each push to `main` |
| Memoria documentation | [memoria.yml](../.github/workflows/memoria.yml) | Each pull request, and each push |
| Codebase parity | [parity.yml](../.github/workflows/parity.yml) | Each pull request, each push to `main`, and by hand |
| Oracle | [oracle.yml](../.github/workflows/oracle.yml) | Nightly at 03:17 UTC, by hand, and on some pull requests ([Oracle](#oracle)) |
| Release | [release.yml](../.github/workflows/release.yml) | A pushed tag `v*`, by hand, and on some pull requests ([Release](#release)) |
| Benchmarks | [bench.yml](../.github/workflows/bench.yml) | Only by hand |
| Memoria Auto-Ack on Renovate | [memoria-auto-ack.yml](../.github/workflows/memoria-auto-ack.yml) | Each pull request to `main`. Its job runs only for Renovate's branches. |

In CI, Codebase parity and Oracle, a newer push to the same branch cancels
the run in progress.

## The CI workflow

| Job | Runner | What it checks | Required |
|---|---|---|---|
| `Go (ubuntu-latest)` | `ubuntu-latest` | Every step of [The Go jobs](#the-go-jobs) | Yes |
| `Go (ubuntu-latest, QuickJS only)` | `ubuntu-latest` | `internal/expr` and `cmd/wltest` again, with the Go evaluator of expressions off (`WL_TEST_QUICKJS_ONLY=1`), so that every case passes on QuickJS-ng alone | No |
| `Go (macos-latest, packages)` and two flow-suite parts | `macos-latest` | The Go job's steps on macOS, in three parts ([The macOS parts](#the-macos-parts)) | Through `Go (macos-latest)` |
| `Go (macos-latest)` | `ubuntu-latest` | That every macOS part passed | Yes |
| `Static Linux engine (amd64)`, `Static Linux engine (arm64)` | `ubuntu-24.04`, `ubuntu-24.04-arm` | [The static Linux engine](#the-static-linux-engine) | No |
| `Expression engine (ubuntu-24.04)`, `Expression engine (ubuntu-26.04)` | Each image by name | [The expression engine jobs](#the-expression-engine-jobs) | Yes |
| `jsonb (PostgreSQL)` | `ubuntu-latest` | The jsonb canonicaliser of [internal/jsonval](../internal/jsonval/README.md) against PostgreSQL 16 in the oracle's pinned image, and the bound on a value's stored size that the job reads use | No |
| `Bun harness checks` | `ubuntu-latest` | `bun test` in `harnesses/bun`: the harness's codec against the frames that the Go tests write, and the build helper's scans | No |
| `Bun SDK types` | `ubuntu-latest` | A type check of the Bun host SDK and the harness runtime as a host that vendors them does it, with the newest and the oldest supported `@types/node` ([type check](../harnesses/bun/README.md#type-check)) | No |
| `Docs (report only)` | `ubuntu-latest` | The countable writing rules on the Markdown that a pull request changes ([What CI checks](writing.md#what-ci-checks)). A warning is an annotation, and never fails the job. | No |
| `Secret scan` | `ubuntu-latest` | Each commit of a pull request, with [gitleaks](https://github.com/gitleaks/gitleaks) 8.30.1 (MIT licence), its default rules and [.gitleaks.toml](../.gitleaks.toml). It runs on pull requests only. | No |

### The Go jobs

Each part of the Go job sets up Go from `go.mod`, Bun 1.4.3 and Node
24.22.1. It downloads `windmill-client@1.862.0` from npm, checks it against
its SHA-512 integrity hash, and installs the build helper's dependencies.

Then `Go (ubuntu-latest)` and `Go (macos-latest, packages)` run all the
steps below. The other parts run only `Test`.

| Step | What it checks |
|---|---|
| `Check formatting` | `gofmt -l .` lists no file |
| `Check go.mod is tidy` | `go mod tidy -diff` shows no change |
| `Vet` | `go vet ./...` |
| `Check that no retarget ledger remains` | `go run ./cmd/wltest ledger`: no retarget ledger and no marker of one exists ([the ledger guard](../cmd/wltest/README.md#the-ledger-guard)) |
| `Test` | `go test -race -timeout 45m` on the part's packages, with `WL_REQUIRE_BUN=1` and `WL_REQUIRE_NODE=1`, so a test that needs Bun, the client or Node fails instead of skipping |
| `Test the expression packages in leak check` | `go test -race ./internal/expr/...` with `WL_QJS_LEAK_CHECK=1`. The engine frees each QuickJS-ng runtime block by block, and the tests fail on a leak. |
| `Build a fixture bundle twice and compare` | Two builds of the fixture bundle, from two copies of the folder, give one digest and the same archive bytes, and `verify` accepts the result |
| `Measure the expression pool under load` | Prints latency percentiles of the pool of QuickJS-ng runtimes that evaluate expressions ([internal/expr/quickjs](../internal/expr/quickjs/README.md)). Report only. |
| `Report the expression budget gate` | Prints the latency of expressions through the whole pipeline, against the bounds of the budget gate ([internal/expr](../internal/expr/README.md#tests-and-cases)). Report only: the bounds hold on the benchmark machine, not on CI's shared runners. |
| `Report the native realms and the process groups` | Runs, by name and with `-v`, the native realm check of [cmd/windmill-lite](../cmd/windmill-lite/README.md#tests) and the tests of the process groups that a job's children join ([WR-55](semantics/languages.md#wr-55)). It fails when one of these tests skips or does not run, which the `Test` step does not show. |
| `Test repository scripts` | `python3 -m unittest discover -s scripts -v`: the tests of the [scripts](../scripts/README.md) |
| `Test the npm package` | The [packaging test](../harnesses/README.md#test-the-package) of the npm package, on Node 22.23.5. Node 22 is the oldest major version that the SDK's test entry supports. |

### The macOS parts

On macOS, the Go tests run as three parallel jobs. Complementary `-run`
and `-skip` patterns split them, so together they run every test that one
`go test ./...` runs:

| Part | What it tests |
|---|---|
| `Go (macos-latest, packages)` | Every package, without the flow suite `TestFlowSuite` |
| `Go (macos-latest, flow suite in warm and fresh mode)` | The modes of `TestFlowSuite` in `cmd/wltest` whose names do not start with `host` |
| `Go (macos-latest, flow suite in host mode)` | The modes of `TestFlowSuite` whose names start with `host` |

The job `Go (macos-latest)` runs after them, on Linux. It passes only when
each part passed. It runs even when a part failed, unless the run was
cancelled.

### The static Linux engine

The released Linux engine is one static binary, which `zig cc` 0.15.2
builds against musl. For `amd64` and `arm64`, this job:

1. builds the engine and the expression test binaries that way;
2. checks that each binary names no program interpreter and needs no
   shared library;
3. runs the expression suite from the static test binaries;
4. reports the expression budget gate from them;
5. starts the engine in an image built `FROM scratch`, with no network, as
   user 65534, and runs `version`, `selfcheck` and `verify` on a bundle that
   it built;
6. compares the expression pool's allocators: mimalloc and the C library's
   own malloc, on musl and on glibc. Report only.

### The expression engine jobs

The C compilers of `ubuntu-24.04` and `ubuntu-26.04` give one JavaScript
call a different C stack, and the engine scales its stack limit to it
([stack limit](../internal/expr/quickjs/README.md#stack-limit)). So the job
runs on each image by name: a move of `ubuntu-latest` cannot drop either
image. On each image it:

1. tests `internal/expr/...`, and `internal/expr` again with
   `WL_TEST_QUICKJS_ONLY=1`;
2. prints the stack per call that the engine measured, and the depths of
   recursion that it reaches;
3. runs the `selfcheck` test;
4. builds the engine with the image's C compiler and runs
   `windmill-lite selfcheck`;
5. builds the static Linux engine and runs `windmill-lite selfcheck`.

## Other workflows

### Memoria documentation

The job `memoria` installs Memoria 0.9.2 on `ubuntu-24.04` and runs
`memoria check`. It fails while a README has a change in its scope that
nobody acknowledged. Memoria generates this workflow: change it only with
`memoria integrations github upgrade --apply`
([docs/memoria.md](memoria.md#ci-gate)).

### Memoria Auto-Ack on Renovate

This workflow acknowledges the README reviews of a Renovate pull request
whose only changes are dependency bumps in `go.mod` and `go.sum`, or
action-pin bumps in workflow files. On any other change it skips, and a
contributor acknowledges by hand
([Renovate bumps](memoria.md#renovate-bumps)).

### Codebase parity

The job `Codebase parity (windmill-cli 1.862.0)` runs the unmodified
`windmill-cli@1.862.0` with `npx` on Node 22.23.5. The CLI pushes
synthetic codebase scripts to a recording HTTP stub. The bundles that it
uploads must equal the bundles that windmill-lite builds with esbuild
(`TestCLIParity` in [internal/bundle](../internal/bundle/README.md)).

It is not a required check. Its fallback, `TestCLIOptionsFollowWR47`,
compares the esbuild settings and runs in the Go jobs.

### Oracle

The [oracle](glossary.md#oracle) workflow records the flow fixtures and the
expression cases on an unmodified Windmill Community Edition `v1.862.0`
image. It fails when an observation differs from the committed
[recording](glossary.md#recording), when a run has no recording, or when
the metadata that the `wmill` CLI generates for a committed sync folder
changes.

On a pull request, it records only when the pull request has the label
`oracle`, or changes one of these paths: `conformance/`,
`internal/oracle/`, `internal/conformance/`, `cmd/wltest/`,
`internal/openflow/`, `internal/yamlx/`, `internal/syncfolder/`,
`.github/workflows/oracle.yml`, `go.mod` or `go.sum`.

It records in seven sessions, side by side. Each session other than
`workers1` and `workers2` records only the fixtures that declare its
configuration.

| Session | Workers | What it records |
|---|---|---|
| `workers1` | 1 | Every fixture and expression case, except those that need two workers or another configuration |
| `workers2` | 2 | The fixtures that need two workers, and the smoke fixture again with `LISTEN_NEW_EVENTS_INTERVAL_SEC=10` |
| `native` | 1 | The native-step fixtures, on a worker that serves the `nativets` tag |
| `long` | 1 | The fixtures whose runs wait a minute or more on the server's own monitors, with a run timeout of 5 minutes |
| `long2` | 2 | The same configuration for the retry-delay fixture `conformance/flows/execution/fx-26-delays`, whose step waits for the flows that it starts, with a run timeout of 9 minutes |
| `result_size` | 1 | The fixtures that need `MAX_RESULT_SIZE_MB=3` |
| `flow_script_off` | 1 | The fixtures that need `DISABLE_FLOW_SCRIPT=true` |

[internal/oracle](../internal/oracle/README.md) and
[cmd/wltest](../cmd/wltest/README.md#mismatched-observations) describe the
recordings and how to read a mismatch.

### Release

The release workflow releases the tag `v<version>`
([RELEASING.md](../RELEASING.md)). It runs as a dry run, which builds and
checks the engines, the npm package and the release assets, but creates no
GitHub Release and publishes nothing to npm:

- when it is started by hand with `dry_run` set, the default;
- on a pull request that changes the workflow, `harnesses/package.json`,
  `harnesses/bun.lock`, `harnesses/npm/` or `scripts/release.py`;
- in a fork.

Its jobs are `Check the version`, `Engine (<os>-<arch>)` for the four
platforms, `npm package`, `Release assets`, `GitHub Release` and
`npm publish`. A dry run skips the last two.

### Benchmarks

The benchmark workflow compares builds of the engine on GitHub's hosted
runners, linux/amd64 and linux/arm64. It runs only when started by hand,
and gates nothing
([comparing builds](benchmarks.md#comparing-builds-on-githubs-runners)).

## Read a failure

1. List the pull request's checks: `gh pr checks <number>`.
2. Open the log of the failed job: `gh run view <run-id> --log-failed`.
3. Find the failed step in the table below.
4. Run its command locally.
5. Fix the cause.
6. Push a new commit.

| Failed check or step | Run locally |
|---|---|
| `Check formatting` | `gofmt -l .`, then `gofmt -w <file>` for each file that it lists |
| `Check go.mod is tidy` | `go mod tidy` |
| `Vet` | `go vet ./...` |
| `Check that no retarget ledger remains` | `go run ./cmd/wltest ledger` |
| `Test`, or `Report the native realms and the process groups` | The failed test, with the variables of [Run the tests locally](#run-the-tests-locally) set |
| `Test repository scripts` | `python3 -m unittest discover -s scripts -v` |
| `Test the npm package` | The packaging test ([harnesses/README.md](../harnesses/README.md#test-the-package)) |
| `memoria` | The per-session flow of [docs/memoria.md](memoria.md#the-per-session-flow). `memoria explain <README.md>` says why a README is pending. |
| `Go (macos-latest)` | Nothing: open the macOS part that failed |
| An annotation of `Docs (report only)` | `python3 scripts/check-docs.py <file>` |

## Run the tests locally

Some tests need Bun 1.4.3, Node, or the unmodified Windmill client
library. When a variable below is not set, each test that needs it skips.
With `WL_REQUIRE_BUN=1` or `WL_REQUIRE_NODE=1` set, as in CI, the test
fails instead.

| Variable | Value | Tests that need it |
|---|---|---|
| `WL_BUN` | The path of a Bun 1.4.3 executable | The harness, build and host-mode tests, in `internal/harness`, `internal/bundle`, `internal/engine`, `internal/platform/...`, `cmd/windmill-lite` and `cmd/wltest` |
| `WL_WINDMILL_CLIENT` | A directory whose `node_modules` holds `windmill-client@1.862.0` | The test that concurrent invocations each see their own `WM_*` environment variables, the test that the [platform facade](../internal/platform/facade/README.md) attributes each call to its step, and the test of the client with shared code in `cmd/wltest` |
| `WL_NODE` | A Node executable. CI uses Node 24.22.1, the Node of Windmill `v1.862.0`'s image. | The step suite's runs of each CommonJS codebase bundle on Node, where Windmill runs it |

To run the tests, from the repository root, in one shell:

1. Point `WL_BUN` at the Bun 1.4.3 executable:
   `export WL_BUN=/path/to/bun-1.4.3/bun`.
2. Install the dependencies of the
   [Bun build helper](../harnesses/bun/build/README.md):
   `(cd harnesses/bun/build && "$WL_BUN" install --frozen-lockfile)`.
3. Make a directory for the client:
   `mkdir -p /path/to/client/node_modules/windmill-client`.
4. Download the client into it:

   ```sh
   curl -fsSL https://registry.npmjs.org/windmill-client/-/windmill-client-1.862.0.tgz \
     | tar -xz -C /path/to/client/node_modules/windmill-client --strip-components=1
   ```

5. Point `WL_WINDMILL_CLIENT` at the directory:
   `export WL_WINDMILL_CLIENT=/path/to/client`.
6. Point `WL_NODE` at a Node executable: `export WL_NODE=/path/to/node`.
7. Run `go test -race ./...`.

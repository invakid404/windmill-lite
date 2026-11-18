# Continuous integration

This guide says which GitHub Actions workflows check windmill-lite, when
each runs, what each job checks, how to read a failure, how to re-run a
check, and how to run a check locally. It is for every contributor whose
pull request runs CI. The workflows are in
[.github/workflows](../.github/workflows).

The guide uses project terms in their [glossary](glossary.md) meaning:
[Bun](glossary.md#bun), the [bundle](glossary.md#bundle), the
[engine](glossary.md#engine), an [expression](glossary.md#expression), the
[harness](glossary.md#harness), [host mode](glossary.md#host-mode), the
[oracle](glossary.md#oracle) and a [fixture](glossary.md#fixture).

## Required checks

Branch protection on `main` requires these five checks. A pull request
needs all five to pass on its latest commit before it merges.

| Check | Workflow | What it checks |
|---|---|---|
| `Go (ubuntu-latest)` | [CI](../.github/workflows/ci.yml) | Formatting, `go.mod`, `go vet`, the full test suite and the other steps of [The Go jobs](#the-go-jobs), on Linux |
| `Go (macos-latest)` | [CI](../.github/workflows/ci.yml) | That every macOS part of the Go job passed ([The macOS parts](#the-macos-parts)) |
| `Expression engine (ubuntu-24.04)` | [CI](../.github/workflows/ci.yml) | The expression engine, built with the C compiler of that image ([The expression engine jobs](#the-expression-engine-jobs)) |
| `Expression engine (ubuntu-26.04)` | [CI](../.github/workflows/ci.yml) | The same, on `ubuntu-26.04` |
| `memoria` | [Memoria documentation](../.github/workflows/memoria.yml) | That every README is acknowledged against its current scope ([docs/memoria.md](memoria.md)) |

- **Prerequisites of a required check.** The three macOS parts of the Go
  job are not required checks themselves, but `Go (macos-latest)` passes
  only when each of them passed, so a failed part blocks a merge.
- **Every other check reports,** and does not block a merge.
- **A skipped job counts as passed.** GitHub reports a required check whose
  job an `if` condition skips as a success
  ([handling skipped but required checks](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/troubleshooting-required-status-checks#handling-skipped-but-required-checks)).
  So `Go (macos-latest)` runs even when a macOS part failed, and fails
  itself.
- **Names matter.** Renaming a required job, or changing the images of the
  `Expression engine` jobs, needs the same change in the branch protection
  settings. A macOS part's name must keep the prefix `Go (macos-latest, `,
  which `Go (macos-latest)` looks for.
- **Up to date is not required.** Branch protection does not require a pull
  request to be up to date with `main` before it merges.

## When each workflow runs

| Workflow | File | Events | What cancels a run |
|---|---|---|---|
| CI | [ci.yml](../.github/workflows/ci.yml) | Each pull request, and each push to `main` | A newer run for the same ref |
| Memoria documentation | [memoria.yml](../.github/workflows/memoria.yml) | Each pull request, and each push of any branch or tag | Nothing |
| Codebase parity | [parity.yml](../.github/workflows/parity.yml) | Each pull request, each push to `main`, and by hand | A newer run for the same ref |
| Oracle | [oracle.yml](../.github/workflows/oracle.yml) | Nightly at 03:17 UTC, by hand, and a pull request that opens, reopens, gets a push or gets any label; it records on a pull request only under the rule of [Oracle](#oracle) | A newer run for the same ref, a new label included |
| Release | [release.yml](../.github/workflows/release.yml) | A pushed tag `v*`, by hand, and a pull request that changes one of the paths of [Release](#release) | A newer run of the same pull request. A run for a tag or started by hand is not cancelled once it runs; a newer run for the same ref replaces it while it is still pending |
| Benchmarks | [bench.yml](../.github/workflows/bench.yml) | Only by hand | Nothing |
| Memoria Auto-Ack on Renovate | [memoria-auto-ack.yml](../.github/workflows/memoria-auto-ack.yml) | A pull request to `main` that opens, reopens or gets a push (`pull_request_target`); its job runs only for Renovate's branches | A newer run for the same pull request |

The ref of a pull request's run is the pull request's own, so a new push to
a pull request cancels its runs in progress in CI, Codebase parity and
Oracle. In every workflow with a concurrency group, a newer run also
replaces an older run of the same group that is still pending, as GitHub
does by default
([concurrency](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)).

## The CI workflow

| Check | Runner | When it runs | What it checks | Required |
|---|---|---|---|---|
| `Go (ubuntu-latest)` | `ubuntu-latest` | Always | Every step of [The Go jobs](#the-go-jobs) | Yes |
| `Go (ubuntu-latest, QuickJS only)` | `ubuntu-latest` | Always | `internal/expr` and `cmd/wltest` again with `WL_TEST_QUICKJS_ONLY=1`, which turns the Go evaluator of expressions (`jsExpr`) off by default, so that the cases pass without it. The shortcuts and member paths still answer in Go, and tests that set the mode themselves still run both ways | No |
| `Go (macos-latest, packages)`, `Go (macos-latest, flow suite in warm and fresh mode)`, `Go (macos-latest, flow suite in host mode)` | `macos-latest` | Always | The Go job's steps on macOS, in three parts ([The macOS parts](#the-macos-parts)) | Through `Go (macos-latest)` |
| `Go (macos-latest)` | `ubuntu-latest` | After all five parts of the Go job, unless the run was cancelled | That every macOS part passed | Yes |
| `Static Linux engine (amd64)`, `Static Linux engine (arm64)` | `ubuntu-24.04`, `ubuntu-24.04-arm` | Always | [The static Linux engine](#the-static-linux-engine) | No |
| `Expression engine (ubuntu-24.04)`, `Expression engine (ubuntu-26.04)` | Each image by name | Always | [The expression engine jobs](#the-expression-engine-jobs) | Yes |
| `jsonb (PostgreSQL)` | `ubuntu-latest` | Always | The jsonb canonicaliser of [internal/jsonval](../internal/jsonval/README.md) against PostgreSQL 16 in the oracle's pinned image, and the bound on a value's stored size that the job reads use | No |
| `Redis` | `ubuntu-latest` | Always | The Redis resource store against Redis 7.2, Sentinel and Toxiproxy containers ([The Redis job](#the-redis-job)) | No |
| `Bun harness checks` | `ubuntu-latest` | Always | `bun test` in `harnesses/bun`, after the build helper's dependencies: the harness's codec against the frames that the Go tests write, and the [build helper](../harnesses/bun/build/README.md)'s scans | No |
| `Bun SDK types` | `ubuntu-latest` | Always | A type check of the Bun host SDK and the harness runtime as a host that vendors them does it, with the newest and the oldest supported `@types/node` ([type check](../harnesses/bun/README.md#type-check)) | No |
| `Docs (report only)` | `ubuntu-latest` | Always; the annotations on pull requests only | The countable writing rules ([Docs (report only)](#docs-report-only)) | No |
| `Secret scan` | `ubuntu-latest` | Pull requests only | Each commit of a pull request ([Secret scan](#secret-scan)) | No |

### The Go jobs

Each part of the Go job takes these first steps:

| Step | What it does |
|---|---|
| `Check out repository` | Checks out the commit, without keeping credentials |
| `Set up Go` | Installs the Go of `go.mod` |
| `Set up Bun` | Installs Bun 1.4.3, the Bun that windmill-lite pins |
| `Set up Node` | Installs Node 24.22.1, the Node of Windmill `v1.862.0`'s image |
| `Locate Node` | Sets `WL_NODE` to that Node |
| `Fetch windmill-client 1.862.0` | Downloads the package from npm, checks it against its SHA-512 integrity hash, and sets `WL_WINDMILL_CLIENT` |
| `Install the build helper's dependencies` | `bun install --frozen-lockfile` in `harnesses/bun/build` |

Then the steps below run in order. Every part runs `Test`;
`Go (ubuntu-latest)` and `Go (macos-latest, packages)` also run every other
step, which the workflow marks `if: matrix.checks`.

| Step | What it checks |
|---|---|
| `Check formatting` | `gofmt -l .` lists no file |
| `Check go.mod is tidy` | `go mod tidy -diff` shows no change |
| `Vet` | `go vet ./...` |
| `Check that no retarget ledger remains` | `go run ./cmd/wltest ledger`: no retarget ledger and no marker of one exists ([the ledger guard](../cmd/wltest/README.md#the-ledger-guard)) |
| `Test` | `go test -race -timeout 45m` on the part's packages, with `WL_REQUIRE_BUN=1` and `WL_REQUIRE_NODE=1`, so a test that needs Bun, the client or Node fails instead of skipping. A part that selects tests with `-run` also passes `-v` |
| `Test the expression packages in leak check` | `go test -race -count=1 ./internal/expr/...` with `WL_QJS_LEAK_CHECK=1`. The engine frees each QuickJS-ng runtime block by block, and a test that checks for leaks fails on one; the tests that make QuickJS-ng leak on purpose opt out |
| `Build a fixture bundle twice and compare` | Two builds of the fixture bundle, one from the fixture folder and one from a copy of it in another directory, give one digest and the same archive bytes, and `verify` accepts the result |
| `Measure the expression pool under load` | Prints latency percentiles of the pool of QuickJS-ng runtimes that evaluate expressions ([internal/expr/quickjs](../internal/expr/quickjs/README.md#benchmarks-and-the-load-test)). Report only |
| `Report the expression budget gate` | Prints the latency of expressions through the whole pipeline, against the bounds of the budget gate ([internal/expr](../internal/expr/README.md#the-budget-gate)). Report only: the bounds hold on the benchmark machine, not on CI's shared runners |
| `Report the native realms and the process groups` | Runs, by name and with `-v`, the native realm check of [cmd/windmill-lite](../cmd/windmill-lite/README.md#tests) and the tests of the process groups that a job's children join. It fails when one of these tests skips or does not run, which the `Test` step does not show. The workflow lists the tests |
| `Test repository scripts` | `python3 -m unittest discover -s scripts -v`: the tests of the [scripts](../scripts/README.md#run-the-tests) |
| `Set up Node for the npm package` | Puts Node 22.23.5 on the `PATH` for the next step only; `WL_NODE` keeps naming Node 24 |
| `Test the npm package` | The [packaging test](../harnesses/README.md#test-the-package) of the npm package. Node 22 is the oldest major version that the SDK's test entry supports |

The process groups are the ones that a job's children join
([WR-55](semantics/languages.md#wr-55)): a child left in a job's group holds
the job open.

### The macOS parts

On macOS, the Go tests run as three parallel jobs. Complementary `-run` and
`-skip` patterns split them, so together they run every test that one
`go test ./...` runs:

| Part | What it tests |
|---|---|
| `Go (macos-latest, packages)` | Every package, without the flow suite `TestFlowSuite` |
| `Go (macos-latest, flow suite in warm and fresh mode)` | The modes of `TestFlowSuite` in `cmd/wltest` whose names do not start with `host` |
| `Go (macos-latest, flow suite in host mode)` | The modes of `TestFlowSuite` whose names start with `host` |

The job `Go (macos-latest)` runs after them, on Linux. It reads the latest
attempt of each macOS part, and passes only when each passed. It runs even
when a part failed, unless the run was cancelled.

### The static Linux engine

The released Linux engine is one static binary, which `zig cc` 0.15.2
builds against musl. For `amd64` and `arm64`, this job:

1. builds the engine and the expression test binaries that way;
2. checks that each binary names no program interpreter and needs no
   shared library;
3. runs the expression suite from the static test binaries;
4. reports the expression budget gate from them, in the job summary;
5. starts the engine in an image built `FROM scratch`, with no network, as
   user 65534, and runs `version`, `selfcheck` and `verify` on a bundle
   that it built;
6. compares the expression pool's allocators, mimalloc and the C library's
   own malloc, on musl and on glibc, in the job summary. Report only
   ([Allocator](../internal/expr/quickjs/README.md#allocator)).

### The expression engine jobs

The C compilers of `ubuntu-24.04` and `ubuntu-26.04` give one JavaScript
call a different C stack, and the engine scales its stack limit to it
([stack limit](../internal/expr/quickjs/README.md#stack-limit)). So the job
runs on each image by name: a move of `ubuntu-latest` cannot drop either
image. On each image, it:

1. tests `internal/expr/...`, and `internal/expr` again with
   `WL_TEST_QUICKJS_ONLY=1`;
2. prints the stack per call that the engine measured, and the depths of
   recursion that it reaches;
3. runs the `selfcheck` test;
4. builds the engine with the image's C compiler, and runs
   `windmill-lite selfcheck`;
5. builds the static Linux engine, and runs `windmill-lite selfcheck`.

### The Redis job

The job checks the Redis resource store of
[wl/redisstore](../wl/redisstore/README.md) against real servers.
`scripts/redis-test-env.sh up` starts a Redis 7.2 server, a Sentinel that
monitors it and a Toxiproxy, pinned by digest, and the job adds the
variables it prints to its environment. Its steps:

1. **Test the Redis store:** `go test -race ./wl/redisstore`, which runs
   the contract suite on the server and through Sentinel, the eviction
   check and the fault tests.
2. **Test the run command on Redis:** `TestRunCommand`, whose two engine
   processes share a state on the server.
3. **Test wltest's fixture stores on Redis:** `TestFixtureStoresAreApart`
   and `TestFixtureStateOnRedis` in `cmd/wltest`: each fixture's runtime
   gets a Redis store of its own, which its runs share.
4. **Check client-api/state on the Redis store:** `wltest check
   -resource-store redis` of the fixture, in each mode of the flow suite.
5. **Remove the containers**, whatever the steps before did.

Without the variables, as in the other jobs, these tests skip.

### Docs (report only)

On a pull request, the job runs
`python3 scripts/check-docs.py --github --diff-base HEAD^1`, which annotates
each changed Markdown block that breaks a countable rule
([What CI checks](writing.md#what-ci-checks)). On every run, it writes the
count of all warnings to the job summary. A warning never fails the job
([scripts](../scripts/README.md#documentation-check)).

### Secret scan

The job scans each commit of a pull request with
[gitleaks](https://github.com/gitleaks/gitleaks) 8.30.1 (MIT licence), its
default rules and [.gitleaks.toml](../.gitleaks.toml).

1. It checks that the rules find a bearer token in a sample recording, and
   find nothing in the checked-out tree.
2. It scans every commit between the pull request's base and its head, so
   it also finds a secret that one commit adds and a later commit removes.

The output is redacted, so a finding is not published in the log.

## Other workflows

### Memoria documentation

| Check | Runner | When it runs | Required |
|---|---|---|---|
| `memoria` | `ubuntu-24.04` | Every run | Yes |

The job installs Memoria 0.9.2 and runs `memoria check`. It fails while a README has a change in its scope that
nobody acknowledged. Memoria generates this workflow: change it only with
`memoria integrations github upgrade --apply`
([docs/memoria.md](memoria.md#ci-gate)).

### Memoria Auto-Ack on Renovate

| Check | Runner | When it runs | Required |
|---|---|---|---|
| `auto-ack` | `ubuntu-24.04` | Only for a branch of this repository whose name starts with `renovate/`, opened by `renovate[bot]`; skipped for every other pull request | No |

This workflow acknowledges the README reviews of a Renovate pull request
whose only changes are dependency bumps in `go.mod` and `go.sum`, or
action-pin bumps in workflow files. It runs the
[auto-ack scripts](../scripts/README.md#renovate-auto-ack) from `main`.

- **Another change.** On a Renovate pull request with any other change, the
  script declines and the job succeeds without an ack. A contributor then
  acknowledges by hand ([Renovate bumps](memoria.md#renovate-bumps)).
- **A missing token.** The job fails, rather than skips, while the
  `MEMORIA_AUTO_ACK_TOKEN` secret is missing.

### Codebase parity

| Check | Runner | When it runs | Required |
|---|---|---|---|
| `Codebase parity (windmill-cli 1.862.0)` | `ubuntu-latest` | Every run | No |

The job runs the unmodified
`windmill-cli@1.862.0` with `npx` on Node 22.23.5. The CLI pushes synthetic
codebase scripts to a recording HTTP stub. The bundles that it uploads must
equal the bundles that windmill-lite builds with esbuild (`TestCLIParity` in
[internal/bundle](../internal/bundle/README.md)).

Its fallback, `TestCLIOptionsFollowWR47`, compares the esbuild settings
and runs in the Go jobs.

### Oracle

The [oracle](glossary.md#oracle) workflow records the flow fixtures and the
expression cases on an unmodified Windmill Community Edition `v1.862.0`
image.

| Check | Runner | When it runs | Required |
|---|---|---|---|
| `Oracle trigger` | `ubuntu-latest` | Pull requests only | No |
| `Oracle record (<session>)`, one per session | `ubuntu-latest` | A schedule or a dispatch; on a pull request, when `Oracle trigger` decides to record; never after a cancellation | No |

A record job fails like any job when a setup step or `Build wltest` fails.
Its two checks are `Record the fixtures`, which fails when an observation
differs from the committed [recording](glossary.md#recording) or a run has
no recording, and `Metadata drift gate (OR-8)`, which fails when the
metadata that the `wmill` CLI generates for a committed sync folder
changes.

The job uploads an artifact, kept for 30 days, also when it fails: the
recordings, the mismatched observations, the raw captures, a report and,
on drift, the changed files.

On a pull request, the job `Oracle trigger` decides first. The workflow
records only when the pull request has the label `oracle`, or changes one of
these paths: `conformance/`, `internal/oracle/`, `internal/conformance/`,
`cmd/wltest/`, `internal/openflow/`, `internal/yamlx/`,
`internal/syncfolder/`, `.github/workflows/oracle.yml`, `go.mod` or
`go.sum`. Otherwise the record jobs are skipped.

It records in seven sessions, side by side. Each session other than
`workers1` and `workers2` records only the fixtures that declare its
configuration. The workflow lists the fixtures of `workers2` and `long2`.

| Session | Workers | What it records |
|---|---|---|
| `workers1` | 1 | Every fixture and expression case, except those that need two workers or another configuration |
| `workers2` | 2 | The fixtures that need two workers, and the smoke fixture again with `LISTEN_NEW_EVENTS_INTERVAL_SEC=10` |
| `native` | 1 | The native-step fixtures, on a worker that serves the `nativets` tag |
| `long` | 1 | The fixtures whose runs wait a minute or more on the server's own monitors, with a run timeout of 5 minutes |
| `long2` | 2 | The same configuration for the fixture of retry delays, whose step waits for the flows that it starts, with a run timeout of 9 minutes |
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
- on a pull request that changes `.github/workflows/release.yml`,
  `harnesses/package.json`, `harnesses/bun.lock`, `harnesses/npm/**` or
  `scripts/release.py`;
- on a tag pushed in a fork.

`release.py mode` decides it, and refuses some runs outright, such as a
dispatch with the dry run off in a fork or on a branch
([release helper](../scripts/README.md#release-helper)). A tag must be
`v` + the version, dry run or not. None of these checks is required.

| Check | Runner | When it runs | What it does |
|---|---|---|---|
| `Check the version` | `ubuntu-24.04` | Every run | Checks the tag against the version, and decides whether the run releases |
| `Engine (<os>-<arch>)` | `ubuntu-24.04` (`linux-amd64`), `ubuntu-24.04-arm` (`linux-arm64`), `macos-15` (`darwin-arm64`), `macos-15-intel` (`darwin-amd64`) | After `Check the version` | Builds and checks the engine for each platform |
| `npm package` | `ubuntu-24.04` | After the version check and the engines | Packs the npm package, tests the tarball with the release engine, and runs `npm publish --dry-run` |
| `Release assets` | `ubuntu-24.04` | After the version check and the engines | Writes `LICENSES.txt` and `SHA256SUMS` |
| `GitHub Release` | `ubuntu-24.04` | Only when the run releases (not a dry run, in `invakid404/windmill-lite`, on a tag, pushed or dispatched), after the version check, the package and the assets | Creates the GitHub Release with the assets |
| `npm publish` | `ubuntu-24.04` | The same condition, after the version check, the package and a successful `GitHub Release` | Publishes the package to npm |

### Benchmarks

The benchmark workflow compares builds of the engine on GitHub's hosted
runners. It runs only when started by hand, and gates nothing
([comparing builds](benchmarks.md#comparing-builds-on-githubs-runners)).

| Check | Runner | When it runs | What it does |
|---|---|---|---|
| `Plan` | `ubuntu-24.04` | Every dispatch | Reads the inputs, checks the memory limit's form, and gives the builds, the groups, the rounds and the memory limit to the bench jobs; the groups `none` give an empty list |
| `Bench (<arch>, <group>)`, one per architecture and group | `ubuntu-24.04` (`amd64`), `ubuntu-24.04-arm` (`arm64`) | When `Plan` lists at least one group; skipped for an empty list | Builds each build's benchmarks, runs the rounds, compares the builds with benchstat, summarises the memory tests, and uploads the results |

Neither check is required.

## Read a failure

1. List the pull request's checks: `gh pr checks <number>`.
2. Open the log of the failed job: `gh run view <run-id> --log-failed`.
3. Find the failed check or step in the table below.
4. Run its local command; for a row that says the check runs only on
   GitHub, read the job's log instead.
5. Fix the cause.
6. Push a new commit.

| Failed check or step | Run locally |
|---|---|
| `Check formatting` | `gofmt -l .`, then `gofmt -w <file>` for each file that it lists |
| `Check go.mod is tidy` | `go mod tidy` |
| `Vet` | `go vet ./...` |
| `Check that no retarget ledger remains` | `go run ./cmd/wltest ledger` |
| `Test`, or `Report the native realms and the process groups` | The failed test, with the variables of [Run the tests locally](#run-the-tests-locally) set: `go test -race -run '^<test>$' ./<package>` |
| `Test the expression packages in leak check` | `WL_QJS_LEAK_CHECK=1 go test -race -count=1 ./internal/expr/...` |
| `Go (ubuntu-latest, QuickJS only)` | `WL_TEST_QUICKJS_ONLY=1 go test -race -timeout 45m ./internal/expr ./cmd/wltest`, with the variables of [Run the tests locally](#run-the-tests-locally) set |
| `Build a fixture bundle twice and compare` | The step's script in `.github/workflows/ci.yml`, with `RUNNER_TEMP` set to an empty directory and `WL_BUN` and `WL_BUILD_HELPER` set |
| `Measure the expression pool under load` | `WL_QUICKJS_LOAD=1 WL_QUICKJS_LOAD_SECONDS=1 go test -count=1 -run '^TestPoolUnderLoad$' -v ./internal/expr/quickjs` (report only: it fails only on an error) |
| `Report the expression budget gate` | `WL_EXPR_BUDGET=report WL_EXPR_BUDGET_SECONDS=1 go test -count=1 -run '^TestBudgetGate$' -v ./internal/expr` (report only) |
| `Expression engine (<image>)`, step `Test the expression packages` | `go test -race -count=1 ./internal/expr/...`, then `WL_TEST_QUICKJS_ONLY=1 go test -race -count=1 ./internal/expr` |
| `Expression engine (<image>)`, step `Print the stack measurements` | `go test -count=1 -v -run '^(TestCallStackBytes\|TestOverflowDepthAtTheDefaultStack\|TestProbeMatchesEvaluations)$' ./internal/expr/quickjs` |
| `Expression engine (<image>)`, step `Test selfcheck` | `go test -count=1 -run '^TestSelfcheck$' ./cmd/windmill-lite` |
| `Expression engine (<image>)`, step `Start the engine built with the image's compiler` | `go build -o windmill-lite ./cmd/windmill-lite`, then `./windmill-lite selfcheck` |
| `Expression engine (<image>)`, step `Start the static Linux engine`, or `Static Linux engine (<arch>)` | On Linux with `zig` 0.15.2 only: the step's commands in `.github/workflows/ci.yml`. The static engine's image test also needs Docker. Elsewhere, read the job's log |
| `jsonb (PostgreSQL)` | The three tests of the step, with `WL_JSONB_PSQL` set to a `psql` command for PostgreSQL 16 ([internal/jsonval](../internal/jsonval/README.md#run-the-tests)) |
| `Redis` | On Linux with Docker: `eval "$(scripts/redis-test-env.sh up | sed 's/^/export /')"`, then the failing step's command from `.github/workflows/ci.yml`, then `scripts/redis-test-env.sh down` ([wl/redisstore](../wl/redisstore/README.md#tests)) |
| `Bun SDK types` | `bun install --frozen-lockfile` in `harnesses/bun/test/typecheck` and in its `oldest/`, then `harnesses/bun/test/typecheck/check.sh` ([type check](../harnesses/bun/README.md#type-check)) |
| `Codebase parity (windmill-cli 1.862.0)` | `WL_WMILL_CLI='npx --yes windmill-cli@1.862.0' go test -count=1 -run '^TestCLIParity$' -v ./internal/bundle`, with Node 22 and network access |
| `Secret scan`, step `Check the rules` | The step's script in `.github/workflows/ci.yml` (a sample bearer token must be found, the tree must be clean), with gitleaks 8.30.1 |
| `Secret scan`, step `Scan the pull request's commits` | `gitleaks git --no-banner --redact --log-opts="origin/main..HEAD" .`, with gitleaks 8.30.1 |
| `Oracle trigger` | Nothing to run: it only reads the pull request's label and paths. Read its log, which lists them |
| `Oracle record (<session>)` | `bin/wltest record --check` with the session's arguments from `.github/workflows/oracle.yml`, after `go build -o bin/wltest ./cmd/wltest`; it needs Docker, the Windmill image and `npx` ([cmd/wltest](../cmd/wltest/README.md#usage)). Read a mismatch in the job's artifact ([mismatched observations](../cmd/wltest/README.md#mismatched-observations)) |
| `auto-ack` | Hosted only: it needs the repository secret. Read its log; acknowledge by hand ([Renovate bumps](memoria.md#renovate-bumps)) |
| `Check the version` | `python3 scripts/release.py check-tag <tag>`, or `mode` with the run's variables ([release helper](../scripts/README.md#release-helper)) |
| `Engine (<os>-<arch>)`, a build step | `python3 scripts/release.py build-engine <out>`, with `--static` and `CC='zig cc -target <target>'` on Linux |
| `Engine (<os>-<arch>)`, step `Check that the Linux engine is static` | On Linux: the step's `readelf` checks in `.github/workflows/release.yml` on the built engine, which must name no program interpreter and need no shared library |
| `Engine (<os>-<arch>)`, step `Check the engine's version and start it` | `python3 scripts/release.py check-engine <out>`, then `<out> selfcheck` |
| `npm package` | The packaging test ([harnesses/README.md](../harnesses/README.md#test-the-package)) |
| `Release assets` | Its step `Write LICENSES.txt and SHA256SUMS` also needs the four engines that the `Engine` jobs upload, so read its log for a missing engine or a checksum that fails. The licence part runs locally as `python3 scripts/release.py notices LICENSES.txt` |
| `GitHub Release`, `npm publish` | Hosted only: they create the release and publish the package. Read the job's log |
| `Plan`, `Bench (<arch>, <group>)` | Read the job's log; [docs/benchmarks.md](benchmarks.md) gives the local benchmark commands |
| `Test repository scripts` | `python3 -m unittest discover -s scripts -v` |
| `Test the npm package` | The packaging test ([harnesses/README.md](../harnesses/README.md#test-the-package)) |
| `Bun harness checks` | `bun test` in `harnesses/bun`, after `bun install --frozen-lockfile` in `harnesses/bun/build` |
| `memoria` | The per-session flow of [docs/memoria.md](memoria.md#the-per-session-flow). `memoria explain <README.md>` says why a README is pending |
| `Go (macos-latest)` | Nothing: open the macOS part that failed |
| An annotation of `Docs (report only)` | `python3 scripts/check-docs.py <file>` |

## Re-run a check

Re-run a check only when it failed for a cause outside your change, such
as a runner or a download that failed. A re-run uses the same commit and
the same ref as the first run, so it cannot pick up a fix: push a new commit
for that. GitHub re-runs the jobs that depend on a re-run job too, and
allows re-runs for 30 days after the first run
([re-running workflows and jobs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs)).

Re-running needs write access to the repository
([repository roles](https://docs.github.com/en/organizations/managing-user-access-to-your-organizations-repositories/managing-repository-roles/repository-roles-for-an-organization)).
A contributor without it pushes a new commit, or asks a maintainer to re-run
the check.

To re-run the failed jobs of a run on the web page:

1. Open the repository's **Actions** tab.
2. Select the workflow, then the run.
3. Open the **Re-run jobs** menu in the upper-right corner.
4. Select **Re-run failed jobs**.
5. Select **Re-run jobs**.

To re-run one job on the web page:

1. Open the repository's **Actions** tab.
2. Select the workflow, then the run.
3. In the **Jobs** list of the left sidebar, select the re-run icon next to
   the job.
4. Select **Re-run jobs**.

To re-run the failed jobs with the GitHub CLI:

1. Find the run's ID: `gh pr checks <number>` links each check to
   `…/actions/runs/<run-id>/job/…`.
2. Run `gh run rerun <run-id> --failed`.

To re-run one job with the GitHub CLI:

1. List the run's job IDs:
   `gh run view <run-id> --json jobs --jq '.jobs[] | {name, databaseId}'`.
2. Run `gh run rerun --job <databaseId>`.

Re-running a failed macOS part also re-runs `Go (macos-latest)`, which
depends on it, and that job reads the latest attempt of each part.

## Run the tests locally

Some tests need Bun 1.4.3, Node, or the unmodified Windmill client library.
When a variable below is not set, each test that needs it skips, or runs
without that side. With `WL_REQUIRE_BUN=1` (for `WL_BUN` and
`WL_WINDMILL_CLIENT`) or `WL_REQUIRE_NODE=1` (for `WL_NODE`) set, as in CI,
the test fails instead.

| Variable | Value | Tests that need it |
|---|---|---|
| `WL_BUN` | The path of a Bun 1.4.3 executable | The harness, build and host-mode tests, in `internal/harness`, `internal/bundle`, `internal/oracle`, `internal/platform/facade`, `wl`, `cmd/windmill-lite` and `cmd/wltest` |
| `WL_WINDMILL_CLIENT` | A directory whose `node_modules` holds `windmill-client@1.862.0` | The test that concurrent invocations each see their own `WM_*` environment variables, the test that the [platform facade](../internal/platform/facade/README.md) attributes each call to its step, and the test of the client with shared code in `cmd/wltest` |
| `WL_NODE` | A Node executable. CI uses Node 24.22.1, the Node of Windmill `v1.862.0`'s image | The step suite's runs of each CommonJS codebase bundle on Node, where Windmill runs it. Without it, the step suite runs on Bun only |

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
7. Run `go test -race -timeout 45m ./...`, as CI's `Test` step does: the
   flow suite in `cmd/wltest` can take longer than `go test`'s default limit
   of 10 minutes.

Each package's README gives its own tests and their variables, such as the
expression packages' leak check and budget gate
([internal/expr](../internal/expr/README.md#tests)).

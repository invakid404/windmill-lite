# cmd/windmill-lite

**Current state:** the `windmill-lite` command builds, verifies and runs
[bundles](../../docs/glossary.md#bundle), checks that its expression engine
starts, serves [host mode](../../docs/glossary.md#host-mode) for a host,
and prints its version. The planned subcommands that serve a bundle over
the network (`serve`), pack it (`pack`, `image`) or generate clients
(`gen`) do not exist yet. This README covers the command in this directory
and its `testdata/`.

The command is the [engine](../../docs/glossary.md#engine): each release
ships it as one binary per platform. The first part of this README is the
command reference. [Work on the command](#work-on-the-command) is for
contributors: the files, the tests and the benchmarks.

## Usage

```text
windmill-lite build <folder> -o <bundle> [flags]
windmill-lite verify <bundle> [-manifest <release manifest>] [-digest <digest>]
windmill-lite run <path> --bundle <bundle> [--args <json|@file>] [flags]
windmill-lite selfcheck
windmill-lite host --bundle <dir> --workdir <dir> [flags]
windmill-lite version
```

### Get the command

- **A release's engine:** download it for your platform and check it
  against the release's sums
  ([Get the engine](../../harnesses/README.md#get-the-engine)). Its
  version is the release's, such as `v0.2.0`.
- **From a checkout:** run `go run ./cmd/windmill-lite <subcommand>`, or
  build a binary with `go build -o windmill-lite ./cmd/windmill-lite`. You
  need Go 1.27.3 or later and a C compiler, because the expression engine
  is C code that cgo compiles.

An engine opens only the bundles that the same engine version built
([version](#version)). So build and run a bundle with one engine.

### Rules for every subcommand

- Flags may come before or after the positional argument.
- A flag takes one dash or two: `-bundle` and `--bundle` are the same
  flag.
- `-h` after `build`, `verify`, `run` or `host` lists that subcommand's
  flags and their defaults, and exits with status 2. `selfcheck` and
  `version` take no flags: an argument is a usage error.
- A usage error prints the usage line on standard error and exits with
  status 2. Usage errors are an unknown subcommand, a missing or extra
  argument, an unknown flag, and a missing required flag.

| Exit status | Meaning |
|---|---|
| 0 | The subcommand did its work. |
| 1 | The subcommand failed: an input, a file or the bundle did not work. Each subcommand's section says when. |
| 2 | A usage error. |

Three environment variables give the defaults of flags:

| Variable | Flag | Subcommands | Value |
|---|---|---|---|
| `WL_BUN` | `-bun` | `build`, `run` | The [Bun](../../docs/glossary.md#bun) 1.4.3 executable. Without it, the command runs `bun` from the `PATH`. |
| `WL_BUILD_HELPER` | `-helper` | `build` | The [build helper](../../docs/glossary.md#build-helper), `harnesses/bun/build/main.ts` of a checkout |
| `WL_HARNESS_RUNTIME` | `-harness-runtime` | `build`, `run` | The directory of the [harness runtime](../../docs/glossary.md#harness-runtime), `harnesses/bun/runtime` of a checkout |

## build

```text
windmill-lite build <folder> -o <bundle> [flags]
```

`build` compiles a [sync folder](../../docs/glossary.md#sync-folder) and an
optional [host contract](../../docs/glossary.md#host-contract) into a
bundle. It reads the folder from its root: the nearest directory at or
above `<folder>` that holds `wmill.yaml`. It bundles each Bun
[script](../../docs/glossary.md#script) with the build helper on Bun 1.4.3,
and writes the bundle with a manifest of its files' digests.

It prints the bundle's digest on standard output, and each warning on
standard error. The [packaging ADR](../../docs/design/0010-packaging.md)
gives the bundle's format, and
[internal/bundle](../../internal/bundle/README.md) describes the build.

| Flag | Value | Default | Meaning |
|---|---|---|---|
| `-o` | A path | None: required | The bundle to write. A path that ends in `.wlb` is an archive file, which must not exist. Any other path is a directory, which must not exist or must be empty. |
| `-contract` | A file | None | The host contract, in YAML or JSON. Without one, every [flow](../../docs/glossary.md#flow) and script of the folder is a [root](../../docs/glossary.md#root). |
| `-helper` | A file | `$WL_BUILD_HELPER` | The build helper, with its dependencies installed. Required. |
| `-bun` | An executable | `$WL_BUN`, else `bun` | The Bun that bundles the scripts: Bun 1.4.3 |
| `-harness-runtime` | A directory | `$WL_HARNESS_RUNTIME`, else `runtime/` beside the helper's directory | The harness runtime that the bundle is built for. The manifest records its digest. |
| `-module-state` | `warm` or `fresh` | `warm` | The bundle's [module state](../../docs/glossary.md#module-state), which `run` uses unless its own flag overrides it |
| `-shared-code` | None | Off | Builds with shared code ([shared module code ADR](../../docs/design/0013-shared-module-code.md)). A `warm` or host-mode process then compiles the dependency modules of each build group once, and gives each step module its own instance of them. The bundle keeps each script's own bundle too, which `fresh` runs. |
| `-strict` | None | Off | Refuses a for-loop without `skip_failures` and a `branchall` without `skip_failure`, in place of their default values |
| `-bun-mismatch` | None | Off | Builds on a Bun other than 1.4.3, and marks the bundle `nonconformant_runtime` |
| `-work` | A directory | A temporary directory | The scratch directory for dependency installs |
| `-verbose` | None | Off | Copies the build helper's output to standard error |

| Exit status | When |
|---|---|
| 0 | The bundle is written, and its digest is on standard output. |
| 1 | The build failed: the contract does not read, the folder does not compile, a script does not bundle, or the `.wlb` file exists. |
| 2 | A usage error, or no build helper is set. |

This example builds the synthetic build [fixture](../../docs/glossary.md#fixture)
of `internal/bundle`, from the root of a checkout:

1. Install the build helper's dependencies:
   `(cd harnesses/bun/build && bun install --frozen-lockfile)`.
2. Point `WL_BUILD_HELPER` at the helper:
   `export WL_BUILD_HELPER=$PWD/harnesses/bun/build/main.ts`.
3. Build the bundle into a file that does not exist yet:

   ```sh
   windmill-lite build internal/bundle/testdata/fixture/windmill \
     -contract internal/bundle/testdata/fixture/contract.yaml -o /tmp/fixture.wlb
   ```

The command prints the digest, such as `sha256:f2ebb0f7…`. Each engine
version gives its own digest, because the manifest records the version.

## verify

```text
windmill-lite verify <bundle> [-manifest <release manifest>] [-digest <digest>]
```

`verify` recomputes the digest of every file of a bundle directory or a
`.wlb` archive. It checks them against the bundle's own manifest, or
against a release manifest. The bundle must hold exactly the files that
the manifest lists.

It prints the bundle's digest when everything matches. Otherwise it lists
each difference on standard error. A bundle that another engine version
built still verifies, with a note that this engine would not open it.

| Flag | Value | Default | Meaning |
|---|---|---|---|
| `-manifest` | A file | None | A release manifest. The bundle's files must match it, and the bundle's own manifest must have the same digest. |
| `-digest` | A digest | None | The digest that the bundle must have |

| Exit status | When |
|---|---|
| 0 | Every digest matches. The bundle's digest is on standard output. |
| 1 | A file, the manifest or the digest differs, or the bundle or the release manifest does not read. |
| 2 | A usage error |

With the bundle of the `build` example:

```sh
windmill-lite verify /tmp/fixture.wlb
```

It prints the same digest as `build`. With `-digest sha256:0000`, it exits
with status 1 and prints:

```text
windmill-lite verify: /tmp/fixture.wlb does not match:
  bundle digest sha256:f2ebb0f7…, want sha256:0000
```

## run

```text
windmill-lite run <path> --bundle <bundle> [--args <json|@file>] [flags]
```

`run` opens a bundle with the [wl](../../wl/README.md) package, runs the
flow at `<path>` once, and prints the outcome as JSON on standard output.
With `-kind script`, `<path>` names a root script instead. The
[steps](../../docs/glossary.md#step) run in Bun
[harness](../../docs/glossary.md#harness) processes that the command
starts. SIGINT and SIGTERM cancel the run.

| Flag | Value | Default | Meaning |
|---|---|---|---|
| `--bundle` | A directory or a `.wlb` file | None: required | The bundle. An archive is extracted into a directory of its own under the data directory, and removed afterwards. |
| `--args` | A JSON object, or `@` and a file that holds one | `{}` | The run's arguments |
| `-kind` | `flow` or `script` | `flow` | What `<path>` names: a flow, or a root script |
| `-id` | A UUID | A new UUID | The [job](../../docs/glossary.md#job) id of the run |
| `-bun` | An executable | `$WL_BUN`, else `bun` | The Bun that runs the steps: Bun 1.4.3 |
| `-harness-runtime` | A directory | `$WL_HARNESS_RUNTIME` | The harness runtime that the bundle was built for. Its digest must be the one in the manifest. Required for a bundle with steps. |
| `-module-state` | `warm` or `fresh` | The bundle's | Runs the steps [`warm`](../../docs/glossary.md#warm) or [`fresh`](../../docs/glossary.md#fresh) |
| `-data` | A directory | A temporary directory, removed afterwards | The data directory that the run writes into |
| `-digest` | A digest | None | The digest that the bundle must have |
| `-variables` | A file | None | The host's variables, keyed by path (below). Secrets go here: a bundle never holds a secret's value. |
| `-resources` | A file | None | The host's resources, keyed by path (below) |
| `-resource-store` | `memory` or `redis` | `memory` | The store of the resources that steps write, such as the stock client's state. `memory` keeps them for the run only, a named [deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations) of the equivalence contract. `redis` keeps them on the Redis that the environment variable `WL_REDIS_URL` names ([Choosing a resource store](../../docs/resource-stores.md)). |
| `-redis-prefix` | A prefix | `windmill-lite:` | The prefix of the Redis store's keys, so deployments can share one Redis: one or more characters without a colon, then a colon, such as `app-1:` |
| `-redis-allow-eviction` | None | Off | Starts on a Redis whose `maxmemory-policy` is `allkeys-*`, which can evict the store's entries, with a warning |
| `-trace` | None | Off | Adds a summary of the [trace](../../docs/glossary.md#trace) to the outcome |
| `-quickjs-only` | None | Off | Turns the Go evaluator of [expressions](../../docs/glossary.md#expression) off: every expression that the shortcuts and member paths do not answer runs in QuickJS-ng |
| `-cpuprofile` | A file | None | Writes a CPU profile of the engine to the file ([Profiling](#profiling)) |

The outcome's fields:

| Field | Meaning |
|---|---|
| `id` | The run's job id |
| `status` | The run's status, such as `success`, `failure`, `canceled` or `skipped` ([wl](../../wl/README.md#result)) |
| `result` | The run's final result, in its stored form |
| `may_have_mutated` | Whether the run may have made a write ([wl](../../wl/README.md#result)) |
| `warnings` | Each warning's `code` and `message`, or `[]` |
| `trace` | With `-trace` only: the module state, each step's `site`, `job`, `status`, `module` and `result`, and each platform call's `site`, `job`, `method`, `path`, `route` and `status`. It is a summary for debugging, not the trace's wire form. |

| Exit status | When |
|---|---|
| 0 | The run's status is `success` or `skipped`. |
| 1 | The run has another status, and the outcome is on standard output. Or the run did not start: standard output is empty, and standard error gives the reason. |
| 2 | A usage error |

### Variables and resources files

A variables file maps each path to the variable's `value`, `is_secret` and
`encrypted`. `encrypted` is `true` when `value` is Windmill's ciphertext.
A resources file maps each path to the resource's `resource_type` and
`value`. These are the forms of a bundle's `data/variables.json` and
`data/resources.json`.

```json
{
  "f/app/token": { "value": "a-secret-value", "is_secret": true, "encrypted": false }
}
```

```json
{
  "f/app/settings": { "resource_type": "c_settings", "value": { "greeting": "hi", "retries": 1 } }
}
```

### Why a run does not start

- Another engine version built the bundle. The error names both versions.
- A file of the bundle does not match the manifest, or the bundle's digest
  is not the one that `-digest` gives.
- The bundle has steps, and `-harness-runtime` is not set, or names a
  runtime with another digest than the bundle's.
- `--args`, `-variables` or `-resources` does not read as JSON, or `<path>`
  names no flow or root script of the bundle.
- `-resource-store` names a store other than `memory` or `redis`.
- `-resource-store redis`, and `WL_REDIS_URL` is not set or does not parse,
  Redis cannot be reached, or its `maxmemory-policy` is `allkeys-*` without
  `-redis-allow-eviction`. The error names the server by its address,
  never by the URL, which holds the password.

With the bundle of the `build` example, from the root of the same
checkout:

```sh
windmill-lite run f/app/main --bundle /tmp/fixture.wlb \
  --args '{"name": "lite"}' -harness-runtime harnesses/bun/runtime
```

It prints the outcome, with `"status": "success"`, and exits with status 0.

## selfcheck

```text
windmill-lite selfcheck
```

`selfcheck` starts the expression engine, QuickJS-ng
([internal/expr/quickjs](../../internal/expr/quickjs/README.md)), and
evaluates three probes. Their results do not depend on the C library:

- the language core: arithmetic, strings, a regular expression, JSON and a
  sort;
- a string that the engine passes in from Go;
- a recursion 800 calls deep, which needs the engine's own thread stacks.

`selfcheck` takes no flags and no arguments.

| Flag | Value | Default | Meaning |
|---|---|---|---|
| None | — | — | `selfcheck` has no flags. |

It prints one line, which also gives the C stack that one JavaScript call
takes in this build, and the stack limit that the engine scaled from it
([stack limit](../../internal/expr/quickjs/README.md#stack-limit)).
CI runs it from the static Linux engine in an empty image
([docs/ci.md](../../docs/ci.md#the-static-linux-engine)).

| Exit status | When |
|---|---|
| 0 | Every probe gave its expected result. |
| 1 | The engine did not start, or a probe failed. Standard error names the probe. |
| 2 | An argument was given. |

The `v0.2.0` engine for macOS arm64 prints:

```text
windmill-lite v0.2.0 (darwin/arm64): expression engine ok, 3 probes in 2ms; 960 bytes of C stack per call, stack limit 1310720 bytes
```

## host

```text
windmill-lite host --bundle <dir> --workdir <dir> [flags]
```

Do not run `host` by hand: a host SDK starts it. It is the engine side of
host mode. The [Bun host SDK](../../docs/glossary.md#bun-host-sdk)'s
`connect()` starts it as a child process
([harnesses/bun](../../harnesses/bun/README.md#host-mode-and-the-host-sdk)).

`host` opens a bundle directory, which the host loads too, and completes
the handshake with the [host](../../docs/glossary.md#host) that started it.
Then it serves the host's runs, whose steps run in the host's process. It
stops when the host's connection ends, or on SIGTERM or SIGINT, which
cancel the runs in flight.

The host that starts it passes:

- its socket, as file descriptor 3;
- optionally, as file descriptor 4, the channel on which it announces the
  process groups of its steps' child processes. A host that passes none
  announces them on its socket;
- its own working directory, as `--workdir`.

| Flag | Value | Default | Meaning |
|---|---|---|---|
| `--bundle` | A directory | None: required | The bundle directory. Host mode does not open a `.wlb` archive. |
| `--workdir` | A directory | None: required | The host's working directory. It must be writable, and it must be the directory that the host's hello names. |
| `--kill-after` | A duration, such as `30s` | `30s` | The watchdog's limit of silence from the host. After it, the engine kills the host. `0` turns the watchdog off. |
| `-data` | A directory | A temporary directory, removed afterwards | The data directory that the runtime writes into |
| `-digest` | A digest | None | The digest that the bundle must have |
| `-variables`, `-resources` | A file | None | The host's variables and resources, as for [`run`](#variables-and-resources-files) |
| `-plugin-timeout` | A duration | `1m` | How long a call to the host's [host plugin](../../docs/glossary.md#host-plugin) waits for its reply |
| `-resource-store` | `memory` or `redis` | `memory` | The store of the resources that steps write, as for `run`. `memory` keeps them while the host's connection lasts, for every run of the host. `redis` keeps them on the Redis that `WL_REDIS_URL` names. |
| `-redis-prefix`, `-redis-allow-eviction` | As for `run` | As for `run` | The Redis store's key prefix, and its start on an evicting policy |
| `-read-cache` | None | Off | Each run keeps the answer of its first read of a host's or bundle's resource or variable, and answers its later reads of that path from it. An error is not kept, and a variable that a step updates and a stored state are read again ([the read cache](../../internal/platform/README.md#the-read-cache)). Windmill reads each time, so this is a named [deviation](../../docs/glossary.md#deviation) of the [equivalence contract](../../docs/design/0001-equivalence-contract.md#5-named-deviations). |
| `-skip-args-file` | None | Off | The host states that its own code never reads `args.json`. Then, for a bundle none of whose scripts can read it, the engine writes no `args.json` for a step. |
| `-identity` | A file | None: windmill-lite's placeholders | The job identity of every job, a JSON object of the optional strings `workspace`, `username`, `email`, `permissioned_as`, `base_url` and `worker_group` ([job identity](../../wl/README.md#job-identity)). A run frame's `identity` replaces the fields that it sets for that run. |
| `-foreign-windmill` | An origin, such as `https://windmill.example.com`; repeat the flag for each | None | A real [Windmill](../../docs/glossary.md#windmill) that the host's steps call through their own client. A step's dispatch there loses its links to the step's own job ids ([wl](../../wl/README.md#job-ids-on-a-real-windmill)). |
| `-result-polls-for-all-jobs` | None | Off | The host's steps' result polls answer for every job of a live run that its job read answers for, as Windmill answers them, and not only for the runs that steps dispatched (`ResultPollsForAllJobs` in [wl](../../wl/README.md#options)). |
| `-quickjs-only` | None | Off | As for `run`: the Go evaluator of expressions is off |
| `-check-bundle` | A digest | None | Checks the bundle directory as host mode opens it, before the handshake: a directory, not an archive, against its manifest, `-digest`, this digest and this engine's version. Then it exits, 0 or 1, without connecting. The Bun host SDK runs it, with its other flags, before it loads a bundle's own plugin. |
| `-cpuprofile` | A file | None | Writes a CPU profile of the engine to the file ([Profiling](#profiling)) |

The engine refuses the host in the handshake, with one of these codes:

| Code | When |
|---|---|
| `workdir_not_writable` | `--workdir` is not a directory that the engine can write. |
| `workdir_mismatch` | `--workdir` is not the directory that the host's hello names. |
| `engine_refused` | The bundle does not open, another engine version built it, or a file of `-variables`, `-resources`, `-identity` or `-cpuprofile` does not open, or the `-identity` file is not an identity. Or the resource store does not start: `-resource-store` names a store other than `memory` or `redis`, or the Redis store fails as for `run`. Or a `-foreign-windmill` value is not an origin that [wl accepts](../../wl/README.md#job-ids-on-a-real-windmill). The message gives the reason, and never repeats a `-foreign-windmill` value. |

The other codes of the handshake, such as `version_mismatch`, are the
host SDK's errors
([harnesses/bun](../../harnesses/bun/README.md#errors-and-refusals)). The
[harness protocol ADR](../../docs/design/0004-harness-protocol.md#host-mode)
and [internal/harness](../../internal/harness/README.md#host-mode)
describe the handshake and the frames.

| Exit status | When |
|---|---|
| 0 | The host's connection ended, or a signal stopped the engine. |
| 1 | The socket is not usable, or the engine refused the host. |
| 2 | A usage error, or a negative `--kill-after` |

The example of [Profiling](#profiling) passes a flag to `host` through
the Bun host SDK.

## version

```text
windmill-lite version
```

`version` prints `windmill-lite` and the engine's version. `windmill-lite`
with no argument, `-version`, `--version` and `-v` do the same. These are
whole commands, not flags: `version` takes no flags and no arguments.

| Flag | Value | Default | Meaning |
|---|---|---|---|
| None | — | — | `version` has no flags. |

| Exit status | When |
|---|---|
| 0 | The version is printed on standard output. |
| 2 | An argument follows, such as `windmill-lite version extra`. The command prints the usage of every subcommand. |

```sh
windmill-lite version
```

The `v0.2.0` engine prints `windmill-lite v0.2.0`.

The version is the first of these that the binary has:

1. the value linked in with `-ldflags "-X main.version=<version>"`, as a
   release's engine has;
2. the module version from the binary's build information: the version
   of `go install github.com/invakid404/windmill-lite/cmd/windmill-lite@<version>`,
   or the pseudo-version that `go build` stamps from the checkout's commit,
   with `+dirty` when the tree has changes;
3. `(devel)`, for `go run` and test binaries.

`build` records this version in the bundle's manifest, and `run` and
`host` open only a bundle with their own version. An engine whose version
is `(devel)` records `(devel)`, and `build` warns about it on standard
error.

## Profiling

`-cpuprofile <file>` on `run` and `host` writes a CPU profile of the
engine process in Go's pprof format. It covers the engine only: the Bun
harness or the host is another process.

- `run` writes the profile when the run ends.
- `host` writes it when the host's connection ends, or when the engine
  gets SIGTERM or SIGINT.
- A profile file that cannot be created fails the command before it opens
  the bundle. `host` then refuses the host with `engine_refused`.

A Bun host passes the flag in `connect()`'s `engineFlags`. This fragment
shows the option only:

```ts
await connect({ engine, bundle, workdir, engineFlags: ["-cpuprofile", "/tmp/engine.pprof"] });
```

Read the profile with Go's pprof tool:

```sh
go tool pprof -top /tmp/engine.pprof
```

## Work on the command

This part is for contributors who change the command, its tests or its
benchmarks.

### Files

| File | What it holds |
|---|---|
| `main.go` | The subcommand switch, the version, the flag parser that takes flags after the positional argument, and `-cpuprofile` |
| `build.go` | `build` and `verify`, through [internal/bundle](../../internal/bundle/README.md) |
| `runcmd.go` | `run`, through [wl](../../wl/README.md), and the outcome's JSON form; the resource store's flags, which `host` shares, and the store they open |
| `selfcheck.go` | `selfcheck` and its three probes |
| `host.go` | `host`, through `wl` in host mode |
| `*_test.go` | The tests and the benchmarks ([Tests](#tests), [Benchmarks](#benchmarks)) |
| `testdata/` | The synthetic sync folders and the Bun hosts of the tests ([Test data](#test-data)) |

## Tests

Most tests need Bun 1.4.3 in `WL_BUN` and the build helper's
dependencies. Without `WL_BUN` they skip, unless `WL_REQUIRE_BUN` is set,
as in CI. [Run the tests locally](../../docs/ci.md#run-the-tests-locally)
gives the setup.

| File | What it checks |
|---|---|
| `main_test.go` | The version output, the link-time version, the usage errors and `selfcheck`. `build` and `verify` end to end: the fixture of `internal/bundle`, built as a directory and as an archive, gives one digest. `verify` confirms it against the release manifest, a changed file fails verification, and `build` does not overwrite an archive. |
| `runcmd_test.go` | `run`'s usage errors, which exit with status 2. Arguments, a bundle or a profile file that do not open exit with status 1, and print nothing on standard output. A synthetic folder's flow, run `warm` and `fresh`, reads a host's secret through its arguments. A run that succeeds exits 0 with its result, trace and CPU profile, a run that fails exits 1, and a run without the harness runtime does not start. |
| `runcmd_test.go`, the resource store | On `-resource-store memory`, each run starts without the state the last one wrote. An unknown store, `redis` without `WL_REDIS_URL`, an unreachable Redis or Sentinel, and URLs that do not parse do not start; each runs in an engine process of its own, whose whole standard error holds no password. With `WL_TEST_REDIS_URL` ([wl/redisstore](../../wl/redisstore/README.md#tests)), two engine processes on one Redis share the state: the second's create is refused as taken, and it reads the first's value. |
| `host_test.go`, `argsfile_test.go`, `readcache_test.go`, `tracedetail_test.go`, `foreign_test.go`, `resultpolls_test.go` | Host mode end to end, on the Bun host `testdata/host/host.ts` ([Host-mode tests](#host-mode-tests)) |
| `memory_test.go` | What `warm` step modules cost a host process ([Memory tests](#memory-tests)) |
| `realm_test.go` | What native invocations leave in a long-lived Bun process ([Native realm tests](#native-realm-tests)) |
| `retain_test.go` | `TestSettledRunLetsGoOfItsValuesOnHosts`: on the plain host and on the plugin host, a run's step results are unreachable once the run has settled, while the runtime and its host go on |
| `bench_test.go`, `bench_large_test.go`, `bench_embed_test.go`, `benchfresh_test.go` | The benchmarks, and the tests that keep them working ([Benchmarks](#benchmarks)) |

### Test data

| Directory | What it is |
|---|---|
| `testdata/hostfolder/` | A synthetic sync folder and its host contract (`contract.yaml`). The tests build it once with `bundle.Build`. |
| `testdata/hostlib/` | `tenant.ts`, a host-provided module outside the sync folder. It is the `relative_path` of a codebases entry, whose script `f/hostout/read.ts` imports it. |
| `testdata/host/` | `host.ts`, a Bun host that embeds the SDK and registers its instances of the folder's host-provided modules. It serves its callers over HTTP on a Unix socket, and submits each run inside the caller's tenant scope. With `WL_SKIP_ARGS_FILE=1`, it connects with `skipArgsFile`, with origins in `WL_FOREIGN_WINDMILL`, with `foreignWindmillOrigins`, and with `WL_IDENTITY` set to a JSON object, with that `identity`. |
| `testdata/haltfolder/`, `testdata/halthost/` | A two-step flow `f/repro/prefix`, and a Bun host that `bun run` starts, for `TestHostRunHalts` |
| `testdata/pluginhost/` | A Bun host that `bun run` starts, for the bundled-plugin host tests: a reconnect, one connect, and two releases at one path |
| `testdata/memhost/` | A Bun host for the memory tests: it answers its resident set and peak (`/rss`) and the outcome of runs that it submits (`/run`) |
| `testdata/memory/` | `closure.package.json` and `closure.bun.lock`, which pin the packages of `TestModuleMemoryClosure`'s closure |
| `testdata/benchhost/` | `host.ts`, the benchmarks' warm host; `inproc.ts`, the in-process side of `BenchmarkEmbedder`; `threadcpu_darwin.c`, which reads CPU time by thread on macOS |

The [host-provided modules](../../docs/glossary.md#host-provided-module)
of `testdata/hostfolder/` are:

- `f/host/scopes.ts`, which holds the tenant scope;
- `f/host/pool.ts`, a pool of leases that the host releases at a run's
  `onSettled`;
- `../hostlib/tenant.ts`, outside the sync root;
- `f/host/lazy/a.ts` and `f/host/lazy/b.ts`: `a.ts` imports `b.ts` while
  it loads, and `b.ts` imports `a.ts` back only from a function.

### Host-mode tests

How these tests run:

1. The test compiles `testdata/host/host.ts` with `bun build --compile`.
2. The host's engine is the test binary itself: `TestMain` runs the
   command when `WL_TEST_ENGINE` is `1`.
3. A supervisor runs the host, and restarts it only when it exits.

| Test | What it shows |
|---|---|
| `TestHostScopesThroughAHostProvidedModule` | 16 concurrent runs, each in its own tenant scope, read their scope through the host-provided module, through the shim that the build generated: 16 of 16. A separately bundled copy of the module reads nothing. |
| `TestHostScopesThroughACodebaseScript` | `f/hostcb/cb_scope.ts`, a CommonJS codebase script, imports `f/host/scopes.ts`, which resolves to the module's generated shim. It reads the host's scope in 16 of 16 concurrent runs on Bun's CommonJS loader, and reads `Bun.env` through the env rewrite. |
| `TestHostScopesThroughAnOutsideModule` | `f/hostout/read.ts`, a codebase script, reads the host's scope through `../hostlib/tenant.ts`, a host-provided module outside the sync folder, in 16 of 16 concurrent runs. |
| `TestHostCodebaseAsset` | The codebase script `f/hostcb/cb_asset.ts` reads its asset, which the SDK extracted into the host's working directory, by a relative path and beside its module. It also reads the `args.json` that the engine wrote there. |
| `TestHostWorkdirRefusedBySDK` | The SDK refuses to connect with `workdir_mismatch` for a directory other than the host's own, and with `workdir_not_writable` for a read-only one. |
| `TestHostWorkdirRefusedByEngine` | The test plays the host: it starts `windmill-lite host` with a socket pair as file descriptor 3, and none as 4. It checks the welcome in the host's own working directory and the refusals `workdir_mismatch` and `workdir_not_writable`. It checks `engine_refused` for a bad bundle or store: an unknown store, or Redis without `WL_REDIS_URL`, unreachable, or with a bad URL, never showing its password. A malformed plugin selector gets `plugin_mismatch`; a usable one is welcomed. |
| | Each `-identity` file that does not open, or is not an identity (null, a second value, a null field, a key in another case), gets `engine_refused`; a usable one is welcomed. |
| `TestHostWorkdirRefusedByEngine`, origins of a real Windmill | It checks `engine_refused` for a `-foreign-windmill` origin with user information, a query or a fragment, or whose host the harness's URL parser refuses. Neither the refusal nor the engine's output shows the origin. |
| `TestHostDrain` | `drain` refuses a new run (`draining`), and waits for the run in flight, which completes. The host lives on. |
| `TestHostDrainAfterALargeOutcome` | A drain that starts while a run with a 3 MiB outcome is in flight (`f/host/big`) ends only after that run's caller has its whole outcome. |
| `TestHostRunSettled` | A step that its timeout abandons runs on: the run's outcome comes at about 1 s, and its `onSettled` at about 1.5 s, in the run's own tenant scope. A run that abandons nothing settles with its outcome. |
| `TestHostPassedDeadline` | A deadline that has already passed, which the SDK sends as 0 for a negative one, ends only its run, canceled with its cause: the hard deadline of a flow and of a root script, and a flow's pre-write deadline. The next run succeeds, and the host is never restarted. |
| `TestHostScriptOutcomeWithDeadline` | A root script's run that fails on its own, before its step or in it, ends `failure` with no cause, though its deadline is far off. A run past its deadline ends `canceled`, cause `deadline`, and one that the host cancels, with no deadline, cause `canceled`. Every run keeps its stored failure value: its own error, before its job or in it, or the killed job's `ExecutionErr` when the cancellation stopped its job. |
| `TestHostScriptRun` | A root script runs as a script job. A run of no target is refused. |
| `TestHostIdentity` | The SDK's connect `identity` reaches every job's environment, through `-identity`, in `f/host/identity`. A run's own `identity` replaces the fields it sets for that run. An identity field that is not a string refuses the run, and the next run on the same host succeeds. The host is never restarted. |
| `TestHostLazyImportCycle` | Two host-provided modules, whose cycle closes only through an `import()` inside a function, build. The root script `f/host/lazy_cycle.ts` reads both through their shims, gets `["ab", "ab"]` from the host's instances, and each module loads once in the process. |
| `TestHostFacade` | A step reads a variable of the bundle through the job API, at the `BASE_URL` that its invocation carries. |
| `TestHostRecycleModules` | The modules recycle trigger trips once a second module loads, in `health()` and `onRecycle`. |
| `TestHostRecycleRetired` | The retired-entry recycle trigger trips once the host's count of pooled entries that retired and were not released passes its maximum, in `health()` and `onRecycle`. The host lives on. |
| `TestHostPluginCases` | The cases of `conformance/plugins/hooks.yaml` in host mode, answered over `host_call` by the host's case plugin (`WL_PLUGIN_CASES`). Each run carries its own hooks and budgets in its run frame, and every hook runs in the run's tenant scope. The sites that ran come from the trace that the plugin's `onRunEnd` got. The plugin's report file (`WL_PLUGIN_REPORT`) shows every run settling after its hooks. |
| `TestHostBundledPluginCases` | The same cases with the case plugin built into the bundle and no `plugin` option: the SDK loads the bundle's plugin, and every case and every run's settlement hold. |
| `TestHostBundledPluginFetchOrder` | A `fetch` wrapper that the bundle's plugin installs at load reaches an ordinary step and not a native one: the SDK loads it after the runtime's hooks, as a spawned harness does. |
| `TestHostReconnectKeepsNativeFetch` | A host that connects on a bundle whose plugin wraps `fetch` at load, closes, and connects again on a bundle with no plugin: the native step never gets the wrapper, which the ordinary step still goes through. |
| `TestHostReconnectKeepsHostFetch` | A host with no bundled plugin that wraps `fetch` itself between two connections: the second connection's native and ordinary steps both go through its wrapper. |
| `TestHostVerifiesBundleBeforePlugin` | The bundle's plugin runs on the bundle as built, with no `-digest` pin, a matching one, or an empty `-digest=`. With a pin that the bundle does not match, also after `-data --`, or once another file of the bundle changes, `connect()` rejects with `bundle` and the plugin never runs. |
| `TestHostCheckBundle` | `host -check-bundle` exits 0 or 1 for a matching digest, a pin, an empty pin, a pin after `-data --`, another digest, an archive and another engine version. |
| `TestHostOverlappingPluginStarts` | Of two overlapping connects on one bundle path with different plugin bytes, the later is refused before it changes anything in the process, and the earlier still serves a platform call. |
| `TestHostRefusesStalePluginRelease` | After a second release, with another plugin file, replaces the bundle at the same path, a reconnect in the same process rejects with `bundle`, asking for a restart. |
| `TestHostSurvivesADetachedRejection` | One detached promise rejection does not end the host. Health counts it. |
| `TestHostNeverSettlingStep` | After one run's step times out, the other run's next step completes, a new run succeeds, and health reports one abandoned invocation. The host is never restarted. |
| `TestHostChildUnderBackpressure` | A step fills the host's socket, starts a child process and blocks the event loop. Its timeout still kills the child, which the host announced on the child channel. |
| `TestHostWatchdog` | A step spins forever, with a second run active. The engine kills the host within `killAfter` plus 1 s and names both runs. The second run's caller sees the exit, and a run on the restarted host succeeds. |
| `TestHostWatchdogFiniteCPU` | A step computes for 2 s, with `killAfter` 1 s and a step timeout of 10 s. The engine kills the host after about 1 s, and a run on the restarted host succeeds. With the watchdog off, the step completes. |
| `TestHostResourceRelease` | A step holds a lease of `f/host/pool.ts`, and uses it after its timeout abandons it. The host releases the run's leases at its `onSettled`, which comes only after the step's last use. |
| `TestHostVersionPairing` | An SDK stamped with another release (`bun run --define WL_RELEASE`) is refused with `version_mismatch` by an engine stamped through `WL_TEST_ENGINE_VERSION`. The SDK refuses a welcome from an engine of another release that does not check. The engine's own release is welcomed and runs. |
| `TestHostStaticIntercept` | The static form of the intercept hook, through the run frame |
| `TestHostStepLatency` | Reports the latency of each step in host mode |
| `TestHostReadCacheOption` | The SDK's `readCache` reaches the engine: the run lists the read-cache deviation, and without the option it does not. |
| `TestHostResourceStoreOption` | The SDK's `resourceStore` of kind `memory` reaches the engine, and the run lists the memory store's deviation, as it does without the option. |
| `TestReadCacheHostCalls` | On the plugin host, the read cache makes one call of the plugin's hook for each run and path, and every step sees the first read's value. Without it, each read is a call, as on Windmill. A value that holds `$WM_JOB_ID` resolves for each step's own job either way. |
| `TestHostArgsFileSharedReader`, `TestHostArgsFileHookReader` | By default, a step that reads `args.json` while the eight-step chain runs reads the chain's latest arguments, and a plugin's `onRunEnd` reads the run's last invocation's arguments. |
| `TestHostForeignWindmillLinks` | The SDK's `foreignWindmillOrigins` reach the host's steps through the engine. On the cases of [wl's fixture](../../wl/README.md#tests), a step's dispatch to the listed loopback server loses the step's own job ids, from the stock client and from a native step, and every other request keeps them. Without the option, every request keeps them. It also needs `WL_WINDMILL_CLIENT`. |
| `TestHostSkipArgsFile`, `TestHostSkipArgsFileOption` | The engine leaves `args.json` out only for a bundle with no reader whose host opted in. The SDK's `skipArgsFile` reaches the engine. |
| `TestHostResultPollsOption` | The SDK's `resultPollsForAllJobs` reaches the engine: on the result-polls fixture's flow, a step's polls of its run's jobs get the recorded answers, and without the option they answer 501, which fails the step. |
| `TestHostArgsFileReaders` | Steps that read `args.json` through a host-provided module, or by a file name that they build, read their own arguments back. |
| `TestPluginTraceDetailHost` | The plugin's `onRunEnd` gets the trace detail that it declares through `WL_BENCH_TRACE`: the whole trace by default and for `full`, the run's own fields for `summary`, and its id and status for `none`. |
| `TestHostCPUProfile` | `-cpuprofile`, passed among the SDK's engine flags, writes a whole profile of the engine once the host exits, and none before. |
| `TestHostRunHalts` | On one connection, the halt host runs `f/repro/prefix` with `haltAfter` its first module, then without. The first run ends `halted` after one module, and the second succeeds after two. A malformed halt is refused, and the next run succeeds. |

### Memory tests

`memory_test.go` runs `testdata/memhost/host.ts`, compiled, under the
supervisor of the host-mode tests. It builds generated sync folders with
`bundle.Build`, each in three layouts:

| Layout | What runs |
|---|---|
| per-script | Each step's bundle inlines its own copy of the shared module. |
| shared-instance | The shared module is host-provided, so the host's one instance serves every step. |
| shared-code | The build's shared code (`Config.SharedCode`): one closure for each build group, compiled once for each process, and an instance of it for each step module. A build that leaves a script on its own bundle (`shared_code_fallback`) fails the test. |

| Test | What it shows |
|---|---|
| `TestModuleMemory` | The public synthetic reproduction of the [shared module code ADR](../../docs/design/0013-shared-module-code.md): six scripts that each import one generated module of 40,000 distinct strings. Per script, each further module costs at least 3 MiB; with one shared instance or with shared code, at most 1.5 MiB. Per script and with shared code, each step counts its own calls in the module's state, as on Windmill. |
| `TestModuleMemoryClosure` | Only with `WL_MEMORY_PROBE` set. The resident set and the peak at connect, with one module, with all 34, with eight concurrent runs, and after a soak of 1,000 runs. With shared code, at most 16 MiB for each further module, and a peak of at most 768 MiB after the soak (`closurePeakBudget`), half of a container limited to 1,536 MiB. |

- `TestModuleMemoryClosure`'s closure has 34 CommonJS codebase scripts of
  about 6.9 MB each. They import one module outside the sync folder, which
  imports mongoose, `windmill-client`, js-yaml, Babel's parser and types,
  and a generated package shaped like an API client.
- The test installs those packages, so it needs the network once. Its
  shared module and the generated package load `node:` built-in modules by
  `import()`, as published packages do.
- It logs each budget on a line of its own:
  `Budget <name>: <value> MiB, at most <limit> MiB`. With
  `WL_MEMORY_VERDICT=median`, it fails on neither, and the caller judges
  the median of several runs, as the `Benchmarks` workflow does.

`WL_MEMHOST_COLLECT` sets the memory host's
[collect trigger](../../harnesses/bun/README.md#collect-garbage). Unset
or empty, the host has no trigger. `<mark>` and `<cooldown>` are decimal
digits:

| Value | The trigger |
|---|---|
| `<mark>` | A mark of `<mark>` bytes, with the SDK's default cooldown and policy |
| `<mark>,<cooldown>` | A cooldown of `<cooldown>` ms as well |
| `<mark>,<cooldown>,<policy>` | A policy as well: `heartbeat` or `idle`. Without one, the SDK's default, `idle`, applies. |

- Any other value, or a mark of 0, stops the host before it starts the
  engine. The value holds no `:`, so a build of the `Benchmarks` workflow
  can set it
  ([Comparing builds on GitHub's runners](../../docs/benchmarks.md#comparing-builds-on-githubs-runners)).
- `/rss` also gives the trigger's collections so far (`collections`, 0
  without the trigger) and how long they took in all, in ms (`collectMs`).
- `TestModuleMemoryClosure` logs them for each layout on a line of its
  own: `<layout>: collections of the collect trigger <n> at connect, …`.

The host reads its own peak: `VmHWM` from `/proc/self/status` on Linux,
and `getrusage`'s peak on macOS. On Linux, `getrusage`'s peak keeps the
peak of the process before its `execve`, the Go test process. A peak below
the resident set fails the test.

### Native realm tests

`realm_test.go` checks what native invocations leave in a long-lived Bun
process: in the host in host mode, and in the `warm` harness otherwise.
Its synthetic flows each run ten native steps that return their argument:

- `f/native/steps`: `bunnative` and `nativets` steps in turn, one of them
  a `nativets` step that imports the embedded client;
- `f/native/varying`: the same, with a `nativets` step that returns its
  argument through a direct `eval` in place of the `bunnative` one.

The probe step `f/native/probe` reads, after a full collection, the
process's count of JavaScriptCore `Script` objects, its JavaScript heap and
its resident set.

| Test | What it shows |
|---|---|
| `TestNativeInvocationsKeepNoScript` | After runs that load every module, 200 and then 400 native invocations leave fewer than 5 more `Script` objects. `f/native/varying` gets a new variable in each invocation from the environment hook ([host hooks ADR](../../docs/design/0008-host-hooks.md)); its 16 runs after the first may keep at most 16 more. |
| `TestNativeVariableNames` | A variable whose name is not a usable identifier, such as `process`, `while`, `WL-DASHED` or a name that is not plain ASCII, gets no `const` ([WR-45](../../docs/semantics/languages.md#wr-45)), so the run returns its input. A variable named `arguments` gets one, so each of 20 runs fails at the step that uses `eval`, with `Cannot declare a variable named arguments in strict mode.` |

CI runs the native realm tests by name, so that a skip fails the job
([docs/ci.md](../../docs/ci.md#the-go-jobs)).

## Benchmarks

Most benchmarks measure the step boundary end to end: synthetic flows run
with the engine in the benchmark's own process, so that the benchmark
reads each run's trace and the engine's CPU time. `BenchmarkArgsFileOps`
measures file writes alone.
[docs/benchmarks.md](../../docs/benchmarks.md) describes each benchmark,
the topologies, the stages and metrics, and how to compare builds. The
recorded baseline is in the
[JSON at step boundaries ADR](../../docs/design/0005-json-step-boundaries.md#baseline).

| Benchmark | File | What it measures |
|---|---|---|
| `BenchmarkStepBoundary` | `bench_test.go` | An eight-step chain, with payloads from 26 B to 4 MiB |
| `BenchmarkHostShapes` | `bench_test.go` | Flows shaped like a host-mode embedder's, 50 to 600 steps |
| `BenchmarkRetainedHeap` | `bench_test.go` | The average net growth of the engine's Go heap per finished run, which can be negative and stays near zero when runs are released |
| `BenchmarkPassThrough` | `bench_large_test.go` | A large value passed on unchanged through steps and subflows |
| `BenchmarkExpressions` | `bench_large_test.go` | Expressions on a large value |
| `BenchmarkRunPeaks` | `bench_large_test.go` | The peak memory of the engine and the host over one run, on Linux |
| `BenchmarkEmbedder` | `bench_embed_test.go` | Twelve workloads on windmill-lite against the same steps in one Bun process |
| `BenchmarkArgsFileOps` | `bench_embed_test.go` | The CPU of writing a step's `args.json` |
| `BenchmarkNativeRealm` | `realm_test.go` | What each native invocation leaves in a long-lived process |

Every benchmark but `BenchmarkArgsFileOps` needs Bun 1.4.3 in `WL_BUN`
and the build helper's dependencies, and fails without `WL_BUN`. One run
of two of them, in one process:

```sh
WL_BUN=/path/to/bun-1.4.3/bun go test ./cmd/windmill-lite -run '^$' \
  -bench 'BenchmarkStepBoundary|BenchmarkHostShapes' -benchtime 3s -count 3
```

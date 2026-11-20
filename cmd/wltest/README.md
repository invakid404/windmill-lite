# cmd/wltest

**Current state:** working. `wltest` has four verbs. `record` records flow
[fixtures](../../docs/glossary.md#fixture) and expression cases on the
oracle, `stub` writes the stubbed copy of a
[sync folder](../../docs/glossary.md#sync-folder)'s tree, `check` runs the step cases, flow fixtures and expression
cases on windmill-lite, and `ledger` guards against a retarget ledger. A
fifth verb, `diff`, prints that it is not implemented and exits 2.

This README covers the `wltest` command in this directory, its `testdata/`,
and `noderun.mjs`, the Node driver that `check` embeds.

`wltest` is the runner of the conformance cases and of the
[oracle](../../docs/glossary.md#oracle), the unmodified Windmill that the
conformance tests take as the truth:

- **`record`** runs [fixtures](../../docs/glossary.md#fixture) on the oracle
  through [internal/oracle](../../internal/oracle/README.md), and writes their
  [recordings](../../docs/glossary.md#recording). It runs only where Docker
  and the oracle's images are, which is public CI.
- **`check`** runs the same cases on windmill-lite, through the public
  [wl](../../wl/README.md) library and the
  [engine](../../internal/engine/README.md), and compares each run with its
  expectation or its recording. It needs the pinned Bun and a checkout of
  this repository, whose Bun harness and build helper it uses, with the
  build helper's dependencies installed.

The case formats, the normaliser and the comparator are in
[internal/conformance](../../internal/conformance/README.md); the cases are
in [conformance](../../conformance/README.md). The
[ADR](../../docs/glossary.md#adr)
[Differential testing and performance measurement](../../docs/design/0011-differential-testing.md)
defines what `wltest` does.

## Usage

| Verb | What it does | What it needs |
|---|---|---|
| `record` | Runs flow fixtures and expression files on the oracle, compares each run with its `expect` when it has one and with its committed recording when there is one, and writes recordings | Docker, the pinned images, `npx` on `PATH`, and disk space ([record](#record)) |
| `stub` | Writes the stubbed copy of a sync tree ([stub](#stub)) | Nothing but the tree |
| `check` | Runs step files, flow fixtures and expression files on windmill-lite, and compares each run ([check](#check)) | Bun 1.4.3 and a checkout of this repository with the build helper's dependencies; Node only for the Node side of a CommonJS codebase case that runs with the Bun boundary; Redis for `-resource-store redis` |
| `ledger` | Fails while a retarget ledger or any of its markers exists ([The ledger guard](#the-ledger-guard)) | A Git checkout |
| `diff` | Prints that it is not implemented, and exits 2 | — |

| Verb | Arguments | Required flags |
|---|---|---|
| `record` | One or more fixture directories, expression files or directories | None |
| `stub` | Exactly one sync tree | `--out DIR`, a directory that does not exist yet |
| `check` | One or more step files, fixture directories, expression files or directories | None; the Bun comes from `--bun`, `WL_BUN` or `PATH` |
| `ledger` | None | None ([its flag](#the-ledger-guard)) |
| `diff` | Any; they are ignored | — |

`wltest -h`, `--help` and `help` print the usage and exit 0. `wltest` with no
verb, or with an unknown one, prints the usage and exits 2.

### Build wltest

`wltest` links the QuickJS-ng engine through cgo, so it needs a C compiler.

1. From the repository root, run `go build -o bin/wltest ./cmd/wltest`.
2. Run `bin/wltest -h` to see the verbs.

`bin/` is ignored by Git.

### Exit codes

| Verb | 0 | 1 | 2 |
|---|---|---|---|
| `record` | No run failed, was refused, was unstable or mismatched its `expect`, and, with `--check`, none is new or changed. Skipped runs count as neither, so a session whose runs were all skipped exits 0 | A run failed, was refused, was unstable or did not match its `expect`, or, with `--check`, a recording is new or changed; or a fixture or file does not load, none is found, or the session could not run | A usage error: a bad flag or flag value (`--workers`, `--repeat`, `--listen-interval`), no argument, or an explicit file of another kind |
| `stub` | The tree was written | The tree was refused, or could not be written | A usage error: no `--out`, or not exactly one tree |
| `check` | Every case and run passed or was unsupported | A case or run failed, a file or fixture is invalid, or an argument cannot be read | A usage error, such as a bad flag, no argument, an explicit file of another kind, an unknown `--mode` or `-resource-store`, `host` with step files, no case found, no repository root found without `--repo`, or `-resource-store redis` without `WL_REDIS_URL` |
| `ledger` | No ledger and no marker | A ledger file or a marker exists, a marker is malformed, or the tree cannot be read | A usage error, such as a positional argument |
| `diff` | — | — | Always |

`record`, `stub`, `check` and `ledger` print their flags on `-h` and exit 2,
as Go's flag package reports `-h` as a parse error. `diff -h` prints that
`diff` is not implemented, and exits 2.

## record

```sh
bin/wltest record [flags] <fixture, expression file or directory>...
```

Each argument is a fixture directory (one that holds `fixture.yaml`), an
expression-case file, or a directory searched for both.

- **A directory** is searched for fixtures, and for expression files: `.yaml`
  files other than `deferred.yaml` whose directory is named `exprs`. The
  search does not look for expression files inside a fixture.
- **An explicit file** is taken when its name has that form: a `.yaml` file,
  not `deferred.yaml`, whose directory is named `exprs`, wherever that
  directory stands. Any other file is refused with exit 2.

- **Order.** Arguments that overlap reach each fixture and file once. Flow
  fixtures are recorded first, then the expression files, with the files
  marked `slow` last, so that what a slow case leaves running on the oracle
  (an evaluation that spins, [JE-2](../../docs/semantics/expressions.md#je-2))
  cannot slow the others.
- **Expression files.** `record` lifts each file into a generated one-step
  flow fixture in a temporary directory
  ([internal/conformance](../../internal/conformance/README.md#lifting)), and
  writes its recordings to `conformance/exprs/recorded/<finding>/<case>.json`.
- **What it needs.** Docker, which pulls the Windmill Community Edition
  `v1.862.0` image and PostgreSQL (the Windmill image was 1,373 MB
  compressed on amd64 when [OR-2](../../docs/semantics/oracle.md#or-2)
  measured it, at `v1.767.0`);
  and `npx` on `PATH`, to run `windmill-cli@1.862.0`. A pull is refused
  without enough free disk ([OR-30](../../docs/semantics/oracle.md#or-30)),
  with `insufficient_disk`; the thresholds are in
  [the disk policy](../../internal/oracle/README.md#the-disk-policy).
- **Where it runs.** The Oracle workflow runs `record` in public CI
  ([docs/ci.md](../../docs/ci.md#oracle)).

A session's steps, its token handling, stub mode, groups and workspaces are
the oracle harness's ([internal/oracle](../../internal/oracle/README.md#a-session)).

### record's flags

| Flag | Default | What it does |
|---|---|---|
| `--check` | off | Fail when a run has no committed recording or its observation differs from it. No recording is written to its default place in the fixture; with `--out`, recordings go under that directory, so `--out .` from the repository root writes into the fixtures. `--regenerate-metadata`, which the Oracle workflow passes with it, still rewrites the sync trees' metadata in place |
| `--out DIR` | — | Write new and changed recordings under `DIR`, at their committed path (`DIR/<fixture path>/recorded/`, `DIR/conformance/exprs/recorded/<finding>/`), instead of in place |
| `--mismatched DIR` | — | Write the observation of every `expect_mismatch` run under `DIR` ([Mismatched observations](#mismatched-observations)). `DIR` must not be the working directory or the `--out` directory |
| `--raw DIR` | — | Write the raw API captures of each repetition of a run that reaches the walk to `DIR`. A skipped run, or one that fails before its walk, has none |
| `--report FILE` | — | Write the session report: disk readings, pulls, images, boot times, the CLI version, deployments, memory peaks and the teardown, and every run's outcome |
| `--repeat N` | 1 | Run each run `N` times, and require equal observations |
| `--regenerate-metadata` | off | Run `folder add-missing` and `generate-metadata` in each fixture's sync tree, in place, before the push, so that CI can diff the tree ([OR-8](../../docs/semantics/oracle.md#or-8)). A fixture with `oracle: {deploy: api}` is never regenerated, and a generated tree always is |
| `--workers N` | 1 | Worker containers, 1 or 2 ([OR-5](../../docs/semantics/oracle.md#or-5)) |
| `--skip-unmet-workers` | off | Report the runs of a fixture that needs more workers as `skipped`, instead of refusing them |
| `--session NAME` | `""` | The oracle configuration: `native`, `long`, `result_size` or `flow_script_off`; empty is the reference configuration. Only the fixtures whose `oracle.session` names it are recorded; an unknown name is refused ([internal/oracle](../../internal/oracle/README.md#oracle-configurations)) |
| `--skip-unmet-session` | off | Report the runs of a fixture that needs another configuration as `skipped`, instead of refusing them |
| `--listen-interval N` | 0, which means the oracle's 1 | The oracle's `LISTEN_NEW_EVENTS_INTERVAL_SEC`, which is in every recording's provenance ([OR-7](../../docs/semantics/oracle.md#or-7)) |
| `--cli CMD` | `npx -y windmill-cli@1.862.0` | The command that runs the CLI. Its `--version` must report exactly `1.862.0`; a pre-release such as `1.862.0-beta` is refused |
| `--docker-data-root DIR` | Docker's data root | The directory whose filesystem the disk preflight measures |
| `--sink-listen ADDR` | `0.0.0.0:0` | The effect sink's address. The oracle reaches it through `host.docker.internal`, which on Linux is the Docker bridge gateway, so the default listens beyond loopback. On a shared network, pass a narrower address |
| `--run-timeout D` | `5m`; `0` also means 5 minutes | Bounds a run's wait for its job to complete, and a run and wait's response and poll; the asynchronous submissions and the walk use the session's context. A value below 60 s is raised to 60 s ([OR-10](../../docs/semantics/oracle.md#or-10)). When it passes, the session tries to cancel the job, and waits up to 30 s for that |

### What record prints

One line per run: the outcome, the fixture and the run, then the file that it
wrote, if any. Under the line come the file of a mismatched observation
(`observation: <file>`), any error (`error: <text>`), and the differences as
JSON Pointers, actual before expected, such as
`/steps/m:b/args/tag: "ho" != "hi"`.

| Outcome | Meaning | Exit 1 |
|---|---|---|
| `unchanged` | The observation equals the committed recording | no |
| `new` | The run had no recording. One is written in place, or under `--out` when it is given; with `--check`, only under `--out`, and without `--out` nothing is written | with `--check` |
| `changed` | The observation differs from the recording. The new one is written as for `new` | with `--check` |
| `unstable` | Repeated observations differ | yes |
| `expect_mismatch` | The observation does not match the run's `expect` | yes |
| `failed` | The run failed in the session | yes |
| `refused` | The harness refused the run, such as one whose committed recording names another oracle version, image or CLI than the pins (`recording_identity_mismatch`); delete the recording to record it again | yes |
| `skipped` | A fixture that needs more workers or another configuration, under the skip flags, or one with provided sites, which only windmill-lite runs | no |

A refusal of the whole session, such as
`{"error":"insufficient_disk",…}`, is printed as JSON on standard output. A
run's `claims`, `delays` and `unit` cases are not checked here:
`go test ./internal/oracle` checks them against the committed recordings, so
a contradicted claim still leaves a recording to inspect.

### Mismatched observations

A run whose observation does not match its `expect` is `expect_mismatch`,
and writes no recording, in place or under `--out`: a recording must meet
its run's `expect`. With `--mismatched DIR`, `record` writes that observation
under `DIR` instead, in the recording format with its provenance
(`{"observation": …, "provenance": …}`), at the path that `--out` would give
its recording.

- **The path** is `DIR/<fixture path>/recorded/<run>.json`. An expression
  case's run has claims but no `expect`, so it never ends as
  `expect_mismatch`.
- **The run** stays `expect_mismatch` and still fails the session. The
  `observation: <file>` line under the run's line, and the `observation`
  field of its entry in the report (`--report`), name the file.
- **An `unstable`, `failed` or `refused` run** writes nothing there. An
  unstable or refused run has no single observation. A failed run may have
  one, but failed for another reason.
- **A file that cannot be written**, or a fixture outside the working
  directory, makes the run `failed`, with the error and the differences.
  The same holds for a recording that cannot be written: a stable,
  completed run then ends as `failed`.

The Oracle workflow passes `--mismatched`, and uploads the directory with
the recordings, the raw captures and the report as the job's artifact
([docs/ci.md](../../docs/ci.md#oracle)). To act on a mismatch from CI:

1. Download the artifact of the failed `Oracle record (<session>)` job.
2. Read the run's lines in the job's log: the differences show what the
   oracle did against what the `expect` says.
3. If the oracle is right, change the run's `expect` to say what the oracle
   now does.
4. Copy that run's file only, from the repository root:
   `cp DIR/<fixture path>/recorded/<run>.json <fixture path>/recorded/`,
   with `DIR` the artifact's mismatched directory. It is now the run's
   recording. The directory can hold other runs' observations: review each
   before you copy it.
5. Run `bin/wltest check <fixture path>`, and push. `check` compares a run
   with its `expect` when it has one, and with the recording only when it
   has none. The next `record --check` reports the run `unchanged` when the
   oracle observes the same again.

## stub

```sh
bin/wltest stub --out <dir> <sync tree>
```

| Flag | Default | What it does |
|---|---|---|
| `--out DIR` | — (required) | The directory to write the stubbed tree to |

`stub` copies the sync tree, the directory of its `wmill.yaml`, to `<dir>`,
with the source of every Bun script and every `!inline` Bun rawscript
replaced by a stub. The stub keeps `main`'s declaration with its parameter
list and the type declarations, never calls it, and exports a function under
the name `main` that asks the replay service for its outcome
([internal/oracle](../../internal/oracle/README.md#stub-mode)).

- `<dir>` must not exist, and must not lie inside the tree or a directory
  that the copy carries. File identity decides it, so a symbolic link or a
  case alias counts.
- When the tree's codebase entries name paths outside it (`relative_path`,
  assets' `from`, `inject`), `wmill.yaml` is kept as written and those paths
  are carried at the same places relative to the stubbed tree
  ([internal/oracle](../../internal/oracle/README.md#copies-of-a-sync-tree)).
  The stubbed tree is then `<dir>/<root>`.
- The copy's metadata still describes the originals. Before pushing it, run
  `npx -y windmill-cli@1.862.0 generate-metadata` in the stubbed tree; `stub`
  says so on standard error.

It prints, on standard output:

| Line | Meaning |
|---|---|
| `stubbed <file>` | One per replaced source |
| `carried <path>` | One per path outside the tree that the copy carries |
| `made <dir>` | One per directory outside the tree that a spelling passes through, made empty |
| `tree <root>` | Where the stubbed tree is below `<dir>`, when it is not `<dir>` itself |
| `{"error":"stub_unsupported","file":…,"step":…,"reason":…}` | A refused tree, with the `file` or `step` where known: exit 1. An executed script would keep its body, or a retry on a loop or branch would make replay calls that the walk cannot match ([the refusals](../../internal/oracle/README.md#the-stubbed-push)) |

A refusal is printed as JSON on standard output, and as an error on standard
error. Any other failure, such as a copy or I/O error, goes to standard error
only.

## check

```sh
WL_BUN=/path/to/bun bin/wltest check [flags] <step file, fixture, expression file or directory>...
```

`check` runs the step cases of `conformance/steps`, the flow fixtures of
`conformance/flows` and the expression cases of `conformance/exprs` on
windmill-lite. Each argument is a step file, a fixture directory, an
expression file, or a directory searched for all three. An explicit file of
any other kind is refused (exit 2).

### check's flags

| Flag | Default | What it does |
|---|---|---|
| `--mode M` | `warm` | `warm`: one harness process serves many invocations. `fresh`: each step, or each run of a step case, gets a process of its own. `host`: flow fixtures and expression files only, on a Bun host that embeds the host SDK ([Host mode](#host-mode)) |
| `--bun PATH` | `WL_BUN`, then `bun` | The Bun executable. The build refuses any Bun but the pinned 1.4.3 |
| `--node PATH` | `WL_NODE` | The Node executable that runs CommonJS codebase cases as Windmill does; empty skips that side |
| `--repo DIR` | the nearest directory up from the working directory that holds `harnesses/bun/runtime/main.ts` | The repository root, whose build helper needs its dependencies installed |
| `-v` | off | Print the harness's diagnostics of a failed case or run |
| `-shared-code` | off | Build every bundle with shared code ([Shared module code](../../docs/design/0013-shared-module-code.md)): `warm` and `host` run the scripts' shared-layout modules, and `fresh` their per-script bundles. This is a [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations) |
| `-quickjs-only` | off | Evaluate every expression that the shortcuts and member paths do not answer on QuickJS-ng, without the expression pipeline's Go evaluator (`wl.Options.QuickJSOnly`) |
| `-resource-store S` | `memory` | The resource store of each flow fixture's runtime: `memory`, or `redis` on the Redis that `WL_REDIS_URL` names, under a key prefix of the fixture's own, `wltest-<random>:` ([Run check on Redis](#run-check-on-redis)) |

Go's flag package accepts each flag with one dash or two.

### What check prints

| Line | Meaning |
|---|---|
| `pass <case> <file>`, `pass <fixture> <run>` | The step case or run passed |
| `fail <case> <file>`, `fail <fixture> <run>` | It failed. Each difference follows on a line of its own, and with `-v` the harness's diagnostics |
| `unsupported <fixture> <run>: <why>` | windmill-lite cannot run it yet ([Unsupported runs](#unsupported-runs)); it does not fail `check` |
| `invalid <file or fixture> <error>` | The file or fixture does not load |
| `<n> passed, <n> failed, <n> unsupported (mode <mode>)` | The summary |

Every required run passes with the Go evaluator on and with it off: with
`WL_TEST_QUICKJS_ONLY=1`, as one CI part sets it, the package's tests run
every flow fixture and expression case as `-quickjs-only` does
([docs/ci.md](../../docs/ci.md#the-go-jobs)). `-quickjs-only` changes only
how flow fixtures and expression files evaluate. Step cases evaluate no
expression, so that CI part runs the same step tests again, unchanged.

To write a new case, follow the steps in
[conformance](../../conformance/README.md): [add a flow fixture](../../conformance/README.md#add-a-flow-fixture),
[add an expression case](../../conformance/README.md#add-an-expression-case)
or [add a step case](../../conformance/README.md#add-a-step-case). The
Oracle workflow records flow fixtures and lifted expression cases; a step
case carries the checks its author writes, and the oracle never runs it.

### Check a step case locally

1. Build `wltest` ([Build wltest](#build-wltest)).
2. Export `WL_BUN`, the path of a Bun 1.4.3 executable:
   `export WL_BUN=/path/to/bun`.
3. Install the build helper's dependencies once, with that Bun:
   `(cd harnesses/bun/build && "$WL_BUN" install --frozen-lockfile)`.
4. For a CommonJS codebase case that runs with the Bun boundary, point
   `WL_NODE` at Node, or its Node side is skipped.
5. Run the file in both modes, for example
   `bin/wltest check conformance/steps/bun/wr-12.yaml` and
   `bin/wltest check --mode fresh conformance/steps/bun/wr-12.yaml`.

### Check a flow fixture locally

1. Do steps 1 to 3 above.
2. Run the fixture's directory, for example
   `bin/wltest check conformance/flows/oracle/smoke`.
3. Run it in the other modes: `--mode fresh` and `--mode host`.
4. Read the lines under a `fail` line ([Read a failure](#read-a-failure)).

An expression file is checked the same way:
`bin/wltest check conformance/exprs/<finding>.yaml`.

### Read a failure

Each line under a `fail` line is one problem:

- **A difference** is a JSON Pointer into the observation, then the actual
  value before the expected one, such as `/steps/m:b/args/tag: "ho" != "hi"`.
- **A build or run error** names the script or the step that failed, such as
  a script that does not build.
- **A replay check that fails**, in stub mode, is `stub: ` followed by its
  error, such as a JSON object with the `error` code `replay_miss`.
- **A claim that does not hold**, or a delay, written
  `delay <from> to <to>: …` with the two sites.
- **`observed`**, then the whole observation as compact JSON. It follows the
  differences and claims of a run whose comparison or claims failed, so the
  reader sees what windmill-lite observed; a failed delay comes after it.
- **`the run has no expect, recording or claims to compare with`** for a run
  with nothing to compare.

`-v` adds the harness's diagnostics. A failure in CI names its job: run
`check` locally with the same flags, such as `-shared-code` or
`-quickjs-only`, or with `WL_TEST_QUICKJS_ONLY=1` for the package's tests
([docs/ci.md](../../docs/ci.md#the-go-jobs)).

### Run check on Redis

`-resource-store redis` gives each flow fixture's runtime the Redis store of
[wl/redisstore](../../wl/redisstore/README.md), under a key prefix of its
own, so each fixture starts with no state, as on the default memory store.

1. Do steps 1 to 3 of [Check a step case locally](#check-a-step-case-locally).
2. Start a Redis and set `WL_REDIS_URL` to its URL, such as
   `redis://127.0.0.1:6379/0`.
3. Run `bin/wltest check -resource-store redis conformance/flows/client-api/state`.

The `Redis` CI job checks `client-api/state` this way in each mode
([docs/ci.md](../../docs/ci.md#the-redis-job)).

## How check runs the cases

This part is for contributors who change `wltest`.

### Step cases

A step file's format is in
[internal/conformance](../../internal/conformance/README.md#step-cases). For
each step file, `check`:

1. **Builds** the cases' scripts and host-provided modules into one bundle:
   it writes them into a sync folder in a temporary directory, and builds
   them with the [Bun build helper](../../harnesses/bun/build/README.md)
   (`bundle.BuildBun`). The bundle's `manifest.json` lists the host-provided
   modules in registration order. Each script is built with the language
   that a deploy records (`ir.DeployedLanguage`).
2. **Runs** each case's runs in order through the harness protocol
   ([internal/harness](../../internal/harness/README.md)), each invoked with
   its language's boundary (`ir.BoundaryOf`) and `engine.Entrypoint`'s
   entrypoint.
   - In `warm` mode a case's runs share a harness process of its own, in
     which module state and `globalThis` survive from one run to the next.
   - In `fresh` mode each run has a process of its own, which ends with it,
     as a Windmill job's does.
   - A native step runs in a realm of its own every time, in either mode.
3. **Maps** the outcome to Windmill's stored value (`engine.StoreOutcome`),
   writes the run's own job id as the job placeholder of its site, and
   compares it (`conformance.CompareStep`).

A run's job variables are the job identity
([internal/platform](../../internal/platform/README.md#job-identity)) of step
`step_id` (default `a`) of the flow `f/wl_step/flow`, in the workspace
`wl_step`: the case's flow run. With `step_id: ""` they are those of a
script job at the case's path, which is its own effective root
([WA-V1](../../docs/semantics/client-api.md#wa-v1)).

**The job placeholder.** The site is `m:<step_id>`, or `m:a` for a script
job, so a case states the `job_id` of a wrapper or native error
([FX-12](../../docs/semantics/execution.md#fx-12)) as `"<job:m:a>"`, and a
flow step's `flow_path` as `f/wl_step/flow`. This is how the flow suite's
normaliser writes a walked job's id
([OR-14](../../docs/semantics/oracle.md#or-14)). Node runs are written the
same way.

**The platform.** Each case has a
[platform facade](../../internal/platform/facade/README.md) of its own, in
its default `fail` policy, through whose `Executor` the runs go:

- it serves the case's `setup`: its variables and resources, with a secret's
  value its plaintext, which the runner passes in as a host passes its
  secrets;
- it has an object store in a directory of the case's own;
- its job registry knows the case's flow run as a live flow job while the
  case runs, so the runs of a flow step's case share its flow user state, in
  `warm` and `fresh` alike;
- it has no dispatcher, so no dispatch target is in the bundle and every
  dispatch is refused
  ([restrictions](../../docs/design/0001-equivalence-contract.md#5a-restrictions));
- a warm process gets its non-listening origin as `BASE_URL` and intercepts
  its fetches; a fresh one gets the listening origin, which it reaches over
  TCP. A native step reads the same origin as `BASE_URL` and
  `BASE_INTERNAL_URL`.

**What a run is observed for:**

| Check | How |
|---|---|
| `calls` | What the facade recorded against the invocation, once it has settled, written as [the step case's `calls`](../../internal/conformance/README.md#step-cases) says |
| Settlement | Every run is observed up to its settlement, at most 10 s, because an outcome fixed early (an intercepted `process.exit`) comes before the call ends. A run that says `settled: pending` is observed for 500 ms instead |
| `warnings`, `left`, `untracked` | The warnings collected until settlement, and the counts of `left_behind` and `native_untracked` |
| `late` | Compared at the end of its case, when the run gives a non-empty `late` or a `settled`, so that a late report of work that the run left running after it settled fails the case. In `warm` mode the process is observed for 200 ms more when the last run gives a non-empty `late`, or a `settled` other than `pending` |
| Time | An invocation that has not returned in 30 s fails. A Node run is killed after 30 s, and what it left is compared as its outcome |
| `timeout` | The run is abandoned when the case's timeout passes, and its value is then `engine.TimeoutError`'s for the boundary |

**Shared code.** A step file with `shared_code: true` is built with shared
code whatever the flag, so its cases compare `warm`, which runs the
shared-layout modules, with `fresh`, which runs the per-script bundles. A
case's `scripts` add workspace scripts beside its `files`, so one process
runs several step modules that share a dependency.

**Codebase cases and Node.** A case with `codebase` is a codebase script:
`check` writes its `files` into the folder and builds it with esbuild
(`bundle.BuildCodebase`), with its entry's settings. With `--node`, each run
of a CommonJS codebase case also runs on Node, as Windmill runs such a
bundle ([WR-50](../../docs/semantics/languages.md#wr-50)), unless its header
makes it native. windmill-lite runs such a bundle on Bun's CommonJS loader
instead, a [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations),
and the Node side shows where the two differ:

1. `check` lays out a fresh job directory in Windmill's order: a placeholder
   `package.json` of `{}`, then the bundle as `main.js` and the entry's
   assets at their paths. So an asset named `package.json` replaces the
   placeholder, and its `type` decides how Node loads `main.js`.
2. It adds the metadata, the arguments and `noderun.mjs`, and runs
   `node --preserve-symlinks noderun.mjs <entry>`.
3. The driver applies the harness's own wrapper rules
   (`harnesses/bun/runtime/wrapper.ts`, transpiled for Node by the pinned
   Bun) in the harness's order, and writes the outcome as a result frame
   carries it. A callable export with no signature in the metadata is called
   with an empty parameter list.
   - **A written outcome** wins, however the process ended.
   - **A process that exits without writing it** ended as `empty` (status 0),
     or as `escaped`, with the last 600 bytes of its standard error. A
     process killed at the time limit ends the same way.
4. The outcome is mapped by `engine.StoreOutcome` and compared with the run's
   `node` check, or with its own `expect` and `claims`
   (`conformance.CompareStepNode`). A difference is reported as
   `run N on node: …`.

### Flow fixtures

A fixture's format is in
[internal/conformance](../../internal/conformance/README.md#fixtures).
`check` runs a few fixtures at a time (`flows.go`), each in four steps.

#### 1. Build

1. **Copy.** `check` copies the sync tree as `record` pushes it
   (`oracle.CopyLayout`, or `oracle.StubTree` in stub mode) into the
   fixture's work directory, and reads the copy, never the tree in place. The
   copy carries what the tree's codebase entries name outside it and nothing
   else. So a file around the tree, such as a `tsconfig.json` above it whose
   path aliases esbuild would read, reaches neither this build nor the
   oracle.
2. **Sort the targets.** It reads the copy with
   [internal/syncfolder](../../internal/syncfolder/README.md), compiles each
   run's target flow with [internal/ir](../../internal/ir/README.md), the
   target as the only root, and opens its program on the engine. So it sorts
   the targets it can run from those that the compiler or the engine refuses
   (unsupported) or cannot compile (failed).
3. **Roots and provided sites.** A fixture's `roots`, the targets its steps
   dispatch, are compiled together the same way. Its `provided` sites go to
   each compile that its roots reach, as the host contract's mandatory
   provided sites, whose targets stay opaque. A selector that a target's
   compile reports as matching no site is left out of that compile.
4. **Bundle.** It builds one [bundle](../../docs/glossary.md#bundle) with
   `bundle.Build` ([internal/bundle](../../internal/bundle/README.md)). The
   host contract's roots are the targets it can run and the `roots`, and its
   provided sites the fixture's. The bundle holds `wl`'s engine version, the
   module state of `--mode` (`warm` for `host`), the program, the Bun scripts that the roots
   reach, the codebase scripts built with esbuild, and the folder's
   resources and variables, but not its secrets.
5. **Name the scripts.** Each Bun script is named by its inventory id:
   `scripts/bun/<path>` for a path script, and `scripts/bun/inline-<16 hex>`
   for an inline step, with its build's extension
   ([internal/bundle](../../internal/bundle/README.md)). A job path
   ([WR-29](../../docs/semantics/languages.md#wr-29)) cannot name a script,
   because an inline step `a` of the flow `f/x` and a path script `f/x/a`
   share one.
6. **Isolate failures.** A build problem of one script (`bundle.ScriptItem`
   names it) fails the targets that reach it, and the bundle is built again
   without them. A folder that does not load, or a build that fails as a
   whole, fails every run.

#### 2. Run

`check` opens the bundle with `wl.Open` ([wl](../../wl/README.md)) in the mode
of `--mode`, on the pinned Bun and the repository's harness runtime. `host`
builds it warm and opens it in host mode ([Host mode](#host-mode)).

- **Providers.** The fixture's named `providers` become
  `wl.Options.Providers`: each answers its sites with its result or its
  error, or declines.
- **Host data** (`providers.go`): the folder's secrets, as the folder holds
  them (ciphertext without `plainSecrets`), then the variables and resources
  of its `setup.yaml`, which the oracle creates after the push and so
  replace the folder's. A setup applied before the push holds nothing at run
  time, because the push removes it
  ([OR-31](../../docs/semantics/oracle.md#or-31)). A setup's secret is its
  plaintext, passed in as a host passes its secrets.
- **The platform.** The runtime owns its facade, in its default `fail`
  policy; the store of the bundle's `data/` and the host data; the job
  registry; an object store in the fixture's work directory; and the default
  dispatcher over the bundle's program. So a step that dispatches one of the
  `roots` starts a run of the same runtime, whose calls are attributed.
- **Processes.** In `warm` mode every run goes to one pool of Bun harness
  processes; in `fresh` mode each step has a process of its own. The engine
  invokes each step with its boundary, a native one included.
- **Order.** Runs go in name order, a `group` at its first run's place with
  its runs concurrent (`conformance.RunBatches`, `groups.go`), each with
  `Runtime.Run`.
- **Groups.** A group is one test of concurrent outer runs, so it runs whole
  or not at all.
- **A group that cannot run.** A run of the group that is unsupported or fails before it
  is submitted keeps its own result. The group's runs that could run take
  one result for the group: unsupported, naming the first unsupported run
  and its reason, when there is one; otherwise failed with
  `group_member_failed`, naming the first failed run. Diagnose the runs that
  have their own reason first.
- **Holding a group.** Each run of a group waits at its first module,
  through the module-start hook ([wl](../../wl/README.md#hooks)), until every
  run of the group is in flight, or for 10 s at most. A run's span runs from
  its submission to its return. After the runs, every run with no problem
  of its own fails with the group's error when one of its runs failed
  (`group_member_failed`), or when one started after another ended, or at
  the same instant (`group_not_concurrent`).
- **Cancellation.** `cancel_after_ms` cancels a run's context with a
  `wl.Cancellation` that names the oracle's reason and canceler
  (`oracle.CancelAfterReason`, `wltest: cancel_after_ms`, by
  `oracle.CancelAfterCanceler`, `admin`), which the engine's `Canceled`
  result quotes ([FX-38](../../docs/semantics/execution.md#fx-38)).
- **Ids.** Each run has its own id, which its steps see as `WM_ROOT_JOB_ID`,
  and its top-level steps as `WM_FLOW_JOB_ID` and `WM_ROOT_FLOW_JOB_ID`.
  `WM_ROOT_FLOW_JOB_ID` names the innermost flow scope, so a subflow's steps,
  and a parallel iteration's, see their own scope's id.
- **Job ids and paths.** The trace records each step's
  job id (`WM_JOB_ID`), superseded attempts' included, with its site. The
  engine names an invocation's script by its inventory id, and gives its job
  path as `WM_JOB_PATH`.

#### 3. Observe

`observe.go` turns the run's trace into the observation that the oracle's
walker produces ([OR-11](../../docs/semantics/oracle.md#or-11)):

- **`not_run` sites.** A module of the flow, or of a child flow that the
  trace lists, that the trace has no record of is `not_run`. A child flow is
  a loop iteration's body, below its iteration's site `m:<loop>/i:<n>`; a
  branch's, below `b:<n>` or `b:default`; or a subflow's, below its step's
  site. So is the failure module's site `s:failure` of a flow that has one, a
  child flow's copy of it included.
- **Stored forms.** The root's result and each site's `args` and `result`
  are in their stored form, as the job API returns them (the ADR
  [JSON at step boundaries](../../docs/design/0005-json-step-boundaries.md)).
  A value with no stored form
  ([FX-48](../../docs/semantics/execution.md#fx-48)) is compared as its
  producer wrote it, so the comparison shows what Windmill would refuse.
- **`carried` and `resolved_args`** keep the trace's texts, the value carried
  on in memory and the arguments that the script received, and are written
  only where their text differs. The top-level members of `resolved_args`
  come in jsonb's key order, as those of `args` do. The engine shuffles a
  step's argument keys in each run, because Windmill leaves their order
  unspecified, so the observation must not depend on it.
- **No result, no `carried`:** a site with no result shows no `carried`. A
  module that failed with no job, at the root's pickup, carries its error to
  what follows it, where the observation shows it.
- **Normalisation** ([OR-14](../../docs/semantics/oracle.md#or-14),
  [the normaliser](../../internal/conformance/README.md#the-normaliser)). Job
  ids become the placeholders of their sites. The run's own id is the root,
  `<job:>`, and a child flow's id is its own site: its iteration's, its
  branch's, or its subflow step's. The id
  of a job that a module ran with no invocation, such as a skipped module's
  identity job, is its module's site.
- **Stacks.** The stack of a site whose job failed, ran and has a result is
  masked wherever its value is carried
  ([OR-V3](../../docs/semantics/oracle.md#or-v3)). Then the fixture's `stack`
  and `mask` pointers apply.
- **Unwalked job ids.** A job id that no record names but an error object
  gives as its `job_id`, such as a job of a flow that a step ran through the
  facade, becomes `<job:?N>`, numbered before any value is normalised. The
  normaliser's rules say which spellings and positions count.
- **Retries.** A failed attempt is a site `<module>/r:<k>`, and a job id
  becomes the placeholder of the site whose record names it, an attempt's
  own. A module's record
  lists the failed attempts of the flow-wide retry state, so an earlier
  module's attempt appears again under a later module whose status lists it
  ([FX-27](../../docs/semantics/execution.md#fx-27),
  [FX-29](../../docs/semantics/execution.md#fx-29)). When the listed attempt
  is a container's, its whole subtree is copied under the later attempt's
  site, nested attempts included, not only its own record
  (`TestObserveNestedAttemptCopies`).
- **Left out.** The virtual `<id>-v` step of a final sleep, as the walker
  leaves it out ([OR-V7](../../docs/semantics/oracle.md#or-v7)), a child
  flow's included. A record or child flow that the trace marks superseded:
  what a retried container's failed attempt ran besides the job that the
  attempt is recorded as ([CF-22](../../docs/semantics/control-flow.md#cf-22)),
  and a collapsed iteration's job that was never pushed.
- **The platform origin.** The runtime's platform origin, which a native step
  reads as `BASE_URL` and `BASE_INTERNAL_URL` and its stack can hold, becomes
  the oracle's base URL, `http://localhost:8000`, unless a further digit of a
  port follows it (`asOracleBase`). Each side reads a value of its own
  deployment, which the oracle keeps stable
  ([OR-7](../../docs/semantics/oracle.md#or-7)). A Bun step's `BASE_URL` still
  differs: the oracle rewrites its `localhost`
  ([WA-2](../../docs/semantics/client-api.md#wa-2)).

#### 4. Compare

`check` compares the observation with the run's `expect`, `carried` and
`resolved_args` included, or with its recording when it has none, which
holds neither. Then it checks the run's `claims`. A run with nothing to
compare fails.

- **Location suffixes.** On the reference's side, `check` strips the
  location suffixes of a message that windmill-lite generated
  (`conformance.StripLocations`,
  [internal/conformance](../../internal/conformance/README.md#location-suffixes)).
  A value counts as generated when it is one of these:
  - a result whose origin the trace records as the engine, a harness or the
    facade;
  - a carried value that is the same value, in either member order;
  - a carried value that the engine set apart from the result, whose origin
    the trace records too;
  - an entry of a loop's or a `branchall`'s array that the trace points to
    as generated, at any depth.
- **Copies.** A copy of such an error that user code passes on, as a failure
  module or a later step that echoes a pickup error does, counts as generated
  only where the run's `copies` declare it. Equality alone never counts,
  because user code can write the same text.
- **Not-found locations** are stripped only at the values that the run's
  `not_found_locs` declares, and only where windmill-lite generated the value
  (`conformance.StripNotFoundLocations`).
- **Never stripped:** a carried value that differs from the result with no
  recorded origin, and a user's error beside generated ones.
- **Claims** are checked on windmill-lite's observation as it is, so a claim
  about a location suffix holds only on the oracle.
- **`delays`** are checked on the trace's job times: each `to` site's job
  starts at least `at_least_ms` after the `from` site's job ended.

After each run that returns a trace, one that timed out included, `check`
removes the files that its steps left in `/tmp` to count their attempts
across retries, which the fixtures name `wl-<fixture>-<job id>-<tag>` after a
job of the run (`removeCounters`). The removal is best effort: an error is
ignored. On Windmill they stay in the worker's `/tmp`, but a
windmill-lite run would leave them in the host's.

#### The run limit

| Limit | Plain build | A build with `-race` |
|---|---|---|
| A run that has not ended fails after, in every mode | 150 s | 450 s |
| The harness watchdog's threshold: how long a `warm` process may stay silent. `fresh` mode and `check`'s host mode turn the watchdog off | 30 s | 90 s |

[internal/racebuild](../../internal/racebuild/README.md) holds the factor
and why a race build needs it. Sleeps and retry delays are waited for on the
real clock. The engine's monitor of flows that hang between steps ends a
runaway expression's run after 60 s, as the oracle does after a first step
([JE-2](../../docs/semantics/expressions.md#je-2)).

#### Effects and stub mode

A fixture with `oracle: {effects: true}` or `stub: true` gets an effect
server of its own (`effects.go`): the oracle's effect sink (`oracle.Sink`),
with the replay service (`conformance.ReplayService`) at `/replay` below it,
as `record` serves them, on a free loopback port.

- **The hook.** Every invocation of a run gets the server's URL as
  `WL_EFFECT_SINK` through the engine's invocation-environment hook, which
  also records the site that each job (`WM_JOB_ID`) launches at. The hooks
  go to the run as the Go callbacks a host gives it (`wl.Hooks`:
  `InvocationEnv`, and a group's `OnModuleStart`), so the runner takes no
  path that a host does not have.
- **Effects.** The sink keeps the effects whose `root` is the run's id, so
  the runs of a group stay apart, and refuses any other root with 409
  ([OR-16](../../docs/semantics/oracle.md#or-16)).
- **Stub mode.** The runs run the stubbed copy of the sync tree that
  `record` pushes (`oracle.StubTree`), built instead of the tree, and each
  run's `replay` table answers its stubs. The service locates a stub's job
  at the site that the hook recorded: the module's site, never an attempt,
  because the service counts attempts itself.
- **The replay check.** After the run, `conformance.CheckReplay`, the check
  that `record` applies to its walk, holds the calls against the trace
  ([internal/conformance](../../internal/conformance/README.md#checkreplay)).
  A subflow step's job is its child flow's, and a superseded record is not
  walked. When this check fails, the observation is still compared, so the
  failing steps are shown too.
- **A job with no invocation.** A record that ran with no job is an identity
  job, or a script job that launched and failed before its invocation (an
  input transform's failure, a missing entrypoint, a canceled scheduled job).
  The oracle walks the latter as a script job that never called, so `check`
  does too, at its attempt site.
- **Telling them apart.** `check` tells them apart by the module that the
  site names: a script module that is not mocked, whose record is not
  flagged skipped and whose job failed.
- **The effects compared.** The run's effects are the sink's in arrival
  order, followed by the replay service's effect of each located call
  (`{site, attempt, step, job, flow, root_flow, root, args}`), as the oracle
  lists them, normalised with the observation's job ids. A call whose job the
  service could not locate got an error answer but has no effect, and a
  repeated call of one job adds none.
- **A refused tree.** A tree that the stub generator refuses
  (`stub_unsupported`) fails the fixture's runs.

#### Unsupported runs

A run is `unsupported` when windmill-lite cannot run it yet. The fixture's
other runs are checked, except a group's other runs, which inherit it, and
every run of a fixture that has `roots` and effects or stub mode.

| Run | Why |
|---|---|
| A run of a fixture that has `roots` and effects or stub mode | The default dispatcher's runs get no run hooks, so no `WL_EFFECT_SINK` |
| A script run (`kind: script`) | `check` runs flows |
| A run-and-wait run (`submit: run_wait_result`) | Its response is the HTTP adapter's, which windmill-lite does not have yet |
| A flow that the compiler refuses | An unsupported feature, a language without a harness, or a pinned hash |
| A flow that the engine refuses at open (`engine.ErrUnsupported`) | Such as a container as the failure module ([CF-31](../../docs/semantics/control-flow.md#cf-31)), a preprocessor, an `ai` sleep or timeout, a flow `skip_expr`, or a module kind that the engine lacks |

### Expression files

An expression file of `conformance/exprs` runs end to end as a generated
flow fixture, in a temporary directory. `check` lifts the file as `record`
does (`conformance.LiftExprFile`): one flow per case, whose module `b` runs a
sink script with an input `x`:

- **A transform case:** `x` is the case's JavaScript expression.
- **A `static` case:** `x` is the case's literal JSON value.
- **A `skip_if` case:** `x` is static `null`, and the expression is `b`'s
  `skip_if`.

The run is compared with the case's recording and its claims. A new case
with no recording yet is checked against its claims alone.

`check` takes such a file explicitly, and finds them in the directories that
it is given. `go test ./internal/expr` checks the same cases at unit level
([internal/expr](../../internal/expr/README.md)).

Two kinds of case differ from the oracle by a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
They can fail here, depending on the C library and the build, and the flow
suite does not require them:

- the transcendental `Math` cases of
  [JE-1](../../docs/semantics/expressions.md#je-1), whose last digit
  depends on the C library: the Linux runner's glibc and macOS each differ
  from Windmill's image on some of them;
- the case of [JE-48](../../docs/semantics/expressions.md#je-48) whose
  recursion of depth 1 000 fits the engine's stack limit, which windmill-lite
  scales to its build.

### Host mode

`--mode host` runs each fixture's runs on a Bun host that embeds the host SDK
(`testdata/flowhost/host.ts`), whose steps run in the host's own process over
the host-mode transport, as every host's do (the ADR
[Harness protocol](../../docs/design/0004-harness-protocol.md)). The checker
keeps the engine in its own process (`hostmode.go`): it opens the fixture's
bundle with `wl.Options.Host`, the runtime that `windmill-lite host` opens.
So each run's trace, its hooks and its cancellation stay the checker's, as in
`warm` and `fresh` mode.

- **The engine that the SDK spawns** is `wltest` itself (the test binary in
  `go test`), in proxy mode (`WLTEST_ENGINE_PROXY`). It passes the host's
  socket and child channel, its file descriptors 3 and 4, to the checker
  over a Unix socket, then waits until the checker has closed the runtime,
  when it exits.
- **Its own copies** of the descriptors stay open until then. On macOS,
  closing a socket while it is still in flight to the checker could end the
  connection before the checker received it.
- **Teardown** closes the runtime, then the host's socket and child channel,
  which the runtime does not own, and the proxy's connection. It waits for the
  host to exit, and copies the host's output, which went to a file, into the
  fixture's diagnostics.
- **The watchdog is off,** because the engine is not the host's child here.
  The watchdog's own cases run on the real topology, in
  [cmd/windmill-lite](../windmill-lite/README.md).
- **One generation per fixture.** The host lives as long as its fixture. A
  host lost before its engine closes, its process exited or its connection
  ended, fails every run that it took. Its output goes to a file, not a pipe:
  the proxy inherits it, and a pipe would keep the host's wait open while the
  proxy lives.
- **The host's environment** is the base environment that a spawned harness
  gets (`harness.BaseEnv`), plus `WL_ENGINE` (the `wltest` binary),
  `WL_BUNDLE` and `WLTEST_ENGINE_PROXY`. Its working directory is
  `<fixture work directory>/host`.

## The flow suite

`TestFlowSuite` runs every flow fixture of `conformance/flows`, and every
expression file of `conformance/exprs` as its generated fixture, in five
configurations: `warm`, `fresh` and `host`, and `warm` and `host` from bundles
built with shared code. Each fixture must load. Every required run must pass
in all five. The log gives the counts of passing, failing and unsupported
runs, and names the runs that pass without being required.

[`testdata/flowsuite.json`](testdata/flowsuite.json) lists the required runs:

| Field | What it holds |
|---|---|
| `about` | A description of the file |
| `implemented` | The work items whose runs the suite requires: ids of the plan's work items in the ADR [Go package layout and implementation milestones](../../docs/design/0012-layout-and-milestones.md), and of later ones |
| `runs` | Each required run, `<fixture directory>/<run>` from the repository root, or `<expression file>/<case>`, with the work item that requires it, which must be in `implemented` |

A run that a recording manifest's `gates`
([internal/oracle](../../internal/oracle/README.md#recording-batches)) gate on
a work item in `implemented` is required too, whether or not the file lists
it.

Every run that the suite finds runs, required or not; being required
decides only whether its failure fails the test. Do not remove a required
run from the file: no test stops it. The run then stays required while an
active manifest gate requires it; otherwise its failure is only reported.

## Decisions where the reference is silent

The comparison's own decisions, such as which location suffixes it strips
and which pointers it masks, are
[internal/conformance's](../../internal/conformance/README.md#decisions-where-the-reference-is-silent);
the differences windmill-lite accepts are the
[named deviations](../../docs/design/0001-equivalence-contract.md#5-named-deviations).

| Question | Choice | Why | Pinned by |
|---|---|---|---|
| Which runs must pass? | The runs that `flowsuite.json` lists, and those that an active manifest gate requires. Every other run runs, and is only reported | A run becomes required when the work that implements it lands, so a run that windmill-lite cannot pass yet does not block other work | `TestFlowSuite` (`requiredRuns`) |
| What does a run compare with? | Its `expect` when it has one, then its recording, then its claims alone; a run with none of them fails | An `expect` states what windmill-lite must do, `carried` and `resolved_args` included, which a recording does not hold | `TestCompareRun`, `TestFlowCheckerReportsDifferences` |
| How does a group fail? | As a whole: each run with no problem of its own takes a group diagnostic, `group_member_failed` naming the first failed run (not its error text), or `group_not_concurrent` | A group is one test of runs in flight together ([OR-15](../../docs/semantics/oracle.md#or-15)) | `TestRunGroup`, `TestStubModeAndEffects` |
| What makes the ledger guard fail? | The ledger file, and any well-formed marker in a tracked or trackable text file; a malformed one only in a Go or bun test file | A marker that a test uses as its skip message skips it with no failure, and the guard matches text, not skip calls, so it cannot tell such a use from another; prose elsewhere may quote a malformed form | `TestLedgerGuard`, `TestParseMarker` |
| Why does a job read of an ended run fail here? | The step fails with `UnsupportedPlatformCall`, where the oracle answers from the job's row. So `job-reads-ended`'s run `ended` fails against its recording; the suite does not require it, and the test checks that it fails with that error | Once a run ends, windmill-lite keeps only a small entry per job (its id, kind, root and path), not the full job record that a read returns ([named deviation D22](../../docs/design/0001-equivalence-contract.md#5-named-deviations)). The step's error quotes the 501's own text | `TestEndedRunJobRead` |

## Tests

### Run the tests

The tests that need Bun skip without it, unless `WL_REQUIRE_BUN` is set.

1. Build nothing: `go test` builds the package.
2. Export the variables of the table below that the tests you run need,
   `WL_BUN` first.
3. Install the build helper's dependencies once:
   `(cd harnesses/bun/build && "$WL_BUN" install --frozen-lockfile)`.
4. Run `go test -timeout 45m ./cmd/wltest`. The flow suite can take longer
   than `go test`'s default 10 minutes. To run one test, use `-run`,
   such as `go test -run '^TestStepSuite$' ./cmd/wltest`.

CI runs it differently: its Go jobs add `-race`, and pick packages and tests
with `-run` and `-skip` per job, so the flow suite runs on macOS in two
parts, and the Redis tests in the `Redis` job
([docs/ci.md](../../docs/ci.md#the-go-jobs)).

| Variable | Value | Tests that need it | When it is not set |
|---|---|---|---|
| `WL_BUN` | The path of a Bun 1.4.3 executable | Every test that runs a step, a fixture or a build | The tests skip, or fail if `WL_REQUIRE_BUN` is set |
| `WL_REQUIRE_BUN` | Any non-empty value | Turns the skips of `WL_BUN`, and of `WL_WINDMILL_CLIENT` when Bun is there, into failures | A missing `WL_BUN` or `WL_WINDMILL_CLIENT` skips |
| `WL_NODE` | The path of a Node executable; CI uses Node 24.22.1, the Node of Windmill 1.862.0's image | The Node side of the CommonJS codebase step cases | Without `WL_REQUIRE_NODE`: the step suite runs on Bun only, and `TestStepCheckerReportsNodeDifferences` skips. With it: those tests fail |
| `WL_REQUIRE_NODE` | Any non-empty value | Turns a missing `WL_NODE` into a failure of the tests that need Node | A missing `WL_NODE` skips the Node side |
| `WL_WINDMILL_CLIENT` | A directory whose `node_modules` holds `windmill-client@1.862.0` | `TestWindmillClientSharesTheClosure`, which copies the client into its fixture; `wltest check` never reads it | The test skips, or fails if `WL_REQUIRE_BUN` is set and Bun is there |
| `WL_TEST_QUICKJS_ONLY` | `1` | Runs every flow fixture and expression case with `-quickjs-only` | The Go evaluator stays on |
| `WL_TEST_REDIS_URL` | A Redis URL | `store_test.go` | Skip |

### Test files

| File | Tests | What they check | Needs |
|---|---|---|---|
| `check_test.go` | `TestStepSuite` | Every step case passes in `warm` and `fresh`, `d18.yaml`'s from a bundle built with shared code; with `WL_NODE`, every CommonJS codebase case holds on Node too | Bun |
| | `TestStepCheckerReportsDifferences`, `TestStepCheckerReportsNodeDifferences`, `TestStepCheckerIsolatesCodebaseFailures` | A wrong expectation of each kind fails in both modes. A wrong or unstated Node difference fails. A codebase case that does not build fails alone | Bun; Node for `TestStepCheckerReportsNodeDifferences` |
| | `TestStepCatalogue` | Every catalogue case of a finding with a step file is a step case or deferred | — |
| | `TestNativeRejectionIsItsInvocationsOwn`, `TestNativeFlowSteps`, `TestNativeGeneratedErrorsAreGenerated`, `TestScriptEnvsReachOnlyBunSteps`, `TestNativeCodebaseFlowStep` | Native steps in flows. An unhandled rejection fails only its invocation. Each kind of native step has its boundary, `BASE_URL` and `BASE_INTERNAL_URL`. Native load and transpile failures count as generated; a path script's `envs` reach only its Bun step, and a `//native` ESM codebase script runs natively | Bun |
| `flows_test.go` | `TestFlowSuite` | [The flow suite](#the-flow-suite) | Bun |
| | `TestCacheFixturesFreshAndWarm`, `TestSubflowRetryFixturesFreshAndWarm` | The `cache_ttl` fixture's runs, and the retried flow step's fixture, in `fresh` and `warm` | Bun |
| | `TestRunWaitOutcomes` | The flow outcomes of run-and-wait runs, submitted as ordinary runs, without the `response` | Bun |
| | `TestFlowCheckerReportsDifferences`, `TestFlowCheckerIsolatesCodebaseFailures` | A differing result, placeholder, recording or claim fails; a flow that the engine refuses is unsupported; a script that does not build fails only the runs that reach it | Bun |
| | `TestObserveTrace`, `TestObserveTraceUnwalkedJobs`, `TestObserveTraceRetries`, `TestObserveChildFlows`, `TestObserveNestedGenerated`, `TestObserveFailureSlotAndCarriedOrigin`, `TestCompareRun`, `TestCarriedLocation`, `TestObserveDeclaredCopies`, `TestObserveUserValueEqualToAGeneratedError`, `TestCheckDelays`, `TestAsOracleBase`, `TestCompactJSON` | The observation of a trace and its comparison: stored forms, `not_run` sites, placeholders, retries, stack masking, the location rule's positive and negative cases, declared copies, delays and the platform origin | — |
| | `TestObserveIncludedResult`, `TestObserveContainerAttempts`, `TestObserveNestedAttemptCopies` | The same, end to end on the engine with hand-built programs: an error stop's included array, a retried loop's attempts | — |
| | `TestRemoveCounters`, `TestTimedOutRunRemovesCounters`, `TestFlowRunTimeout` | The attempt counters removed after a run, a timed-out one included; the run limit and the watchdog's threshold in a plain and a race build | — / Bun |
| `observe_test.go` | `TestResolvedArgsKeyOrder`, `TestKeyOrdered` | Every order of a step's argument keys observes the same `resolved_args` text | — |
| `effects_test.go` | `TestStubCasesMatchTheOracle` | The stub fixtures match their `expect` and, without it, the oracle's recording, effects included, in `fresh` and `warm`; a group's runs are in flight together | Bun |
| | `TestStubModeAndEffects` | A missing outcome fails with `replay_miss`; a differing effect shows; mocked and skipped modules need no call; an input transform's failure counts as never calling; a group member's failure; `stub_unsupported`; a foreign root gets 409 | Bun |
| | `TestRunEffectsLocates`, `TestEffectServerKeepsRunsApart`, `TestCheckStub`, `TestModuleAt`, `TestRunGroup` | The hook; per-run routing; the trace side of the replay check; the module a site names; the group's hold and failures | — |
| `jobreads_test.go` | `TestEndedRunJobRead` | A job read of a run that has ended fails the step with `UnsupportedPlatformCall`, in `fresh` and `warm` ([why](#decisions-where-the-reference-is-silent)) | Bun |
| `sharedcode_test.go`, `esmpackages_test.go` | `TestBuiltinImportsShareTheClosure`, `TestESModulePackagesShareTheClosure`, `TestWindmillClientSharesTheClosure` | The synthetic fixtures `testdata/builtin-imports`, `testdata/esm-packages` and `testdata/esm-client` build with shared code, with no `shared_code_fallback` warning, and pass in every mode. The four builtin-import scripts share one closure; each codebase entry gets a closure of its own. A probe of the built export shows that a CommonJS entry assigns `module.exports` once and exposes `main`, and an ES module exposes `main` with no such assignment | Bun; the last also the stock client |
| `tree_test.go` | `TestCheckBuildsTheTreeRecordPushes` | `check` builds the copy that `record` pushes: `testdata/path-alias`'s `tsconfig.json` alias works in the tree, and not beside it | Bun |
| `hostmode_test.go` | `TestHostModeHostLossFailsTheFixture`, `TestHostModeClosesItsChannels` | A host killed after its fixture's runs fails them all; closed fixtures leave no descriptor open | Bun |
| `store_test.go` | `TestFixtureStoresAreApart`, `TestFixtureStateOnRedis`, `TestStateTestClaim` | Two fixtures' Redis stores keep one path apart; `testdata/state-isolation` run twice counts from zero per fixture; the test's random id is claimed with `SET NX`, and a second claim is refused. The cleanup of a claim deletes only the keys of a claim it won, so the loser's cleanup leaves the winner's state | `WL_TEST_REDIS_URL`; Bun for `TestFixtureStateOnRedis` |
| `testmode_test.go` | (an `init`) | Turns on QuickJS-only mode for the flow fixtures and expression files that any test runs, when `WL_TEST_QUICKJS_ONLY=1`; step cases evaluate no expression | — |
| `main_test.go` | `TestMain` | Lets the test binary serve as host mode's engine proxy | — |
| | `TestUsage`, `TestPrintRuns`, `TestStub`, `TestHarnessInfo` | The verbs' flag refusals, `--mismatched` at the working directory or at `--out` among them, before Docker is asked anything; `record`'s per-run lines; `stub`'s output and refusal; the build's provenance | — |
| `ledger_test.go` | `TestLedgerGuard`, `TestParseMarker` | [The ledger guard](#the-ledger-guard) on generated Git trees, staged and untracked; the marker form | — |

The sessions behind `record` are tested in
[internal/oracle](../../internal/oracle/README.md#tests).

## The ledger guard

`wltest ledger [--repo DIR]` keeps a retarget ledger out of every tree that CI
checks. A retarget ledger lists the failures that pull requests into a
retarget branch knowingly leave: a branch that moves windmill-lite to a new
Windmill or Bun version.

| Flag | Default | What it does |
|---|---|---|
| `--repo DIR` | `.`, the working directory | The repository's root, whose files the guard reads |

A retarget ledger has two parts:

- **The ledger file**, `cmd/wltest/testdata/flowsuite-pending.json`.
- **Markers.** A marker is text of the form
  `retarget-pending: <milestone> <reason>`, such as the skip message of a Go
  test or the name of a skipped bun test. The `<milestone>` is the id of the
  retarget's work item, the prefix of its pull request's title: the capital
  letter `W` or `X`, a number and an optional lower-case letter. The
  `<reason>` is one non-empty line, with no space around it.

No test reads the ledger file, and a marker that a test uses as its skip
message skips that test without a failure. The guard matches text, not skip
calls, and fails while the ledger file exists, even empty. It
also fails while a well-formed marker stands anywhere in a text file that
Git tracks or would track: in a test, but also in documentation or a data
file, such as a fixture's JSON. It prints:

| Line | Meaning |
|---|---|
| `retarget ledger cmd/wltest/testdata/flowsuite-pending.json: absent` | The ledger file's state: `absent` or `present` |
| `markers: <n>` | How many markers it found |
| `  <file>:<line>: retarget-pending: <milestone> <reason>` | One per marker |

Each malformed marker, the ledger file and a count of the markers left are
also reported on standard error.

In a Go or bun test file, an occurrence of the prefix that is not a
well-formed marker also fails the guard, because it could still skip a test.
In any other file a malformed occurrence is prose, such as this section. The CI jobs `Go (ubuntu-latest)` and
`Go (macos-latest, packages)` run the guard
([docs/ci.md](../../docs/ci.md#the-go-jobs)).

## Version

A recording's provenance names the build: the harness name `wltest`, the Go
version, the version from
`-ldflags "-X main.version=…"` or the module version, and the VCS revision
and modified flag that `go build` stamps from a checkout.

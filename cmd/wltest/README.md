# cmd/wltest

**Current state:** three verbs, `record` (milestone E2 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md); recording batch
E3a added expression files), `stub` (milestone E4a) and `check`, for step
cases (milestone C3a) and flow fixtures (milestone E1). This README covers
the `wltest` command in this directory and its `testdata/`. `diff`, which
[ADR 0011](../../docs/design/0011-differential-testing.md) describes as
running the oracle and windmill-lite live, is in no milestone of ADR 0012
yet: it prints that it is not implemented and exits 2.

`wltest` is windmill-lite's conformance and oracle runner
([ADR 0011](../../docs/design/0011-differential-testing.md)). `record` runs
flow fixtures (the format of [OR-15](../../docs/semantics/oracle.md#or-15),
loaded by [internal/conformance](../../internal/conformance/README.md)) on
the pinned Windmill CE `v1.767.0` oracle through
[internal/oracle](../../internal/oracle/README.md), and writes
`recorded/<run>.json` with provenance. It also takes expression-case files
(`conformance/exprs/<finding>.yaml`), lifts each into a generated one-step
flow fixture in a temporary directory, and writes its recordings to
`conformance/exprs/recorded/<finding>/<case>.json`.

## Usage

```sh
go build -o bin/wltest ./cmd/wltest
bin/wltest record [flags] <fixture, expression file or directory>...
```

Each argument is a fixture directory (one holding `fixture.yaml`), an
expression-case file, or a directory searched for both: a `.yaml` file in a
directory named `exprs`, other than `deferred.yaml` and outside any fixture,
is an expression-case file. Arguments that overlap reach each fixture and
file once, and an explicit file of any other kind is refused (exit 2). Flow
fixtures are recorded first, then the expression files, with files marked
`slow` last, so that what a slow case leaves running on the oracle (the
spinning evaluation of [JE-2](../../docs/semantics/expressions.md#je-2))
cannot slow the others. It needs Docker, which pulls the CE image
(1,373 MB compressed on amd64, [OR-2](../../docs/semantics/oracle.md#or-2))
and Postgres, and `npx` on `PATH` to run `windmill-cli@1.767.0`. Pulls are
refused without enough free disk ([OR-30](../../docs/semantics/oracle.md#or-30)).
The oracle runs in public CI; see
[the oracle workflow](../../.github/workflows/oracle.yml).

| Flag | Default | Meaning |
|---|---|---|
| `--check` | off | Fail when a run has no committed recording or its observation differs from it, and never write into a fixture. |
| `--out DIR` | — | Write new and changed recordings under `DIR`, at their committed path (`DIR/<fixture path>/recorded/`, `DIR/conformance/exprs/recorded/<finding>/`), instead of in place. |
| `--raw DIR` | — | Write the raw API captures of every run to `DIR`. |
| `--report FILE` | — | Write the session report: disk readings, pulls, images, boot times, CLI version, deployments, memory peaks, teardown. |
| `--repeat N` | 1 | Run each run `N` times and require equal observations. |
| `--regenerate-metadata` | off | Run `folder add-missing` and `generate-metadata` in each fixture's sync tree, in place, before pushing; CI then diffs the tree ([OR-8](../../docs/semantics/oracle.md#or-8)). A fixture with `oracle: {deploy: api}` is never regenerated: its tree is created through the API as written. A generated tree always gets them. |
| `--workers N` | 1 | Worker containers, 1 or 2 ([OR-5](../../docs/semantics/oracle.md#or-5)). |
| `--skip-unmet-workers` | off | Skip a fixture that needs more workers than `--workers`, reporting its runs as `skipped`, instead of refusing it ([OR-5/a](../../docs/semantics/conformance.md#or-5)): the oracle workflow's two-worker session records it. |
| `--listen-interval N` | 1 | The oracle's `LISTEN_NEW_EVENTS_INTERVAL_SEC`, for the knob comparison of [OR-7/a](../../docs/semantics/conformance.md#or-7); the value is in every recording's provenance, which is never compared. |
| `--cli CMD` | `npx -y windmill-cli@1.767.0` | The command that runs the CLI; its `--version` must report exactly `1.767.0` (a pre-release such as `1.767.0-beta` is refused). |
| `--docker-data-root DIR` | Docker's data root | The directory whose filesystem the disk preflight measures. |
| `--sink-listen ADDR` | `0.0.0.0:0` | The effect sink's address; the oracle reaches it through `host.docker.internal`, which on Linux is the Docker bridge gateway, so the default listens beyond loopback. On a shared network, pass a narrower address. |
| `--run-timeout D` | `5m` | The timeout of one run; less than 60 s is raised to 60 s ([OR-10](../../docs/semantics/oracle.md#or-10)). A run that times out is canceled. |

It prints one line per run: `unchanged`, `new`, `changed`, `unstable`,
`expect_mismatch`, `failed`, `refused` or `skipped`, the fixture and the run, then any
error and the JSON-pointer differences (`/steps/m:b/args/tag: "ho" != "hi"`,
actual before expected). It exits 1 when a run failed, was refused, was
unstable or did not match its `expect`, and in check mode also when a
recording is new or changed. A run whose committed recording names another
oracle, image or CLI is `refused` (`recording_identity_mismatch`) before
anything runs; delete the recording to re-record it. A refusal of the whole
session (for example `{"error":"insufficient_disk",…}`) is printed as JSON on
standard output. A run's `claims`, `delays` and `unit` cases are not checked
here: `go test ./internal/oracle` checks them against the committed
recordings, so a contradicted claim still leaves a recording to inspect.

Fixtures in stub mode (`stub: true`) are pushed stubbed: `record` writes
the stubbed copy of the sync tree, runs the CLI's `generate-metadata` in
it, requires every script's schema to equal the original's, and answers
each run's stubs from its `replay` table through the replay service at
`$WL_EFFECT_SINK/replay`. A run also fails when a stub's call has no
outcome in the table, when a call disagrees with the walk, or when a
script job never called ([internal/oracle](../../internal/oracle/README.md#stub-mode)).
Runs that share a `group` are submitted together, as concurrent outer runs,
and fail together when one fails or when they did not overlap. Each
fixture's workspace is deleted after its runs, because CE admits only two
workspaces besides `admins`.

## stub

```sh
bin/wltest stub --out <dir> <sync tree>
```

Copies the sync tree (the directory of its `wmill.yaml`) to `<dir>`, which
must not exist and must not lie inside the tree (compared by file identity,
so a symbolic link or a case alias counts),
with the source of every Bun script and every `!inline` Bun rawscript
replaced by a stub that keeps `main`'s declaration with its parameter list
and the type declarations, never calls it, and exports a function under
the name `main` that asks the replay service for its outcome
([ADR 0011](../../docs/design/0011-differential-testing.md) stub mode). It
prints one `stubbed <file>` line per replaced source. The copy's metadata
still describes the originals, so run `npx -y windmill-cli@1.767.0
generate-metadata` in it before pushing it. A tree in which some executed
script would keep its body is refused, with
`{"error":"stub_unsupported","file":…,"reason":…}` on standard output and
exit 1.

## check

```sh
WL_BUN=/path/to/bun-1.3.10/bun bin/wltest check [--mode warm] [--bun PATH] [--repo DIR] [-v] <step file, fixture or directory>...
```

Runs the step cases of `conformance/steps` and the flow fixtures of
`conformance/flows` on windmill-lite. Each argument is a step file, a
fixture directory (one holding `fixture.yaml`), or a directory searched for
both. An explicit file of any other kind is refused (exit 2); expression
cases are checked by `go test ./internal/expr`, not here.

It prints `pass` or `fail`, the case and its file, or the fixture and the
run, then each difference; `unsupported`, the fixture, the run and why it
was not checked; and a summary. It exits 1 when a case or run failed or a
file or fixture is invalid, and 2 for usage errors. Unsupported runs do not
fail it. `--mode` is `warm` only: `fresh` is milestone C3b's, host mode
F3b's. `--bun` defaults to `WL_BUN`, then `bun`; the build refuses any Bun
but the pin. `--repo` defaults to the nearest directory up from the working
directory that holds `harnesses/bun`, whose build helper needs its
dependencies installed (`bun install --frozen-lockfile` in
`harnesses/bun/build`). `-v` prints the harness's diagnostics of a failed
case or run.

### Step cases

For each step file ([ADR 0003](../../docs/design/0003-semantics-placement.md),
the format in [conformance](../../conformance/README.md#step-cases)) it
writes the cases' scripts and host-provided modules into a sync folder in a
temporary directory, builds them with the
[Bun build helper](../../harnesses/bun/build/README.md) (`bundle.BuildBun`)
into one bundle, whose `manifest.json` lists the host-provided modules in
registration order, and runs each case in a harness process of its own
([internal/harness](../../internal/harness/README.md)), its runs in order
through the protocol: that is the `warm` configuration, in which module
state and `globalThis` survive from one run to the next (D1). Each run's
entrypoint is `engine.Entrypoint`'s, its job variables the core `WM_*`
ones of a flow step (`WM_FLOW_STEP_ID` is the case's `step_id`, default
`a`) or of a script job (`step_id: ""`), and its outcome is mapped to
Windmill's stored value by `engine.StoreOutcome`, then compared by
`conformance.CompareStep`. Every run is observed up to its settlement (at
most 10 s), since an outcome fixed early (an intercepted `process.exit`)
comes before the call ends: its warnings are those collected until then,
and its late reports are compared when the case states them. A run whose
call must not settle says `settled: pending` and is observed for 500 ms
instead.

### Flow fixtures

For each fixture (the format of [OR-15](../../docs/semantics/oracle.md#or-15),
loaded by [internal/conformance](../../internal/conformance/README.md#fixtures)),
a few at a time (`flows.go`):

1. **Build.** It reads the sync tree with
   [internal/syncfolder](../../internal/syncfolder/README.md) and compiles
   each run's target flow with [internal/ir](../../internal/ir/README.md),
   the target as the bundle's only root, so that a flow the compiler refuses
   leaves the fixture's others to run. The Bun scripts the targets reach are
   built with `bundle.BuildBun` into one bundle, each named by its inventory
   id ([ADR 0002](../../docs/design/0002-ir-and-loader.md)):
   `scripts/bun/<path>.mjs` for a path script, `scripts/bun/inline-<16
   hex>.mjs` for an inline step. A script's job path
   ([WR-29](../../docs/semantics/languages.md#wr-29)) cannot name it, since
   an inline step `a` of flow `f/x` and a path script `f/x/a` share one. A
   codebase script ([SY-8](../../docs/semantics/openflow.md#sy-8)) is built
   the same way, as an ordinary Bun script: a stand-in, printed as a `note`
   under the run, until milestone C2b builds it with esbuild. What goes
   wrong with one target (a compile error, a refused script, a script that
   does not build) fails or skips only the runs of that target; a folder
   that does not load, or a build that fails as a whole, fails them all.
2. **Run.** Each target flow gets a walking skeleton of its own
   (`engine.Skeleton`, milestone W1), and every run goes to one pool of Bun
   harness processes per fixture, the `warm` configuration. Runs go in name
   order, a `group` at its first run's place with its runs concurrent
   (`conformance.RunBatches`); `cancel_after_ms` cancels a run, and a run
   that has not ended after 60 s fails. Each run has its own id, which its
   steps see as `WM_FLOW_JOB_ID` and `WM_ROOT_JOB_ID`, and the checker
   records the id of every invocation it makes (`WM_JOB_ID`) with its site.
   The skeleton names an invocation's script by its job path, which stays
   the step's `WM_JOB_PATH`; the checker sends it with the inventory id of
   the script the compiler put at that site.
3. **Observe** (`observe.go`). The run's trace becomes the observation the
   oracle's walker produces ([ADR 0011](../../docs/design/0011-differential-testing.md),
   [OR-11](../../docs/semantics/oracle.md#or-11)): a module of the flow that
   the trace has no record of is `not_run`; the root's result and each
   site's `args` and `result` are in their stored form, as the job API
   returns them ([ADR 0005](../../docs/design/0005-json-step-boundaries.md));
   `carried` and `resolved_args` keep the trace's texts, the value carried
   on in memory and the arguments the script received, and are written only
   where their text differs. The values are then normalised by
   [OR-14](../../docs/semantics/oracle.md#or-14)'s rules: job ids become the
   placeholders of their sites (the run's own id is the root, `<job:>`), and
   a failed site's stack is masked wherever its value is carried
   ([OR-V3](../../docs/semantics/oracle.md#or-v3) clause a); then the
   fixture's `stack` and `mask` pointers apply.
4. **Compare.** With the run's `expect`, `carried` and `resolved_args`
   included, or with its recording when it has none, which holds neither;
   then its `claims` are checked. On the reference's side, the location of
   the recognised internal error is stripped where windmill-lite generated
   the value (`conformance.StripLocations`, ADR 0011's location rule): a
   result whose origin the trace records as the engine, a harness or the
   facade, and a carried value that is the same value, in either member
   order. A carried value that differs from the result has no recorded
   origin and is never stripped. A run with nothing to compare fails.

A run is `unsupported` when windmill-lite cannot run it yet: a stub fixture
(milestone E4b), a fixture with `setup.yaml` (resources and variables,
PF2) or effects (the effect sink), a script run (`kind: script`), a
run-and-wait run (`submit: run_wait_result`, whose response is the HTTP
adapter's, F7b), a flow the
compiler refuses as an unsupported feature, language or pinned hash, a
script of another harness or boundary (C3n), or a flow the walking
skeleton refuses at load (containers, failure modules, `skip_if`, retries
and the other features N1a and its successors implement).

### Tests

`go test ./cmd/wltest` runs both suites. The Bun tests need `WL_BUN`;
without it they skip, unless `WL_REQUIRE_BUN` is set, as CI sets it.

- `TestStepSuite` is milestone C3a's exit gate: every step case passes.
  `TestStepCheckerReportsDifferences` checks that a wrong expectation of
  each kind fails, and `TestStepCatalogue` that every catalogue case of a
  finding with a step file is a step case or deferred.
- `TestFlowSuite` is milestone E1's exit gate: every run the flow suite
  requires passes in `warm`. `testdata/flowsuite.json` lists them in
  `runs`, each with the milestone whose pull request first made it pass;
  a run that a recording batch's manifest
  (`internal/oracle/testdata/batches/<batch>.json`) gates on a milestone
  listed in its `implemented` is required too, so a later engine milestone
  adds itself there. A required run stays required. The test runs every
  other fixture of `conformance/flows` as well: each must load, and the log
  gives the counts of passing, failing and unsupported runs, and names the
  runs that pass without being required.
- `TestFlowCheckerReportsDifferences` checks that a run whose result, job
  placeholder, recording or claim differs fails with its differences, that
  a flow the skeleton refuses is unsupported, that an inline step and a
  path script sharing a job path both run and both see it, that a script
  that does not build fails only the runs that reach it, and that a run
  that holds passes. `TestObserveTrace`, `TestCompareRun` and
  `TestCarriedLocation` need no Bun: they check the observation of a trace
  (stored forms, `not_run` sites, placeholders, stack masking, the kept
  `carried` and `resolved_args`, the generated values) and which reference
  a run is compared with, including the location rule's positive and
  negative cases for a result and for a carried value in either member
  order.

## Version

Provenance names the build: the version from `-ldflags "-X main.version=…"`
or the module version, and the VCS revision and modified flag that `go build`
stamps from a checkout.

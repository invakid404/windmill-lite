# cmd/wltest

**Current state:** three verbs, `record` (milestone E2 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md); recording batch
E3a added expression files), `stub` (milestone E4a) and `check`, for step
cases (milestone C3a; C2b added codebase scripts and their Node side),
flow fixtures (milestone E1) and, on the engine core, expression files run
end to end (milestone N1a), with the for loops of milestone N3a. This README covers the `wltest` command in
this directory, its `testdata/`, and `noderun.mjs`, the Node driver `check`
embeds. `diff`, which
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
| `--cli CMD` | `npx -y windmill-cli@1.767.0` | The command that runs the CLI; its `--version` must report exactly `1.767.0` (a pre-release such as `1.767.0-beta` is refused). |
| `--docker-data-root DIR` | Docker's data root | The directory whose filesystem the disk preflight measures. |
| `--sink-listen ADDR` | `0.0.0.0:0` | The effect sink's address; the oracle reaches it through `host.docker.internal`, which on Linux is the Docker bridge gateway, so the default listens beyond loopback. On a shared network, pass a narrower address. |
| `--run-timeout D` | `5m` | The timeout of one run; less than 60 s is raised to 60 s ([OR-10](../../docs/semantics/oracle.md#or-10)). A run that times out is canceled. |

It prints one line per run: `unchanged`, `new`, `changed`, `unstable`,
`expect_mismatch`, `failed` or `refused`, the fixture and the run, then any
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
WL_BUN=/path/to/bun-1.3.10/bun bin/wltest check [--mode warm|fresh] [--bun PATH] [--node PATH] [--repo DIR] [-v] <step file, fixture, expression file or directory>...
```

Runs the step cases of `conformance/steps`, the flow fixtures of
`conformance/flows` and the expression cases of `conformance/exprs` on
windmill-lite. Each argument is a step file, a fixture directory (one
holding `fixture.yaml`), an expression file, or a directory searched for
all three. An explicit file of any other kind is refused (exit 2).

It prints `pass` or `fail`, the case and its file, or the fixture and the
run, then each difference; `unsupported`, the fixture, the run and why it
was not checked; and a summary. It exits 1 when a case or run failed or a
file or fixture is invalid, and 2 for usage errors. Unsupported runs do not
fail it. `--mode` is `warm` (the default) or `fresh` (milestone C3b); host
mode is F3b's. Flow fixtures run in `warm` only: in `fresh` mode every
flow run is unsupported, since the flow suite runs in `fresh` through `wl`
(milestone F1). `--bun` defaults to `WL_BUN`, then `bun`; `--node` to
`WL_NODE`, and empty skips the Node side of CommonJS codebase cases
(below); the build refuses any Bun but the pin. `--repo` defaults to the
nearest directory up from the working directory that holds `harnesses/bun`, whose build helper needs its
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
registration order, and runs each case's runs in order through the protocol
([internal/harness](../../internal/harness/README.md)). In the `warm`
configuration a case's runs share a harness process of its own, in which
module state and `globalThis` survive from one run to the next (D1); in
`fresh` each run has a process of its own, which ends with it, as a
Windmill job's does. Each run's entrypoint is `engine.Entrypoint`'s, its
job variables the core `WM_*` ones of a flow step (`WM_FLOW_STEP_ID` is the
case's `step_id`, default `a`) or of a script job (`step_id: ""`), and its
outcome is mapped to Windmill's stored value by `engine.StoreOutcome`, then
compared by `conformance.CompareStep`. Every run is observed up to its
settlement (at most 10 s), since an outcome fixed early (an intercepted
`process.exit`) comes before the call ends: its warnings are those
collected until then, and `left` compares the counts of its `left_behind`
warning. A run whose call must not settle says `settled: pending` and is
observed for 500 ms instead. A run's late reports are compared, when the
case states them, at the end of its case, so that a late report of work it
left running after it settled, which the D9 fence must stop, fails the
case; in `warm` mode the process is observed for 200 ms more after a last
run that states its late reports or settlement, before it is drained.

A case with `codebase` is a codebase script: `check` writes its `files`
into the folder and builds it with esbuild (`bundle.BuildCodebase`), with
its entry's settings and the shims the scan wrote; its runs go to the
harness as any other's. With `--node` (default `WL_NODE`; empty skips
it), each run of a **CommonJS** codebase case also runs on Node, as
Windmill runs such a bundle (deviation D13,
[WR-50](../../docs/semantics/languages.md#wr-50)): in a fresh job
directory laid out in Windmill's order: a placeholder `package.json` of
`{}`, then the bundle as `main.js` and the entry's assets at their paths,
so an asset named `package.json` replaces the placeholder and its `type`
decides how Node loads `main.js`; then its metadata, its arguments and
`noderun.mjs`, run as `node --preserve-symlinks noderun.mjs <entry>`. The
driver applies the harness's own wrapper rules (`harnesses/bun/runtime/wrapper.ts`,
transpiled for Node by the pinned Bun) in the harness's order, an
unsigned callable export getting an empty parameter list as on the
harness, and writes
the outcome as a result frame carries it; a process that exits without
writing it ended as `empty` (status 0) or `escaped` (the last 600
characters of its stderr). The outcome is mapped by `engine.StoreOutcome`
and compared with the run's `node` check, or its own expect and claims
(`conformance.CompareStepNode`); differences are reported as `run N on
node: …`.

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
   with esbuild (`bundle.BuildCodebase`, milestone C2b), with the settings
   of the `codebases` entry that selects it, into the same bundle. What goes
   wrong with one target (a compile error, a refused script, a script that
   does not build) fails or skips only the runs of that target; a folder
   that does not load, or a build that fails as a whole, fails them all.
2. **Run.** Each target flow's program gets an engine of its own
   (`engine.Engine`, milestone N1a), and every run goes to one pool of Bun
   harness processes per fixture, the `warm` configuration. Runs go in name
   order, a `group` at its first run's place with its runs concurrent
   (`conformance.RunBatches`); `cancel_after_ms` cancels a run, and a run
   that has not ended after 60 s fails. Each run has its own id, which its
   steps see as `WM_FLOW_JOB_ID` and `WM_ROOT_JOB_ID`, and the checker
   records the id of every invocation it makes (`WM_JOB_ID`) with its site.
   The engine names an invocation's script by its inventory id, as the
   bundle does, and gives its job path as `WM_JOB_PATH`.
3. **Observe** (`observe.go`). The run's trace becomes the observation the
   oracle's walker produces ([ADR 0011](../../docs/design/0011-differential-testing.md),
   [OR-11](../../docs/semantics/oracle.md#or-11)): a module of the flow, or
   of a child flow the trace lists (a loop iteration's body, below its
   iteration's site `m:<loop>/i:<n>`), that the trace has no record of is
   `not_run`; the root's result and each
   site's `args` and `result` are in their stored form, as the job API
   returns them ([ADR 0005](../../docs/design/0005-json-step-boundaries.md));
   `carried` and `resolved_args` keep the trace's texts, the value carried
   on in memory and the arguments the script received, and are written only
   where their text differs, and a site with no result has no `carried`
   (a module that failed at the root's pickup with no job carries its
   error to the flow's result). The values are then normalised by
   [OR-14](../../docs/semantics/oracle.md#or-14)'s rules: job ids become the
   placeholders of their sites (the run's own id is the root, `<job:>`, and
   a child flow's id its iteration's site, as the walker reaches it), and
   a failed site's stack is masked wherever its value is carried
   ([OR-V3](../../docs/semantics/oracle.md#or-v3) clause a); then the
   fixture's `stack` and `mask` pointers apply.
4. **Compare.** With the run's `expect`, `carried` and `resolved_args`
   included, or with its recording when it has none, which holds neither;
   then its `claims` are checked. On the reference's side, the location of
   a templated error is stripped where windmill-lite generated the value
   (`conformance.StripLocations`, ADR 0011's location rule): a result
   whose origin the trace records as the engine, a harness or the facade,
   a carried value that is the same value, in either member order, and a
   carried value whose own recorded origin is one of those (the
   `EarlyStopError` an error stop carries over a step's result, FX-V2). A
   carried value with no generated origin is never stripped. A run with
   nothing to compare fails.

A run is `unsupported` when windmill-lite cannot run it yet: a stub fixture
(milestone E4b), a fixture with effects (the effect sink), a script run
(`kind: script`), a flow the compiler refuses as an unsupported feature,
language or pinned hash, a script of another harness or boundary (C3n), a
flow the engine refuses at open (`engine.ErrUnsupported`, which names the
milestone: failure modules, waits, retries, containers, subflows), and a
flow that reads the platform, which needs the platform providers of PF2:
its compiled program holds `variable(`, `resource(`, `flow_user_state(`,
`$var:`, `$res:`, `$jsonvar:` or `$encrypted:`, or it or a script it
reaches names a variable or resource of the fixture's `setup.yaml`. The
fixture's other runs are checked. So is a run the oracle canceled on its
own, with no `cancel_after_ms` (JE-2/a: its monitor of flows that hang
between steps), which has no counterpart on windmill-lite.

### Expression files

An expression-case file of `conformance/exprs`
([ADR 0003](../../docs/design/0003-semantics-placement.md), the expression
level) runs end to end as the generated one-step flow fixture that `record`
lifts it into (`conformance.LiftExprFile`), in a temporary directory: one
flow per case, whose module `b` runs a sink script with the case's
expression as its input `x` (or as its `skip_if`), compared with the case's
recording and claims. `check` takes such a file explicitly, and finds them
in the directories it is given, as `record` does. The unit-level checks of
the same cases are `go test ./internal/expr`'s (milestones B2a and B2b).
Cases that differ by a named deviation fail here as they do there, and
the flow suite does not require them: the transcendental `Math` cases of
JE-1, whose last digit depends on the C library (D16: the Linux runner's
glibc and macOS each differ from Windmill's image on some of them), and
JE-48/a, which does not overflow the 1 MiB stack (D17).

### Tests

`go test ./cmd/wltest` runs both suites. The Bun tests need `WL_BUN`;
without it they skip, unless `WL_REQUIRE_BUN` is set, as CI sets it. The
Node side needs `WL_NODE`; without it the step suite runs on Bun only,
unless `WL_REQUIRE_NODE` is set, as CI sets it (with Node 20, the major of
Windmill v1.767.0's image).

- `TestStepSuite` is the exit gate of milestones C3a and C3b: every step
  case passes, in `warm` and in `fresh`; with `WL_NODE` it is also
  milestone C2b's D13 gate, every CommonJS codebase case holding on Node
  too. `TestStepCheckerReportsDifferences` checks that a wrong expectation
  of each kind fails, in both modes, `TestStepCheckerReportsNodeDifferences`
  that a wrong or unstated Node difference fails, and `TestStepCatalogue`
  that every catalogue case of a finding with a step file is a step case or
  deferred.
- `TestFlowSuite` is the exit gate of milestones E1, N1a and N3a: every run the
  flow suite requires passes in `warm`. `testdata/flowsuite.json` lists them in
  `runs`, each with the milestone whose pull request first made it pass;
  a run that a recording batch's manifest
  (`internal/oracle/testdata/batches/<batch>.json`) gates on a milestone
  listed in its `implemented` is required too, so a later engine milestone
  adds itself there. A required run stays required. The test runs every
  other fixture of `conformance/flows`, and every expression file of
  `conformance/exprs` as its generated fixture (a run is keyed
  `<file>/<case>`), as well: each must load, and the log
  gives the counts of passing, failing and unsupported runs, and names the
  runs that pass without being required.
- `TestFlowCheckerReportsDifferences` checks that a run whose result, job
  placeholder, recording or claim differs fails with its differences, that
  a flow the engine refuses is unsupported, that an inline step and a
  path script sharing a job path both run and both see it, that a script
  that does not build fails only the runs that reach it, and that a run
  that holds passes. `TestFlowCheckerIsolatesCodebaseFailures` does the
  same for codebase scripts: one that does not bundle and one whose asset
  collides fail their own flows, while a flow whose codebase script reads
  its asset passes; `TestStepCheckerIsolatesCodebaseFailures` checks the
  same isolation for codebase step cases. `TestObserveTrace`,
  `TestObserveChildFlows`, `TestCompareRun` and
  `TestCarriedLocation` need no Bun: they check the observation of a trace
  (stored forms, `not_run` sites, a child flow's sites and id, placeholders,
  stack masking, the kept `carried` and `resolved_args`, the generated
  values) and which reference
  a run is compared with, including the location rule's positive and
  negative cases for a result and for a carried value in either member
  order.

## Version

Provenance names the build: the version from `-ldflags "-X main.version=…"`
or the module version, and the VCS revision and modified flag that `go build`
stamps from a checkout.

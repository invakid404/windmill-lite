# cmd/wltest

**Current state:** three verbs, `record` (milestone E2 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md); recording batch
E3a added expression files), `stub` (milestone E4a) and `check` for step
cases (milestone C3a). This README covers the `wltest` command in this
directory. `check` for flow fixtures, and `diff`, are milestone E1's; `diff`
prints that it is not implemented and exits 2.

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
WL_BUN=/path/to/bun-1.3.10/bun bin/wltest check [--mode warm|fresh] [--bun PATH] [--repo DIR] [-v] <step file or directory>...
```

Runs the step cases of `conformance/steps` ([ADR 0003](../../docs/design/0003-semantics-placement.md),
the format in [conformance](../../conformance/README.md#step-cases)) on
windmill-lite. For each file it writes the cases' scripts and
host-provided modules into a sync folder in a temporary directory, builds
them with the [Bun build helper](../../harnesses/bun/build/README.md)
(`bundle.BuildBun`) into one bundle, whose `manifest.json` lists the
host-provided modules in registration order, and runs each case's runs in
order through the protocol ([internal/harness](../../internal/harness/README.md)).
In the `warm` configuration a case's runs share a harness process of its
own, in which module state and `globalThis` survive from one run to the
next (D1); in `fresh` each run has a process of its own, which ends with
it, as a Windmill job's does. Each run's entrypoint is `engine.Entrypoint`'s, its job variables the
core `WM_*` ones of a flow step (`WM_FLOW_STEP_ID` is the case's
`step_id`, default `a`) or of a script job (`step_id: ""`), and its
outcome is mapped to Windmill's stored value by `engine.StoreOutcome`, then
compared by `conformance.CompareStep`. Every run is observed up to its
settlement (at most 10 s), since an outcome fixed early (an intercepted
`process.exit`) comes before the call ends: its warnings are those
collected until then, and its late reports are compared when the case
states them. A run whose call must not settle says `settled: pending` and
is observed for 500 ms instead. A run's late reports are compared at the
end of its case, so that a late report of work it left running after it
settled, which the D9 fence must stop, fails the case. `left` compares the
counts of the run's `left_behind` warning.

It prints `pass` or `fail`, the case and its file, then each difference,
and a summary; it exits 1 when a case failed or a file is invalid, and 2
for usage errors. `--mode` is `warm` (the default) or `fresh`; host mode is
F3b's. `--bun` defaults to `WL_BUN`, then `bun`; the build refuses any
Bun but the pin. `--repo` defaults to the nearest directory up from the
working directory that holds `harnesses/bun`, whose build helper needs its
dependencies installed (`bun install --frozen-lockfile` in
`harnesses/bun/build`). `-v` prints the harness's diagnostics of a failed
case.

`go test ./cmd/wltest` runs the suite in both modes (`TestStepSuite`, the
exit gate of milestones C3a and C3b), checks that a wrong expectation of
each kind fails, in both modes
(`TestStepCheckerReportsDifferences`), and that every catalogue case of a
finding with a step file is a step case or deferred (`TestStepCatalogue`).
The Bun tests need `WL_BUN`; without it they skip, unless `WL_REQUIRE_BUN`
is set, as CI sets it.

## Version

Provenance names the build: the version from `-ldflags "-X main.version=…"`
or the module version, and the VCS revision and modified flag that `go build`
stamps from a checkout.

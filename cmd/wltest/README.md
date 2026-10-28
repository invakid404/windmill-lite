# cmd/wltest

**Current state:** one verb, `record` (milestone E2 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md); recording batch
E3a added expression files). This README covers the `wltest` command in
this directory. `check` (E1), `diff` and `stub` (E4a) print that they are not
implemented and exit 2.

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
bin/wltest record [flags] <fixture or directory>...
```

Each argument is a fixture directory (one holding `fixture.yaml`), an
expression-case file, or a directory searched for both: a `.yaml` file in a
directory named `exprs`, other than `deferred.yaml` and outside any fixture,
is an expression-case file. Arguments that overlap reach each fixture and
file once, and an explicit file of any other kind is refused (exit 2). Flow fixtures are recorded first, then the expression files, with
files marked `slow` last, so that what a slow case leaves running on the
oracle (the spinning evaluation of [JE-2](../../docs/semantics/expressions.md#je-2))
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
| `--regenerate-metadata` | off | Run `folder add-missing` and `generate-metadata` in each fixture's sync tree, in place, before pushing; CI then diffs the tree ([OR-8](../../docs/semantics/oracle.md#or-8)). A generated tree always gets them. |
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
anything runs; delete the recording to re-record it. A refusal of the whole session (for example
`{"error":"insufficient_disk",…}`) is printed as JSON on standard output.

## Version

Provenance names the build: the version from `-ldflags "-X main.version=…"`
or the module version, and the VCS revision and modified flag that `go build`
stamps from a checkout.

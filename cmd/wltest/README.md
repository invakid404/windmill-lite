# cmd/wltest

**Current state:** two verbs, `record` (milestone E2 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md)) and `stub`
(milestone E4a). This README covers the `wltest` command in this
directory. `check` and `diff` (E1) print that they are not implemented and
exit 2.

`wltest` is windmill-lite's conformance and oracle runner
([ADR 0011](../../docs/design/0011-differential-testing.md)). `record` runs
flow fixtures (the format of [OR-15](../../docs/semantics/oracle.md#or-15),
loaded by [internal/conformance](../../internal/conformance/README.md)) on
the pinned Windmill CE `v1.767.0` oracle through
[internal/oracle](../../internal/oracle/README.md), and writes
`recorded/<run>.json` with provenance.

## Usage

```sh
go build -o bin/wltest ./cmd/wltest
bin/wltest record [flags] <fixture or directory>...
```

Each argument is a fixture directory (one holding `fixture.yaml`) or a
directory searched for them. It needs Docker, which pulls the CE image
(1,373 MB compressed on amd64, [OR-2](../../docs/semantics/oracle.md#or-2))
and Postgres, and `npx` on `PATH` to run `windmill-cli@1.767.0`. Pulls are
refused without enough free disk ([OR-30](../../docs/semantics/oracle.md#or-30)).
The oracle runs in public CI; see
[the oracle workflow](../../.github/workflows/oracle.yml).

| Flag | Default | Meaning |
|---|---|---|
| `--check` | off | Fail when a run has no committed recording or its observation differs from it, and never write into a fixture. |
| `--out DIR` | — | Write new and changed recordings to `DIR/<fixture path>/recorded/` instead of in place. |
| `--raw DIR` | — | Write the raw API captures of every run to `DIR`. |
| `--report FILE` | — | Write the session report: disk readings, pulls, images, boot times, CLI version, deployments, memory peaks, teardown. |
| `--repeat N` | 1 | Run each run `N` times and require equal observations. |
| `--regenerate-metadata` | off | Run `folder add-missing` and `generate-metadata` in each fixture's sync tree, in place, before pushing; CI then diffs the tree ([OR-8](../../docs/semantics/oracle.md#or-8)). |
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
must not exist and must not lie inside the tree (symbolic links resolved),
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

## Version

Provenance names the build: the version from `-ldflags "-X main.version=…"`
or the module version, and the VCS revision and modified flag that `go build`
stamps from a checkout.

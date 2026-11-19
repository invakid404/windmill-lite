# scripts

**Current state:** working: the documentation check runs in CI and reports
warnings only. The [release helper](#release-helper) serves the release
workflow and the npm package's packaging test. The
[Renovate auto-ack](#renovate-auto-ack) scripts serve the auto-ack
workflow, which is active, and `redis-test-env.sh` the Redis CI job.
This README covers the scripts in this directory; none of them is part of
windmill-lite itself.

These are the repository's helper scripts, in Python and shell. CI runs
their unit tests in the `Test repository scripts` step
([docs/ci.md](../docs/ci.md)).

| Script | What it does | Who runs it |
|---|---|---|
| `check-docs.py` | Reports the Markdown that breaks a countable rule of the writing standard | CI's `Docs (report only)` job; contributors |
| `release.py` | Reads and checks the version, decides whether a release run is a dry run, builds and checks the engine, and writes the licence notices | The [release workflow](../.github/workflows/release.yml); the npm package's packaging test; maintainers ([RELEASING.md](../RELEASING.md)) |
| `memoria-auto-ack.sh` | Acknowledges the README reviews that a safe Renovate bump makes pending | The [auto-ack workflow](../.github/workflows/memoria-auto-ack.yml) only |
| `check-go-mod-diff.py` | Accepts a `go.mod` diff that only bumps versions | `memoria-auto-ack.sh` |
| `check-workflow-yaml-diff.py` | Accepts a workflow diff that only bumps action pins | `memoria-auto-ack.sh` |
| `redis-test-env.sh` | Starts the containers of the Redis store's tests: Redis, Sentinel, Toxiproxy and a second Redis for the fault tests alone, pinned by digest, and prints their variables (`up`); removes them with their anonymous volumes, and the network (`down`) | The Redis job of the [CI workflow](../.github/workflows/ci.yml); contributors on Linux with Docker ([wl/redisstore](../wl/redisstore/README.md#tests)) |
| `test_check_docs.py`, `test_release.py`, `test_check_go_mod_diff.py` | The unit tests | CI's `Test repository scripts` step; contributors |

## Documentation check

`check-docs.py` reports the Markdown that breaks one of the four countable
rules of the writing standard, which its section
[What CI checks](../docs/writing.md#what-ci-checks) lists. Without a path,
it checks every Markdown file that Git tracks, except the skill packages
that Memoria installs.

| Option | What it does |
|---|---|
| `PATH ...` | Checks these files only |
| `--diff-base REV` | Reports only the blocks that overlap lines changed since `REV`, in the Markdown files changed since `REV` |
| `--github` | Prints each finding as a GitHub Actions warning annotation |
| `--summary FILE` | Appends a Markdown summary of the findings to `FILE` |
| `--strict` | Exits with 1 when there is any finding |

- **Exit status.** Without `--strict`, findings leave the exit status at 0.
  An error, such as a file that it cannot read, exits with a non-zero
  status.
- **In CI.** The `Docs (report only)` job of the
  [CI workflow](../.github/workflows/ci.yml) runs
  `python3 scripts/check-docs.py --github --diff-base HEAD^1` on each pull
  request, which annotates only the blocks that the pull request changed. On
  every run, it writes the count of all warnings to the job summary with
  `--summary`. The job is not a required check, and a warning never fails
  it.
- **Its tests.** `test_check_docs.py` also checks that `docs/writing.md`
  and `memoria.toml` state the script's limits and the identifier rule in
  the same words. It renders the standard's Go comment example with `go` to
  check its link; that test skips without `go`.

To check your changes before you push:

1. Check the files that you changed:
   `python3 scripts/check-docs.py AGENTS.md docs/writing.md` (the two paths
   are an example).
2. Check only the blocks that your branch changed:
   `python3 scripts/check-docs.py --diff-base "$(git merge-base origin/main HEAD)"`.

## Release helper

windmill-lite has one version: the `version` field of
[harnesses/package.json](../harnesses/package.json), the manifest of the
npm package `windmill-lite`. The release engine and the package both carry
it, and a release is the Git tag `v` + that version, such as `v0.1.0`.
[RELEASING.md](../RELEASING.md) gives the release steps.

`release.py` has six commands, run from the repository root as
`python3 scripts/release.py <command>`. Each first checks the version's
form: `MAJOR.MINOR.PATCH` with an optional pre-release, such as
`0.2.0-rc.1`, whose numeric identifiers have no leading zeros.

| Command | What it does | Who runs it |
|---|---|---|
| `release.py version` | Prints the version | The release workflow; the packaging test; maintainers |
| `release.py check-tag <tag>` | Exits with 1 unless the tag is `v` + the version | Maintainers; `mode` makes the same check on a tag |
| `release.py mode` | Prints `dry_run=true` or `dry_run=false` for a run of the release workflow, or exits with 1 for a run that may not release | The release workflow |
| `release.py build-engine <out> [--static]` | Builds `./cmd/windmill-lite` with `-X main.version=v<version>`, so that `windmill-lite version` prints `windmill-lite v<version>`. `--static` adds the static link of the Linux release engine | The release workflow; the packaging test |
| `release.py check-engine <binary>` | Exits with 1 unless the binary's `version` prints that line | The release workflow; the packaging test |
| `release.py notices <out>` | Writes `LICENSES.txt`, the release asset with the licence texts that the engine binary carries | The release workflow |

- **`mode`** reads the run's GitHub Actions variables and its
  `DRY_RUN_INPUT`, and decides by the table below. On a tag it first checks
  that the tag is `v` + the version, dry run or not; a wrong tag exits
  with 1.
- **`notices`** writes the Unlicense, and the licence and notice files of
  the third-party code in this repository: QuickJS-ng, mimalloc, the
  PostgreSQL ports, and Windmill's Apache-2.0 OpenAPI files. It adds those
  of each Go module that `./cmd/windmill-lite` links, and Go's own. It fails
  when a Go module has no licence file.
- **Errors.** A command that fails prints `release.py: <reason>` and exits
  with 1. An unknown command prints the usage and exits with 2.

| Run of the release workflow | `mode` gives |
|---|---|
| A pushed tag in `invakid404/windmill-lite` | `dry_run=false`: it releases |
| A pushed tag in a fork | `dry_run=true` |
| A pushed branch | Exit 1: a branch does not release |
| A dispatch with the dry run on (the default) | `dry_run=true`, in any repository and on any ref |
| A dispatch with the dry run off, on a tag of `invakid404/windmill-lite` | `dry_run=false`: it releases |
| A dispatch with the dry run off in a fork, or on a branch | Exit 1 |
| A pull request | `dry_run=true` |
| Any other event | Exit 1 |
- **The SDK's version.** The package's build, `harnesses/npm/build.ts`,
  stamps the same `v<version>` into the Bun host SDK.

`test_release.py` holds the tests of `release.py`:

- one test builds the engine with `build-engine` and checks that it prints
  the package's version, so the release engine and the package cannot
  differ;
- another checks that `notices` covers each third-party part of the engine;
- both skip without `go`;
- `ModeTest` checks `mode` for a tag, a pull request and a dispatch, each in
  this repository and in a fork.

## Renovate auto-ack

Renovate, the bot that updates this repository's dependencies, opens pull
requests that can change files in a README's scope. These scripts serve
the [auto-ack workflow](../.github/workflows/memoria-auto-ack.yml), which
acknowledges such README reviews automatically and pushes with the
`MEMORIA_AUTO_ACK_TOKEN` repository secret. A Renovate pull request with
any other change still needs a manual ack
([docs/memoria.md](../docs/memoria.md#renovate-bumps)).

`memoria-auto-ack.sh` runs on a Renovate branch's tip, and takes these
steps:

1. It lists the files that changed since the base (`BASE_REF`, by default
   `origin/main`).
2. It stops, leaving the branch untouched, when any file is outside the
   allowlist: `go.mod` and `go.sum` in any module directory, and
   `.github/workflows/*.yml` or `*.yaml`.
3. It stops when a changed `go.mod` fails `check-go-mod-diff.py`, or a
   changed workflow fails `check-workflow-yaml-diff.py`.
4. It acknowledges each README whose review is pending only because its
   inputs changed. A README with any other cause, or reviewed under changed
   guidance, is left for a person.
5. It runs `memoria check`, and stages `memoria.lock`. The workflow commits
   and pushes it.

The script exits with a non-zero status only on an internal error. A change
that is not safe to acknowledge is an expected outcome.

| Script | It accepts | It rejects |
|---|---|---|
| `check-go-mod-diff.py <base-ref> [go.mod-path]` | A `go.mod` diff that changes nothing but the versions of existing requirements or the `toolchain` line. A `toolchain` change passes only between concrete releases (`go1.N.P`, or a pre-release such as `go1.NrcK`) | An added or removed requirement, a `go` line change, any `replace`, `exclude` or `retract` edit, and a `toolchain` move to or from `default` or any other operand |
| `check-workflow-yaml-diff.py <base-ref> <workflow>` | A workflow whose parsed YAML differs only in action pins: `uses:` values that keep the same action and move from one commit SHA to another. It compares the parsed structures, so a change to comments or layout alone passes | Any other difference in the parsed YAML. It needs PyYAML |

- **How to run them.** From the repository root, as
  `python3 scripts/check-go-mod-diff.py origin/main go.mod`, for example;
  the base ref is any Git revision.
- **Exit status.** Each checker exits with 0 for a safe diff, 1 for an
  unsafe one, 2 for wrong arguments (with its usage), and 3 when it cannot
  read or parse the file.
- **Trust.** The workflow runs these scripts from the trusted `main`
  checkout against the pull request's files, and never executes code from
  the pull request.
- **Its tests.** `test_check_go_mod_diff.py` checks accepted requirement and
  concrete toolchain bumps, and rejected structural and toolchain-policy
  changes (`default`, `local`, bare language versions, custom suffixes).

## Run the tests

The tests need Python 3.10 or later: `release.py` uses a `match` statement. Without `go` on the `PATH`, the tests of
`test_release.py` that build the engine or write the notices skip, and so
does the test of `test_check_docs.py` that renders the standard's Go
example; the rest run.

1. Put `go` on the `PATH`, to run every test. The engine's build also needs
   a C compiler (cgo).
2. Run the scripts' unit tests from the repository root, as CI's `Test
   repository scripts` step does: `python3 -m unittest discover -s scripts -v`.

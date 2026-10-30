# scripts

**Current state:** the documentation check runs in CI and reports warnings
only. The [Renovate auto-ack](#renovate-auto-ack) scripts are installed,
awaiting activation. This README covers the scripts in this directory, and
none of them is part of windmill-lite itself.

## Documentation check

| Script | What it does |
|---|---|
| `check-docs.py` | Reports the Markdown that breaks one of the four countable rules of the writing standard, which its section [What CI checks](../docs/writing.md#what-ci-checks) lists. Its warnings leave the exit status at 0; with `--strict`, any warning makes it 1. An error, such as a file that it cannot read, exits non-zero. |
| `test_check_docs.py` | Unit tests for `check-docs.py`, run by CI. They also check that the script's limits equal the limits in `docs/writing.md` and `memoria.toml`. |

The `Docs (report only)` job of the [CI workflow](../.github/workflows/ci.yml)
runs `check-docs.py` on each pull request. It annotates only the blocks that
the pull request changed, and writes the count of all warnings to the job
summary. The job is not a required check, and a warning never fails it.

To check your changes before you push:

```sh
python3 scripts/check-docs.py AGENTS.md docs/writing.md
python3 scripts/check-docs.py --diff-base "$(git merge-base origin/main HEAD)"
```

The second command reports only the blocks that your branch changed.

## Renovate auto-ack

Renovate, the bot that updates this repository's dependencies, opens pull
requests that can change files in a README's scope. These scripts serve the
[auto-ack workflow](../.github/workflows/memoria-auto-ack.yml), which
acknowledges such README reviews automatically. It stops at its first step
until the owner provisions its
`MEMORIA_AUTO_ACK_TOKEN` secret ([docs/memoria.md](../docs/memoria.md#renovate-bumps)).
Until then, Renovate pull requests are acknowledged by hand.

| Script | What it does |
|---|---|
| `memoria-auto-ack.sh` | On a Renovate branch whose only changes are safe dependency bumps, acknowledges every README that is pending only because its inputs changed, then runs `memoria check` and stages `memoria.lock`. Any other change leaves the branch untouched for a human review. |
| `check-go-mod-diff.py` | Exits 0 only when a `go.mod` diff changes nothing but the versions of existing requirements or the `toolchain` line. An added or removed requirement, a `go` line change, or any `replace`, `exclude` or `retract` edit is rejected. A `toolchain` change passes only between concrete releases (`go1.N.P`, or a pre-release such as `go1.NrcK`); a move to or from `default` or any other operand is rejected. |
| `test_check_go_mod_diff.py` | Unit tests for `check-go-mod-diff.py`, run by CI: accepted requirement and concrete toolchain bumps, and rejected structural and toolchain-policy changes (`default`, `local`, bare language versions, custom suffixes). |
| `check-workflow-yaml-diff.py` | Exits 0 only when a workflow diff changes nothing but action pins: `uses:` values that keep the same action and move from one commit SHA to another. Needs PyYAML. |

The workflow runs these scripts from the trusted `main` checkout against the
pull request's files and never executes code from the pull request. The
allowlist of paths a Renovate change may touch is `go.mod` and `go.sum` in
any module directory, and `.github/workflows/*.yml` or `*.yaml`.

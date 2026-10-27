# scripts

**Current state:** installed, awaiting activation. This README covers the
repository tooling in this directory. These scripts serve the
[Renovate auto-ack workflow](../.github/workflows/memoria-auto-ack.yml), which
stops at its first step until the owner provisions its
`MEMORIA_AUTO_ACK_TOKEN` secret (see [docs/memoria.md](../docs/memoria.md#renovate-bumps));
until then Renovate pull requests are acknowledged by hand. None of these
scripts is part of windmill-lite itself.

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

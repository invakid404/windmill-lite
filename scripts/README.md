# scripts

**Current state:** in use by CI. This README covers the repository tooling
in this directory. These scripts serve the
[Renovate auto-ack workflow](../.github/workflows/memoria-auto-ack.yml); none
of them is part of windmill-lite itself.

| Script | What it does |
|---|---|
| `memoria-auto-ack.sh` | On a Renovate branch whose only changes are safe dependency bumps, acknowledges every README that is pending only because its inputs changed, then runs `memoria check` and stages `memoria.lock`. Any other change leaves the branch untouched for a human review. |
| `check-go-mod-diff.py` | Exits 0 only when a `go.mod` diff changes nothing but the versions of existing requirements or the `toolchain` line. An added or removed requirement, a `go` line change, or any `replace`, `exclude` or `retract` edit is rejected. |
| `check-workflow-yaml-diff.py` | Exits 0 only when a workflow diff changes nothing but action pins: `uses:` values that keep the same action and move from one commit SHA to another. Needs PyYAML. |

The workflow runs these scripts from the trusted `main` checkout against the
pull request's files and never executes code from the pull request. The
allowlist of paths a Renovate change may touch is `go.mod` and `go.sum` in
any module directory, and `.github/workflows/*.yml` or `*.yaml`.

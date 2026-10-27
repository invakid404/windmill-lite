# windmill-lite

windmill-lite runs the flows and scripts of a [Windmill](https://www.windmill.dev)
folder with Windmill's semantics, but without Windmill's scheduling: no job
queue, no job per step, and no worker pickup.

**Current state:** bootstrapping. Nothing runs flows yet. The repository holds
a Go module, a placeholder command that prints its version, CI, a
documentation gate, and the [semantics reference](docs/semantics/README.md):
the Windmill `v1.767.0` behaviour that windmill-lite must match, written as
numbered findings with conformance cases. The design, and with it the package
layout, follows from that reference.

windmill-lite is not affiliated with Windmill Labs. It is released into the
public domain under the [Unlicense](LICENSE).

## Quick start

Requires Go 1.27.3 or later (the `go` directive in `go.mod`).

```sh
go run ./cmd/windmill-lite    # prints: windmill-lite (devel)
go test -race ./...
```

## Index

| Path | What it covers |
|---|---|
| [cmd/windmill-lite](cmd/windmill-lite/README.md) | The `windmill-lite` command. Today it only prints its version. |
| [scripts](scripts/README.md) | Repository tooling for acknowledging Renovate dependency bumps automatically; installed, awaiting activation (a repository secret). |
| [docs/semantics](docs/semantics/README.md) | The Windmill `v1.767.0` semantics reference: findings by area, the conformance catalogue, the oracle harness design, and the licence map. |
| [docs/memoria.md](docs/memoria.md) | How the documentation gate works and the per-session review flow. |
| [AGENTS.md](AGENTS.md) | Contributor rules: the clean-room rule, synthetic fixtures, Go as the primary language, pull requests. |

## Contributing

Read [AGENTS.md](AGENTS.md) first. Its clean-room rule is a hard constraint:
Windmill's server is AGPLv3, so everyone who designs, reviews or implements
windmill-lite works without reading that source. They work from the semantics
reference, Windmill's public documentation, the Apache-2.0 parts of Windmill,
and observed behaviour.

[CI](.github/workflows/ci.yml) checks `gofmt`, runs `go mod tidy -diff`,
`go vet ./...` and `go test -race ./...`, and runs the unit tests of the
helpers in `scripts/`, on Ubuntu and macOS for pull requests and pushes to
`main`. The [Memoria gate](.github/workflows/memoria.yml) fails while any
README has an unacknowledged change in its scope.
[Renovate](renovate.json) keeps dependencies and action pins current.

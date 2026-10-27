# Contributing to windmill-lite

These rules apply to every contributor, human or coding agent. `CLAUDE.md`
points here, so there is one copy.

## Current state

The repository is bootstrapping. It holds a Go module, a placeholder
`windmill-lite` command that prints its version, CI, and the documentation
gate. The package layout is decided by the design phase, which will land
under `docs/design/`; until then, do not add packages or top-level
directories that pre-empt it.

## Language

- **Go is the primary language**: the core, the compiler and CLI, and the
  host adapters are Go.
- A language harness is written in the language it runs, because it has to
  load that language's scripts in-process (for example TypeScript for Bun).
  Keep harnesses as small as their job allows.
- Windmill semantics must be defined once. When Go and a harness both need a
  rule, encode it as a shared, language-neutral conformance case that both
  sides run, not as two independent implementations that happen to agree.
- Go code is `gofmt`-formatted, passes `go vet ./...`, and passes
  `go test -race ./...`. CI enforces all three on Linux and macOS.

## Clean room

windmill-lite is released under the Unlicense. Windmill itself is mixed
licence: most of its repository (`backend/`, `frontend/`, and everything not
otherwise marked) is AGPLv3, some parts are proprietary enterprise code (the
`enterprise` feature and `*_ee*` files), and a few parts are Apache-2.0
(`openflow.openapi.yaml` and the other OpenAPI files, `python-client/`,
`deno-client/`, `go-client/`, `powershell-client/`, and
`typescript-client/`). AGPL or proprietary code must never enter this
repository, copied or translated. So:

1. **Implementation code** (Go, harnesses, tests, tooling): do not open
   Windmill's AGPL or proprietary source while you write it. Work from the
   semantics reference in this repository (`docs/semantics/`, once it lands),
   the public documentation at <https://www.windmill.dev/docs>, the
   Apache-2.0 parts listed above, and black-box behaviour observed on a
   running Windmill.
2. **The semantics reference** (`docs/semantics/`) may be written by reading
   Windmill's AGPL source at a pinned release. Describe the behaviour in your
   own words: rules, tables, decision lists, input-to-output examples, and
   source pointers such as `path/to/file.rs:123 @ v1.767.0`. Never include
   code blocks or line-by-line paraphrases of that source.
3. **Keep the roles apart.** If you have read Windmill's AGPL source for an
   area, contribute to that area's semantics reference, not to its
   implementation.
4. Identifiers, JSON field names, enum values, error-message strings and
   wire-format literals that compatibility requires may be used anywhere.
5. Apache-2.0 parts may be used as dependencies, or copied with their licence
   notices kept. Never relabel them as Unlicense.
6. Enterprise-only behaviour is out of scope unless it is observable on the
   Community Edition; mark it `EE` where you meet it.

When in doubt, ask in the pull request before writing the code.

## Fixtures and public hygiene

- Tests use **synthetic** Windmill fixtures written for this repository:
  scripts, flows, resources and variables invented to exercise one behaviour.
  Do not copy flows or scripts from any real Windmill workspace, and do not
  copy Windmill's own test fixtures.
- Nothing in the repository may name a customer, tenant or organisation that
  uses windmill-lite, contain internal hostnames, or contain secrets, tokens
  or credentials, even expired ones.
- Agent process records (implementation notes, review rounds, fix logs such as
  `impl.md`, `review-*.md`, `fix-r*.md`, `publish.md`, `done.md`) stay out of
  the repository tree.

## Documentation gate

Every module directory has a `README.md`, indexed from the root
[README](README.md). [Memoria](docs/memoria.md) ties each README to the files
it describes, and CI fails while a README has an unacknowledged change in its
scope. After any change, follow the per-session flow in
[docs/memoria.md](docs/memoria.md) until `memoria check` passes, and do it
after your last edit: changing any file in a README's scope re-opens its
review. Memoria needs a real Git worktree.

## Pull requests

1. Branch from `main`; `main` accepts changes only through pull requests.
2. CI (`Go (ubuntu-latest)`, `Go (macos-latest)`) and the Memoria gate must
   pass. Force pushes to `main` and its deletion are blocked.
3. Pull requests are squash-merged.

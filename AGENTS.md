# Contributing to windmill-lite

These rules apply to every contributor, person or coding agent. `CLAUDE.md`
points here, so the rules have one copy. The [README](README.md) says what
windmill-lite is and what works today.

## Layout

Add a package or a top-level directory only where
[ADR 0012](docs/design/0012-layout-and-milestones.md) places it, or amend
ADR 0012 in the same pull request.

## Language

Go is the primary language.

- Write the core, the compiler, the CLI and the host adapters in Go.
- Write a language harness in the language that it runs, because the
  harness loads that language's scripts in-process. For example, the Bun
  harness is TypeScript. Keep each harness as small as its job allows.
- Define each Windmill rule once. When Go and a harness both need a rule,
  write it as a shared, language-neutral conformance case that both sides
  run. Do not write two implementations that happen to agree.
- Format Go code with `gofmt`. It must also pass `go vet ./...` and
  `go test -race ./...`. CI checks all three on Linux and macOS.

## Clean room

windmill-lite is released under the Unlicense. Windmill's repository has
three licences, and only its Apache-2.0 parts can enter this repository:

| Part of Windmill's repository | Licence | In this repository |
|---|---|---|
| `backend/`, `frontend/`, and everything not marked otherwise | AGPLv3 | Never, copied or translated |
| The `enterprise` and `private` features, and `*_ee*` files | Proprietary | Never, copied or translated |
| `openflow.openapi.yaml` and the other OpenAPI files, `python-client/`, `deno-client/`, `go-client/`, `powershell-client/` and `typescript-client/` | Apache-2.0 | As a dependency, or copied with its licence notices kept. Never relabel it as Unlicense. |

Every contributor works in one of two roles: the build team or the
semantics team. Pick one. If you have read Windmill's AGPL or proprietary
source, you are on the semantics team.

### Build team

The build team shapes or writes windmill-lite: the authors of designs
(`docs/design/`), the reviewers of designs and of implementation pull
requests, and the implementers of Go code, harnesses, tests and tooling.

- Never open Windmill's AGPL or proprietary source, for any area, at any
  time. The separation works only if nothing from that source reaches a
  design before an implementer reads it.
- Work from these sources: the semantics reference (`docs/semantics/`), the
  public documentation at <https://www.windmill.dev/docs>, the Apache-2.0
  parts in the table above, and the black-box behaviour of a running
  Windmill.
- When you need a behaviour that the reference does not cover, ask the
  semantics team to add it to the reference. Never ask for source excerpts.

### Semantics team

The semantics team writes and validates the semantics reference
(`docs/semantics/`).

- You may read Windmill's AGPL source at a pinned release.
- Describe its behaviour in your own words: rules, tables, decision lists,
  input-to-output examples, and source pointers such as
  `path/to/file.rs:123 @ v1.767.0`.
- Never include code blocks or line-by-line paraphrases of that source.
- Do not author or review designs or implementation for this repository.

### Both teams

- Use identifiers, JSON field names, enum values, error-message strings and
  wire-format literals anywhere, when compatibility requires them.
- Enterprise-only behaviour is out of scope, unless the Community Edition
  shows it. Mark it `EE` where you meet it.
- When in doubt, ask in the pull request before you start.

## Fixtures and public hygiene

- Write tests with synthetic Windmill fixtures made for this repository:
  scripts, flows, resources and variables invented to exercise one
  behaviour. Do not copy flows or scripts from a real Windmill workspace,
  and do not copy Windmill's own test fixtures.
- Never name a customer, tenant or organisation that uses windmill-lite.
  Never commit an internal hostname, or a secret, token or credential, even
  an expired one.
- Keep agent process records out of the repository tree: implementation
  notes, review rounds and fix logs, such as `impl.md`, `review-*.md`,
  `fix-r*.md`, `publish.md` and `done.md`.

## Documentation

- Write documentation to the [writing standard](docs/writing.md), and use
  the terms in the [glossary](docs/glossary.md).
- Give every module directory a `README.md`, and index it from the root
  [README](README.md).
- After your last edit, follow the per-session flow in
  [docs/memoria.md](docs/memoria.md) until `memoria check` passes. The
  Memoria gate in CI fails while a README has an unacknowledged change in
  its scope, and any change to a file in that scope re-opens the review.
  Memoria needs a real Git worktree.
- CI reports the writing rules that it can count, as warnings
  ([What CI checks](docs/writing.md#what-ci-checks)).

## Pull requests

1. Branch from `main`.
2. Open a pull request. `main` accepts changes only through pull requests.
3. Make the required checks pass: `Go (ubuntu-latest)`, `Go (macos-latest)`
   and the Memoria gate, `memoria`.

Pull requests are squash-merged. Force pushes to `main` and the deletion of
`main` are blocked.

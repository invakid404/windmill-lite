# Contributing to windmill-lite

These rules apply to every contributor, person or coding agent. `CLAUDE.md`
points here, so the rules have one copy. The [README](README.md) says what
windmill-lite is and what works today, and the [glossary](docs/glossary.md)
defines its terms.

## Layout

Add a package or a top-level directory only where the package layout
places it, or amend that layout's architecture decision record
([ADR 0012](docs/design/0012-layout-and-milestones.md)) in the same pull
request.

## Language

Go is the primary language.

- Write the core, the compiler, the command-line interface (CLI) and the
  adapters that [hosts](docs/glossary.md#host) use in Go.
- Write a language [harness](docs/glossary.md#harness) in the language that
  it runs, because the harness loads that language's scripts in-process. For
  example, the [Bun](docs/glossary.md#bun) harness is TypeScript.
- A harness only runs steps. Keep in it only the rules that make sense in
  its language alone, such as parsing a TypeScript signature. Flows,
  expressions and every other rule live in Go
  ([ADR 0003](docs/design/0003-semantics-placement.md#decision)).
- Define each Windmill rule once. When Go and a harness both need a rule,
  write it as a shared, language-neutral
  [conformance case](docs/glossary.md#conformance-case) that both sides
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
- Behaviour of Windmill's Enterprise Edition (EE) is out of scope, unless
  the Community Edition shows it too. Mark it `EE` where you meet it.
- When in doubt, ask in the pull request before you start.

## Fixtures and public hygiene

- Write tests with synthetic Windmill [fixtures](docs/glossary.md#fixture)
  made for this repository:
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
- Keep documentation self-contained and durable: no milestone IDs or other
  work codes, and no history of the work
  ([Self-contained and durable](docs/writing.md#self-contained-and-durable)).
- Give every module directory a `README.md`, and index it from the root
  [README](README.md).
- After your last edit, follow the per-session flow in
  [docs/memoria.md](docs/memoria.md) until `memoria check` passes.
  [Memoria](docs/glossary.md#memoria) fails CI while a README has an
  unacknowledged change in its scope, the files that it describes. Any
  change to a file in that scope re-opens the review.
- Run Memoria in a real Git worktree
  ([docs/memoria.md](docs/memoria.md#when-you-interact-with-it)).
- CI reports the writing rules that it can count, as warnings
  ([What CI checks](docs/writing.md#what-ci-checks)).

## Pull requests

1. Branch from `main`.
2. Open a pull request. `main` accepts changes only through pull requests.
3. Make the required checks pass: `Go (ubuntu-latest)`, `Go (macos-latest)`
   and the Memoria gate, `memoria`.

Pull requests are squash-merged. Force pushes to `main` and the deletion of
`main` are blocked.

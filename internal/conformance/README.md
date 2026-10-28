# internal/conformance

**Current state:** the shared identity and observation vocabulary of
milestone K1 ([ADR 0012](../../docs/design/0012-layout-and-milestones.md)).
This README covers `internal/conformance` and its `testdata/`. The site-path
encoder, the run and step statuses, and the observation types exist and are
tested. Fixture loading, the oracle walker and the comparator do not exist
yet (milestones E1 and E2).

## What it holds

| File | Contents |
|---|---|
| `sitepath.go` | `SitePath`, `Segment` and the encoder of [ADR 0011](../../docs/design/0011-differential-testing.md), which answers [Q-OR-17](../../docs/semantics/oracle.md#q-or-17) |
| `status.go` | `RunStatus` (Windmill's four job statuses plus windmill-lite's `latched` and `halted`) and `StepStatus` ([OR-V2](../../docs/semantics/oracle.md#or-v2)) |
| `observation.go` | `Observation`, `StepObservation` and `BranchChosen`: the normalised observation of [OR-15](../../docs/semantics/oracle.md#or-15). A `not_run` step always encodes `"ran":false`, as [OR-V2/a](../../docs/semantics/conformance.md#or-v2) expects, and decoding sets it |

The engine, the harness, the oracle and `wltest` share this vocabulary, so
the package imports no other windmill-lite package. Code that runs
windmill-lite to produce an observation lives with its caller.

## Site paths

A site path is a sequence of typed segments joined by `/`, each written
`<kind>:<value>`:

| Kind | Value | Built by |
|---|---|---|
| `m` | a module id; `~<k>` is appended when the id occurs more than once in its module list | `Module`, `RepeatedModule`, `ModuleSegments` |
| `i` | a loop iteration index | `Iteration` |
| `b` | a branch index, or `default` | `Branch`, `DefaultBranch` |
| `s` | `failure` or `preprocessor` | `FailureSlot`, `PreprocessorSlot` |
| `r` | an attempt of a retried module, from 0 | `Attempt` |

Module ids are percent-escaped for `%`, `/`, `:` and `~` (`%25`, `%2F`,
`%3A`, `%7E`). Examples: `m:outer/i:1/m:inner/i:0/m:s`, `m:a/r:0`,
`s:failure` beside `m:failure`, and `m:a%2Fb` beside `m:a%252Fb`.

- **Canonical text.** A `SitePath` holds its canonical text, so `==` compares
  paths and a path can key a map. `ParseSitePath` accepts exactly the texts
  the encoder writes and rejects every other spelling, such as lower-case
  escapes or leading zeros.
- **Placeholders.** `Placeholder` writes `<job:PATH>` and spells a `>` inside
  the path as `%3E`. A placeholder therefore ends at its first `>`, and two
  sites never share one, even when a module id contains `>`.
- **JSON keys.** `SitePath` implements `encoding.TextMarshaler`, so it can key
  JSON objects. It refuses text that is not valid UTF-8, because JSON would
  replace the invalid bytes and two paths could then collide.

## The encoder's negative tests

These are the exit gate of K1. The cases are data files under
`testdata/sitepath/`. Each case cites its spec IDs and carries provenance
`policy`, because the encoding is windmill-lite's own choice.

| File | What it checks |
|---|---|
| `encode.json` | typed segments → canonical text and placeholder, and back |
| `reject.json` | texts the parser must reject, including the drafted untyped paths (`failure`, `a[0]`, `a#0`) |
| `flows.json` | synthetic runs whose sites must keep distinct paths, distinct placeholders and distinct observation records |

`flows.json` covers every negative test that ADR 0011 lists:

- two modules with one id in one list;
- the same id in different container bodies;
- an ordinary `failure` module beside a real failure handler, and an
  ordinary `preprocessor` module beside a real preprocessor;
- ids containing `%`, `/`, `:` and `~`;
- cases OR-V8/a–c.

Five cases name a swap pair: OR-V8/a–c, the handler that is also named
`failure`, and the nested case. For each pair, swapping the two sites'
results, or which of their jobs a result references, changes the
observation.

Three fuzz targets back the data files, and `go test` runs their seed
corpora:

- `FuzzParseSitePath`: every accepted text re-encodes to itself;
- `FuzzModuleSegmentInjective`: distinct module segments never share a text
  or a placeholder;
- `FuzzSitePathInjective`: random multi-segment paths of every kind parse
  back to their segments, and two paths share a text or a placeholder only
  when their segments are equal.

Run them longer with, for example:

```sh
go test ./internal/conformance -run '^$' -fuzz FuzzSitePathInjective -fuzztime 30s
```

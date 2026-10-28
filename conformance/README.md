# conformance

**Current state:** three levels of the shared conformance suite exist: the
OpenFlow decode cases in `openflow/`, the stored-form cases in `jsonb/`, and
the sync-folder load cases in `syncfolder/`. This README covers
`conformance/`. The flow, step and expression levels that
[0003](../docs/design/0003-semantics-placement.md) defines have no cases or
runners yet.

The suite pins each implementation to the
[semantics reference](../docs/semantics/README.md). Every case cites the
catalogue case it encodes (`docs/semantics/conformance.md`), or the finding
whose rule it encodes, and fixtures are synthetic: invented for this
repository to exercise one behaviour each.

## Levels

| Level | Directory | Status |
|---|---|---|
| decode | `openflow/<finding>.json` | in use; run by `go test ./internal/openflow` |
| jsonb | `jsonb/<finding>.yaml` | in use; run by `go test ./internal/jsonval`, and against PostgreSQL by CI's `jsonb (PostgreSQL)` job |
| load | `syncfolder/<finding>.json` | in use; run by `go test ./internal/syncfolder` |
| flow | `flows/<area>/<case>/` | not yet |
| step | `steps/<lang>/<case>.yaml` | not yet |
| expr | `exprs/<case>.yaml` | not yet |

## OpenFlow decode cases

A decode case gives a flow `value` as JSON and the decision of Windmill's
effective decoder ([OF-2](../docs/semantics/openflow.md#of-2)): a rejection,
or an accepted value and parts of its decoded form. The decoded form is
described in [internal/openflow](../internal/openflow/README.md).

Each `of-<n>.json` file holds the cases of one finding:

```json
{
  "finding": "OF-10",
  "cases": [
    {
      "spec": "OF-10/a",
      "provenance": "source-derived",
      "title": "static without value",
      "given": {"modules": [{"id": "a", "value": {"type": "script", "path": "f/demo/s", "input_transforms": {"x": {"type": "static"}}}}]},
      "expect": {"accept": {"at": {"/modules/0/value/input_transforms/x": {"type": "static", "value": null}}}}
    }
  ]
}
```

Case fields:

| Field | Meaning |
|---|---|
| `spec` | the catalogue case id |
| `provenance` | the catalogue's provenance tag, which the runner checks |
| `variant` | distinguishes several inputs of one catalogue case |
| `scope` | set only where it differs from the finding's (also allowed at file level) |
| `part` | `decode` when the catalogue case has a run-time or deploy half that is checked elsewhere; `note` says which |
| `deviation` | the named deviation of [0001](../docs/design/0001-equivalence-contract.md) whose behaviour the expectation shows |
| `given` | the flow value, as JSON |
| `expect.reject` | `kind` (`flow_value`, `module`, `retry_base`, `step_path`, …), `contains` and `not_contains` (message substrings, compared as the reference compares them), `not_prefix` |
| `expect.accept` | `at` (JSON Pointer to expected JSON in the decoded form), `matches` (JSON Pointer to a regular expression for a string), `absent` (JSON Pointers that must not resolve) |

`deferred.json` lists the catalogue's OF cases that are not decode cases,
each with the reason and where it is expected to be checked. A test fails
unless every OF case of the catalogue is a decode case here or is deferred.

## Stored-form cases

A stored-form case gives a value's *producer text*, as a step, QuickJS-ng or
a static transform writes it, and expects its *stored text*, the text
PostgreSQL `jsonb` gives back without its output spaces, or the rejection
that leaves it with no stored form. Both texts are described in
[internal/jsonval](../internal/jsonval/README.md). The catalogue cases are
flow cases; a stored-form case checks only the texts, and names where the
rest of the case is checked.

Each `<finding>.yaml` file holds the cases of one finding. Texts are YAML
single-quoted strings, in which a backslash is literal and only a quote is
doubled, so the JSON text stays byte for byte as written:

```yaml
finding: JE-44
cases:
  - spec: JE-44/b
    provenance: source-derived
    title: an older step is read from the database (jsonb order)
    read: stored
    given: '{"bbb":1,"a":2,"cc":3}'
    expect:
      keys: [a, cc, bbb]
      stored: '{"a":2,"cc":3,"bbb":1}'
    rest_in: N1a, which serves results.a past a step in between from the stored text (JE-18)
```

Case fields:

| Field | Meaning |
|---|---|
| `spec` | the catalogue case id, or the finding id for a case that encodes the finding's rule beyond its catalogue cases |
| `variant` | names a case that has only a finding id |
| `provenance` | for a catalogue case, the catalogue's tag, which the runner checks; for a rule case, the finding's, or `policy` where the case pins bytes the rule leaves open or PostgreSQL behaviour the reference does not state |
| `scope` | the catalogue's scope tag, where it gives one |
| `read` | `producer` or `stored`: the text the catalogue case's consumer reads, by the routing table of [0005](../docs/design/0005-json-step-boundaries.md) |
| `given` | the producer text |
| `expect.stored` | the exact stored text |
| `expect.keys` | the top-level keys of the `read` text, in order, as the catalogue expects them |
| `expect.reject` | `kind` (`nul`, `surrogate`, `numeric`, `syntax`) and optionally `database`, PostgreSQL's message |
| `rest_in` | for a catalogue case, the milestone that checks the rest of it |
| `note` | why the case reads as it does |

`deferred.yaml` lists the catalogue cases of these findings that are not
stored-form cases, each with the reason and where it is expected to be
checked. A test fails unless every catalogue case of a finding with a file
here is a case or is deferred.

## Sync-folder load cases

A load case gives a sync folder as a map of files and what the loader of
[internal/syncfolder](../internal/syncfolder/README.md) makes of it: whether
the folder opens, which items it lists or filters out, and for named items
parts of the record read or the error
([SY-1](../docs/semantics/openflow.md#sy-1) to
[SY-15](../docs/semantics/openflow.md#sy-15),
[SY-V1](../docs/semantics/openflow.md#sy-v1) to
[SY-V4](../docs/semantics/openflow.md#sy-v4), PA-1, PA-2).

Each `sy-<n>.json` or `pa-<n>.json` file holds the cases of one finding:

```json
{
  "finding": "SY-5",
  "cases": [
    {
      "spec": "SY-5/b",
      "provenance": "source-derived",
      "title": "legacy array lock",
      "given": {"files": {"wmill.yaml": "defaultTs: bun\n", "f/a/x.ts": "...", "f/a/x.script.yaml": "lock: [a, b]\n"}},
      "expect": {"items": {"script": {"f/a/x": {"at": {"/lock": "a\nb"}}}}}
    }
  ]
}
```

Case fields beyond those of the decode cases:

| Field | Meaning |
|---|---|
| `spec` | the catalogue case id, or the finding id alone for a case that extends the catalogue |
| `policy` | the windmill-lite policy of [0001](../docs/design/0001-equivalence-contract.md) or [0002](../docs/design/0002-ir-and-loader.md) whose behaviour the expectation shows, where it differs from the CLI's |
| `given.files`, `given.hex_files` | the folder: file paths and their text, or their bytes in hex for text that is not UTF-8 |
| `given.start` | the working directory the folder is opened from, relative to the case; default the case root |
| `given.step` | a flow path and module id whose by-path target `expect.target` describes |
| `expect.load` | the error `kind` and message substrings (`contains`) when the folder does not open |
| `expect.root` | the sync root, relative to the case |
| `expect.paths`, `expect.filtered` | per item kind, the exact sorted paths listed, or filtered out |
| `expect.items` | per item kind and path: `at` (JSON Pointer into the item's JSON form) and `absent`, or `error` (`kind`, `contains`) |
| `expect.target` | the same, for `given.step`'s target |
| `expect.warnings`, `expect.not_warnings` | substrings that some warning holds, or that no warning holds |
| `expect.skipped` | files listed as recognised and not loaded, with their kind |
| `expect.check_ok`, `expect.check_error` | `Check` succeeds, or fails with an error of that kind |

The runner writes each case to a temporary directory, with a `.git`
directory at its root so that the upward search for `wmill.yaml` stays in the
case. `deferred.json` lists the catalogue's SY and PA cases checked elsewhere,
each with the reason; the SY-9 cases are the YAML dialect's, in
[internal/yamlx](../internal/yamlx/README.md).

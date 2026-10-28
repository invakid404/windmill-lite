# conformance

**Current state:** two levels of the shared conformance suite exist: the
OpenFlow decode cases in `openflow/`, and the first flow fixture in
`flows/`, the oracle smoke fixture, which `wltest record` records on the
Windmill CE oracle. This README covers `conformance/`. Nothing runs the flow
fixtures on windmill-lite yet (`wltest check`, milestone E1), and the step
and expression levels that
[0003](../docs/design/0003-semantics-placement.md) defines have no cases or
runners yet.

The suite pins each implementation to the
[semantics reference](../docs/semantics/README.md). Every case cites the
catalogue case it encodes (`docs/semantics/conformance.md`), and fixtures are
synthetic: invented for this repository to exercise one behaviour each.

## Levels

| Level | Directory | Status |
|---|---|---|
| decode | `openflow/<finding>.json` | in use; run by `go test ./internal/openflow` |
| flow | `flows/<area>/<case>/` | one fixture, `flows/oracle/smoke`; recorded on the oracle by `wltest record` |
| step | `steps/<lang>/<case>.yaml` | not yet |
| expr | `exprs/<case>.yaml` | not yet |

## Flow fixtures

A flow fixture is a directory in the format of
[OR-15](../docs/semantics/oracle.md#or-15): `fixture.yaml`, a `windmill/`
sync tree, an optional `setup.yaml`, one `runs/<name>.yaml` per run with its
`expect`, and `recorded/<name>.json` once the oracle has run it. The format
is described in [internal/conformance](../internal/conformance/README.md#fixtures),
and recording in [internal/oracle](../internal/oracle/README.md).

| Fixture | Encodes |
|---|---|
| `flows/oracle/smoke` | [OR-20](../docs/semantics/oracle.md#or-20): a path script doubles `x`, an inline Bun step formats it and throws above 100. Runs `ok` (`{"x":3}`, OR-11/a) and `fail` (`{"x":60}`, OR-12/a). |

The sync tree holds what `wmill folder add-missing` and `generate-metadata`
write, generated against the oracle and committed as written
([OR-8](../docs/semantics/oracle.md#or-8)); the oracle workflow regenerates
it on every recording and fails on any change.

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

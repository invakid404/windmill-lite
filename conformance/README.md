# conformance

**Current state:** six levels of the shared conformance suite exist: the
OpenFlow decode cases in `openflow/`, the stored-form cases in `jsonb/`, the
sync-folder load cases in `syncfolder/`, the compile cases in `ir/`, the
harness protocol cases in `protocol/`, and the flow fixtures in `flows/`,
which `wltest record` records on the Windmill CE oracle: the oracle smoke
fixture and the fixtures of recording batches E3c (flow execution errors),
E3b (flow execution core) and E3d (loops, branches and the result cache). This
README covers `conformance/`. Nothing runs the flow fixtures on
windmill-lite yet (`wltest check`, milestone E1), and the step and
expression levels that [0003](../docs/design/0003-semantics-placement.md)
defines have no cases or runners yet.

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
| compile | `ir/<finding>.json` | in use; run by `go test ./internal/ir` |
| protocol | `protocol/frames.json`, `protocol/exchanges/<case>.json` | in use; run by `go test ./internal/harness` (the exchanges need the pinned Bun) and, for the codec cases, by `bun test` in `harnesses/bun` |
| flow | `flows/<area>/<case>/` | `flows/oracle/smoke`, `flows/execution/fx-*` and `flows/control-flow/cf-*`; recorded on the oracle by `wltest record` |
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
| `flows/execution/fx-6` | [FX-6](../docs/semantics/execution.md#fx-6): step 0 sees the flow input; the step after a tolerated failure sees the wrapped error; a retry re-derives `previous_result` as the string `"{}"`. |
| `flows/execution/fx-7` | [FX-7](../docs/semantics/execution.md#fx-7): JavaScript and static transforms, `params`, `undefined`, an unbound `result`. |
| `flows/execution/fx-8` | [FX-8](../docs/semantics/execution.md#fx-8): `result` and `previous_result` in `stop_after_if`; no `results` in `sleep`. |
| `flows/execution/fx-9` | [FX-9](../docs/semantics/execution.md#fx-9): one `skip_if` per value the rule lists. |
| `flows/execution/fx-10` | [FX-10](../docs/semantics/execution.md#fx-10): a failure module reads an error-stopped step three ways; unknown and not-yet-run ids; which engine reads of the preceding id use memory, without a failure module ([Q-FX-15](../docs/semantics/execution.md#q-fx-15)). |
| `flows/execution/fx-16` | [FX-16](../docs/semantics/execution.md#fx-16): identity with direct input, and as a passthrough. |
| `flows/execution/fx-17` | [FX-17](../docs/semantics/execution.md#fx-17): a skipped step's passthrough; a step skipped only on its retry; no `stop_after_if` on a skipped step. |
| `flows/execution/fx-18` | [FX-18](../docs/semantics/execution.md#fx-18) (`later`): a mocked step; `stop_after_if` on the mock value; a false `skip_if` beside a mock. |
| `flows/execution/fx-19` | [FX-19](../docs/semantics/execution.md#fx-19): the clean stops, the error-stop's value, and its matrix of retry, `continue_on_error` and the failure module. |
| `flows/execution/fx-20` | [FX-20](../docs/semantics/execution.md#fx-20): a clean stop and an error-stop in a `branchone` body. |
| `flows/execution/fx-21` | [FX-21](../docs/semantics/execution.md#fx-21): a throwing `stop_after_if`, with and without a failure module. |
| `flows/execution/fx-42` | [FX-42](../docs/semantics/execution.md#fx-42)/h ([Q-FX-16](../docs/semantics/execution.md#q-fx-16)): the arguments file after `$jsonvar:` resolution, and what a JavaScript consumer sees of it. |
| `flows/execution/fx-12` … `fx-38`, `fx-v6` | Recording batch E3c (ADR 0012): the catalogue cases of [FX-12](../docs/semantics/execution.md#fx-12) to [FX-15](../docs/semantics/execution.md#fx-15) and [FX-30](../docs/semantics/execution.md#fx-30) to [FX-38](../docs/semantics/execution.md#fx-38) (FX-36's executor part), FX-V6/a and FX-10/a. Covered: error values, chaining errors, `continue_on_error`, the failure module (what it sees, its retry, `recover`, inside loop bodies), the preprocessor, final-result selection, `early_return` and cancellation. One fixture per finding, `fx-12-deno` for the Deno case. Each run's `expect` states the catalogue's expectation. It holds the recorded value instead where the catalogue asserts only a prefix of a text, says nothing about a field, or is contradicted by the oracle: one case, FX-34/a's handler id, listed in the batch manifest and in [0001](../docs/design/0001-equivalence-contract.md) §6. The batch's coverage, deferrals and prefix checks are in `internal/oracle/testdata/batches/e3c.json` ([internal/oracle](../internal/oracle/README.md)). |
| `flows/control-flow/cf-2` to `cf-15` | Batch E3d: the catalogue cases of [CF-2](../docs/semantics/control-flow.md#cf-2) to [CF-15](../docs/semantics/control-flow.md#cf-15), one fixture per finding, including the contested CF-12 cases ([Q-CF-14](../docs/semantics/control-flow.md#q-cf-14), recorded as reading (b)). The for-loop runs (cf-2 to cf-8, cf-9 run `a`, cf-11, cf-13 and the loop runs of cf-12) gate N3a; the while and branch runs (cf-10, cf-9 run `b`, cf-14, cf-15 and the branch runs `d` and `e-*` of cf-12) gate N3b. |
| `flows/execution/fx-40`, `fx-40-hash` | Batch E3d: [FX-40](../docs/semantics/execution.md#fx-40), `cache_ttl` on steps, flows and collapsed loops, including the contested FX-40/d ([Q-FX-17](../docs/semantics/execution.md#q-fx-17), recorded as reading (b)); `fx-40-hash` pins the body's script by the hash its deployment assigns. Each cached call takes a per-run nonce (`WM_ROOT_JOB_ID`) and returns its own `WM_JOB_ID`, so a cache hit shows as a result that names another job's site. Gates N4. |

`internal/oracle/testdata/batches/<batch>.json` names each batch's
findings, fixtures and the cases it defers, and `TestRecordingBatches`
holds every run of those fixtures to its recording and its recording to its
`expect`.

The sync tree holds what `wmill folder add-missing` and `generate-metadata`
write, generated against the oracle and committed as written
([OR-8](../docs/semantics/oracle.md#or-8)); the oracle workflow regenerates
it on every recording and fails on any change. `generate-metadata` writes a
container's defaults into `flow.yaml` (`skip_failures` and `parallel` on a
loop, `parallel` on a `branchall`, and `skip_failure`, `parallel` and `expr`
on each branch), so a field left out of such a tree never reaches the
oracle absent. A fixture with `oracle: {deploy: api}` (`cf-6`, `cf-15`,
`fx-40-hash`) is instead created through the REST API exactly as written
and never regenerated, so its tree holds no generated metadata; each of its
recordings' provenance holds the target flow's submitted value, the value
the API answers after the dependency job, and what `generate-metadata`
writes for the same source ([internal/oracle](../internal/oracle/README.md#api-deployment)).
Either way the recordings show deployed flows, the reference configuration
of [ADR 0001](../docs/design/0001-equivalence-contract.md) §1.

Each E3b run's `expect` was written from the reference before the oracle
first ran it. Where the oracle disagreed, the run's `note` says what was
predicted, what was recorded, and in which oracle runs.

### Recording batches

The E3b rows above are recording batch E3b of
[0012](../docs/design/0012-layout-and-milestones.md), the flow-execution
core. Its manifest, with each run's gate, is
`internal/oracle/testdata/batches/e3b.json`, described in
[internal/oracle](../internal/oracle/README.md#recording-batches).

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

## Compile cases

A compile case gives a sync folder, as a load case does, and optionally a
host contract and build options, and what the compiler of
[internal/ir](../internal/ir/README.md) makes of it: the errors, or parts of
the compiled program ([0002](../docs/design/0002-ir-and-loader.md)). Most
catalogue cases they encode are flow cases whose expectation depends on
what a deploy applies (container defaults, the native switch, virtual
steps, collapse); a compile case checks that part (`part: compile`), and
its `note` says which milestone checks the run.

```json
{
  "finding": "CF-8",
  "cases": [
    {
      "spec": "CF-8/c",
      "provenance": "disputed",
      "part": "compile",
      "note": "Both readings agree; the case is carried as disputed with its finding. The run half, every iteration getting the flow input, is N3a's.",
      "title": "a loop at step 0 collapses",
      "given": {"files": {"wmill.yaml": "defaultTs: bun\n", "f/x/main.flow/flow.yaml": "..."}},
      "expect": {"steps": {"f/x/main m:l": {"at": {"/forloop/collapse": true}}}}
    }
  ]
}
```

Case fields beyond those of the load cases:

| Field | Meaning |
|---|---|
| `provenance` | as the catalogue tags the case, `disputed` included: a disputed case's `note` says which reading it encodes and why the design fixes it ([0001](../docs/design/0001-equivalence-contract.md) §6) |
| `part` | `compile` when the catalogue case's run half is checked elsewhere |
| `given.contract` | the host contract, as the YAML text of its file; without one every flow and script is a root |
| `given.options` | build options (`{"strict": true}`) |
| `expect.errors` | errors that must all be among those the compile reports: `kind` and message substrings (`contains`) |
| `expect.at`, `expect.absent` | JSON Pointers into the program's JSON form |
| `expect.flows`, `expect.no_flows` | per flow path, pointers into that flow; flows that must not be in the program (opaque or unreached) |
| `expect.steps` | per `"<flow path> <site>"`, pointers into that module, and `script` for its inventory entry; the site is a site path with iteration segments left out (`m:l/m:x`, `m:o/b:default/m:c`, `s:failure`) |
| `expect.lists` | per `"<flow path>"` or `"<flow path> <site>"` (a loop, or a branch), the site segments of that module list in order, occurrences and virtual steps included |
| `expect.scripts` | per inventory id, pointers into that entry |

`deferred.json` lists the catalogue cases of these findings that are
neither compile cases nor checked at the decode or load level, each with the
reason and where it is expected to be checked. A test fails unless every
catalogue case of a finding with a file here is accounted for, and unless
every case that `openflow/deferred.json` defers to `conformance/ir` is a
compile case.

## Protocol cases

The harness protocol ([0004](../docs/design/0004-harness-protocol.md)) is
windmill-lite's own, so these cases carry provenance `policy`. They pin the
Go side ([internal/harness](../internal/harness/README.md)) and each harness
([harnesses/bun](../harnesses/bun/README.md)) to one set of frames. Each
file names the design section it encodes (`design`) and the findings it
relies on (`relies_on`); a case that also encodes a catalogue case says so
in `spec` (the version check, `WR-3/a`).

**`frames.json`** holds the codec's golden frames. `go test ./internal/harness
-run TestFrameCases -args -update` writes `payload` and `frames` from Go's
encoder; both codecs then check every case unless `sides` limits it.

| Kind | Fields | Check |
|---|---|---|
| encode | `message` (JSON) or `message_text` (a payload in any key order), `payload` | the message decodes and encodes to `payload`, which re-encodes to itself; `raw` names values whose bytes must survive exactly |
| split | an encode case plus `split` (`stream`, `chunk_bytes`) and `frames` | the payload splits into exactly those part frames, which reassemble to it |
| sequence | `frames`, `messages` | the frames, read in order, decode and reassemble to the messages |
| reject | `bytes_hex` or `frames`, optional `max_frame` and `max_message`, `error` | reading raises the protocol error code `error` |

**`exchanges/<case>.json`** are transcripts that the Go tests play against
the real harness, on the synthetic bundle in `protocol/bundle/` (a
`manifest.json` whose SHA-256 is the bundle digest, and one module per
script under `scripts/bun/`). Each step does one thing:

| Step | Meaning |
|---|---|
| `send` | encode a message canonically and send it, as part frames when it is larger than a frame; `fill` stands in for a long string, `split` sends it as small parts with one whole frame between them |
| `send_text` | send one frame with this exact payload |
| `expect` | the next message from the harness matches, keys in order; `parts` is how many part frames carried it |
| `quiet_ms` | no message arrives for that long |
| `exit` | the process exits with this code and sends nothing more |
| `stdout_contains` | the process's stdout contains the text |

Expected values may use placeholders: `<any>`, `<string>`, `<int>`, `<pid>`
(the harness's pid), `<bundle>` (the fixture bundle's digest) and `<c*N>`
(a string of `N` copies of `c`).

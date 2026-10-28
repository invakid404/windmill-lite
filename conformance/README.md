# conformance

**Current state:** six levels of the shared conformance suite exist: the
OpenFlow decode cases in `openflow/`, the stored-form cases in `jsonb/`, the
sync-folder load cases in `syncfolder/`, the compile cases in `ir/`, the
harness protocol cases in `protocol/`, the Bun build cases in `build/`, and
the first flow fixture in `flows/`, the oracle smoke fixture, which
`wltest record` records on the Windmill CE oracle. This
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
| build | `build/<finding>.json` | in use; run by `go test ./internal/bundle` through the Bun build helper on the pinned Bun |
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

## Build cases

A build case gives a sync folder and one or more Bun scripts, builds them
with [internal/bundle](../internal/bundle/README.md) and the
[Bun build helper](../harnesses/bun/build/README.md), and checks what the
build records for each script (the signature of each export, its exports,
annotations, bundler target, workflow-as-code markers, the workspace
scripts it inlined, the host-provided modules it uses) or the problem it
reports. A case can also run the built modules on the pinned Bun and check
what their exports return, which is how the import transforms
([WR-26](../docs/semantics/languages.md#wr-26),
[WR-28](../docs/semantics/languages.md#wr-28),
[WR-29](../docs/semantics/languages.md#wr-29)), the env rewrite and the
host-provided-module shims are checked against real behaviour. Most cases
check the build half of a catalogue case whose run belongs to the step
suite; `part` says so and `rest_in` names where the rest is checked.

Each `<finding>.json` holds the cases of one finding, or `d12.json` those
of deviation D12, which has no finding:

```json
{
  "finding": "WR-8",
  "cases": [
    {
      "spec": "WR-8/a",
      "provenance": "source-derived",
      "title": "a local alias to Date",
      "part": "build",
      "rest_in": "C3a hydrates the argument",
      "given": {"source": "type Ts = Date; export function main(d: Ts) { return d instanceof Date; }"},
      "expect": {"signatures": {"main": {"params": [{"name": "d", "date": true}]}}}
    }
  ]
}
```

Case fields beyond those of the decode cases:

| Field | Meaning |
|---|---|
| `given.source`, `given.path` | the script, and its Windmill path (default `f/case/main`), written as `<path>.ts` |
| `given.inline`, `given.flow` | the script is inline; `flow` gives `path`, `module` and `explicit_path`, from which the import base is computed (WR-29) |
| `given.entries` | more scripts, each with `id` and the fields above |
| `given.files` | other folder files; every `.ts` file is a script at its path without the extension |
| `given.dependencies`, `given.packages` | a script's npm dependencies, and the synthetic packages (`name@version` and their files) that a registry the test serves holds; the runner makes the lock |
| `given.host_modules` | host-provided modules: `id`, `key`, and a folder `file` or a `package` with its `dependencies` |
| `expect.signatures` | per export, `params` exactly, or `error_contains` |
| `expect.exports`, `expect.annotations`, `expect.target`, `expect.workflow_as_code`, `expect.imports`, `expect.host_modules` | what the build recorded; `annotations` lists the flags that are set |
| `expect.problem` | the problem `kind` and message substrings |
| `expect.bundle_contains`, `expect.bundle_not_contains`, `expect.first_line` | text of the bundled module |
| `expect.entries` | the same expectations for other scripts, by id |
| `expect.provided`, `expect.host_module_order`, `expect.order_problem`, `expect.warnings` | host-provided modules: exports, imports or problem by id; the registration order or the cycle; warning codes |
| `expect.run` | `env` (process.env is replaced by it), `register` (host-provided copies registered, in order) and `calls`, each an `entry`, an `export` (default `main`), positional `args` and the `result` or an `error` substring |

`deferred.json` lists the catalogue cases of these findings that are not
build cases, each with the reason and where it is checked. A test fails
unless every catalogue case of a finding with a file here is a case or is
deferred.

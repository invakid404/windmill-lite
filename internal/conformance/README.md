# internal/conformance

**Current state:** the shared identity and observation vocabulary of
milestone K1, the fixture format, normaliser and comparator of milestone
E2, and the expression-case format, its lifting into generated flows, and
the claims and unit cases of recording batch E3a
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)). This README
covers `internal/conformance` and its `testdata/`. The oracle harness
([internal/oracle](../oracle/README.md)) uses all of it; the flow runner
that checks windmill-lite against a fixture (`wltest check`) is milestone E1
and does not exist yet.

## What it holds

| File | Contents |
|---|---|
| `sitepath.go` | `SitePath`, `Segment` and the encoder of [ADR 0011](../../docs/design/0011-differential-testing.md), which answers [Q-OR-17](../../docs/semantics/oracle.md#q-or-17) |
| `status.go` | `RunStatus` (Windmill's four job statuses plus windmill-lite's `latched` and `halted`), `StepStatus` ([OR-V2](../../docs/semantics/oracle.md#or-v2)) and `ModuleStatus` ([FX-1](../../docs/semantics/execution.md#fx-1)) |
| `observation.go` | `Observation`, `StepObservation` and `BranchChosen`: the normalised observation of [OR-15](../../docs/semantics/oracle.md#or-15), plus the module status, carried value and resolved arguments (see below) |
| `fixture.go` | `Fixture`, `Run`, `Setup`, `Recording`: the flow-fixture format of [OR-15](../../docs/semantics/oracle.md#or-15), with `LoadFixture`, `FindFixtures` and `WriteRecording` (see [Fixtures](#fixtures)) |
| `exprcase.go` | `ExprFile`, `ExprCase`, `ExprExpect`: the expression level of [ADR 0003](../../docs/design/0003-semantics-placement.md), with `LoadExprFile`, `FindExprFiles` and `LiftExprFile` (see [Expression cases](#expression-cases)) |
| `claims.go` | `Claim`, `CheckClaims`, `Delay`, `ValueAt`, `SameJSON`: partial assertions about an observation |
| `unit.go` | `UnitCase`, `UnitContext`, `MessageMatch`: a flow fixture's expressions restated at unit level |
| `compare.go` | `Compare`, `CompareOptions`, `Difference`: the comparator; JSON Pointer helpers; `IndentJSON` |
| `text.go` | `EncodeText`, `ParseText`, `SplitJobIDs`: normalised strings, with the escape that keeps them injective |
| `normalise.go` | `Normaliser` and `MaskPaths`: the value rules of [OR-14](../../docs/semantics/oracle.md#or-14) |
| `yamljson.go` | `YAMLToJSON`: fixture YAML to JSON text, keeping member order and number text |

The engine, the harness, the oracle and `wltest` share this vocabulary, so
the package imports no other windmill-lite package. Code that runs
windmill-lite or the oracle to produce an observation lives with its caller.

## Observations

A step's observation has OR-15's fields (`status`, `ran`, `args`, `result`,
`branch_chosen`, `skipped`) and three more, which
[ADR 0011](../../docs/design/0011-differential-testing.md) adds because the
stored fields cannot recover them:

- `module`: the module's own status, which differs from its job's after an
  error stop ([FX-V2](../../docs/semantics/execution.md#fx-v2));
- `carried`: the value the module carries on;
- `resolved_args`: the arguments after resolution
  ([JE-V3](../../docs/semantics/expressions.md#je-v3)).

Encoding and decoding put every step in canonical form:

- a `not_run` step has `"ran":false` ([OR-V2/a](../../docs/semantics/conformance.md#or-v2));
- `module` is dropped when it equals what the job status implies;
- `carried` is dropped when it has the same text as `result`, and
  `resolved_args` when it has the same text as `args`, whitespace aside. A
  value with the same keys in another order, or a number spelt otherwise,
  is kept: the codec cannot know whether a fixture compares with
  `key_order: strict` ([OR-14](../../docs/semantics/oracle.md#or-14)), so a
  comparator expands the absences and then applies the fixture's policy.

Steps whose fields contradict each other are rejected, as is a module
status on an attempt, iteration or branch site that differs from the one
the job implies; the implied one is accepted there and dropped, whether the
observation was decoded or built in Go.
`testdata/observation/dimensions.json` holds the cases, which
`TestObservationDimensionCases` runs:

- observations that differ only in one of the three fields must compare
  unequal, from FX-V2/a and JE-V3/a;
- spellings that must compare equal;
- explicit values that must survive a round trip, because they differ from
  their default only in key order or number spelling;
- observations that must be rejected.

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

## Fixtures

A flow fixture is a directory (OR-15, [ADR 0003](../../docs/design/0003-semantics-placement.md)):

| Entry | Content |
|---|---|
| `fixture.yaml` | `id`; `spec` (findings and catalogue cases); `scope` (`core`, `later`, `out`, `EE`); `provenance` (the catalogue's tags); `languages`; `oracle: {workers, effects, concurrency}`; `compare: {mode, key_order, stack, mask, unordered}`; `note` |
| `windmill/` | the sync tree the oracle's CLI pushes, with the metadata and locks `generate-metadata` writes |
| `setup.yaml` | optional `resource_types`, `variables` and `resources`, created after the push |
| `runs/<name>.yaml` | `target`, `kind` (`flow` or `script`), `args` (an object), `cancel_after_ms`, `spec`, `expect` (an observation), `note` |
| `recorded/<name>.json` | `{"observation": …, "provenance": …}`, written by `wltest record` |

Every file decodes strictly: unknown keys, YAML aliases and merge keys,
repeated keys, and numbers that are not JSON numbers (`0x1F`, `1_000`,
`.inf`) are refused, so a value is always the JSON its author wrote.
`compare` paths are RFC 6901 JSON Pointers into the observation, for example
`/steps/m:a0/args/fi_keys`; a `/` inside a site path is `~1`. `stack` masks a
string as `<stack>` (OR-V3 clause c) and `mask` replaces any value with
`<masked>` (environment-derived values, OR-29).

A run may also carry `claims`, `delays` and `unit`, which the comparator
does not read (see [Claims](#claims)). A fixture whose tree is generated
sets `Tree`, `RecordedIn` and `Generated` instead of using `windmill/` and
`recorded/` under its directory.

## Expression cases

`LoadExprFile` reads one `conformance/exprs/<finding>.yaml` strictly, and
refuses a case that names a predecessor, a handler or a container with
`contextual_case_needs_flow_fixture` (`ExprCaseError`): a case key such as
`previous_result`, `results` or `failure_module`, or a site other than
`transform`, `static` and `skip_if`. It also refuses a file not named after
its finding, and an expectation its site cannot produce. `FindExprFiles`
finds the files below its roots once each, skipping flow fixtures, and
`IsExprFile` tells one by its path. The format is described in
[conformance](../../conformance/README.md#expression-cases).

`LiftExprFile` writes a file's generated sync tree into a new directory and
returns its `Fixture`: `wmill.yaml`, the sink script `f/wl_expr/sink.ts`
(it returns `null`), and per case a flow `f/wl_expr/<case>` written as
JSON, which the CLI and `internal/yamlx` read alike. Each case becomes one
run, named by its spec and variant (`je-7-a`, `je-1-math-trig`), with the
case's flow input as its arguments and, as its claims, what the case's
`expect` says about module `b`'s recorded argument or error. Recordings are
read from and written to `conformance/exprs/recorded/<finding>/`.

## Claims

A `Claim` is a JSON Pointer into an observation's canonical encoding and
one assertion about the value there: `equals` (compared as the comparator
compares values, with `key_order: strict` optional), a string's `prefix`,
`contains` and `not_contains`, an array's `length`, or `absent`.
`CheckClaims` returns one line per claim that does not hold. A `Delay` is a
lower bound on the time from one job's completion to another's start,
checked against a recording's provenance. A `UnitCase` restates an
expression at unit level with the context its evaluation sees; a
`MessageMatch` matches an error object's name and message exactly or by
prefix and substrings, as the catalogue compares engine messages.
`ValueAt` reads one value of an observation, and `SameJSON` compares two
JSON texts as the comparator does.

## Normalised text

A normalised string ([OR-14](../../docs/semantics/oracle.md#or-14) items 1
and 2) is user text with walked job ids replaced by placeholders
`<job:PATH>`, plus three markers: `<stack>`, `<loc>` and `<masked>`. User
text that already looks like one of them would make two different values
normalise alike, and the drafted `<<job:` escape does not prevent it: a
user `<` before a walked id gives the same text. ADR 0011 left the escape to
milestone E2, which chose this one:

- every maximal run of `<` in user text that is followed by a placeholder, a
  marker, or text beginning `job:`, `stack>`, `loc>` or `masked>` is written
  twice as long;
- every other `<` is written once, so text without such a run is unchanged.

A reader counts the run of `<` before such a head: an odd count ends in a
placeholder or marker, an even count is literal. `ParseText` is that reader;
`EncodeText(ParseText(s)) == s` for every text it accepts. Examples, with
module `a`'s job id written `ID`:

| Raw text | Normalised |
|---|---|
| `ID` | `<job:m:a>` |
| `<job:m:a>` | `<<job:m:a>` |
| `<ID` | `<<<job:m:a>` |
| `<stack>` | `<<stack>` |
| `a < b` | `a < b` |

`testdata/text/cases.json` holds the encodings and the pairs that must stay
distinct; `FuzzTextRoundTrip`, `FuzzTextInjective` and `FuzzParseText` back
them.

## The normaliser

`Normaliser.Value` rewrites one raw JSON value, keeping member order,
number text and duplicate elements:

1. **Stacks** ([OR-V3](../../docs/semantics/oracle.md#or-v3)). Given the
   run's runtime errors (the stored results of failed leaf jobs that ran a
   script), a value deep-equal to one of them has its `error.stack` masked,
   and a value deep-equal to one's inner error has its `stack` masked.
   Nothing else is: a handler's own `{"error":{"stack":…}}` keeps its data.
2. **Job ids**, in every string and member name, become placeholders, and
   user text is escaped as above. Unknown UUIDs stay.
3. **The NUL `InternalErr`** ([OR-27](../../docs/semantics/oracle.md#or-27)):
   for `name: "InternalErr"` with a message that starts
   `Internal: Could not add completed job ` and ends in
   ` @<file>:<line>:<col>`, the suffix becomes ` <loc>`. No other message
   loses its suffix.

`MaskPaths` then applies a fixture's `stack` and `mask` pointers.
`testdata/normalise/cases.json` holds the cases.

## Comparison

`Compare(got, want, options)` returns the differences as
`<pointer>: <got> != <want>` (case OR-15/b):

- **exact** by default; `Subset` accepts extra members and steps, and has
  no member order to compare, so a fixture cannot combine `subset` with
  `key_order: strict`;
- objects without regard to member order ([OR-13](../../docs/semantics/oracle.md#or-13));
  `StrictKeyOrder` compares member order inside values, never in the
  observation's own fields;
- arrays in order, except at `Unordered` pointers, which compare as
  multisets ([OR-23](../../docs/semantics/oracle.md#or-23)): the elements
  are matched one to one by a maximum bipartite matching, so in subset mode
  a broad expected element never takes the only match of a narrower one.
  Each expected element is compared at its own index, so strict key order
  applies inside it and unordered pointers below it (for example
  `/result/0/values`) still hold;
- numbers by their JSON text;
- a step field one side leaves to its canonical absence and the other spells
  out is expanded first, so the two spellings compare equal;
- `WithRecording` leaves `carried` and `resolved_args` out, because a
  recording holds neither (ADR 0011).

`testdata/compare/cases.json` holds the cases.

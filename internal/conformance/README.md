# internal/conformance

**Current state:** working. The package holds the vocabulary that every
conformance test shares: site paths, statuses and observations. It loads
the case formats of flow fixtures, expression cases, step cases and
host-plugin cases, and it holds the normaliser, the comparator and stub
mode's replay service. This README covers `internal/conformance` and its
`testdata/`; the cases themselves are in [conformance](../../conformance/README.md).

A [conformance case](../../docs/glossary.md#conformance-case) states inputs
and the outputs that windmill-lite must produce. This package defines how
the flow-level and step-level cases are written, how a run becomes an
observation, and how two observations are compared:

- **Who calls it:**
  - the [oracle](../../docs/glossary.md#oracle) harness
    ([internal/oracle](../oracle/README.md)), which records fixtures with it;
  - `wltest` ([cmd/wltest](../../cmd/wltest/README.md)), which runs the step
    cases and flow fixtures on windmill-lite;
  - the engine ([internal/engine](../engine/README.md)), which writes its
    statuses and site paths;
  - [wl](../../wl/README.md), which writes site paths and run statuses, and
    whose tests run the host-plugin cases;
  - [internal/ir](../ir/README.md), which builds site paths;
  - [cmd/windmill-lite](../../cmd/windmill-lite/README.md), which reads the
    statuses, and whose test `host_test.go` runs the plugin cases in host
    mode;
  - the tests of [internal/harness](../harness/README.md), which build a
    site path;
  - [internal/expr](../expr/README.md), whose tests run the expression
    cases.
- **What it calls:** no other windmill-lite package, so every side can
  share it. Code that runs windmill-lite or the oracle to produce an
  observation lives with its caller.

The [ADR](../../docs/glossary.md#adr)
[Where semantics live, and the conformance-suite format](../../docs/design/0003-semantics-placement.md)
defines the levels and their formats, and the ADR
[Differential testing and performance measurement](../../docs/design/0011-differential-testing.md)
defines site paths, the observation's extra fields, stub mode and the
location rule. The oracle findings of the
[semantics reference](../../docs/semantics/oracle.md) define the
observation, its normalisation and its comparison.

## Files

| File | What it holds |
|---|---|
| `sitepath.go` | `SitePath`, `Segment` and the encoder |
| `status.go` | `RunStatus`, `StepStatus` and `ModuleStatus` |
| `observation.go` | `Observation`, `StepObservation`, `BranchChosen` and `Response` |
| `fixture.go` | The flow-fixture format: `Fixture`, `Run`, `Setup`, `Recording` and their loaders |
| `exprcase.go` | The expression-case format, its loader, and `LiftExprFile` |
| `stepcase.go` | The step-case format, its loader, and the step comparison |
| `plugins.go` | The host-plugin case format, its loader, and the check of a run's outcome |
| `claims.go` | `Claim`, `CheckClaims`, `Delay`, `ValueAt`, `SameJSON` |
| `unit.go` | `UnitCase`, `UnitContext`, `UnitExpect`, `MessageMatch` |
| `compare.go` | The comparator, JSON Pointer helpers and `IndentJSON` |
| `text.go` | Normalised text: `EncodeText`, `ParseText`, `SplitJobIDs` |
| `normalise.go` | `Normaliser`, `MaskPaths`, and the location rules `StripLocations` and `StripNotFoundLocations` |
| `replay.go` | Stub mode: `ReplayTable`, `ReplayService`, `CheckReplay` |
| `yamljson.go` | `YAMLToJSON`: case YAML to JSON text, member order and number text kept |
| `doc.go` | The package comment |

## Public surface

Every exported package-level name, grouped by concern. Their methods and
fields are in `go doc`.

### Site paths and statuses

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `SitePath`, `ParseSitePath`, `ErrInvalidSitePath` | struct, function, variable | A site path in canonical text ([Site paths](#site-paths)) | engine, oracle, `wltest`, `internal/ir`, `wl`, `cmd/windmill-lite`, the tests of `internal/harness` and `internal/expr`; `ErrInvalidSitePath` only inside the package |
| `Segment`, `SegmentKind` (`KindModule`, `KindIteration`, `KindBranch`, `KindSlot`, `KindAttempt`) | struct, type | One typed segment | `Segment`: engine, oracle, `wltest`, `internal/ir`; `SegmentKind`: `wl` |
| `Module`, `RepeatedModule`, `ModuleSegments`, `Iteration`, `Branch`, `DefaultBranch`, `FailureSlot`, `PreprocessorSlot`, `Attempt` | functions | Build segments | engine, oracle, `wltest`, `internal/ir`; `Module` also the tests of `internal/harness` |
| `RunStatus` (`RunSuccess`, `RunFailure`, `RunCanceled`, `RunSkipped`, `RunLatched`, `RunHalted`) | type | A run's status: Windmill's four, and windmill-lite's `latched` and `halted` | engine, `wl`, oracle, `wltest` |
| `StepStatus` (`StepSuccess`, `StepFailure`, `StepCanceled`, `StepSkipped`, `StepNotRun`) | type | A step's job status ([OR-V2](../../docs/semantics/oracle.md#or-v2)) | engine, oracle, `wltest`, `cmd/windmill-lite` |
| `ModuleStatus` (`ModuleSuccess`, `ModuleFailure`, `ModuleNotRun`, `ModuleInProgress`) | type | A module's own status ([FX-1](../../docs/semantics/execution.md#fx-1)) | engine, oracle, `cmd/windmill-lite` |

### Observations and recordings

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Observation`, `StepObservation`, `BranchChosen` | structs | A run's normalised observation ([Observations](#observations)) | engine, oracle, `wltest` |
| `Response`, `BodyBase64` | struct, constant | A run-and-wait run's HTTP response; `base64`, the `encoding` of a body that is not UTF-8 | oracle, `wltest` |
| `Recording`, `WriteRecording` | struct, function | A recording, `{"observation", "provenance"}`, and its writer | oracle |

### Flow fixtures

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Fixture`, `LoadFixture`, `FindFixtures` | struct, functions | A flow fixture, its loader, and the search below roots ([Fixtures](#fixtures)) | oracle, `wltest` |
| `FixtureFile`, `SetupFile`, `SyncTreeDir`, `RunsDir`, `RecordedDir` | constants | `fixture.yaml`, `setup.yaml`, `windmill`, `runs`, `recorded` | the loaders |
| `OracleNeeds`, `CompareSpec` | structs | A fixture's `oracle` and `compare` | oracle, `wltest` |
| `DeployCLI`, `DeployAPI` | constants | `cli` and `api`, the values of `oracle.deploy` | oracle |
| `SetupAfterPush`, `SetupBeforePush`, `SetupEachRun` | constants | The values of `oracle.setup` | oracle, `wltest` |
| `Setup`, `SetupResourceType`, `SetupVariable`, `SetupResource` | structs | `setup.yaml` | oracle, `wltest` |
| `Run`, `RunBatches`, `SubmitRunWait` | struct, function, constant | A run; the runs in submission batches; `run_wait_result` | oracle, `wltest` |
| `Copy`, `FixtureProvider` | structs | A run's `copies` entry; a fixture's named provider | `wltest` |
| `YAMLToJSON` | function | Case YAML to JSON text | the loaders, oracle |

### Expression and step cases

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `ExprFile`, `ExprCase`, `ExprExpect` | structs | An expression file; one case of it; what the catalogue expects of the case ([Expression cases](#expression-cases)) | oracle, `wltest`, `internal/expr` |
| `LoadExprFile` | function | Read and check one expression file | oracle, `wltest`, `internal/expr` |
| `FindExprFiles`, `IsExprFile` | functions | Find the expression files under directories, skipping fixtures; tell whether a path names one | oracle, `wltest`; `FindExprFiles` also the tests of `internal/expr` |
| `LiftExprFile` | function | An expression file as a generated fixture | oracle, `wltest` |
| `ExprCaseError`, `ErrContextualCase` | struct, constant | A refused expression case; `contextual_case_needs_flow_fixture` | the loader |
| `ExprDirName`, `ExprDeferredFile`, `ExprFolder`, `ExprSink`, `ExprStep`, `ExprInput` | constants | `exprs`, `deferred.yaml`, and the generated flows' folder `f/wl_expr`, sink `f/wl_expr/sink`, module `b` and input `x` | the lifter; `internal/expr` uses `ExprDirName` |
| `SiteTransform`, `SiteStatic`, `SiteSkipIf` | constants | The sites an expression case may use | the loader |
| `StepFile`, `StepCase`, `StepRun` | structs | A step file; one case, its script and settings; one run of it ([Step cases](#step-cases)) | `wltest` |
| `StepCheck`, `StepLate` | structs | What a run checks; one report that arrives after the outcome | `wltest` |
| `StepCompare`, `StepHostModule` | structs | A case's comparison options; one host-provided module of a case | `wltest` |
| `LoadStepFile` | function | Read and check one step file | `wltest` |
| `FindStepFiles`, `IsStepFile` | functions | Find the step files under directories; tell whether a path names one | `wltest` |
| `StepDeferred`, `LoadStepDeferred` | struct, function | `steps/<lang>/deferred.yaml` | `wltest` |
| `CompareStep`, `CompareStepCheck`, `CompareStepNode`, `DefaultStepStack` | functions, constant | The comparison of a step's observation; `/result/error/stack` | `wltest` |
| `StepDirName`, `StepDeferredFile`, `StepFolder`, `StepModule` | constants | `steps`, `deferred.yaml`, the default folder `f/wl_step` and module `a` | `wltest` |
| `ModeWarm`, `ModeFresh`, `LangBun`, `LangBunNative`, `LangNativeTS` | constants | The modes of a `by_mode` check, and a step case's languages | `wltest` |
| `StepLeftBehind`, `StepNativeUntracked`, `StepPending` | constants | `left_behind`, `native_untracked`, `pending` | `wltest` |

### Plugin case types

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `PluginFile`, `PluginCase` | structs | The plugin file; one case ([Host-plugin cases](#host-plugin-cases)) | `wl`, `cmd/windmill-lite` tests |
| `PluginSpec`, `PluginFailure` | structs | A case's declarative plugin; how one of its providers fails for a path | `wl`, `cmd/windmill-lite` tests |
| `PluginRun`, `PluginThen`, `PluginExpect` | structs | A run's own hooks and budgets; one later run of the case; what a run expects | `wl`, `cmd/windmill-lite` tests |
| `LoadPluginFile` | function | Read and check the plugin file | `wl`, `cmd/windmill-lite` tests |
| `PluginFile.BundledPlugin`, `BundledPluginPath` | method, constant | The TypeScript case plugin as files of the cases' folder, over a copy of `caseplugin.ts`, and the host contract that names it, for a bundle built with it | the same |
| `PluginOutcome` | struct | What a run of a case showed, which `PluginCase.Check` compares | the same |
| `PluginLog`, `PluginTrace`, `ReadSettlement` | constants, function | `plugin_log`, the warning that carries a plugin's log; `plugin_trace`, the one that carries what the run's trace showed the TypeScript plugin; the report of settled runs | the same |

### Claims, comparison and normalisation

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Claim`, `CheckClaims` | struct, function | A partial assertion at a JSON Pointer; check a list of them on an observation ([Claims and unit cases](#claims-and-unit-cases)) | oracle, `wltest` |
| `Delay` | struct | A lower bound on the time between two sites' jobs | oracle, `wltest` |
| `ValueAt`, `SameJSON` | functions | The value at a JSON Pointer of an observation; whether two JSON texts are equal, with or without key order | oracle, `wltest` |
| `UnitCase`, `UnitContext`, `UnitExpect`, `MessageMatch` | structs | Unit cases | `internal/expr`, oracle |
| `Compare`, `CompareOptions`, `Difference` | function, structs | The comparator ([Comparison](#comparison)) | oracle, `wltest` |
| `SplitPointer`, `JoinPointer`, `IndentJSON` | functions | JSON Pointer helpers, and JSON indented with its order and text kept | oracle, `wltest` |
| `Normaliser`, `MaskPaths` | struct, function | The normaliser ([The normaliser](#the-normaliser)) | oracle, `wltest` |
| `StripLocations`, `StripNotFoundLocations` | functions | The location rules ([Location suffixes](#location-suffixes)) | `wltest` |
| `TextPart` | struct | One part of a normalised text: a literal, a walked job's placeholder, an unwalked job's number, or a marker ([Normalised text](#normalised-text)) | the normaliser |
| `Literal`, `JobPart`, `UnwalkedPart`, `MarkerPart` | functions | Make a part: user text; a walked job's site; an unwalked job's number (from 1); a marker | the normaliser |
| `EncodeText`, `ParseText`, `ErrInvalidText` | functions, variable | Write parts as normalised text, and read it back; `ParseText`'s error | the normaliser |
| `SplitJobIDs` | function | Split a raw string at the walked job ids it holds | the normaliser |
| `UnwalkedPlaceholder` | function | `<job:?N>` for an unwalked job's number | the normaliser |
| `MarkerStack`, `MarkerLoc`, `MarkerMasked` | constants | `<stack>`, `<loc>`, `<masked>` | the normaliser |

### Replay types

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `ReplayTable`, `Outcome` | type, struct | A run's `replay` table ([The replay table](#the-replay-table)) | oracle, `wltest` |
| `ReplayService`, `ReplayPath`, `Locator` | struct, constant, interface | The replay service at `/replay`, and how it finds a job's site | oracle, `wltest` |
| `ReplayRequest`, `ReplayCall`, `ReplayEffect` | structs, function | A stub's call, as received and as answered | `ReplayCall`: oracle, `wltest`; the others inside the package |
| `CheckReplay` | function | The check of a run's calls after it ([CheckReplay](#checkreplay)) | oracle, `wltest` |

## Observations

An observation is what a run shows, normalised
([OR-15](../../docs/semantics/oracle.md#or-15)): the oracle's walk of a
run, or windmill-lite's [trace](../../docs/glossary.md#trace) of one.

| Field | Type and requirement | What it holds |
|---|---|---|
| `status` | string, required | The run's status (`RunStatus`) |
| `result` | JSON, required | The run's result, as JSON |
| `steps` | object of site path to step, optional | Each step by its site path ([Site paths](#site-paths)) |
| `effects` | list of JSON, optional | The effects that the run's scripts posted to the effect sink, and in stub mode its replay calls |
| `response` | object, optional | A run-and-wait run's HTTP response, and nothing for any other run |

A step's observation:

| Field | Type and requirement | What it holds |
|---|---|---|
| `status` | string, required | The step's job status (`StepStatus`) |
| `ran` | boolean; absent means `true` | Whether a job ran: `false` for a `not_run` step and a module with a nil job |
| `args`, `result` | JSON, optional | The job's stored arguments and result |
| `branch_chosen` | object `{type, branch}`; a default branch has no `branch` | A `branchone`'s chosen branch: `type` and `branch` |
| `skipped` | boolean; absent means `false` | The module's `skipped` flag |
| `module` | string; absent means the status the job implies | The module's own status, which differs from its job's after an error stop ([FX-V2](../../docs/semantics/execution.md#fx-v2)) |
| `carried` | JSON; absent means the same as `result` | The value the module carries on |
| `resolved_args` | JSON; absent means the same as `args` | The arguments after resolution ([JE-V3](../../docs/semantics/expressions.md#je-v3)) |

The differential testing ADR adds the last three to both sides'
observations. The oracle's API shows the module's own status, but neither a
carried value nor resolved arguments, so a recording holds `module` but not
`carried` or `resolved_args`.

### Canonical form

Encoding and decoding put every step in canonical form:

- a `not_run` step has `"ran":false`. A step with `ran: true` and status
  `not_run`, or whose job and module disagree on `not_run`, is refused;
- `module` is dropped when it equals what the job status implies: a
  successful or skipped job implies a successful module, a failed or
  canceled job a failed one, and an unrun job an unrun module;
- `carried` is dropped when it has the same text as `result`, and
  `resolved_args` when it has the same text as `args`, whitespace aside;
- a value with the same keys in another order, or a number spelled
  otherwise, is kept: the codec cannot know whether a fixture compares with
  `key_order: strict` ([OR-14](../../docs/semantics/oracle.md#or-14)), so the
  comparator expands the absences and then applies the fixture's policy. An
  absent `ran` reads as `true` and an absent `skipped` as `false` where they
  apply.

Steps whose fields contradict each other are refused. So is a module status
on an attempt, iteration or branch site that differs from the one the job
implies; the implied one is accepted there and dropped, whether the
observation was decoded or built in Go.

### The response

A run submitted through run and wait (`submit: run_wait_result`) has a
`response` ([FX-36](../../docs/semantics/execution.md#fx-36),
[FX-37](../../docs/semantics/execution.md#fx-37)):

| Field | Type and requirement | What it holds |
|---|---|---|
| `status` | integer, required | The HTTP status |
| `content_type` | string, optional | The `Content-Type` header as sent, parameters included |
| `encoding` | string, optional | `base64` for a body that is not UTF-8; absent otherwise |
| `body` | string, required | The body as text, or its base64 text; `Response.Bytes` decodes it |

The body is text, not a value, because its spelling is what an HTTP client
sees: jsonb's text output with spaces, or a string result unquoted under a
content type. A base64 transfer encoding can decode to any bytes, so a body
that is not UTF-8 is kept losslessly as base64.

`testdata/observation/dimensions.json` holds the cases that
`TestObservationDimensionCases` runs: observations that differ only in one
of the three extra fields must compare unequal; spellings that must compare
equal; explicit values that must survive a round trip; and observations
that must be refused.

## Site paths

A site path is a sequence of typed segments joined by `/`, each written
`<kind>:<value>`:

| Kind | Value | Built by |
|---|---|---|
| `m` | A module id. When the id occurs more than once in its module list, every occurrence, the first included, gets `~<k>`, counted from 0 | `Module`, `RepeatedModule`, `ModuleSegments` |
| `i` | A loop iteration index | `Iteration` |
| `b` | A branch index, or `default` | `Branch`, `DefaultBranch` |
| `s` | `failure` or `preprocessor` | `FailureSlot`, `PreprocessorSlot` |
| `r` | An attempt of a retried module, from 0 | `Attempt` |

Module ids are percent-escaped for `%`, `/`, `:` and `~` (`%25`, `%2F`,
`%3A`, `%7E`). Examples: `m:outer/i:1/m:inner/i:0/m:s`, `m:a/r:0`,
`s:failure` beside `m:failure`, and `m:a%2Fb` beside `m:a%252Fb`. The typed,
escaped encoding is how windmill-lite names a site so that no two collide
([Q-OR-17](../../docs/semantics/oracle.md#q-or-17)).

- **Canonical text.** A `SitePath` holds its canonical text, so `==` compares
  paths and a path can key a map. `ParseSitePath` accepts exactly the texts
  that the encoder writes, and refuses every other spelling, such as
  lower-case escapes or leading zeros.
- **Placeholders.** `Placeholder` writes `<job:PATH>`, and spells a `>`
  inside the path as `%3E`. So a placeholder ends at its first `>`, and two
  sites never share one, even when a module id contains `>`.
- **The root and building.** The zero `SitePath` is the root, whose text is
  `""` (`IsRoot`). `Child` appends a segment, and panics on a zero
  `Segment`; `Segments` and `String` read a path back, and a segment's
  `Kind`, `Value`, `Occurrence` and `Index` read it. `RepeatedModule`,
  `Iteration`, `Branch` and `Attempt` panic on a negative number.
- **JSON keys.** `SitePath` implements `encoding.TextMarshaler`, so it can
  key JSON objects. It refuses text that is not valid UTF-8, because JSON
  would replace the invalid bytes, and two paths could then collide.

## The encoder's negative tests

The encoding is windmill-lite's own choice, so its cases carry provenance
`policy`. They are data files under `testdata/sitepath/`, and each cites its
findings:

| File | What it checks |
|---|---|
| `encode.json` | Typed segments to canonical text and placeholder, and back |
| `reject.json` | Texts the parser must refuse, the untyped forms `failure`, `a[0]` and `a#0` among them |
| `flows.json` | Synthetic runs whose sites must keep distinct paths, distinct placeholders and distinct observation records |

`flows.json` covers the negative tests that the differential testing ADR
lists:

- two modules with one id in one list;
- the same id in different container bodies;
- an ordinary `failure` module beside a real failure handler, and an
  ordinary `preprocessor` module beside a real preprocessor;
- ids that contain `%`, `/`, `:` and `~`;
- the three colliding shapes of the oracle's walker
  ([OR-V8](../../docs/semantics/oracle.md#or-v8)): a step `failure` beside
  the failure module, a step `preprocessor` beside the preprocessor, and a
  literal `/` in a module id beside the literal text of its escape.

Five cases name a swap pair: the three colliding shapes, the handler that is also
named `failure`, and the nested case. For each pair, swapping the two sites'
results, or which of their jobs a result references, changes the
observation.

Three fuzz targets back the data files, and `go test` runs their seed
corpora:

- `FuzzParseSitePath`: every accepted text encodes again to itself;
- `FuzzModuleSegmentInjective`: distinct module segments never share a text
  or a placeholder;
- `FuzzSitePathInjective`: random paths of every kind parse back to their
  segments, and two paths share a text or a placeholder only when their
  segments are equal.

## Fixtures

A flow [fixture](../../docs/glossary.md#fixture) is a directory
([OR-15](../../docs/semantics/oracle.md#or-15)). `LoadFixture` reads one, and
`FindFixtures` finds the fixtures below its roots.

| Entry | What it holds |
|---|---|
| `fixture.yaml` | The fixture's fields (below) |
| `windmill/` | The sync tree that the oracle deploys, pushed by its CLI unless `oracle.deploy` is `api`, with the metadata and locks that `generate-metadata` writes |
| `setup.yaml` | Optional: the resource types, variables and resources created on the oracle (below) |
| `runs/<name>.yaml` | One run (below) |
| `recorded/<name>.json` | `{"observation": …, "provenance": …}`, written by `wltest record` |

### fixture.yaml

| Field | Type and requirement | What it holds |
|---|---|---|
| `id` | string, required | The fixture's id |
| `spec` | list of strings, required | The findings and catalogue cases that it encodes |
| `scope` | string, required | `core`, `later`, `out` or `EE` |
| `provenance` | string, required | One of `source-derived`, `oracle-observed`, `disputed` or `policy` |
| `languages` | list of strings, optional; omitted or `null` is accepted, and a value of another JSON type, such as the string `bun`, fails decoding | The script languages it uses, for its reader: no runner uses the list |
| `oracle` | object, optional | What the oracle needs (below) |
| `compare` | object, optional | How the runs compare (below) |
| `stub` | boolean; default `false` | `true` for stub mode ([Stub mode](#stub-mode)); such a fixture deploys through the CLI only |
| `roots` | list of strings, optional | windmill-lite only: the bundle's other roots, the scripts and flows that the steps dispatch, as a [host contract](../../docs/glossary.md#host-contract) lists them. `wltest check` compiles them as the default dispatcher's program; the oracle runs the whole workspace and ignores them |
| `provided` | list of provided-site entries, optional | windmill-lite only: the mandatory [provided sites](../../docs/glossary.md#provided-site) of the bundle's host contract, each written as the contract writes them (a selector, a `static` value or a `provider` name, and `mandatory: true`). The oracle would run the real targets, so it skips every run of such a fixture and records the reason ([internal/oracle](../oracle/README.md#4-per-fixture)); such a fixture holds expectations only |
| `providers` | object of name to provider, optional | The named providers that `wltest check` registers for `provided`, each with exactly one of `result`, `error` (an object) and `decline: true` (`FixtureProvider`) |
| `note` | string, optional | Why the fixture reads as it does |

`oracle`:

| Field | Type and requirement | What it holds |
|---|---|---|
| `workers` | integer; 0 or absent means 1 | How many workers the fixture needs: 0, 1 or 2 |
| `effects`, `concurrency` | booleans; default `false` | Whether its runs post effects; whether the fixture asserts something about concurrency, which needs as many workers as it says |
| `deploy` | string; default `cli` | `cli`, the default, or `api`: the oracle harness creates the tree through the REST API as written ([internal/oracle](../oracle/README.md#api-deployment)) |
| `setup` | string; default `after_push` | When `setup.yaml` is applied: `after_push` (the default: once, after the push); `before_push` (once, before the push, which then removes what it seeded; CLI deployment only, [OR-31](../../docs/semantics/oracle.md#or-31)); or `each_run` (after the push, and its variables again before every run) |
| `pull` | boolean; default `false` | With `deploy: api`: read the deployed tree back with the CLI's `sync pull` into the runs' deployment evidence |
| `session` | string; default `""` | The oracle configuration that the fixture needs, such as `native`; empty for the reference configuration ([internal/oracle](../oracle/README.md#oracle-configurations)) |

`compare`:

| Field | Type and requirement | What it holds |
|---|---|---|
| `mode` | string; default `exact` | `exact`, the default, or `subset` |
| `key_order` | string; default `loose` | `strict` compares member order inside values |
| `stack` | list of JSON Pointers, optional | JSON Pointers whose string becomes `<stack>`, on both sides |
| `mask` | list of JSON Pointers, optional | JSON Pointers whose value becomes `<masked>`, on both sides, for values that depend on the environment ([OR-29](../../docs/semantics/oracle.md#or-29)) |
| `unordered` | list of JSON Pointers, optional | JSON Pointers to arrays that compare as multisets |

The pointers are RFC 6901 JSON Pointers into the observation, such as
`/steps/m:a0/args/fi_keys`; a `/` inside a site path is `~1`. `stack` and
`mask` do not reach inside a run-and-wait body, which is text; a pointer can
mask the whole `/response/body`.

### setup.yaml

| Field | Type and requirement | What it holds |
|---|---|---|
| `resource_types` | list, optional; each `name` required | Each `name`, with an optional `schema` and `description` |
| `variables` | list, optional; each `path` required, `value` a string (absent means `""`) | Each `path`, `value`, and optional `is_secret` and `description` |
| `resources` | list, optional; each `path`, `resource_type` and `value` required | Each `path`, `resource_type`, `value` and optional `description` |

### runs/<name>.yaml

| Field | Type and requirement | What it holds |
|---|---|---|
| `target` | string, required | The flow's or script's path |
| `kind` | string; default `flow` | `flow`, the default, or `script` |
| `args` | object; default `{}` | The run's arguments, an object |
| `cancel_after_ms` | integer of 0 or more, optional | Cancel the run after this many milliseconds |
| `group` | string, optional | Runs with the same group are submitted together, as concurrent outer runs |
| `submit` | string, optional | Empty, or `run_wait_result`: submit through the run-and-wait endpoint |
| `replay` | object of site path to outcomes, optional | Stub mode only: the stubs' outcomes ([The replay table](#the-replay-table)) |
| `spec` | list of strings, optional | The findings and cases that this run records |
| `expect` | observation object, optional | The expected observation |
| `claims`, `delays`, `unit` | lists, optional | Partial assertions ([Claims and unit cases](#claims-and-unit-cases)) |
| `not_found_locs`, `copies` | lists, optional | Hints for the comparison with windmill-lite ([Location suffixes](#location-suffixes)) |
| `note` | string, optional | Why the run reads as it does |

### Loading rules

- **Strict decoding.** Every file decodes strictly: unknown keys, YAML
  aliases and merge keys, repeated keys, a mapping key that is not a scalar
  (such as `? [a]`), and numbers that are not JSON
  numbers (`0x1F`, `1_000`, `.inf`) are refused. So a value is always the
  JSON its author wrote. A recording's envelope refuses unknown members, but
  an observation, in a recording or in a run's `expect`, ignores members it
  does not know.
- **`compare`.** A fixture cannot combine `subset` with `key_order: strict`,
  because a subset has no member order to compare.
- **`oracle.setup`.** `before_push` and `each_run` need variables in
  `setup.yaml`. `each_run` refuses runs in a `group`, which would share the
  variables between resets.
- **Run and wait.** A run with `submit: run_wait_result` runs a flow alone,
  so it takes no `kind: script`, `group`, `cancel_after_ms` or `replay`. The
  oracle then polls and walks it like any other run
  ([internal/oracle](../oracle/README.md#5-per-run)), where a group's runs
  must also be in flight together.
- **Stub mode.** `stub: true` with `oracle: {deploy: api}` is refused, and
  `replay` needs `stub: true`.
- **Hints.** Loading checks that each pointer and site path of
  `not_found_locs` and `copies` parses, and that a copy's `of` is not the
  root `""`.
- **Groups.** `LoadFixture` sorts the runs by name. `RunBatches` keeps the
  order it is given, and makes each group one batch at its first run's
  place, and every other run a batch alone.
- **Required and checked fields.** The loader refuses a fixture with no `id`
  or `spec`, a `scope` other than `core`, `later`, `out` and `EE`, and
  another provenance. It refuses `oracle.workers` other than 0, 1 or 2; an
  unknown `deploy`, `setup`, `compare.mode` or `key_order`; `before_push`
  with `deploy: api`; and `pull` without `deploy: api`.
- **Providers.** A provider must give exactly one of `result`, `error` (an
  object) and `decline`, and `providers` needs `provided`. A `stack`,
  `mask` or `unordered` pointer must parse.
- **Files.** A fixture on disk needs its `windmill/` sync tree; a fixture that
  the expression lifter returns carries a generated `Tree` instead. A fixture
  needs at least one `runs/*.yaml`, and a `recorded/<name>.json` with unknown
  members in its envelope is refused. In `setup.yaml`, a resource type needs a `name`, a variable a
  `path`, and a resource a `path`, `resource_type` and `value`.
- **Runs.** A run needs a `target`; `kind` is `flow` or `script`; `args` is an
  object; `cancel_after_ms` is not negative; `submit` is known; its claims,
  delays and unit cases pass their own checks
  ([Claims and unit cases](#claims-and-unit-cases)).
- **Generated trees.** A fixture whose tree is generated, such as one lifted
  from an expression file, sets `Tree`, `RecordedIn` and `Generated` instead
  of using `windmill/` and `recorded/` under its directory.

## Expression cases

An expression case is context-free: an input transform or the `skip_if`
predicate of a flow's first step, whose only bound values are `flow_input`
and `flow_env`. `LoadExprFile` reads one `conformance/exprs/<finding>.yaml`
strictly. `FindExprFiles` finds the files below its roots once each,
skipping flow fixtures, and `IsExprFile` tells one by its path.

The file's fields:

| Field | Type and requirement | What it holds |
|---|---|---|
| `finding` | string, required | The finding, which names the file in lower case (`je-7.yaml`) |
| `scope` | string, required | The finding's scope |
| `flow_input`, `flow_env` | string of a JSON object, optional | The defaults of the cases, as JSON object texts |
| `slow` | boolean; default `false` | `true` has the recorder record the file last |
| `mask` | list of JSON Pointers, optional | JSON Pointers into every case's observation whose values become `<masked>`, for text with times or host names |
| `note` | string, optional | Why the file reads as it does |
| `cases` | list, required | The cases |

A case's fields:

| Field | Type and requirement | What it holds |
|---|---|---|
| `spec`, `variant` | strings; `spec` required | The catalogue case, or the finding with a `variant` for a case beyond the catalogue |
| `title` | string, required | What the case shows (required) |
| `note` | string, optional | Why it reads as it does |
| `provenance`, `scope` | strings; `provenance` required | The catalogue's tags; a recorded case is `oracle-observed` |
| `site` | string; default `transform` | `transform` (the default: a `javascript` input transform `x`), `static` (a `static` transform) or `skip_if` |
| `expr`, `static` | strings; one, by site | The expression text, or the static value's JSON text |
| `flow_input`, `flow_env` | string of a JSON object, optional | The run's arguments and the flow's `flow_env`, as JSON object texts |
| `expect.value` | string of JSON | Argument `x` as the job API records it, as JSON text: jsonb, before `$var:` resolution ([the notation](../../docs/semantics/conformance.md#je-notation)) |
| `expect.key_order` | string; default `loose` | `strict` compares the value's member order |
| `expect.text` | string of JSON | The evaluator's exact output, which the oracle cannot observe; its stored form must be `expect.value` |
| `expect.error` | error match object | The step's error object, when the expression fails the step: `name`, and `message` exactly, or `prefix` with `contains` and `not_contains` |
| `expect.flow_error` | error match object | The run's error object, when a predicate fails the flow at its first pickup ([JE-35](../../docs/semantics/expressions.md#je-35)) |
| `expect.skipped` | boolean | Whether `skip_if` skipped the step |
| `expect.status` | string, optional | The run's status, where it is not the one the expectation implies (`success` for a value or a skip, `failure` for an error), such as `canceled` |

The loader refuses:

- a case that names a predecessor, a handler or a container: a key such as
  `previous_result`, `results` or `failure_module`, or a site other than
  `transform`, `static` and `skip_if` (`contextual_case_needs_flow_fixture`,
  an `ExprCaseError`). Such a case is a flow fixture;
- a file that is not named after its finding;
- a file with no `finding`, no `cases`, or a bad `scope`; `flow_input` or
  `flow_env` that is not a JSON object text; a `mask` pointer that does not
  parse; unknown keys;
- a case given twice, or without `spec` or `title`; a `variant` that is not
  lower-case words joined by hyphens; a finding cited without a variant; a
  bad `provenance` or `scope`;
- a `transform` or `skip_if` case without `expr` or with `static`, and a
  `static` case without a JSON `static` or with `expr`;
- an `expect` without exactly one of `value`, `error`, `flow_error` and
  `skipped`, a `value` or `text` that is not JSON, and a `text` without a
  `value`;
- a `key_order` other than `loose` and `strict`; a `status` other than
  `success`, `failure`, `canceled` and `skipped`; an empty error match, or a
  `message` with `prefix` or `contains`;
- an expectation that its site cannot produce. A `skip_if` case expects
  `skipped` or `flow_error`; a transform or static case expects `value` or
  `error`, or `flow_error` with `status: canceled` when the oracle cancels
  the run before the step exists.

At module 0, `previous_result` is the flow input
([JE-36](../../docs/semantics/expressions.md#je-36)) and the previous id is
`""` ([JE-8](../../docs/semantics/expressions.md#je-8)), so a case may use
them too.

### Lifting

`LiftExprFile` writes a file's generated sync tree into a new directory and
returns its `Fixture`:

- `wmill.yaml`, and the sink script `f/wl_expr/sink.ts`, which returns
  `null`, so the observation shows what the expression produced, not what
  Bun made of it;
- per case, a flow `f/wl_expr/<run name, hyphens as underscores>` (such as
  `f/wl_expr/je_7_a`) at `<path>.flow/flow.yaml`, written as JSON, which the CLI and
  `internal/yamlx` read alike. Its one module, `b`, runs the sink with the
  expression as its input `x`, or as its `skip_if` with `x` static `null`;
- per case, one run, named by its spec and variant (`je-7-a`,
  `je-1-math-trig`), with the case's flow input as its arguments. Its claims
  are what the case's `expect` says about module `b`'s recorded argument or
  error, the run's `/status`, `b`'s skipped flag and status, and, for a
  `flow_error`, the run's `/result/error`.

Recordings are read from and written to
`conformance/exprs/recorded/<finding>/`. The generated tree is never
committed.

## Step cases

A step case runs one script and its runs on windmill-lite's Bun harness,
through `wltest check`. `LoadStepFile` reads one
`conformance/steps/<lang>/<name>.yaml` strictly. `FindStepFiles` finds the
files below its roots, `deferred.yaml` aside, and `IsStepFile` tells one by
its path.

A run's observation is the step's stored value, `{"status": "success" |
"failure", "result": …}`: the result, or the failure value
([WR-21](../../docs/semantics/languages.md#wr-21)).

The file's fields:

| Field | Type and requirement | What it holds |
|---|---|---|
| `finding` | string, required | The finding, or a named deviation that has none, which names the file in lower case |
| `scope`, `note` | strings; `scope` required | The scope, and why the file reads as it does |
| `shared_code` | boolean; default `false` | `true` builds the file's bundle with shared code (the ADR [Shared module code](../../docs/design/0013-shared-module-code.md)) |
| `cases` | list, required | The cases |

A case's fields:

| Field | Type and requirement | What it holds |
|---|---|---|
| `spec`, `variant`, `provenance`, `scope`, `title`, `note` | strings; `spec`, `provenance` and `title` required | As for expression cases. A case of a finding beyond the catalogue, or of a deviation, cites the finding and needs a `variant` |
| `script`, `path` | strings; `script` required | The script, and its Windmill path (default `f/wl_step/<name in snake case>`), which is its import base |
| `language` | string; default `bun` | `bun` (the default), `bunnative` or `nativets`. A deploy relabels `bun` and `bunnative` from the script's `//native` header, and `bunnative` and `nativets` run with the native boundary ([WR-40](../../docs/semantics/languages.md#wr-40)) |
| `timeout` | positive integer (whole seconds), optional | The step's static timeout in seconds. When it passes, the run is abandoned, and its observation is the engine's timeout error for the step's boundary ([WR-37](../../docs/semantics/languages.md#wr-37), [WR-44](../../docs/semantics/languages.md#wr-44)) |
| `step_id` | string; default `a` | The module id it runs as, which `WM_FLOW_STEP_ID` carries (default `a`). `""` runs it as a script job outside any flow, whose errors carry no `step_id` and no `flow_path` |
| `env` | object of name to string, optional | Job variables that add to or replace those of the job identity ([internal/platform](../platform/README.md#job-identity)) |
| `host_modules` | list of `{path, script}` records, optional | Scripts registered as [host-provided modules](../../docs/glossary.md#host-provided-module): `path` and `script`, keyed by the file path `<path>.ts` |
| `codebase` | object, optional | Makes the script a codebase script, bundled by esbuild: the settings of its `codebases` entry as `wmill.yaml` gives them (`format`, `define`, `inject`, `loader`, `banner`, `external`, `assets`) |
| `files` | object of path to text, optional | Other files of the folder, by path from the sync root, that a codebase build reads. For a Bun script, the workspace scripts it imports, by their `.ts` paths (`f/<folder>/<name>.ts`, [WR-28](../../docs/semantics/languages.md#wr-28)), without `codebase` |
| `scripts` | object of Windmill path to source, optional | More entry scripts of the case, by Windmill path, built as the case's own script is |
| `setup` | object in `setup.yaml`'s format, optional | The variables and resources that the case's platform facade serves, in `setup.yaml`'s format; a secret's `value` is its plaintext |
| `compare` | object, optional | `stack` and `mask` pointers, `unordered` arrays, `strict_key_order`, and `keep_stack` (below) |
| `deviation` | string, optional | The [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations) whose two readings the case's `by_mode` gives, or `5a` for the [restriction](../../docs/design/0001-equivalence-contract.md#5a-restrictions) that a warm process reaches the platform only through `fetch` |
| `runs` | list, required | The runs, in order |

A run's fields:

| Field | Type and requirement | What it holds |
|---|---|---|
| `args` | string of a JSON object, required | The argument object's JSON text, as the script receives it. `_ENTRYPOINT_OVERRIDE` in it selects the export ([WR-12](../../docs/semantics/languages.md#wr-12)) |
| `script` | string, optional | The path of one of the case's `scripts` that the run invokes instead of the case's own script: in the case's one process in `warm` mode, in a process of the run's own in `fresh` mode |
| `expect`, `claims` | observation object; list of claims; each optional, but a run without `by_mode` needs at least one non-empty check field (`expect`, `claims`, `late`, `settled`, `warnings`, `left`, `untracked` or `calls`, `[]` included) | The whole observation, and partial assertions such as an error's `name` or a message's prefix |
| `late` | list of `{kind, final, body}`, optional | What the invocation reports after its outcome, up to the end of the case: `kind`, `final`, `body` |
| `settled` | string, optional | How the invocation settles: `completed`, `not_started` or `process_exited`, or `pending` for a call that must still run at the end of a 500 ms window. Without `pending`, every run must settle within 10 s, whether or not `settled` names a kind |
| `warnings` | list of strings; absent means none | The codes of every warning that the invocation gets up to its settlement: `unattributed_error`, `left_behind`, `native_untracked`; none when absent |
| `left` | object of kind to count, optional | The counts of the `left_behind` warning by kind (`timeout`, `interval`, `immediate`, `fetch`, `child`) |
| `untracked` | object of kind to count, optional | The counts of the `native_untracked` warning by kind (a class name, or `function <name>`) |
| `calls` | list of strings, optional | The platform calls that the facade recorded against the invocation, in order, each `<status> <METHOD> <route template>`, such as `501 POST /w/{ws}/jobs/run/p/{path}`, or `<status> <METHOD> <path>` with `/api` removed where no route matched; `[]` for none. A call that the facade could not attribute to the invocation, such as one with an unknown token or one the kernel refused, is never listed |
| `by_mode` | object with `warm` and `fresh` checks, optional | `warm` and `fresh`, each a check (`expect`, `claims`, `late`, `settled`, `warnings`, `left`, `untracked`, `calls`) instead of the run's own; `args` and `script` stay the run's |
| `node` | check object, optional | For a CommonJS codebase case: what the same bundle shows on Node (`expect`, `claims`), where it differs from the run's own check |
| `note` | string, optional | Why the run reads as it does |

`late` is compared when a non-empty `late` or a `settled` is given; an empty
`late: []` without `settled` compares nothing. `settled`, `left`,
`untracked` and `calls` are compared when given; `warnings` is always
compared, and absent means none. They are the runner's to check, because only it sees the invocation's
reporter and the facade's records.

### What LoadStepFile refuses

- an unknown key, and a file that is not named after its finding;
- a case whose spec cites a finding without a variant, and a run that checks
  nothing;
- a `by_mode` without both `warm` and `fresh`, or without the case's
  `deviation`;
- a `left` without `left_behind` among its `warnings`, or with a count below
  1, and the same for `untracked` and `native_untracked`;
- a `codebase` that is not a mapping, and `files` with a path that leaves
  the sync root;
- a `node` check on a case that is not a CommonJS codebase script, or one
  that gives `late`, `settled` or `warnings`, or neither `expect` nor
  `claims`;
- a `language` other than `bun`, `bunnative` and `nativets`, and a `timeout`
  that is not a positive integer;
- a `scripts` path that is not a Windmill path, and a run's `script` that is
  not one of the case's `scripts`;
- two cases of one file that give one path two sources: a file's scripts and
  files share one bundle;
- a `by_mode` with a third key, beside the run's own check, or with a mode
  that checks nothing, and a `deviation` with no run that gives `by_mode`;
- a `files` path that is empty, starts with `/`, holds `..` or a backslash,
  and, without `codebase`, one that is not `f/<folder>/<name>.ts`;
- a case's `path`, a `scripts` path or a `host_modules` path that is not a
  Windmill path in a folder: `f`, then two or more segments of lower-case
  letters, digits and `_`, such as `f/team/tasks/read`;
- a language directory other than `bun`; a file with no `finding` or no
  `cases`; a bad scope, variant or provenance;
- a case given twice, or without `spec`, `title` or `script`; a `compare`
  pointer that does not parse; a case with no runs;
- `args` that is not a JSON object text; an `expect` status other than
  `success` and `failure`, or an `expect` with `steps` or `effects`;
- a `late` kind other than `result`, `error` and `log`, or a `late` body that
  is not JSON; an unknown `settled` value; and claims that fail their checks.

Only `bun` has a language directory: native steps run in the Bun harness, in
a realm of their own each time.

### How a step run is compared

`CompareStep` compares one run's observation with the run's check for a
mode:

1. It masks the case's own `stack` and `mask` pointers on both sides.
2. It masks `/result/error/stack` (`DefaultStepStack`) on a side whose status
   is `failure`, because a successful result is data, unless the case sets
   `keep_stack`, so that claims can read the stack's text.
3. It compares with `Compare`, the case's `unordered` pointers and its
   `strict_key_order`.
4. It checks the claims.

`CompareStepCheck` does the same for any check.

A codebase case built as CommonJS (its entry's `format` is not `esm`,
[WR-47](../../docs/semantics/languages.md#wr-47)) also runs on Node, where
Windmill runs it. That needs a Node for `wltest check` (`--node` or
`WL_NODE`), and a script that runs with the Bun boundary, not a native one. windmill-lite runs
such a bundle on Bun's CommonJS loader, a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations). `CompareStepNode` compares what Node showed with the run's
`node` check, or with its own `expect` and `claims` when the two runtimes
agree.

[`steps/bun/deferred.yaml`](../../conformance/steps/bun/deferred.yaml)
(`LoadStepDeferred`) lists the catalogue cases of the step files' findings
that are not step cases. The file's top-level keys are `note` and
`deferred`, the list. Each entry needs `spec`, the catalogue case; `reason`,
why it is not a step case; and `expected_in`, where it is checked instead,
such as another level's check or planned work.

## Host-plugin cases

`LoadPluginFile` reads `conformance/plugins/hooks.yaml`: host-plugin cases,
each one synthetic flow and a declarative
[host plugin](../../docs/glossary.md#host-plugin) that answers its
[hooks](../../docs/glossary.md#hook). These are windmill-lite's rules for
its hosts, not Windmill's, so the oracle never records them.

| Field | Type and requirement | What it holds |
|---|---|---|
| `timeout_ms` | integer; the loader does not require it, and the runners use it as given, so the file sets it | At the top of the file: the plugin timeout of the runtimes that run the cases |
| `scripts` | object of path to source | At the top of the file: the folder's scripts, by path. With each case's `flow`, at `f/plugins/<id>`, they make one sync folder, whose host contract has every flow as a root and the cases' `provided` sites |
| `cases` | list, required | At the top of the file: the cases, at least one |
| `id`, `spec`, `title` | string, list of strings, string; `id` required and unique | The case's id, findings and title |
| `flow` | object, required | The flow's value |
| `plugin` | object, required; `{}` for a case with no hooks | The case's declarative plugin: `intercept`, `rewrite_args`, `halt_before` and `halt_after`, `write_capable`, `first_write`, `fatal_error_names`, `around`, `observe`, `env`, `variables`, `fail_variables`, `fail_resources`, `slow_start_ms`, `providers`, `warnings`. The header of [`hooks.yaml`](../../conformance/plugins/hooks.yaml) says what each does |
| `provided` | list of `{module, provider}`, optional | The case's provided sites: each `module` and the `provider` that answers it |
| `run` | object, optional | The run's own hooks and budgets, which a Go host gives in `RunOptions` and a TypeScript host in its run frame: `write_capable`, `halt_before`, `halt_after`, `fatal_error_names`, `deadline_ms`, `pre_write_deadline_ms` (0 for a deadline that has already passed), `clock` |
| `expect` | object, required; `status` required | The run's `status`, `may_have_mutated`, `result`, `latched` (a text of the latched error), the plugin's `log`, its `warnings`, the sites that ran no job (`not_ran`), the run's `deviations` and, for a canceled run, its `cancel_cause` |
| `then` | list of `{run, expect}`, optional | More runs of the case's flow, each with its own `run` and `expect`, after its run on the same runtime and, in host mode, on the same connection |
| `go` | boolean; default `true` | `false` runs the case only through the TypeScript plugin, as a failing or slow plugin needs |

The cases run through a TypeScript plugin
(`harnesses/bun/test/caseplugin.ts`) and through Go callbacks
([conformance](../../conformance/README.md#plugin-cases) lists where). Every plugin's `onRunEnd` reports
its log of the run in the warning `plugin_log`. The TypeScript plugin's
also reports, in the warning `plugin_trace`, the sites whose job ran and
the run's cause of cancellation, as the trace that it got shows them.

The TypeScript plugin's `onRunSettled` also writes each run to a report
file, with whether one of the run's hooks still ran, which `ReadSettlement`
reads. Its two runners, in `warm` processes and in host mode, check that
every run settles, and none early.

`PluginCase.Check` compares a run's `PluginOutcome` with its `expect`, field
by field:

- `status`, `may_have_mutated` and `result` must equal the outcome's, when
  given;
- `latched` must be a substring of the latched error's text; an empty
  `latched` requires that nothing latched;
- `warnings` lists codes that must be among the run's; others may come too.
  This is looser than a step run's `warnings`, which must be exact;
- `log` and `deviations`, when given, must equal the outcome's, in order;
- each `not_ran` site must have run no job: by the run's trace, or, for a
  runner without it in host mode, by the plugin's `plugin_trace`. Such a
  runner's outcome needs that report, with its array of sites, an empty
  one included: without it the check fails;
- `cancel_cause`, when given, must equal the outcome's, and a run that is
  not `canceled` must have none;
- the cause in `plugin_trace`, when the plugin sent one, must equal the
  outcome's, so the plugin's `onRunEnd` saw it.

The loader decodes the file strictly, and needs at least one case:

- a case's `id` must be unique, and hold no `/` or space;
- every run and `then` entry needs `expect.status`;
- `clock` is `real` or `record`, and deadlines are not negative;
- a plugin with `fail_variables`, `fail_resources` or `slow_start_ms` needs
  `go: false`.

A case's `then` runs show that a run's own hooks reach that run alone: a run
with its own halt ends `halted`, and the next run of the flow, without one,
runs every module.

## Claims and unit cases

Runs of flow fixtures, and the runs lifted from expression cases, can carry
partial assertions beyond `expect`, and two hints for the comparison with
windmill-lite. `go test ./internal/oracle` checks the assertions against the
committed recordings, and `wltest check` checks the claims on windmill-lite's
own observation too. So a claim states only what both sides write: a
location suffix that windmill-lite never writes stays in the recording.

| Field | Type and requirement | What it holds |
|---|---|---|
| `claims` | list of objects, optional | Each a JSON Pointer `at` into the observation's canonical encoding, and one assertion. A step field left to its canonical absence is absent there. The assertion is `equals` (a JSON value, with `key_order: strict` if member order counts), `prefix`, `contains`, `not_contains`, `length` (of an array) or `absent` |
| `delays` | list of `{from, to, at_least_ms}`, optional | Each `from` and `to` sites and `at_least_ms`: a lower bound on the time from one job's completion to the next one's start, read from the recording's provenance, because timings are never compared ([JE-28](../../docs/semantics/expressions.md#je-28)) |
| `unit` | list of unit cases, optional | The run's expressions restated at unit level (below) |
| `not_found_locs` | list of JSON Pointers, optional | JSON Pointers to the values whose error message quotes the lookup helper's not-found text with a location that Windmill generated, where the expected text writes `<loc>` for it ([Location suffixes](#location-suffixes)) |
| `copies` | list of `{at, of}`, optional | The values that the case's flow copies, through user code, from an error that windmill-lite generates at another site: each `at` (a JSON Pointer) and `of` (that site's path). The value at `at` counts as generated when it equals that site's error, its error member or its message |

A claim may carry a `note`. The loader refuses a claim whose `at` is not a
pointer, and one that does not hold exactly one of `equals`, a string match
(`prefix`, `contains`, `not_contains`), `length` and `absent`. It also
refuses a `key_order` other than `loose` and `strict`, `strict` without
`equals`, and an `equals` that is not JSON. A delay's `from` and `to` must parse as site
paths, and its `at_least_ms` must be positive.

`CheckClaims` returns one line per claim that does not hold. `ValueAt` reads
one value of an observation, and `SameJSON` compares two JSON texts as the
comparator does. The recorder does not check claims, so a contradicted claim
still leaves its recording to inspect.

### Unit cases

A `UnitCase` restates an expression at unit level, with the context that its
evaluation sees. `internal/expr`'s tests run it, and
`go test ./internal/oracle` ties it to its flow and recording.

| Field | Type and requirement | What it holds |
|---|---|---|
| `spec` | string, required | The catalogue case, or the finding for a case beyond the catalogue |
| `site`, `input` | strings, required | The module's site path, such as `m:b`, and the expression there: an input transform's name, or the module field `skip_if`, `stop_after_if`, `timeout` or `sleep`. The type also allows `iterator`, but the oracle's test cannot tie a loop's iterator to a recording |
| `expr` | string, required | The expression, which must be the one at that site |
| `context.previous_id` | string, required | The id of the module before it |
| `context.previous_result` | string of JSON, optional | `previous_result` as the worker holds it: after an ordinary step the producer's text; at a top-level flow's first module the submitted flow input. Other cases follow [JE-44](../../docs/semantics/expressions.md#je-44)'s table. At the first module of a body, the oracle's test refuses it, because a recording does not hold the body's input |
| `context.flow_input` | string of JSON, optional | The flow input as submitted. Inside a body, the oracle's test refuses it, for the same reason |
| `context.results` | object of site to stored text, optional | The stored texts that a database read returns |
| `expect.text`, `expect.error` | string of JSON, or an error match; exactly one | The expected text, or an error (`MessageMatch`: `name`, and `message` exactly, or `prefix` with `contains` and `not_contains`, as the catalogue compares engine messages) |
| `note` | string, optional | Why the case reads as it does |

The loader requires `spec`, `site`, `input`, a non-empty `expr` and
`context.previous_id`; a parsing `site`; JSON text in `previous_result`,
`flow_input`, `results` and `expect.text`; and exactly one of `expect.text`
and `expect.error`. An error match needs a `name`, `message`, `prefix` or
substring, and cannot combine `message` with `prefix` or `contains`.

The oracle's test checks that `expr` is the flow's expression at the site,
and that `previous_id` is the module before it; it refuses slot sites. It
also checks these things:

- the stored forms of `flow_input`, and of `previous_result` at a top-level
  flow's first module, are the run's arguments;
- the stored forms of `previous_result` after another module, and of the
  texts in `results`, are the recorded results;
- at an input transform, the stored form of the expected text is the
  recorded argument, and an expected error matches the recorded error;
- at a `timeout` site, the expected text is exactly the value that the
  recorded i32 parse error quotes, spacing and key order included
  ([JE-29](../../docs/semantics/expressions.md#je-29)).

## Stub mode

A fixture with `stub: true` runs with every script body replaced by a stub
that keeps `main`'s signature, and asks a run-scoped replay service for its
outcome. The stub generator and the oracle's side are in
[internal/oracle](../oracle/README.md#stub-mode). This package holds what
the oracle and windmill-lite share.

### The replay table

A run's `replay` gives, for each site, the outcomes of its attempts, from
attempt 0:

```yaml
replay:
  "m:loop/i:0/m:call": {result: {item: apple}}
  "m:loop/i:1/m:call":
    - {error: {message: busy}}
    - {error: {name: Busy, message: busy, code: 7}}
    - {result: {item: pear}}
```

- **A site** is canonical site-path text that ends in a module or slot
  segment, or the root `""` for a script run. An attempt is a list index,
  never an `r:` segment.
- **An outcome** is exactly one of `{result: …}` and `{error: {message,
  name?, …}}`. The stub returns the result in the table's member order, or
  throws an `Error` whose other members become own properties (Windmill's
  `extra`). An error cannot set `stack` or `step_id`, which Windmill writes.
- **A lone outcome** stands for a list of one.

| Part | Type and requirement | What it holds |
|---|---|---|
| A key | string: a site path | The site, as above |
| A value | an outcome, or a list of outcomes | The outcomes of the site's attempts, from attempt 0 |
| `result` | JSON | The value that the stub returns |
| `error` | object; `message` required | The error that the stub throws: `message`, an optional `name`, and other members as own properties |

- **Refused:** an empty outcome list; an outcome that is not an object; an
  error that is not an object or has no `message`; and a `message` or
  `name` that is a JSON value other than a string or `null`. A `null` is
  accepted, and the stub receives it as JSON `null`, since the service
  answers with the error's JSON as the table wrote it.

`testdata/replay/tables.json` holds the cases.

### The replay service

A stub posts one call as a JSON object:

| Field | Type and requirement | What it holds |
|---|---|---|
| `root` | string, required, not empty | The outer run's job id, which selects the open run |
| `job` | string, required, not empty | The calling job's id |
| `root_flow`, `flow`, `step` | JSON, optional; `null` when the step's environment has no value | The job identities that the step sees ([OR-29](../../docs/semantics/oracle.md#or-29)) |
| `args` | JSON, optional; the stored effect has `null` when it is absent | The arguments that `main` received, in parameter order |

The service adds the resolved `site` and `attempt` when it stores the call's
effect.

`ReplayService` is an HTTP handler for `POST /replay` below the effect
sink's URL. `Open(root, table, locator)` starts answering a run, and
`Close(root)` returns its calls; several runs can be open at once. For each
call it:

1. refuses a body that is not a call, or is larger than 1 MiB, or has an
   empty `root` or `job`; then a root that is no open run;
2. answers a job that it already answered with the same outcome, without
   asking the locator again: it is idempotent per raw job id;
3. asks the run's `Locator` for the job's site, within `LocateTimeout` (60 s
   when zero);
4. counts the attempt as the number of distinct jobs already seen at that
   site in the run, and answers the table's outcome.

| Answer | When |
|---|---|
| `200`, the outcome | The table has an outcome for the site and attempt |
| `409` `replay_foreign_root` | The root is no open run |
| `400` `replay_bad_request` | The body is not a call, is larger than 1 MiB, or has an empty `root` or `job` |
| `504` `replay_unresolved` | The locator failed, or found no site in time |
| `405` | A request that is not a `POST`, with no refusal code |
| `404` `replay_miss` | The table has no outcome for the site and attempt |

A call is stored once per job, as a `ReplayCall`:

- **A located call** has an `Effect`, whether the table has its outcome or
  not, with its members in this order:
  `{"site":"m:loop/i:1/m:call","attempt":2,"step":"call","job":"<id>","flow":"<id>","root_flow":"<id>","root":"<id>","args":{"item":"pear"}}`.
  The runner appends the effects to the run's effects.
- **A call whose job the locator did not find** is stored with its error and
  no effect.
- **A repeated call of a job** counts as a duplicate of the stored call, and
  gets its answer again.
- **A malformed or foreign call** is not stored; `Foreign` counts the calls
  refused for a foreign root.

The locator differs by side. On the oracle, it walks the job tree. On
windmill-lite, `wltest check` records the site that each job launches at,
through the engine's invocation-environment hook
([cmd/wltest](../../cmd/wltest/README.md#flow-fixtures)).

### CheckReplay

`CheckReplay(calls, walked, scripts)` is the check that both sides apply
after the run. `walked` maps every job id that the run's observation reached
to its site, a failed attempt `k` at `<module>/r:<k>`: the oracle's walk, or
windmill-lite's trace. `scripts` are the jobs that executed a script.

- **A refused call** fails the run, at the first one: `replay_miss` or
  `replay_unresolved`, as the stub received it.
- **A call whose site and attempt disagree** with where its job was walked
  fails it with `replay_site_mismatch`. The call's site must be the job's
  walked site without attempt segments. Its attempt must be the job's place
  among the jobs walked at that site that execute a script or called, in
  walk order.
- **Identity jobs take no place:** a skipped or mocked step's job does not
  call, so the replay service does not count it. That puts a failed attempt
  `k` at `<site>/r:<k>`, and the last job at `<site>` with `k` the number of
  failed attempts before it.
- **Below a retried flow step**, each failed attempt's subflow is walked
  below the step's `r:<k>`, and the last attempt's below the step's own
  site. So a subflow step's jobs are ordered by the flow step's attempt
  first, then by their own.
- **A script job that never called** fails the run with `stub_not_called`
  and the sorted sites.

The error's text is the refusal as one JSON object.

## Normalised text

A normalised string ([OR-14](../../docs/semantics/oracle.md#or-14)) is user
text with walked job ids replaced by placeholders `<job:PATH>`, and three
markers: `<stack>`, `<loc>` and `<masked>`. User text that already looks
like one of them would make two different values normalise alike. A plain
`<<job:` escape does not prevent it: a user `<` before a walked id gives the
same text. So windmill-lite escapes this way:

- every maximal run of `<` in user text that a placeholder, a marker, or
  text beginning `job:`, `stack>`, `loc>` or `masked>` follows is written
  twice as long;
- every other `<` is written once, so text without such a run is unchanged.

The placeholder of an unwalked job, `<job:?N>`
([The normaliser](#the-normaliser)), shares the `job:` head, so the same
escape covers it: a user `<job:?1>` reads `<<job:?1>`.

A reader counts the run of `<` before such a head: an odd count ends in a
placeholder or marker, and an even count is literal. `ParseText` is that
reader, and `EncodeText(ParseText(s)) == s` for every text it accepts.
Examples, with module `a`'s job id written `ID`:

| Raw text | Normalised |
|---|---|
| `ID` | `<job:m:a>` |
| `<job:m:a>` | `<<job:m:a>` |
| `<ID` | `<<<job:m:a>` |
| `<stack>` | `<<stack>` |
| `a < b` | `a < b` |

`testdata/text/cases.json` holds the encodings and the pairs that must stay
distinct.

## The normaliser

`Normaliser.Value` rewrites one raw JSON value, and keeps member order,
number text and duplicate elements:

1. **Stacks** ([OR-V3](../../docs/semantics/oracle.md#or-v3)). Given the
   run's runtime errors, the stored results of the failed leaf jobs that ran
   a script, a value deep-equal to one of them has its `error.stack` masked,
   and a value deep-equal to one's inner error has its `stack` masked.
   Nothing else is masked: a handler's own `{"error":{"stack":…}}` keeps its
   data.
2. **Job ids**, in every string and member name, become placeholders, and
   user text is escaped as above. Unknown UUIDs stay, except the unwalked
   job ids below.
3. **The completion failure's `InternalErr`**
   ([OR-27](../../docs/semantics/oracle.md#or-27),
   [FX-51](../../docs/semantics/execution.md#fx-51)). For `name:
   "InternalErr"` with a message that starts `Internal: Could not add
   completed job ` and ends in ` @<file>:<line>:<col>`, the suffix becomes
   ` <loc>`. No other message loses its suffix. Windmill fails a completion
   this way when the job table refuses the result, which on CE `v1.862.0` an
   unpaired surrogate escape does.

`MaskPaths` then applies a fixture's `stack` and `mask` pointers. A pointer
that names nothing is skipped, and so is a `stack` pointer to a value that is
not a string. A mask that leaves the observation invalid, such as one that
masks a step's `status`, returns a decoding error.
`testdata/normalise/cases.json` holds the cases.

### Unwalked job ids

A step's error object names its job as `job_id`, and a step can return the
error of a job that the walk never reaches, such as one of a flow it ran
through the API. Such an id differs on every execution, so it gets a
placeholder of its own, `<job:?N>`
([OR-14](../../docs/semantics/oracle.md#or-14)):

- **Which ids.** A string that is the whole value of a member `job_id`, in an
  object that is the value of a member `error`, that is a UUID in lowercase
  hex (`8-4-4-4-12`), and that is no walked job's id.
- **Numbering.** `NumberUnwalked` reads the raw observation before any value
  is normalised: the effects, the result, then each step by site path, its
  `args`, `carried`, `resolved_args` and `result`, with every object's
  members sorted by name. Names sort with walked ids replaced, so a walked
  id's value cannot change the order. The distinct ids are numbered from 1
  as they first occur.
- **Replacement.** `Value` and `Text` then replace each numbered id anywhere,
  in strings and member names, as they replace walked ids. A response body
  is text and holds no member, so it numbers nothing, but its ids are
  replaced.

The numbers follow where the ids stand, so two executions of a run agree.
Both sides call `NumberUnwalked` with their own observation.
`testdata/normalise/unwalked.json` holds the cases.

### Normalising a response body

`Normaliser.Text` normalises a text kept as written, a run-and-wait body.
Job ids become placeholders. When the text is a JSON value, a stack is masked
exactly where `Value` would mask it: in an object deep-equal to a runtime
error, or to its inner error, only that `stack` string's span becomes
`"<stack>"`. The same string anywhere else, a handler's own data among it,
stays, and so do spacing, member order and every other character.

### Location suffixes

Comparing windmill-lite with the oracle adds one rule. windmill-lite writes
Windmill's messages without the source location that Windmill appends (a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations)).
So where windmill-lite's value is an error that it generated, the oracle's
location suffix is stripped, but only from the kinds of message below. The
code chooses them by the error's name, or a string error, and strips by the
count of layer prefixes.

`StripLocations(o, at)` applies the rule to the oracle's normalised
observation, an expectation or a recording, at each pointer of `at`: a value
that windmill-lite's trace says it generated. It follows
[FX-50](../../docs/semantics/execution.md#fx-50)'s masking rule:

1. **Trailing suffixes, by layer.** A message that is Windmill's error display
   begins with k layer prefixes, one after another: `Error: `, `Internal: `,
   `SqlErr: `, `IoErr: `, `Utf8Err: `, `UuidErr: `, `SerdeJson: ` and
   `HexErr: ` (`Error parsing …` and `Error during …` are not prefixes). It
   loses at most k trailing ` @<file>.rs:<line>:<col>` suffixes from the end,
   or the `<loc>` markers that the expected text writes for them. A message
   with no layer prefix loses none, and a suffix beyond the k-th is the
   message's own.
2. **Parenthesised locations, only where written.** The location in
   parentheses, `(<file>.rs:<line>)`, that ends a not-found message is not
   a layer's suffix. No text tells a generated one from a user's, so it is
   never searched for. It goes only where the expected text writes `<loc>`
   for it, as a case whose step reads a result that the lookup cannot find
   does ([CF-19](../../docs/semantics/control-flow.md#cf-19)).
   - Every ` <loc>` marker that step 1 left in a generated value's message
     is removed, with the space before it, wherever it stands; a `<loc>`
     that no space precedes stays.
   - A recording keeps such a raw location, and the comparison shows it,
     except at a value that the run declares in `not_found_locs`, where
     `StripNotFoundLocations` removes it from the recording or the
     expectation alike.

Step 1 reads these displays:

| Template | Where the display starts |
|---|---|
| An error named `InternalErr`, such as the completion failure, whose location the normaliser masked as `<loc>` | The message |
| A chaining error, named `InternalError` (a later transition) or `Anyhow` (a pickup) ([FX-15](../../docs/semantics/execution.md#fx-15), [FX-50](../../docs/semantics/execution.md#fx-50), [JE-35](../../docs/semantics/expressions.md#je-35)) | The message |
| The string error of a throwing `stop_after_if` ([FX-21](../../docs/semantics/execution.md#fx-21)) | The string |
| The error stop of a `stop_after_all_iters_if` that fails to evaluate ([CF-13](../../docs/semantics/control-flow.md#cf-13)): an `EarlyStopError` whose message quotes the expression and then gives its cause | After the quoted expression |
| The worker's execution error, `ExecutionErr` ([WR-37](../../docs/semantics/languages.md#wr-37), [WR-43](../../docs/semantics/languages.md#wr-43), [FX-51](../../docs/semantics/execution.md#fx-51)), an argument resolution's among them ([JE-42](../../docs/semantics/expressions.md#je-42), [WR-5](../../docs/semantics/languages.md#wr-5)) | After its `execution error:\n` lead-in |

So `Error: Error: <evaluator text> <loc> <loc>` loses both suffixes,
`Error: <typed message> <loc>` and `Error: Internal: <text> <loc>` lose one,
and `Error: SqlErr: <detail> <loc> <loc>` loses both. Each marker, like each
real ` @<file>.rs:<line>:<col>` suffix, has a space before it. A typed message with no
prefix, such as the sleep messages or `Expected an integer, …`, and the
execution errors that the catalogue writes without a location, such as
`Proxy is an EE feature` and the timeouts, keep their text, and the
comparison shows any suffix.

What the rule never touches:

- A pointer may name the error object or a failure value `{"error":…}`, and
  names only a value that windmill-lite generated: a whole value, or an
  entry that the trace points to inside a loop's array. So user text is
  never stripped.
- A user's suffix inside a generated message stands before the engine's
  stack, not at the end.
- An error stop's own `error_message` matches no template.
- The normaliser, a separate pass before the comparison, masks a location
  only in the completion failure's `InternalErr`, which it recognises by its
  fields wherever it stands; it escapes a literal `<loc>` as `<<loc>`.

`StripNotFoundLocations(o, at)` handles the parenthesised not-found location
` (<file>.rs:<line>)` that ends the lookup helper's text, `<kind> not found
at name <name>`. That text can stand inside a message, as in a transform
error that quotes a failed fetch of a step's result, and no textual rule
tells it from a user's own phrase of the same shape.

So it is stripped only where the case's expected text writes `<loc>` for it:
the run lists those values in `not_found_locs`. At each pointer that names an error object, a
value whose `error` member is one, or the message itself, it removes the
location of the first not-found text in the message, with its leading
space. `wltest check` passes it only the declared pointers at or below a
value that windmill-lite generated.

## Comparison

`Compare(got, want, options)` returns the differences, each written
`<pointer>: <got> != <want>`:

- **Exact** by default. `Subset` accepts extra members and steps.
- **Objects** compare without regard to member order
  ([OR-13](../../docs/semantics/oracle.md#or-13)). `StrictKeyOrder` compares
  member order inside values, never in the observation's own fields.
- **Arrays** compare in order, except at `Unordered` pointers, which compare
  as multisets ([OR-23](../../docs/semantics/oracle.md#or-23)).
- **Numbers** compare by their JSON text.
- **A step field** that one side leaves to its canonical absence and the
  other spells out is expanded first, so the two spellings compare equal.
- **`WithRecording`** leaves `carried` and `resolved_args` out, because a
  recording holds neither.

For an unordered array, each expected element is compared at its own index,
so strict key order applies inside it, and unordered pointers below it (such
as `/result/0/values`) still hold:

- **With such a pointer below the array**, equality depends on the index,
  and the elements are matched one to one by a maximum bipartite matching.
- **Otherwise** elements with the same canonical text (members sorted by key,
  or in order under strict key order) are interchangeable. An exact
  comparison counts them by that text.
- **A subset comparison**, whose equality is not symmetric, matches groups of
  them with their multiplicities, comparing each pair of groups once. So a
  broad expected element never takes the only match of a narrower one, and
  repeated elements cost one comparison.

`BenchmarkCompareUnordered` measures the counting and the group-matching
paths. `Compare` ignores `StrictKeyOrder` in a subset comparison; the fixture
loader refuses that pair.
`testdata/compare/cases.json` holds the cases.

## Errors

| Error | When |
|---|---|
| `ErrInvalidSitePath` | `ParseSitePath` (and `UnmarshalText`) meets a text that the encoder does not write. A segment constructor given a negative number, and `Child` given a zero `Segment`, panic instead |
| `ErrInvalidText` | `ParseText` meets a placeholder with no closing `>`, a path that is not a canonical site path, or a bad `?N` number; a marker never fails |
| A panic | `UnwalkedPart` given a number below 1, and `MarkerPart` given text that is not a marker |
| `ExprCaseError` (`contextual_case_needs_flow_fixture`) | An expression case names a predecessor, a handler or a container, or uses a site other than `transform`, `static` and `skip_if` |
| A loader's error | A fixture, expression, step or plugin file breaks a loading rule above; the error names the file or the fixture's directory |
| Encoding and decoding errors | `Response.Bytes` on an unknown encoding, or on a body that is not valid base64. An observation that does not encode: an unknown status, a site path that is not valid UTF-8, contradicting step fields |
| Other errors | `LiftExprFile` into an existing directory; a pointer that does not parse in `Compare`, `MaskPaths`, `StripLocations`, `StripNotFoundLocations` or `ValueAt`; a runtime error value that is not JSON |
| `replay_foreign_root`, `replay_bad_request`, `replay_unresolved`, `replay_miss` | The replay service's answers ([The replay service](#the-replay-service)) |
| `replay_site_mismatch`, `stub_not_called` | `CheckReplay` ([CheckReplay](#checkreplay)) |

## Decisions where the reference is silent

| Question | The code's choice | Why | Pinned by |
|---|---|---|---|
| How is a site named? | Typed segments in one canonical text, with percent escapes and `~<k>` for a repeated id | Every site gets a distinct path and placeholder | `testdata/sitepath/`, the fuzz targets |
| How is user text that looks like a placeholder kept apart? | A run of `<` before such a head is doubled | Normalisation stays injective | `TestTextCases`, `FuzzTextInjective` |
| How are a module's own status, its carried value and its resolved arguments written when they add nothing? | Left out (canonical absence), and expanded before a comparison | One spelling per observation, and either spelling compares equal | `TestObservationDimensionCases`, `TestCompareCases` |
| How does stub mode give a stub its outcome? | A run's `replay` table, by site and attempt, through a run-scoped service | The same table drives the oracle and windmill-lite | `TestReplayServiceAnswers`, `TestCheckReplay` |
| Which location suffixes does a comparison strip? | At a value that windmill-lite generated, chosen by the error's kind and its layer prefixes: trailing ` @<file>.rs:<line>:<col>` locations and spaced `<loc>` markers. A not-found location only where the run declares it | User text is never stripped, and windmill-lite writes no location | `TestStripLocations`, `TestStripNotFoundLocations` |
| What does a step case's comparison mask by default? | `/result/error/stack` on a side whose status is `failure`, unless the case sets `keep_stack` | A successful result is data, and a failure's stack differs between runtimes | `TestCompareStepMasksOnlyAFailuresStack`, `TestCompareStepKeepStack` |
| How do unwalked ids that are themselves member names sort? | Not settled: such names sort by their raw ids, so the numbers can follow the ids' values. No recorded run has one | The oracle finding does not say | code only |

## Tests

The tests need no environment variable, no Bun and no cgo.

1. From the repository root, run `go test ./internal/conformance`.
2. To run a fuzz target longer, run it alone, for example
   `go test ./internal/conformance -run '^$' -fuzz FuzzSitePathInjective -fuzztime 30s`.

CI runs the package's tests with `-race` in its Go jobs
([docs/ci.md](../../docs/ci.md#the-go-jobs)).

| File | Tests | What they check |
|---|---|---|
| `sitepath_test.go` | `TestSitePathEncodeCases`, `TestSitePathRejectCases`, `TestSitePathFlowCases` | `testdata/sitepath/`: encodings, refusals and the negative tests |
| | `TestPlaceholderStaysDelimitedInsideStrings`, `TestModuleSegments`, `TestSegmentAccessors`, `TestConstructorsRejectNegativeNumbers`, `TestMarshalTextRejectsInvalidUTF8` | Placeholders, repeated ids, accessors, negative numbers, and text that is not UTF-8 |
| | `FuzzParseSitePath`, `FuzzModuleSegmentInjective`, `FuzzSitePathInjective` | The fuzz targets of [The encoder's negative tests](#the-encoders-negative-tests) |
| `observation_test.go`, `dimensions_test.go` | `TestObservationJSONKeysAreCanonicalSitePaths`, `TestObservationDecodingRejectsBadKeysAndStatuses`, `TestStatusVocabulary`, `TestNotRunStepsCarryRanFalse`, `TestObservationDimensionCases`, `TestImpliedModuleStatusOnNonModuleSitesBuiltInGo`, `TestModuleStatusIsImpliedByTheJobStatus` | The observation's keys, statuses and canonical form |
| `fixture_test.go` | `TestLoadFixtures`, `TestSmokeFixture`, `TestLoadFixtureRejects`, `TestFixtureOptionChecks`, `TestFixtureProviders`, `TestYAMLToJSON` | Every fixture under `conformance/flows` loads, and the smoke fixture's fields; the loading rules and refusals; the providers; YAML to JSON with order and number text kept |
| `exprcase_test.go` | `TestExprCaseRefusesContext`, `TestExprCaseChecks`, `TestExprFileNamedAfterFinding`, `TestLiftExprFile`, `TestFindExprFiles`, `TestCheckClaims` | The expression loader's refusals, lifting, the search, and the claims |
| `stepcase_test.go` | `TestLoadStepFiles`, `TestLoadStepFileRefuses`, `TestStepCaseLanguageAndTimeout` | Every committed step file loads; the refusals; languages and timeouts |
| | `TestCompareStep`, `TestCompareStepMasksOnlyAFailuresStack`, `TestCompareStepKeepStack` | The step comparison and its stack mask |
| `plugins_test.go` | `TestPluginFileLoads` | `conformance/plugins/hooks.yaml` loads, and its folder and contract are written |
| `replay_test.go` | `TestReplayTableCases`, `TestOutcomeRoundTrip`, `TestFixtureReplayNeedsStub`, `TestRunBatches` | `testdata/replay/tables.json`; outcomes; `replay` without `stub`; the submission batches |
| | `TestReplayServiceAnswers`, `TestReplayServiceConcurrentRuns`, `TestReplayServiceRacedDuplicates` | The service's answers, concurrent runs, and a job answered once |
| | `TestCheckReplay`, `TestCheckReplayAcrossFlowStepAttempts`, `TestCheckReplaySkipsIdentityJobs` | Each failure of `CheckReplay`; a retried flow step; identity jobs |
| `text_test.go` | `TestTextCases`, `TestTextMarkersDistinct`, `TestTextUnwalkedPlaceholder`, `TestParseTextRejects` | `testdata/text/cases.json`; a marker differs from user text that spells it; the unwalked placeholder's encodings; unclosed placeholders, bad site paths and malformed numbers refused |
| | `FuzzTextRoundTrip`, `FuzzTextInjective`, `FuzzParseText` | Round trips and injectivity, with an unwalked id among the inputs |
| `normalise_test.go` | `TestNormaliseCases`, `TestMaskPaths`, `TestNormaliseUnwalkedJobIDs` | `testdata/normalise/cases.json`; the pointers; `testdata/normalise/unwalked.json`, each case run twice with the ids drawn in the opposite order of value |
| | `TestStripLocations`, `TestStripNotFoundLocations` | A generated error compares equal to the oracle's with its location, two user errors that differ only in a suffix still differ, an escaped literal is kept, each template's cases, and a not-found location stripped only where declared |
| | `TestNormaliseText`, `TestNormaliseTextKeepsHandlerData`, `TestResponseBytes` | A response body's normalisation, a handler's own data kept, and the base64 body |
| `compare_test.go` | `TestCompareCases`, `TestPointers`, `TestIndentJSONKeepsOrderAndText` | `testdata/compare/cases.json`; the pointer helpers; `IndentJSON` |
| | `TestUnorderedCountingAgreesWithMatching`, `TestUnorderedSubsetGroupsAgreeWithMatching` | On random arrays, counting agrees with the matching under either key-order policy, and in subset mode the matching of groups agrees with that of single elements |
| | `BenchmarkCompareUnordered` | The cost of the counting and group-matching paths |

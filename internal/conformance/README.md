# internal/conformance

**Current state:** the shared identity and observation vocabulary of
milestone K1, the fixture format, normaliser and comparator of milestone
E2, the expression-case format, its lifting into generated flows, and the
claims and unit cases of recording batch E3a, and the replay table and
replay service of stub mode, milestone E4a, the step-case format and
its comparison, milestone C3a, the location rule of comparisons with
the oracle, milestone E1, which milestones N1b and N3a extended to the
flow engine's templates, and the run-and-wait response, the setup policies
and the pull of recording batch E3e, and the oracle configuration a
fixture needs (`oracle.session`) of recording batch E3f
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)), and the
host-plugin case format of milestone F2c. This README
covers `internal/conformance` and its `testdata/`. The oracle harness
([internal/oracle](../oracle/README.md)) uses all of it; `wltest check`
([cmd/wltest](../../cmd/wltest/README.md)) runs the step cases and the flow
fixtures on windmill-lite.

## What it holds

| File | Contents |
|---|---|
| `sitepath.go` | `SitePath`, `Segment` and the encoder of [ADR 0011](../../docs/design/0011-differential-testing.md), which answers [Q-OR-17](../../docs/semantics/oracle.md#q-or-17) |
| `status.go` | `RunStatus` (Windmill's four job statuses plus windmill-lite's `latched` and `halted`), `StepStatus` ([OR-V2](../../docs/semantics/oracle.md#or-v2)) and `ModuleStatus` ([FX-1](../../docs/semantics/execution.md#fx-1)) |
| `observation.go` | `Observation`, `StepObservation`, `BranchChosen` and `Response`: the normalised observation of [OR-15](../../docs/semantics/oracle.md#or-15), plus the module status, carried value and resolved arguments, and a run-and-wait run's HTTP response (see below) |
| `fixture.go` | `Fixture`, `Run`, `Copy`, `Setup`, `Recording`, `FixtureProvider`: the flow-fixture format of [OR-15](../../docs/semantics/oracle.md#or-15), with `LoadFixture`, `FindFixtures` and `WriteRecording` (see [Fixtures](#fixtures)) |
| `exprcase.go` | `ExprFile`, `ExprCase`, `ExprExpect`: the expression level of [ADR 0003](../../docs/design/0003-semantics-placement.md), with `LoadExprFile`, `FindExprFiles` and `LiftExprFile` (see [Expression cases](#expression-cases)) |
| `stepcase.go` | `StepFile`, `StepCase`, `StepRun`, `StepCheck`, `StepLate`: the step level of [ADR 0003](../../docs/design/0003-semantics-placement.md), with `LoadStepFile`, `FindStepFiles`, `LoadStepDeferred`, `CompareStep` and `CompareStepNode` (see [Step cases](#step-cases)) |
| `claims.go` | `Claim`, `CheckClaims`, `Delay`, `ValueAt`, `SameJSON`: partial assertions about an observation |
| `unit.go` | `UnitCase`, `UnitContext`, `MessageMatch`: a flow fixture's expressions restated at unit level |
| `compare.go` | `Compare`, `CompareOptions`, `Difference`: the comparator; JSON Pointer helpers; `IndentJSON` |
| `text.go` | `EncodeText`, `ParseText`, `SplitJobIDs`: normalised strings, with the escape that keeps them injective |
| `normalise.go` | `Normaliser` and `MaskPaths`: the value and text rules of [OR-14](../../docs/semantics/oracle.md#or-14); `StripLocations` and `StripNotFoundLocations`, the rules a comparison with windmill-lite adds (see [Location suffixes](#location-suffixes)) |
| `yamljson.go` | `YAMLToJSON`: fixture YAML to JSON text, keeping member order and number text |
| `plugins.go` | `PluginFile`, `PluginCase`, `PluginSpec`, `PluginFailure`, `PluginRun`, `PluginThen`, `PluginExpect`, `PluginOutcome`, `LoadPluginFile`, `ReadSettlement` (the case plugin's report of settled runs): the host-plugin cases of `conformance/plugins` and a case's later runs, their folder and host contract, and the check of each run's outcome |
| `replay.go` | `ReplayTable`, `Outcome`, `ReplayService`, `Locator`, `ReplayCall`, `ReplayEffect`, `CheckReplay`: stub mode's replay table, the run-scoped service that answers stubs, and the check of a run's calls after it (see [Stub mode](#stub-mode)) |

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

A run submitted through run-and-wait (`submit: run_wait_result`) also has a
`response`: `{status, content_type, encoding, body}`, the HTTP status, the
Content-Type header as sent, parameters included, and the body
([FX-36](../../docs/semantics/execution.md#fx-36),
[FX-37](../../docs/semantics/execution.md#fx-37),
[OR-10/b](../../docs/semantics/conformance.md#or-10)). The body is text, not
a value, because its spelling is what an HTTP client sees: jsonb's text
output with spaces, or a string result unquoted under a content type. Every
other run has no `response`. A body that is not UTF-8 (base64 transfer
encoding can decode to any bytes) is kept losslessly instead: `encoding` is
`base64` and `body` its base64 text, which `Response.Bytes` decodes.

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
| `fixture.yaml` | `id`; `spec` (findings and catalogue cases); `scope` (`core`, `later`, `out`, `EE`); `provenance` (the catalogue's tags); `languages`; `oracle: {workers, effects, concurrency, deploy, setup, pull, session}` (`deploy`: `cli`, the default, or `api`, which has the oracle harness create the tree through the REST API as written; `setup`: when `setup.yaml` is applied, below; `pull`: with `deploy: api`, read the deployed tree back with the CLI's `sync pull` into the runs' deployment evidence; `session`: the oracle configuration the fixture needs, a name of the oracle harness's session configurations such as `native`, a worker that serves `nativets`, empty for the reference configuration, so that only a session of that configuration records it); `compare: {mode, key_order, stack, mask, unordered}`; `stub` (stub mode, which deploys through the CLI only); `roots` (windmill-lite only, milestone PF3: the bundle's other roots, the scripts and flows the steps dispatch locally, which a host contract lists, ADR 0002; `wltest check` compiles them as the default dispatcher's program, and the oracle, which runs the whole workspace, ignores them); `provided` (windmill-lite only, milestone F2a: the mandatory host-provided sites of the bundle's host contract, each written as the contract writes its `provided` entries, a selector, a `static` value or a `provider` name, and `mandatory: true`, ADR 0002; `wltest check` builds the bundle with them, and the oracle, which would run the real targets, ignores them, so such a fixture holds expects); `providers` (the named providers `wltest check` registers for them, by name, each with exactly one of `result`, `error`, an object, and `decline: true`, `FixtureProvider`); `note` |
| `windmill/` | the sync tree the oracle's CLI pushes, with the metadata and locks `generate-metadata` writes |
| `setup.yaml` | optional `resource_types`, `variables` and `resources`, created after the push unless `oracle.setup` says otherwise |
| `runs/<name>.yaml` | `target`, `kind` (`flow` or `script`), `args` (an object), `cancel_after_ms`, `group`, `submit` (empty, or `run_wait_result`), `replay` (stub mode only), `spec`, `expect` (an observation), `note` |
| `recorded/<name>.json` | `{"observation": …, "provenance": …}`, written by `wltest record` |

Every file decodes strictly: unknown keys, YAML aliases and merge keys,
repeated keys, and numbers that are not JSON numbers (`0x1F`, `1_000`,
`.inf`) are refused, so a value is always the JSON its author wrote.
`compare` paths are RFC 6901 JSON Pointers into the observation, for example
`/steps/m:a0/args/fi_keys`; a `/` inside a site path is `~1`. `stack` masks a
string as `<stack>` (OR-V3 clause c) and `mask` replaces any value with
`<masked>` (environment-derived values, OR-29). Neither reaches inside a
run-and-wait response's body, which is text: a pointer can mask the whole
`/response/body`.

`oracle.setup` is `after_push` (the default: once, after the push),
`before_push` (once, before the push, which then removes what it seeded,
[OR-31](../../docs/semantics/oracle.md#or-31); CLI deployment only) or
`each_run` (after the push, and its variables again before every run, for
a fixture whose runs change them). The last two need variables in
`setup.yaml`, and `each_run` refuses runs in a `group`, which would share
the variables between resets. A run with `submit: run_wait_result` is submitted through the
synchronous run-and-wait endpoint, then polled and walked like any other,
and its observation holds the response; it runs a flow alone, so it takes
no `kind: script`, `group`, `cancel_after_ms` or `replay`.

Runs with the same `group` are submitted together, as concurrent outer runs
(`RunBatches`: each group at its first run's place, in name order); every
other run runs alone. The oracle fails a group as a whole when one of its
runs fails or when they were not in flight together.

A run may also carry `claims`, `delays` and `unit`, which the comparator
does not read (see [Claims](#claims)), and two hints only the comparison
with windmill-lite reads (see [Location suffixes](#location-suffixes)):
`not_found_locs`, the values whose parenthesised not-found location
Windmill generated, and `copies` (`Copy`: `at`, a JSON Pointer, and
`of`, a site path), the values the case's flow copies, through user
code, from the error windmill-lite generates at a site. The recorder
ignores both; loading checks that each pointer and site path parses. A fixture whose tree is generated
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

## Step cases

`LoadStepFile` reads one `conformance/steps/<lang>/<finding>.yaml`
strictly: unknown keys are refused, as is a file not named after its
finding, a case whose spec cites a finding without a variant, a run that
checks nothing, a `by_mode` without both `warm` and `fresh` or without the
case's `deviation` (a deviation of ADR 0001 §5, or `5a` for its fetch-only
restriction), a `left` without `left_behind` among its `warnings` or
with a count below 1, an `untracked` without `native_untracked` among its
`warnings` or with a count below 1, a `codebase` that is not a mapping, `files` without
`codebase` or with a path that leaves the sync root, a `node` check on a
case that is not a CommonJS codebase script or that gives more than
`expect` and `claims`, a `language` other than `bun`, `bunnative` and
`nativets`, a `timeout` that is not a positive number of seconds, and two
cases of one file that give one path two sources (a file's scripts and
files share one bundle). Only `bun` has a language directory: native steps
run in the Bun harness (D5). `FindStepFiles` finds the files below its roots, `deferred.yaml`
aside, and `IsStepFile` tells one by its path. The format is described in
[conformance](../../conformance/README.md#step-cases).

A step file can also ask for shared code ([ADR 0013](../../docs/design/0013-shared-module-code.md)):

- A file's `shared_code` builds its bundle with shared code.
- A case's `scripts` are more entry scripts, by Windmill path, and a run's
  `script` invokes one of them. `LoadStepFile` refuses a `scripts` path
  that is not a Windmill path, and a run's `script` that is not one of
  the case's `scripts`.
- A Bun script's `files` may be workspace scripts that it imports
  (`f/<folder>/<name>.ts`) without `codebase`.

`CompareStep` compares one run's observation, the step's stored value as
`{"status", "result"}`, with the run's check for a configuration: it masks
the case's own `stack` and `mask` pointers on both sides, and
`/result/error/stack` (`DefaultStepStack`) on a side whose status is
`failure`, since a successful result is data, unless the case sets
`keep_stack` so that claims read the stack's text, compares with `Compare` and the case's
`unordered` and `strict_key_order`, and checks the claims. A run's `late`,
`settled`, `warnings`, `left` and `untracked` are the runner's to check, since only it
sees the invocation's Reporter, and so are its `calls`, the platform calls
the facade recorded against the invocation (milestone PF1). A case's
`setup`, in `setup.yaml`'s format, is the variables and resources its
facade serves (milestone PF2). `CommonJS` tells a codebase case built as CommonJS
(its entry's `format` is not `esm`, WR-47), which also runs on Node (D13);
`CompareStepNode` compares what Node showed with the run's `node` check,
or with its own `expect` and `claims` when the two runtimes agree, as
`CompareStepCheck` does for any check.

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

## Stub mode

A fixture with `stub: true` runs with every script body replaced by a stub
that keeps `main`'s signature and asks a run-scoped replay service for its
outcome ([ADR 0011](../../docs/design/0011-differential-testing.md)). The
stub generator and the oracle's side are in
[internal/oracle](../oracle/README.md#stub-mode); this package holds what
the oracle and windmill-lite (milestone E4b) share.

**The replay table** is a run's `replay`: for each site, the outcomes of
its attempts, from attempt 0.

```yaml
replay:
  "m:loop/i:0/m:call": {result: {item: apple}}
  "m:loop/i:1/m:call":
    - {error: {message: busy}}
    - {error: {name: Busy, message: busy, code: 7}}
    - {result: {item: pear}}
```

A site is canonical site-path text ending in a module or slot segment (the
root `""` for a script run); an attempt is a list index, never an `r:`
segment. An outcome is `{result: …}` or `{error: {message, name?, …}}`,
exactly one; the stub returns the result in the table's member order, or
throws an `Error` whose other members become own properties (Windmill's
`extra`). An error cannot set `stack` or `step_id`, which Windmill writes.
A lone outcome stands for a list of one. `testdata/replay/tables.json` holds
the cases.

**The service** (`ReplayService`) is an HTTP handler for `POST
/replay` below the effect sink's URL. `Open(root, table, locator)` starts
answering a run and `Close(root)` returns its calls; several runs can be
open at once. For each call it:

1. refuses a root that is no open run (`409 replay_foreign_root`) and a
   body that is not a call (`400 replay_bad_request`);
2. answers a job it already answered with the same outcome (idempotent per
   raw job id), without asking the locator again;
3. asks the run's `Locator` for the job's site, within 60 s
   (`504 replay_unresolved` otherwise);
4. counts the attempt as the number of distinct jobs already seen at that
   site in the run, and answers the table's outcome (`404 replay_miss` when
   it has none).

Each answered call is a `ReplayCall` whose `Effect` is
`{"site","attempt","step","job","flow","root_flow","root","args"}` in that
order: the resolved site and attempt, then what the stub sent. The runner
appends the calls to the run's effects.

**After the run**, `CheckReplay(calls, walked, scripts)` is the check
both sides apply (milestone E4b moved it here from the oracle). `walked`
maps every job id the run's observation reached to its site, a failed
attempt `k` at `<module>/r:<k>`: the oracle's walk, or windmill-lite's
trace. `scripts` are the jobs that executed a script. It fails the run on
the first refused call (`replay_miss`, `replay_unresolved`, as the stub
received it), and on a call whose site and attempt disagree with where its
job was walked (`replay_site_mismatch`). The call's site must be the job's
walked site without attempt segments, and its attempt the job's place
among the jobs walked at that site that execute a script or called, in
walk order, as the replay service counts only the jobs that call: an
identity job, a skipped or mocked step's, takes no place. That puts a failed
attempt `k` at `<site>/r:<k>` and the last job at `<site>`, with `k` the
number of failed attempts before it. Below a retried flow step (milestone
N5), each attempt's subflow is walked below the step's `r:<k>`, so a
subflow step's jobs are ordered by the flow step's attempt first, then by
their own. It also fails a run in which a script job never called
(`stub_not_called`, with the sorted sites). The error's text is the
refusal as one JSON object. `TestCheckReplay` and
`TestCheckReplayAcrossFlowStepAttempts` and `TestCheckReplaySkipsIdentityJobs` cover each case.

The locator differs by side. On the oracle it walks the job tree
([internal/oracle](../oracle/README.md#stub-mode)). On windmill-lite,
`wltest check` records the site each job launches at through the
engine's invocation-environment hook
([cmd/wltest](../../cmd/wltest/README.md#flow-fixtures)).
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

`Normaliser.Text` normalises a text kept as written, a run-and-wait body:
walked job ids become placeholders, and, when the text is a JSON value, a
stack is masked exactly where `Value` would mask it: in an object deep-equal
to a runtime error, or to its inner error, only that `stack` string's span
becomes `"<stack>"`. The same string anywhere else, a handler's own data
among it, stays, and so do spacing, member order and every other
character. `TestNormaliseText` and `TestNormaliseTextKeepsHandlerData` hold
its cases.

### Location suffixes

Comparing windmill-lite with the oracle adds one rule
([ADR 0011](../../docs/design/0011-differential-testing.md), Location
suffixes): windmill-lite generates Windmill's messages without a Rust
location (D14), so where windmill-lite's value is an error it generated,
the oracle's location suffix is stripped, but only from a message that
matches a catalogue template carrying `<loc>`. `StripLocations(o, at)`
applies it to the oracle's normalised observation (an expect or a
recording): at each pointer, a value windmill-lite's trace says it
generated, it follows [FX-50](../../docs/semantics/execution.md#fx-50)'s
masking rule:

1. **Trailing suffixes, by layer.** A message that is Windmill's error
   display begins with k layer prefixes, one after another: `Error: `,
   `Internal: `, `SqlErr: `, `IoErr: `, `Utf8Err: `, `UuidErr: `,
   `SerdeJson: ` and `HexErr: ` (`Error parsing …` and `Error during …`
   are not prefixes). It loses at most k trailing
   ` @<file>.rs:<line>:<col>` suffixes from the end, or `<loc>` markers
   the expected text writes for them. A message with no layer prefix
   loses none, and a suffix beyond the k-th is the message's own.
2. **Parenthesised locations, only where written.** The
   ` (<file>.rs:<line>)` that ends a not-found message is not a layer's
   suffix, and no text tells a generated one from a user's
   ([FX-50/d](../../docs/semantics/conformance.md#fx-50)), so it is never
   searched for. It goes only where the expected text writes `<loc>` for
   it, as a case whose step reads a result the lookup cannot find does
   ([CF-19](../../docs/semantics/control-flow.md#cf-19)/c): every `<loc>`
   marker step 1 left in a generated value's message is removed, wherever
   it stands. A recording, whose normaliser masks no such location, keeps
   it, and the comparison shows it.

Step 1 reads these displays:

| Template | Where the display starts |
|---|---|
| the recognised internal error of item 3, `InternalErr`, whose location the normaliser masked as `<loc>` | the message |
| a chaining error, named `InternalError` (a later transition) or `Anyhow` (a pickup) ([FX-15](../../docs/semantics/execution.md#fx-15), [FX-50](../../docs/semantics/execution.md#fx-50), [JE-35](../../docs/semantics/expressions.md#je-35)) | the message |
| the string error of a throwing `stop_after_if` ([FX-21](../../docs/semantics/execution.md#fx-21)) | the string |
| the error stop of a `stop_after_all_iters_if` that fails to evaluate ([CF-13](../../docs/semantics/control-flow.md#cf-13), milestone N3a), an `EarlyStopError` whose message quotes the expression and then gives its cause | after the quoted expression |
| the worker's execution error, `ExecutionErr` ([WR-37](../../docs/semantics/languages.md#wr-37), [WR-43](../../docs/semantics/languages.md#wr-43), [FX-51](../../docs/semantics/execution.md#fx-51) rule 5), an argument resolution's among them ([JE-42](../../docs/semantics/expressions.md#je-42), [WR-5](../../docs/semantics/languages.md#wr-5)/b, PF2) | after its `execution error:\n` lead-in |

So `Error: Error: <evaluator text><loc><loc>` loses both suffixes,
`Error: <typed message><loc>` and `Error: Internal: <text><loc>` one, and
`Error: SqlErr: <detail><loc><loc>` both, while a typed message with no
prefix (the sleep messages, `Expected an integer, …`) and the execution
errors the catalogue writes without a location (`Proxy is an EE feature`,
the timeouts) keep their text, and the comparison shows any suffix. A
pointer may name the error object or a failure value `{"error":…}`.
Nothing else changes: user text is never stripped, since a pointer names
only a value windmill-lite generated (a whole value, or an entry the trace
points to inside a loop's array, milestone N3a), a user's suffix inside a
generated message stands before the engine's stack, not at the end
([FX-50/c](../../docs/semantics/conformance.md#fx-50)), and an error stop's
own `error_message` matches no template; the normaliser masks no location
in user text and escapes a literal `<loc>` (`<<loc>`).
`TestStripLocations` holds ADR 0011's positive case (a generated
`Internal:` error compares equal to the oracle's with its location) and
its negative one (two user errors that differ only in such a suffix still
differ), that an escaped literal is never stripped, each template's
positive and negative cases, FX-50/a to FX-50/d, and a parenthesised
location masked where the expected text writes `<loc>` and kept where it
writes the location out (milestone FU6).

The parenthesised not-found location ` (<file>.rs:<line>)` that ends the
lookup helper's text, `<kind> not found at name <name>`, can stand inside
a message, as in a transform error quoting a failed fetch of a step's
result ([CF-19](../../docs/semantics/control-flow.md#cf-19)/c), and no
textual rule tells it from a user's own phrase of the same shape
([FX-50](../../docs/semantics/execution.md#fx-50)/d). So it is stripped
only where the case's expected text writes `<loc>` for it (FX-50 rule 3):
the run lists those values in `not_found_locs` (milestone N3c), and
`StripNotFoundLocations(o, at)` removes, at each pointer that names an
error object, a value whose `error` member is one, or the message itself,
the location of the first not-found text in the message, with its leading
space. `wltest check` passes it only the declared pointers at or below a
value windmill-lite generated. `TestStripNotFoundLocations` covers a
declared value, an undeclared user phrase that keeps its location, and
pointers that name no such message.

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
  multisets ([OR-23](../../docs/semantics/oracle.md#or-23)). Each expected
  element is compared at its own index, so strict key order applies inside
  it and unordered pointers below it (for example `/result/0/values`) still
  hold. With such a pointer below the array, equality depends on the
  index, and the elements are matched one to one by a maximum bipartite
  matching. Otherwise elements with the same canonical text (members sorted
  by key, or in order under strict key order) are interchangeable. An exact
  comparison counts them by that text, in about linear time. A subset
  comparison, whose equality is not symmetric, matches groups of them with
  their multiplicities, comparing each pair of groups once, so a broad
  expected element never takes the only match of a narrower one and
  repeated elements cost one comparison (500 repeated scalars: 63 µs, from
  23 ms for the matching of single elements);
  `BenchmarkCompareUnordered` measures each path;
- numbers by their JSON text;
- a step field one side leaves to its canonical absence and the other spells
  out is expanded first, so the two spellings compare equal;
- `WithRecording` leaves `carried` and `resolved_args` out, because a
  recording holds neither (ADR 0011).

`testdata/compare/cases.json` holds the cases.
`TestUnorderedCountingAgreesWithMatching` checks, on random arrays with
repeated elements and reshuffled members, that counting and the matching
agree under either key-order policy, and
`TestUnorderedSubsetGroupsAgreeWithMatching` that, in subset mode, the
matching of groups agrees with the matching of single elements when the
actual elements carry extra members.

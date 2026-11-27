# conformance

**Current state:** working. The suite has eleven levels, from
[OpenFlow](../docs/semantics/openflow.md) decode cases to whole flow
fixtures, and each runs in CI. The [oracle](../docs/glossary.md#oracle), an
unmodified Windmill Community Edition `v1.862.0`, records the flow fixtures
and the expression cases, but not a fixture with
[provided sites](../docs/glossary.md#provided-site), which only
windmill-lite runs. The cases of the other levels are written from the
[semantics reference](../docs/semantics/README.md), or from windmill-lite's
own design where a level tests its own rules.

This README covers `conformance/`.

A [conformance case](../docs/glossary.md#conformance-case) states inputs and
the outputs that windmill-lite must produce. The suite pins each
implementation, the Go packages and the
[Bun harness](../harnesses/bun/README.md), to the semantics reference:

- Every case cites what it encodes: the catalogue case
  ([docs/semantics/conformance.md](../docs/semantics/conformance.md)), the
  [finding](../docs/glossary.md#finding) whose rule it encodes, or, for
  windmill-lite's own rules, the design section or named deviation.
- Every fixture is synthetic: invented for this repository to exercise one
  behaviour.
- A case's provenance says where its expectation comes from. It is
  `oracle-observed` when the catalogue's expectation was observed on the
  oracle; a case that checks only one half of it, such as a compile case,
  still carries the tag. It is `source-derived` when the semantics
  reference states it, and `policy` for windmill-lite's own rule. A
  `disputed` case's rule is not settled, and its `note` says which reading
  it encodes.
- A case's scope is the catalogue's: `core`, `later` (windmill-lite does not
  have to pass it yet, even when the oracle has recorded it), `out` (out of
  scope), or `EE` (enterprise only).

The ADR [Where semantics live, and the conformance-suite format](../docs/design/0003-semantics-placement.md)
defines the levels, and the ADR
[Differential testing and performance measurement](../docs/design/0011-differential-testing.md)
defines the oracle's recordings and stub mode.

## Levels

| Level | Directory | Format | What runs it | Recorded by the oracle |
|---|---|---|---|---|
| decode | `openflow/<finding>.json` | [OpenFlow decode cases](#openflow-decode-cases) | `go test ./internal/openflow` | no |
| jsonb | `jsonb/<finding>.yaml` | [Stored-form cases](#stored-form-cases) | `go test ./internal/jsonval`, and against PostgreSQL by CI's `jsonb (PostgreSQL)` job | no |
| load | `syncfolder/<finding>.json` | [Sync-folder load cases](#sync-folder-load-cases) | `go test ./internal/syncfolder` | no |
| compile | `ir/<finding>.json` | [Compile cases](#compile-cases) | `go test ./internal/ir` | no |
| protocol | `protocol/frames.json`, `protocol/exchanges/<case>.json` with the bundle in `protocol/bundle/`, `protocol/host/workdir.json`, `protocol/host/versions.json` | [Protocol cases](#protocol-cases) | `go test ./internal/harness` (the exchanges on the [pinned](../docs/glossary.md#pinned-version) Bun, 1.4.3), and for the codec and host cases `bun test` in `harnesses/bun` | no |
| plugins | `plugins/hooks.yaml` | [internal/conformance](../internal/conformance/README.md#host-plugin-cases) | `go test ./wl` and `go test ./cmd/windmill-lite`, on the pinned Bun | no: windmill-lite's rules |
| build | `build/<finding>.json`, and `build/annotations.json` | [Build cases](#build-cases) | `go test ./internal/bundle`, through the Bun build helper on the pinned Bun; `bun test` in `harnesses/bun` for `annotations.json` | no |
| hooks | `hooks/<topic>.yaml` | [Hook cases](#hook-cases) | `go test ./wl` (`TestHookCases`) | no: windmill-lite's rules |
| flow | `flows/<area>/<case>/` | [internal/conformance](../internal/conformance/README.md#fixtures) | `wltest record` on the oracle; `go test ./internal/oracle` against the recordings; `wltest check` and `go test ./cmd/wltest` on windmill-lite; `go test ./internal/expr` for the runs' unit cases | yes, except a fixture with `provided` sites |
| step | `steps/<lang>/<finding>.yaml` | [internal/conformance](../internal/conformance/README.md#step-cases) | `wltest check` and `go test ./cmd/wltest`, on the pinned Bun, in `warm` and `fresh` | no |
| expr | `exprs/<finding>.yaml`, recordings in `exprs/recorded/<finding>/<case>.json` | [internal/conformance](../internal/conformance/README.md#expression-cases) | `wltest record` on the oracle; `go test ./internal/oracle`; `go test ./internal/expr` at unit level; `wltest check` and `go test ./cmd/wltest` end to end | yes |

The decode, jsonb, load, compile, build, step and expr levels have a
deferred file. It lists the catalogue cases of their findings that are not
cases at that level, each with the reason and where it is checked:
[openflow](openflow/deferred.json), [jsonb](jsonb/deferred.yaml),
[syncfolder](syncfolder/deferred.json), [ir](ir/deferred.json),
[build](build/deferred.json), [steps](steps/bun/deferred.yaml) and
[exprs](exprs/deferred.yaml).

The jsonb, compile, build and step tests fail unless every catalogue case of a
finding with a file there is a case or is deferred. The decode, load and
expr tests require it of every catalogue case of their findings: the OpenFlow
findings; the sync-folder and path findings; and the expression findings with
the `results` lookups across containers. The flow fixtures defer through the recording
manifests ([Recording batches](#recording-batches)).

`go test ./internal/conformance` also loads every flow fixture, step file
and the plugin file, so a case that breaks its format fails there.

## Run the tests

1. For a level that runs on Bun, export `WL_BUN`, the path of Bun 1.4.3,
   and install the build helper's dependencies once:
   `(cd harnesses/bun/build && "$WL_BUN" install --frozen-lockfile)`.
   Export the other variables of the table below that the level needs.
2. For `wltest check`, build `wltest`
   ([cmd/wltest](../cmd/wltest/README.md#build-wltest)).
3. From the repository root, run the level's command from the table above,
   such as `go test ./internal/ir`. To run one test, add `-run`, such as
   `go test -run '^TestConformanceIR$' ./internal/ir`.

| Level | Test that runs the cases, and its file | Its catalogue check |
|---|---|---|
| decode | `TestConformanceOpenFlow`, `internal/openflow/conformance_test.go` | `TestConformanceOpenFlowCatalogue`, the same file |
| jsonb | `TestConformanceStored`, `internal/jsonval/conformance_test.go`; `TestStoredMatchesPostgreSQL` against PostgreSQL, `internal/jsonval/postgres_test.go` | `TestConformanceStoredCatalogue`, `conformance_test.go` |
| load | `TestConformanceSyncFolder`, `internal/syncfolder/conformance_test.go` | `TestConformanceSyncFolderCatalogue`, the same file |
| compile | `TestConformanceIR`, `internal/ir/conformance_test.go` | `TestConformanceIRCatalogue`, the same file |
| protocol | `TestFrameCases`, `internal/harness/frames_test.go`; `TestExchanges`, `exchange_test.go`; the host-mode tests, `hostmode_test.go`; `bun test` in `harnesses/bun` | — |
| plugins | `TestPluginCases`, `wl/plugincases_test.go`; `TestHostPluginCases`, `cmd/windmill-lite/host_test.go`; `TestPluginFileLoads`, `internal/conformance/plugins_test.go`, loads them | — |
| build | `TestConformanceBuild`, `internal/bundle/conformance_test.go`; `TestAnnotationCases`, `annotations_test.go` | `TestConformanceBuildCatalogue`, `conformance_test.go` |
| hooks | `TestHookCases`, `wl/hookcases_test.go` | — |
| flow | The flow suite's tests and others, `cmd/wltest/flows_test.go`; `TestRecordingBatches`, `internal/oracle/batch_test.go`; `TestRecordingsSatisfyClaims`, `TestUnitCasesMatchRecordings`, `internal/oracle/recordings_test.go` | `TestBatchCoverage`, `recordings_test.go` |
| step | `TestStepSuite`, `cmd/wltest/check_test.go` | `TestStepCatalogue`, the same file |
| expr | The flow suite's tests, `cmd/wltest/flows_test.go`; `TestExprCasesAgreeWithRecordings`, `internal/oracle/recordings_test.go`; `TestRecordedCasesThroughPipeline`, `internal/expr/conformance_test.go` | `TestBatchCoverage`, `recordings_test.go` |

| Variable | Value | What needs it | When it is not set |
|---|---|---|---|
| `WL_BUN` | The path of Bun 1.4.3 | The protocol exchanges, plugins, build, step, flow and expr levels on windmill-lite | The tests skip, unless `WL_REQUIRE_BUN` is set: then they fail. `wltest check` uses `bun` on `PATH` |
| `WL_REQUIRE_BUN` | Any non-empty value | CI, so that a missing Bun fails | A missing Bun skips |
| `WL_NODE` | The path of Node; CI uses 24.22.1, the Node of Windmill's image | The Node side of a CommonJS codebase step case, such as `d13.yaml`'s | That side is skipped; with `WL_REQUIRE_NODE` set, as CI sets it, the tests that need Node fail ([cmd/wltest](../cmd/wltest/README.md#tests)) |
| `WL_JSONB_PSQL` | A `psql` command for a PostgreSQL | `TestStoredMatchesPostgreSQL`, which CI's `jsonb (PostgreSQL)` job runs | The test skips |

| What fails | Why | Where it is explained |
|---|---|---|
| A catalogue test: a catalogue case is neither a case nor deferred | A finding has a file at this level, and one of its catalogue cases has no case and no entry in the level's deferred file | The level's section below |
| `contextual_case_needs_flow_fixture` | An expression case names a predecessor, a handler or a container | [Expression cases](#expression-cases) |
| `TestFrameCases` on the file's layout | `frames.json` is not in the layout that `-update` writes | [frames.json](#framesjson) |
| `workdir_not_writable`, `workdir_mismatch` | A host mode working-directory case's expected refusal | [host](#host) |
| `invalid <fixture> <error>` from `wltest check` | The fixture, run or step file does not load | [internal/conformance](../internal/conformance/README.md#fixtures) |
| A `fail` line from `wltest check` | A difference from the expectation, a build or run error, a replay check, a claim or a delay that fails | [Diagnose a failed fixture](#diagnose-a-failed-fixture) |

## Flow fixtures

A flow [fixture](../docs/glossary.md#fixture) is a directory: `fixture.yaml`,
a `windmill/` sync tree, an optional `setup.yaml`, one `runs/<name>.yaml` per
run, and `recorded/<name>.json` once the oracle has run it
([OR-15](../docs/semantics/oracle.md#or-15)). A run gives its `expect`, its
`claims`, both, or neither, when windmill-lite is compared with its
recording alone, as `client-api/job-reads-ended`'s run `ended` is.

The format, field by field,
is in [internal/conformance](../internal/conformance/README.md#fixtures):
the `oracle` options such as `oracle.setup`, the `compare` masks (`stack`,
`mask`, `unordered`), and a run's `submit: run_wait_result`, `claims` and
`delays`. Recording is in
[internal/oracle](../internal/oracle/README.md#a-session).

### Add a flow fixture

1. Pick the area directory under `flows/`, and create the fixture's
   directory, named after its finding or its topic, such as
   `flows/execution/fx-6` or `flows/client-api/state`.
2. Write the sync tree under `windmill/`: `wmill.yaml`, the flows and the
   scripts that the runs need, invented for this one behaviour.
3. Write `fixture.yaml`: its `id`, `spec`, `scope`, `provenance` and
   `languages`, and its `oracle` and `compare` options.
4. Write one `runs/<name>.yaml` per run: the `target`, the `args`, the
   `spec` it records, and the `expect` that the reference predicts, or
   `claims` where only part of the observation is known. A fixture that a
   recording manifest lists needs an `expect` on every run
   ([Recording batches](#recording-batches)).
5. Check it on windmill-lite, from the repository root:
   `bin/wltest check conformance/flows/<area>/<case>`. This needs `wltest`
   built, the build helper's dependencies, and Bun 1.4.3, through `WL_BUN`,
   `--bun` or as `bun` on `PATH`
   ([cmd/wltest](../cmd/wltest/README.md#check-a-flow-fixture-locally)).
6. If the fixture needs two workers (`oracle.workers: 2`), add it to the
   argument list of the session that has its configuration and two
   workers, in
   [`.github/workflows/oracle.yml`](../.github/workflows/oracle.yml):
   `workers2` for the reference
   configuration, `long2` for `long`. The `native`, `result_size` and
   `flow_script_off` sessions have one worker, so a two-worker fixture of
   those configurations needs a new session.
7. Open a pull request. The Oracle workflow records the fixture on the
   oracle, because the pull request changes `conformance/`.
8. Download the artifact of each `Oracle record (<session>)` job. In it,
   `recorded/` holds the new and changed recordings at their repository
   paths, `mismatched/` the observations that did not match an `expect`,
   `tree/` and `tree.diff` any sync-tree metadata the drift gate found
   changed, and `report.json` every run's outcome.
9. Copy the fixture's recordings from `recorded/` into the fixture, and its
   metadata from `tree/` if the drift gate reported a change, and commit
   them.

A fixture with `provided` sites ends at step 5: the oracle skips every run
of it, so its runs hold expectations written from the ADRs, and it gets no
recording.

### Diagnose a failed fixture

1. Find the failing job. An `Oracle record (<session>)` job ran the oracle;
   a Go bucket ran windmill-lite, in a test of the flow suite
   ([cmd/wltest](../cmd/wltest/README.md#the-flow-suite)).
2. On the oracle, read the run's line in the job's log: `expect_mismatch`
   means the oracle did not do what the `expect` says. Under `--check`,
   `new` means the run has no committed recording, and `changed` that the
   observation differs from the recording's; a change of the file's
   formatting or provenance alone is not one. A failed
   drift gate means the sync tree's metadata changed. The steps to act on
   each are in
   [cmd/wltest](../cmd/wltest/README.md#mismatched-observations).
3. On windmill-lite, run the fixture alone:
   `bin/wltest check conformance/flows/<area>/<case>`, with the job's
   `--mode`.
4. Read each difference: a JSON Pointer into the observation, then
   windmill-lite's value before the expected one
   ([cmd/wltest](../cmd/wltest/README.md#read-a-failure)).

### What a fixture commits

- **The sync tree as the CLI writes it.** The tree holds what
  `wmill folder add-missing` and `generate-metadata` write, generated against
  the oracle and committed as written
  ([OR-8](../docs/semantics/oracle.md#or-8)). For a fixture deployed through
  the CLI, the Oracle workflow regenerates it in place on every recording
  (`--regenerate-metadata`, beside `--check`), and its drift gate fails on
  any change.
- **Container defaults.** `generate-metadata` writes a container's defaults into `flow.yaml`
  (`skip_failures` and `parallel` on a loop, `parallel` on a `branchall`,
  and `skip_failure`, `parallel` and `expr` on each branch). So a field left
  out of such a tree never reaches the oracle absent.
- **A tree deployed through the API.** A fixture with
  `oracle: {deploy: api}` is created through the REST API exactly as written,
  and never regenerated, so its tree holds no generated metadata. A flow
  run, not a script job's, keeps deployment evidence in its recording's
  provenance: the flow's submitted value, the value that the API
  answers after the dependency job, and what `generate-metadata` writes for
  the same source
  ([internal/oracle](../internal/oracle/README.md#api-deployment)).
- **A stub fixture's original tree.** A fixture with `stub: true` commits its
  original tree, and each run's `replay` table gives the stubs' outcomes. Its
  scripts that call a service call hosts under `.invalid`, which nothing
  resolves, so a script that kept its body would fail.
- **The stubbed tree** of a stub fixture is written, and its
  metadata regenerated, in a temporary copy on every recording
  ([internal/oracle](../internal/oracle/README.md#stub-mode)). `wltest check`
  runs the same stubbed copy on windmill-lite
  ([cmd/wltest](../cmd/wltest/README.md#flow-fixtures)).

Either way, the runs execute what was deployed, as the reference
configuration of the
[equivalence contract](../docs/design/0001-equivalence-contract.md#1-the-reference-configuration)
requires.

### How a run's expectation is written

- **Written first, then recorded.** A run's `expect` is written from the
  reference before the oracle first runs it, except where a fixture is
  recorded first so that the recordings decide, as `subflow-retry` was. Where the oracle disagreed, the
  run's `note` says what was predicted and what was recorded.
- **Partial expectations.** Runs whose expectation is partial, such as an
  error message's prefix or one argument, say what the catalogue expects in
  `claims`, `delays` and `unit`, and leave the whole observation to the
  recording ([Claims and unit cases](#claims-and-unit-cases)).
- **What only windmill-lite shows.** A module's `carried` value is not in
  Windmill's stored fields, so the oracle records none, and the recorder
  compares an `expect` without it. A run states it where an error stop or a throwing
  `stop_after_if` fails the module. It states it too where a subflow's own
  timeout ends it: the timeout error, or the subflow's failure module's
  result, while the step's job keeps the canceled value
  ([FX-52](../docs/semantics/execution.md#fx-52)).
- **Another configuration.** A fixture that needs the oracle in another
  configuration names it in `oracle.session` (`native`, `long`,
  `result_size`, `flow_script_off`). The Oracle workflow records it in the
  session of that configuration, and every other session skips it
  ([internal/oracle](../internal/oracle/README.md#oracle-configurations)).

### Recording batches

The recording manifests are the files of
[internal/oracle/testdata/batches](../internal/oracle/testdata/batches):
each names a group of findings, the fixtures that record them, and the cases
that it defers. `TestRecordingBatches` holds every run of those fixtures to
its recording, and its recording to its `expect`, so every run of such a
fixture needs both. Their format is in
[internal/oracle](../internal/oracle/README.md#recording-batches).

[cmd/wltest/testdata/flowsuite.json](../cmd/wltest/testdata/flowsuite.json) lists the runs that windmill-lite must
pass, with the runs that a manifest gates on one of its work items ([cmd/wltest](../cmd/wltest/README.md#the-flow-suite)).

### The fixtures

Each table lists the fixtures of one area, with what each encodes. The
findings are linked; the fixture's `spec` and each run's name the cases.

#### flows/oracle

| Fixture | What it encodes |
|---|---|
| `smoke` | The oracle's smoke fixture ([OR-20](../docs/semantics/oracle.md#or-20)): a path script doubles `x`, and an inline Bun step formats it and throws above 100. Runs `ok` (`{"x":3}`) and `fail` (`{"x":60}`) |
| `probes` | The [probe catalogue](../docs/semantics/oracle.md#probe-catalogue) rebuilt as executable fixtures with every binding written out ([Q-OR-16](../docs/semantics/oracle.md#q-or-16), [OR-V9](../docs/semantics/oracle.md#or-v9)): key order, loops, branches, retries, failure modules, stops, `skip_if`, `$var:`, effects, `delete_after_use`, `sleep`, response metadata, and the environment |
| `big-payload`, `nul` | A big payload whole in a result and the next step's arguments ([OR-V1](../docs/semantics/oracle.md#or-v1)); NUL in an object key, a nested value and a Python result, the other positions of [Q-OR-6](../docs/semantics/oracle.md#q-or-6) ([OR-27](../docs/semantics/oracle.md#or-27)) |
| `seeded` | A variable seeded before the push, which removes it ([OR-31](../docs/semantics/oracle.md#or-31); `oracle.setup: before_push`) |
| `key-order` | Key-order cases of [OR-24](../docs/semantics/oracle.md#or-24): integer keys, a predecessor read again on a retry, and an object that a flow's preprocessor built |
| `workers2` | Two workers ([OR-5](../docs/semantics/oracle.md#or-5)): a step that waits for a script that it starts, and parallel bodies whose index 0 is slow ([Q-OR-7](../docs/semantics/oracle.md#q-or-7), [Q-OR-15](../docs/semantics/oracle.md#q-or-15)) |
| `preprocessor` | A script's own preprocessor run as a script job through the run API, and a flow step by path with `apply_preprocessor`, unpinned and pinned to the deployed hash ([WR-11](../docs/semantics/languages.md#wr-11)); deployed through the API |
| `colliding-ids` | Step ids that collide in records and placeholders ([Q-OR-17](../docs/semantics/oracle.md#q-or-17)): a step `failure` beside a failure module, a step `preprocessor` beside a preprocessor, and ids `a/b` and `a%2Fb`; deployed through the API |
| `result-size` | A result over the size limit, about 4.5 MiB against `MAX_RESULT_SIZE_MB=3`, fails with `ResultTooLarge`, also when it carries `wm_failure` ([OR-7](../docs/semantics/oracle.md#or-7)); in the `result_size` configuration |

#### flows/execution

| Fixture | What it encodes |
|---|---|
| `fx-6` | [FX-6](../docs/semantics/execution.md#fx-6): step 0 sees the flow input; the step after a tolerated failure sees the wrapped error; a retry derives `previous_result` again as the string `"{}"` |
| `fx-7` | [FX-7](../docs/semantics/execution.md#fx-7): JavaScript and static transforms, `params`, `undefined`, an unbound `result` |
| `fx-8` | [FX-8](../docs/semantics/execution.md#fx-8): `result` and `previous_result` in `stop_after_if`; no `results` in `sleep` |
| `fx-9` | [FX-9](../docs/semantics/execution.md#fx-9): `skip_if` uses JavaScript truthiness. The string `'false'`, an empty array and an empty object skip a step; `0`, `''`, `null`, `undefined`, `NaN` and `false` do not |
| `fx-10`, `fx-10-c` | [FX-10](../docs/semantics/execution.md#fx-10): a failure module reads an error-stopped step three ways; unknown and not-yet-run ids; which reads of the preceding id use memory ([Q-FX-15](../docs/semantics/execution.md#q-fx-15)); `results[""]` inside a step's own `stop_after_if` |
| `fx-12`, `fx-13` | [FX-12](../docs/semantics/execution.md#fx-12): the failure values of Bun script jobs, and a failed flow whose result is its failure module's, unwrapped. [FX-13](../docs/semantics/execution.md#fx-13): a result with a string `wm_failure` becomes a failure with a `ManualFailure` error |
| `fx-14`, `fx-15` | [FX-14](../docs/semantics/execution.md#fx-14): a throwing input transform fails its step before the script runs, with empty args; a retry re-launches it with them. [FX-15](../docs/semantics/execution.md#fx-15): chaining errors at a later transition, at the root's pickup and at a child flow's pickup |
| `fx-30` | [FX-30](../docs/semantics/execution.md#fx-30): a tolerated failure of the last module ends the flow successfully with its wrapped error, and no failure module runs; in the middle, the next module gets the wrapped error as `previous_result` |
| `fx-31` | [FX-31](../docs/semantics/execution.md#fx-31): an untolerated failure runs the failure module, and the module after the failure never runs |
| `fx-32` | [FX-32](../docs/semantics/execution.md#fx-32): in the failure module, `previous_result` and the failed step's `results` entry are the wrapped error, `error` is its inner object, and its own step id is `failure`. Its retry reuses its failed arguments |
| `fx-33` | [FX-33](../docs/semantics/execution.md#fx-33): the flow's final result is the failure module's. The flow succeeds only when that result recovers: `{"recover": true}` or `[true]`. A child flow that recovered still reports failure to its parent |
| `fx-34` | [FX-34](../docs/semantics/execution.md#fx-34): in a loop body the copied failure module runs once per failing iteration, in the iteration's child flow; with `skip_failures` its result is that iteration's entry, and the flow's own failure module does not run |
| `fx-35` | [FX-35](../docs/semantics/execution.md#fx-35): a deployed flow started through the run API without `skip_preprocessor=true` runs its preprocessor first. Its result `5` becomes the flow input `{"value": 5}`; by the rule, an object is the input as it is, and `null` becomes `{}` |
| `fx-36` | [FX-36](../docs/semantics/execution.md#fx-36): an empty flow returns its input, and normal completion the last module's result. A clean stop returns the stopping module's result, an error stop without a failure module the `EarlyStopError`, and a failed `branchall` its branches' array |
| `fx-37` | [FX-37](../docs/semantics/execution.md#fx-37): `early_return` affects only the run-and-wait answer; the flow keeps running, and its own result is unchanged |
| `fx-38` | [FX-38](../docs/semantics/execution.md#fx-38): canceling a flow while its step runs kills the step, and ends the flow as canceled with the `Canceled` error, without running the failure module |
| `fx-v6` | [FX-V6](../docs/semantics/execution.md#fx-v6): a failure module after an error stop reads the stopped module by computed index from storage without a `previous_result` binding, and from memory with one. A failed Deno step is deferred: the CE image ships no Deno |
| `fx-16`, `fx-17`, `fx-18` | [FX-16](../docs/semantics/execution.md#fx-16): identity with direct input and as a passthrough. [FX-17](../docs/semantics/execution.md#fx-17): a skipped step's passthrough, a step skipped only on its retry, and a skipped step, whose `stop_after_if` does not run. [FX-18](../docs/semantics/execution.md#fx-18): a mocked step, `stop_after_if` on the mock value, a false `skip_if` beside a mock |
| `fx-19`, `fx-20`, `fx-21` | [FX-19](../docs/semantics/execution.md#fx-19): the clean stops, the error stop's value and its matrix of retry, `continue_on_error` and the failure module. [FX-20](../docs/semantics/execution.md#fx-20): stops in a `branchone` body. [FX-21](../docs/semantics/execution.md#fx-21): a throwing `stop_after_if` |
| `fx-23` to `fx-29`, `fx-26-delays` | [FX-23](../docs/semantics/execution.md#fx-23) to [FX-29](../docs/semantics/execution.md#fx-29): `sleep`, the step timeout, a timeout tolerated and retried, the retry schedule's phases with `delays` read from the job times, argument reuse, `retry_if`, and the flow-wide retry counter. Retry delays of hours are checked at engine unit level instead ([internal/engine](../internal/engine/README.md)) |
| `fx-36-http`, `fx-37-branches` | [FX-36](../docs/semantics/execution.md#fx-36)'s HTTP response, every run submitted through run and wait (`submit: run_wait_result`); [FX-37](../docs/semantics/execution.md#fx-37)'s early return inside branches |
| `fx-40`, `fx-40-hash`, `fx-40-cases`, `fx-40-raw` | [FX-40](../docs/semantics/execution.md#fx-40), `cache_ttl` on steps, flows and collapsed loops ([Q-FX-17](../docs/semantics/execution.md#q-fx-17)). Each cached call returns its own `WM_JOB_ID`, so a cache hit shows as a result that names another job's site. `fx-40-hash` pins the body's script by its deployed hash, which windmill-lite cannot supply, so its runs stay unsupported |
| `fx-42`, `fx-42-decode`, `fx-42-notation`, `fx-42-monitor` | [FX-42](../docs/semantics/execution.md#fx-42): the arguments file after `$jsonvar:` resolution ([Q-FX-16](../docs/semantics/execution.md#q-fx-16)), decoding, number notation, and child results that the backend cannot decode, which leave the flow waiting for the server's monitor (the `long` configuration) |
| `fx-49` | [FX-49](../docs/semantics/execution.md#fx-49) ([Q-FX-11](../docs/semantics/execution.md#q-fx-11)): a flow-level `skip_expr` that the loop at module 0 flips, with a collapsed and a two-step body, and to a throw. `oracle.setup: each_run` resets its variable |
| `fx-52-flow-timeout` | [FX-52](../docs/semantics/execution.md#fx-52): a flow's own `timeout`; a subflow's timeout fails its caller's step. Also a `concurrent_limit` of 0 ([OF-16](../docs/semantics/openflow.md#of-16)) |
| `fx-13-identity` | [FX-13](../docs/semantics/execution.md#fx-13): a mocked value that carries `wm_failure`, and a tolerated marker that the next identity job receives, fail the identity job, and the flow stops there |
| `fx-18-rest` | The remaining single cases of [Q-FX-18](../docs/semantics/execution.md#q-fx-18), one run each, listed in the rows below |
| `fx-18-rest`: `fx1a`, `fx4a`, `fx4_ids` | Two steps end with the last one's result ([FX-1](../docs/semantics/execution.md#fx-1)). A sleep on the last step keeps the earlier result, and a run's first status lists the generated `-v` step ([FX-4](../docs/semantics/execution.md#fx-4)) |
| `fx-18-rest`: `fx5a`, `fx5_root`, `fx11a`, `fx11b` | A subflow by path skips its preprocessor, which the run API runs ([FX-5](../docs/semantics/execution.md#fx-5)). `flow_env` reaches a transform, in a loop body too, and a missing `$var:` in it fails ([FX-11](../docs/semantics/execution.md#fx-11)) |
| `fx-18-rest`: `fxv1a`, `fxv1b`, `fxv2a` | A throwing `stop_after_all_iters_if` fails with an `EarlyStopError` ([FX-V1](../docs/semantics/execution.md#fx-v1)). A stop's handler sees its error, while `results.a.x` reads the stored success ([FX-V2](../docs/semantics/execution.md#fx-v2)) |
| `fx-30-due` | [FX-30](../docs/semantics/execution.md#fx-30): a retry that is due at the last module runs before `continue_on_error` tolerates the failure |
| `fx-38-pickup` | [FX-38](../docs/semantics/execution.md#fx-38): a subflow step, a Bun step and a retried subflow step, each canceled while it waits to start, behind a sleep or a retry delay |
| `fx-47-null` | [FX-47](../docs/semantics/execution.md#fx-47): a thrown `null`. The message quotes a path under the worker's directory, which differs between oracle sessions, so the fixture masks it |
| `fx-48-jsonb` | [FX-48](../docs/semantics/execution.md#fx-48), [FX-50](../docs/semantics/execution.md#fx-50), [FX-51](../docs/semantics/execution.md#fx-51): push failures at the jsonb boundary. A NUL and an unpaired surrogate generated in arguments, at the root's pickup and at a later transition; a result whose surrogate the completion insert refuses after its retries; `$jsonvar:` texts that the decoder refuses |

#### flows/control-flow and flows/cf

| Fixture | What it encodes |
|---|---|
| `cf-2` to `cf-15` | One fixture per finding of [CF-2](../docs/semantics/control-flow.md#cf-2), [CF-3](../docs/semantics/control-flow.md#cf-3), [CF-4](../docs/semantics/control-flow.md#cf-4), [CF-5](../docs/semantics/control-flow.md#cf-5), [CF-6](../docs/semantics/control-flow.md#cf-6), [CF-7](../docs/semantics/control-flow.md#cf-7), [CF-8](../docs/semantics/control-flow.md#cf-8), [CF-9](../docs/semantics/control-flow.md#cf-9), [CF-10](../docs/semantics/control-flow.md#cf-10), [CF-11](../docs/semantics/control-flow.md#cf-11), [CF-12](../docs/semantics/control-flow.md#cf-12), [CF-13](../docs/semantics/control-flow.md#cf-13), [CF-14](../docs/semantics/control-flow.md#cf-14), [CF-15](../docs/semantics/control-flow.md#cf-15): for loops, while loops and branches. The contested [CF-12](../docs/semantics/control-flow.md#cf-12) cases of [Q-CF-14](../docs/semantics/control-flow.md#q-cf-14) are recorded as the reference settled them: a sequential loop that breaks on a body error stop, with no handler and `skip_failures: false`, fails, and the enclosing flow fails without running its next step |
| `cf-16` to `cf-18`, `cf-20`, `cf-29`, `cf-v1` to `cf-v5` | [CF-16](../docs/semantics/control-flow.md#cf-16) to [CF-18](../docs/semantics/control-flow.md#cf-18), [CF-20](../docs/semantics/control-flow.md#cf-20), [CF-29](../docs/semantics/control-flow.md#cf-29), [CF-V1](../docs/semantics/control-flow.md#cf-v1) to [CF-V5](../docs/semantics/control-flow.md#cf-v5), deployed through the API. They show the copied failure module in a loop body, subflows by path, a skipped loop's passthrough, independent branch copies, and a subflow's pickup error. An empty container skips its `stop_after_all_iters_if`, while its own `stop_after_if` still stops the flow. They also show recovery counted as failed by sequential containers and as succeeded by parallel ones, stored child nodes' handler ids, and the fast path's eligibility |
| `cf-21` to `cf-28`, `waits` | [CF-21](../docs/semantics/control-flow.md#cf-21) to [CF-28](../docs/semantics/control-flow.md#cf-28): a loop's `skip_if` evaluated once ([Q-CF-6](../docs/semantics/control-flow.md#q-cf-6)), sleeps, timeouts and retries of containers ([Q-CF-7](../docs/semantics/control-flow.md#q-cf-7)), iterator errors, job paths in bodies, `squash`, the virtual identity of a body. Also cancellation inside containers; `waits` holds waits and retries inside containers |
| `subflow-retry` | A retry on a flow step that runs a flow by path ([CF-17](../docs/semantics/control-flow.md#cf-17)), recorded without an expectation first so that the recordings decide; also a retry on a while loop and on a `branchone` ([CF-22](../docs/semantics/control-flow.md#cf-22)). Each failed attempt's subflow job is at the step's `r:<k>` |
| `pull` | A flow created through the API as written and read back with the CLI's `sync pull` (`oracle: {deploy: api, pull: true}`) |
| `cf-1-30` | [CF-1](../docs/semantics/control-flow.md#cf-1)'s deployed half and [CF-30](../docs/semantics/control-flow.md#cf-30), each run by path, so that the run API applies the preprocessor; a run request that the API refuses |
| `cf-7-ai`, `cf-7-parallel`, `cf-7-release` | [CF-7](../docs/semantics/control-flow.md#cf-7): an `ai` parallelism that holds every iteration until the run is canceled. A failed parallel loop under `continue_on_error`, whose message embeds start times and is masked. On two workers, parallelism 2 over four iterations of different lengths, whose start and end times a last step reads |
| `cf-16-tolerated` | A loop's own `continue_on_error` and retry around a body whose failure its copy of the flow's handler handles, and a forwarded error stop that ignores the loop's `continue_on_error` ([CF-16](../docs/semantics/control-flow.md#cf-16), [CF-12](../docs/semantics/control-flow.md#cf-12)) |
| `cf-17-recursive` | [CF-17](../docs/semantics/control-flow.md#cf-17): a flow that calls itself by path through a `branchone` until `n` reaches 0 |
| `cf-21-throwing` | [CF-21](../docs/semantics/control-flow.md#cf-21): a container's throwing timeout and parallelism, and a container timeout that reads an `iter` that the enclosing flow's input has |
| `cf-22-retried` | [CF-22](../docs/semantics/control-flow.md#cf-22): a retried parallel loop with parallelism 1, whose iteration 0 fails first and whose iteration 1 completes last |
| `cf-23-branchone`, `cf-23-retry` | [CF-23](../docs/semantics/control-flow.md#cf-23): a `branchone` aborted at a later transition, where storage holds the iterator's error while the parent sees the branch's carried value; and one aborted while chaining, retried by a retry object whose `retry_if` sees the carried value |
| `cf-25-squash`, `cf-25-runtime` | [CF-25](../docs/semantics/control-flow.md#cf-25): `squash` on a loop with a sleep and on a parallel loop; `ai` parallelism and `squash` on the CE image ([Q-CF-19](../docs/semantics/control-flow.md#q-cf-19)), with one-step, two-step and Bash bodies, beside the same loops without `squash` |
| `cf-27-flow-timeouts` | [CF-27](../docs/semantics/control-flow.md#cf-27): a 1 s timeout on a `branchone`, a parallel `branchall` and a subflow step, whose bodies sleep 3 s |
| `cf-31-slot`, `cf-31-parallel` | [CF-31](../docs/semantics/control-flow.md#cf-31): containers as the failure module: sequential loops, a one-item loop and a `branchone`; and a parallel loop, whose flow only the server's monitor ends (the `long` configuration) |
| `flows/cf/cf-19` | `results` lookups across containers ([CF-19](../docs/semantics/control-flow.md#cf-19)), and the contested cases of [Q-CF-16](../docs/semantics/control-flow.md#q-cf-16) |

#### flows/je

| Fixture | What it encodes |
|---|---|
| `shortcuts` | The shortcut and read-route cases that name a predecessor ([JE-8](../docs/semantics/expressions.md#je-8), [JE-9](../docs/semantics/expressions.md#je-9) and others), and the key-order discriminator ([The key-order discriminator](#the-key-order-discriminator)) |
| `sites`, `errors`, `waits` | Sequential expression sites; failure-module transforms and error routes ([JE-35](../docs/semantics/expressions.md#je-35)); `sleep`, `timeout` and `retry_if` sites and retried arguments |
| `loops`, `branches`, `scopes` | For-loop cases; `branchone`, `branchall` and while-loop cases; cases across scopes |
| `providers`, `je-38-env-refs` | Variable helpers and `flow_env` and `$var:` resolution; what a step's input transforms store from `flow_env` ([JE-38](../docs/semantics/expressions.md#je-38)), deployed through the API |
| `shortcut-texts` | The text that the shortcuts answer for a value read from storage, quoted by a `timeout`'s parse error, with unit cases that restate them |
| `transform-order`, `transform-order-api` | The evaluation order of a step's input transforms ([JE-23](../docs/semantics/expressions.md#je-23)), in the CLI's name order and, created through the API, as written |
| `je-11-rewrite` | [JE-11](../docs/semantics/expressions.md#je-11): the `variable()` call rewrite on non-ASCII text, and an insertion inside a multi-byte character, which fails |
| `je-15-stringify` | [JE-15](../docs/semantics/expressions.md#je-15): a replaced `JSON.stringify` supplies a transform's result text; a text that is not JSON fails the step's push at jsonb |
| `je-20-providers` | [JE-20](../docs/semantics/expressions.md#je-20), [JE-42](../docs/semantics/expressions.md#je-42): `resource()` and the provider texts, with the resources and variables of `setup.yaml` |
| `je-21-object`, `je-open` | [JE-21](../docs/semantics/expressions.md#je-21): a thrown object, whose address in the message is masked; the thrown primitives, the `typeof` list of [JE-1](../docs/semantics/expressions.md#je-1), and [JE-50](../docs/semantics/expressions.md#je-50) to [JE-52](../docs/semantics/expressions.md#je-52) end to end |
| `je-49-deep` | [JE-49](../docs/semantics/expressions.md#je-49): a flow input nested 127 levels deep, read by a transform |
| `je-51-chain` | [JE-51](../docs/semantics/expressions.md#je-51): a job chain that never ends, at a module-0 transform, for about 70 s (the `long` configuration) |

#### flows/languages

| Fixture | What it encodes |
|---|---|
| `bun-results` | A Bun step's outcome in a sequential flow: a value as stored ([WR-13](../docs/semantics/languages.md#wr-13)), a key that held `undefined` ([WR-14](../docs/semantics/languages.md#wr-14)), a thrown string ([FX-47](../docs/semantics/execution.md#fx-47)), and a timeout ([WR-37](../docs/semantics/languages.md#wr-37)) |
| `codebase`, `codebase-cases`, `codebase-outside` | Codebase scripts ([SY-8](../docs/semantics/openflow.md#sy-8)) in ESM and CommonJS entries ([WR-47](../docs/semantics/languages.md#wr-47), [WR-50](../docs/semantics/languages.md#wr-50)): esbuild's options, assets ([WR-49](../docs/semantics/languages.md#wr-49)), the runtime select and the Node version, and an entry that reaches outside the sync tree |
| `wr-16` | Result streaming ([WR-16](../docs/semantics/languages.md#wr-16), [Q-WR-15](../docs/semantics/languages.md#q-wr-15)): a Bun step whose result is an async iterable streams it as `WM_STREAM:` log lines, which the worker joins into the result; a line that a step prints itself is merged into an object result |
| `wr-28`, `wr-28-lock` | Static and dynamic imports of workspace scripts ([WR-28](../docs/semantics/languages.md#wr-28)), created through the API in path order; a script whose deploy fails at lock time on CE `v1.862.0` |
| `native`, `native-codebase`, `native-timeout`, `wr-40` to `wr-46`, `wr-43-stack`, `wr-45-useragent` | Native steps (`//native` and `nativets`) in the `native` configuration ([Q-WR-21](../docs/semantics/languages.md#q-wr-21)): values, throws, fetches of the sink, codebase scripts under `//native`, and the in-process timeout |
| `import-base-inline` | The import base of inline scripts ([WR-29](../docs/semantics/languages.md#wr-29)) |
| `import-base`, `wr-22-envs` | [WR-22](../docs/semantics/languages.md#wr-22): a CommonJS codebase script's environment, and a single-file codebase script whose `main.js` keeps the CRLF line ends of the CLI's upload ([WR-49](../docs/semantics/languages.md#wr-49)); script `envs` and the Bun cache directory |
| `wr-54-validation` | [WR-54](../docs/semantics/languages.md#wr-54): handler errors and `// schema_validation`: wrong types, `null` for a required and an optional parameter, a string outside a literal union, and a missing or `null` element or property |
| `wr-55-processes` | [WR-55](../docs/semantics/languages.md#wr-55): leftover processes and process reuse, read back through the jobs API; the pids are masked |
| `wr-v2` | [WR-V2](../docs/semantics/languages.md#wr-v2): the empty-result `InternalErr` of a script that exits 0 without writing a result, as a plain job and as a flow step |

#### flows/client-api

| Fixture | What it encodes |
|---|---|
| `job-env`, `identity` | The job variables of inline and path steps ([WA-4](../docs/semantics/client-api.md#wa-4)); the job identity: the state path ([WA-5](../docs/semantics/client-api.md#wa-5)), the base URL variables ([WA-2](../docs/semantics/client-api.md#wa-2)), the token, and the root lookup by fetch ([WA-14](../docs/semantics/client-api.md#wa-14)) |
| `providers`, `whole-value` | The platform services that a step reads, from `setup.yaml` ([WA-40](../docs/semantics/client-api.md#wa-40)): arguments the worker resolved, the stock client's reads by fetch, and resolution failures; resources whose whole value is one reference string ([WR-5](../docs/semantics/languages.md#wr-5)) |
| `user-state` | Flow user state by the stock helpers' calls ([WA-14](../docs/semantics/client-api.md#wa-14)): the root lookup, the state routes at the root, a script job's 404 and `null`, a parallel loop sharing the root's map, and the text that a value comes back as |
| `state`, `state-client` | State and the resource routes behind it ([WA-13](../docs/semantics/client-api.md#wa-13)): each answer of create, `update_value`, delete, `exists` and the reads. A counter across iterations and across runs; values jsonb refuses; the paths a create accepts; two creates racing; values up to 32 MiB. Then the stock client's state helpers |
| `state-parallel` | Parallel writers and readers of one state on two workers: every read is one whole written value, and the last completed write stays |
| `resource-writes` | The resource writes beside state: other types, an unknown type, another type's entry, and the create options the stock client never sends |
| `dispatch` | The runs that a step dispatches, polled as `waitJob` polls ([WA-15](../docs/semantics/client-api.md#wa-15), [WA-V2](../docs/semantics/client-api.md#wa-v2)), on two workers. Its `roots` list the dispatched targets |
| `result-polls` | The result polls of jobs that a flow created ([WA-V2](../docs/semantics/client-api.md#wa-v2), [WA-19](../docs/semantics/client-api.md#wa-19)), on two workers: a completed step, a failed sub-flow's flow job and a running sibling, each polled three ways while the sibling waits. windmill-lite answers them with `result_polls_for_all_jobs` |
| `flow-path` | The runnable paths that a body step sees ([WA-6](../docs/semantics/client-api.md#wa-6)) |
| `job-reads`, `job-reads-parallel` | The job reads of a live run ([WA-19](../docs/semantics/client-api.md#wa-19)): a step reads its own job, its ancestors and the jobs their statuses name, the error answers, and what `no_logs` and `no_code` change; parallel containers read while their children run |
| `job-reads-large` | Large results and arguments, stored at 89,999 and 90,000 bytes after PostgreSQL's compression, either side of where Windmill replaces them |
| `job-reads-ended` | `jobs_u/get` and `jobs_u/get_args` on a job of a run that has ended, which windmill-lite answers 501, a [named deviation](../docs/design/0001-equivalence-contract.md#5-named-deviations). `TestEndedRunJobRead` checks it, and the flow suite does not require the run |
| `facade-answers` | The facade's error answers ([WA-14](../docs/semantics/client-api.md#wa-14), [WA-19](../docs/semantics/client-api.md#wa-19), [WA-V2](../docs/semantics/client-api.md#wa-v2)), secrets and `job_id` on the variable and resource routes ([WA-11](../docs/semantics/client-api.md#wa-11), [WA-12](../docs/semantics/client-api.md#wa-12)), and the answers to variable writes |
| `raw-requests` | Input as stored ([FX-48](../docs/semantics/execution.md#fx-48), [FX-51](../docs/semantics/execution.md#fx-51), [Q-FX-7](../docs/semantics/execution.md#q-fx-7)), from run requests that a step sends with bodies the harness's arguments cannot carry |
| `object-storage` | [WA-16](../docs/semantics/client-api.md#wa-16): the stock client's `writeS3File` with no workspace storage configured |
| `driven` | What only the API shows of runs that a step starts and waits for, on two workers: oracle, OpenFlow and language cases, and a restart, which windmill-lite answers as the Community Edition does |

#### flows/openflow, flows/stub and flows/host

| Fixture | What it encodes |
|---|---|
| `openflow/of-3-native`, `openflow/of-deployed` | The native switch ([OF-3](../docs/semantics/openflow.md#of-3)); deployed values ([Q-OF-1](../docs/semantics/openflow.md#q-of-1)), checked in a recording manifest against each recording's deployment evidence |
| `openflow/of-drivers`, `openflow/flow-script-off` | Steps that run flows through the API (previews), and a flow whose first lock fails, on two workers; flows with flow scripts turned off, in the `flow_script_off` configuration |
| `stub/retry_loop` | A retry inside a loop: iterations fail and succeed by their `replay` tables, and a run exhausts its retries ([FX-26](../docs/semantics/execution.md#fx-26), [CF-5](../docs/semantics/control-flow.md#cf-5)) |
| `stub/collapsed` | A collapsed body ([CF-8](../docs/semantics/control-flow.md#cf-8)), each iteration at `i:<n>/m:book`, whose stub keeps a `Date` parameter through a local alias ([WR-V3](../docs/semantics/languages.md#wr-v3)) |
| `stub/nested` | Nested loops whose bodies do not collapse, the inner one reading the outer iteration as `iter_parent` ([CF-3](../docs/semantics/control-flow.md#cf-3)) |
| `stub/scopes` | A named subflow and a parallel loop under one outer run, and two concurrent outer runs (`r` and `t`, group `rt`), with every call's job variables ([OR-V6](../docs/semantics/oracle.md#or-v6), [Q-OR-10](../docs/semantics/oracle.md#q-or-10)) |
| `stub/codebase`, `stub/outside` | Stub mode on codebase scripts, which stay codebase scripts; and on an entry that reaches outside the sync tree, whose stubbed copy carries `shared/` |
| `stub/subflow_retry` | A retried flow step: each subflow step has one outcome per job, counted across the attempts, and a skipped step's identity job takes no place in the count ([CF-17](../docs/semantics/control-flow.md#cf-17), [FX-17](../docs/semantics/execution.md#fx-17)) |
| `host/provided` | windmill-lite only: provided sites that the host's providers replace, the run-time half of the provided-site policy case of the ADR [IR, loader and validation](../docs/design/0002-ir-and-loader.md). The oracle would run the real semaphore subflow, so the runs hold expectations written from the ADRs. In `granted` the provider's value stands in, and the next step reads it; in `refused` the site's `stop_after_if` stops on a static value. In `declined` the run ends latched with `provider_declined`; in `failed` the provider's error fails the site |

## Expression cases

An expression case is context-free: an input transform or the `skip_if`
predicate of a flow's first step. A case supplies only `flow_input` and
`flow_env`. At the first step `previous_result` is the flow input and the
previous id is empty, and an expression may read them, as
[exprs/je-6.yaml](exprs/je-6.yaml) does.

Each `exprs/<finding>.yaml` holds the cases of one finding, and is named
after it in lower case, as its recordings are. The fields are
in [internal/conformance](../internal/conformance/README.md#expression-cases).

```yaml
finding: JE-7
scope: core
flow_input: '{"n":12345678901234567890,"a.b":"lit","a":{"b":"nested"}}'
cases:
  - spec: JE-7/a
    provenance: oracle-observed
    title: a top-level key keeps full precision
    expr: flow_input.n
    expect: {value: '12345678901234567890'}
```

A case that supplies a predecessor, a handler or a container as one of its
fields is refused (`contextual_case_needs_flow_fixture`): it belongs in a flow fixture, such as
those under `flows/je`.

### Add an expression case

1. Add the case to `exprs/<finding>.yaml`, or create the file with its
   `finding` and `scope`.
2. Give it a `spec`: the catalogue case, or the finding with a `variant`
   that tells the case apart. Give it a `title`, a `provenance`, and a
   `site` when it is not an input transform: `static` for a literal JSON
   value, `skip_if` for the predicate. Then the `expr` or `static`, its
   `flow_input` or `flow_env` if they differ from the file's, and the
   `expect` that the reference predicts.
3. Check it on windmill-lite, from the repository root:
   `bin/wltest check conformance/exprs/<finding>.yaml`, with `wltest` built,
   the build helper's dependencies, and Bun 1.4.3. `check` lifts the file as
   `record` does, and checks a case with no recording against its claims.
4. Open a pull request. The Oracle workflow records the cases, and writes
   the recordings into its artifact, not into your checkout: take
   `recorded/conformance/exprs/recorded/<finding>/` from the artifact of the
   `Oracle record (workers1)` job, copy it to
   `conformance/exprs/recorded/<finding>/` from the repository root, and
   commit it.

`wltest record` lifts each file into generated one-step flows, one per case,
whose module `b` runs a sink script with an input `x`. That is the
expression for a transform case, and the literal value for a `static` case.
A `skip_if` case's expression becomes `b`'s `skip_if`, and `x` is `null`.
The generated tree is never committed
([internal/conformance](../internal/conformance/README.md#lifting)).

### What is checked where

- `exprs/deferred.yaml` lists the catalogue cases of the expression
  findings, of [CF-19](../docs/semantics/control-flow.md#cf-19) and of
  [CF-V6](../docs/semantics/control-flow.md#cf-v6) that no expression case and no flow fixture encodes, and those
  recorded only in part, each with the reason and where it is checked.
  `TestBatchCoverage` ([internal/oracle](../internal/oracle/README.md#tests))
  requires every catalogue case of those findings to be an expression case, a
  fixture's run or deferred.
- The pipeline cases of [internal/expr](../internal/expr/README.md) restate
  recorded cases and give cases of their own at evaluator level, which its
  tests run on the engine; [internal/engine](../internal/engine/README.md) checks a few at engine
  level.
- Cases whose run would restart a flow are enterprise-only (`EE`), because
  the Community Edition has no restart.

### The key-order discriminator

The key-order example of
[Where semantics live](../docs/design/0003-semantics-placement.md) for
[JE-8](../docs/semantics/expressions.md#je-8) cannot be seen in a recorded
argument, because the job API stores arguments as jsonb, which reorders
keys. `flows/je/shortcuts` keeps it as a unit case (`je8-order-transform`: the
evaluator returns the producer text `{"b":1,"a":2}`, whose stored form is the
recorded `{"a":2,"b":1}`). It observes it on the oracle at the `timeout`
site, whose parse error quotes the evaluated text (`je8-order-timeout`, with
the database route as its control in `je8-order-timeout-db`).

## Claims and unit cases

Runs of flow fixtures, and the runs lifted from expression cases, carry
assertions beyond `expect`. They are `claims` (partial assertions at a JSON
Pointer), `delays` (lower bounds on the time between two jobs) and `unit`
(the run's expressions restated at unit level). Two more fields,
`not_found_locs` and `copies`, are hints for the comparison with
windmill-lite. Their fields are in
[internal/conformance](../internal/conformance/README.md#claims-and-unit-cases).

| Field | On the oracle's side | On windmill-lite's side |
|---|---|---|
| `claims` | `go test ./internal/oracle` checks them against the committed recordings (`TestRecordingsSatisfyClaims`) | `wltest check` checks them on its own observation |
| `delays` | The same test, on the recordings' job times | `wltest check`, on the trace's job times |
| `unit` | `TestUnitCasesMatchRecordings` ties each case's inputs to the recording | `go test ./internal/expr` runs the cases on the evaluator |
| `copies`, `not_found_locs` | Not used | `wltest check` uses `copies` when it observes a run, and `not_found_locs` when it compares |

The recorder checks none of them, so a contradicted claim still leaves its
recording to inspect. A claim holds on both sides, so it states only what
both write: a location suffix, which windmill-lite never writes, stays in
the recording.

## Add a case at a file level

The decode, jsonb, load, compile and build levels take the same steps:

1. Open the finding's file, such as `ir/cf-8.json`, or create it with its
   `finding` and `cases`.
2. Add the case: its `spec`, `provenance` and `title`, its `given` input
   and its `expect`, by the level's field table below.
3. If a catalogue case of the finding gets no case at this level, list it
   in the level's deferred file, with the reason and where it is checked.
   Otherwise the level's catalogue test fails.
4. Run the level's tests ([Run the tests](#run-the-tests)), such as
   `go test -run '^TestConformanceIR' ./internal/ir`. The build level needs
   Bun; the jsonb level compares with PostgreSQL only with
   `WL_JSONB_PSQL`.

The other levels have steps of their own:
[protocol](#add-a-protocol-case), [plugins](#plugin-cases),
[hooks](#hook-cases), [flow](#add-a-flow-fixture),
[expr](#add-an-expression-case) and [step](#add-a-step-case).

## OpenFlow decode cases

A decode case gives a flow `value` as JSON, and the decision of Windmill's
effective decoder ([OF-2](../docs/semantics/openflow.md#of-2)): a rejection,
or an accepted value and parts of its decoded form. The decoded form is
described in [internal/openflow](../internal/openflow/README.md). Each
`openflow/of-<n>.json` file holds the cases of one finding:

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

| Field | What it holds |
|---|---|
| `spec` | The catalogue case id |
| `title`, `note` | What the case shows, and why it reads as it does (`finding` is the file's) |
| `provenance` | The catalogue's provenance tag, which the runner checks |
| `variant` | Tells several inputs of one catalogue case apart |
| `scope` | Set only where it differs from the finding's; also allowed at file level |
| `part` | `decode` when the catalogue case has a run-time or deploy half that is checked elsewhere; `note` says which |
| `deviation` | The [named deviation](../docs/design/0001-equivalence-contract.md#5-named-deviations) whose behaviour the expectation shows |
| `given` | The flow value, as JSON |
| `expect.reject` | `kind` (`flow_value`, `module`, `retry_base`, `step_path`, …), `contains` and `not_contains` (substrings of the message), and `not_prefix` |
| `expect.accept` | `at` (a JSON Pointer to expected JSON in the decoded form), `matches` (a JSON Pointer to a regular expression for a string), and `absent` (JSON Pointers that must not resolve) |

`openflow/deferred.json` lists the catalogue's OF cases that are not decode
cases.

## Stored-form cases

A stored-form case gives a value's producer text, as a step, QuickJS-ng or a
static transform writes it. It expects the value's stored text: the text that
PostgreSQL `jsonb` gives back without its output spaces, or the rejection
that leaves it with no stored form. Both texts are described in
[internal/jsonval](../internal/jsonval/README.md). The catalogue cases are
flow cases; a stored-form case checks only the texts, and names where the
rest of the case is checked.

Each `jsonb/<finding>.yaml` file holds the cases of one finding. Texts are
YAML single-quoted strings, in which a backslash is literal and only a quote
is doubled, so the JSON text stays byte for byte as written:

```yaml
finding: JE-44
cases:
  - spec: JE-44/b
    provenance: oracle-observed
    title: an older step is read from the database (jsonb order)
    read: stored
    given: '{"bbb":1,"a":2,"cc":3}'
    expect:
      keys: [a, cc, bbb]
      stored: '{"a":2,"cc":3,"bbb":1}'
```

This is an excerpt of [jsonb/je-44.yaml](jsonb/je-44.yaml). The complete case
also has `rest_in`, which names where the route half, the engine's read of
the older step, is checked.

| Field | What it holds |
|---|---|
| `spec` | The catalogue case id, or the finding id for a case that encodes the finding's rule beyond its catalogue cases |
| `variant` | Names a case that has only a finding id |
| `title`, `note` | What the case shows, and why it reads as it does; the file can carry a `note` too |
| `provenance` | For a catalogue case, the catalogue's tag, which the runner checks; for a rule case, the finding's, or `policy` where the case pins bytes that the rule leaves open or PostgreSQL behaviour that the reference does not state |
| `scope` | The catalogue's scope tag, where it gives one |
| `read` | `producer` or `stored`: the text that the catalogue case's consumer reads, by the routing table of the ADR [JSON at step boundaries](../docs/design/0005-json-step-boundaries.md) |
| `given` | The producer text |
| `expect.stored` | The exact stored text |
| `expect.keys` | The top-level keys of the `read` text, in order, as the catalogue expects them |
| `expect.reject` | `kind` (`nul`, `surrogate`, `numeric`, `syntax`) and, optionally, `database`: PostgreSQL's message |
| `rest_in` | For a catalogue case, where the rest of it is checked |
| `note` | Why the case reads as it does |

`jsonb/deferred.yaml` lists the catalogue cases of these findings that are
not stored-form cases. CI's `jsonb (PostgreSQL)` job also compares the
stored texts with a real PostgreSQL ([docs/ci.md](../docs/ci.md#the-ci-workflow)).

## Sync-folder load cases

A load case gives a [sync folder](../docs/glossary.md#sync-folder) as a map
of files, and what the loader of
[internal/syncfolder](../internal/syncfolder/README.md) makes of it. That is
whether the folder opens, which items it lists or filters out, and, for named
items, parts of the record read or the error. Each `syncfolder/sy-<n>.json` or
`pa-<n>.json` file holds the cases of one finding
([SY-1](../docs/semantics/openflow.md#sy-1) to
[SY-20](../docs/semantics/openflow.md#sy-20),
[SY-V1](../docs/semantics/openflow.md#sy-v1) to
[SY-V4](../docs/semantics/openflow.md#sy-v4), and the PA findings):

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

Load cases keep the decode cases' `spec`, `variant`, `provenance`, `scope`, `part`, `title` and
`note`, without `deviation` or `expect.accept` and `expect.reject`. Their own fields:

| Field | What it holds |
|---|---|
| `spec` | The catalogue case id, or the finding id alone for a case that extends the catalogue |
| `policy` | The windmill-lite policy of the [equivalence contract](../docs/design/0001-equivalence-contract.md) or of [IR, loader and validation](../docs/design/0002-ir-and-loader.md) whose behaviour the expectation shows, where it differs from the CLI's |
| `given.files`, `given.hex_files` | The folder: file paths and their text, or their bytes in hex for text that is not UTF-8 |
| `given.start` | The working directory that the folder is opened from, relative to the case; by default the case's root |
| `given.step` | A flow path and a module id, whose by-path target `expect.target` describes |
| `expect.load` | The error's `kind` and message substrings (`contains`), when the folder does not open |
| `expect.root` | The sync root, relative to the case |
| `expect.paths`, `expect.filtered` | Per item kind, the exact sorted paths listed, or filtered out |
| `expect.items` | Per item kind and path: `at` (a JSON Pointer into the item's JSON form) and `absent`, or `error` (`kind`, `contains`) |
| `expect.target` | The same, for `given.step`'s target |
| `expect.warnings`, `expect.not_warnings` | Substrings that some warning holds, or that no warning holds |
| `expect.skipped` | Files listed as recognised and not loaded, with their kind |
| `expect.check_ok`, `expect.check_error` | `Check` succeeds, or fails with an error of that kind |

The runner writes each case to a temporary directory, with a `.git`
directory at its root, so that the upward search for `wmill.yaml` stays in
the case. A case without `wmill.yaml` thus has its start directory as the
root ([SY-13](../docs/semantics/openflow.md#sy-13)).
`syncfolder/deferred.json` lists the catalogue's SY and PA cases that are not
load cases here. There is no `sy-9.json`: the cases of the YAML dialect are in
[internal/yamlx](../internal/yamlx/README.md).

## Compile cases

A compile case gives a sync folder, as a load case does, and optionally a
host contract and build options. It expects what the compiler of
[internal/ir](../internal/ir/README.md) makes of them: the errors, or parts
of the compiled program ([IR, loader and validation](../docs/design/0002-ir-and-loader.md)).

Most catalogue cases that they encode are flow cases whose expectation
depends on what a deploy applies: container defaults, the native switch,
virtual steps, collapse. A compile case checks that part (`part: compile`),
and its `note` says where the run is checked.

```json
{
  "finding": "CF-8",
  "cases": [
    {
      "spec": "CF-8/c",
      "provenance": "oracle-observed",
      "part": "compile",
      "note": "…",
      "title": "a loop at step 0 collapses",
      "given": {"files": {"wmill.yaml": "defaultTs: bun\n", "f/x/main.flow/flow.yaml": "value:\n  modules:\n    - id: l\n      value:\n        type: forloopflow\n        iterator: {type: static, value: [1, 2, 3]}\n        modules:\n          - id: x\n            value:\n              type: rawscript\n              language: bun\n              content: \"export async function main(x: number) { return x }\"\n              input_transforms: {}\n"}},
      "expect": {"steps": {"f/x/main m:l": {"at": {"/forloop/collapse": true}}}}
    }
  ]
}
```

This is an excerpt of [ir/cf-8.json](ir/cf-8.json). The loader requires a
`note` on a case with `part: compile`, saying where its run half is checked;
the complete case gives it.

Compile cases take the load cases' `given.files`, `given.hex_files`, `policy` and case fields, but not
`given.start`, `given.step` or the load level's `expect` fields, and add `deviation`. Their own fields:

| Field | What it holds |
|---|---|
| `provenance` | As the catalogue tags the case, or `policy` for windmill-lite's own rule (with a `policy` field naming it). A `disputed` case's `note` says which reading it encodes, and why the design fixes it ([findings that are not settled](../docs/design/0001-equivalence-contract.md#6-findings-that-are-not-settled)) |
| `part` | `compile` when the catalogue case's run half is checked elsewhere |
| `given.contract` | The host contract, as the YAML text of its file; without one, every flow and script is a root |
| `given.options` | Build options, such as `{"strict": true}` |
| `expect.errors` | Errors that must all be among those that the compile reports: `kind` and message substrings (`contains`) |
| `expect.at`, `expect.absent` | JSON Pointers into the program's JSON form |
| `expect.flows`, `expect.no_flows` | Per flow path, pointers into that flow; the flows that must not be in the program (opaque or unreached) |
| `expect.steps` | Per `"<flow path> <site>"`, pointers into that module, and `script` for its inventory entry. The site is a site path with its iteration segments left out (`m:l/m:x`, `m:o/b:default/m:c`, `s:failure`) |
| `expect.lists` | Per `"<flow path>"` or `"<flow path> <site>"` (a loop, or a branch), the site segments of that module list in order, occurrences and virtual steps included |
| `expect.scripts` | Per inventory id, pointers into that entry |

`ir/deferred.json` lists the catalogue cases of these findings that are
neither compile cases nor accounted for at the decode or load level. Its test
also fails unless every case that `openflow/deferred.json` or
`syncfolder/deferred.json` defers to `conformance/ir` is a compile case.

## Protocol cases

The harness protocol ([Harness protocol](../docs/design/0004-harness-protocol.md))
is windmill-lite's own, so these cases carry provenance `policy`. They pin the
Go side ([internal/harness](../internal/harness/README.md)) and each harness
([harnesses/bun](../harnesses/bun/README.md)) to one set of frames. Each file
names the design section that it encodes (`design`) and, except the `host/`
files, the findings it relies on (`relies_on`); a case that also encodes a catalogue case says so in
`spec`, such as the version check's `WR-3/a`.

### Add a protocol case

1. **A frame case:** add it to `protocol/frames.json` in the file's layout,
   or run `go test ./internal/harness -run TestFrameCases -args -update`
   after it, which writes the `payload` and `frames` from Go's encoder.
2. **An exchange:** add `protocol/exchanges/<case>.json`, a transcript of the
   steps below on the scripts of `protocol/bundle/`, and run
   `go test -run '^TestExchanges$' ./internal/harness` with `WL_BUN`.
3. **A host case:** add it to `protocol/host/versions.json` or
   `workdir.json`, and run both sides: `go test ./internal/harness` and
   `bun test` in `harnesses/bun`.

### frames.json

`protocol/frames.json` holds the codec's golden frames.
`go test ./internal/harness -run TestFrameCases -args -update` writes
`payload` and `frames` from Go's encoder; both codecs then check every case,
unless `sides` limits it. The file is laid out as `-update` writes it, and the
Go test fails when it is not: write a new case by hand in that layout, or run
`-update` after it.

| Kind | Fields | Check |
|---|---|---|
| encode | `message` (JSON) or `message_text` (a payload in any key order), `payload` | The message decodes and encodes to `payload`, which encodes to itself again; `raw` names values whose bytes must survive exactly |
| split | an encode case, plus `split` (`stream`, `chunk_bytes`) and `frames` | The payload splits into exactly those part frames, which reassemble to it |
| sequence | `frames`, `messages` | The frames, read in order, decode and reassemble to the messages |
| reject | `bytes_hex` or `frames`, optional `max_frame` and `max_message`, `error` | Reading raises the protocol error code `error` |

### exchanges

`protocol/exchanges/<case>.json` are transcripts that the Go tests play
against the real harness, on the synthetic bundle in `protocol/bundle/`. The
bundle holds a `manifest.json` whose SHA-256 is the bundle digest. Per script
under `scripts/bun/`, it holds a module written by hand that takes named
parameters, and the `.meta.json` signature that the build helper would
record for it. Each
step does one thing:

| Step | What it does |
|---|---|
| `send` | Encode a message canonically and send it, as part frames when it is larger than a frame. `fill` stands in for a long string, and `split` sends it as small parts with one whole frame between them |
| `send_text` | Send one frame with this exact payload |
| `expect` | The next message from the harness matches, keys in order, within `within_ms` when given. `parts` is how many part frames carried it. Messages whose type `ignore` lists are dropped while it waits |
| `quiet_ms` | No message arrives for that long |
| `exit` | The process exits with this code, and sends nothing more |
| `stdout_contains` | The process's standard output contains the text |
| `release` | Create the file of this name in the harness's working directory, which ends a `gate` step that waits for it |

Expected values may use placeholders: `<any>`, `<string>`, `<int>`, `<pid>`
(the harness's pid), `<bundle>` (the fixture bundle's digest) and `<c*N>` (a
string of `N` copies of `c`).

- **No timing.** A transcript never relies on a step outlasting the frames
  that follow it. A call that must still run when a later frame arrives is a
  `gate` step (`scripts/bun/gate.mjs`), which runs until a `release` step
  creates its file.
- **Logs.** What a step writes through `console`, `process.stdout` or
  `process.stderr` arrives as `log` frames ahead of the frame that reports
  its outcome, so a transcript expects them, with `<any>` for the text of a
  stack.
- **Fresh mode.** `welcome-fresh`, `fresh-one-invocation` and `fresh-exit` run
  a harness in `fresh` mode, which serves one invocation and exits with it.

### host

- **`protocol/host/versions.json`** holds the cases of the pairing between a
  host SDK's version and an engine's ([Host adapters and typed methods](../docs/design/0009-host-adapters.md)):
  whether they pair. An unstamped `dev`, `(devel)` or empty version pairs with anything, a
  leading `v` is ignored,
  and two stamped releases pair when they are equal. Both sides run them: the
  engine's `harness.VersionsPair`, which refuses a host with
  `version_mismatch` in the handshake (`go test ./internal/harness`), and the
  SDK's `versionsPair`, which refuses such an engine's welcome (`bun test`).
- **`protocol/host/workdir.json`** holds host mode's working-directory cases,
  which both sides check: the host SDK before it starts the engine
  (`checkWorkdir`, run by `bun test`), and the engine in the handshake
  (`harness.CheckWorkdir`, run by `go test ./internal/harness`). Each case
  lays out a fresh directory with `cwd/` (the host's working directory),
  `other/`, `link` (to `cwd/`) and `file`, sets the modes that it names
  (skipped as root), and passes `workdir`. `expect` is the refusal's code
  (`workdir_not_writable`, `workdir_mismatch`) or `null`.

## Plugin cases

`plugins/hooks.yaml` holds the host-plugin cases of the ADR
[Host hooks](../docs/design/0008-host-hooks.md). Its header says that they
are windmill-lite's rules for its hosts, not Windmill's. The format is in
[internal/conformance](../internal/conformance/README.md#host-plugin-cases).
The cases cover:

- every hook that a plugin answers over `host_call`, a mandatory provided
  site, and an `around` wrapper around a step;
- the errors, declines and timeouts that latch a run;
- fatal error names from the plugin or from the run, the run's deadlines,
  one that has already passed included, the cause that a canceled run
  names, and a run's own hooks and budgets as the run carries them;
- settlement: a run settles only once its hooks and providers finish; and
  later runs of a flow (`then`);
- step code's `wl.latch` and `wl.signal`;
- a pinned run whose plugin state stays whole in one process, while a run
  that it dispatches starts a second.

The same cases run through a TypeScript plugin in `warm` processes and in host
mode, and through Go callbacks built from the same plugin, except a case with
`go: false`, which runs only through the TypeScript plugin. A case with a
`pool` of its own runs only in `warm` processes, in a runtime of its own.

To add a plugin case:

1. Add it to [plugins/hooks.yaml](plugins/hooks.yaml): a unique `id`, its
   `spec` and `title`, its `flow`, its `plugin` (`{}` for none), and its
   `run` and `expect`, by the format in
   [internal/conformance](../internal/conformance/README.md#host-plugin-cases).
2. Set `go: false` if the plugin fails or starts slowly, which only the
   TypeScript plugin can do.
3. Run `go test -run '^TestPluginFileLoads$' ./internal/conformance`, then,
   with `WL_BUN`, `go test -run '^TestPluginCases$' ./wl` and
   `go test -run '^TestHostPluginCases$' ./cmd/windmill-lite`.

## Hook cases

`hooks/latch-precedence.yaml` holds the cases of the host's signals of the
ADR [Host hooks](../docs/design/0008-host-hooks.md) against Windmill's own
continue decision ([FX-2](../docs/semantics/execution.md#fx-2)): retry,
`continue_on_error`, `skip_failures` and the failure module. The signals are
the latch, cancellation and deadlines, fatal error names, halts and the
clock's record mode. A host's signal has a channel of its own, which no error
tolerance of the flow swallows.

- Each case runs one synthetic flow of identity modules, at `f/hooks/case`,
  through [wl](../wl/README.md), with no harness. The host answers the
  modules' identity jobs where a case needs a failure or a value.
- A case has a `name`, a `spec`, a `flow` and `args` (the flow input). The
  file's header lists the other fields: the host's signals (`fail`, `value`,
  `latch_at_start`, `latch_at_end`, `cancel_at_start`, `cancel_at_end`,
  `canceled`, `fatal_error_names`, `halt_before`, `halt_after`, `clock`,
  `pre_write_deadline_ms`) and the expectation (`status`, `latched`, `ran`,
  `not_ran`, `result`, `deviations`).
- The header says that these are windmill-lite's rules, so nothing is
  recorded on the oracle.

To add a hook case:

1. Add it to the topic's file, such as
   [hooks/latch-precedence.yaml](hooks/latch-precedence.yaml): a `name`, a
   `spec`, a `flow` and `args`.
2. Give the host's signals and the expectation, from the fields that the
   file's header lists.
3. Run `go test -run '^TestHookCases$' ./wl`, which needs no Bun.

## Step cases

A step file holds the step cases of one finding, or of a named deviation that
has no finding, and is named after it in lower case. Each case is one script, with any more
`scripts` that its runs invoke, and its runs, in order. The format is in
[internal/conformance](../internal/conformance/README.md#step-cases), and how
`wltest check` runs them is in
[cmd/wltest](../cmd/wltest/README.md#step-cases).

```yaml
finding: WR-12
scope: core
cases:
  - spec: WR-12/b
    provenance: source-derived
    title: an arrow preprocessor receives the non-underscore values, integer-like keys ascending
    step_id: preprocessor
    script: |
      export const preprocessor = (x, y) => ({ x, y });
    runs:
      - args: '{"2":"two","1":"one","_ENTRYPOINT_OVERRIDE":"preprocessor"}'
        expect: {status: success, result: {x: one, y: two}}
```

- **Modes.** In `warm` mode, one harness process serves a case's runs, so a
  later run sees what an earlier one left in memory. In `fresh` mode each run
  has a process of its own. A native step runs in a realm of its own each
  time, in either mode.
- **The observation** is the step's stored value,
  `{"status": "success" | "failure", "result": …}`: the result, or the
  failure value ([WR-21](../docs/semantics/languages.md#wr-21)).
- **Languages.** Only `bun` has a language directory: native steps run in the
  Bun harness.

### Add a step case

1. Add the case to `steps/bun/<finding>.yaml`, or create the file with its
   `finding` and `scope`.
2. Write the script and its runs. Each run has its `args`, and either its
   own check (`expect`, `claims`, or another non-empty check field, such as
   `calls: []`), or, where `warm` and `fresh` differ, a `by_mode` with
   exactly a `warm` and a `fresh` check. A case with
   `by_mode` names its `deviation`, as
   [steps/bun/wa-31.yaml](steps/bun/wa-31.yaml) does.
3. Run it in both modes, from the repository root with `wltest` built, the
   build helper's dependencies and Bun 1.4.3 (and `WL_NODE` for a CommonJS
   codebase case): `bin/wltest check conformance/steps/bun/<finding>.yaml`
   and `bin/wltest check --mode fresh conformance/steps/bun/<finding>.yaml`
   ([cmd/wltest](../cmd/wltest/README.md#check-a-step-case-locally)).
4. If a catalogue case of the finding stays without a step case, list it in
   `steps/bun/deferred.yaml` with the reason and where it is checked.

### Step files for deviations and services

| File | What its cases show |
|---|---|
| [`d5.yaml`](steps/bun/d5.yaml) | The native boundary: a native step runs in a realm of its own, and an object that the boundary does not track is reported (`native_untracked`) when the invocation settles |
| [`d9.yaml`](steps/bun/d9.yaml), [`d11.yaml`](steps/bun/d11.yaml), [`d12.yaml`](steps/bun/d12.yaml) | Process-level errors in a shared process; an intercepted `process.exit`, which fixes the step's outcome at the call while the call goes on; [host-provided modules](../docs/glossary.md#host-provided-module) |
| [`d13.yaml`](steps/bun/d13.yaml) | Where Node and the Bun harness agree, and where they differ, on a CommonJS codebase script. `wltest check --node /path/to/node conformance/steps/bun/d13.yaml`, or the same with `WL_NODE` set, runs each such run on Node as well, as Windmill runs such a bundle ([WR-30](../docs/semantics/languages.md#wr-30), [WR-50](../docs/semantics/languages.md#wr-50)) |
| [`d18.yaml`](steps/bun/d18.yaml) | Shared code ([Shared module code](../docs/design/0013-shared-module-code.md)): its bundle is built with shared code, so `warm` runs each script's shared-layout module, with its own instance of its group's closure, and `fresh` runs the per-script bundles. Its cases' scripts share a dependency module, and many invoke a second script in the same process (`runs[].script`); and show that each step module keeps module state of its own |
| [`wr-5.yaml`](steps/bun/wr-5.yaml) | A script reads its own arguments from `args.json` in its working directory, which the harness writes before each invocation ([WR-5](../docs/semantics/languages.md#wr-5)): run after run of one warm process, whose directory every invocation shares, and in each fresh process. The file holds no `_MODULES` or `_TEMP_SCRIPT_REFS`, and every other member keeps its text and place |
| [`wr-12.yaml`](steps/bun/wr-12.yaml) | Alternate entrypoints and arrow exports ([WR-12](../docs/semantics/languages.md#wr-12)), as the [harness protocol](../docs/design/0004-harness-protocol.md) invokes them |
| [`wr-6.yaml`](steps/bun/wr-6.yaml), [`wr-7.yaml`](steps/bun/wr-7.yaml), [`wr-8.yaml`](steps/bun/wr-8.yaml) | The run halves of build cases: argument binding (`wr-6.yaml`), a missing export (`wr-7.yaml`), and `Date` hydration (`wr-8.yaml`) |
| [`wr-50.yaml`](steps/bun/wr-50.yaml) | Built as a bundle of its own: the case whose `package.json` asset declares `"type": "module"`, and `//native` codebase cases. The harness extracts every asset of a step file's bundle into its working directory, and such a file there makes Bun import every CommonJS module beside it as Node does |
| [`wa-14.yaml`](steps/bun/wa-14.yaml) | Flow user state ([WA-14](../docs/semantics/client-api.md#wa-14)): a flow step's case runs in its case's flow run, so a later run reads what an earlier one wrote; also the web framework's rejections |
| [`wa-19.yaml`](steps/bun/wa-19.yaml), [`wa-v2.yaml`](steps/bun/wa-v2.yaml) | The root lookup ([WA-19](../docs/semantics/client-api.md#wa-19)): a step's own job, an id with no job, and an id that is not a UUID. The result poll ([WA-V2](../docs/semantics/client-api.md#wa-v2)): a UUID that names no job, an id that is not a UUID, and the step's own job under another workspace's path, which reads as unknown |
| [`wa-16.yaml`](steps/bun/wa-16.yaml) | The object store's job helpers for explicit keys ([WA-16](../docs/semantics/client-api.md#wa-16)), each case with a store of its own; and an upload that asks the server for a key, answered 501, which fails the step as `UnsupportedPlatformCall` |
| [`wa-15.yaml`](steps/bun/wa-15.yaml) | The dispatch restriction: a step case's bundle has no root to dispatch, so every dispatch is refused with `dispatch target not in bundle: <path>`, which fails the step as `UnsupportedPlatformCall` ([restrictions](../docs/design/0001-equivalence-contract.md#5a-restrictions)) |
| [`wa-31.yaml`](steps/bun/wa-31.yaml) | A warm process reaches the platform only through `fetch`, a restriction that its `by_mode` shows |
| [`wa-10.yaml`](steps/bun/wa-10.yaml), [`wa-11.yaml`](steps/bun/wa-11.yaml), [`wa-12.yaml`](steps/bun/wa-12.yaml), [`wa-42.yaml`](steps/bun/wa-42.yaml), [`wa-v3.yaml`](steps/bun/wa-v3.yaml) | The resource and variable reads, against the variables and resources of a case's `setup`, or with no setup, such as `wa-10.yaml`'s 404 for an absent resource; `wa-11.yaml` also the variable update behind `setVariable` |
| [`wa-2.yaml`](steps/bun/wa-2.yaml), [`wa-5.yaml`](steps/bun/wa-5.yaml), [`wa-41.yaml`](steps/bun/wa-41.yaml) | The base URL variables ([WA-2](../docs/semantics/client-api.md#wa-2)); the state path ([WA-5](../docs/semantics/client-api.md#wa-5)); the 501 policy on the stock client ([WA-41](../docs/semantics/client-api.md#wa-41)) |

Every deviation file's name is the deviation's own name in the
[named deviations](../docs/design/0001-equivalence-contract.md#5-named-deviations).

## Build cases

A build case gives a sync folder and one or more Bun scripts, builds them with
[internal/bundle](../internal/bundle/README.md) and the
[Bun build helper](../harnesses/bun/build/README.md), and checks what the
build records for each script. That is the signature of each export, its
exports, annotations, bundler target, workflow-as-code markers, the
workspace scripts it inlined and the host-provided modules it uses, or the
problem it reports.

A case can also run the built modules on the pinned Bun, and check what their
exports return. Some cases check only the build half of a catalogue case
whose run belongs to the step suite: `part` says so, and `rest_in` names
where the rest is checked. Most cite a finding or a deviation alone.

Each `build/<finding>.json` holds the cases of one finding, or of a named
deviation that has no finding:

```json
{
  "finding": "WR-8",
  "cases": [
    {
      "spec": "WR-8/a",
      "provenance": "source-derived",
      "title": "a local alias to Date",
      "part": "build",
      "rest_in": "…",
      "given": {"source": "type Ts = Date; export function main(d: Ts) { return d instanceof Date; }"},
      "expect": {"signatures": {"main": {"params": [{"name": "d", "date": true}]}}}
    }
  ]
}
```

This is an excerpt of [build/wr-8.json](build/wr-8.json). Its `rest_in` names
where the run half, the hydration of the argument, is checked: the step
file [steps/bun/wr-8.yaml](steps/bun/wr-8.yaml).

The fields beyond those of the decode cases:

| Field | What it holds |
|---|---|
| `rest_in`, `policy` | Where the rest of a catalogue case is checked; the windmill-lite policy that the expectation shows |
| `given.source`, `given.path`, `given.language` | The script, its Windmill path (default `f/case/main`), written as `<path>.ts`, and the language that a deploy records |
| `given.inline`, `given.flow` | The script is inline. `flow` gives `path`, `module` and `explicit_path`, from which the import base is computed ([WR-29](../docs/semantics/languages.md#wr-29)) |
| `given.entries` | More scripts, each with `id` and the fields above |
| `given.files` | Other folder files; every `.ts` file is a script at its path without the extension |
| `given.dependencies`, `given.packages` | A script's npm dependencies, and the synthetic packages (`name@version` and their files) that a registry the test serves holds; the runner makes the lock |
| `given.host_modules` | Host-provided modules: `id`, `key`, and a folder `file` or a `package` with its `dependencies` |
| `expect.signatures` | Per export, `params` exactly, or `error_contains` |
| `expect.exports`, `expect.annotations`, `expect.target`, `expect.workflow_as_code`, `expect.imports`, `expect.host_modules` | What the build recorded; `annotations` lists the flags that are set |
| `expect.format`, `expect.boundary`, `expect.transpile_error` | The module's format, its boundary, and a native script's transpile error |
| `expect.problem` | The problem's `kind` and message substrings |
| `expect.bundle_contains`, `expect.bundle_not_contains`, `expect.first_line` | Text of the bundled module |
| `expect.entries` | The same expectations for other scripts, by id |
| `expect.provided`, `expect.host_module_order`, `expect.order_problem`, `expect.warnings` | Host-provided modules: exports, imports or problem by id; the registration order or the cycle; warning codes |
| `expect.run` | `env` replaces `process.env`. `register` lists the host-provided copies to register, in order, and `register_error` is a substring of the error that ends the registration. `calls` are each an `entry`, an `export` (default `main`), positional `args`, and the `result` or an `error` substring |

### Whole-folder cases

A whole-folder case gives `given.folder` instead: a whole sync folder,
`wmill.yaml` included, which the runner builds with `bundle.Build`, the build
of `windmill-lite build`. So the rules that the build checks on the compiled
program apply: workflow-as-code is refused
([WR-32](../docs/semantics/languages.md#wr-32)), and so is a script that
imports a package without a lock
([WR-26](../docs/semantics/languages.md#wr-26)).

| Field | What it holds |
|---|---|
| `given.folder` | The folder's files, by path from the sync root |
| `given.outside` | Files outside the sync root, by path from it, each starting with a single `../`: a codebase entry's `relative_path` may lie there ([WR-49](../docs/semantics/languages.md#wr-49)) |
| `given.contract` | The host contract's text; without it, every flow and script is a root |
| `given.locks` | By script file, the npm dependencies whose lock the runner makes from `given.packages` and writes as `<path>.script.lock` |
| `given.shared_code` | Build with shared code |
| `expect.build_problems` | Every problem that the build reports: an `item` (`script <path>`, `<flow> step '<id>' (script inline-` for an inline step, `host module <path>`, or `host modules`), a `kind`, and message substrings in `contains`. Empty for a clean build, which must then verify |
| `expect.entries` | For a clean build, by script id: what its metadata in the bundle records (`format`, `target`, `exports`, `signatures`, `host_modules`), and text of its module (`bundle_contains`, `bundle_not_contains`, `first_line`) |
| `expect.assets` | For a clean build, the files under the bundle's `assets/`, by path, with their content |
| `expect.warnings` | For a clean build, when given, the codes of every warning that the build reports, in order |
| `expect.host_module_order` | For a clean build, the ids of the manifest's `host_modules`, in registration order |
| `expect.run` | For a clean build, as above: each call's `entry` is a script id, and its module the bundle's `.cjs`, `.mjs` or `.native.js` |
| `expect.shared` | For a build with shared code: each script's record by id (`closure:<label>`, `fallback:<reason>` or `none`); the dependency modules that each closure names `once`, and text it `contains` or `not_contains`; the manifest must name shared code and the closures |
| `expect.shared.modules`, `expect.shared.per_script_identical` | By script id, text that its shared-layout module `contains` or `not_contains`; and a check of every file but the metadata against a build without shared code |

### The build files

- `wr-47.json`, `wr-48.json`, `wr-49.json`: how the pinned CLI bundles a
  codebase script ([WR-47](../docs/semantics/languages.md#wr-47)); a falsy
  `customBundler` leaves the bundle to esbuild
  ([WR-48](../docs/semantics/languages.md#wr-48)); assets
  ([WR-49](../docs/semantics/languages.md#wr-49)).
- `wa-31.json`: the environment rewrite, its codebase variants in CommonJS
  and ESM output.
- `d12.json`: host-provided modules through their shims, those outside the
  folder included; the shared copy of a listed codebase script; the refused
  import of one by a Bun script; the registration order by load-time imports.
- `d18.json`: shared code: closure membership and the `import.meta`
  parameter, build groups, installations and targets, fallbacks, and native
  scripts. An import of a module that the runtime has built in causes no
  fallback, and `node:` before a name of Bun's own, such as `node:ws`, names
  no built-in module and still does.
- `d5.json`, `wa-33.json` and the WR files not named above (`wr-6`, `wr-7`,
  `wr-8`, `wr-26` to `wr-29`, `wr-31`, `wr-32`, `wr-38`, `wr-39`, `wr-v1`,
  `wr-v3`, `wr-v4`) hold each finding's build cases. They cover signatures
  and hydration, import transforms, annotations, bundler targets, and the
  native scripts' build.
- `annotations.json`, a shared case file with `relies_on` instead of a
  finding: the leading-comment annotation cases
  ([WR-31](../docs/semantics/languages.md#wr-31),
  [SY-V3](../docs/semantics/openflow.md#sy-v3)): a `source` and the `flags` it
  sets. Both readers of the rule run them: the Go loader's
  `syncfolder.HasAnnotation` and the Bun build helper, in
  `go test ./internal/bundle`, and the helper's parser in `bun test`.

`build/deferred.json` lists the catalogue cases of these findings that are not
build cases.

## Decisions where the reference is silent

| Question | Choice | Why | Pinned by |
|---|---|---|---|
| Which level does a case go to? | The lowest level that observes the rule; a flow fixture only where the rule needs a whole run: on the oracle, or, for windmill-lite's own policy such as provided sites, on windmill-lite alone, where the oracle skips it | A lower level runs without the oracle, and often without Bun, and fails closer to the cause ([Where semantics live](../docs/design/0003-semantics-placement.md)) | Each level's catalogue test |
| When does a catalogue case go to a deferred file? | When its finding has a file at a level, and the case cannot be checked there. The entry says why, and where it is checked | The catalogue test can then require every case of the finding to be accounted for | `TestConformanceIRCatalogue` and the other catalogue tests, `TestBatchCoverage` |
| Can a recorded run stay outside the runs windmill-lite must pass? | Yes. The oracle records what Windmill does; `flowsuite.json` and the manifests' gates decide which runs windmill-lite must pass, and `later` marks a case it need not pass yet | Recording comes before the work that implements a behaviour | `TestRecordingBatches`, the flow suite's tests |
| Which cases are `policy`? | windmill-lite's own rules: the protocol, the plugin and hook cases, provided sites, and the choices of the ADRs. The oracle never records them | Windmill has no such behaviour to observe | The protocol, plugin and hook tests |
| What happens to a case that needs a restart? | It is `EE`, and deferred | The Community Edition has no restart | [exprs/deferred.yaml](exprs/deferred.yaml) |

# internal/oracle

**Current state:** implemented (milestone E2 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md), extended by
the recording batches E3c and E3b). This README
covers `internal/oracle/`, its vendored `openapi/` directory and its
`testdata/`. The package is the oracle harness of
[ADR 0011](../../docs/design/0011-differential-testing.md): it records flow
fixtures on an unmodified Windmill CE `v1.767.0`, which runs in public CI
only, through the `wltest record` command ([cmd/wltest](../../cmd/wltest/README.md))
and the [oracle workflow](../../.github/workflows/oracle.yml). That workflow
records the OR-20 smoke fixture ([conformance/flows/oracle/smoke](../../conformance/flows/oracle/smoke))
and the fixtures of the recording batches, E3c's and E3b's under
[conformance/flows/execution](../../conformance/flows/execution), and the
expression cases of batch E3a, which `wltest record` lifts into generated
flows ([conformance](../../conformance/README.md#expression-cases)). E3c
also extended the session and the walker (below), E3b the walker and the
batch test, and E3a the session and its own batch tests. Comparing
windmill-lite against a recording (`wltest check`, milestone E1) and stub
mode (E4a) do not exist yet.

## A session

One `Session.Run` is one oracle session ([OR-18](../../docs/semantics/oracle.md#or-18)):

1. **Preflight.** Refuse when an earlier `wl-oracle` compose project was not
   torn down. Measure free space on the filesystem of Docker's data root and
   pull a missing image only under the fail-closed policy of
   [OR-30](../../docs/semantics/oracle.md#or-30): at least 8 GiB free and
   5 GiB left after the estimated need; a watchdog aborts the pull below
   5 GiB, nothing resumes, and a settled reading below 5 GiB after the pull
   removes the images it pulled. Then require each image's `RepoDigests` to
   carry its pinned digest ([OR-1](../../docs/semantics/oracle.md#or-1)),
   and, whether or not anything was pulled, at least 5 GiB free before the
   oracle starts; images that were already present stay.
2. **Start.** Start the effect sink, write `compose.yaml` (Postgres on tmpfs
   and the CE image in standalone mode, pinned by digest, never pulled by
   compose, ports on `127.0.0.1`; [OR-4](../../docs/semantics/oracle.md#or-4))
   and `docker compose up -d`. A two-worker session enables the `workers2`
   profile ([OR-5](../../docs/semantics/oracle.md#or-5)). The disk watchdog
   covers the start and the rest of the session.
3. **Boot.** Wait for `/api/version`, which must be exactly `CE v1.767.0`,
   then for a healthy status with enough workers; log in as the seeded
   superadmin ([OR-6](../../docs/semantics/oracle.md#or-6)).
4. **Per fixture.** Refuse a fixture that needs more workers than the
   session has, and a run whose committed recording names another oracle
   version, image or CLI than the pins (`recording_identity_mismatch`,
   [OR-V5](../../docs/semantics/oracle.md#or-v5)/b), before anything is
   deployed; to re-record against a new pin, delete the recording. Create a
   fresh workspace `wl-<6 hex>-<n>`, which is deleted again after the
   fixture's runs, or after its deployment failed, with a context of its own
   so that a canceled session still cleans up
   (`DELETE /workspaces/delete/{id}`): CE refuses a third
   workspace besides `admins` ("You have reached the maximum number of
   workspaces (2 outside of default workspace 'admins') without an
   enterprise license"), and a workspace that cannot be deleted ends the
   session. Optionally run
   `folder add-missing` and `generate-metadata` in the fixture's sync tree
   (the drift gate of [OR-8](../../docs/semantics/oracle.md#or-8)), and
   always in a generated tree, which has no committed metadata; push a
   copy of the tree with the CLI; apply `setup.yaml` after the push
   ([OR-31](../../docs/semantics/oracle.md#or-31)); pass the deployment gate
   ([OR-9](../../docs/semantics/oracle.md#or-9)) and check each deployed
   flow's module ids against the tree (OR-8/b).
5. **Per run.** Pre-assign the root job id, open the sink for it, submit
   asynchronously and poll `get_result_maybe` until completion, never
   `run_wait_result` ([OR-10](../../docs/semantics/oracle.md#or-10)); a run
   that times out is canceled, so it cannot hold the worker. Walk the run,
   normalise it, and repeat as asked (the observations must be equal,
   OR-14/a). Compare the observation with the run's `expect`, under the
   fixture's compare options, and with its committed recording, exactly
   (two oracle observations are never compared as a subset), then write the
   recording as the session's policy says.
6. **Teardown.** Record the containers' cgroup memory peaks where the host
   exposes them, `docker compose down -v`, and require that no container,
   volume or network with the project label is left (OR-18/a). Teardown
   always runs, and the disk watchdog stops the session below 5 GiB.
   Docker and CLI commands run in a process group of their own, killed as a
   whole when the session is canceled, so a launcher such as `npx` cannot
   leave the CLI running and block the teardown.

Every refusal is a `*Refusal` whose text is the JSON the reference gives it,
for example `{"error":"fixture_requires_workers","need":2,"have":1}`.

## The pieces

| File | What it does |
|---|---|
| `pins.go` | The image pins (tag and multi-arch index digest), the version string, the CLI package, the compose project and the seeded superadmin. |
| `compose.yaml` | The oracle's compose file, embedded. A test checks it against the pins, the hygiene rules and `OracleEnv`. |
| `docker.go` | The docker runner. It drops `COMPOSE_*` and `DOCKER_DEFAULT_PLATFORM` from the environment, which would change the topology behind the compose file. `CheckDockerArgs` allows only the commands the harness needs (`pull`, `image inspect`/`rm`, `compose up`/`down`/`ps`/`logs`, listings); `push`, `tag`, `build`, `commit`, `save` and the rest are refused, so the harness itself can never derive or re-push an image ([OR-3](../../docs/semantics/oracle.md#or-3)). |
| `disk.go` | The OR-30 policy: `PullNeed`, `CheckPull`, the `Watch` watchdog and `SettledFree`. |
| `client.go` | The REST client, written against the Apache-2.0 `openapi/openapi.yaml`. Arguments and results stay raw JSON text, because member order and number spelling are observable ([OR-13](../../docs/semantics/oracle.md#or-13)); full values come only from `get_args` and `get_result_maybe` ([OR-V1](../../docs/semantics/oracle.md#or-v1)). `Capture` records exchanges for replay. |
| `cli.go` | Runs the published `windmill-cli@1.767.0` from npm as a black-box tool, with its own HOME, npm cache and config directory and no inherited environment. Its `--version` must report exactly `1.767.0`. |
| `proc_unix.go` | Gives each docker and CLI command its own process group, killed on cancellation, with a bound on draining its pipes. |
| `deploy.go` | Boot, login, workspaces, `setup.yaml`, the deployment gate and the deployed-module check. |
| `sink.go` | The effect sink ([OR-16](../../docs/semantics/oracle.md#or-16), [OR-V6](../../docs/semantics/oracle.md#or-v6)): it keeps an effect only when its `root` is the submitted root, and answers anything else with `{"error":"foreign_effect"}`. |
| `walk.go` | The walker ([OR-11](../../docs/semantics/oracle.md#or-11)). |
| `record.go` | The session, `Observe` (walk to normalised observation) and provenance. |
| `lint.go` | The repository checks: `LintImages` and `GuardManifests`. |
| `openapi/` | `openapi.yaml` and `LICENSE-APACHE`, copied unmodified from Windmill `v1.767.0`, with their [NOTICE](openapi/NOTICE). Apache-2.0, not Unlicense. |

### The CLI is a tool, not a dependency

[OR-8](../../docs/semantics/oracle.md#or-8) makes the published CLI the
reference loader, because it is an independent implementation of folder to
API. The build team never reads, depends on or copies it: no manifest names
it, and the manifest guard rejects it. Running a published tool is use
([licensing.md](../../docs/semantics/licensing.md), design README item 2).
Everything else the harness does (boot, login, workspaces, setup, the gate,
runs, capture) goes through the Apache-2.0 API surface.
`TestClientUsesTheOpenAPISurface` calls every `Client` method and requires
each request's method, path and JSON body properties to be in the vendored
schema.

## Sites

The walker names every site with the typed site path of ADR 0011
(`internal/conformance`). It reads each job's actual status, never the job
kind ([OR-28](../../docs/semantics/oracle.md#or-28)), and the fixture's own
flow definitions (decoded with `internal/openflow`) to tell user modules
from generated ones, to name collapsed bodies, and to tell a `branchall`
from a loop.

| What ran | Its site, and what it records |
|---|---|
| a module with a job (a script, a subflow by path) | `m:<id>`: the job's status (canceled, else skipped, else success or failure; [OR-V2](../../docs/semantics/oracle.md#or-v2)), its stored args and result, the module's own status where it differs, its `skipped` flag. A subflow's modules are below it, `m:sub/m:x`. |
| a module without a job | `m:<id>`, `not_run`, nothing fetched. |
| a module with a nil job | its entry type, with `ran: false` (an empty `branchone` branch, a failed predicate). |
| a loop or `branchall` (`flow_jobs`, even empty) | `m:<id>`: status from the entry type, the children's results in index order as its result, `ran: false` with no children. Child `n` is `i:<n>`, or `b:<n>` for a branchall, which the fixture's definition identifies because a completed entry need not carry its `branchall` field. A child flow's modules are below it, and so is its copy of the enclosing flow's failure module, at `i:<n>/s:failure` or `b:<n>/s:failure` ([FX-5](../../docs/semantics/execution.md#fx-5), [FX-34](../../docs/semantics/execution.md#fx-34)); a collapsed iteration is the body module's own job at `i:<n>/m:<body>`, the path a non-collapsed iteration gives. |
| a `branchone` | `m:<id>`: the chosen body's status and result and `branch_chosen`; the body's modules are under `b:<n>` or `b:default`. |
| a failed attempt | `<module>/r:<k>`, in `failed_retries` order. A failure module that succeeds lists the flow's earlier failed attempts again ([FX-27](../../docs/semantics/execution.md#fx-27), [FX-29](../../docs/semantics/execution.md#fx-29)), so the same job is recorded at `s:failure/r:<k>` too, as OR-11 says; its placeholder names the site the walk reached first, the failed module's attempt. |
| the failure or preprocessor module | `s:failure`, `s:preprocessor`, kept only when the flow defines one; an inline body defines its enclosing flow's failure module, a subflow by path its own. |
| an empty flow (the fixture defines no modules) | no step: its status's one `Success` entry with a nil job is not a module ([FX-3](../../docs/semantics/execution.md#fx-3)). |
| a generated `<id>-v` | omitted ([OR-V7](../../docs/semantics/oracle.md#or-v7)); a user-authored one stays. |

A container module records no args: its job's args are Windmill's
(`{"iter":…}`, the parent's flow args), not the flow's. Iteration and branch
body jobs get no record of their own, but their ids map to `i:<n>` and
`b:<n>` for placeholders. A flow status that does not match the fixture's
module list is an error, not an observation. These rules follow the shapes
oracle run 1 recorded, as the reference describes them. E2 recorded the
sequential and failure shapes of the smoke fixture; E3c recorded the
failure module (also inside loop bodies), its retries, subflows by path,
`branchall`, empty flows and cancellation, and corrected the `branchall`
and empty-flow rules above; E3b recorded skipped and identity modules,
mocks, clean and error stops, retried modules, failure modules after a
retry and at the end of the flow, and a `branchone` body, and added the
first-site rule for a repeated attempt. The other recording batches confirm
the rest.

## Normalisation and comparison

`Observe` normalises with `conformance.Normaliser` and the fixture's
`stack` and `mask` paths: walked job ids become `<job:PATH>` placeholders;
a stack becomes `<stack>` only on a failed script leaf's stored error and on
values deep-equal to it or to its inner error ([OR-V3](../../docs/semantics/oracle.md#or-v3));
only the NUL `InternalErr` loses its location to `<loc>`
([OR-27](../../docs/semantics/oracle.md#or-27)); user text that looks like
a placeholder or marker is escaped. The comparator, the escape and the
fixture format are in [internal/conformance](../conformance/README.md).

A recording is `{"observation": …, "provenance": …}`. Provenance holds the
oracle version, image and Postgres image references and ids, the CLI and its
`--version` output, the harness build, the host and CI run, the recording
time, the workers, the oracle's non-default environment
([OR-7](../../docs/semantics/oracle.md#or-7)), the repeat count, raw
timings and foreign effects. It is never compared
([OR-V5](../../docs/semantics/oracle.md#or-v5)). Recordings hold neither
carried values nor resolved arguments, which the oracle does not expose.

## Recording batches

A recording batch is one of milestones E3a to E3e of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md).
`testdata/batches/<batch>.json` holds one, and `TestRecordingBatches`
checks it on every CI run:

| Field | Meaning |
|---|---|
| `batch` | the milestone id; its row in ADR 0012 names finding ranges (`FX-6 to FX-10`) and cases (`FX-42/h`) the batch must cover |
| `findings`, `cases` | the findings whose every catalogue case is in the batch, and further single cases |
| `fixtures` | the batch's fixture directories, from the repository root; each run's own `spec` says which cases it records |
| `deferred`, `parts` | cases, or halves of recorded cases, left to a later milestone, each with the milestone and a reason |
| `prefixes` | the catalogue's prefix-only assertions, checked on a run's recorded text |
| `contradictions` | catalogue claims the oracle contradicted, with the run, pointer and recorded value the run's `expect` holds instead |
| `gates` | optional: for every run, `<fixture directory>/<run>`, the milestone whose exit gate runs it, which must need the batch merged; E3b's gates N1a, N1b, N2, NI, N3b, PF2 or F1 by the features each flow needs |

Batch E3a was built beside these manifests and has none: most of its runs
state partial `claims` rather than a whole `expect`, and half of its cases
are expression cases, whose lifted runs have no `expect` at all. Its
coverage is `conformance/exprs/deferred.yaml`, checked by
`TestBatchCoverage`, and its runs are checked by
`TestRecordingsSatisfyClaims` and the other tests of `recordings_test.go`.

## Repository checks

Two Go tests run on every pull request in the `Go` CI jobs (ADR 0011, CI
item 1):

- **Image lint** (`LintImages`, OR-3): no Dockerfile or Containerfile names
  a Windmill image at all (so none can bake it in through `FROM`,
  `COPY --from`, `RUN --mount=from=` or an `ARG` that `FROM ${…}` expands),
  and no workflow, composite action (any YAML under `.github/`), shell
  script, Makefile or justfile that names a Windmill image runs `docker push`, `docker tag`, `docker image push|tag`,
  `buildx imagetools create`, `skopeo copy`, `crane` copy commands or
  `regctl image copy`. Pulling and running the image is allowed.
- **Manifest guard** (`GuardManifests`, LIC-1/a and LIC-1/b): `go.mod` and
  `go.sum` may name only the Apache Go client among `github.com/windmill-labs/`
  modules; `package.json`, lockfiles and import maps may not name the
  AGPL-derived `windmill-parser-wasm-*` packages, `windmill-yaml-validator`,
  `windmill-utils-internal` or `windmill-cli` (Q-OF-7(a), decided no), or
  depend on the Windmill repository or an npm tarball of an excluded package;
  `Cargo.toml` may not name the Rust client or a Windmill crate, by key,
  table or `package =`; and a directory whose `NOTICE` names the Windmill
  repository may hold only the Apache schemas, their licence and the notice,
  and may cite no `backend/` path but `backend/windmill-api/openapi.yaml`.

`TestRepositoryPassesTheChecks` runs both on this repository. Directories
named `testdata`, `node_modules`, `.git` and `.jj` are not scanned.

## Tests

| Test | What it checks | Cases |
|---|---|---|
| `TestWalkCases` | the walker and `Observe` on a fake job API, one case per shape above | `testdata/walk/cases.json` |
| `TestSessionRecordsTheSmokeFixture`, `TestSessionUnstableObservation`, `TestSessionRefusals`, `TestSessionSubsetFixtureRecordsExactly`, `TestSessionCancelsATimedOutRun`, `TestSessionOutDirStaysInside`, `TestSessionRefusesARecordingOfAnotherOracle`, `TestSessionKillsAHungCLIAndTearsDown`, `TestSessionDeletesEachWorkspace`, `TestSessionFailsWhenAWorkspaceCannotBeDeleted` | whole sessions against a fake docker, a fake Windmill that runs the smoke flow and, like CE, refuses a third workspace, and this test binary as the CLI: recording, check mode, provenance never compared (OR-V5/a), a recording of another oracle refused before deployment (OR-V5/b), effects grouped by the outer root, instability, exact comparison of recordings under a subset fixture, cancellation of a timed-out run, a hung CLI killed and the stack torn down when the disk falls below the floor, `--out` kept inside its directory, three fixtures in one session with each workspace deleted, a deletion that fails ending the session after the fixture's runs with the stack torn down, and the refusals of OR-1/b, OR-1 image identity, OR-5/a, OR-30/a, OR-30/c, cached images below the floor, and a stale session | the smoke fixture |
| `TestCLIVersionExact`, `TestCLICancelKillsTheGroup` | the exact version check, and that canceling a command or `--version` kills its children promptly | — |
| `TestCommittedRecordingsNameThePinnedOracle` | every committed recording names the pinned oracle, image and CLI | `conformance/flows` |
| `TestReplayRecordedCaptures` | raw API captures of CI recordings (the smoke fixture's, one run per walker shape that E3c recorded, and every run of E3b), replayed offline through the walker and normaliser, reproduce the committed recordings; the job kinds met follow OR-28. A capture may come from a later run than its recording: the observation does not depend on job ids | `testdata/captures/` |
| `TestRefusalForms`, `TestCheckPull`, `TestWatchAbortsBelowFloor`, `TestSettledFree` | the refusal JSON and the OR-30 policy | — |
| `TestDockerAllowlist`, `TestComposeFile`, `TestImageIdentity` | the docker allowlist, the compose contract, image identity | — |
| `TestClientUsesTheOpenAPISurface` | every client call against the vendored schema | `openapi/openapi.yaml` |
| `TestSinkGroupsByOuterRoot` | OR-16/c | — |
| `TestGuardCases`, `TestRepositoryPassesTheChecks` | the image lint and the manifest guard | `testdata/guard/cases.json` |
| `TestCatalogueCoverage` | every OR and LIC-1 case of the catalogue is cited by a case, checked live by every session, or deferred to a named milestone | `testdata/coverage.json` |
| `TestSessionDeletesWorkspacesOnFailure` | a workspace whose check after creation or whose deployment gate failed is deleted too, and a workspace that cannot be deleted ends the session | the smoke fixture |
| `TestExprFilesLift`, `TestBatchFixturesDecode` | every expression file lifts into flows that the walker reads back as their cases describe them; every flow fixture's tree decodes and holds its runs' targets | `conformance/exprs`, `conformance/flows` |
| `TestRecordingsSatisfyClaims`, `TestExprCasesAgreeWithRecordings`, `TestUnitCasesMatchRecordings` | committed recordings against what their cases say (batch E3a's form): claims and delays hold, a recorded case is `oracle-observed`, an expected evaluator text has the recorded argument as its stored form, and each unit case's expression and previous id match its flow, and its flow input, previous result and database reads have the recorded texts as their stored forms | `conformance/exprs`, `conformance/flows` |
| `TestBatchCoverage` | every JE case of the catalogue, and every CF-19 and CF-V6 case, is an expression case, a flow fixture's run, or deferred with a reason and a milestone (batch E3a) | `conformance/exprs/deferred.yaml` |
| `TestRecordingBatches` | each recording batch against its ADR 0012 row: the batch covers the finding ranges and cases its row names; every catalogue case of its findings is recorded by a run of its fixtures or deferred with a milestone and a reason; every run has an `expect` and a recording of the pinned oracle that meets it; the catalogue's prefix-only assertions (`<starts "…">`) hold on the recorded text; each catalogue claim the oracle contradicted is listed with the value it recorded; a batch with `gates` names for every run a milestone that needs the batch merged | `testdata/batches/` |

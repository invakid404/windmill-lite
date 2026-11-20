# internal/oracle

**Current state:** working. The package records flow
[fixtures](../../docs/glossary.md#fixture) and expression cases on an
unmodified Windmill Community Edition (CE) `v1.862.0`. It compares each run
with its expectation and its committed
[recording](../../docs/glossary.md#recording), where the run has them, and
writes the recordings with their provenance.

It also generates the stubbed trees of stub mode and serves their replay
calls. A session needs Docker, so
recording runs only in public CI, through `wltest record` and the Oracle
workflow.

This README covers `internal/oracle/`, its vendored `openapi/`
directory and its `testdata/`.

The [oracle](../../docs/glossary.md#oracle) is that unmodified Windmill,
which windmill-lite's conformance tests take as the truth. This package is
the harness that runs it:

- **Who calls it:** `wltest record` runs a session, and `wltest stub` and
  `wltest check` use its stub generator, its copies of a
  [sync folder](../../docs/glossary.md#sync-folder)'s tree and its effect
  sink ([cmd/wltest](../../cmd/wltest/README.md)).
- **What it calls:** the case formats, the normaliser and the comparator of
  [internal/conformance](../conformance/README.md); the flow decoder of
  [internal/openflow](../openflow/README.md);
  [internal/syncfolder](../syncfolder/README.md) and
  [internal/yamlx](../yamlx/README.md) to read sync trees; Docker; and the
  published `windmill-cli`.

The [ADR](../../docs/glossary.md#adr)
[Differential testing and performance measurement](../../docs/design/0011-differential-testing.md)
defines the oracle harness. The oracle findings of the
[semantics reference](../../docs/semantics/oracle.md) state how a session
must run, and the [Oracle](../../docs/ci.md#oracle) section of the CI guide
says when the workflow records and in which sessions.

## Files

| File | What it holds |
|---|---|
| `pins.go` | The pinned images, the oracle's version, the CLI package, the version of [Bun](../../docs/glossary.md#bun) in the image, the compose project and base URL, the seeded superadmin, the reason and canceler of a run's `cancel_after_ms`, and `SessionConfigs` |
| `compose.yaml` | The oracle's compose file, embedded |
| `docker.go` | The docker runner and its allowlist |
| `disk.go`, `disk_unix.go`, `disk_other.go` | The fail-closed disk policy |
| `client.go` | The REST client, and the capture of its exchanges |
| `cli.go` | The published CLI, run as a tool |
| `redact.go` | Keeps the session token out of everything the session writes |
| `layout.go` | Copies of a sync tree |
| `proc_unix.go`, `proc_other.go` | A process group of its own for each docker and CLI command, on Linux and macOS |
| `deploy.go` | Boot, login, workspaces, `setup.yaml`, the deployment gate and the deployed-module check |
| `apideploy.go` | The API deployment |
| `sink.go` | The effect sink, which also serves the replay service and the user-agent route |
| `stub.go`, `stubts.go` | Stub mode's generator |
| `locate.go` | The `Locator` of the replay service |
| `replay.go` | The check of a stub run's calls against its walk |
| `walk.go` | The walker |
| `record.go` | The session, `Observe` and the provenance |
| `errors.go` | `Refusal` and the refusal constructors (`ErrStubUnsupported` is in `stub.go`) |
| `lint.go` | The repository checks |
| `doc.go` | The package comment |
| `openapi/` | The vendored OpenAPI description ([The vendored OpenAPI description](#the-vendored-openapi-description)) |
| `testdata/` | The walker's cases (`walk/`), raw captures (`captures/`), the stub cases (`stub/`), the repository checks' cases (`guard/`), the catalogue coverage (`coverage.json`) and the recording manifests (`batches/`) |

## Public surface

Every exported name, grouped by concern. "`wltest`" names
[cmd/wltest](../../cmd/wltest/README.md), the one caller outside the package
besides the CLI parity test of [internal/bundle](../bundle/README.md), and
the package's tests.

### Sessions and reports

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Session`, `Session.Run` | struct, method | One oracle session ([A session](#a-session)) | `wltest` |
| `Report`, `SessionReport`, `RunReport` (`Failed`) | structs | A session's outcome: its measurements and each run's outcome | `wltest` |
| `OutcomeUnchanged`, `OutcomeNew`, `OutcomeChanged`, `OutcomeUnstable`, `OutcomeExpectMismatch`, `OutcomeFailed`, `OutcomeRefused`, `OutcomeSkipped` | constants | A run's outcome ([Run outcomes](#run-outcomes)) | the session; `wltest` reads them through `RunReport` |
| `ErrNoFixtures` | variable | The error `wltest record` reports when it finds no fixture to record; `Session.Run` does not return it | `wltest` |
| `SessionConfigs`, `SessionEnvFile`, `ListenIntervalVar`, `OracleEnv` | variables, constants | The oracle's configurations and environment ([Oracle configurations](#oracle-configurations)) | the session, `TestComposeFile` |
| `Provenance`, `OracleProvenance`, `HarnessInfo`, `HostInfo`, `RunTimings`, `JobTiming` | structs | A recording's provenance ([Recordings and provenance](#recordings-and-provenance)) | the session; `wltest` fills `HarnessInfo` |
| `CheckRecordingIdentity` | function | Requires a recording's provenance to name the pinned oracle | the session, tests |

### The pinned oracle

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `WindmillRepo`, `WindmillDigest`, `WindmillImage`, `PostgresRepo`, `PostgresDigest`, `PostgresImage` | constants | The pinned images, by tag and multi-arch index digest | the session, `compose.yaml`'s test |
| `OracleVersion`, `CLIVersion`, `CLIPackage`, `BunVersion` | constants | `CE v1.862.0`, which `/api/version` must answer; the CLI `windmill-cli@1.862.0`; the Bun inside the image, a reference value that nothing reads | the session; `wltest` uses `CLIPackage`; `internal/bundle`'s parity test uses `CLIVersion` and `CLIPackage` |
| `ProjectName`, `BaseURL` | constants | The compose project `wl-oracle`, and `http://localhost:8000`, where the oracle listens | the session; `wltest check` uses `BaseURL` |
| `AdminEmail`, `AdminPassword` | constants | Windmill's public default seed of the superadmin, not secrets | the session |
| `CancelAfterReason`, `CancelAfterCanceler` | constants | The reason a run's `cancel_after_ms` cancels it with, and the canceler that Windmill then names | the session, `wltest check` |
| `Images`, `Pinned` (`CheckIdentity`) | variable, struct | The session's two images, and the check that an image carries its pinned digest | the session |
| `ImageInfo`, `InspectImage` | struct, function | What `docker image inspect` reports | the session |

### Docker, compose and disk

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Docker`, `ExecDocker` | interface, struct | Runs the docker CLI; `ExecDocker` runs the binary on `PATH`, each call checked by `CheckDockerArgs` | `wltest` |
| `CheckDockerArgs` | function | Refuses a docker command outside the allowlist ([Decisions](#decisions-where-the-reference-is-silent)) | `ExecDocker` |
| `Compose` (`Up`, `Down`, `ContainerID`), `ComposeFile`, `WriteComposeFile` | struct, functions | The compose project and its file | the session; `ComposeFile` by `TestComposeFile` |
| `DockerArch`, `DockerDataRoot`, `Leftovers`, `MemoryPeak` | functions | The server's architecture; the directory the disk policy measures; what a teardown left; a container's cgroup memory peak | the session |
| `MinFreeToPull`, `Reserve`, `TransientMargin`, `GiB` | constants | The disk policy's figures ([The disk policy](#the-disk-policy)) | the session |
| `FreeSpace`, `PullNeed`, `CheckPull`, `Watch`, `SettledFree` | type, functions | The disk policy's steps | the session |

### The REST client and the CLI

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Client` | struct | The REST client: login, version, health, workspaces and their queue count, folders, scripts, flows, resource types, resources and variables, runs, job reads, cancellation and deployment status | the session |
| `APIError` | struct | A response with an unexpected status | the session |
| `Capture`, `Exchange`, `RawCapture` | structs | Recorded exchanges, served again for a replay, and a run's raw capture | the session, tests |
| `HTTPResponse` | struct | A run-and-wait response, whatever its status | the session |
| `Health`, `Boot`, `WaitReady`, `Bootstrap`, `NewWorkspace` | types, functions | Wait for a fresh oracle, log in, and make a fixture's workspace | the session |
| `Job`, `ResultMaybe`, `FlowStatus`, `FlowStatusModule`, `FlowStatusRetry`, `BranchChosen` | structs | What the walker reads of a job and its flow status | the walker |
| `WaitJob` | function | Polls `get_result_maybe` until a job completes | the session |
| `ScriptRef`, `NewScript` | structs | A deployed script, and the body of a script's creation | the gate (`ScriptRef`), the API deployment (`NewScript`) |
| `RedactedToken` | constant | `<redacted-token>`, which replaces the session token ([The session token](#the-session-token)) | the session |
| `CLI` (`Version`, `AddMissingFolders`, `GenerateMetadata`, `Push`, `Pull`), `Target`, `DefaultCLICommand` | struct, struct, variable | The published CLI, run as a tool, and the workspace it acts on | the session, `wltest` |
| `CopyTree` | function | Copies a directory or a file to a destination that should not exist yet | `wltest`'s tests |

### Deployment

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `ApplySetup`, `CheckSetup`, `ResetSetup` | functions | Apply a fixture's `setup.yaml`, read it back, and set its variables again | the session |
| `Gate`, `Deployment`, `DeploymentStatus` | function, structs | The deployment gate, and what it saw | the session |
| `SyncTree`, `LoadSyncTree`, `CheckDeployedModules` | struct, functions | A fixture's flows by path, and the check of each deployed flow's module ids | the session, the walker |
| `DeployAPI`, `APIDeployment`, `FlowDeployment`, `PinToDeployedHash` | function, structs, constant | The API deployment and its evidence ([API deployment](#api-deployment)) | the session |
| `Layout`, `PlanLayout`, `CopyLayout` | struct, functions | Copies of a sync tree ([Copies of a sync tree](#copies-of-a-sync-tree)) | the session, `wltest` |

### Stub mode, walk and observation

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `StubTree`, `StubReport`, `StubSource` | function, struct, function | The stubbed tree and the stub of one script ([Stub mode](#stub-mode)) | the session, `wltest` |
| `CheckStubSignatures`, `RegeneratedSchemas` | functions | Compare the stub's schemas with the original's, and the committed schemas with regenerated ones | the session |
| `Locator` (`Locate`) | struct | The oracle's side of the replay service | the session |
| `Sink` (`Open`, `Close`, `Listen`, `ServeHTTP`, `Shutdown`, `Replay`), `UserAgentPath` | struct, constant | The effect sink ([The effect sink](#the-effect-sink)) | the session, `wltest check` |
| `Walker` (`Walk`), `Walk`, `Site` | structs | The walker and what it gives ([Sites](#sites)) | the session |
| `Observe` | function | A walk, its effects and its response normalised into an observation | the session |

### Refusals and repository checks

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Refusal` (`Error`, `JSON`), `RefusalField` | structs | A run or session the harness refuses, as JSON ([Refusals](#refusals)) | the session, `wltest` |
| `ErrVersionMismatch`, `ErrImageMismatch`, `ErrRequiresWorkers`, `ErrRequiresSession`, `ErrUnknownSession`, `ErrProvidedSites`, `ErrForeignEffect`, `ErrInsufficientDisk`, `ErrDiskFloor`, `ErrDiskAfterPull`, `ErrStubUnsupported` | functions | One refusal each | the session |
| `LintImages`, `GuardManifests`, `Finding` | functions, struct | The repository checks ([Repository checks](#repository-checks)) | tests |
| `RuleImageFrom`, `RuleImageRepush`, `RuleManifestDep`, `RuleVendoredPath`, `RuleVendoredFile`, `RuleVendoredInput` | constants | The rules a finding names | the checks |

## A session

One `Session.Run` is one oracle session
([OR-18](../../docs/semantics/oracle.md#or-18)): one compose stack,
bootstrapped once, a fresh workspace per fixture, and the runs recorded in
sequence, a group's runs together. Once it has tried to start the stack, a
session tears it down, whatever happened.

### 1. Preflight

1. Refuse when an earlier `wl-oracle` compose project was not torn down.
2. Measure the free space on the filesystem of Docker's data root.
3. Pull a missing image only under the disk policy
   ([The disk policy](#the-disk-policy)).
4. Require each image's `RepoDigests` to carry its pinned digest
   ([OR-1](../../docs/semantics/oracle.md#or-1)).
5. Require at least 5 GiB free before the oracle starts, whether or not
   anything was pulled. Images that were already present stay.

### 2. Start

1. Start the effect sink.
2. Write `compose.yaml`: PostgreSQL on tmpfs and the CE image in standalone
   mode, pinned by digest, never pulled by compose, with its ports on
   `127.0.0.1` ([OR-4](../../docs/semantics/oracle.md#or-4)).
3. Start the stack: `docker compose -p wl-oracle -f <compose file> up -d`.
   A session with two workers adds `--profile workers2` ([OR-5](../../docs/semantics/oracle.md#or-5)).

The disk watchdog covers the start and the rest of the session.

### 3. Boot

1. Wait for `/api/version`, which must answer exactly `CE v1.862.0`.
2. Wait for a healthy status with enough workers.
3. Log in as the seeded superadmin
   ([OR-6](../../docs/semantics/oracle.md#or-6)).

### 4. Per fixture

1. **Refuse or skip what this session cannot run**, before anything is
   deployed:
   - a fixture with [provided sites](../../docs/glossary.md#provided-site)
     is skipped, its runs reported `skipped`
     (`fixture_has_provided_sites`). windmill-lite replaces those sites with
     the host's providers, while the oracle would run their real targets, so
     such a fixture holds expectations only;
   - a fixture that needs another configuration is refused, or skipped with
     `SkipUnmetSession`; one that names an unknown configuration is refused
     (`unknown_session`, [Oracle configurations](#oracle-configurations));
   - a fixture that needs more workers than the session has is refused, or
     skipped with `SkipUnmetWorkers`;
   - a run whose committed recording names another oracle version, image or
     CLI than the pins is refused (`recording_identity_mismatch`,
     [OR-V5](../../docs/semantics/oracle.md#or-v5)). To re-record against a
     new pin, delete the recording.
2. **Create a fresh workspace** `wl-<6 hex>-<n>`. The session deletes it
   (`DELETE /api/workspaces/delete/{id}`) after the fixture's runs, or after
   its deployment failed, with a context of its own so that a canceled
   session still cleans up.
3. **Regenerate metadata** when the session asks for it, for a fixture
   pushed with the CLI: `folder
   add-missing` and `generate-metadata` in the fixture's sync tree (the
   drift gate of [OR-8](../../docs/semantics/oracle.md#or-8)). A generated
   tree, which has no committed metadata, always gets it.
4. **Seed before the push**, only with `oracle.setup: before_push`
   ([OR-31](../../docs/semantics/oracle.md#or-31)): apply `setup.yaml` once,
   and read each of its variables back; each must answer 200
   (`setup_not_applied` otherwise).
5. **Deploy the tree:** push a copy of it with the CLI
   ([Copies of a sync tree](#copies-of-a-sync-tree)), or create it through
   the API for a fixture with `oracle: {deploy: api}`
   ([API deployment](#api-deployment)). A stub fixture pushes its stubbed
   tree ([Stub mode](#stub-mode)).
6. **Apply or check `setup.yaml`** after the push:
   - by default, apply it now, and read each variable back: each must
     answer 200 (`setup_not_applied` otherwise);
   - with `before_push`, read each variable again: each must answer 404,
     because the push removed it (`seed_survived_push` otherwise).
7. **Wait for the deployment gate**
   ([OR-9](../../docs/semantics/oracle.md#or-9)), at most 2 minutes. For
   each script and flow, a lock job that the deployment status names must
   complete successfully; the status, read again, must have empty
   `lock_error_logs`; and a script must have its lock. A flow's job id
   stays after its lock job, so the gate does not wait for it to clear. A
   gate that fails is a fixture's error, not a difference from Windmill.
8. **Check** each deployed flow's top-level module ids against the tree.

CE refuses a third workspace besides `admins` ("You have reached the
maximum number of workspaces (2 outside of default workspace 'admins')
without an enterprise license"). So a workspace that cannot be deleted ends
the session.

### 5. Per run

1. **Group.** Runs that share a `group` are submitted together, as
   concurrent outer runs, and every other run alone.
   - Each member that failed keeps its own error. When one failed, each
     member with no error of its own fails with `group_member_failed`,
     which names the first failed run.
   - When they all ran but their root jobs were not in
     flight together by the oracle's timestamps, every member fails with
     `group_not_concurrent`.
   - Instability and expectation mismatches stay with the member that shows
     them.
2. **Reset.** Before every run of a fixture with `oracle.setup: each_run`,
   set its variables to `setup.yaml`'s values again.
3. **Submit.** Pre-assign the root job id, and open the sink for it (and, in
   stub mode, the replay service). Submit asynchronously and poll
   `get_result_maybe` until the run completes, never `run_wait_result`
   ([OR-10](../../docs/semantics/oracle.md#or-10)), within the run timeout.
   - When the poll fails or times out, the session tries to cancel the run,
     so it cannot hold the worker. Only when the cancel succeeds does it
     wait for the run to stop. The cancel and the wait share one 30 s
     deadline, and either can fail. A failed run-and-wait submission is
     cleaned up the same way.
4. **Run and wait.** A run with `submit: run_wait_result` is submitted
   through that endpoint instead, with the same pre-assigned id, to record
   the HTTP response ([FX-36](../../docs/semantics/execution.md#fx-36),
   [FX-37](../../docs/semantics/execution.md#fx-37)). It is then polled to
   completion, because an `early_return` answers while the flow runs on.
5. **Walk and normalise** the run ([Sites](#sites),
   [Normalisation and comparison](#normalisation-and-comparison)).
6. **Repeat** as the session asks. The observations must be equal
   ([OR-14](../../docs/semantics/oracle.md#or-14)).
7. **Compare** the observation with the run's `expect`, when it has one,
   under the fixture's compare options; and with its committed recording,
   when there is one, exactly: two oracle observations are never compared
   as a subset. A lifted expression case's run has claims and no `expect`.
8. **Write** the recording as the session's options say, unless it is
   unchanged.

For a run and wait, a redirect that the endpoint answers is the observation
and is not followed. The run timeout bounds the response and the poll
together; the asynchronous submissions and the walk use the session's
context.
A submission refused without a job fails the run with its response. The
provenance of a run-and-wait run holds the response's time
(`submit_to_response_ms`).

### 6. Teardown

1. Record the containers' cgroup memory peaks where the host exposes them.
2. Remove the stack: `docker compose -p wl-oracle -f <compose file> down -v --remove-orphans`.
3. Require that no container, volume or network with the project's label is
   left.

A session that finds an earlier stack not torn down refuses at once, before
it starts or tears down anything. The disk watchdog stops the session below
5 GiB. On Linux and macOS, Docker and CLI commands run in a process group of
their own, killed as a whole when the session is canceled, with 5 s to drain
their pipes after the kill. So a launcher such as `npx` cannot leave
the CLI running and block the teardown.

### Session options

`wltest record` sets these fields, most from its flags
([cmd/wltest](../../cmd/wltest/README.md#records-flags)):

| Field | What it does |
|---|---|
| `Workers` | The number of worker containers, 1 or 2 |
| `RunTimeout` | Bounds a run's wait for its job and a run and wait's response and poll, not the asynchronous submissions or the walk, which use the session's context: 5 minutes when zero, and raised to 60 s when lower. When it passes, the session tries to cancel the job and, only if the cancel succeeds, waits for it, both within one 30 s deadline ([5. Per run](#5-per-run)) |
| `Repeat` | Runs every run that many times and requires equal observations; 1 when zero |
| `RegenerateMetadata` | Runs `folder add-missing` and `generate-metadata` in each CLI-deployed fixture's tree, in place, so a CI step can diff the tree |
| `Check` | Fails a run that has no committed recording or whose observation differs from it, and does not write a recording to its default place in the fixture. An `OutDir` still receives it, and an `OutDir` equal to `RepoRoot` is the fixture's own recording directory; `RegenerateMetadata` still rewrites a fixture's metadata |
| `OutDir` | Receives new and changed recordings at `<OutDir>/<recording directory relative to RepoRoot>/<run>.json`, instead of the recording directory |
| `MismatchDir` | Receives the observation of each run that does not match its `expect` ([Mismatched observations](#mismatched-observations)) |
| `RawDir` | Receives the raw API captures of each repetition of a run that reaches the walk; a skipped run, and one that fails at its submission or poll, has none |
| `RepoRoot` | The directory that fixture paths are reported relative to |
| `ListenInterval` | The oracle's `LISTEN_NEW_EVENTS_INTERVAL_SEC` in place of 1, set through compose's `WL_LISTEN_NEW_EVENTS_INTERVAL_SEC`; it is in the provenance |
| `Config` | The oracle configuration ([Oracle configurations](#oracle-configurations)) |
| `SkipUnmetWorkers`, `SkipUnmetSession` | Report the runs of a fixture that needs more workers, or another configuration, as `skipped` instead of refusing them |
| `Docker`, `CLI`, `Log`, `Harness` | The docker runner, the CLI, the progress log, and the harness build for the provenance |
| `DataRoot` | The directory that the disk policy measures: Docker's data root when empty, or the home directory when that root is not on this host |
| `SinkListen` | The effect sink's listen address: `0.0.0.0:0` when empty |

### Run outcomes

| Outcome | Meaning | Fails the session |
|---|---|---|
| `unchanged` | The observation equals the committed recording | no |
| `new` | The run had no recording; one is written, unless `Check` is set without `OutDir` | only with `Check` |
| `changed` | The observation differs from the committed recording; the new one is written, unless `Check` is set without `OutDir` | only with `Check` |
| `unstable` | Repeated observations differ | yes |
| `expect_mismatch` | The observation does not match the run's `expect` | yes |
| `failed` | The run failed in the session | yes |
| `refused` | The harness refused the run | yes |
| `skipped` | The session cannot run it ([4. Per fixture](#4-per-fixture)) | no |

A run that fails in the session itself, after the oracle's part, has
`local_failure` set in its report. Its observation could not be compared
with the committed recording, or its recording, observation or raw capture
could not be placed or written.

### Mismatched observations

A run that does not match its `expect` writes no recording. A session's
`MismatchDir` receives its observation instead, with its provenance, in the
recording format, at the path that `OutDir` would give its recording, and
the run's report names the file (`observation`).

- A write that fails, or a fixture outside `RepoRoot`, fails the run with
  its differences kept.
- The session refuses to start when `MismatchDir` is `RepoRoot`, where the
  observations would replace the fixtures' recordings, or `OutDir`.

[cmd/wltest](../../cmd/wltest/README.md#mismatched-observations) says how
to read a mismatch from CI.

### Oracle configurations

A session's `Config` names an oracle configuration of `SessionConfigs`: the
variables that the server and the worker containers get, written to
`session.env` beside `compose.yaml` and recorded in the provenance's
environment ([OR-7](../../docs/semantics/oracle.md#or-7)). A fixture names
the configuration it needs in `oracle.session`.

| Name | Variables | For |
|---|---|---|
| `""` | none | The reference configuration ([OR-4](../../docs/semantics/oracle.md#or-4)) |
| `native` | `WORKER_TAGS=nativets` | `//native` and `nativets` steps: the worker serves the `nativets` tag ([Q-WR-21](../../docs/semantics/languages.md#q-wr-21)) |
| `long` | none | The reference configuration, for runs that wait a minute or more on the server's own monitors; the workflow gives them a longer run timeout |
| `result_size` | `MAX_RESULT_SIZE_MB=3` | The result size limit ([OR-7](../../docs/semantics/oracle.md#or-7)) |
| `flow_script_off` | `DISABLE_FLOW_SCRIPT=true` | Flows with flow scripts turned off ([OF-V5](../../docs/semantics/openflow.md#of-v5)) |

An unknown name is refused (`unknown_session`). The Oracle workflow,
[`.github/workflows/oracle.yml`](../../.github/workflows/oracle.yml), runs one
session per configuration, the reference configuration with one and with two
workers, and `long` with one and with two
([docs/ci.md](../../docs/ci.md#oracle)).

### The disk policy

The policy fails closed ([OR-30](../../docs/semantics/oracle.md#or-30)). The
figures are floors, never lowered:

| Constant | Value | Rule |
|---|---|---|
| `MinFreeToPull` | 8 GiB | No pull starts below it |
| `Reserve` | 5 GiB | A pull must leave it after the estimated need, and a pull or a run stops below it |
| `TransientMargin` | 1.5 GiB | Added to the images' estimates for the compressed layers in flight during a pull |

- A watchdog aborts a pull that goes below 5 GiB, and nothing resumes.
- After a pull, a settled reading below 5 GiB makes the session try to
  remove the images that it pulled, ignoring a failed removal, so the
  refusal does not prove that the space came back. A first reading at or above `Reserve` is accepted; a lower
  one is read again after a pause, and the second decides.
- A failed measurement also stops the work.

## The tools a session uses

### The CLI is a tool, not a dependency

The published CLI is the reference loader, because it is an independent
implementation of a sync folder to the API
([OR-8](../../docs/semantics/oracle.md#or-8)). The [build team](../../docs/glossary.md#build-team) never
reads, depends on or copies it: no manifest names it, and the manifest guard
rejects it. Running a published tool is use
([licensing](../../docs/semantics/licensing.md)).

- `CLI` runs `windmill-cli@1.862.0` from npm, with its own `HOME`, npm cache
  and configuration directory, and no inherited environment except `PATH`.
- Its `--version` must report exactly `1.862.0`.
- It runs `folder add-missing`, `generate-metadata`, `sync push` and, for
  `oracle.pull`, `sync pull`. A push is destructive, so the session pushes
  only into a workspace it created.
- Everything else the harness does (boot, login, workspaces, setup, the
  gate, runs, capture) goes through the Apache-2.0 API surface
  ([The vendored OpenAPI description](#the-vendored-openapi-description)).

### Docker

The docker runner drops `COMPOSE_*` and `DOCKER_DEFAULT_PLATFORM` from the
environment, which would change the topology behind the compose file.
`CheckDockerArgs` allows only the commands that the harness needs: `info`,
`version`, `pull`, `inspect`, `image inspect` and `image rm`, `compose up`,
`down`, `ps` and `logs`, and the listings `ps`, `volume ls` and
`network ls`. It refuses `push`, `tag`, `build`, `commit`, `save` and the rest,
so the harness itself can never derive or push an image
([OR-3](../../docs/semantics/oracle.md#or-3)).

### The REST client

`Client` calls the endpoints of the vendored `openapi.yaml`. Arguments and
results stay raw JSON text, because member order and number spelling are
observable ([OR-13](../../docs/semantics/oracle.md#or-13)).

- Full values come only from `get_args` and `get_result_maybe`, because the
  generic job read truncates large payloads
  ([OR-V1](../../docs/semantics/oracle.md#or-v1)).
- `RunWaitFlow` returns the run-and-wait response whatever its status.
- `Capture` records the exchanges for a replay; the last response for a
  method and path with query wins.

### The effect sink

The effect sink is an HTTP recorder on the harness host, to which fixture
scripts post their effects ([OR-16](../../docs/semantics/oracle.md#or-16)).
The oracle reaches it through `WL_EFFECT_SINK`, the only host variable that
`OracleEnv` lets jobs see.

- It keeps an effect only when its `root` is the submitted root of a run
  that is open, and several can be, for concurrent outer runs
  ([OR-V6](../../docs/semantics/oracle.md#or-v6)). It answers an effect of no
  open run 409 `{"error":"foreign_effect"}`.
- It serves the replay service at `/replay` ([Stub mode](#stub-mode)).
- It answers `GET /user-agent` with the request's `User-Agent` header and
  records nothing, so a step can report the header that its `fetch` sent
  ([WR-45](../../docs/semantics/languages.md#wr-45)).

## The session token

A session logs in as the oracle's seeded superadmin, after the readiness
checks, which need no token. It sends the token that the login returns with
every authenticated client call and every CLI command that targets the
workspace.

The token belongs to a throwaway Windmill in the session's own CI job, but no
file that can be committed may hold it. Windmill hands the token on: a flow started over HTTP
carries the request's headers, `Authorization` included, in its trigger
event, so a preprocessor's arguments hold it.

The session therefore replaces the token with `<redacted-token>`
(`RedactedToken`) where data enters it:

- `Client` replaces it in every response body and `Content-Type` header that
  it returns or captures, so walks, run-and-wait responses, errors and raw
  captures never hold it. A flow's result can set the run-and-wait
  response's `Content-Type`, parameters included
  ([FX-36](../../docs/semantics/execution.md#fx-36)). `Login` never captures
  its exchange, whose body is the token.
- The session replaces it in the effects and stub calls that scripts post to
  the effect sink, which bypass the client.
- `CLI` replaces it in the CLI's output and in the error of a failed
  command. The command line already shows `<token>` in its place.

Recordings, raw captures, the session report and log lines derive from those
sources, so none holds the token, whatever a fixture records or masks. A
script that encodes the token, for example in base64, gets past the
replacement; the CI secret scan (`.gitleaks.toml`) has a rule for bearer
tokens in recorded headers.

## Sites

The walker names every site with the typed site path of
[internal/conformance](../conformance/README.md#site-paths). It reads each
job's actual status, never the job kind
([OR-28](../../docs/semantics/oracle.md#or-28)). It reads the fixture's own
flow definitions, decoded with `internal/openflow`, to tell user modules from
generated ones, to name collapsed bodies, and to tell a `branchall` from a
loop ([OR-11](../../docs/semantics/oracle.md#or-11)).

| What ran | Its site, and what it records |
|---|---|
| A module with a job (a script, a subflow by path) | `m:<id>`: the job's status (canceled, else skipped, else success or failure; [OR-V2](../../docs/semantics/oracle.md#or-v2)), its stored args and result, the module's own status where it differs, and its `skipped` flag. A subflow's modules are below it, such as `m:sub/m:x` |
| A module whose status entry has no `job` | `m:<id>`, `not_run`, nothing fetched; such an entry whose module status is not `not_run` is an error |
| A module whose `job` is the all-zero UUID | Its entry type, with `ran: false`: an empty `branchone` branch, a failed predicate |
| The modules of a cached subflow, on a cache hit | `not_run`: the subflow did not run them |
| A loop or `branchall` (`flow_jobs`, even empty) | `m:<id>`: the status from the entry type, and the children's results in index order as its result; `ran: false` with no children. Child `n` is `i:<n>`, or `b:<n>` for a `branchall`, which the fixture's definition identifies because a completed entry need not carry its `branchall` field |
| A child flow of a loop or `branchall` | Its modules are below its child site, and so is its copy of the enclosing flow's failure module, at `i:<n>/s:failure` or `b:<n>/s:failure` ([FX-5](../../docs/semantics/execution.md#fx-5), [FX-34](../../docs/semantics/execution.md#fx-34)). A collapsed iteration is the body module's own job, at `i:<n>/m:<body>`, the path a child-flow iteration gives |
| A `branchone` | `m:<id>`: the chosen body's status and result, and `branch_chosen`. The body's modules are under `b:<n>` or `b:default` |
| A failed attempt | `<module>/r:<k>`, in `failed_retries` order |
| A failure module that succeeds | It lists the flow's earlier failed attempts again ([FX-27](../../docs/semantics/execution.md#fx-27), [FX-29](../../docs/semantics/execution.md#fx-29)), so the same job is at `s:failure/r:<k>` too. Its placeholder names the site that the walk reached first, the failed module's attempt |
| The failure or preprocessor module | `s:failure`, `s:preprocessor`, kept only when the flow defines one. An inline body defines its enclosing flow's failure module; a subflow by path defines its own |
| An empty flow (the fixture defines no modules) | No step: its status's one `Success` entry with a nil job is not a module ([FX-3](../../docs/semantics/execution.md#fx-3)) |
| A generated `<id>-v` module | Omitted ([OR-V7](../../docs/semantics/oracle.md#or-v7)); a user-authored one stays |

- A container module records no args: its job's args are Windmill's
  (`{"iter":…}`, the parent's flow args), not the flow's.
- Iteration and branch body jobs get no record of their own, but their ids
  map to `i:<n>` and `b:<n>` for placeholders.
- A flow status that does not match the fixture's module list is an error,
  not an observation.

`TestWalkCases` holds one case per shape, and `TestReplayRecordedCaptures`
replays the raw captures of CI's recordings through the walker.

## Copies of a sync tree

The session pushes a copy of a fixture's tree, never the checkout, and
`wltest stub` and `wltest check` write the stubbed copy the same way. A
codebase entry of `wmill.yaml`
([SY-8](../../docs/semantics/openflow.md#sy-8)) may name paths outside the
tree:

- its `relative_path`, whose files the CLI digests into the codebase id
  ([WR-49](../../docs/semantics/languages.md#wr-49));
- its assets' `from` files, which the CLI uploads with the bundle and
  windmill-lite's build reads;
- its `inject` files, which esbuild bundles
  ([WR-47](../../docs/semantics/languages.md#wr-47)).

Each resolves against the tree's root, the CLI's working directory. The copy
cannot rewrite them, because the CLI digests the entry's settings as
written, so `CopyLayout` carries them instead.

### What PlanLayout carries

`PlanLayout` reads `wmill.yaml` and walks each such path whose target exists,
component by component as it is spelled. `..` climbs from the tree's
directory with symbolic links resolved, as the CLI's working directory has
it. An absolute path resolves the same from a copy, and a path whose target
does not exist is skipped. A `wmill.yaml` that is missing or does not parse
names nothing; the CLI and the build report it.

- **The target**, with `.` and `..` applied, is carried when it lies outside
  the tree. A target below another carried one, which climbs no further than
  it, is carried with it.
- **`Root`** is set by the furthest that the spelling climbs above the tree,
  also when it comes back in: `../windmill/f/cb` from a tree named
  `windmill` needs the copied tree to be named `windmill` too.
- **`Dirs`** are the directories that a spelling passes through outside the
  tree on its way, such as `../detour` in `../detour/../shared/a.txt`. They
  are kept when they exist in the original and no carried path makes them:
  the deepest of them, because making a directory makes its parents.

### How CopyLayout copies

1. Copy each carried path, a directory whole or a file, to its place
   relative to where the copied tree goes.
2. Copy the tree to `<output>/<Root>`. `Root` is the last components of the
   tree's absolute path, as many as the furthest path climbs (`windmill` for
   `../shared`), or `.` when none climbs out, so a copy of a tree without
   such paths is unchanged.
3. Make each of `Dirs` there, empty.

`wmill.yaml` is unchanged, and every path it names reaches from the copied
tree a copy of what it reaches from the original. That holds whether the path
is walked on disk as spelled or joined with `.` and `..` applied first, as
windmill-lite's build does. So the CLI digests the same files under
`relative_path` and uploads the same assets.

- A spelling that climbs above the file system root cannot be laid out
  inside the copy, and is refused.
- An import of a codebase script that leaves the tree resolves in the copy
  when it lands below a carried path, as an import of a shared library below
  the entry's `relative_path` does. esbuild follows imports wherever they
  lead, and `PlanLayout` does not follow them. A stub has no imports.

## Stub mode

Stub mode compares the orchestration of flows whose steps need systems that
the oracle cannot reach, such as a third-party API
([Differential testing and performance measurement](../../docs/design/0011-differential-testing.md)).
A fixture with `stub: true` is pushed with every script body replaced by a
stub, and each run's `replay` table gives the stubs their outcomes. This
section is the oracle's side; the replay service and the table's format are
in [internal/conformance](../conformance/README.md).

### The stubbed push

A stub fixture is always deployed through the CLI: `stub: true` with
`oracle: {deploy: api}` is refused when the fixture loads, and again by the
session.

1. `StubTree` copies the fixture's tree, laid out as every copy of a sync
   tree is ([Copies of a sync tree](#copies-of-a-sync-tree)). The stubbed
   tree is at `Root` below the output, beside the paths outside it that its
   codebase entries name, which are never stubbed.
2. It replaces the source of every Bun script, and of every `!inline` Bun
   rawscript of a flow, with its stub. It finds them with
   [internal/syncfolder](../syncfolder/README.md).
3. The CLI runs `generate-metadata` in the stubbed tree, because its locks
   and `wmill-lock.yaml` still describe the originals.
4. `CheckStubSignatures` requires every path script's generated schema to
   equal the schema that the same CLI generates for the original source
   (`stub_signature_changed` otherwise). The CLI derives the schema from
   `main`'s signature, so equal schemas show that the stub kept what
   Windmill's parser reads.
5. The deployment report lists the stubbed files (`stubbed`) and the
   carried paths (`outside`).

Committed metadata may come from another CLI version. So unless the session
has just regenerated it in place (`RegenerateMetadata`), the CLI also runs
`generate-metadata` in another copy of the original, and the stub is
compared with that copy. `RegeneratedSchemas` lists the scripts whose
committed schema differs from the generated one. The deployment report gives
them as `regenerated_schemas`, and they are not refused.

Inline scripts have no schema: for them, the scanner's cases and the
arguments that each recorded call shows are the evidence.

`StubTree` refuses, with `stub_unsupported` and the file or step and the
reason, a tree in which an executed script would keep its real body:

| Refused | Why |
|---|---|
| A script in another language, or with the `native` annotation | Only a Bun script gets a stub |
| A rawscript whose content is written in `flow.yaml` | It has no file to replace |
| A step whose target is a hub path, or is not in the tree | Its body is not in the tree |
| A script step pinned by hash | It runs that deployed version, not the stub pushed at its path ([OF-14](../../docs/semantics/openflow.md#of-14)) |
| A `flowscript` step, or an AI-agent step | Its body is not a script of the tree |
| A retry on a loop or branch step | A failed attempt is walked as its last failed child alone, under `r:<k>` without the child's `i:` or `b:` segment, so none of its calls matches the site that the replay service resolved |
| A source that `StubSource` refuses | [The stub](#the-stub) |

A retry on a flow step is accepted: each attempt's subflow is walked whole
below the step's `r:<k>`, and the replay service counts its steps' jobs
across the attempts.

The output must not lie inside the tree or inside a carried directory. File
identity decides it (`os.SameFile` against each existing ancestor of the
output, links resolved), so a symbolic link, another letter case on a
case-insensitive file system, or any other alias of the tree counts. A copy
that fails removes the output and any parent directory it made.

### The stub

`StubSource` keeps what Windmill's parser reads, and drops the rest:

- **`main`'s declaration**, with its parameter list verbatim, because the
  parser finds a `function main` declaration, exported or not, and binds
  arguments by the names it finds there
  ([WR-6](../../docs/semantics/languages.md#wr-6),
  [WR-7](../../docs/semantics/languages.md#wr-7)). That `main` has an empty
  body and is never called, so its parameter defaults never run.
- **Every top-level type alias and interface**, verbatim, because the parser
  resolves local aliases to decide which parameters it hydrates as `Date`
  ([WR-8](../../docs/semantics/languages.md#wr-8),
  [WR-V3](../../docs/semantics/languages.md#wr-v3)). Types are erased
  before the code runs.
- **The `npm`, `nodejs`, `nobundling` and `sandbox` flags** of the leading
  comment block, first in the stub, because they decide how Windmill runs
  the script ([WR-31](../../docs/semantics/languages.md#wr-31)).
- Imports, other top-level code and the body are dropped.

The module exports a function `__wlMain` under the name `main`, which the
wrapper calls with the bound arguments. No user parameter, such as one named
`Array`, can shadow what it uses. When called, it:

1. posts `{root, root_flow, flow, job, step, args}` to
   `$WL_EFFECT_SINK/replay`, from its job variables
   ([OR-29](../../docs/semantics/oracle.md#or-29)). `root` is the submitted
   outer run, `root_flow` the innermost flow scope, `flow` the parent flow,
   `job` the stub's own job and `step` its module id. `args` holds the
   arguments it received, in parameter order;
2. returns the outcome's result, or throws its error as an `Error` with its
   name, message and other members as own properties, which the Bun wrapper
   stores under `extra` ([OR-12](../../docs/semantics/oracle.md#or-12),
   [WR-18](../../docs/semantics/languages.md#wr-18));
3. throws a refused call as an `Error` that names the refusal.

The scan is a small lexer that tracks bracket depth, so text in strings,
comments, templates and regular expressions never counts as code. It
refuses:

- a script without a `function main` declaration (an arrow `main` gets no
  arguments from Windmill);
- a `main` that is exported neither where it is declared nor through an
  export list (`export { main }`, `export { other as main }`). A type-only
  entry such as `export { type main }` exports nothing at run time;
- a `main` re-exported from another module, and a default-exported,
  generic, generator or overloaded `main`;
- a `preprocessor`.

At the top level, `type` and a name on one line before `=` or `<`, and
`interface` and a name, are always a declaration. A type alias ends at `;`,
or at a line that does not continue it: one that neither starts with `|`,
`&`, `?`, `:`, `.`, `?.` or `=>` nor follows a token a type cannot end with.
TypeScript reads `[`, `<` and `extends` on a new line as a new statement.

### Locating a call

While a run is in progress, `Locator` walks its job tree from the root with
the walker's rules, and finds the calling job:

- a module's job, or one of its `failed_retries`, is at the module's site;
- a leaf in `flow_jobs` is a collapsed iteration, at `i:<n>/m:<body>`;
- child flows (iterations, branch bodies, named subflows) are walked in
  turn;
- the slots are `s:failure` and `s:preprocessor`;
- a generated `-v` module and the lone entry of an empty flow are skipped;
- as in the walker, a container body carries the enclosing flow's failure
  module, and the fixture's definition tells a `branchall`.

A job that is not in its parent's status yet, or a walk that failed, is
looked for again until the call's deadline, 60 s, which reports the last
walk's error. Completed jobs are cached. The attempt is the replay service's
count of distinct jobs already seen at the site.

### After the run

`checkReplay` applies `conformance.CheckReplay`, the check that `wltest
check` applies to windmill-lite's trace too, to the walk's job ids and the
walked jobs of the kinds that execute a script. It fails the run when:

| Failure | When |
|---|---|
| `replay_miss` | A call was refused: the table has no outcome for the site and attempt |
| `replay_unresolved` | A call was refused because its job could not be located |
| `replay_site_mismatch` | A call's site and attempt disagree with where the walk put its job: a failed attempt `k` must be at `<site>/r:<k>`, and the last job at `<site>`, with `k` equal to the failed attempts before it |
| `stub_not_called` | A walked job that executed a script never called |

The calls are then the run's effects, after the sink's, normalised like any
value. So a recording holds each call's site, attempt, step id, job ids (as
placeholders) and arguments.

## Normalisation and comparison

`Observe` normalises a walk, its effects and, for a run and wait, its
response. Its inputs are the walk's job ids and sites, the stored errors of
the failed script leaves (for the stack rule), and the fixture's `stack` and
`mask` pointers. The rules themselves (placeholders, stacks, the location of
the completion failure, escapes, unwalked ids and the pointers) have one
home, [internal/conformance](../conformance/README.md#the-normaliser).

- **The response** keeps its status and its `Content-Type` header as sent,
  parameters included. Its body is normalised text when it is UTF-8, and
  base64 otherwise.

A job that the walk never reaches can still appear, such as the job of a
flow that a step ran through the API. `Observe` has the normaliser number
such ids over the whole observation before any value is normalised, so the
two executions of a run agree
([OR-14](../../docs/semantics/oracle.md#or-14)).

### Recordings and provenance

A recording is `{"observation": …, "provenance": …}`. The provenance is
never compared ([OR-V5](../../docs/semantics/oracle.md#or-v5)). It holds:

- the oracle's version, its image and the PostgreSQL image, by reference and
  id, and the CLI with its `--version` output;
- the harness build, the host, and the CI run (from `GITHUB_ACTIONS`,
  `GITHUB_SERVER_URL`, `GITHUB_REPOSITORY` and `GITHUB_RUN_ID` when the
  session runs in GitHub Actions);
- the recording time, the workers, and the oracle's non-default environment
  ([OR-7](../../docs/semantics/oracle.md#or-7));
- the repeat count and raw timings; the count of foreign effects and of
  foreign replay calls, refused while the run was open; and the group it was
  submitted with;
- for an API-deployed fixture, the target flow's deployment
  ([API deployment](#api-deployment)).

`CheckRecordingIdentity` requires the provenance to name the pinned oracle,
image and CLI. A recording holds neither carried values nor resolved
arguments, which the oracle does not expose.

## API deployment

A fixture with `oracle: {deploy: api}` needs its tree to reach the server
as written, which the CLI cannot do. `generate-metadata` writes container
defaults into `flow.yaml`, which some cases need absent
([CF-6](../../docs/semantics/control-flow.md#cf-6),
[CF-15](../../docs/semantics/control-flow.md#cf-15)). And a tree cannot hold
a script hash that the server has not assigned yet, which a by-hash case
needs ([FX-40](../../docs/semantics/execution.md#fx-40)).

`DeployAPI` creates the tree through the REST API instead: folders first,
then scripts, then flows, each in path order. Nothing regenerates or pushes
the tree, so the drift gate does not apply to it. It reads a narrow tree and
refuses anything else:

| File | What it gives |
|---|---|
| `wmill.yaml`, `folder.meta.yaml` | Not sent. A folder is created for every `f/<folder>/` |
| `f/<folder>/<name>.ts`, `f/<folder>/<dir>/<name>.ts` | A Bun script, also under a directory of the folder, as a flow step's relative import of `./x` from the flow `f/<folder>/<flow>` needs |
| `<name>.script.yaml` (optional) | The script's `summary`, `description`, `cache_ttl`, `envs`, `schema` and `delete_after_secs` |
| `f/<folder>/<name>.flow/flow.yaml` | A flow. Its rawscript `content` and `lock` may be `!inline <file>` in the flow's directory, and its by-path `script` modules may pin the hash `0000000000000000` (`PinToDeployedHash`), which is replaced by the hash the deployment assigned the path |

Only modules are resolved: the flow's module list, its failure and
preprocessor modules, and the bodies of loops and branches. Everything else
is sent as written ([FX-7](../../docs/semantics/execution.md#fx-7)), a static
input, an iterator or a mock whose value looks like a module value included.

The session then applies `setup.yaml` and passes the same deployment gate.
For each flow it keeps a `FlowDeployment`, and a run's provenance holds its
target flow's as `deployment`:

- `submitted`: the value submitted;
- `materialized`: the value that `GET /api/w/{workspace}/flows/get/{path}`
  answers after the gate,
  after the dependency job's write-back;
- `cli_generated`: what `generate-metadata` writes for the same source, run
  on a copy of the tree against the same workspace;
- `pulled`, with `oracle.pull`: what the CLI's `sync pull` writes back,
  pulled into a directory that holds only the fixture's `wmill.yaml`
  ([SY-6](../../docs/semantics/openflow.md#sy-6)).

## Recording batches

A recording manifest, `testdata/batches/<batch id in lower case>.json`
([the manifests](testdata/batches)), ties a group of
findings to the fixtures that record them. Its `batch` value is the id of a
work item of the ADR
[Go package layout and implementation milestones](../../docs/design/0012-layout-and-milestones.md),
whose row names the finding ranges and cases that the manifest must cover.
`TestRecordingBatches` checks every manifest in the Go CI jobs
([docs/ci.md](../../docs/ci.md#the-go-jobs)); the Oracle workflow runs
`wltest record`, not this test.

| Field | What it holds |
|---|---|
| `about` | A description of the manifest |
| `batch` | The work item's id, which must be a row of the ADR's table |
| `findings` | The findings whose every catalogue case the manifest records |
| `cases` | Further single catalogue cases that it records |
| `fixtures` | Its fixture directories, from the repository root. Each run's own `spec` says which cases it records |
| `deferred` | Cases that this manifest's runs do not record, each with a work item's id (or `none` or `later`) in `milestone`, and a reason. Another manifest's fixture may record such a case |
| `parts` | Halves of cases that this manifest records, which its runs do not observe, each with a work item's id and a reason; another fixture may observe them |
| `prefixes` | The catalogue's prefix-only assertions (`<starts "…">`), checked on a run's recorded text at a pointer |
| `texts` | The catalogue's other partial assertions about a recorded text: substrings it contains, and ones it must not contain |
| `deployments` | The catalogue's claims about a deployed flow value, checked on a run's deployment evidence (`submitted` or `materialized`) at a pointer: a value, or no member there |
| `contradictions` | Catalogue claims that the oracle contradicted, with the run, the pointer and the recorded value that the run's `expect` holds instead |
| `gates` (optional) | For every run of its fixtures, `<fixture directory>/<run>`, the work item whose tests run it on windmill-lite. That work item must need the manifest's, directly or through the items it needs, in the ADR's table; a gate for a run that does not exist is refused |

`TestRecordingBatches` requires that:

1. the manifest covers the finding ranges and cases that its row of the ADR
   names, and each of its fixtures is `oracle-observed`, has at least one
   run, and targets a flow of its tree (a script run excepted);
2. every catalogue case of its findings is recorded by a run of its fixtures,
   or deferred with a reason, and no case is both recorded and deferred in
   this manifest;
3. every run has an `expect`, and a recording of the pinned oracle that
   meets it;
4. the prefix, text and deployment assertions hold on the recordings;
5. each entry of `contradictions` matches the recorded value at its pointer.
   The test cannot find a contradiction that no entry lists: listing each is
   the author's task;
6. a manifest with `gates` names a work item for every run.

The expression cases have no manifest: most of their runs state partial
`claims` rather than a whole `expect`. Their coverage is
`conformance/exprs/deferred.yaml`, which `TestBatchCoverage` checks, and
`recordings_test.go` checks their recordings.

## Repository checks

Two checks run on every pull request, in the Go CI jobs, through
`TestRepositoryPassesTheChecks`. They scan the repository except for the
directories named `testdata`, `node_modules`, `.git` and `.jj`.

| Check | What it refuses |
|---|---|
| Image lint, images (`LintImages`) | A Dockerfile or Containerfile that names a Windmill image at all, so none can bake it in through `FROM`, `COPY --from`, `RUN --mount=from=` or an `ARG` that `FROM ${…}` expands. Pulling and running the image is allowed ([OR-3](../../docs/semantics/oracle.md#or-3)) |
| Image lint, pushes (`LintImages`) | A file that names a Windmill image and runs a push command. The files are workflows, composite actions (any YAML under `.github/`), shell scripts, Makefiles and justfiles. The commands are `docker push`, `docker tag`, `docker image push` or `tag`, `buildx imagetools create`, `skopeo copy`, a `crane` copy, cp, push, tag, mutate, append or rebase, or `regctl image copy` or `mod` |
| Manifest guard (`GuardManifests`) | In `go.mod` and `go.sum`, any `github.com/windmill-labs/` module but the Apache-2.0 Go client. In `package.json`, lockfiles and import maps, the AGPL-derived `windmill-parser-wasm-*` packages, `windmill-yaml-validator`, `windmill-utils-internal` and `windmill-cli`. In `package.json`, a dependency on the Windmill repository or on an npm tarball of an excluded package; in lockfiles and import maps, a reference to the Windmill repository. In `Cargo.toml`, the Rust client or a Windmill crate, by key, table or `package =` ([LIC-1](../../docs/semantics/licensing.md#lic-1)) |
| Vendored directories (`GuardManifests`) | A directory whose `NOTICE` names the Windmill repository may hold only the Apache-2.0 schemas, their licence and the notice, and may cite no `backend/` path but `backend/windmill-api/openapi.yaml` |

A finding names its rule: `image-from`, `image-repush`,
`manifest-dependency`, `vendored-path`, `vendored-file` or
`vendored-input`. `testdata/guard/cases.json` holds the checks' cases.

## The vendored OpenAPI description

`openapi/` holds `openapi.yaml`, Windmill's REST API description, and
`LICENSE-APACHE`, copied unmodified from Windmill `v1.862.0`, with a
[NOTICE](openapi/NOTICE) that names their source and digests. They are
Apache-2.0, not Unlicense, as the root README's
[licence table](../../README.md#licence) says.

- `TestClientUsesTheOpenAPISurface` calls every exported `Client` method,
  and requires each request's method and path to be an operation of the
  vendored description. Where the test could read an operation's top-level
  property names from the description, a request body's top-level names
  must be among them. An operation whose properties it could not read is
  checked by method and path only. It does
  not validate the body against the schema.
- The facade's route table is generated from the same file
  ([internal/platform/facade](../platform/facade/README.md#the-route-table)).

## Refusals

A refusal of a run or of a session's preflight is a `*Refusal`, whose text
is the JSON that the reference gives it ([OR-1](../../docs/semantics/oracle.md#or-1)), such as
`{"error":"fixture_requires_workers","need":2,"have":1}`:

| `error` | Constructor | When |
|---|---|---|
| `oracle_version_mismatch` | `ErrVersionMismatch` | `/api/version` answers another version |
| `oracle_image_mismatch` | `ErrImageMismatch` | An image does not carry its pinned digest |
| `insufficient_disk` | `ErrInsufficientDisk` | A pull may not start ([The disk policy](#the-disk-policy)) |
| `disk_floor_reached` | `ErrDiskFloor` | The watchdog, or the check before the oracle starts, finds less than 5 GiB free |
| `insufficient_disk_after_pull` | `ErrDiskAfterPull` | The settled reading after a pull is below 5 GiB; the session tries to remove the pulled images, and ignores a removal that fails |
| `fixture_requires_workers` | `ErrRequiresWorkers` | A fixture needs more workers than the session has |
| `fixture_requires_session` | `ErrRequiresSession` | A fixture needs another configuration |
| `unknown_session` | `ErrUnknownSession` | The session, or a fixture's `oracle.session`, names an unknown configuration |
| `fixture_has_provided_sites` | `ErrProvidedSites` | A fixture has provided sites (its runs are skipped) |
| `foreign_effect` | `ErrForeignEffect` | The sink's answer to an effect of no open run |
| `stub_unsupported` | `ErrStubUnsupported` | `StubTree` or `StubSource` refuses a tree or a script |

Other failures of a run name their reason the same way, and `RunReport.Failed`
says whether a run fails the session: `cli_version_mismatch`,
`recording_identity_mismatch`,
`setup_not_applied`, `seed_survived_push`, `group_member_failed`,
`group_not_concurrent`, `stub_signature_changed` and the replay failures of
[After the run](#after-the-run).

Some errors stop the whole session before it starts, and `Session.Run`
returns them: an unknown `Config` (the refusal `unknown_session`), and the
ordinary errors of an earlier stack that was not torn down and of a
`MismatchDir` at `RepoRoot` or `OutDir`.

## Decisions where the reference is silent

| Question | The code's choice | Why | Pinned by |
|---|---|---|---|
| How does the harness load a sync tree into Windmill? | The published CLI, run as a tool; never read, copied or depended on | The CLI is the reference loader, and using a published tool is not using its code | `TestCLIVersionExact`, `TestGuardCases` |
| Which docker commands may the harness run? | An allowlist: no `push`, `tag`, `build`, `commit` or `save` | The harness can never derive or push an image | `TestDockerAllowlist` |
| How is the session token kept out of the repository? | Replaced with `<redacted-token>` where data enters the session | Windmill hands the token on in trigger events | `TestSessionNeverWritesTheToken` |
| Where is a sync tree pushed from? | A copy that carries the paths its codebase entries name outside it | The push must not change the checkout, and the CLI digests the entry's settings as written | `TestSessionPushesACopyWithTheOutsidePaths` |
| How are a step's outcomes given in stub mode? | A stub that keeps `main`'s signature and type declarations, and calls the replay service | Windmill's parser must bind the same arguments | `TestStubSourceCases`, `TestCheckStubSignatures` |
| Which stub trees are refused? | Any in which an executed script would keep its real body, and a retry on a loop or branch step | Their calls could not be matched to a site | `TestStubTreeRefuses` |
| What does a mismatched run write? | No recording; its observation to `MismatchDir` | A recording must meet its `expect` | `TestSessionWritesMismatchedObservations` |
| How long does a locator look for a job? | Until the call's deadline, 60 s by the replay service's default | A job may not be in its parent's status yet | `TestLocatorGivesUp` (gives up at the deadline) |

## Tests

The package's tests run sessions against a fake docker, a fake Windmill and
the test binary as the CLI. They need local sockets for their HTTP servers,
and no Docker or external service. Recording
on the real oracle runs only in the Oracle workflow
([docs/ci.md](../../docs/ci.md#oracle)).

### Run the tests

1. To run the tests that need Bun, set `WL_BUN` to a Bun 1.4.3 executable.
2. From the repository root, run `go test ./internal/oracle`.
3. Run `go test -race ./internal/oracle`, as CI's Go jobs do
   ([docs/ci.md](../../docs/ci.md#the-go-jobs)).

| Variable | Value | Tests that need it | When it is not set |
|---|---|---|---|
| `WL_BUN` | The path of a Bun 1.4.3 executable | `TestStubRunsOnBun`, `TestStubIsolationOnBun`, `TestExportListsOnBun` | Skip, unless `WL_REQUIRE_BUN` is set: then fail |
| `WL_REQUIRE_BUN` | Any non-empty value | the same | Skip as above |

### Test files

| File | Tests | What they check |
|---|---|---|
| `walk_test.go` | `TestWalkCases` | The walker and `Observe` on a fake job API, one case per shape of [Sites](#sites) (`testdata/walk/cases.json`) |
| `record_test.go` | `TestSessionRecordsTheSmokeFixture`, `TestSessionUnstableObservation`, `TestSessionSubsetFixtureRecordsExactly`, `TestSessionRefusesARecordingOfAnotherOracle` | Whole sessions on the smoke fixture: recording, check mode, provenance never compared, a recording of another oracle refused before deployment, instability, and exact comparison of recordings under a subset fixture |
| | `TestSessionRefusals`, `TestSessionCancelsATimedOutRun`, `TestSessionKillsAHungCLIAndTearsDown`, `TestSessionOutDirStaysInside` | The session's refusals (version, image identity, disk, a stale session, workers); a timed-out run canceled; a hung CLI killed and the stack torn down below the disk floor; `OutDir` kept inside its directory |
| | `TestSessionDeletesEachWorkspace`, `TestSessionDeletesWorkspacesOnFailure`, `TestSessionFailsWhenAWorkspaceCannotBeDeleted` | Each fixture's workspace deleted, also after a failed check or gate; a deletion that fails ends the session with the stack torn down |
| | `TestSessionWritesMismatchedObservations`, `TestSessionMismatchDirRefusals`, `TestLocalFailureOnSessions` | `MismatchDir`: the observation written and named, and its refusals; `local_failure` |
| | `TestFakeCLI` | The fake CLI that the session tests run |
| `options_test.go` | `TestSessionRecordsRunAndWait`, `TestSessionRunAndWaitRefused`, `TestRunWaitKeepsARedirect`, `TestObserveKeepsTheResponse` | A run-and-wait run's response beside its walk, a refused submission, a redirect kept, the `Content-Type` and a body that is not UTF-8 kept |
| | `TestSessionSetupPolicies`, `TestSessionSkipsUnmetWorkers`, `TestSessionSkipsProvidedSites`, `TestSessionListenInterval`, `TestSessionConfiguration`, `TestSessionPullsTheDeployedTree` | `setup.yaml` after the push, before it and before every run; the skips; the listen interval; a session's configuration; the pulled flow in the deployment evidence |
| `record_stub_test.go` | `TestSessionRecordsTheStubFixtures`, `TestSessionStubConcurrentRuns`, `TestSessionStubRefusals`, `TestSessionStubSignatures` | Whole stub-mode sessions against a fake Windmill that runs the stub fixtures job by job: every run matches its `expect` and records unchanged; a group's runs together; `replay_miss` and `stub_not_called`; the schema check |
| | `TestCheckOverlap` | A group's root jobs in flight together |
| `redact_test.go`, `cli_test.go` | `TestRedactingWriter`, `TestClientHidesItsToken`, `TestSessionNeverWritesTheToken`, `TestCLIOutputHidesTheToken` | Where the session token is replaced; a whole session whose run hands the token back writes no file that holds it |
| `cli_test.go` | `TestCLIVersionExact`, `TestCLICancelKillsTheGroup` | The exact version check; a canceled command's children killed promptly |
| `capture_test.go` | `TestReplayRecordedCaptures`, `TestUnwalkedJobIDsAcrossRepeats` | Raw captures of CI's recordings, replayed through the walker and the normaliser, give the committed recordings; the job kinds met; the two executions of a run that names unwalked jobs give one observation (`testdata/captures/`) |
| | `TestCommittedRecordingsNameThePinnedOracle` | Every committed recording names the pinned oracle, image and CLI |
| `fixtures_test.go` | `TestFlowFixtureTreesResolve`, `TestTreeScriptsAreSources` | Every fixture's tree decodes, and holds each run's target and every flow or script a module names; only a script's source file makes a script |
| | `TestRecordedParallelismReleaseOrder`, `TestRecordedDeploymentEvidence`, `TestRecordedEarlyReturn`, `TestRecordedBigPayload`, `TestRecordedOutOfOrderCompletion` | Rules read from committed recordings: a loop's `parallelism`, the deployment evidence, an early return, a big payload whole, and results in index order |
| `apideploy_test.go` | `TestDeployAPI`, `TestDeployAPIRefusals`, `TestDeployAPIResolvesOnlyModules`, `TestDeployAPINestedScript`, `TestSessionDeploysThroughTheAPI` | The API deployment against a recording server: order, bodies, an absent field left absent, `!inline` files and a pinned hash resolved in module positions only, a nested script, and the narrow tree's refusals |
| `layout_test.go` | `TestPlanLayout`, `TestCopyLayout`, `TestCopiesResolveTheSpellings`, `TestCopyLayoutRefuses`, `TestStubTreeCarriesTheOutsidePaths`, `TestSessionPushesACopyWithTheOutsidePaths` | Which paths are carried, `Root` and `Dirs`; every configured path reaches the same bytes from the copy, as spelled and with `.` and `..` applied; the refusals; the stubbed tree and a session's push carry the outside paths |
| `stubts_test.go` | `TestStubSourceCases`, `TestStubSourceGolden` | The stub of each source, or its refusal; stubbing is idempotent; one whole stub pinned (`testdata/stub/`) |
| `stub_test.go` | `TestStubTreeOfTheFixtures`, `TestStubTreeRefuses`, `TestCheckStubSignatures`, `TestStubTreeKeepsItsOutputOutsideTheTree`, `TestStubTreeRefusesACaseAlias` | The stubbed tree of every stub fixture; every refusal of `StubTree`; the schema check; an output inside the tree, also through a link or a letter case, refused |
| | `TestStubRunsOnBun`, `TestStubIsolationOnBun`, `TestExportListsOnBun` | On Bun: a stub's request and outcomes against the real replay service; a default or a parameter named `Array` cannot reach the stub's code; the scanner accepts a script exactly when Bun gives it a callable `main` |
| `locate_test.go` | `TestLocatorFindsALateJob`, `TestLocatorGivesUp`, `TestLocatorSites`, `TestCheckReplay` | The locator on job trees that change while it looks, its deadline and every site shape; each failure of `checkReplay` but `replay_unresolved` |
| `sink_test.go` | `TestSinkGroupsByOuterRoot`, `TestSinkKeepsConcurrentRunsApart`, `TestSinkRoutesReplayCalls`, `TestSinkAnswersTheUserAgent`, `TestSinkListen` | Effects grouped by outer root and kept apart per open run; `/replay`; `GET /user-agent`; the listen address |
| `disk_test.go` | `TestRefusalForms`, `TestCheckPull`, `TestPullNeed`, `TestWatchAbortsBelowFloor`, `TestWatchLetsWorkFinish`, `TestSettledFree` | The refusals' JSON, and each step of the disk policy |
| `docker_test.go` | `TestDockerAllowlist`, `TestComposeFile`, `TestImageIdentity`, `TestDockerEnvDropsTopologyVariables` | The allowlist; the compose file against the pins, `OracleEnv` and its `env_file`; image identity; the dropped variables |
| `openapi_test.go` | `TestClientUsesTheOpenAPISurface` | Every client call's method and path against the vendored description, and its body's top-level property names where the description's could be read |
| `lint_test.go` | `TestGuardCases`, `TestRepositoryPassesTheChecks` | The repository checks on their cases, and on this repository |
| `coverage_test.go` | `TestCatalogueCoverage` | Every case of the oracle and licensing findings in the catalogue is cited by a case, checked by every session, or deferred with a reason (`testdata/coverage.json`) |
| `recordings_test.go` | `TestExprFilesLift`, `TestBatchFixturesDecode` | Every expression file lifts into flows that the walker reads back; every flow fixture decodes |
| | `TestRecordingsSatisfyClaims`, `TestExprCasesAgreeWithRecordings`, `TestUnitCasesMatchRecordings`, `TestBatchCoverage` | Committed recordings against what their cases claim; the coverage of the expression findings (`conformance/exprs/deferred.yaml`) |
| `batch_test.go` | `TestRecordingBatches` | Every recording manifest ([Recording batches](#recording-batches)) |

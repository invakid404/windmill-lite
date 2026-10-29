# internal/oracle

**Current state:** implemented (milestones E2 and E4a of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md), extended by
the recording batches E3a to E3e). This README
covers `internal/oracle/`, its vendored `openapi/` directory and its
`testdata/`. The package is the oracle harness of
[ADR 0011](../../docs/design/0011-differential-testing.md): it records flow
fixtures on an unmodified Windmill CE `v1.767.0`, which runs in public CI
only, through the `wltest record` command ([cmd/wltest](../../cmd/wltest/README.md))
and the [oracle workflow](../../.github/workflows/oracle.yml). That workflow
records the OR-20 smoke fixture ([conformance/flows/oracle/smoke](../../conformance/flows/oracle/smoke)),
the fixtures of the recording batches, E3c's and E3b's under
[conformance/flows/execution](../../conformance/flows/execution) and E3d's
and N3c's under [conformance/flows/control-flow](../../conformance/flows/control-flow),
the fixtures engine milestones N2 and NI recorded themselves, beside them,
the stub-mode acceptance cases ([conformance/flows/stub](../../conformance/flows/stub),
see [Stub mode](#stub-mode)), milestone E1's first fixtures under
`conformance/flows/languages` and `conformance/flows/client-api`, the
fixtures of recording batch E3e (among them the probe catalogue under
`conformance/flows/oracle`, and its two-worker fixture, which the
workflow's second session records), and the expression cases of batch
E3a, which
`wltest record` lifts into generated flows
([conformance](../../conformance/README.md#expression-cases)). E3c extended
the session and the walker (below), E3b the walker and the batch test, E3d
the walker and added the API deployment (below), E3a the session and
its own batch tests, and E3e the run-and-wait submission, the setup
policies, the pull read-back and the two-worker session (below). `wltest
check` compares windmill-lite's flow runs with each run's `expect` or
recording (milestone E1, [cmd/wltest](../../cmd/wltest/README.md)), and
since milestone E4b runs stub fixtures there with this package's stub
generator and effect sink.

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
4. **Per fixture.** Skip a fixture with host-provided sites
   (`provided`, milestone F2a), reporting its runs as `skipped`
   (`fixture_has_provided_sites`): windmill-lite replaces those sites with
   the host's providers, while the oracle would run their real targets,
   such as a subflow that suspends, so such a fixture holds expects.
   Refuse a fixture that needs more workers than the
   session has, or, with `SkipUnmetWorkers` (`--skip-unmet-workers`),
   report its runs as `skipped`, and a run whose committed recording names another oracle
   version, image or CLI than the pins (`recording_identity_mismatch`,
   [OR-V5](../../docs/semantics/oracle.md#or-v5)/b), before anything is
   deployed; to re-record against a new pin, delete the recording. Create a
   fresh workspace `wl-<6 hex>-<n>`, which is deleted again after the
   fixture's runs, or after its deployment failed, with a context of its own
   so that a canceled session still cleans up
   (`DELETE /workspaces/delete/{id}`): CE refuses a third workspace besides
   `admins` ("You have reached the maximum number of workspaces (2 outside
   of default workspace 'admins') without an enterprise license", observed
   by E3c, E3a and E4a), and a workspace that cannot be deleted ends the
   session. Optionally run
   `folder add-missing` and `generate-metadata` in the fixture's sync tree
   (the drift gate of [OR-8](../../docs/semantics/oracle.md#or-8)), and
   always in a generated tree, which has no committed metadata; push a
   copy of the tree with the CLI, or create it through the API for a
   fixture with `oracle: {deploy: api}` ([API deployment](#api-deployment));
   apply `setup.yaml` after the push
   ([OR-31](../../docs/semantics/oracle.md#or-31)), or before it for a
   fixture with `oracle.setup: before_push`, and read each of its variables
   back: after the push it must answer 200 once applied and 404 when the
   push removed what was seeded (`CheckSetup`, OR-31/b;
   `setup_not_applied`, `seed_survived_push`); pass the deployment gate
   ([OR-9](../../docs/semantics/oracle.md#or-9)) and check each deployed
   flow's module ids against the tree (OR-8/b). A stub fixture pushes its
   stubbed tree instead (see [Stub mode](#stub-mode)).
5. **Per run.** Runs that share a `group` are submitted together, as
   concurrent outer runs, and every other run alone; a group fails as a
   whole when one of its runs fails (`group_member_failed`) or when their
   root jobs were not in flight together by the oracle's timestamps
   (`group_not_concurrent`). Pre-assign the root
   job id, open the sink for it (and, in stub mode, the replay service), submit
   asynchronously and poll `get_result_maybe` until completion, never
   `run_wait_result` ([OR-10](../../docs/semantics/oracle.md#or-10)); a run
   that times out is canceled, so it cannot hold the worker. A run with
   `submit: run_wait_result` is submitted through that endpoint instead,
   with the same pre-assigned id, to record the HTTP response
   ([FX-36](../../docs/semantics/execution.md#fx-36),
   [FX-37](../../docs/semantics/execution.md#fx-37)), and is then polled to
   completion like the others, since an `early_return` answers while the
   flow runs on; a redirect it answers is the observation and is not
   followed; one deadline bounds the response and the poll together,
   a submission refused without a job fails the run with its response,
   and the response's time is in the provenance. Before every
   repetition of a fixture with `oracle.setup: each_run`, its variables are
   set to `setup.yaml`'s values again (`ResetSetup`). Walk the run,
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

A session's `ListenInterval` (`--listen-interval`) sets the oracle's
`LISTEN_NEW_EVENTS_INTERVAL_SEC` through compose's
`WL_LISTEN_NEW_EVENTS_INTERVAL_SEC`, in place of 1; it is in the
provenance's environment. The oracle workflow runs two sessions, as a job
matrix: one worker for every fixture, skipping those that need two, and
two workers with `--listen-interval 10` for the fixtures that need two
([conformance/flows/oracle/workers2](../../conformance/flows/oracle/workers2),
[OR-5/b](../../docs/semantics/conformance.md#or-5)) and the smoke fixture,
whose recordings, made at 1 s with one worker, must hold unchanged
([OR-7/a](../../docs/semantics/conformance.md#or-7)).

Every refusal is a `*Refusal` whose text is the JSON the reference gives it,
for example `{"error":"fixture_requires_workers","need":2,"have":1}`.

## The pieces

| File | What it does |
|---|---|
| `pins.go` | The image pins (tag and multi-arch index digest), the version string, the CLI package, the compose project and the seeded superadmin; the reason a run's `cancel_after_ms` cancels it with and the canceler Windmill then names, which `wltest check` gives windmill-lite's run too ([FX-38](../../docs/semantics/execution.md#fx-38)). |
| `compose.yaml` | The oracle's compose file, embedded. A test checks it against the pins, the hygiene rules and `OracleEnv`. |
| `docker.go` | The docker runner. It drops `COMPOSE_*` and `DOCKER_DEFAULT_PLATFORM` from the environment, which would change the topology behind the compose file. `CheckDockerArgs` allows only the commands the harness needs (`pull`, `image inspect`/`rm`, `compose up`/`down`/`ps`/`logs`, listings); `push`, `tag`, `build`, `commit`, `save` and the rest are refused, so the harness itself can never derive or re-push an image ([OR-3](../../docs/semantics/oracle.md#or-3)). |
| `disk.go` | The OR-30 policy: `PullNeed`, `CheckPull`, the `Watch` watchdog and `SettledFree`. |
| `client.go` | The REST client, written against the Apache-2.0 `openapi/openapi.yaml`. Arguments and results stay raw JSON text, because member order and number spelling are observable ([OR-13](../../docs/semantics/oracle.md#or-13)); full values come only from `get_args` and `get_result_maybe` ([OR-V1](../../docs/semantics/oracle.md#or-v1)). `RunWaitFlow` returns the run-and-wait response whatever its status. `Capture` records exchanges for replay. |
| `cli.go` | Runs the published `windmill-cli@1.767.0` from npm as a black-box tool, with its own HOME, npm cache and config directory and no inherited environment. Its `--version` must report exactly `1.767.0`. It runs `folder add-missing`, `generate-metadata`, `sync push` and, for `oracle.pull`, `sync pull`. |
| `proc_unix.go` | Gives each docker and CLI command its own process group, killed on cancellation, with a bound on draining its pipes. |
| `deploy.go` | Boot, login, workspaces, `setup.yaml` (`ApplySetup`, `CheckSetup`, `ResetSetup`), the deployment gate and the deployed-module check. |
| `apideploy.go` | The API deployment of a fixture with `oracle: {deploy: api}` (see below). |
| `sink.go` | The effect sink ([OR-16](../../docs/semantics/oracle.md#or-16), [OR-V6](../../docs/semantics/oracle.md#or-v6)): it keeps an effect only when its `root` is the submitted root of a run that is open (several can be, for concurrent outer runs), and answers anything else with `{"error":"foreign_effect"}`. It also serves the replay service at `/replay`. |
| `stub.go`, `stubts.go` | Stub mode's generator: `StubTree` writes a stubbed copy of a sync tree, `StubSource` the stub of one Bun script, `CheckStubSignatures` compares schemas after `generate-metadata`. |
| `locate.go` | The oracle's `Locator` for the replay service: the site of a stub's job in a run that is still in progress. |
| `replay.go` | `checkReplay`, which holds a stub run's answered calls against its walk with `conformance.CheckReplay`. |
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
first-site rule for a repeated attempt. E3d recorded collapsed and
child-flow loops at two depths, parallel loops, while loops, `branchone`,
sequential and parallel `branchall` with empty branches, early stops in
bodies, and cached subflows, whose inner modules are `not_run` on a hit.
E4a's stub fixtures ([conformance/flows/stub](../../conformance/flows/stub))
recorded failed attempts, sequential, collapsed, parallel and nested loop
iterations and a named subflow. E3e recorded the probe catalogue's shapes
again from rebuilt fixtures, a flow preprocessor, parallel bodies that
complete out of index order on two workers, and a loop left canceled with
its module in progress when the flow's `skip_expr` throws mid-run. The
other recording batches confirm the rest.

## Stub mode

Stub mode ([ADR 0011](../../docs/design/0011-differential-testing.md))
compares the orchestration of flows whose steps need systems the oracle
cannot reach. A fixture with `stub: true` is pushed with every script body
replaced by a stub, and each run's `replay` table gives the stubs their
outcomes; the service and the table format are in
[internal/conformance](../conformance/README.md).

**The stubbed push.** A stub fixture is always deployed through the CLI
(`stub: true` with `oracle: {deploy: api}` is refused when the fixture
loads, and again by the session). `StubTree` copies the fixture's tree and replaces the
source of every Bun script and every `!inline` Bun rawscript of a flow,
which it finds with [internal/syncfolder](../syncfolder/README.md). It
refuses (`stub_unsupported`, with the file or step and the reason) a tree
in which an executed script would keep its real body: another language, the
`native` annotation, a rawscript whose content is written in `flow.yaml`, a
step whose target is a hub path or not in the tree, a script step pinned by
hash (it runs that deployed version, not the stub pushed at its path,
[OF-14](../../docs/semantics/openflow.md#of-14)), a `flowscript` step, an
AI-agent step, a retry on a subflow, loop or branch step (a failed attempt's inner steps
are walked under `r:<k>`, which a table does not address), and a source
`StubSource` refuses. The output must not lie inside the tree, which is
decided by file identity (`os.SameFile` against each existing ancestor of
the output, links resolved), so a symbolic link, another letter case on a
case-insensitive file system or any other alias of the tree counts; a copy
that fails removes the output and any parent directory it made. The CLI then runs
`generate-metadata` in the copy,
since its locks and `wmill-lock.yaml` still describe the originals, and
`CheckStubSignatures` requires every path script's generated schema to
equal the original's (`stub_signature_changed` otherwise): the CLI derives
the schema from `main`'s signature, so equal schemas show the stub kept
what Windmill's parser reads. Inline scripts have no schema; for them the
scanner's cases and the arguments each recorded call shows are the
evidence. The deployment report lists the stubbed files.

**The stub.** `StubSource` keeps `main`'s declaration with its parameter
list verbatim, because Windmill's parser finds a `function main`
declaration, exported or not, and binds arguments by the names it finds
there ([WR-6](../../docs/semantics/languages.md#wr-6),
[WR-7](../../docs/semantics/languages.md#wr-7)). That `main` is declared
with an empty body and never called, so its parameter defaults never run;
the module exports `__wlMain` under the name `main`, which the wrapper calls
with the bound arguments and in whose scope no user parameter (one named
`Array`, say) can shadow what it uses. It also keeps every top-level type
alias and interface verbatim, because the parser resolves local aliases to
decide which parameters are hydrated as `Date`
([WR-8](../../docs/semantics/languages.md#wr-8),
[WR-V3](../../docs/semantics/languages.md#wr-v3)); types are erased before
the code runs. It also keeps the `npm`, `nodejs`, `nobundling` and
`sandbox` flags of the leading comment block, first in the stub, since they
decide how Windmill runs the script
([WR-31](../../docs/semantics/languages.md#wr-31)). Imports, top-level code
and the body are dropped. The exported function
POSTs `{root, root_flow, flow, job, step, args}` (the job identities of
[OR-29](../../docs/semantics/oracle.md#or-29) and the arguments it received,
in parameter order) to `$WL_EFFECT_SINK/replay`, then returns the outcome's
result or throws its error as an `Error` with its name, message and other
members as own properties, which the Bun wrapper stores under `extra`
([OR-12](../../docs/semantics/oracle.md#or-12),
[WR-18](../../docs/semantics/languages.md#wr-18)); a refused call is thrown
as an `Error` naming the refusal. The scan is a small lexer that tracks
bracket depth, so text in strings, comments, templates and regular
expressions never counts as code. It refuses a script without a `function
main` declaration (an arrow `main` gets no arguments from Windmill), one
that is neither exported where it is declared nor through an export list
(`export { main }`, `export { other as main }`; a type-only entry such as
`export { type main }` exports nothing at run time), one re-exported from
another module, a default-exported, generic, generator or overloaded
`main`, and a `preprocessor`. At the top level, `type` and
a name on one line before `=` or `<`, and `interface` and a name, are
always a declaration; a type alias ends at `;` or at a line that does not
continue it (one starting with `|`, `&`, `?`, `:`, `.` or `=>`, or
following a token a type cannot end with), since TypeScript reads `[`, `<`
and `extends` on a new line as a new statement.

**Locating a call.** While a run is in progress, `Locator` walks its job
tree from the root with the walker's rules and finds the calling job: a
module's job, or one of its `failed_retries`, is at the module's site; a
leaf in `flow_jobs` is a collapsed iteration at `i:<n>/m:<body>`; child
flows (iterations, branch bodies, named subflows) are walked in turn; the
slots are `s:failure` and `s:preprocessor`; a generated `-v` module and
the lone entry of an empty flow are skipped; and, as for the walker, a
container body carries the enclosing flow's failure module and the
fixture's definition tells a `branchall`. A job not in its parent's status yet, or a walk that failed, is looked
for again until the call's deadline (60 s), which reports the last walk's
error. Completed jobs are cached. The attempt is the
replay service's count of distinct jobs already seen at the site.

**After the run.** `checkReplay` applies `conformance.CheckReplay`, the
check `wltest check` applies to windmill-lite's trace too, to the walk's
job ids and the walked jobs of the kinds that execute a script. It fails
the run when a call was refused
(`replay_miss`: the table has no outcome for the site and attempt;
`replay_unresolved`), when a call's site and attempt disagree with where
the walk put its job (`replay_site_mismatch`: a failed attempt `k` must be
at `<site>/r:<k>`, the last job at `<site>` with `k` equal to the failed
attempts before it), or when a walked job that executed a script never
called (`stub_not_called`). The calls are then the run's effects, after the
sink's, normalised like any value, so a recording holds each call's site,
attempt, step id, job ids (as placeholders) and arguments.

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
timings, foreign effects and, for an API-deployed fixture, the target
flow's deployment (below). It is never compared
([OR-V5](../../docs/semantics/oracle.md#or-v5)). Recordings hold neither
carried values nor resolved arguments, which the oracle does not expose.

## API deployment

A fixture with `oracle: {deploy: api}` needs its tree to reach the server
as written, which the CLI path cannot do: `generate-metadata` writes
container defaults into `flow.yaml` (CF-6/a and CF-15/a need them absent),
and a tree cannot hold a script hash the server has not assigned yet
(FX-40/d pins one). `DeployAPI` creates the tree through the REST API
instead (`createFolder`, `createScript`, `createFlow`), and nothing
regenerates or pushes it, so the drift gate does not apply to it. It reads
a narrow tree and refuses anything else:

- `wmill.yaml` and `folder.meta.yaml`, which are not sent; a folder is
  created for every `f/<folder>/`;
- Bun scripts `f/<folder>/<name>.ts`, with an optional
  `<name>.script.yaml` of `summary`, `description` and `cache_ttl`;
- flows `f/<folder>/<name>.flow/flow.yaml`, whose rawscript `content` and
  `lock` may be `!inline <file>` in the flow's directory, and whose by-path
  `script` modules may pin the hash `0000000000000000`
  (`PinToDeployedHash`), which is replaced by the hash the deployment
  assigned the path.

Only modules are resolved: the flow's module list, its failure and
preprocessor modules, and the bodies of loops and branches. Everything else
is sent as written ([FX-7](../../docs/semantics/execution.md#fx-7)),
including a static input, an iterator or a mock whose value looks like a
module value.

The session then applies `setup.yaml` and passes the same deployment gate.
For each flow it keeps a `FlowDeployment`: the value submitted, the value
`GET /flows/get` answers after the gate (after the dependency job's
write-back, [CF-6](../../docs/semantics/control-flow.md#cf-6)), and what
`generate-metadata` writes for the same source, run on a copy of the tree
against the same workspace; with `oracle.pull`, also what the CLI's `sync
pull` writes back, pulled into a directory that holds only the fixture's
`wmill.yaml` ([SY-6](../../docs/semantics/openflow.md#sy-6), CF-6/b). A
run's provenance holds its target flow's, as `deployment`.

## Recording batches

A recording batch is one of milestones E3a to E3e of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md), or an engine
milestone that records its own fixtures, as N2 records FX-23 to FX-29
(`n2.json`), N3c records CF-16 to CF-18, CF-20, CF-29 and CF-V1 to
CF-V5 (`n3c.json`), and NI records CF-21 to CF-28 with FX-24/d and /e
(`ni.json`). `testdata/batches/<batch>.json` holds one, and
`TestRecordingBatches` checks it on every CI run:

| Field | Meaning |
|---|---|
| `batch` | the milestone id; its row in ADR 0012 names finding ranges (`FX-6 to FX-10`) and cases (`FX-42/h`) the batch must cover |
| `findings`, `cases` | the findings whose every catalogue case is in the batch, and further single cases |
| `fixtures` | the batch's fixture directories, from the repository root; each run's own `spec` says which cases it records |
| `deferred`, `parts` | cases, or halves of recorded cases, left to a later milestone, each with the milestone and a reason |
| `prefixes` | the catalogue's prefix-only assertions, checked on a run's recorded text |
| `contradictions` | catalogue claims the oracle contradicted, with the run, pointer and recorded value the run's `expect` holds instead |
| `gates` | optional: for every run, `<fixture directory>/<run>`, the milestone whose exit gate runs it, which must need the batch merged; E3b's gates N1a, N1b, N2, NI, N3b, PF2 or F1 by the features each flow needs |

Batch E3e's manifest has no `gates`, since only F7b needs E3e merged. It
defers to `none`, with the reason, what the oracle cannot record or the
contract leaves out, and to the milestone that checks it what no run of
the batch records: FX-36/i and FX-36/m's `Location` header to F7b, the
HTTP adapter, WR-28/f to C2a, and WR-50/c and WR-50/f to C3n.

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
| `TestSessionRecordsTheSmokeFixture`, `TestSessionUnstableObservation`, `TestSessionRefusals`, `TestSessionSubsetFixtureRecordsExactly`, `TestSessionCancelsATimedOutRun`, `TestSessionOutDirStaysInside`, `TestSessionRefusesARecordingOfAnotherOracle`, `TestSessionKillsAHungCLIAndTearsDown`, `TestSessionDeletesEachWorkspace`, `TestSessionDeploysThroughTheAPI`, `TestSessionFailsWhenAWorkspaceCannotBeDeleted` | whole sessions against a fake docker, a fake Windmill that runs the smoke flow and, like CE, refuses a third workspace, and this test binary as the CLI: recording, check mode, provenance never compared (OR-V5/a), a recording of another oracle refused before deployment (OR-V5/b), effects grouped by the outer root, instability, exact comparison of recordings under a subset fixture, cancellation of a timed-out run, a hung CLI killed and the stack torn down when the disk falls below the floor, `--out` kept inside its directory, three fixtures in one session with each workspace deleted, a deletion that fails ending the session after the fixture's runs with the stack torn down, and the refusals of OR-1/b, OR-1 image identity, OR-5/a, OR-30/a, OR-30/c, cached images below the floor, and a stale session | the smoke fixture |
| `TestCLIVersionExact`, `TestCLICancelKillsTheGroup` | the exact version check, and that canceling a command or `--version` kills its children promptly | — |
| `TestCommittedRecordingsNameThePinnedOracle` | every committed recording names the pinned oracle, image and CLI | `conformance/flows` |
| `TestReplayRecordedCaptures` | raw API captures of CI recordings (the smoke fixture's, one run per walker shape that E3c and E3d recorded, and every run of E3b), replayed offline through the walker and normaliser, reproduce the committed recordings; the job kinds met follow OR-28. A capture may come from a later run than its recording: the observation does not depend on job ids | `testdata/captures/` |
| `TestFlowFixtureTreesResolve` | every fixture's sync tree decodes as the walker reads it, and each run's target and every flow or script a module names by path is in the tree (a script by its source file, in any language, as the sync folder reads it) | `conformance/flows` |
| `TestTreeScriptsAreSources` | only a script's source file makes a script there: a flow directory, a resource, a variable or a script's metadata of the same name does not (E3a review R1-2) | a synthetic tree |
| `TestRecordedParallelismReleaseOrder` | CF-7/b on its recording: with `parallelism: 1`, each iteration's child flow starts after the previous one completed, while CF-7/a's child flows overlap (job times from the provenance) | `conformance/flows/control-flow/cf-7` |
| `TestDeployAPI`, `TestDeployAPIRefusals`, `TestDeployAPIResolvesOnlyModules` | the API deployment against a recording server: order, request bodies, an absent field left absent, `!inline` files read and a pinned hash resolved in every module position and nowhere else (module-like static values, iterators and mocks stay as written), and the narrow tree's refusals | — |
| `TestRecordedDeploymentEvidence` | the API-deployed recordings: CF-6/a's and CF-15/a,b's fields are absent when submitted and filled in the API's answer and in generate-metadata's output; the pulled flow keeps an identity module's extra field and fills the loop's `skip_failures` (CF-6/b, SY-6/c); FX-40/d's body carries the deployed script's hash | `conformance/flows` |
| `TestRecordedEarlyReturn`, `TestRecordedBigPayload`, `TestRecordedOutOfOrderCompletion` | E3e's recordings: run-and-wait answered `early_return`'s value at least a second before the flow completed (FX-37/a); the big payload is whole in a's result and b's arguments (OR-V1/a); on two workers the slow index-0 body completed after the next one, while results stay in index order (Q-OR-7, Q-OR-15) | `conformance/flows` |
| `TestSessionRecordsRunAndWait`, `TestSessionRunAndWaitRefused`, `TestRunWaitKeepsARedirect`, `TestObserveKeepsTheResponse`, `TestSessionSetupPolicies`, `TestSessionSkipsUnmetWorkers`, `TestSessionSkipsProvidedSites`, `TestSessionListenInterval`, `TestSessionPullsTheDeployedTree` | E3e's session options against the fake Windmill: a run-and-wait run's response (200 and 422, the failed step's stack masked inside the body) beside its walk, a refused submission failing at once, a redirect kept and not followed, the Content-Type kept with its parameters and a body that is not UTF-8 kept as base64 through observations, raw captures and their replay; `setup.yaml` after the push, before it (read back before and after the push) and before every run, with `setup_not_applied` and `seed_survived_push`; a fixture that needs two workers skipped, and one with host-provided sites; the listen-interval knob in compose's environment and the provenance; the pulled flow in the deployment evidence | the smoke fixture |
| `TestRefusalForms`, `TestCheckPull`, `TestWatchAbortsBelowFloor`, `TestSettledFree` | the refusal JSON and the OR-30 policy | — |
| `TestDockerAllowlist`, `TestComposeFile`, `TestImageIdentity` | the docker allowlist, the compose contract, image identity | — |
| `TestClientUsesTheOpenAPISurface` | every client call against the vendored schema | `openapi/openapi.yaml` |
| `TestSinkGroupsByOuterRoot`, `TestSinkKeepsConcurrentRunsApart`, `TestSinkRoutesReplayCalls` | OR-16/c; two open runs each keep their own effects (OR-V6/a); `/replay` goes to the replay service | — |
| `TestStubSourceCases`, `TestStubSourceGolden` | the stub of each source keeps `main`'s parameter list and the type declarations verbatim and drops the rest, and stubbing is idempotent; or the source is refused with its reason; one whole stub pinned | `testdata/stub/sources.json`, `testdata/stub/book.stub.ts` |
| `TestStubTreeOfTheFixtures`, `TestStubTreeRefuses`, `TestCheckStubSignatures` | the stubbed tree of every stub fixture (only Bun sources change); every refusal of `StubTree`, the hash-pinned script step and the `flowscript` step among them; the schema check | `conformance/flows/stub` |
| `TestStubRunsOnBun` | a generated stub on the pinned Bun (`WL_BUN`) against the real replay service: the request it sends (identities, the hydrated `Date` as its JSON), an error outcome thrown with its name, message and own property, a result returned with its member order, a refusal thrown | `conformance/flows/stub/collapsed` |
| `TestStubIsolationOnBun` | stubs on the pinned Bun with `fetch` recorded: a default that would call a service or name a dropped binding never runs, and a parameter or destructured binding named `Array` cannot reach the stub's code; the replay call is the only request. Each original runs through the same driver first, to show the recorder sees what a default does | — |
| `TestStubTreeKeepsItsOutputOutsideTheTree`, `TestStubTreeRefusesACaseAlias` | an output inside the tree, directly, through a symbolic link, or through another letter case where the file system makes it the same directory, is refused with the tree unchanged (where the spellings are two directories, the stub is written); a failed copy removes its output and the parents it made | — |
| `TestExportListsOnBun` | the scanner accepts a script exactly when importing it on the pinned Bun gives a callable `main` (export lists, type-only entries, aliases), and every accepted script's stub exports one too | — |
| `TestLocatorFindsALateJob`, `TestLocatorGivesUp`, `TestLocatorSites` | the locator on job trees that change while it looks: a job that appears late, a deadline, repeated module ids, failed attempts (also inside a parallel iteration), the failure slot, a `branchone` body, a `branchall` (also without its `branchall` field), a named subflow, an empty subflow, a failure handler inside a loop body, a collapsed iteration, a loop that names its parent, a child not known yet, the cache, a leaf iteration without a one-module body (reported at the deadline), a script run | — |
| `TestCheckReplay`, `TestCheckOverlap` | each failure of `checkReplay`; a group's root jobs in flight together | — |
| `TestSessionRecordsTheStubFixtures`, `TestSessionStubConcurrentRuns`, `TestSessionStubRefusals` | whole stub-mode sessions against a fake Windmill that executes E4a's four stub fixtures (`collapsed`, `nested`, `retry_loop`, `scopes`) job by job, with in-progress statuses, retries, collapsed and parallel loops and subflows, and makes the stubs' calls: every run matches its `expect` and records unchanged on a second session; runs `r` and `t` run together and keep apart (the fake starts a group's runs once all of them are submitted, as the oracle's runs outlast the gap between submissions, so a slow machine cannot finish one first; a session that submits them one after the other still gets `group_not_concurrent`); `replay_miss` and `stub_not_called` fail a run | `conformance/flows/stub` |
| `TestGuardCases`, `TestRepositoryPassesTheChecks` | the image lint and the manifest guard | `testdata/guard/cases.json` |
| `TestCatalogueCoverage` | every OR and LIC-1 case of the catalogue is cited by a case, checked live by every session, or deferred to a named milestone | `testdata/coverage.json` |
| `TestSessionDeletesWorkspacesOnFailure` | a workspace whose check after creation or whose deployment gate failed is deleted too, and a workspace that cannot be deleted ends the session | the smoke fixture |
| `TestExprFilesLift`, `TestBatchFixturesDecode` | every expression file lifts into flows that the walker reads back as their cases describe them; every flow fixture's tree decodes and holds its runs' targets | `conformance/exprs`, `conformance/flows` |
| `TestRecordingsSatisfyClaims`, `TestExprCasesAgreeWithRecordings`, `TestUnitCasesMatchRecordings` | committed recordings against what their cases say (batch E3a's form): claims and delays hold, a recorded case is `oracle-observed`, an expected evaluator text has the recorded argument as its stored form, and each unit case's expression and previous id match its flow, and its flow input, previous result and database reads have the recorded texts as their stored forms; at a `timeout` site the expected text is exactly the value the recorded i32 parse error quotes ([JE-29](../../docs/semantics/expressions.md#je-29)), and a timeout case whose recording quotes none fails (FU5) | `conformance/exprs`, `conformance/flows` |
| `TestBatchCoverage` | every JE case of the catalogue, and every CF-19 and CF-V6 case, is an expression case, a flow fixture's run, or deferred with a reason and a milestone (batch E3a) | `conformance/exprs/deferred.yaml` |
| `TestRecordingBatches` | each recording batch against its ADR 0012 row: the batch covers the finding ranges and cases its row names; every catalogue case of its findings is recorded by a run of its fixtures or deferred with a milestone and a reason; every run has an `expect` and a recording of the pinned oracle that meets it; the catalogue's prefix-only assertions (`<starts "…">`) hold on the recorded text; each catalogue claim the oracle contradicted is listed with the value it recorded; a batch with `gates` names for every run a milestone that needs the batch merged | `testdata/batches/` |

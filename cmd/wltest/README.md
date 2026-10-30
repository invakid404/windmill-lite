# cmd/wltest

**Current state:** three verbs, `record` (milestone E2 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md); recording batch
E3a added expression files, E3e `--skip-unmet-workers` and
`--listen-interval`, and E3f `--session` and `--skip-unmet-session`), `stub` (milestone E4a) and `check`, for step
cases (milestone C3a; C2b added codebase scripts and their Node side),
flow fixtures (milestone E1) and, on the engine core, expression files run
end to end (milestone N1a), with the for loops of milestone N3a, the
while loops and branches of N3b, the platform providers of milestone PF2
(`providers.go`), the job registry, flow user state, object store and
dispatcher of milestone PF3, the containers' waits, retries and
cancellation of milestone NI, the effect sink and stub mode of milestone
E4b (`effects.go`), and, since milestone F1, through the public
[wl](../../wl/README.md) library in `warm` and `fresh`. This README covers the `wltest` command in
this directory, its `testdata/`, and `noderun.mjs`, the Node driver `check`
embeds. `diff`, which
[ADR 0011](../../docs/design/0011-differential-testing.md) describes as
running the oracle and windmill-lite live, is in no milestone of ADR 0012
yet: it prints that it is not implemented and exits 2.

`check` can build every bundle with the shared code of
[ADR 0013](../../docs/design/0013-shared-module-code.md) (`-shared-code`, below).

`wltest` is windmill-lite's conformance and oracle runner
([ADR 0011](../../docs/design/0011-differential-testing.md)). `record` runs
flow fixtures (the format of [OR-15](../../docs/semantics/oracle.md#or-15),
loaded by [internal/conformance](../../internal/conformance/README.md)) on
the pinned Windmill CE `v1.767.0` oracle through
[internal/oracle](../../internal/oracle/README.md), and writes
`recorded/<run>.json` with provenance. It also takes expression-case files
(`conformance/exprs/<finding>.yaml`), lifts each into a generated one-step
flow fixture in a temporary directory, and writes its recordings to
`conformance/exprs/recorded/<finding>/<case>.json`.

## Usage

```sh
go build -o bin/wltest ./cmd/wltest
bin/wltest record [flags] <fixture, expression file or directory>...
```

Each argument is a fixture directory (one holding `fixture.yaml`), an
expression-case file, or a directory searched for both: a `.yaml` file in a
directory named `exprs`, other than `deferred.yaml` and outside any fixture,
is an expression-case file. Arguments that overlap reach each fixture and
file once, and an explicit file of any other kind is refused (exit 2). Flow
fixtures are recorded first, then the expression files, with files marked
`slow` last, so that what a slow case leaves running on the oracle (the
spinning evaluation of [JE-2](../../docs/semantics/expressions.md#je-2))
cannot slow the others. It needs Docker, which pulls the CE image
(1,373 MB compressed on amd64, [OR-2](../../docs/semantics/oracle.md#or-2))
and Postgres, and `npx` on `PATH` to run `windmill-cli@1.767.0`. Pulls are
refused without enough free disk ([OR-30](../../docs/semantics/oracle.md#or-30)).
The oracle runs in public CI; see
[the oracle workflow](../../.github/workflows/oracle.yml).

| Flag | Default | Meaning |
|---|---|---|
| `--check` | off | Fail when a run has no committed recording or its observation differs from it, and never write into a fixture. |
| `--out DIR` | — | Write new and changed recordings under `DIR`, at their committed path (`DIR/<fixture path>/recorded/`, `DIR/conformance/exprs/recorded/<finding>/`), instead of in place. |
| `--raw DIR` | — | Write the raw API captures of every run to `DIR`. |
| `--report FILE` | — | Write the session report: disk readings, pulls, images, boot times, CLI version, deployments, memory peaks, teardown. |
| `--repeat N` | 1 | Run each run `N` times and require equal observations. |
| `--regenerate-metadata` | off | Run `folder add-missing` and `generate-metadata` in each fixture's sync tree, in place, before pushing; CI then diffs the tree ([OR-8](../../docs/semantics/oracle.md#or-8)). A fixture with `oracle: {deploy: api}` is never regenerated: its tree is created through the API as written. A generated tree always gets them. |
| `--workers N` | 1 | Worker containers, 1 or 2 ([OR-5](../../docs/semantics/oracle.md#or-5)). |
| `--skip-unmet-workers` | off | Skip a fixture that needs more workers than `--workers`, reporting its runs as `skipped`, instead of refusing it ([OR-5/a](../../docs/semantics/conformance.md#or-5)): the oracle workflow's two-worker session records it. |
| `--session NAME` | `""` | The oracle configuration of the session, a name of `SessionConfigs` in [internal/oracle](../../internal/oracle/README.md#a-session): `native` (a worker that serves `nativets`), `long`, `result_size` or `flow_script_off`; empty is the reference configuration. A fixture runs only in the session its `oracle.session` names; an unknown name is refused (batch E3f). |
| `--skip-unmet-session` | off | Skip a fixture that needs another session's configuration, reporting its runs as `skipped`, instead of refusing it; the oracle workflow runs one session per configuration. |
| `--listen-interval N` | 1 | The oracle's `LISTEN_NEW_EVENTS_INTERVAL_SEC`, for the knob comparison of [OR-7/a](../../docs/semantics/conformance.md#or-7); the value is in every recording's provenance, which is never compared. |
| `--cli CMD` | `npx -y windmill-cli@1.767.0` | The command that runs the CLI; its `--version` must report exactly `1.767.0` (a pre-release such as `1.767.0-beta` is refused). |
| `--docker-data-root DIR` | Docker's data root | The directory whose filesystem the disk preflight measures. |
| `--sink-listen ADDR` | `0.0.0.0:0` | The effect sink's address; the oracle reaches it through `host.docker.internal`, which on Linux is the Docker bridge gateway, so the default listens beyond loopback. On a shared network, pass a narrower address. |
| `--run-timeout D` | `5m` | The timeout of one run; less than 60 s is raised to 60 s ([OR-10](../../docs/semantics/oracle.md#or-10)). A run that times out is canceled. |

It prints one line per run: `unchanged`, `new`, `changed`, `unstable`,
`expect_mismatch`, `failed`, `refused` or `skipped` (a fixture that needs
more workers, under `--skip-unmet-workers`, one that needs another
session's configuration, under `--skip-unmet-session`, or one with host-provided
sites, which only windmill-lite runs), the fixture and the run, then any
error and the JSON-pointer differences (`/steps/m:b/args/tag: "ho" != "hi"`,
actual before expected). It exits 1 when a run failed, was refused, was
unstable or did not match its `expect`, and in check mode also when a
recording is new or changed. A run whose committed recording names another
oracle, image or CLI is `refused` (`recording_identity_mismatch`) before
anything runs; delete the recording to re-record it. A refusal of the whole
session (for example `{"error":"insufficient_disk",…}`) is printed as JSON on
standard output. A run's `claims`, `delays` and `unit` cases are not checked
here: `go test ./internal/oracle` checks them against the committed
recordings, so a contradicted claim still leaves a recording to inspect.

Fixtures in stub mode (`stub: true`) are pushed stubbed: `record` writes
the stubbed copy of the sync tree, runs the CLI's `generate-metadata` in
it, requires every script's schema to equal the original's, and answers
each run's stubs from its `replay` table through the replay service at
`$WL_EFFECT_SINK/replay`. A run also fails when a stub's call has no
outcome in the table, when a call disagrees with the walk, or when a
script job never called ([internal/oracle](../../internal/oracle/README.md#stub-mode)).
Runs that share a `group` are submitted together, as concurrent outer runs,
and fail together when one fails or when they did not overlap. Each
fixture's workspace is deleted after its runs, because CE admits only two
workspaces besides `admins`.

## stub

```sh
bin/wltest stub --out <dir> <sync tree>
```

Copies the sync tree (the directory of its `wmill.yaml`) to `<dir>`, which
must not exist and must not lie inside the tree or a directory the copy
carries (compared by file identity, so a symbolic link or a case alias
counts), with the source of every Bun script and every `!inline` Bun rawscript
replaced by a stub that keeps `main`'s declaration with its parameter list
and the type declarations, never calls it, and exports a function under
the name `main` that asks the replay service for its outcome
([ADR 0011](../../docs/design/0011-differential-testing.md) stub mode). It
prints one `stubbed <file>` line per replaced source. When the tree's
codebase entries name paths outside it (`relative_path`, assets' `from`,
`inject`), `wmill.yaml` is kept as written and those paths are carried at
the same places relative to the stubbed tree: the stubbed tree is then
`<dir>/<root>`, below as many of the tree's parent directories' names as
the furthest path climbs as it is spelled, and the command prints a
`carried <path>` line per carried path, a `made <dir>` line per directory
outside the tree that a spelling passes through (made empty), and a
`tree <root>` line
([internal/oracle](../../internal/oracle/README.md#copies-of-a-sync-tree)).
The copy's metadata still describes the originals, so run `npx -y
windmill-cli@1.767.0 generate-metadata` in the stubbed tree before pushing
it. A tree in which some executed script would keep its body is refused,
with `{"error":"stub_unsupported","file":…,"reason":…}` on standard output
and exit 1.

## check

```sh
WL_BUN=/path/to/bun-1.3.10/bun bin/wltest check [--mode warm|fresh|host] [-shared-code] [--bun PATH] [--node PATH] [--repo DIR] [-v] <step file, fixture, expression file or directory>...
```

Runs the step cases of `conformance/steps`, the flow fixtures of
`conformance/flows` and the expression cases of `conformance/exprs` on
windmill-lite. Each argument is a step file, a fixture directory (one
holding `fixture.yaml`), an expression file, or a directory searched for
all three. An explicit file of any other kind is refused (exit 2).

It prints `pass` or `fail`, the case and its file, or the fixture and the
run, then each difference; `unsupported`, the fixture, the run and why it
was not checked; and a summary. It exits 1 when a case or run failed or a
file or fixture is invalid, and 2 for usage errors. Unsupported runs do not
fail it. `--mode` is `warm` (the default), `fresh` (milestone C3b) or
`host` (milestone F3b, flow fixtures and expression files only). Flow
fixtures run in any mode, through `wl` (milestone F1): in `fresh` each step
gets a harness process of its own, as a step case does; in `host` each
fixture's steps run in a Bun host that embeds the host SDK (host mode,
below). `--bun` defaults to `WL_BUN`, then `bun`; `--node` to
`WL_NODE`, and empty skips the Node side of CommonJS codebase cases
(below); the build refuses any Bun but the pin. `--repo` defaults to the
nearest directory up from the working directory that holds `harnesses/bun`, whose build helper needs its
dependencies installed (`bun install --frozen-lockfile` in
`harnesses/bun/build`). `-v` prints the harness's diagnostics of a failed
case or run.

`-shared-code` builds every bundle with shared code
([ADR 0013](../../docs/design/0013-shared-module-code.md); deviation [D18](../../docs/design/0001-equivalence-contract.md#5-named-deviations)): `warm` and `host` then run the scripts'
shared-layout modules, and `fresh` their per-script bundles, as a bundle
built so runs them.

### Step cases

For each step file ([ADR 0003](../../docs/design/0003-semantics-placement.md),
the format in [conformance](../../conformance/README.md#step-cases)) it
writes the cases' scripts and host-provided modules into a sync folder in a
temporary directory, builds them with the
[Bun build helper](../../harnesses/bun/build/README.md) (`bundle.BuildBun`)
into one bundle, whose `manifest.json` lists the host-provided modules in
registration order, and runs each case's runs in order through the protocol
([internal/harness](../../internal/harness/README.md)). In the `warm`
configuration a case's runs share a harness process of its own, in which
module state and `globalThis` survive from one run to the next (D1); in
`fresh` each run has a process of its own, which ends with it, as a
Windmill job's does. A native step runs in a realm of its own every time,
in either (D5). Each script is built with the language a deploy records
(`ir.DeployedLanguage`), and invoked with that language's boundary
(`ir.BoundaryOf`). Each run's entrypoint is `engine.Entrypoint`'s, its
job variables the job identity
([internal/platform](../../internal/platform/README.md)) of step
`step_id` (default `a`) of the flow `f/wl_step/flow` in workspace
`wl_step`, the case's flow run, or of a script job at the case's path
(`step_id: ""`), which is its own effective root
([WA-V1](../../docs/semantics/client-api.md#wa-v1)), and its
outcome is mapped to Windmill's stored value by `engine.StoreOutcome`, then
compared by `conformance.CompareStep`; a case's `timeout` abandons the run
when it passes, and its value is then `engine.TimeoutError`'s for the
boundary. Each case has a platform facade of its own
([internal/platform/facade](../../internal/platform/facade/README.md),
milestone PF1), in its default `fail` policy, through whose `Executor` the
runs go, and which serves the case's `setup`, its variables and resources
(milestone PF2; a secret's value is its plaintext, which the runner passes
in as a host passes its secrets) and an object store in a directory of the
case's own (milestone PF3). Its job registry knows the case's flow run as
a live flow job while the case runs, so the runs of a flow step's case
share its flow user state, in `warm` and `fresh` alike; it has no
dispatcher, so no dispatch target is in the bundle and every dispatch is
refused (ADR 0001 §5a): a warm process gets its non-listening origin as `BASE_URL` and
intercepts its fetches, a fresh one its listening origin, which it
reaches over TCP. A native step reads the same origin as `BASE_URL` and
`BASE_INTERNAL_URL`. A run's `calls` compares what the facade recorded
against the invocation, once it has settled, each as `<status> <METHOD>
<route template>`. Every run is
observed up to its settlement (at most 10 s), since an outcome fixed early
(an intercepted `process.exit`) comes before the call ends: its warnings
are those collected until then, `left` compares the counts of its
`left_behind` warning, and `untracked` those of its `native_untracked`
warning. A run whose call must not settle says `settled: pending` and is
observed for 500 ms instead. A run's late reports are compared, when the
case states them, at the end of its case, so that a late report of work it
left running after it settled, which the D9 fence must stop, fails the
case; in `warm` mode the process is observed for 200 ms more after a last
run that states its late reports or settlement, before it is drained.

A step file with `shared_code: true` (`d18.yaml`) is built
with shared code whatever the flag, so its cases compare `warm`, which runs
the shared-layout modules, with `fresh`, which runs the per-script
bundles. A case's `scripts` add workspace scripts beside its `files`, each
built and invocable; a run's `script` invokes one of them instead of the
case's own script, so one process runs several step modules that share a
dependency.

A case with `codebase` is a codebase script: `check` writes its `files`
into the folder and builds it with esbuild (`bundle.BuildCodebase`), with
its entry's settings and the shims the scan wrote; its runs go to the
harness as any other's. With `--node` (default `WL_NODE`; empty skips
it), each run of a **CommonJS** codebase case also runs on Node, as
Windmill runs such a bundle (deviation D13,
[WR-50](../../docs/semantics/languages.md#wr-50)), unless its header makes
it native, which Windmill gives to the native runtime whatever its
bundle's format (WR-50/c, `onNode`): in a fresh job
directory laid out in Windmill's order: a placeholder `package.json` of
`{}`, then the bundle as `main.js` and the entry's assets at their paths,
so an asset named `package.json` replaces the placeholder and its `type`
decides how Node loads `main.js`; then its metadata, its arguments and
`noderun.mjs`, run as `node --preserve-symlinks noderun.mjs <entry>`. The
driver applies the harness's own wrapper rules (`harnesses/bun/runtime/wrapper.ts`,
transpiled for Node by the pinned Bun) in the harness's order, an
unsigned callable export getting an empty parameter list as on the
harness, and writes
the outcome as a result frame carries it; a process that exits without
writing it ended as `empty` (status 0) or `escaped` (the last 600
characters of its stderr). The outcome is mapped by `engine.StoreOutcome`
and compared with the run's `node` check, or its own expect and claims
(`conformance.CompareStepNode`); differences are reported as `run N on
node: …`.

### Flow fixtures

For each fixture (the format of [OR-15](../../docs/semantics/oracle.md#or-15),
loaded by [internal/conformance](../../internal/conformance/README.md#fixtures)),
a few at a time (`flows.go`):

1. **Build.** It copies the sync tree as `record` pushes it
   (`oracle.CopyLayout`, or in stub mode `oracle.StubTree`, see Effects and
   stub mode below) into the fixture's work directory, and reads the copy,
   never the tree in place. The copy carries what the tree's codebase
   entries name outside it and nothing else. So a file around the tree,
   such as a `tsconfig.json` above it whose path aliases esbuild would
   read, reaches neither this build nor the oracle
   ([ADR 0011](../../docs/design/0011-differential-testing.md)). It reads
   the copy with
   [internal/syncfolder](../../internal/syncfolder/README.md) and compiles
   each run's target flow with [internal/ir](../../internal/ir/README.md),
   the target as the only root, and opens its program on the engine, to
   sort the targets it can run from those the compiler or the engine
   refuses (unsupported) or cannot compile (failed). When the fixture lists
   `roots`, the bundle's other roots (the targets its steps dispatch, which
   a host contract lists, ADR 0002), they are compiled together the same
   way. When it lists `provided` sites (F2a), each compile gets those of
   them its roots reach, as the host contract's mandatory provided sites,
   whose targets stay opaque: a selector that a target's compile reports
   matching no site is left out of that compile. It then builds one bundle of
   [ADR 0010](../../docs/design/0010-packaging.md) with `bundle.Build`
   ([internal/bundle](../../internal/bundle/README.md)), whose contract's
   roots are the targets it can run and the roots, and its provided sites
   the fixture's, with `wl`'s engine
   version and the module state of `--mode`: the program, the Bun scripts
   the roots reach, built with the Bun build helper and each named by its
   inventory id ([ADR 0002](../../docs/design/0002-ir-and-loader.md)),
   `scripts/bun/<path>.mjs` for a path script and `scripts/bun/inline-<16
   hex>.mjs` for an inline step (a script's job path,
   [WR-29](../../docs/semantics/languages.md#wr-29), cannot name it, since
   an inline step `a` of flow `f/x` and a path script `f/x/a` share one),
   codebase scripts ([SY-8](../../docs/semantics/openflow.md#sy-8)) built
   with esbuild with the settings of the `codebases` entry that selects
   them (C2b), the folder's resources and variables but its secrets in
   `data/`, and the manifest. A build problem of one script
   (`bundle.ScriptItem` names it) fails the targets that reach it, and the
   bundle is built again without them; a folder that does not load, or a
   build that fails as a whole, fails every run.
2. **Run.** The bundle is opened with `wl.Open`
   ([wl](../../wl/README.md)) in the mode of `--mode` (`host` builds it
   warm and opens it in host mode, below), on the pinned Bun and
   the repository's harness runtime, with the fixture's named `providers`
   as `wl.Options.Providers` (`fixtureProviders`: each answers its sites
   with its result or its error, or declines), and with its host data
   (`hostDataOf`, `providers.go`): the folder's secrets, as the folder
   holds them (ciphertext without `plainSecrets`), then the variables and
   resources of its `setup.yaml`, which the oracle creates after the push
   and so replace the folder's; a setup applied before the push holds
   nothing at run time, since the push removes it (OR-31). A setup's secret
   is its plaintext, passed in as a host passes its secrets, never through
   a bundle. The runtime owns the platform services (milestones PF1 to
   PF3): its facade, in its default `fail` policy, which attributes every
   call (a warm process reaches it through its intercepted `fetch` at a
   non-listening origin, which a native step reads as `BASE_URL` and
   `BASE_INTERNAL_URL`, a fresh one over loopback TCP); the store of the
   bundle's `data/` and the host data, which the engine reads too; the job
   registry, which knows every run and step; an object store in the
   fixture's work directory; and the default dispatcher over the bundle's
   program, so a step that dispatches one of the `roots` starts a run of
   the same runtime and its calls are attributed. In `warm` mode every run
   goes to one pool of Bun harness processes; in `fresh` mode each step has
   a process of its own. The engine invokes each step with its boundary, a
   native one included (C3n). Runs go in name
   order, a `group` at its first run's place with its runs concurrent
   (`conformance.RunBatches`, `groups.go`), each with `Runtime.Run`. A
   group is one test of
   concurrent outer runs (ADR 0011), so it runs whole or not at all: when
   one of its runs is unsupported, so are the others, and when one fails
   before it starts, the others fail with `group_member_failed`. Each run
   of a group waits at its first module, through the module-start hook
   (H6, passed as `wl.RunOptions.Hooks`), until every run of the group is
   in flight, or for 10 s at most.
   So the runs are in flight together by construction, not by the
   scheduler's chance. After the runs, as `record` does on the oracle, the
   group fails as a whole when one of its runs failed (`group_member_failed`)
   or when one started after another ended (`group_not_concurrent`), by
   each run's span from submission to end. `cancel_after_ms` cancels a
   run's context with a `wl.Cancellation` naming the reason and the
   canceler the oracle's cancellation names
   (`oracle.CancelAfterReason`, `wltest: cancel_after_ms`, by
   `oracle.CancelAfterCanceler`, `admin`), which the engine's `Canceled`
   result quotes ([FX-38](../../docs/semantics/execution.md#fx-38)), and a
   run that has not ended after 150 s fails: sleeps and retry delays are
   waited for on the real clock, and the monitor of flows that hang between
   steps ends a runaway expression's run after 60 s, as on the oracle
   (JE-2/a, milestone N2). Each run has its own id, which its
   steps see as `WM_FLOW_JOB_ID`, `WM_ROOT_FLOW_JOB_ID` and
   `WM_ROOT_JOB_ID`; the trace records each step's job id (`WM_JOB_ID`),
   superseded attempts' included, with its site.
   The engine names an invocation's script by its inventory id, as the
   bundle does, and gives its job path as `WM_JOB_PATH`.
3. **Observe** (`observe.go`). The run's trace becomes the observation the
   oracle's walker produces ([ADR 0011](../../docs/design/0011-differential-testing.md),
   [OR-11](../../docs/semantics/oracle.md#or-11)): a module of the flow, or
   of a child flow the trace lists (a loop iteration's body, below its
   iteration's site `m:<loop>/i:<n>`, a branch's, below `b:<n>` or
   `b:default`, or a subflow's, below its step's site, milestone N3c),
   that the trace has no record of is
   `not_run`, and so is the failure module's site `s:failure` of a flow
   that has one, a child flow's copy of it included (`<child>/s:failure`,
   N3c); the root's result and each
   site's `args` and `result` are in their stored form, as the job API
   returns them ([ADR 0005](../../docs/design/0005-json-step-boundaries.md));
   `carried` and `resolved_args` keep the trace's texts, the value carried
   on in memory and the arguments the script received, and are written only
   where their text differs. The top-level members of `resolved_args`
   come in jsonb's key order, as those of `args` do, so the observation is
   the same in every run. The engine shuffles the order of a step's
   argument keys in each run, because Windmill leaves it unspecified
   ([ADR 0001](../../docs/design/0001-equivalence-contract.md) §2). A site
   with no result has no `carried`
   (a module that failed at the root's pickup with no job carries its
   error to the flow's result). The values are then normalised by
   [OR-14](../../docs/semantics/oracle.md#or-14)'s rules: job ids become the
   placeholders of their sites (the run's own id is the root, `<job:>`, and
   a child flow's id its iteration's or branch's site, as the walker
   reaches it), and
   a failed site's stack is masked wherever its value is carried
   ([OR-V3](../../docs/semantics/oracle.md#or-v3) clause a); then the
   fixture's `stack` and `mask` pointers apply. Retries (milestone N2):
   a failed attempt is a site `<module>/r:<k>`, and a module's record
   lists the failed attempts of the flow-wide retry state, which the walker
   fetches at `<module>/r:<k>` too, so an earlier module's attempt appears
   again under a later module whose status lists it
   ([FX-27](../../docs/semantics/execution.md#fx-27),
   [FX-29](../../docs/semantics/execution.md#fx-29)); a job id becomes the
   placeholder of the site whose record names it, an attempt's own. The
   virtual `<id>-v` step of a final sleep is left out, as the walker leaves
   it ([OF-17](../../docs/semantics/openflow.md#of-17),
   [OR-V7](../../docs/semantics/oracle.md#or-v7)), a child flow's included
   (`ChildFlow.Virtual`, milestone NI). A record or child flow the trace
   marks superseded is left out too, since the walker no longer reaches it
   (milestone NI): what a retried container's failed attempt ran besides
   the job the attempt is recorded as, at `<container>/r:<k>` with a child
   flow's modules below it
   ([CF-22](../../docs/semantics/control-flow.md#cf-22)), which a later
   module that lists the attempt (FX-29) shows again below its own
   `r:<k>`, with everything below it, its modules' own attempts
   included, as the walker walks the attempt's flow there; what a loop ran
   before its `skip_if` held mid-loop
   ([CF-21](../../docs/semantics/control-flow.md#cf-21)); and a collapsed
   iteration's job that was never pushed.
4. **Compare.** With the run's `expect`, `carried` and `resolved_args`
   included, or with its recording when it has none, which holds neither;
   then its `claims` are checked. On the reference's side, the location
   suffixes of a message that matches a catalogue template carrying `<loc>`
   (the recognised internal error, the chaining errors of FX-15, the
   string of FX-21, a failed `stop_after_all_iters_if`'s error stop, the
   worker's execution error) are stripped by FX-50's rule, at most one per
   layer prefix, and a parenthesised location only where the expected text
   writes `<loc>` for it, where windmill-lite generated the value
   (`conformance.StripLocations`, ADR 0011's location rule): a result whose
   origin the trace records as the engine, a harness or the facade; a
   carried value that is the same value, in either member order; a
   carried value the engine set apart from the result, whose origin the
   trace records too (an error stop's, a throwing `stop_after_if`'s, a
   chaining error with no job, milestone N1b); and, inside a loop's or a
   branchall's array, the entries the trace points to as generated
   (`ResultNested`), at any depth (milestones N3a and N3b). A copy of
   such an error that user code passes on, as a failure module or a later
   step that echoes a pickup error does (FX-15/b, JE-35/d), is generated
   only where the run's `copies` declare it (milestone N3c): the value at
   a declared pointer counts when it equals, as JSON, the generated error
   of the site the declaration names (its result or carried value, one
   the observation does not show included), that error's error member, or
   its message. Equality alone never counts, since user code can write
   the same text. A
   parenthesised not-found location ` (<file>.rs:<line>)` inside a
   message is stripped only at the values the run's `not_found_locs`
   declares, and only where windmill-lite generated the value
   (`conformance.StripNotFoundLocations`, FX-50 rule 3, CF-19/c), since a
   user's own message can hold the same phrase (FX-50/d). A carried
   value that differs from the result with no recorded origin is never
   stripped, nor a user's error beside generated ones. Claims are checked
   on windmill-lite's observation as it is, so a claim about a location
   suffix holds only on the oracle. A run's `delays` are checked on the
   trace's job times (milestone N2): each `to` site's job starts at least
   `at_least_ms` after the `from` site's job ended, where the oracle's
   side reads the recording's provenance. A run
   with nothing to compare fails. After each run, one that timed out
   included, the files its steps left
   in `/tmp` to count their attempts across retries, which the fixtures name
   `wl-<fixture>-<job id>-<tag>` after a job of the run, are removed
   (`removeCounters`, milestone FU8): on Windmill they stay in the worker's
   `/tmp`, but a windmill-lite run would leave them in the host's.

**Effects and stub mode** (milestone E4b, `effects.go`). A fixture with
`oracle: {effects: true}` or `stub: true` gets an effect server of its
own: the oracle's effect sink (`oracle.Sink`,
[OR-16](../../docs/semantics/oracle.md#or-16)) with the replay service
(`conformance.ReplayService`) at `/replay` below it, as `record` serves
them, on a free loopback port. Every invocation of a run gets the
server's URL as `WL_EFFECT_SINK` through the engine's
invocation-environment hook (H12), which also records the site each job
(`WM_JOB_ID`) launches at. The hooks go to the run as the Go callbacks a
host gives it (`wl.Hooks`, milestone F2a: `InvocationEnv`, and a group's
`OnModuleStart`, `runHooks`), so the runner takes no path a host does not
have. The sink keeps
the effects whose `root` is the run's id (`WM_ROOT_JOB_ID`), so the runs
of a group stay apart, and refuses any other root with 409 (OR-16/c).
In stub mode the runs run the stubbed copy of the sync tree that `record`
pushes (`oracle.StubTree`, written to the fixture's work directory and
built instead of the tree, with the paths outside the tree that its
codebase entries name carried beside it, as every copy is), and each run's `replay` table answers its
stubs. The service locates a stub's job at the site the hook recorded:
the module's site, never an attempt, since the service counts attempts
itself (ADR 0011). After the run, `conformance.CheckReplay`, the check
`record` applies to its walk, holds the calls against the trace. Each
call must have been answered, with no `replay_miss` or
`replay_unresolved`. Each must have resolved where the trace put its job
(`StepRecord.Job`): a failed attempt `k` at `<site>/r:<k>`, the last
attempt at `<site>`. Every leaf job of the trace must have called;
`stub_not_called` names the sites that did not. A subflow step's job is
its child flow's, and a superseded record is not walked. A record that
ran with no job is an identity job, which runs no script, or a script job
that launched and failed before its invocation (an input transform's
failure, FX-14; no entrypoint, WR-12; a canceled scheduled job, FX-38).
The oracle walks the latter as a script job that never called, so this
does too, at its attempt site. It tells them apart with the module the
site names in the compiled program: a script module that is not mocked
(FX-18), whose record is not flagged skipped (FX-16, FX-17) and whose job
failed. A job that launched with no invocation and did not fail is an
identity job, such as a skipped failure module's, which the trace does
not flag skipped (FX-32). When this check fails, the
run's observation is still compared, so the failing steps are shown too.
The run's
effects are then the sink's in arrival order, followed by each answered
call (`{site, attempt, step, job, flow, root_flow, root, args}`), as the
oracle lists them. They are normalised with the observation's job ids
and compared like the rest of it. A tree the stub generator refuses
(`stub_unsupported`) fails the fixture's runs.

A run is `unsupported` when windmill-lite cannot run it yet: a run of a
fixture that has `roots` and effects or stub mode, because the default
dispatcher's runs get no run hooks and so no `WL_EFFECT_SINK`; a script run
(`kind: script`), a run-and-wait run (`submit: run_wait_result`, whose
response is the HTTP adapter's, F7b), a flow the compiler refuses as an
unsupported feature, language or pinned hash, a script of another harness
(L1), a flow the engine refuses at open (`engine.ErrUnsupported`, which
names the milestone: a container as the failure module (CF-30),
caching, a preprocessor). The fixture's other runs are checked.

### Expression files

An expression-case file of `conformance/exprs`
([ADR 0003](../../docs/design/0003-semantics-placement.md), the expression
level) runs end to end as the generated one-step flow fixture that `record`
lifts it into (`conformance.LiftExprFile`), in a temporary directory: one
flow per case, whose module `b` runs a sink script with the case's
expression as its input `x` (or as its `skip_if`), compared with the case's
recording and claims. `check` takes such a file explicitly, and finds them
in the directories it is given, as `record` does. The unit-level checks of
the same cases are `go test ./internal/expr`'s (milestones B2a and B2b).
Cases that differ by a named deviation fail here as they do there, and
the flow suite does not require them: the transcendental `Math` cases of
JE-1, whose last digit depends on the C library (D16: the Linux runner's
glibc and macOS each differ from Windmill's image on some of them), and
JE-48/a, which does not overflow the 1 MiB stack (D17).

### Tests

`go test ./cmd/wltest` runs both suites. The Bun tests need `WL_BUN`;
without it they skip, unless `WL_REQUIRE_BUN` is set, as CI sets it. The
Node side needs `WL_NODE`; without it the step suite runs on Bun only,
unless `WL_REQUIRE_NODE` is set, as CI sets it (with Node 20, the major of
Windmill v1.767.0's image).

- `TestStepSuite` is the exit gate of milestones C3a and C3b, and with
  `wr-40.yaml` to `wr-45.yaml` and `d5.yaml` milestone C3n's: every step
  case passes, in `warm` and in `fresh`; with `WL_NODE` it is also
  milestone C2b's D13 gate, every CommonJS codebase case holding on Node
  too. `TestStepCheckerReportsDifferences` checks that a wrong expectation
  of each kind fails, in both modes, `TestStepCheckerReportsNodeDifferences`
  that a wrong or unstated Node difference fails, and `TestStepCatalogue`
  that every catalogue case of a finding with a step file is a step case or
  deferred.
  `TestNativeRejectionIsItsInvocationsOwn` checks that a native
  invocation's unhandled rejection of a web API's promise fails it and no
  invocation beside it in the same process.
  `TestNativeFlowSteps` runs a flow's three kinds of native step (nativets
  with and without `//native`, bun with it) through the real build and
  harness, checking each one's boundary and its `BASE_URL` and
  `BASE_INTERNAL_URL`. Milestone FU5 added three such flows:
  `TestNativeGeneratedErrorsAreGenerated` (a native load failure and a
  transpile failure are recorded as generated, so the oracle's location
  suffix is stripped from them, D14), `TestScriptEnvsReachOnlyBunSteps` (a path script's `envs` reach
  its Bun step under `PATH` and the reserved variables, and never a native
  step, WR-22 and WR-45) and `TestNativeCodebaseFlowStep` (WR-50/c reached
  through a flow: a `//native` ESM codebase script runs natively, its
  control on Bun).
- `TestStepSuite` runs `d18.yaml`'s cases from a bundle built with shared
  code ([ADR 0013](../../docs/design/0013-shared-module-code.md)): `warm` runs the shared-layout modules and
  `fresh` the per-script bundles.
- `TestFlowSuite` is the exit gate of milestones E1, N1a, N1b, N3a, N3b,
  N3c, N4, PF2 (with PF1's fixture), N2, PF3, NI, E4b, F1, F2a, N5 and
  E3f, and the flow-suite half of F3b's: every run the flow suite requires
  passes in `warm` and in `fresh`, and in `host` (F3b),
  through `wl` (F1, whose own runs are JE-V2/a and /b and FX-17/b: a retry
  with a variable that the step updates through the job API; F2a, whose
  runs are `host/provided`'s, the run-time half of ADR 0002's provided-site
  policy case). `testdata/flowsuite.json` lists them in
  `runs`, each with the milestone whose pull request first made it pass;
  a run that a recording batch's manifest
  (`internal/oracle/testdata/batches/<batch>.json`) gates on a milestone
  listed in its `implemented` is required too, so a later engine milestone
  adds itself there; recording batches E3e and E3f listed the runs of
  their fixtures that windmill-lite already passes (E3f's native fixtures
  in its native mode), FU5 the `oracle/nul` runs that
  N1b's engine passes, E4b the stub runs and the runs of the probe
  catalogue (`oracle/probes`, a fixture with effects) that the effect sink
  made checkable, and FU8 `control-flow/cf-23`'s `carry_*` runs, route 3
  inside a branchone (CF-23/d to f). A required run stays required. The test runs every
  other fixture of `conformance/flows`, and every expression file of
  `conformance/exprs` as its generated fixture (a run is keyed
  `<file>/<case>`), as well: each must load, and the log
  gives the counts of passing, failing and unsupported runs, and names the
  runs that pass without being required.
- `TestFlowSuite` also requires every required run to pass in `warm` and
  `host` from bundles built with shared code ([ADR 0013](../../docs/design/0013-shared-module-code.md)).
- `TestCacheFixturesFreshAndWarm` is the rest of milestone N4's gate: the
  seven runs of `conformance/flows/execution/fx-40` (FX-40/a to /d) pass
  in `fresh` and in `warm`, since the result cache lives in the engine,
  not in a harness process.
- `TestFlowCheckerReportsDifferences` checks that a run whose result, job
  placeholder, recording or claim differs fails with its differences, that
  a flow the engine refuses is unsupported, that an inline step and a
  path script sharing a job path both run and both see it, that a script
  that does not build fails only the runs that reach it, and that a run
  that holds passes. `TestFlowCheckerIsolatesCodebaseFailures` does the
  same for codebase scripts: one that does not bundle and one whose asset
  collides fail their own flows, while a flow whose codebase script reads
  its asset passes; `TestStepCheckerIsolatesCodebaseFailures` checks the
  same isolation for codebase step cases. `TestCheckBuildsTheTreeRecordPushes`
  checks that `check` builds the copy `record` pushes, with the synthetic
  fixture `testdata/path-alias`, whose codebase script imports through a
  `tsconfig.json` path alias. The run fails its build when the
  `tsconfig.json` lies beside the sync tree, which the copy leaves out, and
  passes when it lies in the tree. `TestObserveTrace`,
  `TestObserveChildFlows`, `TestObserveNestedGenerated`,
  `TestObserveIncludedResult` (an error stop's included array, end to end
  on the engine), `TestCompareRun`,
  `TestCarriedLocation` and `TestObserveFailureSlotAndCarriedOrigin` need
  no Bun: they check the observation of a trace (stored forms, `not_run`
  sites, the failure module's site, a child flow's sites and id,
  placeholders, stack masking, the kept `carried` and `resolved_args`, the
  generated values; `TestResolvedArgsKeyOrder` and `TestKeyOrdered`, that
  every order of a step's argument keys observes the same `resolved_args`
  text) and which reference
  a run is compared with, including the location rule's positive and
  negative cases for a result, for a carried value in either member order,
  and for a carried value with its own origin, and a declared not-found
  location stripped only where windmill-lite generated the value.
  `TestObserveDeclaredCopies` (N3c) checks that declared copies of a
  generated error, and of its message, are generated, while the failure
  module's own error, an undeclared copy, a copy declared against a site
  that generated nothing and one whose value differs keep the reference's
  location; `TestObserveUserValueEqualToAGeneratedError` checks that a
  user's message equal to a later generated error keeps it.
  `TestObserveTraceRetries`
  checks the attempt sites, a later module's copies of an earlier one's
  attempts with the attempt's own placeholder, a listed attempt with no
  record, and the virtual step left out; `TestCheckDelays` checks a run's
  `delays` on the trace's job times (milestone N2).
  `TestObserveContainerAttempts` (milestone NI, end to end on the engine)
  checks a retried loop's failed attempt, its child flow's modules below
  `r:0`, the superseded first attempt left out, and a later module's copy
  of the attempt with its subtree, and `TestObserveNestedAttemptCopies`
  the copy of a body module's carried-over attempt inside that subtree.
- `TestSubflowRetryFixturesFreshAndWarm` is milestone N5's gate. Every
  run of `control-flow/subflow-retry` (a retry on a flow step, and on a
  while loop and a branchone) matches its recording in `fresh` and in
  `warm`.
- `TestStubCasesMatchTheOracle` is milestone E4b's exit gate. It runs
  ADR 0011's stub acceptance cases (`conformance/flows/stub`: a retry
  inside a loop, a collapsed body, nested loops, a named subflow and a
  parallel loop, and two concurrent outer runs), plus E4b's retry inside
  a loop on codebase scripts (`stub/codebase`), N5's retried flow step
  (`stub/subflow_retry`) and E4c's codebase entry whose `relative_path`
  and asset lie outside the tree (`stub/outside`), on windmill-lite. Each run
  must match its `expect` and, with the `expect` dropped, the oracle's
  recording, effects included, in `fresh` and in `warm`. A run without a
  recording fails the test without stopping it. Its group, `scopes` runs
  `r` and `t`, must be in flight together. `TestStubModeAndEffects` checks
  the failures and the identity jobs:
  - a table without an attempt the run makes fails the run with `replay_miss`;
  - an effect that differs is shown;
  - a mocked and a skipped script module need no call;
  - a script job whose input transform failed (FX-14) fails the run as
    never having called;
  - a group whose other run failed fails with `group_member_failed`;
  - a tree the generator refuses fails with `stub_unsupported`;
  - a plain sink fixture observes its own effect while a foreign root gets
    409 and is not observed.

  `TestRunEffectsLocates`, `TestEffectServerKeepsRunsApart`,
  `TestCheckStub`, `TestModuleAt` and `TestRunGroup` need no Bun. They check:
  - the hook;
  - the per-run routing of effects and stub calls;
  - the trace side of the replay check: attempt sites, a subflow step's job,
    identity jobs (an identity module, a mocked one with a failed
    `stop_after_if`, a skipped one, a skipped failure module), a superseded
    job, and a script leaf's attempt that launched with no invocation;
  - the module a site names, through bodies, branches, subflows, repeated
    ids and slots;
  - the group: a run held until the other is in flight, runs one after the
    other failing with `group_not_concurrent`, a failed run with
    `group_member_failed`, and a run that starts no module holding up no
    other.

### Host mode

`--mode host` (milestone F3b, the flow suite half of its gate) runs each
fixture's runs on a Bun host that embeds the host SDK
(`testdata/flowhost/host.ts`), whose steps run in the host's own process
over the host-mode transport (ADR 0004 host mode), as every host's do.
The checker keeps the engine in its own process (`hostmode.go`): the
fixture's bundle is opened with `wl.Options.Host`, the runtime
`windmill-lite host` opens, so that each run's trace, its hooks (effect
sinks, concurrent groups) and its cancellation with a reason stay the
checker's, as in warm and fresh mode.

- **The engine the SDK spawns** is wltest itself (the test binary in
  `go test`), in proxy mode (`WLTEST_ENGINE_PROXY`): it passes the host's
  socket and child channel, its file descriptors 3 and 4, to the checker
  over a Unix socket (`engineProxy`), then waits until the checker has
  closed the runtime, when it exits and the host sees its engine exit and
  exits. It keeps its own copies open until then: on macOS, closing a
  socket while it is still in flight to the checker could end the
  connection before the checker received it, which failed a fixture's
  start now and then.
- **Teardown** (`hostRuntime.close`) closes the runtime, then the host's
  socket and child channel, which the runtime does not own, and the
  proxy's connection; it waits for the host to exit and copies the host's
  output, which went to a file, into the fixture's diagnostics.
- **The watchdog is off,** since the engine is not the host's child here;
  the engine's parent check is the watchdog's (ADR 0004 host mode item 4).
  The watchdog's own cases run on the real topology, in
  `cmd/windmill-lite`.
- **One generation per fixture.** The host lives as long as its fixture;
  one lost before its engine closes, its process exited or its connection
  ended, fails every run it took, whatever those runs observed. The host's
  output goes to a file, not a pipe: the proxy inherits it, and a pipe
  would keep the host's wait open while the proxy lives.

`hostmode_test.go` checks that a host killed after its fixture's runs
fails them all (`TestHostModeHostLossFailsTheFixture`) and that closed
fixtures leave no descriptor open, with garbage collection off
(`TestHostModeClosesItsChannels`).
- **The host's environment** is the base environment a spawned harness
  gets (`harness.BaseEnv`), and its working directory is the fixture's.

`TestFlowSuite` runs the suite in `warm`, `fresh` and `host` mode, and in
`warm` and `host` from bundles built with shared code; every
required run must pass in all five.

## Version

Provenance names the build: the version from `-ldflags "-X main.version=…"`
or the module version, and the VCS revision and modified flag that `go build`
stamps from a checkout.

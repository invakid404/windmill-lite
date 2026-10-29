# 0011 · Differential testing and performance measurement

**Status:** proposed · **Topic:** checking windmill-lite against a real
Windmill (the oracle), the step identity encoding shared by every comparison,
CI, and how performance is measured.

## Context

Almost every expectation in the reference is source-derived; only the
oracle area has recorded observations (36 cases, run 1). The reference
designs the oracle harness ([OR-1](../semantics/oracle.md#or-1) to
[OR-31](../semantics/oracle.md#or-31)) and leaves two core items open: a
collision-free identity for compared steps ([Q-OR-17](../semantics/oracle.md#q-or-17))
and an executable reconstruction of the probe catalogue
([Q-OR-16](../semantics/oracle.md#q-or-16)). Public CI may pull and run the
unmodified CE image as a test oracle; windmill-lite never redistributes,
modifies, wraps or bakes it ([LIC-1](../semantics/licensing.md#lic-1),
[OR-3](../semantics/oracle.md#or-3)). The oracle is a semantic reference,
never a latency baseline ([OR-17](../semantics/oracle.md#or-17)).

## Decision

### The oracle harness

The `oracle` package implements the reference's design as written:

1. **Topology** (image pins [OR-1](../semantics/oracle.md#or-1) and
   [OR-2](../semantics/oracle.md#or-2); topology [OR-4](../semantics/oracle.md#or-4);
   lifecycle and port binding [OR-18](../semantics/oracle.md#or-18)):
   `ghcr.io/windmill-labs/windmill:1.767.0` pinned by its full index digest,
   in `MODE=standalone`, next to `postgres:16-alpine` pinned by digest, data
   on tmpfs, memory limits, ports on `127.0.0.1`; a preflight that requires
   `/api/version` to be exactly `CE v1.767.0`. Fixtures that declare
   `workers: 2` get a second worker container ([OR-5](../semantics/oracle.md#or-5)).
2. **Bootstrap and push** ([OR-6](../semantics/oracle.md#or-6),
   [OR-8](../semantics/oracle.md#or-8), [OR-9](../semantics/oracle.md#or-9),
   [OR-31](../semantics/oracle.md#or-31)): a fresh workspace per fixture run,
   deleted after its runs, because CE refuses a third workspace besides
   `admins`;
   the sync tree pushed with `windmill-cli@1.767.0` from npm on the runner,
   never the CLI inside the image; setup applied after the push; the
   deployment gate polls each lock job to success. A fixture whose tree
   must reach the server as written (`oracle.deploy: api`: a container
   field left out, or a script pinned by the hash the deployment assigns
   it) is created through the REST API instead, never regenerated or pushed
   by the CLI, and passes the same gate.
3. **Run and capture** ([OR-10](../semantics/oracle.md#or-10),
   [OR-V1](../semantics/oracle.md#or-v1), [OR-V2](../semantics/oracle.md#or-v2)):
   asynchronous submission and `get_result_maybe` polling, never
   `run_wait_result` to observe a run; full arguments and results only from
   `get_args` and `get_result_maybe`; status from job metadata. A run that
   records the run-and-wait response itself (`submit: run_wait_result`,
   FX-36's HTTP part) is submitted there, then polled and walked the same
   way, the response joining its observation (milestone E3e).
4. **Walk** ([OR-11](../semantics/oracle.md#or-11) topology,
   [OR-V7](../semantics/oracle.md#or-v7)): by `flow_jobs` children in index
   order, `failed_retries` as attempts, generated `-v` steps omitted, the
   collapse rules never relied on.
5. **Normalise** ([OR-14](../semantics/oracle.md#or-14),
   [OR-V3](../semantics/oracle.md#or-v3), [OR-V5](../semantics/oracle.md#or-v5)):
   only walked job ids replaced; stacks masked only with runtime-error
   provenance; the NUL `InternalErr` location masked and nothing broader;
   provenance kept apart from the compared observation. These rules govern
   oracle-to-oracle comparisons unchanged. Comparing windmill-lite with the
   oracle adds one narrow rule for Rust location suffixes on generated
   messages only (§Location suffixes below,
   [0001](0001-equivalence-contract.md) §2).
6. **Effects** ([OR-16](../semantics/oracle.md#or-16), [OR-V6](../semantics/oracle.md#or-v6)):
   an HTTP sink on the runner, grouped by `WM_ROOT_JOB_ID` against the
   submitted root. windmill-lite injects the same sink variable.
7. **Disk** ([OR-30](../semantics/oracle.md#or-30)): the fail-closed policy
   for local runs (pull only with at least 8 GiB free and 5 GiB left after
   the image; stop below 5 GiB; no automatic resume).

### Step identity encoding (answers Q-OR-17)

A **site path** is a sequence of typed segments, written as text by joining
segments with `/`. Each segment is `<kind>:<value>`:

| Kind | Value | Example |
|---|---|---|
| `m` | a module id in a module list, with `~<k>` appended when the id occurs more than once in that list (`k` counts those occurrences from 0, in list order) | `m:a`, `m:a~1` |
| `i` | a loop iteration index | `i:2` |
| `b` | a branch: `default` or an index for `branchone`, an index for `branchall` | `b:0` |
| `s` | a special slot: `failure` or `preprocessor` | `s:failure` |
| `r` | an attempt number of a retried module, from 0 | `r:1` |

Values are percent-escaped for `%`, `/`, `:` and `~` (`%25`, `%2F`, `%3A`,
`%7E`), so the escape character itself is escaped. Examples:
`m:outer/i:1/m:inner/i:0/m:s`, `m:a/r:0` (the failed first attempt of `a`),
`s:failure`, `m:failure` for an ordinary module that happens to be named
`failure`, and `m:a~0` and `m:a~1` for two modules both named `a` in one list
(Windmill does not require unique ids, [OF-2](../semantics/openflow.md#of-2)).
A unique id carries no suffix, so adding a module elsewhere in a flow does
not change the identities of the others, which keeps replay tables and
recorded fixtures stable across flow revisions. The one exception: adding a
second module with an id that was unique renames the first from `m:a` to
`m:a~0`, so fixtures and replay tables keyed on it must be updated in the
same change.

The encoding is injective: the kind separates ordinary modules from slots,
the occurrence separates repeated ids, and escaping covers the escape
character and the occurrence marker. Negative tests of the encoder: two
modules with one id in one list; the same id in different container bodies
(distinct paths, no suffix needed); an ordinary module named `failure` beside
a real failure handler, and one named `preprocessor` beside a real
preprocessor; ids containing `%`, `/`, `:` and `~`; and cases OR-V8/a–c. A collapsed loop's iterations
appear as `m:<loop>/i:<n>/m:<body>`, the same path a non-collapsed loop gives,
so the shape of Windmill's job tree never leaks into identities. The same
encoding is used by job-id placeholders (`<job:m:a/i:1/m:s>`), host-hook
sites ([0008](0008-host-hooks.md)), run traces, and any replay service keyed
by site. Inside a placeholder, a `>` from a module id is written `%3E`.
Path text never contains `%3E`, since a literal `%` is escaped, so a
placeholder ends at its first `>` and distinct sites never share a
placeholder. That alone does not make a whole normalised string injective:
user text that already looks like a placeholder needs its own escape, and
OR-14's drafted `<<job:` rule is not injective on its own, because a user's
`<` followed by a walked job id gives the same text. The normaliser
(milestone E2) chooses that escape. Cases OR-V8/a–c become negative tests of
the encoder.

**The escape (chosen by E2).** The normaliser writes four kinds of marker
into strings: `<job:PATH>` and the fixed markers `<stack>`, `<loc>` and
`<masked>`. In user text, every maximal run of `<` that is followed by a
marker, or by text beginning `job:`, `stack>`, `loc>` or `masked>`, is
written twice as long; every other `<` is kept, so text without such a run
is unchanged. A reader counts the run of `<` before a marker head: odd ends
in a marker, even is literal. So a user `<job:m:a>` becomes `<<job:m:a>`, a
user `<` before module `a`'s job id becomes `<<<job:m:a>`, and the two no
longer collide. Decoding is a left inverse of encoding, which makes a
normalised string injective in the raw text and the walked ids.

**Container sites (chosen by E2).** A module is a site `m:<id>` whatever
its type. A loop or `branchall` module's record holds its status and, as
its result, its children's results in index order; a `branchone`
module's record holds the chosen body's status and result and
`branch_chosen`. Container modules record no arguments, because the jobs
behind them carry Windmill's own (`{"iter":…}`, the parent's flow args).
Iteration and branch bodies (`i:<n>`, `b:<n>`, `b:default`) are path
segments without records of their own; the modules inside them are sites.
A collapsed iteration is recorded at `i:<n>/m:<body>`, as a
non-collapsed one is. An inline body's child flow carries a copy of the
enclosing flow's failure module ([FX-34](../semantics/execution.md#fx-34)),
recorded at `i:<n>/s:failure` or `b:<n>/s:failure` when that flow defines
one (milestone E3c). The harness walker of `internal/oracle` produces
these records from the oracle, and windmill-lite's runner must produce the
same records from its trace.

### Fixtures and verbs

Flow fixtures use the format of [OR-15](../semantics/oracle.md#or-15)
(`fixture.yaml`, `windmill/`, `setup.yaml`, `runs/*.yaml`, `recorded/*.json`)
under `conformance/flows/` ([0003](0003-semantics-placement.md)). The
`wltest` command has three verbs:

- `record`: run fixtures on the oracle and write `recorded/` with provenance;
- `check`: run fixtures on windmill-lite and compare with `expect`, or with
  `recorded` where `expect` is absent; a failure prints JSON-pointer diffs
  (case OR-15/b);
- `diff`: run both live and compare.

`check` takes `--mode fresh|warm|host`; host mode drives the cases through a
small public Bun test host built on the SDK. Exact comparison is the gate.
Step cases, and expression cases (which are context-free by construction,
[0003](0003-semantics-placement.md)), are lifted into generated one-step flow
fixtures for `record`. Contextual expression cases are flow fixtures already
and are recorded as such.

**Observation fields beyond OR-15.** A step's observation adds three fields
to OR-15's step record. [0001](0001-equivalence-contract.md) §2 compares
them, and the stored fields cannot recover them:

- `module`: the module's own status
  ([FX-1](../semantics/execution.md#fx-1)), which differs from its job's
  after an error stop ([FX-V2](../semantics/execution.md#fx-v2));
- `carried`: the value the module carries on to what follows it;
- `resolved_args`: the arguments after `$var:`/`$res:` resolution
  ([JE-V3](../semantics/expressions.md#je-v3)).

Each is written only where it differs from what the other fields imply
(`module` from the job status, `carried` from `result`, `resolved_args` from
`args`), so most expectations read as OR-15 writes them. `carried` and
`resolved_args` differ when their text does, whitespace aside: the same keys
in another order still count as a difference, because a `key_order: strict`
comparison ([OR-14](../semantics/oracle.md#or-14)) and the producer and
stored routes of [0005](0005-json-step-boundaries.md) can tell them apart. A
comparator expands these absences before it applies a fixture's key-order
policy. The oracle's API
exposes the module status but neither a carried value nor resolved
arguments, so recordings hold neither. A comparison with a recording
therefore leaves `carried` and `resolved_args` out, and a comparison with
`expect` includes them. A fixture shows them to the oracle through what its
flow returns: a step that returns its arguments, a later step's
`previous_result`, or a failure handler's input.

**Location suffixes.** Comparing windmill-lite with the oracle adds one rule
to [OR-14](../semantics/oracle.md#or-14)'s normaliser: at a path where
windmill-lite's value is an error it generated (its trace records provenance
for every error), and the oracle's message matches a catalogue template that
carries `<loc>`, the oracle's Rust location suffix is stripped
([0001](0001-equivalence-contract.md) §2, D14). Nothing else is stripped.
Positive test: a generated `Internal: …` error whose oracle text ends in a
location compares equal to windmill-lite's. Negative test: a user error
`Error("boom @a.rs:1:2")` against `Error("boom @b.rs:3:4")` still differs,
because windmill-lite's value at that path has user provenance.

### Stub mode

`wltest stub <folder>` rewrites a sync tree so that every executed script
body is replaced by a recorder stub that keeps the original `main` signature
(so hydration is unchanged) and asks a run-scoped replay service for its
outcome, keyed by the outer root (`WM_ROOT_JOB_ID`), the site path and the
attempt. On the oracle, the service resolves the site by walking the job
tree; on windmill-lite it reads the run trace. Orchestration of flows whose
real steps need private systems can then be compared on the oracle without
those systems; the flows and tables stay with whoever owns them. Outcomes
are idempotent per raw job id, and the attempt number counts from 0, as the
`r:` segment does: it is the number of distinct job ids already seen at that
site in the run. Milestone E4a owns the stub generator and the replay
service, with acceptance cases on the oracle for a retry inside a loop, a
collapsed body, nested loops, a named subflow and a parallel loop under one
outer run, and two concurrent outer runs. Milestone E4b runs the same cases
on windmill-lite, once the container and retry milestones they exercise have
merged.

**The stub (chosen by E4a).** A stub keeps what Windmill's signature parser
reads and nothing that runs: `main`'s declaration with its parameter list
verbatim, since the parser finds a `function main` declaration, exported or
not, and binds arguments by the names found there
([WR-6](../semantics/languages.md#wr-6), [WR-7](../semantics/languages.md#wr-7)),
and every top-level type alias and interface verbatim, since local aliases
decide `Date` hydration ([WR-8](../semantics/languages.md#wr-8),
[WR-V3](../semantics/languages.md#wr-v3)) and types are erased before code
runs. The declared `main` is never called, so its parameter defaults never
run; the module exports a generated function under the name `main`, which
the wrapper calls with the arguments that list binds, and which no
parameter of the user's can shadow. It also keeps the leading block's
`npm`, `nodejs`, `nobundling` and `sandbox` flags, which decide how Windmill
runs the script ([WR-31](../semantics/languages.md#wr-31)). Imports,
top-level code and the body are dropped. Only Bun scripts and `!inline` Bun rawscripts are stubbed; the
generator refuses a tree in which some executed script would keep its body
(another language, the `native` annotation, content written in
`flow.yaml`, a hub or out-of-tree target, a script step pinned by hash,
which selects a deployed version rather than the stub pushed at its path
([OF-14](../semantics/openflow.md#of-14)), a `flowscript` step, an
AI-agent step), a retry on a subflow, loop or branch step (the walk puts a
failed attempt's inner steps under `r:<k>`, which a table does not
address), and a script whose `main` Windmill would not call as the stub's
(no `function main` declaration, one not exported directly or through an
export list (a type-only entry exports nothing), a re-exported,
default-exported, generic, generator or overloaded `main`, a
`preprocessor`). `wltest stub` writes the stubbed copy to a new directory
outside the tree, decided by file identity rather than spelling, never in
place. Because the copy's metadata still describes the
originals, `record` runs the CLI's `generate-metadata` in it and requires
every path script's generated schema to equal the original's: the CLI
derives the schema from the signature, so this checks on the oracle that the
stub kept it. Inline scripts have no schema; for them the scanner's cases,
and the arguments each recorded call shows, are the evidence.

**The replay service (chosen by E4a).** It is served by the effect sink's
server at `$WL_EFFECT_SINK/replay`, so the stub needs no new whitelisted
variable. A stub POSTs `{root, root_flow, flow, job, step, args}`
(`WM_ROOT_JOB_ID`, `WM_ROOT_FLOW_JOB_ID`, `WM_FLOW_JOB_ID`, `WM_JOB_ID`,
`WM_FLOW_STEP_ID`, and the arguments it received in parameter order). The
service routes by `root`, refusing a root that is no open run; resolves the
job's module site (on the oracle by walking the job tree while the run is
in progress: a module's job and its `failed_retries` are at the module's
site, a leaf in `flow_jobs` is a collapsed iteration at `i:<n>/m:<body>`);
counts the attempt as defined above; and answers the run's table, which a
fixture gives per run as `replay:` (site to outcomes by attempt, each
`{result: …}` or `{error: {message, name?, …}}`, the error's other members
becoming the thrown error's own properties). A site or attempt the table
lacks is refused (`replay_miss`), which fails the run. Every answered call
is recorded as one of the run's effects, `{site, attempt, step, job, flow,
root_flow, root, args}`, normalised like any value, so a recording shows
where each call resolved and which scopes Windmill reported. After the run,
each call must agree with the walk (a failed attempt `k` at
`<site>/r:<k>`, the module's last job at `<site>`) and every walked job that
executed a script must have called, or the run fails. A fixture opts in
with `stub: true`; runs that share a `group` are submitted together as
concurrent outer runs, and fail together when one of them fails or when
their root jobs were not in flight together.

### CI

1. **Every pull request** (no Docker): `wltest check` on all levels, in
   `fresh` and `warm`; host mode once milestone F3b lands; the
   **image lint** (no Dockerfile `FROM ghcr.io/windmill-labs/`, no
   `docker push`/`docker tag` of a Windmill image); the **manifest guard**,
   which rejects AGPL-derived Windmill packages (for example
   `windmill-parser-wasm-*`) and, until [Q-OF-7](../semantics/openflow.md#q-of-7)(a)
   is decided, the metadata-only Apache packages, in windmill-lite's own
   manifests (cases LIC-1/a, LIC-1/b).
2. **Oracle workflow** (nightly, on a `oracle` label, and on changes under
   `conformance/` or to the harness itself, `internal/oracle/`,
   `internal/conformance/`, `cmd/wltest/`, the decoders it reads fixtures
   with (`internal/openflow/`, `internal/yamlx/`), the sync-folder reader
   the stub generator uses (`internal/syncfolder/`) and the workflow): `wltest record`
   on `ubuntu-latest` amd64, failing when a recording differs from the
   committed one, when a run has no recording, or when the sync trees'
   generated metadata drifts ([OR-8](../semantics/oracle.md#or-8)). A changed recording is
   committed by a pull request that also updates the case's provenance and,
   where it settles a contested finding, the one engine function that
   implements it ([0001](0001-equivalence-contract.md) §6).

### Performance

1. **Micro-benchmarks** (milestone G1): the boundary costs of
   [0005](0005-json-step-boundaries.md) and the protocol round trip of
   [0004](0004-harness-protocol.md). The expression engine has its own
   **bounded-pool benchmark** in B2b, which gates that milestone
   ([0003](0003-semantics-placement.md)): cold and warm bursts, sustained
   concurrency below and above the pool size, replenishment under load,
   inputs of 4, 64 and 256 KB, on the native backend, reporting p50, p95 and p99
   latency (including the wait for a prepared slot), CPU per evaluation and
   memory per slot.
2. **Run spans.** Every run trace carries durations per module, per
   invocation (queue, harness, serialisation), per expression evaluation and
   per platform call, plus every requested wait.
3. **Comparing against a baseline implementation** (a host's existing code,
   never the oracle): run both on the same seeds with the `record` clock
   (H10), so deliberate waits are recorded rather than slept; report elapsed
   time, CPU and peak RSS at p50 and p95 over at least 50 runs after warm-up,
   with request counts. Equal request counts and equal recorded waits are
   checked first: a difference there is a semantic difference, not a
   performance one.

## Consequences

- Every comparison, placeholder, hook and trace uses one identity encoding.
- Milestone K1 added the three observation fields beyond OR-15, so that a
  successful job under a failed module, an error stop masked by a recovering
  handler, and a wrongly resolved argument that the script ignores all stay
  visible in a comparison.
- Milestone K1 added the `%3E` rule for placeholders. Without it, a string
  holding module `a`'s job id followed by `b>` would normalise to the same
  text as the job id of a module named `a>b`. Windmill puts no restriction
  on module ids ([OF-2](../semantics/openflow.md#of-2)). K1 also recorded
  that the escape for placeholder-like user text is still E2's to choose.
- Recording batch E3c found three things on the oracle that the
  harness had to follow. CE allows two workspaces besides `admins`, so each
  fixture's workspace is deleted after its runs. A completed `branchall`
  status entry need not say that it is one, so the walker takes that from
  the fixture. An empty flow's status holds one entry with a nil job that is
  no module. E3c also placed a body's failure-module copy at
  `<body>/s:failure`.
- Milestone E2 chose that escape and the container sites above. It also
  runs the oracle workflow when the harness changes, not only the fixtures,
  because a harness change can change every recording.
- Recording batch E3a lifted expression cases as this record says: one
  generated fixture per expression file, whose tree (one flow per case,
  each a single step that runs a sink script returning `null`, so the
  recorded argument is the observation) lives only for the session and gets
  its metadata from the CLI before each push; the recordings are committed
  under `conformance/exprs/recorded/`. Runs gained `claims` (partial
  assertions in the catalogue's terms, such as a message's prefix),
  `delays` (lower bounds on a sleep, read from provenance timings, which are
  never compared) and `unit` cases, all checked against the committed
  recordings by Go tests rather than by `record`, so a contradicted claim
  still leaves its recording to inspect.
- E3a met the workspace limit and the completed `branchall` entry of E3c's
  item independently; the session also deletes a workspace whose deployment
  failed, under a context of its own.
- Milestone E4a chose the stub's shape and the replay protocol above, and
  answers [Q-OR-10](../semantics/oracle.md#q-or-10) with its recordings
  (`conformance/flows/stub/scopes`): inside a named subflow and inside each
  parallel iteration, beside a concurrent outer run, `WM_ROOT_JOB_ID` is
  the submitted root, while `WM_ROOT_FLOW_JOB_ID` and `WM_FLOW_JOB_ID` are
  the subflow's or the iteration's own job; in sequential iterations and a
  collapsed body `WM_ROOT_FLOW_JOB_ID` stays the root. Its first recording
  met CE's two-workspace limit independently of E3c. The stub locator
  follows E3c's walker rules (a body's failure-module copy, `branchall`
  from the fixture, the empty flow's entry).
- The oracle runs in public CI as a black box and only there, never inside a
  windmill-lite artefact.
- Milestone E3d added the API deployment of item 2. The CLI path cannot
  push a container field absent, because `generate-metadata` writes the
  defaults and the drift gate requires its output, and a tree cannot hold a
  script hash before the server assigns it. An API-deployed tree is read
  narrowly (Bun scripts, flows, their `!inline` files, the hash
  `0000000000000000` meaning "pin to the deployed version"), and each
  recording's provenance holds the target flow's submitted value, the value
  the API answers after the gate, and what `generate-metadata` writes for
  the same source, run on a copy.
- Disputed findings get settled by recordings, early, through a reviewed
  pull request that changes one function.
- Milestone E1 implemented `check` for flow fixtures. The runner turns a
  run's trace into the walker's records: a module the trace has no record
  of is `not_run`; stored values (the root's result, each site's arguments
  and result) are in their stored form, and `carried` and `resolved_args`
  are the texts the trace holds. It normalises them by OR-14, with the run's
  id as the root's job id and each invocation's id as its site's, and
  compares them with `expect`, or with `recorded` where there is none. The
  location rule above is `conformance.StripLocations`, applied on the
  reference's side at the values the trace says windmill-lite generated: a
  result whose recorded origin is windmill-lite's, and a carried value that
  is the same value. Steps run the scripts the compiler's inventory names,
  never a script found by its job path, which two scripts can share.
- Milestone N3a added a template to the location rule beside N1b's: the
  error stop of a `stop_after_all_iters_if` that fails to evaluate, an
  `EarlyStopError` whose cause, after the quoted expression, is Windmill's
  generic display and loses its suffixes the same way; a user's
  `error_message` matches no template. Inside a loop's array the trace
  points to the errors windmill-lite generated (`ResultNested`), at every
  depth up to the run's result, and the rule applies there only. A child
  flow's modules are observed below its iteration's site, `not_run` where
  the trace has no record of them, its job id is that site, and a site
  with no result shows no `carried`.

- Recording batch E3e added four harness options, each recorded by its
  fixtures. A run may be submitted through the synchronous run-and-wait
  endpoint (`submit: run_wait_result`, item 3): the run is still polled to
  completion and walked, and the observation gains a `response` (status,
  the Content-Type as sent, and the body: text when it is UTF-8, else
  base64), so FX-36's HTTP part and `early_return` (FX-37/a) are recorded
  for the HTTP adapter. The body is text because its spelling is what a
  client sees; the normaliser rewrites only job ids in it, and the stack of
  a runtime error where the body holds that error's object. A redirect is
  recorded, never followed. A fixture
  may apply `setup.yaml` before the push, which must then remove what it
  seeded, or before every run (`oracle.setup`), and every session reads
  `setup.yaml`'s variables back after the push (OR-31/b). An API-deployed
  fixture may read its tree back with the CLI's `sync pull`
  (`oracle.pull`; CF-6/b, SY-6/c). The oracle workflow runs a second
  session with two workers (OR-5/b, Q-OR-7, Q-OR-15) and
  `LISTEN_NEW_EVENTS_INTERVAL_SEC=10`, under which the smoke fixture's
  recordings must hold (OR-7/a); the first session skips the fixtures
  that need two workers. E3e also rebuilt the probe catalogue as
  executable fixtures (`conformance/flows/oracle/`), which answers
  [Q-OR-16](../semantics/oracle.md#q-or-16): the recipes OR-V9 disputes
  run with their bindings written out.

## Spec IDs relied on

OR-1 to OR-31, OR-V1 to OR-V8, OF-2, Q-OR-10, Q-OR-16, Q-OR-17, LIC-1,
Q-OF-7, WR-6 to WR-8, WR-18, WR-V3, and the conformance catalogue.

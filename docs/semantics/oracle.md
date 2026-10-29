# The oracle: differential testing against a real Windmill

This area specifies the oracle harness: how windmill-lite runs synthetic
fixtures on an unmodified Windmill Community Edition (CE) v1.767.0, captures
each run's result, error, per-step results and timings over the API,
normalises them, and compares them with windmill-lite's own observations. It
also records the Windmill behaviours that were observed on that oracle. The
semantics behind those behaviours belong to [openflow.md](openflow.md) (flow
model and deployment), [execution.md](execution.md) (flow execution),
[control-flow.md](control-flow.md) (loops, branches, retries),
[expressions.md](expressions.md) (input transforms),
[languages.md](languages.md) (the Bun wrapper) and
[client-api.md](client-api.md) (the client surface); the licence position is in
[licensing.md](licensing.md#oracle-image). Oracle findings use the `OR-` prefix.

**Pinned version:** Windmill `v1.767.0` (commit `6de4ec0f665ad856002495c4d86269105ec28ad7`).
Source pointers are `path:line @ v1.767.0` relative to the Windmill repository
root. Status, scope and confidence tags are defined in the [index](README.md#legend).

**ID prefixes in this file:** `OR-` oracle findings (they use `OR-` because
[openflow.md](openflow.md) keeps `OF-`); `OR-Vn` findings were added during
validation. Open questions are `Q-OR-n`.

<a id="conventions"></a>
## Conventions

- **Kinds of rule.** Inside a Rule, **Windmill:** marks what the pinned Windmill
  does (source-traced or observed on the oracle). **Harness:** marks a
  requirement on windmill-lite's oracle harness, the test tool this area
  designs; it is not Windmill behaviour. Guidance for windmill-lite itself is
  kept in separate **Design note (not Windmill behaviour)** bullets.
- **Oracle run 1.** "Observed on the v1.767.0 CE oracle" means recorded in
  oracle run 1: the minimal compose of the [Runbook](#runbook) on an arm64
  host, one standalone worker, 26 synthetic flows and 45 runs, every fixture
  pushed with `windmill-cli@1.767.0`. Its raw captures and fixture tree were
  not retained; the [Probe catalogue](#probe-catalogue) is the durable record
  of what ran and what was observed.
- **Committed recordings.** Since milestone E2 the oracle also runs in public
  CI (GitHub-hosted `ubuntu-latest`, amd64, one standalone worker unless a
  fixture asks for two) through the harness of design record 0011, and each
  recording is committed beside its fixture under `conformance/flows/` or
  `conformance/exprs/`: `recorded/<run>.json` holds the normalised
  observation and its provenance (image digest, CLI version, harness
  revision, the CI run URL, and every job's kind and timings). The smoke
  fixture (`conformance/flows/oracle/smoke`, E2), recording batches E3a to
  E3d (`conformance/exprs`, `conformance/flows/je`, `conformance/flows/cf`,
  `conformance/flows/execution`, `conformance/flows/control-flow`),
  E4a's stub fixtures (`conformance/flows/stub`) and recording batch E3e
  (`conformance/flows/oracle`, `conformance/flows/languages` and further
  fixtures in the directories above) are recorded this way. Since E3e the
  oracle workflow runs a second session with two workers, which records
  the fixtures that declare `oracle: {workers: 2}` and the smoke fixture
  again under another listen interval ([OR-5](#or-5), [OR-7](#or-7)).
  "Recorded" in this area names one of these recordings; "observed on the
  v1.767.0 CE oracle" with no fixture named still means oracle run 1.
- **API paths** are relative to the oracle's base URL (`http://localhost:8000`).
  "openapi" means `backend/windmill-api/openapi.yaml` (Apache-2.0); a number
  after it is a line number in that file.
- **Step paths and placeholders.** The drafted semantic path of a step is its
  module id (`a`), `id[i]` for loop iteration or branch `i`, `id/child` for a
  nested step, `id#k` for the k-th failed retry attempt, and `failure` /
  `preprocessor` for the special slots. Normalisation writes a walked job id as
  `<job:PATH>`. This path encoding is **disputed** ([OR-V8](#or-v8),
  [Q-OR-17](#q-or-17)): use it to read the examples, not as a final identity
  scheme. Other placeholders: `<ws>` (workspace), `<stack>` (masked runtime
  stack), `<loc>` (in a normalised observation, the masked source location
  of one recognised internal error, [OR-27](#or-27); in an expected message
  text, Windmill's Rust location suffix, [OR-14](#or-14)). In quoted raw values, `<worker>`, `<job>`, `<uuid>` and
  `<64-hex>` stand for volatile parts elided in this document.
- **Recipe notation** (probe catalogue): every probe is a Bun `rawscript`
  unless noted; `x→v` means input transform `x = <javascript v>`; "static"
  marks a static input transform.

## Summary for the design

1. **The oracle is two containers.** The unmodified CE image
   `ghcr.io/windmill-labs/windmill:1.767.0` pinned by its full digest
   ([OR-1](#or-1); abbreviated digests are not usable pins) runs in
   `MODE=standalone` (API server and one worker in one process, no privileged
   mode), next to `postgres:16-alpine` pinned by digest, with its data on tmpfs
   and a 1 GiB memory limit. Observed: healthy 6 s after `up` on arm64 and
   6.4 s on amd64 in CI, `/api/version` =
   `CE v1.767.0`, Bun `1.3.10`. Public CI may pull and run the image unmodified
   as a test oracle; windmill-lite never redistributes, rebuilds, modifies or
   wraps it ([OR-1](#or-1)–[OR-4](#or-4)).
2. **Fixtures are pushed by the pinned CLI.** `windmill-cli@1.767.0` from npm,
   run on the harness host, is the reference loader for the sync tree. Commit
   exactly what `generate-metadata` writes and gate drift by regenerating and
   diffing. `sync push` removes selected remote items that are absent locally,
   so push only into harness-owned workspaces and seed variables and resources
   after the push ([OR-8](#or-8), [OR-31](#or-31)). CE refuses a third
   workspace besides `admins`, so the harness deletes each fixture's
   workspace after its runs ([OR-6](#or-6)).
3. **Run asynchronously and capture through dedicated endpoints.** Gate on the
   lock jobs reaching success (a persisting `job_id` is not a pending flag),
   submit with `jobs/run/f` and poll `get_result_maybe`. Use
   `run_wait_result` only for a run whose HTTP response is under test, and
   then poll and walk the job as well (it rewrites results into HTTP
   responses and cancels the job on disconnect). Take full args and results
   only from `get_args` / `get_result_maybe`, because `jobs_u/get`
   substitutes `WINDMILL_TOO_BIG` for large payloads and gives args in a
   varying key order. Derive status from job metadata: canceled > skipped >
   success/failure ([OR-9](#or-9), [OR-10](#or-10), [OR-V1](#or-v1),
   [OR-V2](#or-v2)).
4. **Walk `flow_status` by its children, never by module `job`.** Children and
   aggregates come from `flow_jobs` in index order; a missing `job` means not
   run; the nil UUID means no child ran and is read together with the module
   type (success or failure); generated `-v` modules are omitted from the
   projection; collapse rules are known from source but never relied on. A
   completed loop or `branchall` entry keeps `flow_jobs` but drops `iterator`
   and `branchall`, so only the flow definition tells the two apart; job kinds
   follow how each body was stored at deploy and are never the walker's key
   ([OR-28](#or-28)). **Do
   not implement the drafted path strings as unique identities**: they collide
   ([OR-11](#or-11), [OR-V7](#or-v7), [OR-V8](#or-v8), [Q-OR-17](#q-or-17)).
5. **Key order is a provenance model.** Stored values (script arguments, older
   results, retry and aggregate fallbacks) arrive in jsonb order and then
   JavaScript enumeration order; an in-memory straight-line `previous_result`
   keeps insertion order; a script's own preprocessor's args keep their own
   order, while a flow preprocessor's result is stored and read back in jsonb
   order; top-level `flow_input` keys in transforms come in a random order per
   run and are compared as a multiset. Recorded: the ordinary path,
   integer-index keys, a retry's predecessor, a loop aggregate and a flow
   preprocessor's object; a script's own preprocessor is source-derived only
   ([OR-13](#or-13), [OR-23](#or-23), [OR-24](#or-24), [Q-OR-11](#q-or-11),
   [Q-OR-12](#q-or-12)).
6. **Normalise narrowly and compare a projection.** Replace only walked job
   ids; mask stacks only with runtime-error provenance; keep handler-returned
   user data and duplicate array elements; mask only the location of the NUL
   `InternalErr`; keep provenance (versions, dates, timings) apart from the
   compared observation ([OR-14](#or-14), [OR-V3](#or-v3), [OR-V5](#or-v5)).
7. **Compatibility edges observed on the oracle:** a Bun string result
   containing NUL fails with `InternalErr` (a core requirement for that scoped
   case); an empty chosen `branchone` gives the next step the string `"{}"`; an
   empty `branchall` branch contributes `{}`; an empty loop gives `[]`; a
   failure handler's plain result becomes the failed root's result
   ([OR-12](#or-12), [OR-25](#or-25), [OR-26](#or-26), [OR-27](#or-27)).
8. **Effects are grouped by the outer run** (`WM_ROOT_JOB_ID` checked against
   the submitted root), never by `WM_ROOT_FLOW_JOB_ID`, which names the
   innermost root flow. Recorded: inside a subflow by path and in each
   parallel iteration `WM_ROOT_JOB_ID` is still the submitted root while
   `WM_ROOT_FLOW_JOB_ID` is that subflow's or iteration's own job
   ([OR-16](#or-16), [OR-V6](#or-v6), [Q-OR-10](#q-or-10)).
9. **CE versus EE:** on CE the asynchronous flow worker skips completion
   cleanup (`delete_after_use`, `delete_after_secs`); synchronous API deletion
   paths are not gated. A deployed flow does not even carry a module's
   `delete_after_use`: the deployment re-serialises the flow without it. No
   wider CE/EE claim is made ([OR-21](#or-21)).
10. **One core item has not converged:** a collision-free identity encoding
    ([Q-OR-17](#q-or-17)). It qualifies any claim that no open question
    blocks the design. The probe catalogue's executable reconstruction, the
    other item that had not converged, was recorded by batch E3e
    ([Q-OR-16](#q-or-16), answered). Every other open question names the
    oracle run that settles it. The oracle is a semantic reference, never a
    latency baseline ([OR-17](#or-17)).

## A. Oracle image, licence and topology

<a id="or-1"></a>
### OR-1 · Pin the oracle image by full version and digest

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:** the CE oracle image is `ghcr.io/windmill-labs/windmill:1.767.0`.
    Its multi-arch index (linux/amd64 + linux/arm64) has digest
    `sha256:9239264a7a849deedda87868147e65eb910456039fe9d961901e2e5b143ebd43`.
    - Labels: `org.opencontainers.image.revision=6de4ec0f665ad856002495c4d86269105ec28ad7`
      (the pinned commit), `version=1.767.0`, `licenses=NOASSERTION`, created
      2026-07-22. Default command `windmill`, working directory `/usr/src/app`,
      port 8000 exposed, `TZ=Etc/UTC`.
    - The arm64 image pulled by that digest has image ID `sha256:a5f82726…37fd4`
      and `Created 2026-07-22T23:31:36Z`, matching the label.
    - Release CI publishes the `1.767.0` and `1.767` tags; `1.767` moves with
      patch releases.
    - Other variants at 1.767.0: `windmill-ee` `sha256:0e743cd6…983c`,
      `windmill-full` `sha256:b72348cd…054a`, `windmill-slim`
      `sha256:73aa1769…5aca` (abbreviated as recorded).
    - `GET /api/version` returns `"CE {GIT_VERSION}"`; an EE image returns
      `EE v1.767.0`. Observed on the v1.767.0 CE oracle: exactly `CE v1.767.0`.
  - **Harness:**
    - always reference `1.767.0@sha256:9239264a…ebd43` with the full digest
      above;
    - pin Postgres by digest too:
      `postgres@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab`
      (image ID `sha256:c54a696c68013b2f8f42f479e00a2b6ad24c5ef10ee31cdfb8972cec3dad7eb7`,
      288,118,565 bytes);
    - the preflight asserts the exact version string and checks image identity
      through `RepoDigests`. Any other version aborts the run with
      `{"error":"oracle_version_mismatch","got":"…"}`.
- **Evidence:** `.github/workflows/docker-image.yml:76-100` (CE build with
  `features=ce`, semver tags); `version.txt` = `1.767.0`;
  `backend/windmill-api/src/lib.rs:1166-1174` (version format); registry
  manifests and image config inspected 2026-10-27; observed on the v1.767.0 CE
  oracle.
- **Conformance:** [OR-1/a, OR-1/b](conformance.md#or-1)

<a id="or-2"></a>
### OR-2 · Image variants: Bun is 1.3.10, slim is amd64-only, the in-image CLI is unpinned

**Scope:** core (Bun); later (other languages) · **Confidence:** high (source and oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - **Bun:** the main and slim images both copy Bun from `oven/bun:1.3.10`.
      Observed: `Bun.version` = `"1.3.10"`.
    - **Deno:** 2.2.1, main image only.
    - **Main image (`windmill`):** Python 3.11 and 3.12 via uv, Go 1.26.0, Deno,
      Bun, PowerShell, kubectl, helm, Node 20, PHP and others; 37 layers.
    - **Slim image (`windmill-slim`):** amd64 only. Bun, uv + Python 3.12,
      nsjail, and the server binary copied from the `windmill:dev` image built
      just before it (its revision label matches the pin). No Deno, no Go; on
      arm64 hosts it runs only under emulation.
    - **Full image (`windmill-full`):** adds Rust, C#, Java, Ruby, R and more;
      far too large for an oracle.
    - **In-image `wmill` CLI:** every image installs it with
      `bun install -g windmill-cli` and no version, so it is whatever was
      latest at build time.
    - **Sizes.** Compressed totals: `windmill` 1323 MB arm64 / 1373 MB amd64;
      `windmill-slim` 713 MB amd64; `windmill-full` 2300 / 2408 MB;
      `windmill-ee` 1361 / 1411 MB. Measured on-disk size of `windmill`:
      3,940,469,084 bytes on arm64 (the gzip trailers of its 37 layers sum to
      3,820 MiB uncompressed) and 4,026,404,788 bytes on amd64 (CI, E2's
      first recording session, oracle run 40649910412). Each figure holds
      only for its architecture.
  - **Harness:**
    - never use the in-image CLI; use `windmill-cli@1.767.0` from npm
      ([OR-8](#or-8));
    - windmill-lite's Bun harness uses Bun 1.3.10 for differential runs, or
      the different version is recorded in provenance. A Bun version different
      from the oracle's fails the provenance check; it never silently lets
      other semantic differences pass.
- **Evidence:** `Dockerfile:283,285,288` (Deno 2.2.1, Bun 1.3.10, unpinned
  CLI); `Dockerfile:141` (Go 1.26.0); `docker/DockerfileSlim:26-110` (slim
  contents) and `:73,77` (slim Bun and CLI); `docker/DockerfileFull:1-40`;
  `.github/workflows/docker-image.yml:505-546` (slim built for `linux/amd64`
  only); registry manifests; observed on the v1.767.0 CE oracle; the amd64
  size from the session report of oracle run 40649910412.
- **Conformance:** [OR-2/a, OR-2/b](conformance.md#or-2)
- **Revision 2026-10-28 (oracle, milestone E2):** the finding said the amd64
  on-disk size was unmeasured. E2's first CI session measured it
  (4,026,404,788 bytes); the confidence tag no longer carries the
  qualification, and [Q-OR-2](#q-or-2) is answered.

<a id="or-3"></a>
### OR-3 · Licence of the CE image, and its use as a public CI oracle

**Scope:** core (policy) · **Confidence:** high (facts) · **Status:** confirmed

- **Rule:**
  - **Windmill:** the published Community Edition image is **not** pure AGPL.
    - It is built with `features=ce`. The `ce` feature enables `ce_core`,
      which in turn enables `oss_core`, `private` and `operator`.
    - Release CI checks out `windmill-ee-private` and copies it in
      (`substitute_ee_code.sh --copy`) before building. The image label is
      `licenses=NOASSERTION`.
    - **Terms** (repository `LICENSE`): free use of all CE features, subject
      only to the in-software limits and quotas; a right to distribute it "as
      is"; no right to "sell, resell, serve as a managed service, modify or
      wrap under any form" without an agreement. The README adds that a
      commercial licence is needed to "build a feature on top of" CE that is
      sold or embedded in a distributable product.
  - **Decision (decided):** public CI **may** pull and run the unmodified
    Windmill CE image as a test oracle. That is use, which its terms allow.
    windmill-lite never redistributes the image, mirrors or re-pushes it,
    bakes it into its own images (no `FROM` it), modifies it, wraps it, ships
    it inside a windmill-lite artefact or exposes it as a service. The image is
    configured only through environment variables and API calls and used only
    as a black-box oracle. See [licensing.md](licensing.md#oracle-image).
  - **Harness:** a CI lint enforces the decision: no
    `FROM ghcr.io/windmill-labs/…` in any Dockerfile in the repository, and no
    `docker push` or `docker tag` of a Windmill image in workflows.
  - **AGPL-only alternative (optional):** a source build with the `oss` feature
    set (`oss_core` + `all_languages` + `no_auth`) contains no proprietary code.
    It costs a full Rust and frontend build; whether it behaves like CE for
    core flow semantics is unverified ([Q-OR-1](#q-or-1)). It is not a
    prerequisite for the CE oracle.
- **Evidence:** `LICENSE:1-31`; `README.md:237`; `backend/Cargo.toml:180-201`
  (`oss_core`, `ce_core`, `oss`, `ce`, `ee`);
  `.github/workflows/docker-image.yml:49-75` (private checkout and
  substitution); image label `licenses=NOASSERTION`.
- **Conformance:** none (policy; enforced by the CI lint above).
- **Note:** the npm licence field of `windmill-cli` ("Apache 2.0") is not
  evidence that CLI source may be copied into windmill-lite; the harness only
  runs the published package as a tool ([OR-8](#or-8)).

<a id="or-4"></a>
### OR-4 · Minimal topology: Postgres plus one `windmill` container in standalone mode

**Scope:** core · **Confidence:** high (source; oracle-observed on arm64 and amd64) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - `MODE` defaults to `standalone`: the API server and a worker run in one
      process. `NUM_WORKERS` defaults to 1; a value above 1 is forced back to 1
      unless `NATIVE_MODE=true` or `I_ACK_NUM_WORKERS_IS_UNSAFE=1`. For more
      workers, add containers ([OR-5](#or-5)).
    - No privileged mode is needed. nsjail is off by default (`DISABLE_NSJAIL`
      defaults to true). PID-namespace isolation through `unshare` is only
      attempted: if its self-test fails, Windmill logs an error and runs jobs
      unisolated; it panics only when `ENABLE_UNSHARE_PID=true`.
    - Migrations run on first boot. The official compose uses separate
      `server` and `worker` containers and `postgres:16`; Windmill's
      integration tests use `postgres:14`. Windmill's rule of thumb is one
      worker per vCPU and 1–2 GB of RAM.
    - Health: `GET /api/health/status` reports `status`
      (`healthy`/`degraded`/`unhealthy`), `checked_at`, `database_healthy` and
      `workers_alive`.
  - **Harness (profile):**
    - `postgres:16-alpine` (`POSTGRES_PASSWORD=changeme`,
      `POSTGRES_DB=windmill`) with its data directory on tmpfs, `mem_limit: 1g`;
    - one `windmill` container, `MODE=standalone`, `mem_limit: 2g`.
    - The 1 GiB Postgres limit is a profile choice: a 512 MiB cap was hit in
      run 1 (cgroup `memory.peak` = 536,870,912), because tmpfs data and shared
      buffers count against the container's memory. It is not a process RSS
      figure and not a universal minimum.
    - tmpfs makes only the **database** ephemeral. Image layers (3.94 + 0.29 GB
      on arm64), container writable layers (about 3 MB) and Docker logs still
      use disk.
  - **Observed on the v1.767.0 CE oracle (arm64):**
    - `docker compose up -d` returned in 3 s; `/api/version` answered at 5 s;
      `/api/health/status?force=true` reported
      `{"status":"healthy","database_healthy":true,"workers_alive":1,…}` at
      6 s, from a cold database with migrations;
    - Windmill: cgroup `memory.peak` 894,414,848 B (page cache included); at the
      end `anon` 230 MB and the `windmill` process RSS 288 MB; highest
      2-second `docker stats` sample 349 MiB;
    - Postgres: cgroup `memory.peak` 700,366,848 B, with 73.7 MB of data on
      tmpfs; highest sample 581 MiB;
    - writable layers: Windmill 3.3 MB, Postgres 183 kB;
    - one standalone worker sufficed for every probe.
  - **Recorded in CI (amd64, `ubuntu-latest`, E2's first session, oracle run
    40649910412):** healthy 6.4 s after `compose up`; cgroup memory peaks
    328,458,240 B (Windmill) and 309,182,464 B (Postgres). That session ran
    only the smoke fixture (two runs, each twice), so its peaks are not
    comparable with run 1's 45 runs; the figures are the harness session
    report's, not a sizing rule.
- **Evidence:** `backend/src/main.rs:153` (`DEFAULT_NUM_WORKERS = 1`),
  `:569-590` (env help: `MODE`, `NO_AUTH`, `SUPERADMIN_SECRET`), `:759-789`
  (worker clamp; server mode for `server` and `standalone`);
  `backend/windmill-worker/src/worker.rs:365-377,440-520` (nsjail and unshare
  defaults, self-test fallback); `backend/windmill-api/src/health.rs:142-155`;
  `docker-compose.yml:11-30,52-72`; `integration_tests/docker-compose.yml:4-75`;
  `README.md:223,254`; observed on the v1.767.0 CE oracle.
- **Conformance:** [OR-4/a](conformance.md#or-4)
- **Revision 2026-10-28 (oracle, milestone E2):** the note said amd64 boot
  time and memory were pending the first CI recording. E2 recorded them
  (above); [Q-OR-2](#q-or-2) is answered.
- **Revision 2026-10-29 (source re-check, chain C3n):** the one worker of
  this topology is in the `default` worker group with no `WORKER_TAGS`, and
  that group's stored tags leave out `nativets`, so it never pulls a native
  job ([WR-40](languages.md#wr-40)). A `nativets` or `bunnative` step stays
  queued here, as WR-50/c's job did in batch E3e. Recording a native case
  needs a worker that serves the tag, for example `WORKER_TAGS=nativets` on
  the standalone container ([Q-WR-21](languages.md#q-wr-21)).

<a id="or-5"></a>
### OR-5 · Consequences of a single worker

**Scope:** core (harness rule); later (multi-worker profile) · **Confidence:** high (source-traced; two-worker case oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - one worker runs one job at a time;
    - flow orchestration returns after scheduling children, so it does not
      hold the worker while steps run; ordinary flows, subflows, loops and
      branches finish on one worker;
    - a step that starts another job and blocks waiting for it (for example
      `wmill` `runScript…`/`runFlow…` followed by a wait, or `waitJob`)
      **deadlocks a one-worker instance** until the job timeout;
    - `parallel: true` loops and `branchall` still produce Windmill's results on
      one worker, but concurrency itself (`parallelism` limits, interleaving,
      completion order) cannot be observed.
    - Recorded: two outer runs submitted together on one worker are in
      flight at the same time, because neither root holds the worker
      between its steps. E4a's group of runs `r` and
      `t` (`conformance/flows/stub/scopes`, oracle run 40689406044) had
      overlapping root jobs by the oracle's own timestamps.
    - Recorded on two workers (E3e, `conformance/flows/oracle/workers2`,
      oracle run 40792040448): a step that runs another script through
      `run_wait_result` and waits for its answer completes, with the child's
      result as its own (OR-5/b); and parallel bodies run concurrently, so a
      slow index-0 body completes after index 1 ([OR-11](#or-11),
      [OR-26](#or-26)).
  - **Harness:**
    - a fixture that needs blocking dispatch declares `oracle: {workers: 2}`;
      the harness then adds a second container with `MODE=worker` and the same
      `DATABASE_URL`. Two workers suffice for a one-level blocking child, not
      for arbitrary recursive blocking dispatch, so each fixture states the
      capacity it needs;
    - fixtures that assert anything about concurrency are tagged
      `oracle: concurrency` and run with N worker containers;
    - the harness refuses a run it cannot honour instead of hanging.
- **Evidence:** worker loop in `backend/windmill-worker/src/worker.rs` (one job
  at a time per worker); `backend/src/main.rs:759-783` (clamp);
  `backend/windmill-worker/src/worker_flow.rs:2777` (`handle_flow`),
  `:4390-4428,4598-4698` (dispatch returns after scheduling children);
  `integration_tests/docker-compose.yml:47-75` (single worker).
- **Conformance:** [OR-5/a, OR-5/b](conformance.md#or-5)
- **Note:** run 1 used only non-blocking fixtures. Parallel completion order
  needs the multi-worker profile ([Q-OR-7](#q-or-7), [Q-OR-15](#q-or-15)).
  So does any case that must tell a release of `parallelism` slots from the
  single worker's own serialisation ([CF-7](control-flow.md#cf-7)).
- **Revision 2026-10-28 (oracle, milestone E4a):** added the recorded
  overlap of two outer runs on one worker, which the harness's concurrent
  run groups rely on.
- **Revision 2026-10-29 (oracle, batch E3e):** the two-worker profile was
  source-derived only. The oracle workflow's second session now runs it:
  the one-level blocking child of OR-5/b completes, and parallel completion
  order departs from index order ([Q-OR-7](#q-or-7), [Q-OR-15](#q-or-15),
  [Q-OR-18](#q-or-18), all answered).

<a id="or-6"></a>
### OR-6 · Bootstrap: identity, token, workspace

**Scope:** core · **Confidence:** high (oracle-observed); medium for `NO_AUTH` effects on jobs · **Status:** confirmed

- **Rule:**
  - **Boot order (harness):**
    1. wait for `GET /api/version` → 200;
    2. wait for `GET /api/health/status?force=true` → `status: "healthy"` with
       `workers_alive >= 1`. `degraded` means the database is fine but no
       worker is up yet. The endpoint caches for 5 s, hence `force=true`.
  - **Windmill (login):**
    - the first migration seeds the superadmin `admin@windmill.dev` with
      password `changeme` (Windmill's public default seed);
    - `POST /api/auth/login` with
      `{"email":"admin@windmill.dev","password":"changeme"}` returns the
      session token as a **plain-text** body; send it as
      `Authorization: Bearer <token>`;
    - optionally, `POST /api/users/tokens/create` with `{"label":"wl-oracle"}`
      returns 201 and a plain-text long-lived token. Submitting with such a
      token changes `WM_USERNAME` ([OR-29](#or-29)).
  - **Windmill (workspace):** `POST /api/workspaces/create` with
    `{"id":"<ws>","name":"<ws>"}`; the name has a 50-character database limit.
    `DELETE /api/workspaces/delete/{ws}` removes it (superadmin): the row is
    deleted outright, not archived. `admins` (and `starter`) cannot be
    deleted.
  - **Windmill (CE workspace cap):** a build without the `enterprise`
    feature, which includes the CE image, refuses to create a workspace once
    two workspaces other than `admins` exist and are not archived. A fresh
    database holds only `admins` (the seeded `starter` and `demo` are removed
    by a later migration when empty), so two creates succeed and the third
    fails with HTTP 400, `Content-Type: text/plain`, body
    `Bad request: You have reached the maximum number of workspaces (2 outside of default workspace 'admins') without an enterprise license. Archive/delete another workspace to create a new one`.
    - The count ignores archived workspaces, but CE caps those separately:
      archiving a second one fails with
      `Bad request: You have reached the maximum number of archived workspaces (1) without an enterprise license. Permanently delete or unarchive the existing archived workspace first`.
      Unarchiving and creating a fork apply the same two-workspace check.
    - Recorded: the first oracle sessions of E3a, E3c, E3d and E4a kept
      one workspace per fixture alive, and the third fixture's create
      failed with this message in each.
  - **Harness:**
    - a fresh workspace per fixture, named `wl-<6 hex>-<n>`, isolates paths,
      variables and state;
    - because of the cap, it is **deleted** (`DELETE /api/workspaces/delete/{ws}`,
      never archived) after the fixture's runs, and also after a failed
      deployment, under a context of its own so that a canceled session
      still cleans up; a workspace that cannot be deleted ends the session.
      At most one fixture workspace exists at a time.
  - **`NO_AUTH=true` (optional, default off):**
    - **Windmill:** accepts `1` or `true`, ignored when `CLOUD_HOSTED`. Every
      request then bypasses bearer validation and resolves as superadmin
      `admin@windmill.dev`; `/auth/login` returns the literal `no_auth`.
    - **Harness:** always call login and use whatever comes back as the bearer
      token, so the same code works in both modes. Normal authentication is
      the core path: successful client calls under `NO_AUTH` would not prove
      that ordinary job-token scopes work ([Q-OR-4](#q-or-4)).
  - **`SUPERADMIN_SECRET`:** not recommended. It authenticates as the synthetic
    `superadmin_secret@windmill.dev`, who is not a workspace member, so
    `WM_USERNAME`, `permissioned_as` and the job token can differ from a normal
    run.
  - **Observed:** the normal-auth bootstrap succeeded on the v1.767.0 CE oracle.
- **Evidence:** `backend/migrations/20220123221903_first.up.sql:257-258` (seed
  user); `backend/windmill-api-users/src/users.rs:136,162` (routes),
  `:1982-1993` (login, `no_auth` short-circuit), `:2231-2245` (token
  creation); `backend/windmill-api/src/lib.rs:881,958-959`;
  `backend/windmill-api/src/health.rs:142-155`;
  `backend/windmill-common/src/worker.rs:375-376`;
  `backend/windmill-api-auth/src/auth.rs:490-505,693-733`; openapi
  `/health/status` (44), `/workspaces/create` (1117),
  `/workspaces/delete/{workspace}` (3219); `cli/test/containerized_backend.ts:1081-1097`;
  `backend/windmill-api-workspaces/src/workspaces.rs:4844-4870` (the CE
  workspace and archive caps), `:4883-4884` (create applies the cap only
  without `enterprise`), `:6653-6654` (forks), `:7350-7351` (archive),
  `:7470` (unarchive), `:216-224` (routes);
  `backend/windmill-api-workspaces/src/workspaces_extra.rs:859-872`
  (`admins` and `starter` refused), `:1132` (hard delete);
  `backend/windmill-common/src/error.rs:55-56,255-261` (`Bad request:`
  prefix, status 400, plain-text body);
  `backend/migrations/20221207103910_admins_workspace.up.sql:1-2`,
  `backend/migrations/20230524065920_remove_demo_starter.up.sql`; recorded
  by the first oracle sessions of E3a, E3c, E3d and E4a.
- **Conformance:** [OR-6/a](conformance.md#or-6)
- **Revision 2026-10-28 (oracle, batches E3a, E3c, E3d and E4a):** the
  finding said a fresh workspace per fixture run isolates each run, and
  named no limit. CE allows two workspaces besides `admins`, so the rule now
  states the cap, its exact refusal and the archive cap, and the harness
  deletes each fixture's workspace after its runs.

<a id="or-7"></a>
### OR-7 · Environment knobs (env only, image unmodified)

**Scope:** later · **Confidence:** high (names and defaults, source-traced; the listen interval's lack of effect oracle-observed; the refused result's error, source-traced) · **Status:** confirmed; drafted (the refused result's error, added 2026-10-29)

- **Rule:**
  - **Windmill: scheduling and debug knobs, no intended semantic effect:**
    - `LISTEN_NEW_EVENTS_INTERVAL_SEC`: default 10; Windmill's integration tests
      and the harness set 1;
    - `SLEEP_QUEUE`: the worker's poll base, derived from `NUM_WORKERS` when
      unset; 1000 ms in agent mode;
    - `WAIT_RESULT_FAST_POLL_INTERVAL_MS` (50),
      `WAIT_RESULT_FAST_POLL_DURATION_SECS` (2),
      `WAIT_RESULT_SLOW_POLL_INTERVAL_MS` (200): only the `run_wait_result`
      endpoints;
    - `KEEP_JOB_DIR=true`: keeps `/tmp/windmill/<worker>/<job_id>/` for
      debugging the generated wrapper;
    - `JSON_FMT=true`: JSON server logs.
  - **Windmill: execution-observable settings:**
    - `MAX_RESULT_SIZE_MB` (default 500) decides which job results are
      accepted: integer MiB, strict greater-than, checked only for results
      above 2 MiB. The size is the byte length of the result's JSON text,
      divided by 1024 twice with the remainder dropped, so a 4.9 MiB result
      counts as 4. A refused result fails the job, without the completion
      insert's retries, with
      `{"error": {"message": "Result of job <job id> is too large: <n>MB > MAX_RESULT_SIZE_MB=<m>MB.\nUse external storages such as the Windmill Object Storage to store large results: https://www.windmill.dev/docs/core_concepts/object_storage_in_windmill", "name": "ResultTooLarge"}}`
      (no location suffix, no `step_id`), and the job's log gains
      `Unexpected error during job execution:\nResultTooLarge: ` followed by
      the same message. In a flow the step then fails like any other step
      ([FX-12](execution.md#fx-12)). A result above 2 MiB but within the
      limit is stored, and the log gains
      `Warning: Result of job <job id> is large: <n>MB.\nRecommended max size is 2MB.\nPrefer using external storages such as the Windmill Object Storage to store large results: https://www.windmill.dev/docs/core_concepts/object_storage_in_windmill`.
      An ordinary error result is never measured: the error the worker
      wraps into `{"error": …}` counts as size 0. A **pre-shaped manual
      failure** is the exception: a result carrying a string `wm_failure`
      ([WR-17](languages.md#wr-17), [FX-13](execution.md#fx-13)) is stored
      as its own JSON text with the `ManualFailure` error added, and that
      text is measured like a successful result. So an oversized result
      with `wm_failure` is refused too, and the job ends with the
      `ResultTooLarge` error above instead of the manual-failure object
      (OR-7/c). The 2 MB limits on output and
      results that the source also has apply to the multi-tenant cloud
      only;
    - `BASE_URL` sets `WM_BASE_URL` and resume URLs; keep it stable
      (`http://localhost:8000`), because jobs can observe it;
    - `WHITELIST_ENVS` decides which host environment variables jobs see
      ([OR-16](#or-16));
    - `TIMEOUT_WAIT_RESULT` (default 600 s) affects the wait endpoints only.
  - **Harness:** fixtures do not depend on the scheduling knobs. Every
    non-default value goes into the recording's provenance.
  - **Recorded:** the smoke fixture's observations, recorded at
    `LISTEN_NEW_EVENTS_INTERVAL_SEC=1` on one worker, are unchanged at `10`
    on two workers. The oracle workflow's second session checks it on every
    run, first in oracle run 40792040448 (OR-7/a).
- **Evidence:** `backend/src/main.rs:245-249`;
  `backend/windmill-worker/src/worker.rs:335-346` (`SLEEP_QUEUE`), `:574`
  (`KEEP_JOB_DIR`); `backend/windmill-api-jobs/src/execution.rs:223-246`,
  `:273`; `backend/windmill-queue/src/jobs.rs:922,1472-1500`
  (`MAX_RESULT_SIZE_MB`; an earlier `windmill-common/src/worker.rs:2215`
  pointer is withdrawn); `integration_tests/docker-compose.yml:37,64`;
  the oracle workflow's `workers2` session.
- **Conformance:** [OR-7/a–c](conformance.md#or-7)
- **Revision 2026-10-29 (SC3, source re-check, Q-OR-14):** added what a
  refused result produces. The completion path measures the result before
  inserting it and returns a `ResultTooLarge` error, which the insert's
  retry policy excludes (`backend/windmill-queue/src/jobs.rs:1001-1012`,
  `:1075-1077`, `:1472-1506`; sizes `:675-718`, an error result measuring
  0 and raw text its byte length); the worker then fails
  the job through its ordinary error path, which logs the error and stores
  `{"message", "name"}` (`backend/windmill-worker/src/result_processor.rs:720-739`,
  `:1880-1927`; `backend/windmill-common/src/worker.rs:2228-2233`;
  `backend/windmill-common/src/error.rs:43-44`, `:103`). The cloud-only
  limits are `backend/windmill-worker/src/common.rs:417-425` and
  `backend/windmill-worker/src/handle_child.rs:367-371`. OR-7/b's
  expectation now gives the shape.
- **Revision 2026-10-29 (SC3 validation round 1, finding 3):** the text
  above said an error result is never measured. Only the wrapped error is
  exempt (it reports size 0, `backend/windmill-queue/src/jobs.rs:675-697`).
  A pre-shaped manual failure keeps its raw JSON text: the worker sends it
  through `add_completed_job_pre_shaped_failure`
  (`backend/windmill-worker/src/result_processor.rs:1584-1597`), which
  forwards it to the ordinary completion (`backend/windmill-queue/src/jobs.rs:848-880`);
  that completion checks the size whatever the status (`:1075-1077`), and
  raw text reports its byte length (`:698-718`). The refusal's error then
  takes the worker's ordinary error path (`result_processor.rs:720-739`),
  as for an oversized success. New source-derived case OR-7/c.
- **Revision 2026-10-29 (oracle, batch E3e):** OR-7/a was pending
  [Q-OR-18](#q-or-18). It is now checked live by every oracle run and has
  held. `MAX_RESULT_SIZE_MB` (OR-7/b, [Q-OR-14](#q-or-14)) is still
  unrecorded.

## B. Loading fixtures and running them

<a id="or-8"></a>
### OR-8 · Pushing a synthetic folder: the pinned CLI is the reference loader

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Harness push path:** `windmill-cli@1.767.0` from npm (package
    `windmill-cli`, licence field "Apache 2.0", bin `wmill`, 3.7 MB unpacked),
    run on the harness host (`bunx windmill-cli@1.767.0 …` or
    `npx -y windmill-cli@1.767.0 …`) against the oracle, never inside the
    oracle image.
    - Command: `wmill --base-url http://localhost:8000 --token <t> --workspace <ws> sync push --yes`
      from the fixture's `windmill/` directory. `--base-url` requires
      `--token` and `--workspace` and ignores stored profiles.
    - Pass `--config-dir <tmp>` (or set `WMILL_CONFIG_DIR`) so that no user
      profile leaks in.
    - Why the CLI: it is an independent implementation of folder → API
      (`!inline` files, metadata, locks, folder meta), so a loader bug in
      windmill-lite is not mirrored in the oracle.
  - **Windmill CLI facts each fixture tree must respect:**
    - **`wmill.yaml`:** the CLI searches upward for one; if none is found it
      warns and uses defaults (`defaultTs` defaults to `bun`). A fixture inside
      the windmill-lite repository must ship its own, so no parent file is
      picked up.
    - **`f/<folder>/folder.meta.yaml`:** without it, an admin push only warns,
      while a non-admin push fails. `wmill folder add-missing --yes` generates
      it (without `--yes` it blocks on an interactive prompt); its output is
      `summary: ""`, `display_name: <folder>`, `owners: []`, `extra_perms: {}`.
    - **Generated metadata (observed with CLI 1.767.0, dependency-free Bun):**
      - `double.script.yaml` with `lock: '!inline f/wl_smoke/double.script.lock'`
        and a schema inferred from the signature;
      - a 43-byte `double.script.lock`: the dependency document
        `{"dependencies": {}}`, then a `//bun.lock` line and `<empty>`;
      - for flows, each inline module is extracted to `<id>.inline_script.ts`
        plus `<id>.inline_script.lock` (or `<file>.lock` next to an existing
        `!inline` file, for example `b.lock`), and `flow.yaml` is rewritten to
        `!inline` references with `lock:` fields;
      - a root `wmill-lock.yaml` (`version: v2`, one hash per script and per
        flow).
      Other cases differ: an empty fetched lock removes the lock file and
      stores `lock: ""` in the metadata. Never require a file the generator
      does not write.
    - **`generate-metadata --dry-run`** returns normally on stale items, so it
      is not a drift check.
  - **Harness metadata rule:** commit exactly what `generate-metadata --yes`
    writes, generated once against the oracle. The drift gate re-runs
    `generate-metadata --yes` against the oracle and fails on any file change
    in the checkout (in CI, `git diff --exit-code` on the fixture tree).
  - **Harness secondary paths:**
    - direct API deployment (`POST /api/w/{ws}/folders/create`,
      `/scripts/create`, `/flows/create`) for a fixture whose defining input
      the CLI path cannot deliver as written, marked `oracle: {deploy: api}`
      (milestone E3d). Two reasons were met: `generate-metadata` writes the
      container defaults into `flow.yaml` ([CF-6](control-flow.md#cf-6)), so
      an absent `skip_failures` or `parallel` cannot reach the server through
      the CLI; and a by-path step pinned to a script hash needs a hash the
      server assigns only at deployment. Such a tree is created as written,
      passes the same deployment gate ([OR-9](#or-9)), and is outside the
      metadata drift gate; its recording keeps the submitted flow value, the
      value `flows/get` answers after the dependency job, and what the CLI
      would have generated;
    - preview runs (`POST /api/w/{ws}/jobs/run/preview_flow` with a
      `FlowPreview`: inline flow value plus args) need no push but are allowed
      only for rawscript-only fixtures and never as the only oracle for
      path-dependent behaviour. Preview jobs have a different job kind, the
      path-derived `WM_*` values are unverified, and the deployed sequential
      simple for-loop fast path is disabled for `FlowPreview`
      ([Q-OR-3](#q-or-3)).
  - `sync push` is destructive ([OR-31](#or-31)).
  - **Observed on the v1.767.0 CE oracle:** `folder add-missing --yes`, then
    `generate-metadata --yes`, then `sync push --yes` applied 125 changes in
    2 s. The CLI printed a harmless "outdated" notice.
  - **Recorded (every CI session since E2):**
    - `wmill --version` prints `CLI version: 1.767.0` and then, on later
      lines, `CLI is outdated. Latest version <v> is available. Run \`wmill upgrade\` to update.`
      and, when no workspace is selected,
      `Cannot fetch backend version: no active workspace selected`; other
      commands print the outdated notice too. Both notices are harmless: the
      harness takes the version from the first line and requires exactly
      `1.767.0`, and the notice names whatever version npm holds that day,
      so it is provenance, never compared;
    - the deployed flows have exactly the module ids of the pushed tree
      (the harness checks each flow against its tree after the gate),
      which is [OR-8/b](conformance.md#or-8).
- **Evidence:** npm registry metadata for `windmill-cli@1.767.0` (licence
  "Apache 2.0", bin `esm/main.js`); `cli/build-npm.ts:58-61`;
  `cli/src/main.ts:137-160` (global flags, `HEADERS`);
  `cli/src/core/conf.ts:201-219` (upward search, warning), `:359`
  (`defaultTs: "bun"`); `cli/src/commands/flow/flow_metadata.ts:163-175`
  (`!inline` paths relative to the flow folder);
  `cli/test/folder_missing_meta.test.ts:269`; `cli/src/utils/metadata.ts:795-808`
  (empty-lock branch); `cli/src/commands/generate-metadata/generate-metadata.ts:605-633,808-811`
  (`--dry-run`); `backend/windmill-worker/src/bun_executor.rs:76-81,356-374`
  (lock document, `//bun.lock` separator, `<empty>` marker);
  `backend/windmill-worker/src/worker_flow.rs:5297-5300`; openapi
  `/jobs/run/preview_flow` (13064), `/jobs/run_wait_result/preview_flow`
  (13103).
- **Conformance:** [OR-8/a, OR-8/b, OR-8/c](conformance.md#or-8)
- **Note:** the file names inside `windmill/` follow the sync-tree layout that
  [openflow.md](openflow.md) describes.
- **Revision 2026-10-28 (oracle, milestones E2 and E3d):** the finding
  limited direct API pushes to harness unit tests. E3d added API deployment
  for fixtures the CLI would rewrite, with its reasons above. Also added: the
  CLI's two harmless notices, and the recorded module-id check behind
  OR-8/b.

<a id="or-9"></a>
### OR-9 · Wait for deployment before running

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - after a push, locks are computed in asynchronous dependency jobs (one per
      script, one per flow with inline scripts); a run started before they
      finish can see a missing lock;
    - `GET /api/w/{ws}/scripts/deployment_status/h/{hash}` reports `lock`,
      `lock_error_logs` and `job_id`; `GET /api/w/{ws}/flows/deployment_status/p/{path}`
      reports `lock_error_logs` and `job_id`;
    - the flow's `job_id` is the stored `deployment_metadata.job_id`. It
      **persists** after the lock job succeeds, so it is not a pending flag;
    - observed: a flow after a successful lock job returned
      `{"lock_error_logs": null, "job_id": "<uuid>"}`; a script pushed with a
      committed lock returned
      `{"lock":"{\n  \"dependencies\": {}\n}\n//bun.lock\n<empty>","lock_error_logs":null,"job_id":null}`
      (no dependency job ran).
  - **Harness gate**, before the first run:
    1. for every flow and script, if `job_id` is present, poll
       `jobs_u/completed/get_result_maybe/{job_id}` until `completed` and
       require `success: true`;
    2. then require an empty or null `lock_error_logs`;
    3. for scripts, also require a non-null `lock`.
    - `GET /api/w/{ws}/jobs/queue/count` reporting `database_length: 0` is
      additional evidence, not the gate. Never wait for `job_id: null` on a
      flow: it can wait forever.
    - A non-empty `lock_error_logs` is a fixture error, not a semantic
      difference.
  - **Observed:** all 25 flow lock jobs had already succeeded when `sync push`
    returned; the gate took 0.07 s.
- **Evidence:** openapi `/scripts/deployment_status/h/{hash}` (9581),
  `/flows/deployment_status/p/{path}` (10741), `/jobs/queue/count` (13209:
  `database_length`, `suspended`); `backend/windmill-api-flows/src/flows.rs:757-766,1367-1377`
  (dependency job id stored), `:1442-1471` (status endpoint);
  `backend/windmill-worker/src/worker_lockfiles.rs:733-779` (successful lock
  updates the flow without clearing that id); observed on the v1.767.0 CE
  oracle.
- **Conformance:** [OR-9/a](conformance.md#or-9)

<a id="or-10"></a>
### OR-10 · Running a flow: asynchronous submit plus poll, never `run_wait_result`

**Scope:** core (harness); later (HTTP composite semantics for an HTTP host adapter) · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill (submit):** `POST /api/w/{ws}/jobs/run/f/{path}` with
    `Content-Type: application/json` and the flow args object as the body
    returns **201 with a text/plain UUID**. Scripts use
    `POST /api/w/{ws}/jobs/run/p/{path}`. `?job_id=<uuid>` pre-assigns the root
    id; the create is rejected (400) only while a `v2_job` row with that id
    exists, not whenever the id was ever used. Otherwise ids are ULID-based and
    sort by time.
  - **Windmill (poll):** `GET /api/w/{ws}/jobs_u/completed/get_result_maybe/{id}`
    returns `{"completed":false,…}` while the job is queued or running and
    `{"started":true,"success":<bool>,"completed":true,"result":<json>}` when it
    is done; `?get_started=true` also reports `started`. The poll's `success`
    is a completion signal, not the status model ([OR-V2](#or-v2)).
  - **Windmill (`run_wait_result/*`):**
    - it rewrites results into HTTP responses: success → 200 and failure →
      **422**, both with the result JSON as the body. A result object with any
      of `windmill_status_code`, `windmill_content_type`, `windmill_headers`
      or `windmill_content_transfer_encoding` (aliases `wm_status_code`,
      `wm_content_type`, `wm_headers`, `wm_content_transfer_encoding`) becomes
      a custom HTTP response: its `result` field is the body, with the given
      status and headers (base64 transfer encoding supported). The full
      rules, an array result's positional decoding and the body's text are
      [FX-36](execution.md#fx-36)'s;
    - it **cancels the job**: if the wait times out (`TIMEOUT_WAIT_RESULT`,
      default 600 s, message `timeout after {n}s`) or the client disconnects,
      a drop guard cancels the job with reason `"http connection broke"`.
  - **Harness:**
    - submit asynchronously and poll by default; never use
      `run_wait_result/*` as the way to obtain a result, because a fixture
      returning the composite keys would be misread and a disconnect would
      cancel the run;
    - a run whose HTTP response is itself under test is submitted through
      `run_wait_result` only when its fixture says so (`submit:
      run_wait_result`, design record 0011). The harness keeps the response
      (status, content type as sent, and the body as text, or as base64 when
      it is not UTF-8), does not follow a redirect, and still polls and walks
      the job, so the observation holds both;
    - cancel tests use `POST /api/w/{ws}/jobs_u/queue/cancel/{id}` with body
      `{"reason":"…"}`;
    - per-run timeouts stay at 60 s or more (a NUL failure took about 30 s,
      [OR-27](#or-27)).
  - **Observed:** `run_wait_result/f/f/wl_probe/composite` returned HTTP
    **201**, `content-type: application/json`, body `{"a": 1}`; the
    asynchronous poll result was `{"result":{"a":1},"windmill_status_code":201}`.
    Recorded again by E3e (`conformance/flows/oracle/probes` runs
    `composite` and `composite_wait`, oracle run 40794872025), with the
    same values.
  - **Design note (not Windmill behaviour):** windmill-lite's flow result must
    equal the stored (asynchronous) form. The HTTP rewriting is a host-adapter
    concern; an HTTP adapter may choose to mimic it.
- **Evidence:** openapi `/jobs/run/f/{path}` (12496), `NewJobId`
  (23102-23110), `/jobs_u/queue/cancel/{id}` (14509);
  `backend/windmill-api/src/jobs.rs:4818-4850` (`run_flow_by_path`),
  `:9198-9270` (`CompletedJobResult`); `backend/windmill-api-jobs/src/execution.rs:178-220`
  (drop guard), `:263-360` (poll loop, timeout), `:362-470`
  (`result_to_response`); `backend/windmill-queue/src/jobs.rs:6244-6258`
  (extant-row check); observed on the v1.767.0 CE oracle; recorded by
  `conformance/flows/oracle/probes` and `conformance/flows/execution/fx-36-http`.
- **Conformance:** [OR-10/a, OR-10/b, OR-10/c](conformance.md#or-10)
- **Revision 2026-10-29 (oracle, batch E3e):** the harness rule said never
  to use `run_wait_result`. Batch E3e records run-and-wait for the runs that
  test the HTTP response (FX-36's HTTP part, FX-37/a's early answer and
  OR-10/b), as design record 0011 now allows; every other run is still
  submitted asynchronously, and run-and-wait runs are polled and walked as
  well. The Windmill rules are unchanged.

## C. Capturing a run

<a id="or-11"></a>
### OR-11 · Capturing per-step results: walk `flow_status`

**Scope:** core · **Confidence:** high (shapes, source and oracle); identity encoding disputed · **Status:** DISPUTED

- **Rule (as drafted):**
  - **Windmill (shapes):**
    - `GET /api/w/{ws}/jobs_u/get/{id}?no_logs=true&no_code=true` returns a
      `Job` tagged `"type":"CompletedJob"` with `id`, `parent_job`,
      `created_at`, `started_at`, `completed_at`, `duration_ms`, `success`,
      `canceled`, `canceled_reason`, `is_skipped`, `job_kind`, `script_path`,
      `args`, `result`, `flow_status`, `raw_flow`, `language`, `is_flow_step`,
      `tag`, `priority`, `self_wait_time_ms`, `aggregate_wait_time_ms`,
      `is_retry`, `worker`, `email`, `permissioned_as` and more. Its `args` and
      `result` can be truncated ([OR-V1](#or-v1)).
    - `flow_status` top level: `step`, `modules[]`, `failure_module` (with
      `parent_module`), `preprocessor_module`, `retry: {fail_count, failed_jobs}`
      (omitted when empty), `user_states`, `cleanup_module`, `restarted_from`.
    - Each `modules[]` entry is tagged by `type`: `WaitingForPriorSteps`,
      `WaitingForEvents`, `WaitingForExecutor`, `InProgress`, `Success` or
      `Failure`. It carries `id` and, except for `WaitingForPriorSteps` (no job
      field), `job`; optionally `flow_jobs`, `flow_jobs_success`,
      `flow_jobs_duration {started_at[], duration_ms[]}`,
      `iterator {index, itered?, itered_len?}`,
      `branch_chosen {type: "branch"|"default", branch?}` and
      `branchall {branch, len}`. `Success` also carries `approvers[]`,
      `failed_retries[]` and `skipped`; `Failure` carries `failed_retries[]`.
      Only `InProgress` has `iterator`, `branchall`, `parallel` and
      `while_loop`: a completed (`Success` or `Failure`) loop or `branchall`
      entry keeps `flow_jobs`, `flow_jobs_success` and `flow_jobs_duration`
      and nothing that says which container it was, so the walker tells a
      `branchall` from a loop by the flow definition (recorded by E3c and
      E3d).
    - **Module `job` is not a semantic field.** While a parallel loop or
      `branchall` is `InProgress`, it is the parent flow's id; at completion it
      is the job whose completion finalised the module (`job_id_for_status`),
      which under parallel execution depends on completion order. In run 1 it
      equalled the last element of `flow_jobs` in all five loop/branchall
      captures on one worker: an observation, never an invariant. On two
      workers it was not: in a parallel loop and in parallel `branchall`s
      whose index-0 body sleeps, that body completed last and the completed
      module's `job` was `flow_jobs[0]` (E3e, `conformance/flows/oracle/workers2`
      runs `loop_slow`, `par_slow` and `par_slow_empty`, oracle run
      40792040448; read from that run's raw captures, which are its CI
      artifact and not committed).
    - Children and aggregates come from `flow_jobs` in index order; the
      aggregate result is built from `flow_jobs`. Recorded on two workers
      (the runs above): `flow_jobs` and the result stay in index order when
      completion order differs, and an empty middle branch keeps its index.
    - The nil UUID `00000000-0000-0000-0000-000000000000` is read with the
      module type ([OR-V2](#or-v2)).
    - Every flow and child flow carries a `failure_module` entry, which is
      `{"type":"WaitingForPriorSteps"}` when no failure module is defined or it
      did not run.
    - **An empty flow** (no modules) completes with `step: 1` and a
      `modules` list holding one entry that is not a module: `Success` with
      id `failure`, the nil `job` and `skipped: false`; its `failure_module`
      entry is still `WaitingForPriorSteps`. The walker records no step for
      it (recorded by E3c, `conformance/flows/execution/fx-36` run `empty`;
      the flow's result is [FX-3](execution.md#fx-3)'s). The child flow of
      an empty `branchall` branch holds the same entry (E3e's raw captures
      of `oracle/workers2` run `par_slow_empty`).
    - **Collapse rules (source).** A for-loop body collapses into leaf
      iterations **iff** the containing flow job is not `FlowPreview`; the loop
      is not `parallel`; the body is exactly one module of type `script`,
      `rawscript` or `flowscript`; that module has no `sleep`, `suspend`,
      `retry`, `stop_after_if`, `stop_after_all_iters_if`, `skip_if` or
      `cache_ttl` and no enabled `mock`; and the flow has no `failure_module`.
      A while-loop uses the same body test, without the parallel and preview
      conditions. Run 1's while body carried `stop_after_if`, so it did not
      collapse, consistent with this rule; E3d's while body without one did
      ([Q-OR-13](#q-or-13)).
    - Recorded topologies: [OR-28](#or-28) and the
      [Probe catalogue](#probe-catalogue).
    - `GET /api/w/{ws}/jobs/result_by_id/{flow_job_id}/{node_id}` resolves a node
      id through nested and restarted flows (an array for loop nodes) and can
      cross-check `results.<id>`. `GET /api/w/{ws}/jobs_u/get_flow_debug_info/{id}`
      answers only for root flows that are still queued or running.
  - **Harness walker:**
    1. **Root:** `jobs_u/get` for topology and status; `get_args` and
       `get_result_maybe` for full data, for every job ([OR-V1](#or-v1)).
    2. **For each `flow_status.modules[]` entry, in order** (and
       `failure_module`, `preprocessor_module` when the flow defines them):
       - no `job` field → `not_run`; do not fetch;
       - `flow_jobs` present → children are `flow_jobs[i]` at path `id[i]`;
         ignore the module's `job` for identity and aggregation;
       - otherwise a non-nil `job` → fetch it at path `id`;
       - nil `job` → the [OR-V2](#or-v2) rule;
       - `failed_retries[k]` → fetch at `id#k`.
    3. **Each fetched child:** if it has a `flow_status`, recurse (a child flow,
       `flownode` or `flowpreview` in the recorded shapes); otherwise it is a
       leaf. Decide per child, never by module type or job kind.
    4. **Generated virtual modules:** apply the [OR-V7](#or-v7) test against the
       fixture's sync tree.
    5. **Collapse:** the rules above predict the expected shape; the walker
       never relies on them, because it reads the actual status.
    - The `failure_module` entry is dropped from the projection unless the flow
      defines a failure module.
    - Per-step record: `status`, `args` (as stored on the step's job, after
      input transforms), `result`, `is_skipped`, `branch_chosen`; timings go to
      provenance ([OR-17](#or-17)).
    - Semantic paths: as in [Conventions](#conventions) (`failure` and
      `preprocessor` for the special slots).
- **Correction (validation):** the topology rules are accepted (actual
  leaf/flow inspection, ordered `flow_jobs`, nil failures, virtual modules),
  but the semantic-path contract used by this walker merges distinct
  identities. An ordinary module named `failure` and a failure handler named
  `recover` both get path `failure`, and the same holds for `preprocessor`;
  Windmill keeps ordinary modules and special slots separately. The percent
  escaping is not injective either ([OR-V8](#or-v8)). Do not implement these
  path strings as unique identities until a shared collision-free encoding is
  chosen.
- **Recorded shapes (2026-10-28, batches E3a to E3d and milestone E4a):** the
  walker steps above held for every recorded topology, read with the typed
  site paths of design record 0011 (`m:<id>`, `i:<n>`, `b:<n>` or
  `b:default`, `s:failure`, `s:preprocessor`, `r:<k>`), which leave the
  identity dispute below as it is:
  - for-loops sequential and collapsed (a collapsed iteration is the body
    module's own job, recorded at `i:<n>/m:<body>`), sequential with child
    flows, parallel, and nested two deep; while-loops; `branchone` with the
    chosen body under `b:<n>` or `b:default`; sequential and parallel
    `branchall` with empty branches; subflows by path, whose modules are
    below the module, and cached subflows, whose inner modules are
    `not_run` on a cache hit; retried modules (`r:<k>`); skipped and mocked
    modules; early stops inside bodies; cancellation (the canceled step's
    error, the failure module `not_run`);
  - the failure module at the top level and copied into loop and branch
    bodies: a body's copy runs inside the body's child flow, so that child
    flow's own `failure_module` entry has a job, and the walker records it
    at `<body>/s:failure` ([FX-34](execution.md#fx-34)). E2's walker
    refused such an entry until a recording settled it; E3c's did;
  - a failure module that succeeds lists the flow's earlier failed attempts
    again in its own `failed_retries`, so one job is met at two sites; its
    placeholder names the site the walk reaches first, the failed module's
    attempt ([FX-27](execution.md#fx-27), [FX-29](execution.md#fx-29));
  - an empty flow's status entry with id `failure` (above) is not a module,
    and a completed `branchall` is recognised from the definition (above).
  - Not recorded: a child job still being canceled after its root has
    completed. The harness's walk refuses that state rather than invent a
    site; no recording has produced it.
- **Open:** [Q-OR-17](#q-or-17). The live confirmations of
  [Q-OR-7](#q-or-7), [Q-OR-8](#q-or-8), [Q-OR-13](#q-or-13) and
  [Q-OR-15](#q-or-15) are recorded.
- **Evidence:** `backend/windmill-types/src/flow_status.rs:19-45` (`FlowStatus`),
  `:73-90` (`Iterator`, `BranchAllStatus`), `:97-101` (`BranchChosen`),
  `:219-297` (module variants and fields), `:506-531` (ordinary modules and
  special slots kept apart); `openflow.openapi.yaml:1119-1260`; openapi
  `CompletedJob` (24674), `Job` with `type` discriminator (25110-25126),
  `/jobs_u/get/{id}` (13896); `backend/windmill-worker/src/worker_flow.rs:766-940,1037-1058`
  (`job_id_for_status`), `:1478-1489` (aggregate from `flow_jobs`), `:3996-4010`
  (empty inner flows), `:4053-4067,5470-5485` (nil-job failure),
  `:4598-4668` (iteration list; parent id while in progress), `:5247-5249,5294-5300,5729-5746`
  (collapse tests), `:6248-6251` (nil job = empty branch);
  `backend/windmill-types/src/flows.rs:662-665`;
  `backend/windmill-queue/src/jobs.rs:4403-4700` (`get_result_by_id`);
  `backend/windmill-api/src/jobs.rs:757-790` (debug info); observed on the
  v1.767.0 CE oracle; recorded by the fixtures under `conformance/flows/`
  (raw API captures of many of them are committed under
  `internal/oracle/testdata/captures/`, among them `fx-36-empty.json` for
  the empty flow).
- **Conformance:** [OR-11/a, OR-11/b](conformance.md#or-11)
- **Revision 2026-10-28 (oracle, batches E3a to E3d and milestone E4a):** the
  shapes were drafted from oracle run 1 alone. Added: completed entries carry
  no container field, the empty-flow entry, the recorded topologies, the
  repeated-attempt rule and the collapsed while body. The identity encoding
  is unchanged and still disputed.
- **Revision 2026-10-29 (oracle, batch E3e):** added the two-worker
  recordings ([Q-OR-7](#q-or-7), [Q-OR-15](#q-or-15)): under real
  parallelism the completed module's `job` is the child that completed
  last (`flow_jobs[0]` when index 0 is slow), confirming that `job` is no
  identity, while `flow_jobs`, the aggregate and an empty branch's index
  keep index order (`backend/windmill-worker/src/worker_flow.rs:1038-1060`,
  the completed module's `job` is the completing child's id). E3e also
  re-recorded the probes `loop_empty`, `loop_seq_simple`, `while_simple` and
  `retry` (`conformance/flows/oracle/probes`, oracle run 40794872025) with
  the shapes above. The identity encoding is still disputed.

<a id="or-12"></a>
### OR-12 · Failure capture: the stored error envelope

**Scope:** core · **Confidence:** high (source and oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - a job failing through the job-error path stores `result = {"error": <value>}`.
      A JSON error raised by the job is used as `<value>` unchanged; any other
      internal error becomes `{"message": <text>, "name": <error kind>}`;
    - **not every failed flow uses that path:** a failure handler that
      succeeds without recovering leaves the root failed and stores the
      handler's result directly as the root result. Observed (`fail_handler`):
      root `success:false`, `result:{"saw":"boom","handled":true}`; the handler
      job's kind is `preview`, its args are `{"error": <the failed step's error object>}`,
      and its status has `parent_module:"a"`;
    - **Bun errors:** the error object carries `name`, `message` and `stack`;
      all other own property names (non-enumerable included) go under `extra`;
      `step_id` is added when present. The root result equals the failing
      step's result. Observed plain `Error`:
      `{"error":{"name":"Error","message":"too big: 120","stack":"Error: too big: 120\n    at main (/tmp/windmill/cache/bun/<64-hex>:5:16)\n    at run (/tmp/windmill/<worker>/<job>/wrapper.mjs:30:21)\n    at /tmp/windmill/<worker>/<job>/wrapper.mjs:42:11\n    at asyncModuleEvaluation (native:2)","step_id":"b"}}`.
      Observed subclass with an own property:
      `{"error":{"name":"MyErr","message":"boom","stack":"…","extra":{"code":42},"step_id":"a"}}`;
    - **non-script errors:** a missing `$var:` reference gives an
      `ExecutionErr` with `step_id` (exact message in
      [conformance](conformance.md#or-12)); the storage failure of a NUL result
      gives an `InternalErr` with **no** `step_id` ([OR-27](#or-27)).
  - **Harness:** record the root status and full `result`, and each failing
    step's `result`; normalise only volatile fields ([OR-14](#or-14)); never
    rely on the HTTP 422 of `run_wait_result` ([OR-10](#or-10)).
- **Evidence:** `backend/windmill-queue/src/jobs.rs:883-917`
  (`add_completed_job_error`); `backend/windmill-common/src/worker.rs:2228-2233`
  (`error_to_value`); `backend/windmill-worker/src/worker_flow.rs:2002-2058`
  (handler result stored on a failed root);
  `backend/windmill-worker/src/bun_executor.rs:1950-1967` (Bun error object;
  an earlier pointer to `:1886-1900` is a different wrapper and is withdrawn);
  observed on the v1.767.0 CE oracle.
- **Conformance:** [OR-12/a, OR-12/b, OR-12/c, OR-12/d](conformance.md#or-12)
- **Note:** the full Bun error contract and the flow-level wrapping belong to
  [languages.md](languages.md) and [execution.md](execution.md).
- **Revision 2026-10-29 (oracle, batch E3e):** the probes `fail_handler` and
  `fail_mid` and the missing variable were recorded as stated
  (`conformance/flows/oracle/probes`, `conformance/flows/oracle/seeded`,
  oracle run 40794872025), the handler job of kind `preview` with
  `parent_module: "a"` included. No rule changed.

<a id="or-13"></a>
### OR-13 · The oracle stores JSON as Postgres `jsonb`, and that is observable

**Scope:** core · **Confidence:** high (storage facts) · **Status:** confirmed

- **Rule:**
  - **Windmill:** job `args` and `result` are `JSONB` columns, so every stored
    arg or result read back has gone through jsonb:
    - object keys come back in jsonb order (shorter keys first, then bytewise),
      not insertion order;
    - duplicate keys collapse to the last one;
    - numbers are stored as `numeric`;
    - Postgres rejects the string escape `\u0000` ([OR-27](#or-27)).
    Not every API value is stored JSON: job metadata, synthesised fields and
    truncation sentinels ([OR-V1](#or-v1)) are not.
  - **Harness:** compare objects key-order-insensitively and arrays in order by
    default (exceptions: `key_order: strict` and `unordered` paths,
    [OR-14](#or-14)).
  - **In-flow ordering** (what a transform or script sees) is specified by
    [OR-23](#or-23) and [OR-24](#or-24), which supersede the original
    whole-object probe of this finding ([OR-V4](#or-v4)).
- **Evidence:** `backend/migrations/20220123221903_first.up.sql:161-162`;
  `backend/migrations/20250201124431_v2_job.up.sql:31`;
  `backend/windmill-queue/src/jobs.rs:1095-1114,6419-6483`;
  `backend/windmill-worker/src/worker_flow.rs:3323-3345`; jsonb ordering,
  duplicate collapse and `\u0000` rejection are documented Postgres behaviour.
- **Conformance:** [OR-13/a](conformance.md#or-13)
- **Revision 2026-10-29 (oracle, batch E3e):** E3e recorded the `key_order`
  probe again (`conformance/flows/oracle/probes`, oracle run 40794872025).
  The jsonb order shows in the step's stored args (`get_args` of `a0` gives
  `{"fi_keys": …, "fi_whole": {"a": 2, "zz": 1, "mmm": 3}}`, jsonb's spaced
  text) and in the object `a0` receives (`["a","zz","mmm"]`). The harness
  does not fetch the root's own `get_args`, and `jobs_u/get` gave the root's
  args in two different top-level orders in the two repeats, because that
  endpoint does not return the stored text ([OR-V1](#or-v1)).

## D. Normalisation, fixture format and side effects

<a id="or-14"></a>
### OR-14 · Normalisation rules

**Scope:** core · **Confidence:** high (volatile fields); medium (completeness) · **Status:** DISPUTED

- **Rule (as drafted):** the harness turns raw oracle and windmill-lite
  captures into normalised observations before comparing them:
  1. **Job ids.** Build the id → semantic-path map from the walk only
     ([OR-11](#or-11)). Replace those ids anywhere, including inside strings
     (a stack path, a returned `WM_JOB_ID`, an `InternalErr` message), with
     `<job:PATH>`. All other UUIDs and user strings are preserved.
  2. **Paths.** `id`, `id[i]` (index from `flow_jobs` order), `id/child`,
     `id#k` (k-th failed attempt in `failed_retries` order), plus `failure` and
     `preprocessor`. A literal `[`, `]`, `/` or `#` inside a module id is
     escaped as `%5B`, `%5D`, `%2F`, `%23`. A user string that already looks
     like `<job:…>` is escaped to `<<job:…>` before replacement.
  3. **Envelope.** Drop only these envelope fields, never same-named keys
     inside args or results: `logs`, `raw_code`, `raw_flow`, `tag`,
     `priority`, `worker`, `mem_peak`, `email`, `created_by`,
     `visible_to_owner`, `labels`, timestamps and durations. Timings go to
     provenance ([OR-V5](#or-v5)).
  4. **Stacks.** Mask under the runtime-error provenance rule of
     [OR-V3](#or-v3).
  5. **Recognised internal error.** Apply the NUL `InternalErr` `<loc>` rule of
     [OR-27](#or-27), and nothing broader.
  6. **Generated modules.** Omit them from the projection ([OR-V7](#or-v7)).
  7. **Status.** Job level canceled > skipped > success/failure; module level
     `not_run`, `ran:false` success, `ran:false` failure; job and module
     `skipped` flags kept separately ([OR-V2](#or-v2)).
  8. **Comparison.** Canonical JSON with object keys sorted for comparison
     only (except `key_order: strict` paths), arrays in order, numbers kept
     exactly as their JSON text. Fixture `unordered` paths use multiset
     equality ([OR-23](#or-23)). Fixture `mask` paths are explicit, for
     example `mask: ["$.steps.a.result.base_url"]`, and cover
     environment-derived values: `WM_BASE_URL`, `WM_WORKSPACE`, user
     identities, `permissioned_as`, tokens.
  9. **Traversal.** Object keys are traversed in sorted order before any
     occurrence-numbered placeholder is assigned, so placeholders are
     deterministic.
  - **Windmill facts behind the stack rule:** a Bun stack contains the
    script's content-hash bundle path `/tmp/windmill/cache/bun/<64-hex>`,
    stable for identical code, and wrapper frames
    `/tmp/windmill/wk-<group>-<container hostname>-<5 chars>/<job>/wrapper.mjs:30:21`
    and `:42:11`. Worker names have the form `<prefix>-<group>-<suffix>`; job
    directories are `<WINDMILL_DIR>/<worker_name>/<job_id>` with
    `WINDMILL_DIR` defaulting to `/tmp/windmill`. None of this is part of any
    contract.
  - **Windmill facts behind location suffixes:** several server-side error
    kinds append the Rust source location that raised them, as the file's
    last path segment, line and column after ` @`: `Error: <text> @<file>.rs:<line>:<col>`
    for a wrapped `anyhow` error, `Internal: <text> @<file>.rs:<line>:<col>`,
    and likewise the SQL, I/O, JSON and similar kinds. Each wrapping layer
    that converts the error again adds its own suffix, so a message can end
    in two (` @worker_flow.rs:2451:11 @worker.rs:3822:13` on an expression
    error at a child flow's pickup). A separate form sits inside the text:
    a not-found lookup reads `Not found: <kind> not found at name <name> (<file>.rs:<line>)`,
    line only, in parentheses. Suffixes recorded so far:
    ` @worker_flow.rs:2451:11`, ` @worker.rs:3822:13`, ` @common.rs:278:20`,
    ` @jobs.rs:1133:22` ([OR-27](#or-27)) and `(jobs.rs:9074)` (inside
    [CF-19/c](conformance.md#cf-19)'s message).
  - **Harness, recordings:** location suffixes stay verbatim in recordings;
    only the NUL `InternalErr`'s is masked (item 5). The comparison of
    windmill-lite with the oracle strips a suffix only where the
    catalogue's expected message writes it as `<loc>` and windmill-lite's
    value is an error it generated (design record 0011, "Location
    suffixes"). In catalogue texts `<loc>` stands for one location suffix
    **including its leading space**: ` @<file>.rs:<line>:<col>`, or the
    parenthesised not-found form ` (<file>.rs:<line>)`. Nothing else
    separates it from the text before it, and two suffixes are written
    `<loc><loc>` ([FX-50](execution.md#fx-50)). The file, line
    and column are Windmill build details, never compared.
  - **Observed stability:** normalised observations were byte-equal for smoke
    `ok` ×3, smoke `fail` ×2, `effects` ×2 and `loop_seq_simple` ×2;
    `key_order` differed only through [OR-23](#or-23), a real
    non-determinism. Two equal runs are necessary evidence of stability but
    do not prove that normalisation keeps meaningful differences
    ([OR-V3](#or-v3)).
- **Correction (validation):** the narrow stack masking, preservation of
  handler user data and multiset equality are accepted, but job placeholders
  inherit the path collisions of [OR-V8](#or-v8): the special-slot paths
  `failure` and `preprocessor` collide with ordinary modules of those names,
  and the escaping does not escape `%`, so literal `a/b` and `a%2Fb` both
  become `a%2Fb`. The propagated-runtime-error case of [OR-V3](#or-v3) (b) is
  an exception to "a handler's own result keeps its user data", not permission
  to mask arbitrary handler objects.
- **Open:** [Q-OR-17](#q-or-17).
- **Evidence:** `backend/windmill-worker/src/worker.rs:1923-1924,1975` (job
  directory, `WINDMILL_DIR`); `backend/windmill-common/src/utils.rs:281-287`
  (worker name), `:337-350` (the parenthesised not-found location);
  `backend/windmill-common/src/error.rs:45-74` (error kinds with a location
  suffix), `:138-145` (the location is the file's last path segment),
  `:146-150` (an `anyhow` conversion records its caller); openapi
  `CompletedJob` field list;
  `backend/windmill-types/src/flow_status.rs:506-531`; observed on the
  v1.767.0 CE oracle; the suffixes as recorded in `conformance/exprs` and
  `conformance/flows` (for example `execution/fx-15` run `child_pickup`,
  `cf/cf-19` run `cf19-c`).
- **Conformance:** [OR-14/a, OR-14/b](conformance.md#or-14)
- **Revision 2026-10-28 (oracle, batches E3a to E3d):** added the location
  suffix forms the recordings hold and the `<loc>` notation the catalogue's
  error texts use for them; the normalisation rules themselves are
  unchanged and still disputed on the identity encoding.

<a id="or-15"></a>
### OR-15 · Fixture format (language-neutral, shared by oracle record and windmill-lite replay)

**Scope:** core · **Confidence:** high (implemented by the harness; the probe catalogue recorded through it, oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Harness:** every conformance fixture is a directory
    `conformance/<area>/<case>/`:

    | Entry | Content |
    |---|---|
    | `fixture.yaml` | id; spec refs (for example `[FX-12, CF-3]`); scope tag; languages; oracle needs `{workers: 1\|2, effects: bool, concurrency: bool}`; compare options `{mode: exact\|subset, key_order, stack, mask[], unordered[]}` |
    | `windmill/` | a complete `wmill sync` tree: `wmill.yaml`, `f/<folder>/folder.meta.yaml`, scripts with the metadata and locks the CLI generates (including `wmill-lock.yaml`, [OR-8](#or-8)), `*.flow/flow.yaml` plus `!inline` files |
    | `setup.yaml` | optional: variables, resources and resource types, applied **after** the push ([OR-31](#or-31)) |
    | `runs/<name>.yaml` | `target` (flow `f/…` or script path); `args`; optional `cancel_after_ms`; `expect` (a normalised observation) |
    | `recorded/<name>.json` | the last oracle observation plus provenance, written by `record` |

  - **Normalised observation:** `status` (`success`, `failure`, `canceled` or
    `skipped`); `result`; `steps` (map from semantic path to
    `{status, ran?, args?, result?, branch_chosen?, skipped?}`, where a module
    status may also be `not_run`); `effects` ([OR-16](#or-16)).
  - **Provenance** in `recorded/*.json`, kept apart from the observation
    ([OR-V5](#or-v5)): `oracle.version` (`CE v1.767.0`), `oracle.image` (with
    digest), `oracle.cli` (`windmill-cli@1.767.0`), harness version, date,
    workers, host architecture, raw timings, non-default environment
    ([OR-7](#or-7)).
  - **Verbs:** `record` runs the oracle and writes `recorded/`; `check` runs
    windmill-lite and compares against `expect` (or `recorded` when `expect`
    is absent); `diff` runs both live and compares them directly.
  - Exact comparison is the default core gate; subset assertions alone cannot
    establish an exact error contract.
  - Fixtures in the public repository contain only synthetic scripts and
    flows.
  - Walker unit tests "over recorded captures" ([OR-28](#or-28)) use the raw
    API captures committed since E2 (`internal/oracle/testdata/captures/`);
    run 1's captures were not retained.
- **Evidence:** design; builds on [OR-8](#or-8) (sync tree as input),
  [OR-11](#or-11)/[OR-12](#or-12) (observation contents) and [OR-14](#or-14)
  (normalisation).
- **Conformance:** [OR-15/a, OR-15/b](conformance.md#or-15)
- **Note:** the format as implemented has more options than the table, for
  example `submit: run_wait_result`, `delays`, `variables`, `oracle.setup`
  and the `compare` masks; [`conformance/README.md`](../../conformance/README.md)
  and design record 0011 describe them. The format was accepted as sound and
  needed evidence only because the probe catalogue that should populate it
  was not an exact executable reconstruction of run 1's fixtures
  ([OR-V9](#or-v9), [Q-OR-16](#q-or-16)).
- **Revision 2026-10-29 (oracle, batch E3e):** E3e reconstructed the probe
  catalogue in this format, with every transform binding written out
  (`conformance/flows/oracle/probes`, plus `oracle/nul`, `oracle/big-payload`
  and `oracle/seeded`; oracle runs 40794872025 and 40798097155). Each
  reconstructed probe reproduced its recorded result, and the recordings and
  raw captures retain the tree and its observations, which run 1 did not.
  The status moves from `needs-evidence` to `confirmed`.

<a id="or-16"></a>
### OR-16 · Observing side effects

**Scope:** core (sink); later (state and variable snapshots) · **Confidence:** high (whitelist and CE cleanup observation; nested grouping oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - only whitelisted host environment variables reach jobs: `WHITELIST_ENVS`
      is a comma-separated list;
    - `WM_ROOT_FLOW_JOB_ID` names the innermost root flow scope, while
      `WM_ROOT_JOB_ID` carries the outer root ([OR-V6](#or-v6));
    - **`delete_after_use` on CE:** on a flow step it has no effect. Observed:
      after the root completed, step `a` still had args `{"x":7}`, result
      `{"secret":7}` and its log line. The asynchronous flow worker's whole
      completion-cleanup block is `enterprise`-gated ([OR-21](#or-21)). A
      generic API helper that blanks args to `{}`, result to `{}` and logs to
      `##DELETED##` exists, but it does not establish that an asynchronously
      run CE flow step is blanked; ungated deletion is reached from
      synchronous API paths ([OR-21](#or-21)). Independently of the
      edition, a flow deployed through the CLI and run by path no longer
      carries the option when it runs: the deployment re-serialises the
      flow without it ([OR-21](#or-21)).
  - **Harness effect sink:**
    - an HTTP recorder runs on the harness host. The oracle container sets
      `WHITELIST_ENVS=WL_EFFECT_SINK` and
      `WL_EFFECT_SINK=http://host.docker.internal:<port>`; on Linux it also
      needs `extra_hosts: ["host.docker.internal:host-gateway"]`.
      windmill-lite's harness injects the same variable;
    - fixture scripts `await` each POST and send
      `{"root": WM_ROOT_JOB_ID, "job": WM_JOB_ID, "step": WM_FLOW_STEP_ID, …}`;
    - the harness groups effects by the outer run: it accepts an effect only if
      `root` equals the root job id it submitted (otherwise
      `{"error":"foreign_effect"}`), keeps `job` and `step` for ancestry, and
      asserts order only where the fixture guarantees it (with one worker,
      arrival order is execution order);
    - environment-semantics tests still record the actual env values; grouping
      is a harness policy.
  - **Harness platform effects:** after the run, read back
    `GET /api/w/{ws}/variables/get/{path}`, `GET /api/w/{ws}/resources/get/{path}`
    and state for the paths the fixture lists under `observe:`. The client
    surface is in [client-api.md](client-api.md).
  - **Harness logs:** `GET /api/w/{ws}/jobs_u/get_logs/{id}` is recorded but
    not compared by default.
  - **Harness tagging:** step-level `delete_after_use` and flow-level
    `delete_after_secs` are tagged `EE`; the CE observation (nothing blanked)
    is the default expectation.
  - **Observed:** two runs of `effects` each recorded
    `[{"step":"a"},{"step":"b"}]` under separate keys. Run 1 keyed effects by
    `WM_ROOT_FLOW_JOB_ID`, which equals the root for a top-level flow.
  - **Recorded (E4a, `conformance/flows/stub`, oracle run 40689406044):**
    every effect posted inside a subflow by path, a parallel iteration, a
    sequential or collapsed iteration and a nested loop carried
    `WM_ROOT_JOB_ID` equal to the submitted root, and two outer runs
    submitted together kept their effects apart. `WM_ROOT_FLOW_JOB_ID` was
    the subflow's job inside the subflow and each parallel iteration's own
    job inside that iteration, and the root inside sequential, collapsed
    and nested sequential iterations ([OR-V6](#or-v6), [Q-OR-10](#q-or-10),
    answered).
- **Evidence:** `backend/windmill-common/src/worker.rs:2200-2206`
  (`WHITELIST_ENVS`); `backend/windmill-api-jobs/src/execution.rs:524-545`
  (blanking helper); `backend/windmill-worker/src/worker_flow.rs:1819-1970`
  (gated completion cleanup), `:4316-4332,4418-4421` (outer root propagated);
  `backend/windmill-common/src/variables.rs:381-417`; openapi
  `/variables/create` (5929), `/resources/create` (7343); observed on the
  v1.767.0 CE oracle; recorded by `conformance/flows/stub/scopes`,
  `nested`, `collapsed` and `retry_loop`.
- **Conformance:** [OR-16/a, OR-16/b, OR-16/c](conformance.md#or-16)
- **Revision 2026-10-28 (oracle, milestone E4a):** the finding said the
  grouping by `WM_ROOT_JOB_ID` inside subflows and parallel loops was not
  shown. E4a's stub recordings show it; [Q-OR-10](#q-or-10) is answered.
  The `observe:` read-back of variables, resources and state is not yet
  implemented by the harness and has no recording.
- **Revision 2026-10-29 (oracle, batch E3e):** E3e recorded the probes
  `effects` (twice, each run's effects `a` then `b` under its own root) and
  `delete_after_use` (`conformance/flows/oracle/probes`, oracle run
  40794872025). The committed `delete_after_use` flow carries no
  `delete_after_use` key, because the pinned CLI's `generate-metadata`
  re-serialises the flow without it, and a deployment would remove it too
  ([OR-21](#or-21)); so the recording shows CE keeping a step's args and
  result, not the option's effect. The expectation is unchanged.

<a id="or-17"></a>
### OR-17 · Timing capture (sanity only; the oracle is not the latency baseline)

**Scope:** later · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - per job: `created_at`, `started_at`, `completed_at`, `duration_ms`,
      `self_wait_time_ms`, `aggregate_wait_time_ms`;
      `GET /api/w/{ws}/jobs_u/completed/get_timing/{id}` returns
      `{created_at, started_at, duration_ms}`;
    - per loop or `branchall`: `flow_jobs_duration.started_at[i]` and
      `duration_ms[i]`;
    - a module's `sleep` delays the **next** module: scheduling the next module
      reads the previous module's sleep. The next job is created at once and
      starts after the sleep. Observed with `sleep: {type: static, value: 2}`
      on `a`: `a.completed_at` 21:18:17.524285Z, `b.created_at`
      21:18:17.527027Z, `b.started_at` 21:18:19.546256Z, a gap of 2021.971 ms.
  - **Harness:**
    - store a `timings` record per run in provenance: end-to-end time (root
      `created_at` → completion observed), each step's `duration_ms`, and its
      queue wait (`started_at − created_at`);
    - never compare timings for equality; only bounds are allowed, for example
      `b.started_at − a.completed_at ≥ 2000 ms` for a sleep on `a` (never
      measured from `created_at`).
  - **Observed:** a 2–3-step flow took 250–310 ms of wall time from submit to
    completed poll.
  - **Design note (not Windmill behaviour):** Windmill's queue, per-step jobs
    and database round trips are what windmill-lite removes, so the oracle is
    not a latency baseline. Measure windmill-lite against its own previous runs
    or against the hand-written composition it replaces.
- **Evidence:** openapi `CompletedJob` fields, `/jobs_u/completed/get_timing/{id}`
  (14323); `backend/windmill-api/src/jobs.rs:9530-9585`;
  `backend/windmill-types/src/flow_status.rs` (`FlowJobsDuration`);
  `backend/windmill-worker/src/worker_flow.rs:3667-3716` (sleep read from the
  previous module); observed on the v1.767.0 CE oracle.
- **Conformance:** [OR-17/a](conformance.md#or-17)
- **Revision 2026-10-29 (oracle, batch E3e):** the `sleep` probe was
  recorded again (`conformance/flows/oracle/probes` run `sleep`, oracle run
  40794872025): `b` was created 2.7 ms after `a` completed and started
  2048.074 ms after it. The run checks the bound, never the value.

## E. Lifecycle, CI and the smoke proof

<a id="or-18"></a>
### OR-18 · Instance lifecycle and hygiene

**Scope:** core · **Confidence:** high (operational design; teardown oracle-observed) · **Status:** confirmed

- **Rule (harness):**
  - **One oracle per test session:** `docker compose -p wl-oracle up -d`, then
    bootstrap once ([OR-6](#or-6)); each fixture gets a fresh workspace and a
    sequential `record`; finish with `docker compose -p wl-oracle down -v`.
  - **No state outside the session:** Postgres on tmpfs, so no named volumes;
    no bind mounts except an optional read-only fixture mount (not needed,
    because the CLI pushes from the host).
  - **Disk:** the fail-closed preflight of [OR-30](#or-30).
  - **Ports:** bind only to `127.0.0.1`.
  - **Image:** never `pull_policy: always` (the official compose uses it); use
    the pinned digest and pull once.
  - **Network:** the oracle needs outbound access only when a fixture has npm
    dependencies. Synthetic fixtures should have none, which keeps `record`
    hermetic and fast.
  - **Cleanup:** containers, network, the temporary CLI config directory, the
    CLI package cache, the fixture copy, captures and harness scratch. Keep the
    synthetic fixture definitions and sanitised normalised captures durably
    ([Q-OR-16](#q-or-16)).
  - **Observed:** `docker compose down -v` removed both containers and the
    `wl-oracle_default` network; no named volumes were created and none
    remained.
- **Evidence:** [OR-1](#or-1), [OR-4](#or-4), [OR-6](#or-6);
  `docker-compose.yml:34` (`pull_policy: always`, deliberately not copied);
  observed on the v1.767.0 CE oracle.
- **Conformance:** [OR-18/a](conformance.md#or-18)

<a id="or-19"></a>
### OR-19 · CI shape

**Scope:** core · **Confidence:** high (design; the oracle workflow has run since E2) · **Status:** confirmed

- **Rule (harness):**
  - **Pull-request CI (no Docker):** `check` windmill-lite against the committed
    `expect` and `recorded` observations. It is fast and needs no Windmill.
  - **Oracle CI** (on fixture changes, nightly, and on a label):
    - a GitHub-hosted `ubuntu-latest` runner (amd64) runs the minimal compose
      with `windmill:1.767.0@<full digest>`; public CI use of the CE image is
      decided ([OR-3](#or-3));
    - the runner's free disk is measured by the [OR-30](#or-30) preflight,
      never assumed;
    - `record` for all fixtures;
    - it fails if any **normalised observation projection** differs from the
      committed one; recording dates, timings and other provenance never fail
      it ([OR-V5](#or-v5)); the diff is uploaded as an artefact.
  - **Image choice:** the same multi-arch main image locally (arm64) and in CI
    (amd64), so both run the same oracle build. The amd64-only slim image is an
    optional CI speed-up, to adopt only after one full `record` shows zero
    diffs against the main image.
  - **EE images** are not used in public CI: they need a licence key, which
    would be a secret.
  - **Recorded:** since E2 the oracle workflow runs on `ubuntu-latest`
    (amd64) for pull requests that change fixtures or the harness, on a
    label, nightly and by hand; `record --check --repeat 2` reports every
    committed recording `unchanged`, with the metadata drift gate clean,
    across the sessions of E2 to E4a.
- **Evidence:** [OR-1](#or-1), [OR-2](#or-2), [OR-3](#or-3);
  `.github/workflows/docker-image.yml:505-546` (slim is amd64-only); the
  repository's oracle workflow and its CI runs (for example oracle runs
  40649910412 and 40706584607).
- **Conformance:** [OR-19/a](conformance.md#or-19)
- **Revision 2026-10-28 (oracle, milestone E2):** the confidence was medium
  pending the first CI recording; the workflow now runs as described, and
  its disk and timing figures are in [OR-2](#or-2), [OR-4](#or-4) and
  [OR-30](#or-30).

<a id="or-20"></a>
### OR-20 · Smoke fixture: a two-step Bun flow with one input transform

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Harness:** the minimal end-to-end proof of the harness, a synthetic
    `f/wl_smoke` folder:
    - step `a` is the workspace script `f/wl_smoke/double` referenced by path,
      with `a.x = flow_input.x`;
    - step `b` is an inline Bun rawscript with the transform under test
      `b.n = results.a.doubled` and a static `b.tag = "hi"`; it throws
      `too big: <n>` when `n > 100`;
    - `windmill/wmill.yaml` holds `defaultTs: bun` and `includes: ["f/**"]`;
      `folder.meta.yaml` comes from `folder add-missing`, and the script
      metadata and locks from `generate-metadata` ([OR-8](#or-8));
    - runs: `ok` = `{"x":3}`, `fail` = `{"x":60}`.
    - The full fixture is in [conformance](conformance.md#or-20).
  - **Observed on the v1.767.0 CE oracle:**
    - `ok`: root `success`, result `"hi:6"`; `a` args `{"x":3}`, result
      `{"doubled":6,"seen":3}`; `b` args `{"n":6,"tag":"hi"}`, result `"hi:6"`;
      stable over 3 runs;
    - `fail`: root `failure` with the plain-`Error` object of [OR-12](#or-12)
      (`step_id:"b"`); `b` holds the same object; `a` result
      `{"doubled":120,"seen":60}`; stable over 2 runs;
    - 250–310 ms per run; push, deployment readiness, repeats, resource
      measurements and teardown were all recorded.
- **Evidence:** field names follow `openflow.openapi.yaml` (`input_transforms`,
  `type: javascript` with `expr`, `type: static` with `value`, `rawscript` with
  `language`/`content`); [OR-8](#or-8), [OR-10](#or-10), [OR-11](#or-11),
  [OR-12](#or-12); observed on the v1.767.0 CE oracle.
- **Conformance:** [OR-20/a, OR-20/b](conformance.md#or-20)

## F. Edition and argument facts

<a id="or-21"></a>
### OR-21 · Edition differences that matter to a CE oracle

**Scope:** EE · **Confidence:** medium (narrowed claim source-traced and CE-observed); high (the deployed value drops `delete_after_use`, source-traced) · **Status:** confirmed; drafted (the deployed value drops `delete_after_use`, and the routes that still carry the option, added 2026-10-29)

- **Rule:**
  - **Windmill:**
    - in CE, the **asynchronous flow worker** skips completion cleanup:
      `delete_after_use`, `delete_after_secs` and scheduled deletion. The whole
      block, including parent propagation and root cleanup, is
      `enterprise`-gated. Observed on CE: a step's `delete_after_use` had no
      effect ([OR-16](#or-16)), at the top level and on a step of a subflow
      by path;
    - **a deployed flow no longer carries `delete_after_use`.** The flow and
      module types read the field but never write it back. The API stores a
      pushed flow value as sent, but the deployment's dependency job parses
      the flow and writes the re-serialised value into `flow` and
      `flow_version`, so once it has completed (the harness waits for it,
      [OR-9](#or-9)) neither a module's nor the flow's `delete_after_use`
      reaches the worker, on any edition. `delete_after_secs` is written
      back and is unaffected. The pinned CLI's `generate-metadata` writes
      the same dependency job's answer into `flow.yaml`, so it removes the
      key from a fixture tree as well;
    - **which routes carry the option to a flow's worker at all** (source
      re-check of 2026-10-29):
      - a deployed flow: only a run that the worker picks up **before** the
        deployment's dependency job has written its value back reads the
        value as sent, key included (and that worker's in-process cache of
        the version may keep it for later runs). This is a race the harness
        never drives, since it waits for the dependency job;
      - a preview (`POST /jobs/run/preview_flow`) never carries it: the
        submitted value is decoded into the typed flow and stored on the job
        by serialising that, which drops the key again;
      - a `script` step by path takes the script's **own** `delete_after_use`
        as well (either one turns the option on for the step's job). That
        flag lives on the deployed script row, survives deployment, and can
        be set only through the scripts API, because the pinned CLI never
        sends the key. This is the one dependable way to make a flow step's
        job carry the option; on CE the flow cleanup that would act on it is
        `enterprise`-gated, so nothing is blanked, as for a module's own key;
    - this is **not** a claim that CE never deletes job metadata: the ungated
      helper `handle_delete_after_completion` is called from synchronous API
      paths: the run-and-wait routes of a script by path
      (`POST` and `GET /api/w/{w}/jobs/run_wait_result/p/<path>`) and by hash
      (`POST …/run_wait_result/h/<hash>`). They pass it the script row's
      `delete_after_use` **and** `delete_after_secs`, and the seconds win
      when set: `delete_after_secs: 0` deletes at once (whatever the
      boolean says), a positive value schedules the deletion instead, and a
      negative value disables it; only with `delete_after_secs` absent does
      `delete_after_use: true` mean "at once". Deleting at once returns the
      result as usual and then sets the job's stored args to `{}`, its
      result to `{}` and its logs to `##DELETED##`, on CE too. A scheduled
      deletion is only written to the deletion schedule: the monitor task
      that carries it out is `enterprise`-gated, so on CE such a job is never
      blanked. No flow route calls the helper;
    - flow-node debouncing is gated by the `private` feature, which the CE
      image contains;
    - some run endpoints (for example `run_wait_result_flow_by_version*`) call a
      licence check only under `enterprise`, so it does not affect CE;
    - no other CE/EE equivalence claim is made; the CE build also substitutes
      private code ([OR-3](#or-3)).
  - **Harness:** a fixture using `delete_after_use` (including inside a
    subflow) or `delete_after_secs` is tagged `EE` for the cleanup part. Its
    committed tree shows no `delete_after_use` key once `generate-metadata`
    has run.
    EE-only module features (for example concurrency limits and priorities, as
    tagged in [openflow.md](openflow.md) and [execution.md](execution.md)) are
    inert or rejected on CE; the oracle records what CE does and those fixtures
    are tagged `EE`.
  - **Design note (not Windmill behaviour):** windmill-lite matches CE by
    default.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:1819-1970`
  (`enterprise`-gated completion cleanup), `:1585-1606,4453-4460` (`private`
  debouncing); `backend/windmill-api-jobs/src/execution.rs:548-562`
  (`handle_delete_after_completion`, ungated); `backend/windmill-api/src/jobs.rs:5816-5818`
  (synchronous caller); `backend/windmill-api/src/jobs.rs`
  (`run_wait_result_flow_by_version*` licence check under `enterprise`);
  `backend/windmill-types/src/flows.rs:305-306` (`FlowValue`) and
  `:574-575` (`FlowModule`): `delete_after_use` is read but never
  serialised; `:121-122` (a new flow's value is kept as sent);
  `backend/windmill-api-flows/src/flows.rs:597-609` (stored as sent);
  `backend/windmill-worker/src/worker_lockfiles.rs:649-669` (the
  dependency job re-serialises the parsed flow), `:733-745` (written into
  `flow` and `flow_version`); `cli/src/commands/flow/flow_metadata.ts:307-347`
  (`generate-metadata` writes the answer of `flow_dependencies_async`,
  `:435-473`, back into `flow.yaml`); recorded by
  `conformance/flows/oracle/probes` runs `delete_after_use` and
  `subflow_dau` (oracle run 40794872025).
- **Conformance:** [OR-21/a–d](conformance.md#or-21)
- **Revision 2026-10-29 (oracle, batch E3e; source re-check):** OR-21/a was
  pending [Q-OR-18](#q-or-18). E3e recorded it: after a subflow by path
  ran, its step still had args `{"x":7}` and result `{"secret":7}`. The
  source re-check adds why that holds before the edition gate is even
  reached: the committed fixture carries no `delete_after_use` key, since
  `generate-metadata` removed it, and a deployment removes it from the
  stored flow in any case. The recordings therefore cannot show the option's
  effect on any edition; the CE expectation (nothing blanked) is unchanged.
- **Revision 2026-10-29 (SC3, source re-check, SC2's oracle note):** SC2
  noted that a deployment drops `delete_after_use`; this revision traces
  every route by which the option could still reach a worker, so the
  oracle harness knows what a recording can show. A deployed flow carries
  it only in the deploy race (`backend/windmill-common/src/lib.rs:1980-1995`:
  a run by path takes the newest version whatever its dependency job's
  state; `backend/windmill-common/src/cache.rs:555-573`: versions are cached
  by id). A preview never does (`backend/windmill-queue/src/jobs.rs:5604`,
  `:5665`, `:6319`, `:6476`: the typed value is re-serialised onto the job).
  A `script` step by path also reads the script's own flag
  (`backend/windmill-worker/src/worker_flow.rs:6051-6105`, combined at
  `:6136-6137`; `backend/windmill-common/src/jobs.rs:117-131`), which the
  scripts API stores (`backend/windmill-types/src/scripts.rs:546-547`,
  read but not written back; `backend/windmill-api-scripts/src/scripts.rs:1568`)
  and the pinned CLI never sends (`cli/src/commands/script/script.ts:472-501`).
  On CE the only observable effect is the synchronous script routes'
  deletion (`backend/windmill-api/src/jobs.rs:181-183`, `:189-190`, `:5817`,
  `:5961`, `:6086`; `backend/windmill-api-jobs/src/execution.rs:524-544`,
  `:549-562`). New cases OR-21/b (a step whose script carries the flag:
  nothing blanked on CE) and OR-21/c (the synchronous script route deletes,
  on CE), both source-derived; their recording is requested from batch
  E3f.
- **Revision 2026-10-29 (SC3 validation round 1, finding 5):** the
  synchronous routes' deletion was stated for every script with
  `delete_after_use: true`. They also pass the row's `delete_after_secs`
  (`backend/windmill-api/src/jobs.rs:5753`, `:5897`, `:5990-6008` read both
  from the script row), and an explicit seconds value takes precedence
  (`backend/windmill-common/src/jobs.rs:68-76`: zero is immediate, positive
  is scheduled, negative disables; only absent seconds with a true boolean
  is immediate), implemented by `backend/windmill-api-jobs/src/execution.rs:549-562`
  and `backend/windmill-common/src/jobs.rs:47-64` (a scheduled deletion is
  a row in `job_delete_schedule`). The task that processes that schedule
  is compiled only with `enterprise` (`backend/src/monitor.rs:1530-1531`,
  its call `:3379-3382`), so on CE a positive delay blanks nothing. The
  scripts API stores both fields (`backend/windmill-api-scripts/src/scripts.rs:1568-1569`).
  OR-21/c now names its setup (no `delete_after_secs`); new source-derived
  case OR-21/d covers the positive delay.

<a id="or-22"></a>
### OR-22 · Arguments as stored versus as received

**Scope:** core · **Confidence:** high (source-traced and oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - a step job's stored `args` is the argument object after input-transform
      evaluation;
    - a static input transform keeps a `$var:` / `$res:` reference string as
      written; the worker resolves it only at execution time and writes the
      resolved args to the job's local args file. Main-signature hydration (for
      example dates and bytes) likewise happens at execution time, so such
      values may still be in their stored form in `args`;
    - a script preprocessor can replace the args the script receives
      ([OR-24](#or-24));
    - observed: stored args `{"s":"$var:f/wl_probe/v"}`, result `"secret-ish"`.
  - **Harness:** a fixture that needs the *received* value has the step echo
    it back (a script returning its own argument); never rely on `args` for
    it.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2746-2750` (static
  transforms keep the reference); `backend/windmill-worker/src/common.rs:122-150,174-208,283-292`
  (worker-side interpolation into the args file);
  `backend/windmill-worker/src/bun_executor.rs:1978-1980` (Bun calls it),
  `:2440-2454` (preprocessor-driven updates); observed on the v1.767.0 CE
  oracle.
- **Conformance:** [OR-22/a](conformance.md#or-22)
- **Note:** resolution timing belongs to [expressions.md](expressions.md)
  (static `$res:`/`$var:`) and [languages.md](languages.md) (signature
  hydration).
- **Revision 2026-10-29 (oracle, batch E3e):** `args_vs_received` was
  recorded again as stated (`conformance/flows/oracle/probes`, oracle run
  40794872025).

## G. Findings from oracle run 1

<a id="or-23"></a>
### OR-23 · Top-level `flow_input` key order inside transforms is random per run

**Scope:** core · **Confidence:** high (source and oracle) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - **random:** `Object.keys(flow_input)` evaluated in an input transform
      returns the top-level keys in an order that varies between runs of the
      same flow with the same args. The transform input is built from a
      `HashMap` whose iteration order is randomly seeded. Observed: 8 runs gave
      5 distinct orders, `[a,mmm,zz]`, `[zz,mmm,a]`, `[zz,a,mmm]`,
      `[a,zz,mmm]`, `[mmm,a,zz]`;
    - **JavaScript enumeration still applies:** integer-index keys (`"0"`,
      `"10"`) enumerate first in ascending numeric order, then the other string
      keys. Only the order of the non-index top-level keys is randomised;
    - **nested objects** inside `flow_input` are parsed into order-preserving
      JSON objects and inserted into JavaScript in that order, so an
      unmodified nested object keeps its stored (jsonb) member order, subject
      to JavaScript enumeration. Objects built later by transforms or
      preprocessors have their own provenance;
    - **stable:** the stored root args, and an object argument as received by
      the script, are in jsonb order (`{"a":2,"zz":1,"mmm":3}` for input
      `{"zz":1,"a":2,"mmm":3}`).
  - **Harness:** arrays at a fixture-declared `unordered` path compare with
    **multiset (permutation) equality**: `["a","a","mmm","zz"]` must fail
    against `["a","mmm","zz"]`. Comparison stays order-sensitive everywhere
    else.
  - **Design note (not Windmill behaviour):** windmill-lite must not promise
    any top-level `flow_input` order in transforms, and fixtures must not
    depend on it.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:2712-2724`
  (`flow_args` as a `HashMap`); `backend/Cargo.toml:435`;
  `backend/windmill-jseval/src/lib.rs:456-465,848-865` (nested values parsed
  in order); observed on the v1.767.0 CE oracle.
- **Conformance:** [OR-23/a, OR-23/b](conformance.md#or-23)
- **Note:** transform semantics belong to [expressions.md](expressions.md).
- **Revision 2026-10-29 (oracle, batch E3e):** the `key_order` probe was
  recorded again (`conformance/flows/oracle/probes`, oracle run
  40794872025), with `fi_keys` compared as a multiset and `fi_whole_keys`
  exactly `["a","zz","mmm"]`.

<a id="or-24"></a>
### OR-24 · Key order at step boundaries: a provenance model

**Scope:** core · **Confidence:** high (ordinary, integer-key, retry, aggregate and flow-preprocessor paths oracle-observed; a script's own preprocessor source-derived) · **Status:** confirmed; drafted (when a script's own preprocessor runs, corrected 2026-10-29)

- **Rule:**
  - **Windmill:**
    1. **What a script's `main` receives** for an ordinary stored argument (no
       preprocessor): the stored jsonb member order (shorter keys first, then
       bytewise), **then** JavaScript enumeration (integer-index keys first,
       ascending). Bun parses the args file and calls `main` with the
       signature-named values. Example: `{"2":…,"a":…,"10":…}` → jsonb
       `["2","a","10"]` → `Object.keys` `["2","10","a"]` (recorded,
       [Q-OR-12](#q-or-12)).
    2. **A script's own preprocessor** builds new in-memory args and passes
       them straight to `main`; their order is the preprocessor's insertion
       order and is not re-sorted ([languages.md](languages.md)). It runs
       whenever a job of the script is pushed with preprocessing requested,
       which the run API does by default for a script that exports a
       `preprocessor` (a run by path or by hash without
       `skip_preprocessor=true`), as well as the triggers; no recording
       shows it yet.
       - **A flow's preprocessor** is different: its job's result is
         stored, and the root flow's args are then replaced, inside the
         database, by that stored result (an object as it is, `null` as
         `{}`, any other value as `{"value": …}`). The flow input the later
         modules see is therefore read back from storage, and an object the
         preprocessor built arrives in jsonb order
         ([FX-35](execution.md#fx-35)).
    3. **Inside transforms:**
       - `results.<id of the preceding step>` resolves to the available
         `previous_result`. That value is in memory (insertion order) **only on
         the ordinary straight-line path**;
       - it comes from storage (jsonb order) when a retry restored
         `previous_result` from stored results, when the flow resumed or
         re-entered without an in-memory last result, or when the preceding
         module is a loop or `branchall`, whose aggregate is fetched from
         storage (its element objects are in jsonb order);
       - every other `results.<id>` comes from storage (jsonb order).
    - A retried script reuses its stored args.
  - **Observed (ordinary path):** `key_order` ×8: `b`'s
    transform-computed `p_keys` and `r_keys` were `["bbb","a","cc"]`, and its
    received whole objects `["a","cc","bbb"]`. `key_order2`: `results.a` two
    steps back gave `["a","cc","bbb"]`, and `previous_result` (`{zzz,y}`) gave
    `["zzz","y"]`. Stable over 8 runs.
  - **Recorded (E3e, oracle run 40794872025):**
    - integer-index keys: a step returning `{"2":1,"a":2,"10":3}` gives
      `["2","10","a"]` both in the next step's transform
      (`Object.keys(results.a)`, the in-memory predecessor) and in the object
      it receives (`conformance/flows/oracle/key-order` run `int_keys`);
    - retry: on the first attempt the retried step's `skip_if` saw the
      in-memory predecessor `["bbb","a","cc"]`; on the retry it saw the
      stored one, `["a","cc","bbb"]` (run `retry_keys`);
    - loop aggregate: after a loop whose iteration returned `{b:1,a:2}`,
      `results.l` reads `[{"a":2,"b":1}]` (`conformance/flows/je/shortcut-texts`
      run `loop_prev`, the text the shortcut quotes);
    - flow preprocessor: an object it built as `{bbb,a,cc}` reached the
      first module's transform and argument as `["a","cc","bbb"]` (run
      `pre_keys`).
  - Not recorded: a script's own preprocessor (item 2; recordable through
    the run API, requested from batch E3f) and resume, which is out of
    scope ([Q-OR-11](#q-or-11)).
  - **Design note (not Windmill behaviour):** windmill-lite implements this
    provenance model, not an unconditional sort before every `main` call
    (which would break the preprocessor case). The rule is to be adopted by
    [execution.md](execution.md) and [expressions.md](expressions.md).
- **Evidence:** `backend/windmill-worker/src/common.rs:122-150`;
  `backend/windmill-worker/src/bun_executor.rs:1754-1764,1913-1916` (args file
  to `main`), `:1779-1790,1927-1937` (preprocessor args);
  `backend/windmill-jseval/src/lib.rs:805-817` (preceding id resolves to
  `previous_result`); `backend/windmill-worker/src/worker_flow.rs:1478-1489`
  (aggregate from storage), `:3323-3345` (in-memory or stored previous
  result), `:3771-3789` (retry restores from storage), `:3890-3907` (retry
  reuses stored args), `:1644-1677` (a flow preprocessor's stored result
  replaces the root's args); `backend/windmill-queue/src/jobs.rs:6419-6483`;
  `backend/migrations/20250201124431_v2_job.up.sql:31`; observed on the
  v1.767.0 CE oracle; recorded by `conformance/flows/oracle/key-order`,
  `conformance/flows/oracle/probes` and `conformance/flows/je/shortcut-texts`.
- **Conformance:** [OR-24/a, OR-24/b, OR-24/c, OR-24/d, OR-24/e, OR-24/f](conformance.md#or-24)
- **Revision 2026-10-29 (oracle, batch E3e; source re-check):** the retry,
  aggregate and integer-key cases were source-derived; E3e recorded each as
  stated ([Q-OR-11](#q-or-11) (a) and (b), [Q-OR-12](#q-or-12)). The
  preprocessor case the finding named is a script's own preprocessor, which
  runs only for a trigger and stays source-derived. E3e recorded a flow
  preprocessor instead, and its object arrives in jsonb order, not the
  preprocessor's: the source re-check found that the root's args are
  replaced in the database by the preprocessor job's stored result (item 2,
  new case OR-24/f). The summary's "preprocessor-built args keep their
  own order" now applies to a script's own preprocessor only.
- **Revision 2026-10-29 (SC3, source re-check, Q-OR-11 (c)):** item 2 said
  a script's own preprocessor runs only for a trigger. The run API also
  requests it: a run of a script by path or by hash marks the job for
  preprocessing when the deployed script has a preprocessor and the query
  does not set `skip_preprocessor` (`backend/windmill-common/src/jobs.rs:80-150`;
  `backend/windmill-api/src/jobs.rs:6053-6054`;
  `backend/windmill-queue/src/jobs.rs:5377-5384`), and the server sets
  that flag on deploy from a Bun script's exported `preprocessor`
  (`backend/windmill-api-scripts/src/scripts.rs:1194-1235`). OR-24/e can
  therefore be recorded without a trigger. The rule itself is unchanged.

<a id="or-25"></a>
### OR-25 · An empty chosen `branchone` branch yields the JSON string `"{}"`

**Scope:** core · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:** when the chosen `branchone` branch has no modules, the module
    is `Success` with a nil `job` and `branch_chosen: {"type":"branch","branch":<i>}`.
    For the **immediately following step**, both `previous_result` and
    `results.<branch id>` are the **string** `"{}"`, not an object: the nil-job
    lookup returns no value and the fallback is that string. No claim is made
    about later lookups of the node.
- **Evidence:** observed `branchone_empty`: `after.args == {"p":"{}","r":"{}"}`
  (strings); `backend/windmill-worker/src/worker_flow.rs:3325-3345` (string
  fallback), `:6244-6251` (nil-job lookup).
- **Conformance:** [OR-25/a](conformance.md#or-25)
- **Note:** the branch semantics belong to [control-flow.md](control-flow.md).
- **Revision 2026-10-29 (oracle, batch E3e):** `branchone_empty` was
  recorded again as stated (`conformance/flows/oracle/probes`, oracle run
  40794872025).

<a id="or-26"></a>
### OR-26 · Empty bodies in `branchall` and `forloopflow`

**Scope:** core · **Confidence:** high (oracle-observed and source) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - an empty `branchall` branch still runs (a `flowpreview` job with args
      `{}`) and contributes `{}` at its index. Both `branchall` callers build a
      payload for an empty body, so branch indices are preserved. A sequential
      and a parallel `branchall` of `[ret 0], [], [ret 2]` both return
      `[0, {}, 2]`;
    - a for-loop over `[]` has a nil `job`, `flow_jobs: []` and result `[]`; the
      next step sees `previous_result == []` and `results.l == []`.
  - **Harness:** record the module status independently of enumerating an
    empty `flow_jobs` list.
- **Evidence:** observed `branchall_seq`, `branchall_par`, `loop_empty`;
  `backend/windmill-worker/src/worker_flow.rs:5039-5065,5545-5560,5609-5632`
  (payload for empty branchall bodies), `:6244-6246` (list results).
- **Conformance:** [OR-26/a, OR-26/b](conformance.md#or-26)
- **Note:** semantics belong to [control-flow.md](control-flow.md); real
  parallelism is [Q-OR-15](#q-or-15), answered.
- **Revision 2026-10-29 (oracle, batch E3e):** `branchall_seq`,
  `branchall_par` and `loop_empty` were recorded again as stated
  (`conformance/flows/oracle/probes`, oracle run 40794872025), and on two
  workers a parallel `branchall` of `[slow 0], [], [2]` still returned
  `[0, {}, 2]` while branch 2 completed first (`conformance/flows/oracle/workers2`
  run `par_slow_empty`, oracle run 40792040448).

<a id="or-27"></a>
### OR-27 · A NUL character in a Bun result fails the step at storage time

**Scope:** core (the failure); later (Python); out (the delay) · **Confidence:** high (error shape and positions, oracle-observed); medium (a flow argument, source-read) · **Status:** confirmed; drafted (a flow argument, added 2026-10-29)

- **Rule:**
  - **Windmill:** a Bun step whose result contains U+0000 anywhere, in a
    string value or in an object key, at the top level or nested, fails,
    because the completion insert into jsonb is rejected:
    - the step and the root have status `failure`;
    - their result is
      `{"error":{"name":"InternalErr","message":"Internal: Could not add completed job <job>: error returned from database: unsupported Unicode escape sequence @jobs.rs:1133:22"}}`,
      with **no** `step_id`;
    - later steps are `not_run`.
    Recorded for the top-level string `"x\u0000y"`, the key of
    `{"k\u0000":1}` and the nested value of `{"a":["x\u0000"]}`.
  - **A Python result does not fail:** the Python wrapper rewrites each
    `\u0000` escape inside a JSON string of the dumped result as the text
    ` null ` (with its spaces) before the result is written, unless the
    script opts out with `#skip_result_postprocessing`
    ([WR-34](languages.md#wr-34)). Recorded: `"x\x00y"` returned by a Python
    step is stored and passed on as `"x null y"`.
  - **A flow argument holding U+0000** makes no job: the run request fails
    when the job row's jsonb `args` are inserted, and an SQL error answers
    HTTP 400 with its text. The harness cannot record it as a run.
  - **Not recorded:** a Deno result ([Q-OR-6](#q-or-6)).
  - **Out of scope:** the run took about 30 s (30,279 ms), and the next flow on
    the same worker took about 10 s instead of 0.27 s. The cause is unknown
    ([Q-OR-5](#q-or-5)).
  - **Harness normalisation:** for an `InternalErr` whose message matches that
    exact prefix, and only for it, the job id is replaced and the location
    `@<file>:<line>:<col>` is masked to `<loc>`, after the space that
    precedes it, so the normalised text ends `… sequence <loc>`. No other
    message suffix is masked.
  - **Design note (not Windmill behaviour):** for this scoped case the failure
    is a core compatibility requirement, not an optional divergence:
    windmill-lite must fail the step with the equivalent error.
- **Evidence:** observed `nul`; `backend/windmill-queue/src/jobs.rs:1095-1133`
  ("Could not add completed job …"), `:6418-6440` (a job's args inserted
  as jsonb); `backend/windmill-worker/src/result_processor.rs:673-735,1939-1975`
  (error propagation); `backend/windmill-worker/src/python_executor.rs:960-964`
  (the ` null ` rewrite), `:3301`, `:3307-3313` (applied to the dumped
  result unless skipped); `backend/windmill-common/src/worker.rs:954`
  (`skip_result_postprocessing`); `backend/windmill-common/src/error.rs:250-260`
  (an SQL error answers 400); Postgres jsonb rejects `\u0000`; recorded by
  `conformance/flows/oracle/nul` (oracle run 40794872025).
- **Conformance:** [OR-27/a, OR-27/b](conformance.md#or-27)
- **Revision 2026-10-29 (oracle, batch E3e; source re-check):** other
  positions and languages were open ([Q-OR-6](#q-or-6)). E3e recorded a NUL
  in a Bun object key and in a nested value, which fail exactly as the
  top-level string does, and a Python result, which does not fail: the
  wrapper's post-processing stores the NUL as ` null `. A flow argument
  holding a NUL was refused at submission, which is not a run; the source
  says why. The normalisation bullet now says the space before `<loc>`
  stays, as the recordings show. Each NUL failure took about 30 s again.

<a id="or-28"></a>
### OR-28 · Job kinds the walker meets

**Scope:** core (harness) · **Confidence:** high (source-traced and oracle-observed for deployed flows); medium (shapes not yet recorded) · **Status:** confirmed

- **Rule:**
  - **Windmill (deployed flows run by path):** a job's kind follows from the
    payload its parent pushes, and that follows from how the deployment
    stored the module:
    - **Deployment.** The lite form of a deployed flow replaces each inline
      `rawscript` in its module lists by a `flowscript` reference to a
      stored node, and stores every loop or branch body as a **flow node**
      unless the body is empty or is "simple": exactly one module of type
      `script`, `rawscript` or `flowscript` with no `sleep`, `suspend`,
      `cache_ttl`, `retry`, `stop_after_if`, `stop_after_all_iters_if` or
      `skip_if` and no enabled `mock`, in a flow without a failure module.
      A simple or empty body stays inline. The flow's `failure_module` and
      `preprocessor_module` are not reduced: they stay `rawscript` (or
      `script` by path), and so does the failure module's copy stored
      inside each body node.
    - **Kinds.**

      | Job | Kind | Stored args (recorded) |
      |---|---|---|
      | root | `flow` | the flow args |
      | inline step (`rawscript` reduced to a node) | `flowscript` | the evaluated input transforms |
      | step running a workspace script by path (also a by-path failure module) | `script` | the evaluated input transforms |
      | subflow by path | `flow` | the evaluated input transforms |
      | loop iteration or branch body stored as a flow node | `flownode` | an iteration: the body flow's args, `{"iter":{"index":…,"value":…}}` added ([CF-3](control-flow.md#cf-3)); a branch body: the parent's flow args |
      | loop iteration or branch body kept inline (a parallel loop or a branch whose body is simple, an empty `branchall` branch) | `flowpreview` | as for `flownode`; a `branchall` body gets the parent's flow args (`{}` when the root's args are `{}`, `{"k":1}` when they are `{"k":1}`) |
      | iteration of a collapsed sequential loop | the body module's own kind, `flowscript` or `script` | the evaluated body transforms, e.g. `{"i":0,"v":10}` |
      | inline failure module, its copy inside a body, inline preprocessor | `preview` | the failure module: its evaluated input transforms ([FX-32](execution.md#fx-32)); the preprocessor: a marker ([FX-35](execution.md#fx-35)) |
      | a step that runs no code: an `identity` module, a step skipped by `skip_if`, a mocked step, a retry that `skip_if` skips | `identity` | its transforms, or `{"previous_result": …}` for a skip or a mock |
      | a failed attempt (`failed_retries`) | the kind of the module's own job | as for that job |

    - A raw-flow child payload is `flowpreview` and an inline script payload
      is `preview` in any flow, so the kind tells neither a preview run from
      a deployed one nor a failure module from a preview script. A
      by-path failure module is `script`. The recorded batches E3a to E3d
      and E4a met every row above (`provenance.timings.jobs` of each
      recording holds each job's kind); shapes no batch recorded, such as
      AI-agent or suspended steps, are not covered.
  - **Harness:** the walker never keys off job kind; it reads each child's
    actual status ([OR-11](#or-11)).
- **Evidence:** `backend/windmill-types/src/jobs.rs:564-587` (payload to
  kind: `Code` → `preview`, `FlowNode` → `flownode`, `RawFlow` →
  `flowpreview`, `FlowScript` → `flowscript`, `ScriptHash` → `script`,
  `Identity` → `identity`); `backend/windmill-types/src/flows.rs:662-665`
  (the simple module types);
  `backend/windmill-worker/src/worker_lockfiles.rs:748-760` (only the module
  list is reduced to the lite form), `:1553-1591` (a body becomes a node
  unless it is empty or simple; the node keeps the failure module),
  `:1594-1700` (rawscripts become `flowscript` nodes; loop and branch
  bodies recurse); `backend/windmill-worker/src/worker_flow.rs:5039-5076`
  (`FlowNode` versus `RawFlow` child payload), `:5298` (simple-loop fast path
  disabled for `FlowPreview`), `:5733-5746` (the simple-body test),
  `:5940-6012` (a `rawscript` value becomes a `Code` payload, a `flowscript`
  value a `FlowScript` payload); observed on the v1.767.0 CE oracle; the
  recordings under `conformance/flows/` and the raw captures under
  `internal/oracle/testdata/captures/`.
- **Conformance:** [OR-28/a](conformance.md#or-28)
- **Revision 2026-10-28 (oracle, batches E3a to E3d and milestone E4a):** the
  table listed oracle run 1's shapes only, with every branch body
  `flowpreview` and every `branchall` body's args `{}`. The recordings show
  branch bodies as `flownode` when the deployment stored them as nodes,
  `branchall` bodies with the parent's flow args, `script` jobs for steps
  and failure modules by path, `flow` jobs for subflows by path, `preview`
  jobs for the preprocessor, and `identity` jobs for mocked steps and
  skipped retries. The rule now derives each kind from how the deployment
  stored the module.

<a id="or-29"></a>
### OR-29 · Environment seen by a Bun step in a flow (CE, standalone)

**Scope:** core (hand-off to languages and client API) · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill (observed, top-level flow `f/wl_probe/env`, step `a`):**
    - `WM_WORKSPACE`, `WM_TOKEN` (present), `WM_EMAIL` (`admin@windmill.dev`),
      `WM_USERNAME`, `WM_PERMISSIONED_AS` (`u/admin`);
    - `WM_JOB_ID` (the step's own job), `WM_FLOW_JOB_ID` (parent flow),
      `WM_ROOT_FLOW_JOB_ID` (innermost root flow), `WM_ROOT_JOB_ID` (outer
      root). The last three coincided only because the probe was a top-level
      flow. Recorded by E4a's stub fixtures (oracle run 40689406044): in a
      step inside a subflow by path, `WM_FLOW_JOB_ID` and
      `WM_ROOT_FLOW_JOB_ID` are the subflow's job; inside a parallel
      iteration both are that iteration's job; inside sequential, collapsed
      or nested sequential iterations `WM_FLOW_JOB_ID` is the iteration's
      flow (or, for a collapsed one, the loop's parent flow) and
      `WM_ROOT_FLOW_JOB_ID` the root; `WM_ROOT_JOB_ID` is the submitted root
      everywhere ([OR-V6](#or-v6), [Q-OR-10](#q-or-10));
    - `WM_FLOW_STEP_ID` (`a`), `WM_JOB_PATH` (`<flow path>/<step id>`),
      `WM_FLOW_PATH`;
    - `WM_STATE_PATH` (`<flow path>/<step id>_user`), `WM_OBJECT_PATH`
      (`f_wl_probe_env/manual/a/<ms timestamp>_<job id>`);
    - `WM_BASE_URL` (= `BASE_URL`), `WM_WORKER_GROUP` (`default`),
      `WM_RUNNABLE_ID`, `WM_SCHEDULED_FOR`;
    - `WM_SCHEDULE_PATH`, `WM_END_USER_EMAIL`, `WM_TESTED_RUNNABLE` (all `""`);
    - the whitelisted `WL_EFFECT_SINK`;
    - not set: `BASE_INTERNAL_URL`;
    - `WM_USERNAME` was `label-wl-oracle` when the run was submitted with an API
      token labelled `wl-oracle`; it depends on how the harness authenticates.
  - **Harness:** mask `WM_USERNAME` (and other environment-derived values,
    [OR-14](#or-14)) when comparing.
- **Evidence:** observed `env` probe; `backend/windmill-common/src/variables.rs:381-417`
  (distinct job ids).
- **Conformance:** [OR-29/a](conformance.md#or-29)
- **Note:** semantics belong to [languages.md](languages.md) and
  [client-api.md](client-api.md).
- **Revision 2026-10-28 (oracle, milestone E4a):** added the recorded values
  of the four job-id variables inside nested scopes, which answer
  [Q-OR-10](#q-or-10).
- **Revision 2026-10-29 (oracle, batch E3e):** the `env` probe was recorded
  (`conformance/flows/oracle/probes` run `env`, oracle run 40798097155):
  Bun `1.3.10` and the 21 `WM_` variables listed above, with `WM_USERNAME`
  `admin` because the harness submits with its login session's token.

<a id="or-30"></a>
### OR-30 · Disk and pull behaviour of the oracle image (harness preflight)

**Scope:** core (harness) · **Confidence:** high (policy); medium (pull measurements beyond the measured host) · **Status:** confirmed

- **Rule:**
  - **Windmill image facts:** the arm64 image uses 3.94 GB on disk (3,820 MiB
    of uncompressed layers); the amd64 variant is 1.37 GB compressed and
    4.03 GB (4,026,404,788 bytes) on disk, measured in CI by E2 (oracle run
    40649910412). During a pull, free space
    dropped about 1–1.5 GiB below the final after-state because of in-flight
    compressed layers. An interrupted pull keeps its completed layers, and the
    next pull resumes. In E2's CI session the pull of both pinned images
    consumed 4.3 GiB, against a computed need of 5.6 GiB (the stand-in
    estimate below), so the estimate erred on the safe side.
  - **Harness: fail-closed preflight and stop/resume policy:**
    1. **Measure** free space on the filesystem that holds the Docker data
       root.
    2. **Estimate the need** = on-disk image size for the target architecture
       (3.94 GB arm64 and 4.03 GB amd64, both measured; the compressed size
       × 3, 4.12 GB for amd64, was the stand-in before the measurement and
       is close to it) + 1.5 GiB transient margin +
       layers not yet present.
    3. **Start a pull only if** free ≥ 8 GiB **and** free − need ≥ 5 GiB.
    4. **While pulling,** abort the moment free < 5 GiB. Never lower the floor.
    5. **After an abort,** do not resume automatically: wait until free space
       has settled (two equal readings at least 30 s apart), recompute the need
       for the remaining layers, and resume only if a fresh measurement
       satisfies step 3. Otherwise stop, record the abort, and leave the
       completed layers for a later attempt or remove them.
    6. **After completion,** if settled free space is < 5 GiB, do not start the
       oracle; remove the image and record it.
    7. **During an oracle run,** stop the stack if free space drops below
       5 GiB.
    8. A planning figure of 10–11 GiB free (image + 5 GiB reserve + transient)
       is guidance, never authorisation: concurrent disk use can defeat any
       estimate, which is why steps 4–7 exist.
    - CI runners are measured the same way.
- **Evidence:** pull measurements of run 1; `docker image inspect` size; layer
  gzip trailers; the session report of oracle run 40649910412 (amd64 size
  and pull consumption).
- **Conformance:** [OR-30/a, OR-30/b, OR-30/c](conformance.md#or-30)
- **Revision 2026-10-28 (oracle, milestone E2):** the amd64 on-disk size was
  unmeasured and the need used the compressed size × 3. E2 measured
  4,026,404,788 bytes, close to the 4.12 GB stand-in, and its pull consumed
  less than the computed need; [Q-OR-2](#q-or-2) is answered. The policy
  itself is unchanged.

<a id="or-31"></a>
### OR-31 · `wmill sync push` is destructive: seed after pushing, only into harness-owned workspaces

**Scope:** core (harness) · **Confidence:** high (oracle-observed and source) · **Status:** confirmed

- **Rule:**
  - **Windmill CLI:**
    - push makes the remote workspace match the local tree **for the item
      kinds and paths selected by the sync configuration** (`wmill.yaml`
      includes and excludes, and the skip options);
    - selected items absent locally are removed; scripts are *archived*
      (`archiveScriptByPath`) rather than deleted;
    - the CLI itself warns that push "overwrites or deletes remote items that
      differ from or are absent locally";
    - observed: a variable `f/wl_probe/v` seeded before the first push was
      deleted by the push, and the probe then failed with
      `Variable f/wl_probe/v not found`; re-seeded after the push, the probe
      passed.
  - **Harness:**
    - seed variables and resources **after** the push (`setup.yaml` is applied
      post-push), or put them into the tree as `*.variable.yaml` /
      `*.resource.yaml`;
    - push only into a workspace the harness created for this run.
- **Evidence:** `cli/src/commands/sync/sync.ts:3625` (warning), `:3807,3827-3830`
  (includes, excludes, type skips), `:4726-4741` (archival and deletion
  logging); observed on the v1.767.0 CE oracle; recorded by
  `conformance/flows/oracle/seeded`.
- **Conformance:** [OR-31/a, OR-31/b](conformance.md#or-31)
- **Revision 2026-10-29 (oracle, batch E3e):** E3e recorded both cases. A
  variable seeded before the push (`oracle.setup: before_push`) answers 404
  after it, and the probe then fails with the missing variable's
  `ExecutionErr` (`conformance/flows/oracle/seeded` run `removed`, oracle
  run 40794872025); a variable seeded after the push answers 200 and the
  probe passes (`oracle/probes` run `args_vs_received`). The harness reads
  each `setup.yaml` variable back after every push, so every session checks
  OR-31/b again ([Q-OR-18](#q-or-18)).

## H. Findings added during validation

<a id="or-v1"></a>
### OR-V1 · Full argument and result capture needs dedicated endpoints

**Scope:** core · **Confidence:** high (source-traced and oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:** the generic `GET /api/w/{ws}/jobs_u/get/{id}` substitutes
    `{"reason":"WINDMILL_TOO_BIG"}` for args and the JSON string
    `"WINDMILL_TOO_BIG"` for results when the corresponding `pg_column_size` is
    at least 90000. This is a storage-size threshold, not a UTF-8
    response-length limit. Full stored args come from
    `GET /api/w/{ws}/jobs_u/get_args/{id}` and full results from
    `GET /api/w/{ws}/jobs_u/completed/get_result_maybe/{id}`.
  - **Observed:** step `a` returned 300,000 characters (306,474 bytes of JSON):
    generic `"result":"WINDMILL_TOO_BIG"`, dedicated endpoint the full object.
    Step `b`'s args (306,481 bytes): generic `{"reason":"WINDMILL_TOO_BIG"}`,
    dedicated complete. Step `c` literally returned `"WINDMILL_TOO_BIG"`: the
    dedicated endpoint gave the same string, and the generic output cannot tell
    the two apart.
  - **Windmill (key order):** `jobs_u/get` reads a job's `args` into a
    randomly seeded hash map of top-level keys and writes it out again, so
    the top-level key order follows no rule and varies between responses;
    `get_args` returns the stored jsonb text. Recorded: one smoke
    step's args read `{"tag":"hi","n":120}` from `jobs_u/get` and
    `{"n": 120, "tag": "hi"}` from `get_args`, and the `key_order` root's
    args came back as `{"mmm":3,"a":2,"zz":1}` and `{"zz":1,"a":2,"mmm":3}`
    in two repeats.
  - **Harness:** use `jobs_u/get` only for status, topology and timing; fetch
    args and results with `get_args` and `get_result_maybe` for every job;
    never treat the substitutions as the step's output.
- **Evidence:** `backend/windmill-api/src/jobs.rs:1424-1427,1463-1472,2505-2553,9215-9270`;
  `backend/windmill-types/src/jobs.rs:192`, `:351` (`args` held as a hash
  map); observed on the v1.767.0 CE oracle; the committed raw capture
  `internal/oracle/testdata/captures/smoke-fail.json`; recorded by
  `conformance/flows/oracle/big-payload` (oracle run 40794872025).
- **Conformance:** [OR-V1/a, OR-V1/b](conformance.md#or-v1)
- **Revision 2026-10-29 (oracle, batch E3e):** `big_payload` was recorded
  again with the same sizes (306,474 bytes of result, 306,481 bytes of
  args); the recording holds the complete payload from the dedicated
  endpoints, and the generic endpoint's substitutes are in that run's raw
  capture (its CI artifact, not committed). Added the key-order bullet, from the committed captures and
  the `key_order` probe's raw captures.

<a id="or-v2"></a>
### OR-V2 · Distinguish skipped, failed, canceled and never run

**Scope:** core · **Confidence:** high (source and oracle) · **Status:** confirmed

- **Rule:**
  - **Windmill:**
    - the poll reports `success` only for database status `success`; the
      generic completed-job endpoint treats `skipped` as successful and also
      exposes `is_skipped`;
    - a `WaitingForPriorSteps` entry has no job;
    - a `branchone` predicate that throws reaches `StepFailure`, which updates
      the status with a nil UUID: a failed module with no executed child.
  - **Harness status model:**
    - **job level:** `canceled` if `canceled`; else `skipped` if `is_skipped`;
      else `success`/`failure` from `success`;
    - **module level:** no `job` field → `not_run`; `Success` + nil job →
      `{"status":"success","ran":false}`; `Failure` + nil job →
      `{"status":"failure","ran":false}` with the flow's error preserved,
      never turned into an empty success;
    - **module `skipped` has two meanings**, kept separate from the job flags:
      the step that *triggered* a skip-stop, and a `skip_if` step that ran as
      an `identity` job;
    - the status projection applies even when `flow_jobs` is empty.
  - **Observed:**
    - `stop_skip` (`skip_if_stopped: true`): poll `success:false`, job
      `success:true, is_skipped:true` → `skipped`; root result `1` (step `a`'s
      result); `a.skipped:true`;
    - `stop_plain` (`skip_if_stopped: false`): `success`, result `1`;
    - `skip_if`: the skipped step ran as an `identity` job (module
      `skipped:true`, job `success:true, is_skipped:false`, args
      `{"previous_result":"a0"}`, result `"a0"`).
  - **Recorded (E3a, `conformance/flows/je/branches` run `je31-a`, oracle run
    40687124625):** a `branchone` whose first predicate throws is a
    `Failure` entry with the nil job, recorded `{"status":"failure","ran":false}`;
    no body runs, and the root fails with the predicate's `Anyhow` error and
    no `step_id` ([Q-OR-8](#q-or-8)).
- **Evidence:** `backend/windmill-api/src/jobs.rs:1424,9220-9225`;
  `backend/windmill-types/src/flow_status.rs:219-297`;
  `backend/windmill-worker/src/worker_flow.rs:3842-3920` (`skip_if` as an
  identity job), `:4000-4028`, `:4053-4067,5470-5485` (nil-job failure);
  observed on the v1.767.0 CE oracle; recorded by E3a's `je31-a`.
- **Conformance:** [OR-V2/a, OR-V2/b, OR-V2/c, OR-V2/d, OR-V2/e](conformance.md#or-v2)
- **Revision 2026-10-28 (oracle, batch E3a):** the shape of a throwing
  `branchone` predicate was pending [Q-OR-8](#q-or-8); E3a recorded it, and
  it matches the nil-failure rule above.
- **Revision 2026-10-29 (oracle, batch E3e):** the probes `stop_skip`,
  `stop_plain`, `loop_empty` and `skip_if` were recorded again as stated
  (`conformance/flows/oracle/probes`, oracle run 40794872025; the skipped
  step's job has kind `identity`).

<a id="or-v3"></a>
### OR-V3 · Normalisation must preserve negative comparisons

**Scope:** core · **Confidence:** high (counterexamples) · **Status:** DISPUTED

- **Rule (as drafted):**
  - **Harness:** replace automatically only established runtime identities
    (walked job ids) and volatile envelope fields. Preserve unknown UUIDs and
    arbitrary user strings; extra masking only through fixture-declared paths.
    Scope envelope dropping (`tag`, `worker`, timestamps …) to the envelope,
    never recursively into user JSON. Traverse object keys deterministically
    before assigning placeholders, and keep exact numeric values.
  - **Stack masking requires runtime-error provenance.** A value's
    `error.stack` is masked only if:
    - (a) the value is the stored result of a **failed leaf job that executed
      a script** (`flowscript`, `script`, or a script-executing `preview`) and
      has the shape `{"error":{…}}`; or
    - (b) it is deep-equal, before normalisation, to such an (a) value in the
      same run, that is a propagated runtime error (including a failure
      handler that deliberately returns the error it received); or
    - (c) the fixture declares the path.
  - Never masked: a successful failure handler's own result, and therefore a
    failed root that carries it, keeps its user data.
  - Unordered arrays compare as multisets, never as sets.
  - Define escaping and collision handling for semantic paths and
    placeholder-looking user strings ([OR-14](#or-14) item 2).
  - **Windmill fact behind it:** Bun results are arbitrary user JSON, and a
    successful handler's result becomes a failed root's result
    ([OR-12](#or-12)).
- **Correction (validation):** the negative comparisons (unknown UUIDs,
  handler data, multiplicity) are fixed, but the consolidated identity
  encoding still allows false equivalence: special-slot paths collide with
  ordinary module ids, and percent escaping does not escape `%`
  ([OR-V8](#or-v8)).
- **Open:** [Q-OR-17](#q-or-17).
- **Evidence:** `backend/windmill-worker/src/bun_executor.rs:1943-1944` (Bun
  results stay arbitrary user JSON); `backend/windmill-worker/src/worker_flow.rs:2002-2058`.
  This is a harness correctness requirement, not a claim that Windmill
  normalises values.
- **Conformance:** [OR-V3/a, OR-V3/b, OR-V3/c, OR-V3/d](conformance.md#or-v3)

<a id="or-v4"></a>
### OR-V4 · Observe key order before the next job's argument boundary

**Scope:** core · **Confidence:** high (method; ordinary-path results oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Harness (probe method):** to test the key order a transform sees, compute
    `Object.keys(previous_result)` and `Object.keys(results.a)` inside the
    JavaScript input transforms themselves and pass the arrays to an echo
    step; probe `Object.keys(flow_input)` the same way at the first step.
    Arrays keep the observed order through later storage. A whole-object echo
    is a separate probe of script-argument serialisation and cannot replace the
    transform-scope probe, because both whole objects become stored step
    arguments before Bun inspects them. Normaliser key sorting must leave these
    arrays untouched.
  - Results: [OR-23](#or-23), [OR-24](#or-24).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:3323-3345`
  (previous-result selection), `:2712-2774` (transform evaluation);
  `backend/windmill-queue/src/jobs.rs:6419-6483` (argument insertion);
  observed on the v1.767.0 CE oracle.
- **Conformance:** [OR-V4/a, OR-V4/b](conformance.md#or-v4)
- **Revision 2026-10-29 (oracle, batch E3e):** the `key_order` probe's `b`
  was recorded again with the same four arrays
  (`conformance/flows/oracle/probes`, oracle run 40794872025), and the
  method carried the new key-order recordings of [OR-24](#or-24).

<a id="or-v5"></a>
### OR-V5 · Stable CI observations and volatile provenance are separate

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Harness:** compare only the declared normalised observation fields. Keep
    recording date, raw timings, host and architecture, image identity, CLI
    version and harness version as provenance, with explicit compatibility
    checks for the pinned versions. A rerun must not fail merely because its
    date or measured durations changed. Canonicalisation covers object
    traversal as well as final serialisation. Keep the raw captures for
    diagnosing normalisation errors.
  - **Windmill:** execution timestamps and durations necessarily differ across
    runs.
- **Evidence:** consistency of [OR-14](#or-14), [OR-15](#or-15),
  [OR-17](#or-17), [OR-19](#or-19); `backend/windmill-api/src/jobs.rs:9530-9585`.
- **Conformance:** [OR-V5/a, OR-V5/b](conformance.md#or-v5)
- **Note:** run 1's equality checks are recorded evidence; they cannot be
  replayed from retained captures.

<a id="or-v6"></a>
### OR-V6 · Use the outer run identity for effects

**Scope:** core (effect capture) · **Confidence:** high (source; nested equality oracle-observed) · **Status:** confirmed

- **Rule:**
  - **Windmill:** `WM_ROOT_FLOW_JOB_ID` is the "Job id of the innermost root
    flow", not necessarily the submitted outer run. Named subflows and parallel
    for-loop payloads start a new innermost scope, while the outer root is
    propagated separately (`WM_ROOT_JOB_ID`).
  - **Harness:** partition a run's effects by the root id the harness
    submitted, using `WM_ROOT_JOB_ID`, and verify that a reported id belongs to
    the harness-owned run instead of treating every new innermost id as a fresh
    comparison. Keep `WM_FLOW_JOB_ID` and parent metadata for ancestry. Keep
    this grouping policy apart from tests of the environment values themselves
    ([OR-16](#or-16)).
- **Evidence:** `backend/windmill-common/src/variables.rs:381-417`;
  `backend/windmill-worker/src/common.rs:535-550,1834-1839`;
  `backend/windmill-worker/src/worker_flow.rs:4316-4332,4418-4421`.
- **Conformance:** [OR-V6/a](conformance.md#or-v6)
- **Note:** where the innermost scope starts: a flow that pushes a child
  for a subflow by path or for a parallel for-loop gives the child no
  innermost root, so the child flow's own steps take that child as their
  innermost root; every other child (sequential iterations, branch bodies,
  steps) inherits the parent's innermost root, or the parent itself when
  it has none. The outer root is copied unchanged into every descendant.
- **Revision 2026-10-28 (oracle, milestone E4a):** the note said the
  nested equality of `WM_ROOT_JOB_ID` was pending [Q-OR-10](#q-or-10).
  E4a's `conformance/flows/stub/scopes` recorded it (oracle run 40689406044):
  a subflow by path and two parallel iterations each started a new
  innermost root while every effect named the submitted root, and a
  concurrent outer run kept its own. Q-OR-10 is answered.

<a id="or-v7"></a>
### OR-V7 · Generated virtual steps require an explicit capture policy

**Scope:** core (trailing sleep capture); out (durable suspend) · **Confidence:** high (source-traced; live shape pending) · **Status:** confirmed; drafted (where the generated module shows, added 2026-10-29)

- **Rule:**
  - **Windmill:** if the **last** module of a module list has `sleep` or
    `suspend`, Windmill appends a generated `identity` module with
    `id` = `<last id>-v` and summary "Virtual module needed for suspend/sleep
    when last module". It is added at lock and deploy time, at raw-flow push,
    and when child payloads are built. Users can also name a module `a-v`.
    - **Where it shows** (source-traced 2026-10-29). At deploy time it is
      added to the **lite** form only, so the deployed value that
      `flows/get` returns (the full form) never contains it, and neither
      does a pulled `flow.yaml` ([OF-3](openflow.md#of-3)). A preview's
      raw-flow push adds it before the status list is built, and
      an inline loop or branch body gets it whenever its payload is built.
    - **Its status entry.** With an initial-lite status
      ([OF-V5](openflow.md#of-v5)) the root's `flow_status.modules` holds a
      slot `{"type": "WaitingForPriorSteps", "id": "a-v"}` from the start.
      When `a` succeeds, the slot's job is pushed as an identity job (a job
      row of its own, kind `identity`) scheduled for `a`'s sleep after that
      moment; it returns its `previous_result` argument, so the entry ends
      `Success` with that job and a result equal to `a`'s. With a
      full-built status there is no slot and no job.
  - **Harness:**
    - a module is generated **iff** it is `identity`, last in its list, has
      `id == <previous id>-v`, the previous module has `sleep`/`suspend`,
      **and** the fixture's sync tree has no module with that id in that list.
      A user-authored `a-v` stays visible;
    - raw captures keep it; the semantic projection omits it from `steps` /
      modules; the root `result` is unchanged, because identity passes the
      value through.
  - **Design note (not Windmill behaviour):** a compiler that implements the
    wait without a synthetic job must not be reported as missing a step.
- **Evidence:** `backend/windmill-types/src/flows.rs:1252-1279`;
  `backend/windmill-worker/src/worker_lockfiles.rs:1704-1707`;
  `backend/windmill-worker/src/worker_flow.rs:5056`;
  `backend/windmill-queue/src/jobs.rs:5605,5956`.
- **Conformance:** [OR-V7/a–c](conformance.md#or-v7)
- **Note:** E3a's `je28-c` recording shows the generated step exists on a
  deployed flow whose last step sleeps (that step's `sleep` is evaluated and
  can fail the flow, [JE-28](expressions.md#je-28)); its raw status entry and
  deployed value are still unrecorded ([Q-OR-9](#q-or-9)), and the source
  now predicts both (OR-V7/c).
- **Revision 2026-10-28 (oracle, batch E3a):** the note said the live
  topology was pending; E3a's recording shows the generated step's effect,
  and Q-OR-9 is re-scoped to the parts still unrecorded.
- **Revision 2026-10-29 (SC3, source re-check, Q-OR-9):** "added at lock
  and deploy time" is narrowed: the dependency job writes the generated
  module into the lite form only (`backend/windmill-worker/src/worker_lockfiles.rs:1706`,
  inside the lite reduction; the full value written back at `:733-745`
  has none), so `flows/get` never shows it. A raw-flow push adds it before
  building the status (`backend/windmill-queue/src/jobs.rs:5605`, status at
  `:5651`); an old-worker enqueue adds it only after the status is built
  (`:5950-5957`). The step runs as an identity job
  (`backend/windmill-worker/src/worker_flow.rs:5141`), scheduled from the
  previous module's sleep (`:3666-3690`), whose result is its
  `previous_result` argument (`backend/windmill-worker/src/worker.rs:4032-4038`).
  New case OR-V7/c gives the predicted deployed value and status entry.

<a id="or-v8"></a>
### OR-V8 · Semantic identities must distinguish special slots and literal module ids

**Scope:** core · **Confidence:** high (specification collision); acceptance of unusual ids not verified · **Status:** DISPUTED

- **Rule (as drafted):** the walker, the `<job:PATH>` placeholders and any
  table keyed by semantic path share the path encoding of [OR-14](#or-14)
  item 2: ordinary modules use their `id`; the special slots use the
  unqualified paths `failure` and `preprocessor`; a literal `[`, `]`, `/` or
  `#` in a module id is percent-escaped (`%5B`, `%5D`, `%2F`, `%23`); a user
  string that looks like `<job:…>` is escaped to `<<job:…>`.
- **Correction (validation):** that encoding is not injective, and the
  identity must be injective:
  - an ordinary module called `failure` and a separate failure handler called
    `recover` both become `failure`; the same holds for an ordinary module
    called `preprocessor`. Windmill keeps ordinary modules and special slots
    separately, `FlowModule.id` is a plain string, the API-side flow validation
    does not reserve these names, and both are ordinary identifier spellings
    in OpenFlow;
  - the escaping does not escape its own escape character, so literal `a/b`
    and literal `a%2Fb` both become `a%2Fb` (reproduced by a local evaluation
    of the stated rule; no oracle used). Whether Windmill admits such ids is
    a separate, unverified question; the `failure` counterexample does not
    depend on unusual characters;
  - **required:** the observation identity distinguishes an ordinary module
    from a failure or preprocessor slot, and escapes its escape character if
    it uses textual escaping; placeholders preserve those distinctions. Typed
    segments (module / iteration / retry / special slot) are one way. A shared
    encoding must be chosen and documented before the harness is implemented.
    This is a harness design requirement; Windmill's naming rules do not
    change.
- **Open:** [Q-OR-17](#q-or-17).
- **Evidence:** `backend/windmill-types/src/flow_status.rs:506-531` (special
  slots kept apart); `backend/windmill-types/src/flows.rs:546`
  (`FlowModule.id` is a string), `:222-265` (flow validation reserves no names);
  `openflow.openapi.yaml:284-286`.
- **Conformance:** [OR-V8/a, OR-V8/b, OR-V8/c](conformance.md#or-v8)

<a id="or-v9"></a>
### OR-V9 · A probe recipe must specify the actual transform scope

**Scope:** core (reliable conformance evidence) · **Confidence:** high (oracle-observed) · **Status:** confirmed

- **Rule:** a probe recipe used as conformance input specifies every
  transform and its actual binding.
  - **Windmill:** no bare `iter` is bound in transform scope. The worker
    inserts the iteration object into the iteration's flow arguments, so
    iteration transforms read `flow_input.iter.index` and
    `flow_input.iter.value`. The literal `iter.value` fails at the
    iteration's first step with
    ``{"error":{"name":"ExecutionErr","message":"Error during isolated evaluation of expression `iter.value`:\nQuickJS evaluation error: Error: iter is not defined\n    at <anonymous> (eval_script:1:1)\n    at <eval> (eval_script:1:39)\n"}}``
    and no `step_id`: that step has empty args, the iteration's later steps
    do not run, and the loop and the flow fail with the iteration results,
    `[{"error": …}]`.
  - **The catalogue's three disputed recipes**, rebuilt with their bindings
    written out (`v→flow_input.iter.value` in `loop_seq_multi` and
    `loop_par`, `flow_input.n` in `branchone`), reproduce run 1's recorded
    results and topologies: `["a12","b12"]` from two-step `flownode`
    iterations, `[10,20,30]` from `flowpreview` iterations, and `"pos"`,
    `"big"`, `"default"` from `flowpreview` bodies.
  - A rebuilt recipe is a reconstruction, not the deleted fixture of run 1;
    the committed fixture is the evidence now.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:4160-4173`
  (iteration object inserted into flow args), `:2711-2765` (transform scope);
  `backend/windmill-jseval/src/lib.rs:281-290,456-465` (`flow_input`
  installed), `:2591-2614` (iteration tests use `flow_input.iter.index/value`);
  recorded by `conformance/flows/oracle/probes` (oracle run 40794872025).
- **Conformance:** [OR-V9/a, OR-V9/b, OR-V9/c, OR-V9/d](conformance.md#or-v9)
- **Revision 2026-10-29 (oracle, batch E3e):** the finding was
  `needs-evidence`. As drafted it took the catalogue's recipes as written:
  `loop_seq_multi` with body `x1(v→iter.value)`, `branchone` with the bare
  predicates `n>10` and `n>0`, and `loop_par` with no transform for `v`, the
  literal bare-`iter` recipe having been reported as succeeding. The
  validation's correction said no bare `iter` exists and the recipes must
  spell their bindings. E3e settled it for the correction: the rebuilt
  recipes reproduce the recorded results, and the literal variant fails
  with the error above (runs `loop_seq_multi`, `loop_seq_multi_bare`,
  `loop_par`, `branchone_5`, `branchone_50`, `branchone_m1`).
  [Q-OR-16](#q-or-16) is answered.

<a id="probe-catalogue"></a>
## Probe catalogue

Synthetic probes recorded in oracle run 1 on CE v1.767.0 with one standalone
worker. Every probe is a Bun `rawscript` unless noted; `x→v` means input
transform `x = <javascript v>`; "static" marks a static value; each flow schema
declares only the listed args; all were pushed with `windmill-cli@1.767.0`
after `generate-metadata`. Observations are normalised projections (status, key
results, topology); raw ids and timings are omitted except where a row gives a
timing. Paired rows expand to 26 flow definitions; with repeats, the missing
variable case and the synchronous composite call, they account for the 45 runs.

The fixture files were not retained. The recipes below are the recorded
descriptions. Recording batch E3e rebuilt every probe as an executable,
committed fixture with its bindings written out
([`conformance/flows/oracle/probes`](../../conformance/flows/oracle/probes),
with `nul`, `big_payload` and the missing variable in
[`oracle/nul`](../../conformance/flows/oracle/nul),
[`oracle/big-payload`](../../conformance/flows/oracle/big-payload) and
[`oracle/seeded`](../../conformance/flows/oracle/seeded)), and each
reproduced the observation below. Rows marked **reconstructed recipe** did
not specify their transform bindings correctly as recorded; the fixture
spells them ([OR-V9](#or-v9), [Q-OR-16](#q-or-16), answered).

| Probe | Definition | Run (args) | Recorded observation | Findings |
|---|---|---|---|---|
| `smoke/two_step` | the [OR-20](#or-20) fixture | `{"x":3}` ×3; `{"x":60}` ×2 | as in [OR-20](#or-20) and [OR-12](#or-12) | [OR-20](#or-20) |
| `key_order` | `a0(fi_keys→Object.keys(flow_input), fi_whole→flow_input)` returns `{fi_keys, fi_whole_keys:Object.keys(fi_whole)}`; then `a()` returns `{bbb:1,a:2,cc:3}`; then `b(p_keys→Object.keys(previous_result), r_keys→Object.keys(results.a), p_whole→previous_result, r_whole→results.a)` returns `{p_keys, r_keys, p_whole_keys, r_whole_keys}` | `{"zz":1,"a":2,"mmm":3}` ×8 | `a0.args.fi_keys`: 5 distinct permutations of `[a,mmm,zz]`. `a0.result.fi_whole_keys`: `[a,zz,mmm]`. `b.result`: `{p_keys:[bbb,a,cc], r_keys:[bbb,a,cc], p_whole_keys:[a,cc,bbb], r_whole_keys:[a,cc,bbb]}`. Stored root args: `{a,zz,mmm}` | [OR-23](#or-23), [OR-24](#or-24), [OR-V4](#or-v4) |
| `key_order2` | `a()` returns `{bbb:1,a:2,cc:3}`; then `m()` returns `{zzz:1,y:2}`; then `b(r_keys→Object.keys(results.a), p_keys→Object.keys(previous_result))` | `{}` | `{r_keys:[a,cc,bbb], p_keys:[zzz,y]}` | [OR-24](#or-24) |
| `nul` | `a()` returns `"x\u0000y"`; then `b(p→previous_result, r→results.a)` | `{}` | `a` and the root fail with the [OR-27](#or-27) `InternalErr`; `b` `not_run`; about 30 s | [OR-27](#or-27) |
| `loop_seq_simple` | for-loop `l`, iterator `[10,20,30]`, sequential; body `x(v→flow_input.iter.value, i→flow_input.iter.index)` returns `v*2+i` | `{}` ×3 | result `[20,41,62]`; collapsed: `l[i]` are `flowscript` leaves with args `{i,v}`; first run 10 s (right after `nul`), then 0.27 s | [OR-11](#or-11), [OR-28](#or-28) |
| `loop_seq_multi` (**reconstructed recipe**) | for-loop `l`, iterator `["a","b"]`, sequential; body `x1(v→iter.value)` returns `v+"1"`, then `x2(p→previous_result)` returns `p+"2"`. The fixture binds `v→flow_input.iter.value`; the literal `iter.value` is run `loop_seq_multi_bare`, which fails with `iter is not defined` ([OR-V9](#or-v9)) | `{}` | result `["a12","b12"]`; `l[i]` are `flownode` with args `{"iter":{index,value}}` | [OR-28](#or-28), [OR-V9](#or-v9) |
| `loop_par` (**reconstructed recipe**) | for-loop, iterator `[1,2,3]`, parallel; body `x(v)` returns `v*10`. The `v` transform was unstated; the fixture binds `v→flow_input.iter.value` | `{}` | result `[10,20,30]`; `l[i]` are `flowpreview` | [OR-28](#or-28), [OR-V9](#or-v9) |
| `loop_empty` | for-loop `l` over `[]`; then `after(p→previous_result, r→results.l)` | `{}` | result `{p:[], r:[]}`; `l` is `Success`, nil job, `flow_jobs:[]` | [OR-26](#or-26), [OR-V2](#or-v2) |
| `while_simple` | while-loop `w`; body `x(i→flow_input.iter.index)` with `stop_after_if {expr: result >= 2, skip_if_stopped: false}` | `{}` | result `[0,1,2]`; `w[i]` are `flownode` (not collapsed: the body has `stop_after_if`) | [OR-11](#or-11), [OR-28](#or-28) |
| `branchone` (**reconstructed recipe**) | `br`: `[n>10 → big returns 'big'; n>0 → pos returns 'pos']`, default `def` returns `'default'`. `n` stands for `flow_input.n`, which the fixture spells | `{n:5}`, `{n:50}`, `{n:-1}` | `"pos"` (branch 1), `"big"` (branch 0), `"default"` (type default); the body is a `flowpreview` child whose args are the flow args | [OR-28](#or-28), [OR-V9](#or-v9) |
| `branchone_empty` | `br`: `[expr 'true', modules []]`, default `def`; then `after(p, r→results.br)` | `{}` | result `{p:"{}", r:"{}"}` (strings); `br` is `Success`, nil job, branch 0 | [OR-25](#or-25) |
| `branchall_seq` / `branchall_par` | `ba`: `[b0 returns 0] [] [b2 returns 2]`, sequential / parallel | `{}` | result `[0,{},2]`; 3 `flowpreview` children; the empty child's result is `{}` | [OR-26](#or-26) |
| `retry` | `a` with `retry {constant {attempts:2, seconds:0}}`; body POSTs a sink counter and throws while n < 2 (the fixture counts in a workspace variable reset before each run, since the sink answers no count) | `{}` | result `{"attempt":2}`; `a.failed_retries` length 1; attempt `a#0` failed | [OR-11](#or-11) |
| `fail_handler` | `a` throws `Error('boom')`; `b` returns `'unreached'`; `failure_module(error→error)` returns `{handled:true, saw:error?.message}` | `{}` | root failure, `{"saw":"boom","handled":true}`; handler job kind `preview`, args `{"error":…}`, `parent_module "a"`; `b` `not_run` | [OR-12](#or-12) |
| `fail_mid` | `a` throws `class MyErr extends Error` (name `MyErr`, `code=42`); `b` unreached | `{}` | root failure with the `MyErr` envelope, `extra:{code:42}`, `step_id:"a"` | [OR-12](#or-12) |
| `stop_skip` / `stop_plain` | `a()` returns `1` with `stop_after_if {expr: result == 1, skip_if_stopped: true/false}`; `b` unreached | `{}` | `stop_skip`: job `success:true, is_skipped:true`, poll `success:false`, result `1`, `a.skipped:true`. `stop_plain`: success, result `1`. Both: `b` `not_run` | [OR-V2](#or-v2) |
| `skip_if` | `a0()` returns `'a0'`; `a` with `skip_if {expr: flow_input.skip}` returns `'ran a'`; `b(p→previous_result, r→results.a)` | `{skip:true}`, `{skip:false}` | `skip:true`: `{p:"a0", r:"a0"}`, `a` an `identity` job with args `{"previous_result":"a0"}`. `skip:false`: `{p:"ran a", r:"ran a"}` | [OR-V2](#or-v2) |
| `args_vs_received` | `a(s static "$var:f/wl_probe/v")` returns `s`; the variable (value `"secret-ish"`) is created **after** the push | `{}` | stored args `{"s":"$var:f/wl_probe/v"}`, result `"secret-ish"`; with the variable missing, the `ExecutionErr` of [OR-12](#or-12) | [OR-22](#or-22), [OR-31](#or-31) |
| `big_payload` | `a` returns `{data}`: 300,000 characters from xorshift32 (seed 2463534242; `s^=s<<13; s>>>=0; s^=s>>>17; s^=s<<5; s>>>=0`; char = 33 + s % 94); `b(d→results.a)` returns `{len, head: first 16}`; `c` returns `'WINDMILL_TOO_BIG'` | `{}` | generic versus dedicated endpoints as in [OR-V1](#or-v1); root result `"WINDMILL_TOO_BIG"` | [OR-V1](#or-v1) |
| `effects` | `a` then `b`, each `await`-POSTing `{step}` to the sink, keyed in run 1 by `WM_ROOT_FLOW_JOB_ID`; the current body is that of [OR-16](#or-16) / [OR-V6](#or-v6) | `{}` ×2 | each run: `[{"step":"a"},{"step":"b"}]` | [OR-16](#or-16) |
| `delete_after_use` | `a(x→flow_input.x)` with `delete_after_use: true`, logs a line, returns `{secret:x}`; `b(p→previous_result)` (the committed fixture has lost the key, which `generate-metadata` and the deployment both drop, [OR-21](#or-21)); the fixture adds `subflow_dau`, the same step inside a subflow by path | `{x:7}` | result `{p:{secret:7}}`; on CE `a`'s args, result and logs are retained | [OR-16](#or-16), [OR-21](#or-21) |
| `sleep` | `a` with `sleep {static 2}` returns `'a'`; `b` returns `'b'` | `{}` | `b.started_at − a.completed_at` = 2021.971 ms | [OR-17](#or-17) |
| `composite` | `a` returns `{windmill_status_code:201, result:{a:1}}` | `{}` | asynchronous: `{"result":{"a":1},"windmill_status_code":201}`; `run_wait_result`: HTTP 201, `application/json`, `{"a": 1}` | [OR-10](#or-10) |
| `env` | `a` returns `{bun: Bun.version, env: <WM_* vars, token masked>}` | `{}` | as in [OR-29](#or-29); `bun` = `"1.3.10"` | [OR-2](#or-2), [OR-29](#or-29) |

<a id="runbook"></a>
## Runbook

### What was executed and what was not

- **Executed (oracle run 1, CE v1.767.0, arm64):** both images pulled by digest;
  the compose below brought up with one standalone worker; normal-auth
  bootstrap and workspace creation; `folder add-missing`, `generate-metadata`
  and `sync push` with `windmill-cli@1.767.0`; the deployment gate; the smoke
  fixture (`ok` ×3, `fail` ×2); every probe in the
  [Probe catalogue](#probe-catalogue); resource measurements; and a verified
  teardown (`down -v`, scratch removed).
- **Not executed:** any multi-worker profile ([OR-5](#or-5), [Q-OR-7](#q-or-7),
  [Q-OR-15](#q-or-15)); any amd64 or CI recording ([Q-OR-2](#q-or-2)); the
  slim-image comparison ([OR-19](#or-19)); the `oss` source build
  ([Q-OR-1](#q-or-1)); preview-run equivalence ([Q-OR-3](#q-or-3)); `NO_AUTH`
  ([Q-OR-4](#q-or-4)); the knob comparison and a lowered result-size limit
  ([OR-7](#or-7), [Q-OR-14](#q-or-14)); the subflow `delete_after_use` probe
  ([OR-21](#or-21)); and every probe named in [Q-OR-5](#q-or-5)–[Q-OR-13](#q-or-13),
  [Q-OR-16](#q-or-16) and [Q-OR-17](#q-or-17). Run 1's raw captures and fixture
  tree were not retained, so no unit test can load them.
- **Since run 1:** the oracle runs in public CI ([OR-19](#or-19)). Recording
  batch E3e has run there the two-worker profile, the knob comparison, the
  subflow `delete_after_use` probe, the probe catalogue's reconstruction and
  the probes of [Q-OR-6](#q-or-6), [Q-OR-7](#q-or-7), [Q-OR-11](#q-or-11),
  [Q-OR-12](#q-or-12) and [Q-OR-15](#q-or-15).

### Minimal compose

Original configuration written for windmill-lite. It applies the 1 GiB
Postgres limit ([OR-4](#or-4)) and the Postgres digest pin ([OR-1](#or-1)).

```yaml
name: wl-oracle
services:
  db:
    image: postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab
    environment: { POSTGRES_PASSWORD: changeme, POSTGRES_DB: windmill }
    tmpfs: [/var/lib/postgresql/data]
    mem_limit: 1g
    healthcheck: { test: ["CMD-SHELL", "pg_isready -U postgres"], interval: 2s, retries: 30 }
  windmill:
    image: ghcr.io/windmill-labs/windmill:1.767.0@sha256:9239264a7a849deedda87868147e65eb910456039fe9d961901e2e5b143ebd43
    environment:
      DATABASE_URL: postgres://postgres:changeme@db/windmill?sslmode=disable
      MODE: standalone
      BASE_URL: http://localhost:8000
      LISTEN_NEW_EVENTS_INTERVAL_SEC: "1"
      WHITELIST_ENVS: WL_EFFECT_SINK
      WL_EFFECT_SINK: http://host.docker.internal:18080
    ports: ["127.0.0.1:8000:8000"]
    extra_hosts: ["host.docker.internal:host-gateway"]
    mem_limit: 2g
    depends_on: { db: { condition: service_healthy } }
    healthcheck: { test: ["CMD-SHELL", "curl -fsS http://localhost:8000/api/version"], interval: 2s, retries: 90 }
```

For a two-worker profile, add a `worker` service with the same image,
`MODE: worker`, the same `DATABASE_URL`, `WHITELIST_ENVS` and `WL_EFFECT_SINK`,
no ports, and `mem_limit: 2g`.

### Steps

1. Run the disk preflight of [OR-30](#or-30); pull both images by digest only
   if it passes.
2. `docker compose -p wl-oracle up -d`; record the wall time until
   `/api/health/status?force=true` reports `healthy` with `workers_alive ≥ 1`.
3. Record `docker image inspect --format '{{.Size}}'` for both images and each
   container's cgroup memory peak (a single `docker stats` sample is not a
   peak).
4. Log in, keep the token, create the run's workspace ([OR-6](#or-6)).
5. From the fixture's `windmill/` directory, with a fresh `--config-dir`, run
   `windmill-cli@1.767.0` `folder add-missing --yes` and `generate-metadata --yes`
   (when creating or refreshing the fixture), then `sync push --yes`
   ([OR-8](#or-8)). Apply `setup.yaml` after the push ([OR-31](#or-31)), then
   pass the deployment gate ([OR-9](#or-9)).
6. Submit each run with `POST /api/w/<ws>/jobs/run/f/<path>`, poll
   `get_result_maybe`, walk `flow_status`, and fetch full data with
   `get_args` / `get_result_maybe` ([OR-10](#or-10), [OR-11](#or-11),
   [OR-V1](#or-v1)).
7. Normalise ([OR-14](#or-14)), write `recorded/` with provenance
   ([OR-15](#or-15), [OR-V5](#or-v5)), and repeat a fixture to check stability.
8. `docker compose -p wl-oracle down -v`; check that no containers, network or
   volumes remain ([OR-18](#or-18)); remove the CLI config directory, CLI
   package cache, fixture copy and scratch; keep the synthetic fixture
   definitions and normalised captures.

## Open questions

<a id="q-or-1"></a>
- **Q-OR-1** · affects [OR-3](#or-3) · Does a source build with the `oss`
  feature set give normalised observations identical to the CE image? If so it
  is a licence-clean alternative oracle. *Settled by:* building the `oss` image,
  running every catalogue fixture on both and diffing the projections.
  Optional.

<a id="q-or-2"></a>
- **Q-OR-2** · affects [OR-2](#or-2), [OR-4](#or-4), [OR-19](#or-19),
  [OR-30](#or-30) · **Answered 2026-10-28.** E2's first CI session on
  `ubuntu-latest` (oracle run 40649910412) measured the amd64 image at
  4,026,404,788 bytes on disk, a healthy oracle 6.4 s after `compose up`, and
  cgroup memory peaks of 328,458,240 B (Windmill) and 309,182,464 B
  (Postgres) for the smoke fixture; the pull consumed 4.3 GiB against a
  computed need of 5.6 GiB ([OR-2](#or-2), [OR-4](#or-4), [OR-30](#or-30)).
  *Originally asked:* what are the amd64 image's on-disk size, peak memory
  and boot time, without extrapolating the arm64 measurements?

<a id="q-or-3"></a>
- **Q-OR-3** · affects [OR-8](#or-8), [OR-28](#or-28) · Is a preview run
  (`run/preview_flow`) semantically equal to a deployed run? Raw topology can
  differ, because the simple for-loop collapse is disabled for `FlowPreview`.
  *Settled by:* running each fixture through `run/preview_flow` and `run/f` and
  diffing the projections. Optional.

<a id="q-or-4"></a>
- **Q-OR-4** · affects [OR-6](#or-6) · Under `NO_AUTH=true`, does a job's
  `WM_TOKEN` work for client calls? *Settled by:* the `env` probe plus one
  `wmill` client call (`getVariable`) under `NO_AUTH`. Optional.

<a id="q-or-5"></a>
- **Q-OR-5** · affects [OR-27](#or-27) · Why did the NUL failure take about
  30 s and the next run about 10 s? Outside the semantic guarantees.
  *Settled by:* re-running `nul` with `JSON_FMT=true` server logs and per-job
  timings. Out of scope. *2026-10-29:* batch E3e saw the same pattern
  without server logs (`conformance/flows/oracle/nul`, oracle run
  40794872025): each failing step took about 30 s, and the runs right after
  a failure took about 10 s more from submission to completion; still open.
  *2026-10-29 (SC3):* the 30 s follows from the source: the completion
  insert is retried 10 times with a constant 3 s delay before the job is
  failed ([FX-48](execution.md#fx-48) case 1;
  `backend/windmill-queue/src/jobs.rs:1001-1012`). A read of the completion
  and error paths does not explain the following run's extra 10 s, which
  only the server logs can show. Still open, out of scope; not requested
  from batch E3f.

<a id="q-or-6"></a>
- **Q-OR-6** · affects [OR-27](#or-27) · **Answered 2026-10-29** (except
  Deno). Batch E3e recorded the variants (`conformance/flows/oracle/nul`,
  oracle run 40794872025): a Bun result with a NUL in an object key
  (`{"k\u0000":1}`) or a nested value (`{"a":["x\u0000"]}`) fails exactly as
  [OR-27/a](conformance.md#or-27) does; a Python step returning `"x\x00y"`
  succeeds with `"x null y"`, because the Python wrapper rewrites the escape
  ([WR-34](languages.md#wr-34)). A flow argument holding a NUL was refused at
  submission, so no job exists to record; the source places the failure at
  the job row's jsonb insert ([OR-27](#or-27)). A Deno result was not
  recorded (`later`). Original question: does a NUL character in an object
  key, a nested value or a flow input, or in a Python or Deno result, also
  fail? *Settled by:* `nul` variants returning `{"k\u0000":1}` and
  `{"a":["x\u0000"]}`, a flow arg `"x\u0000"`, and a Python step returning the
  same. Status: closed (the Deno half stays unrecorded).

<a id="q-or-7"></a>
- **Q-OR-7** · affects [OR-11](#or-11), [OR-5](#or-5) · **Answered
  2026-10-29.** Batch E3e ran the settling profile in the oracle workflow's
  two-worker session (`conformance/flows/oracle/workers2` runs `loop_slow`
  and `par_slow`, oracle run 40792040448): the slow index-0 body of a
  parallel loop and of a parallel `branchall` completed after index 1, the
  completed module's `job` was that last-completing child, `flow_jobs[0]`,
  and `flow_jobs` and the result stayed in index order (`[0, 1]`), as the
  source rule says ([OR-11](#or-11)). The `job` values are in the session's
  raw captures, not the observation. Original question: with two or more
  workers, what is the module `job` when parallel completion order differs
  from index order? *Settled by:* a two-worker profile running a parallel
  `branchall` / loop whose index 0 is slow (`await Bun.sleep(1500)`),
  recording `job`, `flow_jobs` and the result order. Status: closed.

<a id="q-or-8"></a>
- **Q-OR-8** · affects [OR-V2](#or-v2), [OR-11](#or-11) · **Answered
  2026-10-28.** E3a recorded a `branchone` after one step whose first
  predicate throws (`conformance/flows/je/branches` run `je31-a`, oracle run
  40687124625, predicate `nope`): the module is `Failure` with the nil job
  (`{"status":"failure","ran":false}`), no branch body runs, and the root
  fails with `{"error":{"name":"Anyhow","message":"Error: QuickJS evaluation error: Error: nope is not defined\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:42)\n<loc>"}}`,
  with no `step_id` ([OR-V2/e](conformance.md#or-v2); the message's other
  sites are [JE-31](expressions.md#je-31) and [JE-35](expressions.md#je-35)).
  *Originally asked:* what module status and root result does a throwing
  `branchone` predicate record?

<a id="q-or-9"></a>
- **Q-OR-9** · affects [OR-V7](#or-v7) · **Re-scoped 2026-10-28.** E3a's
  `conformance/flows/je/waits` run `je28-c` (oracle run 40687124625) shows
  that a deployed flow whose last step sleeps runs a generated step after
  it: the last step's invalid `sleep` is evaluated when the generated step
  is scheduled, and fails the flow ([JE-28](expressions.md#je-28),
  [OF-17](openflow.md#of-17)). Still unrecorded: the generated module's
  deployed value in `flows/get` and its `flow_status` entry, which the
  walker omits. *Settled by:* a fixture whose last step has
  `sleep: {type: static, value: 1}`, recording `flows/get` and the raw
  `flow_status`. *2026-10-29:* batch E3e did not record this (its `sleep`
  probe sleeps on a step that is not last); still open. **Re-scoped
  2026-10-29 (SC3).** The source predicts both parts ([OR-V7](#or-v7), new
  case [OR-V7/c](conformance.md#or-v7)): `flows/get` shows no `a-v`, since
  only the lite form carries it, and the raw `flow_status` ends with a
  `Success` entry `a-v` whose job is an identity job scheduled about 1 s
  after `a` completed, with `a`'s result. What remains is the recording,
  requested from batch E3f.

<a id="q-or-10"></a>
- **Q-OR-10** · affects [OR-16](#or-16), [OR-V6](#or-v6), [OR-29](#or-29) ·
  **Answered 2026-10-28: yes.** E4a recorded exactly the settling fixture
  (`conformance/flows/stub/scopes`, runs `r` and `t` submitted together and
  `alone`, oracle run 40689406044): inside the subflow by path and in each
  parallel iteration `WM_ROOT_JOB_ID` is the submitted root while
  `WM_ROOT_FLOW_JOB_ID` and `WM_FLOW_JOB_ID` are the subflow's or the
  iteration's own job, and the concurrent run keeps its own root.
  Sequential, collapsed and nested sequential iterations keep the root as
  `WM_ROOT_FLOW_JOB_ID` (`stub/nested`, `stub/collapsed`, `stub/retry_loop`).
  The source agrees ([OR-V6](#or-v6)). *Originally asked:* is
  `WM_ROOT_JOB_ID` the submitted root inside named subflows and parallel
  loops?

<a id="q-or-11"></a>
- **Q-OR-11** · affects [OR-24](#or-24) · **Answered 2026-10-29 for (a) and
  (b); (c) re-scoped.** Batch E3e recorded (oracle run 40794872025):
  (a) on the retry the retried step's `skip_if` saw the stored predecessor,
  `["a","cc","bbb"]`, after seeing the in-memory `["bbb","a","cc"]` on the
  first attempt (`conformance/flows/oracle/key-order` run `retry_keys`;
  the throwing predicate then failed the flow with a chaining
  `InternalError`, [OR-24/c](conformance.md#or-24)); (b) a loop aggregate
  is read in jsonb order (`conformance/flows/je/shortcut-texts` run
  `loop_prev`, [OR-24/d](conformance.md#or-24)). For (c) E3e recorded a
  *flow's* preprocessor, whose object is read back in jsonb order
  (`a, cc, bbb`; run `pre_keys`, new [OR-24/f](conformance.md#or-24)). A
  script's own preprocessor, which (c) names, runs only for a trigger
  ([WR-11](languages.md#wr-11)); the harness fires none, so
  [OR-24/e](conformance.md#or-24) stays source-derived and (c) stays open
  for it. (d) is out of scope. Original question: what key order is seen on
  the retry, aggregate, resume and preprocessor paths? *Settled by:*
  - (a) **retry**, probed at the retried step's own predicate, not after it
    (the step after a retried step sees that step's new successful result, and
    a retried script reuses its stored args): `a` returns `{bbb:1,a:2,cc:3}`;
    `b` has one retry and its body throws; `b`'s `skip_if` returns false while
    `Object.keys(previous_result)[0]` is `bbb` and otherwise throws a
    diagnostic containing the whole key array. On the retry, record whether
    the diagnostic contains `["a","cc","bbb"]`, the stored predecessor's
    order;
  - (b) the step after a loop of objects reports
    `Object.keys(previous_result[0])`;
  - (c) a Bun script with a `preprocessor` that builds `{bbb,a,cc}` reports
    `Object.keys` of the argument;
  - (d) resume is out of scope (durable suspend).

  Status: closed for (a) and (b); open for (c), a script's own
  preprocessor. **Re-scoped 2026-10-29 (SC3).** (c) needs no trigger: the
  run API requests preprocessing itself. A run of a script by path or by
  hash (`POST /api/w/{w}/jobs/run/p/<path>`, `run_wait_result/p/<path>`,
  `run_wait_result/h/<hash>`) marks the job for preprocessing whenever the
  deployed script has a preprocessor and the query does not set
  `skip_preprocessor=true`, and on deploy the server sets that flag itself
  from a Bun script's exported `preprocessor`
  (`backend/windmill-common/src/jobs.rs:80-150`, the flag from the
  deployed row; `backend/windmill-api/src/jobs.rs:6053-6054`;
  `backend/windmill-queue/src/jobs.rs:5377-5384`;
  `backend/windmill-api-scripts/src/scripts.rs:1194-1235`). So
  [OR-24/e](conformance.md#or-24) can be recorded with the ordinary run
  API; requested from batch E3f.

<a id="q-or-12"></a>
- **Q-OR-12** · affects [OR-23](#or-23), [OR-24](#or-24) · **Answered
  2026-10-29: yes.** Batch E3e recorded the settling run
  (`conformance/flows/oracle/key-order` run `int_keys`, oracle run
  40794872025): both the next step's transform `Object.keys(results.a)`
  and the object it received gave `["2","10","a"]`
  ([OR-24/b](conformance.md#or-24)). Original question: end to end, do
  integer-index keys enumerate first? *Settled by:* a step returning
  `{"2":1,"a":2,"10":3}`, then the next step's transform
  `Object.keys(results.<id>)` and its received-object keys. Status: closed.

<a id="q-or-13"></a>
- **Q-OR-13** · affects [OR-11](#or-11) · **Answered 2026-10-28: yes.** E3d
  recorded a while-loop whose single body module has no `stop_after_if` and
  which the loop module's own `stop_after_if` ends
  (`conformance/flows/control-flow/cf-10` run `a`, oracle run 40685057858,
  [CF-10](control-flow.md#cf-10), [CF-11](control-flow.md#cf-11)): the body
  collapses, each iteration is the body module's own `flowscript` job with
  the evaluated transforms (`{"x":0}`, `{"x":1}`, `{"x":2}`) and no child
  flow, the loop's result keeps the iteration that made the predicate true,
  and the flow continues. *Originally asked:* can a collapsible while body
  terminate, and what shape does it record?

<a id="q-or-14"></a>
- **Q-OR-14** · affects [OR-7](#or-7) · What error does the result-size limit
  produce? *Settled by:* a lowered `MAX_RESULT_SIZE_MB=3` profile with a 4 MB
  result. Optional; distinct from the API truncation of [OR-V1](#or-v1).
  *2026-10-29:* batch E3e did not record this (OR-7/b is deferred: the knob
  would change every observation of its session); still open. **Answered
  2026-10-29 (SC3)** from the source ([OR-7](#or-7), [OR-7/b](conformance.md#or-7)):
  the job fails at once with name `ResultTooLarge` and the message
  `Result of job <job id> is too large: <n>MB > MAX_RESULT_SIZE_MB=<m>MB.` plus
  a pointer to object storage, without retries or location suffix. A
  recording in a session of its own remains optional (batch E3f).

<a id="q-or-15"></a>
- **Q-OR-15** · affects [OR-26](#or-26), [OR-11](#or-11) · **Answered
  2026-10-29: yes.** Batch E3e ran the [Q-OR-7](#q-or-7) profile with an
  empty middle branch (`conformance/flows/oracle/workers2` run
  `par_slow_empty`, oracle run 40792040448): branch 2 completed before the
  slow branch 0, and the result was `[0, {}, 2]`, the empty branch at
  index 1. Original question: are `branchall` branch indices preserved
  under real parallelism? *Settled by:* the Q-OR-7 profile with an empty
  middle branch. Status: closed.

<a id="q-or-16"></a>
- **Q-OR-16** · affects [OR-V9](#or-v9), [OR-15](#or-15), [OR-28](#or-28) ·
  **Answered 2026-10-29: yes.** Batch E3e rebuilt the catalogue as executable
  fixtures with every binding written out
  (`conformance/flows/oracle/probes`, 30 runs, with `oracle/nul`,
  `oracle/big-payload` and `oracle/seeded`; oracle runs 40794872025 and
  40798097155). Each probe reproduced its recorded result and topology;
  `loop_seq_multi` with `flow_input.iter.value` gave `["a12","b12"]` from
  two-step `flownode` iterations, and the literal bare-`iter` variant failed
  with `iter is not defined` ([OR-V9/b](conformance.md#or-v9)). The
  recordings and 17 committed raw captures retain the tree and the
  observations. [OR-15](#or-15) and [OR-V9](#or-v9) are now `confirmed`.
  Original question (not converged): can the probe catalogue be
  reconstructed into executable fixtures with retained evidence? As
  recorded, `loop_seq_multi` uses a bare
  `iter.value`, which is not a transform binding; `branchone` uses undeclared
  shorthand `n`; `loop_par` omits the transform for `v`. *Settled by:*
  reconstructing and retaining the synthetic sync tree and normalised
  captures; running `loop_seq_multi` with `flow_input.iter.value` and requiring
  `["a12","b12"]` with two-step child flows; separately running the literal
  bare-`iter` variant and recording its evaluation failure (capturing the
  exact wrapper, not inventing it); spelling out `flow_input.n` and
  `flow_input.iter.value` in the branch and parallel recipes and reproducing
  their recorded results. This verifies reconstructed fixtures; it does not
  recover run 1's deleted artefacts. Status: closed.

<a id="q-or-17"></a>
- **Q-OR-17** · affects [OR-V8](#or-v8), [OR-11](#or-11), [OR-14](#or-14),
  [OR-V3](#or-v3) · **Not converged.** What collision-free identity encoding
  will the walker, the placeholders and any path-keyed table share? The
  drafted special paths `failure` / `preprocessor` collide with ordinary module
  ids, and the percent escaping omits the escape character. *Settled by:*
  choosing an explicitly injective encoding (for example typed path segments),
  backed by local negative-comparison tests and oracle captures: an ordinary
  module named `failure` that throws plus a separate handler named `recover`;
  an ordinary `preprocessor` module plus a differently named actual
  preprocessor (each pair kept as distinct records and placeholders); and, if
  unusual ids are admitted, `a/b` and `a%2Fb` kept distinct (otherwise record
  their rejection). *2026-10-29:* the harness has chosen typed site paths
  (design record 0011: `m:<id>` for an ordinary module, `s:failure` and
  `s:preprocessor` for the slots), and every recording since E2 uses them,
  but batch E3e recorded none of the colliding pairs above; still open
  until those captures exist. *2026-10-29 (SC3):* nothing in the source
  settles this: the encoding is a harness design choice (already made), and
  what remains is the colliding-pair captures, requested from batch E3f.

<a id="q-or-18"></a>
- **Q-OR-18** · affects [OR-5](#or-5), [OR-7](#or-7), [OR-8](#or-8),
  [OR-21](#or-21), [OR-31](#or-31) · **Answered 2026-10-29.** Batch E3e
  recorded the remaining parts: the one-level blocking child completes on
  two workers, with the child's result as the step's
  ([OR-5/b](conformance.md#or-5); `conformance/flows/oracle/workers2` run
  `blocking`, oracle run 40792040448); the smoke observations are unchanged
  at `LISTEN_NEW_EVENTS_INTERVAL_SEC=10` on two workers, which the
  workflow's second session checks every run ([OR-7/a](conformance.md#or-7));
  a subflow step's args and result are not blanked on CE
  ([OR-21/a](conformance.md#or-21); `conformance/flows/oracle/probes` run
  `subflow_dau`, oracle run 40794872025), though the deployed flow no longer
  carries `delete_after_use` at all ([OR-21](#or-21)); and a variable
  seeded before a push answers 404 after it and 200 once `setup.yaml` is
  applied, which every session checks ([OR-31/b](conformance.md#or-31)).
  Status: closed. Earlier entry: **Re-scoped 2026-10-28.** `flows/get`
  returning the pushed module ids is recorded: since E2 the harness checks
  every deployed flow's module ids against its tree after the deployment
  gate ([OR-8/b](conformance.md#or-8)). The rest is still open. (Added when
  this area was assembled, to
  track source-derived expectations that no question above covers.) Do the
  remaining harness-integration expectations hold on the oracle: a one-level
  blocking child completing on a two-worker profile; byte-identical normalised
  smoke observations with `LISTEN_NEW_EVENTS_INTERVAL_SEC=1` and `10`;
  the CE outcome of `delete_after_use` inside a subflow; and the 404 / 200
  variable check around a push? *Settled by:* the first `record` run that
  includes [OR-5/b](conformance.md#or-5), [OR-7/a](conformance.md#or-7),
  [OR-21/a](conformance.md#or-21) and [OR-31/b](conformance.md#or-31).

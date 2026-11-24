# Platform interface, the `wmill` shim, and hybrid dispatch

**Status:** Accepted · **Topic:** the services a step can call (resources,
variables, secrets, state, object store, async dispatch, resume), how stock
client code reaches them, job identity, and delegating chosen steps to a
real Windmill.

Step code reaches the platform through the
[platform facade](../glossary.md#platform-facade), by intercepted `fetch` in
warm and host-mode processes and by a per-invocation token over loopback
TCP in [`fresh`](../glossary.md#fresh) ones; a token that names no live
invocation gets a 401. Each service is a Go interface that a host can
replace, with a local default for every service but `Resume`, and under the
default `fail` policy a step that called an unsupported route fails with
`UnsupportedPlatformCall`.

**Not built yet:**

- hybrid dispatch to a real Windmill: step delegation and the proxied
  platform, with their interfaces;
- the `Resume` provider, and cacheable answers from TypeScript providers;
- `createVariable`, resource writes of a type other than `state`, object
  deletion, generated object keys and presigning, which answer 501;
- the job routes other than the root lookup, the job reads, flow user
  state, dispatch, the result polls and the flow restart, which answer 501
  too;
- the option that makes the result polls answer for a job of a live run
  that the dispatcher did not start (§Services);
- the settings of the job identity in the go-plugin adapter (§Job
  identity);
- removing windmill-lite's job ids from a step's own dispatch to a real
  Windmill (§Async dispatch).

## Context

Step code calls the platform through the stock `windmill-client` (TS) or
`wmill` (Python): about 25 helper endpoints, plus generated services and raw
`fetch` against `OpenAPI.BASE` ([WA-1](../semantics/client-api.md#wa-1),
[WA-8](../semantics/client-api.md#wa-8), [WA-9](../semantics/client-api.md#wa-9)).
The TS client fixes its base URL and token at import; every other `WM_*` is
read at call time. Error bodies and status codes are part of the contract
([WA-42](../semantics/client-api.md#wa-42)), and so is the stock clients'
habit of not failing on some error statuses
([WA-41](../semantics/client-api.md#wa-41)). Resolution of `$var:`/`$res:`
has three contexts with different rules ([WA-12](../semantics/client-api.md#wa-12),
[WA-V3](../semantics/client-api.md#wa-v3), [WA-40](../semantics/client-api.md#wa-40)).
A host may need its own secret layer inside resource values, and may want
some steps to run on a real Windmill.

## Options considered

1. **Alias the client module** (`windmill-client` → a shim). The shim must
   then reproduce the whole export surface, including generated services
   scripts call directly ([WA-V4](../semantics/client-api.md#wa-v4)); any
   bundled copy of the client escapes it. Kept only for narrow overrides.
2. **A loopback HTTP facade** the stock client talks to unmodified.
   Language-neutral and faithful to the HTTP contract.
3. **In-process `fetch` interception** in Bun, routed to the same Go router
   over the harness channel. No TCP for the common case.

## Decision

**(2) and (3) together, backed by one Go router**, as the reference's design
note recommends ([WA-30](../semantics/client-api.md#wa-30)); policy case
WA-30/a checks that both paths give identical step results and identical
recorded calls.

### Services

Each service is a Go interface with a local default; a host replaces any of
them, per runtime or per run ([host hooks](0008-host-hooks.md)).

| Service | Routes it serves | Default | Scope |
|---|---|---|---|
| `Resources` | `resources/get_value_interpolated`, `get_value`, `exists`, `create`, `update_value`, `delete` | the bundle's resource files plus host overrides, then the resource store's entries | core (read; write of type `state`), later (write of other types) |
| `Variables` | `variables/get_value`, `exists`, `create`, `update` | the bundle's variable files; secrets through `Secrets` | core (read), later (write) |
| `Secrets` | used by the two above | plain-secret folders work; ciphertext without a host resolver fails as a value Windmill cannot decrypt | core |
| `State` | the resource routes at the state path, type `state` | `ResourceStore`: a memory store per runtime (D24), unless the host gives one, such as the Redis store of `wl/redisstore` | core |
| `FlowUserState` | `jobs_u/get_root_job_id`, `jobs/flow/user_states/{root}/{key}` | a map per effective root, discarded when the root run ends; `null` for a missing key | core |
| `ObjectStore` | `job_helpers/download_s3_file`, `upload_s3_file`, `delete_s3_file` | a local directory keyed by (storage, key) | core (explicit keys), later (the rest) |
| `Dispatcher` | `jobs/run/p`, `run/h`, `run/f`, `jobs_u/completed/get_result_maybe`, `get_result` | runs the target from the bundle as a detached run of the same runtime | core (interface) |
| `Jobs` | `jobs_u/get_root_job_id/{id}`, `jobs_u/get/{id}`, `jobs_u/get_args/{id}` | answers from the run registry for windmill-lite ids ([D3](0001-equivalence-contract.md#5-named-deviations), job ids that are windmill-lite's own): the root lookup for a live or an ended job, the job reads for a job of a live run (D22 for an ended one) | core |
| `ResultCache` | none (engine-internal, `cache_ttl`) | per process (D4); the key and TTL rules of [FX-40](../semantics/execution.md#fx-40) | core; later (S3-aware argument hashing) |
| `Resume` | `jobs/resume_urls` | none: a step that asks gets `UnsupportedPlatformCall`; a host provider may return `{approvalPage, resume, cancel}` | core (call shape, provider interface); out (resuming, since `suspend` is rejected) |

**A sibling's outcome.** A step can read another job of its own run, such
as a subflow's flow job whose id it took from its flow's status:

- The job read (`jobs_u/get/{id}`) answers for the jobs of a live run,
  with `success` and `result` once the job has completed
  ([WA-19](../semantics/client-api.md#wa-19)), for the records that the
  oracle has recorded, and 501 for the others. These include a job that
  windmill-lite canceled, a flow job that ended neither in a success nor in
  a failure, a flow with a preprocessor module, and a flow whose provided
  site waits for a sleep or a retry delay.
- The result polls (`jobs_u/completed/get_result_maybe/{id}` and
  `get_result/{id}`) answer only for the runs that the dispatcher started.
  For another job that the job registry knows in the caller's workspace
  they answer 501, and for an id that it does not know, Windmill's answer
  for a job with no row.
- A runtime option is planned to make the result polls answer for every
  job of a live run that the job read answers for, from the run registry,
  with the shapes of [WA-V2](../semantics/client-api.md#wa-v2) and
  [WA-19](../semantics/client-api.md#wa-19). A job of an ended run then
  answers as the job read does
  ([D22](0001-equivalence-contract.md#5-named-deviations)), and the 501s
  stay the default.
- A provided site has no job, so a step that reads its outcome gets it
  from a provider too
  ([host hooks](0008-host-hooks.md#patterns-built-from-the-hooks)).
- The exact answers for a job that a flow created wait for a recording
  ([recording requests](README.md#recording-requests)).

**Where a run's resources and variables come from.** A read takes the
first answer of these sources, in this order:

1. the run's own values: a Go host's `RunPlatform` ([H13](0008-host-hooks.md#the-hooks)), or the
   `resources` and `variables` of a go-plugin `RunRequest` (planned);
2. the run's own providers on a go-plugin host's broker (planned), which
   are the run's hooks and so come before the plugin's;
3. the host plugin's `resource` and `variable` providers;
4. the runtime's host values, `wl`'s `Options.Resources` and `Variables`,
   which the go-plugin adapter's configuration is to fill;
5. the bundle's `data/`;
6. for a resource, the resource store's entries.

With the read cache on
([D21](0001-equivalence-contract.md#5-named-deviations)), a run keeps the
providers' first successful answer for a path, before interpolation, and
answers its later reads of that path from it. An error is not kept, a
variable that a step updates is read again, and the resource store's
entries, below the providers, are read each time.

A source that reads a real
Windmill's API is the proxied platform of hybrid dispatch (§Hybrid dispatch
to a real Windmill), which waits for the decision named there. A host's own
provider may read any source that the host chooses.

**Flows that suspend until notified**, such as a step that waits for a
semaphore that another job releases. windmill-lite runs no `suspend`: the
loader refuses one that a run can reach, unless every path to it passes
through a mandatory host-provided call site
([IR, loader and validation](0002-ir-and-loader.md#host-contract-and-mandatory-host-provided-call-sites),
[FX-39](../semantics/execution.md#fx-39)).

The decision for such a flow is a declared stand-in. The host contract
makes the call site of the subflow that suspends a provided site, whose
provider gives the result that the subflow would give once notified. The
provider is a static value, a host plugin's provider or, under go-plugin, a
provider on the broker, so a host that admits work per tenant can make its
own admission the stand-in
([host hooks](0008-host-hooks.md#patterns-built-from-the-hooks)).

Rejected for such flows:

- **Not supported, documented, alone.** A host could then not run a flow
  that waits on such a semaphore at all, although every other step of it
  runs here.
- **A polling wait in the engine.** It would rebuild Windmill's durable
  suspend and resume, which the
  [equivalence contract](0001-equivalence-contract.md#3-out-of-scope)
  leaves out, and a run would hold its harness process and its deadline
  while it polls.

### One interpolation walker, three modes

`platform` implements resolution once ([WA-12](../semantics/client-api.md#wa-12)):

1. **raw read** (`get_value`): no interpolation;
2. **client interpolated read** (`get_value_interpolated` without a job id):
   `$WM_*` stays literal;
3. **worker argument resolution** (before a step runs,
   [JSON at step boundaries](0005-json-step-boundaries.md)): `$var:`, `$jsonvar:`, `$res:`,
   `$encrypted:` and reserved `$<NAME>` strings. A nested `$res:` is an
   interpolated resource read made **with** the job's id: unlike mode 2, its
   `$WM_*` strings resolve, from the job's stored fields (so
   `$WM_ROOT_JOB_ID` can be `""`), and it starts a fresh depth counter
   ([WA-V3](../semantics/client-api.md#wa-v3)).

Depth fails at 50 on every node; arrays are walked only at depth ≤ 2 with at
most 1000 items; `$var:` and `$jsonvar:` output is terminal; a `$res:` path
needs a `/`. API reads answer with the bodies of
[WA-42](../semantics/client-api.md#wa-42), for example
`Not found: Resource not found at name <p>` for a missing nested resource,
which Windmill ends in a parenthesised Rust location that windmill-lite
does not write (D14). Worker argument resolution
fails the step with its own texts instead, for example
``Variable <p> not found for `<name>`…`` ([JE-42](../semantics/expressions.md#je-42),
[WR-5](../semantics/languages.md#wr-5)).

**Host secret layer.** After interpolation, each resource value and each
secret variable passes through the host's `ValueResolver` chain. A host can
recognise its own markers inside values (for example a `$secret:` prefix)
and replace them. windmill-lite defines no marker syntax of its own and never
decrypts Windmill ciphertext; it hands that to the resolver
([JE-43](../semantics/expressions.md#je-43), [SY-11](../semantics/openflow.md#sy-11)).

**Values windmill-lite seals.** A flow step stores a value computed from a
secret `flow_env` entry as an `$encrypted:` value under its root job's key,
which only that root's jobs decrypt
([JE-38](../semantics/expressions.md#je-38)). windmill-lite holds no
workspace key, so it seals such a value with a key of its own: a random key
per process, from which each root job's key is derived.

- Worker argument resolution opens a value sealed for the job's root in the
  same process, and hands every other `$encrypted:` value to `Secrets`.
- Nothing outside the engine's process opens a sealed value, so a
  host-provided site gets the value in clear ([host hooks](0008-host-hooks.md)).

### Serving

- The Go router holds the routes above, written from the Apache
  `openapi.yaml` ([LIC-1](../semantics/licensing.md#lic-1)), with Windmill's
  status codes and `text/plain` error bodies.
- **Transports and attribution.** Every platform call must be attributed to
  the invocation that made it, and never inferred from the token: the stock
  client captures `WM_TOKEN` and its base URL at import, so in a warm process
  every invocation of a script sends the token of that script's first
  invocation, since each script bundle has its own copy of the client ([WA-1](../semantics/client-api.md#wa-1),
  [WA-31](../semantics/client-api.md#wa-31)). Two transports exist:
  1. **Intercepted `fetch`** (warm and host-mode Bun processes): the harness
     turns each `fetch` to the facade origin into a `platform` frame carrying
     the invocation id from its `AsyncLocalStorage` context
     ([harness protocol](0004-harness-protocol.md)). The token in the request is ignored.
     This is the only supported transport in those processes: the facade
     origin is a loopback port the engine binds without listening on, so any
     other transport is refused by the kernel, unrecorded (a restriction of
     the [equivalence contract](0001-equivalence-contract.md#5a-restrictions)).
  2. **Loopback TCP** (`fresh` processes, and later harnesses that run one
     invocation per process): the port listens, and each process gets a
     token that names its one invocation. A call whose token is unknown, or
     names an invocation that has ended, answers `401` with
     `Not authorized: unknown windmill-lite invocation token` and is recorded
     against the process; it is never guessed onto another invocation.

  `BASE_URL` (and, for languages other than Bun, `BASE_INTERNAL_URL`) point at
  the facade origin before any module loads ([WA-2](../semantics/client-api.md#wa-2)).
  Test: two runs at once, in one warm process, with the
  client first imported during the first run's module load: every call of
  each run is attributed to its own invocation on the intercepted path; a
  `node:http` call from a warm step gets "connection refused" and reaches no
  provider (nothing records it: the refusal carries no invocation); in `fresh`
  mode each process's TCP calls carry its own token; a call replaying an
  ended invocation's token gets the `401`.
- **Calls made by host plugins** outside any step (an interceptor that calls
  step code, [host hooks](0008-host-hooks.md) H1) are attributed to the site being
  intercepted: they see the environment that site's invocation would have
  had, and the 501 policy below applies to that site's outcome.
- **Providers written in TypeScript** (a Bun host's per-run providers, H13)
  stay behind the Go router: the router does interpolation, errors and the
  501 policy, and asks the TypeScript provider only for the raw value over a
  `host_call`. A provider-backed call therefore costs two round trips inside
  one process, about 25–35 µs at the measured 12.5–18 µs each
  ([where semantics live](0003-semantics-placement.md)).
  - A provider can mark answers cacheable for the run to avoid repeating
    them; cacheable answers are not built yet.
  - The bridge is the host plugin's `resource(path)` and `variable(path)`,
    asked after the run's own providers and before the runtime's, for the
    job API and for argument resolution alike.
  - A TypeScript provider that throws or times out latches the run, as any
    plugin hook does ([host hooks](0008-host-hooks.md) H5). The router still
    answers the call with its error, but neither the step that tolerates
    that answer nor `continue_on_error` keeps the run going.
  - Answering `platform` frames directly in TypeScript was rejected because
    it would copy the router's rules into a second language.
- **Unsupported routes** answer `501` with `text/plain` body
  `Not implemented in windmill-lite: <METHOD> <route template>`, never `404`,
  because stock clients read a 404 as "absent"
  ([WA-41](../semantics/client-api.md#wa-41)). Each 501 is recorded against
  the invocation. In the default `fail` mode, a step that settles with any
  recorded 501 fails with
  `{"error":{"name":"UnsupportedPlatformCall","message":"Not implemented in windmill-lite: …","calls":[…]}}`;
  in `observe` mode the stock behaviour stands, for oracle comparison (policy
  case WA-41/b).

### Job identity

Every invocation gets the variables of [WR-22](../semantics/languages.md#wr-22)
and [WA-4](../semantics/client-api.md#wa-4):

- `WM_JOB_ID`: a fresh UUID per invocation, as a job id would be;
- `WM_FLOW_JOB_ID`: the id of the flow scope the step runs in. Inline bodies
  that Windmill runs as child flows get child flow ids; collapsed loop
  iterations do not ([FX-5](../semantics/execution.md#fx-5),
  [FX-46](../semantics/execution.md#fx-46));
- `WM_ROOT_FLOW_JOB_ID` follows the innermost-scope rule and
  `WM_ROOT_JOB_ID` the effective root ([WA-V1](../semantics/client-api.md#wa-v1),
  [WA-6](../semantics/client-api.md#wa-6));
- `WM_FLOW_STEP_ID`, `WM_FLOW_PATH`, `WM_JOB_PATH` and `WM_STATE_PATH` follow
  the path rules of [WA-5](../semantics/client-api.md#wa-5) and
  [WA-6](../semantics/client-api.md#wa-6);
- `WM_WORKSPACE`, `WM_EMAIL`, `WM_USERNAME` and `WM_PERMISSIONED_AS` are host
  configuration, and so are `WM_BASE_URL` and `WM_WORKER_GROUP`
  (`default`, the reference configuration's group; windmill-lite has no
  worker groups);
- the remaining variables of WA-4 are set as a job windmill-lite runs has
  them: `WM_SCHEDULED_FOR` is the job's launch, after any
  sleep or retry delay, since every job is scheduled to start at once;
  `WM_OBJECT_PATH` follows WA-4's formula for a manually started job;
  `WM_ROOT_WORKSPACE` is the workspace, since windmill-lite has no forks,
  and `WM_END_USER_EMAIL` the run's user's address, `WM_EMAIL`, as for a
  run its user started through the run API (retarget to Windmill 1.862.0);
  `WM_SCHEDULE_PATH`, `WM_TESTED_RUNNABLE` and `WM_RUNNABLE_ID` are empty,
  since no schedule or CI test starts a job and windmill-lite keeps no
  script hashes ([WA-V10](../semantics/client-api.md#wa-v10)).

**The identity's settings.** A runtime option (`wl`'s `Options.Identity`)
sets this host configuration once, for the job environment, the facade's
check of a call's workspace and the resource store's key alike. A field it
leaves empty takes windmill-lite's placeholder, such as `WM_WORKSPACE`
`windmill-lite`.

Host mode takes it as the host SDK's connect option `identity`, which
reaches the engine as `windmill-lite host -identity`. The go-plugin
adapter's configuration is to carry the option (planned).

**A workspace per run.** A run may set the identity for itself
(`RunOptions.Identity`, or a host-mode `run` frame's `identity`): each field
it sets replaces the runtime's for the run's jobs and the runs that they
dispatch. One runtime can so hold runs of several workspaces, each job in
its run's workspace.

- **A job's workspace** is its run's effective one: the configured
  workspace, or `windmill-lite` when neither the runtime nor the run sets
  one. A run that names `windmill-lite` and one that names none share a
  workspace, as they share a job environment and a resource store key.
- **The lookups**: the job reads, the result polls, the root lookup with
  its record of ended jobs, flow user state, a read's `job_id` and the
  dispatcher's results. Each finds a job only through a path in its own
  workspace, from a run of that workspace, and answers for a job of
  another as for an unknown id, as Windmill does
  ([WA-V2](../semantics/client-api.md#wa-v2)). Each checks the workspace
  on the registry entry that it answers from.
- **The services it needs.** The guarantees of this section hold with the
  services' own job registry and flow user state store, which `wl` always
  uses, shared between engines or not. A run in a workspace other than the
  engine's configured one, which only a run's own identity asks for, needs
  them: with a host's registry or store the engine refuses it.
- **A host's registry or store** keeps the base's behaviour. An engine
  configured with one workspace (`Config.Workspace`) over a host's
  registry or store runs as it always did. Engines that share a registry
  through a host's `Jobs` get no isolation from this section.
- **One workspace per id.** A run whose id a job of another workspace
  holds, live or among the ended jobs kept, is refused before it starts.
  A step whose hook sets a `WM_JOB_ID` held so registers under its own
  invocation id, and a `root_job` that names another workspace's ended
  job leaves that job its workspace's. Runs of two workspaces so never
  share a registry entry.
- **Flow user state.** Two workspaces never share a flow job's state: the
  services' own store keeps each map with the registration (`Job.Gen`)
  and workspace that wrote it. A job record lists the state of the
  registration that the read checked, while that registration is a live
  flow job.
- **Reuse within a workspace.** In the services' own store, a run whose
  id is registered again starts with no state. A host's registry or store,
  which serves the engine's configured workspace alone, keeps the state of
  a reused id as before, by the job's id.
- **A hook's `WM_WORKSPACE`** ([H12](0008-host-hooks.md#the-hooks)) is the
  workspace of a run without a configured one, as it always was. A run
  with a configured workspace stays in it: its token is valid there only,
  whatever its environment says.
- **The invocation's identity.** The facade takes each invocation's
  workspace from its run, never from a lookup by its `WM_JOB_ID`, which a
  hook may change and another live invocation may share.
- **A dispatch's links.** A `parent_job` or `root_job` that names a job of
  another workspace is as unknown to the dispatched run: it does not take
  that parent's path, and `flow_user_state()` reads null through that root.
- **Keys.** The resource store keys a run's entries under its workspace,
  and the result cache its results.

`FlowUserState` keys on the effective root; `getRootJobId` answers from the
run registry. A host can override the environment of an invocation
([host hooks](0008-host-hooks.md)), for example to keep `WM_JOB_ID` unset for code
that treats it as "not running in a job".

### Async dispatch

`runScript*Async`/`runFlowAsync` reach the `Dispatcher` with the target, the
arguments, and the optional `parent_job`, `root_job`, `scheduled_in_secs` and
`tag` ([WA-6](../semantics/client-api.md#wa-6), client table). The default
dispatcher runs the target from the bundle as a separate run and returns its
UUID as `text/plain`; `waitJob` and `getResultMaybe` answer for it with
Windmill's shapes ([WA-V2](../semantics/client-api.md#wa-v2)).

The target must be a root of the bundle (the host contract of
[IR, loader and validation](0002-ir-and-loader.md#host-contract-and-mandatory-host-provided-call-sites));
otherwise the dispatching step fails with `UnsupportedPlatformCall`,
`dispatch target not in bundle: <path>`. A dispatched run starts at once:
no default cap delays it, so a parent that waits for it cannot starve it
([harness protocol](0004-harness-protocol.md); a host-set cap is
[D15](0001-equivalence-contract.md#5-named-deviations), a deviation).

**windmill-lite's job ids stay local.** They name no job of any Windmill
instance ([D3](0001-equivalence-contract.md#5-named-deviations)), so
windmill-lite keeps them out of `parent_job` and `root_job` wherever it
handles a request to a real Windmill:

- A dispatcher that routes a step's dispatch to a real Windmill, and the
  steps that hybrid dispatch runs remotely (§Hybrid dispatch to a real
  Windmill), send neither link. A host's own dispatcher that routes there
  drops them too.
- A step's own client call to a real Windmill bypasses the facade. The
  stock clients add `parent_job` from `WM_JOB_ID` whenever it is set and
  `root_job` from `WM_ROOT_FLOW_JOB_ID` when it is not empty, on a script's
  dispatch and on `runFlow`, while `runFlowAsync` sends neither by default
  ([WA-15](../semantics/client-api.md#wa-15)).
- A runtime setting that names such a Windmill's origins is planned. On a
  request from a step, a native step's included, to one of them under
  `/api/w/{workspace}/jobs/run/`, the harness's `fetch` view then removes a
  `parent_job` or `root_job` query parameter whose value is one of the
  invocation's own job ids.
- A request that windmill-lite does not handle keeps them. That is one to
  an origin that the setting does not name, one made outside the `fetch`
  view (by a child process or a socket that the step opens), and, until the
  setting exists, every such call. A host that runs such code removes the
  links itself.

### Hybrid dispatch to a real Windmill

Two modes, both behind interfaces in the core:

1. **Step delegation.** A selector ([host hooks](0008-host-hooks.md)) marks steps
   or by-path subflows to run remotely. The engine submits them with
   `jobs/run/p|f/{path}` and polls `get_result_maybe`; it never uses
   `run_wait_result`, which rewrites responses and cancels the job on
   disconnect ([WA-50](../semantics/client-api.md#wa-50),
   [OR-10](../semantics/oracle.md#or-10)). The recorded arguments are sent;
   the remote worker resolves `$var:`/`$res:` against the real workspace
   ([WA-51](../semantics/client-api.md#wa-51)). A remote failure becomes the
   module's failure with the same error object as a local one; run
   cancellation cancels the remote job. At start, the runtime checks that
   each delegated target's deployed content equals the bundle's and refuses
   to run otherwise.
2. **Proxied platform.** The facade forwards resources, variables, object
   store and state to the real Windmill, while flow user state and the job
   tree stay local (the reference's design-note mode (a); policy case WA-51/b).

Whether shipping a client that dispatches to a user's own Windmill counts as
"wrapping" under the CE terms is open ([Q-OF-7](../semantics/openflow.md#q-of-7)(b)).
The interfaces and selectors are built regardless; the package that talks to
a remote Windmill (`platform/remote`) is merged only after that decision.

## Consequences

- Stock client code runs unchanged; nothing in a user's bundle is aliased
  except the host-provided modules a host lists ([packaging](0010-packaging.md), D12).
- Every platform call is attributed to one step, even when steps run
  concurrently in one process, because attribution never depends on the
  token. The price is a restriction: in warm processes the platform is
  reachable only through `fetch`.
- A step that hits an unsupported route fails loudly by default instead of
  continuing with a wrong value.
- Hosts get one extension point per service and one secret layer, not a
  fork of the facade.
- The router, both transports, attribution, the 501 policy and job identity
  are implemented in
  [internal/platform/facade](../../internal/platform/facade/README.md) and
  [internal/platform](../../internal/platform/README.md), with these
  details:
  - **The warm origin.** Its port is the local end of a loopback
    connection the engine holds both ends of, so no other socket is given
    the port and nothing listens on it. A socket that is only bound would
    not do: macOS drops the SYNs that reach a bound socket that does not
    listen, so a client would wait for its timeout instead of being
    refused. On Linux and macOS alike the kernel refuses every connection
    to it.
  - **Fresh processes** are not told the facade in their welcome, so their
    calls go over TCP to the listening origin, attributed by their token,
    `WM_TOKEN`: `wl_` and 128 random bits. "Recorded against the process"
    became recorded as a stray call: the TCP server cannot tell which
    process sent a call whose token names none.
  - **In order.** The harness hands the facade each `platform` frame on
    the connection's reader, in frame order, and the facade decides there
    whether the call is unsupported; the harness never sends a result
    ahead of a large platform call of its invocation
    ([harness protocol](0004-harness-protocol.md)). So the 501 policy counts every call
    whose frame arrived before the step's result, an unawaited one
    included. Over TCP the call and the result travel on different
    connections, so a call a fresh step never awaited may be counted or
    not, as on Windmill, where the process exits under it. The policy also
    waits for the answers of calls still being answered, until the step's
    context ends, so a handler's 501 counts.
  - **Naming a 501.** The route template is the API's, from the vendored
    Apache-2.0 schema, with its workspace parameter written `{ws}`, as the
    reference's cases write it (WA-41/a, WA-V5); a path that no route of
    the API has, and one outside `/api/`, answer 501 too, named by the
    method and the path as sent. Every 501, a handler's included, counts.
  - **The policy's error** replaces any outcome, a thrown error included;
    it carries no `step_id`, and its origin is `facade`. `calls` lists each
    unsupported call as `<METHOD> <route template>`, in order, repeats
    included.
  - **The root lookup** (`getRootJobId`) answers from the job registry
    (below), for a live or an ended job; an id it does not know answers
    `400`, as Windmill answers an id with no job row (below).
  - **An intercepted reply** carries the reason phrase Go's `net/http`
    writes for its status, so the two transports give a client the same
    response (WA-30/a, checked on the stock client by
    `TestTransportsAgree`); a raw `fetch` without the token is attributed
    in warm and refused in fresh, which the stock client never does.
  - **Job identity** sets the core variables of WA-4 and the host's
    `WM_EMAIL`, `WM_USERNAME` and `WM_PERMISSIONED_AS`, with windmill-lite
    placeholders where the host sets none; the `later` variables are not
    set. No schedule triggers a run, so the state path's trigger part is
    always `user` (WA-5).
  - **Failed handlers.** A handler that panics, or answers a status outside
    200 to 599, gets a `502` on both transports, which the record keeps. Its
    call stops counting as in flight however it ended, so the 501 policy
    never waits on a failed call.
  - **`HEAD` and bodiless answers.** The answer to `HEAD`, and one with a
    null-body status (204, 205, 304), has no body on either transport. The
    API has no `HEAD` operation, so a `HEAD` request answers 501; whether
    Windmill answers `HEAD` on its `GET` routes is not in the reference.
  - **Shared tokens.** A token that two registrations carried names none
    until both have ended.
  - **Records and host mode.** The facade records each call (`Record`), and
    the run trace carries the calls of the run's steps. Host mode has the
    facade too: each host-mode invocation carries its origin as `BASE_URL`
    ([harness protocol](0004-harness-protocol.md), host mode item 7). An
    authenticated route whose path names another workspace than the
    caller's answers `401`; a `jobs_u` lookup there answers as for an
    unknown job (the workspace of a token, below).
- The resource, variable and secret services, their local defaults, the
  host secret layer and the interpolation walker
  ([internal/platform](../../internal/platform/README.md)), their routes in
  the facade, and the engine's reads of them
  ([internal/engine](../../internal/engine/README.md)) work as follows:
  - **One set of services.** `platform.Services` holds `Resources`,
    `Variables`, `Secrets` and the `ValueResolver` chain; the facade and
    the engine take the same one, and a nil field takes its default. The
    local default is `platform.Store`, filled from a bundle's `data/`
    (`platform.LoadData`); a host's overrides come first in a
    `ResourceChain` or `VariableChain`. A run's own services (H13) come in
    `wl.RunOptions.Platform`.
  - **Secrets** are never in a bundle; a host passes them, as plaintext in
    a store of its own or through `Secrets`. `LocalSecrets` reveals a
    secret held as plaintext and fails ciphertext under the workspace key,
    and an `$encrypted:` value, with `secret_unavailable`. The services
    answer that as Windmill answers a value it cannot decrypt, since
    Windmill 1.829.0 with one text whatever the cause (retarget to Windmill
    1.862.0): a read answers the 500 `Internal: Error decrypting variable
    <p>: Internal: Could not decrypt value. The value may have been
    encrypted with a different key.`, which argument resolution quotes as
    its cause, and an `$encrypted:` argument fails with `Internal: Could
    not decrypt value. …`, without Windmill's locations (D14). Before,
    Windmill's texts named the cause, which windmill-lite could not know,
    and the services answered `Internal: secret_unavailable: …` instead.
  - **The host secret layer** runs on each resource value once its
    interpolation is done, a nested one's included, and on each secret's
    plaintext; a raw read is not passed through it.
  - **Written out again.** What the walker parsed is written out as
    Windmill's backend JSON library writes it (FX-42 rule 2): key order
    kept, integers exact within 64 bits, every other number a double in
    the library's notation, minimal escapes; a resource is read in jsonb's
    stored form first, as the API reads its column. Parse errors are in the
    library's words, with its line and column (FX-51/c).
  - **A read with a job id** (`job_id`, contract (b)) resolves `$WM_*` from
    the stored fields of a live invocation's job (`platform.APIEnv`: its
    stored root, `""` for a step directly in a top-level flow). The job is
    looked up only for a `$WM_*` string, and a job the facade does not know
    of answers Windmill's 404 (a read's `job_id`, below). The engine passes
    a step's stored fields directly.
  - **Argument resolution** happens at the job's pickup, after its job
    identity, its script's `envs` and H12, before the step runs; a failure
    fails the job with the worker's execution error. `$<NAME>` reads the
    job's reserved variables, its job identity, which neither the
    script's `envs` nor H12 change.
  - **`flow_env`** is resolved on every transition, into the frame that
    starts the module, so the iterations of a parallel loop share no
    state; reading, a child flow's `$WM_*` strings are the top-level flow
    job's. **`resource()`** in an expression reads as the stock client
    does, without a job id, since the reference does not say which
    contract Windmill's helper uses.
  - **The arguments file.** FX-42/h observes the resolver's spelling
    through `args.json` (WR-5), so the harness writes it into the step's
    working directory before each invocation, the latest invocation's in a
    warm process (D10), without the worker's internal keys `_MODULES` and
    `_TEMP_SCRIPT_REFS`; the script's parameters bind from the same text.
    A job's resolved arguments are its trace's O3 however the job then
    ends.
  - **Writes.** windmill-lite serves `updateVariable`, except with
    `already_encrypted=true`, a rename or a read-only variable service, and
    the resource store serves the writes of state, and so `setState`
    (below). `createVariable` and `createResource` of a type other than
    `state` answer 501. A secret's plaintext is not registered for log
    masking ([Q-WA-9](../semantics/client-api.md#q-wa-9), `later`).

- The job registry, flow user state, the object store and the dispatcher
  ([internal/platform](../../internal/platform/README.md)), their routes in
  the facade
  ([internal/platform/facade](../../internal/platform/facade/README.md)),
  and the default dispatcher and each run's registration in the engine
  ([internal/engine](../../internal/engine/README.md)) work as follows:
  - **One registry.** `platform.Services` gained `Jobs`, `UserState`,
    `Objects` and `Dispatcher`; a nil `Jobs` is one `platform.Registry`
    per `Services`, and a nil `UserState` one `platform.UserStates` over
    whichever `Jobs` the `Services` hold, a host's included, each made
    when first needed, so the facade and the engine that share a
    `*Services` share them. The engine
    registers each flow run, its own root; the facade registers each
    invocation as a script job, and the flow scopes its variables name
    become known with it.
    - A job stays live while its root lives.
    - Once the root and every job of its tree have ended, the registry
      keeps each job as an ended job: its id, kind, root and path, without
      a script job's variables. It keeps the latest 4096, as Windmill keeps
      a completed job's row until its job retention removes it
      ([WA-19](../semantics/client-api.md#wa-19)).
    - The root lookup answers for a live or an ended job; a read's
      `job_id` needs a live job (below).
  - **Flow user state** is a map per flow job, which the stock helpers
    address by the effective root. A read gives `null` for a missing key, a
    job that is not a live flow job and a run that has ended. A write
    anywhere but a live flow job answers `404` `Not found: Flow job not
    found` ([WA-14](../semantics/client-api.md#wa-14)).
  - **After the run.** The map is discarded with its run, so a write by id
    after the run is refused with that 404, as Windmill refuses it once the
    root's completion is committed ([Q-WA-10](../semantics/client-api.md#q-wa-10), [WA-14/c](../semantics/conformance.md#wa-14)), and a read gives
    `null`, as Windmill's does.
  - **Values.** A value comes back as the oracle recorded it
    (`conformance/flows/client-api/user-state`, run `order`): in jsonb's key
    order, with numbers as Windmill's JSON library writes them (`1e3` as
    `1000.0`). The engine's `flow_user_state()` reads the same map.
  - **Bounds.** The default forgets a run's maps when the run's registration
    through `Services.JobRegistry` ends, and drops the maps of jobs the
    registry no longer reports live as it grows, so it stays bounded
    whatever `Jobs` service a host gives.
  - **The object store** default, `platform.DirObjects`, is a directory
    that keeps each object as one file named by the SHA-256 of its storage
    (`_default_` when none) and key, so no key names a path outside it:
    one version, its metadata line and its content, renamed into place
    whole, so a reader never pairs one upload's content with another's
    content type and a failed upload leaves the object as it was.
    - It serves explicit keys only: an upload without `file_key` and a call
      with `s3_resource_path` answer 501, named. Deletion, credentials and
      presigning have no handler (`later`). Its error texts are
      windmill-lite's (Q-WA-3).
    - When the host configured no store, an upload and a download answer
      `500` `Internal: windmill-lite: no object store is configured`, as
      Windmill CE answers with no workspace store
      ([WA-16](../semantics/client-api.md#wa-16)/d). So the stock client
      throws `Internal Server Error`.
  - **The default dispatcher is the engine's** (`engine.Dispatcher`): it
    runs the program's roots, so it needs the engine, which imports
    `internal/platform`; the interface and the request types are in
    `internal/platform`. It runs a script root for `jobs/run/p` as a script
    job (`engine.RunScript`) and a flow root for `jobs/run/f` as a run,
    detached from the call and from the dispatching run, at once or after
    `scheduled_in_secs`; `Close` cancels what still runs. `tag` is
    recorded and has no effect; `scheduled_for`, `cache_ttl` and `job_id`,
    which no stock helper sends, answer 501. A hash names no root, since a
    bundle records no script hash, so `jobs/run/h` is always refused.
  - **A dispatched job's identity** follows its links, as the oracle
    recorded for a script that `runScriptByPath` dispatched
    (`conformance/flows/client-api/dispatch`, run `identity`): its parent
    (`WM_FLOW_JOB_ID`) is the `parent_job`, the dispatching job; its
    innermost scope (`WM_ROOT_FLOW_JOB_ID`) the `root_job`; its effective
    root the first of `root_job`, `parent_job` and itself that is set, so
    it shares the dispatching run's flow user state; its flow path the
    dispatching job's runnable path, taken from the job registry when the
    dispatch is accepted, so that a run that starts after its parent has
    ended keeps it, and so its state path is `<that path>/nostep_user`; no
    step id. A dispatched failure's error is the
    wrapper's `{message, name, stack}`, without a step id. A flow
    dispatched with the links (`runFlow`) runs its steps with the
    dispatched flow as their parent and the `root_job` as their innermost
    scope and effective root; dispatched without them (`runFlowAsync`), it
    is its own root and scope, a separate run.
  - **The refusal.** A target that is not a root answers `501` with the
    body `dispatch target not in bundle: <path>`, and the 501 policy's
    error now quotes the first unsupported call's answer, so the step fails
    with `UnsupportedPlatformCall`, `dispatch target not in bundle:
    <path>`, as
    [§5a of the equivalence contract](0001-equivalence-contract.md#5a-restrictions)
    says. A result
    poll whose id is not a UUID answers the web framework's `400` (Request
    extraction, below), so a `waitJob` handed the refusal's body as an id
    fails at once. A UUID that names no job
    answers WA-V2's shapes; a windmill-lite job the dispatcher did not
    start answers the polls with a 501, since the result polls answer
    only for dispatched runs.
  - **Not yet:** deletion, generated keys and presigning in the object
    store are `later`. So are the job routes other than the root lookup,
    the job reads (below), flow user state, dispatch, the result polls of
    dispatched runs and the flow restart. The flow restart answers
    CE's `400`, since restarting a flow is `EE`.

- The result cache ([internal/platform](../../internal/platform/README.md),
  the engine's use of it in [internal/engine](../../internal/engine/README.md))
  works as follows:
  - **The interface is a store.** `platform.ResultCache` has two calls:
    `Get(key, now)`, the value stored under a key while it expires after
    now, and `Put(key, value, expires)`. The engine owns the key, the TTL
    rules of FX-40, what a value holds and when a job stores, so a host's
    store keeps opaque bytes and compares expiries with the time the engine
    gives, its run's clock (H10). `platform.Services` gained `Results`; a
    nil one is one `platform.MemoryResults` per `Services`, made when first
    needed, so every engine that shares a `*Services` shares it (D4's
    process scope), and an engine with no services keeps its own. The
    default keeps a heap of expiries and drops, on every call, each entry
    that expired by the latest time a caller gave, so it holds only live
    entries whatever their number.
  - **A hit is a job.** A leaf's or a script root's hit gets the identity
    it would have run with and is registered in the job registry, known
    while its root lives, as a job that ran is. A flow job's hit goes
    through the host's start and completion checks
    ([host hooks](0008-host-hooks.md), H4, H5), so a
    cached result never turns a canceled run into a successful one.
  - **The key** is a SHA-256 over the bundle's digest, the job's kind, its
    runnable (an inline step's inventory id, a path script's path, a
    flow's path) and its arguments in sorted key order, each as its name
    and stored text. Windmill keys a deployed job by a runnable id, never by
    a path that differs between jobs, which is why the iterations of one
    collapsed loop share an entry (FX-40/d); the bundle's digest stands for
    the deployed version those ids belong to, so an entry never crosses
    bundles in a shared store.
  - **The key's workspace.** The key of a run of another workspace than
    `windmill-lite` (§Job identity) starts with a marker of its own and the
    workspace. Runs of two workspaces so never share an entry, and a run of
    `windmill-lite` keeps the key that every run had before.
  - **A store's failure** is a miss and a run warning
    (`result_cache_unavailable`), never a failed run.
  - **S3 object arguments.** An argument whose whole value is an S3
    object record is hashed with its object's ETag, the SHA-256 of the
    content the object store holds, and its text follows unless the job's
    `cache_ignore_s3_path` is set. With no object store or no such object
    it is hashed as its JSON and the flag has no effect, as in a Windmill
    workspace without object storage
    ([FX-40](../semantics/execution.md#fx-40)).

- The facade ([internal/platform/facade](../../internal/platform/facade/README.md))
  and the services ([internal/platform](../../internal/platform/README.md))
  follow these rules of the reference:
  - **The workspace of a token** ([WA-4](../semantics/client-api.md#wa-4),
    [WA-42](../semantics/client-api.md#wa-42)). A job token is valid only
    for the job's workspace. An authenticated route whose path names
    another workspace answers `401` `Not authorized: Unauthorized` first:
    before the 501 of an operation without a handler, and before any
    rejection of its path or body. Every route of a workspace
    (`/w/{workspace}/…`) is authenticated but those of the unauthenticated
    groups: `jobs_u`, which the reference names, and `apps_u`, the public
    apps' routes, which have no handler and keep their 501.
  - **The caller's workspace** is its run's configured one, and without
    one its `WM_WORKSPACE`; a caller with neither is of every workspace.
    Every token windmill-lite issues names one of its invocations, so no
    caller is anonymous. A token that names no invocation keeps its own
    `401` text (Transports, above).
  - **A token of another workspace.** A runtime with runs of two
    workspaces holds live tokens of both, so a step can send its token to a
    path of the other workspace. An authenticated route answers it `401`.
    A `jobs_u` route finds no job there (the next item), as for
    [WA-V2/d](../semantics/conformance.md#wa-v2)'s second request.
  - **[WA-V2/d](../semantics/conformance.md#wa-v2)'s first request** reads a job of the path's
    workspace with another workspace's token. Windmill reads that caller as
    anonymous, and answers `400` for a job that a user ran. windmill-lite
    has no anonymous callers, which the equivalence contract leaves
    [out of scope](0001-equivalence-contract.md#3-out-of-scope), and
    answers it as an unknown id.
  - **Job lookups in the path's workspace.** A `jobs_u` lookup in another
    workspace than the caller's finds no job. Neither does a lookup of a
    job of a run of another workspace (§Job identity, a workspace per run).
    The root lookup then answers as for an unknown id, and the result polls
    as for a UUID with no job ([WA-V2/d](../semantics/conformance.md#wa-v2)'s second request).
  - **The root lookup of an unknown id** answers as Windmill answers an id
    with no job row ([WA-19](../semantics/client-api.md#wa-19)/b): `400`
    `text/plain` `SqlErr: no rows returned by a query that expected to
    return at least one row`, without the Rust location (D14). That
    covers an id the registry never knew, and an ended job that it has
    forgotten because it keeps only the latest 4096 ended jobs, where
    Windmill keeps a job's row for its job retention, 30 days by default
    (D3).
  - **`getResult`'s not-found** is Windmill's `Not found: Completed Job not
    found at name <id>`, without its location (WA-19/d).
  - **A read's `job_id`** ([WA-12](../semantics/client-api.md#wa-12)/g) is
    looked up only when the walk meets a `$WM_*` string, at each one
    (`platform.JobLookup`), so a value without one reads as if no job id
    had been sent.
    - A `job_id` that is not a UUID, the empty one included, is refused
      with `400` before any read (Request extraction, below).
    - The job must be live and of the path's workspace
      (`Services.ReadJobEnv`): an unknown id, a job that has ended and a
      job of another workspace answer `404` `Not found: Job not found at
      name <id>`, without the location.
    - A live flow job answers `501` (`… with the job_id of a flow job`),
      since the registry keeps the job variables of script jobs only.
  - **Request extraction.** The facade answers as Windmill's web framework
    does when it cannot extract a request's parameters, before the handler
    runs and after the token check
    ([WA-42](../semantics/client-api.md#wa-42)), with `Content-Type:
    text/plain; charset=utf-8`:
    - a path id that is not a UUID: `400` ``Invalid URL: Cannot parse
      `<param>` with value `<id>`: UUID parsing failed: …``
      ([WA-V2](../semantics/client-api.md#wa-v2)/c,
      [WA-14](../semantics/client-api.md#wa-14)/d,
      [WA-19](../semantics/client-api.md#wa-19)/c), the id checked before
      the body;
    - a body sent without a JSON content type: `415`;
    - a body that does not parse: `400` `Failed to parse the request body
      as JSON: …` (WA-14/d);
    - a variable update whose body parses but is not an object: `422`;
    - a `job_id` query that is not a UUID: `400`, with windmill-lite's
      text, since the reference gives only the status
      ([WA-12](../semantics/client-api.md#wa-12)/g).

    A handler's successful text answer (a variable update, `Flow job state
    updated`, a dispatch's `201`) is typed `text/plain; charset=utf-8` too,
    and an error variant's answer bare `text/plain`.

    Not reproduced: Windmill puts the member's path in front of a parse
    error inside a member (`a: ` for `{"a": x}`). Only a raw HTTP request
    meets this.
  - **Not reproduced.**
    - windmill-lite never decrypts Windmill ciphertext (Secrets, above):
      a secret held as ciphertext and an `$encrypted:` value that a host
      does not supply fail with CE's one decryption text
      ([WA-11](../semantics/client-api.md#wa-11)/c,
      [JE-42](../semantics/expressions.md#je-42)/j), even where Windmill,
      holding the workspace key, would decrypt them. `createVariable`
      still has no handler, so no secret is created with
      `already_encrypted=true`, and
      [WA-11/d](../semantics/conformance.md#wa-11)'s shape check waits until
      `createVariable` is served.
    - windmill-lite has no instance database and no instance super
      admins. Since Windmill refuses a job's token on a
      `CUSTOM_INSTANCE_DB/` path of the interpolated read (WA-12/h,
      retarget to Windmill 1.862.0), and every caller windmill-lite serves
      is a job, such a read answers that 401 before anything else; the
      raw read, and a nested `$res:`, look the path up as any other.

- **The job reads** ([WA-19](../semantics/client-api.md#wa-19)). `GET
  /w/{ws}/jobs_u/get/{id}` and `jobs_u/get_args/{id}` answer for every job
  of a live root run, running or completed
  ([internal/engine](../../internal/engine/README.md),
  [internal/platform](../../internal/platform/README.md)). These are the
  run's flow job, its steps, subflows and their steps, the child flows of
  loops and branches, a collapsed iteration's job, a skipped or mocked
  module's identity job, a retried step's failed attempts, each attempt
  of a retried subflow or container, a subflow that failed before any of
  its modules ran, a parallel loop's children its parallelism still
  holds, and a failure module's job.
  - **References, not copies.** The job registry keeps, with each job of a
    live tree, a reference to the run that holds it (`platform.Job.Run` and
    `View`), and drops it with the tree. The run indexes the jobs it starts
    by id, with where each one's state lives: a flow job's frame, a leaf's
    record in the trace. It renders a record only when a step asks, so a
    run that no step asks about pays for the index alone.
  - **Each flow job shows its own run.** A frame holds, per module, the
    record of its current attempt and the child flows of that attempt,
    and the records of its own attempts. A retry runs at the same sites as
    the attempt it replaces, so a flow job of a superseded attempt still
    reads its own modules and jobs, not the retry's.
  - **The record** has the fields the oracle recorded that windmill-lite
    reproduces exactly: `type`, `id`, `parent_job`, `root_job`, `running`,
    `flow_step_id`, `canceled`, `success`, `result`, `args`,
    `script_path`, `is_flow_step`, `language`, the workspace and user
    fields, and a flow job's `flow_status`. WA-19 names each field left
    out with its reason; `no_logs` and `no_code` change nothing. Values
    are jsonb's text output, as Windmill writes them. An argument map or a
    result whose stored column takes 90000 bytes or more is replaced, as
    Windmill replaces it: `jsonval.ToastedSize` computes `pg_column_size`
    from the value, jsonb's binary form compressed by a port of
    PostgreSQL's pglz (PostgreSQL License), and CI checks both against
    PostgreSQL.
  - **A job pushed for later**, after the sleep of the step before it or a
    retry's delay, gets its id when it is pushed, not when it starts, so
    that its flow's status names it while it waits and it is read: a
    step's job (`jobScheduled`), the identity job of a skipped, mocked or
    identity module (a container's included), a job that its input
    transform or its entrypoint fails at its pickup, a flow step's
    subflow (`subflowScheduled`), one whose input transform failed
    included, and a container's child flow at its pickup. Whatever
    completes the job keeps that id (`run.takeScheduled`): its
    invocation, the result cache's answer at its pickup, or a completion
    with no invocation.
  - **An identity job**, and a script job that failed before its
    invocation, get an id when they run (`StepRecord.NoInvocationJob`),
    or keep the one they were pushed with, since Windmill's flow status
    names one for them; the run registers it with its tree.
  - **Not served:** a job of a run that has ended answers 501 with the
    reason (D22), since the engine releases a run when it ends. So do the
    states no recording shows: a job that windmill-lite canceled, latched
    or halted, a flow with a preprocessor module, and a flow job while a
    host-provided step of it waits for the sleep of the step before it,
    since a provided site has no Windmill job to record. The 501 policy
    then fails the step that asked.

- A host may put a read cache in front of a run's resource and variable
  providers (`wl.Options.ReadCache`, off by default). Each run then reads
  each path once and answers its later reads from that read, which is
  deviation [D21](0001-equivalence-contract.md#5-named-deviations). The cache holds
  the providers' values as stored, below the interpolation walker, so
  `$WM_*` strings still resolve for each job.
- The resource store served the writes of state
  ([WA-13](../semantics/client-api.md#wa-13)), and so the stock client's
  `setState`. `platform.ResourceStore`, re-exported as `wl.ResourceStore`,
  keeps the entries that steps write, keyed by workspace and path; its doc
  comment and the suite `wl/storetest` hold its contract. The default
  store keeps them in memory, one per runtime
  ([D24](0001-equivalence-contract.md#5-named-deviations), the memory store), and other stores implement
  the interface.
- `wl/redisstore` keeps them in Redis, shared by every runtime on the same
  Redis and key prefix: one hash per entry, and each operation one command
  or one Lua script on that one key, so each is atomic. It adds no
  deviation beyond durability, which Redis's configuration sets.
- `UpdateValue` and `Delete` take the type they may change and act only
  on an entry of it, in one atomic step, since a host may share the store
  and write it directly.
- `createResource` of type `state`, and `updateResourceValue` and
  `deleteResource` of a stored state, answer as the recordings of
  `conformance/flows/client-api/state` do. Another type and the options
  the stock client never sends answer 501, and an update or a delete of a
  provided resource answers the restriction's 501
  ([§5a of the equivalence contract](0001-equivalence-contract.md#5a-restrictions)).
- Every read, the engine's `$res:` arguments and `resource()` included,
  finds the host's and the bundle's resources first, then the store,
  never cached. A failing store answers 500 `Internal: windmill-lite: the
  resource store failed: <err>`, which the run's trace keeps as a
  warning, so `getState` throws rather than reading a missing state.

## Spec IDs relied on

WA-1 to WA-6, WA-8, WA-9, WA-11, WA-12, WA-14 to WA-16, WA-19, WA-30,
WA-31, WA-40 to WA-42, WA-50, WA-51, WA-V1 to WA-V5, FX-5, FX-40, FX-46,
WR-22, JE-20, JE-42, JE-43, SY-11, OR-5, OR-10, LIC-1, Q-OF-7, Q-WA-3,
Q-WA-10, and the client table of client-api.md.

[FX-39](../semantics/execution.md#fx-39), for flows that suspend until
notified.

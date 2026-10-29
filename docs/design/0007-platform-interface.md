# 0007 · Platform interface, the `wmill` shim, and hybrid dispatch

**Status:** proposed · **Topic:** the services a step can call (resources,
variables, secrets, state, object store, async dispatch, resume), how stock
client code reaches them, job identity, and delegating chosen steps to a
real Windmill.

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
them, per runtime or per run ([0008](0008-host-hooks.md)).

| Service | Routes it serves | Default | Scope |
|---|---|---|---|
| `Resources` | `resources/get_value_interpolated`, `get_value`, `exists`, `create`, `update_value` | the bundle's resource files plus host overrides | core (read), later (write) |
| `Variables` | `variables/get_value`, `exists`, `create`, `update` | the bundle's variable files; secrets through `Secrets` | core (read), later (write) |
| `Secrets` | used by the two above | plain-secret folders work; ciphertext without a host resolver fails with `secret_unavailable` | core |
| `State` | the resource routes at the state path, type `state` | in memory, per bundle runtime | later |
| `FlowUserState` | `jobs_u/get_root_job_id`, `jobs/flow/user_states/{root}/{key}` | a map per effective root, discarded when the root run ends; `null` for a missing key | core |
| `ObjectStore` | `job_helpers/download_s3_file`, `upload_s3_file`, `delete_s3_file` | a local directory keyed by (storage, key) | core (explicit keys), later (the rest) |
| `Dispatcher` | `jobs/run/p`, `run/h`, `run/f`, `jobs_u/completed/get_result_maybe`, `get_result` | runs the target from the bundle as a detached run of the same runtime | core (interface) |
| `Jobs` | `jobs_u/get`, `jobs_u/get_args/{id}`, job tree lookups | answers from the run registry for windmill-lite ids ([0001](0001-equivalence-contract.md) D3) | later; core for root lookup |
| `ResultCache` | none (engine-internal, `cache_ttl`) | per process (D4); the key and TTL rules of [FX-40](../semantics/execution.md#fx-40) | core (N4); later (S3-aware argument hashing) |
| `Resume` | `jobs/resume_urls` | none: a step that asks gets `UnsupportedPlatformCall`; a host provider may return `{approvalPage, resume, cancel}` | core (call shape, provider interface); out (resuming, since `suspend` is rejected) |

### One interpolation walker, three modes

`platform` implements resolution once ([WA-12](../semantics/client-api.md#wa-12)):

1. **raw read** (`get_value`): no interpolation;
2. **client interpolated read** (`get_value_interpolated` without a job id):
   `$WM_*` stays literal;
3. **worker argument resolution** (before a step runs,
   [0005](0005-json-step-boundaries.md)): `$var:`, `$jsonvar:`, `$res:`,
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
     ([0004](0004-harness-protocol.md)). The token in the request is ignored.
     This is the only supported transport in those processes: the facade
     origin is a loopback port the engine binds without listening on, so any
     other transport is refused by the kernel, unrecorded (restriction,
     [0001](0001-equivalence-contract.md) §5a).
  2. **Loopback TCP** (`fresh` processes, and later harnesses that run one
     invocation per process): the port listens, and each process gets a
     token that names its one invocation. A call whose token is unknown, or
     names an invocation that has ended, answers `401` with
     `Not authorized: unknown windmill-lite invocation token` and is recorded
     against the process; it is never guessed onto another invocation.

  `BASE_URL` (and, for languages other than Bun, `BASE_INTERNAL_URL`) point at
  the facade origin before any module loads ([WA-2](../semantics/client-api.md#wa-2)).
  Test (milestone PF1): two runs at once, in one warm process, with the
  client first imported during the first run's module load: every call of
  each run is attributed to its own invocation on the intercepted path; a
  `node:http` call from a warm step gets "connection refused" and reaches no
  provider (nothing records it: the refusal carries no invocation); in `fresh`
  mode each process's TCP calls carry its own token; a call replaying an
  ended invocation's token gets the `401`.
- **Calls made by host plugins** outside any step (an interceptor that calls
  step code, [0008](0008-host-hooks.md) H1) are attributed to the site being
  intercepted: they see the environment that site's invocation would have
  had, and the 501 policy below applies to that site's outcome.
- **Providers written in TypeScript** (a Bun host's per-run providers, H13)
  stay behind the Go router: the router does interpolation, errors and the
  501 policy, and asks the TypeScript provider only for the raw value over a
  `host_call`. A provider-backed call therefore costs two round trips inside
  one process, about 25–35 µs at the measured 12.5–18 µs each
  ([0003](0003-semantics-placement.md)); a provider can mark answers
  cacheable for the run to avoid repeating them. Milestone F2c built this
  bridge as the plugin's `resource(path)` and `variable(path)`, asked
  after the run's own providers and before the runtime's, for the job API
  and for argument resolution alike; cacheable answers are not built yet. Answering `platform` frames
  directly in TypeScript was rejected because it would copy the router's
  rules into a second language.
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
  them (milestone FU7): `WM_SCHEDULED_FOR` is the job's launch, after any
  sleep or retry delay, since every job is scheduled to start at once;
  `WM_OBJECT_PATH` follows WA-4's formula for a manually started job;
  `WM_SCHEDULE_PATH`, `WM_END_USER_EMAIL`, `WM_TESTED_RUNNABLE` and
  `WM_RUNNABLE_ID` are empty, since no schedule, app or CI test starts a
  job and windmill-lite keeps no script hashes
  ([WA-V10](../semantics/client-api.md#wa-v10)).

`FlowUserState` keys on the effective root; `getRootJobId` answers from the
run registry. A host can override the environment of an invocation
([0008](0008-host-hooks.md)), for example to keep `WM_JOB_ID` unset for code
that treats it as "not running in a job".

### Async dispatch

`runScript*Async`/`runFlowAsync` reach the `Dispatcher` with the target, the
arguments, and the optional `parent_job`, `root_job`, `scheduled_in_secs` and
`tag` ([WA-6](../semantics/client-api.md#wa-6), client table). The default
dispatcher runs the target from the bundle as a separate run and returns its
UUID as `text/plain`; `waitJob` and `getResultMaybe` answer for it with
Windmill's shapes ([WA-V2](../semantics/client-api.md#wa-v2)). The target
must be a root of the bundle ([0002](0002-ir-and-loader.md) host contract);
otherwise the dispatching step fails with `UnsupportedPlatformCall`,
`dispatch target not in bundle: <path>`. A dispatched run starts at once:
no default cap delays it, so a parent that waits for it cannot starve it
([0004](0004-harness-protocol.md); a host-set cap is D15). A host
dispatcher may route elsewhere, for example to a real Windmill, and decides
whether parent and root links naming windmill-lite ids are sent on. Code
that bypasses the facade and posts to a real Windmill itself is outside the
dispatcher's reach; a host that runs such code strips the links in its own
`fetch` wrapper.

### Hybrid dispatch to a real Windmill

Two modes, both behind interfaces in the core:

1. **Step delegation.** A selector ([0008](0008-host-hooks.md)) marks steps
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
  except the host-provided modules a host lists ([0010](0010-packaging.md), D12).
- Every platform call is attributed to one step, even when steps run
  concurrently in one process, because attribution never depends on the
  token. The price is a restriction: in warm processes the platform is
  reachable only through `fetch`.
- A step that hits an unsupported route fails loudly by default instead of
  continuing with a wrong value.
- Hosts get one extension point per service and one secret layer, not a
  fork of the facade.
- Milestone PF1 implemented the router, both transports, attribution, the
  501 policy and job identity
  ([internal/platform/facade](../../internal/platform/facade/README.md),
  [internal/platform](../../internal/platform/README.md)), and fixed what
  this record left open:
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
    ([0004](0004-harness-protocol.md)). So the 501 policy counts every call
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
  - **The root lookup** (`getRootJobId`) answers from the identities of the
    invocations registered at the time, the job of any of them or a flow
    scope or root it runs in; a job it does not know of answers 501 until
    the job registry of milestone PF3.
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
  - **Fix round 1 of PF1's review.** A handler that panics, or answers a
    status outside 200 to 599, gets a `502` on both transports, which the
    record keeps, and its call stops counting as in flight however it
    ended, so the 501 policy never waits on a failed call. The answer to
    `HEAD`, and one with a null-body status (204, 205, 304), has no body on
    either transport. A token two registrations carried names none until
    both have ended. The API has no `HEAD` operation, so a `HEAD` request
    answers 501; whether Windmill answers `HEAD` on its `GET` routes is not
    in the reference.
  - **Not yet:** recorded calls are the facade's (`Record`), not part of the
    run trace; host mode stays on the walking skeleton without a facade
    until F3a, whose SDK must set `BASE_URL` from the welcome before any
    step module loads; and a call's `{workspace}` is not checked against
    the caller's `WM_WORKSPACE`.
- Milestone PF2 implemented the resource, variable and secret services,
  their local defaults, the host secret layer and the interpolation walker
  ([internal/platform](../../internal/platform/README.md)), their routes in
  the facade, and the engine's reads of them
  ([internal/engine](../../internal/engine/README.md)), and fixed what this
  record left open:
  - **One set of services.** `platform.Services` holds `Resources`,
    `Variables`, `Secrets` and the `ValueResolver` chain; the facade and
    the engine take the same one, and a nil field takes its default. The
    local default is `platform.Store`, filled from a bundle's `data/`
    (`platform.LoadData`); a host's overrides come first in a
    `ResourceChain` or `VariableChain`. Per-run services (H13) are F2a's.
  - **Secrets** are never in a bundle; a host passes them, as plaintext in
    a store of its own or through `Secrets`. `LocalSecrets` reveals a
    secret held as plaintext and fails ciphertext under the workspace key,
    and an `$encrypted:` value, with `secret_unavailable`, which a read
    answers as the 500 `Internal: secret_unavailable: …` and argument
    resolution quotes as its cause.
  - **The host secret layer** runs on each resource value once its
    interpolation is done, a nested one's included, and on each secret's
    plaintext; a raw read is not passed through it.
  - **Written out again.** What the walker parsed is written out as
    Windmill's backend JSON library writes it (FX-42 rule 2): key order
    kept, integers exact within 64 bits, every other number a double in
    the library's notation, minimal escapes; a resource is read in jsonb's
    stored form first, as the API reads its column. Parse errors are in the
    library's words, with its line and column (FX-51/c).
  - **A read with a job id** (`job_id`, contract (b)) resolves `$WM_*`
    from the stored fields of a live invocation's job
    (`platform.APIEnv`: its stored root, `""` for a step directly in a
    top-level flow); a job the facade does not know of answers 501, as the
    root lookup does, until PF3's job registry. The engine passes a step's
    stored fields directly.
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
  - **Not yet:** the writes (`createResource`, `updateResourceValue`,
    `createVariable`, `updateVariable`) and so `setState` are `later` and
    answer 501; a secret's plaintext is not registered for log masking
    ([Q-WA-9](../semantics/client-api.md#q-wa-9), `later`); per-run
    providers (H13) are F2a's.

- Milestone PF3 implemented the job registry, flow user state, the object
  store and the dispatcher ([internal/platform](../../internal/platform/README.md)),
  their routes in the facade
  ([internal/platform/facade](../../internal/platform/facade/README.md)),
  and the default dispatcher and each run's registration in the engine
  ([internal/engine](../../internal/engine/README.md)), and fixed what this
  record left open:
  - **One registry.** `platform.Services` gained `Jobs`, `UserState`,
    `Objects` and `Dispatcher`; a nil `Jobs` is one `platform.Registry`
    per `Services`, and a nil `UserState` one `platform.UserStates` over
    whichever `Jobs` the `Services` hold, a host's included, each made
    when first needed, so the facade and the engine that share a
    `*Services` share them. The engine
    registers each flow run, its own root; the facade registers each
    invocation as a script job, and the flow scopes its variables name
    become known with it. A job stays known while its root lives; once the
    root and every job of its tree have ended, the tree is forgotten. The
    root lookup and a read's `job_id` answer from it; an id it does not
    know still answers 501, since what Windmill answers for one is not in
    the reference.
  - **Flow user state** is a map per flow job, which the stock helpers
    address by the effective root. A read gives `null` for a missing key,
    a job that is not a live flow job and a run that has ended; a write
    anywhere but a live flow job answers `404` `Not found: Flow job not
    found` (WA-14). The map is discarded with its run, so a write by id
    after the run is refused with that 404 where whether Windmill accepts
    it is Q-WA-10. A value comes back as the oracle recorded it
    (`conformance/flows/client-api/user-state`, run `order`): in jsonb's
    key order, with numbers as Windmill's JSON library writes them (`1e3`
    as `1000.0`). The engine's `flow_user_state()` reads the same map. The
    default forgets a run's maps when the run's registration through
    `Services.JobRegistry` ends, and drops the maps of jobs the registry no
    longer reports live as it grows, so it stays bounded whatever `Jobs`
    service a host gives.
  - **The object store** default, `platform.DirObjects`, is a directory
    that keeps each object as one file named by the SHA-256 of its storage
    (`_default_` when none) and key, so no key names a path outside it:
    one version, its metadata line and its content, renamed into place
    whole, so a reader never pairs one upload's content with another's
    content type and a failed upload leaves the object as it was. It
    serves explicit keys only: an upload without
    `file_key`, a call with `s3_resource_path`, and every call when the
    host configured no store answer 501, named; deletion, credentials and
    presigning have no handler (`later`). Its error texts are
    windmill-lite's (Q-WA-3).
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
    <path>`, as §5a of [0001](0001-equivalence-contract.md) says. A result
    poll whose id is not a UUID answers `400`, so that a `waitJob` handed
    the refusal's body as an id fails at once; a UUID that names no job
    answers WA-V2's shapes; a windmill-lite job the dispatcher did not
    start answers the polls with a 501, since job introspection beyond
    dispatched runs (`Jobs`'s `jobs_u/get`, `get_args`) stays `later`.
  - **Not yet:** a host's per-run services (H13) are F2a's; deletion,
    generated keys and presigning in the object store, and job
    introspection beyond the root lookup and dispatched runs, are `later`.

- Milestone N4 implemented the result cache
  ([internal/platform](../../internal/platform/README.md), the engine's
  use of it in [internal/engine](../../internal/engine/README.md)):
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
    through the host's start and completion checks (0008, H4, H5), so a
    cached result never turns a canceled run into a successful one.
  - **The key** is a SHA-256 over the bundle's digest, the job's kind, its
    runnable (an inline step's inventory id, a path script's path, a
    flow's path) and its arguments in sorted key order, each as its name
    and stored text. Windmill keys a deployed job by a runnable id, never by
    a path that differs between jobs, which is why the iterations of one
    collapsed loop share an entry (FX-40/d); the bundle's digest stands for
    the deployed version those ids belong to, so an entry never crosses
    bundles in a shared store.
  - **A store's failure** is a miss and a run warning
    (`result_cache_unavailable`), never a failed run.
  - **Not yet:** S3-aware argument hashing, which includes an object's ETag
    (FX-40), needs an ETag the object store does not keep: an argument
    that names an S3 object is hashed as its JSON, and
    `cache_ignore_s3_path` is refused (`later`).

## Spec IDs relied on

WA-1 to WA-6, WA-8, WA-9, WA-12, WA-14 to WA-16, WA-30, WA-31, WA-40 to
WA-42, WA-50, WA-51, WA-V1 to WA-V5, FX-5, FX-40, FX-46, WR-22, JE-20,
JE-43, SY-11, OR-5, OR-10, LIC-1, Q-OF-7, Q-WA-3, Q-WA-10, and the client
table of client-api.md.

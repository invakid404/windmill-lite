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
| `ResultCache` | none (engine-internal, `cache_ttl`) | per process (D4); the key and TTL rules of [FX-40](../semantics/execution.md#fx-40) | later |
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
`Not found: Resource not found at name <p> (resources.rs:<line>)` for a
missing nested resource (the line is normalised). Worker argument resolution
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
- It listens on loopback TCP for harnesses or code paths that do not use
  `fetch`. `BASE_URL` (and, for languages other than Bun, `BASE_INTERNAL_URL`)
  point at it before any module loads ([WA-2](../semantics/client-api.md#wa-2)).
- The Bun harness intercepts `fetch` calls to that origin and sends them as
  `platform` frames ([0004](0004-harness-protocol.md)), so they skip TCP.
- **Attribution** comes from the invocation, never from the token: the token
  is fixed at import and shared by every step in the process
  ([WA-31](../semantics/client-api.md#wa-31)). Frames carry the invocation
  id; TCP calls carry the invocation's own `WM_TOKEN`, a per-invocation
  opaque value that the facade maps back to the invocation.
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
  configuration.

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
Windmill's shapes ([WA-V2](../semantics/client-api.md#wa-v2)). A host
dispatcher may route elsewhere, for example to a real Windmill, and decides
whether parent and root links naming windmill-lite ids are sent on.

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

- Stock client code runs unchanged; nothing in a user's bundle is aliased.
- Every platform call is attributed to one step, even when steps run
  concurrently in one process.
- A step that hits an unsupported route fails loudly by default instead of
  continuing with a wrong value.
- Hosts get one extension point per service and one secret layer, not a
  fork of the facade.

## Spec IDs relied on

WA-1 to WA-6, WA-8, WA-9, WA-12, WA-30, WA-31, WA-40 to WA-42, WA-50, WA-51,
WA-V1 to WA-V4, FX-5, FX-40, FX-46, WR-22, JE-43, SY-11, OR-10, LIC-1,
Q-OF-7, and the client table of client-api.md.

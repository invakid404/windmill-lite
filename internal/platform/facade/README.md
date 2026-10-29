# internal/platform/facade

**Current state:** milestones PF1 to PF3 of
[ADR 0012](../../../docs/design/0012-layout-and-milestones.md): the platform
facade of [ADR 0007](../../../docs/design/0007-platform-interface.md). This
README covers `internal/platform/facade`. It is the Go router behind the
stock client's platform calls: the routes of Windmill's API, attribution of
every call to the invocation that made it over two transports, the 501
policy for the routes windmill-lite does not serve, and Windmill's
`text/plain` error bodies. It serves the root lookup (PF1), the resource
and variable reads (PF2), and the job registry's lookups, flow user state,
dispatch and its result polls, and the object store (PF3). Job identity
and the services are [internal/platform](../README.md).

## Files

| File | Contents |
|---|---|
| `facade.go` | `Facade`, `Config`, `Policy`, `Session`, `Call`, `Reply`, `Handler`, `Record`: registration, attribution, recording, the 501 decision |
| `routes.go`, `routes.json` | The route table and its matching |
| `transport.go` | The two origins and the harness's handler (`Platform`) |
| `executor.go` | `Executor`: a step executor that registers each invocation and applies the 501 policy |
| `jobs.go` | The root lookup, the job environment a read's `job_id` names, flow user state and the result polls, from the job registry and the dispatcher (PF1, PF3) |
| `providers.go` | The resource and variable reads (PF2) |
| `dispatch.go` | The dispatch routes (PF3) |
| `objects.go` | The object store's job helpers (PF3) |
| `errors.go` | `Error`: an API error as a reply, with the variants of `platform.Error` (WA-42) |

## Routes

`routes.json` lists every operation of Windmill's REST API: its method, its
path template relative to the API base (`OpenAPI.BASE`, `<origin>/api`) and
its operationId. It is generated from the Apache-2.0 `openapi.yaml` that
milestone E2 vendored with its notice in
[internal/oracle/openapi](../../oracle/README.md) (778 operations), and
`TestRouteTable` fails while the two differ
(`go test ./internal/platform/facade -run TestRouteTable -args -update`
rewrites it). Route templates are wire-format literals.

**Matching.** A request path must start with `/api/`. A template's
parameter matches one segment, except its last, which may take the rest of
the path: Windmill's paths (`u/x/script`) travel with their slashes, since
the stock client encodes path parameters with `encodeURI`. Parameters are
unescaped. Among the routes that match, the one with the most literal
segments wins, then the shorter one.

**Handlers** are registered by operationId (`Facade.Handle`); registering an
operation the API does not have panics. A handler gets the `Call`: its
`Caller` (the invocation's id, run and job environment), the matched
`Route`, its parameters, query, headers and body. It answers a `Reply`;
`Error` renders [WA-42](../../../docs/semantics/client-api.md#wa-42)'s
variants with their statuses, `text/plain`, without the Rust location some
of them carry on Windmill (D14). A handler that panics, or answers a
status outside 200 to 599, gets a `502` in its place (`Bad gateway:
windmill-lite: the handler of <route> failed: <panic>`), on both
transports alike; the record keeps that status, and the call stops
counting as in flight however the handler ended, so the 501 policy never
waits on a call that has failed.

The root lookup, `getRootJobId`, `GET /w/{ws}/jobs_u/get_root_job_id/{id}`,
answers the effective root of the job as a JSON string
([WA-14](../../../docs/semantics/client-api.md#wa-14),
[WA-V1](../../../docs/semantics/client-api.md#wa-v1)) from the job registry
([internal/platform](../README.md#the-job-registry-and-flow-user-state),
D3): a job of a run that is still running, its job ended or not, a flow
scope one of its jobs runs in, or the run itself. The facade registers
each invocation there as a script job while it runs (`Register`, its
`WM_JOB_ID`, `WM_ROOT_JOB_ID` and `WM_JOB_PATH`), and the engine each flow
run. A job the registry does not know of (another workspace's, or one of
a run that has ended) answers 501, since what Windmill answers for an id
with no job is not in the reference.

**The resource and variable reads** (PF2) answer from `Config.Platform`
([internal/platform](../README.md#services)), nil meaning none, so every
read is a not-found. They keep the three resource-read contracts of
[WA-40](../../../docs/semantics/client-api.md#wa-40) apart, and answer
`application/json` on success and WA-42's `text/plain` errors:

| Operation | Route | Answer |
|---|---|---|
| `getResourceValueInterpolated` | `GET /w/{ws}/resources/get_value_interpolated/{path}` | the interpolated value (contract (b), [WA-12](../../../docs/semantics/client-api.md#wa-12)); `$WM_*` strings stay literal unless `job_id` names a script job the job registry knows (PF3), whose stored fields they then are (`platform.APIEnv`); a `job_id` the registry does not know answers 501, as the root lookup does; `allow_cache` is ignored |
| `getResourceValue` | `GET /w/{ws}/resources/get_value/{path}` | the stored value, nothing resolved (contract (a)) |
| `existsResource`, `existsVariable` | `GET /w/{ws}/resources/exists/{path}`, `…/variables/exists/{path}` | `true` or `false` |
| `getVariableValue` | `GET /w/{ws}/variables/get_value/{path}` | the value as a JSON string, a secret's plaintext through `Secrets` and the value resolvers ([WA-11](../../../docs/semantics/client-api.md#wa-11)) |

The writes (`createResource`, `updateResourceValue`, `createVariable`,
`updateVariable`) are `later` and answer 501, as does state's write
(`setState`); `getState` reads the state path through
`get_value_interpolated`, so a state never written is a 404, which the
stock client turns into `undefined`
([WA-41](../../../docs/semantics/client-api.md#wa-41)/b).

**Flow user state** ([WA-14](../../../docs/semantics/client-api.md#wa-14),
PF3) answers from `platform.Services`:

| Operation | Route | Answer |
|---|---|---|
| `getFlowUserState` | `GET /w/{ws}/jobs/flow/user_states/{id}/{key}` | the value at key of the flow job, as `application/json`; `null` for a missing key, for a job that is not a live flow job (a script job, an unknown one, a run that has ended) |
| `setFlowUserState` | `POST /w/{ws}/jobs/flow/user_states/{id}/{key}` | the body, any JSON, stored at key, answered `200` `text/plain` `Flow job state updated`; `404` `Not found: Flow job not found` unless the job is a live flow job; `400` for a body that is not JSON jsonb could store |

A value comes back in jsonb's key order with its numbers as Windmill's
backend JSON library writes them, as the oracle recorded
(`conformance/flows/client-api/user-state`, run `order`).

**Dispatch** ([WA-15](../../../docs/semantics/client-api.md#wa-15), PF3)
hands each run to `Services.Dispatcher`
([internal/platform](../README.md#the-dispatcher); the default is
`engine.Dispatcher`):

| Operation | Route | Answer |
|---|---|---|
| `runScriptByPath`, `runFlowByPath` | `POST /w/{ws}/jobs/run/p/{path}`, `…/run/f/{path}` | `201` `text/plain` with the new job's id; the body is the run's arguments, and the query's `parent_job`, `root_job`, `tag` and `scheduled_in_secs` go to the dispatcher with the caller's job variables |
| `runScriptByHash` | `POST /w/{ws}/jobs/run/h/{hash}` | refused: a windmill-lite bundle records no script hash, so no hash names a root |
| `getCompletedJobResultMaybe` | `GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}` | what waitJob polls ([WA-V2](../../../docs/semantics/client-api.md#wa-v2)): a dispatched run's `started`, `success`, `completed` and `result`, with explicit nulls while it runs (`started` only when `get_started=true` asks); for a UUID that names no job, Windmill's answer for an id with no job row |
| `getCompletedJobResult` | `GET /w/{ws}/jobs_u/completed/get_result/{id}` | a dispatched run's result once it has completed; `404` before, and for a UUID that names no job |

- **A target that is not a root of the bundle** (ADR 0001 §5a), and
  every target when there is no dispatcher, answers `501` with the body
  `dispatch target not in bundle: <target>`; the 501 policy quotes it as
  the step's error. The stock client's async helpers take that body for a
  job id (WA-V5/a).
- **A result poll whose id is not a UUID** answers `400` (`Bad request:
  windmill-lite: the job id "<id>" is not a UUID`), as a request whose
  uuid parameter does not parse is refused, so that a `waitJob` handed a
  refused dispatch's body fails at once instead of polling forever.
- **A job of windmill-lite's that the dispatcher did not start** answers
  the polls with a 501: job introspection beyond dispatched runs is
  `later` (ADR 0007, `Jobs`).
- **A query parameter that would change the run** and that no stock
  helper sends (`scheduled_for`, `cache_ttl`, `job_id`) answers 501, named
  by it; a `scheduled_in_secs` that is not a number of seconds answers 400.

**The object store** ([WA-16](../../../docs/semantics/client-api.md#wa-16),
PF3) answers from `Services.Objects`, for explicit keys:

| Operation | Route | Answer |
|---|---|---|
| `fileUpload` | `POST /w/{ws}/job_helpers/upload_s3_file` | the raw body stored at (`storage`, or `_default_`; `file_key`) with the query's `content_type` and `content_disposition`, answered `{"file_key": <file_key>}` |
| `fileDownload` | `GET /w/{ws}/job_helpers/download_s3_file` | the content, with its upload's content type (`application/octet-stream` when none) and disposition; `404` for an object that is not there |

What the client contract leaves to the server answers 501, named: an
upload without `file_key` (a key the server generates), a call with
`s3_resource_path` (a resource's own bucket), and every call when no
object store is configured; `delete_s3_file` and the credential and
presigning routes have no handler.

The error texts the reference does not give (an unknown job's result, a
missing object, a body that is not JSON, an id that is not a UUID) are
windmill-lite's, `Not found: windmill-lite: …` or `Bad request:
windmill-lite: …`, with the status Windmill's API gives the variant.

## Attribution and transports

Every call is attributed to the invocation that made it, never inferred
from the token in the request. An invocation is registered (`Register`,
which `Executor` does) before it runs, with its id, its run and its job
environment, and its registration ends when it settles (`Session.End`).
`New` opens two loopback origins, and a process gets one as `BASE_URL`
([internal/harness](../../harness/README.md)):

| Origin | Process | Transport | Attributed by |
|---|---|---|---|
| `WarmOrigin` | warm and host mode | the harness intercepts every `fetch` to it and sends a `platform` frame; `Platform` is the harness's handler | the invocation the frame names, from the fetch's async context |
| `FreshOrigin` | `fresh` | loopback TCP; the facade listens there | the bearer token (`WM_TOKEN`), since a fresh process is one invocation |

- **Nothing listens at the warm origin.** Its port is the local end of a
  loopback connection the facade holds both ends of, so no other socket is
  given the port and the kernel refuses every connection to it, on Linux
  and on macOS alike. A socket that is only bound would not do: macOS drops
  the SYNs that reach a bound socket that does not listen, so a client
  would wait for its timeout instead of being refused. A warm step's
  `node:http` call, or any transport but the intercepted `fetch`, gets
  "connection refused", reaches no handler and leaves no record
  (restriction, [ADR 0001](../../../docs/design/0001-equivalence-contract.md)
  §5a).
- **A call that names no live invocation** (an unknown token, the token of
  an invocation that has settled, a token two live invocations carry, an
  unregistered invocation id) answers `401` with `Not authorized: unknown
  windmill-lite invocation token`. It is recorded as a stray call
  (`Stray`, the latest 256), never against an invocation. A token two
  registrations carried names none until both have ended; a later
  registration is then its sole owner.
- **One reply for both transports.** The answer to `HEAD`, and one with a
  null-body status (204, 205, 304), has no body, whichever transport
  carries it, as Go's HTTP server sends it over TCP and as a `Response`
  must be built; its status and record are kept. The API has no `HEAD`
  operation, so a `HEAD` request answers 501, named by its path.
- **The decision is made in order.** The harness hands the facade each
  `platform` frame on the connection's reader, in frame order, and the
  facade attributes and records it there, deciding at once whether the
  route is unsupported; only the answer runs on a goroutine of its own. So
  every call whose frame arrived before the invocation's result is counted
  before `Execute` returns that result. Over TCP the request and the
  result travel on different connections, so a call a fresh step never
  awaited may be counted or not, as on Windmill, where the process exits
  under it.

`Record` is a call as recorded: the invocation, method, path and query as
sent, headers, body, the matched route's name, the status and whether it
was unsupported. A session's records go to `Config.Recorded` when they are
released, once the invocation has settled and its outcome is decided.

## The 501 policy

An operation without a handler, a path no route of the API has, and a path
outside `/api/` answer `501` `text/plain` with
`Not implemented in windmill-lite: <METHOD> <route template>`, never `404`,
which the stock helpers read as "absent"
([WA-41](../../../docs/semantics/client-api.md#wa-41)). The template is the
API's, with its workspace parameter written `{ws}` as the reference writes
it (`GET /w/{ws}/jobs_u/get/{id}`); a path no route matched is named as sent,
without `/api`. Every 501, a handler's included, is recorded against the
invocation as unsupported.

`Executor(inner)` wraps a step executor (a harness `Pool` or `Process`). In
`PolicyFail`, the default, an outcome of an invocation with any recorded
unsupported call, whatever its kind, becomes

```json
{"name":"UnsupportedPlatformCall","message":"Not implemented in windmill-lite: <first call>","calls":["<METHOD> <route template>", …]}
```

an `OutcomeError` with origin `facade`, which the engine stores as
`{"error": …}`. Its `message` is the first unsupported call's answer
(`Record.Message`): the `text/plain` body of a handler's 501, such as a
refused dispatch's `dispatch target not in bundle: <path>`, or the route's
`Not implemented in windmill-lite: …`. `calls` lists every unsupported call in order, repeats
included. The answers of calls still being answered are waited for, until
the step's context ends. In `PolicyObserve` the stock clients' behaviour
stands ([WA-V5](../../../docs/semantics/client-api.md#wa-v5)), for oracle
comparison. An abandoned invocation has no outcome to replace.

## Tests

- `routes_test.go`: the table against the vendored schema, and matching
  (trailing parameters, literals over parameters, unescaping, misses).
- `facade_test.go`, without Bun: attribution on the intercepted path (two
  invocations sending one token), over TCP (own token, another live
  invocation's, none, an unknown one, an ended one, a shared one, and its
  sole owner once the sharers have ended), the warm origin refusing a
  connection at once, the 501 bodies and names, the policy in both modes
  and on an error outcome, the wait for a call still being answered, a
  handler's panic and invalid status on both transports
  (`TestHandlerFailuresBothTransports`), bodyless replies to `HEAD` and
  204 on both (`TestBodylessReplies`), WA-42/a's rows, and handler
  registration.
- `services_test.go` (PF3), without Bun: the root lookup and a read's
  `job_id` from the job registry (an ended job of a live run, a flow
  scope, the run, a job of an ended run); flow user state on both
  transports (the write's answer, the read, null, the 404, the 400); the
  dispatch routes with a fake dispatcher (what reaches it, the 201, a
  refused target and its quoted message, a hash, the refused query
  parameters, a bad `scheduled_in_secs`, no dispatcher); the result polls
  (completed, running with and without `get_started`, WA-V2/a and /b, an
  id that is not a UUID, a job the dispatcher did not start); and the
  object store routes (a round trip with metadata, a missing object, the
  501s of a generated key, a resource's bucket, deletion and no store).
- `providers_test.go`, without Bun: the resource and variable reads on
  both transports, their statuses, content types and bodies (WA-11/a,
  WA-12/a, WA-40's raw read and exists, WA-42/b and /c, WA-V3/a), a read
  with a live invocation's `job_id` and with an unknown one, a write's 501,
  and a facade with no platform.
- `bun_test.go`, on the pinned Bun with the stock `windmill-client@1.767.0`
  (`WL_BUN`, `WL_WINDMILL_CLIENT`; CI sets both and `WL_REQUIRE_BUN`):
  - `TestAttribution`, ADR 0007's attribution test and milestone PF1's
    exit gate: two runs at once in one warm process, the client imported
    during the first run's module load; every call of each run is recorded
    against its own invocation while both send the first run's token. A
    warm step's `node:http` call gets `ECONNREFUSED` and nothing is
    recorded. Two fresh processes' TCP calls carry their own tokens and are
    attributed by them, and a call replaying the first one's token after it
    ended gets the 401;
  - `TestTransportsAgree`, [WA-30/a](../../../docs/semantics/conformance.md#wa-30):
    one step through the intercepted fetch and over TCP gives the same
    result and the same recorded calls (method, path, query, body), its
    `HEAD` requests included;
  - `TestHandlerPanicReleasesTheStep`: a step whose call's handler panics
    gets the 502 and ends, and its outcome comes at once, in warm and in
    fresh, with the 502 recorded;
  - `TestUnsupportedPolicyOnTheClient`, WA-41/a, WA-41/b and WA-V5/a–c on
    the stock client, in both modes and both policies; since PF3 serves
    dispatch and flow user state, the test answers those two routes 501
    itself, as WA-V5's recording facade does;
  - `TestUnawaitedUnsupportedCall`: a warm step's unsupported call that it
    never awaits, whose 3 MiB body travels in part frames, fails it 50 of
    50 times;
  - `TestProvidersOnTheClient` (PF2): the stock client's `getResource`
    and `getVariable` against the reads, in warm and fresh mode: its `$res:`
    and `res://` prefixes, `var://` stripped and `$var:` not (WA-11/a), a
    missing resource's wrapped error and `undefined` (WA-10/a), the wrapped
    depth error (WA-12/c) and nested not-found (WA-42/c), `$WM_*` left
    literal (WA-V3/a), a secret's plaintext, and `getState()` returning
    `undefined` for a state never written (WA-41/b), with no unsupported
    call;
  - in `services_bun_test.go` (PF3), on the stock client in warm and fresh
    mode: `TestFlowUserStateOnTheClient` (WA-14/a: two steps of a run share
    its map, a missing key and an undefined value read null, a script
    job's write is swallowed with the helper's log line, or rejects when
    strict); `TestObjectsOnTheClient` (WA-16/a–c: the upload request with
    its `file_key` and body, the round trip, another storage, binary
    content on both transports, a bare string refused before any request,
    a missing object's error); and `TestDispatchRefusedOnTheClient` (the
    restriction of ADR 0001 §5a: the async helpers resolve to the
    refusal's body, the synchronous one rejects at once, and in fail mode
    the step fails with `UnsupportedPlatformCall` quoting the refusal).

The step cases of [conformance/steps](../../../conformance/README.md)
(`wa-2.yaml`, `wa-5.yaml`, `wa-31.yaml`, `wa-41.yaml`; PF2's `wa-10.yaml`,
`wa-11.yaml`, `wa-12.yaml`, `wa-42.yaml`, `wa-v3.yaml`; PF3's `wa-14.yaml`,
`wa-15.yaml`, `wa-16.yaml`, `wa-v2.yaml`) run the facade through `wltest
check` in `warm` and `fresh`, and the flow fixtures `client-api/user-state`
and `client-api/dispatch` run it with the engine on the oracle's
recordings.

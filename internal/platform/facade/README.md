# internal/platform/facade

**Current state:** the platform facade of
[ADR 0007](../../../docs/design/0007-platform-interface.md) serves the root
lookup, the resource and variable reads, a variable's update, flow user
state, dispatch and its result polls, and the object store; every other
route of Windmill's API answers 501. This README covers
`internal/platform/facade`. Job identity and the services are
[internal/platform](../README.md).

The facade is the Go router behind the stock client's platform calls. It
attributes every call to the invocation that made it, over two
transports. It answers with Windmill's statuses, `text/plain` error bodies
and web framework rejections, and applies the 501 policy to the routes
windmill-lite does not serve.

## Files

| File | Contents |
|---|---|
| `facade.go` | `Facade`, `Config`, `Policy`, `Session`, `Call`, `Reply`, `Handler`, `Record`: registration, attribution, recording, a token's workspace, the 501 decision |
| `routes.go`, `routes.json` | The route table and its matching |
| `transport.go` | The two origins and the harness's handler (`Platform`) |
| `executor.go` | `Executor`: a step executor that registers each invocation and applies the 501 policy |
| `jobs.go` | The root lookup, flow user state and the result polls, from the job registry and the dispatcher |
| `providers.go` | The resource and variable reads, a read's `job_id`, and the variable update, answered from the caller's run's own services when it has them (H13) |
| `dispatch.go` | The dispatch routes |
| `objects.go` | The object store's job helpers |
| `errors.go` | `Error`: an API error as a reply, with the variants of `platform.Error` (WA-42) |
| `extract.go` | The web framework's rejections of a request it cannot extract (a UUID parameter, a JSON body) and the content type of a handler's text answer (WA-42) |

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
`Caller` (the invocation's id, run and job environment, and the platform
services of a run that chose its own, F2a), the matched
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
[WA-V1](../../../docs/semantics/client-api.md#wa-v1)), from the job
registry
([internal/platform](../README.md#the-job-registry-and-flow-user-state),
D3). The facade registers each invocation there as a script job while it
runs (`Register`, its `WM_JOB_ID`, `WM_ROOT_JOB_ID` and `WM_JOB_PATH`),
and the engine each flow run. The lookup answers:

- **A known job:** a job of a run that is still running, ended or not, a
  flow scope one of its jobs runs in, the run itself, and a job of a run
  that has ended. Windmill keeps a completed job's row until its job
  retention removes it, and the registry keeps the latest 4096 ended jobs
  ([WA-19](../../../docs/semantics/client-api.md#wa-19)).
- **No job in the path's workspace:** an id the registry does not know of
  or has forgotten, and any id looked up in a workspace other than the
  caller's. It answers as Windmill answers an id with no job row (WA-19/b):
  `400` `text/plain` `SqlErr: no rows returned by a query that expected
  to return at least one row`, without Windmill's Rust location (D14).
- **An id that is not a UUID:** the web framework's `400` (WA-19/c,
  [Request rejections](#request-rejections)).

**The resource and variable reads** answer from `Config.Platform`
([internal/platform](../README.md#services)), nil meaning none, so every
read is a not-found; for an invocation whose run chose its own providers
(H13: `engine.Invocation.Platform`, kept as `Caller.Platform`), they
and the variable update answer from those services instead, which share
the facade's job registry, flow user state, object store and dispatcher
that the other routes answer from. They keep the three resource-read contracts of
[WA-40](../../../docs/semantics/client-api.md#wa-40) apart, and answer
`application/json` on success and WA-42's `text/plain` errors:

| Operation | Route | Answer |
|---|---|---|
| `getResourceValueInterpolated` | `GET /w/{ws}/resources/get_value_interpolated/{path}` | the interpolated value (contract (b), [WA-12](../../../docs/semantics/client-api.md#wa-12)), with `$WM_*` strings literal unless `job_id` is given; `allow_cache` is ignored; [A read's `job_id`](#a-reads-job_id) |
| `getResourceValue` | `GET /w/{ws}/resources/get_value/{path}` | the stored value, nothing resolved (contract (a)) |
| `existsResource`, `existsVariable` | `GET /w/{ws}/resources/exists/{path}`, `…/variables/exists/{path}` | `true` or `false` |
| `getVariableValue` | `GET /w/{ws}/variables/get_value/{path}` | the value as a JSON string, a secret's plaintext through `Secrets` and the value resolvers ([WA-11](../../../docs/semantics/client-api.md#wa-11)) |

<a id="a-reads-job_id"></a>
**A read's `job_id`** (WA-12/g) is checked before the read: a value that
is not a UUID, the empty one included, is refused with `400`, whatever
the resource holds ([Request rejections](#request-rejections)).

A UUID's job is looked up only when the walk meets a `$WM_*` string, at each one,
as Windmill looks it up, so a value without one reads as if no `job_id`
had been sent. The job must be a live job of the path's workspace, whose
stored fields the strings then are (`platform.ReadJobEnv`,
`platform.APIEnv`), else the read answers `404` `Not found: Job not found
at name <id>`. A live flow job, whose job variables the registry does not
keep, answers 501.

`updateVariable`, `POST /w/{ws}/variables/update/{path}`
([WA-11](../../../docs/semantics/client-api.md#wa-11)), updates an existing
variable in the services' variable store (`platform.Services.UpdateVariable`),
which later reads, the engine's included, see. It answers as Windmill does:

- **The body** is an `EditVariable`, whose `value` replaces the value and
  whose `is_secret` sets its secrecy. It goes through the web framework's
  checks first ([Request rejections](#request-rejections)); a body that
  parses but is not an object answers `422` `Failed to deserialize the
  JSON body into the target type: <message>`, whose message is
  windmill-lite's.
- **The answer** is `200` `text/plain; charset=utf-8` `variable <path>
  updated (npath: "<path>")`, the path after the update, quoted with `"`
  and `\` escaped (WA-11/e).
- **Refusals:** a missing variable is `404` `Not found: Variable not found
  at name <path>`, without Windmill's location (D14); a secrecy that
  differs from the variable's, sent without a value, is `400` `Bad
  request: cannot change is_secret without updating value too`.
- **Not served:** a rename (a `path` other than the route's), a value
  already encrypted (`already_encrypted=true`) and an update of a
  read-only variable service answer 501.

The stock clients never show the answer's text to a script: TS
`setVariable` asks `existsVariable` first, then updates or creates, and
resolves to `undefined`. The other writes (`createResource`,
`updateResourceValue`, `createVariable`) are `later` and answer 501, as
does state's write (`setState`); `getState` reads the state path through
`get_value_interpolated`, so a state never written is a 404, which the
stock client turns into `undefined`
([WA-41](../../../docs/semantics/client-api.md#wa-41)/b).

**Flow user state** ([WA-14](../../../docs/semantics/client-api.md#wa-14),
PF3) answers from `platform.Services`:

| Operation | Route | Answer |
|---|---|---|
| `getFlowUserState` | `GET /w/{ws}/jobs/flow/user_states/{id}/{key}` | the value at key of the flow job, as `application/json`; `null` for a missing key, for a job that is not a live flow job (a script job, an unknown one, a run that has ended) |
| `setFlowUserState` | `POST /w/{ws}/jobs/flow/user_states/{id}/{key}` | the body, any JSON, stored at key, answered `200` `text/plain; charset=utf-8` `Flow job state updated`; `404` `Not found: Flow job not found` unless the job is a live flow job; a JSON body that jsonb could not store is a `400` with windmill-lite's text |

Both routes take the id as a UUID, named `job_id` in Windmill's route,
and the POST a JSON body: the web framework's rejections answer first,
the id before the body ([Request rejections](#request-rejections),
WA-14/d).

A value comes back in jsonb's key order with its numbers as Windmill's
backend JSON library writes them, as the oracle recorded
(`conformance/flows/client-api/user-state`, run `order`).

**Dispatch** ([WA-15](../../../docs/semantics/client-api.md#wa-15), PF3)
hands each run to `Services.Dispatcher`
([internal/platform](../README.md#the-dispatcher); the default is
`engine.Dispatcher`):

| Operation | Route | Answer |
|---|---|---|
| `runScriptByPath`, `runFlowByPath` | `POST /w/{ws}/jobs/run/p/{path}`, `…/run/f/{path}` | `201` `text/plain; charset=utf-8` with the new job's id; the body is the run's arguments, and the query's `parent_job`, `root_job`, `tag` and `scheduled_in_secs` go to the dispatcher with the caller's job variables |
| `runScriptByHash` | `POST /w/{ws}/jobs/run/h/{hash}` | refused: a windmill-lite bundle records no script hash, so no hash names a root |
| `getCompletedJobResultMaybe` | `GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}` | what waitJob polls ([WA-V2](../../../docs/semantics/client-api.md#wa-v2)): a dispatched run's `started`, `success`, `completed` and `result`, with explicit nulls while it runs (`started` only when `get_started=true` asks). A UUID that names no job, and any job looked up in a workspace other than the caller's, get Windmill's answer for an id with no job row |
| `getCompletedJobResult` | `GET /w/{ws}/jobs_u/completed/get_result/{id}` | a dispatched run's result once it has completed; before, for a UUID that names no job, and in a workspace other than the caller's, `404` `Not found: Completed Job not found at name <id>` ([WA-19](../../../docs/semantics/client-api.md#wa-19)/d), without Windmill's location |

- **A target that is not a root of the bundle** (ADR 0001 §5a), and
  every target when there is no dispatcher, answers `501` with the body
  `dispatch target not in bundle: <target>`; the 501 policy quotes it as
  the step's error. The stock client's async helpers take that body for a
  job id (WA-V5/a).
- **A result poll whose id is not a UUID** gets the web framework's `400`
  ([Request rejections](#request-rejections), WA-V2/c), so a `waitJob`
  handed a refused dispatch's body fails at once instead of polling
  forever.
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
upload without `file_key` (a key the server generates) and a call with
`s3_resource_path` (a resource's own bucket). `delete_s3_file` and the
credential and presigning routes have no handler.

With no object store configured, both routes answer `500` `Internal:
windmill-lite: no object store is configured`. Windmill CE answers an
upload with no workspace storage with a 500
([WA-16](../../../docs/semantics/client-api.md#wa-16)/d), so the stock
client's `writeS3File` throws `Internal Server Error` on both. CE's body
was not recorded, and the download's status is windmill-lite's choice,
the upload's.

Where the reference gives no text, the text is windmill-lite's, with the
status that Windmill gives:

- a missing object: `404` `Not found: windmill-lite: no object "<key>" in
  storage "<storage>"`;
- no object store: the `500` above;
- a variable update's body that parses but is not an object: the `422`
  above, whose message starts `windmill-lite:`.

## Request rejections

Windmill's web framework refuses a request whose path, query or JSON body
it cannot extract, before the handler runs and after the token check
([WA-42](../../../docs/semantics/client-api.md#wa-42)). Its answers carry
no error variant's prefix and are typed `text/plain; charset=utf-8`, the
type a handler's successful text answer has too; an `Error` answer is
bare `text/plain`. The facade gives these answers on the routes it serves
(`extract.go`):

| Request | Routes | Answer |
|---|---|---|
| A job id that is not a UUID | the root lookup, the result polls (`id`), flow user state (`job_id`) | `400` ``Invalid URL: Cannot parse `<param>` with value `<id>`: UUID parsing failed: <detail>`` (WA-V2/c, WA-19/c, WA-14/d) |
| A `job_id` query parameter that is not a UUID | `getResourceValueInterpolated` | `400` with windmill-lite's text, `windmill-lite: the query parameter job_id, "<id>", is not a UUID: <detail>`; the reference gives the status only (WA-12/g) |
| A body without a JSON content type (`application/json`, or `application/<x>+json`) | `setFlowUserState`, `updateVariable` | `415` ``Expected request with `Content-Type: application/json` `` |
| A body that does not parse as JSON | `setFlowUserState`, `updateVariable` | `400` `Failed to parse the request body as JSON: <message>`, the message in the backend JSON library's words, without the member's path that Windmill puts in front of an error inside a member or an element (`a: ` for `{"a": x}`) |

A job id is read as Windmill's UUID parser reads it: 32 hex digits, the
hyphenated form, or the hyphenated form in braces or after `urn:uuid:`,
in either case. The facade looks up its lower-case hyphenated form.

The `<detail>` is the parser's reason in the reference's words:

- `invalid length: found <n>` for hex digits alone;
- ``invalid character: found `<c>` at <i>``;
- `invalid group count: expected 5, found <n>`;
- `invalid group length in group <g>: expected <e>, found <n>`.

The reference does not say from where `<i>` and `<g>` count. The facade
counts the character's byte position from 1 and the group from 0.

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
- **A token is valid for its job's workspace only**
  ([WA-4](../../../docs/semantics/client-api.md#wa-4), FU8). An
  authenticated route whose path names a workspace other than the
  caller's (`WM_WORKSPACE`) answers `401` `Not authorized: Unauthorized`
  before its handler runs: before the 501 of an operation without one and
  before any rejection of its path or body. The call is recorded against
  its invocation with that status, and is not unsupported. Every route of
  a workspace (`/w/{ws}/…`) is authenticated but those of the groups whose
  segment ends in `_u`: `jobs_u`, whose routes check no token and look
  their job up in the path's workspace, where windmill-lite has none
  unless it is the caller's; and `apps_u`, which has no handler. A caller
  without `WM_WORKSPACE` is of every workspace.
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
`Config.Answered` receives each call of a registered invocation as soon as
its answer is decided, in the order the facade answers them, so a runtime
that keeps a run's calls ([wl](../../../wl/README.md), F1) has those of a
step that is abandoned and never released.

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
  invocations sending one token), each answered call given to `Answered`
  before any release, a stray one not (F1), over TCP (own token, another live
  invocation's, none, an unknown one, an ended one, a shared one, and its
  sole owner once the sharers have ended), the warm origin refusing a
  connection at once, the 501 bodies and names, the policy in both modes
  and on an error outcome, the wait for a call still being answered, a
  handler's panic and invalid status on both transports
  (`TestHandlerFailuresBothTransports`), bodyless replies to `HEAD` and
  204 on both (`TestBodylessReplies`), WA-42/a's rows, and handler
  registration.
- `services_test.go`, without Bun, on both transports:
  - the root lookup from the job registry: an ended job of a live run, a
    flow scope, the run, a job of an ended run and the ended run, and a
    UUID in each of the parser's other forms;
  - the root lookup's `400` for an unknown id and for a job looked up in
    another workspace, and the framework's `400` for an id that is not a
    UUID;
  - the workspace of a token (`TestTokenWorkspace`): the `401` before a
    handler, a 501 and a rejection, recorded and not unsupported, with the
    state unchanged. `jobs_u`, `apps_u` (its 501), the routes outside a
    workspace and a caller without `WM_WORKSPACE` are not refused;
  - flow user state: the write's answer and its type, the read, null and
    the 404;
  - flow user state's rejections, with the state unchanged: an id that is
    not a UUID before the body, a body that is not JSON, and a body
    without a JSON content type;
  - the dispatch routes with a fake dispatcher: what reaches it, the 201
    and its type, a refused target and its quoted message, a hash, the
    refused query parameters, a bad `scheduled_in_secs`, no dispatcher;
  - the result polls: completed, running with and without `get_started`,
    and a UUID that names no job;
  - the result polls' refusals: the framework's `400` and the parser's
    other reasons, a job the dispatcher did not start, `getResult`'s
    not-found, and a job looked up in another workspace;
  - the object store routes: a round trip with metadata, a missing object,
    the 501s of a generated key, a resource's bucket and deletion, and the
    500s with no store, which are not unsupported.
- `providers_test.go`, without Bun:
  - the resource and variable reads on both transports: their statuses,
    content types and bodies, the raw read and exists, a create's 501, and
    a facade with no platform;
  - a read with a live invocation's `job_id`, with an unknown one and with
    one that is not a UUID;
  - a read's `job_id` (`TestReadWithAJobID`): ignored for a value without
    a `$WM_*` string when it is a UUID. For a value with one, the 404 of a
    random UUID, an ended job and another workspace's job, and the
    caller's own job resolved, also in upper case;
  - the `400` of a `job_id` that is not a UUID or is empty, for either
    value, and a live flow job's 501, which the policy quotes;
  - the variable update (`TestVariableUpdate`): Windmill's text and type on
    both transports, a path with a quote and a backslash, a value later
    reads see, and its secrecy with a value;
  - the update's refusals: a secrecy change without a value, the missing
    variable's 404, the framework's 415 and 400, and the 422 of a body
    that is not an object. A rename, an encrypted value and a read-only
    service answer 501;
  - the per-run providers (H13): an invocation whose run chose its own
    services reads variables and resources from them, another the
    facade's, and both the shared job registry.
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
  - in `services_bun_test.go`, on the stock client in warm and fresh
    mode:
    - `TestFlowUserStateOnTheClient`: two steps of a run share its map, a
      missing key and an undefined value read null, and a script job's
      write is swallowed with the helper's log line, or rejects when
      strict;
    - `TestObjectsOnTheClient`: the upload request with its `file_key` and
      body, the round trip, another storage, binary content on both
      transports, a bare string refused before any request, and a missing
      object's error;
    - `TestNoObjectStoreOnTheClient`: with no object store, `writeS3File`
      rejects with `Internal Server Error`, status 500, and the step's own
      result stands;
    - `TestSetVariableOnTheClient`: `setVariable` asks `existsVariable`,
      updates, resolves to `undefined`, and the variable reads the new
      value;
    - `TestDispatchRefusedOnTheClient`: the restriction of ADR 0001 §5a.
      The async helpers resolve to the refusal's body, the synchronous one
      rejects at once, and in fail mode the step fails with
      `UnsupportedPlatformCall` quoting the refusal.

The step cases of [conformance/steps](../../../conformance/README.md) run
the facade through `wltest check` in `warm` and `fresh`: `wa-2.yaml`,
`wa-5.yaml`, `wa-10.yaml`, `wa-11.yaml`, `wa-12.yaml`, `wa-14.yaml`,
`wa-15.yaml`, `wa-16.yaml`, `wa-19.yaml`, `wa-31.yaml`, `wa-41.yaml`,
`wa-42.yaml`, `wa-v2.yaml` and `wa-v3.yaml`. The flow fixtures
`client-api/user-state` and `client-api/dispatch` run it with the engine on
the oracle's recordings.

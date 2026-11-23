# internal/platform/facade

**Current state:** working. The facade serves the root lookup, the job reads
of a live run, the resource and variable reads, a variable's update, the
writes of [state](../../../docs/glossary.md#state), dispatch with its result
polls, and the object store. It also serves
[flow user state](../README.md#the-job-registry-and-flow-user-state): values
that last as long as one flow run, where state lives in the resource store.
It answers a request to restart a flow as Windmill's Community Edition does.

Any other route of Windmill's API answers 501 to a call that names a live
invocation. On an authenticated workspace route, one under `/w/{ws}/` whose
group does not end in `_u`, a call to another workspace gets 401 first; the
`_u` groups, such as `apps_u`, keep their 501
([The workspace of a token](#the-workspace-of-a-token)).

This README covers `internal/platform/facade`. Job identity and the services
that the routes answer from are [internal/platform](../README.md).

The [platform facade](../../../docs/glossary.md#platform-facade) is the Go
router behind the platform calls of a step's stock `windmill-client`. It
attributes every call to the
[invocation](../../../docs/glossary.md#invocation) that made it, over two
transports, and answers with Windmill's statuses, `text/plain` error bodies
and web framework rejections. A step that calls a route windmill-lite does
not serve fails under [the 501 policy](#the-501-policy).

- **Who calls it:** the host that builds a runtime makes one facade per
  runtime ([wl](../../../wl/README.md), and `wltest` for each case,
  [cmd/wltest](../../../cmd/wltest/README.md)). Its `Executor` wraps a
  step executor, such as a harness `Pool` or `Process` or `wl`'s host
  executor, and is the executor that the engine runs steps with
  ([internal/engine](../../engine/README.md)). The
  [harness](../../../docs/glossary.md#harness) hands it the calls of
  [`warm`](../../../docs/glossary.md#warm) and
  [host mode](../../../docs/glossary.md#host-mode) processes through
  `Platform`
  ([internal/harness](../../harness/README.md)).
- **What it calls:** the platform services
  ([internal/platform](../README.md#services)), which the engine shares.

The [ADR](../../../docs/glossary.md#adr)
[Platform interface, the `wmill` shim, and hybrid dispatch](../../../docs/design/0007-platform-interface.md)
defines the facade, its transports and the 501 policy. Windmill's rules come
from the client API findings of the
[semantics reference](../../../docs/semantics/client-api.md).

## Files

| File | What it holds |
|---|---|
| `facade.go` | `Facade`, `Config`, `Policy`, `Session`, `Call`, `Caller`, `Reply`, `Handler`, `Record`, `RegistrationKey`: registration, attribution, recording, a token's workspace and the 501 decision |
| `routes.go`, `routes.json` | The route table and its matching |
| `transport.go` | The two origins, and the harness's handler (`Platform`) |
| `executor.go` | `Executor`: a step executor that registers each invocation and applies the 501 policy |
| `jobs.go` | The root lookup, the job reads, flow user state, the result polls and the restart route |
| `providers.go` | The resource and variable reads, a read's `job_id`, and the variable update |
| `resources.go` | The writes of state |
| `dispatch.go` | The dispatch routes |
| `objects.go` | The object store's job helpers |
| `errors.go` | `Error`: an API error as a reply |
| `extract.go` | The web framework's rejections, and the content type of a handler's text answer |

## Public surface

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `New`, `Config`, `Facade` | function, structs | Make a facade with its two origins; the host receives a `*Facade`, safe for concurrent use. `Config` holds the 501 policy (`Policy`), the largest TCP body (`MaxBody`, 512 MiB when zero), the `Recorded` and `Answered` callbacks and the services (`Platform`) | `wl`, `wltest` |
| `Facade.Close` | method | Release the origins; the listening one stops serving | `wl`, `wltest` |
| `Facade.Executor` | method | A step executor that registers each invocation, applies the 501 policy and ends the registration when it settles | `wl`, `wltest` |
| `Facade.Register`, `Session` (`Calls`, `End`, `Release`, `Unsupported`) | method, struct | One invocation's registration ([Attribution and transports](#attribution-and-transports)) | `Executor`; tests |
| `Facade.Platform` | method | The harness's handler for the intercepted path | `wl`, `wltest`, through the harness |
| `Facade.WarmOrigin`, `FreshOrigin` | methods | The two origins, one of which a process gets as `BASE_URL` | `wl`, `wltest` |
| `Facade.Handle`, `Handler`, `Call`, `Caller`, `Reply`, `Route` (`Name`) | method, types | Register a handler by operationId, and what it receives and answers ([Handlers](#handlers)) | the package's route files |
| `Facade.Policy`, `Policy` (`PolicyFail`, `PolicyObserve`) | method, type | The 501 policy | tests; `wl` and `wltest` keep the default, `PolicyFail` |
| `Facade.Stray` | method | The latest 256 calls that named no invocation | tests |
| `Record`, `RegistrationKey` | struct, function | A call as recorded, and the opaque key of the registration that admitted it | `wl` (a run's trace); `wltest` uses `Record` |
| `Error` (`Reply`) | type | An API error as Windmill renders it: `platform.Error`'s variants | handlers |
| `Unauthorized` | constant | `Not authorized: Unauthorized`, the 401 of a token used outside its workspace | the package |
| `UnknownToken` | constant | `Not authorized: unknown windmill-lite invocation token`, the 401 of a call that names no live invocation | the package |
| `UnsupportedPrefix` | constant | `Not implemented in windmill-lite: `, the start of every 501 body that names a route | the package |

## The route table

`routes.json` lists every operation of Windmill's REST API: its method, its
path template relative to the API base (`<origin>/api`) and its
operationId. `TestRouteTable` generates it from the Apache-2.0
`openapi.yaml` vendored with its notice in
[internal/oracle](../../oracle/README.md), and fails while the two differ.
To regenerate it, run
`go test ./internal/platform/facade -run TestRouteTable -args -update`.
Route templates are wire-format literals.

How a request finds its route:

- A request path must start with `/api/`.
- A template's parameter matches one segment, except its last, which may
  take the rest of the path. Windmill's paths, such as `u/x/script`, travel
  with their slashes, because the stock client encodes path parameters with
  `encodeURI`.
- Parameters are unescaped.
- Among the routes that match, the one with the most literal segments wins,
  then the shorter one.

## Handlers

`Facade.Handle` registers a handler by operationId. Registering an operation
that the API does not have panics. A handler runs on a goroutine of its own
and receives the `Call`:

- its `Caller`: the invocation's id, run and job environment, the
  services of a run that chose its own providers (`Caller.Platform`), and
  its run's host configuration (`Caller.Host`), whose workspace scopes the
  job lookups;
- the matched `Route` and its unescaped parameters;
- the method, path, query, headers and body as sent.

It answers a `Reply`. `Error` renders the variants of Windmill's API error
type ([WA-42](../../../docs/semantics/client-api.md#wa-42)) with their
statuses, as bare `text/plain`. A text that windmill-lite writes in place of
Windmill's server text carries no source location, here and below: a
[named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations).

A handler that panics, or answers a status outside 200 to 599, gets a 502 in
its place on both transports: `Bad gateway: windmill-lite: the handler of
<route> failed: <panic>` for a panic, and `Bad gateway: windmill-lite: the
handler of <route> answered status <n>` for a status. The record keeps that status. The call stops
counting as in flight however the handler ended, so the 501 policy never
waits on a call that has failed.

## Routes served

Every route answers from `Config.Platform`, the `*platform.Services` that the
engine shares; nil means services of the facade's own, with nothing in
them.

The resource and variable routes answer an invocation whose run chose
its own providers from that run's services instead
([internal/platform](../README.md#per-run-providers)), which share the job
registry, flow user state, object store, dispatcher, result cache and
resource store. Each route that takes a UUID, a `job_id` or a JSON body runs
the web framework's checks first
([Request rejections](#request-rejections)). The state writes of services
with no resource store answer their 501 before them.

### Jobs

| Operation | Route | Answer |
|---|---|---|
| `getRootJobId` | `GET /w/{ws}/jobs_u/get_root_job_id/{id}` | The effective root of the job as a JSON string ([WA-14](../../../docs/semantics/client-api.md#wa-14), [WA-V1](../../../docs/semantics/client-api.md#wa-v1)) |
| `getJob` | `GET /w/{ws}/jobs_u/get/{id}` | `200` `application/json`: the record of a job of a live run, with the fields windmill-lite serves ([WA-19](../../../docs/semantics/client-api.md#wa-19)) |
| `getJobArgs` | `GET /w/{ws}/jobs_u/get_args/{id}` | `200` `application/json`: the job's stored arguments, in jsonb's text output |
| `restartFlowAtStep` | `POST /w/{ws}/jobs/restart/f/{id}` | An enterprise feature (`EE`): `400` `text/plain` `Bad request: Restarting a flow is a feature only available in enterprise version`, whatever the id and body, as the Community Edition answers ([JE-19](../../../docs/semantics/expressions.md#je-19), [OR-21](../../../docs/semantics/oracle.md#or-21)) |

The job routes answer from the job registry, which reaches the run that
holds each job ([internal/platform](../README.md#the-job-registry-and-flow-user-state)).
The facade registers each invocation there as a script job while it runs
(`Register`, with its `WM_JOB_ID`, `WM_ROOT_JOB_ID` and `WM_JOB_PATH`), and
the engine registers each flow run.

The root lookup answers:

- **A known job:** a job of a run that is still running, ended or not; a
  flow scope one of its jobs runs in; and the run itself.
- **A job of a run that has ended**, among the latest 4096 ended jobs that
  the registry keeps.
- **No job in the path's workspace:** an unknown or forgotten id, any id
  looked up in a workspace other than the caller's, and a job of a run of
  another workspace ([A job's workspace](#a-jobs-workspace)). It answers as
  Windmill answers an id with no job row: `400` `text/plain` `SqlErr: no
  rows returned by a query that expected to return at least one row`.
- **An id that is not a UUID:** the web framework's `400`.

The job reads answer:

- **A job of a live run**, running or completed: its record or arguments.
  The query's `no_logs` and `no_code` change nothing, because windmill-lite
  serves neither the logs nor the code that they leave out.
- **No job in the path's workspace:** `404` `text/plain`, `Not found: Job
  not found at name <id>` or `Not found: Job Args not found at name <id>`.
- **A job of a run that has ended:** `501`, with the reason after the
  route's name, which the 501 policy quotes when it fails the step. Windmill
  answers from the job's row: a
  [named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **A job that no run of the runtime holds, and a record windmill-lite does
  not reproduce**, such as a canceled job's: `501` too.
- **An id that is not a UUID:** the web framework's `400`.

### Resources and variables

The reads keep Windmill's three ways of resolving a resource apart: the raw
read, the client's interpolated read and worker argument resolution
([WA-40](../../../docs/semantics/client-api.md#wa-40)). The walker's rules
are in [internal/platform](../README.md#the-interpolation-walker). The
routes answer `application/json` on success, and the platform's
`text/plain` errors.

| Operation | Route | Answer |
|---|---|---|
| `getResourceValueInterpolated` | `GET /w/{ws}/resources/get_value_interpolated/{path}` | The interpolated value ([WA-12](../../../docs/semantics/client-api.md#wa-12)), with `$WM_*` strings literal unless `job_id` names a job. `allow_cache` is ignored. A path under `CUSTOM_INSTANCE_DB/` answers `401` before it is looked up, after the `job_id` check, because the caller holds a job's token |
| `getResourceValue` | `GET /w/{ws}/resources/get_value/{path}` | The stored value, nothing resolved |
| `existsResource`, `existsVariable` | `GET /w/{ws}/resources/exists/{path}`, `GET /w/{ws}/variables/exists/{path}` | `true` or `false` |
| `getVariableValue` | `GET /w/{ws}/variables/get_value/{path}` | The value as a JSON string: a secret's plaintext through `Secrets` and the value resolvers ([WA-11](../../../docs/semantics/client-api.md#wa-11)). A secret that no secret service can reveal answers `500` with Windmill's one decryption text |
| `updateVariable` | `POST /w/{ws}/variables/update/{path}` | `200` `text/plain; charset=utf-8` `variable <path> updated (npath: "<path>")`, with the path quoted with `"` and `\` escaped ([WA-11](../../../docs/semantics/client-api.md#wa-11)) |

A read's `job_id` query parameter:

- **Is checked before the read.** A value that is not a UUID, the empty one
  included, is refused with `400`, whatever the resource holds.
- **Is used by the walker** only for a `$WM_*` string, as the platform's
  [interpolated read](../README.md#contract-b-the-interpolated-read) says.
- **A job that the walker cannot use** answers `404` `Not found: Job not
  found at name <id>`: an unknown id, an ended job, or a job of another
  workspace.
- **A live flow job**, whose job variables the registry does not keep,
  answers 501.

The variable update changes an existing variable through the services
(`platform.Services.UpdateVariable`), so later reads see it, the engine's
included:

- **The body** is an `EditVariable`: its `value` replaces the value, and its
  `is_secret` sets the secrecy. A body that parses but is not an object
  answers `422` `Failed to deserialize the JSON body into the target type:
  <message>`, with a message that starts `windmill-lite:`.
- **Refusals:** a missing variable is `404` `Not found: Variable not found
  at name <path>`. A secrecy that differs from the variable's, sent without
  a value, is `400` `Bad request: cannot change is_secret without updating
  value too`.
- **Not served (501):** a rename (a `path` other than the route's), a value
  already encrypted (`already_encrypted=true`), and an update through a
  read-only variable service.
- **The stock clients never show the answer's text to a script.**
  `setVariable` asks `existsVariable` first, then updates or creates, and
  resolves to `undefined`. `createVariable` answers 501.

### State

The writes of state ([WA-13](../../../docs/semantics/client-api.md#wa-13))
change the services' resource store
([internal/platform](../README.md#the-resource-store)), which every read
finds after the providers' resources. The stock client's `setState` asks
`existsResource` first, then creates the state or updates its value.
`getState` reads it through `get_value_interpolated`, so a state never
written is a 404, which the client turns into `undefined`.

| Operation | Route | Answer |
|---|---|---|
| `createResource` | `POST /w/{ws}/resources/create` | `201` `text/plain; charset=utf-8` `resource <path> created`, for `resource_type` `state` with a valid path and value at a path that neither a provider nor the store holds. An occupied path: `400` `text/plain` `Bad request: Resource <path> already exists`. Another type: 501. An invalid path or value: `400` (below) |
| `updateResourceValue` | `POST /w/{ws}/resources/update_value/{path}` | `200` `text/plain; charset=utf-8` `value of resource <path> updated` for a stored state; `404` `text/plain` `Not found: Resource <path> not found` without one |
| `deleteResource` | `DELETE /w/{ws}/resources/delete/{path}` | `200` `text/plain; charset=utf-8` `resource <path> deleted` for a stored state; `404` `text/plain` `Not found: Resource not found at name <path>` without one |

- **The value and the path.** The services check the path of a create and
  store the value as jsonb stores it: keys in jsonb's order
  ([internal/jsonval](../../jsonval/README.md#the-stored-text)), and each
  number in the route's spelling
  ([internal/platform](../README.md#the-writes-of-state)). A value that
  jsonb refuses, such as a NUL or a lone surrogate, answers `400` `SqlErr:
  error returned from database: <message>` and stores nothing.
- **A path of another form** answers `400` with Windmill's `Bad request:
  Invalid path: …` text.
- **The body.** A create without `path` or `resource_type` answers `422`
  `Failed to deserialize the JSON body into the target type: missing field`,
  with the member's name and the position after the body's closing brace,
  as the web framework gives it. A `path` or `resource_type` that is not a
  JSON string, `null` included, answers `422` too. A `value` of JSON `null`
  stores `null`.
- **Not served (501):** another resource type; `update_if_exists`; the
  options that the stock client never sends (`description`, `labels`,
  `ws_specific`, `preserve_on_behalf_of`); an update or a delete of an entry
  of another type; and every write when the services have no store.
- **A provider's resource.** An update or a delete of it answers 501 with
  the reason after the route's name, a
  [restriction](../../../docs/design/0001-equivalence-contract.md#5a-restrictions).
- **A failing store** answers `500` `text/plain` `Internal: windmill-lite:
  the resource store failed: <err>` on every read and write that reaches
  it, and the call's `Record.Warning` carries the text for the run's trace.
  A failure never reads as a missing state. On the Redis store
  ([wl/redisstore](../../../wl/redisstore/README.md)), `<err>` is the store's
  error, such as a refused connection, and never holds the URL.

### Flow user state

Flow user state ([WA-14](../../../docs/semantics/client-api.md#wa-14))
answers from the services
([internal/platform](../README.md#flow-user-state)):

| Operation | Route | Answer |
|---|---|---|
| `getFlowUserState` | `GET /w/{ws}/jobs/flow/user_states/{id}/{key}` | The value at the key of the flow job, as `application/json`; `null` for a missing key and for a job that is not a live flow job (a script job, an unknown one, a run that has ended) |
| `setFlowUserState` | `POST /w/{ws}/jobs/flow/user_states/{id}/{key}` | The body, any JSON, stored at the key: `200` `text/plain; charset=utf-8` `Flow job state updated`. `404` `Not found: Flow job not found` unless the job is a live flow job. A JSON body that jsonb could not store is a `400` with windmill-lite's text |

- Both routes take the id as a UUID, which Windmill's route names `job_id`.
  The web framework's rejections answer first, the id before the body.
- A flow job of a run of another workspace is not a live flow job of the
  caller's ([A job's workspace](#a-jobs-workspace)): it reads `null` and
  its update answers `404`.
- A value comes back in jsonb's key order with its numbers as Windmill's
  backend JSON library writes them, as the oracle recorded
  (`conformance/flows/client-api/user-state`, run `order`).

### Dispatch and results

Dispatch ([WA-15](../../../docs/semantics/client-api.md#wa-15)) hands each
run to the services' dispatcher
([internal/platform](../README.md#the-dispatcher)). The default dispatcher
is `engine.Dispatcher`.

| Operation | Route | Answer |
|---|---|---|
| `runScriptByPath`, `runFlowByPath` | `POST /w/{ws}/jobs/run/p/{path}`, `POST /w/{ws}/jobs/run/f/{path}` | `201` `text/plain; charset=utf-8` with the new job's id. The body is the run's arguments. The query's `parent_job`, `root_job`, `tag` and `scheduled_in_secs` go to the dispatcher with the caller's job variables |
| `runScriptByHash` | `POST /w/{ws}/jobs/run/h/{hash}` | Refused as a target not in the bundle: a windmill-lite bundle records no script hash, so no hash names a root |
| `getCompletedJobResultMaybe` | `GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}` | What `waitJob` polls ([WA-V2](../../../docs/semantics/client-api.md#wa-v2)): a dispatched run's `started`, `success`, `completed` and `result`, with `200` in the cases below the table |
| `getCompletedJobResult` | `GET /w/{ws}/jobs_u/completed/get_result/{id}` | A dispatched run's result once it has completed. Before that, for a UUID that names no job, and in another workspace: `404` `Not found: Completed Job not found at name <id>` ([WA-19](../../../docs/semantics/client-api.md#wa-19)) |

- **What a result poll answers** with `200`, for a UUID in the caller's
  request (an id that is not a UUID gets `400`, and a job the dispatcher did
  not start `501`, below):
  - a completed run: `{"started":true,"success":<bool>,"completed":true,"result":<result>}`;
  - a run in progress: `started`, `success` and `result` are `null` and
    `completed` is `false`; with `get_started=true`, `started` is `true`;
  - a UUID that names no job, and a job looked up in another workspace:
    the same as a run in progress, except that with `get_started=true`
    `started` is `false`. Windmill answers an id with no job row so, while
    `getCompletedJobResult` answers it `404`.
- **A target that is not a root of the bundle**, and every target when there
  is no dispatcher, answers `501` with the body `dispatch target not in
  bundle: <target>` ([restrictions](../../../docs/design/0001-equivalence-contract.md#5a-restrictions)).
  The 501 policy quotes it as the step's error. The stock client's async
  helpers take that body for a job id
  ([WA-V5](../../../docs/semantics/client-api.md#wa-v5)).
- **A result poll whose id is not a UUID** gets the web framework's `400`. So
  a `waitJob` handed a refused dispatch's body fails at once instead of
  polling forever.
- **A job that the dispatcher did not start** answers the polls with a 501:
  windmill-lite answers them only for dispatched runs.
- **A query parameter that would change the run** and that no stock helper
  sends (`scheduled_for`, `cache_ttl`, `job_id`) answers 501, named by it. A
  `scheduled_in_secs`, when given and not empty, must be a base-ten integer
  from 0 to 9223372036854775807 (a signed 64-bit integer that is not
  negative), such as `0` or `1`; an empty value keeps the default.
  An invalid value, such as `-1` or `1.5`, answers `400`.

### Objects

The object store ([WA-16](../../../docs/semantics/client-api.md#wa-16))
answers from the services' `Objects`
([internal/platform](../README.md#the-object-store)), for explicit keys:

| Operation | Route | Answer |
|---|---|---|
| `fileUpload` | `POST /w/{ws}/job_helpers/upload_s3_file` | The raw body stored at (`storage`, or `_default_`; `file_key`) with the query's `content_type` and `content_disposition`; answered `{"file_key": <file_key>}` |
| `fileDownload` | `GET /w/{ws}/job_helpers/download_s3_file` | The content, with its upload's content type (`application/octet-stream` when it gave none) and disposition. `404` `Not found: windmill-lite: no object "<key>" in storage "<storage>"` for an object that is not there |

- **What the client contract leaves to the server** answers 501, named: an
  upload without `file_key` (a key the server generates) and a call with
  `s3_resource_path` (a resource's own bucket).
- **No handler:** `delete_s3_file` and the credential and presigning routes
  answer 501.
- **No object store configured:** both routes answer `500` `Internal:
  windmill-lite: no object store is configured`, which is not an unsupported
  call. The stock client's `writeS3File` then throws `Internal Server
  Error`, as it does on Windmill's Community Edition with no workspace
  storage.

## Request rejections

Windmill's web framework refuses a request whose path, query or JSON body it
cannot extract, after the token check and before the handler runs
([WA-42](../../../docs/semantics/client-api.md#wa-42)). Its answers carry no
error variant's prefix, and their type is `text/plain; charset=utf-8`, which
a handler's successful text answer has too. An `Error` answer is bare
`text/plain`. The facade gives these answers on the routes it serves
(`extract.go`):

| Request | Routes | Answer |
|---|---|---|
| A job id that is not a UUID | The root lookup, the job reads and the result polls (`id`); flow user state (`job_id`) | `400` ``Invalid URL: Cannot parse `<param>` with value `<id>`: UUID parsing failed: <detail>`` |
| A `job_id` query parameter that is not a UUID | `getResourceValueInterpolated` | `400` with windmill-lite's text, `windmill-lite: the query parameter job_id, "<id>", is not a UUID: <detail>`; the reference gives the status only |
| A body without a JSON content type (`application/json`, or `application/<x>+json`) | `setFlowUserState`, `updateVariable`, and `createResource` and `updateResourceValue` with a resource store | `415` ``Expected request with `Content-Type: application/json` `` |
| A body that does not parse as JSON | `setFlowUserState`, `updateVariable`, and `createResource` and `updateResourceValue` with a resource store | `400` `Failed to parse the request body as JSON: <message>`, the message in the backend JSON library's words |

The body's message leaves out the member's path that Windmill puts in front
of an error inside a member or an element, such as `a: ` for `{"a": x}`.

A job id is read as Windmill's UUID parser reads it: 32 hex digits, the
hyphenated form, or the hyphenated form in braces or after `urn:uuid:`, in
either case. The facade looks up its lower-case hyphenated form. The
`<detail>` is the parser's reason in the reference's words:

- `invalid length: found <n>` for hex digits alone;
- ``invalid character: found `<c>` at <i>``;
- `invalid group count: expected 5, found <n>`;
- `invalid group length in group <g>: expected <e>, found <n>`.

## Attribution and transports

Every call is attributed to the invocation that made it. On the intercepted
path, that is the invocation that the harness names, whatever token the
request carries. Over TCP, it is the bearer token's, because a fresh process
is one invocation.

An invocation is registered (`Register`, which `Executor` does) before it
runs, with its id, its run and its job environment. Its registration ends
when it settles (`Session.End`).

`New` opens two loopback origins, and a process gets one of them as
`BASE_URL` ([internal/harness](../../harness/README.md)):

| Origin | Process | Transport | Attributed by |
|---|---|---|---|
| `WarmOrigin` | warm and host mode | The harness intercepts every `fetch` to it and sends a `platform` frame; `Platform` is the harness's handler | The invocation that the frame names, from the fetch's async context |
| `FreshOrigin` | `fresh` | Loopback TCP; the facade listens there | The bearer token (`WM_TOKEN`), because a fresh process is one invocation |

### The warm origin

Nothing listens at the warm origin. Its port is the local end of a loopback
connection that the facade holds both ends of, so no other socket gets the
port, and the kernel refuses every connection to it, on Linux and on macOS.

- A warm step's `node:http` call, or any transport but the intercepted
  `fetch`, gets "connection refused", reaches no handler and leaves no
  record: a
  [restriction](../../../docs/design/0001-equivalence-contract.md#5a-restrictions).
- A socket that is only bound would not do. macOS drops the SYNs that reach
  a bound socket that does not listen, so a client would wait for its
  timeout instead of being refused.

### Calls that name no invocation

A call that names no live invocation answers `401` `Not authorized: unknown
windmill-lite invocation token` (`UnknownToken`): an unknown token, the
token of an invocation that has settled, a token that two live invocations
carry, or an unregistered invocation id.

- It is recorded as a stray call (`Stray`, the latest 256), never against an
  invocation.
- A token that two registrations carried names none until both have ended.
  A later registration is then its sole owner.

### The workspace of a token

A token is valid for its job's workspace only
([WA-4](../../../docs/semantics/client-api.md#wa-4)). An authenticated route
whose path names a workspace other than the caller's `WM_WORKSPACE` answers
`401` `Not authorized: Unauthorized` (`Unauthorized`) before its handler
runs: before the 501 of an operation without a handler, and before any
rejection of its path or body.

- The call is recorded against its invocation with that status, and is not
  an unsupported call.
- Every call must first name a live invocation, on every route
  ([Calls that name no invocation](#calls-that-name-no-invocation)). The
  workspace check then applies to every route of a workspace (`/w/{ws}/…`),
  except the groups whose segment ends in `_u`. The `jobs_u` routes skip it
  and look their job up in the path's workspace, where windmill-lite has none
  unless it is the caller's. `apps_u` and `ai_u` have no handler, so their
  calls keep their 501 whatever the workspace.
- A caller without `WM_WORKSPACE` belongs to every workspace.

### A job's workspace

A job is in its run's workspace: the one that the runtime or the run
configured (`platform.Job.Host`), not the caller's `WM_WORKSPACE`, which a
host hook may change. Only a call from a run of the same workspace finds
it ([WA-V2](../../../docs/semantics/client-api.md#wa-v2)).

- **The lookups that check it:** the root lookup, the job reads, flow user
  state and the result polls of the jobs that the dispatcher started.
- **A job of another workspace's run** answers as an unknown id on each
  of them. An id that the registry does not know at all answers as it
  always did.
- **Without a configured workspace** every job and every caller have the
  same scope, so the lookups answer as for one workspace.

### One reply for both transports

The answer to `HEAD`, and an answer with a null-body status (204, 205, 304),
has no body on either transport, as Go's HTTP server sends it over TCP and
as a `Response` must be built. Its status and record are kept. The API has
no `HEAD` operation, so a `HEAD` request answers 501, named by its path.

### Counting calls in order

The harness hands the facade each `platform` frame on the connection's
reader, in frame order. The facade attributes and records it there, and
decides at once whether the route is unsupported; only the answer runs on a
goroutine of its own. So every call whose frame arrived before the
invocation's result is counted before `Execute` returns that result.

Over TCP, the request and the result travel on different connections. So a
call that a fresh step never awaited may be counted or not, as on Windmill,
where the process exits under it.

### Records

`Record` is a call as recorded: the invocation, the method, path and query as
sent, the headers, the body, the matched route's name, the status, whether
it was unsupported, its message and its warning. `Registration` is the
`RegistrationKey` of the `WM_TOKEN` of the registration that admitted the
call, from its job environment, not the request: an opaque digest. A late answer
tells its own registration's calls from those of a later registration of
the same id by this key.

- **`Config.Recorded`** receives a session's records when they are released,
  once the invocation has settled and its outcome is decided.
- **`Config.Answered`** receives each call of a registered invocation once
  its answer is decided, outside the facade's lock. Two calls answered at
  once can reach it in either order; the session's records keep the order in
  which the facade accepted the calls. So a
  runtime that keeps a run's calls ([wl](../../../wl/README.md)) has those of
  a step that is abandoned and never released.
- **The Reporter** that `Executor` hands the step lets go of its session once
  the invocation has settled. A harness keeps the Reporters of its last
  settled invocations for their late output. A repeated `Settled` is passed
  on and does nothing else.

## The 501 policy

An operation without a handler, a path that no route of the API has, and a
path outside `/api/` answer `501` `text/plain` with `Not implemented in
windmill-lite: <METHOD> <route template>` (`UnsupportedPrefix`). They never
answer `404`, which the stock helpers read as "absent"
([WA-41](../../../docs/semantics/client-api.md#wa-41)).

- **The route template** is the API's, with its workspace parameter written
  `{ws}` as the reference writes it, such as `GET
  /w/{ws}/jobs_u/get_logs/{id}`. A path that no route matched is named as
  sent, without `/api`.
- **Every 501**, a handler's included, is recorded against the invocation as
  unsupported.

`Executor(inner)` wraps a step executor, such as a harness `Pool` or
`Process`. In `PolicyFail`, the default, any outcome of an invocation with a
recorded unsupported call becomes this error, an `OutcomeError` with origin
`facade`, which the engine stores as `{"error": …}`. For example, for a step
that called two routes that windmill-lite does not serve:

```json
{"name":"UnsupportedPlatformCall","message":"Not implemented in windmill-lite: GET /w/{ws}/jobs_u/get_logs/{id}","calls":["GET /w/{ws}/jobs_u/get_logs/{id}","POST /w/{ws}/jobs/run/p/{path}"]}
```

- **`message`** is the first unsupported call's answer (`Record.Message`):
  the `text/plain` body of a handler's 501, such as a refused dispatch's
  `dispatch target not in bundle: <path>`, or the route's `Not implemented
  in windmill-lite: …`.
- **`calls`** lists every unsupported call in order, repeats included.
- **Calls still being answered** are waited for, until the step's context
  ends.
- **In `PolicyObserve`** the stock clients' behaviour stands
  ([WA-V5](../../../docs/semantics/client-api.md#wa-v5)), for a comparison
  with the oracle.
- **An abandoned invocation** has no outcome to replace.

## Errors

The facade answers the services' errors as Windmill's API does
([internal/platform](../README.md#errors)). Its own answers:

| Status | Body | When |
|---|---|---|
| 401 | `Not authorized: unknown windmill-lite invocation token` | A call names no live invocation |
| 401 | `Not authorized: Unauthorized` | A token is used outside its job's workspace |
| 501 | `Not implemented in windmill-lite: <METHOD> <route template>`, or a handler's reason after the route's name | A route that windmill-lite does not serve ([The 501 policy](#the-501-policy)) |
| 501 | `dispatch target not in bundle: <target>` | A dispatch target that is not a root of the bundle |
| 502 | `Bad gateway: windmill-lite: the handler of <route> failed: <panic>`, or `… answered status <n>` | A handler panics or answers a status outside 200 to 599 |
| 413 | `Bad request: the request body exceeds windmill-lite's limit` | A TCP body larger than `MaxBody`; recorded nowhere |
| 400 | `Bad request: the request body could not be read` | A TCP body that cannot be read; recorded nowhere |
| 500 | `Internal: windmill-lite: no object store is configured` | An object route with no object store |
| 500 | `Internal: windmill-lite: the resource store failed: <err>` | The resource store fails |
| 400, 415, 422 | The web framework's texts, or windmill-lite's with its status (a `job_id` that is not a UUID, a body that is not an object) | [Request rejections](#request-rejections) |

## Decisions where the reference is silent

| Question | The code's choice | Why | Pinned by |
|---|---|---|---|
| What does a route that windmill-lite does not serve answer? | `501`, named, and in `PolicyFail` the step fails with `UnsupportedPlatformCall` | A 404 reads as "absent" to the stock helpers, which would hide the gap | `TestUnsupportedRoutes`, `TestExecutorPolicy`, `TestUnsupportedPolicyOnTheClient` |
| What does a failed handler answer? | `502`, on both transports, recorded | A client gets an answer, and the policy never waits on it | `TestHandlerFailuresBothTransports`, `TestHandlerPanicReleasesTheStep` |
| What answers a call that names no invocation? | `401`, recorded as stray, never guessed onto an invocation | A token two invocations share cannot name either | `TestTCPAttribution`, `TestInterceptedAttribution` |
| How is a warm step kept off other transports? | Nothing listens at the warm origin; the kernel refuses connections | A bound socket alone makes macOS clients wait for a timeout | `TestWarmOriginRefuses`, `TestAttribution` |
| Which text answers a missing object, a missing object store, a body that is not an object, or a `job_id` that is not a UUID? | windmill-lite's own text with Windmill's status | The reference gives the status but not the text | `TestObjectRoutes`, `TestVariableUpdate`, `TestProviderRoutes` |
| What does a download answer with no object store? | `500`, the upload's status | The oracle recorded only the upload's | `TestObjectRoutes` (the upload and the download), `TestNoObjectStoreOnTheClient` (the upload on the client) |
| May a step write a resource that the host or the bundle provides? | No: an update or a delete answers 501 with its reason | A provider's resources are configuration ([restrictions](../../../docs/design/0001-equivalence-contract.md#5a-restrictions)) | `TestStateWriteRefusals` |
| From where do `<i>` and `<g>` of a UUID detail count? | The character's byte position from 1, the group from 0 | The reference does not say | `TestResultRoutes` (the position); the group: code only |

## Tests

### Run the tests

1. To run the tests that need Bun, install Bun 1.4.3 and the stock client,
   and export `WL_BUN` and `WL_WINDMILL_CLIENT` as the table below says.
2. From the repository root, run `go test ./internal/platform/facade`. With
   both variables set, every test runs.
3. To check for data races as CI does, run
   `go test -race ./internal/platform/facade`, which needs cgo.

| Variable | Value | Tests that need it | When it is not set |
|---|---|---|---|
| `WL_BUN` | The path of a Bun 1.4.3 executable | `bun_test.go`, `services_bun_test.go`, `state_bun_test.go` | The tests skip, or fail if `WL_REQUIRE_BUN` is set |
| `WL_WINDMILL_CLIENT` | A directory whose `node_modules` holds `windmill-client@1.862.0` | the same | The tests skip, or fail if `WL_REQUIRE_BUN` is set |
| `WL_REQUIRE_BUN` | Any non-empty value | the same | A missing `WL_BUN` or `WL_WINDMILL_CLIENT` skips the tests |

CI sets all three in its Go jobs
([docs/ci.md](../../../docs/ci.md#the-go-jobs)).

### Test files

| File | Needs | What it checks |
|---|---|---|
| `routes_test.go` | — | `TestRouteTable`: the table against the vendored schema. `TestRouteMatch`: trailing parameters, literals over parameters, unescaping, misses |
| `facade_test.go` | — | Attribution on the intercepted path, where two invocations send one token, and over TCP: its own token, another live invocation's, none, an unknown one, an ended one, a shared one. `Answered`; the warm origin's refusal; the 501 bodies and names; the policy in both modes and its wait |
| | — | A failed handler's 502 and bodyless replies on both transports; the enterprise restart's 400; the status and body of 14 error variants, `Generic` among them; handler registration |
| `executor_test.go` | — | `TestSessionReporterLetsGo`: the Reporter lets go of its session once the invocation has settled, and a repeated `Settled` does nothing else |
| `services_test.go` | — | On both transports: the root lookup and its `400`s, the workspace of a token (`TestTokenWorkspace`), and flow user state with its rejections. On the intercepted path: dispatch with a fake dispatcher, the result polls and their refusals, and the object routes |
| `jobreads_test.go` | — | `TestJobReadRoutes`: the job reads of a live run's jobs, a UUID in upper case, the 404 texts, the framework's `400`, and the 501 of an ended run's job while the root lookup still answers. `TestUnservedJobRoutes`: the `jobs_u` routes that stay unserved |
| `providers_test.go` | — | The resource and variable reads on both transports; a facade with no services; a read's `job_id` (`TestReadWithAJobID`); the variable update and its refusals (`TestVariableUpdate`); a run's own providers next to the facade's (`TestCallerPlatform`) |
| `resources_test.go` | — | The writes of state against the oracle's recording of `client-api/state` (`TestStateRoutes`, `TestStateValuesAndPaths`); the writes that answer 501 and the 422s (`TestStateWriteRefusals`); no store (`TestStateWritesWithoutAStore`); a failing store's 500 and warning (`TestStoreFailureAnswers`) |
| `bun_test.go` | Bun, the stock client | `TestAttribution`: two runs at once in one warm process each get their own calls while both send the first run's token, a warm `node:http` call gets `ECONNREFUSED`, and fresh processes are attributed by their tokens |
| | | `TestTransportsAgree`: one step through the intercepted fetch and over TCP gives the same result and the same recorded calls ([WA-30](../../../docs/semantics/conformance.md#wa-30)) |
| | | `TestHandlerPanicReleasesTheStep`: a step whose call's handler panics gets the 502 and its outcome at once, in warm and fresh |
| | | `TestUnsupportedPolicyOnTheClient`: the 501 policy on the stock client, in both modes and both policies. The test answers dispatch and flow user state 501 itself, since the facade serves them ([WA-V5](../../../docs/semantics/client-api.md#wa-v5)) |
| | | `TestUnawaitedUnsupportedCall`: a warm step's unsupported call that it never awaits, with a 3 MiB body, fails the step 50 times out of 50 |
| | | `TestProvidersOnTheClient`: the client's `getResource`, `getVariable` and `getState` against the reads in warm and fresh mode, with no unsupported call. The client strips a `var://` prefix but sends `$var:` as part of the path ([WA-11](../../../docs/semantics/client-api.md#wa-11)) |
| `services_bun_test.go` | Bun, the stock client | `TestFlowUserStateOnTheClient`, `TestObjectsOnTheClient`, `TestNoObjectStoreOnTheClient`, `TestSetVariableOnTheClient`, `TestDispatchRefusedOnTheClient`: the client's helpers for these routes in warm and fresh mode. A script job's flow user state write is swallowed with the helper's log line, and rejects when strict ([WA-14](../../../docs/semantics/client-api.md#wa-14)) |
| `state_bun_test.go` | Bun, the stock client | `TestStateOnTheClient`: `getState` and `setState` against the resource store in warm and fresh mode. `TestConcurrentStateOnTheClient`: 32 invocations at once in warm mode each set a state of 64 KiB and read it back. Those that lose the create race reject with the recorded 400, and every read is one whole value |

The step cases of the conformance catalogue run the facade through
`wltest check` in `warm` and `fresh`
([conformance](../../../conformance/README.md)), and flow fixtures such as
`client-api/user-state` and `client-api/dispatch` run it with the engine
against the oracle's recordings.

# internal/platform/facade

**Current state:** milestone PF1 of
[ADR 0012](../../../docs/design/0012-layout-and-milestones.md): the platform
facade of [ADR 0007](../../../docs/design/0007-platform-interface.md). This
README covers `internal/platform/facade`. It is the Go router behind the
stock client's platform calls: the routes of Windmill's API, attribution of
every call to the invocation that made it over two transports, the 501
policy for the routes windmill-lite does not serve, and Windmill's
`text/plain` error bodies. It serves one route itself, the root lookup;
the services behind the others (resources, variables, flow user state,
object store, dispatch, the job registry) are milestones PF2 and PF3,
which register their handlers here. Job identity is
[internal/platform](../README.md).

## Files

| File | Contents |
|---|---|
| `facade.go` | `Facade`, `Config`, `Policy`, `Session`, `Call`, `Reply`, `Handler`, `Record`: registration, attribution, recording, the 501 decision |
| `routes.go`, `routes.json` | The route table and its matching |
| `transport.go` | The two origins and the harness's handler (`Platform`) |
| `executor.go` | `Executor`: a step executor that registers each invocation and applies the 501 policy |
| `jobs.go` | The root lookup, `getRootJobId` |
| `errors.go` | `Error`: the error variants of WA-42 and their bodies |

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

PF1's one handler answers `getRootJobId`, `GET
/w/{ws}/jobs_u/get_root_job_id/{id}`, with the effective root of the job as
a JSON string ([WA-14](../../../docs/semantics/client-api.md#wa-14),
[WA-V1](../../../docs/semantics/client-api.md#wa-v1)), from the identities
of the invocations registered at the time (D3): the job of any of them, or
a flow scope or root it runs in. A job it does not know of answers 501,
because answering it needs PF3's job registry.

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
`{"error": …}`. `calls` lists every unsupported call in order, repeats
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
    the stock client, in both modes and both policies;
  - `TestUnawaitedUnsupportedCall`: a warm step's unsupported call that it
    never awaits, whose 3 MiB body travels in part frames, fails it 50 of
    50 times.

The step cases of [conformance/steps](../../../conformance/README.md)
(`wa-2.yaml`, `wa-5.yaml`, `wa-31.yaml`, `wa-41.yaml`) run the facade
through `wltest check` in `warm` and `fresh`.

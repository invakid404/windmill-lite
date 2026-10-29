# internal/platform

**Current state:** milestone PF1 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md): job identity,
the part of the platform interface of
[ADR 0007](../../docs/design/0007-platform-interface.md) that the engine and
the facade share. This README covers `internal/platform` itself; the facade
(the router, its transports, attribution and the 501 policy) is
[internal/platform/facade](facade/README.md). The service interfaces of
ADR 0007's table (resources, variables, secrets, flow user state, object
store, dispatcher, job registry) and the interpolation walker are not here
yet: they are milestones PF2 and PF3.

## Job identity

`Identity` is one invocation's job identity, and `Identity.Env(Host)` its
job variables ([WA-4](../../docs/semantics/client-api.md#wa-4),
[WR-22](../../docs/semantics/languages.md#wr-22)). The engine fills it from
the run ([internal/engine](../engine/README.md)), and `wltest check` from a
step case ([cmd/wltest](../../cmd/wltest/README.md)).

| Variable | Value |
|---|---|
| `WM_JOB_ID` | the invocation's id, a fresh UUID (D3) |
| `WM_FLOW_JOB_ID` | the flow scope the step runs in, its parent job; `""` for a script job |
| `WM_ROOT_FLOW_JOB_ID` | the innermost flow scope ([WA-V1](../../docs/semantics/client-api.md#wa-v1)); `""` for a script job |
| `WM_ROOT_JOB_ID` | the effective root, never empty (WA-V1) |
| `WM_FLOW_PATH` | the parent's runnable path ([WA-6](../../docs/semantics/client-api.md#wa-6)) |
| `WM_FLOW_STEP_ID` | the module id |
| `WM_JOB_PATH` | the runnable path: `<flow path>/<step id>` for an inline step, the script's path for a path script |
| `WM_STATE_PATH` | `StatePath`, below |
| `WM_TOKEN` | a fresh `NewToken()`: `wl_` and 128 random bits in hex |
| `WM_WORKSPACE`, `WM_USERNAME`, `WM_EMAIL`, `WM_PERMISSIONED_AS`, `WM_BASE_URL` | the `Host` configuration |

A step directly in a flow has the run as its parent, its innermost scope
and its effective root; the containers of milestones N3a to N3c give their
bodies other scopes. The `later` variables of WA-4's table
(`WM_SCHEDULED_FOR`, `WM_OBJECT_PATH`, `WM_RUNNABLE_ID` and the rest) are not
set. `BASE_URL` is the process's, not the invocation's: the harness sets it
at spawn ([internal/harness](../harness/README.md)).

**Host configuration.** `Host` holds what an instance and the run's user
would supply. A zero field takes `DefaultHost`'s: workspace and username
`windmill-lite`, email `windmill-lite@localhost`, `WM_PERMISSIONED_AS`
`u/<username>`, and `WM_BASE_URL` `http://localhost:3000`, which is the
stock client's own default for an unset `WM_BASE_URL`
([WA-1](../../docs/semantics/client-api.md#wa-1)). These are windmill-lite's
placeholders, not Windmill's.

**The state path** ([WA-5](../../docs/semantics/client-api.md#wa-5)). No
schedule triggers a windmill-lite run (schedules are out of scope,
[ADR 0001](../../docs/design/0001-equivalence-contract.md) §3), so the
trigger part is always `user`:

1. a flow step: `<flow path>/<step id, or nostep>_user`;
2. a script job with a path: `<script path>/user`, after appending `state`
   to a path that ends with `/`;
3. otherwise: `u/<username>/tmp_state`.

**The token.** The facade never attributes a call on the intercepted path
by the token it carries, because the stock client captures `WM_TOKEN` at
import ([WA-31](../../docs/semantics/client-api.md#wa-31)). Over loopback
TCP, where a fresh process is one invocation, the token names it, so it is
random and unguessable.

## Tests

`identity_test.go` checks the state path's branches (WA-5/a and WA-5/c
among them), the variables `Env` sets with and without a host
configuration, and the token's form.

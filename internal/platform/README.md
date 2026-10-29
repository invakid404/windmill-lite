# internal/platform

**Current state:** milestones PF1 and PF2 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md): job identity,
the part of the platform interface of
[ADR 0007](../../docs/design/0007-platform-interface.md) that the engine and
the facade share (PF1), and the resource, variable and secret services with
their local defaults and the interpolation walker (PF2). This README covers
`internal/platform` and its `testdata/`; the facade (the router, its
transports, attribution, the 501 policy, and the routes that serve these
services) is [internal/platform/facade](facade/README.md). The flow user
state, object store, dispatcher and job registry services are milestone
PF3.

## Files

| File | Contents |
|---|---|
| `identity.go` | `Identity`, `Host`, `StatePath`, `NewToken`: job identity (PF1) |
| `services.go` | `Resource`, `Variable`, the `Resources`, `Variables`, `Secrets` and `ValueResolver` interfaces, `LocalSecrets`, `Store`, `ResourceChain`, `VariableChain`, `Services` |
| `interpolate.go` | the walker: `RawResource`, `InterpolatedResource`, `ResourceExists`, `VariableExists`, `VariableValue`, `ResolveArgs`, `APIEnv` |
| `json.go` | JSON values as Windmill's backend JSON library parses and writes them ([FX-42](../../docs/semantics/execution.md#fx-42) rules 2 and 9), `JSONString`, and `Reprint` and `FormatDouble`, which the engine writes a loop's result and serde's error texts with |
| `errors.go` | `Error`: the variants of the API's error type, their statuses and displays (WA-42) |
| `data.go` | `LoadData`: a bundle's `data/` as a `Store` |

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

## Services

`Services` holds the services of ADR 0007's table that PF2 implements:
`Resources`, `Variables` and `Secrets`, and the host's `ValueResolver`
chain. A nil field takes its default (no resources, no variables,
`LocalSecrets`, no resolvers), and a nil `*Services` has nothing, so every
read is a not-found. The facade answers its routes from it and the engine
reads it ([internal/engine](../engine/README.md)); a host gives both the
same one.

- **Records.** A `Resource` is its type and its value as the workspace
  holds it, `$var:` and `$res:` strings kept. A `Variable` is its value,
  whether it is a secret, and whether that value is `Encrypted`: ciphertext
  under the workspace key, as a sync folder pulled without `plainSecrets`
  holds a secret ([SY-11](../../docs/semantics/openflow.md#sy-11)).
- **The local default** is `Store`, an in-memory map of resources and
  variables. `LoadData` fills one from a bundle's `data/resources.json` and
  `data/variables.json`, the format
  [internal/bundle](../bundle/README.md) writes; a bundle never holds a
  secret, so the host supplies secrets. `SetResource` refuses a value
  jsonb would refuse (a NUL escape, an unpaired surrogate escape, a number
  beyond numeric's range), as Windmill refuses to create one.
- **Host overrides.** `ResourceChain` and `VariableChain` put services in
  order, the first that has a path answering for it (WA-40's design note):
  a host's own store before the bundle's.
- **Secrets.** `Secrets` reveals the plaintext of a secret variable and
  decrypts an `$encrypted:` value. windmill-lite never decrypts Windmill
  ciphertext ([JE-43](../../docs/semantics/expressions.md#je-43)):
  `LocalSecrets` reveals a secret held as plaintext (a folder pulled with
  `plainSecrets`, or a secret a host passed in) and fails ciphertext and
  `$encrypted:` with `ErrSecretUnavailable`, which a read reports as the
  500 `Internal: secret_unavailable: …`.
- **The host secret layer.** After interpolation, each resource value
  (nested ones included) and each secret's plaintext passes through
  `Resolvers`, in order, which may replace markers of the host's own. A
  raw read is not passed through them. windmill-lite defines no marker of
  its own.

## The interpolation walker

One walker implements resolution for the three contracts of
[WA-40](../../docs/semantics/client-api.md#wa-40), and keeps them apart:

| Contract | Method | What it resolves |
|---|---|---|
| (a) raw read, `resources/get_value` | `RawResource` | nothing |
| (b) client interpolated read, `resources/get_value_interpolated` ([WA-12](../../docs/semantics/client-api.md#wa-12)) | `InterpolatedResource` | `$var:`, `$jsonvar:`, nested `$res:`; `$WM_*` only with a job's variables |
| (c) worker argument resolution ([JE-41](../../docs/semantics/expressions.md#je-41), [JE-42](../../docs/semantics/expressions.md#je-42), [WR-5](../../docs/semantics/languages.md#wr-5)) | `ResolveArgs` | `$var:`, `$jsonvar:`, `$res:` (a read of (b) with the job's variables), `$encrypted:`, `$<NAME>` |

- **Both walkers** fail at depth 50 before looking at the value there,
  walk arrays only at a depth of at most 2 with at most 1000 items, and
  never walk what `$var:` or `$jsonvar:` substituted. A string is replaced
  only when it starts with a marker.
- **Contract (b)** starts at depth 0, loads a nested `$res:` at the
  referring string's depth plus one (its path needs a `/`), and resolves
  `$WM_*` strings only from a job's variables: `APIEnv` of a job's process
  environment, its stored root in place of its effective root, so a step
  directly in a top-level flow sees `""` there
  ([WA-V3](../../docs/semantics/client-api.md#wa-v3),
  [WA-V1](../../docs/semantics/client-api.md#wa-v1)). Its errors are the
  API's (`Error`): `Not found: Resource <p> not found` for the read's own
  resource, `Not found: Resource not found at name <p>` for a nested one
  (without Windmill's Rust location, D14), `Not found: Variable <p> not
  found`, and the 500s of an invalid path, a `$jsonvar:` that is not JSON
  and the depth limit.
- **Contract (c)** walks a top-level argument only when its text holds
  `$var:`, `$jsonvar:`, `$res:` or `$encrypted:` anywhere; any other
  argument is kept as it is. A nested `$res:` starts a depth counter of its
  own. `$<NAME>` reads the job's reserved variables (the engine passes its
  job identity's). Its failures name the innermost object key, or the argument:
  ``Variable <p> not found for `<name>`: <cause>`` and ``Resource <p> not
  found for `<name>`: <cause>``, the cause the API's error text, and the
  internal errors of an invalid `$res:` path, a `$jsonvar:` that is not
  JSON and the depth limit.
- **Written out again.** A value the walker parsed is written out as
  Windmill's backend JSON library writes it (`json.go`,
  [FX-42](../../docs/semantics/execution.md#fx-42) rule 2): its key order
  kept, a repeated key in its first place with its last value, an integer
  exact when a signed or unsigned 64-bit integer holds it, every other
  number a double in the library's notation (`1000.0`, `1e+21`,
  `1.2345678901234568e+29`, plain decimals for a decimal exponent from -5
  to 15), minimal string escapes, no whitespace. Parse failures are in the
  library's words, with its line and column (`unexpected end of hex escape
  at line 1 column 14`). A resource's value is read in jsonb's stored form
  first, as the API reads it from its column.

## Tests

- `identity_test.go` checks the state path's branches (WA-5/a and WA-5/c
  among them), the variables `Env` sets with and without a host
  configuration, and the token's form.
- `interpolate_test.go` runs `testdata/interpolation.json`, the walker's
  cases, each citing its catalogue case or finding: WA-10/a, WA-11/a,
  WA-12/a to WA-12/f, WA-42/b and /c, WA-V3/a and /b, JE-42/a to /h,
  WR-5/a to /e, FX-42/h and FX-51/c, among others, through the three
  contracts. It also checks the secret layer (plaintext, ciphertext with
  and without a host's `Secrets`, a host's resolvers), the chains, and
  `APIEnv`.
- `json_test.go` checks the double notation, parse and write, and the
  parse errors with their positions.
- `data_test.go` loads `testdata/bundle/data/`, a bundle's `data/` in
  internal/bundle's format.

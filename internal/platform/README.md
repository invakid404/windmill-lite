# internal/platform

**Current state:** milestones PF1 to PF3 and N4 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md): job identity,
the part of the platform interface of
[ADR 0007](../../docs/design/0007-platform-interface.md) that the engine and
the facade share (PF1); the resource, variable and secret services with
their local defaults and the interpolation walker (PF2); the job
registry, flow user state, object store and dispatcher services (PF3);
and the result cache service of `cache_ttl` with its in-memory default
(N4).
This README covers `internal/platform` and its `testdata/`; the facade
(the router, its transports, attribution, the 501 policy, and the routes
that serve these services) is [internal/platform/facade](facade/README.md),
and the default dispatcher, which runs a bundle's roots, is the engine's
([internal/engine](../engine/README.md)).

## Files

| File | Contents |
|---|---|
| `identity.go` | `Identity`, `Host`, `StatePath`, `NewToken`: job identity (PF1) |
| `services.go` | `Resource`, `Variable`, the `Resources`, `Variables`, `VariableUpdater`, `Secrets` and `ValueResolver` interfaces, `LocalSecrets`, `Store`, `ResourceChain`, `VariableChain`, `Services` |
| `jobs.go` | `Job`, `JobKind`, the `Jobs` and `FlowUserState` interfaces, `Registry` and `UserStates`, their local defaults, and the services' lookups: `RootJob`, `JobEnv`, `FlowUserState`, `SetFlowUserState` (PF3) |
| `objects.go` | `Object`, the `ObjectStore` interface, `DirObjects`, `DefaultStorage` (PF3) |
| `dispatch.go` | `Dispatch`, `DispatchKind`, `JobResult`, the `Dispatcher` interface, `NotInBundleError` (PF3) |
| `results.go` | the `ResultCache` interface and `MemoryResults`, its local default (N4) |
| `interpolate.go` | the walker: `RawResource`, `InterpolatedResource`, `ResourceExists`, `VariableExists`, `VariableValue`, `ResolveArgs`, `APIEnv`; `UpdateVariable` |
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

`Services` holds the services of ADR 0007's table: `Resources`,
`Variables` and `Secrets`, and the host's `ValueResolver` chain (PF2);
`Jobs`, `UserState`, `Objects` and `Dispatcher` (PF3); and `Results`
(N4), which only the engine uses. A nil field takes its default: no
resources, no variables, `LocalSecrets`, no resolvers; for `Jobs` one
`Registry` per `Services` (`Services.Registry`), and for `UserState` one
`UserStates` per `Services` over whichever `Jobs` it holds, each made
when first needed, so the facade and the engine that share a `*Services`
share them; no object store, whose routes then answer 501; no
dispatcher, so that no dispatch target is in the bundle; and for
`Results` one `MemoryResults` per `Services` (`Services.ResultCache`),
made when first needed. A
nil `*Services` has nothing, so every read is a not-found and no job is
kept. The facade answers its routes from it and the engine reads it and
registers its runs with it ([internal/engine](../engine/README.md)); a
host gives both the same one, and sets every field before any run
starts.

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
- **Updates** (F1, [WA-11](../../docs/semantics/client-api.md#wa-11)).
  `Services.UpdateVariable` updates an existing variable through the
  variable service when it is a `VariableUpdater`, as `Store` is: a new
  value replaces the old one (and is plaintext, no longer `Encrypted`), and
  a new secrecy replaces the old; a missing variable is the read's
  not-found. A variable service that is not one, a chain among them, is
  read-only (`ErrReadOnlyVariables`), and the facade answers 501. A runtime
  that owns its store ([wl](../../wl/README.md)) thus keeps a step's update
  for the runs after it.
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

## The job registry and flow user state

`Jobs` is the job registry of ADR 0007 (D3): windmill-lite's job ids are
its own, and job lookups answer from the runs a runtime started, not from
a job table. Its local default, `Registry`, keeps each `Job`: its id, its
kind (`JobFlow` or `JobScript`), its effective root
([WA-V1](../../docs/semantics/client-api.md#wa-v1)), its runnable path and,
for a script job, its job variables.

- **Who registers what.** The engine registers each flow run it starts,
  its own root, from start to end
  ([internal/engine](../engine/README.md)); the facade registers each
  invocation, a script job, from `Register` to the end of its registration
  ([internal/platform/facade](facade/README.md)). A script job's parent
  flow, innermost flow scope and root (`WM_FLOW_JOB_ID`,
  `WM_ROOT_FLOW_JOB_ID`, `WM_ROOT_JOB_ID`) become known with it, as flow
  jobs of its root, live while the root is: a loop iteration's child flow
  is known while one of its steps has run and its run lives.
- **How long.** A job stays known while its root lives, so a step's job
  can be looked up after it ended. Once the root has ended, the jobs of its
  tree that have ended are forgotten with their user state; a job still
  live (a step abandoned at the run's end, D2) stays, with the scopes it
  runs in, until it ends. A long-lived runtime keeps only the trees of the
  work it still runs.
- **The lookups.** `Services.RootJob` answers `getRootJobId`: the
  effective root of a known job. `Services.JobEnv` gives the job variables
  of a known script job, from which a resource read made with its `job_id`
  resolves `$WM_*` strings (`APIEnv`).

**Flow user state** ([WA-14](../../docs/semantics/client-api.md#wa-14)) is
a key/value map per flow job, which the stock helpers address by their
effective root. `Services.FlowUserState` reads a key: the value stored, or
JSON `null` for a missing key, for a job that is not a flow job, for an
unknown job, and once the run has ended, as the API answers once a root
has left the queue. `Services.SetFlowUserState` writes a key of a live flow
job only, and otherwise fails with `ErrFlowJobNotFound`, the API's 404
`Not found: Flow job not found`; a body that is not JSON jsonb could store
is a 400. The value is held as a read gives it back, which the oracle
recorded: jsonb's key order, numbers as Windmill's JSON library writes
them. The local default, `UserStates`, works with whichever `Jobs` the
services hold, a host's included: it keeps each map with its job's
effective root, read from that registry, and forgets a run's maps when
the run's registration through `Services.JobRegistry` ends and the
registry no longer reports it live: windmill-lite's lifetime policy
(WA-14's design note), faithful for every access the stock helpers make
during the run; so a write by id after the run is refused with the 404,
where whether Windmill accepts it is
[Q-WA-10](../../docs/semantics/client-api.md#q-wa-10). So that it stays
bounded with a registry that knows runs registered elsewhere, it also
drops, each time it has doubled since the last sweep, the maps of jobs
the registry no longer reports live, which no read could see. A host's
own `FlowUserState` gets only reads and writes of live flow jobs, and
keeps its own lifetime.

## The object store

`ObjectStore` ([WA-16](../../docs/semantics/client-api.md#wa-16)) keeps
files by storage and key, the storage `_default_` (`DefaultStorage`) for an
object that names none: what the job helpers `writeS3File` and
`loadS3File` reach, for explicit keys. Its local default, `DirObjects`,
keeps each object in a directory as one file, named by the SHA-256 of its
storage and key, so that no key names a path outside the directory
whatever its slashes or dots. The file holds one version of the object
whole, a line of JSON metadata (its storage, key, content type and
disposition) and then its content; a `Put` writes the new version to a
temporary file and renames it into place, so concurrent readers and
writers only see versions a `Put` wrote, content and metadata together,
the last rename wins, and a `Put` that fails leaves the version before
it as it was. A file whose metadata names another object is refused. It
claims no parity with a CE
server for what the client contract leaves open (keys a server generates,
a resource's own bucket, quotas, errors: WA-16's design note,
[Q-WA-3](../../docs/semantics/client-api.md#q-wa-3)).

## The dispatcher

`Dispatcher` (ADR 0007) starts the runs that steps dispatch with
`runScript*Async`, `runFlowAsync` and their synchronous forms
([WA-15](../../docs/semantics/client-api.md#wa-15)), from a `Dispatch`: the
target (a script's or flow's path, or a script's hash), the arguments, the
query's `parent_job`, `root_job`, `tag` and `scheduled_in_secs`, and the
caller's job variables. It answers the result polls for them with a
`JobResult` ([WA-V2](../../docs/semantics/client-api.md#wa-v2)). A target
that is not a root of the bundle fails with `NotInBundleError`, whose text
`dispatch target not in bundle: <target>` the facade answers as a 501 and
the 501 policy quotes (ADR 0001 §5a). The default, which runs a bundle's
roots as separate runs of the same runtime, is `engine.Dispatcher`.

## The result cache

`ResultCache` (ADR 0007, milestone N4) keeps the results of the jobs that
run with a `cache_ttl` ([FX-40](../../docs/semantics/execution.md#fx-40)):
`Get(key, now)` answers a value stored under key that expires after now,
and `Put(key, value, expires)` stores one, replacing what was there. The
engine owns everything else (the key, which TTL applies, what a value
holds and when a job stores), so a store keeps opaque bytes and compares
expiries with the time the engine gives it, its run's clock (H10), never
its own. A host puts a shared store behind it (D4: a cache per process
unless the host plugs in a shared store). The local default,
`MemoryResults`, keeps entries in memory, one per `Services`, with a heap
of their expiries: every `Get` and `Put` drops, earliest first, each
entry that expired by the latest time a `Get` gave, so it holds only
entries still live then, whatever their number, and the heap, which
keeps the older expiries of keys stored again until they come up, is
rebuilt from the live entries once it has outgrown twice their number.
A hit compares an entry's expiry with its own `Get`'s time; only the
reclaiming uses the latest one, so with clocks that disagree (H10's
record mode) an entry may go before its own run's clock reaches its
expiry, a miss and never a stale hit.

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
- `jobs_test.go` (PF3) checks the registry's lifetime: a run and its jobs,
  the scopes a script job names, a job that ended while its run lives, a
  step that outlives its run, jobs of a root never registered, and the
  tree forgotten with nothing left behind; flow user state (WA-14/a's
  reads and writes, the stored form, the 404 and the 400, and WA-14/c's
  read and write after the run, with its maps forgotten), with a host's
  `Jobs` (writes, reads and the maps forgotten with the run), bounded with
  a registry whose runs end elsewhere, and with a host's `FlowUserState`;
  and the services' one registry, shared by callers that first ask at
  once.
- `objects_test.go` (PF3) checks `DirObjects`: storages kept apart,
  metadata, a replacing write, a missing object, keys that would escape
  the directory, no temporary file left, concurrent writers and readers
  seeing only whole versions and ending on a writer's last, a failed
  `Put` leaving the committed version, and a foreign file refused.
- `results_test.go` (N4) checks `MemoryResults`: a value until its expiry
  and not at it, a replacing write, copies kept and returned, a burst of
  expired entries reclaimed while a small working set goes on, a key
  stored again many times keeping the heap bounded, the services' one
  default and a host's cache, and concurrent writers and readers seeing
  whole values.
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

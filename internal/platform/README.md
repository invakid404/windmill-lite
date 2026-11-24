# internal/platform

**Current state:** working. Job identity, the platform services with their
local defaults, the read cache, the resource store and the interpolation
walker work, and so do the job registry,
[flow user state](#the-job-registry-and-flow-user-state) and the job reads
of a live run. Flow user state lasts as long as its flow run, unlike a
step's state in the resource store. The package defines the dispatcher's interface but has no
dispatcher of its own, and it never decrypts Windmill's ciphertext.

Once a run has ended, the package keeps only the id, kind, root and path of
its jobs, for the root lookup: not their records, arguments or variables.

This README covers `internal/platform` and its `testdata/`, but not its
`facade/` directory, which [internal/platform/facade](facade/README.md)
covers.

This package holds the platform services that a running
[flow](../../docs/glossary.md#flow) uses besides the code of its
[steps](../../docs/glossary.md#step): job
identity, resources, variables and secrets, the job registry, flow user
state, the object store, the dispatcher and the result cache. Two callers
share one `*Services`:

- the [engine](../../docs/glossary.md#engine)
  ([internal/engine](../engine/README.md)) reads the services to resolve a
  step's arguments and `flow_env`, registers its runs with them, and keeps
  `cache_ttl` results in them;
- the [platform facade](../../docs/glossary.md#platform-facade)
  ([internal/platform/facade](facade/README.md)) answers the job API calls
  of a step's script from them.

A host builds the `*Services`: [wl](../../wl/README.md), the Go host SDK,
for the `windmill-lite` command and Go hosts, and `wltest` for the
conformance runs ([cmd/wltest](../../cmd/wltest/README.md)). The default
dispatcher, which runs a bundle's [roots](../../docs/glossary.md#root), is
the engine's `engine.Dispatcher`.

The [ADR](../../docs/glossary.md#adr)
[Platform interface, the `wmill` shim, and hybrid dispatch](../../docs/design/0007-platform-interface.md)
defines these services. Windmill's rules come from the client API findings
of the [semantics reference](../../docs/semantics/client-api.md). Where
windmill-lite departs from Windmill, the
[equivalence contract](../../docs/design/0001-equivalence-contract.md)
names the departure among its
[named deviations](../../docs/design/0001-equivalence-contract.md#5-named-deviations).

## Files

| File | What it holds |
|---|---|
| `identity.go` | Job identity: `Identity`, `Host`, `DefaultHost`, `StatePath`, `ObjectPath`, `NewToken` |
| `services.go` | `Services`, the service interfaces, the records `Resource` and `Variable`, `Store`, the chains, `LocalSecrets`, `Providers` |
| `data.go` | `LoadData`: a bundle's `data/` directory as a `Store` |
| `readcache.go` | `ReadCache`, a run's read cache |
| `resourcestore.go` | The `ResourceStore` interface, whose doc comment is its contract, and `MemoryResourceStore` |
| `statewrites.go` | The writes of state: `CreateStateResource`, `UpdateStateResource`, `DeleteStateResource`, the path check and the stored form of a value |
| `jobs.go` | The job registry (`Jobs`, `Registry`), flow user state (`FlowUserState`, `UserStates`), the job reads and the job lookups |
| `objects.go` | The object store: `ObjectStore`, `DirObjects` |
| `dispatch.go` | The dispatcher's interface: `Dispatch`, `Dispatcher`, `JobResult`, `NotInBundleError` |
| `results.go` | The result cache: `ResultCache`, `MemoryResults` |
| `interpolate.go` | The interpolation walker and the variable reads and updates |
| `rootseal.go` | `SealForRoot` and the secret test of a `flow_env` entry |
| `json.go` | JSON as Windmill's backend JSON library parses and writes it |
| `errors.go` | `Error`, the variants of Windmill's API error type |

## Public surface

Every exported name, grouped by concern. "Engine", "facade", "`wl`" and
"`wltest`" name the callers outside the package.

### Services and their records

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Services` | struct | The platform services of one runtime ([Services](#services)) | built by `wl` and `wltest`; read by the engine and the facade |
| `Resources`, `Variables`, `Secrets` | interfaces | The resource, variable and secret services | hosts implement them |
| `VariableUpdater` | interface | A variable service that takes writes ([Variable updates](#variable-updates)) | `Store`, `VariableChain` and `ReadCache` implement it |
| `ValueResolver` | interface | The host secret layer ([Secrets](#secrets)) | hosts implement it |
| `Resource`, `Variable` | structs | A resource (type and stored value) and a variable (value, `IsSecret`, `Encrypted`) | every service |
| `Store`, `NewStore` | struct, constructor | The local default of resources and variables: `Resource`, `Variable`, `SetResource`, `SetVariable`, `UpdateVariable`, `Paths` | `wl`, `wltest` |
| `LoadData` | function | A bundle's `data/` directory as a `Store` | `wl` |
| `DataResources`, `DataVariables` | constants | `data/resources.json` and `data/variables.json`, the files `LoadData` reads | the package |
| `ResourceChain`, `VariableChain` | types | Services in order: the first that has a path answers for it | `wl` |
| `LocalSecrets` | struct | The default secret service | the package |
| `Providers` | struct | The providers a run chooses for itself ([Per-run providers](#per-run-providers)) | `wl` |

### Identity and environment

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Identity`, `Identity.Env` | struct, method | One invocation's job identity, and its job variables ([Job identity](#job-identity)) | engine, `wltest` |
| `Host`, `DefaultHost` | struct, variable | The host configuration and its defaults ([Host configuration](#host-configuration)) | engine, `wltest` |
| `StatePath`, `ObjectPath` | functions | `WM_STATE_PATH` and `WM_OBJECT_PATH` | `Identity.Env` |
| `NewToken` | function | A fresh `WM_TOKEN` | engine, `wltest` |
| `APIEnv` | function | The variables an interpolated read with a job id resolves `$WM_*` strings from ([Contract (b)](#contract-b-the-interpolated-read)) | engine, the package |

### The read cache and the resource store

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `ReadCache`, `NewReadCache` | struct, constructor | A run's read cache ([The read cache](#the-read-cache)) | `wl` |
| `ResourceStore` | interface | Where the resources that steps write are kept ([The resource store](#the-resource-store)) | `wl`, `wl/redisstore`, hosts |
| `MemoryResourceStore`, `NewMemoryResourceStore` | struct, constructor | The memory store ([The memory store](#the-memory-store)) | `wl` |
| `ReleaseMemoryResourceStore` | function | Drops a memory store's entries when its runtime closes | `wl` |
| `StateType` | constant | `state`, the one type the job API writes | facade |
| `Services.CreateStateResource`, `UpdateStateResource`, `DeleteStateResource` | methods | The writes of state ([The writes of state](#the-writes-of-state)) | facade |
| `WithStoreFailureReport`, `WithoutStoreFailureReport` | functions | Add or remove the report function of a run's store failures | `wl`; facade |
| `WriteBodySyntaxError` | function | Checks a resource write's body as Windmill's web framework does | facade |

### Jobs and flow user state

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Jobs`, `Registry`, `NewRegistry` | interface, struct, constructor | The job registry and its local default ([The job registry and flow user state](#the-job-registry-and-flow-user-state)) | the package; the engine, the facade and `wltest` reach `Jobs` through `Services.JobRegistry` |
| `Job`, `JobKind` (`JobFlow`, `JobScript`) | struct, type | What the registry knows of one job | engine, facade, `wltest` |
| `JobView` | interface | Renders a live run's job records | engine implements it |
| `FlowUserState`, `FlowUserStateLister`, `UserStates`, `NewUserStates` | interfaces, struct, constructor | Flow user state and its local default | the package; hosts implement `FlowUserState` |
| `JobLookup` | function type | Finds the job an interpolated read names | facade |
| `Services.Registry`, `JobRegistry`, `RootJob`, `JobEnv`, `ReadJobEnv`, `GetJob`, `GetJobArgs`, `ResultPoll`, `FlowUserState`, `FlowUserStates`, `SetFlowUserState` | methods | The lookups ([Lookups](#lookups)) and the job reads | engine, facade, `wltest` (`JobRegistry`); `Services.Registry` only inside the package |

### Objects, dispatch and results

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `ObjectStore`, `Object` | interface, struct | The object store ([The object store](#the-object-store)) | facade, engine |
| `DirObjects`, `NewDirObjects` | struct, constructor | Its local default, a directory | `wl`, `wltest` |
| `DefaultStorage` | constant | `_default_`, the storage of an object that names none | engine, facade |
| `Dispatcher`, `Dispatch`, `DispatchKind` (`DispatchScript`, `DispatchFlow`, `DispatchHash`), `JobResult` | interface, structs, type | The dispatcher ([The dispatcher](#the-dispatcher)) | engine implements it; facade calls it |
| `ResultCache`, `MemoryResults`, `NewMemoryResults`, `Services.ResultCache` | interface, struct, constructor, method | The result cache ([The result cache](#the-result-cache)) | engine; `wl` (`ResultCache`) |

### The walker and JSON

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Services.RawResource`, `InterpolatedResource`, `InterpolatedResourceOf`, `ResourceExists` | methods | Resource reads ([The interpolation walker](#the-interpolation-walker)) | facade, engine |
| `Services.VariableValue`, `VariableExists`, `UpdateVariable` | methods | Variable reads and updates | facade, engine |
| `Services.ResolveArgs` | method | Worker argument resolution of a step's arguments and `flow_env` | engine |
| `Services.WithProviders` | method | Services with a run's own providers | `wl` |
| `SealForRoot`, `Services.SecretEntry`, `Services.SecretVariable` | function, methods | [Values windmill-lite seals](#values-windmill-lite-seals) | engine (`SealForRoot`, `SecretEntry`); `SecretVariable` only inside the package |
| `Reprint`, `FormatDouble`, `JSONString` | functions | JSON written as Windmill's backend writes it ([JSON](#json-as-windmills-backend-writes-it)) | engine; facade |
| `JSONSyntaxError` | function | Why a text is not JSON, in the backend's words | facade |

### Error values

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Error` (`Body`, `HTTPStatus`) | struct | A variant of Windmill's API error type | facade renders it |
| `NotServed`, `NotInBundleError` | structs | A call windmill-lite does not answer | facade, engine |
| `ErrEndedRun`, `ErrFlowJobNotFound`, `ErrNoJobVariables`, `ErrNotStateResource`, `ErrProvidedResource`, `ErrReadOnlyVariables`, `ErrSecretUnavailable`, `ErrStoreReleased` | variables | The package's errors ([Errors](#errors)) | facade (`ErrNoJobVariables`, `ErrNotStateResource`, `ErrProvidedResource`, `ErrReadOnlyVariables`); engine (`ErrEndedRun`); the others inside the package and in tests |

## Job identity

`Identity` is one invocation's job identity: the job it is and the flow
scopes it runs in. `Identity.Env(Host)` returns its job variables, every
reserved variable that Windmill gives a job
([WA-4](../../docs/semantics/client-api.md#wa-4),
[WR-22](../../docs/semantics/languages.md#wr-22)). The engine fills the
identity from the run, and `wltest check` fills it from a step case.

| Variable | Value |
|---|---|
| `WM_JOB_ID` | A fresh UUID per invocation. windmill-lite's job ids are its own (a [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations)) |
| `WM_FLOW_JOB_ID` | The flow scope the step runs in, its parent job; `""` for a script job |
| `WM_ROOT_FLOW_JOB_ID` | The innermost flow scope ([WA-V1](../../docs/semantics/client-api.md#wa-v1)); `""` for a script job |
| `WM_ROOT_JOB_ID` | The effective root: the first that is set of the stored root, the innermost scope, the parent and the job itself, so never empty |
| `WM_FLOW_PATH` | The parent's runnable path ([WA-6](../../docs/semantics/client-api.md#wa-6)) |
| `WM_FLOW_STEP_ID` | The module id |
| `WM_JOB_PATH` | The runnable path: `<flow path>/<step id>` for an inline step, the script's path for a path script |
| `WM_STATE_PATH` | `StatePath` ([The object path and the state path](#the-object-path-and-the-state-path)) |
| `WM_TOKEN` | A fresh `NewToken()`: `wl_` and 128 random bits in hex |
| `WM_WORKSPACE`, `WM_USERNAME`, `WM_EMAIL`, `WM_PERMISSIONED_AS`, `WM_BASE_URL`, `WM_WORKER_GROUP` | The `Host` configuration |
| `WM_ROOT_WORKSPACE` | The workspace, `WM_WORKSPACE` |
| `WM_END_USER_EMAIL` | The run's user's address, `WM_EMAIL` |
| `WM_SCHEDULED_FOR` | The job's `Start`, as RFC 3339 in whole seconds, UTC |
| `WM_OBJECT_PATH` | `ObjectPath` |
| `WM_SCHEDULE_PATH`, `WM_TESTED_RUNNABLE`, `WM_RUNNABLE_ID` | `""` |

A step directly in a flow has the run as its parent, its innermost scope and
its effective root. The containers that run their bodies as child flows,
such as loops and branches, give their steps other scopes
([internal/engine](../engine/README.md)).

### Why each value is what it is

`Env` sets every variable of Windmill's table, as the oracle's environment
probe records them ([OR-29](../../docs/semantics/oracle.md#or-29)):

- **`WM_SCHEDULED_FOR`** is the job's `Start`, the time the engine launches
  it. The engine launches a job when it is due, after any sleep, retry delay
  or dispatch delay (`scheduled_in_secs`) that it waited out, so this is its
  scheduled time.
- **`WM_END_USER_EMAIL`** is `WM_EMAIL`: a run is started by its user,
  whose address is its end user's, and every step and every job a step
  dispatches inherits it ([WA-4](../../docs/semantics/client-api.md#wa-4)).
- **`WM_ROOT_WORKSPACE`** is the workspace, because windmill-lite has no
  workspace forks.
- **`WM_SCHEDULE_PATH`** and **`WM_TESTED_RUNNABLE`** are empty, because no
  schedule and no CI test starts a job.
- **`WM_RUNNABLE_ID`** is empty, because windmill-lite keeps no script
  hashes. Windmill leaves it empty too for a flow step that it did not
  reduce ([WA-V10](../../docs/semantics/client-api.md#wa-v10)).
- **`BASE_URL`** belongs to the process, not to the invocation: the
  [harness](../../docs/glossary.md#harness) sets it when it spawns the
  process ([internal/harness](../harness/README.md)).

### Host configuration

`Host` holds what an instance and the run's user would supply. A zero field
takes `DefaultHost`'s value, and a zero `PermissionedAs` is
`u/<username>`:

| Field | Variable | Default |
|---|---|---|
| `Workspace` | `WM_WORKSPACE` | `windmill-lite` |
| `Username` | `WM_USERNAME` | `windmill-lite` |
| `Email` | `WM_EMAIL` | `windmill-lite@localhost` |
| `PermissionedAs` | `WM_PERMISSIONED_AS` | `u/<username>` |
| `BaseURL` | `WM_BASE_URL` | `http://localhost:3000`, the stock client's own default for an unset `WM_BASE_URL` ([WA-1](../../docs/semantics/client-api.md#wa-1)) |
| `WorkerGroup` | `WM_WORKER_GROUP` | `default`, the group of the reference configuration's one worker ([OR-29](../../docs/semantics/oracle.md#or-29)); windmill-lite has no worker groups |

The default workspace, username, email and `WM_PERMISSIONED_AS` are
windmill-lite's placeholders, not Windmill's values. A comparison with the
[oracle](../../docs/glossary.md#oracle) masks them, as it masks values that
depend on how a run was submitted
([OR-29](../../docs/semantics/oracle.md#or-29)).

### The object path and the state path

No schedule triggers a windmill-lite run, because schedules are
[out of scope](../../docs/design/0001-equivalence-contract.md#3-out-of-scope).
So the trigger part of both paths is fixed.

`ObjectPath` gives `WM_OBJECT_PATH`
([WA-4](../../docs/semantics/client-api.md#wa-4)), with the trigger part
`manual` and the job's start in epoch milliseconds:

1. a flow step: `<flow path, each / an _>/manual/<step id>/<ms>_<job id>`;
2. otherwise: `<runnable path, each / an _>/manual/<ms>_<job id>`.

`StatePath` gives `WM_STATE_PATH`
([WA-5](../../docs/semantics/client-api.md#wa-5)), with the trigger part
`user`:

1. a flow step: `<flow path>/<step id, or nostep>_user`;
2. a script job with a path: `<script path>/user`, after appending `state`
   to a path that ends with `/`;
3. otherwise: `u/<username>/tmp_state`.

### The token

The facade never attributes a call on the intercepted path by the token
that the call carries, because the stock client captures `WM_TOKEN` when a
script imports it ([WA-31](../../docs/semantics/client-api.md#wa-31)). Over
loopback TCP, where a fresh process is one invocation, the token names the
invocation, so it is random and unguessable.

## Services

`Services` holds the services of one runtime. The facade answers its routes
from it, and the engine reads it and registers its runs with it. A host
gives both the same `*Services`, and sets every field before any run
starts.

| Field | What it is | A nil field means |
|---|---|---|
| `Resources` | The resources a run can read | no resources |
| `Variables` | The variables a run can read, secrets included | no variables |
| `Secrets` | Reveals a secret variable and decrypts an `$encrypted:` value | `LocalSecrets` |
| `Resolvers` | The host secret layer, in order | none |
| `Jobs` | The job registry | one `Registry` per `Services` (`Services.Registry`), made when first needed |
| `UserState` | Flow user state | one `UserStates` per `Services` over whichever `Jobs` it holds, made when first needed |
| `Objects` | The object store | none: the facade answers the object routes 500 `Internal: windmill-lite: no object store is configured` |
| `Dispatcher` | Starts the runs that steps dispatch | none: no dispatch target is in the bundle |
| `Results` | The result cache, which only the engine uses | one `MemoryResults` per `Services` (`Services.ResultCache`), made when first needed |
| `ResourceStore` | The resources that steps write ([The resource store](#the-resource-store)) | no store: no read finds an entry there, and the facade answers the writes 501 |
| `Workspace` | The workspace that the store keys entries under, the runtime's `WM_WORKSPACE` | `DefaultHost`'s workspace |

The facade and the engine that share a `*Services` share its default
registry, user state and result cache. A nil `*Services` has no services of
any kind and keeps no job. Each operation then answers as its own section
says for an absent item: a read of an ordinary missing resource or variable
is a not-found, an existence check `false`, and a flow user state read JSON
`null`. An interpolated read of a `CUSTOM_INSTANCE_DB/` path still answers
its 401 first.

### Resources, variables and secrets

- **Records.** A `Resource` is its type and its value as the workspace holds
  it, with `$var:` and `$res:` strings kept. A `Variable` is its value,
  whether it is a secret, and whether that value is `Encrypted`: ciphertext
  under the workspace key, as a [sync folder](../../docs/glossary.md#sync-folder)
  pulled without `plainSecrets` holds a secret
  ([SY-11](../../docs/semantics/openflow.md#sy-11)).
- **The local default** is `Store`, an in-memory map of resources and
  variables. `LoadData` fills one from a bundle's `data/resources.json` and
  `data/variables.json`, the format that
  [internal/bundle](../bundle/README.md) writes. A bundle never holds a
  secret, so the host supplies secrets.
- **`SetResource` refuses a value that jsonb would refuse**: a NUL escape,
  an unpaired surrogate escape, or a number beyond numeric's range. Windmill
  refuses to create such a resource.
- **Host overrides.** `ResourceChain` and `VariableChain` put services in
  order, and the first that has a path answers for it, so a host's own
  store comes before the bundle's
  ([WA-40](../../docs/semantics/client-api.md#wa-40)).

### Variable updates

`Services.UpdateVariable` answers a step's update of an existing variable
([WA-11](../../docs/semantics/client-api.md#wa-11)) through the variable
service, when that service is a `VariableUpdater`, as `Store` is. A runtime
that owns its store ([wl](../../wl/README.md)) so keeps a step's update for
the runs after it.

- A new value replaces the old one, and is plaintext: the variable is no
  longer `Encrypted`. A new secrecy replaces the old one.
- A missing variable is the 404 `Not found: Variable not found at name
  <path>`.
- A secrecy that differs from the variable's, sent without a value, is the
  400 `Bad request: cannot change is_secret without updating value too`,
  and the variable stays as it was.
- A `VariableChain` updates the first service that has the variable, the
  one that a read answers from. That service must be an updater too.
- A variable service that is not an updater is read-only: the update fails
  with `ErrReadOnlyVariables`, and the facade answers 501.

The texts that windmill-lite writes in place of Windmill's server texts,
here and in every section below, carry no source location: a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).

### Secrets

`Secrets` reveals the plaintext of a secret variable and decrypts an
`$encrypted:` value. windmill-lite never decrypts Windmill's ciphertext
([JE-43](../../docs/semantics/expressions.md#je-43)).

- **`LocalSecrets`**, the default, reveals a secret held as plaintext: a
  folder pulled with `plainSecrets`, or a secret that a host passed in. It
  fails ciphertext and `$encrypted:` values with `ErrSecretUnavailable`.
- **A value that windmill-lite cannot decrypt** is answered as Windmill
  answers every value that it cannot decrypt, with one text whatever the
  cause ([WA-11](../../docs/semantics/client-api.md#wa-11),
  [JE-42](../../docs/semantics/expressions.md#je-42)). The texts are in
  [Errors](#errors). A step's `$var:` argument quotes the read's text as
  its cause.
- **A host's secret service** answers with its own errors.
- **Decrypted text that is not JSON** fails with `Internal: Failed to
  decrypt '$encrypted:' value: <message>`, in the JSON library's words.
- **The host secret layer.** After interpolation, each resource value,
  nested ones included, and each secret's plaintext passes through
  `Resolvers` in order, and a resolver may replace markers of the host's
  own. A raw read does not pass through them. windmill-lite defines no
  marker of its own.

### Values windmill-lite seals

A flow step stores a value computed from a secret `flow_env` entry as an
`$encrypted:` value under its root job's key
([JE-38](../../docs/semantics/expressions.md#je-38)). windmill-lite has no
workspace key, so `SealForRoot` seals such a value with a key of its own:

- one random key per process, and a key for each root job derived from it
  with HKDF-SHA256;
- AES-256-GCM with a random nonce, so the sealed text differs every time;
- written as `$encrypted:` and standard padded base64.

`ResolveArgs` opens such a value for a job whose `WM_ROOT_JOB_ID` is the root
it was sealed for, in the same process, and substitutes the plaintext parsed
as JSON. It hands every other `$encrypted:` value to `Secrets`, a value
sealed for another root included. With `LocalSecrets`, that value fails as
one Windmill cannot decrypt.

`SecretEntry` reports whether a `flow_env` entry is secret: a literal
`$encrypted:` string, or a `$var:` or `$jsonvar:` reference to a variable
stored as secret (`SecretVariable`). A `$res:` reference, a missing variable
and any other value are not secret.

### Per-run providers

`Providers` holds the providers that a run may choose for itself:
resources, variables, secrets and value resolvers, chosen from the run's
data. A Go host sets them with the providers hook
([wl](../../wl/README.md#providers), the ADR
[Host hooks](../../docs/design/0008-host-hooks.md)).

`Services.WithProviders` returns services that answer with a run's
providers in place of their own, where the run sets them. They share the
job registry, flow user state, object store, dispatcher, result cache and
resource store, so a run with its own providers is still one of the
runtime's runs, whose jobs the shared registry knows. Fields set on the
original services later are not seen.

### The read cache

`ReadCache` is one run's read cache, which a host turns on. It sits in front
of a run's resource and variable providers, and asks them once per path, at
the run's first read of it. It answers every later read of that path in the
run from that answer, a not-found included, but not an error: the next read
asks again.

Windmill reads a resource or variable again for each job and each call, so a
value that changes during a run reaches the run's later steps there, and
here only the next run. This is a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations)
that a host opts into.

- The cache keeps the value as stored, so the walker still interpolates it
  at each read. A `$WM_*` string resolves for the job that reads it, and a
  nested reference is read through the cache.
- Reads of one path that overlap share one provider read.
- `UpdateVariable` updates through the provider and drops the path's answer,
  so the run's next read asks again.
- The cache never holds the resource store's entries
  ([The resource store](#the-resource-store)).

## The resource store

`Services.ResourceStore` is the runtime's
[resource store](../../docs/glossary.md#resource-store): it keeps the
resources that steps write through the job API, resources of type
[`state`](../../docs/glossary.md#state), which the stock client's `setState`
writes ([WA-13](../../docs/semantics/client-api.md#wa-13)). Its entries are
keyed by workspace (`Services.Workspace`, or the default host's) and path.
A nil store serves no writes, and the facade answers them 501.

- **The read order.** Every read asks the providers first, then the store:
  `RawResource`, `InterpolatedResource`, `ResourceExists`, a `$res:`
  reference, `flow_env` and `resource()`. A provider's resource shadows a
  stored entry at its path.
- **No cache.** No cache holds the store's entries, the read cache included,
  so a read sees the latest write.
- **The contract.** Each operation is atomic per workspace and path, a value
  comes back byte for byte, and an error is never reported as a missing
  entry. The `ResourceStore` doc comment states the whole contract, and
  [wl/storetest](../../wl/storetest/README.md) checks a store against it.
- **The stores.** `MemoryResourceStore` is the store that a `wl` runtime
  makes unless the host passes one. The Redis store of
  [wl/redisstore](../../wl/redisstore/README.md) lets runtimes in several
  processes share one. A host can pass its own.
- **A failure** of the store fails the read or the write with `Internal:
  windmill-lite: the resource store failed: <err>`, an `*Error` marked
  `StoreFailure`. The facade answers it 500 and records it as a warning. A
  `$res:` argument keeps that error rather than reporting a missing resource.
- **The report function.** A read under `WithStoreFailureReport`'s context,
  a run's own, also calls that context's report function. The facade's calls
  take it off (`WithoutStoreFailureReport`), because they answer the failure
  themselves.

### The writes of state

The writes check the request as Windmill does
([WA-13](../../docs/semantics/client-api.md#wa-13)). An update and a delete
pass the type `state` to the store, which changes only an entry of that
type, in one atomic step with its check. So a host that shares the store and
puts another type at the path is never overwritten.

| Write | Checks, in order | Answer |
|---|---|---|
| `CreateStateResource` (`createResource`) | the path's form; the value; whether a provider or the store holds the path | `Bad request: Invalid path: …` (400); `SqlErr: error returned from database: <reason>` (400) for a value jsonb refuses; `Bad request: Resource <path> already exists` (400), changing nothing |
| `UpdateStateResource` (`updateResourceValue`) | a provider holds the path; the stored entry's type; the value | `ErrProvidedResource` or `ErrNotStateResource` (the facade answers 501); `Not found: Resource <path> not found` (404), creating nothing; `SqlErr: error returned from database: <reason>` (400) for a value jsonb refuses |
| `DeleteStateResource` (`deleteResource`) | a provider holds the path; the stored entry's type | `ErrProvidedResource` or `ErrNotStateResource` (501); `Not found: Resource not found at name <path>` (404) |

- **The path of a create** is `u`, `f` or `g`, then at least two segments.
  Every segment is not empty, and is made of letters of any script, ASCII
  digits, `_` and `-`.
- **No value** in the request stores JSON `null`.
- **A value is stored as jsonb would store it.** Its numbers are spelled as
  each route stores them: an update stores `1e3` as `1000.0`, and a create
  stores it as `1000`.
- **A race.** When the store reports no state after the first check found
  one, the write reads the path again, and answers 404 or the other type's
  501.

### The memory store

`MemoryResourceStore` holds its entries in one map under one mutex. An entry
is never changed in place, and bytes are copied in and out, so a read never
holds the lock while it copies.

The memory store's entries live in one process's memory, where Windmill keeps
them in its database (a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations)).
Its lifetime depends on who made it:

- **A runtime's own store.** A runtime that the host gave no store makes one,
  and `Close` releases it with `ReleaseMemoryResourceStore`, which drops the
  entries under the lock. No other runtime sees them.
- **A host's store.** A store that the host made with
  `NewMemoryResourceStore` and passed to several runtimes is shared by them,
  and lasts as long as the host holds it; no runtime releases it
  ([wl](../../wl/README.md#the-resource-store)).

After a release:

- An operation that takes the lock afterwards fails with `ErrStoreReleased`,
  or with its canceled context's error, which comes first.
- An operation that took the lock before completes as usual: a `Get` that
  read its entry returns a copy of it.

## The job registry and flow user state

`Jobs` is the job registry. windmill-lite's job ids are its own, and job
lookups answer from the runs that a runtime started, not from a job table (a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations)).
Its local default, `Registry`, keeps each `Job`: its id, its kind
(`JobFlow` or `JobScript`), its effective root
([WA-V1](../../docs/semantics/client-api.md#wa-v1)), its runnable path and,
for a script job, its job variables.

### What the registry keeps

- **Who registers what.** The engine registers each flow run it starts,
  under its effective root (itself for a run that nothing dispatched), from
  start to end. It also registers each child flow of the run
  (a loop iteration's, a branch's, a subflow's) while it runs, with its
  runnable path ([internal/engine](../engine/README.md)).
- **The facade** registers each invocation, a script job, from `Register` to
  the end of its registration ([internal/platform/facade](facade/README.md)).
  The script job's parent flow, innermost flow scope and root become known
  with it, as flow jobs of its root that are live while the root is, unless
  the engine registered them first.
- **How long.** A job is live until it ends, and stays known while its root
  lives, so a step's job can be looked up after it ended. Once the root has
  ended, the registry keeps only what the next items say, and the tree's
  user state is forgotten.
- **A step abandoned at the run's end** stays live until it ends, and the
  scopes it runs in stay known with it. A step that times out is abandoned, not killed (a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations)).
- **Ended jobs.** `Registry` keeps the id, kind, root and path of each job of
  an ended tree, without a script job's variables, so the root lookup still
  answers for it. It keeps the latest 4096 such jobs, and forgets older
  ones, whose ids are then unknown. Registering a job again makes it live
  again.

Windmill keeps a completed job's row until its job retention removes it, 30
days by default ([WA-19](../../docs/semantics/client-api.md#wa-19)).
windmill-lite bounds the memory by count instead.

### Lookups

| Method | What it answers | When it fails |
|---|---|---|
| `Services.RootJob` | `getRootJobId`: the effective root of a known job, live or ended | false for an unknown id |
| `Services.JobEnv` | The job variables of a script job whose tree has not ended, the step ended or not | false otherwise; the ended jobs that the registry keeps have none |
| `Services.ReadJobEnv` | The job that a resource read's `job_id` names: a live script job of the path's workspace, whose variables the read's `$WM_*` strings resolve from (`APIEnv`), as Windmill looks it up among the queued and running jobs ([WA-12](../../docs/semantics/client-api.md#wa-12)) | `Not found: Job not found at name <id>` (404) for an unknown id, an ended job or another workspace's job; `ErrNoJobVariables` for a live flow job |
| `Services.JobRegistry` | The registry that the engine and the facade register with: `Jobs`, or the services' own | — |

### Job reads of a live run

`Services.GetJob` answers `GET jobs_u/get/{id}` and `Services.GetJobArgs`
answers `GET jobs_u/get_args/{id}`
([WA-19](../../docs/semantics/client-api.md#wa-19)), while the job's root
lives, the job running or completed.

- **How a job reaches its run.** A run registers its flow job with its own
  id as `Job.Run` and itself as `Job.View`, a `JobView`. Every other job of
  the run names the run in `Run`, so `Registry.Job` gives it that view.
- **What the view supplies.** `GetJob` answers with the view's
  `JobRecord(id)`, and `GetJobArgs` with its `JobArgs(id)`. A `JobView`
  implementer, the engine's run, renders both.
- **No copy.** The registry keeps the view, a reference to the run, never a
  copy of its state, and drops it with the tree, as it drops a job's
  variables.
- **An unknown id** fails with the 404 `Not found: Job not found at name
  <id>` or `Not found: Job Args not found at name <id>`.
- **A job whose root has ended** fails with `ErrEndedRun`, a `NotServed`
  that the facade answers 501, where Windmill answers from the job's row (a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations)).
  The root lookup still answers for it.
- **A job that no run of the runtime holds**, such as one in a host's `Jobs`
  without views, fails with a `NotServed` too.
- **`Services.ResultPoll`** answers the result polls that the facade
  answers with its `ResultPollsForAllJobs` option
  ([WA-V2](../../docs/semantics/client-api.md#wa-v2)) from the run's
  `JobPoller`, the engine's run: the record's state, and a completed job's
  whole result in jsonb's text output. It looks the id up once, fails as
  `GetJob` fails, and reports an unknown id as unknown.
- **`Services.FlowUserStates`** lists a live flow job's user state as its
  flow status holds it, in jsonb's key order, when the services'
  `FlowUserState` can list maps (`FlowUserStateLister`, which `UserStates`
  implements).

### Flow user state

Flow user state is a key/value map per flow job, which the stock helpers
address by their effective root
([WA-14](../../docs/semantics/client-api.md#wa-14)).

- **A read** (`Services.FlowUserState`) gives the value stored at the key.
  It gives JSON `null` for a missing key, for a job that is not a flow job,
  for an unknown job, and once the run has ended, as the API answers once a
  root has left the queue.
- **A write** (`Services.SetFlowUserState`) changes a key of a live flow job
  only. Otherwise it fails with `ErrFlowJobNotFound`, the 404 `Not found:
  Flow job not found`. A body that is not JSON, or that jsonb refuses, is a
  400 with windmill-lite's text.
- **The stored form** is what a read gives back, which the oracle recorded:
  jsonb's key order, and numbers as Windmill's JSON library writes them
  (`1e3` as `1000.0`, `1.50` as `1.5`).
- **The lifetime.** The local default, `UserStates`, keeps each map with its
  job's effective root, read from whichever `Jobs` the services hold. It
  forgets a run's maps when the run's registration through
  `Services.JobRegistry` ends and the registry no longer reports it live.
- **Faithful where a step can look.** This lifetime matches every access the
  stock helpers make during the run. A read or a write by id after the run
  gets `null` or the 404, as Windmill answers once the root's completion is
  committed ([Q-WA-10](../../docs/semantics/client-api.md#q-wa-10)).
- **The sweep.** With a registry that knows runs registered elsewhere,
  `UserStates` stays bounded: each time its number of maps has doubled since
  the last sweep (from 64), it drops the maps of jobs that the registry no
  longer reports live.
- **A host's own `FlowUserState`** gets only the reads and writes of live
  flow jobs, and keeps its own lifetime.

## The object store

`ObjectStore` keeps files by storage and key
([WA-16](../../docs/semantics/client-api.md#wa-16)): what the job helpers
`writeS3File` and `loadS3File` reach, for explicit keys. An object that
names no storage is in `_default_` (`DefaultStorage`).

Its local default, `DirObjects`, keeps each object in a directory as one
file. `NewDirObjects` creates the directory when it does not exist.

- **The file name** is the SHA-256 of the object's storage and key, so no
  key names a path outside the directory, whatever its slashes or dots.
- **The file** holds one version of the object, whole: a line of JSON
  metadata (its storage, key, content type and disposition), then its
  content.
- **A `Put`** writes the new version to a temporary file and renames it into
  place. Concurrent readers and writers see only versions that a `Put`
  wrote, content and metadata together, and the last rename wins. A `Put`
  that fails leaves the version before it as it was.
- **A file whose metadata names another object** is refused.
- **No parity claim.** `DirObjects` claims no parity with a Windmill server
  for what the client contract leaves open: keys that a server generates, a
  resource's own bucket, deletion, quotas and errors
  ([Q-WA-3](../../docs/semantics/client-api.md#q-wa-3)).

## The dispatcher

`Dispatcher` starts the runs that steps dispatch with `runScript*Async`,
`runFlowAsync` and their synchronous forms
([WA-15](../../docs/semantics/client-api.md#wa-15)), and answers the result
polls for them with a `JobResult`
([WA-V2](../../docs/semantics/client-api.md#wa-v2)).

- **A `Dispatch`** holds the target (a script's or a flow's path, or a
  script's hash, by `DispatchKind`), the arguments, the query's
  `parent_job`, `root_job`, `tag` and `scheduled_in_secs`, and the caller's
  job variables.
- **A `JobResult`** says whether the job has started and, once it has
  completed, whether it succeeded and its result as stored JSON text.
- **A target that is not a root of the bundle** fails with
  `NotInBundleError`. Its text, `dispatch target not in bundle: <target>`,
  is what the facade answers as a 501, which fails the step
  ([restrictions](../../docs/design/0001-equivalence-contract.md#5a-restrictions)).
- **The default** is `engine.Dispatcher`, which runs a bundle's roots as
  separate runs of the same runtime. A host's dispatcher may route elsewhere.

## The result cache

`ResultCache` keeps the results of the jobs that run with a `cache_ttl`
([FX-40](../../docs/semantics/execution.md#fx-40)). `Get(key, now)` answers a
value stored under the key that expires after `now`, and `Put(key, value,
expires)` stores one, replacing what was there.

- **The engine owns the rest:** the key, which TTL applies, what a value
  holds and when a job stores one. A store keeps opaque bytes.
- **The run's clock.** A store compares expiries with the time that the
  engine gives it, its run's clock, never its own. A host can replace that
  clock ([Host hooks](../../docs/design/0008-host-hooks.md#the-hooks)).
- **Per process.** The cache is one per runtime unless the host plugs in a
  shared store, where Windmill's lives in its database (a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations)).

The local default, `MemoryResults`, keeps entries in memory, one per
`Services`, with a heap of their expiries:

- Every `Get` and `Put` drops, earliest first, each entry that expired by the
  latest time that a `Get` gave. So it holds only the entries still live
  then, whatever their number.
- The heap keeps the older expiries of keys stored again until they come up.
  A `Put` rebuilds it from the live entries once it holds more than 64
  expiries and more than twice their number.
- The heap's array keeps no key it has given up: a popped slot is cleared,
  a rebuild clears the slots past the live expiries, and a heap that a drain
  empties gives its array up.
- A hit compares an entry's expiry with its own `Get`'s time. Only the
  reclaiming uses the latest time. So with clocks that disagree, such as the
  clock hook's record mode, which runs ahead of the wall clock, an entry may
  go before its own run's clock reaches its expiry: a miss, never a stale
  hit.

## The interpolation walker

One walker implements Windmill's three ways of resolving a resource's value,
and keeps them apart: the raw read, the client's interpolated read, and
worker argument resolution
([WA-40](../../docs/semantics/client-api.md#wa-40)):

| Contract | Method | What it resolves |
|---|---|---|
| (a) raw read, `resources/get_value` | `RawResource` | nothing |
| (b) client interpolated read, `resources/get_value_interpolated` ([WA-12](../../docs/semantics/client-api.md#wa-12)) | `InterpolatedResource`, `InterpolatedResourceOf` | `$var:`, `$jsonvar:`, nested `$res:`; `$WM_*` only with a job's variables |
| (c) worker argument resolution ([JE-41](../../docs/semantics/expressions.md#je-41), [JE-42](../../docs/semantics/expressions.md#je-42), [WR-5](../../docs/semantics/languages.md#wr-5)) | `ResolveArgs` | `$var:`, `$jsonvar:`, `$res:` (a read of contract (b) with the job's variables), `$encrypted:`, `$<NAME>` |

### Limits

Both walkers of contracts (b) and (c) share these rules:

| Rule | Value |
|---|---|
| Depth at which the walk fails, before it looks at the value there | 50 |
| Depth at which an array is walked | at most 2 |
| Items of an array that is walked | at most 1000 |
| What `$var:` or `$jsonvar:` substituted | never walked |
| A string that is replaced | only one that starts with a marker |

### Contract (b): the interpolated read

- **Depth.** The walk starts at depth 0. A nested `$res:` loads at the
  referring string's depth plus one, and its path needs a `/`.
- **`$WM_*` strings** resolve only from a job's variables. Without a job,
  they stay literal, as the stock clients' reads leave them.
- **A read that names its job by id** (`InterpolatedResourceOf`, the
  facade's `job_id`) looks the job up through a `JobLookup` only when the
  walk meets a `$WM_*` string, at each one. A value without one reads as if
  no job had been named, and a failed lookup fails the read with its error
  ([WA-12](../../docs/semantics/client-api.md#wa-12)).
- **The job's variables** are `APIEnv` of the job's process environment:
  the same, except that `WM_ROOT_JOB_ID` is the job's stored root in place
  of its effective root. So a step directly in a top-level flow sees `""`
  there ([WA-V3](../../docs/semantics/client-api.md#wa-v3),
  [WA-V1](../../docs/semantics/client-api.md#wa-v1)).
- **`CUSTOM_INSTANCE_DB/`.** An interpolated read whose own path starts with
  `CUSTOM_INSTANCE_DB/` answers 401 before anything else
  ([WA-12](../../docs/semantics/client-api.md#wa-12)). Windmill refuses that
  path to a caller with a job's token, and every caller windmill-lite serves
  is a job: on the route, through `resource()` and through a `$res:`
  argument. The raw read and a nested `$res:` look the path up as any other.
- **Its errors** are the API's `Error`s ([Errors](#errors)).

### Contract (c): worker argument resolution

- **Which arguments.** The walker walks a top-level argument only when its
  text holds `$var:`, `$jsonvar:`, `$res:` or `$encrypted:` anywhere. It
  keeps any other argument as it is.
- **What it substitutes.** A string that starts with a marker becomes the
  variable (a string), the variable parsed as JSON, the resource's value
  read through contract (b) with the job's `APIEnv`, or the decrypted value.
  `$<NAME>` becomes the job's reserved variable `NAME`, from the job identity
  that the engine passes, when the job has it.
- **A nested `$res:`** starts a depth counter of its own.
- **`flow_env`** is resolved the same way
  ([JE-38](../../docs/semantics/expressions.md#je-38)).
- **Its failures** fail the job before its script runs ([Errors](#errors)).
  A missing variable or resource, an invalid `$res:` path and the depth
  limit name the innermost object key, or the argument.

## JSON as Windmill's backend writes it

A value that the walker parsed is written out again as Windmill's backend
JSON library writes it (`json.go`,
[FX-42](../../docs/semantics/execution.md#fx-42)). The engine writes a loop's
result and the library's error texts with the same code (`Reprint`,
`FormatDouble`).

- **Keys** keep their order. A repeated key keeps its first place with its
  last value.
- **An integer** stays exact when a signed or an unsigned 64-bit integer
  holds it. Every other number is a double in the library's notation:
  `1000.0`, `1e+21`, `1.2345678901234568e+29`, and plain decimals for a
  decimal exponent from -5 to 15. `-0` is `-0.0`.
- **Strings** have minimal escapes, and the text has no whitespace.
- **A parse failure** is in the library's words, with its line and column,
  such as `unexpected end of hex escape at line 1 column 14`.
- **Nesting.** A value nests at most 127 deep: an array or object opened at
  depth 128 fails.
- **A resource's value** is read in jsonb's stored form first, as the API
  reads it from its column.
- **`JSONSyntaxError`** gives the message of the 400 that Windmill's web
  framework answers for a request body that is not JSON, which the facade
  sends after `Failed to parse the request body as JSON: `.
  `WriteBodySyntaxError` does the same for a resource write, except inside
  the top-level member `value`, which the body parser keeps as raw text: a
  lone surrogate escape there is the database's error, not the parser's.

## Errors

`Error` is a variant of Windmill's API error type
([WA-42](../../docs/semantics/client-api.md#wa-42)): `HTTPStatus` gives its
status, and `Body` its display string, which the facade sends as a
`text/plain` body. The package raises only `NotFound`, `NotAuthorized`,
`BadRequest`, `SqlErr` and `InternalErr`. The facade can render every
variant that `errors.go` maps:

| Variants | Status | Body |
|---|---|---|
| `NotFound` | 404 | `Not found: <message>` |
| `NotAuthorized` | 401 | `Not authorized: <message>` |
| `PermissionDenied`, `RequireAdmin` | 403 | `Permission denied: <message>`, `Require Admin privileges for <message>` |
| `BadRequest`, `QuotaExceeded`, `SqlErr` | 400 | `Bad request: <message>`, `Quota exceeded: <message>`, `SqlErr: <message>` |
| `AIError` | 400 | The message alone |
| `BadGateway` | 502 | `Bad gateway: <message>` |
| `InternalErr`, `InternalErrLoc` | 500 | `Internal: <message>` |
| `BadConfig` | 500 | `Bad config: <message>` |
| `ConnectingToDatabase` | 500 | `Connecting to database: <message>` |
| `MetricNotFound` | 500 | `Metric not found: <message>` |
| `IoErr`, `Utf8Err`, `UuidErr`, `SerdeJson`, `HexErr`, `ExecutionRawError` | 500 | The variant's name, `: `, then the message, such as `IoErr: <message>` |
| `DatabaseMigration` | 500 | `Migrating database: <message>` |
| `Anyhow` | 500 | `Error: <message>` |
| `WacSuspended` | 500 | `WAC job suspended: <message>` |
| `FindPythonError` | 500 | `Find python error: <message>` |
| `ArgumentErr` | 500 | `Problem with arguments: <message>` |
| `ExecutionErr`, `ResultTooLarge`, `AlreadyCompleted` | 500 | The message alone |
| `Generic` | its own | Its own message |
| An unknown variant | 500 | `Internal: windmill-lite: unknown error variant <name>: <message>` |

The package's own errors:

| Error | When | What the caller sees |
|---|---|---|
| `Not found: Resource <p> not found` (404) | A read's own resource is missing | The facade's answer; a `$res:` argument's cause |
| `Not found: Resource not found at name <p>` (404) | A nested `$res:` is missing | as above |
| `Not found: Variable <p> not found` (404) | A variable read or `$var:` finds no variable | as above |
| `Not authorized: CUSTOM_INSTANCE_DB cannot be resolved from a job token ($WM_TOKEN)` (401) | An interpolated read of a `CUSTOM_INSTANCE_DB/` path | as above |
| `Internal: Maximum resource/variable interpolation depth (50) exceeded; …` (500) | The depth limit of a read | as above |
| ``Internal: Maximum resource/variable interpolation depth (50) exceeded for `<name>`; …`` (500) | The depth limit of contract (c) | The job fails before its script runs |
| `Internal: Failed to parse $jsonvar value as JSON: <message>` (500) | A `$jsonvar:` that is not JSON | The facade's answer; a `$res:` argument's cause; a `$jsonvar:` argument fails the job |
| `Internal: Invalid resource path: <p>` (500) | A nested `$res:` path without `/` | as above |
| ``Not found: Variable <p> not found for `<name>`: <cause>``, ``Not found: Resource <p> not found for `<name>`: <cause>`` (404) | Contract (c) cannot read a variable or resource | The job fails before its script runs |
| ``Internal: Argument `<name>` is an invalid resource path: <p>`` (500) | Contract (c) meets an invalid `$res:` path | as above |
| `Internal: Error decrypting variable <p>: Internal: Could not decrypt value. The value may have been encrypted with a different key.` (500) | A secret variable that no secret service can reveal | The facade's answer; a `$var:` argument's cause |
| `Internal: Could not decrypt value. …` (500) | An `$encrypted:` argument that no secret service can decrypt | The job fails |
| `Internal: windmill-lite: a value resolver replaced the secret variable <p> with a value that is not a string` (500) | A host resolver turns a secret's plaintext into a value other than a string | The facade's answer; a `$var:` argument's cause |
| `Internal: windmill-lite: a value resolver answered <p> with text that is not JSON: <error>` (500) | A host resolver's answer for a resource is not JSON | as above, for `$res:` |
| `Internal: windmill-lite: the resource store failed: <err>` (500, `StoreFailure`) | The resource store fails | The facade's 500 and a trace warning; a `$res:` argument keeps it |
| `ErrSecretUnavailable` | `LocalSecrets` meets ciphertext or an `$encrypted:` value | The services answer it as a value that cannot be decrypted |
| `ErrReadOnlyVariables` | An update through a variable service that takes no writes | The facade answers 501 |
| `ErrProvidedResource`, `ErrNotStateResource` | An update or a delete of a provider's resource, or of a stored entry of another type | The facade answers 501 |
| `ErrFlowJobNotFound` (`Not found: Flow job not found`, 404) | A flow user state write whose job is not a live flow job | The facade's 404 |
| `ErrNoJobVariables` | A resource read's `job_id` names a live flow job | The facade answers 501 |
| `ErrEndedRun` (`NotServed`) | A job read of a job whose run has ended | The facade answers 501 |
| `NotServed` | A job read that no live run holds, or a record windmill-lite does not reproduce | The facade answers 501 |
| `NotInBundleError` | A dispatch target that is not a root of the bundle | The facade answers 501, which fails the step |
| `ErrStoreReleased` | An operation on a runtime's own memory store after that runtime closed and released it | A store failure, never a missing entry |
| The JSON syntax errors (`JSONSyntaxError`, `WriteBodySyntaxError`) | A request body that is not JSON | The facade's 400 |

## Decisions where the reference is silent

| Question | The code's choice | Why | Pinned by |
|---|---|---|---|
| Which workspace, user and email does a job get with no host configuration? | `DefaultHost`'s placeholders | No instance or user exists to take them from; the oracle comparison masks them | `TestIdentityEnv` |
| How long does the registry remember ended jobs? | The latest 4096, by count | Bounds the memory of a long-lived runtime; Windmill keeps rows for 30 days | `TestRegistryEndedBound` |
| When is a run's flow user state forgotten? | When the run's registration ends and the registry no longer reports it live; a sweep each time the maps double | Matches every access a step can make; stays bounded with a host's registry | `TestFlowUserState`, `TestFlowUserStateSweepsDeadJobs` |
| How does the local object store keep files? | One file per object, named by a SHA-256, written by rename | No key can escape the directory, and readers see whole versions | `TestDirObjects`, `TestDirObjectsConcurrentVersions` |
| What does the object store promise beyond explicit keys? | Nothing: no parity claim | The client contract leaves it open | code only |
| When does the memory result cache drop an entry? | At the latest time any `Get` gave | Bounded memory; a disagreeing clock gives a miss, never a stale hit | `TestMemoryResultsReclaim` |
| Which paths may a create use? | The narrowest rule the recorded cases allow: letters of any script, ASCII digits, `_`, `-` | A non-ASCII digit is in no recording | `TestValidResourcePath` |
| How are a write's numbers spelled? | One rule per route, fitted to the recorded spellings | Numbers no case records follow the same rules | `TestStoredValueSpellsRecordedNumbers`, `TestRespellNumber` |
| With which key does windmill-lite seal a secret-derived value? | A random key per process, a derived key per root job, AES-256-GCM | There is no workspace key | `TestSealForRoot` |
| May a store failure read as a missing resource? | No: it is a 500, and a `$res:` argument keeps it | A `getState` in an outage throws rather than reading a state never written | `TestStoreFailure`; `wl`'s `TestResourceStoreOnBun` |
| Does a run see a value that changes during it? | Yes by default. With the read cache that a host turns on, a run answers a path's later reads from its first successful read. A provider's error is not kept, and an update of a variable drops its cached value, so those reads ask the provider again ([The read cache](#the-read-cache)) | A host whose providers are remote pays a round trip per read; a named deviation, in the ADR [Platform interface, the `wmill` shim, and hybrid dispatch](../../docs/design/0007-platform-interface.md) | `TestReadCache` |
| How long do the entries of the memory store live? | A runtime's own store until the runtime closes; a host's store as long as the host holds it | The memory store needs nothing beyond the runtime; a host that needs longer lives passes a store of its own ([wl](../../wl/README.md#the-resource-store)) | `TestReleaseDropsTheEntries`; `wl`'s `TestCloseReleasesItsOwnStore`, `TestResourceStoreOnBun` |
| May the job API write a provider's resource, or another type? | No: an update or a delete is refused (the facade's 501), and a create at a provider's path is the 400 `already exists` | A provider's resources are configuration; windmill-lite writes only `state` | `TestStateWrites`, `TestStateWritesNeverChangeAReplacement` |

## Tests

The package's tests need no environment variable and no Bun.
`go test ./internal/platform` needs no cgo; the race detector of step 2
does.

### Run the tests

1. From the repository root, run `go test ./internal/platform`.
2. To check for data races as CI does, run
   `go test -race ./internal/platform`.

CI runs every package's tests with `-race` in its Go jobs
([docs/ci.md](../../docs/ci.md#the-go-jobs)).

### Test files

| File | Tests | What they check |
|---|---|---|
| `identity_test.go` | `TestStatePath`, `TestIdentityEnv`, `TestNewToken` | The state path's branches; the variables `Env` sets, with and without a host configuration; the token's form |
| `interpolate_test.go` | `TestInterpolationCases` | `testdata/interpolation.json`: the walker's cases through the three contracts. Each case names its catalogue case or finding in its `spec` field |
| | `TestSecrets`, `TestCustomInstanceDB`, `TestChains`, `TestAPIEnv` | The secret layer (plaintext, ciphertext with and without a host's `Secrets`, a host's resolvers); `CUSTOM_INSTANCE_DB/`; the chains; `APIEnv` |
| | `TestInterpolatedReadJobLookup` | A read's job is looked up only for a `$WM_*` string, nested resources included, and a failed lookup fails the read |
| | `TestUpdateVariable`, `TestWithProviders` | The variable updates, a chain's included, and their refusals; the per-run providers and what they share |
| `rootseal_test.go` | `TestSealForRoot` | Sealed values: standard base64 that differs every time, opened for their own root only, a tampered one refused, and the host's `Secrets` asked for any other value |
| | `TestSecretEntry` | Which `flow_env` entries are secret, and that a failed variable lookup is an error |
| `readcache_test.go` | `TestReadCache` | One provider read per path, through the walker, a variable read or a missing path. An error is not kept. 16 overlapping reads share one, and a reader whose context ends leaves. An update is read again |
| | `TestReadCacheResolvesPerJob`, `TestReadCacheReadOnly` | `$WM_*` strings resolved for each of two jobs from one read; an update over a read-only provider fails |
| `statewrites_test.go` | `TestStoredValueSpellsRecordedNumbers`, `TestRespellNumber`, `TestRespellNumbersLeavesStrings` | Both routes' number spellings, on every recorded number and on others; digits inside strings and keys left alone |
| | `TestValidResourcePath` | The path check, on every recorded path |
| | `TestStateWrites`, `TestStateWritesNeverChangeAReplacement` | The recorded answers of create, update and delete; a provider's path; an entry of another type |
| | `TestStoreReadOrder`, `TestStoreFailure`, `TestWithProvidersSharesTheStore`, `TestReleaseDropsTheEntries` | The read order behind the read cache; a failing store on every read and write; the store that a run's own providers share; a release dropping the memory store's entries |
| `jobs_test.go` | `TestRegistryLifetime`, `TestRegistryOutlivedRoot`, `TestRegistryUnregisteredRoot` | A run and its jobs, the scopes a script job names, a job that ended while its run lives, a step that outlives its run, and jobs of a root never registered |
| | `TestRegistryEndedBound` | The bound on ended jobs: the oldest forgotten, and an id registered and ended again kept by its later end |
| | `TestFlowUserState`, `TestFlowUserStateWithAHostsJobs`, `TestFlowUserStateSweepsDeadJobs`, `TestFlowUserStateWithAHostsStates` | Flow user state during and after the run, its stored form, the 404 and the 400; with a host's `Jobs`, with the sweep, and with a host's `FlowUserState` |
| | `TestReadJobEnv`, `TestServicesRegistry` | The job that a read's `job_id` names; the services' one registry, shared by callers that first ask at once |
| `jobreads_test.go` | `TestJobReadsDuringARun` | Each job of a run answered by its run's view, the 404 texts, and every job once the run has ended, which the root lookup still answers |
| | `TestEndedRunIsReleased`, `TestJobReadsOfADispatchedRun` | An ended run keeps no view and no variables; a dispatched run in another run's tree answers by its own view until the root ends |
| | `TestJobReadsOfConcurrentRuns`, `TestFlowUserStatesList` | Eight runs at once, each reading its own jobs and the others' runs; a flow job's user state listed in jsonb's key order |
| `objects_test.go` | `TestDirObjects`, `TestDirObjectsConcurrentVersions` | Storages kept apart, metadata, a replacing write, a missing object, keys that would escape the directory, no temporary file left; concurrent writers and readers see only whole versions |
| | `TestDirObjectsFailedPut`, `TestDirObjectsForeignFile` | A failed `Put` leaves the committed version (skipped when the tests run as root); a foreign file is refused |
| `results_test.go` | `TestMemoryResults`, `TestMemoryResultsReclaim`, `TestMemoryResultsReleasesKeys` | A value until its expiry and not at it, a replacing write, copies; a burst of expired entries reclaimed; a heap that stays bounded and holds no key it gave up |
| | `TestServicesResultCache`, `TestMemoryResultsConcurrent` | The services' one default and a host's cache; concurrent writers and readers |
| `json_test.go` | `TestFormatDouble`, `TestParseAndWrite`, `TestParseErrors` | The double notation; parse and write; the parse errors and their positions, such as `[1,` (a value due) and `{"a":1` (unclosed); the nesting limit |
| | `TestStringFastPathMatchesPerChar` | The string reader's ASCII fast path gives the same value, or the same error, as the per-character path |
| `data_test.go` | `TestLoadData` | `LoadData` on `testdata/bundle/data/`, a bundle's `data/` in [internal/bundle](../bundle/README.md)'s format |

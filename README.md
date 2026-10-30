# windmill-lite

windmill-lite runs the flows and scripts of a [Windmill](https://www.windmill.dev)
folder with Windmill's semantics, but without Windmill's scheduling: no job
queue, no job per step, and no worker pickup.

**Current state:** first implementation milestones. The engine core runs
flows of Bun steps in sequence from a compiled program, with their failure
modules and for loops, whose steps reach a platform facade that serves
resources and variables, and with their sleeps, timeouts and retries; the public `wl` package opens
a built bundle and runs its flows, with the host's hooks as Go callbacks
and the host-provided sites its contract names, and the budgets, host
plugins and adapters come in the next milestones. The repository holds
a Go module, the `windmill-lite` command, which builds, verifies and runs
bundles and serves host mode,
CI, a
documentation gate, the [semantics reference](docs/semantics/README.md) (the
Windmill `v1.767.0` behaviour that windmill-lite must match, written as
numbered findings with conformance cases), and the
[design](docs/design/README.md) (decision records for the architecture, the
equivalence contract, the package layout and the implementation milestones).
Forty-seven of the fifty-six milestones in ADR 0012's tables have landed,
with follow-ups and spec corrections between them. F4 gives the
[`windmill-lite` command](cmd/windmill-lite/README.md) its first work, in
[internal/bundle](internal/bundle/README.md): `build` turns a sync folder
and a host contract into a bundle (the compiled program, the bundled Bun
scripts and the folder's data) with a manifest that records the digest of
every file and of every input, and whose own digest identifies the bundle;
`verify` checks a bundle against a release manifest; `selfcheck` starts the
expression engine. F1's [wl](wl/README.md), the public Go API, opens a
built bundle and runs its flows and root scripts, in `warm` or `fresh`
mode, with the platform services it owns, and `run` is its command line;
F2a gives hosts the hooks of ADR 0008 through it as Go callbacks with
selectors (interception, argument rewrites, observers, halts, the run's
end, the invocation environment, per-run platform providers) and runs the
mandatory host-provided sites of a host contract with the host's
providers; F2b adds its budgets and latches (write-capable steps and the
first-write signal, the pre-write deadline, the latch, fatal error names,
the clock and the jitter), whose precedence the `hooks` conformance cases
hold.
`build` runs nine earlier
milestones: the OpenFlow decoder in
[internal/openflow](internal/openflow/README.md) (A3); the YAML dialect of
the `wmill` CLI in [internal/yamlx](internal/yamlx/README.md) (A2); the
sync-folder loader in [internal/syncfolder](internal/syncfolder/README.md)
(A4); the compiler of a folder and a host contract to the program the
engine runs, in [internal/ir](internal/ir/README.md) (A5); K1, the shared
types and interfaces under `internal/` that later milestones implement and
code against; and C2a, the Bun side of the build (the same package and the
[Bun build helper](harnesses/bun/build/README.md)), which bundles Bun
scripts with Windmill's settings and records their signatures, annotations
and workflow-as-code markers; and C2b, which bundles codebase scripts as
the `wmill` CLI does, with esbuild through its Go API, and carries their
assets. With `-shared-code`, an opt-in that
[ADR 0013](docs/design/0013-shared-module-code.md) decides, it also builds shared
code: a `warm` or host-mode process then compiles each build group's
dependency code once and gives each step module its own instance of it.
`selfcheck` runs the QuickJS-ng expression
engine of [internal/expr/quickjs](internal/expr/quickjs/README.md) (B1a).
`host` serves host mode, where a Bun program embeds the host SDK and starts
`windmill-lite host` as its engine, which opens a built bundle with `wl`
in host mode (F3a) and runs the host's runs with their steps in the host,
in the scopes the host captured: W1, the walking skeleton, built it first
to test the process model, on a test layout of its own. It runs on the
jsonb canonicaliser in
[internal/jsonval](internal/jsonval/README.md) (A1), which no other command
uses, and on C1, the harness protocol between the engine
([internal/harness](internal/harness/README.md)) and the Bun harness
([harnesses/bun](harnesses/bun/README.md)), whose step runtime core, C3a,
runs each step with Windmill's wrapper rules: binding and `Date` hydration
by the signature the build recorded, the result and error shapes,
`process.exit` interception and the attribution of process-level errors.
C3b adds each step's own log and child-process environment, the
settle-time fence, which revokes the timers, requests and output a step
leaves running when it settles and reports them, and `fresh` mode, one
process per step as on Windmill; C3n adds the native boundary mode, which
runs `nativets` and `//native` steps each in a realm of its own with the
rules of Windmill's native runtime. `wltest check` (C3a) runs the step
cases of [conformance/steps](conformance/README.md) through the same
harness, in `warm` and `fresh` mode, and runs each CommonJS codebase bundle
on Node as well, where Windmill runs it (C2b, deviation D13), and (E1, N1a)
the flow fixtures of [conformance/flows](conformance/README.md) and the
expression cases, lifted into one-step flows, on N1a's engine core in
[internal/engine](internal/engine/README.md), through `wl` in `warm` and
`fresh` (F1), comparing each run with what the oracle recorded. The engine core runs a compiled
program's flows in sequence, with Windmill's decision list for input
transforms, the `results` routes, `skip_if`, `mock`, identity steps and
`stop_after_if`, and K1's host-hook seams, and (N1b) its errors:
`continue_on_error`, the failure module and recovery, chaining errors,
the error shapes and the cancellation outcome, (N2) its waits and time:
`sleep`, the step timeout and its texts, retries with Windmill's 16-bit
delay arithmetic, jitter and argument reuse, the monitor of flows that
hang between steps, the pre-write deadline and an injectable clock, and
(N3a, N3b) its containers: for and while loops, `branchone` and
`branchall`, and (N3c) subflows by path and the failure module's copy in
every container body, (N4) the `cache_ttl` result cache, a store a
host may replace, and (N5) a retry on a subflow step, recorded on the
oracle; `host` runs its flows on it too. PF1 gives every step its job identity
([internal/platform](internal/platform/README.md)) and a platform facade
([internal/platform/facade](internal/platform/facade/README.md)): the
router behind the stock `windmill-client`, which attributes every call to
the step that made it (through the harness's intercepted `fetch` in a warm
process, by the step's token over loopback TCP in a `fresh` one), refuses
any other transport in a warm process, and fails a step that called a
route windmill-lite does not serve with `UnsupportedPlatformCall`. PF2
serves resources, variables and secrets behind it from one set of
services, with the interpolation walker that keeps Windmill's three
resource-read contracts apart, and the engine reads the same services: it
resolves a step's `$var:` and `$res:` arguments when the step's job is
picked up, `flow_env` on every transition, and `variable()` and
`resource()` in expressions; the harness writes each step's `args.json`.
PF3 adds the rest of the platform behind the facade: a job registry that
answers root lookups from the runs, flow user state shared by a run's
steps and its `flow_user_state()`, an object store for the S3 helpers,
and a dispatcher that runs a bundle's roots as separate runs when a step
dispatches them and refuses any other target. Its expressions run on the expression pipeline in
[internal/expr](internal/expr/README.md), whose textual stages and
shortcuts (B2a), which the compiler now runs for every expression, run on
QuickJS-ng with Windmill's program around each expression, its
serialisation and its error texts (B2b), held to every expression that
batch E3a recorded. The last eight are the oracle's: E2, the oracle
harness ([internal/oracle](internal/oracle/README.md), `wltest record`),
which records synthetic flow fixtures on an unmodified Windmill CE image in
public CI; recording batch E3c, which recorded the reference's
flow-execution error cases on that oracle
([conformance/flows/execution](conformance/flows/execution)) and settled
two contested findings; recording batch E3b, which recorded the
flow-execution core in the same directory and settled two more; recording
batch E3d, which recorded the control-flow cases
([conformance/flows/control-flow](conformance/flows/control-flow)) and
settled CF-12 and FX-40; E4a, its stub mode (`wltest stub` and the
replay service), which runs a flow with every script body replaced by a
stub that takes its outcome from a table, and whose acceptance cases
milestone E4b runs on windmill-lite too, matching the oracle's; and
recording batch E3a, which
recorded the expression cases of the reference's JE findings and the
`results` lookup cases CF-19 and CF-V6
([conformance](conformance/README.md)) and settled another; recording
batch E3e, which recorded what the others left: FX-36's HTTP responses,
the contested `later` cases, codebase scripts in CommonJS and ESM, and the
reference's probe catalogue rebuilt as executable fixtures; and recording
batch E3f, which recorded native steps on a worker that serves the
`nativets` tag and what the reference's revisions SC3 and SC4 asked the
oracle for, each in the oracle configuration it needs.

windmill-lite is not affiliated with Windmill Labs. It is released into the
public domain under the [Unlicense](LICENSE), except for vendored files that
keep their own licences: the Apache-2.0 files from Windmill under
`internal/openflow/schema/` ([NOTICE](internal/openflow/schema/NOTICE)) and
`internal/oracle/openapi/` ([NOTICE](internal/oracle/openapi/NOTICE)), and
the MIT-licensed QuickJS-ng sources under `internal/expr/quickjs/quickjs-ng/`
([NOTICE](internal/expr/quickjs/quickjs-ng/NOTICE)) and mimalloc sources
under `internal/expr/quickjs/mimalloc/`
([NOTICE](internal/expr/quickjs/mimalloc/NOTICE)).

## Quick start

Requires Go 1.27.3 or later (the `go` directive in `go.mod`) and a C
compiler, because the expression engine is C source built through cgo.

```sh
go run ./cmd/windmill-lite              # prints: windmill-lite (devel)
go run ./cmd/windmill-lite selfcheck    # starts the expression engine
go test -race ./...
```

The harness, build, skeleton and host-mode tests also need Bun 1.3.10, the
version Windmill v1.767.0 ships; without it they skip. Point `WL_BUN` at its
executable, and install the build helper's parser once. The Q-WA-4 test and
the facade's attribution test also need the stock `windmill-client@1.767.0`:
point `WL_WINDMILL_CLIENT` at a directory whose `node_modules` holds it (CI
fetches it by its npm integrity hash):

```sh
(cd harnesses/bun/build && bun install --frozen-lockfile)
WL_BUN=/path/to/bun-1.3.10/bun WL_WINDMILL_CLIENT=/path/to/client \
  go test -race ./internal/harness ./internal/bundle ./internal/engine ./internal/platform/... ./cmd/windmill-lite
```

The step suite (`go test ./cmd/wltest`) also runs each CommonJS codebase
bundle on Node, as Windmill does (deviation D13), when `WL_NODE` names a
Node executable; CI uses Node 20, the major of Windmill v1.767.0's image.

To build a bundle from the synthetic fixture folder and verify it:

```sh
export WL_BUN=/path/to/bun-1.3.10/bun WL_BUILD_HELPER=$PWD/harnesses/bun/build/main.ts
go run ./cmd/windmill-lite build internal/bundle/testdata/fixture/windmill \
  -contract internal/bundle/testdata/fixture/contract.yaml -o /tmp/fixture.wlb
go run ./cmd/windmill-lite verify /tmp/fixture.wlb
```

To run a flow of a built bundle, on the harness runtime it was built for:

```sh
go run ./cmd/windmill-lite run f/app/main --bundle /tmp/fixture.wlb \
  --args '{"name": "lite"}' -harness-runtime harnesses/bun/runtime
```

## Index

| Path | What it covers |
|---|---|
| [wl](wl/README.md) | The public Go library API (ADR 0009): `Open` verifies and opens a bundle with the platform services the runtime owns (a host's `cache_ttl` result cache among them), `Run` runs a flow or root script in `warm` or `fresh` mode and returns its status, result, trace and the platform calls its steps made, `BuildInfo` reports the bundle, `Close` drains it; the host hooks as Go callbacks with selectors, the providers of host-provided sites, and per-run platform providers (F2a); budgets and latches: write-capable steps, the pre-write deadline, fatal error names, the clock and the jitter (F2b); `Options.Host` opens it in host mode, serving the runs of the host process whose steps it runs (F3a); host plugins in TypeScript answer the hooks and provided sites over `host_call`, `Options.Plugin` in `warm` and the host's own in host mode, a host-mode run frame carries the run's budgets and latches, and steps call `wl.latch` and `wl.signal` (F2c). |
| [cmd/windmill-lite](cmd/windmill-lite/README.md) | The `windmill-lite` command: `build` writes a bundle from a sync folder, `verify` checks one against a manifest, `run` runs a bundle's flow or script through `wl` and prints the outcome as JSON, `selfcheck` starts the expression engine, `host` serves host mode on a built bundle through `wl` for a host such as the Bun host SDK, `version` prints its version. |
| [cmd/wltest](cmd/wltest/README.md) | The conformance and oracle runner. Today it has three verbs: `record`, which records flow fixtures, stub fixtures included, and expression cases lifted into generated flows on the Windmill CE oracle, `stub`, which writes the stubbed copy of a sync tree, and `check`, which runs the step cases on windmill-lite's Bun harness (and CommonJS codebase bundles on Node too, D13), and the flow fixtures (stub fixtures and fixtures with effects included, through the effect sink and replay service) and the expression cases, as generated flows, through `wl` in `warm` and `fresh`. |
| [internal/openflow](internal/openflow/README.md) | OpenFlow types and Windmill's effective decoder for flow values, with the vendored Apache-2.0 schema and the schema-coverage test. |
| [internal/yamlx](internal/yamlx/README.md) | Parses sync-folder YAML into the values the `wmill` CLI would send to Windmill (SY-9); refuses YAML it cannot read as the CLI does. |
| [internal/syncfolder](internal/syncfolder/README.md) | Reads a `wmill sync` folder as the CLI pushes it: the root, `wmill.yaml` and its filters, Windmill paths, scripts with their metadata and locks, flows with their inline scripts, resources, resource types and variables (SY-1 to SY-19). |
| [internal/ir](internal/ir/README.md) | The compiler: a sync folder and a host contract become the program the engine runs, normalised to Windmill's deployed form (container defaults, the native switch, virtual steps), with collapse decided, harness classes, provided sites, occurrences of repeated ids and the script inventory, and validated. |
| [internal/jsonval](internal/jsonval/README.md) | The engine's JSON value type: producer text, the NUL flag, and the stored text, which its jsonb canonicaliser computes and CI checks against PostgreSQL. |
| [internal/expr](internal/expr/README.md) | The expression pipeline: the compiled-expression IR, the JavaScript-engine and evaluator interfaces, the textual stages (trimming, the shortcuts, the rewrites, statement mode, bound names), the evaluator's shortcuts and bindings, and the program each expression runs as on QuickJS-ng: its preludes, serialisation, error texts, attempt retries and depth check; the expression suite against the E3a recordings and the budget gate. |
| [internal/expr/quickjs](internal/expr/quickjs/README.md) | The JavaScript engine for expressions: vendored QuickJS-ng `be664ca1` through cgo, with a heap on vendored mimalloc, a pool of prepared runtimes, the heap, stack and time limits, the compile cache and interrupts. |
| [internal/engine](internal/engine/README.md) | The flow interpreter: the engine core, which runs a compiled program's flows in sequence (input transforms and the argument pipeline, the `results` routes, `skip_if`, `mock`, identity, `stop_after_if`, a static step timeout, `continue_on_error`, the failure module and recovery, chaining errors and the cancellation outcome), its containers (for and while loops, `branchone`, `branchall`, each body carrying the failure module), subflows by path, retried like any step, and the `cache_ttl` result cache, through the hook seams and the precedence of host signals; the contracts it is built on (the step executor, the hooks with no-op defaults, the run trace); the mapping of a step's outcome to Windmill's stored value and the entrypoint's selection; a root script's run as a script job and the default dispatcher; and the walking skeleton's bundle loader. |
| [internal/conformance](internal/conformance/README.md) | The shared identity and observation vocabulary (the site-path encoder with its negative tests, run and step statuses, the observation types), the flow-fixture, expression-case and step-case formats, the lifting of expression cases into generated flows, claims and unit cases, the normaliser and the comparator, with the location rule of comparisons with windmill-lite; stub mode's replay table, replay service and the check of a run's calls that the oracle and windmill-lite share. |
| [internal/oracle](internal/oracle/README.md) | The oracle harness: compose with pinned digests, bootstrap, the CLI push, the deployment gate, capture, the walker and recordings with provenance; stub mode's generator and the locator that finds a stub's site in a running job tree; the image lint and the manifest guard; the vendored Apache-2.0 REST schema. |
| [internal/platform](internal/platform/README.md) | Job identity: the `WM_*` variables of each invocation, with the state path, the token and the host's configuration; the resource, variable and secret services, their local default store and host chains, the host's value resolvers, and the interpolation walker for the three resource-read contracts and worker argument resolution; the job registry, flow user state, the local object store, and the dispatcher's interface; the result cache service with its in-memory default. |
| [internal/platform/facade](internal/platform/facade/README.md) | The platform facade: Windmill's API routes from the vendored schema, attribution of every call to its invocation over the intercepted `fetch` and loopback TCP, the warm origin where nothing listens, the 501 policy, the root lookup, the resource and variable reads, flow user state, dispatch and its result polls, the object store's job helpers, and Windmill's error bodies. |
| [internal/harness](internal/harness/README.md) | The engine side of the harness protocol: frames, spawned harness processes with the handshake, settlement and the watchdog, warm and `fresh` pools, platform calls, warnings, each step's `args.json`, and host mode. It implements the step executor. |
| [internal/bundle](internal/bundle/README.md) | The bundle: the build of a sync folder, the bundle format and its `.wlb` archive, the manifest and the bundle digest, verification; the build parts both bundlers share (the env rewrite, the shim generator for host-provided modules, an inline script's import base), the Bun side of the build, which runs the Bun build helper, and codebase scripts, which esbuild bundles through its Go API as the `wmill` CLI does, with their assets and the CLI parity check. |
| [harnesses/bun](harnesses/bun/README.md) | The Bun harness on Bun 1.3.10: the runtime side of the protocol, with the per-invocation environment, log and `fetch` views and child-process wrapping, the step runner with Windmill's wrapper rules, the `process.exit` interception, the attribution of process-level errors, the settle-time fence on the per-invocation registry and the process scope, `fresh` mode, codebase modules (CommonJS on Bun's CommonJS loader) and their assets in the working directory, the native boundary mode for `nativets` and `//native` steps, and the host SDK (working-directory checks, host-provided modules, assets, runs and their settlement, health with recycle triggers, drain, the watchdog option, and a host plugin), and host plugins answered over `host_call`, with `around` wrappers, and the step API (`wl.latch`, `wl.signal`). |
| [harnesses/bun/build](harnesses/bun/build/README.md) | The Bun build helper: bundling with Windmill's settings and import rules, the signature parse, annotations, workflow-as-code detection, the export scan of host-provided modules, the source parse of codebase scripts, and the native form of native steps with their embedded client. |
| [conformance](conformance/README.md) | The shared conformance cases: the OpenFlow decode cases, the stored-form (jsonb) cases, the sync-folder load cases, the compile cases, the harness protocol cases (host mode's working-directory cases among them), the host-plugin cases, the Bun build cases (codebase scripts included), the Bun step cases (with D13's, Node against Bun), the flow fixtures the oracle records (the smoke fixture, recording batches E3c's and E3b's flow-execution fixtures, E3d's and N3c's control-flow fixtures, the stub-mode acceptance cases, and the JE, CF-19 and CF-V6 fixtures of batch E3a, E1's first fixtures of the languages and client-api areas, among them a codebase-script fixture, PF1's job-identity fixture, PF2's platform-services fixture, and batch E3e's remaining recordings) and the context-free expression cases, with their recordings, which `wltest check` runs end to end on the engine core. |
| [scripts](scripts/README.md) | Repository tooling for acknowledging Renovate dependency bumps automatically; installed, awaiting activation (a repository secret). |
| [docs/semantics](docs/semantics/README.md) | The Windmill `v1.767.0` semantics reference: findings by area, the conformance catalogue, the oracle harness design, and the licence map. |
| [docs/design](docs/design/README.md) | The design: decision records for the equivalence contract, the IR and loader, where semantics live, the harness protocol, JSON boundaries, module state, the platform interface, host hooks and adapters, packaging, differential testing, and the package layout with its milestones. |
| [docs/memoria.md](docs/memoria.md) | How the documentation gate works and the per-session review flow. |
| [AGENTS.md](AGENTS.md) | Contributor rules: the clean-room rule, synthetic fixtures, Go as the primary language, pull requests. |

## Contributing

Read [AGENTS.md](AGENTS.md) first. Its clean-room rule is a hard constraint:
Windmill's server is AGPLv3, so everyone who designs, reviews or implements
windmill-lite works without reading that source. They work from the semantics
reference, Windmill's public documentation, the Apache-2.0 parts of Windmill,
and observed behaviour.

[CI](.github/workflows/ci.yml) checks `gofmt`, runs `go mod tidy -diff`,
`go vet ./...` and `go test -race -timeout 45m ./...` (with Bun 1.3.10, the build
helper's parser and `windmill-client@1.767.0` installed, so the harness,
build and host-mode tests cannot skip, and Node 20, on which the step suite
runs each CommonJS codebase bundle as Windmill does, deviation D13; the
tests include the image lint and the manifest guard), builds a fixture
bundle twice with `windmill-lite build` and compares the digests and the
archives, prints a load measurement of the expression engine's pool and
the expression budget gate (report only), and runs the unit tests of the helpers in `scripts/`, on Ubuntu and
macOS for pull requests and pushes to `main`. Two more jobs run on Linux
runners. One builds the engine and the expression suite as static musl
binaries with `zig cc` for `amd64` and `arm64`, checks that they are static,
runs the suite, reports the expression budget gate from those binaries,
starts the engine in an image built `FROM scratch`, and compares the
expression pool's allocators (report only). The other,
`jsonb (PostgreSQL)`, compares
the canonicaliser with PostgreSQL in the oracle's pinned image. A separate
job runs the Bun harness's own `bun test`, which is not a required check.
The [codebase parity workflow](.github/workflows/parity.yml) runs the
pinned `windmill-cli@1.767.0`, unmodified, as a tool: it pushes synthetic
codebase scripts to a recording HTTP stub, and the bundles it uploads must
equal windmill-lite's esbuild reproduction of them; it is not a required
check, and its fallback, the settings comparison, runs in the Go jobs.
The [oracle workflow](.github/workflows/oracle.yml) records the flow
fixtures and the expression cases on the Windmill CE oracle nightly, on
demand, and on pull requests that change the cases or the harness or carry
the `oracle` label, in sessions: one worker for every fixture of the
reference configuration, two workers for the fixtures that need them, and
one session for each other oracle configuration a fixture names
(`oracle.session`: a worker that serves `nativets`, a longer run timeout,
`MAX_RESULT_SIZE_MB=3`, `DISABLE_FLOW_SCRIPT=true`). The
[Memoria gate](.github/workflows/memoria.yml) fails while any README has an
unacknowledged change in its scope.
[Renovate](renovate.json) keeps dependencies and action pins current.

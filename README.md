# windmill-lite

windmill-lite runs the flows and scripts of a [Windmill](https://www.windmill.dev)
folder with Windmill's semantics, but without Windmill's scheduling: no job
queue, no job per step, and no worker pickup.

**Current state:** first implementation milestones. Nothing runs flows yet.
The repository holds
a Go module, a placeholder command that prints its version, CI, a
documentation gate, the [semantics reference](docs/semantics/README.md) (the
Windmill `v1.767.0` behaviour that windmill-lite must match, written as
numbered findings with conformance cases), and the
[design](docs/design/README.md) (decision records for the architecture, the
equivalence contract, the package layout and the implementation milestones).
Fifteen milestones have landed. The newest, F4, gives the
[`windmill-lite` command](cmd/windmill-lite/README.md) its first work, in
[internal/bundle](internal/bundle/README.md): `build` turns a sync folder
and a host contract into a bundle (the compiled program, the bundled Bun
scripts and the folder's data) with a manifest that records the digest of
every file and of every input, and whose own digest identifies the bundle;
`verify` checks a bundle against a release manifest; `selfcheck` starts the
expression engine. Nothing runs a bundle yet. `build` runs eight earlier
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
and workflow-as-code markers. `selfcheck` runs the QuickJS-ng expression
engine of [internal/expr/quickjs](internal/expr/quickjs/README.md) (B1a).
No command uses two more yet: the jsonb canonicaliser in
[internal/jsonval](internal/jsonval/README.md) (A1), and C1, the harness
protocol between the engine ([internal/harness](internal/harness/README.md))
and a skeleton Bun harness ([harnesses/bun](harnesses/bun/README.md)), which
runs one exported function of a bundled module per step but not yet with
Windmill's wrapper rules. The last five are the oracle's: E2, the oracle
harness ([internal/oracle](internal/oracle/README.md), `wltest record`),
which records synthetic flow fixtures on an unmodified Windmill CE image in
public CI; recording batch E3c, which recorded the reference's
flow-execution error cases on that oracle
([conformance/flows/execution](conformance/flows/execution)) and settled
two contested findings; recording batch E3b, which recorded the
flow-execution core in the same directory and settled two more; recording
batch E3d, which recorded the control-flow cases
([conformance/flows/control-flow](conformance/flows/control-flow)) and
settled CF-12 and FX-40; and E4a, its stub mode (`wltest stub` and the
replay service), which runs a flow with every script body replaced by a
stub that takes its outcome from a table.

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

The harness and build tests also need Bun 1.3.10, the version Windmill
v1.767.0 ships; without it they skip. Point `WL_BUN` at its executable, and
install the build helper's parser once:

```sh
(cd harnesses/bun/build && bun install --frozen-lockfile)
WL_BUN=/path/to/bun-1.3.10/bun go test -race ./internal/harness ./internal/bundle
```

To build a bundle from the synthetic fixture folder and verify it:

```sh
export WL_BUN=/path/to/bun-1.3.10/bun WL_BUILD_HELPER=$PWD/harnesses/bun/build/main.ts
go run ./cmd/windmill-lite build internal/bundle/testdata/fixture/windmill \
  -contract internal/bundle/testdata/fixture/contract.yaml -o /tmp/fixture.wlb
go run ./cmd/windmill-lite verify /tmp/fixture.wlb
```

## Index

| Path | What it covers |
|---|---|
| [cmd/windmill-lite](cmd/windmill-lite/README.md) | The `windmill-lite` command: `build` writes a bundle from a sync folder, `verify` checks one against a manifest, `selfcheck` starts the expression engine, `version` prints its version. |
| [cmd/wltest](cmd/wltest/README.md) | The conformance and oracle runner. Today it has two verbs: `record`, which records flow fixtures on the Windmill CE oracle, stub fixtures included, and `stub`, which writes the stubbed copy of a sync tree. |
| [internal/openflow](internal/openflow/README.md) | OpenFlow types and Windmill's effective decoder for flow values, with the vendored Apache-2.0 schema and the schema-coverage test. |
| [internal/yamlx](internal/yamlx/README.md) | Parses sync-folder YAML into the values the `wmill` CLI would send to Windmill (SY-9); refuses YAML it cannot read as the CLI does. |
| [internal/syncfolder](internal/syncfolder/README.md) | Reads a `wmill sync` folder as the CLI pushes it: the root, `wmill.yaml` and its filters, Windmill paths, scripts with their metadata and locks, flows with their inline scripts, resources, resource types and variables (SY-1 to SY-15). |
| [internal/ir](internal/ir/README.md) | The compiler: a sync folder and a host contract become the program the engine runs, normalised to Windmill's deployed form (container defaults, the native switch, virtual steps), with collapse decided, harness classes, provided sites, occurrences of repeated ids and the script inventory, and validated. |
| [internal/jsonval](internal/jsonval/README.md) | The engine's JSON value type: producer text, the NUL flag, and the stored text, which its jsonb canonicaliser computes and CI checks against PostgreSQL. |
| [internal/expr](internal/expr/README.md) | Contracts of the expression pipeline: the compiled-expression IR, the JavaScript-engine interface and the evaluator interface. The pipeline is not implemented yet. |
| [internal/expr/quickjs](internal/expr/quickjs/README.md) | The JavaScript engine for expressions: vendored QuickJS-ng `be664ca1` through cgo, with a heap on vendored mimalloc, a pool of prepared runtimes, the heap, stack and time limits, the compile cache and interrupts. |
| [internal/engine](internal/engine/README.md) | Contracts of the flow interpreter: the step executor, the hook seams with no-op defaults, the precedence of host signals, and the run trace. No interpreter yet. |
| [internal/conformance](internal/conformance/README.md) | The shared identity and observation vocabulary (the site-path encoder with its negative tests, run and step statuses, the observation types), the flow-fixture format, the normaliser and the comparator; stub mode's replay table and replay service. |
| [internal/oracle](internal/oracle/README.md) | The oracle harness: compose with pinned digests, bootstrap, the CLI push, the deployment gate, capture, the walker and recordings with provenance; stub mode's generator and the locator that finds a stub's site in a running job tree; the image lint and the manifest guard; the vendored Apache-2.0 REST schema. |
| [internal/harness](internal/harness/README.md) | The engine side of the harness protocol: frames, spawned harness processes with the handshake, settlement and the watchdog, and pools. It implements the step executor. |
| [internal/bundle](internal/bundle/README.md) | The bundle: the build of a sync folder, the bundle format and its `.wlb` archive, the manifest and the bundle digest, verification; the build parts both bundlers share (the env rewrite, the shim generator for host-provided modules, an inline script's import base) and the Bun side of the build, which runs the Bun build helper. |
| [harnesses/bun](harnesses/bun/README.md) | The Bun harness: the runtime side of the protocol and a skeleton step runner, on Bun 1.3.10. |
| [harnesses/bun/build](harnesses/bun/build/README.md) | The Bun build helper: bundling with Windmill's settings and import rules, the signature parse, annotations, workflow-as-code detection, and the export scan of host-provided modules. |
| [conformance](conformance/README.md) | The shared conformance cases: the OpenFlow decode cases, the stored-form (jsonb) cases, the sync-folder load cases, the compile cases, the harness protocol cases, the Bun build cases, and the flow fixtures the oracle records: the smoke fixture, recording batches E3c's and E3b's flow-execution fixtures and the stub-mode acceptance cases. |
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
`go vet ./...` and `go test -race ./...` (with Bun 1.3.10 and the build
helper's parser installed, so the harness and build tests cannot skip; the
tests include the image lint and the manifest guard), prints a load measurement of the expression engine's pool (report
only), and runs the unit tests of the helpers in `scripts/`, on Ubuntu and
macOS for pull requests and pushes to `main`. Two more jobs run on Linux
runners. One builds the expression engine's tests as a static musl binary
with `zig cc` for `amd64` and `arm64`, checks that it is static, and runs it
on a runner of that architecture. The other, `jsonb (PostgreSQL)`, compares
the canonicaliser with PostgreSQL in the oracle's pinned image. A separate
job runs the Bun harness's own `bun test`, which is not a required check.
The [oracle workflow](.github/workflows/oracle.yml) records the flow
fixtures on the Windmill CE oracle nightly, on demand, and on pull requests
that change the fixtures or the harness or carry the `oracle` label. The
[Memoria gate](.github/workflows/memoria.yml) fails while any README has an
unacknowledged change in its scope.
[Renovate](renovate.json) keeps dependencies and action pins current.

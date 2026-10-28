# windmill-lite

windmill-lite runs the flows and scripts of a [Windmill](https://www.windmill.dev)
folder with Windmill's semantics, but without Windmill's scheduling: no job
queue, no job per step, and no worker pickup.

**Current state:** first implementation milestones. Only the walking
skeleton runs flows yet: one sequential flow at a time, a test scaffold for
the process model. The repository holds
a Go module, a command that prints its version and serves the walking
skeleton's host mode, CI, a documentation gate, the [semantics reference](docs/semantics/README.md) (the
Windmill `v1.767.0` behaviour that windmill-lite must match, written as
numbered findings with conformance cases), and the
[design](docs/design/README.md) (decision records for the architecture, the
equivalence contract, the package layout and the implementation milestones).
Twelve milestones have landed: the OpenFlow decoder in
[internal/openflow](internal/openflow/README.md) (A3); the YAML dialect of
the `wmill` CLI in [internal/yamlx](internal/yamlx/README.md) (A2); the
jsonb canonicaliser in [internal/jsonval](internal/jsonval/README.md) (A1);
the sync-folder loader in [internal/syncfolder](internal/syncfolder/README.md)
(A4); the compiler of a folder and a host contract to the program the
engine runs, in [internal/ir](internal/ir/README.md) (A5); K1, the shared
types and interfaces under `internal/` that later
milestones implement and code against; the QuickJS-ng expression engine in
[internal/expr/quickjs](internal/expr/quickjs/README.md) (B1a); C1, the
harness protocol between the engine
([internal/harness](internal/harness/README.md)) and a skeleton Bun harness
([harnesses/bun](harnesses/bun/README.md)), which runs one exported function
of a bundled module per step but not yet with Windmill's wrapper rules; E2,
the oracle harness ([internal/oracle](internal/oracle/README.md),
`wltest record`), which records synthetic flow fixtures on an unmodified
Windmill CE image in public CI; E3c, the recording batch of the
reference's flow-execution error cases on that oracle
([conformance/flows/execution](conformance/flows/execution)), which settled
two contested findings; E3b, the recording batch of the flow-execution
core in the same directory, which settled two more; and W1, the walking
skeleton. W1 runs one
sequential flow end to end on the pinned Bun, in a spawned harness and in
host mode, where a Bun program embeds the host SDK and starts
`windmill-lite host` as its engine. It is scaffolding that later milestones
replace, built to test the process model first.

windmill-lite is not affiliated with Windmill Labs. It is released into the
public domain under the [Unlicense](LICENSE), except for vendored files that
keep their own licences: the Apache-2.0 files from Windmill under
`internal/openflow/schema/` ([NOTICE](internal/openflow/schema/NOTICE)) and
`internal/oracle/openapi/` ([NOTICE](internal/oracle/openapi/NOTICE)), and
the MIT-licensed QuickJS-ng sources under `internal/expr/quickjs/quickjs-ng/`
([NOTICE](internal/expr/quickjs/quickjs-ng/NOTICE)).

## Quick start

Requires Go 1.27.3 or later (the `go` directive in `go.mod`) and a C
compiler, because the expression engine is C source built through cgo.

```sh
go run ./cmd/windmill-lite    # prints: windmill-lite (devel)
go test -race ./...
```

The harness, skeleton and host-mode tests also need Bun 1.3.10, the
version Windmill v1.767.0 ships; without it they skip. Point `WL_BUN` at its
executable. The Q-WA-4 test also needs the stock `windmill-client@1.767.0`:
point `WL_WINDMILL_CLIENT` at a directory whose `node_modules` holds it (CI
fetches it by its npm integrity hash):

```sh
WL_BUN=/path/to/bun-1.3.10/bun WL_WINDMILL_CLIENT=/path/to/client go test -race ./...
```

## Index

| Path | What it covers |
|---|---|
| [cmd/windmill-lite](cmd/windmill-lite/README.md) | The `windmill-lite` command. Today it prints its version and serves host mode for the Bun host SDK with the walking skeleton (`windmill-lite host`). |
| [cmd/wltest](cmd/wltest/README.md) | The conformance and oracle runner. Today it has one verb, `record`, which records flow fixtures on the Windmill CE oracle. |
| [internal/openflow](internal/openflow/README.md) | OpenFlow types and Windmill's effective decoder for flow values, with the vendored Apache-2.0 schema and the schema-coverage test. |
| [internal/yamlx](internal/yamlx/README.md) | Parses sync-folder YAML into the values the `wmill` CLI would send to Windmill (SY-9); refuses YAML it cannot read as the CLI does. |
| [internal/syncfolder](internal/syncfolder/README.md) | Reads a `wmill sync` folder as the CLI pushes it: the root, `wmill.yaml` and its filters, Windmill paths, scripts with their metadata and locks, flows with their inline scripts, resources, resource types and variables (SY-1 to SY-15). |
| [internal/ir](internal/ir/README.md) | The compiler: a sync folder and a host contract become the program the engine runs, normalised to Windmill's deployed form (container defaults, the native switch, virtual steps), with collapse decided, harness classes, provided sites, occurrences of repeated ids and the script inventory, and validated. |
| [internal/jsonval](internal/jsonval/README.md) | The engine's JSON value type: producer text, the NUL flag, and the stored text, which its jsonb canonicaliser computes and CI checks against PostgreSQL. |
| [internal/expr](internal/expr/README.md) | Contracts of the expression pipeline: the compiled-expression IR, the JavaScript-engine interface and the evaluator interface. The pipeline is not implemented yet. |
| [internal/expr/quickjs](internal/expr/quickjs/README.md) | The JavaScript engine for expressions: vendored QuickJS-ng `be664ca1` through cgo, a pool of prepared runtimes, the heap, stack and time limits, the compile cache and interrupts. |
| [internal/engine](internal/engine/README.md) | Contracts of the flow interpreter: the step executor, the hook seams with no-op defaults, the precedence of host signals, and the run trace; and the walking skeleton's interpreter, which runs one sequential flow and which N1a replaces. |
| [internal/conformance](internal/conformance/README.md) | The shared identity and observation vocabulary (the site-path encoder with its negative tests, run and step statuses, the observation types), the flow-fixture format, the normaliser and the comparator. |
| [internal/oracle](internal/oracle/README.md) | The oracle harness: compose with pinned digests, bootstrap, the CLI push, the deployment gate, capture, the walker and recordings with provenance; the image lint and the manifest guard; the vendored Apache-2.0 REST schema. |
| [internal/harness](internal/harness/README.md) | The engine side of the harness protocol: frames, spawned harness processes with the handshake, settlement and the watchdog, pools, platform calls, and host mode. It implements the step executor. |
| [harnesses/bun](harnesses/bun/README.md) | The Bun harness on Bun 1.3.10: the runtime side of the protocol with the per-invocation environment and facade fetch, a skeleton step runner, and a minimal host SDK. |
| [conformance](conformance/README.md) | The shared conformance cases: the OpenFlow decode cases, the stored-form (jsonb) cases, the sync-folder load cases, the compile cases, the harness protocol cases, and the flow fixtures the oracle records: the smoke fixture and recording batches E3c's and E3b's flow-execution fixtures. |
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
`go vet ./...` and `go test -race ./...` (with Bun 1.3.10 and
`windmill-client@1.767.0` installed, so the harness and host-mode tests
cannot skip; the tests include the image lint and the manifest guard),
prints a load measurement of the expression engine's pool (report
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

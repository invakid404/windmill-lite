# proto

**Current state:** `windmill_lite/v1/runner.proto` defines version 1 of the
`Runner` service of windmill-lite's
[go-plugin adapter](../docs/design/0009-host-adapters.md#the-go-plugin-adapter),
the engine's `HooksControl` service beside it, and the `HostHooks` service
that a host serves on go-plugin's broker. Their Go code is generated into
[wl/plugin](../wl/plugin/README.md) and committed. The engine serves
`Runner` and `HooksControl`, and calls `HostHooks`
([internal/host](../internal/host/README.md)).

This README covers `proto/`: the service definitions and the script that
generates their Go code.

`runner.proto` defines gRPC services and their messages in the Protocol
Buffers language. The services are the contract between the engine, served
as a [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin) plugin,
and the Go host that launches it
([host adapters](../docs/design/0009-host-adapters.md#the-runner-service)).
The design record gives the rules of each call and field; the comments in
`runner.proto` name them.

| File | Contents |
|---|---|
| `windmill_lite/v1/runner.proto` | The package `windmill_lite.v1`: the services `Runner`, `HooksControl` and `HostHooks`, and their messages |
| `generate.sh` | The script that generates the Go code into `wl/plugin` |

## Generate the Go code

Generate the code again after every change to `runner.proto`, and commit
the result with it. You need Go 1.27.3 or later and protoc 36.2, the
Protocol Buffers compiler, on the `PATH`
([release v36.2](https://github.com/protocolbuffers/protobuf/releases/tag/v36.2)).
protoc's version is part of each generated file, so another version gives
other files.

1. Run `go generate ./wl/plugin` from the repository root. It runs
   `proto/generate.sh`, which writes `wl/plugin/runner.pb.go` and
   `wl/plugin/runner_grpc.pb.go`.
2. Commit `runner.proto` and the two files together.

`generate.sh` exits with 1, and writes nothing, when protoc is missing or
`protoc --version` does not print `libprotoc 36.2`.

The `Check the generated protobuf code` step of the CI workflow installs
protoc 36.2, generates the code again and fails when a file under
`wl/plugin` or `proto` differs from the commit
([docs/ci.md](../docs/ci.md#the-go-jobs)).

### The generation tools

| Tool | What it does | Version from | Licence |
|---|---|---|---|
| protoc | Compiles `runner.proto` and runs the two generators | `generate.sh` and the CI step: 36.2 | BSD-3-Clause |
| `protoc-gen-go` | Writes the message types | the `tool` directive of the root `go.mod`, module `google.golang.org/protobuf` | BSD-3-Clause |
| `protoc-gen-go-grpc` | Writes the gRPC client and server types | the `tool` directive of the root `go.mod`, module `google.golang.org/grpc/cmd/protoc-gen-go-grpc` | Apache-2.0 |

- **The generators as tools of the root module.** `generate.sh` builds each
  generator with `go tool -n`, which prints the path of the executable. So
  the generated code matches the versions of `google.golang.org/protobuf`
  and `google.golang.org/grpc` that `wl/plugin` builds with.
- **Placement.** The generators' option `module` places each file by the
  `go_package` of `runner.proto`, relative to the repository root.
- **Not buf.** buf, a Protocol Buffers compiler written in Go, could run as
  a Go tool without protoc. Its module graph holds a module under the
  Mozilla Public License 2.0, `pgregory.net/rapid`, through a test of one of
  its dependencies. To keep the modules under that licence to
  hashicorp/go-plugin and hashicorp/yamux, the generation uses protoc.

## Change the service

Version 1 changes only by additions, so that an engine and a host of
different releases still understand each other:

- Add a field with a field number that the message has never used. Never
  renumber, retype or reuse a field.
- Add a call to a service.
- For any other change, define the package `windmill_lite.v2` in
  `windmill_lite/v2/`, beside version 1, which stays until hosts have moved.

Keep the field names that host mode's `run`, `run_done` and `run_cancel`
frames share with `RunRequest`, `RunResult` and `CancelRequest`
([harness protocol](../docs/design/0004-harness-protocol.md#messages)).

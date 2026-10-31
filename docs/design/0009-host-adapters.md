# 0009 · Host adapters and typed methods

**Status:** proposed · **Topic:** how a host drives windmill-lite
(hashicorp/go-plugin first, then plain gRPC, HTTP, a CLI, an embedded Go
library, and a host written in Bun), and how each flow and script becomes a
typed method.

## Context

Hosts differ in language and in how much of windmill-lite they want to own.
A Go service may want the engine in its own process; another may want it as a
separate, pinned plugin binary; a host written in TypeScript may need its run
scopes and plugins to live in the same process as the steps
([0008](0008-host-hooks.md) H11). The engine API must be one thing, with
adapters as thin transports over it.

Windmill does not validate inputs against a flow's `schema`: the schema has
no server-side execution semantics ([OF-18](../semantics/openflow.md#of-18)).
Scripts differ only when they opt in with a `schema_validation` annotation.
Types derived from schemas can therefore help callers but must not change
what a run does.

## Decision

### The core: a Go library

Package `wl` (import path `github.com/invakid404/windmill-lite/wl`) is the
only engine API. In outline:

1. `wl.Open(bundle, options)` loads a bundle, verifies its manifest
   ([0010](0010-packaging.md)), and starts harness pools lazily.
2. `runtime.Run(ctx, target, args, runOptions)` runs a flow or script by path
   and returns its status, final result, `mayHaveMutated` flag, trace and
   warnings. `runOptions` carries hooks ([0008](0008-host-hooks.md)),
   per-run platform providers ([0007](0007-platform-interface.md)), deadlines,
   the clock, run data, and a module-state override. (F1 takes the
   module-state mode in `options`, once per runtime, since one harness pool
   and one engine hold it; see [0012](0012-layout-and-milestones.md)'s
   Consequences.)
3. `runtime.BuildInfo()` reports the bundle digest, engine and harness
   versions, the Bun version, the host contract and the deviations in effect.
4. `runtime.Close()` drains the pools.

Every adapter below is a transport over these calls.

### Adapters, in order of delivery

1. **go-plugin.** A plugin binary serves the `windmill_lite.v1.Runner` gRPC
   service: `GetBuildInfo`, `Run` (unary, the gRPC deadline is the run's
   hard deadline), `RunStream` (module events while the run proceeds) and
   `Cancel`. A small Go package gives hosts the client side, the handshake
   config and the plugin map. The host pins the plugin executable with
   go-plugin's `SecureConfig` checksum and compares `GetBuildInfo` with the
   release manifest. Go hosts get hooks through a host-side callback service
   on the go-plugin broker. The service is defined in
   `proto/windmill_lite/v1/runner.proto`.
2. **Bun host SDK** (host mode). A TypeScript package for hosts that are
   themselves Bun programs: the host process is also the Bun harness. It
   starts the engine binary the host names as a child on a socketpair
   (`windmill-lite host`, which opens the bundle with `wl` in host mode,
   `wl.Options.Host`), with the bundle digest from the bundle's manifest,
   registers host plugins as in-process functions, and submits runs (`run`
   frames, [0004](0004-harness-protocol.md)).
   The steps of those runs execute in the host process, inside the scopes
   captured at submission (H11). This is the adapter for a host whose
   per-run state lives in `AsyncLocalStorage`.
   - Its `run`, `run_done` and `run_cancel` frames are the JSON mapping of the
     `Runner` messages, so the go-plugin service and host mode share one
     schema.
   - It registers the host's instances of the host-provided modules
     ([0010](0010-packaging.md)) before any step loads.
   - `health()` reports in-flight, abandoned and unattributed-error counts;
     `drain(timeout)` stops new runs and lets the host exit; `onRunSettled`
     complements `onRunEnd` (a run's `onSettled`); and the connect options
     set the engine's watchdog (`watchdog.killAfter`, default 30 s), which
     kills the host when its event loop stops answering
     ([0004](0004-harness-protocol.md), host mode), and recycle triggers that
     `health()` reports. The host's supervisor only has to restart the host
     when it exits.
   - The host passes a writable working directory, which must already be its
     working directory; the SDK extracts codebase assets there and refuses to
     connect otherwise (`workdir_not_writable`, `workdir_mismatch`), and the
     engine checks the same again in the handshake.
   - **Consumption.** Nothing is published to a package registry. A host
     vendors the SDK together with the harness runtime it imports, from the
     commit or release of the engine it runs, and checks the copy's
     provenance ([Vendoring the Bun host SDK](#vendoring-the-bun-host-sdk)).
     For a release, the SDK version equals the engine version, and the
     handshake rejects any other pairing.
   - **Types.** A host that vendors the SDK, with the harness runtime it
     imports, typechecks them with its own program. So the SDK and the
     runtime use only names that the pinned Bun's type declarations
     (`@types/bun`) define, with or without the DOM library, and CI
     typechecks them that way (`harnesses/bun/test/typecheck`).
3. **Plain gRPC.** The same `Runner` service on a Unix or TCP socket, without
   the go-plugin handshake, for hosts in other languages.
4. **HTTP.** `POST /v1/run/{f|p}/{path}` with the arguments as the body,
   answering with the run's status, result and, on request, its trace;
   `GET /v1/build-info`; `GET /healthz`. Not Windmill's API: windmill-lite
   does not pretend to be a Windmill server.
5. **CLI.** `windmill-lite run <path> --bundle <b> --args <json|@file>`,
   printing the outcome as JSON and exiting non-zero on failure; used by the
   conformance runner and for debugging.

### Vendoring the Bun host SDK

A Bun host copies the SDK into its own repository. A package manager cannot
install it as a git dependency, because neither the repository root nor
`harnesses/bun` has a `package.json`.

1. **Copy two directories together.** `harnesses/bun/host/index.ts` imports
   `../runtime/context.ts`, `../runtime/plugin.ts` and
   `../runtime/runtime.ts`, which import every other file of
   `harnesses/bun/runtime` but `main.ts`, the entry point of a spawned
   harness.
   Copy both directories from one commit, as siblings named `host/` and
   `runtime/`, so that the relative imports resolve. A copy of `host/`
   alone does not load.
2. **Change no file.** The runtime is the harness that every step of the
   host runs under: Windmill's wrapper rules, the settle-time fence and the
   native boundary mode. An edited copy runs steps differently from the code
   that the engine's tests checked, and no engine check detects the edit
   (item 4).
3. **Build the engine and the bundle from the same commit.** `wl.Open`
   refuses a bundle that another engine version built
   (`ErrEngineVersion`). For a release, the handshake also refuses an SDK of
   another release (`version_mismatch`).
4. **Keep a provenance record, and check it in the host's tests.** In host
   mode the engine does not compare the host's runtime with the bundle's
   `harness_runtimes` digest, because the host process is the harness. A
   copy without a release stamp reports the harness version `dev`, which
   pairs with any engine. So the host checks the copy itself:
   - Next to the copy, record the repository URL, the full commit ID, and
     the SHA-256 of every copied file by its path.
   - In a test, recompute the SHA-256 of every file in the copy, and fail
     on a changed, missing or extra file.
   - In the same test, check that the engine binary was built from the
     recorded commit (below).
5. **Update both directories at once.** To move to another commit, replace
   both directories and the record, rebuild the engine and the bundle, and
   run the check again.

`windmill-lite version` prints the module version that Go stamps into the
binary. Built from a clean checkout of the commit, or with
`go install github.com/invakid404/windmill-lite/cmd/windmill-lite@<commit>`,
it is a pseudo-version that ends in the first 12 hexadecimal digits of the
commit ID, such as `v0.0.0-20261028113407-7c82cc9ab979`. Go appends
`+dirty` to the version of a build from a checkout with uncommitted
changes.

### Typed methods

`windmill-lite gen go|ts --bundle <b>` generates client code: one method per
flow and script, with an input type derived from its JSON schema (the flow's
`schema`, or the script's `.script.yaml` schema), calling the generic `Run`.

- The schema clean-ups that windmill-ts learned are applied: `default: null`,
  defaults outside an enum, `enum: null`, empty objects, `dynselect-*`, the
  two S3 object spellings, `format: resource-<type>`
  ([PA-3](../semantics/openflow.md#pa-3), [WA-60](../semantics/client-api.md#wa-60)).
  windmill-ts is Unlicense and may be copied.
- Results are untyped (raw JSON), because Windmill has no result schema.
- The generated code embeds the bundle digest; connecting to a runtime with
  another digest fails, so a client cannot silently drift from the flows it
  was generated for.
- The server does not validate flow arguments against the schema, matching
  OF-18. A script that opts into validation with `schema_validation` is
  validated as Windmill does, once the reference covers that annotation's
  rules ([languages.md](../semantics/languages.md)); until then such a script
  fails at load with `unsupported_feature` naming the annotation. Types are a
  compile-time aid only.

The wire stays generic, so a changed flow needs regenerated clients but never
a new protocol.

## Consequences

- One engine API; adapters can be added without touching semantics.
- A Bun host keeps its in-process plugins and scopes, while the engine stays
  in Go.
- go-plugin hosts get the same pinning and build-identity checks they use for
  any plugin.
- Milestone F3a made host mode a runtime of `wl` (`Options.Host`), so the
  Bun host SDK and any other host that speaks the protocol share one engine
  path. The engine binary's path is the host's to give, not the bundle
  manifest's: a manifest that recorded an engine digest would make the
  bundle digest differ per platform. What pairs the pieces is that the
  engine opens only a bundle its own version built (refusing the host with
  `engine_refused` otherwise), and the handshake checks the protocol
  version, the harness kind, the pinned Bun and the bundle digest. The
  SDK's own version was not compared with the engine's then: that needed the
  release to stamp it. A host that pins the engine binary by digest
  does so itself (as `pack --single --host bun` will, F6).
- Milestone F3b made the pairing real. The SDK's version is the harness
  version its hello names (`harnesses/bun/runtime/version.ts`), which a
  release stamps with `--define WL_RELEASE='"vX.Y.Z"'` when it runs or
  builds the host, or by setting the default in its tag; the repository's
  own is `dev`. The engine refuses a host whose stamped SDK version differs
  from its own stamped version with `version_mismatch` in the handshake,
  and the SDK refuses an engine of another release in the welcome too, so
  that an engine from before the check is not used either. An unstamped
  version (`dev`, a development build's `(devel)`) pairs with anything,
  since there is no release to compare. Both sides run the cases of
  `conformance/protocol/host/versions.json`.

## Spec IDs relied on

OF-18, PA-3, WA-60, and the hooks of [0008](0008-host-hooks.md).

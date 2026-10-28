# cmd/windmill-lite

**Current state:** milestone F4 of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md). This README
covers the `windmill-lite` command in this directory. The command prints its
version, builds a bundle from a Windmill sync folder, verifies a bundle, and
checks that its expression engine starts. The commands that run a bundle
(`run`, `serve`), pack it (`pack`, `image`) or generate clients (`gen`) do
not exist yet.

## Usage

```sh
go run ./cmd/windmill-lite            # windmill-lite (devel)
go run ./cmd/windmill-lite version    # same; --version, -version and -v also work
windmill-lite build <folder> -o <bundle> [flags]
windmill-lite verify <bundle> [-manifest <release manifest>] [-digest <digest>]
windmill-lite selfcheck
```

Flags may come before or after the positional argument. A usage error exits
with status 2; any other failure with status 1.

### build

`windmill-lite build <folder> -o <bundle>` builds the bundle of
[ADR 0010](../../docs/design/0010-packaging.md) with
[internal/bundle](../../internal/bundle/README.md): it reads the sync folder
from the root above `<folder>` (the directory of `wmill.yaml`), compiles it,
bundles its Bun scripts on the pinned Bun 1.3.10, and writes the bundle. It
prints the bundle digest on standard output, and warnings on standard
error.

| Flag | Meaning |
|---|---|
| `-o` | the bundle to write: a directory that must not exist or be empty, or a file ending in `.wlb` that must not exist, written as an archive |
| `-contract` | the host contract file (YAML or JSON); without one every flow and script of the folder is a root |
| `-bun` | the Bun executable; `$WL_BUN`, or `bun` on the `PATH` |
| `-helper` | the Bun build helper, `harnesses/bun/build/main.ts` with its dependencies installed; `$WL_BUILD_HELPER`. It is required |
| `-harness-runtime` | the Bun harness runtime directory whose digest the manifest records; `$WL_HARNESS_RUNTIME`, or `runtime/` beside the helper's directory |
| `-bun-mismatch` | build on another Bun and mark the bundle `nonconformant_runtime` |
| `-module-state` | `warm` (default) or `fresh` ([ADR 0006](../../docs/design/0006-module-state.md)) |
| `-strict` | refuse absent for-loop `skip_failures` and branchall `skip_failure` instead of defaulting them |
| `-work` | a scratch directory for dependency installs; a temporary one by default |
| `-verbose` | copy the build helper's output to standard error |

From a checkout, with the helper's dependencies installed
(`bun install --frozen-lockfile` in `harnesses/bun/build`):

```sh
WL_BUILD_HELPER=$PWD/harnesses/bun/build/main.ts \
  windmill-lite build internal/bundle/testdata/fixture/windmill \
  -contract internal/bundle/testdata/fixture/contract.yaml -o /tmp/fixture.wlb
```

The manifest records the engine's version (below). An engine without one
records `(devel)` and says so on standard error.

### verify

`windmill-lite verify <bundle>` recomputes the digest of every file of a
bundle directory or `.wlb` archive and checks them against the bundle's own
manifest, or against a release manifest given with `-manifest`, whose bytes
the bundle's manifest must then equal. `-digest` pins the bundle digest. It
prints the bundle digest and exits with status 0 when everything matches;
otherwise it lists every difference and exits with status 1. A bundle that
a different engine version built still verifies, with a note that this
engine would not open it.

### selfcheck

`windmill-lite selfcheck` starts the expression engine (QuickJS-ng, through
[internal/expr/quickjs](../../internal/expr/quickjs/README.md)) and
evaluates three probes whose results do not depend on the C library: the
language core, a bound host string, and a recursion 800 calls deep, which
needs the engine's own thread stacks. It prints one line and exits with
status 0, or names the failing probe and exits with status 1. CI runs it
from the static Linux binary in an image built `FROM scratch`
([ADR 0010](../../docs/design/0010-packaging.md), drift guard 5).

## Version

The printed version, which `build` records as the engine version, is, in
order of preference:

1. the value linked in with `-ldflags "-X main.version=<version>"`;
2. the module version from the binary's build information: the version of
   `go install github.com/invakid404/windmill-lite/cmd/windmill-lite@<version>`,
   or the pseudo-version `go build` stamps from the checkout's commit, with
   `+dirty` when the tree has changes;
3. `(devel)`, for `go run` and test binaries.

## Tests

`main_test.go` covers the version output, the link-time override, the usage
errors, `selfcheck`, and `build` and `verify` end to end: the fixture of
internal/bundle built as a directory and as an archive gives one digest,
which `verify` confirms against the release manifest, and a changed file
fails verification. The build test needs the pinned Bun in `WL_BUN` and
skips without it, unless `WL_REQUIRE_BUN` is set, as CI sets it.

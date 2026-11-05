# windmill-lite on npm

**Current state:** the npm package `windmill-lite` holds one runtime's SDK,
`windmill-lite/bun`: the Bun host SDK, with the Bun harness runtime that it
imports. Its version is the version of the windmill-lite
[engine](../docs/glossary.md#engine) that it pairs with. This README covers
`harnesses/`, the package's root: its manifest, its build and its
packaging test. The Bun harness and the SDK's sources have their own
README: [bun/](bun/README.md).

[windmill-lite](https://github.com/invakid404/windmill-lite) runs the flows
and scripts of a [Windmill](https://www.windmill.dev) folder with
Windmill's semantics, without Windmill's job queue. A Bun
[host](../docs/glossary.md#host) is a Bun program that runs those flows:
the SDK starts the windmill-lite engine as a child process, and the steps
run in the host's own process
([host mode](../docs/glossary.md#host-mode)).

## Install

```sh
bun add windmill-lite
```

A host needs three more things:

| Requirement | Why |
|---|---|
| [Bun](../docs/glossary.md#bun) 1.3.10 | The steps run on the host's Bun, and the engine refuses a host on any other Bun with `runtime_mismatch`. 1.3.10 is the Bun of Windmill v1.767.0's images. |
| The windmill-lite engine of the package's version | The SDK carries the package's version, and an engine of another release refuses it with `version_mismatch` (a development build of the engine, which has no release version, accepts any SDK). |
| A [bundle](../docs/glossary.md#bundle) that this engine built | The engine opens only a bundle that its own version built. |

The GitHub Release of each version, such as `v0.1.0`, has the engine for
Linux (amd64 and arm64, static binaries) and for macOS (arm64 and amd64),
with `LICENSES.txt` and `SHA256SUMS`.

1. Download the engine for your platform and `SHA256SUMS`.
2. Check it against its sum:
   `grep windmill-lite-<os>-<arch> SHA256SUMS | sha256sum --check`
   (on macOS, `shasum -a 256 --check` in place of `sha256sum --check`).
3. Make it executable: `chmod +x windmill-lite-<os>-<arch>`.

`windmill-lite version` prints `windmill-lite v0.1.0` for the engine of
`v0.1.0`.

To build a bundle, `windmill-lite build` also needs the Bun build helper,
which neither the release nor the package ships. Take it from a checkout
of the release's tag:

1. Check out the tag, such as `v0.1.0`.
2. Install the helper's dependencies:
   `(cd harnesses/bun/build && bun install --frozen-lockfile)`.
3. Build the bundle with that checkout's helper, and with Bun 1.3.10 as
   `bun` on the `PATH` or in `WL_BUN`:
   `WL_BUILD_HELPER=$PWD/harnesses/bun/build/main.ts windmill-lite build <folder> -o <bundle>`.

The [`build` command](../cmd/windmill-lite/README.md#build) describes its
flags, such as `-contract` for the host contract.

## Use

```ts
import { connect } from "windmill-lite/bun";

// The working directory must be a writable directory, given as workdir.
const host = await connect({ engine: "/usr/local/bin/windmill-lite", bundle: "./bundle", workdir: process.cwd() });
const run = await host.run("f/app/main", { name: "lite" });
console.log(run.status, run.result);
await host.close();
```

The [Bun harness README](bun/README.md#host-mode-and-the-host-sdk)
describes `connect()`, its options, runs, health, drain and host plugins.

## Types

The package ships JavaScript and type declarations (`.d.ts`), so a host's
`tsc` checks only the declarations, never the SDK's code. The packaging
test type-checks an import of `windmill-lite/bun` with:

- TypeScript 5.9.3 and the `tsconfig.json` that `bun init` of Bun 1.3.10
  writes;
- TypeScript 5.9.3 and 6.0.3 with `NodeNext` module resolution, and with
  `skipLibCheck` off, so that `tsc` checks the declarations too.

The declarations use Bun's type declarations, `@types/bun`, which `bun init`
installs. TypeScript 6 loads an `@types` package only when `types` names it,
so a TypeScript 6 project sets `"types": ["bun"]`.

## What the package holds

| File | What it is |
|---|---|
| `dist/bun/host/index.js` | The Bun host SDK and the harness runtime, bundled into one ES module by Bun 1.3.10. Its harness version is `v` + the package's version. |
| `dist/bun/host/index.d.ts`, `dist/bun/runtime/*.d.ts` | The type declarations of the SDK, and of the runtime modules whose types its declarations import: `context`, `frame`, `plugin`, `runtime` and `version` |
| `package.json`, `README.md`, `LICENSE` | The manifest, this README and the [Unlicense](LICENSE) |

`package.json` exports only `./bun`. Another runtime's SDK becomes another
subpath of this package.

## Build the package

`npm/build.ts`, the package's `prepack` script, builds `dist/`. So
`npm pack` and `npm publish` build first.

1. Install the build's dependencies, TypeScript and `@types/bun`:
   `(cd harnesses && bun install --frozen-lockfile)`.
2. Point `WL_BUN` at a Bun 1.3.10 executable. The build refuses any other
   Bun, because Bun's bundler transpiles the runtime.
3. Run `npm pack` in `harnesses/`.

The build reads the version from `package.json`, and builds
`dist/bun/host/index.js` with `Bun.build`, which defines `WL_RELEASE` as
`"v<version>"`. It checks that the module sets that harness version.

Then `tsc -p npm/tsconfig.json` emits the declarations, and the build
keeps only those that `dist/bun/host/index.d.ts` imports, directly or
through another kept file. The declarations keep the sources' relative
imports, which end in `.ts`; TypeScript resolves each to its `.d.ts` file.

## Test the package

`npm/test/check.sh` tests the package as a host gets it, and exits 1
at the first check that fails:

1. `npm pack` packs the package.
2. The tarball holds exactly the files of `npm/test/files.txt`. Its
   `package.json` has the fields that npm and provenance need and no
   dependencies, and its `LICENSE` is the repository's.
3. `bun add` installs the tarball into a copy of `npm/test/project/`, an
   empty project with the `tsconfig.json` of `bun init`.
4. `tsc` type-checks `npm/test/project/smoke.ts`, which imports
   `windmill-lite/bun`, in the three setups of [Types](#types).
5. The engine of the checkout, stamped with the package's version as a
   release stamps it, builds a bundle of the synthetic folder
   `npm/test/folder/`. The smoke host runs
   its flow `f/smoke/hello` through the installed SDK, which must return
   `"hello, npm"`.
6. An engine stamped with another version builds its own bundle, and must
   refuse the smoke host with `version_mismatch`. So the packed SDK carries
   exactly the package's version.

To run it, install the dependencies of the package and of the
[build helper](bun/build/README.md), then run the script with
`WL_BUN` set:

```sh
(cd harnesses && bun install --frozen-lockfile)
(cd harnesses/bun/build && bun install --frozen-lockfile)
WL_BUN=/path/to/bun-1.3.10/bun harnesses/npm/test/check.sh
```

It also needs `npm`, `python3`, and `go` with a C compiler. With
`WL_TARBALL` it checks that tarball instead of packing one, and with
`WL_ENGINE` it uses that engine binary instead of building one. The
[release workflow](../.github/workflows/release.yml) passes both. CI's Go
jobs on Ubuntu and macOS run it as the step `Test the npm package`.

## Versions and releases

The `version` field of `package.json` is the one version of windmill-lite,
of this package and of the engine
([scripts/release.py](../scripts/README.md#release-helper)). A release is
the Git tag `v` + the version. The release workflow builds the engine
binaries, creates the GitHub Release, and publishes this package with npm
provenance through npm trusted publishing. [RELEASING.md](../RELEASING.md)
gives the steps.

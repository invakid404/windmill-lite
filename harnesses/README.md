# windmill-lite on npm

**Current state:** the npm package `windmill-lite` holds the Bun host SDK,
`windmill-lite/bun`, and its test entry, `windmill-lite/bun/testing`,
which runs on Bun and on Node. Its version is the version of the
windmill-lite [engine](../docs/glossary.md#engine) that it works with.
This README covers `harnesses/`, the package's root: its manifest, its
build and its packaging test. The Bun harness and the SDK's sources have
their own README: [bun/](bun/README.md).

[windmill-lite](https://github.com/invakid404/windmill-lite) runs the
[flows](../docs/glossary.md#flow) and [scripts](../docs/glossary.md#script)
of a [Windmill](https://www.windmill.dev) folder with Windmill's
behaviour, without a Windmill server. A [Bun](../docs/glossary.md#bun)
[host](../docs/glossary.md#host) is a Bun program that runs those flows:
the [SDK](../docs/glossary.md#bun-host-sdk) starts the windmill-lite engine
as a child process, and the [steps](../docs/glossary.md#step) run in the
host's own process
([host mode](../docs/glossary.md#host-mode)).

## Install

Install the package at an exact version:

```sh
bun add --exact windmill-lite@0.4.1
```

Pin the version, because the SDK works only with the engine of the same
version ([Versions](#versions)). A version range lets a later install
take an SDK that your engine refuses.

| npm dist-tag | What it names |
|---|---|
| `latest` | The latest release, `0.4.1` |
| `next` | The latest [release candidate](../RELEASING.md#release-a-release-candidate), `0.2.0-rc.1` |

## Versions

A host needs Bun, the engine and a [bundle](../docs/glossary.md#bundle),
and each must match the SDK.
Otherwise the engine refuses the host, and `connect()` rejects with a
`HostError` whose code names the mismatch.

| What must match | The error otherwise | How to get it |
|---|---|---|
| [Bun](../docs/glossary.md#bun) 1.4.3: the steps run on the host's Bun | `runtime_mismatch` | [Bun 1.4.3](https://github.com/oven-sh/bun/releases/tag/bun-v1.4.3) |
| The engine of the SDK's version | `version_mismatch`. A development build of the engine, which has no release version, accepts any SDK. | [Get the engine](#get-the-engine) |
| A [bundle](../docs/glossary.md#bundle) that this engine built | `engine_refused` | [Build a bundle](#build-a-bundle) |

### Get the engine

Each version has a GitHub Release `v<version>`, such as
[v0.4.1](https://github.com/invakid404/windmill-lite/releases/tag/v0.4.1).
It holds the engine for Linux (amd64 and arm64, static binaries) and for
macOS (arm64 and amd64), with `LICENSES.txt` and `SHA256SUMS`. In these
steps, replace `<os>-<arch>` with `linux-amd64`, `linux-arm64`,
`darwin-arm64` or `darwin-amd64`.

1. Download the engine:

   ```sh
   curl -fLO https://github.com/invakid404/windmill-lite/releases/download/v0.4.1/windmill-lite-<os>-<arch>
   ```

2. Download the release's sums:

   ```sh
   curl -fLO https://github.com/invakid404/windmill-lite/releases/download/v0.4.1/SHA256SUMS
   ```

3. Check the engine against its sum:
   `grep windmill-lite-<os>-<arch> SHA256SUMS | sha256sum --check`. On
   macOS, use `shasum -a 256 --check` in place of `sha256sum --check`.
4. Make it executable: `chmod +x windmill-lite-<os>-<arch>`.
5. Check its version: `./windmill-lite-<os>-<arch> version` prints
   `windmill-lite v0.4.1`.

### Build a bundle

`windmill-lite build` needs the Bun
[build helper](../docs/glossary.md#build-helper), which neither the
release nor the package ships. Take it from a checkout of the release's
tag:

1. Clone the tag:
   `git clone --branch v0.4.1 https://github.com/invakid404/windmill-lite.git`.
2. Install the helper's dependencies:
   `(cd windmill-lite/harnesses/bun/build && bun install --frozen-lockfile)`.
3. Build the bundle with Bun 1.4.3 as `bun` on the `PATH` or in `WL_BUN`.
   This example builds the synthetic folder of the package's own test:

   ```sh
   WL_BUILD_HELPER=$PWD/windmill-lite/harnesses/bun/build/main.ts \
     ./windmill-lite-<os>-<arch> build windmill-lite/harnesses/npm/test/folder -o bundle
   ```

The [`build` command](../cmd/windmill-lite/README.md#build) describes its
flags, such as `-contract` for the
[host contract](../docs/glossary.md#host-contract).

## Run a flow from a host

This host runs the flow `f/smoke/hello` of the bundle above. Put it in the
directory that holds the engine and `bundle`, and run it there with
`bun host.ts`. It prints `success hello, lite`. On a platform other than
macOS arm64, change the engine's file name.

```ts
import { resolve } from "node:path";
import { connect } from "windmill-lite/bun";

const host = await connect({
  engine: resolve("windmill-lite-darwin-arm64"),
  bundle: resolve("bundle"),
  workdir: process.cwd(),
});
const run = await host.run("f/smoke/hello", { name: "lite" });
console.log(run.status, run.result);
await host.close();
```

- `connect()` starts the engine as `windmill-lite host`, and runs the
  steps in this process. Give `bundle` as an absolute path: in versions
  0.2.0, 0.3.0, 0.4.0 and 0.4.1, `connect()` fails on a relative one.
- `workdir` must be a writable directory that is already the process's
  working directory. Otherwise `connect()` rejects with
  `workdir_not_writable` or `workdir_mismatch`.
- `host.run()` runs the flow with its arguments and resolves with its
  `status`, `result`, `warnings` and other fields. It rejects with a
  `HostError` when the run does not start.
- `host.close()` ends the connection and resolves once the engine has
  exited. The host process lives on.

The [Bun harness README](bun/README.md#host-mode-and-the-host-sdk)
describes `connect()`'s options, runs and their options, `health()`,
`drain()` and [host plugins](../docs/glossary.md#host-plugin).

## Test a host's process-level code

A host's process-level code is its own code that runs in the process
where its steps run: its pooled database and cache clients, their timers,
and the work that it starts outside every step. When an
[invocation](../docs/glossary.md#invocation) of a step
[settles](../docs/glossary.md#settle), the
[settle-time fence](../docs/glossary.md#settle-time-fence) clears the
timers that the invocation still has.

Work started through the
[process scope](../docs/glossary.md#process-scope) belongs to no
invocation, and the fence keeps it. A host's pooled client captures the
process scope when it loads, and runs its factories through it
([the process scope](bun/README.md#the-per-invocation-context)).

The test entry `windmill-lite/bun/testing` lets a host's tests check what
the fence does to that code, without an engine or a bundle. It runs on Bun
1.4.3 and on Node 22 or newer, so on Node test runners such as vitest too.

| Function | What it does |
|---|---|
| `install()` | Installs the runtime's step machinery in this process, as the harness does before any step loads: the timer fence, the process scope (`globalThis[Symbol.for("windmill-lite.processScope")]`) and the step API (`wl`). A second call changes nothing. |
| `invoke(fn, options?)` | Runs `fn` as one invocation, then settles it with the runtime's own settlement code. It resolves with `{ result, left }`: what `fn` returned, and what settlement released, counted by kind, such as `{ interval: 1 }`, or `{}`. When `fn` throws, `invoke` rejects with that error once the invocation has settled. |
| `currentInvocation()` | The invocation of the caller's async context, as `{ id, run, settled }`, or `undefined` outside every invocation and in a process scope |

- `invoke`'s options set the invocation's `id` and `run`, each a random
  UUID by default.
- `fn` keeps the caller's other `AsyncLocalStorage` stores, and calls of
  `invoke` may nest.
- `invoke` awaits `fn`'s result inside the invocation. So the work that a
  returned thenable, such as a lazy query object, starts in its `then()`
  belongs to the invocation too.
- `left` counts `timeout`, `interval` and `immediate` handles.

In a process where the host SDK has connected, the connection also
installs these views, and so changes what `invoke` does:

- the `fetch` view and the child-process wrappers: `left` counts `fetch`,
  `child` and `detached` too;
- the log view: what `fn` writes through `console`, `process.stdout` and
  `process.stderr` goes to the invocation's log, which nothing prints and
  settlement drops. Without a connected host, that output reaches the
  process's streams.

To test a host's code:

1. Call `install()` in the test runner's setup file, before a test imports
   the host's code. On Bun 1.4.3, the first named import of `node:timers`
   fixes its named bindings for every later importer
   ([the timer fence](bun/README.md#the-per-invocation-context)).
2. Give the setup file to the runner: `bun test --preload ./test/setup.ts`,
   or vitest's `setupFiles`.
3. In each test, run the code that a step would run inside `invoke`, and
   check `left`.

```ts
// test/setup.ts
import { install } from "windmill-lite/bun/testing";

install();
```

This test passes: an interval that the invocation leaves running is what
the fence clears, and a timeout that the code clears itself is not.

```ts
// test/fence.test.ts
import { expect, test } from "bun:test";
import { currentInvocation, invoke } from "windmill-lite/bun/testing";

test("an interval started in an invocation is left for the fence", async () => {
  const { result, left } = await invoke(() => {
    setInterval(() => {}, 1000);
    return currentInvocation() !== undefined;
  });
  expect(result).toBe(true);
  expect(left).toEqual({ interval: 1 });
});

test("work that clears its own timer leaves nothing", async () => {
  const { left } = await invoke(() => clearTimeout(setTimeout(() => {}, 10)));
  expect(left).toEqual({});
  expect(currentInvocation()).toBeUndefined();
});
```

A host's real test runs its own code the same way, for example the first
query of a database pool inside `invoke`, and expects `left` to be `{}`
when the pool starts its maintenance timer through the process scope.

A process that imports both `windmill-lite/bun` and
`windmill-lite/bun/testing` has one copy of the runtime's state: one
invocation context, one timer fence and one process scope. So
`currentInvocation()` also reports the invocations of the steps that a
connected host runs.

## Types

The package ships JavaScript and type declarations (`.d.ts`), so a host's
`tsc` checks only the declarations, never the SDK's code.

- The SDK's declarations use Bun's type declarations, `@types/bun`, which
  `bun init` installs. TypeScript 6 and 7 and `bun check` load an `@types`
  package only when `types` names it, so set `"types": ["bun"]` in
  `tsconfig.json`, as `bun init` of Bun 1.4.3 does.
- The test entry's declarations import nothing, so a project without
  `@types/bun` can import it.

The packaging test type-checks these setups, with `skipLibCheck` off where
the table says so:

| Import | Setup |
|---|---|
| `windmill-lite/bun` and `windmill-lite/bun/testing` | TypeScript 7.0.2 with the `tsconfig.json` that `bun init` of Bun 1.4.3 writes, and `bun check` with the same file |
| `windmill-lite/bun` and `windmill-lite/bun/testing` | TypeScript 7.0.2, 6.0.3 and 5.9.3 with `NodeNext` module resolution and `skipLibCheck` off |
| `windmill-lite/bun/testing` | Node's type declarations only, with TypeScript 7.0.2, 6.0.3 and 5.9.3, `NodeNext` and `skipLibCheck` off |

## Work on the package

This section is for contributors who change the package.

### What the package holds

| File | What it is |
|---|---|
| `dist/bun/host/index.js` | `windmill-lite/bun`: the Bun host SDK and the [harness runtime](../docs/glossary.md#harness-runtime), bundled by Bun 1.4.3 into an ES module that imports `dist/bun/runtime/context.js`. Its harness version is `v` + the package's version. |
| `dist/bun/host/testing.js` | `windmill-lite/bun/testing`: the test entry, an ES module that imports `dist/bun/runtime/context.js` and Node's built-in modules only |
| `dist/bun/runtime/context.js` | The runtime module that both entries import: the invocation context, the timer fence and the process scope. A process that loads both entries has one copy of them. |
| `dist/bun/host/index.d.ts`, `dist/bun/runtime/*.d.ts` | The type declarations of the SDK, and of the runtime modules whose types its declarations import: `context`, `frame`, `plugin`, `runtime` and `version` |
| `dist/bun/host/testing.d.ts` | The type declarations of the test entry, which import nothing |
| `package.json`, `README.md`, `LICENSE` | The manifest, this README and the [Unlicense](LICENSE) |

`package.json` exports `./bun` and `./bun/testing`. Another runtime's SDK
becomes another subpath of this package.

### Build the package

`npm/build.ts`, the package's `prepack` script, builds `dist/`. So
`npm pack` and `npm publish` build first.

1. Install the build's dependencies, TypeScript and `@types/bun`:
   `(cd harnesses && bun install --frozen-lockfile)`.
2. Point `WL_BUN` at a Bun 1.4.3 executable. The build refuses any other
   Bun, because Bun's bundler transpiles the runtime.
3. Run `npm pack` in `harnesses/`.

What the build does:

- It reads the version from `package.json`, and builds the two entries,
  `bun/host/index.ts` and `bun/host/testing.ts`, in one `Bun.build` that
  defines `WL_RELEASE` as `"v<version>"`. It checks that
  `dist/bun/host/index.js` sets that harness version.
- The `Bun.build` splits the code that the two entries share into
  `dist/bun/runtime/context.js`. The build fails unless that module is the
  only one they share, holds only `runtime/context.ts`, and both entries
  import it. So the package has one copy of the runtime's state.
- `tsc -p npm/tsconfig.json` emits the declarations. The build keeps only
  those that `dist/bun/host/index.d.ts` and `dist/bun/host/testing.d.ts`
  import, directly or through another kept file. The declarations keep the
  sources' relative imports, which end in `.ts`; TypeScript resolves each
  to its `.d.ts` file.

### Test the package

`npm/test/check.sh` tests the package as a host gets it, and exits 1
at the first check that fails:

1. `npm pack` packs the package.
2. The tarball holds exactly the files of `npm/test/files.txt`. Its
   `package.json` has the fields that npm and provenance need and no
   dependencies, and its `LICENSE` is the repository's.
3. `bun add` installs the tarball into a copy of `npm/test/project/`, an
   empty project with the `tsconfig.json` of `bun init` and the
   TypeScript versions of [Types](#types).
4. `tsc` type-checks `npm/test/project/smoke.ts`, which imports both
   entries, and `npm/test/project/testing.ts`, which imports the test
   entry, in the setups of [Types](#types).
5. `testing.ts` checks the test entry's functions against the packed
   runtime, on Bun 1.4.3. Then it runs as the JavaScript that `tsc` emits
   from it on Node, which must be Node 22 or newer.
6. The engine of the checkout, stamped with the package's version as a
   release stamps it, builds a bundle of the synthetic folder
   `npm/test/folder/`. The smoke host runs its flow `f/smoke/hello`
   through the installed SDK, which must return `"hello, npm"`.
7. The smoke host also checks that the two entries share one runtime
   state. A process scope captured inside an `invoke` leaves its
   invocation, `connect()` keeps the timer functions that `install()`
   fenced, and `currentInvocation()` reports the invocation that runs the
   flow's step.
8. An engine stamped with another version builds its own bundle, and must
   refuse the smoke host with `version_mismatch`. So the packed SDK carries
   exactly the package's version.

To run it, install the dependencies of the package and of the
[build helper](bun/build/README.md), then run the script with
`WL_BUN` set:

```sh
(cd harnesses && bun install --frozen-lockfile)
(cd harnesses/bun/build && bun install --frozen-lockfile)
WL_BUN=/path/to/bun-1.4.3/bun harnesses/npm/test/check.sh
```

It also needs Node 22 or newer as `node` on the `PATH`, `npm`, `python3`,
and `go` with a C compiler.

- With `WL_TARBALL`, it checks that tarball instead of packing one.
- With `WL_ENGINE`, it uses that engine binary instead of building one.
- The [release workflow](../.github/workflows/release.yml) passes both,
  and runs it on Node 24.22.3.
- CI's Go jobs on Ubuntu and macOS run it on Node 22.23.5, as the step
  `Test the npm package`.

### Versions and releases

The `version` field of `package.json` is the one version of windmill-lite,
of this package and of the engine
([scripts/release.py](../scripts/README.md#release-helper)). A release is
the Git tag `v` + the version. The release workflow builds the engine
binaries, creates the GitHub Release, and publishes this package with npm
provenance through npm trusted publishing. [RELEASING.md](../RELEASING.md)
gives the steps.

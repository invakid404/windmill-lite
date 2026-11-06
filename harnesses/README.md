# windmill-lite on npm

**Current state:** the npm package `windmill-lite` holds one runtime's SDK,
`windmill-lite/bun`: the Bun host SDK, with the Bun harness runtime that it
imports, and the SDK's test entry, `windmill-lite/bun/testing`, which runs
on Bun and on Node. Its version is the version of the windmill-lite
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
| [Bun](../docs/glossary.md#bun) 1.4.3 | The steps run on the host's Bun, and the engine refuses a host on any other Bun with `runtime_mismatch`. |
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
3. Build the bundle with that checkout's helper, and with Bun 1.4.3 as
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

## Test a host's process-level code

A host's own process-level code runs in the process where its steps run:
its pooled database and cache clients, their timers, and the work it
starts outside every step. When an
[invocation](../docs/glossary.md#invocation) of a step
[settles](../docs/glossary.md#settle), the settle-time fence clears the
timers that the invocation still has. Work started through the process
scope belongs to no invocation, and the fence keeps it.

The test entry `windmill-lite/bun/testing` lets a host's tests check what
the fence does to that code, without an engine or a bundle. It runs on Bun
1.4.3 and on Node 22 or newer, so on Node test runners such as vitest
too.

| Function | What it does |
|---|---|
| `install()` | Installs the runtime's step machinery in this process, as the harness does before any step loads: the timer fence, the process scope (`globalThis[Symbol.for("windmill-lite.processScope")]`) and the step API (`wl`). A second call changes nothing. |
| `invoke(fn, options?)` | Runs `fn` as one invocation, then settles it with the runtime's own settlement code. It resolves with `{ result, left }`: what `fn` returned, and what settlement released, counted by kind, such as `{ interval: 1 }`, or `{}`. When `fn` throws, `invoke` rejects with that error once the invocation has settled. |
| `currentInvocation()` | The invocation of the caller's async context, as `{ id, run, settled }`, or `undefined` outside every invocation and in a process scope |

`invoke`'s options set the invocation's `id` and `run`, each a random UUID
by default. `fn` keeps the caller's other `AsyncLocalStorage` stores, and
calls of `invoke` may nest. `invoke` awaits `fn`'s result inside the
invocation, so the work that a returned thenable, such as a lazy query
object, starts in its `then()` is the invocation's too.

`left` counts `timeout`, `interval` and `immediate` handles. In a process
where the host SDK has connected, the connection also installs these
views, and so changes what `invoke` does:

- the `fetch` view and the child-process wrappers: `left` counts `fetch`,
  `child` and `detached` too;
- the log view: what `fn` writes through `console`, `process.stdout` and
  `process.stderr` goes to the invocation's log, which nothing prints and
  settlement drops. The test's output from inside `fn` is lost. Without a
  connected host, that output reaches the process's streams.

Call `install()` in the test runner's setup file, before a test imports
the host's code. On Bun 1.4.3 the first named import of `node:timers`
fixes its named bindings for every later importer
([the timer fence](bun/README.md#the-per-invocation-context)).

```ts
// test/setup.ts: vitest's setupFiles, or bun test's --preload
import { install } from "windmill-lite/bun/testing";

install();
```

This test fails when the pool's first query, made inside a step, starts
the pool's maintenance timer in that step's invocation, where the fence
clears it:

```ts
// test/pool.test.ts
import { expect, test } from "vitest";
import { invoke } from "windmill-lite/bun/testing";
import { query } from "../src/db.ts";

test("a step's first query leaves nothing for the fence to clear", async () => {
  const { left } = await invoke(() => query("select 1"));
  expect(left).toEqual({});
});
```

A process that imports both `windmill-lite/bun` and
`windmill-lite/bun/testing` has one copy of the runtime's state: one
invocation context, one timer fence and one process scope. So
`currentInvocation()` also reports the invocations of the steps that a
connected host runs.

## Types

The package ships JavaScript and type declarations (`.d.ts`), so a host's
`tsc` checks only the declarations, never the SDK's code. The packaging
test type-checks imports of `windmill-lite/bun` and of
`windmill-lite/bun/testing` with:

- TypeScript 7.0.2 and the `tsconfig.json` that `bun init` of Bun 1.4.3
  writes, and `bun check`, the type check that the `typecheck` script of
  `bun init` runs, with the same `tsconfig.json`;
- TypeScript 7.0.2, 6.0.3 and 5.9.3 with `NodeNext` module resolution,
  and with `skipLibCheck` off, so that `tsc` checks the declarations too.

The SDK's declarations use Bun's type declarations, `@types/bun`, which
`bun init` installs. TypeScript 6 and 7 and `bun check` load an `@types`
package only when `types` names it, so such a project sets
`"types": ["bun"]`, as `bun init` of Bun 1.4.3 does.

The test entry's declarations import nothing, so a project without
`@types/bun` can import it. The packaging test checks an import of
`windmill-lite/bun/testing` with Node's type declarations only, with
TypeScript 7.0.2, 6.0.3 and 5.9.3, `NodeNext` and `skipLibCheck` off.

## What the package holds

| File | What it is |
|---|---|
| `dist/bun/host/index.js` | `windmill-lite/bun`: the Bun host SDK and the harness runtime, bundled by Bun 1.4.3 into an ES module that imports `dist/bun/runtime/context.js`. Its harness version is `v` + the package's version. |
| `dist/bun/host/testing.js` | `windmill-lite/bun/testing`: the test entry, an ES module that imports `dist/bun/runtime/context.js` and Node's built-in modules only |
| `dist/bun/runtime/context.js` | The runtime module that both entries import: the invocation context, the timer fence and the process scope. A process that loads both entries has one copy of them. |
| `dist/bun/host/index.d.ts`, `dist/bun/runtime/*.d.ts` | The type declarations of the SDK, and of the runtime modules whose types its declarations import: `context`, `frame`, `plugin`, `runtime` and `version` |
| `dist/bun/host/testing.d.ts` | The type declarations of the test entry, which import nothing |
| `package.json`, `README.md`, `LICENSE` | The manifest, this README and the [Unlicense](LICENSE) |

`package.json` exports `./bun` and `./bun/testing`. Another runtime's SDK
becomes another subpath of this package.

## Build the package

`npm/build.ts`, the package's `prepack` script, builds `dist/`. So
`npm pack` and `npm publish` build first.

1. Install the build's dependencies, TypeScript and `@types/bun`:
   `(cd harnesses && bun install --frozen-lockfile)`.
2. Point `WL_BUN` at a Bun 1.4.3 executable. The build refuses any other
   Bun, because Bun's bundler transpiles the runtime.
3. Run `npm pack` in `harnesses/`.

The build reads the version from `package.json`. It builds the two
entries, `bun/host/index.ts` and `bun/host/testing.ts`, in one `Bun.build`
that defines `WL_RELEASE` as `"v<version>"`. It checks that
`dist/bun/host/index.js` sets that harness version.

The `Bun.build` splits the code that the two entries share into
`dist/bun/runtime/context.js`, which both import. The build fails unless
that module is the only one they share, holds only `runtime/context.ts`,
and both entries import it. So the package has one copy of the runtime's
state.

Then `tsc -p npm/tsconfig.json` emits the declarations. The build keeps
only those that `dist/bun/host/index.d.ts` and
`dist/bun/host/testing.d.ts` import, directly or through another kept
file. The declarations keep the sources' relative imports, which end in
`.ts`; TypeScript resolves each to its `.d.ts` file.

## Test the package

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
   `npm/test/folder/`. The smoke host runs
   its flow `f/smoke/hello` through the installed SDK, which must return
   `"hello, npm"`.
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
and `go` with a C compiler. With `WL_TARBALL` it checks that tarball
instead of packing one, and with `WL_ENGINE` it uses that engine binary
instead of building one. The
[release workflow](../.github/workflows/release.yml) passes both, and runs
it on Node 24.22.1. CI's Go jobs on Ubuntu and macOS run it on Node
22.23.4, as the step `Test the npm package`.

## Versions and releases

The `version` field of `package.json` is the one version of windmill-lite,
of this package and of the engine
([scripts/release.py](../scripts/README.md#release-helper)). A release is
the Git tag `v` + the version. The release workflow builds the engine
binaries, creates the GitHub Release, and publishes this package with npm
provenance through npm trusted publishing. [RELEASING.md](../RELEASING.md)
gives the steps.

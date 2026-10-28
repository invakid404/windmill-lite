# 0010 · Packaging, build identity and drift guard

**Status:** proposed · **Topic:** the bundle format, the single executable
for single-harness selections, the OCI image, build identity, and the guards
that keep what runs equal to what was built.

## Context

Once flows are data, a deployed worker must prove which flows, scripts,
locks and runtimes it carries. A Windmill job runs on a pinned Bun
(`1.3.10` in the reference image, [WR-3](../semantics/languages.md#wr-3)) and
bundles each script with fixed settings ([WR-27](../semantics/languages.md#wr-27)).
Practices that worked for a compiled Bun worker carry over: refuse to build on
an unpinned Bun, keep the bundler's metafile, write a release manifest with
digests, answer health checks and a build-info call, and let the host pin the
executable by digest.

## Decision

### The bundle

A bundle is a directory, or the same tree as one `.wlb` file (a tar archive):

| Path | Content |
|---|---|
| `manifest.json` | format version; the exact engine version that built it; the digest of every other file; the source inventory (every input file of the sync folder with its digest, locks included); the host contract (roots, provided sites, host-provided modules); the Bun and esbuild versions; deviations in effect ([0001](0001-equivalence-contract.md)); module-state mode; harness runtime digest |
| `ir.json` | the compiled program ([0002](0002-ir-and-loader.md)), a cache for that exact engine version, not an exchange format |
| `scripts/bun/<id>.mjs` | one bundled module per Bun script, plus `<id>.meta.json` (the bundler's metafile and the parsed signatures of every export) |
| `scripts/bun/provided/<id>.mjs` | a bundled copy of each host-provided module, used when no host registers its own |
| `assets/` | codebase assets, extracted into a harness process's working directory at start ([0002](0002-ir-and-loader.md)) |
| `data/` | resources, resource types and non-secret variables from the folder |
| `plugins/` | host plugins, when the host ships any ([0008](0008-host-hooks.md)) |

Secrets are never written into a bundle unless the host passes them
explicitly. A runtime opens a bundle only if the manifest's engine version is
its own; `ir.json` carries no compatibility promise across versions, so a
host upgrades the engine and rebuilds the bundle together.

### The build

`windmill-lite build <folder> -o <bundle>`:

1. loads and compiles the folder ([0002](0002-ir-and-loader.md));
2. runs the **Bun build helper** on the pinned Bun, refusing any other
   version unless `--bun-mismatch` is given, which marks the manifest
   `nonconformant_runtime`;
3. bundles each script with Windmill's settings: target `bun`, ESM,
   syntax-only minification, `electron` external
   ([WR-27](../semantics/languages.md#wr-27)); pinned-version removal and the
   `.ts` import rewrite as textual passes ([WR-26](../semantics/languages.md#wr-26),
   [WR-28](../semantics/languages.md#wr-28)); the inline import base
   ([WR-29](../semantics/languages.md#wr-29)); relative imports resolved from
   the folder; `//nobundling` scripts bundled too (deviation D7). Codebase
   scripts are bundled with esbuild instead, through its Go API, with the
   settings of [0002](0002-ir-and-loader.md) (WR-30, SY-8);
4. parses each script's signature and annotations into its metafile
   ([WR-6](../semantics/languages.md#wr-6), [WR-8](../semantics/languages.md#wr-8),
   [WR-31](../semantics/languages.md#wr-31), [WR-32](../semantics/languages.md#wr-32),
   [WR-39](../semantics/languages.md#wr-39)), and warns about each static
   import of a native script that the native runtime cannot load
   (`native_import_unavailable`, D5): the step still builds, since Windmill
   fails it only when it runs. A missing global is not checked; it fails at
   the reference, as on Windmill;
5. applies the **env rewrite** for per-invocation environments
   ([WA-31](../semantics/client-api.md#wa-31)) in both bundlers, from one
   define table and one support module owned by the Go build. The support
   module defines, once per process, a global proxy of the real `Bun` whose
   `env` is `process.env`. Every reference to the global `Bun`, also through
   `globalThis`, `global` or `self` (so an alias, or destructuring such as
   `const { env } = Bun`), becomes that global; `Bun.env` (also written
   `Bun["env"]`) becomes its `env` in every output, and `import.meta.env`
   does only in ESM output. In CommonJS output `import.meta` stays empty, as
   it does in the CLI's esbuild bundles, so code that reads
   `import.meta.env` there fails as it would on Windmill. A rewrite at run
   time cannot do this: on Bun 1.3.10 the global `Bun` and its `env` are
   read-only and non-configurable. Every bundle imports the support module
   before its own code; being a module of its own, its references to the
   real globals are kept apart from a script's bindings by the bundler,
   which renames those. The global's name is one that no input of the
   bundle spells, so no binding of a script can capture a replaced
   reference. What a define cannot see
   reads the real environment: imports of the `bun` module, optional
   chaining or reflection through the global object, and code built at run
   time. The Bun helper applies the rewrite to Bun-target bundles; `//native`
   and `//nodejs` scripts do not run with Bun's globals on Windmill, and
   their environment is the native boundary's (C3n);
6. resolves imports of **host-provided modules** (below) to generated shims,
   in both bundlers;
7. writes the manifest last.

**One implementation for both bundlers.** Ordinary scripts are bundled by
`Bun.build` in the Bun helper and codebase scripts by esbuild in Go
([0002](0002-ir-and-loader.md)). The windmill-lite-specific transforms are
written once: the Bun helper scans the export names of each host-provided
module (`Bun.Transpiler().scan`, the one scanner); the Go build generates
one shim source file per module into the bundle tree; and each bundler
resolves imports of a listed module to that same file through a plugin that
only maps paths, so neither plugin has logic of its own. The define table of
step 5 is passed to both bundlers. Measured (prototype outside the
repository): an esbuild bundle of the synthetic step, in CJS and in ESM,
resolving the module to the generated shim, read the host's store in 16 of
16 concurrent invocations on Bun 1.3.10 and 1.4.2, and the define table
rewrote `Bun.env` in both outputs and `import.meta.env` in ESM only.

### Host-provided modules

The host contract can list module paths (source files of the folder or its
codebase, or package specifiers) whose one instance the host and every
script must share: typically modules holding an `AsyncLocalStorage` the host
enters, or a registry the host fills. Without this, each step bundle inlines
its own copy, and a scope the host enters is invisible to step code. Measured
on Bun 1.3.10 and 1.4.2 (prototype outside the repository): a host and a
step built separately from the same module, in one process, with the host
entering the module's `AsyncLocalStorage` and re-entering a snapshot for the
step, read `undefined` in the step; with the mechanism below they read the
host's value in 16 of 16 concurrent invocations of two runs, including when
the host was a compiled Bun executable.

1. **Build.** The build helper resolves each import of a listed module, by
   a script or by one of its dependencies, to a generated shim that takes
   every export, by name, from a process-wide registry: a `Map` at
   `globalThis[Symbol.for("windmill-lite.hostModules")]` from the module's
   key in the host contract to its exports. Export names come from the one
   scan above, so a host-provided module must be an ES module: a CommonJS
   module's names are its exports object's properties, known only when it
   runs, and it is refused with `host_module_commonjs`. A shim cannot follow
   a live binding, so a module that declares
   `export let` or `export var` is refused with `host_module_live_binding`;
   other ways of exporting a binding that is later reassigned are not
   detected, and the shim then sees the value as it was when the step's
   module loaded. A listed module that imports another listed module is
   registered after it; a cycle fails the build (`host_module_cycle`).
2. **Host mode.** The Bun host SDK registers the host's own instances (the
   modules the host's program imported) before any step module loads.
3. **Elsewhere.** The harness loads the bundled copy from
   `scripts/bun/provided/` once per process and registers it, so every
   script in the process shares that one instance.
4. **Missing.** A step that imports a host-provided module the registry
   lacks fails at module load with `host-provided module not registered: <path>`.
5. **Not listed** means per-script, as on Windmill. A host lists only modules
   that must be shared; a client library that must stay per step (so that
   its import-time configuration is the facade's, not the host's) is not
   listed.
6. **Transitive imports.** A listed module's own imports come with it: in
   host mode they are the host's instances, elsewhere the bundled copy's. So
   a library that a listed module imports reaches steps with the host's
   import-time configuration whenever steps call through the listed module.
   The build warns (`host_module_shared_dependency`) when a listed module
   imports a package that a step also imports directly, since that package
   then exists twice with different configuration.

This departs from Windmill, which inlines every import into each script's
bundle ([WR-27](../semantics/languages.md#wr-27)): it is deviation D12, and the
manifest lists the modules it applies to.

The same inputs and pins produce the same bundle digest; CI builds a fixture
bundle twice and compares.

### Build identity

The **bundle digest** is the SHA-256 of the canonical `manifest.json`. Since
the manifest lists the digest of the IR, of every built script, of every
source file and lock, of the host contract and of the harness runtime, any
change to a flow (one `stop_after_if`, say), a script, a lock, a pin or the
host contract changes the digest. It appears in:

- `GetBuildInfo` and `/healthz`, with engine, harness and Bun versions;
- the harness handshake, which fails on a mismatch ([0004](0004-harness-protocol.md));
- every run trace;
- generated typed clients ([0009](0009-host-adapters.md)).

### Single executable

For a bundle with one harness class, `windmill-lite pack --single` writes one
file per platform (`linux/amd64`, `linux/arm64`, `darwin/arm64`):

- the Go engine with the bundle appended;
- the Bun harness compiled with `bun build --compile`, the script modules
  embedded, appended as well.

On start the executable extracts the harness into a private directory
(mode 0700), checks its digest against the manifest, and spawns it. `pack
--single --host bun` builds the mirror image for host mode: a compiled Bun
host program that carries the engine binary and starts it
([0009](0009-host-adapters.md)).

### OCI image

`windmill-lite image <bundle>` writes an OCI image layout (or pushes it) with
a pure-Go image library, so no Docker daemon is needed. The image holds the
single executable and its manifest, with the chosen adapter as entrypoint,
on the smallest base that runs it: an empty base is enough for the static
engine, while the compiled Bun harness inside the single executable links a
C library, so the base must provide one. F6's exit gate runs the image and
records the base in the manifest. No windmill-lite image is ever based on, or contains, a
Windmill image ([OR-3](../semantics/oracle.md#or-3)); a CI lint enforces it
([0011](0011-differential-testing.md)).

### Drift guard

1. The build refuses an unpinned Bun.
2. The harness and the engine refuse each other on a protocol, runtime or
   bundle-digest mismatch.
3. `windmill-lite verify <artifact>` recomputes every digest against a
   release manifest, for release jobs and for hosts that pin artifacts.
4. A host that pins the executable by digest (go-plugin `SecureConfig`) and
   compares `GetBuildInfo` with its release record catches a swapped binary
   and a swapped bundle.
5. The Linux engine binaries are static (built with `zig cc` against musl,
   [0003](0003-semantics-placement.md)); F4's exit check starts each one on an
   empty base and fails the release if it does not run there.

## Consequences

- A deployed worker can prove which flows it runs, not just which code.
- Milestone C2a built the Bun side (`internal/bundle`, `harnesses/bun/build`)
  and found that Bun 1.3.10's global `Bun` and its `env` are read-only and
  non-configurable, so step 5 gained the support module: the define table
  alone missed aliases and destructuring of `Bun`. A first version put the
  proxy in a banner, where a script's own `Object`, `process` or
  `globalThis` captured its names; the support module and a binding name no
  input spells replaced it in the first fix round. A script that
  Windmill would fail to bundle at every run fails the build instead
  (`bundle_failed`, [0001](0001-equivalence-contract.md) §5a). The build
  helper's one dependency is its TypeScript parser, `@babel/parser` (MIT),
  installed from its lockfile; the signature parse needs an AST, and Bun
  exposes none. `Bun.build` inlines `process.env.NODE_ENV` from its own
  environment, so the helper runs without one, and every bundle reads
  `"development"` there; which value Windmill's worker build sees is a
  question for the semantics team.
- The pinned Bun is the reference's; a host on another Bun gets a visible
  `nonconformant_runtime` flag, not a silent difference.
- One-file deployment is available for the common case of a single language.
- Milestone F4 built the bundle (`internal/bundle`, `windmill-lite build`,
  `verify` and `selfcheck`) and settled what this record left open:
  - **Names.** A script's files are named by its inventory id
    ([0002](0002-ir-and-loader.md)), whose `/`-separated segments become
    directories (`scripts/bun/f/app/report.mjs`), as the harness runtime
    already resolved an id; a host-provided module's by its script path.
  - **Canonical form.** `manifest.json` is written in its canonical form:
    keys sorted by their UTF-8 bytes, numbers as written, minimal string
    escapes, two-space indentation and a final newline. The bundle digest is
    the SHA-256 of those bytes, which the harness computes from the file. A
    manifest in any other form is refused.
  - **Manifest.** Beyond the table above, it records the expression
    engine's stack limit ([0003](0003-semantics-placement.md)), the
    host-provided modules in registration order, and the harness runtime
    as the digest of its directory tree, which refuses a symbolic link
    below the runtime's directory. Its deviations are the program's,
    those of the module-state mode (D1, D2, D9 to D11 in `warm`) and those
    every bundle has (D3, D8, D14).
  - **Source inventory.** It lists the files the build read: what the
    compiler read through a recording file system, the workspace scripts
    the bundles inline, and each host-provided module's file, metadata and
    lock. A file nothing reaches is not an input, so editing it leaves the
    digest. Nor is a secret variable's file: with `plainSecrets` its digest
    would let a reader of the manifest test guesses of the value.
  - **`data/`** holds one JSON file per kind: resources, resource types,
    and variables without secrets, whose values a folder holds as
    ciphertext or, with `plainSecrets`, plain text; the host supplies
    secrets.
  - **Archive.** A `.wlb` is a tar file with `manifest.json` first, then the
    listed files in path order, with fixed headers, so one bundle gives one
    archive, byte for byte. Reading one, `verify` accepts only regular files
    and the bundle's own directories, with clean relative names that form
    one tree even where names compare without case (as on macOS), so an
    archive that verifies extracts to the tree its manifest lists; a
    manifest whose paths would collide that way is refused.
  - **`verify`** checks the files against a release manifest when given one
    (and the bundle's own manifest must then be its bytes), otherwise
    against the bundle's own: alone, that proves a bundle intact, not that
    it is the one released.
  - **Refusals.** The build applies validation rule 5 of
    [0002](0002-ir-and-loader.md) (`missing_lock`) and refuses
    workflow-as-code (`unsupported: workflow_as_code`, from the helper's
    markers). Until their milestones, it also refuses codebase scripts
    (C2b) and host-provided packages: a package needs a lock to install
    from, and the host contract has no place for one yet.
  - **Drift guard 5** is the `Static Linux engine` CI job: it builds the
    engine with `zig cc` against musl for `amd64` and `arm64`, checks that
    it is static, and runs `windmill-lite selfcheck`, which starts the
    expression engine, and `verify` in an image built `FROM scratch`.
- Milestone C3n built native scripts in their **native form**,
  `scripts/bun/<id>.native.js`: the source of one async function, which the
  harness compiles in the invocation's realm with the environment preamble
  inserted at a recorded offset, and calls with the static imports it
  resolved and linked. An ES module cannot be evaluated in such a realm:
  Bun 1.3.10's `vm.SourceTextModule` ends the process on top-level `await`.
  The script's metadata records the form (`boundary`, `native`).
  - A **nativets** script is transpiled, not bundled
    ([WR-40](../semantics/languages.md#wr-40)); one that does not transpile
    builds, and its diagnostic fails the step when it runs
    ([WR-43](../semantics/languages.md#wr-43)).
  - A **`//native`** script is bundled for the browser target, without the
    env rewrite, and inlines host-provided modules instead of using their
    shims: a realm does not reach the process-wide registry, so D12 does not
    extend to native steps.
  - The **embedded client** that a nativets script imports as
    `./windmill.ts` is `windmill-client@1.326.0` (Apache-2.0), pinned in the
    Bun helper's lock, bundled as CommonJS for the browser target into
    `scripts/native/windmill.js` when the bundle has a nativets script.
- Milestone W1 measured on Bun 1.3.10 that the harness's environment view
  isolates `Bun.env` and `import.meta.env` reads at run time even without
  the env rewrite of step 5 ([0004](0004-harness-protocol.md)), including
  in unbundled code. Step 5 stays: it keeps a bundle's reads on
  `process.env`, the one name the view replaces, so their isolation does
  not depend on the run-time mechanism alone.

## Spec IDs relied on

WR-3, WR-6, WR-8, WR-25 to WR-32, WR-38 to WR-40, WR-43, WR-45, WA-31, WA-33,
SY-8, OR-3.

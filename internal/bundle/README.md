# internal/bundle

**Current state:** milestones C2a, F4 and C2b of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md). This README
covers `internal/bundle` and its `testdata/`. The package builds
windmill-lite bundles ([ADR 0010](../../docs/design/0010-packaging.md)):

- `Build` loads and compiles a sync folder with its host contract, bundles
  its Bun scripts and host-provided modules, and writes the bundle with its
  manifest; `windmill-lite build` calls it;
- the manifest, its canonical encoding and the bundle digest; the `.wlb`
  archive; `Verify`, which `windmill-lite verify` calls;
- the build parts both bundlers share (the env rewrite, the shim generator,
  the import base of inline scripts), `BuildBun`, which runs the
  [Bun build helper](../../harnesses/bun/build/README.md), and
  `BuildCodebase`, which bundles codebase scripts with esbuild through its
  Go API, with the same env rewrite and shims, and carries their assets.

A build asks for shared code with `Config.SharedCode`
([ADR 0013](../../docs/design/0013-shared-module-code.md), below).

[wl](../../wl/README.md)`.Open` (F1) opens a bundle to run it: it calls
`Verify`, or `Extract` for an archive, and `TreeDigest` for the harness
runtime.

## Files

| File | Contents |
|---|---|
| `build.go` | `Build` and `Config`: the whole build, its refusals, `data/` |
| `manifest.go` | `Manifest`, `Canonicalize`, `Digest`, `ParseManifest`, the deviations a bundle lists, `ModeDeviations` |
| `sources.go` | the recording file system behind the source inventory; `TreeDigest` |
| `archive.go` | `WriteArchive`, `WriteArchiveFile`, `Verify` and `Extract` |
| `env.go` | `EnvRewrite`: the define table and support module of the env rewrite |
| `shim.go` | `Shim`: the shim of a host-provided module; `LateShim`, its late shim; the registry key |
| `importbase.go` | `ImportBase`: an inline script's import base |
| `bun.go` | `BuildBun`: the two helper runs, the shims, the metadata, the registration order and warnings |
| `loadorder.go` | The registration order of host-provided modules: which listed modules each imports at load time (`loadTimeHostModules`), and the order (`hostModuleOrder`) |
| `esbuild.go` | `CodebaseSettings` (a codebases entry, WR-47), `CLIOptions` and `CLIBundle` (the pinned CLI's build), `BuildCodebase`, assets |
| `outside.go` | `BuildEsbuildHostModules`: the host-provided modules esbuild builds (those outside the folder, and listed codebase scripts); `OutsideID` |
| `meta.go` | `ScriptMeta` and the other result types, problems and warnings; `SharedMeta` and the fallback reasons of shared code |
| `sharedcode.go` | Shared code ([ADR 0013](../../docs/design/0013-shared-module-code.md)): the codebase groups, their closures and shared-layout modules, the dependency plugin and inject proxies, the fallbacks, the closure wrapper, `ClosureRegistry` |

## The bundle

A bundle is a directory, or the same tree as one `.wlb` archive:

| Path | Content |
|---|---|
| `manifest.json` | the manifest (below), in canonical form |
| `ir.json` | the compiled program, `(*ir.Program).MarshalIndent` ([internal/ir](../ir/README.md)) |
| `scripts/bun/<id>.mjs`, `<id>.meta.json` | each Bun script's module and metadata (`BuildBun`, below), named by its inventory id: `scripts/bun/f/app/report.mjs` for the path script `f/app/report`, `scripts/bun/inline-<16 hex>.mjs` for an inline step, as the harness runtime resolves an id; a native-boundary script's module is `<id>.native.js` instead |
| `scripts/bun/<id>.cjs`, `<id>.meta.json` | a codebase script built as CommonJS (`BuildCodebase`, below); one built as ESM is `<id>.mjs` |
| `assets/<to>` | every codebase entry's assets, at the path in the working directory they are extracted to |
| `scripts/native/windmill.js`, `windmill.meta.json` | the native runtime's embedded client, when a script is nativets |
| `scripts/bun/closures/<id>.cjs`, `<id>.mjs` | with shared code, each build group's closure (below), an ES module for an ES module codebase group |
| `scripts/bun/<id>.shared.mjs`, `<id>.shared.cjs` | with shared code, each script's shared-layout module, beside its per-script bundle |
| `scripts/bun/provided/<id>.mjs`, `provided/shims/<id>.mjs` | each host-provided module's bundled copy and shim, named by its script path, or `outside/<n>/<path>` for a module outside the folder |
| `data/resources.json`, `data/resource_types.json`, `data/variables.json` | every resource and resource type the folder keeps, and every variable but secrets, each file an object keyed by path (a resource type by name), with the record of [internal/syncfolder](../syncfolder/README.md) |

`plugins/` (host plugins, F2c) arrives with its milestone. A secret variable is never written: its value in a
folder is ciphertext (or plain text, with `plainSecrets`), and the host
supplies secrets (PF2).

## The manifest and the bundle digest

`manifest.json` holds:

| Field | Holds |
|---|---|
| `format` | `FormatVersion`, 1 |
| `engine` | `version`, the exact engine version that built the bundle, and `expression_stack_bytes`, the expression engine's stack limit, 1 MiB, which is observable ([ADR 0003](../../docs/design/0003-semantics-placement.md), [JE-48](../../docs/semantics/expressions.md#je-48)) |
| `bundlers` | the Bun version that built the scripts; esbuild's (`EsbuildVersion`, 0.28.0) when the bundle has codebase scripts |
| `nonconformant_runtime` | set when the Bun was not the pin (`-bun-mismatch`) |
| `args_file` | set when a built script names `args.json`, the file in which Windmill's worker leaves a job's arguments ([WR-5](../../docs/semantics/languages.md#wr-5)): host mode writes the file only for such a bundle ([ADR 0004](../../docs/design/0004-harness-protocol.md), host mode item 7) |
| `module_state` | `warm` or `fresh` ([ADR 0006](../../docs/design/0006-module-state.md)) |
| `shared_code`, `closures` | set for a bundle built with shared code, and the ids of its closures ([ADR 0013](../../docs/design/0013-shared-module-code.md)). Such a bundle in `warm` mode also lists [D18](../../docs/design/0001-equivalence-contract.md#5-named-deviations), the differences of shared code, among its `deviations`, and `ModeDeviations` swaps it as it swaps the mode's other deviations |
| `deviations` | the deviations of [ADR 0001](../../docs/design/0001-equivalence-contract.md) §5 in effect: the program's (D5, D6, D7, D12, D13, D19), D1, D2 and D9 to D11 in `warm` mode, and D3, D8, D14, D16 and D17, which every bundle has. Those that depend on a run-time setting (D4, D15, D2's watchdog) are the run trace's. `ModeDeviations` swaps the mode's deviations for another mode's and keeps the rest, which `wl` reports when a runtime overrides the bundle's mode |
| `contract` | the host contract compiled against; with none, every flow and script of the folder is a root |
| `host_modules` | the host-provided modules (D12), `key` and `id`, in registration order: a module after the modules it imports |
| `harness_runtimes` | by harness class, the `TreeDigest` of the harness runtime the bundle was built for |
| `sources` | the digest of every file of the sync folder the build read, by its path from the sync root (below) |
| `files` | the digest of every other file of the bundle |

A digest is `sha256:` and 64 hexadecimal digits. The manifest is written in
its **canonical form** (`Canonicalize`): object keys sorted by their UTF-8
bytes, numbers as written, strings escaped only where JSON requires it
(and U+2028, U+2029), two-space indentation and a final newline. The
**bundle digest** is the digest of those bytes, which is what the harness
runtime computes from `manifest.json` for its handshake.
`ParseManifest` refuses a manifest that is not in canonical form, has an
unknown field, another format, a file path that is not a clean relative
path, paths that would not extract to one tree where names compare without
case (two that differ only in case, or a file that is a directory of
another), or a host-provided module whose bundled copy it does not list.

**Sources.** The build reads the folder through a file system that records
every file read: `wmill.yaml`, the flows and scripts the roots reach with
their metadata, locks and `!inline` files, and the data files. It adds the
workspace scripts that bundles inline and each host-provided module's
file, metadata and lock, which the Bun helper reads itself, and what
esbuild read for each codebase script inside the folder (its metafile's
inputs, outside `node_modules`) with the sources of its assets. A package
or a file outside the folder that a codebase bundle inlines reaches the
digest through the bundled module itself. Files the build
does not read, such as a script nothing reaches, are not inputs. Nor is a
secret variable's file: with `plainSecrets` it holds the value in plain
text, and its digest in a shipped manifest would let anyone test guesses of
the value.

**What changes the digest:** any source, a lock, the contract, the
module-state mode, the engine version, the Bun version, the harness
runtime, and any built file. `TreeDigest(dir)` is the digest of the
canonical JSON object that maps the slash path of every regular file under
`dir` (skipping `node_modules` and names that start with `.`) to its digest.
It refuses a symbolic link below `dir`, or any other file that is not
regular: Bun loads through a link, so a digest that skipped one would not
change with what runs. `dir` itself may be a link.

## Build

`Build(ctx, Config)`:

1. finds the sync root from `Config.Folder`, reads the folder, parses the
   contract and compiles the program (`ir.Compile`, with `Strict`); the
   compiler's errors come back as they are;
2. reads `data/`;
3. resolves the contract's `host_modules`: each must be the file of a Bun
   script of the folder (`f/shared/ctx.ts`), whose path becomes its id, or
   a file outside the folder in a codebase entry's `relative_path`, named
   by its path from the sync root (`../lib/ctx.ts`), whose id is
   `OutsideID`'s (`outside/1/lib/ctx.ts`); a script that fails to read is
   refused with its load error, and an entry that leaves the root but
   names no such file with `host_module_unknown`;
4. runs three builds in this order, each with the shims that the earlier
   ones wrote:
   - `BuildBun` for every script of the inventory that is not a codebase
     script, and for the listed modules that are neither codebase scripts
     nor outside the folder, with every `.ts` script of the folder
     available to workspace imports;
   - `BuildEsbuildHostModules` for the other listed modules;
   - `BuildCodebase` for the codebase scripts, each with the settings of
     the `codebases` entry that selects it
     ([SY-8](../../docs/semantics/openflow.md#sy-8)).

   A Bun script or a module of the Bun build that imports a listed codebase
   script is refused with `unsupported_feature`: the Bun build has no shim
   for it, so it would inline a second instance;
5. checks each script's metadata: workflow-as-code by any detection path is
   refused with `unsupported_feature`, `unsupported: workflow_as_code`
   ([ADR 0001](../../docs/design/0001-equivalence-contract.md) §3;
   [WA-33](../../docs/semantics/client-api.md#wa-33),
   [WR-32](../../docs/semantics/languages.md#wr-32)), and a script whose
   own source imports a package and has no lock (nil or blank) is refused
   with `missing_lock`
   ([ADR 0002](../../docs/design/0002-ir-and-loader.md), validation 5): a
   lock is the only thing the build installs from. An empty lock with no
   package imports builds. A codebase script has no lock: esbuild inlines
   its packages from the folder's `node_modules`, as the CLI does
   ([WR-47](../../docs/semantics/languages.md#wr-47));
6. warns `host_module_shared_dependency` for each codebase script that
   imports a package directly that a listed module it uses also imports,
   as `BuildBun` warns for Bun scripts (below);
7. writes `ir.json` and `data/`, then the manifest, last.

Before bundling it refuses, with `unsupported_feature`, a host-provided
module that is neither a script of the folder nor outside it: a
host-provided package would need a lock, which the contract cannot give
yet. Problems with scripts and modules come back
together as a `*BuildError`, sorted by item; a path script is named
`script <path>`, an inline step `<flow> step '<id>' (script <inventory id>)`
(`ScriptItem`, so a caller can tell which of its roots reach the script).

`Config.Out` must not exist or be empty. The build writes a sibling
directory and renames it to `Out` last, so a failed build leaves nothing
there. `WriteArchive` writes a bundle directory as a `.wlb`: a tar file
whose first entry is `manifest.json`, then every file the manifest lists in
byte order of its path, each with fixed headers (mode 0644, owner 0,
modification time 0, PAX format), so one bundle gives one archive. A file
that no longer matches the manifest fails the write.

## Verify

`Verify(artifact, VerifyOptions)` reads a bundle directory or `.wlb`,
recomputes the digest of every file, and checks them against a manifest:
`VerifyOptions.Release`, a release manifest, when given (and the bundle's
own manifest must then be the same bytes), otherwise the bundle's own.
`VerifyOptions.Digest` pins the bundle digest. The artifact must hold
exactly the listed files, as regular files. An archive's entries must be
regular files, each named once, or directories, all with clean relative
names (a leading `./` and a directory's trailing `/` aside). They must form
one tree as extraction makes it where names compare without case, as on
macOS by default: no two names may differ only in case, no name may be both
a file and a directory, and nothing may lie below a file, whatever the
order of the entries. A directory entry must also be a directory of the
bundle, spelled as the manifest spells it. Otherwise extracting a verified
archive could replace a file the manifest lists with a directory.
Differences come back together as a `*MismatchError`. A
bundle checked only against its own manifest proves it is intact, not that
it is the one released: a changed file with a manifest rewritten to match
passes alone and fails against the release manifest or digest.

`Extract(archive, dir, VerifyOptions)` writes a `.wlb` into `dir`, which
must not exist or be empty: it verifies the archive first, so its entries
are clean relative names that make one tree, writes every regular entry
with mode 0644 and without following or replacing anything, and verifies
the written directory again against the archive's digest.

## Codebase scripts

A codebase script is a Bun script that a `codebases` entry of `wmill.yaml`
selects ([SY-8](../../docs/semantics/openflow.md#sy-8)); Windmill's CLI
bundles it with esbuild at deploy time
([WR-30](../../docs/semantics/languages.md#wr-30)). `BuildCodebase(ctx,
CodebaseConfig)` bundles each with esbuild through its Go API, pinned in
`go.mod` at the CLI's version, 0.28.0 (`EsbuildVersion`,
[WR-47](../../docs/semantics/languages.md#wr-47)):

- **The CLI's build.** `ParseCodebaseSettings` reads the entry
  (`format`, `external`, `inject`, `define`, `loader`, `banner`, `assets`;
  `relative_path`, `includes` and `excludes` do not shape the bundle).
  `CLIOptions` is WR-47's table: the script file as the single entry point,
  resolved from the sync root (the CLI's working directory, which `inject`
  paths and asset sources are relative to too), `bundle`, platform `node`,
  packages inlined, format `cjs` (absent `format`; engine `node20.15.1`) or
  `esm` (target `esnext`), output in memory under `/`, the entry's
  `external`, `inject`, `define` and `banner` unchanged, its `loader` map in
  place of the default `{".node": "file"}`, and no minification, source map,
  splitting or metafile. `CLIBundle` runs it as it is: the CLI parity check
  compares it with what the CLI uploads.
- **windmill-lite's transforms**, from the same single implementation as
  the Bun build ([ADR 0010](../../docs/design/0010-packaging.md)): the env
  rewrite (below) for the entry's format, with the define table's keys
  taking precedence over the entry's own and the support module injected
  before the entry's own `inject` files, so it runs before any code of the
  bundle; and imports that resolve to a listed host-provided module's file,
  or name a listed package, resolved to that module's shim. Both reach
  esbuild through a plugin: the support module and a listed package's
  shim load from its `windmill-lite` namespace, and a listed file's shim
  loads in place of the file when esbuild loads it (but the entry's own
  file: a listed codebase script that is a root is bundled from its own
  source, with its entry's settings), under its path from
  the sync root, so the output does not depend on where it was built.
  Every other import resolves as it would without the plugin, so a
  listed module costs no extra resolution (resolving each import a
  second time, outside esbuild's directory cache, made a build that
  imports a package of many modules 16 to 60 times slower, milestone
  F3h; `TestCodebaseHostModulesKeepBuildTime` and
  `BenchmarkCodebaseHostModules` guard it) and the bundle keeps every
  load effect it would have without the list
  (`TestHostModulesKeepLoadEffects`). esbuild calls the plugin
  concurrently, one call per import being resolved, so what it records
  is kept under a lock. The binding
  name is chosen as the Bun helper chooses it (`spells` applies the same
  rule). `//native` and `//nodejs` scripts are not
  rewritten. A `//native` codebase script runs in the native runtime
  (WR-50): it is bundled with host-provided modules inlined, and the Bun
  helper's `native` op writes the bundle's native form,
  `scripts/bun/<id>.native.js`, in place of its `.cjs` or `.mjs`.
- **Metadata.** The helper's `parse` op reads each source for its
  annotations, workflow-as-code markers and packages, and records a
  signature for each export of the bundle: the export names come from the
  metafile of an ES module build of the same entry, with the same plugin as
  the real build, so an import of a host-provided module resolves to its
  shim there too (`bundleExports`; a CommonJS bundle's `module.exports`
  takes the same names), and a function the entry re-exports with
  `export *` is signed. CommonJS source has no static export list, so its
  exports are left unsigned; a build that cannot read the names says so in
  an `esbuild_warning`. The harness calls a callable export of a codebase
  module that the metadata does not sign with an empty parameter list. A function the entry
  source declares gets its parameters, as Windmill parses a codebase
  script's content (WR-6, WR-7); one it does not declare gets an empty
  list, never a dependency's parameters. The metadata adds
  `bundler: "esbuild"`, the `format`, the host-provided modules used and
  esbuild's metafile inputs.
- **Output.** `scripts/bun/<id>.cjs` for CommonJS (or `.mjs`) and its
  `.meta.json`; every asset at `assets/<to>`. esbuild's warnings (such as an
  empty `import.meta` in CommonJS output) are build warnings,
  `esbuild_warning`.
- **Refusals**, each a problem of the script: settings of the wrong type
  or a `format` other than `cjs` and `esm` (`bundle_failed`); an esbuild
  error (`bundle_failed`); a bundle with several output files, such as a
  `file` loader's, whose upload layout is `later`
  ([WR-49](../../docs/semantics/languages.md#wr-49), `unsupported_feature`);
  an asset whose `to` is not a clean relative path or whose source does not
  read (`invalid_asset`); and two scripts that extract different content to
  one path (`asset_collision`,
  [ADR 0002](../../docs/design/0002-ir-and-loader.md), decision 3). Two
  scripts that extract the same content to one path share it.
- **Partial success.** Scripts build in id order, and one that fails
  does not stop the others: as `BuildBun` writes the metadata of every
  script that built, each script whose module and assets succeed gets its
  metadata and its assets written, so a caller that runs only the scripts
  that built (the step and flow checkers of `wltest check`) can run them.
  An entry's assets are taken all or none, and one that collides with an
  asset an earlier script took is the later script's problem. Paths that
  would not extract to one tree (a file that is also another's directory)
  are the bundle's problem, reported as the item `assets`
  (`asset_collision`); then no asset or metadata is written. `Build` still
  refuses the whole folder on any problem.

The harness runs a CommonJS bundle on Bun's CommonJS loader (D13), from a
copy in its working directory, where it has extracted the assets
([harnesses/bun](../../harnesses/bun/README.md)).

## The env rewrite

A harness gives each invocation its own `process.env`
([WA-31](../../docs/semantics/client-api.md#wa-31)), but `Bun.env` is a
separate object, and on Bun 1.3.10 both it and the global `Bun` are
read-only and non-configurable, so the runtime cannot replace them.
`EnvRewrite(format)` returns what every bundler applies instead
([ADR 0010](../../docs/design/0010-packaging.md) step 5):

| Define | Becomes | Formats |
|---|---|---|
| `Bun.env` (also as `Bun["env"]`) | `__windmill_lite_Bun.env` | ESM and CJS |
| `import.meta.env` | `__windmill_lite_Bun.env` | ESM only: in CJS output `import.meta` stays empty, as in the CLI's esbuild bundles |
| `Bun`, `globalThis.Bun`, `global.Bun`, `self.Bun` | `__windmill_lite_Bun` | ESM and CJS |

`__windmill_lite_Bun` (`EnvBinding`) is a global that the support module
defines, once per process, as a proxy of the real `Bun` whose `env` is
`process.env`. The proxy forwards every other read, key listing and
descriptor, and sends writes and deletes to the real `Bun`, so they fail as
they would there; freezing or sealing it, or defining a non-configurable
property on it, throws. Every bundle imports the support module before its
own code. It is a module of its own, so the bundler links its references to
`globalThis`, `Reflect`, `Proxy`, `Object` and `process` to the real
globals, renaming a script's own top-level bindings of those names; nothing
is inserted into a script's scope. A bundler replaces only free references
with a define, and the build uses `__windmill_lite_Bun` only when no input
of the bundle spells it, as written or through Unicode escapes (otherwise
the name with a suffix), so no binding of a script can capture the rewrite. So an alias (`const B = Bun`),
destructuring (`const { env } = Bun`) and a dependency's reads all see the
invocation's environment, and a local binding named `Bun` is left alone. What a define cannot see still reads the real environment:
imports of the `bun` module (`import { env } from "bun"`), optional chaining
through the global object (`globalThis?.Bun`), reflection, an alias of the
global object, and `eval` or `new Function`. The Bun build applies the
rewrite to Bun-target bundles only: a `//native` script runs at the native
boundary (C3n), and a `//nodejs` one, which Windmill runs on Node with Bun's
environment (WR-50), runs on the Bun harness, whose environment view covers
it at run time (D13).

The inputs the binding check reads are the bundle's files. esbuild's
virtual inputs, such as the `<define:NAME>` module it makes of an object or
array define value, are not files, and the check skips them.

## Shared code

`Config.SharedCode` (`windmill-lite build -shared-code`) builds the bundle
with shared code ([ADR 0013](../../docs/design/0013-shared-module-code.md)). Its differences from a per-script bundle are
deviation [D18](../../docs/design/0001-equivalence-contract.md#5-named-deviations). Every script keeps its per-script bundle, byte for byte as
without shared code: `fresh` runs it, and the CLI parity check compares it.

**Build groups.** The scripts that one bundler builds with one set of
effective settings, in one installation and one strictness, form a group:

- Codebase groups (`shareCode`, here) are keyed by the entry's settings
  text, its format, whether the env rewrite applies, and whether its
  CommonJS output starts with `"use strict"`.
- Bun-built groups are the build helper's, keyed by target and lock.
- Native scripts are in none, since each of their invocations runs in a
  realm of its own.

**The closure** of a group is a CommonJS bundle of an entry that maps each
dependency module that its scripts reach, by its path from the sync root,
to a function that requires it (`closureEntrySource`):

- It is built with the group's settings, the env rewrite and the shims.
- For an ES module group, dependency code reads `import.meta` from a
  parameter of the instance. That parameter is the first of `__wl_meta`,
  `__wl_meta_2`, … that no input of the closure spells, so no binding of a
  dependency captures it (`buildClosure`, as for the env rewrite's
  binding).
- The entry hands the instance's table back through a global property of
  its own (`ClosureTable`), not `module.exports`, which stays the step's.

**The closure file** (`wrapClosure`) is in Bun's own form of a module that a
bundler wrote, which Bun loads without transpiling it again:

- A CommonJS group's closure is a CommonJS file (`// @bun @bun-cjs`). Its
  factory runs the bundle with the calling module's `module`, `exports`,
  `require`, file names and `import.meta`, so dependency code sees the
  step's, as in its per-script bundle. A strict group's closure is strict.
- An ES module codebase group's closure is an ES module
  (`ClosureModuleFile`, `// @bun`), whose factory takes no `module` or
  `exports`. Dependency code that reads one finds no binding, as in its ES
  module per-script bundle.
- The Bun build helper's closure bundles go through the same wrapper
  (`BuildBun`), as CommonJS files.

**The shared-layout module** of a script is a second build of it with the
dependency plugin (`depPlugin`):

- Each of the entry's imports of a module of its own build resolves to a
  reference to that module in the script's instance, keeping the package's
  `sideEffects`. A module's key is its path from the folder's real path, as
  esbuild resolves files.
- A CommonJS module gets a CommonJS reference (`depReference`). An ES
  module gets an ES module reference (`esmReference`), whose named exports
  stay live and whose default is read once.
- An ES module reference lists the module's export names (`moduleExports`,
  from a bundle of the module alone). Its namespace object then has the
  module's names, and not the CommonJS interop marker of the reference that
  it reads them from.
- The env support module, host-provided modules' shims, external modules
  and the entry itself stay as the per-script bundle has them.
- Each inject file is replaced by a proxy that re-exports its names from
  the instance (`injectProxies`). The script's own code and its
  dependencies then reach one instance of it.
- The support module (`closureSupport`) makes the instance at the first
  reference, through the harness runtime's registry (`ClosureRegistry`).

**Fallbacks.** A script keeps its per-script bundle (`SharedMeta.Fallback`,
and the warning `shared_code_fallback`, which names the module) for:

- a `banner`, or an object or array `define` value, on its entry;
- a cycle through its own entry;
- `namespace_import` (`namespaceClash`): its entry and another module of
  its bundle both take the namespace object of one ES module dependency, by
  a namespace import, a namespace re-export or an `import()` of a literal
  specifier. The per-script bundle has one such object; the shared layout
  would have two.
- `export_names` (`exportNamesClash`): its entry takes the namespace
  object of an ES module whose names are not all known (a star re-export of
  an external module), or imports a module that exports `__esModule`;
- by module of the closure: top-level await, which fails the CommonJS
  build; a static import of an external module from an ES module group;
  and a kept `import()`.

A module the runtime has built in is neither an external import nor a kept
`import()`: the runtime resolves it to the same module from every file
([ADR 0013](../../docs/design/0013-shared-module-code.md), decision item
5). A specifier names one when it is one of `builtins`, spelled exactly; an
`import()` counts only with a literal specifier.

`builtins` holds the specifiers the build helper accepts on its Bun: each
name that `node:module`'s `builtinModules` lists, alone or after `node:`,
that Bun's resolver resolves to a built-in module. Bun 1.3.10 resolves its
own modules only bare, so `node:ws` and `node:bun:sqlite`, for example, are
external modules, while `node:fs/promises` is built in. The build requires
that Bun to be 1.3.10 unless `BunConfig.AllowBunMismatch` is set.

The build helper finds namespace imports and kept imports by parsing with
Babel: its `namespaces` op parses each module, and its `kept` op parses the
closure bundle and returns the built-in specifiers.

**Recorded:** the manifest's `shared_code` and `closures`, [D18](../../docs/design/0001-equivalence-contract.md#5-named-deviations) among its
deviations, and each script's `shared` record (its closure or its
fallback).

## Host-provided modules

`Shim(key, exports)` is the source every bundler resolves imports of one
host-provided module to (D12). It reads the process-wide registry,
`globalThis[Symbol.for("windmill-lite.hostModules")]`: a `Map` from the
module's key, as the host contract names it, to the object holding its
exports, normally its module namespace. It takes each export by name when
the step module loads, and fails that load with
`host-provided module not registered: <key>` when the registry or the
module is missing. The host SDK registers the host's instances; elsewhere a
harness registers the bundled copies from `scripts/bun/provided/`, in
`BunResult.HostModuleOrder`, which puts a module after the modules it
imports (W1, C3a). Export names are sorted; a name that is not an ASCII
identifier is written as a string export name.

`BuildEsbuildHostModules(ctx, EsbuildHostConfig)` builds the listed
modules that esbuild bundles, as it bundles the codebase scripts that
import them, and which only codebase scripts can import:

- each file outside the folder, in a codebase entry's `relative_path`;
- each codebase script of the folder, so that its shared copy has its
  entry's `define`, `loader`, `inject` and `banner`, and resolves packages
  from the folder's `node_modules`, as the script's own bundle does
  ([WR-47](../../docs/semantics/languages.md#wr-47)).

The copy takes no `assets` from its entry. A step's working directory
holds the assets of the scripts the bundle runs (D10). On Windmill, the
bundle of a script that imports the module carries the importing
script's entry's assets, not the module's entry's
([WR-49](../../docs/semantics/languages.md#wr-49)). So a module that
reads an asset of its own entry finds it only when that entry's scripts
are in the bundle, or when the importing script's entry lists the asset
too.

The build has these steps:

1. The helper's `parse` op reads each source for
   `host_module_live_binding` (`export let` or `export var`), with the
   syntax of the loader esbuild bundles it with. `loaderFor` picks it as
   esbuild 0.28.0 does: the longest suffix of the name that starts at a
   dot, over esbuild's own loaders with the entry's `loader` map on top.
   - TSX and JSX parse; another loader's file is not read.
   - A file with no loader (no suffix has one, or its loader is `default`)
     is not read either: esbuild's refusal, `No loader is configured`, is
     its problem.
2. A first esbuild build of each, with the shims of these modules as
   CommonJS placeholders that take any import name, records which listed
   modules it imports. Its metafile is the import graph that orders them
   ([Registration order](#registration-order-of-host-provided-modules));
   the helper's `lazy` op parses each module of it that holds an
   `import()`.
3. Each is bundled in that order, as an ES module with its codebase
   entry's settings, the env rewrite and the other modules' shims, into
   `scripts/bun/provided/<id>.mjs`. Its shim is generated from the export
   names of that bundle's metafile (`host_module_commonjs` when the entry
   is CommonJS source).
4. A module that imports a later module lazily is bundled again once every
   copy is: its first copy holds that module's placeholder. The second
   holds the module's late shim, from the export names of the module's
   copy, or its plain shim when it has no exports. The two copies must
   export the same names (`host_module_exports_differ`).

The manifest registers these modules after the Bun build's, which cannot
import them, and `Build` adds the files of the folder the copies read
(`EsbuildHostResult.Inputs`) to the sources.

## Registration order of host-provided modules

A listed module registers after each listed module that it imports while
it loads ([ADR 0010](../../docs/design/0010-packaging.md#host-provided-modules),
item 1). `loadorder.go` decides the order for both bundlers' builds, from
an `importGraph` per module: every import of a listed module in its
bundle, with the module that holds it, the bundler's kind and the
specifier.

`loadTimeHostModules` returns the listed modules a module imports at load
time (`HostModuleMeta.LoadHostModules`). A listed module is lazy only when
every import of it is lazy:

- **Lazy:** an `import()` of the listed module whose specifier its module
  imports only from inside functions, as the helper's parse reports
  ([build/](../../harnesses/bun/build/README.md)). Its module can be any
  module of the bundle.
- **Load time:** every other import, wherever its module sits: a static
  import or re-export, a `require()`, a top-level `import()`. A module
  that the bundle loads lazily still reads its static imports while it is
  initialised, once, so they order too.
- **Unknown:** an `import()` in a module that does not parse, and a listed
  module that the bundle imports with no import the records show, count as
  load-time. The parse gives a module with a top-level `import()` of a
  specifier that is not a literal no lazy specifiers, since a bundler can
  fold that specifier into any module's.

`hostModuleOrder` orders the modules by a depth-first walk, in the
configured order:

- each module comes after every listed module that it imports at load
  time; a cycle of these fails with `host_module_cycle`, naming the cycle by
  module ID;
- each module also comes after every listed module that it imports only
  lazily, unless that closes a cycle. The first lazy import on such a cycle
  is set aside, and the walk starts again;
- a list without lazy cycles therefore gets the order it had when every
  import ordered it, and builds the same bytes.

An import set aside is late: its importer registers first. The importer's
copy resolves it to the module's late shim (`LateShim`): an ES module that
re-exports each name from a CommonJS module whose getters read the
registry on every read. Late shims are inlined into the copies, and no file
of the bundle holds one alone.

- Before the module registers, an `import()` of it and a read of its
  values throw `host-provided module not registered: <key> (…)`, every
  time; after it, both work. A module whose loading awaits such an import
  at its top level fails to load, and both bundlers fail every later import
  of it with the same error.
- The late shim always exports `then`, from the registry like its other
  names. An `import()` reads `then` on the namespace it resolves to, so it
  rejects before the module registers, a module without exports included.
- Both bundlers initialise a module once, so a plain shim that failed can
  never yield the registered values. On Bun 1.3.10 and esbuild, every later
  import gets its exports undefined, or seems to succeed for a module
  without exports.

## BuildBun

`BuildBun(ctx, BunConfig)` builds every Bun script and host-provided module
of a bundle:

1. **Scan**, when there are host-provided modules: the helper reports each
   module's export names, the listed modules it imports and the packages it
   imports, or `host_module_live_binding` for `export let` or `export var`,
   or `host_module_commonjs` for a CommonJS module. For a module that
   imports another listed module, it also reports the import graph of the
   scan's bundle.
   `BuildBun` writes `scripts/bun/provided/shims/<id>.mjs` for each and
   orders them
   ([Registration order](#registration-order-of-host-provided-modules)).
2. **Build**: the helper bundles the host-provided copies and every script.
   A copy resolves its imports of a module it imports lazily and registers
   before to that module's late shim, which the request carries.
   `BuildBun` checks each copy's exports against its scan
   (`host_module_exports_differ`), records the workspace scripts each copy
   inlined, and writes `scripts/bun/<id>.meta.json`, a `ScriptMeta` with the
   metafile in canonical form.
3. **Warnings**: `host_module_shared_dependency` when a script imports a
   package directly that a host-provided module it uses also imports, so the
   package exists twice in the process (unless the package is itself a
   listed one, whose shim both resolve to); `native_import_unavailable` for
   each static import of a native-boundary script that the native runtime
   cannot load (anything but the embedded client, which only a nativets
   script reaches, [WR-40](../../docs/semantics/languages.md#wr-40)): the
   script builds, and the step fails when it runs, as on Windmill.

**Native scripts** (deviation D5). `BunScript.Language` is the language a
deploy records. A `nativets` script, and a Bun-family one whose header says
`//native`, are built in their native form
([build/](../../harnesses/bun/build/README.md)): `scripts/bun/<id>.native.js`
and no `.mjs`. Their `ScriptMeta` has `boundary` `native` and a `native`
record (`NativeMeta`: the kind, the function's parameter and preamble
offset, the static imports and the links they need, the `//useragent` and
`//proxy` header, and a nativets script's transpile error, which does not
fail the build). A nativets script imports nothing at build time, so it is
never `missing_lock`.

A script's lock is in the format of
[WR-25](../../docs/semantics/languages.md#wr-25); scripts with one lock
share one dependency set, keyed by its digest. The build runs on
`harness.PinnedBun` and refuses any other Bun (`HelperError`,
`runtime_mismatch`) unless `AllowBunMismatch` is set, in which case
`BunResult.NonconformantRuntime` says so. The helper runs without
`NODE_ENV`, which `Bun.build` inlines, so a bundle does not depend on the
builder's. The helper runs in a process group of its own; when a run
ends, by its timeout, cancellation or otherwise, `BuildBun` kills what is
left of the group (an install's lifecycle scripts) and waits until none of
it remains. An ID is one or more `/`-separated segments, none empty, `.`
or `..`, and none holding a backslash or NUL, the rule the harness runtime
applies to an inventory id; a script's first segment is not `provided`.
`BunConfig` refuses other IDs, IDs that differ only in case (they name
files) and a folder host module whose file is not the script at its path.
Problems with scripts or modules
come back together as a `*BuildError`, with the result so far; a script that
Windmill would fail to bundle at every run (`bundle_failed`) fails the build
(ADR 0001 §5a).

The bundle tree it writes:

| Path | Content |
|---|---|
| `scripts/bun/<id>.mjs` | the bundled module |
| `scripts/bun/<id>.native.js` | a native-boundary script's module, in its native form, instead |
| `scripts/bun/<id>.meta.json` | its `ScriptMeta`: import base, target, boundary, annotations, workflow-as-code markers, exports, the signature of each export and of each other top-level function declaration, the workspace scripts inlined, packages, host-provided modules used, the metafile, and for a native-boundary script its `native` record |
| `scripts/native/windmill.js`, `windmill.meta.json` | the embedded client of nativets scripts, in its native form, and its record |
| `scripts/bun/provided/<id>.mjs` | a host-provided module's bundled copy |
| `scripts/bun/provided/shims/<id>.mjs` | its shim |

The same inputs give the same files, from any folder, work or output
directory (`TestBuildIsReproducible`).

## ImportBase

`ImportBase(flowPath, moduleID, explicitPath)` is an inline script's import
base ([WR-29](../../docs/semantics/languages.md#wr-29)): `<flowPath>/<moduleID>`
at any nesting, or the explicit `path` where a deployed flow keeps it (the
failure and preprocessor slots; [internal/ir](../ir/README.md) decides, and
`Build` takes `Script.ImportBase` from it), with a nested-body segment
(`forloop-0` and the like) collapsed to `<prefix>/flow` by
`ir.CollapseBodies`. A path script's import base is its own path.

## Tests

The build tests run the real helper on the pinned Bun. Install the helper's
parser once, then point `WL_BUN` at Bun 1.3.10; without it they skip, unless
`WL_REQUIRE_BUN` is set, as CI sets it:

```sh
(cd harnesses/bun/build && bun install --frozen-lockfile)
WL_BUN=/path/to/bun-1.3.10/bun go test -race ./internal/bundle
```

- `TestConformanceBuild` runs every case of
  [conformance/build](../../conformance/README.md): it writes the case's
  folder, builds it and checks what the build recorded or refused.
  `TestConformanceBuildCatalogue` fails unless every catalogue case of a
  finding with a case file is a case or listed in `deferred.json`.
  - Cases with a `run` import the built modules on the pinned Bun, with
    `process.env` replaced as a harness's view does and host-provided
    copies registered, and check what their exports return.
  - Synthetic npm packages come from a registry the test serves, with an
    install cache of its own. A package's `package.json` dependencies are
    installed with it.
  - The [WR-27](../../docs/semantics/languages.md#wr-27) cases check that a
    module that a `sideEffects: false` package re-exports stays in the
    bundle behind an ordinary package import, and behind a bare import that
    ends in `.ts` (deviation D19).
  - Whole-folder cases (`given.folder`) run `Build` instead, and list every
    problem it must report: workflow-as-code (WA-33/c, WR-32), the missing
    lock (WR-26), the codebase refusals (WR-47, WR-49). A clean one can
    check what it recorded for each script, its assets and warnings, and
    run its modules.
  - The whole-folder cases cover the codebase settings of WR-47 (the CLI's
    settings), WR-49 (assets) and WA-31 (the env rewrite in CommonJS and
    ESM output).
  - The whole-folder D12 cases cover shims and a listed codebase script
    that is itself a root. They cover the shared copy of a listed codebase
    script with its entry's settings, a Bun script's refused import of one,
    and the shared-dependency warning for codebase scripts. They also
    cover modules outside the folder, whose files a case gives in
    `given.outside`: TSX and JSX modules, and one with no loader.
  - The D12 order cases cover cycles that close through a lazy import and
    through a load-time one: direct, through a module that is not listed,
    between listed packages, and through a folded specifier, for both
    bundlers. They also cover a lazy import that runs before its module
    registers, caught and uncaught (`run.register_error`), a module
    whose loading awaits one, and an `import()` of a module without exports
    attempted twice before its registration.
- The shared-code cases of `TestConformanceBuild` (`given.shared_code`)
  check `expect.shared`: each script's record by a case-local closure
  label, what each closure names and contains, and text of each script's
  shared-layout module. For `t-closure-membership` they also check that
  the per-script bundles are the same bytes as without shared code.
- `TestBuiltins` checks the build helper's `kept` op: an `import()` of
  `node:async_hooks` is not kept, and one of a variable or of `node:ws` is.
  It also checks that the built-in specifiers it returns hold Node's
  modules and their subpaths, alone and after `node:`, and Bun's own
  alone, and that neither `node:` before a name of Bun's own, such as
  `node:bun:sqlite`, nor a `node:` name outside the list is built in.
- `TestBuildFixture` builds `testdata/fixture` (a synthetic folder with a
  flow, an inline step, a path script, a workspace import, a host-provided
  module, a codebase script with a define, an import of its codebase and an
  asset, a resource, a resource type, a variable and a secret) and checks
  the bundle's files, every manifest field, `data/`, `ir.json`, and that its
  modules run on Bun sharing the host-provided module.
  `TestBuildTwiceGivesOneDigest` is F4's exit gate: two builds of copies of
  the fixture in other directories, with other work and output
  directories, give one digest, one manifest and one archive, byte for
  byte. `TestDigestFollowsInputs` changes each input in turn (a flow's
  `stop_after_if`, scripts, the host-provided module and its metadata, a
  lock, data, the contract, the mode, the engine version, the harness
  runtime, a codebase script, a module it imports, its entry's define, its
  asset) and checks that the digest changes, and that a secret's file and
  a file the build does not read leave it alone. `TestBuildOutput`,
  `TestBuildRefusesUnbuilt` and `TestBuildCompileErrors` cover the output
  directory and the refusals.
- `TestLoaderFor` checks the loader picks: compound suffixes over shorter
  ones from either map, `default`, a name without a dot, the CLI's `.node`
  default; the D12 `codebase-outside-compound-*` cases check them against
  esbuild's builds.
- `TestBuildOutsideHostModuleSources` checks that a file of the folder
  that only an outside module's copy reads is a source, and that a
  comment in it, which leaves the copy alone, changes the digest.
- `TestBuildOutsideHostModule` (milestone F3h) builds a folder whose
  codebase script imports, from outside the folder, a module holding an
  `AsyncLocalStorage` store, listed by its path from the sync root: it
  checks the manifest, the copy, the shim and the script's metadata, then
  runs the script with the host's own instance registered, inside and
  outside the host's scope, and with the bundled copy. `TestOutsideID`
  checks the ids. `TestCodebaseHostModulesKeepBuildTime` bundles six
  codebase scripts that import a package of 400 modules, without and with
  a listed module, and fails when listing one makes the build more than
  three times slower (61 times before F3h); `BenchmarkCodebaseHostModules`
  times both. `TestHostModulesKeepLoadEffects` builds one folder without
  and with host-provided modules, runs each step in a process of its own
  with every copy registered, and checks that the steps see the same load
  effects (modules of packages and directories marked
  `sideEffects: false`, outside modules listed or not, a listed module's
  Bun copy, a Bun script with a lock), but for D12's one difference: a
  listed module that a step imports without using.
- `TestVerify`, `TestVerifyArchiveEntries`, `TestWriteArchive`,
  `TestExtract` (an archive extracted into an empty directory verifies with
  the archive's digest; a non-empty directory is refused),
  `TestParseManifest`, `TestTreeDigest`, `TestCanonicalize` and
  `TestManifestDeviations` (with `ModeDeviations` both ways) need
  no Bun: they cover a changed, extra or missing file, a symbolic link, a
  forgery whose own manifest matches, archive entries that repeat or escape,
  directory entries that collide with a file or lie below one, in the same
  or another case (before or after it, against a release manifest and a
  digest), directory entries the bundle does not have, ordinary directory
  headers, links below a harness runtime's directory, and the canonical
  form. `TestFoldKey` checks the case-folding key against
  `strings.EqualFold`.
- `TestEnvRewriteGolden`, `TestShimGolden` and `TestLateShimGolden` pin
  the define tables, the support module, generated shims and late shims in
  `testdata/`; `go test ./internal/bundle -run Golden -args -update`
  rewrites them. `TestShimBehaviour` runs shims on Bun.
- `TestLoadTimeHostModules` and `TestHostModuleOrder` need no Bun: they
  check which imports of listed modules are lazy, and the order, with its
  late imports and cycle errors. `TestHostModuleOrderWithoutLazyCycles`
  orders 6,000 random lists of up to 8 modules: a list without lazy cycles
  gets the order that counting every import gives. `TestLazyImports` runs
  the helper's `lazy` request on sources whose `import()` calls sit in
  functions, at top level and in class bodies, with literal and folded
  specifiers.
- `TestCLIParity` is the CLI parity check of
  [ADR 0002](../../docs/design/0002-ir-and-loader.md), decision 4
  ([Q-WR-24](../../docs/semantics/languages.md#q-wr-24)). With
  `WL_WMILL_CLI` naming the command that runs the unmodified
  `windmill-cli@1.767.0` (CI's `parity.yml`: `npx --yes
  windmill-cli@1.767.0`; never run locally, never a dependency), it pushes
  each synthetic WR-47 fixture with `wmill script push` to a recording HTTP
  stub and checks that the bundle the CLI uploads (`main.js`, or a tar of
  it and the assets) equals `CLIBundle`'s, byte for byte. Without it, it
  skips. `TestCLIOptionsFollowWR47` is its documented fallback, run
  everywhere: the options `CLIOptions` gives, option by option, are WR-47's
  table, and `go.mod` pins esbuild at the CLI's version.
  `TestSpells` checks the binding-name collision rule, and
  `TestBuildCodebaseConcurrentHostImports` builds 64 modules that each
  import a listed folder module and a listed package, which esbuild
  resolves concurrently (run under `-race` in CI), and runs the bundle.
  `TestBuildCodebasePartialSuccess` builds a script that fails, one that
  builds and reads its asset, and one whose asset collides, and checks that
  only the one that built has its metadata and asset, and runs; and that
  assets that cannot form one tree write nothing.
- `TestBuildIsReproducible`, `TestMetaFile`, `TestBuildLocks`,
  `TestBuildRefusesAnotherBun`, `TestBuildIgnoresNodeEnv`,
  `TestBuildRefusesAncestorPackages`, `TestHelperRefusesPathsOutsideTheFolder`,
  `TestBuildCancelsInstallTree`, `TestBunConfigValidation` and
  `TestImportBase` cover the rest.
- `TestAnnotationCases` runs `conformance/build/annotations.json` through
  both readers of leading-comment annotations: `syncfolder.HasAnnotation`
  and the helper.

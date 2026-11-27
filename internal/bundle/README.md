# internal/bundle

**Current state:** the package builds, archives, verifies and extracts
windmill-lite bundles. A bundle holds the compiled program, every Bun script
built by the Bun [build helper](../../docs/glossary.md#build-helper) or by
esbuild, the [host-provided modules](../../docs/glossary.md#host-provided-module),
shared code and the folder's data, with a manifest of digests. The build
refuses what it cannot build as Windmill would run it: workflow-as-code, a
host-provided package, a non-codebase script whose build reports a
package import and that has no lock, and the other problems of
[Build](#build).

This README covers `internal/bundle` and its `testdata/`: `fixture/`, a
synthetic sync folder with a host contract, and `env/` and `shim/`, the
golden files of the env rewrite and the shims.

The package turns a [sync folder](../../docs/glossary.md#sync-folder) and a
[host contract](../../docs/glossary.md#host-contract) into a
[bundle](../../docs/glossary.md#bundle), as the
[packaging](../../docs/design/0010-packaging.md) design record defines it,
and checks a bundle against a manifest.

- **Callers:** `windmill-lite build` calls `Build`, and
  `windmill-lite verify` calls `Verify`
  ([cmd/windmill-lite](../../cmd/windmill-lite/README.md)).
  [wl](../../wl/README.md)'s `Open` opens a bundle to run it: it calls
  `Verify`, or `Extract` for an archive, and `TreeDigest` for the harness
  runtime. `wltest check` ([cmd/wltest](../../cmd/wltest/README.md)) calls
  `Build`, `BuildBun`, `BuildCodebase` and `ScriptItem`.
- **What it calls:** [internal/syncfolder](../syncfolder/README.md) (the
  folder), [internal/ir](../ir/README.md) (the compiler), the
  [Bun build helper](../../harnesses/bun/build/README.md) (`BuildBun`), and
  esbuild through its Go API (`BuildCodebase`).
- **Shared code** is a build option (`Config.SharedCode`), which the
  [shared module code](../../docs/design/0013-shared-module-code.md) design
  record defines ([Shared code](#shared-code)).

## Files

| File | What it holds |
|---|---|
| `build.go` | `Build` and `Config`: the whole build, its refusals, `data/` |
| `manifest.go` | `Manifest`, `Canonicalize`, `Digest`, `ParseManifest`, the deviations that a bundle lists, `ModeDeviations` |
| `sources.go` | The recording file system behind the source inventory; `TreeDigest` |
| `archive.go` | `WriteArchive`, `WriteArchiveFile`, `Verify` and `Extract` |
| `argsfile.go` | The search behind the manifest's `no_args_file_reader` |
| `env.go` | `EnvRewrite`: the define table and the support module of the env rewrite |
| `shim.go` | `Shim`: the shim of a host-provided module; `LateShim`, its late shim; the registry key |
| `importbase.go` | `ImportBase`: an inline script's import base |
| `bun.go` | `BuildBun`: the two helper runs, the shims, the metadata, the registration order and the warnings |
| `loadorder.go` | The registration order of host-provided modules: which listed modules each imports at load time (`loadTimeHostModules`), and the order (`hostModuleOrder`) |
| `esbuild.go` | `CodebaseSettings` (a `codebases` entry), `CLIOptions` and `CLIBundle` (the pinned CLI's build), `BuildCodebase`, assets |
| `outside.go` | `BuildEsbuildHostModules`: the host-provided modules that esbuild builds; `OutsideID` |
| `meta.go` | `ScriptMeta` and the other result types, problems and warnings; `SharedMeta` and the fallback reasons of shared code |
| `sharedcode.go` | Shared code: the codebase groups, their closures and shared-layout modules, the dependency plugin and inject proxies, the fallbacks, the closure wrapper, `ClosureRegistry` |
| `proc_unix.go`, `proc_other.go` | The build helper's process group, and killing what is left of it |
| `doc.go` | The package comment |

## Public surface

### Build and verify

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Build`, `Config`, `Result` | func, structs | The whole build ([Build](#build)) | `cmd/windmill-lite`, `wltest`, tests |
| `Verify`, `Extract`, `VerifyOptions`, `Verification` | funcs, structs | Check a bundle against a manifest; extract a `.wlb` ([Verify](#verify)) | `wl`, `cmd/windmill-lite` |
| `WriteArchive`, `WriteArchiveFile`, `ArchiveExt` | funcs, const | Write a bundle directory as a `.wlb` | `cmd/windmill-lite` |
| `TreeDigest` | func | The digest of a directory's regular files | `wl`, `Build` |
| `BuildError`, `ItemProblem`, `Problem` | structs | The build's problems, by item | callers of `Build`, `BuildBun`, `BuildCodebase` and `BuildEsbuildHostModules`; `wltest` |
| `MismatchError` | struct | The differences that `Verify` found | callers of `Verify` |
| `HelperError` | struct | A request that the Bun build helper refused, such as `runtime_mismatch` on another Bun. A crash, a timeout or a bad response is a plain error | callers of the bundlers |
| `ScriptItem` | func | How a problem names a script | `wltest` |

### The manifest

`Build` writes the manifest and `Verify` reads it with `ParseManifest`.
Outside the package, `wl` reads a verified bundle's `Manifest` and applies
`ModeDeviations` for the runtime's mode, which can differ from the
bundle's. `wl`'s tests build manifests of their own, and those of
`cmd/windmill-lite` read a built bundle's manifest.

| Name | Kind | What it does |
|---|---|---|
| `Manifest`, `Engine`, `Bundlers`, `HostModuleEntry`, `PluginEntry` | structs | `manifest.json` and its parts ([The manifest and the bundle digest](#the-manifest-and-the-bundle-digest)) |
| `ParseManifest`, `Manifest.Canonical` | func, method | Read a manifest, refusing any that is not canonical; write one |
| `Canonicalize`, `Digest` | funcs | The canonical form of JSON text; `sha256:` and 64 hex digits |
| `ModeDeviations`, `SharedCodeDeviation` | func, const | The deviations of another module-state mode; `"D18"` |
| `FormatVersion`, `ManifestFile` | const | `1`; `manifest.json` |
| `ArgsFileName` | const | `args.json`, which `no_args_file_reader` is about |

### The bundlers

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `BuildBun`, `BunConfig`, `BunResult`, `BunScript`, `HostModule`, `BunPlugin`, `PluginMeta` | func, structs | The Bun build helper's build of Bun scripts, host-provided modules and the host plugin ([BuildBun](#buildbun)) | `Build`, `wltest`, tests |
| `BuildCodebase`, `CodebaseConfig`, `CodebaseResult`, `CodebaseScript`, `CodebaseHostModule` | func, structs | esbuild's build of codebase scripts ([Codebase scripts](#codebase-scripts)) | `Build`, `wltest`, tests |
| `CodebaseSettings`, `ParseCodebaseSettings`, `CodebaseSettings.CLIOptions`, `CodebaseSettings.CLIBundle` | struct, func, methods | A `codebases` entry, and the CLI's own build of it | `Build`, the CLI parity check |
| `BuildEsbuildHostModules`, `EsbuildHostConfig`, `EsbuildHostModule`, `EsbuildHostResult`, `OutsideID` | func, structs, func | The host-provided modules that esbuild builds ([Modules that esbuild builds](#modules-that-esbuild-builds)) | `Build` |
| `EsbuildVersion` | const | `0.28.2`, the CLI's esbuild | `Build` (the manifest's `bundlers.esbuild`); `TestCLIOptionsFollowWR47` checks that `go.mod` pins it |
| `Format`, `ESM`, `CJS` | string type, const | A codebase entry's output format | `ParseCodebaseSettings`, the bundlers |
| `EnvRewrite`, `EnvBinding` | func, const | The env rewrite ([The env rewrite](#the-env-rewrite)); `__windmill_lite_Bun` | the bundlers |
| `Shim`, `LateShim`, `LateShimMessage`, `HostModuleRegistry` | funcs, const | A host-provided module's shim and late shim; the registry's key, `windmill-lite.hostModules` | the bundlers |
| `ClosureFile`, `ClosureModuleFile`, `ClosureRegistry`, `ClosureTable` | funcs, const | Shared code's file names and registry keys ([Shared code](#shared-code)) | the bundlers, `ParseManifest` |
| `ImportBase` | func | An inline script's import base ([ImportBase](#importbase)) | tests; `internal/ir` computes the same base itself (`Script.ImportBase`) |

### Script metadata, problems and warnings

The bundlers return each script's metadata as a `*ScriptMeta`, which
`Build` checks and uses to fill the manifest; they also write it to the
script's `.meta.json` in the bundle. `TestConformanceBuild` compares a
build's assets, problems and warnings with each build case. Outside the
package, `wltest check` reports a `BuildError`'s problems against its step
cases and reads a codebase entry's `Asset` list; the tests of `wltest` and
`cmd/windmill-lite` read `ScriptMeta` and `WarningSharedCodeFallback`.

| Name | Kind | What it holds |
|---|---|---|
| `ScriptMeta` | struct | A script's `.meta.json` ([BuildBun](#buildbun)) |
| `Annotations`, `WorkflowAsCode`, `Signature`, `Param` | structs | Its leading-comment annotations, workflow-as-code markers, and each export's signature |
| `NativeMeta`, `NativeLink` | structs | A native-boundary script's `native` record |
| `SharedMeta` | struct | A script's `shared` record: its closure or its fallback |
| `HostModuleMeta` | struct | A host-provided module's exports, the listed modules that it imports (and which at load time), its packages and its imports |
| `Asset` | struct | A codebase entry's asset |
| `Warning` | struct | A build warning |
| `ProblemMissingLock`, `ProblemUnsupportedFeature`, `ProblemBundleFailed`, `ProblemParseFailed`, `ProblemInvalidLock`, `ProblemInstallFailed`, `ProblemUnsupported`, `ProblemLiveBinding`, `ProblemExportsDiffer`, `ProblemAssetCollision`, `ProblemInvalidAsset`, `ProblemUnknownHostModule`, `ProblemHostModuleCycle`, `ProblemHostModuleCommonJS` | const | The problem codes ([Build](#build)) |
| `WarningEsbuild`, `WarningNativeImport`, `WarningSharedCodeFallback`, `WarningSharedDependency` | const | The warning codes |
| `FallbackBanner`, `FallbackDefineObject`, `FallbackTopLevelAwait`, `FallbackExternalImport`, `FallbackDynamicImport`, `FallbackEntryCycle`, `FallbackNamespaceImport`, `FallbackExportNames` | const | Why a script keeps its per-script bundle ([Fallbacks](#fallbacks)) |

## The bundle

A bundle is a directory, or the same tree as one `.wlb` archive:

| Path | Content |
|---|---|
| `manifest.json` | The manifest (below), in canonical form |
| `ir.json` | The compiled program, `(*ir.Program).MarshalIndent` ([internal/ir](../ir/README.md)) |
| `scripts/bun/<id>.mjs`, `<id>.meta.json` | Each Bun script's module and metadata (`BuildBun`), named by its inventory id, as the harness runtime resolves an id: `scripts/bun/f/app/report.mjs` for the path script `f/app/report`, `scripts/bun/inline-<16 hex>.mjs` for an inline step |
| `scripts/bun/<id>.native.js` | A native-boundary script's module, in place of its `.mjs` |
| `scripts/bun/<id>.cjs`, `<id>.meta.json` | A codebase script built as CommonJS (`BuildCodebase`); one built as an ES module is `<id>.mjs` |
| `assets/<to>` | Every codebase entry's assets, at the path in the working directory that they are extracted to |
| `scripts/native/windmill.js`, `windmill.meta.json` | The native runtime's embedded client, when a script is `nativets` |
| `scripts/bun/closures/<id>.cjs`, `<id>.mjs` | With shared code, each build group's closure; an ES module for an ES module codebase group |
| `scripts/bun/<id>.shared.mjs`, `<id>.shared.cjs` | With shared code, each script's shared-layout module, beside its per-script bundle |
| `scripts/bun/provided/<id>.mjs`, `provided/shims/<id>.mjs` | Each host-provided module's bundled copy and shim, named by its script path, or `outside/<n>/<path>` for a module outside the folder |
| `plugins/<id>.mjs` | The host plugin that the host contract's `plugin` field names, named by its script path, with every host-provided module resolved to its shim |
| `data/resources.json`, `data/resource_types.json`, `data/variables.json` | Every resource and resource type that the folder keeps, and every variable but secrets. Each file is an object keyed by path (a resource type by name), with the record of [internal/syncfolder](../syncfolder/README.md) |

- **At most one host plugin.** A bundle holds the host plugin that its
  host contract names, if any. Without one, a host gives its plugin when
  it runs the bundle.
- **No secret.** A secret variable is never written. Its value in a folder
  is ciphertext (or plain text, with `plainSecrets`), and the host supplies
  secrets.

## The manifest and the bundle digest

`manifest.json` holds these fields:

| Field | Holds |
|---|---|
| `format` | `FormatVersion`, 1 |
| `engine` | `version`, the exact engine version that built the bundle, and `expression_stack_bytes`, the expression engine's configured stack limit, 1 MiB, which is observable. The engine scales it to the C stack of a call in its build ([where semantics live](../../docs/design/0003-semantics-placement.md), [JE-48](../../docs/semantics/expressions.md#je-48)) |
| `bundlers` | The Bun version that built the scripts; esbuild's (`EsbuildVersion`, 0.28.2) when the bundle has codebase scripts |
| `nonconformant_runtime` | Set when the Bun was not the pinned one (`-bun-mismatch`) |
| `no_args_file_reader` | Set when no script of the bundle can read `args.json`, the job's arguments ([WR-5](../../docs/semantics/languages.md#wr-5)). Host mode leaves the file out only for such a bundle, when its host opts in ([harness protocol](../../docs/design/0004-harness-protocol.md#host-mode)) |
| `module_state` | `warm` or `fresh` ([module state](../../docs/design/0006-module-state.md)) |
| `shared_code`, `closures` | Set for a bundle built with shared code, and the ids of its closures |
| `deviations` | The deviations in effect (below) |
| `contract` | The host contract compiled against; with none, every flow and script of the folder is a root |
| `host_modules` | The host-provided modules, `key` and `id`, in registration order: a module after the modules that it imports |
| `plugin` | The host plugin, when the contract names one: `key`, its path in the contract, and `file`, its bundle's path, whose digest `files` holds. `ParseManifest` refuses an entry whose `file` is not a module of `plugins/` that `files` lists |
| `harness_runtimes` | By harness class, the `TreeDigest` of the harness runtime that the bundle was built for |
| `sources` | The digest of every file of the sync folder that the build read, by its path from the sync root ([Sources](#sources)) |
| `files` | The digest of every other file of the bundle |

**`no_args_file_reader`'s search** (`argsfile.go`) reads each script's
module after the build's own leading sections. A script is no reader only
when its module holds no way to a file, a process or code made at run
time, and calls no host-provided module, shared closure, codebase or
native code.

### The deviations a bundle lists

`deviations` lists the
[named deviations](../../docs/design/0001-equivalence-contract.md#5-named-deviations)
of the equivalence contract that the bundle has, by the contract's ids.
The run trace lists the others. Some depend on a run-time setting: a cap
on invocations, the host's read cache, and the watchdog's part of an
abandoned step. One depends on what a run meets: a flow left waiting after
an unreadable child result.

| Values | What differs from Windmill | When the bundle lists it |
|---|---|---|
| `"D5"`, `"D6"`, `"D7"`, `"D12"`, `"D13"`, `"D19"` | The program's own deviations, which the compiler decides ([The deviations a program lists](../ir/README.md#the-deviations-a-program-lists)) | When the program lists them |
| `"D1"`, `"D2"`, `"D9"`, `"D10"`, `"D11"` | Module state survives; an abandoned step is not killed; the settle-time fence; one working directory per process; an intercepted `process.exit` | In `warm` mode |
| `"D18"` | Shared code's differences from the per-script bundle | With shared code, in `warm` mode |
| `"D3"`, `"D8"`, `"D14"`, `"D16"`, `"D17"`, `"D22"`, `"D23"` | windmill-lite job ids; an interrupted expression attempt; no Rust location suffix; `Math`'s last digit; deeper recursion in an expression; no job read of an ended run; Bun 1.4.3 | Always |

`ModeDeviations` swaps the deviations of one mode for another's and keeps
the rest. `wl` reports its result when a runtime overrides the bundle's
mode.

### The canonical form and the digest

- **A digest** is `sha256:` and 64 hexadecimal digits.
- **The canonical form** (`Canonicalize`): object keys sorted by their
  UTF-8 bytes, numbers as written, strings escaped only where JSON
  requires it (and U+2028, U+2029), two-space indentation and a final
  newline. The manifest is written in it.
- **The bundle digest** is the digest of those bytes. The harness runtime
  computes it from `manifest.json` for its handshake.
- **`ParseManifest` refuses** a manifest that:
  - is not in canonical form, has an unknown field, or another format;
  - has a file path that is not a clean relative path;
  - has paths that would not extract to one tree where names compare
    without case: two that differ only in case, or a file that is a
    directory of another;
  - has a host-provided module whose bundled copy it does not list;
  - lists `manifest.json` among its `files`;
  - has `closures` without `shared_code`, an invalid closure id, or a
    closure without exactly one `.cjs` or `.mjs` file.

### Sources

The build reads the folder through a file system that records every file
read. These are the bundle's inputs:

- `wmill.yaml`, the flows and scripts that the roots reach with their
  metadata, locks and `!inline` files, and the data files;
- the workspace scripts that bundles inline, and each host-provided
  module's file, metadata and lock, which the Bun helper reads itself;
- what esbuild read for each codebase script inside the folder (its
  metafile's inputs, outside `node_modules`), with the sources of its
  assets.

These are not inputs:

- a package or a file outside the folder that a codebase bundle inlines:
  it reaches the digest through the bundled module itself;
- a file that the build does not read, such as a script that nothing
  reaches;
- a secret variable's file. With `plainSecrets` it holds the value in
  plain text, and its digest in a shipped manifest would let anyone test
  guesses of the value.

### What changes the digest

- **Inputs:** any source, a lock, the contract, the module-state mode, the
  engine version, the Bun version, the harness runtime, and any built
  file.
- **`TreeDigest(dir)`** is the digest of the canonical JSON object that
  maps the slash path of every regular file under `dir` to its digest. It
  skips `node_modules` and names that start with `.`.
- **Links.** `TreeDigest` refuses a symbolic link below `dir`, or any other
  file that is not regular. Bun loads through a link, so a digest that
  skipped one would not change with what runs. `dir` itself may be a link.

## Build

`Build(ctx, Config)` runs these steps:

1. **Compile.** It finds the sync root from `Config.Folder`, reads the
   folder, parses the contract, and compiles the program (`ir.Compile`,
   with `Config.Strict`, which `windmill-lite build -strict` sets). The
   compiler's errors come back as they are.
2. **Data.** It reads the folder's resources, resource types and
   variables, and sets secrets aside, for `data/`.
3. **Host-provided modules.** It resolves the contract's `host_modules`.
   Each must be one of these:
   - the file of a Bun script of the folder (`f/shared/ctx.ts`), whose path
     becomes its id;
   - a file outside the folder, in a codebase entry's `relative_path`,
     named by its path from the sync root (`../lib/ctx.ts`), whose id is
     `OutsideID`'s (`outside/1/lib/ctx.ts`).
4. **Three builds,** in this order, each with the shims that the earlier
   ones wrote:
   1. `BuildBun`, for every script of the inventory that is not a codebase
      script, and for the listed modules that are neither codebase scripts
      nor outside the folder. Every `.ts` script of the folder is
      available to workspace imports.
      - **The host plugin** (`BunConfig.Plugin`) is built here too. The
        contract's `plugin` must be the file of a `bun` script of the
        folder, not a codebase, Deno, `nativets` or `//native` script, and
        like a Bun step it may not import a listed codebase script
        (`unsupported_feature` otherwise). Its source and the scripts that
        its bundle inlined join the sources.
   2. `BuildEsbuildHostModules`, for the other listed modules.
   3. `BuildCodebase`, for the codebase scripts, each with the settings of
      the `codebases` entry that selects it
      ([SY-8](../../docs/semantics/openflow.md#sy-8)).
5. **Warnings.** It warns `host_module_shared_dependency` for each
   codebase script that imports a package directly that a listed module
   it uses also imports, as `BuildBun` warns for Bun scripts.
6. **Metadata checks.** It checks each script's metadata (the refusals
   below).
7. **Writing.** It writes `ir.json` and `data/`, then the manifest, last.

The build refuses these, each a problem of its script or module:

| Problem | When |
|---|---|
| The script's load error | A listed host-provided module that is a script that fails to read |
| `host_module_unknown` | A listed entry that leaves the root but names no file in a codebase entry's `relative_path` |
| `unsupported_feature` | A host-provided module that is neither a script of the folder nor outside it: a host-provided package would need a lock, and the contract has no field for one. Checked before bundling |
| `unsupported_feature` | A Bun script, or a module of the Bun build, that imports a listed codebase script: the Bun build has no shim for it, so it would inline a second instance |
| `unsupported_feature`, `unsupported: workflow_as_code` | Workflow-as-code, by any detection path ([WA-33](../../docs/semantics/client-api.md#wa-33), [WR-32](../../docs/semantics/languages.md#wr-32)), which the [equivalence contract](../../docs/design/0001-equivalence-contract.md#3-out-of-scope) puts out of scope |
| `missing_lock` | A script that is not a codebase script, has no lock (nil or blank), and whose build reports a package import (`ScriptMeta.Packages`, the packages that its own source imports; a `nativets` script reports none). A lock is the only thing that the build installs from ([IR and loader](../../docs/design/0002-ir-and-loader.md)). An empty lock with no such imports builds |
| The bundlers' problems | [BuildBun](#buildbun), [Output and refusals](#output-and-refusals), [Modules that esbuild builds](#modules-that-esbuild-builds) |

- **A codebase script has no lock:** esbuild inlines its packages from the
  folder's `node_modules`, as the CLI does
  ([WR-47](../../docs/semantics/languages.md#wr-47)).
- **Problems come back together** as a `*BuildError`, sorted by item. A
  path script is named `script <path>`, and an inline step
  `<flow> step '<id>' (script <inventory id>)` (`ScriptItem`), so a caller
  can tell which of its roots reach the script.

### The output directory and the archive

- **`Config.Out`** must not exist, or must be empty. The build writes a
  sibling directory, and renames it to `Out` last, so a failed build leaves
  nothing there.
- **`WriteArchive`** writes a bundle directory as a `.wlb`: a tar file
  whose first entry is `manifest.json`, then every file that the manifest
  lists, in byte order of its path.
- **Fixed headers.** Each entry has mode `0644`, owner 0, modification time
  0 and the PAX format, so one bundle gives one archive.
- **A file that no longer matches the manifest** fails the write.

## Verify

`Verify(artifact, VerifyOptions)` reads a bundle directory or a `.wlb`,
recomputes the digest of every file, and checks them against a manifest.

- **Which manifest.** `VerifyOptions.Release`, a release manifest, when
  given; the bundle's own manifest must then be the same bytes. Otherwise
  the bundle's own.
- **The digest.** `VerifyOptions.Digest` pins the bundle digest.
- **The files.** The artifact must hold exactly the listed files, as
  regular files.
- **An archive's entries** must be regular files, each named once, or
  directories, all with clean relative names (a leading `./` and a
  directory's trailing `/` aside).
- **One tree.** They must form one tree as extraction makes it where names
  compare without case, as on macOS by default. No two names may differ
  only in case, no name may be both a file and a directory, and nothing
  may lie below a file, whatever the order of the entries.
- **Directory entries** must be directories of the bundle, spelled as the
  manifest spells them. Otherwise extracting a verified archive could
  replace a file that the manifest lists with a directory.
- **Differences** come back together as a `*MismatchError`.
- **Intact is not released.** A bundle checked only against its own
  manifest proves that it is intact, not that it is the one released. A
  changed file with a manifest rewritten to match passes alone, and fails
  against the release manifest or the digest.

`Extract(archive, dir, VerifyOptions)` writes a `.wlb` into `dir`, which
must not exist or must be empty:

1. It verifies the archive first, so its entries are clean relative names
   that make one tree.
2. It writes every regular entry with mode `0644`, without following or
   replacing anything.
3. It verifies the written directory again, against the archive's digest.

## Codebase scripts

A codebase script is a Bun script that a `codebases` entry of
`wmill.yaml` selects ([SY-8](../../docs/semantics/openflow.md#sy-8)).
Windmill's CLI bundles it with esbuild at deploy time
([WR-30](../../docs/semantics/languages.md#wr-30)). `BuildCodebase(ctx,
CodebaseConfig)` bundles each with esbuild through its Go API, pinned in
`go.mod` at the CLI's version, 0.28.2 (`EsbuildVersion`,
[WR-47](../../docs/semantics/languages.md#wr-47)).

### The CLI's build

`ParseCodebaseSettings` reads the entry: `format`, `external`, `inject`,
`define`, `loader`, `banner` and `assets`. Its `relative_path`, `includes`
and `excludes` do not shape the bundle. `CLIOptions` is [WR-47](../../docs/semantics/languages.md#wr-47)'s table, and
`CLIBundle` runs it as it is; the CLI parity check compares it with what
the CLI uploads.

- **The entry point** is the script file, the single entry point, resolved
  from the sync root. That is the CLI's working directory, which `inject`
  paths and asset sources are relative to too.
- **Bundling:** `bundle`, platform `node`, packages inlined, and output in
  memory under `/`.
- **Format:** `cjs` for an absent `format`, with engine `node20.15.1`, or
  `esm` with target `esnext`.
- **The entry's own settings:** its `external`, `inject`, `define` and
  `banner` unchanged, and its `loader` map in place of the default
  `{".node": "file"}`.
- **The metafile** is on, as on the CLI's deploy path, where it changes no
  output. There is no minification, source map or splitting.

### windmill-lite's transforms

The transforms come from the same single implementation as the Bun
build's ([packaging](../../docs/design/0010-packaging.md)):

- **The env rewrite** ([The env rewrite](#the-env-rewrite)), for the
  entry's format. The define table's keys take precedence over the entry's
  own. The support module is injected before the entry's own `inject`
  files, so it runs before any code of the bundle.
- **Host-provided modules.** Imports that resolve to a listed host-provided
  module's file, or that name a listed package, resolve to that module's
  shim.
- **The plugin.** Both reach esbuild through a plugin. The support module
  and a listed package's shim load from its `windmill-lite` namespace. A
  listed file's shim loads in place of the file when esbuild loads it,
  under its path from the sync root, so the output does not depend on where
  it was built.
- **The entry's own file** is the exception: a listed codebase script that
  is a root is bundled from its own source, with its entry's settings.
- **Other imports** resolve as they would without the plugin. So a listed
  module costs no extra resolution, and the bundle keeps every load effect
  that it would have without the list (`TestHostModulesKeepLoadEffects`).
- **Why no second resolution.** Resolving each import a second time,
  outside esbuild's directory cache, makes a build that imports a package
  of many modules tens of times slower (16 to 44 times, as the test's
  comment records). `TestCodebaseHostModulesKeepBuildTime` fails when a
  build with a listed module takes more than three times the plain build
  plus 300 ms, each the best of three; it does not run the old resolver.
  `BenchmarkCodebaseHostModules` times both builds.
- **Concurrency.** esbuild calls the plugin concurrently, one call per
  import being resolved, so what it records is kept under a lock.
- **The binding name** is chosen as the Bun helper chooses it (`spells`
  applies the same rule).
- **`//native` and `//nodejs` scripts** are not rewritten. A `//native`
  codebase script runs in the native runtime
  ([WR-50](../../docs/semantics/languages.md#wr-50)). It is bundled with
  host-provided modules inlined, and the Bun helper's `native` op writes the
  bundle's native form, `scripts/bun/<id>.native.js`, in place of its
  `.cjs` or `.mjs`.

### Metadata

- **The helper's `parse` op** reads each source for its annotations,
  workflow-as-code markers and packages.
- **Export names.** It records a signature for each export of the bundle.
  The names come from the metafile of an ES module build of the same entry,
  with the same plugin as the real build, so an import of a host-provided
  module resolves to its shim there too (`bundleExports`).
- **CommonJS.** A CommonJS bundle's `module.exports` takes the same names,
  and a function that the entry re-exports with `export *` is signed.
  CommonJS source has no static export list, so its exports are left
  unsigned. A build that cannot read the names says so in an
  `esbuild_warning`.
- **Unsigned exports.** The harness calls a callable export of a codebase
  module that the metadata does not sign with an empty parameter list.
- **Parameters.** A function that the entry source declares gets its
  parameters, as Windmill parses a codebase script's content
  ([WR-6](../../docs/semantics/languages.md#wr-6),
  [WR-7](../../docs/semantics/languages.md#wr-7)). One that it does not
  declare gets an empty list, never a dependency's parameters.
- **More fields.** The metadata adds `bundler: "esbuild"`, the `format`,
  the host-provided modules used, and esbuild's metafile inputs.

### Output and refusals

- **Output:** `scripts/bun/<id>.cjs` for CommonJS (or `.mjs`) and its
  `.meta.json`, and every asset at `assets/<to>`.
- **Warnings.** esbuild's warnings, such as an empty `import.meta` in
  CommonJS output, are build warnings (`esbuild_warning`).
- **Running.** The harness runs a CommonJS bundle on Bun's CommonJS loader,
  a [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations),
  from a copy in its working directory, where it has extracted the assets
  ([harnesses/bun](../../harnesses/bun/README.md)).

| Problem | When |
|---|---|
| `bundle_failed` | Settings of the wrong type, or a `format` other than `cjs` and `esm`; an esbuild error |
| `unsupported_feature` | A bundle with several output files, such as a `file` loader's: windmill-lite does not support that upload layout ([WR-49](../../docs/semantics/languages.md#wr-49)) |
| `invalid_asset` | An asset whose `to` is not a clean relative path, or whose source does not read |
| `asset_collision` | Two scripts that extract different content to one path ([IR and loader](../../docs/design/0002-ir-and-loader.md)). Two that extract the same content to one path share it |

### Partial success

- **One failure stops nothing else.** Scripts build in id order, and one
  that fails does not stop the others.
- **What is written.** As `BuildBun` writes the metadata of every script
  that built, each script whose module and assets succeed gets its
  metadata and its assets written. So a caller that runs only the scripts
  that built, such as the step and flow checkers of `wltest check`, can run
  them.
- **Assets.** An entry's assets are taken all or none. One that collides
  with an asset that an earlier script took is the later script's problem.
- **One tree.** Paths that would not extract to one tree (a file that is
  also another's directory) are the bundle's problem, reported as the item
  `assets` (`asset_collision`). Then no asset or metadata is written.
- **`Build`** still refuses the whole folder on any problem.

## The env rewrite

A harness gives each invocation its own `process.env`
([WA-31](../../docs/semantics/client-api.md#wa-31)). But `Bun.env` is a
separate object, and on Bun 1.4.3 both it and the global `Bun` are
read-only and non-configurable, so the runtime cannot replace them.
`EnvRewrite(format)` returns what every bundler applies instead
([packaging](../../docs/design/0010-packaging.md)):

| Define | Becomes | Formats |
|---|---|---|
| `Bun.env` (also as `Bun["env"]`) | `__windmill_lite_Bun.env` | ESM and CJS |
| `import.meta.env` | `__windmill_lite_Bun.env` | ESM only: in CJS output `import.meta` stays empty, as in the CLI's esbuild bundles |
| `Bun`, `globalThis.Bun`, `global.Bun`, `self.Bun` | `__windmill_lite_Bun` | ESM and CJS |

- **The proxy.** `__windmill_lite_Bun` (`EnvBinding`) is a global that the
  support module defines, once per process, as a proxy of the real `Bun`
  whose `env` is `process.env`.
- **What it forwards.** The proxy forwards every other read, key listing
  and descriptor. It sends writes and deletes to the real `Bun`, so they
  fail as they would there. Freezing or sealing it, or defining a
  non-configurable property on it, throws.
- **The support module first.** Every bundle imports the support module
  before its own code. It is a module of its own, so the bundler links its
  references to `globalThis`, `Reflect`, `Proxy`, `Object` and `process`
  to the real globals, renaming a script's own top-level bindings of those
  names. Nothing is inserted into a script's scope.
- **No capture.** A bundler replaces only free references with a define.
  The build uses `__windmill_lite_Bun` only when no input of the bundle
  spells it, as written or through Unicode escapes; otherwise it adds a
  suffix to the name. So no binding of a script can capture the rewrite.
- **What it reaches.** An alias (`const B = Bun`), destructuring
  (`const { env } = Bun`) and a dependency's reads all see the invocation's
  environment. A local binding named `Bun` is left alone.
- **What a define cannot see** still reads the real environment: imports of
  the `bun` module (`import { env } from "bun"`), optional chaining through
  the global object (`globalThis?.Bun`), reflection, an alias of the
  global object, and `eval` or `new Function`.
- **Which bundles.** The Bun build applies the rewrite to Bun-target
  bundles only. A `//native` script runs at the native boundary. A
  `//nodejs` one, which Windmill runs on Node with Bun's environment
  ([WR-50](../../docs/semantics/languages.md#wr-50)), runs on the Bun
  harness, whose environment view covers it at run time.
- **Virtual inputs.** The inputs that the binding check reads are the
  bundle's files. esbuild's virtual inputs, such as the `<define:NAME>`
  module that it makes of an object or array define value, are not files,
  and the check skips them.

## Shared code

`Config.SharedCode` (`windmill-lite build -shared-code`) builds the bundle
with shared code
([shared module code](../../docs/design/0013-shared-module-code.md)). Its
differences from a per-script bundle are a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
Every script keeps its per-script bundle, byte for byte as without shared
code: `fresh` runs it, and the CLI parity check compares it.

### Build groups

The scripts that one bundler builds with one set of effective settings, in
one installation and one strictness, form a group:

- **Codebase groups** (`shareCode`, here) are keyed by the entry's settings
  text, its format, whether the env rewrite applies, and whether its
  CommonJS output starts with `"use strict"`.
- **Bun-built groups** are the build helper's, keyed by target and lock.
- **Native scripts** are in none, since each of their invocations runs in
  a realm of its own.

### The closure

The closure of a group is a CommonJS bundle of an entry that maps each
dependency module that its scripts reach, by its path from the sync root,
to a function that requires it (`closureEntrySource`).

- It is built with the group's settings, the env rewrite and the shims.
- **`import.meta`.** For an ES module group, dependency code reads
  `import.meta` from a parameter of the instance. That parameter is the
  first of `__wl_meta`, `__wl_meta_2`, … that no input of the closure
  spells, so no binding of a dependency captures it (`buildClosure`, as
  for the env rewrite's binding).
- **The table.** The entry hands the instance's table back through a
  global property of its own (`ClosureTable`), not `module.exports`, which
  stays the step's.

### The closure file

The closure file (`wrapClosure`) is in Bun's own form of a module that a
bundler wrote, which Bun loads without transpiling it again:

- **A CommonJS group's closure** is a CommonJS file (`// @bun @bun-cjs`).
  Its factory runs the bundle with the calling module's scope, so
  dependency code sees the step's, as in its per-script bundle. A strict
  group's closure is strict.
- **What the scope holds.** A CommonJS codebase group passes `module`,
  `exports`, `require` and the file names. A Bun-built group's module is an
  ES module, so it passes `require`, the file names and `import.meta`, and
  no `module` or `exports`.
- **An ES module codebase group's closure** is an ES module
  (`ClosureModuleFile`, `// @bun`), whose factory takes no `module` or
  `exports`. Dependency code that reads one finds no binding, as in its ES
  module per-script bundle.
- **The Bun build helper's closure bundles** go through the same wrapper
  (`BuildBun`), as CommonJS files.

### The shared-layout module

The shared-layout module of a script is a second build of it with the
dependency plugin (`depPlugin`):

- **References.** Each of the entry's imports of a module of its own build
  resolves to a reference to that module in the script's instance, keeping
  the package's `sideEffects`. A module's key is its path from the folder's
  real path, as esbuild resolves files.
- **Their kinds.** A CommonJS module gets a CommonJS reference
  (`depReference`). An ES module gets an ES module reference
  (`esmReference`), whose named exports stay live and whose default is read
  once.
- **Export names.** An ES module reference lists the module's export names
  (`moduleExports`, from a bundle of the module alone). Its namespace
  object then has the module's names, and not the CommonJS interop marker
  of the reference that it reads them from.
- **Extensions.** esbuild takes a module's type from its path's extension
  in every namespace: `.mjs` and `.mts` an ES module, `.cjs` and `.cts`
  CommonJS. So the CommonJS reference that an ES module reference reads
  from has a path of its own ending `.cjs` (`cjsReferencePath`, in the
  namespace `windmill-lite-dep-cjs`).
- **Edge cases.** An ES module reference to a module without exports holds
  an empty export statement. A dependency named `index.mjs` or `x.cts`
  keeps both references' types.
- **What stays.** The env support module, host-provided modules' shims,
  external modules and the entry itself stay as the per-script bundle has
  them.
- **Inject files.** Each is replaced by a proxy that re-exports its names
  from the instance (`injectProxies`). The script's own code and its
  dependencies then reach one instance of it.
- **The instance.** The support module (`closureSupport`) makes the
  instance at the first reference, through the harness runtime's registry
  (`ClosureRegistry`).

### Fallbacks

A script keeps its per-script bundle when shared code would change what it
does. `SharedMeta.Fallback` names the reason, and the warning
`shared_code_fallback` says why. For a codebase script whose cause is one
module (`namespace_import`, `export_names`, `top_level_await`,
`external_import`, `dynamic_import`), the warning names that module too:

| Reason | When |
|---|---|
| `banner` | Its entry sets a `banner` |
| `define_object` | Its entry's `define` gives an object or array value |
| `entry_cycle` | A cycle through its own entry |
| `namespace_import` (`namespaceClash`) | Its entry and another module of its bundle both take the namespace object of one ES module dependency: by a namespace import, a namespace re-export, or an `import()` of a literal specifier. The per-script bundle has one such object; the shared layout would have two |
| `export_names` (`exportNamesClash`) | Its entry takes the namespace object of an ES module whose names are not all known (a star re-export of an external module), or imports a module that exports `__esModule` |
| `top_level_await` | A module of the closure uses top-level await, which fails the CommonJS build |
| `external_import` | In an ES module group, a module of the closure imports an external module statically |
| `dynamic_import` | A module of the closure keeps an `import()` |

### Built-in modules

A module that the runtime has built in is neither an external import nor a
kept `import()`: the runtime resolves it to the same module from every
file (the shared module code record's decision).

- **A specifier names one** when it is one of `builtins`, spelled exactly.
  An `import()` counts only with a literal specifier.
- **`builtins`** holds the specifiers that the build helper accepts on its
  Bun: each name that `node:module`'s `builtinModules` lists, alone or
  after `node:`, that Bun's resolver resolves to a built-in module.
- **Bun's own modules** resolve only bare. So `node:ws` and
  `node:bun:sqlite`, for example, are external modules, while
  `node:fs/promises` is built in. Bun 1.4.3 lists `node:sqlite` only with
  its prefix.
- **The pinned Bun.** The build requires Bun 1.4.3, unless
  `BunConfig.AllowBunMismatch` is set.
- **Babel.** The build helper finds namespace imports and kept imports by
  parsing with Babel. Its `namespaces` op parses each module, and its
  `kept` op parses the closure bundle and returns the built-in specifiers.

### What the bundle records

The manifest's `shared_code` and `closures`, `"D18"` among its deviations
([The deviations a bundle lists](#the-deviations-a-bundle-lists)), and each
script's `shared` record (its closure or its fallback).

## Host-provided modules

A [host-provided module](../../docs/glossary.md#host-provided-module) has
one instance per process, shared by the host and every script that imports
it, a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
Every bundler resolves imports of one to its
[shim](../../docs/glossary.md#shim).

- **`Shim(key, exports)`** is that shim's source. It reads the process-wide
  registry, `globalThis[Symbol.for("windmill-lite.hostModules")]`: a `Map`
  from the module's key, as the host contract names it, to the object that
  holds its exports, normally its module namespace.
- **Loading.** The shim takes each export by name when the step module
  loads. It fails that load with `host-provided module not registered:
  <key>` when the registry or the module is missing.
- **Who registers.** The host SDK registers the host's instances. Elsewhere
  a harness registers the bundled copies from `scripts/bun/provided/`, in
  the manifest's `host_modules` order: `BunResult.HostModuleOrder`, then
  `EsbuildHostResult.Order`. Each puts a module after the modules that it
  imports at load time ([Registration order](#registration-order-of-host-provided-modules)).
- **Export names** are sorted. A name that is not an ASCII identifier is
  written as a string export name.

### Modules that esbuild builds

`BuildEsbuildHostModules(ctx, EsbuildHostConfig)` builds the listed modules
that esbuild bundles, as it bundles the codebase scripts that import them.
Only codebase scripts can import these:

- each file outside the folder, in a codebase entry's `relative_path`;
- each codebase script of the folder, so that its shared copy has its
  entry's `define`, `loader`, `inject` and `banner`, and resolves packages
  from the folder's `node_modules`, as the script's own bundle does
  ([WR-47](../../docs/semantics/languages.md#wr-47)).

**Assets.** The copy takes no `assets` from its entry: a step's working
directory, one per process, holds the assets of the scripts that the
bundle runs. On Windmill, the bundle of a script that imports the
module carries the importing script's entry's assets, not the module's
entry's ([WR-49](../../docs/semantics/languages.md#wr-49)). So a module that
reads an asset of its own entry finds it only when that entry's scripts are
in the bundle, or when the importing script's entry lists the asset too.

The build has these steps:

1. **Live bindings.** The helper's `parse` op reads each source for
   `host_module_live_binding` (`export let` or `export var`), with the
   syntax of the loader that esbuild bundles it with. `loaderFor` picks it
   as esbuild 0.28.2 does: the longest suffix of the name that starts at a
   dot, over esbuild's own loaders, with the entry's `loader` map on top.
   - TSX and JSX parse; another loader's file is not read.
   - A file with no loader (no suffix has one, or its loader is `default`)
     is not read either: esbuild's refusal, `No loader is configured`, is
     its problem.
2. **The import graph.** A first esbuild build of each, with the shims of
   these modules as CommonJS placeholders that take any import name,
   records which listed modules it imports. Its metafile is the import
   graph that orders them
   ([Registration order](#registration-order-of-host-provided-modules)).
   The helper's `lazy` op parses each module of it that holds an
   `import()` of a listed module, in `ts`, `tsx`, `js` or `jsx` syntax.
3. **The copies.** Each is bundled in that order, as an ES module with its
   codebase entry's settings, the env rewrite and the other modules' shims,
   into `scripts/bun/provided/<id>.mjs`. Its shim is generated from the
   export names of that bundle's metafile (`host_module_commonjs` when the
   entry is CommonJS source).
4. **Late imports.** A module that imports a later module lazily is bundled
   again once every copy is: its first copy holds that module's
   placeholder. The second holds the module's late shim (`LateShim`), from
   the export names of the module's copy, a module without exports
   included.
   The two copies must export the same names
   (`host_module_exports_differ`).

The manifest registers these modules after the Bun build's, which cannot
import them. `Build` adds the files of the folder that the copies read
(`EsbuildHostResult.Inputs`) to the sources.

## Registration order of host-provided modules

A listed module registers after each listed module that it imports while
it loads
([packaging](../../docs/design/0010-packaging.md#host-provided-modules)).
`loadorder.go` decides the order for both bundlers' builds, from an
`importGraph` per module: every import of a listed module in its bundle,
with the module that holds it, the bundler's kind and the specifier.

`loadTimeHostModules` returns the listed modules that a module imports at
load time (`HostModuleMeta.LoadHostModules`). A listed module is lazy only
when every import of it is lazy:

| Kind | Imports |
|---|---|
| Lazy | An `import()` of the listed module whose specifier its module imports only from inside functions, as the helper's parse reports ([build/](../../harnesses/bun/build/README.md)). Its module can be any module of the bundle |
| Load time | Every other import, wherever its module sits: a static import or re-export, a `require()`, a top-level `import()`. A module that the bundle loads lazily still reads its static imports while it is initialised, once, so they order too |
| Unknown, counted as load time | An `import()` in a module that does not parse, and a listed module that the bundle imports with no import that the records show |

A module with a top-level `import()` of a specifier that is not a literal
gets no lazy specifiers from the parse, since a bundler can fold that
specifier into any module's.

`hostModuleOrder` orders the modules by a depth-first walk, in the
configured order:

- each module comes after every listed module that it imports at load
  time. A cycle of these fails with `host_module_cycle`, which names the
  cycle by module ID;
- each module also comes after every listed module that it imports only
  lazily, unless that closes a cycle. A lazy import of a module already on
  the walk's path is set aside. When a load-time import closes a cycle
  through a lazy import on the path, the first such lazy import on the
  cycle is set aside, and the walk starts again;
- so a list without lazy cycles gets the order that it had when every
  import ordered it, and builds the same bytes.

### Late imports

An import set aside is late: its importer registers first. The importer's
copy resolves it to the module's late shim (`LateShim`): an ES module that
re-exports each name from a CommonJS module whose getters read the
registry on every read. Late shims are inlined into the copies, and no
file of the bundle holds one alone.

- **Before and after registration.** Before the module registers, an
  `import()` of it and a read of its values throw
  `host-provided module not registered: <key> (…)`, every time; after it,
  both work. A module whose loading awaits such an import at its top level
  fails to load, and both bundlers fail every later import of it with the
  same error.
- **`then`.** The late shim always exports `then`, from the registry like
  its other names. An `import()` reads `then` on the namespace that it
  resolves to, so it rejects before the module registers, a module without
  exports included.
- **Bun's unused `import()`.** Bun 1.4.3 gives an `import()` whose value
  goes unused, or only has exports read, an empty object in place of the
  namespace. Nothing reads `then` then, so in a Bun-built copy such an
  `import()` resolves before the module registers, without its load
  effects. esbuild keeps the namespace.
- **Waiting for load effects.** A module that waits for another's load
  effects keeps the namespace that its `import()` gives.
- **A failed plain shim.** Both bundlers initialise a module once, so a
  plain shim that failed can never yield the registered values: esbuild
  0.28.2 and Bun 1.4.3 both throw the first failure again on every later
  import.

## BuildBun

`BuildBun(ctx, BunConfig)` builds every Bun script and host-provided
module of a bundle with the
[Bun build helper](../../harnesses/bun/build/README.md):

1. **Scan,** when there are host-provided modules. The helper reports each
   module's export names, the listed modules that it imports and the
   packages that it imports, or `host_module_live_binding` (`export let` or
   `export var`) or `host_module_commonjs` (a CommonJS module). For a module that imports another listed module, it also reports
   the import graph of the scan's bundle. `BuildBun` writes
   `scripts/bun/provided/shims/<id>.mjs` for each, and orders them
   ([Registration order](#registration-order-of-host-provided-modules)).
2. **Build.** The helper bundles the host-provided copies and every script.
   A copy resolves its imports of a module that it imports lazily and
   registers before to that module's late shim, which the request carries.
   It bundles the host plugin, when there is one, into `plugins/<id>.mjs`,
   with every host-provided module resolved to its shim, so the plugin and
   the steps share each module's instance in a process.
3. **Checks and metadata.** `BuildBun` checks each copy's exports against
   its scan (`host_module_exports_differ`), and records the workspace
   scripts that each copy inlined. It writes `scripts/bun/<id>.meta.json`,
   a `ScriptMeta` with the metafile in canonical form.
4. **Warnings.**
   - `host_module_shared_dependency`: a script imports a package directly
     that a host-provided module that it uses also imports, so the package
     exists twice in the process. A package that is itself listed is the
     exception, since both resolve to its shim.
   - `native_import_unavailable`: a static import of a native-boundary
     script that the native runtime cannot load: anything but the embedded
     client, which only a `nativets` script reaches
     ([WR-40](../../docs/semantics/languages.md#wr-40)). The script builds,
     and the step fails when it runs, as on Windmill.

### Native scripts

Native scripts run in the Bun harness under a native boundary mode, a
[named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).

- **Which scripts.** `BunScript.Language` is the language that a deploy
  records. A `nativets` script, and a Bun-family one whose header says
  `//native`, are built in their native form
  ([build/](../../harnesses/bun/build/README.md)):
  `scripts/bun/<id>.native.js`, and no `.mjs`.
- **Their metadata.** Their `ScriptMeta` has `boundary` `native`, and a
  `native` record (`NativeMeta`). The record holds the kind, the function's parameter and
  preamble offset, the static imports and the links that they need, the
  `//useragent` and `//proxy` header, and a `nativets` script's transpile
  error, which does not fail the build.
- **The line map.** A `nativets` script's `native` record also holds its
  transpile's line map (`line_map`). The harness reads in it the source
  line in front of an error's stack
  ([WR-43](../../docs/semantics/languages.md#wr-43)).
- **No lock.** A `nativets` script imports nothing at build time, so it is
  never `missing_lock`.

### Rules of the Bun build

- **Locks.** A script's lock is in the format of
  [WR-25](../../docs/semantics/languages.md#wr-25). Scripts with one lock
  share one dependency set, keyed by its digest.
- **The pinned Bun.** The build runs on `harness.PinnedBun`, and refuses
  any other Bun (`HelperError`, `runtime_mismatch`) unless
  `AllowBunMismatch` is set. Then `BunResult.NonconformantRuntime` says so.
- **`NODE_ENV`.** The helper runs without `NODE_ENV`, which `Bun.build`
  inlines, so a bundle does not depend on the builder's.
- **Its process group.** The helper runs in a process group of its own.
  When a run ends, by its timeout, cancellation or otherwise, `BuildBun`
  kills what is left of the group (an install's lifecycle scripts), and
  waits until none of it remains.
- **IDs.** An ID is one or more `/`-separated segments, none empty, `.` or
  `..`, and none holding a backslash or NUL: the rule that the harness
  runtime applies to an inventory id. A script's first segment is not
  `provided`.
- **What `BunConfig` refuses:** other IDs, IDs that differ only in case
  (they name files), and a folder host module whose file is not the script
  at its path.
- **Problems** with scripts or modules come back together as a
  `*BuildError`, with the result so far. A script that Windmill would fail
  to bundle at every run (`bundle_failed`) fails the build, a
  [restriction](../../docs/design/0001-equivalence-contract.md#5a-restrictions)
  of the equivalence contract.

The bundle tree that it writes:

| Path | Content |
|---|---|
| `scripts/bun/<id>.mjs` | The bundled module |
| `scripts/bun/<id>.native.js` | A native-boundary script's module, in its native form, instead |
| `scripts/bun/<id>.meta.json` | Its `ScriptMeta`: import base, target, boundary, annotations, workflow-as-code markers and exports. The signature of each export and of each other top-level function declaration; the workspace scripts inlined, packages and host-provided modules used; the metafile. Its `shared` record with shared code, and for a native-boundary script its `native` record |
| `scripts/native/windmill.js`, `windmill.meta.json` | The embedded client of `nativets` scripts, in its native form, and its record |
| `scripts/bun/provided/<id>.mjs` | A host-provided module's bundled copy |
| `scripts/bun/provided/shims/<id>.mjs` | Its shim |
| `scripts/bun/closures/<id>.cjs`, `scripts/bun/<id>.shared.mjs` | With shared code, each Bun-built group's closure, and each script's shared-layout module |

The same inputs give the same files, from any folder, work or output
directory (`TestBuildIsReproducible`).

## ImportBase

`ImportBase(flowPath, moduleID, explicitPath)` returns `explicitPath`
when it is not empty, otherwise `<flowPath>/<moduleID>`, with a
nested-body segment collapsed to `<prefix>/flow` by `ir.CollapseBodies`.
Which of these a step gets, by its slot, is the compiler's rule
([The script inventory](../ir/README.md#the-script-inventory),
[WR-29](../../docs/semantics/languages.md#wr-29)). `Build` takes
`Script.ImportBase` from the program and does not call this function,
whose cases `TestImportBase` checks.

## Tests

The build tests run the real build helper on the pinned Bun. The archive,
manifest, digest, shim-order and loader tests need no Bun.

### Run the tests

1. Install the build helper's dependencies once:
   `(cd harnesses/bun/build && bun install --frozen-lockfile)`.
2. Export `WL_BUN` with the path of a Bun 1.4.3 executable.
3. Run the package's tests, as CI's Go jobs do:
   `go test -race ./internal/bundle`

```sh
(cd harnesses/bun/build && bun install --frozen-lockfile)
WL_BUN=/path/to/bun-1.4.3/bun go test -race ./internal/bundle
```

To rewrite the golden files of the env rewrite and the shims in
`testdata/`, run `go test ./internal/bundle -run Golden -args -update`.

To run the build-time benchmark, which needs no Bun:
`go test -run '^$' -bench BenchmarkCodebaseHostModules ./internal/bundle`

CI runs these tests in the Go buckets of `ubuntu-latest` and
`macos-latest`, with `WL_REQUIRE_BUN` set
([docs/ci.md](../../docs/ci.md#the-go-jobs)). The Codebase parity workflow
runs the CLI parity check
([docs/ci.md](../../docs/ci.md#codebase-parity)). CI also builds the
fixture bundle twice, from two directories, and compares the archives.

| Variable | Value | Tests that need it | When it is not set |
|---|---|---|---|
| `WL_BUN` | The path of a Bun 1.4.3 executable | Every test that runs the build helper or a built module | The test skips, or fails when `WL_REQUIRE_BUN` is set. A missing `harnesses/bun/build/node_modules` does the same |
| `WL_REQUIRE_BUN` | Any non-empty value | The same | Missing tools skip their tests |
| `WL_WMILL_CLI` | The command that runs the unmodified `windmill-cli` at the oracle's pinned version (`oracle.CLIPackage`) | `TestCLIParity` | The test skips. It runs wherever the variable is set; CI's Codebase parity workflow sets it. The CLI is never a dependency of the module |

### The conformance build cases

`TestConformanceBuild` (`conformance_test.go`) runs every case of
[conformance/build](../../conformance/README.md): it writes the case's
folder, builds it, and checks what the build recorded or refused.
`TestConformanceBuildCatalogue` fails unless every catalogue case of a
finding with a case file is a case, or is listed in `deferred.json`.

- **Cases with a `run`** import the built modules on the pinned Bun, with
  `process.env` replaced as a harness's view does and the host-provided
  copies registered, and check what their exports return.
- **npm packages.** Synthetic npm packages come from a registry that the
  test serves, with an install cache of its own. A package's
  `package.json` dependencies are installed with it.
- **Re-exports.** The [WR-27](../../docs/semantics/languages.md#wr-27)
  cases check that a module that a `sideEffects: false` package re-exports
  stays in the bundle, behind an ordinary package import and behind a bare
  import that ends in `.ts`.
- **Whole-folder cases** (`given.folder`) run `Build` instead, and list
  every problem that it must report: workflow-as-code
  ([WA-33/c](../../docs/semantics/conformance.md#wa-33),
  [WR-32](../../docs/semantics/languages.md#wr-32)), the missing lock
  ([WR-26](../../docs/semantics/languages.md#wr-26)), and the codebase
  refusals ([WR-47](../../docs/semantics/languages.md#wr-47),
  [WR-49](../../docs/semantics/languages.md#wr-49)). A clean one can check
  what it recorded for each script, its assets and warnings, and run its
  modules.
- **Codebase settings.** The whole-folder cases cover the CLI's settings
  ([WR-47](../../docs/semantics/languages.md#wr-47)), assets ([WR-49](../../docs/semantics/languages.md#wr-49)), and the env rewrite in CommonJS and ES module
  output ([WA-31](../../docs/semantics/client-api.md#wa-31)).
- **Host-provided modules.** The whole-folder cases of host-provided
  modules cover shims, and a listed codebase script that is itself a root.
  They cover the shared copy of a listed codebase script with its entry's
  settings, a Bun script's refused import of one, and the
  shared-dependency warning for codebase scripts. They also cover modules
  outside the folder, whose files a case gives in `given.outside`: TSX and
  JSX modules, and one with no loader.
- **The registration order.** The order cases cover cycles that close
  through a lazy import and through a load-time one: direct, through a
  module that is not listed, between listed packages, and through a folded
  specifier, for both bundlers.
- **Late registration.** They also cover a lazy import that runs before its
  module registers, caught and uncaught (`run.register_error`), a module
  whose loading awaits one, and an `import()` of a module without exports
  attempted twice before its registration. An `import()` made to wait for a
  module's load effects keeps the namespace that it gives, and a Bun-built
  copy's `import()` whose value goes unused resolves before the
  registration.
- **Shared code.** The shared-code cases (`given.shared_code`) check
  `expect.shared`: each script's record by a case-local closure label,
  what each closure names and contains, and text of each script's
  shared-layout module. For `t-closure-membership` they also check that the
  per-script bundles are the same bytes as without shared code.

### Other tests

| File | Test | What it checks | Needs |
|---|---|---|---|
| `build_test.go` | `TestBuildFixture` | Builds `testdata/fixture`: a flow, an inline step, a path script, a workspace import, a host-provided module, a codebase script with a define, an import of its codebase and an asset, a resource, a resource type, a variable and a secret. Checks the bundle's files, every manifest field, `data/` and `ir.json`, and that its modules run on Bun sharing the host-provided module | `WL_BUN` |
| `build_test.go` | `TestBuildTwiceGivesOneDigest` | Two builds of copies of the fixture in other directories, with other work and output directories, give one digest, one manifest and one archive, byte for byte | `WL_BUN` |
| `build_test.go` | `TestDigestFollowsInputs` | Changing each input in turn changes the digest. The inputs are a flow's `stop_after_if`, scripts, the host-provided module and its metadata, a lock, data, the contract's roots, the mode, the engine version, the harness runtime, a codebase script, a module that it imports, its entry's define, its asset. A secret's file, a file that the build does not read, a script that nothing reaches, and an empty `provided` list leave it alone | `WL_BUN` |
| `build_test.go` | `TestBuildOutput`, `TestBuildRefusesUnbuilt`, `TestBuildCompileErrors` | The output directory, and the refusals | `WL_BUN` for the first two |
| `build_test.go`, `argsfile_test.go` | `TestBuildNoArgsFileReader`, `TestArgsFileFree` | `no_args_file_reader`: unset for the fixture, whose scripts call a host-provided module; set for a bundle of plain steps; unset once one step reads the file by a built name. `TestArgsFileFree` holds the search to its rule on code snippets | `WL_BUN` for the first |
| `sharedcode_test.go` | `TestBuiltins` | The build helper's `kept` op: an `import()` of `node:async_hooks` is not kept, and one of a variable or of `node:ws` is. The built-in specifiers hold Node's modules and their subpaths, alone and after `node:`, and Bun's own alone; `node:bun:sqlite` and a `node:` name outside the list are not built in | `WL_BUN` |
| `outside_test.go` | `TestLoaderFor` | The loader picks: compound suffixes over shorter ones from either map, `default`, a name without a dot, the CLI's `.node` default. The `codebase-outside-compound-*` cases check them against esbuild's builds | |
| `outside_test.go` | `TestBuildOutsideHostModuleSources` | A file of the folder that only an outside module's copy reads is a source, and a comment in it, which leaves the copy alone, changes the digest | `WL_BUN` |
| `outside_test.go` | `TestBuildOutsideHostModule`, `TestOutsideID` | A codebase script that imports, from outside the folder, a module that holds an `AsyncLocalStorage` store, listed by its path from the sync root: the manifest, the copy, the shim and the script's metadata. The script then runs with the host's own instance registered, inside and outside the host's scope, and with the bundled copy. `TestOutsideID` checks the ids | `WL_BUN` for the first |
| `hostmodules_test.go` | `TestCodebaseHostModulesKeepBuildTime`, `BenchmarkCodebaseHostModules` | Six codebase scripts that import a package of 400 modules, without and with a listed module. The test fails when the build with one listed takes more than three times the plain build plus 300 ms, each the best of three runs; the benchmark times both | |
| `hostmodules_test.go` | `TestHostModulesKeepLoadEffects` | One folder built without and with host-provided modules, each step run in a process of its own with every copy registered. The steps see the same load effects (`sideEffects: false` packages and directories, outside modules listed or not, a listed module's Bun copy, a Bun script with a lock), but for one difference: a listed module that a step imports without using | `WL_BUN` |
| `plugin_test.go` | `TestBundleDigestCoversPlugin`, `TestBuildPluginProblems`, `TestVerifyRefusesPluginReference` | A host contract that names a plugin: the manifest records it, and `files` and `sources` hold its bundle's and its source's digests. Another plugin source changes the bundle digest, and `Verify` refuses a plugin changed after the build. A plugin that is no `bun` script of the folder (no script, a Deno or a `//native` one), or that imports a listed codebase script, is refused, named. `Verify` refuses a plugin entry outside `plugins/`, unlisted, or outside the bundle | `WL_BUN` |
| `verify_test.go` | `TestVerify`, `TestVerifyArchiveEntries`, `TestWriteArchive`, `TestExtract`, `TestParseManifest`, `TestTreeDigest`, `TestCanonicalize`, `TestManifestDeviations`, `TestFoldKey` | A changed, extra or missing file; a symbolic link; a forgery whose own manifest matches. Archive entries that repeat or escape; directory entries that collide with a file, lie below one, or that the bundle does not have. Links below a harness runtime's directory, the canonical form, `ModeDeviations` both ways, and the case-folding key against `strings.EqualFold`. An extracted archive verifies with the archive's digest, and a non-empty directory is refused | |
| `env_test.go`, `shim_test.go` | `TestEnvRewriteGolden`, `TestShimGolden`, `TestLateShimGolden`, `TestShimBehaviour`, `TestEnvRewriteFormats`, `TestShimRefusals` | The define tables, the support module, generated shims and late shims, pinned in `testdata/`; shims run on Bun; the rewrite per format; the shim's refusals | `WL_BUN` for `TestShimBehaviour` |
| `loadorder_test.go` | `TestLoadTimeHostModules`, `TestHostModuleOrder`, `TestHostModuleOrderWithoutLazyCycles`, `TestLazyImports` | Which imports of listed modules are lazy; the order, with its late imports and cycle errors. 6,000 random lists of up to 8 modules: each without lazy cycles gets the order that counting every import gives. The helper's `lazy` request on `import()` calls in functions, at top level and in class bodies, with literal and folded specifiers | `WL_BUN` for the last |
| `parity_test.go` | `TestCLIParity`, `TestCLIOptionsFollowWR47` | The CLI parity check of the [IR and loader](../../docs/design/0002-ir-and-loader.md) design record ([Q-WR-24](../../docs/semantics/languages.md#q-wr-24)): each synthetic [WR-47](../../docs/semantics/languages.md#wr-47) fixture, pushed with `wmill script push` to a recording HTTP stub, uploads exactly `CLIBundle`'s output (`main.js`, or a tar of it and the assets). `TestCLIOptionsFollowWR47` runs everywhere. It checks `CLIOptions`: CommonJS with a `node20.15.1` engine for an entry with no format, ES modules with `esnext` for `esm`, and the fixed options. It also checks that `go.mod` pins esbuild at `EsbuildVersion` | `WL_WMILL_CLI` for the first |
| `esbuild_test.go` | `TestSpells`, `TestBuildCodebaseConcurrentHostImports` | The binding-name collision rule; 64 modules that each import a listed folder module and a listed package, which esbuild resolves concurrently (under `-race` in CI), then the bundle runs | `WL_BUN` for the second |
| `esbuild_test.go` | `TestBuildCodebasePartialSuccess` | A script that fails, one that builds and reads its asset, and one whose asset collides: only the one that built has its metadata and asset, and runs. Assets that cannot form one tree write nothing | `WL_BUN` |
| `bun_test.go`, `importbase_test.go`, `proc_unix_test.go` | `TestBuildIsReproducible`, `TestMetaFile`, `TestBuildLocks`, `TestBuildRefusesAnotherBun`, `TestBuildIgnoresNodeEnv`, `TestBuildRefusesAncestorPackages`, `TestHelperRefusesPathsOutsideTheFolder`, `TestBuildCancelsInstallTree`, `TestBunConfigValidation`, `TestImportBase`, `TestNativeURL` | Reproducible output, the metafile, locks, another Bun, `NODE_ENV`, packages above the folder, paths outside the folder, the install tree killed at a timeout, `BunConfig`'s checks, `ImportBase`, the native runtime's URL | `WL_BUN`, except for `TestBunConfigValidation`, `TestImportBase` and `TestNativeURL` |
| `annotations_test.go` | `TestAnnotationCases` | `conformance/build/annotations.json` through both readers of leading-comment annotations: `syncfolder.HasAnnotation` and the helper | `WL_BUN` for the helper's half |

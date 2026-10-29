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

Nothing opens a bundle to run it yet: that is `wl.Open` (F1).

## Files

| File | Contents |
|---|---|
| `build.go` | `Build` and `Config`: the whole build, its refusals, `data/` |
| `manifest.go` | `Manifest`, `Canonicalize`, `Digest`, `ParseManifest`, the deviations a bundle lists |
| `sources.go` | the recording file system behind the source inventory; `TreeDigest` |
| `archive.go` | `WriteArchive`, `WriteArchiveFile` and `Verify` |
| `env.go` | `EnvRewrite`: the define table and support module of the env rewrite |
| `shim.go` | `Shim`: the shim of a host-provided module; the registry key |
| `importbase.go` | `ImportBase`: an inline script's import base |
| `bun.go` | `BuildBun`: the two helper runs, the shims, the metadata, the registration order and warnings |
| `esbuild.go` | `CodebaseSettings` (a codebases entry, WR-47), `CLIOptions` and `CLIBundle` (the pinned CLI's build), `BuildCodebase`, assets |
| `meta.go` | `ScriptMeta` and the other result types, problems and warnings |

## The bundle

A bundle is a directory, or the same tree as one `.wlb` archive:

| Path | Content |
|---|---|
| `manifest.json` | the manifest (below), in canonical form |
| `ir.json` | the compiled program, `(*ir.Program).MarshalIndent` ([internal/ir](../ir/README.md)) |
| `scripts/bun/<id>.mjs`, `<id>.meta.json` | each Bun script's module and metadata (`BuildBun`, below), named by its inventory id: `scripts/bun/f/app/report.mjs` for the path script `f/app/report`, `scripts/bun/inline-<16 hex>.mjs` for an inline step, as the harness runtime resolves an id |
| `scripts/bun/<id>.cjs`, `<id>.meta.json` | a codebase script built as CommonJS (`BuildCodebase`, below); one built as ESM is `<id>.mjs` |
| `assets/<to>` | every codebase entry's assets, at the path in the working directory they are extracted to |
| `scripts/bun/provided/<id>.mjs`, `provided/shims/<id>.mjs` | each host-provided module's bundled copy and shim, named by its script path |
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
| `module_state` | `warm` or `fresh` ([ADR 0006](../../docs/design/0006-module-state.md)) |
| `deviations` | the deviations of [ADR 0001](../../docs/design/0001-equivalence-contract.md) §5 in effect: the program's (D5, D6, D7, D12, D13), D1, D2 and D9 to D11 in `warm` mode, and D3, D8, D14, D16 and D17, which every bundle has. Those that depend on a run-time setting (D4, D15, D2's watchdog) are the run trace's |
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
   script of the folder (`f/shared/ctx.ts`), whose path becomes its id; a
   script that fails to read is refused with its load error;
4. runs `BuildBun` for every script of the inventory that is not a
   codebase script, with every `.ts` script of the folder available to
   workspace imports, then `BuildCodebase` for the codebase scripts, each
   with the settings of the `codebases` entry that selects it
   ([SY-8](../../docs/semantics/openflow.md#sy-8)) and the shims the scan
   wrote;
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
6. writes `ir.json` and `data/`, then the manifest, last.

Before bundling it refuses, with `unsupported_feature`, a host-provided
module that is not a script of the folder: a host-provided package would
need a lock, which the contract cannot give yet. Problems with scripts and modules come back
together as a `*BuildError`, sorted by item; a path script is named
`script <path>`, an inline step `<flow> step '<id>' (script <inventory id>)`.

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
  esbuild through a plugin that only maps paths: the support module and
  the shims load from its `windmill-lite` namespace, so no bundle names a
  file outside the folder and the output does not depend on where it was
  built. esbuild calls the plugin concurrently, one call per import
  being resolved, so what it records is kept under a lock. The binding
  name is chosen as the Bun helper chooses it (`spells` applies the same
  rule). `//native` and `//nodejs` scripts are not
  rewritten.
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
rewrite to Bun-target bundles only; `//native` and `//nodejs` ones are the
native boundary's (C3n).

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

## BuildBun

`BuildBun(ctx, BunConfig)` builds every Bun script and host-provided module
of a bundle:

1. **Scan**, when there are host-provided modules: the helper reports each
   module's export names, the listed modules it imports and the packages it
   imports, or `host_module_live_binding` for `export let` or `export var`,
   or `host_module_commonjs` for a CommonJS module.
   `BuildBun` writes `scripts/bun/provided/shims/<id>.mjs` for each and
   orders them, failing on a cycle (`host_module_cycle`).
2. **Build**: the helper bundles the host-provided copies and every script.
   `BuildBun` checks each copy's exports against its scan
   (`host_module_exports_differ`), records the workspace scripts each copy
   inlined, and writes `scripts/bun/<id>.meta.json`, a `ScriptMeta` with the
   metafile in canonical form.
3. **Warnings**: `host_module_shared_dependency` when a script imports a
   package directly that a host-provided module it uses also imports, so the
   package exists twice in the process (unless the package is itself a
   listed one, whose shim both resolve to).

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
| `scripts/bun/<id>.meta.json` | its `ScriptMeta`: import base, target, annotations, workflow-as-code markers, exports, the signature of each export and of each other top-level function declaration, the workspace scripts inlined, packages, host-provided modules used, the metafile |
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
  folder, builds it and checks what the build recorded or refused. Cases with
  a `run` import the built modules on the pinned Bun, with `process.env`
  replaced as a harness's view does and host-provided copies registered, and
  check what their exports return. Synthetic npm packages come from a
  registry the test serves, with an install cache of its own.
  `TestConformanceBuildCatalogue` fails unless every catalogue case of a
  finding with a case file is a case or listed in `deferred.json`.
  Whole-folder cases (`given.folder`) run `Build` instead, and list every
  problem it must report: workflow-as-code (WA-33/c, WR-32), the missing
  lock (WR-26), the codebase refusals (WR-47, WR-49). A clean one can check
  what it recorded for each script, its assets and warnings, and run its
  modules: the codebase cases of WR-47 (the CLI's settings), WR-49 (assets),
  WA-31 (the env rewrite in CommonJS and ESM output) and D12 (shims).
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
- `TestVerify`, `TestVerifyArchiveEntries`, `TestWriteArchive`,
  `TestParseManifest`, `TestTreeDigest`, `TestCanonicalize` and
  `TestManifestDeviations` need
  no Bun: they cover a changed, extra or missing file, a symbolic link, a
  forgery whose own manifest matches, archive entries that repeat or escape,
  directory entries that collide with a file or lie below one, in the same
  or another case (before or after it, against a release manifest and a
  digest), directory entries the bundle does not have, ordinary directory
  headers, links below a harness runtime's directory, and the canonical
  form. `TestFoldKey` checks the case-folding key against
  `strings.EqualFold`.
- `TestEnvRewriteGolden` and `TestShimGolden` pin the define tables, the
  support module and generated shims in `testdata/`; `go test ./internal/bundle -run
  Golden -args -update` rewrites them. `TestShimBehaviour` runs shims on Bun.
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

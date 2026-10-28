# internal/bundle

**Current state:** milestone C2a of
[ADR 0012](../../docs/design/0012-layout-and-milestones.md). This README
covers `internal/bundle`. The package holds the build parts that both
bundlers share and the Bun side of the build: the env rewrite, the shim
generator, the import base of inline scripts, and `BuildBun`, which runs the
[Bun build helper](../../harnesses/bun/build/README.md). Nothing calls it
yet. The bundle format, its manifest and digest, `windmill-lite build` and
`verify` are milestone F4; codebase bundling through esbuild, which uses the
same env rewrite and shims, is C2b.

## Files

| File | Contents |
|---|---|
| `env.go` | `EnvRewrite`: the define table and prelude of the env rewrite |
| `shim.go` | `Shim`: the shim of a host-provided module; the registry key |
| `importbase.go` | `ImportBase`: an inline script's import base |
| `bun.go` | `BuildBun`: the two helper runs, the shims, the metadata, the registration order and warnings |
| `meta.go` | `ScriptMeta` and the other result types, problems and warnings |

## The env rewrite

A harness gives each invocation its own `process.env`
([WA-31](../../docs/semantics/client-api.md#wa-31)), but `Bun.env` is a
separate object, and on Bun 1.3.10 both it and the global `Bun` are
read-only and non-configurable, so the runtime cannot replace them.
`EnvRewrite(format)` returns what every bundler applies instead
([ADR 0010](../../docs/design/0010-packaging.md) step 5):

| Define | Becomes | Formats |
|---|---|---|
| `Bun.env` (also as `Bun["env"]`) | `process.env` | ESM and CJS |
| `import.meta.env` | `process.env` | ESM only: in CJS output `import.meta` stays empty, as in the CLI's esbuild bundles |
| `Bun`, `globalThis.Bun`, `global.Bun`, `self.Bun` | `__windmill_lite_Bun` | ESM and CJS |

The prelude, put before the bundle as a banner, binds `__windmill_lite_Bun`
to a proxy of the real `Bun` whose `env` is `process.env`; it forwards every
other read, key listing and descriptor, and sends writes and deletes to the
real `Bun`, so they fail as they would there; freezing or sealing it, or
defining a non-configurable property on it, throws. So an alias
(`const B = Bun`), destructuring (`const { env } = Bun`) and a dependency's
reads all see the invocation's environment. A local binding named `Bun` is
left alone. What a define cannot see still reads the real environment:
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
   imports, or `host_module_live_binding` for `export let` or `export var`.
   `BuildBun` writes `scripts/bun/provided/shims/<id>.mjs` for each and
   orders them, failing on a cycle (`host_module_cycle`).
2. **Build**: the helper bundles the host-provided copies and every script.
   `BuildBun` checks each copy's exports against its scan
   (`host_module_exports_differ`) and writes `scripts/bun/<id>.meta.json`,
   a `ScriptMeta` with the metafile in canonical form.
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
builder's. `BunConfig` refuses IDs that differ only in case (they name
files) and a folder host module whose file is not the script at its path.
Problems with scripts or modules
come back together as a `*BuildError`, with the result so far; a script that
Windmill would fail to bundle at every run (`bundle_failed`) fails the build
(ADR 0001 §5a).

The bundle tree it writes:

| Path | Content |
|---|---|
| `scripts/bun/<id>.mjs` | the bundled module |
| `scripts/bun/<id>.meta.json` | its `ScriptMeta`: import base, target, annotations, workflow-as-code markers, exports, the signature of each export, the workspace scripts inlined, packages, host-provided modules used, the metafile |
| `scripts/bun/provided/<id>.mjs` | a host-provided module's bundled copy |
| `scripts/bun/provided/shims/<id>.mjs` | its shim |

The same inputs give the same files, from any folder, work or output
directory (`TestBuildIsReproducible`).

## ImportBase

`ImportBase(flowPath, moduleID, explicitPath)` is an inline script's import
base ([WR-29](../../docs/semantics/languages.md#wr-29)): `<flowPath>/<moduleID>`
at any nesting, or the explicit `path`, with a nested-body segment
(`forloop-0` and the like) collapsed to `<prefix>/flow`. A path script's
import base is its own path.

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
- `TestEnvRewriteGolden` and `TestShimGolden` pin the define tables, the
  prelude and generated shims in `testdata/`; `go test ./internal/bundle -run
  Golden -args -update` rewrites them. `TestShimBehaviour` runs shims on Bun.
- `TestBuildIsReproducible`, `TestMetaFile`, `TestBuildLocks`,
  `TestBuildRefusesAnotherBun`, `TestBuildIgnoresNodeEnv`,
  `TestBuildRefusesAncestorPackages`, `TestHelperRefusesPathsOutsideTheFolder`,
  `TestBunConfigValidation` and `TestImportBase` cover the rest.

# harnesses/bun/build

**Current state:** working. The helper bundles a folder's Bun scripts and
transpiles its `nativets` scripts, parses the sources of codebase scripts,
writes the native form of native scripts, builds shared code, and reads the
host-provided modules that the Go build needs. The Go build in
[internal/bundle](../../../internal/bundle/README.md), behind
`windmill-lite build`, runs it; a person runs it by hand only to debug a
request. This README covers
`harnesses/bun/build`.

The [build helper](../../../docs/glossary.md#build-helper) is the part of
`windmill-lite build` that runs on [Bun](../../../docs/glossary.md#bun). It
turns each Bun [script](../../../docs/glossary.md#script) of a
[sync folder](../../../docs/glossary.md#sync-folder) into the module that
the [bundle](../../../docs/glossary.md#bundle) holds, as Windmill's own Bun
build would, and reads each script's signature. The
[ADR](../../../docs/glossary.md#adr)
[Packaging](../../../docs/design/0010-packaging.md) defines the build that
it serves. A person can also run the helper by hand on a request file
([Running](#running)).

## What the helper does to your scripts

This section is for a person who runs `windmill-lite build`. The sections
after it are for contributors to the helper.

- **Where it comes from.** Neither a GitHub Release nor the npm package
  ships the helper, so a build takes `harnesses/bun/build/main.ts` from a
  checkout of the engine's version.
- **How the command finds it.** Give `windmill-lite build` the path of
  `main.ts` with `-helper`, or set `WL_BUILD_HELPER`
  ([cmd/windmill-lite](../../../cmd/windmill-lite/README.md#build)).
- **What it needs.** Bun 1.4.3, the version that windmill-lite pins
  ([WR-3](../../../docs/semantics/languages.md#wr-3)), and its own
  dependencies, installed once ([Running](#running)). The helper refuses
  another Bun unless the caller allows it.
- **An ordinary Bun script.** The helper installs the packages of the
  script's lock, applies Windmill's two text rewrites of imports, bundles
  the script with `Bun.build` into one ES module, and records its
  signature, annotations and imports
  ([Bundling each script](#bundling-each-script)).
- **A `nativets` script.** The helper installs nothing and rewrites no
  import: it transpiles the script and writes its native form. A syntax
  error that Bun's transpiler reports does not stop the build: the step
  fails when it runs ([Native scripts](#native-scripts)).
- **A `//native` script** is bundled like an ordinary one for its target
  (`browser`, or `node` with `//nodejs`), without the env rewrite, then
  written in its native form.
- **A codebase script** is bundled by esbuild in Go. The helper only parses
  its source, and writes the native form of a `//native` codebase script's
  bundle ([Requests](#requests)).
- **What it refuses.** It refuses an `//npm` script, a lock with neither the
  `//bun.lock` nor the `//bun.lockb` separator, a failed install, and a
  script that does not parse. A `nativets` script that Bun's transpiler
  rejects is the exception: its step fails when it runs.
- **More refusals.** It also refuses a bundle that resolves a file outside
  its dependency set or emits more than one file, and a host-provided
  module that is CommonJS or whose source directly exports a `let` or `var`
  declaration
  ([Exit codes and problems](#exit-codes-and-problems)).
- **How a problem shows.** A problem with one script is reported against
  that script, and the other scripts still build. `windmill-lite build`
  then lists the problems of every build that ran, each with its script,
  and exits with 1
  ([internal/bundle](../../../internal/bundle/README.md#build)).

## Running

From the repository root:

1. Install the helper's dependencies once:
   `(cd harnesses/bun/build && bun install --frozen-lockfile)`.
2. Write a request, such as `request.json` ([Requests](#requests)).
3. Run the helper with the request's path and the path where it writes the
   response: `bun harnesses/bun/build/main.ts request.json response.json`.

The Go build writes the request, runs the helper on the pinned Bun, and
reads the response. A person runs it by hand only to debug a request.

### Dependencies

The helper has two dependencies, pinned in `package.json` with their own
dependencies in `bun.lock`. `node_modules/` is not committed.

| Package | Version | Licence | What it is for |
|---|---|---|---|
| `@babel/parser` | 8.1.3 | MIT | Every parse of the helper: signatures, pinned imports, packages and live bindings in TypeScript, host sources in their own syntax, the native form in JavaScript, and the `kept`, `namespaces` and `lazy` requests |
| `windmill-client` | 1.326.0 | Apache-2.0 | The package that the native runtime's embedded client is built from |

`windmill-client` stays at the version that Windmill embeds, at v1.862.0 as
at v1.767.0 ([WR-40](../../../docs/semantics/languages.md#wr-40)); Renovate
holds it there.

## Files

| File | Contents |
|---|---|
| `main.ts` | Reads the request, checks the Bun version, runs the request, writes the response |
| `protocol.ts` | The request and the response |
| `build.ts` | Dependency installs, the import transforms and resolution, `Bun.build`, host-provided modules, the metafile |
| `imports.ts` | Pinned-import removal, the `.ts` rewrite, workspace paths, package names |
| `signature.ts` | The parse: signatures, `Date` marking, import specifiers, live bindings |
| `annotations.ts` | Leading-comment annotations and the bundler target that they select |
| `wac.ts` | Workflow-as-code detection |
| `native.ts` | The native form of a module, the nativets transpile and its line map, the `//useragent` and `//proxy` header, the embedded client |
| `text.ts` | Rust's line and whitespace rules, for the textual scans |
| `shared.ts` | Shared code: the support module, dependency references and closure entry, the closure id, the runtime's built-in modules, the parses for kept `import()` and namespace imports, and `syntaxOf` |
| `loadorder.ts` | What orders host-provided modules: where each `import()` sits (`lazyImports`), and the import graph of a bundle (`bundleImports`) |

## Requests

A request file is JSON with `version` 1 and an `op`. The Go build in
[internal/bundle](../../../internal/bundle/README.md) sends each one;
`protocol.ts` has the types, and the Go types mirror them.

| `op` | What the helper does | Sent by | Returns |
|---|---|---|---|
| `scan` | Reads the exports of each host-provided module that the Bun build bundles, so that Go can write its shim | `BuildBun`, first, when it has host-provided modules | `host_modules`: each module's `exports`, `host_modules` and `packages`, and its import `graph` when it imports another listed module |
| `build` | Builds the entries of the Bun build, which holds no codebase script. It bundles each Bun script, or transpiles a `nativets` one to its native form, and bundles each host-provided module's copy and the request's `plugin`, writing the modules into the bundle. With `shared_code`, it also builds shared code | `BuildBun`, always; after the scan and the shims when there are host-provided modules | `host_modules` (with the workspace scripts each copy inlined); `entries`: each script's `meta`; `plugin`; `closures` and `closure_meta` |
| `parse` | Records what each entry's source holds, for codebase scripts, and checks the source of each host-provided module that esbuild bundles. It installs and bundles nothing, and writes only its response | The esbuild builds of codebase scripts and of host-provided modules (outside the folder, or listed codebase scripts) | `entries`; `host_modules` with `packages` |
| `native` | Writes the native form of a `//native` codebase script's esbuild bundle | The esbuild build of codebase scripts | `entries` |
| `kept` | Parses closure bundles for the modules that keep an `import()` | The Go build of codebase shared code | `kept` by closure, and `builtins` |
| `namespaces` | Parses module files for the namespace objects that they take | The Go build of codebase shared code | `namespaces` by file (`null` for one that does not parse or read) |
| `lazy` | Parses module files for the specifiers that they import only from inside functions | The Go build's order of host-provided modules | `lazy` by file (`null` for one that does not parse or read) |

Every request also carries the folder, the work and output directories, the
folder's scripts by Windmill path, the dependency sets, the env rewrite's
define table, support module and binding, the host-provided modules and the
entries. `require_bun` names the Bun that the helper must run on; empty, any
Bun is accepted.

Every response holds `version` (1) and `bun_version`, the Bun that ran it,
besides the fields of its request in the table. A `build` request's
host-provided module result lists, in `imports`, the workspace scripts that
the module's copy inlined.

The helper writes the response file whenever it has both paths and does not
stop on an uncaught error. That covers a request that it serves in full or
in part, and one that it refuses (an `error`, exit code 2). It writes none
when a path is missing, or when an uncaught error ends it (exit code 1)
([Exit codes and problems](#exit-codes-and-problems)).

### parse

`parse` (`Builder.parseEntry`) is for codebase scripts. For each entry, it
reports what the source records: the annotations and bundler target, the
workflow-as-code markers, and the packages that it imports. It also reports
a signature for each export of the entry's bundle, and for each top-level
function declaration that no export names.

- **Exports.** The Go build reads the export names from esbuild, and sends
  them as the entry's `exports`. Without them, the source's own exports are
  scanned.
- **Parameters.** As for any Bun script, a function that the source declares
  gets its parameters, and an export that it does not declare gets none.
- **Host-provided modules.** Each gives a `source`, the absolute path of a
  module that the Go build bundles with esbuild (one outside the folder, or a
  listed codebase script), and the `loader` that esbuild bundles it with.
  The helper reads the source with that loader's syntax (`ts`, `tsx`, `js`
  or `jsx`), and reports the packages that it imports or
  `host_module_live_binding`.
- **Other loaders.** It does not parse a file of another loader, nor read
  one without a `loader`.

### Exit codes and problems

| Exit code | When | The response |
|---|---|---|
| 0 | The request ran, in full or in part: a problem with one script or module is in its result | Each entry's `meta`, and also an `error` (with `meta.path`) when it failed; each host-provided module's results, or an `error`; or the fields of a `kept`, `namespaces` or `lazy` request |
| 1 | A `kept` request, or the shared code of a `build` request, failed | `error`, with what was built before it |
| 1 | An uncaught error in the helper | No response file is written; Bun prints the error |
| 2 | The request cannot be served at all: a request that cannot be read, another version or op, another Bun than `require_bun`, or a request that the builder refuses | `error` |
| 2 | The request or response path is missing | No response file; the usage goes to standard error |

A problem is a `kind` and a `message`:

| Kind | When |
|---|---|
| `invalid_request` | The request cannot be read, or the builder refuses it (exit code 2). For one script or module: a file that leaves the folder or cannot be read, an unknown dependency set, a missing shim or late shim, a native bundle outside the work directory, or a host source that cannot be read |
| `runtime_mismatch` | The helper runs on another Bun than `require_bun` (exit code 2) |
| `invalid_lock` | A script's lock has no `//bun.lock` or `//bun.lockb` separator; the message is Windmill's |
| `install_failed` | `bun install --frozen-lockfile` failed for a lock |
| `unsupported_feature` | An `//npm` script: windmill-lite installs with Bun only |
| `parse_failed` | A source does not parse ([The parse](#the-parse)) |
| `bundle_failed` | A workspace import that names no script, a bundle that took a file from outside its dependency set, a bundle with more than one output file, or any other `Bun.build` failure. Also no free binding name, a host package that does not resolve, a failed native form or embedded client, a failed text rewrite, or a failed closure |
| `host_module_commonjs` | A host-provided module is CommonJS |
| `host_module_live_binding` | A host-provided module's source directly exports a `let` or `var` declaration (`export let`, `export var`; not an ambient `declare`) |
| `internal` | An unexpected error in the helper, with its stack |

## Bundling each script

For each Bun script that is not a `nativets` script, the `build` request
takes these steps ([Native scripts](#native-scripts) gives the `nativets`
path):

1. **Installs its dependencies** from its lock, in the format of
   [WR-25](../../../docs/semantics/languages.md#wr-25): `package.json`,
   then `//bun.lock` and the text lock, or `//bun.lockb` and the base64
   binary one.
   - A lock part of `<empty>` installs nothing.
   - It runs `bun install --frozen-lockfile` once per distinct lock, in
     `<work>/deps/<lock digest>/`, with the caller's environment (the
     registry configuration, `BUN_INSTALL_CACHE_DIR`).
   - A lock without a separator is `invalid_lock`, with Windmill's message.
     A failed install is `install_failed`.
   - An `//npm` script is refused (`unsupported_feature`): windmill-lite
     installs with Bun only.
2. **Transforms the source**, the script's and that of every workspace
   script it imports, with Windmill's two whole-text replacements
   ([WR-V4](../../../docs/semantics/languages.md#wr-v4)):
   - **Pinned-import removal**
     ([WR-26](../../../docs/semantics/languages.md#wr-26)). Every
     specifier `pkg@version[/sub]` or `@scope/pkg@version[/sub]` that the
     parser reports is replaced, longest first, everywhere in the text, by
     its unpinned form. The parser reports static imports (type-only ones
     included), re-exports, and dynamic imports of a string literal. An
     `npm:` prefix stays.
   - **The `.ts` rewrite**
     ([WR-28](../../../docs/semantics/languages.md#wr-28)). Each static
     import that Bun's scanner reports, starting with `.`, `/u/` or `/f/`
     and not ending in `.ts`, gets `.ts` appended to every occurrence of its
     text. The rewrite goes in the scanner's order, without deduplication.
   - **The scanner** is `Bun.Transpiler().scan`, which reads a hashbang
     line, where Windmill's `scanImports` throws. What Windmill's scanner
     reports is recorded in
     [WR-52](../../../docs/semantics/languages.md#wr-52).
   - **Collisions.** Overlapping and repeated specifiers therefore collide
     as on Windmill `v1.767.0`
     ([WR-38](../../../docs/semantics/languages.md#wr-38)), which the design
     chose to reproduce
     ([policy questions](../../../docs/design/0001-equivalence-contract.md#7-policy-questions-the-reference-left-to-the-design)).
     At `v1.862.0`, Windmill replaces only whole quoted specifiers, so the
     collisions no longer arise there.
   - **Differences from Windmill's parse.** The helper also unpins dynamic
     imports of a string literal, which Windmill's parse does not collect,
     and it stops `..` segments at the root
     ([WR-52](../../../docs/semantics/languages.md#wr-52);
     [Decisions](#decisions-and-differences-from-the-reference)).
3. **Resolves workspace imports from the sync folder.** A plugin intercepts
   the relative and absolute specifiers of a workspace script.
   - A specifier ending in `.ts` names the Windmill path of
     [WR-28](../../../docs/semantics/languages.md#wr-28), relative to the
     importer's path taken as a file: the script's import base
     ([WR-29](../../../docs/semantics/languages.md#wr-29)), which the
     caller gives. Dot segments are normalised, and never go above the root.
   - The request's script map gives the file. No script there fails the
     bundle (`bundle_failed`).
   - Any other relative or absolute specifier, files prefixed `_wm_`
     included, fails the bundle too. Windmill's Bun would resolve it against
     its job directory, where no workspace script lies.
   - Bare specifiers resolve from the dependency set's `node_modules`. A
     bundle that took a file from outside the dependency set's directory (a
     `node_modules` above it, which no lock installed) fails.
   - The plugin's resolve filter matches only the specifiers that it
     resolves ([The resolve filter](#the-resolve-filter)).
4. **Bundles** with `Bun.build` and Windmill's settings
   ([WR-27](../../../docs/semantics/languages.md#wr-27)): ES module output,
   syntax-only minification, only `electron` external, and every npm package
   and workspace import inlined.
   - **The target** is `node` for `//nodejs`, else `browser` for
     `//native`, else `bun`, checked in that order
     ([WR-40](../../../docs/semantics/languages.md#wr-40)).
   - **`//nobundling` scripts** are bundled the same way, where Windmill
     resolves their imports and installs their packages at run time: a
     [named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations),
     so that a bundle runs offline.
   - **The env rewrite.** The define table and the support module come from
     the Go build ([packaging](../../../docs/design/0010-packaging.md)), and
     apply to `bun`-target bundles only. There `Bun.env`, `Bun["env"]`,
     `import.meta.env`, aliases and destructuring of `Bun` read
     `process.env`, through a global proxy that the support module defines.
   - **The support module** (`windmill-lite:env`) is imported by the entry
     before its own imports, after a hashbang line. It is bundled like any
     other module, so the bundler renames a script's own top-level `Object`,
     `process`, `globalThis` and the like around its references.
   - **The define table's binding** is the Go build's name, or that name
     with a suffix (`_2`, `_3`, …) when an input of the bundle contains it.
     An input contains it as written or through Unicode escapes
     (`__windmill_lite_B\u0075n` spells it too; `spells`). So no binding of
     a script or a dependency can capture it.
   - **`//native` and `//nodejs` bundles** are left as written. A native
     script's environment is the native boundary's, and a `//nodejs`
     script's is the Bun harness's at run time.
   - **`NODE_ENV`.** The Go build runs the helper without `NODE_ENV`, which
     `Bun.build` inlines, so every bundle reads `"development"` there.
   - **One module.** A bundle must be a single module; one that emits other
     files fails.
5. **Records** what the build found:
   - the bundle's exports (`Bun.Transpiler().scan` of the output), and the
     signature of every export and of every other top-level function
     declaration;
   - the annotations, the target and the workflow-as-code markers;
   - the workspace scripts inlined, the packages that the source imports,
     and the host-provided modules used;
   - the metafile, with paths relative to the dependency set's directory.
   - It writes `scripts/bun/<id>.mjs` into the bundle, or
     `scripts/bun/<id>.native.js` for a native script; Go writes
     `<id>.meta.json`.
   - An id is the script's inventory id: one or more `/`-separated
     segments, none empty, `.` or `..`, and none holding a backslash or NUL.
     So the path script `f/app/x` is written to `scripts/bun/f/app/x.mjs`.

Sources are written under the dependency set's directory before bundling,
so the paths that a bundle's comments name are the same on every machine:
`entry/<id>.ts`, `src/<Windmill path>.ts`, `host/<id>.mjs` and
`node_modules/…`.

## The resolve filter

The build's plugin (`windmill-lite`) has a resolve filter that matches only
the specifiers that it resolves:
relative and absolute ones, the env support module, listed packages, and
`windmill-lite:late/<key>`, by which a late shim imports its CommonJS
module.

- **Why.** Bun 1.3.10 drops a module that a package marked
  `sideEffects: false` re-exports (`export { x } from "./x.js"`), whenever a
  resolve filter matches the import of that package, even when the callback
  resolves nothing. The bundle then references a binding that it never
  defines. Bun 1.4.3 keeps that module
  ([measurements behind the decisions](../../../docs/design/README.md#measurements-behind-the-decisions)),
  and the filter is unchanged.
- **Against Windmill.** At `v1.767.0`, Windmill's import plugin matches
  every specifier that ends in `.ts`. So a bare import such as `leaf.ts` or
  `pkg/index.ts` from a package loses the re-exported module on Windmill,
  and the step fails with a `ReferenceError`.
- **windmill-lite's filter** leaves such an import to Bun, so the module
  stays and the step runs: a
  [named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations)
  ([WR-27](../../../docs/semantics/languages.md#wr-27)). Windmill `v1.862.0`,
  whose image runs Bun 1.4.4, is not checked yet.

## Shared code

A `build` request with `shared_code` also builds shared code for its `bun`
and `node` target scripts (`buildShared`), as the
ADR [Shared module code](../../../docs/design/0013-shared-module-code.md)
decides. Each script's per-script bundle is built as without
shared code. How shared code differs from a per-script bundle is a
[named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations).

### Groups

The scripts with one target and one lock, that is one dependency set, form a
group. `//native` scripts form none. A group's closure id is `bun-` and
twelve hex digits of the SHA-256 of what makes the group.

### The shared-layout module

A script's shared-layout module is a second bundle of its entry, written to
`scripts/bun/<id>.shared.mjs`:

- Each import that the entry's own bundle resolved to a module of the
  dependency set becomes a reference to that module in the script's
  instance of the closure. Such a module is a workspace script under `src/`
  or a package under `node_modules/`.
- A CommonJS module gets a CommonJS reference. An ES module gets an ES
  module reference, whose named exports stay live and whose default, when it
  has one, is read once.
- An ES module reference lists the module's export names for the script's
  target (`exportNamesOf`, from a bundle of the module alone), when all of
  them are known; otherwise it re-exports with `export *`. Its namespace
  object then has the module's names, and not the CommonJS interop marker of
  the reference that it reads them from.
- The env support module and the shims of host-provided modules stay as the
  per-script bundle has them. An import that the per-script bundle dropped
  (a module that it pruned) becomes an empty module.

### The closure

The closure is a CommonJS bundle, written to `<work>/closures/<id>.cjs`,
which Go wraps into its factory:

- Its entry requires the env support module first, for a `bun`-target
  group. It then maps each module that a script's entry imports directly,
  by its path in the dependency set's directory, to a function that
  requires the module. What those modules import is bundled into the
  closure, but is not in the table.
- The entry hands that table over through a global property
  (`CLOSURE_TABLE`), so the instance's `module` stays the step's.
- `import.meta` reads a parameter of the instance: the first of `__wl_meta`,
  `__wl_meta_2`, … that no input of the closure spells, so no binding of a
  dependency captures it. The response gives the name (`closure_meta`).

### Fallbacks

In these cases a script keeps its per-script bundle, and the response
records the reason instead of a closure:

| Reason | When |
|---|---|
| `namespace_import` | The script's entry and another module of its bundle both take the namespace object of one ES module dependency. In the shared layout, the entry's object would be its reference's, and the dependency's the closure's |
| `export_names` | The entry takes the namespace object of an ES module whose names are not all known (a star re-export of an external module), or imports a module that exports `__esModule` |
| `top_level_await` | A module that the script reaches uses top-level `await`, which the CommonJS closure cannot hold |
| `external_import` | A module that the script reaches has a static import of an external module |
| `dynamic_import` | A module that the script reaches keeps an `import()` |

For the last three, the closure is then built again without those scripts.

### Built-in modules

A module that the runtime has built in is neither an external module nor a
kept `import()`: the runtime resolves it to the same module from every
file, as the ADR Shared module code decides.

- `builtinModule` decides it from `BUILTIN_SPECIFIERS`: each name that
  `node:module`'s `builtinModules` lists on the helper's Bun, alone or after
  `node:`, that `Bun.resolveSync` resolves to a built-in module.
- Bun resolves its own modules only bare. So `node:ws` and `node:bun:sqlite`
  are external modules, while `node:fs/promises` is built in.
- An `import()` counts only with a literal specifier.

### Parsing for shared code

The build finds namespace objects (`namespaceImports`) and kept imports
(`keptImports`) by parsing with Babel, so text that only looks like an
import does not count. Two requests run the same parses for the Go build's
codebase groups:

- `namespaces` parses the modules of their bundles.
- `kept` parses their closure bundles and returns, for each, the modules
  that keep an `import()`. It also returns `BUILTIN_SPECIFIERS`, which the Go
  build applies to the imports that esbuild's metafile lists, so both builds
  accept the same spellings.

### Resolution by absolute path

Every file that the shared build writes, and every file that a closure's
entry requires, is reached by the absolute path that a plugin returns, never
by Bun's resolver.

The reason is that Bun 1.3.10 keeps directory listings across builds in one
process. On a file system with coarse timestamps, a file written in the same
tick as an earlier read of its directory can be missing from that listing,
so a relative import of it can fail to resolve. A comment of `build.ts`
records this; no measurement in the repository holds it, and Bun 1.4.3 has
not been measured for it.

## Native scripts

A `nativets` script, or a Bun-family script whose header says `//native`,
runs with the native boundary
([WR-40](../../../docs/semantics/languages.md#wr-40)): each invocation in
a fresh JavaScriptCore realm of the Bun harness, not in Windmill's V8
isolate, which is a
[named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations).
The request's `language` says `nativets`; the header says `//native`.

- **nativets** is transpiled with `Bun.Transpiler` (types stripped, nothing
  bundled, no import transform), with target `none`. Its imports stay for
  the harness to resolve. Its `packages` are empty, since nothing is
  installed for it.
- **A transpile error** is not a build problem. The metadata records the
  transpiler's diagnostic, with its position as
  `file:///eval.ts:<line>:<column>`, and the step fails when it runs
  ([WR-43](../../../docs/semantics/languages.md#wr-43)).
- **`//native`** is bundled as any other script for its target (`browser`,
  or `node` with `//nodejs`), without the env rewrite. Host-provided modules
  are inlined rather than shimmed: a native step's realm does not reach the
  process registry.
- **A `//native` codebase script**
  ([WR-50](../../../docs/semantics/languages.md#wr-50)) is bundled by
  esbuild in Go. The `native` request takes that bundle (a file under the
  work directory) and writes its native form, with the source's header
  annotations. The native runtime evaluates it as an ES module whatever its
  format, so a CommonJS bundle fails with
  `ReferenceError: module is not defined` when it is evaluated, as on
  Windmill ([WR-50](../../../docs/semantics/languages.md#wr-50)).

### The native form

Either module is then parsed as JavaScript and written as the source of one
async arrow function, `scripts/bun/<id>.native.js` (no `.mjs`):

- an arrow, so that the module's scope has no `arguments`;
- its import declarations bound on the first line from the function's
  parameter, its `export` keywords removed, and every later line kept in
  place;
- `import.meta` and `import(…)` read from the parameter;
- the module's own `this` (outside every function but arrows, and outside
  every class member) written as `(void 0)`. A nativets script's own `this`
  is written so before its transpile too, because Bun's transpiler writes it
  as `null`. A `//native` bundle keeps what Bun's bundler makes of it, as
  Windmill's bundle of it does;
- a namespace of getters returned, so live bindings stay live;
- a default export of an expression, or of an anonymous declaration, as a
  `const`;
- a parameter name that the text never spells.

The metadata's `boundary` is `native`, and its `native` part records the
kind, the parameter, the offset where the harness inserts the preamble, the
static imports in order, and the named imports that must exist (`links`).
It also records the `//useragent` and `//proxy` header annotations, read
from the leading run of `//` lines, which a blank line ends
([WR-45](../../../docs/semantics/languages.md#wr-45)). The signatures come
from the source, as for any script.

### The line map

The line map of a nativets script (`line_map` in its `native` part) is what
a source map of its transpile would give a line. The harness reads the
source line in front of an error's stack in it, as the native runtime does
through its transpiler's map
([WR-43](../../../docs/semantics/languages.md#wr-43)).

- **How.** `Bun.Transpiler` writes no map, so `lineMap` derives one. The
  source's nodes, less what the transpile leaves out, are aligned with the
  JavaScript's by Myers' difference algorithm. Each node that the two share
  maps, where it starts and at its last character, to the source's line
  there.
- **Left out** are types, declarations of types alone, ambient and abstract
  members, type-only imports and exports, enums and namespaces (which the
  transpile rewrites), and the wrappers of an expression.
- **Its form.** Triples (line, column, source line) in the JavaScript's
  order, the first of each run with one source line. A lookup gives the
  source line of the last mapping at or before a position, which that
  keeps.
- **Empty** when either text does not parse, or when they differ in more
  than 1024 places, since the alignment's memory grows with their square.

### The embedded client

When the bundle has a nativets script that transpiles and parses, the
helper bundles `windmill-client`
for the browser target as CommonJS. It refuses another version than
1.326.0. It writes the client in its native form to
`scripts/native/windmill.js`, with `windmill.meta.json`. Its namespace is
its exports object's own enumerable names, less `__esModule`, read once the
module has run.

Windmill 1.862.0's bundle of that version lacks
`WorkspaceService.editDeployTo`. So the build takes that method, with its
comment, out of the package's generated services module. It fails unless it
finds the method exactly once.

## The parse

A source that does not parse as TypeScript (an ES module), with its
decorators (class, member and parameter ones) and auto-accessors, is
`parse_failed`. That includes the deprecated `import … assert {}` form,
which the parser's version has dropped.

- **Signatures**
  ([WR-6](../../../docs/semantics/languages.md#wr-6) to
  [WR-8](../../../docs/semantics/languages.md#wr-8),
  [WR-V3](../../../docs/semantics/languages.md#wr-v3)) come from an AST of
  the source, so comments and strings never change them
  ([WR-39](../../../docs/semantics/languages.md#wr-39)). An export's
  parameters are those of the top-level function declaration of its name,
  exported or not; any other export has none.
- **Unexported declarations.** A top-level function declaration that no
  export names is recorded under its own name too, because Windmill's
  parser reads a declaration whether it is exported or not
  ([WR-7](../../../docs/semantics/languages.md#wr-7)). A job whose
  entrypoint names one that does not parse fails before it runs, and the
  harness checks the export only after the parse.
- **Parameter names.** Destructured objects are named `anon1`, `anon2`, ….
  A rest parameter or an array pattern records
  `parameter syntax unsupported: …` for that export.
- **`Date`.** A parameter is marked `date` when its annotation is `Date`, a
  local alias of it (looked up by snake-case name, recursively, wherever it
  is declared), parentheses around one, or a two-member union of one with
  `undefined` or `null`.
- **Annotations**
  ([WR-31](../../../docs/semantics/languages.md#wr-31),
  [SY-V3](../../../docs/semantics/openflow.md#sy-v3)) are the leading block
  of `//` lines. Blank lines (white space only included) are skipped, and an
  indented line ends the block.
- **An annotation's key** is the comment's text after `//`, trimmed, then
  cut at the first `=` and not trimmed again. So `//native=false` sets
  `native`, and `//native =true` does not. The Go loader reads them the same
  way (`syncfolder.HasAnnotation`, which `internal/ir` uses), and
  `conformance/build/annotations.json` holds the cases that both run.
- **Workflow-as-code**, for the default entrypoint `main`: the helper
  records three markers, and the Go build refuses a script with any of them
  (`unsupported: workflow_as_code`), because the equivalence contract puts
  workflow-as-code
  [out of scope](../../../docs/design/0001-equivalence-contract.md#3-out-of-scope).
  - `wrapper`: the worker's line scan
    ([WR-32](../../../docs/semantics/languages.md#wr-32)). It skips lines
    that start with `//`, and fires when the source imports
    `windmill-client` and holds `workflow` inside that import or on a line
    with both `export` and `workflow(`. A multi-line `import {` … `}` block
    counts as an import only when the whole block names `windmill-client`;
  - `parsed`: an exported `main = workflow(async …)` with no declaration of
    `main` ([WA-33](../../../docs/semantics/client-api.md#wa-33)). Windmill
    also counts one that is not exported, `export default workflow(…)`, and
    one with a later `function main`, which the helper does not;
  - `substring`: the parser's fallback, only when `parsed` is false and
    there is no declaration of `main`, where the text holds `workflow(`,
    `task(` and `windmill-client`. So `parsed` and `substring` are never
    both set.

## Host-provided modules

A [host-provided module](../../../docs/glossary.md#host-provided-module) is
a script of the folder or an installed package here, with one instance per
process, shared by the host and every script that imports it. Windmill
inlines every import into each script's bundle instead: a
[named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations)
that the ADR [Packaging](../../../docs/design/0010-packaging.md#host-provided-modules)
decides. A module outside the folder is bundled by the Go
build with esbuild; the helper only parses its source ([parse](#parse)).

- **Scan.** The module must be an ES module. A CommonJS module, whose names
  are its exports object's properties and are known only when it runs, is
  refused with `host_module_commonjs`.
- **No reassignable exports.** Its own source must not directly export a
  `let` or `var` declaration (`export let`, `export var`; an ambient
  `declare` does not count); otherwise it is refused with
  `host_module_live_binding`, naming the bindings.
- **Export names** are the scan of its bundled copy, with other
  host-provided modules left external, so `export *` of a local file is
  listed. The scan also reports which other host-provided modules it imports
  and which packages its source imports.
- **Build.** Every import of a listed module, by a script or by one of its
  dependencies, resolves to the shim that the Go build generated. A listed
  package's entry is the file that the bundler resolves it to for a script.
- **The copy.** Each module's bundled copy is written to
  `scripts/bun/provided/<id>.mjs`, from an entry that imports the env
  support module and re-exports the scanned names. The module's own imports
  of other listed modules resolve to their shims. The build reports the
  workspace scripts that each copy inlined, which the Go build adds to the
  bundle's sources.
- **A copy whose exports differ from its scan** (it re-exports another
  listed module with `export *`) is a problem in Go.
- **The host plugin.** The build op's `plugin`, a script of the folder, is
  bundled as a copy is, to `plugins/<id>.mjs`, with every listed module
  resolved to its shim. Its entry re-exports the source's names, its
  default export included. The build reports the listed modules that it
  imports, the packages that its source imports, and the workspace scripts
  that it inlined.
- **Late shims.** A module's request may name, in `late`, listed modules
  that it imports lazily and registers before. Its copy resolves its imports
  of each to that module's late shim, whose two sources the module's own
  request carries (`late_esm`, `late_cjs`). The resolve filter matches
  `windmill-lite:late/<key>`, the specifier by which the ES module imports
  the CommonJS one.

### What orders host-provided modules

The Go build orders the listed modules by their load-time imports only
([internal/bundle](../../../internal/bundle/README.md#registration-order-of-host-provided-modules)).
The helper reports what that order needs:

- **The import graph.** A scan of a module that imports another listed
  module also returns its bundle's imports of listed modules
  (`bundleImports`): the module that holds each, Bun's kind and the
  specifier. They come from the metafile's external imports, which the
  build's plugin resolved to listed modules.
- **Lazy imports.** `lazyImports` parses a module with Babel, and returns
  the specifiers that it imports only through `import()` calls inside a
  function, an arrow or a method body. An `import()` in a class's static
  block, a field initialiser, a computed key or a decorator runs where the
  class is defined, so it is not lazy. A module that does not parse returns
  `null`.
- **Folded specifiers.** A module with a top-level `import()` whose
  specifier is not a literal (`"./" + "a.ts"`, a template literal with a
  substitution) gets no lazy specifiers: a bundler can fold that specifier
  into any module's.
- **The `lazy` request.** The scan parses the modules of its graph that hold
  an `import()` of a listed module. For a module that esbuild bundles, the
  Go build sends each file with its syntax, and the response gives each
  file's lazy specifiers.

## Decisions and differences from the reference

The helper takes these choices on details of Windmill's build and parse.
The semantics reference records most of the details for Windmill.

- **Agrees:** the choice is what the reference records.
- **Named deviation:** the choice differs, and a
  [named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations)
  of the equivalence contract covers it.
- **Differs:** the choice differs, and no named deviation covers it yet.

The "Why" column gives the reason for the choice: for a choice that agrees,
that it follows the reference's rule; otherwise the code's or the case's
reason, or "Not recorded" where neither gives one.

| Question | The helper's choice | The reference | Why | Pinned by |
|---|---|---|---|---|
| Which lines does the wrapper scan read as an import of `windmill-client`? | Lines are trimmed and `//` lines skipped. An import is a line that starts with `import` or `from` and names `windmill-client`, or a block from an `import` line with `{` and no `}` to the first line with `}`, when the whole block names it | **Differs** on two edge cases: the helper checks an open block before the client-line rule, and its block lines skip the `export` and `workflow(` test ([WR-32](../../../docs/semantics/languages.md#wr-32)) | Not recorded | The cases `multi-line-import` of `conformance/build/wr-32.json` and `comment-lines` of `wr-39.json`; `build.test.ts` (`the wrapper scan`) |
| Of overloads, which signature gives the parameters? | The first | **Differs:** the last one wins ([WR-51](../../../docs/semantics/languages.md#wr-51)) | An overload signature comes before its implementation (`signature.ts`) | The case `overloads` of `wr-7.json` |
| Is a TypeScript `this` parameter a parameter? | No | **Differs:** Windmill lists `this`, and its wrapper then fails to parse ([WR-51](../../../docs/semantics/languages.md#wr-51)) | TypeScript erases a `this` parameter | The case `this-parameter` of `wr-7.json` |
| Is an array-destructuring parameter supported? | No, like a rest parameter | **Agrees** ([WR-51](../../../docs/semantics/languages.md#wr-51)) | Follows the reference | The case `array-pattern` of `wr-7.json` |
| Is an ambient `declare function` a declaration? | Yes | **Agrees** ([WR-51](../../../docs/semantics/languages.md#wr-51)) | Follows the reference | The case `ambient-declaration` of `wa-33.json` |
| Under which name is `export default function main` exported? | As `default` only | **Agrees:** it is not an entrypoint declaration ([WR-7](../../../docs/semantics/languages.md#wr-7)) | Follows the reference | The case `default-export` of `wr-7.json` |
| Does an exported type alias take part in `Date` resolution? | Yes, exported or not | **Agrees** ([WR-8](../../../docs/semantics/languages.md#wr-8)) | Follows the reference | The case `alias-chain` of `wr-8.json` |
| How are destructured parameters numbered? | Among themselves, one with a default included | **Agrees** ([WR-51](../../../docs/semantics/languages.md#wr-51)) | Follows the reference | The case `anon-numbering` of `wr-7.json` |
| Is a qualified `globalThis.Date` a `Date`? | No | **Differs:** a qualified type is matched by its last segment, so it is `Date` ([WR-51](../../../docs/semantics/languages.md#wr-51)) | Not recorded | The case `qualified-date` of `wr-8.json` |
| What do aliases that refer to each other resolve to? | Nothing, so never `Date` | **Agrees** that they are never `Date` ([WR-51](../../../docs/semantics/languages.md#wr-51)) | Follows the reference | The case `alias-cycle` of `wr-8.json` |
| Where do a workspace import's `..` segments stop? | At the root | **Differs:** Windmill's fetch does not stop them, and the build fails with `Failed to find relative import` ([WR-52](../../../docs/semantics/languages.md#wr-52)) | Not recorded | The case `above-root` of `wr-28.json` |
| Does the parse accept decorators and auto-accessors? | Yes, both | **Differs** on decorators, which Windmill rejects; agrees on auto-accessors ([WR-51](../../../docs/semantics/languages.md#wr-51)) | The parser accepts what Bun bundles (`signature.ts`) | The case `decorators` of `wr-39.json` |
| Does the parse accept `import … assert {}`? | No | **Differs:** Windmill accepts it ([WR-51](../../../docs/semantics/languages.md#wr-51)) | The parser's version, Babel 8, dropped the form (`signature.ts`) | Code only (`signature.ts`) |
| How are alias names compared? | In snake case. A word starts at an upper-case letter that follows a lower-case letter or a digit, and at the last capital of a run of capitals that a lower-case letter follows (`HTTPServer` is `http_server`); `-`, `_` and spaces separate words. Of two aliases with one snake-case name, the later wins | **Agrees** ([WR-51](../../../docs/semantics/languages.md#wr-51)) | Follows the reference | The case `snake-case-lookup` of `wr-8.json`; `build.test.ts` (`snake case of alias names`); the later-wins rule code only (`signature.ts`) |
| Where do the import rewrites take their specifiers? | Pin removal from the helper's TypeScript parse; the `.ts` rewrite from Bun's `scan`, with the `ts` loader | **Differs:** Windmill's pin removal parses TSX, which fails on angle-bracket type assertions, and its `.ts` rewrite uses `scanImports`, which a hashbang line breaks ([WR-52](../../../docs/semantics/languages.md#wr-52)) | `scan` accepts a hashbang line, where `scanImports` throws; no reason recorded for the TypeScript parse | The cases `hashbang` and `re-export` of `wr-28.json`; `build.test.ts` (`pin removal, longest first and everywhere`) |
| Which `NODE_ENV` does a bundle see? | None: the Go build runs the helper without it, so a bundle inlines `"development"` | **Agrees:** Windmill builds without it too ([WR-53](../../../docs/semantics/languages.md#wr-53)) | Follows the reference | `TestBuildIgnoresNodeEnv` in `internal/bundle` |
| What text has a nativets transpile error? | Bun's transpiler's, with a position in `file:///eval.ts` | **Named deviation:** nativets runs under the native boundary in Bun, so Windmill's parser diagnostic differs | The native boundary transpiles with Bun's transpiler | The case `nativets-syntax-error` of `d5.json`; `native.test.ts` |
| What does a native module's anonymous `export default function` become? | A `const`, so it is not hoisted | The reference does not cover it | Not recorded | Code only (`native.ts`) |

## Tests

The merge gate is the Go side:
[internal/bundle](../../../internal/bundle/README.md#tests) runs every case
of `conformance/build` through this helper on the pinned Bun. The Bun tests
here are developer checks.

The Bun tests need Bun 1.4.3 and the helper's dependencies; they need no
variable. From the repository root:

1. Install the helper's dependencies:
   `(cd harnesses/bun/build && bun install --frozen-lockfile)`.
2. Run the helper's Bun tests:
   `(cd harnesses/bun && bun test test/build.test.ts test/native.test.ts)`.

| Test file | What it checks |
|---|---|
| `harnesses/bun/test/build.test.ts` | The scans, the parse, the import transforms, and shared code's built-in specifiers and kept imports |
| `harnesses/bun/test/native.test.ts` | The native form, the native realm (`runtime/native.ts`), the generated errors, the preamble, the line map, stack source lines and the embedded client |

CI's `Bun harness checks` job installs the helper's dependencies and runs
`bun test` in `harnesses/bun`, which runs these files with the harness's
other tests. It is not a required check
([docs/ci.md](../../../docs/ci.md)).

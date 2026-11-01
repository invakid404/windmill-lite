# harnesses/bun/build

**Current state:** the Bun build helper of milestone C2a
([ADR 0012](../../../docs/design/0012-layout-and-milestones.md)). This README
covers `harnesses/bun/build`. The Go build in
[internal/bundle](../../../internal/bundle/README.md), behind
`windmill-lite build` (F4), runs it; nothing else does. It runs on the
pinned Bun 1.3.10 ([WR-3](../../../docs/semantics/languages.md#wr-3)) and
refuses another unless the caller allows it. Codebase scripts are not
bundled here: esbuild bundles them in Go (C2b), and the helper only parses
their sources (`parse`, below). Milestone C3n added the native form of
`nativets` and `//native` scripts and the embedded client (below, "Native
scripts"). The workspace plugin's resolve filter matches only the
specifiers it resolves (below, "The resolve filter"), and `parse` checks
the sources of the host-provided modules that the Go build bundles with
esbuild.

The helper also builds shared code
([ADR 0013](../../../docs/design/0013-shared-module-code.md); below,
"Shared code"), with the `kept` and `namespaces` requests.

## Running

```sh
cd harnesses/bun/build && bun install --frozen-lockfile   # once: the parser
bun harnesses/bun/build/main.ts <request.json> <response.json>
```

The helper has two dependencies, pinned with their own in `bun.lock`:
`@babel/parser` (MIT), which parses TypeScript for the signature parse and
JavaScript for the native form, and `windmill-client@1.326.0` (Apache-2.0),
the package the native runtime's embedded client is built from. Its
version is the one Windmill embeds
([WR-40](../../../docs/semantics/languages.md#wr-40)); Renovate holds it
there.
`node_modules/` is not committed.

The request and response types are in `protocol.ts`. A request is `scan`,
`build` or `parse`; the Go side sends a scan first when the bundle has
host-provided modules, writes their shims, then sends the build.

`parse` (`Builder.parseEntry`) is for codebase scripts. For each entry it
reports what its source records (annotations and bundler target,
workflow-as-code markers, the packages it imports) and a signature for
each export of its bundle. The Go build reads the export names from
esbuild and sends them as the entry's `exports`; without them, the
source's own exports are scanned.

As for any Bun script, a function the source declares gets its parameters
and an export it does not declare gets none. A `parse` request installs,
bundles and writes nothing.

A `parse` request's host-provided modules each give a `source`, the
absolute path of a module that the Go build bundles with esbuild (one
outside the folder, or a listed codebase script), and the `loader` esbuild
bundles it with. The helper reads the source with that loader's syntax
(`ts`, `tsx`, `js` or `jsx`) and reports the packages it imports, or
`host_module_live_binding`. It does not parse a file of another loader,
nor read one without a `loader`, which has none.

A request it cannot serve at all (unreadable, another Bun) exits with code
2 and an `error` in the response. A problem with one script or module is
reported against it, and the rest are still built.

Shared code adds two more requests, `kept` and `namespaces` (below,
"Shared code"). The order of host-provided modules adds `lazy` (below,
"Host-provided modules").

## What it does

| File | Contents |
|---|---|
| `main.ts` | Reads the request, checks the Bun version, runs the scan, the build or the parse, writes the response |
| `build.ts` | Dependency installs, the import transforms and resolution, `Bun.build`, host-provided modules, the metafile |
| `imports.ts` | Pinned-import removal, the `.ts` rewrite, workspace paths, package names |
| `signature.ts` | The AST parse: signatures, `Date` marking, import specifiers, live bindings |
| `annotations.ts` | Leading-comment annotations and the bundler target they select |
| `wac.ts` | Workflow-as-code detection |
| `native.ts` | The native form of a module, the nativets transpile, the `//useragent` and `//proxy` header, the embedded client |
| `text.ts` | Rust's line and whitespace rules, for the textual scans |
| `shared.ts` | Shared code: the support module, dependency references and closure entry, the closure id, the runtime's built-in modules, the parse for kept `import()` |
| `loadorder.ts` | What orders host-provided modules: the parse of where each `import()` sits (`lazyImports`), and the import graph of a bundle (`bundleImports`) |
| `protocol.ts` | The request and response |

### Bundling each script

For each script the build:

1. **Installs its dependencies** from its lock, in the format of
   [WR-25](../../../docs/semantics/languages.md#wr-25): `package.json`, then
   `//bun.lock` and the text lock or `//bun.lockb` and the base64 binary one.
   A lock part of `<empty>` installs nothing. It runs
   `bun install --frozen-lockfile` once per distinct lock, in
   `<work>/deps/<lock digest>/`, with the caller's environment (registry
   configuration, `BUN_INSTALL_CACHE_DIR`). A lock without a separator is
   `invalid_lock`, with Windmill's message; a failed install is
   `install_failed`. An `//npm` script is refused (`unsupported_feature`):
   windmill-lite installs with Bun only.
2. **Transforms the source**, the script's and every workspace script it
   imports, with Windmill's two whole-text replacements
   ([WR-V4](../../../docs/semantics/languages.md#wr-v4)):
   - pinned-import removal ([WR-26](../../../docs/semantics/languages.md#wr-26)):
     every specifier `pkg@version[/sub]` or `@scope/pkg@version[/sub]` that the
     parser reports (static imports, type-only ones included; re-exports;
     dynamic imports of a string literal) is replaced, longest first,
     everywhere in the text by its unpinned form; an `npm:` prefix stays;
   - the `.ts` rewrite ([WR-28](../../../docs/semantics/languages.md#wr-28)):
     each static import that Bun's scanner (`Bun.Transpiler().scan`, which
     reads a hashbang line, where `scanImports` throws) reports, starting
     with `.`, `/u/` or `/f/` and not ending in `.ts`, gets `.ts` appended to
     every occurrence of its text, in the scanner's order and without
     deduplication. Bun 1.3.10's scanner lists `export … from` as a static
     import, keeps source order, does not deduplicate and omits type-only
     imports. Overlapping and repeated specifiers therefore collide as on
     Windmill ([WR-38](../../../docs/semantics/languages.md#wr-38)), which the
     design chose to reproduce (ADR 0001 §7, Q-WR-17).
3. **Resolves workspace imports from the sync folder.** A plugin intercepts
   relative and absolute specifiers of a workspace script: one ending in
   `.ts` names the Windmill path of
   [WR-28](../../../docs/semantics/languages.md#wr-28), relative to the
   importer's path taken as a file (the script's import base,
   [WR-29](../../../docs/semantics/languages.md#wr-29), which the caller
   gives), with dot segments normalised and never above the root. The
   request's script map gives the file; no script there, or any other
   relative or absolute specifier (which Windmill's Bun would resolve against
   its job directory, where no workspace script lies; files prefixed `_wm_`
   included), fails the bundle (`bundle_failed`). Bare specifiers resolve
   from the dependency set's `node_modules`; a bundle that took a file from
   outside the dependency set's directory (a `node_modules` above it, which
   no lock installed) fails. The plugin's resolve filter matches only the
   specifiers it resolves (below, "The resolve filter").
4. **Bundles** with `Bun.build` and Windmill's settings
   ([WR-27](../../../docs/semantics/languages.md#wr-27)): ESM, syntax-only
   minification, only `electron` external, every npm package and workspace
   import inlined. The target is `bun`, or `browser` for `//native` and
   `node` for `//nodejs`, checked in that order
   ([WR-40](../../../docs/semantics/languages.md#wr-40)). `//nobundling`
   scripts are bundled the same way (D7). The define table and support
   module of the env rewrite come from the Go build (ADR 0010 step 5) and
   apply to Bun-target bundles only: there `Bun.env`, `Bun["env"]`,
   `import.meta.env`, aliases and destructuring of `Bun` read `process.env`,
   through a global proxy that the support module defines. The entry
   imports the support module (`windmill-lite:env`) before its own imports,
   after a hashbang line; the module is bundled like any other, so the
   bundler renames a script's own top-level `Object`, `process`,
   `globalThis` and the like around its references. The define table's
   binding is the Go build's name, or that name with a suffix (`_2`, `_3`,
   …) when an input of the bundle contains it, as written or through
   Unicode escapes (`__windmill_lite_B\u0075n` spells it too; `spells`), so
   no binding of a script or a dependency can capture it. `//native` and `//nodejs` bundles are left
   as written; their environment is the native boundary's (C3n). The Go build runs the helper without
   `NODE_ENV`, which `Bun.build` inlines, so every bundle reads
   `"development"` there. A bundle must be a single module; one that emits
   other files fails.
5. **Records** the bundle's exports (`Bun.Transpiler().scan` of the output),
   the signature of every export and of every other top-level function
   declaration, the annotations, the target, the
   workflow-as-code markers, the workspace scripts inlined, the packages the
   source imports, the host-provided modules used, and the metafile with
   paths relative to the dependency set's directory. It writes
   `scripts/bun/<id>.mjs` into the bundle; Go writes `<id>.meta.json`. An
   id is the script's inventory id: one or more `/`-separated segments,
   none empty, `.` or `..`, and none holding a backslash or NUL, so the
   path script `f/app/x` is written to `scripts/bun/f/app/x.mjs`.

Sources are written under the dependency set's directory before bundling,
so the paths a bundle's comments name are the same on every machine:
`entry/<id>.ts`, `src/<Windmill path>.ts`, `host/<id>.mjs` and
`node_modules/…`.

### The resolve filter

The plugin's resolve filter matches only the specifiers it resolves:
relative and absolute ones, the env support module and listed packages.
Bun 1.3.10 drops a module that a package marked `sideEffects: false`
re-exports (`export { x } from "./x.js"`) whenever a resolve filter
matches the import of that package, even when the callback resolves
nothing. The bundle then references a binding that it never defines.

Windmill's import plugin matches every specifier that ends in `.ts`. So a
bare import such as `leaf.ts` or `pkg/index.ts` from a package loses the
re-exported module on Windmill, and the step fails with a
`ReferenceError`. windmill-lite's filter leaves such an import to Bun, so
the module stays and the step runs (deviation D19,
[WR-27](../../../docs/semantics/languages.md#wr-27)).

### Shared code

A `build` request with `shared_code` also builds shared code for its `bun`
and `node` target scripts (`buildShared`). Shared code is the decision of
[ADR 0013](../../../docs/design/0013-shared-module-code.md); its
differences from a per-script bundle are deviation [D18](../../../docs/design/0001-equivalence-contract.md#5-named-deviations). Each script's
per-script bundle is built as without shared code.

**Groups.** The scripts with one target and one lock, that is one
dependency set, form a group. `//native` scripts form none. A group's
closure id is `bun-` and twelve hex digits of the SHA-256 of what makes the
group.

**The shared-layout module** of a script is a second bundle of its entry,
written to `scripts/bun/<id>.shared.mjs`:

- Each import that the entry's own bundle resolved to a module of the
  dependency set becomes a reference to that module in the script's
  instance of the closure. Such a module is a workspace script under `src/`
  or a package under `node_modules/`.
- A CommonJS module gets a CommonJS reference. An ES module gets an ES
  module reference, whose named exports stay live and whose default, when
  it has one, is read once.
- An ES module reference lists the module's export names for the script's
  target (`exportNamesOf`, from a bundle of the module alone). Its
  namespace object then has the module's names, and not the CommonJS
  interop marker of the reference that it reads them from.
- The env support module and the shims of host-provided modules stay as
  the per-script bundle has them. An import that the per-script bundle
  dropped (a module it pruned) becomes an empty module.

**The closure** is a CommonJS bundle, written to
`<work>/closures/<id>.cjs`, which Go wraps into its factory:

- Its entry requires the env support module first. It then maps each
  module that the group's scripts reach, by its path in the dependency
  set's directory, to a function that requires the module.
- The entry hands that table over through a global property
  (`CLOSURE_TABLE`), so the instance's `module` stays the step's.
- `import.meta` reads a parameter of the instance: the first of
  `__wl_meta`, `__wl_meta_2`, … that no input of the closure spells, so no
  binding of a dependency captures it. The response gives the name
  (`closure_meta`).

**Fallbacks.** In these cases a script keeps its per-script bundle, and the
response records the reason instead of a closure:

- `namespace_import`: the script's entry and another module of its bundle
  both take the namespace object of one ES module dependency. In the shared
  layout the entry's object would be its reference's and the dependency's
  the closure's.
- `export_names`: the entry takes the namespace object of an ES module
  whose names are not all known (a star re-export of an external module),
  or imports a module that exports `__esModule`.
- `top_level_await`, `external_import` (a static import of an external
  module) and `dynamic_import` (a kept `import()`): a module that the
  script reaches cannot be held by the CommonJS closure. The closure is
  then built again without those scripts.

A module the runtime has built in is neither an external module nor a kept
`import()`: the runtime resolves it to the same module from every file
([ADR 0013](../../../docs/design/0013-shared-module-code.md), decision item
5). `builtinModule` decides it from `BUILTIN_SPECIFIERS`: each name that
`node:module`'s `builtinModules` lists on the helper's Bun, alone or after
`node:`, that `Bun.resolveSync` resolves to a built-in module. Bun 1.3.10
resolves its own modules only bare: `node:ws` and `node:bun:sqlite`, for
example, are external modules, while `node:fs/promises` is built in. An
`import()` counts only with a literal specifier.

**Parsing.** The build finds namespace objects (`namespaceImports`) and
kept imports (`keptImports`) by parsing with Babel, so text that only looks
like an import does not count. Two requests run the same parses for the Go
build's codebase groups:

- `namespaces` parses the modules of their bundles.
- `kept` parses their closure bundles and returns, for each, the modules
  that keep an `import()`, and returns `BUILTIN_SPECIFIERS`, which the Go
  build applies to the imports that esbuild's metafile lists, so both
  builds accept the same spellings.

**Resolution.** Every file that the shared build writes, and every file
that a closure's entry requires, is reached by the absolute path that a
plugin returns, never by Bun's resolver. Bun 1.3.10 keeps directory
listings across builds in one process. On a file system with coarse
timestamps, a file written in the same tick as an earlier read of its
directory can be missing from that listing, so a relative import of it can
fail to resolve.

### Native scripts

A nativets script, or a Bun-family one whose header says `//native`, runs
with the native boundary (deviation D5;
[WR-40](../../../docs/semantics/languages.md#wr-40)). The request's
`language` says nativets; the header says `//native`.

- **nativets** is transpiled with `Bun.Transpiler` (types stripped,
  nothing bundled, no import transform), with target `none`. Its imports
  stay for the harness to resolve. Text the transpiler rejects is not a
  build problem: the metadata records its diagnostic, with its position as
  `file:///eval.ts:<line>:<column>`, and the step fails when it runs
  ([WR-43](../../../docs/semantics/languages.md#wr-43)). Its `packages` are
  empty, since nothing is installed for it.
- **`//native`** is bundled as any other script for its target (browser,
  or node with `//nodejs`), without the env rewrite, and with host-provided
  modules inlined rather than shimmed: a native step's realm does not reach
  the process registry.
- **The native form.** Either module is then parsed as JavaScript and
  written as the source of one async arrow function,
  `scripts/bun/<id>.native.js` (no `.mjs`): an arrow, so that the module's
  scope has no `arguments`; its import declarations bound on the first line
  from the function's parameter, its `export` keywords removed and every
  later line kept in place, `import.meta` and `import(…)` read from the
  parameter, the module's own `this` (outside every function but arrows,
  and every class member) written as `(void 0)`, and a namespace of getters
  returned, so live bindings stay live. A nativets script's own `this` is
  written so before its transpile too, because Bun's transpiler writes it
  as `null`; a `//native` bundle keeps what Bun's bundler makes of it, as
  Windmill's bundle of it does. A default
  export of an expression, or of an anonymous declaration, becomes a
  `const`. The parameter's name is one the text never spells. The metadata's
  `boundary` is `native`, and its `native` part records the kind, the
  parameter, the offset where the harness inserts the preamble, the static
  imports in order, the named imports that must exist (`links`), and the
  `//useragent` and `//proxy` header annotations, read from the leading
  run of `//` lines, which a blank line ends
  ([WR-45](../../../docs/semantics/languages.md#wr-45)). The signatures
  come from the source, as for any script.
- **A `//native` codebase script** (WR-50) is bundled by esbuild in Go; the
  `native` op takes that bundle (a file under the work directory) and
  writes its native form, with the source's header annotations. The native
  runtime evaluates it as an ES module whatever its format, so a CommonJS
  bundle fails on its first use of `module`, `exports` or `require`, as on
  Windmill.
- **The embedded client.** When the bundle has a nativets script, the
  helper bundles `windmill-client` (it refuses another version than
  1.326.0) for the browser target as CommonJS, and writes it in its native
  form to `scripts/native/windmill.js`, with `windmill.meta.json`; its
  namespace is its exports object's own names.

### The parse

A source that does not parse as TypeScript (an ES module), with its
decorators (class, member and parameter ones) and auto-accessors, is
`parse_failed`. That includes the deprecated `import … assert {}` form,
which the parser's version has dropped.

- **Signatures** ([WR-6](../../../docs/semantics/languages.md#wr-6) to
  [WR-8](../../../docs/semantics/languages.md#wr-8),
  [WR-V3](../../../docs/semantics/languages.md#wr-v3)) come from an AST of
  the source, so comments and strings never change them
  ([WR-39](../../../docs/semantics/languages.md#wr-39)). An export's
  parameters are those of the top-level function declaration of its name,
  exported or not; any other export has none. A top-level function
  declaration that no export names is recorded under its own name too,
  because Windmill's parser reads a declaration whether it is exported or
  not ([WR-7](../../../docs/semantics/languages.md#wr-7)): a job whose
  entrypoint names one that does not parse fails before it runs, and the
  harness checks the export only after the parse. Destructured objects are named
  `anon1`, `anon2`, …; a rest parameter or an array pattern records
  `parameter syntax unsupported: …` for that export. A parameter is marked
  `date` when its annotation is `Date`, a local alias of it (looked up by
  snake-case name, recursively, wherever declared), parentheses around one,
  or a two-member union of one with `undefined` or `null`.
- **Annotations** ([WR-31](../../../docs/semantics/languages.md#wr-31),
  [SY-V3](../../../docs/semantics/openflow.md#sy-v3)) are the leading block
  of `//` lines: blank lines (white space only included) skipped, an
  indented line ends the block. A comment's key is its text after `//`,
  trimmed, then cut at the first `=` and not trimmed again, so
  `//native=false` sets `native` and `//native =true` does not. The Go
  loader reads them the same way (`syncfolder.HasAnnotation`, which
  `internal/ir` uses); `conformance/build/annotations.json` holds the cases
  both run.
- **Workflow-as-code**, for the default entrypoint `main`, three markers the
  loader refuses (ADR 0001 §3): `wrapper`, the worker's line scan
  ([WR-32](../../../docs/semantics/languages.md#wr-32)); `parsed`, an
  exported `main = workflow(async …)` with no declaration of `main`
  ([WA-33](../../../docs/semantics/client-api.md#wa-33)); `substring`, the
  parser's fallback with no declaration of `main`: the text holds
  `workflow(`, `task(` and `windmill-client`.

### Host-provided modules

A host-provided module ([ADR 0010](../../../docs/design/0010-packaging.md),
D12) is a script of the folder or an installed package here. One outside
the folder is bundled by the Go build with esbuild; the helper only parses
its source (`parse`, above).

- **Scan.** The module must be an ES module: a CommonJS one, whose names
  are its exports object's properties and are known only when it runs, is
  refused with `host_module_commonjs`. Its own source must not declare
  `export let` or `export var`; otherwise it is refused with
  `host_module_live_binding`, naming the bindings. Its export names are the scan of its bundled copy,
  with other host-provided modules left external, so `export *` of a local
  file is listed. The scan also reports which other host-provided modules it
  imports and which packages its source imports.
- **Build.** Every import of a listed module, by a script or by one of its
  dependencies, resolves to the shim the Go build generated. A listed
  package's entry is the file the bundler resolves it to for a script. Each
  module's bundled copy is written to `scripts/bun/provided/<id>.mjs`, from
  an entry that imports the env support module and re-exports the scanned
  names, with the module's own imports of other listed modules resolved to
  their shims. The build reports the workspace scripts each copy inlined,
  which the Go build adds to the bundle's sources. A copy whose
  exports differ from its scan (it re-exports another listed module with
  `export *`) is a problem in Go.
- **Late shims.** A module's request may name, in `late`, listed modules
  that it imports lazily and registers before. Its copy resolves its
  imports of each to that module's late shim, whose two sources the
  module's own request carries (`late_esm`, `late_cjs`). The resolve filter
  matches `windmill-lite:late/<key>`, the specifier by which the ES module
  imports the CommonJS one.

The Go build orders the listed modules by their load-time imports only
([internal/bundle](../../../internal/bundle/README.md#registration-order-of-host-provided-modules)).
The helper reports what that order needs:

- **The import graph.** A scan of a module that imports another listed
  module also returns its bundle's graph (`bundleImports`): every import of
  every module, with Bun's kind and the specifier, and the module or listed
  module it resolves to. That comes from the metafile and from what the
  build's plugin resolved, by importer.
- **Lazy imports.** `lazyImports` parses a module with Babel and returns
  the specifiers that it imports only through `import()` calls inside a
  function, an arrow or a method body. An `import()` in a class's static
  block, a field initialiser, a computed key or a decorator runs where the
  class is defined, so it is not lazy. A module that does not parse
  returns null.
- **`lazy` request.** The scan parses the modules of its graph that hold an
  `import()`. For a module that esbuild bundles, the Go build sends each
  file with its syntax, and the response gives each file's lazy
  specifiers.

## Readings of the reference

Where the reference leaves a detail open, the build takes the reading below
and the case that pins it carries provenance `policy` or a note
(`conformance/build`). Each is an open question for the semantics team.

- The wrapper scan trims each line; an import is a line starting with
  `import` or `from` that names `windmill-client`, or a block from an
  `import` line with `{` and no `}` to the first line holding `}`.
- An array-destructuring parameter is unsupported; a TypeScript `this`
  parameter is not a parameter; of overloads, the first signature counts; an
  ambient `declare function` is a declaration; `export default function main`
  is exported as `default` only; exported type aliases count. Destructured
  parameters are numbered among themselves, one with a default included.
- A qualified `globalThis.Date` is not `Date`; aliases that refer to each
  other resolve to nothing.
- Dot segments of a workspace import stop at the root.
- The parse accepts decorators and auto-accessors.
- Alias names compare in snake case, with a word boundary before an upper-case
  letter that follows a lower-case letter or digit, or that starts a word
  after an acronym; of two aliases with one snake-case name, the later wins.
- Pin removal takes its specifiers from the parse; the `.ts` rewrite from
  Bun's `scan` (not `scanImports`), with the `ts` loader.
- A bundle is built without `NODE_ENV`, so it inlines `"development"`.
- A nativets transpile error's text is Bun's transpiler's, with a position
  in `file:///eval.ts`; Windmill's parser diagnostic differs.
- An anonymous `export default function` of a native module becomes a
  `const`, so it is not hoisted (Bun's transpiler names one, so only a
  hand-written native module meets this).

## Tests

The merge gate is the Go side:
[internal/bundle](../../../internal/bundle/README.md) runs every case of
`conformance/build` through this helper on the pinned Bun.
`harnesses/bun/test/build.test.ts` has developer checks of the scans and the
parse, and `harnesses/bun/test/native.test.ts` of the native form, run by
`bun test` in `harnesses/bun`.

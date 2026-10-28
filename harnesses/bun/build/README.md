# harnesses/bun/build

**Current state:** the Bun build helper of milestone C2a
([ADR 0012](../../../docs/design/0012-layout-and-milestones.md)). This README
covers `harnesses/bun/build`. The Go build in
[internal/bundle](../../../internal/bundle/README.md) runs it; nothing else
does yet, and no `windmill-lite build` command exists (F4). It runs on the
pinned Bun 1.3.10 ([WR-3](../../../docs/semantics/languages.md#wr-3)) and
refuses another unless the caller allows it. Codebase scripts are not
bundled here: they go through esbuild in Go (C2b).

## Running

```sh
cd harnesses/bun/build && bun install --frozen-lockfile   # once: the parser
bun harnesses/bun/build/main.ts <request.json> <response.json>
```

The helper has one dependency, `@babel/parser` (MIT), pinned with its own
dependencies in `bun.lock`; it parses TypeScript for the signature parse.
`node_modules/` is not committed.

The request and response types are in `protocol.ts`. A request is `scan` or
`build`; the Go side sends a scan first when the bundle has host-provided
modules, writes their shims, then sends the build. A request it cannot serve
at all (unreadable, another Bun) exits with code 2 and an `error` in the
response. A problem with one script or module is reported against it, and
the rest are still built.

## What it does

| File | Contents |
|---|---|
| `main.ts` | Reads the request, checks the Bun version, runs the scan or the build, writes the response |
| `build.ts` | Dependency installs, the import transforms and resolution, `Bun.build`, host-provided modules, the metafile |
| `imports.ts` | Pinned-import removal, the `.ts` rewrite, workspace paths, package names |
| `signature.ts` | The AST parse: signatures, `Date` marking, import specifiers, live bindings |
| `annotations.ts` | Leading-comment annotations and the bundler target they select |
| `wac.ts` | Workflow-as-code detection |
| `text.ts` | Rust's line and whitespace rules, for the textual scans |
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
   no lock installed) fails.
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
   …) when an input of the bundle contains it, so no binding of a script or
   a dependency can capture it. `//native` and `//nodejs` bundles are left
   as written; their environment is the native boundary's (C3n). The Go build runs the helper without
   `NODE_ENV`, which `Bun.build` inlines, so every bundle reads
   `"development"` there. A bundle must be a single module; one that emits
   other files fails.
5. **Records** the bundle's exports (`Bun.Transpiler().scan` of the output),
   the signature of every export, the annotations, the target, the
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
  exported or not; any other export has none. Destructured objects are named
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
D12) is a script of the folder or an installed package.

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

## Tests

The merge gate is the Go side:
[internal/bundle](../../../internal/bundle/README.md) runs every case of
`conformance/build` through this helper on the pinned Bun.
`harnesses/bun/test/build.test.ts` has developer checks of the scans and the
parse, run by `bun test` in `harnesses/bun`.

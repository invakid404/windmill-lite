# internal/ir

**Current state:** implemented (milestone A5 of
[0012](../../docs/design/0012-layout-and-milestones.md)); `ir.Compile` has
two callers: the build of [internal/bundle](../bundle/README.md),
`bundle.Build`, and the flow runner of `wltest check`
([cmd/wltest](../../cmd/wltest/README.md)), which compiles each target flow
as its only root. This README covers `internal/ir/` and its `testdata/`. The
package is the compiler of [0002](../../docs/design/0002-ir-and-loader.md):
`ir.Compile` turns a sync folder, read by
[internal/syncfolder](../syncfolder/README.md), and a host contract into the
program the engine runs, with its script inventory. It applies once what a
successful deploy would have applied, so that the engine receives a fully
decided program, and it validates what the contract's roots reach.

What it does not do, and which milestone does:

- **Expressions:** an expression holds its site and its text as written.
  B2a's textual pipeline, `expr.Compile` (trimming, shortcuts, rewrites,
  mode, bound names, the engine source; JE-5 to JE-15), computes the rest
  of [`expr.Expr`](../expr/README.md), but `compileExpr` still fills only
  `Site` and `Text`: filling the rest from `expr.Compile` is still to do.
- **The build:** signatures, the relative-import closure, workflow-as-code
  detection and the refusal of a bundled script that imports a bare
  package with no lock need the Bun helper's scans (C2a), and bundling is
  F4's and C2b's. The inventory records what they start from: the code,
  the lock, the import base, the bundling mode and the codebase entry.
- **Run time:** a provided site's provider must be registered when
  `wl.Open` runs (`missing_provider`), and a declining one latches
  `provider_declined`; both are F2a's.

## API

| Name | What it does |
|---|---|
| `Compile(folder, contract, opts)` | Walks the closure of the contract's roots, normalises, validates and returns the `*Program`, or every error found, joined. With a nil contract every flow and script the folder lists is a root. |
| `Options` | `Strict`: refuse an absent for-loop `skip_failures` or branchall branch `skip_failure` instead of defaulting it ([Q-CF-13](../../docs/semantics/control-flow.md#q-cf-13)). |
| `ParseContract(src)`, `(*Contract).Validate` | Read and check a host contract (below). |
| `Program`, `Flow`, `Module`, `Leaf`, `Subflow`, `ForLoop`, `WhileLoop`, `BranchOne`, `BranchAll`, `Provided`, `Transform`, `Stop`, `Retry`, `Mock` | The IR, a tree of plain values. |
| `Script` | One entry of the script inventory. |
| `(*Program).MarshalIndent` | The JSON form, `ir.json` ([0010](../../docs/design/0010-packaging.md)); it decodes back to the same program. |
| `*Error` | Every failure: `Kind`, the flow and step, the script, or the provided site, a message, and the wrapped `*syncfolder.Error` of a load failure. |

## Normalising to the deployed form

windmill-lite matches a successfully deployed flow, run as the lite form
([0001](../../docs/design/0001-equivalence-contract.md) §1). The compiler
applies, once:

- **container defaults** ([OF-6](../../docs/semantics/openflow.md#of-6),
  [OF-7](../../docs/semantics/openflow.md#of-7),
  [CF-6](../../docs/semantics/control-flow.md#cf-6)): an absent for-loop
  `skip_failures` and branchall branch `skip_failure` are `true`, a
  while-loop's `skip_failures` is `false`, branchall `parallel` is `true`;
  under `Strict` the first two are refused instead (`absent_skip_flag`);
- **the language a deploy records** for a Bun-family rawscript: `bunnative`
  when its header carries the `native` annotation, `bun` otherwise, in
  either direction ([SY-V3](../../docs/semantics/openflow.md#sy-v3),
  [OF-15](../../docs/semantics/openflow.md#of-15),
  [WR-40](../../docs/semantics/languages.md#wr-40)); path scripts get it
  from syncfolder;
- **the virtual `<id>-v` identity** after a module list whose last module
  has a `sleep`, at every level, with Windmill's summary
  ([OF-17](../../docs/semantics/openflow.md#of-17),
  [FX-4](../../docs/semantics/execution.md#fx-4)). The failure and
  preprocessor modules sit in slots, not lists, and get none;
- **dropped fields:** while-loop `parallel` and `parallelism`
  ([OF-8](../../docs/semantics/openflow.md#of-8)), `delete_after_use` and
  `delete_after_secs` ([OF-12](../../docs/semantics/openflow.md#of-12)),
  queue and placement fields (`priority`, concurrency, debouncing, `tag`,
  `preserve_step_tags`), and a `script` or `flow` value's own
  `pass_flow_input_directly`, which only the envelope's triggers
  ([OF-V4](../../docs/semantics/openflow.md#of-v4));
- **locks** are kept exactly as the folder gives them, never generated or
  relocked; an empty or absent lock is not an error by itself
  ([OF-15](../../docs/semantics/openflow.md#of-15),
  [OF-V6](../../docs/semantics/openflow.md#of-v6),
  [Q-OF-5](../../docs/semantics/openflow.md#q-of-5)).

## The program

`Program.Flows` holds every flow the roots reach, by path, and
`Program.Scripts` the inventory. A `Module` has its id, its kind, its
envelope already decoded (`skip_if`, `stop_after_if`,
`stop_after_all_iters_if`, `sleep`, `timeout`, `retry`, `continue_on_error`,
`mock`, `cache_ttl`, `pass_flow_input_directly`, …) and one kind field:

| Kind | Field | Holds |
|---|---|---|
| `script`, `rawscript` | `Leaf` | the inventory id, the harness class and boundary, the input transforms |
| `flow` | `Subflow` | the flow's path (compiled into `Program.Flows`) and the input transforms |
| `forloopflow`, `whileloopflow` | `ForLoop`, `WhileLoop` | the body, the skip flag, `parallel` and `parallelism` (for loops), `squash`, and `collapse` |
| `branchone`, `branchall` | `BranchOne`, `BranchAll` | the branches (predicates for branchone, skip flags for branchall), the default, `parallel` |
| `identity` | none | `virtual` marks the `<id>-v` step |
| `provided` | `Provided` | the contract's site index, the static value or provider name, the original target and its input transforms |

**Occurrences.** Windmill does not check ids
([OF-2](../../docs/semantics/openflow.md#of-2)). A module whose id occurs
more than once in its module list records its occurrence, counted from 0 in
list order over the final list, virtual steps included. Site paths carry it
as `m:<id>~<k>` ([0011](../../docs/design/0011-differential-testing.md)).

**Transforms** are `static` (the JSON text verbatim,
[JE-40](../../docs/semantics/expressions.md#je-40)), `javascript` (an
expression with its site kind) or `ai`, which the engine leaves out of a
step's arguments ([FX-7](../../docs/semantics/execution.md#fx-7)). The input
transforms of the failure module itself have the site
`failure_input_transform` ([JE-35](../../docs/semantics/expressions.md#je-35)).

**Collapse** ([FX-46](../../docs/semantics/execution.md#fx-46),
[CF-8](../../docs/semantics/control-flow.md#cf-8),
[CF-V5](../../docs/semantics/control-flow.md#cf-v5)) is decided once, by one
function, `collapses`. In the lite form nested bodies are stored nodes, so
the deployed rule holds at every depth: a loop collapses when it is a
sequential for loop or any while loop, its body as written is exactly one
`script` or `rawscript` step with none of `sleep`, `suspend`, `cache_ttl`,
`retry`, `stop_after_if`, `stop_after_all_iters_if`, `skip_if` or an enabled
`mock`, and its flow has no failure module. A body module that is a
provided site keeps its original kind for the rule.

## Harness classes

| Language | Class | Boundary |
|---|---|---|
| `bun` | `bun` | `bun` |
| `bunnative`, `nativets` | `bun` | `native` (deviation D5) |
| `python3`, `deno`, `go`, `bash` | one class each, of the later harnesses (L1) | their own |
| anything else | none | |

Only the `bun` class has a harness in this release, so a leaf of any other
language fails with `unsupported_language: <lang>`, naming the step, or
the script for a root script.

## The script inventory

Every path script and inline script the roots reach has one `Script` entry:

| Field | Holds |
|---|---|
| `ID` | a path script's path; for an inline step `inline-` and 16 hex digits of the SHA-256 of its flow path and site, which is unique, file-name safe and stable while the step stays where it is |
| `Path` | the Windmill path: the script's own, or `<flow path>/<module id>` for an inline step |
| `Flow`, `Module`, `Site` | where an inline step is: its site path within the flow, iteration segments left out (`m:l/m:b`, `m:o/b:default/m:c`, `s:failure`) |
| `File`, `LockFile` | the files the code and lock were read from |
| `Language`, `Harness`, `Boundary` | as above |
| `Content`, `ContentDigest` | the code (not in the JSON form) and its SHA-256 |
| `Lock` | the lock text, byte for byte, or null |
| `Bundling` | `bundled`; `nobundling` for a `//nobundling` script, which the build bundles all the same (D7); `codebase`; `transpiled` for `nativets` ([WR-27](../../docs/semantics/languages.md#wr-27), [WR-40](../../docs/semantics/languages.md#wr-40)) |
| `Codebase` | the entry that selects a codebase script, and its format: `esm` only when the entry says so, otherwise `cjs` ([WR-47](../../docs/semantics/languages.md#wr-47)) |
| `ImportBase` | what relative imports resolve against, as a file ([WR-29](../../docs/semantics/languages.md#wr-29)): a path script's path; for an inline step `<flow path>/<module id>` at top level and in the failure slot, `<flow path>/preprocessor` for the preprocessor, `<flow path>/flow` in a body, or the rawscript's own `path` |
| `Annotations` | the recognised header flags of a Bun-family script ([WR-31](../../docs/semantics/languages.md#wr-31)) |
| `Schema`, `Metadata` | a path script's `.script.yaml` schema, and its `envs`, `timeout`, `cache_ttl` and `has_preprocessor` |
| `DependencySet` | the dependency part of the harness pool key ([0004](../../docs/design/0004-harness-protocol.md)): `bundled`, `lock:sha256:<hex>` for a nobundling script, `codebase:<index>` |

`Program.Deviations` lists the deviations the folder's content puts in
effect: D5, D6 (a derived module id), D7, D12 (the contract lists host
modules) and D13 (a CommonJS codebase script).

## The host contract

```yaml
roots:                      # the flows and scripts the host runs, and every
  - flow: f/app/root        # by-path target that steps dispatch locally
  - script: f/app/dispatched
provided:                   # the mandatory host-provided call sites
  - selector: {flow: f/app/root, module: permit, occurrence: 0}
    provider: permitCheck   # a provider the host registers at run time
    mandatory: true
  - selector: {script: f/app/audit_log}
    static: {logged: true}  # or a static value
    mandatory: true
host_modules: [f/app/scope.ts]   # shared module instances (0010, D12)
```

The file is YAML or JSON, read with the sync folder's dialect
([internal/yamlx](../yamlx/README.md)); unknown and repeated keys are
errors. A **selector** ([0008](../../docs/design/0008-host-hooks.md)) is
`{flow, module}`, which names that id in any module list of the flow, slots
and container bodies included, with an optional `occurrence`; or
`{script}`, which names every call of a script: a `script` step by its path
and an inline step by `<flow path>/<module id>`. A module id may be empty
(`module: ''`), so `Selector.Module` is a pointer and nil means no module
field. A static provider's value must be JSON text, `null` included.

1. The walk stops at a provided site: its target is not loaded, validated or
   bundled and need not be in the folder, unless another path reaches it.
   Flows are read with their `!inline` references unresolved, and a
   rawscript's content and lock files are read only when it is compiled as a
   step, so a provided rawscript's files are never read.
2. The site becomes a `provided` module that keeps its envelope and its
   input transforms, so the orchestration around it is the real one. A
   `suspend` on the site's own envelope belongs to its flow and is still
   refused.
3. Every provided site must match exactly one site. A selector that names an
   id occurring more than once in one list without its occurrence, or that
   matches two sites, fails with `ambiguous_selector`; one that matches
   nothing, two sites that select one module, and a site that is not a
   `script`, `flow` or `rawscript` step fail with `invalid_contract`.

The compile-time half of 0002's policy case is in
`conformance/ir/fx-39.json`: a root that calls a suspending subflow fails
with `unsupported: suspend` naming the subflow, compiles with the call site
provided, and fails with `ambiguous_selector` when the selector matches two
sites.

## Validation and errors

| `Kind` | When |
|---|---|
| `load` | the folder cannot supply an item the closure reaches, or it fails to read: the wrapped `*syncfolder.Error` says which (`not_in_folder`, `filtered`, `hub_path`, `invalid_flow`, …, [SY-15](../../docs/semantics/openflow.md#sy-15)); an inline step's files (`missing_inline_file`, `invalid_text`, `path_escape`) fail that step |
| `unsupported_feature` | `unsupported: <feature>` for `suspend` ([FX-39](../../docs/semantics/execution.md#fx-39)), `aiagent`, `flowscript` (the lite form's internal reference, which a folder cannot hold), a codebase entry's `customBundler`, and a `schema_validation` annotation ([0001](../../docs/design/0001-equivalence-contract.md) §3, §5a) |
| `unsupported_language` | a leaf whose language has no harness in this release |
| `pinned_hash` | a `script` step with a `hash` ([OF-14](../../docs/semantics/openflow.md#of-14)): a folder never records the hash Windmill gave its version, so no pin can be shown to be the folder's |
| `absent_skip_flag` | under `Strict`, an absent or null for-loop `skip_failures` or branchall branch `skip_failure` |
| `ambiguous_selector`, `invalid_contract` | the host contract (above) |

Errors name the step as `<flow path> step '<id>'`, or the flow, the script
or the provided site. A malformed contract fails on its own, before any
reading. Otherwise `Compile` returns every error, in walk order: the root
scripts, then each flow in the order the walk reaches it, module by module,
and last the provided sites that matched no site or more than one.

## Readings the compiler takes

A5 took each as the smallest reading consistent with the reference and the
design, which did not settle them then. The reference's revision of
2026-10-28 settles most of them: it states the first three, and it differs
from the compiler's reading of the import base, `schema_validation` and
`customBundler`. The compiler still takes the readings below.

- **Collapse in the preprocessor's bodies.** A deploy does not reduce the
  preprocessor module to stored nodes
  ([OF-3](../../docs/semantics/openflow.md#of-3)), so its bodies run as raw
  inline flows, in which a for loop never collapses
  ([CF-V5](../../docs/semantics/control-flow.md#cf-v5)); a while loop
  there still does. [CF-30](../../docs/semantics/control-flow.md#cf-30)
  now states this.
- **Collapse disqualifiers are presences.** `retry: {}` and `cache_ttl: 0`
  disqualify a body as any other value does, as
  [CF-8](../../docs/semantics/control-flow.md#cf-8) now states.
- **The virtual step** is added for a final `sleep` of any transform type,
  and not after the failure or preprocessor module, which are slots
  ([CF-30](../../docs/semantics/control-flow.md#cf-30)).
- **The import base** of an inline step with an explicit `path` is that path
  as written, at any depth. [WR-29](../../docs/semantics/languages.md#wr-29)
  now differs for module lists: a deployed flow drops the field from the
  inline scripts of its module lists and their container bodies, so the
  default base applies there; its failure and preprocessor slots stay raw
  and keep it, as the compiler does (WR-29/c).
- **`schema_validation`** is read as a header annotation key by the rule of
  [WR-31](../../docs/semantics/languages.md#wr-31).
  [WR-54](../../docs/semantics/languages.md#wr-54) now differs: the line has
  its own test, which needs exactly `// schema_validation` (for TypeScript)
  and stops at a blank line.
- **`customBundler`** counts when it is set to anything but null or `""`.
  [WR-48](../../docs/semantics/languages.md#wr-48) now differs: `false` and
  `0` mean unset as well.
- **A pinned `hash`** is refused whatever its value (above). On Windmill a
  pin that names no version deploys and fails at run time
  ([OF-14](../../docs/semantics/openflow.md#of-14)/f).
- **A provided site** must be mandatory, a call site, and match one module
  in every iteration and branch: `iterations` and `branches` are refused on
  it, since its target is replaced everywhere.

## Tests

- `conformance_test.go` runs the compile cases of
  [`conformance/ir`](../../conformance/README.md): it writes each case's
  folder to a temporary directory, compiles it with the case's contract and
  options, and checks the errors, or parts of the program by JSON Pointer,
  by flow, by a step's site, by a module list's sites, and by inventory id;
  every compiled program must also round-trip through its JSON form. A
  second test reads the catalogue in `docs/semantics/conformance.md`: every
  cited case exists with its provenance, every catalogue case of a finding
  with a file there is accounted for at this level, the decode level or
  the load level, and every case the decode level defers here is a case.
- `golden_test.go` compiles the folders under `testdata/golden/<case>/` with
  their `contract.yaml` and compares the whole program with `ir.json`:
  `deployed` (defaults, virtual steps, collapse, occurrences and every
  expression site), `provided` (the provided-site policy case) and
  `inventory` (every bundling mode, codebases, native steps, import bases).
  After a change to the IR, rewrite and review them:

  ```sh
  go test ./internal/ir -run TestGolden -args -update
  ```

- `contract_test.go` covers the contract's format and its refusals;
  `compile_test.go` covers load errors and their order, subflows compiled
  once and cycles, the nil contract, determinism and stable inline ids,
  provided-site details, the harness table over every language of
  [OF-13](../../docs/semantics/openflow.md#of-13), and the JSON form.
  `FuzzCompile` checks that compiling any `flow.yaml` never panics and that
  what compiles round-trips:

  ```sh
  go test ./internal/ir -run '^$' -fuzz FuzzCompile -fuzztime 60s
  ```

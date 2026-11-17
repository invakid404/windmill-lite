# internal/ir

**Current state:** `Compile` turns a
[sync folder](../../docs/glossary.md#sync-folder) and a
[host contract](../../docs/glossary.md#host-contract) into the
[program](../../docs/glossary.md#program) that the engine runs, with every expression's textual stages
compiled. Only Bun steps have a harness: a step in another language fails
with `unsupported_language`. The compiler refuses `suspend`, AI agent
steps, a codebase's custom bundler, `schema_validation` and pinned script
hashes ([Errors](#errors)).

This README covers `internal/ir/` and its `testdata/`: the golden
programs under `testdata/golden/`.

The package is the compiler of the
[IR, loader and validation](../../docs/design/0002-ir-and-loader.md)
design record. `Compile` turns a sync folder, read by
[internal/syncfolder](../syncfolder/README.md), and a host contract into
the program, with its script inventory. It
applies once what a successful deploy would have applied, so that the
engine receives a fully decided program, and it validates what the
contract's [roots](../../docs/glossary.md#root) reach.

- **Callers:** the build of [internal/bundle](../bundle/README.md)
  (`bundle.Build`), and the flow runner of `wltest check`
  ([cmd/wltest](../../cmd/wltest/README.md)). It compiles each target
  flow alone as the only root, and the fixture's other roots on their
  own. It then builds a bundle from a contract of every target that
  compiled, with those roots. When the build reports a script's problem,
  the targets that reach that script fail, and it builds again without
  them.
- **Its output** is what the engine runs
  ([internal/engine](../engine/README.md)); `ir.json` in a bundle is its
  JSON form.
- **What it calls:** [internal/syncfolder](../syncfolder/README.md) (the
  folder), [internal/openflow](../openflow/README.md) (the decoded flow),
  [internal/expr](../expr/README.md) (`expr.Compile`),
  [internal/yamlx](../yamlx/README.md) (the contract's YAML) and
  [internal/conformance](../conformance/README.md) (site paths and
  occurrences).

### What the compiler leaves to other packages

| What | Who does it |
|---|---|
| An expression's bytecode | The engine compiles each expression's source when it opens the program (`engine.New`). The compiler runs only the textual stages with [`expr.Compile`](../expr/README.md): trimming, shortcuts, the helper rewrite, the steps to prefetch, the mode, the bound names and the engine source ([JE-5](../../docs/semantics/expressions.md#je-5) to [JE-15](../../docs/semantics/expressions.md#je-15)). They depend on the site and the text alone, so `ir.json` carries them |
| Bundling, signatures, the relative-import closure, workflow-as-code detection, the refusal of a non-codebase script whose build reports a package import and that has no lock | [internal/bundle](../bundle/README.md), with the Bun build helper's scans. The inventory records what they start from: the code, the lock, the import base, the bundling mode and the codebase entry |
| A provided site's provider at run time | `wl`'s `Open` refuses a named provider that is not registered (`missing_provider`), and the engine latches `provider_declined` when one declines ([wl](../../wl/README.md), [internal/engine](../engine/README.md#host-provided-sites)) |

## Files

| File | What it holds |
|---|---|
| `compile.go` | `Compile`, `Options`, `StoredNode`: the walk of the roots' closure, normalising to the deployed form, validation |
| `ir.go` | `Program`, `Flow`, `Module` and its kind fields, the envelope's types, transforms, `Target`, `Version`, and the JSON form |
| `inventory.go` | The script inventory: `Script`, `ScriptKind`, `Bundling`, `Codebase`, `Metadata`, import bases and `CollapseBodies` |
| `harness.go` | Harness classes and boundaries: `Class`, `Boundary`, `BoundaryOf`, `DeployedLanguage` |
| `contract.go` | The host contract: `Contract`, `Root`, `ProvidedSite`, `Selector`, `ParseContract`, `Contract.Validate` |
| `selector.go` | How a selector names a module, and the ambiguity check |
| `errors.go` | `Error`, `ErrorKind` and the error kinds |

## Public surface

### Compile

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Compile` | func | Walks the closure of the contract's roots, normalises, validates, and returns the `*Program`, or every error found, joined. With a nil contract, every flow and script that the folder lists is a root | `internal/bundle`, `wltest check` |
| `Options` | struct | `Strict`: refuse an absent for-loop `skip_failures` or branchall branch `skip_failure` instead of defaulting it ([Q-CF-13](../../docs/semantics/control-flow.md#q-cf-13)) | the same |
| `Error`, `ErrorKind` | struct, string type | Every failure: `Kind`, the flow and step, the script or the provided site, a message, and the wrapped `*syncfolder.Error` of a load failure ([Errors](#errors)) | `wltest`; `internal/bundle` passes the errors on |
| `Version` | const | `2`: the version of the JSON form, which carries the expressions compiled | `wl`, which refuses an `ir.json` of another version |

### Program types

The engine walks these types when it opens the program and in every run.
`internal/bundle` writes the program as `ir.json`, `wl` reads its
`Version` and matches selectors against its modules, and `wltest check`
reads its flows and modules.

| Name | Kind | What it holds |
|---|---|---|
| `Program` | struct | Every flow that the roots reach, the script inventory, the contract, the deviations. `Program.MarshalIndent` writes `ir.json`, which decodes back to the same program |
| `Flow` | struct | A flow: its modules, its failure and preprocessor slots, its settings and timeouts |
| `Module`, `Kind` | struct, string type | One step: its id, its envelope, and one kind field. `KindScript`, `KindRawScript`, `KindIdentity`, `KindSubflow`, `KindForLoop`, `KindWhileLoop`, `KindBranchOne`, `KindBranchAll`, `KindProvided` |
| `Leaf`, `Subflow`, `ForLoop`, `WhileLoop`, `BranchOne`, `OneBranch`, `BranchAll`, `AllBranch`, `Provided` | structs | The kind fields ([The program](#the-program)) |
| `Transform`, `TransformKind`, `Input` | structs, string type | An input transform, an iterator, a sleep, a timeout or a parallelism: `Static`, `Javascript` or `AI` |
| `Stop`, `Retry`, `ConstantRetry`, `ExponentialRetry`, `Mock` | structs | The envelope's stop, retry and mock |
| `StoredNode` | func | Whether a deploy stores a container's body as a node ([Collapse](#collapse)) |
| `CollapseBodies` | func | A runnable path cut at its first nested-body segment, as `<prefix>/flow` |

### Scripts and harnesses

`internal/bundle` builds each `Script` of the inventory, and the engine
reads a step's script, class and boundary. `wltest check` applies
`BoundaryOf` and `DeployedLanguage` to its step cases.

| Name | Kind | What it holds |
|---|---|---|
| `Script`, `ScriptKind` | struct, string type | One entry of the script inventory ([The script inventory](#the-script-inventory)): `PathScript` or `InlineScript` |
| `Metadata`, `EnvVar` | structs | A path script's `.script.yaml` metadata; one entry of a flow's `flow_env` |
| `Codebase` | struct | The `codebases` entry that selects a script, and its format |
| `Bundling` | string type | `BundlingBundled`, `BundlingNone` (`nobundling`), `BundlingCodebase`, `BundlingTranspiled` |
| `Class`, `Boundary` | string types | A harness class (`ClassBun`, `ClassPython3`, `ClassDeno`, `ClassGo`, `ClassBash`) and a boundary (`BoundaryBun`, `BoundaryNative`, `BoundaryOwn`) ([Harness classes](#harness-classes)) |
| `BoundaryOf`, `DeployedLanguage` | funcs | A language's boundary; the language that a deploy records for a Bun-family script. `wltest check`'s step cases apply them too |

### Contract types

`internal/bundle` reads a contract with `ParseContract` and passes it to
`Compile`. `wltest check` reads a fixture's provided sites with
`ParseContract`, and passes `Compile` a `Contract` of its own, with `Root`
and `ProvidedSite` values. `wl` gives hosts `Selector` as
`wl.Selector`.

| Name | Kind | What it holds |
|---|---|---|
| `Contract`, `ParseContract`, `Contract.Validate` | struct, func, method | A host contract: read it, check it ([The host contract](#the-host-contract)) |
| `Root`, `Target` | structs | A root; a provided site's original target: its kind (`script`, `flow` or `rawscript`) and path |
| `ProvidedSite`, `Selector` | structs | A mandatory host-provided call site, and the selector that names it |

## Normalising to the deployed form

windmill-lite matches a successfully deployed flow, run as the lite form
(the [equivalence contract](../../docs/design/0001-equivalence-contract.md#1-the-reference-configuration)).
The compiler applies these once:

- **Container defaults** ([OF-6](../../docs/semantics/openflow.md#of-6),
  [OF-7](../../docs/semantics/openflow.md#of-7),
  [CF-6](../../docs/semantics/control-flow.md#cf-6)): an absent for-loop
  `skip_failures` is `true`; a branchall branch's `skip_failure` and a
  while loop's `skip_failures` are `false`; branchall `parallel` is
  `true`. Under `Strict`, an absent for-loop or branch flag is refused
  instead (`absent_skip_flag`).
- **The language that a deploy records** for a Bun-family rawscript:
  `bunnative` when its header carries the `native` annotation, `bun`
  otherwise, in either direction
  ([SY-V3](../../docs/semantics/openflow.md#sy-v3),
  [OF-15](../../docs/semantics/openflow.md#of-15),
  [WR-40](../../docs/semantics/languages.md#wr-40)). Path scripts get it
  from syncfolder. `DeployedLanguage` is the rule, and `BoundaryOf` the
  boundary of a language.
- **The virtual `<id>-v` identity** after a module list whose last module
  has a `sleep`, at every level, with Windmill's summary
  ([OF-17](../../docs/semantics/openflow.md#of-17),
  [FX-4](../../docs/semantics/execution.md#fx-4)). The failure and
  preprocessor modules sit in slots, not lists, and get none.
- **Dropped fields:** while-loop `parallel` and `parallelism`
  ([OF-8](../../docs/semantics/openflow.md#of-8)); `delete_after_use` and
  `delete_after_secs` ([OF-12](../../docs/semantics/openflow.md#of-12));
  queue and placement fields (`priority`, concurrency, debouncing, `tag`,
  `preserve_step_tags`); and a `script` or `flow` value's own
  `pass_flow_input_directly`: only the envelope's takes effect
  ([OF-V4](../../docs/semantics/openflow.md#of-v4)).
- **Dropped permission fields:** the flow's `on_behalf_of` and
  `on_behalf_of_email`, and a step's `job_token_scopes`, which the
  equivalence contract puts
  [out of scope](../../docs/design/0001-equivalence-contract.md#3-out-of-scope).
- **Locks** are kept exactly as the folder gives them, never generated or
  relocked. An empty or absent lock is not an error by itself
  ([OF-15](../../docs/semantics/openflow.md#of-15),
  [OF-V6](../../docs/semantics/openflow.md#of-v6),
  [Q-OF-5](../../docs/semantics/openflow.md#q-of-5)).

## The program

`Program.Flows` holds every flow that the roots reach, by path, and
`Program.Scripts` the inventory. A `Module` has its id, its kind, its
envelope already decoded (`skip_if`, `stop_after_if`,
`stop_after_all_iters_if`, `sleep`, `timeout`, `retry`,
`continue_on_error`, `mock`, `cache_ttl`, `pass_flow_input_directly`, …),
and one kind field:

| Kind | Field | Holds |
|---|---|---|
| `script`, `rawscript` | `Leaf` | The inventory id, the harness class and boundary, the input transforms |
| `flow` | `Subflow` | The flow's path (compiled into `Program.Flows`) and the input transforms |
| `forloopflow`, `whileloopflow` | `ForLoop`, `WhileLoop` | The body, the skip flag, `parallel` and `parallelism` (for loops), `squash`, and `collapse` |
| `branchone`, `branchall` | `BranchOne`, `BranchAll` | The branches (predicates for `branchone`, skip flags for `branchall`), the default, `parallel` |
| `identity` | none | `virtual` marks the `<id>-v` step |
| `provided` | `Provided` | The contract's site index, the static value or the provider's name, the original target and its input transforms |

### Flow timeouts

A `Flow` keeps two timeouts apart:

- **`ValueTimeout`** is the flow's `value.timeout`, its wall-clock limit,
  as written. The engine treats a value of 0 or less as unset
  ([FX-52](../../docs/semantics/execution.md#fx-52)).
- **`Timeout`** is the metadata `timeout` beside `value`, which is stored
  and enforces nothing ([OF-16](../../docs/semantics/openflow.md#of-16)).

### Occurrences

Windmill does not check ids
([OF-2](../../docs/semantics/openflow.md#of-2)). A module whose id occurs
more than once in its module list records its occurrence, counted from 0
in list order over the final list, virtual steps included. Site paths
carry it as `m:<id>~<k>`
([differential testing](../../docs/design/0011-differential-testing.md)).

### Transforms

A transform is `static` (the JSON text verbatim,
[JE-40](../../docs/semantics/expressions.md#je-40)), `javascript` (an
expression compiled for its site kind), or `ai`, which the engine leaves
out of a step's arguments ([FX-7](../../docs/semantics/execution.md#fx-7)).
The input transforms of the failure module itself have the site
`failure_input_transform`
([JE-35](../../docs/semantics/expressions.md#je-35)).

### Collapse

One function, `collapses`, decides whether a loop collapses
([FX-46](../../docs/semantics/execution.md#fx-46),
[CF-8](../../docs/semantics/control-flow.md#cf-8),
[CF-V5](../../docs/semantics/control-flow.md#cf-v5)). In the lite form,
nested bodies are stored nodes, so the deployed rule holds at every depth.
A loop collapses when all of these hold:

- it is a sequential for loop, or any while loop;
- its body as written is exactly one `script` or `rawscript` step;
- that step has none of `sleep`, `suspend`, `cache_ttl`, `retry`,
  `timeout`, `stop_after_if`, `stop_after_all_iters_if`, `skip_if` or an
  enabled `mock`;
- its flow has no failure module;
- it is not a for loop in the preprocessor module's bodies
  ([Decisions where the reference is silent](#decisions-where-the-reference-is-silent)).

A body module that is a provided site keeps its original kind for the
rule.

**Stored nodes.** A deploy applies the same body test, except that
`timeout` does not count, to choose which container bodies it stores as
nodes ([CF-V4](../../docs/semantics/control-flow.md#cf-v4)). `StoredNode`
reports it for a compiled body: every body but an empty or a simple one.
The engine's job records follow it, so a child flow of a stored body reads
as a `FlowNode` job's.

[CF-8](../../docs/semantics/control-flow.md#cf-8)'s test, which the
deploy's follows, has no `timeout`; the collapse test of
[FX-46](../../docs/semantics/execution.md#fx-46) adds it.

## Harness classes

| Language | Class | Boundary |
|---|---|---|
| `bun` | `bun` | `bun` |
| `bunnative`, `nativets` | `bun` | `native` |
| `python3`, `deno`, `go`, `bash` | One class each, which has no harness yet | `own` |
| Anything else | None | |

- **The native boundary.** Native steps run in the Bun harness under a
  native boundary mode, not in Windmill's isolate, a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **Only the `bun` class has a harness,** so a leaf of any other language
  fails with `unsupported_language: <lang>`, naming the step, or the
  script for a root script.

## The script inventory

Every path script and inline script that the roots reach has one `Script`
entry:

| Field | Holds |
|---|---|
| `ID` | A path script's path. For an inline step, `inline-` and 16 hex digits of the SHA-256 of its flow path and site, which is unique, file-name safe, and stable while the step stays where it is |
| `Path` | The Windmill path: the script's own, or `<flow path>/<module id>` for an inline step |
| `Flow`, `Module`, `Site` | Where an inline step is: its site path within the flow, with iteration segments left out (`m:l/m:b`, `m:o/b:default/m:c`, `s:failure`) |
| `File`, `LockFile` | The files that the code and the lock were read from |
| `Language`, `Harness`, `Boundary` | As in [Harness classes](#harness-classes) |
| `Content`, `ContentDigest` | The code (not in the JSON form), and its SHA-256 |
| `Lock` | The lock text, byte for byte, or null |
| `Bundling` | `bundled`; `nobundling` for a `//nobundling` script, which the build bundles all the same; `codebase`; `transpiled` for `nativets` ([WR-27](../../docs/semantics/languages.md#wr-27), [WR-40](../../docs/semantics/languages.md#wr-40)) |
| `Codebase` | The entry that selects a codebase script, and its format: `esm` only when the entry says so, otherwise `cjs` ([WR-47](../../docs/semantics/languages.md#wr-47)) |
| `ImportBase` | What relative imports resolve against, as a file ([WR-29](../../docs/semantics/languages.md#wr-29); below) |
| `Annotations` | The recognised header flags of a Bun-family script ([WR-31](../../docs/semantics/languages.md#wr-31)) |
| `Schema`, `Metadata` | A path script's `.script.yaml` schema, and its `envs`, `timeout`, `cache_ttl` and `has_preprocessor` |
| `DependencySet` | The dependency part of the harness pool key ([harness protocol](../../docs/design/0004-harness-protocol.md)): `bundled`, `lock:sha256:<hex>` for a `nobundling` script, or `codebase:<index>` |

An inline step's `ImportBase` is one of these:

- `<flow path>/<module id>` at top level and in the failure slot;
- `<flow path>/preprocessor` for the preprocessor;
- `<flow path>/flow` in a body;
- in the failure and preprocessor slots and the containers that fill them,
  the rawscript's own `path`, collapsed at a body segment
  (`ir.CollapseBodies`). A deploy drops that `path` from the module lists.

A path script's import base is its path.

### The deviations a program lists

`Program.Deviations` lists, sorted, the
[named deviations](../../docs/design/0001-equivalence-contract.md#5-named-deviations)
that the folder's content puts in effect, by the contract's ids. The
bundle's manifest adds those of every bundle and of its mode
([internal/bundle](../bundle/README.md#the-deviations-a-bundle-lists)).

| Value | When | What differs from Windmill |
|---|---|---|
| `"D5"` | A step at the native boundary | It runs in the Bun harness under the native boundary, not in Windmill's V8 isolate |
| `"D6"` | A module id derived from its position, in a folder that was never deployed | Windmill gives such a module a random id |
| `"D7"` | A `//nobundling` script, which the build bundles | Windmill resolves its imports and installs its packages at run time |
| `"D12"` | The contract lists host-provided modules | One instance per process, shared by the host and every script that imports it |
| `"D13"` | A CommonJS codebase script | It runs on Bun's CommonJS loader, not Node |
| `"D19"` | A `bundled` script | A module that a `sideEffects: false` package re-exports stays, when it is reached through a bare import that ends in `.ts` |

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
host_modules: [f/app/scope.ts]   # modules of which the host and the steps share one instance
```

- **The format.** The file is YAML or JSON, read with the sync folder's
  dialect ([internal/yamlx](../yamlx/README.md)). Unknown and repeated keys
  are errors.
- **A selector** ([host hooks](../../docs/design/0008-host-hooks.md#selectors))
  is `{flow, module}`, which names that id in any module list of the flow,
  slots and container bodies included, with an optional `occurrence`. Or
  it is `{script}`, which names every call of a script: a `script` step by
  its path, and an inline step by `<flow path>/<module id>`.
- **An empty module id.** A module id may be empty (`module: ''`), so
  `Selector.Module` is a pointer, and nil means no module field.
- **A static provider's value** must be JSON text, `null` included.

The rules of provided sites:

1. **The walk stops at a provided site.** Its target is not loaded,
   validated or bundled, and need not be in the folder, unless another path
   reaches it. Flows are read with their `!inline` references unresolved,
   and a rawscript's content and lock files are read only when it is
   compiled as a step. So a provided rawscript's files are never read.
2. **The site becomes a `provided` module** that keeps its envelope and its
   input transforms, so the orchestration around it is the real one. A
   `suspend` on the site's own envelope belongs to its flow, and is still
   refused.
3. **Every provided site must match exactly one site.** A selector that
   names an id occurring more than once in one list, without its
   occurrence, or that matches two sites, fails with `ambiguous_selector`.
   One that matches nothing, two sites that select one module, and a site
   that is not a `script`, `flow` or `rawscript` step fail with
   `invalid_contract`.
4. **A provided site** must be mandatory, a call site, and match one module
   in every iteration and branch: `iterations` and `branches` are refused
   on it, since its target is replaced everywhere.

The compile-time half of the IR and loader design record's policy case is
`conformance/ir/fx-39.json`. A root that calls a suspending subflow fails
with `unsupported: suspend`, naming the subflow. It compiles with the call
site provided, and fails with `ambiguous_selector` when the selector
matches two sites.

## Errors

`Compile` returns an `*Error` for each failure, with one of these kinds:

| `Kind` | When it is raised |
|---|---|
| `load` (`ErrLoad`) | The folder cannot supply an item that the closure reaches, or the item fails to read. The wrapped `*syncfolder.Error` says which (`not_in_folder`, `filtered`, `hub_path`, `invalid_flow`, …; [SY-15](../../docs/semantics/openflow.md#sy-15)). An inline step's files (`missing_inline_file`, `invalid_text`, `path_escape`) fail that step |
| `unsupported_feature` (`ErrUnsupported`) | `unsupported: <feature>` for `suspend` ([FX-39](../../docs/semantics/execution.md#fx-39)), `aiagent`, and `flowscript` (the lite form's internal reference, which a folder cannot hold). Also for a codebase entry's `customBundler` when it is set, JavaScript-truthy ([WR-48](../../docs/semantics/languages.md#wr-48)), and a `schema_validation` annotation. The equivalence contract puts these [out of scope](../../docs/design/0001-equivalence-contract.md#3-out-of-scope) or [restricts](../../docs/design/0001-equivalence-contract.md#5a-restrictions) them |
| `unsupported_language` (`ErrLanguage`) | A leaf whose language has no harness ([Harness classes](#harness-classes)) |
| `pinned_hash` (`ErrPinnedHash`) | A `script` step with a `hash` ([OF-14](../../docs/semantics/openflow.md#of-14)): a folder never records the hash that Windmill gave its version, so no pin can be shown to be the folder's |
| `absent_skip_flag` (`ErrAbsentSkipFlag`) | Under `Strict`, an absent or null for-loop `skip_failures`, or an absent branchall branch `skip_failure` (a null one is a decode error, [OF-V1](../../docs/semantics/openflow.md#of-v1)) |
| `ambiguous_selector` (`ErrAmbiguousSelector`), `invalid_contract` (`ErrContract`) | The host contract ([The host contract](#the-host-contract)). `invalid_contract` also when the contract is nil and the folder has no flow or script |

- **Names.** Errors name the step as `<flow path> step '<id>'`, or the
  flow, the script or the provided site.
- **A malformed contract** fails on its own, before any reading.
- **Order.** Otherwise `Compile` returns every error, in walk order: the
  root scripts; then each flow in the order that the walk reaches it,
  module by module; and last the provided sites that matched no site or
  more than one.

## Decisions where the reference is silent

The reference left these open when the compiler took them. Where it now
states the rule, the row says so.

| Question | The compiler's choice | Why | Pinned by |
|---|---|---|---|
| Does a loop in the preprocessor's bodies collapse? | A for loop never does; a while loop still does | A deploy does not reduce the preprocessor module to stored nodes ([OF-3](../../docs/semantics/openflow.md#of-3)), so its bodies run as raw inline flows, in which a for loop never collapses ([CF-V5](../../docs/semantics/control-flow.md#cf-v5)). [CF-30](../../docs/semantics/control-flow.md#cf-30) now states this | `conformance/ir/cf-v5.json` (variant `preprocessor`) |
| Are collapse disqualifiers presences or values? | Presences: `retry: {}` and `cache_ttl: 0` disqualify a body, as any other value does | [CF-8](../../docs/semantics/control-flow.md#cf-8) now states this | `conformance/ir/fx-46.json` (`retry: {}`); `TestStoredNode` (`cache_ttl: 0` for stored nodes); `cache_ttl: 0` for collapse is code only |
| When is the virtual step added? | For a final `sleep` of any transform type; not after the failure or preprocessor module, which are slots | [CF-30](../../docs/semantics/control-flow.md#cf-30) | `testdata/golden/deployed` (any transform type); `conformance/ir/of-17.json` (the failure slot); the preprocessor slot is code only |
| What is the import base of an inline step with an explicit `path`? | The default base in the module lists and their container bodies, since a deployed flow drops the field there. The failure and preprocessor slots stay raw and keep it, collapsed at a nested-body segment as a runnable path is | [WR-29](../../docs/semantics/languages.md#wr-29) (cases [WR-29/b](../../docs/semantics/conformance.md#wr-29), [WR-29/c](../../docs/semantics/conformance.md#wr-29)) | `testdata/golden/inventory`, `conformance/ir` |
| How is `schema_validation` read? | As a header annotation key, by the rule of [WR-31](../../docs/semantics/languages.md#wr-31) | The compiler's reading; [WR-54](../../docs/semantics/languages.md#wr-54) now differs: the line has its own test, which needs exactly `// schema_validation` (for TypeScript) and stops at a blank line | `conformance/ir` |
| When does `customBundler` count as set? | When it is JavaScript-truthy: absent, null, `false`, `0` and `""` leave the entry to esbuild; any other value, `"0"` and `true` included, is refused | [WR-48](../../docs/semantics/languages.md#wr-48) | `conformance/ir/wr-48.json`, `conformance/build/wr-48.json` |
| Is a pinned `hash` refused only when it names no version? | It is refused whatever its value | A folder cannot show a pin to be its own. On Windmill a pin that names no version deploys and fails at run time ([OF-14](../../docs/semantics/openflow.md#of-14)/f) | `conformance/ir/of-14.json` (one value); "whatever its value" is code only |
| What may a provided site be? | Mandatory, a call site, and one module in every iteration and branch; `iterations` and `branches` are refused | Its target is replaced everywhere | `conformance/ir/fx-39.json` (a call site); `contract_test.go` (mandatory; `iterations` and `branches`) |

## Tests

The tests need no Bun and no environment variable.

### Run the tests

1. Run the package's tests, as CI's Go jobs do:
   `go test -race ./internal/ir`
2. After a change to the IR, rewrite the golden programs, then review the
   diff:
   `go test ./internal/ir -run TestGolden -args -update`
3. Fuzz the compiler for 60 s; the command stops then, and fails on a
   panic or a program that does not round-trip:
   `go test ./internal/ir -run '^$' -fuzz FuzzCompile -fuzztime 60s`

CI runs the tests (not the fuzzer) in `Go (ubuntu-latest)` and
`Go (macos-latest, packages)` ([docs/ci.md](../../docs/ci.md#the-go-jobs)).

### Test files

| File | What it checks |
|---|---|
| `conformance_test.go` | The compile cases of [`conformance/ir`](../../conformance/README.md). It writes each case's folder to a temporary directory, and compiles it with the case's contract and options. It checks the errors, or parts of the program: by JSON Pointer, by flow, by a step's site, by a module list's sites and by inventory id. Every compiled program must also round-trip through its JSON form |
| `conformance_test.go`, the catalogue test | Reads the catalogue in `docs/semantics/conformance.md`. Every cited case exists with its provenance. Every catalogue case of a finding with a file there is accounted for at this level, the decode level or the load level. Every case that the decode and load levels defer here is a case |
| `golden_test.go` | Compiles the folders under `testdata/golden/<case>/` with their `contract.yaml`, and compares the whole program with `ir.json`. The cases are `deployed` (defaults, virtual steps, collapse, occurrences and every expression site but `parallelism`, which is static there), `provided` (the provided-site policy case) and `inventory` (every bundling mode, codebases, native steps, import bases) |
| `contract_test.go` | The contract's format and its refusals, selector fields, an empty module id, a static value that must be JSON |
| `compile_test.go` | Error texts; load errors and their order; subflows compiled once, and cycles; the nil contract; determinism and stable inline ids; provided-site details; the host-modules deviation. The harness class and boundary of every language that the decoder accepts (`openflow.Languages`, [OF-13](../../docs/semantics/openflow.md#of-13)); `DeployedLanguage`; the truthiness of `customBundler`; `StoredNode`; the JSON form |
| `fuzz_test.go` | `FuzzCompile`: compiling any `flow.yaml` never panics, and what compiles round-trips |

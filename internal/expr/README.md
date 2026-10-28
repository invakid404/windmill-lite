# internal/expr

**Current state:** contracts, from milestone K1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)), with one
implementation of the engine. This README covers `internal/expr` except its
`quickjs/` directory. The package defines the compiled-expression IR and
the two interfaces that the expression pipeline is built around. No
pipeline stage is implemented yet. The `Engine` is implemented by the
QuickJS-ng backend of milestone B1a, in
[internal/expr/quickjs](quickjs/README.md). The textual pipeline is B2a and
its run on the real engine is B2b.

## Contracts

| Name | What it is | Implemented by |
|---|---|---|
| `SiteKind` | the expression sites of [JE-22](../../docs/semantics/expressions.md#je-22) (the suspend site is left out, because `suspend` is rejected at load); `Predicate` marks the boolean sites of [JE-24](../../docs/semantics/expressions.md#je-24) | this package |
| `Expr`, `Shortcut`, `Mode` | a compiled expression as the IR carries it ([ADR 0002](../../docs/design/0002-ir-and-loader.md)): the text as written, the trimmed text, the shortcuts its text allows ([JE-6](../../docs/semantics/expressions.md#je-6) to [JE-9](../../docs/semantics/expressions.md#je-9)), the mode ([JE-13](../../docs/semantics/expressions.md#je-13)), the bound names ([JE-10](../../docs/semantics/expressions.md#je-10)) and the engine source | B2a fills it |
| `Engine`, `Program`, `Input`, `Exception`, `Limits` | the JavaScript engine behind a Go interface ([ADR 0003](../../docs/design/0003-semantics-placement.md)): compile once, evaluate on a fresh runtime with host strings and host functions, one attempt's limits | B1a: [internal/expr/quickjs](quickjs/README.md) |
| `Evaluator`, `Scope`, `Results`, `Helpers` | what the flow interpreter calls at an expression site: the scope of [JE-22](../../docs/semantics/expressions.md#je-22), the results lookups of [JE-18](../../docs/semantics/expressions.md#je-18) and [JE-19](../../docs/semantics/expressions.md#je-19), and the `variable`, `resource` and `flow_user_state` helpers of [JE-20](../../docs/semantics/expressions.md#je-20) | B2a, B2b; the platform milestones provide `Helpers` |

## The engine boundary

- **Bindings.** An evaluation binds context values as host strings, which
  the program parses itself. Values are never spliced into source text.
- **Host functions.** These cover the `results` lookups and the helpers. They
  return text that resolves a Promise, or an error that rejects it.
- **Result.** The program's completion value must be a string, which the
  engine returns as UTF-8. Serialisation (`JSON.stringify(value ?? null)`,
  [JE-15](../../docs/semantics/expressions.md#je-15)) is the program's job,
  so all the semantics stay in the source that the pipeline builds.
- **Errors.** `Exception` keeps what was thrown (an `Error`, `null`,
  `undefined` or another value). QuickJS-ng reports running out of memory as
  an `InternalError` whose text is exactly `out of memory`, or as a bare
  `null` or `undefined` throw, and Windmill treats all three as out of memory
  ([JE-3](../../docs/semantics/expressions.md#je-3)). Message texts belong to
  the pipeline.
- **Limits.** `DefaultLimits` gives 128 MiB per runtime
  ([JE-3](../../docs/semantics/expressions.md#je-3)), 20 s per attempt
  ([JE-2](../../docs/semantics/expressions.md#je-2); an attempt that reaches
  it is interrupted, deviation D8) and a stack of 1 MiB, QuickJS-ng's
  built-in default, which Windmill's embedding never changes
  ([JE-48](../../docs/semantics/expressions.md#je-48)).

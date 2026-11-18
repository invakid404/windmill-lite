# internal/expr

**Current state:** working. The textual stages, the evaluator with its
shortcuts and its Go answers, and the run of each expression's program on
QuickJS-ng all work, and their tests hold them to the
[oracle](../../docs/glossary.md#oracle)'s
[recordings](../../docs/glossary.md#recording). Every site of a flow
compiles, but the engine refuses a flow that uses `skip_expr`
([internal/engine](../engine/README.md)). This README covers
`internal/expr` and its `testdata/`, but not its `quickjs/` directory.

This package evaluates the [expressions](../../docs/glossary.md#expression)
of a [flow](../../docs/glossary.md#flow) (a Windmill workflow of steps) as
Windmill does. It has two halves:

- **`Compile`** runs the textual stages of an expression: trimming, the
  shortcuts that the text allows, the mode, the names it binds, the steps it
  reads, and the rewrite of helper calls. [internal/ir](../ir/README.md)
  calls it for every expression of a program, so `ir.json` carries the
  results.
- **`Pipeline`** is the evaluator. It answers with a shortcut, answers in Go
  what QuickJS-ng would answer the same way, or runs the expression's
  program on an `Engine`. The engine ([internal/engine](../engine/README.md))
  prepares a `Pipeline` for each program that it opens, and gives it each
  site's scope.

`Engine` is the interface to the JavaScript engine, which the QuickJS-ng
backend in [internal/expr/quickjs](quickjs/README.md) implements. The
program that it runs is Windmill's preludes, the expression in its wrapper
and the serialisation of its value. The pipeline writes Windmill's error
texts in Go from what the engine reports.

The [ADR](../../docs/glossary.md#adr)
[Where semantics live](../../docs/design/0003-semantics-placement.md) places
all of this in Go, and the ADR
[IR, loader and validation](../../docs/design/0002-ir-and-loader.md) defines
what the IR carries. The rules come from the expression
findings of the [semantics reference](../../docs/semantics/expressions.md).

## Files

| File | What it holds |
|---|---|
| `expr.go` | `SiteKind`, `Mode`, `Expr`, `Shortcut`, `FlowKey`, `ResultsPath`: the compiled expression and its JSON form |
| `compile.go` | `Compile`: the seven textual stages |
| `evaluator.go` | `Evaluator`, `Scope`, `Named`, `Results`, `Helpers`: what the engine gives an evaluation |
| `pipeline.go` | `Pipeline`: `Prepare`, `Eval`, the shortcuts, the bindings and the host functions; `ErrNotCompiled` |
| `memberpath.go` | `ExactMemberPaths` and the member paths that the pipeline answers in Go |
| `pathexpr.go` | Null tests and object literals of member paths, answered in Go |
| `jsexpr.go` | `jsExpr`: templates, arithmetic, comparisons and predicates answered in Go, with the bounds that keep it under the engine's limits; `MemoryResults` |
| `jsontext.go` | `DatabaseText`, and the JSON scanners that the shortcuts use |
| `program.go` | The preludes and the program that an evaluation runs; `EvalError` and Windmill's error texts; the depth check |
| `debugform.go` | The debug form of a thrown value that is not an `Error` |
| `engine.go` | The engine boundary: `Engine`, `Program`, `Input`, `HostString`, `HostFunc`, `Exception`, `Thrown`, `ThrownValue`, `ValueKind`, `Limits`, `DefaultLimits`, `ErrAttemptTimeout`, `ErrNotString` |

## Public surface

### Compiling

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Compile` | func | Runs the textual stages on an expression as written at a site, and returns the `*Expr` | `internal/ir`, for every expression site |
| `Expr` | struct | A compiled expression: `Site`, `Text` (as written), `Trimmed`, `Shortcut`, `Mode`, `Binds`, `Prefetch`, `Source`, `RewritePanic`. Its JSON form is the one that `ir.json` holds: snake-case names, the site and mode by name, empty parts left out | `internal/ir`, `internal/engine` |
| `SiteKind` | type | Where an expression sits, which fixes its scope ([JE-22](../../docs/semantics/expressions.md#je-22)); `Valid`, `String`, `Predicate`, `MarshalText`, `UnmarshalText` | `internal/ir`, `internal/engine` |
| `SiteInputTransform`, `SiteFailureInputTransform`, `SiteIterator`, `SiteParallelism`, `SiteBranchOne`, `SiteSkipIf`, `SiteStopAfterIf`, `SiteStopAfterAllItersIf`, `SiteSleep`, `SiteTimeout`, `SiteRetryIf`, `SiteSkipExpr` | `SiteKind` constants | The sites. The suspend site is left out, because the loader rejects `suspend`. `Predicate` is true for the boolean sites ([JE-24](../../docs/semantics/expressions.md#je-24)): `branchone`, `skip_if`, `stop_after_if`, `stop_after_all_iters_if`, `retry_if` and `skip_expr` | `internal/ir` |
| `Mode` | type | How the engine source runs the text ([JE-13](../../docs/semantics/expressions.md#je-13)): `ModeExpression` or `ModeStatement`; `String`, `MarshalText`, `UnmarshalText` | the pipeline; `internal/engine` checks that it is set |
| `Shortcut` | struct | The shortcuts that the text alone allows: `ContextName`, `FlowKey`, `PreviousID`, `ResultsPath` | the pipeline; `internal/engine`, for a `flow_env` read by key |
| `FlowKey` | struct | The key of a `flow_input` or `flow_env` shortcut: `Env`, `Key` | the pipeline; `internal/engine` |
| `ResultsPath` | struct | The first line that the results path shortcut matches: `Step`, `Index`, `Segments`, `Declines` | the pipeline |

### Evaluating

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Evaluator` | interface | `Eval(ctx, *Expr, *Scope)`: what the engine calls at an expression site. Safe for concurrent use | `internal/engine` |
| `Pipeline` | struct | The `Evaluator`. Safe for concurrent use | `internal/engine` |
| `NewPipeline` | func | Returns a `Pipeline` that evaluates on an `Engine` | `internal/engine` (`engine.New`) |
| `(*Pipeline).Prepare` | method | Compiles the preludes and the programs of a bundle's expressions on the engine; never fails | `internal/engine` |
| `(*Pipeline).Eval` | method | Evaluates a compiled expression in a scope, and returns the value's producer text | `internal/engine` |
| `(*Pipeline).SetQuickJSOnly` | method | Turns `jsExpr` off, before the first evaluation | `internal/engine` (`Config.QuickJSOnly`) |
| `Scope` | struct | What one evaluation can see at its site: `PreviousID`, `Context`, `FlowInput`, `FlowEnv`, `Results`, `Helpers`. Its documentation lists the form of each text | `internal/engine` |
| `Named` | struct | One context value of `Scope.Context`: `Name`, `Value` | `internal/engine` |
| `Results` | interface | The site's results lookups. `Lookup` reads another step of the current flow, or any other id by node id, from storage ([JE-18](../../docs/semantics/expressions.md#je-18)); `Stored` finds a stored result by node id through the scope chain ([JE-19](../../docs/semantics/expressions.md#je-19)) | implemented by `internal/engine` |
| `MemoryResults` | interface | Marks a `Results` whose `Lookup` reads memory under a short lock and never waits | implemented by the engine's frame results |
| `Helpers` | interface | The `variable`, `resource` and `flow_user_state` helpers of [JE-20](../../docs/semantics/expressions.md#je-20) for one run | implemented by `internal/engine` over the platform services |
| `ExactMemberPaths` | interface | Marks an `Engine` that evaluates member paths over JSON values exactly as the pipeline's own answer does | implemented by `internal/expr/quickjs` |
| `DatabaseText` | func | Returns jsonb's printed form of a stored text: a space after every `:` and `,` outside strings ([JE-44](../../docs/semantics/expressions.md#je-44)) | `internal/engine`, for the flow input's members and for stored results that it reads back |

### Engine types

| Name | Kind | What it does | Who uses it |
|---|---|---|---|
| `Engine` | interface | `Compile(source)`, `Eval(ctx, Program, Input)`, `Limits()`. Safe for concurrent use | implemented by [internal/expr/quickjs](quickjs/README.md); called by `Pipeline` and `windmill-lite selfcheck` |
| `Program` | type | A program that an `Engine` compiled; only that engine can run it | `Pipeline`, the engine |
| `Input` | struct | What one evaluation binds: `Strings`, `Funcs`, `Preludes` | built by `Pipeline` and `windmill-lite selfcheck`; read by the engine |
| `HostString` | struct | A global string property: `Name` and `Text`, a JSON text that the engine does not parse | built by `Pipeline`; read by the engine |
| `HostFunc` | struct | A global function that returns a Promise: `Name`, `Call`, and `Inline` for a call that never blocks | built by `Pipeline`; called by the engine |
| `Limits` | struct | The per-evaluation limits: `MemoryMB`, `Attempt`, `StackBytes`; `Validate` reports a limit that is not positive | the engine, `Pipeline` (its messages) |
| `DefaultLimits` | func | Windmill's limits (see [Limits](#limits)) | `internal/expr/quickjs`; `internal/bundle`, for the manifest's stack size |
| `Exception` | struct | A value that a program threw: `Thrown`, the `Error`'s `Name`, `Message`, `HasMessage`, `Stack`, `HasStack`, a `Value` for anything else, and a diagnostic `Text` | made by the engine; read by `Pipeline` |
| `Thrown` | type | What kind of value was thrown: `ThrownError`, `ThrownNull`, `ThrownUndefined`, `ThrownOther` | the same |
| `ThrownValue` | struct | A thrown value that is not an `Error`: its `Kind` and its payload (`Int`, `Float`, `Bool`, `String`, `Address`) | the same |
| `ValueKind` | type | The kind of such a value, as Windmill's embedding (rquickjs 0.11.0) classifies it: `KindUnknown`, `KindBool`, `KindInt`, `KindFloat`, `KindString`, `KindSymbol`, `KindArray`, `KindConstructor`, `KindFunction`, `KindPromise`, `KindProxy`, `KindObject`, `KindBigInt`; `String` | the same |

### Errors

| Name | Kind | Meaning | Who sees it |
|---|---|---|---|
| `EvalError` | struct | A failed evaluation: `Message` is Windmill's text, `Err` the cause; `Error`, `Unwrap` | `internal/engine`, which wraps `Message` in its site's text |
| `ErrNotCompiled` | var | An `Expr` that `Compile` did not fill | the caller of `Eval` |
| `ErrOutOfMemory` | var | The evaluation ran out of memory, or threw `null` or `undefined` | an `EvalError`'s cause |
| `ErrAttemptTimeout` | var | An attempt reached its time limit and was interrupted | returned by the engine; an `EvalError`'s cause |
| `ErrTooDeep` | var | A bound value (a context value, `flow_input` or a `flow_env` member) is nested deeper than 127 levels | an `EvalError`'s cause |
| `ErrRewritePanic` | var | The text has a helper call that Windmill's rewrite panics on | an `EvalError`'s cause |
| `ErrNotString` | var | The program completed with something other than a string | returned by the engine; `Pipeline` turns it into `null` |

[Errors](#errors-and-their-texts) says when each is raised and what the
caller sees.

## The textual stages

`Compile(site, text)` depends on the site and the text alone. Nothing in it
depends on a run. Each stage implements a finding of the semantics
reference, linked at the stage.

1. **Wrap and trim**
   ([JE-5](../../docs/semantics/expressions.md#je-5),
   [JE-24](../../docs/semantics/expressions.md#je-24)). A predicate's text
   is wrapped as `Boolean(<text>)`, untrimmed. The result is trimmed of
   Unicode white space, as Rust's `str::trim` trims, which is
   `strings.TrimSpace`. `Text` keeps the text as written, which a site's
   error message quotes.
2. **The shortcuts that the text allows.** Each fires or misses at
   evaluation time.
   - `ContextName`: the text is one of the context names
     ([JE-6](../../docs/semantics/expressions.md#je-6)).
   - `FlowKey`: the text starts with `flow_input.` or `flow_env.` (the key
     is everything after the first dot), or is `flow_input["<key>"]` or
     `flow_env["<key>"]` with a non-empty key
     ([JE-7](../../docs/semantics/expressions.md#je-7)).
   - `PreviousID`: the id that the text names when the whole text is
     `results["<id>"]`, `results?.["<id>"]`, `results.<id>` or
     `results?.<id>`, the bracket forms read first
     ([JE-8](../../docs/semantics/expressions.md#je-8)). Evaluation compares
     the whole text with the four spellings of the previous id, so a text
     that spells two ids fires for either.
   - `ResultsPath`: the first line that is, alone and from column 0,
     `results` or `results?`, a dot, a step id of `[A-Za-z0-9_]`, an
     optional `[<digits>]`, then `.segment` parts. It `Declines` when its
     last segment is `length`, and later lines are not searched
     ([JE-9](../../docs/semantics/expressions.md#je-9)).
3. **Mode** ([JE-13](../../docs/semantics/expressions.md#je-13)). The text
   runs in statement mode when it starts with `return ` or `return;`, is
   `return`, starts with a statement prefix (`const `, `let `, `if `, `if(`,
   `try{`, `await ` and the others of `compile.go`), or holds a `;` outside
   quotes. A quote that no backslash precedes opens a quote of its kind when
   none is open, and closes the open quote of the same kind. The scan knows
   no comments or regular expressions.
4. **Bound names**
   ([JE-10](../../docs/semantics/expressions.md#je-10),
   [JE-17](../../docs/semantics/expressions.md#je-17)). `Binds` lists,
   sorted, the context names, `WM_SCHEDULED_FOR`, `flow_input`, `flow_env`,
   `results` and `error` that the text holds as substrings. It also lists
   `previous_result` when the text holds `error`.
5. **Static results reads**
   ([JE-12](../../docs/semantics/expressions.md#je-12)). `Prefetch` names
   the steps that the trimmed text reads statically, before the helper
   rewrite. At a site with a results context, the program fetches these
   steps before the code runs; the reads themselves are not rewritten.
   - A static read is `results`, at the text's start or after a character
     outside `[A-Za-z0-9_$]`, then a dot read or a bracket read.
   - A dot read is `.` or `?.` and `[A-Za-z0-9_]+`. A bracket read is `[`
     or `?.[`, one whole string literal in double or single quotes, then
     `]`. The literal may hold escapes, but no raw line break.
   - Each read is named by a literal: a dot read's identifier in double
     quotes, or a bracket read's literal as written. The literals are sorted
     by their bytes, without repeats.
   - The scan knows no strings, comments or scopes: `"see results.a"` names
     `a`, `flow_input.results.x` names `x`, and `my_results.x` names
     nothing. A computed key such as `results[k]` names no step.
6. **Helper rewrite**
   ([JE-11](../../docs/semantics/expressions.md#je-11)). Every
   `variable(`, `resource(` and `flow_user_state(` call is awaited.
   - The helpers are rewritten in that order, each over the text that the
     one before produced. The rewrite knows no words, strings or comments.
   - The text is cut at every occurrence of the call text. Each part after
     one, up to the next or the end of the text, becomes
     `(await <call><part>`, with one `)` inserted.
   - Parentheses are counted from the part's start, strings included. The
     `)` goes before the one that closes the call, or at the end of the part
     when none does. So a call nested in a call of the same helper cuts the
     outer one short.
   - The position is counted in characters from the call and applied as a
     byte offset. A part that is ASCII up to there is rewritten exactly, and
     text before the call moves nothing. Otherwise the `)` lands early by the
     extra bytes before it: `variable("é")` becomes `(await variable("é)")`,
     which does not compile.
   - Where the `)` would land inside a character, Windmill's evaluator
     panics. `Compile` then gives no `Source`, and records as
     `RewritePanic` the byte offset in the text that this helper's pass
     rewrites.
   - `TestJE11OnEngine` and the cases of `testdata/pipeline/je-11.json` pin
     the rewrite.
7. **Wrapper.** `Source` is the rewritten code in the mode's wrapper,
   called at once: `(async function() { return <code>; })()` or
   `(async function() { <code> })()`. The wrapper's opening and closing
   text sit on the code's first and last lines, so a `//` comment on the
   code's last line comments the closing text out
   ([JE-14](../../docs/semantics/expressions.md#je-14)). Stack frames show
   positions in the program around it
   ([Stack frames](#stack-frames)).

## How Eval answers

`Eval` answers by one of three routes, in this order: a shortcut, an answer
in Go, or the expression's program on the engine. Each Go answer gives what
the program would give, and declines everything else, so the engine then
runs as it would.

### Preparing a bundle's expressions

- `NewPipeline(engine)` returns the evaluator.
- `Prepare` compiles the preludes and each expression's program on the
  engine when a bundle opens. It compiles the program as a site with a
  results context runs it. The program without the prefetch, which an
  evaluation with no results lookup runs, is compiled on first use.
- A program that the engine rejects with an `*Exception` (a syntax error,
  say) is kept with that error. `Eval` fails with it each time the
  expression reaches the engine: an `*EvalError` whose cause is the
  `*Exception`. A shortcut still answers the expression without the engine.
- Any other compile error, such as a closed engine's, is not kept: the next
  evaluation compiles again (`TestCompileFailureNotKept`).
- An expression that `Prepare` did not see is compiled when an evaluation
  of it reaches the engine and its source is not yet in the cache. An
  evaluation that a shortcut or a Go answer serves compiles nothing; whether
  a shortcut fires depends on the scope, so a later evaluation of the same
  expression can still reach the engine. Two evaluations that meet a new
  source at once may both compile it; the programs are equivalent.
- Programs are kept by source text for the `Pipeline`'s life, so one
  `Pipeline` serves one bundle's expressions.

### The shortcuts

`Eval(ctx, e, scope)` first returns the context's cause if the context has
ended, and `ErrNotCompiled` for an `Expr` that `Compile` did not fill. It
then tries the shortcuts in this order. The first that fires answers,
unless the context has ended by then (as it can while the results path
shortcut reads), which gives the context's cause.

| Shortcut | Fires when | Answer |
|---|---|---|
| Context name ([JE-6](../../docs/semantics/expressions.md#je-6)) | The scope holds the context name | Its text verbatim |
| Flow key ([JE-7](../../docs/semantics/expressions.md#je-7)) | The flow input (or the flow env) has the key | The member's text verbatim, from the scope's text |
| Previous id ([JE-8](../../docs/semantics/expressions.md#je-8)) | The whole text is a spelling of the scope's previous id, and the scope has a results context and `previous_result` | `previous_result`'s text verbatim |
| Results path ([JE-9](../../docs/semantics/expressions.md#je-9)) | The path does not decline, and the scope has a results context | The path applied to the step's stored text as PostgreSQL's `#>` applies it, in jsonb's text output |

- The results path answers `null` for a missing step, key or index, a type
  mismatch or a null intermediate. jsonb's text output has a space after
  each `:` and `,`.
- The results path shortcut also needs an authenticated client. Every site
  with a results context has one
  ([JE-22](../../docs/semantics/expressions.md#je-22)), so the results
  context alone decides.
- A shortcut's text is observable: the `timeout` site's parse error quotes
  it, and the oracle's recordings show jsonb's spaces in a results path's
  answer ([JSON at step boundaries](../../docs/design/0005-json-step-boundaries.md)).
- The other shortcuts answer the scope's texts as they are. So the form
  that the engine puts there for a value read from the database is
  observable too: [JE-44](../../docs/semantics/expressions.md#je-44)'s table
  gives it, and `Scope`'s documentation lists it.
- The flow input's members are in jsonb's printed form, which the engine
  writes with `DatabaseText`. `previous_result` after a loop is a compact
  array with its numbers printed again.

### Member paths answered in Go

When no shortcut fires, the bindings are made and checked for depth
([The depth check](#the-depth-check)). Then the pipeline answers in Go a
text that only reads a member path of a JSON value. It does so only on an
engine that implements `ExactMemberPaths`, as QuickJS-ng's does
(`memberpath.go`).

- **The root** is a bound context value, or `results.<id>` for the
  previous id, which the results prelude answers from memory
  ([JE-18](../../docs/semantics/expressions.md#je-18), rule 1).
- **The reads** are `.key`, `?.key`, `[n]`, `["key"]` or `['key']`, as in
  `previous_result.items[0]?.id`.
- **A null test** (`pathexpr.go`): a path compared with `null` or
  `undefined` by `==`, `!=`, `===` or `!==`, alone (`true` or `false`), or
  choosing with `?:` between two terms. Each term is a path, `null` or
  `undefined`, as in `previous_result.x == null ? null : previous_result.x.id`.
  Only the chosen branch is read, as JavaScript reads it. Every path in a
  null test or an object literal reads the same root.
- **An object literal of paths** (`pathexpr.go`), in parentheses, each key
  an identifier or a quoted string other than `__proto__`, as in
  `({ id: flow_input.v.id, text: flow_input.v.text })`. Its keys come in
  JavaScript's order, array indexes first and ascending. A repeated key
  keeps its first place and its last value, and an `undefined` value is
  left out, as `JSON.stringify` leaves it out.

The answer is what the program would give. The root is parsed as
`JSON.parse` parses it, the members are read as JavaScript reads them, with
optional chaining, and the result is written as `JSON.stringify` writes it.
That covers repeated keys, key order, number spelling and escapes.

These are not answered as member paths, and go on to `jsExpr` or to the
engine:

- a property that `Object.prototype` gives, any key of an array other than
  an index or `length`, or a primitive's member;
- a read of `null` or `undefined` that is not optional, which throws;
- a bound value that is not JSON, nests deeper than 128, exceeds 1 MiB, or
  needs the rebuild by assignment
  ([JE-16](../../docs/semantics/expressions.md#je-16));
- a text that the helper rewrite changes (a helper call's text, in a quoted
  key or id too), since the program then reads what the rewrite spells;
- `results.<id>` for any other id. `jsExpr` may answer it from a
  `MemoryResults` lookup; otherwise the engine reads it, with a host call
  under the attempt limit, cancellation and retries of
  [JE-2](../../docs/semantics/expressions.md#je-2).

### Expressions answered in Go (jsExpr)

On the same engines, `jsExpr` (`jsexpr.go`) then answers in Go what it can
of the rest. It answers what the program would answer, and declines
everything else, before or during the evaluation. The engine then runs as it
would.

It takes texts built from these parts, and declines a text that is one
literal alone, which gains nothing:

- member paths of the bound roots, and of `results.<id>` read through a
  `MemoryResults` lookup (the engine's frame results). It reads every step
  that its roots name before it evaluates, in the order of the program's
  prefetch;
- template literals; string, number, boolean and null literals; `.length`,
  `!` and unary `-`;
- the arithmetic and relational operators, `===`, `!==`, `&&`, `||` and
  `?:`;
- a predicate site's text inside its `Boolean(` … `)`, the only call that it
  parses. That wrapper is the only one that a predicate site adds
  ([JE-24](../../docs/semantics/expressions.md#je-24)).

It declines:

- a member of `null` or `undefined` that is not optional, an inherited
  member, a string's member other than `length`, and `__proto__`;
- two objects compared, an object that an operator made and returned, and
  an object with a `toString` or `valueOf` key converted to a primitive;
- a string or an object converted to a number, and a lone surrogate;
- a lookup that fails, even of a step that the expression would not read.
  The program keeps the failure's text on its heap, which `jsExpr` does not
  bound ([JE-3](../../docs/semantics/expressions.md#je-3));
- a root that is not bound, and a static results read that is not one of its
  roots (in a string, say);
- a text that is not UTF-8, or that holds a comment or a line break;
- any evaluation that might meet a limit, by the bounds below.

| Bound (`jsexpr.go`) | `jsExpr` declines when |
|---|---|
| The heap cap ([JE-3](../../docs/semantics/expressions.md#je-3)) | An upper bound of its QuickJS heap passes the cap |
| `jsMaxDepth` | The parser nests deeper than 32 levels: each `?:` and unary level counts one, and each parenthesis two |
| `jsMaxText` | The text is longer than 16 KiB |
| `jsMinAttempt` | The attempt limit is less than 5 s |
| `jsMaxWork` | Its own work passes 4 MiB |
| The time it spends | It has spent a hundredth of the attempt limit |

`jsexpr.go` documents the constants, and why each is an upper bound.

- **Reads.** Its reads go through a memo, in the program's order, and only
  where the program makes them. When it declines after a read, the engine's
  bindings reuse what it read, so nothing is read twice.
- **Parsing.** A text's parse is cached by its mode and trimmed text, apart
  from the evaluation. Two evaluations that meet a new text at once may both
  parse it.
- **Turning it off.** `SetQuickJSOnly` turns it off. The engine's
  `Config.QuickJSOnly`, `wl.Options.QuickJSOnly` and the CLI's
  `-quickjs-only` set it. `WL_TEST_QUICKJS_ONLY=1` turns it off in this
  package's tests ([Tests](#tests)).

### The bindings

Otherwise the expression's program runs on the engine with these bindings
([JE-10](../../docs/semantics/expressions.md#je-10)). Values are bound as
host strings that the program parses; they are never spliced into source
text.

| Binding | Kind | When |
|---|---|---|
| Each context value (`previous_result`, `result`, `params`, …) | Host string: its text | The text names it and the scope holds it; `previous_result` also when the text names `error` or spells the previous id |
| `flow_input` | Host string: the flow input, or `null` | The text names it |
| `flow_env` | Host string: the flow env, or `{}` | The text names it |
| `__wl_results` | Host function | The text names `results`, and the scope has a results context |
| `__wl_variable`, `__wl_resource`, `__wl_flow_user_state` | Host functions | Always |
| `__wl_rebuild` | Host string: a JSON array that names the bound values that the context prelude rebuilds by assignment ([JE-16](../../docs/semantics/expressions.md#je-16)) | A bound value's text holds `__proto` or a `\u` escape |
| `__wl_previous_id` | Host string: the previous id, which the results prelude answers from memory | `__wl_results` and `previous_result` are both bound |

### The host functions

Every host function answers JSON text.

- **The results lookup.** Every id that it is asked is a `Results.Lookup`
  read, in stored text. It reads each step that the text names once, before
  the code runs, and each computed read of another step when that read
  runs. A failed read rejects with the read's message.
- **The previous id.** When `previous_result` is bound, the results prelude
  answers the previous id from memory, so the lookup never sees it
  ([JE-18](../../docs/semantics/expressions.md#je-18), rule 1). The
  equivalence contract's table of
  [findings that are not settled](../../docs/design/0001-equivalence-contract.md#6-findings-that-are-not-settled)
  records how the oracle settled this reading.
- **A lookup from memory.** When the scope's `Results` reads memory
  (`MemoryResults`, the engine's frame results), the results lookup is
  marked `Inline`. The engine may then answer it on its own thread instead
  of a goroutine. The helpers are never marked `Inline`.
- **The helpers.** They answer from `Helpers`. Without them, they reject
  with the `… is not available without an authenticated client` text of
  [JE-20](../../docs/semantics/expressions.md#je-20).
  `flow_user_state` without a results context rejects with
  `flow_user_state() is not available in this context`
  (`TestHelpersOnEngine`).

### The result

- **The value.** The engine's text, `JSON.stringify`'s output, is returned
  as the value's producer text, as Windmill reads it back
  ([JE-15](../../docs/semantics/expressions.md#je-15),
  `TestSerialisation`).
- **Not checked.** A string that is UTF-8 is returned as it is. So text that
  is not JSON passes when the expression replaced `JSON.stringify`
  (`TestReplacedSerialisation`, and the case `unchecked` of
  `testdata/pipeline/je-15.json`).
- **Anything else** (a value that is not a string, or a string with an
  unpaired surrogate) reads as `null`.
- **A failure** of the evaluation is an `*EvalError` whose message is
  Windmill's text. Some errors come back without one, such as a context
  that ends during the evaluation
  ([Errors and their texts](#errors-and-their-texts)).

## The program on the engine

`program.go` writes what an evaluation runs. An evaluation runs these
global scripts on one fresh context, each named `eval_script` and each in
strict mode ([JE-50](../../docs/semantics/expressions.md#je-50),
`TestStrictMode`):

1. **The context prelude**, always.
   - It converts the bound context values, `flow_input` and `flow_env` in
     place. Each becomes a property of `globalThis`, holding a value that
     keeps its text's key order (integer-like keys still first), `-0` and a
     double's precision
     ([JE-16](../../docs/semantics/expressions.md#je-16)).
   - Windmill's host rebuilds each object by assignment, key by key, so a
     `__proto__` key sets the prototype (an object or `null`) or is dropped
     ([JE-16](../../docs/semantics/expressions.md#je-16)).
   - The prelude parses with `JSON.parse`, and rebuilds by assignment only
     the values whose text can hold such a key: those with `__proto` or a
     `\u` escape. The pipeline finds them before it binds, and names them in
     `__wl_rebuild`, which the prelude removes (`TestBindings`).
   - It defines `variable`, `resource` and `flow_user_state` on
     `globalThis`, each awaiting its host function, and removes the host
     functions. A helper whose host function rejects throws
     `new Error(<the API's text>)` from its own frame
     ([JE-20](../../docs/semantics/expressions.md#je-20)).
2. **The results prelude**, when the evaluation binds the results lookup.
   It declares, with one `const`, the lexical `results`, a proxy over an
   empty object, so that `JSON.stringify(results)` is `{}`
   ([JE-12](../../docs/semantics/expressions.md#je-12)), and
   `__loadResults`, the prefetch.
   - `__loadResults` decodes each literal of `Prefetch` with an indirect
     `eval`, as the engine reads a string literal, and drops one that does
     not decode. It resolves every named step at once, and keeps each
     outcome: the value or the error.
   - A read of a kept step gives its value, or throws its error, at once,
     whatever its spelling, inside nested functions too. A read of any other
     string key resolves the step then, and gives a Promise. A symbol key
     gives `undefined` ([JE-18](../../docs/semantics/expressions.md#je-18)).
   - Resolving the previous id that `__wl_previous_id` names gives the
     `previous_result` global itself: the value that the context prelude
     rebuilt. So the previous step reads in the producer's key order, and
     another step in jsonb's, from the database (`TestJE8KeyOrderOnEngine`).
   - Resolving any other key reads it through the host lookup. The text is
     parsed with `JSON.parse` as it is when the lookup answers, so a
     `__proto__` key is an own property. A failed read throws the read's
     text from `__throwOrParse`, called from `__getResult`.
   - The kept outcomes are null-prototype records, and arrays read by index,
     so nothing that the user's code replaces reaches a read.
   - The `resource` and `flow_user_state` helpers parse the same way.
     `variable` answers a string, which Windmill does not parse.
   - `TestKeptReads` pins the results reads, and `TestResultsProxy` the
     proxy. The helpers' parse is pinned by the code only.
3. **The error prelude**, when the text mentions `error`. It declares the
   lexical `error` with `let`, so the expression may assign it
   ([JE-10](../../docs/semantics/expressions.md#je-10)). Its value comes
   from `previous_result` by the decision list of
   [JE-17](../../docs/semantics/expressions.md#je-17), whose fallbacks for
   an entry's tag and text are falsy tests (`||`). `TestErrorHelper` and
   the cases of `testdata/pipeline/je-10.json` pin `error` and its
   assignment.
4. **The program**
   ([JE-13](../../docs/semantics/expressions.md#je-13)): the prefetch, then
   `.then(() => `, `Source` and `)`, then
   `.then((v) => JSON.stringify(v ?? null) ?? "null")`.
   - The program calls the serialisation's `then` before the expression's
     function runs, and adopts the function's Promise through
     `Promise.prototype.then` as it is then. So an expression that replaces
     `then` with one that never calls back runs into the attempt limit
     (`TestReplacedThenOnEngine`).
   - The expression's function runs once every named step has settled. A
     value that `JSON.stringify` gives no text for becomes `null`
     ([JE-15](../../docs/semantics/expressions.md#je-15)).
   - The prefetch is `__loadResults(<list>)` at a site with a results
     context when the text names a step. The list is a JSON array of
     `Prefetch`'s literals, written as serde_json writes strings. Anywhere
     else the prefetch is `Promise.resolve()`.
   - The parts sit on `Source`'s first and last lines, with nothing between
     them. So the code's first line starts at column `n + 40` in expression
     mode and `n + 33` in statement mode, where `n` is the prefetch's length
     (17 for `Promise.resolve()`).

### One kept value per step

The prefetch keeps each step's outcome, so every read of a named step gives
the same value. `results.a === results['a']` is `true`, and a write through
one read shows through the other. A step whose fetch failed fails the
evaluation only where it is read: `true ? 1 : results.f` gives `1`.

The kept values are parsed before the expression's code runs. So among the
results reads, a replacement of `JSON.parse` in the code reaches only a
computed read of a step that no static read names. It also reaches the
`resource` and `flow_user_state` helpers, which parse when they run.

### Stack frames

The preludes take the intrinsics that they call later
(`Promise.prototype.then`, `Reflect.apply`, and the `JSON.parse` that
`variable` and the conversion of the bindings use) before the user's code
runs. A stack frame in an error message gives a position in these scripts, so their
layout is observable
([JE-21](../../docs/semantics/expressions.md#je-21)). The layout reproduces
the frames that the oracle recorded on Windmill CE 1.862.0:

- `<anonymous> (eval_script:1:1)` and
  `<anonymous> (eval_script:1:<58 + length>)` for an unknown name that a
  one-line expression with no step to prefetch reads. The second frame is
  the arrow function that calls the expression's function.
- `stringify (native)`, called from `eval_script:1:94`, for a
  three-character expression.
- `variable (eval_script:7:50)`.
- `__throwOrParse (eval_script:5:54)` with
  `__getResult (eval_script:11:59)`, the frames that a kept error keeps.

### The depth check

Before the scripts run, each bound context value is checked for depth
([JE-49](../../docs/semantics/expressions.md#je-49)).

- **The order** follows Windmill's: the context values that the text
  names; `previous_result` when only `error` or a spelling of the previous
  id bound it; `flow_input`; then each member of `flow_env`. Within the
  context values and within `flow_env`, Windmill's order is a hash map's;
  windmill-lite takes the scope's order and the text's
  ([Decisions](#decisions-where-the-reference-is-silent)).
- **The failure.** The first value nested deeper than 127 levels fails with
  `recursion limit exceeded at line <l> column <c>`, at the position of the
  bracket that goes past the limit. No JavaScript runs.
- **The position** counts in the text that serde_json reads there: a
  context value's and a `flow_env` member's own text, as the scope holds
  it. For `flow_input` it counts in the object written out again
  compactly, its keys escaped as serde_json escapes them and its members'
  texts copied in, so that the keys before the deep member count.
- **No scan** is made of a text with no more than 127 opening brackets
  (`[` and `{`).
- `TestJE49OnEngine` pins the order and the positions.

## The engine boundary

The `Engine` interface keeps the backend replaceable: the semantics stay in
this package, and the engine knows nothing about flows.

- **Bindings.** An evaluation binds context values as host strings, which
  the program parses itself.
- **Preludes.** Programs that the evaluation runs, in order, before the
  program, on its context. Their top-level lexical declarations are
  `results` and `__loadResults` (`const`) and `error` (`let`), and the
  user's code sees all three. `results` and `error` are the names that
  Windmill's expressions use; `__loadResults` is the prefetch that the
  program calls.
- **A prelude that throws** ends the evaluation, and the program does not
  run.
- **Host functions.** These cover the results lookups and the helpers. Each
  returns text that resolves a Promise, or an error that rejects it.
- **Result.** The program's completion value is a string. The engine returns
  it as it converts it to UTF-8, an unpaired surrogate in its three-byte
  form; anything else is `ErrNotString`. Serialisation
  (`JSON.stringify(value ?? null)`) is the program's job, and reading the
  completion back is the pipeline's.
- **Jobs.** Every queued job runs, the jobs that those jobs queue included,
  before the engine reads the completion value. A job that throws is
  dropped ([JE-51](../../docs/semantics/expressions.md#je-51),
  `TestJobQueue`). A chain of jobs that never ends reaches the attempt
  limit.
- **Errors.** `Exception` keeps what was thrown: an `Error`'s name, message
  and stack, and whether it has a message and a stack; another value's kind
  and payload. QuickJS-ng reports running out of memory as an
  `InternalError` whose text is exactly `out of memory`, or as a bare `null`
  or `undefined` throw. Windmill treats all three as out of memory
  ([JE-3](../../docs/semantics/expressions.md#je-3)).
- **Message texts** belong to the pipeline, which reads the limits that they
  quote from `Engine.Limits`.

### Limits

`DefaultLimits` gives Windmill's limits:

| Limit | Value | Rule |
|---|---|---|
| `MemoryMB` | 128 (MiB per runtime) | [JE-3](../../docs/semantics/expressions.md#je-3) |
| `Attempt` | 20 s per attempt | [JE-2](../../docs/semantics/expressions.md#je-2). An attempt that reaches it is interrupted, where Windmill leaves the evaluation running: a [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations) |
| `StackBytes` | 1 MiB | QuickJS-ng's built-in default, which Windmill's embedding never changes ([JE-48](../../docs/semantics/expressions.md#je-48)) |

The native engine scales the stack limit to the C stack that one call takes
in its build. A simple recursion then fits about the same depth on every
build measured ([internal/expr/quickjs](quickjs/README.md#stack-limit)).

### Deviations from the engine

Besides the interrupted attempt, two more
[named deviations](../../docs/design/0001-equivalence-contract.md#5-named-deviations)
come from the engine's build, not from this program:

- **`Math` digits.** A transcendental `Math` function gives what the C
  library of the engine's build gives. On a C library other than the glibc
  of Windmill's image, its last digit can differ
  ([JE-1](../../docs/semantics/expressions.md#je-1)).
- **Recursion depth.** A recursion fits deeper than on Windmill, because
  the engine scales its stack limit to its build's frames
  ([internal/expr/quickjs](quickjs/README.md#stack-limit),
  [JE-48](../../docs/semantics/expressions.md#je-48), `TestJE48OnEngine`).

## Errors and their texts

The engine's errors become Windmill's texts: the `*EvalError` message that
each site then wraps
([JE-23](../../docs/semantics/expressions.md#je-23) to
[JE-35](../../docs/semantics/expressions.md#je-35)).

| When | Message | Cause (`Err`) |
|---|---|---|
| A thrown `Error` of any class, or a source that does not compile | `QuickJS evaluation error: Error:`, then a space and the message when there is one, then a newline and the stack when there is one ([JE-21](../../docs/semantics/expressions.md#je-21)) | the `*Exception` |
| Another thrown value | `QuickJS evaluation error: Exception generated by quickjs: <debug form>` and a newline ([JE-21](../../docs/semantics/expressions.md#je-21)) | the `*Exception` |
| A `null` or `undefined` throw, or `InternalError: out of memory` | ``The expression evaluation exceeded the memory limit of <N> MB. …`` ([JE-3](../../docs/semantics/expressions.md#je-3)) | `ErrOutOfMemory` |
| The attempt's time limit, three times | ``The expression evaluation `<trimmed text>` took too long to execute (>20000ms)`` ([JE-2](../../docs/semantics/expressions.md#je-2)) | `ErrAttemptTimeout` |
| A bound value nested deeper than 127 levels | `recursion limit exceeded at line <l> column <c>` ([JE-49](../../docs/semantics/expressions.md#je-49)) | `ErrTooDeep` |
| A helper call that Windmill's rewrite panics on ([JE-11](../../docs/semantics/expressions.md#je-11)), when no shortcut answers | `task <n> panicked with message "assertion failed: self.is_char_boundary(idx)"`, the runtime's report of the panicked task | `ErrRewritePanic` |

- `<N>` and the time limit come from the engine's `Limits`
  (`TestErrorTexts`; `TestJE3OnEngine` pins the memory limit's text on the
  engine).
- In the panic report, Windmill's `<n>` differs from run to run;
  windmill-lite numbers its panicked rewrites from 1. Windmill's runtime
  catches the panic, and the site handles the failure as any failed
  evaluation. The oracle recorded such a failure end to end
  ([Q-JE-13](../../docs/semantics/expressions.md#q-je-13)); the recording
  masks the message, whose task number differs between runs.
- `Eval` returns some errors without an `*EvalError`: `ErrNotCompiled`,
  the context's cause when the context ends, and an engine's error that is
  not about the program, such as a closed engine's. A prelude that does not
  compile comes back as `expr: compiling a prelude: <the engine's error>`.
- An engine's `ErrNotString`, and a completion that is not UTF-8, give the
  value `null` with no error (`TestCompletionReadBack`).

### Retries after a time limit

The engine's evaluation of the program is tried again when its failure is
an `*EvalError` whose message contains `took too long`, the time limit's
text or the user's own ([JE-2](../../docs/semantics/expressions.md#je-2)):

1. Each attempt runs the program, and is interrupted at the attempt limit,
   where Windmill leaves it running: a
   [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
2. A success, or a failure without that text, ends the evaluation.
3. Otherwise the pipeline pauses 5 s and tries again, up to three attempts
   in all. A context that ends during a pause ends the evaluation with its
   cause (`TestTimeoutRetried`, `TestJE2OnEngine`).

The shortcuts, the Go answers, the depth check and a rewrite panic are
never retried, and neither is an error without an `*EvalError`.

### Reading a thrown value back

- **An `Error`'s message and stack** are read as Windmill's embedding
  (rquickjs 0.11.0) reads them, in that order, and converted to strings.
  Each is absent when the property is `null` or `undefined`, when reading
  or converting it throws, or when the text is not UTF-8
  ([JE-52](../../docs/semantics/expressions.md#je-52)). A message that holds
  an unpaired surrogate is not UTF-8.
- **Nothing else read back runs the program's code.** An `Error`'s name,
  which the memory check needs, comes from data properties only.
- **A value that is not an `Error`** is not converted unless it is a
  string, a number or a boolean. So a `toString`, `Symbol.toPrimitive` or
  Proxy trap is never called.
- **A BigInt's decimal text** is never written: it can take longer to write
  than the attempt has left. `TestReadbackRunsNoCode` and
  `TestReadbackOfALargeBigInt` pin the read back.

### The debug form of a thrown value

A thrown value that is not an `Error` is written in its debug form: the
type name and payload that rquickjs prints (`debugform.go`).

| Value | Debug form |
|---|---|
| A string | `String(Ok("s"))`, with Rust's quoting and escapes |
| A string that is not UTF-8 | `String(Err(Utf8(Utf8Error { valid_up_to: <n>, error_len: Some(<k>) })))`, or `error_len: None` when the string ends inside a sequence |
| A number that the engine holds as a 32-bit integer | `Int(1)` |
| Any other number | `Float(1.5)`, `Float(1e300)`, `Float(NaN)`, in Rust's notation |
| A boolean | `Bool(true)` |
| A BigInt | `BigInt"BigInt"` |
| A symbol, an array, a constructor, another function, a promise, a proxy, or another object | Its type name and the engine's address, as in `Object(0x…)`, which changes from run to run |

`TestDebugFormMatchesRust` and `TestErrorTexts` pin the forms. Rust's float
notation writes the shortest digits that read back. A tie
between two is broken upward, as in `1000000000000000.3` for
`1000000000000000.25`. A whole number is written as `1.0`, and an exponent
is used from 1e16 and below 1e-4.

## Decisions where the reference is silent

| Question | The code's choice | Why | Pinned by |
|---|---|---|---|
| When does the previous step's read take the `previous_result` global? | As it is when the read resolves: before the code runs for a static read, which the prefetch resolves, and when the read runs for a computed one | The reference says that the read returns the global, not when it is read | Code only (`program.go`, the results prelude); `TestKeptReads` pins that the read gives the global itself |
| Does a read of a kept step give the kept object or a copy? | The kept object: every read of a step gives the same value | The reference says that the outcome is kept, not which object a read gives | `TestKeptReads`, `TestResultsProxy` |
| Is a read right after a bracket read's `]`, as in `results["a"]results.b`, a static read? | Yes: `]` is a character outside `[A-Za-z0-9_$]` | The reference does not say whether the character before `results` may end the previous read | The pipeline case `adjacent-reads` of `testdata/pipeline/je-12.json`, run by `TestPipelineCases` |
| Which value does the depth check report when several are too deep? | The first in the order of [The depth check](#the-depth-check). Within the named context values that order is the scope's, and within `flow_env` its text's | Windmill iterates hash maps within the context values and within `flow_env`, whose order the reference cannot fix ([JE-49](../../docs/semantics/expressions.md#je-49)) | `TestJE49OnEngine` |
| Which characters does a debug form's string escape? | `\0`, `\t`, `\r`, `\n`, `\\`, `\"`, grapheme extenders, and every character outside Go's Unicode categories L, M, N, P and S other than the space | Rust uses its own Unicode tables; Go's (Unicode 17) stand for them. A later Unicode version on either side could differ on new characters | `TestDebugFormMatchesRust` over `testdata/debugform.json` |

The tables were compared once: Go's tables and rustc 1.94.0's agreed on
every Unicode scalar value, and the float notation agreed on 270 588
values. That is a measurement. `testdata/debugform.json` keeps 239 floats
and 1,002 strings of it, texts that rustc printed, which
`TestDebugFormMatchesRust` compares with this package's output.

## Pipeline cases

The pipeline's cases are in `testdata/pipeline/<finding>.json`: one file per
finding, named after it in lower case. Every case states what `Compile`
makes of the text. There are three kinds of case:

- a restated case gives `recorded`, a case that the oracle recorded. It
  must also give the route that answers on a fake engine, and a shortcut's
  text;
- a case of its own gives `context`, its own scope, and may give the route,
  the texts and what the real engine gives;
- a compile-only case gives neither, and checks only `Compile`.

### The case format

A file holds `finding` and `cases`. Each case has these fields:

| Field | Meaning |
|---|---|
| `spec`, `variant` | The catalogue case, or the finding with a variant for a case beyond the catalogue |
| `provenance` | The restated case's provenance, or `source-derived` for a case of the pipeline's own |
| `title`, `note` | What the case shows (required), and a free note |
| `recorded` | The recorded case that it restates: `exprs` and `case` (a file of [conformance/exprs](../../conformance/README.md) and its case's name), or `fixture`, `run`, `at` and `input` (a unit case of a flow fixture) |
| `site`, `text` | The site and the expression as written; for a restated case, they must equal the recording's |
| `context` | For a case of its own: `previous_id`, `previous_result`, `flow_input`, `flow_env`, and `results` (stored texts by id) |
| `expect` | What the pipeline must give (the next table). A case checks only the fields that it gives, except that a restated case must give `route`, and `text` when a shortcut answers |

| `expect` field | Meaning |
|---|---|
| `trimmed`, `shortcut`, `mode`, `binds` | What `Compile` gives |
| `code` | The rewritten code inside the wrapper |
| `prefetch` | The literals of `Prefetch` |
| `rewrite_panic` | The byte offset where the helper rewrite panics, for a text with no code |
| `route` | `engine`, or a shortcut's name: `context_name`, `flow_key`, `previous_id`, `results_path`. Not on a compile-only case |
| `text` | A shortcut's exact answer. Not on a compile-only case |
| `bound` | The engine's host strings. Not on a compile-only case |
| `results_lookup` | Whether the engine's call binds the results lookup. Not on a compile-only case |
| `engine` | Only on a case with its own `context`: what the real engine gives, the evaluator's exact `text`, its exact `error` (where `<n>` stands for a number that differs between runs), or an `error_prefix` |

### How a case's scope is built

- **Provenance.** A case of its own and a compile-only case have
  provenance `source-derived`.
- **A restated case's scope** is rebuilt from the recorded case's context.
- **A context-free case** is at module 0. Its previous id is `""`, and
  `previous_result` and `flow_input` are the flow input as Windmill's worker
  holds it: a compact object whose members are each in jsonb's printed form
  ([JE-44](../../docs/semantics/expressions.md#je-44)'s table).
- **A unit case** supplies its previous id, `previous_result`'s text and the
  database's texts directly. It gives its flow input as submitted, which
  becomes the worker's text in the same way.
- **A shortcut's answer** is held to the recorded expectation: the exact
  evaluator text of a unit case, or a context-free case's recorded argument
  as its stored form.

### Add a case

1. Open `testdata/pipeline/<finding>.json` for the finding, in lower case,
   or create it with `finding` and `cases`.
2. Add a case with `spec`, `provenance`, `title`, `site` and `text`. A
   `spec` that names only the finding needs a `variant` too.
3. Choose the kind of case:
   - for a case that the oracle recorded, give `recorded`, the recording's
     `site`, `text` and `provenance`, and in `expect` the `route` and, for a
     shortcut, its `text`;
   - for a case with a scope of its own, give `context` and provenance
     `source-derived`, and in `expect` the route and texts that matter, and
     `engine` when the real engine's answer matters;
   - for a compile-only case, give neither, provenance `source-derived`, and
     in `expect` only what `Compile` makes of the text (no `route`, `text`,
     `bound` or `results_lookup`).
4. Give in `expect` what `Compile` must make of the text: any of `trimmed`,
   `shortcut`, `mode`, `binds`, `code`, `prefetch` and `rewrite_panic`.
5. Run `go test -run '^TestPipelineCases$|^TestPipelineCasesOnEngine$|^TestPipelineCoverage$' ./internal/expr`.

## Tests

### Run the tests

The tests link [internal/expr/quickjs](quickjs/README.md), which builds
QuickJS-ng through cgo, so they need a C compiler.

1. Run the package and `internal/expr/quickjs` with the race detector, as
   CI's `Test` step runs every package: `go test -race ./internal/expr/...`.
2. Run it in leak check, as CI's `Test the expression packages in leak
   check` step does: `WL_QJS_LEAK_CHECK=1 go test -race -count=1 ./internal/expr/...`.
3. Run it with `jsExpr` off, as the `Expression engine` jobs do (CI's
   `Go (ubuntu-latest, QuickJS only)` part runs the same package with
   `cmd/wltest`): `WL_TEST_QUICKJS_ONLY=1 go test -race -count=1 ./internal/expr`.
4. Print the budget gate's table, as CI's `Report the expression budget
   gate` step does:
   `WL_EXPR_BUDGET=report WL_EXPR_BUDGET_SECONDS=1 go test -count=1 -run '^TestBudgetGate$' -v ./internal/expr`.

[docs/ci.md](../../docs/ci.md#the-go-jobs) says which job runs each step,
and on which compilers and C libraries.

### Environment variables

| Variable | Value | Tests that read it | When it is unset |
|---|---|---|---|
| `WL_TEST_QUICKJS_ONLY` | `1` | Every test: each `Pipeline` that the tests make starts with `jsExpr` off (`TestQuickJSOnlyTestMode` checks it). Tests that set the mode themselves still run both ways | `jsExpr` is on |
| `WL_QJS_LEAK_CHECK` | `1` | Every test that starts the real engine through the registration: it runs in leak check, so the check that no heap block leaked counts blocks | The engine frees each heap whole and counts no leaks, unless it was built with the `wl_libc_malloc` tag, which always runs leak check |
| `WL_EXPR_BUDGET` | `gate` or `report` | `TestBudgetGate`: `gate` fails a paced row past its bounds; `report` prints the table | `TestBudgetGate` skips |
| `WL_EXPR_BUDGET_SECONDS` | Seconds | `TestBudgetGate`: the length of each row | 3 s |
| `WL_JSEXPR_CASES` | A positive number | `TestJSExprsMatchEngine`: the number of generated expressions | 6,000, or 1,500 with `-short` |
| `WL_JSEXPR_SEED` | A number | `TestJSExprsMatchEngine`: the generator's seed | 7 |

### Fuzz tests and benchmarks

| Name | What it checks | Command |
|---|---|---|
| `FuzzCompile` | The textual stages on any text | `go test -run '^$' -fuzz FuzzCompile ./internal/expr` |
| `FuzzJSONText` | The JSON scanners | `go test -run '^$' -fuzz FuzzJSONText ./internal/expr` |
| `FuzzJSExprMatchesEngine` | Wherever `jsExpr` answers, the engine gives the same text | `go test -run '^$' -fuzz FuzzJSExprMatchesEngine ./internal/expr` |
| `BenchmarkEvalMemberPath` | Three member paths through `Eval` | `go test -run '^$' -bench BenchmarkEvalMemberPath ./internal/expr` |
| `BenchmarkEvalJSExpr` | A template, arithmetic and a predicate through `Eval`, with `jsExpr` on and off | `go test -run '^$' -bench BenchmarkEvalJSExpr ./internal/expr` |

- `go test` runs each fuzz test's seeds and its corpus in `testdata/fuzz`
  without `-fuzz`.
- `FuzzJSExprMatchesEngine` is seeded from the generator and from every
  expression of the conformance suite with its scope (`conformance/exprs`
  and the flow fixtures' unit cases). Its corpus holds a text that is not
  UTF-8, which fuzzing found.
- A fuzzed case's first byte is its site. A `SiteKind` replays at that
  site, and any other byte maps onto one, so the fuzzer reaches every site,
  each with its scope as the conformance cases give it.
- A longer sweep of the generated expressions runs by its variables:
  `WL_JSEXPR_CASES=30000 WL_JSEXPR_SEED=3 go test -run '^TestJSExprsMatchEngine$' ./internal/expr`.

### The budget gate

`TestBudgetGate` measures the budget gate of the ADR
[Where semantics live](../../docs/design/0003-semantics-placement.md) through
the whole pipeline, on the real engine with the default pool.
It evaluates a `skip_if` predicate whose results read goes through a test
`Results` that returns a synthetic value of each size from memory, so the
time is the pipeline's and the engine's, with no database. It runs 8
concurrent callers. Each caller
issues its next evaluation 0.5 ms after the previous one returns.

| Results value | p95 bound | p99 bound |
|---|---|---|
| 4 KiB | 500 µs | 1.5 ms |
| 64 KiB | 2 ms | 6 ms |
| 256 KiB | 6 ms | 20 ms |

With `WL_EXPR_BUDGET=gate`, the test fails a paced row past these bounds,
which hold on the benchmark machine only. With `report`, it prints the
table, as CI does on its shared runners and from the static musl engine. It
also reports back-to-back load, and the paced load with the read step as the
previous step, beside the gate.

### The real engine in the tests

Package `expr` cannot import [internal/expr/quickjs](quickjs/README.md),
which imports it. So `quickjs_test.go`, of the external test package
`expr_test` in the same test binary, registers the engine for the tests
that need it. An engine that a test starts through it must end with no
leaked heap block; the tests that make QuickJS-ng leak opt out (below).

The engine counts leaked blocks only in leak check (`WL_QJS_LEAK_CHECK=1`).
`TestJSExprKeepsTheHeapCap` drives the engine out of its cap on strings
with characters that are not ASCII, where QuickJS-ng's `JS_ToCStringLen2`
leaks the string that it was converting. That test, and
`TestJSExprDeclinesAFailedPrefetch`, therefore use `realLeakyEngine`, which
logs leaks instead of failing.

### Test files

Every test file builds into one test binary, which links the real engine,
so every run needs cgo and a C compiler. The "Engine" column says which
engine a file's tests evaluate on.

| File | Engine | What it checks |
|---|---|---|
| `expr_test.go` | none | The predicate sites (`TestPredicateSites`), the sites' names (`TestSiteKindText`), `DefaultLimits` (`TestDefaultLimitsAreWindmills`), and the JSON form of `Expr` (`TestExprJSON`) |
| `compile_test.go` | none | The wrapper's lines ([JE-14](../../docs/semantics/expressions.md#je-14), `TestWrapperSingleLine`), the program's layout (`TestProgramLayout`), the predicate wrapping (`TestPredicateWrapping`), the IR's JSON form (`TestCompiledExprJSON`); `FuzzCompile` |
| `pipeline_test.go` | fake | On a fake engine: `TestEvalChecksContextFirst`, `TestEvalNeedsACompiledExpr` (`ErrNotCompiled`), `TestShortcutCanceled` (a context that ends during the results path's lookup), `TestCompileErrorRaisedAtEval`, `TestCompileFailureNotKept`, `TestPrepareCompilesEachSourceOnce`, `TestEngineErrorsBecomeWindmillTexts`, `TestCompletionReadBack`, `TestTimeoutRetried`, `TestResultsLookupFromMemory`, `TestResultsLookupErrors`, `TestHelpers`, and `TestConcurrentEval` under `-race` |
| `conformance_test.go` | fake and real | `TestPipelineCases` runs every pipeline case. `TestPipelineCasesOnEngine` runs on the real engine every case that gives `expect.engine`. `TestPipelineCoverage` and `TestRecordedCasesThroughPipeline` are described below. `TestJE8KeyOrderExample` checks the key-order example of the ADR [Where semantics live](../../docs/design/0003-semantics-placement.md) |
| `engine_suite_test.go` | real | `TestE3aOnEngine`: every recorded expression case on the real engine (below) |
| `program_test.go` | real | The program on the engine, case by case from the findings (below), and `TestRecordedFramesOnEngine` |
| `debugform_test.go` | none | `TestDebugFormMatchesRust` holds the float and string notation to `testdata/debugform.json`, texts that rustc printed, among them floats whose two shortest digit strings tie. `TestDebugFloat` pins the presentation rules on named values |
| `memberpath_test.go` | real | `TestMemberPathsMatchEngine`, `TestMemberPathPipeline`, `TestMemberPathHelperCallTexts`, `TestMemberPathLookupKeepsTheAttemptLimit` (below) |
| `pathexpr_test.go` | real | `TestPathExprsMatchEngine`, `TestParsePathExpr`, `TestKeptReads`, and `TestSkipChecked` for the scanner's skip of a checked value |
| `jsexpr_test.go` | real; `WL_JSEXPR_CASES`, `WL_JSEXPR_SEED` optional | `TestParseJSExpr` (what the grammar takes and refuses), `TestJSExprsMatchEngine` (below), and `TestJSExprReadMemoKeys`: reads that differ only in a key's spelling never share a memo entry |
| `jsexpr_limits_test.go` | real | `TestJSExprKeepsTheHeapCap`, `TestJSExprKeepsTheStackLimit`, `TestJSExprKeepsTheAttemptLimit`, `TestJSExprBoundsItsOwnWork`, `TestJSExprWorstCaseTime`: `jsExpr` keeps the evaluation's limits. `TestJSExprDeclinesAFailedPrefetch`, and `TestJSExprIsolation`: evaluations share nothing |
| `jsexpr_order_test.go` | real | `TestJSExprKeepsReadOrder` and `TestBindInlinesMemoryLookups` (below) |
| `jsexpr_race_test.go` | real | `TestJSExprConcurrentPipeline`: 16 goroutines on one pipeline, under `-race`, every outcome held to QuickJS's for the same scope. `TestInlineLookupsLetHelpersSettle` (below) |
| `jsexpr_fuzz_test.go` | real | `FuzzJSExprMatchesEngine`, and `TestFuzzSeedsKeepTheirSites`: every conformance seed replays at its recorded site, with that site's wrapping, results lookup and context names |
| `jsontext_test.go` | none | `TestMember`, `TestWalkAgainstReference` (the member lookup and the `#>` walk against an `encoding/json` implementation, on generated values), `TestDatabaseText` (jsonb's text output on fixed cases; `TestWalkAgainstReference` covers it on generated values); `FuzzJSONText` |
| `budget_test.go` | real; runs only with `WL_EXPR_BUDGET` | `TestBudgetGate` ([The budget gate](#the-budget-gate)) |
| `testmode_test.go` | real | `TestQuickJSOnlyTestMode`: `WL_TEST_QUICKJS_ONLY` sets the pipelines' mode |
| `bench_test.go` | real | The benchmarks |
| `quickjs_test.go`, `fake_test.go`, `export_test.go` | — | The real engine's registration, the fake engine, and test helpers |

**The recorded cases.**

- `TestPipelineCoverage` checks that every recorded case of the textual
  stages and the shortcuts
  ([JE-5](../../docs/semantics/expressions.md#je-5) to
  [JE-14](../../docs/semantics/expressions.md#je-14),
  [JE-V6](../../docs/semantics/expressions.md#je-v6)), and of a results
  path read out of a multi-line text
  ([CF-V6](../../docs/semantics/conformance.md#cf-v6)), is restated exactly
  once. Every catalogue case of those findings that a recorded fixture or
  run cites must be the spec of a restatement. `TestPipelineCases` checks
  that each restatement gives its route and a shortcut's text.
- `TestPipelineCasesOnEngine` includes the catalogue cases that no
  recording holds yet (`conformance/exprs/deferred.yaml` names them), and
  those that flow fixtures recorded end to end. Each is held to its exact
  text or message, except two held to an `error_prefix`.
- `TestRecordedCasesThroughPipeline` runs every recorded context-free case
  and unit case, of every finding, on the fake engine. Wherever a shortcut
  answers, the answer must be what the oracle recorded. A unit case whose
  rewrite panics has no shortcut to check. A flow fixture's expressions that
  no unit case restates are not run.
- `TestE3aOnEngine` runs every recorded expression, the context-free cases
  and the flow fixtures' unit cases, through the pipeline on the real
  engine, with the context that the recording gives. It holds each to its
  whole expectation: a value compared through its stored form, an exact
  evaluator text, a predicate's skip, or an error, wrapped as its site
  wraps it.
- Each evaluator error in `TestE3aOnEngine` must also stand in a message
  that the oracle recorded, byte for byte, stack included: at its end, or
  before a Rust location suffix.
- Two kinds of case in `TestE3aOnEngine` differ by a
  [named deviation](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
  The transcendental `Math` cases of
  [JE-1](../../docs/semantics/expressions.md#je-1) accept the oracle's value
  or one unit in the last place from it, and log every difference. The
  recursion of depth 1,000 gives `null`, because the engine does not
  overflow there ([JE-48](../../docs/semantics/expressions.md#je-48)).
- `TestE3aOnEngine` does not run two cases. The expression that runs into
  the time limit takes about 70 s for its three attempts, so
  `TestJE2OnEngine` runs it with a short limit
  ([JE-2](../../docs/semantics/expressions.md#je-2)). The rewrite panic's
  report names a task number that differs between runs, so its recording
  masks the message; a pipeline case of `testdata/pipeline/je-11.json`
  holds the evaluator's text
  ([JE-11](../../docs/semantics/expressions.md#je-11)).

**The program on the engine** (`program_test.go`) holds one test per rule.
Each is also named beside its rule in the sections above: `TestSerialisation`,
`TestReplacedSerialisation`, `TestReplacedThenOnEngine` (a replaced `then`),
`TestErrorTexts`, `TestJE2OnEngine`, `TestJE3OnEngine`, `TestErrorHelper`,
`TestResultsProxy`, `TestHelpersOnEngine`, `TestBindings`,
`TestJE49OnEngine`, `TestJE48OnEngine`, `TestStrictMode`, `TestJobQueue`,
`TestJE11OnEngine`, `TestReadbackRunsNoCode`, `TestReadbackOfALargeBigInt`
and `TestJE8KeyOrderOnEngine`. Also:

- `TestRecordedFramesOnEngine` holds the evaluator's text to the oracle's
  recording for flow runs whose expression error the flow suite does not
  require: `execution/fx-49` run `throws` (a helper's await before the
  throw) and `oracle/key-order` run `retry_keys`.

**The Go answers against the engine.**

- `TestMemberPathsMatchEngine` reads thousands of generated values and paths
  through `memberPath` and through the program on the real engine. Wherever
  `memberPath` answers, the engine must give the same text. The values hold
  repeated, integer-like and inherited keys, escapes, unpaired surrogates,
  edge numbers, and text that is not JSON. The paths read missing members,
  indexes past the end and `length`, and chain optionally over `null` and
  `undefined`.
- `TestMemberPathsMatchEngine` also checks the list of
  `Object.prototype`'s names against the engine.
- `TestPathExprsMatchEngine` does the same for null tests and object
  literals of paths: each operator against `null` and `undefined`, alone or
  choosing between paths, and literals with repeated, integer-like and
  quoted keys.
- `TestJSExprsMatchEngine` evaluates 6,000 generated expressions per run
  (1,500 with `-short`) through `jsExpr` and on the real engine. Wherever
  `jsExpr` answers, the engine must give the same text; where the engine
  fails, `jsExpr` must decline. The generator covers templates, arithmetic,
  comparisons, predicates, short-circuits, keys that spell like syntax
  (`?b`, `a.b`, `[`), failing lookups and every kind of decline.
- `TestMemberPathPipeline` checks that a member path over a bound value or
  the previous result does not reach an engine that says it is exact, that
  one that needs the results lookup does, and that any other engine gets
  them all. The lookup is read once, with the same error either way. It
  runs with `jsExpr` on and off.
- `TestMemberPathHelperCallTexts` checks that a text that the helper
  rewrite changes stays the engine's: each helper spelling, in quoted keys
  and results ids, rewritten into another key or into a syntax error.
- `TestMemberPathLookupKeepsTheAttemptLimit` has a lookup slower than the
  attempt limit fail with the limit's error, with member paths on or off.
- `TestKeptReads` checks the prefetch's kept outcomes on the real engine:
  one lookup per named step, one value per step, a kept error thrown only by
  the reads of its step, and the previous step read from memory.
- `TestJSExprBoundsItsOwnWork` holds the process's CPU time to its bound,
  not the wall clock, so the time that an evaluation waits for a CPU while
  other processes run does not count. It takes the least CPU time of up to
  three runs.
- `TestJSExprKeepsReadOrder` runs sequences of expressions with `jsExpr` on
  and off, over a lookup that reads memory (answered inline) and one that
  does not (on goroutines). It compares the four runs' helper calls and
  outcomes in order, and each expression's reads as a set, since the
  prefetch resolves them at once.
- `TestBindInlinesMemoryLookups` checks that only a memory lookup is marked
  `Inline`, through the read memo too.
- `TestInlineLookupsLetHelpersSettle` races a helper call against an
  endless loop of results lookups, computed reads that no prefetch answers,
  through the pipeline. The helper's answer wins within a second, with
  memory lookups (inline) and with goroutine ones.

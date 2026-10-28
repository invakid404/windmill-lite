# internal/expr

**Current state:** the contracts of milestone K1, the textual pipeline
and evaluator of milestone B2a, and their run on the real engine of
milestone B2b
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)). This README
covers `internal/expr` and its `testdata/`, but not its `quickjs/`
directory. `Compile` runs the textual stages of an expression, and
`Pipeline` is the evaluator: it answers with a shortcut, or runs the
expression's program on an `Engine`, the QuickJS-ng backend of milestone
B1a in [internal/expr/quickjs](quickjs/README.md): Windmill's preludes,
the expression in its wrapper, the serialisation of its value, and
Windmill's error texts. Nothing outside this package calls it yet.
[internal/ir](../ir/README.md) still writes each expression with only its
site and text; the engine milestone N1a decides whether the IR or the
engine, when a bundle opens, calls `Compile`, and gives the evaluator its
scopes.

## Contracts

| Name | What it is | Implemented by |
|---|---|---|
| `SiteKind` | the expression sites of [JE-22](../../docs/semantics/expressions.md#je-22) (the suspend site is left out, because `suspend` is rejected at load); `Predicate` marks the boolean sites of [JE-24](../../docs/semantics/expressions.md#je-24) | this package |
| `Expr`, `Shortcut`, `Mode` | a compiled expression as the IR carries it ([ADR 0002](../../docs/design/0002-ir-and-loader.md)): the text as written, the trimmed text, the shortcuts its text allows ([JE-6](../../docs/semantics/expressions.md#je-6) to [JE-9](../../docs/semantics/expressions.md#je-9)), the mode ([JE-13](../../docs/semantics/expressions.md#je-13)), the bound names ([JE-10](../../docs/semantics/expressions.md#je-10)) and the engine source; its JSON form (snake-case names, the site and mode by name, empty parts omitted) is the one the IR's `ir.json` holds | `Compile` (B2a), from the site and the text that [internal/ir](../ir/README.md) sets |
| `Engine`, `Program`, `Input`, `Exception`, `Limits` | the JavaScript engine behind a Go interface ([ADR 0003](../../docs/design/0003-semantics-placement.md)): compile once, evaluate on a fresh runtime with host strings, host functions and preludes (global scripts that run first, B2b), one attempt's limits | B1a: [internal/expr/quickjs](quickjs/README.md) |
| `Evaluator`, `Scope`, `Results`, `Helpers` | what the flow interpreter calls at an expression site: the scope of [JE-22](../../docs/semantics/expressions.md#je-22), the results lookups of [JE-18](../../docs/semantics/expressions.md#je-18) and [JE-19](../../docs/semantics/expressions.md#je-19), and the `variable`, `resource` and `flow_user_state` helpers of [JE-20](../../docs/semantics/expressions.md#je-20) | `Pipeline` (B2a, B2b); the engine provides `Scope` and `Results`, and the platform milestones `Helpers` |
| `EvalError`, `ErrOutOfMemory`, `ErrTooDeep` | a failed evaluation with Windmill's text, and its cause | `Pipeline` (B2b) |

## The textual stages

`Compile(site, text)` depends on the site and the text alone; nothing in
it depends on a run.

1. **Wrap and trim** ([JE-5](../../docs/semantics/expressions.md#je-5),
   [JE-24](../../docs/semantics/expressions.md#je-24)). A predicate's text
   is wrapped as `Boolean(<text>)`, untrimmed. The result is trimmed of
   Unicode white space, as Rust's `str::trim` trims, which is
   `strings.TrimSpace`. `Text` keeps the text as written, which a site's
   error message quotes.
2. **Shortcuts the text allows.** Each fires or misses at evaluation time.
   - `ContextName`: the text is one of JE-6's context names.
   - `FlowKey`: the text starts with `flow_input.` or `flow_env.` (the key
     is everything after the first dot), or is `flow_input["<key>"]` or
     `flow_env["<key>"]` with a non-empty key (JE-7).
   - `PreviousID`: the id named by the text when the whole text is
     `results["<id>"]`, `results?.["<id>"]`, `results.<id>` or
     `results?.<id>`, the bracket forms read first (JE-8). Evaluation
     compares the whole text with the four spellings of the previous id, so
     a text that spells two ids fires for either.
   - `ResultsPath`: the first line that is, alone and from column 0,
     `results` or `results?`, a dot and a step id of `[A-Za-z0-9_]`, an
     optional `[<digits>]`, then `.segment` parts. `Declines` when its last
     segment is `length`, and later lines are not searched (JE-9).
3. **Mode** ([JE-13](../../docs/semantics/expressions.md#je-13)). Statement
   mode when the text starts with `return ` or `return;`, is `return`,
   starts with one of the listed keywords, or holds a `;` outside quotes. A
   quote that no backslash precedes opens a quote of its kind or closes the
   one it is in; the scan knows no comments or regular expressions.
4. **Bound names** ([JE-10](../../docs/semantics/expressions.md#je-10),
   [JE-17](../../docs/semantics/expressions.md#je-17)). `Binds` lists,
   sorted, the context names, `flow_input`, `flow_env`, `results` and
   `error` that the text holds as substrings, and `previous_result` when it
   holds `error`.
5. **Rewrites and wrapper.** Every `variable(`, `resource(` and
   `flow_user_state(` call is awaited, its end found by counting
   parentheses, strings included; a call that never closes is awaited to
   the end of the text
   ([JE-11](../../docs/semantics/expressions.md#je-11)). Then every match
   of `results`, an optional `?`, and `.<name>` or `["…"]` on one line is
   awaited ([JE-12](../../docs/semantics/expressions.md#je-12)). Neither
   knows words, strings or comments. `Source` is the result in the mode's
   wrapper, called at once: `(async function() { return <code>; })()` or
   `(async function() { <code> })()`. The wrapper's opening and closing
   text sit on the code's first and last lines, so a trailing `//` comment
   on one line comments the closing text out
   ([JE-14](../../docs/semantics/expressions.md#je-14)). The wrapper's
   text is observable in stack frames: the one above puts the code where
   E3a's recorded stacks place it, at column 28 in expression mode and 21
   in statement mode.

[Request 8](../../docs/design/README.md) to the semantics team lists the
details these stages decide where the reference is silent.

## Evaluation

`NewPipeline(engine)` returns the evaluator. `Prepare` compiles expressions'
sources on the engine when a bundle opens. A source that does not compile
is kept, and `Eval` raises its `*Exception` each time the expression reaches
the engine. A shortcut answers it without the engine all the same.
Programs are kept by source text, one bundle's worth.

`Eval(ctx, e, scope)` returns the context's cause if it has ended, and
`ErrNotCompiled` for an `Expr` that `Compile` did not fill. It then tries
the shortcuts in the order JE-6, JE-7, JE-8, JE-9. The first that fires
answers, unless the context ended while it read (JE-9's lookup), which
gives the context's cause:

| Shortcut | Fires when | Answer |
|---|---|---|
| JE-6 | the scope holds the context name | its text verbatim |
| JE-7 | the flow input (or env) has the key | the member's text verbatim, from the scope's text |
| JE-8 | the whole text is a spelling of the scope's previous id, and the scope has a results context and `previous_result` | `previous_result`'s text verbatim |
| JE-9 | the path does not decline and the scope has a results context | the path applied to the step's stored text as PostgreSQL's `#>` applies it, in jsonb's text output (a space after each `:` and `,`), or `null` for a missing step, key or index, a type mismatch or a null intermediate |

JE-9 also needs an authenticated client, but every site with a results
context has one ([JE-22](../../docs/semantics/expressions.md#je-22)), so the
results context alone decides. A shortcut's text is observable: the
`timeout` site's parse error quotes it, and E3a recorded jsonb's spaces in
JE-9's answer ([ADR 0005](../../docs/design/0005-json-step-boundaries.md)).
JE-6 to JE-8 answer the scope's texts as they are, so the form the engine
puts there for a value read from the database is observable too, and is
open (request 8).

When no shortcut fires, the source runs on the engine with these bindings
(JE-10):

| Binding | Kind | When |
|---|---|---|
| each context value (`previous_result`, `result`, `params`, …) | host string, its text | the text names it and the scope holds it; `previous_result` also when the text names `error` or spells the previous id (JE-8) |
| `flow_input` | host string: the flow input, or `null` | the text names it |
| `flow_env` | host string: the flow env, or `{}` | the text names it |
| `__wl_results` | host function | the text names `results` and the scope has a results context |
| `__wl_variable`, `__wl_resource`, `__wl_flow_user_state` | host functions | always |

Every host function answers JSON text:

- **The results lookup.** It answers the previous id with
  `previous_result`'s text from memory when `previous_result` is bound
  (JE-18 step 1, the reading [0001](../../docs/design/0001-equivalence-contract.md)
  §6 records for FX-V6). Any other id is a `Results.Lookup` database read,
  in stored text. A failed read rejects with the read's message.
- **The helpers.** They answer from `Helpers`. Without them they reject
  with JE-20's `… is not available without an authenticated client`, and
  `flow_user_state` without a results context with `flow_user_state() is
  not available in this context`.

The engine's text, `JSON.stringify`'s output, is returned as the value's
producer text. A failure is an `*EvalError` whose message is Windmill's
text (below); a context that ends during the evaluation gives its cause.

## The program on the engine

`program.go` writes what an evaluation runs (milestone B2b). An evaluation
runs these global scripts, each named `eval_script`, on one fresh context:

1. **The context prelude**, always. It parses each bound host string in
   place with `JSON.parse`, so a context value, `flow_input` and `flow_env`
   are properties of `globalThis` holding values that keep their text's
   key order, `-0` and a double's precision
   ([JE-10](../../docs/semantics/expressions.md#je-10),
   [JE-16](../../docs/semantics/expressions.md#je-16)). It defines
   `variable`, `resource` and `flow_user_state` on `globalThis`, each
   awaiting its host function, and removes the host functions. A helper
   whose host function rejects throws `new Error(<the API's text>)` from
   its own frame (JE-20).
2. **The results prelude**, when the evaluation binds the results lookup.
   It declares the lexical `results`: a proxy over an empty object, so
   that `JSON.stringify(results)` is `{}`
   ([JE-12](../../docs/semantics/expressions.md#je-12)), whose symbol keys
   give `undefined` and whose string keys give a Promise of the step's
   value (JE-18). A failed read throws the read's text from
   `__throwOrParse`, called from `__getResult`.
3. **The error prelude**, when the text mentions `error`. It declares the
   lexical `error`, derived from `previous_result` by
   [JE-17](../../docs/semantics/expressions.md#je-17)'s decision list.
4. **The program**: `Source`, then `.then((v) => JSON.stringify(v ?? null)
   ?? "null")` on its last line, so a value `JSON.stringify` gives no text
   for becomes `null`
   ([JE-15](../../docs/semantics/expressions.md#je-15)).

The preludes take the intrinsics they call later (`JSON.parse`,
`Promise.prototype.then`, `Reflect.apply`) before the user's code runs. A
stack frame in an error message gives a position in these scripts, so
their layout is observable (JE-21). It reproduces the frames E3a recorded:
`<anonymous> (eval_script:1:1)` and `<eval> (eval_script:1:<29 + length>)`
for an expression, `stringify (native)` called from `eval_script:1:64` for a
three-character expression, `variable (eval_script:7:50)`, and
`__throwOrParse (eval_script:5:54)` with `__getResult (eval_script:11:59)`.

Before the scripts run, each bound context value is checked for depth
([JE-49](../../docs/semantics/expressions.md#je-49)): `flow_input` as a
whole, compacted, its object a level; each other context value as bound;
each member of `flow_env` on its own. A value nested deeper than 127 levels
fails with `recursion limit exceeded at line <l> column <c>`, the position
of the bracket that goes past the limit, and no JavaScript runs. A text
with no more than 127 brackets is not scanned.

The engine's errors become Windmill's texts, the `*EvalError` message that
each site then wraps
([JE-23](../../docs/semantics/expressions.md#je-23) to
[JE-35](../../docs/semantics/expressions.md#je-35)):

| The engine reports | Message | Cause |
|---|---|---|
| a thrown `Error` of any class, or a source that does not compile | `QuickJS evaluation error: Error: <message>`, then a newline and the stack when there is one ([JE-21](../../docs/semantics/expressions.md#je-21)) | the `*Exception` |
| another thrown value | `QuickJS evaluation error: Exception generated by quickjs: <its string form>` and a newline (JE-21) | the `*Exception` |
| a `null` or `undefined` throw, or `InternalError: out of memory` | ``The expression evaluation exceeded the memory limit of <N> MB. …`` ([JE-3](../../docs/semantics/expressions.md#je-3)) | `ErrOutOfMemory` |
| the attempt's time limit, three times | ``The expression evaluation `<trimmed text>` took too long to execute (>20000ms)`` ([JE-2](../../docs/semantics/expressions.md#je-2)) | `ErrAttemptTimeout` |
| a completion value that is not a string (`JSON.stringify` replaced by the user's code) | `QuickJS evaluation error: the serialised value is not a string`, windmill-lite's own | `ErrNotString` |

`<N>` and the time limit are the engine's `Limits`. An evaluation whose
error text contains `took too long`, the time limit's or the user's own, is
tried three times in all, with a pause of 5 s between attempts (JE-2); a
context that ends during a pause ends it. Each attempt is interrupted at
the limit (deviation D8).

Readings where the reference is silent, each pinned by a test:

- **A thrown value that is not an `Error`.** Windmill prints the engine's
  debug form of the value, which is open
  ([Q-JE-1](../../docs/semantics/expressions.md#q-je-1)); windmill-lite
  prints its string form.
- **JE-17's "or else"** for an entry's tag and text: read as `??`, so an
  empty `step_id` or `message` is kept.
- **JE-49's positions** for `flow_input` and `flow_env`, which Windmill
  re-serialises: counted in the compact text, members of `flow_env` in key
  order. Which value is reported first when several are too deep:
  `flow_input`, the context values by name, then `flow_env`.
- **`results` and `error`** are `const` declarations.

Two named deviations of
[ADR 0001](../../docs/design/0001-equivalence-contract.md) §5 come from the
engine's build, not from this program: D16, the last digit of a
transcendental `Math` function on a C library other than glibc, and D17,
the recursion depth that fits the 1 MiB stack.

## The engine boundary

- **Bindings.** An evaluation binds context values as host strings, which
  the program parses itself. Values are never spliced into source text.
- **Preludes.** Programs the evaluation runs, in order, before the
  program, on its context; their top-level `const` declarations are the
  lexical `results` and `error` of JE-10.
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
  the pipeline, which reads the limits they quote from `Engine.Limits`.
- **Limits.** `DefaultLimits` gives 128 MiB per runtime
  ([JE-3](../../docs/semantics/expressions.md#je-3)), 20 s per attempt
  ([JE-2](../../docs/semantics/expressions.md#je-2); an attempt that reaches
  it is interrupted, deviation D8) and a stack of 1 MiB, QuickJS-ng's
  built-in default, which Windmill's embedding never changes
  ([JE-48](../../docs/semantics/expressions.md#je-48)).

## Tests and cases

The pipeline's cases are in `testdata/pipeline/<finding>.json`, one file
per finding, named after it in lower case. A case restates a case that
recording batch E3a recorded, or gives its own context. Either way it
states what `Compile` makes of the text and which route answers on a fake
engine.

| Field | Meaning |
|---|---|
| `spec`, `variant` | the catalogue case, or the finding with a variant for a case beyond the catalogue |
| `provenance`, `title`, `note` | the restated case's provenance, or `source-derived` for a case of the pipeline's own |
| `recorded` | the E3a case restated: `exprs` and `case` (a file of [conformance/exprs](../../conformance/README.md) and its case's name), or `fixture`, `run`, `at` and `input` (a unit case of a flow fixture) |
| `site`, `text` | the site and the expression as written; for a restated case, its own |
| `context` | for a case of its own: `previous_id`, `previous_result`, `flow_input`, `flow_env` and `results` (stored texts by id) |
| `expect` | any of `trimmed`, `shortcut`, `mode`, `binds`, `code` (the rewritten code inside the wrapper), `route` (`engine` or a shortcut's name: `context_name`, `flow_key`, `previous_id`, `results_path`), `text` (a shortcut's exact answer), `bound` (the engine's host strings) and `results_lookup` |

A restated case's scope is rebuilt from the recorded case's context. A
context-free case is at module 0: its previous id is `""`, and
`previous_result` and `flow_input` are the stored flow input. A unit case
supplies its previous id, `previous_result`'s producer text and the
database's texts directly (ADR 0012, B2a). A shortcut's answer is held to
the recorded expectation: the exact evaluator text of a unit case, or a
context-free case's recorded argument as its stored form.

- `TestPipelineCases` runs every case.
- `TestPipelineCoverage` is the exit gate. Every case of JE-5 to JE-14,
  JE-V6 and CF-V6 that E3a recorded is restated exactly once, and every
  catalogue case of those findings that an E3a fixture or run cites is the
  spec of a restatement. A restatement gives its route, and a shortcut's
  text.
- `TestRecordedCasesThroughPipeline` runs every context-free case and
  every unit case that E3a recorded, of every finding, on the fake engine.
  Wherever a shortcut answers, the answer must be what the oracle recorded.
  A flow fixture's expressions that no unit case restates are not run.
- `TestJE8KeyOrderExample` keeps the raw-text assertion of ADR 0003's JE-8
  example (review item cx R16). It also ties the text to the oracle's
  `timeout` errors, which quote the producer's key order from memory and
  jsonb's text through the database.
- Further tests cover:
  - the wrapper's lines (JE-14) and the predicate wrapping;
  - the IR's JSON form;
  - compile errors raised at evaluation, and compiling once;
  - a context that ends during JE-9's lookup;
  - the engine's errors passing through;
  - the results lookup from memory or storage, and its errors;
  - the helpers;
  - concurrent use under `-race`.
- `jsontext_test.go` checks the member lookup and the `#>` walk against
  an `encoding/json` implementation, on generated values. jsonb's text
  output is checked on fixed cases, and on generated values: it parses to
  the same value, with one space per `:` and `,` of the decoded value.
  `FuzzCompile` and `FuzzJSONText` fuzz the stages and the scanners.

The tests of milestone B2b run on the real engine. Package `expr` cannot
import [internal/expr/quickjs](quickjs/README.md), which imports it, so
`quickjs_test.go`, of the external test package `expr_test` in the same
test binary, registers the engine for them; an engine a test starts must
end with no leaked heap block.

- `TestE3aOnEngine` is B2b's exit gate (ADR 0012: the expression suite
  against the E3a recordings, at unit level as in B2a). It runs every
  expression E3a recorded, the context-free cases and the unit cases of
  the flow fixtures, through the pipeline with the context the recording
  gives, as `TestRecordedCasesThroughPipeline` does on the fake engine. It
  holds each to its whole expectation: a value through its stored form
  (point O2) or an exact evaluator text (O1), a predicate's skip, or an
  error, wrapped as its site wraps it. Each evaluator error must also stand
  in a message the oracle recorded, byte for byte, stack included, at its
  end or before a Rust location suffix. Two cases differ by a named
  deviation and say what windmill-lite gives: the transcendental `Math`
  cases of JE-1 (review item cl R3-2), exact with glibc and within one
  unit in the last place otherwise (D16), and JE-48/a (D17). JE-2/a is not
  run: the oracle canceled its run before the evaluator's text surfaced;
  `TestJE2OnEngine` runs the same expression with a short limit.
- `TestBudgetGate` measures ADR 0003's budget gate through the pipeline. It
  runs with `WL_EXPR_BUDGET=gate` (asserts the bounds, on the benchmark
  machine) or `=report` (prints the table, as CI does on its runners and
  from the static musl engine); `WL_EXPR_BUDGET_SECONDS` sets each row's
  length.
- `program_test.go` covers the program on the engine, case by case from
  the findings: serialisation (JE-15), JE-21's error forms, the attempt
  retries (JE-2), the memory limit's text (JE-3), `error` (JE-17), the
  `results` proxy (JE-12, JE-18), the helpers (JE-20), the bindings
  (JE-10, JE-16), the depth check (JE-49), the recursion depth (JE-48,
  D17), and ADR 0003's JE-8 key-order example on the engine's routes: the
  proxy answers the previous step from memory in the producer's key order
  and another step from the database in jsonb's (review item cx R16).
- The fake-engine tests check the retries and the error texts' mapping
  for every kind of engine error.

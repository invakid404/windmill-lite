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
Windmill's error texts. Milestone N1a wired both in:
[internal/ir](../ir/README.md) calls `Compile` for every expression of a
program, so `ir.json` carries the textual stages, and the engine
([internal/engine](../engine/README.md)) prepares a `Pipeline` for each
program it opens and gives the evaluator each site's scope.

## Contracts

| Name | What it is | Implemented by |
|---|---|---|
| `SiteKind` | the expression sites of [JE-22](../../docs/semantics/expressions.md#je-22) (the suspend site is left out, because `suspend` is rejected at load); `Predicate` marks the boolean sites of [JE-24](../../docs/semantics/expressions.md#je-24) | this package |
| `Expr`, `Shortcut`, `Mode` | a compiled expression as the IR carries it ([ADR 0002](../../docs/design/0002-ir-and-loader.md)): the text as written, the trimmed text, the shortcuts its text allows ([JE-6](../../docs/semantics/expressions.md#je-6) to [JE-9](../../docs/semantics/expressions.md#je-9)), the mode ([JE-13](../../docs/semantics/expressions.md#je-13)), the bound names ([JE-10](../../docs/semantics/expressions.md#je-10)) and the engine source; its JSON form (snake-case names, the site and mode by name, empty parts omitted) is the one the IR's `ir.json` holds | `Compile` (B2a), which [internal/ir](../ir/README.md) calls for each expression site |
| `Engine`, `Program`, `Input`, `Exception`, `Limits` | the JavaScript engine behind a Go interface ([ADR 0003](../../docs/design/0003-semantics-placement.md)): compile once, evaluate on a fresh runtime with host strings, host functions and preludes (global scripts that run first, B2b), one attempt's limits | B1a: [internal/expr/quickjs](quickjs/README.md) |
| `Evaluator`, `Scope`, `Results`, `Helpers` | what the flow interpreter calls at an expression site: the scope of [JE-22](../../docs/semantics/expressions.md#je-22), the results lookups of [JE-18](../../docs/semantics/expressions.md#je-18) and [JE-19](../../docs/semantics/expressions.md#je-19), and the `variable`, `resource` and `flow_user_state` helpers of [JE-20](../../docs/semantics/expressions.md#je-20) | `Pipeline` (B2a, B2b); the engine provides `Scope` and `Results`, and the platform milestones `Helpers` |
| `EvalError`, `ErrOutOfMemory`, `ErrTooDeep`, `ErrRewritePanic` | a failed evaluation with Windmill's text, and its cause | `Pipeline` (B2b, FU3) |

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
   ([JE-11](../../docs/semantics/expressions.md#je-11)). The inserted `)`
   goes where Windmill puts it: before the call's closing parenthesis, at a
   position counted in characters from the start of the text and applied as
   a byte offset. Text that is ASCII up to there is rewritten exactly;
   otherwise the `)` lands early by the extra bytes before it
   (`variable("é")` becomes `(await variable("é)")`, which does not
   compile), and where it would land inside a character Windmill's
   evaluator panics: `Compile` then gives no `Source` and records the byte
   offset as `RewritePanic`. Then every match
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

[Request 8](../../docs/design/README.md) asked the semantics team about
the details these stages decided where the reference was silent. The
answers confirm them all except JE-11 for text that is not ASCII, which
FU3 follows as above.

## Evaluation

`NewPipeline(engine)` returns the evaluator. `Prepare` compiles the
preludes and expressions' programs on the engine when a bundle opens. A
program that does not compile is kept, and `Eval` fails with its error
(an `*EvalError` whose cause is the engine's `*Exception`) each time the
expression reaches the engine. A shortcut answers it without the engine all
the same. Programs are kept by source text, one bundle's worth.

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
puts there for a value read from the database is observable too:
[JE-44](../../docs/semantics/expressions.md#je-44)'s table of texts gives
it, and `Scope`'s documentation lists it. The flow input's members are in
jsonb's printed form, which the engine writes with `DatabaseText`, and
`previous_result` after a loop is a compact array with re-printed numbers.

When no shortcut fires, the expression's program runs on the engine with
these bindings (JE-10):

| Binding | Kind | When |
|---|---|---|
| each context value (`previous_result`, `result`, `params`, …) | host string, its text | the text names it and the scope holds it; `previous_result` also when the text names `error` or spells the previous id (JE-8) |
| `flow_input` | host string: the flow input, or `null` | the text names it |
| `flow_env` | host string: the flow env, or `{}` | the text names it |
| `__wl_results` | host function | the text names `results` and the scope has a results context |
| `__wl_variable`, `__wl_resource`, `__wl_flow_user_state` | host functions | always |
| `__wl_rebuild` | host string: a JSON array naming the bound values the context prelude rebuilds by assignment (JE-16) | a bound value's text holds `__proto` or a `\u` escape |

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
producer text, as Windmill reads it back
([JE-15](../../docs/semantics/expressions.md#je-15)): a string that is
UTF-8 as it is, unchecked, so text that is not JSON passes when the
expression replaced `JSON.stringify`, and anything else, a value that is
not a string or a string with an unpaired surrogate, as `null`. A failure
is an `*EvalError` whose message is Windmill's text (below); a context that
ends during the evaluation gives its cause. A text whose call rewrite
panics on Windmill fails, when no shortcut answers it, with
windmill-lite's own text, cause `ErrRewritePanic`; what the panic does to
Windmill's job is not recorded
([Q-JE-13](../../docs/semantics/expressions.md#q-je-13)).

## The program on the engine

`program.go` writes what an evaluation runs (milestone B2b). An evaluation
runs these global scripts, each named `eval_script` and in strict mode
([JE-50](../../docs/semantics/expressions.md#je-50)), on one fresh
context:

1. **The context prelude**, always. It converts each bound host string in
   place, so a context value, `flow_input` and `flow_env` are properties of
   `globalThis` holding values that keep their text's key order, `-0` and a
   double's precision
   ([JE-10](../../docs/semantics/expressions.md#je-10),
   [JE-16](../../docs/semantics/expressions.md#je-16)). Windmill's host
   rebuilds each object by assignment, key by key, so a `__proto__` key
   sets the prototype (an object or `null`) or is dropped; the prelude
   parses with `JSON.parse` and rebuilds by assignment the values whose
   text can hold such a key, those with `__proto` or a `\u` escape, which
   the pipeline finds before binding and names in a host string,
   `__wl_rebuild`, that the prelude removes. It defines
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
   lexical `error` with `let`, so the expression may assign it (JE-10),
   derived from `previous_result` by
   [JE-17](../../docs/semantics/expressions.md#je-17)'s decision list,
   whose fallbacks for an entry's tag and text are falsy tests (`||`).
4. **The program**: `Source`, then `.then((v) => JSON.stringify(v ?? null)
   ?? "null")` on its last line, so a value `JSON.stringify` gives no text
   for becomes `null`
   ([JE-15](../../docs/semantics/expressions.md#je-15)).

The preludes take the intrinsics they call later (`JSON.parse`,
`Promise.prototype.then`, `Reflect.apply`) before the user's code runs. A
stack frame in an error message gives a position in these scripts, so
their layout is observable (JE-21). It reproduces the frames E3a recorded:
`<anonymous> (eval_script:1:1)` and `<eval> (eval_script:1:<29 + length>)`
for an unknown name read by a one-line expression, `stringify (native)`
called from `eval_script:1:64` for a three-character expression,
`variable (eval_script:7:50)`, and `__throwOrParse (eval_script:5:54)` with
`__getResult (eval_script:11:59)`.

Before the scripts run, each bound context value is checked for depth
([JE-49](../../docs/semantics/expressions.md#je-49)), in Windmill's order:
the context values the text names, in the scope's order; `previous_result`
when only `error` or a spelling of the previous id bound it; `flow_input`;
then each member of `flow_env`, in its text's order. The first value nested
deeper than 127 levels fails with `recursion limit exceeded at line <l>
column <c>`, the position of the bracket that goes past the limit, and no
JavaScript runs. The position counts in the text serde_json reads there: a
context value's and a `flow_env` member's own text as the scope holds it,
and for `flow_input` the object written out again compactly, its keys
escaped as serde_json escapes them and its members' texts copied in, so
that the keys before the deep member count. A text with no more than 127
brackets is not scanned.

The engine's errors become Windmill's texts, the `*EvalError` message that
each site then wraps
([JE-23](../../docs/semantics/expressions.md#je-23) to
[JE-35](../../docs/semantics/expressions.md#je-35)):

| The engine reports | Message | Cause |
|---|---|---|
| a thrown `Error` of any class, or a source that does not compile | `QuickJS evaluation error: Error:`, then a space and the message when there is one, then a newline and the stack when there is one ([JE-21](../../docs/semantics/expressions.md#je-21)) | the `*Exception` |
| another thrown value | `QuickJS evaluation error: Exception generated by quickjs: <debug form>` and a newline (JE-21) | the `*Exception` |
| a `null` or `undefined` throw, or `InternalError: out of memory` | ``The expression evaluation exceeded the memory limit of <N> MB. …`` ([JE-3](../../docs/semantics/expressions.md#je-3)) | `ErrOutOfMemory` |
| the attempt's time limit, three times | ``The expression evaluation `<trimmed text>` took too long to execute (>20000ms)`` ([JE-2](../../docs/semantics/expressions.md#je-2)) | `ErrAttemptTimeout` |
| a call rewrite that Windmill's evaluator panics on (JE-11) | ``Expression evaluation failed: the rewrite of its helper calls inserts a parenthesis at byte <n> of `<trimmed text>`, inside a character, where Windmill's evaluator panics``, windmill-lite's own | `ErrRewritePanic` |

An Error's message and stack are read as Windmill's embedding (rquickjs
0.11.0) reads them, in that order: converted to strings, and absent when
the property is `null` or `undefined`, when reading or converting it
throws, or when the text is not UTF-8, as a message holding an unpaired
surrogate is not ([JE-52](../../docs/semantics/expressions.md#je-52)).
Nothing else read back runs the program's code: an Error's name, which the
memory check needs, comes from data properties only, and a value that is
not an Error is not converted unless it is a string, a number or a
boolean, so a `toString`, `Symbol.toPrimitive` or Proxy trap is never
called, and a BigInt's decimal text, which can take longer to write than
an attempt has left, is never written. A value's debug form is rquickjs's type name and payload:
`String(Ok("s"))` (Rust's quoting and escapes), `Int(1)` for a number the
engine holds as a 32-bit integer, `Float(1.5)`, `Float(1e300)` or
`Float(NaN)` for any other (Rust's notation: the shortest digits that read
back, a tie between two broken upward, as in `1000000000000000.3` for
`1000000000000000.25`; `1.0`; exponents from 1e16 and below 1e-4),
`Bool(true)`, `BigInt"BigInt"`, and for a symbol, an array, a constructor,
another function, a promise, a proxy or another object its type name and
the engine's address, `Object(0x…)`, which changes from run to run.
`debugform.go` writes them.

`<N>` and the time limit are the engine's `Limits`. An evaluation whose
error text contains `took too long`, the time limit's or the user's own, is
tried three times in all, with a pause of 5 s between attempts (JE-2); a
context that ends during a pause ends it. Each attempt is interrupted at
the limit (deviation D8).

Milestone B2b's readings where the reference was silent were answered by
the semantics team (design request 9) and FU3 follows the answers: the
debug form above (JE-21), the falsy fallbacks of JE-17, JE-49's order and
positions, `error` as a `let` binding (JE-10), the rebuild by assignment
(JE-16), and the unchecked completion (JE-15). Readings where the
reference is still silent, each pinned by a test:

- **JE-11's count** starts at the start of the text, and also places the
  `)` of a call that never closes; Q-JE-13's recordings settle calls at
  the start of the text only. Where Windmill panics, windmill-lite fails
  the evaluation with its own text.
- **The values of `results` reads and of the helpers** are parsed with
  `JSON.parse`, not rebuilt by assignment: JE-16 says how context values
  are rebuilt, not those.
- **The scope's order** stands for Windmill's hash maps where JE-49 reports
  the first of several values nested too deeply.
- **Rust's notation** in a debug form uses Go's Unicode tables to decide
  which characters a string escapes. Go's tables (Unicode 17) and rustc
  1.94.0's agreed on every Unicode scalar value, and the float notation on
  270 588 values; a later Unicode version on either side could differ on
  newly assigned characters.

Two named deviations of
[ADR 0001](../../docs/design/0001-equivalence-contract.md) §5 come from the
engine's build, not from this program: D16, the last digit of a
transcendental `Math` function on a C library other than the glibc of
Windmill's image, and D17, the recursion depth that fits the 1 MiB stack.

## The engine boundary

- **Bindings.** An evaluation binds context values as host strings, which
  the program parses itself. Values are never spliced into source text.
- **Preludes.** Programs the evaluation runs, in order, before the
  program, on its context; their top-level declarations are the lexical
  `results` (`const`) and `error` (`let`) of JE-10.
- **Host functions.** These cover the `results` lookups and the helpers. They
  return text that resolves a Promise, or an error that rejects it.
- **Result.** The program's completion value is a string, which the
  engine returns as it converts it to UTF-8 (an unpaired surrogate in its
  three-byte form), or `ErrNotString`. Serialisation
  (`JSON.stringify(value ?? null)`,
  [JE-15](../../docs/semantics/expressions.md#je-15)) is the program's job,
  and reading the completion back is the pipeline's, so all the semantics
  stay in this package.
- **Jobs.** Every queued job runs, the jobs those jobs queue included,
  before the engine reads the completion value, and a job that throws is
  dropped ([JE-51](../../docs/semantics/expressions.md#je-51)). A chain of
  jobs that never ends reaches the attempt limit.
- **Errors.** `Exception` keeps what was thrown (an `Error`, `null`,
  `undefined` or another value): an `Error`'s name, message and stack, and
  whether it has a message and a stack; another value's kind and payload.
  QuickJS-ng reports running out of memory as
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
| `expect` | any of `trimmed`, `shortcut`, `mode`, `binds`, `code` (the rewritten code inside the wrapper), `rewrite_panic` (the byte offset where JE-11's rewrite panics, for a text with no code), `route` (`engine` or a shortcut's name: `context_name`, `flow_key`, `previous_id`, `results_path`), `text` (a shortcut's exact answer), `bound` (the engine's host strings), `results_lookup`, and `engine`: what the real engine gives, the evaluator's exact `text`, its exact `error` or an `error_prefix` |

A restated case's scope is rebuilt from the recorded case's context. A
context-free case is at module 0: its previous id is `""`, and
`previous_result` and `flow_input` are the flow input as Windmill's worker
holds it, a compact object whose members are each in jsonb's printed form
(JE-44's table). A unit case supplies its previous id, `previous_result`'s
text and the database's texts directly (ADR 0012, B2a), and its flow input
as submitted, which becomes the worker's text the same way. A shortcut's answer is held to
the recorded expectation: the exact evaluator text of a unit case, or a
context-free case's recorded argument as its stored form.

- `TestPipelineCases` runs every case.
- `TestPipelineCoverage` is the exit gate. Every case of JE-5 to JE-14,
  JE-V6 and CF-V6 that E3a recorded is restated exactly once, and every
  catalogue case of those findings that an E3a fixture or run cites is the
  spec of a restatement. A restatement gives its route, and a shortcut's
  text.
- `TestDebugFormMatchesRust` holds the debug form's float and string
  notation to `testdata/debugform.json`, texts rustc printed, among them
  floats whose two shortest digit strings tie; `TestDebugFloat` pins the
  presentation rules on named values.
- `TestPipelineCasesOnEngine` runs on the real engine every case that
  gives `expect.engine`, in its own context: among them the catalogue
  cases that no recording holds yet (JE-10/e, JE-15/j, JE-16/c, JE-16/g,
  JE-17/d, JE-21/b, JE-50/a, JE-51/a, JE-52/a; `conformance/exprs/deferred.yaml`
  names them), each held to its exact text or message.
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
  cases of JE-1 (review item cl R3-2), each value the oracle's or one unit
  in the last place from it, every difference logged (D16; ADR 0001 gives
  what each C library measured), and JE-48/a (D17). JE-2/a is not run: the oracle
  canceled its run before the evaluator's text surfaced; `TestJE2OnEngine`
  runs the same expression with a short limit.
- `TestBudgetGate` measures ADR 0003's budget gate through the pipeline. It
  runs with `WL_EXPR_BUDGET=gate` (asserts the bounds, on the benchmark
  machine) or `=report` (prints the table, as CI does on its runners and
  from the static musl engine); `WL_EXPR_BUDGET_SECONDS` sets each row's
  length.
- `program_test.go` covers the program on the engine, case by case from
  the findings: serialisation (JE-15) and a replaced serialiser, JE-21's
  error forms and debug forms (JE-52's dropped message among them), the
  attempt retries (JE-2), the memory limit's text (JE-3), `error` (JE-17)
  and its assignment (JE-10), the `results` proxy (JE-12, JE-18), the
  helpers (JE-20), the bindings and their rebuild by assignment (JE-10,
  JE-16), the depth check's positions and order (JE-49), the recursion
  depth (JE-48, D17), strict mode (JE-50), the job queue (JE-51), JE-11's
  rewrite of text that is not ASCII, a readback that runs none of the
  program's code (`TestReadbackRunsNoCode`) and writes no BigInt's
  decimal text (`TestReadbackOfALargeBigInt`), and ADR 0003's JE-8
  key-order example on the engine's routes: the proxy answers the previous
  step from memory in the producer's key order and another step from the
  database in jsonb's (review item cx R16).
- The fake-engine tests check the retries, the error texts' mapping for
  every kind of engine error and every kind of thrown value, and how the
  completion is read back (`TestCompletionReadBack`).

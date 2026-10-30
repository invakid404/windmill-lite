# internal/expr/quickjs

**Current state:** implemented (milestone B1a of
[0012](../../../docs/design/0012-layout-and-milestones.md)); milestone F4
gave its heap the mimalloc allocator, and milestone B2b runs the expression
pipeline of [internal/expr](../README.md) on it, with preludes. Outside the
tests, `windmill-lite selfcheck` and the [wl](../../../wl/README.md)
runtime call it: `windmill-lite run`, `windmill-lite host` (host mode,
F3a) and `wltest check` for flow fixtures run their expressions on it. This
README covers `internal/expr/quickjs/`, with its vendored
`quickjs-ng/` and `mimalloc/` sources and its `testdata/`. The package is windmill-lite's
JavaScript engine for flow expressions: QuickJS-ng at revision `be664ca1`,
the engine Windmill's expressions run on
([JE-1](../../../docs/semantics/expressions.md#je-1)), compiled from C source
through cgo. `*Engine` implements the `expr.Engine` interface of
[internal/expr](../README.md).

What the package does not do:

- build the program an expression runs as, bind context values, or
  serialise the result: the expression pipeline does that (B2a, B2b);
- produce Windmill's error messages. It reports what the engine saw (the
  thrown value, an interrupted attempt), and the pipeline wraps that in
  Windmill's texts, for example the time-limit and memory-limit messages of
  [JE-2](../../../docs/semantics/expressions.md#je-2) and
  [JE-3](../../../docs/semantics/expressions.md#je-3);
- retry an attempt that reached the time limit (JE-2's three attempts with
  5 s pauses are the pipeline's).

## API

| Name | What it does |
|---|---|
| `New(Options)` | Starts an engine and its pool. It returns once every slot has prepared its first runtime. |
| `Options` | `Limits` (zero value: `expr.DefaultLimits()`), `Slots` (0: `min(GOMAXPROCS, 8)`), `CacheEntries` (0: 1024; negative: no cache). |
| `(*Engine).Compile(source)` | Compiles a global script named `eval_script`, the script name in Windmill's engine stack frames ([JE-21](../../../docs/semantics/expressions.md#je-21)), to bytecode, with strict mode forced, as Windmill's evaluator compiles every script ([JE-50](../../../docs/semantics/expressions.md#je-50)). A source that does not compile returns an `*expr.Exception`. Results, compile errors included, are cached by source text. |
| `(*Engine).Eval(ctx, program, input)` | Runs the input's preludes, then the bytecode, on a fresh runtime and context, and returns the completion string or an error. |
| `(*Engine).Limits()`, `(*Engine).Stats()` | The limits in force, and counters: evaluations, compiles, waits for a slot and their total time, timeouts, cancellations, and heap blocks that QuickJS-ng leaked and the engine freed. |
| `(*Engine).Close()` | Stops the engine. Callers still waiting get `ErrClosed`; running evaluations finish first. |
| `CLibrary()` | The C library the engine was built against: `glibc`, `musl` (the static Linux engine), `apple` or `other`. QuickJS-ng's transcendental `Math` functions call its libm, so their last digit can differ from the glibc of Windmill's image (deviation D16 of [0001](../../../docs/design/0001-equivalence-contract.md)). |

## How an evaluation runs

1. **A slot.** The engine owns a pool of slots. A slot is a thread created in
   C with a stack of its own size, on which a Go loop drives one runtime at
   a time. Each runtime is a QuickJS-ng runtime and *full context*: the
   context that QuickJS-ng's `JS_NewContext` builds, with every intrinsic
   it adds. `performance.now()` counts from the evaluation's start, as in a
   context created for it (step 3).
2. **Waiting.** `Eval` hands the job to the next slot that has a prepared
   runtime. When none has, the caller waits, and waiting callers are served
   in arrival order. `Stats` counts these waits.
3. **Binding.** Each `HostString` becomes a writable, enumerable and
   configurable global property holding its text as a string; the program
   parses it ([ADR 0003](../../../docs/design/0003-semantics-placement.md)).
   Each `HostFunc` becomes a global function. A call converts its first
   argument with `ToString` (a conversion that throws makes the call throw)
   and returns a Promise, and `Call` runs on its own goroutine with a
   context that ends with the attempt. At most 64 calls of one evaluation
   run at once; the others wait, queued, in call order. The Promise settles
   only after the jobs queued so far have run: it resolves with the returned
   text, or rejects with an `Error` whose message is `err.Error()`, built
   with no frames on the stack (so its `stack` is empty unless the program
   set `Error.prepareStackTrace`, which then runs). The runtime is prepared
   before a job arrives, so an evaluation first restarts the context's
   `performance` clock, before the bindings, so that one named `performance`
   replaces it.
4. **Running.** Each of the input's preludes runs, in order, as a global
   script of the context: its top-level `let`, `const` and `class`
   declarations become global lexical bindings the program sees, which no
   property of `globalThis` gives, and its completion value is discarded. A
   prelude that throws ends the evaluation with its exception. Then the
   bytecode runs, and every pending job runs, the jobs those jobs queue
   included, until the queue is empty; only then is the completion value
   read, as Windmill's evaluator reads it
   ([JE-51](../../../docs/semantics/expressions.md#je-51)). A job that
   throws (a `queueMicrotask` callback, say) is dropped, and its exception
   fails nothing. A completion value that is not a Promise is settled at
   once, and so is a Promise that has fulfilled or rejected; a pending one
   waits for a host call to settle, after which the queue empties again. A
   chain of jobs that never ends runs until the attempt limit.
5. **The answer.** A fulfilled string is returned as UTF-8, except that
   QuickJS-ng writes an unpaired surrogate as three bytes (WTF-8);
   `JSON.stringify` escapes those, so its text is plain UTF-8. Anything else
   gives `expr.ErrNotString`. A throw or rejection gives an `*expr.Exception`
   that keeps the thrown kind (`Error`, `null`, `undefined`, other). For an
   `Error` it keeps the `message` and `stack` as Windmill's embedding reads
   them, first and in that order: converted to strings, each absent when it
   is `null` or `undefined` or its conversion throws. It keeps the `name`
   too, read from data properties along the prototype chain only, never
   through a getter or a Proxy, and a text that joins the name and message
   as `Error.prototype.toString` does, without calling it. For another
   value it keeps its kind as rquickjs 0.11.0 classifies it (by tag; an
   object as the first of array, constructor, function, promise and proxy
   that it is, else object) and its payload: a number, a boolean, a
   string's bytes, or the engine's address of a symbol or an object, and
   the string form of a string, a number or a boolean. A symbol or an
   object is never converted, so its `toString`, `Symbol.toPrimitive` or
   traps do not run, and neither is a BigInt, whose decimal text can take
   longer to write than the attempt has left. The pipeline writes Windmill's texts from these
   ([JE-21](../../../docs/semantics/expressions.md#je-21)).
   rquickjs tests a promise by `JS_PromiseState(…) >= 0`, which works
   because the quickjs-ng it vendors adds `JS_PROMISE_NOT_A_PROMISE = -1`;
   this revision's enum has no negative member, so the engine tests the
   class with `JS_IsPromise`.
6. **Replenishing.** The slot replies first, then frees the runtime and
   prepares the next one, off the caller's path. A runtime is never reused:
   nothing survives from one evaluation to the next (JE-1). Every block a
   runtime allocates is tracked, so a block QuickJS-ng fails to free (it
   does for some programs, such as one that assigns an object to
   `Error.stackTraceLimit`) is freed with the runtime and counted in
   `Stats().LeakedBlocks`. The tests fail on any other leak.

## Limits

| Limit | Default | What happens |
|---|---|---|
| heap | 128 MiB (`Limits.MemoryMB`) | Allocation fails past the cap. QuickJS-ng then throws an `InternalError` with the text `out of memory`, or a bare `null` or `undefined`, and the exception keeps that kind ([JE-3](../../../docs/semantics/expressions.md#je-3)). The cap also covers the engine's host-call bookkeeping: one record per outstanding call, reused once the call settles, and the argument copies of calls not yet handed to Go, which are freed as Go copies them. Reading the completion value or the exception back gets 16 MiB of headroom above the cap. |
| stack | 1 MiB (`Limits.StackBytes`) | QuickJS-ng's built-in default, which Windmill's embedding never changes ([JE-48](../../../docs/semantics/expressions.md#je-48)). It is measured from where each call enters the engine: the top level, each round of the job loop, each settlement. Overflow is a catchable `RangeError`, `Maximum call stack size exceeded`. A slot thread's own stack is at least 8 MiB and at least 4 MiB more than this limit, so a deep recursion meets the engine's error, never the end of the thread. Threads that the Go runtime creates would have the C library's default stack, which is 128 KiB on musl. |
| attempt | 20 s (`Limits.Attempt`) | The attempt is interrupted through QuickJS-ng's interrupt handler, which the engine polls in loops, calls and regular-expression matching. The interrupt cannot be caught. A built-in that runs long without reaching the handler (`"x".repeat(n)` on a large `n`, for one), or a long readback, still ends the attempt: whatever the program got to, `Eval` returns `expr.ErrAttemptTimeout` once the limit has passed, and the slot prepares a new runtime. Windmill leaves a timed-out evaluation running; interrupting it is deviation D8 of [0001](../../../docs/design/0001-equivalence-contract.md). A Promise that never settles also ends here ([JE-2](../../../docs/semantics/expressions.md#je-2)). |
| context | the caller's `ctx` | When `ctx` ends during an evaluation, the program is interrupted the same way and `Eval` returns `context.Cause(ctx)`, again whatever the program got to. |

## Vendored source and licence

`quickjs-ng/` holds QuickJS-ng's engine sources at revision
`be664ca1ccd86cbadf8df7348b8e15e77a32a1ba`, copied unmodified under the MIT
licence. [NOTICE](quickjs-ng/NOTICE) lists every file with its SHA-256, and
a test checks them. The `qjs_*.c` files compile them as part of this
package, and `wl_quickjs.c` is the thin C layer that the Go slot loop calls.

The C sources are built with `-O2 -DNDEBUG`. QuickJS-ng's assertions are
off, because a program can reach one of them: the one in `JS_FreeRuntime`
that checks for leaked objects. The tracked heap handles those leaks
instead.

`mimalloc/` holds the allocator the engine's heap uses (below): mimalloc
v3.5.3, revision `d4881d338125e1cb7c47ba4cfb398d6f7c0c8d45`, copied
unmodified under the MIT licence, with its own
[NOTICE](mimalloc/NOTICE) that the same test checks. `qjs_mimalloc.c`
compiles it.

Building needs cgo and a C compiler: the system clang on macOS. The Linux
release binaries are static, built with `zig cc` against musl. The `Static
Linux engine` CI job builds the tests of this package and of
[internal/expr](../README.md) that way for `amd64` and `arm64`, checks that
the binaries are static, and runs them on a runner of that architecture.

## Allocator

The engine allocates every block of a runtime's heap with mimalloc
(`mi_malloc` and the rest, in `wl_quickjs.c`), and the heap cap counts each
block's `mi_usable_size`. mimalloc does not replace the C library's
`malloc`, which the rest of the program keeps.

musl's `malloc`, which the static Linux engine links, makes every slot
thread wait on one lock. Measured in CI (`TestPoolUnderLoad`, CI run
40709537760), the CPU per evaluation with 8 callers back to back:

| Runner | Bound value | musl and mimalloc (release) | musl's malloc | glibc's malloc |
|---|---|---|---|---|
| `linux/arm64` | 4 KB | 189 µs | 2.27 ms | 261 µs |
| `linux/arm64` | 256 KB | 1.46 ms | 12.9 ms | 1.69 ms |
| `linux/amd64` | 4 KB | 340 µs | 1.74 ms | 433 µs |
| `linux/amd64` | 256 KB | 2.82 ms | 8.45 ms | 3.04 ms |

One caller costs about the same with every allocator. With mimalloc, the
glibc and musl builds measure the same, and the heap accounting no longer
depends on the C library's block sizes. On a Linux C library other than
glibc, `qjs_mimalloc.c` sets mimalloc's `MI_LIBC_MUSL` (musl defines no
macro of its own), which only stops mimalloc reading the thread pointer
directly.

The build tag `wl_libc_malloc` (`alloc_libc.go`) puts the C library's
allocator back. Only measurements use it: the `Static Linux engine` CI job
prints the pool's table for four builds in its summary (musl or glibc,
each with mimalloc and with its own `malloc`).

## Tests and cases

- `testdata/engine/<finding>.json` holds the engine-level cases of JE-1,
  JE-2, JE-3 and JE-48. Each case encodes a catalogue case
  (`docs/semantics/conformance.md`) or a rule of its finding. It gives a
  program and its bindings and limits, and the outcome the engine itself
  produces: the result text, the exception's kind, name and message, a
  compile exception, or `attempt_timeout`. The runner wraps a case's `expr`
  as an async function that awaits the value and returns
  `JSON.stringify(value ?? null)`, a stand-in for the pipeline's wrapper. A
  test fails unless every catalogue case of those four findings is encoded,
  with the catalogue's provenance. Cases that show a deviation (D8, D17)
  name it.
- `testdata/globals.json` pins the names the full context defines. JE-1
  lists what Windmill's context lacks and has, among them
  `queueMicrotask`, `performance` and `DOMException`, which QuickJS-ng's
  full context adds; batch E3f recorded those on the oracle
  ([Q-JE-2](../../../docs/semantics/expressions.md#q-je-2), answered).
- The Go tests cover the binding, the preludes, the C library's name, host
  functions (concurrency and its bound, rejection, settling after queued
  jobs, the attempt's end, program code that calls the host while a call
  is settled, arguments counted against the cap, sequential calls whose
  arguments add up past the cap, record reuse), exceptions and the kinds
  of thrown values, the job queue (every queued job runs, and a throwing
  one is dropped), the compile cache, fresh runtimes, leaks, interrupts
  and cancellations (also when
  they come during a long built-in or readback, while a host call is
  pending, or once a completed readback is about to be returned, which the
  tests reach through `attemptReturned`), `Close`, the arrival
  order of waiting callers, concurrent use under `-race`, and the overflow
  depth at the default stack, which they log for each platform and check
  lies between 600 (fits) and 1 000 000 (overflows), the bracket JE-48
  records on Windmill, where depth 1 000 overflows too; that this engine
  fits depth 1 000 (JE-48/a's `depth-1000` case) is deviation D17 of
  [0001](../../../docs/design/0001-equivalence-contract.md). An engine
  started through the tests' shared helper must end with no leaked heap
  block; the one test that makes QuickJS-ng leak uses a helper without that
  check.
- `TestPoolUnderLoad` measures latency under ADR 0003's load shapes and
  prints a table. It runs only with `WL_QUICKJS_LOAD=1`, and CI runs it as a
  report-only step.

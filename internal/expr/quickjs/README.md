# internal/expr/quickjs

**Current state:** implemented (milestone B1a of
[0012](../../../docs/design/0012-layout-and-milestones.md)); milestone F4
gave its heap the mimalloc allocator. Only `windmill-lite selfcheck` and
`windmill-lite host` (`cmd/windmill-lite/host.go`) call it yet. This README covers `internal/expr/quickjs/`, with its vendored
`quickjs-ng/` and `mimalloc/` sources and its `testdata/`. The package is windmill-lite's
JavaScript engine for flow expressions: QuickJS-ng at revision `be664ca1`,
the engine Windmill's expressions run on
([JE-1](../../../docs/semantics/expressions.md#je-1)), compiled from C source
through cgo. `*Engine` implements the `expr.Engine` interface of
[internal/expr](../README.md).

What the package does not do:

- build the program an expression runs as, bind context values, or
  serialise the result: the textual pipeline (B2a) and its run on this
  engine (B2b) do that;
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
| `(*Engine).Compile(source)` | Compiles a global script named `eval_script`, the script name in Windmill's engine stack frames ([JE-21](../../../docs/semantics/expressions.md#je-21)), to bytecode. It does not force strict mode, which Windmill's evaluator does ([JE-50](../../../docs/semantics/expressions.md#je-50)). A source that does not compile returns an `*expr.Exception`. Results, compile errors included, are cached by source text. |
| `(*Engine).Eval(ctx, program, input)` | Runs the bytecode on a fresh runtime and context, then returns the completion string or an error. |
| `(*Engine).Limits()`, `(*Engine).Stats()` | The limits in force, and counters: evaluations, compiles, waits for a slot and their total time, timeouts, cancellations, and heap blocks that QuickJS-ng leaked and the engine freed. |
| `(*Engine).Close()` | Stops the engine. Callers still waiting get `ErrClosed`; running evaluations finish first. |

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
4. **Running.** The bytecode runs, then pending jobs run until the
   completion value has settled. A completion value that is not a Promise is
   settled at once, and so is a Promise that has fulfilled or rejected; jobs
   still queued then never run. A job that throws (a `queueMicrotask`
   callback, say) fails the evaluation with its exception. Windmill differs
   on both ([JE-51](../../../docs/semantics/expressions.md#je-51), `later`):
   it reads the result only once the queue is empty, and it drops a job
   that throws.
5. **The answer.** A fulfilled string is returned as UTF-8, except that
   QuickJS-ng writes an unpaired surrogate as three bytes (WTF-8);
   `JSON.stringify` escapes those, so its text is plain UTF-8. Anything else
   gives `expr.ErrNotString`. A throw or rejection gives an `*expr.Exception`
   that keeps the thrown kind (`Error`, `null`, `undefined`, other), the
   Error's `name`, `message` and `stack`, and the value's string form
   (`Symbol(<description>)` for a symbol).
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
  with the catalogue's provenance. Cases that show deviation D8 name it.
- `testdata/globals.json` pins the names the full context defines. JE-1
  lists what Windmill's context lacks and has. Whether it also has
  `queueMicrotask`, `performance` and `DOMException`, which QuickJS-ng's
  full context adds, is open
  ([Q-JE-2](../../../docs/semantics/expressions.md#q-je-2)).
- The Go tests cover the binding, host functions (concurrency and its
  bound, rejection, settling after queued jobs, the attempt's end, program
  code that calls the host while a call is settled, arguments counted
  against the cap, sequential calls whose arguments add up past the cap,
  record reuse), exceptions, the compile cache, fresh runtimes, leaks,
  interrupts and cancellations (also when they come during a long built-in
  or readback), `Close`, the arrival order of waiting callers,
  concurrent use under `-race`, and the overflow depth at the default
  stack, which they log for each platform and check lies between 1 000
  (fits) and 1 000 000 (overflows). JE-48's compatibility band is now wider:
  depths up to 600 fit, and Windmill itself overflows at 1 000. An engine started through the tests' shared helper must end with
  no leaked heap block; the one test that makes QuickJS-ng leak uses a
  helper without that check.
- `TestPoolUnderLoad` measures latency under ADR 0003's load shapes and
  prints a table. It runs only with `WL_QUICKJS_LOAD=1`, and CI runs it as a
  report-only step.

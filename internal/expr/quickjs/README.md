# internal/expr/quickjs

**Current state:** working. The engine runs every flow expression that
reaches it, for `windmill-lite selfcheck`, for the
[wl](../../../wl/README.md) runtime (`windmill-lite run`, `windmill-lite
host` and `wltest check`), and for the tests. Nothing in it is a stub. This
README covers `internal/expr/quickjs/`, with its vendored `quickjs-ng/` and
`mimalloc/` sources and its `testdata/`.

This package is windmill-lite's JavaScript engine for flow
[expressions](../../../docs/glossary.md#expression): QuickJS-ng at revision
`be664ca1`, the engine that Windmill's expressions run on
([JE-1](../../../docs/semantics/expressions.md#je-1)), compiled from C
source through cgo. `*Engine` implements the `expr.Engine` interface of
[internal/expr](../README.md). The pipeline runs on it each expression
that its shortcuts and its Go answers leave to the engine, and
`windmill-lite selfcheck` runs its probes on it directly. The
[ADR](../../../docs/glossary.md#adr)
[Where semantics live](../../../docs/design/0003-semantics-placement.md)
chose this engine and its pool.

The package does not:

- build the program that an expression runs as, bind context values, or
  serialise the result: the expression pipeline does that;
- write Windmill's error messages. It reports what the engine saw (the
  thrown value, an interrupted attempt), and the pipeline wraps that in
  Windmill's texts, such as the time-limit and memory-limit messages of
  [JE-2](../../../docs/semantics/expressions.md#je-2) and
  [JE-3](../../../docs/semantics/expressions.md#je-3);
- retry an attempt that reached the time limit. The three attempts with
  5 s pauses are the pipeline's.

## Files

| File | What it holds |
|---|---|
| `doc.go` | The package documentation |
| `engine.go` | `Engine`, `Options`, `Stats`, `New`, `Compile`, `Eval`, `Close`, `ErrClosed` |
| `slot.go` | The slot loop that drives one runtime at a time; the cgo flags |
| `stack.go` | `CallStackBytes`, `StackLimit`: the measured stack per call and the scaled limit |
| `libc.go` | `CLibrary` |
| `alloc_libc.go` | The `wl_libc_malloc` build tag |
| `wl_quickjs.c`, `wl_quickjs.h` | The C layer that the Go slot loop calls: runtimes, host calls, the readback, the heap's modes |
| `qjs_cutils.c`, `qjs_dtoa.c`, `qjs_libregexp.c`, `qjs_libunicode.c`, `qjs_quickjs.c` | Compile QuickJS-ng's sources as part of this package |
| `qjs_mimalloc.c` | Compiles mimalloc |
| `quickjs-ng/`, `mimalloc/` | The vendored sources ([Vendored sources and licences](#vendored-sources-and-licences)) |
| `testdata/` | The engine cases and the global names ([Engine cases](#engine-cases)) |

## Public surface

| Name | Kind | What it does | Who calls it |
|---|---|---|---|
| `Engine` | struct | The native QuickJS-ng engine with its pool of slots, which implements `expr.Engine` and `expr.ExactMemberPaths`. Safe for concurrent use; close it to stop its threads | `wl`, `windmill-lite selfcheck`, `wltest check`, the tests |
| `New` | func | Starts an engine and its pool. It returns once every slot has prepared its first runtime. The first `New` of a process also measures the C stack that one JavaScript call takes ([Stack limit](#stack-limit)), and fails if the measurement fails or the scaled limit is too large for a thread | `wl`, `windmill-lite selfcheck`, `wltest check`, the tests |
| `Options` | struct | `Limits` (zero value: `expr.DefaultLimits()`); `Slots` (0: `min(GOMAXPROCS, 8)`); `CacheEntries` (0: 1024; negative: no cache); `LeakCheck` ([Allocator](#allocator)) | the callers of `New` |
| `(*Engine).Compile` | method | Compiles a source to bytecode as a global script named `eval_script`, the name in Windmill's stack frames ([JE-21](../../../docs/semantics/expressions.md#je-21)), with strict mode forced ([JE-50](../../../docs/semantics/expressions.md#je-50)). A source that does not compile returns an `*expr.Exception`. Results, compile errors included, are cached by source text | the pipeline, `windmill-lite selfcheck`, the tests |
| `(*Engine).Eval` | method | Runs the input's preludes, then the bytecode, on a fresh runtime and context, and returns the completion string or an error ([How an evaluation runs](#how-an-evaluation-runs)) | the pipeline, `windmill-lite selfcheck`, the tests |
| `(*Engine).ExactMemberPaths` | method | Says that the engine evaluates a member path over JSON values as package `expr`'s own answer does, so the pipeline answers those paths without it | the pipeline |
| `(*Engine).Limits` | method | The limits in force; `StackBytes` as configured, not scaled | the pipeline |
| `(*Engine).StackLimit` | method | The stack limit in bytes that each evaluation gets: `Limits().StackBytes` scaled to the build | `windmill-lite selfcheck`, the tests |
| `(*Engine).Stats` | method | The counters since `New` (`Stats`) | the tests |
| `Stats` | struct | `Slots`; `Evaluations` and `Compiles`; `Waited` and `WaitTime` (jobs that found no prepared slot); `Timeouts` and `Canceled`; `LeakedBlocks` (in leak check only; 0 otherwise) | the tests |
| `(*Engine).Close` | method | Stops the engine. Callers still waiting get `ErrClosed`; running evaluations finish first. It waits for every slot thread to free its runtime and exit | the callers of `New` |
| `ErrClosed` | var | Returned by an engine that has been closed | `TestClose` checks it; no caller tests for it |
| `CallStackBytes` | func | The C stack in bytes that one JavaScript call takes in this build, measured once per process ([Stack limit](#stack-limit)) | `windmill-lite selfcheck`, the tests |
| `CLibrary` | func | The C library that the engine was built against: `glibc`, `musl` (the static Linux engine), `apple` or `other` | `internal/expr`'s tests, the tests |

`CLibrary` matters because QuickJS-ng's transcendental `Math` functions call
its libm. Their last digit can then differ from the glibc of Windmill's
image: a [named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations).

## Errors

| Error | When | Returned by |
|---|---|---|
| The error of `expr.Limits.Validate` | `Options.Limits` is not the zero value (which takes `expr.DefaultLimits()`) and one of its limits is not positive | `New` |
| `expr/quickjs: slot count <n> is negative` | `Options.Slots` is negative | `New` |
| The error of `CallStackBytes` | The measurement of the stack per call failed, or gave less than 64 B or more than 16 KiB | `New`, `CallStackBytes` |
| `expr/quickjs: stack limit of <n> bytes is too large for a thread` | The scaled stack limit does not fit a slot thread | `New` |
| `expr/quickjs: cannot allocate a slot`, `expr/quickjs: cannot start a slot thread: <errno>`, `expr/quickjs: cannot create a runtime` | A slot, its thread or its first runtime could not be created; `New` closes the slots it made | `New` |
| `*expr.Exception` | A source that does not compile (`Compile`; the error is cached), or a program that throws or whose Promise rejects (`Eval`) | `Compile`, `Eval` |
| `ErrClosed` | The engine is closed, or closes while the caller waits for a slot. A source that `Compile` has cached still returns its cached result | `Compile`, `Eval` |
| `expr/quickjs: program was not compiled by this engine` | The program or a prelude is not one that this engine compiled | `Eval` |
| `expr/quickjs: host function has no Call: "<name>"` | A `HostFunc` has a nil `Call` | `Eval` |
| `expr.ErrAttemptTimeout` | The attempt reached `Limits.Attempt` | `Eval` |
| `expr.ErrNotString` | The program completed with something other than a string | `Eval` |
| `context.Cause(ctx)` | The caller's context ended before or during the evaluation | `Eval` |

The pipeline of [internal/expr](../README.md#errors-and-their-texts) turns
an `*expr.Exception` and `expr.ErrAttemptTimeout` into Windmill's texts, and
`expr.ErrNotString` into the value `null`. It passes `ErrClosed`, the
context's cause and the other errors of `Eval` on as they are. The errors of
`New` reach its caller (`wl`, `windmill-lite selfcheck`, `wltest check`)
directly.

## How an evaluation runs

1. **A slot.** The engine owns a pool of slots. A slot is a thread created
   in C with a stack of its own size, on which a Go loop drives one runtime
   at a time. Each runtime is a QuickJS-ng runtime and *full context*: the
   context that QuickJS-ng's `JS_NewContext` builds, with every intrinsic
   that it adds.
2. **Waiting.** `Eval` hands the job to the next slot that has a prepared
   runtime. When none has, the caller waits, and waiting callers are served
   in arrival order. `Stats` counts these waits.
3. **Binding.** The runtime was prepared before the job arrived.
   - The evaluation first restarts the context's `performance` clock, so
     that `performance.now()` counts from the evaluation's start. It does
     so before the bindings, so that a binding named `performance` replaces
     it.
   - Each `HostString` becomes a writable, enumerable and configurable
     global property, holding its text as a string. The program parses it.
   - Each `HostFunc` becomes a global function that returns a Promise
     ([Host calls](#host-calls)).
4. **Running.**
   - Each of the input's preludes runs, in order, as a global script of the
     context. Its top-level `let`, `const` and `class` declarations become
     global lexical bindings that the program sees, which no property of
     `globalThis` gives. Its completion value is discarded. A prelude that
     throws ends the evaluation with its exception.
   - Then the bytecode runs, and every pending job runs, the jobs that
     those jobs queue included, until the queue is empty. Only then is the
     completion value read, as Windmill's evaluator reads it
     ([JE-51](../../../docs/semantics/expressions.md#je-51)).
   - A job that throws (a `queueMicrotask` callback, say) is dropped, and
     its exception fails nothing.
   - A completion value that is not a Promise is settled at once, and so is
     a Promise that has fulfilled or rejected. A pending Promise waits for
     a host call to settle, after which the queue empties again. A chain of
     jobs that never ends runs until the attempt limit.
5. **The answer.**
   - A fulfilled string is returned as UTF-8, except that QuickJS-ng writes
     an unpaired surrogate as three bytes (WTF-8). `JSON.stringify` escapes
     those, so its text is plain UTF-8. Anything else gives
     `expr.ErrNotString`.
   - A throw or a rejection gives an `*expr.Exception` that keeps the thrown
     kind: `Error`, `null`, `undefined` or other.
   - For an `Error`, it keeps the `message` and the `stack` as Windmill's
     embedding reads them, first and in that order. Each is converted to a
     string, and is absent when it is `null` or `undefined` or its
     conversion throws.
   - It keeps the `Error`'s `name` too, read from data properties along the
     prototype chain only, never through a getter or a Proxy. It also keeps
     a text that joins the name and message as `Error.prototype.toString`
     does, without calling it.
   - For another value, it keeps its kind as rquickjs 0.11.0 classifies it:
     by tag, and an object as the first of array, constructor, function,
     promise and proxy that it is, else object. It keeps the payload: a
     number, a boolean, a string's bytes, or the engine's address of a
     symbol or an object; and the string form of a string, a number or a
     boolean.
   - A symbol or an object is never converted, so its `toString`,
     `Symbol.toPrimitive` or traps do not run. Neither is a BigInt, whose
     decimal text can take longer to write than the attempt has left. The
     pipeline writes Windmill's texts from what is kept
     ([JE-21](../../../docs/semantics/expressions.md#je-21)).
   - rquickjs tests a promise with `JS_PromiseState(…) >= 0`, which works
     because the quickjs-ng that it vendors adds
     `JS_PROMISE_NOT_A_PROMISE = -1`. This revision's enum has no negative
     member, so the engine tests the class with `JS_IsPromise`.
6. **Replenishing.** The slot replies first, then frees the runtime and
   prepares the next one, off the caller's path. A runtime is never reused:
   nothing survives from one evaluation to the next
   ([JE-1](../../../docs/semantics/expressions.md#je-1)). How the runtime is
   freed depends on the heap's mode ([Allocator](#allocator)).

### Host calls

A call of a `HostFunc` from the program:

- converts its first argument with `ToString` (a missing one is
  `"undefined"`). A conversion that throws makes the call throw;
- runs `Call` on its own goroutine, with a context that ends with the
  attempt. A `Call` still running when the evaluation ends is abandoned,
  and its result is dropped;
- runs a `HostFunc` marked `Inline` (one that never blocks, such as the
  pipeline's results lookup when it reads memory) on the slot's thread
  instead, when the calls are taken. The attempt's end is checked when it
  returns;
- settles inline answers in call order, taking turns with the goroutine
  calls that have answered, so a chain of inline calls cannot keep an
  answered call waiting;
- waits, queued in call order, while 64 calls of the evaluation run or hold
  an inline answer not yet settled;
- settles its Promise only after the jobs queued so far have run. The
  Promise resolves with the returned text, or rejects with an `Error` whose
  message is `err.Error()`.

The rejection's `Error` is built with no frames on the stack. So its
`stack` is empty, unless the program set `Error.prepareStackTrace`, which
then runs.

## Limits

| Limit | Default | What happens |
|---|---|---|
| Heap | 128 MiB (`Limits.MemoryMB`) | Allocation fails past the cap. QuickJS-ng then throws an `InternalError` with the text `out of memory`, or a bare `null` or `undefined`, and the exception keeps that kind ([JE-3](../../../docs/semantics/expressions.md#je-3)) |
| Stack | 1 MiB (`Limits.StackBytes`), scaled to the build | QuickJS-ng's built-in default, which Windmill's embedding never changes ([JE-48](../../../docs/semantics/expressions.md#je-48)). Overflow is a catchable `RangeError`, `Maximum call stack size exceeded` ([Stack limit](#stack-limit)) |
| Attempt | 20 s (`Limits.Attempt`) | The attempt is interrupted, and `Eval` returns `expr.ErrAttemptTimeout` ([JE-2](../../../docs/semantics/expressions.md#je-2)). A Promise that never settles also ends here |
| Context | The caller's `ctx` | When `ctx` ends during an evaluation, the program is interrupted the same way, and `Eval` returns `context.Cause(ctx)`, whatever the program got to |

- **The heap cap covers the engine's host-call bookkeeping:** one record
  per outstanding call, reused once the call settles, and the argument
  copies of calls not yet handed to Go, which are freed as Go copies them.
- **The readback** of the completion value or the exception gets 16 MiB of
  headroom above the cap.
- **The interrupt** goes through QuickJS-ng's interrupt handler, which the
  engine polls in loops, calls and regular-expression matching. The program
  cannot catch it.
- **A long built-in or readback** that does not reach the handler
  (`"x".repeat(n)` on a large `n`, for one) still ends the attempt.
  Whatever the program got to, `Eval` returns `expr.ErrAttemptTimeout` once
  the limit has passed, and the slot prepares a new runtime.
- **Windmill leaves a timed-out evaluation running.** Interrupting it is a
  [named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations).

### Stack limit

Each evaluation gets `Limits.StackBytes` scaled to the C stack that one
JavaScript call takes in the engine's build (`StackLimit`). A recursion
such as `f = n => n === 0 ? 0 : 1 + f(n - 1)` then fits about
`StackBytes` ÷ 768 B calls on every build measured: about 1 365 at the
default 1 MiB.

- **The depth is a deviation.** Windmill stops at 1 MiB of C stack, where a
  recursion of depth 1 000 overflows. windmill-lite fits about a third more
  on every build: a
  [named deviation](../../../docs/design/0001-equivalence-contract.md#5-named-deviations)
  ([JE-48](../../../docs/semantics/expressions.md#je-48)).
- **Cheaper builds.** The scaling holds for every build whose call takes
  768 B or more. A build with cheaper calls keeps the configured size, and
  fits more.
- **What keeps the configured size.** `Limits()` and the bundle manifest
  keep the configured size, not the scaled one.

The scaling exists because the C stack of one call depends on the compiler.
GCC 14 and 15 inline more small functions into the interpreter's
`JS_CallInternal` than GCC 13 does, which doubles its frame. Measured on
`linux/amd64`, the recursion above takes 784 B per call with GCC 13 and
1 728 B with GCC 15, and clang builds take 1 008 to 1 040 B
([measurements behind the decisions](../../../docs/design/README.md#measurements-behind-the-decisions)).

The `Print the stack measurements` step of the `Expression engine` CI jobs
prints the cost and the depths below on every run
([docs/ci.md](../../../docs/ci.md)).

The engine measures and applies the cost in these steps:

1. When a process starts its first engine, `CallStackBytes` runs the
   recursion on a runtime of its own, on a thread with an 8 MiB stack. It
   runs it until it overflows a 256 KiB limit, then a 4 MiB limit.
2. It divides the 3.75 MiB between the two limits by the difference between
   the two depths. The frames above the recursion cancel out.
3. A cost under 64 B or over 16 KiB per call is refused as a broken
   measurement. `New` then fails with an error that names the bytes, and
   the next call measures again.
4. Every engine gives QuickJS-ng `StackBytes` × cost ÷ 768 B, rounded up.
   A build whose calls take less than 768 B keeps `StackBytes` unscaled.

Where the limit applies:

- **Where it is measured from.** The limit counts from where each call
  enters the engine: the top level, each round of the job loop, each
  settlement.
- **Every check gets the scaled bytes,** also those in the parser, in
  `JSON.parse` and in `JSON.stringify`. Those checks have frames of their
  own, which also differ by compiler, so their depth differs between
  builds.
- **Other functions.** A function with more locals or arguments than the
  recursion above takes the same extra stack per call on every build, so its
  depth varies a little with the build too.
- **The thread's stack.** A slot thread's own stack is at least 8 MiB, and
  at least 4 MiB more than the scaled limit, in whole MiB. So a deep
  recursion meets the engine's error, never the end of the thread. Threads
  that the Go runtime creates would have the C library's default stack,
  which is 128 KiB on musl.

Measured at the default limit on `linux/amd64`, every build fits 1 362
calls of the recursion above. `JSON.parse` nests 8 578 levels with `zig
cc`, 13 339 with GCC 13 and 29 426 with GCC 15
([measurements behind the decisions](../../../docs/design/README.md#measurements-behind-the-decisions)).

## Vendored sources and licences

| Directory | Project | Revision | Licence | File list |
|---|---|---|---|---|
| `quickjs-ng/` | QuickJS-ng's engine sources | `be664ca1ccd86cbadf8df7348b8e15e77a32a1ba` | MIT | [NOTICE](quickjs-ng/NOTICE) |
| `mimalloc/` | mimalloc v3.5.3, the allocator of the engine's heap | `d4881d338125e1cb7c47ba4cfb398d6f7c0c8d45` | MIT | [NOTICE](mimalloc/NOTICE) |

- **Unmodified.** Both are copied unmodified. Each `NOTICE` lists every file
  with its SHA-256, and `TestVendoredSourcesAreUnmodified` checks them.
- **How they compile.** The `qjs_*.c` files compile them as part of this
  package, and `wl_quickjs.c` is the thin C layer that the Go slot loop
  calls.
- **Build flags.** The C sources are built with `-O2 -DNDEBUG`. QuickJS-ng's
  assertions are off, because a program can reach one of them: the one in
  `JS_FreeRuntime` that checks for leaked objects. The heap's modes handle
  those leaks instead.
- **Compilers.** Building needs cgo and a C compiler: the system clang on
  macOS. The Linux release binaries are static, built with `zig cc` against
  musl.

## Allocator

The engine allocates every block of a runtime's heap with mimalloc
(`wl_quickjs.c`), unless it is built with the `wl_libc_malloc` tag
(below). mimalloc does not replace the C library's `malloc`, which
the rest of the program keeps. A runtime's heap has two modes:

| Mode | Allocates from | How a runtime is freed | Leaks |
|---|---|---|---|
| Default | A mimalloc heap of the runtime's own (`mi_heap_malloc` and the rest), made on the slot's thread | The slot destroys the heap whole (`mi_heap_destroy`) instead of freeing the runtime block by block. QuickJS-ng's teardown does not run, which nothing can observe: no JavaScript runs while a runtime is freed | A block that QuickJS-ng fails to free is in the heap, so it is reclaimed with the rest, uncounted |
| Leak check (`Options.LeakCheck`, and always with the `wl_libc_malloc` tag) | The thread's heap (`mi_malloc`), or the C library's `malloc` with the `wl_libc_malloc` tag | Every block that a runtime allocates is linked into the slot's list. After `JS_FreeRuntime`, the slot frees the blocks still linked | Counted in `Stats().LeakedBlocks` |

- **The cap** counts each block's usable size, less the engine's own
  header in leak check (`heap_usable_size`).
- **Known leaks.** QuickJS-ng fails to free blocks for some programs, such
  as one that assigns an object to `Error.stackTraceLimit`. Any other leak
  is a bug.
- **Why destroy the heap whole.** It saves the per-block frees and
  QuickJS-ng's teardown of every object. `BenchmarkEvalCPU` compares the
  two modes.
- **Why mimalloc.** musl's `malloc`, which the static Linux engine links,
  makes every slot thread wait on one lock. With mimalloc, the glibc and
  musl builds measure the same under load
  ([where semantics live](../../../docs/design/0003-semantics-placement.md)),
  and the heap accounting no longer depends on the C library's block
  sizes.
- **musl.** On a Linux C library other than glibc, `qjs_mimalloc.c` sets
  mimalloc's `MI_LIBC_MUSL` (musl defines no macro of its own). That only
  stops mimalloc from reading the thread pointer directly.
- **The `wl_libc_malloc` build tag** (`alloc_libc.go`) puts the C library's
  allocator back, with leak check always on: the C library has no heap to
  destroy whole. Only measurements use it.

The CPU per evaluation with 8 callers back to back, measured by
`TestPoolUnderLoad` in the `Compare the expression pool's allocators` step
of the `Static Linux engine` CI jobs, in CI run 40709537760
([measurements behind the decisions](../../../docs/design/README.md#measurements-behind-the-decisions)):

| Runner | Bound value | musl and mimalloc (release) | musl's malloc | glibc's malloc |
|---|---|---|---|---|
| `linux/arm64` | 4 KiB | 189 µs | 2.27 ms | 261 µs |
| `linux/arm64` | 256 KiB | 1.46 ms | 12.9 ms | 1.69 ms |
| `linux/amd64` | 4 KiB | 340 µs | 1.74 ms | 433 µs |
| `linux/amd64` | 256 KiB | 2.82 ms | 8.45 ms | 3.04 ms |

That step prints the pool's table for four builds in its job summary on
every run: musl or glibc, each with mimalloc and with its own `malloc`. The
mimalloc builds destroy each runtime's heap, and the others free it block
by block. So the table compares the release configuration with each C
library's allocator, not the allocators alone.

## Engine cases

- **`testdata/engine/<finding>.json`** holds the engine-level cases of four
  findings: a fresh runtime and its globals
  ([JE-1](../../../docs/semantics/expressions.md#je-1)), the attempt limit
  ([JE-2](../../../docs/semantics/expressions.md#je-2)), the memory limit
  ([JE-3](../../../docs/semantics/expressions.md#je-3)) and the stack limit
  ([JE-48](../../../docs/semantics/expressions.md#je-48)). Each case encodes
  a catalogue case (`docs/semantics/conformance.md`) or a rule of its
  finding.
- **A case** gives a program, its bindings and limits, and the outcome that
  the engine itself produces: the result text, the exception's kind, name
  and message, `out_of_memory`, a compile exception, `attempt_timeout`, or
  `not_string`.
- **The runner** wraps a case's `expr` as an async function that awaits the
  value and returns `JSON.stringify(value ?? null)`, a stand-in for the
  pipeline's wrapper.
- **Coverage.** `TestEngineCasesCoverTheCatalogue` fails unless every
  catalogue case of those four findings is encoded, with the catalogue's
  provenance.
- **Deviations.** A case that shows a named deviation names it: the
  interrupted attempt, and the recursion depth. The case `depth-1000` of
  `testdata/engine/je-48.json`, a recursion of depth 1 000 that overflows on
  Windmill, fits this engine.
- **`testdata/globals.json`** pins the names that the full context defines.
  [JE-1](../../../docs/semantics/expressions.md#je-1) lists what Windmill's
  context lacks and has, among them `queueMicrotask`, `performance` and
  `DOMException`, which QuickJS-ng's full context adds. The oracle recorded
  those names ([Q-JE-2](../../../docs/semantics/expressions.md#q-je-2),
  answered).

## Tests

### Run the tests

The tests need cgo and a C compiler.

1. Run them with the race detector, as CI's Go buckets run the tests of
   every package: `go test -race ./internal/expr/quickjs`.
2. Run them in leak check, as CI's `Test the expression packages in leak
   check` step does: `WL_QJS_LEAK_CHECK=1 go test -race -count=1 ./internal/expr/...`.
3. Print the stack measurements, as the `Expression engine` jobs do:
   `go test -count=1 -v -run '^(TestCallStackBytes|TestOverflowDepthAtTheDefaultStack|TestProbeMatchesEvaluations)$' ./internal/expr/quickjs`.
4. Measure the pool under load, as CI's `Measure the expression pool under
   load` step does:
   `WL_QUICKJS_LOAD=1 WL_QUICKJS_LOAD_SECONDS=1 go test -count=1 -run '^TestPoolUnderLoad$' -v ./internal/expr/quickjs`.

CI builds and tests this package with these compilers
([docs/ci.md](../../../docs/ci.md#the-ci-workflow)):

- the GCC of `ubuntu-latest`, in the Go jobs on Linux;
- the GCC of `ubuntu-24.04` and `ubuntu-24.04-arm`, in the glibc builds of
  the allocator comparison in the `Static Linux engine` jobs;
- Apple clang, in the Go jobs on macOS;
- GCC 13 and GCC 15, on the two Ubuntu images of the `Expression engine`
  jobs;
- `zig cc` against musl, as a static build for `amd64` and `arm64`, in the
  `Static Linux engine` jobs.

### Environment variables and build tags

| Variable or tag | Value | Tests that need it | When it is unset |
|---|---|---|---|
| `WL_QJS_LEAK_CHECK` | `1` | Every engine that the tests' helpers `newEngine` and `newLeakyEngine` start runs in leak check, so the check that it ends with no leaked heap block counts blocks. Tests that call `New` themselves set their own mode | Those engines run in the default mode and count no leaks, unless the package is built with the `wl_libc_malloc` tag |
| `WL_QUICKJS_LOAD` | `1` | `TestPoolUnderLoad` | `TestPoolUnderLoad` skips |
| `WL_QUICKJS_LOAD_SECONDS` | Seconds | `TestPoolUnderLoad`: the length of each row | 2 s |
| `wl_libc_malloc` (build tag) | `go test -tags wl_libc_malloc ./internal/expr/quickjs` | The allocator comparison. The tag always runs leak check, so `WL_QJS_LEAK_CHECK` changes nothing then | mimalloc |

### Leak check in the tests

An engine started through the tests' helper `newEngine` must end with no
leaked heap block. The engine counts leaked blocks only in leak check, which
`WL_QJS_LEAK_CHECK=1` sets, and which one CI step sets for this package and
[internal/expr](../README.md). The two tests that make QuickJS-ng leak do
without that check: `TestEngineLeaksAreReclaimed` uses `newLeakyEngine`, and
`TestDestroyedHeapsReclaimLeaks` calls `New` itself.

### Benchmarks and the load test

- `BenchmarkEvalCPU` reports the CPU per evaluation, preparing and freeing
  the runtimes included, in the default mode and in leak check:
  `go test -run '^$' -bench BenchmarkEvalCPU ./internal/expr/quickjs`.
- `TestPoolUnderLoad` measures latency under the load shapes of the
  ADR Where semantics live, and prints a table. It sets no threshold. It
  runs only with `WL_QUICKJS_LOAD=1`.

### Test files

Every test needs cgo and a C compiler; only the load test needs a variable.

| File | What it checks |
|---|---|
| `engine_test.go` | The evaluation itself, host strings and host functions, exceptions, the compile cache, runtimes, the clock, interruption, the preludes, `Close`, `Options` and the stack depths (below) |
| `robustness_test.go` | Host calls under stress, edge cases of bindings and host functions, late completions, and the leak tests (below) |
| `inline_test.go` | Inline host functions (below) |
| `stack_test.go` | The stack limit (below) |
| `conformance_test.go` | `TestEngineCases` runs `testdata/engine/`; `TestEngineCasesCoverTheCatalogue` |
| `load_test.go` | `TestPoolUnderLoad`, which runs only with `WL_QUICKJS_LOAD=1` |
| `bench_prepare_test.go` | `BenchmarkEvalCPU` |
| `testmode_test.go` | `testOptions`: `WL_QJS_LEAK_CHECK` for the helpers `newEngine` and `newLeakyEngine` of `engine_test.go` |

**The tests of `engine_test.go`.**

| Test | What it checks |
|---|---|
| `TestEvalReturnsTheCompletionString`, `TestEvalRejectsANonStringCompletion` | The completion string, and `expr.ErrNotString` |
| `TestEvalRunsEveryQueuedJob` | Every queued job runs before the completion is read |
| `TestHostStringsAreGlobalStrings` | A `HostString` is a global string property |
| `TestHostFuncResolvesWithText`, `TestHostFuncRejectsWithAnError` | A host call resolves with its text, or rejects with an `Error` |
| `TestHostCallsRunConcurrently`, `TestHostResultSettlesAfterQueuedJobs`, `TestHostCallSeesTheAttemptEnd` | Host calls run at once, settle after the queued jobs, and see the attempt's end |
| `TestExceptions`, `TestCompileErrorsAreExceptions` | The kinds of thrown values, and compile errors as exceptions |
| `TestCompileCache`, `TestFreshRuntimePerEvaluation` | The compile cache, and a fresh runtime per evaluation |
| `TestPerformanceClockStartsWithTheEvaluation`, `TestGlobalSurface` | The `performance` clock, and the global names against `testdata/globals.json` |
| `TestContextEndsTheEvaluation`, `TestInterruptedSlotPreparesAFreshRuntime` | Cancellation, and a fresh runtime after an interrupt |
| `TestForeignPrograms` | A program from another engine is refused |
| `TestPreludes`, `TestCLibrary`, `TestClose`, `TestWaitingCallersAreServedInArrivalOrder`, `TestOptions` | The preludes, `CLibrary`, `Close`, the arrival order of waiting callers, and `Options` |
| `TestOverflowDepthAtTheDefaultStack`, `TestLargeStackLimitStaysInsideTheThread` | The stack depths (below) |
| `TestConcurrentEvaluations` | Concurrent use under `-race` |
| `TestVendoredSourcesAreUnmodified` | The vendored sources against their `NOTICE` files |

**The tests of `robustness_test.go`.**

| Test | What it checks |
|---|---|
| `TestSettlingSurvivesProgramCodeThatCallsTheHost` | Host calls from program code while a call settles |
| `TestHostCallArgumentsCountAgainstTheCap`, `TestSequentialHostCallsDoNotAccumulate` | Arguments count against the heap cap, and sequential calls' arguments do not add up past it |
| `TestHostCallsInFlightAreBounded`, `TestSettledCallRecordsAreReused` | The bound of 64 calls in flight, and the reuse of call records |
| `TestBindingNamedLikeAGlobalKeepsItsValue`, `TestManyHostFunctions`, `TestHostArgumentWhoseToStringThrows`, `TestHostFuncWithoutCall` | Edge cases of bindings and host functions |
| `TestUnpairedSurrogatesComeBackAsWTF8` | An unpaired surrogate comes back as WTF-8 |
| `TestAThrowingJobIsDropped` | A job that throws is dropped |
| `TestAttemptEndWinsOverALateCompletion`, `TestCancellationDuringAHostCall`, `TestCancellationWinsOverALateCompletion` | An attempt's end or a cancellation against a late completion, or during a host call |
| `TestEngineLeaksAreReclaimed`, `TestDestroyedHeapsReclaimLeaks` | The leak tests (below) |

**The leak tests.**

- `TestEngineLeaksAreReclaimed` runs in leak check. It checks that a known
  leak is seen and freed, and that the slot recovers.
- `TestDestroyedHeapsReclaimLeaks` checks the default mode. In leak check, a
  program first shows that it leaks an 8 MiB string per evaluation. Then,
  after 100 evaluations of it in the default mode, mimalloc's committed
  memory has grown by at most 64 MiB, `LeakedBlocks` is 0 (not counted),
  and a 48 MiB string still fits under a 64 MiB cap.
- The interrupt and cancellation tests also cover a long built-in or
  readback, a pending host call, and a completed readback that is about to
  be returned (the tests reach that through `attemptReturned`).

**Inline host functions** (`inline_test.go`):

- their answers settle in call order (`TestInlineHostCallsSettleInCallOrder`);
- a chain of inline calls, each settlement making the next, lets a goroutine
  call that has answered settle (`TestInlineChainLetsAnsweredCallsSettle`).
  A chain of goroutine calls lets an inline answer settle
  (`TestRunningChainLetsInlineAnswersSettle`). Both settle within a second,
  not at the attempt limit;
- with 2,000 queued calls, the engine holds exactly the bound, 64, at once
  (`TestInlineHostCallsAreBounded`);
- programs give the same outcome with their functions inline and on
  goroutines: answers, rejections, chained and mixed calls, `Promise.all`,
  `allSettled` and `race`, under `-race`
  (`TestInlineHostCallsMatchGoroutineCalls`);
- a call that blocks past the attempt limit still ends the attempt with the
  limit's error (`TestInlineHostCallKeepsTheAttemptLimit`).

**What the stack tests assert** (`stack_test.go`, and
`TestOverflowDepthAtTheDefaultStack` and
`TestLargeStackLimitStaysInsideTheThread` in `engine_test.go`). The tests
log the measured stack per call and the overflow depth for each platform,
and check that:

- the default limit fits at least 1 351 calls, 99 % of 1 365;
- evaluations take the stack per call that the probe measured, within 6
  bytes, less than the 16 bytes that another function shape would take
  (`TestProbeMatchesEvaluations`);
- a 6 MiB limit, scaled, still ends in a `RangeError` inside the slot's
  thread (`TestLargeStackLimitStaysInsideTheThread`);
- a measured cost under 64 B or over 16 KiB is refused
  (`TestPlausibleCallStack`);
- the limit follows the scaling rule and its rounding
  (`TestStackLimitScalesToTheCallStack`), and the thread's stack its sizes
  (`TestThreadStack`);
- `New` refuses a limit too large for a thread (`TestStackLimitTooLarge`);
- the cost is measured once, and `StackLimit` scales while
  `Limits().StackBytes` keeps the configured size (`TestCallStackBytes`).

`TestOverflowDepthAtTheDefaultStack` also logs how deep `JSON.parse`,
`JSON.stringify` and parenthesised source text nest at the default limit.

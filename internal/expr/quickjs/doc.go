// Package quickjs is windmill-lite's expression engine: QuickJS-ng at
// revision be664ca1 (the engine Windmill's expressions run on, JE-1),
// vendored as C source under quickjs-ng/ and embedded natively through cgo
// (ADR 0003, milestone B1a). *Engine implements expr.Engine.
//
// An Engine owns a pool of slots. A slot is a thread created in C with a
// stack of its own size, on which a Go loop drives one prepared runtime at a
// time: it takes a job, runs it on a fresh runtime and full context (JE-1),
// replies, frees the runtime and prepares the next one before it takes
// another job. Callers that find no prepared slot wait in FIFO order.
//
// The limits of expr.Limits apply per evaluation: the heap cap (JE-3), the
// stack limit, measured from where each call enters the engine (JE-48), and
// the attempt time limit (JE-2), which interrupts the program (deviation D8).
// The texts that Windmill wraps around these failures are the pipeline's.
package quickjs

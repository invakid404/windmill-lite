package expr

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Engine is the JavaScript engine that runs expression programs: QuickJS-ng
// at revision be664ca1 embedded natively (ADR 0003; implemented by
// internal/expr/quickjs, milestone B1a). The interface keeps the backend
// replaceable.
//
// An Engine is safe for concurrent use. It knows nothing about flows: the
// meaning of an expression is in the program source that the pipeline
// builds and in the inputs it binds.
type Engine interface {
	// Compile compiles program source once, when a bundle opens. A source
	// that does not compile returns an *Exception holding the engine's error
	// (a SyntaxError); the pipeline records it and raises it at evaluation
	// time, as Windmill reports it (ADR 0003).
	Compile(source string) (Program, error)

	// Eval runs a compiled program on a fresh runtime and context that
	// nothing else has used and nothing will reuse (JE-1), with the host
	// inputs of in, and returns the program's result text.
	//
	// Before the program runs, each HostString is a global property holding
	// its text as a JavaScript string, and each HostFunc is a global
	// function; nothing else is defined beyond the engine's built-ins. Then
	// each of in.Preludes runs, in order, as a global script of the same
	// context. The program's completion value must be a string, or a
	// Promise that settles with one; Eval runs pending jobs until it settles
	// and returns the string as UTF-8.
	//
	// Errors:
	//   - *Exception: the program threw, or its Promise rejected;
	//   - ErrAttemptTimeout: the attempt reached Limits.Attempt and was
	//     interrupted (deviation D8);
	//   - ErrNotString: the program completed with something other than a
	//     string;
	//   - context.Cause(ctx), when ctx ended before or during the
	//     evaluation, which is then interrupted.
	Eval(ctx context.Context, p Program, in Input) ([]byte, error)

	// Limits returns the per-evaluation limits the engine applies, which
	// Windmill's time-limit and memory-limit messages quote (JE-2, JE-3).
	Limits() Limits
}

// Program is a program compiled by an Engine, for example its bytecode. Only
// the Engine that compiled it can run it.
type Program any

// Input is what one evaluation binds: context values as host strings that
// the program parses once (ADR 0003: values are never spliced into source
// text), host functions for lookups the program awaits, and the preludes
// that run before the program.
type Input struct {
	Strings []HostString
	Funcs   []HostFunc
	// Preludes are programs compiled by the same Engine that run, in order,
	// after the bindings and before the program, each as a global script of
	// the evaluation's context. A prelude's top-level let, const and class
	// declarations are therefore global lexical bindings that the program
	// sees, which no property of globalThis can give (JE-10). A prelude's
	// completion value is discarded, and pending jobs do not run between
	// preludes. A prelude that throws ends the evaluation with its
	// exception, and the program does not run.
	Preludes []Program
}

// HostString is a global string property: Text, a JSON text in practice,
// exposed as a JavaScript string without being parsed.
type HostString struct {
	Name string
	Text []byte
}

// HostFunc is a global function that returns a Promise. Call receives the
// first argument (the program normalises arguments before calling) and
// returns text that resolves the Promise as a string; a non-nil error
// rejects it with an Error whose message is err.Error(). The results
// lookups of JE-18 and the helpers of JE-11 and JE-20 are host functions.
type HostFunc struct {
	Name string
	Call func(ctx context.Context, arg string) ([]byte, error)
}

// Thrown says what kind of value a program threw. QuickJS-ng signals running
// out of memory either as an InternalError whose text is exactly "out of
// memory" or as a bare null or undefined throw, and Windmill reads all three
// as out of memory (JE-3), so the kind is kept. Any other InternalError, such
// as a stack overflow, is an ordinary error.
type Thrown uint8

const (
	// ThrownError is an Error object (any value whose name and message the
	// engine read).
	ThrownError Thrown = iota + 1
	// ThrownNull is a bare throw null.
	ThrownNull
	// ThrownUndefined is a bare throw undefined.
	ThrownUndefined
	// ThrownOther is any other thrown value.
	ThrownOther
)

// Exception is a value a program threw, or with which its Promise rejected.
// It carries the engine's own texts; the messages that wrap them are the
// pipeline's (B2b).
type Exception struct {
	Thrown Thrown
	// Name and Message are the Error object's name and message, for
	// ThrownError.
	Name    string
	Message string
	// Text is the thrown value converted to a string by the engine, for
	// example "ReferenceError: x is not defined".
	Text string
	// Stack is the Error object's stack, if it has one.
	Stack string
}

func (e *Exception) Error() string { return "expr: JavaScript exception: " + e.Text }

// ErrAttemptTimeout reports an evaluation attempt that reached its wall-clock
// limit and was interrupted (JE-2, deviation D8).
var ErrAttemptTimeout = errors.New("expr: evaluation attempt reached its time limit")

// ErrNotString reports a program whose completion value is not a string.
var ErrNotString = errors.New("expr: program did not complete with a string")

// Limits are an Engine's per-evaluation limits.
type Limits struct {
	// MemoryMB caps one runtime's heap at MemoryMB × 2^20 bytes (JE-3).
	// Hosts may set it from QUICKJS_MEMORY_LIMIT_MB, as on Windmill.
	MemoryMB int
	// Attempt is the wall-clock limit of one attempt (JE-2); an attempt that
	// reaches it is interrupted (D8).
	Attempt time.Duration
	// StackBytes is the engine's maximum stack size, measured from where
	// each call enters the engine (JE-48). It is observable as
	// stack-overflow depth, so it is pinned and recorded in the bundle
	// manifest (ADR 0003).
	StackBytes int
}

// DefaultLimits returns Windmill's limits: 128 MiB per runtime (JE-3), 20 s
// per attempt (JE-2), and QuickJS-ng's built-in maximum stack of 1 MiB,
// which Windmill's embedding never changes (JE-48).
func DefaultLimits() Limits {
	return Limits{MemoryMB: 128, Attempt: 20 * time.Second, StackBytes: 1 << 20}
}

// Validate reports a limit that is not positive.
func (l Limits) Validate() error {
	switch {
	case l.MemoryMB <= 0:
		return fmt.Errorf("expr: memory limit %d MB is not positive", l.MemoryMB)
	case l.Attempt <= 0:
		return fmt.Errorf("expr: attempt limit %v is not positive", l.Attempt)
	case l.StackBytes <= 0:
		return fmt.Errorf("expr: stack size %d is not positive", l.StackBytes)
	}
	return nil
}

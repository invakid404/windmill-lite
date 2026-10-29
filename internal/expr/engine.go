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
	// context. Every script is a strict-mode global script (JE-50). The
	// program's completion value is a string, or a Promise that settles
	// with one, which Eval returns as the engine converts it to UTF-8: an
	// unpaired surrogate comes back as its three-byte (WTF-8) form, which is
	// not UTF-8.
	//
	// Eval reads the completion value only once no job is queued: it runs
	// every queued job, the jobs those jobs queue included, in order, before
	// it looks, and again after each host call settles. A job that throws is
	// dropped, and fails nothing (JE-51).
	//
	// Errors:
	//   - *Exception: the program threw, or its Promise rejected;
	//   - ErrAttemptTimeout: the attempt reached Limits.Attempt and was
	//     interrupted (deviation D8);
	//   - ErrNotString: the program completed with something other than a
	//     string, which no conversion turns into one;
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
	// ThrownError is an object of the engine's Error class, an instance of
	// any of its subclasses included; an object that only looks like one is
	// ThrownOther (JE-21).
	ThrownError Thrown = iota + 1
	// ThrownNull is a bare throw null.
	ThrownNull
	// ThrownUndefined is a bare throw undefined.
	ThrownUndefined
	// ThrownOther is any other thrown value.
	ThrownOther
)

// Exception is a value a program threw, or with which its Promise rejected.
// It carries the engine's own texts and what it knows of the value; the
// messages written from them are the pipeline's (B2b).
//
// Texts are as the engine converts them to UTF-8, so an unpaired surrogate
// comes back in its three-byte form, which is not UTF-8 (JE-52).
type Exception struct {
	Thrown Thrown
	// Name is the Error object's name, for ThrownError.
	Name string
	// Message and Stack are the Error object's message and stack, for
	// ThrownError, each converted to a string as Windmill's embedding reads
	// it. HasMessage and HasStack report whether there is one: there is
	// none when the property is undefined or null, or when reading it or
	// converting it to a string throws.
	Message    string
	HasMessage bool
	Stack      string
	HasStack   bool
	// Value is what the engine knows of the thrown value, for ThrownOther.
	Value ThrownValue
	// Text is a diagnostic: for an Error, its name and message joined as
	// Error.prototype.toString joins them ("ReferenceError: x is not
	// defined"); for a thrown primitive other than a symbol, its string
	// form; and empty for a symbol or an object, whose conversion would run
	// the program's code. Reading the exception back runs none of it beyond
	// what Windmill's embedding runs: an Error's message and stack.
	Text string
}

// ThrownValue is a thrown value that is not an Error: its kind, as
// Windmill's embedding (rquickjs 0.11.0) classifies it, and its payload,
// from which the pipeline writes the value's debug form (JE-21).
type ThrownValue struct {
	Kind ValueKind
	// Int, Float and Bool are the payloads of KindInt, KindFloat and
	// KindBool.
	Int   int32
	Float float64
	Bool  bool
	// String is KindString's payload, as the engine converts it.
	String string
	// Address is the engine's address of a symbol or an object, which
	// changes from run to run.
	Address uintptr
}

// ValueKind is the kind of a thrown value that is not an Error: the
// engine's tag, and for an object the first of array, constructor,
// function, promise and proxy that it is.
type ValueKind uint8

const (
	KindUnknown ValueKind = iota
	KindBool
	// KindInt is a number the engine holds as a 32-bit integer.
	KindInt
	// KindFloat is any other number.
	KindFloat
	KindString
	KindSymbol
	KindArray
	// KindConstructor is a function the engine can construct with: a
	// class, or a function declared with function.
	KindConstructor
	// KindFunction is any other function, such as an arrow function.
	KindFunction
	KindPromise
	KindProxy
	KindObject
	KindBigInt
)

var valueKindNames = [...]string{
	KindUnknown:     "value",
	KindBool:        "boolean",
	KindInt:         "integer",
	KindFloat:       "number",
	KindString:      "string",
	KindSymbol:      "symbol",
	KindArray:       "array",
	KindConstructor: "constructor",
	KindFunction:    "function",
	KindPromise:     "promise",
	KindProxy:       "proxy",
	KindObject:      "object",
	KindBigInt:      "BigInt",
}

// String names the kind in lower case, for diagnostics.
func (k ValueKind) String() string {
	if int(k) < len(valueKindNames) {
		return valueKindNames[k]
	}
	return fmt.Sprintf("ValueKind(%d)", uint8(k))
}

func (e *Exception) Error() string {
	if e.Text == "" && e.Thrown == ThrownOther {
		return "expr: JavaScript exception: a thrown " + e.Value.Kind.String()
	}
	return "expr: JavaScript exception: " + e.Text
}

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

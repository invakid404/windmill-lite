package engine

import (
	"context"
	"errors"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// StepExecutor runs leaf invocations in language harnesses: the engine side
// of the harness protocol (ADR 0004; internal/harness implements it,
// milestone C1). It is safe for concurrent use: invocations run at once
// unless a host set a cap (deviation D15).
//
// Execute runs inv and returns its outcome. When ctx ends first (the step's
// timeout, the run's cancellation, the run's end), Execute sends the
// harness a cancel with CancelReasonOf(ctx), abandons the invocation
// (deviation D2) and returns at once with context.Cause(ctx); whatever the
// invocation produces afterwards reaches inv.Report as Late, possibly after
// the run has ended.
//
// A harness process that dies fails its in-flight invocations as
// OutcomeEscaped, which is an outcome, not an error. A non-nil error with no
// outcome means the invocation could not be run or its harness was lost
// with it (host mode); the interpreter fails the module with an error it
// generates.
type StepExecutor interface {
	Execute(ctx context.Context, inv Invocation) (StepOutcome, error)
}

// Invocation is one run of one leaf step: the invoke frame of ADR 0004.
type Invocation struct {
	// ID identifies the invocation for platform-call attribution, which
	// never relies on the token a step sends (ADR 0007).
	ID string
	// Run is the id of the run the invocation belongs to.
	Run string
	// Site is the module's site path; Attempt is the attempt number, from
	// 0, as the replay service keys it (ADR 0011). A failed attempt k is
	// recorded at Site/r:k.
	Site    conformance.SitePath
	Attempt int
	// Script is the script's inventory id; Entry is the export to call,
	// already validated against ^[A-Za-z_][A-Za-z0-9_]*$ (ADR 0004).
	Script string
	Entry  string
	// Args is the argument object after storage and $var:/$res:
	// resolution, as the script receives it (ADR 0005).
	Args jsonval.Value
	// Env is the invocation's job environment: the WM_* variables of WR-22
	// and WA-4 after host changes (H12).
	Env map[string]string
	// Boundary selects the serialisation rules for results and errors.
	Boundary Boundary
	// ModuleState is the module-state mode the invocation runs in
	// (ADR 0006).
	ModuleState ModuleState
	// Report receives what the invocation produces outside its outcome. It
	// is never nil; NopReporter discards.
	Report Reporter
}

// Boundary is a step's boundary mode (ADR 0002).
type Boundary string

const (
	// BoundaryBun is the Bun wrapper's rules (WR-13, WR-18).
	BoundaryBun Boundary = "bun"
	// BoundaryNative is the native runtime's rules (deviation D5), which
	// wait for semantics request 1.
	BoundaryNative Boundary = "native"
)

// ModuleState is a module-state mode (ADR 0006).
type ModuleState string

const (
	// ModuleStateFresh runs one harness process per invocation, as Windmill
	// does.
	ModuleStateFresh ModuleState = "fresh"
	// ModuleStateWarm keeps one module instance per script for the
	// process's lifetime (deviation D1). It is the default.
	ModuleStateWarm ModuleState = "warm"
)

// Reporter receives what an invocation produces outside its outcome, so
// that the run trace records it (deviation D9).
type Reporter interface {
	// Late reports something an invocation produced after it settled or was
	// abandoned.
	Late(Late)
	// Warning reports a process-level error that the harness could not
	// attribute to any invocation, raised while this one was in flight.
	Warning(Warning)
}

// NopReporter discards everything.
type NopReporter struct{}

// Late discards l.
func (NopReporter) Late(Late) {}

// Warning discards w.
func (NopReporter) Warning(Warning) {}

// OutcomeKind is how an invocation ended, as the harness saw it
// (ADR 0004).
type OutcomeKind string

const (
	// OutcomeOK: the entrypoint returned; Body is the serialised result.
	OutcomeOK OutcomeKind = "ok"
	// OutcomeError: the wrapper caught a throw; Body is the error object
	// (WR-18 for Bun).
	OutcomeError OutcomeKind = "error"
	// OutcomeEscaped: the error escaped the wrapper (a module or import
	// failure, throw null, a non-zero process.exit, an attributed
	// process-level error, a dead process); Tail, ExitCode and Signal
	// describe it (WR-20, WR-4).
	OutcomeEscaped OutcomeKind = "escaped"
	// OutcomeEmpty: process.exit(0), or no result (WR-V2).
	OutcomeEmpty OutcomeKind = "empty"
)

// StepOutcome is a leaf's outcome before the engine maps it to Windmill's
// stored value (WR-21, WR-V5). A host plugin that answers for a module (H1)
// returns the same type, serialised by the same function, so it is
// indistinguishable from a step's outcome (ADR 0005).
type StepOutcome struct {
	Kind OutcomeKind
	// Body is the producer text of the result (OutcomeOK) or of the error
	// object (OutcomeError); zero otherwise.
	Body jsonval.Value
	// Tail is the invocation's last log text (OutcomeEscaped).
	Tail string
	// ExitCode is the exit code an escaped error would have given the job's
	// process (OutcomeEscaped; 1 for an attributed error, Q-WR-2).
	ExitCode int
	// Signal names the signal that ended the harness process, such as
	// "SIGKILL", when one did (OutcomeEscaped, WR-4).
	Signal string
}

// CancelReason is why the engine abandons an invocation: the reason of a
// cancel frame (ADR 0004). It is an error, so the engine passes it as the
// cause of the invocation's context (context.WithCancelCause).
type CancelReason string

const (
	// CancelTimeout: the step's own timeout fired (FX-24).
	CancelTimeout CancelReason = "timeout"
	// CancelCanceled: the run was canceled, or reached a host deadline.
	CancelCanceled CancelReason = "canceled"
	// CancelRunEnded: the run ended while the invocation was in flight.
	CancelRunEnded CancelReason = "run_ended"
)

func (r CancelReason) Error() string { return "invocation abandoned: " + string(r) }

// CancelReasonOf returns the reason to send when ctx has ended: its cause
// when the cause is a CancelReason, and CancelCanceled otherwise, since a
// deadline or cancellation the engine did not name comes from the run.
func CancelReasonOf(ctx context.Context) CancelReason {
	var r CancelReason
	if errors.As(context.Cause(ctx), &r) {
		return r
	}
	return CancelCanceled
}

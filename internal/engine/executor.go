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
//
// Settlement. Every call of Execute leads to exactly one inv.Report.Settled
// call, which says that no code of the invocation's entrypoint call can run
// any more, so resources it could reach may be released (H9, ADR 0004 host
// mode item 5):
//
//   - when Execute returns an outcome, or an error that is not ctx's cause,
//     Settled has already been called, except for an outcome fixed before
//     the entrypoint call ended: an intercepted process.exit (D11), or an
//     attributed process-level error that fails the invocation as escaped
//     at once while its call may still be pending (ADR 0004, the
//     shared-process failure model, item 2). Such a call settles later,
//     when it ends (SettleCompleted, after its Final Late) or its process
//     does (SettleProcessExited);
//   - when Execute returns ctx's cause, Settled comes before or after the
//     return: SettleNotStarted if the invocation never reached a harness,
//     otherwise SettleCompleted when the harness reports that the call
//     ended, or SettleProcessExited when its process ends first. An
//     abandoned call that never ends in a process that lives on never
//     settles (ADR 0004: "possibly never").
//
// Work the call left running (D9: unawaited promises, timers) is not part of
// settlement; its output keeps arriving as Late.
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
	// BoundaryNative is the native runtime's rules (WR-40 to WR-45), which
	// the Bun harness emulates (deviation D5).
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
// that the run trace records it (deviation D9), and when the invocation
// settles, so that the engine knows when a run's resources may be released.
//
// The executor never calls one invocation's Reporter from two goroutines at
// once, and calls it in this order:
//
//  1. Late and Warning calls;
//  2. at most one Late with Final set: how an entrypoint call that ended
//     after its outcome was fixed, or after it was abandoned, came to an
//     end;
//  3. Settled, exactly once;
//  4. only Late calls, without Final: output of work the call left running
//     (D9), never the invocation's outcome.
type Reporter interface {
	// Late reports something an invocation produced after its outcome was
	// fixed or it was abandoned.
	Late(Late)
	// Warning reports a process-level error that the harness could not
	// attribute to any invocation, raised while this one was in flight, or
	// the work this one left behind when it settled (D9).
	Warning(Warning)
	// Settled reports that the invocation's entrypoint call can run no more
	// code, and how.
	Settled(SettleKind)
}

// SettleKind is how an invocation settled.
type SettleKind string

const (
	// SettleCompleted: the entrypoint call ended, by returning or throwing.
	// For an abandoned call, or one whose outcome was fixed before it ended
	// (an intercepted process.exit, D11, or an attributed process-level
	// error), the harness reports it later, after the Final Late that says
	// how the call ended.
	SettleCompleted SettleKind = "completed"
	// SettleNotStarted: the invocation never reached a harness: its context
	// ended before dispatch, or it could not be dispatched.
	SettleNotStarted SettleKind = "not_started"
	// SettleProcessExited: the invocation's harness process ended (an exit,
	// a signal, a lost host) before the entrypoint call did.
	SettleProcessExited SettleKind = "process_exited"
)

// NopReporter discards everything.
type NopReporter struct{}

// Late discards l.
func (NopReporter) Late(Late) {}

// Warning discards w.
func (NopReporter) Warning(Warning) {}

// Settled discards k.
func (NopReporter) Settled(SettleKind) {}

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

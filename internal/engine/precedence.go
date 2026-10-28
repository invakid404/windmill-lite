package engine

import (
	"fmt"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// Verdict is what a precedence check decides: hand over to Windmill's own
// checks, or end the run because of a host signal (ADR 0008).
type Verdict uint8

const (
	// Proceed: no host signal applies; Windmill's own checks decide (FX-2).
	Proceed Verdict = iota
	// EndLatched: the run starts nothing more and ends latched (H5). When
	// Completion returns it for a fatal error name (H7), the engine first
	// latches that outcome's error.
	EndLatched
	// EndCanceled: the run starts nothing more and ends canceled, without
	// retry, continue_on_error or the failure module (H4, FX-38).
	EndCanceled
	// EndHalted: the run ends halted with its trace so far (H8).
	EndHalted
)

// Status returns the run status the verdict ends the run with, and false
// for Proceed.
func (v Verdict) Status() (conformance.RunStatus, bool) {
	switch v {
	case EndLatched:
		return conformance.RunLatched, true
	case EndCanceled:
		return conformance.RunCanceled, true
	case EndHalted:
		return conformance.RunHalted, true
	}
	return "", false
}

func (v Verdict) String() string {
	switch v {
	case Proceed:
		return "proceed"
	case EndLatched:
		return "latched"
	case EndCanceled:
		return "canceled"
	case EndHalted:
		return "halted"
	}
	return fmt.Sprintf("Verdict(%d)", uint8(v))
}

// StartSignals are the host signals at a start phase: before a module,
// iteration, branch or retry attempt starts.
type StartSignals struct {
	// Latched: an error is latched on the run.
	Latched bool
	// Canceled: the run's context has ended: the host canceled it, or its
	// hard deadline, or its pre-write deadline before the first write,
	// passed (H4).
	Canceled bool
	// ContinuesErrorStop: this start is what Windmill's continue decision
	// asked for after an error stop that coincided with the cancellation,
	// in a flow with a failure module (Completion let it through, FX-38).
	// By FX-19 that is another attempt of the stopped module when a retry
	// is due, the next module under continue_on_error, or else the failure
	// module. It still starts; the run ends canceled at its next completion.
	ContinuesErrorStop bool
	// FailureModule: the module about to start is a flow's failure module.
	// Halting never applies to it, so that a failure stays visible.
	FailureModule bool
	// HaltBefore: H8's haltBefore matches the module about to start, at its
	// first attempt; a retry is not a new start of the module.
	HaltBefore bool
}

// Start decides a start phase's host checks, in ADR 0008's order: the latch,
// then cancellation and deadlines, then halt. Proceed hands over to
// Windmill's start checks (FX-2 start steps 1 to 10), where interception
// happens at launch (H1).
//
// A halt never changes a run's status from canceled: when it falls on a
// start that FX-38 lets through, the run ends canceled before that start.
// It never applies to a failure module.
func Start(s StartSignals) Verdict {
	halt := s.HaltBefore && !s.FailureModule
	switch {
	case s.Latched:
		return EndLatched
	case s.Canceled && (!s.ContinuesErrorStop || halt):
		return EndCanceled
	case halt:
		return EndHalted
	}
	return Proceed
}

// CompletionSignals are the host signals when a module completes.
type CompletionSignals struct {
	// Latched: an error is latched on the run.
	Latched bool
	// Canceled: the run's context has ended (see StartSignals.Canceled).
	Canceled bool
	// ErrorStopWithFailureModule: the outcome is an error stop (FX-19) and
	// the flow has a failure module. Windmill's continue decision checks a
	// stop before the cancel flag, so it continues the flow even under
	// cancellation (FX-2 completion rule 7.1, FX-38); the start it asks for
	// carries StartSignals.ContinuesErrorStop.
	ErrorStopWithFailureModule bool
	// FatalName: the outcome is a failure whose error name the host listed
	// as fatal (H7).
	FatalName bool
}

// Completion decides a completion phase's host checks, in ADR 0008's
// order: the latch, then cancellation, then fatal error names. Proceed hands
// over to Windmill's continue decision (FX-2 completion step 7); after it,
// AfterContinue applies H8's haltAfter.
func Completion(s CompletionSignals) Verdict {
	switch {
	case s.Latched:
		return EndLatched
	case s.Canceled && !s.ErrorStopWithFailureModule:
		return EndCanceled
	case s.FatalName:
		return EndLatched
	}
	return Proceed
}

// ContinueSignals describe a completion once Windmill's continue decision
// has been made.
type ContinueSignals struct {
	// RunContinues: after this completion the run would start something
	// else: a next module, a failure module, another attempt, iteration or
	// branch, or a module of an enclosing flow.
	RunContinues bool
	// Retry: what the run would start next is another attempt of the same
	// module, so the module has no final outcome yet. The attempt is due by
	// FX-2 completion rule 7.6, or by the re-entry after rule 7.1 that
	// FX-19 describes for an error stop.
	Retry bool
	// ToFailureModule: what the run would start next is a failure module.
	ToFailureModule bool
	// Canceled: the run is canceled and goes on only because Completion let
	// an error stop through (FX-38).
	Canceled bool
	// HaltAfter: H8's haltAfter matches the module that completed.
	HaltAfter bool
}

// AfterContinue applies H8's haltAfter once the module has its final
// outcome (no retry is due) and the run would go on: the run ends halted
// instead, or canceled if it is canceled. A halt never applies when the run
// is about to enter a failure module, and a completion that ends the run
// anyway keeps the status Windmill gives it, so halting never hides a
// failure or a cancellation.
func AfterContinue(s ContinueSignals) Verdict {
	if !s.HaltAfter || !s.RunContinues || s.Retry || s.ToFailureModule {
		return Proceed
	}
	if s.Canceled {
		return EndCanceled
	}
	return EndHalted
}

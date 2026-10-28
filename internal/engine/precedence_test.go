package engine

import (
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// The tables below state ADR 0008's precedence case by case, written out
// rather than computed. The invariant tests then check every combination of
// signals against the rules the ADR states in words.

func TestStartPrecedence(t *testing.T) {
	tests := []struct {
		s    StartSignals
		want Verdict
		why  string
	}{
		{StartSignals{}, Proceed, "no signal: Windmill's start checks decide"},
		{StartSignals{HaltBefore: true}, EndHalted, "haltBefore ends the run before the module"},
		{StartSignals{FailureModule: true}, Proceed, "a failure module starts as Windmill decides"},
		{StartSignals{FailureModule: true, HaltBefore: true}, Proceed, "halting never applies to a failure module"},
		{StartSignals{Canceled: true}, EndCanceled, "cancellation outranks Windmill's checks"},
		{StartSignals{Canceled: true, HaltBefore: true}, EndCanceled, "cancellation outranks halt"},
		{StartSignals{Canceled: true, FailureModule: true}, EndCanceled, "cancellation skips the failure module (FX-38)"},
		{StartSignals{Canceled: true, ContinuesErrorStop: true}, Proceed, "FX-38: what an error stop's continue decision asked for still starts"},
		{StartSignals{Canceled: true, ContinuesErrorStop: true, FailureModule: true}, Proceed, "FX-38: including the failure module"},
		{StartSignals{Canceled: true, ContinuesErrorStop: true, FailureModule: true, HaltBefore: true}, Proceed, "a halt on that failure module does not apply"},
		{StartSignals{Canceled: true, ContinuesErrorStop: true, HaltBefore: true}, EndCanceled, "a halt on an FX-38 start stops it, but the run stays canceled"},
		{StartSignals{Latched: true}, EndLatched, "the latch outranks everything"},
		{StartSignals{Latched: true, Canceled: true}, EndLatched, "latch over cancellation"},
		{StartSignals{Latched: true, HaltBefore: true}, EndLatched, "latch over halt"},
		{StartSignals{Latched: true, Canceled: true, ContinuesErrorStop: true}, EndLatched, "latch over the FX-38 exception"},
	}
	for _, tt := range tests {
		if got := Start(tt.s); got != tt.want {
			t.Errorf("Start(%+v) = %v, want %v: %s", tt.s, got, tt.want, tt.why)
		}
	}
}

func TestStartInvariants(t *testing.T) {
	for _, s := range allStartSignals() {
		got := Start(s)
		switch {
		case s.Latched:
			if got != EndLatched {
				t.Errorf("Start(%+v) = %v: the latch must win", s, got)
			}
		case s.Canceled:
			// A canceled run never ends halted, and goes on only for the
			// FX-38 continuation, unless a halt that applies stops it.
			want := EndCanceled
			if s.ContinuesErrorStop && !(s.HaltBefore && !s.FailureModule) {
				want = Proceed
			}
			if got != want {
				t.Errorf("Start(%+v) = %v, want %v", s, got, want)
			}
		case s.HaltBefore && !s.FailureModule:
			if got != EndHalted {
				t.Errorf("Start(%+v) = %v, want halted", s, got)
			}
		default:
			if got != Proceed {
				t.Errorf("Start(%+v) = %v: no host signal applies", s, got)
			}
		}
		if s.FailureModule && got == EndHalted {
			t.Errorf("Start(%+v) halted a failure module", s)
		}
	}
}

func TestCompletionPrecedence(t *testing.T) {
	// Every combination of latched, canceled, error stop with a failure
	// module, and fatal name.
	tests := []struct {
		s    CompletionSignals
		want Verdict
		why  string
	}{
		{CompletionSignals{}, Proceed, "no signal: Windmill's continue decision decides"},
		{CompletionSignals{FatalName: true}, EndLatched, "H7: a fatal name latches instead of reaching retry or the failure module"},
		{CompletionSignals{ErrorStopWithFailureModule: true}, Proceed, "an error stop goes to Windmill's continue decision"},
		{CompletionSignals{ErrorStopWithFailureModule: true, FatalName: true}, EndLatched, "a fatal name outranks the failure module"},
		{CompletionSignals{Canceled: true}, EndCanceled, "FX-38: no retry, no continue_on_error, no failure module"},
		{CompletionSignals{Canceled: true, FatalName: true}, EndCanceled, "cancellation outranks fatal names"},
		{CompletionSignals{Canceled: true, ErrorStopWithFailureModule: true}, Proceed, "FX-38: an error stop that coincides with the cancel still continues"},
		{CompletionSignals{Canceled: true, ErrorStopWithFailureModule: true, FatalName: true}, EndLatched, "FX-38 exception, then the fatal name"},
		{CompletionSignals{Latched: true}, EndLatched, "the latch outranks every later step outcome"},
		{CompletionSignals{Latched: true, FatalName: true}, EndLatched, "latch over all"},
		{CompletionSignals{Latched: true, ErrorStopWithFailureModule: true}, EndLatched, "latch over the failure module"},
		{CompletionSignals{Latched: true, ErrorStopWithFailureModule: true, FatalName: true}, EndLatched, "latch over all"},
		{CompletionSignals{Latched: true, Canceled: true}, EndLatched, "latch over cancellation"},
		{CompletionSignals{Latched: true, Canceled: true, FatalName: true}, EndLatched, "latch over all"},
		{CompletionSignals{Latched: true, Canceled: true, ErrorStopWithFailureModule: true}, EndLatched, "latch over all"},
		{CompletionSignals{Latched: true, Canceled: true, ErrorStopWithFailureModule: true, FatalName: true}, EndLatched, "latch over all"},
	}
	if len(tests) != 16 {
		t.Fatalf("table has %d rows, want all 16 combinations", len(tests))
	}
	seen := map[CompletionSignals]bool{}
	for _, tt := range tests {
		if seen[tt.s] {
			t.Fatalf("combination %+v listed twice", tt.s)
		}
		seen[tt.s] = true
		if got := Completion(tt.s); got != tt.want {
			t.Errorf("Completion(%+v) = %v, want %v: %s", tt.s, got, tt.want, tt.why)
		}
	}
}

func TestAfterContinue(t *testing.T) {
	tests := []struct {
		s    ContinueSignals
		want Verdict
		why  string
	}{
		{ContinueSignals{RunContinues: true}, Proceed, "no halt selected"},
		{ContinueSignals{HaltAfter: true}, Proceed, "the completion ends the run anyway: Windmill's status stands"},
		{ContinueSignals{HaltAfter: true, RunContinues: true}, EndHalted, "the module is final and the run would go on"},
		{ContinueSignals{HaltAfter: true, RunContinues: true, Retry: true}, Proceed, "a retry is due: the module has no final outcome yet"},
		{ContinueSignals{HaltAfter: true, RunContinues: true, ToFailureModule: true}, Proceed, "a failed module goes on to its failure module: halting would hide the failure"},
		{ContinueSignals{HaltAfter: true, RunContinues: true, Canceled: true}, EndCanceled, "a halt on an FX-38 continuation stops it, but the run stays canceled"},
		{ContinueSignals{HaltAfter: true, RunContinues: true, Canceled: true, ToFailureModule: true}, Proceed, "FX-38's failure module still starts"},
		{ContinueSignals{HaltAfter: true, RunContinues: true, Canceled: true, Retry: true}, Proceed, "FX-38's retry still starts"},
	}
	for _, tt := range tests {
		if got := AfterContinue(tt.s); got != tt.want {
			t.Errorf("AfterContinue(%+v) = %v, want %v: %s", tt.s, got, tt.want, tt.why)
		}
	}
}

func TestAfterContinueInvariants(t *testing.T) {
	for _, s := range allContinueSignals() {
		got := AfterContinue(s)
		if !s.HaltAfter && got != Proceed {
			t.Errorf("AfterContinue(%+v) = %v without a halt", s, got)
		}
		if (!s.RunContinues || s.Retry || s.ToFailureModule) && got != Proceed {
			t.Errorf("AfterContinue(%+v) = %v: the module is not final, the run ends anyway, or a failure module is next", s, got)
		}
		if s.Canceled && got == EndHalted {
			t.Errorf("AfterContinue(%+v) turned a canceled run into a halted one", s)
		}
	}
}

func TestVerdictStatus(t *testing.T) {
	for v, want := range map[Verdict]conformance.RunStatus{
		EndLatched:  conformance.RunLatched,
		EndCanceled: conformance.RunCanceled,
		EndHalted:   conformance.RunHalted,
	} {
		got, ok := v.Status()
		if !ok || got != want {
			t.Errorf("%v.Status() = %q, %v; want %q", v, got, ok, want)
		}
		if !got.Valid() {
			t.Errorf("%v ends the run with an invalid status %q", v, got)
		}
	}
	if s, ok := Proceed.Status(); ok || s != "" {
		t.Errorf("Proceed.Status() = %q, %v", s, ok)
	}
}

func allStartSignals() []StartSignals {
	var all []StartSignals
	for i := range 1 << 5 {
		all = append(all, StartSignals{
			Latched:            i&1 != 0,
			Canceled:           i&2 != 0,
			ContinuesErrorStop: i&4 != 0,
			FailureModule:      i&8 != 0,
			HaltBefore:         i&16 != 0,
		})
	}
	return all
}

func allContinueSignals() []ContinueSignals {
	var all []ContinueSignals
	for i := range 1 << 5 {
		all = append(all, ContinueSignals{
			RunContinues:    i&1 != 0,
			Retry:           i&2 != 0,
			ToFailureModule: i&4 != 0,
			Canceled:        i&8 != 0,
			HaltAfter:       i&16 != 0,
		})
	}
	return all
}

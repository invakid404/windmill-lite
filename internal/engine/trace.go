package engine

import (
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Trace is what every run returns besides its result (H9): per site, what
// happened and why, with the provenance and timings that comparisons and
// replay need. Its wire form belongs to the adapters (runner.proto,
// milestone F5).
//
// One run builds its trace, and the trace it returns when it ends is a
// snapshot that nothing changes afterwards, so hosts read it without
// synchronisation. What abandoned invocations produce later (D9) goes into
// a new snapshot, the one Hooks.OnRunSettled receives.
type Trace struct {
	// RunID is the run's id; Bundle is the digest of the bundle it ran
	// (ADR 0010); ModuleState is the mode it ran in (ADR 0006).
	RunID       string
	Bundle      string
	ModuleState ModuleState

	// Status is the run's final status.
	Status conformance.RunStatus
	// Result is the final result returned to the host, in stored form
	// (FX-36, FX-42); zero for a latched or halted run.
	Result jsonval.Value
	// ResultOrigin is who produced Result when it is an error.
	ResultOrigin Origin
	// Latched is the error the run latched (H5) when Status is
	// conformance.RunLatched: the first one latched.
	Latched error
	// MayHaveMutated is true once the first write-capable module launched
	// (H3), in failed runs too.
	MayHaveMutated bool

	// Steps holds one record per site the run reached, in the order the
	// engine recorded them; compare them by Site, never by position.
	Steps []StepRecord
	// Warnings are the run's warnings: unattributed process-level errors
	// and the work invocations left behind when they settled (D9), and
	// those that hooks appended (H9).
	Warnings []Warning
	// Deviations lists the deviations that depend on a run-time setting the
	// host chose, by id (D2's watchdog, D15's cap, the H7 rule), as ADR 0001
	// §5 requires.
	Deviations []string
	// Spans are the run's timings (ADR 0011, run spans).
	Spans []Span
}

// Step returns the record of the site at p.
func (t *Trace) Step(p conformance.SitePath) (StepRecord, bool) {
	for _, s := range t.Steps {
		if s.Site == p {
			return s, true
		}
	}
	return StepRecord{}, false
}

// StepRecord is the trace of one site: one module, or one failed attempt of
// a retried module at <module>/r:<k>.
type StepRecord struct {
	Site    conformance.SitePath
	Attempt int

	// Status is the status of the site's job (OR-V2), or StepNotRun when it
	// has none. Module is the module's own status (FX-1), for a site that is
	// a module; it is empty for attempt, iteration and branch sites. The two
	// are recorded apart because they differ after an error stop: the job
	// succeeded and the module failed (FX-V2).
	Status conformance.StepStatus
	Module conformance.ModuleStatus
	// Ran is false when no job ran for the site (OR-V2); Skipped is the
	// module's skipped flag.
	Ran     bool
	Skipped bool
	// Abandoned is true when the engine stopped waiting for the invocation
	// (a timeout or a cancellation) while it was still running (D2).
	Abandoned bool
	// Hooks names the hooks that changed what Windmill would have done at
	// this site ("H1", "H2", "H7", "H8"), so a comparison with the oracle
	// can account for them (ADR 0008).
	Hooks []string

	// ArgsBeforeRewrite are the evaluated arguments before H2 replaced
	// them; zero when H2 did not run.
	ArgsBeforeRewrite jsonval.Value
	// RecordedArgs are the arguments as stored (after transforms and H2);
	// ResolvedArgs are the same after $var:/$res: resolution, as the script
	// received them. They are distinct observation points (JE-V3).
	RecordedArgs jsonval.Value
	ResolvedArgs jsonval.Value

	// Result is the stored result of the site's own job; Carried is the
	// value the module carries on to what follows it, which differs from
	// Result after an error stop (FX-V2). Together with Module and
	// ResolvedArgs they are the dimensions ADR 0001 §2 compares beyond
	// OR-15's step record (conformance.StepObservation).
	Result  jsonval.Value
	Carried jsonval.Value
	// ResultOrigin is who produced Result when it is an error, which decides
	// what a comparison may normalise in it (ADR 0001 §2, OR-V3).
	// CarriedOrigin is who produced Carried when the engine set it apart
	// from the job's result: an error stop's EarlyStopError, a throwing
	// stop_after_if's string error (FX-V2), a chaining error at the root's
	// first pickup, which has no job (FX-15). When Carried is the job's
	// result, ResultOrigin is its origin.
	ResultOrigin  Origin
	CarriedOrigin Origin
	// BranchChosen is the branch a branchone took.
	BranchChosen *conformance.BranchChosen

	Start    time.Time
	Duration time.Duration
	// Late holds what the invocation produced after it settled or was
	// abandoned, up to the snapshot's time (D9).
	Late []Late
}

// Origin is who produced an error value (ADR 0001 §2). Only errors that
// windmill-lite generates in place of Windmill's own messages may have a
// Rust location suffix stripped from the oracle's side of a comparison.
type Origin string

const (
	// OriginNone: the value is not an error.
	OriginNone Origin = ""
	// OriginUser: thrown or returned by step code.
	OriginUser Origin = "user"
	// OriginEngine: generated by the flow interpreter or the expression
	// pipeline, in place of a message Windmill's server generates.
	OriginEngine Origin = "engine"
	// OriginHarness: generated by a harness, such as an escaped or empty
	// outcome's error.
	OriginHarness Origin = "harness"
	// OriginFacade: generated by the platform facade.
	OriginFacade Origin = "facade"
	// OriginHost: produced by a host hook or plugin.
	OriginHost Origin = "host"
)

// Generated reports whether o is windmill-lite generating one of Windmill's
// own messages (engine, harness or facade).
func (o Origin) Generated() bool {
	return o == OriginEngine || o == OriginHarness || o == OriginFacade
}

// Late is something an invocation produced after its outcome was fixed or
// it was abandoned: the late frame of ADR 0004.
type Late struct {
	Kind LateKind
	Body jsonval.Value
	// Final marks the late result or error that says how the entrypoint
	// call ended, when it ended after the invocation was abandoned or after
	// its outcome was fixed: by an intercepted process.exit (D11) or by an
	// attributed process-level error that failed it as escaped. Settled
	// follows it. Without Final, a LateError is an attributed error from
	// work the call left running (D9) and says nothing about settlement.
	Final bool
}

// LateKind is what a late frame carries.
type LateKind string

const (
	LateResult LateKind = "result"
	LateError  LateKind = "error"
	LateLog    LateKind = "log"
)

// Warning is a run warning, which never changes the run's status.
type Warning struct {
	Code    string
	Message string
	// Left counts, by kind, what an invocation still had when it settled,
	// for a warning of code "left_behind": the timers, requests and child
	// processes the settle-time fence revoked or left running (D9). It is
	// nil for other warnings.
	Left map[string]int
	// Untracked counts, by kind, what the native boundary handed a native
	// invocation but does not track, for a warning of code
	// "native_untracked" (ADR 0001, D5). It is nil for other warnings.
	Untracked map[string]int
}

// Span is one timed interval of a run (ADR 0011, run spans).
type Span struct {
	Site  conformance.SitePath
	Kind  SpanKind
	Start time.Time
	// Duration is the measured length; for SpanWait under the recording
	// clock (H10) it is the requested wait, which was not slept.
	Duration time.Duration
}

// SpanKind is what a span measures.
type SpanKind string

const (
	SpanModule     SpanKind = "module"     // a module, start to completion
	SpanQueue      SpanKind = "queue"      // an invocation waiting for a harness
	SpanHarness    SpanKind = "harness"    // an invocation inside its harness
	SpanSerialise  SpanKind = "serialise"  // boundary JSON work for an invocation
	SpanExpression SpanKind = "expression" // one expression evaluation, pool wait included
	SpanPlatform   SpanKind = "platform"   // one platform call
	SpanWait       SpanKind = "wait"       // a requested wait: sleep or retry delay
)

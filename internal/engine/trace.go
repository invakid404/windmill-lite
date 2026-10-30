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
	// (ADR 0010); ModuleState is the mode it ran in (ADR 0006); SharedCode
	// is set when its steps ran their shared-layout modules (ADR 0013),
	// which also puts the deviation of shared code among its Deviations.
	RunID       string
	Bundle      string
	ModuleState ModuleState
	SharedCode  bool

	// Status is the run's final status.
	Status conformance.RunStatus
	// Result is the final result returned to the host, in stored form
	// (FX-36, FX-42); zero for a latched or halted run.
	Result jsonval.Value
	// ResultOrigin is who produced Result when it is an error.
	// ResultNested lists, when Result aggregates other values (a loop's or
	// a branchall's array), the JSON pointers relative to it of the errors
	// inside it that windmill-lite generated, such as "/0" for an
	// iteration's (see StepRecord.ResultNested).
	ResultOrigin Origin
	ResultNested []string
	// Latched is the error the run latched (H5) when Status is
	// conformance.RunLatched: the first one latched.
	Latched error
	// MayHaveMutated is true once the first write-capable module launched
	// (H3), in failed runs too.
	MayHaveMutated bool
	// Cached is true when the result cache answered the run's flow job,
	// for a flow with a cache_ttl (FX-40): it succeeded with the cached
	// result, and no module ran.
	Cached bool

	// Steps holds one record per site the run reached, in the order the
	// engine recorded them; compare them by Site, never by position.
	Steps []StepRecord
	// Children are the child flows the run created (CF-1), in the order it
	// created them.
	Children []ChildFlow
	// Warnings are the run's warnings: unattributed process-level errors
	// and the work invocations left behind when they settled (D9), and
	// those that hooks appended (H9).
	Warnings []Warning
	// Deviations lists the deviations that depend on a run-time setting the
	// host chose, by id (D2's watchdog, D15's cap, the H7 rule), as ADR 0001
	// §5 requires. With shared code in warm mode it also lists the
	// deviation of shared code (ADR 0013).
	Deviations []string
	// Spans are the run's timings (ADR 0011, run spans).
	Spans []Span
	// PlatformCalls are the platform calls the run's steps made, as the
	// platform facade recorded them (ADR 0007). The runtime that serves
	// the facade fills them (wl, milestone F1); the engine itself does
	// not see the facade.
	PlatformCalls []PlatformCall
}

// PlatformCall is one platform call a step of the run made, as the facade
// recorded it when the step's invocation was released (ADR 0007): the
// step's site and invocation id, the request as sent, the route it matched
// ("" for none), the answer's status, and, for a call answered 501, the
// answer's text, which the 501 policy quotes.
type PlatformCall struct {
	Site        conformance.SitePath
	Job         string
	Method      string
	Path        string
	Query       string
	Route       string
	Status      int
	Unsupported bool
	Message     string
}

// Step returns the record of the site at p.
func (t *Trace) Step(p conformance.SitePath) (StepRecord, bool) {
	for _, s := range t.Steps {
		if s.Site == p && !s.Superseded {
			return s, true
		}
	}
	return StepRecord{}, false
}

// StepRecord is the trace of one site: one module, or one failed attempt of
// a retried module at <module>/r:<k>, where k is the attempt's place in
// the flow-wide list of failed attempts (FX-27), which counts an earlier
// module's attempts after a tolerated failure (FX-29).
type StepRecord struct {
	Site conformance.SitePath
	// Superseded marks a record that Windmill's job tree no longer reaches
	// (milestone NI): what a container's failed attempt ran besides the job
	// its retry records (CF-22), what a sequential loop ran before its
	// skip_if held mid-loop (CF-21), and the job a collapsed iteration
	// never pushed. Trace.Step and observations leave it out.
	Superseded bool
	// Attempt is the module's attempt the record is of: 0 for its first
	// run, n for its nth retry.
	Attempt int
	// FailedRetries are, on a module's record, the sites of the failed
	// attempts its Success or Failure status lists: the flow-wide list
	// that Windmill copies into the status of every module that completes,
	// its own retried attempts and, after a tolerated failure, an earlier
	// module's (FX-27, FX-29). Each names the record of that attempt.
	FailedRetries []conformance.SitePath
	// Job is the id of the site's job, once it launched: for a script
	// step, its leaf job, the invocation's WM_JOB_ID; for a subflow
	// step, the child flow's job, which its steps see as WM_FLOW_JOB_ID
	// (CF-17); for a leaf the result cache answered, the id of the job
	// that completed without running (FX-40). Empty for identity jobs,
	// which have no invocation, and for sites with no job.
	Job string

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
	// Cached is true when the result cache answered the site's job (FX-40):
	// a leaf's, which ran no invocation, or a subflow step's flow job, which
	// ran no module.
	Cached bool
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
	// Result after an error stop (FX-V2), and is zero for a failed attempt,
	// which carries nothing on. Together with Module and
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
	// ResultNested and CarriedNested list, for a value that aggregates
	// others (a loop's or a branchall's array of its children's results),
	// the JSON pointers relative to it of the errors windmill-lite generated
	// inside it: "/0" for the first child's, "/1/0" inside a nested loop's
	// array. An origin says who produced a value as a whole; these say it
	// of the parts, so that a comparison applies ADR 0011's location rule
	// there and nowhere else.
	ResultNested  []string
	CarriedNested []string
	// BranchChosen is the branch a branchone took.
	BranchChosen *conformance.BranchChosen

	// Start and Duration time the site's job, from its launch, after any
	// sleep or retry delay, to its outcome; for a site with no job, the
	// time its module took to reach that point. The module's own span,
	// waits included, is a SpanModule span.
	Start    time.Time
	Duration time.Duration
	// Late holds what the invocation produced after it settled or was
	// abandoned, up to the snapshot's time (D9).
	Late []Late
}

// ChildFlow is a child flow a run created: the body of a loop iteration
// that ran as a flow of its own, not collapsed, or of a branch, or the
// flow a subflow step runs (CF-1, CF-8, CF-17).
type ChildFlow struct {
	// Site is the iteration's site, <loop>/i:<n>, the branch's,
	// <container>/b:<n> or <container>/b:default, or a subflow step's own
	// (ADR 0011), under which the child's modules have theirs.
	Site conformance.SitePath
	// ID is the child flow's job id, which its steps see as
	// WM_FLOW_JOB_ID (WA-6).
	ID string
	// Modules are the ids of the child's modules, in order: a module with
	// no record in Steps never got a job (OR-11). Virtual marks, in the same
	// order, the virtual `<id>-v` identity of a body whose last module has
	// a sleep (OF-17, CF-26), which the oracle's walker leaves out (OR-V7).
	Modules []string
	Virtual []bool
	// Failure says that the child has a failure module, its copy of its
	// enclosing flow's or a subflow's own, at <Site>/s:failure (CF-16,
	// FX-34), which never got a job when Steps has no record of it.
	Failure bool
	// Superseded marks a child flow that Windmill's job tree no longer
	// reaches, as StepRecord.Superseded does a record.
	Superseded bool
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

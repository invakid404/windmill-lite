package conformance

import "fmt"

// RunStatus is the final status of a run.
//
// The first four are Windmill's job-level statuses as the oracle harness
// reads them (OR-V2): canceled before skipped before success or failure.
// RunLatched and RunHalted exist only in windmill-lite: a run that a host
// latched (H5) or halted (H8) ends with them (ADR 0008), so an oracle
// recording never carries them.
type RunStatus string

// The run statuses.
const (
	RunSuccess  RunStatus = "success"
	RunFailure  RunStatus = "failure"
	RunCanceled RunStatus = "canceled"
	RunSkipped  RunStatus = "skipped"
	RunLatched  RunStatus = "latched"
	RunHalted   RunStatus = "halted"
)

// Valid reports whether s is one of the run statuses.
func (s RunStatus) Valid() bool {
	switch s {
	case RunSuccess, RunFailure, RunCanceled, RunSkipped, RunLatched, RunHalted:
		return true
	}
	return false
}

// HostDecided reports whether s is a status that only a host signal produces
// (RunLatched, RunHalted), never Windmill.
func (s RunStatus) HostDecided() bool { return s == RunLatched || s == RunHalted }

// MarshalText returns s, failing on an unknown status.
func (s RunStatus) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("conformance: unknown run status %q", string(s))
	}
	return []byte(s), nil
}

// UnmarshalText accepts only the run statuses.
func (s *RunStatus) UnmarshalText(text []byte) error {
	v := RunStatus(text)
	if !v.Valid() {
		return fmt.Errorf("conformance: unknown run status %q", string(text))
	}
	*s = v
	return nil
}

// StepStatus is the status of one site in an observation (OR-V2, OR-15).
//
// A site whose module has a job takes that job's status (canceled, skipped,
// success or failure). A module that never got a job is StepNotRun, with ran
// false. A module that completed without running a child (an empty loop, a
// failed branchone predicate) is StepSuccess or StepFailure, also with ran
// false (OR-V2).
type StepStatus string

// The step statuses.
const (
	StepSuccess  StepStatus = "success"
	StepFailure  StepStatus = "failure"
	StepCanceled StepStatus = "canceled"
	StepSkipped  StepStatus = "skipped"
	StepNotRun   StepStatus = "not_run"
)

// Valid reports whether s is one of the step statuses.
func (s StepStatus) Valid() bool {
	switch s {
	case StepSuccess, StepFailure, StepCanceled, StepSkipped, StepNotRun:
		return true
	}
	return false
}

// MarshalText returns s, failing on an unknown status.
func (s StepStatus) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("conformance: unknown step status %q", string(s))
	}
	return []byte(s), nil
}

// UnmarshalText accepts only the step statuses.
func (s *StepStatus) UnmarshalText(text []byte) error {
	v := StepStatus(text)
	if !v.Valid() {
		return fmt.Errorf("conformance: unknown step status %q", string(text))
	}
	*s = v
	return nil
}

// ModuleStatus is a module's own status (FX-1), which is not its job's: a
// module whose script succeeded fails when its stop_after_if gives an error
// stop, while the job stays successful (FX-V2).
type ModuleStatus string

// The module statuses, from Windmill's module status kinds at the end of a
// run (FX-1): Success, Failure, WaitingForPriorSteps (never reached), and
// InProgress, WaitingForExecutor or WaitingForEvents (a run that ended while
// the module was unfinished, which only cancellation leaves).
const (
	ModuleSuccess    ModuleStatus = "success"
	ModuleFailure    ModuleStatus = "failure"
	ModuleNotRun     ModuleStatus = "not_run"
	ModuleInProgress ModuleStatus = "in_progress"
)

// Valid reports whether s is one of the module statuses.
func (s ModuleStatus) Valid() bool {
	switch s {
	case ModuleSuccess, ModuleFailure, ModuleNotRun, ModuleInProgress:
		return true
	}
	return false
}

// MarshalText returns s, failing on an unknown status.
func (s ModuleStatus) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("conformance: unknown module status %q", string(s))
	}
	return []byte(s), nil
}

// UnmarshalText accepts only the module statuses.
func (s *ModuleStatus) UnmarshalText(text []byte) error {
	v := ModuleStatus(text)
	if !v.Valid() {
		return fmt.Errorf("conformance: unknown module status %q", string(text))
	}
	*s = v
	return nil
}

// ImpliedModule returns the module status that a site with job status s has
// unless an observation says otherwise: success for a successful or skipped
// job, failure for a failed or canceled one, not_run for a site with no job.
func (s StepStatus) ImpliedModule() ModuleStatus {
	switch s {
	case StepSuccess, StepSkipped:
		return ModuleSuccess
	case StepFailure, StepCanceled:
		return ModuleFailure
	case StepNotRun:
		return ModuleNotRun
	}
	return ""
}

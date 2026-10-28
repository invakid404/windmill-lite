package conformance

import (
	"encoding/json"
	"fmt"
)

// Observation is the normalised observation of one run (OR-15): what a
// flow fixture's expect, or a recording, says about the run. Values are JSON
// text after normalisation (OR-14): job ids replaced by placeholders, stacks
// masked where provenance allows. Provenance (versions, timings, host) is
// kept apart from it (OR-V5).
//
// Encoded with encoding/json, Steps is keyed by canonical site-path text and
// its keys come out sorted, so equal observations encode to equal bytes.
type Observation struct {
	Status  RunStatus                    `json:"status"`
	Result  json.RawMessage              `json:"result"`
	Steps   map[SitePath]StepObservation `json:"steps,omitempty"`
	Effects []json.RawMessage            `json:"effects,omitempty"`
}

// StepObservation is the observation of one site (OR-11, OR-15, OR-V2).
type StepObservation struct {
	Status StepStatus `json:"status"`
	// Ran is false when no job ran for the site: always for StepNotRun,
	// and for a module that completed without running a child, such as an
	// empty loop (OR-V2). It is nil when a job ran. Encoding writes
	// "ran":false for every StepNotRun, and decoding sets it, so the two
	// spellings of a step that never ran compare equal; a StepNotRun with
	// ran true is rejected both ways.
	Ran *bool `json:"ran,omitempty"`
	// Args are the arguments as stored on the step's job, after input
	// transforms and before $var:/$res: resolution (JE-V3 point O2).
	Args json.RawMessage `json:"args,omitempty"`
	// Result is the stored result of the step's own job.
	Result json.RawMessage `json:"result,omitempty"`
	// BranchChosen is the branch a branchone module took.
	BranchChosen *BranchChosen `json:"branch_chosen,omitempty"`
	// Skipped is the module's skipped flag, which is not the job-level
	// skipped status (OR-V2).
	Skipped *bool `json:"skipped,omitempty"`
}

// BranchChosen is the branch a branchone module took: {"type":"branch",
// "branch":n} or {"type":"default"} (OR-11).
type BranchChosen struct {
	Type   string `json:"type"`
	Branch *int   `json:"branch,omitempty"`
}

// stepObservationJSON has StepObservation's fields without its methods.
type stepObservationJSON StepObservation

// MarshalJSON writes the step, with "ran":false for StepNotRun (OR-V2/a).
func (s StepObservation) MarshalJSON() ([]byte, error) {
	if err := s.normaliseRan(); err != nil {
		return nil, err
	}
	return json.Marshal(stepObservationJSON(s))
}

// UnmarshalJSON reads the step, setting ran to false for StepNotRun.
func (s *StepObservation) UnmarshalJSON(data []byte) error {
	var v stepObservationJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o := StepObservation(v)
	if err := o.normaliseRan(); err != nil {
		return err
	}
	*s = o
	return nil
}

// normaliseRan sets Ran to false for StepNotRun.
func (s *StepObservation) normaliseRan() error {
	if s.Status != StepNotRun {
		return nil
	}
	if s.Ran != nil && *s.Ran {
		return fmt.Errorf("conformance: a %s step cannot have ran true", StepNotRun)
	}
	no := false
	s.Ran = &no
	return nil
}

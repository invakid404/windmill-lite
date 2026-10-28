package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// Observation is the normalised observation of one run (OR-15): what a
// flow fixture's expect, or a recording, says about the run. Values are JSON
// text after normalisation (OR-14): job ids replaced by placeholders, stacks
// masked where provenance allows. Provenance (versions, timings, host) is
// kept apart from it (OR-V5).
//
// Encoded with encoding/json, Steps is keyed by canonical site-path text and
// its keys come out sorted, and every step is in canonical form (see
// StepObservation), so two observations of the same run encode alike.
// Comparisons parse the values and compare objects without regard to key
// order (OR-13).
type Observation struct {
	Status  RunStatus                    `json:"status"`
	Result  json.RawMessage              `json:"result"`
	Steps   map[SitePath]StepObservation `json:"steps,omitempty"`
	Effects []json.RawMessage            `json:"effects,omitempty"`
}

// StepObservation is the observation of one site. It holds OR-15's step
// record (OR-11, OR-V2) and three more dimensions that ADR 0001 §2 compares
// and that the stored fields cannot recover: the module's own status, the
// value it carries on (FX-V2), and the arguments after $var:/$res:
// resolution (JE-V3).
//
// Each optional field has a canonical absence, which encoding and decoding
// apply, so an observation can leave out what it does not differ in:
//
//	Field          Absent means                       On the oracle
//	ran            a job ran for the site              walked (OR-V2)
//	module         the status ImpliedModule gives      walked from flow_status
//	skipped        the module was not skipped          walked
//	args           the site's job has no arguments     get_args (O2)
//	resolved_args  equal to args                       not exposed
//	result         the site's job has no result        get_result_maybe
//	carried        equal to result                     not exposed
//
// The oracle's API exposes neither resolved arguments nor carried values,
// so a recording holds neither: a fixture that needs them asserts them in
// its expect, and shows them to the oracle through what the flow returns
// (a step that returns its arguments, a later step's previous_result, a
// failure handler's input). ADR 0011 says how comparisons treat them.
type StepObservation struct {
	// Status is the site's job-level status (OR-V2): the status of the
	// site's job, or StepNotRun when it has none.
	Status StepStatus `json:"status"`
	// Ran is false when no job ran for the site: always for StepNotRun,
	// and for a module that completed without running a child, such as an
	// empty loop (OR-V2). It is nil when a job ran. A StepNotRun with ran
	// true is rejected.
	Ran *bool `json:"ran,omitempty"`
	// Module is the module's own status (FX-1), for a site that is a module
	// (its last segment is m or s); attempt, iteration and branch sites have
	// none. Empty means Status.ImpliedModule(). It differs from that after
	// an error stop: the job succeeded and the module failed (FX-V2).
	Module ModuleStatus `json:"module,omitempty"`
	// Skipped is the module's skipped flag, which is not the job-level
	// skipped status (OR-V2).
	Skipped *bool `json:"skipped,omitempty"`
	// Args are the arguments as stored on the site's job, after input
	// transforms and before $var:/$res: resolution (JE-V3 point O2).
	Args json.RawMessage `json:"args,omitempty"`
	// ResolvedArgs are the arguments the script received, after resolution
	// (JE-V3 point O3). Absent means equal to Args.
	ResolvedArgs json.RawMessage `json:"resolved_args,omitempty"`
	// Result is the stored result of the site's own job.
	Result json.RawMessage `json:"result,omitempty"`
	// Carried is the value the module carries on to what follows it: the
	// next module's previous_result, its failure module's input, the
	// flow's result. Absent means equal to Result; after an error stop it is
	// the EarlyStopError while Result keeps the script's value (FX-V2).
	Carried json.RawMessage `json:"carried,omitempty"`
	// BranchChosen is the branch a branchone module took.
	BranchChosen *BranchChosen `json:"branch_chosen,omitempty"`
}

// ModuleStatus returns the module's status: Module, or the status that
// Status implies when Module is empty.
func (s StepObservation) ModuleStatus() ModuleStatus {
	if s.Module != "" {
		return s.Module
	}
	return s.Status.ImpliedModule()
}

// BranchChosen is the branch a branchone module took: {"type":"branch",
// "branch":n} or {"type":"default"} (OR-11).
type BranchChosen struct {
	Type   string `json:"type"`
	Branch *int   `json:"branch,omitempty"`
}

// stepObservationJSON has StepObservation's fields without its methods.
type stepObservationJSON StepObservation

// MarshalJSON writes the step in canonical form.
func (s StepObservation) MarshalJSON() ([]byte, error) {
	if err := s.canonicalise(); err != nil {
		return nil, err
	}
	return json.Marshal(stepObservationJSON(s))
}

// UnmarshalJSON reads the step and puts it in canonical form.
func (s *StepObservation) UnmarshalJSON(data []byte) error {
	var v stepObservationJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o := StepObservation(v)
	if err := o.canonicalise(); err != nil {
		return err
	}
	*s = o
	return nil
}

// canonicalise applies the canonical absences: ran false for StepNotRun, no
// module equal to the implied one, no resolved_args equal to args, no
// carried equal to result. It rejects a step whose fields contradict each
// other.
func (s *StepObservation) canonicalise() error {
	if !s.Status.Valid() {
		return fmt.Errorf("conformance: step has no valid status (%q)", s.Status)
	}
	if s.Status == StepNotRun {
		if s.Ran != nil && *s.Ran {
			return fmt.Errorf("conformance: a %s step cannot have ran true", StepNotRun)
		}
		no := false
		s.Ran = &no
	}
	if (s.Status == StepNotRun) != (s.ModuleStatus() == ModuleNotRun) {
		return fmt.Errorf("conformance: step status %q contradicts module status %q", s.Status, s.ModuleStatus())
	}
	if s.Module == s.Status.ImpliedModule() {
		s.Module = ""
	}
	if s.ResolvedArgs != nil && jsonEqual(s.ResolvedArgs, s.Args) {
		s.ResolvedArgs = nil
	}
	if s.Carried != nil && jsonEqual(s.Carried, s.Result) {
		s.Carried = nil
	}
	return nil
}

// jsonEqual reports whether two JSON texts hold the same value, with
// objects compared without regard to key order and numbers by their text.
func jsonEqual(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va, errA := decodeJSON(a)
	vb, errB := decodeJSON(b)
	if errA != nil || errB != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(va, vb)
}

func decodeJSON(text []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// observationJSON has Observation's fields without its methods.
type observationJSON Observation

// MarshalJSON writes the observation, rejecting a module status on a site
// that is not a module.
func (o Observation) MarshalJSON() ([]byte, error) {
	if err := o.checkSites(); err != nil {
		return nil, err
	}
	return json.Marshal(observationJSON(o))
}

// UnmarshalJSON reads the observation, rejecting a module status on a site
// that is not a module.
func (o *Observation) UnmarshalJSON(data []byte) error {
	var v observationJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if err := Observation(v).checkSites(); err != nil {
		return err
	}
	*o = Observation(v)
	return nil
}

func (o Observation) checkSites() error {
	for p, s := range o.Steps {
		if s.Module == "" {
			continue
		}
		segs := p.Segments()
		if len(segs) == 0 || (segs[len(segs)-1].Kind() != KindModule && segs[len(segs)-1].Kind() != KindSlot) {
			return fmt.Errorf("conformance: site %q is not a module but has module status %q", p, s.Module)
		}
	}
	return nil
}

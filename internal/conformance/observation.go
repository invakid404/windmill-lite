package conformance

import (
	"bytes"
	"encoding/base64"
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
// its keys come out sorted, and every step is in canonical form (see
// StepObservation), so two observations of the same run encode alike.
// Comparisons parse the values and compare objects without regard to key
// order (OR-13).
type Observation struct {
	Status  RunStatus                    `json:"status"`
	Result  json.RawMessage              `json:"result"`
	Steps   map[SitePath]StepObservation `json:"steps,omitempty"`
	Effects []json.RawMessage            `json:"effects,omitempty"`
	// Response is the synchronous run-and-wait response of a run submitted
	// that way (Run.Submit), and nil for any other run.
	Response *Response `json:"response,omitempty"`
}

// Response is the HTTP response of the run-and-wait endpoint (FX-36,
// FX-37, OR-10/b): the status, the Content-Type header as sent, parameters
// included, and the body. A body that is UTF-8 is kept as text with its
// exact spelling, because the endpoint writes a stored result in jsonb's
// text output, with spaces ({"a": 2, "b": 1}), and unquotes a string under
// a content type; only job ids and the stacks of runtime errors are
// rewritten in it (Normaliser.Text). Any other body, such as the bytes a
// base64 transfer encoding decodes to, is kept losslessly: Encoding is
// BodyBase64 and Body its standard base64 text, compared as written.
type Response struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
	Body        string `json:"body"`
}

// BodyBase64 is Response.Encoding for a body that is not UTF-8.
const BodyBase64 = "base64"

// Bytes returns the body's bytes: Body itself for a text body, or Body
// decoded for a base64 one.
func (r Response) Bytes() ([]byte, error) {
	switch r.Encoding {
	case "":
		return []byte(r.Body), nil
	case BodyBase64:
		return base64.StdEncoding.DecodeString(r.Body)
	}
	return nil, fmt.Errorf("conformance: response body encoding %q is not %s", r.Encoding, BodyBase64)
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
//	resolved_args  the same text as args               not exposed
//	result         the site's job has no result        get_result_maybe
//	carried        the same text as result             not exposed
//
// "The same text" means equal once whitespace is removed, so a value whose
// keys come in another order, or whose numbers are spelt otherwise, is kept:
// the codec cannot know the fixture's key-order policy. A comparator expands
// the absences and then applies that policy (OR-13, OR-14 item 8).
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
	// (JE-V3 point O3). Absent means the same text as Args.
	ResolvedArgs json.RawMessage `json:"resolved_args,omitempty"`
	// Result is the stored result of the site's own job.
	Result json.RawMessage `json:"result,omitempty"`
	// Carried is the value the module carries on to what follows it: the
	// next module's previous_result, its failure module's input, the
	// flow's result. Absent means the same text as Result; after an error
	// stop it is the EarlyStopError while Result keeps the script's value
	// (FX-V2).
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
// module equal to the implied one, no resolved_args with the same text as
// args, no carried with the same text as result. It rejects a step whose
// fields contradict each other.
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
	if s.ResolvedArgs != nil && sameText(s.ResolvedArgs, s.Args) {
		s.ResolvedArgs = nil
	}
	if s.Carried != nil && sameText(s.Carried, s.Result) {
		s.Carried = nil
	}
	return nil
}

// sameText reports whether two JSON texts are the same text once whitespace
// is removed: same key order, same number spelling. Values that differ only
// in key order are kept apart on purpose, because a key_order: strict
// comparison (OR-13, OR-14 item 8) and the producer and stored routes of
// ADR 0005 can tell them apart.
func sameText(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// observationJSON has Observation's fields without its methods.
type observationJSON Observation

// MarshalJSON writes the observation, rejecting a module status other than
// the implied one on a site that is not a module.
func (o Observation) MarshalJSON() ([]byte, error) {
	if err := o.checkSites(); err != nil {
		return nil, err
	}
	return json.Marshal(observationJSON(o))
}

// UnmarshalJSON reads the observation, rejecting a module status other than
// the implied one on a site that is not a module.
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
		// An implied module status says nothing and is dropped when the
		// step is encoded, so it is accepted on any site, whether the step
		// was decoded or built in Go.
		if s.Module == "" || s.Module == s.Status.ImpliedModule() {
			continue
		}
		segs := p.Segments()
		if len(segs) == 0 || (segs[len(segs)-1].Kind() != KindModule && segs[len(segs)-1].Kind() != KindSlot) {
			return fmt.Errorf("conformance: site %q is not a module but has module status %q", p, s.Module)
		}
	}
	return nil
}

package main

import (
	"encoding/json"
	"fmt"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// observeTrace is windmill-lite's side of an observation (OR-15): the
// records the oracle's walker produces from Windmill's job tree, produced
// here from a run's trace (ADR 0011), then normalised by the same rules
// (OR-14) and masked by the fixture's stack and mask pointers.
//
//   - modules are the ids of the run's flow, in order, and the trace's
//     child flows name theirs (a loop iteration's body, CF-1): each is a
//     site, and a module the trace has no record of never got a job, so it
//     is not_run, as the walker reads a flow_status entry without one;
//   - jobs maps the id of every invocation the run made, which steps see as
//     WM_JOB_ID, to its site; the run's own id, WM_FLOW_JOB_ID and
//     WM_ROOT_JOB_ID, is the root, and a child flow's id is its iteration's
//     site, as the walker reaches it through flow_jobs (OR-11);
//   - stored values (the root's result, each site's args and result) are in
//     their stored form, as the job API returns them (ADR 0005), while
//     carried and resolved_args keep the texts the trace holds: the value
//     carried on in memory, and the arguments as the script received them.
//     A site with no result shows no carried either: a module that failed
//     with no job (FX-15 context 2) carries its error to the flow's result,
//     where the observation shows it;
//   - a failed site whose job ran is a runtime error for stack masking
//     (OR-V3 clause a), as a failed leaf that executed a script is.
//
// It also returns the pointers of the values windmill-lite generated (the
// errors whose origin is the engine, a harness or the facade): a site's
// result, its carried value when the trace records its origin as generated
// or it is the same value as a generated result, and the root's result;
// and, inside a value that aggregates others (a loop's array), the
// generated errors the trace points to (StepRecord.ResultNested). There a
// comparison with the oracle strips a location suffix
// (conformance.StripLocations), and nowhere else.
func observeTrace(t *engine.Trace, modules []string, jobs map[string]conformance.SitePath, spec conformance.CompareSpec) (conformance.Observation, []string, error) {
	ids := map[string]conformance.SitePath{t.RunID: {}}
	for _, c := range t.Children {
		ids[c.ID] = c.Site
	}
	for id, site := range jobs {
		ids[id] = site
	}
	n := &conformance.Normaliser{IDs: ids}
	o := conformance.Observation{Status: t.Status}
	var generated []string
	value := func(v jsonval.Value, stored bool) json.RawMessage {
		if v.IsZero() {
			return nil
		}
		if stored {
			// A value with no stored form (FX-48) is compared as produced,
			// so the comparison shows what Windmill would have refused.
			if s, err := v.Stored(); err == nil {
				return s
			}
		}
		return v.Producer()
	}

	steps := map[conformance.SitePath]conformance.StepObservation{}
	for _, rec := range t.Steps {
		if _, dup := steps[rec.Site]; dup {
			return o, nil, fmt.Errorf("site %s recorded twice", rec.Site)
		}
		st := conformance.StepObservation{Status: rec.Status, Module: rec.Module, BranchChosen: rec.BranchChosen}
		if !rec.Ran {
			st.Ran = boolPtr(false)
		}
		if rec.Skipped {
			st.Skipped = boolPtr(true)
		}
		st.Args, st.ResolvedArgs = value(rec.RecordedArgs, true), value(rec.ResolvedArgs, false)
		st.Result, st.Carried = value(rec.Result, true), value(rec.Carried, false)
		if st.Result == nil {
			st.Carried = nil
		}
		if rec.Status == conformance.StepFailure && rec.Ran && st.Result != nil {
			n.RuntimeErrors = append(n.RuntimeErrors, st.Result)
		}
		carried := conformance.JoinPointer([]string{"steps", rec.Site.String(), "carried"})
		if rec.ResultOrigin.Generated() {
			generated = append(generated, conformance.JoinPointer([]string{"steps", rec.Site.String(), "result"}))
			// A carried value that is the same value, in whatever member
			// order, has the result's provenance.
			if !rec.Carried.IsZero() && !rec.CarriedOrigin.Generated() {
				if same, err := conformance.SameJSON(rec.Carried.Producer(), rec.Result.Producer(), false); err == nil && same {
					generated = append(generated, carried)
				}
			}
		}
		// One that differs has its own, such as an error stop's
		// EarlyStopError carried over a user's result; with none recorded
		// it is never stripped.
		if st.Carried != nil && rec.CarriedOrigin.Generated() {
			generated = append(generated, carried)
		}
		result := conformance.JoinPointer([]string{"steps", rec.Site.String(), "result"})
		for _, p := range rec.ResultNested {
			generated = append(generated, result+p)
		}
		if st.Carried != nil {
			for _, p := range rec.CarriedNested {
				generated = append(generated, carried+p)
			}
		}
		steps[rec.Site] = st
	}
	notRun := func(prefix conformance.SitePath, modules []string) {
		for _, seg := range conformance.ModuleSegments(modules) {
			site := prefix.Child(seg)
			if _, ok := steps[site]; !ok {
				steps[site] = conformance.StepObservation{Status: conformance.StepNotRun}
			}
		}
	}
	notRun(conformance.SitePath{}, modules)
	for _, c := range t.Children {
		notRun(c.Site, c.Modules)
	}
	if t.ResultOrigin.Generated() {
		generated = append(generated, "/result")
	}
	for _, p := range t.ResultNested {
		generated = append(generated, "/result"+p)
	}

	// Values are normalised once every runtime error is known, as the
	// oracle's are after its walk.
	result := t.Result
	if result.IsZero() {
		result = jsonval.FromProducer([]byte("null"))
	}
	var err error
	if o.Result, err = n.Value(value(result, true)); err != nil {
		return o, nil, err
	}
	o.Steps = make(map[conformance.SitePath]conformance.StepObservation, len(steps))
	for site, st := range steps {
		for _, v := range []*json.RawMessage{&st.Args, &st.ResolvedArgs, &st.Result, &st.Carried} {
			if *v, err = n.Value(*v); err != nil {
				return o, nil, err
			}
		}
		o.Steps[site] = st
	}
	o, err = conformance.MaskPaths(o, spec.Stack, spec.Mask)
	return o, generated, err
}

func boolPtr(b bool) *bool { return &b }

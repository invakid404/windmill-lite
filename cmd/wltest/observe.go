package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// observeTrace is windmill-lite's side of an observation (OR-15): the
// records the oracle's walker produces from Windmill's job tree, produced
// here from a run's trace (ADR 0011), then normalised by the same rules
// (OR-14) and masked by the fixture's stack and mask pointers.
//
//   - modules are the ids of the run's flow, in order, and failure says
//     that it has a failure module, at s:failure, and the trace's child
//     flows name theirs (a loop iteration's or a branch's body, CF-1): each
//     is a site, and a module the trace has no record of never got a job,
//     so it is not_run, as the walker reads a flow_status entry without one;
//     virtual marks the generated `<id>-v` step of a final sleep (OF-17),
//     which the walker leaves out of the projection (OR-V7), and so does
//     this, a child flow's included (ChildFlow.Virtual, CF-26);
//   - jobs maps the id of every invocation the run made, which steps see as
//     WM_JOB_ID, to its site, and a record's own job (StepRecord.Job)
//     overrides it, so a retried attempt's job is named by its attempt's
//     site; the run's own id, WM_FLOW_JOB_ID and WM_ROOT_JOB_ID, is the
//     root, and a child flow's id is its iteration's or branch's site, as
//     the walker reaches it (OR-11);
//   - a module's record lists the failed attempts of the flow-wide retry
//     state (StepRecord.FailedRetries, FX-27): the walker fetches the k-th
//     at <module>/r:<k>, so the attempt's record appears there too, also
//     when it is an earlier module's (FX-29), and with it what is below it,
//     a retried container's failed child flow (CF-22);
//   - stored values (the root's result, each site's args and result) are in
//     their stored form, as the job API returns them (ADR 0005), while
//     carried and resolved_args keep the texts the trace holds: the value
//     carried on in memory, and the arguments as the script received them.
//     A site with no result shows no carried either: a module that failed
//     with no job (FX-15 context 2) carries its error to what follows it,
//     where the observation shows it;
//   - a failed site whose job ran is a runtime error for stack masking
//     (OR-V3 clause a), as a failed leaf that executed a script is;
//   - a superseded record or child flow, which Windmill's job tree no
//     longer reaches (a container's failed attempt, a loop skipped
//     mid-loop, a collapsed job never pushed; milestone NI), is left out.
//
// It also returns the pointers of the values windmill-lite generated (the
// errors whose origin is the engine, a harness or the facade): a site's
// result, its carried value when that is the same value or the engine set
// it apart (an error stop's, a throwing stop_after_if's, a chaining error
// at the root's first pickup), and the root's result; and, inside a value
// that aggregates others (a loop's array), the generated errors the trace
// points to (StepRecord.ResultNested). There a comparison with the oracle
// strips a location suffix (conformance.StripLocations), and nowhere else.
func observeTrace(t *engine.Trace, modules []string, virtual []bool, failure bool, jobs map[string]conformance.SitePath, spec conformance.CompareSpec) (conformance.Observation, []string, error) {
	ids := map[string]conformance.SitePath{t.RunID: {}}
	var children []engine.ChildFlow
	for _, c := range t.Children {
		if !c.Superseded {
			children = append(children, c)
			ids[c.ID] = c.Site
		}
	}
	for id, site := range jobs {
		ids[id] = site
	}
	segs := conformance.ModuleSegments(modules)
	hidden := map[conformance.SitePath]bool{}
	for i, v := range virtual {
		if v {
			hidden[conformance.SitePath{}.Child(segs[i])] = true
		}
	}
	for _, c := range children {
		csegs := conformance.ModuleSegments(c.Modules)
		for i, v := range c.Virtual {
			if v {
				hidden[c.Site.Child(csegs[i])] = true
			}
		}
	}
	records := map[conformance.SitePath]engine.StepRecord{}
	var live []engine.StepRecord
	for _, rec := range t.Steps {
		if rec.Superseded {
			continue
		}
		live = append(live, rec)
		records[rec.Site] = rec
		if rec.Job != "" {
			ids[rec.Job] = rec.Site
		}
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
	var expanded []engine.StepRecord
	// moves are the failed attempts the walker meets again below a later
	// module (FX-29): an attempt that is a child flow's job, a retried
	// container's (CF-22), is walked in turn there, with everything the
	// walker met below it the first time, its modules' own attempts
	// included (OR-11).
	type move struct{ from, to conformance.SitePath }
	var moves []move
	rebase := func(p conformance.SitePath, m move) (conformance.SitePath, bool) {
		if p == m.from {
			return m.to, true
		}
		rest, ok := strings.CutPrefix(p.String(), m.from.String()+"/")
		if !ok {
			return p, false
		}
		q, err := conformance.ParseSitePath(m.to.String() + "/" + rest)
		return q, err == nil
	}
	for _, rec := range live {
		if hidden[rec.Site] {
			continue
		}
		expanded = append(expanded, rec)
		for k, at := range rec.FailedRetries {
			src, ok := records[at]
			if !ok {
				return o, nil, fmt.Errorf("site %s lists the failed attempt %s, which has no record", rec.Site, at)
			}
			site := rec.Site.Child(conformance.Attempt(k))
			if site == at {
				continue
			}
			// The walker meets the attempt's job again here: a copy whose
			// pointers are its own, and copies of what it met below it,
			// which a trace records before any module lists the attempt,
			// so expanded already holds it, attempts and their copies
			// included.
			m := move{at, site}
			moves = append(moves, m)
			n := len(expanded)
			src.Site, src.FailedRetries = site, nil
			expanded = append(expanded, src)
			for _, sub := range expanded[:n] {
				if q, ok := rebase(sub.Site, m); ok && sub.Site != at {
					sub.Site, sub.FailedRetries = q, nil
					expanded = append(expanded, sub)
				}
			}
		}
	}
	for _, rec := range expanded {
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
		result := conformance.JoinPointer([]string{"steps", rec.Site.String(), "result"})
		carried := conformance.JoinPointer([]string{"steps", rec.Site.String(), "carried"})
		if rec.ResultOrigin.Generated() {
			generated = append(generated, result)
		}
		for _, p := range rec.ResultNested {
			generated = append(generated, result+p)
		}
		// A carried value has its own provenance when the engine set it
		// apart from the job's result (CarriedOrigin); otherwise one that
		// is the result, in whatever member order, has the result's. One
		// that differs with none recorded is never stripped.
		if st.Carried != nil {
			gen := rec.CarriedOrigin.Generated()
			if !gen && rec.ResultOrigin.Generated() {
				same, err := conformance.SameJSON(rec.Carried.Producer(), rec.Result.Producer(), false)
				gen = err == nil && same
			}
			if gen {
				generated = append(generated, carried)
			}
			for _, p := range rec.CarriedNested {
				generated = append(generated, carried+p)
			}
		}
		steps[rec.Site] = st
	}
	notRun := func(prefix conformance.SitePath, segs []conformance.Segment) {
		for _, seg := range segs {
			site := prefix.Child(seg)
			if _, ok := steps[site]; !ok && !hidden[site] {
				steps[site] = conformance.StepObservation{Status: conformance.StepNotRun}
			}
		}
	}
	if failure {
		segs = append(segs, conformance.FailureSlot())
	}
	notRun(conformance.SitePath{}, segs)
	// The child flows the walker meets: the trace's, then their copies
	// below each move, in the order the moves were made, so that a copy of
	// a copy is found.
	flows := slices.Clone(children)
	for _, m := range moves {
		for _, c := range flows {
			if at, ok := rebase(c.Site, m); ok {
				c.Site = at
				flows = append(flows, c)
			}
		}
	}
	for _, c := range flows[len(children):] {
		csegs := conformance.ModuleSegments(c.Modules)
		for i, v := range c.Virtual {
			if v {
				hidden[c.Site.Child(csegs[i])] = true
			}
		}
	}
	for _, c := range flows {
		notRun(c.Site, conformance.ModuleSegments(c.Modules))
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

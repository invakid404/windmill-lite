package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"

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
//     flows name theirs (a loop iteration's or a branch's body, CF-1, or a
//     subflow's, CF-17) and their failure modules (CF-16): each is a site,
//     and a module the trace has no record of never got a job,
//     so it is not_run, as the walker reads a flow_status entry without one;
//     virtual marks the generated `<id>-v` step of a final sleep (OF-17),
//     which the walker leaves out of the projection (OR-V7), and so does
//     this;
//   - jobs maps the id of every invocation the run made, which steps see as
//     WM_JOB_ID, to its site, and a record's own job (StepRecord.Job)
//     overrides it, so a retried attempt's job is named by its attempt's
//     site; the run's own id, WM_FLOW_JOB_ID and WM_ROOT_JOB_ID, is the
//     root, and a child flow's id is its iteration's or branch's site, as
//     the walker reaches it (OR-11);
//   - a module's record lists the failed attempts of the flow-wide retry
//     state (StepRecord.FailedRetries, FX-27): the walker fetches the k-th
//     at <module>/r:<k>, so the attempt's record appears there too, also
//     when it is an earlier module's (FX-29);
//   - stored values (the root's result, each site's args and result) are in
//     their stored form, as the job API returns them (ADR 0005), while
//     carried and resolved_args keep the texts the trace holds: the value
//     carried on in memory, and the arguments as the script received them.
//     A site with no result shows no carried either: a module that failed
//     with no job (FX-15 context 2) carries its error to what follows it,
//     where the observation shows it;
//   - a failed site whose job ran is a runtime error for stack masking
//     (OR-V3 clause a), as a failed leaf that executed a script is.
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
	for _, c := range t.Children {
		ids[c.ID] = c.Site
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
	records := map[conformance.SitePath]engine.StepRecord{}
	for _, rec := range t.Steps {
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
	var unseen []json.RawMessage
	var expanded []engine.StepRecord
	for _, rec := range t.Steps {
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
			// pointers are its own.
			src.Site, src.FailedRetries = site, nil
			expanded = append(expanded, src)
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
			if st.Carried != nil && rec.CarriedOrigin.Generated() {
				// Not observed, since the site has no result (a failure at
				// the root's pickup, FX-15), but what follows may copy it.
				unseen = append(unseen, st.Carried)
			}
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
	for _, c := range t.Children {
		segs := conformance.ModuleSegments(c.Modules)
		if c.Failure {
			segs = append(segs, conformance.FailureSlot())
		}
		notRun(c.Site, segs)
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
	for k := range unseen {
		if unseen[k], err = n.Value(unseen[k]); err != nil {
			return o, nil, err
		}
	}
	o, err = conformance.MaskPaths(o, spec.Stack, spec.Mask)
	if err != nil {
		return o, nil, err
	}
	derived, err := copiedGenerated(o, generated, unseen)
	return o, append(generated, derived...), err
}

// copiedGenerated returns the pointers of the values in o that are copies
// of an error windmill-lite generated: an argument, a result or a carried
// value of a site, or the run's result, holding at any depth a value equal
// to one at a generated pointer or in unseen (generated values o does not
// show), its error, or that error's message, outside the values already
// at a generated pointer (StripLocations reads an error object, a value
// whose error member is one, and a string in Windmill's error display).
// User code cannot build such a value without reading the generated one
// (its text holds the engine's own wording), so a copy carried on through
// a step, a container's array, a failure module's input or the flow's
// result keeps its provenance, and the location rule of ADR 0011 applies
// there too (FX-15/b, JE-35/d: a failure module or a later step that
// echoes a pickup error).
func copiedGenerated(o conformance.Observation, generated []string, unseen []json.RawMessage) ([]string, error) {
	data, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	canon := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	at := func(p string) (any, bool) {
		toks, err := conformance.SplitPointer(p)
		if err != nil {
			return nil, false
		}
		v := tree
		for _, tok := range toks {
			m, ok := v.(map[string]any)
			if !ok {
				a, isArr := v.([]any)
				k, err := strconv.Atoi(tok)
				if !isArr || err != nil || k < 0 || k >= len(a) {
					return nil, false
				}
				v = a[k]
				continue
			}
			if v, ok = m[tok]; !ok {
				return nil, false
			}
		}
		return v, true
	}
	forms, seen := map[string]bool{}, map[string]bool{}
	var values []any
	for _, p := range generated {
		seen[p] = true
		if v, ok := at(p); ok {
			values = append(values, v)
		}
	}
	for _, raw := range unseen {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) == nil {
			values = append(values, v)
		}
	}
	for _, v := range values {
		forms[canon(v)] = true
		if m, ok := v.(map[string]any); ok && m["error"] != nil {
			forms[canon(m["error"])] = true
			if e, ok := m["error"].(map[string]any); ok && e["message"] != nil {
				forms[canon(e["message"])] = true
			}
		}
	}
	var out []string
	var walk func(v any, toks []string)
	walk = func(v any, toks []string) {
		p := conformance.JoinPointer(toks)
		if seen[p] {
			return
		}
		if forms[canon(v)] {
			seen[p] = true
			out = append(out, p)
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(x)) {
				walk(x[k], append(slices.Clip(toks), k))
			}
		case []any:
			for k, e := range x {
				walk(e, append(slices.Clip(toks), strconv.Itoa(k)))
			}
		}
	}
	if len(forms) == 0 {
		return nil, nil
	}
	if r, ok := at("/result"); ok {
		walk(r, []string{"result"})
	}
	steps, _ := at("/steps")
	sm, _ := steps.(map[string]any)
	for _, site := range slices.Sorted(maps.Keys(sm)) {
		st, _ := sm[site].(map[string]any)
		for _, field := range []string{"args", "resolved_args", "result", "carried"} {
			if v, ok := st[field]; ok {
				walk(v, []string{"steps", site, field})
			}
		}
	}
	return out, nil
}

func boolPtr(b bool) *bool { return &b }

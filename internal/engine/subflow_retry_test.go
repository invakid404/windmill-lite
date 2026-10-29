package engine

import (
	"slices"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Milestone N5's engine tests: a retry on a flow step that runs a flow by
// path (CF-17: the caller's retry applies to a failed subflow), on the
// synthetic folders and the real QuickJS-ng engine of engine_test.go; the
// control-flow/subflow-retry fixture runs the same shapes on the real Bun
// harness against the oracle's recordings (cmd/wltest, TestFlowSuite).

// sp parses a site path.
func sp(t *testing.T, s string) conformance.SitePath {
	t.Helper()
	p, err := conformance.ParseSitePath(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// childAt returns the trace's child flow at site s that is not
// superseded, and fails unless there is exactly one.
func childAt(t *testing.T, tr *Trace, s string) ChildFlow {
	t.Helper()
	var found []ChildFlow
	for _, c := range tr.Children {
		if c.Site.String() == s && !c.Superseded {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d child flows at %s in %+v", len(found), s, tr.Children)
	}
	return found[0]
}

// CF-17, FX-26, FX-27, JE-V2: a flow step whose subflow fails is retried
// like any step: the subflow runs again, a new child flow with the failed
// attempt's arguments, and the failed attempt, the first subflow's job,
// moves to the step's r:0 with everything it ran below it, where the
// walker meets it (OR-11). The step's status lists the attempt, and each
// attempt's steps see their own subflow's job as their flow.
func TestSubflowRetry(t *testing.T) {
	f := &flaky{}
	e := mustOpen(t, map[string]string{
		"f/t/child": flowOf(mod("x", "flaky", inputs("v", js("flow_input.v")), "")),
		"f/t/main": flowOf(
			subflowOf("a", "f/t/child", inputs("v", js("flow_input.n")), retryOnce),
			mod("b", "echo", inputs("a", js("results.a")), ""),
		),
	}, f)
	tr := runReq(t, e, RunRequest{Args: jsonval.FromProducer([]byte(`{"n":3}`))})
	wantStatus(t, tr, conformance.RunSuccess, `{"a":{"v":3}}`)
	wantCalls(t, &f.scripts, "m:a/m:x", "m:a/m:x", "m:b")
	a, r0 := record(t, tr, sp(t, "m:a")), record(t, tr, sp(t, "m:a/r:0"))
	if a.Status != conformance.StepSuccess || a.Attempt != 1 || !slices.Equal(a.FailedRetries, []conformance.SitePath{sp(t, "m:a/r:0")}) {
		t.Fatalf("a %+v", a)
	}
	if r0.Status != conformance.StepFailure || r0.Module != "" || r0.Job == "" || r0.Job == a.Job {
		t.Fatalf("a/r:0 %+v", r0)
	}
	sameJSON(t, "the failed attempt's arguments", r0.RecordedArgs, `{"v":3}`)
	sameJSON(t, "the retry's arguments", a.RecordedArgs, `{"v":3}`)
	if x := record(t, tr, sp(t, "m:a/r:0/m:x")); x.Status != conformance.StepFailure {
		t.Fatalf("a/r:0/x %+v", x)
	}
	if x := record(t, tr, sp(t, "m:a/m:x")); x.Status != conformance.StepSuccess {
		t.Fatalf("a/x %+v", x)
	}
	if c := childAt(t, tr, "m:a/r:0"); c.ID != r0.Job {
		t.Fatalf("the failed attempt's child flow %+v, want job %s", c, r0.Job)
	}
	if c := childAt(t, tr, "m:a"); c.ID != a.Job {
		t.Fatalf("the last attempt's child flow %+v, want job %s", c, a.Job)
	}
	var flows []string
	for _, inv := range f.invs {
		if inv.Site.String() == "m:a/m:x" {
			flows = append(flows, inv.Env["WM_FLOW_JOB_ID"])
		}
	}
	if !slices.Equal(flows, []string{r0.Job, a.Job}) {
		t.Fatalf("the attempts' steps ran in flows %v, want %v", flows, []string{r0.Job, a.Job})
	}
}

// CF-17, FX-26, FX-30, CF-16: a subflow that fails on every attempt fails
// the step once the retries have run out; each failed attempt keeps its
// subflow's steps below its own site; continue_on_error and the failure
// module then apply to the step's last failure.
func TestSubflowRetryExhausted(t *testing.T) {
	child := map[string]string{"f/t/child": flowOf(mod("x", "fail", "", ""))}
	open := func(main string) *Engine {
		flows := map[string]string{"f/t/main": main}
		for k, v := range child {
			flows[k] = v
		}
		return mustOpen(t, flows, &scripts{})
	}
	sub := subflowOf("a", "f/t/child", "", `"retry":{"constant":{"attempts":2,"seconds":0}}`)
	tr := runReq(t, open(flowOf(sub, mod("b", "echo", "", ""))), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, "")
	for _, s := range []string{"m:a/r:0", "m:a/r:0/m:x", "m:a/r:1", "m:a/r:1/m:x", "m:a", "m:a/m:x"} {
		if rec := record(t, tr, sp(t, s)); rec.Status != conformance.StepFailure {
			t.Fatalf("%s %+v", s, rec)
		}
	}
	if a := record(t, tr, sp(t, "m:a")); !slices.Equal(a.FailedRetries, []conformance.SitePath{sp(t, "m:a/r:0"), sp(t, "m:a/r:1")}) {
		t.Fatalf("a's failed attempts %v", a.FailedRetries)
	}
	noStep(t, tr, "b")

	cont := subflowOf("a", "f/t/child", "", retryOnce+`,"continue_on_error":true`)
	tr = runReq(t, open(flowOf(cont, mod("b", "echo", inputs("a", js("results.a.error.message")), ""))), RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `{"a":"boom"}`)

	tr = runReq(t, open(withFailure(handler("echo", inputs("e", js("error.message")), ""), subflowOf("a", "f/t/child", "", retryOnce))), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, `{"e":"boom"}`)
	if rec := record(t, tr, sp(t, "m:a/r:0/m:x")); rec.Status != conformance.StepFailure {
		t.Fatalf("a/r:0/x %+v", rec)
	}
}

// FX-27: a subflow keeps its own retry state. Its retried step's attempts
// stay below the subflow's own attempt when the caller retries that.
func TestSubflowRetryNested(t *testing.T) {
	f := &flaky{}
	e := mustOpen(t, map[string]string{
		// x fails its first invocation and recovers on its own retry; y
		// fails its first, which fails the first subflow.
		"f/t/child": flowOf(mod("x", "flaky", inputs("k", static(`"x"`)), retryOnce), mod("y", "flaky", inputs("k", static(`"y"`)), "")),
		"f/t/main":  flowOf(subflowOf("a", "f/t/child", "", retryOnce)),
	}, f)
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `{"k":"y"}`)
	wantCalls(t, &f.scripts, "m:a/m:x", "m:a/m:x", "m:a/m:y", "m:a/m:x", "m:a/m:y")
	for s, st := range map[string]conformance.StepStatus{
		"m:a/r:0":         conformance.StepFailure,
		"m:a/r:0/m:x/r:0": conformance.StepFailure,
		"m:a/r:0/m:x":     conformance.StepSuccess,
		"m:a/r:0/m:y":     conformance.StepFailure,
		"m:a":             conformance.StepSuccess,
		"m:a/m:x":         conformance.StepSuccess,
		"m:a/m:y":         conformance.StepSuccess,
	} {
		if rec := record(t, tr, sp(t, s)); rec.Status != st {
			t.Fatalf("%s %+v, want %s", s, rec, st)
		}
	}
	if x := record(t, tr, sp(t, "m:a/r:0/m:x")); !slices.Equal(x.FailedRetries, []conformance.SitePath{sp(t, "m:a/r:0/m:x/r:0")}) {
		t.Fatalf("the first subflow's x lists %v", x.FailedRetries)
	}
	if _, ok := tr.Step(sp(t, "m:a/m:x/r:0")); ok {
		t.Fatal("the second subflow's x has an attempt of the first's")
	}
}

// CF-1, CF-7, FX-27: a retried flow step in a loop's body retries inside
// its iteration's child flow, sequential or parallel, each iteration with
// its own attempts.
func TestSubflowRetryInLoops(t *testing.T) {
	for _, parallel := range []string{"false", "true"} {
		f := &flaky{}
		e := mustOpen(t, map[string]string{
			"f/t/child": flowOf(mod("x", "flaky", inputs("v", js("flow_input.v")), "")),
			"f/t/main": flowOf(loopOf("l", []string{subflowOf("a", "f/t/child", inputs("v", js("flow_input.iter.value")), retryOnce)},
				`"iterator":{"type":"static","value":[1,2]},"skip_failures":false,"parallel":`+parallel, "")),
		}, f)
		tr := runReq(t, e, RunRequest{})
		wantStatus(t, tr, conformance.RunSuccess, `[{"v":1},{"v":2}]`)
		for _, it := range []string{"m:l/i:0", "m:l/i:1"} {
			r0, a := record(t, tr, sp(t, it+"/m:a/r:0")), record(t, tr, sp(t, it+"/m:a"))
			if r0.Status != conformance.StepFailure || a.Status != conformance.StepSuccess || !slices.Equal(a.FailedRetries, []conformance.SitePath{sp(t, it+"/m:a/r:0")}) {
				t.Fatalf("parallel %s, %s: r:0 %+v, a %+v", parallel, it, r0, a)
			}
			record(t, tr, sp(t, it+"/m:a/r:0/m:x"))
			childAt(t, tr, it+"/m:a/r:0")
			childAt(t, tr, it+"/m:a")
		}
	}
}

// A retried flow step inside a retried flow step's subflow, and a retried
// loop inside one: the inner attempts, their child flows and their
// modules move with the outer attempt below its r:<k>, each keeping its
// own attempts there (FX-27, CF-22).
func TestSubflowRetryMovesWhatItRan(t *testing.T) {
	f := &flaky{}
	e := mustOpen(t, map[string]string{
		"f/t/inner": flowOf(mod("x", "flaky", inputs("k", static(`"x"`)), "")),
		// b's first subflow fails at x and its retry recovers; y fails its
		// first invocation, which fails mid's first run.
		"f/t/mid":  flowOf(subflowOf("b", "f/t/inner", "", retryOnce), mod("y", "flaky", inputs("k", static(`"y"`)), "")),
		"f/t/main": flowOf(subflowOf("a", "f/t/mid", "", retryOnce)),
	}, f)
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `{"k":"y"}`)
	for s, st := range map[string]conformance.StepStatus{
		"m:a/r:0":             conformance.StepFailure,
		"m:a/r:0/m:b/r:0":     conformance.StepFailure,
		"m:a/r:0/m:b/r:0/m:x": conformance.StepFailure,
		"m:a/r:0/m:b":         conformance.StepSuccess,
		"m:a/r:0/m:b/m:x":     conformance.StepSuccess,
		"m:a/r:0/m:y":         conformance.StepFailure,
		"m:a":                 conformance.StepSuccess,
		"m:a/m:b":             conformance.StepSuccess,
		"m:a/m:b/m:x":         conformance.StepSuccess,
		"m:a/m:y":             conformance.StepSuccess,
	} {
		if rec := record(t, tr, sp(t, s)); rec.Status != st || rec.Superseded {
			t.Fatalf("%s %+v, want %s", s, rec, st)
		}
	}
	if b := record(t, tr, sp(t, "m:a/r:0/m:b")); !slices.Equal(b.FailedRetries, []conformance.SitePath{sp(t, "m:a/r:0/m:b/r:0")}) {
		t.Fatalf("the first run of mid lists %v for b", b.FailedRetries)
	}
	for _, s := range []string{"m:a/r:0", "m:a/r:0/m:b/r:0", "m:a/r:0/m:b", "m:a", "m:a/m:b"} {
		childAt(t, tr, s)
	}

	f = &flaky{}
	e = mustOpen(t, map[string]string{
		// l's first iteration fails at z and the loop's retry recovers; w
		// fails its first invocation, which fails lsub's first run.
		"f/t/lsub": flowOf(loopOf("l", []string{mod("z", "flaky", inputs("v", js("flow_input.iter.value")), ""), value("c", "1", "")}, `"iterator":{"type":"static","value":[1]},"skip_failures":false`, retryOnce), mod("w", "flaky", inputs("k", static(`"w"`)), "")),
		"f/t/main": flowOf(subflowOf("a", "f/t/lsub", "", retryOnce)),
	}, f)
	tr = runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `{"k":"w"}`)
	for _, s := range []string{"m:a/r:0/m:l/r:0", "m:a/r:0/m:l/r:0/m:z", "m:a/r:0/m:l", "m:a/r:0/m:l/i:0/m:z", "m:a/r:0/m:w", "m:a/m:l", "m:a/m:l/i:0/m:z", "m:a/m:w"} {
		if rec := record(t, tr, sp(t, s)); rec.Superseded {
			t.Fatalf("%s superseded", s)
		}
	}
	if l := record(t, tr, sp(t, "m:a/r:0/m:l")); !slices.Equal(l.FailedRetries, []conformance.SitePath{sp(t, "m:a/r:0/m:l/r:0")}) {
		t.Fatalf("the first run of lsub lists %v for l", l.FailedRetries)
	}
	for _, s := range []string{"m:a/r:0", "m:a/r:0/m:l/r:0", "m:a/r:0/m:l/i:0", "m:a", "m:a/m:l/i:0"} {
		childAt(t, tr, s)
	}
}

// FX-14, FX-27, JE-V2: a flow step whose input transform throws launches
// no subflow and fails; its attempt keeps its own record at r:0, and the
// retry runs the subflow with the failed attempt's recorded arguments, an
// empty object, without evaluating the transform again (recorded by
// control-flow/subflow-retry's run pre_run).
func TestSubflowRetryAfterAPreRunFailure(t *testing.T) {
	s := &scripts{}
	e := mustOpen(t, map[string]string{
		"f/t/child": flowOf(mod("x", "echo", inputs("v", js("flow_input.v ?? null")), "")),
		"f/t/main":  flowOf(subflowOf("a", "f/t/child", inputs("v", js("flow_input.missing.x")), retryOnce)),
	}, s)
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `{"v":null}`)
	wantCalls(t, s, "m:a/m:x")
	r0 := record(t, tr, sp(t, "m:a/r:0"))
	if r0.Status != conformance.StepFailure || r0.Job != "" || !r0.Ran {
		t.Fatalf("a/r:0 %+v", r0)
	}
	sameJSON(t, "the retry's arguments", record(t, tr, sp(t, "m:a")).RecordedArgs, `{}`)
	for _, c := range tr.Children {
		if c.Site.String() != "m:a" {
			t.Fatalf("a child flow at %s", c.Site)
		}
	}
}

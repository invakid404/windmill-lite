package engine

import (
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// The tests of failure modules in container bodies (milestone N3c): every
// child flow carries a copy of its enclosing flow's failure module (CF-1,
// CF-16, FX-34).

// CF-16, FX-34, FX-32, CF-8: in a flow with a failure module, a loop never
// collapses and each iteration's child flow runs its copy of the failure
// module, at <iteration>/s:failure with its own id, once per failed
// iteration; its result is the iteration's entry. It sees the child's
// input, the failing value, and the child's job as flow_job_id. The flow's
// own failure module does not run again for the loop's failure, whether
// skip_failures takes it or not (CF-16 item 4, CF-16/b).
func TestFailureModuleInLoopBodies(t *testing.T) {
	copyOf := handler("echo", inputs("v", js("flow_input.iter.value"), "e", js("previous_result.error.message"), "j", js("flow_job_id")), "")
	loop := func(skip bool) string {
		return loopOf("l", []string{mod("x", "fail", "", "")}, `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":`+map[bool]string{true: "true", false: "false"}[skip], "")
	}
	s := &loopSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": withFailure(copyOf, loop(true))}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, "")
	arr := arrayElems(t, tr.Result)
	if len(arr) != 2 || len(tr.Children) != 2 || !tr.Children[0].Failure {
		t.Fatalf("result %s, children %+v", tr.Result, tr.Children)
	}
	h := at(t, tr, "m:l/i:1/s:failure")
	sameJSON(t, "i:1's handler arguments", h.RecordedArgs, `{"v":2,"e":"boom","j":"`+tr.Children[1].ID+`"}`)
	if inv := s.inv(t, "m:l/i:1/s:failure"); inv.Env["WM_FLOW_STEP_ID"] != "failure" || inv.Env["WM_FLOW_JOB_ID"] != tr.Children[1].ID {
		t.Fatalf("the copy's environment %v", inv.Env)
	}
	noRecord(t, tr, "s:failure")

	// Without skip_failures the loop stops after the first handled failure
	// and fails the flow with its array; the flow's own failure module does
	// not run for it.
	e = mustOpen(t, map[string]string{"f/t/main": withFailure(copyOf, loop(false))}, &loopSteps{})
	tr = runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "")
	noRecord(t, tr, "m:l/i:1/m:x")
	noRecord(t, tr, "s:failure")
	array := "[" + string(at(t, tr, "m:l/i:0/s:failure").Result.Producer()) + "]"
	sameJSON(t, "the loop's array", at(t, tr, "m:l").Result, array)
	sameJSON(t, "the flow's result", tr.Result, array)
}

// CF-16, CF-V1, FX-33: a child flow whose failure module recovered counts
// as failed for a sequential loop, a sequential branchall and a branchone,
// and as succeeded for a parallel loop and a parallel branchall, which
// judge its status. Either way the flow's own failure module does not run
// again for it (CF-16 item 4, CF-V1/a). The sequential container's failure
// is marked as recovered, so the root flow that it ends succeeds with the
// container's result (CF-V1/a, CF-V1/c at v1.862.0).
func TestFailureModuleRecoveryInContainers(t *testing.T) {
	recover := handler("value", inputs("v", static(`{"recover":true}`)), "")
	fail := mod("x", "fail", "", "")
	for _, c := range []struct {
		name, flow string
		result     string
	}{
		{"sequential loop", withFailure(recover, loopOf("l", []string{fail}, `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":false`, "")), `[{"recover":true}]`},
		{"parallel loop", withFailure(recover, loopOf("l", []string{fail}, `"iterator":{"type":"static","value":[1,2]},"parallel":true,"skip_failures":false`, "")), `[{"recover":true},{"recover":true}]`},
		{"sequential branchall", withFailure(recover, branchAllOf("l", []string{branch(skipFailure(false), fail), branch(skipFailure(false), mod("y", "fail", "", ""))}, false, "")), `[{"recover":true}]`},
		{"parallel branchall", withFailure(recover, branchAllOf("l", []string{branch(skipFailure(false), fail), branch(skipFailure(false), mod("y", "fail", "", ""))}, true, "")), `[{"recover":true},{"recover":true}]`},
		{"branchone", withFailure(recover, branchOneOf("l", nil, []string{fail}, "")), `{"recover":true}`},
		{"while loop", withFailure(recover, whileOf("l", []string{fail}, false, "")), `[{"recover":true}]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, &loopSteps{})
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, conformance.RunSuccess, c.result)
			l := at(t, tr, "m:l")
			// A sequential container fails with its result, and the root
			// flow it ends succeeds; a parallel one succeeds.
			parallel := strings.HasPrefix(c.name, "parallel")
			if parallel != (l.Module == conformance.ModuleSuccess) {
				t.Fatalf("the container %+v", l)
			}
			noRecord(t, tr, "s:failure")
		})
	}
	// A recovered branch is a successful job but a failed branchone.
	e := mustOpen(t, map[string]string{"f/t/main": withFailure(recover, branchOneOf("l", nil, []string{fail}, `"continue_on_error":true`), mod("n", "echo", "", ""))}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	if l := at(t, tr, "m:l"); l.Status != conformance.StepSuccess || l.Module != conformance.ModuleFailure {
		t.Fatalf("the branchone %+v", l)
	}
}

// FX-33, CF-V1, CF-16 item 5: the recovered mark of a failure. A subflow
// by path whose failure module recovered fails its step with the mark, and
// a flow that the step's failure ends passes it on: an iteration's flow,
// which has no failure module of its own, fails, and the sequential loop
// it fails keeps the mark, so the root flow succeeds with the loop's
// array. A parallel loop judges its iterations by their status and drops
// the mark, and so does a stop of the failed sequential loop, clean or an
// error, so the root flow fails.
func TestRecoveredFailureMark(t *testing.T) {
	child := withFailure(handler("value", inputs("v", static(`{"recover":true}`)), ""), mod("a", "fail", "", ""))
	body := []string{subflowOf("t", "f/t/child", "", "")}
	const seq, par = `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":false`, `"iterator":{"type":"static","value":[1,2]},"parallel":true,"skip_failures":false`
	for _, c := range []struct {
		name, main string
		status     conformance.RunStatus
		result     string
	}{
		{"sequential loop", flowOf(loopOf("l", body, seq, "")), conformance.RunSuccess, `[{"recover":true}]`},
		{"parallel loop", flowOf(loopOf("l", body, par, "")), conformance.RunFailure, `[{"recover":true},{"recover":true}]`},
		{"clean stop", flowOf(loopOf("l", body, seq, `"stop_after_all_iters_if":{"expr":"true","skip_if_stopped":false}`)), conformance.RunFailure, `[{"recover":true}]`},
		{"error stop", flowOf(loopOf("l", body, seq, `"stop_after_all_iters_if":{"expr":"null.x","skip_if_stopped":false}`)), conformance.RunFailure, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/child": child, "f/t/main": c.main}, &loopSteps{})
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, c.status, c.result)
			// The subflow job recovered, so it is a successful job, whose
			// step failed.
			if s := at(t, tr, "m:l/i:0/m:t"); s.Status != conformance.StepSuccess || s.Module != conformance.ModuleFailure {
				t.Fatalf("the flow step %+v", s)
			}
			if l := at(t, tr, "m:l"); l.Status != conformance.StepFailure || l.Module != conformance.ModuleFailure {
				t.Fatalf("the loop %+v", l)
			}
		})
	}
}

// CF-16 item 4, CF-13, CF-12 item 4: a failed inner loop whose clean
// stop_after_all_iters_if keeps its failure stops its flow with that
// failure, which its child's copy already handled, so neither the flow it
// is in nor any above it runs the failure module again, whether a loop,
// a branchone or a sequential branchall forwards it. An error stop of the
// inner loop's predicate is a failure of its own, for which the flow it
// is in runs its copy, and the levels above still do not run theirs.
func TestFailureModuleOnceThroughACleanContainerStop(t *testing.T) {
	copyOf := handler("value", inputs("v", static(`"handled"`)), "")
	inner := func(stop string) string {
		return loopOf("inner", []string{mod("x", "fail", "", "")}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`,
			`"stop_after_all_iters_if":{"expr":"true",`+stop+`"skip_if_stopped":false}`)
	}
	for _, c := range []struct {
		name, outer, at, result string
	}{
		{"loop", loopOf("o", []string{inner("")}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""), "m:o/i:0/m:inner/i:0/s:failure", `[["handled"]]`},
		{"branchone", branchOneOf("o", nil, []string{inner("")}, ""), "m:o/b:default/m:inner/i:0/s:failure", `["handled"]`},
		{"sequential branchall", branchAllOf("o", []string{branch(skipFailure(false), inner(""))}, false, ""), "m:o/b:0/m:inner/i:0/s:failure", `[["handled"]]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": withFailure(copyOf, c.outer)}, &loopSteps{})
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, conformance.RunFailure, c.result)
			if h := at(t, tr, c.at); !h.Ran {
				t.Fatalf("the copy that handled the failure %+v", h)
			}
			var ran []string
			for _, rec := range tr.Steps {
				if rec.Ran && strings.HasSuffix(rec.Site.String(), "s:failure") {
					ran = append(ran, rec.Site.String())
				}
			}
			if len(ran) != 1 {
				t.Fatalf("failure modules that ran: %v", ran)
			}
		})
	}
	// The predicate's error stop is a failure of its own: the outer
	// iteration's copy runs for it, and the flow's own does not.
	e := mustOpen(t, map[string]string{"f/t/main": withFailure(copyOf,
		loopOf("o", []string{inner(`"error_message":"halt",`)}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""),
	)}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, `["handled"]`)
	for _, p := range []string{"m:o/i:0/m:inner/i:0/s:failure", "m:o/i:0/s:failure"} {
		if h := at(t, tr, p); !h.Ran {
			t.Fatalf("%s %+v", p, h)
		}
	}
	noRecord(t, tr, "s:failure")
}

// CF-12 item 3: an error stop in a body runs the child's copy of the
// failure module, whose completion does not forward the stop: the
// container sees an ordinary failed child, so a sequential loop with
// skip_failures goes on, and a branchone fails without stopping its flow.
func TestFailureModuleAfterErrorStopInBodies(t *testing.T) {
	copyOf := handler("value", inputs("v", static(`"handled"`)), "")
	stop := mod("b", "value", inputs("v", js("flow_input.iter.value")), `"stop_after_if":{"expr":"true","error_message":"halt","skip_if_stopped":false}`)
	e := mustOpen(t, map[string]string{"f/t/main": withFailure(copyOf,
		loopOf("l", []string{stop, mod("c", "value", inputs("v", static("0")), "")}, `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":true`, ""),
	)}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `["handled","handled"]`)
	noRecord(t, tr, "m:l/i:0/m:c")
	noRecord(t, tr, "s:failure")

	stopOne := mod("b", "value", inputs("v", static("1")), `"stop_after_if":{"expr":"true","error_message":"halt","skip_if_stopped":false}`)
	e = mustOpen(t, map[string]string{"f/t/main": withFailure(copyOf,
		branchOneOf("br", nil, []string{stopOne}, `"continue_on_error":true`),
		mod("n", "echo", inputs("p", js("previous_result")), ""),
	)}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `{"p":"handled"}`)
}

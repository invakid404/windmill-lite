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
// again for it (CF-16 item 4, CF-V1/a).
func TestFailureModuleRecoveryInContainers(t *testing.T) {
	recover := handler("value", inputs("v", static(`{"recover":true}`)), "")
	fail := mod("x", "fail", "", "")
	for _, c := range []struct {
		name, flow string
		status     conformance.RunStatus
		result     string
	}{
		{"sequential loop", withFailure(recover, loopOf("l", []string{fail}, `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":false`, "")), conformance.RunFailure, `[{"recover":true}]`},
		{"parallel loop", withFailure(recover, loopOf("l", []string{fail}, `"iterator":{"type":"static","value":[1,2]},"parallel":true,"skip_failures":false`, "")), conformance.RunSuccess, `[{"recover":true},{"recover":true}]`},
		{"sequential branchall", withFailure(recover, branchAllOf("l", []string{branch(skipFailure(false), fail), branch(skipFailure(false), mod("y", "fail", "", ""))}, false, "")), conformance.RunFailure, `[{"recover":true}]`},
		{"parallel branchall", withFailure(recover, branchAllOf("l", []string{branch(skipFailure(false), fail), branch(skipFailure(false), mod("y", "fail", "", ""))}, true, "")), conformance.RunSuccess, `[{"recover":true},{"recover":true}]`},
		{"branchone", withFailure(recover, branchOneOf("l", nil, []string{fail}, "")), conformance.RunFailure, `{"recover":true}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, &loopSteps{})
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, c.status, c.result)
			l := at(t, tr, "m:l")
			// A sequential container fails the flow with its result; a
			// parallel one succeeds.
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

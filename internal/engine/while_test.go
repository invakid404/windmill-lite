package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The while-loop tests (milestone N3b) run synthetic flows on the real
// QuickJS-ng engine, with steps answered in Go; the E3d and E3a while-loop
// fixtures of conformance/flows run the same loops on the real Bun harness
// (cmd/wltest, TestFlowSuite).

// whileOf writes a while loop module: its body modules and its
// skip_failures, with envelope fields after them.
func whileOf(id string, body []string, skip bool, envelope string) string {
	if envelope != "" {
		envelope = "," + envelope
	}
	return fmt.Sprintf(`{"id":%q,"value":{"type":"whileloopflow","modules":[%s],"skip_failures":%v}%s}`, id, strings.Join(body, ","), skip, envelope)
}

// breakAt is a loop module's stop_after_if that holds when expr does.
func breakAt(expr string) string {
	b, _ := json.Marshal(expr)
	return `"stop_after_if":{"expr":` + string(b) + `,"skip_if_stopped":false}`
}

// memberValue is a member of a JSON object's text, as a value.
func memberValue(t *testing.T, v jsonval.Value, key string) jsonval.Value {
	t.Helper()
	return jsonval.FromProducer([]byte(memberOf(t, v, key)))
}

// arrayElems returns the elements of a JSON array's text.
func arrayElems(t *testing.T, v jsonval.Value) []json.RawMessage {
	t.Helper()
	var a []json.RawMessage
	if err := json.Unmarshal(v.Producer(), &a); err != nil {
		t.Fatalf("%s: %v", v, err)
	}
	return a
}

// CF-10, CF-3, CF-8, CF-11, CF-24, JE-36: a while loop with a one-step body
// collapses in every job kind; iteration k's iter is {"index": k, "value":
// k}; its stop_after_if breaks it after the iteration that made it true,
// which the result keeps, and the flow continues. Each iteration's
// previous_result is the previous iteration's result, the step before the
// loop's at first; the body job runs in the loop's flow with the loop's id
// as its step id, and an inline body's job path is <flow>/loop-<i>.
func TestWhileCollapsed(t *testing.T) {
	s := &loopSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		mod("a", "value", inputs("v", static("9")), ""),
		whileOf("w", []string{inlineOf("b", inputs("prev", js("previous_result"), "iter", js("flow_input.iter")))}, false, breakAt("result.iter.index >= 2")),
		mod("n", "echo", inputs("x", js("previous_result"), "b", js("results.b")), ""),
	)}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, "")
	w := at(t, tr, "m:w")
	if !w.Ran || w.Status != conformance.StepSuccess || len(tr.Children) != 0 {
		t.Fatalf("the loop %+v, children %+v", w, tr.Children)
	}
	sameJSON(t, "i:0's arguments", at(t, tr, "m:w/i:0/m:b").RecordedArgs, `{"prev":9,"iter":{"index":0,"value":0}}`)
	sameJSON(t, "i:1's arguments", at(t, tr, "m:w/i:1/m:b").RecordedArgs, `{"prev":{"prev":9,"iter":{"index":0,"value":0}},"iter":{"index":1,"value":1}}`)
	sameJSON(t, "i:2's iter", memberValue(t, at(t, tr, "m:w/i:2/m:b").RecordedArgs, "iter"), `{"index":2,"value":2}`)
	noRecord(t, tr, "m:w/i:3/m:b")
	if got := len(arrayElems(t, w.Result)); got != 3 {
		t.Fatalf("the loop's result has %d entries: %s", got, w.Result)
	}
	// Nothing is registered under the body's id (CF-8).
	sameJSON(t, "n's b", memberValue(t, step(t, tr, "n").RecordedArgs, "b"), `null`)
	inv := s.inv(t, "m:w/i:1/m:b")
	if inv.Env["WM_FLOW_STEP_ID"] != "w" || inv.Env["WM_JOB_PATH"] != "f/t/main/loop-1" || inv.Env["WM_FLOW_JOB_ID"] != tr.RunID {
		t.Fatalf("a collapsed iteration's environment %v", inv.Env)
	}

	// A loop at the first module gives every iteration the flow's input
	// (JE-36 rule 1); its stop_after_if sees the flow's input, with no iter
	// (CF-11).
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(
		whileOf("w", []string{mod("b", "echo", inputs("prev", js("previous_result")), "")}, false, breakAt("all_iters.length === 2 && flow_input.iter === undefined")),
	)}, &loopSteps{})
	tr = runIt(t, e, `{"k":1}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `[{"prev":{"k":1}},{"prev":{"k":1}}]`)
}

// CF-10, CF-1, CF-9, CF-19, CF-24, WA-6: a two-step body runs as a child
// flow per iteration, whose path is <flow>/loop-<i> and whose first step's
// previous_result is its input; its steps write into the root's results
// scope, so the last iteration's are visible after the loop.
func TestWhileChildFlows(t *testing.T) {
	s := &loopSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		whileOf("w", []string{
			mod("b", "echo", inputs("prev", js("previous_result")), ""),
			inlineOf("c", inputs("i", js("flow_input.iter.value"))),
		}, false, breakAt("all_iters.length === 2")),
		mod("n", "echo", inputs("c", js("results.c"), "w", js("results.w")), ""),
	)}, s)
	tr := runIt(t, e, `{"k":1}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"c":{"i":1},"w":[{"i":0},{"i":1}]}`)
	sameJSON(t, "b's arguments at i:1", at(t, tr, "m:w/i:1/m:b").RecordedArgs, `{"prev":{"k":1,"iter":{"index":1,"value":1}}}`)
	if len(tr.Children) != 2 || tr.Children[1].Site.String() != "m:w/i:1" || !slices.Equal(tr.Children[1].Modules, []string{"b", "c"}) {
		t.Fatalf("children %+v", tr.Children)
	}
	c := s.inv(t, "m:w/i:1/m:c")
	if c.Env["WM_FLOW_JOB_ID"] != tr.Children[1].ID || c.Env["WM_ROOT_FLOW_JOB_ID"] != tr.RunID ||
		c.Env["WM_FLOW_PATH"] != "f/t/main/loop-1" || c.Env["WM_JOB_PATH"] != "f/t/main/loop-1/c" {
		t.Fatalf("a body step's environment %v", c.Env)
	}
}

// CF-10, CF-5, CF-6, CF-12, CF-29, CF-13: a failed iteration ends a while
// loop unless skip_failures, which takes it and goes on; a clean stop in
// the body breaks it and an error stop breaks it too, failing it unless
// skip_failures; an empty body returns [] at once and evaluates its
// stop_after_if as an ordinary stop, never its stop_after_all_iters_if,
// which an active loop evaluates once with its array.
func TestWhileEnds(t *testing.T) {
	failAt1 := mod("b", "fail", "", `"skip_if":{"expr":"flow_input.iter.index !== 1"}`)
	after := mod("n", "value", inputs("v", static(`"after"`)), "")
	for _, c := range []struct {
		name   string
		flow   string
		status conformance.RunStatus
		result string
		iters  int
	}{
		{"a failure ends it", flowOf(whileOf("w", []string{failAt1}, false, ""), after), conformance.RunFailure, "", 2},
		{"skip_failures goes on", flowOf(whileOf("w", []string{failAt1}, true, breakAt("result.iter.index === 2")), after), conformance.RunSuccess, `"after"`, 3},
		{"a clean stop breaks it", flowOf(whileOf("w", []string{mod("b", "value", inputs("v", js("flow_input.iter.index")), `"stop_after_if":{"expr":"result === 1","skip_if_stopped":false}`)}, false, ""), after), conformance.RunSuccess, `"after"`, 2},
		{"an error stop fails it", flowOf(whileOf("w", []string{mod("b", "value", inputs("v", js("flow_input.iter.index")), `"stop_after_if":{"expr":"result === 1","error_message":"halt","skip_if_stopped":false}`)}, false, ""), after), conformance.RunFailure, `[0,{"error":{"name":"EarlyStopError","message":"halt"}}]`, 2},
		{"an error stop with skip_failures", flowOf(whileOf("w", []string{mod("b", "value", inputs("v", js("flow_input.iter.index")), `"stop_after_if":{"expr":"result === 1","error_message":"halt","skip_if_stopped":false}`)}, true, ""), after), conformance.RunSuccess, `"after"`, 2},
		{"an empty body", flowOf(whileOf("w", nil, false, `"stop_after_if":{"expr":"result.length === 0","skip_if_stopped":true},"stop_after_all_iters_if":{"expr":"nope","skip_if_stopped":false}`), after), conformance.RunSkipped, `[]`, 0},
		{"stop_after_all_iters_if", flowOf(whileOf("w", []string{mod("b", "value", inputs("v", js("flow_input.iter.index")), "")}, false, breakAt("result === 1")+`,"stop_after_all_iters_if":{"expr":"result.length === 2","error_message":"all","skip_if_stopped":false}`), after), conformance.RunFailure, `{"error":{"name":"EarlyStopError","message":"all"}}`, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &loopSteps{}
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, s)
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, c.status, c.result)
			if got := len(arrayElems(t, at(t, tr, "m:w").Result)); got != c.iters {
				t.Fatalf("%d iterations: %s", got, at(t, tr, "m:w").Result)
			}
			if c.status == conformance.RunFailure || c.status == conformance.RunSkipped {
				noRecord(t, tr, "m:n")
			}
		})
	}
}

// CF-10, CF-28, FX-38: nothing but the run's end stops a while loop with
// no stop: its cancellation abandons the running iteration and starts no
// other, and the run ends canceled. The loop completes as a failure whose
// array ends with the killed body job's result (recorded by cf-28/held).
func TestWhileRunsUntilCanceled(t *testing.T) {
	g := newGated()
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(whileOf("w", []string{mod("b", "value", inputs("v", js("flow_input.iter.index")), "")}, false, ""))}, g)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := runAsync(ctx, e, `{}`, nil)
	for k := range 5 {
		if s := g.next(t); s != fmt.Sprintf("m:w/i:%d/m:b", k) {
			t.Fatalf("iteration %d started at %s", k, s)
		}
		if k < 4 {
			g.done(fmt.Sprintf("m:w/i:%d/m:b", k))
		}
	}
	cancel()
	tr := <-out
	wantStatus(t, tr, conformance.RunCanceled, "")
	if w := at(t, tr, "m:w"); w.Status != conformance.StepFailure || len(arrayElems(t, w.Result)) != 5 || errorName(jsonval.FromProducer(arrayElems(t, w.Result)[4])) != "ExecutionErr" {
		t.Fatalf("the loop %+v", w)
	}
}

// ADR 0008 H8 in a while loop: a body module's halt-after halts while the
// loop would start another iteration, and a stop that ends the flow keeps
// its status.
func TestWhileHaltAfter(t *testing.T) {
	body := []string{mod("b", "value", inputs("v", js("flow_input.iter.index")), "")}
	s := &loopSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(whileOf("w", body, false, breakAt("result === 3")))}, s)
	if tr := runIt(t, e, `{}`, haltAt{after: "m:w/i:0/m:b"}); tr.Status != conformance.RunHalted || len(s.invs) != 1 {
		t.Fatalf("status %s, invocations %d", tr.Status, len(s.invs))
	}
	stop := []string{mod("b", "value", inputs("v", static("0")), `"stop_after_if":{"expr":"true","skip_if_stopped":false}`)}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(whileOf("w", stop, false, ""))}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, haltAt{after: "m:w/i:0/m:b"}), conformance.RunSuccess, `[0]`)
}

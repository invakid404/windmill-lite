package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The for-loop tests (milestone N3a) run synthetic flows on the real
// QuickJS-ng engine, with steps answered in Go; the E3d and E3a for-loop
// fixtures of conformance/flows run the same loops on the real Bun harness
// (cmd/wltest, TestFlowSuite).

// loopOf writes a for loop module: its body modules and its fields, as
// JSON, with envelope fields after them.
func loopOf(id string, body []string, fields, envelope string) string {
	if envelope != "" {
		envelope = "," + envelope
	}
	return fmt.Sprintf(`{"id":%q,"value":{"type":"forloopflow","modules":[%s],%s}%s}`, id, strings.Join(body, ","), fields, envelope)
}

// inlineOf writes an inline Bun step, which loopSteps answers by echoing
// its arguments.
func inlineOf(id, inputs string) string {
	return fmt.Sprintf(`{"id":%q,"value":{"type":"rawscript","language":"bun","content":"export async function main(...a: any[]) { return null }","input_transforms":%s}}`, id, inputs)
}

// loopSteps answers the f/t scripts as scripts does, and an inline step by
// echoing its arguments.
type loopSteps struct{ scripts }

func (s *loopSteps) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	if strings.HasPrefix(inv.Script, "inline-") {
		s.mu.Lock()
		s.invs = append(s.invs, inv)
		s.mu.Unlock()
		defer inv.Report.Settled(SettleCompleted)
		return StepOutcome{Kind: OutcomeOK, Body: inv.Args}, nil
	}
	return s.scripts.Execute(ctx, inv)
}

// inv returns the invocation made at site p.
func (s *scripts) inv(t *testing.T, p string) Invocation {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, inv := range s.invs {
		if inv.Site.String() == p {
			return inv
		}
	}
	t.Fatalf("no invocation at %s in %v", p, s.invs)
	return Invocation{}
}

// at returns the record of the site written p.
func at(t *testing.T, tr *Trace, p string) StepRecord {
	t.Helper()
	site, err := conformance.ParseSitePath(p)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := tr.Step(site)
	if !ok {
		var sites []string
		for _, s := range tr.Steps {
			sites = append(sites, s.Site.String())
		}
		t.Fatalf("no record at %s; records %v", p, sites)
	}
	return rec
}

func noRecord(t *testing.T, tr *Trace, p string) {
	t.Helper()
	site, _ := conformance.ParseSitePath(p)
	if _, ok := tr.Step(site); ok {
		t.Fatalf("a record at %s", p)
	}
}

// CF-1, CF-3, CF-9, CF-19, JE-19, WA-6, WA-V1: a body of two steps runs as
// a child flow per iteration. Its input is the flow input plus iter, which
// its first step gets as previous_result; its steps write into the root's
// results scope, so the last iteration's are visible after the loop; its
// steps see the child flow's job id and path and the root's innermost
// scope.
func TestLoopSequentialChildFlows(t *testing.T) {
	s := &loopSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		mod("a", "value", inputs("v", static("7")), ""),
		loopOf("l", []string{
			mod("b", "echo", inputs("prev", js("previous_result"), "iter", js("flow_input.iter")), ""),
			inlineOf("c", inputs("a", js("results.a"), "b", js("results.b.iter.value"))),
		}, `"iterator":{"type":"static","value":[10,20]},"parallel":false,"skip_failures":false`, ""),
		mod("n", "echo", inputs("b", js("results.b"), "c", js("results.c"), "l", js("results.l")), ""),
	)}, s)
	tr := runIt(t, e, `{"k":1}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, "")
	sameJSON(t, "b's arguments at i:0", at(t, tr, "m:l/i:0/m:b").RecordedArgs, `{"prev":{"k":1,"iter":{"index":0,"value":10}},"iter":{"index":0,"value":10}}`)
	sameJSON(t, "c's arguments at i:1", at(t, tr, "m:l/i:1/m:c").RecordedArgs, `{"a":7,"b":20}`)
	l := at(t, tr, "m:l")
	if !l.Ran || l.Status != conformance.StepSuccess || l.Module != conformance.ModuleSuccess {
		t.Fatalf("the loop %+v", l)
	}
	sameJSON(t, "the loop's result", l.Result, `[{"a":7,"b":10},{"a":7,"b":20}]`)
	sameJSON(t, "n's arguments", step(t, tr, "n").RecordedArgs, `{"b":{"prev":{"k":1,"iter":{"index":1,"value":20}},"iter":{"index":1,"value":20}},"c":{"a":7,"b":20},"l":[{"a":7,"b":10},{"a":7,"b":20}]}`)

	if len(tr.Children) != 2 || tr.Children[1].Site.String() != "m:l/i:1" || !slices.Equal(tr.Children[1].Modules, []string{"b", "c"}) {
		t.Fatalf("children %+v", tr.Children)
	}
	b, c := s.inv(t, "m:l/i:1/m:b"), s.inv(t, "m:l/i:1/m:c")
	if b.Env["WM_FLOW_JOB_ID"] != tr.Children[1].ID || b.Env["WM_ROOT_FLOW_JOB_ID"] != tr.RunID || b.Env["WM_ROOT_JOB_ID"] != tr.RunID ||
		b.Env["WM_FLOW_PATH"] != "f/t/main/loop-1" || b.Env["WM_FLOW_STEP_ID"] != "b" || b.Env["WM_JOB_PATH"] != "f/t/echo" {
		t.Fatalf("b's environment %v", b.Env)
	}
	if c.Env["WM_JOB_PATH"] != "f/t/main/loop-1/c" || c.Env["WM_FLOW_STEP_ID"] != "c" {
		t.Fatalf("an inline body step's job path is its child flow's path and its id: %v", c.Env)
	}
}

// CF-8, JE-36, CF-24, JE-19: a sequential loop with a one-step body
// collapses. Each iteration's previous_result is the previous iteration's
// result, the step before the loop's at first; the body job runs in the
// loop's flow with the loop's id as its step id; nothing is registered
// under the body's id. An inline body's job path is <flow>/loop-<i>.
func TestLoopCollapsed(t *testing.T) {
	s := &loopSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		mod("a", "value", inputs("v", static("100")), ""),
		loopOf("l", []string{mod("b", "value", inputs("v", js(`(typeof previous_result === "number" ? previous_result : -1000) + flow_input.iter.value`)), "")},
			`"iterator":{"type":"static","value":[1,2,3]},"parallel":false,"skip_failures":false`, ""),
		loopOf("m", []string{inlineOf("i", inputs("x", js("flow_input.iter.index"), "seen", js("results.m.length")))},
			`"iterator":{"type":"static","value":["p","q"]},"parallel":false,"skip_failures":false`, ""),
		mod("n", "echo", inputs("b", js("results.b"), "i", js("results.i"), "l", js("results.l")), ""),
	)}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, "")
	sameJSON(t, "the collapsed loop's result", at(t, tr, "m:l").Result, `[101,103,106]`)
	sameJSON(t, "n's arguments", step(t, tr, "n").RecordedArgs, `{"b":null,"i":null,"l":[101,103,106]}`)
	if len(tr.Children) != 0 {
		t.Fatalf("a collapsed loop creates no child flow: %+v", tr.Children)
	}
	b := s.inv(t, "m:l/i:2/m:b")
	if b.Env["WM_FLOW_STEP_ID"] != "l" || b.Env["WM_FLOW_JOB_ID"] != tr.RunID || b.Env["WM_FLOW_PATH"] != "f/t/main" || b.Env["WM_JOB_PATH"] != "f/t/value" {
		t.Fatalf("a collapsed body's environment %v", b.Env)
	}
	if i := s.inv(t, "m:m/i:1/m:i"); i.Env["WM_JOB_PATH"] != "f/t/main/loop-1" || i.Env["WM_FLOW_STEP_ID"] != "m" {
		t.Fatalf("a collapsed inline body's job path is <flow>/loop-<i>: %v", i.Env)
	}
	// Reading: a collapsed iteration that reads its own loop sees the
	// iterations so far (JE-18 step 2).
	sameJSON(t, "m's result", at(t, tr, "m:m").Result, `[{"x":0,"seen":0},{"x":1,"seen":1}]`)

	// CF-8 item 5: a collapsed body takes its loop's
	// pass_flow_input_directly, not its own, and then receives its
	// iteration's flow input.
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(
		loopOf("l", []string{mod("b", "echo", inputs("x", static("1")), `"pass_flow_input_directly":true`)}, `"iterator":{"type":"static","value":[5]},"parallel":false,"skip_failures":false`, ""),
		loopOf("m", []string{mod("c", "echo", inputs("x", static("1")), "")}, `"iterator":{"type":"static","value":[6]},"parallel":false,"skip_failures":false`, `"pass_flow_input_directly":true`),
	)}, &loopSteps{})
	tr = runIt(t, e, `{"z":1}`, nil)
	sameJSON(t, "l's result", at(t, tr, "m:l").Result, `[{"x":1}]`)
	sameJSON(t, "m's result", at(t, tr, "m:m").Result, `[{"z":1,"iter":{"index":0,"value":6}}]`)

	// CF-8 item 5: a collapsed body's own continue_on_error is not read,
	// so its failure fails the iteration and, without skip_failures, the
	// loop; the loop's own continue_on_error then lets the flow go on.
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(
		loopOf("l", []string{mod("b", "fail", "", `"continue_on_error":true`)}, `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":false`, `"continue_on_error":true`),
		mod("n", "echo", inputs("p", js("previous_result")), ""),
	)}, &loopSteps{})
	tr = runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"p":[{"error":{"message":"boom","name":"Error","step_id":"l"}}]}`)
	if l := at(t, tr, "m:l"); l.Status != conformance.StepFailure {
		t.Fatalf("the loop %+v", l)
	}

	// JE-36 rule 1: a collapsed loop at the first module gives every
	// iteration the flow input, without iter.
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(
		loopOf("l", []string{mod("b", "echo", inputs("p", js("previous_result")), "")}, `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":false`, ""),
	)}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{"z":1}`, nil), conformance.RunSuccess, `[{"p":{"z":1}},{"p":{"z":1}}]`)
}

// CF-8 item 5, CF-27, JE-23, JE-39, WR-37: a collapsed body takes the
// loop's timeout; its transform error carries the collapsed prefix; its
// errors carry the loop's id as step_id; the loop fails with the array.
func TestLoopCollapsedFailures(t *testing.T) {
	body := func(m string) []string { return []string{m} }
	fields := `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":false`
	for _, c := range []struct {
		name, flow, entry string
	}{
		// waits honours the step's timeout, which loopSteps's scripts do
		// not see.
		{"loop timeout", flowOf(loopOf("l", body(mod("b", "echo", "", "")), fields, `"timeout":{"type":"static","value":0}`)),
			`{"error":{"message":"execution error:\njob process terminated due to timeout after exceeding job-specific duration limit","name":"ExecutionErr","step_id":"l"}}`},
		{"body error", flowOf(loopOf("l", body(mod("b", "fail", "", "")), fields, "")),
			`{"error":{"message":"boom","name":"Error","step_id":"l"}}`},
		{"transform error", flowOf(loopOf("l", body(mod("b", "echo", inputs("x", js("iter.value")), "")), fields, "")), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var exec StepExecutor = &loopSteps{}
			if c.name == "loop timeout" {
				exec = waits{}
			}
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, exec)
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, conformance.RunFailure, "")
			l := at(t, tr, "m:l")
			if l.Status != conformance.StepFailure || string(l.Result.Producer()) != string(tr.Result.Producer()) {
				t.Fatalf("the loop %+v, the flow's result %s", l, tr.Result)
			}
			noRecord(t, tr, "m:l/i:1/m:b")
			got := at(t, tr, "m:l/i:0/m:b").Result
			if c.entry != "" {
				sameJSON(t, "the failed iteration", got, c.entry)
				return
			}
			var v struct {
				Error struct{ Name, Message string }
			}
			if json.Unmarshal(got.Producer(), &v); v.Error.Name != "ExecutionErr" ||
				!strings.HasPrefix(v.Error.Message, "could not transform input using an expr: Error during isolated evaluation of expression `iter.value`:\nQuickJS evaluation error: Error: iter is not defined") {
				t.Fatalf("the transform error %s", got)
			}
		})
	}
}

// CF-5, CF-6: a failed iteration stops a sequential loop unless
// skip_failures; the loop then fails with the array so far, which is the
// flow's result, and the next module does not run. The body's skip_if
// keeps it from collapsing, so a skipped iteration's result is its child
// flow's input (FX-16, CF-9).
func TestLoopSkipFailures(t *testing.T) {
	flow := func(skip bool) string {
		return flowOf(
			loopOf("l", []string{mod("b", "fail", inputs("name", js(`flow_input.iter.value === 2 ? "Two" : "Error"`)), `"skip_if":{"expr":"flow_input.iter.value === 0"}`)},
				fmt.Sprintf(`"iterator":{"type":"static","value":[0,2,3]},"parallel":false,"skip_failures":%v`, skip), ""),
			mod("n", "value", inputs("v", static(`"after"`)), ""),
		)
	}
	e := mustOpen(t, map[string]string{"f/t/main": flow(false)}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	want := `[{"iter":{"index":0,"value":0}},{"error":{"message":"boom","name":"Two","step_id":"b"}}]`
	wantStatus(t, tr, conformance.RunFailure, want)
	noRecord(t, tr, "m:l/i:2/m:b")
	noRecord(t, tr, "m:n")

	e = mustOpen(t, map[string]string{"f/t/main": flow(true)}, &loopSteps{})
	tr = runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `"after"`)
	sameJSON(t, "the loop's result", at(t, tr, "m:l").Result, `[{"iter":{"index":0,"value":0}},{"error":{"message":"boom","name":"Two","step_id":"b"}},{"error":{"message":"boom","name":"Error","step_id":"b"}}]`)
}

// CF-11, CF-12, JE-26: a sequential loop's stop_after_if is a break after
// a successful iteration, with all_iters bound and error_message without
// effect; it is not evaluated after a failed one. A clean stop in the body
// breaks the loop; an error stop breaks it too, failing it unless
// skip_failures.
func TestLoopBreaks(t *testing.T) {
	iter := `"iterator":{"type":"static","value":[1,2,3,4]},"parallel":false`
	stopBody := func(envelope string) []string {
		return []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), envelope), mod("c", "value", inputs("v", js("previous_result * 10")), "")}
	}
	after := mod("n", "value", inputs("v", static(`"after"`)), "")
	for _, c := range []struct {
		name, loop, status, result, loopResult string
		notRun                                 []string
	}{
		{"loop break with all_iters", loopOf("l", []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), "")}, iter+`,"skip_failures":false`, `"stop_after_if":{"expr":"all_iters.length >= 2","error_message":"x","skip_if_stopped":true}`),
			"success", `"after"`, `[1,2]`, nil},
		{"no break after a failure", loopOf("l", []string{mod("b", "fail", "", `"skip_if":{"expr":"flow_input.iter.value > 1"}`)}, iter+`,"skip_failures":true`, `"stop_after_if":{"expr":"true","skip_if_stopped":false}`),
			"success", `"after"`, `[{"error":{"message":"boom","name":"Error","step_id":"b"}},{"iter":{"index":1,"value":2}}]`, nil},
		{"clean stop in the body", loopOf("l", stopBody(`"stop_after_if":{"expr":"result === 2","skip_if_stopped":false}`), iter+`,"skip_failures":false`, ""),
			"success", `"after"`, `[10,2]`, []string{"m:l/i:1/m:c"}},
		{"error stop, skip_failures false", loopOf("l", stopBody(`"stop_after_if":{"expr":"result === 2","error_message":"halt","skip_if_stopped":false}`), iter+`,"skip_failures":false`, ""),
			"failure", `[10,{"error":{"name":"EarlyStopError","message":"halt"}}]`, `[10,{"error":{"name":"EarlyStopError","message":"halt"}}]`, []string{"m:l/i:1/m:c", "m:n"}},
		{"error stop, skip_failures true", loopOf("l", stopBody(`"stop_after_if":{"expr":"result === 2","error_message":"halt","skip_if_stopped":false}`), iter+`,"skip_failures":true`, ""),
			"success", `"after"`, `[10,{"error":{"name":"EarlyStopError","message":"halt"}}]`, []string{"m:l/i:1/m:c"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(c.loop, after)}, &loopSteps{})
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, conformance.RunStatus(c.status), c.result)
			sameJSON(t, "the loop's result", at(t, tr, "m:l").Result, c.loopResult)
			for _, p := range c.notRun {
				noRecord(t, tr, p)
			}
			if len(c.notRun) > 0 && strings.Contains(c.notRun[0], "/m:c") {
				if len(tr.Children) != 2 || !slices.Equal(tr.Children[1].Modules, []string{"b", "c"}) {
					t.Fatalf("a stopped child still names its modules: %+v", tr.Children)
				}
			}
		})
	}
}

// CF-13, CF-29, CF-V2, JE-27: stop_after_all_iters_if stops the flow the
// loop is in, once, with the array; an evaluation error is an error stop.
// An empty loop evaluates its stop_after_if as an ordinary stop with [] and
// no all_iters, and never its stop_after_all_iters_if; with no job, its
// status is its module's, which an error stop fails (CF-29/a).
func TestLoopStopsOfTheFlow(t *testing.T) {
	iter := `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":false`
	empty := `"iterator":{"type":"static","value":[]},"parallel":false,"skip_failures":false`
	body := []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), "")}
	after := mod("n", "value", inputs("v", static(`"after"`)), "")
	for _, c := range []struct {
		name, loop, status, result string
		ran                        bool
	}{
		{"skipped", loopOf("l", body, iter, `"stop_after_all_iters_if":{"expr":"result.length === 2","skip_if_stopped":true}`), "skipped", `[1,2]`, true},
		{"clean", loopOf("l", body, iter, `"stop_after_all_iters_if":{"expr":"true","skip_if_stopped":false}`), "success", `[1,2]`, true},
		{"error message", loopOf("l", body, iter, `"stop_after_all_iters_if":{"expr":"true","error_message":"enough","error_include_result":true,"skip_if_stopped":false}`),
			"failure", `{"error":{"name":"EarlyStopError","message":"enough","result":[1,2]}}`, true},
		{"not truthy", loopOf("l", body, iter, `"stop_after_all_iters_if":{"expr":"false","skip_if_stopped":false}`), "success", `"after"`, true},
		{"empty, not evaluated", loopOf("l", body, empty, `"stop_after_all_iters_if":{"expr":"true","error_message":"e","skip_if_stopped":false}`), "success", `"after"`, false},
		{"empty stop_after_if", loopOf("l", body, empty, `"stop_after_if":{"expr":"result.length === 0","error_message":"e","error_include_result":true,"skip_if_stopped":false}`),
			"failure", `{"error":{"name":"EarlyStopError","message":"e","result":[]}}`, false},
		{"empty clean stop", loopOf("l", body, empty, `"stop_after_if":{"expr":"true","skip_if_stopped":true}`), "skipped", `[]`, false},
		{"empty body", loopOf("l", nil, `"iterator":{"type":"javascript","expr":"nope"},"parallel":false,"skip_failures":false`, ""), "success", `"after"`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(c.loop, after)}, &loopSteps{})
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, conformance.RunStatus(c.status), c.result)
			l := at(t, tr, "m:l")
			if l.Ran != c.ran {
				t.Fatalf("the loop %+v", l)
			}
			if failed := c.status == "failure"; !c.ran && (l.Status == conformance.StepFailure) != failed {
				t.Fatalf("the empty loop %+v", l)
			}
		})
	}

	// The evaluation error: the loop fails, its job result the array, and
	// carries the EarlyStopError, which the engine generated.
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", body, iter, `"stop_after_all_iters_if":{"expr":"bad!!!syntax","skip_if_stopped":false}`), after)}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "")
	l := at(t, tr, "m:l")
	var v struct {
		Error struct{ Name, Message string }
	}
	_ = json.Unmarshal(l.Carried.Producer(), &v)
	if l.Status != conformance.StepFailure || l.Module != conformance.ModuleFailure || string(l.Result.Producer()) != `[1,2]` || l.CarriedOrigin != OriginEngine ||
		v.Error.Name != "EarlyStopError" || !strings.HasPrefix(v.Error.Message, "Error evaluating stop_after_all_iters_if expression `bad!!!syntax`: Error: QuickJS evaluation error: Error: ") {
		t.Fatalf("the loop %+v", l)
	}
	// all_iters is not bound for an empty loop, so naming it throws (CF-29).
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", body, empty, `"stop_after_if":{"expr":"all_iters.length > 0","skip_if_stopped":false}`), after)}, &loopSteps{})
	tr = runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "")
	if !strings.Contains(string(tr.Result.Producer()), "all_iters is not defined") {
		t.Fatalf("the result %s", tr.Result)
	}
}

// CF-2, JE-30, CF-23, JE-35: the iterator is evaluated once with P and
// results; a value that is not an array, or a throw, is a chaining error:
// at the root's pickup a failure of the loop named Anyhow, later the
// flow's InternalError. The array check quotes serde's message.
func TestLoopIterator(t *testing.T) {
	first := mod("a", "value", inputs("v", static(`{"items":[1,2],"o":{"p":1}}`)), "")
	body := []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), "")}
	loop := func(iterator string) string {
		return loopOf("l", body, `"iterator":`+iterator+`,"parallel":false,"skip_failures":false`, "")
	}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(first, loop(js("results.a.items")))}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `[1,2]`)
	const args = `{"o":{"p":1}}`

	const prefix = "Expected an array value in the iterator expression, found: "
	for _, c := range []struct{ iterator, message string }{
		{js("flow_input.o"), prefix + "invalid type: map, expected a sequence at line 1 column 0"},
		{static(`5`), prefix + "invalid type: integer `5`, expected a sequence at line 1 column 1"},
		{static(`-5`), prefix + "invalid type: integer `-5`, expected a sequence at line 1 column 2"},
		{static(`1.5`), prefix + "invalid type: floating point `1.5`, expected a sequence at line 1 column 3"},
		{static(`"ab\"c"`), prefix + `invalid type: string "ab\"c", expected a sequence at line 1 column 7`},
		{static(`true`), prefix + "invalid type: boolean `true`, expected a sequence at line 1 column 4"},
		{static(`null`), prefix + "invalid type: null, expected a sequence at line 1 column 4"},
		{`{"type":"ai"}`, "AI input transform not supported for iterator"},
		// A replaced JSON.stringify returns text that is not JSON (JE-15).
		{js(`(JSON.stringify = () => "nope", [1])`), prefix + "expected ident at line 1 column 2"},
	} {
		e := mustOpen(t, map[string]string{"f/t/main": flowOf(first, loop(c.iterator))}, &loopSteps{})
		tr := runIt(t, e, args, nil)
		wantStatus(t, tr, conformance.RunFailure, `{"error":{"name":"InternalError","message":`+string(jsonString(c.message))+`}}`)
		if l := at(t, tr, "m:l"); l.Status != conformance.StepNotRun || tr.ResultOrigin != OriginEngine {
			t.Fatalf("%s: the loop %+v", c.iterator, l)
		}
		// At the root's pickup, the loop fails, with no job.
		e = mustOpen(t, map[string]string{"f/t/main": flowOf(loop(c.iterator))}, &loopSteps{})
		tr = runIt(t, e, args, nil)
		wantStatus(t, tr, conformance.RunFailure, `{"error":{"name":"Anyhow","message":`+string(jsonString("Error: "+c.message))+`}}`)
		if l := at(t, tr, "m:l"); l.Status != conformance.StepFailure || l.Ran || !l.Result.IsZero() || l.CarriedOrigin != OriginEngine {
			t.Fatalf("%s: the loop at the pickup %+v", c.iterator, l)
		}
	}
	// A throw is the evaluator's error in its generic form, wrapped once
	// more at the pickup.
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loop(js("nope")))}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	if name, msg := errorOf(tr.Result); name != "Anyhow" || !strings.HasPrefix(msg, "Error: Error: QuickJS evaluation error: Error: nope is not defined") {
		t.Fatalf("the result %s", tr.Result)
	}
}

// CF-3, JE-37: nested loops move the outer iter to iter_parent, and an
// iter already in the input moves up a level.
func TestLoopNestedIter(t *testing.T) {
	inner := loopOf("inner", []string{mod("c", "echo", inputs("i", js("flow_input.iter"), "p", js("flow_input.iter_parent"), "pp", js("flow_input.iter_parent_parent ?? null")), "")},
		`"iterator":{"type":"static","value":["x"]},"parallel":false,"skip_failures":false`, "")
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("outer", []string{inner}, `"iterator":{"type":"static","value":[{"b":1,"a":2}]},"parallel":false,"skip_failures":false`, ""))}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `[[{"i":{"index":0,"value":"x"},"p":{"index":0,"value":{"a":2,"b":1}},"pp":null}]]`)
	tr := runIt(t, e, `{"iter":"mine"}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `[[{"i":{"index":0,"value":"x"},"p":{"index":0,"value":{"a":2,"b":1}},"pp":"mine"}]]`)
}

// gated answers f/t/value like scripts, but holds every invocation in a
// loop body (or whose site holds hold) until the test releases it, and
// records the order they started in and how many ran at once.
type gated struct {
	mu       sync.Mutex
	started  []string
	release  map[string]chan struct{}
	running  int
	peak     int
	starting chan string
	// hold is the site part of the invocations it holds: iterations'
	// ("/i:") unless set.
	hold string
}

func newGated() *gated {
	return &gated{release: map[string]chan struct{}{}, starting: make(chan string, 64)}
}

func (g *gated) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	site := inv.Site.String()
	hold := g.hold
	if hold == "" {
		hold = "/i:"
	}
	if !strings.Contains(site, hold) {
		// Other steps are not held: they echo their arguments.
		defer inv.Report.Settled(SettleCompleted)
		return StepOutcome{Kind: OutcomeOK, Body: inv.Args}, nil
	}
	ch := make(chan struct{})
	g.mu.Lock()
	g.started = append(g.started, site)
	g.release[site] = ch
	g.running++
	g.peak = max(g.peak, g.running)
	g.mu.Unlock()
	g.starting <- site
	defer inv.Report.Settled(SettleCompleted)
	defer func() {
		g.mu.Lock()
		g.running--
		g.mu.Unlock()
	}()
	select {
	case <-ch:
	case <-ctx.Done():
		return StepOutcome{}, context.Cause(ctx)
	}
	var args map[string]json.RawMessage
	_ = json.Unmarshal(inv.Args.Producer(), &args)
	if string(args["v"]) == `"fail"` {
		return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"message":"boom","name":"Error","step_id":"` + inv.Env["WM_FLOW_STEP_ID"] + `"}`))}, nil
	}
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer(args["v"])}, nil
}

// next waits for the next invocation to start and returns its site.
func (g *gated) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-g.starting:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("no invocation started")
		return ""
	}
}

// none checks that no invocation starts for a while.
func (g *gated) none(t *testing.T) {
	t.Helper()
	select {
	case s := <-g.starting:
		t.Fatalf("%s started", s)
	case <-time.After(50 * time.Millisecond):
	}
}

func (g *gated) done(site string) {
	g.mu.Lock()
	ch := g.release[site]
	g.mu.Unlock()
	close(ch)
}

// runAsync runs f/t/main on a goroutine.
func runAsync(ctx context.Context, e *Engine, args string, hooks Hooks) <-chan *Trace {
	out := make(chan *Trace, 1)
	go func() {
		tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(args)), Hooks: hooks, Seed: 1})
		if err != nil {
			panic(err)
		}
		out <- tr
	}()
	return out
}

func parallelLoop(n int, parallelism string, skip bool, values ...string) string {
	iter := make([]string, n)
	for k := range iter {
		iter[k] = fmt.Sprint(k)
	}
	if len(values) > 0 {
		iter = values
	}
	fields := fmt.Sprintf(`"iterator":{"type":"static","value":[%s]},"parallel":true,"skip_failures":%v`, strings.Join(iter, ","), skip)
	if parallelism != "" {
		fields += `,"parallelism":` + parallelism
	}
	return loopOf("l", []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), "")}, fields, "")
}

// CF-4, CF-7: a parallel loop starts every iteration at once and collects
// their results in index order, whatever order they complete in; it never
// collapses, so each iteration is a child flow with its own innermost
// scope (WA-V1) and path.
func TestLoopParallelOrder(t *testing.T) {
	g := newGated()
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(parallelLoop(3, "", false, `"a"`, `"b"`, `"c"`))}, g)
	out := runAsync(context.Background(), e, `{}`, nil)
	seen := []string{g.next(t), g.next(t), g.next(t)}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"m:l/i:0/m:b", "m:l/i:1/m:b", "m:l/i:2/m:b"}) {
		t.Fatalf("started %v", seen)
	}
	for _, s := range []string{"m:l/i:2/m:b", "m:l/i:0/m:b", "m:l/i:1/m:b"} {
		g.done(s)
	}
	tr := <-out
	wantStatus(t, tr, conformance.RunSuccess, `["a","b","c"]`)
	if len(tr.Children) != 3 || g.peak != 3 {
		t.Fatalf("children %+v, peak %d", tr.Children, g.peak)
	}
}

// CF-7, JE-V1: parallelism N holds the iterations from N on; each
// completion releases the next held one, in index order.
func TestLoopParallelism(t *testing.T) {
	for _, c := range []struct {
		name, parallelism string
	}{
		{"static", `{"type":"static","value":2}`},
		{"bare number", `2`},
		{"expression", js("previous_result.cap")},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newGated()
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", inputs("cap", static("2")), ""), parallelLoop(4, c.parallelism, false))}, g)
			out := runAsync(context.Background(), e, `{}`, nil)
			first := []string{g.next(t), g.next(t)}
			slices.Sort(first)
			if !slices.Equal(first, []string{"m:l/i:0/m:b", "m:l/i:1/m:b"}) {
				t.Fatalf("first started %v", first)
			}
			g.none(t)
			g.done("m:l/i:1/m:b")
			if s := g.next(t); s != "m:l/i:2/m:b" {
				t.Fatalf("released %s", s)
			}
			g.done("m:l/i:0/m:b")
			if s := g.next(t); s != "m:l/i:3/m:b" {
				t.Fatalf("released %s", s)
			}
			g.done("m:l/i:3/m:b")
			g.done("m:l/i:2/m:b")
			wantStatus(t, <-out, conformance.RunSuccess, `[0,1,2,3]`)
			if g.peak != 2 {
				t.Fatalf("peak %d", g.peak)
			}
		})
	}

	// parallelism 1 runs the iterations one at a time, in index order.
	g := newGated()
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(parallelLoop(3, `1`, false))}, g)
	out := runAsync(context.Background(), e, `{}`, nil)
	for k := range 3 {
		s := g.next(t)
		if s != fmt.Sprintf("m:l/i:%d/m:b", k) {
			t.Fatalf("started %s", s)
		}
		g.none(t)
		g.done(s)
	}
	wantStatus(t, <-out, conformance.RunSuccess, `[0,1,2]`)

	// parallelism 0 holds every iteration, and only the run's end stops
	// the wait.
	g = newGated()
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(parallelLoop(2, `0`, false))}, g)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	tr := <-runAsync(ctx, e, `{}`, nil)
	if tr.Status != conformance.RunCanceled || len(g.started) != 0 {
		t.Fatalf("status %s, started %v", tr.Status, g.started)
	}
}

// CF-7, CF-12, CF-19, Q-18 (decisions): a parallel loop runs every
// iteration even after one fails (continue-all), then fails with the full
// array unless skip_failures; a clean stop ends only its iteration; each
// iteration is its own results scope, so its steps are null after the
// loop; its steps see the iteration's own job as their innermost scope.
func TestLoopParallelFailuresAndScopes(t *testing.T) {
	for _, skip := range []bool{false, true} {
		g := newGated()
		e := mustOpen(t, map[string]string{"f/t/main": flowOf(parallelLoop(3, `1`, skip, `"x"`, `"fail"`, `"z"`))}, g)
		out := runAsync(context.Background(), e, `{}`, nil)
		for range 3 {
			g.done(g.next(t))
		}
		want, st := `["x",{"error":{"message":"boom","name":"Error","step_id":"b"}},"z"]`, conformance.RunFailure
		if skip {
			st = conformance.RunSuccess
		}
		tr := <-out
		wantStatus(t, tr, st, want)
		if l := at(t, tr, "m:l"); (l.Status == conformance.StepSuccess) != skip {
			t.Fatalf("skip_failures %v: the loop %+v", skip, l)
		}
	}

	s := &loopSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		loopOf("l", []string{
			mod("h", "value", inputs("v", js("flow_input.iter.value")), `"stop_after_if":{"expr":"result === 2","skip_if_stopped":false}`),
			mod("i", "value", inputs("v", js("previous_result * 10")), ""),
		}, `"iterator":{"type":"static","value":[1,2,3]},"parallel":true,"skip_failures":false`, `"stop_after_if":{"expr":"true","skip_if_stopped":false}`),
		mod("n", "echo", inputs("i", js("results.i"), "l", js("results.l")), ""),
	)}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"i":null,"l":[10,2,30]}`)
	noRecord(t, tr, "m:l/i:1/m:i")
	h := s.inv(t, "m:l/i:2/m:h")
	if h.Env["WM_ROOT_FLOW_JOB_ID"] != h.Env["WM_FLOW_JOB_ID"] || h.Env["WM_FLOW_JOB_ID"] != tr.Children[2].ID || h.Env["WM_FLOW_PATH"] != "f/t/main/forloop-2" || h.Env["WM_ROOT_JOB_ID"] != tr.RunID {
		t.Fatalf("a parallel iteration's environment %v", h.Env)
	}
}

// JE-V1, CF-7, CF-23: parallelism that does not parse as a u16, or throws,
// and several children in a same_worker flow, fail the loop's start; a
// sequential loop never evaluates parallelism.
func TestLoopParallelStartErrors(t *testing.T) {
	first := mod("a", "value", inputs("v", static("1")), "")
	for _, c := range []struct{ parallelism, message string }{
		{js("1.5"), "Error parsing result as u16: invalid type: floating point `1.5`, expected u16 at line 1 column 3. Value was: 1.5"},
		{js("70000"), "Error parsing result as u16: invalid value: integer `70000`, expected u16 at line 1 column 5. Value was: 70000"},
		{js("-1"), "Error parsing result as u16: invalid value: integer `-1`, expected u16 at line 1 column 2. Value was: -1"},
		{js(`"2"`), `Error parsing result as u16: invalid type: string "2", expected u16 at line 1 column 3. Value was: "2"`},
		{js("null"), "Error parsing result as u16: invalid type: null, expected u16 at line 1 column 4. Value was: null"},
	} {
		e := mustOpen(t, map[string]string{"f/t/main": flowOf(first, parallelLoop(2, c.parallelism, false))}, &loopSteps{})
		wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunFailure, `{"error":{"name":"InternalError","message":`+string(jsonString(c.message))+`}}`)
	}
	// One JSON integer and white space only: serde_json checks a value's
	// type and range before what follows it (JE-V1).
	for _, c := range []struct{ parallelism, message string }{
		{js(`(JSON.stringify = () => "1x", 1)`), "Error parsing result as u16: trailing characters at line 1 column 2. Value was: 1x"},
		{js(`(JSON.stringify = () => "01", 1)`), "Error parsing result as u16: invalid number at line 1 column 2. Value was: 01"},
		{js(`(JSON.stringify = () => "70000x", 1)`), "Error parsing result as u16: invalid value: integer `70000`, expected u16 at line 1 column 5. Value was: 70000x"},
	} {
		s := &loopSteps{}
		e := mustOpen(t, map[string]string{"f/t/main": flowOf(first, parallelLoop(2, c.parallelism, false))}, s)
		wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunFailure, `{"error":{"name":"InternalError","message":`+string(jsonString(c.message))+`}}`)
		if len(s.invs) != 1 {
			t.Fatalf("%s: invocations %d", c.parallelism, len(s.invs))
		}
	}
	s := &loopSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(first, parallelLoop(2, js(`(JSON.stringify = () => " 2\n", 1)`), false))}, s)
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `[0,1]`)
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(first, parallelLoop(2, js("flow_input.iter.value"), false))}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	if name, msg := errorOf(tr.Result); name != "InternalError" || !strings.HasPrefix(msg, "Error during evaluation of expression `flow_input.iter.value`:\nQuickJS evaluation error: Error: cannot read property 'value' of undefined") {
		t.Fatalf("the result %s", tr.Result)
	}
	// A static null is no limit (reading, OF-V2).
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(parallelLoop(2, `{"type":"static"}`, false))}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `[0,1]`)
	// A sequential loop ignores its parallelism.
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), "")},
		`"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":false,"parallelism":`+js("nope"), ""))}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `[1,2]`)

	sameWorker := func(loop string) string {
		return `{"same_worker":true,"modules":[` + loop + `]}`
	}
	e = mustOpen(t, map[string]string{"f/t/main": sameWorker(parallelLoop(2, "", false))}, &loopSteps{})
	tr = runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, `{"error":{"name":"Anyhow","message":"Error: Bad request: Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker"}}`)
	if l := at(t, tr, "m:l"); l.Status != conformance.StepFailure || l.Ran || len(tr.Children) != 0 {
		t.Fatalf("the loop %+v, children %v", l, tr.Children)
	}
	e = mustOpen(t, map[string]string{"f/t/main": sameWorker(parallelLoop(1, "", false))}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `[0]`)
}

// CF-23, CF-V3, FX-15: a chaining error at a child flow's first module
// fails the child at its pickup with Anyhow, its module never started; one
// at a later module of the child is its InternalError. The loop sees a
// failed child.
func TestLoopChildChainingErrors(t *testing.T) {
	for _, c := range []struct{ name, body, notRun, entry string }{
		{"pickup", mod("b", "echo", "", `"skip_if":{"expr":"flow_input.iter.value.x.y"}`) + "," + mod("c", "echo", "", ""), "m:l/i:0/m:b",
			"Error: Error: QuickJS evaluation error: Error: cannot read property 'y' of undefined"},
		{"later", mod("b", "echo", "", "") + "," + mod("c", "echo", "", `"skip_if":{"expr":"flow_input.iter.value.x.y"}`), "m:l/i:0/m:c",
			"Error: QuickJS evaluation error: Error: cannot read property 'y' of undefined"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", []string{c.body}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""))}, &loopSteps{})
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, conformance.RunFailure, "")
			if rec := at(t, tr, c.notRun); rec.Status != conformance.StepNotRun {
				t.Fatalf("the module %+v", rec)
			}
			var entries []struct {
				Error struct{ Name, Message string }
			}
			_ = json.Unmarshal(tr.Result.Producer(), &entries)
			name := map[string]string{"pickup": "Anyhow", "later": "InternalError"}[c.name]
			if len(entries) != 1 || entries[0].Error.Name != name || !strings.HasPrefix(entries[0].Error.Message, c.entry) {
				t.Fatalf("the result %s", tr.Result)
			}
		})
	}
}

// ADR 0008: H6 observes the loop, each body module and each collapsed
// iteration; H7 latches a body's fatal error before skip_failures can take
// it; H8 halts after a body module; a latch or a cancellation starts no
// further iteration.
func TestLoopHooks(t *testing.T) {
	collapsed := loopOf("l", []string{mod("b", "fail", inputs("name", js(`flow_input.iter.value === 1 ? "Fatal" : "Error"`)), "")},
		`"iterator":{"type":"static","value":[0,1,2]},"parallel":false,"skip_failures":true`, "")
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(collapsed)}, &loopSteps{})
	h := &recordingHooks{}
	wantStatus(t, runIt(t, e, `{}`, h), conformance.RunSuccess, "")
	want := []string{"start m:l", "start m:l/i:0/m:b", "end m:l/i:0/m:b failure", "start m:l/i:1/m:b", "end m:l/i:1/m:b failure", "start m:l/i:2/m:b", "end m:l/i:2/m:b failure", "end m:l success"}
	if !slices.Equal(h.calls, want) {
		t.Fatalf("calls %q\nwant %q", h.calls, want)
	}
	tr := runIt(t, e, `{}`, &recordingHooks{fatal: "Fatal"})
	var fe *FatalError
	if tr.Status != conformance.RunLatched || !errors.As(tr.Latched, &fe) || fe.Site.String() != "m:l/i:1/m:b" {
		t.Fatalf("H7: status %s, latched %v", tr.Status, tr.Latched)
	}
	noRecord(t, tr, "m:l/i:2/m:b")

	s := &loopSteps{}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", []string{mod("b", "value", inputs("v", static("1")), ""), mod("c", "value", inputs("v", static("2")), "")},
		`"iterator":{"type":"static","value":[0,1]},"parallel":false,"skip_failures":false`, ""))}, s)
	tr = runIt(t, e, `{}`, &recordingHooks{haltAfter: "b"})
	if tr.Status != conformance.RunHalted || len(s.invs) != 1 || !slices.Equal(at(t, tr, "m:l/i:0/m:b").Hooks, []string{"H8"}) {
		t.Fatalf("haltAfter: status %s, invocations %d", tr.Status, len(s.invs))
	}

	// A latch in one parallel iteration starts no held iteration.
	g := newGated()
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(parallelLoop(3, `1`, false))}, g)
	out := runAsync(context.Background(), e, `{}`, latchAfter{})
	g.done(g.next(t))
	tr = <-out
	if tr.Status != conformance.RunLatched || len(g.started) != 1 {
		t.Fatalf("latched: status %s, started %v", tr.Status, g.started)
	}

	// A cancellation abandons the running iteration and starts no other.
	g = newGated()
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", []string{mod("b", "value", inputs("v", static("1")), "")}, `"iterator":{"type":"static","value":[0,1]},"parallel":false,"skip_failures":false`, ""))}, g)
	ctx, cancel := context.WithCancel(context.Background())
	out = runAsync(ctx, e, `{}`, nil)
	g.next(t)
	cancel()
	tr = <-out
	if tr.Status != conformance.RunCanceled || len(g.started) != 1 {
		t.Fatalf("canceled: status %s, started %v", tr.Status, g.started)
	}
}

// errorOf returns the name and message of a failure value's error.
func errorOf(v jsonval.Value) (string, string) {
	var e struct {
		Error struct{ Name, Message string }
	}
	_ = json.Unmarshal(v.Producer(), &e)
	return e.Error.Name, e.Error.Message
}

// haltAt halts the run before or after the modules at the given site
// paths (H8); decided, when set, is closed once haltBefore has matched.
type haltAt struct {
	NoHooks
	before, after string
	decided       chan struct{}
}

func (h haltAt) HaltBefore(s Site) bool {
	if s.Path.String() != h.before {
		return false
	}
	if h.decided != nil {
		close(h.decided)
	}
	return true
}
func (h haltAt) HaltAfter(s Site) bool { return s.Path.String() == h.after }

// ADR 0008 in a parallel loop: a halt in one iteration stops the others at
// their next module and wakes an iteration whose loop holds every
// iteration; the latch, then the cancellation, then a halt decide the
// run's status, whichever iteration met its signal first; a body's last
// module halts after it while iterations are still held.
func TestLoopParallelHostSignals(t *testing.T) {
	body := []string{mod("b", "value", inputs("v", static("1")), ""), mod("c", "value", inputs("v", static("2")), "")}
	loop := loopOf("l", body, `"iterator":{"type":"static","value":[0,1]},"parallel":true,"skip_failures":false`, "")

	// A halt before i:0's c stops i:1 before its c.
	g := newGated()
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(loop)}, g)
	decided := make(chan struct{})
	out := runAsync(context.Background(), e, `{}`, haltAt{before: "m:l/i:0/m:c", decided: decided})
	first := []string{g.next(t), g.next(t)}
	slices.Sort(first)
	if !slices.Equal(first, []string{"m:l/i:0/m:b", "m:l/i:1/m:b"}) {
		t.Fatalf("started %v", first)
	}
	g.done("m:l/i:0/m:b")
	<-decided
	// The engine records the halt right after haltBefore answers.
	time.Sleep(20 * time.Millisecond)
	g.done("m:l/i:1/m:b")
	tr := <-out
	if tr.Status != conformance.RunHalted || len(g.started) != 2 {
		t.Fatalf("halt before: status %s, started %v", tr.Status, g.started)
	}

	// The same halt, then the cancellation while i:1 runs: canceled.
	g = newGated()
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loop)}, g)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	decided = make(chan struct{})
	out = runAsync(ctx, e, `{}`, haltAt{before: "m:l/i:0/m:c", decided: decided})
	g.next(t)
	g.next(t)
	g.done("m:l/i:0/m:b")
	<-decided
	cancel()
	if tr = <-out; tr.Status != conformance.RunCanceled {
		t.Fatalf("halt then cancel: status %s", tr.Status)
	}

	// An inner loop that holds every iteration wakes when a sibling of its
	// iteration halts the run.
	inner := loopOf("inner", []string{mod("x", "value", inputs("v", static("0")), "")}, `"iterator":{"type":"static","value":[0]},"parallel":true,"skip_failures":false,"parallelism":0`, "")
	outer := loopOf("l", []string{mod("b", "value", inputs("v", static("1")), ""), inner}, `"iterator":{"type":"static","value":[0,1]},"parallel":true,"skip_failures":false`, "")
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(outer)}, &loopSteps{})
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: haltAt{before: "m:l/i:1/m:inner"}, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != conformance.RunHalted || time.Since(start) > 5*time.Second {
		t.Fatalf("a held loop: status %s after %s", tr.Status, time.Since(start))
	}

	// haltAfter at a body's last module halts while an iteration is held.
	s := &loopSteps{}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(parallelLoop(2, `1`, false))}, s)
	tr = runIt(t, e, `{}`, haltAt{after: "m:l/i:0/m:b"})
	if tr.Status != conformance.RunHalted || len(s.invs) != 1 {
		t.Fatalf("halt after: status %s, invocations %d", tr.Status, len(s.invs))
	}
	// With nothing held after it, the last iteration's halt changes
	// nothing: the run would start nothing more.
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(parallelLoop(2, `1`, false))}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, haltAt{after: "m:l/i:1/m:b"}), conformance.RunSuccess, `[0,1]`)
}

// CF-2, FX-48, FX-51: the evaluated iterator is stored, so an element
// jsonb refuses fails the loop's start, a chaining error, before any
// iteration runs, whatever the body reads.
func TestLoopIteratorStorage(t *testing.T) {
	for _, iterator := range []string{`[String.fromCharCode(0)]`, `["\ud800"]`} {
		for _, mode := range []string{"collapsed", "child", "parallel"} {
			t.Run(mode, func(t *testing.T) {
				body := []string{mod("b", "value", inputs("v", static("1")), "")}
				if mode == "child" {
					body = append(body, mod("c", "value", inputs("v", static("2")), ""))
				}
				fields := `"iterator":` + js(iterator) + `,"parallel":` + fmt.Sprint(mode == "parallel") + `,"skip_failures":true`
				for _, first := range []bool{true, false} {
					mods := []string{loopOf("l", body, fields, "")}
					if !first {
						mods = append([]string{mod("a", "value", inputs("v", static("0")), "")}, mods...)
					}
					s := &loopSteps{}
					e := mustOpen(t, map[string]string{"f/t/main": flowOf(mods...)}, s)
					tr := runIt(t, e, `{}`, nil)
					name, msg := errorOf(tr.Result)
					want, prefix := "InternalError", ""
					if first {
						want, prefix = "Anyhow", "Error: "
					}
					if tr.Status != conformance.RunFailure || name != want || len(s.invs) != len(mods)-1 || len(tr.Children) != 0 {
						t.Fatalf("%s, first %v: status %s, result %s, invocations %d", iterator, first, tr.Status, tr.Result, len(s.invs))
					}
					// The database's text; its detail is open (Q-FX-8).
					if !strings.HasPrefix(msg, prefix+"error returned from database: ") {
						t.Fatalf("%s: the message %q", iterator, msg)
					}
				}
			})
		}
	}
}

// limitsOf answers the parallelism expression "limit" with the next of
// its values, one per child, and every other expression as the engine
// does: parallelism is evaluated per child (JE-V1), and an expression may
// answer each differently.
type limitsOf struct {
	expr.Evaluator
	mu     sync.Mutex
	values []string
}

func (v *limitsOf) Eval(ctx context.Context, x *expr.Expr, s *expr.Scope) (jsonval.Value, error) {
	if x.Text != "limit" {
		return v.Evaluator.Eval(ctx, x, s)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	next := v.values[0]
	v.values = v.values[1:]
	return jsonval.FromProducer([]byte(next)), nil
}

// CF-7, JE-V1: each child is held or not by its own parallelism, and a
// held child waits only for its own count of completions: a child that is
// not held never waits behind one that is.
func TestLoopPerChildParallelism(t *testing.T) {
	for _, c := range []struct {
		limits, order []string
	}{
		{[]string{"0", "2"}, []string{"m:l/i:1/m:b", "m:l/i:0/m:b"}},
		{[]string{"1", "0", "3"}, []string{"m:l/i:0/m:b", "m:l/i:2/m:b", "m:l/i:1/m:b"}},
	} {
		s := &loopSteps{}
		e := mustOpen(t, map[string]string{"f/t/main": flowOf(parallelLoop(len(c.limits), js("limit"), false))}, s)
		e.eval = &limitsOf{Evaluator: e.eval, values: c.limits}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		want := make([]string, len(c.limits))
		for k := range want {
			want[k] = fmt.Sprint(k)
		}
		wantStatus(t, tr, conformance.RunSuccess, "["+strings.Join(want, ",")+"]")
		// The first runs alone when the others are held; with two not held
		// either order can start them, but the held one starts last.
		got := s.calls()
		if got[len(got)-1] != c.order[len(c.order)-1] || len(c.limits) == 2 && !slices.Equal(got, c.order) {
			t.Fatalf("limits %v: calls %v, want %v", c.limits, got, c.order)
		}
	}
}

// ADR 0008 H8 in nested loops: a body module's halt-after asks the
// containing loops whether the run would start more, an outer parallel
// loop's held iteration included; with nothing left to start it changes
// nothing.
func TestLoopNestedHaltAfter(t *testing.T) {
	innerOf := func(parallel bool) string {
		return loopOf("inner", []string{mod("b", "value", inputs("v", static("1")), "")}, fmt.Sprintf(`"iterator":{"type":"static","value":[0]},"parallel":%v,"skip_failures":false`, parallel), "")
	}
	outerOf := func(inner string, parallel bool) string {
		fields := `"iterator":{"type":"static","value":[0,1]},"parallel":false,"skip_failures":false`
		if parallel {
			fields = `"iterator":{"type":"static","value":[0,1]},"parallel":true,"parallelism":1,"skip_failures":false`
		}
		return loopOf("outer", []string{inner}, fields, "")
	}
	for _, c := range []struct {
		name               string
		outerPar, innerPar bool
		at                 string
		status             conformance.RunStatus
		invocations        int
	}{
		{"sequential in parallel", true, false, "m:outer/i:0/m:inner/i:0/m:b", conformance.RunHalted, 1},
		{"parallel in parallel", true, true, "m:outer/i:0/m:inner/i:0/m:b", conformance.RunHalted, 1},
		{"sequential in sequential", false, false, "m:outer/i:0/m:inner/i:0/m:b", conformance.RunHalted, 1},
		{"parallel in sequential", false, true, "m:outer/i:0/m:inner/i:0/m:b", conformance.RunHalted, 1},
		{"nothing left", true, false, "m:outer/i:1/m:inner/i:0/m:b", conformance.RunSuccess, 2},
		{"nothing left, sequential", false, true, "m:outer/i:1/m:inner/i:0/m:b", conformance.RunSuccess, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &loopSteps{}
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(outerOf(innerOf(c.innerPar), c.outerPar))}, s)
			tr := runIt(t, e, `{}`, haltAt{after: c.at})
			if tr.Status != c.status || len(s.invs) != c.invocations {
				t.Fatalf("status %s, invocations %d", tr.Status, len(s.invs))
			}
		})
	}
}

// ADR 0008 H8: a body module's halt-after applies only when the run would
// start more after that outcome: a further iteration after a success or a
// failure skip_failures tolerates, an iteration a parallel loop holds, or
// what follows the loop given how it ends. A failure that ends the loop
// and the run, or a stop that breaks the loop at the end of the flow,
// keeps its status (CF-5, CF-12).
func TestLoopHaltAfterOutcome(t *testing.T) {
	fail := func(envelope string) string { return mod("b", "fail", "", envelope) }
	stopAt := func(stop string) string {
		return mod("b", "value", inputs("v", js("flow_input.iter.value")), `"stop_after_if":{"expr":"result === 0",`+stop+`"skip_if_stopped":false}`)
	}
	after := mod("n", "value", inputs("v", static(`"after"`)), "")
	seq := func(skip bool) string {
		return fmt.Sprintf(`"iterator":{"type":"static","value":[0,1]},"parallel":false,"skip_failures":%v`, skip)
	}
	for _, c := range []struct {
		name        string
		flow        string
		status      conformance.RunStatus
		invocations int
	}{
		{"collapsed failure", flowOf(loopOf("l", []string{fail("")}, seq(false), "")), conformance.RunFailure, 1},
		{"child failure", flowOf(loopOf("l", []string{fail(`"skip_if":{"expr":"false"}`)}, seq(false), "")), conformance.RunFailure, 1},
		{"tolerated failure", flowOf(loopOf("l", []string{fail("")}, seq(true), "")), conformance.RunHalted, 1},
		{"loop's continue_on_error", flowOf(loopOf("l", []string{fail("")}, seq(false), `"continue_on_error":true`), after), conformance.RunHalted, 1},
		{"error stop, then a module", flowOf(loopOf("l", []string{stopAt(`"error_message":"e",`)}, seq(true), ""), after), conformance.RunHalted, 1},
		{"error stop ends the flow", flowOf(loopOf("l", []string{stopAt(`"error_message":"e",`)}, seq(true), "")), conformance.RunSuccess, 1},
		{"error stop fails the loop", flowOf(loopOf("l", []string{stopAt(`"error_message":"e",`)}, seq(false), ""), after), conformance.RunFailure, 1},
		{"clean stop, then a module", flowOf(loopOf("l", []string{stopAt("")}, seq(false), ""), after), conformance.RunHalted, 1},
		{"clean stop ends the flow", flowOf(loopOf("l", []string{stopAt("")}, seq(false), "")), conformance.RunSuccess, 1},
		{"parallel, held", flowOf(parallelLoopOf([]string{fail("")}, `1`, false)), conformance.RunHalted, 1},
		{"parallel, nothing held", flowOf(parallelLoopOf([]string{fail("")}, "", false)), conformance.RunFailure, 2},
		// Both iterations start at once, so the other may or may not have
		// launched its step when the halt lands: its count is not checked.
		{"parallel, tolerated", flowOf(parallelLoopOf([]string{fail("")}, "", true), after), conformance.RunHalted, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &loopSteps{}
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, s)
			tr := runIt(t, e, `{}`, haltAt{after: "m:l/i:0/m:b"})
			if tr.Status != c.status || c.invocations >= 0 && len(s.invs) != c.invocations {
				t.Fatalf("status %s, invocations %d, result %s", tr.Status, len(s.invs), tr.Result)
			}
		})
	}
}

// parallelLoopOf is a two-iteration parallel loop l with the given body.
func parallelLoopOf(body []string, parallelism string, skip bool) string {
	fields := fmt.Sprintf(`"iterator":{"type":"static","value":[0,1]},"parallel":true,"skip_failures":%v`, skip)
	if parallelism != "" {
		fields += `,"parallelism":` + parallelism
	}
	return loopOf("l", body, fields, "")
}

// ADR 0011, D14: the errors the engine generates inside a loop's array,
// and inside nested arrays, keep their provenance as pointers into the
// array, on the loop's record and on the run's result; a user's error
// there has none.
func TestLoopNestedProvenance(t *testing.T) {
	bad := loopOf("innermost", []string{mod("x", "value", inputs("v", static("1")), "")}, `"iterator":{"type":"static","value":{}},"parallel":false,"skip_failures":false`, "")
	middle := loopOf("inner", []string{bad, mod("y", "value", inputs("v", static("1")), "")}, `"iterator":{"type":"static","value":[0]},"parallel":false,"skip_failures":false`, "")
	outer := loopOf("outer", []string{middle, mod("z", "value", inputs("v", static("1")), "")}, `"iterator":{"type":"static","value":[0]},"parallel":true,"skip_failures":false`, "")
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(outer)}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "")
	if name, msg := errorOf(jsonval.FromProducer([]byte(strings.TrimSuffix(strings.TrimPrefix(string(tr.Result.Producer()), "[["), "]]")))); name != "Anyhow" || !strings.HasPrefix(msg, "Error: Expected an array value") {
		t.Fatalf("the result %s", tr.Result)
	}
	if !slices.Equal(tr.ResultNested, []string{"/0/0"}) || tr.ResultOrigin.Generated() {
		t.Fatalf("the run's result: nested %v, origin %q", tr.ResultNested, tr.ResultOrigin)
	}
	if l := at(t, tr, "m:outer"); !slices.Equal(l.ResultNested, []string{"/0/0"}) || !slices.Equal(l.CarriedNested, []string{"/0/0"}) {
		t.Fatalf("the outer loop %+v", l)
	}
	if l := at(t, tr, "m:outer/i:0/m:inner"); !slices.Equal(l.ResultNested, []string{"/0"}) {
		t.Fatalf("the inner loop %+v", l)
	}
	// An error stop that includes the array keeps its generated entries'
	// provenance below /error/result; a user's error beside them has none.
	inner := loopOf("inner", []string{mod("x", "fail", inputs("name", static(`"Anyhow"`)), `"skip_if":{"expr":"false"}`)},
		`"iterator":{"type":"javascript","expr":"flow_input.iter.value === 0 ? ({}) : [0]"},"parallel":false,"skip_failures":false`, "")
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(
		loopOf("l", []string{inner}, `"iterator":{"type":"static","value":[0,1]},"parallel":false,"skip_failures":true`,
			`"stop_after_all_iters_if":{"expr":"true","error_message":"stop","error_include_result":true,"skip_if_stopped":false}`),
	)}, &loopSteps{})
	tr = runIt(t, e, `{}`, nil)
	if l := at(t, tr, "m:l"); !slices.Equal(tr.ResultNested, []string{"/error/result/0"}) || !slices.Equal(l.CarriedNested, []string{"/error/result/0"}) || !slices.Equal(l.ResultNested, []string{"/0"}) {
		t.Fatalf("the included array: run %v, loop %+v, result %s", tr.ResultNested, l, tr.Result)
	}

	// A user's error, and an error stop's EarlyStopError over the array,
	// which replaces the carried value as a whole.
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(
		loopOf("l", []string{mod("b", "fail", inputs("name", static(`"Anyhow"`)), "")}, `"iterator":{"type":"static","value":[0]},"parallel":true,"skip_failures":true`, `"stop_after_all_iters_if":{"expr":"true","error_message":"e","skip_if_stopped":false}`),
	)}, &loopSteps{})
	tr = runIt(t, e, `{}`, nil)
	if l := at(t, tr, "m:l"); len(l.ResultNested) != 0 || len(l.CarriedNested) != 0 || len(tr.ResultNested) != 0 {
		t.Fatalf("a user's error: %+v, the run's %v", l, tr.ResultNested)
	}
}

// latchAfter latches the run once a module ends.
type latchAfter struct{ NoHooks }

func (latchAfter) OnModuleEnd(_ context.Context, r Run, s Site, _ StepRecord) error {
	if s.Module == "b" {
		r.Latch(errors.New("stop"))
	}
	return nil
}

// A parallel loop's iterations share the engine with concurrent runs
// (run with -race).
func TestLoopConcurrentRuns(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		parallelLoop(8, `3`, false),
		loopOf("m", []string{mod("c", "value", inputs("v", js("flow_input.iter.value * 2")), "")}, `"iterator":{"type":"javascript","expr":"results.l"},"parallel":false,"skip_failures":false`, ""),
	)}, &loopSteps{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))})
			if err != nil || tr.Status != conformance.RunSuccess {
				t.Errorf("run: %v %+v", err, tr)
			}
		}()
	}
	wg.Wait()
}

// The texts serde_json writes (CF-2, JE-V1, FX-42 rule 2, JE-44).
func TestSerdeTexts(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`: "invalid type: map, expected a sequence at line 1 column 0",
		` [1]`:    "invalid type: sequence, expected a sequence at line 1 column 1",
		"\n12":    "invalid type: integer `12`, expected a sequence at line 2 column 2",
		`1e3`:     "invalid type: floating point `1000.0`, expected a sequence at line 1 column 3",
		`"é"`:     `invalid type: string "é", expected a sequence at line 1 column 4`,
		`false`:   "invalid type: boolean `false`, expected a sequence at line 1 column 5",
		// Text that is not JSON, which a replaced JSON.stringify returns.
		`hello`: "expected value at line 1 column 1",
		`nope`:  "expected ident at line 1 column 2",
		`tru`:   "EOF while parsing a value at line 1 column 3",
		` `:     "EOF while parsing a value at line 1 column 1",
	} {
		if got := serdeError([]byte(in), "a sequence"); got != want {
			t.Errorf("serdeError(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]int{`0`: 0, `65535`: 65535, ` 2`: 2} {
		if n, msg := parseU16([]byte(in)); n != want || msg != "" {
			t.Errorf("parseU16(%q) = %d, %q", in, n, msg)
		}
	}
	if _, msg := parseU16([]byte(`65536`)); msg != "invalid value: integer `65536`, expected u16 at line 1 column 5" {
		t.Errorf("65536: %q", msg)
	}
	for in, want := range map[string]string{
		`[2]`:   "invalid type: sequence, expected u16 at line 1 column 0",
		`1x`:    "trailing characters at line 1 column 2",
		`1 x`:   "trailing characters at line 1 column 3",
		`01`:    "invalid number at line 1 column 2",
		`-01`:   "invalid number at line 1 column 3",
		`1.5x`:  "invalid type: floating point `1.5`, expected u16 at line 1 column 3",
		`-1 x`:  "invalid value: integer `-1`, expected u16 at line 1 column 2",
		`true1`: "invalid type: boolean `true`, expected u16 at line 1 column 4",
	} {
		if _, msg := parseU16([]byte(in)); msg != want {
			t.Errorf("parseU16(%q) = %q, want %q", in, msg, want)
		}
	}
	if n, msg := parseU16([]byte(" 2 \n")); n != 2 || msg != "" {
		t.Errorf("white space around a u16: %d, %q", n, msg)
	}
	for in, want := range map[string]string{
		`[1]x`:  "trailing characters at line 1 column 4",
		`[1] `:  "",
		`01`:    "invalid number at line 1 column 2",
		`{} []`: "invalid type: map, expected a sequence at line 1 column 0",
	} {
		if want == "" {
			if _, ok := elements([]byte(in)); !ok {
				t.Errorf("elements(%q) refused", in)
			}
			continue
		}
		if got := serdeSequenceError([]byte(in)); got != want {
			t.Errorf("serdeSequenceError(%q) = %q, want %q", in, got, want)
		}
	}
	for f, want := range map[float64]string{
		1.5: "1.5", 1000: "1000.0", 1e15: "1000000000000000.0", 1e16: "1e+16", 1e21: "1e+21", 123456789012345678901234567890: "1.2345678901234568e+29",
		0.0001: "0.0001", 1e-5: "0.00001", 1e-6: "1e-6", 1.5e-7: "1.5e-7", -2.25: "-2.25", 0.1: "0.1",
	} {
		if got := rustFloat(f); got != want {
			t.Errorf("rustFloat(%v) = %q, want %q", f, got, want)
		}
	}
	if got := string(reprintNumbers([]byte(`{"a":1.50,"b":[2.0,-3,1e2,"1.50",9223372036854775807,18446744073709551615,18446744073709551616]}`))); got != `{"a":1.5,"b":[2.0,-3,100.0,"1.50",9223372036854775807,18446744073709551615,1.8446744073709552e+19]}` {
		t.Errorf("reprintNumbers: %s", got)
	}
	// A loop's result is the children's stored results, compact, with
	// their numbers re-printed (JE-44).
	if got := string(arrayOf([]jsonval.Value{jsonval.FromProducer([]byte(`{"b": 1.50, "a": 1}`)), {}, jsonval.FromProducer([]byte(`"x"`))}).Producer()); got != `[{"a":1,"b":1.5},null,"x"]` {
		t.Errorf("arrayOf: %s", got)
	}
	// CF-3: iter moves up a level, and so does every iter_parent above it.
	in := jsonval.FromProducer([]byte(`{"iter":1,"iter_parent":2,"iter_parent_parent":3,"z":0}`))
	if got := string(iterInput(in, 4, []byte(`"v"`)).Producer()); got != `{"iter_parent":1,"iter_parent_parent":2,"iter_parent_parent_parent":3,"z":0,"iter":{"index":4,"value":"v"}}` {
		t.Errorf("iterInput: %s", got)
	}
	if got := string(iterInput(jsonval.FromProducer([]byte(`{"iter_parent":2}`)), 0, []byte(`1`)).Producer()); got != `{"iter_parent":2,"iter":{"index":0,"value":1}}` {
		t.Errorf("iterInput with no iter: %s", got)
	}
}

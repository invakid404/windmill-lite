package engine

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// The tests of milestone NI: N2's waits and retries inside containers, and
// the evaluation timing of container-level fields (CF-21 to CF-28), on the
// synthetic folders of engine_test.go and the real QuickJS-ng engine; the
// fixtures conformance/flows/control-flow/cf-21 to cf-28 and waits run the
// same flows on the real Bun harness against the oracle's recordings
// (cmd/wltest, TestFlowSuite).

// CF-21, FX-23: the sleep of the module before a container delays the
// container's first child only: one wait, whatever the container.
func TestContainerSleepBefore(t *testing.T) {
	one := []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), "")}
	two := []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), ""), mod("c", "value", inputs("v", js("results.b")), "")}
	for _, c := range []struct {
		name, container, result string
	}{
		{"sequential loop", loopOf("l", two, `"iterator":{"type":"static","value":[1,2]},"skip_failures":false`, ""), `[1,2]`},
		{"collapsed loop", loopOf("l", one, `"iterator":{"type":"static","value":[1,2]},"skip_failures":false`, ""), `[1,2]`},
		{"while loop", whileOf("l", []string{mod("b", "value", inputs("v", js("flow_input.iter.index")), "")}, false, breakAt("result == 1")), `[0,1]`},
		{"branchone", branchOneOf("l", nil, []string{value("b", "1", "")}, ""), `1`},
		{"sequential branchall", branchAllOf("l", []string{branch(skipFailure(false), value("b", "1", "")), branch(skipFailure(false), value("c", "2", ""))}, false, ""), `[1,2]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", inputs("v", static("0")), `"sleep":`+static("3")), c.container)}, &scripts{})
			clock := &RecordClock{}
			tr := runReq(t, e, RunRequest{Clock: clock})
			wantStatus(t, tr, conformance.RunSuccess, c.result)
			waitsNear(t, clock, 3*time.Second)
		})
	}
}

// CF-21, CF-27, FX-15: a container's timeout is evaluated for each child it
// pushes, with the container's own scope, and nothing enforces it on a
// child flow; its failure is a chaining error of the container's flow,
// routed by where it is raised.
func TestContainerTimeoutPerChild(t *testing.T) {
	two := []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), ""), mod("c", "value", inputs("v", js("results.b")), "")}
	// The second child's push reads one iteration in results.l, whose
	// timeout is then negative: a later transition's chaining error.
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", two, `"iterator":{"type":"static","value":[1,2]},"skip_failures":false`, `"timeout":`+js("results.l.length > 0 ? -1 : 0")))}, &scripts{})
	s := e.exec.(*scripts)
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, `{"error":{"message":"Timeout value cannot be negative","name":"InternalError"}}`)
	wantCalls(t, s, "m:l/i:0/m:b", "m:l/i:0/m:c")

	for _, c := range []struct {
		name, flow, result string
	}{
		{"branchone at the root's pickup", flowOf(branchOneOf("l", nil, []string{value("b", "1", "")}, `"timeout":`+static("-1"))),
			`{"error":{"message":"Error: Timeout value cannot be negative","name":"Anyhow"}}`},
		{"parallel branchall later", flowOf(mod("a", "value", "", ""), branchAllOf("l", []string{branch(skipFailure(false), value("b", "1", ""))}, true, `"timeout":`+js("-1"))),
			`{"error":{"message":"Timeout value cannot be negative","name":"InternalError"}}`},
		// The timeout is evaluated before the parallelism.
		{"parallel loop later", flowOf(mod("a", "value", "", ""), loopOf("l", two, `"iterator":{"type":"static","value":[1]},"parallel":true,"parallelism":`+js("never")+`,"skip_failures":false`, `"timeout":`+js("nope"))),
			"{\"error\":{\"message\":\"Error during evaluation of expression `nope`:\\nQuickJS evaluation error: Error: nope is not defined\\n    at <anonymous> (eval_script:1:1)\\n    at <eval> (eval_script:1:33)\\n\",\"name\":\"InternalError\"}}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, &scripts{})
			tr := runReq(t, e, RunRequest{})
			wantStatus(t, tr, conformance.RunFailure, c.result)
			if len(e.exec.(*scripts).calls()) > 1 {
				t.Fatalf("calls %v", e.exec.(*scripts).calls())
			}
		})
	}

	// A zero timeout times nothing in a child flow (CF-27 rule 3).
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", two, `"iterator":{"type":"static","value":[1]},"skip_failures":false`, `"timeout":`+static("0")))}, &scripts{})
	wantStatus(t, runReq(t, e, RunRequest{}), conformance.RunSuccess, `[1]`)
}

// CF-22: a failed container with a retry runs again from scratch after the
// backoff: its iterator again and every child; the failed attempt is
// recorded at the container's r:0, and the flow's retry state counts it.
// A retried loop's children start from the last failed child's stored
// input, so a child flow sees the failed child's iter as iter_parent.
func TestContainerRetry(t *testing.T) {
	body := []string{
		mod("b", "flaky", inputs("p", js("flow_input.iter_parent ?? null")), ""),
		mod("c", "value", inputs("v", js("results.b.p")), ""),
	}
	f := &flaky{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", body, `"iterator":{"type":"static","value":[1]},"skip_failures":false`, retryOnce))}, f)
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `[{"index":0,"value":1}]`)
	wantCalls(t, &f.scripts, "m:l/i:0/m:b", "m:l/i:0/m:b", "m:l/i:0/m:c")
	if l := record(t, tr, site("l")); !slices.Equal(l.FailedRetries, []conformance.SitePath{attemptSite("l", 0)}) || l.Attempt != 1 {
		t.Fatalf("l %+v", l)
	}
	if r := record(t, tr, attemptSite("l", 0)); r.Status != conformance.StepFailure || r.Module != "" {
		t.Fatalf("l/r:0 %+v", r)
	}

	// A branchall runs every branch again.
	f = &flaky{}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(branchAllOf("l", []string{branch(skipFailure(false), mod("b", "flaky", inputs("x", static("0")), "")), branch(skipFailure(false), value("c", "1", ""))}, true, retryOnce))}, f)
	tr = runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `[{"x":0},1]`)
	if got := f.calls(); len(got) != 4 {
		t.Fatalf("calls %v", got)
	}

	// The retry's attempts are spent: the loop fails with its second
	// attempt's array.
	s := &scripts{}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", []string{mod("b", "fail", "", ""), mod("c", "value", "", "")}, `"iterator":{"type":"static","value":[1]},"skip_failures":false`, retryOnce))}, s)
	tr = runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, `[{"error":{"message":"boom","name":"Error","step_id":"b"}}]`)
	wantCalls(t, s, "m:l/i:0/m:b", "m:l/i:0/m:b")
}

// CF-21, Q-CF-6: a sequential loop's skip_if is evaluated again before
// every iteration after the first, with the flow's last result as
// previous_result: when it holds, the loop ends as a skipped identity job
// of that result, its array lost. A parallel loop evaluates it once.
func TestLoopSkipIfMidLoop(t *testing.T) {
	one := []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), "")}
	two := append(slices.Clone(one), mod("c", "value", inputs("v", js("results.b")), ""))
	skip := `"skip_if":` + exprOf("previous_result == 2")
	for _, c := range []struct {
		name, loop string
		calls      int
		result     string
	}{
		{"collapsed", loopOf("l", one, `"iterator":{"type":"static","value":[1,2,3]},"skip_failures":false`, skip), 2, "2"},
		{"child flows", loopOf("l", two, `"iterator":{"type":"static","value":[1,2,3]},"skip_failures":false`, skip), 4, "2"},
		{"while loop", whileOf("l", []string{mod("b", "value", inputs("v", js("flow_input.iter.index + 1")), "")}, false, skip+","+breakAt("result >= 5")), 2, "2"},
		{"parallel", loopOf("l", one, `"iterator":{"type":"static","value":[1,2,3]},"parallel":true,"skip_failures":false`, `"skip_if":`+exprOf("previous_result != 0")), 3, "[1,2,3]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", inputs("v", static("0")), ""), c.loop)}, s)
			tr := runReq(t, e, RunRequest{})
			wantStatus(t, tr, conformance.RunSuccess, c.result)
			if got := len(s.calls()) - 1; got != c.calls {
				t.Fatalf("calls %v", s.calls())
			}
			l := record(t, tr, site("l"))
			if c.name != "parallel" {
				if !l.Skipped || l.Status != conformance.StepSuccess {
					t.Fatalf("l %+v", l)
				}
				sameJSON(t, "l's arguments", l.RecordedArgs, `{"previous_result":2}`)
			}
		})
	}

	// A throw before a later iteration is a chaining error of a later
	// transition.
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(value("a", `{"x":{"y":false}}`, ""), loopOf("l", one, `"iterator":{"type":"static","value":[1,2]},"skip_failures":false`, `"skip_if":`+exprOf("previous_result.x.y")))}, &scripts{})
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, "")
	if errorName(tr.Result) != "InternalError" {
		t.Fatalf("result %s", tr.Result)
	}
}

// CF-25: on the Community Edition a squashed loop runs as if it were not
// squashed, sequential or parallel, a for loop or a while loop, collapsed
// or not (recorded by the cf-25 fixture, where the catalogue predicted an
// error).
func TestContainerSquash(t *testing.T) {
	one := []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), "")}
	two := append(slices.Clone(one), mod("c", "value", inputs("v", js("results.b")), ""))
	sq := `"squash":true,"skip_failures":false`
	for _, c := range []struct {
		name, loop, result string
	}{
		{"at the root's pickup", loopOf("l", two, `"iterator":{"type":"static","value":[1,2]},`+sq, ""), `[1,2]`},
		{"collapsed", loopOf("l", one, `"iterator":{"type":"static","value":[1,2]},`+sq, ""), `[1,2]`},
		{"while loop", `{"id":"l","value":{"type":"whileloopflow","squash":true,"skip_failures":false,"modules":[` + strings.Join(two, ",") + `]},` + breakAt("result == 1") + `}`, `[0,1]`},
		{"empty", loopOf("l", two, `"iterator":{"type":"static","value":[]},`+sq, ""), `[]`},
		{"parallel", loopOf("l", two, `"iterator":{"type":"static","value":[1]},"parallel":true,`+sq, ""), `[1]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(c.loop)}, &scripts{})
			wantStatus(t, runReq(t, e, RunRequest{}), conformance.RunSuccess, c.result)
		})
	}
}

// FX-23, FX-26, FX-27, OF-17, CF-26: N2's waits and retries inside a
// container's child flows: each flow keeps its own retry state, so a
// parallel loop's iterations retry independently; a sleep between body
// modules waits; a body whose last module sleeps ends in the virtual
// identity, which the trace marks so that observations leave it out.
func TestContainerBodyWaits(t *testing.T) {
	f := &flaky{}
	body := []string{mod("b", "flaky", inputs("x", js("flow_input.iter.value")), retryOnce), mod("c", "value", inputs("v", js("results.b.x")), "")}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", body, `"iterator":{"type":"static","value":[1,2]},"parallel":true,"skip_failures":false`, ""))}, f)
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `[1,2]`)
	for _, k := range []string{"0", "1"} {
		b := at(t, tr, "m:l/i:"+k+"/m:b")
		r := at(t, tr, "m:l/i:"+k+"/m:b/r:0")
		if b.Status != conformance.StepSuccess || len(b.FailedRetries) != 1 || b.FailedRetries[0] != r.Site || r.Status != conformance.StepFailure {
			t.Fatalf("iteration %s: b %+v, r:0 %+v", k, b, r)
		}
	}

	clock := &RecordClock{}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", []string{mod("b", "value", inputs("v", static("1")), `"sleep":`+static("2")), mod("c", "value", inputs("v", static("2")), "")}, `"iterator":{"type":"static","value":[1]},"skip_failures":false`, ""))}, &scripts{})
	wantStatus(t, runReq(t, e, RunRequest{Clock: clock}), conformance.RunSuccess, `[2]`)
	waitsNear(t, clock, 2*time.Second)

	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", []string{mod("b", "value", inputs("v", js("flow_input.iter.value * 10")), `"sleep":`+static("0"))}, `"iterator":{"type":"static","value":[1,2]},"skip_failures":false`, ""))}, &scripts{})
	tr = runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `[10,20]`)
	for _, c := range tr.Children {
		if !slices.Equal(c.Modules, []string{"b", "b-v"}) || !slices.Equal(c.Virtual, []bool{false, true}) {
			t.Fatalf("child %+v", c)
		}
	}
}

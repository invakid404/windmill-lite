package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The tests of sleeps, timeouts and retries inside containers, and the
// evaluation timing of container-level fields (CF-21 to CF-28), on the
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

	// The delayed child is the child flow itself, picked up once the sleep
	// has passed: a first module that completes without a job, an empty
	// loop, does not use the delay up (FX-23).
	empty := loopOf("empty", []string{value("unused", "1", "")}, `"iterator":{"type":"static","value":[]},"skip_failures":false`, "")
	body := []string{empty, value("b", "7", "")}
	for _, container := range []string{
		branchOneOf("l", nil, body, ""),
		loopOf("l", body, `"iterator":{"type":"static","value":[1]},"skip_failures":false`, ""),
	} {
		e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", inputs("v", static("0")), `"sleep":`+static("3")), container)}, &scripts{})
		clock := &RecordClock{}
		wantStatus(t, runReq(t, e, RunRequest{Clock: clock}), conformance.RunSuccess, "")
		waitsNear(t, clock, 3*time.Second)
	}

	// A cancellation while the child is still scheduled fails it without
	// running its body (CF-28 rule 2): the loop fails with its Canceled
	// error and the run ends canceled.
	s := &scripts{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", inputs("v", static("0")), `"sleep":`+static("30")), loopOf("l", two, `"iterator":{"type":"static","value":[1]},"skip_failures":false`, ""))}, s)
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(100*time.Millisecond, func() { cancel(&Cancellation{Reason: "r", Canceler: "u"}) })
	tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, tr, conformance.RunCanceled, "")
	wantCalls(t, s, "m:a")
	if l := record(t, tr, site("l")); l.Status != conformance.StepFailure {
		t.Fatalf("l %+v", l)
	} else {
		sameJSON(t, "the loop's array", l.Result, `[{"error":{"message":"Job canceled: r by u","name":"Canceled","reason":"r","canceler":"u"}}]`)
	}
}

// JE-38, CF-21: each child a sequential container pushes after the previous
// one completed is pushed by a transition of the container's flow, which
// resolves flow_env again, so its skip_if and its timeout see what the
// previous child changed, for collapsed jobs and child flows alike, and
// for a sequential branchall's timeout.
func TestContainerEnvPerTransition(t *testing.T) {
	for _, mode := range []string{"skip collapsed", "skip flow", "timeout collapsed", "timeout flow", "timeout branchall"} {
		t.Run(mode, func(t *testing.T) {
			store := platform.NewStore()
			store.SetVariable("u/t/flag", platform.Variable{Value: "0"})
			s := &scripts{}
			// Every step sets the variable once it has run.
			exec := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
				out, err := s.Execute(ctx, inv)
				store.SetVariable("u/t/flag", platform.Variable{Value: "1"})
				return out, err
			})
			body := []string{mod("b", "value", inputs("v", js("flow_input.iter.value")), "")}
			if strings.HasSuffix(mode, "flow") {
				body = append(body, mod("c", "value", inputs("v", js("results.b")), ""))
			}
			envelope := `"skip_if":` + exprOf("flow_env.flag === '1'")
			if strings.HasPrefix(mode, "timeout") {
				envelope = `"timeout":` + js("flow_env.flag === '1' ? -1 : 10")
			}
			container := loopOf("l", body, `"iterator":{"type":"static","value":[1,2,3]},"skip_failures":false`, envelope)
			if mode == "timeout branchall" {
				container = branchAllOf("l", []string{branch(skipFailure(false), value("b", "1", "")), branch(skipFailure(false), value("c", "2", ""))}, false, envelope)
			}
			e := openWithPlatform(t, map[string]string{"f/t/main": `{"modules":[` + container + `],"flow_env":{"flag":"$var:u/t/flag"}}`}, exec, &platform.Services{Variables: store, Resources: store})
			tr := runReq(t, e, RunRequest{})
			if strings.HasPrefix(mode, "timeout") {
				wantStatus(t, tr, conformance.RunFailure, `{"error":{"message":"Timeout value cannot be negative","name":"InternalError"}}`)
			} else if l := record(t, tr, site("l")); !l.Skipped {
				t.Fatalf("l %+v", l)
			}
			if n := len(s.calls()); n != len(body) {
				t.Fatalf("calls %v", s.calls())
			}
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

	// A retry's re-entry is a later transition (FX-15): an iterator that
	// throws at the root's pickup fails the loop, which is retried, and the
	// same throw at the re-entry fails the flow at once, the loop not run.
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", body, `"iterator":`+js("flow_input.x.y")+`,"skip_failures":false`, `"retry":{"constant":{"attempts":2,"seconds":0}}`))}, &scripts{})
	tr = runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, "")
	if errorName(tr.Result) != "InternalError" {
		t.Fatalf("result %s", tr.Result)
	}
	if l := record(t, tr, site("l")); l.Status != conformance.StepNotRun || len(l.FailedRetries) != 0 {
		t.Fatalf("l %+v", l)
	}
	record(t, tr, attemptSite("l", 0))
	if _, ok := tr.Step(attemptSite("l", 1)); ok {
		t.Fatal("a second retry")
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

// squash is accepted and ignored ([CF-25]). windmill-lite runs a squashed
// loop unsquashed, sequential or parallel, a for loop or a while loop,
// collapsed or not, which gives the values the Community Edition image
// recorded (the cf-25 fixture). The runtime that image keeps for each body
// step across a squashed loop's iterations, with WM_JOB_ID reading
// NOT_AVAILABLE, is not reproduced; the engine package's README gives why.
//
// [CF-25]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/control-flow.md#cf-25
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

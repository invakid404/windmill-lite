package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The subflow tests (milestone N3c) run synthetic flows on the real
// QuickJS-ng engine, with steps answered in Go; the E3a, E3c and N3c
// fixtures of conformance/flows run the same subflows on the real Bun
// harness (cmd/wltest, TestFlowSuite).

// subflowOf writes a flow step that runs the flow at path, with input
// transforms and envelope fields.
func subflowOf(id, path, inputs, envelope string) string {
	if inputs == "" {
		inputs = "{}"
	}
	if envelope != "" {
		envelope = "," + envelope
	}
	return fmt.Sprintf(`{"id":%q,"value":{"type":"flow","path":%q,"input_transforms":%s}%s}`, id, path, inputs, envelope)
}

// withEnv adds a flow_env to a flow value.
func withEnv(flow, env string) string {
	return strings.TrimSuffix(flow, "}") + `,"flow_env":` + env + `}`
}

// CF-17, CF-19, CF-9, JE-37, WA-6, WA-V1, CF-24: a flow step runs its
// subflow as a child flow whose input is the step's evaluated arguments,
// its first step's previous_result; its result is the step's. Its steps
// write into a results scope of their own, so the caller reads null for
// them, and see the subflow's job id, path and innermost scope; an inline
// step's job path is under the subflow's path. The trace lists the child
// flow at the step's site.
func TestSubflowRuns(t *testing.T) {
	s := &loopSteps{}
	e := mustOpen(t, map[string]string{
		"f/t/child": flowOf(
			mod("s", "echo", inputs("v", js("flow_input.v"), "p", js("previous_result")), ""),
			inlineOf("i", inputs("v", js("results.s.v"))),
		),
		"f/t/main": flowOf(
			mod("a", "value", inputs("v", static("4")), ""),
			subflowOf("c", "f/t/child", inputs("v", js("flow_input.n")), ""),
			mod("d", "echo", inputs("p", js("previous_result"), "s", js(`results.s ?? "hidden"`), "c", js("results.c")), ""),
		),
	}, s)
	tr := runIt(t, e, `{"n":4}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"p":{"v":4},"s":"hidden","c":{"v":4}}`)
	c := at(t, tr, "m:c")
	if !c.Ran || c.Status != conformance.StepSuccess || c.Module != conformance.ModuleSuccess || c.Job == "" {
		t.Fatalf("the flow step %+v", c)
	}
	sameJSON(t, "its arguments", c.RecordedArgs, `{"v":4}`)
	sameJSON(t, "s's arguments", at(t, tr, "m:c/m:s").RecordedArgs, `{"v":4,"p":{"v":4}}`)
	if len(tr.Children) != 1 || tr.Children[0].Site.String() != "m:c" || tr.Children[0].ID != c.Job || !slices.Equal(tr.Children[0].Modules, []string{"s", "i"}) || tr.Children[0].Failure {
		t.Fatalf("children %+v", tr.Children)
	}
	sv, iv := s.inv(t, "m:c/m:s"), s.inv(t, "m:c/m:i")
	if sv.Env["WM_FLOW_JOB_ID"] != c.Job || sv.Env["WM_ROOT_FLOW_JOB_ID"] != c.Job || sv.Env["WM_ROOT_JOB_ID"] != tr.RunID || sv.Env["WM_FLOW_PATH"] != "f/t/child" {
		t.Fatalf("a subflow step's environment %v", sv.Env)
	}
	if iv.Env["WM_JOB_PATH"] != "f/t/child/i" || iv.Env["WM_FLOW_STEP_ID"] != "i" {
		t.Fatalf("an inline subflow step's environment %v", iv.Env)
	}
}

// CF-19, Q-CF-12 (ADR 0001 §6: matched), JE-19, JE-38, CF-17: a subflow
// reads its caller's results when it has no step of that id, as Windmill's
// scoped lookup walks up; it keeps a flow_env of its own, or its caller's
// when it defines none.
func TestSubflowScopes(t *testing.T) {
	e := mustOpen(t, map[string]string{
		"f/t/leak":  flowOf(mod("s", "echo", inputs("a", js("results.a"), "k", js("flow_env.K")), "")),
		"f/t/owned": withEnv(flowOf(mod("s", "echo", inputs("k", js("flow_env.K")), "")), `{"K":"child"}`),
		"f/t/main": withEnv(flowOf(
			mod("a", "value", inputs("v", static("7")), ""),
			subflowOf("l", "f/t/leak", "", ""),
			subflowOf("o", "f/t/owned", "", ""),
			mod("n", "echo", inputs("l", js("results.l"), "o", js("results.o")), ""),
		), `{"K":"parent"}`),
	}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `{"l":{"a":7,"k":"parent"},"o":{"k":"child"}}`)
}

// CF-17, CF-V1, FX-33, CF-12, FX-20: a subflow that fails fails its step
// with its result, and the caller's continue_on_error and failure module
// apply, the caller's failure module running even when the subflow's ran;
// a subflow whose failure module recovered is a successful job but a
// failed step. A stop inside a subflow ends only the subflow: a clean one
// lets the caller go on, the step's job skipped with skip_if_stopped, and
// an error stop fails the step.
func TestSubflowOutcomes(t *testing.T) {
	handled := func(ret string) string {
		return withFailure(handler("value", inputs("v", static(ret)), ""), mod("a", "fail", "", ""))
	}
	stopping := func(stop string) string {
		return flowOf(mod("a", "value", inputs("v", static("1")), `"stop_after_if":{"expr":"true",`+stop+`"skip_if_stopped":false}`), mod("b", "value", inputs("v", static("2")), ""))
	}
	skipping := flowOf(mod("a", "value", inputs("v", static("1")), `"stop_after_if":{"expr":"true","skip_if_stopped":true}`))
	after := mod("n", "echo", inputs("p", js("previous_result")), "")
	for _, c := range []struct {
		name, child, main string
		status            conformance.RunStatus
		result            string
		step              conformance.StepStatus
		module            conformance.ModuleStatus
	}{
		{"failure", flowOf(mod("a", "fail", "", "")), flowOf(subflowOf("s", "f/t/child", "", ""), after), conformance.RunFailure, `{"error":{"message":"boom","name":"Error","step_id":"a"}}`, conformance.StepFailure, conformance.ModuleFailure},
		{"tolerated", flowOf(mod("a", "fail", "", "")), flowOf(subflowOf("s", "f/t/child", "", `"continue_on_error":true`), after), conformance.RunSuccess, `{"p":{"error":{"message":"boom","name":"Error","step_id":"a"}}}`, conformance.StepFailure, conformance.ModuleFailure},
		{"both handlers", handled(`"child"`), withFailure(handler("echo", inputs("p", js("previous_result")), ""), subflowOf("s", "f/t/child", "", "")), conformance.RunFailure, `{"p":"child"}`, conformance.StepFailure, conformance.ModuleFailure},
		{"recovered", handled(`{"recover":true}`), flowOf(subflowOf("s", "f/t/child", "", ""), after), conformance.RunFailure, `{"recover":true}`, conformance.StepSuccess, conformance.ModuleFailure},
		{"clean stop", stopping(""), flowOf(subflowOf("s", "f/t/child", "", ""), after), conformance.RunSuccess, `{"p":1}`, conformance.StepSuccess, conformance.ModuleSuccess},
		{"skipped stop", skipping, flowOf(subflowOf("s", "f/t/child", "", ""), after), conformance.RunSuccess, `{"p":1}`, conformance.StepSkipped, conformance.ModuleSuccess},
		{"error stop", stopping(`"error_message":"halt",`), flowOf(subflowOf("s", "f/t/child", "", `"continue_on_error":true`), after), conformance.RunSuccess, `{"p":{"error":{"name":"EarlyStopError","message":"halt"}}}`, conformance.StepFailure, conformance.ModuleFailure},
		{"its own stop", flowOf(mod("a", "value", inputs("v", static("1")), "")), flowOf(subflowOf("s", "f/t/child", "", `"stop_after_if":{"expr":"result === 1","skip_if_stopped":true}`), after), conformance.RunSkipped, `1`, conformance.StepSuccess, conformance.ModuleSuccess},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/child": c.child, "f/t/main": c.main}, &loopSteps{})
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, c.status, c.result)
			if s := at(t, tr, "m:s"); s.Status != c.step || s.Module != c.module {
				t.Fatalf("the flow step %+v", s)
			}
		})
	}
	// The subflow's own failure module ran at the step's site.
	e := mustOpen(t, map[string]string{"f/t/child": handled(`"child"`), "f/t/main": flowOf(subflowOf("s", "f/t/child", "", `"continue_on_error":true`))}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	if h := at(t, tr, "m:s/s:failure"); h.Status != conformance.StepSuccess || !tr.Children[0].Failure {
		t.Fatalf("the subflow's failure module %+v, children %+v", h, tr.Children)
	}
}

// CF-23, CF-V3, FX-15 context 3, JE-35 (c): a chaining error at a
// subflow's first module fails the subflow at its pickup, named Anyhow
// with two prefixes, its own failure module bypassed; the caller's failure
// module runs.
func TestSubflowPickupError(t *testing.T) {
	child := withFailure(handler("value", inputs("v", static(`"child-handled"`)), ""), mod("c", "echo", "", `"skip_if":{"expr":"null.x"}`))
	e := mustOpen(t, map[string]string{
		"f/t/child": child,
		"f/t/main":  withFailure(handler("echo", inputs("p", js("previous_result.error.name")), ""), subflowOf("s", "f/t/child", "", "")),
	}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, `{"p":"Anyhow"}`)
	s := at(t, tr, "m:s")
	if name, msg := errorOf(s.Result); name != "Anyhow" || !strings.HasPrefix(msg, "Error: Error: QuickJS evaluation error:") || s.ResultOrigin != OriginEngine {
		t.Fatalf("the flow step %+v", s)
	}
	noRecord(t, tr, "m:s/s:failure")
	if c := at(t, tr, "m:s/m:c"); c.Status != conformance.StepNotRun {
		t.Fatalf("the subflow's first module %+v", c)
	}
}

// ADR 0008 in subflows: hooks see a subflow's modules as modules of the
// subflow's flow (Site.Flow), H1 answers for a flow step as for any launch,
// and H2 rewrites its arguments.
func TestSubflowHooks(t *testing.T) {
	e := mustOpen(t, map[string]string{
		"f/t/child": flowOf(mod("x", "echo", inputs("v", js("flow_input.v")), "")),
		"f/t/main":  flowOf(subflowOf("s", "f/t/child", inputs("v", static("1")), ""), subflowOf("t", "f/t/child", inputs("v", static("2")), "")),
	}, &loopSteps{})
	h := &flowSites{intercept: "t"}
	tr := runIt(t, e, `{}`, h)
	wantStatus(t, tr, conformance.RunSuccess, `{"answered":"t"}`)
	if want := []string{"f/t/main s", "f/t/child x", "f/t/main t"}; !slices.Equal(h.starts, want) {
		t.Fatalf("starts %q, want %q", h.starts, want)
	}
	if s := at(t, tr, "m:t"); !slices.Equal(s.Hooks, []string{"H1"}) {
		t.Fatalf("the answered step %+v", s)
	}
	noRecord(t, tr, "m:t/m:x")
}

// flowSites records the flow and module of every module start, and
// answers for the module intercept with {"answered": <id>}.
type flowSites struct {
	NoHooks
	mu        sync.Mutex
	starts    []string
	intercept string
}

func (h *flowSites) OnModuleStart(_ context.Context, _ Run, s Site) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts = append(h.starts, s.Flow+" "+s.Module)
	return nil
}

func (h *flowSites) Intercept(_ context.Context, _ Run, s Site, _ jsonval.Value) (StepOutcome, bool, error) {
	if s.Module != h.intercept {
		return StepOutcome{}, false, nil
	}
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`{"answered":"` + s.Module + `"}`))}, true, nil
}

// CF-7, CF-17: a subflow keeps its own same_worker flag: its parallel loop
// of two iterations fails its start when the subflow sets it, whatever the
// caller's.
func TestSubflowSameWorker(t *testing.T) {
	loop := loopOf("l", []string{mod("x", "value", inputs("v", static("1")), "")}, `"iterator":{"type":"static","value":[0,1]},"parallel":true,"skip_failures":false`, "")
	sameWorker := func(flow string) string { return strings.TrimSuffix(flow, "}") + `,"same_worker":true}` }
	e := mustOpen(t, map[string]string{"f/t/child": sameWorker(flowOf(loop)), "f/t/main": flowOf(subflowOf("s", "f/t/child", "", ""))}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "")
	if _, msg := errorOf(tr.Result); !strings.Contains(msg, "Cannot continue on same worker") {
		t.Fatalf("the result %s", tr.Result)
	}
	e = mustOpen(t, map[string]string{"f/t/child": flowOf(loop), "f/t/main": sameWorker(flowOf(subflowOf("s", "f/t/child", "", "")))}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `[1,1]`)
}

// A run needs a step executor when a leaf runs anywhere, a subflow's
// included.
func TestSubflowNeedsExecutor(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/child": flowOf(mod("x", "echo", "", "")), "f/t/main": flowOf(subflowOf("s", "f/t/child", "", ""))}, nil)
	if _, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))}); err == nil || !strings.Contains(err.Error(), "no step executor") {
		t.Fatalf("run without an executor: %v", err)
	}
}

// Subflows share the engine with concurrent runs (run with -race).
func TestSubflowConcurrentRuns(t *testing.T) {
	e := mustOpen(t, map[string]string{
		"f/t/child": flowOf(mod("x", "echo", inputs("v", js("flow_input.v")), "")),
		"f/t/main": flowOf(loopOf("l", []string{subflowOf("s", "f/t/child", inputs("v", js("flow_input.iter.value")), "")},
			`"iterator":{"type":"static","value":[1,2,3]},"parallel":true,"skip_failures":false`, "")),
	}, &loopSteps{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))})
			switch {
			case err != nil:
				errs <- err
			case tr.Status != conformance.RunSuccess:
				errs <- errors.New(string(tr.Status))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

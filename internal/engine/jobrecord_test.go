package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// readingExec answers step b by reading, through the job reads of the
// services ([WA-19]), its own job, the run's flow job and the job of step
// a, which completed before it; every other step answers "a".
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
type readingExec struct {
	services *platform.Services
	mu       sync.Mutex
	reads    map[string]string
}

func (x *readingExec) Execute(_ context.Context, inv Invocation) (StepOutcome, error) {
	if inv.Env["WM_FLOW_STEP_ID"] != "b" {
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"a"`))}, nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.reads = map[string]string{}
	read := func(name, id string, get func(string) ([]byte, error)) {
		body, err := get(id)
		if err != nil {
			x.reads[name] = "error: " + err.Error()
			return
		}
		x.reads[name] = string(body)
	}
	read("own", inv.Env["WM_JOB_ID"], x.services.GetJob)
	read("own args", inv.Env["WM_JOB_ID"], x.services.GetJobArgs)
	read("root", inv.Env["WM_ROOT_JOB_ID"], x.services.GetJob)
	var root struct {
		FlowStatus struct {
			Modules []struct{ ID, Job string }
		} `json:"flow_status"`
	}
	if err := json.Unmarshal([]byte(x.reads["root"]), &root); err == nil && len(root.FlowStatus.Modules) > 0 {
		read("a", root.FlowStatus.Modules[0].Job, x.services.GetJob)
	}
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"b"`))}, nil
}

// A step of a live run reads its own job, running, the run's flow job,
// running with the step's module in progress and the module before it
// succeeded, and that module's job, completed, from the run that holds
// them; and its own arguments in jsonb's text output.
func TestJobRecordsDuringARun(t *testing.T) {
	services := &platform.Services{}
	x := &readingExec{services: services}
	flows := map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""), mod("b", "echo", inputs("n", js("flow_input.n")), ""))}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, x}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	tr := runIt(t, e, `{"in":1,"n":{"k":[1,2.50]}}`, NoHooks{})
	if tr.Status != "success" {
		t.Fatalf("the run: %s", tr.Status)
	}
	aJob, bJob := tr.Steps[0].Job, tr.Steps[1].Job
	root := tr.RunID
	host := `"workspace_id":"windmill-lite","created_by":"windmill-lite","email":"windmill-lite@localhost","permissioned_as":"u/windmill-lite"`
	want := map[string]string{
		"own": `{"type":"QueuedJob","id":"` + bJob + `","parent_job":"` + root + `","root_job":"` + root + `","running":true,"flow_step_id":"b","canceled":false,` +
			`"args":{"n":{"k": [1, 2.50]}},"script_path":"f/t/echo","language":"bun","is_flow_step":true,` + host + `}`,
		"own args": `{"n": {"k": [1, 2.50]}}`,
		"root": `{"type":"QueuedJob","id":"` + root + `","running":true,"canceled":false,"args":{"n":{"k": [1, 2.50]},"in":1},"script_path":"f/t/main","is_flow_step":false,` + host +
			`,"flow_status":{"step":1,"modules":[{"id":"a","type":"Success","job":"` + aJob + `","skipped":false},{"id":"b","type":"InProgress","job":"` + bJob + `"}],` +
			`"failure_module":{"id":"failure","type":"WaitingForPriorSteps"},"preprocessor_module":null,"cleanup_module":{}}}`,
		"a": `{"type":"CompletedJob","id":"` + aJob + `","parent_job":"` + root + `","canceled":false,"success":true,"result":"a","args":{},"script_path":"f/t/echo","language":"bun","is_flow_step":true,` + host + `}`,
	}
	for name, w := range want {
		if got := x.reads[name]; got != w {
			t.Errorf("%s:\n got  %s\n want %s", name, got, w)
		}
	}
	// Once the run has ended, its jobs are known to the root lookup and
	// not read (D22).
	for _, id := range []string{root, aJob, bJob} {
		if _, err := services.GetJob(id); !errors.Is(err, platform.ErrEndedRun) {
			t.Errorf("%s after the run ended: %v", id, err)
		}
		if r, ok := services.RootJob(id); !ok || r != root {
			t.Errorf("the root of %s after the run ended: %q %v", id, r, ok)
		}
	}
}

// A run that has ended is released, as before job reads: its step results
// are unreachable once it has settled, though the job registry it was
// registered with knows its jobs' ids for the root lookup, and its jobs
// are no longer read (deviation D22).
func TestEndedRunReleasesItsJobRecords(t *testing.T) {
	services := &platform.Services{}
	x := &retainExec{}
	flows := map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""), mod("b", "echo", "", ""))}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, x}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	settled := settledTrace{ch: make(chan *Trace, 1)}
	tr := runIt(t, e, `{}`, settled)
	if len(tr.Steps) != 2 || !strings.HasPrefix(string(tr.Steps[1].Result.Producer()), `"xxx`) {
		t.Fatalf("the run: %+v", tr.Status)
	}
	ids := []string{tr.RunID, tr.Steps[0].Job, tr.Steps[1].Job}
	<-settled.ch
	tr = nil
	if n := x.reachable(); n != 0 {
		t.Fatalf("%d of 2 results of a run that has ended are still reachable", n)
	}
	for _, id := range ids {
		j, live, ok := services.JobRegistry().Job(id)
		if !ok || live || j.View != nil {
			t.Errorf("%s: %+v live %v known %v", id, j, live, ok)
		}
		if _, err := services.GetJob(id); !errors.Is(err, platform.ErrEndedRun) {
			t.Errorf("%s: %v", id, err)
		}
	}
}

// bigExec answers every step with a string result of 100,000 characters.
type bigExec struct{}

func (bigExec) Execute(_ context.Context, inv Invocation) (StepOutcome, error) {
	if inv.Env["WM_FLOW_STEP_ID"] == "b" {
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"b"`))}, nil
	}
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"` + strings.Repeat("x", 100000) + `"`))}, nil
}

// Deviation D23: a job's result is served whole, however large. Windmill
// replaces a result whose stored column takes 90000 bytes or more with
// "WINDMILL_TOO_BIG" ([OR-V1]), and a smaller compressible one only after
// PostgreSQL's compression, which windmill-lite does not reproduce.
//
// [OR-V1]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/oracle.md#or-v1
func TestJobRecordServesLargeValuesWhole(t *testing.T) {
	services := &platform.Services{}
	var record []byte
	x := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		if inv.Env["WM_FLOW_STEP_ID"] == "b" {
			var root struct {
				FlowStatus struct{ Modules []struct{ Job string } } `json:"flow_status"`
			}
			body, err := services.GetJob(inv.Env["WM_ROOT_JOB_ID"])
			if err == nil && json.Unmarshal(body, &root) == nil {
				record, err = services.GetJob(root.FlowStatus.Modules[0].Job)
			}
			if err != nil {
				return StepOutcome{}, err
			}
		}
		return bigExec{}.Execute(context.Background(), inv)
	}}
	flows := map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""), mod("b", "echo", "", ""))}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, x}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	if tr := runIt(t, e, `{}`, NoHooks{}); tr.Status != "success" {
		t.Fatalf("the run: %s", tr.Status)
	}
	var a struct{ Result string }
	if err := json.Unmarshal(record, &a); err != nil || len(a.Result) != 100000 {
		t.Errorf("a's record: %d characters of result, %v", len(a.Result), err)
	}
}

// stepFunc is a step executor from a function.
type stepFunc struct {
	f func(Invocation) (StepOutcome, error)
}

func (x *stepFunc) Execute(_ context.Context, inv Invocation) (StepOutcome, error) { return x.f(inv) }

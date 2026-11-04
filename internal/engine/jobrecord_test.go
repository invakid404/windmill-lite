package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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

// jobReader reads, from a step, records of the run's jobs through the job
// reads of the services, as a step that reads its own job tree does.
type jobReader struct {
	t        *testing.T
	services *platform.Services
}

// jobRec is the fields of a job record that the tests below read.
type jobRec struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	ParentJob  string          `json:"parent_job"`
	Success    *bool           `json:"success"`
	ScriptPath *string         `json:"script_path"`
	Args       json.RawMessage `json:"args"`
	Result     json.RawMessage `json:"result"`
	FlowStatus struct {
		Step    int `json:"step"`
		Modules []struct {
			ID            string   `json:"id"`
			Type          string   `json:"type"`
			Job           string   `json:"job"`
			Skipped       *bool    `json:"skipped"`
			FailedRetries []string `json:"failed_retries"`
		} `json:"modules"`
	} `json:"flow_status"`
}

func (x jobReader) read(id string) (jobRec, error) {
	var rec jobRec
	body, err := x.services.GetJob(id)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		x.t.Errorf("%s: %v", body, err)
	}
	return rec, nil
}

func (x jobReader) must(id string) jobRec {
	x.t.Helper()
	rec, err := x.read(id)
	if err != nil {
		x.t.Errorf("%s: %v", id, err)
	}
	return rec
}

// A subflow whose first run failed and whose retry succeeded: each of its
// two flow jobs shows its own run, its step's job and that job's status,
// though the retry ran at the same sites; and each step's job names its
// own flow job as its parent ([WA-19]/l).
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
func TestJobRecordsOfARetriedSubflow(t *testing.T) {
	services := &platform.Services{}
	x := jobReader{t, services}
	var mu sync.Mutex
	var leaves []string
	var read func() map[string]jobRec
	got := map[string]jobRec{}
	var root string
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		mu.Lock()
		defer mu.Unlock()
		root = inv.Env["WM_ROOT_JOB_ID"]
		switch inv.Env["WM_FLOW_STEP_ID"] {
		case "a":
			leaves = append(leaves, inv.ID)
			if len(leaves) == 1 {
				return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"message":"first","name":"Error"}`))}, nil
			}
		case "b":
			got = read()
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"ok"`))}, nil
	}}
	read = func() map[string]jobRec {
		out := map[string]jobRec{"root": x.must(root)}
		s := out["root"].FlowStatus.Modules[0]
		if len(s.FailedRetries) != 1 {
			t.Fatalf("the subflow step's entry: %+v", s)
		}
		out["first"], out["second"] = x.must(s.FailedRetries[0]), x.must(s.Job)
		for _, l := range leaves {
			out[l] = x.must(l)
		}
		return out
	}
	retry := `"retry":{"constant":{"attempts":1,"seconds":0}}`
	flows := map[string]string{
		"f/t/main": flowOf(`{"id":"s","value":{"type":"flow","path":"f/t/sub","input_transforms":{}},`+retry+`}`, mod("b", "echo", "", "")),
		"f/t/sub":  flowOf(mod("a", "echo", "", "")),
	}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, exec}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: NoHooks{}, Seed: 1})
	if err != nil || tr.Status != "success" {
		t.Fatalf("the run: %v %+v", err, tr)
	}
	if len(leaves) != 2 {
		t.Fatalf("step a ran %d times", len(leaves))
	}
	first, second := got["first"], got["second"]
	for _, c := range []struct {
		name string
		flow jobRec
		leaf string
		typ  string
		step int
		ok   bool
	}{
		{"the first run", first, leaves[0], "Failure", 0, false},
		{"the retry", second, leaves[1], "Success", 1, true},
	} {
		m := c.flow.FlowStatus.Modules
		if c.flow.Success == nil || *c.flow.Success != c.ok || len(m) != 1 || m[0].Job != c.leaf || m[0].Type != c.typ || c.flow.FlowStatus.Step != c.step {
			t.Errorf("%s: %+v, want its step's job %s %s", c.name, c.flow, c.leaf, c.typ)
		}
		if leaf := got[c.leaf]; leaf.ParentJob != c.flow.ID {
			t.Errorf("%s's step job's parent: %q, want %q", c.name, leaf.ParentJob, c.flow.ID)
		}
	}
}

// A subflow whose input transform failed launched its flow job, which
// failed before any of its modules ran: the flow's status names the job,
// and the job is read, with every module waiting, and its arguments
// ([WA-19]/m).
func TestJobRecordOfAnUnlaunchedSubflow(t *testing.T) {
	services := &platform.Services{}
	x := jobReader{t, services}
	var sub jobRec
	var subErr, argsErr error
	var root string
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		if inv.Env["WM_FLOW_STEP_ID"] == "b" {
			root = inv.Env["WM_ROOT_JOB_ID"]
			rec := x.must(root)
			sub, subErr = x.read(rec.FlowStatus.Modules[0].Job)
			_, argsErr = services.GetJobArgs(rec.FlowStatus.Modules[0].Job)
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"ok"`))}, nil
	}}
	flows := map[string]string{
		"f/t/main": flowOf(`{"id":"s","value":{"type":"flow","path":"f/t/sub","input_transforms":{"x":`+js("flow_input.nope.deeper")+`}},"continue_on_error":true}`, mod("b", "echo", "", "")),
		"f/t/sub":  flowOf(mod("a", "echo", "", ""), mod("c", "echo", "", "")),
	}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, exec}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: NoHooks{}, Seed: 1})
	if err != nil || tr.Status != "success" {
		t.Fatalf("the run: %v %+v", err, tr)
	}
	if subErr != nil || argsErr != nil {
		t.Fatalf("the subflow's job: %v; its arguments: %v", subErr, argsErr)
	}
	m := sub.FlowStatus.Modules
	if sub.Type != "CompletedJob" || sub.Success == nil || *sub.Success || len(m) != 2 || m[0].Type != "WaitingForPriorSteps" || m[1].Type != "WaitingForPriorSteps" {
		t.Errorf("the subflow's job: %+v", sub)
	}
}

// A subflow step that skip_if skipped, and one that a mock answered, ran
// an identity job in place of the subflow, which the flow's status names
// and which is read, with no script path ([WA-19]/m, FX-16).
func TestJobRecordsOfSkippedAndMockedSubflows(t *testing.T) {
	services := &platform.Services{}
	x := jobReader{t, services}
	var root string
	var got []jobRec
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		if inv.Env["WM_FLOW_STEP_ID"] == "b" {
			root = inv.Env["WM_ROOT_JOB_ID"]
			for _, m := range x.must(root).FlowStatus.Modules[:2] {
				got = append(got, x.must(m.Job))
			}
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"ok"`))}, nil
	}}
	sub := `"value":{"type":"flow","path":"f/t/sub","input_transforms":{}}`
	flows := map[string]string{
		"f/t/main": flowOf(`{"id":"s1",`+sub+`,"skip_if":{"expr":"true"}}`, `{"id":"s2",`+sub+`,"mock":{"enabled":true,"return_value":{"m":1}}}`, mod("b", "echo", "", "")),
		"f/t/sub":  flowOf(mod("a", "echo", "", "")),
	}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, exec}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: NoHooks{}, Seed: 1})
	if err != nil || tr.Status != "success" {
		t.Fatalf("the run: %v %+v", err, tr)
	}
	if len(got) != 2 {
		t.Fatalf("read %d jobs", len(got))
	}
	for i, rec := range got {
		if rec.ID == "" || rec.Type != "CompletedJob" || rec.Success == nil || !*rec.Success || rec.ScriptPath != nil {
			t.Errorf("s%d's job: %+v", i+1, rec)
		}
	}
	if string(got[1].Result) != `{"m": 1}` {
		t.Errorf("the mocked subflow's result: %s", got[1].Result)
	}
}

// A flow job whose module failed and waits for its retry delay is not
// read: its status in that state has not been recorded ([WA-19]/n). Branch
// b0 of a parallel branchall fails once and waits a second for its retry;
// branch b1 reads b0's flow job meanwhile, then after the retry.
func TestJobRecordWhileAModuleWaitsForItsRetry(t *testing.T) {
	services := &platform.Services{}
	x := jobReader{t, services}
	failed := make(chan struct{})
	var once sync.Once
	var during, after error
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		switch inv.Env["WM_FLOW_STEP_ID"] {
		case "f":
			first := false
			once.Do(func() { first = true })
			if first {
				defer close(failed)
				return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"message":"first","name":"Error"}`))}, nil
			}
		case "r":
			<-failed
			root := inv.Env["WM_ROOT_JOB_ID"]
			flowJob := ""
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				body, err := services.GetJob(root)
				if err == nil {
					var v struct {
						FlowStatus struct {
							Modules []struct {
								FlowJobs []string `json:"flow_jobs"`
							} `json:"modules"`
						} `json:"flow_status"`
					}
					if json.Unmarshal(body, &v) == nil && len(v.FlowStatus.Modules[0].FlowJobs) == 2 {
						flowJob = v.FlowStatus.Modules[0].FlowJobs[0]
					}
				}
				if flowJob != "" {
					if _, during = x.read(flowJob); during != nil {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			for time.Now().Before(deadline) {
				if _, after = x.read(flowJob); after == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"ok"`))}, nil
	}}
	retry := `"retry":{"constant":{"attempts":1,"seconds":1}}`
	flows := map[string]string{
		"f/t/main": flowOf(`{"id":"ba","value":{"type":"branchall","parallel":true,"branches":[{"modules":[` + mod("f", "echo", "", retry) + `]},{"modules":[` + mod("r", "echo", "", "") + `]}]}}`),
	}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, exec}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: NoHooks{}, Seed: 1}); err != nil {
		t.Fatal(err)
	}
	var ns *platform.NotServed
	if !errors.As(during, &ns) || ns.Reason != "a module of the flow waits for a sleep or a retry delay" {
		t.Errorf("the flow job while its module waits for its retry: %v", during)
	}
	if after != nil {
		t.Errorf("the flow job after the retry: %v", after)
	}
}

// A job's result or arguments that may take 90000 bytes or more stored
// are not served: Windmill replaces such a value or not depending on
// PostgreSQL's compression ([WA-19]/o, [OR-V1]). Below that bound the
// value is served whole, and GET jobs_u/get_args/{id} serves arguments
// whole at any size.
//
// [OR-V1]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/oracle.md#or-v1
func TestJobRecordLargeValues(t *testing.T) {
	services := &platform.Services{}
	var root string
	type read struct {
		rec  []byte
		args []byte
		err  error
	}
	reads := map[string]read{}
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		switch id := inv.Env["WM_FLOW_STEP_ID"]; id {
		case "small":
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"` + strings.Repeat("x", 60000) + `"`))}, nil
		case "big", "arg":
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"` + strings.Repeat("x", 100000) + `"`))}, nil
		case "r":
			root = inv.Env["WM_ROOT_JOB_ID"]
			body, err := services.GetJob(root)
			if err != nil {
				return StepOutcome{}, err
			}
			var v struct {
				FlowStatus struct{ Modules []struct{ ID, Job string } } `json:"flow_status"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return StepOutcome{}, err
			}
			for _, m := range v.FlowStatus.Modules[:3] {
				var rd read
				rd.rec, rd.err = services.GetJob(m.Job)
				rd.args, _ = services.GetJobArgs(m.Job)
				reads[m.ID] = rd
			}
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`1`))}, nil
	}}
	flows := map[string]string{"f/t/main": flowOf(mod("small", "echo", "", ""), mod("big", "echo", "", ""), mod("arg", "echo", inputs("x", js("results.big")), ""), mod("r", "echo", "", ""))}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, exec}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	if tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: NoHooks{}, Seed: 1}); err != nil || tr.Status != "success" {
		t.Fatalf("the run: %v %+v", err, tr)
	}
	var small struct{ Result string }
	if rd := reads["small"]; rd.err != nil || json.Unmarshal(rd.rec, &small) != nil || len(small.Result) != 60000 {
		t.Errorf("a result of 60,000 characters: %d characters, %v", len(small.Result), rd.err)
	}
	var ns *platform.NotServed
	for _, id := range []string{"big", "arg"} {
		if rd := reads[id]; !errors.As(rd.err, &ns) || !strings.Contains(ns.Reason, "90000 bytes") {
			t.Errorf("%s: %v", id, rd.err)
		}
	}
	if args := reads["arg"].args; len(args) < 100000 {
		t.Errorf("the arguments of 100,000 characters from get_args: %d bytes", len(args))
	}
}

// stepFunc is a step executor from a function.
type stepFunc struct {
	f func(Invocation) (StepOutcome, error)
}

func (x *stepFunc) Execute(_ context.Context, inv Invocation) (StepOutcome, error) { return x.f(inv) }

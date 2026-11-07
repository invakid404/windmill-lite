package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
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
		"a": `{"type":"CompletedJob","id":"` + aJob + `","parent_job":"` + root + `","canceled":false,"success":true,"result":"a","resolved":false,"args":{},"script_path":"f/t/echo","language":"bun","is_flow_step":true,` + host + `}`,
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

// A flow job whose module failed and waits for its retry delay is read
// ([WA-19]/p): the module's entry waits for its executor and names the
// retry's job, pushed for later, which is queued and not running; once
// the retry has run, the same job has completed. Branch b0 of a parallel
// branchall fails once and waits a second for its retry; branch b1 reads
// b0's flow job and the retry's job meanwhile, then after the retry.
func TestJobRecordWhileAModuleWaitsForItsRetry(t *testing.T) {
	services := &platform.Services{}
	x := jobReader{t, services}
	failed := make(chan struct{})
	var once sync.Once
	var during jobRec
	var retryJob, retryRan string
	var waiting, after struct {
		Type    string `json:"type"`
		Running *bool  `json:"running"`
		Success *bool  `json:"success"`
	}
	var readErr error
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		switch inv.Env["WM_FLOW_STEP_ID"] {
		case "f":
			first := false
			once.Do(func() { first = true })
			if first {
				defer close(failed)
				return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"message":"first","name":"Error"}`))}, nil
			}
			retryRan = inv.ID
		case "r":
			<-failed
			root := inv.Env["WM_ROOT_JOB_ID"]
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) && retryJob == "" {
				var v struct {
					FlowStatus struct {
						Modules []struct {
							FlowJobs []string `json:"flow_jobs"`
						} `json:"modules"`
					} `json:"flow_status"`
				}
				if body, err := services.GetJob(root); err == nil && json.Unmarshal(body, &v) == nil {
					if fj := v.FlowStatus.Modules[0].FlowJobs; len(fj) == 2 {
						if rec, err := x.read(fj[0]); err == nil && len(rec.FlowStatus.Modules) == 1 && rec.FlowStatus.Modules[0].Type == "WaitingForExecutor" {
							during, retryJob = rec, rec.FlowStatus.Modules[0].Job
						}
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			var body []byte
			if body, readErr = services.GetJob(retryJob); readErr == nil {
				readErr = json.Unmarshal(body, &waiting)
			}
			for time.Now().Before(deadline) {
				if body, err := services.GetJob(retryJob); err == nil && json.Unmarshal(body, &after) == nil && after.Type == "CompletedJob" {
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
	if retryJob == "" {
		t.Fatalf("b0's flow job never showed its module waiting for its executor: %+v", during)
	}
	if m := during.FlowStatus.Modules[0]; during.Type != "QueuedJob" || during.FlowStatus.Step != 0 || m.ID != "f" || len(m.FailedRetries) != 0 {
		t.Errorf("b0's flow job while f waits for its retry: %+v", during)
	}
	if readErr != nil || waiting.Type != "QueuedJob" || waiting.Running == nil || *waiting.Running {
		t.Errorf("the retry's job while it waits: %+v %v", waiting, readErr)
	}
	if retryRan != retryJob {
		t.Errorf("the retry ran as %q, its pushed job was %q", retryRan, retryJob)
	}
	if after.Type != "CompletedJob" || after.Success == nil || !*after.Success {
		t.Errorf("the retry's job once it ran: %+v", after)
	}
}

// CE 1.862.0's job reads, as the job-reads fixtures record them: a
// completed job's record holds resolved, false; the status of a child flow
// whose body the deploy stored as a node, one that is not CF-8's simple
// body, holds no_inherited_flow_env, true, while an inline body's does
// not; and a running parallel container names the flow job it runs in as
// its job, where its children are its flow_jobs.
func TestJobRecordsOfNodesAndParallelContainers(t *testing.T) {
	services := &platform.Services{}
	var mu sync.Mutex
	raw := map[string][]byte{}
	read := func(name, id string) []byte {
		body, err := services.GetJob(id)
		if err != nil {
			t.Errorf("%s (%s): %v", name, id, err)
		}
		mu.Lock()
		raw[name] = body
		mu.Unlock()
		return body
	}
	parentOf := func(body []byte) string {
		var v struct {
			ParentJob string `json:"parent_job"`
		}
		_ = json.Unmarshal(body, &v)
		return v.ParentJob
	}
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		switch step := inv.Env["WM_FLOW_STEP_ID"]; step {
		case "r", "q":
			read(step+" parent", inv.Env["WM_FLOW_JOB_ID"])
		case "p":
			parent := read("p parent", inv.Env["WM_FLOW_JOB_ID"])
			root := read("p root", parentOf(parent))
			var v struct {
				FlowStatus struct {
					Modules []struct{ Job string } `json:"modules"`
				} `json:"flow_status"`
			}
			if err := json.Unmarshal(root, &v); err == nil && len(v.FlowStatus.Modules) > 0 {
				read("s child", v.FlowStatus.Modules[0].Job)
			}
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"x"`))}, nil
	}}
	iter := `"iterator":{"type":"static","value":[1]},"skip_failures":false`
	flows := map[string]string{"f/t/main": flowOf(
		loopOf("s", []string{mod("a", "echo", "", ""), mod("r", "echo", "", "")}, iter+`,"parallel":false`, ""),
		loopOf("l", []string{mod("p", "echo", "", "")}, iter+`,"parallel":true`, ""),
		`{"id":"b","value":{"type":"branchone","branches":[{"expr":"true","modules":[`+mod("q", "echo", "", "")+`]}],"default":[]}}`,
	)}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, exec}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: NoHooks{}, Seed: 1})
	if err != nil || tr.Status != "success" {
		t.Fatalf("the run: %v %+v", err, tr)
	}
	type record struct {
		ID         string                     `json:"id"`
		Type       string                     `json:"type"`
		Resolved   *bool                      `json:"resolved"`
		FlowStatus map[string]json.RawMessage `json:"flow_status"`
	}
	rec := func(name string) record {
		var r record
		if err := json.Unmarshal(raw[name], &r); err != nil {
			t.Fatalf("%s: %s %v", name, raw[name], err)
		}
		return r
	}
	for name, node := range map[string]bool{"r parent": true, "s child": true, "p parent": false, "q parent": false, "p root": false} {
		r := rec(name)
		if got, ok := r.FlowStatus["no_inherited_flow_env"]; ok != node || ok && string(got) != "true" {
			t.Errorf("%s: no_inherited_flow_env %s (present %v), want present %v", name, got, ok, node)
		}
		if done := r.Type == "CompletedJob"; done != (r.Resolved != nil) || done && *r.Resolved {
			t.Errorf("%s: %s with resolved %v", name, r.Type, r.Resolved)
		}
	}
	if r := rec("s child"); r.Type != "CompletedJob" || r.Resolved == nil {
		t.Errorf("the loop's completed child: %+v", r)
	}
	var root struct {
		ID         string `json:"id"`
		FlowStatus struct {
			Modules []struct {
				ID       string   `json:"id"`
				Type     string   `json:"type"`
				Job      string   `json:"job"`
				Parallel bool     `json:"parallel"`
				FlowJobs []string `json:"flow_jobs"`
			} `json:"modules"`
		} `json:"flow_status"`
	}
	if err := json.Unmarshal(raw["p root"], &root); err != nil {
		t.Fatal(err)
	}
	if m := root.FlowStatus.Modules[1]; m.ID != "l" || m.Type != "InProgress" || !m.Parallel || m.Job != root.ID || len(m.FlowJobs) != 1 || m.FlowJobs[0] == root.ID {
		t.Errorf("the running parallel loop's entry: %+v, its flow job %s", m, root.ID)
	}
}

// A job pushed for later keeps its id through its completion, whatever
// completes it ([WA-19]/p): an identity job of a skipped step, the result
// cache's answer at its pickup, a failure of its input transform, an
// entrypoint override that fails it before it runs, the identity job of a
// skipped loop and of a mocked branchone, and a flow step's child flow
// that its input transform fails. Each waits for the
// sleep of the step before it, in a branch of a parallel branchall, while
// the last branch reads the branches' flow jobs: each names its waiting
// step's job, waiting for its executor, which reads queued and not
// running; once the run has ended, the trace names the same job.
func TestJobsPushedForLaterKeepTheirIds(t *testing.T) {
	services := &platform.Services{}
	x := jobReader{t, services}
	type waited struct {
		id      string
		queued  bool
		running *bool
	}
	got := map[string]waited{}
	var readErr error
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		if inv.Env["WM_FLOW_STEP_ID"] != "r" {
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"ok"`))}, nil
		}
		want := map[string]int{"b0": 1, "b1": 2, "b2": 1, "b3": 1, "b4": 1, "b5": 1, "b6": 1}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && len(got) < len(want) {
			root := x.must(inv.Env["WM_ROOT_JOB_ID"])
			var v struct {
				FlowStatus struct {
					Modules []struct {
						FlowJobs []string `json:"flow_jobs"`
					} `json:"modules"`
				} `json:"flow_status"`
			}
			body, _ := services.GetJob(root.ID)
			if json.Unmarshal(body, &v) != nil || len(v.FlowStatus.Modules[0].FlowJobs) != 8 {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			for b, k := range want {
				n := int(b[1] - '0')
				fl, err := x.read(v.FlowStatus.Modules[0].FlowJobs[n])
				if err != nil {
					readErr = err
					continue
				}
				if m := fl.FlowStatus.Modules; len(m) > k && m[k].Type == "WaitingForExecutor" && m[k].Job != "" {
					var job struct {
						Type    string `json:"type"`
						Running *bool  `json:"running"`
					}
					jb, err := services.GetJob(m[k].Job)
					if err != nil || json.Unmarshal(jb, &job) != nil {
						readErr = err
						continue
					}
					got[b] = waited{id: m[k].Job, queued: job.Type == "QueuedJob", running: job.Running}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"read"`))}, nil
	}}
	sleep := `"sleep":` + static("1")
	cached := cachedInline("c0", "", `"cache_ttl":60`)
	branch := func(mods ...string) string { return `{"modules":[` + strings.Join(mods, ",") + `]}` }
	flows := map[string]string{"f/t/main": flowOf(`{"id":"ba","value":{"type":"branchall","parallel":true,"branches":[` + strings.Join([]string{
		branch(mod("a0", "echo", "", sleep), mod("s", "echo", "", `"skip_if":{"expr":"true"}`)),
		branch(cached, mod("a1", "echo", "", sleep), strings.Replace(cached, `"id":"c0"`, `"id":"c1"`, 1)),
		branch(mod("a2", "echo", "", sleep), mod("t", "echo", inputs("x", js("flow_input.nope.deeper")), `"continue_on_error":true`)),
		branch(mod("a3", "echo", "", sleep), mod("e", "echo", inputs("_ENTRYPOINT_OVERRIDE", static(`"1bad"`)), `"continue_on_error":true`)),
		branch(mod("a4", "echo", "", sleep), loopOf("l", []string{mod("li", "echo", "", "")}, `"iterator":{"type":"static","value":[1]},"skip_failures":false`, `"skip_if":{"expr":"true"}`)),
		branch(mod("a5", "echo", "", sleep), `{"id":"bo","value":{"type":"branchone","branches":[],"default":[`+mod("bi", "echo", "", "")+`]},"mock":{"enabled":true,"return_value":{"b":1}}}`),
		branch(mod("a6", "echo", "", sleep), `{"id":"sf","value":{"type":"flow","path":"f/t/sub","input_transforms":{"x":`+js("flow_input.nope.deeper")+`}},"continue_on_error":true}`),
		branch(mod("r", "echo", "", "")),
	}, ",") + `]}}`), "f/t/sub": flowOf(mod("o", "echo", "", ""))}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, exec}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: NoHooks{}, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("waiting jobs read: %+v (%v)", got, readErr)
	}
	final := map[string]string{}
	for _, rec := range tr.Steps {
		for b, step := range map[string]string{"b0": "/m:s", "b1": "/m:c1", "b2": "/m:t", "b3": "/m:e", "b4": "/m:l", "b5": "/m:bo", "b6": "/m:sf"} {
			if strings.HasSuffix(rec.Site.String(), step) && !rec.Superseded {
				final[b] = jobOf(rec)
			}
		}
	}
	for b, w := range got {
		if !w.queued || w.running == nil || *w.running {
			t.Errorf("%s's waiting job: %+v", b, w)
		}
		if final[b] != w.id {
			t.Errorf("%s's job waited as %q and completed as %q", b, w.id, final[b])
		}
	}
}

// boundaryText builds the texts of jsonval's boundaryInputs, which
// PostgreSQL stores in the sizes TestReplacedFromNinetyThousandBytes
// names (jsonval's TestToastedSizeMatchesPostgreSQL): n characters, of
// which a share, repeats, repeats earlier text.
func boundaryText(n int, repeats float64) string {
	rng := rand.New(rand.NewPCG(5, 6))
	letters := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	b := make([]byte, 0, n+32)
	for len(b) < n {
		if len(b) > 64 && rng.Float64() < repeats {
			at := max(0, len(b)-4000) + rng.IntN(min(len(b), 4000)-32)
			b = append(b, b[at:at+3+rng.IntN(29)]...)
		} else {
			for range 16 {
				b = append(b, letters[rng.IntN(64)])
			}
		}
	}
	return string(b[:n])
}

// A job's result, and its arguments, are replaced from 90,000 bytes
// stored ([WA-19]/o): at 89,999 they stay whole, and at 90,000 and 90,001
// they are replaced, whether pglz shrinks them to that size (text half of
// whose pieces repeat) or not (random characters).
func TestReplacedFromNinetyThousandBytes(t *testing.T) {
	for i, stored := range []int{89999, 90000, 90001} {
		for _, c := range []struct {
			n       int
			repeats float64
			args    bool
		}{{89991 + i, 0, false}, {89986 + i, 0, true}, {144031 + i, 0.5, false}, {144026 + i, 0.5, true}} {
			quoted, _ := json.Marshal(boundaryText(c.n, c.repeats))
			text := quoted
			if c.args {
				text = []byte(`{"x":` + string(quoted) + `}`)
			}
			name := fmt.Sprintf("%d characters, %.0f%% repeats, arguments %v", c.n, 100*c.repeats, c.args)
			if size, err := jsonval.ToastedSize(text); err != nil || size != stored {
				t.Fatalf("%s: stored in %d bytes (%v), want %d", name, size, err, stored)
			}
			want := stored >= 90000
			if got := replaced(text); got != want {
				t.Errorf("%s, stored in %d bytes: replaced %v", name, stored, got)
			}
			result, args := []byte(`1`), []byte(`{}`)
			if c.args {
				args = text
			} else {
				result = text
			}
			gotResult, gotArgs := tooBig(true, result, args)
			if c.args && (string(gotArgs) == `{"reason":"WINDMILL_TOO_BIG"}`) != want {
				t.Errorf("%s, stored in %d bytes: arguments %.40s", name, stored, gotArgs)
			}
			if !c.args && (string(gotResult) == `"WINDMILL_TOO_BIG"`) != want {
				t.Errorf("%s, stored in %d bytes: result %.40s", name, stored, gotResult)
			}
		}
	}
}

// A job's result and arguments that take 90000 bytes or more stored are
// replaced in the record, by "WINDMILL_TOO_BIG" and
// {"reason":"WINDMILL_TOO_BIG"}, as Windmill replaces them ([WA-19]/o,
// [OR-V1]); the stored size is after PostgreSQL's compression, so 100,000
// times "x" is read whole and 100,000 characters that do not compress are
// replaced. GET jobs_u/get_args/{id} serves arguments whole at any size.
//
// [OR-V1]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/oracle.md#or-v1
func TestJobRecordLargeValues(t *testing.T) {
	services := &platform.Services{}
	type read struct {
		rec  []byte
		args []byte
		err  error
	}
	reads := map[string]read{}
	rng := rand.New(rand.NewPCG(5, 6))
	letters := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	noise := make([]byte, 100000)
	for i := range noise {
		noise[i] = letters[rng.IntN(64)]
	}
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		switch id := inv.Env["WM_FLOW_STEP_ID"]; id {
		case "xs":
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"` + strings.Repeat("x", 100000) + `"`))}, nil
		case "big", "arg":
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"` + string(noise) + `"`))}, nil
		case "r":
			body, err := services.GetJob(inv.Env["WM_ROOT_JOB_ID"])
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
	flows := map[string]string{"f/t/main": flowOf(mod("xs", "echo", "", ""), mod("big", "echo", "", ""), mod("arg", "echo", inputs("x", js("results.big")), ""), mod("r", "echo", "", ""))}
	e, err := New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, exec}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	if tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: NoHooks{}, Seed: 1}); err != nil || tr.Status != "success" {
		t.Fatalf("the run: %v %+v", err, tr)
	}
	var rec struct {
		Result json.RawMessage `json:"result"`
		Args   json.RawMessage `json:"args"`
	}
	if rd := reads["xs"]; rd.err != nil || json.Unmarshal(rd.rec, &rec) != nil || len(rec.Result) != 100002 {
		t.Errorf("a result of 100,000 times x: %d bytes, %v", len(rec.Result), rd.err)
	}
	if rd := reads["big"]; rd.err != nil || json.Unmarshal(rd.rec, &rec) != nil || string(rec.Result) != `"WINDMILL_TOO_BIG"` {
		t.Errorf("a result of 100,000 characters that do not compress: %.40s, %v", rec.Result, rd.err)
	}
	if rd := reads["arg"]; rd.err != nil || json.Unmarshal(rd.rec, &rec) != nil || string(rec.Args) != `{"reason":"WINDMILL_TOO_BIG"}` {
		t.Errorf("arguments holding them: %.40s, %v", rec.Args, rd.err)
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

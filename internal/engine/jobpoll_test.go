package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// TestJobPollStates checks the result polls' states that a run gives
// (platform.JobPoller, through Services.ResultPoll), as the facade's
// ResultPollsForAllJobs option reads them. In a parallel branchall, step f
// fails once and waits a second for its retry, while step r polls: its own
// job runs; the retry's job, pushed for later, has not started; and once
// the retry has run, that job has completed with its result in jsonb's
// text output.
func TestJobPollStates(t *testing.T) {
	services := &platform.Services{}
	failed := make(chan struct{})
	var once sync.Once
	var retryJob string
	var own, waiting, after platform.JobResult
	var problems []error
	poll := func(id string) platform.JobResult {
		r, known, err := services.ResultPoll(id)
		if err != nil || !known {
			problems = append(problems, fmt.Errorf("the poll of %s: known %t, %v", id, known, err))
		}
		return r
	}
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		switch inv.Env["WM_FLOW_STEP_ID"] {
		case "f":
			first := false
			once.Do(func() { first = true })
			if first {
				defer close(failed)
				return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"message":"first","name":"Error"}`))}, nil
			}
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`{"k":[1,2.50]}`))}, nil
		case "r":
			<-failed
			own = poll(inv.ID)
			root := inv.Env["WM_ROOT_JOB_ID"]
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) && retryJob == "" {
				var top, branch struct {
					FlowStatus struct {
						Modules []struct {
							Type     string   `json:"type"`
							Job      string   `json:"job"`
							FlowJobs []string `json:"flow_jobs"`
						} `json:"modules"`
					} `json:"flow_status"`
				}
				if body, err := services.GetJob(root); err == nil && json.Unmarshal(body, &top) == nil {
					if fj := top.FlowStatus.Modules[0].FlowJobs; len(fj) == 2 {
						if body, err := services.GetJob(fj[0]); err == nil && json.Unmarshal(body, &branch) == nil &&
							len(branch.FlowStatus.Modules) == 1 && branch.FlowStatus.Modules[0].Type == "WaitingForExecutor" {
							retryJob = branch.FlowStatus.Modules[0].Job
						}
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			if retryJob == "" {
				problems = append(problems, fmt.Errorf("f's retry never waited for its executor"))
				break
			}
			waiting = poll(retryJob)
			for time.Now().Before(deadline) {
				if after = poll(retryJob); after.Completed {
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
	for _, p := range problems {
		t.Error(p)
	}
	if own.Started != true || own.Completed {
		t.Errorf("the polling step's own job: %+v, want started and not completed", own)
	}
	if waiting.Started || waiting.Completed {
		t.Errorf("the retry's job while it waits: %+v, want not started", waiting)
	}
	if !after.Completed || !after.Started || !after.Success || string(after.Result) != `{"k": [1, 2.50]}` {
		t.Errorf("the retry's job once it ran: %+v %s, want completed with {\"k\": [1, 2.50]}", after, after.Result)
	}
}

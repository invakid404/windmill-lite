package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// heldClock is a Clock whose waits last until the test releases them, so
// that a job pushed for later stays pushed while the test looks at it.
type heldClock struct{ release chan struct{} }

func (heldClock) Now() time.Time { return time.Now() }

func (c heldClock) Wait(ctx context.Context, _ time.Duration) error {
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// polledStatus is a flow job's record, as far as the poll tests read its
// status.
type polledStatus struct {
	FlowStatus struct {
		Modules []struct {
			ID       string   `json:"id"`
			Type     string   `json:"type"`
			Job      string   `json:"job"`
			FlowJobs []string `json:"flow_jobs"`
		} `json:"modules"`
	} `json:"flow_status"`
}

// TestJobPollStates checks the result polls' states that a run gives
// (platform.JobView's JobPoll, through Services.ResultPoll), as the
// facade's ResultPollsForAllJobs option reads them. In a parallel
// branchall, step f fails once, and its retry waits on the run's clock,
// which holds it until the test releases it, while step r polls: its own
// job runs; the retry's job, pushed for later, has not started; and once
// the clock lets the retry run, that job has completed with its result in
// jsonb's text output.
func TestJobPollStates(t *testing.T) {
	services := &platform.Services{}
	clock := heldClock{release: make(chan struct{})}
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
			deadline := time.Now().Add(10 * time.Second)
			// The retry waits on the held clock, so its job stays pushed
			// for later until the release below.
			for time.Now().Before(deadline) && retryJob == "" {
				var top, branch polledStatus
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
				close(clock.release)
				break
			}
			waiting = poll(retryJob)
			close(clock.release)
			for time.Now().Before(deadline) {
				if after = poll(retryJob); after.Completed {
					break
				}
				time.Sleep(5 * time.Millisecond)
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
	if _, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: NoHooks{}, Seed: 1, Clock: clock}); err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	if !own.Started || own.Completed {
		t.Errorf("the polling step's own job: %+v, want started and not completed", own)
	}
	if waiting.Started || waiting.Completed {
		t.Errorf("the retry's job while it waits: %+v, want not started", waiting)
	}
	if !after.Completed || !after.Started || !after.Success || string(after.Result) != `{"k": [1, 2.50]}` {
		t.Errorf("the retry's job once it ran: %+v %s, want completed with {\"k\": [1, 2.50]}", after, after.Result)
	}
}

// largeText is a JSON string of 120,000 characters from a fixed
// pseudo-random sequence: a result that does not compress, so that a job's
// record, which the job read answers, replaces it with its placeholder.
func largeText() string {
	const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	rng := rand.New(rand.NewPCG(7, 11))
	b := make([]byte, 0, 120002)
	b = append(b, '"')
	for range 120000 {
		b = append(b, abc[rng.IntN(len(abc))])
	}
	return string(append(b, '"'))
}

// TestJobPollWholeResults checks that the result polls give a completed
// job's whole stored result where the job read's record gives its
// placeholder, for each kind of job a run holds: a step's leaf job, a
// child flow's job, and a script that a step dispatched. A step that
// returns the placeholder's text itself gives that text on both. The runs
// register with a job registry that the services are configured with, the
// path that a host's Jobs takes, and the polls go to the registry
// (Services.ResultPoll), as the facade's ResultPollsForAllJobs option asks
// it, also for the dispatched script, whose own polls the dispatcher would
// answer first.
func TestJobPollWholeResults(t *testing.T) {
	large := largeText()
	const placeholder = `"WINDMILL_TOO_BIG"`
	services := &platform.Services{Jobs: platform.NewRegistry()}
	type got struct {
		poll   platform.JobResult
		record string
		err    error
	}
	var mu sync.Mutex
	polled := map[string]got{}
	look := func(name, id string) {
		r, known, err := services.ResultPoll(id)
		if err == nil && !known {
			err = fmt.Errorf("unknown")
		}
		var rec struct {
			Result json.RawMessage `json:"result"`
		}
		if body, rerr := services.GetJob(id); rerr == nil {
			_ = json.Unmarshal(body, &rec)
		}
		mu.Lock()
		polled[name] = got{r, string(rec.Result), err}
		mu.Unlock()
	}
	var e *Engine
	var dispatched string
	exec := &stepFunc{f: func(inv Invocation) (StepOutcome, error) {
		switch inv.Env["WM_FLOW_STEP_ID"] {
		case "big", "inner", "":
			// A step's result, the child flow's last step's, and the
			// dispatched script's.
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(large))}, nil
		case "same":
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(placeholder))}, nil
		case "r":
			var top polledStatus
			body, err := services.GetJob(inv.Env["WM_ROOT_JOB_ID"])
			if err == nil {
				err = json.Unmarshal(body, &top)
			}
			if err != nil {
				return StepOutcome{}, err
			}
			for _, m := range top.FlowStatus.Modules {
				if m.ID == "big" || m.ID == "same" || m.ID == "c" {
					look(m.ID, m.Job)
				}
			}
			// A script dispatched tracked in this run, polled once it
			// has completed.
			d := NewDispatcher(e, nil)
			defer d.Close(context.Background())
			id, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: platform.DispatchScript, Target: "f/t/echo", Args: []byte(`{}`), ParentJob: inv.ID, RootJob: inv.Env["WM_ROOT_JOB_ID"]})
			if err != nil {
				return StepOutcome{}, err
			}
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				if r, ok, _ := d.Result(context.Background(), id); ok && r.Completed {
					break
				}
			}
			dispatched = id
			look("dispatched", id)
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"ok"`))}, nil
	}}
	flows := map[string]string{
		"f/t/main": flowOf(mod("big", "echo", "", ""), mod("same", "echo", "", ""), `{"id":"c","value":{"type":"flow","path":"f/t/sub","input_transforms":{}}}`, mod("r", "echo", "", "")),
		"f/t/sub":  flowOf(mod("inner", "echo", "", "")),
	}
	var err error
	e, err = New(Config{Program: compileFlows(t, flows), Bundle: "sha256:test", Exec: registering{services, exec}, JS: jsEng, Platform: services})
	if err != nil {
		t.Fatal(err)
	}
	tr := runIt(t, e, `{}`, NoHooks{})
	if tr.Status != "success" {
		t.Fatalf("the run: %s %s", tr.Status, tr.Result.Producer())
	}
	if dispatched == "" {
		t.Fatal("step r dispatched no script")
	}
	for _, c := range []struct {
		name, result, record string
	}{
		{"big", large, placeholder},
		{"c", large, placeholder},
		{"dispatched", large, placeholder},
		{"same", placeholder, placeholder},
	} {
		g, ok := polled[c.name]
		switch {
		case !ok:
			t.Errorf("%s: never polled", c.name)
		case g.err != nil || !g.poll.Completed || !g.poll.Success:
			t.Errorf("%s: %+v, %v, want completed with its result", c.name, g.poll, g.err)
		case string(g.poll.Result) != c.result:
			t.Errorf("%s: the poll's result has %d bytes (%.40s), want %d (%.40s)", c.name, len(g.poll.Result), g.poll.Result, len(c.result), c.result)
		case g.record != c.record:
			t.Errorf("%s: the job read's result has %d bytes (%.40s), want %s", c.name, len(g.record), g.record, c.record)
		}
	}
}

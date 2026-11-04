package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// testView renders a job as the id of the run that holds it and the job's
// id, so a test sees which run answered.
type testView struct{ run string }

func (v testView) JobRecord(id string) ([]byte, error) {
	return []byte(fmt.Sprintf(`{"run":%q,"id":%q}`, v.run, id)), nil
}

func (v testView) JobArgs(id string) ([]byte, error) {
	return []byte(fmt.Sprintf(`{"run": %q}`, v.run)), nil
}

func readRun(t *testing.T, body []byte) (run, id string) {
	t.Helper()
	var v struct{ Run, ID string }
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return v.Run, v.ID
}

// The job reads of a live run ([WA-19]) go to the view of the run that
// holds each job: the run's own flow job, a step job registered with only
// its run, while it runs and after it ended, and a flow scope a step's
// variables name. An id the registry never knew answers the API's 404, a
// job of a run that has ended the 501 of deviation D22, and a job no run
// of the runtime holds a NotServed of its own.
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
func TestJobReadsDuringARun(t *testing.T) {
	s := &Services{}
	reg := s.JobRegistry()
	endRun := reg.Begin(Job{ID: "run", Kind: JobFlow, Root: "run", Path: "f/x", Run: "run", View: testView{"run"}})
	env := map[string]string{"WM_FLOW_JOB_ID": "child", "WM_ROOT_FLOW_JOB_ID": "run", "WM_FLOW_PATH": "f/x/forloop-0"}
	endStep := reg.Begin(Job{ID: "step", Kind: JobScript, Root: "run", Env: env, Run: "run"})
	served := func(what, id string) {
		t.Helper()
		for _, read := range []func(string) ([]byte, error){s.GetJob, s.GetJobArgs} {
			body, err := read(id)
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
			if run, _ := readRun(t, body); run != "run" {
				t.Errorf("%s: answered by %q", what, run)
			}
		}
	}
	served("the run", "run")
	served("a running step", "step")
	served("a flow scope a step's variables name", "child")
	endStep()
	served("a step that ended, of a live run", "step")

	var api *Error
	if _, err := s.GetJob("unknown"); !errors.As(err, &api) || api.Variant != "NotFound" || api.Message != "Job not found at name unknown" {
		t.Errorf("an unknown id: %v", err)
	}
	if _, err := s.GetJobArgs("unknown"); !errors.As(err, &api) || api.Variant != "NotFound" || api.Message != "Job Args not found at name unknown" {
		t.Errorf("an unknown id's arguments: %v", err)
	}

	endHost := reg.Begin(Job{ID: "host", Kind: JobFlow, Root: "host"})
	var ns *NotServed
	if _, err := s.GetJob("host"); !errors.As(err, &ns) || errors.Is(err, ErrEndedRun) {
		t.Errorf("a job no run of the runtime holds: %v", err)
	}
	endHost()

	endRun()
	for _, id := range []string{"run", "step", "child"} {
		if _, err := s.GetJob(id); !errors.Is(err, ErrEndedRun) {
			t.Errorf("%s of a run that has ended: %v", id, err)
		}
		if _, err := s.GetJobArgs(id); !errors.Is(err, ErrEndedRun) {
			t.Errorf("%s's arguments, of a run that has ended: %v", id, err)
		}
		// The root lookup still answers for it (D3).
		if root, ok := s.RootJob(id); !ok || root != "run" {
			t.Errorf("%s's root after its run ended: %q %v", id, root, ok)
		}
	}
}

// A run that has ended is released (no new retention): the registry keeps
// its jobs' ids, kinds, roots and paths for the root lookup, and drops the
// reference to the run that a View is, with the job's variables.
func TestEndedRunIsReleased(t *testing.T) {
	r := NewRegistry()
	endRun := r.Begin(Job{ID: "run", Kind: JobFlow, Root: "run", Run: "run", View: testView{"run"}})
	endStep := r.Begin(Job{ID: "step", Kind: JobScript, Root: "run", Env: map[string]string{"WM_FLOW_JOB_ID": "run"}, Run: "run"})
	if j, _, _ := r.Job("step"); j.View == nil {
		t.Fatal("a step of a live run has no view")
	}
	endStep()
	endRun()
	for _, id := range []string{"run", "step"} {
		j, live, ok := r.Job(id)
		if !ok || live || j.View != nil || j.Env != nil {
			t.Errorf("%s after its run ended: %+v live %v known %v", id, j, live, ok)
		}
	}
	if len(r.jobs) != 0 || len(r.trees) != 0 {
		t.Errorf("left %d jobs, %d trees", len(r.jobs), len(r.trees))
	}
}

// A dispatch places a run in another run's tree: the dispatched run's jobs
// are answered by the dispatched run's own view while the root lives, the
// dispatched run having ended included, and the root's jobs by the root's.
func TestJobReadsOfADispatchedRun(t *testing.T) {
	s := &Services{}
	reg := s.JobRegistry()
	endRoot := reg.Begin(Job{ID: "root", Kind: JobFlow, Root: "root", Run: "root", View: testView{"root"}})
	endD := reg.Begin(Job{ID: "d", Kind: JobFlow, Root: "root", Run: "d", View: testView{"d"}})
	endStep := reg.Begin(Job{ID: "d-step", Kind: JobScript, Root: "root", Env: map[string]string{"WM_FLOW_JOB_ID": "d"}, Run: "d"})
	endStep()
	endD()
	for id, want := range map[string]string{"root": "root", "d": "d", "d-step": "d"} {
		body, err := s.GetJob(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if run, _ := readRun(t, body); run != want {
			t.Errorf("%s: answered by %q, want %q", id, run, want)
		}
	}
	endRoot()
	if _, err := s.GetJob("d-step"); !errors.Is(err, ErrEndedRun) {
		t.Errorf("a job of the dispatched run once the root ended: %v", err)
	}
}

// Concurrent runs, each reading its own jobs and the jobs of the others,
// under -race: every id is answered by the run that holds it, while that
// run lives, and by none once it has ended.
func TestJobReadsOfConcurrentRuns(t *testing.T) {
	s := &Services{}
	reg := s.JobRegistry()
	const runs, steps = 8, 50
	started := make(chan struct{})
	var ready, done sync.WaitGroup
	ends := make([]func(), runs)
	for k := range runs {
		run := fmt.Sprintf("run-%d", k)
		ends[k] = reg.Begin(Job{ID: run, Kind: JobFlow, Root: run, Run: run, View: testView{run}})
	}
	ready.Add(runs)
	done.Add(runs)
	errs := make(chan error, runs*steps*runs)
	for k := range runs {
		go func() {
			defer done.Done()
			run := fmt.Sprintf("run-%d", k)
			ready.Done()
			<-started
			for i := range steps {
				id := fmt.Sprintf("%s-step-%d", run, i)
				end := reg.Begin(Job{ID: id, Kind: JobScript, Root: run, Env: map[string]string{"WM_FLOW_JOB_ID": run}, Run: run})
				for o := range runs {
					other := fmt.Sprintf("run-%d", o)
					body, err := s.GetJob(other)
					if err != nil {
						errs <- fmt.Errorf("%s reading %s: %w", run, other, err)
						continue
					}
					var v struct{ Run string }
					if json.Unmarshal(body, &v) != nil || v.Run != other {
						errs <- fmt.Errorf("%s reading %s: answered %s", run, other, body)
					}
				}
				end()
				body, err := s.GetJob(id)
				var v struct{ Run, ID string }
				if err != nil || json.Unmarshal(body, &v) != nil || v.Run != run || v.ID != id {
					errs <- fmt.Errorf("%s's ended step %s: %s %v", run, id, body, err)
				}
			}
		}()
	}
	ready.Wait()
	close(started)
	done.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for k := range runs {
		ends[k]()
	}
	for k := range runs {
		if _, err := s.GetJob(fmt.Sprintf("run-%d-step-%d", k, steps-1)); !errors.Is(err, ErrEndedRun) {
			t.Errorf("run %d's step after the run ended: %v", k, err)
		}
	}
}

// A flow job's user state lists as its flow status holds it ([WA-14]):
// each key with the value as a read gives it back, in jsonb's key order;
// none for a flow job without state, and none for a job that is not a
// live flow job.
//
// [WA-14]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-14
func TestFlowUserStatesList(t *testing.T) {
	ctx := context.Background()
	s := &Services{}
	end := s.JobRegistry().Begin(Job{ID: "run", Kind: JobFlow, Root: "run"})
	if _, ok, err := s.FlowUserStates(ctx, "run"); ok || err != nil {
		t.Errorf("a flow job without state: %v %v", ok, err)
	}
	for k, v := range map[string]string{"bb": `{"b":1,"a":1.50}`, "a": `[1e3]`, "c": `"x"`} {
		if err := s.SetFlowUserState(ctx, "run", k, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.FlowUserStates(ctx, "run")
	if want := `{"a":[1000.0],"c":"x","bb":{"a":1.5,"b":1}}`; !ok || err != nil || string(got) != want {
		t.Errorf("listed %s %v %v, want %s", got, ok, err, want)
	}
	end()
	if _, ok, _ := s.FlowUserStates(ctx, "run"); ok {
		t.Error("a run that has ended still lists its state")
	}
}

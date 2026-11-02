package facade

import (
	"context"
	"testing"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The Reporter the executor hands the step lets go of its session once the
// invocation has settled: a harness keeps it for late output
// (D9), and the session reaches the run's own services. A repeated Settled
// is passed on, as before, and ends and releases nothing again; output
// after settlement still reaches the run's Reporter.
func TestSessionReporterLetsGo(t *testing.T) {
	released := 0
	f := newFacade(t, Config{Recorded: func(string, []Record) { released++ }})
	rep := &lateRecorder{}
	var kept engine.Reporter
	x := &fakeExec{out: engine.StepOutcome{Kind: engine.OutcomeOK, Body: jsonval.FromProducer([]byte(`1`))}, do: func(inv engine.Invocation) {
		kept = inv.Report
		inv.Report.Settled(engine.SettleCompleted)
	}}
	if _, err := f.Executor(x).Execute(context.Background(), engine.Invocation{ID: "inv-a", Run: "run-a", Env: env("job-a", "root-a", "tok-a"), Report: rep}); err != nil {
		t.Fatal(err)
	}
	// fakeExec settles once more after do: a repeated Settled.
	kept.Settled(engine.SettleCompleted)
	if len(rep.settled) != 3 {
		t.Errorf("the run's Reporter heard Settled %d times, want every call passed on (3)", len(rep.settled))
	}
	if released != 1 {
		t.Errorf("the record was released %d times, want once", released)
	}
	sr := kept.(*sessionReporter)
	sr.mu.Lock()
	s, settled := sr.s, sr.settled
	sr.mu.Unlock()
	if s != nil || settled != nil {
		t.Error("the settled invocation's Reporter still holds its session")
	}
	if code, _ := get(t, f.FreshOrigin()+"/api/w/ws/jobs_u/get_root_job_id/job-a", "tok-a"); code != 401 {
		t.Errorf("token after settlement: %d", code)
	}
	kept.Late(engine.Late{Kind: engine.LateLog, Body: jsonval.FromProducer([]byte(`["after"]`))})
	if rep.lates != 1 {
		t.Errorf("late output after settlement reached the run's Reporter %d times, want once", rep.lates)
	}
}

// lateRecorder is a settleRecorder that counts late output too.
type lateRecorder struct {
	settleRecorder
	lates int
}

func (r *lateRecorder) Late(engine.Late) { r.lates++ }

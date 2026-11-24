package facade

import (
	"fmt"
	"testing"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// recordsOf renders each job's record as given, as the run that holds the
// jobs renders them (internal/engine's job records), or fails as the run
// fails for a job whose record windmill-lite does not reproduce.
type recordsOf map[string]struct {
	record string
	err    error
}

func (v recordsOf) JobRecord(id string) ([]byte, error) {
	r, ok := v[id]
	if !ok {
		return nil, &platform.NotServed{Reason: "the job's run does not hold it"}
	}
	return []byte(r.record), r.err
}

func (v recordsOf) JobArgs(string) ([]byte, error) { return []byte("{}"), nil }

// TestResultPollsFlowJobs checks the result polls ([WA-15], [WA-V2]) of
// the jobs of a live flow run that no step dispatched, with
// Config.ResultPollsForAllJobs and without it. The run holds a completed
// step, a sub-flow's flow job that failed, a running sibling and a job
// pushed for later, whose records are the engine's, and a canceled leaf
// and a latched flow job, whose records it does not serve.
//
//   - Without the option, today's answers stand: each job of the run
//     answers 501 on both routes.
//   - With it, the answers are the ones the oracle recorded (fixture
//     conformance/flows/client-api/result-polls): a completed job's
//     success and its result, embedded in jsonb's text output as the
//     record holds it, and get_result's 200 with that text; the unfinished
//     answer for a job that has not completed, started only when asked
//     for, true once it runs, and get_result's 404.
//   - In both, the canceled leaf and the latched flow job answer 501 (with
//     the option, naming the reason), and so does a job of the run once
//     the run has ended (platform.ErrEndedRun); an id with no job, and a
//     job of the run looked up in another workspace, answer as for an id
//     with no job row.
//
// [WA-15]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-15
// [WA-V2]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-v2
func TestResultPollsFlowJobs(t *testing.T) {
	const (
		maybeRoute  = "GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}"
		resultRoute = "GET /w/{ws}/jobs_u/completed/get_result/{id}"
		unfinished  = `{"started":null,"success":null,"completed":false,"result":null}`
	)
	run, a, c, w, s, x, l := uid("run"), uid("a"), uid("c"), uid("w"), uid("s"), uid("x"), uid("l")
	views := recordsOf{
		a: {record: fmt.Sprintf(`{"type":"CompletedJob","id":%q,"parent_job":%q,"canceled":false,"success":true,"result":{"k": [1, 2.5]},"resolved":false,"args":{},"is_flow_step":true}`, a, run)},
		c: {record: fmt.Sprintf(`{"type":"CompletedJob","id":%q,"parent_job":%q,"canceled":false,"success":false,"result":{"error": {"name": "Error", "message": "e <&>", "step_id": "x"}},"resolved":false,"args":{},"script_path":"f/x/fails","is_flow_step":true,"flow_status":{}}`, c, run)},
		w: {record: fmt.Sprintf(`{"type":"QueuedJob","id":%q,"parent_job":%q,"root_job":%q,"running":true,"flow_step_id":"w","canceled":false,"args":{},"is_flow_step":true}`, w, run, run)},
		s: {record: fmt.Sprintf(`{"type":"QueuedJob","id":%q,"parent_job":%q,"root_job":%q,"running":false,"flow_step_id":"s","canceled":false,"args":{},"is_flow_step":true}`, s, run, run)},
		x: {err: &platform.NotServed{Reason: "the job was canceled"}},
		l: {err: &platform.NotServed{Reason: "the flow job ended latched"}},
	}
	type answer struct {
		status int
		typ    string
		body   string
	}
	json := func(body string) answer { return answer{200, "application/json", body} }
	notFound := func(id string) answer {
		return answer{404, "text/plain", "Not found: Completed Job not found at name " + id}
	}
	refused := func(route, reason string) answer {
		if reason != "" {
			route += ": " + reason
		}
		return answer{501, "text/plain", UnsupportedPrefix + route}
	}
	type poll struct {
		what  string
		path  string
		route string
		on    answer
		both  bool
	}
	unknown := "00000000-0000-4000-8000-000000000000"
	polls := []poll{
		{what: "a completed step", path: "get_result_maybe/" + a, route: maybeRoute, on: json(`{"started":true,"success":true,"completed":true,"result":{"k": [1, 2.5]}}`)},
		{what: "a completed step, started asked for", path: "get_result_maybe/" + a + "?get_started=true", route: maybeRoute, on: json(`{"started":true,"success":true,"completed":true,"result":{"k": [1, 2.5]}}`)},
		{what: "a completed step's result", path: "get_result/" + a, route: resultRoute, on: json(`{"k": [1, 2.5]}`)},
		{what: "a failed flow job", path: "get_result_maybe/" + c, route: maybeRoute, on: json(`{"started":true,"success":false,"completed":true,"result":{"error": {"name": "Error", "message": "e <&>", "step_id": "x"}}}`)},
		{what: "a failed flow job's result", path: "get_result/" + c, route: resultRoute, on: json(`{"error": {"name": "Error", "message": "e <&>", "step_id": "x"}}`)},
		{what: "a running sibling", path: "get_result_maybe/" + w, route: maybeRoute, on: json(unfinished)},
		{what: "a running sibling, started asked for", path: "get_result_maybe/" + w + "?get_started=true", route: maybeRoute, on: json(`{"started":true,"success":null,"completed":false,"result":null}`)},
		{what: "a running sibling's result", path: "get_result/" + w, route: resultRoute, on: notFound(w)},
		{what: "a job pushed for later, started asked for", path: "get_result_maybe/" + s + "?get_started=true", route: maybeRoute, on: json(`{"started":false,"success":null,"completed":false,"result":null}`)},
		{what: "a canceled leaf", path: "get_result_maybe/" + x, route: maybeRoute, on: refused(maybeRoute, "the job was canceled")},
		{what: "a canceled leaf's result", path: "get_result/" + x, route: resultRoute, on: refused(resultRoute, "the job was canceled")},
		{what: "a latched flow job", path: "get_result_maybe/" + l, route: maybeRoute, on: refused(maybeRoute, "the flow job ended latched")},
		{what: "a latched flow job's result", path: "get_result/" + l, route: resultRoute, on: refused(resultRoute, "the flow job ended latched")},
		{what: "an id with no job", path: "get_result_maybe/" + unknown + "?get_started=true", both: true, on: json(`{"started":false,"success":null,"completed":false,"result":null}`)},
		{what: "an id with no job's result", path: "get_result/" + unknown, both: true, on: notFound(unknown)},
	}
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("option %t", on), func(t *testing.T) {
			services := &platform.Services{}
			f := newFacade(t, Config{Platform: services, ResultPollsForAllJobs: on})
			reg := services.JobRegistry()
			endRun := reg.Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run, Path: "f/x/main", Run: run, View: views})
			for _, id := range []string{a, w, s, x} {
				reg.Begin(platform.Job{ID: id, Kind: platform.JobScript, Root: run, Run: run})()
			}
			for _, id := range []string{c, l} {
				reg.Begin(platform.Job{ID: id, Kind: platform.JobFlow, Root: run, Path: "f/x/fails", Run: run})()
			}
			d := f.Register("inv-d", run, env("d", "run", "tok-d"))
			defer d.End()
			origin := f.WarmOrigin()
			ask := func(ws, path string) answer {
				rep := intercept(f, "inv-d", "GET", origin+"/api/w/"+ws+"/jobs_u/completed/"+path, nil, nil)
				return answer{rep.Status, rep.Headers["content-type"], string(rep.Body)}
			}
			for _, p := range polls {
				want := p.on
				if !on && !p.both {
					want = refused(p.route, "")
				}
				if got := ask("ws", p.path); got != want {
					t.Errorf("%s: %+v, want %+v", p.what, got, want)
				}
			}
			// A job of the run, looked up in another workspace, is one
			// with no job row there.
			if got, want := ask("other", "get_result_maybe/"+a), json(unfinished); got != want {
				t.Errorf("a completed step in another workspace: %+v, want %+v", got, want)
			}
			if got, want := ask("other", "get_result/"+a), notFound(a); got != want {
				t.Errorf("a completed step's result in another workspace: %+v, want %+v", got, want)
			}
			// Once the run has ended, its jobs answer 501
			// (platform.ErrEndedRun).
			endRun()
			reason := ""
			if on {
				reason = platform.ErrEndedRun.Reason
			}
			for _, r := range []struct{ path, route string }{{"get_result_maybe/" + a, maybeRoute}, {"get_result/" + a, resultRoute}} {
				if got, want := ask("ws", r.path), refused(r.route, reason); got != want {
					t.Errorf("a job of a run that has ended, %s: %+v, want %+v", r.path, got, want)
				}
			}
		})
	}
}

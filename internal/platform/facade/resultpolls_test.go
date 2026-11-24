package facade

import (
	"fmt"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// pollsOf answers the result polls of each job as given, as the run that
// holds the jobs answers them (platform.JobView.JobPoll), or fails as the run
// fails for a job whose record windmill-lite does not reproduce.
type pollsOf map[string]struct {
	res platform.JobResult
	err error
}

func (v pollsOf) JobPoll(j platform.Job) (platform.JobResult, error) {
	r, ok := v[j.ID]
	if !ok {
		return platform.JobResult{}, &platform.NotServed{Reason: "the job's run does not hold it"}
	}
	return r.res, r.err
}

func (v pollsOf) JobRecord(platform.Job) ([]byte, error) { return []byte("{}"), nil }

func (v pollsOf) JobArgs(string) ([]byte, error) { return []byte("{}"), nil }

// TestResultPollsFlowJobs checks the facade's answers to the result polls
// ([WA-15], [WA-V2]) of the jobs of a live flow run that no step
// dispatched, with Config.ResultPollsForAllJobs and without it, given the
// states that the run reports: a completed step, a sub-flow's flow job
// that failed, a running sibling, a job pushed for later, and two jobs
// whose run refuses them. The engine's own states, a canceled leaf and a
// canceled child flow among them, and a large result, are checked end to
// end by wl's TestResultPollsRealStates.
//
//   - Without the option, today's answers stand: each job of the run
//     answers 501 on both routes.
//   - With it, the answers are the ones the oracle recorded (fixture
//     conformance/flows/client-api/result-polls): a completed job's
//     success and its result, embedded in jsonb's text output as the run
//     gives it, and get_result's 200 with that text; the unfinished answer
//     for a job that has not completed, started only when asked for, true
//     once it runs, and get_result's 404.
//   - In both, a refused job answers 501 (with the option, naming the
//     run's reason), and so does a job of the run once the run has ended
//     (platform.ErrEndedRun); an id with no job, and a job of the run
//     looked up in another workspace, answer as for an id with no job row.
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
	views := pollsOf{
		a: {res: platform.JobResult{Started: true, Completed: true, Success: true, Result: []byte(`{"k": [1, 2.5]}`)}},
		c: {res: platform.JobResult{Started: true, Completed: true, Result: []byte(`{"error": {"name": "Error", "message": "e <&>", "step_id": "x"}}`)}},
		w: {res: platform.JobResult{Started: true}},
		s: {res: platform.JobResult{}},
		x: {err: &platform.NotServed{Reason: "the job was canceled"}},
		l: {err: &platform.NotServed{Reason: "the flow job ended canceled"}},
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
		{what: "a job the run refuses", path: "get_result_maybe/" + x, route: maybeRoute, on: refused(maybeRoute, "the job was canceled")},
		{what: "a job the run refuses, its result", path: "get_result/" + x, route: resultRoute, on: refused(resultRoute, "the job was canceled")},
		{what: "a flow job the run refuses", path: "get_result_maybe/" + l, route: maybeRoute, on: refused(maybeRoute, "the flow job ended canceled")},
		{what: "a flow job the run refuses, its result", path: "get_result/" + l, route: resultRoute, on: refused(resultRoute, "the flow job ended canceled")},
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

// shiftingJobs is a job registry whose answer for one id changes after its
// first lookup of that id, as the local registry's does when it forgets the
// oldest ended job or registers a job between two lookups. Its root is
// live and holds the job's run, which answers the polls as views does.
type shiftingJobs struct {
	mu           sync.Mutex
	id, root     string
	views        pollsOf
	known, later bool // whether the id is known at its first lookup, and after
	lookups      int
}

func (s *shiftingJobs) Begin(platform.Job) func() { return func() {} }

func (s *shiftingJobs) Job(id string) (platform.Job, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch id {
	case s.root:
		return platform.Job{ID: s.root, Kind: platform.JobFlow, Root: s.root, Run: s.root, View: s.views}, true, true
	case s.id:
		s.lookups++
		if (s.lookups == 1 && s.known) || (s.lookups > 1 && s.later) {
			return platform.Job{ID: s.id, Kind: platform.JobScript, Root: s.root, Run: s.root, View: s.views}, true, true
		}
	}
	return platform.Job{}, false, false
}

// TestResultPollsOneLookup checks that, with Config.ResultPollsForAllJobs,
// one poll classifies its job against one state of the job registry: the
// registry is asked for the id once, and the answer follows that one
// lookup.
//
//   - A job known at the lookup and forgotten right after answers from its
//     run, completed with its result, and not with the job read's 404.
//   - An id unknown at the lookup and registered right after answers as an
//     id with no job: the unfinished answer, or get_result's 404, and not
//     the default 501 of a known job that no step dispatched.
func TestResultPollsOneLookup(t *testing.T) {
	root, id := uid("run"), uid("a")
	views := pollsOf{id: {res: platform.JobResult{Started: true, Completed: true, Success: true, Result: []byte(`{"k": [1, 2.5]}`)}}}
	for _, c := range []struct {
		what         string
		known, later bool
		path, want   string
		status       int
	}{
		{"known, then forgotten", true, false, "get_result_maybe/" + id, `{"started":true,"success":true,"completed":true,"result":{"k": [1, 2.5]}}`, 200},
		{"known, then forgotten, its result", true, false, "get_result/" + id, `{"k": [1, 2.5]}`, 200},
		{"unknown, then registered", false, true, "get_result_maybe/" + id + "?get_started=true", `{"started":false,"success":null,"completed":false,"result":null}`, 200},
		{"unknown, then registered, its result", false, true, "get_result/" + id, "Not found: Completed Job not found at name " + id, 404},
	} {
		jobs := &shiftingJobs{id: id, root: root, views: views, known: c.known, later: c.later}
		f := newFacade(t, Config{Platform: &platform.Services{Jobs: jobs}, ResultPollsForAllJobs: true})
		d := f.Register("inv-d", root, env("d", "run", "tok-d"))
		rep := intercept(f, "inv-d", "GET", f.WarmOrigin()+"/api/w/ws/jobs_u/completed/"+c.path, nil, nil)
		d.End()
		if rep.Status != c.status || string(rep.Body) != c.want {
			t.Errorf("%s: %d %s, want %d %s", c.what, rep.Status, rep.Body, c.status, c.want)
		}
		if jobs.lookups != 1 {
			t.Errorf("%s: the registry was asked for the id %d times, want once", c.what, jobs.lookups)
		}
	}
}

// reusedRoot is a job registry that keeps one root id after its run has
// ended, without a view, and registers a new live run under the same id
// right after its first lookup of that id.
type reusedRoot struct {
	mu      sync.Mutex
	id      string
	views   pollsOf
	lookups int
}

func (r *reusedRoot) Begin(platform.Job) func() { return func() {} }

func (r *reusedRoot) Job(id string) (platform.Job, bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.id {
		return platform.Job{}, false, false
	}
	r.lookups++
	if r.lookups == 1 {
		return platform.Job{ID: id, Kind: platform.JobFlow, Root: id}, false, true
	}
	return platform.Job{ID: id, Kind: platform.JobFlow, Root: id, Run: id, View: r.views}, true, true
}

// TestResultPollsReusedRoot checks one poll of a job that is its own root,
// with Config.ResultPollsForAllJobs: the registry's one lookup finds the
// root ended, so the poll answers the ended run's 501, though a new run
// takes the id right after; the id is looked up once.
func TestResultPollsReusedRoot(t *testing.T) {
	id := uid("run")
	views := pollsOf{id: {res: platform.JobResult{Started: true}}}
	for _, route := range []string{"get_result_maybe", "get_result"} {
		jobs := &reusedRoot{id: id, views: views}
		f := newFacade(t, Config{Platform: &platform.Services{Jobs: jobs}, ResultPollsForAllJobs: true})
		d := f.Register("inv-d", id, env("d", "run", "tok-d"))
		rep := intercept(f, "inv-d", "GET", f.WarmOrigin()+"/api/w/ws/jobs_u/completed/"+route+"/"+id, nil, nil)
		d.End()
		want := UnsupportedPrefix + "GET /w/{ws}/jobs_u/completed/" + route + "/{id}: " + platform.ErrEndedRun.Reason
		if rep.Status != 501 || string(rep.Body) != want {
			t.Errorf("%s: %d %s, want 501 %s", route, rep.Status, rep.Body, want)
		}
		if jobs.lookups != 1 {
			t.Errorf("%s: the registry was asked for the id %d times, want once", route, jobs.lookups)
		}
	}
}

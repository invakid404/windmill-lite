package facade

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// changingJobs is a job registry whose answer for the job id changes after
// its first lookup of it, as it would if the job's run ended and a run of
// another workspace registered the id again: the first lookup gives first
// (unknown when firstKnown is false), each later one then. Every other id
// is a live job of roots.
type changingJobs struct {
	id          string
	first, then platform.Job
	firstKnown  bool
	roots       map[string]platform.Job
	mu          sync.Mutex
	lookups     int
}

func (j *changingJobs) Begin(platform.Job) func() { return func() {} }

func (j *changingJobs) Job(id string) (platform.Job, bool, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if id != j.id {
		r, ok := j.roots[id]
		return r, ok, ok
	}
	j.lookups++
	if j.lookups == 1 {
		return j.first, j.firstKnown, j.firstKnown
	}
	return j.then, true, true
}

// namedView renders every job as its name.
type namedView string

func (v namedView) JobRecord(platform.Job) ([]byte, error) {
	return []byte(`{"view":"` + string(v) + `"}`), nil
}
func (v namedView) JobArgs(string) ([]byte, error) {
	return []byte(`{"args":"` + string(v) + `"}`), nil
}

// JobPoll answers a completed job whose result is the view's name.
func (v namedView) JobPoll(string) (platform.JobResult, error) {
	return platform.JobResult{Started: true, Completed: true, Success: true, Result: []byte(`"` + string(v) + `"`)}, nil
}

// mapStates is a flow user state store over a map, by job and key.
type mapStates map[string]json.RawMessage

func (m mapStates) UserState(_ context.Context, job, key string) (json.RawMessage, bool, error) {
	v, ok := m[job+"/"+key]
	return v, ok, nil
}

func (m mapStates) SetUserState(_ context.Context, job, key string, v json.RawMessage) error {
	m[job+"/"+key] = v
	return nil
}

// A job lookup checks the job's workspace on the registry entry that it
// answers from: when the id's entry changes from unknown, or from a job of
// the caller's workspace, to a job of another workspace between two
// lookups, the root lookup, the job reads and the flow user state read
// never answer with the other workspace's job. The job reads answer from
// the entry they checked. The state read, through a host's store (the
// value under the key the store receives, the job's id), gives the
// caller's value while its registration stays, and null once it finds the
// id registered again.
func TestJobLookupsAgainstARegistrationChange(t *testing.T) {
	const job, localRoot, otherRoot = "00000000-0000-4000-8000-0000000000c1", "00000000-0000-4000-8000-0000000000c2", "00000000-0000-4000-8000-0000000000c3"
	local := platform.Job{ID: job, Kind: platform.JobFlow, Root: localRoot, Run: localRoot, View: namedView("local"), Host: platform.Host{Workspace: "ws-a"}}
	other := platform.Job{ID: job, Kind: platform.JobFlow, Root: otherRoot, Run: otherRoot, View: namedView("other"), Host: platform.Host{Workspace: "ws-b"}}
	jobs := &changingJobs{id: job, roots: map[string]platform.Job{
		localRoot: {ID: localRoot, Kind: platform.JobFlow, Root: localRoot, Host: platform.Host{Workspace: "ws-a"}},
		otherRoot: {ID: otherRoot, Kind: platform.JobFlow, Root: otherRoot, Host: platform.Host{Workspace: "ws-b"}},
	}}
	states := mapStates{job + "/k": json.RawMessage(`"local's"`)}
	f := newFacade(t, Config{Platform: &platform.Services{Jobs: jobs, UserState: states}})
	s := f.register("inv-a", localRoot, map[string]string{"WM_JOB_ID": "00000000-0000-4000-8000-0000000000c4", "WM_ROOT_JOB_ID": localRoot, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws-a"}, nil, &platform.Host{Workspace: "ws-a"})
	defer s.End()
	for _, tc := range []struct {
		name        string
		first, then platform.Job
		firstKnown  bool
		want        map[string]string
	}{
		{"unknown, then of another workspace", platform.Job{}, other, false, map[string]string{
			"jobs_u/get_root_job_id/": "400 SqlErr: " + noRows,
			"jobs_u/get/":             "404 Not found: Job not found at name " + job,
			"jobs_u/get_args/":        "404 Not found: Job Args not found at name " + job,
			"jobs/flow/user_states/":  "200 null",
		}},
		{"of the caller's workspace, then of another", local, other, true, map[string]string{
			"jobs_u/get_root_job_id/": `200 "` + localRoot + `"`,
			"jobs_u/get/":             `200 {"view":"local"}`,
			"jobs_u/get_args/":        `200 {"args":"local"}`,
			// The read finds the id registered again when it looks again.
			"jobs/flow/user_states/": "200 null",
		}},
		{"of the caller's workspace throughout", local, local, true, map[string]string{
			"jobs/flow/user_states/": `200 "local's"`,
		}},
	} {
		for route, want := range tc.want {
			jobs.mu.Lock()
			jobs.first, jobs.then, jobs.firstKnown, jobs.lookups = tc.first, tc.then, tc.firstKnown, 0
			jobs.mu.Unlock()
			path := "http://x/api/w/ws-a/" + route + job
			if strings.HasPrefix(route, "jobs/flow") {
				path += "/k"
			}
			rep := intercept(f, "inv-a", "GET", path, nil, nil)
			if got := strings.TrimSpace(strconv.Itoa(rep.Status) + " " + string(rep.Body)); got != want {
				t.Errorf("%s: %s: %s, want %s", tc.name, route, got, want)
			}
		}
	}
}

// A flow user state write answers a flow job of another workspace as it
// answers an unknown id, in the same order: a value that the store
// refuses gets the same 400 first, and a value that it takes the same 404.
func TestStateWriteOfAnotherWorkspace(t *testing.T) {
	services := &platform.Services{}
	f := newFacade(t, Config{Platform: services})
	const mine, theirs, unknown = "00000000-0000-4000-8000-0000000000d1", "00000000-0000-4000-8000-0000000000d2", "00000000-0000-4000-8000-0000000000d3"
	jobs := services.JobRegistry()
	defer jobs.Begin(platform.Job{ID: mine, Kind: platform.JobFlow, Run: mine, Host: platform.Host{Workspace: "ws-a"}})()
	defer jobs.Begin(platform.Job{ID: theirs, Kind: platform.JobFlow, Run: theirs, Host: platform.Host{Workspace: "ws-b"}})()
	s := f.register("inv-a", mine, map[string]string{"WM_JOB_ID": "00000000-0000-4000-8000-0000000000d4", "WM_ROOT_JOB_ID": mine, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws-a"}, nil, &platform.Host{Workspace: "ws-a"})
	defer s.End()
	for _, body := range []string{`"a\u0000b"`, `1`} {
		write := func(id string) string {
			rep := intercept(f, "inv-a", "POST", "http://x/api/w/ws-a/jobs/flow/user_states/"+id+"/k", map[string]string{"content-type": "application/json"}, []byte(body))
			return strconv.Itoa(rep.Status) + " " + string(rep.Body)
		}
		got, want := write(theirs), write(unknown)
		if got != want || !strings.HasPrefix(want, map[string]string{`"a\u0000b"`: "400 ", `1`: "404 "}[body]) {
			t.Errorf("write %s: another workspace's flow job %s, an unknown id %s", body, got, want)
		}
	}
	if rep := intercept(f, "inv-a", "POST", "http://x/api/w/ws-a/jobs/flow/user_states/"+mine+"/k", map[string]string{"content-type": "application/json"}, []byte(`1`)); rep.Status != 200 {
		t.Errorf("write to its own flow job: %d %s", rep.Status, rep.Body)
	}
}

// A resource read with the job_id of a live job of another workspace
// answers 404, as for an unknown id: a flow job (which, of the caller's
// workspace, answers 501), and a script job whose WM_WORKSPACE a hook set
// to the caller's. A read that needs no job variables answers as without
// the job_id. That script job's own workspace reads it through its own
// path, whatever its WM_WORKSPACE says.
func TestReadWithAJobIDOfAnotherWorkspace(t *testing.T) {
	store := platform.NewStore()
	for path, v := range map[string]string{"u/x/plain": `{"a":1}`, "u/x/ctx": `{"j":"$WM_JOB_ID"}`} {
		if err := store.SetResource(path, platform.Resource{Type: "object", Value: json.RawMessage(v)}); err != nil {
			t.Fatal(err)
		}
	}
	services := &platform.Services{Resources: store}
	f := newFacade(t, Config{Platform: services})
	const runA, runB, stepB = "00000000-0000-4000-8000-0000000000e1", "00000000-0000-4000-8000-0000000000e2", "00000000-0000-4000-8000-0000000000e3"
	jobs := services.JobRegistry()
	defer jobs.Begin(platform.Job{ID: runA, Kind: platform.JobFlow, Run: runA, Host: platform.Host{Workspace: "ws-a"}})()
	defer jobs.Begin(platform.Job{ID: runB, Kind: platform.JobFlow, Run: runB, Host: platform.Host{Workspace: "ws-b"}})()
	a := f.register("inv-a", runA, map[string]string{"WM_JOB_ID": "00000000-0000-4000-8000-0000000000e4", "WM_FLOW_JOB_ID": runA, "WM_ROOT_JOB_ID": runA, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws-a"}, nil, &platform.Host{Workspace: "ws-a"})
	defer a.End()
	// A step of ws-b whose hook set its WM_WORKSPACE to ws-a.
	b := f.register("inv-b", runB, map[string]string{"WM_JOB_ID": stepB, "WM_FLOW_JOB_ID": runB, "WM_ROOT_JOB_ID": runB, "WM_TOKEN": "tok-b", "WM_WORKSPACE": "ws-a"}, nil, &platform.Host{Workspace: "ws-b"})
	defer b.End()
	read := func(path, id string) string {
		rep := intercept(f, "inv-a", "GET", "http://x/api/w/ws-a/resources/get_value_interpolated/"+path+"?job_id="+id, nil, nil)
		return strconv.Itoa(rep.Status) + " " + string(rep.Body)
	}
	for _, id := range []string{runB, stepB} {
		if got, want := read("u/x/ctx", id), "404 Not found: Job not found at name "+id; got != want {
			t.Errorf("u/x/ctx with ws-b's job %s: %s, want %s", id, got, want)
		}
		if got := read("u/x/plain", id); got != `200 {"a":1}` {
			t.Errorf("u/x/plain with ws-b's job %s: %s", id, got)
		}
	}
	if got := read("u/x/ctx", runA); !strings.HasPrefix(got, "501 ") {
		t.Errorf("u/x/ctx with its own flow job: %s, want 501", got)
	}
	rep := intercept(f, "inv-b", "GET", "http://x/api/w/ws-b/resources/get_value_interpolated/u/x/ctx?job_id="+stepB, nil, nil)
	if got, want := strconv.Itoa(rep.Status)+" "+string(rep.Body), `200 {"j":"`+stepB+`"}`; got != want {
		t.Errorf("ws-b's read with its own job, through ws-b: %s, want %s", got, want)
	}
}

// listingView renders a job's record with its flow user state, which it
// lists through the services as the engine's run view does.
type listingView struct{ s *platform.Services }

func (v listingView) JobRecord(j platform.Job) ([]byte, error) {
	states, ok, err := v.s.FlowUserStatesOf(context.Background(), j)
	if err != nil {
		return nil, err
	}
	if !ok {
		states = json.RawMessage("null")
	}
	return []byte(`{"view":"local","states":` + string(states) + `}`), nil
}

func (listingView) JobArgs(string) ([]byte, error) { return []byte(`{}`), nil }
func (listingView) JobPoll(string) (platform.JobResult, error) {
	return platform.JobResult{Started: true}, nil
}

// listingStates is mapStates that lists a job's map.
type listingStates struct{ mapStates }

func (m listingStates) UserStates(_ context.Context, job string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for k, v := range m.mapStates {
		if key, ok := strings.CutPrefix(k, job+"/"); ok {
			out[key] = v
		}
	}
	return out, nil
}

// A job record lists the flow user state of the registration that the
// read checked: when the id ends and another registration takes it between
// the view's choice and the state's listing, the record lists none of the
// other registration's state. So it is, with a host's store, for a run of
// another workspace, and with the services' own store and a registry that
// numbers no registration, for the id passing from A to B and back to A.
func TestJobRecordAgainstARegistrationChange(t *testing.T) {
	const job, localRoot, otherRoot = "00000000-0000-4000-8000-0000000000f1", "00000000-0000-4000-8000-0000000000f2", "00000000-0000-4000-8000-0000000000f3"
	roots := map[string]platform.Job{
		localRoot: {ID: localRoot, Kind: platform.JobFlow, Root: localRoot, Host: platform.Host{Workspace: "ws-a"}},
		otherRoot: {ID: otherRoot, Kind: platform.JobFlow, Root: otherRoot, Host: platform.Host{Workspace: "ws-b"}},
	}
	read := func(t *testing.T, services *platform.Services) string {
		t.Helper()
		f := newFacade(t, Config{Platform: services})
		s := f.register("inv-a", localRoot, map[string]string{"WM_JOB_ID": "00000000-0000-4000-8000-0000000000f4", "WM_ROOT_JOB_ID": localRoot, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws-a"}, nil, &platform.Host{Workspace: "ws-a"})
		defer s.End()
		rep := intercept(f, "inv-a", "GET", "http://x/api/w/ws-a/jobs_u/get/"+job, nil, nil)
		return strconv.Itoa(rep.Status) + " " + string(rep.Body)
	}
	t.Run("a host's store", func(t *testing.T) {
		services := &platform.Services{}
		view := listingView{services}
		local := platform.Job{ID: job, Kind: platform.JobFlow, Root: localRoot, Run: localRoot, View: view, Host: platform.Host{Workspace: "ws-a"}, Gen: 1}
		other := platform.Job{ID: job, Kind: platform.JobFlow, Root: otherRoot, Run: otherRoot, View: view, Host: platform.Host{Workspace: "ws-b"}, Gen: 2}
		services.Jobs = &changingJobs{id: job, first: local, firstKnown: true, then: other, roots: roots}
		// The other registration's value, under the key a host's store
		// receives, the job's id.
		services.UserState = listingStates{mapStates{job + "/k": json.RawMessage(`"other's"`)}}
		if got, want := read(t, services), `200 {"view":"local","states":null}`; got != want {
			t.Errorf("the job record: %s, want %s", got, want)
		}
	})
	t.Run("the services' store, unnumbered", func(t *testing.T) {
		services := &platform.Services{}
		view := listingView{services}
		a := platform.Job{ID: job, Kind: platform.JobFlow, Root: localRoot, Run: localRoot, View: view, Host: platform.Host{Workspace: "ws-a"}}
		b := platform.Job{ID: job, Kind: platform.JobFlow, Root: otherRoot, Run: otherRoot, View: view, Host: platform.Host{Workspace: "ws-b"}}
		jobs := &sequenceJobs{id: job, roots: roots}
		states := platform.NewUserStates(jobs)
		services.Jobs, services.UserState = jobs, states
		// B writes its value while it holds the id.
		jobs.set(b)
		if err := services.SetFlowUserStateIn(context.Background(), job, "k", []byte(`"b's"`), "ws-b"); err != nil {
			t.Fatal(err)
		}
		// The read checks A, B holds the id when the state is listed, and
		// A again after.
		jobs.set(a, b, a)
		if got, want := read(t, services), `200 {"view":"local","states":null}`; got != want {
			t.Errorf("the job record: %s, want %s", got, want)
		}
	})
}

// sequenceJobs answers each lookup of id with the next of its answers, the
// last one from then on; every other id is one of roots.
type sequenceJobs struct {
	id      string
	roots   map[string]platform.Job
	mu      sync.Mutex
	answers []platform.Job
	n       int
}

func (j *sequenceJobs) Begin(platform.Job) func() { return func() {} }

func (j *sequenceJobs) Job(id string) (platform.Job, bool, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if id != j.id {
		r, ok := j.roots[id]
		return r, ok, ok
	}
	if len(j.answers) == 0 {
		return platform.Job{}, false, false
	}
	a := j.answers[min(j.n, len(j.answers)-1)]
	j.n++
	return a, true, true
}

func (j *sequenceJobs) set(answers ...platform.Job) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.answers, j.n = answers, 0
}

// A step whose hook gave it the id of a live job of another workspace as
// its WM_JOB_ID registers under its own invocation id: the job that holds
// the id stays that workspace's, whose reads find it as before, and the
// step's workspace reads it as an unknown id.
func TestHookedJobIDOfAnotherWorkspace(t *testing.T) {
	services := &platform.Services{}
	f := newFacade(t, Config{Platform: services})
	const runA, runB = "00000000-0000-4000-8000-0000000000b1", "00000000-0000-4000-8000-0000000000b2"
	jobs := services.JobRegistry()
	defer jobs.Begin(platform.Job{ID: runA, Kind: platform.JobFlow, Run: runA, View: namedView("a"), Host: platform.Host{Workspace: "ws-a"}})()
	defer jobs.Begin(platform.Job{ID: runB, Kind: platform.JobFlow, Run: runB, View: namedView("b"), Host: platform.Host{}})()
	a := f.register("00000000-0000-4000-8000-0000000000b3", runA, map[string]string{"WM_JOB_ID": runB, "WM_ROOT_JOB_ID": runA, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws-a"}, nil, &platform.Host{Workspace: "ws-a"})
	defer a.End()
	b := f.register("00000000-0000-4000-8000-0000000000b4", runB, map[string]string{"WM_JOB_ID": "00000000-0000-4000-8000-0000000000b5", "WM_ROOT_JOB_ID": runB, "WM_TOKEN": "tok-b", "WM_WORKSPACE": "windmill-lite"}, nil, &platform.Host{})
	defer b.End()
	for _, tc := range []struct{ inv, ws, id, want string }{
		{"00000000-0000-4000-8000-0000000000b3", "ws-a", runB, "404 Not found: Job Args not found at name " + runB},
		{"00000000-0000-4000-8000-0000000000b4", "windmill-lite", runB, `200 {"args":"b"}`},
		{"00000000-0000-4000-8000-0000000000b3", "ws-a", "00000000-0000-4000-8000-0000000000b3", `200 {"args":"a"}`},
	} {
		rep := intercept(f, tc.inv, "GET", "http://x/api/w/"+tc.ws+"/jobs_u/get_args/"+tc.id, nil, nil)
		if got := strconv.Itoa(rep.Status) + " " + string(rep.Body); got != tc.want {
			t.Errorf("%s reads %s: %s, want %s", tc.ws, tc.id, got, tc.want)
		}
	}
	if scope, ok := services.JobScope(runB); !ok || scope != platform.DefaultHost.Workspace {
		t.Errorf("B's run is in %q, %v; want windmill-lite", scope, ok)
	}
}

// endingView renders a job whose run ends while the read renders it, as the
// engine's run view answers then: ErrEndedRun.
type endingView struct{}

func (endingView) JobRecord(platform.Job) ([]byte, error) { return nil, platform.ErrEndedRun }
func (endingView) JobArgs(string) ([]byte, error)         { return nil, platform.ErrEndedRun }
func (endingView) JobPoll(string) (platform.JobResult, error) {
	return platform.JobResult{}, platform.ErrEndedRun
}

// A job read of a run that ends while the read renders it answers as for a
// job of a run that has ended (501), on the default identity, though the
// registry keeps only the job's ended record by the time the read is done.
func TestJobReadOfARunThatEndsDuringTheRead(t *testing.T) {
	const job, root = "00000000-0000-4000-8000-0000000000e9", "00000000-0000-4000-8000-0000000000e8"
	live := platform.Job{ID: job, Kind: platform.JobFlow, Root: root, Run: root, View: endingView{}, Gen: 1}
	ended := platform.Job{ID: job, Kind: platform.JobFlow, Root: root}
	jobs := &changingJobs{id: job, first: live, firstKnown: true, then: ended, roots: map[string]platform.Job{root: {ID: root, Kind: platform.JobFlow, Root: root}}}
	f := newFacade(t, Config{Platform: &platform.Services{Jobs: jobs}})
	s := f.register("inv-a", root, map[string]string{"WM_JOB_ID": "00000000-0000-4000-8000-0000000000e7", "WM_ROOT_JOB_ID": root, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "windmill-lite"}, nil, &platform.Host{})
	defer s.End()
	for _, route := range []string{"jobs_u/get/", "jobs_u/get_args/"} {
		jobs.mu.Lock()
		jobs.lookups = 0
		jobs.mu.Unlock()
		rep := intercept(f, "inv-a", "GET", "http://x/api/w/windmill-lite/"+route+job, nil, nil)
		if rep.Status != 501 || !strings.Contains(string(rep.Body), "has ended") {
			t.Errorf("%s: %d %s, want the 501 of a job of a run that has ended", route, rep.Status, rep.Body)
		}
	}
}

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

func (v namedView) JobRecord(string) ([]byte, error) {
	return []byte(`{"view":"` + string(v) + `"}`), nil
}
func (v namedView) JobArgs(string) ([]byte, error) {
	return []byte(`{"args":"` + string(v) + `"}`), nil
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
// never answer with the other workspace's job.
func TestJobLookupsAgainstARegistrationChange(t *testing.T) {
	const job, localRoot, otherRoot = "00000000-0000-4000-8000-0000000000c1", "00000000-0000-4000-8000-0000000000c2", "00000000-0000-4000-8000-0000000000c3"
	local := platform.Job{ID: job, Kind: platform.JobFlow, Root: localRoot, Run: localRoot, View: namedView("local"), Host: platform.Host{Workspace: "ws-a"}}
	other := platform.Job{ID: job, Kind: platform.JobFlow, Root: otherRoot, Run: otherRoot, View: namedView("other"), Host: platform.Host{Workspace: "ws-b"}}
	jobs := &changingJobs{id: job, then: other, roots: map[string]platform.Job{
		localRoot: {ID: localRoot, Kind: platform.JobFlow, Root: localRoot, Host: platform.Host{Workspace: "ws-a"}},
		otherRoot: {ID: otherRoot, Kind: platform.JobFlow, Root: otherRoot, Host: platform.Host{Workspace: "ws-b"}},
	}}
	f := newFacade(t, Config{Platform: &platform.Services{Jobs: jobs, UserState: mapStates{job + "/k": json.RawMessage(`"other's"`)}}})
	s := f.register("inv-a", localRoot, map[string]string{"WM_JOB_ID": "00000000-0000-4000-8000-0000000000c4", "WM_ROOT_JOB_ID": localRoot, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws-a"}, nil, platform.Host{Workspace: "ws-a"})
	defer s.End()
	for _, tc := range []struct {
		name       string
		first      platform.Job
		firstKnown bool
		want       map[string]string
	}{
		{"unknown, then of another workspace", platform.Job{}, false, map[string]string{
			"jobs_u/get_root_job_id/": "400 SqlErr: " + noRows,
			"jobs_u/get/":             "404 Not found: Job not found at name " + job,
			"jobs_u/get_args/":        "404 Not found: Job Args not found at name " + job,
			"jobs/flow/user_states/":  "200 null",
		}},
		{"of the caller's workspace, then of another", local, true, map[string]string{
			"jobs_u/get_root_job_id/": `200 "` + localRoot + `"`,
			"jobs_u/get/":             `200 {"view":"local"}`,
			"jobs_u/get_args/":        `200 {"args":"local"}`,
			// The read finds the id changed when it looks again.
			"jobs/flow/user_states/": "200 null",
		}},
	} {
		for route, want := range tc.want {
			jobs.mu.Lock()
			jobs.first, jobs.firstKnown, jobs.lookups = tc.first, tc.firstKnown, 0
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
	s := f.register("inv-a", mine, map[string]string{"WM_JOB_ID": "00000000-0000-4000-8000-0000000000d4", "WM_ROOT_JOB_ID": mine, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws-a"}, nil, platform.Host{Workspace: "ws-a"})
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
// the job_id.
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
	a := f.register("inv-a", runA, map[string]string{"WM_JOB_ID": "00000000-0000-4000-8000-0000000000e4", "WM_FLOW_JOB_ID": runA, "WM_ROOT_JOB_ID": runA, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws-a"}, nil, platform.Host{Workspace: "ws-a"})
	defer a.End()
	// A step of ws-b whose hook set its WM_WORKSPACE to ws-a.
	b := f.register("inv-b", runB, map[string]string{"WM_JOB_ID": stepB, "WM_FLOW_JOB_ID": runB, "WM_ROOT_JOB_ID": runB, "WM_TOKEN": "tok-b", "WM_WORKSPACE": "ws-a"}, nil, platform.Host{Workspace: "ws-b"})
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
}

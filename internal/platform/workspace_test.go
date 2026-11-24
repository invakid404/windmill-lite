package platform

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// scripted is one answer of scriptedJobs.
type scripted struct {
	job  Job
	live bool
}

// scriptedJobs answers each lookup of id with the next of its answers, and
// with the last one from then on, as a registry would while the id's
// registration changes between two lookups; every other id is unknown.
type scriptedJobs struct {
	mu      sync.Mutex
	id      string
	answers []scripted
	n       int
}

func (s *scriptedJobs) Begin(Job) func() { return func() {} }

func (s *scriptedJobs) Job(id string) (Job, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.id || len(s.answers) == 0 {
		return Job{}, false, false
	}
	a := s.answers[min(s.n, len(s.answers)-1)]
	s.n++
	return a.job, a.live, true
}

func (s *scriptedJobs) set(answers ...scripted) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers, s.n = answers, 0
}

// A flow job's user state belongs to the registration that it was checked
// against: a write checked against workspace A's registration of an id
// that lands once the id is B's is refused and B never reads it; a value
// that A's registration wrote is not that of A's next registration of the
// id, to read or to list; B's own value is B's alone; and the maps of
// registrations that the registry no longer holds are dropped.
func TestUserStateBoundToItsRegistration(t *testing.T) {
	ctx := context.Background()
	const id = "x"
	a1 := scripted{Job{ID: id, Kind: JobFlow, Root: "root-a", Host: Host{Workspace: "ws-a"}, Gen: 1}, true}
	b2 := scripted{Job{ID: id, Kind: JobFlow, Root: "root-b", Host: Host{Workspace: "ws-b"}, Gen: 2}, true}
	a3 := scripted{Job{ID: id, Kind: JobFlow, Root: "root-a", Host: Host{Workspace: "ws-a"}, Gen: 3}, true}
	jobs := &scriptedJobs{id: id}
	states := NewUserStates(jobs)
	s := &Services{Jobs: jobs, UserState: states}
	read := func(scope string) string {
		t.Helper()
		v, err := s.FlowUserStateIn(ctx, id, "k", scope)
		if err != nil {
			t.Fatal(err)
		}
		return string(v)
	}
	jobs.set(a1, b2)
	if err := s.SetFlowUserStateIn(ctx, id, "k", []byte(`"a"`), "ws-a"); !errors.Is(err, ErrFlowJobNotFound) {
		t.Errorf("a write checked against A's registration, landing on B's: %v, want ErrFlowJobNotFound", err)
	}
	jobs.set(b2)
	if got := read("ws-b"); got != "null" {
		t.Errorf("B read %s after A's write, want null", got)
	}
	jobs.set(a1)
	if err := s.SetFlowUserStateIn(ctx, id, "k", []byte(`"first"`), "ws-a"); err != nil {
		t.Fatal(err)
	}
	if got := read("ws-a"); got != `"first"` {
		t.Errorf("A's registration read %s, want its own value", got)
	}
	jobs.set(a3)
	if got := read("ws-a"); got != "null" {
		t.Errorf("A's next registration read %s, want null", got)
	}
	if m, ok, _ := s.FlowUserStates(ctx, id); ok {
		t.Errorf("A's next registration lists %s", m)
	}
	jobs.set(b2)
	if err := s.SetFlowUserStateIn(ctx, id, "k", []byte(`"b"`), "ws-b"); err != nil {
		t.Fatal(err)
	}
	if got := read("ws-b"); got != `"b"` {
		t.Errorf("B read %s, want its own value", got)
	}
	if m, ok, _ := s.FlowUserStates(ctx, id); !ok || string(m) != `{"k":"b"}` {
		t.Errorf("B lists %s, %v", m, ok)
	}
	jobs.set(a1)
	if got := read("ws-a"); got != "null" {
		t.Errorf("A read %s after B's write, want null", got)
	}
	states.mu.Lock()
	states.dropDead()
	states.mu.Unlock()
	if n := states.size(); n != 0 {
		t.Errorf("%d maps kept of a registration the registry no longer holds", n)
	}
}

// argsView renders every job's arguments as its name.
type argsView string

func (v argsView) JobRecord(string) ([]byte, error) {
	return []byte(`{"view":"` + string(v) + `"}`), nil
}
func (v argsView) JobArgs(string) ([]byte, error) { return []byte(`{"args":"` + string(v) + `"}`), nil }

// When runs of two workspaces register one job id at once, as a hook that
// sets WM_JOB_ID or a host that reuses a run id can make them, the id stays
// the first registration's: each workspace reads its own run's arguments
// or none, and never the other's, whichever registers first.
func TestDuplicateRegistrationAcrossWorkspaces(t *testing.T) {
	for _, first := range []string{"ws-a", "ws-b"} {
		r := NewRegistry()
		s := &Services{Jobs: r}
		a, b := Host{Workspace: "ws-a"}, Host{Workspace: "ws-b"}
		defer r.Begin(Job{ID: "run-a", Kind: JobFlow, Run: "run-a", View: argsView("a"), Host: a})()
		// A's step, whose hook gave it B's run id as its WM_JOB_ID, and B's
		// run of that id.
		stepA := Job{ID: "x", Kind: JobScript, Root: "run-a", Run: "run-a", Host: a}
		runB := Job{ID: "x", Kind: JobFlow, Run: "x", View: argsView("b"), Host: b}
		if first == "ws-a" {
			defer r.Begin(stepA)()
			defer r.Begin(runB)()
		} else {
			defer r.Begin(runB)()
			defer r.Begin(stepA)()
		}
		for _, ws := range []string{"ws-a", "ws-b"} {
			body, err := s.GetJobArgsIn("x", ws)
			other := map[string]string{"ws-a": `{"args":"b"}`, "ws-b": `{"args":"a"}`}[ws]
			if string(body) == other {
				t.Errorf("%s registered first: %s read the other workspace's arguments %s", first, ws, body)
			}
			if own := map[string]string{"ws-a": `{"args":"a"}`, "ws-b": `{"args":"b"}`}[ws]; ws == first && (err != nil || string(body) != own) {
				t.Errorf("%s registered first: %s read %s, %v; want its own %s", first, ws, body, err, own)
			}
		}
	}
}

// A script job of workspace B whose root is the id of an ended job that
// the registry keeps for workspace A, as a dispatch with that id as its
// root_job makes it, leaves the id A's: the root lookup answers for A and
// not for B before the script job, while it runs and after it ends.
func TestTrackedRootOfAnotherWorkspace(t *testing.T) {
	r := NewRegistry()
	s := &Services{Jobs: r}
	r.Begin(Job{ID: "run-a", Kind: JobFlow, Run: "run-a", Host: Host{Workspace: "ws-a"}})()
	check := func(when string) {
		t.Helper()
		if root, ok := s.RootJobIn("run-a", "ws-a"); !ok || root != "run-a" {
			t.Errorf("%s: A's root lookup %q, %v; want run-a", when, root, ok)
		}
		if root, ok := s.RootJobIn("run-a", "ws-b"); ok {
			t.Errorf("%s: B's root lookup found %q", when, root)
		}
	}
	check("before")
	end := r.Begin(Job{ID: "step-b", Kind: JobScript, Root: "run-a", Run: "run-b", Host: Host{Workspace: "ws-b"},
		Env: map[string]string{"WM_ROOT_JOB_ID": "run-a", "WM_ROOT_FLOW_JOB_ID": "run-a"}})
	check("while B's script job runs")
	end()
	check("after")
}

// Registry numbers each new registration of an id: the user state that one
// registration wrote is not that of the next registration of the id, even
// when nothing dropped the old map in between, and a flow job known only
// from a script job keeps its number when its run registers it.
func TestRegistrationNumbers(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry()
	s := &Services{Jobs: r}
	ws := Host{Workspace: "ws-a"}
	end := r.Begin(Job{ID: "x", Kind: JobFlow, Run: "x", Host: ws})
	if err := s.SetFlowUserStateIn(ctx, "x", "k", []byte(`1`), "ws-a"); err != nil {
		t.Fatal(err)
	}
	first, _, _ := r.Job("x")
	end()
	defer r.Begin(Job{ID: "x", Kind: JobFlow, Run: "x", Host: ws})()
	again, _, _ := r.Job("x")
	if again.Gen == first.Gen {
		t.Errorf("two registrations of x share the number %d", again.Gen)
	}
	if v, _ := s.FlowUserStateIn(ctx, "x", "k", "ws-a"); string(v) != "null" {
		t.Errorf("the next registration of x read %s, want null", v)
	}
	defer r.Begin(Job{ID: "step", Kind: JobScript, Root: "root", Run: "root", Host: ws, Env: map[string]string{"WM_ROOT_JOB_ID": "root"}})()
	derived, _, _ := r.Job("root")
	defer r.Begin(Job{ID: "root", Kind: JobFlow, Run: "root", Host: ws})()
	if registered, _, _ := r.Job("root"); registered.Gen != derived.Gen {
		t.Errorf("the derived root's number %d changed to %d when its run registered it", derived.Gen, registered.Gen)
	}
}

// With a registry that numbers no registration (Gen 0), as a host's Jobs
// may, the state of a flow job of workspace A is still not that of B when
// the id is reused: in the services' own store and in a host's store.
func TestUserStateWithoutRegistrationNumbers(t *testing.T) {
	ctx := context.Background()
	const id = "x"
	a := scripted{Job{ID: id, Kind: JobFlow, Root: id, Host: Host{Workspace: "ws-a"}}, true}
	b := scripted{Job{ID: id, Kind: JobFlow, Root: id, Host: Host{Workspace: "ws-b"}}, true}
	for name, store := range map[string]func(Jobs) FlowUserState{
		"the services' store": func(j Jobs) FlowUserState { return NewUserStates(j) },
		"a host's store":      func(Jobs) FlowUserState { return &hostStates{vals: map[string]json.RawMessage{}} },
	} {
		jobs := &scriptedJobs{id: id}
		s := &Services{Jobs: jobs, UserState: store(jobs)}
		jobs.set(a)
		if err := s.SetFlowUserStateIn(ctx, id, "k", []byte(`"a"`), "ws-a"); err != nil {
			t.Fatal(err)
		}
		jobs.set(b)
		if v, _ := s.FlowUserStateIn(ctx, id, "k", "ws-b"); string(v) != "null" {
			t.Errorf("%s: B read A's %s", name, v)
		}
		if err := s.SetFlowUserStateIn(ctx, id, "k", []byte(`"b"`), "ws-b"); err != nil {
			t.Fatal(err)
		}
		jobs.set(a)
		if v, _ := s.FlowUserStateIn(ctx, id, "k", "ws-a"); string(v) != `"a"` && string(v) != "null" {
			t.Errorf("%s: A read %s, want its own value or none", name, v)
		}
		if v, _ := s.FlowUserStateIn(ctx, id, "k", "ws-a"); string(v) == `"b"` {
			t.Errorf("%s: A read B's value", name)
		}
	}
}

// The cleanup of a root's ended registration drops only that
// registration's maps: when a new registration of the id writes before the
// cleanup runs, and the cleanup finds the id no longer live (it ended in
// turn), the new registration's value stays until its own cleanup.
func TestStateCleanupKeepsALaterRegistration(t *testing.T) {
	ctx := context.Background()
	const id = "x"
	b2 := scripted{Job{ID: id, Kind: JobFlow, Root: id, Host: Host{Workspace: "ws-b"}, Gen: 2}, true}
	ended := scripted{Job{ID: id, Kind: JobFlow, Root: id, Host: Host{Workspace: "ws-b"}, Gen: 2}, false}
	jobs := &scriptedJobs{id: id}
	states := NewUserStates(jobs)
	s := &Services{Jobs: jobs, UserState: states}
	// The write's three lookups see the new registration; the cleanup's
	// sees it ended.
	jobs.set(b2, b2, b2, ended)
	if err := s.SetFlowUserStateIn(ctx, id, "k", []byte(`"b"`), "ws-b"); err != nil {
		t.Fatal(err)
	}
	states.forgetRoot(id, 1)
	if m := states.statesAt(b2.job); string(m["k"]) != `"b"` {
		t.Errorf("the cleanup of registration 1 left registration 2 with %v", m)
	}
	states.forgetRoot(id, 2)
	if m := states.statesAt(b2.job); m != nil {
		t.Errorf("the cleanup of registration 2 left %v", m)
	}
}

// The jobs of a run of workspace B whose run id is a job of workspace A
// (registered first) are B's: a child flow job with its own view, and a
// step that falls back to its run's view, which B's registration of the
// shared id keeps for B. A reads them as unknown ids, and B reads its own.
// Once both runs have ended, the registry holds none of their jobs, so no
// view of B's run stays reachable.
func TestJobsOfACollidingRun(t *testing.T) {
	r := NewRegistry()
	s := &Services{Jobs: r}
	a, b := Host{Workspace: "ws-a"}, Host{Workspace: "ws-b"}
	endRunA := r.Begin(Job{ID: "run-a", Kind: JobFlow, Run: "run-a", View: argsView("a"), Host: a})
	endStepA := r.Begin(Job{ID: "x", Kind: JobScript, Root: "run-a", Run: "run-a", Host: a})
	endRunB := r.Begin(Job{ID: "x", Kind: JobFlow, Run: "x", View: argsView("b"), Host: b})
	endChild := r.Begin(Job{ID: "child", Kind: JobFlow, Root: "x", Run: "x", View: argsView("child-b"), Host: b})
	endStepB := r.Begin(Job{ID: "step", Kind: JobScript, Root: "x", Run: "x", Host: b})
	for _, tc := range []struct{ id, ws, want string }{
		{"child", "ws-a", "Not found: Job Args not found at name child"},
		{"step", "ws-a", "Not found: Job Args not found at name step"},
		{"child", "ws-b", `{"args":"child-b"}`},
		{"step", "ws-b", `{"args":"b"}`},
	} {
		body, err := s.GetJobArgsIn(tc.id, tc.ws)
		got := string(body)
		var api *Error
		if errors.As(err, &api) {
			got = api.Body()
		} else if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("%s reads %s: %s, want %s", tc.ws, tc.id, got, tc.want)
		}
	}
	endStepB()
	endChild()
	endRunB()
	endStepA()
	endRunA()
	r.mu.Lock()
	held := len(r.jobs)
	r.mu.Unlock()
	if held != 0 {
		t.Errorf("the registry holds %d jobs after both runs ended", held)
	}
	if j, live, ok := r.Job("child"); !ok || live || j.View != nil {
		t.Errorf("B's ended child: %+v, live %v, known %v; want an ended record without a view", j, live, ok)
	}
}

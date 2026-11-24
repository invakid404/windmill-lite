package platform

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
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

func (v argsView) JobRecord(Job) ([]byte, error) {
	return []byte(`{"view":"` + string(v) + `"}`), nil
}
func (v argsView) JobArgs(string) ([]byte, error) { return []byte(`{"args":"` + string(v) + `"}`), nil }
func (v argsView) JobPoll(Job) (JobResult, error) {
	return JobResult{Started: true, Completed: true, Success: true, Result: []byte(`"` + string(v) + `"`)}, nil
}

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

// The services' own store keeps workspaces apart even over a registry that
// numbers no registration (Gen 0): the state of a flow job of workspace A
// is not that of B when the id passes from A to B.
func TestUserStateWithoutRegistrationNumbers(t *testing.T) {
	ctx := context.Background()
	const id = "x"
	a := scripted{Job{ID: id, Kind: JobFlow, Root: id, Host: Host{Workspace: "ws-a"}}, true}
	b := scripted{Job{ID: id, Kind: JobFlow, Root: id, Host: Host{Workspace: "ws-b"}}, true}
	jobs := &scriptedJobs{id: id}
	s := &Services{Jobs: jobs, UserState: NewUserStates(jobs)}
	jobs.set(a)
	if err := s.SetFlowUserStateIn(ctx, id, "k", []byte(`"a"`), "ws-a"); err != nil {
		t.Fatal(err)
	}
	jobs.set(b)
	if v, _ := s.FlowUserStateIn(ctx, id, "k", "ws-b"); string(v) != "null" {
		t.Errorf("B read A's %s", v)
	}
	if err := s.SetFlowUserStateIn(ctx, id, "k", []byte(`"b"`), "ws-b"); err != nil {
		t.Fatal(err)
	}
	jobs.set(a)
	if v, _ := s.FlowUserStateIn(ctx, id, "k", "ws-a"); string(v) == `"b"` {
		t.Error("A read B's value")
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

// BeginExclusive refuses a registration whose id a job of another
// workspace holds, live or among the ended jobs the registry keeps, and
// takes one of the same workspace, as Begin does. A job known only from a
// script job's variables holds nothing.
func TestExclusiveRegistration(t *testing.T) {
	r := NewRegistry()
	a, b := Host{Workspace: "ws-a"}, Host{}
	endA := r.Begin(Job{ID: "x", Kind: JobFlow, Run: "x", Host: a})
	if _, ok := r.BeginExclusive(Job{ID: "x", Kind: JobFlow, Run: "x", Host: b}); ok {
		t.Error("windmill-lite registered ws-a's live x")
	}
	end, ok := r.BeginExclusive(Job{ID: "x", Kind: JobScript, Root: "x", Run: "x", Host: a})
	if !ok {
		t.Fatal("ws-a could not register its own x again")
	}
	end()
	endA()
	if _, ok := r.BeginExclusive(Job{ID: "x", Kind: JobFlow, Run: "x", Host: b}); ok {
		t.Error("windmill-lite registered ws-a's ended x")
	}
	if j, _, ok := r.Job("x"); !ok || j.Host.EffectiveWorkspace() != "ws-a" {
		t.Errorf("x after the refusal: %+v, %v; want ws-a's ended job", j, ok)
	}
	// y is known only from a script job of ws-a's variables.
	defer r.Begin(Job{ID: "step", Kind: JobScript, Root: "y", Run: "run", Host: a, Env: map[string]string{"WM_ROOT_JOB_ID": "y"}})()
	endY, ok := r.BeginExclusive(Job{ID: "y", Kind: JobFlow, Run: "y", Host: b})
	if !ok {
		t.Fatal("windmill-lite could not register y, which only a script job named")
	}
	endY()
}

// A host's store, which serves windmill-lite's workspace only (the engine
// runs another workspace's runs only with the services' own store), gets
// each flow job's id as its key, as it always did: a store that already
// holds a value for a job whose id has a slash gives it back, and takes a
// write.
func TestHostStoreKeepsJobIDs(t *testing.T) {
	ctx := context.Background()
	jobs := &scriptedJobs{id: "alpha/u"}
	store := &hostStates{vals: map[string]json.RawMessage{"alpha/u/k": json.RawMessage(`"kept"`)}}
	s := &Services{Jobs: jobs, UserState: store}
	jobs.set(scripted{Job{ID: "alpha/u", Kind: JobFlow, Root: "alpha/u"}, true})
	if v, _ := s.FlowUserStateIn(ctx, "alpha/u", "k", ""); string(v) != `"kept"` {
		t.Errorf("alpha/u read %s, want the value the store held", v)
	}
	if err := s.SetFlowUserStateIn(ctx, "alpha/u", "j", []byte(`1`), ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.FlowUserStateIn(ctx, "alpha/u", "j", ""); string(v) != `1` {
		t.Errorf("alpha/u read back %s", v)
	}
	store.mu.Lock()
	keys := slices.Sorted(maps.Keys(store.vals))
	store.mu.Unlock()
	if want := []string{"alpha/u/j", "alpha/u/k"}; !slices.Equal(keys, want) {
		t.Errorf("the host's store holds %v, want %v", keys, want)
	}
}

// With the services' own registry a job keeps the owner it registered with,
// windmill-lite's (no Host) included: a step of an unconfigured run that
// outlives its run stays windmill-lite's after the run's id is forgotten
// and a run of ws-a registers it, so ws-a never reads the step's job
// variables or root, and windmill-lite still does.
func TestStepOwnerAfterItsRunIDIsReused(t *testing.T) {
	r := NewRegistry()
	r.keep = 1
	s := &Services{Jobs: r}
	const run, step = "run", "step"
	endRun := r.Begin(Job{ID: run, Kind: JobFlow, Run: run})
	endStep := r.Begin(Job{ID: step, Kind: JobScript, Root: run, Run: run, Env: map[string]string{"WM_JOB_ID": step, "WM_WORKSPACE": "windmill-lite", "WM_TOKEN": "t"}})
	defer endStep()
	endRun()
	// Two more ended jobs push the run out of the kept ended jobs.
	r.Begin(Job{ID: "x1", Kind: JobFlow, Run: "x1"})()
	r.Begin(Job{ID: "x2", Kind: JobFlow, Run: "x2"})()
	endReused, ok := r.BeginExclusive(Job{ID: run, Kind: JobFlow, Run: run, Host: Host{Workspace: "ws-a"}})
	if !ok {
		t.Fatal("ws-a could not register the forgotten run id")
	}
	defer endReused()
	if scope, ok := s.JobScope(step); !ok || scope != DefaultHost.Workspace {
		t.Errorf("the step's scope: %q, %v; want windmill-lite", scope, ok)
	}
	if _, err := s.ReadJobEnvIn(step, "ws-a", "ws-a"); err == nil {
		t.Error("ws-a read the step's job variables")
	}
	if root, ok := s.RootJobIn(step, "ws-a"); ok {
		t.Errorf("ws-a found the step's root %q", root)
	}
	if env, err := s.ReadJobEnvIn(step, "windmill-lite", DefaultHost.Workspace); err != nil || env["WM_JOB_ID"] != step {
		t.Errorf("windmill-lite's read of its step: %v, %v", env, err)
	}
	if root, ok := s.RootJobIn(step, DefaultHost.Workspace); !ok || root != run {
		t.Errorf("windmill-lite's root lookup of its step: %q, %v", root, ok)
	}
}

// compactJobs is a host's registry that keeps of an ended job only its id,
// kind, root and path, as the services' own registry did before jobs had
// owners: its jobs come back with no Host and no Run.
type compactJobs map[string]Job

func (compactJobs) Begin(Job) func() { return func() {} }

func (c compactJobs) Job(id string) (Job, bool, bool) {
	j, ok := c[id]
	return j, false, ok
}

// A compatibility control: with a host's registry the job lookups answer as
// they always did, without a workspace scope, so an engine configured with a
// workspace finds through it a job whose record the registry compacted to
// its id, kind, root and path: the root lookup answers its root, and a job
// read answers as for a job of a run that has ended.
func TestHostRegistryCompactedJob(t *testing.T) {
	s := &Services{Jobs: compactJobs{
		"job":  {ID: "job", Kind: JobFlow, Root: "root", Path: "f/x"},
		"root": {ID: "root", Kind: JobFlow, Root: "root", Path: "f/x"},
	}}
	if root, ok := s.RootJobIn("job", "ws-x"); !ok || root != "root" {
		t.Errorf("the root lookup through ws-x: %q, %v; want root", root, ok)
	}
	if _, err := s.GetJobIn("job", "ws-x"); !errors.Is(err, ErrEndedRun) {
		t.Errorf("the job read through ws-x: %v, want ErrEndedRun", err)
	}
}

// A compatibility control: with a host's registry, a read's job_id keeps the
// base's rule, the path's workspace against the job's WM_WORKSPACE (which a
// hook may have set), whatever Host the job was registered with.
func TestHostRegistryJobEnvRule(t *testing.T) {
	jobs := newFakeJobs()
	s := &Services{Jobs: jobs}
	defer jobs.Begin(Job{ID: "step", Kind: JobScript, Host: Host{Workspace: "ws-x"}, Env: map[string]string{"WM_JOB_ID": "step", "WM_WORKSPACE": "ws-y"}})()
	if env, err := s.ReadJobEnvIn("step", "ws-y", "ws-x"); err != nil || env["WM_JOB_ID"] != "step" {
		t.Errorf("a read through ws-y, the step's WM_WORKSPACE: %v, %v; want its variables", env, err)
	}
	if _, err := s.ReadJobEnvIn("step", "ws-x", "ws-x"); err == nil {
		t.Error("a read through ws-x, not the step's WM_WORKSPACE, found it")
	}
}

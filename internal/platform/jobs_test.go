package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// The registry knows a run and its jobs while the run lives (D3): a script
// job's parent flow, innermost scope and root become known with it, and a
// job that ended stays live while its root runs. Once the root and every
// job of it have ended, the tree's jobs are no longer live but stay known
// with their kind, root and path, without a script job's variables, as
// Windmill keeps completed jobs' rows (WA-19).
func TestRegistryLifetime(t *testing.T) {
	r := NewRegistry()
	endRun := r.Begin(Job{ID: "run", Kind: JobFlow, Root: "run", Path: "f/x"})
	env := map[string]string{"WM_FLOW_JOB_ID": "child", "WM_ROOT_FLOW_JOB_ID": "inner", "WM_FLOW_PATH": "f/x/forloop-0"}
	endStep := r.Begin(Job{ID: "step", Kind: JobScript, Root: "run", Path: "f/x/forloop-0/a", Env: env})
	for _, c := range []struct {
		id         string
		kind       JobKind
		root, path string
	}{{"run", JobFlow, "run", "f/x"}, {"step", JobScript, "run", "f/x/forloop-0/a"}, {"child", JobFlow, "run", "f/x/forloop-0"}, {"inner", JobFlow, "run", ""}} {
		j, live, ok := r.Job(c.id)
		if !ok || !live || j.Kind != c.kind || j.Root != c.root || j.Path != c.path {
			t.Errorf("%s: %+v live %v known %v", c.id, j, live, ok)
		}
	}
	endStep()
	endStep() // a second end changes nothing
	if _, live, ok := r.Job("step"); !ok || live {
		t.Errorf("an ended step of a live run: live %v known %v", live, ok)
	}
	endRun()
	for _, c := range []struct {
		id         string
		kind       JobKind
		root, path string
	}{{"run", JobFlow, "run", "f/x"}, {"step", JobScript, "run", "f/x/forloop-0/a"}, {"child", JobFlow, "run", "f/x/forloop-0"}, {"inner", JobFlow, "run", ""}} {
		j, live, ok := r.Job(c.id)
		if !ok || live || j.Kind != c.kind || j.Root != c.root || j.Path != c.path || j.Env != nil {
			t.Errorf("%s after its run ended: %+v live %v known %v", c.id, j, live, ok)
		}
	}
	if len(r.jobs) != 0 || len(r.trees) != 0 {
		t.Errorf("left %d jobs, %d trees", len(r.jobs), len(r.trees))
	}
	// Registered again, an ended id is live again.
	endAgain := r.Begin(Job{ID: "step", Kind: JobScript, Root: "run2"})
	if j, live, ok := r.Job("step"); !ok || !live || j.Root != "run2" {
		t.Errorf("an ended id registered again: %+v live %v known %v", j, live, ok)
	}
	endAgain()
	if j, live, ok := r.Job("step"); !ok || live || j.Root != "run2" {
		t.Errorf("that id ended again: %+v live %v known %v", j, live, ok)
	}
}

// The registry keeps the latest keep jobs of ended trees: an older one is
// forgotten, and its id is unknown again; a stale entry, an id registered
// and ended again, does not evict the id's later entry early.
func TestRegistryEndedBound(t *testing.T) {
	r := NewRegistry()
	if r.keep != endedJobs {
		t.Fatalf("keep %d, want %d", r.keep, endedJobs)
	}
	r.keep = 3
	run := func(id string) { r.Begin(Job{ID: id, Kind: JobFlow})() }
	for _, id := range []string{"a", "b", "c"} {
		run(id)
	}
	run("a") // a again: its first entry is stale
	run("d") // evicts b, the oldest entry that is not stale
	for id, want := range map[string]bool{"a": true, "b": false, "c": true, "d": true} {
		if _, live, ok := r.Job(id); ok != want || live {
			t.Errorf("%s: known %v live %v, want known %v", id, ok, live, want)
		}
	}
	for i := range 100 {
		run(fmt.Sprint("x", i))
	}
	if len(r.ended) != 3 || len(r.order) > 2*r.keep {
		t.Errorf("kept %d ended jobs in an order of %d, want 3 in at most %d", len(r.ended), len(r.order), 2*r.keep)
	}
}

// A job still live when its root ends (an abandoned step, D2) stays known
// until it ends, with the scopes it runs in, and the tree goes with it.
func TestRegistryOutlivedRoot(t *testing.T) {
	r := NewRegistry()
	endRun := r.Begin(Job{ID: "run", Kind: JobFlow, Root: "run"})
	endStep := r.Begin(Job{ID: "step", Kind: JobScript, Root: "run", Env: map[string]string{"WM_FLOW_JOB_ID": "child"}})
	endRun()
	if _, live, ok := r.Job("step"); !ok || !live {
		t.Errorf("a live step after its root ended: live %v known %v", live, ok)
	}
	if j, live, ok := r.Job("child"); !ok || live || j.Root != "run" {
		t.Errorf("its scope: %+v live %v known %v (live only while the root is)", j, live, ok)
	}
	endStep()
	if len(r.jobs) != 0 || len(r.trees) != 0 {
		t.Errorf("left %d jobs, %d trees", len(r.jobs), len(r.trees))
	}
}

// Jobs of a root the registry was never told of (the facade's invocations
// alone) are known while one of them lives, and forgotten with the last.
func TestRegistryUnregisteredRoot(t *testing.T) {
	r := NewRegistry()
	endA := r.Begin(Job{ID: "a", Kind: JobScript, Root: "root"})
	endB := r.Begin(Job{ID: "b", Kind: JobScript, Root: "root"})
	endA()
	if j, live, ok := r.Job("root"); !ok || live || j.Kind != JobFlow {
		t.Errorf("the root while b lives: %+v live %v known %v", j, live, ok)
	}
	endB()
	if len(r.jobs) != 0 || len(r.trees) != 0 {
		t.Errorf("left %d jobs, %d trees", len(r.jobs), len(r.trees))
	}
}

// Flow user state (WA-14): a live flow job's map, read as JSON null for a
// missing key, for a script job, for an unknown job and once the run has
// ended; a write anywhere but a live flow job is the API's 404, and a
// body that is not JSON, or that jsonb refuses, its 400. A value comes
// back as the oracle recorded it (client-api/user-state, run order).
func TestFlowUserState(t *testing.T) {
	ctx := context.Background()
	s := &Services{}
	end := s.JobRegistry().Begin(Job{ID: "run", Kind: JobFlow, Root: "run"})
	endStep := s.JobRegistry().Begin(Job{ID: "step", Kind: JobScript, Root: "run", Env: map[string]string{"WM_FLOW_JOB_ID": "run"}})
	defer endStep()
	if err := s.SetFlowUserState(ctx, "run", "k", []byte(`{"b":1, "a":[1.50,"\u0041",1e3,1.0,12345678901234567890123],"ab":null,"c":{"y":1,"x":2}}`)); err != nil {
		t.Fatal(err)
	}
	get := func(id, key string) string {
		v, err := s.FlowUserState(ctx, id, key)
		if err != nil {
			t.Fatal(err)
		}
		return string(v)
	}
	// As the oracle recorded (client-api/user-state, run order): jsonb's
	// key order, numbers as the backend's JSON library writes them.
	if got := get("run", "k"); got != `{"a":[1.5,"A",1000.0,1.0,1.2345678901234568e+22],"b":1,"c":{"x":2,"y":1},"ab":null}` {
		t.Errorf("stored form %s", got)
	}
	for _, c := range [][2]string{{"run", "missing"}, {"step", "k"}, {"nobody", "k"}} {
		if got := get(c[0], c[1]); got != "null" {
			t.Errorf("%s/%s: %s", c[0], c[1], got)
		}
	}
	var pe *Error
	for _, id := range []string{"step", "nobody"} {
		if err := s.SetFlowUserState(ctx, id, "k", []byte("1")); !errors.As(err, &pe) || pe.HTTPStatus() != 404 || pe.Body() != "Not found: Flow job not found" {
			t.Errorf("a write at %s: %v", id, err)
		}
	}
	for _, body := range []string{"{", `"\u0000"`, ""} {
		if err := s.SetFlowUserState(ctx, "run", "k", []byte(body)); !errors.As(err, &pe) || pe.HTTPStatus() != 400 {
			t.Errorf("a body %q: %v", body, err)
		}
	}
	end()
	// WA-14/c: after the run a read by its id is null and a write is
	// refused with the 404, as Windmill answers them once the root has
	// completed; windmill-lite discarded the map with the run.
	if got := get("run", "k"); got != "null" {
		t.Errorf("after the run: %s", got)
	}
	if n := s.defaultStates().size(); n != 0 {
		t.Errorf("%d maps kept after the run", n)
	}
	if err := s.SetFlowUserState(ctx, "run", "k", []byte("1")); !errors.As(err, &pe) || pe.HTTPStatus() != 404 {
		t.Errorf("a write after the run: %v", err)
	}
}

// WA-12/g: the job a resource read's job_id names must be live and of the
// path's workspace, else the read fails with the API's 404; a live script
// job gives its stored fields (APIEnv), and a live flow job, whose job
// variables the registry does not keep, ErrNoJobVariables.
func TestReadJobEnv(t *testing.T) {
	s := &Services{}
	endRun := s.JobRegistry().Begin(Job{ID: "run", Kind: JobFlow})
	defer endRun()
	env := map[string]string{"WM_JOB_ID": "step", "WM_FLOW_JOB_ID": "run", "WM_ROOT_FLOW_JOB_ID": "run", "WM_ROOT_JOB_ID": "run", "WM_WORKSPACE": "ws"}
	endStep := s.JobRegistry().Begin(Job{ID: "step", Kind: JobScript, Root: "run", Env: env})
	for _, ws := range []string{"ws", ""} {
		got, err := s.ReadJobEnv("step", ws)
		if err != nil || got["WM_JOB_ID"] != "step" || got["WM_ROOT_JOB_ID"] != "" {
			t.Errorf("a live step in %q: %v %v", ws, got, err)
		}
	}
	if _, err := s.ReadJobEnv("run", "ws"); !errors.Is(err, ErrNoJobVariables) {
		t.Errorf("a live flow job: %v", err)
	}
	notFound := func(what, id, ws string) {
		t.Helper()
		var pe *Error
		if _, err := s.ReadJobEnv(id, ws); !errors.As(err, &pe) || pe.HTTPStatus() != 404 || pe.Body() != "Not found: Job not found at name "+id {
			t.Errorf("%s: %v", what, err)
		}
	}
	notFound("another workspace's", "step", "other")
	notFound("an unknown id", "00000000-0000-4000-8000-000000000001", "ws")
	endStep()
	notFound("an ended job of a live run", "step", "ws")
}

// Two Services share no registry; one Services gives every caller the
// same, even when they first ask at once.
func TestServicesRegistry(t *testing.T) {
	s := &Services{}
	var wg sync.WaitGroup
	regs := make([]*Registry, 8)
	for i := range regs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			regs[i] = s.Registry()
		}()
	}
	wg.Wait()
	for _, r := range regs {
		if r != regs[0] || r == nil {
			t.Fatalf("registries %p and %p", r, regs[0])
		}
	}
	if (&Services{}).Registry() == regs[0] {
		t.Error("two Services share a registry")
	}
	var none *Services
	if none.Registry() != nil {
		t.Error("a nil Services has a registry")
	}
	if _, ok := none.RootJob("x"); ok {
		t.Error("a nil Services knows a job")
	}
	end := none.JobRegistry().Begin(Job{ID: "x"})
	end()
	custom := NewRegistry()
	(&Services{Jobs: custom}).JobRegistry().Begin(Job{ID: "y"})
	if _, live, _ := custom.Job("y"); !live {
		t.Error("Jobs is not used")
	}
}

// fakeJobs is a host's job registry: not a Registry, it knows the jobs
// registered with it, live until their registration ends, and others a
// host marks live itself (runs registered elsewhere).
type fakeJobs struct {
	mu   sync.Mutex
	jobs map[string]Job
	live map[string]int
}

func newFakeJobs() *fakeJobs { return &fakeJobs{jobs: map[string]Job{}, live: map[string]int{}} }

func (f *fakeJobs) Begin(j Job) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[j.ID] = j
	f.live[j.ID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.live[j.ID]--
		})
	}
}

func (f *fakeJobs) Job(id string) (Job, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	return j, f.live[id] > 0, ok
}

// pf3 review round 1, P2-1: the default flow user state works with a
// host's Jobs service, not only with the services' own registry. Two
// flow jobs of a run (the run and a dispatched flow in its tree) write and
// read their maps through it, and the step helpers' reads share them; once
// the run's registration through the services ends, the maps are
// forgotten and a read is null.
func TestFlowUserStateWithAHostsJobs(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobs()
	s := &Services{Jobs: jobs}
	endRun := s.JobRegistry().Begin(Job{ID: "run", Kind: JobFlow, Root: "run"})
	endChild := s.JobRegistry().Begin(Job{ID: "child", Kind: JobFlow, Root: "run"})
	for _, id := range []string{"run", "child"} {
		if err := s.SetFlowUserState(ctx, id, "k", []byte(`{"from":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
		if v, err := s.FlowUserState(ctx, id, "k"); err != nil || string(v) != `{"from":"`+id+`"}` {
			t.Fatalf("%s: a write lost: %s %v", id, v, err)
		}
	}
	if j, live, ok := jobs.Job("run"); !ok || !live || j.Root != "run" {
		t.Errorf("the host's registry did not get the registration: %+v %v %v", j, live, ok)
	}
	endChild()
	endRun()
	if n := s.defaultStates().size(); n != 0 {
		t.Errorf("%d maps kept after the run", n)
	}
	if v, _ := s.FlowUserState(ctx, "run", "k"); string(v) != "null" {
		t.Errorf("after the run: %s", v)
	}
}

// The default user state stays bounded with a host's Jobs service that
// knows runs registered elsewhere, whose ends it never hears of: the maps
// of jobs that registry no longer reports live are swept as the store
// grows, while a live job's map is kept.
func TestFlowUserStateSweepsDeadJobs(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobs()
	s := &Services{Jobs: jobs}
	keep := jobs.Begin(Job{ID: "kept", Kind: JobFlow, Root: "kept"})
	defer keep()
	if err := s.SetFlowUserState(ctx, "kept", "k", []byte("1")); err != nil {
		t.Fatal(err)
	}
	for i := range 10 * minSweep {
		id := fmt.Sprintf("run-%d", i)
		end := jobs.Begin(Job{ID: id, Kind: JobFlow, Root: id}) // not through the services
		if err := s.SetFlowUserState(ctx, id, "k", []byte("1")); err != nil {
			t.Fatal(err)
		}
		end()
	}
	if n := s.defaultStates().size(); n > minSweep+1 {
		t.Errorf("%d maps kept for %d ended runs", n, 10*minSweep)
	}
	if v, _ := s.FlowUserState(ctx, "kept", "k"); string(v) != "1" {
		t.Errorf("a live job's map was swept: %s", v)
	}
}

// hostStates is a host's flow user state service.
type hostStates struct {
	mu   sync.Mutex
	vals map[string]json.RawMessage
}

func (h *hostStates) UserState(_ context.Context, job, key string) (json.RawMessage, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.vals[job+"/"+key]
	return v, ok, nil
}

func (h *hostStates) SetUserState(_ context.Context, job, key string, v json.RawMessage) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.vals[job+"/"+key] = v
	return nil
}

// The inverse configuration: a host's FlowUserState with the services' own
// registry gets the writes of live flow jobs only, keeps its own lifetime,
// and no default store is made.
func TestFlowUserStateWithAHostsStates(t *testing.T) {
	ctx := context.Background()
	h := &hostStates{vals: map[string]json.RawMessage{}}
	s := &Services{UserState: h}
	end := s.JobRegistry().Begin(Job{ID: "run", Kind: JobFlow, Root: "run"})
	if err := s.SetFlowUserState(ctx, "run", "k", []byte("1")); err != nil {
		t.Fatal(err)
	}
	var pe *Error
	if err := s.SetFlowUserState(ctx, "nobody", "k", []byte("1")); !errors.As(err, &pe) || pe.HTTPStatus() != 404 {
		t.Errorf("a write at an unknown job: %v", err)
	}
	if v, _ := s.FlowUserState(ctx, "run", "k"); string(v) != "1" {
		t.Errorf("read %s", v)
	}
	end()
	if string(h.vals["run/k"]) != "1" || len(h.vals) != 1 {
		t.Errorf("the host's store: %v", h.vals)
	}
	if s.states != nil {
		t.Error("a default store was made beside the host's")
	}
}

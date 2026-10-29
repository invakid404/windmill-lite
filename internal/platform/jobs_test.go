package platform

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// The registry knows a run and its jobs while the run lives (D3): a script
// job's parent flow, innermost scope and root become known with it, a job
// that ended stays known while its root runs, and the tree is forgotten
// once the root and every job of it have ended.
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
	for _, id := range []string{"run", "step", "child", "inner"} {
		if _, _, ok := r.Job(id); ok {
			t.Errorf("%s is known after its run ended", id)
		}
	}
	if len(r.jobs) != 0 || len(r.trees) != 0 || len(r.state) != 0 {
		t.Errorf("left %d jobs, %d trees, %d maps", len(r.jobs), len(r.trees), len(r.state))
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
	// WA-14/c: after the run a read by its id is null; the write is
	// refused, since the map was discarded with the run (Q-WA-10).
	if got := get("run", "k"); got != "null" {
		t.Errorf("after the run: %s", got)
	}
	if err := s.SetFlowUserState(ctx, "run", "k", []byte("1")); !errors.As(err, &pe) || pe.HTTPStatus() != 404 {
		t.Errorf("a write after the run: %v", err)
	}
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
	if (&Services{Jobs: custom}).JobRegistry() != Jobs(custom) {
		t.Error("Jobs is not used")
	}
}

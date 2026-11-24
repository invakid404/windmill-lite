package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// A run whose id a job of another workspace holds in the job registry,
// live or ended, is refused before it starts, a flow run and a root
// script's alike, configured or not: runs of two workspaces never share an
// entry of the registry. A run of the same workspace takes the id, as it
// always did.
func TestRunIDHeldByAnotherWorkspace(t *testing.T) {
	services := &platform.Services{}
	ran := 0
	x := execFunc(func(_ context.Context, inv Invocation) (StepOutcome, error) {
		ran++
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""))}, x, services)
	const live, ended = "00000000-0000-4000-8000-0000000000d1", "00000000-0000-4000-8000-0000000000d2"
	jobs := services.JobRegistry()
	defer jobs.Begin(platform.Job{ID: live, Kind: platform.JobScript, Root: "00000000-0000-4000-8000-0000000000d0", Host: platform.Host{Workspace: "ws-a"}})()
	jobs.Begin(platform.Job{ID: ended, Kind: platform.JobFlow, Run: ended, Host: platform.Host{Workspace: "ws-a"}})()
	args := jsonval.FromProducer([]byte(`{}`))
	for _, id := range []string{live, ended} {
		for _, host := range []platform.Host{{Workspace: "ws-b"}, {}} {
			if _, err := e.Run(context.Background(), RunRequest{ID: id, Path: "f/t/main", Args: args, Host: host}); err == nil || !strings.Contains(err.Error(), "held by a job of another workspace") {
				t.Errorf("a flow run of %q with id %s: %v, want refused", host.Workspace, id, err)
			}
			if _, err := e.RunScript(context.Background(), ScriptRequest{ID: id, Path: "f/t/echo", Args: args, Host: host}); err == nil || !strings.Contains(err.Error(), "held by a job of another workspace") {
				t.Errorf("a script run of %q with id %s: %v, want refused", host.Workspace, id, err)
			}
		}
	}
	if ran != 0 {
		t.Errorf("%d steps of refused runs ran", ran)
	}
	tr, err := e.Run(context.Background(), RunRequest{ID: ended, Path: "f/t/main", Args: args, Host: platform.Host{Workspace: "ws-a"}})
	if err != nil || tr.Status != "success" {
		t.Fatalf("ws-a's run with its own ended id: %v, %+v", err, tr)
	}
}

// forwardingJobs is a job registry that forwards to another, as a host's
// wrapper around the services' own registry would: it is not their own.
type forwardingJobs struct{ platform.Jobs }

// hostStates is a host's flow user state store over a map.
type hostStates struct {
	mu   sync.Mutex
	vals map[string]map[string]json.RawMessage
}

func (h *hostStates) UserState(_ context.Context, job, key string) (json.RawMessage, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.vals[job][key]
	return v, ok, nil
}

func (h *hostStates) SetUserState(_ context.Context, job, key string, v json.RawMessage) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.vals[job] == nil {
		h.vals[job] = map[string]json.RawMessage{}
	}
	h.vals[job][key] = v
	return nil
}

// A run in a workspace other than the engine's configured one, which only a
// run's own identity (RunRequest.Host, ScriptRequest.Host) can ask for,
// needs the services' own job registry and flow user state store, which
// alone keep workspaces apart: with a host's registry (here one that forwards
// to the services' own) or a host's store, such a flow run and root script
// run are refused before any step, while runs in the engine's workspace run.
func TestWorkspaceNeedsOwnServices(t *testing.T) {
	for name, services := range map[string]func() *platform.Services{
		"a host's registry": func() *platform.Services {
			s := &platform.Services{}
			s.Jobs = forwardingJobs{s.Registry()}
			return s
		},
		"a host's store": func() *platform.Services {
			return &platform.Services{UserState: &hostStates{vals: map[string]map[string]json.RawMessage{}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ran := 0
			x := execFunc(func(_ context.Context, inv Invocation) (StepOutcome, error) {
				ran++
				return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
			})
			e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""))}, x, services())
			args := jsonval.FromProducer([]byte(`{}`))
			ws := platform.Host{Workspace: "ws-b"}
			if _, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: args, Host: ws}); err == nil || !strings.Contains(err.Error(), "needs the services' own job registry") {
				t.Errorf("a flow run of ws-b: %v, want refused", err)
			}
			if _, err := e.RunScript(context.Background(), ScriptRequest{Path: "f/t/echo", Args: args, Host: ws}); err == nil || !strings.Contains(err.Error(), "needs the services' own job registry") {
				t.Errorf("a script run of ws-b: %v, want refused", err)
			}
			if ran != 0 {
				t.Errorf("%d steps of refused runs ran", ran)
			}
			if tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: args}); err != nil || tr.Status != "success" {
				t.Errorf("a run in the engine's workspace: %v, %+v", err, tr)
			}
		})
	}
}

// openInWorkspace is openWithPlatform for an engine configured with the
// workspace ws (Config.Workspace), as a host sets it without identities.
func openInWorkspace(t *testing.T, flows map[string]string, exec StepExecutor, p *platform.Services, ws string) *Engine {
	t.Helper()
	jsOnce.Do(func() { jsEng, jsErr = newTestJS() })
	if jsErr != nil {
		t.Fatal(jsErr)
	}
	folder, err := syncfolder.Load(testFolder(flows))
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	prog, err := ir.Compile(folder, nil, ir.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Program: prog, Bundle: "sha256:test", Exec: exec, JS: jsEng, Platform: p, Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// A compatibility control: an engine configured with a workspace other than
// windmill-lite (Config.Workspace) over a host's job registry, or a host's
// flow user state store, runs its flows and root scripts as it always did.
// The host's registry registers the run, and the host's store keeps the
// run's state under the run's id.
func TestEngineWorkspaceWithHostServices(t *testing.T) {
	const id, scriptID = "00000000-0000-4000-8000-0000000000e1", "00000000-0000-4000-8000-0000000000e2"
	for _, tc := range []struct {
		name  string
		build func() (*platform.Services, *hostStates)
	}{
		{"a host's registry", func() (*platform.Services, *hostStates) {
			s := &platform.Services{}
			s.Jobs = forwardingJobs{s.Registry()}
			return s, nil
		}},
		{"a host's store", func() (*platform.Services, *hostStates) {
			store := &hostStates{vals: map[string]map[string]json.RawMessage{}}
			return &platform.Services{UserState: store}, store
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			services, store := tc.build()
			var workspace, state, root string
			x := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
				workspace = inv.Env["WM_WORKSPACE"]
				if inv.Env["WM_FLOW_JOB_ID"] == id {
					if err := services.SetFlowUserState(ctx, id, "k", []byte(`"kept"`)); err != nil {
						return StepOutcome{}, err
					}
					v, _ := services.FlowUserState(ctx, id, "k")
					state = string(v)
					root, _ = services.RootJob(id)
				}
				return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
			})
			e := openInWorkspace(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""))}, x, services, "ws-x")
			args := jsonval.FromProducer([]byte(`{}`))
			if tr, err := e.Run(context.Background(), RunRequest{ID: id, Path: "f/t/main", Args: args}); err != nil || tr.Status != "success" {
				t.Fatalf("the flow run: %v, %+v", err, tr)
			}
			if workspace != "ws-x" || state != `"kept"` || root != id {
				t.Errorf("the flow's step saw workspace %q, state %s, root %q; want ws-x, \"kept\", %s", workspace, state, root, id)
			}
			if store != nil && string(store.vals[id]["k"]) != `"kept"` {
				t.Errorf("the host's store holds %v, want the run's state under its id", store.vals)
			}
			if r, err := e.RunScript(context.Background(), ScriptRequest{ID: scriptID, Path: "f/t/echo", Args: args}); err != nil || r.Failed {
				t.Fatalf("the root script run: %v, %+v", err, r)
			}
			if workspace != "ws-x" {
				t.Errorf("the root script saw workspace %q, want ws-x", workspace)
			}
		})
	}
}

// Where the workspace guarantees do not reach, behaviour is the base's: a
// default-workspace engine whose host's registry forwards to a registry that
// another engine's run of ws-a uses registers its run as it always did, with
// no refusal, though ws-a's job holds the id. Workspaces are kept apart only
// through the services' own registry (see TestSharedOwnRegistryRefusesOverlap).
func TestHostRegistryKeepsBaseRegistration(t *testing.T) {
	shared := &platform.Services{}
	const id = "00000000-0000-4000-8000-0000000000e3"
	defer shared.JobRegistry().Begin(platform.Job{ID: id, Kind: platform.JobScript, Root: "00000000-0000-4000-8000-0000000000e4", Run: "00000000-0000-4000-8000-0000000000e4", Host: platform.Host{Workspace: "ws-a"}})()
	host := &platform.Services{Jobs: forwardingJobs{shared.Registry()}}
	x := execFunc(func(_ context.Context, inv Invocation) (StepOutcome, error) {
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""))}, x, host)
	if tr, err := e.Run(context.Background(), RunRequest{ID: id, Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))}); err != nil || tr.Status != "success" {
		t.Fatalf("the default run through a host's registry: %v, %+v; want it to run, as at the base", err, tr)
	}
}

// The services' own registry, shared between engines (WithProviders gives a
// copy of the services the same registry and store), never lets runs of two
// workspaces share an id: a default-workspace engine's flow run and root
// script run with an id that the other engine's run of ws-a holds, or that
// a step of ws-a holds as its WM_JOB_ID, or that an ended job of ws-a keeps,
// are refused before they register anything. No job, tree or state of
// theirs can so ever sit under that id.
func TestSharedOwnRegistryRefusesOverlap(t *testing.T) {
	own := &platform.Services{}
	copied := own.WithProviders(platform.Providers{})
	if !copied.OwnsJobs() {
		t.Fatal("a copy of the services' own does not own its registry and store")
	}
	const runA, stepA = "00000000-0000-4000-8000-0000000000e5", "00000000-0000-4000-8000-0000000000e6"
	var refusals []string
	args := jsonval.FromProducer([]byte(`{}`))
	var other *Engine
	try := func(id string) {
		if _, err := other.Run(context.Background(), RunRequest{ID: id, Path: "f/t/main", Args: args}); err == nil || !strings.Contains(err.Error(), "held by a job of another workspace") {
			refusals = append(refusals, "flow "+id+": "+errString(err))
		}
		if _, err := other.RunScript(context.Background(), ScriptRequest{ID: id, Path: "f/t/echo", Args: args}); err == nil || !strings.Contains(err.Error(), "held by a job of another workspace") {
			refusals = append(refusals, "script "+id+": "+errString(err))
		}
	}
	steps := 0
	xa := execFunc(func(_ context.Context, inv Invocation) (StepOutcome, error) {
		// While ws-a's run lives, the default engine tries its id.
		try(runA)
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	xb := execFunc(func(_ context.Context, inv Invocation) (StepOutcome, error) {
		steps++
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	flows := map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""))}
	a := openWithPlatform(t, flows, xa, own)
	other = openWithPlatform(t, flows, xb, copied)
	if tr, err := a.Run(context.Background(), RunRequest{ID: runA, Path: "f/t/main", Args: args, Host: platform.Host{Workspace: "ws-a"}}); err != nil || tr.Status != "success" {
		t.Fatalf("ws-a's run: %v, %+v", err, tr)
	}
	// A step of ws-a that holds an id as its WM_JOB_ID, as the facade
	// registers it, and then the same id ended and kept.
	end := own.JobRegistry().Begin(platform.Job{ID: stepA, Kind: platform.JobScript, Root: runA, Run: runA, Host: platform.Host{Workspace: "ws-a"}})
	try(stepA)
	end()
	try(stepA)
	try(runA)
	if len(refusals) != 0 {
		t.Errorf("not refused: %v", refusals)
	}
	if steps != 0 {
		t.Errorf("%d steps of the default engine's runs ran", steps)
	}
}

// The record of a run's flow job lists the flow user state of the
// registration that it is rendered for: the run's own lists the state its
// step wrote, and another registration of the id (another number, or
// another workspace) lists none.
func TestRunRecordListsTheCheckedRegistration(t *testing.T) {
	services := &platform.Services{}
	const id = "00000000-0000-4000-8000-0000000000da"
	records := map[string]string{}
	x := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
		if err := services.SetFlowUserStateIn(ctx, id, "k", []byte(`"mine"`), "ws-a"); err != nil {
			return StepOutcome{}, err
		}
		j, _, _ := services.JobRegistry().Job(id)
		later, elsewhere := j, j
		later.Gen++
		elsewhere.Host = platform.Host{Workspace: "ws-b"}
		for name, reg := range map[string]platform.Job{"own": j, "later": later, "elsewhere": elsewhere} {
			b, err := j.View.JobRecord(reg)
			if err != nil {
				records[name] = err.Error()
			} else {
				records[name] = string(b)
			}
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""))}, x, services)
	if _, err := e.Run(context.Background(), RunRequest{ID: id, Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Host: platform.Host{Workspace: "ws-a"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(records["own"], `"mine"`) {
		t.Errorf("the run's record for its own registration: %s; want its state", records["own"])
	}
	for _, name := range []string{"later", "elsewhere"} {
		if !strings.Contains(records[name], `"id":"`+id+`"`) || strings.Contains(records[name], `"mine"`) {
			t.Errorf("the run's record for another registration (%s): %s; want the record without the state", name, records[name])
		}
	}
}

// A child flow job's record lists its flow user state while the child runs,
// and none once it has completed, though the run that holds it lives on:
// state is listed for a live flow job only, as it always was.
func TestCompletedChildRecordListsNoState(t *testing.T) {
	services := &platform.Services{}
	var mu sync.Mutex
	var child, live, completed string
	x := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
		mu.Lock()
		defer mu.Unlock()
		switch inv.Env["WM_FLOW_STEP_ID"] {
		case "s":
			child = inv.Env["WM_FLOW_JOB_ID"]
			if err := services.SetFlowUserState(ctx, child, "k", []byte(`"child's"`)); err != nil {
				return StepOutcome{}, err
			}
			b, err := services.GetJob(child)
			live = string(b) + errString(err)
		case "after":
			b, err := services.GetJob(child)
			completed = string(b) + errString(err)
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(
		branchAllOf("b", []string{`{"modules":[` + mod("s", "echo", "", "") + `]}`}, false, ""),
		mod("after", "echo", "", ""),
	)}, x, services)
	if tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))}); err != nil || tr.Status != "success" {
		t.Fatalf("%v, %+v", err, tr)
	}
	if !strings.Contains(live, `"child's"`) {
		t.Errorf("the running child's record: %s; want its state", live)
	}
	if !strings.Contains(completed, `"id":"`+child+`"`) || strings.Contains(completed, "user_states") {
		t.Errorf("the completed child's record: %s; want it without user_states", completed)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return " " + err.Error()
}

// An engine with no services registers no job, so it keeps nothing to set
// apart: a run whose own identity names another workspace than the
// engine's runs on it.
func TestWorkspaceWithoutServices(t *testing.T) {
	var workspace string
	x := execFunc(func(_ context.Context, inv Invocation) (StepOutcome, error) {
		workspace = inv.Env["WM_WORKSPACE"]
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	e := openInWorkspace(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""))}, x, nil, "")
	if tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Host: platform.Host{Workspace: "ws-b"}}); err != nil || tr.Status != "success" || workspace != "ws-b" {
		t.Fatalf("a run of ws-b on an engine with no services: %v, %+v, workspace %q", err, tr, workspace)
	}
}

// verbatimJobs is a host's job registry that keeps each job as it was
// submitted, as a host's Jobs may: it does not give a job its run's Host.
type verbatimJobs struct {
	mu   sync.Mutex
	jobs map[string]platform.Job
	live map[string]int
}

func (v *verbatimJobs) Begin(j platform.Job) func() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if j.Root == "" {
		j.Root = j.ID
	}
	if old, ok := v.jobs[j.ID]; !ok || old.View == nil {
		v.jobs[j.ID] = j
	}
	v.live[j.ID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			v.mu.Lock()
			defer v.mu.Unlock()
			v.live[j.ID]--
		})
	}
}

func (v *verbatimJobs) Job(id string) (platform.Job, bool, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	j, ok := v.jobs[id]
	return j, v.live[id] > 0, ok
}

// A compatibility control: an engine configured with a workspace over a
// host's registry that keeps jobs as submitted (so a child flow job comes
// back without a Host) still finds a child flow's job through that
// workspace, as the job reads of the engine's own workspace always did: its
// arguments, its root, and its flow user state.
func TestEngineWorkspaceWithAVerbatimRegistry(t *testing.T) {
	services := &platform.Services{Jobs: &verbatimJobs{jobs: map[string]platform.Job{}, live: map[string]int{}}}
	var args, root, state string
	var child string
	x := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
		child = inv.Env["WM_FLOW_JOB_ID"]
		b, err := services.GetJobArgsIn(child, "ws-x")
		args = string(b) + errString(err)
		root, _ = services.RootJobIn(child, "ws-x")
		if err := services.SetFlowUserStateIn(ctx, child, "k", []byte(`"child's"`), "ws-x"); err != nil {
			state = err.Error()
		} else {
			v, _ := services.FlowUserStateIn(ctx, child, "k", "ws-x")
			state = string(v)
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	e := openInWorkspace(t, map[string]string{"f/t/main": flowOf(
		branchAllOf("b", []string{`{"modules":[` + mod("s", "echo", "", "") + `]}`}, false, ""),
	)}, x, services, "ws-x")
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{"n":1}`))})
	if err != nil || tr.Status != "success" {
		t.Fatalf("%v, %+v", err, tr)
	}
	if child == tr.RunID || !strings.HasPrefix(args, "{") || root != tr.RunID || state != `"child's"` {
		t.Errorf("the branch child %s through ws-x: args %s, root %q, state %s; want its arguments, the run %s and its state", child, args, root, state, tr.RunID)
	}
}

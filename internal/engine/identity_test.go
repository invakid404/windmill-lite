package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
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

// A run in a workspace other than windmill-lite needs the services' own job
// registry and flow user state store, which alone keep workspaces apart: with
// a host's registry (here one that forwards to the services' own) or a host's
// store, a flow run and a root script run of another workspace are refused
// before any step, while runs of windmill-lite run as they always did.
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
				t.Errorf("a run of windmill-lite: %v, %+v", err, tr)
			}
		})
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

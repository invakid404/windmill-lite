package engine

import (
	"context"
	"encoding/json"
	"maps"
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

// switchingJobs is a job registry that, once armed, answers the third and
// later lookups of id with other, as if the run that held id ended and a
// run of another workspace registered it between a job read's check and
// its state listing.
type switchingJobs struct {
	platform.Jobs
	id    string
	other platform.Job
	mu    sync.Mutex
	armed bool
	seen  int
}

func (j *switchingJobs) Job(id string) (platform.Job, bool, bool) {
	j.mu.Lock()
	if id == j.id && j.armed {
		j.seen++
		if j.seen > 2 {
			j.mu.Unlock()
			return j.other, true, true
		}
	}
	j.mu.Unlock()
	return j.Jobs.Job(id)
}

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

func (h *hostStates) UserStates(_ context.Context, job string) (map[string]json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return maps.Clone(h.vals[job]), nil
}

// The record of a run's flow job lists the flow user state of the run's
// registration that the job read checked: when another workspace's run
// takes the id between the check and the listing, the record lists none
// of that run's state.
func TestRunRecordListsTheCheckedRegistration(t *testing.T) {
	const id = "00000000-0000-4000-8000-0000000000da"
	store := &hostStates{vals: map[string]map[string]json.RawMessage{
		"ws-b/" + id: {"k": json.RawMessage(`"other's"`)},
	}}
	services := &platform.Services{UserState: store}
	jobs := &switchingJobs{Jobs: services.Registry(), id: id, other: platform.Job{ID: id, Kind: platform.JobFlow, Root: id, Run: id, Host: platform.Host{Workspace: "ws-b"}}}
	services.Jobs = jobs
	var record string
	x := execFunc(func(_ context.Context, inv Invocation) (StepOutcome, error) {
		jobs.mu.Lock()
		jobs.armed = true
		jobs.mu.Unlock()
		b, err := services.GetJobIn(id, "ws-a")
		if err != nil {
			record = err.Error()
		} else {
			record = string(b)
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""))}, x, services)
	if _, err := e.Run(context.Background(), RunRequest{ID: id, Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Host: platform.Host{Workspace: "ws-a"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(record, `"id":"`+id+`"`) || strings.Contains(record, "other's") {
		t.Errorf("the run's record: %s; want the run's own, without the other run's state", record)
	}
}

package engine

import (
	"context"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The child flow jobs of a run (a branch's, the iterations of a loop and of
// a parallel loop) register in the run's workspace, even when the run's id
// is a live job of another workspace, as a hook's WM_JOB_ID can make it:
// the run's configured workspace, or windmill-lite for a run with none,
// never the workspace of the job that holds the id.
func TestCollidingRunChildWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name string
		host platform.Host
		want string
	}{
		{"configured", platform.Host{Workspace: "ws-b"}, "ws-b"},
		{"not configured", platform.Host{}, platform.DefaultHost.Workspace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			services := &platform.Services{}
			const id = "00000000-0000-4000-8000-0000000000c9"
			defer services.JobRegistry().Begin(platform.Job{ID: id, Kind: platform.JobScript, Root: "00000000-0000-4000-8000-0000000000c8", Run: "00000000-0000-4000-8000-0000000000c8", Host: platform.Host{Workspace: "ws-a"}})()
			var mu sync.Mutex
			scopes := map[string]string{}
			x := execFunc(func(_ context.Context, inv Invocation) (StepOutcome, error) {
				flow := inv.Env["WM_FLOW_JOB_ID"]
				ws, _ := services.JobScope(flow)
				mu.Lock()
				scopes[flow] = ws
				mu.Unlock()
				return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
			})
			e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(
				branchAllOf("b", []string{`{"modules":[` + mod("s", "echo", "", "") + `]}`}, false, ""),
				loopOf("l", []string{mod("t", "echo", "", ""), mod("u", "echo", "", "")}, `"iterator":{"type":"static","value":[1,2]}`, ""),
				loopOf("p", []string{mod("v", "echo", "", ""), mod("w", "echo", "", "")}, `"iterator":{"type":"static","value":[1,2]},"parallel":true`, ""),
			)}, x, services)
			tr, err := e.Run(context.Background(), RunRequest{ID: id, Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Host: tc.host})
			if err != nil {
				t.Fatal(err)
			}
			if tr.Status != "success" {
				t.Fatalf("%s %s", tr.Status, tr.Result)
			}
			children := 0
			for flow, ws := range scopes {
				if flow == id {
					continue
				}
				children++
				if ws != tc.want {
					t.Errorf("the flow job %s that a step ran in is in %q, want %s", flow, ws, tc.want)
				}
			}
			if children < 5 {
				t.Errorf("steps ran in %d child flow jobs, want the branch's and four iterations': %v", children, scopes)
			}
		})
	}
}

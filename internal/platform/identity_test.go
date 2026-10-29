package platform

import (
	"strings"
	"testing"
)

// WA-5's state path, for a job no schedule triggered: WA-5/a (a flow step)
// and WA-5/c (a standalone script), and the formula's other branches.
func TestStatePath(t *testing.T) {
	for _, c := range []struct{ flow, step, job, want string }{
		{"f/t/state", "a", "f/t/state/a", "f/t/state/a_user"}, // WA-5/a
		{"", "", "u/bob/s", "u/bob/s/user"},                   // WA-5/c
		{"f/t/state", "", "", "f/t/state/nostep_user"},
		{"", "", "u/bob/dir/", "u/bob/dir/state/user"},
		{"", "", "", "u/someone/tmp_state"},
	} {
		if got := StatePath(c.flow, c.step, c.job, "someone"); got != c.want {
			t.Errorf("StatePath(%q, %q, %q) = %q, want %q", c.flow, c.step, c.job, got, c.want)
		}
	}
}

// The job variables of WA-4's core set and the host's, with DefaultHost
// filling what a host leaves out.
func TestIdentityEnv(t *testing.T) {
	id := Identity{JobID: "j", FlowJobID: "f", RootFlowJobID: "f", RootJobID: "f", FlowPath: "f/t/envs", StepID: "b", JobPath: "f/t/envs/b", Token: "tok"}
	got := id.Env(Host{Workspace: "ws", Username: "alice"})
	want := map[string]string{
		"WM_WORKSPACE": "ws", "WM_TOKEN": "tok", "WM_EMAIL": "windmill-lite@localhost", "WM_USERNAME": "alice",
		"WM_PERMISSIONED_AS": "u/alice", "WM_BASE_URL": "http://localhost:3000", "WM_JOB_ID": "j", "WM_FLOW_JOB_ID": "f",
		"WM_ROOT_FLOW_JOB_ID": "f", "WM_ROOT_JOB_ID": "f", "WM_FLOW_PATH": "f/t/envs", "WM_FLOW_STEP_ID": "b",
		"WM_JOB_PATH": "f/t/envs/b", "WM_STATE_PATH": "f/t/envs/b_user",
	}
	if len(got) != len(want) {
		t.Errorf("%d variables, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if d := (Identity{}).Env(Host{}); d["WM_WORKSPACE"] != "windmill-lite" || d["WM_PERMISSIONED_AS"] != "u/windmill-lite" {
		t.Errorf("defaults: %v", d)
	}
}

func TestNewToken(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b || !strings.HasPrefix(a, "wl_") || len(a) != 35 {
		t.Errorf("tokens %q and %q", a, b)
	}
}

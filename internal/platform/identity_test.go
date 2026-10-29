package platform

import (
	"strings"
	"testing"
	"time"
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

// Every reserved variable of WA-4 (WR-22, the 21 of OR-29's environment
// probe): the core set, the host's, with DefaultHost filling what a host
// leaves out, and the others a job windmill-lite runs has: its scheduled
// time in whole seconds, its object path, and the variables no schedule,
// app, CI test or script hash fills, empty.
func TestIdentityEnv(t *testing.T) {
	start := time.Date(2026, 10, 29, 12, 0, 1, 789_000_000, time.UTC)
	id := Identity{JobID: "j", FlowJobID: "f", RootFlowJobID: "f", RootJobID: "f", FlowPath: "f/t/envs", StepID: "b", JobPath: "f/t/envs/b", Token: "tok", Start: start}
	got := id.Env(Host{Workspace: "ws", Username: "alice"})
	want := map[string]string{
		"WM_WORKSPACE": "ws", "WM_TOKEN": "tok", "WM_EMAIL": "windmill-lite@localhost", "WM_USERNAME": "alice",
		"WM_PERMISSIONED_AS": "u/alice", "WM_BASE_URL": "http://localhost:3000", "WM_JOB_ID": "j", "WM_FLOW_JOB_ID": "f",
		"WM_ROOT_FLOW_JOB_ID": "f", "WM_ROOT_JOB_ID": "f", "WM_FLOW_PATH": "f/t/envs", "WM_FLOW_STEP_ID": "b",
		"WM_JOB_PATH": "f/t/envs/b", "WM_STATE_PATH": "f/t/envs/b_user",
		"WM_SCHEDULED_FOR": "2026-10-29T12:00:01Z", "WM_SCHEDULE_PATH": "", "WM_WORKER_GROUP": "default",
		"WM_OBJECT_PATH": "f_t_envs/manual/b/1793275201789_j", "WM_RUNNABLE_ID": "", "WM_END_USER_EMAIL": "", "WM_TESTED_RUNNABLE": "",
	}
	if len(got) != len(want) {
		t.Errorf("%d variables, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if d := (Identity{}).Env(Host{}); d["WM_WORKSPACE"] != "windmill-lite" || d["WM_PERMISSIONED_AS"] != "u/windmill-lite" || d["WM_WORKER_GROUP"] != "default" || d["WM_SCHEDULED_FOR"] == "" {
		t.Errorf("defaults: %v", d)
	}
	// A script job outside a flow: its runnable path, with no step part.
	if p := ObjectPath("", "", "f/t/script", start, "j"); p != "f_t_script/manual/1793275201789_j" {
		t.Errorf("a script job's object path %q", p)
	}
}

func TestNewToken(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b || !strings.HasPrefix(a, "wl_") || len(a) != 35 {
		t.Errorf("tokens %q and %q", a, b)
	}
}

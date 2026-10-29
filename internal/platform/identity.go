// Package platform holds what the platform interface of ADR 0007 shares
// between the engine and the facade: job identity (milestone PF1), the
// variables of WR-22 and WA-4 that every invocation's environment carries.
// The facade's router, its transports and the 501 policy are in
// internal/platform/facade; the services behind it arrive with milestones
// PF2 and PF3.
package platform

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Host is the part of a job's environment that comes from the instance and
// the run's user rather than from the run (ADR 0007, job identity): the
// workspace, the user the run is permissioned as, and the instance's
// public URL. Zero fields take DefaultHost's.
type Host struct {
	// Workspace is WM_WORKSPACE, the workspace id.
	Workspace string
	// Username is WM_USERNAME (the job's created_by), and Email WM_EMAIL
	// (permissioned_as_email).
	Username string
	Email    string
	// PermissionedAs is WM_PERMISSIONED_AS, u/<user> or g/<group>; empty
	// means u/<Username>.
	PermissionedAs string
	// BaseURL is WM_BASE_URL, the instance's public URL, which the stock
	// client uses for the links it builds, never for its calls (WA-1).
	BaseURL string
}

// DefaultHost is the host configuration of a host that sets none. The
// values are windmill-lite's own placeholders, not Windmill's: no instance
// or user exists to take them from. BaseURL is the stock client's own
// default for an unset WM_BASE_URL (WA-1), so a client reads the same
// links either way.
var DefaultHost = Host{
	Workspace: "windmill-lite",
	Username:  "windmill-lite",
	Email:     "windmill-lite@localhost",
	BaseURL:   "http://localhost:3000",
}

// withDefaults returns h with its zero fields taken from DefaultHost.
func (h Host) withDefaults() Host {
	if h.Workspace == "" {
		h.Workspace = DefaultHost.Workspace
	}
	if h.Username == "" {
		h.Username = DefaultHost.Username
	}
	if h.Email == "" {
		h.Email = DefaultHost.Email
	}
	if h.PermissionedAs == "" {
		h.PermissionedAs = "u/" + h.Username
	}
	if h.BaseURL == "" {
		h.BaseURL = DefaultHost.BaseURL
	}
	return h
}

// Identity is one invocation's job identity (WA-4, WR-22, WA-V1): the job
// it is and the flow scopes it runs in. The engine fills it from the run;
// a step outside any flow (a script job) has no flow fields.
type Identity struct {
	// JobID is WM_JOB_ID, a fresh UUID per invocation, as a job id would
	// be (D3).
	JobID string
	// FlowJobID is WM_FLOW_JOB_ID: the job of the flow scope the step runs
	// in (its parent job), or "" for a script job. Inline bodies that
	// Windmill runs as child flows give their steps the child flow's id;
	// collapsed loop iterations keep the enclosing flow's (FX-5, FX-46).
	FlowJobID string
	// RootFlowJobID is WM_ROOT_FLOW_JOB_ID, the innermost flow scope
	// (WA-V1), or "".
	RootFlowJobID string
	// RootJobID is WM_ROOT_JOB_ID, the effective root: the first of the
	// stored root, the innermost scope, the parent and the job itself
	// that is set, so it is never empty (WA-4, WA-V1). Flow user state is
	// keyed on it, and getRootJobId answers it (WA-14).
	RootJobID string
	// FlowPath is WM_FLOW_PATH, the parent's runnable path (WA-6), or "".
	FlowPath string
	// StepID is WM_FLOW_STEP_ID: the module id, preprocessor for the
	// preprocessor, the loop module's id for a collapsed iteration
	// (FX-46), or "" for a script job.
	StepID string
	// JobPath is WM_JOB_PATH, the job's runnable path: <flow path>/<step
	// id> for an inline step, the script's own path for a path script
	// (WA-6, WR-29).
	JobPath string
	// Token is WM_TOKEN. The facade never attributes a call by the token a
	// step sends on the intercepted path, because the stock client
	// captures it at import (WA-1, WA-31); on loopback TCP, where a process
	// is one invocation, the token names it (ADR 0007).
	Token string
}

// Env returns the identity's job variables with h's host configuration:
// the core variables of WA-4 (WM_WORKSPACE, WM_TOKEN, WM_BASE_URL,
// WM_JOB_ID, WM_FLOW_JOB_ID, WM_ROOT_FLOW_JOB_ID, WM_ROOT_JOB_ID,
// WM_FLOW_PATH, WM_FLOW_STEP_ID, WM_JOB_PATH, WM_STATE_PATH) and the host's
// WM_EMAIL, WM_USERNAME and WM_PERMISSIONED_AS. The other variables of the
// table are `later` and are not set; BASE_URL belongs to the process, not
// the invocation (ADR 0004).
func (id Identity) Env(h Host) map[string]string {
	h = h.withDefaults()
	return map[string]string{
		"WM_WORKSPACE":        h.Workspace,
		"WM_TOKEN":            id.Token,
		"WM_EMAIL":            h.Email,
		"WM_USERNAME":         h.Username,
		"WM_PERMISSIONED_AS":  h.PermissionedAs,
		"WM_BASE_URL":         h.BaseURL,
		"WM_JOB_ID":           id.JobID,
		"WM_FLOW_JOB_ID":      id.FlowJobID,
		"WM_ROOT_FLOW_JOB_ID": id.RootFlowJobID,
		"WM_ROOT_JOB_ID":      id.RootJobID,
		"WM_FLOW_PATH":        id.FlowPath,
		"WM_FLOW_STEP_ID":     id.StepID,
		"WM_JOB_PATH":         id.JobPath,
		"WM_STATE_PATH":       StatePath(id.FlowPath, id.StepID, id.JobPath, h.Username),
	}
}

// StatePath is WM_STATE_PATH (WA-5), for a job that no schedule
// triggered, which is every job windmill-lite runs (schedules are out of
// scope, ADR 0001 §3), so the trigger part is always "user":
//
//   - a flow step: <flow path>/<step id, or "nostep">_user;
//   - a script job with a path: <script path>/user, after appending
//     "state" to a path that ends with "/";
//   - otherwise: u/<username>/tmp_state.
func StatePath(flowPath, stepID, jobPath, username string) string {
	const trigger = "user"
	switch {
	case flowPath != "":
		if stepID == "" {
			stepID = "nostep"
		}
		return flowPath + "/" + stepID + "_" + trigger
	case jobPath != "":
		if strings.HasSuffix(jobPath, "/") {
			jobPath += "state"
		}
		return jobPath + "/" + trigger
	default:
		return "u/" + username + "/tmp_state"
	}
}

// NewToken returns a fresh WM_TOKEN: 128 random bits, unguessable, so a
// token names its invocation on loopback TCP and nothing else can forge
// it (ADR 0007).
func NewToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("platform: no randomness for a token: " + err.Error())
	}
	return "wl_" + hex.EncodeToString(b[:])
}

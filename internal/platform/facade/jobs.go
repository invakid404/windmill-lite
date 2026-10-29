package facade

import (
	"context"
	"encoding/json"
)

// rootJobID answers getRootJobId, GET /w/{ws}/jobs_u/get_root_job_id/{id}
// (WA-8, WA-14): the effective root of a job (WA-V1), as a JSON string.
// It answers from the identities of the invocations registered now (D3):
// the job of any of them, and the flow scopes and root each runs in, all
// of which have that invocation's effective root. A job it does not know
// of (one whose invocations have all settled, or another workspace's)
// answers 501: answering it needs the job registry of milestone PF3, and
// a guess would be worse than a loud failure.
func (f *Facade) rootJobID(_ context.Context, c *Call) Reply {
	id := c.Params["id"]
	if root, ok := f.rootOf(id); ok {
		body, _ := json.Marshal(root)
		return Reply{Status: 200, Headers: map[string]string{"content-type": "application/json"}, Body: body}
	}
	return text(501, UnsupportedPrefix+c.Route.Name())
}

// rootOf finds the effective root of a job among the registered
// invocations.
func (f *Facade) rootOf(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.invs {
		env := e.caller.Env
		root := env["WM_ROOT_JOB_ID"]
		if root == "" {
			continue
		}
		for _, k := range []string{"WM_JOB_ID", "WM_FLOW_JOB_ID", "WM_ROOT_FLOW_JOB_ID", "WM_ROOT_JOB_ID"} {
			if env[k] == id {
				return root, true
			}
		}
	}
	return "", false
}

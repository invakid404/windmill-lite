package facade

import (
	"context"
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The job routes of ADR 0007's services: the root lookup, from the job
// registry (D3); flow user state (WA-14); and the result polls of the
// runs a step dispatched (WA-15, WA-V2). Each route's job id is a UUID
// path parameter, which Windmill's web framework refuses with a 400 of
// its own before the handler runs when it is not one (pathUUID).
func (f *Facade) serveJobs() {
	f.Handle("getRootJobId", f.rootJobID)
	f.Handle("getFlowUserState", f.getFlowUserState)
	f.Handle("setFlowUserState", f.setFlowUserState)
	f.Handle("getCompletedJobResultMaybe", f.resultMaybe)
	f.Handle("getCompletedJobResult", f.result)
}

// noRows is the database's text for a lookup that expected a row and
// found none, which the root lookup answers as SqlErr (WA-19).
const noRows = "no rows returned by a query that expected to return at least one row"

// rootJobID answers getRootJobId, GET /w/{ws}/jobs_u/get_root_job_id/{id}
// (WA-8, WA-14, WA-19): the effective root of a job (WA-V1), as a JSON
// string, from the job registry: a job of a run that is still running, a
// flow scope one of its jobs runs in, the run itself, or a job of a run
// that has ended, which the registry still knows (platform.Registry). An
// id with no job in the path's workspace (one the registry does not know
// of, or a job looked up in a workspace other than the caller's) answers
// as Windmill answers an id with no job row: 400 `SqlErr: no rows
// returned by a query that expected to return at least one row`, without
// Windmill's Rust location (D14). The route checks no token (WA-4).
func (f *Facade) rootJobID(_ context.Context, c *Call) Reply {
	id, refused := pathUUID(c, "id", "id")
	if refused != nil {
		return *refused
	}
	if ownWorkspace(c) {
		if root, ok := f.platform.RootJob(id); ok {
			return jsonReply(200, jsonString(root))
		}
	}
	return Error{Variant: "SqlErr", Message: noRows}.Reply()
}

// getFlowUserState answers GET /w/{ws}/jobs/flow/user_states/{id}/{key}
// (WA-14): the value stored at key for the flow job, JSON null when there
// is none, when the job is not a live flow job, and when it has ended.
func (f *Facade) getFlowUserState(ctx context.Context, c *Call) Reply {
	id, refused := pathUUID(c, "id", "job_id")
	if refused != nil {
		return *refused
	}
	v, err := f.platform.FlowUserState(ctx, id, c.Params["key"])
	return answer(v, err)
}

// setFlowUserState answers POST /w/{ws}/jobs/flow/user_states/{id}/{key}
// (WA-14): the body, any JSON, checked as the framework checks a JSON
// body (jsonBody) after the id, is stored at key for the flow job, and
// the answer is the text `Flow job state updated`, typed as a handler's
// text answer; a job that is not a live flow job answers 404 `Not found:
// Flow job not found`.
func (f *Facade) setFlowUserState(ctx context.Context, c *Call) Reply {
	id, refused := pathUUID(c, "id", "job_id")
	if refused == nil {
		refused = jsonBody(c)
	}
	if refused != nil {
		return *refused
	}
	if err := f.platform.SetFlowUserState(ctx, id, c.Params["key"], c.Body); err != nil {
		return answer(nil, err)
	}
	return plainText(200, "Flow job state updated")
}

// resultMaybe answers getCompletedJobResultMaybe, GET
// /w/{ws}/jobs_u/completed/get_result_maybe/{id}, which waitJob polls
// (WA-15, WA-V2), for the runs the dispatcher started: completed, with its
// success and result; or not, with started null unless get_started=true
// asks for it. A UUID that names no job, and any job looked up in a
// workspace other than the caller's, answer as Windmill does for an id
// with no job row (WA-V2/a, /b, /d); a job of windmill-lite's that the
// dispatcher did not start answers 501, since job introspection beyond
// dispatched runs is `later` (ADR 0007, Jobs).
func (f *Facade) resultMaybe(ctx context.Context, c *Call) Reply {
	id, refused := pathUUID(c, "id", "id")
	if refused != nil {
		return *refused
	}
	var out struct {
		Started   *bool           `json:"started"`
		Success   *bool           `json:"success"`
		Completed bool            `json:"completed"`
		Result    json.RawMessage `json:"result"`
	}
	out.Result = json.RawMessage("null")
	asked := c.Query.Get("get_started") == "true"
	r, ok, err := f.dispatched(ctx, c, id)
	switch {
	case err != nil:
		return answer(nil, err)
	case ok && r.Completed:
		started := true
		out.Started, out.Success, out.Completed, out.Result = &started, &r.Success, true, r.Result
	case ok:
		if asked {
			out.Started = &r.Started
		}
	default:
		if f.known(c, id) {
			return text(501, UnsupportedPrefix+c.Route.Name())
		}
		if asked {
			out.Started = new(bool)
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return answer(nil, err)
	}
	return jsonReply(200, body)
}

// result answers getCompletedJobResult, GET
// /w/{ws}/jobs_u/completed/get_result/{id} (WA-15, WA-19): the result of
// a run the dispatcher started, once it has completed, failed or not. One
// that has not completed, a UUID that names no job, and any job looked up
// in a workspace other than the caller's answer 404 `Not found: Completed
// Job not found at name <id>` (WA-15/e, WA-19/d), without Windmill's
// parenthesised Rust location (D14). A job of windmill-lite's that the
// dispatcher did not start answers 501, as resultMaybe's does.
func (f *Facade) result(ctx context.Context, c *Call) Reply {
	id, refused := pathUUID(c, "id", "id")
	if refused != nil {
		return *refused
	}
	r, ok, err := f.dispatched(ctx, c, id)
	switch {
	case err != nil:
		return answer(nil, err)
	case ok && r.Completed:
		return jsonReply(200, r.Result)
	case !ok && f.known(c, id):
		return text(501, UnsupportedPrefix+c.Route.Name())
	}
	return Error{Variant: "NotFound", Message: "Completed Job not found at name " + id}.Reply()
}

// dispatched returns the state of a run the dispatcher started, looked up
// in the workspace of c's path: a run in no other workspace than the
// caller's.
func (f *Facade) dispatched(ctx context.Context, c *Call, id string) (platform.JobResult, bool, error) {
	d := f.platform.Dispatcher
	if d == nil || !ownWorkspace(c) {
		return platform.JobResult{}, false, nil
	}
	return d.Result(ctx, id)
}

// known reports whether the job registry knows the job id names, looked
// up in the workspace of c's path.
func (f *Facade) known(c *Call, id string) bool {
	_, _, ok := f.platform.JobRegistry().Job(id)
	return ok && ownWorkspace(c)
}

func jsonString(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

func jsonReply(status int, body []byte) Reply {
	return Reply{Status: status, Headers: map[string]string{"content-type": "application/json"}, Body: body}
}

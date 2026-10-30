package facade

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The job routes of ADR 0007's services (milestones PF1 and PF3): the root
// lookup, from the job registry (D3); flow user state (WA-14); and the
// result polls of the runs a step dispatched (WA-15, WA-V2).
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
// flow scope one of its jobs runs in, or the run itself. An id with no
// job in the path's workspace (one the registry does not know of, a job
// looked up in a workspace other than the caller's, or a job whose run
// has ended and was forgotten) answers as Windmill answers an id with no
// job row: 400 `SqlErr: no rows returned by a query that expected to
// return at least one row`, without Windmill's Rust location (D14). The
// route checks no token (WA-4), and an id that is not a UUID answers the
// same, where Windmill's web framework refuses it with a 400 of its own
// (WA-19/c).
func (f *Facade) rootJobID(_ context.Context, c *Call) Reply {
	if ownWorkspace(c) {
		if root, ok := f.platform.RootJob(c.Params["id"]); ok {
			return jsonReply(200, jsonString(root))
		}
	}
	return Error{Variant: "SqlErr", Message: noRows}.Reply()
}

// getFlowUserState answers GET /w/{ws}/jobs/flow/user_states/{id}/{key}
// (WA-14): the value stored at key for the flow job, JSON null when there
// is none, when the job is not a live flow job, and when it has ended.
func (f *Facade) getFlowUserState(ctx context.Context, c *Call) Reply {
	v, err := f.platform.FlowUserState(ctx, c.Params["id"], c.Params["key"])
	return answer(v, err)
}

// setFlowUserState answers POST /w/{ws}/jobs/flow/user_states/{id}/{key}
// (WA-14): the body, any JSON, is stored at key for the flow job, and the
// answer is the text `Flow job state updated`; a job that is not a live
// flow job answers 404 `Not found: Flow job not found`.
func (f *Facade) setFlowUserState(ctx context.Context, c *Call) Reply {
	if err := f.platform.SetFlowUserState(ctx, c.Params["id"], c.Params["key"], c.Body); err != nil {
		return answer(nil, err)
	}
	return text(200, "Flow job state updated")
}

// uuidPattern matches a UUID in any case, the form a job id parameter
// takes (the schema's JobId is a uuid).
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// badJobID answers a result poll whose id is not a UUID with a 400, as a
// request whose uuid parameter does not parse is refused, rather than as
// an unknown job, so that a waitJob given the body of a refused dispatch
// as its id fails at once instead of polling forever (WA-V5/a). The text
// is windmill-lite's; the reference does not give Windmill's.
func badJobID(id string) Reply {
	return Error{Variant: "BadRequest", Message: "windmill-lite: the job id " + string(jsonString(id)) + " is not a UUID"}.Reply()
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
	id := c.Params["id"]
	if !uuidPattern.MatchString(id) {
		return badJobID(id)
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
	id := c.Params["id"]
	if !uuidPattern.MatchString(id) {
		return badJobID(id)
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

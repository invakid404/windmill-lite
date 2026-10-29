package facade

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The job routes of ADR 0007's services (milestones PF1 and PF3): the root
// lookup and the job environment a read's job_id names, from the job
// registry (D3); flow user state (WA-14); and the result polls of the
// runs a step dispatched (WA-15, WA-V2).
func (f *Facade) serveJobs() {
	f.Handle("getRootJobId", f.rootJobID)
	f.Handle("getFlowUserState", f.getFlowUserState)
	f.Handle("setFlowUserState", f.setFlowUserState)
	f.Handle("getCompletedJobResultMaybe", f.resultMaybe)
	f.Handle("getCompletedJobResult", f.result)
}

// rootJobID answers getRootJobId, GET /w/{ws}/jobs_u/get_root_job_id/{id}
// (WA-8, WA-14): the effective root of a job (WA-V1), as a JSON string,
// from the job registry: a job of a run that is still running, a flow
// scope one of its jobs runs in, or the run itself. A job the registry
// does not know of (another workspace's, or one whose run has ended)
// answers 501: what Windmill answers for an id with no job is not in the
// reference, and a guess would be worse than a loud failure.
func (f *Facade) rootJobID(_ context.Context, c *Call) Reply {
	if root, ok := f.platform.RootJob(c.Params["id"]); ok {
		return jsonReply(200, jsonString(root))
	}
	return text(501, UnsupportedPrefix+c.Route.Name())
}

// jobEnv is the job environment of the script job a read's job_id names,
// from the job registry.
func (f *Facade) jobEnv(id string) (map[string]string, bool) {
	return f.platform.JobEnv(id)
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
// asks for it. A UUID that names no job answers as Windmill does for an id
// with no job row (WA-V2/a, /b); a job of windmill-lite's that the
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
	r, ok, err := f.dispatched(ctx, id)
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
		if _, _, known := f.platform.JobRegistry().Job(id); known {
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
// /w/{ws}/jobs_u/completed/get_result/{id} (WA-15): the result of a run
// the dispatcher started, once it has completed, failed or not. One that
// has not completed, and a UUID that names no job, answer 404 (WA-15/e);
// the text is windmill-lite's. A job of windmill-lite's that the
// dispatcher did not start answers 501, as resultMaybe's does.
func (f *Facade) result(ctx context.Context, c *Call) Reply {
	id := c.Params["id"]
	if !uuidPattern.MatchString(id) {
		return badJobID(id)
	}
	r, ok, err := f.dispatched(ctx, id)
	switch {
	case err != nil:
		return answer(nil, err)
	case ok && r.Completed:
		return jsonReply(200, r.Result)
	case !ok:
		if _, _, known := f.platform.JobRegistry().Job(id); known {
			return text(501, UnsupportedPrefix+c.Route.Name())
		}
	}
	return Error{Variant: "NotFound", Message: "windmill-lite: no completed job " + id}.Reply()
}

// dispatched returns the state of a run the dispatcher started.
func (f *Facade) dispatched(ctx context.Context, id string) (platform.JobResult, bool, error) {
	d := f.platform.Dispatcher
	if d == nil {
		return platform.JobResult{}, false, nil
	}
	return d.Result(ctx, id)
}

func jsonString(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

func jsonReply(status int, body []byte) Reply {
	return Reply{Status: status, Headers: map[string]string{"content-type": "application/json"}, Body: body}
}

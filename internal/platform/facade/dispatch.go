package facade

import (
	"context"
	"errors"
	"strconv"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The dispatch routes of ADR 0007's Dispatcher: what
// runScript*Async and runFlowAsync post, and their synchronous forms
// before waitJob (WA-15). The result polls are in jobs.go.
func (f *Facade) serveDispatch() {
	f.Handle("runScriptByPath", f.dispatch(platform.DispatchScript, "path"))
	f.Handle("runFlowByPath", f.dispatch(platform.DispatchFlow, "path"))
	f.Handle("runScriptByHash", f.dispatch(platform.DispatchHash, "hash"))
}

// unservedRunQuery are the query parameters of the run routes that would
// change the run, which no stock helper sends and the default dispatcher
// does not do: a dispatch with one answers 501, named by it.
var unservedRunQuery = []string{"scheduled_for", "cache_ttl", "job_id"}

// dispatch answers POST /w/{ws}/jobs/run/p/{path}, run/f/{path} and
// run/h/{hash} ([WA-15]): it hands the target, the body as the run's
// arguments, and the query's parent_job, root_job, tag and
// scheduled_in_secs to the Dispatcher, and answers 201 with the new job's
// id as text, typed text/plain; charset=utf-8 as a handler's text answer
// is ([WA-42]). A target that is not a root of the bundle, and every
// target when there is no dispatcher, answers 501 with the body
// `dispatch target not in bundle: <target>`, which the 501 policy turns
// into the dispatching step's error (ADR 0001 §5a); the stock client
// takes that body for a job id ([WA-V5]/a), whose result poll then fails at
// once, refused as an id that is not a UUID (pathUUID). A hash names no root, since a windmill-lite bundle
// records no script hash.
//
// [WA-15]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-15
// [WA-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-42
// [WA-V5]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-v5
func (f *Facade) dispatch(kind platform.DispatchKind, param string) Handler {
	return func(ctx context.Context, c *Call) Reply {
		for _, q := range unservedRunQuery {
			if c.Query.Has(q) {
				return text(501, UnsupportedPrefix+c.Route.Name()+" with "+q)
			}
		}
		d := platform.Dispatch{
			Kind:      kind,
			Target:    c.Params[param],
			Args:      c.Body,
			ParentJob: c.Query.Get("parent_job"),
			RootJob:   c.Query.Get("root_job"),
			Tag:       c.Query.Get("tag"),
			Caller:    c.Caller.Env,
		}
		if s := c.Query.Get("scheduled_in_secs"); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 0 {
				return Error{Variant: "BadRequest", Message: "windmill-lite: scheduled_in_secs " + strconv.Quote(s) + " is not a number of seconds"}.Reply()
			}
			d.ScheduledInSecs = n
		}
		id, err := f.startRun(ctx, d)
		var nb *platform.NotInBundleError
		switch {
		case errors.As(err, &nb):
			return text(501, nb.Error())
		case err != nil:
			return answer(nil, err)
		}
		return plainText(201, id)
	}
}

// startRun hands a dispatch to the dispatcher; with none, no target is in
// the bundle.
func (f *Facade) startRun(ctx context.Context, d platform.Dispatch) (string, error) {
	if f.platform.Dispatcher == nil || d.Kind == platform.DispatchHash {
		return "", &platform.NotInBundleError{Target: d.Target}
	}
	return f.platform.Dispatcher.Dispatch(ctx, d)
}

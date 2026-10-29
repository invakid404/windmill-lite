package facade

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The resource and variable reads of ADR 0007's services (milestone PF2),
// which the facade answers from Config.Platform: the three resource-read
// contracts of WA-40 kept apart, and Windmill's statuses and bodies
// (WA-10 to WA-12, WA-42). Writes (create, update) are `later` and answer
// 501.
func (f *Facade) serveProviders() {
	f.Handle("getResourceValueInterpolated", f.resourceInterpolated)
	f.Handle("getResourceValue", f.resourceRaw)
	f.Handle("existsResource", f.resourceExists)
	f.Handle("getVariableValue", f.variableValue)
	f.Handle("existsVariable", f.variableExists)
}

// resourceInterpolated answers GET /w/{ws}/resources/get_value_interpolated/{path}
// (WA-12): contract (b). Without job_id, as the stock clients call it,
// $WM_* strings stay literal; with one, they are the stored fields of
// that job (platform.APIEnv), which must be a live invocation's: a job it
// does not know of answers 501, as the root lookup does, until the job
// registry of milestone PF3. allow_cache is ignored: windmill-lite caches
// nothing (WA-12, caching).
func (f *Facade) resourceInterpolated(ctx context.Context, c *Call) Reply {
	var job map[string]string
	if id := c.Query.Get("job_id"); id != "" {
		env, ok := f.jobEnv(id)
		if !ok {
			return text(501, UnsupportedPrefix+c.Route.Name())
		}
		job = platform.APIEnv(env)
	}
	body, err := f.platform.InterpolatedResource(ctx, c.Params["path"], job)
	return answer(body, err)
}

// resourceRaw answers GET /w/{ws}/resources/get_value/{path}: contract
// (a), nothing resolved.
func (f *Facade) resourceRaw(ctx context.Context, c *Call) Reply {
	body, err := f.platform.RawResource(ctx, c.Params["path"])
	return answer(body, err)
}

func (f *Facade) resourceExists(ctx context.Context, c *Call) Reply {
	ok, err := f.platform.ResourceExists(ctx, c.Params["path"])
	return answer(boolJSON(ok), err)
}

// variableValue answers GET /w/{ws}/variables/get_value/{path} (WA-11):
// the value as a JSON string, a secret's plaintext through Secrets and
// the value resolvers.
func (f *Facade) variableValue(ctx context.Context, c *Call) Reply {
	v, err := f.platform.VariableValue(ctx, c.Params["path"])
	if err != nil {
		return answer(nil, err)
	}
	return answer(platform.JSONString(v), nil)
}

func (f *Facade) variableExists(ctx context.Context, c *Call) Reply {
	ok, err := f.platform.VariableExists(ctx, c.Params["path"])
	return answer(boolJSON(ok), err)
}

func boolJSON(b bool) []byte {
	out, _ := json.Marshal(b)
	return out
}

// answer is a read's reply: the JSON body, or the error as the API
// renders it (WA-42).
func answer(body []byte, err error) Reply {
	var pe *platform.Error
	switch {
	case errors.As(err, &pe):
		return Error(*pe).Reply()
	case err != nil:
		return Error{Variant: "InternalErr", Message: err.Error()}.Reply()
	}
	return Reply{Status: 200, Headers: map[string]string{"content-type": "application/json"}, Body: body}
}

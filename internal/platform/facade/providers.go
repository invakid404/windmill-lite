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
// (WA-10 to WA-12, WA-42); and the update of a variable's value (WA-11,
// milestone F1), when the variable service takes writes. The other writes
// (creating a variable, the resource writes) are `later` and answer 501.
func (f *Facade) serveProviders() {
	f.Handle("getResourceValueInterpolated", f.resourceInterpolated)
	f.Handle("getResourceValue", f.resourceRaw)
	f.Handle("existsResource", f.resourceExists)
	f.Handle("getVariableValue", f.variableValue)
	f.Handle("existsVariable", f.variableExists)
	f.Handle("updateVariable", f.variableUpdate)
}

// resourceInterpolated answers GET /w/{ws}/resources/get_value_interpolated/{path}
// (WA-12): contract (b). Without job_id, as the stock clients call it,
// $WM_* strings stay literal. With one, the job is looked up in the job
// registry only when the walk meets a $WM_* string, as Windmill looks it
// up, and those strings are then its stored fields (platform.ReadJobEnv):
// a value without one reads as if no job_id had been sent. The job must
// be live and of the path's workspace, else the read answers 404 `Not
// found: Job not found at name <id>` (WA-12/g); a live flow job, whose
// job variables the registry does not keep, answers 501. allow_cache is
// ignored: windmill-lite caches nothing (WA-12, caching).
func (f *Facade) resourceInterpolated(ctx context.Context, c *Call) Reply {
	var job platform.JobLookup
	if id := c.Query.Get("job_id"); id != "" {
		job = func() (map[string]string, error) { return f.platform.ReadJobEnv(id, c.Params["workspace"]) }
	}
	body, err := f.providers(c).InterpolatedResourceOf(ctx, c.Params["path"], job)
	if errors.Is(err, platform.ErrNoJobVariables) {
		return text(501, UnsupportedPrefix+c.Route.Name()+" with the job_id of a flow job")
	}
	return answer(body, err)
}

// resourceRaw answers GET /w/{ws}/resources/get_value/{path}: contract
// (a), nothing resolved.
func (f *Facade) resourceRaw(ctx context.Context, c *Call) Reply {
	body, err := f.providers(c).RawResource(ctx, c.Params["path"])
	return answer(body, err)
}

func (f *Facade) resourceExists(ctx context.Context, c *Call) Reply {
	ok, err := f.providers(c).ResourceExists(ctx, c.Params["path"])
	return answer(boolJSON(ok), err)
}

// variableValue answers GET /w/{ws}/variables/get_value/{path} (WA-11):
// the value as a JSON string, a secret's plaintext through Secrets and
// the value resolvers.
func (f *Facade) variableValue(ctx context.Context, c *Call) Reply {
	v, err := f.providers(c).VariableValue(ctx, c.Params["path"])
	if err != nil {
		return answer(nil, err)
	}
	return answer(platform.JSONString(v), nil)
}

// variableUpdate answers POST /w/{ws}/variables/update/{path} (WA-11):
// its body is the API's EditVariable, of which the value and is_secret
// are applied; description is not kept, since the services hold none. A
// missing variable is the read's 404. A rename (a path other than the
// route's) and a value already encrypted (already_encrypted=true) are not
// served, and nor is any update when the variable service is read-only:
// each answers 501. The answer is 200 with a text/plain body, as the API's
// schema gives it; the reference does not give its text, which here names
// the variable.
func (f *Facade) variableUpdate(ctx context.Context, c *Call) Reply {
	path := c.Params["path"]
	var edit struct {
		Path     *string `json:"path"`
		Value    *string `json:"value"`
		IsSecret *bool   `json:"is_secret"`
	}
	if err := json.Unmarshal(c.Body, &edit); err != nil {
		return Error{Variant: "BadRequest", Message: "the body is not an EditVariable: " + err.Error()}.Reply()
	}
	if c.Query.Get("already_encrypted") == "true" || edit.Path != nil && *edit.Path != path {
		return text(501, UnsupportedPrefix+c.Route.Name())
	}
	switch err := f.providers(c).UpdateVariable(ctx, path, edit.Value, edit.IsSecret); {
	case errors.Is(err, platform.ErrReadOnlyVariables):
		return text(501, UnsupportedPrefix+c.Route.Name())
	case err != nil:
		return answer(nil, err)
	}
	return text(200, "updated variable "+path)
}

func (f *Facade) variableExists(ctx context.Context, c *Call) Reply {
	ok, err := f.providers(c).VariableExists(ctx, c.Params["path"])
	return answer(boolJSON(ok), err)
}

func boolJSON(b bool) []byte {
	out, _ := json.Marshal(b)
	return out
}

// providers is the services the resource and variable routes answer c
// from: those its caller's run chose (H13), or the facade's.
func (f *Facade) providers(c *Call) *platform.Services {
	if c.Caller != nil && c.Caller.Platform != nil {
		return c.Caller.Platform
	}
	return f.platform
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

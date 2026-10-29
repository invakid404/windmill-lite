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
	switch err := f.platform.UpdateVariable(ctx, path, edit.Value, edit.IsSecret); {
	case errors.Is(err, platform.ErrReadOnlyVariables):
		return text(501, UnsupportedPrefix+c.Route.Name())
	case err != nil:
		return answer(nil, err)
	}
	return text(200, "updated variable "+path)
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

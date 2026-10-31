package facade

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The resource and variable reads answer from Config.Platform with
// Windmill's statuses, content types and bodies, the same on both
// transports ([WA-10] to [WA-12], [WA-40], [WA-42]/b and /c, [WA-V3]/a).
//
// [WA-10]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-10
// [WA-12]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-12
// [WA-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-40
// [WA-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-42
// [WA-V3]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-v3
func TestProviderRoutes(t *testing.T) {
	store := platform.NewStore()
	store.SetVariable("u/x/pw", platform.Variable{Value: "s3cret"})
	store.SetVariable("u/x/q", platform.Variable{Value: "a\"b\n"})
	for path, v := range map[string]string{
		"u/x/db":         `{"password":"$var:u/x/pw","tok":"$WM_TOKEN"}`,
		"u/t/outer":      `{"x":"$res:u/t/missing"}`,
		"u/test/context": `{"root":"$WM_ROOT_JOB_ID","job":"$WM_JOB_ID"}`,
	} {
		if err := store.SetResource(path, platform.Resource{Type: "object", Value: json.RawMessage(v)}); err != nil {
			t.Fatal(err)
		}
	}
	f := newFacade(t, Config{Platform: &platform.Services{Resources: store, Variables: store}})
	// A direct step S of top-level flow F: its stored root is "".
	const flow, step = "00000000-0000-4000-8000-00000000000f", "00000000-0000-4000-8000-000000000005"
	f.Register("inv-a", flow, map[string]string{"WM_JOB_ID": step, "WM_FLOW_JOB_ID": flow, "WM_ROOT_FLOW_JOB_ID": flow, "WM_ROOT_JOB_ID": flow, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws"})
	const base = "/api/w/ws/"
	for _, c := range []struct {
		spec, path string
		status     int
		ctype      string
		body       string
	}{
		{"WA-12/a", "resources/get_value_interpolated/u/x/db", 200, "application/json", `{"tok":"$WM_TOKEN","password":"s3cret"}`},
		{"WA-40", "resources/get_value/u/x/db", 200, "application/json", `{"tok":"$WM_TOKEN","password":"$var:u/x/pw"}`},
		{"WA-40", "resources/exists/u/x/db", 200, "application/json", `true`},
		{"WA-40", "resources/exists/u/x/none", 200, "application/json", `false`},
		{"WA-11/a", "variables/get_value/u/x/pw", 200, "application/json", `"s3cret"`},
		{"WA-11", "variables/get_value/u/x/q", 200, "application/json", `"a\"b\n"`},
		{"WA-11/a", "variables/get_value/$var:u/x/pw", 404, "text/plain", `Not found: Variable $var:u/x/pw not found`},
		{"WA-40", "variables/exists/u/x/pw", 200, "application/json", `true`},
		{"WA-42/b", "resources/get_value_interpolated/u/x/none", 404, "text/plain", `Not found: Resource u/x/none not found`},
		{"WA-42/c", "resources/get_value_interpolated/u/t/outer", 404, "text/plain", `Not found: Resource not found at name u/t/missing`},
		{"WA-V3/a", "resources/get_value_interpolated/u/test/context", 200, "application/json", `{"job":"$WM_JOB_ID","root":"$WM_ROOT_JOB_ID"}`},
		// With the job's id, $WM_* strings are its stored fields: the
		// stored root of a direct step is "".
		{"WA-V3", "resources/get_value_interpolated/u/test/context?job_id=" + step, 200, "application/json", `{"job":"` + step + `","root":""}`},
		// A job the job registry does not know of, looked up for a $WM_*
		// string (WA-12/g).
		{"WA-12/g", "resources/get_value_interpolated/u/test/context?job_id=00000000-0000-4000-8000-0000000000ff", 404, "text/plain", "Not found: Job not found at name 00000000-0000-4000-8000-0000000000ff"},
		// A job_id that is not a UUID is refused before the read.
		{"WA-12/g", "resources/get_value_interpolated/u/test/context?job_id=nope", 400, "text/plain; charset=utf-8", "windmill-lite: the query parameter job_id, \"nope\", is not a UUID: invalid character: found `n` at 1"},
	} {
		t.Run(c.spec+" "+c.path, func(t *testing.T) {
			rep := intercept(f, "inv-a", "GET", "http://x"+base+c.path, nil, nil)
			if rep.Status != c.status || rep.Headers["content-type"] != c.ctype || string(rep.Body) != c.body {
				t.Errorf("intercepted: %d %q %s, want %d %q %s", rep.Status, rep.Headers["content-type"], rep.Body, c.status, c.ctype, c.body)
			}
			if code, body := get(t, f.FreshOrigin()+base+c.path, "tok-a"); code != c.status || body != c.body {
				t.Errorf("TCP: %d %s, want %d %s", code, body, c.status, c.body)
			}
		})
	}
	// A write is `later`: 501.
	rep := intercept(f, "inv-a", "POST", "http://x"+base+"variables/create", nil, []byte(`{}`))
	if rep.Status != 501 || string(rep.Body) != UnsupportedPrefix+"POST /w/{ws}/variables/create" {
		t.Errorf("create: %d %s", rep.Status, rep.Body)
	}
}

// WA-12/g: a read's job_id is looked up only for a $WM_* string, so a
// value without one reads as if none had been sent, whatever UUID it
// names. For one, the job must be live and of the path's workspace: a
// random UUID, a job that has ended and a job of another workspace answer
// 404, and the caller's own job resolves, also when the id is written in
// another of the UUID parser's forms. A job_id that is not a UUID, the
// empty one included, is refused with a 400 for either value, as
// Windmill's web framework refuses the query string. A live flow job,
// whose job variables windmill-lite does not keep, answers 501, which the
// policy counts.
func TestReadWithAJobID(t *testing.T) {
	store := platform.NewStore()
	for path, v := range map[string]string{"u/x/plain": `{"a":1}`, "u/x/ctx": `{"j":"$WM_JOB_ID"}`} {
		if err := store.SetResource(path, platform.Resource{Type: "object", Value: json.RawMessage(v)}); err != nil {
			t.Fatal(err)
		}
	}
	services := &platform.Services{Resources: store}
	f := newFacade(t, Config{Platform: services})
	const run, step, done, foreign = "00000000-0000-4000-8000-0000000000f0", "00000000-0000-4000-8000-0000000000f1", "00000000-0000-4000-8000-0000000000f2", "00000000-0000-4000-8000-0000000000f3"
	const random = "00000000-0000-4000-8000-0000000000ff"
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run})
	defer endRun()
	s := f.Register("inv-a", run, env(step, run, "tok-a"))
	defer s.End()
	f.Register("inv-done", run, env(done, run, "tok-done")).End()
	o := map[string]string{"WM_JOB_ID": foreign, "WM_FLOW_JOB_ID": run, "WM_ROOT_FLOW_JOB_ID": run, "WM_ROOT_JOB_ID": run, "WM_TOKEN": "tok-o", "WM_WORKSPACE": "other"}
	defer f.Register("inv-o", run, o).End()
	read := func(path, id string) harness.PlatformReply {
		return intercept(f, "inv-a", "GET", "http://x/api/w/ws/resources/get_value_interpolated/"+path+"?job_id="+id, nil, nil)
	}
	for _, id := range []string{random, done, foreign, step, run} {
		if rep := read("u/x/plain", id); rep.Status != 200 || string(rep.Body) != `{"a":1}` {
			t.Errorf("u/x/plain with %s: %d %s", id, rep.Status, rep.Body)
		}
	}
	for _, path := range []string{"u/x/plain", "u/x/ctx"} {
		for _, id := range []string{"nope", ""} {
			if rep := read(path, id); rep.Status != 400 || rep.Headers["content-type"] != "text/plain; charset=utf-8" {
				t.Errorf("%s with job_id %q: %d %v %s", path, id, rep.Status, rep.Headers, rep.Body)
			}
		}
	}
	if rep := read("u/x/ctx", strings.ToUpper(step)); rep.Status != 200 || string(rep.Body) != `{"j":"`+step+`"}` {
		t.Errorf("u/x/ctx with its own job in upper case: %d %s", rep.Status, rep.Body)
	}
	for _, id := range []string{random, done, foreign} {
		if rep := read("u/x/ctx", id); rep.Status != 404 || rep.Headers["content-type"] != "text/plain" || string(rep.Body) != "Not found: Job not found at name "+id {
			t.Errorf("u/x/ctx with %s: %d %s", id, rep.Status, rep.Body)
		}
	}
	if rep := read("u/x/ctx", step); rep.Status != 200 || string(rep.Body) != `{"j":"`+step+`"}` {
		t.Errorf("u/x/ctx with its own job: %d %s", rep.Status, rep.Body)
	}
	if got := s.Unsupported(context.Background()); len(got) != 0 {
		t.Fatalf("unsupported before the flow job's read: %q", got)
	}
	const flowJob = UnsupportedPrefix + "GET /w/{ws}/resources/get_value_interpolated/{path} with the job_id of a flow job"
	if rep := read("u/x/ctx", run); rep.Status != 501 || string(rep.Body) != flowJob {
		t.Errorf("u/x/ctx with a live flow job: %d %s", rep.Status, rep.Body)
	}
	if _, message := s.unsupported(context.Background()); message != flowJob {
		t.Errorf("the policy's message %q", message)
	}
}

// A facade with no platform answers every read as not found.
func TestProviderRoutesWithoutPlatform(t *testing.T) {
	f := newFacade(t, Config{})
	f.Register("inv-a", "F", env("S", "F", "tok-a"))
	rep := intercept(f, "inv-a", "GET", "http://x/api/w/ws/variables/get_value/u/x/v", nil, nil)
	if rep.Status != 404 || string(rep.Body) != "Not found: Variable u/x/v not found" {
		t.Fatalf("%d %s", rep.Status, rep.Body)
	}
}

// see, and its secrecy with a value, and answers Windmill's text, typed
// as a handler's text answer ([WA-11]/e). Windmill's refusals: a missing
// variable's 404, a secrecy change without a value (the variable left as
// it was), and the web framework's for a body without a JSON content type
// (415), that is not JSON (400) or not an object (422). A rename, a value
// already encrypted, and any update of a read-only variable service
// answer 501.
//
// [WA-11]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-11
func TestVariableUpdate(t *testing.T) {
	store := platform.NewStore()
	store.SetVariable("u/x/v", platform.Variable{Value: "first"})
	store.SetVariable(`u/x/a"b\c`, platform.Variable{Value: "q"})
	f := newFacade(t, Config{Platform: &platform.Services{Resources: store, Variables: store}})
	f.Register("inv-a", "F", env("S", "F", "tok-a"))
	const base = "http://x/api/w/ws/variables/"
	jsonType := map[string]string{"content-type": "application/json"}
	post := func(path, body string) harness.PlatformReply {
		return intercept(f, "inv-a", "POST", base+path, jsonType, []byte(body))
	}
	const updated = `variable u/x/v updated (npath: "u/x/v")`
	if rep := post("update/u/x/v", `{"value":"second"}`); rep.Status != 200 || rep.Headers["content-type"] != "text/plain; charset=utf-8" || string(rep.Body) != updated {
		t.Fatalf("update: %d %v %s", rep.Status, rep.Headers, rep.Body)
	}
	if code, body := tcpPost(t, f.FreshOrigin()+"/api/w/ws/variables/update/u/x/v", "tok-a", "application/json", `{"value":"second"}`); code != 200 || body != updated {
		t.Fatalf("update over TCP: %d %s", code, body)
	}
	if rep := intercept(f, "inv-a", "GET", base+"get_value/u/x/v", nil, nil); rep.Status != 200 || string(rep.Body) != `"second"` {
		t.Fatalf("read after the update: %d %s", rep.Status, rep.Body)
	}
	if rep := post(`update/u/x/a%22b%5Cc`, `{"value":"r"}`); rep.Status != 200 || string(rep.Body) != `variable u/x/a"b\c updated (npath: "u/x/a\"b\\c")` {
		t.Errorf("a path with a quote and a backslash: %d %s", rep.Status, rep.Body)
	}
	const secrecy = "Bad request: cannot change is_secret without updating value too"
	if rep := post("update/u/x/v", `{"is_secret":true}`); rep.Status != 400 || rep.Headers["content-type"] != "text/plain" || string(rep.Body) != secrecy {
		t.Fatalf("secrecy without a value: %d %v %s", rep.Status, rep.Headers, rep.Body)
	}
	if v, _, _ := store.Variable(t.Context(), "u/x/v"); v.IsSecret || v.Value != "second" {
		t.Fatalf("the variable after a refused secrecy change: %+v", v)
	}
	if rep := post("update/u/x/v", `{"value":"third","is_secret":true}`); rep.Status != 200 || string(rep.Body) != updated {
		t.Fatalf("secrecy with a value: %d %s", rep.Status, rep.Body)
	}
	if v, _, _ := store.Variable(t.Context(), "u/x/v"); !v.IsSecret || v.Value != "third" {
		t.Fatalf("the variable after a secrecy update: %+v", v)
	}
	if rep := post("update/u/x/v", `{"is_secret":true}`); rep.Status != 200 {
		t.Fatalf("the secrecy it has, without a value: %d %s", rep.Status, rep.Body)
	}
	for _, c := range []struct {
		name, path, body string
		headers          map[string]string
		status           int
		typ, text        string
	}{
		{"missing", "update/u/x/none", `{"value":"x"}`, jsonType, 404, "text/plain", "Not found: Variable not found at name u/x/none"},
		{"missing, no field", "update/u/x/none", `{}`, jsonType, 404, "text/plain", "Not found: Variable not found at name u/x/none"},
		{"missing, secrecy only", "update/u/x/none", `{"is_secret":true}`, jsonType, 404, "text/plain", "Not found: Variable not found at name u/x/none"},
		{"no content type", "update/u/x/v", `{"value":"x"}`, nil, 415, "text/plain; charset=utf-8", "Expected request with `Content-Type: application/json`"},
		{"not JSON", "update/u/x/v", `hello`, jsonType, 400, "text/plain; charset=utf-8", "Failed to parse the request body as JSON: expected value at line 1 column 1"},
		{"not an object", "update/u/x/v", `[1]`, jsonType, 422, "text/plain; charset=utf-8", "Failed to deserialize the JSON body into the target type: windmill-lite: the body is not an EditVariable object"},
		{"null", "update/u/x/v", `null`, jsonType, 422, "text/plain; charset=utf-8", "Failed to deserialize the JSON body into the target type: windmill-lite: the body is not an EditVariable object"},
		{"rename", "update/u/x/v", `{"path":"u/x/w"}`, jsonType, 501, "text/plain", UnsupportedPrefix + "POST /w/{ws}/variables/update/{path}"},
		{"already encrypted", "update/u/x/v?already_encrypted=true", `{"value":"x"}`, jsonType, 501, "text/plain", UnsupportedPrefix + "POST /w/{ws}/variables/update/{path}"},
	} {
		rep := intercept(f, "inv-a", "POST", base+c.path, c.headers, []byte(c.body))
		if rep.Status != c.status || rep.Headers["content-type"] != c.typ || string(rep.Body) != c.text {
			t.Errorf("%s: %d %v %s", c.name, rep.Status, rep.Headers, rep.Body)
		}
	}
	if v, _, _ := store.Variable(t.Context(), "u/x/v"); v.Value != "third" {
		t.Fatalf("the variable after the refused updates: %+v", v)
	}
	// A variable service that takes no writes.
	ro := newFacade(t, Config{Platform: &platform.Services{Variables: readOnly{store}}})
	ro.Register("inv-b", "F", env("S", "F", "tok-b"))
	if rep := intercept(ro, "inv-b", "POST", base+"update/u/x/v", jsonType, []byte(`{"value":"x"}`)); rep.Status != 501 {
		t.Fatalf("read-only: %d %s", rep.Status, rep.Body)
	}
}

// tcpPost posts body with the content type to url over TCP, with the
// token, and returns the answer's status and body.
func tcpPost(t *testing.T, url, token, ctype, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", ctype)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// H13, milestone F2a: an invocation of a run that chose its own providers
// (engine.Invocation.Platform) reads and updates variables and reads
// resources from them; another invocation reads the facade's, and the root
// lookup answers from the job registry the services share.
func TestCallerPlatform(t *testing.T) {
	base := platform.NewStore()
	base.SetVariable("u/x/v", platform.Variable{Value: "base"})
	services := &platform.Services{Resources: base, Variables: base}
	f := newFacade(t, Config{Platform: services})
	run := platform.NewStore()
	run.SetVariable("u/x/v", platform.Variable{Value: "run"})
	if err := run.SetResource("u/x/r", platform.Resource{Type: "t", Value: []byte(`{"tenant":"a"}`)}); err != nil {
		t.Fatal(err)
	}
	own := services.WithProviders(platform.Providers{Resources: platform.ResourceChain{run, base}, Variables: platform.VariableChain{run, base}})
	x := &fakeExec{out: engine.StepOutcome{Kind: engine.OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}}
	var got []string
	x.do = func(inv engine.Invocation) {
		const api = "http://x/api/w/ws/"
		for _, p := range []string{"variables/get_value/u/x/v", "resources/get_value/u/x/r", "jobs_u/get_root_job_id/" + inv.Env["WM_JOB_ID"]} {
			rep := intercept(f, inv.ID, "GET", api+p, nil, nil)
			got = append(got, string(rep.Body))
		}
	}
	for _, c := range []struct {
		p    *platform.Services
		want []string
	}{
		{own, []string{`"run"`, `{"tenant":"a"}`, `"` + uid("root-a") + `"`}},
		{nil, []string{`"base"`, "Not found: Resource u/x/r not found", `"` + uid("root-a") + `"`}},
	} {
		got = nil
		inv := engine.Invocation{ID: "inv-a", Run: "run-a", Env: env("job-a", "root-a", "tok-a"), Report: &settleRecorder{}, Platform: c.p}
		if _, err := f.Executor(x).Execute(t.Context(), inv); err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[0] != c.want[0] || got[1] != c.want[1] || got[2] != c.want[2] {
			t.Errorf("own services %v: %q, want %q", c.p != nil, got, c.want)
		}
	}
}

// readOnly is a variable service that is not a platform.VariableUpdater.
type readOnly struct{ v platform.Variables }

func (r readOnly) Variable(ctx context.Context, path string) (platform.Variable, bool, error) {
	return r.v.Variable(ctx, path)
}

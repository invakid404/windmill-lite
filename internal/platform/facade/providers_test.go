package facade

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The resource and variable reads (milestone PF2) answer from
// Config.Platform with Windmill's statuses, content types and bodies, the
// same on both transports (WA-10 to WA-12, WA-40, WA-42/b and /c, WA-V3/a).
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
	// A direct step of top-level flow F: its stored root is "".
	f.Register("inv-a", "F", map[string]string{"WM_JOB_ID": "S", "WM_FLOW_JOB_ID": "F", "WM_ROOT_FLOW_JOB_ID": "F", "WM_ROOT_JOB_ID": "F", "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws"})
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
		{"WA-V3", "resources/get_value_interpolated/u/test/context?job_id=S", 200, "application/json", `{"job":"S","root":""}`},
		// A job the job registry does not know of, looked up for a $WM_*
		// string (WA-12/g).
		{"WA-12/g", "resources/get_value_interpolated/u/test/context?job_id=nope", 404, "text/plain", "Not found: Job not found at name nope"},
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
// value without one reads as if none had been sent, whatever it names.
// For one, the job must be live and of the path's workspace: a random
// UUID, a job that has ended and a job of another workspace answer 404,
// and the caller's own job resolves. A live flow job, whose job variables
// windmill-lite does not keep, answers 501, which the policy counts.
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
	for _, id := range []string{random, done, foreign, step, run, "nope"} {
		if rep := read("u/x/plain", id); rep.Status != 200 || string(rep.Body) != `{"a":1}` {
			t.Errorf("u/x/plain with %s: %d %s", id, rep.Status, rep.Body)
		}
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

// WA-11, milestone F1: an update sets an existing variable's value, which
// later reads see, and its secrecy; a missing variable is the read's 404;
// a rename, a value already encrypted, and any update of a read-only
// variable service answer 501.
func TestVariableUpdate(t *testing.T) {
	store := platform.NewStore()
	store.SetVariable("u/x/v", platform.Variable{Value: "first"})
	f := newFacade(t, Config{Platform: &platform.Services{Resources: store, Variables: store}})
	f.Register("inv-a", "F", env("S", "F", "tok-a"))
	const base = "http://x/api/w/ws/variables/"
	post := func(path, body string) harness.PlatformReply {
		return intercept(f, "inv-a", "POST", base+path, nil, []byte(body))
	}
	if rep := post("update/u/x/v", `{"value":"second"}`); rep.Status != 200 || rep.Headers["content-type"] != "text/plain" {
		t.Fatalf("update: %d %s", rep.Status, rep.Body)
	}
	if rep := intercept(f, "inv-a", "GET", base+"get_value/u/x/v", nil, nil); rep.Status != 200 || string(rep.Body) != `"second"` {
		t.Fatalf("read after the update: %d %s", rep.Status, rep.Body)
	}
	if rep := post("update/u/x/v", `{"is_secret":true}`); rep.Status != 200 {
		t.Fatalf("secrecy: %d %s", rep.Status, rep.Body)
	}
	if v, _, _ := store.Variable(t.Context(), "u/x/v"); !v.IsSecret || v.Value != "second" {
		t.Fatalf("the variable after a secrecy update: %+v", v)
	}
	for _, c := range []struct {
		name, path, body string
		status           int
		text             string
	}{
		{"missing", "update/u/x/none", `{"value":"x"}`, 404, "Not found: Variable u/x/none not found"},
		{"rename", "update/u/x/v", `{"path":"u/x/w"}`, 501, UnsupportedPrefix + "POST /w/{ws}/variables/update/{path}"},
		{"already encrypted", "update/u/x/v?already_encrypted=true", `{"value":"x"}`, 501, UnsupportedPrefix + "POST /w/{ws}/variables/update/{path}"},
		{"not an object", "update/u/x/v", `[1]`, 400, ""},
	} {
		rep := post(c.path, c.body)
		if rep.Status != c.status || c.text != "" && string(rep.Body) != c.text {
			t.Errorf("%s: %d %s", c.name, rep.Status, rep.Body)
		}
	}
	// A variable service that takes no writes.
	ro := newFacade(t, Config{Platform: &platform.Services{Variables: readOnly{store}}})
	ro.Register("inv-b", "F", env("S", "F", "tok-b"))
	if rep := intercept(ro, "inv-b", "POST", base+"update/u/x/v", nil, []byte(`{"value":"x"}`)); rep.Status != 501 {
		t.Fatalf("read-only: %d %s", rep.Status, rep.Body)
	}
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
		{own, []string{`"run"`, `{"tenant":"a"}`, `"root-a"`}},
		{nil, []string{`"base"`, "Not found: Resource u/x/r not found", `"root-a"`}},
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

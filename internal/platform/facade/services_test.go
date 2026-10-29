package facade

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The root lookup answers from the job registry (milestone PF3): a job of
// a live run after the job itself ended, the run, and a flow scope a job
// runs in; a job whose run has ended, and one never registered, answer
// 501. A read's job_id finds a script job's variables the same way.
func TestRootLookupFromTheRegistry(t *testing.T) {
	services := &platform.Services{}
	f := newFacade(t, Config{Platform: services})
	const run, child = "00000000-0000-4000-8000-0000000000aa", "00000000-0000-4000-8000-0000000000bb"
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run, Path: "f/x"})
	stepEnv := map[string]string{"WM_JOB_ID": "job-a", "WM_FLOW_JOB_ID": child, "WM_ROOT_FLOW_JOB_ID": child, "WM_ROOT_JOB_ID": run, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws"}
	a := f.Register("inv-a", run, stepEnv)
	a.End()
	b := f.Register("inv-b", run, env("job-b", run, "tok-b"))
	defer b.End()
	origin := f.WarmOrigin()
	lookup := func(id string) (int, string) {
		rep := intercept(f, "inv-b", "GET", origin+"/api/w/ws/jobs_u/get_root_job_id/"+id, nil, nil)
		return rep.Status, string(rep.Body)
	}
	for _, id := range []string{"job-a", run, child, "job-b"} {
		if code, body := lookup(id); code != 200 || body != `"`+run+`"` {
			t.Errorf("%s: %d %s", id, code, body)
		}
	}
	if code, body := lookup("elsewhere"); code != 501 || body != "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/get_root_job_id/{id}" {
		t.Errorf("an unknown job: %d %s", code, body)
	}
	if env, ok := f.jobEnv("job-a"); !ok || env["WM_FLOW_JOB_ID"] != child {
		t.Errorf("the ended job's variables: %v %v", env, ok)
	}
	if _, ok := f.jobEnv(child); ok {
		t.Error("a flow job has a script job's variables")
	}
	c := f.Register("inv-c", "run-c", env("job-c", "root-c", "tok-c"))
	defer c.End()
	endRun()
	b.End()
	rep := intercept(f, "inv-c", "GET", origin+"/api/w/ws/jobs_u/get_root_job_id/job-a", nil, nil)
	if rep.Status != 501 {
		t.Errorf("a job of an ended run: %d %s", rep.Status, rep.Body)
	}
}

// Flow user state through the routes (WA-14), on both transports: the
// write's text answer and the read's JSON, null for a missing key and for
// a script job, and the 404 of a write at a script job.
func TestFlowUserStateRoutes(t *testing.T) {
	services := &platform.Services{}
	f := newFacade(t, Config{Platform: services})
	const run = "00000000-0000-4000-8000-0000000000aa"
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run})
	defer endRun()
	s := f.Register("inv-a", run, env("job-a", run, "tok-a"))
	defer s.End()
	base := f.WarmOrigin() + "/api/w/ws/jobs/flow/user_states/"
	for _, c := range []struct {
		method, path, body string
		status             int
		typ, answer        string
	}{
		{"POST", run + "/k", `{"n": 1}`, 200, "text/plain", "Flow job state updated"},
		{"GET", run + "/k", "", 200, "application/json", `{"n":1}`},
		{"GET", run + "/missing", "", 200, "application/json", "null"},
		{"POST", "job-a/k", "1", 404, "text/plain", "Not found: Flow job not found"},
		{"GET", "job-a/k", "", 200, "application/json", "null"},
		{"POST", run + "/k", "not json", 400, "text/plain", ""},
	} {
		var body []byte
		if c.body != "" {
			body = []byte(c.body)
		}
		rep := intercept(f, "inv-a", c.method, base+c.path, nil, body)
		if rep.Status != c.status || rep.Headers["content-type"] != c.typ || (c.answer != "" && string(rep.Body) != c.answer) {
			t.Errorf("%s %s: %d %v %s", c.method, c.path, rep.Status, rep.Headers, rep.Body)
		}
	}
	if code, body := get(t, f.FreshOrigin()+"/api/w/ws/jobs/flow/user_states/"+run+"/k", "tok-a"); code != 200 || body != `{"n":1}` {
		t.Errorf("over TCP: %d %s", code, body)
	}
	if got := s.Unsupported(context.Background()); len(got) != 0 {
		t.Errorf("unsupported %q", got)
	}
}

// fakeDispatcher records dispatches and answers results from a table.
type fakeDispatcher struct {
	mu      sync.Mutex
	got     []platform.Dispatch
	results map[string]platform.JobResult
	err     error
}

func (d *fakeDispatcher) Dispatch(_ context.Context, req platform.Dispatch) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, req)
	if d.err != nil {
		return "", d.err
	}
	return "00000000-0000-4000-8000-00000000d150", nil
}

func (d *fakeDispatcher) Result(_ context.Context, id string) (platform.JobResult, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.results[id]
	return r, ok, nil
}

// The dispatch routes (WA-15): the target, arguments and query reach the
// dispatcher with the caller's variables, and the answer is 201 with the
// job id as text; a target not in the bundle, a hash, and every target
// without a dispatcher answer 501 with "dispatch target not in bundle:
// <target>", which the policy quotes; a query parameter that would change
// the run answers 501; a bad scheduled_in_secs and arguments that are not
// an object answer 400.
func TestDispatchRoutes(t *testing.T) {
	d := &fakeDispatcher{}
	f := newFacade(t, Config{Platform: &platform.Services{Dispatcher: d}})
	s := f.Register("inv-a", "run-a", env("job-a", "root-a", "tok-a"))
	base := f.WarmOrigin() + "/api/w/ws/jobs/run/"
	rep := intercept(f, "inv-a", "POST", base+"p/u/t/x?parent_job=job-a&root_job=root-a&tag=t1&scheduled_in_secs=2", nil, []byte(`{"x":1}`))
	if rep.Status != 201 || rep.Headers["content-type"] != "text/plain" || string(rep.Body) != "00000000-0000-4000-8000-00000000d150" {
		t.Errorf("dispatch: %d %v %s", rep.Status, rep.Headers, rep.Body)
	}
	intercept(f, "inv-a", "POST", base+"f/f/t/y", nil, nil)
	if len(d.got) != 2 {
		t.Fatalf("dispatched %+v", d.got)
	}
	g := d.got[0]
	if g.Kind != platform.DispatchScript || g.Target != "u/t/x" || string(g.Args) != `{"x":1}` || g.ParentJob != "job-a" || g.RootJob != "root-a" || g.Tag != "t1" || g.ScheduledInSecs != 2 || g.Caller["WM_JOB_ID"] != "job-a" {
		t.Errorf("the script dispatch: %+v", g)
	}
	if g := d.got[1]; g.Kind != platform.DispatchFlow || g.Target != "f/t/y" || g.Args != nil || g.ParentJob != "" {
		t.Errorf("the flow dispatch: %+v", g)
	}
	for _, c := range []struct{ url, body string }{
		{base + "h/abc123", "dispatch target not in bundle: abc123"},
		{base + "p/u/t/x?cache_ttl=5", "Not implemented in windmill-lite: POST /w/{ws}/jobs/run/p/{path} with cache_ttl"},
		{base + "f/f/t/y?scheduled_for=2030-01-01T00:00:00Z", "Not implemented in windmill-lite: POST /w/{ws}/jobs/run/f/{path} with scheduled_for"},
		{base + "p/u/t/x?job_id=00000000-0000-4000-8000-000000000001", "Not implemented in windmill-lite: POST /w/{ws}/jobs/run/p/{path} with job_id"},
	} {
		if rep := intercept(f, "inv-a", "POST", c.url, nil, nil); rep.Status != 501 || string(rep.Body) != c.body {
			t.Errorf("%s: %d %s", c.url, rep.Status, rep.Body)
		}
	}
	if rep := intercept(f, "inv-a", "POST", base+"p/u/t/x?scheduled_in_secs=soon", nil, nil); rep.Status != 400 {
		t.Errorf("a bad scheduled_in_secs: %d %s", rep.Status, rep.Body)
	}
	d.err = &platform.NotInBundleError{Target: "u/t/z"}
	if rep := intercept(f, "inv-a", "POST", base+"p/u/t/z", nil, nil); rep.Status != 501 || string(rep.Body) != "dispatch target not in bundle: u/t/z" {
		t.Errorf("refused: %d %s", rep.Status, rep.Body)
	}
	d.err = &platform.Error{Variant: "BadRequest", Message: "the arguments are not an object"}
	if rep := intercept(f, "inv-a", "POST", base+"p/u/t/x", nil, []byte("[]")); rep.Status != 400 || string(rep.Body) != "Bad request: the arguments are not an object" {
		t.Errorf("bad arguments: %d %s", rep.Status, rep.Body)
	}
	_, message := s.unsupported(context.Background())
	if message != "dispatch target not in bundle: abc123" {
		t.Errorf("the policy's message %q", message)
	}

	none := newFacade(t, Config{})
	none.Register("inv-b", "run-b", env("job-b", "root-b", "tok-b"))
	if rep := intercept(none, "inv-b", "POST", none.WarmOrigin()+"/api/w/ws/jobs/run/p/u/t/x", nil, nil); rep.Status != 501 || string(rep.Body) != "dispatch target not in bundle: u/t/x" {
		t.Errorf("no dispatcher: %d %s", rep.Status, rep.Body)
	}
}

// The result polls (WA-15, WA-V2): a completed dispatched run, a running
// one with and without get_started, a UUID that names no job, an id that
// is not a UUID (400, so that waitJob fails at once), and a job of
// windmill-lite's that the dispatcher did not start (501).
func TestResultRoutes(t *testing.T) {
	const done, running, unknown = "00000000-0000-4000-8000-0000000000d0", "00000000-0000-4000-8000-0000000000d1", "00000000-0000-4000-8000-0000000000d2"
	const step = "00000000-0000-4000-8000-0000000000e0"
	d := &fakeDispatcher{results: map[string]platform.JobResult{
		done:    {Started: true, Completed: true, Success: false, Result: json.RawMessage(`{"error":{"message":"boom","name":"Error"}}`)},
		running: {Started: true},
	}}
	f := newFacade(t, Config{Platform: &platform.Services{Dispatcher: d}})
	s := f.Register("inv-a", "run-a", env(step, "root-a", "tok-a"))
	defer s.End()
	base := f.WarmOrigin() + "/api/w/ws/jobs_u/completed/"
	for _, c := range []struct {
		path   string
		status int
		body   string
	}{
		{"get_result_maybe/" + done, 200, `{"started":true,"success":false,"completed":true,"result":{"error":{"message":"boom","name":"Error"}}}`},
		{"get_result_maybe/" + running, 200, `{"started":null,"success":null,"completed":false,"result":null}`},
		{"get_result_maybe/" + running + "?get_started=true", 200, `{"started":true,"success":null,"completed":false,"result":null}`},
		{"get_result_maybe/" + unknown, 200, `{"started":null,"success":null,"completed":false,"result":null}`},
		{"get_result_maybe/" + unknown + "?get_started=true", 200, `{"started":false,"success":null,"completed":false,"result":null}`},
		{"get_result_maybe/dispatch target not in bundle: u/t/x", 400, `Bad request: windmill-lite: the job id "dispatch target not in bundle: u/t/x" is not a UUID`},
		{"get_result_maybe/" + step, 501, "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}"},
		{"get_result/" + done, 200, `{"error":{"message":"boom","name":"Error"}}`},
		{"get_result/" + running, 404, "Not found: windmill-lite: no completed job " + running},
		{"get_result/" + unknown, 404, "Not found: windmill-lite: no completed job " + unknown},
		{"get_result/" + step, 501, "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/completed/get_result/{id}"},
		{"get_result/nope", 400, `Bad request: windmill-lite: the job id "nope" is not a UUID`},
	} {
		rep := intercept(f, "inv-a", "GET", base+c.path, nil, nil)
		if rep.Status != c.status || string(rep.Body) != c.body {
			t.Errorf("%s: %d %s", c.path, rep.Status, rep.Body)
		}
	}
}

// The object store routes (WA-16): an explicit-key upload answers its
// file_key and a download gives the content back with the upload's
// content type; what the store does not serve (no store, no file_key, a
// resource's bucket) answers 501 naming it, and a missing object 404.
func TestObjectRoutes(t *testing.T) {
	objects, err := platform.NewDirObjects(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := newFacade(t, Config{Platform: &platform.Services{Objects: objects}})
	f.Register("inv-a", "run-a", env("job-a", "root-a", "tok-a"))
	base := f.WarmOrigin() + "/api/w/ws/job_helpers/"
	rep := intercept(f, "inv-a", "POST", base+"upload_s3_file?file_key=t%2Fa.txt&storage=s1&content_type=text%2Fcsv&content_disposition=inline", nil, []byte("a,b"))
	if rep.Status != 200 || rep.Headers["content-type"] != "application/json" || string(rep.Body) != `{"file_key":"t/a.txt"}` {
		t.Errorf("upload: %d %v %s", rep.Status, rep.Headers, rep.Body)
	}
	rep = intercept(f, "inv-a", "GET", base+"download_s3_file?file_key=t%2Fa.txt&storage=s1", nil, nil)
	if rep.Status != 200 || rep.Headers["content-type"] != "text/csv" || rep.Headers["content-disposition"] != "inline" || string(rep.Body) != "a,b" {
		t.Errorf("download: %d %v %s", rep.Status, rep.Headers, rep.Body)
	}
	for _, c := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "download_s3_file?file_key=t%2Fa.txt", 404, `Not found: windmill-lite: no object "t/a.txt" in storage "_default_"`},
		{"POST", "upload_s3_file", 501, "Not implemented in windmill-lite: POST /w/{ws}/job_helpers/upload_s3_file without file_key (a key the server generates)"},
		{"POST", "upload_s3_file?file_key=k&s3_resource_path=u%2Fx%2Fs3", 501, "Not implemented in windmill-lite: POST /w/{ws}/job_helpers/upload_s3_file with s3_resource_path (a resource's own bucket)"},
		{"DELETE", "delete_s3_file?file_key=k", 501, "Not implemented in windmill-lite: DELETE /w/{ws}/job_helpers/delete_s3_file"},
	} {
		rep := intercept(f, "inv-a", c.method, base+c.path, nil, nil)
		if rep.Status != c.status || string(rep.Body) != c.body {
			t.Errorf("%s %s: %d %s", c.method, c.path, rep.Status, rep.Body)
		}
	}
	none := newFacade(t, Config{})
	none.Register("inv-b", "run-b", env("job-b", "root-b", "tok-b"))
	rep = intercept(none, "inv-b", "POST", none.WarmOrigin()+"/api/w/ws/job_helpers/upload_s3_file?file_key=k", nil, []byte("x"))
	if rep.Status != 501 || !strings.HasSuffix(string(rep.Body), "(no object store is configured)") {
		t.Errorf("no store: %d %s", rep.Status, rep.Body)
	}
}

package facade

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The root lookup answers from the job registry: a job of a live run
// after the job itself ended, the run, a flow scope a job runs in, and a
// job of a run that has ended, which Windmill still answers from its job
// row (WA-19). The id is read as Windmill's UUID parser reads it, so its
// other forms name the same job. An id with no job in the path's
// workspace answers as Windmill answers one with no job row (WA-19/b):
// one never registered, and a job looked up in another workspace, which
// the unauthenticated route does not refuse for the token (WA-4/b). An id
// that is not a UUID gets the web framework's 400 (WA-19/c).
func TestRootLookupFromTheRegistry(t *testing.T) {
	services := &platform.Services{}
	f := newFacade(t, Config{Platform: services})
	const run, child = "00000000-0000-4000-8000-0000000000aa", "00000000-0000-4000-8000-0000000000bb"
	jobA, jobB := uid("job-a"), uid("job-b")
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run, Path: "f/x"})
	stepEnv := map[string]string{"WM_JOB_ID": jobA, "WM_FLOW_JOB_ID": child, "WM_ROOT_FLOW_JOB_ID": child, "WM_ROOT_JOB_ID": run, "WM_TOKEN": "tok-a", "WM_WORKSPACE": "ws"}
	a := f.Register("inv-a", run, stepEnv)
	a.End()
	b := f.Register("inv-b", run, env(jobB, run, "tok-b"))
	defer b.End()
	origin := f.WarmOrigin()
	lookup := func(inv, ws, id string) harness.PlatformReply {
		return intercept(f, inv, "GET", origin+"/api/w/"+ws+"/jobs_u/get_root_job_id/"+id, nil, nil)
	}
	isRoot := func(what string, rep harness.PlatformReply) {
		t.Helper()
		if rep.Status != 200 || rep.Headers["content-type"] != "application/json" || string(rep.Body) != `"`+run+`"` {
			t.Errorf("%s: %d %s", what, rep.Status, rep.Body)
		}
	}
	for _, id := range []string{jobA, run, child, jobB} {
		isRoot(id, lookup("inv-b", "ws", id))
	}
	for _, id := range []string{strings.ToUpper(jobA), strings.ReplaceAll(jobA, "-", ""), "{" + jobA + "}", "urn:uuid:" + jobA} {
		isRoot("the form "+id, lookup("inv-b", "ws", id))
	}
	const noRow = "SqlErr: no rows returned by a query that expected to return at least one row"
	noJob := func(what string, rep harness.PlatformReply) {
		t.Helper()
		if rep.Status != 400 || rep.Headers["content-type"] != "text/plain" || string(rep.Body) != noRow {
			t.Errorf("%s: %d %v %s", what, rep.Status, rep.Headers, rep.Body)
		}
	}
	noJob("an unknown job", lookup("inv-b", "ws", "00000000-0000-0000-0000-000000000001"))
	noJob("its own job in another workspace", lookup("inv-b", "other", jobB))
	if code, body := get(t, f.FreshOrigin()+"/api/w/ws/jobs_u/get_root_job_id/00000000-0000-0000-0000-000000000001", "tok-b"); code != 400 || body != noRow {
		t.Errorf("an unknown job over TCP: %d %s", code, body)
	}
	const notUUID = "Invalid URL: Cannot parse `id` with value `abc`: UUID parsing failed: invalid length: found 3"
	if rep := lookup("inv-b", "ws", "abc"); rep.Status != 400 || rep.Headers["content-type"] != "text/plain; charset=utf-8" || string(rep.Body) != notUUID {
		t.Errorf("an id that is not a UUID: %d %v %s", rep.Status, rep.Headers, rep.Body)
	}
	c := f.Register("inv-c", "run-c", env("job-c", "root-c", "tok-c"))
	defer c.End()
	endRun()
	b.End()
	isRoot("a job of an ended run", lookup("inv-c", "ws", jobA))
	isRoot("an ended run", lookup("inv-c", "ws", run))
	if got := c.Unsupported(context.Background()); len(got) != 0 {
		t.Errorf("unsupported %q", got)
	}
}

// Flow user state through the routes (WA-14), on both transports: the
// write's text answer, typed as a handler's text answer is, and the read's
// JSON, null for a missing key and for a script job, and the 404 of a
// write at a script job. The web framework's rejections come first
// (WA-14/d): an id that is not a UUID, on either method and before the
// body, then a body without a JSON content type (415) or that is not
// JSON (400), and the state is unchanged.
func TestFlowUserStateRoutes(t *testing.T) {
	services := &platform.Services{}
	f := newFacade(t, Config{Platform: services})
	const run = "00000000-0000-4000-8000-0000000000aa"
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run})
	defer endRun()
	s := f.Register("inv-a", run, env("job-a", run, "tok-a"))
	defer s.End()
	base := f.WarmOrigin() + "/api/w/ws/jobs/flow/user_states/"
	jsonType := map[string]string{"content-type": "application/json"}
	const notUUID = "Invalid URL: Cannot parse `job_id` with value `abc`: UUID parsing failed: invalid length: found 3"
	for _, c := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
		typ, answer        string
	}{
		{"POST", run + "/k", `{"n": 1}`, jsonType, 200, "text/plain; charset=utf-8", "Flow job state updated"},
		{"GET", run + "/k", "", nil, 200, "application/json", `{"n":1}`},
		{"GET", run + "/missing", "", nil, 200, "application/json", "null"},
		{"POST", uid("job-a") + "/k", "1", jsonType, 404, "text/plain", "Not found: Flow job not found"},
		{"GET", uid("job-a") + "/k", "", nil, 200, "application/json", "null"},
		{"POST", run + "/k", "hello", jsonType, 400, "text/plain; charset=utf-8", "Failed to parse the request body as JSON: expected value at line 1 column 1"},
		{"POST", run + "/k", `{"n": 2}`, nil, 415, "text/plain; charset=utf-8", "Expected request with `Content-Type: application/json`"},
		{"POST", run + "/k", `{"n": 2}`, map[string]string{"content-type": "text/plain"}, 415, "text/plain; charset=utf-8", "Expected request with `Content-Type: application/json`"},
		{"POST", run + "/k2", `{"n": 2}`, map[string]string{"content-type": "application/vnd.x+json; charset=utf-8"}, 200, "text/plain; charset=utf-8", "Flow job state updated"},
		{"GET", "abc/k", "", nil, 400, "text/plain; charset=utf-8", notUUID},
		{"POST", "abc/k", "hello", nil, 400, "text/plain; charset=utf-8", notUUID},
		{"GET", run + "/k", "", nil, 200, "application/json", `{"n":1}`},
	} {
		var body []byte
		if c.body != "" {
			body = []byte(c.body)
		}
		rep := intercept(f, "inv-a", c.method, base+c.path, c.headers, body)
		if rep.Status != c.status || rep.Headers["content-type"] != c.typ || string(rep.Body) != c.answer {
			t.Errorf("%s %s %v: %d %v %s", c.method, c.path, c.headers, rep.Status, rep.Headers, rep.Body)
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
	if rep.Status != 201 || rep.Headers["content-type"] != "text/plain; charset=utf-8" || string(rep.Body) != "00000000-0000-4000-8000-00000000d150" {
		t.Errorf("dispatch: %d %v %s", rep.Status, rep.Headers, rep.Body)
	}
	intercept(f, "inv-a", "POST", base+"f/f/t/y", nil, nil)
	if len(d.got) != 2 {
		t.Fatalf("dispatched %+v", d.got)
	}
	g := d.got[0]
	if g.Kind != platform.DispatchScript || g.Target != "u/t/x" || string(g.Args) != `{"x":1}` || g.ParentJob != "job-a" || g.RootJob != "root-a" || g.Tag != "t1" || g.ScheduledInSecs != 2 || g.Caller["WM_JOB_ID"] != uid("job-a") {
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

// The result polls (WA-15, WA-V2, WA-19): a completed dispatched run, a
// running one with and without get_started, a UUID that names no job, an
// id that is not a UUID (the web framework's 400, WA-V2/c, so that
// waitJob fails at once), a job of windmill-lite's that the dispatcher
// did not start (501), and a job looked up in a workspace other than the
// caller's, which reads as unknown (WA-V2/d's second request).
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
		{"get_result_maybe/abc", 400, "Invalid URL: Cannot parse `id` with value `abc`: UUID parsing failed: invalid length: found 3"},
		{"get_result_maybe/dispatch target not in bundle: u/t/x", 400, "Invalid URL: Cannot parse `id` with value `dispatch target not in bundle: u/t/x`: UUID parsing failed: invalid character: found `i` at 2"},
		{"get_result_maybe/" + strings.ToUpper(done), 200, `{"started":true,"success":false,"completed":true,"result":{"error":{"message":"boom","name":"Error"}}}`},
		{"get_result_maybe/" + step, 501, "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}"},
		{"get_result/" + done, 200, `{"error":{"message":"boom","name":"Error"}}`},
		{"get_result/" + running, 404, "Not found: Completed Job not found at name " + running},
		{"get_result/" + unknown, 404, "Not found: Completed Job not found at name " + unknown},
		{"get_result/" + step, 501, "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/completed/get_result/{id}"},
		{"get_result/nope", 400, "Invalid URL: Cannot parse `id` with value `nope`: UUID parsing failed: invalid character: found `n` at 1"},
	} {
		rep := intercept(f, "inv-a", "GET", base+c.path, nil, nil)
		if rep.Status != c.status || string(rep.Body) != c.body {
			t.Errorf("%s: %d %s", c.path, rep.Status, rep.Body)
		}
		if want := "text/plain; charset=utf-8"; c.status == 400 && rep.Headers["content-type"] != want {
			t.Errorf("%s: typed %q, want %q", c.path, rep.Headers["content-type"], want)
		}
	}
	other := f.WarmOrigin() + "/api/w/other/jobs_u/completed/"
	for _, c := range []struct {
		path   string
		status int
		body   string
	}{
		{"get_result_maybe/" + done, 200, `{"started":null,"success":null,"completed":false,"result":null}`},
		{"get_result_maybe/" + step + "?get_started=true", 200, `{"started":false,"success":null,"completed":false,"result":null}`},
		{"get_result/" + done, 404, "Not found: Completed Job not found at name " + done},
		{"get_result/" + step, 404, "Not found: Completed Job not found at name " + step},
	} {
		rep := intercept(f, "inv-a", "GET", other+c.path, nil, nil)
		if rep.Status != c.status || string(rep.Body) != c.body {
			t.Errorf("another workspace's %s: %d %s", c.path, rep.Status, rep.Body)
		}
	}
}

// The object store routes (WA-16): an explicit-key upload answers its
// file_key and a download gives the content back with the upload's
// content type; what the store does not serve (no file_key, a resource's
// bucket) answers 501 naming it, and a missing object 404. With no object
// store, both routes answer 500, as Windmill CE answers an upload with no
// workspace storage (WA-16/d), and the call is not unsupported.
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
	s := none.Register("inv-b", "run-b", env("job-b", "root-b", "tok-b"))
	defer s.End()
	for _, c := range []struct{ method, path string }{{"POST", "upload_s3_file?file_key=k"}, {"GET", "download_s3_file?file_key=k"}} {
		rep = intercept(none, "inv-b", c.method, none.WarmOrigin()+"/api/w/ws/job_helpers/"+c.path, nil, []byte("x"))
		if rep.Status != 500 || rep.Headers["content-type"] != "text/plain" || string(rep.Body) != "Internal: windmill-lite: no object store is configured" {
			t.Errorf("no store, %s %s: %d %v %s", c.method, c.path, rep.Status, rep.Headers, rep.Body)
		}
	}
	if got := s.Unsupported(context.Background()); len(got) != 0 {
		t.Errorf("no store: unsupported %q", got)
	}
}

// WA-4/b: an authenticated route whose path names a workspace other than
// the caller's (WM_WORKSPACE, which its token is valid for) answers 401
// `Not authorized: Unauthorized` before any handler runs: before the 501
// of an operation without one, and before a path or body rejection, on
// both transports, and the stored state is unchanged. The call is
// recorded with its 401 and is not unsupported. The unauthenticated
// jobs_u routes and the routes outside a workspace are not refused for
// it, and a caller without WM_WORKSPACE is of every workspace.
func TestTokenWorkspace(t *testing.T) {
	store := platform.NewStore()
	store.SetVariable("u/admin/v", platform.Variable{Value: "x"})
	services := &platform.Services{Variables: store}
	var mu sync.Mutex
	statuses := map[string]int{}
	f := newFacade(t, Config{Platform: services, Answered: func(_ string, r Record) {
		mu.Lock()
		defer mu.Unlock()
		statuses[r.Method+" "+r.Path] = r.Status
	}})
	const run = "00000000-0000-4000-8000-0000000000aa"
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run})
	defer endRun()
	s := f.Register("inv-a", run, env("job-a", run, "tok-a"))
	defer s.End()
	origin := f.WarmOrigin()
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/w/other/variables/get_value/u/admin/v", ""},
		{"POST", "/api/w/other/jobs/flow/user_states/" + run + "/k", "1"},
		{"POST", "/api/w/other/jobs/flow/user_states/abc/k", "hello"},
		{"POST", "/api/w/other/variables/create", "{}"},
	} {
		rep := intercept(f, "inv-a", c.method, origin+c.path, nil, []byte(c.body))
		if rep.Status != 401 || rep.Headers["content-type"] != "text/plain" || string(rep.Body) != Unauthorized {
			t.Errorf("%s %s: %d %v %s", c.method, c.path, rep.Status, rep.Headers, rep.Body)
		}
		mu.Lock()
		if got := statuses[c.method+" "+c.path]; got != 401 {
			t.Errorf("%s %s recorded with %d", c.method, c.path, got)
		}
		mu.Unlock()
	}
	if code, body := get(t, f.FreshOrigin()+"/api/w/other/variables/get_value/u/admin/v", "tok-a"); code != 401 || body != Unauthorized {
		t.Errorf("over TCP: %d %s", code, body)
	}
	if got := s.Unsupported(context.Background()); len(got) != 0 {
		t.Errorf("unsupported %q", got)
	}
	for _, c := range []struct {
		path   string
		status int
		body   string
	}{
		{"/api/w/ws/jobs/flow/user_states/" + run + "/k", 200, "null"},
		{"/api/w/ws/variables/get_value/u/admin/v", 200, `"x"`},
		{"/api/w/other/jobs_u/get_root_job_id/" + uid("job-a"), 400, "SqlErr: no rows returned by a query that expected to return at least one row"},
		{"/api/w/ws/jobs_u/get_root_job_id/" + uid("job-a"), 200, `"` + run + `"`},
		{"/api/users/whoami", 501, UnsupportedPrefix + "GET /users/whoami"},
		{"/api/w/other/apps_u/public_app/f/x", 501, UnsupportedPrefix + "GET /w/{ws}/apps_u/public_app/{path}"},
	} {
		if rep := intercept(f, "inv-a", "GET", origin+c.path, nil, nil); rep.Status != c.status || string(rep.Body) != c.body {
			t.Errorf("%s: %d %s", c.path, rep.Status, rep.Body)
		}
	}
	n := f.Register("inv-n", run, map[string]string{"WM_JOB_ID": "job-n", "WM_TOKEN": "tok-n"})
	defer n.End()
	if rep := intercept(f, "inv-n", "GET", origin+"/api/w/any/variables/get_value/u/admin/v", nil, nil); rep.Status != 200 || string(rep.Body) != `"x"` {
		t.Errorf("a caller without WM_WORKSPACE: %d %s", rep.Status, rep.Body)
	}
}

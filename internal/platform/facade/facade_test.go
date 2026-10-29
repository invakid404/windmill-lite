package facade

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

func newFacade(t *testing.T, cfg Config) *Facade {
	t.Helper()
	f, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// intercept sends one platform frame's request through the facade's
// harness handler, as the Process does, and returns the reply.
func intercept(f *Facade, inv, method, url string, headers map[string]string, body []byte) harness.PlatformReply {
	serve := f.Platform()(harness.PlatformRequest{ID: "p", Inv: inv, Method: method, URL: url, Headers: headers, Body: body})
	return serve(context.Background())
}

func get(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func env(job, root, token string) map[string]string {
	return map[string]string{"WM_JOB_ID": job, "WM_FLOW_JOB_ID": root, "WM_ROOT_FLOW_JOB_ID": root, "WM_ROOT_JOB_ID": root, "WM_TOKEN": token, "WM_WORKSPACE": "ws"}
}

// On the intercepted path a call is attributed to the invocation the
// harness named, whatever token it carries (WA-31, ADR 0007): two
// invocations sending the same token, as a client imported once sends
// it, each get their own answers and records.
func TestInterceptedAttribution(t *testing.T) {
	f := newFacade(t, Config{})
	a := f.Register("inv-a", "run-a", env("job-a", "root-a", "tok-a"))
	b := f.Register("inv-b", "run-b", env("job-b", "root-b", "tok-b"))
	origin := f.WarmOrigin()
	for _, c := range []struct{ inv, job, root string }{{"inv-a", "job-a", "root-a"}, {"inv-b", "job-b", "root-b"}} {
		rep := intercept(f, c.inv, "GET", origin+"/api/w/ws/jobs_u/get_root_job_id/"+c.job, map[string]string{"authorization": "Bearer tok-a"}, nil)
		if rep.Status != 200 || string(rep.Body) != `"`+c.root+`"` || rep.Headers["content-type"] != "application/json" {
			t.Errorf("%s: %d %s %v", c.inv, rep.Status, rep.Body, rep.Headers)
		}
	}
	for s, want := range map[*Session]string{a: "inv-a", b: "inv-b"} {
		calls := s.Calls()
		if len(calls) != 1 || calls[0].Inv != want || calls[0].Route != "GET /w/{ws}/jobs_u/get_root_job_id/{id}" || calls[0].Status != 200 || calls[0].Headers["authorization"] != "Bearer tok-a" {
			t.Errorf("%s: %+v", want, calls)
		}
	}
	// An invocation the facade does not know of is answered 401, as a
	// stray call.
	rep := intercept(f, "inv-unknown", "GET", origin+"/api/w/ws/jobs_u/get_root_job_id/job-a", nil, nil)
	if rep.Status != 401 || string(rep.Body) != UnknownToken {
		t.Errorf("unknown invocation: %d %s", rep.Status, rep.Body)
	}
	if s := f.Stray(); len(s) != 1 || s[0].Inv != "" || s[0].Status != 401 {
		t.Errorf("stray: %+v", s)
	}
}

// Over TCP a call is attributed by its token; an unknown token, or the
// token of an invocation that has ended, answers 401 and is recorded as a
// stray, never guessed onto another invocation (ADR 0007).
func TestTCPAttribution(t *testing.T) {
	f := newFacade(t, Config{})
	a := f.Register("inv-a", "run-a", env("job-a", "root-a", "tok-a"))
	f.Register("inv-b", "run-b", env("job-b", "root-b", "tok-b"))
	url := f.FreshOrigin() + "/api/w/ws/jobs_u/get_root_job_id/job-a"
	if code, body := get(t, url, "tok-a"); code != 200 || body != `"root-a"` {
		t.Errorf("own token: %d %s", code, body)
	}
	// Another live invocation's token is that invocation's: the call is
	// attributed to it, and the job it names is still answered.
	if code, _ := get(t, url, "tok-b"); code != 200 {
		t.Errorf("another token: %d", code)
	}
	for _, tok := range []string{"", "nope"} {
		if code, body := get(t, url, tok); code != 401 || body != UnknownToken {
			t.Errorf("token %q: %d %s", tok, code, body)
		}
	}
	a.End()
	if code, body := get(t, url, "tok-a"); code != 401 || body != UnknownToken {
		t.Errorf("ended token: %d %s", code, body)
	}
	if calls := a.Calls(); len(calls) != 1 || calls[0].Path != "/api/w/ws/jobs_u/get_root_job_id/job-a" {
		t.Errorf("a's calls: %+v", calls)
	}
	if s := f.Stray(); len(s) != 3 {
		t.Errorf("%d strays, want 3: %+v", len(s), s)
	}
	// A token two live invocations carry names neither.
	f.Register("inv-c", "run-c", env("job-c", "root-c", "shared"))
	f.Register("inv-d", "run-d", env("job-d", "root-d", "shared"))
	if code, _ := get(t, url, "shared"); code != 401 {
		t.Errorf("shared token: %d", code)
	}
}

// The warm origin refuses every connection (ADR 0001 §5a): nothing listens
// there, so a transport other than the intercepted fetch reaches no
// handler and leaves no record.
func TestWarmOriginRefuses(t *testing.T) {
	f := newFacade(t, Config{})
	f.Register("inv-a", "run-a", env("job-a", "root-a", "tok-a"))
	addr := strings.TrimPrefix(f.WarmOrigin(), "http://")
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err == nil {
		c.Close()
		t.Fatal("a connection to the warm origin was accepted")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("dial: %v, want connection refused", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("refused after %s, want at once", d)
	}
	if s := f.Stray(); len(s) != 0 {
		t.Errorf("strays: %+v", s)
	}
	if !strings.HasPrefix(f.WarmOrigin(), "http://127.0.0.1:") || !strings.HasPrefix(f.FreshOrigin(), "http://127.0.0.1:") || f.WarmOrigin() == f.FreshOrigin() {
		t.Errorf("origins %s and %s", f.WarmOrigin(), f.FreshOrigin())
	}
}

// A route with no handler, a path no route has, and a path outside the
// API all answer 501 text/plain, never 404, naming the route as WA-41 and
// WA-V5 write it, and are recorded as unsupported (ADR 0007).
func TestUnsupportedRoutes(t *testing.T) {
	f := newFacade(t, Config{})
	s := f.Register("inv-a", "run-a", env("job-a", "root-a", "tok-a"))
	o := f.WarmOrigin()
	for _, c := range []struct{ method, url, body string }{
		{"GET", o + "/api/w/ws/jobs_u/get/j1", "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/get/{id}"},
		{"POST", o + "/api/w/ws/jobs/run/p/u/t/x?tag=a", "Not implemented in windmill-lite: POST /w/{ws}/jobs/run/p/{path}"},
		{"POST", o + "/api/w/ws/jobs/flow/user_states/r1/k", "Not implemented in windmill-lite: POST /w/{ws}/jobs/flow/user_states/{id}/{key}"},
		// A job the facade does not know of (PF3's registry).
		{"GET", o + "/api/w/ws/jobs_u/get_root_job_id/elsewhere", "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/get_root_job_id/{id}"},
		{"GET", o + "/api/w/ws/no_such/route", "Not implemented in windmill-lite: GET /w/ws/no_such/route"},
		{"GET", o + "/elsewhere", "Not implemented in windmill-lite: GET /elsewhere"},
	} {
		rep := intercept(f, "inv-a", c.method, c.url, nil, nil)
		if rep.Status != 501 || string(rep.Body) != c.body || rep.Headers["content-type"] != "text/plain" {
			t.Errorf("%s %s: %d %q %v", c.method, c.url, rep.Status, rep.Body, rep.Headers)
		}
	}
	got := s.Unsupported(context.Background())
	want := []string{"GET /w/{ws}/jobs_u/get/{id}", "POST /w/{ws}/jobs/run/p/{path}", "POST /w/{ws}/jobs/flow/user_states/{id}/{key}",
		"GET /w/{ws}/jobs_u/get_root_job_id/{id}", "GET /w/ws/no_such/route", "GET /elsewhere"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("unsupported %q, want %q", got, want)
	}
	if calls := s.Calls(); calls[1].Query != "tag=a" || calls[1].Path != "/api/w/ws/jobs/run/p/u/t/x" {
		t.Errorf("recorded %+v", calls[1])
	}
}

// fakeExec answers each invocation with a fixed outcome after running do,
// which stands for the step's platform calls, and settles it.
type fakeExec struct {
	out engine.StepOutcome
	err error
	do  func(inv engine.Invocation)
}

func (x *fakeExec) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	if x.do != nil {
		x.do(inv)
	}
	if x.err != nil {
		return engine.StepOutcome{}, x.err
	}
	inv.Report.Settled(engine.SettleCompleted)
	return x.out, nil
}

type settleRecorder struct {
	engine.NopReporter
	mu      sync.Mutex
	settled []engine.SettleKind
}

func (r *settleRecorder) Settled(k engine.SettleKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled = append(r.settled, k)
}

// The 501 policy (WA-41/b, ADR 0007): in fail mode any outcome of an
// invocation that made an unsupported call becomes UnsupportedPlatformCall,
// naming the first call and listing all; in observe mode the outcome
// stands; an invocation without one is untouched; the registration ends at
// settlement and the record goes to Recorded.
func TestExecutorPolicy(t *testing.T) {
	ok := engine.StepOutcome{Kind: engine.OutcomeOK, Body: jsonval.FromProducer([]byte(`"job id"`))}
	thrown := engine.StepOutcome{Kind: engine.OutcomeError, Body: jsonval.FromProducer([]byte(`{"name":"ApiError"}`))}
	calls := func(urls ...string) func(f *Facade) func(engine.Invocation) {
		return func(f *Facade) func(engine.Invocation) {
			return func(inv engine.Invocation) {
				for _, u := range urls {
					intercept(f, inv.ID, "POST", f.WarmOrigin()+u, nil, nil)
				}
			}
		}
	}
	const want = `{"name":"UnsupportedPlatformCall","message":"Not implemented in windmill-lite: POST /w/{ws}/jobs/run/p/{path}","calls":["POST /w/{ws}/jobs/run/p/{path}","POST /w/{ws}/oidc/token/{audience}"]}`
	for _, c := range []struct {
		name   string
		policy Policy
		out    engine.StepOutcome
		do     func(f *Facade) func(engine.Invocation)
		want   string
		origin engine.Origin
	}{
		{"fail, ok replaced", PolicyFail, ok, calls("/api/w/ws/jobs/run/p/u/t/x", "/api/w/ws/oidc/token/aud"), want, engine.OriginFacade},
		{"fail, error replaced", PolicyFail, thrown, calls("/api/w/ws/jobs/run/p/u/t/x", "/api/w/ws/oidc/token/aud"), want, engine.OriginFacade},
		{"observe", PolicyObserve, ok, calls("/api/w/ws/jobs/run/p/u/t/x"), `"job id"`, ""},
		{"no unsupported call", PolicyFail, ok, nil, `"job id"`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var recorded []Record
			f := newFacade(t, Config{Policy: c.policy, Recorded: func(inv string, rs []Record) { recorded = rs }})
			x := &fakeExec{out: c.out}
			if c.do != nil {
				x.do = c.do(f)
			}
			rep := &settleRecorder{}
			inv := engine.Invocation{ID: "inv-a", Run: "run-a", Env: env("job-a", "root-a", "tok-a"), Report: rep}
			out, err := f.Executor(x).Execute(context.Background(), inv)
			if err != nil {
				t.Fatal(err)
			}
			if string(out.Body.Producer()) != c.want || out.Origin != c.origin {
				t.Errorf("outcome %s (origin %q), want %s (origin %q)", out.Body.Producer(), out.Origin, c.want, c.origin)
			}
			if c.origin == engine.OriginFacade {
				st := engine.StoreOutcome(out, "a")
				if !st.Failed || st.Origin != engine.OriginFacade || string(st.Value.Producer()) != `{"error":`+want+`}` {
					t.Errorf("stored %+v %s", st, st.Value.Producer())
				}
			}
			if len(rep.settled) != 1 {
				t.Errorf("settled %v", rep.settled)
			}
			if c.do != nil && len(recorded) == 0 {
				t.Error("nothing recorded")
			}
			// The registration has ended: the token answers 401.
			if code, _ := get(t, f.FreshOrigin()+"/api/w/ws/jobs_u/get_root_job_id/job-a", "tok-a"); code != 401 {
				t.Errorf("token after settlement: %d", code)
			}
		})
	}
	// An invocation that never ran keeps its error, settled not_started.
	f := newFacade(t, Config{})
	boom := errors.New("not dispatched")
	_, err := f.Executor(&fakeExec{err: boom}).Execute(context.Background(), engine.Invocation{ID: "i", Env: env("j", "r", "t"), Report: engine.NopReporter{}})
	if !errors.Is(err, boom) {
		t.Errorf("error %v", err)
	}
}

// The policy waits for the calls still being answered when the outcome
// arrives, so a slow handler's 501 is counted (until the step's context
// ends).
func TestExecutorWaitsForCallsInFlight(t *testing.T) {
	f := newFacade(t, Config{})
	release := make(chan struct{})
	f.Handle("getJob", func(ctx context.Context, c *Call) Reply {
		<-release
		return text(501, UnsupportedPrefix+c.Route.Name())
	})
	x := &fakeExec{out: engine.StepOutcome{Kind: engine.OutcomeOK, Body: jsonval.FromProducer([]byte(`1`))}}
	x.do = func(inv engine.Invocation) {
		serve := f.Platform()(harness.PlatformRequest{ID: "p", Inv: inv.ID, Method: "GET", URL: f.WarmOrigin() + "/api/w/ws/jobs_u/get/j1"})
		go serve(context.Background())
		time.AfterFunc(50*time.Millisecond, func() { close(release) })
	}
	out, err := f.Executor(x).Execute(context.Background(), engine.Invocation{ID: "i", Env: env("j", "r", "t"), Report: engine.NopReporter{}})
	if err != nil || out.Kind != engine.OutcomeError || !strings.Contains(string(out.Body.Producer()), "GET /w/{ws}/jobs_u/get/{id}") {
		t.Fatalf("%+v %s %v", out, out.Body.Producer(), err)
	}
}

// WA-42/a: the status and body of each error variant, AIError and
// MetricNotFound among them.
func TestErrorVariants(t *testing.T) {
	for _, c := range []struct {
		e      Error
		status int
		body   string
	}{
		{Error{Variant: "NotFound", Message: "Variable u/x/v not found"}, 404, "Not found: Variable u/x/v not found"},
		{Error{Variant: "NotAuthorized", Message: "m"}, 401, "Not authorized: m"},
		{Error{Variant: "PermissionDenied", Message: "m"}, 403, "Permission denied: m"},
		{Error{Variant: "RequireAdmin", Message: "m"}, 403, "Require Admin privileges for m"},
		{Error{Variant: "BadRequest", Message: "m"}, 400, "Bad request: m"},
		{Error{Variant: "QuotaExceeded", Message: "m"}, 400, "Quota exceeded: m"},
		{Error{Variant: "AIError", Message: "x"}, 400, "x"},
		{Error{Variant: "BadGateway", Message: "m"}, 502, "Bad gateway: m"},
		{Error{Variant: "Generic", Status: 418, Message: "m"}, 418, "m"},
		{Error{Variant: "MetricNotFound", Message: "m"}, 500, "Metric not found: m"},
		{Error{Variant: "ExecutionErr", Message: "timeout after 600s"}, 500, "timeout after 600s"},
		{Error{Variant: "InternalErr", Message: "m"}, 500, "Internal: m"},
		{Error{Variant: "ArgumentErr", Message: "m"}, 500, "Problem with arguments: m"},
		{Error{Variant: "WacSuspended", Message: "m"}, 500, "WAC job suspended: m"},
	} {
		r := c.e.Reply()
		if r.Status != c.status || string(r.Body) != c.body || r.Headers["content-type"] != "text/plain" {
			t.Errorf("%s: %d %q, want %d %q", c.e.Variant, r.Status, r.Body, c.status, c.body)
		}
	}
}

// A handler registered by operationId answers its route; registering an
// operation the API does not have is a programming error.
func TestHandle(t *testing.T) {
	f := newFacade(t, Config{})
	f.Handle("getVariableValue", func(ctx context.Context, c *Call) Reply {
		b, _ := json.Marshal(c.Caller.ID + ":" + c.Params["path"])
		return Reply{Status: 200, Headers: map[string]string{"content-type": "application/json"}, Body: b}
	})
	f.Register("inv-a", "run-a", env("job-a", "root-a", "tok-a"))
	if code, body := get(t, f.FreshOrigin()+"/api/w/ws/variables/get_value/u/x/v", "tok-a"); code != 200 || body != `"inv-a:u/x/v"` {
		t.Errorf("%d %s", code, body)
	}
	defer func() {
		if recover() == nil {
			t.Error("no panic for an unknown operation")
		}
	}()
	f.Handle("noSuchOperation", nil)
}

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// hasNotStarted reports whether err carries the RUN_NOT_STARTED detail.
func hasNotStarted(err error) bool {
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == plugin.ErrorDomain && info.GetReason() == plugin.ReasonRunNotStarted {
			return true
		}
	}
	return false
}

// health is the Gate's answer to a health Check of the Runner service.
func health(t *testing.T, g *Gate) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	res, err := g.unary(context.Background(), &healthpb.HealthCheckRequest{Service: plugin.Runner_ServiceDesc.ServiceName}, &grpc.UnaryServerInfo{FullMethod: healthpb.Health_Check_FullMethodName}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.(*healthpb.HealthCheckResponse).GetStatus()
}

// tapRunner is a fake transport of Runner calls in front of the Gate: it
// records each run's token and each lookup's answer, and its hooks may
// hold or change calls.
type tapRunner struct {
	plugin.RunnerServer
	run    func(ctx context.Context, req *plugin.RunRequest) (*plugin.RunResult, error, bool)
	lookup func(ctx context.Context, req *plugin.GetRunRequest) (*plugin.GetRunResponse, error, bool)

	mu       sync.Mutex
	tokens   map[string]string // run id → token
	lookups  map[string]int    // token → lookups
	answers  map[string][]error
	states   map[string][]string // token → each lookup's state, "" for an error
	brokers  map[uint32]bool
	brokerOf map[string]uint32 // run id → broker id
	runs     map[string]int    // run id → requests
}

func newTap(s plugin.RunnerServer) *tapRunner {
	return &tapRunner{RunnerServer: s, tokens: map[string]string{}, lookups: map[string]int{}, answers: map[string][]error{}, states: map[string][]string{}, brokers: map[uint32]bool{}, brokerOf: map[string]uint32{}, runs: map[string]int{}}
}

// broker returns the broker id of the run id's request.
func (r *tapRunner) broker(id string) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.brokerOf[id]
}

// runsOf is how many requests of the run id reached the tap.
func (r *tapRunner) runsOf(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id]
}

// tokensSeen returns the tokens of the requests that reached the tap.
func (r *tapRunner) tokensSeen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, tok := range r.tokens {
		out = append(out, tok)
	}
	return out
}

func (r *tapRunner) Run(ctx context.Context, req *plugin.RunRequest) (*plugin.RunResult, error) {
	r.mu.Lock()
	r.runs[req.GetId()]++
	r.mu.Unlock()
	if h := req.GetHooks(); h != nil {
		r.mu.Lock()
		r.tokens[req.GetId()] = h.GetToken()
		r.brokers[h.GetBrokerId()] = true
		r.brokerOf[req.GetId()] = h.GetBrokerId()
		r.mu.Unlock()
	}
	if r.run != nil {
		if res, err, ok := r.run(ctx, req); ok {
			return res, err
		}
	}
	return r.RunnerServer.Run(ctx, req)
}

func (r *tapRunner) GetRun(ctx context.Context, req *plugin.GetRunRequest) (*plugin.GetRunResponse, error) {
	r.mu.Lock()
	r.lookups[req.GetToken()]++
	r.mu.Unlock()
	res, err, ok := (*plugin.GetRunResponse)(nil), error(nil), false
	if r.lookup != nil {
		res, err, ok = r.lookup(ctx, req)
	}
	if !ok {
		res, err = r.RunnerServer.GetRun(ctx, req)
	}
	r.mu.Lock()
	r.answers[req.GetToken()] = append(r.answers[req.GetToken()], err)
	r.states[req.GetToken()] = append(r.states[req.GetToken()], res.GetState())
	r.mu.Unlock()
	return res, err
}

// answersOf returns the errors of the lookups of token, nil for a gone or
// held answer.
func (r *tapRunner) answersOf(token string) []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.answers[token]...)
}

func (r *tapRunner) token(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokens[id]
}

func (r *tapRunner) lookupsOf(token string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookups[token]
}

// tapHooks is a fake transport of the engine's hook and release calls: drop
// reports whether a call is lost on its way (the engine gets an error), and
// copy whether the transport delivers it and keeps a copy for later.
type tapHooks struct {
	plugin.HostHooksClient
	drop  func(*plugin.HookCall) bool
	copy  func(*plugin.HookCall) bool
	alter func(*plugin.HookCall) *plugin.HookCall

	mu      sync.Mutex
	dropped []*plugin.HookCall
	copies  []*plugin.HookCall
}

func (h *tapHooks) Call(ctx context.Context, c *plugin.HookCall, opts ...grpc.CallOption) (*plugin.HookReply, error) {
	if h.drop != nil && h.drop(c) {
		h.mu.Lock()
		h.dropped = append(h.dropped, c)
		h.mu.Unlock()
		return nil, status.Error(codes.Unavailable, "the test's transport lost the call")
	}
	if h.copy != nil && h.copy(c) {
		h.mu.Lock()
		h.copies = append(h.copies, c)
		h.mu.Unlock()
	}
	if h.alter != nil {
		c = h.alter(c)
	}
	return h.HostHooksClient.Call(ctx, c, opts...)
}

func (h *tapHooks) droppedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.dropped)
}

// copied returns the calls that the copy filter kept so far.
func (h *tapHooks) copied() []*plugin.HookCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*plugin.HookCall(nil), h.copies...)
}

// released reports whether c is the engine's release call.
func released(c *plugin.HookCall) bool { return c.GetHook() == releasedHook }

// holdDir returns a directory for the hold flow, and the function that
// releases its run.
func holdDir(t *testing.T) (string, func()) {
	dir := t.TempDir()
	return dir, func() {
		if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o644); err != nil {
			t.Error(err)
		}
	}
}

// waitStarted waits for the hold flow's step in dir to start.
func waitStarted(t *testing.T, dir string) {
	t.Helper()
	waitFor(t, "the held step starts", func() bool {
		_, err := os.Stat(filepath.Join(dir, "started"))
		return err == nil
	})
}

// outcome is a Run call's result.
type outcome struct {
	res *plugin.RunResult
	err error
}

// runAsync runs f in a goroutine and returns the channel of its outcome.
func runAsync(f func() (*plugin.RunResult, error)) <-chan outcome {
	ch := make(chan outcome, 1)
	go func() {
		res, err := f()
		ch <- outcome{res, err}
	}()
	return ch
}

// one is the request of the flow f/broker/one.
func one(id string) *plugin.RunRequest {
	return &plugin.RunRequest{Id: id, Path: "f/broker/one", Args: []byte(`{}`)}
}

// interceptOne declares an interception of f/broker/one's step.
var interceptOne = `{"intercept": [{"selector": {"flow": "f/broker/one", "module": "a"}}]}`

// answerWith answers every interception with result.
func answerWith(result string) func(context.Context, *plugin.HookCall) (*plugin.HookReply, error) {
	return func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		if c.GetHook() == "intercept" {
			return &plugin.HookReply{Payload: []byte(`{"result": ` + result + `}`)}, nil
		}
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}
}

// TestBrokerAroundRefused checks that a run's hooks that declare around
// are refused: around runs only in the harness process. The run does not
// start (INVALID_ARGUMENT, RUN_NOT_STARTED: ErrNotStarted), no call
// reaches the host, and the Client drops the registration at once.
func TestBrokerAroundRefused(t *testing.T) {
	tap := (*tapRunner)(nil)
	e := startEngine(t, engineConfig{runner: func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap }})
	var rec recorder
	decl := `{"around": [{"selector": {"flow": "f/broker/one", "module": "a"}}]}`
	_, err := e.client.RunWithHooks(context.Background(), one(""), []byte(decl), rec.handle)
	if !errors.Is(err, plugin.ErrNotStarted) || !strings.Contains(err.Error(), "around") {
		t.Fatalf("a run whose hooks declare around: %v, want ErrNotStarted naming around", err)
	}
	if n := e.client.Registrations(); n != 0 {
		t.Errorf("%d registrations after the refusal, want 0", n)
	}
	if got := rec.heard(); len(got) != 0 {
		t.Errorf("the host heard %v of a refused run", got)
	}
}

// TestBrokerHookTimeout checks that a run's hook whose host does not answer
// within the plugin timeout latches the run, as a host plugin's does: the
// host holds the interception until the engine gives the call up, and
// answers only once the run has returned, so that only the engine's own
// timeout ends the call.
func TestBrokerHookTimeout(t *testing.T) {
	e := startEngine(t, engineConfig{pluginTimeout: 300 * time.Millisecond})
	gaveUp, returned := make(chan struct{}), make(chan struct{})
	defer close(returned)
	rec := recorder{answer: func(ctx context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		if c.GetHook() == "intercept" {
			<-ctx.Done()
			close(gaveUp)
			<-returned
			return nil, ctx.Err()
		}
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}}
	res, err := e.client.RunWithHooks(context.Background(), one(""), []byte(interceptOne), rec.handle)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "latched" || !strings.Contains(res.GetLatched(), "got no reply from the host plugin within") {
		t.Errorf("a run whose interception got no answer: status %s, latched %q; want latched by the host_call timeout", res.GetStatus(), res.GetLatched())
	}
	select {
	case <-gaveUp:
	case <-time.After(30 * time.Second):
		t.Error("the engine never gave the call up")
	}
}

// TestBrokerProviderMissing checks a named provider that the run's hooks
// answer (a provider set to broker): a run whose hooks serve it gets the
// host's answer at its mandatory site; a run whose hooks do not declare
// it, and a run without hooks, latch there.
func TestBrokerProviderMissing(t *testing.T) {
	e := startEngine(t, engineConfig{})
	req := func() *plugin.RunRequest { return &plugin.RunRequest{Path: "f/broker/prov", Args: []byte(`{}`)} }
	rec := recorder{answer: func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		if c.GetHook() == "provide" {
			return &plugin.HookReply{Payload: []byte(`{"result": "from the host"}`)}, nil
		}
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}}
	res, err := e.client.RunWithHooks(context.Background(), req(), []byte(`{"providers": ["bp"]}`), rec.handle)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "success" || string(res.GetResult()) != `"from the host"` {
		t.Errorf("a run whose hooks serve bp: status %s, result %s; want success, the host's answer", res.GetStatus(), res.GetResult())
	}
	var other recorder
	res, err = e.client.RunWithHooks(context.Background(), req(), []byte(`{"on_run_end": true}`), other.handle)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "latched" || !strings.Contains(res.GetLatched(), `"bp"`) {
		t.Errorf("a run whose hooks do not serve bp: status %s, latched %q; want latched, naming bp", res.GetStatus(), res.GetLatched())
	}
	res, err = e.client.Run(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "latched" || !strings.Contains(res.GetLatched(), `"bp"`) {
		t.Errorf("a run without hooks: status %s, latched %q; want latched, naming bp", res.GetStatus(), res.GetLatched())
	}
}

// TestBrokerPhaseFailures checks what a failing hook of a run's hooks does
// in each phase, as a host plugin's: a failing onRunEnd is a warning and
// leaves the status alone; a failing onRunSettled is a diagnostic; a
// failing observer latches the run.
func TestBrokerPhaseFailures(t *testing.T) {
	var diag syncBuffer
	e := startEngine(t, engineConfig{diagnostics: &diag})
	failing := func(hook string) recorder {
		return recorder{answer: func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
			if c.GetHook() == hook {
				return nil, fmt.Errorf("the host's %s failed", hook)
			}
			return &plugin.HookReply{Payload: []byte(`{}`)}, nil
		}}
	}

	end := failing("on_run_end")
	res, err := e.client.RunWithHooks(context.Background(), one(""), []byte(`{"on_run_end": true}`), end.handle)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "success" || !strings.Contains(string(res.GetWarnings()), "the host's on_run_end failed") {
		t.Errorf("a failing onRunEnd: status %s, warnings %s; want success with its warning", res.GetStatus(), res.GetWarnings())
	}

	settled := failing("on_run_settled")
	res, err = e.client.RunWithHooks(context.Background(), one(""), []byte(`{"on_run_settled": true}`), settled.handle)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "success" {
		t.Errorf("a failing onRunSettled: status %s, want success", res.GetStatus())
	}
	waitFor(t, "the failing onRunSettled's diagnostic", func() bool { return strings.Contains(diag.String(), "the host's on_run_settled failed") })

	observer := failing("on_module_start")
	res, err = e.client.RunWithHooks(context.Background(), one(""), []byte(`{"on_module_start": true}`), observer.handle)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "latched" || !strings.Contains(res.GetLatched(), "the host's on_module_start failed") {
		t.Errorf("a failing observer: status %s, latched %q; want latched by its error", res.GetStatus(), res.GetLatched())
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// orderPlugin is a host plugin that intercepts f/broker/order's step a and
// answers the resource u/broker/r, both with "ts"; with provider set, it
// declares the named provider bp too.
func orderPlugin(t *testing.T, provider bool) string {
	src := `export default {
  intercept: [{ selector: { flow: "f/broker/order", module: "a" }, fn: () => ({ result: "ts" }) }],
  resource: (path: string) => (path === "u/broker/r" ? { type: "object", value: "ts" } : undefined),
`
	if provider {
		src += "  providers: { bp: () => ({ result: \"ts\" }) },\n"
	}
	src += "};\n"
	p := filepath.Join(t.TempDir(), "plugin.ts")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestBrokerOrderWithPlugin checks that a run's hooks are its own, ahead
// of the runtime's host plugin: where both intercept a step and answer a
// resource, the run's hooks answer; a run without hooks gets the plugin's
// answers. A named provider set to broker that the plugin also declares
// refuses the runtime's start.
func TestBrokerOrderWithPlugin(t *testing.T) {
	e := startEngine(t, engineConfig{plugin: orderPlugin(t, false)})
	req := func() *plugin.RunRequest { return &plugin.RunRequest{Path: "f/broker/order", Args: []byte(`{}`)} }
	rec := recorder{answer: func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		switch c.GetHook() {
		case "intercept":
			return &plugin.HookReply{Payload: []byte(`{"result": "broker"}`)}, nil
		case "resource":
			return &plugin.HookReply{Payload: []byte(`{"found": true, "type": "object", "value": "broker"}`)}, nil
		}
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}}
	decl := `{"intercept": [{"selector": {"flow": "f/broker/order", "module": "a"}}], "resource": true}`
	res, err := e.client.RunWithHooks(context.Background(), req(), []byte(decl), rec.handle)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "success" || !sameJSON(res.GetResult(), `{"prev":"broker","res":"broker"}`) {
		t.Errorf("a run whose hooks and the plugin both answer: status %s (%s), result %s; want the hooks' answers", res.GetStatus(), res.GetLatched(), res.GetResult())
	}
	res, err = e.client.Run(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStatus() != "success" || !sameJSON(res.GetResult(), `{"prev":"ts","res":"ts"}`) {
		t.Errorf("a run without hooks: status %s (%s), result %s; want the plugin's answers", res.GetStatus(), res.GetLatched(), res.GetResult())
	}

	if rt, err := openRuntime(t, engineConfig{plugin: orderPlugin(t, true)}); err == nil {
		rt.Close()
		t.Error("a runtime whose broker provider bp the plugin declares too started")
	} else if !strings.Contains(err.Error(), "bp") {
		t.Errorf("the refused start: %v, want it to name bp", err)
	}
}

// sameJSON reports whether got is the JSON value want.
func sameJSON(got []byte, want string) bool {
	var a, b any
	return json.Unmarshal(got, &a) == nil && json.Unmarshal([]byte(want), &b) == nil && reflect.DeepEqual(a, b)
}

// TestBrokerLateCallback checks that a run's onRunSettled reaches its host
// after its Run call has returned, through the same connection, and is
// served by the run's handler: Cancel ends the run once its step has
// started, and the step, which runs on, holds the run's settlement until
// the test releases it. The registration is dropped only after.
func TestBrokerLateCallback(t *testing.T) {
	e := startEngine(t, engineConfig{lookupAfter: time.Hour})
	arrived := make(chan string, 1)
	rec := recorder{answer: func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		if c.GetHook() == "on_run_settled" {
			arrived <- c.GetRun()
		}
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}}
	dir, release := holdDir(t)
	done := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "late", Path: "f/broker/hold", Args: jsonOf(t, map[string]string{"dir": dir})}, []byte(`{"on_run_settled": true}`), rec.handle)
	})
	waitStarted(t, dir)
	if _, err := e.client.Cancel(context.Background(), &plugin.CancelRequest{Id: "late", Reason: "the test", Canceler: "test"}); err != nil {
		t.Fatal(err)
	}
	var o outcome
	select {
	case o = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the canceled run did not return while its step ran on")
	}
	if o.err != nil || o.res.GetStatus() != "canceled" {
		t.Fatalf("the canceled run: %v, %v; want canceled", o.res, o.err)
	}
	if got := rec.heard(); len(got) != 0 {
		t.Fatalf("the host heard %v before the run settled", got)
	}
	if n := e.client.Registrations(); n != 1 {
		t.Errorf("%d registrations before the run settled, want 1", n)
	}
	release()
	select {
	case run := <-arrived:
		if run != "late" {
			t.Errorf("onRunSettled of run %s, want late", run)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("onRunSettled never reached the host")
	}
	waitFor(t, "the release after onRunSettled", func() bool { return e.client.Registrations() == 0 })
}

// TestBrokerIsolationNextRun checks that a run's hooks are its own: the
// next run, with other hooks or none, never reaches them. A test transport
// records each hook call that the engine sends, since a call that reached
// a released run would only be answered unknown_run, which is no answer.
func TestBrokerIsolationNextRun(t *testing.T) {
	hooks := &tapHooks{copy: func(c *plugin.HookCall) bool { return !released(c) }}
	e := startEngine(t, engineConfig{hooks: func(c plugin.HostHooksClient) plugin.HostHooksClient { hooks.HostHooksClient = c; return hooks }})
	first := recorder{answer: answerWith(`"first"`)}
	res, err := e.client.RunWithHooks(context.Background(), one(""), []byte(interceptOne), first.handle)
	if err != nil || string(res.GetResult()) != `"first"` {
		t.Fatalf("the first run: %v, %v", res, err)
	}
	sent := hooks.copied()
	if len(sent) == 0 {
		t.Fatal("the first run sent no hook call")
	}
	token := sent[0].GetToken()
	heard := len(first.heard())
	res, err = e.client.Run(context.Background(), one(""))
	if err != nil || string(res.GetResult()) != `"one"` {
		t.Errorf("the next run without hooks: %v, %v; want its step's own result", res, err)
	}
	if after := hooks.copied(); len(after) != len(sent) {
		t.Errorf("the next run without hooks sent %d hook calls, want none", len(after)-len(sent))
	}
	n := len(hooks.copied())
	var second recorder
	res, err = e.client.RunWithHooks(context.Background(), one(""), []byte(`{"on_module_start": true}`), second.handle)
	if err != nil || string(res.GetResult()) != `"one"` {
		t.Errorf("the next run with an observer: %v, %v; want its step's own result", res, err)
	}
	if got := set(second.heard()); len(got) != 1 || got[0] != "on_module_start@m:a" {
		t.Errorf("the second run's host heard %v, want its observer alone", got)
	}
	for _, c := range hooks.copied()[n:] {
		if c.GetToken() == token {
			t.Errorf("the next run with an observer sent %s with the first run's token", c.GetHook())
		}
	}
	if more := len(first.heard()) - heard; more != 0 {
		t.Errorf("the first run's host heard %d more calls after its run", more)
	}
}

// openFiles counts the process's open descriptors, its listeners among
// them.
func openFiles(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip("no /dev/fd: ", err)
	}
	return len(ents)
}

// TestBrokerNoListenerGrowth checks that the runs' hooks share one broker
// connection: 1,000 completed runs with hooks leave the process's open
// descriptors and goroutines where they were, use one broker id, and leave
// no registration.
func TestBrokerNoListenerGrowth(t *testing.T) {
	tap := (*tapRunner)(nil)
	e := startEngine(t, engineConfig{lookupAfter: time.Hour, runner: func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap }})
	var rec recorder
	run := func() {
		res, err := e.client.RunWithHooks(context.Background(), one(""), []byte(`{"on_run_end": true}`), rec.handle)
		if err != nil || res.GetStatus() != "success" {
			t.Fatalf("a run: %v, %v", res, err)
		}
	}
	for range 20 {
		run()
	}
	waitFor(t, "the warm-up's registrations dropped", func() bool { return e.client.Registrations() == 0 })
	files, routines := openFiles(t), runtime.NumGoroutine()
	for range 1000 {
		run()
	}
	waitFor(t, "every registration dropped", func() bool { return e.client.Registrations() == 0 })
	var after int
	waitFor(t, "the goroutines back to where they were", func() bool {
		after = runtime.NumGoroutine()
		return after <= routines+5
	})
	if f := openFiles(t); f > files+5 {
		t.Errorf("open descriptors %d after 1,000 runs, %d before", f, files)
	}
	tap.mu.Lock()
	brokers := len(tap.brokers)
	tap.mu.Unlock()
	if brokers != 1 {
		t.Errorf("the runs used %d broker ids, want 1", brokers)
	}
	if conns := e.srv.hooks.dialled(); conns != 1 {
		t.Errorf("the engine dialled %d broker connections, want 1", conns)
	}
}

// TestBrokerRegistrations checks the Client's registrations: a run with
// a provider alone (no onRunSettled) is dropped at the engine's release
// call, with no lookup; a run without an id gets one, to which its calls
// go; a refused run is dropped at once; 1,000 such runs leave none; an id
// still registered is refused, and one released can be used again.
func TestBrokerRegistrations(t *testing.T) {
	tap := (*tapRunner)(nil)
	e := startEngine(t, engineConfig{lookupAfter: time.Hour, runner: func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap }})
	prov := recorder{answer: func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		return &plugin.HookReply{Payload: []byte(`{"result": "from the host"}`)}, nil
	}}
	res, err := e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Path: "f/broker/prov", Args: []byte(`{}`)}, []byte(`{"providers": ["bp"]}`), prov.handle)
	if err != nil || res.GetStatus() != "success" {
		t.Fatalf("the provider's run: %v, %v", res, err)
	}
	if id := res.GetId(); id == "" || !prov.runs[id] || len(prov.runs) != 1 {
		t.Errorf("a run without an id: result id %q, calls of runs %v; want one id, to which the calls went", id, prov.runs)
	}
	waitFor(t, "the release drops the provider's run", func() bool { return e.client.Registrations() == 0 })
	if n := tap.lookupsOf(tap.token(res.GetId())); n != 0 {
		t.Errorf("%d lookups of the released run, want 0", n)
	}

	var rec recorder
	for i := range 1000 {
		if i%2 == 0 {
			before := e.client.Registrations()
			_, err := e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Path: "f/broker/missing"}, []byte(interceptOne), rec.handle)
			if !errors.Is(err, plugin.ErrNotStarted) {
				t.Fatalf("a run of no flow: %v, want ErrNotStarted", err)
			}
			if n := e.client.Registrations(); n > before {
				t.Fatalf("%d registrations after a refused run, %d before it; want it dropped at once", n, before)
			}
			continue
		}
		res, err := e.client.RunWithHooks(context.Background(), one(""), []byte(`{"on_run_end": true}`), rec.handle)
		if err != nil || res.GetStatus() != "success" {
			t.Fatalf("run %d: %v, %v", i, res, err)
		}
	}
	waitFor(t, "every registration of 1,000 runs dropped", func() bool { return e.client.Registrations() == 0 })

	dir, release := holdDir(t)
	held := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "reused", Path: "f/broker/hold", Args: jsonOf(t, map[string]string{"dir": dir})}, []byte(`{"on_run_end": true}`), rec.handle)
	})
	waitStarted(t, dir)
	if _, err := e.client.RunWithHooks(context.Background(), one("reused"), []byte(`{"on_run_end": true}`), rec.handle); !errors.Is(err, plugin.ErrNotStarted) {
		t.Errorf("a run with an id still registered: %v, want ErrNotStarted", err)
	}
	if n := e.client.Registrations(); n != 1 {
		t.Errorf("%d registrations after the refused reuse, want the held run's", n)
	}
	release()
	if o := <-held; o.err != nil || o.res.GetStatus() != "success" {
		t.Fatalf("the held run: %v, %v", o.res, o.err)
	}
	waitFor(t, "the held run's registration dropped", func() bool { return e.client.Registrations() == 0 })
	if res, err := e.client.RunWithHooks(context.Background(), one("reused"), []byte(`{"on_run_end": true}`), rec.handle); err != nil || res.GetStatus() != "success" {
		t.Errorf("a run with a released id: %v, %v; want success", res, err)
	}
}

// lostEngine starts an engine for one case of TestBrokerLostMessages, with
// fake transports that the case fixes before the engine starts: run and
// lookup on the Runner tap, drop, copy and alter on the HostHooks tap.
func lostEngine(t *testing.T, lookupAfter time.Duration, taps func(*tapRunner, *tapHooks)) (*testEngine, *tapRunner, *tapHooks) {
	var tap *tapRunner
	hooks := &tapHooks{}
	taps(nil, hooks)
	e := startEngine(t, engineConfig{
		lookupAfter: lookupAfter,
		runner: func(s plugin.RunnerServer) plugin.RunnerServer {
			tap = newTap(s)
			taps(tap, nil)
			return tap
		},
		hooks: func(c plugin.HostHooksClient) plugin.HostHooksClient { hooks.HostHooksClient = c; return hooks },
	})
	return e, tap, hooks
}

// firstReleaseOf returns a drop of the first release call of the run id.
func firstReleaseOf(id string) func(*plugin.HookCall) bool {
	var dropped atomic.Bool
	return func(c *plugin.HookCall) bool {
		return released(c) && c.GetRun() == id && dropped.CompareAndSwap(false, true)
	}
}

// TestBrokerLostMessages checks reconciliation when messages are lost,
// the engine alive throughout, each case on an engine of its own with its
// transports fixed: (a) the reply of a refusal lost: the call ends
// ErrOutcomeUnknown, the lookup answers gone, and the registration is
// dropped; (b) a release call lost: the lookup drops the registration; (c)
// a delayed hook call of an earlier run, arriving once its id is reused,
// is answered unknown_run and never reaches the new run's hooks; (d) a
// run without a deadline whose release call is lost and whose first
// lookup fails: the retry drops it, the caller's success stays, and the id
// is used again; (e) a run's request delayed past a gone answer for its
// token is refused with RUN_NOT_STARTED, and no call reaches the host; (f)
// while a lost release keeps a run's registration, a plain Run of its id,
// which the engine has freed, is refused.
func TestBrokerLostMessages(t *testing.T) {
	t.Run("a: lost refusal", func(t *testing.T) {
		e, tap, _ := lostEngine(t, 10*time.Millisecond, func(r *tapRunner, _ *tapHooks) {
			if r == nil {
				return
			}
			r.run = func(ctx context.Context, req *plugin.RunRequest) (*plugin.RunResult, error, bool) {
				if req.GetId() != "lost-refusal" {
					return nil, nil, false
				}
				_, err := r.RunnerServer.Run(ctx, req)
				if !hasNotStarted(err) {
					t.Errorf("the engine's answer: %v, want its refusal", err)
				}
				return nil, status.Error(codes.Unavailable, "the test's transport lost the reply"), true
			}
		})
		var rec recorder
		_, err := e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "lost-refusal", Path: "f/broker/missing"}, []byte(interceptOne), rec.handle)
		if !errors.Is(err, plugin.ErrOutcomeUnknown) {
			t.Fatalf("a refusal whose reply was lost: %v, want ErrOutcomeUnknown", err)
		}
		waitFor(t, "the lookup drops the registration", func() bool { return e.client.Registrations() == 0 })
		if n := tap.lookupsOf(tap.token("lost-refusal")); n == 0 {
			t.Error("the registration was dropped without a lookup")
		}
		if !errors.Is(err, plugin.ErrOutcomeUnknown) {
			t.Errorf("the caller's error became %v", err)
		}
	})

	t.Run("b: lost release", func(t *testing.T) {
		e, tap, hooks := lostEngine(t, 10*time.Millisecond, func(_ *tapRunner, h *tapHooks) {
			if h != nil {
				h.drop = firstReleaseOf("lost-release")
			}
		})
		var rec recorder
		res, err := e.client.RunWithHooks(context.Background(), one("lost-release"), []byte(`{"on_run_end": true}`), rec.handle)
		if err != nil || res.GetStatus() != "success" {
			t.Fatalf("the run: %v, %v", res, err)
		}
		waitFor(t, "the lookup drops the registration", func() bool { return e.client.Registrations() == 0 })
		if hooks.droppedCount() == 0 {
			t.Error("no release call was lost")
		}
		if n := tap.lookupsOf(tap.token("lost-release")); n == 0 {
			t.Error("the registration was dropped without a lookup")
		}
	})

	t.Run("c: delayed call", func(t *testing.T) {
		e, _, hooks := lostEngine(t, 10*time.Millisecond, func(_ *tapRunner, h *tapHooks) {
			if h != nil {
				h.copy = func(c *plugin.HookCall) bool { return c.GetRun() == "reused-c" && c.GetHook() == "on_module_end" }
			}
		})
		var first recorder
		res, err := e.client.RunWithHooks(context.Background(), one("reused-c"), []byte(`{"on_module_end": true}`), first.handle)
		if err != nil || res.GetStatus() != "success" {
			t.Fatalf("the first run: %v, %v", res, err)
		}
		waitFor(t, "the first run's registration dropped", func() bool { return e.client.Registrations() == 0 })
		hooks.mu.Lock()
		delayed := hooks.copies[0]
		hooks.mu.Unlock()
		dir, release := holdDir(t)
		var second recorder
		run := runAsync(func() (*plugin.RunResult, error) {
			return e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "reused-c", Path: "f/broker/hold", Args: jsonOf(t, map[string]string{"dir": dir})}, []byte(`{"on_module_end": true}`), second.handle)
		})
		waitStarted(t, dir)
		rep, err := hooks.HostHooksClient.Call(context.Background(), delayed)
		if err != nil || rep.GetError().GetCode() != plugin.HookCodeUnknownRun {
			t.Errorf("the delayed call of the first run: %v, %v; want the error unknown_run", rep, err)
		}
		if got := second.heard(); len(got) != 0 {
			t.Errorf("the second run's hooks heard %v before its step ended", got)
		}
		if n := e.client.Registrations(); n != 1 {
			t.Errorf("%d registrations after the delayed call, want the second run's", n)
		}
		release()
		if o := <-run; o.err != nil || o.res.GetStatus() != "success" {
			t.Fatalf("the second run: %v, %v", o.res, o.err)
		}
		if got := set(second.heard()); len(got) != 1 || got[0] != "on_module_end@m:a" {
			t.Errorf("the second run's hooks heard %v, want its own observer", got)
		}
	})

	t.Run("d: no deadline, a failed lookup", func(t *testing.T) {
		var failed atomic.Bool
		e, tap, _ := lostEngine(t, 10*time.Millisecond, func(r *tapRunner, h *tapHooks) {
			if h != nil {
				h.drop = firstReleaseOf("no-deadline")
			}
			if r != nil {
				r.lookup = func(_ context.Context, req *plugin.GetRunRequest) (*plugin.GetRunResponse, error, bool) {
					if req.GetId() == "no-deadline" && failed.CompareAndSwap(false, true) {
						return nil, status.Error(codes.Unavailable, "the test's transport failed the lookup"), true
					}
					return nil, nil, false
				}
			}
		})
		var rec recorder
		res, err := e.client.RunWithHooks(context.Background(), one("no-deadline"), []byte(`{"on_run_end": true}`), rec.handle)
		if err != nil || res.GetStatus() != "success" {
			t.Fatalf("the run: %v, %v", res, err)
		}
		token := tap.token("no-deadline")
		waitFor(t, "the retried lookup drops the registration", func() bool { return e.client.Registrations() == 0 })
		if n := tap.lookupsOf(token); n < 2 || !failed.Load() {
			t.Errorf("%d lookups, the first failed: %v; want the failed one and its retry", n, failed.Load())
		}
		if res.GetStatus() != "success" {
			t.Errorf("the caller's result became %s", res.GetStatus())
		}
		next := recorder{answer: answerWith(`"again"`)}
		again, err := e.client.RunWithHooks(context.Background(), one("no-deadline"), []byte(interceptOne), next.handle)
		if err != nil || string(again.GetResult()) != `"again"` {
			t.Errorf("the id used again: %v, %v", again, err)
		}
		if tap.token("no-deadline") == token {
			t.Error("the id used again kept its old token")
		}
	})

	t.Run("e: request delayed past gone", func(t *testing.T) {
		delayed := make(chan *plugin.RunRequest, 1)
		e, tap, _ := lostEngine(t, 10*time.Millisecond, func(r *tapRunner, _ *tapHooks) {
			if r == nil {
				return
			}
			r.run = func(_ context.Context, req *plugin.RunRequest) (*plugin.RunResult, error, bool) {
				if req.GetId() != "delayed" {
					return nil, nil, false
				}
				delayed <- req
				return nil, status.Error(codes.Unavailable, "the test's transport holds the request"), true
			}
		})
		var rec recorder
		_, err := e.client.RunWithHooks(context.Background(), one("delayed"), []byte(interceptOne), rec.handle)
		if !errors.Is(err, plugin.ErrOutcomeUnknown) {
			t.Fatalf("a request in transit: %v, want ErrOutcomeUnknown", err)
		}
		waitFor(t, "the gone answer drops the registration", func() bool { return e.client.Registrations() == 0 })
		_, err = tap.RunnerServer.Run(context.Background(), <-delayed)
		if status.Code(err) == codes.OK || !hasNotStarted(err) {
			t.Errorf("the delayed request: %v, want a refusal with RUN_NOT_STARTED", err)
		}
		if got := rec.heard(); len(got) != 0 {
			t.Errorf("the host heard %v of the delayed request", got)
		}
	})

	t.Run("f: plain run of a held id", func(t *testing.T) {
		e, tap, hooks := lostEngine(t, time.Hour, func(r *tapRunner, h *tapHooks) {
			if h != nil {
				h.drop = firstReleaseOf("kept")
			}
		})
		var rec recorder
		res, err := e.client.RunWithHooks(context.Background(), one("kept"), []byte(`{"on_run_end": true}`), rec.handle)
		if err != nil || res.GetStatus() != "success" {
			t.Fatalf("the run: %v, %v", res, err)
		}
		waitFor(t, "the release call lost", func() bool { return hooks.droppedCount() == 1 })
		if n := e.client.Registrations(); n != 1 {
			t.Fatalf("%d registrations after the lost release, want 1", n)
		}
		before := len(tap.tokensSeen())
		if _, err := e.client.Run(context.Background(), one("kept")); !errors.Is(err, plugin.ErrNotStarted) {
			t.Errorf("a plain run of an id still registered: %v, want ErrNotStarted", err)
		}
		if _, err := e.client.RunWithHooks(context.Background(), one("kept"), []byte(interceptOne), rec.handle); !errors.Is(err, plugin.ErrNotStarted) {
			t.Errorf("a run with hooks of an id still registered: %v, want ErrNotStarted", err)
		}
		if n := len(tap.tokensSeen()); n != before || tap.runsOf("kept") != 1 {
			t.Errorf("a refused submission reached the engine: %d requests of the id", tap.runsOf("kept"))
		}
	})
}

// TestBrokerTokenBound checks the engine's bound of gone tokens, lowered
// to 8, and the Client's answer to it: a run held in flight keeps the
// draining engine up. Nine runs whose release calls are lost. The test
// holds each one's first lookup until the engine has let all nine tokens
// go, through a transport that keeps a lookup's deadline off the wire, so
// that a held lookup still answers its Client. Let go, eight lookups
// answer gone, which makes the engine keep 8 tokens, turn health
// NOT_SERVING and drain, and the Client drops their eight registrations.
// While it drains, (a) the ninth run's lookups answer UNAVAILABLE without
// RUN_NOT_STARTED, also from the Server behind the Gate, and the Client
// keeps its registration; (b) a new run, with a fresh token or an old
// one, is refused with RUN_NOT_STARTED, and no call reaches the host; (c)
// the engine keeps 8 tokens throughout. Released, the held run succeeds,
// the generation's end drops the ninth registration, and every caller's
// result stays success.
func TestBrokerTokenBound(t *testing.T) {
	brokerBundle(t)
	dir, err := os.MkdirTemp("", "wlt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// go-plugin makes the test connection's socket in os.TempDir.
	t.Setenv("TMPDIR", dir)
	var tap *tapRunner
	hooks := &tapHooks{drop: released}
	lookups := make(chan struct{})
	e := startEngine(t, engineConfig{
		tokenBound:  8,
		lookupAfter: 10 * time.Millisecond,
		runnerConn: func() *grpc.ClientConn {
			socks, err := socketsIn(dir)
			if err != nil || len(socks) != 1 {
				t.Fatalf("the test connection's socket: %v, %v", socks, err)
			}
			noDeadline := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				if method == plugin.Runner_GetRun_FullMethodName {
					ctx = context.WithoutCancel(ctx)
				}
				return invoker(ctx, method, req, reply, cc, opts...)
			}
			cc, err := grpc.NewClient("unix://"+socks[0], grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithChainUnaryInterceptor(noDeadline))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cc.Close() })
			return cc
		},
		runner: func(s plugin.RunnerServer) plugin.RunnerServer {
			tap = newTap(s)
			tap.lookup = func(context.Context, *plugin.GetRunRequest) (*plugin.GetRunResponse, error, bool) {
				<-lookups
				return nil, nil, false
			}
			return tap
		},
		hooks: func(c plugin.HostHooksClient) plugin.HostHooksClient { hooks.HostHooksClient = c; return hooks },
	})
	dirHeld, release := holdDir(t)
	held := runAsync(func() (*plugin.RunResult, error) {
		return e.client.Run(context.Background(), &plugin.RunRequest{Path: "f/broker/hold", Args: jsonOf(t, map[string]string{"dir": dirHeld})})
	})
	waitStarted(t, dirHeld)

	var rec recorder
	ids := make([]string, 9)
	var results []*plugin.RunResult
	for i := range ids {
		ids[i] = fmt.Sprintf("bound-%d", i)
		res, err := e.client.RunWithHooks(context.Background(), one(ids[i]), []byte(`{"on_run_end": true}`), rec.handle)
		if err != nil || res.GetStatus() != "success" {
			t.Fatalf("run %d: %v, %v", i, res, err)
		}
		results = append(results, res)
	}
	waitFor(t, "the nine release calls lost, their tokens let go, and a lookup of each held", func() bool {
		if hooks.droppedCount() != 9 || e.srv.hooks.heldTokens() != 0 {
			return false
		}
		for _, id := range ids {
			if tap.lookupsOf(tap.token(id)) == 0 {
				return false
			}
		}
		return true
	})
	close(lookups)
	waitFor(t, "the nine held lookups answered", func() bool {
		for _, id := range ids {
			if len(tap.answersOf(tap.token(id))) == 0 {
				return false
			}
		}
		return true
	})
	var gone []string
	var last string
	for _, id := range ids {
		state, err := tap.stateOf(tap.token(id), 0)
		switch {
		case err == nil && state == "gone":
			gone = append(gone, id)
		case status.Code(err) == codes.Unavailable && !hasNotStarted(err):
			last = id
		default:
			t.Errorf("the held lookup of %s: %q, %v; want gone, or UNAVAILABLE without RUN_NOT_STARTED", id, state, err)
		}
	}
	if len(gone) != 8 || last == "" {
		t.Fatalf("%d held lookups answered gone and the UNAVAILABLE one is %q; want 8, and the ninth", len(gone), last)
	}
	waitFor(t, "the Client drops the eight registrations", func() bool { return e.client.Registrations() == 1 })
	for _, id := range gone {
		if n := tap.lookupsOf(tap.token(id)); n != 1 {
			t.Errorf("%s was looked up %d times, want once", id, n)
		}
	}
	if n := e.srv.hooks.goneTokens(); n != 8 {
		t.Errorf("the engine keeps %d tokens, want 8", n)
	}
	if s := health(t, e.gate); s != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("health %v at the bound, want NOT_SERVING", s)
	}
	if _, err := e.srv.GetRun(context.Background(), &plugin.GetRunRequest{Id: last, Token: tap.token(last)}); status.Code(err) != codes.Unavailable || hasNotStarted(err) {
		t.Errorf("the Server's own lookup of the ninth run: %v, want UNAVAILABLE without RUN_NOT_STARTED", err)
	}
	waitFor(t, "the Client's next lookup of the ninth run", func() bool { return len(tap.answersOf(tap.token(last))) >= 2 })
	for i, err := range tap.answersOf(tap.token(last)) {
		if status.Code(err) != codes.Unavailable || hasNotStarted(err) {
			t.Errorf("lookup %d of the ninth run while draining: %v, want UNAVAILABLE without RUN_NOT_STARTED", i, err)
		}
	}
	if n := e.client.Registrations(); n != 1 {
		t.Errorf("%d registrations while draining, want the ninth run's", n)
	}

	heard := len(rec.heard())
	if _, err := e.client.RunWithHooks(context.Background(), one("fresh"), []byte(interceptOne), rec.handle); !errors.Is(err, plugin.ErrNotStarted) {
		t.Errorf("a new run while draining: %v, want ErrNotStarted", err)
	}
	old := tap.token("bound-0")
	_, err = e.gate.Runner().Run(context.Background(), &plugin.RunRequest{Id: "old", Path: "f/broker/one", Hooks: &plugin.Hooks{Token: old, Declaration: []byte(interceptOne)}})
	if !hasNotStarted(err) {
		t.Errorf("a run with an old token while draining: %v, want RUN_NOT_STARTED", err)
	}
	if n := len(rec.heard()); n != heard {
		t.Errorf("the host heard %d calls of refused runs", n-heard)
	}
	if n := e.srv.hooks.goneTokens(); n != 8 {
		t.Errorf("the engine keeps %d tokens while draining, want 8", n)
	}

	release()
	if o := <-held; o.err != nil || o.res.GetStatus() != "success" {
		t.Fatalf("the held run: %v, %v", o.res, o.err)
	}
	// The engine exits once it has drained: go-plugin ends the
	// generation, and the Client drops the ninth run's registration.
	e.endGeneration()
	waitFor(t, "the generation's end drops the ninth registration", func() bool { return e.client.Registrations() == 0 })
	for i, res := range results {
		if res.GetStatus() != "success" {
			t.Errorf("run %d's result became %s", i, res.GetStatus())
		}
	}
	if n := e.srv.hooks.goneTokens(); n != 8 {
		t.Errorf("the engine keeps %d tokens at the end, want 8", n)
	}
}

// stateOf returns the state and error of lookup i of token.
func (r *tapRunner) stateOf(token string, i int) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.states[token][i], r.answers[token][i]
}

// noWarnings reports whether a run's warnings, as JSON, hold none.
func noWarnings(w []byte) bool {
	var ws []json.RawMessage
	return len(bytes.TrimSpace(w)) == 0 || (json.Unmarshal(w, &ws) == nil && len(ws) == 0)
}

// heldTokens is how many tokens the engine holds.
func (b *brokerHooks) heldTokens() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.held)
}

// TestBrokerStaleCalls checks the engine's outcome of a call that the
// host answers unknown_run, the test's transport giving the stale runs'
// calls a stale token: no answer, which latches nothing. A stale observer
// and a stale interception leave the run to succeed with its step's own
// result; a stale provider declines, so its mandatory site still fails
// closed; a stale first-write verdict lets the write go ahead, as a run
// without that verdict. A registered run's handler that replies with the
// code unknown_run fails its verdict, which latches the run before the
// write.
func TestBrokerStaleCalls(t *testing.T) {
	hooks := &tapHooks{alter: func(c *plugin.HookCall) *plugin.HookCall {
		if released(c) || !strings.HasPrefix(c.GetRun(), "stale-") {
			return c
		}
		return &plugin.HookCall{Hook: c.GetHook(), Run: c.GetRun(), Token: "stale", Site: c.GetSite(), Payload: c.GetPayload()}
	}}
	e := startEngine(t, engineConfig{hooks: func(c plugin.HostHooksClient) plugin.HostHooksClient { hooks.HostHooksClient = c; return hooks }})
	var rec recorder
	decl := `{"intercept": [{"selector": {"flow": "f/broker/one", "module": "a"}}], "on_module_start": true, "on_module_end": true, "on_run_end": true}`
	res, err := e.client.RunWithHooks(context.Background(), one("stale-observed"), []byte(decl), rec.handle)
	if err != nil || res.GetStatus() != "success" || string(res.GetResult()) != `"one"` {
		t.Errorf("a run whose calls are stale: %v, %v; want success with its step's result", res, err)
	}
	res, err = e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "stale-provided", Path: "f/broker/prov", Args: []byte(`{}`)}, []byte(`{"providers": ["bp"]}`), rec.handle)
	if err != nil || res.GetStatus() != "latched" || !strings.Contains(res.GetLatched(), "provider_declined") {
		t.Errorf("a stale provider at a mandatory site: %v, %v; want latched, provider_declined", res, err)
	}
	firstWrite := `{"write_capable": [{"flow": "f/broker/one", "module": "a"}], "before_first_write": true}`
	res, err = e.client.RunWithHooks(context.Background(), one("stale-write"), []byte(firstWrite), rec.handle)
	if err != nil || res.GetStatus() != "success" || string(res.GetResult()) != `"one"` {
		t.Errorf("a stale first-write verdict: %v, %v; want success with its step's result", res, err)
	}
	if got := rec.heard(); len(got) != 0 {
		t.Errorf("stale calls reached a handler: %v", got)
	}

	live := recorder{answer: func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		if c.GetHook() == "before_first_write" {
			return &plugin.HookReply{Error: &plugin.HookError{Code: plugin.HookCodeUnknownRun, Message: "the host's own refusal"}}, nil
		}
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}}
	res, err = e.client.RunWithHooks(context.Background(), one("live-write"), []byte(firstWrite), live.handle)
	if err != nil || res.GetStatus() != "latched" || !strings.Contains(res.GetLatched(), "the host's own refusal") || string(res.GetResult()) == `"one"` {
		t.Errorf("a registered run's verdict that replies unknown_run: %v, %v; want latched before the write, with its message", res, err)
	}
	if got := set(live.heard()); len(got) != 1 || got[0] != "before_first_write@m:a" {
		t.Errorf("the registered run's host heard %v, want its verdict", got)
	}
}

// TestBrokerMessageLimits checks the message limits of the hooks' calls:
// the host takes an onRunEnd whose full trace is over gRPC's default of 4
// MiB, within its limit of 512 MiB; and an engine whose limit is 1 MiB
// refuses an interception's answer of 2 MiB, which latches the run.
func TestBrokerMessageLimits(t *testing.T) {
	e := startEngine(t, engineConfig{})
	var size atomic.Int64
	rec := recorder{answer: func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		if c.GetHook() == "on_run_end" {
			size.Store(int64(len(c.GetPayload())))
		}
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}}
	res, err := e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Path: "f/broker/big", Args: []byte(`{}`)}, []byte(`{"on_run_end": true}`), rec.handle)
	if err != nil || res.GetStatus() != "success" || strings.Contains(string(res.GetWarnings()), "on_run_end") {
		t.Errorf("a run with a 5 MiB trace: %v, %v; want success without an onRunEnd warning", res, err)
	}
	if n := size.Load(); n < 5<<20 {
		t.Errorf("the host's onRunEnd payload has %d bytes, want the full trace, over 5 MiB", n)
	}

	small := startEngine(t, engineConfig{maxMessageBytes: 1 << 20})
	big := recorder{answer: func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
		if c.GetHook() == "intercept" {
			return &plugin.HookReply{Payload: []byte(`{"result": "` + strings.Repeat("x", 2<<20) + `"}`)}, nil
		}
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}}
	res, err = small.client.RunWithHooks(context.Background(), one(""), []byte(interceptOne), big.handle)
	if err != nil || res.GetStatus() != "latched" || !strings.Contains(res.GetLatched(), "ResourceExhausted") {
		t.Errorf("an answer over the engine's limit: %v, %v; want latched, ResourceExhausted", res, err)
	}
}

// TestBrokerDialStall checks that a broker id that does not answer, which
// go-plugin's dial waits on, stalls nothing else: the test holds that
// dial, and while it waits GetRun answers and a run with hooks on the
// host's own connection completes. Let go, the dial fails, and its run is
// refused.
func TestBrokerDialStall(t *testing.T) {
	e := startEngine(t, engineConfig{})
	var rec recorder
	if res, err := e.client.RunWithHooks(context.Background(), one(""), []byte(`{"on_run_end": true}`), rec.handle); err != nil || res.GetStatus() != "success" {
		t.Fatalf("the first run: %v, %v", res, err)
	}
	dialing, hold := make(chan struct{}), make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(hold) }) }
	t.Cleanup(letGo)
	e.srv.hooks.dialing = func(id uint32) {
		if id == 9999 {
			close(dialing)
			<-hold
		}
	}
	stalled := runAsync(func() (*plugin.RunResult, error) {
		return e.gate.Runner().Run(context.Background(), &plugin.RunRequest{Id: "stalled", Path: "f/broker/one", Hooks: &plugin.Hooks{BrokerId: 9999, Token: "stall", Declaration: []byte(interceptOne)}})
	})
	within(t, "the dial of the silent broker id", dialing)
	lookup := make(chan error, 1)
	go func() {
		_, err := e.gate.Runner().GetRun(context.Background(), &plugin.GetRunRequest{Id: "x", Token: "unrelated"})
		lookup <- err
	}()
	if err := within(t, "GetRun while the dial waits", lookup); err != nil {
		t.Fatalf("GetRun while the dial waits: %v", err)
	}
	other := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), one(""), []byte(`{"on_run_end": true}`), rec.handle)
	})
	if o := within(t, "a run with hooks while the dial waits", other); o.err != nil || o.res.GetStatus() != "success" {
		t.Errorf("a run with hooks while the dial waits: %v, %v", o.res, o.err)
	}
	letGo()
	if o := within(t, "the stalled admission", stalled); !hasNotStarted(o.err) {
		t.Errorf("the admission of a silent broker id: %v, want a refusal", o.err)
	}
}

// within receives from ch, failing the test after 30 seconds.
func within[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: nothing within 30 seconds", what)
		var zero T
		return zero
	}
}

// TestBrokerClientClose checks that Client.Close stops the Client's broker
// connection while another Client of the connection goes on, with a run
// of the closed Client held in flight. The Client's ReleaseHooks call, on
// the engine's HooksControl service, tells the engine that it let the
// connection go, so the engine closes it at once and holds only the other
// Client's; a run with hooks after Close is
// refused without reaching the engine; the other Client's runs are still
// served. Released, the held run succeeds, its onRunEnd answered by no
// one, and the engine lets its token go.
func TestBrokerClientClose(t *testing.T) {
	var tap *tapRunner
	e := startEngine(t, engineConfig{lookupAfter: time.Hour, runner: func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap }})
	dir, letGo := holdDir(t)
	var rec recorder
	held := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "held", Path: "f/broker/hold", Args: jsonOf(t, map[string]string{"dir": dir})}, []byte(`{"on_run_end": true}`), rec.handle)
	})
	waitStarted(t, dir)
	bc, err := e.srv.hooks.client(tap.broker("held"))
	if err != nil {
		t.Fatal(err)
	}
	conn := bc.client
	if rep, err := conn.Call(context.Background(), &plugin.HookCall{Hook: "on_run_end", Run: "x", Token: "y"}); err != nil || rep.GetError().GetCode() != plugin.HookCodeUnknownRun {
		t.Fatalf("a call before Close: %v, %v; want unknown_run from the serving Client", rep, err)
	}
	other := e.dispense()
	var otherRec recorder
	if res, err := other.RunWithHooks(context.Background(), one("other"), []byte(`{"on_run_end": true}`), otherRec.handle); err != nil || res.GetStatus() != "success" {
		t.Fatalf("the other Client's run: %v, %v", res, err)
	}
	if n := e.srv.hooks.dialled(); n != 2 {
		t.Fatalf("the engine holds %d broker connections, want the two Clients'", n)
	}

	e.client.Close()
	waitFor(t, "the engine closes the closed Client's connection, its run still held", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := conn.Call(ctx, &plugin.HookCall{Hook: "on_run_end", Run: "x", Token: "y"})
		return status.Code(err) == codes.Canceled && e.srv.hooks.dialled() == 1
	})
	if n := e.srv.hooks.heldTokens(); n != 1 {
		t.Errorf("the engine holds %d tokens, want the held run's", n)
	}
	if _, err := e.client.RunWithHooks(context.Background(), one("after"), []byte(`{"on_run_end": true}`), rec.handle); !errors.Is(err, plugin.ErrNotStarted) {
		t.Errorf("a run with hooks after Close: %v, want ErrNotStarted", err)
	}
	if n := tap.runsOf("after"); n != 0 {
		t.Errorf("a run after Close reached the engine %d times", n)
	}
	heard := len(otherRec.heard())
	if res, err := other.RunWithHooks(context.Background(), one("other-again"), []byte(`{"on_run_end": true}`), otherRec.handle); err != nil || res.GetStatus() != "success" {
		t.Errorf("the other Client's run after Close: %v, %v", res, err)
	}
	if n := len(otherRec.heard()) - heard; n != 1 {
		t.Errorf("the other Client's host heard %d calls of its run after Close, want its onRunEnd", n)
	}

	letGo()
	if o := within(t, "the held run", held); o.err != nil || o.res.GetStatus() != "success" || !noWarnings(o.res.GetWarnings()) {
		t.Errorf("the held run of the closed Client: %v, %v; want success without warnings", o.res, o.err)
	}
	if got := rec.heard(); len(got) != 0 {
		t.Errorf("the closed Client's host heard %v", got)
	}
	waitFor(t, "the held run's token let go", func() bool { return e.srv.hooks.heldTokens() == 0 })
}

// TestBrokerFirstRunRefused checks a first run with hooks that the engine
// refuses before its admission dials the host, for a kind that it does
// not know. go-plugin drops the address of a broker id that no dial took
// after 5 seconds, and the test waits that long. The next run with hooks
// on the same Client opens a new broker connection and is served.
func TestBrokerFirstRunRefused(t *testing.T) {
	var tap *tapRunner
	e := startEngine(t, engineConfig{runner: func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap }})
	var rec recorder
	if _, err := e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "refused", Kind: "nonesuch", Path: "f/broker/one"}, []byte(`{"on_run_end": true}`), rec.handle); !errors.Is(err, plugin.ErrNotStarted) {
		t.Fatalf("a run of an unknown kind: %v, want ErrNotStarted", err)
	}
	if n := e.client.Registrations(); n != 0 {
		t.Errorf("%d registrations after the refused run, want 0", n)
	}
	time.Sleep(6 * time.Second)
	res, err := e.client.RunWithHooks(context.Background(), one("later"), []byte(`{"on_run_end": true}`), rec.handle)
	if err != nil || res.GetStatus() != "success" {
		t.Fatalf("the later run: %v, %v", res, err)
	}
	if got := set(rec.heard()); len(got) != 1 || got[0] != "on_run_end@" {
		t.Errorf("the host heard %v, want the later run's onRunEnd", got)
	}
	if a, b := tap.broker("refused"), tap.broker("later"); a == b {
		t.Errorf("the later run named the refused run's broker id %d, whose address go-plugin dropped", a)
	}
	if n := e.srv.hooks.dialled(); n != 1 {
		t.Errorf("the engine holds %d broker connections, want the later run's", n)
	}
}

// socketsIn returns the go-plugin sockets in dir, newest first.
func socketsIn(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type sock struct {
		path string
		mod  time.Time
	}
	var socks []sock
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "plugin") || e.Type()&os.ModeSocket == 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		socks = append(socks, sock{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(socks, func(i, j int) bool { return socks[i].mod.After(socks[j].mod) })
	out := make([]string, len(socks))
	for i, s := range socks {
		out[i] = s.path
	}
	return out, nil
}

// connStates returns the state of each broker connection that the engine
// holds.
func (b *brokerHooks) connStates() []connectivity.State {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	var out []connectivity.State
	for _, c := range b.conns {
		select {
		case <-c.done:
			if c.conn != nil {
				out = append(out, c.conn.GetState())
			}
		default:
		}
	}
	return out
}

// TestBrokerTransientFailure checks connections to the host's listener
// that fail while the listener still serves: the test's dialer drops the
// engine's connection and fails the dials that follow until the test lets
// them through. A run with hooks in flight keeps the connection, which
// connects again, so the run's onRunEnd reaches the host. With no run in
// flight, the engine closes a connection that cannot connect, and the next
// run with hooks on the same Client opens a new broker connection and is
// served. The same holds while the Client still holds the registration of
// a run whose release call was lost: the next run is served on a new
// broker id before that registration is reconciled. Last, Close during a
// failure, with a run held in flight: the engine hears of it on the Runner
// connection and closes its side before the run ends, and the run's later
// onRunEnd gets no answer.
func TestBrokerTransientFailure(t *testing.T) {
	brokerBundle(t)
	dir, err := os.MkdirTemp("", "wlb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// go-plugin makes each listener's socket in os.TempDir.
	t.Setenv("TMPDIR", dir)
	var mainSocket atomic.Value
	var broken atomic.Bool
	var mu sync.Mutex
	var live []net.Conn
	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		if broken.Load() {
			return nil, errors.New("the test's transport failed the dial")
		}
		socks, err := socketsIn(dir)
		if err != nil {
			return nil, err
		}
		for _, s := range socks {
			if s == mainSocket.Load() {
				continue
			}
			c, err := (&net.Dialer{}).DialContext(ctx, "unix", s)
			if err == nil {
				mu.Lock()
				live = append(live, c)
				mu.Unlock()
			}
			return c, err
		}
		return nil, errors.New("no listener of the host's hooks")
	}
	fail := func() {
		broken.Store(true)
		mu.Lock()
		defer mu.Unlock()
		for _, c := range live {
			c.Close()
		}
		live = nil
	}
	hooks := &tapHooks{drop: firstReleaseOf("lost")}
	var tap *tapRunner
	e := startEngine(t, engineConfig{
		lookupAfter: time.Hour,
		dialOptions: []grpc.DialOption{grpc.WithContextDialer(dial)},
		runner:      func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap },
		hooks:       func(c plugin.HostHooksClient) plugin.HostHooksClient { hooks.HostHooksClient = c; return hooks },
	})
	socks, err := socketsIn(dir)
	if err != nil || len(socks) != 1 {
		t.Fatalf("the sockets before any run: %v, %v; want the connection's own", socks, err)
	}
	mainSocket.Store(socks[0])
	inState := func(want connectivity.State) func() bool {
		return func() bool {
			for _, s := range e.srv.hooks.connStates() {
				if s == want {
					return true
				}
			}
			return false
		}
	}

	hold, letGo := holdDir(t)
	var rec recorder
	inFlight := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "held", Path: "f/broker/hold", Args: jsonOf(t, map[string]string{"dir": hold})}, []byte(`{"on_run_end": true}`), rec.handle)
	})
	waitStarted(t, hold)
	waitFor(t, "the engine's connection to the host", inState(connectivity.Ready))
	fail()
	waitFor(t, "the connection failing", inState(connectivity.TransientFailure))
	broken.Store(false)
	waitFor(t, "the run's connection back", inState(connectivity.Ready))
	letGo()
	if o := within(t, "the run in flight", inFlight); o.err != nil || o.res.GetStatus() != "success" || strings.Contains(string(o.res.GetWarnings()), "on_run_end") {
		t.Fatalf("the run in flight: %v, %v; want success without an onRunEnd warning", o.res, o.err)
	}
	if got := set(rec.heard()); len(got) != 1 || got[0] != "on_run_end@" {
		t.Errorf("the host heard %v, want the run's onRunEnd", got)
	}

	waitFor(t, "the run's token let go", func() bool { return e.srv.hooks.heldTokens() == 0 })
	bc, err := e.srv.hooks.client(tap.broker("held"))
	if err != nil {
		t.Fatal(err)
	}
	fail()
	waitFor(t, "the engine forgets and closes the connection that cannot connect", func() bool {
		return e.srv.hooks.dialled() == 0 && bc.conn.GetState() == connectivity.Shutdown
	})
	broken.Store(false)
	var next recorder
	res, err := e.client.RunWithHooks(context.Background(), one("next"), []byte(`{"on_run_end": true}`), next.handle)
	if err != nil || res.GetStatus() != "success" {
		t.Fatalf("the next run on the same Client: %v, %v", res, err)
	}
	if got := set(next.heard()); len(got) != 1 || got[0] != "on_run_end@" {
		t.Errorf("the next run's host heard %v, want its onRunEnd", got)
	}

	if res, err := e.client.RunWithHooks(context.Background(), one("lost"), []byte(`{"on_run_end": true}`), next.handle); err != nil || res.GetStatus() != "success" {
		t.Fatalf("the run whose release is lost: %v, %v", res, err)
	}
	waitFor(t, "the lost release, and its token let go", func() bool { return hooks.droppedCount() == 1 && e.srv.hooks.heldTokens() == 0 })
	if n := e.client.Registrations(); n != 1 {
		t.Fatalf("%d registrations after the lost release, want its run's", n)
	}
	fail()
	waitFor(t, "the engine closes the connection that cannot connect", func() bool { return e.srv.hooks.dialled() == 0 })
	broken.Store(false)
	var fresh recorder
	if res, err := e.client.RunWithHooks(context.Background(), one("fresh"), []byte(`{"on_run_end": true}`), fresh.handle); err != nil || res.GetStatus() != "success" {
		t.Fatalf("a run while the lost release's registration is held: %v, %v", res, err)
	}
	if got := set(fresh.heard()); len(got) != 1 || got[0] != "on_run_end@" {
		t.Errorf("that run's host heard %v, want its onRunEnd", got)
	}
	if tap.broker("fresh") == tap.broker("lost") {
		t.Errorf("the run named the broker id %d, whose connection the engine closed", tap.broker("fresh"))
	}
	// The run's own release drops its registration; the lost release's
	// stays, not yet reconciled.
	waitFor(t, "the run's release, the lost release's registration kept", func() bool { return e.client.Registrations() == 1 })

	hold2, letGo2 := holdDir(t)
	var closing recorder
	inFlight2 := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "closing", Path: "f/broker/hold", Args: jsonOf(t, map[string]string{"dir": hold2})}, []byte(`{"on_run_end": true}`), closing.handle)
	})
	waitStarted(t, hold2)
	waitFor(t, "the closing run's connection", inState(connectivity.Ready))
	bc2, err := e.srv.hooks.client(tap.broker("closing"))
	if err != nil {
		t.Fatal(err)
	}
	fail()
	waitFor(t, "the closing run's connection failing", inState(connectivity.TransientFailure))
	e.client.Close()
	waitFor(t, "the engine closes the closed Client's connection, its run still held", func() bool {
		return e.srv.hooks.dialled() == 0 && bc2.conn.GetState() == connectivity.Shutdown
	})
	if n := e.srv.hooks.heldTokens(); n != 1 {
		t.Errorf("the engine holds %d tokens, want the closing run's", n)
	}
	broken.Store(false)
	letGo2()
	if o := within(t, "the closing run", inFlight2); o.err != nil || o.res.GetStatus() != "success" || !noWarnings(o.res.GetWarnings()) {
		t.Errorf("the run held through Close: %v, %v; want success without warnings", o.res, o.err)
	}
	if got := closing.heard(); len(got) != 0 {
		t.Errorf("the closed Client's host heard %v", got)
	}
}

// TestBrokerCloseDuringVerdict checks a broker connection let go while the
// host's handler of a first-write verdict still runs, its answer an
// approval that never arrives. First by Close, which stops the Client's
// listener; then by the engine's own side, a release while the host still
// serves. Either way the verdict's call, cut, fails instead of counting as
// no answer, so the run latches on it and its write-capable step never
// runs. The plugin timeout is a minute, so only the letting go can end the
// call.
func TestBrokerCloseDuringVerdict(t *testing.T) {
	var tap *tapRunner
	e := startEngine(t, engineConfig{pluginTimeout: time.Minute, lookupAfter: time.Hour, runner: func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap }})
	firstWrite := `{"write_capable": [{"flow": "f/broker/one", "module": "a"}], "before_first_write": true}`
	cut := func(t *testing.T, c *plugin.Client, id string, letGo func()) {
		t.Helper()
		entered, answer := make(chan struct{}), make(chan struct{})
		var once sync.Once
		approve := func() { once.Do(func() { close(answer) }) }
		t.Cleanup(approve)
		h := func(_ context.Context, call *plugin.HookCall) (*plugin.HookReply, error) {
			if call.GetHook() == "before_first_write" {
				close(entered)
				<-answer
			}
			return &plugin.HookReply{Payload: []byte(`{}`)}, nil
		}
		run := runAsync(func() (*plugin.RunResult, error) {
			return c.RunWithHooks(context.Background(), one(id), []byte(firstWrite), h)
		})
		within(t, "the verdict's handler", entered)
		letGo()
		o := within(t, "the run whose verdict was cut", run)
		if o.err != nil || o.res.GetStatus() != "latched" || !strings.Contains(o.res.GetLatched(), "the host's hooks service") || string(o.res.GetResult()) == `"one"` {
			t.Errorf("the run whose verdict was cut: %v, %v; want latched on the cut call, its step not run", o.res, o.err)
		}
		approve()
	}
	cut(t, e.client, "by-close", e.client.Close)
	other := e.dispense()
	cut(t, other, "by-release", func() {
		if _, err := e.gate.Hooks().ReleaseHooks(context.Background(), &plugin.ReleaseHooksRequest{BrokerId: tap.broker("by-release")}); err != nil {
			t.Fatalf("the engine's release: %v", err)
		}
	})
}

// lossyControl is a fake transport of the HooksControl service that loses
// the first release call before it reaches the engine.
type lossyControl struct {
	plugin.HooksControlServer
	calls atomic.Int32
}

func (l *lossyControl) ReleaseHooks(ctx context.Context, req *plugin.ReleaseHooksRequest) (*plugin.ReleaseHooksResponse, error) {
	if l.calls.Add(1) == 1 {
		return nil, status.Error(codes.Unavailable, "the test's transport lost the release call")
	}
	return l.HooksControlServer.ReleaseHooks(ctx, req)
}

// TestBrokerReleaseRetried checks a release call lost before it reaches
// the engine, at Close with a run of the Client held in flight: the
// Client stops its listener and sends the call again, and the engine
// closes its connection while the run is still held. The run then
// succeeds, its onRunEnd answered by no one.
func TestBrokerReleaseRetried(t *testing.T) {
	var tap *tapRunner
	lossy := &lossyControl{}
	e := startEngine(t, engineConfig{
		lookupAfter: 10 * time.Millisecond,
		runner:      func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap },
		control: func(s plugin.HooksControlServer) plugin.HooksControlServer {
			lossy.HooksControlServer = s
			return lossy
		},
	})
	dir, letGo := holdDir(t)
	var rec recorder
	held := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "held", Path: "f/broker/hold", Args: jsonOf(t, map[string]string{"dir": dir})}, []byte(`{"on_run_end": true}`), rec.handle)
	})
	waitStarted(t, dir)
	bc, err := e.srv.hooks.client(tap.broker("held"))
	if err != nil {
		t.Fatal(err)
	}
	e.client.Close()
	waitFor(t, "the engine closes the closed Client's connection, its run still held", func() bool {
		return e.srv.hooks.dialled() == 0 && bc.conn.GetState() == connectivity.Shutdown && e.srv.hooks.heldTokens() == 1
	})
	if n := lossy.calls.Load(); n < 2 {
		t.Errorf("%d release calls, want the lost one and the one sent again", n)
	}
	letGo()
	if o := within(t, "the held run", held); o.err != nil || o.res.GetStatus() != "success" || !noWarnings(o.res.GetWarnings()) {
		t.Errorf("the held run of the closed Client: %v, %v; want success without warnings", o.res, o.err)
	}
	if got := rec.heard(); len(got) != 0 {
		t.Errorf("the closed Client's host heard %v", got)
	}
}

// TestBrokerReleaseDuringDial checks a release that comes while the
// engine's admission still dials the run's broker id, between the dial's
// connection and the dial's end: the dial closes that connection as it
// ends, so the engine holds no connection while the run is held, and the
// run's later onRunEnd gets no answer.
func TestBrokerReleaseDuringDial(t *testing.T) {
	var tap *tapRunner
	e := startEngine(t, engineConfig{lookupAfter: time.Hour, runner: func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap }})
	atDial, proceed := make(chan *grpc.ClientConn, 1), make(chan struct{})
	var once sync.Once
	goOn := func() { once.Do(func() { close(proceed) }) }
	t.Cleanup(goOn)
	e.srv.hooks.dialed = func(_ uint32, conn *grpc.ClientConn) {
		atDial <- conn
		<-proceed
	}
	dir, letGo := holdDir(t)
	var rec recorder
	held := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), &plugin.RunRequest{Id: "dialled", Path: "f/broker/hold", Args: jsonOf(t, map[string]string{"dir": dir})}, []byte(`{"on_run_end": true}`), rec.handle)
	})
	conn := within(t, "the dial of the run's broker id", atDial)
	if _, err := e.gate.Hooks().ReleaseHooks(context.Background(), &plugin.ReleaseHooksRequest{BrokerId: tap.broker("dialled")}); err != nil {
		t.Fatalf("the release during the dial: %v", err)
	}
	goOn()
	waitStarted(t, dir)
	waitFor(t, "the dial closes its connection, the run held", func() bool {
		return e.srv.hooks.dialled() == 0 && conn.GetState() == connectivity.Shutdown && e.srv.hooks.heldTokens() == 1
	})
	letGo()
	if o := within(t, "the held run", held); o.err != nil || o.res.GetStatus() != "success" || !noWarnings(o.res.GetWarnings()) {
		t.Errorf("the run whose connection was let go during its dial: %v, %v; want success without warnings", o.res, o.err)
	}
	if got := rec.heard(); len(got) != 0 {
		t.Errorf("the host heard %v", got)
	}
}

// countingControl is a fake transport of the HooksControl service that
// counts the release calls that the engine answered.
type countingControl struct {
	plugin.HooksControlServer
	answered atomic.Int32
}

func (c *countingControl) ReleaseHooks(ctx context.Context, req *plugin.ReleaseHooksRequest) (*plugin.ReleaseHooksResponse, error) {
	res, err := c.HooksControlServer.ReleaseHooks(ctx, req)
	if err == nil {
		c.answered.Add(1)
	}
	return res, err
}

// TestBrokerReleaseBeforeAdmission checks a release that overtakes the
// first admission of its broker id: the test holds a run's request before
// the engine admits it, and Close stops the Client's listener and gets the
// engine's answer to the release before the request goes on. The
// admission's dial cannot connect, so the run is refused before it
// starts, and the engine holds no connection and no token.
func TestBrokerReleaseBeforeAdmission(t *testing.T) {
	var tap *tapRunner
	reached, goOn := make(chan struct{}), make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(goOn) }) }
	t.Cleanup(letGo)
	control := &countingControl{}
	e := startEngine(t, engineConfig{
		lookupAfter: time.Hour,
		runner: func(s plugin.RunnerServer) plugin.RunnerServer {
			tap = newTap(s)
			tap.run = func(_ context.Context, req *plugin.RunRequest) (*plugin.RunResult, error, bool) {
				if req.GetId() == "overtaken" {
					close(reached)
					<-goOn
				}
				return nil, nil, false
			}
			return tap
		},
		control: func(s plugin.HooksControlServer) plugin.HooksControlServer {
			control.HooksControlServer = s
			return control
		},
	})
	var rec recorder
	run := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), one("overtaken"), []byte(`{"on_run_end": true}`), rec.handle)
	})
	within(t, "the run's request at the engine", reached)
	e.client.Close()
	waitFor(t, "the engine's answer to the release", func() bool { return control.answered.Load() >= 1 })
	letGo()
	if o := within(t, "the overtaken run", run); !errors.Is(o.err, plugin.ErrNotStarted) {
		t.Errorf("the run whose release came first: %v, %v; want ErrNotStarted", o.res, o.err)
	}
	if n := e.srv.hooks.dialled(); n != 0 {
		t.Errorf("the engine holds %d broker connections, want none", n)
	}
	if n := e.srv.hooks.heldTokens(); n != 0 {
		t.Errorf("the engine holds %d tokens, want none", n)
	}
	if got := rec.heard(); len(got) != 0 {
		t.Errorf("the host heard %v", got)
	}
}

// TestBrokerReleaseSharedDial checks two runs whose admissions share one
// dial, with the host's release while that dial is under way: the first
// admission holds its new connection, and the second waits on it. The
// dial ends with the connection let go, and the test holds the first
// admission there: the second run, woken, sees it let go at once, so its
// interception gets no answer although the host still serves, and it
// succeeds with its step's own result. So does the first, and the host
// hears nothing.
func TestBrokerReleaseSharedDial(t *testing.T) {
	var tap *tapRunner
	e := startEngine(t, engineConfig{lookupAfter: time.Hour, runner: func(s plugin.RunnerServer) plugin.RunnerServer { tap = newTap(s); return tap }})
	atDial, dialGo := make(chan struct{}), make(chan struct{})
	atEnd, endGo := make(chan struct{}), make(chan struct{})
	joined := make(chan struct{}, 1)
	var onceDial, onceEnd sync.Once
	letDial := func() { onceDial.Do(func() { close(dialGo) }) }
	letEnd := func() { onceEnd.Do(func() { close(endGo) }) }
	t.Cleanup(func() { letDial(); letEnd() })
	e.srv.hooks.dialed = func(uint32, *grpc.ClientConn) {
		close(atDial)
		<-dialGo
	}
	e.srv.hooks.joined = func(uint32) { joined <- struct{}{} }
	e.srv.hooks.published = func(uint32) {
		close(atEnd)
		<-endGo
	}
	rec := recorder{answer: answerWith(`"host"`)}
	first := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), one("first"), []byte(interceptOne), rec.handle)
	})
	within(t, "the first admission's dial", atDial)
	second := runAsync(func() (*plugin.RunResult, error) {
		return e.client.RunWithHooks(context.Background(), one("second"), []byte(interceptOne), rec.handle)
	})
	within(t, "the second admission waiting on the dial", joined)
	if _, err := e.gate.Hooks().ReleaseHooks(context.Background(), &plugin.ReleaseHooksRequest{BrokerId: tap.broker("first")}); err != nil {
		t.Fatalf("the release during the dial: %v", err)
	}
	letDial()
	within(t, "the dial's end", atEnd)
	if o := within(t, "the second run", second); o.err != nil || o.res.GetStatus() != "success" || string(o.res.GetResult()) != `"one"` {
		t.Errorf("the run that waited on the let-go dial: %v, %v; want success with its step's own result", o.res, o.err)
	}
	letEnd()
	if o := within(t, "the first run", first); o.err != nil || o.res.GetStatus() != "success" || string(o.res.GetResult()) != `"one"` {
		t.Errorf("the run that dialled: %v, %v; want success with its step's own result", o.res, o.err)
	}
	if got := rec.heard(); len(got) != 0 {
		t.Errorf("the host heard %v after the release", got)
	}
}

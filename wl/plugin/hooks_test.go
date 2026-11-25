package plugin

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lookupRunner is a fake transport of GetRun: each lookup takes the next
// of answers, a state or an error.
type lookupRunner struct {
	RunnerClient
	answers chan any
	asked   chan *GetRunRequest
}

// blockLookup is the answer that holds a lookup until its context ends,
// whose error ended reports.
type blockLookup struct{ ended chan error }

func (r *lookupRunner) GetRun(ctx context.Context, req *GetRunRequest, _ ...grpc.CallOption) (*GetRunResponse, error) {
	r.asked <- req
	switch a := (<-r.answers).(type) {
	case blockLookup:
		<-ctx.Done()
		a.ended <- ctx.Err()
		return nil, ctx.Err()
	case error:
		return nil, a
	default:
		return &GetRunResponse{State: a.(string)}, nil
	}
}

// Run fails the test's expectations: no run reaches the engine.
func (r *lookupRunner) Run(context.Context, *RunRequest, ...grpc.CallOption) (*RunResult, error) {
	return nil, errors.New("a run reached the engine")
}

// fakeTimers is the Client's timer hook: each wait is sent on waits, and
// fires when the test sends on fire.
type fakeTimers struct {
	waits   chan time.Duration
	fire    chan struct{}
	stopped chan struct{}
}

func newFakeTimers() *fakeTimers {
	return &fakeTimers{waits: make(chan time.Duration, 16), fire: make(chan struct{}), stopped: make(chan struct{}, 16)}
}

func (f *fakeTimers) timer(d time.Duration) (<-chan time.Time, func() bool) {
	f.waits <- d
	ch := make(chan time.Time, 1)
	go func() {
		if _, ok := <-f.fire; ok {
			ch <- time.Now()
		}
	}()
	return ch, func() bool { f.stopped <- struct{}{}; return true }
}

// within receives from ch, failing the test after 10 seconds.
func within[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: nothing within 10 seconds", what)
		var zero T
		return zero
	}
}

// lookupClient is a Client whose lookups go to a lookupRunner and whose
// waits are fake, with a registration of the run r1, token t1.
func lookupClient(timeout time.Duration) (*Client, *lookupRunner, *fakeTimers, *registration) {
	c := newClient(nil, nil, DefaultReplyMargin)
	runner := &lookupRunner{answers: make(chan any, 16), asked: make(chan *GetRunRequest, 16)}
	timers := newFakeTimers()
	c.runner, c.timer, c.hooks.timeout = runner, timers.timer, timeout
	reg := &registration{token: "t1", sv: &serving{runs: 1}, done: make(chan struct{})}
	c.hooks.regs["r1"] = reg
	return c, runner, timers, reg
}

// TestClientLookupSchedule checks when the Client asks GetRun about a
// registration: first after the plugin timeout, then at twice the last
// interval while the engine answers held or the lookup fails, at most 10
// minutes apart; it drops the registration at gone and asks no more.
func TestClientLookupSchedule(t *testing.T) {
	c, runner, timers, reg := lookupClient(4 * time.Minute)
	go c.reconcile("r1", reg)
	answers := []any{"held", status.Error(codes.Unavailable, "lost"), "held", "gone"}
	want := []time.Duration{4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i, a := range answers {
		if got := within(t, "a wait", timers.waits); got != want[i] {
			t.Errorf("wait %d: %v, want %v", i, got, want[i])
		}
		timers.fire <- struct{}{}
		req := within(t, "a lookup", runner.asked)
		if req.GetId() != "r1" || req.GetToken() != "t1" {
			t.Errorf("lookup %d: %v, want run r1 with token t1", i, req)
		}
		if c.Registrations() != 1 {
			t.Errorf("lookup %d: the registration was dropped before the answer", i)
		}
		runner.answers <- a
	}
	within(t, "the registration's drop", reg.done)
	if n := c.Registrations(); n != 0 {
		t.Errorf("%d registrations after gone, want 0", n)
	}
	select {
	case d := <-timers.waits:
		t.Errorf("a wait of %v after gone", d)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestClientLookupStopsAtDrop checks that a registration dropped otherwise
// (the engine's release call, the Client's Close) stops its lookups: the
// wait is stopped and GetRun is never asked.
func TestClientLookupStopsAtDrop(t *testing.T) {
	c, runner, timers, reg := lookupClient(time.Minute)
	go c.reconcile("r1", reg)
	within(t, "the first wait", timers.waits)
	if _, err := (hookServer{c: c}).Call(context.Background(), &HookCall{Hook: releasedHook, Run: "r1", Token: "t1"}); err != nil {
		t.Fatal(err)
	}
	within(t, "the stopped wait", timers.stopped)
	select {
	case req := <-runner.asked:
		t.Errorf("a lookup %v after the release", req)
	case <-time.After(100 * time.Millisecond):
	}

	c, runner, timers, reg = lookupClient(time.Minute)
	go c.reconcile("r1", reg)
	within(t, "the first wait", timers.waits)
	c.Close()
	within(t, "the stopped wait", timers.stopped)
	if n := c.Registrations(); n != 0 {
		t.Errorf("%d registrations after Close, want 0", n)
	}
}

// TestClientHookCalls checks how the Client answers the engine's calls: a
// call of a registered run and token goes to its handler, whose error
// answers plugin_error, and so does its reply of the code unknown_run; a
// call whose run or token names no registration answers unknown_run
// without reaching a handler; the release call drops only the registration
// whose token it carries.
func TestClientHookCalls(t *testing.T) {
	c := newClient(nil, nil, DefaultReplyMargin)
	var heard []string
	c.hooks.regs["r1"] = &registration{token: "t1", sv: &serving{runs: 1}, done: make(chan struct{}), handler: func(_ context.Context, call *HookCall) (*HookReply, error) {
		heard = append(heard, call.GetHook())
		switch call.GetHook() {
		case "on_run_end":
			return nil, errors.New("the host failed")
		case "before_first_write":
			return &HookReply{Error: &HookError{Code: HookCodeUnknownRun, Message: "the handler's own"}}, nil
		}
		return &HookReply{Payload: []byte(`{"result": 1}`)}, nil
	}}
	s := hookServer{c: c}
	call := func(hook, run, token string) *HookReply {
		t.Helper()
		rep, err := s.Call(context.Background(), &HookCall{Hook: hook, Run: run, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	if rep := call("intercept", "r1", "t1"); string(rep.GetPayload()) != `{"result": 1}` || rep.GetError() != nil {
		t.Errorf("a registered run's call: %v, want its handler's answer", rep)
	}
	if rep := call("on_run_end", "r1", "t1"); rep.GetError().GetCode() != HookCodePluginError || rep.GetError().GetMessage() != "the host failed" {
		t.Errorf("a failing handler: %v, want plugin_error with its message", rep)
	}
	if rep := call("before_first_write", "r1", "t1"); rep.GetError().GetCode() != HookCodePluginError || rep.GetError().GetMessage() != "the handler's own" {
		t.Errorf("a handler's reply of the code unknown_run: %v, want plugin_error with its message", rep)
	}
	for _, rt := range [][2]string{{"r1", "old"}, {"r2", "t1"}} {
		if rep := call("intercept", rt[0], rt[1]); rep.GetError().GetCode() != HookCodeUnknownRun {
			t.Errorf("a call of run %s, token %s: %v, want unknown_run", rt[0], rt[1], rep)
		}
	}
	if rep := call(releasedHook, "r1", "old"); rep.GetError().GetCode() != HookCodeUnknownRun {
		t.Errorf("a release call with another token: %v, want unknown_run", rep)
	}
	if c.Registrations() != 1 {
		t.Error("a release call with another token dropped the registration")
	}
	call(releasedHook, "r1", "t1")
	if c.Registrations() != 0 {
		t.Error("the release call did not drop the registration")
	}
	if len(heard) != 3 {
		t.Errorf("the handler heard %v, want the three calls of its run and token", heard)
	}
}

// TestClientGenerationEnd checks that the end of the engine's generation
// closes a Client that serves hooks, on go-plugin's test connection: the
// run's registration is dropped, and a later run with hooks is refused. In
// a generation that has ended, a first run with hooks is refused without
// reaching the engine.
func TestClientGenerationEnd(t *testing.T) {
	generation, end := context.WithCancel(context.Background())
	defer end()
	c := echoClient(t, &echoEngine{generation: generation})
	if res, err := c.RunWithHooks(context.Background(), &RunRequest{Path: "f/x"}, []byte(`{"on_run_end": true}`), answerEmpty); err != nil || res.GetStatus() != "success" {
		t.Fatalf("the run: %v, %v", res, err)
	}
	if n := c.Registrations(); n != 1 {
		t.Fatalf("%d registrations after the run, want its own", n)
	}
	end()
	eventually(t, "the registration's drop", func() bool { return c.Registrations() == 0 })
	if _, err := c.RunWithHooks(context.Background(), &RunRequest{Path: "f/x"}, []byte(`{"on_run_end": true}`), answerEmpty); !errors.Is(err, ErrNotStarted) {
		t.Errorf("a run with hooks after the generation's end: %v, want ErrNotStarted", err)
	}

	ended, endNow := context.WithCancel(context.Background())
	endNow()
	engine := &echoEngine{generation: ended}
	late := echoClient(t, engine)
	if _, err := late.RunWithHooks(context.Background(), &RunRequest{Path: "f/x"}, []byte(`{"on_run_end": true}`), answerEmpty); !errors.Is(err, ErrNotStarted) || !strings.Contains(err.Error(), "generation has ended") {
		t.Errorf("a first run with hooks in an ended generation: %v, want ErrNotStarted for the generation's end", err)
	}
	if n := engine.runs.Load(); n != 0 {
		t.Errorf("the first run in an ended generation reached the engine %d times", n)
	}
	if n := late.Registrations(); n != 0 {
		t.Errorf("%d registrations after the refused run, want 0", n)
	}
}

// TestClientLookupTimeout checks that a lookup that gets no reply ends at
// the lookup's bound and is tried again later, and that a registration
// dropped while its lookup waits ends that lookup at once.
func TestClientLookupTimeout(t *testing.T) {
	c, runner, timers, reg := lookupClient(time.Minute)
	c.hooks.lookupTimeout = 20 * time.Millisecond
	go c.reconcile("r1", reg)
	within(t, "the first wait", timers.waits)
	timers.fire <- struct{}{}
	within(t, "the first lookup", runner.asked)
	ended := make(chan error, 1)
	runner.answers <- blockLookup{ended}
	if err := within(t, "the end of the silent lookup", ended); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the silent lookup ended with %v, want its bound", err)
	}
	if d := within(t, "the retry's wait", timers.waits); d != 2*time.Minute {
		t.Errorf("the retry's wait: %v, want 2m", d)
	}
	timers.fire <- struct{}{}
	within(t, "the second lookup", runner.asked)
	runner.answers <- "gone"
	within(t, "the drop at gone", reg.done)

	c, runner, timers, reg = lookupClient(time.Minute)
	c.hooks.lookupTimeout = time.Hour
	go c.reconcile("r1", reg)
	within(t, "the first wait", timers.waits)
	timers.fire <- struct{}{}
	within(t, "the lookup", runner.asked)
	ended = make(chan error, 1)
	runner.answers <- blockLookup{ended}
	if _, err := (hookServer{c: c}).Call(context.Background(), &HookCall{Hook: releasedHook, Run: "r1", Token: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := within(t, "the end of the waiting lookup", ended); !errors.Is(err, context.Canceled) {
		t.Errorf("the waiting lookup of a dropped registration ended with %v, want canceled", err)
	}
	select {
	case d := <-timers.waits:
		t.Errorf("a wait of %v after the drop", d)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestClientRunRefusesRegisteredID checks that a plain Run with the id of
// a run whose hooks are still registered is refused, without a call.
func TestClientRunRefusesRegisteredID(t *testing.T) {
	c, _, _, _ := lookupClient(time.Minute)
	if _, err := c.Run(context.Background(), &RunRequest{Id: "r1", Path: "f/x"}); !errors.Is(err, ErrNotStarted) {
		t.Errorf("a run with a registered id: %v, want ErrNotStarted", err)
	}
}

// echoEngine is a fake engine on go-plugin's test connection: its Run dials
// the run's broker connection, keeps it, calls the run's onRunEnd on it and
// succeeds. Its Clients get generation, when set, as the context of the
// engine's generation.
type echoEngine struct {
	UnimplementedRunnerServer
	goplugin.NetRPCUnsupportedPlugin
	generation context.Context
	broker     atomic.Pointer[goplugin.GRPCBroker]
	runs       atomic.Int32
	mu         sync.Mutex
	conn       HostHooksClient
}

func (e *echoEngine) GRPCServer(b *goplugin.GRPCBroker, s *grpc.Server) error {
	e.broker.Store(b)
	RegisterRunnerServer(s, e)
	return nil
}

func (e *echoEngine) GRPCClient(ctx context.Context, b *goplugin.GRPCBroker, cc *grpc.ClientConn) (any, error) {
	if e.generation != nil {
		ctx = e.generation
	}
	return (&RunnerPlugin{PluginTimeout: time.Hour}).GRPCClient(ctx, b, cc)
}

func (e *echoEngine) Run(ctx context.Context, req *RunRequest) (*RunResult, error) {
	e.runs.Add(1)
	conn, err := e.broker.Load().Dial(req.GetHooks().GetBrokerId())
	if err != nil {
		return nil, err
	}
	c := NewHostHooksClient(conn)
	e.mu.Lock()
	e.conn = c
	e.mu.Unlock()
	_, _ = c.Call(ctx, &HookCall{Hook: "on_run_end", Run: req.GetId(), Token: req.GetHooks().GetToken()})
	return &RunResult{Id: req.GetId(), Status: "success"}, nil
}

// TestClientCloseAtFirstUse checks Close against a first RunWithHooks
// that runs at the same time, on go-plugin's test connection with a real
// broker: either the run is refused, or it ran and Close then stopped the
// broker connection that it opened, so the engine's next call fails.
// Neither leaves a registration.
func TestClientCloseAtFirstUse(t *testing.T) {
	for i := range 10 {
		engine := &echoEngine{}
		gc, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{PluginName: engine})
		raw, err := gc.Dispense(PluginName)
		if err != nil {
			t.Fatal(err)
		}
		c := raw.(*Client)
		var wg sync.WaitGroup
		var runErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, runErr = c.RunWithHooks(context.Background(), &RunRequest{Path: "f/x"}, []byte(`{"on_run_end": true}`), func(context.Context, *HookCall) (*HookReply, error) {
				return &HookReply{Payload: []byte(`{}`)}, nil
			})
		}()
		go func() {
			defer wg.Done()
			c.Close()
		}()
		wg.Wait()
		switch {
		case errors.Is(runErr, ErrNotStarted):
		case runErr != nil:
			t.Fatalf("round %d: %v", i, runErr)
		default:
			engine.mu.Lock()
			conn := engine.conn
			engine.mu.Unlock()
			for until := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, err := conn.Call(ctx, &HookCall{Hook: "on_run_end", Run: "x", Token: "y"})
				cancel()
				if err != nil {
					break
				}
				if time.Now().After(until) {
					t.Fatalf("round %d: the broker connection still serves after Close", i)
				}
			}
		}
		if n := c.Registrations(); n != 0 {
			t.Errorf("round %d: %d registrations after Close", i, n)
		}
		gc.Close()
	}
}

// TestClientCloseStopsLateServer checks the HostHooks server that go-plugin
// asks for after Close, which a Close during a first RunWithHooks can
// leave to come: it is stopped already, so its Serve returns at once
// instead of serving.
func TestClientCloseStopsLateServer(t *testing.T) {
	c := newClient(nil, nil, DefaultReplyMargin)
	c.Close()
	s := c.hooksServer(&serving{}, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(ln) }()
	if err := within(t, "the late server's Serve", served); !errors.Is(err, grpc.ErrServerStopped) {
		t.Errorf("the late server's Serve: %v, want grpc.ErrServerStopped", err)
	}
}

// echoClient dispenses a Client of e on go-plugin's test connection.
func echoClient(t *testing.T, e *echoEngine) *Client {
	t.Helper()
	gc, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{PluginName: e})
	raw, err := gc.Dispense(PluginName)
	if err != nil {
		t.Fatal(err)
	}
	c := raw.(*Client)
	t.Cleanup(func() {
		c.Close()
		gc.Close()
	})
	return c
}

// answerEmpty answers every call with an empty object.
func answerEmpty(context.Context, *HookCall) (*HookReply, error) {
	return &HookReply{Payload: []byte(`{}`)}, nil
}

// eventually waits until cond holds, failing the test after 10 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for until := time.Now().Add(10 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("%s: not within 10 seconds", what)
		}
	}
}

// collected waits until a full collection has freed every Client of ps,
// failing the test after 10 seconds.
func collected(t *testing.T, what string, ps []weak.Pointer[Client]) {
	t.Helper()
	eventually(t, what, func() bool {
		runtime.GC()
		for _, p := range ps {
			if p.Value() != nil {
				return false
			}
		}
		return true
	})
}

// TestClientGenerationKeepsNoClient checks that the engine's generation,
// while it goes on, keeps no Client alive: Clients dispensed and never
// used for hooks are collected, and so are, on go-plugin's test
// connection, a Client closed after a run with hooks and a Client whose
// first listen failed.
func TestClientGenerationKeepsNoClient(t *testing.T) {
	generation, end := context.WithCancel(context.Background())
	defer end()
	var plain []weak.Pointer[Client]
	for range 100 {
		raw, err := (&RunnerPlugin{}).GRPCClient(generation, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		plain = append(plain, weak.Make(raw.(*Client)))
	}
	collected(t, "the Clients never used for hooks collected", plain)

	gc, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{PluginName: &echoEngine{generation: generation}})
	defer gc.Close()
	hooked := func() weak.Pointer[Client] {
		raw, err := gc.Dispense(PluginName)
		if err != nil {
			t.Fatal(err)
		}
		c := raw.(*Client)
		if res, err := c.RunWithHooks(context.Background(), &RunRequest{Path: "f/x"}, []byte(`{"on_run_end": true}`), answerEmpty); err != nil || res.GetStatus() != "success" {
			t.Fatalf("the run with hooks: %v, %v", res, err)
		}
		c.Close()
		return weak.Make(c)
	}()
	collected(t, "the Client closed after a run with hooks collected", []weak.Pointer[Client]{hooked})

	tmp, missing := os.Getenv("TMPDIR"), filepath.Join(t.TempDir(), "missing")
	failed := func() weak.Pointer[Client] {
		raw, err := gc.Dispense(PluginName)
		if err != nil {
			t.Fatal(err)
		}
		c := raw.(*Client)
		// go-plugin makes each listener's socket in os.TempDir.
		t.Setenv("TMPDIR", missing)
		defer t.Setenv("TMPDIR", tmp)
		if _, err := c.RunWithHooks(context.Background(), &RunRequest{Path: "f/x"}, []byte(`{"on_run_end": true}`), answerEmpty); !errors.Is(err, ErrNotStarted) {
			t.Fatalf("a run whose broker connection cannot listen: %v, want ErrNotStarted", err)
		}
		return weak.Make(c)
	}()
	collected(t, "the Client whose first listen failed collected", []weak.Pointer[Client]{failed})
}

// TestClientListenRetry checks a first run with hooks whose broker
// connection go-plugin cannot listen on, its socket's directory missing:
// the run is refused without reaching the engine and leaves no
// registration, and the next run on the same Client opens the connection
// again and is served.
func TestClientListenRetry(t *testing.T) {
	engine := &echoEngine{}
	c := echoClient(t, engine)
	tmp := os.Getenv("TMPDIR")
	// go-plugin makes each listener's socket in os.TempDir.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	var heard atomic.Int32
	h := func(context.Context, *HookCall) (*HookReply, error) {
		heard.Add(1)
		return &HookReply{Payload: []byte(`{}`)}, nil
	}
	if _, err := c.RunWithHooks(context.Background(), &RunRequest{Path: "f/x"}, []byte(`{"on_run_end": true}`), h); !errors.Is(err, ErrNotStarted) {
		t.Errorf("a run whose broker connection cannot listen: %v, want ErrNotStarted", err)
	}
	if n := engine.runs.Load(); n != 0 {
		t.Errorf("the refused run reached the engine %d times", n)
	}
	if n := c.Registrations(); n != 0 {
		t.Errorf("%d registrations after the refused run, want 0", n)
	}
	t.Setenv("TMPDIR", tmp)
	if res, err := c.RunWithHooks(context.Background(), &RunRequest{Path: "f/x"}, []byte(`{"on_run_end": true}`), h); err != nil || res.GetStatus() != "success" {
		t.Errorf("the next run: %v, %v; want success", res, err)
	}
	if n := heard.Load(); n != 1 {
		t.Errorf("the handler heard %d calls, want the next run's onRunEnd", n)
	}
}

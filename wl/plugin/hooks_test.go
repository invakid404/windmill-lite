package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

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

func (r *lookupRunner) GetRun(_ context.Context, req *GetRunRequest, _ ...grpc.CallOption) (*GetRunResponse, error) {
	r.asked <- req
	switch a := (<-r.answers).(type) {
	case error:
		return nil, a
	default:
		return &GetRunResponse{State: a.(string)}, nil
	}
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
	reg := &registration{token: "t1", done: make(chan struct{})}
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
// answers plugin_error; a call whose run or token names no registration
// answers unknown_run without reaching a handler; the release call drops
// only the registration whose token it carries.
func TestClientHookCalls(t *testing.T) {
	c := newClient(nil, nil, DefaultReplyMargin)
	var heard []string
	c.hooks.regs["r1"] = &registration{token: "t1", done: make(chan struct{}), handler: func(_ context.Context, call *HookCall) (*HookReply, error) {
		heard = append(heard, call.GetHook())
		if call.GetHook() == "on_run_end" {
			return nil, errors.New("the host failed")
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
	for _, rt := range [][2]string{{"r1", "old"}, {"r2", "t1"}} {
		if rep := call("intercept", rt[0], rt[1]); rep.GetError().GetCode() != HookCodeUnknownRun {
			t.Errorf("a call of run %s, token %s: %v, want unknown_run", rt[0], rt[1], rep)
		}
	}
	call(releasedHook, "r1", "old")
	if c.Registrations() != 1 {
		t.Error("a release call with another token dropped the registration")
	}
	call(releasedHook, "r1", "t1")
	if c.Registrations() != 0 {
		t.Error("the release call did not drop the registration")
	}
	if len(heard) != 2 {
		t.Errorf("the handler heard %v, want the two calls of its run and token", heard)
	}
}

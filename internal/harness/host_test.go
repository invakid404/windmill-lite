package harness

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// These tests play the host's side of host mode (ADR 0004 host mode) on a
// socketpair, so they need no Bun; cmd/windmill-lite runs host mode end to
// end with the Bun host SDK.

const fakeHostPID = 4242

type fakeHost struct {
	t    *testing.T
	nc   net.Conn
	msgs chan Message
	mu   sync.Mutex // serialises writes
}

func socketConns(t *testing.T) (engineSide, hostSide net.Conn) {
	t.Helper()
	a, b, err := socketpair()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer b.Close()
	ea, err := net.FileConn(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := net.FileConn(b)
	if err != nil {
		t.Fatal(err)
	}
	return ea, hb
}

// attach starts AttachHost with cfg on one end of a socketpair and greets
// it as a host with hello; it returns the fake host and AttachHost's
// result.
func attach(t *testing.T, cfg HostConfig, hello Hello) (*fakeHost, *Process, error) {
	t.Helper()
	ec, hc := socketConns(t)
	h := &fakeHost{t: t, nc: hc, msgs: make(chan Message, 64)}
	go func() {
		r := bufio.NewReader(hc)
		defer close(h.msgs)
		for {
			payload, err := readFrame(r, MaxFrame)
			if err != nil {
				return
			}
			m, err := Decode(payload)
			if err != nil {
				t.Errorf("fake host: %v", err)
				return
			}
			h.msgs <- m
			if w, ok := m.(Welcome); ok && w.Reject != nil {
				// A refused host exits, as the SDK's does.
				hc.Close()
				return
			}
		}
	}()
	t.Cleanup(func() { hc.Close() })
	cfg.Conn = ec
	if cfg.Kind == "" {
		cfg.Kind, cfg.Runtime, cfg.Bundle = "bun", PinnedBun, "sha256:b"
	}
	if cfg.Diagnostics == nil {
		cfg.Diagnostics = &syncBuffer{}
	}
	if cfg.getppid == nil {
		cfg.getppid = func() int { return fakeHostPID }
	}
	if cfg.signal == nil {
		// The fake host's pid is no process of the test's: nothing may
		// signal it.
		cfg.signal = func(pid int) error {
			t.Errorf("signal to pid %d", pid)
			return nil
		}
	}
	type res struct {
		p   *Process
		err error
	}
	done := make(chan res, 1)
	go func() {
		p, err := AttachHost(context.Background(), cfg)
		done <- res{p, err}
	}()
	h.send(hello)
	r := <-done
	if r.p != nil {
		t.Cleanup(func() {
			hc.Close()
			<-r.p.Done()
		})
	}
	return h, r.p, r.err
}

func hostHello() Hello {
	return Hello{Proto: ProtocolVersion, Harness: HarnessInfo{Kind: "bun", Version: "dev"}, Runtime: PinnedBun, Caps: Caps{Fetch: true, Host: true}, Bundle: "sha256:b", PID: fakeHostPID}
}

func (h *fakeHost) send(m Message) {
	h.t.Helper()
	payload, err := Encode(m)
	if err != nil {
		h.t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.nc.Write(appendFrame(nil, payload)); err != nil {
		h.t.Errorf("fake host write: %v", err)
	}
}

// expect returns the next message that is not a ping, pong or welcome
// unless t is one of those.
func (h *fakeHost) expect(t string) Message {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-h.msgs:
			if !ok {
				h.t.Fatalf("the engine closed the connection; want %s", t)
			}
			if m.Type() == t {
				return m
			}
		case <-deadline:
			h.t.Fatalf("no %s within 5 s", t)
		}
	}
}

func (h *fakeHost) closed(within time.Duration) bool {
	deadline := time.After(within)
	for {
		select {
		case _, ok := <-h.msgs:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func echoRun(ctx context.Context, p *Process, r RunRequest) RunDone {
	out, err := p.Execute(ctx, engine.Invocation{ID: "inv-" + r.ID, Run: r.ID, Script: "s", Entry: "main", Args: r.Args, Boundary: engine.BoundaryBun})
	if err != nil {
		return RunDone{Status: "failure", Result: jsonval.FromProducer([]byte(`{"error":"` + err.Error() + `"}`))}
	}
	return RunDone{Status: "success", Result: out.Body}
}

// ADR 0004 host mode item 4: in host mode the engine is the host's child,
// so the process that greets it must be its parent, which its watchdog
// would signal; with the watchdog off (F3b) that check, the watchdog's,
// does not apply. A host must offer the host capability.
func TestHostHandshake(t *testing.T) {
	for _, tc := range []struct {
		name      string
		edit      func(*Hello)
		ppid      int
		killAfter time.Duration
		code      string
		works     bool
	}{
		{"parent", func(*Hello) {}, fakeHostPID, 0, "", true},
		{"not the parent", func(*Hello) {}, fakeHostPID + 1, 0, RejectPID, false},
		{"not the parent, no watchdog", func(*Hello) {}, fakeHostPID + 1, WatchdogOff, "", true},
		{"no host capability", func(h *Hello) { h.Caps.Host = false }, fakeHostPID, 0, RejectHarness, false},
		{"runtime", func(h *Hello) { h.Runtime = "1.4.2" }, fakeHostPID, 0, RejectRuntime, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hello := hostHello()
			tc.edit(&hello)
			h, p, err := attach(t, HostConfig{Run: echoRun, KillAfter: tc.killAfter, getppid: func() int { return tc.ppid }}, hello)
			w := h.expect(TypeWelcome).(Welcome)
			if tc.works {
				if err != nil || w.Config == nil || w.Config.ModuleState != "warm" || p.PID() != fakeHostPID {
					t.Fatalf("%v, welcome %+v", err, w)
				}
				return
			}
			var he *HandshakeError
			if !errors.As(err, &he) || he.Code != tc.code || w.Reject == nil || w.Reject.Code != tc.code {
				t.Fatalf("got %v, welcome %+v; want %s", err, w, tc.code)
			}
		})
	}
}

// A run frame starts a run whose invocations go to the host, which is the
// harness; run_done carries the outcome under the run's id.
func TestHostRun(t *testing.T) {
	h, _, err := attach(t, HostConfig{Run: echoRun}, hostHello())
	if err != nil {
		t.Fatal(err)
	}
	h.expect(TypeWelcome)
	h.send(RunRequest{ID: "r1", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{"a":1}`))})
	inv := h.expect(TypeInvoke).(Invoke)
	if inv.ID != "inv-r1" || inv.Run != "r1" || string(inv.Args.Producer()) != `{"a":1}` {
		t.Fatalf("invoke %+v", inv)
	}
	h.send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: jsonval.FromProducer([]byte(`"done"`)), Settled: true})
	d := h.expect(TypeRunDone).(RunDone)
	if d.ID != "r1" || d.Status != "success" || string(d.Result.Producer()) != `"done"` {
		t.Fatalf("run_done %+v", d)
	}
}

// run_cancel ends the run's context with cause canceled; the invocation in
// flight is abandoned with a cancel frame, and the host process is not
// touched (host mode item 1).
func TestHostRunCancel(t *testing.T) {
	causes := make(chan error, 1)
	h, p, err := attach(t, HostConfig{Run: func(ctx context.Context, p *Process, r RunRequest) RunDone {
		_, err := p.Execute(ctx, engine.Invocation{ID: "i", Run: r.ID, Script: "s", Entry: "main", Args: r.Args})
		causes <- err
		return RunDone{Status: "canceled"}
	}}, hostHello())
	if err != nil {
		t.Fatal(err)
	}
	h.send(RunRequest{ID: "r1", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{}`))})
	h.expect(TypeInvoke)
	h.send(RunCancel{ID: "r1"})
	if c := h.expect(TypeCancel).(Cancel); c.ID != "i" || c.Reason != "canceled" {
		t.Fatalf("cancel %+v", c)
	}
	if err := <-causes; !errors.Is(err, engine.CancelCanceled) {
		t.Fatalf("Execute returned %v", err)
	}
	if d := h.expect(TypeRunDone).(RunDone); d.Status != "canceled" || !d.Result.IsZero() {
		t.Fatalf("run_done %+v", d)
	}
	if in, ab := p.Load(); in != 0 || ab != 1 {
		t.Fatalf("load %d in flight, %d abandoned; want the abandoned one counted", in, ab)
	}
}

// Host mode item 6: when the host's connection ends, its runs are lost
// with it: an invocation in flight fails with ErrHostLost and settles with
// the process, the run's context ends, and the engine is done. The run's
// context has its cause before the invocation is failed, so a run reads
// it whichever wakes it first, here also with a gap between the two
// (FU7: the run read a nil cause when the outcome came first).
func TestHostLost(t *testing.T) {
	for _, gap := range []time.Duration{0, 20 * time.Millisecond} {
		t.Run(fmt.Sprint("gap ", gap), func(t *testing.T) {
			old := hostLostGap
			hostLostGap = func() { time.Sleep(gap) }
			t.Cleanup(func() { hostLostGap = old })
			testHostLost(t)
		})
	}
}

func testHostLost(t *testing.T) {
	type got struct{ exec, ctx error }
	results := make(chan got, 1)
	rep := newRecorder()
	h, p, err := attach(t, HostConfig{Run: func(ctx context.Context, p *Process, r RunRequest) RunDone {
		_, err := p.Execute(ctx, engine.Invocation{ID: "i", Run: r.ID, Script: "s", Entry: "main", Args: r.Args, Report: rep})
		results <- got{err, context.Cause(ctx)}
		return RunDone{Status: "failure"}
	}}, hostHello())
	if err != nil {
		t.Fatal(err)
	}
	h.send(RunRequest{ID: "r1", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{}`))})
	h.expect(TypeInvoke)
	h.nc.Close()
	r := <-results
	if !errors.Is(r.exec, ErrHostLost) || !errors.Is(r.ctx, ErrHostLost) {
		t.Fatalf("Execute %v, run context %v", r.exec, r.ctx)
	}
	if k := rep.waitSettled(t, time.Second); k != engine.SettleProcessExited {
		t.Fatalf("settled %s", k)
	}
	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done did not close")
	}
}

// The watchdog of host mode (item 4, review item cl P4-3): when no frame
// has come from the host for killAfter, the engine names the runs in
// flight, sends SIGKILL to the host's pid only while the host is still its
// parent, and ends.
func TestHostWatchdog(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reparent bool
	}{
		{"signals its parent", false},
		// Once the host has exited, the engine is reparented and the pid may
		// already belong to another process: it is not signalled.
		{"spares a pid that is no longer its parent", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var silent atomic.Bool
			signalled := make(chan int, 1)
			diag := &syncBuffer{}
			h, p, err := attach(t, HostConfig{
				Run: func(ctx context.Context, p *Process, r RunRequest) RunDone {
					p.Execute(ctx, engine.Invocation{ID: "i", Run: r.ID, Script: "s", Entry: "main", Args: r.Args})
					return RunDone{Status: "failure"}
				},
				KillAfter:   300 * time.Millisecond,
				Heartbeat:   50 * time.Millisecond,
				Diagnostics: diag,
				getppid: func() int {
					calls.Add(1)
					if tc.reparent && silent.Load() {
						return 1
					}
					return fakeHostPID
				},
				signal: func(pid int) error {
					signalled <- pid
					return nil
				},
			}, hostHello())
			if err != nil {
				t.Fatal(err)
			}
			h.send(RunRequest{ID: "r1", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{}`))})
			h.expect(TypeInvoke)
			// The host answers pings for twice killAfter: frames it sends
			// keep the watchdog quiet.
			for range 12 {
				h.send(Ping{ID: "h", Health: &Health{InFlight: 1}})
				time.Sleep(50 * time.Millisecond)
			}
			select {
			case <-p.Done():
				t.Fatal("the watchdog fired while the host was sending frames")
			default:
			}
			silent.Store(true)
			start := time.Now()
			select {
			case <-p.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("the watchdog did not end the engine's side")
			}
			if d := time.Since(start); d > 300*time.Millisecond+time.Second {
				t.Fatalf("ended %v after the host went silent", d)
			}
			select {
			case pid := <-signalled:
				if tc.reparent || pid != fakeHostPID {
					t.Fatalf("signalled pid %d", pid)
				}
			default:
				if !tc.reparent {
					t.Fatal("the host was not signalled")
				}
			}
			out := diag.String()
			if !strings.Contains(out, "in-flight runs: r1") || (tc.reparent != strings.Contains(out, "not signalling")) {
				t.Fatalf("diagnostics:\n%s", out)
			}
			if !h.closed(time.Second) {
				t.Fatal("the connection stayed open")
			}
		})
	}
}

// A spawned harness cannot submit runs: run frames are host mode's.
func TestRunFramesNeedHostMode(t *testing.T) {
	var p Process
	if err := p.onRun(RunRequest{ID: "r"}); !IsProtocolError(err, CodeUnexpected) {
		t.Fatalf("got %v", err)
	}
}

// A host that goes away before its hello, or stays silent past the
// handshake timeout, is an error of AttachHost, not a crash of the engine.
func TestHostGoneBeforeHello(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close bool
	}{{"closes", true}, {"silent", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ec, hc := socketConns(t)
			defer hc.Close()
			if tc.close {
				hc.Close()
			}
			_, err := AttachHost(context.Background(), HostConfig{
				Conn: ec, Kind: "bun", Runtime: PinnedBun, Bundle: "b", Run: echoRun,
				HandshakeTimeout: 200 * time.Millisecond, Diagnostics: &syncBuffer{},
			})
			if err == nil || !strings.Contains(err.Error(), "host") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// One bad answer of a platform handler must not end the harness and every
// invocation in it: a status no Response can carry, or a panic in either
// part of the handler, becomes a 502 the step's client sees.
func TestPlatformAnswerGuardsTheHarness(t *testing.T) {
	req := PlatformRequest{ID: "p1", Inv: "i"}
	answer := func(rep PlatformReply) PlatformHandler {
		return func(PlatformRequest) func(context.Context) PlatformReply {
			return func(context.Context) PlatformReply { return rep }
		}
	}
	for name, h := range map[string]PlatformHandler{
		"zero reply": answer(PlatformReply{}),
		"status 101": answer(PlatformReply{Status: 101}),
		"panic":      func(PlatformRequest) func(context.Context) PlatformReply { panic("boom") },
		"panic later": func(PlatformRequest) func(context.Context) PlatformReply {
			return func(context.Context) PlatformReply { panic("boom") }
		},
		"no answer":   func(PlatformRequest) func(context.Context) PlatformReply { return nil },
		"no handler":  nil,
		"good answer": answer(PlatformReply{Status: 204}),
	} {
		rep := platformAnswer(context.Background(), platformAccept(h, req), req)
		want := 502
		switch name {
		case "no handler":
			want = 501
		case "good answer":
			want = 204
		}
		if rep.ID != "p1" || rep.Status != want {
			t.Errorf("%s: %+v, want status %d", name, rep, want)
		}
		if _, err := Encode(rep); err != nil {
			t.Errorf("%s: the reply does not encode: %v", name, err)
		}
	}
	if _, err := Encode(PlatformReply{ID: "p", Status: 0}); !IsProtocolError(err, CodeBadField) {
		t.Fatalf("a reply with status 0 encoded: %v", err)
	}
}

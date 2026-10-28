package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

type testRun struct {
	latch Latch
}

func (r *testRun) ID() string      { return "run-1" }
func (r *testRun) Latch(err error) { r.latch.Set(err) }

func TestNoHooksChangeNothing(t *testing.T) {
	ctx := context.Background()
	var h Hooks = NoHooks{}
	r := &testRun{}
	s := Site{Path: conformance.SitePath{}.Child(conformance.Module("a")), Flow: "f/demo/flow", Module: "a", Script: "f/demo/flow/a"}
	args := jsonval.FromProducer([]byte(`{"x":1}`))

	if out, ok, err := h.Intercept(ctx, r, s, args); ok || err != nil || out.Kind != "" {
		t.Errorf("Intercept = %+v, %v, %v; want a decline", out, ok, err)
	}
	got, err := h.RewriteArgs(ctx, r, s, args)
	if err != nil || string(got.Producer()) != `{"x":1}` {
		t.Errorf("RewriteArgs = %s, %v; want the arguments unchanged", got, err)
	}
	if h.WriteCapable(s) || h.FatalErrorName("Error") || h.HaltBefore(s) || h.HaltAfter(s) {
		t.Error("a no-op hook marked, halted or made something fatal")
	}
	if err := h.BeforeFirstWrite(ctx, r, s); err != nil {
		t.Errorf("BeforeFirstWrite vetoed: %v", err)
	}
	if err := h.OnModuleStart(ctx, r, s); err != nil {
		t.Error(err)
	}
	if err := h.OnModuleEnd(ctx, r, s, StepRecord{Site: s.Path}); err != nil {
		t.Error(err)
	}
	env := map[string]string{"WM_JOB_ID": "j"}
	if err := h.InvocationEnv(ctx, r, s, env); err != nil || len(env) != 1 || env["WM_JOB_ID"] != "j" {
		t.Errorf("InvocationEnv changed the environment: %v, %v", env, err)
	}
	if w := h.OnRunEnd(ctx, &Trace{}); w != nil {
		t.Errorf("OnRunEnd added warnings: %v", w)
	}
	h.OnRunSettled(&Trace{})
	if r.latch.Err() != nil {
		t.Errorf("a no-op hook latched: %v", r.latch.Err())
	}
}

func TestHookLatchError(t *testing.T) {
	live := context.Background()
	boom := errors.New("evidence store unreachable")

	canceled, cancel := context.WithCancelCause(context.Background())
	cancel(CancelCanceled)
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()

	for name, tc := range map[string]struct {
		ctx     context.Context
		err     error
		latches error
	}{
		"nil error":                          {live, nil, nil},
		"hook failure":                       {live, boom, boom},
		"hook failure after cancellation":    {canceled, boom, boom},
		"run's cancellation cause":           {canceled, fmt.Errorf("plugin: %w", CancelCanceled), nil},
		"run's context error":                {canceled, context.Canceled, nil},
		"run's deadline":                     {deadline, fmt.Errorf("host_call: %w", context.DeadlineExceeded), nil},
		"a cancellation error on a live run": {live, context.Canceled, context.Canceled},
	} {
		t.Run(name, func(t *testing.T) {
			if got := HookLatchError(tc.ctx, tc.err); !errors.Is(got, tc.latches) || (got == nil) != (tc.latches == nil) {
				t.Errorf("HookLatchError = %v, want %v", got, tc.latches)
			}
		})
	}
}

func TestLatchFirstErrorWins(t *testing.T) {
	var l Latch
	if l.Err() != nil {
		t.Fatal("zero Latch is latched")
	}
	if l.Set(nil) || l.Err() != nil {
		t.Fatal("a nil error latched")
	}
	first, second := errors.New("deadline evidence missing"), errors.New("later")
	if !l.Set(first) {
		t.Fatal("first Set did not latch")
	}
	if l.Set(second) {
		t.Fatal("second Set latched")
	}
	if !errors.Is(l.Err(), first) {
		t.Fatalf("Err() = %v, want the first error", l.Err())
	}
}

func TestLatchUnderConcurrency(t *testing.T) {
	var l Latch
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Set(fmt.Errorf("latch %d", i)) {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d calls latched, want exactly 1", wins.Load())
	}
	if l.Err() == nil {
		t.Fatal("nothing latched")
	}
}

func TestRealClockWait(t *testing.T) {
	var c Clock = RealClock{}
	if err := c.Wait(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("Wait = %v", err)
	}
	if err := c.Wait(context.Background(), 0); err != nil {
		t.Fatalf("Wait(0) = %v", err)
	}

	// A cancellation ends a wait at once, with the cause the engine set
	// (FX-23, ADR 0004: sleeps and retry delays are timers on the run).
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel(CancelCanceled)
	}()
	start := time.Now()
	err := c.Wait(ctx, time.Hour)
	if !errors.Is(err, CancelCanceled) {
		t.Fatalf("Wait = %v, want the cancellation cause", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Wait did not end promptly")
	}
	// An ended context ends a wait before it starts.
	if err := c.Wait(ctx, time.Hour); !errors.Is(err, CancelCanceled) {
		t.Fatalf("Wait on an ended context = %v", err)
	}
}

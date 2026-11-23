package wl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// causeFlows are synthetic flows that wait: f/t/wait sleeps 30 s after its
// first module, and f/t/timeout does the same under its own limit of 1 s
// (FX-52).
var causeFlows = map[string]string{
	"f/t/echo": testFlows["f/t/echo"],
	"f/t/wait": `{"modules":[{"id":"a","value":{"type":"identity"}},{"id":"b","value":{"type":"identity"},"sleep":{"type":"static","value":30}}]}`,
	"f/t/timeout": `{"modules":[{"id":"a","value":{"type":"identity"}},{"id":"b","value":{"type":"identity"},"sleep":{"type":"static","value":30}}],` +
		`"timeout":1}`,
}

// A canceled run's Result names its cause, besides the status canceled:
// the context's deadline, a pre-write deadline that has passed, the
// context's cancellation with a *Cancellation, Close, and the flow's own
// limit. A run that ends otherwise names none.
func TestRunCancelCause(t *testing.T) {
	rt, err := Open(writeBundleOf(t, causeFlows, nil, Version(), nil, nil), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	// started reports, on the channel it returns, once the run's first
	// module has ended, so the run is in flight.
	started := func() (*Hooks, <-chan struct{}) {
		ch := make(chan struct{})
		return &Hooks{OnModuleEnd: func(_ context.Context, _ RunHandle, s Site, _ StepRecord) error {
			if s.Module == "a" {
				close(ch)
			}
			return nil
		}}, ch
	}
	for _, tc := range []struct {
		name, flow string
		run        func(t *testing.T, flow string) (*Result, error)
		cause      string
	}{
		{"the context's deadline", "f/t/wait", func(t *testing.T, flow string) (*Result, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			return rt.Run(ctx, flow, nil, RunOptions{})
		}, CauseDeadline},
		{"a pre-write deadline that has passed", "f/t/echo", func(t *testing.T, flow string) (*Result, error) {
			return rt.Run(context.Background(), flow, nil, RunOptions{PreWriteDeadline: time.Now().Add(-time.Second)})
		}, CausePreWriteDeadline},
		{"a cancellation", "f/t/wait", func(t *testing.T, flow string) (*Result, error) {
			ctx, cancel := context.WithCancelCause(context.Background())
			h, in := started()
			go func() {
				<-in
				cancel(&Cancellation{Reason: "the tenant was suspended", Canceler: "tenant-admin"})
			}()
			return rt.Run(ctx, flow, nil, RunOptions{Hooks: h})
		}, CauseCanceled},
		{"the flow's own limit", "f/t/timeout", func(t *testing.T, flow string) (*Result, error) {
			return rt.Run(context.Background(), flow, nil, RunOptions{})
		}, CauseFlowTimeout},
		{"no cancellation", "f/t/echo", func(t *testing.T, flow string) (*Result, error) {
			return rt.Run(context.Background(), flow, json.RawMessage(`{"x":1}`), RunOptions{})
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.run(t, tc.flow)
			if err != nil {
				t.Fatal(err)
			}
			status := StatusCanceled
			if tc.cause == "" {
				status = StatusSuccess
			}
			if res.Status != status || res.CancelCause != tc.cause {
				t.Fatalf("status %s, cause %q (result %s); want %s, cause %q", res.Status, res.CancelCause, res.Result, status, tc.cause)
			}
		})
	}
	t.Run("Close", func(t *testing.T) {
		h, in := started()
		type outcome struct {
			res *Result
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			res, err := rt.Run(context.Background(), "f/t/wait", nil, RunOptions{Hooks: h})
			done <- outcome{res, err}
		}()
		<-in
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
		o := <-done
		if o.err != nil || o.res.Status != StatusCanceled || o.res.CancelCause != CauseClosing {
			t.Fatalf("%+v, %v; want canceled, cause %q", o.res, o.err, CauseClosing)
		}
	})
}

// A root script's run ends canceled when its context ends, with the
// context's cause: one whose deadline has passed before it starts, with the
// cause deadline, and one that its caller cancels while its step waits,
// with the cause canceled. Its result stays the killed job's error of a
// script job, which names the cancellation's reason and canceler, not a
// flow's Canceled error. A run with time left that is not canceled names
// no cause.
func TestScriptRunCancelCause(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBundle(t, bun)
	// f/t/set sets u/t/v to "second", then waits a minute when it read
	// "slow".
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Variables: map[string]Variable{"u/t/v": {Value: "slow"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	check := func(t *testing.T, res *Result, err error, cause, killed string) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusCanceled || res.CancelCause != cause {
			t.Fatalf("status %s, cause %q (result %s); want canceled, cause %q", res.Status, res.CancelCause, res.Result, cause)
		}
		var e struct {
			Error struct{ Name, Message string } `json:"error"`
		}
		if json.Unmarshal(res.Result, &e) != nil || e.Error.Name != "ExecutionErr" || !strings.Contains(e.Error.Message, killed) {
			t.Fatalf("result %s; want the killed job's ExecutionErr, %q", res.Result, killed)
		}
	}
	t.Run("a deadline that has passed", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		res, err := rt.Run(ctx, "f/t/set", json.RawMessage(`{"v":"u/t/v"}`), RunOptions{Kind: KindScript})
		check(t, res, err, CauseDeadline, "cancelled by unknown (reason: no reason given)")
	})
	t.Run("the caller's cancellation while the step waits", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		type outcome struct {
			res *Result
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			res, err := rt.Run(ctx, "f/t/set", json.RawMessage(`{"v":"u/t/v"}`), RunOptions{Kind: KindScript})
			done <- outcome{res, err}
		}()
		// The step has read "slow" and set the variable, and waits.
		deadline := time.After(10 * time.Second)
		for {
			if v, err := rt.services.VariableValue(context.Background(), "u/t/v"); err != nil {
				t.Fatal(err)
			} else if v == "second" {
				break
			}
			select {
			case <-deadline:
				t.Fatal("the step never updated the variable")
			case <-time.After(10 * time.Millisecond):
			}
		}
		// The update's answer reaches the step after the store changed.
		time.Sleep(100 * time.Millisecond)
		cancel(&Cancellation{Reason: "the tenant was suspended", Canceler: "tenant-admin"})
		select {
		case o := <-done:
			check(t, o.res, o.err, CauseCanceled, "cancelled by tenant-admin (reason: the tenant was suspended)")
		case <-time.After(10 * time.Second):
			t.Fatal("the canceled run did not return")
		}
	})
	// The same script with time left runs, and reads the canceled run's
	// update.
	if res, err := rt.Run(context.Background(), "f/t/set", json.RawMessage(`{"v":"u/t/v"}`), RunOptions{Kind: KindScript}); err != nil || res.Status != StatusSuccess || res.CancelCause != "" || string(res.Result) != `"second"` {
		t.Fatalf("%+v, %v; want success, \"second\", with no cause", res, err)
	}
}

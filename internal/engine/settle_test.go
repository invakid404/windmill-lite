package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// fakeHarness is a StepExecutor that follows the settlement contract: a
// call it completes settles before Execute returns, a call that never
// reached it settles as not started, and an abandoned call, or one whose
// outcome it fixed early, keeps running until the test reports how it ended
// or ends its process.
type fakeHarness struct {
	mu      sync.Mutex
	running map[string]*fakeCall
	started chan string
}

type fakeCall struct {
	inv    Invocation
	result chan StepOutcome
	// early fixes the outcome while the entrypoint call goes on, as an
	// attributed process-level error does (ADR 0004 failure model item 2).
	early chan StepOutcome
}

func newFakeHarness() *fakeHarness {
	return &fakeHarness{running: map[string]*fakeCall{}, started: make(chan string, 8)}
}

func (h *fakeHarness) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	if ctx.Err() != nil {
		inv.Report.Settled(SettleNotStarted)
		return StepOutcome{}, context.Cause(ctx)
	}
	c := &fakeCall{inv: inv, result: make(chan StepOutcome), early: make(chan StepOutcome)}
	h.mu.Lock()
	h.running[inv.ID] = c
	h.mu.Unlock()
	h.started <- inv.ID
	select {
	case out := <-c.result:
		h.take(inv.ID)
		inv.Report.Settled(SettleCompleted)
		return out, nil
	case out := <-c.early:
		// The outcome is fixed, but the call is still pending: no Settled.
		return out, nil
	case <-ctx.Done():
		// Abandoned (D2): the call goes on inside the harness.
		return StepOutcome{}, context.Cause(ctx)
	}
}

func (h *fakeHarness) take(id string) *fakeCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.running[id]
	delete(h.running, id)
	return c
}

func (h *fakeHarness) peek(id string) *fakeCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running[id]
}

// detached reports an attributed error from work a call left running.
func (h *fakeHarness) detached(id string) {
	h.peek(id).inv.Report.Late(Late{Kind: LateError, Body: jsonval.FromProducer([]byte(`{"message":"timer threw"}`))})
}

// ends reports that an abandoned call's entrypoint ended, with a late frame
// whose settled flag is set (ADR 0004).
func (h *fakeHarness) ends(id string) {
	c := h.take(id)
	c.inv.Report.Late(Late{Kind: LateError, Body: jsonval.FromProducer([]byte(`{"message":"gave up"}`)), Final: true})
	c.inv.Report.Settled(SettleCompleted)
}

// exitProcess ends the harness process: every call still in it settles.
func (h *fakeHarness) exitProcess() {
	h.mu.Lock()
	calls := h.running
	h.running = map[string]*fakeCall{}
	h.mu.Unlock()
	for _, c := range calls {
		c.inv.Report.Settled(SettleProcessExited)
	}
}

// recorder keeps one invocation's Reporter calls in order.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) Late(l Late)          { r.add(fmt.Sprintf("late %s final=%v", l.Kind, l.Final)) }
func (r *recorder) Warning(w Warning)    { r.add("warning " + w.Code) }
func (r *recorder) Settled(k SettleKind) { r.add("settled " + string(k)) }

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func TestRunSettlesOnlyWhenEveryInvocationHasSettled(t *testing.T) {
	var fired atomic.Int32
	run := NewRunSettlement(func() { fired.Add(1) })
	h := newFakeHarness()
	recs := map[string]*recorder{}
	reps := map[string]Reporter{}
	invoke := func(ctx context.Context, id string) <-chan error {
		recs[id] = &recorder{}
		reps[id] = run.Track(recs[id])
		inv := Invocation{ID: id, Run: "r", Report: reps[id]}
		done := make(chan error, 1)
		go func() {
			_, err := h.Execute(ctx, inv)
			done <- err
		}()
		return done
	}

	// a: completes normally, and has settled by the time Execute returns.
	doneA := invoke(context.Background(), "a")
	<-h.started
	h.peek("a").result <- StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`1`))}
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	if got := recs["a"].get(); len(got) != 1 || got[0] != "settled completed" {
		t.Fatalf("a: %v, want settled before Execute returned", got)
	}

	// b: abandoned on its step timeout; the call goes on.
	ctxB, cancelB := context.WithCancelCause(context.Background())
	doneB := invoke(ctxB, "b")
	<-h.started
	cancelB(CancelTimeout)
	if err := <-doneB; !errors.Is(err, CancelTimeout) {
		t.Fatalf("b: Execute = %v, want the timeout cause", err)
	}

	// c: abandoned when the run ends.
	ctxC, cancelC := context.WithCancelCause(context.Background())
	doneC := invoke(ctxC, "c")
	<-h.started
	cancelC(CancelRunEnded)
	<-doneC

	// d: the run was canceled before d could be dispatched.
	ctxD, cancelD := context.WithCancelCause(context.Background())
	cancelD(CancelCanceled)
	if err := <-invoke(ctxD, "d"); !errors.Is(err, CancelCanceled) {
		t.Fatalf("d: Execute = %v", err)
	}
	if got := recs["d"].get(); len(got) != 1 || got[0] != "settled not_started" {
		t.Fatalf("d: %v", got)
	}

	run.End()
	if fired.Load() != 0 || run.Open() != 2 {
		t.Fatalf("after End: fired %d, open %d; want 0 and 2 (b and c abandoned)", fired.Load(), run.Open())
	}

	// Neither abandonment nor a late error from detached work settles b.
	h.detached("b")
	if fired.Load() != 0 || run.Open() != 2 {
		t.Fatalf("a non-final late error settled something: fired %d, open %d", fired.Load(), run.Open())
	}

	// b's entrypoint call ends: b settles, the run still waits for c.
	h.ends("b")
	if fired.Load() != 0 || run.Open() != 1 {
		t.Fatalf("after b settled: fired %d, open %d", fired.Load(), run.Open())
	}

	// c's process exits: c settles and the run settles, once.
	h.exitProcess()
	if fired.Load() != 1 || run.Open() != 0 {
		t.Fatalf("after the process exit: fired %d, open %d", fired.Load(), run.Open())
	}

	// A second exit, or a duplicate Settled from a faulty executor, changes
	// nothing.
	h.exitProcess()
	reps["c"].Settled(SettleCompleted)
	if fired.Load() != 1 || run.Open() != 0 {
		t.Fatalf("a duplicate settlement changed the run: fired %d, open %d", fired.Load(), run.Open())
	}

	// b's Reporter saw the contract's order: detached output, the final
	// late, then Settled; c saw only its process exit.
	if got, want := fmt.Sprint(recs["b"].get()), "[late error final=false late error final=true settled completed]"; got != want {
		t.Errorf("b: %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(recs["c"].get()), "[settled process_exited]"; got != want {
		t.Errorf("c: %s, want %s", got, want)
	}
}

func TestEscapedOutcomeBeforeTheCallEndsDoesNotSettle(t *testing.T) {
	// A timer of the entrypoint throws while the call awaits other I/O: the
	// attributed error fails the invocation as escaped at once, but the
	// call can still resume and use what the run holds.
	for name, tc := range map[string]struct {
		terminal func(h *fakeHarness)
		events   string
	}{
		"the call ends later": {
			func(h *fakeHarness) { h.ends("e") },
			"[late error final=false late error final=true settled completed]",
		},
		"its process exits first": {
			func(h *fakeHarness) { h.exitProcess() },
			"[late error final=false settled process_exited]",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var fired atomic.Int32
			run := NewRunSettlement(func() { fired.Add(1) })
			h := newFakeHarness()
			rec := &recorder{}
			type result struct {
				out StepOutcome
				err error
			}
			done := make(chan result, 1)
			go func() {
				out, err := h.Execute(context.Background(), Invocation{ID: "e", Run: "r", Report: run.Track(rec)})
				done <- result{out, err}
			}()
			<-h.started
			h.peek("e").early <- StepOutcome{Kind: OutcomeEscaped, ExitCode: 1, Tail: "timer threw"}
			r := <-done
			if r.err != nil || r.out.Kind != OutcomeEscaped {
				t.Fatalf("Execute = %+v, %v; want the escaped outcome at once", r.out, r.err)
			}
			if got := rec.get(); len(got) != 0 {
				t.Fatalf("the escaped outcome settled the call: %v", got)
			}

			run.End()
			if fired.Load() != 0 || run.Open() != 1 {
				t.Fatalf("after End: fired %d, open %d; the pending call holds the run", fired.Load(), run.Open())
			}
			h.detached("e")
			if fired.Load() != 0 {
				t.Fatal("a non-final late error settled the run")
			}

			tc.terminal(h)
			if fired.Load() != 1 || run.Open() != 0 {
				t.Fatalf("after the call's end: fired %d, open %d", fired.Load(), run.Open())
			}
			h.exitProcess()
			if fired.Load() != 1 {
				t.Fatalf("a later process exit settled the run again: fired %d", fired.Load())
			}
			if got := fmt.Sprint(rec.get()); got != tc.events {
				t.Errorf("reporter saw %s, want %s", got, tc.events)
			}
		})
	}
}

func TestRunSettlementIgnoresDuplicatesAndLateOutput(t *testing.T) {
	var fired atomic.Int32
	run := NewRunSettlement(func() { fired.Add(1) })
	rec := &recorder{}
	r := run.Track(rec)
	r.Settled(SettleCompleted)
	run.End()
	if fired.Load() != 1 {
		t.Fatalf("fired %d after the only invocation settled and the run ended", fired.Load())
	}
	r.Settled(SettleProcessExited)
	r.Late(Late{Kind: LateLog})
	if fired.Load() != 1 || run.Open() != 0 {
		t.Fatalf("a duplicate Settled changed the run: fired %d, open %d", fired.Load(), run.Open())
	}
	want := []string{"settled completed", "late log final=false"}
	if got := rec.get(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("forwarded %v, want %v: the duplicate is dropped, later output still reaches the trace", got, want)
	}
}

func TestRunWithNoInvocationsSettlesAtEnd(t *testing.T) {
	var fired atomic.Int32
	run := NewRunSettlement(func() { fired.Add(1) })
	run.End()
	run.End()
	if fired.Load() != 1 {
		t.Fatalf("fired %d times", fired.Load())
	}
}

func TestTrackAfterEndPanics(t *testing.T) {
	run := NewRunSettlement(func() {})
	run.End()
	defer func() {
		if recover() == nil {
			t.Error("Track after End did not panic")
		}
	}()
	run.Track(NopReporter{})
}

func TestConcurrentSettlementFiresOnce(t *testing.T) {
	var fired atomic.Int32
	run := NewRunSettlement(func() { fired.Add(1) })
	reps := make([]Reporter, 64)
	for i := range reps {
		reps[i] = run.Track(NopReporter{})
	}
	var wg sync.WaitGroup
	for _, r := range reps {
		wg.Add(2)
		go func() { defer wg.Done(); r.Settled(SettleCompleted) }()
		go func() { defer wg.Done(); r.Settled(SettleProcessExited) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); time.Sleep(time.Millisecond); run.End() }()
	wg.Wait()
	if fired.Load() != 1 || run.Open() != 0 {
		t.Fatalf("fired %d, open %d", fired.Load(), run.Open())
	}
}

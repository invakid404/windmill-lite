package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// runLimited runs f/t/main with limits on a goroutine.
func runLimited(e *Engine, args string, l RunLimits) <-chan *Trace {
	out := make(chan *Trace, 1)
	go func() {
		tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(args)), Seed: 1, Limits: l})
		if err != nil {
			panic(err)
		}
		out <- tr
	}()
	return out
}

// A run's bound on its invocations in flight: of a parallel branchall of
// four, two run at once, and each of the others starts once one of those
// returns. The trace lists D15.
func TestRunMaxInFlight(t *testing.T) {
	g := newGated()
	g.hold = "/b:"
	held := func(k int) string {
		return branch(skipFailure(false), value(fmt.Sprintf("b%d", k), fmt.Sprintf(`"r%d"`, k), ""))
	}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(branchAllOf("ba", []string{held(0), held(1), held(2), held(3)}, true, ""))}, g)
	out := runLimited(e, `{}`, RunLimits{MaxInFlight: 2})
	running := []string{g.next(t), g.next(t)}
	for range 2 {
		g.none(t)
		g.done(running[0])
		running = append(running[1:], g.next(t))
	}
	g.none(t)
	for _, s := range running {
		g.done(s)
	}
	tr := <-out
	wantStatus(t, tr, conformance.RunSuccess, `["r0","r1","r2","r3"]`)
	if g.peak != 2 {
		t.Fatalf("%d invocations in flight at once, want 2", g.peak)
	}
	if !slices.Contains(tr.Deviations, "D15") {
		t.Fatalf("deviations %v, want D15", tr.Deviations)
	}
	// Without the bound all four run at once, and the trace has no D15.
	g = newGated()
	g.hold = "/b:"
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(branchAllOf("ba", []string{held(0), held(1), held(2), held(3)}, true, ""))}, g)
	out = runLimited(e, `{}`, RunLimits{})
	running = []string{g.next(t), g.next(t), g.next(t), g.next(t)}
	for _, s := range running {
		g.done(s)
	}
	if tr := <-out; g.peak != 4 || slices.Contains(tr.Deviations, "D15") {
		t.Fatalf("unbounded: peak %d, deviations %v", g.peak, tr.Deviations)
	}
}

// A run's bound on the bytes of its stored values: the step whose stored
// result passes it latches the run with ErrRunMemoryLimit, and nothing
// starts after it. Arguments that pass it alone latch the run before its
// first step.
func TestRunHeldBytes(t *testing.T) {
	big := `"` + strings.Repeat("x", 100) + `"`
	flow := flowOf(value("a", big, ""), value("b", big, ""), value("c", big, ""))
	e := mustOpen(t, map[string]string{"f/t/main": flow}, newGated())
	tr := <-runLimited(e, `{}`, RunLimits{MaxHeldBytes: 150})
	if tr.Status != conformance.RunLatched || !errors.Is(tr.Latched, ErrRunMemoryLimit) {
		t.Fatalf("status %s, latched %v; want run_memory_limit", tr.Status, tr.Latched)
	}
	if !at(t, tr, "m:a").Ran || !at(t, tr, "m:b").Ran {
		t.Fatal("the steps up to the one that passed the limit ran")
	}
	noRecord(t, tr, "m:c")
	tr = <-runLimited(e, `{"k":`+big+`,"l":`+big+`}`, RunLimits{MaxHeldBytes: 150})
	if tr.Status != conformance.RunLatched || !errors.Is(tr.Latched, ErrRunMemoryLimit) {
		t.Fatalf("large arguments: status %s, latched %v", tr.Status, tr.Latched)
	}
	noRecord(t, tr, "m:a")
	// Under the limit, the run completes.
	tr = <-runLimited(e, `{}`, RunLimits{MaxHeldBytes: 1000})
	wantStatus(t, tr, conformance.RunSuccess, `{"v":`+big+`}`)
}

// A reservation returns when the invocation that made it settles, not
// when its outcome is fixed: the step after one that returned without
// settling finds its bytes still taken, and gets them once it settles. A
// runtime's total bounds the reservations of every run that shares it.
func TestByteBudgetReturnsAtSettlement(t *testing.T) {
	var (
		unsettled Reporter
		got       []string
	)
	note := func(err error) {
		if err != nil {
			got = append(got, err.Error())
		} else {
			got = append(got, "ok")
		}
	}
	exec := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
		if inv.Budget == nil {
			got = append(got, "no budget")
			defer inv.Report.Settled(SettleCompleted)
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`1`))}, nil
		}
		switch inv.Site.String() {
		case "m:a":
			note(inv.Budget.Reserve(600))
			unsettled = inv.Report
		case "m:b":
			note(inv.Budget.Reserve(600))
			unsettled.Settled(SettleCompleted)
			note(inv.Budget.Reserve(600))
			note(inv.Budget.Reserve(0))
			inv.Report.Settled(SettleCompleted)
			note(inv.Budget.Reserve(1))
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`1`))}, nil
	})
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(value("a", "1", ""), value("b", "1", ""))}, exec)
	total := NewByteTotal(1000)
	tr := <-runLimited(e, `{}`, RunLimits{Bytes: 1000, ByteTotal: total})
	wantStatus(t, tr, conformance.RunSuccess, `1`)
	want := []string{
		"ok",
		"windmill-lite: wl.reserve(600): the run's byte budget of 1000 has 400 left",
		"ok",
		"windmill-lite: wl.reserve(0): a reservation is a positive number of bytes",
		ErrBudgetSettled.Error(),
	}
	if !slices.Equal(got, want) || total.Used() != 0 {
		t.Fatalf("reservations %q, %d bytes left taken; want %q", got, total.Used(), want)
	}
	// The total bounds a run with no budget of its own, and one run's
	// reservations leave less of it for another.
	got = nil
	if err := total.take(300); err != nil {
		t.Fatal(err)
	}
	tr = <-runLimited(e, `{}`, RunLimits{ByteTotal: total})
	wantStatus(t, tr, conformance.RunSuccess, `1`)
	if got[0] != "ok" || got[1] != "windmill-lite: wl.reserve(600): the runtime's byte total of 1000 has 100 left" || got[2] != "ok" || total.Used() != 300 {
		t.Fatalf("under the total: %q, %d taken", got, total.Used())
	}
	// A run with neither has no Budget.
	got = nil
	<-runLimited(e, `{}`, RunLimits{})
	if !slices.Equal(got, []string{"no budget", "no budget"}) {
		t.Fatalf("no budget: %q", got)
	}
}

// MaxHeldBytes counts a value at its stored size, not its producer's: a
// result whose stored text expands an exponent passes a limit that its
// producer's text would not, in a flow and in a script job.
func TestRunHeldBytesStoredSize(t *testing.T) {
	exec := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
		defer inv.Report.Settled(SettleCompleted)
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`1e+100`))}, nil
	})
	if n := storedLen(jsonval.FromProducer([]byte(`1e+100`))); n != 101 {
		t.Fatalf("the stored size of 1e+100 is %d; the test needs 101", n)
	}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(value("a", "1", ""), value("b", "1", ""))}, exec)
	// The arguments' 2 bytes and a's 101 pass 50; its producer's 6 would not.
	tr := <-runLimited(e, `{}`, RunLimits{MaxHeldBytes: 50})
	if tr.Status != conformance.RunLatched || !errors.Is(tr.Latched, ErrRunMemoryLimit) {
		t.Fatalf("status %s, latched %v; want run_memory_limit", tr.Status, tr.Latched)
	}
	noRecord(t, tr, "m:b")
	r, err := e.RunScript(context.Background(), ScriptRequest{Path: "f/t/echo", Args: jsonval.FromProducer([]byte(`{}`)), MaxHeldBytes: 50})
	if err != nil || !errors.Is(r.Latched, ErrRunMemoryLimit) {
		t.Fatalf("a script job: %+v, %v; want run_memory_limit", r, err)
	}
	if r, err := e.RunScript(context.Background(), ScriptRequest{Path: "f/t/echo", Args: jsonval.FromProducer([]byte(`{}`)), MaxHeldBytes: 103}); err != nil || r.Latched != nil || r.Failed {
		t.Fatalf("a script job at its limit: %+v, %v", r, err)
	}
}

// A script job's stored arguments alone over MaxHeldBytes latch it before
// its script runs. A result that the cache answers counts too, for a root
// script and a root flow alike, though nothing runs.
func TestRunHeldBytesBeforeAndWithoutExecution(t *testing.T) {
	big := `"` + strings.Repeat("x", 100) + `"`
	var calls atomic.Int32
	exec := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
		calls.Add(1)
		defer inv.Report.Settled(SettleCompleted)
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(big))}, nil
	})
	e := mustOpen(t, map[string]string{"f/t/main": withTTL(flowOf(mod("a", "echo", "", "")), 60)}, exec)
	ctx := context.Background()
	r, err := e.RunScript(ctx, ScriptRequest{Path: "f/t/echo", Args: jsonval.FromProducer([]byte(`{"k":` + big + `}`)), MaxHeldBytes: 50})
	if err != nil || !errors.Is(r.Latched, ErrRunMemoryLimit) || calls.Load() != 0 {
		t.Fatalf("large arguments: %+v, %v, %d executor calls; want run_memory_limit and none", r, err, calls.Load())
	}
	// The first run of each root fills the cache; the second, limited,
	// gets the hit.
	if r, err := e.RunScript(ctx, ScriptRequest{Path: "f/t/cached", Args: jsonval.FromProducer([]byte(`{}`))}); err != nil || r.Failed {
		t.Fatalf("%+v, %v", r, err)
	}
	wantStatus(t, <-runLimited(e, `{}`, RunLimits{}), conformance.RunSuccess, big)
	before := calls.Load()
	r, err = e.RunScript(ctx, ScriptRequest{Path: "f/t/cached", Args: jsonval.FromProducer([]byte(`{}`)), MaxHeldBytes: 50})
	if err != nil || !errors.Is(r.Latched, ErrRunMemoryLimit) {
		t.Fatalf("a script's cached result: %+v, %v; want run_memory_limit", r, err)
	}
	tr := <-runLimited(e, `{}`, RunLimits{MaxHeldBytes: 50})
	if tr.Status != conformance.RunLatched || !errors.Is(tr.Latched, ErrRunMemoryLimit) {
		t.Fatalf("a flow's cached result: status %s, latched %v; want run_memory_limit", tr.Status, tr.Latched)
	}
	if calls.Load() != before {
		t.Fatalf("%d executor calls for two cache hits", calls.Load()-before)
	}
}

// A reservation is compared with what is left, so no sum overflows: at a
// total and a budget of math.MaxInt64, the exact rest fits, one byte more
// does not, and a refusal leaves the balances as they were.
func TestByteBudgetBoundaries(t *testing.T) {
	const max = math.MaxInt64
	total := NewByteTotal(max)
	if err := total.take(max - 10); err != nil {
		t.Fatal(err)
	}
	if err := total.take(11); err == nil || total.Used() != max-10 {
		t.Fatalf("11 bytes past 10 left: %v, %d used", err, total.Used())
	}
	if err := total.take(10); err != nil || total.Used() != max {
		t.Fatalf("the exact rest: %v, %d used", err, total.Used())
	}
	if err := total.take(1); err == nil || total.Used() != max {
		t.Fatalf("a byte past a full total: %v, %d used", err, total.Used())
	}
	b, _ := newByteAccount(RunLimits{Bytes: max}).draw(NopReporter{})
	if err := b.Reserve(max - 10); err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve(11); err == nil {
		t.Fatal("11 bytes past a budget with 10 left")
	}
	if err := b.Reserve(10); err != nil {
		t.Fatalf("the exact rest of the budget: %v", err)
	}
	if err := b.Reserve(1); err == nil {
		t.Fatal("a byte past a full budget")
	}
}

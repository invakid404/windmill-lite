package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

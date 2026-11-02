package engine

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"weak"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// retainExec answers each step with a fresh result of 1 MiB and keeps its
// invocation's Reporter afterwards, as the harness keeps its last settled
// invocations for their late output (D9). It keeps a weak pointer to each
// result's bytes, which tell whether anything else still holds them. With
// open set, it leaves the invocation unsettled: its outcome is fixed and
// its call goes on, as a step's whose work outlives its result.
type retainExec struct {
	mu        sync.Mutex
	open      bool
	reporters []Reporter
	results   []weak.Pointer[byte]
}

func (x *retainExec) Execute(_ context.Context, inv Invocation) (StepOutcome, error) {
	body := []byte(`"` + strings.Repeat("x", 1<<20) + `"`)
	x.mu.Lock()
	x.reporters = append(x.reporters, inv.Report)
	x.results = append(x.results, weak.Make(&body[0]))
	open := x.open
	x.mu.Unlock()
	if !open {
		inv.Report.Settled(SettleCompleted)
	}
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer(body)}, nil
}

// reachable counts the results something still holds, after two
// collections.
func (x *retainExec) reachable() int {
	runtime.GC()
	runtime.GC()
	x.mu.Lock()
	defer x.mu.Unlock()
	n := 0
	for _, w := range x.results {
		if w.Value() != nil {
			n++
		}
	}
	return n
}

type settledTrace struct {
	NoHooks
	ch chan *Trace
}

func (h settledTrace) OnRunSettled(t *Trace) { h.ch <- t }

// TestSettledRunLetsGoOfItsValues is lever J5's proof. The executor keeps
// every invocation's Reporter, as the harness's table of settled
// invocations does. Once a run has settled, its step results are
// unreachable however long the Reporters are kept, and a late call on one
// of them goes nowhere. While a run that has ended still has an invocation
// open, its late output reaches it, in the trace that OnRunSettled
// receives, and its values stay reachable until it settles.
func TestSettledRunLetsGoOfItsValues(t *testing.T) {
	flows := map[string]string{"f/t/main": flowOf(mod("a", "echo", "", ""), mod("b", "echo", "", ""))}

	t.Run("settled", func(t *testing.T) {
		x := &retainExec{}
		e := mustOpen(t, flows, x)
		settled := settledTrace{ch: make(chan *Trace, 1)}
		tr := runIt(t, e, `{}`, settled)
		if len(tr.Steps) != 2 || string(tr.Steps[1].Result.Producer())[:4] != `"xxx` {
			t.Fatalf("the run: %+v", tr.Status)
		}
		<-settled.ch
		tr = nil
		if n := x.reachable(); n != 0 {
			t.Fatalf("%d of 2 results of a settled run are still reachable through its kept Reporters", n)
		}
		for _, r := range x.reporters {
			r.Late(Late{Kind: LateLog, Body: jsonval.FromProducer([]byte(`["after"]`))})
		}
		runtime.KeepAlive(x.reporters)
	})

	t.Run("open", func(t *testing.T) {
		x := &retainExec{open: true}
		e := mustOpen(t, flows, x)
		settled := settledTrace{ch: make(chan *Trace, 1)}
		tr := runIt(t, e, `{}`, settled)
		if len(tr.Steps) != 2 {
			t.Fatalf("the run: %+v", tr.Status)
		}
		tr = nil
		if n := x.reachable(); n != 2 {
			t.Fatalf("%d of 2 results of a run with open invocations reachable, want 2", n)
		}
		late := Late{Kind: LateLog, Body: jsonval.FromProducer([]byte(`["late line"]`))}
		x.reporters[0].Late(late)
		for _, r := range x.reporters {
			r.Settled(SettleCompleted)
		}
		got := <-settled.ch
		if l := got.Steps[0].Late; len(l) != 1 || string(l[0].Body.Producer()) != `["late line"]` {
			t.Fatalf("the settled trace's late output %+v, want the late line", l)
		}
		got = nil
		if n := x.reachable(); n != 0 {
			t.Fatalf("%d of 2 results still reachable once the run settled", n)
		}
		runtime.KeepAlive(x.reporters)
	})
}

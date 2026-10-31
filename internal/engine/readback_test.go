package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The read-back tests run synthetic flows on the real QuickJS-ng engine,
// with steps answered in Go; the execution/fx-42-decode and
// execution/fx-42-monitor fixtures of conformance/flows run the recorded
// cases on the real Bun harness (cmd/wltest).

// readSteps answers f/t/value with body, a result the backend's JSON
// library may refuse to read back, as a Bun step can return one nested to
// depth 128, and every other script as loopSteps does.
type readSteps struct {
	loopSteps
	body string
}

func (s *readSteps) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	if inv.Script != "f/t/value" {
		return s.loopSteps.Execute(ctx, inv)
	}
	s.mu.Lock()
	s.invs = append(s.invs, inv)
	s.mu.Unlock()
	defer inv.Report.Settled(SettleCompleted)
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(s.body))}, nil
}

// nested is v inside n arrays.
func nested(n int, v string) string { return strings.Repeat("[", n) + v + strings.Repeat("]", n) }

// hanging is the monitor's reason for a waiting flow of id, in the
// subflow form when subflow says so, on the default host.
func hanging(id string, subflow bool) string {
	page := id + " (http://localhost:3000/run/" + id + "?workspace=windmill-lite)"
	if subflow {
		return "Flow was cancelled because subflow " + page + " was hanging in between 2 steps."
	}
	return "Flow " + page + " was cancelled because it was hanging in between 2 steps."
}

// wantD20 fails unless the trace lists deviation D20 exactly when want
// says so: the engine ended a waiting flow at once.
func wantD20(t *testing.T, tr *Trace, want bool) {
	t.Helper()
	if got := slices.Contains(tr.Deviations, "D20"); got != want {
		t.Fatalf("deviations %v, D20 %v", tr.Deviations, want)
	}
}

// canceledBy decodes a run's Canceled result and checks its canceler and
// reason.
func canceledBy(t *testing.T, tr *Trace, canceler, reason string) {
	t.Helper()
	var res struct {
		Error struct{ Name, Canceler, Reason string }
	}
	if err := json.Unmarshal(tr.Result.Producer(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Error.Name != "Canceled" || res.Error.Canceler != canceler || res.Error.Reason != reason {
		t.Fatalf("result %s", tr.Result)
	}
}

// wantInProgress fails unless rec is a container left unfinished, the way
// the oracle's walker reports it: in progress, carrying nothing, with the
// status st of its job, or canceled for a container with no job of its own.
func wantInProgress(t *testing.T, rec StepRecord, st conformance.StepStatus) {
	t.Helper()
	if !rec.Ran || rec.Status != st || rec.Module != conformance.ModuleInProgress || !rec.Carried.IsZero() {
		t.Fatalf("the container %+v", rec)
	}
}

// [FX-42] rule 9, [FX-42]/m, [FX-42]/n: when a container's children are child
// flows, a child result the backend's JSON library cannot read back (a
// number beyond a double, which an identity child carries from the flow
// input as jsonb keeps it, or nesting to depth 128) fails no job: the flow
// waits with the container in progress, its next module and its failure
// module do not run, and the zombie-flow monitor cancels the run.
//
// [FX-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-42
func TestReadBackLeavesTheFlowWaiting(t *testing.T) {
	// spec: FX-42/m, FX-42/n, JE-44
	identity := `{"id":"x","value":{"type":"identity"}}`
	deep := nested(127, "")
	for _, c := range []struct {
		name, flow, input string
	}{
		{"loop, overflow", flowOf(loopOf("l", []string{identity}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""), mod("n", "echo", "", "")), `{"n":1e400}`},
		{"loop, nesting, with a failure module", withFailure(handler("echo", "", ""), loopOf("l", []string{identity}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""), mod("n", "echo", "", "")), `{"k":` + deep + `}`},
		{"parallel loop, overflow", flowOf(loopOf("l", []string{identity}, `"iterator":{"type":"static","value":[1,2]},"parallel":true,"skip_failures":true`, ""), mod("n", "echo", "", "")), `{"n":-1e400}`},
		{"while loop, overflow", flowOf(whileOf("l", []string{identity}, false, breakAt("true")), mod("n", "echo", "", "")), `{"n":1e400}`},
		{"branchall, overflow", flowOf(branchAllOf("l", []string{branch(skipFailure(false), identity)}, false, ""), mod("n", "echo", "", "")), `{"n":1e400}`},
		{"parallel branchall, nesting", flowOf(branchAllOf("l", []string{branch(skipFailure(false), identity), branch(skipFailure(true), identity)}, true, ""), mod("n", "echo", "", "")), `{"k":` + deep + `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &loopSteps{}
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, s)
			tr := runIt(t, e, c.input, nil)
			wantStatus(t, tr, conformance.RunCanceled, "")
			canceledBy(t, tr, "monitor", hanging(tr.RunID, false))
			wantD20(t, tr, true)
			wantInProgress(t, step(t, tr, "l"), conformance.StepCanceled)
			if calls := s.calls(); len(calls) != 0 {
				t.Errorf("calls %v: the next module or the failure module ran", calls)
			}
		})
	}
	// With 126 nested arrays below the object the read succeeds.
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", []string{identity}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""))}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{"k":`+nested(126, "")+`}`, nil), conformance.RunSuccess, "")
	// The sites that show the iterations so far without reading them back
	// keep a result the library refuses as stored.
	if got := string(arrayOf([]jsonval.Value{jsonval.FromProducer([]byte(`1e400`))}).Producer()); !strings.HasPrefix(got, "[1000") {
		t.Errorf("arrayOf: %.20s…", got)
	}
}

// [FX-42] rule 9, [FX-42]/o: when a container's children are the flow's own
// leaf jobs, a collapsed loop's iterations, the read's failure fails the
// flow at once with InternalErr, with no retry, continue_on_error or later
// step, and the loop stays in progress. The error's column counts in the
// text PostgreSQL prints for the child's stored result: the bracket that
// opens depth 128. In a child flow, the failure does not reach its
// parent, which waits until the monitor cancels the run.
//
// [FX-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-42
func TestReadBackCollapsedBody(t *testing.T) {
	// spec: FX-42/o, JE-44
	const prefix = "SqlErr: error occurred while decoding column 0: recursion limit exceeded at line 1 column "
	failing := func(col string) string {
		return `{"error":{"name":"InternalErr","message":"` + prefix + col + `"}}`
	}
	body := []string{mod("x", "value", "", "")}
	tolerant := `"continue_on_error":true,"retry":{"constant":{"attempts":2,"seconds":0}}`
	for _, c := range []struct {
		name, flow, body, result string
	}{
		{"a for loop", flowOf(loopOf("l", body, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, tolerant), mod("n", "echo", "", "")), nested(128, "1"), failing("128")},
		{"a while loop", flowOf(whileOf("l", body, false, breakAt("true")+","+tolerant), mod("n", "echo", "", "")), nested(128, "1"), failing("128")},
		{"an object's printed form", flowOf(loopOf("l", body, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""), mod("n", "echo", "", "")), `{"k":` + nested(127, "") + `}`, failing("133")},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &readSteps{body: c.body}
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, s)
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, conformance.RunFailure, c.result)
			wantD20(t, tr, false)
			wantInProgress(t, step(t, tr, "l"), conformance.StepCanceled)
			if calls := s.calls(); len(calls) != 1 {
				t.Errorf("calls %v: want the one iteration, with no retry or later step", calls)
			}
		})
	}
	// With 127 wrappings the loop succeeds.
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", body, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""))}, &readSteps{body: nested(127, "1")})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, "["+nested(127, "1")+"]")
	wantD20(t, tr, false)

	// The same loop as a branchone's default branch: the branch's child flow
	// fails, which its parent is never told, so the branchone stays in
	// progress with the branch's failure as its job's, no module end is
	// observed for it or the loop, and the root waits until the monitor
	// cancels it.
	collapsed := loopOf("l", body, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, "")
	s := &readSteps{body: nested(128, "1")}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(branchOneOf("br", nil, []string{collapsed}, ""), mod("n", "echo", "", ""))}, s)
	h := &moduleEnds{}
	tr = runIt(t, e, `{}`, h)
	wantStatus(t, tr, conformance.RunCanceled, "")
	canceledBy(t, tr, "monitor", hanging(tr.RunID, false))
	wantD20(t, tr, true)
	wantInProgress(t, at(t, tr, "m:br/b:default/m:l"), conformance.StepCanceled)
	br := step(t, tr, "br")
	wantInProgress(t, br, conformance.StepFailure)
	sameJSON(t, "the branchone's result", br.Result, failing("128"))
	if calls := s.calls(); len(calls) != 1 {
		t.Errorf("calls %v: n ran", calls)
	}
	if ends := h.ended(); slices.Contains(ends, "m:br") || slices.Contains(ends, "m:br/b:default/m:l") {
		t.Errorf("module ends observed: %v", ends)
	}

	// Inside a subflow called by path, the waiting flow is the subflow, so
	// the monitor's reason takes the subflow form; the flow step waits too.
	e = mustOpen(t, map[string]string{
		"f/t/child": flowOf(branchOneOf("br", nil, []string{collapsed}, "")),
		"f/t/main":  flowOf(subflowOf("s", "f/t/child", "", ""), mod("n", "echo", "", "")),
	}, &readSteps{body: nested(128, "1")})
	tr = runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunCanceled, "")
	sub := step(t, tr, "s")
	canceledBy(t, tr, "monitor", hanging(sub.Job, true))
	wantD20(t, tr, true)
	wantInProgress(t, sub, conformance.StepCanceled)
	wantInProgress(t, at(t, tr, "m:s/m:br"), conformance.StepFailure)
}

// childSteps answers f/t/value with an unreadable result, nested to depth
// 128, in the iterations under the site prefix unreadable, and with 1
// elsewhere. Once a step under unreadable has started, f/t/echo blocks
// until its step is canceled.
type childSteps struct {
	scripts
	unreadable string
	started    chan struct{}
	once       sync.Once
}

func (s *childSteps) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	s.mu.Lock()
	s.invs = append(s.invs, inv)
	s.mu.Unlock()
	defer inv.Report.Settled(SettleCompleted)
	if inv.Script == "f/t/echo" {
		s.once.Do(func() { close(s.started) })
		<-ctx.Done()
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	}
	if strings.HasPrefix(inv.Site.String(), s.unreadable) {
		if s.started != nil {
			<-s.started
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(nested(128, "1")))}, nil
	}
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`1`))}, nil
}

// [FX-42] rule 9, [FX-42]/o: a child flow of a parallel container whose
// collapsed loop fails the read never tells the container's flow that it
// ended. The container does not count it, so it releases no held
// iteration, and the container's flow waits: the monitor cancels the run
// at once (D20), which cancels the container's other children, whose
// records stay.
//
// [FX-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-42
func TestReadBackInAParallelChild(t *testing.T) {
	// spec: FX-42/o, CF-7, JE-2
	inner := loopOf("inner", []string{mod("x", "value", "", "")}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, "")

	// parallelism 1: the second iteration is held, and the first one's end,
	// which the loop is never told, does not release it.
	outer := loopOf("outer", []string{inner}, `"iterator":{"type":"static","value":[1,2]},"parallel":true,"parallelism":{"type":"static","value":1},"skip_failures":false`, "")
	s := &childSteps{unreadable: "m:outer/i:0/"}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(outer, mod("n", "echo", "", ""))}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunCanceled, "")
	canceledBy(t, tr, "monitor", hanging(tr.RunID, false))
	wantD20(t, tr, true)
	wantInProgress(t, step(t, tr, "outer"), conformance.StepCanceled)
	if calls := s.calls(); !slices.Equal(calls, []string{"m:outer/i:0/m:inner/i:0/m:x"}) {
		t.Errorf("calls %v: the held iteration or n ran", calls)
	}

	// A parallel branchall whose other branch is still running: the monitor
	// cancels the run as soon as the failing branch ends, not when the
	// other branch does, and the canceled branch keeps its record.
	all := branchAllOf("outer", []string{branch(skipFailure(false), inner), branch(skipFailure(false), mod("wait", "echo", "", ""))}, true, "")
	s = &childSteps{unreadable: "m:outer/b:0/", started: make(chan struct{})}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(all)}, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, tr, conformance.RunCanceled, "")
	canceledBy(t, tr, "monitor", hanging(tr.RunID, false))
	wantD20(t, tr, true)
	wantInProgress(t, step(t, tr, "outer"), conformance.StepCanceled)
	if rec := at(t, tr, "m:outer/b:1/m:wait"); !rec.Ran {
		t.Errorf("the other branch's step: %+v", rec)
	}
}

// With the monitor disabled, the other children of a parallel loop whose
// child flow never reported its end still complete, and their completions
// release held iterations; the loop never completes, and the run waits
// until it is canceled.
func TestReadBackInAParallelChildWithoutMonitor(t *testing.T) {
	// spec: FX-42/o, CF-7, JE-2
	inner := loopOf("inner", []string{mod("x", "value", "", "")}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, "")
	for _, c := range []struct {
		name, parallelism string
		calls             []string
	}{
		// The first iteration's end releases nothing.
		{"held behind the unreported iteration", "1", []string{"m:outer/i:0/m:inner/i:0/m:x"}},
		// The second iteration's end releases the third.
		{"released by another iteration", "2", []string{"m:outer/i:0/m:inner/i:0/m:x", "m:outer/i:1/m:inner/i:0/m:x", "m:outer/i:2/m:inner/i:0/m:x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			outer := loopOf("outer", []string{inner}, `"iterator":{"type":"static","value":[1,2,3]},"parallel":true,"parallelism":{"type":"static","value":`+c.parallelism+`},"skip_failures":false`, "")
			s := &childSteps{unreadable: "m:outer/i:0/"}
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(outer, mod("n", "echo", "", ""))}, s)
			e.transition = 0
			ctx, cancel := context.WithCancelCause(context.Background())
			time.AfterFunc(200*time.Millisecond, func() { cancel(&Cancellation{Reason: "r", Canceler: "u"}) })
			start := time.Now()
			tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))})
			if err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d < 200*time.Millisecond {
				t.Fatalf("the run ended after %v, before its cancellation", d)
			}
			canceledBy(t, tr, "u", "r")
			wantD20(t, tr, false)
			wantInProgress(t, step(t, tr, "outer"), conformance.StepCanceled)
			calls := s.calls()
			slices.Sort(calls)
			if !slices.Equal(calls, c.calls) {
				t.Errorf("calls %v, want %v", calls, c.calls)
			}
		})
	}
}

// moduleEnds records the sites whose module end H6 observes.
type moduleEnds struct {
	NoHooks
	mu    sync.Mutex
	sites []string
}

func (h *moduleEnds) OnModuleEnd(_ context.Context, _ Run, s Site, _ StepRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sites = append(h.sites, s.Path.String())
	return nil
}

func (h *moduleEnds) ended() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.sites)
}

// With the zombie-flow monitor disabled, as DISABLE_ZOMBIE_JOBS_MONITORING
// disables Windmill's, a flow left waiting by a failed read waits until
// the run is canceled.
func TestReadBackWithoutMonitor(t *testing.T) {
	// spec: FX-42/m, JE-2
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", []string{`{"id":"x","value":{"type":"identity"}}`}, `"iterator":{"type":"static","value":[1]},"parallel":false,"skip_failures":false`, ""))}, &loopSteps{})
	e.transition = 0
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(200*time.Millisecond, func() { cancel(&Cancellation{Reason: "r", Canceler: "u"}) })
	start := time.Now()
	tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{"n":1e400}`))})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("the run ended after %v, before its cancellation", d)
	}
	wantStatus(t, tr, conformance.RunCanceled, `{"error":{"message":"Job canceled: r by u","name":"Canceled","reason":"r","canceler":"u"}}`)
	wantD20(t, tr, false)
	wantInProgress(t, step(t, tr, "l"), conformance.StepCanceled)
}

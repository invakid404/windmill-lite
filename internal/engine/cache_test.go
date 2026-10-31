package engine

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// The result cache tests (milestone N4, FX-40) run synthetic flows on the
// real QuickJS-ng engine, with steps answered in Go; the E3d fixtures of
// conformance/flows/execution/fx-40 run the recorded cases on the real Bun
// harness (cmd/wltest, TestFlowSuite and TestCacheFixturesFreshAndWarm).

// idSteps answers every step but f/t/fail with its own job id, which the
// fixtures' scripts return as WM_JOB_ID, so a cache hit shows as a result
// naming another job; f/t/fail fails as scripts' does.
type idSteps struct{ scripts }

func (s *idSteps) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	if inv.Script == "f/t/fail" {
		return s.scripts.Execute(ctx, inv)
	}
	s.mu.Lock()
	s.invs = append(s.invs, inv)
	s.mu.Unlock()
	defer inv.Report.Settled(SettleCompleted)
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"` + inv.ID + `"`))}, nil
}

// count returns the invocations of script made so far.
func (s *scripts) count(script string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, inv := range s.invs {
		if inv.Script == script || strings.HasPrefix(inv.Script, "inline-") && script == "inline" {
			n++
		}
	}
	return n
}

// cachedInline writes an inline Bun step with input transforms and
// envelope fields.
func cachedInline(id, inputs, envelope string) string {
	if inputs == "" {
		inputs = "{}"
	}
	if envelope != "" {
		envelope = "," + envelope
	}
	return `{"id":"` + id + `","value":{"type":"rawscript","language":"bun","content":"export async function main(...a: any[]) { return null }","input_transforms":` + inputs + `}` + envelope + `}`
}

// withTTL adds a flow-level cache_ttl to a flow value.
func withTTL(flow string, ttl int) string {
	return strings.TrimSuffix(flow, "}") + `,"cache_ttl":` + strconv.Itoa(ttl) + `}`
}

// compileFlows compiles the flows, every one a root, with the test
// folder's scripts.
func compileFlows(t *testing.T, flows map[string]string) *ir.Program {
	t.Helper()
	jsOnce.Do(func() { jsEng, jsErr = newTestJS() })
	if jsErr != nil {
		t.Fatal(jsErr)
	}
	folder, err := syncfolder.Load(testFolder(flows))
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	prog, err := ir.Compile(folder, nil, ir.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return prog
}

// testClock is a Clock whose time the test sets.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Wait(ctx context.Context, d time.Duration) error { return context.Cause(ctx) }

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// runAt runs f/t/main with args on clock c.
func runAt(t *testing.T, e *Engine, args string, c Clock) *Trace {
	t.Helper()
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(args)), Seed: 1, Clock: c})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// FX-40/a: an inline step with a cache_ttl stores its result, and a job of
// the same step with the same arguments completes at once with it, with no
// invocation and a job id of its own: here the subflow's step on its
// second call, and both calls on a later run. Other arguments miss.
func TestCacheStepHit(t *testing.T) {
	x := &idSteps{}
	e := mustOpen(t, map[string]string{
		"f/t/child": flowOf(cachedInline("r", inputs("v", js("flow_input.v")), `"cache_ttl":60`)),
		"f/t/main": flowOf(
			subflowOf("s1", "f/t/child", inputs("v", js("flow_input.v")), ""),
			subflowOf("s2", "f/t/child", inputs("v", js("flow_input.v")), ""),
		),
	}, x)
	tr := runIt(t, e, `{"v":1}`, nil)
	first := at(t, tr, "m:s1/m:r")
	hit := at(t, tr, "m:s2/m:r")
	wantStatus(t, tr, conformance.RunSuccess, string(first.Result.Producer()))
	if first.Cached || !hit.Cached || !hit.Ran || hit.Status != conformance.StepSuccess || hit.Job == "" || hit.Job == first.Job {
		t.Fatalf("the first job %+v, the hit %+v", first, hit)
	}
	sameJSON(t, "the hit's result", hit.Result, string(first.Result.Producer()))
	sameJSON(t, "the hit's arguments", hit.RecordedArgs, `{"v":1}`)
	if n := x.count("inline"); n != 1 {
		t.Fatalf("%d invocations, want 1", n)
	}
	// A later run hits on both calls; other arguments run again.
	tr = runIt(t, e, `{"v":1}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, string(first.Result.Producer()))
	if !at(t, tr, "m:s1/m:r").Cached || x.count("inline") != 1 {
		t.Fatalf("a later run invoked %d", x.count("inline"))
	}
	tr = runIt(t, e, `{"v":2}`, nil)
	if at(t, tr, "m:s1/m:r").Cached || !at(t, tr, "m:s2/m:r").Cached || x.count("inline") != 2 {
		t.Fatalf("other arguments: %d invocations", x.count("inline"))
	}
}

// FX-40: an entry lives until now plus the TTL on the run's clock (H10),
// and a job after that runs again and stores anew.
func TestCacheExpiry(t *testing.T) {
	x := &idSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "cached", inputs("v", static("1")), ""))}, x)
	c := &testClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	t0 := c.Now()
	first := runAt(t, e, `{}`, c)
	for _, step := range []struct {
		at     time.Duration
		cached bool
	}{{59 * time.Second, true}, {60 * time.Second, false}, {119 * time.Second, true}, {121 * time.Second, false}} {
		c.set(t0.Add(step.at))
		tr := runAt(t, e, `{}`, c)
		if a := at(t, tr, "m:a"); a.Cached != step.cached {
			t.Fatalf("at %v: cached %v", step.at, a.Cached)
		}
	}
	if n := x.count("f/t/cached"); n != 3 {
		t.Fatalf("%d invocations, want 3", n)
	}
	_ = first
}

// FX-40's matrix: a path script takes its own cache_ttl and drops its
// module's, so two modules running it with the same arguments share an
// entry, and a path script without one is not cached whatever its module
// says; an identity's and a subflow step's own cache_ttl cache nothing.
func TestCacheTTLMatrix(t *testing.T) {
	for _, c := range []struct {
		name, flow, script string
		invocations        int
	}{
		{"a script's own", flowOf(mod("a", "cached", inputs("v", static("1")), ""), mod("b", "cached", inputs("v", static("1")), "")), "f/t/cached", 1},
		{"its module's dropped", flowOf(mod("a", "value", inputs("v", static("1")), `"cache_ttl":60`), mod("b", "value", inputs("v", static("1")), `"cache_ttl":60`)), "f/t/value", 2},
		{"its own over its module's", flowOf(mod("a", "cached", inputs("v", static("1")), `"cache_ttl":0`), mod("b", "cached", inputs("v", static("1")), "")), "f/t/cached", 1},
		{"a subflow step's dropped", flowOf(subflowOf("a", "f/t/child", "", `"cache_ttl":60`), subflowOf("b", "f/t/child", "", `"cache_ttl":60`)), "f/t/value", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := &idSteps{}
			e := mustOpen(t, map[string]string{"f/t/main": c.flow, "f/t/child": flowOf(mod("s", "value", inputs("v", static("1")), ""))}, x)
			runIt(t, e, `{}`, nil)
			if n := x.count(c.script); n != c.invocations {
				t.Fatalf("%d invocations of %s, want %d", n, c.script, c.invocations)
			}
		})
	}
	// Two inline steps are two runnables, even with the same code and
	// arguments: neither hits the other's entry.
	x := &idSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(cachedInline("a", inputs("v", static("1")), `"cache_ttl":60`), cachedInline("b", inputs("v", static("1")), `"cache_ttl":60`))}, x)
	tr := runIt(t, e, `{}`, nil)
	if at(t, tr, "m:b").Cached || x.count("inline") != 2 {
		t.Fatal("two inline steps shared an entry")
	}
}

// FX-40/c, FX-40/d: a collapsed loop's iterations take the loop module's
// cache_ttl for an inline body, so iterations with the same arguments
// share an entry, iteration 1 returning iteration 0's result; a path
// script body takes its own, the loop's dropped.
func TestCacheCollapsedLoop(t *testing.T) {
	loop := func(body, envelope string) string {
		return loopOf("l", []string{body}, `"iterator":{"type":"static","value":[1,2]},"parallel":false,"skip_failures":false`, envelope)
	}
	for _, c := range []struct {
		name, flow  string
		invocations int
		shared      bool
	}{
		{"inline body, loop TTL", flowOf(loop(cachedInline("b", inputs("v", static(`"x"`)), ""), `"cache_ttl":60`)), 1, true},
		{"inline body, iteration arguments", flowOf(loop(cachedInline("b", inputs("v", js("flow_input.iter.value")), ""), `"cache_ttl":60`)), 2, false},
		{"path body without its own", flowOf(loop(mod("b", "value", inputs("v", static(`"x"`)), ""), `"cache_ttl":60`)), 2, false},
		{"path body with its own", flowOf(loop(mod("b", "cached", inputs("v", static(`"x"`)), ""), "")), 1, true},
		{"inline body, no TTL", flowOf(loop(cachedInline("b", inputs("v", static(`"x"`)), ""), "")), 2, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := &idSteps{}
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, x)
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, conformance.RunSuccess, "")
			i0, i1 := at(t, tr, "m:l/i:0/m:b"), at(t, tr, "m:l/i:1/m:b")
			same := string(i0.Result.Producer()) == string(i1.Result.Producer())
			if len(x.invs) != c.invocations || same != c.shared || i1.Cached != c.shared {
				t.Fatalf("%d invocations, iterations %s and %s (cached %v)", len(x.invs), i0.Result, i1.Result, i1.Cached)
			}
		})
	}
}

// FX-40/b: a flow job with a flow-level cache_ttl stores its final result
// when its last job succeeded, before the recover check: a failed subflow
// whose failure module ran without recover caches the failure module's
// result, which its next call returns as a success with no module run.
// A flow that failed with no failure module, or whose failure module
// failed, stores nothing.
func TestCacheFlowJob(t *testing.T) {
	handled := withTTL(withFailure(handler("value", "", ""), mod("a", "fail", "", "")), 60)
	x := &idSteps{}
	e := mustOpen(t, map[string]string{
		"f/t/child": handled,
		"f/t/main": flowOf(
			subflowOf("s1", "f/t/child", inputs("v", js("flow_input.v")), `"continue_on_error":true`),
			subflowOf("s2", "f/t/child", inputs("v", js("flow_input.v")), ""),
		),
	}, x)
	tr := runIt(t, e, `{"v":1}`, nil)
	// The failure module's result is its job's id (idSteps).
	h := at(t, tr, "m:s1/s:failure")
	wantStatus(t, tr, conformance.RunSuccess, string(h.Result.Producer()))
	s1, s2 := at(t, tr, "m:s1"), at(t, tr, "m:s2")
	if s1.Status != conformance.StepFailure || s1.Cached || s2.Status != conformance.StepSuccess || !s2.Cached || s2.Job == "" {
		t.Fatalf("s1 %+v, s2 %+v", s1, s2)
	}
	sameJSON(t, "s1's result", s1.Result, string(h.Result.Producer()))
	sameJSON(t, "s2's result", s2.Result, string(h.Result.Producer()))
	noRecord(t, tr, "m:s2/m:a")
	noRecord(t, tr, "m:s2/s:failure")
	found := false
	for _, c := range tr.Children {
		found = found || c.Site.String() == "m:s2" && c.ID == s2.Job && c.Failure
	}
	if !found {
		t.Fatalf("the cached subflow is not a child flow: %+v", tr.Children)
	}
	// s1's a and its failure module; s2 invoked nothing.
	if n := len(x.invs); n != 2 {
		t.Fatalf("%d invocations, want 2", n)
	}

	// The store table: a flow stores when its completion counts as a
	// success before the recover check, and only then.
	halt := `"stop_after_if":{"expr":"true","skip_if_stopped":false,"error_message":"halt"}`
	for _, c := range []struct {
		name, child string
		stored      bool
		status      conformance.StepStatus
	}{
		{"no failure module", withTTL(flowOf(mod("a", "fail", "", "")), 60), false, conformance.StepFailure},
		{"a failing failure module", withTTL(withFailure(handler("fail", "", ""), mod("a", "fail", "", "")), 60), false, conformance.StepFailure},
		{"an error stop", withTTL(flowOf(mod("a", "value", inputs("v", static("1")), halt)), 60), false, conformance.StepFailure},
		{"a tolerated failure at the last module", withTTL(flowOf(mod("a", "fail", "", `"continue_on_error":true`)), 60), true, conformance.StepSuccess},
		{"a tolerated error stop at the last module", withTTL(flowOf(mod("a", "value", inputs("v", static("1")), halt+`,"continue_on_error":true`)), 60), true, conformance.StepSuccess},
		{"a clean stop", withTTL(flowOf(mod("a", "value", inputs("v", static("1")), `"stop_after_if":{"expr":"true","skip_if_stopped":true}`), mod("b", "value", "", "")), 60), true, conformance.StepSkipped},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{
				"f/t/child": c.child,
				"f/t/main": flowOf(
					subflowOf("s1", "f/t/child", "", `"continue_on_error":true`),
					subflowOf("s2", "f/t/child", "", `"continue_on_error":true`),
				),
			}, &idSteps{})
			tr := runIt(t, e, `{}`, nil)
			s1, s2 := at(t, tr, "m:s1"), at(t, tr, "m:s2")
			if s1.Status != c.status {
				t.Fatalf("the first call %+v", s1)
			}
			if s2.Cached != c.stored {
				t.Fatalf("the second call hit an entry: %v, want %v", s2.Cached, c.stored)
			}
			if !c.stored {
				at(t, tr, "m:s2/m:a")
				return
			}
			// The hit is a success, whatever the stored run's status.
			if s2.Status != conformance.StepSuccess {
				t.Fatalf("the hit %+v", s2)
			}
			sameJSON(t, "the hit's result", s2.Result, string(s1.Result.Producer()))
			noRecord(t, tr, "m:s2/m:a")
		})
	}
}

// FX-40: the root flow's own flow-level cache_ttl caches the run: a run
// with the same input completes successfully at once with the stored
// result, no module run, and other input runs.
func TestCacheRootFlow(t *testing.T) {
	x := &idSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": withTTL(flowOf(mod("a", "value", inputs("v", js("flow_input.v")), "")), 60)}, x)
	first := runIt(t, e, `{"v":"one"}`, nil)
	if first.Cached || len(first.Steps) != 1 {
		t.Fatalf("the first run %+v", first)
	}
	again := runIt(t, e, `{"v":"one"}`, nil)
	wantStatus(t, again, conformance.RunSuccess, string(first.Result.Producer()))
	if !again.Cached || len(again.Steps) != 0 || len(x.invs) != 1 {
		t.Fatalf("the second run: cached %v, %d steps, %d invocations", again.Cached, len(again.Steps), len(x.invs))
	}
	if other := runIt(t, e, `{"v":"two"}`, nil); other.Cached {
		t.Fatal("other input hit")
	}
}

// failingResults is a host result cache that fails every call.
type failingResults struct{}

func (failingResults) Get(context.Context, string, time.Time) ([]byte, bool, error) {
	return nil, false, errors.New("store down")
}

func (failingResults) Put(context.Context, string, []byte, time.Time) error {
	return errors.New("store down")
}

// D4, ADR 0007: a host's result cache is shared by the engines it is given
// to, an entry never crossing bundles; a store that fails is a miss and a
// warning, never a failed run.
func TestCacheHostStore(t *testing.T) {
	host := platform.NewMemoryResults()
	open := func(bundle string, exec StepExecutor, results platform.ResultCache) *Engine {
		t.Helper()
		prog := compileFlows(t, map[string]string{"f/t/main": flowOf(mod("a", "cached", inputs("v", static("1")), ""))})
		e, err := New(Config{Program: prog, Bundle: bundle, Exec: exec, JS: jsEng, Platform: &platform.Services{Results: results}})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	x := &idSteps{}
	runIt(t, open("sha256:one", x, host), `{}`, nil)
	if tr := runIt(t, open("sha256:one", x, host), `{}`, nil); !at(t, tr, "m:a").Cached {
		t.Fatal("an engine of the same bundle missed the host's entry")
	}
	if tr := runIt(t, open("sha256:two", x, host), `{}`, nil); at(t, tr, "m:a").Cached {
		t.Fatal("an engine of another bundle hit")
	}
	tr := runIt(t, open("sha256:one", x, failingResults{}), `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, "")
	codes := map[string]int{}
	for _, w := range tr.Warnings {
		codes[w.Code]++
	}
	if at(t, tr, "m:a").Cached || codes["result_cache_unavailable"] != 2 {
		t.Fatalf("warnings %+v", tr.Warnings)
	}
}

// H1 answers at the launch, before a job exists, so an intercepted module
// neither reads the result cache nor stores its outcome.
func TestCacheIntercepted(t *testing.T) {
	x := &idSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "cached", inputs("v", static("1")), ""))}, x)
	hooks := &interceptAll{}
	if tr := runIt(t, e, `{}`, hooks); at(t, tr, "m:a").Cached {
		t.Fatal("an intercepted module hit")
	}
	if tr := runIt(t, e, `{}`, nil); at(t, tr, "m:a").Cached || len(x.invs) != 1 {
		t.Fatal("an intercepted outcome was stored")
	}
	if tr := runIt(t, e, `{}`, hooks); at(t, tr, "m:a").Cached || string(at(t, tr, "m:a").Result.Producer()) != `"a"` {
		t.Fatal("an intercepted module read the cache")
	}
}

// ADR 0006: the engine's invocations run in the module-state mode it was
// opened with, warm by default, which its traces record; another mode is
// refused.
func TestEngineModuleState(t *testing.T) {
	prog := compileFlows(t, map[string]string{"f/t/main": flowOf(mod("a", "cached", "", ""))})
	for _, c := range []struct {
		state, want ModuleState
	}{{"", ModuleStateWarm}, {ModuleStateWarm, ModuleStateWarm}, {ModuleStateFresh, ModuleStateFresh}} {
		x := &idSteps{}
		e, err := New(Config{Program: prog, Exec: x, JS: jsEng, ModuleState: c.state})
		if err != nil {
			t.Fatal(err)
		}
		tr := runIt(t, e, `{}`, nil)
		if tr.ModuleState != c.want || x.invs[0].ModuleState != c.want {
			t.Fatalf("%q: trace %q, invocation %q", c.state, tr.ModuleState, x.invs[0].ModuleState)
		}
	}
	if _, err := New(Config{Program: prog, JS: jsEng, ModuleState: "host"}); err == nil {
		t.Fatal("an unknown module state was accepted")
	}
}

// cancelCache is a host result cache that cancels the run's context
// during its Get or its Put, as a host store whose call outlives the
// run's cancellation would.
type cancelCache struct {
	platform.ResultCache
	cancel context.CancelFunc
	onPut  bool
}

func (c cancelCache) Get(ctx context.Context, key string, now time.Time) ([]byte, bool, error) {
	b, ok, err := c.ResultCache.Get(ctx, key, now)
	if !c.onPut {
		c.cancel()
	}
	return b, ok, err
}

func (c cancelCache) Put(ctx context.Context, key string, b []byte, expires time.Time) error {
	if c.onPut {
		c.cancel()
	}
	return c.ResultCache.Put(ctx, key, b, expires)
}

// ADR 0008 H4 and H5, FX-38: a flow job the result cache answers still
// goes through the host's start and completion checks, so a cached result
// never turns a canceled run into a successful one: neither with the
// cancellation already there, nor with a passed pre-write deadline, nor
// with the cancellation arriving during the store's Get, or during its
// Put after the flow's own last completion check.
func TestCacheHostChecks(t *testing.T) {
	flow := map[string]string{"f/t/main": withTTL(flowOf(mod("a", "value", inputs("v", static("1")), "")), 60)}
	run := func(e *Engine, ctx context.Context, deadline time.Time) *Trace {
		t.Helper()
		tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), PreWriteDeadline: deadline})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	e := mustOpen(t, flow, &scripts{})
	runIt(t, e, `{}`, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if tr := run(e, ctx, time.Time{}); tr.Status != conformance.RunCanceled || tr.Cached {
		t.Fatalf("a canceled run: %s, cached %v", tr.Status, tr.Cached)
	}
	if tr := run(e, context.Background(), time.Now().Add(-time.Second)); tr.Status != conformance.RunCanceled || tr.Cached {
		t.Fatalf("a passed pre-write deadline: %s, cached %v", tr.Status, tr.Cached)
	}
	if tr := runIt(t, e, `{}`, nil); tr.Status != conformance.RunSuccess || !tr.Cached {
		t.Fatalf("the entry is gone: %s, cached %v", tr.Status, tr.Cached)
	}
	for _, onPut := range []bool{false, true} {
		e := mustOpen(t, flow, &scripts{})
		if !onPut {
			runIt(t, e, `{}`, nil)
		}
		ctx, cancel := context.WithCancel(context.Background())
		e.results = cancelCache{ResultCache: e.results, cancel: cancel, onPut: onPut}
		tr := run(e, ctx, time.Time{})
		cancel()
		if tr.Status != conformance.RunCanceled || tr.Cached {
			t.Fatalf("canceled during the Put %v: %s, cached %v", onPut, tr.Status, tr.Cached)
		}
	}
}

// ADR 0007, [FX-40]: a job the result cache completes is still a job,
// which the job registry knows while its root lives, with its identity,
// as it knows one that ran; once the root has ended, neither is live, and
// both keep only their root ([WA-19]). Here the run's jobs share an outer
// root that a dispatch tracked in its parent gives them, which lives on
// after the run.
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19
func TestCacheHitRegistersJob(t *testing.T) {
	p := &platform.Services{}
	end := p.JobRegistry().Begin(platform.Job{ID: "outer", Kind: platform.JobFlow, Root: "outer"})
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "cached", inputs("v", static("1")), ""))}, &idSteps{}, p)
	var jobs []string
	for range 2 {
		tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), ParentJob: "outer", RootJob: "outer"})
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, at(t, tr, "m:a").Job)
	}
	// The first job ran through an executor that registers nothing, as the
	// facade's would; the hit registers its own.
	if root, known := p.RootJob(jobs[1]); !known || root != "outer" {
		t.Fatalf("the hit's job %s: root %q, known %v", jobs[1], root, known)
	}
	if env, ok := p.JobEnv(jobs[1]); !ok || env["WM_FLOW_STEP_ID"] != "a" || env["WM_JOB_PATH"] != "f/t/cached" {
		t.Fatalf("the hit's job variables %v (%v)", env, ok)
	}
	end()
	if _, live, _ := p.JobRegistry().Job(jobs[1]); live {
		t.Fatal("the hit's job outlived its root")
	}
	if _, ok := p.JobEnv(jobs[1]); ok {
		t.Fatal("the hit's job variables outlived its root")
	}

	// A script root's hit, dispatched tracked in its parent, is known as
	// the cold run's job is, through the executor that registers it.
	p = &platform.Services{}
	end = p.JobRegistry().Begin(platform.Job{ID: "parent", Kind: platform.JobFlow, Root: "parent"})
	e = openWithPlatform(t, map[string]string{}, registering{p: p, inner: &idSteps{}}, p)
	var ids []string
	for range 2 {
		r, err := e.RunScript(context.Background(), ScriptRequest{Path: "f/t/cached", Args: jsonval.FromProducer([]byte(`{}`)), ParentJob: "parent", RootJob: "parent"})
		if err != nil || r.Failed {
			t.Fatalf("%+v %v", r, err)
		}
		ids = append(ids, r.ID)
	}
	for _, id := range ids {
		if root, known := p.RootJob(id); !known || root != "parent" {
			t.Fatalf("job %s: root %q, known %v", id, root, known)
		}
	}
	end()
	for _, id := range ids {
		if _, live, _ := p.JobRegistry().Job(id); live {
			t.Fatalf("job %s outlived its root", id)
		}
	}
	// A canceled run's hit is killed, as a canceled run's job is.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := e.RunScript(ctx, ScriptRequest{Path: "f/t/cached", Args: jsonval.FromProducer([]byte(`{}`))}); err != nil || !r.Failed {
		t.Fatalf("a canceled hit: %+v %v", r, err)
	}
}

// registering registers each invocation's job for as long as it runs, as
// the facade's executor does (facade.Register).
type registering struct {
	p     *platform.Services
	inner StepExecutor
}

func (x registering) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	end := x.p.JobRegistry().Begin(platform.Job{ID: inv.ID, Kind: platform.JobScript, Root: inv.Env["WM_ROOT_JOB_ID"], Path: inv.Env["WM_JOB_PATH"], Env: inv.Env})
	defer end()
	return x.inner.Execute(ctx, inv)
}

// [FX-40]/m: a job whose cache_ttl is 0 still looks the cache up, since a
// lookup tests only the stored entry's own expiry: it never hits its own
// entries, which expire as they are stored, but it hits an entry that a
// job with a positive TTL stored under the same key.
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
func TestCacheZeroTTL(t *testing.T) {
	x := &idSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(cachedInline("a", inputs("v", static("1")), `"cache_ttl":0`))}, x)
	runIt(t, e, `{}`, nil)
	if tr := runIt(t, e, `{}`, nil); at(t, tr, "m:a").Cached || x.count("inline") != 2 {
		t.Fatalf("a zero TTL hit its own entry: %d invocations", x.count("inline"))
	}
	var id string
	for _, s := range e.prog.Scripts {
		if s.Kind != ir.PathScript {
			id = s.ID
		}
	}
	ctx := context.Background()
	key, ok := e.cacheKey(ctx, cacheKindFlowScript, id, jsonval.FromProducer([]byte(`{"v":1}`)), false)
	if !ok {
		t.Fatal("no key")
	}
	if err := e.cachePut(ctx, key, time.Now(), time.Minute, jsonval.FromProducer([]byte(`"stored"`)), OriginNone, nil); err != nil {
		t.Fatal(err)
	}
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `"stored"`)
	if !at(t, tr, "m:a").Cached || x.count("inline") != 2 {
		t.Fatalf("the zero TTL missed a positive TTL's entry: %d invocations", x.count("inline"))
	}
}

// An entry's expiry ([FX-40]) is its store time in whole seconds plus the
// TTL, and it is valid while the current time, in whole seconds, is
// earlier: an entry stored at 12:00:00.9 with a TTL of 60 s is valid at
// 12:00:59.95 and gone at 12:01:00.1.
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
func TestCacheExpiryWholeSeconds(t *testing.T) {
	x := &idSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "cached", inputs("v", static("1")), ""))}, x)
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	c := &testClock{now: t0.Add(900 * time.Millisecond)}
	runAt(t, e, `{}`, c)
	for _, step := range []struct {
		at     time.Duration
		cached bool
	}{{59950 * time.Millisecond, true}, {60100 * time.Millisecond, false}} {
		c.set(t0.Add(step.at))
		if a := at(t, runAt(t, e, `{}`, c), "m:a"); a.Cached != step.cached {
			t.Fatalf("at %v: cached %v", step.at, a.Cached)
		}
	}
}

// [FX-40]/k, [FX-40]: an argument whose whole value is an S3 object record is
// hashed with the object's ETag when the object store holds the object,
// and its text follows unless cache_ignore_s3_path is set. Without an ETag
// (no object store, no such object, another storage, a record nested in
// the argument) it is hashed as its text, and the flag has no effect.
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
func TestCacheS3Objects(t *testing.T) {
	ctx := context.Background()
	step := func(ignore bool) string {
		return flowOf(cachedInline("a", inputs("f", js("flow_input.f")), `"cache_ttl":60,"cache_ignore_s3_path":`+strconv.FormatBool(ignore)))
	}
	type call struct {
		input string
		hit   bool
	}
	s3 := func(key string) string { return `{"f":{"s3":"` + key + `"}}` }
	run := func(t *testing.T, e *Engine, x *idSteps, calls []call) {
		t.Helper()
		for k, c := range calls {
			if got := at(t, runIt(t, e, c.input, nil), "m:a").Cached; got != c.hit {
				t.Fatalf("call %d with %s: hit %v, want %v", k, c.input, got, c.hit)
			}
		}
	}

	t.Run("no object store", func(t *testing.T) {
		x := &idSteps{}
		e := mustOpen(t, map[string]string{"f/t/main": step(true)}, x)
		run(t, e, x, []call{{s3("t/a.txt"), false}, {s3("t/a.txt"), true}, {s3("t/b.txt"), false}})
	})

	objects, err := platform.NewDirObjects(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	put := func(key, content string) {
		t.Helper()
		if err := objects.Put(ctx, platform.DefaultStorage, key, platform.Object{Content: []byte(content)}); err != nil {
			t.Fatal(err)
		}
	}
	put("t/a.txt", "x")
	put("t/b.txt", "x")
	put("t/c.txt", "y")

	t.Run("the path hashed", func(t *testing.T) {
		x := &idSteps{}
		e := openWithPlatform(t, map[string]string{"f/t/main": step(false)}, x, &platform.Services{Objects: objects})
		run(t, e, x, []call{{s3("t/a.txt"), false}, {s3("t/a.txt"), true}, {s3("t/b.txt"), false}})
		// New content under the same key misses.
		put("t/a.txt", "x2")
		defer put("t/a.txt", "x")
		run(t, e, x, []call{{s3("t/a.txt"), false}, {s3("t/a.txt"), true}})
	})

	t.Run("the path ignored", func(t *testing.T) {
		x := &idSteps{}
		e := openWithPlatform(t, map[string]string{"f/t/main": step(true)}, x, &platform.Services{Objects: objects})
		run(t, e, x, []call{
			{s3("t/a.txt"), false},
			// The same content under another key, with other members.
			{`{"f":{"s3":"t/b.txt","filename":"b.txt","extra":1}}`, true},
			{s3("t/c.txt"), false},
			// No such object, another storage, a member of the wrong type,
			// a nested record: each is hashed as its text.
			{s3("t/none"), false},
			{s3("t/none2"), false},
			{`{"f":{"s3":"t/a.txt","storage":"other"}}`, false},
			{`{"f":{"s3":"t/a.txt","storage":1}}`, false},
			{`{"f":{"x":{"s3":"t/b.txt"}}}`, false},
			{`{"f":{"x":{"s3":"t/c.txt"}}}`, false},
		})
	})

	t.Run("a flow's own flag", func(t *testing.T) {
		x := &idSteps{}
		flow := withTTL(flowOf(mod("a", "echo", inputs("f", js("flow_input.f")), "")), 60)
		flow = strings.TrimSuffix(flow, "}") + `,"cache_ignore_s3_path":true}`
		e := openWithPlatform(t, map[string]string{"f/t/main": flow}, x, &platform.Services{Objects: objects})
		for k, c := range []call{{s3("t/a.txt"), false}, {s3("t/b.txt"), true}, {s3("t/c.txt"), false}} {
			if got := runIt(t, e, c.input, nil).Cached; got != c.hit {
				t.Fatalf("call %d with %s: hit %v, want %v", k, c.input, got, c.hit)
			}
		}
	})
}

// [FX-40]/i: arguments share a key when they are equal as jsonb values:
// member order and whitespace do not matter, a number's spelling does where
// jsonb keeps it (1 and 1.0 differ, 1e3 and 1000 do not), and a $var:
// reference is hashed as its literal text.
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
func TestCacheArgumentEquality(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": flowOf()}, &scripts{})
	ctx := context.Background()
	key := func(args string) string {
		t.Helper()
		k, ok := e.cacheKey(ctx, cacheKindFlow, "f/t/main", jsonval.FromProducer([]byte(args)), false)
		if !ok {
			t.Fatalf("no key for %s", args)
		}
		return k
	}
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{`{"o":{"b":1,"a":2}}`, `{"o": {"a": 2, "b": 1}}`, true},
		{`{"x":1,"y":2}`, `{"y":2,"x":1}`, true},
		{`{"n":1}`, `{"n":1.0}`, false},
		{`{"n":1e3}`, `{"n":1000}`, true},
		{`{"v":"$var:u/test/v"}`, `{"v":"$var:u/test/w"}`, false},
	} {
		if got := key(c.a) == key(c.b); got != c.same {
			t.Errorf("%s and %s: same key %v, want %v", c.a, c.b, got, c.same)
		}
	}
	if _, ok := e.cacheKey(ctx, cacheKindFlow, "f/t/main", jsonval.FromProducer([]byte(`[1]`)), false); ok {
		t.Error("arguments that are not an object have a key")
	}
}

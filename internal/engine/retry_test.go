package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Milestone N2's engine tests: sleep, the step timeout's values and texts,
// retry and retry_if with the flow-wide retry state, the restored
// previous_result, the clock and the jitter source (H10), the monitor of
// flows that hang between steps (JE-2) and the pre-write deadline (H4), on
// the same synthetic folders and the real QuickJS-ng engine as
// engine_test.go.

// flaky answers f/t/flaky: it fails its first invocation at each site of
// each run, then returns its arguments; it answers every other script as
// scripts does.
type flaky struct {
	scripts
	mu   sync.Mutex
	seen map[string]int
}

func (f *flaky) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	if inv.Script != "f/t/flaky" {
		return f.scripts.Execute(ctx, inv)
	}
	f.scripts.mu.Lock()
	f.scripts.invs = append(f.scripts.invs, inv)
	f.scripts.mu.Unlock()
	defer inv.Report.Settled(SettleCompleted)
	f.mu.Lock()
	if f.seen == nil {
		f.seen = map[string]int{}
	}
	key := inv.Run + " " + inv.Site.String()
	f.seen[key]++
	first := f.seen[key] == 1
	f.mu.Unlock()
	if first {
		return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"message":"once","name":"Error","step_id":"` + inv.Env["WM_FLOW_STEP_ID"] + `"}`))}, nil
	}
	return StepOutcome{Kind: OutcomeOK, Body: inv.Args}, nil
}

// run runs f/t/main with req's other fields and a fixed seed.
func runReq(t *testing.T, e *Engine, req RunRequest) *Trace {
	t.Helper()
	req.Path = "f/t/main"
	if req.Args.IsZero() {
		req.Args = jsonval.FromProducer([]byte(`{}`))
	}
	if req.Seed == 0 {
		req.Seed = 1
	}
	tr, err := e.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func site(id string) conformance.SitePath {
	return conformance.SitePath{}.Child(conformance.Module(id))
}

func attemptSite(id string, k int) conformance.SitePath {
	return site(id).Child(conformance.Attempt(k))
}

// record returns the trace's record at p.
func record(t *testing.T, tr *Trace, p conformance.SitePath) StepRecord {
	t.Helper()
	rec, ok := tr.Step(p)
	if !ok {
		var sites []string
		for _, s := range tr.Steps {
			sites = append(sites, s.Site.String())
		}
		t.Fatalf("no record at %s; the trace has %v", p, sites)
	}
	return rec
}

func wantCalls(t *testing.T, s *scripts, want ...string) {
	t.Helper()
	if got := s.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

func wantSites(t *testing.T, got []conformance.SitePath, want ...conformance.SitePath) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("sites %v, want %v", got, want)
	}
}

const retryOnce = `"retry":{"constant":{"attempts":1,"seconds":0}}`

// exprOf writes a predicate's {"expr": text}.
func exprOf(text string) string {
	b, _ := json.Marshal(map[string]string{"expr": text})
	return string(b)
}

// FX-27, JE-V2: a retry re-runs the module with the failed attempt's
// recorded arguments, without evaluating its transform again; the failed
// attempt is recorded at m:a/r:0 and listed by the module's status, and
// each record names its own job.
func TestEngineRetryReusesArgs(t *testing.T) {
	f := &flaky{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "flaky", inputs("x", js("Math.random()")), retryOnce))}, f)
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, "")
	wantCalls(t, &f.scripts, "m:a", "m:a")
	r0, a := record(t, tr, attemptSite("a", 0)), step(t, tr, "a")
	if r0.Status != conformance.StepFailure || r0.Module != "" || !r0.Carried.IsZero() || r0.Attempt != 0 || len(r0.FailedRetries) != 0 {
		t.Fatalf("the failed attempt %+v", r0)
	}
	if a.Status != conformance.StepSuccess || a.Attempt != 1 {
		t.Fatalf("the retry %+v", a)
	}
	sameJSON(t, "the retry's arguments", a.RecordedArgs, string(r0.RecordedArgs.Producer()))
	sameJSON(t, "the result", tr.Result, string(r0.RecordedArgs.Producer()))
	wantSites(t, a.FailedRetries, attemptSite("a", 0))
	if r0.Job == "" || a.Job == "" || r0.Job == a.Job {
		t.Fatalf("jobs %q and %q", r0.Job, a.Job)
	}
	if f.invs[0].ID != r0.Job || f.invs[1].ID != a.Job {
		t.Fatal("the records do not name their invocations")
	}
}

// FX-29, FX-27: the retry state is flow-wide. After a tolerated failure
// the counter carries over, so b gets fewer retries, and so does the list
// of failed attempts, which b's status lists as its own; a success in
// between clears both. An attempt's site counts its place in the list.
func TestEngineRetryCounterCarriesOver(t *testing.T) {
	retry := func(n int) string { return `"retry":{"constant":{"attempts":` + string(rune('0'+n)) + `,"seconds":0}}` }
	a := mod("a", "fail", "", retry(2)+`,"continue_on_error":true`)

	s := &scripts{}
	tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(a, mod("b", "fail", "", retry(2)))}, s), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, `{"error":{"message":"boom","name":"Error","step_id":"b"}}`)
	wantCalls(t, s, "m:a", "m:a", "m:a", "m:b")
	wantSites(t, step(t, tr, "a").FailedRetries, attemptSite("a", 0), attemptSite("a", 1))
	wantSites(t, step(t, tr, "b").FailedRetries, attemptSite("a", 0), attemptSite("a", 1))

	s = &scripts{}
	tr = runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(a, mod("s", "value", inputs("v", static("0")), ""), mod("b", "fail", "", retry(2)))}, s), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, "")
	wantCalls(t, s, "m:a", "m:a", "m:a", "m:s", "m:b", "m:b", "m:b")
	wantSites(t, step(t, tr, "s").FailedRetries, attemptSite("a", 0), attemptSite("a", 1))
	wantSites(t, step(t, tr, "b").FailedRetries, attemptSite("b", 0), attemptSite("b", 1))

	// b has three attempts: the counter is at 2, so it gets one retry,
	// whose failed first attempt is the third of the list.
	s = &scripts{}
	tr = runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(a, mod("b", "fail", "", retry(3)))}, s), RunRequest{})
	wantCalls(t, s, "m:a", "m:a", "m:a", "m:b", "m:b")
	wantSites(t, step(t, tr, "b").FailedRetries, attemptSite("a", 0), attemptSite("a", 1), attemptSite("b", 2))
	if r := record(t, tr, attemptSite("b", 2)); r.Status != conformance.StepFailure || r.Attempt != 0 {
		t.Fatalf("b's failed attempt %+v", r)
	}
}

// waitsNear checks a recording clock's waits: each wait is the delay less
// the little time from scheduling to launch.
func waitsNear(t *testing.T, c *RecordClock, want ...time.Duration) {
	t.Helper()
	got := c.Waits()
	if len(got) != len(want) {
		t.Fatalf("waits %v, want %v", got, want)
	}
	for i := range got {
		if got[i] > want[i] || got[i] < want[i]-time.Second {
			t.Fatalf("waits %v, want %v", got, want)
		}
	}
}

// FX-26 through the engine, with the recording clock (H10): the delays of
// the catalogue's cases, hours long for /c to /e, recorded rather than
// slept, each as a wait span; the run records H10 among its deviations.
func TestEngineRetryDelays(t *testing.T) {
	for _, c := range []struct {
		name, retry string
		want        []time.Duration
	}{
		{"FX-26/a", `{"constant":{"attempts":2,"seconds":1},"exponential":{"attempts":2,"multiplier":1,"seconds":2}}`, []time.Duration{time.Second, time.Second, 8 * time.Second, 16 * time.Second}},
		{"FX-26/b", `{"exponential":{"attempts":1,"multiplier":3,"seconds":2}}`, []time.Duration{6 * time.Second}},
		{"FX-26/c", `{"exponential":{"attempts":1,"multiplier":250,"seconds":300}}`, []time.Duration{9464 * time.Second}},
		{"FX-26/d", `{"exponential":{"attempts":1,"multiplier":1,"seconds":30000}}`, []time.Duration{21600 * time.Second}},
		{"FX-26/e", `{"exponential":{"attempts":2,"multiplier":1,"seconds":300}}`, []time.Duration{300 * time.Second, 21600 * time.Second}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "fail", "", `"retry":`+c.retry))}, s)
			clock := &RecordClock{}
			start := time.Now()
			tr := runReq(t, e, RunRequest{Clock: clock})
			if time.Since(start) > 5*time.Second {
				t.Fatal("the recording clock slept")
			}
			wantStatus(t, tr, conformance.RunFailure, wrappedBoom)
			if got := len(s.calls()); got != len(c.want)+1 {
				t.Fatalf("%d runs, want %d", got, len(c.want)+1)
			}
			waitsNear(t, clock, c.want...)
			waitSpans := 0
			for _, sp := range tr.Spans {
				if sp.Kind == SpanWait {
					waitSpans++
				}
			}
			if waitSpans != len(c.want) || !slices.Contains(tr.Deviations, "H10") {
				t.Fatalf("%d wait spans, deviations %v", waitSpans, tr.Deviations)
			}
		})
	}
	// A real clock is no deviation, and a delay of 0 is no wait.
	tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "fail", "", retryOnce))}, &scripts{}), RunRequest{})
	if slices.Contains(tr.Deviations, "H10") || slices.ContainsFunc(tr.Spans, func(s Span) bool { return s.Kind == SpanWait }) {
		t.Fatalf("deviations %v, spans %+v", tr.Deviations, tr.Spans)
	}
}

// FX-26, H10: the exponential phase's jitter comes from the run's jitter
// source, or from its seed, so a fixed seed fixes it.
func TestEngineRetryJitter(t *testing.T) {
	jittered := flowOf(mod("a", "fail", "", `"retry":{"exponential":{"attempts":3,"multiplier":1,"seconds":100,"random_factor":50}}`))
	e := mustOpen(t, map[string]string{"f/t/main": jittered}, &scripts{})
	clock := &RecordClock{}
	runReq(t, e, RunRequest{Clock: clock, Jitter: &fixedJitter{p: 20, up: true}})
	// 100 + 20 %; 10000 + (10000 × 20 mod 65536) / 100 = 10033, the
	// product wrapping; 100^3 saturates at 65535, and the cap applies.
	waitsNear(t, clock, 120*time.Second, 10033*time.Second, 21600*time.Second)

	small := flowOf(mod("a", "fail", "", `"retry":{"exponential":{"attempts":3,"multiplier":1,"seconds":2,"random_factor":100}}`))
	e = mustOpen(t, map[string]string{"f/t/main": small}, &scripts{})
	waits := func(seed uint64) []time.Duration {
		c := &RecordClock{}
		runReq(t, e, RunRequest{Clock: c, Seed: seed})
		var out []time.Duration
		for _, w := range c.Waits() {
			out = append(out, w.Round(time.Second))
		}
		return out
	}
	if a, b := waits(42), waits(42); !slices.Equal(a, b) {
		t.Fatalf("one seed, two jitters: %v and %v", a, b)
	}
}

// FX-28, JE-32: retry_if is evaluated before the attempts check, with the
// failed attempt's value as result and error, and no results. False
// prevents the retry; a throw while the completion is decided makes the
// failure unrecoverable with the predicate's text as a string (as FX-21),
// which the failure module sees and continue_on_error does not tolerate.
func TestEngineRetryIf(t *testing.T) {
	retryIf := func(expr string) string {
		return `"retry":{"constant":{"attempts":3,"seconds":0},"retry_if":` + exprOf(expr) + `}`
	}
	for _, c := range []struct {
		name, expr string
		runs       int
	}{
		{"false", "error.message === 'transient'", 1},
		{"true", "error.message === 'boom' && result.error.name === 'Error'", 4},
		{"no results", "typeof results === 'undefined'", 4},
	} {
		s := &scripts{}
		tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "fail", "", retryIf(c.expr)))}, s), RunRequest{})
		wantStatus(t, tr, conformance.RunFailure, wrappedBoom)
		if got := len(s.calls()); got != c.runs {
			t.Errorf("%s: %d runs, want %d", c.name, got, c.runs)
		}
	}

	// Converted twice: two "Error: " prefixes (FX-50; Windmill's two
	// location suffixes are D14's).
	const thrown = "Error: Error: QuickJS evaluation error: Error: cannot read property 'x' of null\n"
	s := &scripts{}
	tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": withFailure(handler("echo", inputs("e", js("previous_result")), ""),
		mod("a", "fail", "", retryIf("null.x")+`,"continue_on_error":true`))}, s), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, "")
	wantCalls(t, s, "m:a", "s:failure")
	a := step(t, tr, "a")
	var carried struct{ Error string }
	if err := json.Unmarshal(a.Carried.Producer(), &carried); err != nil || !strings.HasPrefix(carried.Error, thrown) || a.Module != conformance.ModuleFailure || a.CarriedOrigin != OriginEngine {
		t.Fatalf("a %+v (%v)", a, err)
	}
	f, _ := failureStep(tr)
	sameJSON(t, "the failure module's arguments", f.RecordedArgs, `{"e":`+string(a.Carried.Producer())+`}`)

	s = &scripts{}
	tr = runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "fail", "", retryIf("null.x")))}, s), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, string(step(t, tr, "a").Carried.Producer()))
	wantCalls(t, s, "m:a")

	// An error stop with a failure module re-enters the module, where
	// retry_if is first evaluated: its throw is then a chaining error of a
	// later transition (FX-15 context 1), and the failure module does not
	// run.
	s = &scripts{}
	tr = runReq(t, mustOpen(t, map[string]string{"f/t/main": withFailure(handler("echo", "", ""),
		mod("a", "value", inputs("v", static("1")), retryIf("null.x")+`,"stop_after_if":{"expr":"true","skip_if_stopped":false,"error_message":"E"}`))}, s), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, "")
	if got := memberOf(t, tr.Result, "error"); !strings.Contains(got, `"name":"InternalError"`) || !strings.Contains(got, "cannot read property") {
		t.Fatalf("result %s", tr.Result)
	}
	wantCalls(t, s, "m:a")
}

// FX-19: an error stop and a retry. With a failure module, re-entry
// retries the module before the failure module runs, which then lists the
// failed attempt; under continue_on_error a retry due keeps the cursor,
// and without a failure module the flow then fails.
func TestEngineErrorStopRetries(t *testing.T) {
	stop := `"stop_after_if":{"expr":"true","skip_if_stopped":false,"error_message":"E"}`
	const early = `{"error":{"name":"EarlyStopError","message":"E"}}`
	a := mod("a", "value", inputs("v", static("1")), retryOnce+","+stop)

	s := &scripts{}
	tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": withFailure(handler("value", inputs("v", static(`"H"`)), ""), a)}, s), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, `"H"`)
	wantCalls(t, s, "m:a", "m:a", "s:failure")
	f, _ := failureStep(tr)
	wantSites(t, f.FailedRetries, attemptSite("a", 0))

	coe := mod("a", "value", inputs("v", static("1")), retryOnce+","+stop+`,"continue_on_error":true`)
	s = &scripts{}
	tr = runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(coe, mod("b", "value", "", ""))}, s), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, early)
	wantCalls(t, s, "m:a")

	// With a failure module the retry happens; the second stop, with no
	// attempt left, is tolerated at the last module, where the failure
	// module runs at cursor len (FX-V4).
	s = &scripts{}
	tr = runReq(t, mustOpen(t, map[string]string{"f/t/main": withFailure(handler("value", inputs("v", static(`"H"`)), ""), coe)}, s), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, `"H"`)
	wantCalls(t, s, "m:a", "m:a", "s:failure")
}

// FX-32, FX-27: the failure module's own retry applies to its failures,
// and starting it resets the counter, so a module's exhausted retries do
// not use up the failure module's; its status lists both modules' failed
// attempts.
func TestEngineFailureModuleRetry(t *testing.T) {
	f := &flaky{}
	e := mustOpen(t, map[string]string{"f/t/main": withFailure(handler("flaky", inputs("v", static("1")), retryOnce), mod("a", "fail", "", retryOnce))}, f)
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, `{"v":1}`)
	wantCalls(t, &f.scripts, "m:a", "m:a", "s:failure", "s:failure")
	fm, _ := failureStep(tr)
	if fm.Status != conformance.StepSuccess || fm.Attempt != 1 {
		t.Fatalf("the failure module %+v", fm)
	}
	wantSites(t, fm.FailedRetries, attemptSite("a", 0), failureSite.Child(conformance.Attempt(1)))
}

// FX-6, JE-36, JE-44: a retry restores previous_result to the preceding
// module's stored result, read back in jsonb's printed form (keys in jsonb
// order), when that module succeeded; after a tolerated failure it is the
// JSON string "{}". skip_if sees it: here it skips the retry, which
// returns its reused arguments' previous_result field, absent, so {}.
func TestEngineRestoredPreviousResult(t *testing.T) {
	retried := func(skip string) string {
		return mod("b", "flaky", inputs("p", js("previous_result")), retryOnce+`,"skip_if":`+exprOf(skip))
	}
	f := &flaky{}
	tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "obj", "", ""), retried("Object.keys(previous_result)[0] === 'b'"))}, f), RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `{}`)
	wantCalls(t, &f.scripts, "m:a", "m:b")
	if b := step(t, tr, "b"); !b.Skipped || b.Attempt != 1 {
		t.Fatalf("b %+v", b)
	}

	f = &flaky{}
	tr = runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "fail", "", `"continue_on_error":true`), retried(`previous_result === "{}"`))}, f), RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `{}`)
	wantCalls(t, &f.scripts, "m:a", "m:b")
}

// FX-23, JE-28, JE-22: a module's sleep delays the next module's job, on
// the run's clock: static seconds, a javascript fraction of the module's
// own result; it still applies after a skipped module and after a
// tolerated failure, and a final module's sleep runs in the virtual a-v
// step, which keeps a's result; never on a retry, and never before the
// failure module. Its failures are chaining errors.
func TestEngineSleep(t *testing.T) {
	sleep := func(t string) string { return `"sleep":` + t }
	for _, c := range []struct {
		name, flow string
		status     conformance.RunStatus
		result     string
		waits      []time.Duration
	}{
		{"static", flowOf(mod("a", "value", inputs("v", static("1")), sleep(static("2"))), mod("b", "value", inputs("v", static("2")), "")), conformance.RunSuccess, "2", []time.Duration{2 * time.Second}},
		{"own result", flowOf(mod("a", "value", inputs("v", static(`{"s":1.5}`)), sleep(js("result.s"))), mod("b", "value", inputs("v", static("2")), "")), conformance.RunSuccess, "2", []time.Duration{1500 * time.Millisecond}},
		{"after a skip", flowOf(mod("a", "value", "", sleep(static("1"))+`,"skip_if":{"expr":"true"}`), mod("b", "value", inputs("v", static("2")), "")), conformance.RunSuccess, "2", []time.Duration{time.Second}},
		{"after a tolerated failure", flowOf(mod("a", "fail", "", sleep(static("1"))+`,"continue_on_error":true`), mod("b", "value", inputs("v", static("2")), "")), conformance.RunSuccess, "2", []time.Duration{time.Second}},
		{"last module", flowOf(mod("a", "value", inputs("v", static("7")), sleep(static("1")))), conformance.RunSuccess, "7", []time.Duration{time.Second}},
		{"not on a retry", flowOf(mod("a", "value", "", sleep(static("3"))), mod("b", "flaky", "", `"retry":{"constant":{"attempts":1,"seconds":2}}`)), conformance.RunSuccess, "{}", []time.Duration{3 * time.Second, 2 * time.Second}},
		{"not before the failure module", withFailure(handler("value", inputs("v", static(`"H"`)), ""), mod("a", "fail", "", sleep(static("5")))), conformance.RunFailure, `"H"`, nil},
		{"a string", flowOf(mod("a", "value", "", sleep(static(`"1"`))), mod("b", "value", "", "")), conformance.RunFailure, `{"error":{"message":"Expected an integer, found: String(\"1\")","name":"InternalError"}}`, nil},
		{"a negative integer", flowOf(mod("a", "value", "", sleep(static("-1"))), mod("b", "value", "", "")), conformance.RunFailure, `{"error":{"message":"Expected an integer, found: -1","name":"InternalError"}}`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, &flaky{})
			clock := &RecordClock{}
			tr := runReq(t, e, RunRequest{Clock: clock})
			wantStatus(t, tr, c.status, c.result)
			waitsNear(t, clock, c.waits...)
		})
	}

	// A failed evaluation: the debug form of the evaluation error, its
	// newlines escaped; there is no results (JE-22).
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", "", `"sleep":`+js("results.a")), mod("b", "value", "", ""))}, &scripts{})
	tr := runReq(t, e, RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, "")
	var res struct {
		Error struct{ Name, Message string }
	}
	if err := json.Unmarshal(tr.Result.Producer(), &res); err != nil {
		t.Fatal(err)
	}
	const want = "Expected a number value, had error instead: ExecutionErr(\"Error during evaluation of expression `results.a`:\\nQuickJS evaluation error: Error: results is not defined\\n"
	if res.Error.Name != "InternalError" || !strings.HasPrefix(res.Error.Message, want) || !strings.HasSuffix(res.Error.Message, `\n")`) {
		t.Fatalf("result %s", tr.Result)
	}
	if b := step(t, tr, "b"); b.Status != conformance.StepNotRun {
		t.Fatalf("b %+v", b)
	}
}

// FX-23, FX-38: a cancellation cuts a pending sleep short; the queued job
// fails without running, with the Canceled error, a failed and not a
// canceled job (as recorded), and the run ends canceled.
func TestEngineSleepCanceled(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", "", `"sleep":`+static("30")), mod("b", "value", inputs("v", static("2")), ""))}, &scripts{})
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(100*time.Millisecond, func() { cancel(&Cancellation{Reason: "r", Canceler: "u"}) })
	start := time.Now()
	tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the sleep was not cut short")
	}
	const canceled = `{"error":{"message":"Job canceled: r by u","name":"Canceled","reason":"r","canceler":"u"}}`
	wantStatus(t, tr, conformance.RunCanceled, canceled)
	b := step(t, tr, "b")
	if b.Status != conformance.StepFailure || !b.Ran || b.Module != conformance.ModuleFailure {
		t.Fatalf("b %+v", b)
	}
	sameJSON(t, "b's result", b.Result, canceled)
	sameJSON(t, "b's arguments", b.RecordedArgs, `{"v":2}`)
}

// FX-24, JE-29, FX-15: every module evaluates its own timeout when its job
// is pushed, identity jobs and skipped modules included; a javascript one
// sees results, and its value must be an i32. A failure is a chaining
// error: an Anyhow failure of the module at the root's first pickup, which
// a retry re-evaluates at a later transition, and otherwise the flow's
// InternalError. A step's leaf job is timed with the evaluated value.
func TestEngineTimeoutValues(t *testing.T) {
	timeout := func(t string) string { return `"timeout":` + t }
	internal := func(msg string) string {
		b, _ := json.Marshal(map[string]map[string]string{"error": {"message": msg, "name": "InternalError"}})
		return string(b)
	}
	for _, c := range []struct {
		name, flow, result string
		calls              []string
	}{
		{"a float", flowOf(mod("x", "value", "", ""), mod("a", "value", "", timeout(js("1.5")))),
			internal("Error parsing result as i32: invalid type: floating point `1.5`, expected i32 at line 1 column 3. Value was: 1.5"), []string{"m:x"}},
		{"negative static", flowOf(mod("x", "value", "", ""), mod("a", "value", "", timeout(static("-1")))),
			internal("Timeout value cannot be negative"), []string{"m:x"}},
		{"an identity job's", flowOf(mod("x", "value", "", ""), `{"id":"a","value":{"type":"identity"},`+timeout(js("-1"))+`}`),
			internal("Timeout value cannot be negative"), []string{"m:x"}},
		{"a skipped module's", flowOf(mod("x", "value", "", ""), mod("a", "value", "", timeout(static("-1"))+`,"skip_if":{"expr":"true"}`)),
			internal("Timeout value cannot be negative"), []string{"m:x"}},
		{"after a failed transform", flowOf(mod("x", "value", "", ""), mod("a", "value", inputs("v", js("null.x")), timeout(static("-1")))),
			internal("Timeout value cannot be negative"), []string{"m:x"}},
		{"a failed evaluation", flowOf(mod("x", "value", "", ""), mod("a", "value", "", timeout(js("nope")))),
			"", []string{"m:x"}},
		{"at the root", flowOf(mod("a", "value", "", timeout(js("-1")))),
			`{"error":{"message":"Error: Timeout value cannot be negative","name":"Anyhow"}}`, nil},
		{"retried from the root", flowOf(mod("a", "value", "", timeout(js("-1"))+","+retryOnce)),
			internal("Timeout value cannot be negative"), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": c.flow}, s), RunRequest{})
			wantStatus(t, tr, conformance.RunFailure, c.result)
			wantCalls(t, s, c.calls...)
		})
	}
	s := &scripts{}
	tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(mod("x", "value", "", ""), mod("a", "value", "", timeout(js("nope"))))}, s), RunRequest{})
	if got := memberOf(t, tr.Result, "error"); !strings.Contains(got, "Error during evaluation of expression `nope`:\\nQuickJS evaluation error: Error: nope is not defined") {
		t.Fatalf("result %s", tr.Result)
	}
	// The retried root failure: the first attempt failed with no job, and
	// the retry, a later transition, fails the flow at once.
	tr = runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", "", timeout(js("-1"))+","+retryOnce))}, &scripts{}), RunRequest{})
	if r0 := record(t, tr, attemptSite("a", 0)); r0.Ran || r0.Status != conformance.StepFailure {
		t.Fatalf("the failed pickup %+v", r0)
	}

	// The evaluated timeout times the leaf: results.a, read from the
	// intercepted a, is 0, which leaves b no time (job-specific).
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", "", ""), mod("b", "value", "", timeout(js("results.a"))))}, waits{})
	tr = runReq(t, e, RunRequest{Hooks: StaticIntercepts{Results: map[string]jsonval.Value{"a": jsonval.FromProducer([]byte("0"))}}})
	wantStatus(t, tr, conformance.RunFailure, `{"error":{"message":"execution error:\njob process terminated due to timeout after exceeding job-specific duration limit","name":"ExecutionErr","step_id":"b"}}`)
}

// JE-2: the time a transition spends evaluating counts against the
// transition timeout, past which the run is canceled by "monitor", as
// Windmill's monitor of flows that hang between steps cancels them. A job
// starts a new transition; a negative timeout disables the monitor.
func TestEngineTransitionMonitor(t *testing.T) {
	// Each busy evaluation takes 400 ms against a budget of 600 ms: one
	// fits a transition with room to spare under load, two do not.
	const busy = "(() => { const t = Date.now(); while (Date.now() - t < 400) {} return 1 })()"
	for _, c := range []struct {
		name, flow string
		limit      time.Duration
		status     conformance.RunStatus
	}{
		{"a runaway transform", flowOf(mod("a", "value", inputs("v", js("(() => { while (true) {} })()")), "")), 600 * time.Millisecond, conformance.RunCanceled},
		{"one transition at a time", flowOf(mod("a", "value", inputs("v", js(busy)), ""), mod("b", "value", inputs("v", js(busy)), "")), 600 * time.Millisecond, conformance.RunSuccess},
		{"two evaluations in one transition", flowOf(mod("a", "value", inputs("v", js(busy), "w", js(busy)), "")), 600 * time.Millisecond, conformance.RunCanceled},
		{"disabled", flowOf(mod("a", "value", inputs("v", js(busy), "w", js(busy)), "")), -1, conformance.RunSuccess},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, &scripts{})
			e.transition = max(c.limit, 0)
			start := time.Now()
			tr := runReq(t, e, RunRequest{})
			wantStatus(t, tr, c.status, "")
			if time.Since(start) > 10*time.Second {
				t.Fatalf("the run took %v", time.Since(start))
			}
			if c.status != conformance.RunCanceled {
				return
			}
			var res struct {
				Error struct{ Name, Canceler, Reason string }
			}
			if err := json.Unmarshal(tr.Result.Producer(), &res); err != nil {
				t.Fatal(err)
			}
			if res.Error.Name != "Canceled" || res.Error.Canceler != "monitor" || !strings.HasPrefix(res.Error.Reason, "Flow "+tr.RunID+" was cancelled because it was hanging in between 2 steps. Last ping: ") {
				t.Fatalf("result %s", tr.Result)
			}
			if a := step(t, tr, "a"); a.Status != conformance.StepNotRun {
				t.Fatalf("a %+v", a)
			}
		})
	}
	if e := mustOpen(t, map[string]string{"f/t/main": flowOf()}, &scripts{}); e.transition != 60*time.Second {
		t.Fatalf("the default transition timeout is %v", e.transition)
	}
}

// writes marks modules write-capable (H3).
type writes struct {
	NoHooks
	at map[string]bool
}

func (w writes) WriteCapable(s Site) bool { return w.at[s.Module] }

// slowApproval is a host whose BeforeFirstWrite takes its time.
type slowApproval struct {
	writes
	wait time.Duration
}

func (h slowApproval) BeforeFirstWrite(context.Context, Run, Site) error {
	time.Sleep(h.wait)
	return nil
}

// H4: a pre-write deadline that passes before the run's first
// write-capable launch ends the run canceled, here during a sleep, or
// while the host decides on that write, which then never launches; once
// the first write launched it no longer applies.
func TestEnginePreWriteDeadline(t *testing.T) {
	flow := flowOf(mod("a", "value", inputs("v", static("1")), `"sleep":`+static("0.5")), mod("b", "value", inputs("v", static("2")), `"sleep":`+static("0.5")), mod("c", "value", inputs("v", static("3")), ""))
	e := mustOpen(t, map[string]string{"f/t/main": flow}, &scripts{})
	tr := runReq(t, e, RunRequest{Hooks: writes{at: map[string]bool{"c": true}}, PreWriteDeadline: time.Now().Add(100 * time.Millisecond)})
	wantStatus(t, tr, conformance.RunCanceled, `{"error":{"message":"Job canceled: no reason given by unknown","name":"Canceled","reason":"no reason given","canceler":"unknown"}}`)
	if tr.MayHaveMutated {
		t.Fatal("the canceled run reports a write")
	}
	tr = runReq(t, e, RunRequest{Hooks: writes{at: map[string]bool{"a": true}}, PreWriteDeadline: time.Now().Add(100 * time.Millisecond)})
	wantStatus(t, tr, conformance.RunSuccess, "3")
	if !tr.MayHaveMutated {
		t.Fatal("the write is not reported")
	}

	s := &scripts{}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", "", ""))}, s)
	tr = runReq(t, e, RunRequest{Hooks: slowApproval{writes{at: map[string]bool{"a": true}}, 200 * time.Millisecond}, PreWriteDeadline: time.Now().Add(50 * time.Millisecond)})
	wantStatus(t, tr, conformance.RunCanceled, "")
	wantCalls(t, s)
	if a := step(t, tr, "a"); tr.MayHaveMutated || a.Ran {
		t.Fatalf("the write launched after the deadline: %+v, mutated %t", a, tr.MayHaveMutated)
	}
}

// JE-2: the monitor's budget is wall time, so a recording clock that
// moves on with another run's waits does not use it up.
func TestEngineMonitorIgnoresTheRunsClock(t *testing.T) {
	two := inputs("v", js("1 + 1"), "w", js("2 + 2"))
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", two, ""), mod("b", "value", two, ""))}, &scripts{})
	clock := &RecordClock{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = clock.Wait(context.Background(), time.Hour)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for range 5 {
		wantStatus(t, runReq(t, e, RunRequest{Clock: clock}), conformance.RunSuccess, "")
	}
}

// H4: a pre-write deadline that has passed when the run starts cancels it
// before anything launches, however the scheduler delivers timers.
func TestEnginePreWriteDeadlinePassed(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	s := &scripts{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", inputs("v", static("1")), ""))}, s)
	for range 10 {
		tr := runReq(t, e, RunRequest{Hooks: writes{at: map[string]bool{"a": true}}, PreWriteDeadline: time.Now().Add(-time.Second)})
		wantStatus(t, tr, conformance.RunCanceled, "")
		if tr.MayHaveMutated {
			t.Fatal("a write launched after the deadline")
		}
	}
	wantCalls(t, s)
}

// H4: the actual time decides between the first write and the pre-write
// deadline, never the delivery of the deadline's timer: a deadline that
// has passed expires, and cancels the run, when the first write would
// disarm it; one still ahead is disarmed, and expiring it later does
// nothing.
func TestPreWriteDeadlineStates(t *testing.T) {
	var cause error
	r := &run{cancel: func(err error) { cause = err }}
	r.preWriteAt = time.Now().Add(-time.Millisecond)
	if r.disarmPreWrite() || r.preWrite.Load() != preWriteExpired || cause != ErrPreWriteDeadline {
		t.Fatalf("a passed deadline: state %d, cause %v", r.preWrite.Load(), cause)
	}
	cause = nil
	r = &run{cancel: func(err error) { cause = err }}
	r.preWriteAt = time.Now().Add(time.Hour)
	if !r.disarmPreWrite() || r.preWrite.Load() != preWriteDisarmed {
		t.Fatalf("a deadline ahead: state %d", r.preWrite.Load())
	}
	if r.expirePreWrite() || cause != nil {
		t.Fatalf("the timer expired a disarmed deadline: cause %v", cause)
	}
	// With no deadline, nothing is due.
	if r = (&run{}); r.preWriteDue() || !r.disarmPreWrite() {
		t.Fatal("no deadline stops a write")
	}
}

// JE-2: a child flow's completion refreshes its loop's flow's ping, so a
// loop whose every transition fits the budget is not canceled however many
// child flows it completes, while one transition over the budget is.
func TestEngineMonitorRefreshedByChildFlows(t *testing.T) {
	busy := func(ms int) string {
		return `"stop_after_if":{"skip_if_stopped":false,"expr":` + string(jsonString(fmt.Sprintf("((t) => { while (Date.now() - t < %d) {} })(Date.now()) || false", ms))) + `}`
	}
	body := []string{mod("a", "value", inputs("v", static("1")), ""), mod("b", "value", inputs("v", static("2")), "")}
	for _, c := range []struct {
		name   string
		ms     int
		status conformance.RunStatus
	}{
		// Five iterations of 150 ms add up to 750 ms, which the budget of
		// 400 ms would cancel were the ping not refreshed.
		{"each transition fits", 150, conformance.RunSuccess},
		{"one transition does not", 600, conformance.RunCanceled},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(loopOf("l", body, `"iterator":{"type":"static","value":[1,2,3,4,5]}`, busy(c.ms)))}, s)
			e.transition = 400 * time.Millisecond
			tr := runReq(t, e, RunRequest{})
			wantStatus(t, tr, c.status, "")
			if c.status == conformance.RunSuccess && len(s.calls()) != 10 {
				t.Fatalf("calls %v", s.calls())
			}
		})
	}
}

// JE-15, JE-28, JE-29: a replaced JSON.stringify hands sleep and timeout
// text that is not JSON, which fails with serde_json's syntax errors (the
// helpers of serde.go): a broken or truncated literal, a leading zero, a
// value on a later line.
func TestEngineTimeValuesNotJSON(t *testing.T) {
	stringify := func(text string) string {
		return js(`(JSON.stringify = () => ` + string(jsonString(text)) + `, 1)`)
	}
	for _, c := range []struct {
		name, flow, want string
	}{
		{"sleep, a broken literal", flowOf(mod("a", "value", "", `"sleep":`+stringify("nope")), mod("b", "value", "", "")), "expected ident at line 1 column 2"},
		{"sleep, a truncated literal", flowOf(mod("a", "value", "", `"sleep":`+stringify("tru")), mod("b", "value", "", "")), "EOF while parsing a value at line 1 column 3"},
		{"sleep, a leading zero", flowOf(mod("a", "value", "", `"sleep":`+stringify("01")), mod("b", "value", "", "")), "invalid number at line 1 column 2"},
		{"timeout, a broken literal", flowOf(mod("a", "value", "", ""), mod("b", "value", "", `"timeout":`+stringify("nope"))), "Error parsing result as i32: expected ident at line 1 column 2. Value was: nope"},
		{"timeout, a leading zero", flowOf(mod("a", "value", "", ""), mod("b", "value", "", `"timeout":`+stringify("01"))), "Error parsing result as i32: invalid number at line 1 column 2. Value was: 01"},
		{"timeout, a later line", flowOf(mod("a", "value", "", ""), mod("b", "value", "", `"timeout":`+stringify("\n  x"))), "Error parsing result as i32: expected value at line 2 column 3. Value was: \n  x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": c.flow}, &scripts{}), RunRequest{})
			var res struct {
				Error struct{ Name, Message string }
			}
			if err := json.Unmarshal(tr.Result.Producer(), &res); err != nil {
				t.Fatal(err)
			}
			if res.Error.Name != "InternalError" || res.Error.Message != c.want {
				t.Fatalf("got %s %q, want %q", res.Error.Name, res.Error.Message, c.want)
			}
		})
	}
}

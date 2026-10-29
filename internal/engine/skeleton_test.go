package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// fakeSteps runs the W1 bundle's scripts in Go: enough of their behaviour
// to check the interpreter without a harness. skeleton_bun_test.go runs the
// same flows on the real Bun harness.
type fakeSteps struct {
	mu   sync.Mutex
	invs []Invocation
}

func (f *fakeSteps) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	f.mu.Lock()
	f.invs = append(f.invs, inv)
	f.mu.Unlock()
	switch inv.Script {
	case "f/w1/echo", "f/w1/transforms/b":
		inv.Report.Settled(SettleCompleted)
		return StepOutcome{Kind: OutcomeOK, Body: inv.Args}, nil
	case "f/w1/throws":
		inv.Report.Settled(SettleCompleted)
		// The wrapper's object carries the step id it read from
		// WM_FLOW_STEP_ID (WR-18).
		return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"message":"boom","name":"Error","stack":"Error: boom","step_id":"` + inv.Env["WM_FLOW_STEP_ID"] + `"}`))}, nil
	case "f/w1/escapes":
		inv.Report.Settled(SettleCompleted)
		return StepOutcome{Kind: OutcomeEscaped, Tail: "last line\n", ExitCode: 1}, nil
	case "f/w1/never":
		<-ctx.Done()
		// Abandoned; it settles later, as a harness would report.
		go inv.Report.Settled(SettleCompleted)
		return StepOutcome{}, context.Cause(ctx)
	}
	inv.Report.Settled(SettleCompleted)
	return StepOutcome{}, errors.New("fake: no script " + inv.Script)
}

func (f *fakeSteps) scripts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s []string
	for _, inv := range f.invs {
		s = append(s, inv.Script)
	}
	return s
}

func skeleton(t *testing.T, exec StepExecutor, edit func(*SkeletonConfig)) *Skeleton {
	t.Helper()
	jsOnce.Do(func() { jsEng, jsErr = newTestJS() })
	if jsErr != nil {
		t.Fatal(jsErr)
	}
	cfg := SkeletonConfig{BundleDir: "testdata/w1", Bundle: "sha256:test", Exec: exec, JS: jsEng}
	if edit != nil {
		edit(&cfg)
	}
	s, err := NewSkeleton(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func runFlow(t *testing.T, s *Skeleton, path, args string, hooks Hooks) *Trace {
	t.Helper()
	tr, err := s.Run(context.Background(), SkeletonRun{Path: path, Args: jsonval.FromProducer([]byte(args)), Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func step(t *testing.T, tr *Trace, id string) StepRecord {
	t.Helper()
	rec, ok := tr.Step(conformance.SitePath{}.Child(conformance.Module(id)))
	if !ok {
		t.Fatalf("no step %s in %+v", id, tr.Steps)
	}
	return rec
}

// JE-40: a static value is copied verbatim, ${} included and number spelling
// kept; JE-13: expression mode, statement mode, and an object literal in
// expression mode; JE-15: undefined becomes null; JE-23: params is {};
// FX-6: previous_result is the step before's result; FX-36: the last
// step's result is the flow's, stored as jsonb.
func TestSkeletonTransforms(t *testing.T) {
	f := &fakeSteps{}
	tr := runFlow(t, skeleton(t, f, nil), "f/w1/transforms", `{"name":"ada","n":2}`, nil)
	if tr.Status != conformance.RunSuccess {
		t.Fatalf("status %s, result %s", tr.Status, tr.Result)
	}
	// The arguments' top-level order is shuffled per run (ADR 0001 §2), so
	// they compare as values; each argument is in its stored form, which
	// keeps a plain decimal's digits (JE-44).
	a := step(t, tr, "a")
	const wantArgs = `{"greeting":"hello ${flow_input.name}","n":1.50,"name":"ADA","sum":3,"obj":{"k":2},"params":{}}`
	sameJSON(t, "a's arguments", a.ResolvedArgs, wantArgs)
	if !strings.Contains(string(a.ResolvedArgs.Producer()), `"n":1.50`) {
		t.Fatalf("a static number keeps its spelling: %s", a.ResolvedArgs)
	}
	// The text previous_result is a context name, so JE-6's shortcut
	// answers it with a's result verbatim, never through the engine: the
	// number keeps its spelling.
	b := step(t, tr, "b")
	sameJSON(t, "b's arguments", b.ResolvedArgs, `{"prev":`+wantArgs+`,"missing":null}`)
	if !strings.Contains(string(b.ResolvedArgs.Producer()), `"n":1.50`) {
		t.Fatalf("JE-6 answers previous_result verbatim: %s", b.ResolvedArgs)
	}
	// jsonb's order: shorter keys first, then bytewise (ADR 0005).
	if got, want := string(tr.Result.Producer()), `{"prev":{"n":1.50,"obj":{"k":2},"sum":3,"name":"ADA","params":{},"greeting":"hello ${flow_input.name}"},"missing":null}`; got != want {
		t.Fatalf("the result is not the last step's stored form\n got %s\nwant %s", got, want)
	}
	if got := f.scripts(); strings.Join(got, ",") != "f/w1/echo,f/w1/transforms/b" {
		t.Fatalf("scripts %v", got)
	}
	inv := f.invs[0]
	if inv.Entry != "main" || inv.Run != tr.RunID || inv.Env["WM_FLOW_JOB_ID"] != tr.RunID || inv.Env["WM_JOB_ID"] != inv.ID ||
		inv.Env["WM_FLOW_STEP_ID"] != "a" || inv.Env["WM_FLOW_PATH"] != "f/w1/transforms" || inv.Env["WM_JOB_PATH"] != "f/w1/echo" {
		t.Fatalf("invocation %+v", inv)
	}
	if f.invs[1].Env["WM_JOB_PATH"] != "f/w1/transforms/b" {
		t.Fatalf("an inline step's job path is <flow path>/<id> (WR-22, WR-29): %+v", f.invs[1].Env)
	}
}

// FX-24, WR-37: a step timeout is a timer in the engine; the step is
// abandoned with cause timeout and fails with Windmill's text.
func TestSkeletonStepTimeout(t *testing.T) {
	for _, tc := range []struct {
		cap  time.Duration
		want string
	}{
		{0, "job-specific duration limit"},
		// A request at or above the instance maximum gets the maximum and
		// the instance-wide wording.
		{time.Second, "instance-wide job duration limit"},
	} {
		s := skeleton(t, &fakeSteps{}, func(c *SkeletonConfig) { c.InstanceTimeout = tc.cap })
		start := time.Now()
		tr := runFlow(t, s, "f/w1/timeout", `{}`, nil)
		if d := time.Since(start); d < 900*time.Millisecond || d > 3*time.Second {
			t.Fatalf("the run took %v; the timeout is 1 s", d)
		}
		want := `{"error":{"message":"execution error:\njob process terminated due to timeout after exceeding ` + tc.want + `","name":"ExecutionErr","step_id":"a"}}`
		if tr.Status != conformance.RunFailure || string(tr.Result.Producer()) != storedOf(t, want) {
			t.Fatalf("status %s, result %s; want %s", tr.Status, tr.Result, want)
		}
		if a := step(t, tr, "a"); !a.Abandoned || a.Status != conformance.StepFailure || a.ResultOrigin != OriginEngine {
			t.Fatalf("step %+v", a)
		}
	}
}

func storedOf(t *testing.T, text string) string {
	t.Helper()
	s, err := jsonval.FromProducer([]byte(text)).Stored()
	if err != nil {
		t.Fatal(err)
	}
	return string(s)
}

type interceptB struct {
	NoHooks
	mu   sync.Mutex
	args string
}

func (h *interceptB) Intercept(_ context.Context, _ Run, s Site, args jsonval.Value) (StepOutcome, bool, error) {
	if s.Module != "b" {
		return StepOutcome{}, false, nil
	}
	h.mu.Lock()
	h.args = string(args.Producer())
	h.mu.Unlock()
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`{"stood":"in"}`))}, true, nil
}

// H1 (ADR 0008): Intercept runs at launch with the evaluated arguments; its
// outcome stands in for the module, which never runs, and the completion
// phase goes on as if it had.
func TestSkeletonIntercept(t *testing.T) {
	f := &fakeSteps{}
	h := &interceptB{}
	tr := runFlow(t, skeleton(t, f, nil), "f/w1/intercept", `{"x":7}`, h)
	if tr.Status != conformance.RunSuccess || string(tr.Result.Producer()) != `{"from_b":{"stood":"in"}}` {
		t.Fatalf("status %s, result %s", tr.Status, tr.Result)
	}
	if h.args != `{"from_a":{"x":7}}` {
		t.Fatalf("Intercept saw %s", h.args)
	}
	if got := f.scripts(); strings.Join(got, ",") != "f/w1/echo,f/w1/echo" {
		t.Fatalf("scripts that ran: %v", got)
	}
	if b := step(t, tr, "b"); b.Ran || len(b.Hooks) != 1 || b.Hooks[0] != "H1" || b.Status != conformance.StepSuccess {
		t.Fatalf("b %+v", b)
	}
	// The static form the host-mode run frame carries.
	tr = runFlow(t, skeleton(t, &fakeSteps{}, nil), "f/w1/intercept", `{"x":7}`,
		StaticIntercepts{Results: map[string]jsonval.Value{"b": jsonval.FromProducer([]byte(`"static"`))}})
	if string(tr.Result.Producer()) != `{"from_b":"static"}` {
		t.Fatalf("static intercept: %s", tr.Result)
	}
}

// FX-12 and FX-36: a thrown error is the wrapper's object with step_id
// added, wrapped as {"error": ...}; the flow fails with it and runs nothing
// more. FX-14: a failing transform fails its step without running it, with
// no step_id. WR-20: an escaped error quotes the tail.
func TestSkeletonFailureValues(t *testing.T) {
	f := &fakeSteps{}
	s := skeleton(t, f, nil)
	tr := runFlow(t, s, "f/w1/throws", `{}`, nil)
	if want := storedOf(t, `{"error":{"message":"boom","name":"Error","stack":"Error: boom","step_id":"a"}}`); tr.Status != conformance.RunFailure || string(tr.Result.Producer()) != want {
		t.Fatalf("status %s, result %s; want %s", tr.Status, tr.Result, want)
	}
	if len(tr.Steps) != 1 {
		t.Fatalf("the flow went on after the failure: %+v", tr.Steps)
	}
	tr = runFlow(t, s, "f/w1/transform-error", `{}`, nil)
	want := storedOf(t, "{\"error\":{\"message\":\"Error during isolated evaluation of expression `flow_input.missing.deeper`:\\nQuickJS evaluation error: Error: cannot read property 'deeper' of undefined\\n    at <anonymous> (eval_script:1:1)\\n    at <eval> (eval_script:1:54)\\n\",\"name\":\"ExecutionErr\"}}")
	if tr.Status != conformance.RunFailure || string(tr.Result.Producer()) != want {
		t.Fatalf("transform error: %s\nwant %s", tr.Result, want)
	}
	if a := step(t, tr, "a"); string(a.ResolvedArgs.Producer()) != "{}" || !a.Ran {
		t.Fatalf("FX-14: the job launches with empty arguments: %+v", a)
	}
	for _, sc := range f.scripts() {
		if sc == "f/w1/must_not_run" {
			t.Fatal("must_not_run ran")
		}
	}
	if got := string(StoreOutcome(StepOutcome{Kind: OutcomeEscaped, Tail: "x\nlast\n", ExitCode: 1}, "a").Value.Producer()); got !=
		`{"error":{"message":"exit code for \"bun run\": 1, last log lines:\nx\nlast","name":"ExecutionErr","step_id":"a","exit_code":1}}` {
		t.Fatalf("WR-20: %s", got)
	}
}

// H4: canceling the run abandons the step in flight; the run ends canceled
// with the Canceled error, Windmill's defaults naming no reason and no
// canceler (FX-38), and settles once that invocation has.
func TestSkeletonCancel(t *testing.T) {
	settled := make(chan *Trace, 1)
	hooks := &settleHook{ch: settled}
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(200*time.Millisecond, func() { cancel(CancelCanceled) })
	s := skeleton(t, &fakeSteps{}, nil)
	tr, err := s.Run(ctx, SkeletonRun{Path: "f/w1/timeout", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != conformance.RunCanceled || tr.ResultOrigin != OriginEngine {
		t.Fatalf("status %s, origin %q", tr.Status, tr.ResultOrigin)
	}
	sameJSON(t, "the result", tr.Result, `{"error":{"message":"Job canceled: no reason given by unknown","name":"Canceled","reason":"no reason given","canceler":"unknown"}}`)
	if a := step(t, tr, "a"); !a.Abandoned || a.Status != conformance.StepCanceled {
		t.Fatalf("step %+v", a)
	}
	select {
	case <-settled:
	case <-time.After(2 * time.Second):
		t.Fatal("OnRunSettled was not called")
	}
}

type settleHook struct {
	NoHooks
	ch    chan *Trace
	ended atomic.Bool
}

func (h *settleHook) OnRunEnd(context.Context, *Trace) []Warning {
	h.ended.Store(true)
	return []Warning{{Code: "w", Message: "from OnRunEnd"}}
}

func (h *settleHook) OnRunSettled(t *Trace) {
	if !h.ended.Load() {
		t.Warnings = append(t.Warnings, Warning{Code: "order", Message: "OnRunSettled before OnRunEnd"})
	}
	h.ch <- t
}

// H9: OnRunEnd comes first, and OnRunSettled follows at once when nothing
// was abandoned, with OnRunEnd's warnings in both traces.
func TestSkeletonRunEndThenSettled(t *testing.T) {
	h := &settleHook{ch: make(chan *Trace, 1)}
	tr := runFlow(t, skeleton(t, &fakeSteps{}, nil), "f/w1/echo", `{"x":1}`, h)
	var settled *Trace
	select {
	case settled = <-h.ch:
	default:
		t.Fatal("OnRunSettled was not called by the end of the run")
	}
	if len(tr.Warnings) != 1 || len(settled.Warnings) != 1 || settled.Warnings[0].Code != "w" {
		t.Fatalf("warnings: returned %+v, settled %+v", tr.Warnings, settled.Warnings)
	}
}

type latchAt struct {
	NoHooks
	where string
}

func (h latchAt) OnModuleStart(context.Context, Run, Site) error {
	if h.where == "start" {
		return errors.New("host says stop")
	}
	return nil
}

func (h latchAt) Intercept(context.Context, Run, Site, jsonval.Value) (StepOutcome, bool, error) {
	if h.where == "intercept" {
		return StepOutcome{}, false, errors.New("host says stop")
	}
	return StepOutcome{}, false, nil
}

func (h latchAt) InvocationEnv(context.Context, Run, Site, map[string]string) error {
	if h.where == "env" {
		return errors.New("host says stop")
	}
	return nil
}

// H5: a hook that latches the run on the way to a module's launch stops
// it there: the step never runs, nothing more starts, and the run ends
// latched with the host's error.
func TestSkeletonLatch(t *testing.T) {
	for _, where := range []string{"start", "intercept", "env"} {
		f := &fakeSteps{}
		tr := runFlow(t, skeleton(t, f, nil), "f/w1/two", `{"ms":0}`, latchAt{where: where})
		if tr.Status != conformance.RunLatched || tr.Latched == nil || tr.Latched.Error() != "host says stop" || len(tr.Steps) != 1 {
			t.Fatalf("%s: status %s, latched %v, steps %d", where, tr.Status, tr.Latched, len(tr.Steps))
		}
		if got := f.scripts(); len(got) != 0 {
			t.Fatalf("%s: launched %v after the latch", where, got)
		}
		if a := step(t, tr, "a"); a.Ran || a.Status != conformance.StepNotRun {
			t.Fatalf("%s: step %+v", where, a)
		}
	}
}

// JE-15: a value JSON.stringify has no text for (a function, a symbol)
// becomes null; JE-10: a name the text never mentions is not bound, so a
// computed access to it reads undefined, which becomes null.
func TestSkeletonUnserialisableAndUnmentioned(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "flows", "f"), 0o755)
	os.WriteFile(filepath.Join(dir, "flows", "f", "x.flow.json"), []byte(`{"value":{"modules":[{"id":"a","value":{"type":"script","path":"f/w1/echo","input_transforms":{
		"fn":{"type":"javascript","expr":"() => 1"},
		"sym":{"type":"javascript","expr":"Symbol('s')"},
		"computed":{"type":"javascript","expr":"globalThis['fl' + 'ow_input']"}}}}]}}`), 0o644)
	s := skeleton(t, &fakeSteps{}, func(c *SkeletonConfig) { c.BundleDir = dir })
	tr := runFlow(t, s, "f/x", `{"k":1}`, nil)
	if tr.Status != conformance.RunSuccess {
		t.Fatalf("status %s, result %s", tr.Status, tr.Result)
	}
	sameJSON(t, "the arguments", step(t, tr, "a").ResolvedArgs, `{"fn":null,"sym":null,"computed":null}`)
}

// Everything outside the skeleton fails at load with
// ErrSkeletonUnsupported, never midway through a run (ADR 0001 §3's rule,
// applied to the skeleton's subset).
func TestSkeletonRefusesWhatItDoesNotRun(t *testing.T) {
	for name, value := range map[string]string{
		"skip_if":   `{"modules":[{"id":"a","skip_if":{"expr":"true"},"value":{"type":"script","path":"f/w1/echo"}}]}`,
		"retry":     `{"modules":[{"id":"a","retry":{"constant":{"attempts":1,"seconds":1}},"value":{"type":"script","path":"f/w1/echo"}}]}`,
		"loop":      `{"modules":[{"id":"a","value":{"type":"forloopflow","iterator":{"type":"static","value":[]},"modules":[]}}]}`,
		"python":    `{"modules":[{"id":"a","value":{"type":"rawscript","language":"python3","content":""}}]}`,
		"js-timout": `{"modules":[{"id":"a","timeout":{"type":"javascript","expr":"1"},"value":{"type":"script","path":"f/w1/echo"}}]}`,
		"failure":   `{"modules":[],"failure_module":{"id":"f","value":{"type":"script","path":"f/w1/echo"}}}`,
		"repeated":  `{"modules":[{"id":"a","value":{"type":"script","path":"f/w1/echo"}},{"id":"a","value":{"type":"script","path":"f/w1/echo"}}]}`,
	} {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "flows", "f"), 0o755)
		doc, _ := json.Marshal(map[string]json.RawMessage{"value": json.RawMessage(value)})
		os.WriteFile(filepath.Join(dir, "flows", "f", "x.flow.json"), doc, 0o644)
		_, err := NewSkeleton(SkeletonConfig{BundleDir: dir, JS: jsEng, Exec: &fakeSteps{}})
		if !errors.Is(err, ErrSkeletonUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// sameJSON fails unless v is the JSON value want, in any member order.
func sameJSON(t *testing.T, what string, v jsonval.Value, want string) {
	t.Helper()
	same, err := conformance.SameJSON(v.Producer(), []byte(want), false)
	if err != nil || !same {
		t.Fatalf("%s\n got %s\nwant %s (%v)", what, v, want, err)
	}
}

// boundaries records the boundary each invocation came with, by script,
// and succeeds with its arguments.
type boundaries struct {
	mu  sync.Mutex
	got map[string]Boundary
}

func (b *boundaries) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	b.mu.Lock()
	b.got[inv.Script] = inv.Boundary
	b.mu.Unlock()
	inv.Report.Settled(SettleCompleted)
	return StepOutcome{Kind: OutcomeOK, Body: inv.Args}, nil
}

// The skeleton invokes each step with its boundary (C3n): an inline step's
// by the compiler's rule, so a nativets step is native with or without the
// //native header (WR-40's note, SC1) and a bun or bunnative one follows
// its header; a path script's by its metadata in the bundle.
func TestSkeletonBoundary(t *testing.T) {
	dir, scripts := t.TempDir(), t.TempDir()
	raw := func(id, lang, content string) string {
		v, _ := json.Marshal(map[string]any{"id": id, "value": map[string]string{"type": "rawscript", "language": lang, "content": content}})
		return string(v)
	}
	path := func(id, p string) string {
		v, _ := json.Marshal(map[string]any{"id": id, "value": map[string]string{"type": "script", "path": p}})
		return string(v)
	}
	modules := []string{
		raw("bun", "bun", "export function main() {}"),
		raw("bunheader", "bun", "//native\nexport function main() {}"),
		raw("bunnative", "bunnative", "export function main() {}"),
		raw("nativets", "nativets", "export function main() {}"),
		raw("nativetsheader", "nativets", "//native\nexport function main() {}"),
		path("pathnative", "f/lib/native"),
		path("pathbun", "f/lib/plain"),
	}
	doc := `{"value":{"modules":[` + strings.Join(modules, ",") + `]}}`
	if err := os.MkdirAll(filepath.Join(dir, "flows", "f"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flows", "f", "x.flow.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	for p, meta := range map[string]string{"f/lib/native": `{"boundary":"native"}`, "f/lib/plain": `{"boundary":"bun"}`} {
		file := filepath.Join(scripts, "scripts", "bun", filepath.FromSlash(p)+".meta.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exec := &boundaries{got: map[string]Boundary{}}
	s := skeleton(t, exec, func(c *SkeletonConfig) { c.BundleDir, c.ScriptsDir = dir, scripts })
	if tr := runFlow(t, s, "f/x", "{}", nil); tr.Status != conformance.RunSuccess {
		t.Fatalf("status %s", tr.Status)
	}
	want := map[string]Boundary{
		"f/x/bun": BoundaryBun, "f/x/bunheader": BoundaryNative, "f/x/bunnative": BoundaryBun,
		"f/x/nativets": BoundaryNative, "f/x/nativetsheader": BoundaryNative,
		"f/lib/native": BoundaryNative, "f/lib/plain": BoundaryBun,
	}
	for script, b := range want {
		if exec.got[script] != b {
			t.Errorf("%s invoked with %q, want %q", script, exec.got[script], b)
		}
	}
}

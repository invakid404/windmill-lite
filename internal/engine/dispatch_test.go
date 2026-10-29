package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// waitResult polls a dispatched run until it has completed.
func waitResult(t *testing.T, d *Dispatcher, id string) platform.JobResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, ok, err := d.Result(context.Background(), id)
		if err != nil || !ok {
			t.Fatalf("result of %s: %v %v", id, ok, err)
		}
		if r.Completed {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s did not complete", id)
	return platform.JobResult{}
}

// The default dispatcher (ADR 0007, milestone PF3) runs a root of the
// program as a separate run: a script root as a script job, its own root,
// with its arguments stored and resolved as a step's (WA-4, WA-5, JE-41);
// a flow root as a flow run. Each completes with its success and stored
// result, a failure's value included (WA-15/a); a target that is not a
// root of the right kind, and a hash, are not in the bundle (ADR 0001
// §5a); arguments that are not an object are refused.
func TestDispatcherRunsRoots(t *testing.T) {
	s := &scripts{}
	services := testServices(t)
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "value", inputs("v", js("flow_input.n * 2")), ""))}, s, services)
	d := NewDispatcher(e, nil)
	defer d.Close(context.Background())
	caller := map[string]string{"WM_ROOT_JOB_ID": "owner"}
	dispatch := func(kind platform.DispatchKind, target, args string) (string, error) {
		return d.Dispatch(context.Background(), platform.Dispatch{Kind: kind, Target: target, Args: []byte(args), Caller: caller})
	}
	for _, c := range []struct {
		kind         platform.DispatchKind
		target, args string
		success      bool
		result       string
	}{
		{platform.DispatchScript, "f/t/value", `{"v":3}`, true, `3`},
		{platform.DispatchScript, "f/t/echo", `{"x":"$var:u/t/v","y":1.0}`, true, `{"x":"s3cr3t","y":1.0}`},
		{platform.DispatchScript, "f/t/echo", ``, true, `{}`},
		{platform.DispatchScript, "f/t/fail", `{}`, false, `{"error":{"name":"Error","message":"boom","step_id":""}}`},
		{platform.DispatchFlow, "f/t/main", `{"n":4}`, true, `8`},
	} {
		id, err := dispatch(c.kind, c.target, c.args)
		if err != nil {
			t.Fatalf("%s: %v", c.target, err)
		}
		r := waitResult(t, d, id)
		if !r.Started || r.Success != c.success || string(r.Result) != c.result {
			t.Errorf("%s %s: %+v %s", c.target, c.args, r, r.Result)
		}
	}
	var script *Invocation
	for i, inv := range s.invs {
		if inv.Script == "f/t/value" && inv.Env["WM_FLOW_STEP_ID"] == "" {
			script = &s.invs[i]
		}
	}
	if script == nil {
		t.Fatal("no script job")
	}
	env := script.Env
	if env["WM_JOB_ID"] != script.ID || env["WM_ROOT_JOB_ID"] != script.ID || env["WM_FLOW_JOB_ID"] != "" || env["WM_ROOT_FLOW_JOB_ID"] != "" ||
		env["WM_JOB_PATH"] != "f/t/value" || env["WM_STATE_PATH"] != "f/t/value/user" || script.Run != script.ID || script.Entry != "main" {
		t.Errorf("the script job's identity: %s %v", script.ID, env)
	}
	var nb *platform.NotInBundleError
	for _, c := range []struct {
		kind   platform.DispatchKind
		target string
	}{{platform.DispatchScript, "f/t/none"}, {platform.DispatchFlow, "f/t/value"}, {platform.DispatchScript, "f/t/main"}, {platform.DispatchHash, "f/t/value"}} {
		if _, err := dispatch(c.kind, c.target, `{}`); !errors.As(err, &nb) || err.Error() != "dispatch target not in bundle: "+c.target {
			t.Errorf("%s %s: %v", c.kind, c.target, err)
		}
	}
	var pe *platform.Error
	for _, args := range []string{`[]`, `"x"`, `{"a":"\u0000"}`} {
		if _, err := dispatch(platform.DispatchScript, "f/t/echo", args); !errors.As(err, &pe) || pe.HTTPStatus() != 400 {
			t.Errorf("arguments %s: %v", args, err)
		}
	}
	if _, ok, _ := d.Result(context.Background(), "00000000-0000-4000-8000-000000000000"); ok {
		t.Error("an id the dispatcher did not start")
	}
}

// gate holds every invocation until released, or until its context ends.
type gate struct {
	release chan struct{}
	started chan Invocation
}

func (g *gate) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	defer inv.Report.Settled(SettleCompleted)
	g.started <- inv
	select {
	case <-g.release:
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"done"`))}, nil
	case <-ctx.Done():
		return StepOutcome{}, context.Cause(ctx)
	}
}

// A dispatched run starts at once and is detached from the call that
// started it: it reports started, not completed, while it runs; Close
// cancels it, its job failing with the killed text of FX-38, and a
// dispatch after Close fails. A scheduled_in_secs delays the start.
func TestDispatcherDetachedAndClose(t *testing.T) {
	g := &gate{release: make(chan struct{}), started: make(chan Invocation, 4)}
	e := openWithPlatform(t, map[string]string{}, g, &platform.Services{})
	d := NewDispatcher(e, nil)
	ctx, cancel := context.WithCancel(context.Background())
	id, err := d.Dispatch(ctx, platform.Dispatch{Kind: platform.DispatchScript, Target: "f/t/echo", Args: []byte(`{}`)})
	cancel() // the call's context is not the run's
	if err != nil {
		t.Fatal(err)
	}
	<-g.started
	if r, ok, _ := d.Result(context.Background(), id); !ok || !r.Started || r.Completed {
		t.Errorf("running: %+v %v", r, ok)
	}
	late, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: platform.DispatchScript, Target: "f/t/echo", Args: []byte(`{}`), ScheduledInSecs: 3600})
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _ := d.Result(context.Background(), late); r.Started {
		t.Errorf("a scheduled run started at once: %+v", r)
	}
	d.Close(context.Background())
	r := waitResult(t, d, id)
	if r.Success || !strings.Contains(string(r.Result), "job process terminated due to cancelled by unknown") {
		t.Errorf("closed: %+v %s", r, r.Result)
	}
	if _, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: platform.DispatchScript, Target: "f/t/echo", Args: []byte(`{}`)}); err == nil {
		t.Error("a dispatch after Close")
	}
}

// A script job's own timeout fails it with the worker's text, without a
// step id (WR-37); f/t/zero's metadata asks for 0 s.
func TestRunScriptTimeout(t *testing.T) {
	g := &gate{release: make(chan struct{}), started: make(chan Invocation, 1)}
	e := openWithPlatform(t, map[string]string{}, g, &platform.Services{})
	r, err := e.RunScript(context.Background(), ScriptRequest{Path: "f/t/zero", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"error":{"message":"execution error:\njob process terminated due to timeout after exceeding job-specific duration limit","name":"ExecutionErr"}}`
	if !r.Failed || string(r.Result.Producer()) != want {
		t.Errorf("%+v %s", r, r.Result.Producer())
	}
	if _, err := e.RunScript(context.Background(), ScriptRequest{Path: "f/t/none", Args: jsonval.FromProducer([]byte(`{}`))}); err == nil {
		t.Error("a script that is not a root")
	}
}

// stateExec answers f/t/obj by writing flow user state at the step's root,
// as the facade's route does for a step's setFlowUserState, and every
// other script as scripts does.
type stateExec struct {
	scripts
	s *platform.Services
}

func (x *stateExec) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	if inv.Script == "f/t/obj" {
		defer inv.Report.Settled(SettleCompleted)
		root := inv.Env["WM_ROOT_JOB_ID"]
		if j, live, ok := x.s.JobRegistry().Job(root); !ok || !live || j.Kind != platform.JobFlow {
			return StepOutcome{}, errors.New("the step's root is not a live flow job")
		}
		if err := x.s.SetFlowUserState(ctx, root, "k", []byte(`{"n":5}`)); err != nil {
			return StepOutcome{}, err
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	}
	return x.scripts.Execute(ctx, inv)
}

// A run is a live flow job of the job registry while it runs, its own
// root (D3), so its steps' writes reach its flow user state, which the
// flow_user_state() helper reads (JE-20, WA-14): the value a step stored,
// and null for a missing key. Once the run has ended, the registry has
// forgotten it and its map.
func TestEngineFlowUserState(t *testing.T) {
	services := &platform.Services{}
	x := &stateExec{s: services}
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(
		mod("a", "obj", "{}", ""),
		mod("b", "echo", inputs("k", js(`flow_user_state("k")`), "none", js(`flow_user_state("none")`)), ""),
	)}, x, services)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"k":{"n":5},"none":null}`)
	if _, ok := services.RootJob(tr.RunID); ok {
		t.Error("the run is known after it ended")
	}
	if v, _ := services.FlowUserState(context.Background(), tr.RunID, "k"); string(v) != "null" {
		t.Errorf("the run's map after it ended: %s", v)
	}
	// Without a registry's run, a helper reads null, as the API does for a
	// job with no flow status.
	if v, err := (helpers{s: services, root: "elsewhere"}).FlowUserState(context.Background(), "k"); err != nil || string(v.Producer()) != "null" {
		t.Errorf("another root: %s %v", v.Producer(), err)
	}
}

// Runs at once each have their own map, and the dispatcher's records are
// kept while the dispatching step's root lives and swept after.
func TestDispatcherRecordsFollowTheirRoot(t *testing.T) {
	services := &platform.Services{}
	e := openWithPlatform(t, map[string]string{}, &scripts{}, services)
	d := NewDispatcher(e, nil)
	defer d.Close(context.Background())
	end := services.JobRegistry().Begin(platform.Job{ID: "owner", Kind: platform.JobFlow, Root: "owner"})
	id, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: platform.DispatchScript, Target: "f/t/echo", Args: []byte(`{}`), Caller: map[string]string{"WM_ROOT_JOB_ID": "owner"}})
	if err != nil {
		t.Fatal(err)
	}
	waitResult(t, d, id)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: platform.DispatchScript, Target: "f/t/echo", Args: []byte(`{}`), Caller: map[string]string{"WM_ROOT_JOB_ID": "owner"}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, ok, _ := d.Result(context.Background(), id); !ok {
		t.Error("a record swept while its root lives")
	}
	end()
	if _, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: platform.DispatchScript, Target: "f/t/echo", Args: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Result(context.Background(), id); ok {
		t.Error("a record kept after its root ended")
	}
}

// The engine refuses at open a script root it cannot run (ADR 0001 §3's
// rule), naming the milestone: a script with its own cache_ttl (N4).
func TestEngineRefusesUnsupportedRootScript(t *testing.T) {
	jsOnce.Do(func() { jsEng, jsErr = newTestJS() })
	if jsErr != nil {
		t.Fatal(jsErr)
	}
	fsys := fstest.MapFS{
		"wmill.yaml":             {Data: []byte("defaultTs: bun\n")},
		"f/t/cached.ts":          {Data: []byte("export async function main() { return 1 }\n")},
		"f/t/cached.script.yaml": {Data: []byte("summary: ''\ndescription: ''\nkind: script\ncache_ttl: 60\nschema:\n  type: object\n  properties: {}\n  required: []\n")},
		"f/t/value.ts":           {Data: []byte("export async function main() { return 1 }\n")},
	}
	folder, err := syncfolder.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	prog, err := ir.Compile(folder, nil, ir.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Program: prog, JS: jsEng}); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "script f/t/cached: a script's own cache_ttl (N4)") {
		t.Errorf("open: %v", err)
	}
}

// A script dispatched tracked in its parent (runScriptByPath's parent_job
// and root_job, WA-15) gets the job identity the oracle recorded (fixture
// client-api/dispatch, run identity): its parent is the dispatching job,
// its innermost scope and effective root the query's root_job, its flow
// path the dispatching job's runnable path, from the job registry, and so
// its state path <that path>/nostep_user; it has no step id.
func TestDispatchedScriptIdentity(t *testing.T) {
	s := &scripts{}
	services := &platform.Services{}
	e := openWithPlatform(t, map[string]string{}, s, services)
	d := NewDispatcher(e, nil)
	defer d.Close(context.Background())
	const run, step = "00000000-0000-4000-8000-00000000000f", "00000000-0000-4000-8000-0000000000a5"
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run, Path: "f/x/flow"})
	defer endRun()
	endStep := services.JobRegistry().Begin(platform.Job{ID: step, Kind: platform.JobScript, Root: run, Path: "f/x/flow/a", Env: map[string]string{"WM_FLOW_JOB_ID": run}})
	defer endStep()
	for _, c := range []struct {
		parent, root                                string
		flowJob, rootFlow, rootJob, flowPath, state string
	}{
		{step, run, step, run, run, "f/x/flow/a", "f/x/flow/a/nostep_user"},
		{step, "", step, "", step, "f/x/flow/a", "f/x/flow/a/nostep_user"},
		{"", "", "", "", "", "", "f/t/echo/user"},
	} {
		id, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: platform.DispatchScript, Target: "f/t/echo", Args: []byte(`{}`), ParentJob: c.parent, RootJob: c.root})
		if err != nil {
			t.Fatal(err)
		}
		waitResult(t, d, id)
		var env map[string]string
		s.mu.Lock()
		for _, inv := range s.invs {
			if inv.ID == id {
				env = inv.Env
			}
		}
		s.mu.Unlock()
		rootJob := cmpOr(c.rootJob, id)
		if env["WM_FLOW_JOB_ID"] != c.flowJob || env["WM_ROOT_FLOW_JOB_ID"] != c.rootFlow || env["WM_ROOT_JOB_ID"] != rootJob ||
			env["WM_FLOW_PATH"] != c.flowPath || env["WM_STATE_PATH"] != c.state || env["WM_FLOW_STEP_ID"] != "" || env["WM_JOB_PATH"] != "f/t/echo" {
			t.Errorf("parent %q root %q: %v", c.parent, c.root, env)
		}
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

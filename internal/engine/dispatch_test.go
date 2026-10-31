package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
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

// FX-48/d, FX-51/d, FX-51/e: a run request whose arguments jsonb refuses
// is refused with no job, as Windmill's run API answers it, with HTTP 400
// and a plain-text body: the database error, SqlErr, for a NUL in a value
// or a key, a number beyond numeric's range and an unpaired surrogate in a
// value; and, for an unpaired surrogate in a top-level key, which the API
// decodes first, Bad request: invalid json with the JSON library's message
// and position, which internal/platform's reader of that library's rules
// gives too.
func TestDispatchRefusesUnstorableArguments(t *testing.T) {
	s := &scripts{}
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", inputs("s", js("flow_input.s")), ""))}, s, testServices(t))
	d := NewDispatcher(e, nil)
	defer d.Close(context.Background())
	for _, c := range []struct {
		args, body string
		key        bool
	}{
		{`{"s": "x\u0000y"}`, "SqlErr: error returned from database: unsupported Unicode escape sequence", false},
		{`{"k\u0000": 1}`, "SqlErr: error returned from database: unsupported Unicode escape sequence", false},
		{`{"x": 1e131072}`, "SqlErr: error returned from database: value overflows numeric format", false},
		{`{"k": "\ud83d"}`, "SqlErr: error returned from database: invalid input syntax for type json", false},
		{`{"k\ud83d": 1}`, "Bad request: invalid json: unexpected end of hex escape at line 1 column 10", true},
		{`{"k\udc00": 1}`, "Bad request: invalid json: lone leading surrogate in hex escape at line 1 column 9", true},
		{`{"k\ud83d\n": 1}`, "Bad request: invalid json: unexpected end of hex escape at line 1 column 11", true},
		{"{\"a\": \"\\u0000\",\n \"k\\ud83d\\u0041\": 1}", "Bad request: invalid json: lone leading surrogate in hex escape at line 2 column 15", true},
		{`{"ok\ud83d\ude00": "\ud83d"}`, "SqlErr: error returned from database: invalid input syntax for type json", false},
	} {
		for _, kind := range []platform.DispatchKind{platform.DispatchFlow, platform.DispatchScript} {
			target := map[platform.DispatchKind]string{platform.DispatchFlow: "f/t/main", platform.DispatchScript: "f/t/echo"}[kind]
			id, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: kind, Target: target, Args: []byte(c.args)})
			var pe *platform.Error
			if id != "" || !errors.As(err, &pe) || pe.HTTPStatus() != 400 || pe.Body() != c.body {
				t.Errorf("%s %s: id %q, error %v", kind, c.args, id, err)
			}
		}
		if c.key {
			if _, err := platform.Reprint([]byte(c.args)); err == nil || "Bad request: invalid json: "+err.Error() != c.body {
				t.Errorf("%s: the library's reader says %v", c.args, err)
			}
		}
	}
	if len(s.invs) != 0 {
		t.Fatalf("a refused run ran %v", s.calls())
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
// and null for a missing key. Once the run has ended, it is no longer
// live, its map is forgotten, and only its root is still known (WA-19).
func TestEngineFlowUserState(t *testing.T) {
	services := &platform.Services{}
	x := &stateExec{s: services}
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(
		mod("a", "obj", "{}", ""),
		mod("b", "echo", inputs("k", js(`flow_user_state("k")`), "none", js(`flow_user_state("none")`)), ""),
	)}, x, services)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"k":{"n":5},"none":null}`)
	if _, live, _ := services.JobRegistry().Job(tr.RunID); live {
		t.Error("the run is live after it ended")
	}
	if root, ok := services.RootJob(tr.RunID); !ok || root != tr.RunID {
		t.Errorf("the ended run's root %q, known %v", root, ok)
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

// FX-40: a script root with its own cache_ttl runs as a script job whose
// result the result cache keeps: a second run with the same arguments
// gets the first one's result without running, and other arguments miss.
func TestRunScriptCache(t *testing.T) {
	x := &idSteps{}
	e := openWithPlatform(t, map[string]string{}, x, &platform.Services{})
	run := func(args string) ScriptResult {
		t.Helper()
		r, err := e.RunScript(context.Background(), ScriptRequest{Path: "f/t/cached", Args: jsonval.FromProducer([]byte(args))})
		if err != nil || r.Failed {
			t.Fatalf("%+v %v", r, err)
		}
		return r
	}
	first := run(`{"v":1}`)
	if again := run(`{"v":1}`); string(again.Result.Producer()) != string(first.Result.Producer()) || again.ID == first.ID {
		t.Fatalf("a second run %+v, the first %+v", again, first)
	}
	if other := run(`{"v":2}`); string(other.Result.Producer()) == string(first.Result.Producer()) {
		t.Fatal("other arguments hit the first run's entry")
	}
	if n := len(x.calls()); n != 2 {
		t.Fatalf("%d invocations, want 2", n)
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

// A flow dispatched tracked in its parent (runFlow's parent_job and
// root_job) runs its steps with the identity the oracle recorded (fixture
// client-api/dispatch, run identity): the dispatched flow is their parent,
// and the query's root_job their innermost scope and effective root, so
// they share the dispatching run's flow user state; untracked
// (runFlowAsync), the flow is its own root and scope. The run is a flow
// job of its root's tree while it runs.
func TestDispatchedFlowIdentity(t *testing.T) {
	services := &platform.Services{}
	var seen sync.Map
	x := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
		defer inv.Report.Settled(SettleCompleted)
		j, live, ok := services.JobRegistry().Job(inv.Run)
		seen.Store(inv.Run, [2]any{inv.Env, ok && live && j.Kind == platform.JobFlow && j.Root == inv.Env["WM_ROOT_JOB_ID"]})
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", "{}", ""))}, x, services)
	d := NewDispatcher(e, nil)
	defer d.Close(context.Background())
	const run, step = "00000000-0000-4000-8000-00000000000f", "00000000-0000-4000-8000-0000000000a5"
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run, Path: "f/x/flow"})
	defer endRun()
	for _, c := range []struct {
		parent, root      string
		rootJob, rootFlow string // "" for the dispatched flow itself
	}{
		{step, run, run, run},
		{"", "", "", ""},
	} {
		id, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: platform.DispatchFlow, Target: "f/t/main", Args: []byte(`{}`), ParentJob: c.parent, RootJob: c.root})
		if err != nil {
			t.Fatal(err)
		}
		if r := waitResult(t, d, id); !r.Success {
			t.Fatalf("%+v %s", r, r.Result)
		}
		v, ok := seen.Load(id)
		if !ok {
			t.Fatalf("no step of %s ran", id)
		}
		env, registered := v.([2]any)[0].(map[string]string), v.([2]any)[1].(bool)
		if env["WM_FLOW_JOB_ID"] != id || env["WM_ROOT_JOB_ID"] != cmpOr(c.rootJob, id) || env["WM_ROOT_FLOW_JOB_ID"] != cmpOr(c.rootFlow, id) ||
			env["WM_FLOW_PATH"] != "f/t/main" || env["WM_STATE_PATH"] != "f/t/main/a_user" || !registered {
			t.Errorf("parent %q root %q: registered %v, %v", c.parent, c.root, registered, env)
		}
	}
}

// execFunc is a StepExecutor written as a function.
type execFunc func(ctx context.Context, inv Invocation) (StepOutcome, error)

func (f execFunc) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	return f(ctx, inv)
}

// flow_user_state() in a flow dispatched tracked in its parent reads the
// root its jobs share, as the stock helpers in its steps do; untracked, the
// flow reads its own.
func TestDispatchedFlowUserState(t *testing.T) {
	services := &platform.Services{}
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", inputs("k", js(`flow_user_state("k")`)), ""))}, &scripts{}, services)
	d := NewDispatcher(e, nil)
	defer d.Close(context.Background())
	const run = "00000000-0000-4000-8000-00000000000f"
	endRun := services.JobRegistry().Begin(platform.Job{ID: run, Kind: platform.JobFlow, Root: run})
	defer endRun()
	if err := services.SetFlowUserState(context.Background(), run, "k", []byte("7")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ root, want string }{{run, `{"k":7}`}, {"", `{"k":null}`}} {
		id, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: platform.DispatchFlow, Target: "f/t/main", Args: []byte(`{}`), RootJob: c.root})
		if err != nil {
			t.Fatal(err)
		}
		if r := waitResult(t, d, id); !r.Success || string(r.Result) != c.want {
			t.Errorf("root %q: %+v %s", c.root, r, r.Result)
		}
	}
}

// pf3 review round 1, P2-3: a dispatch's links are taken when it is
// accepted, so a run that starts after the dispatching job and its run
// have ended, after a delay or at once, still gets the parent's path. A
// script's flow path and state path are the parent's; a flow's own job
// carries them too, which its flow_env's $WM_FLOW_PATH reads (JE-38).
func TestDispatchedLinksOutliveTheParent(t *testing.T) {
	s := &scripts{}
	services := testServices(t)
	e := openWithPlatform(t, map[string]string{
		"f/t/main": `{"modules":[` + mod("a", "echo", inputs("p", js(`flow_env.p[0]`)), "") + `],"flow_env":{"p":["$WM_FLOW_PATH","$var:u/t/v"]}}`,
	}, s, services)
	d := NewDispatcher(e, nil)
	defer d.Close(context.Background())
	endRun := services.JobRegistry().Begin(platform.Job{ID: "run", Kind: platform.JobFlow, Root: "run", Path: "f/x/flow"})
	endParent := services.JobRegistry().Begin(platform.Job{ID: "parent", Kind: platform.JobScript, Root: "run", Path: "f/x/flow/a"})
	dispatch := func(kind platform.DispatchKind, target string, delay int64) string {
		id, err := d.Dispatch(context.Background(), platform.Dispatch{Kind: kind, Target: target, Args: []byte(`{}`), ParentJob: "parent", RootJob: "run", ScheduledInSecs: delay})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	late, lateFlow := dispatch(platform.DispatchScript, "f/t/echo", 1), dispatch(platform.DispatchFlow, "f/t/main", 1)
	soon := dispatch(platform.DispatchScript, "f/t/echo", 0)
	endParent()
	endRun()
	if _, live, _ := services.JobRegistry().Job("parent"); live {
		t.Fatal("the parent is still live after its run ended")
	}
	for _, id := range []string{late, soon} {
		waitResult(t, d, id)
		var env map[string]string
		s.mu.Lock()
		for _, inv := range s.invs {
			if inv.ID == id {
				env = inv.Env
			}
		}
		s.mu.Unlock()
		if env["WM_FLOW_PATH"] != "f/x/flow/a" || env["WM_STATE_PATH"] != "f/x/flow/a/nostep_user" || env["WM_FLOW_JOB_ID"] != "parent" || env["WM_ROOT_JOB_ID"] != "run" {
			t.Errorf("%s: the parent's links lost: %v", id, env)
		}
	}
	if r := waitResult(t, d, lateFlow); !r.Success || string(r.Result) != `{"p":"f/x/flow/a"}` {
		t.Errorf("the flow's own job: %+v %s", r, r.Result)
	}
}

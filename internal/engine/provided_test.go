package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// compileContract compiles the flows with contract (YAML or JSON) as the
// host contract.
func compileContract(t *testing.T, flows map[string]string, contract string) *ir.Program {
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
	c, err := ir.ParseContract([]byte(contract))
	if err != nil {
		t.Fatal(err)
	}
	prog, err := ir.Compile(folder, c, ir.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return prog
}

// The run-time half of ADR 0002's provided-site policy case, and ADR 0008's
// mandatory sites: a flow step whose target suspends (FX-39), which the
// compiler refuses unless the contract provides the site, runs with the
// provider's value in place of its subflow. The provider gets the site's
// evaluated arguments; the site keeps its envelope, so its stop_after_if,
// retry and the next step's results route apply to the provided value as
// to a real module's; a static site needs no provider; a provider that
// declines latches the run with ErrProviderDeclined, and one with no
// Provide declines.
func TestProvidedSites(t *testing.T) {
	wait := func(env string) string {
		return `{"id":"wait","value":{"type":"flow","path":"f/t/permit","input_transforms":{"key":` + js("flow_input.key") + `}},` +
			`"stop_after_if":{"expr":"result.granted === false","skip_if_stopped":false}` + env + `}`
	}
	use := mod("b", "value", inputs("v", js("results.wait.ticket")), "")
	flows := map[string]string{
		"f/t/main":    flowOf(wait(""), use),
		"f/t/retried": flowOf(wait(`,"retry":{"constant":{"attempts":1,"seconds":0}}`), use),
		"f/t/fixed":   flowOf(wait(""), use),
		// The permit flow suspends: it runs only behind a provided site.
		"f/t/permit": flowOf(`{"id":"s","value":{"type":"identity"},"suspend":{"required_events":1,"timeout":60}}`),
	}
	contract := `{"roots":[{"flow":"f/t/main"},{"flow":"f/t/retried"},{"flow":"f/t/fixed"}],"provided":[` +
		`{"selector":{"flow":"f/t/main","module":"wait"},"provider":"permit","mandatory":true},` +
		`{"selector":{"flow":"f/t/retried","module":"wait"},"provider":"flaky","mandatory":true},` +
		`{"selector":{"flow":"f/t/fixed","module":"wait"},"static":{"granted":true,"ticket":"static"},"mandatory":true}]}`
	prog := compileContract(t, flows, contract)
	if _, ok := prog.Flows["f/t/permit"]; ok {
		t.Fatal("the provided site's target was compiled")
	}
	var mu sync.Mutex
	var calls []string
	flaky := 0
	provide := func(_ context.Context, _ Run, s Site, name string, args jsonval.Value) (StepOutcome, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name+" "+s.Flow+" "+s.Module+" "+string(args.Producer()))
		var a struct{ Key string }
		_ = json.Unmarshal(args.Producer(), &a)
		switch {
		case name == "flaky":
			if flaky++; flaky == 1 {
				return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"name":"Busy","message":"try again"}`))}, true, nil
			}
		case a.Key == "decline":
			return StepOutcome{}, false, nil
		case a.Key == "boom":
			return StepOutcome{}, false, errors.New("host failure")
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`{"granted":` + map[bool]string{true: "false", false: "true"}[a.Key == "no"] + `,"ticket":"t-` + a.Key + `"}`))}, true, nil
	}
	s := &scripts{}
	e, err := New(Config{Program: prog, Exec: s, JS: jsEng, Provide: provide})
	if err != nil {
		t.Fatal(err)
	}
	run := func(flow, args string) *Trace {
		t.Helper()
		tr, err := e.Run(context.Background(), RunRequest{Path: flow, Args: jsonval.FromProducer([]byte(args)), Seed: 1})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}

	tr := run("f/t/main", `{"key":"k1"}`)
	wantStatus(t, tr, conformance.RunSuccess, `"t-k1"`)
	if w := step(t, tr, "wait"); !w.Ran || w.Job != "" || len(w.Hooks) != 1 || w.Hooks[0] != "provided" || string(w.RecordedArgs.Producer()) != `{"key":"k1"}` {
		t.Fatalf("the provided site's record: %+v", w)
	}
	if len(calls) != 1 || calls[0] != `permit f/t/main wait {"key":"k1"}` {
		t.Fatalf("provider calls %q", calls)
	}

	// The site's own stop_after_if applies to the provided value.
	tr = run("f/t/main", `{"key":"no"}`)
	wantStatus(t, tr, conformance.RunSuccess, `{"granted":false,"ticket":"t-no"}`)
	if _, ran := tr.Step(conformance.SitePath{}.Child(conformance.Module("b"))); ran {
		t.Fatal("the step after a stop ran")
	}

	// The site's retry applies around its provider.
	tr = run("f/t/retried", `{"key":"r"}`)
	wantStatus(t, tr, conformance.RunSuccess, `"t-r"`)
	if flaky != 2 {
		t.Fatalf("the flaky provider was called %d times", flaky)
	}

	// A static site needs no provider.
	tr = run("f/t/fixed", `{"key":"x"}`)
	wantStatus(t, tr, conformance.RunSuccess, `"static"`)

	// A declining provider fails the run closed; a provider's error
	// latches it too.
	for key, want := range map[string]error{"decline": ErrProviderDeclined} {
		tr = run("f/t/main", `{"key":"`+key+`"}`)
		if tr.Status != conformance.RunLatched || !errors.Is(tr.Latched, want) || !strings.Contains(tr.Latched.Error(), `"permit"`) {
			t.Fatalf("%s: status %s, latched %v", key, tr.Status, tr.Latched)
		}
	}
	tr = run("f/t/main", `{"key":"boom"}`)
	if tr.Status != conformance.RunLatched || tr.Latched == nil || tr.Latched.Error() != "host failure" {
		t.Fatalf("a provider's error: status %s, latched %v", tr.Status, tr.Latched)
	}

	// Without Provide, a named site declines.
	e2, err := New(Config{Program: prog, Exec: s, JS: jsEng})
	if err != nil {
		t.Fatal(err)
	}
	tr2, err := e2.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{"key":"k"}`))})
	if err != nil {
		t.Fatal(err)
	}
	if tr2.Status != conformance.RunLatched || !errors.Is(tr2.Latched, ErrProviderDeclined) {
		t.Fatalf("no Provide: status %s, latched %v", tr2.Status, tr2.Latched)
	}
}

// Child flows are flow jobs of the run's tree while they run (ADR 0007,
// D3): a loop iteration's, a branch's and a subflow's, with their runnable
// paths, known before any step of theirs registers. A dispatch whose
// parent_job names one (WA-15) runs with its path as WM_FLOW_PATH.
func TestChildFlowsAreJobs(t *testing.T) {
	services := &platform.Services{}
	var mu sync.Mutex
	seen := map[string]platform.Job{}
	envs := map[string]map[string]string{} // by run
	var d *Dispatcher
	var dispatched string
	x := execFunc(func(ctx context.Context, inv Invocation) (StepOutcome, error) {
		defer inv.Report.Settled(SettleCompleted)
		flow := inv.Env["WM_FLOW_JOB_ID"]
		j, live, ok := services.JobRegistry().Job(flow)
		mu.Lock()
		if ok && live {
			seen[inv.Site.String()] = j
		}
		envs[inv.Run] = inv.Env
		mu.Unlock()
		if inv.Site.String() == "m:loop/i:0/m:a" {
			id, err := d.Dispatch(ctx, platform.Dispatch{Kind: platform.DispatchScript, Target: "f/t/echo", Args: []byte(`{}`), ParentJob: flow})
			if err != nil {
				return StepOutcome{}, err
			}
			mu.Lock()
			dispatched = id
			mu.Unlock()
		}
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`null`))}, nil
	})
	body := `[` + mod("a", "echo", "{}", "") + `,` + mod("a2", "echo", "{}", "") + `]`
	e := openWithPlatform(t, map[string]string{
		"f/t/main": flowOf(
			`{"id":"loop","value":{"type":"forloopflow","iterator":`+js("[1]")+`,"skip_failures":false,"parallel":true,"modules":`+body+`}}`,
			`{"id":"br","value":{"type":"branchone","branches":[{"expr":"true","modules":[`+mod("c", "echo", "{}", "")+`]}],"default":[]}}`,
			`{"id":"sub","value":{"type":"flow","path":"f/t/sub","input_transforms":{}}}`),
		"f/t/sub": flowOf(mod("d", "echo", "{}", "")),
	}, x, services)
	d = NewDispatcher(e, nil)
	defer d.Close(context.Background())
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, tr, conformance.RunSuccess, `null`)
	mu.Lock()
	defer mu.Unlock()
	for site, path := range map[string]string{"m:loop/i:0/m:a": "f/t/main/forloop-0", "m:br/b:0/m:c": "f/t/main/branchone-1", "m:sub/m:d": "f/t/sub"} {
		j, ok := seen[site]
		if !ok || j.Kind != platform.JobFlow || j.Path != path || j.Root != tr.RunID {
			t.Errorf("%s: the flow job %+v (known %v), want path %s in the run's tree", site, j, ok, path)
		}
	}
	if dispatched == "" {
		t.Fatal("nothing was dispatched")
	}
	mu.Unlock()
	waitResult(t, d, dispatched)
	mu.Lock()
	env := envs[dispatched]
	if env["WM_FLOW_PATH"] != "f/t/main/forloop-0" || env["WM_FLOW_JOB_ID"] == "" || env["WM_STATE_PATH"] != "f/t/main/forloop-0/nostep_user" {
		t.Errorf("the dispatched job's identity: %v", env)
	}
}

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// openWithPlatform is openEngine with the platform services runs read.
func openWithPlatform(t *testing.T, flows map[string]string, exec StepExecutor, p *platform.Services) *Engine {
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
	e, err := New(Config{Program: prog, Bundle: "sha256:test", Exec: exec, JS: jsEng, Platform: p})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func testServices(t *testing.T) *platform.Services {
	t.Helper()
	s := platform.NewStore()
	s.SetVariable("u/t/v", platform.Variable{Value: "s3cr3t"})
	s.SetVariable("u/t/secret", platform.Variable{Value: "hidden", IsSecret: true})
	for path, v := range map[string]string{
		"u/t/r":       `{"pw":"$var:u/t/v","n":1}`,
		"u/t/context": `{"root":"$WM_ROOT_JOB_ID","job":"$WM_JOB_ID"}`,
	} {
		if err := s.SetResource(path, platform.Resource{Type: "object", Value: json.RawMessage(v)}); err != nil {
			t.Fatal(err)
		}
	}
	return &platform.Services{Resources: s, Variables: s}
}

// JE-41/a–c, JE-V3/a, JE-42/d, WA-V3/a: a leaf's arguments are resolved
// when its job is picked up, whatever produced them, and only there: the
// trace records them as stored (O2) and as resolved (O3), the script gets
// the resolved ones, expressions see the markers, and $WM_* strings are
// the process's in an argument and the job's stored fields in a resource.
func TestEngineResolvesArguments(t *testing.T) {
	s := &scripts{}
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(
		mod("b", "echo", inputs(
			"static", static(`"$var:u/t/v"`),
			"expr", js(`"$var:" + "u/t/v"`),
			"len", js(`flow_input.v.length`),
			"input", js(`flow_input.v`),
			"ctx", static(`"$res:u/t/context"`),
			"names", static(`["$WM_ROOT_JOB_ID", "$WM_JOB_ID", "$var:u/t/secret"]`),
			"plain", static(`["$WM_JOB_ID"]`),
		), ""),
	)}, s, testServices(t))
	tr := runIt(t, e, `{"v":"$var:u/t/v"}`, nil)
	if tr.Status != conformance.RunSuccess || len(s.invs) != 1 {
		t.Fatalf("%s %s %d", tr.Status, tr.Result, len(s.invs))
	}
	inv := s.invs[0]
	job, root := inv.Env["WM_JOB_ID"], inv.Env["WM_ROOT_JOB_ID"]
	if root != tr.RunID || job == "" {
		t.Fatalf("env %v", inv.Env)
	}
	rec := tr.Steps[0]
	sameJSON(t, "recorded (O2)", rec.RecordedArgs, `{"static":"$var:u/t/v","expr":"$var:u/t/v","len":10,"input":"$var:u/t/v","ctx":"$res:u/t/context","names":["$WM_ROOT_JOB_ID","$WM_JOB_ID","$var:u/t/secret"],"plain":["$WM_JOB_ID"]}`)
	want := `{"static":"s3cr3t","expr":"s3cr3t","len":10,"input":"s3cr3t","ctx":{"root":"","job":"` + job + `"},"names":["` + root + `","` + job + `","hidden"],"plain":["$WM_JOB_ID"]}`
	sameJSON(t, "resolved (O3)", rec.ResolvedArgs, want)
	sameJSON(t, "the script's arguments", inv.Args, want)
	sameJSON(t, "the step's result", rec.Result, want)
}

// JE-42/e, WR-5/e: a failed resolution fails the job before its script
// runs, with the worker's execution error; the flow handles it as any
// failure, and the run settles.
func TestEngineResolutionFailure(t *testing.T) {
	s := &scripts{}
	settled := make(chan struct{})
	e := openWithPlatform(t, map[string]string{"f/t/main": flowOf(
		mod("b", "echo", inputs("x", static(`"$var:u/t/missing"`)), ""),
		mod("c", "echo", "", ""),
	)}, s, testServices(t))
	tr := runIt(t, e, `{}`, settledHook{settled: settled})
	<-settled
	want := `{"error":{"message":"execution error:\nNot found: Variable u/t/missing not found for ` + "`x`" + `: Not found: Variable u/t/missing not found","name":"ExecutionErr","step_id":"b"}}`
	wantStatus(t, tr, conformance.RunFailure, want)
	if len(s.invs) != 0 {
		t.Fatalf("a script ran: %v", s.calls())
	}
	rec := tr.Steps[0]
	if rec.Status != conformance.StepFailure || !rec.Ran || rec.ResultOrigin != OriginEngine {
		t.Fatalf("%+v", rec)
	}
	sameJSON(t, "recorded", rec.RecordedArgs, `{"x":"$var:u/t/missing"}`)
}

type settledHook struct {
	NoHooks
	settled chan struct{}
}

func (h settledHook) OnRunSettled(*Trace) { close(h.settled) }

// JE-38/a and /b: flow_env's values are resolved for every transition, and
// expressions see them resolved; a failed resolution leaves the map as
// written, with a warning.
func TestEngineFlowEnv(t *testing.T) {
	s := &scripts{}
	flow := func(env, b string) string {
		return `{"modules":[` + mod("a", "value", inputs("v", js(`flow_env.K`)), "") + `,` +
			mod("b", "value", inputs("v", js(b)), `"stop_after_if":{"expr":"flow_env.S === 's3cr3t'","skip_if_stopped":false}`) + `],"flow_env":` + env + `}`
	}
	// The unresolved value never reaches a leaf's arguments in f/t/bad,
	// where it would be resolved, and fail.
	e := openWithPlatform(t, map[string]string{
		"f/t/main": flow(`{"K":"v","S":"$var:u/t/v"}`, `flow_env.S`),
		"f/t/bad":  flow(`{"K":"v","S":"$var:u/t/missing"}`, `flow_env.S.slice(1)`),
	}, s, testServices(t))
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `"s3cr3t"`)
	sameJSON(t, "a", tr.Steps[0].RecordedArgs, `{"v":"v"}`)
	sameJSON(t, "b", tr.Steps[1].RecordedArgs, `{"v":"s3cr3t"}`)
	if len(tr.Warnings) != 0 {
		t.Fatalf("warnings %v", tr.Warnings)
	}

	bad, err := e.Run(context.Background(), RunRequest{Path: "f/t/bad", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, bad, conformance.RunSuccess, `"var:u/t/missing"`)
	n := 0
	for _, w := range bad.Warnings {
		if w.Code == "flow_env_unresolved" && strings.Contains(w.Message, "Variable u/t/missing not found for `S`") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want a warning per transition, got %v", bad.Warnings)
	}
}

// JE-20/a and /b: variable() and resource() read the platform; a failure
// throws the API's error text, which fails the transform.
func TestEngineHelpers(t *testing.T) {
	s := &scripts{}
	e := openWithPlatform(t, map[string]string{
		"f/t/main":    flowOf(mod("b", "echo", inputs("v", js(`variable("u/t/v")`), "r", js(`resource("u/t/r")`), "s", js(`variable("u/t/secret")`)), "")),
		"f/t/missing": flowOf(mod("b", "echo", inputs("v", js(`variable("u/t/missing")`)), "")),
	}, s, testServices(t))
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"v":"s3cr3t","r":{"pw":"s3cr3t","n":1},"s":"hidden"}`)

	bad, err := e.Run(context.Background(), RunRequest{Path: "f/t/missing", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Status != conformance.RunFailure || !strings.Contains(string(bad.Result.Producer()), `QuickJS evaluation error: Error: Not found: Variable u/t/missing not found`) {
		t.Fatalf("%s %s", bad.Status, bad.Result)
	}
}

// An engine without platform services has no resources or variables: a
// reference is a not-found, not left as written.
func TestEngineWithoutPlatform(t *testing.T) {
	s := &scripts{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("b", "echo", inputs("x", static(`"$var:u/t/v"`)), ""))}, s)
	tr := runIt(t, e, `{}`, nil)
	if tr.Status != conformance.RunFailure || !strings.Contains(string(tr.Result.Producer()), "Variable u/t/v not found for `x`") {
		t.Fatalf("%s %s", tr.Status, tr.Result)
	}
}

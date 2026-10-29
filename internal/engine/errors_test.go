package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Milestone N1b's tests: continue_on_error, the failure module and
// recovery, the error stops and the unrecoverable stop, chaining errors
// with a failure module, the final result and the cancellation outcome,
// on the same synthetic folders and the real QuickJS-ng engine as
// engine_test.go.

// withFailure writes a flow value with modules and a failure module.
func withFailure(failure string, mods ...string) string {
	return `{"modules":[` + strings.Join(mods, ",") + `],"failure_module":` + failure + `}`
}

// handler writes a failure module, id failure, that runs f/t/<script>.
func handler(script, inputs, envelope string) string { return mod("failure", script, inputs, envelope) }

var failureSite = conformance.SitePath{}.Child(conformance.FailureSlot())

// failureStep returns the failure module's record, and whether it has one.
func failureStep(tr *Trace) (StepRecord, bool) { return tr.Step(failureSite) }

// noStep fails unless the trace has no record of module id.
func noStep(t *testing.T, tr *Trace, id string) {
	t.Helper()
	if rec, ok := tr.Step(conformance.SitePath{}.Child(conformance.Module(id))); ok {
		t.Fatalf("%s ran: %+v", id, rec)
	}
}

// member returns a member of a JSON object's text.
func memberOf(t *testing.T, v jsonval.Value, key string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(v.Producer(), &m); err != nil {
		t.Fatalf("%s: %v", v, err)
	}
	return string(m[key])
}

const wrappedBoom = `{"error":{"message":"boom","name":"Error","step_id":"a"}}`

// FX-30, FX-6, FX-10: a failure that continue_on_error tolerates leaves
// its module Failure but advances the cursor; the next module sees the
// failure value as previous_result and as results.a, from memory, and
// storage holds it too. At the last module the flow succeeds with it, and
// the failure module does not run.
func TestEngineContinueOnError(t *testing.T) {
	tolerated := mod("a", "fail", "", `"continue_on_error":true`)
	s := &scripts{}
	e := mustOpen(t, map[string]string{
		"f/t/main":  flowOf(tolerated, mod("b", "echo", inputs("p", js("previous_result"), "r", js("results.a"), "m", js("results.a.error.message")), "")),
		"f/t/last":  withFailure(handler("echo", "", ""), tolerated),
		"f/t/plain": flowOf(mod("a", "fail", "", ""), mod("b", "echo", "", "")),
	}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"p":`+wrappedBoom+`,"r":`+wrappedBoom+`,"m":"boom"}`)
	if a := step(t, tr, "a"); a.Status != conformance.StepFailure || a.Module != conformance.ModuleFailure || !a.Ran || a.ResultOrigin != OriginUser {
		t.Fatalf("a %+v", a)
	}

	run := func(path string) *Trace {
		t.Helper()
		tr, err := e.Run(context.Background(), RunRequest{Path: path, Args: jsonval.FromProducer([]byte(`{}`))})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	s.invs = nil
	tr = run("f/t/last")
	wantStatus(t, tr, conformance.RunSuccess, wrappedBoom)
	if _, ok := failureStep(tr); ok || !slices.Equal(s.calls(), []string{"m:a"}) {
		t.Fatalf("the failure module ran for a tolerated failure: %v", s.calls())
	}
	tr = run("f/t/plain")
	wantStatus(t, tr, conformance.RunFailure, wrappedBoom)
	noStep(t, tr, "b")
}

// FX-31, FX-32, FX-33, JE-35: an untolerated failure starts the failure
// module at s:failure, whose transforms see the failing value as
// previous_result, its error as error, the failed module's id as the
// preceding id, and started_at and flow_job_id; its WM_FLOW_STEP_ID is its
// own id. The flow fails with the failure module's result, stored as it
// is, and later modules never run.
func TestEngineFailureModule(t *testing.T) {
	s := &scripts{}
	e := mustOpen(t, map[string]string{"f/t/main": withFailure(
		handler("echo", inputs("e", js("error"), "p", js("previous_result"), "r", js("results.a"), "id", js("flow_job_id"), "at", js("started_at")), ""),
		mod("a", "fail", "", ""), mod("b", "value", "", ""),
	)}, s)
	before := time.Now()
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "")
	noStep(t, tr, "b")
	f, ok := failureStep(tr)
	if !ok || f.Status != conformance.StepSuccess || f.Module != conformance.ModuleSuccess || !f.Ran || f.Skipped {
		t.Fatalf("failure module %+v", f)
	}
	sameJSON(t, "the run's result is the failure module's", tr.Result, string(f.Result.Producer()))
	for key, want := range map[string]string{"e": `{"message":"boom","name":"Error","step_id":"a"}`, "p": wrappedBoom, "r": wrappedBoom, "id": string(jsonString(tr.RunID))} {
		sameJSON(t, "the failure module's "+key, jsonval.FromProducer([]byte(memberOf(t, f.RecordedArgs, key))), want)
	}
	var at string
	if err := json.Unmarshal([]byte(memberOf(t, f.RecordedArgs, "at")), &at); err != nil {
		t.Fatal(err)
	}
	if started, err := time.Parse(time.RFC3339Nano, at); err != nil || started.Before(before.Add(-time.Second)) || !strings.HasSuffix(at, "Z") {
		t.Fatalf("started_at %q (%v)", at, err)
	}
	if inv := s.invs[1]; inv.Site != failureSite || inv.Env["WM_FLOW_STEP_ID"] != "failure" {
		t.Fatalf("the failure module's invocation %+v", inv)
	}

	for _, c := range []struct {
		name, flow, result string
		status             conformance.RunStatus
		calls              []string
		check              func(t *testing.T, f StepRecord)
	}{
		// FX-33: a failure module that fails fails the flow with its own
		// failure value.
		{name: "fails", flow: withFailure(handler("fail", inputs("name", static(`"H"`)), ""), mod("a", "fail", "", "")),
			status: conformance.RunFailure, result: `{"error":{"message":"boom","name":"H","step_id":"failure"}}`, calls: []string{"m:a", "s:failure"},
			check: func(t *testing.T, f StepRecord) {
				if f.Status != conformance.StepFailure || f.ResultOrigin != OriginUser {
					t.Fatalf("failure module %+v", f)
				}
			}},
		// FX-17, FX-32: its skip_if replaces it with a passthrough of the
		// failing value, never flagged skipped; the flow does not recover.
		{name: "skipped", flow: withFailure(handler("fail", "", `"skip_if":{"expr":"previous_result.error.message === 'boom'"}`), mod("a", "fail", "", "")),
			status: conformance.RunFailure, result: wrappedBoom, calls: []string{"m:a"},
			check: func(t *testing.T, f StepRecord) {
				if f.Skipped || f.Status != conformance.StepSuccess || !f.Ran {
					t.Fatalf("failure module %+v", f)
				}
			}},
		// FX-19, FX-30: it has no stop, and its own continue_on_error is
		// ignored; a throwing stop_after_if would have failed it.
		{name: "no stop", flow: withFailure(handler("value", inputs("v", static(`{"recover":false}`)), `"stop_after_if":{"expr":"null.x","skip_if_stopped":false},"continue_on_error":true`), mod("a", "fail", "", "")),
			status: conformance.RunFailure, result: `{"recover":false}`, calls: []string{"m:a", "s:failure"}},
		// FX-15 context 1: a chaining error while it starts fails the flow
		// at once with an InternalError, and it never starts.
		{name: "chaining", flow: withFailure(handler("echo", "", `"skip_if":{"expr":"nope"}`), mod("a", "fail", "", "")),
			status: conformance.RunFailure, result: `{"error":{"name":"InternalError","message":"Error: QuickJS evaluation error: Error: nope is not defined\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:42)\n"}}`, calls: []string{"m:a"},
			check: func(t *testing.T, f StepRecord) {
				if f.Ran || f.Status != conformance.StepNotRun {
					t.Fatalf("failure module %+v", f)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			tr := runIt(t, mustOpen(t, map[string]string{"f/t/main": c.flow}, s), `{}`, nil)
			wantStatus(t, tr, c.status, c.result)
			if !slices.Equal(s.calls(), c.calls) {
				t.Fatalf("calls %v, want %v", s.calls(), c.calls)
			}
			f, ok := failureStep(tr)
			if !ok {
				t.Fatal("no failure module record")
			}
			if c.check != nil {
				c.check(t, f)
			}
		})
	}
}

// FX-33: the flow recovers only when the failure module's job succeeded and
// its result decodes as the recovery record with recover true: an object's
// boolean member, or a one-element array by position.
func TestEngineRecover(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": withFailure(handler("value", inputs("v", js("flow_input.ret")), ""), mod("a", "fail", "", ""))}, &scripts{})
	for ret, recovers := range map[string]bool{
		`{"recover":true,"v":1}`: true,
		`[true]`:                 true,
		`{"recover":"true"}`:     false,
		`[false]`:                false,
		`[null]`:                 false,
		`[]`:                     false,
		`[true,false]`:           false,
		`{"recover":null}`:       false,
		`{"v":1}`:                false,
		`true`:                   false,
		`null`:                   false,
		`"recover"`:              false,
	} {
		tr := runIt(t, e, `{"ret":`+ret+`}`, nil)
		want := map[bool]conformance.RunStatus{true: conformance.RunSuccess, false: conformance.RunFailure}[recovers]
		if tr.Status != want {
			t.Errorf("%s: status %s, want %s", ret, tr.Status, want)
		}
		sameJSON(t, ret, tr.Result, ret)
	}
	// serde's struct decoder refuses a repeated field.
	if recovers(jsonval.FromProducer([]byte(`{"recover":true,"recover":true}`))) {
		t.Fatal("a repeated recover recovered")
	}
}

const earlyStop = `{"error":{"name":"EarlyStopError","message":"E"}}`

// FX-19, FX-V4, FX-10, FX-32: an error stop behaves as a failure carrying
// its EarlyStopError, while its job keeps its success (FX-V2): with no
// failure module and no tolerance the flow fails; a failure module runs
// with the failed module as the preceding id; continue_on_error advances
// the cursor to the next module, and past the last one to the failure
// module, whose preceding id is then empty, or ends the flow successfully
// when there is none.
func TestEngineErrorStops(t *testing.T) {
	stopping := func(envelope string) string {
		return mod("a", "obj", "", `"stop_after_if":{"expr":"true","skip_if_stopped":false,"error_message":"E"}`+envelope)
	}
	tolerated := `,"continue_on_error":true`
	b := mod("b", "value", inputs("v", static("4")), "")
	fm := handler("echo", inputs("p", js("previous_result"), "r", js("results.a"), "zz", js("results.a.zz")), "")
	const stored = `{"zz":1,"b":2,"aaa":3}`
	for _, c := range []struct {
		name, flow, result string
		status             conformance.RunStatus
		calls              []string
		// r is what the failure module reads as results.a: the carried
		// EarlyStopError from memory, or a's stored result.
		r string
	}{
		{name: "no handler", flow: flowOf(stopping(""), b), status: conformance.RunFailure, result: earlyStop, calls: []string{"m:a"}},
		{name: "handler", flow: withFailure(fm, stopping(""), b), status: conformance.RunFailure, calls: []string{"m:a", "s:failure"}, r: earlyStop},
		{name: "tolerated, next, handler", flow: withFailure(fm, stopping(tolerated), b), status: conformance.RunSuccess, result: `4`, calls: []string{"m:a", "m:b"}},
		{name: "tolerated, next, no handler", flow: flowOf(stopping(tolerated), b), status: conformance.RunFailure, result: earlyStop, calls: []string{"m:a"}},
		{name: "tolerated, last, no handler", flow: flowOf(stopping(tolerated)), status: conformance.RunSuccess, result: earlyStop, calls: []string{"m:a"}},
		{name: "tolerated, last, handler", flow: withFailure(fm, stopping(tolerated)), status: conformance.RunFailure, calls: []string{"m:a", "s:failure"}, r: stored},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			tr := runIt(t, mustOpen(t, map[string]string{"f/t/main": c.flow}, s), `{}`, nil)
			wantStatus(t, tr, c.status, c.result)
			if !slices.Equal(s.calls(), c.calls) {
				t.Fatalf("calls %v, want %v", s.calls(), c.calls)
			}
			a := step(t, tr, "a")
			if a.Status != conformance.StepSuccess || a.Module != conformance.ModuleFailure || a.CarriedOrigin != OriginEngine {
				t.Fatalf("a %+v", a)
			}
			sameJSON(t, "a's result", a.Result, stored)
			sameJSON(t, "a's carried value", a.Carried, earlyStop)
			f, ok := failureStep(tr)
			if ok != (c.r != "") {
				t.Fatalf("the failure module ran: %v", ok)
			}
			if ok {
				sameJSON(t, "the run's result", tr.Result, string(f.Result.Producer()))
				sameJSON(t, "previous_result", jsonval.FromProducer([]byte(memberOf(t, f.RecordedArgs, "p"))), earlyStop)
				sameJSON(t, "results.a", jsonval.FromProducer([]byte(memberOf(t, f.RecordedArgs, "r"))), c.r)
				if zz := memberOf(t, f.RecordedArgs, "zz"); zz != "1" {
					t.Fatalf("results.a.zz %s", zz)
				}
			}
		})
	}
}

// FX-21, FX-44, JE-26: a throwing stop_after_if fails its module
// unrecoverably with the predicate's text as a string error:
// continue_on_error does not apply, and the failure module runs, its error
// the string.
func TestEngineUnrecoverableStop(t *testing.T) {
	const text = `"Error: QuickJS evaluation error: Error: cannot read property 'x' of null\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:44)\n"`
	throwing := mod("a", "value", inputs("v", static("1")), `"stop_after_if":{"expr":"null.x","skip_if_stopped":false},"continue_on_error":true`)
	b := mod("b", "value", "", "")
	s := &scripts{}
	e := mustOpen(t, map[string]string{
		"f/t/main":    flowOf(throwing, b),
		"f/t/handled": withFailure(handler("echo", inputs("e", js("error"), "t", js("typeof error")), ""), throwing, b),
	}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, `{"error":`+text+`}`)
	noStep(t, tr, "b")
	if a := step(t, tr, "a"); a.Module != conformance.ModuleFailure || a.Status != conformance.StepSuccess || a.CarriedOrigin != OriginEngine || tr.ResultOrigin != OriginEngine {
		t.Fatalf("a %+v, origin %q", a, tr.ResultOrigin)
	}
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/handled", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, tr, conformance.RunFailure, `{"e":`+text+`,"t":"string"}`)
	noStep(t, tr, "b")
}

// FX-15, JE-35: a chaining error at the root's first pickup is a failure
// of the first module, with no job, which the failure module handles
// (JE-35/c) and continue_on_error tolerates; at a later transition the
// flow fails at once with an InternalError and the failure module does not
// run (JE-35/a).
func TestEngineChainingErrorsWithFailureModule(t *testing.T) {
	const anyhow = `{"error":{"name":"Anyhow","message":"Error: Error: QuickJS evaluation error: Error: nope is not defined\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:42)\n"}}`
	throwing := `"skip_if":{"expr":"nope"}`
	fm := handler("echo", inputs("e", js("error"), "r", js("results.a")), "")
	s := &scripts{}
	e := mustOpen(t, map[string]string{
		"f/t/main":      withFailure(fm, mod("a", "value", "", throwing)),
		"f/t/tolerated": flowOf(mod("a", "value", "", throwing+`,"continue_on_error":true`), mod("b", "echo", inputs("p", js("previous_result")), "")),
		"f/t/later":     withFailure(fm, mod("z", "value", "", ""), mod("a", "value", "", throwing)),
	}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "")
	if a := step(t, tr, "a"); a.Ran || a.Status != conformance.StepFailure || a.CarriedOrigin != OriginEngine {
		t.Fatalf("a %+v", a)
	}
	f, ok := failureStep(tr)
	if !ok {
		t.Fatal("the failure module did not run")
	}
	var inner struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(anyhow), &inner); err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "error", jsonval.FromProducer([]byte(memberOf(t, f.RecordedArgs, "e"))), string(inner.Error))
	sameJSON(t, "results.a", jsonval.FromProducer([]byte(memberOf(t, f.RecordedArgs, "r"))), anyhow)

	run := func(path string) *Trace {
		t.Helper()
		tr, err := e.Run(context.Background(), RunRequest{Path: path, Args: jsonval.FromProducer([]byte(`{}`))})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	wantStatus(t, run("f/t/tolerated"), conformance.RunSuccess, `{"p":`+anyhow+`}`)
	tr = run("f/t/later")
	wantStatus(t, tr, conformance.RunFailure, "")
	if name := memberOf(t, jsonval.FromProducer([]byte(memberOf(t, tr.Result, "error"))), "name"); name != `"InternalError"` {
		t.Fatalf("result %s", tr.Result)
	}
	if _, ok := failureStep(tr); ok {
		t.Fatal("the failure module ran after a later transition's chaining error")
	}
}

// cancelAt cancels the run when module at ends, before the completion
// checks.
type cancelAt struct {
	NoHooks
	at     string
	cancel context.CancelCauseFunc
	halt   string // HaltBefore matches this module
}

func (h *cancelAt) OnModuleEnd(_ context.Context, _ Run, s Site, _ StepRecord) error {
	if s.Module == h.at {
		h.cancel(&Cancellation{Reason: "r", Canceler: "u"})
	}
	return nil
}

func (h *cancelAt) HaltBefore(s Site) bool { return s.Module == h.halt }

const canceledRU = `{"error":{"message":"Job canceled: r by u","name":"Canceled","reason":"r","canceler":"u"}}`

// FX-38, FX-36: a canceled flow ends canceled with the Canceled error,
// naming the cause's reason and canceler; the step in flight is killed
// with the worker's text, and the failure module does not run. The
// continue decision checks a stop before the cancel flag, so a clean stop
// still ends canceled, while an error stop in a flow with a failure module
// starts what the decision asks for, and the run ends canceled at its next
// completion; a halt there does not make the run halted.
func TestEngineCancellation(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": withFailure(handler("echo", "", ""), mod("a", "value", "", ""))}, waits{})
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(50*time.Millisecond, func() { cancel(&Cancellation{Reason: "r", Canceler: "u"}) })
	tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, tr, conformance.RunCanceled, canceledRU)
	a := step(t, tr, "a")
	if a.Status != conformance.StepCanceled || !a.Abandoned || a.Module != conformance.ModuleFailure || a.ResultOrigin != OriginEngine {
		t.Fatalf("a %+v", a)
	}
	sameJSON(t, "a's result", a.Result, `{"error":{"message":"execution error:\njob process terminated due to cancelled by u (reason: r)","name":"ExecutionErr","step_id":"a"}}`)
	if _, ok := failureStep(tr); ok {
		t.Fatal("the failure module ran on cancel")
	}

	// Canceled before it starts, with Windmill's defaults.
	ctx, stop := context.WithCancel(context.Background())
	stop()
	tr, err = e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, tr, conformance.RunCanceled, `{"error":{"message":"Job canceled: no reason given by unknown","name":"Canceled","reason":"no reason given","canceler":"unknown"}}`)
	if len(tr.Steps) != 0 {
		t.Fatalf("steps %+v", tr.Steps)
	}

	errorStop := `"stop_after_if":{"expr":"true","skip_if_stopped":false,"error_message":"E"}`
	for _, c := range []struct {
		name, flow string
		halt       string
		// started lists the sites that started after a's completion.
		started []string
	}{
		{name: "clean stop", flow: flowOf(mod("a", "value", "", `"stop_after_if":{"expr":"true","skip_if_stopped":true}`), mod("b", "value", "", ""))},
		{name: "error stop, no handler", flow: flowOf(mod("a", "value", "", errorStop), mod("b", "value", "", ""))},
		{name: "error stop, handler", flow: withFailure(handler("echo", "", ""), mod("a", "value", "", errorStop)), started: []string{"s:failure"}},
		{name: "error stop, tolerated, handler", flow: withFailure(handler("echo", "", ""), mod("a", "value", "", errorStop+`,"continue_on_error":true`), mod("b", "value", "", "")), started: []string{"m:b"}},
		{name: "error stop, tolerated, handler, halt", flow: withFailure(handler("echo", "", ""), mod("a", "value", "", errorStop+`,"continue_on_error":true`), mod("b", "value", "", "")), halt: "b"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, s)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Hooks: &cancelAt{at: "a", cancel: cancel, halt: c.halt}})
			if err != nil {
				t.Fatal(err)
			}
			wantStatus(t, tr, conformance.RunCanceled, canceledRU)
			if got := s.calls()[1:]; !slices.Equal(got, c.started) && (len(got) != 0 || len(c.started) != 0) {
				t.Fatalf("started %v, want %v", got, c.started)
			}
		})
	}
}

// ADR 0008 with a failure module: H6 observes it; H7 latches a fatal error
// name before the failure module runs; haltAfter never applies to a
// completion that enters the failure module, and haltBefore never to the
// failure module.
func TestEngineHooksWithFailureModule(t *testing.T) {
	flow := withFailure(handler("value", inputs("v", static(`"H"`)), ""), mod("a", "fail", inputs("name", static(`"Fatal"`)), ""))
	h := &recordingHooks{haltAfter: "a", haltBefor: "failure"}
	tr := runIt(t, mustOpen(t, map[string]string{"f/t/main": flow}, &scripts{}), `{}`, h)
	wantStatus(t, tr, conformance.RunFailure, `"H"`)
	if want := []string{"start m:a", "end m:a failure", "start s:failure", "end s:failure success"}; !slices.Equal(h.calls, want) {
		t.Fatalf("calls %q\nwant %q", h.calls, want)
	}
	tr = runIt(t, mustOpen(t, map[string]string{"f/t/main": flow}, &scripts{}), `{}`, &recordingHooks{fatal: "Fatal"})
	if _, ok := failureStep(tr); tr.Status != conformance.RunLatched || ok {
		t.Fatalf("H7: status %s, steps %+v", tr.Status, tr.Steps)
	}
	// haltAfter applies to a tolerated failure, which goes on to the next
	// module.
	s := &scripts{}
	tr = runIt(t, mustOpen(t, map[string]string{"f/t/main": withFailure(handler("echo", "", ""), mod("a", "fail", "", `"continue_on_error":true`), mod("b", "echo", "", ""))}, s), `{}`, &recordingHooks{haltAfter: "a"})
	if tr.Status != conformance.RunHalted || !slices.Equal(s.calls(), []string{"m:a"}) {
		t.Fatalf("haltAfter a tolerated failure: status %s, calls %v", tr.Status, s.calls())
	}
}

// FX-16, JE-44: an identity job reads its arguments from storage, member
// by member, so its result is its previous_result in jsonb's printed form,
// as a flow input's member is.
func TestEngineIdentityResultText(t *testing.T) {
	tr := runIt(t, mustOpen(t, map[string]string{"f/t/main": flowOf(
		mod("a", "value", inputs("v", static(`{"k":[1, 2],"j":{"x":"a,b"}}`)), ""),
		`{"id":"b","value":{"type":"identity"}}`,
	)}, &scripts{}), `{}`, nil)
	if got := string(step(t, tr, "b").Result.Producer()); got != `{"j": {"x": "a,b"}, "k": [1, 2]}` {
		t.Fatalf("b's result %s", got)
	}
	wantStatus(t, tr, conformance.RunSuccess, `{"j":{"x":"a,b"},"k":[1,2]}`)
}

// nulResults answers f/t/obj with a result jsonb refuses, a string holding
// a NUL escape, and every other script as scripts does.
type nulResults struct{ scripts }

func (n *nulResults) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	if inv.Script != "f/t/obj" {
		return n.scripts.Execute(ctx, inv)
	}
	n.mu.Lock()
	n.invs = append(n.invs, inv)
	n.mu.Unlock()
	inv.Report.Settled(SettleCompleted)
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(`"x\u0000y"`))}, nil
}

// FX-48, OR-27: a result jsonb refuses fails its job at the completion
// insert with InternalErr and no step_id, an ordinary failure that the
// failure module handles; arguments jsonb refuses fail the push, a
// chaining error, which at the root's first pickup the failure module
// handles too (FX-15 context 2).
func TestEngineStorageFailures(t *testing.T) {
	s := &nulResults{}
	e := mustOpen(t, map[string]string{
		"f/t/main": withFailure(handler("echo", inputs("e", js("error")), ""), mod("a", "obj", "", ""), mod("b", "value", "", "")),
		"f/t/push": withFailure(handler("echo", inputs("e", js("error")), ""), mod("a", "echo", inputs("x", js("String.fromCharCode(0)")), "")),
	}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "")
	noStep(t, tr, "b")
	a := step(t, tr, "a")
	want := `{"error":{"message":"Internal: Could not add completed job ` + s.invs[0].ID + `: error returned from database: unsupported Unicode escape sequence","name":"InternalErr"}}`
	if a.Status != conformance.StepFailure || a.ResultOrigin != OriginEngine || !a.Ran {
		t.Fatalf("a %+v", a)
	}
	sameJSON(t, "a's result", a.Result, want)
	f, ok := failureStep(tr)
	if !ok {
		t.Fatal("the failure module did not run")
	}
	sameJSON(t, "the failure module's error", jsonval.FromProducer([]byte(memberOf(t, f.RecordedArgs, "e"))), memberOf(t, jsonval.FromProducer([]byte(want)), "error"))

	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/push", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, tr, conformance.RunFailure, `{"e":{"name":"Anyhow","message":"Error: error returned from database: unsupported Unicode escape sequence"}}`)
	if a := step(t, tr, "a"); a.Ran || a.Status != conformance.StepFailure {
		t.Fatalf("a %+v", a)
	}
}

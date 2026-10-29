package wl

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// hookFlows are synthetic flows of identity modules, which launch identity
// jobs that hooks see (ADR 0008), so no harness runs. f/h/main runs a, b
// and c in turn; f/h/loop runs a parallel loop over two items whose body
// is x then y; f/h/dup repeats the id d in one module list.
var hookFlows = map[string]string{
	"f/h/main": `{"modules":[{"id":"a","value":{"type":"identity"}},{"id":"b","value":{"type":"identity"}},{"id":"c","value":{"type":"identity"}}]}`,
	"f/h/loop": `{"modules":[{"id":"l","value":{"type":"forloopflow","iterator":{"type":"javascript","expr":"[1, 2]"},"skip_failures":false,"parallel":true,` +
		`"modules":[{"id":"x","value":{"type":"identity"}},{"id":"y","value":{"type":"identity"}}]}}]}`,
	"f/h/dup": `{"modules":[{"id":"d","value":{"type":"identity"}},{"id":"d","value":{"type":"identity"}}]}`,
}

func module(flow, id string) Selector {
	return Selector{Flow: flow, Module: &id}
}

func openHooks(t *testing.T) *Runtime {
	t.Helper()
	rt, err := Open(writeBundleOf(t, hookFlows, nil, Version(), nil, nil), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return rt
}

// H1, H2, H6 and H9 through wl's Go callbacks (ADR 0008): an interception
// by function and a static one answer for their modules' jobs, recorded as
// H1; a rewrite changes a module's arguments, the trace keeping both; the
// observers see every module, with its record at its end; OnRunEnd's
// warnings are the run's, and OnRunSettled gets the run's trace.
func TestHooksInterceptRewriteObserve(t *testing.T) {
	rt := openHooks(t)
	var mu sync.Mutex
	var started, ended []string
	var calls []Call
	settled := make(chan *Trace, 1)
	h := &Hooks{
		Intercept: []Interceptor{
			{Selector: module("f/h/main", "a"), Func: func(_ context.Context, c Call) (Outcome, bool, error) {
				mu.Lock()
				calls = append(calls, c)
				mu.Unlock()
				return Outcome{Result: json.RawMessage(`{"from":"a"}`)}, true, nil
			}},
			{Selector: module("f/h/main", "b"), Static: json.RawMessage(`2`)},
		},
		RewriteArgs: []ArgRewriter{{Selector: module("f/h/main", "c"), Func: func(_ context.Context, c Call) (json.RawMessage, error) {
			return json.RawMessage(`{"previous_result":"rewritten"}`), nil
		}}},
		OnModuleStart: func(_ context.Context, _ RunHandle, s Site) error {
			mu.Lock()
			defer mu.Unlock()
			started = append(started, s.Path.String())
			return nil
		},
		OnModuleEnd: func(_ context.Context, _ RunHandle, s Site, rec StepRecord) error {
			mu.Lock()
			defer mu.Unlock()
			ended = append(ended, s.Path.String()+" "+string(rec.Status))
			return nil
		},
		OnRunEnd: func(_ context.Context, tr *Trace) []Warning {
			return []Warning{{Code: "host_note", Message: "ended " + string(tr.Status)}}
		},
		OnRunSettled: func(tr *Trace) { settled <- tr },
	}
	res, err := rt.Run(context.Background(), "f/h/main", json.RawMessage(`{"in":1}`), RunOptions{Hooks: h})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusSuccess || string(res.Result) != `"rewritten"` {
		t.Fatalf("%s %s", res.Status, res.Result)
	}
	rec := func(id string) StepRecord {
		r, ok := res.Trace.Step(conformance.SitePath{}.Child(conformance.Module(id)))
		if !ok {
			t.Fatalf("no record of %s", id)
		}
		return r
	}
	if a := rec("a"); string(a.Result.Producer()) != `{"from":"a"}` || !slices.Equal(a.Hooks, []string{"H1"}) {
		t.Errorf("a: %s %v", a.Result.Producer(), a.Hooks)
	}
	if b := rec("b"); string(b.Result.Producer()) != `2` || !slices.Equal(b.Hooks, []string{"H1"}) {
		t.Errorf("b: %s %v", b.Result.Producer(), b.Hooks)
	}
	if c := rec("c"); !slices.Equal(c.Hooks, []string{"H2"}) || string(c.ArgsBeforeRewrite.Producer()) != `{"previous_result":2}` {
		t.Errorf("c: %v %s", c.Hooks, c.ArgsBeforeRewrite.Producer())
	}
	if len(calls) != 1 || calls[0].Site.Flow != "f/h/main" || calls[0].Site.Module != "a" || calls[0].Run.ID() != res.ID ||
		string(calls[0].Args) != `{"previous_result":{"in":1}}` {
		t.Errorf("the interception's call: %+v", calls)
	}
	if !slices.Equal(started, []string{"m:a", "m:b", "m:c"}) || !slices.Equal(ended, []string{"m:a success", "m:b success", "m:c success"}) {
		t.Errorf("observed %q, %q", started, ended)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "host_note" || res.Warnings[0].Message != "ended success" {
		t.Errorf("warnings %+v", res.Warnings)
	}
	if tr := <-settled; tr.RunID != res.ID {
		t.Errorf("settled %s", tr.RunID)
	}
}

// H8: haltBefore ends the run halted before a matching module starts, and
// haltAfter once it has its final outcome. A hook's error and a Latch end
// the run latched with it (H5). Interception narrowed to an iteration
// answers only there, and a script selector names nothing here.
func TestHooksHaltLatchNarrow(t *testing.T) {
	rt := openHooks(t)
	ran := func(res *Result, id string) bool {
		_, ok := res.Trace.Step(conformance.SitePath{}.Child(conformance.Module(id)))
		return ok
	}
	res, err := rt.Run(context.Background(), "f/h/main", nil, RunOptions{Hooks: &Hooks{HaltBefore: []Selector{module("f/h/main", "b")}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusHalted || !ran(res, "a") || ran(res, "b") {
		t.Fatalf("halt before b: %s, records %d", res.Status, len(res.Trace.Steps))
	}
	res, err = rt.Run(context.Background(), "f/h/main", nil, RunOptions{Hooks: &Hooks{HaltAfter: []Selector{module("f/h/main", "b")}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusHalted || !ran(res, "b") || ran(res, "c") {
		t.Fatalf("halt after b: %s", res.Status)
	}

	boom := errors.New("host says no")
	res, err = rt.Run(context.Background(), "f/h/main", nil, RunOptions{Hooks: &Hooks{OnModuleStart: func(_ context.Context, r RunHandle, s Site) error {
		if s.Module == "b" {
			return boom
		}
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusLatched || !errors.Is(res.Latched, boom) || ran(res, "c") {
		t.Fatalf("a hook's error: %s %v", res.Status, res.Latched)
	}
	res, err = rt.Run(context.Background(), "f/h/main", nil, RunOptions{Hooks: &Hooks{OnModuleEnd: func(_ context.Context, r RunHandle, s Site, _ StepRecord) error {
		if s.Module == "a" {
			r.Latch(boom)
		}
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusLatched || !errors.Is(res.Latched, boom) {
		t.Fatalf("a latch: %s %v", res.Status, res.Latched)
	}

	sel := module("f/h/loop", "y")
	sel.Iterations = []int{1}
	res, err = rt.Run(context.Background(), "f/h/loop", nil, RunOptions{Hooks: &Hooks{Intercept: []Interceptor{
		{Selector: sel, Static: json.RawMessage(`"second"`)},
		{Selector: Selector{Script: "f/h/none"}, Static: json.RawMessage(`"never"`)},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusSuccess || !strings.Contains(string(res.Result), `"second"`) || strings.Contains(string(res.Result), "never") {
		t.Fatalf("narrowed: %s %s", res.Status, res.Result)
	}
	var y0, y1 StepRecord
	for _, r := range res.Trace.Steps {
		switch r.Site.String() {
		case "m:l/i:0/m:y":
			y0 = r
		case "m:l/i:1/m:y":
			y1 = r
		}
	}
	if len(y0.Hooks) != 0 || !slices.Equal(y1.Hooks, []string{"H1"}) {
		t.Fatalf("iteration 0's y %v, iteration 1's %v", y0.Hooks, y1.Hooks)
	}
}

// Selectors are checked before the run starts (ADR 0008): one that names
// both or neither of a module and a script, or narrows a script, is
// ErrSelector; a module selector without an occurrence whose id repeats in
// one module list is ErrAmbiguousSelector, which an occurrence settles.
func TestHookSelectors(t *testing.T) {
	rt := openHooks(t)
	d := "d"
	for _, c := range []struct {
		sel  Selector
		want error
	}{
		{Selector{Flow: "f/h/main"}, ErrSelector},
		{Selector{Flow: "f/h/main", Module: &d, Script: "f/h/x"}, ErrSelector},
		{Selector{Script: "f/h/x", Iterations: []int{0}}, ErrSelector},
		{Selector{Flow: "f/h/main", Module: &d, Branches: []string{"first"}}, ErrSelector},
		{module("f/h/dup", "d"), ErrAmbiguousSelector},
	} {
		_, err := rt.Run(context.Background(), "f/h/main", nil, RunOptions{Hooks: &Hooks{HaltBefore: []Selector{c.sel}}})
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.sel, err, c.want)
		}
	}
	one := 1
	sel := module("f/h/dup", "d")
	sel.Occurrence = &one
	res, err := rt.Run(context.Background(), "f/h/dup", nil, RunOptions{Hooks: &Hooks{Intercept: []Interceptor{{Selector: sel, Static: json.RawMessage(`"second d"`)}}}})
	if err != nil || res.Status != StatusSuccess || string(res.Result) != `"second d"` {
		t.Fatalf("an occurrence: %+v %v", res, err)
	}
	if _, err := rt.Run(context.Background(), "f/h/main", nil, RunOptions{Hooks: &Hooks{Intercept: []Interceptor{{Selector: module("f/h/main", "a")}}}}); err == nil {
		t.Error("ran with an interception that has neither Static nor Func")
	}
}

// providedFlows: f/p/main's step wait calls f/p/permit, which suspends
// (FX-39) and so compiles only behind a provided site; its stop_after_if
// stops the flow when the permit is refused; the identity step after it
// passes the permit on.
var providedFlows = map[string]string{
	"f/p/main": `{"modules":[{"id":"wait","value":{"type":"flow","path":"f/p/permit","input_transforms":{"key":{"type":"javascript","expr":"flow_input.key"}}},` +
		`"stop_after_if":{"expr":"result.granted === false","skip_if_stopped":false}},{"id":"done","value":{"type":"identity"}}]}`,
	"f/p/permit": `{"modules":[{"id":"s","value":{"type":"identity"},"suspend":{"required_events":1,"timeout":60}}]}`,
}

const providedContract = `{"roots":[{"flow":"f/p/main"}],"provided":[{"selector":{"flow":"f/p/main","module":"wait"},"provider":"permit","mandatory":true}]}`

// The run-time half of ADR 0002's provided-site policy case, through wl:
// with the provider registered, the run succeeds with the provider's value,
// which the provider computed from the site's arguments, and the site's
// stop_after_if applies to it; with no provider registered, Open fails
// with missing_provider; a provider that declines fails the run closed
// with provider_declined.
func TestProvidedSitePolicy(t *testing.T) {
	dir := writeBundleOf(t, providedFlows, []byte(providedContract), Version(), nil, nil)
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrMissingProvider) || !strings.Contains(err.Error(), `"permit"`) {
		t.Fatalf("no provider: %v", err)
	}
	if _, err := Open(dir, Options{Providers: map[string]Provider{"other": nil}}); !errors.Is(err, ErrMissingProvider) {
		t.Fatalf("another provider: %v", err)
	}
	rt, err := Open(dir, Options{Providers: map[string]Provider{"permit": func(_ context.Context, c Call) (Outcome, bool, error) {
		var a struct{ Key string }
		if err := json.Unmarshal(c.Args, &a); err != nil {
			return Outcome{}, false, err
		}
		switch a.Key {
		case "decline":
			return Outcome{}, false, nil
		case "refuse":
			return Outcome{Result: json.RawMessage(`{"granted":false}`)}, true, nil
		case "fail":
			return Outcome{Result: json.RawMessage(`{"name":"PermitError","message":"no permit"}`), Failed: true}, true, nil
		}
		return Outcome{Result: json.RawMessage(`{"granted":true,"ticket":"t-` + a.Key + `"}`)}, true, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	for _, c := range []struct {
		key, status, result string
		done                bool
	}{
		{"k1", StatusSuccess, `{"ticket":"t-k1","granted":true}`, true},
		{"refuse", StatusSuccess, `{"granted":false}`, false},
		{"fail", StatusFailure, `{"error":{"name":"PermitError","message":"no permit"}}`, false},
	} {
		res, err := rt.Run(context.Background(), "f/p/main", json.RawMessage(`{"key":"`+c.key+`"}`), RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, done := res.Trace.Step(conformance.SitePath{}.Child(conformance.Module("done")))
		if res.Status != c.status || string(res.Result) != c.result || done != c.done {
			t.Errorf("%s: %s %s, done ran %v", c.key, res.Status, res.Result, done)
		}
	}
	res, err := rt.Run(context.Background(), "f/p/main", json.RawMessage(`{"key":"decline"}`), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusLatched || !errors.Is(res.Latched, ErrProviderDeclined) {
		t.Fatalf("a declining provider: %s %v", res.Status, res.Latched)
	}
}

// H13 through wl: a run's own variables replace the runtime's for that run
// only, as the expression helpers read them (ADR 0007), and the run's
// resolver supplies a secret's plaintext; the next run without them reads
// the runtime's.
func TestRunPlatform(t *testing.T) {
	flows := map[string]string{"f/t/which": testFlows["f/t/which"]}
	dir := writeBundleOf(t, flows, nil, Version(), map[string]string{"u/t/v": "bundle"}, nil)
	rt, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	for _, c := range []struct {
		p    *RunPlatform
		want string
	}{
		{&RunPlatform{Variables: map[string]Variable{"u/t/v": {Value: "host"}}}, `"host"`},
		{nil, `"bundle"`},
		{&RunPlatform{
			Variables: map[string]Variable{"u/t/v": {Value: "marker", Secret: true}},
			Resolve: func(_ context.Context, path string, resource bool, v json.RawMessage) (json.RawMessage, error) {
				if path == "u/t/v" && !resource && string(v) == `"marker"` {
					return json.RawMessage(`"host"`), nil
				}
				return v, nil
			},
		}, `"host"`},
	} {
		res, err := rt.Run(context.Background(), "f/t/which", nil, RunOptions{Platform: c.p})
		if err != nil || res.Status != StatusSuccess || string(res.Result) != c.want {
			t.Errorf("%+v: %+v %v, want %s", c.p, res, err, c.want)
		}
	}
}

// A host-provided site keeps the call-site identity of the step it
// replaces (ADR 0002, ADR 0008): a script selector names a provided path
// script by its path and a provided inline script by <flow path>/<module
// id>, so H2 rewrites what the provider gets, HaltBefore stops before the
// provider runs and HaltAfter once its value is final. An optional H1 on
// the site does not apply: the mandatory provider answers it.
func TestHooksOnProvidedScripts(t *testing.T) {
	for _, c := range []struct{ kind, value, script string }{
		{"script", `{"type":"script","path":"f/opaque/work","input_transforms":{"v":{"type":"static","value":"original"}}}`, "f/opaque/work"},
		{"rawscript", `{"type":"rawscript","language":"bun","content":"export function main(v) { return v }","input_transforms":{"v":{"type":"static","value":"original"}}}`, "f/q/main/work"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			flows := map[string]string{"f/q/main": `{"modules":[{"id":"work","value":` + c.value + `},{"id":"tail","value":{"type":"identity"}}]}`}
			contract := []byte(`{"roots":[{"flow":"f/q/main"}],"provided":[{"selector":{"flow":"f/q/main","module":"work"},"provider":"work","mandatory":true}]}`)
			var mu sync.Mutex
			var got []string
			rt, err := Open(writeBundleOf(t, flows, contract, Version(), nil, nil), Options{Providers: map[string]Provider{"work": func(_ context.Context, call Call) (Outcome, bool, error) {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, call.Site.Script+" "+string(call.Args))
				return Outcome{Result: call.Args}, true, nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			sel := Selector{Script: c.script}
			run := func(h *Hooks) *Result {
				t.Helper()
				mu.Lock()
				got = nil
				mu.Unlock()
				res, err := rt.Run(context.Background(), "f/q/main", nil, RunOptions{Hooks: h})
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			ran := func(res *Result, id string) bool {
				_, ok := res.Trace.Step(conformance.SitePath{}.Child(conformance.Module(id)))
				return ok
			}

			if res := run(&Hooks{HaltBefore: []Selector{sel}}); res.Status != StatusHalted || len(got) != 0 {
				t.Errorf("HaltBefore: %s, provider calls %q", res.Status, got)
			}
			if res := run(&Hooks{HaltAfter: []Selector{sel}}); res.Status != StatusHalted || len(got) != 1 || ran(res, "tail") {
				t.Errorf("HaltAfter: %s, provider calls %q, tail ran %v", res.Status, got, ran(res, "tail"))
			}
			res := run(&Hooks{RewriteArgs: []ArgRewriter{{Selector: sel, Func: func(context.Context, Call) (json.RawMessage, error) {
				return json.RawMessage(`{"v":"rewritten"}`), nil
			}}}})
			if res.Status != StatusSuccess || string(res.Result) != `{"v":"rewritten"}` || len(got) != 1 || got[0] != c.script+` {"v":"rewritten"}` {
				t.Errorf("H2: %s %s, provider calls %q", res.Status, res.Result, got)
			}
			res = run(&Hooks{Intercept: []Interceptor{{Selector: sel, Static: json.RawMessage(`"intercepted"`)}}})
			if res.Status != StatusSuccess || string(res.Result) != `{"v":"original"}` || len(got) != 1 {
				t.Errorf("H1 on a provided site: %s %s, provider calls %q", res.Status, res.Result, got)
			}
		})
	}
}

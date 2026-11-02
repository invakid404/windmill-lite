package wl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// A host plugin needs a warm harness: a fresh runtime refuses one, and in
// host mode the plugin is the host's, not Options.Plugin (F2c).
func TestPluginRefusedWhereItCannotRun(t *testing.T) {
	dir := writeTestBundle(t, "test-engine", nil, nil)
	if _, err := Open(dir, Options{EngineVersion: "test-engine", ModuleState: ModuleStateFresh, Plugin: "/nonexistent/plugin.ts"}); err == nil || !strings.Contains(err.Error(), "warm") {
		t.Fatalf("a fresh runtime with a plugin: %v", err)
	}
	work := t.TempDir()
	_, err, _, w := openHost(t, dir, Options{Plugin: "/nonexistent/plugin.ts", Host: &Host{Workdir: work}}, work)
	if err == nil || w.Reject == nil || w.Reject.Code != RejectEngine {
		t.Fatalf("host mode with Options.Plugin: %v, welcome %+v", err, w)
	}
}

// In host mode the handshake checks the host's plugin declaration and
// refuses a host whose selectors are malformed or ambiguous with
// plugin_mismatch; a usable one is welcomed, and its runs get its hooks.
func TestHostPluginDeclarationChecked(t *testing.T) {
	dir := writeTestBundle(t, "test-engine", nil, nil)
	work := t.TempDir()
	for _, tc := range []struct {
		name, plugin, reject string
	}{
		{"a selector with neither module nor script", `{"intercept":[{"selector":{"flow":"f/t/echo"}}]}`, "a flow and a module"},
		{"a selector with both", `{"halt_before":[{"flow":"f/t/echo","module":"a","script":"f/t/x"}]}`, "a flow and a module, or a script"},
		{"an ambiguous selector", `{"rewrite_args":[{"selector":{"flow":"f/t/dup","module":"a"}}]}`, "ambiguous_selector"},
		{"usable", `{"halt_before":[{"flow":"f/t/echo","module":"a"}],"on_module_start":true}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, err, _, w := openPluginHost(t, dir, Options{Host: &Host{Workdir: work}}, work, tc.plugin)
			if tc.name == "usable" {
				if err != nil || w.Config == nil {
					t.Fatalf("%v, welcome %+v", err, w)
				}
				if rt.plugin.Load() == nil || len(rt.plugin.Load().hooks.HaltBefore) != 1 {
					t.Fatal("the runtime has not the host's plugin")
				}
				return
			}
			if err == nil || w.Reject == nil || w.Reject.Code != harness.RejectPlugin || !strings.Contains(w.Reject.Message, tc.reject) {
				t.Fatalf("%v, welcome %+v; want plugin_mismatch naming %q", err, w, tc.reject)
			}
		})
	}
}

// The plugin's providers count toward the host contract's named providers
// (missing_provider), and a name cannot be both a Go provider and the
// plugin's.
func TestPluginProviders(t *testing.T) {
	mod := "a"
	prog := &ir.Program{Flows: map[string]*ir.Flow{}, Contract: ir.Contract{Provided: []ir.ProvidedSite{{Selector: ir.Selector{Flow: "f/x", Module: &mod}, Provider: "permit", Mandatory: true}}}}
	call := func(context.Context, string, string, string, jsonval.Value) (jsonval.Value, error) {
		return jsonval.FromProducer([]byte(`{"result":1}`)), nil
	}
	rt := &Runtime{prog: prog}
	if err := rt.usePlugin(jsonval.FromProducer([]byte(`{"providers":["other"]}`)), call, 0, func(string) {}); !errors.Is(err, ErrMissingProvider) {
		t.Fatalf("%v; want missing_provider", err)
	}
	if err := rt.usePlugin(jsonval.FromProducer([]byte(`{"providers":["permit"]}`)), call, 0, func(string) {}); err != nil {
		t.Fatal(err)
	}
	rt = &Runtime{prog: prog, goProviders: map[string]Provider{"permit": func(context.Context, Call) (Outcome, bool, error) { return Outcome{}, false, nil }}}
	if err := rt.usePlugin(jsonval.FromProducer([]byte(`{"providers":["permit"]}`)), call, 0, func(string) {}); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("%v; want a provider named twice refused", err)
	}
}

// The declaration becomes Go hooks whose callbacks send host_calls: each
// with its hook, run, site and payload, an interception by its index; a
// reply's decline, an error and a timeout are what F2a's seams take.
func TestPluginHooksAsHostCalls(t *testing.T) {
	type sent struct {
		hook, run, site string
		payload         map[string]any
	}
	var calls []sent
	reply := `{"result":7}`
	var replyErr error
	call := func(ctx context.Context, hook, run, site string, payload jsonval.Value) (jsonval.Value, error) {
		var m map[string]any
		json.Unmarshal(payload.Producer(), &m)
		calls = append(calls, sent{hook, run, site, m})
		if replyErr != nil {
			return jsonval.Value{}, replyErr
		}
		return jsonval.FromProducer([]byte(reply)), nil
	}
	prog := &ir.Program{Flows: map[string]*ir.Flow{}}
	decl := `{"intercept":[{"selector":{"script":"f/x/s"},"static":{"s":1}},{"selector":{"script":"f/x/t"}}],"around":[{"selector":{"script":"f/x/t"}}]}`
	p, err := newPlugin(jsonval.FromProducer([]byte(decl)), prog, call, 0, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if string(p.hooks.Intercept[0].Static) != `{"s":1}` || p.hooks.Intercept[0].Func != nil {
		t.Fatal("a static interception asks the plugin nothing")
	}
	out, ok, err := p.hooks.Intercept[1].Func(context.Background(), Call{Run: runID("r1"), Site: Site{Script: "f/x/t"}, Args: json.RawMessage(`{"a":1}`)})
	if err != nil || !ok || string(out.Result) != "7" || len(calls) != 1 || calls[0].hook != "intercept" || calls[0].run != "r1" || calls[0].payload["index"] != float64(1) {
		t.Fatalf("%+v %v %v; calls %+v", out, ok, err, calls)
	}
	reply = `{"decline":true}`
	if _, ok, err := p.hooks.Intercept[1].Func(context.Background(), Call{Run: runID("r1"), Site: Site{Script: "f/x/t"}, Args: json.RawMessage(`{}`)}); ok || err != nil {
		t.Fatalf("a decline: %v %v", ok, err)
	}
	if got := p.around(Site{Script: "f/x/t"}); len(got) != 1 || got[0] != 0 {
		t.Fatalf("around %v", got)
	}
	if got := p.around(Site{Script: "f/x/s"}); len(got) != 0 {
		t.Fatalf("around %v", got)
	}
	// A reply that never comes within the plugin timeout.
	slow, _ := newPlugin(jsonval.FromProducer([]byte(`{"on_module_start":true}`)), prog, func(ctx context.Context, _, _, _ string, _ jsonval.Value) (jsonval.Value, error) {
		<-ctx.Done()
		return jsonval.Value{}, context.Cause(ctx)
	}, 20e6, func(string) {})
	if err := slow.hooks.OnModuleStart(context.Background(), runID("r1"), Site{}); !errors.Is(err, ErrHostCallTimeout) {
		t.Fatalf("%v; want host_call_timeout", err)
	}
	// The run's own end is not a timeout: its cause comes back, which the
	// engine does not latch on.
	ctx, cancel := context.WithCancelCause(context.Background())
	stop := errors.New("the run ended")
	cancel(stop)
	if err := slow.hooks.OnModuleStart(ctx, runID("r1"), Site{}); !errors.Is(err, stop) {
		t.Fatalf("%v; want the run's cause", err)
	}
}

// mergeHooks puts the run's own hooks first, then the plugin's.
func TestMergeHooks(t *testing.T) {
	var order []string
	a := &Hooks{OnModuleStart: func(context.Context, RunHandle, Site) error { order = append(order, "go"); return nil },
		OnRunEnd: func(context.Context, *Trace) []Warning { return []Warning{{Code: "go"}} }}
	b := &Hooks{OnModuleStart: func(context.Context, RunHandle, Site) error { order = append(order, "plugin"); return nil },
		OnRunEnd: func(context.Context, *Trace) []Warning { return []Warning{{Code: "plugin"}} }}
	m := mergeHooks(a, b)
	m.OnModuleStart(context.Background(), runID("r"), Site{})
	ws := m.OnRunEnd(context.Background(), &Trace{})
	if strings.Join(order, ",") != "go,plugin" || len(ws) != 2 || ws[0].Code != "go" || ws[1].Code != "plugin" {
		t.Fatalf("order %v, warnings %+v", order, ws)
	}
}

// runID is a RunHandle with an id, for hooks called directly.
type runID string

func (r runID) ID() string    { return string(r) }
func (runID) Latch(err error) {}

// A plugin's hook holds its run's settlement while the host_call is in
// flight, from before it is sent (F2c review R2-1): the run does not
// settle while the hook runs; and a hook of a run that has settled is not
// called at all, its resources being gone.
func TestPluginCallsHoldSettlement(t *testing.T) {
	rt := openHooks(t)
	inCall := make(chan struct{})
	finish := make(chan struct{})
	var calls atomic.Int32
	call := func(ctx context.Context, hook, run, site string, payload jsonval.Value) (jsonval.Value, error) {
		if calls.Add(1) == 1 {
			close(inCall)
			<-finish
		}
		return jsonval.FromProducer([]byte(`{}`)), nil
	}
	p, err := newPlugin(jsonval.FromProducer([]byte(`{}`)), rt.prog, call, time.Minute, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	settled := make(chan struct{})
	var runCtx context.Context
	hooks := &Hooks{
		OnModuleStart: func(ctx context.Context, r RunHandle, s Site) error {
			if s.Module == "a" {
				runCtx = ctx
				// Work the run's own hook started, which outlives the run's
				// outcome: a provider call a step began, say.
				go p.do(ctx, "variable", r.ID(), nil, map[string]any{"path": "u/x/v"}, nil)
				<-inCall
			}
			return nil
		},
		OnRunSettled: func(*Trace) { close(settled) },
	}
	res, err := rt.Run(context.Background(), "f/h/main", nil, RunOptions{Hooks: hooks})
	if err != nil || res.Status != StatusSuccess {
		t.Fatalf("%v %v", res, err)
	}
	select {
	case <-settled:
		t.Fatal("the run settled while a hook's host_call was in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(finish)
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never settled after the hook answered")
	}
	if err := p.do(runCtx, "variable", res.ID, nil, map[string]any{"path": "u/x/v"}, nil); !errors.Is(err, errRunSettled) || calls.Load() != 1 {
		t.Fatalf("%v, %d calls: a hook of a settled run must not be called", err, calls.Load())
	}
}

// A run-end hook gets the trace its plugin declares (trace_detail): the
// full trace by default, the run's own fields without its per-step parts
// for summary, the run's id and status alone for none. A declaration
// that names a hook it lacks, or a level that is not one, is refused.
func TestPluginTraceDetail(t *testing.T) {
	prog := &ir.Program{Flows: map[string]*ir.Flow{}}
	tr := &Trace{
		RunID: "r1", Bundle: "b1", Status: "success", Deviations: []string{"D21"},
		Warnings: []Warning{{Code: "c", Message: "m"}},
		Result:   jsonval.FromProducer([]byte(`{"big":"result"}`)),
	}
	full := map[string]any{}
	for _, tc := range []struct {
		detail string
		keys   []string
	}{
		{"", nil},
		{`"full"`, nil},
		{`"summary"`, []string{"bundle", "cached", "deviations", "may_have_mutated", "module_state", "run", "shared_code", "status", "warnings"}},
		{`"none"`, []string{"run", "status"}},
	} {
		decl := `{"on_run_end":true,"on_run_settled":true}`
		if tc.detail != "" {
			decl = `{"on_run_end":true,"on_run_settled":true,"trace_detail":{"on_run_end":` + tc.detail + `,"on_run_settled":` + tc.detail + `}}`
		}
		got := map[string]map[string]any{}
		call := func(_ context.Context, hook, _, _ string, payload jsonval.Value) (jsonval.Value, error) {
			var m struct {
				Trace map[string]any `json:"trace"`
			}
			if err := json.Unmarshal(payload.Producer(), &m); err != nil {
				t.Fatal(err)
			}
			got[hook] = m.Trace
			return jsonval.FromProducer([]byte(`{}`)), nil
		}
		p, err := newPlugin(jsonval.FromProducer([]byte(decl)), prog, call, 0, func(string) {})
		if err != nil {
			t.Fatalf("%s: %v", decl, err)
		}
		p.hooks.OnRunEnd(context.Background(), tr)
		p.hooks.OnRunSettled(tr)
		for _, hook := range []string{"on_run_end", "on_run_settled"} {
			m := got[hook]
			if tc.keys == nil {
				if _, ok := m["steps"]; !ok || m["result"] == nil {
					t.Fatalf("%s %s: the full trace has no steps or result: %v", tc.detail, hook, m)
				}
				if len(full) == 0 {
					full = m
				} else if !maps.EqualFunc(full, m, func(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }) {
					t.Fatalf("%s %s: %v, the default gave %v", tc.detail, hook, m, full)
				}
				continue
			}
			if k := slices.Sorted(maps.Keys(m)); !slices.Equal(k, tc.keys) {
				t.Fatalf("%s %s: keys %v, want %v", tc.detail, hook, k, tc.keys)
			}
			for _, k := range tc.keys {
				if fmt.Sprint(m[k]) != fmt.Sprint(full[k]) {
					t.Fatalf("%s %s: %s is %v, the full trace's %v", tc.detail, hook, k, m[k], full[k])
				}
			}
		}
	}
	for decl, want := range map[string]string{
		`{"on_run_end":true,"trace_detail":{"on_run_settled":"none"}}`: "does not declare",
		`{"on_run_end":true,"trace_detail":{"on_module_end":"none"}}`:  "not a run-end hook",
		`{"on_run_end":true,"trace_detail":{"on_run_end":"some"}}`:     "not full, summary or none",
	} {
		if _, err := newPlugin(jsonval.FromProducer([]byte(decl)), prog, nil, 0, func(string) {}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", decl, err, want)
		}
	}
}

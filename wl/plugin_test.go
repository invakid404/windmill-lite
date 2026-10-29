package wl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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

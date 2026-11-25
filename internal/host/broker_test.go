package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/wlext"
	"github.com/invakid404/windmill-lite/wl"
	"github.com/invakid404/windmill-lite/wl/plugin"
)

// The broker tests run the engine in-process: a Server over a wl runtime,
// behind a Gate, served on go-plugin's test connection, which has a real
// broker on both sides, with the host's *plugin.Client dispensed from it.
// Their bundle holds the plugin cases' folder (conformance/plugins) and
// the flows below.

// brokerScripts and brokerFlows are the broker tests' own scripts and
// flows, beside the cases'.
var brokerScripts = map[string]string{
	// Writes started into its directory, then waits until go is there.
	"f/broker/hold.ts": `import { existsSync, writeFileSync } from "node:fs";
export async function main(dir: string) {
  writeFileSync(dir + "/started", "");
  while (!existsSync(dir + "/go")) await Bun.sleep(10);
  return "released";
}
`,
	// Returns a text of n bytes.
	"f/broker/big.ts": `export async function main(n: number) {
  return "x".repeat(n);
}
`,
	// Returns its argument with the value of the resource u/broker/r, read
	// through the job API.
	"f/broker/getres.ts": `export async function main(prev: unknown) {
  const r = await fetch(` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/resources/get_value_interpolated/u/broker/r`" + `, {
    headers: { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + ` },
  });
  return { prev, res: r.status === 200 ? await r.json() : r.status };
}
`,
}

var brokerFlows = map[string]string{
	// a is intercepted by a run's hooks and by a host plugin; r reads a
	// resource that both answer.
	"f/broker/order": `{"summary": "order", "value": {"modules": [
  {"id": "a", "value": {"type": "script", "path": "f/plugins/echo", "input_transforms": {"x": {"type": "static", "value": "step"}}}},
  {"id": "r", "value": {"type": "script", "path": "f/broker/getres", "input_transforms": {"prev": {"type": "javascript", "expr": "results.a"}}}}
]}}`,
	// a is a mandatory site of the named provider bp.
	"f/broker/prov": `{"summary": "prov", "value": {"modules": [
  {"id": "a", "value": {"type": "script", "path": "f/plugins/label", "input_transforms": {"label": {"type": "static", "value": "step"}}}}
]}}`,
	// a waits for its directory's go file.
	"f/broker/hold": `{"summary": "hold", "value": {"modules": [
  {"id": "a", "value": {"type": "script", "path": "f/broker/hold", "input_transforms": {"dir": {"type": "javascript", "expr": "flow_input.dir"}}}}
]}}`,
	// a returns 5 MiB, which the full trace holds; b returns a label.
	"f/broker/big": `{"summary": "big", "value": {"modules": [
  {"id": "a", "value": {"type": "script", "path": "f/broker/big", "input_transforms": {"n": {"type": "static", "value": 5242880}}}},
  {"id": "b", "value": {"type": "script", "path": "f/plugins/label", "input_transforms": {"label": {"type": "static", "value": "small"}}}}
]}}`,
	// a waits for its directory's go file; b is a mandatory site of bp.
	"f/broker/holdprov": `{"summary": "holdprov", "value": {"modules": [
  {"id": "a", "value": {"type": "script", "path": "f/broker/hold", "input_transforms": {"dir": {"type": "javascript", "expr": "flow_input.dir"}}}},
  {"id": "b", "value": {"type": "script", "path": "f/plugins/label", "input_transforms": {"label": {"type": "static", "value": "step"}}}}
]}}`,
	// One step.
	"f/broker/one": `{"summary": "one", "value": {"modules": [
  {"id": "a", "value": {"type": "script", "path": "f/plugins/label", "input_transforms": {"label": {"type": "static", "value": "one"}}}}
]}}`,
}

// brokerProviders are the named providers of the bundle, each answered by
// the hooks of the run that reaches it.
var brokerProviders = []string{"bp", "p_result", "p_decline", "p_throw", "p_slow"}

var (
	bundleOnce              sync.Once
	bundleOut, bundleRT     string
	bundleErr               error
	bundleTemp, bundleCases = "", (*conformance.PluginFile)(nil)
)

func TestMain(m *testing.M) {
	code := m.Run()
	if bundleTemp != "" {
		os.RemoveAll(bundleTemp)
	}
	os.Exit(code)
}

// testBun returns the Bun of WL_BUN; without it the test is skipped,
// unless WL_REQUIRE_BUN is set, as CI sets it.
func testBun(t *testing.T) string {
	t.Helper()
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set")
	}
	return bun
}

// brokerBundle builds, once, the bundle of the plugin cases and the broker
// tests' flows, and returns it with the cases and the harness runtime.
func brokerBundle(t *testing.T) (bun, out, runtimeDir string, cases *conformance.PluginFile) {
	t.Helper()
	bun = testBun(t)
	bundleOnce.Do(func() {
		repo, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			bundleErr = err
			return
		}
		f, err := conformance.LoadPluginFile(filepath.Join(repo, "conformance", "plugins", "hooks.yaml"))
		if err != nil {
			bundleErr = err
			return
		}
		bundleCases = f
		if bundleTemp, err = os.MkdirTemp("", "broker-test-"); err != nil {
			bundleErr = err
			return
		}
		folder := filepath.Join(bundleTemp, "folder")
		if err := f.WriteFolder(folder); err != nil {
			bundleErr = err
			return
		}
		files := map[string]string{}
		for p, src := range brokerScripts {
			files[p] = src
		}
		for p, src := range brokerFlows {
			files[p+".flow/flow.yaml"] = src
		}
		for p, src := range files {
			full := filepath.Join(folder, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				bundleErr = err
				return
			}
			if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
				bundleErr = err
				return
			}
		}
		var contract struct {
			Roots    []map[string]any `json:"roots"`
			Provided []map[string]any `json:"provided"`
		}
		if err := json.Unmarshal(f.Contract(), &contract); err != nil {
			bundleErr = err
			return
		}
		for p := range brokerFlows {
			contract.Roots = append(contract.Roots, map[string]any{"flow": p})
		}
		contract.Provided = append(contract.Provided,
			map[string]any{"selector": map[string]any{"flow": "f/broker/prov", "module": "a"}, "provider": "bp", "mandatory": true},
			map[string]any{"selector": map[string]any{"flow": "f/broker/holdprov", "module": "b"}, "provider": "bp", "mandatory": true})
		c, err := json.Marshal(contract)
		if err != nil {
			bundleErr = err
			return
		}
		bundleRT = filepath.Join(repo, "harnesses", "bun", "runtime")
		bundleOut = filepath.Join(bundleTemp, "bundle")
		_, bundleErr = bundle.Build(context.Background(), bundle.Config{
			Folder: folder, Contract: c, EngineVersion: wl.Version(), Bun: bun,
			Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
			HarnessRuntime: bundleRT, Out: bundleOut, Work: filepath.Join(bundleTemp, "work"),
		})
	})
	if bundleErr != nil {
		t.Fatal(bundleErr)
	}
	return bun, bundleOut, bundleRT, bundleCases
}

// engineConfig is how startEngine starts an engine.
type engineConfig struct {
	// plugin is the runtime's Options.Plugin; empty for none.
	plugin string
	// pluginTimeout is the runtime's plugin timeout and the Client's;
	// zero means a second.
	pluginTimeout time.Duration
	// lookupAfter, when set, is the Client's own plugin timeout, after
	// which it first asks GetRun.
	lookupAfter time.Duration
	// tokenBound is the Server's bound of gone tokens; zero, the default.
	tokenBound int
	// runner wraps the Gate's Runner as the connection serves it: a fake
	// transport of Runner calls.
	runner func(plugin.RunnerServer) plugin.RunnerServer
	// hooks wraps each HostHooks client that the Server dials: a fake
	// transport of the engine's hook and release calls.
	hooks func(plugin.HostHooksClient) plugin.HostHooksClient
	// providers are the runtime's own Go providers.
	providers map[string]wl.Provider
	// diagnostics receives the runtime's diagnostics.
	diagnostics io.Writer
	// pool is the runtime's pool.
	pool wl.PoolOptions
	// maxMessageBytes is the Server's message limit of the hooks' calls;
	// zero, the default.
	maxMessageBytes int
}

// testEngine is an engine in-process and the host's Client of it.
type testEngine struct {
	rt     *wl.Runtime
	srv    *Server
	gate   *Gate
	client *plugin.Client
	server *goplugin.GRPCServer
	// endGeneration ends the engine's generation as go-plugin does when
	// the engine's process exits: the connection's server stops, and the
	// context that go-plugin gave the Client ends.
	endGeneration func()
	// dispense dispenses another Client of the connection.
	dispense func() *plugin.Client
}

// enginePlugin is the Runner plugin of the test connection: on the
// engine's side it keeps go-plugin's broker and registers the Gate's
// Runner; on the host's side it dispenses a Client.
type enginePlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	runner plugin.RunnerServer
	broker atomic.Pointer[goplugin.GRPCBroker]
	client plugin.RunnerPlugin
	// generation is the context that the Client gets, as go-plugin's
	// own, which ends with the engine's process.
	generation context.Context
}

func (p *enginePlugin) GRPCServer(b *goplugin.GRPCBroker, s *grpc.Server) error {
	p.broker.Store(b)
	plugin.RegisterRunnerServer(s, p.runner)
	return nil
}

func (p *enginePlugin) GRPCClient(ctx context.Context, b *goplugin.GRPCBroker, cc *grpc.ClientConn) (any, error) {
	return p.client.GRPCClient(p.generation, b, cc)
}

// openRuntime opens the broker tests' bundle with cfg's plugin, timeout
// and providers, every named provider of brokerProviders answered by the
// run's hooks.
func openRuntime(t *testing.T, cfg engineConfig) (*wl.Runtime, error) {
	bun, out, runtimeDir, _ := brokerBundle(t)
	timeout := cfg.pluginTimeout
	if timeout == 0 {
		timeout = time.Second
	}
	opts := wl.Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: cfg.plugin, PluginTimeout: timeout, Diagnostics: cfg.diagnostics, Pool: cfg.pool}
	for name, p := range cfg.providers {
		if opts.Providers == nil {
			opts.Providers = map[string]wl.Provider{}
		}
		opts.Providers[name] = p
	}
	var names []string
	for _, n := range brokerProviders {
		if cfg.providers[n] == nil {
			names = append(names, n)
		}
	}
	wlext.BrokerProviders(&opts, names)
	return wl.Open(out, opts)
}

// startEngine starts an engine of cfg and connects its Client.
func startEngine(t *testing.T, cfg engineConfig) *testEngine {
	t.Helper()
	rt, err := openRuntime(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	timeout := cfg.pluginTimeout
	if timeout == 0 {
		timeout = time.Second
	}
	lookup := cfg.lookupAfter
	if lookup == 0 {
		lookup = timeout
	}
	e := &testEngine{rt: rt, gate: NewGate()}
	generation, end := context.WithCancel(context.Background())
	// Each lookup has a minute, so that a lookup that a test holds still
	// reaches its Client when the test lets it go.
	p := &enginePlugin{runner: e.gate.Runner(), client: plugin.RunnerPlugin{PluginTimeout: lookup, LookupTimeout: time.Minute}, generation: generation}
	if cfg.runner != nil {
		p.runner = cfg.runner(p.runner)
	}
	gc, gs := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{plugin.PluginName: p})
	e.server = gs
	// The connection's Close stops the server too, through go-plugin's
	// controller.
	t.Cleanup(func() {
		e.client.Close()
		gc.Close()
		end()
	})
	e.endGeneration = func() {
		gs.Stop()
		end()
	}
	e.srv = New(Config{Runtime: rt, Broker: p.broker.Load, PluginTimeout: timeout, MaxMessageBytes: cfg.maxMessageBytes, TokenBound: cfg.tokenBound, OnTokenBound: e.gate.Drain})
	e.srv.hooks.wrap = cfg.hooks
	e.gate.Serve(e.srv)
	e.dispense = func() *plugin.Client {
		raw, err := gc.Dispense(plugin.PluginName)
		if err != nil {
			t.Fatal(err)
		}
		c := raw.(*plugin.Client)
		t.Cleanup(c.Close)
		return c
	}
	e.client = e.dispense()
	return e
}

// call is one call of a run's hooks as the host heard it.
type call struct{ hook, site string }

func (c call) String() string { return c.hook + "@" + c.site }

// recorder is a HookHandler that records each call by hook and site and
// answers with answer, which may be nil for an empty answer.
type recorder struct {
	answer func(ctx context.Context, c *plugin.HookCall) (*plugin.HookReply, error)

	mu    sync.Mutex
	calls []call
	runs  map[string]bool
}

func (r *recorder) handle(ctx context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call{c.GetHook(), c.GetSite()})
	if r.runs == nil {
		r.runs = map[string]bool{}
	}
	r.runs[c.GetRun()] = true
	r.mu.Unlock()
	if r.answer == nil {
		return &plugin.HookReply{Payload: []byte(`{}`)}, nil
	}
	return r.answer(ctx, c)
}

// heard returns the calls that r recorded, in order.
func (r *recorder) heard() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// set returns the calls as a sorted set.
func set(cs []call) []string {
	m := map[string]bool{}
	for _, c := range cs {
		m[c.String()] = true
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// jsonOf marshals v, which must marshal.
func jsonOf(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sel is the JSON selector of module m of flow.
func sel(flow, m string) map[string]any { return map[string]any{"flow": flow, "module": m} }

// waitFor waits up to 30 seconds for cond, failing the test with what
// otherwise.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for until := time.Now().Add(30 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("%s: not within 30 seconds", what)
		}
	}
}

// caseHost is a case's declarative plugin as a run's hooks on the broker:
// its declaration, and the answers of the TypeScript case plugin
// (harnesses/bun/test/caseplugin.ts) for the case's flow. Its state is the
// run's count of state: true.
type caseHost struct {
	spec  conformance.PluginSpec
	rec   recorder
	mu    sync.Mutex
	log   []string
	state int
}

// declaration is the case's plugin as a host plugin's declaration: its
// interceptions and rewrites in order, its halts, write-capable modules,
// first-write verdict, fatal error names, observers, environment,
// variables and named providers, and onRunEnd, which every case's plugin
// answers.
func (h *caseHost) declaration(flow string) map[string]any {
	s := h.spec
	d := map[string]any{"on_run_end": true}
	var intercept, rewrite, halts, haltsAfter, writes []any
	for _, x := range s.Intercept {
		e := map[string]any{"selector": sel(flow, x.Module)}
		if x.Static != nil {
			e["static"] = x.Static
		}
		intercept = append(intercept, e)
	}
	for _, x := range s.RewriteArgs {
		rewrite = append(rewrite, map[string]any{"selector": sel(flow, x.Module)})
	}
	for _, m := range s.HaltBefore {
		halts = append(halts, sel(flow, m))
	}
	for _, m := range s.HaltAfter {
		haltsAfter = append(haltsAfter, sel(flow, m))
	}
	for _, m := range s.WriteCapable {
		writes = append(writes, sel(flow, m))
	}
	for k, v := range map[string][]any{"intercept": intercept, "rewrite_args": rewrite, "halt_before": halts, "halt_after": haltsAfter, "write_capable": writes} {
		if len(v) > 0 {
			d[k] = v
		}
	}
	if s.FirstWrite != "" {
		d["before_first_write"] = true
	}
	if len(s.FatalErrorNames) > 0 {
		d["fatal_error_names"] = s.FatalErrorNames
	}
	if s.Observe {
		d["on_module_start"], d["on_module_end"] = true, true
	}
	if s.State {
		d["on_module_start"] = true
	}
	if len(s.Env) > 0 {
		d["invocation_env"] = true
	}
	if len(s.Variables) > 0 {
		d["variable"] = true
	}
	if len(s.Providers) > 0 {
		names := make([]string, 0, len(s.Providers))
		for n := range s.Providers {
			names = append(names, n)
		}
		sort.Strings(names)
		d["providers"] = names
	}
	return d
}

// outcomeAnswer is an interception's or a provider's answer, as the
// TypeScript case plugin gives it: a throw answers a plugin error, a delay
// answers past it.
func outcomeAnswer(ctx context.Context, result, failed json.RawMessage, decline bool, throw string, delayMS int) (*plugin.HookReply, error) {
	if delayMS > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(delayMS) * time.Millisecond):
		}
	}
	switch {
	case throw != "":
		return nil, errors.New(throw)
	case decline:
		return &plugin.HookReply{Payload: []byte(`{"decline": true}`)}, nil
	case failed != nil:
		b, _ := json.Marshal(map[string]any{"result": failed, "failed": true})
		return &plugin.HookReply{Payload: b}, nil
	}
	if result == nil {
		result = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{"result": result})
	return &plugin.HookReply{Payload: b}, nil
}

// answer answers one call of the case's hooks.
func (h *caseHost) answer(ctx context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
	var p struct {
		Index int               `json:"index"`
		Name  string            `json:"name"`
		Path  string            `json:"path"`
		Env   map[string]string `json:"env"`
		Site  struct {
			Path string `json:"path"`
		} `json:"site"`
		Record struct {
			Status string `json:"status"`
		} `json:"record"`
	}
	if len(c.GetPayload()) > 0 {
		if err := json.Unmarshal(c.GetPayload(), &p); err != nil {
			return nil, err
		}
	}
	s := h.spec
	switch c.GetHook() {
	case "intercept":
		x := s.Intercept[p.Index]
		return outcomeAnswer(ctx, x.Result, x.Failed, x.Decline, x.Throw, 0)
	case "rewrite_args":
		b, _ := json.Marshal(map[string]any{"args": s.RewriteArgs[p.Index].Args})
		return &plugin.HookReply{Payload: b}, nil
	case "provide":
		a := s.Providers[p.Name]
		return outcomeAnswer(ctx, a.Result, a.Failed, a.Decline, a.Throw, a.DelayMS)
	case "before_first_write":
		if reason, ok := strings.CutPrefix(s.FirstWrite, "veto: "); ok {
			return nil, errors.New(reason)
		}
	case "on_module_start":
		h.mu.Lock()
		if s.Observe {
			h.log = append(h.log, "start:"+p.Site.Path)
		}
		if s.State {
			h.state++
			h.log = append(h.log, fmt.Sprintf("state:%s:%d", p.Site.Path, h.state))
		}
		h.mu.Unlock()
	case "on_module_end":
		h.mu.Lock()
		h.log = append(h.log, fmt.Sprintf("end:%s:%s", p.Site.Path, p.Record.Status))
		h.mu.Unlock()
	case "invocation_env":
		env := p.Env
		if env == nil {
			env = map[string]string{}
		}
		for k, v := range s.Env {
			env[k] = v
		}
		b, _ := json.Marshal(map[string]any{"env": env})
		return &plugin.HookReply{Payload: b}, nil
	case "variable":
		v, ok := s.Variables[p.Path]
		b, _ := json.Marshal(map[string]any{"found": ok, "value": v})
		return &plugin.HookReply{Payload: b}, nil
	case "on_run_end":
		h.mu.Lock()
		log, _ := json.Marshal(append([]string{}, h.log...))
		h.mu.Unlock()
		ws := []map[string]string{{"code": conformance.PluginLog, "message": string(log)}}
		for _, w := range s.Warnings {
			ws = append(ws, map[string]string{"code": w.Code, "message": w.Message})
		}
		b, _ := json.Marshal(map[string]any{"warnings": ws})
		return &plugin.HookReply{Payload: b}, nil
	}
	return &plugin.HookReply{Payload: []byte(`{}`)}, nil
}

// brokerExcluded are the cases that the broker does not run, and why.
var brokerExcluded = map[string]string{
	"around":       "around runs only in the harness process: TestBrokerAroundRefused",
	"around_throw": "around runs only in the harness process: TestBrokerAroundRefused",
	"step_latch":   "step code, no hook",
	"step_signal":  "step code, no hook",
}

// brokerRuns reports whether the broker runs case c: not one excluded, nor
// one built on the TypeScript plugin's own failure options (fail_variables,
// fail_resources, slow_start_ms), whose broker counterparts are
// TestBrokerHookTimeout and TestBrokerPhaseFailures.
func brokerRuns(c conformance.PluginCase) (bool, string) {
	if why, ok := brokerExcluded[c.ID]; ok {
		return false, why
	}
	s := c.Hooks
	if len(s.FailVariables) > 0 || len(s.FailResources) > 0 || s.SlowStartMS > 0 {
		return false, "a TypeScript-only failure option: TestBrokerHookTimeout and TestBrokerPhaseFailures"
	}
	return true, ""
}

// traceSteps reads a full trace's step records: each site with whether a
// job ran for it.
func traceSteps(t *testing.T, trace []byte) map[string]bool {
	t.Helper()
	var tr struct {
		Steps []struct {
			Site string `json:"site"`
			Ran  bool   `json:"ran"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(trace, &tr); err != nil {
		t.Fatalf("the run's trace: %v", err)
	}
	out := map[string]bool{}
	for _, s := range tr.Steps {
		out[s.Site] = out[s.Site] || s.Ran
	}
	return out
}

// expectedCalls computes the calls of a case's run from its fixture: each
// hook its plugin declares at a module that its run reached (steps: each
// site of the trace with whether a job ran for it), and onRunEnd.
// Interceptions without a static answer, rewrites and named providers are
// called at a module the run reached; observers and the environment at a
// module whose job ran; the variable provider when its flow ran a job; the
// first-write verdict at the first write-capable module that the run
// reached and did not halt before.
func expectedCalls(c conformance.PluginCase, r conformance.PluginRun, steps map[string]bool) []string {
	s := c.Hooks
	site := func(m string) string { return "m:" + m }
	var out []call
	reached := func(m string) bool { _, ok := steps[site(m)]; return ok }
	for _, x := range s.Intercept {
		if x.Static == nil && reached(x.Module) {
			out = append(out, call{"intercept", site(x.Module)})
		}
	}
	for _, x := range s.RewriteArgs {
		if reached(x.Module) {
			out = append(out, call{"rewrite_args", site(x.Module)})
		}
	}
	for _, p := range c.Provided {
		if _, ok := s.Providers[p.Provider]; ok && reached(p.Module) {
			out = append(out, call{"provide", site(p.Module)})
		}
	}
	ran := false
	for st, jobRan := range steps {
		if !jobRan {
			continue
		}
		ran = true
		if s.Observe {
			out = append(out, call{"on_module_start", st}, call{"on_module_end", st})
		}
		if s.State {
			out = append(out, call{"on_module_start", st})
		}
		if len(s.Env) > 0 {
			out = append(out, call{"invocation_env", st})
		}
	}
	if len(s.Variables) > 0 && ran {
		out = append(out, call{"variable", ""})
	}
	if s.FirstWrite != "" {
		halted := append(slices.Clone(s.HaltBefore), r.HaltBefore...)
		for _, m := range flowModules(c) {
			writes := slices.Contains(s.WriteCapable, m) || slices.Contains(r.WriteCapable, m)
			if writes && reached(m) && !slices.Contains(halted, m) {
				out = append(out, call{"before_first_write", site(m)})
				break
			}
		}
	}
	out = append(out, call{"on_run_end", ""})
	return set(out)
}

// flowModules are the ids of a case flow's top-level modules, in order.
func flowModules(c conformance.PluginCase) []string {
	var f struct {
		Modules []struct {
			ID string `json:"id"`
		} `json:"modules"`
	}
	_ = json.Unmarshal(c.Flow, &f)
	var out []string
	for _, m := range f.Modules {
		out = append(out, m.ID)
	}
	return out
}

// caseRequest is one of a case's runs as a RunRequest, with the full trace.
func caseRequest(t *testing.T, c conformance.PluginCase, r conformance.PluginRun) *plugin.RunRequest {
	flow := c.FlowPath()
	sels := func(ms []string) []byte {
		if len(ms) == 0 {
			return nil
		}
		var out []any
		for _, m := range ms {
			out = append(out, sel(flow, m))
		}
		return jsonOf(t, out)
	}
	req := &plugin.RunRequest{
		Path: flow, Args: []byte(`{}`), Trace: "full",
		WriteCapable: sels(r.WriteCapable), HaltBefore: sels(r.HaltBefore), HaltAfter: sels(r.HaltAfter),
		FatalErrorNames: r.FatalErrorNames,
	}
	if r.DeadlineMS != nil {
		ms := int64(*r.DeadlineMS)
		req.DeadlineMs = &ms
	}
	if r.PreWriteDeadlineMS != nil {
		ms := int64(*r.PreWriteDeadlineMS)
		req.PreWriteDeadlineMs = &ms
	}
	if r.Clock == "record" {
		req.Clock = "record"
	}
	return req
}

// outcomeOf is a RunResult as a case checks it.
func outcomeOf(t *testing.T, res *plugin.RunResult, steps map[string]bool) conformance.PluginOutcome {
	t.Helper()
	o := conformance.PluginOutcome{Status: res.GetStatus(), MayHaveMutated: res.GetMayHaveMutated(), Result: res.GetResult(), Latched: res.GetLatched(), Ran: steps, CancelCause: res.GetCancelCause(), Deviations: res.GetDeviations()}
	if len(res.GetWarnings()) > 0 {
		var ws []struct{ Code, Message string }
		if err := json.Unmarshal(res.GetWarnings(), &ws); err != nil {
			t.Fatalf("the run's warnings: %v", err)
		}
		o.Warnings = ws
	}
	return o
}

// TestBrokerCases runs the plugin cases (conformance/plugins/hooks.yaml)
// through the broker: each run's hooks are its case's plugin, declared in
// RunRequest.hooks and answered by the host's Client over go-plugin's
// broker. Each run must meet its case's expectations, which a go: false
// case takes from the TypeScript plugin's run, and the host must hear
// exactly the calls that the case's fixture gives (expectedCalls): no
// call of the run's hooks is lost, none is extra. A case with a pool of
// its own runs on an engine with that pool.
func TestBrokerCases(t *testing.T) {
	_, _, _, f := brokerBundle(t)
	timeout := time.Duration(f.TimeoutMS) * time.Millisecond
	shared := startEngine(t, engineConfig{pluginTimeout: timeout})
	for _, c := range f.Cases {
		t.Run(c.ID, func(t *testing.T) {
			if ok, why := brokerRuns(c); !ok {
				t.Skip("not through the broker: " + why)
			}
			e := shared
			if p := c.Pool; p != nil {
				e = startEngine(t, engineConfig{pluginTimeout: timeout, pool: wl.PoolOptions{Max: p.Max, LagThreshold: time.Duration(p.LagThresholdMS) * time.Millisecond, PinRuns: p.PinRuns}})
			}
			for i, r := range append([]conformance.PluginThen{{Run: c.Run, Expect: c.Expect}}, c.Then...) {
				h := &caseHost{spec: c.Hooks}
				h.rec.answer = h.answer
				res, err := e.client.RunWithHooks(context.Background(), caseRequest(t, c, r.Run), jsonOf(t, h.declaration(c.FlowPath())), h.rec.handle)
				if err != nil {
					t.Fatalf("run %d: %v", i, err)
				}
				steps := traceSteps(t, res.GetTrace())
				if d := r.Expect.Check(outcomeOf(t, res, steps)); len(d) > 0 {
					t.Errorf("%s, run %d:\n  %s", c.Title, i, strings.Join(d, "\n  "))
				}
				if got, want := set(h.rec.heard()), expectedCalls(c, r.Run, steps); !slices.Equal(got, want) {
					t.Errorf("run %d: the host heard %v, want %v", i, got, want)
				}
			}
		})
	}
}

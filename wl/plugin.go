package wl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonlib"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// Host plugins (ADR 0008, milestone F2c): hooks written in TypeScript and
// run in the Bun harness, which the engine calls over host_call frames
// (ADR 0004). A plugin declares its hooks and their selectors up front, in
// its harness's hello; the runtime turns that declaration into the same
// Hooks and Providers a Go host passes (F2a), whose callbacks send a
// host_call and wait for its reply. So both kinds of host take one path
// through the engine's seams, and selectors match in Go only. In host
// mode the plugin is the host's (the host SDK's connect option plugin);
// in a warm runtime it is the module Options.Plugin names, which every
// harness process loads.

// DefaultPluginTimeout is how long a host_call waits for its reply unless
// Options.PluginTimeout says otherwise.
const DefaultPluginTimeout = 60 * time.Second

// ErrHostCallTimeout marks a host_call that got no reply within the
// plugin timeout: the hook's error, which latches the run (H5), or fails
// it closed at a host-provided site.
var ErrHostCallTimeout = errors.New("host_call_timeout")

// HostCallError is the error a host plugin answered a hook with: its hook
// threw or answered what the hook does not answer (code plugin_error), or
// it does not declare the hook (unknown_hook). It latches the run.
type HostCallError = harness.HostCallError

// pluginDecl is a host plugin's declaration, the hello's plugin field.
type pluginDecl struct {
	Intercept []struct {
		Selector Selector        `json:"selector"`
		Static   json.RawMessage `json:"static"`
	} `json:"intercept"`
	RewriteArgs []struct {
		Selector Selector `json:"selector"`
	} `json:"rewrite_args"`
	HaltBefore []Selector `json:"halt_before"`
	HaltAfter  []Selector `json:"halt_after"`
	Around     []struct {
		Selector Selector `json:"selector"`
	} `json:"around"`
	WriteCapable     []Selector `json:"write_capable"`
	BeforeFirstWrite bool       `json:"before_first_write"`
	FatalErrorNames  []string   `json:"fatal_error_names"`
	OnModuleStart    bool       `json:"on_module_start"`
	OnModuleEnd      bool       `json:"on_module_end"`
	InvocationEnv    bool       `json:"invocation_env"`
	OnRunEnd         bool       `json:"on_run_end"`
	OnRunSettled     bool       `json:"on_run_settled"`
	Providers        []string   `json:"providers"`
	Resource         bool       `json:"resource"`
	Variable         bool       `json:"variable"`
}

// hostCaller sends one host_call: the pool's or the host's.
type hostCaller func(ctx context.Context, hook, run, site string, payload jsonval.Value) (jsonval.Value, error)

// plugin is a host plugin as the runtime calls it.
type plugin struct {
	decl    pluginDecl
	call    hostCaller
	timeout time.Duration
	diag    func(string)
	// hooks and providers are the declaration as Go callbacks.
	hooks     *Hooks
	providers map[string]Provider
}

// newPlugin decodes a declaration and checks it against the program:
// every selector well formed and unambiguous (ADR 0008), and every static
// interception JSON.
func newPlugin(decl jsonval.Value, prog *ir.Program, call hostCaller, timeout time.Duration, diag func(string)) (*plugin, error) {
	p := &plugin{call: call, timeout: timeout, diag: diag}
	if p.timeout <= 0 {
		p.timeout = DefaultPluginTimeout
	}
	if err := json.Unmarshal(decl.Producer(), &p.decl); err != nil {
		return nil, fmt.Errorf("wl: the host plugin's declaration: %w", err)
	}
	check := func(where string, s Selector) error {
		if err := checkSelectorIn(s, prog); err != nil {
			return fmt.Errorf("the host plugin's %s: %w", where, err)
		}
		return nil
	}
	for i, x := range p.decl.Intercept {
		if err := check(fmt.Sprintf("intercept[%d]", i), x.Selector); err != nil {
			return nil, err
		}
		if x.Static != nil && !json.Valid(x.Static) {
			return nil, fmt.Errorf("the host plugin's intercept[%d]: a static value that is not JSON", i)
		}
	}
	for i, x := range p.decl.RewriteArgs {
		if err := check(fmt.Sprintf("rewriteArgs[%d]", i), x.Selector); err != nil {
			return nil, err
		}
	}
	for i, s := range p.decl.HaltBefore {
		if err := check(fmt.Sprintf("haltBefore[%d]", i), s); err != nil {
			return nil, err
		}
	}
	for i, s := range p.decl.HaltAfter {
		if err := check(fmt.Sprintf("haltAfter[%d]", i), s); err != nil {
			return nil, err
		}
	}
	for i, x := range p.decl.Around {
		if err := check(fmt.Sprintf("around[%d]", i), x.Selector); err != nil {
			return nil, err
		}
	}
	for i, s := range p.decl.WriteCapable {
		if err := check(fmt.Sprintf("writeCapable[%d]", i), s); err != nil {
			return nil, err
		}
	}
	p.hooks = p.goHooks()
	p.providers = map[string]Provider{}
	for _, name := range p.decl.Providers {
		p.providers[name] = p.provider(name)
	}
	return p, nil
}

// do sends one host_call for run, at site when it is not nil, with
// payload, and decodes its answer into out. It waits no longer than the
// plugin timeout: then it fails with ErrHostCallTimeout. When the run's
// own context ends first, it returns that context's cause, which the
// engine does not latch on. The call holds the settlement of the run whose
// context ctx is until the hook has finished (doHeld).
func (p *plugin) do(ctx context.Context, hook, run string, site *engine.Site, payload map[string]any, out any) error {
	return p.doHeld(ctx, ctx, hook, run, site, payload, out)
}

// errRunSettled is the error of a hook that was not called because its run
// had settled.
var errRunSettled = errors.New("the run has settled")

// doHeld is do, holding the settlement of the run that held is the
// context of (engine.HoldSettlement) from before the host_call is sent
// until the hook has finished: at its reply, or, when the engine stops
// waiting first, at its late reply or the harness's exit. A run therefore
// never settles, and its resources are never released (ADR 0004 §Host
// plugins, ADR 0008 H9), while one of its hooks runs, whatever ends the
// wait: the plugin timeout, the run's own end, or a step that settled
// meanwhile, as one whose job-API read wl.signal() aborted does. A hook of
// a run that has settled is not called. A context of no run, the
// settlement callback's own (onRunSettled), holds nothing.
func (p *plugin) doHeld(ctx, held context.Context, hook, run string, site *engine.Site, payload map[string]any, out any) error {
	path := ""
	if site != nil {
		path = site.Path.String()
		payload["site"] = siteJSON(*site)
	}
	b, err := jsonlib.Marshal(payload)
	if err != nil {
		return fmt.Errorf("wl: host_call %s: %w", hook, err)
	}
	release, ok := engine.HoldSettlement(held)
	if !ok {
		return fmt.Errorf("wl: host_call %s for run %s: %w", hook, run, errRunSettled)
	}
	timeout := fmt.Errorf("%w: hook %s at %q got no reply from the host plugin within %s", ErrHostCallTimeout, hook, path, p.timeout)
	cctx, cancel := context.WithTimeoutCause(ctx, p.timeout, timeout)
	defer cancel()
	ans, err := p.call(cctx, hook, run, path, jsonval.FromProducer(b))
	if late := (*harness.LateHostCallError)(nil); errors.As(err, &late) {
		// The hook runs on: the hold passes to its end.
		go func() {
			<-late.Done
			release()
		}()
	} else {
		release()
	}
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return err
	}
	if out == nil {
		return nil
	}
	if err := jsonlib.Unmarshal(ans.Producer(), out); err != nil {
		return fmt.Errorf("wl: the host plugin's answer to %s: %w", hook, err)
	}
	return nil
}

// siteJSON is a site as the plugin's hooks see it.
func siteJSON(s engine.Site) map[string]any {
	return map[string]any{"path": s.Path.String(), "flow": s.Flow, "module": s.Module, "script": s.Script}
}

// outcomeReply is what an interception or a provider answers.
type outcomeReply struct {
	Decline bool            `json:"decline"`
	Result  json.RawMessage `json:"result"`
	Failed  bool            `json:"failed"`
}

func (r outcomeReply) outcome() (Outcome, bool) {
	if r.Decline {
		return Outcome{}, false
	}
	return Outcome{Result: r.Result, Failed: r.Failed}, true
}

// goHooks are the declaration as Go hooks, each callback a host_call.
func (p *plugin) goHooks() *Hooks {
	h := &Hooks{HaltBefore: p.decl.HaltBefore, HaltAfter: p.decl.HaltAfter, WriteCapable: p.decl.WriteCapable, FatalErrorNames: p.decl.FatalErrorNames}
	if p.decl.BeforeFirstWrite {
		// H3: an error from the plugin vetoes the first write.
		h.BeforeFirstWrite = func(ctx context.Context, run RunHandle, site Site) error {
			return p.do(ctx, "before_first_write", run.ID(), &site, map[string]any{}, nil)
		}
	}
	for i, x := range p.decl.Intercept {
		in := Interceptor{Selector: x.Selector}
		if x.Static != nil {
			in.Static = x.Static
		} else {
			in.Func = func(ctx context.Context, c Call) (Outcome, bool, error) {
				var r outcomeReply
				if err := p.do(ctx, "intercept", c.Run.ID(), &c.Site, map[string]any{"index": i, "args": c.Args}, &r); err != nil {
					return Outcome{}, false, err
				}
				o, ok := r.outcome()
				return o, ok, nil
			}
		}
		h.Intercept = append(h.Intercept, in)
	}
	for i, x := range p.decl.RewriteArgs {
		h.RewriteArgs = append(h.RewriteArgs, ArgRewriter{Selector: x.Selector, Func: func(ctx context.Context, c Call) (json.RawMessage, error) {
			var r struct {
				Args json.RawMessage `json:"args"`
			}
			if err := p.do(ctx, "rewrite_args", c.Run.ID(), &c.Site, map[string]any{"index": i, "args": c.Args}, &r); err != nil {
				return nil, err
			}
			return r.Args, nil
		}})
	}
	if p.decl.OnModuleStart {
		h.OnModuleStart = func(ctx context.Context, run RunHandle, site Site) error {
			return p.do(ctx, "on_module_start", run.ID(), &site, map[string]any{}, nil)
		}
	}
	if p.decl.OnModuleEnd {
		h.OnModuleEnd = func(ctx context.Context, run RunHandle, site Site, rec StepRecord) error {
			return p.do(ctx, "on_module_end", run.ID(), &site, map[string]any{"record": recordJSON(rec)}, nil)
		}
	}
	if p.decl.InvocationEnv {
		h.InvocationEnv = func(ctx context.Context, run RunHandle, site Site, env map[string]string) error {
			var r struct {
				Env map[string]string `json:"env"`
			}
			if err := p.do(ctx, "invocation_env", run.ID(), &site, map[string]any{"env": env}, &r); err != nil {
				return err
			}
			if r.Env == nil {
				return errors.New("wl: the host plugin's invocationEnv answered no environment")
			}
			maps.DeleteFunc(env, func(k, _ string) bool { _, keep := r.Env[k]; return !keep })
			maps.Copy(env, r.Env)
			return nil
		}
	}
	if p.decl.OnRunEnd {
		h.OnRunEnd = func(ctx context.Context, t *Trace) []Warning {
			var r struct {
				Warnings []struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"warnings"`
			}
			// A run its context ended, canceled or past a deadline, still
			// reports its end: the call has the plugin timeout alone.
			if err := p.do(context.WithoutCancel(ctx), "on_run_end", t.RunID, nil, map[string]any{"trace": traceJSON(t)}, &r); err != nil {
				// The run's outcome is decided: a failed onRunEnd can only
				// say so.
				return []Warning{{Code: harness.HostErrorPlugin, Message: "the host plugin's onRunEnd: " + err.Error()}}
			}
			ws := make([]Warning, 0, len(r.Warnings))
			for _, w := range r.Warnings {
				ws = append(ws, Warning{Code: w.Code, Message: w.Message})
			}
			return ws
		}
	}
	if p.decl.OnRunSettled {
		h.OnRunSettled = func(t *Trace) {
			if err := p.do(context.Background(), "on_run_settled", t.RunID, nil, map[string]any{"trace": traceJSON(t)}, nil); err != nil {
				p.diag(fmt.Sprintf("windmill-lite: run %s: the host plugin's onRunSettled: %v", t.RunID, err))
			}
		}
	}
	return h
}

// provider is the named provider of a host-provided site, answered by the
// plugin.
func (p *plugin) provider(name string) Provider {
	return func(ctx context.Context, c Call) (Outcome, bool, error) {
		var r outcomeReply
		if err := p.do(ctx, "provide", c.Run.ID(), &c.Site, map[string]any{"name": name, "args": c.Args}, &r); err != nil {
			return Outcome{}, false, err
		}
		o, ok := r.outcome()
		return o, ok, nil
	}
}

// around gives the plugin's around wrappers whose selectors match a site,
// by index, in declaration order, the first outermost.
func (p *plugin) around(s engine.Site) []int {
	var out []int
	for i, x := range p.decl.Around {
		if matches(x.Selector, s) {
			out = append(out, i)
		}
	}
	return out
}

// platform are the plugin's resources and variables for one run (H13, the
// provider bridge of ADR 0007), or nil when it declares neither: raw
// values the engine's router interpolates, resolves and answers with.
// Their calls come from the run's argument resolution and expressions,
// and from its steps' job-API requests, which the facade serves outside
// the run's context, so b, bound when the run starts, is how they reach
// the run.
func (p *plugin) platform(b *runBinding) (platform.Resources, platform.Variables) {
	var res platform.Resources
	var vars platform.Variables
	if p.decl.Resource {
		res = pluginResources{p, b}
	}
	if p.decl.Variable {
		vars = pluginVariables{p, b}
	}
	return res, vars
}

// runBinding is a run as what serves it outside its hooks knows it, once
// the engine has started it (engine.RunRequest.Started): its context,
// which holds its settlement and ends when its outcome is decided, and its
// handle, which latches it.
type runBinding struct {
	id  string
	mu  sync.Mutex
	ctx context.Context
	run engine.Run
}

func (b *runBinding) started(ctx context.Context, r engine.Run) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ctx, b.run = ctx, r
}

// call is the plugin's host_call for the run, holding its settlement while
// a hook the engine stopped waiting for runs on. Its error latches the run,
// as a hook's does (H5, ADR 0004 §Host plugins): a provider's failure or
// timeout is never an error the run's steps or its continue_on_error can
// tolerate. A call that ended with ctx, or once the run's outcome was
// decided, latches nothing.
func (b *runBinding) call(ctx context.Context, p *plugin, hook, path string, out any) error {
	b.mu.Lock()
	held, run := b.ctx, b.run
	b.mu.Unlock()
	if held == nil {
		held = ctx
	}
	err := p.doHeld(ctx, held, hook, b.id, nil, map[string]any{"path": path}, out)
	if err != nil && run != nil && held.Err() == nil && engine.HookLatchError(ctx, err) != nil {
		run.Latch(err)
	}
	return err
}

type pluginResources struct {
	p *plugin
	b *runBinding
}

func (r pluginResources) Resource(ctx context.Context, path string) (platform.Resource, bool, error) {
	var a struct {
		Found bool            `json:"found"`
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := r.b.call(ctx, r.p, "resource", path, &a); err != nil || !a.Found {
		return platform.Resource{}, false, err
	}
	return platform.Resource{Type: a.Type, Value: a.Value}, true, nil
}

type pluginVariables struct {
	p *plugin
	b *runBinding
}

func (v pluginVariables) Variable(ctx context.Context, path string) (platform.Variable, bool, error) {
	var a struct {
		Found  bool   `json:"found"`
		Value  string `json:"value"`
		Secret bool   `json:"secret"`
	}
	if err := v.b.call(ctx, v.p, "variable", path, &a); err != nil || !a.Found {
		return platform.Variable{}, false, err
	}
	return platform.Variable{Value: a.Value, IsSecret: a.Secret}, true, nil
}

// recordJSON is a site's record as the plugin's hooks see it (H6, H9):
// every field of the Go StepRecord, in snake_case, so that a TypeScript
// hook has the evidence a Go hook has. Arguments and results are in stored
// form; Start is RFC 3339 and Duration is in milliseconds.
func recordJSON(rec StepRecord) map[string]any {
	m := map[string]any{
		"site": rec.Site.String(), "attempt": rec.Attempt, "status": rec.Status, "ran": rec.Ran, "skipped": rec.Skipped,
		"abandoned": rec.Abandoned, "cached": rec.Cached, "hooks": append([]string{}, rec.Hooks...),
		"failed_retries": sitesJSON(rec.FailedRetries), "duration_ms": msJSON(rec.Duration),
	}
	for k, v := range map[string]string{"job": rec.Job, "module": string(rec.Module), "result_origin": string(rec.ResultOrigin), "carried_origin": string(rec.CarriedOrigin)} {
		if v != "" {
			m[k] = v
		}
	}
	for k, v := range map[string]jsonval.Value{"args_before_rewrite": rec.ArgsBeforeRewrite, "args": rec.RecordedArgs, "resolved_args": rec.ResolvedArgs, "result": rec.Result, "carried": rec.Carried} {
		if t := storedText(v); t != nil {
			m[k] = t
		}
	}
	if len(rec.ResultNested) > 0 {
		m["result_nested"] = rec.ResultNested
	}
	if len(rec.CarriedNested) > 0 {
		m["carried_nested"] = rec.CarriedNested
	}
	if rec.BranchChosen != nil {
		m["branch_chosen"] = rec.BranchChosen
	}
	if !rec.Start.IsZero() {
		m["start"] = rec.Start.UTC().Format(time.RFC3339Nano)
	}
	late := []any{}
	for _, l := range rec.Late {
		e := map[string]any{"kind": l.Kind, "final": l.Final}
		if t := storedText(l.Body); t != nil {
			e["body"] = t
		}
		late = append(late, e)
	}
	m["late"] = late
	return m
}

// traceJSON is a run's trace as onRunEnd and onRunSettled see it (H9):
// every field of the Go Trace, in snake_case, with its records
// (recordJSON), the child flows, timings, deviations and the platform
// calls its steps made (ADR 0007). Superseded records and child flows are
// left out, as Trace.Step leaves them out.
func traceJSON(t *Trace) map[string]any {
	steps := []any{}
	for _, s := range t.Steps {
		if !s.Superseded {
			steps = append(steps, recordJSON(s))
		}
	}
	ws := []any{}
	for _, w := range t.Warnings {
		ws = append(ws, map[string]string{"code": w.Code, "message": w.Message})
	}
	children := []any{}
	for _, c := range t.Children {
		if !c.Superseded {
			children = append(children, map[string]any{"site": c.Site.String(), "id": c.ID, "modules": append([]string{}, c.Modules...), "virtual": append([]bool{}, c.Virtual...), "failure": c.Failure})
		}
	}
	spans := []any{}
	for _, sp := range t.Spans {
		spans = append(spans, map[string]any{"site": sp.Site.String(), "kind": sp.Kind, "start": sp.Start.UTC().Format(time.RFC3339Nano), "duration_ms": msJSON(sp.Duration)})
	}
	calls := []any{}
	for _, c := range t.PlatformCalls {
		e := map[string]any{"site": c.Site.String(), "job": c.Job, "method": c.Method, "path": c.Path, "query": c.Query, "route": c.Route, "status": c.Status, "unsupported": c.Unsupported}
		if c.Message != "" {
			e["message"] = c.Message
		}
		calls = append(calls, e)
	}
	m := map[string]any{
		"run": t.RunID, "bundle": t.Bundle, "module_state": t.ModuleState, "shared_code": t.SharedCode, "status": t.Status, "may_have_mutated": t.MayHaveMutated, "cached": t.Cached,
		"steps": steps, "children": children, "warnings": ws, "deviations": append([]string{}, t.Deviations...), "spans": spans, "platform_calls": calls,
	}
	if r := storedText(t.Result); r != nil {
		m["result"] = r
	}
	if t.ResultOrigin != "" {
		m["result_origin"] = t.ResultOrigin
	}
	if len(t.ResultNested) > 0 {
		m["result_nested"] = t.ResultNested
	}
	if t.Latched != nil {
		m["latched"] = t.Latched.Error()
	}
	return m
}

// sitesJSON is a list of site paths as text.
func sitesJSON(ps []conformance.SitePath) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// msJSON is a duration in milliseconds.
func msJSON(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// withPlugin are the hooks of a run: the run's own, and the plugin's after
// them, or either alone (mergeHooks).
func (rt *Runtime) withPlugin(h *Hooks) *Hooks {
	p := rt.plugin.Load()
	if p == nil {
		return h
	}
	if h == nil {
		return p.hooks
	}
	return mergeHooks(h, p.hooks)
}

// mergeHooks are a's hooks followed by b's: interceptions, rewrites, halts,
// write-capable selectors and fatal names in that order (the first
// matching interception answers), and both observers, first-write
// signals (a veto from either vetoes), environment hooks and run-end
// hooks, a's first.
func mergeHooks(a, b *Hooks) *Hooks {
	m := &Hooks{
		Intercept:       slices.Concat(a.Intercept, b.Intercept),
		RewriteArgs:     slices.Concat(a.RewriteArgs, b.RewriteArgs),
		HaltBefore:      slices.Concat(a.HaltBefore, b.HaltBefore),
		HaltAfter:       slices.Concat(a.HaltAfter, b.HaltAfter),
		WriteCapable:    slices.Concat(a.WriteCapable, b.WriteCapable),
		FatalErrorNames: slices.Concat(a.FatalErrorNames, b.FatalErrorNames),
	}
	m.OnModuleStart = both(a.OnModuleStart, b.OnModuleStart)
	m.BeforeFirstWrite = both(a.BeforeFirstWrite, b.BeforeFirstWrite)
	if a.OnModuleEnd != nil || b.OnModuleEnd != nil {
		m.OnModuleEnd = func(ctx context.Context, r RunHandle, s Site, rec StepRecord) error {
			for _, f := range []func(context.Context, RunHandle, Site, StepRecord) error{a.OnModuleEnd, b.OnModuleEnd} {
				if f != nil {
					if err := f(ctx, r, s, rec); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	if a.InvocationEnv != nil || b.InvocationEnv != nil {
		m.InvocationEnv = func(ctx context.Context, r RunHandle, s Site, env map[string]string) error {
			for _, f := range []func(context.Context, RunHandle, Site, map[string]string) error{a.InvocationEnv, b.InvocationEnv} {
				if f != nil {
					if err := f(ctx, r, s, env); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	if a.OnRunEnd != nil || b.OnRunEnd != nil {
		m.OnRunEnd = func(ctx context.Context, t *Trace) []Warning {
			var ws []Warning
			for _, f := range []func(context.Context, *Trace) []Warning{a.OnRunEnd, b.OnRunEnd} {
				if f != nil {
					ws = append(ws, f(ctx, t)...)
				}
			}
			return ws
		}
	}
	if a.OnRunSettled != nil || b.OnRunSettled != nil {
		m.OnRunSettled = func(t *Trace) {
			for _, f := range []func(*Trace){a.OnRunSettled, b.OnRunSettled} {
				if f != nil {
					f(t)
				}
			}
		}
	}
	return m
}

// both chains two observers of one shape, a's first, stopping at an error.
func both(a, b func(context.Context, RunHandle, Site) error) func(context.Context, RunHandle, Site) error {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return func(ctx context.Context, r RunHandle, s Site) error {
		if err := a(ctx, r, s); err != nil {
			return err
		}
		return b(ctx, r, s)
	}
}

// pluginServices are the services of a run whose plugin answers resources
// or variables (H13): the run's own providers first (RunOptions.Platform),
// then the plugin's, then the runtime's. own is the run's own store, or
// nil.
func (rt *Runtime) pluginServices(run *runBinding, own *platform.Store, resolvers []platform.ValueResolver) *platform.Services {
	p := rt.plugin.Load()
	var res platform.ResourceChain
	var vars platform.VariableChain
	if own != nil {
		res, vars = append(res, own), append(vars, own)
	}
	if p != nil {
		pr, pv := p.platform(run)
		if pr != nil {
			res = append(res, pr)
		}
		if pv != nil {
			vars = append(vars, pv)
		}
	}
	res, vars = append(res, rt.store), append(vars, rt.store)
	return rt.services.WithProviders(platform.Providers{Resources: res, Variables: vars, Resolvers: resolvers})
}

// answersPlatform reports whether the runtime's plugin answers resources
// or variables (H13).
func (rt *Runtime) answersPlatform() bool {
	p := rt.plugin.Load()
	return p != nil && (p.decl.Resource || p.decl.Variable)
}

// usePlugin installs the plugin the runtime's harness declared, after
// checking that every provider the host contract names is a Go provider or
// the plugin's (missing_provider), and that none is both.
func (rt *Runtime) usePlugin(decl jsonval.Value, call hostCaller, timeout time.Duration, diag func(string)) error {
	p, err := newPlugin(decl, rt.prog, call, timeout, diag)
	if err != nil {
		return err
	}
	all := maps.Clone(rt.goProviders)
	if all == nil {
		all = map[string]Provider{}
	}
	for name, f := range p.providers {
		if _, dup := all[name]; dup {
			return fmt.Errorf("wl: the provider %q is both in Options.Providers and the host plugin's", name)
		}
		all[name] = f
	}
	if err := checkProviders(rt.prog, all); err != nil {
		return err
	}
	rt.providers.Store(&all)
	rt.plugin.Store(p)
	return nil
}

// provide is the engine's Config.Provide: the runtime's providers, the Go
// host's and its plugin's, which a host-mode runtime knows only once its
// host's hello has declared them.
func (rt *Runtime) provide(ctx context.Context, r engine.Run, s engine.Site, name string, args jsonval.Value) (engine.StepOutcome, bool, error) {
	var providers map[string]Provider
	if m := rt.providers.Load(); m != nil {
		providers = *m
	}
	return provide(providers)(ctx, r, s, name, args)
}

// aroundAt is the engine's RunRequest.Around for the runtime's plugin, or
// nil when it has no around wrapper.
func (rt *Runtime) aroundAt() func(engine.Site) []int {
	p := rt.plugin.Load()
	if p == nil || len(p.decl.Around) == 0 {
		return nil
	}
	return p.around
}

// pluginArgs are the harness arguments that load the plugin module path.
func pluginArgs(path string) ([]string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return []string{"--plugin", abs}, nil
}

// loadPlugin reads the declaration of the plugin Options.Plugin names from
// the pool's first harness process, which it starts, and installs it.
func (rt *Runtime) loadPlugin(opts Options) error {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	decl, err := rt.pool.Plugin(ctx)
	if err != nil {
		return fmt.Errorf("wl: starting the harness for the host plugin: %w", err)
	}
	if decl.IsZero() {
		return fmt.Errorf("wl: the harness loaded no host plugin from %s", opts.Plugin)
	}
	return rt.usePlugin(decl, rt.pool.HostCall, opts.PluginTimeout, rt.diagLine(opts.Diagnostics))
}

// hostCheck is host mode's check of the host's hello (F2c): the plugin it
// declares, or, when it declares none, the Go providers alone, must cover
// the host contract's named providers.
func (rt *Runtime) hostCheck(opts Options) func(h harness.Hello) *harness.Reject {
	return func(h harness.Hello) *harness.Reject {
		var err error
		if h.Plugin.IsZero() {
			err = checkProviders(rt.prog, rt.goProviders)
		} else {
			call := func(ctx context.Context, hook, run, site string, payload jsonval.Value) (jsonval.Value, error) {
				p := rt.hostProc.Load()
				if p == nil {
					return jsonval.Value{}, errors.New("wl: the host is not connected")
				}
				return p.HostCall(ctx, hook, run, site, payload)
			}
			err = rt.usePlugin(h.Plugin, call, opts.PluginTimeout, rt.diagLine(opts.Diagnostics))
		}
		if err != nil {
			return &harness.Reject{Code: harness.RejectPlugin, Message: err.Error()}
		}
		return nil
	}
}

// diagLine writes one diagnostic line to w, or os.Stderr.
func (rt *Runtime) diagLine(w io.Writer) func(string) {
	if w == nil {
		w = os.Stderr
	}
	return func(line string) { fmt.Fprintln(w, line) }
}

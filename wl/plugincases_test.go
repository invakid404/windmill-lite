package wl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/conformance"
)

// The plugin cases of conformance/plugins/hooks.yaml (milestone F2c), run
// through a TypeScript host plugin in warm harness processes
// (Options.Plugin, over host_call), and through Go callbacks built from the
// same declarative plugins (F2a's path): every case must give its expected
// outcome both ways. cmd/windmill-lite runs them in host mode. They need
// the pinned Bun (WL_BUN) and the build helper's dependencies.

func loadPluginCases(t *testing.T) *conformance.PluginFile {
	t.Helper()
	f, err := conformance.LoadPluginFile("../conformance/plugins/hooks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// buildPluginBundle builds the cases' folder and contract into a bundle.
func buildPluginBundle(t *testing.T, bun string, f *conformance.PluginFile) (out, runtimeDir string) {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	if err := f.WriteFolder(folder); err != nil {
		t.Fatal(err)
	}
	runtimeDir = filepath.Join(repo, "harnesses", "bun", "runtime")
	out = filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder: folder, Contract: f.Contract(), EngineVersion: Version(), Bun: bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: runtimeDir, Out: out, Work: filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	return out, runtimeDir
}

// writeCasePlugin writes the plugin module a warm harness loads: the case
// plugin of harnesses/bun/test, over every case's declarative plugin, and
// returns it with the file its onRunSettled reports to.
func writeCasePlugin(t *testing.T, f *conformance.PluginFile) (plugin, report string) {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := json.Marshal(casePluginInput(f))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	report = filepath.Join(dir, "settled.jsonl")
	src := fmt.Sprintf("import { casePlugin } from %q;\nexport default casePlugin(%s, %q);\n", filepath.Join(repo, "harnesses", "bun", "test", "caseplugin.ts"), cases, report)
	plugin = filepath.Join(dir, "plugin.ts")
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return plugin, report
}

// casePluginInput is what the TypeScript case plugin takes.
func casePluginInput(f *conformance.PluginFile) []map[string]any {
	var out []map[string]any
	for _, c := range f.Cases {
		out = append(out, map[string]any{"flow": c.FlowPath(), "plugin": c.Plugin})
	}
	return out
}

// pluginOutcome is a run's outcome as a case checks it.
func pluginOutcome(res *Result) conformance.PluginOutcome {
	o := conformance.PluginOutcome{Status: res.Status, MayHaveMutated: res.MayHaveMutated, Result: res.Result, Ran: map[string]bool{}}
	if res.Latched != nil {
		o.Latched = res.Latched.Error()
	}
	for _, w := range res.Warnings {
		o.Warnings = append(o.Warnings, struct{ Code, Message string }{w.Code, w.Message})
	}
	if res.Trace != nil {
		for _, s := range res.Trace.Steps {
			if s.Ran {
				o.Ran[s.Site.String()] = true
			}
		}
		o.Deviations = res.Trace.Deviations
	}
	return o
}

// caseRun is how a case's run is started: its context, bounded by the
// run's hard deadline, and base with the run's budgets and latches (its
// write-capable modules and fatal error names after base's own hooks).
func caseRun(c conformance.PluginCase, base RunOptions) (context.Context, context.CancelFunc, RunOptions) {
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if c.Run.DeadlineMS > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(c.Run.DeadlineMS)*time.Millisecond)
	}
	opts := base
	if len(c.Run.WriteCapable) > 0 || len(c.Run.FatalErrorNames) > 0 {
		var h Hooks
		if base.Hooks != nil {
			h = *base.Hooks
		}
		h.WriteCapable = slices.Clone(h.WriteCapable)
		for _, m := range c.Run.WriteCapable {
			h.WriteCapable = append(h.WriteCapable, Selector{Flow: c.FlowPath(), Module: &m})
		}
		h.FatalErrorNames = append(slices.Clone(h.FatalErrorNames), c.Run.FatalErrorNames...)
		opts.Hooks = &h
	}
	if c.Run.PreWriteDeadlineMS > 0 {
		opts.PreWriteDeadline = time.Now().Add(time.Duration(c.Run.PreWriteDeadlineMS) * time.Millisecond)
	}
	if c.Run.Clock == "record" {
		opts.Clock = &RecordClock{}
	}
	return ctx, cancel, opts
}

func TestPluginCases(t *testing.T) {
	bun := bunOrSkip(t)
	f := loadPluginCases(t)
	out, runtimeDir := buildPluginBundle(t, bun, f)
	timeout := time.Duration(f.TimeoutMS) * time.Millisecond

	t.Run("typescript, warm", func(t *testing.T) {
		plugin, report := writeCasePlugin(t, f)
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: plugin, PluginTimeout: timeout})
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		var runs []string
		for _, c := range f.Cases {
			t.Run(c.ID, func(t *testing.T) {
				ctx, cancel, opts := caseRun(c, RunOptions{})
				defer cancel()
				res, err := rt.Run(ctx, c.FlowPath(), json.RawMessage(`{}`), opts)
				if err != nil {
					t.Fatal(err)
				}
				runs = append(runs, res.ID)
				if d := c.Check(pluginOutcome(res)); len(d) > 0 {
					t.Fatalf("%s:\n  %s", c.Title, strings.Join(d, "\n  "))
				}
				// onRunEnd reports every run, a canceled one too.
				if !slices.ContainsFunc(res.Warnings, func(w Warning) bool { return w.Code == conformance.PluginLog }) {
					t.Fatalf("no %s warning: the plugin's onRunEnd did not report the run: %+v", conformance.PluginLog, res.Warnings)
				}
			})
		}
		// H9's onRunSettled: every case's run settles, and none while one
		// of its hooks still ran, a hook the engine stopped waiting for
		// included.
		checkSettlement(t, runs, report)
	})

	t.Run("go callbacks", func(t *testing.T) {
		g := newGoCasePlugin(f)
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Providers: g.providers})
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		for _, c := range f.Cases {
			if !c.RunsGo() {
				continue
			}
			t.Run(c.ID, func(t *testing.T) {
				vars := map[string]Variable{}
				for p, v := range c.Hooks.Variables {
					vars[p] = Variable{Value: v}
				}
				ctx, cancel, opts := caseRun(c, RunOptions{Hooks: g.hooks, Platform: &RunPlatform{Variables: vars}})
				defer cancel()
				res, err := rt.Run(ctx, c.FlowPath(), json.RawMessage(`{}`), opts)
				if err != nil {
					t.Fatal(err)
				}
				if d := c.Check(pluginOutcome(res)); len(d) > 0 {
					t.Fatalf("%s:\n  %s", c.Title, strings.Join(d, "\n  "))
				}
			})
		}
	})
}

// goCasePlugin is the cases' declarative plugins as Go callbacks, as the
// TypeScript case plugin answers them, for every case's flow.
type goCasePlugin struct {
	hooks     *Hooks
	providers map[string]Provider
	mu        sync.Mutex
	logs      map[string][]string
	runFlow   map[string]string
}

func newGoCasePlugin(f *conformance.PluginFile) *goCasePlugin {
	g := &goCasePlugin{hooks: &Hooks{}, providers: map[string]Provider{}, logs: map[string][]string{}, runFlow: map[string]string{}}
	byFlow := map[string]conformance.PluginSpec{}
	answer := func(result, failed json.RawMessage, decline bool, throw string, delay int) (Outcome, bool, error) {
		time.Sleep(time.Duration(delay) * time.Millisecond)
		switch {
		case throw != "":
			return Outcome{}, false, errors.New(throw)
		case decline:
			return Outcome{}, false, nil
		case failed != nil:
			return Outcome{Result: failed, Failed: true}, true, nil
		}
		return Outcome{Result: result}, true, nil
	}
	for _, c := range f.Cases {
		flow := c.FlowPath()
		byFlow[flow] = c.Hooks
		sel := func(m string) Selector { return Selector{Flow: flow, Module: &m} }
		for _, x := range c.Hooks.Intercept {
			in := Interceptor{Selector: sel(x.Module)}
			if x.Static != nil {
				in.Static = x.Static
			} else {
				in.Func = func(context.Context, Call) (Outcome, bool, error) {
					return answer(x.Result, x.Failed, x.Decline, x.Throw, 0)
				}
			}
			g.hooks.Intercept = append(g.hooks.Intercept, in)
		}
		for _, x := range c.Hooks.RewriteArgs {
			g.hooks.RewriteArgs = append(g.hooks.RewriteArgs, ArgRewriter{Selector: sel(x.Module), Func: func(context.Context, Call) (json.RawMessage, error) {
				return x.Args, nil
			}})
		}
		for _, m := range c.Hooks.HaltBefore {
			g.hooks.HaltBefore = append(g.hooks.HaltBefore, sel(m))
		}
		for _, m := range c.Hooks.HaltAfter {
			g.hooks.HaltAfter = append(g.hooks.HaltAfter, sel(m))
		}
		for _, m := range c.Hooks.WriteCapable {
			g.hooks.WriteCapable = append(g.hooks.WriteCapable, sel(m))
		}
		g.hooks.FatalErrorNames = append(g.hooks.FatalErrorNames, c.Hooks.FatalErrorNames...)
		for name, a := range c.Hooks.Providers {
			g.providers[name] = func(context.Context, Call) (Outcome, bool, error) {
				return answer(a.Result, a.Failed, a.Decline, a.Throw, a.DelayMS)
			}
		}
	}
	g.hooks.BeforeFirstWrite = func(_ context.Context, _ RunHandle, s Site) error {
		if reason, ok := strings.CutPrefix(byFlow[s.Flow].FirstWrite, "veto: "); ok {
			return errors.New(reason)
		}
		return nil
	}
	g.hooks.OnModuleStart = func(_ context.Context, r RunHandle, s Site) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.runFlow[r.ID()] = s.Flow
		if byFlow[s.Flow].Observe {
			g.logs[r.ID()] = append(g.logs[r.ID()], "start:"+s.Path.String())
		}
		return nil
	}
	g.hooks.OnModuleEnd = func(_ context.Context, r RunHandle, s Site, rec StepRecord) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		if byFlow[s.Flow].Observe {
			g.logs[r.ID()] = append(g.logs[r.ID()], fmt.Sprintf("end:%s:%s", s.Path, rec.Status))
		}
		return nil
	}
	g.hooks.InvocationEnv = func(_ context.Context, _ RunHandle, s Site, env map[string]string) error {
		for k, v := range byFlow[s.Flow].Env {
			env[k] = v
		}
		return nil
	}
	g.hooks.OnRunEnd = func(_ context.Context, t *Trace) []Warning {
		g.mu.Lock()
		defer g.mu.Unlock()
		log, _ := json.Marshal(append([]string{}, g.logs[t.RunID]...))
		ws := []Warning{{Code: conformance.PluginLog, Message: string(log)}}
		for _, w := range byFlow[g.runFlow[t.RunID]].Warnings {
			ws = append(ws, Warning{Code: w.Code, Message: w.Message})
		}
		return ws
	}
	return g
}

// checkSettlement checks, from the case plugin's report file, that its
// onRunSettled heard of every one of runs, trying for a few seconds, and
// that none of them settled while one of its hooks still ran.
func checkSettlement(t *testing.T, runs []string, report string) {
	t.Helper()
	var settled, early []string
	var err error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if settled, early, err = conformance.ReadSettlement(report); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(runs, func(r string) bool { return !slices.Contains(settled, r) }) {
			break
		}
	}
	for _, r := range runs {
		if !slices.Contains(settled, r) {
			t.Errorf("the plugin's onRunSettled never heard of run %s", r)
		}
	}
	if len(early) > 0 {
		t.Errorf("runs %q settled while one of their hooks still ran", early)
	}
}

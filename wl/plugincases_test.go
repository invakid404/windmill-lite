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
// plugin of harnesses/bun/test, over every case's declarative plugin.
func writeCasePlugin(t *testing.T, f *conformance.PluginFile) string {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := json.Marshal(casePluginInput(f))
	if err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf("import { casePlugin } from %q;\nexport default casePlugin(%s);\n", filepath.Join(repo, "harnesses", "bun", "test", "caseplugin.ts"), cases)
	path := filepath.Join(t.TempDir(), "plugin.ts")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
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
	o := conformance.PluginOutcome{Status: res.Status, Result: res.Result, Ran: map[string]bool{}}
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
	}
	return o
}

func TestPluginCases(t *testing.T) {
	bun := bunOrSkip(t)
	f := loadPluginCases(t)
	out, runtimeDir := buildPluginBundle(t, bun, f)
	timeout := time.Duration(f.TimeoutMS) * time.Millisecond

	t.Run("typescript, warm", func(t *testing.T) {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: writeCasePlugin(t, f), PluginTimeout: timeout})
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		for _, c := range f.Cases {
			t.Run(c.ID, func(t *testing.T) {
				res, err := rt.Run(context.Background(), c.FlowPath(), json.RawMessage(`{}`), RunOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if d := c.Check(pluginOutcome(res)); len(d) > 0 {
					t.Fatalf("%s:\n  %s", c.Title, strings.Join(d, "\n  "))
				}
			})
		}
		// H9's onRunSettled: a run that settled is in the list the plugin
		// keeps, which a later run's onRunEnd reports (plugin_settled).
		first, err := rt.Run(context.Background(), f.Cases[0].FlowPath(), json.RawMessage(`{}`), RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !settledBy(t, func() []Warning {
			res, err := rt.Run(context.Background(), f.Cases[0].FlowPath(), json.RawMessage(`{}`), RunOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return res.Warnings
		}, first.ID) {
			t.Fatalf("the plugin's onRunSettled never heard of run %s", first.ID)
		}
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
				res, err := rt.Run(context.Background(), c.FlowPath(), json.RawMessage(`{}`), RunOptions{Hooks: g.hooks, Platform: &RunPlatform{Variables: vars}})
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
		for name, a := range c.Hooks.Providers {
			g.providers[name] = func(context.Context, Call) (Outcome, bool, error) {
				return answer(a.Result, a.Failed, a.Decline, a.Throw, a.DelayMS)
			}
		}
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

// settledBy reports whether the plugin's onRunSettled heard of run, as the
// warnings of the runs next gives report it, trying for a few seconds.
func settledBy(t *testing.T, next func() []Warning, run string) bool {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		for _, w := range next() {
			var settled []string
			if w.Code == "plugin_settled" && json.Unmarshal([]byte(w.Message), &settled) == nil && slices.Contains(settled, run) {
				return true
			}
		}
	}
	return false
}

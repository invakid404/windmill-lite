package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/oracle"
	"github.com/invakid404/windmill-lite/wl"
)

// effectSinkVar is the variable that names the effect sink to a fixture's
// scripts (OR-16). On the oracle it is the one host variable the worker
// passes on to jobs; on windmill-lite the runner sets it in every
// invocation's environment (ADR 0011, Effects).
const effectSinkVar = "WL_EFFECT_SINK"

// effectServer is one fixture's effect sink on windmill-lite and, below
// it, the replay service of stub mode (milestone E4b): the same server
// wltest record runs for the oracle (oracle.Sink, routing
// conformance.ReplayPath to the conformance.ReplayService), here on the
// loopback interface, which a step's fetch reaches directly. Effects are
// grouped by the outer run, whose root job id is the run's id
// (WM_ROOT_JOB_ID), so the runs of a group are kept apart (OR-V6).
type effectServer struct {
	sink   oracle.Sink
	replay conformance.ReplayService
	url    string
}

// newEffectServer starts an effect server on a free loopback port.
func newEffectServer() (*effectServer, error) {
	s := &effectServer{}
	s.sink.Replay = &s.replay
	port, err := s.sink.Listen("127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.url = fmt.Sprintf("http://127.0.0.1:%d", port)
	return s, nil
}

// Close stops the server.
func (s *effectServer) Close() { _ = s.sink.Shutdown() }

// openRun starts collecting the effects of the run whose id is root and,
// in stub mode, answering its stubs from table. The run's invocations must
// get the environment the returned hooks give (runHooks).
func (s *effectServer) openRun(root string, stub bool, table conformance.ReplayTable) *runEffects {
	h := &runEffects{url: s.url, sites: map[string]conformance.SitePath{}}
	s.sink.Open(root)
	if stub {
		s.replay.Open(root, table, h)
	}
	return h
}

// closeRun stops collecting the run's effects and returns them, the
// sink's, in arrival order, and the answered stub calls, in arrival order:
// none when the run was not in stub mode.
func (s *effectServer) closeRun(root string) ([]json.RawMessage, []conformance.ReplayCall) {
	effects, _ := s.sink.Close(root)
	return effects, s.replay.Close(root)
}

// runEffects is one run's hooks for the effect server. It gives every
// invocation of the run WL_EFFECT_SINK, and records the site each job
// launches at (WM_JOB_ID), from which the replay service locates a stub's
// job while the run is in progress: windmill-lite's side of the oracle's
// job-tree walk (ADR 0011, the replay service). It uses the engine's
// invocation-environment hook (H12), which a host gives a run as
// wl.Hooks.InvocationEnv (milestone F2a), as this runner does.
type runEffects struct {
	url   string
	mu    sync.Mutex
	sites map[string]conformance.SitePath
}

var _ conformance.Locator = (*runEffects)(nil)

// InvocationEnv sets WL_EFFECT_SINK and records the job's site: the
// module's, never an attempt, which the replay service counts itself.
func (h *runEffects) InvocationEnv(_ context.Context, _ wl.RunHandle, s wl.Site, env map[string]string) error {
	env[effectSinkVar] = h.url
	h.mu.Lock()
	h.sites[env["WM_JOB_ID"]] = s.Path
	h.mu.Unlock()
	return nil
}

// Locate returns the site the job launched at. The engine sets a job's
// environment before the job starts, so a stub's call never comes first;
// a job the run did not launch is unresolved at once.
func (h *runEffects) Locate(_ context.Context, root, job string) (conformance.SitePath, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	site, ok := h.sites[job]
	if !ok {
		return conformance.SitePath{}, fmt.Errorf("run %s launched no job %s", root, job)
	}
	return site, nil
}

// checkStub holds a stub run's answered calls against its trace by the
// rules the oracle applies to its walk (conformance.CheckReplay), where
// the jobs that must have called are the ones whose kind executes a
// script. The trace's records put each launched job (StepRecord.Job) at
// its site, a failed attempt at <module>/r:<k>. A job that is no child
// flow's (a subflow step's record carries its child flow's job) is a
// leaf's, and runs a stub, since the stubbed tree keeps no other script.
// A record that ran with no job is either an identity job, which runs no
// script, or a script job that launched and failed before its invocation
// (an input transform's failure, FX-14; no entrypoint, WR-12; a canceled
// scheduled job, FX-38), which is a job of the walk that never called.
// launchedScript tells them apart; such a job is walked under a key of its
// own, since it has no id. A superseded record is one Windmill's job tree
// no longer reaches, so its job is not walked. prog and flow are the run's
// program and target flow.
func checkStub(t *engine.Trace, prog *ir.Program, flow string, calls []conformance.ReplayCall) error {
	flows := map[string]bool{t.RunID: true}
	for _, c := range t.Children {
		flows[c.ID] = true
	}
	walked := map[string]conformance.SitePath{}
	var scripts []string
	for _, rec := range t.Steps {
		switch {
		case rec.Superseded:
		case rec.Job != "":
			walked[rec.Job] = rec.Site
			if !flows[rec.Job] {
				scripts = append(scripts, rec.Job)
			}
		case rec.Ran && launchedScript(rec, moduleAt(prog, flow, moduleSite(rec.Site))):
			key := "(no invocation) " + rec.Site.String()
			walked[key] = rec.Site
			scripts = append(scripts, key)
		}
	}
	return conformance.CheckReplay(calls, walked, scripts)
}

// launchedScript reports whether rec, a record of module m that ran with
// no job, is a script job that failed before its invocation rather than an
// identity job. A mocked module runs an identity job whatever its kind
// (FX-18), and so does a skipped one (FX-16, FX-17), which the trace flags
// skipped except for the failure module (FX-32). A job that launched with
// no invocation and did not fail is an identity job, since a script job
// that never reaches its invocation fails (FX-14, WR-12, FX-38): that
// covers the failure module's skipped identity job, which has no
// stop_after_if to fail it (FX-2), while a mocked module's identity job,
// whose stop_after_if can fail it (FX-21), is recognised by its mock.
func launchedScript(rec engine.StepRecord, m *ir.Module) bool {
	return m != nil && m.Leaf != nil && (m.Mock == nil || !m.Mock.Enabled) &&
		!rec.Skipped && rec.Status == conformance.StepFailure
}

// moduleSite is the site of the module a site is of: an attempt's
// module, or the site itself.
func moduleSite(p conformance.SitePath) conformance.SitePath {
	segs := p.Segments()
	if len(segs) == 0 || segs[len(segs)-1].Kind() != conformance.KindAttempt {
		return p
	}
	return conformance.SitePath{}.Child(segs[:len(segs)-1]...)
}

// moduleAt returns the module of prog at site in the flow at path flow,
// or nil when the site names none. It walks the site's segments down the
// module lists as the encoder names their modules (ADR 0011): an
// iteration into its loop's body, a branch into that branch's modules, a
// subflow step's site into the subflow's modules, and a slot to the
// failure or preprocessor module of the flow that holds it, a container
// body's copy being its enclosing flow's (FX-34). An attempt is its
// module's.
func moduleAt(prog *ir.Program, flow string, site conformance.SitePath) *ir.Module {
	owner := prog.Flows[flow]
	if owner == nil {
		return nil
	}
	list := owner.Modules
	var m *ir.Module
	for _, seg := range site.Segments() {
		if m != nil && m.Subflow != nil && (seg.Kind() == conformance.KindModule || seg.Kind() == conformance.KindSlot) {
			// What follows a subflow step's site is the subflow's.
			if owner = prog.Flows[m.Subflow.Path]; owner == nil {
				return nil
			}
			list = owner.Modules
		}
		switch seg.Kind() {
		case conformance.KindModule:
			ids := make([]string, len(list))
			for i, x := range list {
				ids[i] = x.ID
			}
			m = nil
			for i, s := range conformance.ModuleSegments(ids) {
				if s == seg {
					m = list[i]
					break
				}
			}
			if m == nil {
				return nil
			}
			list = nil
		case conformance.KindIteration:
			switch {
			case m != nil && m.ForLoop != nil:
				list = m.ForLoop.Modules
			case m != nil && m.WhileLoop != nil:
				list = m.WhileLoop.Modules
			default:
				return nil
			}
		case conformance.KindBranch:
			n, indexed := seg.Index()
			switch {
			case m != nil && m.BranchOne != nil && seg == conformance.DefaultBranch():
				list = m.BranchOne.Default
			case m != nil && m.BranchOne != nil && indexed && n < len(m.BranchOne.Branches):
				list = m.BranchOne.Branches[n].Modules
			case m != nil && m.BranchAll != nil && indexed && n < len(m.BranchAll.Branches):
				list = m.BranchAll.Branches[n].Modules
			default:
				return nil
			}
		case conformance.KindSlot:
			switch seg {
			case conformance.FailureSlot():
				m = owner.FailureModule
			case conformance.PreprocessorSlot():
				m = owner.PreprocessorModule
			default:
				m = nil
			}
			if m == nil {
				return nil
			}
			list = nil
		case conformance.KindAttempt:
		}
	}
	return m
}

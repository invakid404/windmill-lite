package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/oracle"
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
// in stub mode, answering its stubs from table. The run must run with the
// hooks it returns.
func (s *effectServer) openRun(root string, stub bool, table conformance.ReplayTable) *runEffects {
	h := &runEffects{url: s.url, sites: map[string]conformance.SitePath{}, leaves: map[conformance.SitePath]bool{}}
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
// job-tree walk (ADR 0011, the replay service). It also records which
// modules are script leaves, for checkStub. It uses the engine's
// invocation-environment and module-start hooks (H12, H6), seams that
// host hooks expose (milestone F2a), so the same wiring serves a run made
// through wl.
type runEffects struct {
	engine.NoHooks
	url    string
	mu     sync.Mutex
	sites  map[string]conformance.SitePath
	leaves map[conformance.SitePath]bool
}

var (
	_ engine.Hooks        = (*runEffects)(nil)
	_ conformance.Locator = (*runEffects)(nil)
)

// InvocationEnv sets WL_EFFECT_SINK and records the job's site: the
// module's, never an attempt, which the replay service counts itself.
func (h *runEffects) InvocationEnv(_ context.Context, _ engine.Run, s engine.Site, env map[string]string) error {
	env[effectSinkVar] = h.url
	h.mu.Lock()
	h.sites[env["WM_JOB_ID"]] = s.Path
	h.mu.Unlock()
	return nil
}

// OnModuleStart records whether the module runs a script.
func (h *runEffects) OnModuleStart(_ context.Context, _ engine.Run, s engine.Site) error {
	if s.Script != "" {
		h.mu.Lock()
		h.leaves[s.Path] = true
		h.mu.Unlock()
	}
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
// rules the oracle applies to its walk (conformance.CheckReplay). The
// trace's records put each launched job (StepRecord.Job) at its site, a
// failed attempt at <module>/r:<k>. A job that is no child flow's (a
// subflow step's record carries its child flow's job) is a leaf's, and
// runs a stub, since the stubbed tree keeps no other script. A script
// leaf's job that launched but never reached an invocation (an input
// transform that failed, FX-14) is a job of the walk too, and one that
// never called: it is walked under a key of its own, since it has no id.
// leaves are the sites of the modules that run a script (runEffects). A
// superseded record is one Windmill's job tree no longer reaches, so its
// job is not walked.
func checkStub(t *engine.Trace, leaves map[conformance.SitePath]bool, calls []conformance.ReplayCall) error {
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
		case rec.Ran && leaves[moduleSite(rec.Site)]:
			key := "(no invocation) " + rec.Site.String()
			walked[key] = rec.Site
			scripts = append(scripts, key)
		}
	}
	return conformance.CheckReplay(calls, walked, scripts)
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

// leafSites returns the sites of the modules the run started that run a
// script.
func (h *runEffects) leafSites() map[conformance.SitePath]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return maps.Clone(h.leaves)
}

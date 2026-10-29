package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// open starts collecting the effects of the run whose id is root and, in
// stub mode, answering its stubs from table. The run must run with the
// hooks it returns.
func (s *effectServer) open(root string, stub bool, table conformance.ReplayTable) *runEffects {
	h := &runEffects{url: s.url, sites: map[string]conformance.SitePath{}}
	s.sink.Open(root)
	if stub {
		s.replay.Open(root, table, h)
	}
	return h
}

// close stops collecting the run's effects and returns them, the sink's,
// in arrival order, and in stub mode the answered calls, in arrival order.
func (s *effectServer) close(root string, stub bool) ([]json.RawMessage, []conformance.ReplayCall) {
	effects, _ := s.sink.Close(root)
	if !stub {
		return effects, nil
	}
	return effects, s.replay.Close(root)
}

// runEffects is one run's hooks for the effect server. It gives every
// invocation of the run WL_EFFECT_SINK, and records the site each job
// launches at (WM_JOB_ID), from which the replay service locates a stub's
// job while the run is in progress: windmill-lite's side of the oracle's
// job-tree walk (ADR 0011, the replay service). It uses the engine's
// invocation-environment hook (H12), the seam that host hooks expose
// (milestone F2a), so the same wiring serves a run made through wl.
type runEffects struct {
	engine.NoHooks
	url   string
	mu    sync.Mutex
	sites map[string]conformance.SitePath
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

// Locate returns the site job launched at. The engine sets a job's
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
// runs a stub, since the stubbed tree keeps no other script. A superseded
// record is one Windmill's job tree no longer reaches, so its job is not
// walked.
func checkStub(t *engine.Trace, calls []conformance.ReplayCall) error {
	flows := map[string]bool{t.RunID: true}
	for _, c := range t.Children {
		flows[c.ID] = true
	}
	walked := map[string]conformance.SitePath{}
	var scripts []string
	for _, rec := range t.Steps {
		if rec.Superseded || rec.Job == "" {
			continue
		}
		walked[rec.Job] = rec.Site
		if !flows[rec.Job] {
			scripts = append(scripts, rec.Job)
		}
	}
	return conformance.CheckReplay(calls, walked, scripts)
}

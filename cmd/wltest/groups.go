package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/wl"
)

// A group of runs (OR-15) is one test of concurrent outer runs: its runs
// are submitted together, and they fail together when one of them fails or
// when their root jobs were not in flight together (ADR 0011), as wltest
// record holds them on the oracle. On windmill-lite each run of a group
// waits, at its first module, until every run of the group is in flight,
// so the group's runs are in flight together by construction, not by the
// scheduler's chance; each run's span, from its submission to its end,
// then shows it.

// groupWait bounds how long a run of a group waits at its first module for
// the group's other runs. A run that waits that long goes on, and its group
// fails unless the spans overlap after all.
const groupWait = 10 * time.Second

// runGroup is one group's runs in flight.
type runGroup struct {
	name string
	wait time.Duration

	mu      sync.Mutex
	pending int           // the runs not yet in flight
	all     chan struct{} // closed once every run is in flight
	spans   map[string]runSpan
}

// runSpan is the time a run of a group was in flight, from its
// submission to its end.
type runSpan struct{ from, to time.Time }

// newRunGroup starts the group name of the runs named runs.
func newRunGroup(name string, runs int) *runGroup {
	return &runGroup{name: name, wait: groupWait, pending: runs, all: make(chan struct{}), spans: map[string]runSpan{}}
}

// member returns the group's side of the run named run.
func (g *runGroup) member(run string) *groupMember {
	return &groupMember{g: g, run: run}
}

// groupMember is one run of a group.
type groupMember struct {
	g    *runGroup
	run  string
	once sync.Once
}

// arrive counts the run in flight, once; it reports whether this call
// did.
func (m *groupMember) arrive() (first bool) {
	m.once.Do(func() {
		first = true
		m.g.mu.Lock()
		defer m.g.mu.Unlock()
		if m.g.pending--; m.g.pending == 0 {
			close(m.g.all)
		}
	})
	return first
}

// inFlight is called at each module start of the run: the first time, it
// counts the run in flight and waits until the group's other runs are too,
// the group's wait has passed, or ctx ends.
func (m *groupMember) inFlight(ctx context.Context) {
	if !m.arrive() {
		return
	}
	t := time.NewTimer(m.g.wait)
	defer t.Stop()
	select {
	case <-m.g.all:
	case <-t.C:
	case <-ctx.Done():
	}
}

// ended records the run's span. A run that ended without starting a module
// is counted in flight then, so that it holds up no other run.
func (m *groupMember) ended(from, to time.Time) {
	m.arrive()
	m.g.mu.Lock()
	defer m.g.mu.Unlock()
	m.g.spans[m.run] = runSpan{from, to}
}

// conclude returns why the group failed as a whole, or nil: a run of the
// group failed (group_member_failed, naming the first in runs' order), or
// the runs were not in flight together (group_not_concurrent: one started
// after another ended). failed reports whether a run failed.
func (g *runGroup) conclude(runs []string, failed func(run string) bool) error {
	for _, r := range runs {
		if failed(r) {
			return fmt.Errorf(`{"error":"group_member_failed","group":%s,"run":%s}`, jsonText(g.name), jsonText(r))
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, a := range runs {
		for _, b := range runs {
			sa, oka := g.spans[a]
			sb, okb := g.spans[b]
			if !oka || !okb {
				return fmt.Errorf(`{"error":"group_not_concurrent","group":%s,"reason":%s}`, jsonText(g.name), jsonText("a run has no span"))
			}
			if a != b && !sa.from.Before(sb.to) {
				return fmt.Errorf(`{"error":"group_not_concurrent","group":%s,"reason":%s}`, jsonText(g.name), jsonText("run "+a+" started after run "+b+" ended"))
			}
		}
	}
	return nil
}

// runHooks are one run's host hooks in wltest check (wl.Hooks, ADR 0008):
// the effect server's, which set the invocation environment (H12,
// runEffects), and its group's, which hold its first module until the
// group's other runs are in flight (H6). They are the Go callbacks a host
// gives a run, so the runner takes no path a host does not have.
func runHooks(fx *runEffects, grp *groupMember) *wl.Hooks {
	h := &wl.Hooks{}
	if fx != nil {
		h.InvocationEnv = fx.InvocationEnv
	}
	if grp != nil {
		h.OnModuleStart = func(ctx context.Context, _ wl.RunHandle, _ wl.Site) error {
			grp.inFlight(ctx)
			return nil
		}
	}
	return h
}

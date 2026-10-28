package oracle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/openflow"
)

// Locator is the oracle's side of the replay service (ADR 0011 stub mode):
// it finds the site of a stub's job in a run that is still in progress by
// walking the run's job tree from the root, with the walker's rules
// (OR-11) and the site encoding of the recordings:
//
//   - a module entry's job is at the module's site, and so is each of its
//     failed_retries: the attempt is the replay service's count;
//   - a flow_jobs entry is at i:<n> (b:<n> for a branchall); a leaf there
//     is a collapsed iteration, at i:<n>/m:<the body's one module>;
//   - a child with a flow_status (a loop iteration, a branch body, a named
//     subflow) is walked in turn, a branchone body under b:<n> or
//     b:default;
//   - the failure and preprocessor slots are at s:failure and
//     s:preprocessor, and a generated -v module is skipped (OR-V7).
//
// A job that is not in the tree yet (its parent's status is written after
// the job is queued) is looked for again every Interval until the
// context ends. Completed jobs are cached, since their status no longer
// changes; it is safe for concurrent use.
type Locator struct {
	Client    *Client
	Workspace string
	Tree      *SyncTree
	// Interval is the wait between walks; zero means 100 ms.
	Interval time.Duration

	mu   sync.Mutex
	done map[string]Job
}

// Locate implements conformance.Locator.
func (l *Locator) Locate(ctx context.Context, root, job string) (conformance.SitePath, error) {
	interval := l.Interval
	if interval == 0 {
		interval = 100 * time.Millisecond
	}
	notFound := func() error {
		return fmt.Errorf("oracle: job %s is not in the job tree of run %s: %w", job, root, ctx.Err())
	}
	for {
		site, found, err := l.find(ctx, root, job)
		if err != nil {
			if ctx.Err() != nil {
				return site, notFound()
			}
			return site, err
		}
		if found {
			return site, nil
		}
		select {
		case <-ctx.Done():
			return conformance.SitePath{}, notFound()
		case <-time.After(interval):
		}
	}
}

type locateState struct {
	*Locator
	ctx     context.Context
	job     string
	visited map[string]bool
}

// find walks the tree once.
func (l *Locator) find(ctx context.Context, root, job string) (conformance.SitePath, bool, error) {
	if job == root {
		return conformance.SitePath{}, true, nil // a script run's root is the stub
	}
	s := &locateState{Locator: l, ctx: ctx, job: job, visited: map[string]bool{root: true}}
	r, ok, err := s.get(root)
	if err != nil || !ok {
		return conformance.SitePath{}, false, err
	}
	if r.FlowStatus == nil {
		return conformance.SitePath{}, false, nil
	}
	def := flowDef{}
	if f := l.Tree.flow(r.ScriptPath); f != nil {
		def = flowDef{modules: f.Value.Modules, failure: f.Value.FailureModule, preprocessor: f.Value.PreprocessorModule, known: true}
	}
	return s.flow(conformance.SitePath{}, r.FlowStatus, def)
}

// get fetches a job, from the cache once it completed. ok is false for a
// job the API does not know yet.
func (s *locateState) get(id string) (Job, bool, error) {
	s.mu.Lock()
	j, cached := s.done[id]
	s.mu.Unlock()
	if cached {
		return j, true, nil
	}
	j, err := s.Client.GetJob(s.ctx, s.Workspace, id)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusNotFound {
		return j, false, nil
	}
	if err != nil {
		return j, false, err
	}
	if j.Type == "CompletedJob" {
		s.mu.Lock()
		if s.done == nil {
			s.done = map[string]Job{}
		}
		s.done[id] = j
		s.mu.Unlock()
	}
	return j, true, nil
}

func (s *locateState) flow(base conformance.SitePath, fs *FlowStatus, def flowDef) (conformance.SitePath, bool, error) {
	user := fs.Modules
	if n := len(user); n >= 2 && generated(user, def) {
		user = user[:n-1]
	}
	ids := make([]string, len(user))
	for i, e := range user {
		ids[i] = e.ID
	}
	if def.known && len(def.modules) != len(user) {
		return conformance.SitePath{}, false, fmt.Errorf("oracle: %s has modules %q, but the fixture defines %d", describe(base), ids, len(def.modules))
	}
	segs := conformance.ModuleSegments(ids)
	for i, e := range user {
		var d *openflow.Module
		if def.known {
			d = &def.modules[i]
		}
		if site, ok, err := s.module(base.Child(segs[i]), e, d); ok || err != nil {
			return site, ok, err
		}
	}
	for _, slot := range []struct {
		seg     conformance.Segment
		entry   *FlowStatusModule
		defined *openflow.Module
	}{
		{conformance.FailureSlot(), fs.FailureModule, def.failure},
		{conformance.PreprocessorSlot(), fs.PreprocessorModule, def.preprocessor},
	} {
		if slot.entry == nil || slot.entry.Job == nil {
			continue
		}
		if site, ok, err := s.module(base.Child(slot.seg), *slot.entry, slot.defined); ok || err != nil {
			return site, ok, err
		}
	}
	return conformance.SitePath{}, false, nil
}

// leafModule reports whether a module's own job can hold no other job.
func leafModule(d *openflow.Module) bool {
	if d == nil {
		return false
	}
	switch d.Value.(type) {
	case *openflow.RawScript, *openflow.PathScript, *openflow.Identity, *openflow.FlowScript:
		return true
	}
	return false
}

func (s *locateState) module(site conformance.SitePath, e FlowStatusModule, d *openflow.Module) (conformance.SitePath, bool, error) {
	for _, id := range e.FailedRetries {
		if id == s.job {
			return site, true, nil
		}
	}
	if e.HasFlowJobs {
		return s.container(site, e, d)
	}
	if e.Job == nil || *e.Job == nilJob {
		return conformance.SitePath{}, false, nil
	}
	id := *e.Job
	if id == s.job {
		return site, true, nil
	}
	if leafModule(d) || s.visited[id] {
		return conformance.SitePath{}, false, nil
	}
	body, sub := site, flowDef{}
	if e.BranchChosen != nil {
		seg := conformance.DefaultBranch()
		if e.BranchChosen.Type == "branch" {
			if e.BranchChosen.Branch == nil {
				return conformance.SitePath{}, false, fmt.Errorf("oracle: site %s chose a branch without an index", site)
			}
			seg = conformance.Branch(*e.BranchChosen.Branch)
		}
		body = site.Child(seg)
		if d != nil {
			if b, ok := d.Value.(*openflow.BranchOne); ok {
				if e.BranchChosen.Type == "default" {
					sub = bodyDef(b.Default)
				} else if *e.BranchChosen.Branch < len(b.Branches) {
					sub = bodyDef(b.Branches[*e.BranchChosen.Branch].Modules)
				}
			}
		}
	} else if d != nil {
		switch v := d.Value.(type) {
		case *openflow.BranchOne:
			return conformance.SitePath{}, false, nil // no branch chosen yet
		case *openflow.PathFlow:
			if f := s.Tree.flow(v.Path); f != nil {
				sub = flowDef{modules: f.Value.Modules, failure: f.Value.FailureModule, preprocessor: f.Value.PreprocessorModule, known: true}
			}
		}
	}
	j, ok, err := s.get(id)
	if err != nil || !ok || j.FlowStatus == nil {
		return conformance.SitePath{}, false, err
	}
	s.visited[id] = true
	return s.flow(body, j.FlowStatus, sub)
}

// container looks through a loop's or branchall's children.
func (s *locateState) container(site conformance.SitePath, e FlowStatusModule, d *openflow.Module) (conformance.SitePath, bool, error) {
	branchAll := e.BranchAll != nil
	var bodies [][]openflow.Module
	if d != nil {
		switch v := d.Value.(type) {
		case *openflow.ForLoop:
			bodies = [][]openflow.Module{v.Modules}
		case *openflow.WhileLoop:
			bodies = [][]openflow.Module{v.Modules}
		case *openflow.BranchAll:
			for _, b := range v.Branches {
				bodies = append(bodies, b.Modules)
			}
		}
	}
	body := func(i int) flowDef {
		switch {
		case branchAll && i < len(bodies):
			return bodyDef(bodies[i])
		case !branchAll && len(bodies) == 1:
			return bodyDef(bodies[0])
		}
		return flowDef{}
	}
	for i, id := range e.FlowJobs {
		seg := conformance.Iteration(i)
		if branchAll {
			seg = conformance.Branch(i)
		}
		child := site.Child(seg)
		if id == s.job {
			bd := body(i)
			if !bd.known || len(bd.modules) != 1 {
				return conformance.SitePath{}, false, fmt.Errorf("oracle: job %s is the leaf iteration %s, but the fixture does not define a one-module body", id, child)
			}
			return child.Child(conformance.Module(bd.modules[0].ID)), true, nil
		}
		if s.visited[id] {
			continue
		}
		j, ok, err := s.get(id)
		if err != nil {
			return conformance.SitePath{}, false, err
		}
		if !ok || j.FlowStatus == nil {
			continue
		}
		s.visited[id] = true
		if site, ok, err := s.flow(child, j.FlowStatus, body(i)); ok || err != nil {
			return site, ok, err
		}
	}
	return conformance.SitePath{}, false, nil
}

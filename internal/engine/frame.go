package engine

import (
	"context"
	"math/rand/v2"
	"strconv"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// frame is one flow the run executes (CF-1): the root flow, or the child
// flow that a loop iteration runs as. A collapsed iteration (CF-8) is a
// frame too, of its one body module, but its job runs in the flow of its
// loop, the frame's parent, whose results context it uses.
type frame struct {
	// modules are the flow's module list, and failure its failure module,
	// at cursor len(modules) (FX-1): the root's own, since no child flow
	// runs one before N3c. sites has one more entry, the failure slot's.
	modules []*ir.Module
	failure *ir.Module
	sites   []conformance.SitePath
	// input is the flow's input, flow_input (JE-37); prev is its first
	// module's previous_result, which is the input except in a collapsed
	// iteration (CF-8, JE-36).
	input, prev jsonval.Value
	// path is the flow's runnable path (WA-6): the root flow's path,
	// <path>/loop-<i> for a sequential iteration, <path>/forloop-<i> for a
	// parallel one. id is the flow's job id (WM_FLOW_JOB_ID) and scope the
	// id of its innermost flow scope (WM_ROOT_FLOW_JOB_ID, WA-V1).
	path, id, scope string
	// site is a child flow's site, its iteration's (<loop>/i:<n>).
	site conformance.SitePath
	// child marks a child flow: a chaining error at its first module fails
	// it at its pickup (FX-15 context 3), where the root's fails that
	// module (context 2).
	child bool
	// after reports whether the run would start something once the frame's
	// last module has completed, which haltAfter needs (AfterContinue): a
	// further iteration of its loop, an iteration its parallel loop still
	// holds, or what follows the loop, asked of the frames that contain it
	// when it is asked; nil for the root, after which nothing starts.
	after func() bool
	// collapsed is the loop whose iteration iter this frame runs collapsed,
	// as a job of parent; nil for a flow.
	collapsed *ir.Module
	iter      int
	parent    *frame
	// nodes is the frame's scope of the result-by-node-id map (JE-19).
	nodes *nodeScope
	// jobs holds, per module (the failure module last), the stored result
	// of its job when it has one (JE-18 step 2); guarded by run.mu.
	jobs []jsonval.Value
	// rng shuffles the frame's unspecified orders (ADR 0001 §2). A parallel
	// iteration has its own, seeded from its loop's frame, since it runs on
	// a goroutine of its own.
	rng *rand.Rand
}

// nodeScope is one scope of the result-by-node-id map (JE-19, CF-19): the
// root flow's, or a parallel iteration's. Every other child flow writes
// into the scope of its parent, and a lookup walks from a scope to the
// root. The run's mu guards it.
type nodeScope struct {
	parent *nodeScope
	m      map[string]jsonval.Value
}

func newNodeScope(parent *nodeScope) *nodeScope {
	return &nodeScope{parent: parent, m: map[string]jsonval.Value{}}
}

// newFrame is a frame of the module list ms at site prefix: its module
// sites carry each module's occurrence when an id repeats (OF-2, ADR 0011),
// and the failure slot's site, s:failure, comes after them.
func newFrame(ms []*ir.Module, prefix conformance.SitePath) *frame {
	fr := &frame{modules: ms, jobs: make([]jsonval.Value, len(ms)+1)}
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	for _, seg := range conformance.ModuleSegments(ids) {
		fr.sites = append(fr.sites, prefix.Child(seg))
	}
	fr.sites = append(fr.sites, prefix.Child(conformance.FailureSlot()))
	return fr
}

// moduleAt is the module at cursor i: one of the list, or the failure
// module at len(modules) (FX-1).
func (fr *frame) moduleAt(i int) *ir.Module {
	if i == len(fr.modules) {
		return fr.failure
	}
	return fr.modules[i]
}

// isFailure reports whether cursor i is the failure module's.
func (fr *frame) isFailure(i int) bool { return i == len(fr.modules) }

// continues reports whether the run would start something once module i
// of the frame has completed: the next module, or what follows the frame.
func (fr *frame) continues(i int) bool {
	return i < len(fr.modules)-1 || fr.after != nil && fr.after()
}

// ids returns the ids of the frame's modules, in order.
func (fr *frame) ids() []string {
	ids := make([]string, len(fr.modules))
	for i, m := range fr.modules {
		ids[i] = m.ID
	}
	return ids
}

// owner is the frame whose flow the frame's jobs belong to: its loop's for
// a collapsed iteration, its own otherwise.
func (fr *frame) owner() *frame {
	if fr.collapsed != nil {
		return fr.parent
	}
	return fr
}

// stepID is the flow step id of module i's job (WM_FLOW_STEP_ID, the
// step_id of its errors): the loop's for a collapsed iteration (CF-8, CF-24
// item 3), the module's own otherwise.
func (fr *frame) stepID(i int) string {
	if fr.collapsed != nil {
		return fr.collapsed.ID
	}
	return fr.moduleAt(i).ID
}

// jobPath is the Windmill path of module i's job (WR-29, CF-24 item 2,
// WA-6): a path script's own; for an inline script, the frame's path and
// the module id, or <path>/loop-<i> in a collapsed iteration. windmill-lite
// runs the lite form, where an inline script has no path of its own
// (ADR 0001 §1).
func (r *run) jobPath(fr *frame, i int) string {
	m := fr.moduleAt(i)
	if s := r.e.prog.Scripts[m.Leaf.Script]; s != nil && s.Kind != ir.InlineScript {
		return s.Path
	}
	if fr.collapsed != nil {
		return fr.parent.path + "/loop-" + strconv.Itoa(fr.iter)
	}
	return fr.path + "/" + m.ID
}

// recordJob records the stored result of module i's job, which results
// lookups read (JE-18, JE-19). A collapsed iteration registers nothing
// under its body module's id (CF-8).
func (r *run) recordJob(fr *frame, i int, v jsonval.Value) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fr.jobs[i] = v
	if fr.collapsed == nil {
		fr.nodes.m[fr.moduleAt(i).ID] = v
	}
}

// results is the results context of the expressions of fr's modules.
func (r *run) results(fr *frame) *frameResults { return &frameResults{r: r, fr: fr} }

// frameResults answers the results lookups of one frame's expressions.
type frameResults struct {
	r  *run
	fr *frame
}

// Lookup is a results read on the engine path (JE-18 steps 2 and 3): the
// first module of the current flow with that id, when it has a job, gives
// its job's result (a loop's is the array of its iterations' results);
// otherwise the result-by-node-id map does, from the frame's scope up to
// the root's (JE-19); anything else is null. Which of two modules with one
// id step 2 finds (OF-2) is a reading: the first in the flow. The value is
// read from storage, so the evaluator binds its stored text.
func (x *frameResults) Lookup(_ context.Context, id string) (jsonval.Value, error) {
	x.r.mu.Lock()
	defer x.r.mu.Unlock()
	own := x.fr.owner()
	for i, m := range own.modules {
		if m.ID == id {
			if v := own.jobs[i]; !v.IsZero() {
				return v, nil
			}
			break
		}
	}
	v, _ := x.fr.nodes.lookup(id)
	return v, nil
}

// Stored is the result-by-node-id lookup of JE-9's direct path (JE-19):
// the job of the module with that id that finished last in the nearest
// scope that has one.
func (x *frameResults) Stored(_ context.Context, id string) (jsonval.Value, bool) {
	x.r.mu.Lock()
	defer x.r.mu.Unlock()
	return x.fr.nodes.lookup(id)
}

func (s *nodeScope) lookup(id string) (jsonval.Value, bool) {
	for ; s != nil; s = s.parent {
		if v, ok := s.m[id]; ok {
			return v, true
		}
	}
	return jsonval.Value{}, false
}

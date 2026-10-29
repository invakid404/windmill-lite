package engine

import (
	"context"
	"errors"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// frame is one flow the run executes (CF-1): the root flow, or the child
// flow that a loop iteration, a branch or a subflow by path runs as. A
// collapsed iteration (CF-8) is a frame too, of its one body module, but
// its job runs in the flow of its loop, the frame's parent, whose results
// context it uses.
type frame struct {
	// modules are the flow's module list, and failure its failure module,
	// at cursor len(modules) (FX-1): the root's own, a body's copy of its
	// enclosing flow's (CF-1, CF-16, FX-34), or a subflow's own (CF-17).
	// sites has one more entry, the failure slot's.
	modules []*ir.Module
	failure *ir.Module
	sites   []conformance.SitePath
	// def is the flow whose module lists hold the frame's modules: the
	// run's flow, a body's enclosing flow, or the flow a subflow step runs
	// (CF-17). Hooks name it (Site.Flow, ADR 0008's selectors), and its
	// same_worker flag is the frame's, which a body copies (CF-1).
	def *ir.Flow
	// envSrc is flow_env as stored for the frame's scope: its flow's own,
	// or, for a body and for a subflow that defines none, its parent's
	// (CF-1 item 5, CF-17, JE-38); zero when there is none.
	envSrc jsonval.Value
	// started is when the frame's flow job started, which its failure
	// module's started_at reads (FX-32, JE-35).
	started time.Time
	// input is the flow's input, flow_input (JE-37); prev is its first
	// module's previous_result, which is the input except in a collapsed
	// iteration (CF-8, JE-36).
	input, prev jsonval.Value
	// path is the flow's runnable path (WA-6, CF-24): the root flow's path,
	// <path>/loop-<i> for a sequential or while iteration, <path>/forloop-<i>
	// for a parallel one, <path>/branchone-<n> or <path>/branchall-<n> for a
	// branch. id is the flow's job id (WM_FLOW_JOB_ID) and scope the id of
	// its innermost flow scope (WM_ROOT_FLOW_JOB_ID, WA-V1).
	path, id, scope string
	// site is a child flow's site: its iteration's (<loop>/i:<n>), its
	// branch's (<container>/b:<n>, <container>/b:default), or a subflow's
	// step's own (<step>), below which its modules have theirs.
	site conformance.SitePath
	// child marks a child flow: a chaining error at its first module fails
	// it at its pickup (FX-15 context 3), where the root's fails that
	// module (context 2).
	child bool
	// collapsed is the loop whose iteration iter this frame runs collapsed,
	// as a job of parent; nil for a flow.
	collapsed *ir.Module
	iter      int
	parent    *frame
	// nodes is the frame's scope of the result-by-node-id map (JE-19).
	nodes *nodeScope
	// jobs holds, per module (the failure module last), the stored result
	// of its job when it has one (JE-18 step 2); nilJob marks a module that
	// completed with a nil job, a branchone that ran no branch (CF-14,
	// OR-25) or whose predicate threw (OR-11). Both are guarded by run.mu.
	jobs   []jsonval.Value
	nilJob []bool
	// rng shuffles the frame's unspecified orders (ADR 0001 §2). A parallel
	// iteration has its own, seeded from its loop's frame, since it runs on
	// a goroutine of its own.
	rng *rand.Rand
	// failCount and failed are the flow's retry state (FX-27, FX-29): the
	// failures of the current retry sequence, and the sites of the failed
	// attempts its retries recorded, which every module of the flow that
	// completes lists as its own; a success clears both. succeeded holds,
	// per module, that its last status is Success, which a retry's
	// restored previous_result depends on (FX-6, JE-36). spent is what the
	// flow's current transition, since its last job ended (pinged), has
	// spent evaluating expressions, against JE-2's monitor. The goroutine
	// that runs the frame owns them all; a collapsed iteration's monitor
	// state is its loop's flow's (owner).
	failCount uint32
	failed    []conformance.SitePath
	succeeded []bool
	spent     time.Duration
	pinged    time.Time
	// env is flow_env as the transition that started the frame's current
	// module resolved it, and envCached the last resolution that succeeded,
	// which the predicates after a child completes read (JE-38); zero
	// before the frame's first module, where its flow's stored map stands
	// (run.envOf). Only the goroutine that drives the frame touches them.
	env, envCached jsonval.Value
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
	fr := &frame{modules: ms, jobs: make([]jsonval.Value, len(ms)+1), nilJob: make([]bool, len(ms)+1), succeeded: make([]bool, len(ms)+1)}
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

// recordNilJob records that module i completed with a nil job (CF-19): an
// engine read of it from its own flow fails to fetch the job, and the
// result-by-node-id map holds it, so that a direct read answers null
// (JE-9) and does not fall through to an outer scope.
func (r *run) recordNilJob(fr *frame, i int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fr.nilJob[i] = true
	fr.nodes.m[fr.moduleAt(i).ID] = jsonNull
}

// results is the results context of the expressions of fr's modules.
func (r *run) results(fr *frame) *frameResults { return &frameResults{r: r, fr: fr} }

// frameResults answers the results lookups of one frame's expressions.
type frameResults struct {
	r  *run
	fr *frame
}

// Lookup is a results read on the engine path (JE-18 steps 2 and 3): the
// last module of the current flow with that id that has a job gives its
// job's result (a loop's or a branchall's is the array of its children's
// results, a branchone's its branch's), and a nil job fails the read with
// the server's not-found text (CF-19 route 5), since Windmill collects the
// step map from the flow's status entries in list order, a later entry of
// an id replacing an earlier one, and keeps only entries with a job
// (JE-18/e, OF-2); a later module of the id that has not started is
// skipped. Otherwise the result-by-node-id map does, from the frame's
// scope up to the root's (JE-19); anything else is null. The value is read
// from storage, so the evaluator binds its stored text. The server's text
// ends in its source location, which windmill-lite does not write (D14).
func (x *frameResults) Lookup(_ context.Context, id string) (jsonval.Value, error) {
	x.r.mu.Lock()
	defer x.r.mu.Unlock()
	own := x.fr.owner()
	for i := len(own.modules) - 1; i >= 0; i-- {
		if own.modules[i].ID == id {
			if own.nilJob[i] {
				return jsonval.Value{}, errors.New("Failed to fetch result for step '" + id + "': Not found: Completed Job not found at name 00000000-0000-0000-0000-000000000000")
			}
			if v := own.jobs[i]; !v.IsZero() {
				return v, nil
			}
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

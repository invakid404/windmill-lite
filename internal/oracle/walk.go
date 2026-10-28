package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// nilJob is the job id of a module that completed without running a child
// (OR-V2).
const nilJob = "00000000-0000-0000-0000-000000000000"

// SyncTree holds the flows of a fixture's sync tree, by Windmill path. The
// walker reads them to tell a user module from a generated one (OR-V7), to
// name the body module of a collapsed loop iteration, and to know whether a
// flow defines a failure or preprocessor module (OR-11).
type SyncTree struct {
	Flows map[string]*openflow.Flow
}

// LoadSyncTree reads every <name>.flow/flow.yaml below dir, in the YAML
// dialect of the wmill CLI (SY-9, internal/yamlx), and decodes it with
// Windmill's effective decoder (internal/openflow). A flow's path is its
// directory's path relative to dir without the .flow suffix, for example
// f/wl_smoke/two_step.
func LoadSyncTree(dir string) (*SyncTree, error) {
	t := &SyncTree{Flows: make(map[string]*openflow.Flow)}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || !strings.HasSuffix(d.Name(), ".flow") {
			return nil
		}
		data, err := os.ReadFile(filepath.Join(p, "flow.yaml"))
		if err != nil {
			return err
		}
		j, err := yamlx.ToJSON(data)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		f, err := openflow.DecodeFlow(j)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		t.Flows[strings.TrimSuffix(filepath.ToSlash(rel), ".flow")] = f
		return filepath.SkipDir
	})
	return t, err
}

// Walk is one run walked into sites (OR-11).
type Walk struct {
	// RootID is the submitted root job.
	RootID string
	// Status and Result are the root's (OR-V2, OR-10).
	Status conformance.RunStatus
	Result json.RawMessage
	// Sites are the observation records, in walk order.
	Sites []Site
	// IDs maps every walked job id to its site: each record's job, each
	// loop iteration and branch body flow, the root (the root path), and
	// generated modules (for placeholders only).
	IDs map[string]conformance.SitePath
	// RuntimeErrors are the stored results of failed leaf jobs that
	// executed a script (OR-V3 clause a).
	RuntimeErrors []json.RawMessage
	// Timings are the walked jobs' times, by site, for provenance only
	// (OR-17).
	Timings map[string]JobTiming
}

// Site is one observation record before normalisation.
type Site struct {
	Path conformance.SitePath
	Step conformance.StepObservation
}

// JobTiming is a job's times as the API reports them (OR-17).
type JobTiming struct {
	Kind        string `json:"kind"`
	CreatedAt   string `json:"created_at,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	DurationMS  *int64 `json:"duration_ms,omitempty"`
}

// flowDef is what the fixture says about one flow body: its module list,
// and the failure and preprocessor modules of a whole flow. known is false
// where the fixture does not define the body.
type flowDef struct {
	modules      []openflow.Module
	failure      *openflow.Module
	preprocessor *openflow.Module
	known        bool
}

func bodyDef(modules []openflow.Module) flowDef { return flowDef{modules: modules, known: true} }

// containerBody is the definition of a container body inside the flow d:
// the body runs as a child flow that carries a copy of d's failure module
// (CF-1 rule 3), so its failure slot is defined exactly when d's is. A body
// has no preprocessor of its own (CF-1 rule 4). A by-path subflow is not a
// container body: it has its own definition.
func (d flowDef) containerBody(modules []openflow.Module) flowDef {
	return flowDef{modules: modules, failure: d.failure, known: true}
}

// Walker walks completed runs through the API.
type Walker struct {
	Client    *Client
	Workspace string
	Tree      *SyncTree
}

type walkState struct {
	*Walker
	ctx context.Context
	w   *Walk
}

// Walk walks the completed run of root, which ran target (a flow path, or
// a script path when script is true). The rules are those of the OR-11
// harness walker:
//
//   - a module entry without a job is not_run, and nothing is fetched;
//   - an entry with flow_jobs (even empty) is a container whose children
//     are flow_jobs[i], at i:<i> (or b:<i> for a branchall); the entry's own
//     job is ignored, because it is not a semantic field;
//   - a nil job is a module that completed without running a child: its
//     status comes from the entry type, with ran false (OR-V2);
//   - failed_retries[k] are fetched at r:<k>;
//   - a fetched job with a flow_status is a flow and is walked in turn,
//     otherwise it is a leaf, decided per job, never by kind (OR-28);
//   - a generated <id>-v module is omitted (OR-V7);
//   - the failure_module entry is kept only when the flow defines one; a
//     container body defines the enclosing flow's, whose copy it carries
//     (CF-1).
//
// Full arguments and results come from get_args and get_result_maybe for
// every job, never from the generic job endpoint (OR-V1).
func (wk *Walker) Walk(ctx context.Context, root, target string, script bool) (*Walk, error) {
	s := &walkState{Walker: wk, ctx: ctx, w: &Walk{
		RootID:  root,
		IDs:     map[string]conformance.SitePath{root: {}},
		Timings: make(map[string]JobTiming),
	}}
	j, err := s.job(conformance.SitePath{}, root)
	if err != nil {
		return nil, err
	}
	st, err := stepStatus(j)
	if err != nil {
		return nil, err
	}
	s.w.Status = conformance.RunStatus(st)
	if s.w.Result, err = s.result(root); err != nil {
		return nil, err
	}
	if j.FlowStatus == nil {
		if !script {
			return nil, fmt.Errorf("oracle: root %s of flow %s has no flow_status", root, target)
		}
		s.noteRuntimeError(j, st, s.w.Result)
		return s.w, nil
	}
	def := flowDef{}
	if f := wk.Tree.flow(target); f != nil {
		def = flowDef{modules: f.Value.Modules, failure: f.Value.FailureModule, preprocessor: f.Value.PreprocessorModule, known: true}
	}
	if err := s.flow(conformance.SitePath{}, j.FlowStatus, def); err != nil {
		return nil, err
	}
	return s.w, nil
}

func (t *SyncTree) flow(path string) *openflow.Flow {
	if t == nil {
		return nil
	}
	f := t.Flows[path]
	if f == nil || f.Value == nil {
		return nil
	}
	return f
}

// flow walks a flow job's status under base.
func (s *walkState) flow(base conformance.SitePath, fs *FlowStatus, def flowDef) error {
	if err := s.list(base, fs.Modules, def); err != nil {
		return err
	}
	for _, slot := range []struct {
		seg     conformance.Segment
		entry   *FlowStatusModule
		defined *openflow.Module
	}{
		{conformance.FailureSlot(), fs.FailureModule, def.failure},
		{conformance.PreprocessorSlot(), fs.PreprocessorModule, def.preprocessor},
	} {
		if slot.entry == nil {
			continue
		}
		hasJob := slot.entry.Job != nil
		if def.known && slot.defined == nil {
			if hasJob {
				return fmt.Errorf("oracle: %s ran a %s module the fixture does not define", describe(base), slot.seg.Value())
			}
			continue
		}
		if !def.known && !hasJob {
			continue
		}
		if err := s.module(base.Child(slot.seg), *slot.entry, slot.defined, def); err != nil {
			return err
		}
	}
	return nil
}

func describe(p conformance.SitePath) string {
	if p.IsRoot() {
		return "the root flow"
	}
	return "site " + p.String()
}

// list walks one module list.
func (s *walkState) list(base conformance.SitePath, entries []FlowStatusModule, def flowDef) error {
	user := entries
	if emptyFlow(entries, def) {
		user = nil
	} else if n := len(entries); n >= 2 && generated(entries, def) {
		user = entries[:n-1]
		if g := entries[n-1]; g.Job != nil && *g.Job != nilJob {
			s.w.IDs[*g.Job] = base.Child(conformance.Module(g.ID))
		}
	}
	ids := make([]string, len(user))
	for i, e := range user {
		ids[i] = e.ID
	}
	if def.known {
		if len(def.modules) != len(user) {
			return fmt.Errorf("oracle: %s has modules %q, but the fixture defines %d", describe(base), ids, len(def.modules))
		}
		for i, m := range def.modules {
			if m.ID != ids[i] {
				return fmt.Errorf("oracle: %s has modules %q, but the fixture's module %d is %q", describe(base), ids, i, m.ID)
			}
		}
	}
	segs := conformance.ModuleSegments(ids)
	for i, e := range user {
		var d *openflow.Module
		if def.known {
			d = &def.modules[i]
		}
		if err := s.module(base.Child(segs[i]), e, d, def); err != nil {
			return err
		}
	}
	return nil
}

// emptyFlow reports the status of a flow the fixture defines with no
// modules: it completes with its input (FX-3) and one Success entry with a
// nil job, which is no module of the flow (its id was failure on oracle run
// 40684856123).
func emptyFlow(entries []FlowStatusModule, def flowDef) bool {
	return def.known && len(def.modules) == 0 && len(entries) == 1 &&
		entries[0].Type == "Success" && entries[0].Job != nil && *entries[0].Job == nilJob
}

// generated applies the OR-V7 test to the last entry of a list: it is
// generated iff its id is the previous id plus "-v", the previous module
// sleeps or suspends, and the fixture's list has no module with its id. A
// list the fixture does not define keeps every entry.
func generated(entries []FlowStatusModule, def flowDef) bool {
	if !def.known || len(def.modules) == 0 {
		return false
	}
	n := len(entries)
	last, prev := entries[n-1], entries[n-2]
	if last.ID != prev.ID+"-v" {
		return false
	}
	for _, m := range def.modules {
		if m.ID == last.ID {
			return false
		}
	}
	p := def.modules[len(def.modules)-1]
	return p.ID == prev.ID && (p.Sleep != nil || p.Suspend != nil)
}

// moduleStatus maps a flow_status entry type to the module's status (FX-1).
func moduleStatus(typ string) (conformance.ModuleStatus, error) {
	switch typ {
	case "Success":
		return conformance.ModuleSuccess, nil
	case "Failure":
		return conformance.ModuleFailure, nil
	case "WaitingForPriorSteps":
		return conformance.ModuleNotRun, nil
	case "InProgress", "WaitingForExecutor", "WaitingForEvents":
		return conformance.ModuleInProgress, nil
	}
	return "", fmt.Errorf("oracle: unknown module status type %q", typ)
}

// stepStatus is the OR-V2 job-level status: canceled, else skipped, else
// success or failure.
func stepStatus(j Job) (conformance.StepStatus, error) {
	switch {
	case j.Type != "CompletedJob":
		return "", fmt.Errorf("oracle: job %s is %q, not completed", j.ID, j.Type)
	case j.Canceled:
		return conformance.StepCanceled, nil
	case j.IsSkipped:
		return conformance.StepSkipped, nil
	case j.Success != nil && *j.Success:
		return conformance.StepSuccess, nil
	}
	return conformance.StepFailure, nil
}

// containerStatus is the status of a module that has no job of its own:
// its entry type, with an unfinished module read as canceled, since only
// cancellation leaves one at the end of a run (FX-1).
func containerStatus(m conformance.ModuleStatus) conformance.StepStatus {
	switch m {
	case conformance.ModuleSuccess:
		return conformance.StepSuccess
	case conformance.ModuleFailure:
		return conformance.StepFailure
	case conformance.ModuleNotRun:
		return conformance.StepNotRun
	}
	return conformance.StepCanceled
}

// module walks one module entry; encl is the definition of the flow whose
// list or slot holds it.
func (s *walkState) module(site conformance.SitePath, e FlowStatusModule, def *openflow.Module, encl flowDef) error {
	ms, err := moduleStatus(e.Type)
	if err != nil {
		return err
	}
	rec := conformance.StepObservation{Module: ms}
	if e.Skipped {
		rec.Skipped = boolPtr(true)
	}
	for k, id := range e.FailedRetries {
		if err := s.attempt(site.Child(conformance.Attempt(k)), id, def); err != nil {
			return err
		}
	}
	switch {
	case e.HasFlowJobs:
		if err := s.container(site, e, def, encl, &rec); err != nil {
			return err
		}
	case e.Job == nil:
		if ms != conformance.ModuleNotRun {
			return fmt.Errorf("oracle: site %s is %s without a job", site, e.Type)
		}
		rec.Status = conformance.StepNotRun
	case *e.Job == nilJob:
		rec.Status = containerStatus(ms)
		rec.Ran = boolPtr(false)
		rec.BranchChosen = branchChosen(e.BranchChosen)
	case e.BranchChosen != nil:
		if err := s.branchOne(site, *e.Job, e.BranchChosen, def, encl, &rec); err != nil {
			return err
		}
	default:
		if _, err := s.leafOrFlow(site, *e.Job, def, &rec); err != nil {
			return err
		}
	}
	s.w.Sites = append(s.w.Sites, Site{Path: site, Step: rec})
	return nil
}

// attempt walks a failed attempt of a retried module.
func (s *walkState) attempt(site conformance.SitePath, id string, def *openflow.Module) error {
	var rec conformance.StepObservation
	if _, err := s.leafOrFlow(site, id, def, &rec); err != nil {
		return err
	}
	s.w.Sites = append(s.w.Sites, Site{Path: site, Step: rec})
	return nil
}

// leafOrFlow fetches a job at site into rec: its status, arguments and
// result, and, when it has a flow_status (a subflow), its own sites.
func (s *walkState) leafOrFlow(site conformance.SitePath, id string, def *openflow.Module, rec *conformance.StepObservation) (Job, error) {
	j, err := s.job(site, id)
	if err != nil {
		return j, err
	}
	if rec.Status, err = stepStatus(j); err != nil {
		return j, err
	}
	if rec.Args, err = s.Client.GetArgs(s.ctx, s.Workspace, id); err != nil {
		return j, err
	}
	if rec.Result, err = s.result(id); err != nil {
		return j, err
	}
	if j.FlowStatus == nil {
		s.noteRuntimeError(j, rec.Status, rec.Result)
		return j, nil
	}
	sub := flowDef{}
	if def != nil {
		if pf, ok := def.Value.(*openflow.PathFlow); ok {
			if f := s.Tree.flow(pf.Path); f != nil {
				sub = flowDef{modules: f.Value.Modules, failure: f.Value.FailureModule, preprocessor: f.Value.PreprocessorModule, known: true}
			}
		}
	}
	return j, s.flow(site, j.FlowStatus, sub)
}

// branchOne walks a branchone module whose job is the chosen branch's body
// flow. The module's record holds the body's status and result and the
// branch chosen; the body's modules are under b:<n> or b:default.
func (s *walkState) branchOne(site conformance.SitePath, id string, bc *BranchChosen, def *openflow.Module, encl flowDef, rec *conformance.StepObservation) error {
	seg := conformance.DefaultBranch()
	if bc.Type == "branch" {
		if bc.Branch == nil {
			return fmt.Errorf("oracle: site %s chose a branch without an index", site)
		}
		seg = conformance.Branch(*bc.Branch)
	}
	body := site.Child(seg)
	j, err := s.job(body, id)
	if err != nil {
		return err
	}
	if rec.Status, err = stepStatus(j); err != nil {
		return err
	}
	if rec.Result, err = s.result(id); err != nil {
		return err
	}
	rec.BranchChosen = branchChosen(bc)
	if j.FlowStatus == nil {
		return fmt.Errorf("oracle: branch body %s has no flow_status", body)
	}
	bd := flowDef{}
	if def != nil {
		if b, ok := def.Value.(*openflow.BranchOne); ok {
			if bc.Type == "default" {
				bd = encl.containerBody(b.Default)
			} else if *bc.Branch < len(b.Branches) {
				bd = encl.containerBody(b.Branches[*bc.Branch].Modules)
			}
		}
	}
	return s.flow(body, j.FlowStatus, bd)
}

// container walks a loop or branchall: one child per flow_jobs entry, at
// i:<n> or b:<n>. A child with a flow_status is a body flow; a child
// without one is a collapsed iteration, the body module's own job, at
// i:<n>/m:<body id> as a non-collapsed iteration would be (ADR 0011). The
// module's result is the aggregate of the children's results in index
// order; with no children it did not run one (OR-26, OR-V2).
func (s *walkState) container(site conformance.SitePath, e FlowStatusModule, def *openflow.Module, encl flowDef, rec *conformance.StepObservation) error {
	branchAll := e.BranchAll != nil
	var bodies [][]openflow.Module
	if def != nil {
		switch v := def.Value.(type) {
		case *openflow.ForLoop:
			bodies = [][]openflow.Module{v.Modules}
		case *openflow.WhileLoop:
			bodies = [][]openflow.Module{v.Modules}
		case *openflow.BranchAll:
			// A completed branchall entry need not carry the branchall
			// field (oracle run 40684856123), so the fixture's definition
			// decides.
			branchAll = true
			for _, b := range v.Branches {
				bodies = append(bodies, b.Modules)
			}
		}
	}
	body := func(i int) flowDef {
		switch {
		case branchAll && i < len(bodies):
			return encl.containerBody(bodies[i])
		case !branchAll && len(bodies) == 1:
			return encl.containerBody(bodies[0])
		}
		return flowDef{}
	}
	var agg bytes.Buffer
	agg.WriteByte('[')
	for i, id := range e.FlowJobs {
		seg := conformance.Iteration(i)
		if branchAll {
			seg = conformance.Branch(i)
		}
		child := site.Child(seg)
		r, err := s.result(id)
		if err != nil {
			return err
		}
		if i > 0 {
			agg.WriteByte(',')
		}
		agg.Write(r)
		j, err := s.Client.GetJob(s.ctx, s.Workspace, id)
		if err != nil {
			return err
		}
		if j.FlowStatus != nil {
			s.record(child, id, j)
			if err := s.flow(child, j.FlowStatus, body(i)); err != nil {
				return err
			}
			continue
		}
		bd := body(i)
		if !bd.known || len(bd.modules) != 1 {
			return fmt.Errorf("oracle: iteration %s is a leaf, but the fixture does not define a one-module body", child)
		}
		leaf := child.Child(conformance.Module(bd.modules[0].ID))
		var lr conformance.StepObservation
		if _, err := s.leafOrFlow(leaf, id, &bd.modules[0], &lr); err != nil {
			return err
		}
		s.w.Sites = append(s.w.Sites, Site{Path: leaf, Step: lr})
	}
	agg.WriteByte(']')
	rec.Status = containerStatus(rec.Module)
	rec.Result = json.RawMessage(agg.Bytes())
	if len(e.FlowJobs) == 0 {
		rec.Ran = boolPtr(false)
	}
	return nil
}

// job fetches a job's status and topology and maps its id to site.
func (s *walkState) job(site conformance.SitePath, id string) (Job, error) {
	j, err := s.Client.GetJob(s.ctx, s.Workspace, id)
	if err != nil {
		return j, err
	}
	s.record(site, id, j)
	return j, nil
}

// record maps id to site, unless an earlier site already holds it: a
// failure module's Success status lists the flow's failed attempts again
// (FX-27), and such a job keeps the attempt site of the module it belongs
// to, which the walk reaches first, for its placeholder.
func (s *walkState) record(site conformance.SitePath, id string, j Job) {
	if _, ok := s.w.IDs[id]; !ok {
		s.w.IDs[id] = site
	}
	s.w.Timings[site.String()] = JobTiming{Kind: j.JobKind, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, CompletedAt: j.CompletedAt, DurationMS: j.DurationMS}
}

// result returns a job's full stored result (OR-V1).
func (s *walkState) result(id string) (json.RawMessage, error) {
	r, err := s.Client.ResultMaybe(s.ctx, s.Workspace, id)
	if err != nil {
		return nil, err
	}
	if !r.Completed {
		return nil, fmt.Errorf("oracle: job %s has not completed", id)
	}
	if r.Result == nil {
		return json.RawMessage("null"), nil
	}
	return r.Result, nil
}

// scriptKinds are the job kinds that execute a script (OR-V3 clause a).
var scriptKinds = map[string]bool{"script": true, "flowscript": true, "preview": true}

// noteRuntimeError records the stored result of a failed leaf that executed
// a script, when it has the shape {"error":{…}} (OR-V3 clause a).
func (s *walkState) noteRuntimeError(j Job, st conformance.StepStatus, result json.RawMessage) {
	if st != conformance.StepFailure || !scriptKinds[j.JobKind] {
		return
	}
	var v struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if json.Unmarshal(result, &v) == nil && v.Error != nil {
		s.w.RuntimeErrors = append(s.w.RuntimeErrors, result)
	}
}

func branchChosen(bc *BranchChosen) *conformance.BranchChosen {
	if bc == nil {
		return nil
	}
	return &conformance.BranchChosen{Type: bc.Type, Branch: bc.Branch}
}

func boolPtr(b bool) *bool { return &b }

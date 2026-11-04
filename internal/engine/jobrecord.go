package engine

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// Job records ([WA-19]): a run renders the record that GET
// jobs_u/get/{id} answers for each job of its tree while the run is live,
// from its own state, which the job registry reaches through the run's
// registration (platform.Job.View). The run indexes each job it starts
// (run.jobs) with a reference to where its state lives: a flow job's
// frame, a leaf job's record in the trace. Nothing is copied, so a run
// that no step asks about pays only for the index.
//
// A record has the fields that windmill-lite serves exactly, as the oracle
// recorded them (fixture conformance/flows/client-api/job-reads): type,
// id, parent_job, root_job, running and flow_step_id while the job runs,
// canceled, success and result once it has completed, args, script_path,
// is_flow_step, language, the workspace and the user the run is
// permissioned as, and a flow job's flow_status. The other fields of
// Windmill's record are left out; WA-19 names each with its reason.
//
// [WA-19]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-19

// nilJob is the job id of a module that completed without running a job
// (OR-V2).
const nilJob = "00000000-0000-0000-0000-000000000000"

// jobRef is what a run knows of one job of its tree: a flow job (the root
// or a child flow) by its frame, or a leaf job by its frame, its module and
// its record. A leaf's record is the trace's, which the run fills as the
// job runs and completes.
type jobRef struct {
	fr  *frame
	i   int // a leaf's module, at cursor i of fr
	idx int // a leaf's record in the trace; -1 for a flow job
	// identity marks a leaf job that runs no script: the identity job of
	// a skipped, mocked or identity module (FX-16), which Windmill records
	// with no script_path or language.
	identity bool
	// end is a child flow's end, once it has ended.
	end *flowEnd
}

// indexFlow indexes the flow job of fr. Called with mu held.
func (r *run) indexFlow(fr *frame) {
	if r.jobs != nil {
		r.jobs[fr.id] = &jobRef{fr: fr, idx: -1}
	}
}

// indexLeaf indexes the leaf job id of module i of fr, whose record is
// idx. Called with mu held.
func (r *run) indexLeaf(id string, fr *frame, i, idx int, identity bool) {
	if r.jobs != nil && id != "" {
		r.jobs[id] = &jobRef{fr: fr, i: i, idx: idx, identity: identity}
	}
}

// childEnded records the end of fr, a child flow, which its record shows.
func (r *run) childEnded(fr *frame, e flowEnd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ref := r.jobs[fr.id]; ref != nil {
		ref.end = &e
	}
}

// jobLaunched records, in module i of fr's record idx, the job its
// invocation runs and the arguments as stored, so that a record of the
// running job and its flow's status name it.
func (r *run) jobLaunched(fr *frame, i, idx int, id string, stored jsonval.Value) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := &r.trace.Steps[idx]
	rec.Job, rec.RecordedArgs = id, stored
	r.indexLeaf(id, fr, i, idx, false)
}

// noInvocationJob gives o, a module of fr at cursor i whose record is idx
// and that ran a job with no invocation, the id of that job: an identity
// job (FX-16), or a script job that failed before its invocation (an input
// transform's failure, FX-14; no entrypoint, WR-12; a cancellation while it
// was scheduled, FX-38). Windmill gives every such job an id, which the
// flow's status names. The job is registered with the run's tree, so that
// the job registry finds it.
func (r *run) noInvocationJob(fr *frame, i, idx int, o *outcome) {
	m := fr.moduleAt(i)
	mocked := m.Mock != nil && m.Mock.Enabled
	switch {
	case !o.rec.Ran || o.rec.Job != "" || o.rec.Status == "":
		return
	case m.Kind == ir.KindSubflow || m.Kind == ir.KindProvided:
		// A flow step's job is its subflow's flow job; a provided site's
		// target is the host's.
		return
	case isContainer(m.Kind) && !o.rec.Skipped && !mocked:
		// A container that ran has no job of its own (CF-1).
		return
	}
	id := newUUID()
	o.rec.NoInvocationJob = id
	// A script job that failed before its invocation is still a script's
	// job; any other job that ran with no invocation is an identity job.
	launched := m.Leaf != nil && !mocked && !o.rec.Skipped && o.rec.Status == conformance.StepFailure
	r.mu.Lock()
	r.indexLeaf(id, fr, i, idx, !launched)
	r.mu.Unlock()
	r.e.platform.JobRegistry().Begin(platform.Job{ID: id, Kind: platform.JobScript, Root: r.rootID, Run: r.id})()
}

// JobRecord renders the record of the job id names (platform.JobView).
func (v runView) JobRecord(id string) ([]byte, error) {
	r := v.r
	// The user state is read before the run's lock: its service takes
	// locks of its own.
	states, hasStates, err := r.platform.FlowUserStates(context.Background(), id)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, err := r.lookupJob(id)
	if err != nil {
		return nil, err
	}
	x := newRecordIndex(r)
	if ref.idx < 0 {
		var us json.RawMessage
		if hasStates {
			us = states
		}
		return x.flowRecord(id, ref, us)
	}
	return x.leafRecord(id, ref)
}

// JobArgs renders the stored arguments of the job id names
// (platform.JobView): a leaf's args, a flow job's input.
func (v runView) JobArgs(id string) ([]byte, error) {
	r := v.r
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, err := r.lookupJob(id)
	if err != nil {
		return nil, err
	}
	if ref.idx < 0 {
		return databaseText(recordText(ref.fr.input)), nil
	}
	return databaseText(recordText(r.trace.Steps[ref.idx].RecordedArgs)), nil
}

// lookupJob returns the run's reference to the job id names. A run that
// is its own root and has ended answers for none of its jobs (D22), even
// while a registration of one of them lingers until its process settles.
// Called with mu held.
func (r *run) lookupJob(id string) (*jobRef, error) {
	if r.rootEnd != nil && r.rootID == r.id {
		return nil, platform.ErrEndedRun
	}
	ref := r.jobs[id]
	if ref == nil {
		return nil, &platform.NotServed{Reason: "the job's run does not hold it"}
	}
	return ref, nil
}

// runView is a run as the job registry reaches it.
type runView struct{ r *run }

// recordIndex is the run's trace indexed for one record: the latest record
// of each site, and the latest child flow at each site, Windmill's job tree
// no longer reaching a superseded one.
type recordIndex struct {
	r        *run
	records  map[conformance.SitePath]int
	children map[conformance.SitePath]string
}

func newRecordIndex(r *run) *recordIndex {
	x := &recordIndex{r: r, records: map[conformance.SitePath]int{}, children: map[conformance.SitePath]string{}}
	for k, rec := range r.trace.Steps {
		if !rec.Superseded {
			x.records[rec.Site] = k
		}
	}
	for _, c := range r.trace.Children {
		if !c.Superseded {
			x.children[c.Site] = c.ID
		}
	}
	return x
}

// leafRecord is the record of a leaf job.
func (x *recordIndex) leafRecord(id string, ref *jobRef) ([]byte, error) {
	r, fr := x.r, ref.fr
	rec := r.trace.Steps[ref.idx]
	done := rec.Status != ""
	if rec.Status == conformance.StepCanceled {
		return nil, &platform.NotServed{Reason: "the job was canceled"}
	}
	in := fr.owner()
	var o object
	o.field("type", jsonText(recordType(done)))
	o.field("id", jsonText(id))
	o.field("parent_job", jsonText(in.id))
	if !done {
		o.field("root_job", jsonText(in.scope))
		o.field("running", []byte("true"))
		o.field("flow_step_id", jsonText(fr.stepID(ref.i)))
	}
	o.field("canceled", []byte("false"))
	if done {
		o.field("success", jsonBool(rec.Status == conformance.StepSuccess))
		o.field("result", databaseText(recordText(rec.Result)))
	}
	o.field("args", argsText(rec.RecordedArgs))
	if !ref.identity {
		o.field("script_path", jsonText(r.jobPath(fr, ref.i)))
		if s := r.e.prog.Scripts[fr.moduleAt(ref.i).Leaf.Script]; s != nil {
			o.field("language", jsonText(string(s.Language)))
		}
	}
	o.field("is_flow_step", []byte("true"))
	x.host(&o)
	return o.close(), nil
}

// flowRecord is the record of a flow job: the root, or a child flow.
func (x *recordIndex) flowRecord(id string, ref *jobRef, states json.RawMessage) ([]byte, error) {
	r, fr := x.r, ref.fr
	end := ref.end
	if fr == r.root {
		end = r.rootEnd
	}
	done := end != nil
	if done && end.status != conformance.RunSuccess && end.status != conformance.RunFailure {
		return nil, &platform.NotServed{Reason: "the flow job ended " + string(end.status)}
	}
	if fr == r.root && r.f.PreprocessorModule != nil {
		return nil, &platform.NotServed{Reason: "a flow with a preprocessor module"}
	}
	var o object
	o.field("type", jsonText(recordType(done)))
	o.field("id", jsonText(id))
	parent := fr.parentJob
	if fr == r.root {
		parent = r.flowJobEnv["WM_FLOW_JOB_ID"]
	}
	if parent != "" {
		o.field("parent_job", jsonText(parent))
	}
	if !done {
		// The stored innermost scope: unset for a flow that starts its own
		// (the root, a subflow, a parallel iteration), else its parent's
		// (WA-V1).
		if fr.scope != fr.id {
			o.field("root_job", jsonText(fr.scope))
		}
		o.field("running", []byte("true"))
		if fr.moduleID != "" {
			o.field("flow_step_id", jsonText(fr.moduleID))
		}
	}
	o.field("canceled", []byte("false"))
	if done {
		o.field("success", jsonBool(end.status == conformance.RunSuccess))
		o.field("result", databaseText(recordText(end.result)))
	}
	o.field("args", argsText(fr.input))
	o.field("script_path", jsonText(fr.path))
	o.field("is_flow_step", jsonBool(fr != r.root))
	x.host(&o)
	o.field("flow_status", x.flowStatus(fr, end, states))
	return o.close(), nil
}

// host writes the fields that the run's host configuration gives every job
// of the run: its workspace, and the user it is permissioned as (ADR 0007,
// job identity).
func (x *recordIndex) host(o *object) {
	env := x.r.flowJobEnv
	o.field("workspace_id", jsonText(env["WM_WORKSPACE"]))
	o.field("created_by", jsonText(env["WM_USERNAME"]))
	o.field("email", jsonText(env["WM_EMAIL"]))
	o.field("permissioned_as", jsonText(env["WM_PERMISSIONED_AS"]))
}

// flowStatus is the flow_status of fr's flow job, which ended with end
// (nil while it runs) and holds the user state states (nil for none).
func (x *recordIndex) flowStatus(fr *frame, end *flowEnd, states json.RawMessage) []byte {
	n := len(fr.modules)
	last := -1 // the last module that started
	mods := make([][]byte, n)
	for k := range n {
		if _, ok := x.records[fr.sites[k]]; ok {
			last = k
		}
		mods[k] = x.entry(fr, k, "")
	}
	failureSite := fr.sites[n]
	fidx, failureRan := x.records[failureSite]
	var failure []byte
	switch {
	case failureRan:
		parent := ""
		if last >= 0 {
			parent = fr.modules[last].ID
		}
		failure = x.entry(fr, n, parent)
	case fr.failure != nil:
		failure = waiting(fr.failure.ID)
	default:
		failure = waiting("failure")
	}
	// step is the cursor of the flow's state machine: the module that
	// runs; once the flow has completed, the module after the last, or
	// the one that failed it; one past the module list once the failure
	// module has completed; 0 for a flow its cache answered.
	step := max(last, 0)
	switch {
	case end != nil && end.cached:
		step = 0
	case failureRan && x.r.trace.Steps[fidx].Status != "":
		step = n + 1
	case failureRan:
		step = n
	case end != nil && end.status == conformance.RunSuccess:
		step = last + 1
	}
	var o object
	o.field("step", []byte(strconv.Itoa(step)))
	o.field("modules", jsonArray(mods))
	o.field("failure_module", failure)
	o.field("preprocessor_module", []byte("null"))
	o.field("cleanup_module", []byte("{}"))
	if states != nil {
		o.field("user_states", states)
	}
	return o.close()
}

// waiting is the status entry of a module that has not started.
func waiting(id string) []byte {
	var o object
	o.field("id", jsonText(id))
	o.field("type", jsonText("WaitingForPriorSteps"))
	return o.close()
}

// entry is the status entry of the module at cursor k of fr; parent names
// the module whose failure the failure module handles, for its entry.
func (x *recordIndex) entry(fr *frame, k int, parent string) []byte {
	m := fr.moduleAt(k)
	site := fr.sites[k]
	idx, ok := x.records[site]
	if !ok {
		return waiting(m.ID)
	}
	rec := x.r.trace.Steps[idx]
	done := rec.Status != ""
	typ := "InProgress"
	if done {
		switch rec.Module {
		case conformance.ModuleSuccess:
			typ = "Success"
		case conformance.ModuleFailure:
			typ = "Failure"
		}
	}
	var o object
	o.field("id", jsonText(m.ID))
	o.field("type", jsonText(typ))
	switch {
	case rec.NoInvocationJob != "":
		o.field("job", jsonText(rec.NoInvocationJob))
	case m.Kind == ir.KindForLoop || m.Kind == ir.KindWhileLoop:
		x.loopEntry(&o, fr, k, rec, done)
	case m.Kind == ir.KindBranchOne:
		x.branchOneEntry(&o, fr, k, rec, done)
	case m.Kind == ir.KindBranchAll:
		x.branchAllEntry(&o, fr, k, done)
	case m.Kind == ir.KindSubflow:
		if id := x.children[site]; id != "" {
			o.field("job", jsonText(id))
		}
	case rec.Job != "":
		o.field("job", jsonText(rec.Job))
	}
	if typ == "Success" {
		o.field("skipped", jsonBool(rec.Skipped))
	}
	if done && len(rec.FailedRetries) > 0 {
		ids := make([][]byte, 0, len(rec.FailedRetries))
		for _, s := range rec.FailedRetries {
			if i, ok := x.records[s]; ok {
				ids = append(ids, jsonText(jobOf(x.r.trace.Steps[i])))
			}
		}
		o.field("failed_retries", jsonArray(ids))
	}
	if parent != "" {
		o.field("parent_module", jsonText(parent))
	}
	return o.close()
}

// jobOf is the id of a record's job: its invocation's, or the one it ran
// with none.
func jobOf(rec StepRecord) string {
	if rec.Job != "" {
		return rec.Job
	}
	return rec.NoInvocationJob
}

// child is the job and the status of a container's child at site: an
// iteration's or a branch's child flow, or a collapsed iteration's job.
// ok is false when the child has not started.
type child struct {
	id      string
	success []byte // true, false, or null while it runs
}

// flowChild is the child flow at site, if one started.
func (x *recordIndex) flowChild(site conformance.SitePath) (child, bool) {
	id, ok := x.children[site]
	if !ok {
		return child{}, false
	}
	c := child{id: id, success: []byte("null")}
	if ref := x.r.jobs[id]; ref != nil && ref.end != nil {
		c.success = jsonBool(ref.end.status == conformance.RunSuccess)
	}
	return c, true
}

// loopEntry writes a for or a while loop's own fields: its children's
// jobs and their success, the last child's job, and, while it runs, its
// iterator and whether it is parallel or a while loop.
func (x *recordIndex) loopEntry(o *object, fr *frame, k int, rec StepRecord, done bool) {
	m := fr.moduleAt(k)
	site := fr.sites[k]
	body := bodyOf(m)
	var kids []child
	for j := 0; ; j++ {
		it := site.Child(conformance.Iteration(j))
		if body.collapse {
			idx, ok := x.records[it.Child(conformance.Module(body.modules[0].ID))]
			if !ok {
				break
			}
			b := x.r.trace.Steps[idx]
			c := child{id: jobOf(b), success: []byte("null")}
			if b.Status != "" {
				c.success = jsonBool(b.Status == conformance.StepSuccess)
			}
			kids = append(kids, c)
			continue
		}
		c, ok := x.flowChild(it)
		if !ok {
			break
		}
		kids = append(kids, c)
	}
	x.writeChildren(o, kids)
	if done {
		return
	}
	n := 0
	if m.Kind == ir.KindForLoop {
		n = x.r.iterLen[site]
	}
	var it object
	it.field("index", []byte(strconv.Itoa(max(len(kids)-1, 0))))
	it.field("itered_len", []byte(strconv.Itoa(n)))
	o.field("iterator", it.close())
	switch {
	case m.Kind == ir.KindWhileLoop:
		o.field("while_loop", []byte("true"))
	case m.ForLoop.Parallel:
		o.field("parallel", []byte("true"))
	}
}

// writeChildren writes a container's children: flow_jobs, flow_jobs_success
// and the last child as its job.
func (x *recordIndex) writeChildren(o *object, kids []child) {
	ids := make([][]byte, len(kids))
	ok := make([][]byte, len(kids))
	for i, c := range kids {
		ids[i], ok[i] = jsonText(c.id), c.success
	}
	if len(kids) > 0 {
		o.field("job", ids[len(ids)-1])
	}
	o.field("flow_jobs", jsonArray(ids))
	o.field("flow_jobs_success", jsonArray(ok))
}

// branchOneEntry writes a branchone's own fields: the branch it chose and
// that branch's child flow as its job.
func (x *recordIndex) branchOneEntry(o *object, fr *frame, k int, rec StepRecord, done bool) {
	m := fr.moduleAt(k)
	site := fr.sites[k]
	segs := make([]conformance.Segment, 0, len(m.BranchOne.Branches)+1)
	for b := range m.BranchOne.Branches {
		segs = append(segs, conformance.Branch(b))
	}
	segs = append(segs, conformance.DefaultBranch())
	for n, seg := range segs {
		c, ok := x.flowChild(site.Child(seg))
		if !ok {
			continue
		}
		var bc object
		if n < len(m.BranchOne.Branches) {
			bc.field("branch", []byte(strconv.Itoa(n)))
			bc.field("type", jsonText("branch"))
		} else {
			bc.field("type", jsonText("default"))
		}
		o.field("branch_chosen", bc.close())
		o.field("job", jsonText(c.id))
		return
	}
	if done && fr.nilJob[k] {
		// A branchone that ran no branch completed with a nil job (CF-14).
		o.field("job", jsonText(nilJob))
	}
}

// branchAllEntry writes a branchall's own fields: its branches' child
// flows and their success, the last as its job, and, while it runs, the
// branch that runs and how many there are.
func (x *recordIndex) branchAllEntry(o *object, fr *frame, k int, done bool) {
	m := fr.moduleAt(k)
	site := fr.sites[k]
	var kids []child
	for b := range m.BranchAll.Branches {
		c, ok := x.flowChild(site.Child(conformance.Branch(b)))
		if !ok {
			break
		}
		kids = append(kids, c)
	}
	x.writeChildren(o, kids)
	if done {
		return
	}
	var ba object
	ba.field("branch", []byte(strconv.Itoa(max(len(kids)-1, 0))))
	ba.field("len", []byte(strconv.Itoa(len(m.BranchAll.Branches))))
	o.field("branchall", ba.close())
}

func recordType(done bool) string {
	if done {
		return "CompletedJob"
	}
	return "QueuedJob"
}

// recordText is a value's text as the job's stored column gives it back,
// or null for none.
func recordText(v jsonval.Value) []byte {
	if v.IsZero() {
		return []byte("null")
	}
	if s, err := v.Stored(); err == nil {
		return s
	}
	return v.Producer()
}

func jsonText(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

func jsonBool(b bool) []byte {
	if b {
		return []byte("true")
	}
	return []byte("false")
}

func jsonArray(items [][]byte) []byte {
	b := []byte{'['}
	for i, it := range items {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, it...)
	}
	return append(b, ']')
}

// object writes a JSON object's members in the order they are added.
type object struct{ b []byte }

func (o *object) field(k string, v []byte) {
	if o.b == nil {
		o.b = []byte{'{'}
	} else {
		o.b = append(o.b, ',')
	}
	o.b = append(o.b, jsonText(k)...)
	o.b = append(o.b, ':')
	o.b = append(o.b, v...)
}

func (o *object) close() []byte {
	if o.b == nil {
		return []byte("{}")
	}
	return append(o.b, '}')
}

// scriptView renders the record of a script run (Engine.RunScript): a job
// a step dispatched. It holds the run's job variables, its script and its
// stored arguments, and its outcome once it has completed.
type scriptView struct {
	id     string
	env    map[string]string
	script *ir.Script
	args   jsonval.Value

	mu      sync.Mutex
	done    bool
	success bool
	result  jsonval.Value
}

// complete records the run's outcome: res, unless err or a latch ended it
// without one.
func (v *scriptView) complete(res ScriptResult, err error, latched bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.done, v.success, v.result = true, err == nil && !latched && !res.Failed, res.Result
}

// JobArgs renders the script run's stored arguments (platform.JobView).
func (v *scriptView) JobArgs(id string) ([]byte, error) {
	if err := v.check(id); err != nil {
		return nil, err
	}
	return databaseText(recordText(v.args)), nil
}

// check fails a read of a job the script run does not hold, and of its job
// once it has completed as a run of its own, whose run has then ended
// (D22).
func (v *scriptView) check(id string) error {
	if id != v.id {
		return &platform.NotServed{Reason: "the job's run does not hold it"}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.done && v.env["WM_ROOT_JOB_ID"] == v.id {
		return platform.ErrEndedRun
	}
	return nil
}

// JobRecord renders the script run's record (platform.JobView).
func (v *scriptView) JobRecord(id string) ([]byte, error) {
	if err := v.check(id); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var o object
	o.field("type", jsonText(recordType(v.done)))
	o.field("id", jsonText(id))
	if p := v.env["WM_FLOW_JOB_ID"]; p != "" {
		o.field("parent_job", jsonText(p))
	}
	if !v.done {
		// The innermost scope the dispatch stored, its root_job (WA-V1).
		if s := v.env["WM_ROOT_FLOW_JOB_ID"]; s != "" {
			o.field("root_job", jsonText(s))
		}
		o.field("running", []byte("true"))
	}
	o.field("canceled", []byte("false"))
	if v.done {
		o.field("success", jsonBool(v.success))
		o.field("result", databaseText(recordText(v.result)))
	}
	o.field("args", argsText(v.args))
	o.field("script_path", jsonText(v.script.Path))
	o.field("language", jsonText(string(v.script.Language)))
	o.field("is_flow_step", []byte("false"))
	o.field("workspace_id", jsonText(v.env["WM_WORKSPACE"]))
	o.field("created_by", jsonText(v.env["WM_USERNAME"]))
	o.field("email", jsonText(v.env["WM_EMAIL"]))
	o.field("permissioned_as", jsonText(v.env["WM_PERMISSIONED_AS"]))
	return o.close(), nil
}

// argsText is a job's arguments as GET jobs_u/get/{id} writes them, as
// the oracle recorded them (WA-19): a compact object whose values are each
// jsonb's text output of the stored value (`{"cfg":{"k": [1, 2.50]}}`);
// Windmill reads the stored arguments into a map of raw values and writes
// the map out again.
func argsText(v jsonval.Value) []byte {
	stored := recordText(v)
	out := []byte{'{'}
	_, ok := jsonval.Members(stored, func(_ string, ks, ke, vs, ve int) {
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, stored[ks:ke]...)
		out = append(out, ':')
		out = append(out, databaseText(stored[vs:ve])...)
	})
	if !ok {
		return databaseText(stored)
	}
	return append(out, '}')
}

// databaseText is jsonb's text output of stored, a stored text: a space
// after each `:` and `,` outside strings, as GET jobs_u/get_args/{id}
// answers a job's arguments (WA-19).
func databaseText(stored []byte) []byte {
	out := make([]byte, 0, len(stored)+len(stored)/8)
	in, esc := false, false
	for _, c := range stored {
		out = append(out, c)
		switch {
		case esc:
			esc = false
		case in && c == '\\':
			esc = true
		case c == '"':
			in = !in
		case !in && (c == ':' || c == ','):
			out = append(out, ' ')
		}
	}
	return out
}

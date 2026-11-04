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
	// end is a child flow's end, once it has ended, and ended its place
	// among the ends of the run's child flows, from 1.
	end   *flowEnd
	ended uint64
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

// attemptStarted records that module i of fr starts an attempt whose
// record is idx: the module's current record, with no child yet, and one
// of the records of fr's flow. Called with mu held.
func (r *run) attemptStarted(fr *frame, i, idx int) {
	fr.recs[i] = idx
	if fr.kids != nil {
		fr.kids[i] = nil
	}
	own := fr.owner()
	own.owned = append(own.owned, idx)
}

// addKid records c as the child at slot of the current attempt of module i
// of fr, or removes the child there when c is nil. Called with mu held.
func (fr *frame) addKid(i, slot int, c *frame) {
	if fr.kids == nil {
		fr.kids = make([][]*frame, len(fr.recs))
	}
	ks := fr.kids[i]
	for len(ks) <= slot {
		ks = append(ks, nil)
	}
	ks[slot] = c
	fr.kids[i] = ks
}

// childEnded records the end of fr, a child flow, which its record shows.
func (r *run) childEnded(fr *frame, e flowEnd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ref := r.jobs[fr.id]; ref != nil {
		r.ends++
		ref.end, ref.ended = &e, r.ends
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
	case m.Kind == ir.KindSubflow && !o.rec.Skipped && !mocked, m.Kind == ir.KindProvided:
		// A flow step's job is its subflow's flow job, unless skip_if or a
		// mock replaced it with an identity job; a provided site's target
		// is the host's.
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
	x := &recordView{r: r}
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

// recordView renders the records of a run's jobs. Each flow job's status
// comes from its own frame: the current attempt of each of its modules
// (frame.recs), the child flows and collapsed iterations of that attempt
// (frame.kids), and the attempts its modules list as failed retries,
// among the records the flow owns (frame.owned). So a flow job of an
// attempt that a retry superseded still shows what it ran, though the
// retry runs at the same sites. The run's mu is held.
type recordView struct {
	r *run
	// err is set when an entry shows a state that Windmill's record has
	// not been seen in, which the record is then not served in.
	err error
}

// notServed records that the record shows a state reason names, which it
// is not served in.
func (x *recordView) notServed(reason string) {
	if x.err == nil {
		x.err = &platform.NotServed{Reason: reason}
	}
}

// leafRecord is the record of a leaf job.
func (x *recordView) leafRecord(id string, ref *jobRef) ([]byte, error) {
	r, fr := x.r, ref.fr
	rec := r.trace.Steps[ref.idx]
	done := rec.Status != ""
	waits := r.sched[ref.idx].waiting
	if rec.Status == conformance.StepCanceled {
		return nil, &platform.NotServed{Reason: "the job was canceled"}
	}
	result, args := tooBig(done, recordText(rec.Result), recordText(rec.RecordedArgs))
	in := fr.owner()
	var o object
	o.field("type", jsonText(recordType(done)))
	o.field("id", jsonText(id))
	o.field("parent_job", jsonText(in.id))
	if !done {
		o.field("root_job", jsonText(in.scope))
		// A job pushed for later has not started (jobScheduled).
		o.field("running", jsonBool(!waits))
		o.field("flow_step_id", jsonText(fr.stepID(ref.i)))
	}
	o.field("canceled", []byte("false"))
	if done {
		o.field("success", jsonBool(rec.Status == conformance.StepSuccess))
		o.field("result", databaseText(result))
	}
	o.field("args", argsText(args))
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
func (x *recordView) flowRecord(id string, ref *jobRef, states json.RawMessage) ([]byte, error) {
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
	var result []byte
	if done {
		result = recordText(end.result)
	}
	result, args := tooBig(done, result, recordText(fr.input))
	status, err := x.flowStatus(fr, end, states)
	if err != nil {
		return nil, err
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
		// A flow job pushed for later has not started (frame.scheduled).
		o.field("running", jsonBool(!fr.scheduled))
		if fr.moduleID != "" {
			o.field("flow_step_id", jsonText(fr.moduleID))
		}
	}
	o.field("canceled", []byte("false"))
	if done {
		o.field("success", jsonBool(end.status == conformance.RunSuccess))
		o.field("result", databaseText(result))
	}
	o.field("args", argsText(args))
	o.field("script_path", jsonText(fr.path))
	o.field("is_flow_step", jsonBool(fr != r.root))
	x.host(&o)
	o.field("flow_status", status)
	return o.close(), nil
}

// host writes the fields that the run's host configuration gives every job
// of the run: its workspace, and the user it is permissioned as (ADR 0007,
// job identity).
func (x *recordView) host(o *object) {
	env := x.r.flowJobEnv
	o.field("workspace_id", jsonText(env["WM_WORKSPACE"]))
	o.field("created_by", jsonText(env["WM_USERNAME"]))
	o.field("email", jsonText(env["WM_EMAIL"]))
	o.field("permissioned_as", jsonText(env["WM_PERMISSIONED_AS"]))
}

// flowStatus is the flow_status of fr's flow job, which ended with end
// (nil while it runs) and holds the user state states (nil for none).
func (x *recordView) flowStatus(fr *frame, end *flowEnd, states json.RawMessage) ([]byte, error) {
	n := len(fr.modules)
	last := -1 // the last module that started
	mods := make([][]byte, n)
	if fr.waiting > 0 {
		return nil, &platform.NotServed{Reason: "a module of the flow waits for a sleep or a retry delay"}
	}
	for k := range n {
		if fr.recs[k] >= 0 {
			last = k
		}
		mods[k] = x.entry(fr, k, "")
	}
	fidx := fr.recs[n]
	failureRan := fidx >= 0
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
	if x.err != nil {
		return nil, x.err
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
	return o.close(), nil
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
func (x *recordView) entry(fr *frame, k int, parent string) []byte {
	m := fr.moduleAt(k)
	idx := fr.recs[k]
	if idx < 0 {
		return waiting(m.ID)
	}
	rec := x.r.trace.Steps[idx]
	done := rec.Status != ""
	typ := "InProgress"
	if x.r.sched[idx].waiting {
		// Its job is pushed, scheduled for when a sleep or a retry delay
		// ends (jobScheduled).
		typ = "WaitingForExecutor"
	}
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
		x.loopEntry(&o, fr, k, rec)
	case m.Kind == ir.KindBranchOne:
		x.branchOneEntry(&o, fr, k, rec)
	case m.Kind == ir.KindBranchAll:
		x.branchAllEntry(&o, fr, k, rec)
	case m.Kind == ir.KindSubflow && fr.kids != nil && len(fr.kids[k]) > 0 && fr.kids[k][0] != nil:
		// A flow step's job is its subflow's, its record naming it only
		// once the step has completed.
		o.field("job", jsonText(fr.kids[k][0].id))
	case rec.Job != "":
		o.field("job", jsonText(rec.Job))
	}
	if typ == "Success" {
		o.field("skipped", jsonBool(rec.Skipped))
	}
	if done && len(rec.FailedRetries) > 0 {
		ids := make([][]byte, 0, len(rec.FailedRetries))
		for _, s := range rec.FailedRetries {
			if i := x.owned(fr, s); i >= 0 {
				id := jobOf(x.r.trace.Steps[i])
				if id == "" {
					id = fr.attempts[i]
				}
				ids = append(ids, jsonText(id))
			}
		}
		o.field("failed_retries", jsonArray(ids))
	}
	if parent != "" {
		o.field("parent_module", jsonText(parent))
	}
	return o.close()
}

// owned is the latest of fr's records at site, or -1.
func (x *recordView) owned(fr *frame, site conformance.SitePath) int {
	for k := len(fr.owned) - 1; k >= 0; k-- {
		if i := fr.owned[k]; x.r.trace.Steps[i].Site == site {
			return i
		}
	}
	return -1
}

// jobOf is the id of a record's job: its invocation's, or the one it ran
// with none.
func jobOf(rec StepRecord) string {
	if rec.Job != "" {
		return rec.Job
	}
	return rec.NoInvocationJob
}

// child is the job and the status of a container's child: an iteration's
// or a branch's child flow, or a collapsed iteration's job; ended orders
// the ends of a run's child flows, 0 while the child runs.
type child struct {
	id       string
	success  []byte // true, false, or null while it runs
	ended    uint64
	released bool
}

// children are the children of the current attempt of module k of fr, in
// slot order, up to the first that has not started.
func (x *recordView) children(fr *frame, k int) []child {
	if fr.kids == nil {
		return nil
	}
	var out []child
	for _, c := range fr.kids[k] {
		if c == nil {
			break
		}
		if c.collapsed != nil {
			idx := c.recs[0]
			if idx < 0 {
				break
			}
			b := x.r.trace.Steps[idx]
			ch := child{id: jobOf(b), success: []byte("null")}
			if b.Status != "" {
				ch.success = jsonBool(b.Status == conformance.StepSuccess)
			}
			out = append(out, ch)
			continue
		}
		ch := child{id: c.id, success: []byte("null"), released: c.released}
		if ref := x.r.jobs[c.id]; ref != nil && ref.end != nil {
			ch.success, ch.ended = jsonBool(ref.end.status == conformance.RunSuccess), ref.ended
		}
		out = append(out, ch)
	}
	return out
}

// loopEntry writes a for or a while loop's own fields, rec being its
// record: its children (containerJobs), and, while it runs, its iterator
// and whether it is parallel or a while loop. A sequential loop's
// iterator is at its last child; a parallel loop's counts its children
// that have completed, all its children being pushed at its start.
func (x *recordView) loopEntry(o *object, fr *frame, k int, rec StepRecord) {
	m := fr.moduleAt(k)
	parallel := m.Kind == ir.KindForLoop && m.ForLoop.Parallel
	kids := x.children(fr, k)
	x.containerJobs(o, kids, rec, parallel, true)
	if rec.Status != "" {
		return
	}
	n, index := 0, max(len(kids)-1, 0)
	if m.Kind == ir.KindForLoop {
		n = x.r.iterLen[fr.sites[k]]
	}
	if parallel {
		index = 0
		for _, c := range kids {
			if c.ended > 0 {
				index++
			}
		}
	}
	var it object
	it.field("index", []byte(strconv.Itoa(index)))
	it.field("itered_len", []byte(strconv.Itoa(n)))
	o.field("iterator", it.close())
	switch {
	case m.Kind == ir.KindWhileLoop:
		o.field("while_loop", []byte("true"))
	case parallel:
		o.field("parallel", []byte("true"))
	}
}

// containerJobs writes a loop's or a branchall's children, rec being its
// record: flow_jobs and flow_jobs_success, and its job: the last child; a
// running parallel loop's last child released to run, and a completed
// parallel container's child that completed last (CF-22); the nil job
// once it has completed with no child (CF-14), with no children's fields
// when its start failed.
func (x *recordView) containerJobs(o *object, kids []child, rec StepRecord, parallel, loop bool) {
	done := rec.Status != ""
	ids := make([][]byte, len(kids))
	ok := make([][]byte, len(kids))
	last, ended := -1, 0
	for i, c := range kids {
		ids[i], ok[i] = jsonText(c.id), c.success
		if c.ended > 0 {
			ended++
			if last < 0 || c.ended > kids[last].ended {
				last = i
			}
		}
	}
	switch {
	case len(kids) == 0 && done && rec.Module == conformance.ModuleSuccess:
		o.field("job", jsonText(nilJob))
	case len(kids) == 0 && done:
		// A container whose start failed, before any child (a chaining
		// error, CF-23; recorded for an iterator that throws): the nil
		// job, and no children's fields.
		o.field("job", jsonText(nilJob))
		return
	case parallel && !done && loop:
		for i := len(kids) - 1; i >= 0; i-- {
			if kids[i].released {
				o.field("job", ids[i])
				break
			}
		}
	case parallel && done:
		o.field("job", ids[last])
	case len(kids) > 0:
		o.field("job", ids[len(ids)-1])
	}
	o.field("flow_jobs", jsonArray(ids))
	o.field("flow_jobs_success", jsonArray(ok))
}

// branchOneEntry writes a branchone's own fields, rec being its record:
// the branch it chose and that branch's child flow as its job, or the nil
// job of a chosen branch that has no module (CF-14) and of a predicate
// that threw.
func (x *recordView) branchOneEntry(o *object, fr *frame, k int, rec StepRecord) {
	m := fr.moduleAt(k)
	if fr.kids != nil {
		for n, c := range fr.kids[k] {
			if c == nil {
				continue
			}
			o.field("branch_chosen", branchChosen(n, len(m.BranchOne.Branches)))
			o.field("job", jsonText(c.id))
			return
		}
	}
	switch {
	case rec.Status == "":
	case rec.BranchChosen == nil:
		// A predicate that threw failed the module with the nil job and
		// chose no branch (OR-11).
		o.field("job", jsonText(nilJob))
	default:
		n := len(m.BranchOne.Branches)
		if rec.BranchChosen.Branch != nil {
			n = *rec.BranchChosen.Branch
		}
		o.field("branch_chosen", branchChosen(n, len(m.BranchOne.Branches)))
		o.field("job", jsonText(nilJob))
	}
}

// branchChosen is a branchone's branch_chosen for branch n of branches,
// the default branch at n = branches.
func branchChosen(n, branches int) []byte {
	var bc object
	if n < branches {
		bc.field("branch", []byte(strconv.Itoa(n)))
		bc.field("type", jsonText("branch"))
	} else {
		bc.field("type", jsonText("default"))
	}
	return bc.close()
}

// branchAllEntry writes a branchall's own fields, rec being its record:
// its branches' child flows (containerJobs), and, while it runs, the
// branch that runs, 0 in a parallel one whose branches are all pushed at
// its start, and how many there are.
func (x *recordView) branchAllEntry(o *object, fr *frame, k int, rec StepRecord) {
	m := fr.moduleAt(k)
	kids := x.children(fr, k)
	x.containerJobs(o, kids, rec, m.BranchAll.Parallel, false)
	if rec.Status != "" {
		return
	}
	branch := max(len(kids)-1, 0)
	if m.BranchAll.Parallel {
		// A parallel branchall's branch counts its branches that have
		// completed, all of them being pushed at its start.
		branch = 0
		for _, c := range kids {
			if c.ended > 0 {
				branch++
			}
		}
		o.field("parallel", []byte("true"))
	}
	var ba object
	ba.field("branch", []byte(strconv.Itoa(branch)))
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
	result, args := tooBig(v.done, recordText(v.result), recordText(v.args))
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
		o.field("result", databaseText(result))
	}
	o.field("args", argsText(args))
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
func argsText(stored []byte) []byte {
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

// replacedFrom is the stored size from which GET jobs_u/get/{id} replaces a
// job's arguments with {"reason":"WINDMILL_TOO_BIG"} and its result with
// "WINDMILL_TOO_BIG" ([OR-V1]): PostgreSQL's pg_column_size of the stored
// jsonb, after its compression.
//
// [OR-V1]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/oracle.md#or-v1
const replacedFrom = 90000

// tooBig gives the job read's text for a job's arguments args and, once
// it has completed, its result: Windmill replaces each whose stored column
// takes replacedFrom bytes or more, by pg_column_size, with
// {"reason":"WINDMILL_TOO_BIG"} and "WINDMILL_TOO_BIG" ([OR-V1], WA-19/o).
// The size is PostgreSQL's after its compression: jsonval.StoredBound
// rules out most values at once, and jsonval.ToastedSize reproduces it
// for the others.
//
// [OR-V1]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/oracle.md#or-v1
func tooBig(done bool, result, args []byte) ([]byte, []byte) {
	if replaced(args) {
		args = []byte(`{"reason":"WINDMILL_TOO_BIG"}`)
	}
	if done && replaced(result) {
		result = []byte(`"WINDMILL_TOO_BIG"`)
	}
	return result, args
}

// replaced reports whether a stored text takes replacedFrom bytes or more
// stored.
func replaced(text []byte) bool {
	if jsonval.StoredBound(text) < replacedFrom {
		return false
	}
	n, err := jsonval.ToastedSize(text)
	return err == nil && n >= replacedFrom
}

// schedJob is a script job pushed for later: its id, and whether it still
// waits.
type schedJob struct {
	id      string
	waiting bool
}

// jobScheduled pushes module i of fr's job, whose record is idx and whose
// stored arguments are stored, for later, after a sleep or a retry delay
// (FX-23, FX-26): it gets its id, its record names it and its arguments,
// and it is registered with the run's tree, as the invocation that takes
// the id registers it again, so that a read finds it waiting (WA-19/p).
func (r *run) jobScheduled(fr *frame, i, idx int, stored jsonval.Value) {
	id := newUUID()
	r.mu.Lock()
	rec := &r.trace.Steps[idx]
	rec.Job, rec.RecordedArgs = id, stored
	r.indexLeaf(id, fr, i, idx, false)
	r.sched[idx] = schedJob{id: id, waiting: true}
	r.nsched.Add(1)
	r.mu.Unlock()
	env := r.jobEnv(fr, i, id)
	r.e.platform.JobRegistry().Begin(platform.Job{ID: id, Kind: platform.JobScript, Root: r.rootID, Path: env["WM_JOB_PATH"], Env: env, Run: r.id})()
}

// released records that c, a parallel container's child flow, is released
// to run (frame.released).
func (r *run) released(c *frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.released = true
}

// subflowScheduled pushes module i of fr's child flow, a flow step's
// subflow whose record is idx, for later, after a sleep or a retry delay:
// it is created and registered with the run's tree, and waits to be picked
// up; the step's status names it, waiting for its executor (WA-19/p).
func (r *run) subflowScheduled(fr *frame, i, idx int, stored jsonval.Value) *frame {
	c := r.subflowFrame(fr, i, r.e.prog.Flows[fr.moduleAt(i).Subflow.Path], stored)
	r.addChild(fr, i, 0, c)
	r.mu.Lock()
	c.scheduled = true
	r.sched[idx] = schedJob{id: c.id, waiting: true}
	r.mu.Unlock()
	r.e.platform.JobRegistry().Begin(platform.Job{ID: c.id, Kind: platform.JobFlow, Root: r.rootID, Path: c.path, Run: r.id, View: runView{r}})()
	return c
}

// unlaunchedSubflow creates the child flow of module i of fr, a flow step
// whose subflow fails at its pickup before any of its modules runs (FX-14,
// FX-38), and registers its job with the run's tree, as runList does for
// one that runs: the flow's status names it, and it is read (WA-19).
func (r *run) unlaunchedSubflow(fr *frame, i int, stored jsonval.Value) *frame {
	c := r.subflowFrame(fr, i, r.e.prog.Flows[fr.moduleAt(i).Subflow.Path], stored)
	r.addChild(fr, i, 0, c)
	r.e.platform.JobRegistry().Begin(platform.Job{ID: c.id, Kind: platform.JobFlow, Root: r.rootID, Path: c.path, Run: r.id, View: runView{r}})()
	return c
}

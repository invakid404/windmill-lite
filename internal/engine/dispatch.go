package engine

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// This file is the default dispatcher of ADR 0007 (milestone PF3): the
// runs a step dispatches with runScript*Async or runFlowAsync, which the
// engine runs from its own program as separate runs of the same runtime,
// and a root script's run as a script job.

// ScriptRequest is one run of a root script as a script job (WA-4): a job
// outside any flow, its own effective root unless the links of a dispatch
// tracked in its parent place it in another job's tree.
type ScriptRequest struct {
	// ID is the job's id, WM_JOB_ID; empty means a new UUID.
	ID string
	// Path is the script's path, a script root of the program; Args is its
	// argument object.
	Path string
	Args jsonval.Value
	// Exec runs the job; nil means Config.Exec.
	Exec StepExecutor
	// ParentJob and RootJob are the parent_job and root_job of a dispatch
	// tracked in its parent (WA-15), "" when absent, and ParentPath the
	// parent job's runnable path. As the oracle recorded for a script that
	// runScriptByPath dispatched (fixture client-api/dispatch, run
	// identity), the job's parent (WM_FLOW_JOB_ID) is then ParentJob, its
	// innermost scope (WM_ROOT_FLOW_JOB_ID) RootJob, its effective root the
	// first of RootJob, ParentJob and itself that is set, and its flow path
	// ParentPath, so its state path is <ParentPath>/nostep_user (WA-5).
	ParentJob, RootJob, ParentPath string
}

// ScriptResult is how a script job ended: its stored result, or its
// failure value (Stored), in its stored form.
type ScriptResult struct {
	ID     string
	Failed bool
	Result jsonval.Value
}

// RunScript runs a root script of the program as a script job and returns
// how it ended: the job's arguments are stored and resolved as a flow
// step's are (JE-41, JE-42, WR-5), its job identity is a script job's at
// its path (WA-4, WA-5: no flow fields, its own effective root) or the
// one its parent links give it (ScriptRequest), and its timeout is the
// script's own or the instance's (WR-37). An error means
// it could not start: a path that is not a script root, or arguments that
// are not a JSON object or that the job API could not store.
func (e *Engine) RunScript(ctx context.Context, req ScriptRequest) (ScriptResult, error) {
	s := e.prog.Scripts[req.Path]
	if s == nil || s.Kind != ir.PathScript || !slices.Contains(e.prog.Roots.Scripts, req.Path) {
		return ScriptResult{}, fmt.Errorf("engine: no script root %q in the program", req.Path)
	}
	if args := bytes.TrimSpace(req.Args.Producer()); len(args) == 0 || args[0] != '{' {
		return ScriptResult{}, errors.New("engine: the arguments of a script run are a JSON object")
	}
	stored, err := req.Args.Stored()
	if err != nil {
		return ScriptResult{}, fmt.Errorf("engine: the arguments cannot be stored: %w", err)
	}
	exec := req.Exec
	if exec == nil {
		exec = e.exec
	}
	if exec == nil {
		return ScriptResult{}, errors.New("engine: no step executor for the run")
	}
	id := req.ID
	if id == "" {
		id = newUUID()
	}
	args := jsonval.FromProducer(stored)
	entry, _, err := Entrypoint(args)
	if err != nil {
		return ScriptResult{ID: id, Failed: true, Result: workerError("", err.Error())}, nil
	}
	boundary := Boundary(s.Boundary)
	ident := platform.Identity{JobID: id, RootJobID: id, JobPath: s.Path, Token: platform.NewToken()}
	if req.ParentJob != "" || req.RootJob != "" {
		ident.FlowJobID, ident.RootFlowJobID, ident.FlowPath = req.ParentJob, req.RootJob, req.ParentPath
		ident.RootJobID = cmp.Or(req.RootJob, req.ParentJob, id)
	}
	env := ident.Env(e.host)
	reserved := maps.Clone(env)
	addScriptEnvs(env, s, boundary)
	resolved, err := e.platform.ResolveArgs(ctx, args, reserved, platform.APIEnv(reserved))
	if err != nil {
		return ScriptResult{ID: id, Failed: true, Result: workerError("", "execution error:\n"+err.Error())}, nil
	}
	timeout, instanceWide := e.cap, true
	if s.Metadata != nil && s.Metadata.Timeout != nil {
		if d := time.Duration(*s.Metadata.Timeout) * time.Second; d < e.cap {
			timeout, instanceWide = d, false
		}
	}
	stepCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	t := time.AfterFunc(timeout, func() { cancel(CancelTimeout) })
	defer t.Stop()
	out, err := exec.Execute(stepCtx, Invocation{
		ID:          id,
		Run:         id,
		Site:        conformance.SitePath{},
		Script:      s.ID,
		Entry:       entry,
		Args:        resolved,
		Env:         env,
		Boundary:    boundary,
		ModuleState: ModuleStateWarm,
		Report:      NopReporter{},
	})
	switch {
	case errors.Is(err, CancelTimeout):
		return ScriptResult{ID: id, Failed: true, Result: TimeoutError(boundary, "", timeout, instanceWide)}, nil
	case err != nil && ctx.Err() != nil:
		reason, canceler := cancellationOf(ctx)
		return ScriptResult{ID: id, Failed: true, Result: killedError("", reason, canceler)}, nil
	case err != nil:
		return ScriptResult{ID: id, Failed: true, Result: engineError("InternalErr", err.Error())}, nil
	}
	st := StoreOutcome(out, "")
	if _, err := st.Value.Stored(); err != nil {
		// FX-48 case 1: the completion insert refuses a value jsonb
		// rejects, and the job fails with the storage error.
		return ScriptResult{ID: id, Failed: true, Result: engineError("InternalErr", databaseError(err).Error())}, nil
	}
	return ScriptResult{ID: id, Failed: st.Failed, Result: st.Value}, nil
}

// Dispatcher is the default dispatcher of ADR 0007: it runs a dispatched
// target from the engine's program as a separate run of the same runtime,
// started at once, since no default cap delays it (D15), and answers the
// result polls for the runs it started (platform.Dispatcher). The target
// must be a root of the program (ADR 0002's host contract): a script root
// for jobs/run/p and a flow root for jobs/run/f; any other fails with
// *platform.NotInBundleError. A dispatched run is detached from the call
// that started it and from the run of the step that made it: it ends on
// its own, or when the dispatcher closes. Its record is kept while the
// root of the step that dispatched it lives in the job registry, so that
// the step's run can read its result however late. It is safe for
// concurrent use.
type Dispatcher struct {
	e      *Engine
	exec   StepExecutor
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	closed bool
	runs   map[string]*dispatched
}

// dispatched is the record of one dispatched run.
type dispatched struct {
	// owner is the effective root of the step that dispatched it, whose
	// life the record follows.
	owner  string
	result platform.JobResult
	done   bool
}

// NewDispatcher returns the dispatcher of e's program, whose runs go to
// exec (the facade's executor, so that their platform calls are
// attributed); nil means e's own executor. A host sets it as the
// services' Dispatcher before any run starts, and closes it when the
// runtime ends.
func NewDispatcher(e *Engine, exec StepExecutor) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{e: e, exec: exec, ctx: ctx, cancel: cancel, runs: map[string]*dispatched{}}
}

// Dispatch starts the run of d's target and returns its job id.
func (x *Dispatcher) Dispatch(_ context.Context, d platform.Dispatch) (string, error) {
	var roots []string
	switch d.Kind {
	case platform.DispatchScript:
		roots = x.e.prog.Roots.Scripts
	case platform.DispatchFlow:
		roots = x.e.prog.Roots.Flows
	}
	if !slices.Contains(roots, d.Target) {
		return "", &platform.NotInBundleError{Target: d.Target}
	}
	args := jsonval.FromProducer(d.Args)
	if len(bytes.TrimSpace(d.Args)) == 0 {
		args = jsonval.FromProducer([]byte("{}"))
	}
	if a := bytes.TrimSpace(args.Producer()); len(a) == 0 || a[0] != '{' {
		return "", &platform.Error{Variant: "BadRequest", Message: "windmill-lite: the arguments of a dispatched run are a JSON object"}
	}
	if _, err := args.Stored(); err != nil {
		return "", &platform.Error{Variant: "BadRequest", Message: "windmill-lite: the arguments of a dispatched run cannot be stored: " + err.Error()}
	}
	// The run's links are taken now, while the dispatching job is still
	// registered: the run may start after it has ended (a delay, or a
	// caller that returns at once).
	links := x.links(d)
	id := newUUID()
	rec := &dispatched{owner: d.Caller["WM_ROOT_JOB_ID"]}
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return "", errors.New("engine: the dispatcher is closed")
	}
	x.sweep()
	x.runs[id] = rec
	x.wg.Add(1)
	x.mu.Unlock()
	go x.run(id, d, args, links, rec)
	return id, nil
}

// run runs one dispatched target to its end.
func (x *Dispatcher) run(id string, d platform.Dispatch, args jsonval.Value, links parentLinks, rec *dispatched) {
	defer x.wg.Done()
	if d.ScheduledInSecs > 0 {
		t := time.NewTimer(time.Duration(d.ScheduledInSecs) * time.Second)
		select {
		case <-t.C:
		case <-x.ctx.Done():
			t.Stop()
		}
	}
	x.mu.Lock()
	rec.result.Started = true
	x.mu.Unlock()
	var res platform.JobResult
	if d.Kind == platform.DispatchFlow {
		res = x.runFlow(id, d.Target, args, links)
	} else {
		res = x.runScript(id, d.Target, args, links)
	}
	res.Started, res.Completed = true, true
	x.mu.Lock()
	rec.result, rec.done = res, true
	x.mu.Unlock()
}

// runFlow runs a dispatched flow, with the links its dispatch sent.
func (x *Dispatcher) runFlow(id, path string, args jsonval.Value, l parentLinks) platform.JobResult {
	t, err := x.e.Run(x.ctx, RunRequest{ID: id, Path: path, Args: args, Exec: x.exec, ParentJob: l.job, RootJob: l.root, ParentPath: l.path})
	if err != nil {
		return platform.JobResult{Result: storedText(engineError("InternalErr", err.Error()))}
	}
	return platform.JobResult{Success: t.Status == conformance.RunSuccess, Result: storedText(t.Result)}
}

// runScript runs a dispatched script, with the links its dispatch sent.
func (x *Dispatcher) runScript(id, path string, args jsonval.Value, l parentLinks) platform.JobResult {
	r, err := x.e.RunScript(x.ctx, ScriptRequest{ID: id, Path: path, Args: args, Exec: x.exec, ParentJob: l.job, RootJob: l.root, ParentPath: l.path})
	if err != nil {
		return platform.JobResult{Result: storedText(engineError("InternalErr", err.Error()))}
	}
	return platform.JobResult{Success: !r.Failed, Result: storedText(r.Result)}
}

// parentLinks are the links a dispatch tracked in its parent gives its
// run: the query's parent_job and root_job, and the parent job's runnable
// path, taken from the job registry when the dispatch is accepted.
type parentLinks struct{ job, root, path string }

// links snapshots d's links; the path is "" when d names no parent, or one
// the registry does not know.
func (x *Dispatcher) links(d platform.Dispatch) parentLinks {
	l := parentLinks{job: d.ParentJob, root: d.RootJob}
	if l.job != "" {
		j, _, _ := x.e.platform.JobRegistry().Job(l.job)
		l.path = j.Path
	}
	return l
}

// storedText is a result's stored form, as the completed job's row holds
// it, or null when it has none.
func storedText(v jsonval.Value) []byte {
	if v.IsZero() {
		return []byte("null")
	}
	s, err := v.Stored()
	if err != nil {
		return []byte("null")
	}
	return s
}

// Result returns the state of a run the dispatcher started.
func (x *Dispatcher) Result(_ context.Context, id string) (platform.JobResult, bool, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	rec := x.runs[id]
	if rec == nil {
		return platform.JobResult{}, false, nil
	}
	return rec.result, true, nil
}

// sweep forgets the completed runs whose dispatching step's root has
// ended, so that the records a long-lived runtime keeps are bounded by
// the work it still runs. Called with mu held.
func (x *Dispatcher) sweep() {
	jobs := x.e.platform.JobRegistry()
	for id, rec := range x.runs {
		if !rec.done {
			continue
		}
		if _, live, ok := jobs.Job(rec.owner); !ok || !live {
			delete(x.runs, id)
		}
	}
}

// Close cancels the runs still going, as a canceled run is (FX-38), and
// waits until they have ended or ctx ends. A Dispatch after Close fails.
func (x *Dispatcher) Close(ctx context.Context) {
	x.mu.Lock()
	x.closed = true
	x.mu.Unlock()
	x.cancel()
	done := make(chan struct{})
	go func() {
		x.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

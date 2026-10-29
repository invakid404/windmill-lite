package wl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform/facade"
)

// Trace is what a flow run returns besides its result (ADR 0008 H9): per
// site, what happened and why, with provenance and timings, and the
// platform calls the run's steps made.
type Trace = engine.Trace

// PlatformCall is one platform call a step made, as the facade recorded
// it (ADR 0007).
type PlatformCall = engine.PlatformCall

// Warning is a warning of a run: an unattributed process-level error, the
// work an invocation left behind when it settled (D9), a resolution that
// fell back.
type Warning = engine.Warning

// Cancellation is a cancellation's reason and canceler: a host that
// cancels a run's context with one as the cause (context.WithCancelCause)
// has the run end with Windmill's Canceled error naming them (FX-38);
// any other cause names Windmill's defaults.
type Cancellation = engine.Cancellation

// Run statuses, Result.Status.
const (
	StatusSuccess  = string(conformance.RunSuccess)
	StatusFailure  = string(conformance.RunFailure)
	StatusCanceled = string(conformance.RunCanceled)
	StatusSkipped  = string(conformance.RunSkipped)
)

// Target kinds, RunOptions.Kind.
const (
	KindFlow   = "flow"
	KindScript = "script"
)

// RunOptions configure one run. The zero value of every field is a
// default; the hooks, per-run providers and budgets of later milestones
// (F2a, F2b) are added here.
type RunOptions struct {
	// Kind is KindFlow, the default, or KindScript: a root script of the
	// bundle, run as a script job (WA-4).
	Kind string
	// ID is the run's job id, which steps see as WM_FLOW_JOB_ID (a
	// script's as WM_JOB_ID); empty means a new UUID.
	ID string
	// Seed fixes the run's shuffles of the orders Windmill leaves
	// unspecified (ADR 0001 §2) and its retry jitter; zero draws one.
	Seed uint64
}

// Result is how a run ended.
type Result struct {
	// ID is the run's job id.
	ID string
	// Status is StatusSuccess, StatusFailure, StatusCanceled or
	// StatusSkipped.
	Status string
	// Result is the final result in its stored form (FX-36): the flow's
	// result, or a script's result or failure value.
	Result json.RawMessage
	// MayHaveMutated is true once the run launched its first write-capable
	// step (ADR 0008 H3).
	MayHaveMutated bool
	// Trace is the flow run's trace; nil for a script run.
	Trace *Trace
	// Warnings are the run's warnings.
	Warnings []Warning
}

// Run runs the flow or script at path with args, a JSON object, and
// returns how it ended (ADR 0009). The run's context bounds it: when it
// ends, the run ends canceled, with its cause's reason and canceler when
// that is a *Cancellation (FX-38). An error means the run could not start:
// no such target, or arguments that are not a JSON object or that cannot
// be stored.
func (rt *Runtime) Run(ctx context.Context, path string, args json.RawMessage, opts RunOptions) (*Result, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	switch opts.Kind {
	case "", KindFlow:
	case KindScript:
		res, err := rt.eng.RunScript(ctx, engine.ScriptRequest{ID: opts.ID, Path: path, Args: jsonval.FromProducer(args)})
		if err != nil {
			return nil, err
		}
		out := &Result{ID: res.ID, Status: StatusSuccess, Result: storedText(res.Result)}
		switch {
		case res.Failed && ctx.Err() != nil:
			// The script failed because the run's context ended (FX-38).
			out.Status = StatusCanceled
		case res.Failed:
			out.Status = StatusFailure
		}
		return out, nil
	default:
		return nil, fmt.Errorf("wl: run kind %q is neither %s nor %s", opts.Kind, KindFlow, KindScript)
	}
	id := opts.ID
	if id == "" {
		id = newUUID()
	}
	calls := rt.exec.begin(id)
	defer rt.exec.end(id)
	tr, err := rt.eng.Run(ctx, engine.RunRequest{ID: id, Path: path, Args: jsonval.FromProducer(args), Seed: opts.Seed})
	if err != nil {
		return nil, err
	}
	tr.PlatformCalls = calls.taken()
	return &Result{
		ID:             tr.RunID,
		Status:         string(tr.Status),
		Result:         storedText(tr.Result),
		MayHaveMutated: tr.MayHaveMutated,
		Trace:          tr,
		Warnings:       tr.Warnings,
	}, nil
}

// newUUID returns a random (version 4) UUID, a job id.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// storedText is a value's stored form, or its producer text when it has
// none, or nil for the zero value.
func storedText(v jsonval.Value) json.RawMessage {
	if v.IsZero() {
		return nil
	}
	if s, err := v.Stored(); err == nil {
		return s
	}
	return v.Producer()
}

// recorder is the runtime's step executor: it passes each invocation on to
// the facade's executor over the harness pool, and keeps, for the runs
// that are in flight, the platform calls of their invocations, which the
// facade hands over when it releases each invocation's record (after it
// settled and its outcome was decided, so before the engine sees the
// outcome, for any invocation that is not abandoned).
type recorder struct {
	inner engine.StepExecutor
	mu    sync.Mutex
	runs  map[string]*callLog
	invs  map[string]invocation
}

type invocation struct {
	run  string
	site conformance.SitePath
}

// callLog is the platform calls of one run.
type callLog struct {
	mu    sync.Mutex
	calls []PlatformCall
}

func (l *callLog) taken() []PlatformCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]PlatformCall(nil), l.calls...)
}

func (x *recorder) begin(run string) *callLog {
	l := &callLog{}
	x.mu.Lock()
	x.runs[run] = l
	x.mu.Unlock()
	return l
}

func (x *recorder) end(run string) {
	x.mu.Lock()
	delete(x.runs, run)
	x.mu.Unlock()
}

func (x *recorder) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	x.mu.Lock()
	x.invs[inv.ID] = invocation{run: inv.Run, site: inv.Site}
	x.mu.Unlock()
	return x.inner.Execute(ctx, inv)
}

// released is the facade's Config.Recorded: an invocation's calls, which
// go to its run while the run is in flight. An invocation released after
// its run ended, an abandoned one, takes its calls with it.
func (x *recorder) released(inv string, calls []facade.Record) {
	x.mu.Lock()
	in, ok := x.invs[inv]
	delete(x.invs, inv)
	l := x.runs[in.run]
	x.mu.Unlock()
	if !ok || l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range calls {
		l.calls = append(l.calls, PlatformCall{
			Site: in.site, Job: inv, Method: c.Method, Path: c.Path, Query: c.Query, Route: c.Route,
			Status: c.Status, Unsupported: c.Unsupported, Message: c.Message,
		})
	}
}

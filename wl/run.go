package wl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
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
	// StatusLatched and StatusHalted are decided by the host, never
	// Windmill: a run that latched an error (H5), and one a halt ended
	// (H8).
	StatusLatched = string(conformance.RunLatched)
	StatusHalted  = string(conformance.RunHalted)
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
	// Hooks are the flow run's host hooks as Go callbacks (ADR 0008: H1,
	// H2, H6, H8, H9, H12); nil means none. Their selectors are checked
	// before the run starts.
	Hooks *Hooks
	// Platform, when set, is the flow run's own platform providers (H13),
	// which a host chooses from its run data, for example per tenant;
	// nil means the runtime's.
	Platform *RunPlatform
}

// RunPlatform is a run's own platform providers (H13, ADR 0008). The run's
// argument resolution, flow_env, expression helpers and its steps' API
// reads answer from them; its job registry, flow user state, object store
// and result cache stay the runtime's. The runs a step dispatches read
// the runtime's providers.
type RunPlatform struct {
	// Resources and Variables replace the runtime's at the same paths for
	// this run only. A step's variable update (WA-11) changes the run's
	// own variable, or the runtime's where the run has none.
	Resources map[string]Resource
	Variables map[string]Variable
	// Resolve, when set, is the run's host secret layer (ADR 0007): after
	// interpolation, each resource value, nested ones included, and each
	// secret's plaintext, as a JSON string, pass through it; it returns
	// the value to use, JSON. A raw resource read does not.
	Resolve func(ctx context.Context, path string, resource bool, value json.RawMessage) (json.RawMessage, error)
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
	// Latched is the error a hook, a provider or the engine latched on a
	// run whose Status is StatusLatched (H5), such as ErrProviderDeclined;
	// nil otherwise.
	Latched error
}

// Run runs the flow or script at path with args, a JSON object, and
// returns how it ended (ADR 0009). The run's context bounds it: when it
// ends, the run ends canceled, with its cause's reason and canceler when
// that is a *Cancellation (FX-38); Close cancels it too, with the reason
// "the runtime is closing" by "windmill-lite". An error means the run
// could not start: the runtime is closed (ErrClosed), no such target, or
// arguments that are not a JSON object or that cannot be stored.
func (rt *Runtime) Run(ctx context.Context, path string, args json.RawMessage, opts RunOptions) (*Result, error) {
	if !rt.admit() {
		return nil, ErrClosed
	}
	defer rt.active.Done()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	defer context.AfterFunc(rt.stop, func() { cancel(context.Cause(rt.stop)) })()
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
	h, err := engineHooks(opts.Hooks, rt.prog)
	if err != nil {
		return nil, err
	}
	var services *platform.Services
	if opts.Platform != nil {
		if services, err = rt.runServices(opts.Platform); err != nil {
			return nil, err
		}
	}
	calls := rt.exec.begin(id)
	defer rt.exec.end(id)
	tr, err := rt.eng.Run(ctx, engine.RunRequest{ID: id, Path: path, Args: jsonval.FromProducer(args), Seed: opts.Seed, Hooks: h, Platform: services})
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
		Latched:        tr.Latched,
	}, nil
}

// runServices are the services of a run with its own providers (H13): its
// resources and variables over the runtime's, and its resolver.
func (rt *Runtime) runServices(p *RunPlatform) (*platform.Services, error) {
	store := platform.NewStore()
	for path, r := range p.Resources {
		if err := store.SetResource(path, platform.Resource{Type: r.Type, Value: r.Value}); err != nil {
			return nil, fmt.Errorf("wl: RunPlatform.Resources[%s]: %w", path, err)
		}
	}
	for path, v := range p.Variables {
		store.SetVariable(path, platform.Variable{Value: v.Value, IsSecret: v.Secret, Encrypted: v.Ciphertext})
	}
	providers := platform.Providers{Resources: platform.ResourceChain{store, rt.store}, Variables: platform.VariableChain{store, rt.store}}
	if p.Resolve != nil {
		providers.Resolvers = append(slices.Clone(rt.services.Resolvers), resolver(p.Resolve))
	}
	return rt.services.WithProviders(providers), nil
}

// resolver is a RunPlatform's Resolve as a value resolver.
type resolver func(ctx context.Context, path string, resource bool, value json.RawMessage) (json.RawMessage, error)

func (f resolver) ResolveValue(ctx context.Context, path string, resource bool, v jsonval.Value) (jsonval.Value, error) {
	out, err := f(ctx, path, resource, v.Producer())
	if err != nil {
		return jsonval.Value{}, err
	}
	if !json.Valid(out) {
		return jsonval.Value{}, fmt.Errorf("wl: the resolver returned a value for %s that is not JSON", path)
	}
	return jsonval.FromProducer(out), nil
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
// facade hands over as it answers each (Config.Answered): before the
// step's outcome, so the trace of a run that ends has every call its
// steps were answered, an abandoned step's included, until the run ends.
// An invocation's run and site stay known until its run ends, not only
// until the facade releases the invocation: a call the step never awaited
// can be answered after the release, while the run goes on.
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

// callLog is the platform calls of one run, and its invocations, which
// end forgets (guarded by the recorder's mu).
type callLog struct {
	mu    sync.Mutex
	calls []PlatformCall
	invs  []string
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
	defer x.mu.Unlock()
	if l := x.runs[run]; l != nil {
		for _, id := range l.invs {
			delete(x.invs, id)
		}
	}
	delete(x.runs, run)
}

func (x *recorder) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	x.mu.Lock()
	x.invs[inv.ID] = invocation{run: inv.Run, site: inv.Site}
	if l := x.runs[inv.Run]; l != nil {
		l.invs = append(l.invs, inv.ID)
	}
	x.mu.Unlock()
	return x.inner.Execute(ctx, inv)
}

// answered is the facade's Config.Answered: a call, which goes to its
// invocation's run while the run is in flight. A call answered after its
// run ended, an abandoned step's, is dropped.
func (x *recorder) answered(inv string, c facade.Record) {
	x.mu.Lock()
	in, ok := x.invs[inv]
	l := x.runs[in.run]
	x.mu.Unlock()
	if !ok || l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, PlatformCall{
		Site: in.site, Job: inv, Method: c.Method, Path: c.Path, Query: c.Query, Route: c.Route,
		Status: c.Status, Unsupported: c.Unsupported, Message: c.Message,
	})
}

// released is the facade's Config.Recorded: the invocation's record is
// dropped. What the recorder knows of it goes too unless its run is in
// flight, whose end forgets it: an answer still on its way to answered,
// for a call the step did not await, must find its run and site.
func (x *recorder) released(inv string, _ []facade.Record) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if in, ok := x.invs[inv]; ok && x.runs[in.run] == nil {
		delete(x.invs, inv)
	}
}

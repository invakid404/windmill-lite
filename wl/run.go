package wl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
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

// The causes of a canceled run, Result.CancelCause (ADR 0004 §Cancellation
// and deadlines). Each keeps the status StatusCanceled and the result: a
// flow's Canceled error (FX-38), or a root script's stored failure value,
// such as the killed job's error when the cancellation stopped its job.
const (
	// CauseDeadline: the run's hard deadline, its context's, passed.
	CauseDeadline = engine.CauseDeadline
	// CausePreWriteDeadline: the flow run's pre-write deadline
	// (RunOptions.PreWriteDeadline) passed before its first write was
	// approved.
	CausePreWriteDeadline = engine.CausePreWriteDeadline
	// CauseCanceled: any other end of the run's context, such as a
	// cancellation with a *Cancellation cause.
	CauseCanceled = engine.CauseCanceled
	// CauseClosing: Close canceled the run.
	CauseClosing = engine.CauseClosing
	// CauseFlowTimeout: the flow's own wall-clock limit (FX-52).
	CauseFlowTimeout = engine.CauseFlowTimeout
)

// Target kinds, RunOptions.Kind.
const (
	KindFlow   = "flow"
	KindScript = "script"
)

// RunOptions configure one run. A script run reads only Kind, ID and
// Data; the other fields, among them the hooks, the run's own providers
// and the pre-write deadline, configure a flow run. The zero value of
// every field is a default.
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
	// PreWriteDeadline is the flow run's pre-write deadline (H4): when it
	// passes before the run's first write is approved (Hooks.WriteCapable
	// and BeforeFirstWrite, H3), the run ends canceled, with
	// ErrPreWriteDeadline as its context's cause; once that approval has
	// been given it no longer applies. Zero means none. The run's hard
	// deadline is its context's.
	PreWriteDeadline time.Time
	// Clock is the flow run's clock (H10), through which every sleep and
	// retry delay waits: nil means the real clock; a *RecordClock records
	// each requested wait and returns at once (quick waits), and the trace
	// then lists H10 among its deviations.
	Clock Clock
	// Jitter is the flow run's retry-jitter source (H10, FX-26); nil draws
	// from one seeded with Seed. SeededJitter fixes it.
	Jitter Jitter
	// Data is the run's data (H11), any JSON value, which the host plugin
	// of each process that the run uses reads as wl.run.data, in its hooks
	// and its around wrappers, until the run has settled; a script run
	// reads it too. Nil, empty or JSON null means none: wl.run.data is
	// then undefined. Data that is not JSON is refused before the run
	// starts.
	Data json.RawMessage
	// Pin pins the run to one warm harness process (ADR 0004 §Pooling), as
	// Options.Pool.PinRuns does for every run: its first invocation or
	// host-plugin call goes to a serving process, and every later one goes
	// there while that process lives, even when it lags, is tainted or
	// leaves for a recycle trigger, so a host plugin can keep the run's
	// state there. The process does not leave while the run is live. If it
	// ends, the run's next invocation or host-plugin call latches the run
	// with ErrPinnedProcessLost. A fresh runtime and host mode refuse it.
	Pin bool
	// Limits are the run's own limits (ADR 0004 §Pooling); the zero value
	// sets none.
	Limits RunLimits
}

// RunLimits are a run's own limits (ADR 0004 §Pooling). A zero field sets
// no limit; Run refuses a negative one.
type RunLimits struct {
	// MaxInFlight bounds a flow run's invocations in flight at once, so
	// its parallel branches and iterations wait for a slot, as jobs
	// waiting in the queue, before their timeouts start. It is deviation
	// D15 (ADR 0001), which the run's trace then lists. A script run has
	// one invocation.
	MaxInFlight int
	// MaxArgsBytes bounds the size of the run's arguments as given to Run:
	// larger ones make Run return ErrRunArgsTooLarge, and nothing runs.
	MaxArgsBytes int
	// MaxHeldBytes bounds the bytes of the stored values that the engine
	// holds for the run: its arguments and the stored result of each of
	// its jobs, a cached one included, counted at their stored size as
	// they are stored (a script job's arguments before it runs), and not
	// returned while the run lasts. Once they pass it, the run latches
	// ErrRunMemoryLimit, as a latched hook does (H5): it starts nothing
	// more.
	MaxHeldBytes int64
	// Bytes is the run's byte budget, which its steps, around wrappers
	// included, draw on with wl.reserve(bytes) in the step API, for data
	// that a step holds, such as a source it downloads. The engine keeps
	// the account, so the budget holds across processes. A reservation
	// that does not fit it, or the runtime's total (Options.Limits.Bytes),
	// rejects in the step with a RangeError, and each reservation returns
	// when the invocation that made it settles. With neither a budget nor
	// a total, a reservation takes nothing.
	Bytes int64
}

// ErrRunArgsTooLarge is Run's error for arguments larger than
// RunOptions.Limits.MaxArgsBytes. Nothing runs.
var ErrRunArgsTooLarge = errors.New("wl: the run's arguments are larger than its limit (RunOptions.Limits.MaxArgsBytes)")

// ErrRunMemoryLimit is the latch of a run whose stored values passed
// RunOptions.Limits.MaxHeldBytes (run_memory_limit).
var ErrRunMemoryLimit = engine.ErrRunMemoryLimit

// ErrPinnedProcessLost is the latch of a pinned run whose process ended
// while the run was live (pinned_process_lost): the run cannot move, since
// its per-run state lived in that process.
var ErrPinnedProcessLost = engine.ErrPinnedProcessLost

// Clock is a run's clock (H10): Now, and Wait, which waits for a duration
// or until the context ends.
type Clock = engine.Clock

// RealClock is the real clock, the default.
type RealClock = engine.RealClock

// RecordClock is H10's record mode: each requested wait is recorded and
// returns at once, its time added to the clock's Now; Waits lists them.
// Its zero value is ready to use; share one only between runs whose waits
// may be mixed.
type RecordClock = engine.RecordClock

// Jitter is a run's retry-jitter source (H10, FX-26).
type Jitter = engine.Jitter

// SeededJitter returns a jitter source fixed by seed (H10).
func SeededJitter(seed uint64) Jitter { return engine.SeededJitter(seed) }

// ErrPreWriteDeadline is the cause of a run's context when its pre-write
// deadline passed before its first write (H4); it wraps
// context.DeadlineExceeded.
var ErrPreWriteDeadline = engine.ErrPreWriteDeadline

// pin makes the routing choice of the run id in a runtime with a pool: it
// pins the run to a pool process when it asks for it (pin) or the runtime
// pins every run (Options.Pool.PinRuns), and otherwise marks it unpinned.
// It returns the choice, nil without a pool, which the run's calls carry
// in their context (harness.WithPin), its late ones included, so that no
// other run's choice, of a later run with the id or of another runtime,
// routes them; the settlement callback, which also forgets a pin, wrapping
// settled; and the function to call when the run has ended, which ends the
// pin's hold on its process. A run that settles is forgotten at its
// settlement, and one that never settles keeps a pin that holds nothing.
// A pin that a live run of the id holds, such as a run that a step
// dispatched, refuses the run with ErrRunInFlight.
func (rt *Runtime) pin(id string, pin bool, settled func()) (*harness.Pin, func(), func(), error) {
	switch {
	case rt.pool == nil:
		return nil, settled, func() {}, nil
	case !(pin || rt.pinRuns):
		return rt.pool.Unpinned(id), settled, func() {}, nil
	}
	p, err := rt.pool.PinRun(id)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %w", ErrRunInFlight, err)
	}
	return p, func() {
			rt.pool.ForgetRun(p)
			if settled != nil {
				settled()
			}
		}, func() {
			rt.pool.RunEnded(p)
		}, nil
}

// forgetPin drops the pin of a run that did not start, which never
// settles.
func (rt *Runtime) forgetPin(p *harness.Pin) {
	if rt.pool != nil {
		rt.pool.ForgetRun(p)
	}
}

// ErrRunInFlight is Run's error for an id (RunOptions.ID) that a run of the
// runtime in flight already has, or, for a pinned run, that a live pinned
// run that a step dispatched has. Nothing runs; the id can be used again
// once that run has returned.
var ErrRunInFlight = errors.New("wl: a run with this id is in flight")

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
	// CancelCause is why a run whose Status is StatusCanceled was
	// canceled: CauseDeadline, CausePreWriteDeadline, CauseCanceled,
	// CauseClosing or CauseFlowTimeout; "" otherwise.
	CancelCause string
}

// Run runs the flow or script at path with args, a JSON object, and
// returns how it ended (ADR 0009). The run's context bounds it: when it
// ends, the run ends canceled, with its cause's reason and canceler when
// that is a *Cancellation (FX-38); Close cancels it too, with the reason
// "the runtime is closing" by "windmill-lite". Result.CancelCause says
// which of these, or which deadline, canceled it. An error means the run
// could not start: the runtime is closed (ErrClosed), no such target, or
// arguments that are not a JSON object or that cannot be stored.
func (rt *Runtime) Run(ctx context.Context, path string, args json.RawMessage, opts RunOptions) (*Result, error) {
	return rt.run(ctx, path, args, opts, nil, nil)
}

// run is Run with, for a flow run whose RunOptions.Hooks is nil, extra as
// its engine hooks (host mode's static intercepts); settled, when not nil,
// is called once the run has settled (ADR 0004 host mode item 5, H9's
// onRunSettled): its outcome is decided and every invocation it started
// has settled, which may be never, and never for a run that did not start.
func (rt *Runtime) run(ctx context.Context, path string, args json.RawMessage, opts RunOptions, extra engine.Hooks, settled func()) (*Result, error) {
	if !rt.admit() {
		return nil, ErrClosed
	}
	defer rt.active.Done()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	defer context.AfterFunc(rt.stop, func() { cancel(context.Cause(rt.stop)) })()
	if l := opts.Limits; l.MaxInFlight < 0 || l.MaxArgsBytes < 0 || l.MaxHeldBytes < 0 || l.Bytes < 0 {
		return nil, fmt.Errorf("wl: a run's limits are not negative (RunOptions.Limits: %+v)", l)
	}
	// The arguments as given, before an empty text becomes {}.
	if l := opts.Limits.MaxArgsBytes; l > 0 && len(args) > l {
		return nil, fmt.Errorf("%w: %d bytes, the limit is %d", ErrRunArgsTooLarge, len(args), l)
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	if opts.Pin && (rt.state == engine.ModuleStateFresh || rt.host != nil) {
		return nil, errors.New("wl: a pinned run needs a warm runtime's pool: a fresh runtime starts a process for each step, and in host mode the host is the one process (RunOptions.Pin)")
	}
	data, err := runData(opts.Data)
	if err != nil {
		return nil, err
	}
	switch opts.Kind {
	case "", KindFlow:
	case KindScript:
		id := opts.ID
		if id == "" {
			id = newUUID()
		}
		// The script's job is its invocation's run: its platform calls,
		// and the store failures they or its own argument reads meet,
		// warn as a flow run's do.
		calls, ok := rt.exec.begin(id)
		if !ok {
			return nil, ErrRunInFlight
		}
		defer rt.exec.end(id)
		ctx = calls.reportStoreFailures(ctx)
		req := engine.ScriptRequest{ID: id, Path: path, Args: jsonval.FromProducer(args), Around: rt.aroundAt(), MaxHeldBytes: opts.Limits.MaxHeldBytes,
			Bytes: opts.Limits.Bytes, ByteTotal: rt.byteTotal}
		p, settled, unpin, err := rt.pin(id, opts.Pin, settled)
		if err != nil {
			return nil, err
		}
		defer unpin()
		ctx = harness.WithPin(ctx, p)
		ctx, _, settled = withRunData(ctx, id, data, settled)
		var s *engine.RunSettlement
		if settled != nil {
			// A script job has its own one invocation, which settles it.
			s = engine.NewRunSettlement(settled)
			req.Exec = settlingExec{inner: rt.exec, settle: s}
		}
		res, err := rt.eng.RunScript(ctx, req)
		if err != nil {
			rt.forgetPin(p)
			return nil, err
		}
		out := &Result{ID: res.ID, Status: StatusSuccess, Result: storedText(res.Result), Warnings: calls.warned()}
		switch {
		case res.Latched != nil:
			// Its step latched the run (H5): wl.latch, or a failed around.
			out.Status, out.Result, out.Latched = StatusLatched, nil, res.Latched
		case res.Failed && ctx.Err() != nil:
			// The script failed because the run's context ended (FX-38).
			out.Status, out.CancelCause = StatusCanceled, engine.CancelCauseOf(ctx)
		case res.Failed:
			out.Status = StatusFailure
		}
		// The run settles once its outcome is decided: in host mode the
		// settlement's callback releases the run's hard deadline, which
		// ends ctx, so a script that failed on its own does not read as
		// canceled.
		if s != nil {
			s.End()
		}
		return out, nil
	default:
		return nil, fmt.Errorf("wl: run kind %q is neither %s nor %s", opts.Kind, KindFlow, KindScript)
	}
	id := opts.ID
	if id == "" {
		id = newUUID()
	}
	calls, ok := rt.exec.begin(id)
	if !ok {
		return nil, ErrRunInFlight
	}
	defer rt.exec.end(id)
	var rd *harness.RunData
	ctx, rd, settled = withRunData(ctx, id, data, settled)
	h, err := engineHooks(rt.withPlugin(opts.Hooks, rd), rt.prog)
	if err != nil {
		return nil, err
	}
	p, settled, unpin, err := rt.pin(id, opts.Pin, settled)
	if err != nil {
		return nil, err
	}
	defer unpin()
	ctx = harness.WithPin(ctx, p)
	if h != nil {
		h.pin = p
	}
	var services *platform.Services
	bind := &runBinding{id: id, pin: p}
	if opts.Platform != nil || rt.answersPlatform() || rt.readCache {
		if services, err = rt.runServices(opts.Platform, bind); err != nil {
			rt.forgetPin(p)
			return nil, err
		}
	}
	req := engine.RunRequest{ID: id, Path: path, Args: jsonval.FromProducer(args), Seed: opts.Seed, Platform: services,
		PreWriteDeadline: opts.PreWriteDeadline, Clock: opts.Clock, Jitter: opts.Jitter, Around: rt.aroundAt(), Started: bind.started,
		Limits: engine.RunLimits{MaxInFlight: opts.Limits.MaxInFlight, MaxHeldBytes: opts.Limits.MaxHeldBytes, Bytes: opts.Limits.Bytes, ByteTotal: rt.byteTotal}}
	if rt.readCache {
		req.Deviations = append(req.Deviations, "D21")
	}
	if rt.memoryStore {
		req.Deviations = append(req.Deviations, "D24")
	}
	if rt.capped {
		req.Deviations = append(req.Deviations, "D15")
	}
	switch {
	case h != nil && extra != nil:
		// Host mode's static intercepts answer first, then the hooks.
		h.calls, req.Hooks = calls.taken, interceptFirst{Hooks: h, first: extra}
	case h != nil:
		h.calls, req.Hooks = calls.taken, h
	case extra != nil:
		req.Hooks = extra
	}
	if settled != nil {
		if req.Hooks == nil {
			req.Hooks = engine.NoHooks{}
		}
		req.Hooks = settledHooks{Hooks: req.Hooks, settled: settled}
	}
	ctx = calls.reportStoreFailures(ctx)
	tr, err := rt.eng.Run(ctx, req)
	if err != nil {
		rt.forgetPin(p)
		return nil, err
	}
	if h != nil {
		// The calls as of the run's end, which the H9 callbacks saw.
		tr.PlatformCalls = h.endCalls()
	} else {
		tr.PlatformCalls = calls.taken()
	}
	tr.Warnings = append(tr.Warnings, calls.warned()...)
	return &Result{
		ID:             tr.RunID,
		Status:         string(tr.Status),
		Result:         storedText(tr.Result),
		MayHaveMutated: tr.MayHaveMutated,
		Trace:          tr,
		Warnings:       tr.Warnings,
		Latched:        tr.Latched,
		CancelCause:    tr.CancelCause,
	}, nil
}

// runServices are the services of a run with its own providers (H13): its
// resources and variables, when p is not nil, then the host plugin's
// which reach the run through run, over the runtime's, and its
// resolver; behind the run's read cache when the runtime has one
// (Options.ReadCache).
func (rt *Runtime) runServices(p *RunPlatform, run *runBinding) (*platform.Services, error) {
	var store *platform.Store
	var resolvers []platform.ValueResolver
	if p != nil {
		store = platform.NewStore()
		for path, r := range p.Resources {
			if err := store.SetResource(path, platform.Resource{Type: r.Type, Value: r.Value}); err != nil {
				return nil, fmt.Errorf("wl: RunPlatform.Resources[%s]: %w", path, err)
			}
		}
		for path, v := range p.Variables {
			store.SetVariable(path, platform.Variable{Value: v.Value, IsSecret: v.Secret, Encrypted: v.Ciphertext})
		}
		if p.Resolve != nil {
			resolvers = append(slices.Clone(rt.services.Resolvers), resolver(p.Resolve))
		}
	}
	return rt.pluginServices(run, store, resolvers), nil
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
	// key is the facade's RegistrationKey of the invocation's job token: an
	// answer delivered late for an earlier invocation of the same id, a
	// root script run again with its id, carries another key.
	key string
}

// callLog is the platform calls of one run, and its invocations, which
// end forgets (guarded by the recorder's mu).
type callLog struct {
	mu       sync.Mutex
	calls    []PlatformCall
	invs     []string
	warnings []Warning
}

func (l *callLog) taken() []PlatformCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]PlatformCall(nil), l.calls...)
}

// warned is the run's warnings of a failure of the resource store: one for
// each platform call it failed, which the facade's answer asked for, and
// one for each read of the run's own ($res: arguments, flow_env,
// resource()) that it failed.
func (l *callLog) warned() []Warning {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Warning(nil), l.warnings...)
}

// reportStoreFailures returns ctx with the report of the resource store's
// failures in reads that no platform call makes (a $res: argument,
// flow_env, resource()), which l keeps as warnings of the run.
func (l *callLog) reportStoreFailures(ctx context.Context) context.Context {
	return platform.WithStoreFailureReport(ctx, func(err error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.warnings = append(l.warnings, Warning{Code: "resource_store_failed", Message: "engine read: Internal: " + err.Error()})
	})
}

// begin registers the call log of run, and returns false, registering
// nothing, while a run of that id is in flight: its log, and the calls and
// warnings it collects, stay its own.
func (x *recorder) begin(run string) (*callLog, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.runs[run] != nil {
		return nil, false
	}
	l := &callLog{}
	x.runs[run] = l
	return l, true
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
	x.invs[inv.ID] = invocation{run: inv.Run, site: inv.Site, key: facade.RegistrationKey(inv.Env["WM_TOKEN"])}
	if l := x.runs[inv.Run]; l != nil {
		l.invs = append(l.invs, inv.ID)
	}
	x.mu.Unlock()
	return x.inner.Execute(ctx, inv)
}

// answered is the facade's Config.Answered: a call, which goes to its
// invocation's run while the run is in flight. A call answered after its
// run ended, an abandoned step's, is dropped, and so is one whose
// registration is not the invocation's now: an earlier run's late answer
// for an id that a later run reuses.
func (x *recorder) answered(inv string, c facade.Record) {
	x.mu.Lock()
	in, ok := x.invs[inv]
	l := x.runs[in.run]
	x.mu.Unlock()
	if !ok || l == nil || c.Registration != in.key {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, PlatformCall{
		Site: in.site, Job: inv, Method: c.Method, Path: c.Path, Query: c.Query, Route: c.Route,
		Status: c.Status, Unsupported: c.Unsupported, Message: c.Message,
	})
	if c.Warning != "" {
		l.warnings = append(l.warnings, Warning{Code: "resource_store_failed", Message: c.Route + ": " + c.Warning})
	}
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

// runData returns a copy of a run's data (RunOptions.Data) as the frames
// carry it: zero for none (nil, JSON whitespace alone, or JSON null), or
// an error for data that is not JSON or not UTF-8, which a harness would
// refuse as a broken frame. The copy is the run's own, so a caller may
// reuse its buffer once Run returns, while the run's late frames still
// carry the data.
func runData(data json.RawMessage) (jsonval.Value, error) {
	// Only JSON's own whitespace: anything else around the value, such as
	// a no-break space, is not JSON.
	trimmed := bytes.Trim(data, " \t\r\n")
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return jsonval.Value{}, nil
	}
	if !json.Valid(trimmed) {
		return jsonval.Value{}, errors.New("wl: the run's data is not JSON")
	}
	if !utf8.Valid(trimmed) {
		return jsonval.Value{}, errors.New("wl: the run's data is not UTF-8")
	}
	return jsonval.FromProducer(bytes.Clone(trimmed)), nil
}

// withRunData returns ctx carrying run's data, if it has any, so that the
// run's invocations and hook calls take it to the processes they reach,
// the data, and the run's settled callback, which then also releases the
// data. A run without data keeps settled as it is, and ctx too, unless ctx
// carries another run's data, which it then hides.
func withRunData(ctx context.Context, run string, data jsonval.Value, settled func()) (context.Context, *harness.RunData, func()) {
	rd := harness.NewRunData(run, data)
	if rd == nil {
		// A run without data: none that ctx carries from another run, as
		// one that a Go hook starts with its context does, reaches it.
		return harness.WithRunData(ctx, nil), nil, settled
	}
	return harness.WithRunData(ctx, rd), rd, func() {
		rd.Release()
		if settled != nil {
			settled()
		}
	}
}

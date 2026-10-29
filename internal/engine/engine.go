package engine

import (
	"bytes"
	"cmp"
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// Engine runs the flows of one compiled program (ir.Program, ADR 0002):
// the flow interpreter of ADR 0003, milestone N1a's engine core with
// N1b's errors, N2's waits and time, N3a's for loops and N3b's while
// loops and branches. It runs a flow's top-level modules in sequence,
// through Windmill's decision list (FX-2): input transforms and the
// argument pipeline (FX-7, ADR 0005), the results routes of expressions
// (FX-10, JE-18), skip_if (FX-17), mock (FX-18), identity (FX-16),
// stop_after_if and its stops (FX-19, FX-21), sleep (FX-23), the step
// timeout (FX-24, FX-25), retry and retry_if (FX-26 to FX-29),
// continue_on_error (FX-30), the failure module and recovery (FX-31 to
// FX-33), chaining errors (FX-15), the final result (FX-36), the
// cancellation outcome (FX-38), the monitor of flows that hang between
// steps (JE-2), and the host hooks of K1 with their precedence (ADR 0008).
// Features that later milestones implement are refused when the engine
// opens, with ErrUnsupported, never midway through a run (ADR 0001 §3's
// rule).
//
// An Engine is safe for concurrent use: runs share nothing but the program,
// the expression pipeline and the step executor.
type Engine struct {
	prog   *ir.Program
	bundle string
	exec   StepExecutor
	eval   expr.Evaluator
	host   platform.Host
	cap    time.Duration
	// transition is the longest a flow may spend between two jobs
	// evaluating expressions before the monitor cancels it (JE-2); zero
	// disables the monitor.
	transition time.Duration
	// platform is the platform providers the run reads (PF2).
	platform *platform.Services
	// results is the result cache of cache_ttl (N4): the platform's, or
	// the engine's own when it has none.
	results platform.ResultCache
	// moduleState is the module-state mode every invocation runs in.
	moduleState ModuleState
}

// Config configures an Engine.
type Config struct {
	// Program is the compiled program; its expressions are compiled
	// (ir.Version 2). Bundle is the bundle's digest, recorded in every
	// trace.
	Program *ir.Program
	Bundle  string
	// Exec runs the steps unless a run names its own.
	Exec StepExecutor
	// JS is the engine the program's expressions run on, when no shortcut
	// answers them; New compiles their programs on it (expr.Pipeline).
	JS expr.Engine
	// Workspace is WM_WORKSPACE; empty means Host's, else "windmill-lite".
	Workspace string
	// Host is the rest of the job environment's host configuration
	// (WM_EMAIL, WM_USERNAME, WM_PERMISSIONED_AS, WM_BASE_URL); zero fields
	// take platform.DefaultHost's.
	Host platform.Host
	// InstanceTimeout is the instance's maximum job duration (WR-37); zero
	// means Windmill's self-hosted default of 604800 s.
	InstanceTimeout time.Duration
	// TransitionTimeout is Windmill's FLOW_ZOMBIE_TRANSITION_TIMEOUT: the
	// longest a flow may spend evaluating expressions between two jobs
	// before the run is canceled by "monitor", as Windmill's monitor of
	// flows that hang between steps cancels them (JE-2). Zero means
	// Windmill's default of 60 s; a negative value disables the monitor, as
	// DISABLE_ZOMBIE_JOBS_MONITORING does.
	TransitionTimeout time.Duration
	// Platform holds the platform services runs use (ADR 0007): the
	// resources, variables and secrets read through a leaf's argument
	// resolution, flow_env and the expression helpers (milestone PF2), and
	// the job registry each run is registered with and the flow user state
	// flow_user_state() reads (milestone PF3). nil means none, so every
	// read is a not-found and no run is registered. A host gives the
	// facade the same services. Its result cache holds the results of the
	// jobs that run with a cache_ttl (milestone N4, FX-40); with no
	// services, the engine keeps one of its own.
	Platform *platform.Services
	// ModuleState is the module-state mode the engine's invocations run in
	// (ADR 0006), which its Exec must serve: ModuleStateWarm, the default,
	// or ModuleStateFresh.
	ModuleState ModuleState
}

// ErrUnsupported marks a program that uses a feature the engine does not
// run yet, because a later milestone of ADR 0012 implements it.
var ErrUnsupported = errors.New("not supported by the engine yet")

// New opens a program: it checks that every flow stays within what the
// engine runs, and compiles every expression's program on cfg.JS
// (ADR 0003: bytecode once per bundle, never per run).
func New(cfg Config) (*Engine, error) {
	if cfg.Program == nil {
		return nil, errors.New("engine: no program")
	}
	if cfg.JS == nil {
		return nil, errors.New("engine: no expression engine")
	}
	e := &Engine{prog: cfg.Program, bundle: cfg.Bundle, exec: cfg.Exec, host: cfg.Host, cap: cfg.InstanceTimeout, platform: cfg.Platform,
		results: resultCache(cfg.Platform), moduleState: cfg.ModuleState}
	switch e.moduleState {
	case "":
		e.moduleState = ModuleStateWarm
	case ModuleStateWarm, ModuleStateFresh:
	default:
		return nil, fmt.Errorf("engine: module state %q is not warm or fresh", cfg.ModuleState)
	}
	if cfg.Workspace != "" {
		e.host.Workspace = cfg.Workspace
	}
	if e.cap == 0 {
		e.cap = 604800 * time.Second
	}
	for _, path := range cfg.Program.Roots.Scripts {
		if err := supportedRootScript(cfg.Program.Scripts[path]); err != nil {
			return nil, fmt.Errorf("engine: script %s: %w", path, err)
		}
	}
	switch t := cfg.TransitionTimeout; {
	case t == 0:
		e.transition = 60 * time.Second
	case t > 0:
		e.transition = t
	}
	var exprs []*expr.Expr
	for _, path := range sortedKeys(cfg.Program.Flows) {
		f := cfg.Program.Flows[path]
		if err := supported(f, cfg.Program.Scripts); err != nil {
			return nil, fmt.Errorf("engine: flow %s: %w", path, err)
		}
		walk(flowModules(f), func(m *ir.Module) {
			if m.SkipIf != nil {
				exprs = append(exprs, m.SkipIf)
			}
			if m.StopAfterIf != nil {
				exprs = append(exprs, m.StopAfterIf.Expr)
			}
			if m.StopAfterAllItersIf != nil {
				exprs = append(exprs, m.StopAfterAllItersIf.Expr)
			}
			for _, t := range []*ir.Transform{m.Sleep, m.Timeout} {
				if t != nil && t.Expr != nil {
					exprs = append(exprs, t.Expr)
				}
			}
			if m.Retry != nil && m.Retry.RetryIf != nil {
				exprs = append(exprs, m.Retry.RetryIf)
			}
			if m.Leaf != nil || m.Subflow != nil {
				for _, in := range inputsOf(m) {
					if in.Transform.Expr != nil {
						exprs = append(exprs, in.Transform.Expr)
					}
				}
			}
			if l := m.ForLoop; l != nil {
				if l.Iterator.Expr != nil {
					exprs = append(exprs, l.Iterator.Expr)
				}
				if l.Parallelism != nil && l.Parallelism.Expr != nil {
					exprs = append(exprs, l.Parallelism.Expr)
				}
			}
			if b := m.BranchOne; b != nil {
				for _, br := range b.Branches {
					exprs = append(exprs, br.Expr)
				}
			}
		})
	}
	for _, x := range exprs {
		if x.Mode == 0 {
			return nil, fmt.Errorf("engine: an expression at a %s site is not compiled (ir.json version 1?)", x.Site)
		}
	}
	p := expr.NewPipeline(cfg.JS)
	p.Prepare(exprs...)
	e.eval = p
	return e, nil
}

// walk calls fn for every module of ms, a container's body modules after
// the container.
func walk(ms []*ir.Module, fn func(*ir.Module)) {
	for _, m := range ms {
		fn(m)
		for _, body := range bodies(m) {
			walk(body, fn)
		}
	}
}

// bodies returns the module lists of a container: a loop's body, a
// branchone's branches then its default, a branchall's branches.
func bodies(m *ir.Module) [][]*ir.Module {
	switch {
	case m.ForLoop != nil:
		return [][]*ir.Module{m.ForLoop.Modules}
	case m.WhileLoop != nil:
		return [][]*ir.Module{m.WhileLoop.Modules}
	case m.BranchOne != nil:
		var out [][]*ir.Module
		for _, br := range m.BranchOne.Branches {
			out = append(out, br.Modules)
		}
		return append(out, m.BranchOne.Default)
	case m.BranchAll != nil:
		var out [][]*ir.Module
		for _, br := range m.BranchAll.Branches {
			out = append(out, br.Modules)
		}
		return out
	}
	return nil
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrUnsupported)
}

// flowModules returns f's top-level modules and then its failure module,
// when it has one: every module the engine may start.
func flowModules(f *ir.Flow) []*ir.Module {
	if f.FailureModule == nil {
		return f.Modules
	}
	return append(slices.Clip(f.Modules), f.FailureModule)
}

// supported checks that f uses only what the engine runs, scripts being
// the program's inventory. Each refusal names the milestone of ADR 0012
// that implements the feature. A flow's own timeout (its metadata's,
// OF-16) is accepted and has no effect: a flow job bypasses every executor
// timer, so nothing enforces it (FX-24, path 4). N2's waits and retries run
// on every module, containers and their bodies included (milestone NI).
func supported(f *ir.Flow, scripts map[string]*ir.Script) error {
	switch {
	case f.PreprocessorModule != nil:
		return unsupported("a preprocessor module")
	case f.SkipExpr != nil:
		return unsupported("a flow skip_expr")
	case f.CacheIgnoreS3Path != nil && *f.CacheIgnoreS3Path:
		return unsupported("cache_ignore_s3_path (later)")
	}
	// A container in the failure module's slot runs its bodies raw, each
	// carrying a copy of the failure module with a suffixed id (CF-30,
	// FX-34), which the reference describes only as source-traced.
	if f.FailureModule != nil && isContainer(f.FailureModule.Kind) {
		return unsupported("a %s as the failure module (CF-30)", f.FailureModule.Kind)
	}
	for _, m := range flowModules(f) {
		if err := supportedModule(m, scripts); err != nil {
			if m == f.FailureModule {
				return fmt.Errorf("failure module %q: %w", m.ID, err)
			}
			return fmt.Errorf("module %q: %w", m.ID, err)
		}
	}
	return nil
}

// supportedList checks the modules of a container's body.
func supportedList(ms []*ir.Module, scripts map[string]*ir.Script) error {
	for _, m := range ms {
		if err := supportedModule(m, scripts); err != nil {
			return fmt.Errorf("module %q: %w", m.ID, err)
		}
	}
	return nil
}

// isContainer reports whether modules of kind k run their bodies as
// containers (CF-1): for and while loops, branchone and branchall.
func isContainer(k ir.Kind) bool {
	return k == ir.KindForLoop || k == ir.KindWhileLoop || k == ir.KindBranchOne || k == ir.KindBranchAll
}

func supportedModule(m *ir.Module, scripts map[string]*ir.Script) error {
	switch m.Kind {
	case ir.KindScript, ir.KindRawScript:
		l := m.Leaf
		if l == nil {
			return fmt.Errorf("a %s module with no leaf", m.Kind)
		}
		if l.Harness != ir.ClassBun || (l.Boundary != ir.BoundaryBun && l.Boundary != ir.BoundaryNative) {
			return unsupported("a %s step in the %s boundary", l.Harness, l.Boundary)
		}
	case ir.KindIdentity:
	case ir.KindSubflow:
		switch r := m.Retry; {
		case m.Subflow == nil:
			return errors.New("a flow module with no subflow")
		case r != nil && (r.RetryIf != nil || r.Constant != nil && r.Constant.Attempts > 0 || r.Exponential != nil && r.Exponential.Attempts > 0):
			// A retried attempt's subflow steps belong under its attempt's
			// site (ADR 0011); no case of NI's, the milestone that integrates
			// N2's retries with child flows, covers it (ADR 0012).
			return unsupported("a retry on a subflow step (later)")
		}
	case ir.KindForLoop:
		if err := supportedLoop(m.ForLoop, scripts); err != nil {
			return err
		}
	case ir.KindWhileLoop:
		if m.WhileLoop == nil {
			return errors.New("a whileloopflow module with no loop")
		}
		if err := supportedList(m.WhileLoop.Modules, scripts); err != nil {
			return err
		}
	case ir.KindBranchOne, ir.KindBranchAll:
		if m.Kind == ir.KindBranchOne && m.BranchOne == nil || m.Kind == ir.KindBranchAll && m.BranchAll == nil {
			return fmt.Errorf("a %s module with no branches", m.Kind)
		}
		for _, body := range bodies(m) {
			if err := supportedList(body, scripts); err != nil {
				return err
			}
		}
	case ir.KindProvided:
		return unsupported("a host-provided site (F2a)")
	default:
		return unsupported("a %s module", m.Kind)
	}
	switch {
	case m.Sleep != nil && m.Sleep.Type == ir.AI:
		return unsupported("an ai sleep (later)")
	case m.Timeout != nil && m.Timeout.Type == ir.AI:
		return unsupported("an ai timeout (later)")
	case m.CacheIgnoreS3Path != nil && *m.CacheIgnoreS3Path:
		return unsupported("cache_ignore_s3_path (later)")
	}
	if t := m.Timeout; t != nil && t.Type == ir.Static {
		if _, err := staticTimeout(t.Value); err != nil && err != errNegativeTimeout {
			return err
		}
	}
	return nil
}

// supportedRootScript checks a script root, which the default dispatcher
// runs as a script job (RunScript): a Bun step in either boundary.
func supportedRootScript(s *ir.Script) error {
	switch {
	case s == nil:
		return errors.New("not in the program's inventory")
	case s.Harness != ir.ClassBun || (s.Boundary != ir.BoundaryBun && s.Boundary != ir.BoundaryNative):
		return unsupported("a %s script in the %s boundary", s.Harness, s.Boundary)
	}
	return nil
}

// supportedLoop checks a for loop: its body, and the fields the engine
// runs.
func supportedLoop(l *ir.ForLoop, scripts map[string]*ir.Script) error {
	switch {
	case l == nil:
		return errors.New("a forloopflow module with no loop")
	case l.Parallelism != nil && l.Parallelism.Type == ir.AI:
		// An ai iterator fails the loop with CF-2's text; the reference
		// gives none for an ai parallelism.
		return unsupported("an ai parallelism")
	}
	return supportedList(l.Modules, scripts)
}

// staticTimeout reads a static step timeout: seconds, or nil for none.
// The decoder admits only an i32 or null (OF-V2); a negative one fails the
// module's start with errNegativeTimeout, a chaining error (FX-24, FX-15).
func staticTimeout(raw []byte) (*int64, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return nil, nil
	}
	secs, err := strconv.ParseInt(string(t), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("engine: timeout %s is not an i32", t)
	}
	if secs < 0 {
		return nil, errNegativeTimeout
	}
	return &secs, nil
}

// RunRequest is one run of a flow.
type RunRequest struct {
	// ID is the run's id, which steps see as WM_FLOW_JOB_ID; empty means a
	// new UUID. In host mode it is the id of the host's run frame, so that
	// the host can re-enter the scope it captured (H11).
	ID string
	// Path is the flow's path in the program; Args is the flow input, a
	// JSON object.
	Path string
	Args jsonval.Value
	// Hooks are the run's hooks; nil means NoHooks.
	Hooks Hooks
	// Exec runs the run's steps; nil means Config.Exec. In host mode it is
	// the host's connection.
	Exec StepExecutor
	// ParentJob and RootJob are the parent_job and root_job of a dispatch
	// tracked in its parent (WA-15), "" when absent, and ParentPath the
	// parent job's runnable path. As the oracle recorded for a flow that
	// runFlow dispatched (fixture client-api/dispatch, run identity), the
	// effective root of the run's jobs is then the first of RootJob,
	// ParentJob and the run that is set, so they share that run's flow
	// user state, and RootJob, when set, is the innermost scope of the
	// run's own steps; the run's own job has ParentJob for its parent.
	ParentJob, RootJob, ParentPath string
	// Seed seeds the run's shuffles of unspecified orders: the top-level
	// keys of the flow input and of every argument map, and the order input
	// transforms are evaluated in (ADR 0001 §2, FX-42, JE-V5), and the
	// retry jitter when Jitter is nil. Zero means a random seed.
	Seed uint64
	// Clock is the run's clock (H10), through which every sleep and retry
	// delay waits; nil means RealClock. A run whose clock is not RealClock,
	// such as a *RecordClock, records H10 among its trace's deviations.
	Clock Clock
	// Jitter is the run's retry-jitter source (H10, FX-26); nil draws from
	// a source seeded with Seed.
	Jitter Jitter
	// PreWriteDeadline is the run's pre-write deadline (H4): when it passes
	// before the run's first write-capable module launched (H3), the run
	// ends canceled, as at its hard deadline, the context's. Zero means
	// none.
	PreWriteDeadline time.Time
}

// ErrPreWriteDeadline is the cause of a run's context when its pre-write
// deadline passed before its first write (H4). It wraps
// context.DeadlineExceeded.
var ErrPreWriteDeadline = fmt.Errorf("engine: the run's pre-write deadline passed before its first write: %w", context.DeadlineExceeded)

// Run runs a flow to its end and returns its trace. ctx is the run's
// cancellation and deadline (H4): a run whose ctx ends finishes canceled,
// with the Canceled error of FX-38 as its result, which names the reason
// and the canceler when ctx's cause is a *Cancellation. An error means the
// run could not start: an unknown flow, arguments that are not a JSON
// object or that the job API could not store, or no step executor.
func (e *Engine) Run(ctx context.Context, req RunRequest) (*Trace, error) {
	f := e.prog.Flows[req.Path]
	if f == nil {
		return nil, fmt.Errorf("engine: no flow %q in the program", req.Path)
	}
	if args := bytes.TrimSpace(req.Args.Producer()); len(args) == 0 || args[0] != '{' {
		return nil, errors.New("engine: the arguments of a flow run are a JSON object")
	}
	// The job API stores the flow input as jsonb before anything reads it
	// (JE-44): flow_input and the first step's previous_result are a
	// compact object the worker writes from the stored arguments, its
	// top-level keys in an unspecified order (FX-42 item 1, ADR 0005) and
	// each member in jsonb's printed form (JE-44's table of texts).
	stored, err := req.Args.Stored()
	if err != nil {
		return nil, fmt.Errorf("engine: the arguments cannot be stored: %w", err)
	}
	hooks := req.Hooks
	if hooks == nil {
		hooks = NoHooks{}
	}
	exec := req.Exec
	if exec == nil {
		exec = e.exec
	}
	if exec == nil && e.hasLeaf(f, map[string]bool{}) {
		return nil, errors.New("engine: no step executor for the run")
	}
	id := req.ID
	if id == "" {
		id = newUUID()
	}
	seed := req.Seed
	if seed == 0 {
		var b [8]byte
		_, _ = cryptoRead(b[:])
		seed = binary.LittleEndian.Uint64(b[:])
	}
	clock := req.Clock
	if clock == nil {
		clock = RealClock{}
	}
	jitter := req.Jitter
	if jitter == nil {
		jitter = SeededJitter(seed ^ 0xa0761d6478bd642f)
	}
	// The run's own context: the monitor of JE-2 and the pre-write
	// deadline (H4) cancel it with their causes, as a host's cancellation
	// would.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	r := newRun(e, f, id, hooks, exec, rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), clock, jitter, cancel)
	r.rootID = cmp.Or(req.RootJob, req.ParentJob, id)
	r.root.scope = cmp.Or(req.RootJob, id)
	r.root.input = jsonval.FromProducer(flowInputText(stored, r.root.rng))
	r.root.envSrc = flowEnv(f)
	if _, real := clock.(RealClock); !real {
		r.trace.Deviations = append(r.trace.Deviations, "H10")
	}
	if !req.PreWriteDeadline.IsZero() {
		// H4: a deadline that has passed cancels the run before it starts;
		// a later one expires on time, unless the first write disarmed it
		// first (run.disarmPreWrite).
		r.preWriteAt = req.PreWriteDeadline
		if !time.Now().Before(r.preWriteAt) {
			r.expirePreWrite()
		} else {
			t := time.AfterFunc(time.Until(r.preWriteAt), func() { r.expirePreWrite() })
			defer t.Stop()
		}
	}
	// The flow job's reserved variables, which flow_env's resolution reads
	// (JE-38), a subflow's included: a top-level flow, with no parent, its
	// own effective root, at the flow's path; or a dispatched one's, with
	// the links its dispatch sent.
	r.flowJobEnv = platform.Identity{JobID: id, FlowJobID: req.ParentJob, RootFlowJobID: req.RootJob, RootJobID: r.rootID, FlowPath: req.ParentPath, JobPath: f.Path, Token: platform.NewToken()}.Env(e.host)
	// The run is a flow job of the job registry while it runs, in its
	// effective root's tree, so that its steps' root lookups, reads with
	// a job id and flow user state find it (ADR 0007, D3, WA-14); its user
	// state is discarded when its tree is.
	endJob := e.platform.JobRegistry().Begin(platform.Job{ID: id, Kind: platform.JobFlow, Root: r.rootID, Path: f.Path})
	r.run(ctx)
	endJob()
	// H9: OnRunEnd sees the outcome first; its warnings are part of the
	// returned trace and of the one OnRunSettled receives, possibly at once
	// from End when no invocation was abandoned.
	t := r.snapshot()
	w := hooks.OnRunEnd(ctx, t)
	r.mu.Lock()
	r.trace.Warnings = append(r.trace.Warnings, w...)
	r.mu.Unlock()
	t.Warnings = append(t.Warnings, w...)
	r.settle.End()
	return t, nil
}

// Cancellation is the cause a host cancels a run with (H4,
// context.WithCancelCause): who canceled it and why, as Windmill's cancel
// records them (FX-38). An empty field takes Windmill's default, "no reason
// given" for the reason and "unknown" for the canceler, as does a run whose
// context ends with any other cause.
type Cancellation struct {
	Reason   string
	Canceler string
}

func (c *Cancellation) Error() string {
	return fmt.Sprintf("engine: run canceled by %s: %s", c.Canceler, c.Reason)
}

// cancellationOf returns the reason and the canceler of a run whose ctx has
// ended, with Windmill's defaults (FX-38).
func cancellationOf(ctx context.Context) (reason, canceler string) {
	reason, canceler = "no reason given", "unknown"
	var c *Cancellation
	if errors.As(context.Cause(ctx), &c) {
		if c.Reason != "" {
			reason = c.Reason
		}
		if c.Canceler != "" {
			canceler = c.Canceler
		}
	}
	return reason, canceler
}

// flowEnv is the flow's flow_env as an object in its stored form: the flow
// value is stored as jsonb, so its keys come in jsonb's order (JE-38,
// ADR 0005), or the zero Value when the flow has none, where expressions
// see {} (JE-10). Its $var: and $res: references are resolved on every
// transition, into the frame that starts a module (run.resolveEnv).
func flowEnv(f *ir.Flow) jsonval.Value {
	if len(f.FlowEnv) == 0 {
		return jsonval.Value{}
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, v := range f.FlowEnv {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonString(v.Name))
		b.WriteByte(':')
		b.Write(v.Value)
	}
	b.WriteByte('}')
	v := jsonval.FromProducer(b.Bytes())
	if s, err := v.Stored(); err == nil {
		return jsonval.FromProducer(s)
	}
	return v
}

// hasLeaf reports whether a step may run anywhere in f, the subflows it
// calls by path included; seen holds the flows already visited.
func (e *Engine) hasLeaf(f *ir.Flow, seen map[string]bool) bool {
	seen[f.Path] = true
	found := false
	walk(flowModules(f), func(m *ir.Module) {
		switch {
		case found:
		case m.Leaf != nil:
			found = true
		case m.Subflow != nil && !seen[m.Subflow.Path]:
			if sub := e.prog.Flows[m.Subflow.Path]; sub != nil {
				found = e.hasLeaf(sub, seen)
			}
		}
	})
	return found
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// cryptoRead fills b from the system's random source.
var cryptoRead = crand.Read

// newUUID returns a random (version 4) UUID: windmill-lite's job ids (D3).
func newUUID() string {
	var b [16]byte
	_, _ = cryptoRead(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

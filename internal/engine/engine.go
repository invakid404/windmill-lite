package engine

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Engine runs the flows of one compiled program (ir.Program, ADR 0002):
// the flow interpreter of ADR 0003, milestone N1a's engine core with
// N1b's errors. It runs a flow's top-level modules in sequence, through
// Windmill's decision list (FX-2): input transforms and the argument
// pipeline (FX-7, ADR 0005), the results routes of expressions (FX-10,
// JE-18), skip_if (FX-17), mock (FX-18), identity (FX-16), stop_after_if
// and its stops (FX-19, FX-21), a static step timeout, continue_on_error
// (FX-30), the failure module and recovery (FX-31 to FX-33), chaining
// errors (FX-15), the final result (FX-36), the cancellation outcome
// (FX-38), and the host hooks of K1 with their precedence (ADR 0008).
// Features that later milestones implement are refused when the engine
// opens, with ErrUnsupported, never midway through a run (ADR 0001 §3's
// rule).
//
// An Engine is safe for concurrent use: runs share nothing but the program,
// the expression pipeline and the step executor.
type Engine struct {
	prog      *ir.Program
	bundle    string
	exec      StepExecutor
	eval      expr.Evaluator
	workspace string
	cap       time.Duration
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
	// Workspace is WM_WORKSPACE; empty means "windmill-lite".
	Workspace string
	// InstanceTimeout is the instance's maximum job duration (WR-37); zero
	// means Windmill's self-hosted default of 604800 s.
	InstanceTimeout time.Duration
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
	e := &Engine{prog: cfg.Program, bundle: cfg.Bundle, exec: cfg.Exec, workspace: cfg.Workspace, cap: cfg.InstanceTimeout}
	if e.workspace == "" {
		e.workspace = "windmill-lite"
	}
	if e.cap == 0 {
		e.cap = 604800 * time.Second
	}
	var exprs []*expr.Expr
	for _, path := range sortedKeys(cfg.Program.Flows) {
		f := cfg.Program.Flows[path]
		if err := supported(f); err != nil {
			return nil, fmt.Errorf("engine: flow %s: %w", path, err)
		}
		for _, m := range flowModules(f) {
			if m.SkipIf != nil {
				exprs = append(exprs, m.SkipIf)
			}
			if m.StopAfterIf != nil {
				exprs = append(exprs, m.StopAfterIf.Expr)
			}
			if m.Leaf != nil {
				for _, in := range m.Leaf.Inputs {
					if in.Transform.Expr != nil {
						exprs = append(exprs, in.Transform.Expr)
					}
				}
			}
		}
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

// supported checks that f uses only what the engine runs. Each refusal
// names the milestone of ADR 0012 that implements the feature.
func supported(f *ir.Flow) error {
	switch {
	case f.PreprocessorModule != nil:
		return unsupported("a preprocessor module")
	case f.SkipExpr != nil:
		return unsupported("a flow skip_expr")
	case f.CacheTTL != nil:
		return unsupported("a flow cache_ttl (N4)")
	case f.Timeout != nil:
		return unsupported("a flow timeout (N2)")
	}
	for _, m := range flowModules(f) {
		if err := supportedModule(m); err != nil {
			if m == f.FailureModule {
				return fmt.Errorf("failure module %q: %w", m.ID, err)
			}
			return fmt.Errorf("module %q: %w", m.ID, err)
		}
	}
	return nil
}

func supportedModule(m *ir.Module) error {
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
		return unsupported("a subflow by path (N3c)")
	case ir.KindForLoop, ir.KindWhileLoop:
		return unsupported("a %s (N3a, N3b)", m.Kind)
	case ir.KindBranchOne, ir.KindBranchAll:
		return unsupported("a %s (N3b)", m.Kind)
	case ir.KindProvided:
		return unsupported("a host-provided site (F2a)")
	default:
		return unsupported("a %s module", m.Kind)
	}
	r := m.Retry
	switch {
	case m.Sleep != nil:
		return unsupported("sleep (N2)")
	case m.Timeout != nil && m.Timeout.Type != ir.Static:
		return unsupported("a %s timeout (N2)", m.Timeout.Type)
	case r != nil && (r.RetryIf != nil || r.Constant != nil && r.Constant.Attempts > 0 || r.Exponential != nil && r.Exponential.Attempts > 0):
		return unsupported("retry (N2)")
	case m.CacheTTL != nil:
		return unsupported("cache_ttl (N4)")
	case m.StopAfterAllItersIf != nil:
		return unsupported("stop_after_all_iters_if (N3a, N3b)")
	}
	if t := m.Timeout; t != nil {
		if _, err := staticTimeout(t.Value); err != nil {
			return err
		}
	}
	return nil
}

// staticTimeout reads a static step timeout: seconds, or nil for none
// (OF-V2). A negative timeout fails the step's start with a chaining error
// (FX-15), which is milestone N2's with the timeout's messages.
func staticTimeout(raw []byte) (*int64, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return nil, nil
	}
	var secs int64
	if _, err := fmt.Sscan(string(t), &secs); err != nil || fmt.Sprint(secs) != string(t) {
		return nil, fmt.Errorf("timeout %s is not an integer", t)
	}
	if secs < 0 {
		return nil, unsupported("a negative timeout (N2)")
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
	// Seed seeds the run's shuffles of unspecified orders: the top-level
	// keys of the flow input and of every argument map, and the order input
	// transforms are evaluated in (ADR 0001 §2, FX-42, JE-V5). Zero means a
	// random seed.
	Seed uint64
}

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
	if exec == nil && slices.ContainsFunc(flowModules(f), func(m *ir.Module) bool { return m.Leaf != nil }) {
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
	r := newRun(e, f, id, hooks, exec, rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)))
	r.input = jsonval.FromProducer(flowInputText(stored, r.rng))
	r.env = flowEnv(f)
	r.run(ctx)
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
// see {} (JE-10). Its $var: and $res: references are resolved by the
// platform providers (PF2); until then the values are bound as written.
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

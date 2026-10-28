package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/openflow"
)

// Skeleton is the walking skeleton's flow interpreter (milestone W1 of
// ADR 0012). It exists to run the real process model end to end on the
// pinned Bun before the engine milestones build on it, and is replaced
// piece by piece: the interpreter by N1a, the expression pipeline by B2b,
// the outcome mapping by C3a and N1a, the bundle by F4 and host mode's
// transport by F3a. Its review bar is that it retires W1's gates, not that
// its code survives.
//
// It runs one sequential flow at a time per run: script steps by path and
// inline Bun steps, their static and javascript input transforms, a static
// step timeout, H1 interception and the start and completion precedence of
// the latch and cancellation. Anything else fails at load with
// ErrSkeletonUnsupported. A Skeleton is safe for concurrent use.
type Skeleton struct {
	bundle    string
	exec      StepExecutor
	js        expr.Engine
	workspace string
	cap       time.Duration
	flows     map[string]*skelFlow
	programs  sync.Map // engine source -> expr.Program
}

// SkeletonConfig configures a Skeleton.
type SkeletonConfig struct {
	// BundleDir holds the flows, as flows/<flow path>.flow.json: the
	// OpenFlow document of each flow as JSON. Bundle is the bundle's
	// digest, recorded in every trace.
	BundleDir string
	Bundle    string
	// Exec runs the steps unless a run names its own; JS evaluates
	// javascript input transforms.
	Exec StepExecutor
	JS   expr.Engine
	// Workspace is WM_WORKSPACE; empty means "windmill-lite".
	Workspace string
	// InstanceTimeout is the instance's maximum job duration (WR-37); zero
	// means Windmill's self-hosted default of 604800 s.
	InstanceTimeout time.Duration
}

// ErrSkeletonUnsupported marks a flow that uses a feature the walking
// skeleton does not run.
var ErrSkeletonUnsupported = errors.New("not supported by the walking skeleton")

type skelFlow struct {
	path    string
	modules []skelModule
}

type skelModule struct {
	id         string
	script     string // inventory id: the path, or <flow path>/<id> inline
	transforms []skelTransform
	timeout    *int64 // seconds, when the step has a static timeout
}

type skelTransform struct {
	name   string
	static jsonval.Value // a static value, verbatim (JE-40)
	text   string        // a javascript expression as written
	source string        // its engine source
	binds  []string      // the names its text mentions (JE-10)
	ai     bool          // left out of the arguments (JE-23)
}

// NewSkeleton loads every flow of cfg.BundleDir and compiles its
// javascript transforms.
func NewSkeleton(cfg SkeletonConfig) (*Skeleton, error) {
	if cfg.JS == nil {
		return nil, errors.New("engine: the skeleton needs JS")
	}
	s := &Skeleton{bundle: cfg.Bundle, exec: cfg.Exec, js: cfg.JS, workspace: cfg.Workspace, cap: cfg.InstanceTimeout, flows: map[string]*skelFlow{}}
	if s.workspace == "" {
		s.workspace = "windmill-lite"
	}
	if s.cap == 0 {
		s.cap = 604800 * time.Second
	}
	root := filepath.Join(cfg.BundleDir, "flows")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".flow.json") {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		path := filepath.ToSlash(strings.TrimSuffix(rel, ".flow.json"))
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		doc, err := openflow.DecodeFlow(b)
		if err != nil {
			return fmt.Errorf("engine: flow %s: %w", path, err)
		}
		f, err := s.compile(path, doc)
		if err != nil {
			return fmt.Errorf("engine: flow %s: %w", path, err)
		}
		s.flows[path] = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrSkeletonUnsupported)
}

// compile checks that the flow stays within the skeleton and prepares its
// modules.
func (s *Skeleton) compile(path string, doc *openflow.Flow) (*skelFlow, error) {
	v := doc.Value
	switch {
	case v.FailureModule != nil:
		return nil, unsupported("a failure module")
	case v.PreprocessorModule != nil:
		return nil, unsupported("a preprocessor")
	case v.SkipExpr != nil || v.EarlyReturn != nil || v.CacheTTL != nil || len(v.FlowEnv) > 0:
		return nil, unsupported("skip_expr, early_return, cache_ttl and flow_env")
	}
	f := &skelFlow{path: path}
	seen := map[string]bool{}
	for _, m := range v.Modules {
		if seen[m.ID] {
			return nil, unsupported("module id %q occurs twice", m.ID)
		}
		seen[m.ID] = true
		sm, err := s.compileModule(path, m)
		if err != nil {
			return nil, fmt.Errorf("module %q: %w", m.ID, err)
		}
		f.modules = append(f.modules, sm)
	}
	return f, nil
}

func (s *Skeleton) compileModule(flowPath string, m openflow.Module) (skelModule, error) {
	sm := skelModule{id: m.ID}
	switch {
	case m.SkipIf != nil, m.StopAfterIf != nil, m.StopAfterAllItersIf != nil, m.Sleep != nil, m.Suspend != nil,
		m.CacheTTL != nil, m.Retry != nil && m.Retry.MaxRetries() > 0, m.ContinueOnError != nil && *m.ContinueOnError,
		m.Mock != nil && m.Mock.Enabled, m.PassFlowInputDirectly != nil && *m.PassFlowInputDirectly:
		return sm, unsupported("skip_if, stop_after_if, sleep, suspend, cache_ttl, retry, continue_on_error, mock and pass_flow_input_directly")
	}
	if t := m.Timeout; t != nil {
		if t.Type != openflow.Static {
			return sm, unsupported("a %s timeout", t.Type)
		}
		var secs *int64
		if err := json.Unmarshal(t.Value, &secs); err != nil {
			return sm, fmt.Errorf("timeout %s: %w", t.Value, err)
		}
		if secs != nil && *secs < 0 {
			return sm, unsupported("a negative timeout")
		}
		sm.timeout = secs
	}
	var transforms openflow.Transforms
	switch val := m.Value.(type) {
	case *openflow.PathScript:
		sm.script, transforms = val.Path, val.InputTransforms
	case *openflow.RawScript:
		if val.Language != openflow.LangBun {
			return sm, unsupported("%s steps", val.Language)
		}
		// The bundle holds the inline script's module under the job path
		// Windmill gives it (WR-29).
		sm.script, transforms = flowPath+"/"+m.ID, val.InputTransforms
	default:
		return sm, unsupported("%s modules", m.Value.Kind())
	}
	for _, nt := range transforms {
		st := skelTransform{name: nt.Name}
		switch nt.Transform.Type {
		case openflow.Static:
			st.static = jsonval.FromProducer(nt.Transform.Value)
		case openflow.JavaScript:
			st.text = nt.Transform.Expr
			st.source, st.binds = transformSource(nt.Transform.Expr)
			if _, err := s.program(st.source); err != nil {
				var exc *expr.Exception
				if !errors.As(err, &exc) {
					return sm, err
				}
				// A syntax error is raised when the step runs, as Windmill
				// reports it (ADR 0003).
			}
		default:
			st.ai = true
		}
		sm.transforms = append(sm.transforms, st)
	}
	return sm, nil
}

// program compiles source once.
func (s *Skeleton) program(source string) (expr.Program, error) {
	if p, ok := s.programs.Load(source); ok {
		return p, nil
	}
	p, err := s.js.Compile(source)
	if err != nil {
		return nil, err
	}
	s.programs.Store(source, p)
	return p, nil
}

// transformSource is the skeleton's stand-in for B2b's expression pipeline
// at an input-transform site: the text trimmed (JE-5), in expression or
// statement mode (JE-13), on one line with its wrapper (JE-14), the bound
// names parsed from host strings (JE-10), and the value serialised as
// JSON.stringify(value ?? null), with null for a value JSON.stringify has
// no text for (JE-15). It returns the source and the names to bind, which
// are only those the text mentions. The shortcuts and rewrites of JE-6 to
// JE-12 are left out.
func transformSource(text string) (string, []string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return `Promise.resolve("null")`, nil
	}
	body := "return " + t + ";"
	if statementMode(t) {
		body = t
	}
	var b strings.Builder
	var binds []string
	for _, name := range []string{"flow_input", "previous_result", "params"} {
		if strings.Contains(t, name) || (name == "previous_result" && strings.Contains(t, "error")) {
			fmt.Fprintf(&b, "globalThis.%s = JSON.parse(globalThis.%s);\n", name, name)
			binds = append(binds, name)
		}
	}
	b.WriteString(`(async function () { ` + body + ` })().then((v) => JSON.stringify(v ?? null) ?? "null")`)
	return b.String(), binds
}

// statementMode applies JE-13's test.
func statementMode(t string) bool {
	if t == "return" || strings.HasPrefix(t, "return ") || strings.HasPrefix(t, "return;") {
		return true
	}
	for _, p := range []string{"const ", "let ", "var ", "if ", "if(", "for ", "for(", "while ", "while(", "switch ", "switch(", "try ", "try{", "throw ", "function ", "class ", "async ", "await "} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	var quote byte
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c == '\\' && quote != 0:
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == ';':
			return true
		}
	}
	return false
}

// SkeletonRun is one run of a flow.
type SkeletonRun struct {
	// ID is the run's id, which steps see as WM_FLOW_JOB_ID; empty means a
	// new UUID. In host mode it is the id of the host's run frame, so that
	// the host can re-enter the scope it captured (H11).
	ID    string
	Path  string
	Args  jsonval.Value
	Hooks Hooks
	// Exec runs the run's steps; nil means SkeletonConfig.Exec. In host
	// mode it is the host's connection.
	Exec StepExecutor
}

// Run runs a flow to its end and returns its trace. ctx is the run's
// cancellation and deadline (H4). An error means the run could not start.
func (s *Skeleton) Run(ctx context.Context, r SkeletonRun) (*Trace, error) {
	f := s.flows[r.Path]
	if f == nil {
		return nil, fmt.Errorf("engine: no flow %q in the bundle", r.Path)
	}
	if args := bytes.TrimSpace(r.Args.Producer()); len(args) == 0 || args[0] != '{' {
		return nil, fmt.Errorf("engine: the arguments of a flow run are a JSON object")
	}
	hooks := r.Hooks
	if hooks == nil {
		hooks = NoHooks{}
	}
	exec := r.Exec
	if exec == nil {
		exec = s.exec
	}
	if exec == nil {
		return nil, errors.New("engine: no step executor for the run")
	}
	id := r.ID
	if id == "" {
		id = newUUID()
	}
	run := &skelRun{s: s, f: f, id: id, hooks: hooks, input: r.Args, exec: exec}
	run.trace = Trace{RunID: id, Bundle: s.bundle, ModuleState: ModuleStateWarm}
	run.settle = NewRunSettlement(func() { hooks.OnRunSettled(run.snapshot()) })
	run.run(ctx)
	// H9: OnRunEnd sees the outcome first; its warnings are part of the
	// returned trace and of the one OnRunSettled receives, possibly at once
	// from End when no invocation was abandoned.
	t := run.snapshot()
	w := hooks.OnRunEnd(ctx, t)
	run.mu.Lock()
	run.trace.Warnings = append(run.trace.Warnings, w...)
	run.mu.Unlock()
	t.Warnings = append(t.Warnings, w...)
	run.settle.End()
	return t, nil
}

type skelRun struct {
	s      *Skeleton
	f      *skelFlow
	id     string
	hooks  Hooks
	input  jsonval.Value
	exec   StepExecutor
	latch  Latch
	settle *RunSettlement

	mu    sync.Mutex // guards trace, which Reporters append to
	trace Trace
}

func (r *skelRun) ID() string      { return r.id }
func (r *skelRun) Latch(err error) { r.latch.Set(err) }
func (r *skelRun) site(id string) Site {
	return Site{Path: conformance.SitePath{}.Child(conformance.Module(id)), Flow: r.f.path, Module: id}
}

// snapshot copies the trace, so that what the run returns never changes
// while abandoned invocations report late output.
func (r *skelRun) snapshot() *Trace {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.trace
	t.Steps = make([]StepRecord, len(r.trace.Steps))
	for i, st := range r.trace.Steps {
		st.Late = append([]Late(nil), st.Late...)
		t.Steps[i] = st
	}
	t.Warnings = append([]Warning(nil), r.trace.Warnings...)
	t.Spans = append([]Span(nil), r.trace.Spans...)
	return &t
}

func (r *skelRun) end(status conformance.RunStatus, result jsonval.Value, origin Origin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Status, r.trace.ResultOrigin = status, origin
	if status == conformance.RunLatched {
		r.trace.Latched = r.latch.Err()
		return
	}
	if result.IsZero() {
		return
	}
	// The final result is stored as jsonb (FX-36, FX-42).
	if stored, err := result.Stored(); err == nil {
		r.trace.Result = jsonval.FromProducer(stored)
	} else {
		r.trace.Result = result
	}
}

func (r *skelRun) run(ctx context.Context) {
	prev := r.input // FX-6: the first step's previous_result is the flow input
	result := r.input
	origin := OriginNone
	for _, m := range r.f.modules {
		v := Start(StartSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil})
		if st, end := v.Status(); end {
			r.end(st, jsonval.Value{}, OriginNone)
			return
		}
		// The step's slot in the trace is reserved first, so late output of
		// its invocation always has a record to go to.
		r.mu.Lock()
		idx := len(r.trace.Steps)
		r.trace.Steps = append(r.trace.Steps, StepRecord{Site: r.site(m.id).Path})
		r.mu.Unlock()
		rec, ok := r.module(ctx, m, prev, idx)
		r.mu.Lock()
		rec.Late = append(r.trace.Steps[idx].Late, rec.Late...)
		r.trace.Steps[idx] = rec
		r.mu.Unlock()
		if err := r.hooks.OnModuleEnd(ctx, r, r.site(m.id), rec); err != nil {
			r.latch.Set(HookLatchError(ctx, err))
		}
		v = Completion(CompletionSignals{Latched: r.latch.Err() != nil, Canceled: ctx.Err() != nil})
		if st, end := v.Status(); end {
			r.end(st, jsonval.Value{}, OriginNone)
			return
		}
		result, origin = rec.Result, rec.ResultOrigin
		if !ok {
			// No continue_on_error in the skeleton: the failed module's
			// result is the flow's (FX-36).
			r.end(conformance.RunFailure, result, origin)
			return
		}
		prev = result
	}
	r.end(conformance.RunSuccess, result, origin)
}

// module runs one module, whose record is the trace's step idx, and
// reports whether it succeeded.
func (r *skelRun) module(ctx context.Context, m skelModule, prev jsonval.Value, idx int) (StepRecord, bool) {
	site := r.site(m.id)
	site.Script = m.script
	rec := StepRecord{Site: site.Path, Start: time.Now()}
	defer func() { rec.Duration = time.Since(rec.Start) }()
	fail := func(result jsonval.Value, origin Origin) (StepRecord, bool) {
		rec.Status, rec.Module, rec.Result, rec.Carried, rec.ResultOrigin = conformance.StepFailure, conformance.ModuleFailure, result, result, origin
		return rec, false
	}
	// H5: a hook that latches the run on the way to the launch stops it;
	// the module never runs, and the completion check ends the run latched.
	notLaunched := func() (StepRecord, bool) {
		rec.Status, rec.Module = conformance.StepNotRun, conformance.ModuleNotRun
		return rec, false
	}
	if err := r.hooks.OnModuleStart(ctx, r, site); err != nil {
		r.latch.Set(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return notLaunched()
	}
	args, failed := r.args(ctx, m, prev)
	if !failed.IsZero() {
		// FX-14: the job still launches, with empty arguments, and fails
		// without running.
		rec.Ran, rec.RecordedArgs, rec.ResolvedArgs = true, jsonval.FromProducer([]byte("{}")), jsonval.FromProducer([]byte("{}"))
		return fail(failed, OriginEngine)
	}
	rec.RecordedArgs, rec.ResolvedArgs = args, args
	timeout, instanceWide := r.timeout(m)

	out, ok, err := r.hooks.Intercept(ctx, r, site, args)
	if err != nil {
		r.latch.Set(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return notLaunched()
	}
	origin := OriginUser
	if ok {
		rec.Hooks, origin = append(rec.Hooks, "H1"), OriginHost
	} else {
		rec.Ran = true
		out, err = r.invoke(ctx, m, site, args, timeout, idx)
		switch {
		case errors.Is(err, errNotLaunched):
			rec.Ran = false
			return notLaunched()
		case errors.Is(err, CancelTimeout):
			rec.Abandoned = true
			limit := "job-specific duration limit"
			if instanceWide {
				limit = "instance-wide job duration limit"
			}
			return fail(stepError(m.id, "execution error:\njob process terminated due to timeout after exceeding "+limit), OriginEngine)
		case err != nil && ctx.Err() != nil:
			// The run was canceled (H4); the precedence check ends it.
			rec.Abandoned = true
			rec.Status, rec.Module = conformance.StepCanceled, conformance.ModuleFailure
			return rec, false
		case err != nil:
			return fail(engineError("InternalErr", err.Error()), OriginEngine)
		}
	}
	switch out.Kind {
	case OutcomeOK:
		rec.Status, rec.Module, rec.Result, rec.Carried = conformance.StepSuccess, conformance.ModuleSuccess, out.Body, out.Body
		return rec, true
	case OutcomeError:
		// FX-12: the Bun wrapper's object, with step_id added.
		return fail(wrapError(withStepID(out.Body, m.id)), origin)
	case OutcomeEscaped:
		if out.Signal != "" {
			return fail(stepError(m.id, "execution error:\nprocess terminated by signal: "+out.Signal), OriginHarness)
		}
		return fail(escapedError(m.id, out), OriginHarness)
	default:
		return fail(engineError("InternalErr", "the step produced no result"), OriginHarness)
	}
}

// args evaluates the module's input transforms in order (JE-23), and
// returns the argument object, or the failure value of the first transform
// that failed (FX-14).
func (r *skelRun) args(ctx context.Context, m skelModule, prev jsonval.Value) (jsonval.Value, jsonval.Value) {
	var b bytes.Buffer
	b.WriteByte('{')
	n := 0
	for _, t := range m.transforms {
		if t.ai {
			continue
		}
		v := t.static
		if t.source != "" {
			out, err := r.evaluate(ctx, t, prev)
			if err != nil {
				return jsonval.Value{}, engineError("ExecutionErr", "Error during isolated evaluation of expression `"+t.text+"`:\n"+exceptionText(err))
			}
			v = jsonval.FromProducer(out)
		}
		if n > 0 {
			b.WriteByte(',')
		}
		n++
		k, _ := json.Marshal(t.name)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v.Producer())
	}
	b.WriteByte('}')
	return jsonval.FromProducer(b.Bytes()), jsonval.Value{}
}

// errNotLaunched: a hook latched the run before the step launched.
var errNotLaunched = errors.New("engine: the run latched before the step launched")

// evaluate runs one transform's program with the names its text mentions
// bound (JE-10); any other name is unavailable to it.
func (r *skelRun) evaluate(ctx context.Context, t skelTransform, prev jsonval.Value) ([]byte, error) {
	p, err := r.s.program(t.source)
	if err != nil {
		return nil, err
	}
	var in expr.Input
	for _, name := range t.binds {
		text := []byte("{}") // params: transforms never see each other (JE-23)
		switch name {
		case "flow_input":
			text = r.input.Producer()
		case "previous_result":
			text = prev.Producer()
		}
		in.Strings = append(in.Strings, expr.HostString{Name: name, Text: text})
	}
	return r.s.js.Eval(ctx, p, in)
}

func exceptionText(err error) string {
	var exc *expr.Exception
	if errors.As(err, &exc) {
		return exc.Text
	}
	return err.Error()
}

// timeout returns the step's deadline, if any, and whether the instance's
// maximum applies instead of the requested one (WR-37).
func (r *skelRun) timeout(m skelModule) (time.Duration, bool) {
	if m.timeout == nil {
		return 0, false
	}
	d := time.Duration(*m.timeout) * time.Second
	if d >= r.s.cap {
		return r.s.cap, true
	}
	return d, false
}

// invoke runs the step in its harness, with its timeout as a timer in the
// engine (ADR 0004 §Cancellation and deadlines item 2).
func (r *skelRun) invoke(ctx context.Context, m skelModule, site Site, args jsonval.Value, timeout time.Duration, idx int) (StepOutcome, error) {
	inv := Invocation{
		ID:          newUUID(),
		Run:         r.id,
		Site:        site.Path,
		Script:      m.script,
		Entry:       "main",
		Args:        args,
		Boundary:    BoundaryBun,
		ModuleState: ModuleStateWarm,
	}
	inv.Env = map[string]string{
		"WM_WORKSPACE":        r.s.workspace,
		"WM_JOB_ID":           inv.ID,
		"WM_FLOW_JOB_ID":      r.id,
		"WM_ROOT_FLOW_JOB_ID": r.id,
		"WM_ROOT_JOB_ID":      r.id,
		"WM_FLOW_PATH":        r.f.path,
		"WM_FLOW_STEP_ID":     m.id,
		"WM_JOB_PATH":         m.script,
	}
	if err := r.hooks.InvocationEnv(ctx, r, site, inv.Env); err != nil {
		r.latch.Set(HookLatchError(ctx, err))
	}
	if r.latch.Err() != nil {
		return StepOutcome{}, errNotLaunched
	}
	inv.Report = r.settle.Track(&skelReporter{run: r, idx: idx})
	stepCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if timeout > 0 {
		t := time.AfterFunc(timeout, func() { cancel(CancelTimeout) })
		defer t.Stop()
	}
	start := time.Now()
	out, err := r.exec.Execute(stepCtx, inv)
	r.mu.Lock()
	r.trace.Spans = append(r.trace.Spans, Span{Site: site.Path, Kind: SpanHarness, Start: start, Duration: time.Since(start)})
	r.mu.Unlock()
	return out, err
}

// skelReporter records what an invocation produces late in its step's
// record.
type skelReporter struct {
	run *skelRun
	idx int
}

func (p *skelReporter) Late(l Late) {
	r := p.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Steps[p.idx].Late = append(r.trace.Steps[p.idx].Late, l)
}

func (p *skelReporter) Warning(w Warning) {
	r := p.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Warnings = append(r.trace.Warnings, w)
}

func (p *skelReporter) Settled(SettleKind) {}

// StaticIntercepts is H1's no-code form (ADR 0008): a fixed result per
// module id of the run's flow, which stands in for that module's run.
type StaticIntercepts struct {
	NoHooks
	Results map[string]jsonval.Value
}

// Intercept answers for the modules listed in Results.
func (h StaticIntercepts) Intercept(_ context.Context, _ Run, s Site, _ jsonval.Value) (StepOutcome, bool, error) {
	v, ok := h.Results[s.Module]
	if !ok {
		return StepOutcome{}, false, nil
	}
	return StepOutcome{Kind: OutcomeOK, Body: v}, true, nil
}

// The failure values below are the skeleton's; C3a and N1a own the final
// mapping (WR-18, WR-20, WR-21, WR-37, WR-V2, FX-12).

// wrapError wraps E as {"error": E} (WR-21).
func wrapError(e []byte) jsonval.Value {
	return jsonval.FromProducer(append(append([]byte(`{"error":`), e...), '}'))
}

// stepError is a worker-side failure: {"message", "name": "ExecutionErr",
// "step_id"} (FX-12, WR-37).
func stepError(stepID, message string) jsonval.Value {
	b, _ := json.Marshal(struct {
		Message string `json:"message"`
		Name    string `json:"name"`
		StepID  string `json:"step_id"`
	}{message, "ExecutionErr", stepID})
	return wrapError(b)
}

// escapedError is WR-20's log-tail error.
func escapedError(stepID string, out StepOutcome) jsonval.Value {
	b, _ := json.Marshal(struct {
		Message  string `json:"message"`
		Name     string `json:"name"`
		StepID   string `json:"step_id"`
		ExitCode int    `json:"exit_code"`
	}{fmt.Sprintf("exit code for \"bun run\": %d, last log lines:\n%s", out.ExitCode, strings.TrimSpace(out.Tail)), "ExecutionErr", stepID, out.ExitCode})
	return wrapError(b)
}

// engineError is a pre-run failure: {"message", "name"} with no step_id
// (FX-12, FX-14).
func engineError(name, message string) jsonval.Value {
	b, _ := json.Marshal(struct {
		Message string `json:"message"`
		Name    string `json:"name"`
	}{message, name})
	return wrapError(b)
}

// withStepID appends "step_id" to the error object the wrapper produced,
// keeping its own text and key order (FX-12).
func withStepID(body jsonval.Value, stepID string) []byte {
	b := bytes.TrimSpace(body.Producer())
	id, _ := json.Marshal(stepID)
	if len(b) < 2 || b[0] != '{' || b[len(b)-1] != '}' {
		return b
	}
	out := append([]byte(nil), b[:len(b)-1]...)
	if len(bytes.TrimSpace(b[1:len(b)-1])) > 0 {
		out = append(out, ',')
	}
	out = append(out, `"step_id":`...)
	return append(append(out, id...), '}')
}

// newUUID returns a random (version 4) UUID: windmill-lite's job ids (D3).
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

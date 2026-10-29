package wl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Selector names call sites (ADR 0008): a module selector (Flow and
// Module, with an optional Occurrence, Iterations or Branches) or a script
// selector (Script). Module is a pointer because a module id may be ""
// (OF-2). It is the host contract's selector type.
type Selector = ir.Selector

// Site is a call site as a hook sees it: its typed path (ADR 0011), the
// flow whose module list holds the module, the module's id and, for a leaf,
// its script's path (<flow path>/<module id> for an inline script).
type Site = engine.Site

// SitePath is a site's typed path (ADR 0011), such as m:a/i:2/m:b.
type SitePath = conformance.SitePath

// StepRecord is what the trace records of one site (H9).
type StepRecord = engine.StepRecord

// RunHandle is a hook's handle on the run it serves: its id, and Latch,
// which latches an error on the run (H5): the run starts nothing more and
// ends latched with the first error latched.
type RunHandle = engine.Run

// Call is what an interception, an argument rewrite or a provider gets:
// the run, the site and the site's arguments, a JSON object in their
// stored form (ADR 0005).
type Call struct {
	Run  RunHandle
	Site Site
	Args json.RawMessage
}

// Outcome is what an interception or a provider answers for a site in
// place of the job it would have run: its result, or, with Failed, its
// error value, the object a failed job's result holds under "error"
// (such as {"name": "Error", "message": "..."}). The flow's completion
// phase applies to it as to the job's: stop_after_if, retry,
// continue_on_error (ADR 0008, H1).
type Outcome struct {
	Result json.RawMessage
	Failed bool
}

// Hooks are a run's host hooks as Go callbacks (ADR 0008), RunOptions.Hooks.
// Every field is optional. A hook that returns an error latches the run
// with it (H5); a hook never fails a module by returning an error, which
// is what an Outcome with Failed is for. A parallel loop's iterations run
// concurrently, so the callbacks of one run may be called from several
// goroutines at once.
type Hooks struct {
	// Intercept (H1) answers for a site's job when the module would
	// launch it: after skip_if, mock, its arguments and its timeout are
	// decided. The first interceptor whose selector matches the site
	// answers; one that declines lets the real module run. A module that
	// skip_if or mock answered is not intercepted, and neither is a
	// container that runs.
	Intercept []Interceptor
	// RewriteArgs (H2) rewrites the arguments of a site whose module
	// launches its own job, every matching rewriter in order; the trace
	// keeps the arguments before and after.
	RewriteArgs []ArgRewriter
	// HaltBefore and HaltAfter (H8) end the run successfully, with status
	// halted, before the first attempt of a module a selector matches, or
	// once such a module has its final outcome and the run would start
	// something other than a failure module (ADR 0008, precedence).
	HaltBefore, HaltAfter []Selector
	// OnModuleStart and OnModuleEnd (H6) observe every module, nested ones
	// and iterations included; OnModuleEnd gets the site's record.
	OnModuleStart func(ctx context.Context, run RunHandle, site Site) error
	OnModuleEnd   func(ctx context.Context, run RunHandle, site Site, rec StepRecord) error
	// InvocationEnv (H12) sets or deletes variables of the job environment
	// of each invocation, before it runs (ADR 0007).
	InvocationEnv func(ctx context.Context, run RunHandle, site Site, env map[string]string) error
	// OnRunEnd (H9) is called once the run's outcome is decided; the
	// warnings it returns are appended to the run's, whose status it cannot
	// change. It must not modify the trace.
	OnRunEnd func(ctx context.Context, t *Trace) []Warning
	// OnRunSettled (H9) is called once every invocation the run abandoned
	// has settled, possibly never, with a trace that adds what they
	// produced after the run ended; resources step code can reach are
	// released here.
	OnRunSettled func(t *Trace)
}

// Interceptor is one H1 interception: the sites Selector matches, and
// either Static, a fixed result (the no-code form), or Func, which
// returns the site's outcome and true, or false to decline.
type Interceptor struct {
	Selector Selector
	Static   json.RawMessage
	Func     func(ctx context.Context, c Call) (Outcome, bool, error)
}

// ArgRewriter is one H2 rewrite: the sites Selector matches, and Func,
// which returns the arguments, a JSON object, the module runs with.
type ArgRewriter struct {
	Selector Selector
	Func     func(ctx context.Context, c Call) (json.RawMessage, error)
}

// Provider answers a host-provided site whose contract entry names it
// (ADR 0002, ADR 0008), Options.Providers: at the site's launch, in place
// of the job its target would have run, it returns the site's outcome and
// true. A mandatory provider cannot decline: false latches the run with
// ErrProviderDeclined, and the run fails closed.
type Provider func(ctx context.Context, c Call) (Outcome, bool, error)

// ErrMissingProvider marks a bundle whose host contract names a provider
// that Options.Providers does not register (missing_provider): Open fails,
// so no run reaches an unprovided site.
var ErrMissingProvider = errors.New("missing_provider")

// ErrProviderDeclined is the latch of a run whose host-provided site's
// provider declined (provider_declined): Result.Latched wraps it.
var ErrProviderDeclined = engine.ErrProviderDeclined

// ErrAmbiguousSelector marks a module selector without an occurrence that
// names an id occurring more than once in one module list of its flow
// (ambiguous_selector): the run does not start.
var ErrAmbiguousSelector = errors.New("ambiguous_selector")

// ErrSelector marks a selector that is not well formed: it must name
// exactly one of a flow's module (Flow and Module) and a script (Script),
// and only a module selector may give Occurrence, Iterations or Branches.
var ErrSelector = errors.New("wl: invalid selector")

// checkProviders checks that every named provider of the program's host
// contract is registered (missing_provider).
func checkProviders(prog *ir.Program, providers map[string]Provider) error {
	for i, p := range prog.Contract.Provided {
		if p.Provider != "" && providers[p.Provider] == nil {
			return fmt.Errorf("%w: provided site %d (%s) names the provider %q, which Options.Providers does not register", ErrMissingProvider, i, p.Selector, p.Provider)
		}
	}
	return nil
}

// provide is the engine's Config.Provide over providers.
func provide(providers map[string]Provider) engine.ProvideFunc {
	return func(ctx context.Context, r engine.Run, s engine.Site, name string, args jsonval.Value) (engine.StepOutcome, bool, error) {
		p := providers[name]
		if p == nil {
			return engine.StepOutcome{}, false, nil
		}
		out, ok, err := p(ctx, Call{Run: r, Site: s, Args: storedText(args)})
		if err != nil || !ok {
			return engine.StepOutcome{}, ok, err
		}
		o, err := out.step()
		return o, err == nil, err
	}
}

// step is the outcome as the engine takes it.
func (o Outcome) step() (engine.StepOutcome, error) {
	body := o.Result
	if len(bytes.TrimSpace(body)) == 0 {
		body = json.RawMessage("null")
	}
	if !json.Valid(body) {
		return engine.StepOutcome{}, errors.New("wl: an outcome whose result is not JSON")
	}
	kind := engine.OutcomeOK
	if o.Failed {
		kind = engine.OutcomeError
	}
	return engine.StepOutcome{Kind: kind, Body: jsonval.FromProducer(body)}, nil
}

// engineHooks adapts h to the engine's hooks for a run of prog, checking
// every selector first.
func engineHooks(h *Hooks, prog *ir.Program) (*hooks, error) {
	if h == nil {
		return nil, nil
	}
	check := func(where string, s Selector) error {
		if err := checkSelector(s); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if err := ambiguous(s, prog); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		return nil
	}
	for i, x := range h.Intercept {
		if err := check(fmt.Sprintf("Hooks.Intercept[%d]", i), x.Selector); err != nil {
			return nil, err
		}
		if (x.Static == nil) == (x.Func == nil) {
			return nil, fmt.Errorf("Hooks.Intercept[%d]: give exactly one of Static and Func", i)
		}
		if x.Static != nil && !json.Valid(x.Static) {
			return nil, fmt.Errorf("Hooks.Intercept[%d].Static: not JSON", i)
		}
	}
	for i, x := range h.RewriteArgs {
		if err := check(fmt.Sprintf("Hooks.RewriteArgs[%d]", i), x.Selector); err != nil {
			return nil, err
		}
		if x.Func == nil {
			return nil, fmt.Errorf("Hooks.RewriteArgs[%d]: no Func", i)
		}
	}
	for i, s := range h.HaltBefore {
		if err := check(fmt.Sprintf("Hooks.HaltBefore[%d]", i), s); err != nil {
			return nil, err
		}
	}
	for i, s := range h.HaltAfter {
		if err := check(fmt.Sprintf("Hooks.HaltAfter[%d]", i), s); err != nil {
			return nil, err
		}
	}
	return &hooks{h: h}, nil
}

// checkSelector checks a selector's form.
func checkSelector(s Selector) error {
	module := s.Flow != "" || s.Module != nil
	switch {
	case module == (s.Script != ""):
		return fmt.Errorf("%w %s: give a flow and a module, or a script", ErrSelector, s)
	case module && (s.Flow == "" || s.Module == nil):
		return fmt.Errorf("%w %s: a module selector needs both a flow and a module", ErrSelector, s)
	case !module && (s.Occurrence != nil || len(s.Iterations) > 0 || len(s.Branches) > 0):
		return fmt.Errorf("%w %s: only a module selector narrows by occurrence, iterations or branches", ErrSelector, s)
	case s.Occurrence != nil && *s.Occurrence < 0:
		return fmt.Errorf("%w %s: a negative occurrence", ErrSelector, s)
	}
	for _, b := range s.Branches {
		if _, err := strconv.Atoi(b); err != nil && b != "default" {
			return fmt.Errorf("%w %s: branch %q is neither an index nor default", ErrSelector, s, b)
		}
	}
	return nil
}

// ambiguous reports a module selector without an occurrence whose id
// occurs more than once in one module list of its flow (ADR 0008).
func ambiguous(s Selector, prog *ir.Program) error {
	if s.Module == nil || s.Occurrence != nil {
		return nil
	}
	f := prog.Flows[s.Flow]
	if f == nil {
		return nil
	}
	var walk func(list []*ir.Module) bool
	walk = func(list []*ir.Module) bool {
		n := 0
		for _, m := range list {
			if m.ID == *s.Module {
				n++
			}
			for _, body := range moduleBodies(m) {
				if walk(body) {
					return true
				}
			}
		}
		return n > 1
	}
	if walk(f.Modules) {
		return fmt.Errorf("%w: %s names an id that occurs more than once in one module list of %s; give its occurrence", ErrAmbiguousSelector, s, s.Flow)
	}
	return nil
}

// moduleBodies are a container's module lists.
func moduleBodies(m *ir.Module) [][]*ir.Module {
	switch {
	case m.ForLoop != nil:
		return [][]*ir.Module{m.ForLoop.Modules}
	case m.WhileLoop != nil:
		return [][]*ir.Module{m.WhileLoop.Modules}
	case m.BranchOne != nil:
		var out [][]*ir.Module
		for _, b := range m.BranchOne.Branches {
			out = append(out, b.Modules)
		}
		return append(out, m.BranchOne.Default)
	case m.BranchAll != nil:
		var out [][]*ir.Module
		for _, b := range m.BranchAll.Branches {
			out = append(out, b.Modules)
		}
		return out
	}
	return nil
}

// matches reports whether s names the site. A script selector names a
// leaf by its script's path; a module selector names a module by its flow
// and id, by its occurrence when it gives one (a unique id is occurrence
// 0), and, with Iterations or Branches, only within those iterations or
// branches of the innermost loop or branching module around it.
func matches(s Selector, site Site) bool {
	if s.Script != "" {
		return site.Script == s.Script
	}
	if s.Module == nil || site.Flow != s.Flow || site.Module != *s.Module {
		return false
	}
	segs := site.Path.Segments()
	if len(segs) == 0 {
		return false
	}
	if s.Occurrence != nil {
		k, _ := segs[len(segs)-1].Occurrence()
		if k != *s.Occurrence {
			return false
		}
	}
	inner := func(kind conformance.SegmentKind) (string, bool) {
		for i := len(segs) - 2; i >= 0; i-- {
			if segs[i].Kind() == kind {
				return segs[i].Value(), true
			}
		}
		return "", false
	}
	if len(s.Iterations) > 0 {
		v, ok := inner(conformance.KindIteration)
		n, err := strconv.Atoi(v)
		if !ok || err != nil || !slices.Contains(s.Iterations, n) {
			return false
		}
	}
	if len(s.Branches) > 0 {
		v, ok := inner(conformance.KindBranch)
		if !ok || !slices.Contains(s.Branches, v) {
			return false
		}
	}
	return true
}

// hooks is the engine's view of a run's Hooks. calls gives the run's
// platform calls so far, which the engine's trace does not hold (the
// runtime collects them): the trace OnRunEnd gets carries them as of the
// run's end, and so do the trace OnRunSettled gets and the one Run
// returns, so all three agree.
type hooks struct {
	h     *Hooks
	calls func() []PlatformCall

	mu    sync.Mutex
	ended bool
	end   []PlatformCall
}

var _ engine.Hooks = (*hooks)(nil)

// endCalls are the run's platform calls as of its end, taken once.
func (x *hooks) endCalls() []PlatformCall {
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.ended {
		x.ended = true
		if x.calls != nil {
			x.end = x.calls()
		}
	}
	return slices.Clone(x.end)
}

// withCalls is a copy of t with the run's platform calls as of its end.
func (x *hooks) withCalls(t *engine.Trace) *engine.Trace {
	c := *t
	c.PlatformCalls = x.endCalls()
	return &c
}

func (x *hooks) Intercept(ctx context.Context, r engine.Run, s engine.Site, args jsonval.Value) (engine.StepOutcome, bool, error) {
	for _, in := range x.h.Intercept {
		if !matches(in.Selector, s) {
			continue
		}
		if in.Static != nil {
			out, err := Outcome{Result: in.Static}.step()
			return out, err == nil, err
		}
		out, ok, err := in.Func(ctx, Call{Run: r, Site: s, Args: storedText(args)})
		if err != nil || !ok {
			return engine.StepOutcome{}, false, err
		}
		o, err := out.step()
		return o, err == nil, err
	}
	return engine.StepOutcome{}, false, nil
}

func (x *hooks) RewriteArgs(ctx context.Context, r engine.Run, s engine.Site, args jsonval.Value) (jsonval.Value, error) {
	for _, rw := range x.h.RewriteArgs {
		if !matches(rw.Selector, s) {
			continue
		}
		out, err := rw.Func(ctx, Call{Run: r, Site: s, Args: args.Producer()})
		if err != nil {
			return args, err
		}
		if !json.Valid(out) {
			return args, errors.New("wl: H2 returned arguments that are not JSON")
		}
		args = jsonval.FromProducer(out)
	}
	return args, nil
}

func (*hooks) WriteCapable(engine.Site) bool { return false }

func (*hooks) BeforeFirstWrite(context.Context, engine.Run, engine.Site) error { return nil }

func (*hooks) FatalErrorName(string) bool { return false }

func (x *hooks) HaltBefore(s engine.Site) bool { return anyMatch(x.h.HaltBefore, s) }

func (x *hooks) HaltAfter(s engine.Site) bool { return anyMatch(x.h.HaltAfter, s) }

func anyMatch(sels []Selector, s engine.Site) bool {
	return slices.ContainsFunc(sels, func(sel Selector) bool { return matches(sel, s) })
}

func (x *hooks) OnModuleStart(ctx context.Context, r engine.Run, s engine.Site) error {
	if x.h.OnModuleStart == nil {
		return nil
	}
	return x.h.OnModuleStart(ctx, r, s)
}

func (x *hooks) OnModuleEnd(ctx context.Context, r engine.Run, s engine.Site, rec engine.StepRecord) error {
	if x.h.OnModuleEnd == nil {
		return nil
	}
	return x.h.OnModuleEnd(ctx, r, s, rec)
}

func (x *hooks) InvocationEnv(ctx context.Context, r engine.Run, s engine.Site, env map[string]string) error {
	if x.h.InvocationEnv == nil {
		return nil
	}
	return x.h.InvocationEnv(ctx, r, s, env)
}

func (x *hooks) OnRunEnd(ctx context.Context, t *engine.Trace) []engine.Warning {
	if x.h.OnRunEnd == nil {
		x.endCalls()
		return nil
	}
	return x.h.OnRunEnd(ctx, x.withCalls(t))
}

func (x *hooks) OnRunSettled(t *engine.Trace) {
	if x.h.OnRunSettled != nil {
		x.h.OnRunSettled(x.withCalls(t))
	}
}

package engine

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Site is a call site as hooks see it: its identity, and what a selector
// matches on (ADR 0008).
type Site struct {
	// Path is the site's typed path (ADR 0011).
	Path conformance.SitePath
	// Flow is the path of the flow whose module list holds the module: the
	// root, or a flow called by path. Container bodies belong to their
	// enclosing flow.
	Flow string
	// Module is the module's id.
	Module string
	// Script is the path of the script a leaf runs, or <flow path>/<module
	// id> for an inline script; "" for other modules.
	Script string
}

// Run is the handle a hook has on the run it serves.
type Run interface {
	// ID returns the run's id.
	ID() string
	// Latch latches err on the run (H5); the first latch wins.
	Latch(err error)
}

// Hooks are the engine's internal hook seams (ADR 0008). The engine calls
// them at the points each method names, with the precedence of Start,
// Completion and AfterContinue. The hook milestones (F2a, F2b, F2c) expose
// them to hosts as Go callbacks and TypeScript plugins; selector matching
// happens behind this interface.
//
// A non-nil error returned by any method latches the run with that error
// (H5), unless it is the run's own cancellation (see HookLatchError). A hook
// never fails a module by returning an error: Intercept answers with an
// error outcome for that.
//
// NoHooks implements every method as a no-op; embed it to implement a
// subset.
type Hooks interface {
	// Intercept (H1) runs when a module would launch, after skip_if, mock,
	// its arguments and its timeout are decided. It returns the outcome that
	// stands in for the module's run and true, or false to decline so that
	// the real module runs. The completion phase applies to the outcome as
	// if the module had run.
	Intercept(ctx context.Context, r Run, s Site, args jsonval.Value) (StepOutcome, bool, error)
	// RewriteArgs (H2) returns the arguments the module runs with, given
	// the evaluated ones. The trace keeps both.
	RewriteArgs(ctx context.Context, r Run, s Site, args jsonval.Value) (jsonval.Value, error)
	// WriteCapable (H3) reports whether the host marked the site
	// write-capable.
	WriteCapable(s Site) bool
	// BeforeFirstWrite (H3) is called once per run, before the first
	// write-capable module launches; an error vetoes it, latching the run.
	BeforeFirstWrite(ctx context.Context, r Run, s Site) error
	// FatalErrorName (H7) reports whether a failure whose error has this
	// name latches the run instead of reaching retry, continue_on_error,
	// skip_failures or skip_failure.
	FatalErrorName(name string) bool
	// HaltBefore (H8) reports whether the run ends halted before the module
	// at s starts.
	HaltBefore(s Site) bool
	// HaltAfter (H8) reports whether the run ends halted once the module at
	// s has its final outcome (see AfterContinue).
	HaltAfter(s Site) bool
	// OnModuleStart and OnModuleEnd (H6) observe every module, nested ones
	// and iterations included.
	OnModuleStart(ctx context.Context, r Run, s Site) error
	OnModuleEnd(ctx context.Context, r Run, s Site, rec StepRecord) error
	// InvocationEnv (H12) may set or delete variables of an invocation's
	// environment before it runs.
	InvocationEnv(ctx context.Context, r Run, s Site, env map[string]string) error
	// OnRunEnd (H9) is called once the run's outcome is decided; the
	// warnings it returns are appended to the trace, whose status it cannot
	// change. It must not modify t.
	OnRunEnd(ctx context.Context, t *Trace) []Warning
	// OnRunSettled (H9) is called once every invocation the run abandoned
	// has settled, which may be never. Resources that step code can reach
	// are released here, never on a timer (ADR 0004). t is a new snapshot
	// that adds what the abandoned invocations produced after the run ended;
	// the trace returned at the run's end is never changed.
	OnRunSettled(t *Trace)
}

// NoHooks is the no-op default of every hook: nothing is intercepted,
// rewritten, write-capable, fatal or halted, and observers do nothing.
type NoHooks struct{}

var _ Hooks = NoHooks{}

// Intercept declines.
func (NoHooks) Intercept(context.Context, Run, Site, jsonval.Value) (StepOutcome, bool, error) {
	return StepOutcome{}, false, nil
}

// RewriteArgs returns args unchanged.
func (NoHooks) RewriteArgs(_ context.Context, _ Run, _ Site, args jsonval.Value) (jsonval.Value, error) {
	return args, nil
}

// WriteCapable reports false.
func (NoHooks) WriteCapable(Site) bool { return false }

// BeforeFirstWrite allows the write.
func (NoHooks) BeforeFirstWrite(context.Context, Run, Site) error { return nil }

// FatalErrorName reports false.
func (NoHooks) FatalErrorName(string) bool { return false }

// HaltBefore reports false.
func (NoHooks) HaltBefore(Site) bool { return false }

// HaltAfter reports false.
func (NoHooks) HaltAfter(Site) bool { return false }

// OnModuleStart does nothing.
func (NoHooks) OnModuleStart(context.Context, Run, Site) error { return nil }

// OnModuleEnd does nothing.
func (NoHooks) OnModuleEnd(context.Context, Run, Site, StepRecord) error { return nil }

// InvocationEnv leaves env unchanged.
func (NoHooks) InvocationEnv(context.Context, Run, Site, map[string]string) error { return nil }

// OnRunEnd adds no warnings.
func (NoHooks) OnRunEnd(context.Context, *Trace) []Warning { return nil }

// OnRunSettled does nothing.
func (NoHooks) OnRunSettled(*Trace) {}

// HookLatchError returns the error that a hook's returned err latches on
// the run, or nil. It is nil for a nil err, and nil when ctx has ended and
// err is ctx's error or cause: a hook that gives up because the run was
// canceled or reached a deadline must not turn that cancellation into a
// latch, which would outrank it (H4, H5).
func HookLatchError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil && (errors.Is(err, ctx.Err()) || errors.Is(err, context.Cause(ctx))) {
		return nil
	}
	return err
}

// Latch holds a run's latched error (H5). The first error latched wins;
// later ones are dropped, because the run already stopped for the first.
// The zero Latch is unlatched. A Latch is safe for concurrent use and must
// not be copied after first use.
type Latch struct {
	p atomic.Pointer[latched]
}

type latched struct{ err error }

// Set latches err and reports whether this call latched the run. A nil err
// latches nothing.
func (l *Latch) Set(err error) bool {
	if err == nil {
		return false
	}
	return l.p.CompareAndSwap(nil, &latched{err: err})
}

// Err returns the latched error, or nil.
func (l *Latch) Err() error {
	if v := l.p.Load(); v != nil {
		return v.err
	}
	return nil
}

// Clock is the engine's clock (H10): every requested wait goes through it,
// so a host can record waits instead of sleeping them. The retry-jitter
// source is a separate seam, defined with the retry arithmetic (FX-26,
// milestone N2).
type Clock interface {
	Now() time.Time
	// Wait waits for d, or until ctx ends, and returns nil when the wait
	// ran its course or context.Cause(ctx) when ctx ended it or had already
	// ended. A recording clock records d and returns at once, with
	// context.Cause(ctx) if ctx has already ended and nil otherwise.
	Wait(ctx context.Context, d time.Duration) error
}

// RealClock is the default Clock: wall time and real waits.
type RealClock struct{}

// Now returns time.Now().
func (RealClock) Now() time.Time { return time.Now() }

// Wait sleeps for d unless ctx ends first.
func (RealClock) Wait(ctx context.Context, d time.Duration) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

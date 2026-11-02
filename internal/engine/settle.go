package engine

import (
	"context"
	"sync"
	"sync/atomic"
)

// RunSettlement tracks whether a run has settled: its outcome is decided
// and every invocation it started has settled, which may be never (H9,
// ADR 0004 host mode item 5). The engine calls Hooks.OnRunSettled from its
// callback, and releases there the resources that step code can reach,
// never on a timer. A RunSettlement is safe for concurrent use.
//
// Once the run has settled, its trace is never read again: OnRunSettled has
// had it. The Reporters it handed out (Track) then let go of the run, so
// that an executor that keeps a settled invocation for its late output
// (D9: the harness's last settled invocations) no longer keeps the run's
// values reachable; late output that still comes goes nowhere, as it went
// to a trace nobody reads.
type RunSettlement struct {
	mu        sync.Mutex
	open      int
	ended     bool
	fired     bool
	onSettled func()
	// tracked are the Reporters Track handed out, until the run settles.
	tracked []*trackedReporter
}

// NewRunSettlement returns a tracker that calls onSettled once, from the
// goroutine of whichever call settles the run.
func NewRunSettlement(onSettled func()) *RunSettlement {
	return &RunSettlement{onSettled: onSettled}
}

// Track counts one more invocation of the run and returns the Reporter to
// give it: Late and Warning pass on to next, and the invocation counts as
// settled at its first Settled call, which also passes on. Later Settled
// calls are dropped, so an executor that reports twice cannot settle the
// run early. Track panics after End: an ended run starts no invocation.
func (s *RunSettlement) Track(next Reporter) Reporter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		panic("engine: invocation started after the run ended")
	}
	s.open++
	r := &trackedReporter{run: s}
	r.next.Store(&next)
	s.tracked = append(s.tracked, r)
	return r
}

// End records that the run's outcome is decided. The run settles once End
// has been called and no tracked invocation is unsettled.
func (s *RunSettlement) End() {
	s.mu.Lock()
	s.ended = true
	fire := s.settleLocked()
	s.mu.Unlock()
	if fire {
		s.fire()
	}
}

// fire calls the settlement callback, then lets go of the run: each
// Reporter Track handed out drops the one it passes on to, and the
// callback, which holds the run, is dropped too.
func (s *RunSettlement) fire() {
	s.onSettled()
	s.mu.Lock()
	tracked := s.tracked
	s.tracked, s.onSettled = nil, nil
	s.mu.Unlock()
	for _, r := range tracked {
		r.next.Store(nil)
	}
}

// Hold counts work of the run's own that is not an invocation, a host
// plugin's hook from the moment its host_call is sent until it has
// finished, which may be after the engine stopped waiting for it
// (milestone F2c), and returns its release: the run does not settle while
// it is held. Unlike Track it is allowed after End. Once the run has
// settled it holds nothing and ok is false: the work must not start, as
// the run's resources may be gone. Release may be called more than once.
func (s *RunSettlement) Hold() (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fired {
		return func() {}, false
	}
	s.open++
	var once sync.Once
	return func() { once.Do(s.settleOne) }, true
}

// settlementKey is the context key of a run's settlement.
type settlementKey struct{}

// HoldSettlement holds the settlement of the run whose context ctx is or
// derives from (RunSettlement.Hold): the engine gives every hook, provider
// and RunRequest.Started that context. ok is false when that run has
// settled. A context of no run, such as the one a settlement callback's
// own work runs in (OnRunSettled), holds nothing, with ok true, so that
// work never waits for itself.
func HoldSettlement(ctx context.Context) (release func(), ok bool) {
	if s, found := ctx.Value(settlementKey{}).(*RunSettlement); found {
		return s.Hold()
	}
	return func() {}, true
}

// Open returns the number of tracked invocations that have not settled.
func (s *RunSettlement) Open() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open
}

func (s *RunSettlement) settleOne() {
	s.mu.Lock()
	s.open--
	fire := s.settleLocked()
	s.mu.Unlock()
	if fire {
		s.fire()
	}
}

// settleLocked reports whether the run settles now, marking it fired.
func (s *RunSettlement) settleLocked() bool {
	if s.fired || !s.ended || s.open > 0 {
		return false
	}
	s.fired = true
	return true
}

type trackedReporter struct {
	run *RunSettlement
	// next is the Reporter calls pass on to, nil once the run has settled.
	next    atomic.Pointer[Reporter]
	settled atomic.Bool
}

func (r *trackedReporter) Late(l Late) {
	if n := r.next.Load(); n != nil {
		(*n).Late(l)
	}
}

func (r *trackedReporter) Warning(w Warning) {
	if n := r.next.Load(); n != nil {
		(*n).Warning(w)
	}
}

func (r *trackedReporter) Settled(k SettleKind) {
	if !r.settled.CompareAndSwap(false, true) {
		return
	}
	if n := r.next.Load(); n != nil {
		(*n).Settled(k)
	}
	r.run.settleOne()
}

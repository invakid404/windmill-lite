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
type RunSettlement struct {
	mu        sync.Mutex
	open      int
	ended     bool
	fired     bool
	onSettled func()
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
	return &trackedReporter{run: s, next: next}
}

// End records that the run's outcome is decided. The run settles once End
// has been called and no tracked invocation is unsettled.
func (s *RunSettlement) End() {
	s.mu.Lock()
	s.ended = true
	fire := s.settleLocked()
	s.mu.Unlock()
	if fire {
		s.onSettled()
	}
}

// Hold counts work of the run's own that is not an invocation and may
// outlast the engine's wait for it, a host plugin's hook whose host_call
// timed out (milestone F2c), and returns its release: the run does not
// settle while it is held. Unlike Track it is allowed after End. A hold
// taken once the run has settled holds nothing, so the settlement callback
// never waits on itself. Release may be called more than once.
func (s *RunSettlement) Hold() (release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fired {
		return func() {}
	}
	s.open++
	var once sync.Once
	return func() { once.Do(s.settleOne) }
}

// settlementKey is the context key of a run's settlement.
type settlementKey struct{}

// HoldSettlement holds the settlement of the run whose context ctx is or
// derives from (RunSettlement.Hold): the engine gives every hook, provider
// and RunRequest.Started that context. Outside a run it holds nothing.
func HoldSettlement(ctx context.Context) (release func()) {
	if s, ok := ctx.Value(settlementKey{}).(*RunSettlement); ok {
		return s.Hold()
	}
	return func() {}
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
		s.onSettled()
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
	run     *RunSettlement
	next    Reporter
	settled atomic.Bool
}

func (r *trackedReporter) Late(l Late)       { r.next.Late(l) }
func (r *trackedReporter) Warning(w Warning) { r.next.Warning(w) }

func (r *trackedReporter) Settled(k SettleKind) {
	if !r.settled.CompareAndSwap(false, true) {
		return
	}
	r.next.Settled(k)
	r.run.settleOne()
}

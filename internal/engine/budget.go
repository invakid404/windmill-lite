package engine

import (
	"errors"
	"fmt"
	"sync"
)

// Budget is an invocation's draw on its run's byte budget and the
// runtime's total (ADR 0004 §Pooling): the step's wl.reserve(bytes) calls
// Reserve. What an invocation reserves returns when it settles.
type Budget interface {
	// Reserve takes n more bytes for the invocation, or returns an error,
	// which the step's wl.reserve throws, when they do not fit the run's
	// budget or the runtime's total.
	Reserve(n int64) error
}

// ErrBudgetSettled is Reserve's error for an invocation that has settled.
var ErrBudgetSettled = errors.New("engine: the invocation has settled, so it reserves nothing more")

// ByteTotal is a runtime's total of bytes that every run's reservations
// share (RunLimits.ByteTotal). Its methods are safe for concurrent use.
type ByteTotal struct {
	mu          sync.Mutex
	limit, used int64
}

// NewByteTotal returns a total of limit bytes; limit is positive.
func NewByteTotal(limit int64) *ByteTotal { return &ByteTotal{limit: limit} }

// Used returns the bytes reserved now.
func (t *ByteTotal) Used() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.used
}

func (t *ByteTotal) take(n int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.used+n > t.limit {
		return fmt.Errorf("windmill-lite: wl.reserve(%d): the runtime's byte total of %d has %d left", n, t.limit, t.limit-t.used)
	}
	t.used += n
	return nil
}

func (t *ByteTotal) give(n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.used -= n
}

// byteAccount is a run's account of its reservations: its budget, zero
// for none, and the runtime's total, nil for none.
type byteAccount struct {
	mu          sync.Mutex
	limit, used int64
	total       *ByteTotal
}

// newByteAccount returns the run's account, or nil when the run has
// neither a budget nor a total.
func newByteAccount(l RunLimits) *byteAccount {
	if l.Bytes <= 0 && l.ByteTotal == nil {
		return nil
	}
	return &byteAccount{limit: l.Bytes, total: l.ByteTotal}
}

func (a *byteAccount) take(n int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.limit > 0 && a.used+n > a.limit {
		return fmt.Errorf("windmill-lite: wl.reserve(%d): the run's byte budget of %d has %d left", n, a.limit, a.limit-a.used)
	}
	if a.total != nil {
		if err := a.total.take(n); err != nil {
			return err
		}
	}
	a.used += n
	return nil
}

func (a *byteAccount) give(n int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.used -= n
	if a.total != nil {
		a.total.give(n)
	}
}

// draw returns an invocation's Budget on the account and the Reporter
// that returns its reservations when the invocation settles, before next
// hears of it.
func (a *byteAccount) draw(next Reporter) (Budget, Reporter) {
	b := &invBudget{a: a}
	return b, &budgetReporter{Reporter: next, b: b}
}

// invBudget is one invocation's reservations on its run's account.
type invBudget struct {
	a       *byteAccount
	mu      sync.Mutex
	held    int64
	settled bool
}

func (b *invBudget) Reserve(n int64) error {
	if n <= 0 {
		return fmt.Errorf("windmill-lite: wl.reserve(%d): a reservation is a positive number of bytes", n)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.settled {
		return ErrBudgetSettled
	}
	if err := b.a.take(n); err != nil {
		return err
	}
	b.held += n
	return nil
}

func (b *invBudget) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settled = true
	b.a.give(b.held)
	b.held = 0
}

// budgetReporter returns an invocation's reservations at its settlement.
type budgetReporter struct {
	Reporter
	b *invBudget
}

func (r *budgetReporter) Settled(k SettleKind) {
	r.b.release()
	r.Reporter.Settled(k)
}

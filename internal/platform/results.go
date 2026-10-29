package platform

import (
	"context"
	"slices"
	"sync"
	"time"
)

// ResultCache is the result cache service of ADR 0007 (milestone N4): the
// results of the jobs that run with a cache_ttl, a step's or a whole flow's,
// kept under the key the engine computes from the job and its arguments,
// each until its expiry (FX-40). The engine owns the key, the TTL rules and
// the encoding of a value; a store keeps opaque bytes and decides nothing
// but expiry, so a host can put any shared store behind it (D4). The engine
// asks with its run's clock (H10), so a store compares expiries with the
// time it is given, never its own.
type ResultCache interface {
	// Get returns the value stored under key, and true, when it expires
	// after now; false when there is none or it has expired.
	Get(ctx context.Context, key string, now time.Time) ([]byte, bool, error)
	// Put stores value under key until expires, replacing what was there.
	Put(ctx context.Context, key string, value []byte, expires time.Time) error
}

// MemoryResults is the local default of the result cache: entries in
// memory, shared by the runs of whatever holds it (per process, D4: one
// per Services). Every entry has an expiry, and none outlives it for long:
// a Get drops the expired entry it finds, and a Put sweeps out every
// expired entry once the cache has doubled since its last sweep, so the
// cache holds at most about twice what is live. It is safe for concurrent
// use.
type MemoryResults struct {
	mu      sync.Mutex
	entries map[string]resultEntry
	sweep   int // the number of entries at which the next Put sweeps
	latest  time.Time
}

// resultEntry is one cached value and its expiry.
type resultEntry struct {
	value   []byte
	expires time.Time
}

// minResultSweep is the number of entries below which MemoryResults never
// sweeps.
const minResultSweep = 64

// NewMemoryResults returns an empty in-memory result cache.
func NewMemoryResults() *MemoryResults {
	return &MemoryResults{entries: map[string]resultEntry{}, sweep: minResultSweep}
}

func (m *MemoryResults) Get(_ context.Context, key string, now time.Time) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observe(now)
	e, ok := m.entries[key]
	if !ok {
		return nil, false, nil
	}
	if !e.expires.After(now) {
		delete(m.entries, key)
		return nil, false, nil
	}
	return slices.Clone(e.value), true, nil
}

func (m *MemoryResults) Put(_ context.Context, key string, value []byte, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = resultEntry{value: slices.Clone(value), expires: expires}
	if len(m.entries) >= m.sweep {
		m.dropExpired()
	}
	return nil
}

// observe keeps the latest time a caller has given, which the sweep takes
// as now: the cache keeps no clock of its own. Called with mu held.
func (m *MemoryResults) observe(now time.Time) {
	if now.After(m.latest) {
		m.latest = now
	}
}

// dropExpired removes every entry that expired by the latest time a Get
// gave, and sets the next sweep at twice the entries that remain. Called
// with mu held.
func (m *MemoryResults) dropExpired() {
	for k, e := range m.entries {
		if !e.expires.After(m.latest) {
			delete(m.entries, k)
		}
	}
	m.sweep = max(minResultSweep, 2*len(m.entries))
}

// size returns the number of entries the cache holds, expired ones it has
// not dropped yet included.
func (m *MemoryResults) size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

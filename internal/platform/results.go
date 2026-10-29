package platform

import (
	"container/heap"
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
// per Services). An entry never outlives its expiry beyond the next call:
// every Get and Put drops each entry that expired by the latest time a Get
// gave (the engine looks a key up before it stores one, with its run's
// clock), in order of expiry, so the cache holds only entries still live
// at that time, whatever their number. It keeps no clock of its own: a hit
// compares an entry's expiry with the time its Get gave, and only the
// reclaiming uses the latest one, so with clocks that disagree (H10's
// record mode runs ahead of the wall clock) an entry may go before its
// own run's clock reaches its expiry, a miss and never a stale hit. It is
// safe for concurrent use.
type MemoryResults struct {
	mu      sync.Mutex
	entries map[string]resultEntry
	// byExpiry holds every entry's key and expiry, earliest first, and the
	// older expiries of keys stored again, which it drops as they come up
	// and which a rebuild clears when they make up more than half of it.
	byExpiry expiryHeap
	latest   time.Time
}

// resultEntry is one cached value and its expiry.
type resultEntry struct {
	value   []byte
	expires time.Time
}

// minResultRebuild is the number of expiries below which MemoryResults
// never rebuilds its expiry heap.
const minResultRebuild = 64

// NewMemoryResults returns an empty in-memory result cache.
func NewMemoryResults() *MemoryResults {
	return &MemoryResults{entries: map[string]resultEntry{}}
}

func (m *MemoryResults) Get(_ context.Context, key string, now time.Time) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if now.After(m.latest) {
		m.latest = now
	}
	m.dropExpired()
	e, ok := m.entries[key]
	if !ok || !e.expires.After(now) {
		return nil, false, nil
	}
	return slices.Clone(e.value), true, nil
}

func (m *MemoryResults) Put(_ context.Context, key string, value []byte, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = resultEntry{value: slices.Clone(value), expires: expires}
	heap.Push(&m.byExpiry, expiry{key: key, at: expires})
	if n := m.byExpiry.Len(); n > minResultRebuild && n > 2*len(m.entries) {
		// Most of the heap is older expiries of keys stored again: keep
		// only the live entries' own.
		m.byExpiry = m.byExpiry[:0]
		for k, e := range m.entries {
			m.byExpiry = append(m.byExpiry, expiry{key: k, at: e.expires})
		}
		heap.Init(&m.byExpiry)
	}
	m.dropExpired()
	return nil
}

// dropExpired removes, earliest first, every entry that expired by the
// latest time a Get gave, and the heap's older expiries of keys stored
// again as they come up. Called with mu held.
func (m *MemoryResults) dropExpired() {
	for m.byExpiry.Len() > 0 && !m.byExpiry[0].at.After(m.latest) {
		x := heap.Pop(&m.byExpiry).(expiry)
		if e, ok := m.entries[x.key]; ok && !e.expires.After(m.latest) {
			delete(m.entries, x.key)
		}
	}
}

// size returns the number of entries the cache holds, and expiries its
// heap holds.
func (m *MemoryResults) size() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries), m.byExpiry.Len()
}

// expiry is a key and an expiry it was stored with.
type expiry struct {
	key string
	at  time.Time
}

// expiryHeap is a min-heap of expiries (container/heap).
type expiryHeap []expiry

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h expiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *expiryHeap) Push(x any)        { *h = append(*h, x.(expiry)) }
func (h *expiryHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

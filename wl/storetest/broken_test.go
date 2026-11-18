package storetest

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/wl"
)

// brokenStore is a store that breaks atomicity in the two ways the
// atomicity tests must catch: an update writes the first half of its value
// and then the whole, each under the lock, so a reader between the two
// sees half a value; and a create checks that the path is free and adds
// its entry under two separate locks, so two creates can both win.
type brokenStore struct {
	mu      sync.Mutex
	entries map[string]wl.Resource
}

func (s *brokenStore) Get(_ context.Context, ws, path string) (wl.Resource, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.entries[ws+"\x00"+path]
	return wl.Resource{Type: r.Type, Value: append(json.RawMessage(nil), r.Value...)}, ok, nil
}

func (s *brokenStore) Create(_ context.Context, ws, path string, r wl.Resource) (bool, error) {
	s.mu.Lock()
	_, taken := s.entries[ws+"\x00"+path]
	s.mu.Unlock()
	if taken {
		return false, nil
	}
	time.Sleep(time.Millisecond)
	s.mu.Lock()
	s.entries[ws+"\x00"+path] = wl.Resource{Type: r.Type, Value: append(json.RawMessage(nil), r.Value...)}
	s.mu.Unlock()
	return true, nil
}

func (s *brokenStore) UpdateValue(_ context.Context, ws, path string, v json.RawMessage) (bool, error) {
	for _, part := range []json.RawMessage{v[:len(v)/2], v} {
		s.mu.Lock()
		r, ok := s.entries[ws+"\x00"+path]
		if !ok {
			s.mu.Unlock()
			return false, nil
		}
		s.entries[ws+"\x00"+path] = wl.Resource{Type: r.Type, Value: append(json.RawMessage(nil), part...)}
		s.mu.Unlock()
	}
	return true, nil
}

func (s *brokenStore) Delete(_ context.Context, ws, path string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[ws+"\x00"+path]
	delete(s.entries, ws+"\x00"+path)
	return ok, nil
}

// TestAtomicityTestsCanFail checks that the atomicity tests catch the
// broken store: the torn-read test finds a read that is not one whole
// value, and the create race more than one winner.
func TestAtomicityTestsCanFail(t *testing.T) {
	problems, reads, _ := tornReads(&brokenStore{entries: map[string]wl.Resource{}}, time.Second)
	if len(problems) == 0 {
		t.Errorf("the torn-read test found nothing wrong in %d reads of a store that writes an update in two steps", reads)
	}
	winners, _ := raceCreates(&brokenStore{entries: map[string]wl.Resource{}})
	if len(winners) < 2 {
		t.Errorf("the create race found %v winning on a store that checks and adds under two locks", winners)
	}
}

// gapStore replaces an entry on update by deleting it and creating it again,
// each under the lock: a reader between the two finds no entry. Its updates
// take turns (wmu), so each finds the entry and reports true: only the gap
// is wrong.
type gapStore struct {
	brokenStore
	wmu sync.Mutex
}

func (s *gapStore) UpdateValue(_ context.Context, ws, path string, v json.RawMessage) (bool, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	r, ok := s.entries[ws+"\x00"+path]
	delete(s.entries, ws+"\x00"+path)
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	time.Sleep(time.Microsecond)
	s.mu.Lock()
	s.entries[ws+"\x00"+path] = wl.Resource{Type: r.Type, Value: append(json.RawMessage(nil), v...)}
	s.mu.Unlock()
	return true, nil
}

// stateOnlyStore keeps every entry as type state, whatever type it was
// created with.
type stateOnlyStore struct{ brokenStore }

func (s *stateOnlyStore) Create(ctx context.Context, ws, path string, r wl.Resource) (bool, error) {
	return s.brokenStore.Create(ctx, ws, path, wl.Resource{Type: "state", Value: r.Value})
}

// TestContractCatchesGapsAndLostTypes checks that the torn-read test finds
// an update that leaves the path empty for a moment, and the type test a
// store that keeps no type.
func TestContractCatchesGapsAndLostTypes(t *testing.T) {
	if problems, reads, _ := tornReads(&gapStore{brokenStore: brokenStore{entries: map[string]wl.Resource{}}}, time.Second); len(problems) == 0 {
		t.Errorf("the torn-read test found nothing wrong in %d reads of a store that deletes and creates on update", reads)
	}
	if problems := typeProblems(&stateOnlyStore{brokenStore{entries: map[string]wl.Resource{}}}); len(problems) == 0 {
		t.Error("the type test found nothing wrong in a store that keeps every entry as state")
	}
}

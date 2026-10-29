package platform

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// The in-memory result cache answers a value until its expiry, on the
// time it is given, and never after: an entry expires at its instant
// (FX-40's "not expired" is an expiry after now). A Put replaces, and the
// cache keeps copies, so a caller's later change to its slice changes
// nothing.
func TestMemoryResults(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryResults()
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	v := []byte(`{"a":1}`)
	if err := m.Put(ctx, "k", v, t0.Add(60*time.Second)); err != nil {
		t.Fatal(err)
	}
	v[2] = 'X'
	for _, c := range []struct {
		at   time.Duration
		want bool
	}{{0, true}, {59 * time.Second, true}, {60 * time.Second, false}, {61 * time.Second, false}} {
		got, ok, err := m.Get(ctx, "k", t0.Add(c.at))
		if err != nil || ok != c.want || ok && string(got) != `{"a":1}` {
			t.Errorf("at %v: %q %v %v", c.at, got, ok, err)
		}
	}
	// The expired entry is gone; another key never was there.
	if n, _ := m.size(); n != 0 {
		t.Fatalf("%d entries after the expired Get", n)
	}
	if _, ok, _ := m.Get(ctx, "other", t0); ok {
		t.Fatal("a hit for a key never stored")
	}
	t1 := t0.Add(2 * time.Minute)
	_ = m.Put(ctx, "k", []byte("1"), t1.Add(time.Second))
	_ = m.Put(ctx, "k", []byte("2"), t1.Add(time.Second))
	got, ok, _ := m.Get(ctx, "k", t1)
	if !ok || string(got) != "2" {
		t.Fatalf("after a replace: %q %v", got, ok)
	}
	got[0] = 'X'
	if again, _, _ := m.Get(ctx, "k", t1); string(again) != "2" {
		t.Fatalf("the cached value changed with the returned slice: %q", again)
	}
}

// The cache does not keep what has expired: every call drops each entry
// that expired by the latest time a Get gave, however many entries there
// are, so after a burst of entries expires, a small working set leaves
// only its own live entries; and a key stored again many times keeps the
// heap of expiries bounded by about twice the live entries.
func TestMemoryResultsReclaim(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryResults()
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for i := range 4096 {
		_ = m.Put(ctx, fmt.Sprint("burst", i), []byte("payload"), t0.Add(time.Second))
	}
	if n, _ := m.size(); n != 4096 {
		t.Fatalf("%d entries after the burst", n)
	}
	// A small working set after the burst has expired: one key, looked up
	// and stored again, each time a little later.
	for i := range 100 {
		later := t0.Add(time.Duration(i+2) * time.Second)
		if _, ok, _ := m.Get(ctx, "hot", later); ok && i == 0 {
			t.Fatal("a hit before the first store")
		}
		_ = m.Put(ctx, "hot", []byte("payload"), later.Add(time.Hour))
	}
	if n, h := m.size(); n != 1 || h > minResultRebuild+1 {
		t.Fatalf("%d entries and %d expiries after the burst expired, want only the hot key's", n, h)
	}
	// The hot key, stored again 10000 times within its TTL, keeps one live
	// entry and a bounded heap.
	for i := range 10000 {
		_ = m.Put(ctx, "hot", []byte("payload"), t0.Add(2*time.Hour+time.Duration(i)*time.Millisecond))
	}
	if n, h := m.size(); n != 1 || h > minResultRebuild+1 {
		t.Fatalf("%d entries and %d expiries after the hot key was stored again", n, h)
	}
	if got, ok, _ := m.Get(ctx, "hot", t0.Add(time.Hour)); !ok || string(got) != "payload" {
		t.Fatalf("the hot key: %q %v", got, ok)
	}
	// An entry is gone by the first call after its expiry, a Put's
	// included, once a Get has given a time past it.
	_ = m.Put(ctx, "short", []byte("x"), t0.Add(3*time.Hour))
	if _, ok, _ := m.Get(ctx, "other", t0.Add(4*time.Hour)); ok {
		t.Fatal("a hit for a key never stored")
	}
	if n, _ := m.size(); n != 0 {
		t.Fatalf("%d entries after every expiry passed", n)
	}
}

// Services answer the result cache the host gives them, or their own
// in-memory one, the same for every caller; a nil *Services has none.
func TestServicesResultCache(t *testing.T) {
	var none *Services
	if none.ResultCache() != nil {
		t.Fatal("a nil *Services has a result cache")
	}
	s := &Services{}
	if s.ResultCache() == nil || s.ResultCache() != s.ResultCache() {
		t.Fatal("the default result cache is not one per Services")
	}
	host := NewMemoryResults()
	if (&Services{Results: host}).ResultCache() != host {
		t.Fatal("the host's result cache is not used")
	}
}

// Concurrent Gets and Puts are safe (run under -race in CI) and each Get
// sees a whole value some Put wrote.
func TestMemoryResultsConcurrent(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryResults()
	t0 := time.Now()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				key := fmt.Sprint("k", i%16)
				_ = m.Put(ctx, key, bytes.Repeat([]byte{byte('a' + g)}, 8), t0.Add(time.Duration(i%3)*time.Second))
				if v, ok, err := m.Get(ctx, key, t0); err != nil || ok && (len(v) != 8 || !bytes.Equal(v, bytes.Repeat(v[:1], 8))) {
					t.Errorf("a torn value %q (%v)", v, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

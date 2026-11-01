package platform

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingStore is a Store that counts the reads of each path, and can
// hold a read until release is closed or fail the next read.
type countingStore struct {
	*Store
	mu      sync.Mutex
	reads   map[string]int
	release chan struct{}
	fail    atomic.Bool
}

func newCountingStore() *countingStore {
	return &countingStore{Store: NewStore(), reads: map[string]int{}}
}

func (s *countingStore) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[path]
}

func (s *countingStore) read(path string) error {
	s.mu.Lock()
	s.reads[path]++
	release := s.release
	s.mu.Unlock()
	if release != nil {
		<-release
	}
	if s.fail.Swap(false) {
		return errors.New("the provider is unreachable")
	}
	return nil
}

func (s *countingStore) Resource(ctx context.Context, path string) (Resource, bool, error) {
	if err := s.read(path); err != nil {
		return Resource{}, false, err
	}
	return s.Store.Resource(ctx, path)
}

func (s *countingStore) Variable(ctx context.Context, path string) (Variable, bool, error) {
	if err := s.read(path); err != nil {
		return Variable{}, false, err
	}
	return s.Store.Variable(ctx, path)
}

// TestReadCache checks deviation D21's cache: one provider read per path,
// found or not, whatever reads it (a resource read through the walker,
// with its nested references; a variable read); an error is not kept;
// reads of one path that overlap share one provider read; and an update
// makes the next read ask again.
func TestReadCache(t *testing.T) {
	ctx := context.Background()
	st := newCountingStore()
	if err := st.SetResource("u/t/db", Resource{Type: "c_db", Value: json.RawMessage(`{"host":"db.example.invalid","password":"$var:u/t/pw","inner":"$res:u/t/inner"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetResource("u/t/inner", Resource{Type: "c_inner", Value: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}
	st.SetVariable("u/t/pw", Variable{Value: "secret-1", IsSecret: true})
	c := NewReadCache(st, st)
	s := &Services{Resources: c, Variables: c}

	first, err := s.InterpolatedResource(ctx, "u/t/db", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, err := s.InterpolatedResource(ctx, "u/t/db", nil)
		if err != nil || string(again) != string(first) {
			t.Fatalf("a later read: %s, %v; the first was %s", again, err, first)
		}
		if v, err := s.VariableValue(ctx, "u/t/pw"); err != nil || v != "secret-1" {
			t.Fatalf("the variable: %q, %v", v, err)
		}
	}
	for _, p := range []string{"u/t/db", "u/t/inner", "u/t/pw"} {
		if n := st.count(p); n != 1 {
			t.Fatalf("%s was read %d times from the provider, want 1", p, n)
		}
	}

	// A path the provider does not have is asked about once too.
	for range 2 {
		if _, ok, err := c.Resource(ctx, "u/t/none"); ok || err != nil {
			t.Fatalf("a missing resource: %v, %v", ok, err)
		}
	}
	if n := st.count("u/t/none"); n != 1 {
		t.Fatalf("a missing resource was asked about %d times", n)
	}

	// An error is not kept: the next read asks again.
	st.fail.Store(true)
	if _, _, err := c.Variable(ctx, "u/t/other"); err == nil {
		t.Fatal("the provider's error was not returned")
	}
	if _, _, err := c.Variable(ctx, "u/t/other"); err != nil {
		t.Fatalf("the read after an error: %v", err)
	}
	if n := st.count("u/t/other"); n != 2 {
		t.Fatalf("u/t/other was read %d times, want 2", n)
	}

	// An update through the cache: the next read asks the provider again
	// and sees the new value.
	v := "secret-2"
	if err := s.UpdateVariable(ctx, "u/t/pw", &v, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.VariableValue(ctx, "u/t/pw"); err != nil || got != "secret-2" || st.count("u/t/pw") != 2 {
		t.Fatalf("after the update: %q, %v, %d reads", got, err, st.count("u/t/pw"))
	}

	// Overlapping reads of one path share one provider read; a reader whose
	// context ends stops waiting.
	st.release = make(chan struct{})
	var wg sync.WaitGroup
	results := make([]string, 16)
	for i := range results {
		wg.Go(func() {
			r, ok, err := c.Resource(ctx, "u/t/slow")
			results[i] = string(r.Value)
			if ok || err != nil {
				results[i] = "unexpected"
			}
		})
	}
	short, cancel := context.WithCancel(ctx)
	waited := make(chan error, 1)
	go func() {
		for st.count("u/t/slow") == 0 {
			time.Sleep(time.Millisecond)
		}
		_, _, err := c.Resource(short, "u/t/slow")
		waited <- err
	}()
	cancel()
	if err := <-waited; !errors.Is(err, context.Canceled) {
		t.Fatalf("a reader whose context ended got %v", err)
	}
	close(st.release)
	wg.Wait()
	if n := st.count("u/t/slow"); n != 1 {
		t.Fatalf("16 overlapping reads made %d provider reads", n)
	}
	for _, r := range results {
		if r == "unexpected" {
			t.Fatal("an overlapping read got another answer")
		}
	}
}

// TestReadCacheResolvesPerJob checks that the cache keeps the value as
// stored: a $WM_* string in it resolves for each job that reads it, from
// one provider read.
func TestReadCacheResolvesPerJob(t *testing.T) {
	ctx := context.Background()
	st := newCountingStore()
	if err := st.SetResource("u/t/job", Resource{Type: "c_job", Value: json.RawMessage(`{"job":"$WM_JOB_ID","ws":"$WM_WORKSPACE"}`)}); err != nil {
		t.Fatal(err)
	}
	c := NewReadCache(st, st)
	s := &Services{Resources: c, Variables: c}
	for _, job := range []string{"job-a", "job-b"} {
		got, err := s.InterpolatedResource(ctx, "u/t/job", map[string]string{"WM_JOB_ID": job, "WM_WORKSPACE": "ws"})
		if err != nil || string(got) != `{"ws":"ws","job":"`+job+`"}` {
			t.Fatalf("job %s read %s, %v", job, got, err)
		}
	}
	if n := st.count("u/t/job"); n != 1 {
		t.Fatalf("two jobs' reads made %d provider reads", n)
	}
}

// TestReadCacheReadOnly checks that an update through the cache over a
// provider that takes no writes fails as the provider itself would, and
// that no providers answer nothing.
func TestReadCacheReadOnly(t *testing.T) {
	ctx := context.Background()
	c := NewReadCache(ResourceChain{NewStore()}, readOnlyVariables{})
	v := "x"
	if _, err := c.UpdateVariable(ctx, "u/t/v", &v, nil); !errors.Is(err, ErrReadOnlyVariables) {
		t.Fatalf("an update over a read-only provider: %v", err)
	}
	none := NewReadCache(nil, nil)
	if _, ok, err := none.Resource(ctx, "u/t/x"); ok || err != nil {
		t.Fatalf("no resource provider: %v, %v", ok, err)
	}
	if _, ok, err := none.Variable(ctx, "u/t/x"); ok || err != nil {
		t.Fatalf("no variable provider: %v, %v", ok, err)
	}
}

type readOnlyVariables struct{}

func (readOnlyVariables) Variable(context.Context, string) (Variable, bool, error) {
	return Variable{}, false, nil
}

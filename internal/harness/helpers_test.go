package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The Bun tests run the real harness on the pinned Bun. WL_BUN names the
// bun executable; without it they skip, unless WL_REQUIRE_BUN is set, as CI
// sets it, which turns a missing Bun into a failure.
func bunPath(t testing.TB) string {
	t.Helper()
	p := os.Getenv("WL_BUN")
	if p == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set; it names the pinned Bun " + PinnedBun)
	}
	return p
}

func repoPath(t testing.TB, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fixtureBundle is the synthetic bundle of conformance/protocol/bundle.
func fixtureBundle(t testing.TB) (dir, digest string) {
	t.Helper()
	dir = repoPath(t, "conformance/protocol/bundle")
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return dir, "sha256:" + hex.EncodeToString(sum[:])
}

// bunConfig is a ProcessConfig for the real Bun harness on the fixture
// bundle, with its diagnostics captured.
func bunConfig(t testing.TB) (ProcessConfig, *syncBuffer) {
	t.Helper()
	bun := bunPath(t)
	dir, digest := fixtureBundle(t)
	path, args := BunCommand(bun, repoPath(t, "harnesses/bun/runtime/main.ts"), dir)
	diag := &syncBuffer{}
	return ProcessConfig{
		Path:        path,
		Args:        args,
		WorkRoot:    t.TempDir(),
		Kind:        "bun",
		Runtime:     PinnedBun,
		Bundle:      digest,
		Diagnostics: diag,
	}, diag
}

func spawn(t testing.TB, cfg ProcessConfig) *Process {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := Spawn(ctx, cfg)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() {
		p.Kill()
		<-p.Done()
	})
	return p
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// recorder is a Reporter that records its calls in order. A call made
// while another is still running is recorded as "CONCURRENT", which breaks
// every expected call log: the executor must never call one invocation's
// Reporter from two goroutines at once.
type recorder struct {
	busy    atomic.Int32
	mu      sync.Mutex
	calls   []string
	late    []engine.Late
	settled chan engine.SettleKind
}

func newRecorder() *recorder { return &recorder{settled: make(chan engine.SettleKind, 4)} }

func (r *recorder) enter() func() {
	if r.busy.Add(1) != 1 {
		r.mu.Lock()
		r.calls = append(r.calls, "CONCURRENT")
		r.mu.Unlock()
	}
	// A short pause widens the window in which an overlapping call would
	// be seen.
	time.Sleep(time.Millisecond)
	return func() { r.busy.Add(-1) }
}

func (r *recorder) Late(l engine.Late) {
	defer r.enter()()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf("late %s final=%v %s", l.Kind, l.Final, l.Body))
	r.late = append(r.late, l)
}

func (r *recorder) Warning(w engine.Warning) {
	defer r.enter()()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "warning "+w.Code)
}

func (r *recorder) Settled(k engine.SettleKind) {
	defer r.enter()()
	r.mu.Lock()
	r.calls = append(r.calls, "settled "+string(k))
	r.mu.Unlock()
	r.settled <- k
}

// note records a call of the test's own, such as a pool's slot release,
// in the same log.
func (r *recorder) note(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *recorder) log() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, "; ")
}

// waitSettled waits for the invocation to settle and returns how.
func (r *recorder) waitSettled(t testing.TB, within time.Duration) engine.SettleKind {
	t.Helper()
	select {
	case k := <-r.settled:
		return k
	case <-time.After(within):
		t.Fatalf("not settled within %s; calls: %s", within, r.log())
		return ""
	}
}

var invSeq struct {
	sync.Mutex
	n int
}

// newInvocation returns an invocation of a fixture script with the given
// argument text.
func newInvocation(script, args string, rep engine.Reporter) engine.Invocation {
	invSeq.Lock()
	invSeq.n++
	n := invSeq.n
	invSeq.Unlock()
	return engine.Invocation{
		ID:          fmt.Sprintf("inv-%d", n),
		Run:         fmt.Sprintf("run-%d", n),
		Site:        conformance.SitePath{}.Child(conformance.Module("a")),
		Script:      script,
		Entry:       "main",
		Args:        jsonval.FromProducer([]byte(args)),
		Env:         map[string]string{"WM_FLOW_STEP_ID": "a"},
		Boundary:    engine.BoundaryBun,
		ModuleState: engine.ModuleStateWarm,
		Report:      rep,
	}
}

func execute(t testing.TB, x engine.StepExecutor, inv engine.Invocation, within time.Duration) (engine.StepOutcome, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	return x.Execute(ctx, inv)
}

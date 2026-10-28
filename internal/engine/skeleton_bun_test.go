package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// These tests run the walking skeleton on the real Bun harness and the real
// protocol, on the pinned Bun. WL_BUN names the bun executable; without it
// they skip, unless WL_REQUIRE_BUN is set, as CI sets it.
func bunSkeleton(t *testing.T) (*engine.Skeleton, *harness.Pool) {
	t.Helper()
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set; it names the pinned Bun " + harness.PinnedBun)
	}
	dir, err := filepath.Abs("testdata/w1")
	if err != nil {
		t.Fatal(err)
	}
	main, err := filepath.Abs("../../harnesses/bun/runtime/main.ts")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	path, args := harness.BunCommand(bun, main, dir)
	pool, err := harness.NewPool(harness.PoolConfig{
		Key:     harness.Key{Class: "bun", Runtime: harness.PinnedBun, Deps: "w1", ModuleState: engine.ModuleStateWarm},
		Process: harness.ProcessConfig{Path: path, Args: args, WorkRoot: t.TempDir(), Kind: "bun", Runtime: harness.PinnedBun, Bundle: digest, Diagnostics: &testLog{t}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool.Close(ctx)
	})
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { js.Close() })
	s, err := engine.NewSkeleton(engine.SkeletonConfig{BundleDir: dir, Bundle: digest, Exec: pool, JS: js})
	if err != nil {
		t.Fatal(err)
	}
	return s, pool
}

type testLog struct{ t *testing.T }

func (l *testLog) Write(b []byte) (int, error) {
	l.t.Logf("%s", strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

func runFlow(t *testing.T, s *engine.Skeleton, path, args string) *engine.Trace {
	t.Helper()
	tr, err := s.Run(context.Background(), engine.SkeletonRun{Path: path, Args: jsonval.FromProducer([]byte(args))})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// W1's row: one sequential flow, one Bun step (here two: by path and
// inline), a static and a JavaScript transform, on the real protocol.
func TestBunSkeletonFlow(t *testing.T) {
	s, _ := bunSkeleton(t)
	tr := runFlow(t, s, "f/w1/transforms", `{"name":"ada","n":2}`)
	const want = `{"prev":{"n":1.5,"obj":{"k":2},"sum":3,"name":"ADA","params":{},"greeting":"hello ${flow_input.name}"},"missing":null}`
	if tr.Status != conformance.RunSuccess || string(tr.Result.Producer()) != want {
		t.Fatalf("status %s, result\n got %s\nwant %s", tr.Status, tr.Result, want)
	}
	tr = runFlow(t, s, "f/w1/throws", `{}`)
	// Stored as jsonb: shorter keys first (ADR 0005).
	got := string(tr.Result.Producer())
	if tr.Status != conformance.RunFailure || !strings.HasPrefix(got, `{"error":{"name":"Error","stack":"Error: boom`) ||
		!strings.HasSuffix(got, `"message":"boom","step_id":"a"}}`) {
		t.Fatalf("throws: %s %s", tr.Status, got)
	}
}

// FX-24, WR-37 and D2 on the real harness: the step that never settles is
// abandoned at its timeout with Windmill's text, and the harness lives on.
func TestBunSkeletonStepTimeout(t *testing.T) {
	s, pool := bunSkeleton(t)
	start := time.Now()
	tr := runFlow(t, s, "f/w1/timeout", `{}`)
	const want = `{"error":{"name":"ExecutionErr","message":"execution error:\njob process terminated due to timeout after exceeding job-specific duration limit","step_id":"a"}}`
	if d := time.Since(start); tr.Status != conformance.RunFailure || string(tr.Result.Producer()) != want || d > 3*time.Second {
		t.Fatalf("after %v: %s %s", d, tr.Status, tr.Result)
	}
	tr = runFlow(t, s, "f/w1/echo", `{"x":1}`)
	if tr.Status != conformance.RunSuccess || string(tr.Result.Producer()) != `{"x":1}` {
		t.Fatalf("after the timeout: %s %s", tr.Status, tr.Result)
	}
	if st := pool.Stats(); len(st) < 1 {
		t.Fatalf("pool %+v", st)
	}
}

// W1's exit gate: "a first per-step latency figure". A one-step flow, run
// sequentially on one warm harness: input transform in QuickJS-ng, invoke,
// result. It reports and sets no threshold (the budget gate is B2b's).
func TestBunSkeletonStepLatency(t *testing.T) {
	s, _ := bunSkeleton(t)
	n := 300
	if testing.Short() {
		n = 50
	}
	for range 20 { // warm the module and the pools
		runFlow(t, s, "f/w1/echo", `{"x":0}`)
	}
	run := make([]time.Duration, 0, n)
	harnessSpan := make([]time.Duration, 0, n)
	for i := range n {
		start := time.Now()
		tr := runFlow(t, s, "f/w1/echo", fmt.Sprintf(`{"x":%d}`, i))
		run = append(run, time.Since(start))
		if tr.Status != conformance.RunSuccess {
			t.Fatalf("run %d: %s %s", i, tr.Status, tr.Result)
		}
		for _, sp := range tr.Spans {
			if sp.Kind == engine.SpanHarness {
				harnessSpan = append(harnessSpan, sp.Duration)
			}
		}
	}
	t.Logf("W1 per-step latency, one-step flow on one warm Bun %s harness, %d sequential runs: run %s; invoke to result %s",
		harness.PinnedBun, n, percentiles(run), percentiles(harnessSpan))
}

func percentiles(d []time.Duration) string {
	s := slices.Clone(d)
	slices.Sort(s)
	at := func(q float64) time.Duration { return s[min(len(s)-1, int(q*float64(len(s))))] }
	return fmt.Sprintf("p50 %v, p95 %v, p99 %v", at(0.50).Round(time.Microsecond), at(0.95).Round(time.Microsecond), at(0.99).Round(time.Microsecond))
}

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// onRun returns the arguments that make a fake invocation one of run, with
// extra arguments if any.
func onRun(run string, extra ...map[string]any) map[string]any {
	a := map[string]any{"run": run}
	for _, e := range extra {
		maps.Copy(a, e)
	}
	return a
}

// statsOf returns the pool's snapshot of the process pid.
func statsOf(pool *Pool, pid int) (ProcessStats, bool) {
	for _, s := range pool.Stats() {
		if s.PID == pid {
			return s, true
		}
	}
	return ProcessStats{}, false
}

// releaseHold opens the hold file of the held or abandoned invocation name.
func releaseHold(t *testing.T, ctl, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ctl, name+".hold"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A pinned run's invocations stay on its process after its own abandoned
// step tainted it, the one exception to the taint rule; an unpinned run
// goes to the replacement. The process does not leave while the run is
// live.
func TestPinnedRunStaysOnTaintedProcess(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1, RecycleTimeout: time.Minute})
	pool.PinRun("A")
	a := fakeRun(t, pool, "s", onRun("A"))
	abandon(t, pool, ctl, "x", onRun("A"))
	if b := fakeRun(t, pool, "s", onRun("B")); b == a {
		t.Fatalf("an unpinned run went to the tainted process %d", a)
	}
	if got := fakeRun(t, pool, "s", onRun("A")); got != a {
		t.Fatalf("the pinned run's next invocation went to %d, want its tainted process %d", got, a)
	}
	if slices.Contains(log.after(""), fmt.Sprintf("drain %d", a)) {
		t.Fatalf("the pinned run's process drained while the run is live: %q", log.after(""))
	}
}

// No new pin goes to a tainted process: a pinned run's first invocation
// goes to a serving one. A tainted process that returns to service takes
// new pins again.
func TestNoNewPinOnTaintedProcess(t *testing.T) {
	pool, ctl, _ := fakePool(t, PoolConfig{Max: 2, RecycleTimeout: time.Minute})
	pool.PinRun("B")
	p := fakeRun(t, pool, "s", onRun("B"))
	// B's abandoned step taints p, which B's live pin keeps, idle.
	abandon(t, pool, ctl, "x", onRun("B"))
	pool.PinRun("A")
	q := fakeRun(t, pool, "s", onRun("A"))
	if q == p {
		t.Fatalf("a new pin went to the tainted process %d", p)
	}
	if got := fakeRun(t, pool, "s", onRun("A")); got != q {
		t.Fatalf("the pinned run moved from %d to %d", q, got)
	}
	// p returns to service once its abandoned invocation settles: the pool
	// is below Max without it. Of two idle processes the first takes the
	// next new pin.
	releaseHold(t, ctl, "x")
	waitFor(t, "the tainted process to return to service", func() bool {
		s, ok := statsOf(pool, p)
		return ok && !s.Tainted && !s.Surplus && s.Abandoned == 0
	})
	pool.PinRun("C")
	if got := fakeRun(t, pool, "s", onRun("C")); got != p {
		t.Fatalf("a new pin went to %d; the process back in service is %d", got, p)
	}
	if got := fakeRun(t, pool, "s", onRun("C")); got != p {
		t.Fatalf("the new pin moved to %d", got)
	}
}

// A pin holds its process until its run has ended: a tainted process that
// is idle while a pinned run is between steps does not leave, and leaves
// once the run has ended.
func TestPinHoldsUntilRunEnd(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1})
	pool.PinRun("A")
	a := fakeRun(t, pool, "s", onRun("A"))
	// Another run's abandoned step taints a; A is between steps.
	abandon(t, pool, ctl, "x", onRun("B"))
	if c := fakeRun(t, pool, "s", onRun("C")); c == a {
		t.Fatalf("an unpinned run went to the tainted process %d", a)
	}
	if slices.Contains(log.after(""), fmt.Sprintf("drain %d", a)) {
		t.Fatalf("the idle tainted process drained while a pinned run is live: %q", log.after(""))
	}
	log.note("run ended")
	pool.RunEnded("A")
	log.waitFor(t, "run ended", fmt.Sprintf("drain %d", a))
}

// The surplus rule with a pin: a tainted process whose taint clears while
// its replacement fills Max is surplus. It takes no new unpinned
// invocation, keeps serving its pinned run, and leaves once the run has
// ended.
func TestPinAfterTaintClears(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1})
	pool.PinRun("A")
	a := fakeRun(t, pool, "s", onRun("A"))
	abandon(t, pool, ctl, "x", onRun("B"))
	q := fakeRun(t, pool, "s", onRun("C"))
	releaseHold(t, ctl, "x")
	waitFor(t, "the process to become surplus", func() bool {
		s, ok := statsOf(pool, a)
		return ok && s.Surplus && !s.Tainted
	})
	if got := fakeRun(t, pool, "s", onRun("D")); got != q {
		t.Fatalf("an unpinned invocation went to %d, want the replacement %d", got, q)
	}
	if got := fakeRun(t, pool, "s", onRun("A")); got != a {
		t.Fatalf("the pinned run went to %d, want its surplus process %d", got, a)
	}
	if slices.Contains(log.after(""), fmt.Sprintf("drain %d", a)) {
		t.Fatalf("the surplus process drained while its pinned run is live: %q", log.after(""))
	}
	log.note("run ended")
	pool.RunEnded("A")
	log.waitFor(t, "run ended", fmt.Sprintf("drain %d", a))
}

// The cap still bounds a pinned run's process: with Cap 1 reached there by
// the run's own held invocation, its next invocation waits for that slot,
// while an unpinned one goes to another process.
func TestPinnedRunCapFull(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 2, Cap: 1})
	pool.PinRun("A")
	h := startHeld(t, pool, ctl, "h", onRun("A"))
	log.note("pinned at cap")
	w := goRun(pool, "s", onRun("A"))
	log.waitFor(t, "pinned at cap", "wait")
	b := fakeRun(t, pool, "s", onRun("B"))
	notReturned(t, w, "the pinned invocation while its process is at the cap")
	h.release(t)
	r := <-h.done
	p := pidOfOutcome(t, r.out, r.err)
	if b == p {
		t.Fatalf("the unpinned invocation went to the pinned process %d at its cap", p)
	}
	if got := wait(t, w); got != p {
		t.Fatalf("the pinned invocation went to %d, want %d", got, p)
	}
}

// A pinned run never moves. An invocation in flight when its process dies
// fails as escaped, as any does; the run's next invocation or host call
// fails with engine.ErrPinnedProcessLost, not dispatched. After the run
// has ended, its late calls go to another process.
func TestPinnedRunProcessLost(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1}, "WL_FAKE_PLUGIN=same")
	pool.PinRun("A")
	h := startHeld(t, pool, ctl, "h", onRun("A"))
	var a int
	for _, s := range pool.Stats() {
		a = s.PID
	}
	memberProcess(t, pool, a).Kill()
	if r := <-h.done; r.err != nil || r.out.Kind != engine.OutcomeEscaped {
		t.Fatalf("the invocation in flight when the process died: %+v, %v; want escaped", r.out, r.err)
	}
	log.waitFor(t, "", fmt.Sprintf("exit %d", a))
	rec := newRecorder()
	inv := fakeInvocation("s", onRun("A"))
	inv.Report = rec
	if _, err := execute(t, pool, inv, 10*time.Second); !errors.Is(err, engine.ErrPinnedProcessLost) {
		t.Fatalf("the pinned run's next invocation: %v; want pinned_process_lost", err)
	}
	if got := rec.log(); got != "settled not_started" {
		t.Fatalf("reporter calls %q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.HostCall(ctx, "resource", "A", "", jsonval.FromProducer([]byte(`{}`))); !errors.Is(err, engine.ErrPinnedProcessLost) {
		t.Fatalf("the pinned run's host call: %v; want pinned_process_lost", err)
	}
	pool.RunEnded("A")
	if _, err := pool.HostCall(ctx, "onRunSettled", "A", "", jsonval.FromProducer([]byte(`{}`))); err != nil {
		t.Fatalf("a late host call of the ended run: %v", err)
	}
}

// A pinned run's host calls go to its process, also when that process is
// tainted, and, after the run has ended, while the process lives: the
// late onRunSettled reaches it. Another live pin keeps the process here.
func TestPinnedRunHostCalls(t *testing.T) {
	pool, ctl, _ := fakePool(t, PoolConfig{Min: 2, Max: 2, RecycleTimeout: time.Minute}, "WL_FAKE_PLUGIN=same")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	pool.PinRun("A")
	pool.PinRun("B")
	a := fakeRun(t, pool, "s", onRun("A"))
	if got := fakeRun(t, pool, "s", onRun("B")); got != a {
		t.Fatalf("the test needs both pins on one process: %d and %d", a, got)
	}
	abandon(t, pool, ctl, "x", onRun("A"))
	hostCall := func(hook, run string) int {
		t.Helper()
		out, err := pool.HostCall(ctx, hook, run, "", jsonval.FromProducer([]byte(`{}`)))
		var r struct{ PID int }
		if err != nil || json.Unmarshal(out.Producer(), &r) != nil {
			t.Fatalf("%s of %s: %s, %v", hook, run, out.Producer(), err)
		}
		return r.PID
	}
	if got := hostCall("resource", "A"); got != a {
		t.Fatalf("the pinned run's host call went to %d, want its tainted process %d", got, a)
	}
	pool.RunEnded("A")
	if got := hostCall("onRunSettled", "A"); got != a {
		t.Fatalf("the ended run's onRunSettled went to %d, want its process %d", got, a)
	}
	if got := hostCall("resource", "C"); got == a {
		t.Fatalf("an unpinned run's host call went to the tainted process %d", a)
	}
}

// The live count with pins (Max 1, MaxLive 2). A pinned run's process is
// tainted, its replacement starts, and its taint clears while the
// replacement fills Max: it is surplus, still serving the pin, with
// unrelated work in flight. The pin ends; that process drains only once the
// unrelated work, held past RecycleTimeout, has completed, and frees its
// place only at its exit. Meanwhile the replacement trips the RSS trigger
// and starts no third process (an invocation held in it keeps it from
// draining); the next replacement trips the module trigger and keeps
// serving while the first leaver for a trigger holds the slot. Spawns
// minus observed exits never pass 2.
func TestPoolAliveCount(t *testing.T) {
	const recycle = 50 * time.Millisecond
	// The first process runs three scripts (s, held, abandoned), which
	// Modules 3 allows.
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1, MaxLive: 2, RecycleTimeout: recycle, Recycle: RecycleTriggers{RSS: 1000, Modules: 3}})
	pool.PinRun("A")
	a := fakeRun(t, pool, "s", onRun("A"))
	u := startHeld(t, pool, ctl, "u", onRun("U"))
	abandon(t, pool, ctl, "x", onRun("X"))
	q := fakeRun(t, pool, "s", onRun("C"))
	releaseHold(t, ctl, "x")
	waitFor(t, "the pinned run's process to become surplus", func() bool {
		s, ok := statsOf(pool, a)
		return ok && s.Surplus && !s.Tainted
	})
	if got := fakeRun(t, pool, "s", onRun("A")); got != a {
		t.Fatalf("the pinned run went to %d, want its surplus process %d", got, a)
	}
	log.note("pin ended")
	pool.RunEnded("A")
	unrelatedSince := time.Now()
	if slices.Contains(log.after("pin ended"), fmt.Sprintf("drain %d", a)) {
		t.Fatalf("the process drained with unrelated work in flight: %q", log.after(""))
	}
	// The replacement trips the RSS trigger with an invocation held in it;
	// MaxLive 2 holds back its own replacement.
	hq := startHeld(t, pool, ctl, "hq", onRun("H"))
	for range 2 {
		if got := fakeRun(t, pool, "s", onRun("D", map[string]any{"rss": 2000})); got != q {
			t.Fatalf("a report went to %d, want %d", got, q)
		}
	}
	if n := log.count("spawn"); n != 2 {
		t.Fatalf("%d spawns with two processes alive and MaxLive 2: %q", n, log.after(""))
	}
	// The unrelated work stays held past RecycleTimeout, then completes.
	time.Sleep(time.Until(unrelatedSince.Add(2 * recycle)))
	log.note("unrelated done")
	u.release(t)
	if r := <-u.done; pidOfOutcome(t, r.out, r.err) != a {
		t.Fatalf("the unrelated work ran elsewhere: %+v", r.out)
	}
	drainA, exitA := fmt.Sprintf("drain %d", a), fmt.Sprintf("exit %d", a)
	log.waitFor(t, "unrelated done", exitA)
	if !log.before("note: unrelated done", 1, drainA, 1) || !log.before(exitA, 1, "spawn", 3) {
		t.Fatalf("want the unrelated work's end, the drain, the exit, then the third spawn: %q", log.after(""))
	}
	// The next replacement trips the module trigger while the RSS leaver
	// still holds the slot: it keeps serving.
	r := fakeRun(t, pool, "s", onRun("E"))
	if r == a || r == q {
		t.Fatalf("went to %d (a %d, q %d)", r, a, q)
	}
	for _, script := range []string{"t", "v", "w"} {
		if got := fakeRun(t, pool, script, onRun("F")); got != r {
			t.Fatalf("script %s went to %d, want %d", script, got, r)
		}
	}
	if got := fakeRun(t, pool, "s", onRun("G")); got != r {
		t.Fatalf("the module retiree stopped serving while the slot is held: went to %d", got)
	}
	exitQ := fmt.Sprintf("exit %d", q)
	hq.release(t)
	if r := <-hq.done; pidOfOutcome(t, r.out, r.err) != q {
		t.Fatalf("the held invocation ran elsewhere: %+v", r.out)
	}
	log.waitFor(t, "unrelated done", exitQ)
	if !log.before(exitQ, 1, "spawn", 4) {
		t.Fatalf("the module retiree's replacement started before the slot freed: %q", log.after(""))
	}
	if _, most := log.stats(); most > 2 {
		t.Fatalf("%d processes alive at once with MaxLive 2: %q", most, log.after(""))
	}
}

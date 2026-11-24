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

// mustPin pins run in pool.
func mustPin(t *testing.T, pool *Pool, run string) *Pin {
	t.Helper()
	p, err := pool.PinRun(run)
	if err != nil {
		t.Fatal(err)
	}
	return p
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
	mustPin(t, pool, "A")
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
	mustPin(t, pool, "B")
	p := fakeRun(t, pool, "s", onRun("B"))
	// B's abandoned step taints p, which B's live pin keeps, idle.
	abandon(t, pool, ctl, "x", onRun("B"))
	mustPin(t, pool, "A")
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
	mustPin(t, pool, "C")
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
	pinA := mustPin(t, pool, "A")
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
	pool.RunEnded(pinA)
	log.waitFor(t, "run ended", fmt.Sprintf("drain %d", a))
}

// The surplus rule with a pin: a tainted process whose taint clears while
// its replacement fills Max is surplus. It takes no new unpinned
// invocation, keeps serving its pinned run, and leaves once the run has
// ended.
func TestPinAfterTaintClears(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1})
	pinA := mustPin(t, pool, "A")
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
	pool.RunEnded(pinA)
	log.waitFor(t, "run ended", fmt.Sprintf("drain %d", a))
}

// The cap still bounds a pinned run's process: with Cap 1 reached there by
// the run's own held invocation, its next invocation waits for that slot,
// while an unpinned one goes to another process.
func TestPinnedRunCapFull(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 2, Cap: 1})
	mustPin(t, pool, "A")
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
	pinA := mustPin(t, pool, "A")
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
	pool.RunEnded(pinA)
	if _, err := pool.HostCall(ctx, "onRunSettled", "A", "", jsonval.FromProducer([]byte(`{}`))); err != nil {
		t.Fatalf("a late host call of the ended run: %v", err)
	}
}

// A pinned run's host calls go to its process, also when that process is
// tainted. After the run's end, its late onRunSettled still reaches that
// process while it can answer: with the last live pin released, the
// process drains, its drain held, and the call goes there. An unpinned
// run's call goes to the replacement, and once the process has exited, so
// do the ended run's late calls.
func TestPinnedRunHostCalls(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1, RecycleTimeout: time.Minute}, "WL_FAKE_PLUGIN=same")
	holdDrains(t, ctl)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hostCall := func(hook, run string) int {
		t.Helper()
		out, err := pool.HostCall(ctx, hook, run, "", jsonval.FromProducer([]byte(`{}`)))
		var r struct{ PID int }
		if err != nil || json.Unmarshal(out.Producer(), &r) != nil {
			t.Fatalf("%s of %s: %s, %v", hook, run, out.Producer(), err)
		}
		return r.PID
	}
	pinA := mustPin(t, pool, "A")
	a := fakeRun(t, pool, "s", onRun("A"))
	abandon(t, pool, ctl, "x", onRun("A"))
	if got := hostCall("resource", "A"); got != a {
		t.Fatalf("the pinned run's host call went to %d, want its tainted process %d", got, a)
	}
	log.note("run ended")
	pool.RunEnded(pinA)
	drainA, exitA := fmt.Sprintf("drain %d", a), fmt.Sprintf("exit %d", a)
	log.waitFor(t, "run ended", drainA)
	if got := hostCall("onRunSettled", "A"); got != a {
		t.Fatalf("the ended run's onRunSettled went to %d, want its draining process %d", got, a)
	}
	if got := hostCall("resource", "C"); got == a {
		t.Fatalf("an unpinned run's host call went to the leaving process %d", a)
	}
	if slices.Contains(log.after("run ended"), exitA) {
		t.Fatalf("the process exited with its drain held: %q", log.after(""))
	}
	releaseDrain(t, ctl, a)
	log.waitFor(t, "run ended", exitA)
	if got := hostCall("onRunSettled", "A"); got == a {
		t.Fatalf("a late call went to the exited process %d", a)
	}
}

// Each admitted run owns its pin. A run that takes the id of an earlier
// run whose settlement is still to come is routed as its own: pinned, by
// its new pin from its first call, so not to the earlier run's tainted
// process; unpinned, as any run. The earlier run's late call, which
// carries its pin, still reaches its process, and its settlement forgets
// its own pin only.
func TestPinIDReuse(t *testing.T) {
	pool, ctl, _ := fakePool(t, PoolConfig{Max: 2, RecycleTimeout: time.Minute}, "WL_FAKE_PLUGIN=same")
	holdDrains(t, ctl)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hostCall := func(ctx context.Context, run string) int {
		t.Helper()
		out, err := pool.HostCall(ctx, "onRunSettled", run, "", jsonval.FromProducer([]byte(`{}`)))
		var r struct{ PID int }
		if err != nil || json.Unmarshal(out.Producer(), &r) != nil {
			t.Fatalf("a host call of %s: %s, %v", run, out.Producer(), err)
		}
		return r.PID
	}
	first := mustPin(t, pool, "A")
	p := fakeRun(t, pool, "s", onRun("A"))
	// Another run's abandoned step taints p, and the first run ends, not
	// yet settled: p drains, its drain held.
	abandon(t, pool, ctl, "x", onRun("X"))
	pool.RunEnded(first)
	second := mustPin(t, pool, "A")
	q := fakeRun(t, pool, "s", onRun("A"))
	if q == p {
		t.Fatalf("the second run with the id went to the first run's tainted process %d", p)
	}
	if got := hostCall(WithPin(ctx, first), "A"); got != p {
		t.Fatalf("the first run's late call went to %d, want its process %d", got, p)
	}
	if got := hostCall(ctx, "A"); got != q {
		t.Fatalf("the second run's host call went to %d, want its process %d", got, q)
	}
	// The first run's settlement leaves the second run's pin: once the
	// second run's own abandoned step taints q, its next step still goes
	// there.
	pool.ForgetRun(first)
	abandon(t, pool, ctl, "y", onRun("A"))
	if got := fakeRun(t, pool, "s", onRun("A")); got != q {
		t.Fatalf("the second run's step went to %d after the first run settled, want its tainted process %d", got, q)
	}
	pool.RunEnded(second)
	pool.ForgetRun(second)

	// An unpinned run that takes the id of an ended pinned run, its calls
	// carrying its choice: its host calls, which an ended pin would still
	// route, and its steps go as any run's, not to the earlier run's
	// leaving process.
	pool2, ctl2, _ := fakePool(t, PoolConfig{Max: 2, RecycleTimeout: time.Minute}, "WL_FAKE_PLUGIN=same")
	holdDrains(t, ctl2)
	pinB := mustPin(t, pool2, "B")
	b := fakeRun(t, pool2, "s", onRun("B"))
	abandon(t, pool2, ctl2, "x", onRun("X"))
	pool2.RunEnded(pinB)
	out, err := pool2.HostCall(WithPin(ctx, pool2.Unpinned("B")), "resource", "B", "", jsonval.FromProducer([]byte(`{}`)))
	var r struct{ PID int }
	if err != nil || json.Unmarshal(out.Producer(), &r) != nil || r.PID == b {
		t.Fatalf("an unpinned run's host call: %s, %v; want another process than the earlier pinned run's %d", out.Producer(), err, b)
	}
	if got := fakeRun(t, pool2, "s", onRun("B")); got == b {
		t.Fatalf("an unpinned run went to the tainted process %d of an earlier pinned run with its id", b)
	}
}

// A pin binds again when every call that it sent to its process was
// refused before dispatch, so no work or state of the run reached that
// process: here the run's first pick finds a process that no longer takes
// the call, which the pool has not seen yet. The call goes to another
// process, and so does the run's next step.
//   - A first step: the process was drained behind the pool's back, its
//     exit held, so it takes no invocation.
//   - A first host call: a draining process still answers host calls, so
//     the process is marked as ending, the state its stopped reader leaves
//     until its exit is observed, which the fake harness cannot hold.
func TestPinRebindsAfterARefusedFirstCall(t *testing.T) {
	for _, first := range []string{"step", "host call"} {
		t.Run(first, func(t *testing.T) {
			pool, ctl, _ := fakePool(t, PoolConfig{Max: 2}, "WL_FAKE_PLUGIN=same")
			holdDrains(t, ctl)
			p := fakeRun(t, pool, "s", onRun("U"))
			proc := memberProcess(t, pool, p)
			if first == "step" {
				go proc.Drain(context.Background()) //nolint:errcheck // its exit is held, and the pool kills it at Close
				waitFor(t, "the process to stop serving", func() bool {
					proc.mu.Lock()
					defer proc.mu.Unlock()
					return proc.state == stateDraining
				})
			} else {
				proc.mu.Lock()
				proc.ending = true
				proc.mu.Unlock()
			}
			mustPin(t, pool, "A")
			var q int
			if first == "step" {
				q = fakeRun(t, pool, "s", onRun("A"))
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				out, err := pool.HostCall(ctx, "resource", "A", "", jsonval.FromProducer([]byte(`{}`)))
				var r struct{ PID int }
				if err != nil || json.Unmarshal(out.Producer(), &r) != nil {
					t.Fatalf("%s, %v", out.Producer(), err)
				}
				q = r.PID
			}
			if q == p {
				t.Fatalf("the run's first call ran in the process %d that stopped serving", p)
			}
			if got := fakeRun(t, pool, "s", onRun("A")); got != q {
				t.Fatalf("the run's next step went to %d, want %d", got, q)
			}
		})
	}
}

// The live count with pins (Max 1, MaxLive 2, drains held). A pinned run's
// process is tainted, its replacement starts, and its taint clears while
// the replacement fills Max: it is surplus, still serving the pin, with
// unrelated work in flight. The pin ends; the replacement trips the RSS
// trigger with work of its own in flight, so two leavers fill MaxLive and
// a new invocation waits. The surplus process drains only once the
// unrelated work, held past RecycleTimeout, has completed, and frees its
// place only at its exit: the new invocation runs in a third process,
// which starts only after that exit. That process trips the module
// trigger and keeps serving while the RSS leaver holds the slot; its
// replacement starts once that leaver has exited. Spawns minus observed
// exits never pass 2. A drain ends at its release or, after
// RecycleTimeout, its kill: the order of the pool's events, not the
// moment of a check, shows each step.
func TestPoolAliveCount(t *testing.T) {
	const recycle = 50 * time.Millisecond
	// The first process runs three scripts (s, held, abandoned), which
	// Modules 3 allows.
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1, MaxLive: 2, RecycleTimeout: recycle, Recycle: RecycleTriggers{RSS: 1000, Modules: 3}})
	holdDrains(t, ctl)
	pinA := mustPin(t, pool, "A")
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
	pool.RunEnded(pinA)
	unrelatedSince := time.Now()
	// The replacement trips the RSS trigger with an invocation held in it.
	hq := startHeld(t, pool, ctl, "hq", onRun("H"))
	for range 2 {
		if got := fakeRun(t, pool, "s", onRun("D", map[string]any{"rss": 2000})); got != q {
			t.Fatalf("a report went to %d, want %d", got, q)
		}
	}
	// Demand while two leavers fill MaxLive: no process can start.
	log.note("demand")
	w := goRun(pool, "s", onRun("W"))
	log.waitFor(t, "demand", "wait")
	notReturned(t, w, "an invocation while two leavers fill MaxLive")
	// The unrelated work outlives RecycleTimeout before it completes.
	time.Sleep(time.Until(unrelatedSince.Add(2 * recycle)))
	drainA, exitA := fmt.Sprintf("drain %d", a), fmt.Sprintf("exit %d", a)
	if slices.Contains(log.after("pin ended"), drainA) {
		t.Fatalf("the process drained with unrelated work in flight: %q", log.after(""))
	}
	log.note("unrelated done")
	u.release(t)
	if r := <-u.done; pidOfOutcome(t, r.out, r.err) != a {
		t.Fatalf("the unrelated work ran elsewhere: %+v", r.out)
	}
	log.waitFor(t, "unrelated done", drainA)
	if !log.before("note: unrelated done", 1, drainA, 1) {
		t.Fatalf("the drain began before the unrelated work completed: %q", log.after(""))
	}
	releaseDrain(t, ctl, a)
	r := wait(t, w)
	if r == a || r == q {
		t.Fatalf("the waiting invocation went to %d (a %d, q %d)", r, a, q)
	}
	if !log.before(exitA, 1, "spawn", 3) {
		t.Fatalf("the third process started before the surplus one exited: %q", log.after(""))
	}
	// The third process trips the module trigger while the RSS leaver
	// still holds the slot: it keeps serving.
	for _, script := range []string{"t", "v", "w"} {
		if got := fakeRun(t, pool, script, onRun("F")); got != r {
			t.Fatalf("script %s went to %d, want %d", script, got, r)
		}
	}
	if got := fakeRun(t, pool, "s", onRun("G")); got != r {
		t.Fatalf("the module retiree stopped serving while the slot is held: went to %d", got)
	}
	log.note("q released")
	hq.release(t)
	if res := <-hq.done; pidOfOutcome(t, res.out, res.err) != q {
		t.Fatalf("the held invocation ran elsewhere: %+v", res.out)
	}
	drainQ, exitQ := fmt.Sprintf("drain %d", q), fmt.Sprintf("exit %d", q)
	log.waitFor(t, "q released", drainQ)
	releaseDrain(t, ctl, q)
	log.waitFor(t, "q released", exitQ)
	waitFor(t, "the module retiree's replacement", func() bool { return log.count("spawn") >= 4 })
	if !log.before(exitQ, 1, "spawn", 4) {
		t.Fatalf("the module retiree's replacement started before the slot freed: %q", log.after(""))
	}
	if _, most := log.stats(); most > 2 {
		t.Fatalf("%d processes alive at once with MaxLive 2: %q", most, log.after(""))
	}
}

// pidOfHostCall asks pool's plugin for its PID, with ctx, as run.
func pidOfHostCall(t *testing.T, ctx context.Context, pool *Pool, run string) int {
	t.Helper()
	out, err := pool.HostCall(ctx, "resource", run, "", jsonval.FromProducer([]byte(`{}`)))
	var r struct{ PID int }
	if err != nil || json.Unmarshal(out.Producer(), &r) != nil {
		t.Fatalf("a host call of %s: %s, %v", run, out.Producer(), err)
	}
	return r.PID
}

// An unpinned run's calls carry its routing choice (Unpinned): its late
// call, after a pinned run took its id, neither binds that run's pin nor
// goes by it.
func TestUnpinnedRunLateCalls(t *testing.T) {
	pool, ctl, _ := fakePool(t, PoolConfig{Max: 3, RecycleTimeout: time.Minute}, "WL_FAKE_PLUGIN=same")
	holdDrains(t, ctl)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	earlier := WithPin(ctx, pool.Unpinned("C"))
	p := fakeRun(t, pool, "s", onRun("U"))
	later := mustPin(t, pool, "C")
	// Before the later run's first call: the earlier run's late call does
	// not bind the later run's pin to p, which an abandoned step then
	// taints.
	if got := pidOfHostCall(t, earlier, pool, "C"); got != p {
		t.Fatalf("the earlier run's late call went to %d, want the serving %d", got, p)
	}
	abandon(t, pool, ctl, "x", onRun("X"))
	q := fakeRun(t, pool, "s", onRun("C"))
	if q == p {
		t.Fatalf("the later run's first step went to the tainted %d: the earlier run's late call bound its pin", p)
	}
	// After it: the later run's process q is tainted by its own abandoned
	// step, which its live pin keeps; the earlier run's late call goes as
	// any call does, so not to q.
	abandon(t, pool, ctl, "y", onRun("C"))
	if got := pidOfHostCall(t, earlier, pool, "C"); got == q {
		t.Fatalf("the earlier run's late call went by the later run's pin to its tainted process %d", q)
	}
	pool.RunEnded(later)
}

// A pool routes only by its own pins: a call to one pool whose context
// carries another pool's pin of the same run id goes as that pool's own
// routing says.
func TestPinOfAnotherPool(t *testing.T) {
	poolA, _, _ := fakePool(t, PoolConfig{Max: 1}, "WL_FAKE_PLUGIN=same")
	poolB, _, _ := fakePool(t, PoolConfig{Max: 1}, "WL_FAKE_PLUGIN=same")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pa := mustPin(t, poolA, "D")
	a := fakeRun(t, poolA, "s", onRun("D"))
	b := fakeRun(t, poolB, "s", onRun("U"))
	if got := pidOfHostCall(t, WithPin(ctx, pa), poolB, "D"); got != b || got == a {
		t.Fatalf("pool B's call with pool A's pin went to %d; pool B's process is %d, pool A's %d", got, b, a)
	}
	poolA.RunEnded(pa)
}

// One id has one live pin: PinRun refuses an id whose pin a live run
// holds, and takes it again once that run has ended.
func TestPinRunRefusesALivePin(t *testing.T) {
	pool, _, _ := fakePool(t, PoolConfig{Max: 1})
	first := mustPin(t, pool, "A")
	if _, err := pool.PinRun("A"); !errors.Is(err, ErrPinLive) {
		t.Fatalf("a second pin of a live run's id: %v; want ErrPinLive", err)
	}
	pool.RunEnded(first)
	if _, err := pool.PinRun("A"); err != nil {
		t.Fatalf("a pin of an ended run's id: %v", err)
	}
}

// A pin binds afresh after its first call ended before it was sent, by
// its own context's end: nothing of the run reached the process. So the
// run's next step, once that process is tainted, goes elsewhere.
func TestPinRebindsAfterACallThatWasNotSent(t *testing.T) {
	pool, ctl, _ := fakePool(t, PoolConfig{Max: 2, RecycleTimeout: time.Minute})
	holdDrains(t, ctl)
	p := fakeRun(t, pool, "s", onRun("U"))
	mustPin(t, pool, "A")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.Execute(ctx, fakeInvocation("s", onRun("A"))); err == nil {
		t.Fatal("an invocation with an ended context ran")
	}
	abandon(t, pool, ctl, "x", onRun("X"))
	if got := fakeRun(t, pool, "s", onRun("A")); got == p {
		t.Fatalf("the run's step went to the tainted %d, which its unsent first call had bound", p)
	}
}

// Concurrent first calls across an observed exit: while the first call of
// a pinned run, taken but not yet sent, is under way on a process whose
// exit the pool then observes, another call of the run waits for it
// rather than report the process lost. Once the first reports that it was
// not sent, the pin binds afresh and the waiting call runs elsewhere.
// (The test takes and releases the first call itself, to hold it between
// the pick and the send.)
func TestPinFirstCallsAcrossAnExit(t *testing.T) {
	pool, _, log := fakePool(t, PoolConfig{Max: 2})
	p := fakeRun(t, pool, "s", onRun("U"))
	mustPin(t, pool, "A")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, pn, err := pool.acquire(ctx, "A", true)
	if err != nil || m.p.PID() != p {
		t.Fatalf("the first call took %v, %v; want %d", m, err, p)
	}
	memberProcess(t, pool, p).Kill()
	log.waitFor(t, "", fmt.Sprintf("exit %d", p))
	log.note("second")
	w := goRun(pool, "s", onRun("A"))
	log.waitFor(t, "second", "wait")
	notReturned(t, w, "the second call while the first is under way")
	pool.release(m, pn, false, true, true, "A")
	if got := wait(t, w); got == p {
		t.Fatalf("the second call ran in the exited %d", p)
	}
}

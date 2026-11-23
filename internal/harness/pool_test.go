package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// poolFake is the fake harness of mode pool, which the tests of a warm
// pool's settings drive. It runs each invocation on a goroutine of its own.
// An invocation's arguments may name a file to create once it starts
// (started), a file to wait for before it answers (hold), and a resident
// set to report in a heartbeat ping just before its result (rss); its
// result is {"pid": <the pid of its process>}. It ignores cancels, so an
// abandoned invocation runs on until its hold file exists. It sends no
// heartbeat of its own. It answers a drain with bye and exits: at once, or,
// while the file hold-drains exists in the directory WL_FAKE_CTL, once
// release-<pid> exists there too.
func poolFake(send func(Message), recv func() Message, pid int) int {
	ctl := os.Getenv("WL_FAKE_CTL")
	exists := func(f string) bool { _, err := os.Stat(f); return err == nil }
	var beats atomic.Int64
	for {
		switch m := recv().(type) {
		case Invoke:
			go func() {
				var a struct {
					Started, Hold string
					RSS           *int64
				}
				json.Unmarshal(m.Args.Producer(), &a)
				if a.Started != "" {
					os.WriteFile(a.Started, nil, 0o600)
				}
				for a.Hold != "" && !exists(a.Hold) {
					time.Sleep(2 * time.Millisecond)
				}
				if a.RSS != nil {
					send(Ping{ID: fmt.Sprintf("h%d", beats.Add(1)), Health: &Health{RSS: *a.RSS}})
				}
				send(Result{ID: m.ID, Outcome: OutcomeOK, Body: jsonval.FromProducer(fmt.Appendf(nil, `{"pid":%d}`, pid)), Settled: true})
			}()
		case Drain:
			go func() {
				for ctl != "" && exists(filepath.Join(ctl, "hold-drains")) && !exists(filepath.Join(ctl, fmt.Sprintf("release-%d", pid))) {
					time.Sleep(2 * time.Millisecond)
				}
				send(Bye{})
				os.Exit(0)
			}()
		}
	}
}

// poolLog records a pool's events (PoolConfig.observe) and the test's own
// notes in one order, and counts the processes alive by those events: each
// spawn that the pool begins counts until it observes the exit of a
// process. maxLive is the most that were alive at once.
type poolLog struct {
	mu      sync.Mutex
	events  []string
	live    int
	maxLive int
	changed chan struct{}
}

func newPoolLog() *poolLog { return &poolLog{changed: make(chan struct{})} }

func (l *poolLog) observe(ev poolEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch ev.kind {
	case "spawn":
		l.live++
		l.maxLive = max(l.maxLive, l.live)
		l.addLocked("spawn")
	case "exit", "drain":
		if ev.kind == "exit" {
			l.live--
		}
		l.addLocked(fmt.Sprintf("%s %d", ev.kind, ev.p.PID()))
	default:
		l.addLocked(ev.kind)
	}
}

// note records a step of the test's own.
func (l *poolLog) note(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addLocked("note: " + s)
}

func (l *poolLog) addLocked(s string) {
	l.events = append(l.events, s)
	close(l.changed)
	l.changed = make(chan struct{})
}

// after returns the events after the note mark, all of them when mark is "".
func (l *poolLog) after(mark string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if mark == "" {
		return slices.Clone(l.events)
	}
	i := slices.Index(l.events, "note: "+mark)
	if i < 0 {
		return nil
	}
	return slices.Clone(l.events[i+1:])
}

func (l *poolLog) count(event string) int {
	n := 0
	for _, e := range l.after("") {
		if e == event {
			n++
		}
	}
	return n
}

func (l *poolLog) stats() (live, maxLive int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live, l.maxLive
}

// waitFor waits until event is among the events after the note mark. It
// fails the test after 10 s, which only a pool that never gets there takes.
func (l *poolLog) waitFor(t *testing.T, mark, event string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		l.mu.Lock()
		ch := l.changed
		l.mu.Unlock()
		if slices.Contains(l.after(mark), event) {
			return
		}
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("no %q after %q; events: %q", event, mark, l.after(""))
		}
	}
}

// before reports whether event a comes before event b (each the n-th of its
// text, from 1), both present.
func (l *poolLog) before(a string, na int, b string, nb int) bool {
	evs := l.after("")
	nth := func(s string, n int) int {
		for i, e := range evs {
			if e == s {
				if n--; n == 0 {
					return i
				}
			}
		}
		return -1
	}
	i, j := nth(a, na), nth(b, nb)
	return i >= 0 && j >= 0 && i < j
}

// fakePool returns a warm pool of fake harnesses in mode pool, the pool's
// control directory and its event log. The fake sends no heartbeat of its
// own, so the watchdog and lag routing are off.
func fakePool(t *testing.T, cfg PoolConfig) (*Pool, string, *poolLog) {
	t.Helper()
	pc, _ := fakeConfig(t, "pool")
	// Made before the pool, so it outlives the pool's close.
	ctl := t.TempDir()
	pc.Env = append(pc.Env, "WL_FAKE_CTL="+ctl)
	pc.KillAfter = WatchdogOff
	cfg.Process = pc
	cfg.LagThreshold = -1
	log := newPoolLog()
	cfg.observe = log.observe
	pool := newPool(t, cfg)
	// Cleanups run last in, first out: held drains end before the pool
	// closes.
	t.Cleanup(func() { os.Remove(filepath.Join(ctl, "hold-drains")) })
	return pool, ctl, log
}

func holdDrains(t *testing.T, ctl string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ctl, "hold-drains"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func releaseDrain(t *testing.T, ctl string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ctl, fmt.Sprintf("release-%d", pid)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeInvocation is an invocation of script for the fake of mode pool.
func fakeInvocation(script string, args map[string]any) engine.Invocation {
	b, _ := json.Marshal(args)
	if args == nil {
		b = []byte(`{}`)
	}
	inv := newInvocation(script, string(b), newRecorder())
	return inv
}

// pidOfOutcome returns the pid in a fake invocation's result.
func pidOfOutcome(t *testing.T, out engine.StepOutcome, err error) int {
	t.Helper()
	var r struct{ PID int }
	if err != nil || out.Kind != engine.OutcomeOK || json.Unmarshal(out.Body.Producer(), &r) != nil || r.PID == 0 {
		t.Fatalf("fake invocation: %+v, %v", out, err)
	}
	return r.PID
}

// fakeRun runs an invocation of script with args on the pool and returns
// the pid of the process it ran in.
func fakeRun(t *testing.T, pool *Pool, script string, args map[string]any) int {
	t.Helper()
	out, err := execute(t, pool, fakeInvocation(script, args), 10*time.Second)
	return pidOfOutcome(t, out, err)
}

type runResult struct {
	out engine.StepOutcome
	err error
}

// goRun starts an invocation of script with args, and returns where its
// result comes.
func goRun(pool *Pool, script string, args map[string]any) <-chan runResult {
	done := make(chan runResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := pool.Execute(ctx, fakeInvocation(script, args))
		done <- runResult{out, err}
	}()
	return done
}

// wait waits for a result of goRun and returns its pid.
func wait(t *testing.T, done <-chan runResult) int {
	t.Helper()
	select {
	case r := <-done:
		return pidOfOutcome(t, r.out, r.err)
	case <-time.After(30 * time.Second):
		t.Fatal("no result")
		return 0
	}
}

// heldInvocation is a fake invocation that has started, and that answers
// once the test releases it.
type heldInvocation struct {
	hold string
	done <-chan runResult
}

// startHeld starts a held invocation and waits until it runs in a process.
func startHeld(t *testing.T, pool *Pool, ctl, name string) heldInvocation {
	t.Helper()
	started, hold := filepath.Join(ctl, name+".started"), filepath.Join(ctl, name+".hold")
	t.Cleanup(func() { os.WriteFile(hold, nil, 0o600) })
	done := goRun(pool, "held", map[string]any{"started": started, "hold": hold})
	waitFile(t, started)
	return heldInvocation{hold: hold, done: done}
}

func (h heldInvocation) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(h.hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// abandon runs a fake invocation that never answers until the test ends,
// and cancels it once it runs: the abandonment taints its process. The
// pool's rules for the taint have run when abandon returns.
func abandon(t *testing.T, pool *Pool, ctl, name string) {
	t.Helper()
	started, hold := filepath.Join(ctl, name+".started"), filepath.Join(ctl, name+".hold")
	t.Cleanup(func() { os.WriteFile(hold, nil, 0o600) })
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				cancel(engine.CancelTimeout)
				return
			}
		}
		cancel(errors.New("the invocation to abandon did not start"))
	}()
	if _, err := pool.Execute(ctx, fakeInvocation("abandoned", map[string]any{"started": started, "hold": hold})); !errors.Is(err, engine.CancelTimeout) {
		t.Fatalf("abandoned invocation: %v", err)
	}
}

func waitFile(t *testing.T, f string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		if _, err := os.Stat(f); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s does not exist", f)
		}
	}
}

func notReturned(t *testing.T, done <-chan runResult, what string) {
	t.Helper()
	select {
	case r := <-done:
		t.Fatalf("%s returned %+v, %v", what, r.out, r.err)
	default:
	}
}

// With Max 1 and MaxLive 2, a tainted process whose drain the test holds
// and its replacement fill the bound: a second taint starts no third
// process, and a new invocation waits until the first leaver has exited.
// The live count, from the spawns that the pool begins and the exits that
// it observes, never passes 2.
func TestPoolMaxLive(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1, MaxLive: 2, RecycleTimeout: time.Minute})
	holdDrains(t, ctl)
	a := fakeRun(t, pool, "s", nil)
	abandon(t, pool, ctl, "x1")
	log.waitFor(t, "", fmt.Sprintf("drain %d", a))
	b := fakeRun(t, pool, "s", nil)
	if b == a {
		t.Fatalf("the tainted process %d got a new invocation", a)
	}
	abandon(t, pool, ctl, "x2")
	log.waitFor(t, "", fmt.Sprintf("drain %d", b))
	// The pool applied the taint before the abandoned Execute returned, so
	// a third start would have begun by now.
	if n := log.count("spawn"); n != 2 {
		t.Fatalf("%d spawns with two processes alive and MaxLive 2; events: %q", n, log.after(""))
	}
	log.note("submit")
	w := goRun(pool, "s", nil)
	log.waitFor(t, "submit", "wait")
	if n := log.count("spawn"); n != 2 {
		t.Fatalf("%d spawns; events: %q", n, log.after(""))
	}
	notReturned(t, w, "the invocation that should wait")
	releaseDrain(t, ctl, a)
	c := wait(t, w)
	if c == a || c == b {
		t.Fatalf("the waiting invocation ran in %d (the leavers are %d and %d)", c, a, b)
	}
	if !log.before(fmt.Sprintf("exit %d", a), 1, "spawn", 3) {
		t.Fatalf("the third process started before the first leaver exited; events: %q", log.after(""))
	}
	if _, most := log.stats(); most > 2 {
		t.Fatalf("%d processes alive at once with MaxLive 2; events: %q", most, log.after(""))
	}
}

// With Min = Max = MaxLive = 1 and a started pool, the first invocation
// goes at once to the serving process: MaxLive gates only starts, never
// routing. Only after a taint does a new invocation wait, for the leaver's
// exit.
func TestPoolMaxLiveRouting(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Min: 1, Max: 1, MaxLive: 1, RecycleTimeout: time.Minute})
	holdDrains(t, ctl)
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := log.count("spawn"); n != 1 || len(pool.Stats()) != 1 {
		t.Fatalf("after Start: %d spawns, %+v", n, pool.Stats())
	}
	log.note("first run")
	a := fakeRun(t, pool, "s", nil)
	if evs := log.after("first run"); len(evs) != 0 {
		t.Fatalf("the first invocation on the started process did not go at once: %q", evs)
	}
	abandon(t, pool, ctl, "x")
	log.waitFor(t, "", fmt.Sprintf("drain %d", a))
	log.note("submit")
	w := goRun(pool, "s", nil)
	log.waitFor(t, "submit", "wait")
	if n := log.count("spawn"); n != 1 {
		t.Fatalf("%d spawns while the leaver is alive; events: %q", n, log.after(""))
	}
	notReturned(t, w, "the invocation that should wait")
	releaseDrain(t, ctl, a)
	if b := wait(t, w); b == a {
		t.Fatalf("the waiting invocation ran in the leaver %d", a)
	}
	if !log.before(fmt.Sprintf("exit %d", a), 1, "spawn", 2) {
		t.Fatalf("the replacement started before the leaver exited; events: %q", log.after(""))
	}
}

// A leaving process waits for the work in flight in it. With Max = MaxLive
// = 1, an abandoned invocation taints the process while another invocation
// in it is held, past RecycleTimeout. The replacement cannot start, so a new
// invocation waits. The held invocation completes, not killed; only then
// does the drain start, and the replacement starts only after the leaver's
// exit is observed.
func TestPoolLeavingWaitsForWork(t *testing.T) {
	const recycle = time.Millisecond
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1, MaxLive: 1, RecycleTimeout: recycle})
	a := fakeRun(t, pool, "s", nil)
	h := startHeld(t, pool, ctl, "h")
	heldSince := time.Now()
	abandon(t, pool, ctl, "x")
	if n, m := log.count("spawn"), len(log.after("")); n != 1 || slices.ContainsFunc(log.after(""), func(e string) bool { return strings.HasPrefix(e, "drain") }) {
		t.Fatalf("a tainted process with work in flight drained or made room: %d spawns, %d events: %q", n, m, log.after(""))
	}
	log.note("submit")
	w := goRun(pool, "s", nil)
	log.waitFor(t, "submit", "wait")
	if n := log.count("spawn"); n != 1 {
		t.Fatalf("%d spawns while the leaver is alive; events: %q", n, log.after(""))
	}
	notReturned(t, w, "the invocation that should wait")
	if held := time.Since(heldSince); held <= recycle {
		t.Fatalf("held for %s, not past RecycleTimeout", held)
	}
	log.note("release h")
	h.release(t)
	if r := <-h.done; pidOfOutcome(t, r.out, r.err) != a {
		t.Fatalf("the held invocation ran elsewhere: %+v", r.out)
	}
	if b := wait(t, w); b == a {
		t.Fatalf("the waiting invocation ran in the leaver %d", a)
	}
	drain, exit := fmt.Sprintf("drain %d", a), fmt.Sprintf("exit %d", a)
	if !log.before("note: release h", 1, drain, 1) || !log.before(drain, 1, exit, 1) || !log.before(exit, 1, "spawn", 2) {
		t.Fatalf("want the held invocation's release, the drain, the exit, then the replacement; events: %q", log.after(""))
	}
}

// The RSS trigger: two health reports in a row at or over the threshold
// (Samples defaults to 2) trip a process. It takes no new invocation, its
// invocation in flight completes, and the next goes to a replacement that
// starts beside it. A second process that trips while the first leaves
// keeps serving until the first has exited.
func TestPoolRecycleOnRSS(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1, Recycle: RecycleTriggers{RSS: 1000}})
	over, under := map[string]any{"rss": 2000}, map[string]any{"rss": 10}
	a := fakeRun(t, pool, "s", nil)
	h := startHeld(t, pool, ctl, "h")
	// One report over, one under: the run of reports starts again.
	for i, args := range []map[string]any{over, under, over, nil} {
		if got := fakeRun(t, pool, "s", args); got != a {
			t.Fatalf("invocation %d went to %d before the trigger tripped", i, got)
		}
	}
	// The second report over in a row; this invocation still ran there.
	if got := fakeRun(t, pool, "s", over); got != a {
		t.Fatalf("went to %d", got)
	}
	b := fakeRun(t, pool, "s", nil)
	if b == a {
		t.Fatalf("the tripped process %d got a new invocation", a)
	}
	if slices.Contains(log.after(""), fmt.Sprintf("drain %d", a)) {
		t.Fatalf("the tripped process drained with an invocation in flight; events: %q", log.after(""))
	}
	// b trips while a leaves, and keeps serving.
	for range 2 {
		if got := fakeRun(t, pool, "s", over); got != b {
			t.Fatalf("went to %d, want %d", got, b)
		}
	}
	if got := fakeRun(t, pool, "s", nil); got != b {
		t.Fatalf("the second tripped process stopped serving while the first left: went to %d", got)
	}
	log.note("release h")
	h.release(t)
	if r := <-h.done; pidOfOutcome(t, r.out, r.err) != a {
		t.Fatalf("the invocation in flight: %+v, %v", r.out, r.err)
	}
	exitA := fmt.Sprintf("exit %d", a)
	log.waitFor(t, "release h", exitA)
	// Now b leaves for its trigger.
	if c := fakeRun(t, pool, "s", nil); c == a || c == b {
		t.Fatalf("went to %d once the first had left (a %d, b %d)", c, a, b)
	}
	if !log.before("note: release h", 1, fmt.Sprintf("drain %d", a), 1) || !log.before(exitA, 1, fmt.Sprintf("drain %d", b), 1) {
		t.Fatalf("events: %q", log.after(""))
	}
}

// The module trigger counts the distinct scripts that the engine sent a
// process. With Modules 2, a repeat does not count, the invocation of a
// third script still runs there, and the next goes to a replacement.
func TestPoolRecycleOnModules(t *testing.T) {
	pool, _, log := fakePool(t, PoolConfig{Max: 1, Recycle: RecycleTriggers{Modules: 2}})
	a := fakeRun(t, pool, "s1", nil)
	for _, script := range []string{"s2", "s1", "s3"} {
		if got := fakeRun(t, pool, script, nil); got != a {
			t.Fatalf("%s went to %d", script, got)
		}
	}
	if b := fakeRun(t, pool, "s1", nil); b == a {
		t.Fatalf("a process that ran three scripts with Modules 2 got a new invocation")
	}
	log.waitFor(t, "", fmt.Sprintf("drain %d", a))
}

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
// (started), a file to wait for before it answers (hold), a number of
// lines to write to stderr once the hold ends (print), a resident set to
// report in a heartbeat ping just before its result (rss), and the
// resident set that the process's pongs report from then on (pong_rss);
// its result is {"pid": <the pid of its process>}, and so is its answer to
// a host_call. It ignores cancels, so an abandoned invocation runs on until
// its hold file exists. It sends no heartbeat of its own. It answers a drain
// with bye and exits: at once, or, while the file hold-drains exists in the
// directory WL_FAKE_CTL, once release-<pid> exists there too. The first
// process that finds the file exit-after-welcome there takes it and exits
// at once.
func poolFake(send func(Message), recv func() Message, pid int) int {
	ctl := os.Getenv("WL_FAKE_CTL")
	exists := func(f string) bool { _, err := os.Stat(f); return err == nil }
	if ctl != "" && os.Rename(filepath.Join(ctl, "exit-after-welcome"), filepath.Join(ctl, "exit-after-welcome.taken")) == nil {
		return 0
	}
	var beats, pongRSS atomic.Int64
	for {
		switch m := recv().(type) {
		case Ping:
			send(Pong{ID: m.ID, Health: &Health{RSS: pongRSS.Load()}})
		case HostCall:
			send(HostReply{ID: m.ID, Payload: jsonval.FromProducer(fmt.Appendf(nil, `{"pid":%d}`, pid))})
		case Invoke:
			go func() {
				var a struct {
					Started, Hold string
					Print         int
					RSS           *int64
					PongRSS       *int64 `json:"pong_rss"`
				}
				json.Unmarshal(m.Args.Producer(), &a)
				if a.PongRSS != nil {
					pongRSS.Store(*a.PongRSS)
				}
				if a.Started != "" {
					os.WriteFile(a.Started, nil, 0o600)
				}
				for a.Hold != "" && !exists(a.Hold) {
					time.Sleep(2 * time.Millisecond)
				}
				for i := range a.Print {
					fmt.Fprintf(os.Stderr, "line %d\n", i)
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
// spawn that the pool begins counts until it fails or the pool observes the
// exit of its process. maxLive is the most that were alive at once.
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
	case "failed":
		l.live--
		l.addLocked("failed")
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

// fakePool returns a warm pool of fake harnesses in mode pool, with env
// added to their environment, the pool's control directory and its event
// log. The fake sends no heartbeat of its own, so the watchdog and lag
// routing are off.
func fakePool(t *testing.T, cfg PoolConfig, env ...string) (*Pool, string, *poolLog) {
	t.Helper()
	pc, _ := fakeConfig(t, "pool")
	// Made before the pool, so it outlives the pool's close.
	ctl := t.TempDir()
	pc.Env = append(append(pc.Env, "WL_FAKE_CTL="+ctl), env...)
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

// startHeld starts a held invocation, with extra arguments if any, and
// waits until it runs in a process.
func startHeld(t *testing.T, pool *Pool, ctl, name string, extra ...map[string]any) heldInvocation {
	t.Helper()
	started, hold := filepath.Join(ctl, name+".started"), filepath.Join(ctl, name+".hold")
	t.Cleanup(func() { os.WriteFile(hold, nil, 0o600) })
	args := map[string]any{"started": started, "hold": hold}
	for _, e := range extra {
		maps.Copy(args, e)
	}
	done := goRun(pool, "held", args)
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
	const recycle = 50 * time.Millisecond
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1, MaxLive: 1, RecycleTimeout: recycle})
	a := fakeRun(t, pool, "s", nil)
	h := startHeld(t, pool, ctl, "h")
	abandon(t, pool, ctl, "x")
	tainted := time.Now()
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
	// The held invocation stays held past RecycleTimeout from the taint, so
	// a drain timed from the taint would have killed the process by now.
	time.Sleep(time.Until(tainted.Add(2 * recycle)))
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
// (Samples defaults to 2) trip a process, whether heartbeat pings or pongs
// carry them. It takes no new invocation, its
// invocation in flight completes, and the next goes to a replacement that
// starts beside it. A second process that trips while the first leaves
// keeps serving until the first has exited.
func TestPoolRecycleOnRSS(t *testing.T) {
	t.Run("heartbeats", testRecycleOnHeartbeats)
	t.Run("pongs", testRecycleOnPongs)
}

// testRecycleOnPongs: the pongs that answer the engine's pings count as
// health reports too, two in a row over the threshold tripping a process.
func testRecycleOnPongs(t *testing.T) {
	pool, _, _ := fakePool(t, PoolConfig{Max: 1, Recycle: RecycleTriggers{RSS: 1000}})
	a := fakeRun(t, pool, "s", map[string]any{"pong_rss": 2000})
	p := memberProcess(t, pool, a)
	ping := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if h, err := p.Ping(ctx); err != nil || h == nil || h.RSS != 2000 {
			t.Fatalf("ping: %+v, %v", h, err)
		}
	}
	ping()
	if got := fakeRun(t, pool, "s", nil); got != a {
		t.Fatalf("one pong over the threshold moved the next invocation to %d", got)
	}
	ping()
	if got := fakeRun(t, pool, "s", nil); got == a {
		t.Fatalf("the process whose two pongs in a row reached the threshold got a new invocation")
	}
}

// memberProcess returns the pool's member whose pid is pid.
func memberProcess(t *testing.T, pool *Pool, pid int) *Process {
	t.Helper()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for _, m := range pool.members {
		if m.p.PID() == pid {
			return m.p
		}
	}
	t.Fatalf("no member %d", pid)
	return nil
}

func testRecycleOnHeartbeats(t *testing.T) {
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

// A process that trips in a pool grown past Min gets its replacement at
// once, beside it, with no further invocation to ask for one. With Min 1,
// Max 2 and Cap 2, three held invocations grow the pool to two processes;
// the first trips while one of them is still in flight there.
func TestPoolRecycleReplacesAboveMin(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Min: 1, Max: 2, Cap: 2, Recycle: RecycleTriggers{RSS: 1000}})
	h1 := startHeld(t, pool, ctl, "h1")
	h2 := startHeld(t, pool, ctl, "h2")
	h3 := startHeld(t, pool, ctl, "h3")
	if n := len(pool.Stats()); n != 2 || log.count("spawn") != 2 {
		t.Fatalf("the pool did not grow to two processes: %+v, events %q", pool.Stats(), log.after(""))
	}
	h2.release(t)
	r2 := <-h2.done
	a := pidOfOutcome(t, r2.out, r2.err)
	// Both reports go to a, which has one invocation in flight (h1) beside
	// the other process's one (h3), and comes first among equals.
	for range 2 {
		if got := fakeRun(t, pool, "s", map[string]any{"rss": 2000}); got != a {
			t.Fatalf("a report went to %d, want %d", got, a)
		}
	}
	if n := log.count("spawn"); n != 3 {
		t.Fatalf("%d spawns after the trip in a pool above Min; events: %q", n, log.after(""))
	}
	if slices.Contains(log.after(""), fmt.Sprintf("drain %d", a)) {
		t.Fatalf("the tripped process drained with an invocation in flight: %q", log.after(""))
	}
	h1.release(t)
	h3.release(t)
	for _, h := range []heldInvocation{h1, h3} {
		r := <-h.done
		pidOfOutcome(t, r.out, r.err)
	}
	log.waitFor(t, "", fmt.Sprintf("drain %d", a))
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

// The module trigger counts only the scripts dispatched to a process: an
// invocation of a new script that the pool picked a process for, but that
// was never sent, because its context had ended, does not count.
func TestPoolModulesCountDispatchedScripts(t *testing.T) {
	pool, _, _ := fakePool(t, PoolConfig{Max: 1, Recycle: RecycleTriggers{Modules: 1}})
	a := fakeRun(t, pool, "s1", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.Execute(ctx, fakeInvocation("s2", nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("an invocation whose context had ended: %v", err)
	}
	if got := fakeRun(t, pool, "s1", nil); got != a {
		t.Fatalf("an invocation that was never sent counted toward Modules: went to %d, want %d", got, a)
	}
	// The second script dispatched still runs there; then the process trips.
	if got := fakeRun(t, pool, "s2", nil); got != a {
		t.Fatalf("went to %d, want %d", got, a)
	}
	if got := fakeRun(t, pool, "s1", nil); got == a {
		t.Fatal("a process that ran two scripts with Modules 1 got a new invocation")
	}
}

// A host call does not count toward the cap: with Cap 1 and the one
// process's step slot taken, a step's own call to the host plugin, which
// the step waits for, still goes to that process at once. The cap still
// bounds invocations.
func TestPoolCapLeavesHostCalls(t *testing.T) {
	pool, ctl, log := fakePool(t, PoolConfig{Max: 1, Cap: 1}, "WL_FAKE_PLUGIN=same")
	h := startHeld(t, pool, ctl, "h")
	log.note("host call")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := pool.HostCall(ctx, "resource", "run-h", "", jsonval.FromProducer([]byte(`{"path":"u/t/r"}`)))
	if err != nil {
		t.Fatalf("a host call beside a step at the cap: %v", err)
	}
	var r struct{ PID int }
	if json.Unmarshal(out.Producer(), &r) != nil || r.PID == 0 {
		t.Fatalf("reply %s", out.Producer())
	}
	if evs := log.after("host call"); slices.Contains(evs, "wait") {
		t.Fatalf("the host call waited for the cap: %q", evs)
	}
	log.note("second step")
	w := goRun(pool, "s", nil)
	log.waitFor(t, "second step", "wait")
	notReturned(t, w, "a second invocation under Cap 1")
	h.release(t)
	if r := <-h.done; pidOfOutcome(t, r.out, r.err) == 0 {
		t.Fatal("the held step failed")
	}
	if got := wait(t, w); got != r.PID {
		t.Fatalf("the second invocation went to %d, want %d", got, r.PID)
	}
}

// A process whose exit comes before the pool admits it is not admitted: it
// would hold a place in the pool for good. The test holds the admission
// until the first process, which exits right after its handshake, has
// exited. With Max = MaxLive = 1, the start that made it fails, and the
// next invocation runs in a new process.
func TestPoolEarlyExitNotAdmitted(t *testing.T) {
	var first atomic.Bool
	pc, _ := fakeConfig(t, "pool")
	ctl := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctl, "exit-after-welcome"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	pc.Env = append(pc.Env, "WL_FAKE_CTL="+ctl)
	pc.KillAfter = WatchdogOff
	log := newPoolLog()
	pool := newPool(t, PoolConfig{Process: pc, Max: 1, MaxLive: 1, LagThreshold: -1, observe: log.observe,
		admitting: func(p *Process) {
			if first.CompareAndSwap(false, true) {
				<-p.Done()
			}
		}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Start(ctx); err == nil || !strings.Contains(err.Error(), "exited right after its handshake") {
		t.Fatalf("Start with a process that exited before its admission: %v", err)
	}
	if n := len(pool.Stats()); n != 0 {
		t.Fatalf("%d members after the early exit: %+v", n, pool.Stats())
	}
	fakeRun(t, pool, "s", nil)
	if live, most := log.stats(); live != 1 || most > 1 || len(pool.Stats()) != 1 {
		t.Fatalf("%d alive (at most %d) by the pool's events, %d members, with MaxLive 1: %q", live, most, len(pool.Stats()), log.after(""))
	}
}

// Processes that started at once, before the pool recorded a host plugin
// declaration, are each held to the first declaration at their admission:
// of two initial processes whose declarations differ, only the first is
// admitted, and every member answers the same hooks.
func TestPoolPluginDeclarationsAgree(t *testing.T) {
	pool, _, log := fakePool(t, PoolConfig{Min: 2, Max: 2}, "WL_FAKE_PLUGIN=pid")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := pool.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "another host plugin") {
		t.Fatalf("Start with two declarations: %v", err)
	}
	// Start begins both spawns at once, before either is admitted.
	if evs := log.after(""); len(evs) < 2 || evs[0] != "spawn" || evs[1] != "spawn" {
		t.Fatalf("the test needs the two initial spawns first: %q", evs)
	}
	pool.mu.Lock()
	var decls []string
	for _, m := range pool.members {
		decls = append(decls, string(m.p.Hello().Plugin.Producer()))
	}
	recorded := string(pool.plugin)
	pool.mu.Unlock()
	if len(decls) != 1 || decls[0] != recorded {
		t.Fatalf("members declare %q; the pool recorded %q", decls, recorded)
	}
}

// syncless is a writer that is not safe for concurrent use, as a host's
// bytes.Buffer is not.
type syncless struct{ b bytes.Buffer }

func (s *syncless) Write(p []byte) (int, error) { return s.b.Write(p) }

// A pool's processes share the host's diagnostics writer, and the pool
// serializes their writes to it: two processes that write output that no
// invocation owns at the same time do not race on a writer that is not
// safe for concurrent use (the race detector would report it), and every
// line arrives whole.
func TestPoolDiagnosticsSerialized(t *testing.T) {
	const lines = 300
	pc, _ := fakeConfig(t, "pool")
	ctl := t.TempDir()
	out := &syncless{}
	pc.Env = append(pc.Env, "WL_FAKE_CTL="+ctl)
	pc.KillAfter = WatchdogOff
	pc.Diagnostics = out
	pool, err := NewPool(PoolConfig{Process: pc, Max: 2, Cap: 1, LagThreshold: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(context.Background()) })
	printing := map[string]any{"print": lines}
	h1 := startHeld(t, pool, ctl, "h1", printing)
	h2 := startHeld(t, pool, ctl, "h2", printing)
	h1.release(t)
	h2.release(t)
	var pids []int
	for _, h := range []heldInvocation{h1, h2} {
		r := <-h.done
		pids = append(pids, pidOfOutcome(t, r.out, r.err))
	}
	if pids[0] == pids[1] {
		t.Fatalf("both invocations ran in %d; the test needs two processes", pids[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Close has waited for both processes' output to end.
	text := out.b.String()
	for _, pid := range pids {
		for i := range lines {
			if want := fmt.Sprintf("harness pid %d stderr: line %d\n", pid, i); !strings.Contains(text, want) {
				t.Fatalf("missing %q", want)
			}
		}
	}
}

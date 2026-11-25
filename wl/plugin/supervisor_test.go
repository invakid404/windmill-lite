//go:build unix

package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests run the Supervisor on testdata/faultplugin, a fake engine
// that misbehaves as each test asks, through the real launch of
// ClientConfig. A fake clock times the admission, the drain, the stop and
// the backoff, so each test moves that time itself; health checks run on
// real time, every 20 ms. The engine's own side of a supervised launch is
// tested with the real engine in cmd/windmill-lite.

// faultInfo is the build identity that the fake engine answers, and the
// one that each test pins unless it says otherwise.
var faultInfo = map[string]any{
	"bundle_digest":  "sha256:" + strings.Repeat("ab", 32),
	"engine_version": "v0.0.0-test",
	"bun_version":    "1.4.3",
	"config_digest":  strings.Repeat("cd", 32),
	"labels":         map[string]string{"release": "test"},
}

var faultIdentity = PinnedIdentity{
	BundleDigest:  "sha256:" + strings.Repeat("ab", 32),
	EngineVersion: "v0.0.0-test",
	BunVersion:    "1.4.3",
	ConfigDigest:  strings.Repeat("cd", 32),
	Labels:        map[string]string{"release": "test"},
}

// buildFault builds testdata/faultplugin into a temporary directory of the
// test, and returns its path and SHA-256. It fails the test if it cannot:
// these tests never skip.
func buildFault(t *testing.T) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "faultplugin")
	if b, err := exec.Command("go", "build", "-o", path, "./testdata/faultplugin").CombinedOutput(); err != nil {
		t.Fatalf("go build of the fault plugin: %v\n%s", err, b)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(data)
	return path, hex.EncodeToString(s[:])
}

// fakeClock is a Supervisor's clock that moves only when a test advances
// it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at   time.Time
	d    time.Duration
	f    func()
	done bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &fakeTimer{at: c.now.Add(d), d: d, f: f}
	c.timers = append(c.timers, tm)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !tm.done
		tm.done = true
		return was
	}
}

// pending returns the durations of the timers that have neither fired nor
// stopped.
func (c *fakeClock) pending() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ds []time.Duration
	for _, tm := range c.timers {
		if !tm.done {
			ds = append(ds, tm.d)
		}
	}
	return ds
}

// advance moves the clock by d and calls the functions of the timers that
// are due, in order.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, tm := range c.timers {
		if !tm.done && !tm.at.After(c.now) {
			tm.done = true
			due = append(due, tm)
		}
	}
	c.mu.Unlock()
	slices.SortStableFunc(due, func(a, b *fakeTimer) int { return a.at.Compare(b.at) })
	for _, tm := range due {
		tm.f()
	}
}

// waitPending waits until the only pending timer has the duration d, and
// fails the test after 30 s.
func (c *fakeClock) waitPending(t *testing.T, d time.Duration) {
	t.Helper()
	until := time.Now().Add(30 * time.Second)
	for {
		p := c.pending()
		if len(p) == 1 && math.Abs(float64(p[0]-d)) <= float64(time.Microsecond) {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("the pending timers are %v after 30 s, want only %v", p, d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fault is one supervised fake engine.
type fault struct {
	t     *testing.T
	exe   string
	dir   string
	sup   *Supervisor
	clock *fakeClock
	stop  context.CancelFunc
	done  chan error
}

// superviseFault runs a Supervisor of the fake engine with the
// configuration cfg, to which it adds the directory of the test's files
// and faultInfo. tune changes the SupervisorConfig before NewSupervisor.
// prepare, when not nil, runs after NewSupervisor and before Run starts,
// to create the test's first files or replace the jitter. The Supervisor
// stops when the test ends.
func superviseFault(t *testing.T, cfg map[string]any, prepare func(f *fault), tune ...func(*SupervisorConfig)) *fault {
	t.Helper()
	exe, sum := buildFault(t)
	dir := t.TempDir()
	full := map[string]any{"dir": dir, "build_info": faultInfo}
	for k, v := range cfg {
		full[k] = v
	}
	data, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sc := SupervisorConfig{
		Launch:         Launch{Path: exe, SHA256: sum, Config: path, Logger: quiet},
		Identity:       faultIdentity,
		HealthInterval: 20 * time.Millisecond,
	}
	for _, f := range tune {
		f(&sc)
	}
	sup, err := NewSupervisor(sc)
	if err != nil {
		t.Fatal(err)
	}
	f := &fault{t: t, exe: exe, dir: dir, sup: sup, clock: newFakeClock(), done: make(chan error, 1)}
	sup.clock = f.clock
	// No spread: each delay of the backoff is its base value.
	sup.jitter = func() float64 { return 0.5 }
	if prepare != nil {
		prepare(f)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.stop = cancel
	go func() { f.done <- sup.Run(ctx) }()
	t.Cleanup(f.end)
	return f
}

// end stops the Supervisor and waits for Run to return, moving the fake
// clock past any drain or stop bound that it waits for.
func (f *fault) end() {
	f.stop()
	until := time.Now().Add(time.Minute)
	for {
		select {
		case err := <-f.done:
			if err != nil {
				f.t.Errorf("Run = %v, want nil", err)
			}
			f.done <- nil
			return
		case <-time.After(20 * time.Millisecond):
			if time.Now().After(until) {
				f.t.Fatal("Run did not return within a minute of its context's end")
			}
			f.clock.advance(time.Minute)
		}
	}
}

// file returns the path of the test's file name.
func (f *fault) file(name string) string { return filepath.Join(f.dir, name) }

// write writes s into the test's file name whole, by a rename, so that the
// fake engine never reads it half written.
func (f *fault) write(name, s string) {
	f.t.Helper()
	if err := os.WriteFile(f.file(name)+".tmp", []byte(s), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(f.file(name)+".tmp", f.file(name)); err != nil {
		f.t.Fatal(err)
	}
}

// waitFile waits until the test's file name exists, for up to 30 s.
func (f *fault) waitFile(name string) {
	f.t.Helper()
	for until := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(f.file(name)); err == nil {
			return
		}
		if time.Now().After(until) {
			f.t.Fatalf("%s did not appear within 30 s", name)
		}
	}
}

// pids returns the process ids of the fake engine's starts, in order.
func (f *fault) pids() []int {
	f.t.Helper()
	b, err := os.ReadFile(f.file("pids"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var pids []int
	for _, s := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(s)
		if err != nil {
			f.t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}

// waitStarts waits until the fake engine has started n times, for up to
// 30 s, and returns the nth start's process id.
func (f *fault) waitStarts(n int) int {
	f.t.Helper()
	for until := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if p := f.pids(); len(p) >= n {
			return p[n-1]
		}
		if time.Now().After(until) {
			f.t.Fatalf("the fake engine started %d times within 30 s, want %d", len(f.pids()), n)
		}
	}
}

// waitReady waits until Ready reports want, for up to 30 s.
func (f *fault) waitReady(want bool) {
	f.t.Helper()
	for until := time.Now().Add(30 * time.Second); f.sup.Ready() != want; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(until) {
			f.t.Fatalf("Ready is %v after 30 s, want %v", !want, want)
		}
	}
}

// alive reports whether a process of id pid exists, a zombie included.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitGoneProcess waits until no process of id pid exists, so that it has
// exited and been reaped, for up to 30 s.
func waitGoneProcess(t *testing.T, pid int) {
	t.Helper()
	for until := time.Now().Add(30 * time.Second); alive(pid); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("the process %d is still there after 30 s", pid)
		}
	}
}

// callPid runs a run through Call within d, and returns the process id
// that answered it.
func (f *fault) callPid(d time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var pid int
	err := f.sup.Call(ctx, func(c *Client) error {
		res, err := c.Run(ctx, &RunRequest{Path: "f/fault/run"})
		if err != nil {
			return err
		}
		var out struct{ Pid int }
		if err := json.Unmarshal(res.GetResult(), &out); err != nil {
			return err
		}
		pid = out.Pid
		return nil
	})
	return pid, err
}

// TestSupervisorReady checks Ready across two generations: false while
// the first is not admitted, true once it serves, false once a health
// check withdraws it and while the next waits for its backoff, true when
// the next serves, and false once Run's context ends.
func TestSupervisorReady(t *testing.T) {
	f := superviseFault(t, nil, func(f *fault) { f.write("health", "NOT_SERVING") })
	first := f.waitStarts(1)
	f.waitFile("answered-NOT_SERVING-" + strconv.Itoa(first))
	if f.sup.Ready() {
		t.Fatal("Ready while the engine answers NOT_SERVING to its admission")
	}
	if err := os.Remove(f.file("health")); err != nil {
		t.Fatal(err)
	}
	f.waitReady(true)

	f.write("health-"+strconv.Itoa(first), "NOT_SERVING")
	f.waitReady(false)
	f.clock.waitPending(t, time.Second)
	waitGoneProcess(t, first)
	if f.sup.Ready() {
		t.Fatal("Ready while the next generation waits for its backoff")
	}
	f.clock.advance(time.Second)
	f.waitReady(true)
	if second := f.waitStarts(2); second == first {
		t.Fatalf("the second generation has the first one's process %d", first)
	}

	f.stop()
	f.waitReady(false)
}

// TestSupervisorCallWaits checks Call: while no generation serves, a call
// waits, and one whose context ends returns ErrNotServing without calling
// its function; once a generation serves, the waiting call runs on it;
// once Run's context has ended, a call returns ErrSupervisorStopped.
func TestSupervisorCallWaits(t *testing.T) {
	f := superviseFault(t, nil, func(f *fault) { f.write("health", "NOT_SERVING") })
	first := f.waitStarts(1)
	f.waitFile("answered-NOT_SERVING-" + strconv.Itoa(first))

	type answer struct {
		pid int
		err error
	}
	waiting := make(chan answer, 1)
	go func() {
		pid, err := f.callPid(time.Minute)
		waiting <- answer{pid, err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	called := false
	err := f.sup.Call(ctx, func(*Client) error { called = true; return nil })
	cancel()
	if !errors.Is(err, ErrNotServing) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Call with no generation serving = %v, want ErrNotServing and context.DeadlineExceeded", err)
	}
	if called {
		t.Error("Call ran its function with no generation serving")
	}
	select {
	case a := <-waiting:
		t.Fatalf("the waiting call returned (%d, %v) before a generation served", a.pid, a.err)
	default:
	}
	if _, err := os.Stat(f.file("run-" + strconv.Itoa(first))); err == nil {
		t.Fatal("a run reached the engine before it was admitted")
	}

	if err := os.Remove(f.file("health")); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-waiting:
		if a.err != nil || a.pid != first {
			t.Fatalf("the waiting call = (%d, %v), want it answered by the process %d", a.pid, a.err, first)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the waiting call did not return within 30 s of the engine serving")
	}

	f.end()
	err = f.sup.Call(context.Background(), func(*Client) error { called = true; return nil })
	if !errors.Is(err, ErrSupervisorStopped) || called {
		t.Errorf("Call after Run ended = %v (function called: %v), want ErrSupervisorStopped", err, called)
	}
}

// TestSupervisorReadsStatus checks that a health check that answers
// NOT_SERVING without an error withdraws the generation, as go-plugin's own
// ping, which discards the status, would not: Ready turns false, the
// engine's process exits and is reaped, and the next launch waits for its
// backoff. The status is written whole, and the fake engine answered it,
// so no check failed.
func TestSupervisorReadsStatus(t *testing.T) {
	f := superviseFault(t, nil, nil)
	f.waitReady(true)
	first := f.waitStarts(1)
	if pid, err := f.callPid(10 * time.Second); err != nil || pid != first {
		t.Fatalf("a call = (%d, %v), want it answered by the process %d", pid, err, first)
	}

	f.write("health-"+strconv.Itoa(first), "NOT_SERVING")
	f.waitReady(false)
	if _, err := os.Stat(f.file("answered-NOT_SERVING-" + strconv.Itoa(first))); err != nil {
		t.Fatalf("the generation was withdrawn without a NOT_SERVING answer: %v", err)
	}
	waitGoneProcess(t, first)
	f.clock.waitPending(t, time.Second)
	if n := len(f.pids()); n != 1 {
		t.Errorf("the fake engine started %d times before the backoff ended, want 1", n)
	}
}

// TestSupervisorStalledIdentity checks the admission timeout: an engine
// that answers SERVING but never answers GetBuildInfo is still in its
// admission just before 30 s, and at 30 s its launch fails with
// ErrNotAdmitted, its process exits and the next launch waits for its
// backoff.
func TestSupervisorStalledIdentity(t *testing.T) {
	f := superviseFault(t, map[string]any{"stall_build_info": true}, nil)
	first := f.waitStarts(1)
	f.waitFile("buildinfo-" + strconv.Itoa(first))
	f.clock.waitPending(t, 30*time.Second)

	f.clock.advance(30*time.Second - time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	if !alive(first) || f.sup.Ready() {
		t.Fatalf("before 30 s: the engine's process is there: %v, Ready: %v; want there and not ready", alive(first), f.sup.Ready())
	}
	if p := f.clock.pending(); len(p) != 1 || p[0] != 30*time.Second {
		t.Fatalf("before 30 s the pending timers are %v, want only the admission's", p)
	}

	f.clock.advance(time.Millisecond)
	waitGoneProcess(t, first)
	f.clock.waitPending(t, time.Second)
	if f.sup.Ready() {
		t.Error("Ready after the admission failed")
	}
	_, err := f.callPid(50 * time.Millisecond)
	if !errors.Is(err, ErrNotServing) || !errors.Is(err, ErrNotAdmitted) || !strings.Contains(err.Error(), "GetBuildInfo") {
		t.Errorf("a call after the failed admission = %v, want ErrNotServing with the admission's failure in GetBuildInfo", err)
	}
}

// TestSupervisorUnresponsiveShutdown checks the stop of an engine that
// never answers go-plugin's shutdown call, on go-plugin's real launch.
// Without any hook, the engine's process is there until the 5 s bound,
// and then the supervisor kills it through the launch's runner and reaps
// it; a process that the test started from the same executable keeps
// running. With the reap of the killed process held by a test hook, no
// next launch is scheduled, however far the clock moves; released, the
// next generation starts and sees the old process gone.
func TestSupervisorUnresponsiveShutdown(t *testing.T) {
	t.Run("bound", func(t *testing.T) {
		f := superviseFault(t, map[string]any{"ignore_shutdown": true}, nil)
		sibling := exec.Command(f.exe, "sleep")
		if err := sibling.Start(); err != nil {
			t.Fatal(err)
		}
		siblingDone := make(chan struct{})
		go func() {
			sibling.Wait()
			close(siblingDone)
		}()
		t.Cleanup(func() {
			sibling.Process.Kill()
			<-siblingDone
		})

		f.waitReady(true)
		first := f.waitStarts(1)
		f.write("health-"+strconv.Itoa(first), "NOT_SERVING")
		f.waitFile("shutdown-" + strconv.Itoa(first))
		f.clock.waitPending(t, 5*time.Second)
		// The reap is not held here, so a process that died would be gone.
		time.Sleep(200 * time.Millisecond)
		if !alive(first) {
			t.Fatal("the engine's process is gone before the stop bound")
		}
		if p := f.clock.pending(); len(p) != 1 || p[0] != 5*time.Second {
			t.Fatalf("before the stop bound the pending timers are %v, want only the stop's", p)
		}

		f.clock.advance(5 * time.Second)
		waitGoneProcess(t, first)
		select {
		case <-siblingDone:
			t.Fatal("the test's own process of the same executable ended")
		default:
		}
		if !alive(sibling.Process.Pid) {
			t.Fatal("the test's own process of the same executable is gone")
		}
		f.clock.waitPending(t, time.Second)
	})

	t.Run("reap before the next launch", func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce, firstOnce sync.Once
		open := func() { releaseOnce.Do(func() { close(release) }) }
		held := make(chan struct{})
		hook := func() {
			// Only the first launch's reap is held.
			first := false
			firstOnce.Do(func() { first = true })
			if first {
				close(held)
				<-release
			}
		}
		beforeReap.Store(&hook)
		t.Cleanup(func() {
			open()
			beforeReap.Store(nil)
		})
		f := superviseFault(t, map[string]any{"ignore_shutdown": true}, nil)
		// Registered after superviseFault, so it runs before the
		// Supervisor is stopped, even when the test fails.
		t.Cleanup(open)

		f.waitReady(true)
		first := f.waitStarts(1)
		<-held
		f.write("health-"+strconv.Itoa(first), "NOT_SERVING")
		f.waitFile("shutdown-" + strconv.Itoa(first))
		f.clock.waitPending(t, 5*time.Second)
		f.clock.advance(5 * time.Second)
		// SIGKILL ends the process, whose reap is held: no next launch may
		// be scheduled, however far the clock moves.
		time.Sleep(300 * time.Millisecond)
		if p := f.clock.pending(); len(p) != 0 {
			t.Fatalf("with the reap held, the pending timers are %v, want none", p)
		}
		f.clock.advance(time.Hour)
		time.Sleep(300 * time.Millisecond)
		if n := len(f.pids()); n != 1 {
			t.Fatalf("the fake engine started %d times while the old process's reap was held, want 1", n)
		}

		open()
		waitGoneProcess(t, first)
		f.clock.waitPending(t, time.Second)
		f.clock.advance(time.Second)
		second := f.waitStarts(2)
		f.waitFile("seen-" + strconv.Itoa(second))
		seen, err := os.ReadFile(f.file("seen-" + strconv.Itoa(second)))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains("\n"+string(seen)+"\n", "\n"+strconv.Itoa(first)+"\n") {
			t.Errorf("the second generation started while the first one's process %d was still there", first)
		}
		f.waitReady(true)
	})
}

// backoffWant is the delay before a launch after retries delays, with the
// spread that the jitter value r gives to every delay but the first.
func backoffWant(retries int, r float64) time.Duration {
	if retries == 0 {
		return time.Second
	}
	d := math.Min(float64(time.Second)*math.Pow(1.6, float64(retries)), float64(120*time.Second))
	return time.Duration(math.Min(d*(1+0.2*(2*r-1)), float64(120*time.Second)))
}

// TestSupervisorRestartBackoff checks the delays between launches on the
// fake clock, with jitter values that spread each delay: launches that
// fail wait 1 s, unspread, then 1.6 times the delay before, at most
// 120 s, each spread by the jitter; a generation that served for 59 s
// does not start the delays again, and one that served for 60 s does,
// with an unspread 1 s.
func TestSupervisorRestartBackoff(t *testing.T) {
	// The kth spread delay takes the jitter value jitters[k % 4]; none is
	// 0.5, which would not spread it.
	jitters := []float64{0, 0.999, 0.25, 0.75}
	var mu sync.Mutex
	calls := 0
	f := superviseFault(t, nil, func(f *fault) {
		f.write("fail", "")
		f.sup.jitter = func() float64 {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return jitters[(calls-1)%len(jitters)]
		}
	})
	f.waitStarts(1)
	spread, retries := 0, 0
	next := func() time.Duration {
		if retries == 0 {
			return backoffWant(0, 0)
		}
		return backoffWant(retries, jitters[spread%len(jitters)])
	}
	wait := func() {
		t.Helper()
		d := next()
		f.clock.waitPending(t, d)
		f.clock.advance(d)
		if retries > 0 {
			spread++
		}
		retries++
	}
	// 1 s, 1.6 s, … reach 120 s at the twelfth delay; three more stay there.
	for i := 0; i < 15; i++ {
		wait()
		f.waitStarts(i + 2)
	}

	// The sixteenth launch has failed once its delay is pending; only then
	// may the next launch be admitted. It serves for 59 s on the fake clock.
	f.clock.waitPending(t, next())
	if err := os.Remove(f.file("fail")); err != nil {
		t.Fatal(err)
	}
	wait()
	f.waitReady(true)
	pid := f.waitStarts(17)
	f.clock.advance(59 * time.Second)
	f.write("health-"+strconv.Itoa(pid), "NOT_SERVING")
	f.waitReady(false)

	// Served for less than 60 s: the delays go on growing.
	wait()
	f.waitReady(true)
	pid = f.waitStarts(18)
	f.clock.advance(60 * time.Second)
	f.write("health-"+strconv.Itoa(pid), "NOT_SERVING")
	f.waitReady(false)

	// Served for 60 s: the next delay is 1 s, unspread.
	f.clock.waitPending(t, time.Second)
	mu.Lock()
	defer mu.Unlock()
	if calls != spread {
		t.Errorf("the jitter was taken %d times, want %d, once for each delay but the first ones", calls, spread)
	}
}

// TestSupervisorDrainBound checks the drain of a withdrawn generation: its
// call in flight goes on until the drain timeout plus the close grace,
// and then the supervisor stops the engine, which ends the call with
// ErrOutcomeUnknown. The bound is 75 s by default, 15 s for a drain
// timeout of 0, and the longest duration when the sum would overflow.
func TestSupervisorDrainBound(t *testing.T) {
	longest := time.Duration(math.MaxInt64)
	for _, c := range []struct {
		name  string
		drain *time.Duration
		bound time.Duration
	}{
		{"default", nil, 75 * time.Second},
		{"zero", new(time.Duration), 15 * time.Second},
		{"overflowing", func() *time.Duration { d := longest - time.Second; return &d }(), longest},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := superviseFault(t, map[string]any{"hold_runs": true}, nil, func(sc *SupervisorConfig) { sc.DrainTimeout = c.drain })
			f.waitReady(true)
			first := f.waitStarts(1)
			type answer struct {
				pid int
				err error
			}
			inFlight := make(chan answer, 1)
			go func() {
				pid, err := f.callPid(5 * time.Minute)
				inFlight <- answer{pid, err}
			}()
			f.waitFile("run-" + strconv.Itoa(first))

			f.write("health-"+strconv.Itoa(first), "NOT_SERVING")
			f.waitReady(false)
			f.clock.waitPending(t, c.bound)
			f.clock.advance(c.bound - time.Millisecond)
			time.Sleep(200 * time.Millisecond)
			select {
			case a := <-inFlight:
				t.Fatalf("the call in flight ended (%d, %v) before the drain's bound", a.pid, a.err)
			default:
			}
			if !alive(first) {
				t.Fatal("the engine's process is gone before the drain's bound")
			}
			if c.bound == longest {
				// No clock moves this far: the call's own end ends the drain.
				f.write("release", "")
				if a := <-inFlight; a.err != nil {
					t.Errorf("the released call = (%d, %v), want success", a.pid, a.err)
				}
				waitGoneProcess(t, first)
				return
			}

			f.clock.advance(time.Millisecond)
			select {
			case a := <-inFlight:
				if !errors.Is(a.err, ErrOutcomeUnknown) {
					t.Errorf("the call in flight = (%d, %v), want ErrOutcomeUnknown", a.pid, a.err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the call in flight did not end within 30 s of the drain's bound")
			}
			waitGoneProcess(t, first)
		})
	}
}

// readMS reads a deadline that the fake engine wrote, in milliseconds.
func (f *fault) readMS(name string) int64 {
	f.t.Helper()
	b, err := os.ReadFile(f.file(name))
	if err != nil {
		f.t.Fatal(err)
	}
	ms, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		f.t.Fatalf("%s holds %q, not a deadline", name, b)
	}
	return ms
}

// healthCheck is one health check that the fake engine logged: the
// milliseconds left until its deadline, and the status that it chose.
type healthCheck struct {
	ms     int64
	status string
}

// checks returns the health checks that the fake engine of pid logged, in
// order.
func (f *fault) checks(pid string) []healthCheck {
	f.t.Helper()
	b, err := os.ReadFile(f.file("checks-" + pid))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var cs []healthCheck
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		ms, status, ok := strings.Cut(line, " ")
		n, err := strconv.ParseInt(ms, 10, 64)
		if !ok || err != nil {
			f.t.Fatalf("checks-%s has the line %q, not a deadline and a status", pid, line)
		}
		cs = append(cs, healthCheck{n, status})
	}
	return cs
}

// TestSupervisorAdmissionDeadlines checks that each call of an admission
// has a deadline within what is left of its 30 s: a health check's is at
// most the 1 s health timeout, and once 29.5 s have passed on the fake
// clock, the health check that answers SERVING and GetBuildInfo each get
// at most 0.5 s. The fake engine logs every check, so the checks after
// the admission do not hide the admission's.
func TestSupervisorAdmissionDeadlines(t *testing.T) {
	f := superviseFault(t, nil, func(f *fault) { f.write("health", "NOT_SERVING") })
	pid := strconv.Itoa(f.waitStarts(1))
	f.waitFile("answered-NOT_SERVING-" + pid)
	if c := f.checks(pid)[0]; c.status != "NOT_SERVING" || c.ms <= 500 || c.ms > 1000 {
		t.Errorf("the first health check had %d ms left and chose %s, want at most the 1 s health timeout and NOT_SERVING", c.ms, c.status)
	}

	f.clock.advance(29500 * time.Millisecond)
	// Checks run one at a time: once one has at most 0.5 s, so do the next.
	for until := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		cs := f.checks(pid)
		if cs[len(cs)-1].ms <= 500 {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("after 29.5 s, health checks still have %d ms left, want at most 500", cs[len(cs)-1].ms)
		}
	}
	if err := os.Remove(f.file("health")); err != nil {
		t.Fatal(err)
	}
	f.waitReady(true)
	admitting := -1
	cs := f.checks(pid)
	for i, c := range cs {
		if c.status == "SERVING" {
			admitting = i
			break
		}
	}
	if admitting < 0 {
		t.Fatalf("no logged health check chose SERVING: %v", cs)
	}
	if c := cs[admitting]; c.ms <= 0 || c.ms > 500 {
		t.Errorf("the health check that admitted the engine had %d ms left, want at most 500", c.ms)
	}
	if b, err := os.ReadFile(f.file("deadline-buildinfo-" + pid)); err != nil || string(b) == "none" {
		t.Fatalf("GetBuildInfo's deadline: %q, %v; want one", b, err)
	}
	if ms := f.readMS("deadline-buildinfo-" + pid); ms <= 0 || ms > 500 {
		t.Errorf("GetBuildInfo had %d ms left, want at most 500", ms)
	}
}

// TestSupervisorCallContexts checks Call's two cut-offs: a call whose
// context has ended calls no function, though a generation serves, and
// returns ErrNotServing with the context's cause; and as soon as the
// context of Run has ended, Ready is false and a call returns
// ErrSupervisorStopped, while Run still drains a call in flight, which
// then completes.
func TestSupervisorCallContexts(t *testing.T) {
	f := superviseFault(t, map[string]any{"hold_runs": true}, nil)
	f.waitReady(true)
	first := f.waitStarts(1)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := f.sup.Call(ended, func(*Client) error { called = true; return nil })
	if called || !errors.Is(err, ErrNotServing) || !errors.Is(err, context.Canceled) {
		t.Errorf("Call with an ended context = %v (function called: %v), want ErrNotServing and context.Canceled, no call", err, called)
	}

	type answer struct {
		pid int
		err error
	}
	inFlight := make(chan answer, 1)
	go func() {
		pid, err := f.callPid(5 * time.Minute)
		inFlight <- answer{pid, err}
	}()
	f.waitFile("run-" + strconv.Itoa(first))
	f.stop()
	if f.sup.Ready() {
		t.Error("Ready right after the context of Run ended")
	}
	err = f.sup.Call(context.Background(), func(*Client) error { called = true; return nil })
	if called || !errors.Is(err, ErrSupervisorStopped) {
		t.Errorf("Call right after the context of Run ended = %v (function called: %v), want ErrSupervisorStopped", err, called)
	}
	select {
	case <-f.done:
		t.Fatal("Run returned with a call in flight")
	default:
	}
	f.write("release", "")
	if a := <-inFlight; a.err != nil || a.pid != first {
		t.Errorf("the call in flight = (%d, %v), want it answered by the process %d", a.pid, a.err, first)
	}
}

// TestSupervisorStopsLaunchAndCheck checks that the end of Run's context
// ends a launch whose engine withholds its handshake, and a health check
// that the engine never answers, though go-plugin's start timeout and the
// health timeout are 10 minutes: Run returns, and the engine's process
// has gone.
func TestSupervisorStopsLaunchAndCheck(t *testing.T) {
	ended := func(t *testing.T, f *fault, pid int) {
		t.Helper()
		f.stop()
		select {
		case err := <-f.done:
			f.done <- err
		case <-time.After(30 * time.Second):
			t.Fatal("Run did not return within 30 s of its context's end")
		}
		waitGoneProcess(t, pid)
	}
	t.Run("handshake", func(t *testing.T) {
		f := superviseFault(t, map[string]any{"hold_handshake": true}, nil, func(sc *SupervisorConfig) {
			sc.Launch.StartTimeout = 10 * time.Minute
		})
		pid := f.waitStarts(1)
		time.Sleep(200 * time.Millisecond)
		ended(t, f, pid)
	})
	t.Run("health check", func(t *testing.T) {
		f := superviseFault(t, nil, nil, func(sc *SupervisorConfig) { sc.HealthTimeout = 10 * time.Minute })
		f.waitReady(true)
		pid := f.waitStarts(1)
		f.write("health-"+strconv.Itoa(pid), "STALL")
		f.waitFile("stalled-" + strconv.Itoa(pid))
		ended(t, f, pid)
	})
}

// TestSupervisorReapsWithDescendant checks that the reap of the engine does
// not wait for its output pipes: the fake engine starts a process that
// holds them open, as a wedged harness process could. Once the generation
// is withdrawn and stopped, the engine's process is reaped while that
// process still runs, and the next generation starts and serves. Each such
// process ends when the test writes its stop file, or once its directory
// has gone, and the test waits for that.
func TestSupervisorReapsWithDescendant(t *testing.T) {
	f := superviseFault(t, map[string]any{"descendant": true}, nil)
	// Registered after superviseFault, so it runs before the Supervisor is
	// stopped and the directory removed, even when the test fails.
	t.Cleanup(func() {
		f.write("descendant-stop", "")
		names, _ := filepath.Glob(f.file("descendant-[0-9]*"))
		for _, name := range names {
			b, err := os.ReadFile(name)
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(string(b))
			if err != nil {
				continue
			}
			for until := time.Now().Add(30 * time.Second); alive(pid); time.Sleep(10 * time.Millisecond) {
				if time.Now().After(until) {
					t.Errorf("the process %d that held the pipes did not end within 30 s of its stop file", pid)
					break
				}
			}
		}
	})
	f.waitReady(true)
	first := f.waitStarts(1)
	f.waitFile("descendant-" + strconv.Itoa(first))
	desc := int(f.readMS("descendant-" + strconv.Itoa(first)))

	f.write("health-"+strconv.Itoa(first), "NOT_SERVING")
	f.waitReady(false)
	waitGoneProcess(t, first)
	if !alive(desc) {
		t.Fatal("the process that holds the pipes has gone; the test needs it")
	}
	f.clock.waitPending(t, time.Second)
	f.clock.advance(time.Second)
	f.waitReady(true)
	f.waitStarts(2)
	if !alive(desc) {
		t.Fatal("the process that holds the pipes has gone before the next generation served")
	}
}

// TestSupervisorKeepsLaunchCopy checks that NewSupervisor keeps a copy of
// Launch.Env: an entry that the host changes after NewSupervisor does not
// reach the engine.
func TestSupervisorKeepsLaunchCopy(t *testing.T) {
	env := []string{"PROBE=before"}
	f := superviseFault(t, nil, func(*fault) { env[0] = "PROBE=after" }, func(sc *SupervisorConfig) { sc.Launch.Env = env })
	pid := strconv.Itoa(f.waitStarts(1))
	f.waitFile("env-" + pid)
	b, err := os.ReadFile(f.file("env-" + pid))
	if err != nil {
		t.Fatal(err)
	}
	vars := strings.Split(string(b), "\n")
	if !slices.Contains(vars, "PROBE=before") || slices.Contains(vars, "PROBE=after") {
		t.Errorf("the engine's environment is %q, want PROBE=before, as at NewSupervisor", vars)
	}
}

// TestSupervisorConfigRefusals checks what NewSupervisor refuses, and that
// Run runs once.
func TestSupervisorConfigRefusals(t *testing.T) {
	good := func() SupervisorConfig {
		return SupervisorConfig{
			Launch:   Launch{Path: "/bin/true", SHA256: strings.Repeat("00", 32), Config: "/dev/null"},
			Identity: faultIdentity,
		}
	}
	for _, c := range []struct {
		name   string
		change func(*SupervisorConfig)
		want   string
	}{
		{"no path", func(c *SupervisorConfig) { c.Launch.Path = "" }, "Launch.Path is empty"},
		{"a short SHA-256", func(c *SupervisorConfig) { c.Launch.SHA256 = "00" }, "Launch.SHA256"},
		{"no bundle digest", func(c *SupervisorConfig) { c.Identity.BundleDigest = "" }, "Identity.BundleDigest is empty"},
		{"no engine version", func(c *SupervisorConfig) { c.Identity.EngineVersion = "" }, "Identity.EngineVersion is empty"},
		{"no Bun version", func(c *SupervisorConfig) { c.Identity.BunVersion = "" }, "Identity.BunVersion is empty"},
		{"no configuration digest", func(c *SupervisorConfig) { c.Identity.ConfigDigest = "" }, "Identity.ConfigDigest is empty"},
		{"a negative admission timeout", func(c *SupervisorConfig) { c.AdmissionTimeout = -1 }, "AdmissionTimeout"},
		{"a negative health interval", func(c *SupervisorConfig) { c.HealthInterval = -1 }, "HealthInterval"},
		{"a negative health timeout", func(c *SupervisorConfig) { c.HealthTimeout = -1 }, "HealthTimeout"},
		{"a negative drain timeout", func(c *SupervisorConfig) { d := time.Duration(-1); c.DrainTimeout = &d }, "DrainTimeout"},
		{"a negative close grace", func(c *SupervisorConfig) { c.CloseGrace = -1 }, "CloseGrace"},
		{"a negative stop timeout", func(c *SupervisorConfig) { c.StopTimeout = -1 }, "StopTimeout"},
	} {
		cfg := good()
		c.change(&cfg)
		if _, err := NewSupervisor(cfg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: NewSupervisor = %v, want an error naming %q", c.name, err, c.want)
		}
	}

	s, err := NewSupervisor(good())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Run(ctx); err != nil {
		t.Errorf("Run with an ended context = %v, want nil", err)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Error("a second Run returned nil, want an error")
	}
	if err := s.Call(context.Background(), func(*Client) error { return nil }); !errors.Is(err, ErrSupervisorStopped) {
		t.Errorf("Call after Run = %v, want ErrSupervisorStopped", err)
	}
}

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
// and faultInfo. prepare, when not nil, runs before Run starts, to create
// the test's first files or replace the jitter. The Supervisor stops when
// the test ends.
func superviseFault(t *testing.T, cfg map[string]any, prepare func(f *fault)) *fault {
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
	sup, err := NewSupervisor(SupervisorConfig{
		Launch:         Launch{Path: exe, SHA256: sum, Config: path, Logger: quiet},
		Identity:       faultIdentity,
		HealthInterval: 20 * time.Millisecond,
	})
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

// write writes s into the test's file name.
func (f *fault) write(name, s string) {
	f.t.Helper()
	if err := os.WriteFile(f.file(name), []byte(s), 0o600); err != nil {
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
	f.waitFile("checked-" + strconv.Itoa(first))
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
	f.waitFile("checked-" + strconv.Itoa(first))

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
// backoff.
func TestSupervisorReadsStatus(t *testing.T) {
	f := superviseFault(t, nil, nil)
	f.waitReady(true)
	first := f.waitStarts(1)
	if pid, err := f.callPid(10 * time.Second); err != nil || pid != first {
		t.Fatalf("a call = (%d, %v), want it answered by the process %d", pid, err, first)
	}

	f.write("health-"+strconv.Itoa(first), "NOT_SERVING")
	f.waitReady(false)
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

// TestSupervisorUnresponsiveShutdown checks the stop bound on go-plugin's
// real launch: an engine that never answers go-plugin's shutdown call
// keeps its process until the 5 s bound, and then the supervisor kills it
// through the launch's runner and reaps it. A process that the test
// started from the same executable keeps running, and the next generation
// starts only after the old process has gone.
func TestSupervisorUnresponsiveShutdown(t *testing.T) {
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
}

// backoffWant is the delay before a launch after retries delays, with the
// spread that the jitter value r gives.
func backoffWant(retries int, r float64) time.Duration {
	d := math.Min(float64(time.Second)*math.Pow(1.6, float64(retries)), float64(120*time.Second))
	return time.Duration(math.Min(d*(1+0.2*(2*r-1)), float64(120*time.Second)))
}

// TestSupervisorRestartBackoff checks the delays between launches on the
// fake clock: launches that fail wait 1 s, then 1.6 times the delay
// before, at most 120 s, each spread by the jitter value; a generation
// that served for 59 s does not start the delays again, and one that
// served for 60 s does.
func TestSupervisorRestartBackoff(t *testing.T) {
	// The kth delay's spread takes the jitter value jitters[k % 5].
	jitters := []float64{0.5, 0, 0.999, 0.25, 0.75}
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
	k, retries := 0, 0
	wait := func() {
		t.Helper()
		d := backoffWant(retries, jitters[k%len(jitters)])
		f.clock.waitPending(t, d)
		f.clock.advance(d)
		k++
		retries++
	}
	// 1 s, 1.6 s, … reach 120 s at the twelfth delay; three more stay there.
	for i := 0; i < 15; i++ {
		wait()
		f.waitStarts(i + 2)
	}

	// The next launch is admitted; it serves for 59 s on the fake clock.
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

	// Served for 60 s: the delays start again from 1 s.
	f.clock.waitPending(t, backoffWant(0, jitters[k%len(jitters)]))
}

// TestSupervisorDrainBound checks the drain of a withdrawn generation: its
// call in flight goes on until the drain timeout plus the close grace, 75 s
// by default, and then the supervisor stops the engine, which ends the
// call with ErrOutcomeUnknown.
func TestSupervisorDrainBound(t *testing.T) {
	f := superviseFault(t, map[string]any{"hold_runs": true}, nil)
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
	f.clock.waitPending(t, 75*time.Second)
	f.clock.advance(75*time.Second - time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	select {
	case a := <-inFlight:
		t.Fatalf("the call in flight ended (%d, %v) before the drain's bound", a.pid, a.err)
	default:
	}
	if !alive(first) {
		t.Fatal("the engine's process is gone before the drain's bound")
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
		{"a negative drain timeout", func(c *SupervisorConfig) { c.DrainTimeout = -1 }, "DrainTimeout"},
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

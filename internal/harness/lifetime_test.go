package harness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// fakeClock is a clock that moves only when the test advances it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// lifetimePool starts a warm pool of the real Bun harness with the host
// plugin src, heartbeats every 20 ms, and the recycle triggers r, timed by
// clock. It returns the pool, its diagnostics and its first process's pid.
func lifetimePool(t *testing.T, src string, r RecycleTriggers, clock *fakeClock) (*Pool, *syncBuffer, int) {
	t.Helper()
	cfg, diag := bunConfig(t)
	pool := lifetimePoolWith(t, cfg, src, r, clock)
	return pool, diag, pool.Stats()[0].PID
}

// lifetimePoolWith is lifetimePool with the process template cfg, whose
// Diagnostics the test chose; it returns the pool alone, whose first
// process's pid is pool.Stats()[0].PID.
func lifetimePoolWith(t *testing.T, cfg ProcessConfig, src string, r RecycleTriggers, clock *fakeClock, opts ...func(*PoolConfig)) *Pool {
	t.Helper()
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Args = append(cfg.Args, "--plugin", plugin)
	cfg.Heartbeat = 20 * time.Millisecond
	pc := PoolConfig{Process: cfg, Recycle: r}
	if clock != nil {
		pc.now = clock.Now
	}
	for _, o := range opts {
		o(&pc)
	}
	pool, err := NewPool(pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool.Close(ctx)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if s := pool.Stats(); len(s) != 1 {
		t.Fatalf("the pool started %d processes, want 1", len(s))
	}
	return pool
}

// waitReports waits until the process pid has sent n more health reports
// that satisfy ok, and fails if it leaves the pool meanwhile.
func waitReports(t *testing.T, pool *Pool, pid, n int, ok func(*Health) bool) {
	t.Helper()
	var last *Health
	seen := 0
	waitFor(t, fmt.Sprintf("%d health reports of process %d", n, pid), func() bool {
		s, in := statsOf(pool, pid)
		if !in {
			t.Fatalf("process %d left the pool", pid)
		}
		if s.Health != nil && s.Health != last {
			last = s.Health
			if ok(s.Health) {
				seen++
			}
		}
		return seen >= n
	})
}

// stays checks, over three more health reports of the process pid, that
// it is still the pool's only process and is not leaving.
func stays(t *testing.T, pool *Pool, pid int, what string) {
	t.Helper()
	waitReports(t, pool, pid, 3, func(*Health) bool { return true })
	if s := pool.Stats(); len(s) != 1 || s[0].PID != pid || s[0].Retiring {
		t.Fatalf("%s: the pool's processes are %+v, want only %d, serving", what, s, pid)
	}
}

// TestPoolRecycleUnsettledRun checks the unsettled-run trigger on the real
// Bun harness, its bound timed by a fake clock from the end of the run.
// The run r-hung has a step held at a gate, and its plugin hook
// onModuleStart never returns once the engine has stopped waiting for it.
// While r-hung still runs, the process stays far past the bound. Once its
// step has ended and the run has ended (RunEnded), the process stays 1 ms
// short of the bound; at the bound a replacement starts beside it. A run
// pinned to the process holds it there, and it leaves only once that run
// has ended, with a diagnostics line that says why. With the trigger off,
// it stays past the bound.
func TestPoolRecycleUnsettledRun(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "on", false: "off"}[on], func(t *testing.T) {
			dir := t.TempDir()
			hookStarted := filepath.Join(dir, "hook.started")
			src := fmt.Sprintf(`import { writeFileSync } from "node:fs";
export default {
  onModuleStart() {
    writeFileSync(%q, "started");
    return new Promise(() => {});
  },
};
`, hookStarted)
			r := RecycleTriggers{}
			if on {
				r.UnsettledRun = time.Minute
			}
			clock := newFakeClock()
			pinnedGate, hungGate := newGate(t, dir, "pinned"), newGate(t, dir, "hung")
			pool, diag, first := lifetimePool(t, src, r, clock)
			// step runs g's step for the run of pin, and reports its end.
			step := func(pin *Pin, run string, g gate) chan error {
				inv := g.invocation(newRecorder())
				inv.Run = run
				done := make(chan error, 1)
				go func() {
					ctx, cancel := context.WithTimeout(WithPin(context.Background(), pin), 30*time.Second)
					defer cancel()
					out, err := pool.Execute(ctx, inv)
					if err == nil && out.Kind != engine.OutcomeOK {
						err = fmt.Errorf("outcome %+v", out)
					}
					done <- err
				}()
				waitFile(t, g.started)
				return done
			}
			pinned := mustPin(t, pool, "r-pinned")
			pinnedStep := step(pinned, "r-pinned", pinnedGate)
			hung := pool.Unpinned("r-hung")
			hungStep := step(hung, "r-hung", hungGate)
			// r-hung's hook, which the engine stops waiting for.
			ctx, stop := context.WithCancel(WithPin(context.Background(), hung))
			late := make(chan error, 1)
			go func() {
				_, err := pool.HostCall(ctx, "on_module_start", "r-hung", "m:a", jsonval.FromProducer([]byte(`{"site":"m:a"}`)))
				late <- err
			}()
			waitFile(t, hookStarted)
			stop()
			if err := <-late; err == nil || !strings.Contains(err.Error(), "context canceled") {
				t.Fatalf("the hung hook's call: %v; want it canceled", err)
			}
			clock.Advance(time.Hour)
			stays(t, pool, first, "an hour past the bound while the hook's run still runs")
			os.WriteFile(hungGate.release, nil, 0o600)
			if err := <-hungStep; err != nil {
				t.Fatalf("r-hung's step: %v", err)
			}
			pool.RunEnded(hung)
			clock.Advance(time.Minute - time.Millisecond)
			stays(t, pool, first, "1 ms short of the bound after the run ended")
			clock.Advance(time.Millisecond)
			if !on {
				stays(t, pool, first, "past the bound with the trigger off")
				os.WriteFile(pinnedGate.release, nil, 0o600)
				if err := <-pinnedStep; err != nil {
					t.Fatalf("the pinned run's step: %v", err)
				}
				return
			}
			waitFor(t, "a replacement beside the process", func() bool { return len(pool.Stats()) == 2 })
			// The pinned run holds the process: it does not leave yet.
			waitReports(t, pool, first, 3, func(*Health) bool { return true })
			if s, in := statsOf(pool, first); !in || s.Retiring {
				t.Fatalf("the process left while a run pinned to it was live: %+v", s)
			}
			os.WriteFile(pinnedGate.release, nil, 0o600)
			if err := <-pinnedStep; err != nil {
				t.Fatalf("the pinned run's step: %v", err)
			}
			pool.RunEnded(pinned)
			waitFor(t, "the process to leave once its pinned run ended", func() bool { _, in := statsOf(pool, first); return !in })
			if s := pool.Stats(); len(s) != 1 || s[0].PID == first {
				t.Fatalf("after the process left, the pool's processes are %+v", s)
			}
			want := "harness pool: process " + strconv.Itoa(first) + " leaves the pool: a host plugin hook has held its ended run unsettled for 1m0s (the bound is 1m0s)"
			waitFor(t, "the diagnostics line", func() bool { return strings.Contains(diag.String(), want) })
		})
	}
}

// TestPoolRecycleUnsettledSettlementHook checks that a late onRunSettled
// does not trip the unsettled-run trigger: its run has settled, so it
// holds nothing (ADR 0004 §Host plugins item 2). The plugin's onRunSettled
// never returns, and the engine stops waiting for it; past the bound, the
// process stays.
func TestPoolRecycleUnsettledSettlementHook(t *testing.T) {
	hookStarted := filepath.Join(t.TempDir(), "hook.started")
	src := fmt.Sprintf(`import { writeFileSync } from "node:fs";
export default {
  onRunSettled() {
    writeFileSync(%q, "started");
    return new Promise(() => {});
  },
};
`, hookStarted)
	clock := newFakeClock()
	pool, _, first := lifetimePool(t, src, RecycleTriggers{UnsettledRun: time.Minute}, clock)
	// The call carries its run's pin, and the run has ended, so that only
	// the hook tells it apart from one that holds the run.
	settled := pool.Unpinned("r-settled")
	ctx, stop := context.WithCancel(WithPin(context.Background(), settled))
	late := make(chan error, 1)
	go func() {
		_, err := pool.HostCall(ctx, "on_run_settled", "r-settled", "", jsonval.FromProducer([]byte(`{"trace":{"run":"r-settled"}}`)))
		late <- err
	}()
	waitFile(t, hookStarted)
	stop()
	if err := <-late; err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("the hung onRunSettled's call: %v; want it canceled", err)
	}
	pool.RunEnded(settled)
	clock.Advance(time.Hour)
	stays(t, pool, first, "a late onRunSettled past the bound")
}

// TestPoolRecycleHeldAge checks the held-age trigger on the real Bun
// harness. The plugin's heldAge reports what the test writes to a file:
// ages under the bound of 2 minutes, which rise and fall as holds that
// overlap would while each ends in time, keep the process; an age at the
// bound replaces it, with a diagnostics line that says why. With the
// trigger off, an age past the bound keeps it.
func TestPoolRecycleHeldAge(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "on", false: "off"}[on], func(t *testing.T) {
			ageFile := filepath.Join(t.TempDir(), "age")
			setAge := func(ms int64) {
				t.Helper()
				if err := os.WriteFile(ageFile, []byte(strconv.FormatInt(ms, 10)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			setAge(0)
			// The plugin keeps its state on its own instance and reads it
			// through this, as a plugin keeps a table of what it holds.
			src := fmt.Sprintf(`import { readFileSync } from "node:fs";
export default {
  ageFile: %q,
  heldAge() {
    return Number(readFileSync(this.ageFile, "utf8"));
  },
};
`, ageFile)
			r := RecycleTriggers{}
			if on {
				r.HeldAge = 2 * time.Minute
			}
			pool, diag, first := lifetimePool(t, src, r, nil)
			for _, ms := range []int64{60_000, 119_999, 30_000, 119_999} {
				setAge(ms)
				waitReports(t, pool, first, 2, func(h *Health) bool { return h.HeldAgeMS == ms })
			}
			stays(t, pool, first, "ages under the bound")
			if !on {
				setAge(600_000)
				waitReports(t, pool, first, 2, func(h *Health) bool { return h.HeldAgeMS == 600_000 })
				stays(t, pool, first, "an age past the bound with the trigger off")
				return
			}
			setAge(120_000)
			waitFor(t, "the process to be replaced", func() bool {
				s := pool.Stats()
				_, in := statsOf(pool, first)
				return len(s) == 1 && !in
			})
			want := "harness pool: process " + strconv.Itoa(first) + " leaves the pool: its host plugin reports a resource held 2m0s after its run ended (the bound is 2m0s)"
			waitFor(t, "the diagnostics line", func() bool { return strings.Contains(diag.String(), want) })
		})
	}
}

// tripGateWriter is a diagnostics writer that blocks each write holding hold
// until release closes, and tells blocked once one waits.
type tripGateWriter struct {
	hold    string
	release chan struct{}
	blocked chan struct{}
	once    sync.Once
	buf     syncBuffer
}

func (w *tripGateWriter) Write(b []byte) (int, error) {
	if strings.Contains(string(b), w.hold) {
		w.once.Do(func() { close(w.blocked) })
		<-w.release
	}
	return w.buf.Write(b)
}

// TestPoolTripLineDoesNotHoldThePool checks that a diagnostics writer that
// blocks on a trip's line holds up no pool operation while the line waits.
// A process trips the held-age trigger, and then its replacement:
//   - starts: the replacement takes its place, and the pool answers;
//   - fails to start, as its program is gone: the failure is still
//     reported (OnStartFailure), and the pool answers, though the failure's
//     own line waits behind the trip's.
//
// Once the writer goes on, the trip's line comes.
func TestPoolTripLineDoesNotHoldThePool(t *testing.T) {
	for _, fails := range []bool{false, true} {
		name := "starts"
		if fails {
			name = "fails to start"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ageFile := filepath.Join(dir, "age")
			if err := os.WriteFile(ageFile, []byte("0"), 0o600); err != nil {
				t.Fatal(err)
			}
			src := fmt.Sprintf(`import { readFileSync } from "node:fs";
export default { heldAge: () => Number(readFileSync(%q, "utf8")) };
`, ageFile)
			cfg, _ := bunConfig(t)
			// The pool starts its processes through a link to the program,
			// which the failing case removes.
			program := cfg.Path
			cfg.Path = filepath.Join(dir, "program")
			if err := os.Symlink(program, cfg.Path); err != nil {
				t.Fatal(err)
			}
			w := &tripGateWriter{hold: "leaves the pool", release: make(chan struct{}), blocked: make(chan struct{})}
			cfg.Diagnostics = w
			failed := make(chan error, 1)
			pool := lifetimePoolWith(t, cfg, src, RecycleTriggers{HeldAge: 2 * time.Minute}, nil, func(pc *PoolConfig) {
				pc.OnStartFailure = func(err error) {
					select {
					case failed <- err:
					default:
					}
				}
			})
			// Registered after the pool's, so it runs first: a pool that
			// the writer held up would not close.
			t.Cleanup(func() {
				select {
				case <-w.release:
				default:
					close(w.release)
				}
			})
			first := pool.Stats()[0].PID
			if fails {
				if err := os.Remove(cfg.Path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(ageFile, []byte("120000"), 0o600); err != nil {
				t.Fatal(err)
			}
			select {
			case <-w.blocked:
			case <-time.After(10 * time.Second):
				t.Fatal("the trip's line never reached the writer")
			}
			if fails {
				select {
				case err := <-failed:
					if !errors.Is(err, fs.ErrNotExist) {
						t.Errorf("OnStartFailure got %v; want the start of a program that is gone", err)
					}
				case <-time.After(20 * time.Second):
					t.Fatal("the failed start was not reported while the trip's line waited")
				}
				stats := make(chan []ProcessStats, 1)
				go func() { stats <- pool.Stats() }()
				select {
				case <-stats:
				case <-time.After(5 * time.Second):
					t.Fatal("the pool did not answer Stats within 5 s while the trip's line waited")
				}
				if err := os.Symlink(program, cfg.Path); err != nil {
					t.Fatal(err)
				}
			} else {
				waitFor(t, "the replacement while the line waits", func() bool {
					s := pool.Stats()
					_, in := statsOf(pool, first)
					return len(s) == 1 && !in
				})
			}
			close(w.release)
			waitFor(t, "the line once the writer goes on", func() bool {
				return strings.Contains(w.buf.String(), "process "+strconv.Itoa(first)+" leaves the pool")
			})
		})
	}
}

// TestPoolHeldAgeRejectedPromise checks that a heldAge that answers a
// rejected promise, at every health report, while a step runs, reports no
// age, counts no process-level error, reaches the step with no warning,
// and writes one line to the harness's standard error.
func TestPoolHeldAgeRejectedPromise(t *testing.T) {
	src := `export default { heldAge: () => Promise.reject(new Error("an age that never came")) };
`
	g := newGate(t, t.TempDir(), "step")
	pool, diag, first := lifetimePool(t, src, RecycleTriggers{HeldAge: 2 * time.Minute}, nil)
	rec := newRecorder()
	done := make(chan error, 1)
	go func() {
		out, err := execute(t, pool, g.invocation(rec), 30*time.Second)
		if err == nil && out.Kind != engine.OutcomeOK {
			err = fmt.Errorf("outcome %+v", out)
		}
		done <- err
	}()
	waitFile(t, g.started)
	waitReports(t, pool, first, 5, func(h *Health) bool { return h.Errors == 0 && h.HeldAgeMS == 0 })
	os.WriteFile(g.release, nil, 0o600)
	if err := <-done; err != nil {
		t.Fatalf("the step: %v", err)
	}
	rec.mu.Lock()
	warnings := slices.Clone(rec.warnings)
	rec.mu.Unlock()
	if len(warnings) != 0 {
		t.Errorf("the step got warnings %+v, want none", warnings)
	}
	if n := strings.Count(diag.String(), "the host plugin's heldAge returned a promise"); n != 1 {
		t.Errorf("the harness wrote %d lines about heldAge, want 1:\n%s", n, diag.String())
	}
}

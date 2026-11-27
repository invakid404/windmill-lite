package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	s := pool.Stats()
	if len(s) != 1 {
		t.Fatalf("the pool started %d processes, want 1", len(s))
	}
	return pool, diag, s[0].PID
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
// Bun harness, its bound timed by a fake clock. The plugin's
// onModuleStart never returns, and the engine stops waiting for it, as
// its run's end would: the run stays unsettled. A run pinned to the
// process holds it in a step. With the trigger at 1 minute, the process
// keeps serving while the clock stands 1 ms short of the bound; at the
// bound a replacement starts beside it, and it leaves only once the pinned
// run has ended, with a diagnostics line that says why. With the trigger
// off, it stays past the bound.
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
			g := newGate(t, dir, "pinned")
			pool, diag, first := lifetimePool(t, src, r, clock)
			pin := mustPin(t, pool, "r-pinned")
			inv := g.invocation(newRecorder())
			inv.Run = "r-pinned"
			held := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(WithPin(context.Background(), pin), 30*time.Second)
				defer cancel()
				out, err := pool.Execute(ctx, inv)
				if err == nil && out.Kind != engine.OutcomeOK {
					err = fmt.Errorf("outcome %+v", out)
				}
				held <- err
			}()
			waitFile(t, g.started)
			// The hook of another run, which the engine stops waiting for.
			ctx, stop := context.WithCancel(context.Background())
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
			clock.Advance(time.Minute - time.Millisecond)
			stays(t, pool, first, "1 ms short of the bound")
			clock.Advance(time.Millisecond)
			if !on {
				stays(t, pool, first, "past the bound with the trigger off")
				os.WriteFile(g.release, nil, 0o600)
				if err := <-held; err != nil {
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
			os.WriteFile(g.release, nil, 0o600)
			if err := <-held; err != nil {
				t.Fatalf("the pinned run's step: %v", err)
			}
			pool.RunEnded(pin)
			waitFor(t, "the process to leave once its pinned run ended", func() bool { _, in := statsOf(pool, first); return !in })
			if s := pool.Stats(); len(s) != 1 || s[0].PID == first {
				t.Fatalf("after the process left, the pool's processes are %+v", s)
			}
			want := "harness pool: process " + strconv.Itoa(first) + " leaves the pool: a host plugin hook has held its ended run unsettled for 1m0s (the bound is 1m0s)"
			if !strings.Contains(diag.String(), want) {
				t.Errorf("the diagnostics lack %q:\n%s", want, diag.String())
			}
		})
	}
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
			src := fmt.Sprintf(`import { readFileSync } from "node:fs";
export default {
  heldAge: () => Number(readFileSync(%q, "utf8")),
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
			if !strings.Contains(diag.String(), want) {
				t.Errorf("the diagnostics lack %q:\n%s", want, diag.String())
			}
		})
	}
}

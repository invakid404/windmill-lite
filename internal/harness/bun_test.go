package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// These tests run Process and Pool against the real Bun harness on the
// pinned Bun (see bunPath).

func TestBunHandshake(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	h := p.Hello()
	if h.Proto != ProtocolVersion || h.Harness.Kind != "bun" || h.Runtime != PinnedBun || h.Bundle != cfg.Bundle || h.PID != p.PID() {
		t.Fatalf("hello %+v", h)
	}
	if h.Caps != (Caps{}) {
		t.Fatalf("the C1 runtime offers no optional capability, got %+v", h.Caps)
	}
}

func TestBunHandshakeRejects(t *testing.T) {
	for _, tc := range []struct {
		code string
		edit func(*ProcessConfig)
	}{
		{RejectRuntime, func(c *ProcessConfig) { c.Runtime = "1.4.2" }},
		{RejectBundle, func(c *ProcessConfig) { c.Bundle = "sha256:other" }},
		{RejectHarness, func(c *ProcessConfig) { c.Kind = "python" }},
	} {
		t.Run(tc.code, func(t *testing.T) {
			cfg, diag := bunConfig(t)
			tc.edit(&cfg)
			_, err := Spawn(context.Background(), cfg)
			var he *HandshakeError
			if !errors.As(err, &he) || he.Code != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
			// The harness said why it exits on its stderr.
			deadline := time.Now().Add(2 * time.Second)
			for !strings.Contains(diag.String(), "rejected: "+tc.code) {
				if time.Now().After(deadline) {
					t.Fatalf("diagnostics %q", diag.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// cl P4-3: the watchdog's SIGKILL must reach the process that speaks the
// protocol. A harness started through a wrapper that forks it says hello
// from another pid, and the engine refuses it.
func TestBunPIDCheckRefusesAWrapper(t *testing.T) {
	cfg, _ := bunConfig(t)
	script := `"$0" "$@"; exit $?`
	cfg.Args = append([]string{"-c", script, cfg.Path}, cfg.Args...)
	cfg.Path = "/bin/sh"
	_, err := Spawn(context.Background(), cfg)
	var he *HandshakeError
	if !errors.As(err, &he) || he.Code != RejectPID {
		t.Fatalf("got %v, want %s", err, RejectPID)
	}
	// The same wrapper with exec keeps the pid and is accepted.
	cfg.Args[1] = `exec "$0" "$@"`
	spawn(t, cfg)
}

func TestBunEcho(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	rec := newRecorder()
	out, err := execute(t, p, newInvocation("echo", `{"b":[1,2],"a":{"x":null},"1":"one"}`, rec), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
	// The harness's own JSON text, unchanged by Go (ADR 0005).
	if got := string(out.Body.Producer()); got != `{"1":"one","b":[1,2],"a":{"x":null}}` {
		t.Fatalf("body %s", got)
	}
	// Settled before Execute returned.
	if got := rec.log(); got != "settled completed" {
		t.Fatalf("reporter calls %q", got)
	}
}

func TestBunErrorAndEscapedOutcomes(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	out, err := execute(t, p, newInvocation("throws", `{"message":"boom"}`, newRecorder()), 5*time.Second)
	var body struct{ Message, Name, Stack string }
	if err != nil || out.Kind != engine.OutcomeError || json.Unmarshal(out.Body.Producer(), &body) != nil ||
		body.Message != "boom" || body.Name != "Error" || !strings.Contains(body.Stack, "boom") {
		t.Fatalf("got %+v, %v", out, err)
	}
	out, err = execute(t, p, newInvocation("top-level-throw", `{}`, newRecorder()), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 1 || !strings.Contains(out.Tail, "top-level failure") {
		t.Fatalf("got %+v, %v", out, err)
	}
}

// ADR 0004: a harness process that dies fails its in-flight invocations as
// escaped with its real exit code; co-resident ones included.
func TestBunProcessDeathFailsEveryInFlightInvocation(t *testing.T) {
	for _, tc := range []struct {
		script, args string
		code         int
	}{
		// Without C3a's process-level handlers, a detached rejection ends
		// the process with code 1.
		{"detached-reject", `{"ms":100}`, 1},
		// Without C3a's interception, process.exit ends the process.
		{"exit", `{"ms":100,"code":7}`, 7},
	} {
		t.Run(tc.script, func(t *testing.T) {
			cfg, _ := bunConfig(t)
			p := spawn(t, cfg)
			type res struct {
				out engine.StepOutcome
				err error
				rec *recorder
			}
			results := make(chan res, 2)
			for _, inv := range []struct{ script, args string }{{tc.script, tc.args}, {"sleep", `{"ms":5000}`}} {
				go func() {
					rec := newRecorder()
					out, err := execute(t, p, newInvocation(inv.script, inv.args, rec), 10*time.Second)
					results <- res{out, err, rec}
				}()
			}
			for range 2 {
				r := <-results
				if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.ExitCode != tc.code || r.out.Signal != "" {
					t.Fatalf("got %+v, %v; want escaped with exit code %d", r.out, r.err, tc.code)
				}
				if got := r.rec.log(); got != "settled process_exited" {
					t.Fatalf("reporter calls %q", got)
				}
			}
			<-p.Done()
			if st := p.ExitStatus(); st.Code != tc.code {
				t.Fatalf("exit status %v", st)
			}
		})
	}
}

func TestBunKillGivesTheSignal(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	done := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, p, newInvocation("never", `{}`, newRecorder()), 10*time.Second)
		done <- out
	}()
	time.Sleep(100 * time.Millisecond)
	p.Kill()
	out := <-done
	if out.Kind != engine.OutcomeEscaped || out.Signal != "SIGKILL" || out.ExitCode != -1 {
		t.Fatalf("got %+v", out)
	}
	if got := p.ExitStatus().String(); got != "process terminated by signal: SIGKILL" {
		t.Fatalf("exit status %q", got)
	}
}

// Abandonment (deviation D2): Execute returns the context's cause at once,
// the harness gets a cancel, and the call's end arrives later as the final
// late result, then the settlement.
func TestBunCancelAbandonsAndSettlesLate(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	rec := newRecorder()
	ctx, cancel := context.WithTimeoutCause(context.Background(), 50*time.Millisecond, engine.CancelTimeout)
	defer cancel()
	start := time.Now()
	_, err := p.Execute(ctx, newInvocation("sleep", `{"ms":1500}`, rec))
	if !errors.Is(err, engine.CancelTimeout) || time.Since(start) > time.Second {
		t.Fatalf("got %v after %s", err, time.Since(start))
	}
	if in, ab := p.Load(); in != 0 || ab != 1 {
		t.Fatalf("load %d in flight, %d abandoned", in, ab)
	}
	h, err := p.Ping(context.Background())
	if err != nil || h == nil || h.Abandoned != 1 || h.InFlight != 0 {
		t.Fatalf("pong %+v, %v", h, err)
	}
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s", k)
	}
	if got := rec.log(); got != "late result final=true 1500; settled completed" {
		t.Fatalf("reporter calls %q", got)
	}
	if in, ab := p.Load(); in != 0 || ab != 0 {
		t.Fatalf("load after settlement %d, %d", in, ab)
	}
}

// A cancel queued while a large invoke is still being written follows its
// last part, so the harness knows the invocation when the cancel arrives
// and abandons it. Ahead of the invoke it would name an unknown invocation
// and be ignored, and the harness would report the call as in flight.
func TestBunCancelDuringLargeInvoke(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	rec := newRecorder()
	ctx, cancel := context.WithCancelCause(context.Background())
	inv := newInvocation("sleep", `{"ms":1500,"pad":"`+strings.Repeat("y", 24<<20)+`"}`, rec)
	done := make(chan error, 1)
	go func() {
		_, err := p.Execute(ctx, inv)
		done <- err
	}()
	// Cancel as soon as it is dispatched, while its parts are still going
	// out.
	for in, _ := p.Load(); in == 0; in, _ = p.Load() {
		time.Sleep(50 * time.Microsecond)
	}
	cancel(engine.CancelCanceled)
	if err := <-done; !errors.Is(err, engine.CancelCanceled) {
		t.Fatalf("got %v", err)
	}
	// A ping is about no invocation, so it may overtake the invoke; ask
	// until the harness has the invocation.
	deadline := time.Now().Add(5 * time.Second)
	for {
		h, err := p.Ping(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if h.InFlight+h.Abandoned == 1 {
			if h.Abandoned != 1 {
				t.Fatalf("the harness runs the invocation as in flight: %+v", h)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the harness never had the invocation: %+v", h)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if k := rec.waitSettled(t, 10*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s", k)
	}
	if got := rec.log(); got != "late result final=true 1500; settled completed" {
		t.Fatalf("reporter calls %q", got)
	}
}

func TestBunNotDispatched(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		edit func(*engine.Invocation)
		want string
	}{
		"canceled before dispatch": {canceled, func(*engine.Invocation) {}, "context canceled"},
		"native boundary":          {context.Background(), func(i *engine.Invocation) { i.Boundary = engine.BoundaryNative }, "no native boundary"},
		"fresh mode":               {context.Background(), func(i *engine.Invocation) { i.ModuleState = engine.ModuleStateFresh }, "C3b"},
		"invalid entrypoint":       {context.Background(), func(i *engine.Invocation) { i.Entry = "not-a-name" }, "not a valid name"},
		"env not UTF-8":            {context.Background(), func(i *engine.Invocation) { i.Env = map[string]string{"X": "\xff"} }, "invalid_utf8"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := newRecorder()
			inv := newInvocation("echo", `{}`, rec)
			tc.edit(&inv)
			_, err := p.Execute(tc.ctx, inv)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if got := rec.log(); got != "settled not_started" {
				t.Fatalf("reporter calls %q", got)
			}
		})
	}
	// The process still serves.
	if out, err := execute(t, p, newInvocation("echo", `{}`, newRecorder()), 5*time.Second); err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
}

func TestBunMessageLimit(t *testing.T) {
	cfg, _ := bunConfig(t)
	cfg.MaxMessage = 2 << 20
	p := spawn(t, cfg)
	// The engine refuses to send a message over the limit.
	rec := newRecorder()
	_, err := execute(t, p, newInvocation("length", `{"data":"`+strings.Repeat("y", 3<<20)+`"}`, rec), 5*time.Second)
	if !IsProtocolError(err, CodeMessageTooLarge) || rec.log() != "settled not_started" {
		t.Fatalf("got %v, calls %q", err, rec.log())
	}
	// The harness refuses to send a result over it, and reports why.
	out, err := execute(t, p, newInvocation("big", `{"bytes":3145728}`, newRecorder()), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || !strings.Contains(out.Tail, "maximum message size of 2097152 bytes") {
		t.Fatalf("got %+v, %v", out, err)
	}
	// Under the limit, both directions go as part streams.
	out, err = execute(t, p, newInvocation("length", `{"data":"`+strings.Repeat("y", 1<<20+100)+`"}`, newRecorder()), 5*time.Second)
	if err != nil || string(out.Body.Producer()) != strconv.Itoa(1<<20+100) {
		t.Fatalf("got %+v, %v", out, err)
	}
	out, err = execute(t, p, newInvocation("big", `{"bytes":1500000}`, newRecorder()), 5*time.Second)
	if err != nil || len(out.Body.Producer()) != 1500002 {
		t.Fatalf("got %d bytes, %v", len(out.Body.Producer()), err)
	}
}

// ADR 0004 failure model item 5: a private working directory per spawned
// process, removed with it; stdout and stderr go to the diagnostics, never
// into a frame (WR-23).
func TestBunWorkingDirectoryAndStdio(t *testing.T) {
	cfg, diag := bunConfig(t)
	p := spawn(t, cfg)
	out, err := execute(t, p, newInvocation("process-info", `{}`, newRecorder()), 5*time.Second)
	var info struct {
		PID int
		Cwd string
	}
	if err != nil || json.Unmarshal(out.Body.Producer(), &info) != nil {
		t.Fatalf("got %+v, %v", out, err)
	}
	if info.PID != p.PID() || !sameDir(t, info.Cwd, p.Dir()) {
		t.Fatalf("step ran in pid %d, %s; the process is %d in %s", info.PID, info.Cwd, p.PID(), p.Dir())
	}
	st, err := os.Stat(p.Dir())
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("working directory %v, %v", st, err)
	}
	if out, err := execute(t, p, newInvocation("print", `{}`, newRecorder()), 5*time.Second); err != nil || string(out.Body.Producer()) != `"printed"` {
		t.Fatalf("got %+v, %v", out, err)
	}
	// The lines arrive through pipes, asynchronously to the frames. Bun
	// colours console.error because the base environment has FORCE_COLOR=1
	// (WR-22), so the stderr line is matched around its escapes.
	pid := strconv.Itoa(p.PID())
	for _, want := range [][2]string{
		{"harness pid " + pid + " stdout: ", "printed on stdout"},
		{"harness pid " + pid + " stderr: ", "printed on stderr"},
		{"harness pid " + pid + " stdout: ", `{"t":"result","id":"forged"`},
	} {
		deadline := time.Now().Add(2 * time.Second)
		for !hasLine(diag.String(), want[0], want[1]) {
			if time.Now().After(deadline) {
				t.Fatalf("diagnostics lack a line %q…%q:\n%s", want[0], want[1], diag.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := p.Drain(context.Background()); err != nil || !p.Bye() || p.ExitStatus().Code != 0 {
		t.Fatalf("drain %v, bye %v, status %v", err, p.Bye(), p.ExitStatus())
	}
	if _, err := os.Stat(p.Dir()); !os.IsNotExist(err) {
		t.Fatalf("working directory survives the process: %v", err)
	}
}

func hasLine(text, prefix, sub string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) && strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

func sameDir(t *testing.T, a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// Case watchdog/finite-cpu of ADR 0004, warm mode, which C3b gates: a step
// that computes for 2 s with killAfter 1 s gets its process killed after
// about 1 s; it and a co-resident invocation awaiting a timer fail as
// escaped by SIGKILL; the next invocation runs on a replacement. With the
// watchdog off the step completes.
func TestBunWatchdogFiniteCPU(t *testing.T) {
	t.Run("killAfter 1s", func(t *testing.T) {
		cfg, diag := bunConfig(t)
		cfg.KillAfter = time.Second
		pool := newPool(t, PoolConfig{Process: cfg})
		first := pidOf(t, pool, "warm-up")
		type res struct {
			out engine.StepOutcome
			err error
		}
		results := make(chan res, 2)
		start := time.Now()
		for _, inv := range []struct{ script, args string }{{"sleep", `{"ms":5000}`}, {"spin", `{"ms":2000}`}} {
			go func() {
				out, err := execute(t, pool, newInvocation(inv.script, inv.args, newRecorder()), 10*time.Second)
				results <- res{out, err}
			}()
			time.Sleep(50 * time.Millisecond)
		}
		for range 2 {
			r := <-results
			if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.Signal != "SIGKILL" {
				t.Fatalf("got %+v, %v; want escaped by SIGKILL", r.out, r.err)
			}
		}
		// Silence starts at the last frame before the spin, which can be a
		// heartbeat up to a quarter of killAfter earlier.
		if d := time.Since(start); d < 700*time.Millisecond || d > 2500*time.Millisecond {
			t.Fatalf("killed after %s, want about 1s", d)
		}
		if !strings.Contains(diag.String(), "watchdog: no frame for") {
			t.Fatalf("no watchdog diagnostic:\n%s", diag.String())
		}
		if next := pidOf(t, pool, "after"); next == first {
			t.Fatalf("the next invocation ran in the killed process %d", first)
		}
	})
	t.Run("watchdog off", func(t *testing.T) {
		cfg, _ := bunConfig(t)
		cfg.KillAfter = WatchdogOff
		cfg.Heartbeat = 100 * time.Millisecond
		pool := newPool(t, PoolConfig{Process: cfg})
		out, err := execute(t, pool, newInvocation("spin", `{"ms":2000}`, newRecorder()), 10*time.Second)
		if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != "2000" {
			t.Fatalf("got %+v, %v", out, err)
		}
	})
}

func newPool(t *testing.T, cfg PoolConfig) *Pool {
	t.Helper()
	pool, err := NewPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool.Close(ctx)
	})
	return pool
}

// pidOf runs the process-info step for run and returns the pid it ran in.
func pidOf(t *testing.T, x engine.StepExecutor, run string) int {
	t.Helper()
	inv := newInvocation("process-info", `{}`, newRecorder())
	inv.Run = run
	out, err := execute(t, x, inv, 10*time.Second)
	var info struct{ PID int }
	if err != nil || out.Kind != engine.OutcomeOK || json.Unmarshal(out.Body.Producer(), &info) != nil {
		t.Fatalf("process-info: %+v, %v", out, err)
	}
	return info.PID
}

// ADR 0004 §Cancellation item 4: an abandoned invocation taints its
// process, which gets no new invocations; with nothing else in flight it
// is drained and replaced, which settles the abandoned call.
func TestBunPoolRecyclesATaintedProcess(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg})
	first := pidOf(t, pool, "r")
	rec := newRecorder()
	ctx, cancel := context.WithTimeoutCause(context.Background(), 50*time.Millisecond, engine.CancelTimeout)
	defer cancel()
	inv := newInvocation("sleep", `{"ms":3000}`, rec)
	inv.Run = "r"
	if _, err := pool.Execute(ctx, inv); !errors.Is(err, engine.CancelTimeout) {
		t.Fatalf("got %v", err)
	}
	if k := rec.waitSettled(t, 3*time.Second); k != engine.SettleProcessExited {
		t.Fatalf("settled %s; the recycle should end the call", k)
	}
	if next := pidOf(t, pool, "r"); next == first {
		t.Fatalf("the tainted process %d got a new invocation", first)
	}
}

// A tainted process with other work in flight keeps it and gets no new
// invocations (a new process takes them). Once its abandoned invocation
// settles it returns to service when the pool has room below Max, and
// leaves once idle when it has not.
func TestBunPoolTaintedProcessReturnsToService(t *testing.T) {
	for _, tc := range []struct {
		max  int
		back bool
	}{{2, true}, {1, false}} {
		t.Run(fmt.Sprintf("max %d", tc.max), func(t *testing.T) {
			cfg, _ := bunConfig(t)
			pool := newPool(t, PoolConfig{Process: cfg, Max: tc.max})
			first := pidOf(t, pool, "a")
			long := make(chan engine.StepOutcome, 1)
			go func() {
				out, _ := execute(t, pool, newInvocation("sleep", `{"ms":1500}`, newRecorder()), 10*time.Second)
				long <- out
			}()
			time.Sleep(100 * time.Millisecond)
			rec := newRecorder()
			ctx, cancel := context.WithTimeoutCause(context.Background(), 50*time.Millisecond, engine.CancelTimeout)
			defer cancel()
			if _, err := pool.Execute(ctx, newInvocation("sleep", `{"ms":300}`, rec)); !errors.Is(err, engine.CancelTimeout) {
				t.Fatalf("got %v", err)
			}
			if other := pidOf(t, pool, "b"); other == first {
				t.Fatalf("a new invocation went to the tainted process %d", first)
			}
			if k := rec.waitSettled(t, 3*time.Second); k != engine.SettleCompleted {
				t.Fatalf("settled %s", k)
			}
			var st *ProcessStats
			for _, s := range pool.Stats() {
				if s.PID == first {
					st = &s
				}
			}
			if st == nil || st.Tainted || st.Surplus != !tc.back {
				t.Fatalf("process %d after its taint cleared: %+v", first, pool.Stats())
			}
			if out := <-long; out.Kind != engine.OutcomeOK {
				t.Fatalf("the co-resident invocation got %+v", out)
			}
			if tc.back {
				return
			}
			// The surplus process leaves once idle; the pool is back to Max.
			deadline := time.Now().Add(5 * time.Second)
			for len(pool.Stats()) != 1 {
				if time.Now().After(deadline) {
					t.Fatalf("still %+v", pool.Stats())
				}
				time.Sleep(20 * time.Millisecond)
			}
			if pool.Stats()[0].PID == first {
				t.Fatal("the surplus process stayed and the other left")
			}
		})
	}
}

func TestBunPoolReplacesADeadProcess(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg})
	first := pidOf(t, pool, "r")
	out, err := execute(t, pool, newInvocation("exit", `{"code":3}`, newRecorder()), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 3 {
		t.Fatalf("got %+v, %v", out, err)
	}
	if next := pidOf(t, pool, "r"); next == first {
		t.Fatal("ran in the dead process")
	}
	if n := len(pool.Stats()); n != 1 {
		t.Fatalf("%d processes, want 1", n)
	}
}

// A CPU-bound step makes its process lag; with room below Max the pool
// starts another process for the next invocation.
func TestBunPoolScalesOutOnLag(t *testing.T) {
	cfg, _ := bunConfig(t)
	cfg.KillAfter = WatchdogOff
	cfg.Heartbeat = 50 * time.Millisecond
	pool := newPool(t, PoolConfig{Process: cfg, Max: 2, LagThreshold: 150 * time.Millisecond})
	first := pidOf(t, pool, "r1")
	spun := make(chan time.Time, 1)
	go func() {
		execute(t, pool, newInvocation("spin", `{"ms":2500}`, newRecorder()), 10*time.Second)
		spun <- time.Now()
	}()
	time.Sleep(500 * time.Millisecond)
	second := pidOf(t, pool, "r2")
	answered := time.Now()
	if second == first {
		t.Fatal("the invocation went to the lagging process")
	}
	if end := <-spun; !answered.Before(end) {
		t.Fatal("the second invocation waited for the spin")
	}
}

func TestBunPoolAffinity(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg, Min: 2, Max: 2})
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := pool.Stats()
	if len(stats) != 2 {
		t.Fatalf("%d processes after Start, want 2", len(stats))
	}
	// Occupy one process, so run r1 lands on the other.
	hold := func(run string) chan struct{} {
		done := make(chan struct{})
		inv := newInvocation("sleep", `{"ms":600}`, newRecorder())
		inv.Run = run
		go func() {
			execute(t, pool, inv, 10*time.Second)
			close(done)
		}()
		time.Sleep(100 * time.Millisecond)
		return done
	}
	d := hold("r0")
	p1 := pidOf(t, pool, "r1")
	<-d
	// Now occupy r1's process: least-loaded would pick the other one, but
	// r1 goes back to its own.
	inv := newInvocation("sleep", `{"ms":600}`, newRecorder())
	inv.Run = "r1"
	d = make(chan struct{})
	go func() {
		execute(t, pool, inv, 10*time.Second)
		close(d)
	}()
	time.Sleep(100 * time.Millisecond)
	if again := pidOf(t, pool, "r1"); again != p1 {
		t.Fatalf("run r1 moved from %d to %d", p1, again)
	}
	<-d
}

// Deviation D15: a cap bounds the invocations in flight per process; the
// rest wait.
func TestBunPoolCap(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg, Cap: 1})
	pidOf(t, pool, "warm-up")
	var wg sync.WaitGroup
	start := time.Now()
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			execute(t, pool, newInvocation("sleep", `{"ms":500}`, newRecorder()), 10*time.Second)
		}()
	}
	wg.Wait()
	if d := time.Since(start); d < time.Second {
		t.Fatalf("two capped invocations took %s, want them one after the other", d)
	}
	// Without the cap they run at once.
	free := newPool(t, PoolConfig{Process: cfg})
	pidOf(t, free, "warm-up")
	start = time.Now()
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			execute(t, free, newInvocation("sleep", `{"ms":500}`, newRecorder()), 10*time.Second)
		}()
	}
	wg.Wait()
	if d := time.Since(start); d > 950*time.Millisecond {
		t.Fatalf("two uncapped invocations took %s", d)
	}
}

func TestBunPoolClose(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool, err := NewPool(PoolConfig{Process: cfg})
	if err != nil {
		t.Fatal(err)
	}
	pidOf(t, pool, "r")
	done := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, pool, newInvocation("sleep", `{"ms":300}`, newRecorder()), 10*time.Second)
		done <- out
	}()
	time.Sleep(50 * time.Millisecond)
	if err := pool.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Close waited for the invocation, then drained the process.
	if out := <-done; out.Kind != engine.OutcomeOK {
		t.Fatalf("in-flight invocation got %+v", out)
	}
	// The drained process said bye and left the pool.
	deadline := time.Now().Add(2 * time.Second)
	for len(pool.Stats()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("processes left: %+v", pool.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := execute(t, pool, newInvocation("echo", `{}`, newRecorder()), time.Second); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("got %v", err)
	}
}

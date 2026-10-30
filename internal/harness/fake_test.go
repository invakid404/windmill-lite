package harness

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The fake harness is this test binary run with WL_FAKE_HARNESS=<mode>. It
// speaks the protocol on fd 3 and misbehaves in the ways the real harness
// cannot be made to on demand: a reader that stops reading, a mute peer,
// outcomes fixed before the call ends, late output, protocol violations.
func TestMain(m *testing.M) {
	if mode := os.Getenv("WL_FAKE_HARNESS"); mode != "" {
		os.Exit(fakeHarness(mode))
	}
	if os.Getenv("WL_WAIT_ALL") != "" {
		os.Exit(waitAll())
	}
	os.Exit(m.Run())
}

func fakeConfig(t testing.TB, mode string) (ProcessConfig, *syncBuffer) {
	diag := &syncBuffer{}
	return ProcessConfig{
		Path:        os.Args[0],
		Args:        []string{"-test.run=^$"},
		Env:         append(BaseEnv(), "WL_FAKE_HARNESS="+mode),
		WorkRoot:    t.TempDir(),
		Kind:        "fake",
		Runtime:     "fake-1",
		Bundle:      "sha256:fake",
		Diagnostics: diag,
	}, diag
}

func fakeHarness(mode string) int {
	f := os.NewFile(3, "engine")
	nc, err := net.FileConn(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake: fd 3:", err)
		return 70
	}
	var sendMu sync.Mutex
	send := func(m Message) {
		p, err := Encode(m)
		if err != nil {
			panic(err)
		}
		sendMu.Lock()
		defer sendMu.Unlock()
		if _, err := nc.Write(appendFrame(nil, p)); err != nil {
			os.Exit(71)
		}
	}
	recv := func() Message {
		p, err := readFrame(nc, MaxFrame)
		if err != nil {
			os.Exit(0)
		}
		m, err := Decode(p)
		if err != nil {
			panic(err)
		}
		return m
	}
	pid := os.Getpid()
	switch mode {
	case "no-hello":
		return 9
	case "bad-pid":
		pid++
	case "slow-hello":
		time.Sleep(time.Second)
	}
	hello := Hello{Proto: ProtocolVersion, Harness: HarnessInfo{"fake", "0"}, Runtime: "fake-1", Bundle: "sha256:fake", PID: pid}
	if mode == "fresh-unread-pong" || mode == "fresh-hang" {
		hello.Caps.Fresh = true
	}
	send(hello)
	w := recv().(Welcome)
	if w.Reject != nil {
		return 2
	}
	heartbeat := time.Duration(w.Config.HeartbeatMS) * time.Millisecond
	switch mode {
	case "silent-reader":
		// Heartbeats on its own and never reads again: the engine's writes
		// back up while frames keep arriving from this side.
		for i := 1; ; i++ {
			send(Ping{ID: fmt.Sprintf("h%d", i), Health: &Health{}})
			time.Sleep(heartbeat)
		}
	case "mute":
		for {
			recv()
		}
	case "exit-leaving-child":
		// A child keeps the socket and stdout open after this process
		// exits, so the engine sees no end of stream for a while.
		child := exec.Command("/bin/sleep", "3")
		child.Stdout = os.Stdout
		child.ExtraFiles = []*os.File{f}
		if err := child.Start(); err != nil {
			return 72
		}
		time.Sleep(50 * time.Millisecond)
		return 5
	case "heartbeat-and-answer":
		go func() {
			for i := 1; ; i++ {
				send(Ping{ID: fmt.Sprintf("h%d", i), Health: &Health{}})
				time.Sleep(heartbeat)
			}
		}()
	}
	for {
		m := recv()
		switch m := m.(type) {
		case Cancel:
			if mode == "settle-on-cancel" {
				// The abandoned call ends the moment its cancel arrives.
				send(Late{ID: m.ID, Kind: LateError, Body: jsonval.FromProducer([]byte(`{"message":"canceled"}`)), Settled: true})
			}
			continue
		case Drain:
			if mode == "exit-leaving-stdout" {
				send(Bye{})
				return 0
			}
			continue
		}
		inv, ok := m.(Invoke)
		if !ok {
			continue
		}
		body := jsonval.FromProducer([]byte(`"done"`))
		switch mode {
		case "settle-on-cancel", "fresh-hang":
			// The call runs until it is canceled, or its process killed.
		case "exit-leaving-stdout":
			// Exits with the invocation in flight, while a child keeps
			// stdout (but not the socket) open, so the engine sees the end of
			// the stream at once and then waits for the pipe.
			f.Close()
			child := exec.Command("/bin/sleep", "3")
			child.Stdout = os.Stdout
			if err := child.Start(); err != nil {
				return 72
			}
			return 5
		case "unsettled":
			// The outcome is fixed at once while the call goes on, then
			// the call ends.
			send(Result{ID: inv.ID, Outcome: OutcomeEscaped, ExitCode: 3, Tail: "exit 3", Settled: false})
			time.Sleep(100 * time.Millisecond)
			send(Late{ID: inv.ID, Kind: LateError, Body: jsonval.FromProducer([]byte(`{"message":"ended"}`)), Settled: true})
		case "d9":
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
			time.Sleep(50 * time.Millisecond)
			send(Late{ID: inv.ID, Kind: LateError, Body: jsonval.FromProducer([]byte(`{"message":"timer"}`))})
			send(Log{ID: inv.ID, Lines: []string{"after"}})
		case "late-before-result":
			send(Late{ID: inv.ID, Kind: LateLog, Body: jsonval.FromProducer([]byte(`[]`))})
		case "double-result":
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: false})
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
		case "logs-then-exit":
			send(Log{ID: inv.ID, Lines: []string{"first line", strings.Repeat("é", 700)}})
			send(Log{ID: inv.ID, Lines: []string{"last line"}})
			time.Sleep(50 * time.Millisecond)
			return 5
		case "heartbeat-and-answer":
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
			send(Late{ID: inv.ID, Kind: LateError, Body: jsonval.FromProducer([]byte(`{"message":"timer"}`))})
		case "fresh-unread-pong":
			// A fresh process whose step outlasts a heartbeat: it pings, the
			// engine answers with a pong, which this process never reads,
			// and it exits right after its result, the pong still unread.
			send(Ping{ID: "h1", Health: &Health{}})
			time.Sleep(300 * time.Millisecond)
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
			return 0
		default:
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
		}
	}
}

// A fresh process that exits right after its result, while a pong the
// engine sent in answer to its heartbeat is still unread, has its result
// delivered, settled completed. On Linux a stream socket closed with unread
// data ends its peer's reads with ECONNRESET instead of EOF; that is the
// harness's end of stream, not a failed connection, which would discard
// the held result as killed by SIGKILL (FU8: the 3 s steps of
// TestFlowSuite/fresh failed that way on ubuntu under -race).
func TestFreshExitWithAnUnreadPong(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-unread-pong")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	rec := newRecorder()
	out, err := execute(t, p, freshInvocation("x", `{}`, rec), 10*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != `"done"` {
		t.Fatalf("got %+v, %v; diagnostics %q", out, err, diag.String())
	}
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s; calls %s", k, rec.log())
	}
	<-p.Done()
	p.mu.Lock()
	reason := p.killReason
	p.mu.Unlock()
	if reason != "" {
		t.Fatalf("the process was killed: %s; diagnostics %q", reason, diag.String())
	}
}

// Every kill but the watchdog's, which says why on its own, is named in the
// diagnostics, so a step that ends as killed by SIGKILL says by what (FU8).
func TestKillIsNamed(t *testing.T) {
	cfg, diag := fakeConfig(t, "ok")
	p := spawn(t, cfg)
	p.Kill()
	<-p.Done()
	waitFor(t, "the kill's diagnostic line", func() bool {
		return strings.Contains(diag.String(), "killing the harness: killed by the engine")
	})
}

// blockedWriter is a diagnostics writer whose writes block until it is
// opened, as a full stderr pipe or a stalled host logger does.
type blockedWriter struct {
	writes atomic.Int32
	opened chan struct{}
	once   sync.Once
}

func newBlockedWriter() *blockedWriter { return &blockedWriter{opened: make(chan struct{})} }

func (w *blockedWriter) Write(b []byte) (int, error) {
	w.writes.Add(1)
	<-w.opened
	return len(b), nil
}

func (w *blockedWriter) open() { w.once.Do(func() { close(w.opened) }) }

// Naming a kill never holds it up (FU8 review): with the diagnostics writer
// blocked, Kill returns and the process ends, its line still waiting for
// the writer.
func TestKillWithBlockedDiagnostics(t *testing.T) {
	cfg, _ := fakeConfig(t, "ok")
	w := newBlockedWriter()
	cfg.Diagnostics = w
	p := spawn(t, cfg)
	t.Cleanup(w.open)
	killed := make(chan struct{})
	go func() {
		p.Kill()
		close(killed)
	}()
	select {
	case <-killed:
	case <-time.After(5 * time.Second):
		t.Fatal("Kill waited for the blocked diagnostics writer")
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the process did not end while the diagnostics writer was blocked")
	}
	waitFor(t, "the kill's line to reach the writer", func() bool { return w.writes.Load() > 0 })
}

// A fresh invocation's cancellation kills its process, whose kill is named,
// and returns at once, never waiting for the diagnostics writer (FU8
// review; WR-37: Windmill kills a job's process at once on a timeout or a
// cancellation).
func TestFreshCancelWithBlockedDiagnostics(t *testing.T) {
	cfg, _ := fakeConfig(t, "fresh-hang")
	cfg.ModuleState = engine.ModuleStateFresh
	w := newBlockedWriter()
	cfg.Diagnostics = w
	p := spawn(t, cfg)
	t.Cleanup(w.open)
	returned := make(chan error, 1)
	go func() {
		_, err := execute(t, p, freshInvocation("x", `{}`, newRecorder()), time.Second)
		returned <- err
	}()
	select {
	case err := <-returned:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Execute returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the canceled invocation waited for the blocked diagnostics writer")
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the process did not end while the diagnostics writer was blocked")
	}
	waitFor(t, "the kill's line to reach the writer", func() bool { return w.writes.Load() > 0 })
	p.mu.Lock()
	reason := p.killReason
	p.mu.Unlock()
	if !strings.HasPrefix(reason, "invocation ") {
		t.Fatalf("kill reason %q", reason)
	}
}

// cl P4-3: silence is measured from frames received, so a harness that
// keeps sending heartbeats is not killed while the engine's own writes are
// blocked behind a large message it does not read.
func TestWatchdogIgnoresEngineWriteBackpressure(t *testing.T) {
	cfg, diag := fakeConfig(t, "silent-reader")
	cfg.KillAfter = 400 * time.Millisecond
	cfg.Heartbeat = 50 * time.Millisecond
	p := spawn(t, cfg)
	big := `{"data":"` + strings.Repeat("y", 32<<20) + `"}`
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Execute(ctx, newInvocation("echo", big, rec))
	select {
	case <-p.Done():
		t.Fatalf("killed although frames kept arriving: %s", diag.String())
	case <-time.After(1500 * time.Millisecond):
	}
	if p.Silence() > 300*time.Millisecond {
		t.Fatalf("silence %s although the fake heartbeats every 50ms", p.Silence())
	}
}

// A harness that sends nothing is killed after KillAfter, and its
// in-flight invocations fail as escaped with the signal.
func TestWatchdogKillsMuteHarness(t *testing.T) {
	cfg, diag := fakeConfig(t, "mute")
	cfg.KillAfter = 300 * time.Millisecond
	p := spawn(t, cfg)
	rec := newRecorder()
	start := time.Now()
	out, err := execute(t, p, newInvocation("echo", `{}`, rec), 5*time.Second)
	elapsed := time.Since(start)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.Signal != "SIGKILL" || out.ExitCode != -1 {
		t.Fatalf("got %+v, %v; want escaped by SIGKILL", out, err)
	}
	if elapsed < 200*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("killed after %s, want about 300ms", elapsed)
	}
	if got := rec.log(); got != "settled process_exited" {
		t.Fatalf("reporter calls %q", got)
	}
	if !strings.Contains(diag.String(), "watchdog: no frame for") || !strings.Contains(diag.String(), "invocation inv-") {
		t.Fatalf("diagnostics %q lack the watchdog line naming the invocation", diag.String())
	}
}

// An outcome fixed while the call goes on (settled false) returns at once;
// the invocation settles when the call's final late frame arrives.
func TestUnsettledResultSettlesLater(t *testing.T) {
	cfg, _ := fakeConfig(t, "unsettled")
	p := spawn(t, cfg)
	rec := newRecorder()
	out, err := execute(t, p, newInvocation("x", `{}`, rec), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 3 || out.Tail != "exit 3" {
		t.Fatalf("got %+v, %v", out, err)
	}
	if got := rec.log(); got != "" {
		t.Fatalf("reporter called before the call ended: %q", got)
	}
	if k := rec.waitSettled(t, 2*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s", k)
	}
	if got := rec.log(); got != `late error final=true {"message":"ended"}; settled completed` {
		t.Fatalf("reporter calls %q", got)
	}
}

// Output of work a settled call left running (D9) still reaches its
// reporter, after Settled and never final.
func TestLateOutputAfterSettlement(t *testing.T) {
	cfg, _ := fakeConfig(t, "d9")
	p := spawn(t, cfg)
	rec := newRecorder()
	out, err := execute(t, p, newInvocation("x", `{}`, rec), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != `"done"` {
		t.Fatalf("got %+v, %v", out, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	want := `settled completed; late error final=false {"message":"timer"}; late log final=false ["after"]`
	for rec.log() != want {
		if time.Now().After(deadline) {
			t.Fatalf("reporter calls %q, want %q", rec.log(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Protocol violations kill the harness; what was in flight fails as
// escaped with the signal.
func TestProtocolViolationsKillTheHarness(t *testing.T) {
	for _, mode := range []string{"late-before-result", "double-result"} {
		t.Run(mode, func(t *testing.T) {
			cfg, diag := fakeConfig(t, mode)
			p := spawn(t, cfg)
			rec := newRecorder()
			out, err := execute(t, p, newInvocation("x", `{}`, rec), 5*time.Second)
			if mode == "double-result" {
				// The first result returned; the second kills the process,
				// which settles the still-running call.
				if err != nil || out.Kind != engine.OutcomeOK {
					t.Fatalf("got %+v, %v", out, err)
				}
				if k := rec.waitSettled(t, 2*time.Second); k != engine.SettleProcessExited {
					t.Fatalf("settled %s", k)
				}
			} else if err != nil || out.Kind != engine.OutcomeEscaped || out.Signal != "SIGKILL" {
				t.Fatalf("got %+v, %v", out, err)
			}
			<-p.Done()
			if !strings.Contains(diag.String(), "protocol error, killing the harness: harness protocol: unexpected_message") {
				t.Fatalf("diagnostics %q", diag.String())
			}
		})
	}
}

// A process that exits fails what is in flight with its exit code and the
// invocation's log tail: the last TailLength characters of its lines.
func TestExitCodeAndTail(t *testing.T) {
	cfg, _ := fakeConfig(t, "logs-then-exit")
	p := spawn(t, cfg)
	rec := newRecorder()
	out, err := execute(t, p, newInvocation("x", `{}`, rec), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 5 || out.Signal != "" {
		t.Fatalf("got %+v, %v", out, err)
	}
	want := strings.Repeat("é", TailLength-len("\nlast line")) + "\nlast line"
	if out.Tail != want {
		t.Fatalf("tail %q (%d runes), want the last %d characters", out.Tail, len([]rune(out.Tail)), TailLength)
	}
}

func TestSpawnFailures(t *testing.T) {
	t.Run("exit before hello", func(t *testing.T) {
		cfg, _ := fakeConfig(t, "no-hello")
		_, err := Spawn(context.Background(), cfg)
		if err == nil || !strings.Contains(err.Error(), "exited before its hello (exit code 9)") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("pid mismatch", func(t *testing.T) {
		cfg, _ := fakeConfig(t, "bad-pid")
		_, err := Spawn(context.Background(), cfg)
		var he *HandshakeError
		if !errors.As(err, &he) || he.Code != RejectPID {
			t.Fatalf("got %v, want %s", err, RejectPID)
		}
	})
	t.Run("no such program", func(t *testing.T) {
		cfg, _ := fakeConfig(t, "ok")
		cfg.Path = "/nonexistent/harness"
		if _, err := Spawn(context.Background(), cfg); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("handshake timeout", func(t *testing.T) {
		cfg, _ := fakeConfig(t, "ok")
		cfg.Path, cfg.Args = "/bin/sleep", []string{"5"}
		cfg.HandshakeTimeout = 200 * time.Millisecond
		start := time.Now()
		_, err := Spawn(context.Background(), cfg)
		if err == nil || !strings.Contains(err.Error(), "no hello within") || time.Since(start) > 3*time.Second {
			t.Fatalf("got %v after %s", err, time.Since(start))
		}
	})
	t.Run("fresh mode without the capability", func(t *testing.T) {
		// The fake offers no fresh capability, so it cannot be a fresh
		// process: it is refused in the handshake.
		cfg, _ := fakeConfig(t, "ok")
		cfg.ModuleState = engine.ModuleStateFresh
		var he *HandshakeError
		if _, err := Spawn(context.Background(), cfg); !errors.As(err, &he) || he.Code != RejectHarness || !strings.Contains(he.Message, "fresh capability") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unknown module mode", func(t *testing.T) {
		cfg, _ := fakeConfig(t, "ok")
		cfg.ModuleState = "shared"
		if _, err := Spawn(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), `unknown module-state mode "shared"`) {
			t.Fatalf("got %v", err)
		}
	})
}

// A pool whose process cannot start fails the invocation without
// dispatching it, and logs why.
func TestPoolSpawnFailure(t *testing.T) {
	cfg, diag := fakeConfig(t, "no-hello")
	pool, err := NewPool(PoolConfig{Process: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close(context.Background())
	rec := newRecorder()
	_, err = execute(t, pool, newInvocation("x", `{}`, rec), 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exit code 9") {
		t.Fatalf("got %v", err)
	}
	if got := rec.log(); got != "settled not_started" {
		t.Fatalf("reporter calls %q", got)
	}
	if !strings.Contains(diag.String(), "harness pool: starting a process failed") {
		t.Fatalf("diagnostics %q", diag.String())
	}
}

func TestPoolConfigValidation(t *testing.T) {
	cfg, _ := fakeConfig(t, "ok")
	for name, pc := range map[string]PoolConfig{
		"min above max":       {Process: cfg, Min: 3, Max: 2},
		"negative cap":        {Process: cfg, Cap: -1},
		"key disagrees":       {Process: cfg, Key: Key{Class: "bun"}},
		"unknown module mode": {Process: cfg, Key: Key{ModuleState: "shared"}},
		"modes disagree":      {Process: func() ProcessConfig { c := cfg; c.ModuleState = engine.ModuleStateWarm; return c }(), Key: Key{ModuleState: engine.ModuleStateFresh}},
	} {
		if _, err := NewPool(pc); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// The key may leave the class and runtime to the process template.
	if _, err := NewPool(PoolConfig{Process: cfg}); err != nil {
		t.Fatal(err)
	}
}

// A process that has exited takes no new invocation, even while a child of
// it keeps the socket open and the end of the stream has not been seen: the
// invocation is not dispatched, rather than failing as if it had run.
func TestNoDispatchToAnExitedProcess(t *testing.T) {
	cfg, _ := fakeConfig(t, "exit-leaving-child")
	p := spawn(t, cfg)
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.mu.Lock()
		ending := p.ending
		p.mu.Unlock()
		if ending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the exit was not noticed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-p.Done():
		t.Fatal("the test needs the stream to be still open")
	default:
	}
	rec := newRecorder()
	if _, err := execute(t, p, newInvocation("x", `{}`, rec), 2*time.Second); !errors.Is(err, errUnavailable) {
		t.Fatalf("got %v, want the process unavailable", err)
	}
	if got := rec.log(); got != "settled not_started" {
		t.Fatalf("reporter calls %q", got)
	}
	<-p.Done()
	if st := p.ExitStatus(); st.Code != 5 {
		t.Fatalf("exit status %v", st)
	}
}

// A process that finishes starting after Close gave up waiting is drained,
// not left running.
func TestPoolCloseDuringStart(t *testing.T) {
	cfg, _ := fakeConfig(t, "slow-hello")
	root := cfg.WorkRoot
	pool, err := NewPool(PoolConfig{Process: cfg, RecycleTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	go pool.Start(context.Background())
	// Closed once the process has started, which it shows by its private
	// directory, and while it has not said hello (FU2: no timing sleep).
	waitFor(t, "the process to start", func() bool { entries, _ := os.ReadDir(root); return len(entries) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := pool.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close: %v", err)
	}
	// The process's private directory goes when it exits.
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(root)
		if len(entries) == 0 && len(pool.Stats()) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still running after Close: %d working directories, %+v", len(entries), pool.Stats())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Reporter calls run away from the connection's reader: a Reporter that
// blocks neither stalls other invocations nor looks like silence to the
// watchdog.
func TestSlowReporterDoesNotStallTheConnection(t *testing.T) {
	cfg, diag := fakeConfig(t, "heartbeat-and-answer")
	cfg.KillAfter = 400 * time.Millisecond
	cfg.Heartbeat = 50 * time.Millisecond
	p := spawn(t, cfg)
	slow := &blockingReporter{recorder: newRecorder(), release: make(chan struct{})}
	if out, err := execute(t, p, newInvocation("x", `{}`, slow), 2*time.Second); err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
	// Its late error now blocks in Late; the connection keeps going.
	time.Sleep(time.Second)
	if out, err := execute(t, p, newInvocation("x", `{}`, newRecorder()), 2*time.Second); err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("second invocation: %+v, %v", out, err)
	}
	select {
	case <-p.Done():
		t.Fatalf("killed while a Reporter was busy: %s", diag.String())
	default:
	}
	close(slow.release)
}

type blockingReporter struct {
	*recorder
	release chan struct{}
}

func (b *blockingReporter) Late(l engine.Late) {
	<-b.release
	b.recorder.Late(l)
}

// A Reporter may call the executor, for example to start a run when one
// settles.
func TestReporterMayCallTheExecutor(t *testing.T) {
	cfg, _ := fakeConfig(t, "ok")
	p := spawn(t, cfg)
	inner := make(chan engine.StepOutcome, 1)
	rep := &reentrantReporter{recorder: newRecorder(), call: func() {
		out, _ := execute(t, p, newInvocation("x", `{}`, newRecorder()), 2*time.Second)
		inner <- out
	}}
	if out, err := execute(t, p, newInvocation("x", `{}`, rep), 2*time.Second); err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
	if out := <-inner; out.Kind != engine.OutcomeOK {
		t.Fatalf("the Execute inside Settled got %+v", out)
	}
}

type reentrantReporter struct {
	*recorder
	call func()
}

func (r *reentrantReporter) Settled(k engine.SettleKind) {
	r.call()
	r.recorder.Settled(k)
}

func TestSpawnConfigValidation(t *testing.T) {
	for name, edit := range map[string]func(*ProcessConfig){
		"heartbeat not shorter than killAfter": func(c *ProcessConfig) { c.KillAfter, c.Heartbeat = time.Second, time.Second },
		"negative heartbeat":                   func(c *ProcessConfig) { c.Heartbeat = -1 },
		"negative message limit":               func(c *ProcessConfig) { c.MaxMessage = -1 },
	} {
		cfg, _ := fakeConfig(t, "ok")
		edit(&cfg)
		if _, err := Spawn(context.Background(), cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The engine's ends of the harness's stdout and stderr are closed a grace
// after the exit, even while a child of the harness still holds the
// writers: no goroutine or descriptor outlives the process.
func TestOutputPipesCloseAfterExit(t *testing.T) {
	cfg, _ := fakeConfig(t, "exit-leaving-child")
	start := time.Now()
	p := spawn(t, cfg)
	<-p.Done()
	// The child sleeps for 3 s; Done comes after the 1 s grace, before it.
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("Done after %s, as late as the child's exit", d)
	}
	b := make([]byte, 1<<20)
	if stack := string(b[:runtime.Stack(b, true)]); strings.Contains(stack, "harness.(*Process).copyLines") {
		t.Fatal("an output pipe is still being read after Done")
	}
	if _, err := p.outR.Stat(); err == nil {
		t.Fatal("the stdout read end is still open")
	}
}

// An abandoned invocation's slot is freed, and the pool is told of the
// abandonment, before its Reporter hears anything, however soon the call
// ends: the fake settles it the moment the cancel arrives, while freeing
// the slot takes its time. Execute returns only once both have run.
func TestAbandonmentComesBeforeTheReporter(t *testing.T) {
	cfg, _ := fakeConfig(t, "settle-on-cancel")
	rec := newRecorder()
	cfg.notify = func(*Process) { rec.note("notified") }
	p := spawn(t, cfg)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go func() {
		for in, _ := p.Load(); in == 0; in, _ = p.Load() {
			time.Sleep(time.Millisecond)
		}
		cancel(engine.CancelCanceled)
	}()
	_, d, err := p.exec(ctx, newInvocation("x", `{}`, rec), func() {
		// Time enough for a late frame that overtook the release to reach
		// the Reporter.
		time.Sleep(200 * time.Millisecond)
		rec.note("slot freed")
	})
	if d != abandoned || !errors.Is(err, engine.CancelCanceled) {
		t.Fatalf("got %v, %v", d, err)
	}
	if got := rec.log(); !strings.HasPrefix(got, "slot freed; notified") {
		t.Fatalf("calls when Execute returned: %q", got)
	}
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s", k)
	}
	if got, want := rec.log(), `slot freed; notified; late error final=true {"message":"canceled"}; notified; settled completed`; got != want {
		t.Fatalf("calls %q, want %q", got, want)
	}
}

// A process that dies leaves its pool before the Reporters of the
// invocations in it hear Settled(process_exited), as a tainted process's
// rules are applied before Settled(completed). A child of the fake keeps
// stdout open, so the engine's exit handling waits a second for the pipe
// after the end of the stream; the pool must not wait with it.
func TestDeadProcessLeavesThePoolBeforeTheReporter(t *testing.T) {
	cfg, _ := fakeConfig(t, "exit-leaving-stdout")
	pool := newPool(t, PoolConfig{Process: cfg})
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	dead := pool.Stats()[0].PID
	rec := &statsRecorder{recorder: newRecorder(), pool: pool}
	out, err := execute(t, pool, newInvocation("x", `{}`, rec), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 5 {
		t.Fatalf("got %+v, %v", out, err)
	}
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleProcessExited {
		t.Fatalf("settled %s", k)
	}
	for _, s := range rec.atSettled {
		if s.PID == dead {
			t.Fatalf("the pool still had the dead process %d when its invocation settled: %+v", dead, rec.atSettled)
		}
	}
}

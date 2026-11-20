package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"weak"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The fake harness is this test binary run with WL_FAKE_HARNESS=<mode>. It
// speaks the protocol on fd 3 and misbehaves in the ways the real harness
// cannot be made to on demand: a reader that stops reading, a mute peer,
// outcomes fixed before the call ends, late output, protocol violations.
func TestMain(m *testing.M) {
	if id := os.Getenv("WL_DRIP_CHILD"); id != "" {
		os.Exit(dripChild(id))
	}
	if mode := os.Getenv("WL_FAKE_HARNESS"); mode != "" {
		os.Exit(fakeHarness(mode))
	}
	if os.Getenv("WL_WAIT_ALL") != "" {
		os.Exit(waitAll())
	}
	if pgid := os.Getenv("WL_STRAY_PARENT"); pgid != "" {
		os.Exit(strayParent(pgid, os.Getenv("WL_STRAY_FILE")))
	}
	os.Exit(m.Run())
}

// strayParent starts /bin/sleep in the process group pgid, writes its own
// pid to file+".parent" and the child's to file, and does not wait for the
// child until file+".done" exists (or 30 s have passed): once killed, the
// child stays in that group as a zombie until then. Then it reaps the child
// and exits; once file+".orphan" exists instead, it exits without reaping
// it, so the child's zombie goes to whoever adopts orphans. Once
// file+".late" exists, it starts a second /bin/sleep in the same group and
// writes its pid to file+".late.pid": a member that joins the group after
// a kill of it.
func strayParent(pgid, file string) int {
	g, err := strconv.Atoi(pgid)
	if err != nil {
		return 2
	}
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: g}
	if err := child.Start(); err != nil {
		return 3
	}
	os.WriteFile(file+".parent", []byte(strconv.Itoa(os.Getpid())), 0o644)
	os.WriteFile(file, []byte(strconv.Itoa(child.Process.Pid)), 0o644)
	var late *exec.Cmd
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(file + ".orphan"); err == nil {
			return 0
		}
		if _, err := os.Stat(file + ".done"); err == nil {
			break
		}
		if _, err := os.Stat(file + ".late"); late == nil && err == nil {
			late = exec.Command("/bin/sleep", "30")
			late.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: g}
			if err := late.Start(); err != nil {
				return 4
			}
			os.WriteFile(file+".late.pid", []byte(strconv.Itoa(late.Process.Pid)), 0o644)
		}
	}
	for _, c := range []*exec.Cmd{child, late} {
		if c != nil {
			c.Process.Kill()
			c.Wait()
		}
	}
	return 0
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
	if mode == "fresh-unread-pong" || mode == "fresh-hang" || mode == "fresh-log-exit" || mode == "fresh-drip-exit" ||
		mode == "fresh-slow-exit" || mode == "fresh-no-exit" || mode == "fresh-no-exit-child" || mode == "fresh-term-after-end" ||
		mode == "fresh-exit-at-once" || mode == "fresh-no-exit-stray" {
		hello.Caps.Fresh = true
	}
	if mode == "plugin" {
		hello.Plugin = jsonval.FromProducer([]byte(`{"on_module_start":true}`))
	}
	send(hello)
	w := recv().(Welcome)
	if w.Reject != nil {
		return 2
	}
	heartbeat := time.Duration(w.Config.HeartbeatMS) * time.Millisecond
	switch mode {
	case "plugin":
		// Answers every host_call with the hook it was for.
		for {
			switch m := recv().(type) {
			case HostCall:
				send(HostReply{ID: m.ID, Payload: jsonval.FromProducer([]byte(`{"hook":"` + m.Hook + `","run":"` + m.Run + `"}`))})
			case Drain:
				send(Bye{})
				return 0
			}
		}
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
		case "warn-then-answer":
			// Concurrently: a warning, then the result after the arguments'
			// after_us microseconds.
			var a struct {
				AfterUS int `json:"after_us"`
			}
			json.Unmarshal(inv.Args.Producer(), &a)
			go func() {
				send(Warning{ID: inv.ID, Code: WarningUnattributed, Message: "first"})
				time.Sleep(time.Duration(a.AfterUS) * time.Microsecond)
				send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
			}()
		case "fresh-unread-pong":
			// A fresh process whose step outlasts a heartbeat: it pings, the
			// engine answers with a pong, which this process never reads,
			// and it exits right after its result, the pong still unread.
			send(Ping{ID: "h1", Health: &Health{}})
			time.Sleep(300 * time.Millisecond)
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
			return 0
		case "fresh-slow-exit", "fresh-no-exit":
			// Ends its stream right after its result, as the Bun runtime
			// does, and exits 1.5 s later, or never.
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
			nc.Close()
			f.Close()
			if mode == "fresh-no-exit" {
				time.Sleep(time.Hour)
			}
			time.Sleep(1500 * time.Millisecond)
			return 0
		case "fresh-no-exit-child", "fresh-no-exit-stray":
			// Leaves a child in its process group, writes the child's pid to
			// the pidFile argument, and once the goFile argument exists ends
			// its stream right after its result, and never exits. The socket
			// is closed on exec, so the child does not hold it. In
			// fresh-no-exit-stray the child is started by another process,
			// in a group of its own, that waits for it only once the test
			// lets it (strayParent).
			var a struct {
				PIDFile string `json:"pidFile"`
				GoFile  string `json:"goFile"`
			}
			json.Unmarshal(inv.Args.Producer(), &a)
			syscall.CloseOnExec(int(f.Fd()))
			if mode == "fresh-no-exit-child" {
				child := exec.Command("/bin/sleep", "30")
				if err := child.Start(); err != nil {
					return 72
				}
				os.WriteFile(a.PIDFile, []byte(strconv.Itoa(child.Process.Pid)), 0o644)
			} else {
				parent := exec.Command(os.Args[0], "-test.run=^$")
				parent.Env = append(os.Environ(), "WL_FAKE_HARNESS=", "WL_STRAY_PARENT="+strconv.Itoa(pid), "WL_STRAY_FILE="+a.PIDFile)
				parent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := parent.Start(); err != nil {
					return 72
				}
			}
			for {
				if _, err := os.Stat(a.GoFile); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
			nc.Close()
			f.Close()
			time.Sleep(time.Hour)
			return 0
		case "fresh-exit-at-once":
			// Exits right after its result without closing its socket, so
			// the engine reads the end of the stream only once the process
			// is gone.
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
			return 0
		case "fresh-term-after-end":
			// Ends its stream right after its result and dies by its own
			// SIGTERM.
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
			nc.Close()
			f.Close()
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
			time.Sleep(time.Hour)
			return 0
		case "fresh-log-exit":
			// Logs a line, and once the engine says its reader has it, 20
			// more, a frame each, and exits with code 6.
			send(Log{ID: inv.ID, Lines: []string{"line 0"}})
			recv()
			for i := 1; i <= 20; i++ {
				send(Log{ID: inv.ID, Lines: []string{fmt.Sprintf("line %d", i)}})
			}
			return 6
		case "fresh-drip-exit":
			// Logs a line, leaves a child that holds the socket and keeps
			// writing to it (dripChild), and exits with code 6. The child
			// has a session of its own, so no kill of this process's group
			// reaches it.
			send(Log{ID: inv.ID, Lines: []string{"line 0"}})
			child := exec.Command(os.Args[0], "-test.run=^$")
			child.Env = append(os.Environ(), "WL_DRIP_CHILD="+inv.ID)
			child.ExtraFiles = []*os.File{f}
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := child.Start(); err != nil {
				fmt.Fprintln(os.Stderr, "fake: drip child:", err)
				return 72
			}
			return 6
		default:
			send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: body, Settled: true})
		}
	}
}

// dripChild is the child a fake harness in mode fresh-drip-exit leaves: on
// the socket it inherited as fd 3, it writes a log line about invocation id
// every 50 ms for 20 s, and stops once the engine has closed its end.
func dripChild(id string) int {
	nc, err := net.FileConn(os.NewFile(3, "engine"))
	if err != nil {
		return 70
	}
	for i := 1; i <= 400; i++ {
		p, err := Encode(Log{ID: id, Lines: []string{fmt.Sprintf("drip %d", i)}})
		if err != nil {
			panic(err)
		}
		if _, err := nc.Write(appendFrame(nil, p)); err != nil {
			return 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

// TestFreshExitWithAnUnreadPong checks that a fresh process that exits
// right after its result, while a pong the engine sent in answer to its
// heartbeat is still unread, has its result delivered, settled completed.
// On Linux a stream socket closed with unread data ends its peer's reads
// with ECONNRESET instead of EOF; that is the harness's end of stream, not
// a failed connection, which would discard the held result as killed by
// SIGKILL.
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

// Every frame a process wrote before it exited reaches its invocation, in
// order, however long the reader takes to come to it: the grace after the
// exit bounds only each read's wait for more, which a child that holds the
// socket would make endless. The reader is held on the first log frame,
// while the other 20 are written and the process exits, until the grace
// has passed. A deadline set once at the exit cut them off: on linux/arm64
// the tail of TestBunFreshEndsWithTheProcess's long log stopped at the end
// of a frame still in the socket when its reader was slow.
func TestFramesWrittenBeforeTheExitAreRead(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-log-exit")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	rec := newRecorder()
	inv := freshInvocation("x", `{}`, rec)
	var once sync.Once
	hold := func(m Message) {
		if l, ok := m.(Log); ok && l.ID == inv.ID {
			once.Do(func() {
				// The fake writes the rest once it reads this.
				p.conn.send(Ping{ID: "rest"})
				<-p.waitDone
				time.Sleep(exitReadGrace + 200*time.Millisecond)
			})
		}
	}
	messageRead.Store(&hold)
	t.Cleanup(func() { messageRead.Store(nil) })
	out, err := execute(t, p, inv, 10*time.Second)
	want := "line 0"
	for i := 1; i <= 20; i++ {
		want += fmt.Sprintf("\nline %d", i)
	}
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 6 || out.Tail != want {
		t.Fatalf("got %s exit %d, tail %q, %v; diagnostics %q", out.Kind, out.ExitCode, out.Tail, err, diag.String())
	}
	if got := rec.log(); got != "settled process_exited" {
		t.Fatalf("reporter calls %q", got)
	}
}

// A child the harness left, holding the socket and writing a frame to it
// every 50 ms, does not keep the reader past exitReadGrace of waiting: the
// process ends, and its invocation with it, about a second after the
// harness exited, though the child goes on for 20 s.
func TestAChildWritingAfterTheExitDoesNotHoldTheReader(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-drip-exit")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	rec := newRecorder()
	type result struct {
		out engine.StepOutcome
		err error
		at  time.Time
	}
	done := make(chan result, 1)
	go func() {
		out, err := execute(t, p, freshInvocation("x", `{}`, rec), 30*time.Second)
		done <- result{out, err, time.Now()}
	}()
	<-p.waitDone
	exit := time.Now()
	select {
	case r := <-done:
		if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.ExitCode != 6 || !strings.HasPrefix(r.out.Tail, "line 0") {
			t.Fatalf("got %s exit %d, tail %q, %v; diagnostics %q", r.out.Kind, r.out.ExitCode, r.out.Tail, r.err, diag.String())
		}
		t.Logf("the invocation ended %v after the exit", r.at.Sub(exit))
	case <-time.After(8 * time.Second):
		t.Fatalf("the invocation is still in flight 8 s after the harness exited; diagnostics %q", diag.String())
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the process has not ended")
	}
}

// A drain that times out after the harness has exited ends the reader at
// once, however long the reader would still wait (here an hour of exit
// grace), while a child the harness left keeps writing to the socket.
func TestADrainEndsTheReaderOfAnExitedProcess(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-drip-exit")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	p.mu.Lock()
	p.exitGrace = time.Hour
	p.mu.Unlock()
	rec := newRecorder()
	executed := make(chan struct{})
	go func() {
		defer close(executed)
		out, err := execute(t, p, freshInvocation("x", `{}`, rec), 30*time.Second)
		t.Logf("the invocation: %s exit %d, %v", out.Kind, out.ExitCode, err)
	}()
	<-p.waitDone
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- p.Drain(ctx) }()
	select {
	case err := <-drained:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("Drain still waits 8 s after its context ended; diagnostics %q", diag.String())
	}
	select {
	case <-executed:
	case <-time.After(5 * time.Second):
		t.Fatal("the invocation is still in flight")
	}
	if !strings.Contains(diag.String(), "killing the harness: drain timed out") {
		t.Fatalf("diagnostics %q", diag.String())
	}
}

// Every kill but the watchdog's, which says why on its own, is named in the
// diagnostics, so a step that ends as killed by SIGKILL says by what.
func TestKillIsNamed(t *testing.T) {
	cfg, diag := fakeConfig(t, "ok")
	p := spawn(t, cfg)
	p.Kill()
	<-p.Done()
	waitFor(t, "the kill's diagnostic line", func() bool {
		return strings.Contains(diag.String(), "killing the harness: killed by the engine")
	})
}

// A settled invocation stays in recent, so it keeps nothing of its caller's
// context: what the context carries is garbage once Execute has returned
// and the caller has let the context go.
func TestSettledInvocationKeepsNoCallerContext(t *testing.T) {
	cfg, _ := fakeConfig(t, "ok")
	p := spawn(t, cfg)
	type key struct{}
	payload := new([4096]byte)
	held := weak.Make(payload)
	inv := newInvocation("x", `{}`, newRecorder())
	func() {
		// No deadline: the runtime can keep a stopped deadline timer, and
		// the context with it, for a while after cancel.
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, payload))
		defer cancel()
		if out, err := p.Execute(ctx, inv); err != nil || out.Kind != engine.OutcomeOK {
			t.Fatalf("got %+v, %v", out, err)
		}
		rec := inv.Report.(*recorder)
		if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleCompleted {
			t.Fatalf("settled %s; calls %s", k, rec.log())
		}
	}()
	payload = nil
	p.mu.Lock()
	_, ok := p.recent.get(inv.ID)
	p.mu.Unlock()
	if !ok {
		t.Fatal("the settled invocation is not in recent")
	}
	// Collect until the value is gone. Goroutines that are still ending
	// can hold the context a moment longer; the invocation in recent would
	// hold it for good, and fail this.
	waitFor(t, "the value of the caller's context to be collected", func() bool {
		runtime.GC()
		return held.Value() == nil
	})
}

// loadingContext is a caller's context whose Done calls back into the
// process, as a context that samples its load would.
type loadingContext struct {
	context.Context
	p *Process
}

func (c loadingContext) Done() <-chan struct{} {
	c.p.Load()
	return c.Context.Done()
}

// A fresh dispatch asks the caller's context for its Done channel without
// holding the process's lock, so a context that calls back into the
// process from Done does not stall it.
func TestFreshDispatchAsksForDoneOutsideTheLock(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-exit-at-once")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		out, err := p.Execute(loadingContext{ctx, p}, freshInvocation("x", `{}`, newRecorder()))
		if err == nil && out.Kind != engine.OutcomeOK {
			err = fmt.Errorf("got %+v", out)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%v; diagnostics %q", err, diag.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatch did not return: it holds the process's lock while it asks for Done")
	}
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

// Naming a kill never holds it up: with the diagnostics writer blocked,
// Kill returns and the process ends, its line still waiting for the
// writer.
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

// The kill lines that wait for blocked diagnostics writers are bounded
// across processes, not only to one for each process: past the bound a
// kill's line is dropped, Kill still returns at once, and the next line
// that is written reports how many were dropped. Every process shares one
// bound unless its config names another.
func TestKillLinesAreBoundedAcrossProcesses(t *testing.T) {
	cfg, _ := fakeConfig(t, "ok")
	if p := spawn(t, cfg); p.killLines != runtimeKillLines {
		t.Fatal("a process with no budget in its config does not share the program's budget")
	}

	budget := &killLineBudget{max: 2}
	w := newBlockedWriter()
	t.Cleanup(w.open)
	var procs []*Process
	for range 5 {
		cfg, _ := fakeConfig(t, "ok")
		cfg.Diagnostics = w
		cfg.killLines = budget
		procs = append(procs, spawn(t, cfg))
	}
	for _, p := range procs {
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
		<-p.Done()
	}
	if got, dropped := budget.pending.Load(), budget.dropped.Load(); got != 2 || dropped != 3 {
		t.Fatalf("with every writer blocked: %d kill lines waiting and %d dropped, want 2 and 3", got, dropped)
	}

	w.open()
	waitFor(t, "the waiting kill lines to be written", func() bool { return budget.pending.Load() == 0 })
	cfg, diag := fakeConfig(t, "ok")
	cfg.killLines = budget
	p := spawn(t, cfg)
	p.Kill()
	<-p.Done()
	waitFor(t, "the next kill line, with the count of dropped lines", func() bool {
		return strings.Contains(diag.String(), "killing the harness: killed by the engine (3 earlier kill lines were dropped while 2 waited for blocked diagnostics writers)")
	})
	if n := budget.dropped.Load(); n != 0 {
		t.Fatalf("%d dropped lines are still counted after the count was reported", n)
	}
}

// A fresh invocation's cancellation kills its process, whose kill is named,
// and returns at once, never waiting for the diagnostics writer ([WR-37]:
// Windmill kills a job's process at once on a timeout or a cancellation).
//
// [WR-37]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/languages.md#wr-37
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

// TestWatchdogIgnoresEngineWriteBackpressure checks that silence is
// measured from frames received, so a harness that keeps sending
// heartbeats is not killed while the engine's own writes are blocked
// behind a large message it does not read.
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
	// directory, and while it has not said hello (no timing sleep).
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

// The reader hands a result's Reporter calls and outcome to the goroutine
// waiting in Execute when none of the invocation's calls is running
// (invocation.drive), and queues them behind those that are. Under -race,
// 120 invocations at once, each answered with a warning and then a result
// about when its context ends, as measured from a round trip: every
// Execute returns, each Reporter's calls never overlap and keep their
// order (the warning before Settled), and each invocation settles once:
// completed whether its result or its cancellation won, or not started
// when its context ended before its dispatch.
func TestResultHandOffRacesCancellation(t *testing.T) {
	cfg, _ := fakeConfig(t, "warn-then-answer")
	p := spawn(t, cfg)
	start := time.Now()
	for range 5 {
		if _, err := execute(t, p, newInvocation("x", `{"after_us":0}`, newRecorder()), 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	rtt := time.Since(start) / 5
	const n = 120
	var wg sync.WaitGroup
	var won, lost, unsent atomic.Int32
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			after := rtt * time.Duration(1+i%4)
			rep := newRecorder()
			inv := newInvocation("x", fmt.Sprintf(`{"after_us":%d}`, after.Microseconds()), rep)
			ctx, cancel := context.WithTimeout(context.Background(), after+rtt*time.Duration(i%7)/3)
			defer cancel()
			out, err := p.Execute(ctx, inv)
			settle := engine.SettleCompleted
			switch {
			case err == nil && out.Kind == engine.OutcomeOK:
				won.Add(1)
			case errors.Is(err, context.DeadlineExceeded) && strings.Contains(rep.log(), string(engine.SettleNotStarted)):
				unsent.Add(1)
				settle = engine.SettleNotStarted
			case errors.Is(err, context.DeadlineExceeded):
				lost.Add(1)
			default:
				t.Errorf("%s: %+v, %v", inv.ID, out, err)
				return
			}
			if k := rep.waitSettled(t, 10*time.Second); k != settle {
				t.Errorf("%s settled %s, want %s", inv.ID, k, settle)
			}
			calls := rep.log()
			if strings.Contains(calls, "CONCURRENT") || !strings.HasSuffix(calls, "settled "+string(settle)) || strings.Count(calls, "settled") != 1 {
				t.Errorf("%s: calls %s", inv.ID, calls)
			}
			if err == nil && !strings.HasPrefix(calls, "warning "+WarningUnattributed) {
				t.Errorf("%s: calls %s, want the warning first", inv.ID, calls)
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("an Execute did not return")
	}
	t.Logf("round trip %s; results won %d, cancellations %d, not started %d", rtt, won.Load(), lost.Load(), unsent.Load())
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
	// A reader marks itself done in a deferred call, so for a moment after
	// Done it can still be on its way out of copyLines. Look again for a
	// short while: one still reading would stay until the child's exit,
	// about 2 s from now.
	b := make([]byte, 1<<20)
	reading := func() bool {
		return strings.Contains(string(b[:runtime.Stack(b, true)]), "harness.(*Process).copyLines")
	}
	for deadline := time.Now().Add(250 * time.Millisecond); reading(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("an output pipe is still being read after Done")
		}
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

// A fresh process that ends its stream right after its result and exits
// 1.5 s later, longer than the reader's grace after an exit, keeps its
// result: the engine waits for the exit itself, as Windmill's worker waits
// for the job's process, and nothing is killed.
func TestFreshSlowExitKeepsItsResult(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-slow-exit")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	rec := newRecorder()
	out, err := execute(t, p, freshInvocation("x", `{}`, rec), 20*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != `"done"` {
		t.Fatalf("got %+v, %v; diagnostics %q", out, err, diag.String())
	}
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s; calls %s", k, rec.log())
	}
	<-p.Done()
	if st := p.ExitStatus(); st.Signal != "" || st.Code != 0 {
		t.Fatalf("exit status %s, want exit code 0", st)
	}
	if d := diag.String(); strings.Contains(d, "killing the harness") {
		t.Fatalf("the process was killed: %q", d)
	}
}

// A fresh process that ends its stream and never exits is killed once
// endWait has passed: its invocation fails as escaped by SIGKILL, its held
// result dropped, and the diagnostics say why.
func TestFreshNoExitIsEnded(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-no-exit")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	p.mu.Lock()
	p.endWait = 300 * time.Millisecond
	p.mu.Unlock()
	rec := newRecorder()
	start := time.Now()
	out, err := execute(t, p, freshInvocation("x", `{}`, rec), 20*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.Signal != "SIGKILL" || out.ExitCode != -1 {
		t.Fatalf("got %+v, %v; diagnostics %q", out, err, diag.String())
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the outcome took %s", d)
	}
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleProcessExited {
		t.Fatalf("settled %s; calls %s", k, rec.log())
	}
	<-p.Done()
	if st := p.ExitStatus(); st.Signal != "SIGKILL" {
		t.Fatalf("exit status %s, want SIGKILL", st)
	}
	waitFor(t, "the kill's diagnostics line", func() bool {
		return strings.Contains(diag.String(), "killing the harness: no exit within 300ms of the end of its connection")
	})
}

// A fresh harness that ends its stream and never exits, leaving a child in
// its group, is ended within the bound: its kill is what ended it, so the
// job has failed, and its group is killed too, then waited for.
func TestFreshNoExitWithAChildIsBounded(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-no-exit-child")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	p.mu.Lock()
	p.endWait = 300 * time.Millisecond
	p.mu.Unlock()
	child, then, done := startHeldChild(t, p, nil)
	if observable && then.ppid != p.pid {
		t.Fatalf("child %d: %+v, want a child of %d", child, then, p.pid)
	}
	r := <-done
	if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.Signal != "SIGKILL" {
		t.Fatalf("got %+v, %v; diagnostics %q", r.out, r.err, diag.String())
	}
	if d := time.Since(r.start); d > 10*time.Second {
		t.Fatalf("the outcome took %s, the child's 30 s", d)
	}
	if observable {
		waitFor(t, "the child to be killed with the group", func() bool {
			now, ok := procInfoOf(child)
			return !ok || now.start != then.start || now.zombie
		})
	}
}

// When the bound's kill has ended a fresh harness, the job settles at most
// the same bound later, whatever is left in its group: here the group keeps
// a zombie whose parent, another process outside the group, does not wait
// for it until the test lets it. While the caller's context lasts, the
// process itself ends once the group has emptied, as it does at main.
func TestFreshNoExitWithAStrayZombieIsBounded(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-no-exit-stray")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	p.mu.Lock()
	p.endWait = 300 * time.Millisecond
	p.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	child, then, done := startHeldChild(t, p, ctx)
	if observable && (then.pgid != p.pid || then.ppid == p.pid || then.ppid == os.Getpid()) {
		t.Fatalf("child %d: %+v, want a member of group %d whose parent is neither", child, then, p.pid)
	}
	r := <-done
	if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.Signal != "SIGKILL" {
		t.Fatalf("got %+v, %v; diagnostics %q", r.out, r.err, diag.String())
	}
	if d := time.Since(r.start); d > 10*time.Second {
		t.Fatalf("the outcome took %s", d)
	}
	// The job settled with the group not empty: it still holds the killed
	// child, as the zombie of its parent, which still runs.
	parent := waitPID(t, then.pidFile+".parent")
	if observable {
		waitFor(t, "the child to be killed with the group", func() bool {
			now, ok := procInfoOf(child)
			return ok && now.start == then.start && now.zombie
		})
		if now, _ := procInfoOf(child); now.ppid != parent || now.pgid != p.pid {
			t.Fatalf("child %d: %+v, want the zombie of %d in group %d", child, now, parent, p.pid)
		}
		if info, ok := procInfoOf(parent); !ok || info.zombie {
			t.Fatalf("the zombie's parent %d has ended: %+v, %v", parent, info, ok)
		}
	}
	// The group is not empty, and the caller's context has not ended: the
	// process has not ended either.
	select {
	case <-p.Done():
		t.Fatal("the process ended while its group still held the zombie")
	default:
	}
	// Once that parent reaps the zombie, the group is empty and the
	// process ends.
	if err := os.WriteFile(then.pidFile+".done", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not end once its group was empty")
	}
}

// A cancellation that abandons the invocation before the landed kill's
// deadline settles it wins, and the process ends as it does at main once
// nothing waits for an outcome: it does not wait for a group that still
// holds a zombie of another process's. The cancellation comes as the
// deadline passes, after the wait has last looked for a pending invocation
// (settleSeen), and the caller's context has no deadline of its own.
func TestFreshCancellationBeforeTheSettlementEndsTheWait(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-no-exit-stray")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	p.mu.Lock()
	p.endWait = 300 * time.Millisecond
	p.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var r heldResult
	dones := make(chan chan heldResult, 1)
	got := make(chan struct{})
	seen := func(q *Process) {
		if q != p {
			return
		}
		cancel()
		r = <-<-dones
		close(got)
	}
	settleSeen.Store(&seen)
	t.Cleanup(func() { settleSeen.Store(nil) })
	_, then, done := startHeldChild(t, p, ctx)
	dones <- done
	select {
	case <-got:
	case <-time.After(20 * time.Second):
		t.Fatalf("the landed kill's deadline did not pass; diagnostics %q", diag.String())
	}
	if !errors.Is(r.err, context.Canceled) {
		t.Fatalf("got %+v, %v, want the cancellation; diagnostics %q", r.out, r.err, diag.String())
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not end once nothing waited for an outcome")
	}
	// It ended while the zombie's parent, outside the group, still ran.
	parent := waitPID(t, then.pidFile+".parent")
	if info, ok := procInfoOf(parent); observable && (!ok || info.zombie) {
		t.Fatalf("the zombie's parent %d has ended: %+v, %v", parent, info, ok)
	}
}

// When the bound's kill has settled a fresh job early, the process goes on
// waiting for its group only for as long as the invocation's caller would
// have waited for the outcome, as at main: until its context ends, here at
// its deadline. The zombie's parent never lets the zombie go before the
// test ends.
func TestFreshSettledJobEndsByItsCallersDeadline(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-no-exit-stray")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	p.mu.Lock()
	p.endWait = 300 * time.Millisecond
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	child, then, done := startHeldChild(t, p, ctx)
	r := <-done
	if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.Signal != "SIGKILL" {
		t.Fatalf("got %+v, %v; diagnostics %q", r.out, r.err, diag.String())
	}
	select {
	case <-p.Done():
	case <-time.After(time.Until(deadline) + 5*time.Second):
		t.Fatal("the process did not end by its caller's deadline")
	}
	if early := time.Until(deadline); early > 0 {
		t.Fatalf("the process ended %s before its caller's deadline", early)
	}
	// The group still held the zombie: only the deadline ended the wait.
	parent := waitPID(t, then.pidFile+".parent")
	if observable {
		now, ok := procInfoOf(child)
		if !ok || now.start != then.start || !now.zombie || now.ppid != parent {
			t.Fatalf("child %d: %+v, %v, want the zombie of %d", child, now, ok, parent)
		}
	}
}

// When the bound's kill has settled a fresh job early, a cancellation of
// the caller's context ends the wait for its group at once, as it would at
// main, where the invocation is still pending then: the process ends while
// the group still holds a zombie of another process's. The engine cannot
// tell such a cancellation from the caller's own release of its context
// once Execute has returned, and ends the wait for either.
func TestFreshCallersContextEndEndsTheWait(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-no-exit-stray")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	p.mu.Lock()
	p.endWait = 300 * time.Millisecond
	p.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	child, then, done := startHeldChild(t, p, ctx)
	r := <-done
	if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.Signal != "SIGKILL" {
		t.Fatalf("got %+v, %v; diagnostics %q", r.out, r.err, diag.String())
	}
	parent := waitPID(t, then.pidFile+".parent")
	if observable {
		waitFor(t, "the child to be killed with the group", func() bool {
			now, ok := procInfoOf(child)
			return ok && now.start == then.start && now.zombie
		})
	}
	select {
	case <-p.Done():
		t.Fatal("the process ended while its caller waited and its group held the zombie")
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the process did not end once its caller's context had ended")
	}
	if observable {
		now, ok := procInfoOf(child)
		if !ok || now.start != then.start || !now.zombie || now.ppid != parent {
			t.Fatalf("child %d: %+v, %v, want the zombie of %d", child, now, ok, parent)
		}
	}
}

// When the caller's context ends after the early settlement, the group is
// killed as it would be at main, where the invocation is still pending then
// and its cancellation kills the job's group: here a member that joined the
// group after the bound's group kill, started by the zombie's parent, is
// killed, and the process ends at once.
func TestFreshCallersContextEndKillsALateMember(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-no-exit-stray")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	p.mu.Lock()
	p.endWait = 300 * time.Millisecond
	p.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	child, then, done := startHeldChild(t, p, ctx)
	r := <-done
	if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.Signal != "SIGKILL" {
		t.Fatalf("got %+v, %v; diagnostics %q", r.out, r.err, diag.String())
	}
	if observable {
		waitFor(t, "the child to be killed with the group", func() bool {
			now, ok := procInfoOf(child)
			return ok && now.start == then.start && now.zombie
		})
	}
	if err := os.WriteFile(then.pidFile+".late", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	late := waitPID(t, then.pidFile+".late.pid")
	var lateThen procInfo
	if observable {
		var ok bool
		lateThen, ok = procInfoOf(late)
		if !ok || lateThen.zombie || lateThen.pgid != p.pid {
			t.Fatalf("late member %d: %+v, %v, want it running in group %d", late, lateThen, ok, p.pid)
		}
	}
	select {
	case <-p.Done():
		t.Fatal("the process ended while its caller waited and its group held members")
	default:
	}
	cancel()
	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the process did not end once its caller's context had ended")
	}
	if observable {
		waitFor(t, "the late member to be killed", func() bool {
			now, ok := procInfoOf(late)
			return !ok || now.start != lateThen.start || now.zombie
		})
	}
}

// observable is set where the tests can read a process's parent, group and
// state (procInfoOf).
var observable = runtime.GOOS == "linux" || runtime.GOOS == "darwin"

type heldResult struct {
	out   engine.StepOutcome
	err   error
	start time.Time
}

// heldChild is what startHeldChild read of the child while it ran, and
// where its pid was written.
type heldChild struct {
	procInfo
	pidFile string
}

// startHeldChild runs an invocation of a fresh-no-exit-child or
// fresh-no-exit-stray harness: it waits for the child's pid, reads the
// child while it runs, then lets the harness end its stream, which starts
// the bound. It returns the child, what was read of it, and the outcome
// to come. The invocation runs under ctx, or, when it is nil, under a
// context that ends 20 s after it starts. However it ends, the harness is
// let go, so the invocation's
// goroutine finishes; in fresh-no-exit-stray the child's parent is let go
// too when the test ends.
func startHeldChild(t *testing.T, p *Process, ctx context.Context) (int, heldChild, chan heldResult) {
	t.Helper()
	dir := t.TempDir()
	pidFile, goFile := filepath.Join(dir, "pid"), filepath.Join(dir, "go")
	done := make(chan heldResult, 1)
	start := make(chan time.Time, 1)
	released := false
	release := func() {
		if !released {
			released = true
			start <- time.Now()
			os.WriteFile(goFile, nil, 0o644)
		}
	}
	defer release()
	// Before the temporary directory goes (registered earlier, so removed
	// later): in fresh-no-exit-stray the child's parent is let go, and the
	// test waits for it to reap the child and exit.
	t.Cleanup(func() {
		if err := os.WriteFile(pidFile+".done", nil, 0o644); err != nil {
			t.Errorf("letting the child's parent go: %v", err)
			return
		}
		b, err := os.ReadFile(pidFile + ".parent")
		if err != nil || !observable {
			return
		}
		if parent, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			waitFor(t, "the child's parent to reap it and exit", func() bool {
				info, ok := procInfoOf(parent)
				return !ok || info.zombie
			})
		}
	})
	go func() {
		inv := freshInvocation("x", `{"pidFile":`+strconv.Quote(pidFile)+`,"goFile":`+strconv.Quote(goFile)+`}`, newRecorder())
		var out engine.StepOutcome
		var err error
		if ctx == nil {
			out, err = execute(t, p, inv, 20*time.Second)
		} else {
			out, err = p.Execute(ctx, inv)
		}
		done <- heldResult{out, err, <-start}
	}()
	child := waitPID(t, pidFile)
	then := heldChild{pidFile: pidFile}
	if observable {
		var ok bool
		then.procInfo, ok = procInfoOf(child)
		if !ok || then.zombie {
			t.Fatalf("child %d: %+v, %v, want it running", child, then.procInfo, ok)
		}
	}
	release()
	return child, then, done
}

// waitPID waits for file to hold a pid, and returns it.
func waitPID(t *testing.T, file string) int {
	t.Helper()
	var pid int
	waitFor(t, "a pid in "+filepath.Base(file), func() bool {
		b, err := os.ReadFile(file)
		if err != nil || len(b) == 0 {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	return pid
}

// A fresh process whose exit is seen only after the bound's kill was
// attempted keeps its held result: the kill found it gone, and its own
// exit status, 0, stands. The engine hears of the exit only once the kill
// has been tried (exitObserved).
func TestFreshExitSeenAfterTheBoundKeepsItsResult(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-exit-at-once")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	p.mu.Lock()
	p.endWait = 300 * time.Millisecond
	p.mu.Unlock()
	late := func(q *Process) {
		if q != p {
			return
		}
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			q.mu.Lock()
			tried := q.killReason != ""
			q.mu.Unlock()
			if tried {
				return
			}
		}
	}
	exitObserved.Store(&late)
	t.Cleanup(func() { exitObserved.Store(nil) })
	rec := newRecorder()
	out, err := execute(t, p, freshInvocation("x", `{}`, rec), 20*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != `"done"` {
		t.Fatalf("got %+v, %v; diagnostics %q", out, err, diag.String())
	}
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s; calls %s", k, rec.log())
	}
	<-p.Done()
	if st := p.ExitStatus(); st.Signal != "" || st.Code != 0 {
		t.Fatalf("exit status %s, want exit code 0", st)
	}
	waitFor(t, "the bound's kill line", func() bool {
		return strings.Contains(diag.String(), "killing the harness: no exit within 300ms")
	})
}

// A fresh process that ends its stream after its result and dies by its
// own SIGTERM before the bound keeps its result, and its exit status is its
// own: nothing is killed.
func TestFreshSignalledExitIsItsOwn(t *testing.T) {
	cfg, diag := fakeConfig(t, "fresh-term-after-end")
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	rec := newRecorder()
	out, err := execute(t, p, freshInvocation("x", `{}`, rec), 20*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != `"done"` {
		t.Fatalf("got %+v, %v; diagnostics %q", out, err, diag.String())
	}
	<-p.Done()
	if st := p.ExitStatus(); st.Signal != "SIGTERM" {
		t.Fatalf("exit status %s, want SIGTERM", st)
	}
	if d := diag.String(); strings.Contains(d, "killing the harness") {
		t.Fatalf("the process was killed: %q", d)
	}
}

// The kill of the process alone is what ended it only when it was sent and
// the process ended by SIGKILL. One that ended with a code, or by another
// signal, ended by itself. This covers the exit that is seen only after
// the bound's kill was sent, which cannot be timed from outside.
func TestKillLanded(t *testing.T) {
	for _, c := range []struct {
		alone bool
		st    ExitStatus
		want  bool
	}{
		{true, ExitStatus{Code: -1, Signal: "SIGKILL"}, true},
		{true, ExitStatus{Code: -1, Signal: "SIGTERM"}, false},
		{true, ExitStatus{Code: 0}, false},
		{true, ExitStatus{Code: 3}, false},
		{false, ExitStatus{Code: -1, Signal: "SIGKILL"}, false},
	} {
		if got := killLanded(c.alone, c.st); got != c.want {
			t.Errorf("killLanded(%v, %s) = %v, want %v", c.alone, c.st, got, c.want)
		}
	}
}

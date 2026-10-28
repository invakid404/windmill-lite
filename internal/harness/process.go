package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Defaults of a ProcessConfig.
const (
	// DefaultKillAfter is the watchdog's silence threshold (ADR 0004 host
	// mode item 4, the architect's default recorded in the design).
	DefaultKillAfter = 30 * time.Second
	// WatchdogOff, as KillAfter, disables the watchdog.
	WatchdogOff time.Duration = -1
	// DefaultHandshakeTimeout bounds the wait for hello.
	DefaultHandshakeTimeout = 10 * time.Second
	// DefaultRecentLate is how many settled invocations keep receiving
	// late output (D9) per process.
	DefaultRecentLate = 4096
	// exitReadGrace is how long the reader keeps draining the socket after
	// the process exited: a child the step started may hold the socket
	// open, so the end of the stream cannot be waited for.
	exitReadGrace = time.Second
	// rejectGrace is how long a rejected harness gets to exit by itself.
	rejectGrace = 2 * time.Second
)

// ProcessConfig describes one harness process for the engine to spawn.
type ProcessConfig struct {
	// Path is the program to run and Args its arguments, without the
	// program name; see BunCommand.
	Path string
	Args []string
	// Env is the process's entire environment; nil means BaseEnv().
	Env []string
	// WorkRoot is the directory in which the process's private working
	// directory is created; empty means os.TempDir().
	WorkRoot string

	// Kind, Runtime and Bundle are what the handshake requires of the
	// harness: its kind ("bun"), its runtime version (the pin, such as
	// PinnedBun) and the digest of the bundle it loaded.
	Kind    string
	Runtime string
	Bundle  string
	// ModuleState is the process's module-state mode. Only
	// engine.ModuleStateWarm is implemented; fresh mode is milestone C3b.
	ModuleState engine.ModuleState
	// EngineVersion is sent in the welcome.
	EngineVersion string

	// KillAfter is the watchdog threshold: the process is killed when no
	// frame has arrived from it for that long. Zero means
	// DefaultKillAfter; WatchdogOff disables the watchdog.
	KillAfter time.Duration
	// Heartbeat is how often the harness pings on its own. Zero means one
	// second, or a quarter of KillAfter when that is shorter.
	Heartbeat time.Duration
	// MaxMessage lowers the largest message either side may send; zero
	// means DefaultMaxMessage.
	MaxMessage int64
	// HandshakeTimeout bounds the wait for hello; zero means
	// DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
	// RecentLate bounds how many settled invocations keep receiving late
	// output; zero means DefaultRecentLate.
	RecentLate int
	// Diagnostics receives the process's stdout and stderr, which no
	// invocation owns, one prefixed line at a time, and the engine's own
	// diagnostics about the process; nil means os.Stderr.
	Diagnostics io.Writer

	// notify is called, outside every lock, after an invocation is
	// abandoned, after an abandoned invocation settles, and after the
	// process exits. The pool sets it.
	notify func(*Process)
}

// PinnedBun is the Bun version the harness must run on: the version of
// Windmill v1.767.0's images (WR-3, ADR 0001).
const PinnedBun = "1.3.10"

// BunCommand returns the Path and Args that run the Bun harness runtime at
// runtimeMain (harnesses/bun/runtime/main.ts) with bun, on the bundle in
// bundleDir.
func BunCommand(bun, runtimeMain, bundleDir string) (string, []string) {
	return bun, []string{runtimeMain, "--bundle", bundleDir}
}

// BaseEnv returns the part of a Bun job's base environment (WR-22, WA-2)
// that needs no other milestone: PATH, HOME (the engine's, or /tmp), TZ
// when the engine has one, FORCE_COLOR=1, DO_NOT_TRACK=1 and
// BUN_RUNTIME_TRANSPILER_CACHE_PATH=0. BUN_INSTALL_CACHE_DIR, NODE_PATH and
// BASE_URL arrive with the milestones that give them values (C3b, PF1).
func BaseEnv() []string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	if tz, ok := os.LookupEnv("TZ"); ok {
		env = append(env, "TZ="+tz)
	}
	return append(env, "FORCE_COLOR=1", "DO_NOT_TRACK=1", "BUN_RUNTIME_TRANSPILER_CACHE_PATH=0")
}

// HandshakeError is a harness the engine refused in the handshake.
type HandshakeError struct {
	Code    string // a Reject code
	Message string
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("harness rejected: %s: %s", e.Code, e.Message)
}

// ExitStatus is how a harness process ended.
type ExitStatus struct {
	// Code is the exit code, or -1 when a signal ended the process.
	Code int
	// Signal names the signal that ended the process ("SIGKILL"), if any.
	Signal string
}

func (s ExitStatus) String() string {
	if s.Signal != "" {
		return "process terminated by signal: " + s.Signal
	}
	return fmt.Sprintf("exit code %d", s.Code)
}

// Process is one spawned harness process and the engine side of its
// connection: the handshake, the invocations in flight, health and the
// watchdog. It is safe for concurrent use.
type Process struct {
	cfg        ProcessConfig
	cmd        *exec.Cmd
	conn       *conn
	dir        string
	pid        int
	killAfter  time.Duration
	heartbeat  time.Duration
	maxMessage int64
	diag       *diagnostics
	stdio      *lineWriter

	waitDone chan struct{} // closed when cmd.Wait has returned
	outR     *os.File      // the read ends of stdout and stderr
	errR     *os.File
	stdioWG  sync.WaitGroup
	exited   chan struct{} // closed when the exit has been handled

	mu         sync.Mutex
	state      procState
	helloCh    chan Hello
	hello      Hello
	invs       map[string]*invocation
	recent     recentInvocations
	inFlight   int
	abandoned  int
	pingSeq    uint64
	pings      map[string]chan *Health
	health     *Health
	bye        bool
	ending     bool      // the reader stopped or the process exited
	exitAt     time.Time // when cmd.Wait returned
	killReason string
	readErr    error
	status     ExitStatus
}

type procState int

const (
	stateHandshake procState = iota
	stateServing
	stateDraining
	stateExited
)

type invState int

const (
	// invPending: dispatched, no outcome yet.
	invPending invState = iota
	// invFixed: the outcome was returned while the call goes on
	// (settled false).
	invFixed
	// invAbandoned: the engine stopped waiting before an outcome.
	invAbandoned
)

type invocation struct {
	id      string
	run     string
	report  engine.Reporter
	state   invState
	tail    tail
	outcome chan engine.StepOutcome // buffered, receives exactly one value
	// calls runs the invocation's Reporter calls and its outcome delivery,
	// one at a time and in order, away from the connection's reader: a slow
	// Reporter neither stalls the frames of other invocations nor looks
	// like silence to the watchdog.
	calls serial
	// fixed, if set, runs once when the engine stops waiting for the
	// invocation: first in calls when its outcome is fixed, before any
	// Reporter call, or when it is abandoned. A pool frees the
	// invocation's slot there, so a Reporter that runs another step does
	// not wait for its own.
	fixed func()
}

// Spawn starts a harness process and completes the handshake. On success
// the process is serving and its watchdog runs.
func Spawn(ctx context.Context, cfg ProcessConfig) (*Process, error) {
	if cfg.ModuleState == "" {
		cfg.ModuleState = engine.ModuleStateWarm
	}
	if cfg.ModuleState != engine.ModuleStateWarm {
		return nil, fmt.Errorf("harness: module-state mode %q is not implemented (fresh mode is milestone C3b)", cfg.ModuleState)
	}
	p := &Process{
		cfg:        cfg,
		killAfter:  cfg.KillAfter,
		heartbeat:  cfg.Heartbeat,
		maxMessage: cfg.MaxMessage,
		waitDone:   make(chan struct{}),
		exited:     make(chan struct{}),
		helloCh:    make(chan Hello, 1),
		invs:       map[string]*invocation{},
		pings:      map[string]chan *Health{},
	}
	if p.killAfter == 0 {
		p.killAfter = DefaultKillAfter
	}
	if p.heartbeat == 0 {
		p.heartbeat = time.Second
		if p.killAfter > 0 && p.killAfter/4 < p.heartbeat {
			p.heartbeat = p.killAfter / 4
		}
	}
	p.heartbeat = max(p.heartbeat, time.Millisecond)
	switch {
	case cfg.Heartbeat < 0 || cfg.MaxMessage < 0:
		return nil, errors.New("harness: Heartbeat and MaxMessage cannot be negative")
	case p.killAfter > 0 && p.heartbeat >= p.killAfter:
		// An idle harness would look silent between two heartbeats.
		return nil, fmt.Errorf("harness: heartbeat %s must be shorter than the watchdog's killAfter %s", p.heartbeat, p.killAfter)
	}
	if p.maxMessage == 0 || p.maxMessage > DefaultMaxMessage {
		p.maxMessage = DefaultMaxMessage
	}
	p.recent.limit = cfg.RecentLate
	if p.recent.limit == 0 {
		p.recent.limit = DefaultRecentLate
	}
	diag := cfg.Diagnostics
	if diag == nil {
		diag = os.Stderr
	}
	p.diag = &diagnostics{w: diag}
	timeout := cfg.HandshakeTimeout
	if timeout == 0 {
		timeout = DefaultHandshakeTimeout
	}

	dir, err := os.MkdirTemp(cfg.WorkRoot, "wl-harness-")
	if err != nil {
		return nil, fmt.Errorf("harness: working directory: %w", err)
	}
	p.dir = dir
	parent, child, err := socketpair()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	cmd := exec.Command(cfg.Path, cfg.Args...)
	cmd.Env = cfg.Env
	if cmd.Env == nil {
		cmd.Env = BaseEnv()
	}
	cmd.Dir = dir
	cmd.ExtraFiles = []*os.File{child} // fd 3 in the child
	// stdout and stderr are pipes of the engine's own, read by its own
	// goroutines, so Wait returns as soon as the harness exits, even while
	// a child of it still holds them open.
	p.stdio = &lineWriter{diag: p.diag, keep: 20}
	outR, outW, err := os.Pipe()
	if err != nil {
		parent.Close()
		child.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("harness: stdout pipe: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		parent.Close()
		child.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("harness: stderr pipe: %w", err)
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	child.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		parent.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("harness: start %s: %w", cfg.Path, err)
	}
	p.outR, p.errR = outR, errR
	p.stdioWG.Add(2)
	go p.copyLines(outR, p.stdio.stream("stdout"))
	go p.copyLines(errR, p.stdio.stream("stderr"))
	p.cmd = cmd
	p.pid = cmd.Process.Pid
	p.diag.pid.Store(int64(p.pid))
	nc, err := net.FileConn(parent)
	parent.Close()
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		outR.Close()
		errR.Close()
		p.stdioWG.Wait()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("harness: socket: %w", err)
	}
	go func() {
		cmd.Wait()
		p.mu.Lock()
		p.status = exitStatus(cmd.ProcessState)
		p.exitAt = time.Now()
		p.ending = true
		p.mu.Unlock()
		close(p.waitDone)
		nc.SetReadDeadline(time.Now().Add(exitReadGrace))
	}()
	p.conn = newConn(nc, connConfig{maxMessage: p.maxMessage, onMessage: p.handle, onClose: p.readerDone})
	p.conn.run()

	if err := p.handshake(ctx, timeout); err != nil {
		return nil, err
	}
	if p.killAfter > 0 {
		go p.watchdog()
	}
	return p, nil
}

func (p *Process) handshake(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var h Hello
	select {
	case h = <-p.helloCh:
	case <-p.exited:
		return fmt.Errorf("harness exited before its hello (%s)%s", p.ExitStatus(), p.stdio.lastLines())
	case <-timer.C:
		p.kill("no hello within " + timeout.String())
		<-p.exited
		return fmt.Errorf("harness sent no hello within %s%s", timeout, p.stdio.lastLines())
	case <-ctx.Done():
		p.kill("handshake canceled")
		<-p.exited
		return context.Cause(ctx)
	}
	w := Welcome{Proto: ProtocolVersion, Engine: p.cfg.EngineVersion, Bundle: p.cfg.Bundle}
	var reject *Reject
	switch {
	case h.Proto != ProtocolVersion:
		reject = &Reject{RejectProto, fmt.Sprintf("protocol %d, engine speaks %d", h.Proto, ProtocolVersion)}
	case h.Harness.Kind != p.cfg.Kind:
		reject = &Reject{RejectHarness, fmt.Sprintf("harness kind %q, want %q", h.Harness.Kind, p.cfg.Kind)}
	case h.Runtime != p.cfg.Runtime:
		reject = &Reject{RejectRuntime, fmt.Sprintf("runtime %q, the bundle pins %q", h.Runtime, p.cfg.Runtime)}
	case h.Bundle != p.cfg.Bundle:
		reject = &Reject{RejectBundle, fmt.Sprintf("bundle %q, want %q", h.Bundle, p.cfg.Bundle)}
	case h.PID != p.pid:
		// The engine would otherwise signal a process other than the one
		// that speaks the protocol, such as a wrapper script.
		reject = &Reject{RejectPID, fmt.Sprintf("hello from pid %d, the engine spawned pid %d", h.PID, p.pid)}
	}
	if reject != nil {
		w.Reject = reject
		p.conn.send(w)
		select {
		case <-p.exited:
		case <-time.After(rejectGrace):
			p.kill("rejected harness did not exit")
			<-p.exited
		}
		return &HandshakeError{Code: reject.Code, Message: reject.Message}
	}
	w.Config = &WelcomeConfig{
		ModuleState: string(p.cfg.ModuleState),
		HeartbeatMS: int(max(p.heartbeat/time.Millisecond, 1)),
		MaxMessage:  p.maxMessage,
	}
	p.mu.Lock()
	if p.state == stateHandshake {
		p.state = stateServing
	}
	p.hello = h
	p.mu.Unlock()
	if err := p.conn.send(w); err != nil {
		p.kill("welcome not sent")
		<-p.exited
		return fmt.Errorf("harness: welcome: %w", err)
	}
	return nil
}

// handle is the reader's callback: it runs on the reader goroutine, one
// message at a time, so one invocation's Reporter is never called from two
// goroutines at once.
func (p *Process) handle(m Message) {
	if err := p.handleMessage(m); err != nil {
		p.diag.printf("protocol error, killing the harness: %v", err)
		p.mu.Lock()
		if p.readErr == nil {
			p.readErr = err
		}
		p.mu.Unlock()
		p.kill("protocol error")
	}
}

func (p *Process) handleMessage(m Message) error {
	p.mu.Lock()
	if p.state == stateHandshake {
		p.mu.Unlock()
		h, ok := m.(Hello)
		if !ok {
			return protoErr(CodeUnexpected, "%s before hello", m.Type())
		}
		select {
		case p.helloCh <- h:
			return nil
		default:
			return protoErr(CodeUnexpected, "second hello")
		}
	}
	if p.killReason != "" {
		// A process being killed may still deliver frames; nothing it
		// says counts any more.
		p.mu.Unlock()
		return nil
	}
	var target *invocation
	var calls []func()
	var err error
	switch m := m.(type) {
	case Result:
		target, calls, err = p.onResult(m)
	case Late:
		target, calls, err = p.onLate(m)
	case Log:
		target, calls = p.onLog(m)
	case Ping:
		// A pong that cannot be sent means the conn is closing, which the
		// reader reports on its own.
		p.conn.send(Pong{ID: m.ID})
		if m.Health != nil {
			p.health = m.Health
		}
	case Pong:
		if ch, ok := p.pings[m.ID]; ok {
			delete(p.pings, m.ID)
			ch <- m.Health
		}
		if m.Health != nil {
			p.health = m.Health
		}
	case Bye:
		p.bye = true
	default:
		err = protoErr(CodeUnexpected, "%s from a harness", m.Type())
	}
	p.mu.Unlock()
	if target != nil {
		target.calls.do(calls...)
	}
	return err
}

// onResult handles a result frame. It returns the invocation and the
// Reporter calls and deliveries to queue for it once p.mu is released.
func (p *Process) onResult(r Result) (*invocation, []func(), error) {
	inv := p.invs[r.ID]
	if inv == nil {
		return nil, nil, protoErr(CodeUnexpected, "result for invocation %q, which is not in flight", r.ID)
	}
	switch inv.state {
	case invPending:
		out := stepOutcome(r)
		p.inFlight--
		var calls []func()
		if inv.fixed != nil {
			calls = append(calls, inv.fixed)
		}
		if r.Settled {
			p.settleLocked(inv)
			calls = append(calls, func() { inv.report.Settled(engine.SettleCompleted) })
		} else {
			inv.state = invFixed
		}
		return inv, append(calls, func() { inv.outcome <- out }), nil
	case invAbandoned:
		l := lateOf(r)
		calls := []func(){func() { inv.report.Late(l) }}
		if r.Settled {
			p.abandoned--
			p.settleLocked(inv)
			calls = append(calls, func() { inv.report.Settled(engine.SettleCompleted) }, p.notifyFunc())
		}
		return inv, calls, nil
	default:
		return nil, nil, protoErr(CodeUnexpected, "second result for invocation %q", r.ID)
	}
}

func (p *Process) onLate(l Late) (*invocation, []func(), error) {
	late := engine.Late{Kind: engine.LateKind(l.Kind), Body: l.Body, Final: l.Settled}
	if inv := p.invs[l.ID]; inv != nil {
		if inv.state == invPending {
			return nil, nil, protoErr(CodeUnexpected, "late frame for invocation %q, which has no outcome", l.ID)
		}
		calls := []func(){func() { inv.report.Late(late) }}
		if l.Settled {
			calls = append(calls, func() { inv.report.Settled(engine.SettleCompleted) })
			if inv.state == invAbandoned {
				p.abandoned--
				calls = append(calls, p.notifyFunc())
			}
			p.settleLocked(inv)
		}
		return inv, calls, nil
	}
	if inv, ok := p.recent.get(l.ID); ok {
		if l.Settled {
			return nil, nil, protoErr(CodeUnexpected, "invocation %q settled twice", l.ID)
		}
		return inv, []func(){func() { inv.report.Late(late) }}, nil
	}
	p.diag.printf("dropped a late %s for unknown invocation %q", l.Kind, l.ID)
	return nil, nil, nil
}

func (p *Process) onLog(l Log) (*invocation, []func()) {
	inv := p.invs[l.ID]
	if inv != nil && inv.state == invPending {
		for _, line := range l.Lines {
			inv.tail.add(line)
		}
		return nil, nil
	}
	if inv == nil {
		var ok bool
		if inv, ok = p.recent.get(l.ID); !ok {
			return nil, nil
		}
	}
	late := engine.Late{Kind: engine.LateLog, Body: linesValue(l.Lines)}
	return inv, []func(){func() { inv.report.Late(late) }}
}

// settleLocked moves inv from the in-flight table to the recent ones,
// which keep receiving late output of work it left running.
func (p *Process) settleLocked(inv *invocation) {
	delete(p.invs, inv.id)
	p.recent.add(inv.id, inv)
}

func (p *Process) notifyFunc() func() {
	return func() {
		if p.cfg.notify != nil {
			p.cfg.notify(p)
		}
	}
}

// readerDone runs on the reader goroutine when it stops: at the end of the
// stream, on a read or protocol error, or when the socket is closed. It
// waits for the process to exit and then fails what is still in flight.
func (p *Process) readerDone(err error) {
	// Nothing new is dispatched from here on: the process is ending, even
	// if its exit takes a moment to observe.
	p.mu.Lock()
	p.ending = true
	p.mu.Unlock()
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, os.ErrDeadlineExceeded) {
		p.mu.Lock()
		if p.readErr == nil {
			p.readErr = err
		}
		dying := p.killReason != ""
		p.mu.Unlock()
		if !dying && IsProtocolError(err, "") {
			p.diag.printf("protocol error, killing the harness: %v", err)
		}
		p.kill("connection failed")
	}
	select {
	case <-p.waitDone:
	case <-time.After(exitReadGrace):
		// The socket ended but the process lives on: it can no longer be
		// spoken to, so it is ended.
		p.kill("connection closed")
		<-p.waitDone
	}
	p.conn.close()

	p.mu.Lock()
	p.state = stateExited
	status := p.status
	invs := p.invs
	p.invs = map[string]*invocation{}
	p.recent = recentInvocations{limit: p.recent.limit}
	p.inFlight, p.abandoned = 0, 0
	for id, ch := range p.pings {
		close(ch)
		delete(p.pings, id)
	}
	p.mu.Unlock()

	// A process that dies fails its in-flight invocations as escaped, with
	// its real exit code or the signal that ended it (ADR 0004, WR-4).
	ids := make([]string, 0, len(invs))
	for id := range invs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		inv := invs[id]
		var calls []func()
		if inv.state == invPending && inv.fixed != nil {
			calls = append(calls, inv.fixed)
		}
		calls = append(calls, func() { inv.report.Settled(engine.SettleProcessExited) })
		if inv.state == invPending {
			out := engine.StepOutcome{
				Kind:     engine.OutcomeEscaped,
				Tail:     inv.tail.String(),
				ExitCode: status.Code,
				Signal:   status.Signal,
			}
			calls = append(calls, func() { inv.outcome <- out })
		}
		inv.calls.do(calls...)
	}
	p.closeStdio()
	os.RemoveAll(p.dir)
	close(p.exited)
	if p.cfg.notify != nil {
		p.cfg.notify(p)
	}
}

// dispatch is how far exec got with an invocation.
type dispatch int

const (
	// notDispatched: the invocation never reached the harness; nothing
	// settled it yet.
	notDispatched dispatch = iota
	// completed: an outcome was returned.
	completed
	// abandoned: ctx ended after dispatch.
	abandoned
)

var errUnavailable = errors.New("harness: process is not serving")

// Execute runs inv on this process, with the settlement contract of
// engine.StepExecutor.
func (p *Process) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	if inv.Report == nil {
		inv.Report = engine.NopReporter{}
	}
	out, d, err := p.exec(ctx, inv, nil)
	if d == notDispatched {
		inv.Report.Settled(engine.SettleNotStarted)
	}
	return out, err
}

// exec runs inv. fixed, if not nil, is called once the engine stops waiting
// for the invocation (see invocation.fixed), but never for one that was
// not dispatched.
func (p *Process) exec(ctx context.Context, inv engine.Invocation, fixed func()) (engine.StepOutcome, dispatch, error) {
	if err := checkInvocation(inv, p.Hello().Caps); err != nil {
		return engine.StepOutcome{}, notDispatched, err
	}
	payload, err := Encode(Invoke{
		ID:       inv.ID,
		Run:      inv.Run,
		Site:     inv.Site.String(),
		Script:   inv.Script,
		Entry:    inv.Entry,
		Args:     inv.Args,
		Env:      inv.Env,
		Boundary: string(inv.Boundary),
	})
	if err != nil {
		return engine.StepOutcome{}, notDispatched, fmt.Errorf("harness: invoke %q: %w", inv.ID, err)
	}
	if ctx.Err() != nil {
		return engine.StepOutcome{}, notDispatched, context.Cause(ctx)
	}
	e := &invocation{id: inv.ID, run: inv.Run, report: inv.Report, outcome: make(chan engine.StepOutcome, 1), fixed: fixed}
	p.mu.Lock()
	if p.state != stateServing || p.killReason != "" || p.ending {
		p.mu.Unlock()
		return engine.StepOutcome{}, notDispatched, errUnavailable
	}
	if _, dup := p.invs[inv.ID]; dup {
		p.mu.Unlock()
		return engine.StepOutcome{}, notDispatched, fmt.Errorf("harness: invocation %q is already in flight", inv.ID)
	}
	if err := p.conn.sendPayload(payload, inv.ID); err != nil {
		p.mu.Unlock()
		if errors.Is(err, net.ErrClosed) {
			return engine.StepOutcome{}, notDispatched, errUnavailable
		}
		return engine.StepOutcome{}, notDispatched, fmt.Errorf("harness: invoke %q: %w", inv.ID, err)
	}
	p.invs[inv.ID] = e
	p.inFlight++
	p.mu.Unlock()

	select {
	case out := <-e.outcome:
		return out, completed, nil
	case <-ctx.Done():
	}
	p.mu.Lock()
	if e.state != invPending || p.invs[inv.ID] != e {
		// The outcome won the race: it is in the channel or on its way.
		p.mu.Unlock()
		return <-e.outcome, completed, nil
	}
	e.state = invAbandoned
	p.inFlight--
	p.abandoned++
	reason := engine.CancelReasonOf(ctx)
	p.conn.send(Cancel{ID: inv.ID, Reason: string(reason)})
	p.mu.Unlock()
	if fixed != nil {
		fixed()
	}
	p.notifyFunc()()
	return engine.StepOutcome{}, abandoned, context.Cause(ctx)
}

var entryName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkInvocation refuses invocations this harness cannot run. They never
// reach it.
func checkInvocation(inv engine.Invocation, caps Caps) error {
	switch {
	case inv.ID == "":
		return errors.New("harness: invocation without an id")
	case inv.Args.IsZero():
		return fmt.Errorf("harness: invocation %q has no arguments", inv.ID)
	case !entryName.MatchString(inv.Entry):
		return fmt.Errorf("harness: invocation %q: entrypoint %q is not a valid name", inv.ID, inv.Entry)
	case inv.ModuleState != "" && inv.ModuleState != engine.ModuleStateWarm:
		return fmt.Errorf("harness: invocation %q: module-state mode %q is not implemented (milestone C3b)", inv.ID, inv.ModuleState)
	}
	switch inv.Boundary {
	case engine.BoundaryBun, "":
	case engine.BoundaryNative:
		if !caps.Native {
			return fmt.Errorf("harness: invocation %q: the harness has no native boundary (milestone C3n)", inv.ID)
		}
	default:
		return fmt.Errorf("harness: invocation %q: unknown boundary %q", inv.ID, inv.Boundary)
	}
	return nil
}

// Ping asks the harness for its health and waits for the pong.
func (p *Process) Ping(ctx context.Context) (*Health, error) {
	p.mu.Lock()
	if p.state != stateServing && p.state != stateDraining {
		p.mu.Unlock()
		return nil, errUnavailable
	}
	p.pingSeq++
	id := fmt.Sprintf("e%d", p.pingSeq)
	ch := make(chan *Health, 1)
	p.pings[id] = ch
	err := p.conn.send(Ping{ID: id})
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case h, ok := <-ch:
		if !ok {
			return nil, errUnavailable
		}
		return h, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pings, id)
		p.mu.Unlock()
		return nil, context.Cause(ctx)
	}
}

// Drain sends drain and waits for the harness to say bye and exit. When
// ctx ends first the process is killed. Invocations still in flight end
// with the process.
func (p *Process) Drain(ctx context.Context) error {
	p.mu.Lock()
	if p.state == stateServing {
		p.state = stateDraining
		p.conn.send(Drain{})
	}
	p.mu.Unlock()
	select {
	case <-p.exited:
		return nil
	case <-ctx.Done():
		p.kill("drain timed out")
		<-p.exited
		return context.Cause(ctx)
	}
}

// Kill ends the process with SIGKILL.
func (p *Process) Kill() { p.kill("killed by the engine") }

// kill sends SIGKILL through the process handle. The handle is pinned to
// the process the engine spawned: on Linux it is a pidfd, and elsewhere Go
// does not signal a child it has already reaped, so a reused pid is never
// hit. The handshake checked that this process is the one speaking the
// protocol.
func (p *Process) kill(reason string) {
	p.mu.Lock()
	if p.killReason == "" {
		p.killReason = reason
	}
	p.mu.Unlock()
	if p.cmd != nil {
		p.cmd.Process.Signal(syscall.SIGKILL)
	}
}

// watchdog kills the process when no frame has arrived from it for
// killAfter (ADR 0004 host mode item 4, spawned processes). Silence is
// measured from received frames only, so the engine's own blocked writes,
// such as a large invoke the harness is slow to read, do not count.
func (p *Process) watchdog() {
	for {
		silence := time.Since(p.conn.lastReceived())
		if silence >= p.killAfter {
			p.mu.Lock()
			var runs []string
			for _, inv := range p.invs {
				runs = append(runs, fmt.Sprintf("run %s invocation %s", inv.run, inv.id))
			}
			p.mu.Unlock()
			slices.Sort(runs)
			p.diag.printf("watchdog: no frame for %s (killAfter %s); sending SIGKILL; in flight: %s",
				silence.Round(time.Millisecond), p.killAfter, orNone(runs))
			p.kill("watchdog")
			return
		}
		select {
		case <-time.After(p.killAfter - silence):
		case <-p.exited:
			return
		}
	}
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

// PID returns the harness's process id.
func (p *Process) PID() int { return p.pid }

// Dir returns the process's private working directory, which is removed
// when the process exits.
func (p *Process) Dir() string { return p.dir }

// Hello returns the harness's hello.
func (p *Process) Hello() Hello {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hello
}

// Done is closed when the process has exited and every invocation in
// flight in it has been failed or settled; their Reporter calls may still
// be running on their own goroutines.
func (p *Process) Done() <-chan struct{} { return p.exited }

// ExitStatus returns how the process ended; it is meaningful once Done is
// closed.
func (p *Process) ExitStatus() ExitStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// Bye reports whether the harness said bye.
func (p *Process) Bye() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bye
}

// Load returns the number of invocations in flight that the engine still
// waits for, and of abandoned ones that have not settled.
func (p *Process) Load() (inFlight, abandoned int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inFlight, p.abandoned
}

// Health returns the last health the harness reported, or nil.
func (p *Process) Health() *Health {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.health
}

// Silence returns how long ago the last frame arrived.
func (p *Process) Silence() time.Duration { return time.Since(p.conn.lastReceived()) }

// Lag estimates how far the harness's event loop is behind: the larger of
// the lag it last reported and how overdue its next heartbeat is.
func (p *Process) Lag() time.Duration {
	lag := p.Silence() - p.heartbeat
	if h := p.Health(); h != nil {
		lag = max(lag, time.Duration(h.LagMS)*time.Millisecond)
	}
	return max(lag, 0)
}

func stepOutcome(r Result) engine.StepOutcome {
	switch r.Outcome {
	case OutcomeOK:
		return engine.StepOutcome{Kind: engine.OutcomeOK, Body: r.Body}
	case OutcomeError:
		return engine.StepOutcome{Kind: engine.OutcomeError, Body: r.Body}
	case OutcomeEscaped:
		return engine.StepOutcome{Kind: engine.OutcomeEscaped, Tail: r.Tail, ExitCode: r.ExitCode}
	default:
		return engine.StepOutcome{Kind: engine.OutcomeEmpty}
	}
}

// lateOf turns the result of an abandoned invocation into the late output
// that reports how its call ended.
func lateOf(r Result) engine.Late {
	switch r.Outcome {
	case OutcomeOK:
		return engine.Late{Kind: engine.LateResult, Body: r.Body, Final: r.Settled}
	case OutcomeError:
		return engine.Late{Kind: engine.LateError, Body: r.Body, Final: r.Settled}
	default:
		return engine.Late{Kind: engine.LateError, Final: r.Settled}
	}
}

// linesValue is the body of a late log: the lines as a JSON array of
// strings. Lines come from a decoded frame, so they are valid UTF-8.
func linesValue(lines []string) jsonval.Value {
	b, _ := appendJSONStrings(nil, lines)
	return jsonval.FromProducer(b)
}

func exitStatus(ps *os.ProcessState) ExitStatus {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ExitStatus{Code: -1, Signal: signalName(ws.Signal())}
	}
	return ExitStatus{Code: ps.ExitCode()}
}

// copyLines forwards one of the process's output pipes until every writer
// has closed it, or until the engine closes it after the exit grace.
func (p *Process) copyLines(r *os.File, w io.Writer) {
	defer p.stdioWG.Done()
	io.Copy(w, r)
}

// closeStdio waits for the output pipes to reach their end until
// exitReadGrace after the exit, then closes their read ends: a child that
// inherited a writer and outlives the harness must not keep the engine's
// descriptors and goroutines.
func (p *Process) closeStdio() {
	done := make(chan struct{})
	go func() {
		p.stdioWG.Wait()
		close(done)
	}()
	p.mu.Lock()
	until := p.exitAt.Add(exitReadGrace)
	p.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Until(until)):
	}
	p.outR.Close()
	p.errR.Close()
	<-done
}

// socketpair returns the two ends of a stream socketpair, both
// close-on-exec; os/exec clears the flag on the end it maps to fd 3.
func socketpair() (parent, child *os.File, err error) {
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, fmt.Errorf("harness: socketpair: %w", err)
	}
	return os.NewFile(uintptr(fds[0]), "harness-engine"), os.NewFile(uintptr(fds[1]), "harness-child"), nil
}

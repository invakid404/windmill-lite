package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	// ModuleState is the process's module-state mode (ADR 0006): warm,
	// the default, serves any number of invocations; fresh serves one,
	// ends with it as Windmill's job process does, has no watchdog, and is
	// killed when the engine stops waiting for its invocation.
	ModuleState engine.ModuleState
	// EngineVersion is sent in the welcome.
	EngineVersion string

	// KillAfter is the watchdog threshold: the process is killed when no
	// frame has arrived from it for that long. Zero means
	// DefaultKillAfter; WatchdogOff disables the watchdog. A fresh process
	// has none: it is the invocation, which only its own timeout ends
	// (ADR 0004, case watchdog/finite-cpu).
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

	// Facade is the platform facade's origin for this process, or "" when
	// the engine serves no platform calls (ADR 0007, transports). The
	// process gets it as BASE_URL, over any value Env has. A warm process
	// is told it in the welcome and intercepts every fetch to it, which
	// becomes a platform frame that Platform answers (nil answers each with
	// a 501); its origin must be one where nothing listens, so any other
	// transport is refused. A fresh process is not told it, so its calls
	// go over loopback TCP to an origin that listens.
	Facade   string
	Platform PlatformHandler

	// notify is called, outside every lock, after an invocation is
	// abandoned and after an abandoned invocation settles completed, before
	// its Reporter hears of either, and once the process has exited, before
	// the Reporters of the invocations in it hear Settled(process_exited).
	// So the pool's rules have run by the time a Reporter is told. The pool
	// sets it.
	notify func(*Process)
}

// PlatformHandler answers platform requests, the fetches of invocations
// to the facade (ADR 0004, ADR 0007; internal/platform/facade implements
// it). The Process calls it on the connection's reader, in the order the
// frames arrive, so it must return at once: it attributes and records the
// request, and returns the function that answers it, which runs on a
// goroutine of its own with a context that ends when the process does.
// Every platform frame that arrived before an invocation's result has
// therefore been handed to it before Execute returns that result.
type PlatformHandler func(req PlatformRequest) func(ctx context.Context) PlatformReply

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
// when the engine has one, FORCE_COLOR=1, DO_NOT_TRACK=1,
// BUN_INSTALL_CACHE_DIR, BUN_RUNTIME_TRANSPILER_CACHE_PATH=0, and NODE_PATH
// when the engine has one. BASE_URL comes from ProcessConfig.Facade
// (milestone PF1).
//
// The engine stands in for the worker (WR-22/b): BUN_INSTALL_CACHE_DIR is
// <WINDMILL_DIR>/cache_nomount/bun, with WINDMILL_DIR read from the
// engine's environment and /tmp/windmill when it is unset or empty, so
// /tmp/windmill/cache_nomount/bun by default, as on the reference image;
// NODE_PATH is the engine's own, copied only when the engine has one.
func BaseEnv() []string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	if tz, ok := os.LookupEnv("TZ"); ok {
		env = append(env, "TZ="+tz)
	}
	dir := os.Getenv("WINDMILL_DIR")
	if dir == "" {
		dir = "/tmp/windmill"
	}
	env = append(env, "FORCE_COLOR=1", "DO_NOT_TRACK=1", "BUN_INSTALL_CACHE_DIR="+dir+"/cache_nomount/bun", "BUN_RUNTIME_TRANSPILER_CACHE_PATH=0")
	if np, ok := os.LookupEnv("NODE_PATH"); ok {
		env = append(env, "NODE_PATH="+np)
	}
	return env
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

// Process is one harness process and the engine side of its connection:
// the handshake, the invocations in flight, health and the watchdog. It is
// a process the engine spawned (Spawn), or in host mode the host the engine
// is a child of (AttachHost). It is safe for concurrent use.
type Process struct {
	cfg        ProcessConfig
	cmd        *exec.Cmd
	host       *hostSide // nil for a spawned process
	conn       *conn
	dir        string
	argsMu     sync.Mutex // orders the writes of args.json in dir
	pid        int
	killAfter  time.Duration
	heartbeat  time.Duration
	maxMessage int64
	diag       *diagnostics
	stdio      *lineWriter

	// group is whether the process leads a process group of its own, as
	// every process the engine spawns does (WR-55), and groupGone whether
	// the group has been seen empty, or killed, since the process exited,
	// after which its id is never signalled again.
	group     bool
	groupGone atomic.Bool

	waitDone chan struct{} // closed when cmd.Wait has returned
	outR     *os.File      // the read ends of stdout and stderr
	errR     *os.File
	stdioWG  sync.WaitGroup
	exited   chan struct{} // closed when the exit has been handled
	// life ends when the connection does; platform handlers and a host's
	// runs run under it.
	life    context.Context
	endLife context.CancelCauseFunc

	mu         sync.Mutex
	state      procState
	helloCh    chan Hello
	hello      Hello
	invs       map[string]*invocation
	served     bool                               // a fresh process's one invocation was dispatched
	runs       map[string]context.CancelCauseFunc // a host's runs in flight
	recent     recentInvocations
	inFlight   int
	abandoned  int
	pingSeq    uint64
	pings      map[string]chan *Health
	health     *Health
	bye        bool
	ending     bool      // the reader stopped or the process exited
	holding    bool      // a fresh result waits for the process group (WR-55)
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
	outcome chan execResult // buffered, receives exactly one value
	// calls runs the invocation's Reporter calls and its outcome delivery,
	// one at a time and in order, away from the connection's reader: a slow
	// Reporter neither stalls the frames of other invocations nor looks
	// like silence to the watchdog.
	calls serial
	// fixed, if set, runs once when the engine stops waiting for the
	// invocation: first in calls when its outcome is fixed or it is
	// abandoned, before any Reporter call. A pool frees the invocation's
	// slot there, so a Reporter that runs another step does not wait for
	// its own.
	fixed func()
	// held is a fresh process's settled outcome, which waits for the
	// process to exit and its process group to empty (WR-55).
	held *engine.StepOutcome
	// heldKilled is set when the held outcome was dropped because the
	// job's group was killed, or the connection failed, before it emptied.
	heldKilled bool
	// children are the process groups of the child processes the
	// invocation started in a warm or host process (Child frames), which
	// the engine kills when the invocation is abandoned or the process
	// ends; childrenKilled is set once it has, after which one announced
	// later is killed at once. Guarded by Process.mu.
	children       []*childGroup
	childrenKilled bool
}

// execResult is what an invocation's outcome channel delivers: an
// outcome, or in host mode the error of a lost host.
type execResult struct {
	out engine.StepOutcome
	err error
}

// Spawn starts a harness process and completes the handshake. On success
// the process is serving and its watchdog runs.
func Spawn(ctx context.Context, cfg ProcessConfig) (*Process, error) {
	p, timeout, err := newProcess(cfg)
	if err != nil {
		return nil, err
	}
	return p.spawn(ctx, timeout)
}

// newProcess applies cfg's defaults and checks them.
func newProcess(cfg ProcessConfig) (*Process, time.Duration, error) {
	if cfg.ModuleState == "" {
		cfg.ModuleState = engine.ModuleStateWarm
	}
	if cfg.ModuleState != engine.ModuleStateWarm && cfg.ModuleState != engine.ModuleStateFresh {
		return nil, 0, fmt.Errorf("harness: unknown module-state mode %q", cfg.ModuleState)
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
		runs:       map[string]context.CancelCauseFunc{},
		pings:      map[string]chan *Health{},
	}
	if p.killAfter == 0 {
		p.killAfter = DefaultKillAfter
	}
	if cfg.ModuleState == engine.ModuleStateFresh {
		p.killAfter = WatchdogOff
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
		return nil, 0, errors.New("harness: Heartbeat and MaxMessage cannot be negative")
	case p.killAfter > 0 && p.heartbeat >= p.killAfter:
		// An idle harness would look silent between two heartbeats.
		return nil, 0, fmt.Errorf("harness: heartbeat %s must be shorter than the watchdog's killAfter %s", p.heartbeat, p.killAfter)
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
	p.life, p.endLife = context.WithCancelCause(context.Background())
	return p, timeout, nil
}

func (p *Process) spawn(ctx context.Context, timeout time.Duration) (*Process, error) {
	cfg := p.cfg
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
	if cfg.Facade != "" {
		// WA-2: the worker sets BASE_URL last, over any value of the same
		// name, before any module loads (WA-1).
		cmd.Env = append(slices.Clone(cmd.Env), "BASE_URL="+cfg.Facade)
	}
	cmd.Dir = dir
	cmd.ExtraFiles = []*os.File{child} // fd 3 in the child
	// Every spawned process leads a process group of its own (WR-55), and
	// its kill and its end reach the group. A fresh process is its
	// invocation's job process: the step's children join its group unless
	// they start a session of their own, and its outcome waits for them
	// (awaitGroup). A warm process's steps give their children groups of
	// their own (Child frames); what stays in its group is what a
	// synchronous child call started, which the end of the process kills.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p.group = true
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
		// A fresh process writes nothing after its settled result, so the
		// reader stops at once; the result waits for the group, not for a
		// child that holds the socket.
		grace := exitReadGrace
		if p.holding {
			grace = 0
		}
		p.mu.Unlock()
		close(p.waitDone)
		nc.SetReadDeadline(time.Now().Add(grace))
	}()
	p.conn = newConn(nc, connConfig{maxMessage: p.maxMessage, onMessage: p.handle, onClose: p.readerDone})
	p.conn.run()

	if err := p.handshake(ctx, timeout); err != nil {
		return nil, err
	}
	if p.killAfter > 0 {
		go p.watchdog()
	}
	go p.watchChildren()
	return p, nil
}

func (p *Process) handshake(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var h Hello
	select {
	case h = <-p.helloCh:
	case <-p.exited:
		if p.host != nil {
			return errors.New("harness: the host's connection ended before its hello")
		}
		return fmt.Errorf("harness exited before its hello (%s)%s", p.ExitStatus(), p.stdio.lastLines())
	case <-timer.C:
		p.kill("no hello within " + timeout.String())
		<-p.exited
		if p.host != nil {
			return fmt.Errorf("harness: the host sent no hello within %s", timeout)
		}
		return fmt.Errorf("harness sent no hello within %s%s", timeout, p.stdio.lastLines())
	case <-ctx.Done():
		p.kill("handshake canceled")
		<-p.exited
		return context.Cause(ctx)
	}
	w := Welcome{Proto: ProtocolVersion, Engine: p.cfg.EngineVersion, Bundle: p.cfg.Bundle}
	var reject *Reject
	switch {
	case p.host != nil && p.host.refuse != nil:
		// The engine cannot serve this host at all, and says why.
		reject = p.host.refuse
	case h.Proto != ProtocolVersion:
		reject = &Reject{RejectProto, fmt.Sprintf("protocol %d, engine speaks %d", h.Proto, ProtocolVersion)}
	case h.Harness.Kind != p.cfg.Kind:
		reject = &Reject{RejectHarness, fmt.Sprintf("harness kind %q, want %q", h.Harness.Kind, p.cfg.Kind)}
	case h.Runtime != p.cfg.Runtime:
		reject = &Reject{RejectRuntime, fmt.Sprintf("runtime %q, the bundle pins %q", h.Runtime, p.cfg.Runtime)}
	case h.Bundle != p.cfg.Bundle:
		reject = &Reject{RejectBundle, fmt.Sprintf("bundle %q, want %q", h.Bundle, p.cfg.Bundle)}
	case p.host == nil && h.PID != p.pid:
		// The engine would otherwise signal a process other than the one
		// that speaks the protocol, such as a wrapper script.
		reject = &Reject{RejectPID, fmt.Sprintf("hello from pid %d, the engine spawned pid %d", h.PID, p.pid)}
	case p.host != nil && !h.Caps.Host:
		reject = &Reject{RejectHarness, "a harness that connects in host mode must offer the host capability"}
	case p.cfg.ModuleState == engine.ModuleStateFresh && !h.Caps.Fresh:
		reject = &Reject{RejectHarness, "a harness spawned in fresh mode must offer the fresh capability"}
	case p.host != nil && h.PID != p.host.getppid():
		// In host mode the engine is the host's child: the process its
		// watchdog would signal is its parent, and the one that greets it
		// must be that process (ADR 0004 host mode item 4).
		reject = &Reject{RejectPID, fmt.Sprintf("hello from pid %d, the engine's parent is pid %d", h.PID, p.host.getppid())}
	case p.host != nil && p.host.workdir != "":
		// The host's working directory is where its steps run and where
		// the engine writes their args.json (ADR 0004, the shared-process
		// failure model, item 5).
		reject = CheckWorkdir(p.host.workdir, h.Workdir)
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
	if p.cfg.ModuleState != engine.ModuleStateFresh {
		// Only a warm or host-mode process intercepts; a fresh one is
		// its invocation and reaches the facade over TCP (ADR 0007).
		w.Config.Facade = p.cfg.Facade
	}
	p.mu.Lock()
	if p.state == stateHandshake {
		p.state = stateServing
	}
	p.hello = h
	if p.host != nil {
		p.pid = h.PID
		p.diag.pid.Store(int64(h.PID))
		// The host's working directory, checked above, takes each
		// invocation's args.json, as a warm process's own directory does
		// (it is the host's, so the engine never removes it). It is set
		// before the welcome goes out: the host may submit a run as soon as
		// it reads it.
		p.dir = p.host.dir
	}
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
	if c, ok := m.(Child); ok {
		kill := p.onChild(c)
		p.mu.Unlock()
		killChildGroups(kill)
		return nil
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
	case Warning:
		target, calls = p.onWarning(m)
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
	case PlatformRequest:
		p.onPlatform(m)
	case RunRequest:
		err = p.onRun(m)
	case Drain:
		err = p.onHostDrain()
	case RunCancel:
		if p.host == nil {
			err = protoErr(CodeUnexpected, "run_cancel from a harness that is not in host mode")
		} else if cancel := p.runs[m.ID]; cancel != nil {
			// A run that already ended is no longer in the table; its
			// cancel crossed the run_done on the wire.
			cancel(engine.CancelCanceled)
		}
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
		if r.Settled && p.cfg.ModuleState == engine.ModuleStateFresh {
			// WR-55: the job of a fresh process completes once the process
			// has exited and no process is left in its group, so the
			// outcome is held, and the invocation stays in flight, until
			// readerDone delivers it. A timeout or a cancellation meanwhile
			// abandons it and kills the group (exec).
			inv.held = &out
			p.holding = true
			return inv, nil, nil
		}
		p.inFlight--
		var calls []func()
		if inv.fixed != nil {
			calls = append(calls, inv.fixed)
		}
		if r.Settled {
			calls = append(calls, p.settleLocked(inv), func() { inv.report.Settled(engine.SettleCompleted) })
		} else {
			inv.state = invFixed
		}
		return inv, append(calls, func() { inv.outcome <- execResult{out: out} }), nil
	case invAbandoned:
		l := lateOf(r)
		calls := []func(){func() { inv.report.Late(l) }}
		if r.Settled {
			p.abandoned--
			calls = append(calls, p.settleLocked(inv), p.notifyFunc(), func() { inv.report.Settled(engine.SettleCompleted) })
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
			if inv.state == invAbandoned {
				p.abandoned--
				calls = append(calls, p.notifyFunc())
			}
			calls = append(calls, p.settleLocked(inv), func() { inv.report.Settled(engine.SettleCompleted) })
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

// onWarning hands a warning to the Reporter of the invocation it names,
// which has not settled: the harness sends warnings only for invocations in
// flight, and ahead of the frame that settles them. One for an invocation
// that is not in flight is dropped.
func (p *Process) onWarning(w Warning) (*invocation, []func()) {
	inv := p.invs[w.ID]
	if inv == nil {
		p.diag.printf("dropped a warning for invocation %q, which is not in flight: %s", w.ID, w.Message)
		return nil, nil
	}
	warning := engine.Warning{Code: w.Code, Message: w.Message, Left: w.Left, Untracked: w.Untracked}
	return inv, []func(){func() { inv.report.Warning(warning) }}
}

// onChild records a child process group an invocation announced (WR-55),
// with p.mu held, and returns the groups to kill now: the group itself
// when the process or the host's connection is ending, the invocation was
// abandoned, or it has settled (its settlement killed its children, so one
// its queued code starts afterwards dies at once, D9). One for an
// invocation the process never ran is dropped, and so is one that names
// the process itself, which its end kills, or a group that has already
// ended.
func (p *Process) onChild(c Child) []*childGroup {
	if c.PID == p.pid {
		p.diag.printf("dropped a child frame for invocation %q that names the harness itself", c.ID)
		return nil
	}
	g, ok := newChildGroup(c.PID, c.Token)
	inv := p.invs[c.ID]
	switch {
	case !ok:
		return nil
	case p.killReason != "" || p.ending:
		// The process or the host's connection is ending, its invocations
		// with it; a host's side channel may still deliver this.
		return []*childGroup{g}
	case inv == nil:
		if _, ok := p.recent.get(c.ID); ok {
			return []*childGroup{g}
		}
		p.diag.printf("dropped a child frame for invocation %q, which is not in flight", c.ID)
		return nil
	case inv.childrenKilled:
		return []*childGroup{g}
	}
	inv.children = append(inv.children, g)
	return nil
}

// takeChildren returns inv's announced child groups and marks them killed,
// with p.mu held: a group it announces afterwards is killed at once.
func takeChildren(inv *invocation) []*childGroup {
	gs := inv.children
	inv.children, inv.childrenKilled = nil, true
	return gs
}

// groupMember is one process of a process group: its pid, and when it
// started, which tells it from a later process given the same pid.
type groupMember struct {
	pid   int
	start uint64
}

// childGroup is a process group a step's child leads (a Child frame). The
// engine alone kills it: on the invocation's abandonment, at its
// settlement and when the process ends (the harness only reports it). Its
// numeric id is not enough to name it, since the kernel gives the id to
// another process once the group has ended. The harness starts the child
// with a guardian in its group, a process that does nothing, with the
// group's token in its arguments (guardArg): while it lives the id cannot be given
// to another process, so the group is the original whatever its other
// members do, and once it is the only member left the engine releases the
// group. Without a guardian the group is held with the members the engine
// last saw in it (continues).
type childGroup struct {
	pgid int
	// token is the guardian's (Child.Token), or "" for an unguarded group.
	token string
	// since is when the engine learned of the group, which sets how often
	// it is looked at (childWatchYoung).
	since time.Time
	mu    sync.Mutex
	// seen are the members the engine last saw in the group, each with its
	// start time; nil once the group is ended (end) or given up, for good.
	seen []groupMember
	// guard is the guardian, once the engine has found it among the
	// members.
	guard *groupMember
}

// guardArg is a guardian's sleep operand, its token after the point: the
// argument by which the engine knows it.
func guardArg(token string) string { return "999999999." + token }

// hasGuardArg reports whether args, a process's arguments each ending in a
// NUL, carry the guardian's token: an argument that is guardArg(token),
// the guardian's sleep operand once it runs sleep; or, with script, also
// guardArg(token) as a word of an argument, between spaces, as the
// prologue's script holds it, which every process of the prologue carries
// until it execs (guardedCommand): the guardian among them, from its fork
// until its exec, so a step's command that exits at once cannot leave the
// engine a group in which it knows no process (review round 5's CI).
func hasGuardArg(args []byte, token string, script bool) bool {
	w := []byte(guardArg(token))
	for i := 0; i < len(args); {
		j := bytes.Index(args[i:], w)
		if j < 0 {
			return false
		}
		j += i
		k := j + len(w)
		whole := (j == 0 || args[j-1] == 0) && (k == len(args) || args[k] == 0)
		word := (j == 0 || args[j-1] == 0 || args[j-1] == ' ') && (k == len(args) || args[k] == 0 || args[k] == ' ')
		if whole || script && word {
			return true
		}
		i = j + 1
	}
	return false
}

// The process operations on child groups, replaced in tests so that no
// real signal is sent: the members of groups, whether a process carries a
// group's token, whether a running process reads no arguments, and a
// group's kill.
var (
	groupMembers = groupMembersOf
	isGuard      = processHasGuard
	argsUnread   = processArgsUnread
	signalGroup  = func(pgid int) { syscall.Kill(-pgid, syscall.SIGKILL) }
)

// newChildGroup holds the group that pid leads, as the engine learns of it,
// right after the child started, with the members it has then and the
// guardian's token, if any. ok is false when it has no member: there is
// nothing to own.
//
// A listing is not taken at one instant: it lists the processes that exist
// as it starts and reads each in turn, and the prologue can run its course
// meanwhile, the leader exiting before it is read and the guardian forked
// after the listing started, so that it shows neither. An empty listing is
// taken again: once the first has ended without the leader, the step's
// command has run, so the guardian, forked before it, is in the second
// (review round 5's CI).
func newChildGroup(pid int, token string) (g *childGroup, ok bool) {
	g = &childGroup{pgid: pid, token: token, since: time.Now()}
	now := groupMembers([]int{pid})[pid]
	if len(now) == 0 {
		now = groupMembers([]int{pid})[pid]
	}
	if len(now) == 0 {
		return nil, false
	}
	g.seen = now
	g.findGuard(now)
	return g, true
}

// findGuard looks for the guardian among now, with g.mu held or g not yet
// shared, until it is found: a process of the group whose arguments carry
// the token (hasGuardArg). The group's leader carries the prologue's
// script too until it execs the step's command, so only the guardian's
// sleep operand makes the leader a guardian, never the script. The
// prologue's subshell may be found first, and exits: a later look finds
// the guardian then (continues).
func (g *childGroup) findGuard(now []groupMember) {
	if g.token == "" || g.guard != nil {
		return
	}
	for _, m := range now {
		if isGuard(m.pid, g.token, m.pid != g.pgid) {
			g.guard = &m
			return
		}
	}
}

// verdict is what a look at its members tells of a child group
// (continues).
type verdict int

const (
	// lost: the group is not the step's any more, as far as the engine
	// can tell, and is given up for good.
	lost verdict = iota
	// held: it is the step's group.
	held
	// alone: it is the step's group, with only its guardian left, so
	// nothing of the step is left in it.
	alone
	// undecided: the look could not tell, since a member's arguments read
	// empty while it runs, as they do while it execs, and it may be the
	// guardian in its exec of sleep. The group is kept as it was, and
	// looked at again (review round 5's CI).
	undecided
)

// continues tells whether the group of g's id is still the group the
// step's child started, given its members now, and if so remembers them.
//
//   - While the guardian is among them (found by its token, then known by
//     its pid and start time), it is: the group's id cannot be given to a
//     new process while the group has a member, and the guardian, which
//     the harness started in the group, has been one throughout. A group
//     that took the id after the original ended has no process with the
//     token.
//   - Without a guardian (none was started, or it was killed), it is when
//     one of the members the engine last saw is still among them, the same
//     process: a group that kept one member throughout is the same group,
//     and a replacement shares no member with it. When none is left, which
//     a group whose members all came and went between two looks shares
//     with a replacement, the group is given up for good: it is left
//     running rather than risk killing another owner's processes; unless
//     the group was started with a guardian and a member other than its
//     leader reads no arguments while it runs (argsUnread): the look is
//     undecided then, and the group is left as it was.
func (g *childGroup) continues(now []groupMember) verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.continuesLocked(now)
}

// continuesLocked is continues with g.mu held.
func (g *childGroup) continuesLocked(now []groupMember) verdict {
	if g.seen == nil {
		return lost
	}
	if g.guard != nil && !slices.Contains(now, *g.guard) {
		// It ended: the guardian killed, or a copy of the prologue that
		// carried the token and exited. Another may carry it still.
		g.guard = nil
	}
	g.findGuard(now)
	if g.guard != nil {
		g.seen = now
		if len(now) == 1 {
			return alone
		}
		return held
	}
	for _, m := range now {
		if slices.Contains(g.seen, m) {
			g.seen = now
			return held
		}
	}
	if g.token != "" && slices.ContainsFunc(now, func(m groupMember) bool { return m.pid != g.pgid && argsUnread(m.pid) }) {
		return undecided
	}
	g.seen = nil
	return lost
}

// look returns what continues tells of each of gs, from one listing of the
// groups' members.
func look(gs []*childGroup) []verdict {
	if len(gs) == 0 {
		return nil
	}
	ids := make([]int, len(gs))
	for i, g := range gs {
		ids[i] = g.pgid
	}
	members := groupMembers(ids)
	out := make([]verdict, len(gs))
	for i, g := range gs {
		out[i] = g.continues(members[g.pgid])
	}
	return out
}

// How long end looks again at a group a look left undecided, at most:
// endTries times, endRetry apart, while the member that read no arguments
// finishes its exec.
var (
	endTries = 40
	endRetry = 5 * time.Millisecond
)

// end is the one kill of g's group, by whichever path comes first: a
// cancellation, a settlement or the process's end (killChildGroups), or
// the release of a group with only its guardian left (lookAtChildren). With
// g.mu held, it looks at the group's members afresh and kills the group
// with SIGKILL only when its id can name a group (validGroupID), it is
// still the group the step's child started (continues) and, for a release,
// only its guardian is left. A look left undecided is taken again, a few
// milliseconds later, for a while (endTries); a group still undecided
// then is given up. A group it kills is ended for good, so that
// no other path, holding a look from before the kill, signals its id
// again: the kill ends the guardian that kept the id reserved (review
// round 4, finding 1). The kill follows the look at once; a group that
// still has a member it had cannot be replaced in that instant. The
// members it killed go to the reaper, which reaps those the engine is the
// parent of as they exit (reapLater; review round 5, finding 1). ended
// reports whether the group is ended or given up.
func (g *childGroup) end(release bool) (ended bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen == nil || !validGroupID(g.pgid) {
		return true
	}
	var now []groupMember
	v := undecided
	for try := 0; v == undecided && try < endTries; try++ {
		if try > 0 {
			time.Sleep(endRetry)
		}
		now = groupMembers([]int{g.pgid})[g.pgid]
		v = g.continuesLocked(now)
	}
	switch {
	case v == lost || v == undecided:
		g.seen, g.guard = nil, nil
		return true
	case release && v != alone:
		return false
	}
	signalGroup(g.pgid)
	g.seen, g.guard = nil, nil
	reapLater(g.pgid, 0, now)
	return true
}

// reapAdopted reaps the members of g, a group the engine still holds, that
// it saw at two looks running, prev and its last, and that have exited
// since with the engine as their parent: a descendant a member left, which
// the engine adopted, does not stay a zombie in the group until the group
// ends (reapMember; review round 5, finding 1).
func (g *childGroup) reapAdopted(prev []groupMember) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, m := range g.seen {
		if slices.Contains(prev, m) {
			reapMember(g.pgid, m)
		}
	}
}

// killChildGroups kills each group, as the worker kills a job's group on a
// timeout or a cancellation (WR-55, WR-37), while it is still the group the
// step's child started (end).
func killChildGroups(gs []*childGroup) {
	for _, g := range gs {
		g.end(false)
	}
}

// How often the engine looks at the child groups it holds
// (retireChildren): every childWatchYoung during a group's first
// childYoung, when a child that starts something and exits is most likely
// to turn its group's members over, and every childWatch after that.
var (
	childWatchYoung = 50 * time.Millisecond
	childYoung      = time.Second
	childWatch      = 250 * time.Millisecond
)

// watchChildren keeps the child groups the engine holds up to date until
// the process's life ends: each look renews a group's members, so that a
// group that outlives its leader stays owned through its members'
// turnover, and retires a group that ended or was given up.
func (p *Process) watchChildren() {
	t := time.NewTicker(childWatchYoung)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-p.life.Done():
			return
		case now := <-t.C:
			all := now.Sub(last) >= childWatch
			if all {
				last = now
			}
			p.lookAtChildren(func(g *childGroup) bool { return all || now.Sub(g.since) < childYoung })
		}
	}
}

// retireChildren looks at every child group the engine holds and drops
// those that no longer continue: they ended, or none of the members it
// last saw is left (continues); such a group is never signalled. It also
// releases a group whose guardian is its only member: it kills the
// guardian through end, which looks again under the group's lock, so a
// look that a cancellation's kill of the same group overtook signals
// nothing, and then drops the group.
func (p *Process) retireChildren() { p.lookAtChildren(func(*childGroup) bool { return true }) }

// lookAtChildren is retireChildren for the groups due says to look at.
func (p *Process) lookAtChildren(due func(*childGroup) bool) {
	p.mu.Lock()
	var gs []*childGroup
	for _, inv := range p.invs {
		for _, g := range inv.children {
			if due(g) {
				gs = append(gs, g)
			}
		}
	}
	p.mu.Unlock()
	// The reaper's pass rides on the watcher's looks too, so a group the
	// watcher released is reaped at its next look.
	defer reapPass()
	prev := make([][]groupMember, len(gs))
	for i, g := range gs {
		g.mu.Lock()
		prev[i] = g.seen
		g.mu.Unlock()
	}
	gone := map[*childGroup]bool{}
	for i, v := range look(gs) {
		g := gs[i]
		switch {
		case v == lost:
			gone[g] = true
		case v == alone && g.end(true):
			gone[g] = true
		case v == held:
			g.reapAdopted(prev[i])
		}
	}
	if len(gone) == 0 {
		return
	}
	p.mu.Lock()
	for _, inv := range p.invs {
		inv.children = slices.DeleteFunc(inv.children, func(g *childGroup) bool { return gone[g] })
	}
	p.mu.Unlock()
}

// settleLocked moves inv from the in-flight table to the recent ones,
// which keep receiving late output of work it left running, and returns
// the call that kills its children's groups: its settlement is the D9
// fence's, and the engine kills what the harness reported left behind
// (WR-55), which the caller queues before the Reporter hears Settled.
func (p *Process) settleLocked(inv *invocation) func() {
	delete(p.invs, inv.id)
	kill := takeChildren(inv)
	p.recent.add(inv.id, inv)
	return func() { killChildGroups(kill) }
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
	if p.host != nil {
		p.hostDone(err)
		return
	}
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
	drained := false
	if p.cfg.ModuleState == engine.ModuleStateFresh {
		drained = p.awaitGroup()
	} else {
		p.endGroup()
	}
	status := p.endInvocations()
	p.mu.Lock()
	clean := p.readErr == nil && p.killReason == ""
	if !drained {
		// A held result is released only when the job's group emptied by
		// itself: a kill or a failed connection before that fails the job,
		// although its script returned (WR-55).
		for _, inv := range p.invs {
			if inv.held != nil {
				inv.held, inv.heldKilled = nil, true
			}
		}
	}
	p.mu.Unlock()
	// A process that dies fails its in-flight invocations as escaped, with
	// its real exit code or the signal that ended it (ADR 0004, WR-4). A
	// fresh process is its invocation's job process: when it exits 0
	// before a result, over a connection that never failed, the job's
	// result is empty (WR-19, WR-V2), as after process.exit(0). One whose
	// result was held for its group, which was killed, ended by SIGKILL,
	// whatever the harness itself exited with.
	p.failInvocations(func(inv *invocation) execResult {
		if inv.heldKilled {
			return execResult{out: engine.StepOutcome{Kind: engine.OutcomeEscaped, Tail: inv.tail.String(), ExitCode: -1, Signal: "SIGKILL"}}
		}
		if p.cfg.ModuleState == engine.ModuleStateFresh && clean && status.Signal == "" && status.Code == 0 {
			return execResult{out: engine.StepOutcome{Kind: engine.OutcomeEmpty}}
		}
		return execResult{out: engine.StepOutcome{
			Kind:     engine.OutcomeEscaped,
			Tail:     inv.tail.String(),
			ExitCode: status.Code,
			Signal:   status.Signal,
		}}
	})
	p.closeStdio()
	os.RemoveAll(p.dir)
	p.finishExit()
}

// groupPoll is how often awaitGroup looks at a process group.
const groupPoll = 20 * time.Millisecond

// awaitGroup waits, once a fresh process has exited, until no process is
// left in its process group, while one of its invocations still waits for
// an outcome (WR-55), and reports whether the group drained by itself: it
// was seen empty with no kill requested and the connection intact, so a
// held result stands. Windmill's worker reaps the job's process and then
// waits for every member of its group that is its own child; as PID 1 of
// its container, that is every orphan left in the group, so a background
// child the step started holds the job open until it exits, whether the
// script returned or failed. The engine is not those orphans' parent, so
// it looks at the group with signal 0 until none is left; when it is their
// parent after all (PID 1, or a subreaper), it reaps them, as the worker
// does, or they would stay in the group as zombies. A child that started a
// session of its own is in no group of the engine's and is not waited for.
// A timeout or a cancellation meanwhile abandons the invocation and kills
// the group (exec), and any other kill (Kill, a pool's close, a protocol
// failure) kills it too, which ends the wait undrained.
func (p *Process) awaitGroup() (drained bool) {
	for {
		p.mu.Lock()
		waiting := false
		for _, inv := range p.invs {
			waiting = waiting || inv.state == invPending
		}
		p.mu.Unlock()
		// Each member by its own pid (reapMember), never the group's id: a
		// process that took the process's own pid is no orphan of its.
		for _, m := range groupMembers([]int{p.pid})[p.pid] {
			if m.pid != p.pid {
				reapMember(p.pid, m)
			}
		}
		empty := !groupAlive(p.pid)
		// A kill sets killReason before it signals, so one that emptied
		// the group is seen here.
		p.mu.Lock()
		forced := p.killReason != "" || p.readErr != nil
		p.mu.Unlock()
		if empty {
			p.groupGone.Store(true)
			return !forced
		}
		if !waiting {
			return false
		}
		time.Sleep(groupPoll)
	}
}

// endGroup ends what a warm process left in its process group, once the
// process has exited: what its steps' synchronous child calls started,
// which the steps' own groups do not hold (ADR 0004, failure model item
// 7). The process's steps can run no more, so nothing of them may run on
// (D9, WR-55).
func (p *Process) endGroup() {
	if p.group && !p.groupGone.Load() && groupAlive(p.pid) {
		members := groupMembers([]int{p.pid})[p.pid]
		syscall.Kill(-p.pid, syscall.SIGKILL)
		// What it killed is reaped when the engine is its parent, the
		// process itself excepted: its exec.Cmd waits for it.
		reapLater(p.pid, p.pid, members)
	}
	p.groupGone.Store(true)
}

// groupAlive reports whether a process is left in the process group pgid.
// A member the engine may not signal is still a member.
func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || err == syscall.EPERM
}

// endInvocations marks the process exited and returns its exit status.
// What was in flight stays in p.invs for failInvocations.
func (p *Process) endInvocations() ExitStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = stateExited
	for id, ch := range p.pings {
		close(ch)
		delete(p.pings, id)
	}
	return p.status
}

// failInvocations settles every invocation still in the table with the
// process (SettleProcessExited), kills the child groups they announced,
// and gives each pending one the outcome that result builds, in id order; a pending one with a held result
// (WR-55) gets that result instead, settled completed. It runs after
// endInvocations, and tells the pool of the exit before it queues any
// Reporter call.
func (p *Process) failInvocations(result func(*invocation) execResult) {
	p.mu.Lock()
	invs := p.invs
	p.invs = map[string]*invocation{}
	p.recent = recentInvocations{limit: p.recent.limit}
	p.inFlight, p.abandoned = 0, 0
	var kill []*childGroup
	for _, inv := range invs {
		kill = append(kill, takeChildren(inv)...)
	}
	p.mu.Unlock()
	// The invocations end with the process or the connection, so their
	// children's groups do too, whatever the harness's event loop could
	// still do (WR-55).
	killChildGroups(kill)
	// The pool drops the process before any Reporter hears
	// Settled(process_exited), as it applies its taint rules before an
	// abandoned invocation's Settled(completed).
	p.notifyFunc()()
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
		// A held result settled its invocation; its delivery waited only
		// for the process group (WR-55).
		settle := engine.SettleProcessExited
		if inv.state == invPending && inv.held != nil {
			settle = engine.SettleCompleted
		}
		calls = append(calls, func() { inv.report.Settled(settle) })
		if inv.state == invPending {
			r := result(inv)
			if inv.held != nil {
				r = execResult{out: *inv.held}
			}
			calls = append(calls, func() { inv.outcome <- r })
		}
		inv.calls.do(calls...)
	}
}

// finishExit ends the process's life: its runs and platform handlers see
// their context end, and Done closes. The pool heard of the exit in
// failInvocations.
func (p *Process) finishExit() {
	p.endLife(errUnavailable)
	close(p.exited)
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

// onPlatform hands a platform request to the handler, on the reader and in
// frame order, and answers it on a goroutine of its own, so a slow facade
// never stalls the reader. It runs with p.mu held. A handler that panics or
// answers with a status no Response can carry gets a 502 in its place: one
// bad answer must not end the harness and every invocation in it.
func (p *Process) onPlatform(req PlatformRequest) {
	serve := platformAccept(p.cfg.Platform, req)
	go func() {
		rep := platformAnswer(p.life, serve, req)
		if err := p.conn.send(rep); err != nil && !errors.Is(err, net.ErrClosed) {
			p.diag.printf("platform reply %q not sent: %v", req.ID, err)
		}
	}()
}

func platformText(req PlatformRequest, status int, msg string) PlatformReply {
	return PlatformReply{ID: req.ID, Status: status, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte(msg)}
}

// platformAccept runs the handler's first part; what it returns answers
// the request.
func platformAccept(h PlatformHandler, req PlatformRequest) (serve func(context.Context) PlatformReply) {
	if h == nil {
		return func(context.Context) PlatformReply {
			return platformText(req, 501, "windmill-lite: this engine serves no platform calls")
		}
	}
	defer func() {
		if r := recover(); r != nil {
			serve = func(context.Context) PlatformReply {
				return platformText(req, 502, fmt.Sprintf("windmill-lite: the platform handler failed: %v", r))
			}
		}
	}()
	if serve = h(req); serve == nil {
		return func(context.Context) PlatformReply {
			return platformText(req, 502, "windmill-lite: the platform handler gave no answer")
		}
	}
	return serve
}

func platformAnswer(ctx context.Context, serve func(context.Context) PlatformReply, req PlatformRequest) (rep PlatformReply) {
	text := func(status int, msg string) PlatformReply { return platformText(req, status, msg) }
	defer func() {
		if r := recover(); r != nil {
			rep = text(502, fmt.Sprintf("windmill-lite: the platform handler failed: %v", r))
		}
	}()
	rep = serve(ctx)
	rep.ID = req.ID
	if !finalStatus(rep.Status) {
		return text(502, fmt.Sprintf("windmill-lite: the platform handler answered status %d", rep.Status))
	}
	return rep
}

// exec runs inv. fixed, if not nil, is called once the engine stops waiting
// for the invocation (see invocation.fixed), but never for one that was
// not dispatched.
func (p *Process) exec(ctx context.Context, inv engine.Invocation, fixed func()) (engine.StepOutcome, dispatch, error) {
	if err := checkInvocation(inv, p.Hello().Caps, p.cfg.ModuleState, p.cfg.Facade); err != nil {
		return engine.StepOutcome{}, notDispatched, err
	}
	// The script receives, and args.json holds, the arguments without the
	// worker's internal keys (WR-5).
	args := scriptArgs(inv.Args)
	env := inv.Env
	if p.host != nil && p.cfg.Facade != "" {
		// A host was not spawned with the base environment, so the
		// invocation carries the facade's origin as BASE_URL, which the
		// worker sets last (WA-2), before any step module loads (WA-1).
		env = maps.Clone(env)
		if env == nil {
			env = map[string]string{}
		}
		env["BASE_URL"] = p.cfg.Facade
	}
	payload, err := Encode(Invoke{
		ID:       inv.ID,
		Run:      inv.Run,
		Site:     inv.Site.String(),
		Script:   inv.Script,
		Entry:    inv.Entry,
		Args:     args,
		Env:      env,
		Boundary: string(inv.Boundary),
	})
	if err != nil {
		return engine.StepOutcome{}, notDispatched, fmt.Errorf("harness: invoke %q: %w", inv.ID, err)
	}
	if ctx.Err() != nil {
		return engine.StepOutcome{}, notDispatched, context.Cause(ctx)
	}
	if err := p.writeArgs(args); err != nil {
		return engine.StepOutcome{}, notDispatched, fmt.Errorf("harness: invoke %q: %w", inv.ID, err)
	}
	e := &invocation{id: inv.ID, run: inv.Run, report: inv.Report, outcome: make(chan execResult, 1), fixed: fixed}
	p.mu.Lock()
	if p.state != stateServing || p.killReason != "" || p.ending {
		p.mu.Unlock()
		return engine.StepOutcome{}, notDispatched, errUnavailable
	}
	if _, dup := p.invs[inv.ID]; dup {
		p.mu.Unlock()
		return engine.StepOutcome{}, notDispatched, fmt.Errorf("harness: invocation %q is already in flight", inv.ID)
	}
	fresh := p.cfg.ModuleState == engine.ModuleStateFresh
	if fresh && p.served {
		p.mu.Unlock()
		return engine.StepOutcome{}, notDispatched, fmt.Errorf("harness: invocation %q: a fresh process serves one invocation", inv.ID)
	}
	if err := p.conn.sendPayload(payload, inv.ID); err != nil {
		p.mu.Unlock()
		if errors.Is(err, net.ErrClosed) {
			return engine.StepOutcome{}, notDispatched, errUnavailable
		}
		return engine.StepOutcome{}, notDispatched, fmt.Errorf("harness: invoke %q: %w", inv.ID, err)
	}
	p.invs[inv.ID] = e
	p.served = p.served || fresh
	p.inFlight++
	p.mu.Unlock()

	select {
	case r := <-e.outcome:
		return r.out, completed, r.err
	case <-ctx.Done():
	}
	p.mu.Lock()
	if e.state != invPending || p.invs[inv.ID] != e {
		// The outcome won the race: it is in the channel or on its way.
		p.mu.Unlock()
		r := <-e.outcome
		return r.out, completed, r.err
	}
	e.state = invAbandoned
	p.inFlight--
	p.abandoned++
	reason := engine.CancelReasonOf(ctx)
	// The slot is freed and the pool hears of the abandonment first in the
	// invocation's calls, queued before the reader can see how the call
	// ended, so they come before its late output and Settled however soon
	// those arrive. Execute returns once they have run.
	freed := make(chan struct{})
	e.calls.do(func() {
		if fixed != nil {
			fixed()
		}
	}, p.notifyFunc(), func() { close(freed) })
	var kill []*childGroup
	if !fresh {
		p.conn.send(Cancel{ID: inv.ID, Reason: string(reason)})
		// Windmill kills the job's process group on a timeout or a
		// cancellation (WR-55, WR-37). The engine kills the groups the
		// invocation announced itself, since the harness's event loop,
		// which also revokes them on the cancel, may be blocked; its call
		// goes on (D2).
		kill = takeChildren(e)
	}
	p.mu.Unlock()
	killChildGroups(kill)
	if fresh {
		// A fresh process is the invocation: Windmill kills the job's
		// process at once on timeout or cancellation (WR-37), so it settles
		// with the process, never late.
		p.kill("invocation " + string(reason))
	}
	<-freed
	return engine.StepOutcome{}, abandoned, context.Cause(ctx)
}

var entryName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkInvocation refuses invocations this harness cannot run. They never
// reach it. One must run in the process's module state. A native one
// needs a harness that offers the native boundary and a facade origin,
// which a native step reads as BASE_URL and BASE_INTERNAL_URL (WR-45).
func checkInvocation(inv engine.Invocation, caps Caps, mode engine.ModuleState, facade string) error {
	switch {
	case inv.ID == "":
		return errors.New("harness: invocation without an id")
	case inv.Args.IsZero():
		return fmt.Errorf("harness: invocation %q has no arguments", inv.ID)
	case !entryName.MatchString(inv.Entry):
		return fmt.Errorf("harness: invocation %q: entrypoint %q is not a valid name", inv.ID, inv.Entry)
	case inv.ModuleState != "" && inv.ModuleState != mode:
		return fmt.Errorf("harness: invocation %q runs in module state %q, the process in %q", inv.ID, inv.ModuleState, mode)
	}
	switch inv.Boundary {
	case engine.BoundaryBun, "":
	case engine.BoundaryNative:
		if !caps.Native {
			return fmt.Errorf("harness: invocation %q: the harness has no native boundary (milestone C3n)", inv.ID)
		}
		if facade == "" {
			return fmt.Errorf("harness: invocation %q: the native boundary needs the platform facade's origin, its BASE_URL and BASE_INTERNAL_URL (WR-45), and the process has none", inv.ID)
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
//
// In host mode the engine signals the host only from its watchdog
// (killHost); kill then only ends the connection, which ends the engine's
// side of it.
func (p *Process) kill(reason string) {
	p.mu.Lock()
	if p.killReason == "" {
		p.killReason = reason
	}
	p.mu.Unlock()
	if p.host != nil {
		p.conn.close()
		return
	}
	if p.cmd != nil {
		p.cmd.Process.Signal(syscall.SIGKILL)
		if p.group && !p.groupGone.Load() {
			// The kill reaches the process's whole group (WR-55, WR-37): a
			// fresh job's children, or what a warm process's synchronous
			// child calls left in it; the announced groups of a warm
			// process's steps go when it ends (failInvocations). While a
			// member is left, the group's id is not given to another
			// process; once awaitGroup or endGroup has seen it empty or
			// killed it, it is never signalled again. The window between
			// the group emptying and that look is one poll, in which the id
			// would have to be reused by a new group leader.
			syscall.Kill(-p.pid, syscall.SIGKILL)
		}
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
			if p.host != nil {
				p.killHost(silence)
				return
			}
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

// ArgsFile is the file in a process's working directory that holds the
// arguments of its latest invocation, as the worker writes them into a
// job's directory before the script runs (WR-5): after $var: and $res:
// resolution, which the engine did, and without _MODULES and
// _TEMP_SCRIPT_REFS (scriptArgs). In a warm process the directory is
// every invocation's (D10), so the file is the latest invocation's.
const ArgsFile = "args.json"

// writeArgs replaces the process's args.json with args, whole: it is
// written beside it and renamed over it, so a script reading it never sees
// it half written. A process without a working directory of its own (a
// host-mode connection) has none.
func (p *Process) writeArgs(args jsonval.Value) error {
	if p.dir == "" {
		return nil
	}
	p.argsMu.Lock()
	defer p.argsMu.Unlock()
	f, err := os.CreateTemp(p.dir, ".args-*.json")
	if err != nil {
		return err
	}
	text := args.Producer()
	if len(text) == 0 {
		text = []byte("{}")
	}
	_, err = f.Write(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(p.dir, ArgsFile))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

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

// hasExited reports whether the process has exited. It is true from when
// endInvocations marks the exit, before the pool hears of it and before
// Done closes.
func (p *Process) hasExited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == stateExited
}

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
		out := engine.StepOutcome{Kind: engine.OutcomeError, Body: r.Body}
		if r.Origin == OriginHarness {
			out.Origin = engine.OriginHarness
		}
		return out
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

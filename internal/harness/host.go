package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// HostConfig describes host mode's connection (ADR 0004 host mode,
// ADR 0009): the engine is a child of a Bun host that is also its harness.
// The host started the engine with one end of a socketpair as file
// descriptor 3 (HostConn), greets it like a spawned harness, and submits
// runs with run frames. Runs execute in the host's process, so the engine
// never taints, recycles or kills it, except through the watchdog. The host
// drains the engine with a drain frame (ADR 0004 host mode item 3): later
// runs are answered RunDraining, and the engine says bye once the runs in
// flight have ended.
type HostConfig struct {
	// Conn is the socket to the host; HostConn returns fd 3's.
	Conn net.Conn
	// Children, when the host passes one, is the side channel on which it
	// announces its steps' child process groups (Child frames, WR-55):
	// written synchronously, so an announcement reaches the engine even
	// when the host's socket is backed up and its event loop then blocks,
	// and read on a goroutine of its own. HostChildren returns fd 4's.
	Children io.Reader
	// Kind, Runtime and Bundle are what the handshake requires of the
	// host, as for a spawned harness; EngineVersion is sent in the welcome.
	Kind          string
	Runtime       string
	Bundle        string
	EngineVersion string
	// KillAfter is the watchdog threshold, which the host chooses when it
	// connects (zero means DefaultKillAfter, WatchdogOff disables it), and
	// Heartbeat how often the host pings on its own; see ProcessConfig.
	KillAfter time.Duration
	Heartbeat time.Duration
	// MaxMessage, HandshakeTimeout, RecentLate and Diagnostics are as in
	// ProcessConfig; Diagnostics defaults to the engine's own stderr,
	// where the watchdog's line belongs.
	MaxMessage       int64
	HandshakeTimeout time.Duration
	RecentLate       int
	Diagnostics      io.Writer
	// Facade and Platform are as in ProcessConfig. A host was not spawned
	// with the base environment, so each of its invocations carries the
	// facade's origin as BASE_URL instead.
	Facade   string
	Platform PlatformHandler
	// Workdir is the working directory the host passed to the engine,
	// which must be a writable directory and the host's own working
	// directory, the workdir of its hello (ADR 0004, the shared-process
	// failure model, item 5); the handshake refuses the host otherwise,
	// with RejectWorkdirNotWritable or RejectWorkdirMismatch
	// (CheckWorkdir). Each invocation's args.json is written there
	// (ArgsFile), as in a warm process's own directory, unless
	// SkipArgsFile is set. Empty skips the checks and writes no args.json.
	Workdir string
	// SkipArgsFile leaves every invocation's args.json out of Workdir:
	// the arguments travel only in the invoke frame. wl sets it only for a
	// bundle none of whose scripts can read the file (the manifest's
	// no_args_file_reader) whose host opted in, stating that its own code
	// does not read it either (wl.Host.SkipArgsFile, ADR 0004 host mode
	// item 7).
	SkipArgsFile bool
	// Refuse, when not nil, refuses the host in the handshake with this
	// reason, whatever its hello says: the engine cannot serve it (the
	// bundle did not open) and tells the host why before it exits. Run is
	// then not needed.
	Refuse *Reject
	// Check, when set, is the engine's own check of the host's hello, after
	// the protocol's: a reject it returns refuses the host, such as one
	// whose host plugin declaration the engine cannot use (milestone F2c).
	// It runs before the welcome goes out.
	Check func(h Hello) *Reject
	// Run serves one run the host submitted, on a goroutine of its own,
	// and returns its outcome; the ID of what it returns is set from the
	// request. ctx ends when the host cancels the run (with cause
	// engine.CancelCanceled) or the connection ends. Invocations of the
	// run go to p, which implements engine.StepExecutor. What it returns
	// goes back as run_done; when its Settled closes, a run_settled
	// follows.
	Run func(ctx context.Context, p *Process, r RunRequest) RunDone

	// getppid and signal stand in for the system calls in tests.
	getppid func() int
	signal  func(pid int) error
}

// hostSide is what a Process in host mode keeps of its HostConfig.
type hostSide struct {
	run     func(ctx context.Context, p *Process, r RunRequest) RunDone
	workdir string
	// dir is workdir made absolute, the Process's dir once the handshake
	// has checked it, set before the welcome lets the host submit a run.
	dir     string
	refuse  *Reject
	check   func(h Hello) *Reject
	getppid func() int
	signal  func(pid int) error

	// target signals the host once the handshake has named it (on Linux
	// through a pidfd, parentTarget); guarded by targetMu, which close
	// also takes.
	targetMu sync.Mutex
	target   hostTarget

	// draining and byeSent are guarded by the Process's mu: the host sent
	// drain, and the engine answered bye.
	draining bool
	byeSent  bool
}

// hostTarget sends SIGKILL to the host, and close releases what it holds.
type hostTarget interface {
	kill() error
	close()
}

// killTarget signals a pid with kill(2).
type killTarget int

func (t killTarget) kill() error { return syscall.Kill(int(t), syscall.SIGKILL) }
func (killTarget) close()        {}

// funcTarget is the signal a test stands in, for a pid.
type funcTarget struct {
	pid    int
	signal func(pid int) error
}

func (t funcTarget) kill() error { return t.signal(t.pid) }
func (funcTarget) close()        {}

// CheckWorkdir is host mode's working-directory check (ADR 0004, the
// shared-process failure model, item 5): passed, the directory the host
// passed to the engine, must be a directory the engine (the host's child,
// with its user) can write, else RejectWorkdirNotWritable; and it must be
// the host's working directory, cwd, once both are resolved to absolute
// paths without symbolic links, else RejectWorkdirMismatch. A relative
// passed is taken from the engine's own working directory. It returns nil
// when the directory is usable. The host SDK checks the same before it
// starts the engine; conformance/protocol/host holds the cases both sides
// run.
func CheckWorkdir(passed, cwd string) *Reject {
	abs, err := filepath.Abs(passed)
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(abs); err == nil && !fi.IsDir() {
			err = errors.New("not a directory")
		}
	}
	if err == nil {
		err = syscall.Access(abs, accessW)
	}
	if err != nil {
		return &Reject{RejectWorkdirNotWritable, fmt.Sprintf("%s: %v", passed, err)}
	}
	if cwd == "" {
		return &Reject{RejectWorkdirMismatch, fmt.Sprintf("%s: the host's hello names no working directory", passed)}
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return &Reject{RejectWorkdirNotWritable, fmt.Sprintf("%s: %v", passed, err)}
	}
	hostReal, err := filepath.EvalSymlinks(cwd)
	if err != nil || hostReal != real {
		return &Reject{RejectWorkdirMismatch, fmt.Sprintf("%s is not the host's working directory %s", passed, cwd)}
	}
	return nil
}

// accessW is access(2)'s W_OK.
const accessW = 0x2

// ErrHostLost is the error of an invocation in flight when the host's
// connection ends (ADR 0004 host mode item 6, harness_lost): its run was
// lost with the host, and nothing is left to report it to.
var ErrHostLost = errors.New("harness_lost: the host's connection ended")

// HostConn returns the connection the host passed as file descriptor 3.
func HostConn() (net.Conn, error) {
	f := os.NewFile(3, "host")
	if f == nil {
		return nil, errors.New("harness: no file descriptor 3")
	}
	c, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("harness: file descriptor 3 is not the host's socket: %w", err)
	}
	return c, nil
}

// HostChildren returns the side channel on which the host announces its
// steps' child groups, file descriptor 4 (HostConfig.Children), or nil when
// the host passed none. Call it before HostConn: when the host passed no
// file descriptor 4, HostConn's duplicate of file descriptor 3 may take
// that number, which HostChildren would then take for the channel.
func HostChildren() net.Conn {
	var st syscall.Stat_t
	if syscall.Fstat(4, &st) != nil {
		return nil
	}
	f := os.NewFile(4, "host-children")
	c, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil
	}
	return c
}

// readChildren reads the host's Child frames from its side channel
// (HostConfig.Children) until the channel ends, and handles each as one
// that came over the connection (onChild). Anything else on it stops the
// reading, with a diagnostic.
func (p *Process) readChildren(r io.Reader) {
	for {
		payload, err := readFrame(r, maxChildFrame)
		if err != nil {
			if !endOfStream(err) {
				p.diag.printf("the host's child channel: %v", err)
			}
			return
		}
		m, err := Decode(payload)
		c, ok := m.(Child)
		if err != nil || !ok {
			p.diag.printf("the host's child channel carries a frame that is not a child frame: %v", err)
			return
		}
		p.mu.Lock()
		kill := p.onChild(c)
		p.mu.Unlock()
		killChildGroups(kill)
	}
}

// maxChildFrame bounds a frame on the host's child channel, which carries
// only Child frames of a few dozen bytes.
const maxChildFrame = 4096

// AttachHost completes the handshake with the host on cfg.Conn and serves
// its run frames until the connection ends; Done closes then. The host's
// hello must name the engine's parent process, which is the process the
// watchdog signals (ADR 0004 host mode item 4).
func AttachHost(ctx context.Context, cfg HostConfig) (*Process, error) {
	if cfg.Conn == nil || (cfg.Run == nil && cfg.Refuse == nil) {
		return nil, errors.New("harness: AttachHost needs Conn and Run")
	}
	p, timeout, err := newProcess(ProcessConfig{
		Kind:             cfg.Kind,
		Runtime:          cfg.Runtime,
		Bundle:           cfg.Bundle,
		ModuleState:      engine.ModuleStateWarm,
		EngineVersion:    cfg.EngineVersion,
		KillAfter:        cfg.KillAfter,
		Heartbeat:        cfg.Heartbeat,
		MaxMessage:       cfg.MaxMessage,
		HandshakeTimeout: cfg.HandshakeTimeout,
		RecentLate:       cfg.RecentLate,
		Diagnostics:      cfg.Diagnostics,
		Facade:           cfg.Facade,
		Platform:         cfg.Platform,
	})
	if err != nil {
		cfg.Conn.Close()
		return nil, err
	}
	p.host = &hostSide{run: cfg.Run, workdir: cfg.Workdir, refuse: cfg.Refuse, check: cfg.Check, getppid: cfg.getppid, signal: cfg.signal}
	p.skipArgs = cfg.SkipArgsFile
	if cfg.Workdir != "" {
		if p.host.dir, err = filepath.Abs(cfg.Workdir); err != nil {
			cfg.Conn.Close()
			return nil, err
		}
	}
	// The host's own stdout and stderr are not the engine's to read.
	p.stdio = &lineWriter{diag: p.diag, keep: 20}
	if p.host.getppid == nil {
		p.host.getppid = os.Getppid
	}
	// Host mode has no process of its own to wait for.
	close(p.waitDone)
	p.conn = newConn(cfg.Conn, connConfig{maxMessage: p.maxMessage, onMessage: p.handle, onClose: p.readerDone})
	p.conn.run()
	if err := p.handshake(ctx, timeout); err != nil {
		return nil, err
	}
	p.openTarget()
	if p.killAfter > 0 {
		go p.watchdog()
	}
	go p.watchChildren()
	if cfg.Children != nil {
		go p.readChildren(cfg.Children)
	}
	return p, nil
}

// openTarget fixes what the watchdog signals: the handshake's pid, which
// the handshake checked is the engine's parent, through a pidfd where the
// system has one (parentTarget), opened now, while the host is known to
// be that process.
func (p *Process) openTarget() {
	h := p.host
	pid := p.Hello().PID
	var t hostTarget
	if h.signal != nil {
		t = funcTarget{pid: pid, signal: h.signal}
	} else {
		t = parentTarget(pid, h.getppid)
	}
	h.targetMu.Lock()
	h.target = t
	h.targetMu.Unlock()
}

// kill sends SIGKILL to the host through its target.
func (h *hostSide) kill() error {
	h.targetMu.Lock()
	defer h.targetMu.Unlock()
	if h.target == nil {
		return errors.New("harness: the host is not connected")
	}
	return h.target.kill()
}

// closeTarget releases the target; the host can no longer be signalled.
func (h *hostSide) closeTarget() {
	h.targetMu.Lock()
	defer h.targetMu.Unlock()
	if h.target != nil {
		h.target.close()
		h.target = nil
	}
}

// onRun starts a run the host submitted. It runs with p.mu held. A run
// that arrives while the host drains is answered RunDraining at once.
func (p *Process) onRun(r RunRequest) error {
	if p.host == nil {
		return protoErr(CodeUnexpected, "run from a harness that is not in host mode")
	}
	if _, dup := p.runs[r.ID]; dup {
		return protoErr(CodeUnexpected, "run %q is already in flight", r.ID)
	}
	if p.host.draining {
		p.conn.send(RunDone{ID: r.ID, Error: &RunError{Code: RunDraining, Message: "the engine is draining and starts no new runs"}})
		return nil
	}
	ctx, cancel := context.WithCancelCause(p.life)
	p.runs[r.ID] = cancel
	go func() {
		done := p.host.run(ctx, p, r)
		done.ID = r.ID
		cancel(nil)
		if err := p.conn.send(done); err != nil && !errors.Is(err, net.ErrClosed) {
			p.diag.printf("run_done %q not sent: %v", r.ID, err)
		}
		// The run_done is queued before the run leaves the table, so a bye
		// that its leaving lets out follows it.
		p.mu.Lock()
		delete(p.runs, r.ID)
		p.maybeByeLocked()
		p.mu.Unlock()
		if done.Settled != nil {
			select {
			case <-done.Settled:
				if err := p.conn.send(RunSettled{ID: r.ID}); err != nil && !errors.Is(err, net.ErrClosed) {
					p.diag.printf("run_settled %q not sent: %v", r.ID, err)
				}
			case <-p.life.Done():
			}
		}
	}()
	return nil
}

// onHostDrain handles a host's drain (ADR 0004 host mode item 3): no new
// run starts, and once the runs in flight have ended the engine answers
// bye. It runs with p.mu held.
func (p *Process) onHostDrain() error {
	if p.host == nil {
		return protoErr(CodeUnexpected, "drain from a harness that is not in host mode")
	}
	p.host.draining = true
	p.maybeByeLocked()
	return nil
}

// maybeByeLocked says bye to a draining host once no run is in flight.
func (p *Process) maybeByeLocked() {
	h := p.host
	if !h.draining || h.byeSent || len(p.runs) > 0 {
		return
	}
	h.byeSent = true
	// Behind every run_done queued before it, whole: a host that exits on
	// bye has each outcome by then, however large (ADR 0004 host mode
	// item 3).
	if err := p.conn.sendAfter(Bye{}, "run:"); err != nil && !errors.Is(err, net.ErrClosed) {
		p.diag.printf("bye not sent: %v", err)
	}
}

// hostDone ends host mode when the connection ends: the host exited,
// closed its end, broke the protocol, or the watchdog killed it. Runs in
// flight are lost with it (ADR 0004 host mode item 6): their invocations
// fail with ErrHostLost and their contexts end, and the engine, whose only
// client was the host, has nothing left to serve.
func (p *Process) hostDone(err error) {
	if err != nil && !endOfStream(err) {
		p.mu.Lock()
		if p.readErr == nil {
			p.readErr = err
		}
		dying := p.killReason != ""
		p.mu.Unlock()
		if !dying {
			p.diag.printf("connection to the host failed: %v", err)
		}
	}
	p.conn.close()
	p.host.closeTarget()
	p.endInvocations()
	// The runs' contexts end first, with the cause: an Execute that is
	// given its invocation's outcome next finds the cause already set, and
	// one woken by its context abandons the invocation, which
	// failInvocations then settles with the process (FU7: TestHostLost
	// read a nil cause when the outcome came first).
	p.mu.Lock()
	for _, cancel := range p.runs {
		cancel(ErrHostLost)
	}
	p.mu.Unlock()
	hostLostGap()
	p.failInvocations(func(*invocation) execResult { return execResult{err: ErrHostLost} })
	p.finishExit()
}

// hostLostGap runs between the end of the runs' contexts and the failure
// of their invocations when the host's connection ends (hostDone); tests
// widen it.
var hostLostGap = func() {}

// killHost is host mode's watchdog: the host's event loop has stopped
// answering, so nothing in it can recover (ADR 0004 host mode item 4). The
// engine names the runs in flight on its stderr and sends SIGKILL to the
// host, but only while the host is still its parent: the pid the handshake
// named belongs to that process only as long as it has not exited, and
// once it has, the engine has been reparented and the pid may be reused.
// The connection then ends, and with it the engine. No run outcome is
// reported: the host was their only receiver.
func (p *Process) killHost(silence time.Duration) {
	p.mu.Lock()
	if p.killReason == "" {
		p.killReason = "watchdog"
	}
	pid := p.hello.PID
	runs := make([]string, 0, len(p.runs))
	for id := range p.runs {
		runs = append(runs, id)
	}
	var invs []string
	for _, inv := range p.invs {
		invs = append(invs, fmt.Sprintf("run %s invocation %s", inv.run, inv.id))
	}
	p.mu.Unlock()
	slices.Sort(runs)
	slices.Sort(invs)
	if ppid := p.host.getppid(); ppid != pid {
		p.diag.printf("watchdog: no frame from the host for %s (killAfter %s), but the engine's parent is now pid %d, not the host's pid %d; not signalling; in-flight runs: %s",
			silence.Round(time.Millisecond), p.killAfter, ppid, pid, orNone(runs))
	} else {
		p.diag.printf("watchdog: no frame from the host for %s (killAfter %s); sending SIGKILL to the host, pid %d, and exiting; in-flight runs: %s; invocations: %s",
			silence.Round(time.Millisecond), p.killAfter, pid, orNone(runs), orNone(invs))
		if err := p.host.kill(); err != nil {
			p.diag.printf("watchdog: SIGKILL to pid %d: %v", pid, err)
		}
	}
	p.conn.close()
}

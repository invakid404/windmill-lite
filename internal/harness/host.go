package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"syscall"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// HostConfig describes host mode's connection (ADR 0004 host mode,
// ADR 0009): the engine is a child of a Bun host that is also its harness.
// The host started the engine with one end of a socketpair as file
// descriptor 3 (HostConn), greets it like a spawned harness, and submits
// runs with run frames. Runs execute in the host's process, so the engine
// never taints, recycles or kills it, except through the watchdog.
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
	// Facade and Platform are as in ProcessConfig.
	Facade   string
	Platform PlatformHandler
	// Run serves one run the host submitted, on a goroutine of its own,
	// and returns its outcome; the ID of what it returns is set from the
	// request. ctx ends when the host cancels the run (with cause
	// engine.CancelCanceled) or the connection ends. Invocations of the
	// run go to p, which implements engine.StepExecutor.
	Run func(ctx context.Context, p *Process, r RunRequest) RunDone

	// getppid and signal stand in for the system calls in tests.
	getppid func() int
	signal  func(pid int) error
}

// hostSide is what a Process in host mode keeps of its HostConfig.
type hostSide struct {
	run     func(ctx context.Context, p *Process, r RunRequest) RunDone
	getppid func() int
	signal  func(pid int) error
}

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
// the host passed none.
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
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
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
	if cfg.Conn == nil || cfg.Run == nil {
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
	p.host = &hostSide{run: cfg.Run, getppid: cfg.getppid, signal: cfg.signal}
	// The host's own stdout and stderr are not the engine's to read.
	p.stdio = &lineWriter{diag: p.diag, keep: 20}
	if p.host.getppid == nil {
		p.host.getppid = os.Getppid
	}
	if p.host.signal == nil {
		p.host.signal = func(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
	}
	// Host mode has no process of its own to wait for.
	close(p.waitDone)
	p.conn = newConn(cfg.Conn, connConfig{maxMessage: p.maxMessage, onMessage: p.handle, onClose: p.readerDone})
	p.conn.run()
	if err := p.handshake(ctx, timeout); err != nil {
		return nil, err
	}
	if p.killAfter > 0 {
		go p.watchdog()
	}
	go p.watchChildren()
	if cfg.Children != nil {
		go p.readChildren(cfg.Children)
	}
	return p, nil
}

// onRun starts a run the host submitted. It runs with p.mu held.
func (p *Process) onRun(r RunRequest) error {
	if p.host == nil {
		return protoErr(CodeUnexpected, "run from a harness that is not in host mode")
	}
	if _, dup := p.runs[r.ID]; dup {
		return protoErr(CodeUnexpected, "run %q is already in flight", r.ID)
	}
	ctx, cancel := context.WithCancelCause(p.life)
	p.runs[r.ID] = cancel
	go func() {
		done := p.host.run(ctx, p, r)
		done.ID = r.ID
		p.mu.Lock()
		delete(p.runs, r.ID)
		p.mu.Unlock()
		cancel(nil)
		if err := p.conn.send(done); err != nil && !errors.Is(err, net.ErrClosed) {
			p.diag.printf("run_done %q not sent: %v", r.ID, err)
		}
	}()
	return nil
}

// hostDone ends host mode when the connection ends: the host exited,
// closed its end, broke the protocol, or the watchdog killed it. Runs in
// flight are lost with it (ADR 0004 host mode item 6): their invocations
// fail with ErrHostLost and their contexts end, and the engine, whose only
// client was the host, has nothing left to serve.
func (p *Process) hostDone(err error) {
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
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
	p.endInvocations()
	p.failInvocations(func(*invocation) execResult { return execResult{err: ErrHostLost} })
	p.mu.Lock()
	for _, cancel := range p.runs {
		cancel(ErrHostLost)
	}
	p.mu.Unlock()
	p.finishExit()
}

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
		if err := p.host.signal(pid); err != nil {
			p.diag.printf("watchdog: SIGKILL to pid %d: %v", pid, err)
		}
	}
	p.conn.close()
}

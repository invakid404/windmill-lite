package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/wl"
)

// Host mode (milestone F3b, ADR 0004 host mode): the flow suite's runs on
// a Bun host that embeds the host SDK (testdata/flowhost/host.ts), whose
// steps run in the host's own process, over the host-mode transport, as
// every host's do. The checker keeps the engine in its own process, as
// wl.Options.Host, so that it reads each run's trace and gives it its hooks
// (effect sinks, concurrent groups, cancellation with a reason) as it does
// in warm and fresh mode. The SDK spawns its engine as a child, as it
// always does; that child is this binary in proxy mode (engineProxy),
// which hands the host's socket and child channel (its file descriptors 3
// and 4) to the checker and waits.
//
// The runtime is the one `windmill-lite host` opens, with the watchdog
// off: the engine is not the host's child here, and the watchdog's own
// cases run on the real topology (cmd/windmill-lite). A fixture's host
// lives as long as its fixture: it exits when the engine closes, and an
// earlier exit fails the fixture's runs, so one generation serves them
// all, which is what a supervisor that only restarts on exit would see.

// ModeHost runs the flow suite in host mode.
const ModeHost = "host"

// proxyEnv names the environment variable that puts this binary in proxy
// mode: the path of the checker's Unix socket.
const proxyEnv = "WLTEST_ENGINE_PROXY"

// hostStart bounds a host's start: its Bun process, the SDK's spawn of
// this binary as its engine, and the hand-over of its socket.
const hostStart = 30 * time.Second

// proxyMain runs this binary as a host's engine proxy when the environment
// asks for it, and exits; otherwise it returns.
func proxyMain() {
	if sock := os.Getenv(proxyEnv); sock != "" {
		os.Exit(engineProxy(sock))
	}
}

// engineProxy is the engine a flow-suite host spawns (proxyEnv): it passes
// its file descriptors 3 (the host's socket) and 4 (the child channel, when
// the host passed one) to the checker listening at sock, closes its own
// copies, and waits until the checker closes the connection, which it does
// once the runtime has closed. The host sees its engine exit then.
func engineProxy(sock string) int {
	c, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wltest engine proxy: %v\n", err)
		return 1
	}
	defer c.Close()
	fds := []int{3}
	var st syscall.Stat_t
	if syscall.Fstat(4, &st) == nil {
		fds = append(fds, 4)
	}
	if _, _, err := c.(*net.UnixConn).WriteMsgUnix([]byte{byte(len(fds))}, syscall.UnixRights(fds...), nil); err != nil {
		fmt.Fprintf(os.Stderr, "wltest engine proxy: %v\n", err)
		return 1
	}
	for _, fd := range fds {
		syscall.Close(fd)
	}
	_, _ = io.Copy(io.Discard, c)
	return 0
}

// hostRuntime is a fixture's host and its runtime.
type hostRuntime struct {
	rt      *wl.Runtime
	cmd     *exec.Cmd
	exited  chan struct{}
	control net.Conn
	ln      net.Listener
}

// openHost starts a flow-suite host on the bundle at out, in a working
// directory below work, and opens the runtime that serves it with opts,
// whose Host it sets. The host's output goes to diag.
func (fc *flowChecker) openHost(out, work string, opts wl.Options, diag io.Writer) (*hostRuntime, error) {
	workdir := filepath.Join(work, "host")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(work, "engine.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	h := &hostRuntime{ln: ln, exited: make(chan struct{})}
	cmd := exec.Command(fc.bun, "run", filepath.Join(fc.repo, "cmd", "wltest", "testdata", "flowhost", "host.ts"))
	cmd.Dir = workdir
	cmd.Env = append(harness.BaseEnv(), "WL_ENGINE="+self, "WL_BUNDLE="+out, proxyEnv+"="+sock)
	cmd.Stdout, cmd.Stderr = diag, diag
	if err := cmd.Start(); err != nil {
		ln.Close()
		return nil, err
	}
	h.cmd = cmd
	go func() {
		_ = cmd.Wait()
		close(h.exited)
	}()
	fail := func(err error) (*hostRuntime, error) {
		h.close()
		return nil, err
	}
	conn, children, control, err := acceptHost(ln, h.exited)
	if err != nil {
		return fail(err)
	}
	h.control = control
	opts.Host = &wl.Host{Conn: conn, Workdir: workdir, KillAfter: -1}
	if children != nil {
		opts.Host.Children = children
	}
	opts.ModuleState = ""
	if h.rt, err = wl.Open(out, opts); err != nil {
		return fail(fmt.Errorf("the host's runtime: %w", err))
	}
	return h, nil
}

// acceptHost waits for the engine proxy of the host whose process exits
// closes, and returns the host's socket and child channel (nil when it
// passed none), and the proxy's connection, which keeps it waiting.
func acceptHost(ln net.Listener, exited <-chan struct{}) (conn net.Conn, children net.Conn, control net.Conn, err error) {
	type accepted struct {
		c   net.Conn
		err error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		ch <- accepted{c, err}
	}()
	var a accepted
	select {
	case a = <-ch:
	case <-exited:
		return nil, nil, nil, errors.New("the host exited before its engine started")
	case <-time.After(hostStart):
		return nil, nil, nil, fmt.Errorf("no engine proxy within %s", hostStart)
	}
	if a.err != nil {
		return nil, nil, nil, a.err
	}
	uc := a.c.(*net.UnixConn)
	_ = uc.SetReadDeadline(time.Now().Add(hostStart))
	buf, oob := make([]byte, 1), make([]byte, syscall.CmsgSpace(2*4))
	n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	_ = uc.SetReadDeadline(time.Time{})
	if err == nil && n != 1 {
		err = errors.New("the engine proxy sent nothing")
	}
	var fds []int
	if err == nil {
		var msgs []syscall.SocketControlMessage
		if msgs, err = syscall.ParseSocketControlMessage(oob[:oobn]); err == nil && len(msgs) == 1 {
			fds, err = syscall.ParseUnixRights(&msgs[0])
		}
	}
	if err == nil && (len(fds) == 0 || len(fds) != int(buf[0])) {
		err = fmt.Errorf("the engine proxy passed %d file descriptors, not %d", len(fds), buf[0])
	}
	if err != nil {
		uc.Close()
		for _, fd := range fds {
			syscall.Close(fd)
		}
		return nil, nil, nil, fmt.Errorf("the engine proxy: %w", err)
	}
	toConn := func(fd int, name string) (net.Conn, error) {
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		return net.FileConn(f)
	}
	if conn, err = toConn(fds[0], "host"); err == nil && len(fds) == 2 {
		children, err = toConn(fds[1], "host-children")
	}
	if err != nil {
		uc.Close()
		return nil, nil, nil, err
	}
	return conn, children, uc, nil
}

// close closes the runtime, which ends the host's connection, lets the
// engine proxy exit, and waits for the host to exit, killing it after a
// while.
func (h *hostRuntime) close() {
	if h.rt != nil {
		h.rt.Close()
	}
	if h.control != nil {
		h.control.Close()
	}
	h.ln.Close()
	if h.cmd != nil {
		select {
		case <-h.exited:
		case <-time.After(10 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.exited
		}
	}
}

// exitedEarly reports whether the host exited while its runtime was open.
func (h *hostRuntime) exitedEarly() bool {
	select {
	case <-h.exited:
		return true
	default:
		return false
	}
}

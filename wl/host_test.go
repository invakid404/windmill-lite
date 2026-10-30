package wl

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// These tests play a host on a socketpair (ADR 0004 host mode): the
// runtime, opened with Options.Host, is its engine. Their bundles run no
// step, so they need no Bun; cmd/windmill-lite runs host mode end to end
// with the Bun host SDK.

type testHost struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	// plugin, when not empty, is the host plugin declaration its hello
	// carries (F2c).
	plugin string
}

// newTestHost returns the engine's end of a socketpair and a host on the
// other.
func newTestHost(t *testing.T) (net.Conn, *testHost) {
	t.Helper()
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]net.Conn, 2)
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "socket")
		conns[i], err = net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { conns[1].Close() })
	return conns[0], &testHost{t: t, conn: conns[1], r: bufio.NewReader(conns[1])}
}

func (h *testHost) send(m harness.Message) {
	h.t.Helper()
	payload, err := harness.Encode(m)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.conn.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(payload))), payload...)); err != nil {
		h.t.Fatal(err)
	}
}

// next returns the next message that is not a ping.
func (h *testHost) next() harness.Message {
	h.t.Helper()
	h.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		var head [4]byte
		if _, err := io.ReadFull(h.r, head[:]); err != nil {
			h.t.Fatalf("read: %v", err)
		}
		payload := make([]byte, binary.BigEndian.Uint32(head[:]))
		if _, err := io.ReadFull(h.r, payload); err != nil {
			h.t.Fatalf("read: %v", err)
		}
		m, err := harness.Decode(payload)
		if err != nil {
			h.t.Fatal(err)
		}
		if _, ping := m.(harness.Ping); !ping {
			return m
		}
	}
}

// hello greets the engine as a host whose working directory is workdir.
// The engine checks that the host is its parent: this test's parent.
func (h *testHost) hello(digest, workdir string) {
	hello := harness.Hello{Proto: harness.ProtocolVersion, Harness: harness.HarnessInfo{Kind: "bun", Version: "dev"}, Runtime: harness.PinnedBun,
		Caps: harness.Caps{Fetch: true, Native: true, Host: true}, Bundle: digest, PID: os.Getppid(), Workdir: workdir}
	if h.plugin != "" {
		hello.Plugin = jsonval.FromProducer([]byte(h.plugin))
	}
	h.send(hello)
}

// openHost opens dir in host mode (opts.Host) on a socketpair, greets it
// from the test host, whose working directory is hostCwd, and returns the
// runtime, Open's error and the host, which has read the welcome.
func openHost(t *testing.T, dir string, opts Options, hostCwd string) (*Runtime, error, *testHost, harness.Welcome) {
	return openPluginHost(t, dir, opts, hostCwd, "")
}

// openPluginHost is openHost for a host whose hello declares the plugin
// plugin (F2c).
func openPluginHost(t *testing.T, dir string, opts Options, hostCwd, plugin string) (*Runtime, error, *testHost, harness.Welcome) {
	t.Helper()
	conn, h := newTestHost(t)
	h.plugin = plugin
	opts.Host.Conn = conn
	// The watchdog stays off: the pid the engine would signal is this
	// test's parent.
	opts.Host.KillAfter = -1
	opts.EngineVersion = "test-engine"
	type opened struct {
		rt  *Runtime
		err error
	}
	done := make(chan opened, 1)
	go func() {
		rt, err := Open(dir, opts)
		done <- opened{rt, err}
	}()
	digest, err := bundleDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	h.hello(digest, hostCwd)
	w, ok := h.next().(harness.Welcome)
	if !ok {
		t.Fatal("the first frame is not the welcome")
	}
	if w.Reject != nil {
		// A refused host exits, as the SDK's does.
		h.conn.Close()
	}
	o := <-done
	if o.rt != nil {
		t.Cleanup(func() { o.rt.Close() })
	}
	return o.rt, o.err, h, w
}

func bundleDigest(dir string) (string, error) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return "sha256:none", err
	}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", err
	}
	return bundle.Digest(b), nil
}

// Host mode on the wl runtime (F3a): Open completes the handshake with the
// host, checks its working directory, and serves the runs it submits:
// run_done carries each outcome, run_settled follows once the run has
// settled, a run that cannot start is answered with its reason, and a
// drain is answered bye once no run is in flight. Close ends the
// connection, and Done closes.
func TestHostMode(t *testing.T) {
	dir := writeTestBundle(t, "test-engine", nil, nil)
	work := t.TempDir()
	rt, err, h, w := openHost(t, dir, Options{Host: &Host{Workdir: work}}, work)
	if err != nil {
		t.Fatal(err)
	}
	if w.Config == nil || w.Config.ModuleState != "warm" || w.Config.Facade == "" {
		t.Fatalf("welcome %+v", w)
	}
	if bi := rt.BuildInfo(); bi.ModuleState != ModuleStateWarm {
		t.Fatalf("module state %s", bi.ModuleState)
	}
	h.send(harness.RunRequest{ID: "0b6d2a4e-5b5d-4f00-9c3e-000000000001", Kind: harness.RunFlow, Path: "f/t/echo", Args: jsonval.FromProducer([]byte(`{"x":1}`))})
	d, ok := h.next().(harness.RunDone)
	if !ok || d.ID != "0b6d2a4e-5b5d-4f00-9c3e-000000000001" || d.Status != StatusSuccess || d.Error != nil {
		t.Fatalf("run_done %+v", d)
	}
	if s, ok := h.next().(harness.RunSettled); !ok || s.ID != d.ID {
		t.Fatalf("after run_done: %+v; want run_settled", s)
	}
	h.send(harness.RunRequest{ID: "r2", Kind: harness.RunFlow, Path: "f/t/no_such_flow", Args: jsonval.FromProducer([]byte(`{}`))})
	if d, ok := h.next().(harness.RunDone); !ok || d.Error == nil || d.Error.Code != harness.RunRefused || d.Status != "" {
		t.Fatalf("a run that cannot start: %+v", d)
	}
	h.send(harness.Drain{})
	if _, ok := h.next().(harness.Bye); !ok {
		t.Fatal("no bye after the drain")
	}
	h.send(harness.RunRequest{ID: "r3", Kind: harness.RunFlow, Path: "f/t/echo", Args: jsonval.FromProducer([]byte(`{}`))})
	if d, ok := h.next().(harness.RunDone); !ok || d.Error == nil || d.Error.Code != harness.RunDraining {
		t.Fatalf("a run after the drain: %+v", d)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rt.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close")
	}
}

// Open refuses a host it cannot serve, in the handshake, so the host
// learns why: a working directory it cannot use (the harness's own
// codes), and anything else Open cannot do with the reject code
// engine_refused (a fresh module state, an archive, a bundle of another
// engine version).
func TestHostModeRefuses(t *testing.T) {
	dir := writeTestBundle(t, "test-engine", nil, nil)
	other := writeTestBundle(t, "another-engine", nil, nil)
	work := t.TempDir()
	for _, tc := range []struct {
		name    string
		dir     string
		opts    Options
		hostCwd string
		code    string
	}{
		{"another working directory", dir, Options{Host: &Host{Workdir: work}}, t.TempDir(), RejectWorkdirMismatch},
		{"a working directory that is not one", dir, Options{Host: &Host{Workdir: work + "/missing"}}, work, RejectWorkdirNotWritable},
		{"fresh", dir, Options{ModuleState: ModuleStateFresh, Host: &Host{Workdir: work}}, work, RejectEngine},
		{"another engine version", other, Options{Host: &Host{Workdir: work}}, work, RejectEngine},
		{"no working directory", dir, Options{Host: &Host{}}, work, RejectEngine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, err, _, w := openHost(t, tc.dir, tc.opts, tc.hostCwd)
			if rt != nil || err == nil {
				t.Fatalf("Open succeeded")
			}
			if w.Reject == nil || w.Reject.Code != tc.code {
				t.Fatalf("welcome %+v, Open %v; want a reject with %s", w, err, tc.code)
			}
			var he *HandshakeError
			if tc.code != RejectEngine && (!errors.As(err, &he) || he.Code != tc.code) {
				t.Fatalf("Open: %v; want a HandshakeError %s", err, tc.code)
			}
		})
	}
}

// A runtime that is not in host mode has no Done.
func TestDoneOutsideHostMode(t *testing.T) {
	rt, err := Open(writeTestBundle(t, "test-engine", nil, nil), Options{EngineVersion: "test-engine"})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.Done() != nil {
		t.Fatal("Done is not nil")
	}
}

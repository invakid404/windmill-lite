package wl

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// A host-mode run's own halts (H8), from its run frame, are that run's
// alone: one connection, one flow, two runs, and only the run that names
// the halt ends halted, with no later module started. The host's plugin
// observes the modules (on_module_start, on_module_end), and its own
// halt, declared in the hello, still applies to every run. A run whose
// halt selector is malformed or ambiguous is refused, naming the selector
// as the host SDK's run options do, and the connection serves on.
func TestHostRunHalts(t *testing.T) {
	flows := map[string]string{
		"f/t/prefix": `{"modules":[{"id":"check","value":{"type":"identity"},"mock":{"enabled":true,"return_value":"check"}},` +
			`{"id":"write","value":{"type":"identity"},"mock":{"enabled":true,"return_value":"write"}}]}`,
		"f/t/plugin_halt": `{"modules":[{"id":"a","value":{"type":"identity"}},{"id":"b","value":{"type":"identity"}}]}`,
		"f/t/dup":         testFlows["f/t/dup"],
	}
	dir := writeBundleOf(t, flows, nil, "test-engine", nil, nil)
	work := t.TempDir()
	_, err, h, _ := openPluginHost(t, dir, Options{Host: &Host{Workdir: work}}, work,
		`{"halt_after":[{"flow":"f/t/plugin_halt","module":"a"}],"on_module_start":true,"on_module_end":true}`)
	if err != nil {
		t.Fatal(err)
	}
	// run submits a run and answers its host calls until its run_done, and
	// returns the run_done and the sites the plugin saw start and end.
	n := 0
	run := func(path, haltBefore, haltAfter string) (d harness.RunDone, started, ended []string) {
		t.Helper()
		n++
		r := harness.RunRequest{ID: fmt.Sprintf("0b6d2a4e-5b5d-4f00-9c3e-%012d", n), Kind: harness.RunFlow, Path: path, Args: jsonval.FromProducer([]byte(`{}`))}
		if haltBefore != "" {
			r.HaltBefore = jsonval.FromProducer([]byte(haltBefore))
		}
		if haltAfter != "" {
			r.HaltAfter = jsonval.FromProducer([]byte(haltAfter))
		}
		h.send(r)
		for {
			switch m := h.next().(type) {
			case harness.HostCall:
				switch m.Hook {
				case "on_module_start":
					started = append(started, m.Site)
				case "on_module_end":
					ended = append(ended, m.Site)
				}
				h.send(harness.HostReply{ID: m.ID, Payload: jsonval.FromProducer([]byte(`{}`))})
			case harness.RunDone:
				if m.ID != r.ID {
					t.Fatalf("run_done %+v for another run than %s", m, r.ID)
				}
				if m.Error == nil {
					if s, ok := h.next().(harness.RunSettled); !ok || s.ID != r.ID {
						t.Fatalf("after run_done: %+v; want run_settled", s)
					}
				}
				return m, started, ended
			default:
				t.Fatalf("unexpected %T %+v", m, m)
			}
		}
	}
	afterCheck := `[{"flow":"f/t/prefix","module":"check"}]`

	d, started, ended := run("f/t/prefix", "", afterCheck)
	if d.Status != StatusHalted || d.Error != nil || !slices.Equal(started, []string{"m:check"}) || !slices.Equal(ended, []string{"m:check"}) {
		t.Fatalf("the run with halt_after check: %+v, started %q, ended %q; want halted after check alone", d, started, ended)
	}
	d, started, ended = run("f/t/prefix", "", "")
	if d.Status != StatusSuccess || string(d.Result.Producer()) != `"write"` || len(started) != 2 || !slices.Equal(ended, []string{"m:check", "m:write"}) {
		t.Fatalf("the next run of the flow, without halts: %+v (result %s), started %q, ended %q; want success, result write, both modules", d, d.Result.Producer(), started, ended)
	}
	d, started, _ = run("f/t/prefix", `[{"flow":"f/t/prefix","module":"write"}]`, "")
	if d.Status != StatusHalted || !slices.Equal(started, []string{"m:check"}) {
		t.Fatalf("the run with halt_before write: %+v, started %q; want halted before write", d, started)
	}
	// The plugin's halt, declared once in the hello, applies to every run.
	for range 2 {
		if d, started, _ := run("f/t/plugin_halt", "", ""); d.Status != StatusHalted || !slices.Equal(started, []string{"m:a"}) {
			t.Fatalf("a run under the plugin's halt_after a: %+v, started %q", d, started)
		}
	}
	for _, tc := range []struct {
		name, before, after, refusal string
	}{
		{"a malformed halt_after", "", `[{"flow":"f/t/prefix"}]`, "the run's haltAfter[0]: wl: invalid selector"},
		{"an ambiguous halt_before", `[{"flow":"f/t/prefix","module":"check"},{"flow":"f/t/dup","module":"a"}]`, "", "the run's haltBefore[1]: ambiguous_selector"},
		{"an unknown selector field", "", `[{"flow":"f/t/prefix","modul":"check"}]`, `the run's halt_after: json: unknown field "modul"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, started, _ := run("f/t/prefix", tc.before, tc.after)
			if d.Error == nil || d.Error.Code != harness.RunRefused || !strings.Contains(d.Error.Message, tc.refusal) || len(started) > 0 {
				t.Fatalf("%+v, started %q; want refused with %q", d.Error, started, tc.refusal)
			}
		})
	}
	if d, _, _ := run("f/t/prefix", "", ""); d.Status != StatusSuccess {
		t.Fatalf("after the refusals: %+v", d)
	}
}

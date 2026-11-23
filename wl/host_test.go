package wl

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
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
	// carries.
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
// plugin.
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

// TestHostMode checks host mode on the wl runtime: Open completes the
// handshake with the host, checks its working directory, and serves the
// runs it submits: run_done carries each outcome, run_settled follows once
// the run has settled, a run that cannot start is answered with its reason,
// and a drain is answered bye once no run is in flight. Close ends the
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

// sendRaw sends payload, a frame's JSON text, as the host wrote it.
func (h *testHost) sendRaw(payload string) {
	h.t.Helper()
	if _, err := h.conn.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(payload))), payload...)); err != nil {
		h.t.Fatal(err)
	}
}

// stepFlows are flows with one Bun step each, which a host-mode runtime
// sends to its host to run: f/t/step returns 1.
var stepFlows = map[string]string{
	"f/t/step": `{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun","content":"export async function main() { return 1 }","input_transforms":{}}}]}`,
}

// A deadline that has already passed ends its run alone (ADR 0004): one
// connection serves a healthy run, which waits on the host until the end,
// and four more, each sent as the host SDK writes its frame. The run with
// no deadline_ms runs to success; the one with deadline_ms 0 ends
// canceled, with the cause deadline, and its step never reaches the host.
// A write-capable step with no pre_write_deadline_ms writes; with
// pre_write_deadline_ms 0 it is canceled before the write, with the cause
// pre_write_deadline, and the run has not mutated. The healthy run then
// succeeds, and host mode goes on.
func TestHostExpiredDeadlineEndsOnlyItsRun(t *testing.T) {
	dir := writeBundleOf(t, stepFlows, nil, "test-engine", nil, nil)
	work := t.TempDir()
	rt, err, h, _ := openHost(t, dir, Options{Host: &Host{Workdir: work}}, work)
	if err != nil {
		t.Fatal(err)
	}
	const healthy = "0b6d2a4e-5b5d-4f00-9c3e-00000000000a"
	run := func(id, budgets string) string {
		return `{"t":"run","id":"` + id + `","kind":"flow","path":"f/t/step","args":{}` + budgets + `}`
	}
	h.sendRaw(run(healthy, ""))
	inv, ok := h.next().(harness.Invoke)
	if !ok || inv.Run != healthy {
		t.Fatalf("the healthy run's step: %+v", inv)
	}
	write := `,"write_capable":[{"flow":"f/t/step","module":"a"}]`
	runs := map[string]string{
		"0b6d2a4e-5b5d-4f00-9c3e-000000000001": "",
		"0b6d2a4e-5b5d-4f00-9c3e-000000000002": `,"deadline_ms":0`,
		"0b6d2a4e-5b5d-4f00-9c3e-000000000003": write,
		"0b6d2a4e-5b5d-4f00-9c3e-000000000004": write + `,"pre_write_deadline_ms":0`,
	}
	for _, id := range slices.Sorted(maps.Keys(runs)) {
		h.sendRaw(run(id, runs[id]))
	}
	done := map[string]harness.RunDone{}
	stepped := map[string]bool{}
	for len(done) < len(runs) {
		switch m := h.next().(type) {
		case harness.Invoke:
			stepped[m.Run] = true
			h.send(harness.Result{ID: m.ID, Outcome: harness.OutcomeOK, Body: jsonval.FromProducer([]byte(`1`)), Settled: true})
		case harness.RunDone:
			done[m.ID] = m
		case harness.RunSettled:
		default:
			t.Fatalf("unexpected %T %+v", m, m)
		}
	}
	for _, tc := range []struct {
		id, status, cause string
		stepped, mutated  bool
	}{
		{"0b6d2a4e-5b5d-4f00-9c3e-000000000001", StatusSuccess, "", true, false},
		{"0b6d2a4e-5b5d-4f00-9c3e-000000000002", StatusCanceled, CauseDeadline, false, false},
		{"0b6d2a4e-5b5d-4f00-9c3e-000000000003", StatusSuccess, "", true, true},
		{"0b6d2a4e-5b5d-4f00-9c3e-000000000004", StatusCanceled, CausePreWriteDeadline, false, false},
	} {
		d := done[tc.id]
		if d.Error != nil || d.Status != tc.status || d.CancelCause != tc.cause || stepped[tc.id] != tc.stepped || d.MayHaveMutated != tc.mutated {
			t.Errorf("run %s%s: %+v, its step sent to the host: %v; want %s, cause %q, step sent %v, mutated %v",
				tc.id, runs[tc.id], d, stepped[tc.id], tc.status, tc.cause, tc.stepped, tc.mutated)
		}
	}
	select {
	case <-rt.Done():
		t.Fatal("host mode ended")
	default:
	}
	h.send(harness.Result{ID: inv.ID, Outcome: harness.OutcomeOK, Body: jsonval.FromProducer([]byte(`1`)), Settled: true})
	for {
		if d, ok := h.next().(harness.RunDone); ok {
			if d.ID != healthy || d.Status != StatusSuccess || d.CancelCause != "" {
				t.Fatalf("the healthy run: %+v", d)
			}
			break
		}
	}
	select {
	case <-rt.Done():
		t.Fatal("host mode ended")
	default:
	}
}

// A run_cancel's reason and canceler are what the run's Canceled error
// names (FX-38), with the cause canceled; without them the error names
// Windmill's defaults.
func TestHostRunCancelReason(t *testing.T) {
	dir := writeBundleOf(t, stepFlows, nil, "test-engine", nil, nil)
	work := t.TempDir()
	_, err, h, _ := openHost(t, dir, Options{Host: &Host{Workdir: work}}, work)
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		cancel string
		want   string
	}{
		{`,"reason":"the tenant was suspended","canceler":"tenant-admin"`, `{"error":{"message":"Job canceled: the tenant was suspended by tenant-admin","name":"Canceled","reason":"the tenant was suspended","canceler":"tenant-admin"}}`},
		{``, `{"error":{"message":"Job canceled: no reason given by unknown","name":"Canceled","reason":"no reason given","canceler":"unknown"}}`},
	} {
		id := fmt.Sprintf("0b6d2a4e-5b5d-4f00-9c3e-%012d", i+1)
		h.sendRaw(`{"t":"run","id":"` + id + `","kind":"flow","path":"f/t/step","args":{}}`)
		if inv, ok := h.next().(harness.Invoke); !ok || inv.Run != id {
			t.Fatalf("the run's step: %+v", inv)
		}
		h.sendRaw(`{"t":"run_cancel","id":"` + id + `"` + tc.cancel + `}`)
		for {
			m := h.next()
			if _, ok := m.(harness.Cancel); ok {
				continue
			}
			d, ok := m.(harness.RunDone)
			if !ok || d.ID != id || d.Status != StatusCanceled || d.CancelCause != CauseCanceled {
				t.Fatalf("after run_cancel%s: %+v", tc.cancel, m)
			}
			if ok, err := jsonEqual(d.Result.Producer(), []byte(tc.want)); err != nil || !ok {
				t.Fatalf("after run_cancel%s: result %s, want %s", tc.cancel, d.Result.Producer(), tc.want)
			}
			break
		}
	}
}

// A run frame's deadline, either budget, converts to a duration exactly up
// to the longest that a time.Duration holds, 9,223,372,036,854 ms, about
// 292 years, and saturates to it above, up to the largest integer of the
// frame rules; 0 or less is a deadline that has passed. The hard deadline
// bounds the run's context, and the pre-write deadline counts from now.
func TestBudgetDuration(t *testing.T) {
	const longest = time.Duration(math.MaxInt64)
	for _, tc := range []struct {
		ms   int64
		want time.Duration
	}{
		{1, time.Millisecond},
		{9_223_372_036_854, 9_223_372_036_854 * time.Millisecond},
		{9_223_372_036_855, longest},
		{9_007_199_254_740_991, longest},
		{0, 0},
		{-1, 0},
		{-9_007_199_254_740_991, 0},
	} {
		t.Run(fmt.Sprint(tc.ms), func(t *testing.T) {
			if got := budgetDuration(tc.ms); got != tc.want {
				t.Fatalf("budgetDuration(%d) = %v, want %v", tc.ms, got, tc.want)
			}
			ms := tc.ms
			before := time.Now()
			ctx, cancel := hostDeadline(context.Background(), harness.RunRequest{DeadlineMS: &ms})
			defer cancel()
			opts, err := hostBudgets(harness.RunRequest{PreWriteDeadlineMS: &ms}, nil)
			after := time.Now()
			if err != nil {
				t.Fatal(err)
			}
			hard, ok := ctx.Deadline()
			if !ok {
				t.Fatal("the run's context has no deadline")
			}
			for _, got := range []struct {
				name string
				at   time.Time
			}{{"the hard deadline", hard}, {"the pre-write deadline", opts.PreWriteDeadline}} {
				if got.at.Before(before.Add(tc.want)) || got.at.After(after.Add(tc.want)) {
					t.Errorf("%s of %d ms is at %v, want %v after now", got.name, tc.ms, got.at.Sub(before), tc.want)
				}
			}
			if tc.want == 0 && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Errorf("a hard deadline of %d ms has not passed: %v", tc.ms, ctx.Err())
			}
		})
	}
	// Without the fields, a run has neither deadline.
	ctx, cancel := hostDeadline(context.Background(), harness.RunRequest{})
	defer cancel()
	opts, err := hostBudgets(harness.RunRequest{}, nil)
	if _, ok := ctx.Deadline(); ok || err != nil || !opts.PreWriteDeadline.IsZero() {
		t.Fatalf("a run without deadlines: context deadline %v, pre-write deadline %v, %v", ok, opts.PreWriteDeadline, err)
	}
}

// A run frame's identity is that run's own job identity: each field it
// sets replaces the runtime's (Options.Identity) in the environment of the
// run's steps, which the host gets in their invoke frames. An identity
// with a field it does not know refuses that run, naming the field, and
// the connection serves on.
func TestHostRunIdentity(t *testing.T) {
	dir := writeBundleOf(t, stepFlows, nil, "test-engine", nil, nil)
	work := t.TempDir()
	_, err, h, _ := openHost(t, dir, Options{Host: &Host{Workdir: work}, Identity: Identity{Workspace: "ws-runtime", Username: "rt-user"}}, work)
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		identity          string
		workspace, user   string
		email, refusedFor string
	}{
		{"", "ws-runtime", "rt-user", "windmill-lite@localhost", ""},
		{`,"identity":{"workspace":"ws-run","email":"run@example.test"}`, "ws-run", "rt-user", "run@example.test", ""},
		{`,"identity":{"workspace":"ws-run","workspac":"x"}`, "", "", "", `unknown field "workspac"`},
	} {
		id := fmt.Sprintf("0b6d2a4e-5b5d-4f00-9c3e-%012d", i+1)
		h.sendRaw(`{"t":"run","id":"` + id + `","kind":"flow","path":"f/t/step","args":{}` + tc.identity + `}`)
		m := h.next()
		for {
			// The earlier run's run_settled may come first.
			if _, ok := m.(harness.RunSettled); !ok {
				break
			}
			m = h.next()
		}
		if tc.refusedFor != "" {
			if d, ok := m.(harness.RunDone); !ok || d.Error == nil || d.Error.Code != harness.RunRefused || !strings.Contains(d.Error.Message, tc.refusedFor) {
				t.Fatalf("run%s: %+v; want refused for %s", tc.identity, m, tc.refusedFor)
			}
			continue
		}
		inv, ok := m.(harness.Invoke)
		if !ok || inv.Run != id {
			t.Fatalf("run%s: %+v; want its step", tc.identity, m)
		}
		if e := inv.Env; e["WM_WORKSPACE"] != tc.workspace || e["WM_ROOT_WORKSPACE"] != tc.workspace || e["WM_USERNAME"] != tc.user || e["WM_EMAIL"] != tc.email {
			t.Fatalf("run%s: the step's identity %s, %s, %s, %s; want %s, %s, %s", tc.identity, e["WM_WORKSPACE"], e["WM_ROOT_WORKSPACE"], e["WM_USERNAME"], e["WM_EMAIL"], tc.workspace, tc.user, tc.email)
		}
		h.send(harness.Result{ID: inv.ID, Outcome: harness.OutcomeOK, Body: jsonval.FromProducer([]byte(`1`)), Settled: true})
		for {
			if d, ok := h.next().(harness.RunDone); ok {
				if d.ID != id || d.Status != StatusSuccess {
					t.Fatalf("run%s: %+v", tc.identity, d)
				}
				break
			}
		}
	}
}

package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// workdirCases are the cases of conformance/protocol/host/workdir.json,
// which the host SDK's tests run too (harnesses/bun/test/host.test.ts).
type workdirCase struct {
	Case      string  `json:"case"`
	Title     string  `json:"title"`
	Workdir   string  `json:"workdir"`
	CwdMode   string  `json:"cwd_mode"`
	OtherMode string  `json:"other_mode"`
	Expect    *string `json:"expect"`
}

func workdirCases(t *testing.T) []workdirCase {
	t.Helper()
	b, err := os.ReadFile("../../conformance/protocol/host/workdir.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []workdirCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases")
	}
	return f.Cases
}

// setup lays out a case's directory: cwd/, other/, link (to cwd/) and
// file, with the modes the case gives, and returns the host's working
// directory and the path the host passes.
func (c workdirCase) setup(t *testing.T) (cwd, passed string) {
	t.Helper()
	if (c.CwdMode != "" || c.OtherMode != "") && os.Geteuid() == 0 {
		t.Skip("root writes through any mode")
	}
	root := t.TempDir()
	cwd = filepath.Join(root, "cwd")
	other := filepath.Join(root, "other")
	for _, d := range []string{cwd, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(cwd, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for dir, mode := range map[string]string{cwd: c.CwdMode, other: c.OtherMode} {
		if mode == "" {
			continue
		}
		m, err := strconv.ParseUint(mode, 8, 32)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, os.FileMode(m)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
	}
	passed = c.Workdir
	if passed != "." {
		passed = filepath.Join(root, c.Workdir)
	}
	// The engine inherits the host's working directory.
	t.Chdir(cwd)
	return cwd, passed
}

// The working-directory checks of host mode (ADR 0004, the shared-process
// failure model, item 5), on the shared cases: CheckWorkdir, and the
// handshake, which refuses a host whose hello names another working
// directory than the one it passed, or one it cannot write.
func TestHostWorkdirCases(t *testing.T) {
	for _, c := range workdirCases(t) {
		t.Run(c.Case, func(t *testing.T) {
			cwd, passed := c.setup(t)
			want := ""
			if c.Expect != nil {
				want = *c.Expect
			}
			got := ""
			if r := CheckWorkdir(passed, cwd); r != nil {
				got = r.Code
			}
			if got != want {
				t.Fatalf("%s: CheckWorkdir(%s) = %q; want %q", c.Title, passed, got, want)
			}
			hello := hostHello()
			hello.Workdir = cwd
			h, p, err := attach(t, HostConfig{Run: echoRun, Workdir: passed}, hello)
			w := h.expect(TypeWelcome).(Welcome)
			if want == "" {
				if err != nil || w.Config == nil {
					t.Fatalf("%v, welcome %+v", err, w)
				}
				abs, _ := filepath.Abs(passed)
				if p.Dir() != abs {
					t.Fatalf("Dir() = %q; want %q, where args.json goes", p.Dir(), abs)
				}
				return
			}
			var he *HandshakeError
			if !errors.As(err, &he) || he.Code != want || w.Reject == nil || w.Reject.Code != want {
				t.Fatalf("got %v, welcome %+v; want %s", err, w, want)
			}
		})
	}
}

// A hello without a working directory, from a host that was given one,
// cannot be checked: workdir_mismatch.
func TestHostWorkdirMissingFromHello(t *testing.T) {
	h, _, err := attach(t, HostConfig{Run: echoRun, Workdir: t.TempDir()}, hostHello())
	w := h.expect(TypeWelcome).(Welcome)
	var he *HandshakeError
	if !errors.As(err, &he) || he.Code != RejectWorkdirMismatch || w.Reject == nil {
		t.Fatalf("got %v, welcome %+v", err, w)
	}
}

// An engine that cannot serve the host at all refuses it in the
// handshake with its reason, so the host learns why.
func TestHostRefuse(t *testing.T) {
	h, _, err := attach(t, HostConfig{Refuse: &Reject{RejectEngine, "the bundle does not verify"}}, hostHello())
	w := h.expect(TypeWelcome).(Welcome)
	var he *HandshakeError
	if !errors.As(err, &he) || he.Code != RejectEngine || w.Reject == nil || w.Reject.Message != "the bundle does not verify" {
		t.Fatalf("got %v, welcome %+v", err, w)
	}
}

// A host was not spawned with the base environment, so its invocations
// carry the facade's origin as BASE_URL, over any value the engine gave
// (WA-2); and their arguments go to args.json in the host's working
// directory (WR-5).
func TestHostInvocationEnvironmentAndArgs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	hello := hostHello()
	hello.Workdir = dir
	const facade = "http://127.0.0.1:9"
	h, _, err := attach(t, HostConfig{Facade: facade, Workdir: dir, Run: func(ctx context.Context, p *Process, r RunRequest) RunDone {
		env := map[string]string{"WM_JOB_ID": "j", "BASE_URL": "http://elsewhere"}
		out, err := p.Execute(ctx, engine.Invocation{ID: "i", Run: r.ID, Script: "s", Entry: "main", Args: r.Args, Env: env})
		if err != nil || env["BASE_URL"] != "http://elsewhere" {
			return RunDone{Status: "failure"}
		}
		return RunDone{Status: "success", Result: out.Body}
	}}, hello)
	if err != nil {
		t.Fatal(err)
	}
	if w := h.expect(TypeWelcome).(Welcome); w.Config == nil || w.Config.Facade != facade {
		t.Fatalf("welcome %+v", w)
	}
	h.send(RunRequest{ID: "r1", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{"a":1,"_MODULES":{}}`))})
	inv := h.expect(TypeInvoke).(Invoke)
	if inv.Env["BASE_URL"] != facade || inv.Env["WM_JOB_ID"] != "j" {
		t.Fatalf("invoke env %v", inv.Env)
	}
	b, err := os.ReadFile(filepath.Join(dir, ArgsFile))
	if err != nil || string(b) != `{"a":1}` {
		t.Fatalf("args.json %q, %v", b, err)
	}
	h.send(Result{ID: inv.ID, Outcome: OutcomeOK, Body: jsonval.FromProducer([]byte(`1`)), Settled: true})
	if d := h.expect(TypeRunDone).(RunDone); d.Status != "success" {
		t.Fatalf("run_done %+v (the caller's environment changed?)", d)
	}
}

// ADR 0004 host mode item 3, from the engine's side: once the host sends
// drain, a new run is answered draining at once, the run in flight goes
// on, and the engine says bye once it has ended, after its run_done.
func TestHostDrain(t *testing.T) {
	release := make(chan struct{})
	h, _, err := attach(t, HostConfig{Run: func(ctx context.Context, p *Process, r RunRequest) RunDone {
		<-release
		return RunDone{Status: "success", Result: jsonval.FromProducer([]byte(`"` + r.ID + `"`)), MayHaveMutated: true,
			Warnings: jsonval.FromProducer([]byte(`[{"code":"c","message":"m"}]`))}
	}}, hostHello())
	if err != nil {
		t.Fatal(err)
	}
	h.expect(TypeWelcome)
	h.send(RunRequest{ID: "r1", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{}`))})
	time.Sleep(50 * time.Millisecond)
	h.send(Drain{})
	h.send(RunRequest{ID: "r2", Kind: RunScript, Path: "f/y", Args: jsonval.FromProducer([]byte(`{}`))})
	d := h.expect(TypeRunDone).(RunDone)
	if d.ID != "r2" || d.Error == nil || d.Error.Code != RunDraining || d.Status != "" {
		t.Fatalf("the run after drain: %+v", d)
	}
	close(release)
	var order []string
	for len(order) < 2 {
		select {
		case m, ok := <-h.msgs:
			if !ok {
				t.Fatalf("closed after %v", order)
			}
			switch m := m.(type) {
			case RunDone:
				if m.ID != "r1" || m.Status != "success" || !m.MayHaveMutated || string(m.Warnings.Producer()) != `[{"code":"c","message":"m"}]` {
					t.Fatalf("run_done %+v", m)
				}
				order = append(order, TypeRunDone)
			case Bye:
				order = append(order, TypeBye)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("got %v", order)
		}
	}
	if order[0] != TypeRunDone || order[1] != TypeBye {
		t.Fatalf("order %v; want run_done, then bye", order)
	}
}

// A drain with no run in flight is answered bye at once; drain from a
// spawned harness is a protocol error (only a host drains its engine).
func TestHostDrainIdle(t *testing.T) {
	h, _, err := attach(t, HostConfig{Run: echoRun}, hostHello())
	if err != nil {
		t.Fatal(err)
	}
	h.expect(TypeWelcome)
	h.send(Drain{})
	h.expect(TypeBye)
	p := &Process{}
	if err := p.onHostDrain(); err == nil {
		t.Fatal("a spawned harness's drain was accepted")
	}
}

// onRunSettled across the connection (ADR 0004 host mode item 5): when the
// run's Settled closes, run_settled follows its run_done, never before.
func TestHostRunSettled(t *testing.T) {
	settled := make(chan struct{})
	close(settled) // settled already when the run ends: still after run_done
	later := make(chan struct{})
	h, _, err := attach(t, HostConfig{Run: func(ctx context.Context, p *Process, r RunRequest) RunDone {
		if r.ID == "now" {
			return RunDone{Status: "success", Settled: settled}
		}
		return RunDone{Status: "failure", Settled: later}
	}}, hostHello())
	if err != nil {
		t.Fatal(err)
	}
	h.expect(TypeWelcome)
	h.send(RunRequest{ID: "now", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{}`))})
	next := func() Message {
		for {
			select {
			case m := <-h.msgs:
				if m.Type() == TypeRunDone || m.Type() == TypeRunSettled {
					return m
				}
			case <-time.After(5 * time.Second):
				t.Fatal("nothing within 5 s")
			}
		}
	}
	if m, ok := next().(RunDone); !ok || m.ID != "now" {
		t.Fatalf("first %+v; want run_done", m)
	}
	if m, ok := next().(RunSettled); !ok || m.ID != "now" {
		t.Fatalf("then %+v; want run_settled", m)
	}
	h.send(RunRequest{ID: "later", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{}`))})
	if m, ok := next().(RunDone); !ok || m.ID != "later" {
		t.Fatalf("%+v; want run_done", m)
	}
	select {
	case m := <-h.msgs:
		if m.Type() == TypeRunSettled {
			t.Fatal("run_settled before the run settled")
		}
	case <-time.After(100 * time.Millisecond):
	}
	close(later)
	if m, ok := next().(RunSettled); !ok || m.ID != "later" {
		t.Fatalf("%+v; want run_settled", m)
	}
}

// Review round 1, finding 1: bye waits behind every run_done queued before
// it, whole, however large, so a host that exits on bye has the outcome of
// every run the engine took; the run that arrived after drain is refused
// at once, ahead of them.
func TestHostDrainWaitsForLargeOutcomes(t *testing.T) {
	release := make(chan struct{})
	h, _, err := attach(t, HostConfig{Run: func(ctx context.Context, p *Process, r RunRequest) RunDone {
		<-release
		if r.ID == "small" {
			return RunDone{Status: "success", Result: jsonval.FromProducer([]byte(`1`))}
		}
		return RunDone{Status: "success", Result: jsonval.FromProducer([]byte(`"` + strings.Repeat("x", 2*MaxFrame) + `"`))}
	}}, hostHello())
	if err != nil {
		t.Fatal(err)
	}
	h.expect(TypeWelcome)
	for _, id := range []string{"large-1", "large-2", "small"} {
		h.send(RunRequest{ID: id, Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{}`))})
	}
	h.send(Drain{})
	h.send(RunRequest{ID: "late", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{}`))})
	if d := h.expect(TypeRunDone).(RunDone); d.ID != "late" || d.Error == nil || d.Error.Code != RunDraining {
		t.Fatalf("the run after drain: %+v", d)
	}
	close(release)
	streams := map[string]bool{} // part stream id: complete
	small := false
	for {
		select {
		case m, ok := <-h.msgs:
			if !ok {
				t.Fatal("the connection closed before bye")
			}
			switch m := m.(type) {
			case Part:
				streams[m.ID] = m.Last
			case RunDone:
				small = small || m.ID == "small"
			case Bye:
				done := 0
				for _, last := range streams {
					if last {
						done++
					}
				}
				if done != 2 || len(streams) != 2 || !small {
					t.Fatalf("bye after %d of the 2 large outcomes whole (streams %v), the small one: %v", done, streams, small)
				}
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no bye")
		}
	}
}

// Review round 1, finding 2: a host may submit a run as soon as it reads
// the welcome, before AttachHost has returned on the engine's side; the
// run's invocation already has its args.json in the host's working
// directory. Each try sends the run at once from a host played here.
func TestHostRunRightAfterWelcome(t *testing.T) {
	for i := range 20 {
		dir := t.TempDir()
		ec, hc := socketConns(t)
		r := bufio.NewReader(hc)
		attached := make(chan *Process, 1)
		go func() {
			p, err := AttachHost(context.Background(), HostConfig{
				Conn: ec, Kind: "bun", Runtime: PinnedBun, Bundle: "sha256:b", Workdir: dir, KillAfter: WatchdogOff,
				Diagnostics: &syncBuffer{}, getppid: func() int { return fakeHostPID }, signal: func(int) error { return nil },
				Run: func(ctx context.Context, p *Process, r RunRequest) RunDone {
					out, err := p.Execute(ctx, engine.Invocation{ID: "inv", Run: r.ID, Script: "s", Entry: "main", Args: r.Args})
					if err != nil {
						return RunDone{Status: "failure"}
					}
					return RunDone{Status: "success", Result: out.Body}
				},
			})
			if err != nil {
				t.Error(err)
			}
			attached <- p
		}()
		write := func(m Message) {
			b, err := Encode(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := hc.Write(appendFrame(nil, b)); err != nil {
				t.Fatal(err)
			}
		}
		read := func() Message {
			for {
				b, err := readFrame(r, MaxFrame)
				if err != nil {
					t.Fatal(err)
				}
				m, err := Decode(b)
				if err != nil {
					t.Fatal(err)
				}
				if _, ping := m.(Ping); !ping {
					return m
				}
			}
		}
		hello := hostHello()
		hello.Workdir = dir
		write(hello)
		if w, ok := read().(Welcome); !ok || w.Config == nil {
			t.Fatalf("try %d: no welcome", i)
		}
		write(RunRequest{ID: "r", Kind: RunFlow, Path: "f/x", Args: jsonval.FromProducer([]byte(`{"try":` + strconv.Itoa(i) + `}`))})
		if _, ok := read().(Invoke); !ok {
			t.Fatalf("try %d: no invoke", i)
		}
		b, err := os.ReadFile(filepath.Join(dir, ArgsFile))
		if want := `{"try":` + strconv.Itoa(i) + `}`; err != nil || string(b) != want {
			t.Fatalf("try %d: args.json %q, %v; want %s", i, b, err, want)
		}
		write(Result{ID: "inv", Outcome: OutcomeOK, Body: jsonval.FromProducer([]byte(`1`)), Settled: true})
		if d, ok := read().(RunDone); !ok || d.Status != "success" {
			t.Fatalf("try %d: run_done %+v", i, d)
		}
		p := <-attached
		hc.Close()
		if p != nil {
			<-p.Done()
		}
	}
}

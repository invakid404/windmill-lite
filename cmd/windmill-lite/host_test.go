package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
)

// These tests run host mode end to end (ADR 0004 host mode, ADR 0009): a
// Bun host compiled with `bun build --compile` (testdata/host/host.ts)
// embeds the host SDK of harnesses/bun/host and starts this package's test
// binary as its engine, `windmill-lite host`, on the W1 bundle of
// internal/engine/testdata/w1. A supervisor that only restarts the host
// when it exits stands in for the host's own, as ADR 0004's validation
// asks. They are the host-mode half of W1's exit gate and need the pinned
// Bun (WL_BUN); without it they skip, unless WL_REQUIRE_BUN is set.

// TestMain lets the test binary serve as the engine: the SDK starts it
// with the host command, and WL_TEST_ENGINE, which the supervisor sets,
// reaches it through the host's environment.
func TestMain(m *testing.M) {
	if os.Getenv("WL_TEST_ENGINE") == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

var (
	buildOnce sync.Once
	buildDir  string
	hostBin   string
	buildErr  error
)

// compiledHost builds the test host once per test binary.
func compiledHost(t *testing.T) string {
	t.Helper()
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set; it names the pinned Bun 1.3.10")
	}
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "wl-w1-host-")
		if buildErr != nil {
			return
		}
		hostBin = filepath.Join(buildDir, "w1host")
		cmd := exec.Command(bun, "build", "--compile", "testdata/host/host.ts", "--outfile", hostBin)
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("bun build --compile: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return hostBin
}

// supervisor runs the host and starts a new generation whenever the
// current one exits. It never restarts a host for any other reason.
type supervisor struct {
	t      *testing.T
	bin    string
	dir    string
	sock   string
	env    []string
	client *http.Client

	mu      sync.Mutex
	gens    []*generation
	stopped bool
	ready   chan *generation
}

type generation struct {
	n   int
	cmd *exec.Cmd
	pid int
	// stderr is a file, shared with the engine, which inherits it: a pipe
	// would make Wait wait for the engine's exit too, and the engine, a
	// race-detector test binary, sleeps a second at exit.
	stderr  *os.File
	exited  chan struct{}
	state   *os.ProcessState
	exitAt  time.Time
	started time.Time
}

func supervise(t *testing.T, killAfterMS int) *supervisor {
	t.Helper()
	bundle, err := filepath.Abs("../../internal/engine/testdata/w1")
	if err != nil {
		t.Fatal(err)
	}
	return superviseBundle(t, bundle, killAfterMS)
}

// superviseBundle runs the compiled host on a bundle of W1's skeleton
// format.
func superviseBundle(t *testing.T, bundle string, killAfterMS int) *supervisor {
	t.Helper()
	bin := compiledHost(t)
	engineBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A short directory: Unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "wl-w1-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &supervisor{
		t: t, bin: bin, dir: dir, sock: filepath.Join(dir, "host.sock"), ready: make(chan *generation, 4),
		env: append(os.Environ(), "WL_TEST_ENGINE=1", "WL_ENGINE="+engineBin, "WL_BUNDLE="+bundle,
			fmt.Sprintf("WL_KILL_AFTER_MS=%d", killAfterMS)),
	}
	s.env = append(s.env, "WL_HOST_SOCKET="+s.sock)
	s.client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.sock)
		},
		DisableKeepAlives: true,
	}}
	s.start()
	t.Cleanup(s.stop)
	s.waitReady(0)
	return s
}

func (s *supervisor) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	os.Remove(s.sock)
	g := &generation{n: len(s.gens), exited: make(chan struct{}), started: time.Now()}
	f, err := os.Create(filepath.Join(s.dir, fmt.Sprintf("host-%d.stderr", g.n)))
	if err != nil {
		s.t.Fatal(err)
	}
	g.stderr = f
	g.cmd = exec.Command(s.bin)
	g.cmd.Dir = s.dir
	g.cmd.Env = s.env
	g.cmd.Stderr = f
	out, err := g.cmd.StdoutPipe()
	if err != nil {
		s.t.Fatal(err)
	}
	if err := g.cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	g.pid = g.cmd.Process.Pid
	s.gens = append(s.gens, g)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "ready ") {
				s.ready <- g
			}
		}
	}()
	go func() {
		g.cmd.Wait()
		g.state, g.exitAt = g.cmd.ProcessState, time.Now()
		g.stderr.Close() // the engine keeps its own descriptor
		close(g.exited)
		// The supervisor's one rule: restart on exit.
		s.start()
	}()
}

func (s *supervisor) waitReady(n int) *generation {
	s.t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case g := <-s.ready:
			if g.n == n {
				return g
			}
		case <-deadline:
			s.t.Fatalf("host generation %d did not get ready; stderr:\n%s", n, s.gen(len(s.gens)-1).log())
		}
	}
}

// log returns what the generation and its engine wrote to stderr so far.
func (g *generation) log() string {
	b, _ := os.ReadFile(g.stderr.Name())
	return string(b)
}

func (s *supervisor) gen(n int) *generation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gens[n]
}

func (s *supervisor) generations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.gens)
}

func (s *supervisor) stop() {
	s.mu.Lock()
	s.stopped = true
	gens := s.gens
	s.mu.Unlock()
	for _, g := range gens {
		g.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-g.exited:
		case <-time.After(5 * time.Second):
			g.cmd.Process.Kill()
			<-g.exited
		}
	}
	if s.t.Failed() {
		for _, g := range gens {
			s.t.Logf("host generation %d stderr:\n%s", g.n, g.log())
		}
	}
}

type runResult struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
}

// run submits a run through the host, as one of its callers.
func (s *supervisor) run(path, tenant string, args any, intercept map[string]any) (runResult, error) {
	body, _ := json.Marshal(map[string]any{"path": path, "tenant": tenant, "args": args, "intercept": intercept})
	resp, err := s.client.Post("http://host/run", "application/json", bytes.NewReader(body))
	if err != nil {
		return runResult{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return runResult{}, err
	}
	if resp.StatusCode != 200 {
		return runResult{}, fmt.Errorf("host answered %d: %s", resp.StatusCode, b)
	}
	var r runResult
	err = json.Unmarshal(b, &r)
	return r, err
}

type hostHealth struct {
	InFlight          int  `json:"in_flight"`
	Abandoned         int  `json:"abandoned"`
	Errors            int  `json:"errors"`
	OldestAbandonedMS int  `json:"oldest_abandoned_ms"`
	Runs              int  `json:"runs"`
	Draining          bool `json:"draining"`
}

func (s *supervisor) health(t *testing.T) hostHealth {
	t.Helper()
	var h hostHealth
	s.getJSON(t, "/health", &h)
	return h
}

func (s *supervisor) getJSON(t *testing.T, path string, v any) {
	t.Helper()
	resp, err := s.client.Get("http://host" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

// W1's exit gate: "the host-provided-module ALS test 16/16 in a compiled
// host" (ADR 0008 H11, ADR 0010, D12). Sixteen concurrent runs, each
// submitted inside its own tenant scope; the step reads the scope through
// the host-provided module, by a shim written by hand, before and after
// awaits. The second step reads it through a separately bundled copy of
// the same module and sees nothing: the shim is what makes it work.
func TestHostScopesThroughAHostProvidedModule(t *testing.T) {
	s := supervise(t, 30000)
	type res struct {
		i int
		r runResult
		e error
	}
	results := make(chan res, 16)
	for i := range 16 {
		go func() {
			r, err := s.run("f/w1/scope", fmt.Sprintf("tenant-%d", i), map[string]any{"delay": (i * 5) % 16}, nil)
			results <- res{i, r, err}
		}()
	}
	shim, copies := 0, 0
	jobs, runs := map[string]bool{}, map[string]bool{}
	for range 16 {
		r := <-results
		if r.e != nil || r.r.Status != "success" {
			t.Fatalf("run %d: %+v, %v", r.i, r.r, r.e)
		}
		var got struct {
			Shim struct {
				Tenant, Before, Job, Run string
			}
			Copy *string
		}
		if err := json.Unmarshal(r.r.Result, &got); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("tenant-%d", r.i)
		if got.Shim.Tenant == want && got.Shim.Before == want {
			shim++
		}
		if got.Copy != nil {
			copies++
		}
		jobs[got.Shim.Job], runs[got.Shim.Run] = true, true
	}
	t.Logf("H11 in a compiled host on Bun 1.3.10: through the host-provided module %d/16; through a bundled copy %d/16 (expected 0)", shim, copies)
	if shim != 16 || copies != 0 || len(jobs) != 16 || len(runs) != 16 {
		t.Fatalf("shim %d/16, copy %d/16, %d job ids, %d run ids", shim, copies, len(jobs), len(runs))
	}
	if g := s.generations(); g != 1 {
		t.Fatalf("the host was restarted: %d generations", g)
	}
}

// C2b's exit gate (ADR 0012): "an esbuild CJS codebase script that imports
// a host-provided module reads the host's store 16/16 on Bun 1.3.10 in
// W1's host-mode skeleton". The script, testdata/codebase/cb_scope.ts, is
// built by bundle.BuildCodebase as the codebase script of a synthetic sync
// folder, its import of f/w1/scopes.ts resolved to the module's generated
// shim, and added to a copy of the W1 bundle with a one-step flow. Sixteen
// concurrent runs, each submitted inside its own tenant scope, read the
// host's scope through it before and after awaits, on Bun's CommonJS
// loader (D13).
func TestHostScopesThroughACodebaseScript(t *testing.T) {
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set; it names the pinned Bun 1.3.10")
	}
	bundleDir := codebaseBundle(t, bun)
	s := superviseBundle(t, bundleDir, 30000)
	type res struct {
		i int
		r runResult
		e error
	}
	results := make(chan res, 16)
	for i := range 16 {
		go func() {
			r, err := s.run("f/w1/cb_scope", fmt.Sprintf("tenant-%d", i), map[string]any{"delay": (i * 5) % 16}, nil)
			results <- res{i, r, err}
		}()
	}
	read := 0
	jobs, runs := map[string]bool{}, map[string]bool{}
	for range 16 {
		r := <-results
		if r.e != nil || r.r.Status != "success" {
			t.Fatalf("run %d: %+v, %v", r.i, r.r, r.e)
		}
		var got struct {
			Tenant, Before, Job, Run string
			BunEnv, CommonJS         bool
		}
		if err := json.Unmarshal(r.r.Result, &got); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("tenant-%d", r.i)
		if got.Tenant == want && got.Before == want && got.BunEnv && got.CommonJS {
			read++
		}
		jobs[got.Job], runs[got.Run] = true, true
	}
	t.Logf("C2b in a compiled host on Bun 1.3.10: an esbuild CommonJS codebase script read the host's store through the host-provided module %d/16", read)
	if read != 16 || len(jobs) != 16 || len(runs) != 16 {
		t.Fatalf("read %d/16, %d job ids, %d run ids", read, len(jobs), len(runs))
	}
	if g := s.generations(); g != 1 {
		t.Fatalf("the host was restarted: %d generations", g)
	}
}

// codebaseBundle returns a copy of the W1 bundle with the codebase script
// f/w1/cb_scope, built by esbuild as CommonJS, and its one-step flow.
func codebaseBundle(t *testing.T, bun string) string {
	t.Helper()
	folder := t.TempDir()
	scopes, err := os.ReadFile("testdata/host/scopes.ts")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("testdata/codebase/cb_scope.ts")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"wmill.yaml":       []byte("defaultTs: bun\ncodebases:\n  - relative_path: f/w1\n    includes: [\"f/w1/cb_*\"]\n"),
		"f/w1/scopes.ts":   scopes,
		"f/w1/cb_scope.ts": script,
	} {
		if data == nil {
			continue
		}
		p := filepath.Join(folder, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The shim the scan would generate for the module's one export.
	shim, err := bundle.Shim("f/w1/scopes.ts", []string{"store"})
	if err != nil {
		t.Fatal(err)
	}
	shimFile := filepath.Join(t.TempDir(), "scopes.mjs")
	if err := os.WriteFile(shimFile, shim, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "w1")
	copyDir(t, "../../internal/engine/testdata/w1", out)
	helper, err := filepath.Abs("../../harnesses/bun/build/main.ts")
	if err != nil {
		t.Fatal(err)
	}
	res, err := bundle.BuildCodebase(context.Background(), bundle.CodebaseConfig{
		Bun: bun, Helper: helper, Folder: folder, Out: out, Work: t.TempDir(),
		Entries: []bundle.CodebaseScript{{ID: "f/w1/cb_scope", Path: "f/w1/cb_scope", File: "f/w1/cb_scope.ts",
			Settings: json.RawMessage(`{"relative_path":"f/w1","includes":["f/w1/cb_*"]}`)}},
		HostModules: []bundle.CodebaseHostModule{{Key: "f/w1/scopes.ts", File: "f/w1/scopes.ts", Shim: shimFile}},
	})
	if err != nil {
		t.Fatalf("BuildCodebase: %v", err)
	}
	if m := res.Scripts["f/w1/cb_scope"]; m.Format != bundle.CJS || !slices.Equal(m.HostModules, []string{"f/w1/scopes.ts"}) {
		t.Fatalf("the codebase script's metadata: %+v", m)
	}
	flow := `{"summary": "C2b: an esbuild CommonJS codebase script reads the host's scope through a host-provided module",
  "value": {"modules": [{"id": "a", "value": {"type": "script", "path": "f/w1/cb_scope", "input_transforms": {"delay": {"type": "javascript", "expr": "flow_input.delay"}}}}]}}
`
	if err := os.WriteFile(filepath.Join(out, "flows", "f", "w1", "cb_scope.flow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

// copyDir copies the regular files below src into dst.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// W1's exit gate: "one detached rejection does not end the host" (ADR 0004,
// the shared-process failure model, item 1). The step returns, the host
// lives on and serves the next run, and the error is counted in health.
func TestHostSurvivesADetachedRejection(t *testing.T) {
	s := supervise(t, 30000)
	r, err := s.run("f/w1/reject", "t", map[string]any{}, nil)
	if err != nil || r.Status != "success" || string(r.Result) != `"survived"` {
		t.Fatalf("%+v, %v", r, err)
	}
	r, err = s.run("f/w1/echo", "t", map[string]any{"x": 1}, nil)
	if err != nil || r.Status != "success" {
		t.Fatalf("the next run: %+v, %v", r, err)
	}
	if h := s.health(t); h.Errors != 1 || h.InFlight != 0 {
		t.Fatalf("health %+v; want the one unattributed error", h)
	}
	if g := s.generations(); g != 1 {
		t.Fatalf("the host was restarted: %d generations", g)
	}
	if !strings.Contains(s.gen(0).log(), "unhandledRejection that no invocation owns: Error: detached rejection") {
		t.Fatalf("stderr:\n%s", s.gen(0).log())
	}
}

// W1's exit gate: the never-settling-step test of ADR 0004 (host mode,
// validation). Two concurrent runs, one of whose steps never settles:
// after that step's timeout, the other run's next step completes, a new
// run afterwards succeeds, and health reports one abandoned invocation.
// The host is never restarted (host mode item 1: no taint, no recycle).
func TestHostNeverSettlingStep(t *testing.T) {
	s := supervise(t, 30000)
	type res struct {
		r  runResult
		e  error
		at time.Time
	}
	never, other := make(chan res, 1), make(chan res, 1)
	start := time.Now()
	go func() {
		r, err := s.run("f/w1/timeout", "a", map[string]any{}, nil)
		never <- res{r, err, time.Now()}
	}()
	go func() {
		// Its first step waits well past the other step's timeout of 1 s.
		r, err := s.run("f/w1/two", "b", map[string]any{"ms": 2500}, nil)
		other <- res{r, err, time.Now()}
	}()
	n, o := <-never, <-other
	const timeoutText = `{"error":{"name":"ExecutionErr","message":"execution error:\njob process terminated due to timeout after exceeding job-specific duration limit","step_id":"a"}}`
	if n.e != nil || n.r.Status != "failure" || string(n.r.Result) != timeoutText {
		t.Fatalf("the never-settling run: %+v, %v", n.r, n.e)
	}
	if d := n.at.Sub(start); d < 900*time.Millisecond || d > 3*time.Second {
		t.Fatalf("the timeout fired after %v", d)
	}
	if o.e != nil || o.r.Status != "success" || string(o.r.Result) != `{"after":2500}` || !o.at.After(n.at) {
		t.Fatalf("the other run: %+v, %v, done %v after the timeout", o.r, o.e, o.at.Sub(n.at))
	}
	r, err := s.run("f/w1/echo", "c", map[string]any{"x": "new"}, nil)
	if err != nil || r.Status != "success" || string(r.Result) != `{"x":"new"}` {
		t.Fatalf("a new run: %+v, %v", r, err)
	}
	h := s.health(t)
	if h.Abandoned != 1 || h.InFlight != 0 || h.OldestAbandonedMS <= 0 || h.Runs != 0 {
		t.Fatalf("health %+v; want one abandoned invocation", h)
	}
	if g := s.generations(); g != 1 {
		t.Fatalf("the host was restarted: %d generations", g)
	}
}

// WR-55 in host mode under backpressure (FU6, review round 2): a step
// backs up the host's socket to the engine with a large log, starts a
// child and blocks the host's event loop. The child's group still reaches
// the engine, on the host's child channel, written synchronously, so the
// step's timeout kills the child although the host can neither handle the
// cancel nor write its socket, and long before the watchdog would end the
// host.
func TestHostChildUnderBackpressure(t *testing.T) {
	// spec: WR-55, WR-37
	s := supervise(t, 30000)
	pidFile := filepath.Join(s.dir, "pid")
	go s.run("f/w1/stall", "a", map[string]any{"pidFile": pidFile}, nil)
	var pid int
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil && len(bytes.TrimSpace(b)) > 0 {
			if _, err := fmt.Sscan(string(b), &pid); err == nil && pid > 1 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the step did not start its child")
		}
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	ended := func() bool {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		// A killed child of the blocked host stays unreaped. Its state is
		// field 3 of /proc/<pid>/stat, after the command name's last ')',
		// where there is a /proc, so a slim Linux image without ps can run
		// this (FU7); ps elsewhere.
		if st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			i := bytes.LastIndexByte(st, ')')
			return i >= 0 && i+2 < len(st) && st[i+2] == 'Z'
		}
		out, err := exec.Command("ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
		return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
	}
	for deadline := time.Now().Add(10 * time.Second); !ended(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the child outlived its step's timeout")
		}
	}
	if g := s.generations(); g != 1 {
		t.Fatalf("the host was restarted, %d generations: the timeout, not the host's end, was to kill the child", g)
	}
	select {
	case <-s.gen(0).exited:
		t.Fatal("the host ended: the timeout was to kill the child while the host still ran")
	default:
	}
}

// W1's exit gate: the watchdog test of ADR 0004 (host mode item 4,
// validation), with review item cl P4-3: a step that spins forever, with a
// second run active. The engine kills the host within watchdog.killAfter
// plus one second, naming the runs in flight on its stderr; the second
// run's caller sees the host's exit; a run on the restarted host succeeds.
func TestHostWatchdog(t *testing.T) {
	const killAfter = time.Second
	s := supervise(t, int(killAfter/time.Millisecond))
	first := s.gen(0)
	other := make(chan error, 1)
	go func() {
		_, err := s.run("f/w1/two", "b", map[string]any{"ms": 20000}, nil)
		other <- err
	}()
	// The second run's step must be in flight before the spin blocks the
	// host (W1 review, finding 2).
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if h := s.health(t); h.InFlight == 1 && h.Runs == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second run's step is not in flight: %+v", s.health(t))
		}
	}
	sent := time.Now()
	spun := make(chan error, 1)
	go func() {
		_, err := s.run("f/w1/spin", "a", map[string]any{}, nil)
		spun <- err
	}()
	select {
	case <-first.exited:
	case <-time.After(killAfter + 5*time.Second):
		t.Fatal("the host was not killed")
	}
	if d := first.exitAt.Sub(sent); d > killAfter+time.Second {
		t.Fatalf("killed %v after the spinning run was sent; want within killAfter + 1 s", d)
	}
	if ws, ok := first.state.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("the host ended with %v, not SIGKILL", first.state)
	}
	if err := <-other; err == nil {
		t.Fatal("the second run's caller did not see the host's exit")
	}
	if err := <-spun; err == nil {
		t.Fatal("the spinning run's caller did not see the host's exit")
	}
	line := ""
	for _, l := range strings.Split(first.log(), "\n") {
		if strings.Contains(l, "watchdog: no frame from the host") {
			line = l
		}
	}
	// The run list ends where the invocations begin; it must name two
	// distinct runs (W1 review, finding 2).
	uuid := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`)
	_, runs, _ := strings.Cut(line, "in-flight runs:")
	runs, _, _ = strings.Cut(runs, "; invocations:")
	distinct := map[string]bool{}
	for _, id := range uuid.FindAllString(runs, -1) {
		distinct[id] = true
	}
	if !strings.Contains(line, fmt.Sprintf("sending SIGKILL to the host, pid %d", first.pid)) || len(distinct) != 2 {
		t.Fatalf("the engine's diagnostic line names the host and both runs; got %q", line)
	}
	s.waitReady(1)
	r, err := s.run("f/w1/echo", "c", map[string]any{"x": 2}, nil)
	if err != nil || r.Status != "success" || string(r.Result) != `{"x":2}` {
		t.Fatalf("a run on the restarted host: %+v, %v", r, err)
	}
	t.Logf("watchdog: the host was killed %v after the spinning run was sent (killAfter %v)", first.exitAt.Sub(sent).Round(time.Millisecond), killAfter)
}

// H1's no-code form through host mode: the run frame carries a static
// stand-in for module b, whose script would throw.
func TestHostStaticIntercept(t *testing.T) {
	s := supervise(t, 30000)
	r, err := s.run("f/w1/intercept", "t", map[string]any{"x": 7}, map[string]any{"b": map[string]any{"stood": "in"}})
	if err != nil || r.Status != "success" || string(r.Result) != `{"from_b":{"stood":"in"}}` {
		t.Fatalf("%+v, %v", r, err)
	}
}

// W1's exit gate: "a first per-step latency figure", in host mode: a
// one-step flow run by the compiled host, timed around host.run. Report
// only.
func TestHostStepLatency(t *testing.T) {
	s := supervise(t, 30000)
	var p struct {
		N     int   `json:"n"`
		P50US int64 `json:"p50_us"`
		P95US int64 `json:"p95_us"`
		P99US int64 `json:"p99_us"`
	}
	s.getJSON(t, "/bench?n=300", &p)
	if p.N != 300 {
		t.Fatalf("bench %+v", p)
	}
	t.Logf("W1 per-step latency in host mode, one-step flow in a compiled Bun 1.3.10 host, %d sequential runs: p50 %dµs, p95 %dµs, p99 %dµs", p.N, p.P50US, p.P95US, p.P99US)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

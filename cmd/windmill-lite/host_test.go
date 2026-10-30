package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// These tests run host mode end to end (ADR 0004 host mode, ADR 0009): a
// Bun host compiled with `bun build --compile` (testdata/host/host.ts)
// embeds the host SDK of harnesses/bun/host and starts this package's test
// binary as its engine, `windmill-lite host`, on a bundle that
// bundle.Build builds from the synthetic sync folder testdata/hostfolder
// and its host contract. A supervisor that only restarts the host when it
// exits stands in for the host's own, as ADR 0004's validation asks. They
// are the host-mode half of W1's exit gate, C2b's host-mode gate and F3a's
// exit gate, and need the pinned Bun (WL_BUN) and the build helper's
// dependencies; without WL_BUN they skip, unless WL_REQUIRE_BUN is set.

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
	buildOnce  sync.Once
	buildDir   string
	hostBin    string
	hostBundle string
	buildErr   error
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
		buildDir, buildErr = os.MkdirTemp("", "wl-host-")
		if buildErr != nil {
			return
		}
		hostBin = filepath.Join(buildDir, "testhost")
		cmd := exec.Command(bun, "build", "--compile", "testdata/host/host.ts", "--outfile", hostBin)
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("bun build --compile: %v\n%s", err, out)
			return
		}
		hostBundle, buildErr = buildHostBundle(bun, filepath.Join(buildDir, "bundle"))
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return hostBin
}

// buildHostBundle builds testdata/hostfolder with its host contract into
// out, for this binary's engine version.
func buildHostBundle(bun, out string) (string, error) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		return "", err
	}
	contract, err := os.ReadFile("testdata/hostfolder/contract.yaml")
	if err != nil {
		return "", err
	}
	_, err = bundle.Build(context.Background(), bundle.Config{
		Folder:         "testdata/hostfolder",
		Contract:       contract,
		EngineVersion:  resolveVersion(),
		Bun:            bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
		Out:            out,
		Work:           filepath.Join(filepath.Dir(out), "work"),
	})
	if err != nil {
		return "", fmt.Errorf("bundle.Build: %w", err)
	}
	return out, nil
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

// supervise runs the compiled host on the host bundle, with env added to
// its environment.
func supervise(t *testing.T, killAfterMS int, env ...string) *supervisor {
	t.Helper()
	bin := compiledHost(t)
	engineBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A short directory: Unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "wl-h-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &supervisor{
		t: t, bin: bin, dir: dir, sock: filepath.Join(dir, "host.sock"), ready: make(chan *generation, 4),
		env: append(append(os.Environ(), "WL_TEST_ENGINE=1", "WL_ENGINE="+engineBin, "WL_BUNDLE="+hostBundle,
			fmt.Sprintf("WL_KILL_AFTER_MS=%d", killAfterMS)), env...),
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
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	Result         json.RawMessage `json:"result"`
	MayHaveMutated bool            `json:"mayHaveMutated"`
	Warnings       []runWarning    `json:"warnings"`
	// Refused is the code of the HostError of a run that did not start.
	Refused string `json:"refused"`
	// Latched is the error a latched run latched.
	Latched string `json:"latched"`
	// Deviations are the run's run-time deviations (H7, H10).
	Deviations []string `json:"deviations"`
	// DoneAt and SettledAt are, for /settle, when the outcome came and
	// when the run settled, in milliseconds after it was submitted.
	DoneAt    float64 `json:"doneAt"`
	SettledAt float64 `json:"settledAt"`
	// SettledTenant is, for /settle, the tenant onSettled read from the
	// host's scope.
	SettledTenant *string `json:"settledTenant"`
}

// run submits a run of the flow at path through the host, as one of its
// callers, in its tenant scope.
func (s *supervisor) run(path, tenant string, args any, intercept map[string]any) (runResult, error) {
	return s.post("/run", map[string]any{"path": path, "tenant": tenant, "args": args, "intercept": intercept})
}

// post sends a run's body to one of the host's endpoints.
func (s *supervisor) post(endpoint string, run map[string]any) (runResult, error) {
	body, _ := json.Marshal(run)
	resp, err := s.client.Post("http://host"+endpoint, "application/json", bytes.NewReader(body))
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
	InFlight          int     `json:"in_flight"`
	Abandoned         int     `json:"abandoned"`
	Errors            int     `json:"errors"`
	OldestAbandonedMS int     `json:"oldest_abandoned_ms"`
	Modules           int     `json:"modules"`
	Runs              int     `json:"runs"`
	Draining          bool    `json:"draining"`
	Recycle           *string `json:"recycle"`
	Recycled          *string `json:"recycled"`
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
// host" (ADR 0008 H11, ADR 0010, D12), on a built bundle (F3a). Sixteen
// concurrent runs, each submitted inside its own tenant scope; the step
// reads the scope through the host-provided module, by the shim the build
// generated, before and after awaits. The second step reads it through a
// separately bundled copy of the same module and sees nothing: the shim
// and the host's registration are what make it work.
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
			r, err := s.run("f/host/scope", fmt.Sprintf("tenant-%d", i), map[string]any{"delay": (i * 5) % 16}, nil)
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
// a host-provided module reads the host's store 16/16 on Bun 1.3.10", now
// in host mode on wl and a built bundle (F3a). The script,
// testdata/hostfolder/f/hostcb/cb_scope.ts, is the CommonJS codebase
// script of the folder, its import of f/host/scopes.ts resolved to the
// module's generated shim. Sixteen concurrent runs, each submitted inside
// its own tenant scope, read the host's scope through it before and after
// awaits, on Bun's CommonJS loader (D13).
func TestHostScopesThroughACodebaseScript(t *testing.T) {
	s := supervise(t, 30000)
	type res struct {
		i int
		r runResult
		e error
	}
	results := make(chan res, 16)
	for i := range 16 {
		go func() {
			r, err := s.run("f/host/cb_scope", fmt.Sprintf("tenant-%d", i), map[string]any{"delay": (i * 5) % 16}, nil)
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

// F3h's host-mode case: a codebase script imports a host-provided module
// outside the sync folder (testdata/hostlib/tenant.ts, in the codebases
// entry ../hostlib), which the host contract lists by its path from the
// sync root. The host registers its own instance under that key, and
// sixteen concurrent runs, each submitted inside its own tenant scope of
// the module, read it before and after awaits.
func TestHostScopesThroughAnOutsideModule(t *testing.T) {
	s := supervise(t, 30000)
	type res struct {
		i int
		r runResult
		e error
	}
	results := make(chan res, 16)
	for i := range 16 {
		go func() {
			r, err := s.run("f/host/outside", fmt.Sprintf("tenant-%d", i), map[string]any{"delay": (i * 5) % 16}, nil)
			results <- res{i, r, err}
		}()
	}
	read := 0
	for range 16 {
		r := <-results
		if r.e != nil || r.r.Status != "success" {
			t.Fatalf("run %d: %+v, %v", r.i, r.r, r.e)
		}
		var got struct{ Tenant, Before string }
		if err := json.Unmarshal(r.r.Result, &got); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("tenant-%d", r.i); got.Tenant == want && got.Before == want {
			read++
		}
	}
	t.Logf("F3h in a compiled host on Bun 1.3.10: a codebase script read the host's store through a host-provided module outside the folder %d/16", read)
	if read != 16 {
		t.Fatalf("read %d/16", read)
	}
	if g := s.generations(); g != 1 {
		t.Fatalf("the host was restarted: %d generations", g)
	}
}

// W1's exit gate: "one detached rejection does not end the host" (ADR 0004,
// the shared-process failure model, item 1). The step returns, the host
// lives on and serves the next run, and the error is counted in health.
func TestHostSurvivesADetachedRejection(t *testing.T) {
	s := supervise(t, 30000)
	r, err := s.run("f/host/reject", "t", map[string]any{}, nil)
	if err != nil || r.Status != "success" || string(r.Result) != `"survived"` {
		t.Fatalf("%+v, %v", r, err)
	}
	r, err = s.run("f/host/echo", "t", map[string]any{"x": 1}, nil)
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
		r, err := s.run("f/host/timeout", "a", map[string]any{}, nil)
		never <- res{r, err, time.Now()}
	}()
	go func() {
		// Its first step waits well past the other step's timeout of 1 s.
		r, err := s.run("f/host/two", "b", map[string]any{"ms": 2500}, nil)
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
	r, err := s.run("f/host/echo", "c", map[string]any{"x": "new"}, nil)
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
	go s.run("f/host/stall", "a", map[string]any{"pidFile": pidFile}, nil)
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
		_, err := s.run("f/host/two", "b", map[string]any{"ms": 20000}, nil)
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
		_, err := s.run("f/host/spin", "a", map[string]any{}, nil)
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
	r, err := s.run("f/host/echo", "c", map[string]any{"x": 2}, nil)
	if err != nil || r.Status != "success" || string(r.Result) != `{"x":2}` {
		t.Fatalf("a run on the restarted host: %+v, %v", r, err)
	}
	t.Logf("watchdog: the host was killed %v after the spinning run was sent (killAfter %v)", first.exitAt.Sub(sent).Round(time.Millisecond), killAfter)
}

// H1's no-code form through host mode: the run frame carries a static
// stand-in for module b, whose script would throw.
func TestHostStaticIntercept(t *testing.T) {
	s := supervise(t, 30000)
	r, err := s.run("f/host/intercept", "t", map[string]any{"x": 7}, map[string]any{"b": map[string]any{"stood": "in"}})
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
	t.Logf("per-step latency in host mode, one-step flow in a compiled Bun 1.3.10 host, %d sequential runs: p50 %dµs, p95 %dµs, p99 %dµs", p.N, p.P50US, p.P95US, p.P99US)
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

// F3a's exit gate (ADR 0012): "a codebase script reads its extracted asset
// in host mode". The host SDK extracts the bundle's assets into the host's
// working directory before any step loads (ADR 0009, D10), where Windmill
// unpacks a codebase entry's assets beside its bundle (WR-49, WR-50): the
// script reads its asset by a relative path and beside its own module,
// and reads the arguments file the engine wrote there (WR-5).
func TestHostCodebaseAsset(t *testing.T) {
	s := supervise(t, 30000)
	r, err := s.run("f/host/cb_asset", "t", map[string]any{"tag": "asset"}, nil)
	if err != nil || r.Status != "success" {
		t.Fatalf("%+v, %v", r, err)
	}
	var got struct {
		Relative, Beside, Tag string
		Args                  map[string]any
	}
	if err := json.Unmarshal(r.Result, &got); err != nil {
		t.Fatal(err)
	}
	const want = "hello from an extracted asset"
	if got.Relative != want || got.Beside != want || got.Tag != "asset" || got.Args["tag"] != "asset" {
		t.Fatalf("the codebase script read %+v", got)
	}
	if b, err := os.ReadFile(filepath.Join(s.dir, "data", "greeting.txt")); err != nil || strings.TrimSpace(string(b)) != want {
		t.Fatalf("the asset in the host's working directory: %q, %v", b, err)
	}
	t.Logf("F3a: a codebase script read its extracted asset in host mode: %q", got.Relative)
}

// F3a's exit gate: the workdir_not_writable and workdir_mismatch cases,
// end to end through the host SDK, which refuses before it starts the
// engine (ADR 0004, the shared-process failure model, item 5): a host that
// passes another directory than its own, and one whose working directory
// it cannot write.
func TestHostWorkdirRefusedBySDK(t *testing.T) {
	bin := compiledHost(t)
	engineBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, code string
		readOnly   bool
	}{
		{"another directory", "workdir_mismatch", false},
		{"read-only", "workdir_not_writable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.readOnly && os.Geteuid() == 0 {
				t.Skip("root writes through any mode")
			}
			cwd, other := t.TempDir(), t.TempDir()
			passed := other
			if tc.readOnly {
				passed = cwd
				if err := os.Chmod(cwd, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(cwd, 0o755) })
			}
			cmd := exec.Command(bin)
			cmd.Dir = cwd
			cmd.Env = append(os.Environ(), "WL_TEST_ENGINE=1", "WL_ENGINE="+engineBin, "WL_BUNDLE="+hostBundle,
				"WL_HOST_SOCKET="+filepath.Join(other, "host.sock"), "WL_WORKDIR="+passed)
			out, err := cmd.Output()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 3 || !strings.HasPrefix(string(out), "refused "+tc.code+" ") {
				t.Fatalf("the host: %v, stdout %q; want it refused with %s", err, out, tc.code)
			}
		})
	}
}

// F3a's exit gate, from the engine's side: `windmill-lite host` checks the
// directory it was passed against the workdir of the host's hello, and
// refuses the host in the handshake, so that a host that does not use the
// SDK is held to the same rule (harness.CheckWorkdir). The test plays the
// host itself: it starts the engine as its child with a socketpair as file
// descriptor 3 and greets it. An engine that cannot open the bundle
// refuses the host too, with its reason.
func TestHostWorkdirRefusedByEngine(t *testing.T) {
	compiledHost(t) // builds the bundle
	manifest, err := os.ReadFile(filepath.Join(hostBundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifest)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		name     string
		readOnly bool
		other    bool
		flags    []string
		code     string
		plugin   string // the host plugin declaration of the hello (F2c)
	}{
		{"its own working directory", false, false, nil, "", ""},
		{"another directory", false, true, nil, harness.RejectWorkdirMismatch, ""},
		{"read-only", true, false, nil, harness.RejectWorkdirNotWritable, ""},
		{"a bundle that does not open", false, false, []string{"-digest", "sha256:0000"}, harness.RejectEngine, ""},
		{"a host plugin with a malformed selector", false, false, nil, harness.RejectPlugin, `{"intercept":[{"selector":{"flow":"f/host/echo"}}]}`},
		{"a usable host plugin", false, false, nil, "", `{"halt_before":[{"flow":"f/host/echo","module":"a"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.readOnly && os.Geteuid() == 0 {
				t.Skip("root writes through any mode")
			}
			workdir, hostCwd := t.TempDir(), ""
			hostCwd = workdir
			if tc.other {
				hostCwd = t.TempDir()
			}
			if tc.readOnly {
				if err := os.Chmod(workdir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(workdir, 0o755) })
			}
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
			f, theirs := os.NewFile(uintptr(fds[0]), "host"), os.NewFile(uintptr(fds[1]), "engine")
			mine, err := net.FileConn(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			defer mine.Close()
			engineBin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(engineBin, append([]string{"host", "--bundle", hostBundle, "--workdir", workdir, "--kill-after", "0"}, tc.flags...)...)
			cmd.Env = append(os.Environ(), "WL_TEST_ENGINE=1")
			cmd.ExtraFiles = []*os.File{theirs}
			var stderr syncBuffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			theirs.Close()
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			hello := harness.Hello{Proto: harness.ProtocolVersion, Harness: harness.HarnessInfo{Kind: "bun", Version: "test"}, Runtime: harness.PinnedBun,
				Caps: harness.Caps{Fetch: true, Native: true, Host: true}, Bundle: digest, PID: os.Getpid(), Workdir: hostCwd}
			if tc.plugin != "" {
				hello.Plugin = jsonval.FromProducer([]byte(tc.plugin))
			}
			payload, err := harness.Encode(hello)
			if err != nil {
				t.Fatal(err)
			}
			frame := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
			if _, err := mine.Write(append(frame, payload...)); err != nil {
				t.Fatal(err)
			}
			mine.SetReadDeadline(time.Now().Add(20 * time.Second))
			var head [4]byte
			if _, err := io.ReadFull(mine, head[:]); err != nil {
				select {
				case <-exited:
				case <-time.After(5 * time.Second):
				}
				t.Fatalf("no welcome: %v; stderr:\n%s", err, stderr.String())
			}
			body := make([]byte, binary.BigEndian.Uint32(head[:]))
			if _, err := io.ReadFull(mine, body); err != nil {
				t.Fatal(err)
			}
			m, err := harness.Decode(body)
			if err != nil {
				t.Fatal(err)
			}
			w, ok := m.(harness.Welcome)
			if !ok {
				t.Fatalf("got %T; want the welcome", m)
			}
			mine.Close()
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				cmd.Process.Kill()
				t.Fatal("the engine did not exit once the host's connection ended")
			}
			if tc.code == "" {
				if w.Config == nil || w.Config.ModuleState != "warm" || w.Config.Facade == "" {
					t.Fatalf("welcome %+v; stderr:\n%s", w, stderr.String())
				}
				return
			}
			if w.Reject == nil || w.Reject.Code != tc.code {
				t.Fatalf("welcome %+v; want a reject with %s; stderr:\n%s", w, tc.code, stderr.String())
			}
			t.Logf("refused: %s: %s", w.Reject.Code, w.Reject.Message)
		})
	}
}

// ADR 0004 host mode item 3: drain(timeout) refuses new runs, in the SDK
// and in the engine, and waits for the run in flight, which completes; the
// engine says bye once it has ended, and the host lives on.
func TestHostDrain(t *testing.T) {
	s := supervise(t, 30000)
	long := make(chan runResult, 1)
	go func() {
		r, _ := s.run("f/host/two", "a", map[string]any{"ms": 1500}, nil)
		long <- r
	}()
	for deadline := time.Now().Add(10 * time.Second); s.health(t).Runs != 1; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the long run did not start")
		}
	}
	type drained struct {
		MS     float64    `json:"ms"`
		Health hostHealth `json:"health"`
	}
	done := make(chan drained, 1)
	go func() {
		var d drained
		s.getJSON(t, "/drain?ms=20000", &d)
		done <- d
	}()
	time.Sleep(100 * time.Millisecond)
	if r, err := s.run("f/host/echo", "b", map[string]any{"x": 1}, nil); err != nil || r.Refused != "draining" {
		t.Fatalf("a run during the drain: %+v, %v; want it refused, draining", r, err)
	}
	d := <-done
	if r := <-long; r.Status != "success" || string(r.Result) != `{"after":1500}` {
		t.Fatalf("the run in flight: %+v", r)
	}
	if d.MS < 900 || d.MS > 10000 || d.Health.Runs != 0 || !d.Health.Draining {
		t.Fatalf("drain %+v; want it to wait for the run in flight", d)
	}
	if g := s.generations(); g != 1 {
		t.Fatalf("the host was restarted: %d generations", g)
	}
}

// Review round 1, finding 1, end to end: a drain that starts while a run
// whose outcome is larger than one frame is in flight ends only after that
// run's caller has its whole outcome; the engine's bye waits for it.
func TestHostDrainAfterALargeOutcome(t *testing.T) {
	s := supervise(t, 30000)
	const size = 3 << 20
	var got struct {
		Order  []string   `json:"order"`
		Status string     `json:"status"`
		Length int        `json:"length"`
		Health hostHealth `json:"health"`
	}
	body, _ := json.Marshal(map[string]any{"path": "f/host/big", "tenant": "t", "args": map[string]any{"ms": 1000, "size": size}})
	resp, err := s.client.Post("http://host/drainorder?after=300", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "success" || got.Length != size || len(got.Order) != 2 || got.Order[0] != "run" || got.Order[1] != "drain" || got.Health.Runs != 0 {
		t.Fatalf("drain with a large outcome in flight: %+v", got)
	}
}

// onRunSettled across the connection (ADR 0004 host mode item 5, H9): a
// step's timeout abandons it while it runs on, so its run's outcome comes
// at about 1 s, and the run settles only when the abandoned step has ended,
// at about 1.5 s; the host hears both, in that order.
func TestHostRunSettled(t *testing.T) {
	s := supervise(t, 30000)
	r, err := s.post("/settle", map[string]any{"path": "f/host/linger", "tenant": "linger", "args": map[string]any{"ms": 1500}})
	if err != nil || r.Status != "failure" {
		t.Fatalf("%+v, %v", r, err)
	}
	if r.DoneAt < 900 || r.DoneAt > 1450 || r.SettledAt < 1450 || r.SettledAt > 5000 {
		t.Fatalf("the outcome at %.0f ms, settled at %.0f ms; want about 1000 and 1500", r.DoneAt, r.SettledAt)
	}
	// onSettled runs in the run's own scope (H11; review round 1, finding
	// 3), after a timeout abandoned its step and its outcome went out.
	if r.SettledTenant == nil || *r.SettledTenant != "linger" {
		t.Fatalf("onSettled read the tenant %v; want the run's, linger", r.SettledTenant)
	}
	t.Logf("onRunSettled: the outcome at %.0f ms, settled at %.0f ms", r.DoneAt, r.SettledAt)
	// A run that abandoned nothing settles with its outcome.
	r, err = s.post("/settle", map[string]any{"path": "f/host/echo", "tenant": "echo", "args": map[string]any{"x": 1}})
	if err != nil || r.Status != "success" || r.SettledAt < 0 || r.SettledTenant == nil || *r.SettledTenant != "echo" {
		t.Fatalf("%+v, %v", r, err)
	}
}

// A host runs a root script as a script job (WA-4), and gets a run that
// does not start refused, with its reason.
func TestHostScriptRun(t *testing.T) {
	s := supervise(t, 30000)
	r, err := s.post("/run", map[string]any{"path": "f/host/sleep", "kind": "script", "args": map[string]any{"ms": 5}})
	if err != nil || r.Status != "success" || string(r.Result) != "5" || r.ID == "" {
		t.Fatalf("%+v, %v", r, err)
	}
	r, err = s.run("f/host/no_such_flow", "t", map[string]any{}, nil)
	if err != nil || r.Refused != "refused" {
		t.Fatalf("%+v, %v; want it refused", r, err)
	}
}

// Host mode serves the platform facade (ADR 0007): a step reads a variable
// of the bundle through the job API at the BASE_URL its invocation
// carries, as a warm process's steps do.
func TestHostFacade(t *testing.T) {
	s := supervise(t, 30000)
	r, err := s.run("f/host/variable", "t", map[string]any{}, nil)
	if err != nil || r.Status != "success" {
		t.Fatalf("%+v, %v", r, err)
	}
	var got struct {
		Status int
		Value  string
		Base   string
	}
	if err := json.Unmarshal(r.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != 200 || got.Value != "hello from the bundle" || !strings.HasPrefix(got.Base, "http://127.0.0.1:") {
		t.Fatalf("the step read %+v", got)
	}
}

// A recycle trigger (the modules one) trips once more script modules are
// loaded than the host allows: health reports it and onRecycle hears it,
// and the host decides what to do (here, nothing: it lives on).
func TestHostRecycleModules(t *testing.T) {
	s := supervise(t, 30000, "WL_RECYCLE_MODULES=1")
	if r, err := s.run("f/host/echo", "t", map[string]any{"x": 1}, nil); err != nil || r.Status != "success" {
		t.Fatalf("%+v, %v", r, err)
	}
	if h := s.health(t); h.Modules != 1 || h.Recycle != nil {
		t.Fatalf("health %+v after one module", h)
	}
	if r, err := s.run("f/host/two", "t", map[string]any{"ms": 1}, nil); err != nil || r.Status != "success" {
		t.Fatalf("%+v, %v", r, err)
	}
	// The trigger is checked at every heartbeat (a second by default).
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		h := s.health(t)
		if h.Recycle != nil && *h.Recycle == "modules" && h.Recycled != nil && *h.Recycled == "modules" && h.Modules == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("health %+v; want the modules trigger", h)
		}
	}
}

// F2c's exit gate in host mode: the plugin cases of
// conformance/plugins/hooks.yaml, answered by the host's own TypeScript
// plugin (the host SDK's plugin option) over host_call. Every case gives
// its expected status, result, latched error, log and warnings, as through
// wl in warm processes; and every hook runs in the scope of the run it
// serves (H11), which the host's onRunEnd reports as the tenant it reads.
func TestHostPluginCases(t *testing.T) {
	compiledHost(t)
	f, err := conformance.LoadPluginFile("../../conformance/plugins/hooks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bundleDir := buildPluginBundle(t, f)
	var cases []map[string]any
	for _, c := range f.Cases {
		cases = append(cases, map[string]any{"flow": c.FlowPath(), "plugin": c.Plugin})
	}
	casesFile := filepath.Join(t.TempDir(), "cases.json")
	b, _ := json.Marshal(cases)
	if err := os.WriteFile(casesFile, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s := supervise(t, 30000, "WL_BUNDLE="+bundleDir, "WL_PLUGIN_CASES="+casesFile,
		fmt.Sprintf("WL_ENGINE_FLAGS=-plugin-timeout %dms", f.TimeoutMS))
	for _, c := range f.Cases {
		t.Run(c.ID, func(t *testing.T) {
			tenant := "tenant-" + c.ID
			r, err := s.post("/run", caseRunBody(c, tenant))
			if err != nil || r.Refused != "" {
				t.Fatalf("%+v, %v", r, err)
			}
			o := conformance.PluginOutcome{Status: r.Status, MayHaveMutated: r.MayHaveMutated, Result: r.Result, Latched: r.Latched, Deviations: r.Deviations}
			scope := ""
			for _, w := range r.Warnings {
				o.Warnings = append(o.Warnings, struct{ Code, Message string }{w.Code, w.Message})
				if w.Code == "plugin_scope" {
					scope = w.Message
				}
			}
			if d := c.Check(o); len(d) > 0 {
				t.Fatalf("%s:\n  %s", c.Title, strings.Join(d, "\n  "))
			}
			if scope != tenant {
				t.Fatalf("the plugin's onRunEnd ran in the scope of tenant %q, want the run's, %q", scope, tenant)
			}
		})
	}
	// H9's onRunSettled through the host's plugin: a later run reports the
	// earlier one among those that settled (plugin_settled).
	first, err := s.run(f.Cases[0].FlowPath(), "t", map[string]any{}, nil)
	if err != nil || first.ID == "" {
		t.Fatalf("%+v, %v", first, err)
	}
	heard := false
	for deadline := time.Now().Add(5 * time.Second); !heard && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		r, err := s.run(f.Cases[0].FlowPath(), "t", map[string]any{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range r.Warnings {
			var settled []string
			if w.Code == "plugin_settled" && json.Unmarshal([]byte(w.Message), &settled) == nil && slices.Contains(settled, first.ID) {
				heard = true
			}
		}
	}
	if !heard {
		t.Fatalf("the host plugin's onRunSettled never heard of run %s", first.ID)
	}
	// The step API's signal in host mode: run_deadline's step waits for
	// wl.signal() alone, so its run settles only once the deadline the run
	// frame carried has aborted it.
	for _, c := range f.Cases {
		if c.ID != "run_deadline" {
			continue
		}
		r, err := s.post("/settle?wait=10000", caseRunBody(c, "t"))
		if err != nil || r.Status != "canceled" || r.SettledAt < 0 {
			t.Fatalf("%+v, %v: want canceled, then settled once wl.signal() aborted", r, err)
		}
	}
	if g := s.generations(); g != 1 {
		t.Fatalf("the host was restarted: %d generations", g)
	}
}

// caseRunBody is a plugin case's run as the host submits it: in tenant's
// scope, with the run's budgets and latches in its run options, which the
// host SDK carries in the run frame.
func caseRunBody(c conformance.PluginCase, tenant string) map[string]any {
	body := map[string]any{"path": c.FlowPath(), "tenant": tenant, "args": map[string]any{}}
	r := c.Run
	if len(r.WriteCapable) > 0 {
		var sels []map[string]any
		for _, m := range r.WriteCapable {
			sels = append(sels, map[string]any{"flow": c.FlowPath(), "module": m})
		}
		body["writeCapable"] = sels
	}
	if len(r.FatalErrorNames) > 0 {
		body["fatalErrorNames"] = r.FatalErrorNames
	}
	if r.DeadlineMS > 0 {
		body["deadlineMs"] = r.DeadlineMS
	}
	if r.PreWriteDeadlineMS > 0 {
		body["preWriteDeadlineMs"] = r.PreWriteDeadlineMS
	}
	if r.Clock != "" {
		body["clock"] = r.Clock
	}
	return body
}

// buildPluginBundle builds the plugin cases' folder and contract into a
// bundle, for this binary's engine version.
func buildPluginBundle(t *testing.T, f *conformance.PluginFile) string {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	if err := f.WriteFolder(folder); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder: folder, Contract: f.Contract(), EngineVersion: resolveVersion(), Bun: os.Getenv("WL_BUN"),
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
		Out:            out, Work: filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

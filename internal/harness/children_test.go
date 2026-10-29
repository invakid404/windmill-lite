package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// The Bun cache directory is <WINDMILL_DIR>/cache_nomount/bun, and
// NODE_PATH is the engine's, only when it has one (WR-22/b).
func TestBaseEnvCacheDirAndNodePath(t *testing.T) {
	// spec: WR-22/b
	lookup := func(env []string, k string) (string, bool) {
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, k+"="); ok {
				return v, true
			}
		}
		return "", false
	}
	t.Setenv("WINDMILL_DIR", "")
	t.Setenv("NODE_PATH", "")
	os.Unsetenv("NODE_PATH")
	env := BaseEnv()
	if v, _ := lookup(env, "BUN_INSTALL_CACHE_DIR"); v != "/tmp/windmill/cache_nomount/bun" {
		t.Errorf("BUN_INSTALL_CACHE_DIR %q, want the reference image's", v)
	}
	if v, ok := lookup(env, "NODE_PATH"); ok {
		t.Errorf("NODE_PATH %q, set though the engine has none", v)
	}
	t.Setenv("WINDMILL_DIR", "/srv/wm")
	t.Setenv("NODE_PATH", "/opt/node")
	env = BaseEnv()
	if v, _ := lookup(env, "BUN_INSTALL_CACHE_DIR"); v != "/srv/wm/cache_nomount/bun" {
		t.Errorf("BUN_INSTALL_CACHE_DIR %q under WINDMILL_DIR=/srv/wm", v)
	}
	if v, ok := lookup(env, "NODE_PATH"); !ok || v != "/opt/node" {
		t.Errorf("NODE_PATH %q (%v), want the engine's", v, ok)
	}
}

// childrenConfig is bunConfig on the bundle of testdata/children.
func childrenConfig(t *testing.T) ProcessConfig {
	t.Helper()
	cfg, _ := bunConfig(t)
	dir, err := filepath.Abs("testdata/children")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	cfg.Bundle = "sha256:" + hex.EncodeToString(sum[:])
	cfg.Path, cfg.Args = BunCommand(cfg.Path, repoPath(t, "harnesses/bun/runtime/main.ts"), dir)
	return cfg
}

// A step on the reference's environment sees the Bun cache directory and
// no NODE_PATH (WR-22/b).
func TestBunJobEnvironment(t *testing.T) {
	// spec: WR-22/b
	t.Setenv("WINDMILL_DIR", "")
	t.Setenv("NODE_PATH", "")
	os.Unsetenv("NODE_PATH")
	p := spawn(t, childrenConfig(t))
	out, err := execute(t, p, newInvocation("bun-env", `{}`, newRecorder()), 10*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != `["/tmp/windmill/cache_nomount/bun","unset"]` {
		t.Fatalf("got %s %s, %v", out.Kind, out.Body.Producer(), err)
	}
}

// childArgs is spawn-child's or spawn-hold's argument text.
func childArgs(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// childPID is the pid a step returned or wrote.
func childPID(t *testing.T, text []byte) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	if err != nil || pid <= 0 {
		t.Fatalf("child pid %q: %v", text, err)
	}
	return pid
}

// leftover kills a child the test leaves running.
func leftover(t *testing.T, pid int) {
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
}

// waitAll is this test binary run with WL_WAIT_ALL set: a program that
// waits for all its children, as wait() and waitpid(-1) do, then prints
// done. It has none of its own, so it waits for any the harness gave it.
func waitAll() int {
	for {
		var ws syscall.WaitStatus
		_, err := syscall.Wait4(-1, &ws, 0, nil)
		if errors.Is(err, syscall.ECHILD) {
			break
		}
		if err != nil && !errors.Is(err, syscall.EINTR) {
			return 70
		}
	}
	os.Stdout.WriteString("done\n")
	return 0
}

// A program a step starts that waits for all its children before it exits
// finishes as it does outside the harness, in both modes: in a warm
// process the guardian of the program's group is no child of the program,
// so the program does not wait for it (WR-55; review round 4, finding 2).
func TestBunWaitAllChild(t *testing.T) {
	// spec: WR-55
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := childArgs(t, map[string]any{"bin": bin})
	check := func(t *testing.T, out engine.StepOutcome, err error) {
		t.Helper()
		if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != `{"out":"done","code":0}` {
			t.Fatalf("got %s %s, %v", out.Kind, out.Body.Producer(), err)
		}
	}
	t.Run("fresh", func(t *testing.T) {
		pool := newPool(t, PoolConfig{Key: Key{ModuleState: engine.ModuleStateFresh}, Process: childrenConfig(t)})
		out, err := execute(t, pool, freshInvocation("spawn-waitall", args, newRecorder()), 10*time.Second)
		check(t, out, err)
	})
	t.Run("warm", func(t *testing.T) {
		p := spawn(t, childrenConfig(t))
		out, err := execute(t, p, newInvocation("spawn-waitall", args, newRecorder()), 10*time.Second)
		check(t, out, err)
	})
}

// WR-55/a: a fresh process is its job's process and leads its group, so a
// background child the step started in it holds the job open: the outcome
// comes only once the child has exited, settled completed. A child that
// started a session of its own is not waited for.
func TestBunFreshChildHoldsTheJob(t *testing.T) {
	// spec: WR-55/a
	pool := newPool(t, PoolConfig{Key: Key{ModuleState: engine.ModuleStateFresh}, Process: childrenConfig(t)})
	t.Run("in the group", func(t *testing.T) {
		rec := newRecorder()
		start := time.Now()
		out, err := execute(t, pool, freshInvocation("spawn-child", childArgs(t, map[string]any{"seconds": 1, "detached": false}), rec), 20*time.Second)
		took := time.Since(start)
		if err != nil || out.Kind != engine.OutcomeOK {
			t.Fatalf("got %+v, %v", out, err)
		}
		pid := childPID(t, out.Body.Producer())
		if took < 900*time.Millisecond {
			t.Fatalf("the outcome came after %s, before the child's 1 s sleep ended", took)
		}
		if !gone(pid) {
			t.Fatalf("the child %d still runs after the outcome", pid)
		}
		if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleCompleted {
			t.Fatalf("settled %s; calls %s", k, rec.log())
		}
		if got := rec.log(); got != "warning left_behind; settled completed" {
			t.Fatalf("reporter calls %q", got)
		}
		if w := rec.warnings[0]; w.Left["child"] != 1 || len(w.Left) != 1 {
			t.Fatalf("left %v, want one child", w.Left)
		}
	})
	t.Run("detached", func(t *testing.T) {
		rec := newRecorder()
		start := time.Now()
		out, err := execute(t, pool, freshInvocation("spawn-child", childArgs(t, map[string]any{"seconds": 30, "detached": true}), rec), 20*time.Second)
		took := time.Since(start)
		if err != nil || out.Kind != engine.OutcomeOK {
			t.Fatalf("got %+v, %v", out, err)
		}
		pid := childPID(t, out.Body.Producer())
		leftover(t, pid)
		if took > 10*time.Second || gone(pid) {
			t.Fatalf("the outcome came after %s; the detached child is gone: %v", took, gone(pid))
		}
		rec.waitSettled(t, 5*time.Second)
		if w := rec.warnings[0]; w.Left["detached"] != 1 || len(w.Left) != 1 {
			t.Fatalf("left %v, want one detached child", w.Left)
		}
	})
}

// WR-55/b: a timeout while the job waits for its group kills the whole
// group, and the invocation ends with the timeout although the script had
// returned its result.
func TestBunFreshTimeoutKillsTheGroup(t *testing.T) {
	// spec: WR-55/b
	pool := newPool(t, PoolConfig{Key: Key{ModuleState: engine.ModuleStateFresh}, Process: childrenConfig(t)})
	pidFile := filepath.Join(t.TempDir(), "pid")
	rec := newRecorder()
	ctx, cancel := context.WithTimeoutCause(context.Background(), 2*time.Second, engine.CancelTimeout)
	defer cancel()
	start := time.Now()
	_, err := pool.Execute(ctx, freshInvocation("spawn-child", childArgs(t, map[string]any{"seconds": 30, "detached": false, "pidFile": pidFile}), rec))
	if !errors.Is(err, engine.CancelTimeout) {
		t.Fatalf("got %v, want the timeout", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("the timeout took %s", took)
	}
	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("the step did not run: %v", rerr)
	}
	pid := childPID(t, b)
	leftover(t, pid)
	waitFor(t, "the group's child to be killed", func() bool { return gone(pid) })
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleProcessExited {
		t.Fatalf("settled %s; calls %s", k, rec.log())
	}
	if strings.Contains(rec.log(), "late") {
		t.Fatalf("a fresh invocation reported late output: %s", rec.log())
	}
}

// WR-55 in a warm process: a child the step started leads a group of its
// own, standing for the step's. A normal return settles at once; the
// child is reported left behind and killed with its group, so nothing of
// the step runs after it settles. A child the step started detached is
// reported and left running.
func TestBunWarmChildren(t *testing.T) {
	// spec: WR-55
	p := spawn(t, childrenConfig(t))
	t.Run("in the group", func(t *testing.T) {
		rec := newRecorder()
		start := time.Now()
		out, err := execute(t, p, newInvocation("spawn-child", childArgs(t, map[string]any{"seconds": 30, "detached": false}), rec), 10*time.Second)
		if err != nil || out.Kind != engine.OutcomeOK {
			t.Fatalf("got %+v, %v", out, err)
		}
		pid := childPID(t, out.Body.Producer())
		leftover(t, pid)
		if took := time.Since(start); took > 5*time.Second {
			t.Fatalf("the outcome took %s", took)
		}
		rec.waitSettled(t, 5*time.Second)
		if got := rec.log(); got != "warning left_behind; settled completed" {
			t.Fatalf("reporter calls %q", got)
		}
		if w := rec.warnings[0]; w.Left["child"] != 1 || len(w.Left) != 1 {
			t.Fatalf("left %v, want one child", w.Left)
		}
		waitFor(t, "the child to be killed at settlement", func() bool { return gone(pid) })
	})
	t.Run("detached", func(t *testing.T) {
		rec := newRecorder()
		out, err := execute(t, p, newInvocation("spawn-child", childArgs(t, map[string]any{"seconds": 30, "detached": true}), rec), 10*time.Second)
		if err != nil || out.Kind != engine.OutcomeOK {
			t.Fatalf("got %+v, %v", out, err)
		}
		pid := childPID(t, out.Body.Producer())
		leftover(t, pid)
		rec.waitSettled(t, 5*time.Second)
		if w := rec.warnings[0]; w.Left["detached"] != 1 || len(w.Left) != 1 {
			t.Fatalf("left %v, want one detached child", w.Left)
		}
		time.Sleep(200 * time.Millisecond)
		if gone(pid) {
			t.Fatal("the detached child was killed")
		}
	})
}

// WR-55, WR-37 in a warm process: the engine's timeout cancels the
// invocation, and the cancel kills the step's children at once, although
// the call itself goes on, abandoned (D9).
func TestBunWarmCancelKillsChildren(t *testing.T) {
	// spec: WR-55, WR-37
	p := spawn(t, childrenConfig(t))
	pidFile := filepath.Join(t.TempDir(), "pid")
	rec := newRecorder()
	inv := newInvocation("spawn-hold", childArgs(t, map[string]any{"seconds": 30, "pidFile": pidFile}), rec)
	var pid int
	waitPID := func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil || len(b) == 0 {
			return false
		}
		pid = childPID(t, b)
		return true
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for !waitPID() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		cancel(engine.CancelTimeout)
	}()
	if _, err := p.Execute(ctx, inv); !errors.Is(err, engine.CancelTimeout) {
		t.Fatalf("got %v", err)
	}
	if pid == 0 && !waitPID() {
		t.Fatal("the step did not start its child")
	}
	leftover(t, pid)
	waitFor(t, "the child to be killed by the cancel", func() bool { return gone(pid) })
	if in, ab := p.Load(); in != 0 || ab != 1 {
		t.Fatalf("load %d in flight, %d abandoned", in, ab)
	}
	if calls := rec.log(); slices.Contains(strings.Split(calls, "; "), "settled completed") {
		t.Fatalf("the abandoned call settled: %s", calls)
	}
}

// ended reports whether pid has exited: it is gone, or a zombie its parent
// has not reaped, as a child of a harness whose event loop is blocked
// stays. It reads the process's state as the engine does (procInfoOf:
// /proc on Linux, kern.proc.pid on macOS), so it needs no ps, which a slim
// Linux image lacks; elsewhere, where the engine reads no process, it
// falls back to ps.
func ended(pid int) bool {
	if gone(pid) {
		return true
	}
	if info, ok := procInfoOf(pid); ok {
		return info.zombie
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		return gone(pid) // it exited and was reaped meanwhile
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// ended tells a zombie from a running process without ps: here with no
// PATH to find one (FU7; a slim Linux image has none).
func TestEnded(t *testing.T) {
	t.Setenv("PATH", "")
	live := exec.Command("/bin/sleep", "5")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { live.Process.Kill(); live.Wait() })
	dead := exec.Command("/bin/sh", "-c", "exit 0")
	if err := dead.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dead.Wait() })
	// Not waited for, it stays a zombie of this process.
	waitFor(t, "the exited child to be a zombie", func() bool { return ended(dead.Process.Pid) })
	if ended(live.Process.Pid) {
		t.Fatal("a running process was taken for ended")
	}
}

// readPID waits for the pid a step writes to pidFile.
func readPID(t *testing.T, pidFile string) int {
	t.Helper()
	var pid int
	waitFor(t, "the step to start its child", func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil || len(strings.TrimSpace(string(b))) == 0 {
			return false
		}
		pid = childPID(t, b)
		return true
	})
	leftover(t, pid)
	return pid
}

// WR-55, WR-37 in a warm process whose event loop the step blocks: the
// harness cannot handle the cancel, so the engine kills the child groups
// the step announced (child frames) itself, at once on the invocation's
// timeout, and when the watchdog ends the process every group still
// announced, so no child of the step outlives the process. A synchronous
// child call's child stays in the harness's group, which the engine
// spawned as its leader and kills with the process (review round 1,
// finding 1).
func TestBunWarmStalledChildren(t *testing.T) {
	// spec: WR-55, WR-37, D2
	t.Run("the timeout kills the group though the loop is blocked", func(t *testing.T) {
		cfg := childrenConfig(t)
		cfg.KillAfter = WatchdogOff
		p := spawn(t, cfg)
		pidFile := filepath.Join(t.TempDir(), "pid")
		rec := newRecorder()
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		done := make(chan error, 1)
		go func() {
			_, err := p.Execute(ctx, newInvocation("stall", childArgs(t, map[string]any{"seconds": 30, "pidFile": pidFile, "sync": false}), rec))
			done <- err
		}()
		pid := readPID(t, pidFile)
		cancel(engine.CancelTimeout)
		if err := <-done; !errors.Is(err, engine.CancelTimeout) {
			t.Fatalf("got %v, want the timeout", err)
		}
		waitFor(t, "the engine to kill the stalled step's child", func() bool { return ended(pid) })
		select {
		case <-p.Done():
			t.Fatal("the process ended: only the child's group was to be killed")
		default:
		}
	})
	t.Run("the watchdog's kill ends the groups with the process", func(t *testing.T) {
		cfg := childrenConfig(t)
		cfg.KillAfter = time.Second
		p := spawn(t, cfg)
		pidFile := filepath.Join(t.TempDir(), "pid")
		rec := newRecorder()
		done := make(chan engine.StepOutcome, 1)
		go func() {
			out, _ := p.Execute(context.Background(), newInvocation("stall", childArgs(t, map[string]any{"seconds": 30, "pidFile": pidFile, "sync": false}), rec))
			done <- out
		}()
		pid := readPID(t, pidFile)
		select {
		case <-p.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the watchdog did not end the stalled process")
		}
		if out := <-done; out.Kind != engine.OutcomeEscaped || out.Signal != "SIGKILL" {
			t.Fatalf("got %+v, want the process's kill", out)
		}
		waitFor(t, "the child to end with the process", func() bool { return gone(pid) })
		if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleProcessExited {
			t.Fatalf("settled %s", k)
		}
	})
	t.Run("a synchronous child ends with the process", func(t *testing.T) {
		cfg := childrenConfig(t)
		cfg.KillAfter = time.Second
		p := spawn(t, cfg)
		pidFile := filepath.Join(t.TempDir(), "pid")
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		go p.Execute(ctx, newInvocation("stall", childArgs(t, map[string]any{"seconds": 30, "pidFile": pidFile, "sync": true}), newRecorder()))
		pid := readPID(t, pidFile)
		cancel(engine.CancelTimeout)
		select {
		case <-p.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the watchdog did not end the process the synchronous call blocks")
		}
		waitFor(t, "the synchronous child to end with the process", func() bool { return gone(pid) })
	})
}

// WR-55: a fresh job's result, held while its group runs, is released only
// when the group empties by itself. A kill meanwhile that is not the
// invocation's own timeout or cancellation (Kill, a pool's close at its
// deadline) kills the group and fails the job, settled with the process,
// though its script returned (review round 1, finding 2).
func TestBunFreshKilledHeldResult(t *testing.T) {
	// spec: WR-55/b, WR-37
	cfg := childrenConfig(t)
	cfg.ModuleState = engine.ModuleStateFresh
	p := spawn(t, cfg)
	pidFile := filepath.Join(t.TempDir(), "pid")
	rec := newRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan engine.StepOutcome, 1)
	go func() {
		out, err := p.Execute(ctx, freshInvocation("spawn-child", childArgs(t, map[string]any{"seconds": 30, "detached": false, "pidFile": pidFile}), rec))
		if err != nil {
			t.Errorf("Execute: %v", err)
		}
		done <- out
	}()
	pid := readPID(t, pidFile)
	waitFor(t, "the result to be held for the group", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.holding
	})
	p.Kill()
	select {
	case out := <-done:
		if out.Kind != engine.OutcomeEscaped || out.Signal != "SIGKILL" || out.ExitCode != -1 {
			t.Fatalf("got %+v, want the group's kill, not the held result", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the killed job's outcome never came")
	}
	waitFor(t, "the group's child to be killed", func() bool { return gone(pid) })
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleProcessExited {
		t.Fatalf("settled %s; calls %s", k, rec.log())
	}
}

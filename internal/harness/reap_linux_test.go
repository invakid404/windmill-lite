package harness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// The engine reaps what it adopts as a subreaper, as it does as PID 1
// without an init, which adopts orphans alike: the guardians of the child
// groups it releases, kills on a cancellation and kills at the process's
// end, what a group's members leave, a descendant that exits while its
// group still runs, and a fresh job's orphans; repeated spawns leave no
// zombie of its (review round 5, finding 1). The test runs in a subprocess
// of this test binary, which makes itself a child subreaper (prctl
// PR_SET_CHILD_SUBREAPER) and runs the real Bun harness.
func TestReapAdoptedOrphans(t *testing.T) {
	// spec: WR-55
	if os.Getenv("WL_SUBREAPER") != "" {
		t.Skip("the subprocess runs TestReapAdoptedOrphansAsSubreaper")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReapAdoptedOrphansAsSubreaper$", "-test.v", "-test.count=1", "-test.timeout=300s")
	cmd.Env = append(os.Environ(), "WL_SUBREAPER=1")
	out, err := cmd.CombinedOutput()
	if bytes.Contains(out, []byte("--- SKIP: TestReapAdoptedOrphansAsSubreaper")) {
		t.Skipf("the subprocess skipped:\n%s", out)
	}
	if err != nil || !bytes.Contains(out, []byte("--- PASS: TestReapAdoptedOrphansAsSubreaper")) {
		t.Fatalf("the subreaper run failed: %v\n%s", err, out)
	}
}

// TestReapAdoptedOrphansAsSubreaper is TestReapAdoptedOrphans's
// subprocess.
func TestReapAdoptedOrphansAsSubreaper(t *testing.T) {
	// spec: WR-55
	if os.Getenv("WL_SUBREAPER") == "" {
		t.Skip("run by TestReapAdoptedOrphans, in a subprocess")
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Skipf("prctl(PR_SET_CHILD_SUBREAPER): %v", err)
	}
	traceOps(t)
	noZombies := func(t *testing.T) {
		t.Helper()
		waitFor(t, "no zombie child of the engine's", func() bool { return len(zombieChildren()) == 0 })
	}
	reaped := func(t *testing.T, what string, pids ...int) {
		t.Helper()
		for _, pid := range pids {
			waitFor(t, what, func() bool {
				_, ok := statOf(pid)
				return !ok
			})
		}
	}
	t.Run("repeated short spawns", func(t *testing.T) {
		diagnose(t)
		p := spawn(t, subreaperConfig(t))
		dir := t.TempDir()
		for i := range 10 {
			pidFile := filepath.Join(dir, strconv.Itoa(i))
			out, err := execute(t, p, newInvocation("spawn-daemon", childArgs(t, map[string]any{"seconds": 0.05, "pidFile": pidFile}), newRecorder()), 10*time.Second)
			if err != nil || out.Kind != engine.OutcomeOK {
				t.Fatalf("spawn %d: got %+v, %v", i, out, err)
			}
		}
		noZombies(t)
	})
	t.Run("a descendant that exits while its group runs", func(t *testing.T) {
		diagnose(t)
		p := spawn(t, subreaperConfig(t))
		pidFile := filepath.Join(t.TempDir(), "pid")
		script := fmt.Sprintf("(sleep 0.3 & echo $! > '%s'); sleep 3", pidFile)
		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := p.Execute(ctx, newInvocation("spawn-shell", childArgs(t, map[string]any{"script": script}), newRecorder()))
			if err == nil && out.Kind != engine.OutcomeOK {
				err = fmt.Errorf("got %+v", out)
			}
			done <- err
		}()
		pid := readPID(t, pidFile)
		waitFor(t, "the engine to adopt the descendant", func() bool {
			info, ok := statOf(pid)
			return ok && info.ppid == os.Getpid()
		})
		guards := adoptedGuardians(t, 1)
		reaped(t, "the descendant reaped", pid)
		select {
		case err := <-done:
			t.Fatalf("the step ended before the descendant was reaped: %v", err)
		default:
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		reaped(t, "the guardian reaped", guards...)
		noZombies(t)
	})
	t.Run("cancel", func(t *testing.T) {
		diagnose(t)
		cfg := subreaperConfig(t)
		cfg.KillAfter = WatchdogOff
		p := spawn(t, cfg)
		pidFile := filepath.Join(t.TempDir(), "pid")
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		done := make(chan error, 1)
		go func() {
			_, err := p.Execute(ctx, newInvocation("stall", childArgs(t, map[string]any{"seconds": 30, "pidFile": pidFile, "sync": false}), newRecorder()))
			done <- err
		}()
		pid := readPID(t, pidFile)
		guards := adoptedGuardians(t, 1)
		cancel(engine.CancelTimeout)
		<-done
		waitFor(t, "the engine to kill the stalled step's child", func() bool {
			info, ok := statOf(pid)
			return !ok || info.zombie
		})
		reaped(t, "the guardian reaped", guards...)
		noZombies(t)
	})
	t.Run("shutdown", func(t *testing.T) {
		diagnose(t)
		for _, sync := range []bool{false, true} {
			cfg := subreaperConfig(t)
			cfg.KillAfter = WatchdogOff
			p := spawn(t, cfg)
			pidFile := filepath.Join(t.TempDir(), "pid")
			go p.Execute(context.Background(), newInvocation("stall", childArgs(t, map[string]any{"seconds": 30, "pidFile": pidFile, "sync": sync}), newRecorder()))
			pid := readPID(t, pidFile)
			// A synchronous call's child gets no group, so no guardian.
			guards := adoptedGuardians(t, map[bool]int{false: 1, true: 0}[sync])
			p.Kill()
			<-p.Done()
			// The child was the harness's: the engine adopted it with the
			// harness's end, killed it with its group and reaped it.
			reaped(t, "the child reaped", pid)
			reaped(t, "the guardian reaped", guards...)
		}
		noZombies(t)
	})
	t.Run("a fresh job's orphan", func(t *testing.T) {
		diagnose(t)
		pool := newPool(t, PoolConfig{Key: Key{ModuleState: engine.ModuleStateFresh}, Process: subreaperConfig(t)})
		start := time.Now()
		out, err := execute(t, pool, freshInvocation("spawn-child", childArgs(t, map[string]any{"seconds": 0.5, "detached": false}), newRecorder()), 10*time.Second)
		if err != nil || out.Kind != engine.OutcomeOK {
			t.Fatalf("got %+v, %v", out, err)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Fatalf("the outcome came after %s: the orphan was not reaped when it exited", took)
		}
		noZombies(t)
	})
}

// opsTrace records the engine's process operations in this subprocess, so
// that a failure shows what the engine saw and did.
var opsTrace struct {
	sync.Mutex
	lines []string
	start time.Time
}

func tracef(format string, args ...any) {
	opsTrace.Lock()
	defer opsTrace.Unlock()
	opsTrace.lines = append(opsTrace.lines, fmt.Sprintf("%6dms ", time.Since(opsTrace.start).Milliseconds())+fmt.Sprintf(format, args...))
}

// traceOps wraps the process operations with tracef, for the rest of this
// subprocess, which runs this test alone, before any process starts.
func traceOps(t *testing.T) {
	opsTrace.start = time.Now()
	gm, ig, au, sg, rp := groupMembers, isGuard, argsUnread, signalGroup, reapPID
	groupMembers = func(pgids []int) map[int][]groupMember {
		out := gm(pgids)
		tracef("members %v: %v", pgids, out)
		return out
	}
	isGuard = func(pid int, token string, script bool) bool {
		ok := ig(pid, token, script)
		b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		tracef("isGuard %d %s script=%v: %v (%q)", pid, token, script, ok, b)
		return ok
	}
	argsUnread = func(pid int) bool {
		ok := au(pid)
		tracef("argsUnread %d: %v", pid, ok)
		return ok
	}
	signalGroup = func(pgid int) {
		tracef("signal %d", pgid)
		sg(pgid)
	}
	reapPID = func(pid int) bool {
		ok := rp(pid)
		tracef("reap %d: %v", pid, ok)
		return ok
	}
	t.Cleanup(func() {
		if t.Failed() {
			opsTrace.Lock()
			defer opsTrace.Unlock()
			lines := opsTrace.lines
			if len(lines) > 400 {
				lines = lines[:400]
			}
			t.Logf("engine operations:\n%s", strings.Join(lines, "\n"))
		}
	})
}

// subreaperConfig is childrenConfig, with the harness's diagnostics logged
// when t fails.
func subreaperConfig(t *testing.T) ProcessConfig {
	t.Helper()
	cfg := childrenConfig(t)
	diag := &syncBuffer{}
	cfg.Diagnostics = diag
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("harness diagnostics:\n%s", diag.String())
		}
	})
	return cfg
}

// diagnose logs, when t fails, the processes the test is about: every
// descendant of this process and every guardian, with its parent, group
// and state, so a failure shows who holds what.
func diagnose(t *testing.T) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ents, _ := os.ReadDir("/proc")
		infos := map[int]procInfo{}
		for _, e := range ents {
			if pid, err := strconv.Atoi(e.Name()); err == nil {
				if info, ok := statOf(pid); ok {
					infos[pid] = info
				}
			}
		}
		descends := func(pid int) bool {
			for range 64 {
				info, ok := infos[pid]
				if !ok || info.ppid <= 1 {
					return false
				}
				if info.ppid == os.Getpid() {
					return true
				}
				pid = info.ppid
			}
			return false
		}
		t.Logf("this process: %d", os.Getpid())
		for pid, info := range infos {
			b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
			cmd := string(bytes.ReplaceAll(bytes.TrimRight(b, "\x00"), []byte{0}, []byte{' '}))
			if descends(pid) || bytes.Contains(b, []byte(guardArg(""))) {
				t.Logf("pid %d ppid %d pgid %d zombie %v: %s", pid, info.ppid, info.pgid, info.zombie, cmd)
			}
		}
	})
}

// zombieChildren lists this process's children that have exited and not
// been reaped.
func zombieChildren() []int {
	var out []int
	for _, pid := range children() {
		if info, ok := statOf(pid); ok && info.zombie {
			out = append(out, pid)
		}
	}
	return out
}

// adoptedGuardians waits until n guardians are this process's children,
// once the prologue's subshell that started them has exited, and returns
// them.
func adoptedGuardians(t *testing.T, n int) []int {
	t.Helper()
	var guards []int
	waitFor(t, fmt.Sprintf("%d adopted guardians", n), func() bool {
		guards = guardianChildren()
		return len(guards) == n
	})
	return guards
}

// guardianChildren lists this process's children that are child groups'
// guardians (guardArg).
func guardianChildren() []int {
	var out []int
	for _, pid := range children() {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err == nil && bytes.HasPrefix(b, []byte("sleep\x00"+guardArg(""))) {
			out = append(out, pid)
		}
	}
	return out
}

// children lists this process's children.
func children() []int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if info, ok := statOf(pid); ok && info.ppid == os.Getpid() {
			out = append(out, pid)
		}
	}
	return out
}

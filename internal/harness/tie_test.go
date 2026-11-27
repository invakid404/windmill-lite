package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// tieSkip is why the tests of the engine-death tie run only on Linux: only
// Linux gives a process a signal when its parent dies, and elsewhere, as on
// macOS, a harness process outlives an engine killed while its event loop
// is stuck (ADR 0004 §Lifecycle). CI's Linux job runs them.
const tieSkip = "the engine-death tie is Linux's parent-death signal; on this system a harness process outlives a killed engine, as documented"

// tieEngine is this test binary as an engine (WL_TIE_ENGINE=<dir>): it
// spawns the real harness on the fixture bundle with the Bun that WL_BUN
// names, starts spin-gate there, whose step blocks the harness's event loop
// until dir/release exists, writes the harness's pid to dir/pid once the
// step has started, and then waits to be killed.
func tieEngine(dir string) int {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return 2
	}
	bundle := filepath.Join(root, "conformance", "protocol", "bundle")
	manifest, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		return 3
	}
	sum := sha256.Sum256(manifest)
	path, args := BunCommand(os.Getenv("WL_BUN"), filepath.Join(root, "harnesses", "bun", "runtime", "main.ts"), bundle)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := Spawn(ctx, ProcessConfig{
		Path:        path,
		Args:        args,
		WorkRoot:    dir,
		Kind:        "bun",
		Runtime:     PinnedBun,
		Bundle:      "sha256:" + hex.EncodeToString(sum[:]),
		Diagnostics: io.Discard,
	})
	if err != nil {
		return 4
	}
	g := gate{started: filepath.Join(dir, "started"), release: filepath.Join(dir, "release")}
	go p.Execute(context.Background(), g.spinInvocation(newRecorder()))
	for {
		if _, err := os.Stat(g.started); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(p.PID())), 0o644); err != nil {
		return 5
	}
	select {}
}

// TestHarnessEndsWithEngine checks the engine-death tie on Linux: an
// engine, this test binary run as one (tieEngine), spawns a harness whose
// step blocks its event loop, and gets SIGKILL. The harness, which would
// otherwise run on, is gone within 20 s: killed, or a zombie that waits to
// be reaped.
func TestHarnessEndsWithEngine(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip(tieSkip)
	}
	bunPath(t)
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	var pid int
	// No signal by pid cleans up: once the harness is gone, its pid can be
	// another process's. The release ends the step even when the tie
	// failed, and a harness whose engine is gone then exits on its own.
	// The step sees the release only while the file exists, so this
	// cleanup, which runs before t.TempDir's, waits for the harness to
	// end before the folder goes. Should its pid be another process's by
	// then, it only waits out the bound.
	t.Cleanup(func() {
		os.WriteFile(release, nil, 0o600)
		if pid == 0 {
			return
		}
		for deadline := time.Now().Add(30 * time.Second); !processGone(pid); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Errorf("the harness %d still runs 30 s after its step's release", pid)
				return
			}
		}
	})
	engineCmd := exec.Command(os.Args[0])
	engineCmd.Env = append(os.Environ(), "WL_TIE_ENGINE="+dir)
	if err := engineCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engineCmd.Process.Kill(); engineCmd.Wait() })
	pidFile := filepath.Join(dir, "pid")
	for deadline := time.Now().Add(30 * time.Second); pid == 0; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(string(b))
		}
		if pid == 0 && time.Now().After(deadline) {
			t.Fatal("the engine did not start a harness with a blocked event loop within 30 s")
		}
	}
	if processGone(pid) {
		t.Fatalf("the harness %d is gone while its engine runs", pid)
	}
	if err := engineCmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	engineCmd.Wait()
	for deadline := time.Now().Add(20 * time.Second); !processGone(pid); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the harness %d, its event loop blocked, still runs 20 s after its engine got SIGKILL", pid)
		}
	}
}

// TestHarnessTieNoEarlyKill checks that the tie cannot end a healthy
// harness while its engine lives: a goroutine that locks its thread spawns
// a harness and returns without unlocking it, so the Go runtime ends that
// thread. Once the thread is gone, the harness still runs a step. A
// parent-death signal tied to the spawning thread would have killed it.
func TestHarnessTieNoEarlyKill(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip(tieSkip)
	}
	cfg, _ := bunConfig(t)
	spawned := make(chan *Process, 1)
	threads := make(chan int, 1)
	go func() {
		runtime.LockOSThread()
		threads <- currentThread()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		p, err := Spawn(ctx, cfg)
		if err != nil {
			t.Errorf("Spawn: %v", err)
		}
		spawned <- p
		// Returns locked: the runtime ends this thread.
	}()
	tid := <-threads
	p := <-spawned
	if p == nil {
		t.FailNow()
	}
	t.Cleanup(func() { p.Kill(); <-p.Done() })
	for deadline := time.Now().Add(20 * time.Second); !threadGone(tid); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the spawning goroutine's thread %d did not end within 20 s", tid)
		}
	}
	out, err := execute(t, p, newInvocation("echo", `{"value":"after the thread"}`, newRecorder()), 10*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("a step after the spawning thread ended: %+v, %v (exit %s)", out, err, p.ExitStatus())
	}
	var got string
	if json.Unmarshal(out.Body.Producer(), &got) != nil || got != "after the thread" {
		t.Errorf("echo returned %s, want its argument", out.Body.Producer())
	}
	if processGone(p.PID()) {
		t.Errorf("the harness %d is gone while its engine lives", p.PID())
	}
}

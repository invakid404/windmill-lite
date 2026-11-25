//go:build unix

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// These tests run wl/plugin's Supervisor on the real engine: this
// package's test binary, which the Supervisor launches through
// wl/plugin's ClientConfig as the plugin tests do, with bundles of their
// synthetic folders. The Supervisor's handling of faults that the engine
// does not make, such as an unanswered shutdown call, is tested on a fake
// engine in wl/plugin.

// identityOf launches the engine with the configuration cfg and returns
// its build identity as a host's release record pins it. The launch ends
// before identityOf returns.
func identityOf(t *testing.T, cfg map[string]any) plugin.PinnedIdentity {
	t.Helper()
	e := launchPlugin(t, cfg)
	defer e.client.Kill()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	info, err := e.runner.BuildInfo(ctx)
	if err != nil {
		t.Fatalf("BuildInfo: %v", err)
	}
	return plugin.PinnedIdentity{
		BundleDigest:  info.GetBundleDigest(),
		EngineVersion: info.GetEngineVersion(),
		BunVersion:    info.GetBunVersion(),
		ConfigDigest:  info.GetConfigDigest(),
		Labels:        info.GetLabels(),
	}
}

// engineSupervisor is a Supervisor of the engine that a test runs.
type engineSupervisor struct {
	sup *plugin.Supervisor
	// pidFile is where each generation's engine writes its process id
	// when it starts (pidEnv).
	pidFile string
	// log holds the Supervisor's log and the engines' standard error.
	log  *lineLog
	stop context.CancelFunc
	done chan error
}

// superviseEngine runs a Supervisor of the engine with the configuration
// cfg, which pins id, until the test ends.
func superviseEngine(t *testing.T, cfg map[string]any, id plugin.PinnedIdentity) *engineSupervisor {
	t.Helper()
	exe, sum := engineExecutable(t)
	s := &engineSupervisor{pidFile: filepath.Join(t.TempDir(), "pid"), log: &lineLog{}, done: make(chan error, 1)}
	sup, err := plugin.NewSupervisor(plugin.SupervisorConfig{
		Launch: plugin.Launch{
			Path: exe, SHA256: sum, Config: writePluginConfig(t, cfg), Env: engineEnv(pidEnv + "=" + s.pidFile),
			StartTimeout: 2 * time.Minute,
			Logger:       hclog.New(&hclog.LoggerOptions{Output: s.log, Level: hclog.Debug}),
		},
		Identity: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.sup = sup
	ctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	go func() { s.done <- sup.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-s.done:
			if err != nil {
				t.Errorf("Run = %v, want nil", err)
			}
		case <-time.After(3 * time.Minute):
			t.Errorf("Run did not return within 3 minutes of its context's end\nits log:\n%s", s.log)
		}
	})
	return s
}

// waitNotAdmitted calls through the Supervisor until a call's error says
// that a launch was refused at its admission, and checks that the error
// has want in it. No generation may serve meanwhile.
func (s *engineSupervisor) waitNotAdmitted(t *testing.T, want string) {
	t.Helper()
	for until := time.Now().Add(2 * time.Minute); ; {
		if s.sup.Ready() {
			t.Fatalf("a generation serves, want each launch refused\nthe log:\n%s", s.log)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := s.sup.Call(ctx, func(*plugin.Client) error { return errors.New("the call ran") })
		cancel()
		if errors.Is(err, plugin.ErrNotAdmitted) {
			if !errors.Is(err, plugin.ErrNotServing) || !strings.Contains(err.Error(), want) {
				t.Fatalf("Call = %v, want ErrNotServing with the refusal %q", err, want)
			}
			return
		}
		if !errors.Is(err, plugin.ErrNotServing) {
			t.Fatalf("Call = %v, want ErrNotServing\nthe log:\n%s", err, s.log)
		}
		if time.Now().After(until) {
			t.Fatalf("no launch was refused within 2 minutes; the last call: %v\nthe log:\n%s", err, s.log)
		}
	}
}

// echo runs the flow f/plugin/echo with x through the Supervisor, within 2
// minutes, fails the test unless it succeeds, and returns the Client that
// ran it.
func (s *engineSupervisor) echo(t *testing.T, x string) *plugin.Client {
	t.Helper()
	used, err := s.callEcho(x)
	if err != nil {
		t.Fatalf("a call through the Supervisor: %v\nthe log:\n%s", err, s.log)
	}
	return used
}

// callEcho runs the flow f/plugin/echo with x through the Supervisor,
// within 2 minutes, and returns the Client that ran it and the call's
// error.
func (s *engineSupervisor) callEcho(x string) (*plugin.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var used *plugin.Client
	err := s.sup.Call(ctx, func(c *plugin.Client) error {
		used = c
		res, err := c.Run(ctx, &plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(fmt.Sprintf(`{"x":%q}`, x))})
		if err != nil {
			return err
		}
		if res.GetStatus() != "success" || string(res.GetResult()) != strconv.Quote(x) {
			return fmt.Errorf("the run ended %s with %s, want success with %q", res.GetStatus(), res.GetResult(), x)
		}
		return nil
	})
	return used, err
}

// pid returns the process id that the last generation's engine wrote.
func (s *engineSupervisor) pid(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(s.pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// copyBundle makes dir a copy of the bundle from, replacing what dir held.
func copyBundle(t *testing.T, from, dir string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, os.DirFS(from)); err != nil {
		t.Fatal(err)
	}
}

// TestSupervisorAdmissionRefusesWrongBundle pins the build identity of one
// bundle, then puts another bundle at the configured path: the Supervisor
// refuses each launch at its admission, naming the bundle's digest, and no
// generation serves. With the pinned bundle back at that path, a later
// launch is admitted and runs a flow.
func TestSupervisorAdmissionRefusesWrongBundle(t *testing.T) {
	bun, pinned := pluginBundle(t)
	_, other := runOptionBundle(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	copyBundle(t, pinned, dir)
	cfg := pluginConfigOf(t, bun, dir)
	id := identityOf(t, cfg)

	copyBundle(t, other, dir)
	s := superviseEngine(t, cfg, id)
	s.waitNotAdmitted(t, ", want "+id.BundleDigest)

	copyBundle(t, pinned, dir)
	s.echo(t, "admitted")
	if !s.sup.Ready() {
		t.Error("Ready is false after a call ran")
	}
}

// TestSupervisorAdmissionConfigMismatch pins the build identity of one
// configuration, then supervises the engine with a configuration that
// differs only in identity.workspace: the configuration's digest differs,
// so the Supervisor refuses each launch at its admission. The pinned
// configuration is admitted.
func TestSupervisorAdmissionConfigMismatch(t *testing.T) {
	bun, out := pluginBundle(t)
	pinned := pluginConfigOf(t, bun, out)
	pinned["identity"] = map[string]any{"workspace": "ws-pinned"}
	other := pluginConfigOf(t, bun, out)
	other["identity"] = map[string]any{"workspace": "ws-other"}
	id := identityOf(t, pinned)
	data, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)

	s := superviseEngine(t, other, id)
	s.waitNotAdmitted(t, fmt.Sprintf("the engine's configuration has the digest %s, want %s", hex.EncodeToString(sum[:]), id.ConfigDigest))

	superviseEngine(t, pinned, id).echo(t, "pinned")
}

// TestSupervisorWithdrawsOnNotServing checks the Supervisor on the
// engine's drain: SIGTERM makes the engine answer NOT_SERVING, and the
// Supervisor withdraws its generation. A call in flight on it completes,
// and a call made after the withdrawal waits and runs on the next
// generation, whose engine is a new process, once the first one has gone.
func TestSupervisorWithdrawsOnNotServing(t *testing.T) {
	bun, out := pluginBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	s := superviseEngine(t, cfg, identityOf(t, cfg))
	firstClient := s.echo(t, "first")
	first := s.pid(t)

	dir, args := newGate(t)
	type answer struct {
		res *plugin.RunResult
		err error
	}
	inFlight := make(chan answer, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var a answer
		a.err = s.sup.Call(ctx, func(c *plugin.Client) error {
			if c != firstClient {
				return errors.New("the call in flight did not reach the first generation")
			}
			a.res, a.err = c.Run(ctx, &plugin.RunRequest{Path: "f/plugin/gate", Args: args})
			return a.err
		})
		inFlight <- a
	}()
	waitFile(t, dir, "entered")

	if err := syscall.Kill(first, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for until := time.Now().Add(30 * time.Second); s.sup.Ready(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("the generation was not withdrawn within 30 s of SIGTERM\nthe log:\n%s", s.log)
		}
	}

	var mu sync.Mutex
	var next *plugin.Client
	var nextErr error
	nextDone := make(chan struct{})
	go func() {
		defer close(nextDone)
		c, err := s.callEcho("next")
		mu.Lock()
		next, nextErr = c, err
		mu.Unlock()
	}()
	select {
	case <-nextDone:
		t.Fatalf("a call made after the withdrawal ended (%v) while the first generation drains", nextErr)
	case <-time.After(500 * time.Millisecond):
	}

	release(t, dir)
	a := <-inFlight
	if a.err != nil || a.res.GetStatus() != "success" || string(a.res.GetResult()) != `"released"` {
		t.Fatalf("the call in flight = (%v, %v), want success with \"released\"\nthe log:\n%s", a.res, a.err, s.log)
	}
	select {
	case <-nextDone:
	case <-time.After(3 * time.Minute):
		t.Fatalf("the call made after the withdrawal did not end within 3 minutes\nthe log:\n%s", s.log)
	}
	mu.Lock()
	defer mu.Unlock()
	if nextErr != nil {
		t.Fatalf("the call made after the withdrawal: %v\nthe log:\n%s", nextErr, s.log)
	}
	if next == nil || next == firstClient {
		t.Fatal("the call made after the withdrawal did not run on a new generation")
	}
	if second := s.pid(t); second == first {
		t.Errorf("the next generation's engine is the first one's process %d", first)
	}
	if err := syscall.Kill(first, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("the first generation's process %d is still there (kill 0: %v)", first, err)
	}
}

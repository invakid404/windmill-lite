//go:build unix

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// directEngine is an engine that a test starts as go-plugin would and
// calls over its socket, so that it sees what go-plugin's client hides:
// the engine's process and its exit status.
type directEngine struct {
	cmd    *exec.Cmd
	runner *plugin.Client
	health healthpb.HealthClient
	stderr *syncBuffer
	exited chan struct{}
	err    error
}

// startDirect starts the engine with the configuration cfg and env added
// to its environment, reads go-plugin's handshake line, connects to the
// socket that it names, and, unless serve is false, waits up to 2 minutes
// for health SERVING. The engine is killed at the end of the test if it
// has not exited.
func startDirect(t *testing.T, cfg map[string]any, serve bool, env ...string) *directEngine {
	t.Helper()
	exe, _ := engineExecutable(t)
	sockets, err := os.MkdirTemp("", "wld-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d := &directEngine{stderr: &syncBuffer{}, exited: make(chan struct{})}
	d.cmd = exec.Command(exe, "plugin", "-config", writePluginConfig(t, cfg))
	d.cmd.Env = engineEnv(append([]string{plugin.Handshake.MagicCookieKey + "=" + plugin.Handshake.MagicCookieValue, "PLUGIN_UNIX_SOCKET_DIR=" + sockets}, env...)...)
	d.cmd.Stdout, d.cmd.Stderr = w, d.stderr
	err = d.cmd.Start()
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		d.err = d.cmd.Wait()
		close(d.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-d.exited:
		default:
			d.cmd.Process.Kill()
			<-d.exited
		}
	})
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(r).ReadString('\n')
		line <- s
	}()
	var handshake string
	select {
	case handshake = <-line:
	case <-time.After(2 * time.Minute):
		t.Fatalf("no handshake line within 2 minutes\nits standard error:\n%s", d.stderr)
	}
	fields := strings.Split(strings.TrimSpace(handshake), "|")
	if len(fields) < 5 || fields[2] != "unix" {
		t.Fatalf("the handshake line %q, want go-plugin's on a Unix socket\nits standard error:\n%s", handshake, d.stderr)
	}
	conn, err := grpc.NewClient("unix:"+fields[3], grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	raw, err := (&plugin.RunnerPlugin{}).GRPCClient(context.Background(), nil, conn)
	if err != nil {
		t.Fatal(err)
	}
	d.runner = raw.(*plugin.Client)
	d.health = healthpb.NewHealthClient(conn)
	if serve {
		d.waitHealth(t, healthpb.HealthCheckResponse_SERVING)
	}
	return d
}

// waitHealth waits up to 2 minutes for the engine's health to answer want.
func (d *directEngine) waitHealth(t *testing.T, want healthpb.HealthCheckResponse_ServingStatus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for {
		st, err := d.runner.Health(ctx)
		if err == nil && st == want {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("health did not answer %v within 2 minutes (last %v, %v)\nits standard error:\n%s", want, st, err, d.stderr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// exitCode waits up to a minute for the engine to exit and returns its exit
// status.
func (d *directEngine) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case <-d.exited:
	case <-time.After(time.Minute):
		t.Fatalf("the engine did not exit within a minute\nits standard error:\n%s", d.stderr)
	}
	var exit *exec.ExitError
	switch {
	case d.err == nil:
		return 0
	case errors.As(d.err, &exit):
		return exit.ExitCode()
	}
	t.Fatalf("the engine: %v", d.err)
	return -1
}

// signal sends the engine sig.
func (d *directEngine) signal(t *testing.T, sig os.Signal) {
	t.Helper()
	if err := d.cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
}

// runAsync starts req and returns the channel of its outcome.
func (d *directEngine) runAsync(req *plugin.RunRequest) <-chan runOutcome {
	out := make(chan runOutcome, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := d.runner.Run(ctx, req)
		out <- runOutcome{res, err}
	}()
	return out
}

// runOutcome is the outcome of a Run call.
type runOutcome struct {
	res *plugin.RunResult
	err error
}

// watchUntilEnd reads w until its stream ends, at most 2 minutes, and
// returns the statuses it received.
func watchUntilEnd(t *testing.T, w healthpb.Health_WatchClient) []healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	got := make(chan []healthpb.HealthCheckResponse_ServingStatus, 1)
	go func() {
		var sts []healthpb.HealthCheckResponse_ServingStatus
		for {
			r, err := w.Recv()
			if err != nil {
				got <- sts
				return
			}
			sts = append(sts, r.GetStatus())
		}
	}()
	select {
	case sts := <-got:
		return sts
	case <-time.After(2 * time.Minute):
		t.Fatal("the Watch did not end within 2 minutes")
		return nil
	}
}

// TestPluginDrainOnSIGTERM checks a drain on SIGTERM (ADR 0009, health,
// readiness and drain): while a run is in its step, which waits for the
// test's release, SIGTERM makes health answer NOT_SERVING, and a new run
// answers UNAVAILABLE with the RUN_NOT_STARTED detail, naming draining,
// while the first call is still in flight. Released, the first run
// succeeds, and the engine then exits with status 0.
func TestPluginDrainOnSIGTERM(t *testing.T) {
	bun, out := pluginBundle(t)
	d := startDirect(t, pluginConfigOf(t, bun, out), true)
	dir, args := newGate(t)
	first := d.runAsync(&plugin.RunRequest{Path: "f/plugin/gate", Args: args})
	waitFile(t, dir, "entered")

	d.signal(t, syscall.SIGTERM)
	d.waitHealth(t, healthpb.HealthCheckResponse_NOT_SERVING)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := d.runner.Run(ctx, &plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x":1}`)})
	checkRefused(t, "a run while the engine drains", err, codes.Unavailable, "draining")
	select {
	case o := <-first:
		t.Fatalf("the run in its step ended before its release: %v, %v", o.res, o.err)
	default:
	}

	release(t, dir)
	o := <-first
	if o.err != nil || o.res.GetStatus() != "success" {
		t.Errorf("the run in flight when the drain began: %v, %v; want success", o.res, o.err)
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the drained engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
	if !strings.Contains(d.stderr.String(), "draining: SIGTERM") {
		t.Errorf("the engine's standard error does not name the drain's reason:\n%s", d.stderr)
	}
}

// TestPluginDrainTimeout checks a drain's bound: with drain_timeout_ms 100,
// a run whose step waits for a release that never comes is canceled once
// the drain's time is up, by the runtime's close (cause closing), and that
// outcome reaches its caller before the engine exits with status 0.
func TestPluginDrainTimeout(t *testing.T) {
	bun, out := pluginBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	cfg["drain_timeout_ms"] = 100
	d := startDirect(t, cfg, true)
	dir, args := newGate(t)
	held := d.runAsync(&plugin.RunRequest{Path: "f/plugin/gate", Args: args})
	waitFile(t, dir, "entered")

	d.signal(t, syscall.SIGTERM)
	o := <-held
	if o.err != nil || o.res.GetStatus() != "canceled" || o.res.GetCancelCause() != "closing" {
		t.Errorf("the run past the drain's time: %v, %v; want canceled with the cause closing", o.res, o.err)
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the drained engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
}

// TestPluginEngineRecycle checks the engine's memory trigger: with an
// engine_recycle_rss of one byte, the engine drains once two consecutive
// samples of its resident set reach it, naming the trigger on its standard
// error; a Watch ends with NOT_SERVING, and the engine exits with status 0.
func TestPluginEngineRecycle(t *testing.T) {
	bun, out := pluginBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	cfg["engine_recycle_rss"] = 1
	d := startDirect(t, cfg, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	w, err := d.health.Watch(ctx, &healthpb.HealthCheckRequest{Service: plugin.Runner_ServiceDesc.ServiceName})
	if err != nil {
		t.Fatal(err)
	}
	sts := watchUntilEnd(t, w)
	if len(sts) == 0 || sts[len(sts)-1] != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("the Watch received %v, want NOT_SERVING last", sts)
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
	if !strings.Contains(d.stderr.String(), "draining: the engine's resident set") || !strings.Contains(d.stderr.String(), "engine_recycle_rss") {
		t.Errorf("the engine's standard error does not name the memory trigger:\n%s", d.stderr)
	}
}

// TestPluginWatchTransition checks that a Watch opened while the engine
// serves receives SERVING, then NOT_SERVING when SIGTERM makes it drain,
// before its stream ends with the engine.
func TestPluginWatchTransition(t *testing.T) {
	bun, out := pluginBundle(t)
	d := startDirect(t, pluginConfigOf(t, bun, out), true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	w, err := d.health.Watch(ctx, &healthpb.HealthCheckRequest{Service: plugin.Runner_ServiceDesc.ServiceName})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := w.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("the Watch opened while the engine serves: first %v, %v; want SERVING", r.GetStatus(), err)
	}
	d.signal(t, syscall.SIGTERM)
	if sts := watchUntilEnd(t, w); len(sts) != 1 || sts[0] != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("the Watch after SIGTERM received %v, want [NOT_SERVING]", sts)
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
}

// TestPluginReplacementFailure checks the drain when the warm pool cannot
// start a harness process once the engine serves. The engine's Bun is a
// fake that runs the real one until the test creates a marker file, and
// fails after. With the marker created, a run whose step ignores its
// cancellation taints the only process; the next run needs a replacement,
// which fails to start, so the engine drains: a Watch ends with
// NOT_SERVING, its standard error names the failed start, and it exits
// with status 0.
func TestPluginReplacementFailure(t *testing.T) {
	bun, out := pluginBundle(t)
	marker := filepath.Join(t.TempDir(), "fail")
	fake := filepath.Join(t.TempDir(), "bun")
	script := fmt.Sprintf("#!/bin/sh\nif [ -e %s ]; then exit 3; fi\nexec %s \"$@\"\n", strconv.Quote(marker), strconv.Quote(bun))
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := pluginConfigOf(t, fake, out)
	cfg["pool"] = map[string]any{"min": 1, "max": 1, "warm_start": true}
	d := startDirect(t, cfg, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	w, err := d.health.Watch(ctx, &healthpb.HealthCheckRequest{Service: plugin.Runner_ServiceDesc.ServiceName})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := w.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("the Watch opened while the engine serves: first %v, %v; want SERVING", r.GetStatus(), err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// The step of this run ignores its cancellation, so the run's
	// invocation is abandoned and taints the only process.
	dir, args := newGate(t)
	tainting := d.runAsync(&plugin.RunRequest{Id: "taints", Path: "f/plugin/gate", Args: args})
	waitFile(t, dir, "entered")
	if _, err := d.runner.Cancel(ctx, &plugin.CancelRequest{Id: "taints", Reason: "taint the process", Canceler: "plugin-test"}); err != nil {
		t.Fatal(err)
	}
	if o := <-tainting; o.err != nil || o.res.GetStatus() != "canceled" {
		t.Fatalf("the canceled run: %v, %v; want canceled", o.res, o.err)
	}
	// The next run's step needs a replacement process, which fails to start.
	next := <-d.runAsync(&plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x":1}`)})
	if next.err == nil && next.res.GetStatus() == "success" {
		t.Errorf("the run that needed a replacement succeeded: %v", next.res)
	}
	if sts := watchUntilEnd(t, w); len(sts) == 0 || sts[len(sts)-1] != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("the Watch received %v after the failed start, want NOT_SERVING last", sts)
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
	if !strings.Contains(d.stderr.String(), "draining: the warm pool failed to start a harness process") {
		t.Errorf("the engine's standard error does not name the failed start:\n%s", d.stderr)
	}
}

// TestEngineRSSTrigger checks the engine's memory trigger, which trips at
// the second consecutive sample at or over its threshold: samples that
// alternate around it never trip it, and two in a row do.
func TestEngineRSSTrigger(t *testing.T) {
	tr := rssTrigger{threshold: 100}
	for i, v := range []int64{100, 99, 150, 0, 100, 50} {
		if tr.sample(v) {
			t.Fatalf("sample %d (%d) tripped the trigger after one sample at or over 100", i, v)
		}
	}
	if tr.sample(100) || !tr.sample(101) {
		t.Errorf("two consecutive samples at or over 100 did not trip the trigger at the second")
	}
}

// TestEngineRSS checks that currentRSS reads the engine's current resident
// set, not its peak: after the test has touched 256 MiB of an anonymous
// mapping and unmapped it, which takes those pages out of the resident set
// on Linux and macOS, it agrees with ps's resident set of the process
// within a quarter, or 16 MiB.
func TestEngineRSS(t *testing.T) {
	const size = 256 << 20
	m, err := syscall.Mmap(-1, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < size; i += 4096 {
		m[i] = 1
	}
	if err := syscall.Munmap(m); err != nil {
		t.Fatal(err)
	}
	got, err := currentRSS()
	if err != nil {
		t.Fatal(err)
	}
	ps, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	kib, err := strconv.ParseInt(strings.TrimSpace(string(ps)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	want := kib * 1024
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > max(want/4, 16<<20) {
		t.Errorf("currentRSS %d bytes, ps %d bytes: want them within a quarter or 16 MiB", got, want)
	}
}

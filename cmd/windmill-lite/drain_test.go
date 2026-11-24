//go:build unix

package main

import (
	"bufio"
	"context"
	"encoding/json"
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

// waitStderr waits up to a minute for the engine's standard error to hold
// text.
func (d *directEngine) waitStderr(t *testing.T, text string) {
	t.Helper()
	for until := time.Now().Add(time.Minute); !strings.Contains(d.stderr.String(), text); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("no %q on the engine's standard error within a minute\nits standard error:\n%s", text, d.stderr)
		}
	}
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

// TestPluginDrainTimeout checks a drain's bound, with the drain's timer in
// the test's hands (drainTimerEnv): the drain asks for the configured
// drain_timeout_ms, 12345; while the timer has not fired, a run whose step
// waits for a release that never comes is still in flight; once it fires,
// the runtime's close cancels the run (cause closing), its caller receives
// that outcome, and the engine exits with status 0, saying that the
// drain's time is up.
func TestPluginDrainTimeout(t *testing.T) {
	bun, out := pluginBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	cfg["drain_timeout_ms"] = 12345
	timer := t.TempDir()
	d := startDirect(t, cfg, true, drainTimerEnv+"="+timer)
	dir, args := newGate(t)
	held := d.runAsync(&plugin.RunRequest{Path: "f/plugin/gate", Args: args})
	waitFile(t, dir, "entered")

	d.signal(t, syscall.SIGTERM)
	waitFile(t, timer, "duration")
	if got, err := os.ReadFile(filepath.Join(timer, "duration")); err != nil || string(got) != "12.345s" {
		t.Errorf("the drain's timer: %q, %v; want drain_timeout_ms, 12.345s", got, err)
	}
	select {
	case o := <-held:
		t.Fatalf("the run ended before the drain's time was up: %v, %v", o.res, o.err)
	default:
	}
	if err := os.WriteFile(filepath.Join(timer, "expire"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	o := <-held
	if o.err != nil || o.res.GetStatus() != "canceled" || o.res.GetCancelCause() != "closing" {
		t.Errorf("the run past the drain's time: %v, %v; want canceled with the cause closing", o.res, o.err)
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the drained engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
	if !strings.Contains(d.stderr.String(), "the drain's time is up") {
		t.Errorf("the engine's standard error does not say that the drain's time is up:\n%s", d.stderr)
	}
}

// TestPluginEngineRecycle checks the engine's memory trigger, with its real
// resident set read and its samples in the test's hands (rssTicksEnv). The
// engine's open is held until a Watch is open; released, the Watch
// receives SERVING. With a run in its step, two samples of an
// engine_recycle_rss of one byte make the engine drain: the Watch receives
// NOT_SERVING, a new run answers UNAVAILABLE (draining), the run in flight
// succeeds once released, and the engine exits with status 0, naming the
// trigger.
func TestPluginEngineRecycle(t *testing.T) {
	bun, out := pluginBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	cfg["engine_recycle_rss"] = 1
	hold := filepath.Join(t.TempDir(), "release-open")
	ticks := t.TempDir()
	d := startDirect(t, cfg, false, holdOpenEnv+"="+hold, rssTicksEnv+"="+ticks)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	w, err := d.health.Watch(ctx, &healthpb.HealthCheckRequest{Service: plugin.Runner_ServiceDesc.ServiceName})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := w.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("the Watch while the engine starts: %v, %v; want NOT_SERVING", r.GetStatus(), err)
	}
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := w.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("the Watch once the engine serves: %v, %v; want SERVING", r.GetStatus(), err)
	}
	dir, args := newGate(t)
	first := d.runAsync(&plugin.RunRequest{Path: "f/plugin/gate", Args: args})
	waitFile(t, dir, "entered")

	for _, tick := range []string{"tick-1", "tick-2"} {
		if err := os.WriteFile(filepath.Join(ticks, tick), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := w.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("the Watch after two samples over engine_recycle_rss: %v, %v; want NOT_SERVING", r.GetStatus(), err)
	}
	_, err = d.runner.Run(ctx, &plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x":1}`)})
	checkRefused(t, "a run while the memory trigger drains the engine", err, codes.Unavailable, "draining")
	release(t, dir)
	if o := <-first; o.err != nil || o.res.GetStatus() != "success" {
		t.Errorf("the run in flight when the drain began: %v, %v; want success", o.res, o.err)
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

// TestPluginDrainWhileStarting checks SIGTERM while the engine's open is
// held: the engine drains without ever serving. A Watch opened before the
// signal receives NOT_SERVING only, and the engine exits with status 0
// once the open is released.
func TestPluginDrainWhileStarting(t *testing.T) {
	bun, out := pluginBundle(t)
	hold := filepath.Join(t.TempDir(), "release-open")
	d := startDirect(t, pluginConfigOf(t, bun, out), false, holdOpenEnv+"="+hold)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	w, err := d.health.Watch(ctx, &healthpb.HealthCheckRequest{Service: plugin.Runner_ServiceDesc.ServiceName})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := w.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("the Watch while the engine starts: %v, %v; want NOT_SERVING", r.GetStatus(), err)
	}
	d.signal(t, syscall.SIGTERM)
	d.waitStderr(t, "draining: SIGTERM")
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, st := range watchUntilEnd(t, w) {
		if st == healthpb.HealthCheckResponse_SERVING {
			t.Errorf("the engine served after a drain while it started")
		}
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
}

// TestPluginDrainDispatchedRun checks that a drain lets a run that a step
// dispatched go on: the script f/plugin/dispatch starts the flow
// f/plugin/gate and returns, so no Run call is in flight while the gate's
// step waits for its release. SIGTERM drains the engine, which says that
// it waits for the runs that steps dispatched; only then is the step
// released. It ends on its own (it writes left), and only then does the
// drain close the runtime (closeMarkEnv's file is written after left), and
// the engine exits with status 0.
func TestPluginDrainDispatchedRun(t *testing.T) {
	bun, out := pluginBundle(t)
	mark := filepath.Join(t.TempDir(), "closing")
	d := startDirect(t, pluginConfigOf(t, bun, out), true, closeMarkEnv+"="+mark)
	dir := t.TempDir()
	args, err := json.Marshal(map[string]any{"dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	if o := <-d.runAsync(&plugin.RunRequest{Kind: "script", Path: "f/plugin/dispatch", Args: args}); o.err != nil || o.res.GetStatus() != "success" {
		t.Fatalf("the dispatch: %v, %v", o.res, o.err)
	}
	waitFile(t, dir, "entered")

	d.signal(t, syscall.SIGTERM)
	d.waitStderr(t, "the drain waits for the runs that steps dispatched")
	release(t, dir)
	waitFile(t, dir, "left")
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
	left, err := os.Stat(filepath.Join(dir, "left"))
	if err != nil {
		t.Fatal(err)
	}
	closing, err := os.Stat(mark)
	if err != nil {
		t.Fatalf("the drain did not close the runtime: %v\nits standard error:\n%s", err, d.stderr)
	}
	if closing.ModTime().Before(left.ModTime()) {
		t.Errorf("the drain closed the runtime at %v, before the dispatched step ended at %v", closing.ModTime(), left.ModTime())
	}
}

// TestPluginDrainAtPublication checks SIGTERM at the moment the engine is
// about to serve, held there by its test hook (holdServeEnv). A Watch
// opened while the engine starts receives NOT_SERVING. The engine writes
// its drain's reason once the Gate drains, so after that line, with the
// engine still held, a run answers UNAVAILABLE (draining). Released, the
// engine never serves: the Watch receives nothing more before it ends, and
// the engine exits with status 0.
func TestPluginDrainAtPublication(t *testing.T) {
	bun, out := pluginBundle(t)
	hold := t.TempDir()
	d := startDirect(t, pluginConfigOf(t, bun, out), false, holdServeEnv+"="+hold)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	w, err := d.health.Watch(ctx, &healthpb.HealthCheckRequest{Service: plugin.Runner_ServiceDesc.ServiceName})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := w.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("the Watch while the engine starts: %v, %v; want NOT_SERVING", r.GetStatus(), err)
	}
	waitFile(t, hold, "at-serve")
	d.signal(t, syscall.SIGTERM)
	d.waitStderr(t, "draining: SIGTERM")
	_, err = d.runner.Run(ctx, &plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x":1}`)})
	checkRefused(t, "a run once a drain began at the engine's publication", err, codes.Unavailable, "draining")

	if err := os.WriteFile(filepath.Join(hold, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if sts := watchUntilEnd(t, w); len(sts) != 0 {
		t.Errorf("the Watch received %v after a drain began before the engine served, want nothing more", sts)
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
}

// TestPluginDrainDispatchedTimeout checks a drain whose time is up while a
// run that a step dispatched is still in its step, never released: once
// the drain's timer fires (drainTimerEnv), the runtime closes, which
// cancels that run as wl's close does, and the engine exits with status 0,
// saying that the drain's time is up; the step never ends on its own.
func TestPluginDrainDispatchedTimeout(t *testing.T) {
	bun, out := pluginBundle(t)
	timer := t.TempDir()
	d := startDirect(t, pluginConfigOf(t, bun, out), true, drainTimerEnv+"="+timer)
	dir := t.TempDir()
	args, err := json.Marshal(map[string]any{"dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	if o := <-d.runAsync(&plugin.RunRequest{Kind: "script", Path: "f/plugin/dispatch", Args: args}); o.err != nil || o.res.GetStatus() != "success" {
		t.Fatalf("the dispatch: %v, %v", o.res, o.err)
	}
	waitFile(t, dir, "entered")

	d.signal(t, syscall.SIGTERM)
	d.waitStderr(t, "the drain waits for the runs that steps dispatched")
	waitFile(t, timer, "duration")
	if err := os.WriteFile(filepath.Join(timer, "expire"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
	if !strings.Contains(d.stderr.String(), "the drain's time is up") {
		t.Errorf("the engine's standard error does not say that the drain's time is up:\n%s", d.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "left")); err == nil {
		t.Errorf("the dispatched step, never released, ended on its own")
	}
}

// TestPluginDrainAnswerGrace checks the bound on a drained engine's wait
// for the Run calls to answer after its runtime's close. Two runs wait in
// their steps; the engine's test hook keeps the call of the run outlives
// from answering (holdAnswerEnv). Once the drain's time is up
// (drainTimerEnv), the other run's caller receives canceled (cause
// closing), and the engine exits with status 0 though the held call never
// answered, saying so.
func TestPluginDrainAnswerGrace(t *testing.T) {
	bun, out := pluginBundle(t)
	timer := t.TempDir()
	d := startDirect(t, pluginConfigOf(t, bun, out), true, holdAnswerEnv+"=outlives", drainTimerEnv+"="+timer)
	heldDir, heldArgs := newGate(t)
	held := d.runAsync(&plugin.RunRequest{Id: "outlives", Path: "f/plugin/gate", Args: heldArgs})
	otherDir, otherArgs := newGate(t)
	other := d.runAsync(&plugin.RunRequest{Path: "f/plugin/gate", Args: otherArgs})
	waitFile(t, heldDir, "entered")
	waitFile(t, otherDir, "entered")

	d.signal(t, syscall.SIGTERM)
	waitFile(t, timer, "duration")
	if err := os.WriteFile(filepath.Join(timer, "expire"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if o := <-other; o.err != nil || o.res.GetStatus() != "canceled" || o.res.GetCancelCause() != "closing" {
		t.Errorf("the run whose call answers: %v, %v; want canceled with the cause closing", o.res, o.err)
	}
	if code := d.exitCode(t); code != 0 {
		t.Errorf("the engine exited with %d, want 0\nits standard error:\n%s", code, d.stderr)
	}
	if o := <-held; o.err == nil {
		t.Errorf("the held call answered %v, want the error of an engine that exited", o.res)
	}
	if !strings.Contains(d.stderr.String(), "a Run call did not answer within") {
		t.Errorf("the engine's standard error does not report the call that did not answer:\n%s", d.stderr)
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

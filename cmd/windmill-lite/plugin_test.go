//go:build unix

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
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

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/host"
	"github.com/invakid404/windmill-lite/wl"
	"github.com/invakid404/windmill-lite/wl/plugin"
)

// These tests run `windmill-lite plugin` end to end (ADR 0009, the go-plugin
// adapter). This package's test binary is the engine: a host launches it
// through wl/plugin's ClientConfig, which checks its SHA-256 and starts it
// with a literal environment, as a go-plugin host does, and calls it
// through plugin.Client over go-plugin's gRPC connection. TestMain serves
// the plugin command when WL_TEST_ENGINE=1 is in that environment. The
// bundles are built from synthetic folders with the pinned Bun (WL_BUN);
// without it the tests skip, unless WL_REQUIRE_BUN is set.

// pluginFiles is the synthetic folder of the plugin tests.
var pluginFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/plugin/sleep.ts": `// Resolves with ms after waiting that long on a timer.
export async function main(ms: number) {
  await Bun.sleep(ms);
  return ms;
}
`,
	"f/plugin/echo.ts": `// Returns its argument.
export async function main(x: unknown) {
  return x;
}
`,
	"f/plugin/fail_after_write.ts": `// Writes the file at path, then fails.
import { writeFileSync } from "node:fs";
export async function main(path: string) {
  writeFileSync(path, "written");
  throw new Error("failed after the write");
}
`,
	"f/plugin/identity.ts": `// Returns the job identity that its environment carries.
export async function main() {
  const e = process.env;
  return { workspace: e.WM_WORKSPACE, username: e.WM_USERNAME, email: e.WM_EMAIL };
}
`,
	"f/plugin/file_mode.ts": `// Creates the file at path and returns its permission bits.
import { statSync, writeFileSync } from "node:fs";
export async function main(path: string) {
  writeFileSync(path, "x");
  return statSync(path).mode & 0o777;
}
`,
	"f/plugin/variable.ts": `// Reads the variable at path through the job API, at the platform
// facade's origin.
export async function main(path: string) {
  const r = await fetch(` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/variables/get_value/${path}`" + `, {
    headers: { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + ` },
  });
  return { status: r.status, value: await r.json() };
}
`,
	"f/plugin/gate.ts": `// Writes entered in dir, then waits until the test writes release there,
// at most 60 s, and writes left.
import { existsSync, writeFileSync } from "node:fs";
export async function main(dir: string) {
  writeFileSync(dir + "/entered", "");
  const until = Date.now() + 60_000;
  while (!existsSync(dir + "/release")) {
    if (Date.now() > until) throw new Error("not released");
    await Bun.sleep(10);
  }
  writeFileSync(dir + "/left", "");
  return "released";
}
`,
	"f/plugin/dispatch.ts": `// Dispatches the flow f/plugin/gate with dir through the job API, as
// runFlowAsync does, to start delay seconds later when delay is given, and
// returns the new job's id.
export async function main(dir: string, delay?: number) {
  const query = delay ? "?scheduled_in_secs=" + delay : "";
  const r = await fetch(` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/jobs/run/f/f/plugin/gate${query}`" + `, {
    method: "POST",
    headers: { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + `, "Content-Type": "application/json" },
    body: JSON.stringify({ dir }),
  });
  if (r.status !== 201) throw new Error("dispatch answered " + r.status + ": " + (await r.text()));
  return await r.text();
}
`,
	"f/plugin/sleep.flow/flow.yaml": `{"summary":"one step that sleeps","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/plugin/sleep",` +
		`"input_transforms":{"ms":{"type":"javascript","expr":"flow_input.ms"}}}}]}}`,
	"f/plugin/echo.flow/flow.yaml": `{"summary":"one step that echoes","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/plugin/echo",` +
		`"input_transforms":{"x":{"type":"javascript","expr":"flow_input.x"}}}}]}}`,
	"f/plugin/variable.flow/flow.yaml": `{"summary":"one step that reads a variable","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/plugin/variable",` +
		`"input_transforms":{"path":{"type":"static","value":"f/plugin/secret"}}}}]}}`,
	"f/plugin/gate.flow/flow.yaml": `{"summary":"one step that waits for its release","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/plugin/gate",` +
		`"input_transforms":{"dir":{"type":"javascript","expr":"flow_input.dir"}}}}]}}`,
	"f/plugin/parent.flow/flow.yaml": `{"summary":"calls the flow f/plugin/sub by path","value":{"modules":[{"id":"c","value":{"type":"flow","path":"f/plugin/sub",` +
		`"input_transforms":{"x":{"type":"javascript","expr":"flow_input.x"}}}}]}}`,
	"f/plugin/sub.flow/flow.yaml": `{"summary":"one step that echoes, called by path","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/plugin/echo",` +
		`"input_transforms":{"x":{"type":"javascript","expr":"flow_input.x"}}}}]}}`,
}

// pluginContract's roots leave out f/plugin/sub, a flow that only
// f/plugin/parent calls.
const pluginContract = `roots:
  - flow: f/plugin/sleep
  - flow: f/plugin/echo
  - flow: f/plugin/variable
  - flow: f/plugin/gate
  - flow: f/plugin/parent
  - script: f/plugin/sleep
  - script: f/plugin/variable
  - script: f/plugin/dispatch
  - script: f/plugin/fail_after_write
  - script: f/plugin/identity
  - script: f/plugin/file_mode
`

var pluginBuild struct {
	once   sync.Once
	dir    string
	bundle string
	err    error
}

var engineBin struct {
	once sync.Once
	path string
	sum  string
	err  error
}

// pluginBundle returns the pinned Bun and the plugin tests' bundle, built
// once per test binary.
func pluginBundle(t *testing.T) (bun, out string) {
	t.Helper()
	bun = absBun(t)
	pluginBuild.once.Do(func() {
		if pluginBuild.dir, pluginBuild.err = os.MkdirTemp("", "wl-plugin-"); pluginBuild.err != nil {
			return
		}
		pluginBuild.bundle = filepath.Join(pluginBuild.dir, "bundle")
		pluginBuild.err = buildFolder(bun, pluginFiles, pluginContract, pluginBuild.bundle)
	})
	if pluginBuild.err != nil {
		t.Fatal(pluginBuild.err)
	}
	return bun, pluginBuild.bundle
}

// absBun is bunForTest as an absolute path, which the engine's literal
// environment cannot resolve otherwise.
func absBun(t *testing.T) string {
	t.Helper()
	bun := bunForTest(t)
	if strings.ContainsRune(bun, filepath.Separator) {
		abs, err := filepath.Abs(bun)
		if err != nil {
			t.Fatal(err)
		}
		return abs
	}
	p, err := exec.LookPath(bun)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// buildFolder builds a bundle of the folder files, with the host contract
// contract, into out.
func buildFolder(bun string, files map[string]string, contract, out string) error {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return err
	}
	folder, err := os.MkdirTemp("", "wl-plugin-folder-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(folder)
	for p, s := range files {
		f := filepath.Join(folder, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			return err
		}
	}
	work, err := os.MkdirTemp("", "wl-plugin-work-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	_, err = bundle.Build(context.Background(), bundle.Config{
		Folder: folder, Contract: []byte(contract), EngineVersion: resolveVersion(), Bun: bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
		Out:            out, Work: work,
	})
	return err
}

// engineExecutable returns the engine, this test binary, and its SHA-256.
func engineExecutable(t *testing.T) (path, sum string) {
	t.Helper()
	engineBin.once.Do(func() {
		if engineBin.path, engineBin.err = os.Executable(); engineBin.err != nil {
			return
		}
		data, err := os.ReadFile(engineBin.path)
		if err != nil {
			engineBin.err = err
			return
		}
		s := sha256.Sum256(data)
		engineBin.sum = hex.EncodeToString(s[:])
	})
	if engineBin.err != nil {
		t.Fatal(engineBin.err)
	}
	return engineBin.path, engineBin.sum
}

// engineEnv is the engine's literal environment: what makes the test
// binary serve as the engine, and the PATH and HOME that its harness
// processes take.
func engineEnv(extra ...string) []string {
	return append([]string{"WL_TEST_ENGINE=1", "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, extra...)
}

// pluginConfigOf is the configuration of bundle out, which a test changes.
func pluginConfigOf(t *testing.T, bun, out string) map[string]any {
	t.Helper()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"bundle":          out,
		"bun":             bun,
		"harness_runtime": filepath.Join(repo, "harnesses", "bun", "runtime"),
	}
}

// writePluginConfig writes cfg as a configuration file, with its keys in
// order, and returns its path.
func writePluginConfig(t *testing.T, cfg map[string]any) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// lineLog collects the lines of the engine's standard error, which
// go-plugin hands to ClientConfig.Stderr one line at a time.
type lineLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.Write(p)
	l.b.WriteByte('\n')
	return len(p), nil
}

func (l *lineLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// pluginEngine is one launch of the engine.
type pluginEngine struct {
	client *goplugin.Client
	runner *plugin.Client
	// health is a gRPC health client of the engine's connection.
	health healthpb.HealthClient
	stderr *lineLog
	// addr is the address of the engine's socket.
	addr string
	// base is the launch's private base directory.
	base string
}

// launchPlugin launches the engine with the configuration cfg, with env
// added to its environment, connects to it and waits until it serves, as a
// host's admission does. The launch ends with the test.
func launchPlugin(t *testing.T, cfg map[string]any, env ...string) *pluginEngine {
	t.Helper()
	e := connectPlugin(t, cfg, env...)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for {
		st, err := e.runner.Health(ctx)
		if err == nil && st == healthpb.HealthCheckResponse_SERVING {
			return e
		}
		if ctx.Err() != nil {
			t.Fatalf("the engine did not serve within 2 minutes (last health %v, %v)\nits standard error:\n%s", st, err, e.stderr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// launchHeld launches the engine with the configuration cfg and connects to
// it, with its runtime's open held by the engine's test hook (holdOpenEnv),
// so it stays in the state Starting until the test calls release.
func launchHeld(t *testing.T, cfg map[string]any) (e *pluginEngine, release func()) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "release-open")
	e = connectPlugin(t, cfg, holdOpenEnv+"="+file)
	return e, func() {
		t.Helper()
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// connectPlugin launches the engine with the configuration cfg, with env
// added to its environment, and connects to it, without waiting for it to
// serve. The launch ends with the test.
func connectPlugin(t *testing.T, cfg map[string]any, env ...string) *pluginEngine {
	t.Helper()
	exe, sum := engineExecutable(t)
	gcfg, cleanup, err := plugin.ClientConfig(plugin.Launch{
		Path: exe, SHA256: sum, Config: writePluginConfig(t, cfg), Env: engineEnv(env...),
		StartTimeout: 2 * time.Minute,
		Logger:       hclog.New(&hclog.LoggerOptions{Output: io.Discard, Level: hclog.Off}),
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &pluginEngine{stderr: &lineLog{}, base: gcfg.UnixSocketConfig.TempDir}
	gcfg.Stderr = e.stderr
	e.client = goplugin.NewClient(gcfg)
	t.Cleanup(func() {
		e.client.Kill()
		cleanup()
	})
	addr, err := e.client.Start()
	if err != nil {
		t.Fatalf("start the engine: %v\nits standard error:\n%s", err, e.stderr)
	}
	e.addr = addr.String()
	rpc, err := e.client.Client()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	raw, err := rpc.Dispense(plugin.PluginName)
	if err != nil {
		t.Fatalf("Dispense: %v", err)
	}
	c, ok := raw.(*plugin.Client)
	if !ok {
		t.Fatalf("Dispense returned %T, want *plugin.Client", raw)
	}
	e.runner = c
	g, ok := rpc.(*goplugin.GRPCClient)
	if !ok {
		t.Fatalf("go-plugin's client is %T, want a gRPC client", rpc)
	}
	e.health = healthpb.NewHealthClient(g.Conn)
	return e
}

// run runs req within 2 minutes, which Client.Run turns into the run's hard
// deadline when req has none or a later one.
func (e *pluginEngine) run(req *plugin.RunRequest) (*plugin.RunResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return e.runner.Run(ctx, req)
}

// mustRun runs req and fails the test unless the run started.
func (e *pluginEngine) mustRun(t *testing.T, req *plugin.RunRequest) *plugin.RunResult {
	t.Helper()
	res, err := e.run(req)
	if err != nil {
		t.Fatalf("Run %s %s: %v\nthe engine's standard error:\n%s", req.GetKind(), req.GetPath(), err, e.stderr)
	}
	return res
}

func ms(v int64) *int64 { return &v }

// newGate returns a directory for the step f/plugin/gate, and the
// arguments that point the gate flow at it.
func newGate(t *testing.T) (dir string, args []byte) {
	t.Helper()
	dir = t.TempDir()
	args, err := json.Marshal(map[string]string{"dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	return dir, args
}

// waitFile waits up to a minute for the file name in dir.
func waitFile(t *testing.T, dir, name string) {
	t.Helper()
	for until := time.Now().Add(time.Minute); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		}
	}
	t.Fatalf("no %s in %s within a minute", name, dir)
}

// release lets the gate step of dir end.
func release(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// checkRefused checks that err is the engine's refusal of a run that did
// not start: the gRPC code code, a message that names field when it is not
// empty, and the RUN_NOT_STARTED detail, which makes Client.Run's error
// wrap ErrNotStarted.
func checkRefused(t *testing.T, what string, err error, code codes.Code, field string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: the run started, want a refusal with %v", what, code)
		return
	}
	if got := status.Code(err); got != code {
		t.Errorf("%s: code %v, want %v (%v)", what, got, code, err)
	}
	if field != "" && !strings.Contains(err.Error(), field) {
		t.Errorf("%s: the refusal %q does not name the field %s", what, err, field)
	}
	if !errors.Is(err, plugin.ErrNotStarted) {
		t.Errorf("%s: the refusal is not ErrNotStarted: %v", what, err)
	}
	if !hasNotStartedDetail(err) {
		t.Errorf("%s: the refusal has no RUN_NOT_STARTED detail: %v", what, err)
	}
}

// hasNotStartedDetail reports whether err's gRPC status holds the
// engine's RUN_NOT_STARTED ErrorInfo.
func hasNotStartedDetail(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == plugin.ErrorDomain && info.GetReason() == plugin.ReasonRunNotStarted {
			return true
		}
	}
	return false
}

// startRefused starts the engine directly, as go-plugin would, with the
// configuration cfg and env added to its environment, and returns its exit
// code and standard error. It fails the test if the engine served, which
// its first byte of standard output, go-plugin's handshake line, shows, or
// did not exit within a minute.
func startRefused(t *testing.T, cfg map[string]any, env ...string) (int, string) {
	t.Helper()
	exe, _ := engineExecutable(t)
	// A socket directory of its own, which a killed engine leaves its
	// socket in.
	sockets, err := os.MkdirTemp("", "wlr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	cmd := exec.Command(exe, "plugin", "-config", writePluginConfig(t, cfg))
	cmd.Env = engineEnv(append([]string{plugin.Handshake.MagicCookieKey + "=" + plugin.Handshake.MagicCookieValue, "PLUGIN_UNIX_SOCKET_DIR=" + sockets}, env...)...)
	var stderr syncBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	served := make(chan []byte, 1)
	go func() {
		b := make([]byte, 256)
		n, _ := stdout.Read(b)
		served <- b[:n]
	}()
	select {
	case b := <-served:
		if len(b) > 0 {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("the engine served (%q on its standard output), want a refused start\nits standard error:\n%s", b, stderr.String())
		}
	case <-time.After(time.Minute):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("the engine did not exit within a minute\nits standard error:\n%s", stderr.String())
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("the engine exited with %v, want a refused start\nits standard error:\n%s", err, stderr.String())
	}
	return exit.ExitCode(), stderr.String()
}

// TestPluginDeadlineOnlyItsRun checks that a run's deadline ends that run
// alone (ADR 0009, deadlines and outcomes): while a run is in its step,
// which signals its entry and waits for the test's release, one with
// deadline_ms 0 ends canceled with the cause deadline, before any write;
// released, the first then succeeds. A run without deadline_ms, whose call
// has no deadline either, runs unbounded.
func TestPluginDeadlineOnlyItsRun(t *testing.T) {
	bun, out := pluginBundle(t)
	e := launchPlugin(t, pluginConfigOf(t, bun, out))

	type outcome struct {
		res *plugin.RunResult
		err error
	}
	dir, args := newGate(t)
	long := make(chan outcome, 1)
	go func() {
		res, err := e.run(&plugin.RunRequest{Id: "long", Path: "f/plugin/gate", Args: args, DeadlineMs: ms(60000)})
		long <- outcome{res, err}
	}()
	waitFile(t, dir, "entered")
	expired := e.mustRun(t, &plugin.RunRequest{Id: "expired", Path: "f/plugin/sleep", Args: []byte(`{"ms":1}`), DeadlineMs: ms(0)})
	if expired.GetStatus() != "canceled" || expired.GetCancelCause() != "deadline" || expired.GetMayHaveMutated() {
		t.Errorf("the run with deadline_ms 0: status %q, cause %q, may_have_mutated %t; want canceled, deadline, false",
			expired.GetStatus(), expired.GetCancelCause(), expired.GetMayHaveMutated())
	}
	release(t, dir)
	o := <-long
	if o.err != nil {
		t.Fatalf("the run in flight: %v", o.err)
	}
	if o.res.GetStatus() != "success" || string(o.res.GetResult()) != `"released"` {
		t.Errorf("the run in flight: status %q, cause %q, result %s; want success, \"released\"", o.res.GetStatus(), o.res.GetCancelCause(), o.res.GetResult())
	}

	res, err := e.runner.Run(context.Background(), &plugin.RunRequest{Id: "unbounded", Path: "f/plugin/sleep", Args: []byte(`{"ms":1200}`)})
	if err != nil {
		t.Fatalf("the run without a deadline: %v", err)
	}
	if res.GetStatus() != "success" || res.GetCancelCause() != "" {
		t.Errorf("the run without a deadline: status %q, cause %q; want success", res.GetStatus(), res.GetCancelCause())
	}
}

// TestPluginDeadlineFromReceipt checks that a hard deadline counts from the
// engine's receipt of the request, as the pre-write deadline does: with a
// clock that stamps the receipt 10 s before the server runs the request,
// a deadline_ms of 5000 has passed when the run starts, so the run ends
// canceled with the cause deadline, and a run stamped now succeeds. The
// server runs in this process over a runtime of the test bundle.
func TestPluginDeadlineFromReceipt(t *testing.T) {
	bun, out := pluginBundle(t)
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := wl.Open(out, wl.Options{Bun: bun, HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"), EngineVersion: resolveVersion()})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	for _, c := range []struct {
		name   string
		before time.Duration
		status string
		cause  string
	}{
		{"stamped 10 s before", 10 * time.Second, "canceled", "deadline"},
		{"stamped now", 0, "success", ""},
	} {
		srv := host.New(host.Config{Runtime: rt, Now: func() time.Time { return time.Now().Add(-c.before) }})
		res, err := srv.Run(context.Background(), &plugin.RunRequest{Path: "f/plugin/sleep", Args: []byte(`{"ms":300}`), DeadlineMs: ms(5000)})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if res.GetStatus() != c.status || res.GetCancelCause() != c.cause {
			t.Errorf("%s: status %q, cause %q; want %q, %q", c.name, res.GetStatus(), res.GetCancelCause(), c.status, c.cause)
		}
	}
}

// TestPluginScriptRun checks a root script's run (ADR 0009, flow and script
// runs): a deadline that has passed ends it canceled before its invocation
// is sent, so it may not have mutated; a script that fails after a write
// fails with may_have_mutated true, since its writes cannot be seen; a
// field that only a flow run takes refuses the run, naming it; and the
// script reads the run's own identity.
func TestPluginScriptRun(t *testing.T) {
	bun, out := pluginBundle(t)
	e := launchPlugin(t, pluginConfigOf(t, bun, out))

	res := e.mustRun(t, &plugin.RunRequest{Kind: "script", Path: "f/plugin/sleep", Args: []byte(`{"ms":1}`), DeadlineMs: ms(0)})
	if res.GetStatus() != "canceled" || res.GetCancelCause() != "deadline" || res.GetMayHaveMutated() {
		t.Errorf("a script with deadline_ms 0: status %q, cause %q, may_have_mutated %t; want canceled, deadline, false",
			res.GetStatus(), res.GetCancelCause(), res.GetMayHaveMutated())
	}

	written := filepath.Join(t.TempDir(), "written")
	args, _ := json.Marshal(map[string]string{"path": written})
	res = e.mustRun(t, &plugin.RunRequest{Kind: "script", Path: "f/plugin/fail_after_write", Args: args})
	if res.GetStatus() != "failure" || !res.GetMayHaveMutated() || res.GetCancelCause() != "" {
		t.Errorf("a script that fails after a write: status %q, may_have_mutated %t, cause %q; want failure, true, none",
			res.GetStatus(), res.GetMayHaveMutated(), res.GetCancelCause())
	}
	if _, err := os.Stat(written); err != nil {
		t.Errorf("the script did not write its file: %v", err)
	}

	for _, c := range []struct {
		field string
		req   *plugin.RunRequest
	}{
		{"pre_write_deadline_ms", &plugin.RunRequest{Kind: "script", Path: "f/plugin/sleep", Args: []byte(`{"ms":1}`), PreWriteDeadlineMs: ms(1000)}},
		{"hooks", &plugin.RunRequest{Kind: "script", Path: "f/plugin/sleep", Args: []byte(`{"ms":1}`), Hooks: &plugin.Hooks{Token: "t"}}},
		{"write_capable", &plugin.RunRequest{Kind: "script", Path: "f/plugin/sleep", Args: []byte(`{"ms":1}`), WriteCapable: []byte(`[]`)}},
		{"trace", &plugin.RunRequest{Kind: "script", Path: "f/plugin/sleep", Args: []byte(`{"ms":1}`), Trace: "summary"}},
	} {
		_, err := e.run(c.req)
		checkRefused(t, "a script with "+c.field, err, codes.InvalidArgument, c.field)
	}

	res = e.mustRun(t, &plugin.RunRequest{Kind: "script", Path: "f/plugin/identity", Trace: "none",
		Identity: &plugin.Identity{Workspace: "ws-plugin", Username: "plugin-user", Email: "plugin-user@example.com"}})
	var id struct{ Workspace, Username, Email string }
	if err := json.Unmarshal(res.GetResult(), &id); err != nil || res.GetStatus() != "success" {
		t.Fatalf("the identity script: status %q, result %s (%v)", res.GetStatus(), res.GetResult(), err)
	}
	if id.Workspace != "ws-plugin" || id.Username != "plugin-user" || id.Email != "plugin-user@example.com" {
		t.Errorf("the script read the identity %+v, want the run's own", id)
	}
	if !res.GetMayHaveMutated() {
		t.Errorf("a script that ran: may_have_mutated false, want true once its invocation was sent")
	}
}

// TestPluginCancelReason checks Cancel (ADR 0009, the Runner service): it
// cancels a run in flight by its id, and the run ends canceled with the
// cause canceled and a Canceled error that names the reason and the
// canceler; an id of no run in flight answers NOT_FOUND.
func TestPluginCancelReason(t *testing.T) {
	bun, out := pluginBundle(t)
	e := launchPlugin(t, pluginConfigOf(t, bun, out))

	type outcome struct {
		res *plugin.RunResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := e.run(&plugin.RunRequest{Id: "to-cancel", Path: "f/plugin/sleep", Args: []byte(`{"ms":60000}`)})
		done <- outcome{res, err}
	}()
	const reason, canceler = "the operator stopped it", "plugin-test"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Cancel answers NOT_FOUND until the engine holds the run.
	for {
		_, err := e.runner.Cancel(ctx, &plugin.CancelRequest{Id: "to-cancel", Reason: reason, Canceler: canceler})
		if err == nil {
			break
		}
		if status.Code(err) != codes.NotFound || ctx.Err() != nil {
			t.Fatalf("Cancel: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	o := <-done
	if o.err != nil {
		t.Fatalf("the canceled run: %v", o.err)
	}
	result := string(o.res.GetResult())
	if o.res.GetStatus() != "canceled" || o.res.GetCancelCause() != "canceled" || !strings.Contains(result, reason) || !strings.Contains(result, canceler) {
		t.Errorf("the canceled run: status %q, cause %q, result %s; want canceled, canceled, an error naming %q and %q",
			o.res.GetStatus(), o.res.GetCancelCause(), result, reason, canceler)
	}
	if _, err := e.runner.Cancel(ctx, &plugin.CancelRequest{Id: "no-such-run"}); status.Code(err) != codes.NotFound {
		t.Errorf("Cancel of an id no run has: %v, want NOT_FOUND", err)
	}
}

// refusalCase is a request that the engine refuses before the run starts.
type refusalCase struct {
	name  string
	req   *plugin.RunRequest
	code  codes.Code
	field string
}

// refusalCases are the refusals of a serving engine (ADR 0009, runs that
// do not start), but ALREADY_EXISTS, which needs a run in flight.
func refusalCases() []refusalCase {
	// A request from a newer host, with a field that this engine does not
	// know.
	field99 := protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1)
	unknown := &plugin.RunRequest{Path: "f/plugin/echo"}
	unknown.ProtoReflect().SetUnknown(field99)
	identity := &plugin.Identity{Workspace: "ws-plugin"}
	identity.ProtoReflect().SetUnknown(field99)
	resource := &plugin.Resource{Type: "t", Value: []byte(`{}`)}
	resource.ProtoReflect().SetUnknown(field99)
	return []refusalCase{
		{"a field that this engine does not know", unknown, codes.InvalidArgument, "field 99"},
		{"an identity field that this engine does not know", &plugin.RunRequest{Path: "f/plugin/echo", Identity: identity}, codes.InvalidArgument, "identity"},
		{"a resource field that this engine does not know", &plugin.RunRequest{Path: "f/plugin/echo", Resources: map[string]*plugin.Resource{"f/plugin/r": resource}}, codes.InvalidArgument, `resources["f/plugin/r"]`},
		{"selectors that are JSON null", &plugin.RunRequest{Path: "f/plugin/echo", WriteCapable: []byte(`null`)}, codes.InvalidArgument, "write_capable"},
		{"selectors followed by a closing bracket", &plugin.RunRequest{Path: "f/plugin/echo", HaltBefore: []byte(`[]]`)}, codes.InvalidArgument, "halt_before"},
		{"no flow at the path", &plugin.RunRequest{Path: "f/plugin/missing"}, codes.NotFound, ""},
		{"no root script at the path", &plugin.RunRequest{Kind: "script", Path: "f/plugin/missing"}, codes.NotFound, ""},
		{"arguments that are not an object", &plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`[1]`)}, codes.InvalidArgument, ""},
		{"an unknown kind", &plugin.RunRequest{Kind: "job", Path: "f/plugin/echo"}, codes.InvalidArgument, "kind"},
		{"a malformed selector", &plugin.RunRequest{Path: "f/plugin/echo", WriteCapable: []byte(`{"flow":`)}, codes.InvalidArgument, "write_capable"},
		{"an unknown trace level", &plugin.RunRequest{Path: "f/plugin/echo", Trace: "verbose"}, codes.InvalidArgument, "trace"},
		{"an unknown clock", &plugin.RunRequest{Path: "f/plugin/echo", Clock: "fast"}, codes.InvalidArgument, "clock"},
		{"an intercept that is not an object", &plugin.RunRequest{Path: "f/plugin/echo", Intercept: []byte(`[1]`)}, codes.InvalidArgument, "intercept"},
	}
}

// alreadyExists runs a second request with the id of a run that is in its
// step, which waits for the test's release, and returns the second's
// error. The first then succeeds.
func alreadyExists(t *testing.T, e *pluginEngine) error {
	t.Helper()
	type outcome struct {
		res *plugin.RunResult
		err error
	}
	dir, args := newGate(t)
	first := make(chan outcome, 1)
	go func() {
		res, err := e.run(&plugin.RunRequest{Id: "dup", Path: "f/plugin/gate", Args: args})
		first <- outcome{res, err}
	}()
	waitFile(t, dir, "entered")
	_, err := e.run(&plugin.RunRequest{Id: "dup", Path: "f/plugin/sleep", Args: []byte(`{"ms":1}`)})
	release(t, dir)
	if o := <-first; o.err != nil || o.res.GetStatus() != "success" {
		t.Errorf("the first run with the id: (%v, %v), want success", o.res, o.err)
	}
	return err
}

// TestPluginErrorCodes checks the gRPC code of each refusal (ADR 0009, runs
// that do not start): UNAVAILABLE while the engine starts, its runtime's
// open held by its test hook; then, on a serving engine, ALREADY_EXISTS for
// an id that a run in flight has, NOT_FOUND for a path that names no root
// of the bundle, INVALID_ARGUMENT for a request that the engine refuses.
func TestPluginErrorCodes(t *testing.T) {
	bun, out := pluginBundle(t)
	held, release := launchHeld(t, pluginConfigOf(t, bun, out))
	if _, err := held.run(&plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x":1}`)}); status.Code(err) != codes.Unavailable {
		t.Errorf("a run while the engine starts: %v, want UNAVAILABLE", err)
	}
	release()
	e := launchPlugin(t, pluginConfigOf(t, bun, out))
	if err := alreadyExists(t, e); status.Code(err) != codes.AlreadyExists {
		t.Errorf("a run whose id a run in flight has: %v, want ALREADY_EXISTS", err)
	}
	for _, c := range refusalCases() {
		_, err := e.run(c.req)
		if got := status.Code(err); got != c.code {
			t.Errorf("%s: %v, want %v", c.name, err, c.code)
		}
		if c.field != "" && (err == nil || !strings.Contains(err.Error(), c.field)) {
			t.Errorf("%s: %v does not name the field %s", c.name, err, c.field)
		}
	}
}

// TestPluginRefusalDetail checks that every refusal of a run that did not
// start, UNAVAILABLE while the engine starts included, carries the
// RUN_NOT_STARTED detail, so that Client.Run reports
// ErrNotStarted and the host may retry it, while a run that started and
// failed, or was canceled, answers its RunResult and never an error.
func TestPluginRefusalDetail(t *testing.T) {
	bun, out := pluginBundle(t)
	held, release := launchHeld(t, pluginConfigOf(t, bun, out))
	_, err := held.run(&plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x":1}`)})
	checkRefused(t, "UNAVAILABLE while the engine starts", err, codes.Unavailable, "")
	release()
	e := launchPlugin(t, pluginConfigOf(t, bun, out))
	checkRefused(t, "ALREADY_EXISTS", alreadyExists(t, e), codes.AlreadyExists, "")
	for _, c := range refusalCases() {
		_, err := e.run(c.req)
		checkRefused(t, c.name, err, c.code, c.field)
	}
	written := filepath.Join(t.TempDir(), "written")
	args, _ := json.Marshal(map[string]string{"path": written})
	for _, req := range []*plugin.RunRequest{
		{Kind: "script", Path: "f/plugin/fail_after_write", Args: args},
		{Path: "f/plugin/sleep", Args: []byte(`{"ms":1}`), DeadlineMs: ms(0)},
	} {
		res, err := e.run(req)
		if err != nil || (res.GetStatus() != "failure" && res.GetStatus() != "canceled") {
			t.Errorf("a run that started and ended %s: (%v, %v), want its RunResult", req.GetPath(), res, err)
		}
	}
}

// TestPluginStagedFields checks the fields that the engine does not serve
// yet: a request that sets one is refused with INVALID_ARGUMENT, naming
// it, and the RUN_NOT_STARTED detail, and a configuration that sets one
// refuses the start, naming it, rather than running without it.
func TestPluginStagedFields(t *testing.T) {
	bun, out := pluginBundle(t)
	e := launchPlugin(t, pluginConfigOf(t, bun, out))
	for _, c := range []struct {
		field string
		req   *plugin.RunRequest
	}{
		{"pin", &plugin.RunRequest{Path: "f/plugin/echo", Pin: true}},
		{"limits", &plugin.RunRequest{Path: "f/plugin/echo", Limits: &plugin.Limits{MaxInFlight: 1}}},
		{"run_data", &plugin.RunRequest{Path: "f/plugin/echo", RunData: []byte(`{"tenant":"a"}`)}},
		{"hooks", &plugin.RunRequest{Path: "f/plugin/echo", Hooks: &plugin.Hooks{BrokerId: 1, Token: "t"}}},
	} {
		_, err := e.run(c.req)
		checkRefused(t, "a request with "+c.field, err, codes.InvalidArgument, c.field)
	}

	for _, c := range []struct {
		field string
		set   func(map[string]any)
	}{
		{"engine_recycle_rss", func(m map[string]any) { m["engine_recycle_rss"] = 1 << 30 }},
		{"drain_timeout_ms", func(m map[string]any) { m["drain_timeout_ms"] = 1000 }},
		{"bytes_total", func(m map[string]any) { m["bytes_total"] = 1 << 20 }},
		{"foreign_windmill_origins", func(m map[string]any) { m["foreign_windmill_origins"] = []string{"https://windmill.example.com"} }},
		{"pool.max_live", func(m map[string]any) { m["pool"] = map[string]any{"max_live": 2} }},
		{"pool.pin_runs", func(m map[string]any) { m["pool"] = map[string]any{"pin_runs": true} }},
		{`providers["tenants"]`, func(m map[string]any) { m["providers"] = map[string]any{"tenants": "broker"} }},
	} {
		cfg := pluginConfigOf(t, bun, out)
		c.set(cfg)
		code, stderr := startRefused(t, cfg)
		if code != 1 || !strings.Contains(stderr, c.field) {
			t.Errorf("a configuration with %s: exit %d, stderr %q; want 1, naming the field", c.field, code, stderr)
		}
	}

	// A bundle whose manifest names a host plugin.
	files := map[string]string{"f/plugin/hostplugin.ts": "export default {};\n"}
	for p, s := range pluginFiles {
		files[p] = s
	}
	withPlugin := filepath.Join(t.TempDir(), "bundle")
	if err := buildFolder(bun, files, "roots: [{flow: f/plugin/echo}]\nplugin: f/plugin/hostplugin.ts\n", withPlugin); err != nil {
		t.Fatal(err)
	}
	code, stderr := startRefused(t, pluginConfigOf(t, bun, withPlugin))
	if code != 1 || !strings.Contains(stderr, "host plugin") {
		t.Errorf("a bundle whose manifest names a host plugin: exit %d, stderr %q; want 1, naming the plugin", code, stderr)
	}

	// A bundle that passes the check and is then replaced by one with a host
	// plugin, before the runtime opens it: the engine's test hook replaces
	// it between the two (swapBundleEnv).
	replaced := filepath.Join(t.TempDir(), "bundle")
	if err := os.CopyFS(replaced, os.DirFS(out)); err != nil {
		t.Fatal(err)
	}
	// The runtime opens once go-plugin has written its handshake line, so
	// this start fails after it, however long go-plugin takes to set its
	// server up: the engine's test hook delays that (delayHandshakeEnv) past
	// the time that the failing open takes.
	code, stderr = startFails(t, pluginConfigOf(t, bun, replaced), swapBundleEnv+"="+withPlugin, delayHandshakeEnv+"=500ms")
	if code != 1 || !strings.Contains(stderr, "digest") {
		t.Errorf("a bundle replaced after its check: exit %d, stderr %q; want 1, naming the digest", code, stderr)
	}
}

// startFails starts the engine directly, as go-plugin would, with the
// configuration cfg and env added to its environment, and returns its exit
// code and standard error once it exits, at most a minute later. Unlike
// startRefused, it requires go-plugin's handshake line, as the first line of
// the engine's standard output, before the start fails.
func startFails(t *testing.T, cfg map[string]any, env ...string) (int, string) {
	t.Helper()
	exe, _ := engineExecutable(t)
	sockets, err := os.MkdirTemp("", "wlr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "plugin", "-config", writePluginConfig(t, cfg))
	cmd.Env = engineEnv(append([]string{plugin.Handshake.MagicCookieKey + "=" + plugin.Handshake.MagicCookieValue, "PLUGIN_UNIX_SOCKET_DIR=" + sockets}, env...)...)
	var stdout, stderr syncBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || ctx.Err() != nil {
		t.Fatalf("the engine exited with %v (context %v), want a failed start\nits standard error:\n%s", err, ctx.Err(), stderr.String())
	}
	first, _, _ := strings.Cut(stdout.String(), "\n")
	if !handshakeLine.MatchString(first + "\n") {
		t.Errorf("the engine's standard output starts %q, want go-plugin's handshake line before the start fails\nits standard error:\n%s", first, stderr.String())
	}
	return exit.ExitCode(), stderr.String()
}

// handshakeLine matches go-plugin's handshake line on a Unix socket.
var handshakeLine = regexp.MustCompile(`^1\|1\|unix\|[^|]+\|grpc\|\n$`)

// holdOpenEnv names a file whose creation releases the engine's runtime
// open: the engine's test hook holds the open until the file exists, at
// most 2 minutes (beforeOpen).
const holdOpenEnv = "WL_TEST_PLUGIN_HOLD_OPEN"

// holdPublishEnv names a file that the engine's test hook writes once its
// runtime has opened, before it serves; the hook then holds the engine
// there until its shutdown, at most 2 minutes (beforePublish).
const holdPublishEnv = "WL_TEST_PLUGIN_HOLD_PUBLISH"

// delayHandshakeEnv is a duration by which the engine's test hook delays
// go-plugin's server setup, and so its handshake line (beforeHandshake).
const delayHandshakeEnv = "WL_TEST_PLUGIN_DELAY_HANDSHAKE"

// swapBundleEnv names a bundle that the engine's test hook copies over the
// configured bundle between its check and the runtime's open
// (afterBundleCheck).
const swapBundleEnv = "WL_TEST_PLUGIN_SWAP_BUNDLE"

// installPluginTestHooks installs, in the engine that TestMain starts, the
// hooks that the plugin tests set through the environment.
func installPluginTestHooks() {
	if file := os.Getenv(holdOpenEnv); file != "" {
		beforeOpen = func() {
			for until := time.Now().Add(2 * time.Minute); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(file); err == nil {
					return
				}
			}
		}
	}
	if file := os.Getenv(holdPublishEnv); file != "" {
		beforePublish = func(stopped <-chan struct{}) {
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				panic(err)
			}
			select {
			case <-stopped:
			case <-time.After(2 * time.Minute):
			}
		}
	}
	if d := os.Getenv(delayHandshakeEnv); d != "" {
		delay, err := time.ParseDuration(d)
		if err != nil {
			panic(err)
		}
		beforeHandshake = func() { time.Sleep(delay) }
	}
	if from := os.Getenv(swapBundleEnv); from != "" {
		afterBundleCheck = func(bundle string) {
			if err := os.RemoveAll(bundle); err != nil {
				panic(err)
			}
			if err := os.CopyFS(bundle, os.DirFS(from)); err != nil {
				panic(err)
			}
		}
	}
}

// cleanupPluginTests removes the plugin tests' shared bundle.
func cleanupPluginTests() {
	if pluginBuild.dir != "" {
		os.RemoveAll(pluginBuild.dir)
	}
}

// TestPluginConfigRefusals checks starts that fail without serving: a
// setting in milliseconds that a duration cannot hold, either way, refuses
// the start, naming the field; and a socket directory in which go-plugin
// cannot listen ends the engine with status 1 and no handshake line.
func TestPluginConfigRefusals(t *testing.T) {
	bun, out := pluginBundle(t)
	for _, c := range []struct {
		field string
		set   func(map[string]any)
	}{
		{"plugin_timeout_ms", func(m map[string]any) { m["plugin_timeout_ms"] = int64(9223372036855) }},
		{"pool.lag_threshold_ms", func(m map[string]any) { m["pool"] = map[string]any{"lag_threshold_ms": int64(9223372036855)} }},
		{"pool.lag_threshold_ms", func(m map[string]any) { m["pool"] = map[string]any{"lag_threshold_ms": int64(-9223372036855)} }},
		{"pool.recycle_timeout_ms", func(m map[string]any) { m["pool"] = map[string]any{"recycle_timeout_ms": int64(9223372036855)} }},
	} {
		cfg := pluginConfigOf(t, bun, out)
		c.set(cfg)
		code, stderr := startRefused(t, cfg)
		if code != 1 || !strings.Contains(stderr, c.field+":") {
			t.Errorf("a configuration with %s out of range: exit %d, stderr %q; want 1, naming the field", c.field, code, stderr)
		}
	}

	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr := startRefused(t, pluginConfigOf(t, bun, out), "PLUGIN_UNIX_SOCKET_DIR="+notADir)
	if code != 1 || !strings.Contains(stderr, "did not start serving") {
		t.Errorf("a socket directory that is a file: exit %d, stderr %q; want 1, saying that it did not serve", code, stderr)
	}
}

// TestPluginDispatchedID checks that the id of a run that a step dispatched
// refuses a run until that run ends (ADR 0009, runs that do not start).
//
//   - Before the dispatched run starts: a script dispatches the flow
//     f/plugin/gate to start in a day, so the dispatcher holds it, before
//     its job exists, for longer than the test may run (its deadline is
//     checked). A request with its id is refused with ALREADY_EXISTS and the
//     RUN_NOT_STARTED detail.
//   - While it runs and after: another dispatch starts the flow at once. A
//     request with its id is refused while its step, which signals its entry
//     and waits for the test's release, runs; released, the run goes on to
//     its end.
func TestPluginDispatchedID(t *testing.T) {
	bun, out := pluginBundle(t)
	e := launchPlugin(t, pluginConfigOf(t, bun, out))
	dispatch := func(dir string, delay int) string {
		t.Helper()
		args, err := json.Marshal(map[string]any{"dir": dir, "delay": delay})
		if err != nil {
			t.Fatal(err)
		}
		res := e.mustRun(t, &plugin.RunRequest{Kind: "script", Path: "f/plugin/dispatch", Args: args})
		var id string
		if err := json.Unmarshal(res.GetResult(), &id); err != nil || res.GetStatus() != "success" || id == "" {
			t.Fatalf("the dispatch: %s %s (%v)", res.GetStatus(), res.GetResult(), err)
		}
		return id
	}

	const day = 24 * 60 * 60
	if dl, ok := t.Deadline(); !ok || time.Until(dl) >= day*time.Second {
		t.Fatalf("the test may run past the held run's start in %d s (deadline %v, %t): run it with a -timeout under a day", day, dl, ok)
	}
	held := dispatch(t.TempDir(), day)
	_, err := e.run(&plugin.RunRequest{Id: held, Path: "f/plugin/echo", Args: []byte(`{"x":1}`)})
	checkRefused(t, "a run with the id of a dispatched run that waits for its start", err, codes.AlreadyExists, held)

	dir := t.TempDir()
	child := dispatch(dir, 0)
	waitFile(t, dir, "entered")
	_, err = e.run(&plugin.RunRequest{Id: child, Path: "f/plugin/echo", Args: []byte(`{"x":1}`)})
	checkRefused(t, "a run with the id of a dispatched run in its step", err, codes.AlreadyExists, child)
	release(t, dir)
	waitFile(t, dir, "left")
}

// TestPluginServesBundle checks readiness (ADR 0009, health, readiness and
// drain): with the runtime's open held by the engine's test hook, the engine
// has completed go-plugin's handshake and answers health NOT_SERVING, not
// go-plugin's own SERVING, and GetBuildInfo UNAVAILABLE. Released, it
// answers SERVING, and GetBuildInfo returns wl's build information of the
// bundle, with the configuration's labels and digest and the protocol
// version.
func TestPluginServesBundle(t *testing.T) {
	bun, out := pluginBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	cfg["labels"] = map[string]string{"region": "test-region"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	e, release := launchHeld(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, name := range []string{"", "plugin", plugin.Runner_ServiceDesc.ServiceName} {
		r, err := e.health.Check(ctx, &healthpb.HealthCheckRequest{Service: name})
		if err != nil || r.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
			t.Errorf("health of %q while the engine starts: %v, %v; want NOT_SERVING", name, r.GetStatus(), err)
		}
	}
	if _, err := e.runner.BuildInfo(ctx); status.Code(err) != codes.Unavailable {
		t.Errorf("GetBuildInfo while the engine starts: %v, want UNAVAILABLE", err)
	}

	release()
	waitServing(t, e)
	info, err := e.runner.BuildInfo(ctx)
	if err != nil {
		t.Fatalf("GetBuildInfo once the engine serves: %v", err)
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := wl.Open(out, wl.Options{Bun: bun, HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"), EngineVersion: resolveVersion()})
	if err != nil {
		t.Fatal(err)
	}
	want := rt.BuildInfo()
	rt.Close()
	switch {
	case info.GetBundleDigest() != want.BundleDigest || info.GetEngineVersion() != want.EngineVersion ||
		info.GetBunVersion() != want.BunVersion || info.GetEsbuildVersion() != want.EsbuildVersion ||
		!maps.Equal(info.GetHarnessRuntimes(), want.HarnessRuntimes) || info.GetModuleState() != want.ModuleState ||
		info.GetSharedCode() != want.SharedCode || string(info.GetContract()) != string(want.Contract) ||
		!slices.Equal(info.GetDeviations(), want.Deviations) || info.GetNonconformantRuntime() != want.NonconformantRuntime:
		t.Errorf("GetBuildInfo %v, want wl's %+v", info, want)
	case info.GetProtocolVersion() != plugin.ProtocolVersion:
		t.Errorf("protocol_version %d, want %d", info.GetProtocolVersion(), plugin.ProtocolVersion)
	case !maps.Equal(info.GetLabels(), map[string]string{"region": "test-region"}):
		t.Errorf("labels %v, want the configuration's", info.GetLabels())
	case info.GetConfigDigest() != hex.EncodeToString(digest[:]):
		t.Errorf("config_digest %s, want the SHA-256 of the file, %x", info.GetConfigDigest(), digest)
	}

	// A Bun that cannot start a harness process: without a warm start, no
	// harness process starts before a run, so the engine serves; with one,
	// the open fails, so the engine never serves, and it exits with status
	// 1, naming the warm start, after go-plugin's handshake.
	broken := filepath.Join(t.TempDir(), "bun")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	launchPlugin(t, pluginConfigOf(t, broken, out))
	warm := pluginConfigOf(t, broken, out)
	warm["pool"] = map[string]any{"min": 1, "warm_start": true}
	w, releaseWarm := launchHeld(t, warm)
	watch, err := w.health.Watch(ctx, &healthpb.HealthCheckRequest{Service: plugin.Runner_ServiceDesc.ServiceName})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := watch.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("a warm start that fails, while the engine starts: %v, %v; want NOT_SERVING", r.GetStatus(), err)
	}
	releaseWarm()
	for {
		r, err := watch.Recv()
		if err != nil {
			break
		}
		if r.GetStatus() == healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("a warm start that fails: the engine serves\nits standard error:\n%s", w.stderr)
		}
	}
	code, stderr := startFails(t, warm)
	if code != 1 || !strings.Contains(stderr, "warm start") {
		t.Errorf("a warm start that fails: exit %d, stderr %q; want 1, naming the warm start", code, stderr)
	}
}

// waitServing waits up to 2 minutes for e's health to answer SERVING.
func waitServing(t *testing.T, e *pluginEngine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for {
		st, err := e.runner.Health(ctx)
		if err == nil && st == healthpb.HealthCheckResponse_SERVING {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("the engine did not serve within 2 minutes (last health %v, %v)\nits standard error:\n%s", st, err, e.stderr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPluginShutdownWhileStarting checks go-plugin's shutdown call, which
// the host's Kill sends, while the engine starts. With the runtime's open
// held by the engine's test hook, the engine still exits within 10
// seconds. With the runtime opened, and the engine held by its test hook
// before it serves (holdPublishEnv), the engine closes the runtime before
// it exits: the runtime's temporary data directory is gone.
func TestPluginShutdownWhileStarting(t *testing.T) {
	bun, out := pluginBundle(t)
	held, _ := launchHeld(t, pluginConfigOf(t, bun, out))
	shutdown(t, held)

	tmp := t.TempDir()
	marker := filepath.Join(t.TempDir(), "opened")
	e := connectPlugin(t, pluginConfigOf(t, bun, out), "TMPDIR="+tmp, holdPublishEnv+"="+marker)
	waitFile(t, filepath.Dir(marker), filepath.Base(marker))
	if data, err := filepath.Glob(filepath.Join(tmp, "wl-*")); err != nil || len(data) != 1 {
		t.Fatalf("the opened runtime's data directories in the engine's TMPDIR: %v, %v; want one", data, err)
	}
	if st, err := e.runner.Health(context.Background()); err != nil || st != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("health before the engine serves: %v, %v; want NOT_SERVING", st, err)
	}
	shutdown(t, e)
	if data, err := filepath.Glob(filepath.Join(tmp, "wl-*")); err != nil || len(data) != 0 {
		t.Errorf("after the shutdown, the runtime's data directories: %v, %v; want none, removed by its Close", data, err)
	}
}

// shutdown sends e go-plugin's shutdown call, as the host's Kill does before
// it waits 2 seconds and kills the engine, and waits up to 10 seconds for
// the engine to exit.
func shutdown(t *testing.T, e *pluginEngine) {
	t.Helper()
	rpc, err := e.client.Client()
	if err != nil {
		t.Fatal(err)
	}
	rpc.Close()
	for until := time.Now().Add(10 * time.Second); !e.client.Exited(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("the engine did not exit within 10 seconds of the shutdown call\nits standard error:\n%s", e.stderr)
		}
	}
}

// TestPluginHealthMethods checks gRPC health (ADR 0009, health, readiness
// and drain): Check answers the engine's state for the empty name, plugin
// and windmill_lite.v1.Runner, NOT_SERVING while it starts and SERVING once
// it serves, and List answers the same three; Check of another name answers
// NOT_FOUND, and Watch of it SERVICE_UNKNOWN; a Watch opened while the
// engine starts receives NOT_SERVING, then SERVING once the engine serves.
func TestPluginHealthMethods(t *testing.T) {
	bun, out := pluginBundle(t)
	e, release := launchHeld(t, pluginConfigOf(t, bun, out))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	names := []string{"", "plugin", plugin.Runner_ServiceDesc.ServiceName}
	checkAll := func(when string, want healthpb.HealthCheckResponse_ServingStatus) {
		t.Helper()
		for _, name := range names {
			r, err := e.health.Check(ctx, &healthpb.HealthCheckRequest{Service: name})
			if err != nil || r.GetStatus() != want {
				t.Errorf("Check(%q) %s: %v, %v; want %v", name, when, r.GetStatus(), err, want)
			}
		}
		l, err := e.health.List(ctx, &healthpb.HealthListRequest{})
		if err != nil || len(l.GetStatuses()) != len(names) {
			t.Fatalf("List %s: %v, %v; want the %d names", when, l, err, len(names))
		}
		for _, name := range names {
			if got := l.GetStatuses()[name].GetStatus(); got != want {
				t.Errorf("List %s: %q is %v, want %v", when, name, got, want)
			}
		}
	}
	checkAll("while the engine starts", healthpb.HealthCheckResponse_NOT_SERVING)
	if _, err := e.health.Check(ctx, &healthpb.HealthCheckRequest{Service: "other"}); status.Code(err) != codes.NotFound {
		t.Errorf("Check(\"other\"): %v, want NOT_FOUND", err)
	}
	unknown, err := e.health.Watch(ctx, &healthpb.HealthCheckRequest{Service: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := unknown.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_SERVICE_UNKNOWN {
		t.Errorf("Watch(\"other\"): %v, %v; want SERVICE_UNKNOWN", r.GetStatus(), err)
	}
	watch, err := e.health.Watch(ctx, &healthpb.HealthCheckRequest{Service: plugin.Runner_ServiceDesc.ServiceName})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := watch.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("the Watch opened while the engine starts: first %v, %v; want NOT_SERVING", r.GetStatus(), err)
	}

	release()
	if r, err := watch.Recv(); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("the Watch opened while the engine starts: next %v, %v; want SERVING", r.GetStatus(), err)
	}
	checkAll("once the engine serves", healthpb.HealthCheckResponse_SERVING)
}

// TestPluginNonRootFlow checks the path of a flow that the bundle compiled
// only because a root calls it: Runner runs it as wl's Runtime.Run does,
// which runs any flow of the bundle, while a path that names no flow
// answers NOT_FOUND (TestPluginErrorCodes).
func TestPluginNonRootFlow(t *testing.T) {
	bun, out := pluginBundle(t)
	e := launchPlugin(t, pluginConfigOf(t, bun, out))
	res := e.mustRun(t, &plugin.RunRequest{Path: "f/plugin/sub", Args: []byte(`{"x":7}`)})
	if res.GetStatus() != "success" || string(res.GetResult()) != "7" {
		t.Errorf("the flow f/plugin/sub, which only f/plugin/parent calls: %s %s; want success, 7", res.GetStatus(), res.GetResult())
	}
}

// TestPluginSecretsByReference checks secrets by reference (ADR 0009, the
// plugin command): a secret variable's value comes from the environment
// variable that value_env names, and reaches the step that reads it; it is
// in no line of the engine's standard error and not in its build
// information; the configuration's digest is the SHA-256 of the file, so
// it changes with the reference's name but not with the value; a variable
// that is not set fails the start, naming it; and a secret written in the
// file refuses the start, naming the field and not the value.
func TestPluginSecretsByReference(t *testing.T) {
	bun, out := pluginBundle(t)
	// The environment variable and its synthetic value, declared apart:
	// on one line, the secret scan's generic rule reads the pair as a key.
	const env = "WL_TEST_PLUGIN_SECRET"
	const value = "synthetic-secret-7f1c2a"
	cfg := pluginConfigOf(t, bun, out)
	cfg["variables"] = map[string]any{"f/plugin/secret": map[string]any{"value_env": env, "is_secret": true}}
	path := writePluginConfig(t, cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)

	e := launchPlugin(t, cfg, env+"="+value)
	res := e.mustRun(t, &plugin.RunRequest{Path: "f/plugin/variable"})
	var got struct {
		Status int
		Value  string
	}
	if err := json.Unmarshal(res.GetResult(), &got); err != nil || res.GetStatus() != "success" || got.Status != 200 || got.Value != value {
		t.Errorf("the step's read of the secret: status %q, result %s (%v); want 200 with the environment's value", res.GetStatus(), res.GetResult(), err)
	}
	info, err := e.runner.BuildInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.GetConfigDigest() != hex.EncodeToString(sum[:]) {
		t.Errorf("config_digest %s, want the SHA-256 of the file, %x", info.GetConfigDigest(), sum)
	}
	if strings.Contains(fmt.Sprint(info), value) {
		t.Errorf("the build information holds the secret's value: %v", info)
	}
	if strings.Contains(e.stderr.String(), value) {
		t.Errorf("the engine's standard error holds the secret's value:\n%s", e.stderr)
	}

	other := launchPlugin(t, cfg, env+"=another-synthetic-value")
	if info2, err := other.runner.BuildInfo(context.Background()); err != nil || info2.GetConfigDigest() != info.GetConfigDigest() {
		t.Errorf("another value of the variable: config_digest %s (%v), want %s", info2.GetConfigDigest(), err, info.GetConfigDigest())
	}
	renamed := pluginConfigOf(t, bun, out)
	renamed["variables"] = map[string]any{"f/plugin/secret": map[string]any{"value_env": env + "_B", "is_secret": true}}
	third := launchPlugin(t, renamed, env+"_B="+value)
	if info3, err := third.runner.BuildInfo(context.Background()); err != nil || info3.GetConfigDigest() == info.GetConfigDigest() {
		t.Errorf("another reference name: config_digest %s (%v), want one other than %s", info3.GetConfigDigest(), err, info.GetConfigDigest())
	}

	// Two variables may name one environment variable.
	twice := pluginConfigOf(t, bun, out)
	twice["variables"] = map[string]any{
		"f/plugin/secret":  map[string]any{"value_env": env, "is_secret": true},
		"f/plugin/secret2": map[string]any{"value_env": env, "is_secret": true},
	}
	both := launchPlugin(t, twice, env+"="+value)
	for _, path := range []string{"f/plugin/secret", "f/plugin/secret2"} {
		args, _ := json.Marshal(map[string]string{"path": path})
		res := both.mustRun(t, &plugin.RunRequest{Kind: "script", Path: "f/plugin/variable", Args: args})
		var read struct {
			Status *int
			Value  *string
		}
		err := json.Unmarshal(res.GetResult(), &read)
		if err != nil || res.GetStatus() != "success" || read.Status == nil || *read.Status != 200 || read.Value == nil || *read.Value != value {
			t.Errorf("one environment variable named twice, the read of %s: %s %s (%v); want success, 200 and its value", path, res.GetStatus(), res.GetResult(), err)
		}
	}

	code, stderr := startRefused(t, cfg)
	if code != 1 || !strings.Contains(stderr, env) {
		t.Errorf("the variable not set: exit %d, stderr %q; want 1, naming %s", code, stderr, env)
	}

	const inline = "synthetic-inline-secret-93ab"
	for _, c := range []struct {
		field string
		set   func(map[string]any)
	}{
		{`variables["f/plugin/secret"].value`, func(m map[string]any) {
			m["variables"] = map[string]any{"f/plugin/secret": map[string]any{"value": inline, "is_secret": true}}
		}},
		{"resource_store.url", func(m map[string]any) {
			m["resource_store"] = map[string]any{"kind": "redis", "url": "redis://:" + inline + "@localhost:6379/0"}
		}},
	} {
		cfg := pluginConfigOf(t, bun, out)
		c.set(cfg)
		code, stderr := startRefused(t, cfg)
		if code != 1 || !strings.Contains(stderr, c.field+":") || strings.Contains(stderr, inline) {
			t.Errorf("a secret written in the file at %s: exit %d, stderr %q; want 1, naming the field and not the value", c.field, code, stderr)
		}
	}
}

// TestPluginSocketPrivate checks what protects the engine's socket (ADR
// 0009, the plugin command): started under the umask 0027, the engine
// serves on a socket with no group or other permission, mode 0700, in
// go-plugin's private directory, while a step creates its files under the
// umask that the engine was started with.
func TestPluginSocketPrivate(t *testing.T) {
	bun, out := pluginBundle(t)
	// The engine inherits the umask of the process that starts it.
	old := syscall.Umask(0o027)
	e := func() *pluginEngine {
		defer syscall.Umask(old)
		return launchPlugin(t, pluginConfigOf(t, bun, out))
	}()
	fi, err := os.Lstat(e.addr)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSocket == 0 || fi.Mode().Perm() != 0o700 {
		t.Errorf("the engine's socket %s has mode %v, want a socket of mode 0700", e.addr, fi.Mode())
	}
	if dir := filepath.Dir(e.addr); filepath.Dir(dir) != e.base {
		t.Errorf("the engine's socket %s is not in a directory of the launch's base directory %s", e.addr, e.base)
	} else if di, err := os.Stat(dir); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("the socket's directory %s: %v, %v; want mode 0700", dir, di, err)
	}

	file := filepath.Join(t.TempDir(), "created")
	args, _ := json.Marshal(map[string]string{"path": file})
	res := e.mustRun(t, &plugin.RunRequest{Kind: "script", Path: "f/plugin/file_mode", Args: args})
	if res.GetStatus() != "success" || string(res.GetResult()) != fmt.Sprint(0o640) {
		t.Errorf("a step's new file: status %q, mode %s; want success, %d (0640, the launch umask 0027)", res.GetStatus(), res.GetResult(), 0o640)
	}
}

// TestRunnerResultPolls checks the configuration's result_polls_for_all_jobs
// through Client.Run, on the recorded result-polls fixture, whose flow
// polls a completed step, a failed sub-flow's flow job and its running
// sibling (conformance/flows/client-api/result-polls). With the field
// true, the run succeeds with the answers that the oracle recorded, such
// as the completed step's result in jsonb's text output; without it, the
// default, the polls of those jobs answer 501, which fails the driver
// with UnsupportedPlatformCall.
func TestRunnerResultPolls(t *testing.T) {
	bun := absBun(t)
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder: resultPollsTree, EngineVersion: resolveVersion(), Bun: bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
		Out:            out, Work: filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	const completed = `{\"started\":true,\"success\":true,\"completed\":true,\"result\":{\"k\": [1, 2.5]}}`
	for _, on := range []bool{true, false} {
		cfg := pluginConfigOf(t, bun, out)
		if on {
			cfg["result_polls_for_all_jobs"] = true
		}
		e := launchPlugin(t, cfg)
		res := e.mustRun(t, &plugin.RunRequest{Path: "f/wl_rp/main", Args: []byte(`{}`)})
		result := string(res.GetResult())
		switch {
		case on && (res.GetStatus() != "success" || !strings.Contains(result, completed)):
			t.Errorf("with result_polls_for_all_jobs: %s %s, want success with %s", res.GetStatus(), result, completed)
		case !on && (res.GetStatus() != "failure" || !strings.Contains(result, "UnsupportedPlatformCall") || !strings.Contains(result, "get_result_maybe")):
			t.Errorf("without it: %s %s, want the driver's UnsupportedPlatformCall for get_result_maybe", res.GetStatus(), result)
		}
	}
}

// TestPluginHandshakeFirstLine checks that go-plugin's handshake line is the
// first line of the engine's standard output, which go-plugin reads before
// anything else, with a configuration whose warm start starts a harness
// process.
func TestPluginHandshakeFirstLine(t *testing.T) {
	bun, out := pluginBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	cfg["pool"] = map[string]any{"min": 1, "warm_start": true}
	// The engine is killed, so it removes no directory of its own.
	cfg["data_dir"] = t.TempDir()
	exe, _ := engineExecutable(t)
	sockets, err := os.MkdirTemp("", "wlh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	cmd := exec.Command(exe, "plugin", "-config", writePluginConfig(t, cfg))
	cmd.Env = engineEnv(plugin.Handshake.MagicCookieKey+"="+plugin.Handshake.MagicCookieValue, "PLUGIN_UNIX_SOCKET_DIR="+sockets)
	var stderr syncBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(stdout).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		if !handshakeLine.MatchString(s) {
			t.Errorf("the engine's first line of standard output is %q, want go-plugin's handshake line\nits standard error:\n%s", s, stderr.String())
		}
	case <-time.After(2 * time.Minute):
		t.Fatalf("no line on the engine's standard output\nits standard error:\n%s", stderr.String())
	}
}

// TestPluginFlowRequest checks how a flow run's request maps to the run:
// its static interceptions stand in for the modules with their ids, its
// trace level gives the trace that a host plugin's run-end hook gets at
// that level, and its own variables replace the runtime's for this run.
func TestPluginFlowRequest(t *testing.T) {
	bun, out := pluginBundle(t)
	cfg := pluginConfigOf(t, bun, out)
	cfg["variables"] = map[string]any{"f/plugin/secret": map[string]any{"value": "the runtime's value"}}
	e := launchPlugin(t, cfg)

	res := e.mustRun(t, &plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x":1}`), Intercept: []byte(`{"a":{"intercepted":true}}`)})
	var intercepted struct{ Intercepted bool }
	if err := json.Unmarshal(res.GetResult(), &intercepted); err != nil || res.GetStatus() != "success" || !intercepted.Intercepted || len(res.GetTrace()) != 0 {
		t.Errorf("an intercepted step: status %q, result %s, trace %s; want success with the interception's result and no trace", res.GetStatus(), res.GetResult(), res.GetTrace())
	}

	for _, level := range []string{"summary", "full"} {
		res := e.mustRun(t, &plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x":2}`), Trace: level})
		var tr map[string]json.RawMessage
		if err := json.Unmarshal(res.GetTrace(), &tr); err != nil {
			t.Fatalf("the %s trace %s: %v", level, res.GetTrace(), err)
		}
		_, steps := tr["steps"]
		if string(tr["status"]) != `"success"` || string(tr["run"]) != `"`+res.GetId()+`"` || steps != (level == "full") {
			t.Errorf("the %s trace %s: want the run's id and status, with steps only at full", level, res.GetTrace())
		}
	}

	res = e.mustRun(t, &plugin.RunRequest{Path: "f/plugin/variable", Variables: map[string]*plugin.Variable{"f/plugin/secret": {Value: "the run's value"}}})
	if !strings.Contains(string(res.GetResult()), "the run's value") {
		t.Errorf("a run with its own variable: %s %s, want its own value", res.GetStatus(), res.GetResult())
	}
	res = e.mustRun(t, &plugin.RunRequest{Path: "f/plugin/variable"})
	if !strings.Contains(string(res.GetResult()), "the runtime's value") {
		t.Errorf("a run without: %s %s, want the runtime's value", res.GetStatus(), res.GetResult())
	}
}

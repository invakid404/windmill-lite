//go:build unix

package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// testPlugins are the executables of testdata/runnerplugin, built once for
// the package's tests: v1 serves only Runner version 1, v2 only version 2.
var testPlugins struct {
	once     sync.Once
	dir      string
	v1, v2   string
	sum1     string
	sum2     string
	buildErr error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if testPlugins.dir != "" {
		os.RemoveAll(testPlugins.dir)
	}
	os.Exit(code)
}

// buildPlugins builds the test plugins with the go command, and fails the
// test if it cannot: a launch test never skips.
func buildPlugins(t *testing.T) {
	t.Helper()
	testPlugins.once.Do(func() {
		dir, err := os.MkdirTemp("", "wlp-test-")
		if err != nil {
			testPlugins.buildErr = err
			return
		}
		testPlugins.dir = dir
		for _, v := range []struct {
			version string
			path    *string
			sum     *string
		}{{"1", &testPlugins.v1, &testPlugins.sum1}, {"2", &testPlugins.v2, &testPlugins.sum2}} {
			out := filepath.Join(dir, "runner-v"+v.version)
			cmd := exec.Command("go", "build", "-o", out, "-ldflags", "-X main.servedVersion="+v.version, "./testdata/runnerplugin")
			if b, err := cmd.CombinedOutput(); err != nil {
				testPlugins.buildErr = fmt.Errorf("go build of the version %s plugin: %v\n%s", v.version, err, b)
				return
			}
			data, err := os.ReadFile(out)
			if err != nil {
				testPlugins.buildErr = err
				return
			}
			sum := sha256.Sum256(data)
			*v.path, *v.sum = out, hex.EncodeToString(sum[:])
		}
	})
	if testPlugins.buildErr != nil {
		t.Fatal(testPlugins.buildErr)
	}
}

// pluginConfig is the configuration file of the test plugin.
type pluginConfig struct {
	Marker string   `json:"marker,omitempty"`
	Env    []string `json:"env,omitempty"`
}

func writeConfig(t *testing.T, c pluginConfig) string {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var quiet = hclog.New(&hclog.LoggerOptions{Output: hclog.DefaultOutput, Level: hclog.Off})

// newLaunch returns go-plugin's client for l, the base directory of its
// configuration and the launch's cleanup. When the test ends, it kills the
// client and then runs the cleanup.
func newLaunch(t *testing.T, l Launch) (*goplugin.Client, string, func() error) {
	t.Helper()
	if l.Logger == nil {
		l.Logger = quiet
	}
	cfg, cleanup, err := ClientConfig(l)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UnixSocketConfig == nil || cfg.UnixSocketConfig.TempDir == "" {
		t.Fatal("ClientConfig gave go-plugin no base directory for its sockets")
	}
	client := goplugin.NewClient(cfg)
	t.Cleanup(func() {
		client.Kill()
		cleanup()
	})
	return client, cfg.UnixSocketConfig.TempDir, cleanup
}

// dispense connects client and returns its Client.
func dispense(t *testing.T, client *goplugin.Client) *Client {
	t.Helper()
	rpc, err := client.Client()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	raw, err := rpc.Dispense(PluginName)
	if err != nil {
		t.Fatalf("Dispense: %v", err)
	}
	c, ok := raw.(*Client)
	if !ok {
		t.Fatalf("Dispense returned %T, want *Client", raw)
	}
	return c
}

func checkGone(t *testing.T, what, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s %s still exists (Lstat: %v)", what, path, err)
	}
}

// waitGone waits up to 10 s for path to be removed.
func waitGone(t *testing.T, what, path string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return
		}
	}
	checkGone(t, what, path)
}

func checkPrivateDir(t *testing.T, what, path string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("%s %s has mode %v, want a directory of mode 0700", what, path, fi.Mode())
	}
}

func checkEmpty(t *testing.T, what, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%s %s holds %d entries, want none", what, dir, len(entries))
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestVersionNegotiationRealHandshake launches two real plugin executables
// through ClientConfig, which lists only Runner version 1: go-plugin's
// handshake refuses the one that serves only version 2, and dispenses from
// the one that serves version 1.
func TestVersionNegotiationRealHandshake(t *testing.T) {
	buildPlugins(t)
	ctx := context.Background()

	v2, _, _ := newLaunch(t, Launch{Path: testPlugins.v2, SHA256: testPlugins.sum2, Config: writeConfig(t, pluginConfig{})})
	if _, err := v2.Client(); err == nil {
		t.Fatal("the version 2 plugin connected; want go-plugin's handshake to refuse it")
	}

	v1, _, _ := newLaunch(t, Launch{Path: testPlugins.v1, SHA256: testPlugins.sum1, Config: writeConfig(t, pluginConfig{})})
	c := dispense(t, v1)
	if n := v1.NegotiatedVersion(); n != 1 {
		t.Errorf("NegotiatedVersion = %d, want 1", n)
	}
	info, err := c.BuildInfo(ctx)
	if err != nil {
		t.Fatalf("BuildInfo: %v", err)
	}
	if info.GetProtocolVersion() != 1 {
		t.Errorf("the plugin serves version %d, want 1", info.GetProtocolVersion())
	}
}

// TestClientConfigPin launches the test plugin through the real launcher:
// a matching SHA-256 starts it, with go-plugin's handshake variables and
// the launch's literal environment only; a mismatching one refuses before
// any process starts, and removes the base directory.
func TestClientConfigPin(t *testing.T) {
	buildPlugins(t)
	ctx := context.Background()
	t.Setenv("WL_PLUGIN_TEST_SENTINEL", "only in the test's environment")
	env := []string{Handshake.MagicCookieKey, "PLUGIN_UNIX_SOCKET_DIR", "PLUGIN_PROTOCOL_VERSIONS", "WL_PLUGIN_TEST_SENTINEL", "WL_PLUGIN_TEST_LITERAL"}

	t.Run("matching", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "started")
		client, base, _ := newLaunch(t, Launch{
			Path:   testPlugins.v1,
			SHA256: testPlugins.sum1,
			Config: writeConfig(t, pluginConfig{Marker: marker, Env: env}),
			Env:    []string{"WL_PLUGIN_TEST_LITERAL=from the launch", Handshake.MagicCookieKey + "=overridden"},
		})
		info, err := dispense(t, client).BuildInfo(ctx)
		if err != nil {
			t.Fatalf("BuildInfo: %v", err)
		}
		labels := info.GetLabels()
		if got := labels["env:"+Handshake.MagicCookieKey]; got != Handshake.MagicCookieValue {
			t.Errorf("the plugin saw the cookie %q, want %q", got, Handshake.MagicCookieValue)
		}
		if got := labels["env:PLUGIN_PROTOCOL_VERSIONS"]; got != "1" {
			t.Errorf("the plugin saw PLUGIN_PROTOCOL_VERSIONS %q, want 1", got)
		}
		if got, ok := labels["env:PLUGIN_UNIX_SOCKET_DIR"]; !ok || filepath.Dir(got) != base {
			t.Errorf("the plugin saw PLUGIN_UNIX_SOCKET_DIR %q (set %v), want a directory in %s", got, ok, base)
		}
		if got := labels["env:WL_PLUGIN_TEST_LITERAL"]; got != "from the launch" {
			t.Errorf("the plugin saw WL_PLUGIN_TEST_LITERAL %q, want the launch's value", got)
		}
		if got, ok := labels["env:WL_PLUGIN_TEST_SENTINEL"]; ok {
			t.Errorf("the plugin saw the host's WL_PLUGIN_TEST_SENTINEL %q", got)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("the plugin wrote no marker: %v", err)
		}
	})

	t.Run("mismatching", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "started")
		client, base, _ := newLaunch(t, Launch{
			Path:   testPlugins.v1,
			SHA256: testPlugins.sum2,
			Config: writeConfig(t, pluginConfig{Marker: marker}),
		})
		if _, err := client.Start(); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("Start = %v, want ErrChecksumMismatch", err)
		}
		if id := client.ID(); id != "" {
			t.Errorf("go-plugin reports the process %q", id)
		}
		if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the plugin started: its marker exists (Stat: %v)", err)
		}
		checkGone(t, "the base directory", base)
	})
}

// TestClientConfigBareFilename launches the engine by a bare file name: the
// launch hashes and starts the same file, the one in the current
// directory, and never the one of that name that PATH finds.
func TestClientConfigBareFilename(t *testing.T) {
	buildPlugins(t)
	cwd, onPath := t.TempDir(), t.TempDir()
	copyExecutable(t, testPlugins.v1, filepath.Join(cwd, "engine"))
	copyExecutable(t, testPlugins.v2, filepath.Join(onPath, "engine"))
	config := writeConfig(t, pluginConfig{})
	t.Chdir(cwd)
	t.Setenv("PATH", onPath)

	client, _, _ := newLaunch(t, Launch{Path: "engine", SHA256: testPlugins.sum1, Config: config})
	info, err := dispense(t, client).BuildInfo(context.Background())
	if err != nil {
		t.Fatalf("BuildInfo: %v", err)
	}
	if info.GetProtocolVersion() != 1 {
		t.Errorf("the launch started the plugin of version %d, want the version 1 plugin it hashed", info.GetProtocolVersion())
	}
}

// fakeHostHooks is a host's HostHooks service that records each call and
// answers with reply.
type fakeHostHooks struct {
	UnimplementedHostHooksServer
	reply []byte
	mu    sync.Mutex
	calls []*HookCall
}

func (h *fakeHostHooks) Call(_ context.Context, call *HookCall) (*HookReply, error) {
	h.mu.Lock()
	h.calls = append(h.calls, call)
	h.mu.Unlock()
	return &HookReply{Payload: h.reply}, nil
}

// TestClientConfigSocketPlacement launches the test plugin, which serves
// with umask 0077 as the engine does, from a host whose umask is 0277 while
// the launch makes its directories: the plugin's socket and the host's
// broker listener lie in a directory of mode 0700 in the base directory of
// mode 0700, the plugin calls the host over that broker connection, and
// both directories are gone after Kill and after a failed launch.
//
// The umask belongs to the whole process; no test of the package runs in
// parallel, so only this launch makes files while it is narrowed.
func TestClientConfigSocketPlacement(t *testing.T) {
	buildPlugins(t)

	// Under the umask 0277, a directory made with mode 0700 is 0500, and the
	// plugin could not create its socket in it.
	l := Launch{Path: testPlugins.v1, SHA256: testPlugins.sum1, Config: writeConfig(t, pluginConfig{})}
	old := syscall.Umask(0o277)
	client, base, addr, err := func() (*goplugin.Client, string, net.Addr, error) {
		defer syscall.Umask(old)
		client, base, _ := newLaunch(t, l)
		addr, err := client.Start()
		return client, base, addr, err
	}()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if addr.Network() != "unix" {
		t.Fatalf("the plugin listens on %s %s, want a Unix socket", addr.Network(), addr)
	}
	sock := addr.String()
	sockDir := filepath.Dir(sock)
	if filepath.Dir(sockDir) != base {
		t.Fatalf("the plugin's socket %s is not in a directory in the base directory %s", sock, base)
	}
	checkPrivateDir(t, "the base directory", base)
	checkPrivateDir(t, "the socket directory", sockDir)
	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSocket == 0 || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("the plugin's socket has mode %v, want a socket with no group or other permission", fi.Mode())
	}

	// The host serves HostHooks on one broker connection, and the plugin
	// dials it and calls it while it runs a run that names it.
	c := dispense(t, client)
	id := c.broker.NextId()
	hooks := &fakeHostHooks{reply: []byte(`{"from":"host"}`)}
	go c.broker.AcceptAndServe(id, func(opts []grpc.ServerOption) *grpc.Server {
		s := grpc.NewServer(opts...)
		RegisterHostHooksServer(s, hooks)
		return s
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := c.Run(ctx, &RunRequest{Id: "r-1", Hooks: &Hooks{BrokerId: id, Token: "tok-1"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.GetResult()); got != `{"from":"host"}` {
		t.Errorf("the run's result is %q, want the host's reply", got)
	}
	hooks.mu.Lock()
	calls := append([]*HookCall(nil), hooks.calls...)
	hooks.mu.Unlock()
	if len(calls) != 1 || calls[0].GetHook() != "probe" || calls[0].GetRun() != "r-1" || calls[0].GetToken() != "tok-1" || string(calls[0].GetPayload()) != `{"from":"plugin"}` {
		t.Errorf("the host got the calls %v, want one probe of r-1 with tok-1", calls)
	}
	var listener string
	entries, err := os.ReadDir(sockDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if p := filepath.Join(sockDir, e.Name()); p != sock && e.Type()&fs.ModeSocket != 0 {
			listener = p
		}
	}
	if listener == "" {
		t.Errorf("the host's broker listener is not in %s", sockDir)
	}

	client.Kill()
	checkGone(t, "the socket directory", sockDir)
	checkGone(t, "the base directory", base)

	// A launch that fails after its process started: go-plugin's handshake
	// refuses a plugin of another version.
	marker := filepath.Join(t.TempDir(), "started")
	failed, failedBase, _ := newLaunch(t, Launch{Path: testPlugins.v2, SHA256: testPlugins.sum2, Config: writeConfig(t, pluginConfig{Marker: marker})})
	if _, err := failed.Start(); err == nil {
		t.Fatal("the version 2 plugin started")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the failed launch started no process: %v", err)
	}
	waitGone(t, "the base directory of the failed launch", failedBase)
}

// TestClientConfigCleanup checks that the cleanup that ClientConfig returns
// removes the base directory where the launch itself cannot: when
// go-plugin's Start fails before it calls the runner, and when the
// configuration is never started. Once the launch has removed the base
// directory, cleanup leaves a later directory of the same name alone, and
// after a failed removal it tries again.
func TestClientConfigCleanup(t *testing.T) {
	buildPlugins(t)
	l := Launch{Path: testPlugins.v1, SHA256: testPlugins.sum1, Config: writeConfig(t, pluginConfig{}), Logger: quiet}

	t.Run("a start that fails before the runner", func(t *testing.T) {
		cfg, cleanup, err := ClientConfig(l)
		if err != nil {
			t.Fatal(err)
		}
		base := cfg.UnixSocketConfig.TempDir
		// go-plugin makes the socket directory, then fails to give it a
		// group that does not exist, before it calls the runner.
		cfg.UnixSocketConfig.Group = "wl-plugin-test-no-such-group"
		client := goplugin.NewClient(cfg)
		defer client.Kill()
		if _, err := client.Start(); err == nil {
			t.Fatal("Start with an unknown socket group succeeded")
		}
		if client.ID() != "" {
			t.Fatalf("go-plugin reports the process %q", client.ID())
		}
		if entries, err := os.ReadDir(base); err != nil || len(entries) != 1 {
			t.Fatalf("the failed start left %d entries in the base directory (%v), want its socket directory", len(entries), err)
		}
		client.Kill()
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		checkGone(t, "the base directory", base)
	})

	t.Run("never started", func(t *testing.T) {
		cfg, cleanup, err := ClientConfig(l)
		if err != nil {
			t.Fatal(err)
		}
		base := cfg.UnixSocketConfig.TempDir
		checkPrivateDir(t, "the base directory", base)
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		checkGone(t, "the base directory", base)
	})

	// After the launch removed its base directory, a later directory of the
	// same name, made by anyone, is not the launch's.
	reuse := func(t *testing.T, base string, cleanup func() error) {
		t.Helper()
		checkGone(t, "the base directory", base)
		if err := os.Mkdir(base, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(base) })
		sentinel := filepath.Join(base, "sentinel")
		if err := os.WriteFile(sentinel, []byte("not the launch's"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := cleanup(); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Errorf("cleanup removed a later directory of the base directory's name: %v", err)
		}
	}

	t.Run("after Kill", func(t *testing.T) {
		client, base, cleanup := newLaunch(t, l)
		dispense(t, client)
		client.Kill()
		reuse(t, base, cleanup)
	})

	t.Run("after a refused start", func(t *testing.T) {
		mismatch := l
		mismatch.SHA256 = testPlugins.sum2
		client, base, cleanup := newLaunch(t, mismatch)
		if _, err := client.Start(); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("Start = %v, want ErrChecksumMismatch", err)
		}
		reuse(t, base, cleanup)
	})

	t.Run("a failed removal is tried again", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root removes a directory whatever its mode")
		}
		cfg, cleanup, err := ClientConfig(l)
		if err != nil {
			t.Fatal(err)
		}
		base := cfg.UnixSocketConfig.TempDir
		// A directory whose entries cannot be removed makes the removal fail.
		locked := filepath.Join(base, "locked")
		if err := os.Mkdir(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locked, "file"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o700); os.RemoveAll(base) })
		if err := cleanup(); err == nil {
			t.Fatal("cleanup removed a directory whose entries cannot be removed")
		}
		if err := os.Chmod(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup after the failure: %v", err)
		}
		checkGone(t, "the base directory", base)
	})
}

// TestClientConfigStartTimeout launches a large executable, a sparse file
// of 4 GiB, whose SHA-256 check takes much longer than the start timeout of
// 50 ms: the timeout ends the check, Start returns the timeout's error, no
// process starts, and the base directory is gone.
func TestClientConfigStartTimeout(t *testing.T) {
	big := filepath.Join(t.TempDir(), "engine")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(4 << 30); err != nil {
		t.Fatal(err)
	}
	f.Close()

	sum := hex.EncodeToString(make([]byte, sha256.Size))
	client, base, _ := newLaunch(t, Launch{Path: big, SHA256: sum, Config: "unused", StartTimeout: 50 * time.Millisecond})
	start := time.Now()
	if _, err := client.Start(); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Start = %v, want context.DeadlineExceeded", err)
	}
	t.Logf("Start returned after %v", time.Since(start))
	if id := client.ID(); id != "" {
		t.Errorf("go-plugin reports the process %q", id)
	}
	checkGone(t, "the base directory", base)
}

// TestClientConfigNotARegularFile launches a named pipe that no process
// writes, whose blocking open would never return, and a directory: each
// launch refuses at once, starts no process and removes its base
// directory.
func TestClientConfigNotARegularFile(t *testing.T) {
	sum := hex.EncodeToString(make([]byte, sha256.Size))
	fifo := filepath.Join(t.TempDir(), "engine")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, t.TempDir()} {
		client, base, _ := newLaunch(t, Launch{Path: path, SHA256: sum, Config: "unused"})
		// If the launch's open of the pipe blocked, this cleanup, which runs
		// before the client's, would open the pipe for writing and close it,
		// so that the open returns and the test ends.
		t.Cleanup(func() {
			if path == fifo {
				if w, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
					w.Close()
				}
			}
		})
		done := make(chan error, 1)
		go func() {
			_, err := client.Start()
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
				t.Errorf("Start(%s) = %v, want the refusal of a file that is not regular", path, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("Start(%s) did not return within 10 s", path)
		}
		if id := client.ID(); id != "" {
			t.Errorf("go-plugin reports the process %q", id)
		}
		checkGone(t, "the base directory", base)
	}
}

// startLauncher runs the runner of a launch of the test plugin directly,
// with a context that the test cancels from the runner's hook after the
// SHA-256 check (afterCheck) or right after the process has started
// (afterSpawn). It returns the runner, the process id that afterSpawn saw,
// or 0, and Start's error.
func startLauncher(t *testing.T, cancelAfterSpawn bool) (*launcher, int, error) {
	t.Helper()
	buildPlugins(t)
	sum, err := hex.DecodeString(testPlugins.sum1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pid := 0
	r := &launcher{path: testPlugins.v1, sum: sum, config: writeConfig(t, pluginConfig{}), base: &baseDir{path: t.TempDir()}}
	r.afterSpawn = func(p int) {
		pid = p
		if cancelAfterSpawn {
			cancel()
		}
	}
	if !cancelAfterSpawn {
		r.afterCheck = cancel
	}
	err = r.Start(ctx)
	return r, pid, err
}

// TestLauncherStartEndsBeforeTheSpawn cancels the start context after the
// SHA-256 check has passed: the runner starts no process and removes the
// base directory.
func TestLauncherStartEndsBeforeTheSpawn(t *testing.T) {
	r, pid, err := startLauncher(t, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want context.Canceled", err)
	}
	if pid != 0 {
		t.Errorf("the runner started the process %d", pid)
	}
	if id := r.ID(); id != "" {
		t.Errorf("the runner reports the process %s", id)
	}
	checkGone(t, "the base directory", r.base.path)
}

// TestLauncherStartEndsAsTheProcessStarts cancels the start context right
// after the process has started: the runner kills and reaps the process,
// returns the context's error and removes the base directory.
func TestLauncherStartEndsAsTheProcessStarts(t *testing.T) {
	r, pid, err := startLauncher(t, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want context.Canceled", err)
	}
	if pid == 0 {
		t.Fatal("the runner started no process")
	}
	// The process was reaped, so no process of that id is left.
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("the process %d is still there (kill 0: %v)", pid, err)
	}
	if id := r.ID(); id != "" {
		t.Errorf("the runner reports the process %s", id)
	}
	checkGone(t, "the base directory", r.base.path)
}

// TestClientConfigRelativeTempDir gives a relative TempDir, then changes the
// working directory during the launch: the base directory is absolute, in
// the TempDir that the relative path named, and gone after Kill.
func TestClientConfigRelativeTempDir(t *testing.T) {
	buildPlugins(t)
	config := writeConfig(t, pluginConfig{})
	// A short directory, so that the socket paths fit macOS's limit.
	root, err := os.MkdirTemp("/tmp", "wlr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, d := range []string{filepath.Join(first, "rel"), second} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(first)

	client, base, _ := newLaunch(t, Launch{Path: testPlugins.v1, SHA256: testPlugins.sum1, Config: config, TempDir: "rel"})
	if !filepath.IsAbs(base) {
		t.Fatalf("the base directory %s is relative", base)
	}
	got, err := filepath.EvalSymlinks(filepath.Dir(base))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(first, "rel"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("the base directory %s is not in %s", base, want)
	}

	if err := os.Chdir(second); err != nil {
		t.Fatal(err)
	}
	dispense(t, client)
	client.Kill()
	checkGone(t, "the base directory", base)
	checkEmpty(t, "the relative TempDir", filepath.Join(first, "rel"))
	checkEmpty(t, "the working directory during the launch", second)
}

// TestClientConfigRunnerWaitTwice runs the runner of ClientConfig's
// configuration directly: a second Wait returns an error, as exec.Cmd's
// does, and does not panic.
func TestClientConfigRunnerWaitTwice(t *testing.T) {
	buildPlugins(t)
	cfg, cleanup, err := ClientConfig(Launch{Path: testPlugins.v1, SHA256: testPlugins.sum1, Config: writeConfig(t, pluginConfig{}), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	socketDir := filepath.Join(cfg.UnixSocketConfig.TempDir, "plugin-dir")
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := cfg.RunnerFunc(quiet, exec.Command(testPlugins.v1), socketDir)
	if err != nil {
		t.Fatal(err)
	}
	// Without go-plugin's handshake variables, the plugin exits at once.
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, r.Stdout())
	go io.Copy(io.Discard, r.Stderr())
	r.Wait(context.Background())
	if err := r.Wait(context.Background()); err == nil {
		t.Error("a second Wait returned nil, want exec's error")
	}
}

// TestLauncherAbortBeforeStart aborts a Supervisor's launch before its
// runner starts, once before go-plugin makes the runner and once after:
// Start returns errLaunchAborted at once, with no SHA-256 check and no
// process, which would have created the plugin's marker file.
func TestLauncherAbortBeforeStart(t *testing.T) {
	buildPlugins(t)
	for _, early := range []bool{true, false} {
		marker := filepath.Join(t.TempDir(), "started")
		cfg, cleanup, proc, err := clientConfig(Launch{Path: testPlugins.v1, SHA256: testPlugins.sum1, Config: writeConfig(t, pluginConfig{Marker: marker}), Logger: quiet}, true)
		if err != nil {
			t.Fatal(err)
		}
		socketDir := filepath.Join(cfg.UnixSocketConfig.TempDir, "plugin-dir")
		if err := os.Mkdir(socketDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if early {
			proc.abort()
		}
		r, err := cfg.RunnerFunc(quiet, exec.Command(testPlugins.v1), socketDir)
		if err != nil {
			t.Fatal(err)
		}
		if !early {
			proc.abort()
		}
		l := r.(*launcher)
		checked, spawned := false, false
		l.afterCheck = func() { checked = true }
		l.afterSpawn = func(int) { spawned = true }
		if err := l.Start(context.Background()); !errors.Is(err, errLaunchAborted) {
			t.Errorf("abort before the runner was made: %v: Start = %v, want errLaunchAborted", early, err)
		}
		if checked || spawned {
			t.Errorf("abort before the runner was made: %v: the runner checked the executable: %v, started a process: %v", early, checked, spawned)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("abort before the runner was made: %v: the plugin started", early)
		}
		if proc.exited() != nil {
			t.Errorf("abort before the runner was made: %v: the launch reports a process", early)
		}
		cleanup()
	}
}

// TestClientConfigRefusals checks the Launch fields that ClientConfig
// refuses before it creates anything.
func TestClientConfigRefusals(t *testing.T) {
	sum := hex.EncodeToString(make([]byte, sha256.Size))
	long := filepath.Join(t.TempDir(), "a-directory-name-that-makes-the-socket-paths-too-long-for-the-platform")
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		l    Launch
	}{
		{"no path", Launch{SHA256: sum, Config: "c"}},
		{"no config", Launch{Path: "p", SHA256: sum}},
		{"no digest", Launch{Path: "p", Config: "c"}},
		{"a short digest", Launch{Path: "p", SHA256: sum[:62], Config: "c"}},
		{"a digest that is not hexadecimal", Launch{Path: "p", SHA256: "z" + sum[1:], Config: "c"}},
		{"a negative message limit", Launch{Path: "p", SHA256: sum, Config: "c", MaxMessageBytes: -1}},
		{"a negative reply margin", Launch{Path: "p", SHA256: sum, Config: "c", ReplyMargin: -time.Second}},
		{"socket paths over the limit", Launch{Path: "p", SHA256: sum, Config: "c", TempDir: long}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ClientConfig(tc.l); err == nil {
				t.Error("ClientConfig accepted the launch")
			}
		})
	}
	checkEmpty(t, "the TempDir of the refused launch", long)
}

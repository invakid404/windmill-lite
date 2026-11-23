//go:build unix

package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

// newLaunch returns go-plugin's client for l, killed when the test ends,
// and the base directory of its configuration.
func newLaunch(t *testing.T, l Launch) (*goplugin.Client, string) {
	t.Helper()
	if l.Logger == nil {
		l.Logger = quiet
	}
	cfg, err := ClientConfig(l)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UnixSocketConfig == nil || cfg.UnixSocketConfig.TempDir == "" {
		t.Fatal("ClientConfig gave go-plugin no base directory for its sockets")
	}
	client := goplugin.NewClient(cfg)
	t.Cleanup(client.Kill)
	return client, cfg.UnixSocketConfig.TempDir
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

// TestVersionNegotiationRealHandshake launches two real plugin executables
// through ClientConfig, which lists only Runner version 1: go-plugin's
// handshake refuses the one that serves only version 2, and dispenses from
// the one that serves version 1.
func TestVersionNegotiationRealHandshake(t *testing.T) {
	buildPlugins(t)
	ctx := context.Background()

	v2, _ := newLaunch(t, Launch{Path: testPlugins.v2, SHA256: testPlugins.sum2, Config: writeConfig(t, pluginConfig{})})
	if _, err := v2.Client(); err == nil {
		t.Fatal("the version 2 plugin connected; want go-plugin's handshake to refuse it")
	}

	v1, _ := newLaunch(t, Launch{Path: testPlugins.v1, SHA256: testPlugins.sum1, Config: writeConfig(t, pluginConfig{})})
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
		client, base := newLaunch(t, Launch{
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
		client, base := newLaunch(t, Launch{
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

// TestClientConfigSocketPlacement launches the test plugin, which serves
// with umask 0077 as the engine does: its socket and the host's broker
// listener lie in a directory of mode 0700 in the base directory of mode
// 0700, also when the host's umask is 0277, and both directories are gone
// after Kill and after a failed launch.
func TestClientConfigSocketPlacement(t *testing.T) {
	buildPlugins(t)

	// The base directory is private and writable whatever the host's umask:
	// under 0277, a directory made with mode 0700 would be 0500.
	l := Launch{Path: testPlugins.v1, SHA256: testPlugins.sum1, Config: writeConfig(t, pluginConfig{})}
	old := syscall.Umask(0o277)
	client, base := func() (*goplugin.Client, string) {
		defer syscall.Umask(old)
		return newLaunch(t, l)
	}()
	addr, err := client.Start()
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

	// The host's side of one broker connection listens in the same private
	// directory.
	c := dispense(t, client)
	go c.broker.AcceptAndServe(c.broker.NextId(), func(opts []grpc.ServerOption) *grpc.Server { return grpc.NewServer(opts...) })
	var listener string
	for deadline := time.Now().Add(10 * time.Second); listener == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		entries, err := os.ReadDir(sockDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if p := filepath.Join(sockDir, e.Name()); p != sock && e.Type()&fs.ModeSocket != 0 {
				listener = p
			}
		}
	}
	if listener == "" {
		t.Fatalf("no broker listener appeared in %s", sockDir)
	}

	client.Kill()
	checkGone(t, "the socket directory", sockDir)
	checkGone(t, "the base directory", base)

	// A launch that fails after its process started: go-plugin's handshake
	// refuses a plugin of another version.
	marker := filepath.Join(t.TempDir(), "started")
	failed, failedBase := newLaunch(t, Launch{Path: testPlugins.v2, SHA256: testPlugins.sum2, Config: writeConfig(t, pluginConfig{Marker: marker})})
	if _, err := failed.Start(); err == nil {
		t.Fatal("the version 2 plugin started")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the failed launch started no process: %v", err)
	}
	waitGone(t, "the base directory of the failed launch", failedBase)
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
			if _, err := ClientConfig(tc.l); err == nil {
				t.Error("ClientConfig accepted the launch")
			}
		})
	}
	entries, err := os.ReadDir(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused launch left %d entries in its TempDir", len(entries))
	}
}

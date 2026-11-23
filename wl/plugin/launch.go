package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/hashicorp/go-plugin/runner"
	"google.golang.org/grpc"
)

// DefaultMaxMessageBytes is the largest gRPC message in either direction
// when Launch.MaxMessageBytes is zero: 536870912 bytes (512 MiB), the
// harness protocol's limit on a message.
const DefaultMaxMessageBytes = 512 << 20

// ErrChecksumMismatch is the error of a launch whose executable does not
// have the pinned SHA-256. The launch starts no process.
var ErrChecksumMismatch = errors.New("wl/plugin: the executable does not have the pinned SHA-256")

// maxSocketPath is the longest Unix socket path that every supported
// platform accepts: macOS refuses one of 104 bytes or more.
const maxSocketPath = 103

// socketPathSlack is the length that go-plugin adds to the base directory
// in the path of each socket: "/plugin-dir" and up to 10 digits, then
// "/plugin" and up to 10 digits.
const socketPathSlack = len("/plugin-dir") + 10 + len("/plugin") + 10

// Launch is what ClientConfig needs to start the engine as a plugin.
type Launch struct {
	// Path is the engine executable.
	Path string
	// SHA256 is the SHA-256 that the executable must have, in hexadecimal.
	// The launch hashes the file just before it starts it, and refuses
	// with ErrChecksumMismatch before any process starts. The host keeps
	// the file where no one else can change it between the two.
	SHA256 string
	// Config is the path of the plugin configuration file. The engine
	// starts as `<Path> plugin -config <Config>`.
	Config string
	// Env is the engine's literal environment, as "KEY=value" entries: the
	// engine sees no other variable of the host's environment. go-plugin's
	// handshake variables come after these entries, so a handshake
	// variable wins over an entry of the same name.
	Env []string
	// TempDir is where ClientConfig creates the launch's private base
	// directory. Empty means os.TempDir().
	TempDir string
	// MaxMessageBytes is the largest gRPC message that the host sends or
	// receives. Zero means DefaultMaxMessageBytes. The engine's
	// configuration sets its own side.
	MaxMessageBytes int
	// ReplyMargin is the Client's RunnerPlugin.ReplyMargin.
	ReplyMargin time.Duration
	// StartTimeout bounds the engine's start, until its handshake line.
	// Zero means go-plugin's default, 1 minute.
	StartTimeout time.Duration
	// Logger receives go-plugin's log and the engine's standard error.
	// Nil means go-plugin's default logger.
	Logger hclog.Logger
}

// ClientConfig returns go-plugin's client configuration for one launch of
// the engine, for go-plugin's NewClient. The configuration:
//
//   - starts the engine through a runner that checks the executable's
//     SHA-256 first (go-plugin's SecureConfig cannot, since a runner leaves
//     the command's path empty);
//   - gives the engine Launch.Env and go-plugin's handshake variables, and
//     none of the host's environment (go-plugin's SkipHostEnv), with an
//     empty standard input;
//   - speaks gRPC only, with Launch.MaxMessageBytes as the message limit;
//   - creates a fresh private base directory, of mode 0700, in which
//     go-plugin makes a directory of mode 0700 for the engine's socket and
//     both sides' broker sockets.
//
// The launch removes the base directory once the engine has exited, or
// when it fails to start; go-plugin's Kill returns after that. A
// configuration that go-plugin never starts leaves its empty base
// directory, UnixSocketConfig.TempDir, to the host. Use each configuration
// for one launch.
func ClientConfig(l Launch) (*goplugin.ClientConfig, error) {
	if l.Path == "" {
		return nil, errors.New("wl/plugin: Launch.Path is empty")
	}
	if l.Config == "" {
		return nil, errors.New("wl/plugin: Launch.Config is empty")
	}
	sum, err := hex.DecodeString(l.SHA256)
	if err != nil || len(sum) != sha256.Size {
		return nil, fmt.Errorf("wl/plugin: Launch.SHA256 %q is not a SHA-256 in hexadecimal", l.SHA256)
	}
	if l.MaxMessageBytes < 0 {
		return nil, fmt.Errorf("wl/plugin: negative Launch.MaxMessageBytes %d", l.MaxMessageBytes)
	}
	if l.ReplyMargin < 0 {
		return nil, fmt.Errorf("wl/plugin: negative Launch.ReplyMargin %v", l.ReplyMargin)
	}
	limit := l.MaxMessageBytes
	if limit == 0 {
		limit = DefaultMaxMessageBytes
	}
	parent := l.TempDir
	if parent == "" {
		parent = os.TempDir()
	}
	base, err := os.MkdirTemp(parent, "wlp-")
	if err != nil {
		return nil, fmt.Errorf("wl/plugin: create the base directory: %w", err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		os.RemoveAll(base)
		return nil, fmt.Errorf("wl/plugin: create the base directory: %w", err)
	}
	if n := len(base) + socketPathSlack; n > maxSocketPath {
		os.RemoveAll(base)
		return nil, fmt.Errorf("wl/plugin: the base directory %s gives socket paths of up to %d bytes, over the limit of %d; set Launch.TempDir to a shorter directory", base, n, maxSocketPath)
	}
	l.Env = append([]string(nil), l.Env...)
	return &goplugin.ClientConfig{
		HandshakeConfig:  Handshake,
		VersionedPlugins: VersionedPlugins(&RunnerPlugin{ReplyMargin: l.ReplyMargin}),
		RunnerFunc: func(_ hclog.Logger, cmd *exec.Cmd, _ string) (runner.Runner, error) {
			env := append(append([]string(nil), l.Env...), cmd.Env...)
			return &launcher{path: l.Path, sum: sum, config: l.Config, env: env, base: base}, nil
		},
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		SkipHostEnv:      true,
		UnixSocketConfig: &goplugin.UnixSocketConfig{TempDir: base},
		GRPCDialOptions: []grpc.DialOption{
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(limit), grpc.MaxCallSendMsgSize(limit)),
		},
		StartTimeout: l.StartTimeout,
		Logger:       l.Logger,
	}, nil
}

// launcher is the runner.Runner of one launch: it checks the executable's
// SHA-256, starts it with its configuration file and the launch's
// environment, and removes the base directory when the process has exited
// or failed to start.
type launcher struct {
	path   string
	sum    []byte
	config string
	env    []string
	base   string

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
	// proc is the engine's process, through which Kill stops it.
	proc *os.Process
}

var _ runner.Runner = (*launcher)(nil)

// Start checks the executable and starts it. It removes the base directory
// when it fails.
func (r *launcher) Start(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			os.RemoveAll(r.base)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	got, err := fileSHA256(r.path)
	if err != nil {
		return fmt.Errorf("wl/plugin: hash %s: %w", r.path, err)
	}
	if !bytes.Equal(got, r.sum) {
		return fmt.Errorf("%w: %s has SHA-256 %x, want %x", ErrChecksumMismatch, r.path, got, r.sum)
	}
	cmd := exec.Command(r.path, "plugin", "-config", r.config)
	cmd.Env = r.env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("wl/plugin: start %s: %w", r.path, err)
	}
	r.mu.Lock()
	r.cmd, r.stdout, r.stderr, r.proc = cmd, stdout, stderr, cmd.Process
	r.mu.Unlock()
	return nil
}

func fileSHA256(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// Wait waits for the process to exit, then removes the base directory.
func (r *launcher) Wait(context.Context) error {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd == nil {
		return errors.New("wl/plugin: the engine was not started")
	}
	err := cmd.Wait()
	os.RemoveAll(r.base)
	return err
}

// Kill sends the process SIGKILL. Wait then reaps it.
func (r *launcher) Kill(context.Context) error {
	r.mu.Lock()
	proc := r.proc
	r.mu.Unlock()
	if proc == nil {
		return nil
	}
	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (r *launcher) Stdout() io.ReadCloser {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stdout
}

func (r *launcher) Stderr() io.ReadCloser {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stderr
}

// Diagnose adds nothing to go-plugin's own report of a failed start.
func (r *launcher) Diagnose(context.Context) string { return "" }

// Name is the executable's path.
func (r *launcher) Name() string { return r.path }

// ID is the process id, or "" before the process starts.
func (r *launcher) ID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil {
		return ""
	}
	return strconv.Itoa(r.proc.Pid)
}

// PluginToHost keeps an address as it is: the engine runs on the host.
func (r *launcher) PluginToHost(network, addr string) (string, string, error) {
	return network, addr, nil
}

// HostToPlugin keeps an address as it is: the engine runs on the host.
func (r *launcher) HostToPlugin(network, addr string) (string, string, error) {
	return network, addr, nil
}

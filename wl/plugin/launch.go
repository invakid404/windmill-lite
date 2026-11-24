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
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
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
	// Path is the engine executable. ClientConfig resolves a relative
	// path against the current directory, once, and the launch both hashes
	// and starts that absolute path: it never searches PATH.
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
	// directory; ClientConfig resolves a relative one against the current
	// directory. Empty means os.TempDir().
	TempDir string
	// MaxMessageBytes is the largest gRPC message that the host sends or
	// receives. Zero means DefaultMaxMessageBytes. The engine's
	// configuration sets its own side.
	MaxMessageBytes int
	// ReplyMargin is the Client's RunnerPlugin.ReplyMargin.
	ReplyMargin time.Duration
	// StartTimeout is go-plugin's start timeout, which applies to two
	// intervals one after the other. The first is the check of the
	// executable's SHA-256 and the start of its process: the runner checks
	// the timeout between reads of the file, just before it starts the
	// process and just after, and when it has ended by then the runner
	// leaves no process running. The second is go-plugin's wait for the
	// engine's handshake line. Zero means go-plugin's default, 1 minute.
	StartTimeout time.Duration
	// Logger receives go-plugin's log and the engine's standard error.
	// Nil means go-plugin's default logger.
	Logger hclog.Logger
}

// ClientConfig returns go-plugin's client configuration for one launch of
// the engine, for go-plugin's NewClient, and the launch's cleanup. The
// configuration:
//
//   - starts the engine through a runner that checks the executable's
//     SHA-256 first (go-plugin's SecureConfig cannot, since a runner leaves
//     the command's path empty);
//   - gives the engine Launch.Env and go-plugin's handshake variables, and
//     none of the host's environment (go-plugin's SkipHostEnv), with an
//     empty standard input;
//   - speaks gRPC only, with Launch.MaxMessageBytes as the message limit;
//   - creates a fresh private base directory, of mode 0700, in which
//     go-plugin makes the directory for the engine's socket and both sides'
//     broker sockets, which the runner sets to mode 0700 before the engine
//     starts.
//
// The launch removes the base directory once the engine has exited, or
// when the runner fails to start it; go-plugin's Kill returns after that.
// cleanup removes the base directory in every other case too, such as a
// failure of go-plugin's Start before it calls the runner, or a
// configuration that is never started. Call it once go-plugin's Kill has
// returned or its Start has failed. The base directory is removed once:
// after a removal by the runner or by cleanup, cleanup does nothing, so it
// never removes a later directory of the same name; after a failed
// removal, cleanup tries again. Use each configuration for one launch.
func ClientConfig(l Launch) (cfg *goplugin.ClientConfig, cleanup func() error, err error) {
	cfg, cleanup, _, err = clientConfig(l)
	return cfg, cleanup, err
}

// check returns the SHA-256 that l pins, or why ClientConfig refuses l.
func (l *Launch) check() ([]byte, error) {
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
	return sum, nil
}

// clientConfig is ClientConfig, and also returns the launch's engine
// process, through which a Supervisor stops the engine and learns when it
// has exited.
func clientConfig(l Launch) (*goplugin.ClientConfig, func() error, *engineProcess, error) {
	sum, err := l.check()
	if err != nil {
		return nil, nil, nil, err
	}
	limit := l.MaxMessageBytes
	if limit == 0 {
		limit = DefaultMaxMessageBytes
	}
	path, err := filepath.Abs(l.Path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wl/plugin: resolve Launch.Path: %w", err)
	}
	parent := l.TempDir
	if parent == "" {
		parent = os.TempDir()
	}
	if parent, err = filepath.Abs(parent); err != nil {
		return nil, nil, nil, fmt.Errorf("wl/plugin: resolve Launch.TempDir: %w", err)
	}
	base, err := os.MkdirTemp(parent, "wlp-")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wl/plugin: create the base directory: %w", err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		os.RemoveAll(base)
		return nil, nil, nil, fmt.Errorf("wl/plugin: create the base directory: %w", err)
	}
	if n := len(base) + socketPathSlack; n > maxSocketPath {
		os.RemoveAll(base)
		return nil, nil, nil, fmt.Errorf("wl/plugin: the base directory %s gives socket paths of up to %d bytes, over the limit of %d; set Launch.TempDir to a shorter directory", base, n, maxSocketPath)
	}
	env := append([]string(nil), l.Env...)
	dir := &baseDir{path: base}
	proc := &engineProcess{}
	return &goplugin.ClientConfig{
		HandshakeConfig:  Handshake,
		VersionedPlugins: VersionedPlugins(&RunnerPlugin{ReplyMargin: l.ReplyMargin}),
		RunnerFunc: func(_ hclog.Logger, cmd *exec.Cmd, socketDir string) (runner.Runner, error) {
			// go-plugin made socketDir with mode 0700 less the host's
			// umask, which may leave the engine unable to create its socket.
			if err := os.Chmod(socketDir, 0o700); err != nil {
				dir.remove()
				return nil, fmt.Errorf("wl/plugin: make the socket directory private: %w", err)
			}
			r := &launcher{path: path, sum: sum, config: l.Config, env: append(append([]string(nil), env...), cmd.Env...), base: dir, exited: make(chan struct{})}
			proc.set(r)
			return r, nil
		},
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		SkipHostEnv:      true,
		UnixSocketConfig: &goplugin.UnixSocketConfig{TempDir: base},
		GRPCDialOptions: []grpc.DialOption{
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(limit), grpc.MaxCallSendMsgSize(limit)),
		},
		StartTimeout: l.StartTimeout,
		Logger:       l.Logger,
	}, dir.remove, proc, nil
}

// engineProcess is a launch's engine process, as its runner holds it.
// go-plugin makes the runner when its Start begins, so a launch that fails
// before that has no runner.
type engineProcess struct {
	mu sync.Mutex
	r  *launcher
}

func (p *engineProcess) set(r *launcher) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.r = r
}

// runner returns the launch's runner, or nil before go-plugin made it.
func (p *engineProcess) runner() *launcher {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.r
}

// exited returns a channel that is closed once the engine's process has
// exited and been reaped, or nil when no process started. Once go-plugin's
// Start has returned, whether a process started no longer changes.
func (p *engineProcess) exited() <-chan struct{} {
	r := p.runner()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil {
		return nil
	}
	return r.exited
}

// baseDir is a launch's private base directory. The runner and the
// cleanup share it, so that it is removed once: after a removal succeeds,
// a later directory of the same name is no longer the launch's.
type baseDir struct {
	path    string
	mu      sync.Mutex
	removed bool
}

// remove removes the directory, unless it was removed before.
func (d *baseDir) remove() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.removed {
		return nil
	}
	if err := os.RemoveAll(d.path); err != nil {
		return err
	}
	d.removed = true
	return nil
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
	base   *baseDir

	// afterCheck and afterSpawn, when set, are called after the SHA-256
	// check and right after the process has started. Tests use them to end
	// the start context between the steps; they are nil otherwise.
	afterCheck func()
	afterSpawn func(pid int)

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdout *os.File
	stderr *os.File
	// proc is the engine's process, through which Kill stops it.
	proc *os.Process
	// exited, when not nil, is closed once Wait has reaped the process.
	exited chan struct{}
}

var _ runner.Runner = (*launcher)(nil)

// Start checks the executable and starts it, unless ctx, go-plugin's start
// context, has ended: it checks ctx between reads of the file, just before
// it starts the process and just after, and it kills and reaps a process
// that started as ctx ended. It removes the base directory when it fails.
func (r *launcher) Start(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			r.base.remove()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	got, err := fileSHA256(ctx, r.path)
	if err != nil {
		return fmt.Errorf("wl/plugin: hash %s: %w", r.path, err)
	}
	if !bytes.Equal(got, r.sum) {
		return fmt.Errorf("%w: %s has SHA-256 %x, want %x", ErrChecksumMismatch, r.path, got, r.sum)
	}
	if r.afterCheck != nil {
		r.afterCheck()
	}
	// The runner makes the pipes itself, so that it can close both ends of
	// each when the start fails; exec.Cmd closes them only in Start and Wait.
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		return err
	}
	stderr, stderrW, err := os.Pipe()
	if err != nil {
		stdout.Close()
		stdoutW.Close()
		return err
	}
	closeAll := func() {
		for _, f := range []*os.File{stdout, stdoutW, stderr, stderrW} {
			f.Close()
		}
	}
	cmd := exec.Command(r.path, "plugin", "-config", r.config)
	cmd.Env = r.env
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW
	if err := ctx.Err(); err != nil {
		closeAll()
		return err
	}
	if err := cmd.Start(); err != nil {
		closeAll()
		return fmt.Errorf("wl/plugin: start %s: %w", r.path, err)
	}
	// The process holds its own copies of the write ends.
	stdoutW.Close()
	stderrW.Close()
	if r.afterSpawn != nil {
		r.afterSpawn(cmd.Process.Pid)
	}
	if err := ctx.Err(); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		stdout.Close()
		stderr.Close()
		return err
	}
	r.mu.Lock()
	r.cmd, r.stdout, r.stderr, r.proc = cmd, stdout, stderr, cmd.Process
	r.mu.Unlock()
	return nil
}

// hashChunk is how much of the executable the runner reads between two
// checks of the start context.
const hashChunk = 1 << 20

// fileSHA256 returns the SHA-256 of the regular file at path, or ctx's
// error once ctx has ended, which it checks before each read of hashChunk
// bytes. It opens the file without blocking, so a named pipe neither blocks
// the open nor passes as an executable.
func fileSHA256(ctx context.Context, path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (mode %v)", path, fi.Mode())
	}
	h := sha256.New()
	buf := make([]byte, hashChunk)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			return h.Sum(nil), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// Wait waits for the process to exit, then closes the pipes, which
// go-plugin has read to their end, and removes the base directory.
func (r *launcher) Wait(context.Context) error {
	r.mu.Lock()
	cmd, stdout, stderr := r.cmd, r.stdout, r.stderr
	r.mu.Unlock()
	if cmd == nil {
		return errors.New("wl/plugin: the engine was not started")
	}
	err := cmd.Wait()
	stdout.Close()
	stderr.Close()
	r.base.remove()
	if r.exited != nil {
		close(r.exited)
	}
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

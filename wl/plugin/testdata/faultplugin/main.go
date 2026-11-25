//go:build unix

// Command faultplugin is a stand-in for the engine in the supervisor's
// tests: a go-plugin plugin that serves a fake Runner and answers health
// for windmill_lite.v1.Runner, and that misbehaves as its test asks. The
// tests build it with `go build` into their own temporary directory.
//
// It starts as the engine does, `faultplugin plugin -config <file>`.
// `faultplugin sleep` only sleeps, as a process of the test's own beside
// it, and `faultplugin hold <pid> <file>` sleeps until the process pid has
// gone, the file exists or the file's directory has gone.
// The configuration file is JSON:
//
//	{"dir": "<path>", "build_info": {...}, "stall_build_info": false,
//	 "ignore_shutdown": false, "hold_runs": false, "hold_handshake": false,
//	 "descendant": false}
//
// The process talks with its test through files in "dir". A file that it
// writes with content appears whole, by a rename:
//
//   - At its start it appends its process id to "pids", after it writes
//     into "seen-<pid>" the ids of the processes listed there before that
//     still exist, one per line, and its environment into "env-<pid>",
//     one variable per line. Then, if "fail" exists, it exits with status
//     1 before go-plugin's handshake, and with "hold_handshake" it never
//     writes the handshake line.
//   - With "descendant", it starts `faultplugin hold <its parent>
//     <dir>/descendant-stop` with its own standard output and error, so
//     that process holds the pipes that go-plugin reads, and writes that
//     process's id into "descendant-<pid>". It reaps that process once it
//     ends, so no zombie of it stays behind while the plugin runs.
//   - Health of any service answers the status named in "health-<pid>",
//     such as NOT_SERVING, or else in "health", or else SERVING, and then
//     creates "answered-<status>-<pid>". The status STALL creates
//     "stalled-<pid>" and answers only when the call ends. Each check first
//     appends a line to "checks-<pid>": the milliseconds left until the
//     call's deadline, or "none", a space and the status.
//   - GetBuildInfo writes the milliseconds left until its deadline, or
//     "none", into "deadline-buildinfo-<pid>", creates "buildinfo-<pid>" and
//     answers "build_info"; with "stall_build_info" it never answers, until
//     its call ends.
//   - With "ignore_shutdown", go-plugin's shutdown call creates
//     "shutdown-<pid>" and never answers, and the server goes on.
//   - Run creates "run-<pid>"; with "hold_runs" it answers only once
//     "release" exists. Its result is {"pid": <pid>}.
//
// The plugin and `sleep` exit once their parent has gone, and `hold` once
// its process or its file's directory has gone, so a test that fails
// before it stops them leaves none behind.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

type buildInfo struct {
	BundleDigest  string            `json:"bundle_digest"`
	EngineVersion string            `json:"engine_version"`
	BunVersion    string            `json:"bun_version"`
	ConfigDigest  string            `json:"config_digest"`
	Labels        map[string]string `json:"labels"`
}

type config struct {
	Dir            string    `json:"dir"`
	BuildInfo      buildInfo `json:"build_info"`
	StallBuildInfo bool      `json:"stall_build_info"`
	IgnoreShutdown bool      `json:"ignore_shutdown"`
	HoldRuns       bool      `json:"hold_runs"`
	HoldHandshake  bool      `json:"hold_handshake"`
	Descendant     bool      `json:"descendant"`
}

type runner struct {
	plugin.UnimplementedRunnerServer
	cfg config
	pid string
}

// touch creates the empty file name in the test's directory.
func (r *runner) touch(name string) {
	if err := os.WriteFile(filepath.Join(r.cfg.Dir, name), nil, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "faultplugin:", err)
	}
}

// publish writes s into the file name of the test's directory, whole.
func (r *runner) publish(name, s string) error {
	path := filepath.Join(r.cfg.Dir, name)
	if err := os.WriteFile(path+".tmp", []byte(s), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// exists reports whether the file name exists in the test's directory.
func (r *runner) exists(name string) bool {
	_, err := os.Stat(filepath.Join(r.cfg.Dir, name))
	return err == nil
}

// left is the time left until the deadline of ctx, in milliseconds, or
// "none".
func left(ctx context.Context) string {
	if dl, ok := ctx.Deadline(); ok {
		return strconv.FormatInt(time.Until(dl).Milliseconds(), 10)
	}
	return "none"
}

// logCheck appends the deadline of a health check and its status to
// "checks-<pid>", in one write.
func (r *runner) logCheck(ctx context.Context, st string) {
	f, err := os.OpenFile(filepath.Join(r.cfg.Dir, "checks-"+r.pid), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.WriteString(left(ctx) + " " + st + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "faultplugin:", err)
	}
}

func (r *runner) GetBuildInfo(ctx context.Context, _ *plugin.GetBuildInfoRequest) (*plugin.BuildInfo, error) {
	if err := r.publish("deadline-buildinfo-"+r.pid, left(ctx)); err != nil {
		fmt.Fprintln(os.Stderr, "faultplugin:", err)
	}
	r.touch("buildinfo-" + r.pid)
	if r.cfg.StallBuildInfo {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	b := r.cfg.BuildInfo
	return &plugin.BuildInfo{
		BundleDigest: b.BundleDigest, EngineVersion: b.EngineVersion, BunVersion: b.BunVersion,
		ConfigDigest: b.ConfigDigest, Labels: b.Labels, ProtocolVersion: plugin.ProtocolVersion,
	}, nil
}

func (r *runner) Run(ctx context.Context, req *plugin.RunRequest) (*plugin.RunResult, error) {
	r.touch("run-" + r.pid)
	for r.cfg.HoldRuns && !r.exists("release") {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return &plugin.RunResult{Id: req.GetId(), Status: "success", Result: []byte(`{"pid":` + r.pid + `}`)}, nil
}

// status is the health status that the test's files name.
func (r *runner) status() (string, error) {
	for _, name := range []string{"health-" + r.pid, "health"} {
		b, err := os.ReadFile(filepath.Join(r.cfg.Dir, name))
		if err != nil {
			continue
		}
		st := strings.TrimSpace(string(b))
		if _, ok := healthpb.HealthCheckResponse_ServingStatus_value[st]; !ok && st != "STALL" {
			return "", fmt.Errorf("%s names no status: %q", name, b)
		}
		return st, nil
	}
	return "SERVING", nil
}

// intercept answers health from the files of the test's directory, and
// holds go-plugin's shutdown call when the configuration says so.
func (r *runner) intercept(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	switch info.FullMethod {
	case "/grpc.health.v1.Health/Check":
		st, err := r.status()
		if err != nil {
			return nil, err
		}
		r.logCheck(ctx, st)
		if st == "STALL" {
			r.touch("stalled-" + r.pid)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		r.touch("answered-" + st + "-" + r.pid)
		return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_ServingStatus(healthpb.HealthCheckResponse_ServingStatus_value[st])}, nil
	case "/plugin.GRPCController/Shutdown":
		if r.cfg.IgnoreShutdown {
			r.touch("shutdown-" + r.pid)
			sleep()
		}
	}
	return next(ctx, req)
}

// sleep never returns. A pending timer keeps the runtime from reporting
// a deadlock, as an empty select with no other goroutine would.
func sleep() {
	for {
		time.Sleep(time.Hour)
	}
}

// orphaned ends the process once its parent has gone, which makes the
// parent process id change.
func orphaned() {
	parent := os.Getppid()
	for {
		time.Sleep(100 * time.Millisecond)
		if os.Getppid() != parent {
			os.Exit(3)
		}
	}
}

// hold sleeps until no process of id pid is left, the file stop exists or
// its directory has gone, then exits.
func hold(pid int, stop string) {
	for syscall.Kill(pid, 0) == nil {
		if _, err := os.Stat(stop); err == nil {
			break
		}
		if _, err := os.Stat(filepath.Dir(stop)); err != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	os.Exit(0)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "faultplugin:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) == 4 && os.Args[1] == "hold" {
		pid, err := strconv.Atoi(os.Args[2])
		if err != nil {
			return err
		}
		hold(pid, os.Args[3])
	}
	go orphaned()
	if len(os.Args) == 2 && os.Args[1] == "sleep" {
		sleep()
	}
	if len(os.Args) < 2 || os.Args[1] != "plugin" {
		return errors.New("usage: faultplugin plugin -config <file> | faultplugin sleep | faultplugin hold <pid> <file>")
	}
	fs := flag.NewFlagSet("plugin", flag.ContinueOnError)
	path := fs.String("config", "", "the configuration file")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	r := &runner{cfg: cfg, pid: strconv.Itoa(os.Getpid())}
	if err := r.record(); err != nil {
		return err
	}
	if r.exists("fail") {
		return errors.New("the test asked this launch to fail")
	}
	if cfg.Descendant {
		if err := r.descend(); err != nil {
			return err
		}
	}
	if cfg.HoldHandshake {
		sleep()
	}
	syscall.Umask(0o077)
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig:  plugin.Handshake,
		VersionedPlugins: plugin.VersionedPlugins(&plugin.RunnerPlugin{Runner: r}),
		GRPCServer: func(o []grpc.ServerOption) *grpc.Server {
			return grpc.NewServer(append(o, grpc.ChainUnaryInterceptor(r.intercept))...)
		},
		Logger: hclog.New(&hclog.LoggerOptions{Output: os.Stderr, Level: hclog.Error}),
	})
	return nil
}

// record writes which processes of the earlier starts still exist into
// "seen-<pid>" and the environment into "env-<pid>", then appends this
// process's id to "pids".
func (r *runner) record() error {
	pids := filepath.Join(r.cfg.Dir, "pids")
	b, err := os.ReadFile(pids)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var alive []string
	for _, f := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			return fmt.Errorf("pids: %w", err)
		}
		if syscall.Kill(pid, 0) == nil {
			alive = append(alive, f)
		}
	}
	if err := r.publish("seen-"+r.pid, strings.Join(alive, "\n")); err != nil {
		return err
	}
	if err := r.publish("env-"+r.pid, strings.Join(os.Environ(), "\n")); err != nil {
		return err
	}
	f, err := os.OpenFile(pids, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, r.pid); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// descend starts `faultplugin hold <parent> <dir>/descendant-stop` on this
// process's standard output and error, writes its process id into
// "descendant-<pid>", and reaps it once it ends. Unreaped, the ended
// process would stay a zombie while this one runs, which a test's
// kill(pid, 0) still finds.
func (r *runner) descend() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "hold", strconv.Itoa(os.Getppid()), filepath.Join(r.cfg.Dir, "descendant-stop"))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return r.publish("descendant-"+r.pid, strconv.Itoa(cmd.Process.Pid))
}

//go:build unix

// Command faultplugin is a stand-in for the engine in the supervisor's
// tests: a go-plugin plugin that serves a fake Runner and answers health
// for windmill_lite.v1.Runner, and that misbehaves as its test asks. The
// tests build it with `go build` into their own temporary directory.
//
// It starts as the engine does, `faultplugin plugin -config <file>`, and
// `faultplugin sleep` only sleeps, as a process of the test's own beside
// it. The configuration file is JSON:
//
//	{"dir": "<path>", "build_info": {...}, "stall_build_info": false,
//	 "ignore_shutdown": false, "hold_runs": false}
//
// The process talks with its test through files in "dir":
//
//   - At its start it appends its process id to "pids", after it writes
//     into "seen-<pid>" the ids of the processes listed there before that
//     still exist, one per line. Then, if "fail" exists, it exits with
//     status 1 before go-plugin's handshake.
//   - Health of any service creates "checked-<pid>" and answers the
//     status named in "health-<pid>", such as NOT_SERVING, or else in
//     "health", or else SERVING.
//   - GetBuildInfo creates "buildinfo-<pid>" and answers "build_info"; with
//     "stall_build_info" it never answers, until its call ends.
//   - With "ignore_shutdown", go-plugin's shutdown call creates
//     "shutdown-<pid>" and never answers, and the server goes on.
//   - Run creates "run-<pid>"; with "hold_runs" it answers only once
//     "release" exists. Its result is {"pid": <pid>}.
//
// In both forms the process exits once its parent has gone, so a test
// that fails before it stops the process leaves none behind.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
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
}

type runner struct {
	plugin.UnimplementedRunnerServer
	cfg config
	pid string
}

// touch creates the file name in the test's directory.
func (r *runner) touch(name string) {
	if err := os.WriteFile(filepath.Join(r.cfg.Dir, name), nil, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "faultplugin:", err)
	}
}

// exists reports whether the file name exists in the test's directory.
func (r *runner) exists(name string) bool {
	_, err := os.Stat(filepath.Join(r.cfg.Dir, name))
	return err == nil
}

func (r *runner) GetBuildInfo(ctx context.Context, _ *plugin.GetBuildInfoRequest) (*plugin.BuildInfo, error) {
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

// intercept answers health from the files of the test's directory, and
// holds go-plugin's shutdown call when the configuration says so.
func (r *runner) intercept(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	switch info.FullMethod {
	case "/grpc.health.v1.Health/Check":
		r.touch("checked-" + r.pid)
		st := healthpb.HealthCheckResponse_SERVING
		for _, name := range []string{"health-" + r.pid, "health"} {
			b, err := os.ReadFile(filepath.Join(r.cfg.Dir, name))
			if err != nil {
				continue
			}
			v, ok := healthpb.HealthCheckResponse_ServingStatus_value[strings.TrimSpace(string(b))]
			if !ok {
				return nil, fmt.Errorf("%s names no status: %q", name, b)
			}
			st = healthpb.HealthCheckResponse_ServingStatus(v)
			break
		}
		return &healthpb.HealthCheckResponse{Status: st}, nil
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "faultplugin:", err)
		os.Exit(1)
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

func run() error {
	go orphaned()
	if len(os.Args) == 2 && os.Args[1] == "sleep" {
		sleep()
	}
	if len(os.Args) < 2 || os.Args[1] != "plugin" {
		return errors.New("usage: faultplugin plugin -config <file> | faultplugin sleep")
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
// "seen-<pid>", then appends this process's id to "pids".
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
	seen := filepath.Join(r.cfg.Dir, "seen-"+r.pid)
	if err := os.WriteFile(seen+".tmp", []byte(strings.Join(alive, "\n")), 0o600); err != nil {
		return err
	}
	if err := os.Rename(seen+".tmp", seen); err != nil {
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

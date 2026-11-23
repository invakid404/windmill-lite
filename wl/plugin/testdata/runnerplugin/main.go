//go:build unix

// Command runnerplugin is a stand-in for the engine in wl/plugin's launch
// tests: a go-plugin plugin that serves a fake Runner. The tests build it
// with `go build`, once for each Runner version that it serves
// (-ldflags "-X main.servedVersion=<n>").
//
// It starts as the engine does, `runnerplugin plugin -config <file>`. The
// file is JSON:
//
//	{"marker": "<path>", "env": ["NAME", ...]}
//
// With "marker", it writes that file first, so a test can tell whether the
// process started. GetBuildInfo reports the version it serves as
// protocol_version, and each listed variable of its environment that is
// set as the label "env:NAME". Like the engine, it serves with umask 0077.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// servedVersion is the one Runner version that the plugin serves.
var servedVersion = "1"

type config struct {
	Marker string   `json:"marker"`
	Env    []string `json:"env"`
}

type runner struct {
	plugin.UnimplementedRunnerServer
	version int
	env     []string
}

func (r *runner) GetBuildInfo(context.Context, *plugin.GetBuildInfoRequest) (*plugin.BuildInfo, error) {
	labels := map[string]string{}
	for _, name := range r.env {
		if v, ok := os.LookupEnv(name); ok {
			labels["env:"+name] = v
		}
	}
	return &plugin.BuildInfo{ProtocolVersion: int32(r.version), Labels: labels}, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "runnerplugin:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 || os.Args[1] != "plugin" {
		return fmt.Errorf("usage: runnerplugin plugin -config <file>")
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
	if cfg.Marker != "" {
		if err := os.WriteFile(cfg.Marker, []byte("started\n"), 0o600); err != nil {
			return err
		}
	}
	version, err := strconv.Atoi(servedVersion)
	if err != nil {
		return err
	}
	syscall.Umask(0o077)
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: plugin.Handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{
			version: {plugin.PluginName: &plugin.RunnerPlugin{Runner: &runner{version: version, env: cfg.Env}}},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
		Logger:     hclog.New(&hclog.LoggerOptions{Output: os.Stderr, Level: hclog.Error}),
	})
	return nil
}

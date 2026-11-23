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
// set as the label "env:NAME". Run with hooks dials the host's broker
// connection hooks.broker_id, calls HostHooks.Call with the hook "probe",
// and answers the reply's payload as its result. Like the engine, it
// serves with umask 0077.
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
	"google.golang.org/grpc"

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
	broker  *goplugin.GRPCBroker
}

// brokered is the Runner plugin with access to go-plugin's broker, which
// RunnerPlugin keeps to itself.
type brokered struct {
	plugin.RunnerPlugin
	r *runner
}

func (p *brokered) GRPCServer(b *goplugin.GRPCBroker, s *grpc.Server) error {
	p.r.broker = b
	return p.RunnerPlugin.GRPCServer(b, s)
}

func (r *runner) Run(ctx context.Context, req *plugin.RunRequest) (*plugin.RunResult, error) {
	hooks := req.GetHooks()
	if hooks == nil {
		return &plugin.RunResult{Id: req.GetId(), Status: "success"}, nil
	}
	conn, err := r.broker.Dial(hooks.GetBrokerId())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	reply, err := plugin.NewHostHooksClient(conn).Call(ctx, &plugin.HookCall{
		Hook:    "probe",
		Run:     req.GetId(),
		Token:   hooks.GetToken(),
		Payload: []byte(`{"from":"plugin"}`),
	})
	if err != nil {
		return nil, err
	}
	return &plugin.RunResult{Id: req.GetId(), Status: "success", Result: reply.GetPayload()}, nil
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
	r := &runner{version: version, env: cfg.Env}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: plugin.Handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{
			version: {plugin.PluginName: &brokered{RunnerPlugin: plugin.RunnerPlugin{Runner: r}, r: r}},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
		Logger:     hclog.New(&hclog.LoggerOptions{Output: os.Stderr, Level: hclog.Error}),
	})
	return nil
}

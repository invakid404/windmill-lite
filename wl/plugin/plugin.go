package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// PluginName is the name under which the engine serves the Runner plugin,
// and under which a host dispenses it.
const PluginName = "runner"

// ProtocolVersion is the version of the Runner service that this package
// speaks: the major version of windmill_lite.v1.Runner.
const ProtocolVersion = 1

// Handshake is go-plugin's handshake configuration for the engine. Its
// magic cookie checks that the engine was started as a plugin; it is not
// authentication.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  ProtocolVersion,
	MagicCookieKey:   "WINDMILL_LITE_PLUGIN",
	MagicCookieValue: "c93d2e63-d2a1-4d29-9df5-de775e3060f2",
}

// DefaultReplyMargin is the margin that Client.Run keeps between a run's
// hard deadline and the deadline of the call's context, when
// RunnerPlugin.ReplyMargin is zero.
const DefaultReplyMargin = time.Second

// VersionedPlugins returns go-plugin's plugin sets by Runner version, for
// the engine's side and the host's alike: version 1 serves p under
// PluginName. go-plugin picks the highest version that both sides list.
func VersionedPlugins(p *RunnerPlugin) map[int]goplugin.PluginSet {
	return map[int]goplugin.PluginSet{
		ProtocolVersion: {PluginName: p},
	}
}

// RunnerPlugin is the go-plugin plugin type of the Runner service. It
// speaks gRPC only: go-plugin's net/rpc protocol is refused. On the
// engine's side it registers Runner; on the host's side it returns a
// *Client.
type RunnerPlugin struct {
	goplugin.NetRPCUnsupportedPlugin

	// Runner is the engine's implementation of the Runner service. The
	// host's side leaves it nil.
	Runner RunnerServer

	// ReplyMargin is how long before the deadline of a call's context
	// Client.Run sets the run's hard deadline: the time that the engine has
	// to end the run and send its outcome before the call ends, which a
	// slow run-end hook can exceed. Zero means DefaultReplyMargin; a
	// negative margin refuses to dispense.
	ReplyMargin time.Duration
}

var _ goplugin.GRPCPlugin = (*RunnerPlugin)(nil)

// GRPCServer registers p.Runner on the engine's gRPC server.
func (p *RunnerPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	if p.Runner == nil {
		return errors.New("wl/plugin: RunnerPlugin has no Runner")
	}
	RegisterRunnerServer(s, p.Runner)
	return nil
}

// GRPCClient returns the *Client of a connection to the engine.
func (p *RunnerPlugin) GRPCClient(_ context.Context, b *goplugin.GRPCBroker, cc *grpc.ClientConn) (any, error) {
	margin := p.ReplyMargin
	switch {
	case margin < 0:
		return nil, fmt.Errorf("wl/plugin: negative ReplyMargin %v", margin)
	case margin == 0:
		margin = DefaultReplyMargin
	}
	return newClient(cc, b, margin), nil
}

// newClient returns the Client of the connection cc.
func newClient(cc grpc.ClientConnInterface, b *goplugin.GRPCBroker, margin time.Duration) *Client {
	return &Client{
		runner: NewRunnerClient(cc),
		health: healthpb.NewHealthClient(cc),
		broker: b,
		margin: margin,
		now:    time.Now,
	}
}

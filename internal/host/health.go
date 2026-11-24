package host

import (
	"context"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// State is the engine's serving state (ADR 0009, health, readiness and
// drain).
type State int

const (
	// Starting: the engine has completed go-plugin's handshake and opens
	// its runtime. Health answers NOT_SERVING, and the Runner calls answer
	// UNAVAILABLE.
	Starting State = iota
	// Serving: the runtime has opened, with its warm start. Health answers
	// SERVING, and the Runner calls run.
	Serving
)

// healthServices are the service names whose health the engine answers
// from its state: the server as a whole, go-plugin's name for its plugin,
// and the Runner service.
var healthServices = []string{"", "plugin", plugin.Runner_ServiceDesc.ServiceName}

// Gate holds the engine's state: it answers gRPC health from it, and the
// Runner calls through the Server once the engine serves. Its methods are
// safe for concurrent use.
type Gate struct {
	mu    sync.Mutex
	state State
	srv   *Server
	// changed closes when the state changes, and is then replaced.
	changed chan struct{}
}

// NewGate returns a Gate in the state Starting.
func NewGate() *Gate {
	return &Gate{changed: make(chan struct{})}
}

// Serve makes the engine serve: the Runner calls go to srv, and health
// answers SERVING, to every open Watch too.
func (g *Gate) Serve(srv *Server) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.srv, g.state = srv, Serving
	close(g.changed)
	g.changed = make(chan struct{})
}

// current returns the state, the Server once serving, and the channel that
// closes at the next change.
func (g *Gate) current() (State, *Server, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state, g.srv, g.changed
}

// healthOf is the health status of a state.
func healthOf(s State) healthpb.HealthCheckResponse_ServingStatus {
	if s == Serving {
		return healthpb.HealthCheckResponse_SERVING
	}
	return healthpb.HealthCheckResponse_NOT_SERVING
}

// known reports whether the engine answers the health of the service name.
func known(name string) bool {
	for _, s := range healthServices {
		if s == name {
			return true
		}
	}
	return false
}

// healthPrefix is the prefix of the full method names of
// grpc.health.v1.Health.
var healthPrefix = "/" + healthpb.Health_ServiceDesc.ServiceName + "/"

// ServerOptions are the options that make a gRPC server answer
// grpc.health.v1.Health from the engine's state: interceptors that answer
// Check, List and Watch, and refuse every other method of the service with
// UNIMPLEMENTED, before the server's own health service sees the call. So
// go-plugin's health server, whose service plugin is SERVING for good,
// never answers a caller.
func (g *Gate) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(g.unary), grpc.ChainStreamInterceptor(g.stream)}
}

func (g *Gate) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if !strings.HasPrefix(info.FullMethod, healthPrefix) {
		return handler(ctx, req)
	}
	state, _, _ := g.current()
	switch info.FullMethod {
	case healthpb.Health_Check_FullMethodName:
		r, ok := req.(*healthpb.HealthCheckRequest)
		if !ok {
			return nil, status.Errorf(codes.Internal, "health check request of type %T", req)
		}
		if !known(r.GetService()) {
			return nil, status.Error(codes.NotFound, "unknown service")
		}
		return &healthpb.HealthCheckResponse{Status: healthOf(state)}, nil
	case healthpb.Health_List_FullMethodName:
		out := &healthpb.HealthListResponse{Statuses: make(map[string]*healthpb.HealthCheckResponse, len(healthServices))}
		for _, s := range healthServices {
			out.Statuses[s] = &healthpb.HealthCheckResponse{Status: healthOf(state)}
		}
		return out, nil
	}
	return nil, status.Errorf(codes.Unimplemented, "method %s is not implemented", info.FullMethod)
}

func (g *Gate) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if !strings.HasPrefix(info.FullMethod, healthPrefix) {
		return handler(srv, ss)
	}
	if info.FullMethod != healthpb.Health_Watch_FullMethodName {
		return status.Errorf(codes.Unimplemented, "method %s is not implemented", info.FullMethod)
	}
	var req healthpb.HealthCheckRequest
	if err := ss.RecvMsg(&req); err != nil {
		return err
	}
	ctx := ss.Context()
	if !known(req.GetService()) {
		// As gRPC's own health server: the status SERVICE_UNKNOWN, and the
		// stream stays open.
		if err := ss.SendMsg(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVICE_UNKNOWN}); err != nil {
			return err
		}
		<-ctx.Done()
		return status.FromContextError(ctx.Err()).Err()
	}
	// Each change of status, from the current one on.
	sent := healthpb.HealthCheckResponse_UNKNOWN
	for {
		state, _, changed := g.current()
		if st := healthOf(state); st != sent {
			if err := ss.SendMsg(&healthpb.HealthCheckResponse{Status: st}); err != nil {
				return err
			}
			sent = st
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
}

// Runner returns the Runner service of the engine: while it starts, Run
// answers UNAVAILABLE with the RUN_NOT_STARTED detail, and GetBuildInfo
// and Cancel answer UNAVAILABLE; once it serves, every call goes to the
// Server.
func (g *Gate) Runner() plugin.RunnerServer {
	return gatedRunner{g: g}
}

// gatedRunner is Gate.Runner's service.
type gatedRunner struct {
	plugin.UnimplementedRunnerServer
	g *Gate
}

// errStarting is the message of a Runner call that the engine refuses while
// it starts.
const errStarting = "the engine is starting: its runtime has not opened yet"

func (r gatedRunner) GetBuildInfo(ctx context.Context, req *plugin.GetBuildInfoRequest) (*plugin.BuildInfo, error) {
	if _, srv, _ := r.g.current(); srv != nil {
		return srv.GetBuildInfo(ctx, req)
	}
	return nil, status.Error(codes.Unavailable, errStarting)
}

func (r gatedRunner) Run(ctx context.Context, req *plugin.RunRequest) (*plugin.RunResult, error) {
	if _, srv, _ := r.g.current(); srv != nil {
		return srv.Run(ctx, req)
	}
	return nil, refusal(codes.Unavailable, errStarting)
}

func (r gatedRunner) Cancel(ctx context.Context, req *plugin.CancelRequest) (*plugin.CancelResponse, error) {
	if _, srv, _ := r.g.current(); srv != nil {
		return srv.Cancel(ctx, req)
	}
	return nil, status.Error(codes.Unavailable, errStarting)
}

func (r gatedRunner) GetRun(ctx context.Context, req *plugin.GetRunRequest) (*plugin.GetRunResponse, error) {
	if _, srv, _ := r.g.current(); srv != nil {
		return srv.GetRun(ctx, req)
	}
	return nil, status.Error(codes.Unavailable, errStarting)
}

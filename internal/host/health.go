package host

import (
	"context"
	"slices"
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
	// Draining, for good: health answers NOT_SERVING, and a new Run
	// answers UNAVAILABLE (draining); the runs in flight go on, and the
	// other Runner calls still go to the Server.
	Draining
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
	srv   plugin.RunnerServer
	// history holds every state that the Gate has been in, in order, so
	// that each Watch sends each change, however close together they come.
	history []State
	// changed closes when the state changes, and is then replaced.
	changed chan struct{}
	// runs counts the Run calls that went to the Server and have not
	// returned; idle, when not nil, closes when it reaches zero.
	runs int
	idle chan struct{}
	// closed closes when Close is called, which ends every Watch.
	closed    chan struct{}
	closeOnce sync.Once
}

// NewGate returns a Gate in the state Starting.
func NewGate() *Gate {
	return &Gate{history: []State{Starting}, changed: make(chan struct{}), closed: make(chan struct{})}
}

// Serve makes the engine serve: the Runner calls go to srv, a *Server, and
// health answers SERVING, to every open Watch too. A Gate that drains
// already keeps draining, and does not take srv: a drain that began while
// the engine started answers every call UNAVAILABLE.
func (g *Gate) Serve(srv plugin.RunnerServer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == Starting {
		g.srv, g.state = srv, Serving
		g.notifyLocked()
	}
}

// Drain makes the engine drain, for good: health answers NOT_SERVING, to
// every open Watch too, and a new Run answers UNAVAILABLE (draining). The
// Run calls in flight go on.
func (g *Gate) Drain() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != Draining {
		g.state = Draining
		g.notifyLocked()
	}
}

// Idle returns a channel that is closed once no Run call that went to the
// Server is in flight: at once if none is.
func (g *Gate) Idle() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runs == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	if g.idle == nil {
		g.idle = make(chan struct{})
	}
	return g.idle
}

// Close ends every Watch, each after it has sent the current status, so
// that a server's graceful stop need not wait for them.
func (g *Gate) Close() {
	g.closeOnce.Do(func() { close(g.closed) })
}

// notifyLocked records the new state and tells every open Watch.
func (g *Gate) notifyLocked() {
	g.history = append(g.history, g.state)
	close(g.changed)
	g.changed = make(chan struct{})
}

// since returns the states from the i-th of the history on, and the
// channel that closes at the next change.
func (g *Gate) since(i int) ([]State, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.history[i:]), g.changed
}

// beginRun returns the state, and the Server once the engine serves or
// drains. While it serves, it counts a Run call in flight, which endRun
// ends.
func (g *Gate) beginRun() (State, plugin.RunnerServer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == Serving {
		g.runs++
	}
	return g.state, g.srv
}

// endRun ends a Run call that beginRun counted.
func (g *Gate) endRun() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runs--
	if g.runs == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
}

// current returns the state, the Server once serving, and the channel that
// closes at the next change.
func (g *Gate) current() (State, plugin.RunnerServer, <-chan struct{}) {
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
		// stream stays open, until Close.
		if err := ss.SendMsg(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVICE_UNKNOWN}); err != nil {
			return err
		}
		select {
		case <-g.closed:
			return nil
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
	// The current status, then each change, in order, from the Gate's
	// history: a change that comes before this stream has sent the last is
	// still sent. After Close, the changes recorded so far are the last.
	sent := healthpb.HealthCheckResponse_UNKNOWN
	states, _ := g.since(0)
	next := len(states) - 1
	for {
		states, changed := g.since(next)
		for _, st := range states {
			if h := healthOf(st); h != sent {
				if err := ss.SendMsg(&healthpb.HealthCheckResponse{Status: h}); err != nil {
					return err
				}
				sent = h
			}
		}
		next += len(states)
		select {
		case <-g.closed:
			if rest, _ := g.since(next); len(rest) > 0 {
				continue
			}
			return nil
		default:
		}
		select {
		case <-changed:
		case <-g.closed:
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
}

// Runner returns the Runner service of the engine: while it starts, Run
// answers UNAVAILABLE with the RUN_NOT_STARTED detail, and GetBuildInfo
// and Cancel answer UNAVAILABLE; once it serves, every call goes to the
// Server. While it drains, Run answers UNAVAILABLE (draining) with the
// RUN_NOT_STARTED detail, and the other calls go to the Server.
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

// errDraining is the message of a Run that the engine refuses while it
// drains; draining is host mode's code for that refusal.
const errDraining = "draining: the engine drains and takes no new runs"

func (r gatedRunner) GetBuildInfo(ctx context.Context, req *plugin.GetBuildInfoRequest) (*plugin.BuildInfo, error) {
	if _, srv, _ := r.g.current(); srv != nil {
		return srv.GetBuildInfo(ctx, req)
	}
	return nil, status.Error(codes.Unavailable, errStarting)
}

func (r gatedRunner) Run(ctx context.Context, req *plugin.RunRequest) (*plugin.RunResult, error) {
	switch state, srv := r.g.beginRun(); state {
	case Serving:
		defer r.g.endRun()
		return srv.Run(ctx, req)
	case Draining:
		return nil, refusal(codes.Unavailable, errDraining)
	}
	return nil, refusal(codes.Unavailable, errStarting)
}

func (r gatedRunner) Cancel(ctx context.Context, req *plugin.CancelRequest) (*plugin.CancelResponse, error) {
	if _, srv, _ := r.g.current(); srv != nil {
		return srv.Cancel(ctx, req)
	}
	return nil, status.Error(codes.Unavailable, errStarting)
}

// GetRun goes to the Server while the engine serves. While it drains, a
// lookup answers UNAVAILABLE without the RUN_NOT_STARTED detail: it says
// nothing about a run, which may have succeeded, and the host keeps its
// registration until the engine's generation ends.
func (r gatedRunner) GetRun(ctx context.Context, req *plugin.GetRunRequest) (*plugin.GetRunResponse, error) {
	switch state, srv, _ := r.g.current(); {
	case state == Serving:
		return srv.GetRun(ctx, req)
	case state == Draining:
		return nil, status.Error(codes.Unavailable, errDrainingLookup)
	}
	return nil, status.Error(codes.Unavailable, errStarting)
}

// ReleaseHooks goes to the Server while it serves and while it drains: a
// host that lets a broker connection go frees the engine's side of it.
func (r gatedRunner) ReleaseHooks(ctx context.Context, req *plugin.ReleaseHooksRequest) (*plugin.ReleaseHooksResponse, error) {
	if _, srv, _ := r.g.current(); srv != nil {
		return srv.ReleaseHooks(ctx, req)
	}
	return nil, status.Error(codes.Unavailable, errStarting)
}

// errDrainingLookup is the message of a GetRun that the engine refuses
// while it drains.
const errDrainingLookup = "draining: the engine drains and answers no lookup"

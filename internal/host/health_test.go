package host

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// watchStream is a grpc.ServerStream of one Watch of service: it records
// the statuses that the Watch sends, and its first send waits until hold
// closes, when hold is not nil.
type watchStream struct {
	ctx     context.Context
	service string
	hold    chan struct{}
	sending chan struct{}

	mu   sync.Mutex
	sent []healthpb.HealthCheckResponse_ServingStatus
}

func (w *watchStream) SetHeader(metadata.MD) error  { return nil }
func (w *watchStream) SendHeader(metadata.MD) error { return nil }
func (w *watchStream) SetTrailer(metadata.MD)       {}
func (w *watchStream) Context() context.Context     { return w.ctx }

func (w *watchStream) RecvMsg(m any) error {
	m.(*healthpb.HealthCheckRequest).Service = w.service
	return nil
}

func (w *watchStream) SendMsg(m any) error {
	w.mu.Lock()
	first := len(w.sent) == 0
	w.sent = append(w.sent, m.(*healthpb.HealthCheckResponse).GetStatus())
	w.mu.Unlock()
	if first && w.hold != nil {
		close(w.sending)
		<-w.hold
	}
	return nil
}

func (w *watchStream) statuses() []healthpb.HealthCheckResponse_ServingStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]healthpb.HealthCheckResponse_ServingStatus(nil), w.sent...)
}

// watch runs a Watch of w on g's interceptor and returns the channel of its
// handler's return.
func watch(g *Gate, w *watchStream) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- g.stream(nil, w, &grpc.StreamServerInfo{FullMethod: healthpb.Health_Watch_FullMethodName, IsServerStream: true}, nil)
	}()
	return done
}

// ended waits up to 10 seconds for a Watch's handler to return.
func ended(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the Watch did not end within 10 seconds of the Gate's Close")
		return nil
	}
}

// TestGateWatchSendsEachChange checks that a Watch sends each change of
// the engine's state in order, also when the changes come before it has
// sent the status before them: its first send, NOT_SERVING while the
// engine starts, waits while the Gate moves to Serving and then to
// Draining, and the Watch then still sends SERVING, then NOT_SERVING.
func TestGateWatchSendsEachChange(t *testing.T) {
	g := NewGate()
	w := &watchStream{ctx: context.Background(), service: plugin.Runner_ServiceDesc.ServiceName, hold: make(chan struct{}), sending: make(chan struct{})}
	done := watch(g, w)
	<-w.sending
	g.Serve(plugin.UnimplementedRunnerServer{})
	g.Drain()
	close(w.hold)
	g.Close()
	if err := ended(t, done); err != nil {
		t.Fatalf("the Watch ended with %v", err)
	}
	want := []healthpb.HealthCheckResponse_ServingStatus{healthpb.HealthCheckResponse_NOT_SERVING, healthpb.HealthCheckResponse_SERVING, healthpb.HealthCheckResponse_NOT_SERVING}
	if got := w.statuses(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("the Watch sent %v, want %v", got, want)
	}
}

// TestGateCloseEndsUnknownWatch checks that the Gate's Close ends a Watch
// of a service name that the engine does not answer, whose client is still
// there: it sends SERVICE_UNKNOWN, and returns without an error once the
// Gate closes.
func TestGateCloseEndsUnknownWatch(t *testing.T) {
	g := NewGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &watchStream{ctx: ctx, service: "other"}
	done := watch(g, w)
	for until := time.Now().Add(10 * time.Second); len(w.statuses()) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(until) {
			t.Fatal("the Watch of another name sent nothing within 10 seconds")
		}
	}
	g.Close()
	if err := ended(t, done); err != nil {
		t.Errorf("the Watch of another name ended with %v, want none", err)
	}
	if got := w.statuses(); len(got) != 1 || got[0] != healthpb.HealthCheckResponse_SERVICE_UNKNOWN {
		t.Errorf("the Watch of another name sent %v, want [SERVICE_UNKNOWN]", got)
	}
}

// TestGateDrainBeforeServe checks the Runner calls of a Gate whose drain
// began while the engine started, and that Serve then finds draining:
// GetBuildInfo, Cancel and GetRun answer UNAVAILABLE and never reach the
// Runner that Serve offered (which would answer UNIMPLEMENTED), and Run
// answers UNAVAILABLE (draining). A Gate that drains after it served keeps
// its Runner for those calls.
func TestGateDrainBeforeServe(t *testing.T) {
	ctx := context.Background()
	g := NewGate()
	g.Drain()
	g.Serve(plugin.UnimplementedRunnerServer{})
	r := g.Runner()
	_, err := r.GetBuildInfo(ctx, &plugin.GetBuildInfoRequest{})
	if status.Code(err) != codes.Unavailable {
		t.Errorf("GetBuildInfo of a Gate that drained while starting: %v, want UNAVAILABLE", err)
	}
	if _, err := r.Cancel(ctx, &plugin.CancelRequest{Id: "x"}); status.Code(err) != codes.Unavailable {
		t.Errorf("Cancel of a Gate that drained while starting: %v, want UNAVAILABLE", err)
	}
	if _, err := r.GetRun(ctx, &plugin.GetRunRequest{}); status.Code(err) != codes.Unavailable {
		t.Errorf("GetRun of a Gate that drained while starting: %v, want UNAVAILABLE", err)
	}
	if _, err := r.Run(ctx, &plugin.RunRequest{Path: "f/x"}); status.Code(err) != codes.Unavailable || !strings.Contains(status.Convert(err).Message(), "draining") {
		t.Errorf("Run of a Gate that drained while starting: %v, want UNAVAILABLE (draining)", err)
	}

	served := NewGate()
	served.Serve(plugin.UnimplementedRunnerServer{})
	served.Drain()
	if _, err := served.Runner().GetBuildInfo(ctx, &plugin.GetBuildInfoRequest{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("GetBuildInfo of a Gate that drained after it served: %v, want the Runner's answer, UNIMPLEMENTED", err)
	}
}

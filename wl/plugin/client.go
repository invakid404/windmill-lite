package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ErrorDomain and ReasonRunNotStarted are the domain and the reason of the
// google.rpc.ErrorInfo detail with which the engine marks a Run error as
// its own refusal of a run that it did not start.
const (
	ErrorDomain         = "windmill-lite"
	ReasonRunNotStarted = "RUN_NOT_STARTED"
)

// ErrNotStarted is the error of a Run call whose run did not start: the
// engine refused it with the RUN_NOT_STARTED detail, or the call's context
// had ended before Run sent it. The host may retry such a run.
var ErrNotStarted = errors.New("wl/plugin: the run did not start")

// ErrOutcomeUnknown is the error of a Run call that ended without a result
// and without the engine's refusal: at the call's deadline, on a lost
// connection or on any other error. The run may have started, so the host
// treats it as a run that may have mutated.
var ErrOutcomeUnknown = errors.New("wl/plugin: the run's outcome is unknown")

// Client is the host's side of one connection to the engine: typed calls
// of the Runner service and of gRPC health. go-plugin's Dispense returns it
// for PluginName. Its methods are safe for concurrent use.
type Client struct {
	runner RunnerClient
	health healthpb.HealthClient
	// broker is go-plugin's broker of the connection, on which the host
	// serves HostHooks.
	broker *goplugin.GRPCBroker
	margin time.Duration
	// now is the clock of the margin; tests replace it.
	now func() time.Time

	// conn is the connection, whose end stops the lookups of the runs'
	// hooks registrations.
	conn grpc.ClientConnInterface
	// hooks is the state of the runs' hooks that the Client serves.
	hooks hooks
	// timer returns a channel that receives after d, and the function
	// that stops it: the waits between GetRun lookups. Tests replace it.
	timer func(d time.Duration) (<-chan time.Time, func() bool)
}

// BuildInfo returns the engine's build identity, which a host compares with
// the release record that it ships before it sends any run.
func (c *Client) BuildInfo(ctx context.Context) (*BuildInfo, error) {
	return c.runner.GetBuildInfo(ctx, &GetBuildInfoRequest{})
}

// Health returns the engine's serving status: the gRPC health status of the
// service windmill_lite.v1.Runner. An engine that does not answer for that
// service, as go-plugin's own health server does not, returns an error.
func (c *Client) Health(ctx context.Context) (healthpb.HealthCheckResponse_ServingStatus, error) {
	res, err := c.health.Check(ctx, &healthpb.HealthCheckRequest{Service: Runner_ServiceDesc.ServiceName})
	if err != nil {
		return healthpb.HealthCheckResponse_UNKNOWN, err
	}
	return res.GetStatus(), nil
}

// Cancel cancels a run in flight by its id, with the reason and the
// canceler that the run's cancellation names.
func (c *Client) Cancel(ctx context.Context, req *CancelRequest) (*CancelResponse, error) {
	return c.runner.Cancel(ctx, req)
}

// Run runs a flow or a root script and returns its outcome. It does not
// change req.
//
// The run's hard deadline is the earlier of req.DeadlineMs and the
// deadline of ctx less the client's reply margin, in whole milliseconds,
// which leaves the engine the margin to end the run and answer before the
// call ends; it cannot guarantee that the answer arrives in time. Without
// either, the run has no hard deadline. A ctx that has already ended
// starts no call.
//
// An error wraps ErrNotStarted when the run did not start, or
// ErrOutcomeUnknown otherwise. The error of a call that Run made also
// wraps the call's gRPC error, whose code status.Code reads.
func (c *Client) Run(ctx context.Context, req *RunRequest) (*RunResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotStarted, context.Cause(ctx))
	}
	if req == nil {
		req = &RunRequest{}
	}
	send := req
	if dl, ok := ctx.Deadline(); ok {
		ms := (dl.Sub(c.now()) - c.margin).Milliseconds()
		if req.DeadlineMs == nil || ms < *req.DeadlineMs {
			send = withDeadline(req, ms)
		}
	}
	res, err := c.runner.Run(ctx, send)
	if err != nil {
		return nil, runError(err)
	}
	return res, nil
}

// withDeadline returns a copy of req whose deadline_ms is ms. The copy
// shares req's byte slices, maps and messages, which neither side changes,
// so a large argument is not copied.
func withDeadline(req *RunRequest, ms int64) *RunRequest {
	return withFields(req, func(r *RunRequest) { r.DeadlineMs = &ms })
}

// withFields returns a shallow copy of req, unknown fields included, that
// set changed.
func withFields(req *RunRequest, set func(*RunRequest)) *RunRequest {
	out := &RunRequest{}
	src, dst := req.ProtoReflect(), out.ProtoReflect()
	src.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		dst.Set(fd, v)
		return true
	})
	dst.SetUnknown(src.GetUnknown())
	set(out)
	return out
}

// runError classifies the error of a Run call: the engine's own refusal,
// which carries the RUN_NOT_STARTED detail, or an unknown outcome.
func runError(err error) error {
	if st, ok := status.FromError(err); ok {
		for _, d := range st.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == ErrorDomain && info.GetReason() == ReasonRunNotStarted {
				return fmt.Errorf("%w: %w", ErrNotStarted, err)
			}
		}
	}
	return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
}

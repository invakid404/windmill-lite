package plugin

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// fakeRunner is a Runner server that records each request it receives and
// answers with its functions.
type fakeRunner struct {
	UnimplementedRunnerServer

	mu      sync.Mutex
	runs    []*RunRequest
	cancels []*CancelRequest

	run  func(context.Context, *RunRequest) (*RunResult, error)
	info *BuildInfo
}

func (f *fakeRunner) Run(ctx context.Context, req *RunRequest) (*RunResult, error) {
	f.mu.Lock()
	f.runs = append(f.runs, req)
	f.mu.Unlock()
	return f.run(ctx, req)
}

func (f *fakeRunner) Cancel(_ context.Context, req *CancelRequest) (*CancelResponse, error) {
	f.mu.Lock()
	f.cancels = append(f.cancels, req)
	f.mu.Unlock()
	return &CancelResponse{}, nil
}

func (f *fakeRunner) GetBuildInfo(context.Context, *GetBuildInfoRequest) (*BuildInfo, error) {
	return f.info, nil
}

func (f *fakeRunner) received() []*RunRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*RunRequest(nil), f.runs...)
}

// trackingListener records the server's connections, so that a test can
// close them under a call in flight.
type trackingListener struct {
	net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		l.conns = append(l.conns, c)
		l.mu.Unlock()
	}
	return c, err
}

func (l *trackingListener) closeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		c.Close()
	}
}

// serve serves r, and the services that register adds, on an in-memory
// gRPC connection, and returns a client connection to it.
func serve(t *testing.T, r RunnerServer, register func(*grpc.Server)) (*grpc.ClientConn, *trackingListener) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	tracked := &trackingListener{Listener: lis}
	s := grpc.NewServer()
	RegisterRunnerServer(s, r)
	if register != nil {
		register(s)
	}
	go s.Serve(tracked)
	t.Cleanup(s.Stop)
	cc, err := grpc.NewClient("passthrough:///runner",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc, tracked
}

// TestRunnerRoundTrip sends Run, Cancel and GetBuildInfo through the
// Client and a real gRPC connection to a fake Runner: JSON values cross as
// the same bytes, an absent deadline stays absent and a deadline of 0
// stays 0.
func TestRunnerRoundTrip(t *testing.T) {
	// JSON text that any decoding and encoding would change: spacing, key
	// order, a duplicate key, escapes, and numbers past float64.
	args := []byte("{ \"b\" :1,\"a\":[1.0, 1e3,12345678901234567890],\n\"a\":\"\\u00e9\\/\" }")
	result := []byte("[ {\"z\":null , \"y\" : 0.10} ]\t")
	runData := []byte(`{"tenant":"t-1"}`)
	contract := []byte("{\"plugin\" : \"p\"}")
	fake := &fakeRunner{
		run: func(_ context.Context, req *RunRequest) (*RunResult, error) {
			return &RunResult{Id: req.GetId(), Status: "success", Result: result, MayHaveMutated: true}, nil
		},
		info: &BuildInfo{BundleDigest: "sha256:b", EngineVersion: "v9.9.9", Contract: contract, ProtocolVersion: 1, Labels: map[string]string{"region": "r1"}, ConfigDigest: "c0ffee"},
	}
	cc, _ := serve(t, fake, nil)
	c := newClient(cc, nil, DefaultReplyMargin)
	ctx := context.Background()

	cases := []struct {
		name               string
		deadline, preWrite *int64
	}{
		{"absent", nil, nil},
		{"zero", proto.Int64(0), proto.Int64(0)},
		{"positive", proto.Int64(1500), proto.Int64(700)},
		{"negative", proto.Int64(-5), nil},
	}
	for i, tc := range cases {
		res, err := c.Run(ctx, &RunRequest{Id: tc.name, Kind: "flow", Path: "f/a/b", Args: args, RunData: runData, DeadlineMs: tc.deadline, PreWriteDeadlineMs: tc.preWrite})
		if err != nil {
			t.Fatalf("%s: Run: %v", tc.name, err)
		}
		if !bytes.Equal(res.GetResult(), result) || res.GetStatus() != "success" || !res.GetMayHaveMutated() || res.GetId() != tc.name {
			t.Errorf("%s: result %q %q %v %q, want %q success true %q", tc.name, res.GetResult(), res.GetStatus(), res.GetMayHaveMutated(), res.GetId(), result, tc.name)
		}
		got := fake.received()[i]
		if !bytes.Equal(got.GetArgs(), args) || !bytes.Equal(got.GetRunData(), runData) {
			t.Errorf("%s: the server got args %q and run data %q, want %q and %q", tc.name, got.GetArgs(), got.GetRunData(), args, runData)
		}
		if !sameOptional(got.DeadlineMs, tc.deadline) || !sameOptional(got.PreWriteDeadlineMs, tc.preWrite) {
			t.Errorf("%s: the server got deadline_ms %s and pre_write_deadline_ms %s, want %s and %s", tc.name, optional(got.DeadlineMs), optional(got.PreWriteDeadlineMs), optional(tc.deadline), optional(tc.preWrite))
		}
	}

	if _, err := c.Cancel(ctx, &CancelRequest{Id: "r-1", Reason: "the tenant left", Canceler: "host"}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if n := len(fake.cancels); n != 1 {
		t.Fatalf("the server got %d cancels, want 1", n)
	}
	if got := fake.cancels[0]; got.GetId() != "r-1" || got.GetReason() != "the tenant left" || got.GetCanceler() != "host" {
		t.Errorf("the server got the cancel %v", got)
	}

	info, err := c.BuildInfo(ctx)
	if err != nil {
		t.Fatalf("BuildInfo: %v", err)
	}
	if !proto.Equal(info, fake.info) || !bytes.Equal(info.GetContract(), contract) {
		t.Errorf("BuildInfo = %v, want %v", info, fake.info)
	}
}

// TestClientHealth reads the gRPC health status of the Runner service, and
// fails for an engine that does not answer for that service.
func TestClientHealth(t *testing.T) {
	hs := health.NewServer()
	cc, _ := serve(t, &fakeRunner{}, func(s *grpc.Server) { healthpb.RegisterHealthServer(s, hs) })
	c := newClient(cc, nil, DefaultReplyMargin)
	ctx := context.Background()

	// Like go-plugin's server, which uses gRPC's standard health server,
	// it answers SERVING for "" and for "plugin", and has no status for
	// the Runner service.
	hs.SetServingStatus("plugin", healthpb.HealthCheckResponse_SERVING)
	if st, err := c.Health(ctx); status.Code(err) != codes.NotFound {
		t.Errorf("Health without the Runner service = %v, %v; want the error NotFound", st, err)
	}
	for _, want := range []healthpb.HealthCheckResponse_ServingStatus{healthpb.HealthCheckResponse_NOT_SERVING, healthpb.HealthCheckResponse_SERVING} {
		hs.SetServingStatus("windmill_lite.v1.Runner", want)
		if st, err := c.Health(ctx); err != nil || st != want {
			t.Errorf("Health = %v, %v; want %v", st, err, want)
		}
	}
}

// TestClientRunOutcomes checks the hard deadline that Run sends, and how it
// classifies a call that ends without a result.
func TestClientRunOutcomes(t *testing.T) {
	ok := func(_ context.Context, req *RunRequest) (*RunResult, error) {
		return &RunResult{Id: req.GetId(), Status: "success"}, nil
	}

	t.Run("deadline from the context", func(t *testing.T) {
		fake := &fakeRunner{run: ok}
		cc, _ := serve(t, fake, nil)
		c := newClient(cc, nil, DefaultReplyMargin)
		dl := time.Now().Add(time.Hour)
		c.now = func() time.Time { return dl.Add(-10 * time.Second) }
		ctx, cancel := context.WithDeadline(context.Background(), dl)
		defer cancel()

		long := &RunRequest{
			Id:                 "long",
			Path:               "f/a/b",
			Args:               []byte(`{ "k" : [1.0] }`),
			RunData:            []byte(`{"t":1}`),
			FatalErrorNames:    []string{"Fatal"},
			Resources:          map[string]*Resource{"u/a/r": {Type: "t", Value: []byte(`{"x" : 1}`)}},
			Identity:           &Identity{Workspace: "w"},
			DeadlineMs:         proto.Int64(20_000),
			PreWriteDeadlineMs: proto.Int64(0),
		}
		sent := proto.Clone(long).(*RunRequest)
		for _, req := range []*RunRequest{{Id: "none"}, long, {Id: "short", DeadlineMs: proto.Int64(500)}} {
			if _, err := c.Run(ctx, req); err != nil {
				t.Fatalf("Run %s: %v", req.GetId(), err)
			}
		}
		got := fake.received()
		// The context leaves 10 s, less the 1 s margin; an explicit shorter
		// budget wins, a longer one does not.
		for i, want := range []int64{9000, 9000, 500} {
			if got[i].DeadlineMs == nil || *got[i].DeadlineMs != want {
				t.Errorf("run %s: the server got deadline_ms %s, want %d", got[i].GetId(), optional(got[i].DeadlineMs), want)
			}
		}
		// Every other field arrives as the caller set it, and the caller's
		// request does not change.
		sent.DeadlineMs = proto.Int64(9000)
		if !proto.Equal(got[1], sent) {
			t.Errorf("the server got %v, want %v", got[1], sent)
		}
		if long.GetDeadlineMs() != 20_000 {
			t.Errorf("Run changed the caller's request: deadline_ms %d", long.GetDeadlineMs())
		}
	})

	t.Run("the plugin's reply margin", func(t *testing.T) {
		fake := &fakeRunner{run: ok}
		cc, _ := serve(t, fake, nil)
		raw, err := (&RunnerPlugin{ReplyMargin: 2500 * time.Millisecond}).GRPCClient(context.Background(), nil, cc)
		if err != nil {
			t.Fatal(err)
		}
		c := raw.(*Client)
		dl := time.Now().Add(time.Hour)
		c.now = func() time.Time { return dl.Add(-10 * time.Second) }
		ctx, cancel := context.WithDeadline(context.Background(), dl)
		defer cancel()
		if _, err := c.Run(ctx, &RunRequest{Id: "r"}); err != nil {
			t.Fatal(err)
		}
		if got := fake.received()[0].DeadlineMs; got == nil || *got != 7500 {
			t.Errorf("the server got deadline_ms %s, want 7500", optional(got))
		}
		if _, err := (&RunnerPlugin{ReplyMargin: -time.Second}).GRPCClient(context.Background(), nil, cc); err == nil {
			t.Error("a negative ReplyMargin dispensed a client")
		}
	})

	t.Run("an ended context starts no call", func(t *testing.T) {
		fake := &fakeRunner{run: ok}
		cc, _ := serve(t, fake, nil)
		c := newClient(cc, nil, DefaultReplyMargin)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.Run(ctx, &RunRequest{Id: "r"})
		if !errors.Is(err, ErrNotStarted) || !errors.Is(err, context.Canceled) || errors.Is(err, ErrOutcomeUnknown) {
			t.Errorf("Run = %v, want ErrNotStarted and context.Canceled", err)
		}
		if n := len(fake.received()); n != 0 {
			t.Errorf("the server got %d runs, want 0", n)
		}
	})

	t.Run("an answer after the call's deadline", func(t *testing.T) {
		received := make(chan struct{})
		returned := make(chan struct{})
		answered := make(chan time.Time, 1)
		fake := &fakeRunner{run: func(_ context.Context, req *RunRequest) (*RunResult, error) {
			close(received)
			// The answer waits for the client's call to end. The server's
			// own context ends at the call's deadline too, so an answer sent
			// then races the client's deadline timer, and can win it.
			<-returned
			answered <- time.Now()
			return &RunResult{Id: req.GetId(), Status: "success"}, nil
		}}
		cc, _ := serve(t, fake, nil)
		c := newClient(cc, nil, DefaultReplyMargin)
		// A first call sets the connection up, so the run's call reaches the
		// server well before its deadline.
		if _, err := c.Cancel(context.Background(), &CancelRequest{Id: "warm-up"}); err != nil {
			t.Fatal(err)
		}
		dl := time.Now().Add(200 * time.Millisecond)
		ctx, cancel := context.WithDeadline(context.Background(), dl)
		defer cancel()
		_, err := c.Run(ctx, &RunRequest{Id: "r"})
		close(returned)
		select {
		case <-received:
		default:
			t.Fatalf("Run = %v before the server got the run", err)
		}
		select {
		case at := <-answered:
			if at.Before(dl) {
				t.Errorf("the server answered at %v, before the call's deadline %v", at, dl)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the server did not answer within 5 s of the call's deadline")
		}
		if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrNotStarted) || status.Code(err) != codes.DeadlineExceeded {
			t.Errorf("Run = %v, want ErrOutcomeUnknown with DeadlineExceeded", err)
		}
	})

	t.Run("a connection lost after a write", func(t *testing.T) {
		var tracked *trackingListener
		wrote := make(chan struct{})
		fake := &fakeRunner{run: func(ctx context.Context, req *RunRequest) (*RunResult, error) {
			close(wrote) // the run's first write happens here
			tracked.closeAll()
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		var cc *grpc.ClientConn
		cc, tracked = serve(t, fake, nil)
		c := newClient(cc, nil, DefaultReplyMargin)
		_, err := c.Run(context.Background(), &RunRequest{Id: "r"})
		select {
		case <-wrote:
		default:
			t.Fatalf("Run = %v before the server got the run", err)
		}
		if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrNotStarted) || status.Code(err) != codes.Unavailable {
			t.Errorf("Run = %v, want ErrOutcomeUnknown with Unavailable", err)
		}
	})

	refusal := func(code codes.Code, info *errdetails.ErrorInfo) func(context.Context, *RunRequest) (*RunResult, error) {
		return func(context.Context, *RunRequest) (*RunResult, error) {
			st := status.New(code, "refused")
			if info != nil {
				var err error
				if st, err = st.WithDetails(info); err != nil {
					return nil, err
				}
			}
			return nil, st.Err()
		}
	}
	for _, tc := range []struct {
		name       string
		code       codes.Code
		info       *errdetails.ErrorInfo
		notStarted bool
	}{
		{"the engine's refusal", codes.AlreadyExists, &errdetails.ErrorInfo{Domain: "windmill-lite", Reason: "RUN_NOT_STARTED"}, true},
		{"the engine's refusal while it drains", codes.Unavailable, &errdetails.ErrorInfo{Domain: "windmill-lite", Reason: "RUN_NOT_STARTED"}, true},
		{"the same code without the detail", codes.AlreadyExists, nil, false},
		{"another domain", codes.AlreadyExists, &errdetails.ErrorInfo{Domain: "example.com", Reason: "RUN_NOT_STARTED"}, false},
		{"another reason", codes.InvalidArgument, &errdetails.ErrorInfo{Domain: "windmill-lite", Reason: "RUN_FAILED"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc, _ := serve(t, &fakeRunner{run: refusal(tc.code, tc.info)}, nil)
			c := newClient(cc, nil, DefaultReplyMargin)
			_, err := c.Run(context.Background(), &RunRequest{Id: "r"})
			if errors.Is(err, ErrNotStarted) != tc.notStarted || errors.Is(err, ErrOutcomeUnknown) == tc.notStarted {
				t.Errorf("Run = %v; ErrNotStarted %v, ErrOutcomeUnknown %v; want ErrNotStarted %v", err, errors.Is(err, ErrNotStarted), errors.Is(err, ErrOutcomeUnknown), tc.notStarted)
			}
			if status.Code(err) != tc.code {
				t.Errorf("status.Code = %v, want %v", status.Code(err), tc.code)
			}
		})
	}
}

func sameOptional(a, b *int64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func optional(p *int64) string {
	if p == nil {
		return "absent"
	}
	return strconv.FormatInt(*p, 10)
}

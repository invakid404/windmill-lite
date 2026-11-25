package host

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/invakid404/windmill-lite/internal/wlext"
	"github.com/invakid404/windmill-lite/wl"
	"github.com/invakid404/windmill-lite/wl/plugin"
)

// DefaultTokenBound is how many tokens that GetRun answered gone for the
// engine keeps, at most, before it stops serving ([ADR 0009], hooks across
// the plugin boundary).
//
// [ADR 0009]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0009-host-adapters.md#hooks-across-the-plugin-boundary
const DefaultTokenBound = 65536

// releasedHook is the name of the internal call that tells the host that a
// run has settled, so that it drops the run's registration.
const releasedHook = "released"

// The states that GetRun answers.
const (
	runHeld = "held"
	runGone = "gone"
)

// brokerHooks serves the runs' hooks on go-plugin's broker: it dials each
// connection of the host's HostHooks once, holds the token of each run
// from its admission until it has sent the run's release call, and keeps
// the tokens that GetRun answered gone for.
type brokerHooks struct {
	broker  func() *goplugin.GRPCBroker
	timeout time.Duration
	limit   int
	bound   int
	onBound func()
	// wrap, when set, wraps each HostHooks client that it dials: a fake
	// transport of the engine's calls. Tests set it.
	wrap func(plugin.HostHooksClient) plugin.HostHooksClient
	// dialing, when set, is called with each broker id just before its
	// dial; dialOptions are added to each dial. Tests set them.
	dialing     func(id uint32)
	dialOptions []grpc.DialOption

	// connMu guards conns: the dial of each broker connection, made once.
	// A dial waits without it, so one broker id that does not answer
	// stalls neither the other ids nor the tokens.
	connMu sync.Mutex
	conns  map[uint32]*brokerConn

	mu   sync.Mutex
	held map[string]heldToken
	gone map[string]struct{}
	full bool
}

// heldToken is the run that holds a token, and the broker connection that
// its hooks use.
type heldToken struct {
	run    string
	broker uint32
}

// brokerConn is the dial of one broker connection: done closes once conn
// and client, or err, are set. closed is set once the host has let the
// connection go; a call on it then gets no answer.
type brokerConn struct {
	done   chan struct{}
	conn   *grpc.ClientConn
	client plugin.HostHooksClient
	err    error
	closed atomic.Bool
}

// admit admits the hooks h of the run id: it refuses a token that is held
// or gone, holds the token, and dials the broker connection that h names.
// It returns ctx carrying the run's hooks and its settlement callback, and
// the function that forgets the token of a run that did not start.
func (b *brokerHooks) admit(ctx context.Context, id string, h *plugin.Hooks) (context.Context, func(), error) {
	token := h.GetToken()
	switch {
	case b == nil || b.broker == nil || b.broker() == nil:
		return nil, nil, invalid("hooks", "this engine has no go-plugin broker to reach the host's hooks on")
	case token == "":
		return nil, nil, invalid("hooks.token", "required")
	}
	b.mu.Lock()
	_, held := b.held[token]
	_, gone := b.gone[token]
	if !held && !gone {
		b.held[token] = heldToken{run: id, broker: h.GetBrokerId()}
	}
	b.mu.Unlock()
	switch {
	case gone:
		// GetRun answered gone for it: the host has dropped the
		// registration, so a delayed request never starts its run.
		return nil, nil, refusal(codes.InvalidArgument, "hooks.token: the engine answered gone for it, so its registration was dropped")
	case held:
		return nil, nil, refusal(codes.AlreadyExists, "hooks.token: a run of the engine holds it")
	}
	forget := func() {
		b.mu.Lock()
		delete(b.held, token)
		b.mu.Unlock()
		b.dropUnused(h.GetBrokerId())
	}
	bc, err := b.client(h.GetBrokerId())
	if err != nil {
		forget()
		return nil, nil, invalid("hooks.broker_id", err.Error())
	}
	call := func(ctx context.Context, hook, run, site string, payload []byte) ([]byte, error) {
		if bc.closed.Load() {
			// The host's Client let the connection go, and with it
			// every registration: no answer, which latches nothing.
			return noAnswer(hook, payload), nil
		}
		rep, err := bc.client.Call(ctx, &plugin.HookCall{Hook: hook, Run: run, Token: token, Site: site, Payload: payload})
		if err != nil {
			if ctx.Err() != nil || pastDeadline(ctx) {
				// The plugin timeout or the run's end, as a host_call's.
				// The host's server ends the call at its deadline, which
				// can come a moment before ctx's own timer.
				<-ctx.Done()
				return nil, context.Cause(ctx)
			}
			if bc.closed.Load() {
				return noAnswer(hook, payload), nil
			}
			return nil, fmt.Errorf("the host's hooks service: %w", err)
		}
		if e := rep.GetError(); e != nil {
			if e.GetCode() == plugin.HookCodeUnknownRun {
				// The host holds no registration of the run: no answer,
				// which latches nothing.
				return noAnswer(hook, payload), nil
			}
			return nil, &wl.HostCallError{Hook: hook, Code: e.GetCode(), Message: e.GetMessage()}
		}
		return rep.GetPayload(), nil
	}
	ctx = wlext.WithRunHooks(ctx, wlext.RunHooks{Declaration: h.GetDeclaration(), Call: call})
	ctx = wlext.WithSettled(ctx, func() { go b.release(bc, h.GetBrokerId(), id, token) })
	return ctx, forget, nil
}

// pastDeadline reports whether ctx has a deadline that has passed.
func pastDeadline(ctx context.Context) bool {
	d, ok := ctx.Deadline()
	return ok && !time.Now().Before(d)
}

// noAnswer is the answer of a hook that has none, as a run without that
// hook has it: an interception or a provider declines, a rewrite keeps
// the arguments, the environment stays, a resource or variable is not
// found, and every other hook answers nothing.
func noAnswer(hook string, payload []byte) []byte {
	var p struct {
		Args json.RawMessage `json:"args"`
		Env  json.RawMessage `json:"env"`
	}
	_ = json.Unmarshal(payload, &p)
	var a any
	switch hook {
	case "intercept", "provide":
		a = map[string]any{"decline": true}
	case "rewrite_args":
		a = map[string]any{"args": p.Args}
	case "invocation_env":
		a = map[string]any{"env": p.Env}
	case "resource", "variable":
		a = map[string]any{"found": false}
	default:
		a = map[string]any{}
	}
	b, _ := json.Marshal(a)
	return b
}

// client returns the dial of the broker connection id, which it dials
// once, with the engine's message limit and no idle timeout, so that the
// connection stays up while the host's listener serves. A failed dial is
// not kept: a later run dials again.
func (b *brokerHooks) client(id uint32) (*brokerConn, error) {
	b.connMu.Lock()
	c := b.conns[id]
	first := c == nil
	if first {
		c = &brokerConn{done: make(chan struct{})}
		b.conns[id] = c
	}
	b.connMu.Unlock()
	if first {
		if b.dialing != nil {
			b.dialing(id)
		}
		opts := append([]grpc.DialOption{grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(b.limit), grpc.MaxCallSendMsgSize(b.limit)), grpc.WithIdleTimeout(0)}, b.dialOptions...)
		conn, err := b.broker().DialWithOptions(id, opts...)
		if err != nil {
			c.err = fmt.Errorf("dial the host's hooks service: %w", err)
			b.connMu.Lock()
			delete(b.conns, id)
			b.connMu.Unlock()
		} else {
			c.conn = conn
			c.client = plugin.NewHostHooksClient(conn)
			if b.wrap != nil {
				c.client = b.wrap(c.client)
			}
			go b.watch(id, c, conn)
			go b.watchHost(id, c)
		}
		close(c.done)
	}
	<-c.done
	if c.err != nil {
		return nil, c.err
	}
	return c, nil
}

// watchHost follows the host's health on the broker connection id, whose
// dial is c. NOT_SERVING says that the host's Client has let the
// connection go, which go-plugin does not tell. The engine then closes it
// at once, even while a run in flight uses it: that run's later calls get
// no answer, as calls for a dropped registration do. A host that serves
// no health is not followed.
func (b *brokerHooks) watchHost(id uint32, c *brokerConn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hc := healthpb.NewHealthClient(c.conn)
	for {
		stream, err := hc.Watch(ctx, &healthpb.HealthCheckRequest{Service: plugin.HostHooks_ServiceDesc.ServiceName}, grpc.WaitForReady(true))
		for err == nil {
			var res *healthpb.HealthCheckResponse
			if res, err = stream.Recv(); err == nil && res.GetStatus() == healthpb.HealthCheckResponse_NOT_SERVING {
				b.dispose(id, c)
				return
			}
		}
		if status.Code(err) == codes.Unimplemented || c.conn.GetState() == connectivity.Shutdown {
			return
		}
		// Watch again once the connection is back, without spinning on
		// one that stays ready.
		if s := c.conn.GetState(); s == connectivity.Ready {
			c.conn.WaitForStateChange(ctx, s)
		}
	}
}

// dispose closes the broker connection id, whose dial is c, which the
// host has let go, and forgets its dial.
func (b *brokerHooks) dispose(id uint32, c *brokerConn) {
	c.closed.Store(true)
	b.connMu.Lock()
	if b.conns[id] == c {
		delete(b.conns, id)
	}
	b.connMu.Unlock()
	_ = c.conn.Close()
}

// watch follows the broker connection id, whose dial is c: a connection
// that was lost connects again, and one that cannot connect is closed
// once no run that the engine holds uses it (dropUnused).
func (b *brokerHooks) watch(id uint32, c *brokerConn, conn *grpc.ClientConn) {
	for {
		s := conn.GetState()
		switch s {
		case connectivity.Idle:
			conn.Connect()
		case connectivity.TransientFailure:
			if b.dropUnused(id) {
				return
			}
		case connectivity.Shutdown:
			return
		}
		conn.WaitForStateChange(context.Background(), s)
	}
}

// dropUnused closes the broker connection id, and forgets its dial, when
// it cannot connect and no run that the engine holds uses it, and
// reports whether it did. gRPC would connect again, and a run in flight
// keeps that chance. Without one, the engine no longer holds a connection
// to the host's listener, so the host serves a new broker id at its next
// run with hooks: go-plugin gives an id's address once, so this one is
// never dialled again. The host's Close, which stops its listener, ends
// here too.
func (b *brokerHooks) dropUnused(id uint32) bool {
	b.connMu.Lock()
	c := b.conns[id]
	if c == nil {
		b.connMu.Unlock()
		return false
	}
	select {
	case <-c.done:
	default:
		// Still dialling.
		b.connMu.Unlock()
		return false
	}
	if c.conn == nil || c.conn.GetState() != connectivity.TransientFailure || b.inUse(id) {
		b.connMu.Unlock()
		return false
	}
	delete(b.conns, id)
	b.connMu.Unlock()
	_ = c.conn.Close()
	return true
}

// inUse reports whether a token that the engine holds names the broker
// connection id.
func (b *brokerHooks) inUse(id uint32) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range b.held {
		if t.broker == id {
			return true
		}
	}
	return false
}

// dialled is how many broker connections the engine holds.
func (b *brokerHooks) dialled() int {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	return len(b.conns)
}

// release sends the release call of the run id, whose token is token, on
// the broker connection broker once the run has settled, then stops
// holding the token. A call that fails is the host's to reconcile
// (GetRun).
func (b *brokerHooks) release(bc *brokerConn, broker uint32, id, token string) {
	if !bc.closed.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
		defer cancel()
		_, _ = bc.client.Call(ctx, &plugin.HookCall{Hook: releasedHook, Run: id, Token: token})
	}
	b.mu.Lock()
	delete(b.held, token)
	b.mu.Unlock()
	b.dropUnused(broker)
}

// lookup answers GetRun: held while the engine holds the token for the run
// id, gone otherwise, which it remembers. At its bound of gone tokens it
// calls onBound, once, and then answers gone only for the tokens that it
// keeps: a lookup of another token answers UNAVAILABLE, so that no engine
// that still takes runs forgets a token.
func (b *brokerHooks) lookup(id, token string) (string, error) {
	if b == nil {
		return runGone, nil
	}
	if token == "" {
		return "", invalidLookup("token: required")
	}
	b.mu.Lock()
	if t, ok := b.held[token]; ok && t.run == id {
		b.mu.Unlock()
		return runHeld, nil
	}
	if _, ok := b.gone[token]; !ok {
		if b.full {
			b.mu.Unlock()
			return "", status.Error(codes.Unavailable, errDrainingLookup)
		}
		b.gone[token] = struct{}{}
	}
	trip := !b.full && len(b.gone) >= b.bound
	if trip {
		b.full = true
	}
	b.mu.Unlock()
	if trip && b.onBound != nil {
		b.onBound()
	}
	return runGone, nil
}

// goneTokens is how many tokens the engine keeps that GetRun answered gone
// for.
func (b *brokerHooks) goneTokens() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.gone)
}

// invalidLookup refuses a GetRun request: a lookup starts no run, so its
// refusal carries no RUN_NOT_STARTED detail.
func invalidLookup(msg string) error {
	return status.Error(codes.InvalidArgument, msg)
}

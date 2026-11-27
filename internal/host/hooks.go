package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
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

// errDial is the error of a dial of the host's hooks service that failed.
var errDial = errors.New("dial the host's hooks service")

// errUsed is the error of an admission that needs a new dial of a broker
// id whose earlier dial succeeded: go-plugin gives an id's address once, so
// a second dial would only wait out go-plugin's 5 seconds and fail.
var errUsed = errors.New("the engine has dialled this broker id before and lost that connection, and go-plugin gives a broker id's address once")

// releasedHook is the name of the internal call that tells the host that a
// run has settled, so that it drops the run's registration.
const releasedHook = "released"

// The states that GetRun answers.
const (
	runHeld = "held"
	runGone = "gone"
)

// brokerHooks serves the runs' hooks on go-plugin's broker: it dials each
// connection of the host's HostHooks once, refusing an id whose connection
// it has dropped, holds the token of each run from its admission until it
// has sent the run's release call, and keeps the tokens that GetRun
// answered gone for.
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
	// dial; dialBroker, when set, gives the dial its connection in place of
	// go-plugin's broker; dialed with each connection that a dial got,
	// before it waits for it to be ready; joined when an admission waits
	// on another's dial; and published once a dial has ended. dialOptions
	// are added to each dial. Tests set them.
	dialing     func(id uint32)
	dialBroker  func(id uint32, opts ...grpc.DialOption) (*grpc.ClientConn, error)
	dialed      func(id uint32, conn *grpc.ClientConn)
	joined      func(id uint32)
	published   func(id uint32)
	dialOptions []grpc.DialOption

	// connMu guards conns, the dial of each broker connection, made once,
	// and used, the broker ids whose dial succeeded and that the host has
	// not released: one entry for each such id, which ReleaseHooks
	// removes. A dial waits without connMu, so one broker id that does not
	// answer stalls neither the other ids nor the tokens.
	connMu sync.Mutex
	conns  map[uint32]*brokerConn
	used   map[uint32]struct{}

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

// brokerConn is the dial of one broker connection: done closes, under
// brokerHooks.connMu, once conn and client, or err, are set; released,
// under connMu too, says that the host let the connection go. closed is
// set once its connection is closed for that; a call that starts on it
// then gets no answer.
type brokerConn struct {
	done     chan struct{}
	conn     *grpc.ClientConn
	client   plugin.HostHooksClient
	err      error
	released bool
	closed   atomic.Bool
}

// admit admits the hooks h of the run id: it refuses a token that is held
// or gone, holds the token, and dials the broker connection that h names.
// arrivedEnded says that the run's deadline had passed when its request
// arrived. It returns ctx carrying the run's hooks and its settlement
// callback, and the function that forgets the token of a run that did not
// start.
func (b *brokerHooks) admit(ctx context.Context, id string, h *plugin.Hooks, arrivedEnded bool) (context.Context, func(), error) {
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
	bc, err := b.client(ctx, h.GetBrokerId(), !arrivedEnded)
	if err != nil {
		forget()
		if ctx.Err() != nil && !errors.Is(err, errDial) {
			// The run's own end, a Cancel or its deadline, came while
			// its dial waited: the run did not start.
			code := codes.Canceled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = codes.DeadlineExceeded
			}
			return nil, nil, refusal(code, fmt.Sprintf("the run ended before the host's hooks service answered: %v", err))
		}
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
			// A call that the host's letting go cut fails too: its
			// answer, a first-write veto say, may have been on its way.
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

// client returns the dial of the broker connection id, which a goroutine
// of its own makes once (dial). An admission waits for that dial, and,
// when ends is set, for the end of its run's ctx, a Cancel or the run's
// deadline, which leaves the dial to the admissions that still wait on
// it. A run whose deadline had passed when its request arrived waits for
// the dial alone, and the runtime then ends it canceled, as a run without
// hooks. A dial that has ended already is taken. An id that needs a new
// dial although an earlier one succeeded fails at once (errUsed): the
// engine has dropped that connection, and the host serves a new id once
// it hears of the refusal.
func (b *brokerHooks) client(ctx context.Context, id uint32, ends bool) (*brokerConn, error) {
	b.connMu.Lock()
	c := b.conns[id]
	first := c == nil
	if _, used := b.used[id]; first && used {
		b.connMu.Unlock()
		return nil, fmt.Errorf("%w: %w", errDial, errUsed)
	}
	if first {
		c = &brokerConn{done: make(chan struct{})}
		b.conns[id] = c
	}
	b.connMu.Unlock()
	if first {
		go b.dial(id, c)
	} else if b.joined != nil {
		b.joined(id)
	}
	var end <-chan struct{}
	if ends {
		end = ctx.Done()
	}
	select {
	case <-c.done:
	default:
		select {
		case <-c.done:
		case <-end:
			return nil, context.Cause(ctx)
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	return c, nil
}

// dial makes the dial c of the broker connection id, with the engine's
// message limit and no idle timeout, so that the connection stays up while
// the host's listener serves. It has two waits: go-plugin's for the
// host's address, which takes up to 5 seconds on the default broker, and
// then one until the connection is ready, at most the plugin timeout. A
// connection that cannot connect fails the dial, so a listener that the
// host has stopped refuses the run instead of leaving a connection that
// only tries again. A failed dial is not kept: a later run dials again. A
// dial that succeeds marks id used, unless the host released id while it
// was dialled.
func (b *brokerHooks) dial(id uint32, c *brokerConn) {
	if b.dialing != nil {
		b.dialing(id)
	}
	opts := append([]grpc.DialOption{grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(b.limit), grpc.MaxCallSendMsgSize(b.limit)), grpc.WithIdleTimeout(0)}, b.dialOptions...)
	dialBroker := b.broker().DialWithOptions
	if b.dialBroker != nil {
		dialBroker = b.dialBroker
	}
	conn, err := dialBroker(id, opts...)
	if err == nil {
		if b.dialed != nil {
			b.dialed(id, conn)
		}
		if err = b.ready(conn); err != nil {
			_ = conn.Close()
		}
	}
	b.connMu.Lock()
	if err != nil {
		c.err = fmt.Errorf("%w: %w", errDial, err)
		if b.conns[id] == c {
			delete(b.conns, id)
		}
	} else {
		c.conn = conn
		c.client = plugin.NewHostHooksClient(conn)
		if b.wrap != nil {
			c.client = b.wrap(c.client)
		}
		if !c.released {
			b.used[id] = struct{}{}
		}
	}
	released := c.released && err == nil
	if released {
		// The host let it go while it was dialled: an admission that
		// waits on this dial sees it closed when it wakes.
		c.closed.Store(true)
	}
	close(c.done)
	b.connMu.Unlock()
	if b.published != nil {
		b.published(id)
	}
	switch {
	case err != nil:
	case released:
		_ = conn.Close()
	default:
		go b.watch(id, c, conn)
	}
}

// letGo closes the broker connection id at once, which the host says it
// has let go (ReleaseHooks), even while a run in flight uses it: a call
// already sent on it fails, and a later call of a run that named it gets
// no answer, as a call of a dropped registration does. go-plugin gives
// no such word, and the word comes on the HooksControl service, so it
// arrives also while the broker connection is down. A dial of id still
// under way closes its connection as it ends. The engine forgets that id
// was used, since the host never names a released id again. An id that
// the engine holds no connection to is otherwise ignored, so the word may
// come again.
func (b *brokerHooks) letGo(id uint32) {
	if b == nil {
		return
	}
	b.connMu.Lock()
	delete(b.used, id)
	c := b.conns[id]
	ended := false
	if c != nil {
		delete(b.conns, id)
		c.released = true
		select {
		case <-c.done:
			ended = c.err == nil
		default:
		}
	}
	b.connMu.Unlock()
	if ended {
		c.closed.Store(true)
		_ = c.conn.Close()
	}
}

// ready waits until conn, a dial's new connection, is ready, at most the
// plugin timeout. A connect that fails ends the wait at once.
func (b *brokerHooks) ready(conn *grpc.ClientConn) error {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	conn.Connect()
	for {
		switch s := conn.GetState(); s {
		case connectivity.Ready:
			return nil
		case connectivity.TransientFailure, connectivity.Shutdown:
			return fmt.Errorf("the connection is %v", s)
		default:
			if !conn.WaitForStateChange(ctx, s) {
				return fmt.Errorf("the connection did not get ready: %w", context.Cause(ctx))
			}
		}
	}
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
// never dialled again, and an admission that still names it is refused at
// once (client). The host's Close, which stops its listener, ends here
// too.
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

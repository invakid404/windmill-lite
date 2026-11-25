package host

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
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
	// dial. Tests set it.
	dialing func(id uint32)

	// connMu guards conns: the dial of each broker connection, made once.
	// A dial waits without it, so one broker id that does not answer
	// stalls neither the other ids nor the tokens.
	connMu sync.Mutex
	conns  map[uint32]*brokerConn

	mu   sync.Mutex
	held map[string]string // token → run id
	gone map[string]struct{}
	full bool
}

// brokerConn is the dial of one broker connection: done closes once client
// or err is set.
type brokerConn struct {
	done   chan struct{}
	client plugin.HostHooksClient
	err    error
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
		b.held[token] = id
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
		defer b.mu.Unlock()
		delete(b.held, token)
	}
	client, err := b.client(h.GetBrokerId())
	if err != nil {
		forget()
		return nil, nil, invalid("hooks.broker_id", err.Error())
	}
	call := func(ctx context.Context, hook, run, site string, payload []byte) ([]byte, error) {
		rep, err := client.Call(ctx, &plugin.HookCall{Hook: hook, Run: run, Token: token, Site: site, Payload: payload})
		if err != nil {
			if ctx.Err() != nil {
				// The plugin timeout or the run's end, as a host_call's.
				return nil, context.Cause(ctx)
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
	ctx = wlext.WithSettled(ctx, func() { go b.release(client, id, token) })
	return ctx, forget, nil
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

// client returns the HostHooks client of the broker connection id, which
// it dials once, with the engine's message limit. A failed dial is not
// kept: a later run dials again. A connection is kept until the host's
// listener is gone.
func (b *brokerHooks) client(id uint32) (plugin.HostHooksClient, error) {
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
		conn, err := b.broker().DialWithOptions(id, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(b.limit), grpc.MaxCallSendMsgSize(b.limit)))
		if err != nil {
			c.err = fmt.Errorf("dial the host's hooks service: %w", err)
			b.connMu.Lock()
			delete(b.conns, id)
			b.connMu.Unlock()
		} else {
			c.client = plugin.NewHostHooksClient(conn)
			if b.wrap != nil {
				c.client = b.wrap(c.client)
			}
			go b.watch(id, c, conn)
		}
		close(c.done)
	}
	<-c.done
	return c.client, c.err
}

// watch closes the broker connection id, whose dial is c, once the host's
// listener is gone: its Client closed, or its process ended. Only the
// engine connects to that listener, so a connect that fails means that it
// is gone, and go-plugin announces an id once, so it never comes back. A
// connection that was lost is connected again to tell the two apart.
func (b *brokerHooks) watch(id uint32, c *brokerConn, conn *grpc.ClientConn) {
	for {
		s := conn.GetState()
		switch s {
		case connectivity.Idle:
			conn.Connect()
		case connectivity.TransientFailure:
			b.connMu.Lock()
			if b.conns[id] == c {
				delete(b.conns, id)
			}
			b.connMu.Unlock()
			_ = conn.Close()
			return
		case connectivity.Shutdown:
			return
		}
		conn.WaitForStateChange(context.Background(), s)
	}
}

// dialled is how many broker connections the engine holds.
func (b *brokerHooks) dialled() int {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	return len(b.conns)
}

// release sends the release call of the run id, whose token is token, once
// it has settled, then stops holding the token. A call that fails is the
// host's to reconcile (GetRun).
func (b *brokerHooks) release(client plugin.HostHooksClient, id, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	_, _ = client.Call(ctx, &plugin.HookCall{Hook: releasedHook, Run: id, Token: token})
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.held, token)
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
	if run, ok := b.held[token]; ok && run == id {
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

package plugin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

// DefaultPluginTimeout is the engine's default plugin_timeout_ms, after
// which Client first asks GetRun about a run's hooks registration.
const DefaultPluginTimeout = 60 * time.Second

// maxLookupInterval bounds the interval between two GetRun lookups of one
// registration.
const maxLookupInterval = 10 * time.Minute

// HookHandler answers the calls of a run's hooks (ADR 0009, hooks across
// the plugin boundary): each HookCall has a host plugin's host_call hook
// name and payload, the run's id and the site of the call. It returns the
// hook's answer, a JSON object as text in HookReply.Payload, or
// HookReply.Error. An error that it returns answers the call with the code
// plugin_error, which latches the run as a host plugin's failed hook does.
type HookHandler func(ctx context.Context, call *HookCall) (*HookReply, error)

// The codes of the HookError with which Client answers a call itself.
const (
	// HookCodeUnknownRun answers a call whose run and token name no
	// registration that Client holds: the call of a run whose registration
	// was dropped, which reaches no hook of a run in progress.
	HookCodeUnknownRun = "unknown_run"
	// HookCodePluginError answers a call whose HookHandler failed.
	HookCodePluginError = "plugin_error"
)

// releasedHook is the hook name of the engine's call that says a run has
// settled.
const releasedHook = "released"

// registration is the hooks of one run that Client serves. done closes
// when the Client drops it.
type registration struct {
	token   string
	handler HookHandler
	done    chan struct{}
}

// hooks is the state of a Client's hooks on go-plugin's broker.
type hooks struct {
	once     sync.Once
	brokerID uint32
	timeout  time.Duration

	mu     sync.Mutex
	regs   map[string]*registration
	closed bool
	done   chan struct{}
}

// RunWithHooks runs req, as Run does, with the run's hooks on go-plugin's
// broker: declaration, a host plugin's declaration as JSON, says which
// hooks the run has, and h answers their calls. The engine refuses
// declaration's around.
//
// The Client serves its hooks on one broker connection, which it opens at
// its first run with hooks. It gives a run without an id a fresh UUID and
// registers h under it with a fresh token before it sends req, and refuses
// a run whose id it holds a registration for, with ErrNotStarted. It drops
// the registration when the engine refuses the run, when the engine says
// that the run has settled, when GetRun answers gone, or at Close. After
// every call it asks GetRun about the registration, at first after the
// plugin timeout and then with a growing interval while the answer is held
// or the lookup fails; the call's own result does not change.
func (c *Client) RunWithHooks(ctx context.Context, req *RunRequest, declaration []byte, h HookHandler) (*RunResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotStarted, context.Cause(ctx))
	}
	if req == nil {
		req = &RunRequest{}
	}
	if h == nil {
		return nil, fmt.Errorf("%w: RunWithHooks without a HookHandler", ErrNotStarted)
	}
	if c.broker == nil {
		return nil, fmt.Errorf("%w: the connection has no go-plugin broker to serve the run's hooks on", ErrNotStarted)
	}
	c.hooks.once.Do(c.serveHooks)
	id := req.GetId()
	if id == "" {
		id = newUUID()
	}
	token := newUUID()
	c.hooks.mu.Lock()
	switch {
	case c.hooks.closed:
		c.hooks.mu.Unlock()
		return nil, fmt.Errorf("%w: the Client is closed", ErrNotStarted)
	case c.hooks.regs[id] != nil:
		c.hooks.mu.Unlock()
		return nil, fmt.Errorf("%w: a run with the id %s still has its hooks registered", ErrNotStarted, id)
	}
	reg := &registration{token: token, handler: h, done: make(chan struct{})}
	c.hooks.regs[id] = reg
	c.hooks.mu.Unlock()

	send := withFields(req, func(r *RunRequest) {
		r.Id = id
		r.Hooks = &Hooks{BrokerId: c.hooks.brokerID, Token: token, Declaration: declaration}
	})
	res, err := c.Run(ctx, send)
	if err != nil && errors.Is(err, ErrNotStarted) {
		c.drop(id, token)
		return nil, err
	}
	go c.reconcile(id, reg)
	return res, err
}

// Registrations is how many runs' hooks the Client holds registered.
func (c *Client) Registrations() int {
	c.hooks.mu.Lock()
	defer c.hooks.mu.Unlock()
	return len(c.hooks.regs)
}

// Close drops every registration of the Client's runs and stops asking
// GetRun about them: the engine's generation has ended. A run with hooks
// started after Close is refused.
func (c *Client) Close() {
	c.hooks.mu.Lock()
	defer c.hooks.mu.Unlock()
	if !c.hooks.closed {
		c.hooks.closed = true
		close(c.hooks.done)
	}
	for id := range c.hooks.regs {
		c.dropLocked(id)
	}
}

// serveHooks opens the Client's broker connection, on which it serves
// HostHooks for as long as the connection lives.
func (c *Client) serveHooks() {
	c.hooks.brokerID = c.broker.NextId()
	go c.broker.AcceptAndServe(c.hooks.brokerID, func(opts []grpc.ServerOption) *grpc.Server {
		s := grpc.NewServer(opts...)
		RegisterHostHooksServer(s, hookServer{c: c})
		return s
	})
}

// drop drops the registration of the run id when its token is token.
func (c *Client) drop(id, token string) {
	c.hooks.mu.Lock()
	defer c.hooks.mu.Unlock()
	if r := c.hooks.regs[id]; r != nil && r.token == token {
		c.dropLocked(id)
	}
}

// dropLocked drops the registration of the run id; c.hooks.mu is held.
func (c *Client) dropLocked(id string) {
	if r := c.hooks.regs[id]; r != nil {
		delete(c.hooks.regs, id)
		close(r.done)
	}
}

// reconcile asks GetRun about the registration reg of the run id, at
// first after the plugin timeout and then with a doubling interval while
// the engine answers held or the lookup fails, until the engine answers
// gone, the registration is dropped otherwise, or the Client's connection
// ends.
func (c *Client) reconcile(id string, reg *registration) {
	wait := c.hooks.timeout
	for {
		fired, stop := c.timer(wait)
		select {
		case <-fired:
		case <-reg.done:
			stop()
			return
		}
		if c.shutdown() {
			c.Close()
			return
		}
		res, err := c.runner.GetRun(context.Background(), &GetRunRequest{Id: id, Token: reg.token})
		if err == nil && res.GetState() == "gone" {
			c.drop(id, reg.token)
			return
		}
		wait = min(2*wait, maxLookupInterval)
	}
}

// shutdown reports whether the Client's connection has ended for good.
func (c *Client) shutdown() bool {
	cc, ok := c.conn.(*grpc.ClientConn)
	return ok && cc.GetState() == connectivity.Shutdown
}

// hookServer is the HostHooks service of a Client.
type hookServer struct {
	UnimplementedHostHooksServer
	c *Client
}

// Call answers one call of a run's hooks: the engine's release call drops
// the run's registration, a call that names no registration answers
// HookCodeUnknownRun, and the run's HookHandler answers the others.
func (s hookServer) Call(ctx context.Context, call *HookCall) (*HookReply, error) {
	s.c.hooks.mu.Lock()
	r := s.c.hooks.regs[call.GetRun()]
	if r != nil && r.token != call.GetToken() {
		r = nil
	}
	if r != nil && call.GetHook() == releasedHook {
		s.c.dropLocked(call.GetRun())
	}
	s.c.hooks.mu.Unlock()
	switch {
	case call.GetHook() == releasedHook:
		return &HookReply{}, nil
	case r == nil:
		return &HookReply{Error: &HookError{Code: HookCodeUnknownRun, Message: fmt.Sprintf("no hooks are registered for the run %s with that token", call.GetRun())}}, nil
	}
	rep, err := r.handler(ctx, call)
	if err != nil {
		return &HookReply{Error: &HookError{Code: HookCodePluginError, Message: err.Error()}}, nil
	}
	if rep == nil {
		rep = &HookReply{}
	}
	return rep, nil
}

// newUUID returns a random UUID (version 4).
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

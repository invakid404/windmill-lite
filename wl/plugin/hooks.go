package plugin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

// DefaultPluginTimeout is the engine's default plugin_timeout_ms, after
// which Client first asks GetRun about a run's hooks registration.
const DefaultPluginTimeout = 60 * time.Second

// maxLookupInterval bounds the interval between two GetRun lookups of one
// registration.
const maxLookupInterval = 10 * time.Minute

// HookHandler answers the calls of a run's hooks ([ADR 0009], hooks across
// the plugin boundary): each HookCall has a host plugin's host_call hook
// name and payload, the run's id and the site of the call. It returns the
// hook's answer, a JSON object as text in HookReply.Payload, or
// HookReply.Error. An error that it returns answers the call with the code
// plugin_error: the failure of a host plugin's hook, with that hook's
// outcome. Before the run's outcome an interception, rewrite, observer,
// first-write verdict or provider latches the run; a failed onRunEnd adds
// a warning, and a failed onRunSettled is a diagnostic line. A
// HookReply.Error with the code HookCodeUnknownRun is sent as
// plugin_error, since only Client answers that code.
//
// [ADR 0009]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0009-host-adapters.md#hooks-across-the-plugin-boundary
type HookHandler func(ctx context.Context, call *HookCall) (*HookReply, error)

// The codes of the HookError with which Client answers a call itself.
const (
	// HookCodeUnknownRun answers a call whose run and token name no
	// registration that Client holds, the engine's release call included:
	// the call of a run whose registration was dropped. The engine takes it
	// as no answer, which latches nothing: an interception or a provider
	// declines, so a mandatory provided site still fails closed. Only
	// Client answers it: a HookHandler's reply with this code is sent as
	// HookCodePluginError.
	HookCodeUnknownRun = "unknown_run"
	// HookCodePluginError answers a call whose HookHandler failed.
	HookCodePluginError = "plugin_error"
)

// releasedHook is the hook name of the engine's call that says a run has
// settled.
const releasedHook = "released"

// registration is the hooks of one run that Client serves, on the opening
// sv whose broker id the run's request names. done closes when the Client
// drops it.
type registration struct {
	token   string
	handler HookHandler
	sv      *serving
	done    chan struct{}
}

// hooks is the state of a Client's hooks on go-plugin's broker.
type hooks struct {
	timeout time.Duration
	// lookupTimeout bounds each GetRun lookup; limit is the largest
	// message of a call of the run's hooks, in either direction.
	lookupTimeout time.Duration
	limit         int

	// mu guards the rest. serving is the opening of the Client's broker
	// connection that runs use, nil before the first and after one that
	// ended or was replaced; old holds the replaced openings that
	// registrations still name. stopGeneration detaches Close from the end
	// of the engine's generation.
	mu             sync.Mutex
	serving        *serving
	old            map[*serving]bool
	stopGeneration func() bool
	regs           map[string]*registration
	closed         bool
	done           chan struct{}
}

// serving is one opening of the Client's broker connection id: ready
// closes once go-plugin has asked for its HostHooks server, and ended
// once go-plugin has stopped serving it, or failed to listen. Under
// hooks.mu, server is that server; conns counts the engine's connections
// to it, runs the registrations whose requests name id, and pending those
// whose requests are still in flight.
type serving struct {
	id      uint32
	ready   chan struct{}
	ended   chan struct{}
	server  *grpc.Server
	conns   int
	runs    int
	pending int
	retired bool
}

// stale reports whether the opening serves, while the engine holds no
// connection to it and no request that names it is in flight: the engine
// never took its address, which go-plugin drops after a while, or dropped
// its connection, and go-plugin gives the address once. hooks.mu is held.
func (sv *serving) stale() bool {
	return sv.server != nil && sv.conns == 0 && sv.pending == 0
}

// RunWithHooks runs req, as Run does, with the run's hooks on go-plugin's
// broker: declaration, a host plugin's declaration as JSON, says which
// hooks the run has, and h answers their calls. The engine refuses
// declaration's around.
//
// The Client serves its hooks on a broker connection, which it opens at
// its first run with hooks and reuses for later runs. A run waits until
// go-plugin serves the connection; one whose listen fails is refused with
// ErrNotStarted, and the next run opens a new connection. So does a run
// that finds the engine holding no connection to the current one while no
// request naming it is in flight: go-plugin gives a broker id's address
// once. The registrations that still name a replaced connection keep it
// serving; when the last of them goes, and for every connection at Close,
// the Client tells the engine (ReleaseHooks) and stops the connection's
// server. The Client refuses a run with hooks once the engine's generation
// has ended.
//
// The Client gives a run without an id a fresh UUID and registers h under
// it with a fresh token before it sends req, and refuses a run whose id it
// holds a registration for, with ErrNotStarted, as Run does. It drops the
// registration when the engine refuses the run, when the engine says that
// the run has settled, when GetRun answers gone, or at Close. The end of
// the engine's generation calls Close once the Client serves hooks. After
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
	case c.generation != nil && c.generation.Err() != nil:
		c.hooks.mu.Unlock()
		return nil, fmt.Errorf("%w: the engine's generation has ended", ErrNotStarted)
	case c.hooks.regs[id] != nil:
		c.hooks.mu.Unlock()
		return nil, fmt.Errorf("%w: a run with the id %s still has its hooks registered", ErrNotStarted, id)
	}
	sv := c.hooks.serving
	if sv != nil && sv.stale() {
		// The engine could not dial its id again: serve a new one. The
		// registrations that name the old one keep it until they go.
		c.hooks.serving = nil
		if sv.runs == 0 {
			c.retireLocked(sv)
		} else {
			c.hooks.old[sv] = true
		}
		sv = nil
	}
	if sv == nil {
		sv = c.serveHooks()
	}
	reg := &registration{token: token, handler: h, sv: sv, done: make(chan struct{})}
	c.hooks.regs[id] = reg
	sv.runs++
	sv.pending++
	c.hooks.mu.Unlock()
	defer func() {
		c.hooks.mu.Lock()
		sv.pending--
		c.hooks.mu.Unlock()
	}()

	select {
	case <-sv.ready:
	case <-sv.ended:
		c.drop(id, token)
		return nil, fmt.Errorf("%w: go-plugin's broker did not serve the connection of the run's hooks", ErrNotStarted)
	case <-reg.done:
	case <-ctx.Done():
		c.drop(id, token)
		return nil, fmt.Errorf("%w: %w", ErrNotStarted, context.Cause(ctx))
	}
	select {
	case <-reg.done:
		// Close dropped it while the connection opened.
		return nil, fmt.Errorf("%w: the Client is closed", ErrNotStarted)
	default:
	}
	send := withFields(req, func(r *RunRequest) {
		r.Id = id
		r.Hooks = &Hooks{BrokerId: sv.id, Token: token, Declaration: declaration}
	})
	res, err := c.run(ctx, send)
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

// Close drops every registration of the Client's runs, stops asking
// GetRun about them and stops serving its broker connection. The end of
// the engine's generation calls it for a Client that serves hooks, and
// Close detaches that call. A run with hooks started after Close is
// refused. Other Clients of the connection keep their own broker
// connections.
func (c *Client) Close() {
	c.hooks.mu.Lock()
	defer c.hooks.mu.Unlock()
	if !c.hooks.closed {
		c.hooks.closed = true
		close(c.hooks.done)
		if c.hooks.stopGeneration != nil {
			c.hooks.stopGeneration()
		}
		if sv := c.hooks.serving; sv != nil {
			c.retireLocked(sv)
		}
		for sv := range c.hooks.old {
			c.retireLocked(sv)
		}
	}
	for id := range c.hooks.regs {
		c.dropLocked(id)
	}
}

// retireLocked lets the opening sv go: at Close, or once a replaced
// opening's last registration has gone (retire). A server that go-plugin
// has not asked for yet is stopped when it does. c.hooks.mu is held.
func (c *Client) retireLocked(sv *serving) {
	if sv.retired {
		return
	}
	sv.retired = true
	delete(c.hooks.old, sv)
	if sv.server != nil {
		go c.retire(sv.server, sv.id)
	}
}

// retire tells the engine with ReleaseHooks that the Client has let the
// broker connection id go, and stops its server s. The first call waits
// at most the plugin timeout; then s stops at once, ending the calls that
// its handlers still run. The engine closes its side before that, so a
// call that starts after it gets no answer instead of a broken
// connection. A call that fails is sent again, at first after the plugin
// timeout and then with a doubling interval, until the engine has it,
// does not serve it, or its generation or connection has ended.
func (c *Client) retire(s *grpc.Server, id uint32) {
	release := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), c.hooks.timeout)
		defer cancel()
		_, err := c.control.ReleaseHooks(ctx, &ReleaseHooksRequest{BrokerId: id})
		return err == nil || status.Code(err) == codes.Unimplemented
	}
	done := release()
	s.Stop()
	for wait := c.hooks.timeout; !done; wait = min(2*wait, maxLookupInterval) {
		if (c.generation != nil && c.generation.Err() != nil) || c.shutdown() {
			return
		}
		fired, _ := c.timer(wait)
		<-fired
		done = release()
	}
}

// registered reports whether the Client holds a registration of the run
// id.
func (c *Client) registered(id string) bool {
	c.hooks.mu.Lock()
	defer c.hooks.mu.Unlock()
	return c.hooks.regs[id] != nil
}

// serveHooks opens the Client's broker connection, on which it serves
// HostHooks until Close, the connection's end or the next opening;
// c.hooks.mu is held. A server that go-plugin asks for after Close is
// stopped at once. Once the opening has ended, the next run opens the
// connection again.
func (c *Client) serveHooks() *serving {
	sv := &serving{id: c.broker.NextId(), ready: make(chan struct{}), ended: make(chan struct{})}
	c.hooks.serving = sv
	go func() {
		// AcceptAndServe returns without asking for the server when it
		// cannot listen.
		c.broker.AcceptAndServe(sv.id, func(opts []grpc.ServerOption) *grpc.Server {
			s := c.hooksServer(sv, opts)
			close(sv.ready)
			return s
		})
		c.hooks.mu.Lock()
		if c.hooks.serving == sv {
			c.hooks.serving = nil
		}
		delete(c.hooks.old, sv)
		c.hooks.mu.Unlock()
		close(sv.ended)
	}()
	return sv
}

// hooksServer is the HostHooks server of the opening sv, with go-plugin's
// options opts and the Client's message limit, which counts the engine's
// connections to it. One that go-plugin asks for after Close is stopped
// already, so its Serve returns at once. The first server makes
// the end of the engine's generation close the Client; a listen that
// failed leaves nothing there.
func (c *Client) hooksServer(sv *serving, opts []grpc.ServerOption) *grpc.Server {
	opts = append(opts, grpc.MaxRecvMsgSize(c.hooks.limit), grpc.MaxSendMsgSize(c.hooks.limit), grpc.StatsHandler(engineConns{c: c, sv: sv}))
	s := grpc.NewServer(opts...)
	RegisterHostHooksServer(s, hookServer{c: c})
	c.hooks.mu.Lock()
	sv.server = s
	closed := c.hooks.closed || sv.retired
	if !closed && c.generation != nil && c.hooks.stopGeneration == nil {
		// go-plugin ends the context with the engine's generation: its
		// process exited or its client was killed. A generation that has
		// ended already closes the Client at once.
		c.hooks.stopGeneration = context.AfterFunc(c.generation, c.Close)
	}
	c.hooks.mu.Unlock()
	if closed {
		s.Stop()
	}
	return s
}

// engineConns counts the engine's connections to the HostHooks server of
// the opening sv: only the engine connects to it.
type engineConns struct {
	c  *Client
	sv *serving
}

func (e engineConns) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }

func (e engineConns) HandleRPC(context.Context, stats.RPCStats) {}

func (e engineConns) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }

func (e engineConns) HandleConn(_ context.Context, s stats.ConnStats) {
	e.c.hooks.mu.Lock()
	defer e.c.hooks.mu.Unlock()
	switch s.(type) {
	case *stats.ConnBegin:
		e.sv.conns++
	case *stats.ConnEnd:
		e.sv.conns--
	}
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
		r.sv.runs--
		if r.sv.runs == 0 && c.hooks.old[r.sv] {
			c.retireLocked(r.sv)
		}
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
		select {
		case <-reg.done:
			// Dropped, also while its lookup waited.
			return
		default:
		}
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
		res, err := c.lookup(id, reg)
		if err == nil && res.GetState() == "gone" {
			c.drop(id, reg.token)
			return
		}
		wait = min(2*wait, maxLookupInterval)
	}
}

// lookup asks GetRun about the registration reg of the run id, within
// the lookup timeout; the call ends early when the registration is
// dropped.
func (c *Client) lookup(id string, reg *registration) (*GetRunResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.hooks.lookupTimeout)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-reg.done:
			cancel()
		case <-stop:
		}
	}()
	return c.runner.GetRun(ctx, &GetRunRequest{Id: id, Token: reg.token})
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
	case r == nil:
		return &HookReply{Error: &HookError{Code: HookCodeUnknownRun, Message: fmt.Sprintf("no hooks are registered for the run %s with that token", call.GetRun())}}, nil
	}
	if call.GetHook() == releasedHook {
		return &HookReply{}, nil
	}
	rep, err := r.handler(ctx, call)
	if err != nil {
		return &HookReply{Error: &HookError{Code: HookCodePluginError, Message: err.Error()}}, nil
	}
	if rep == nil {
		rep = &HookReply{}
	}
	if e := rep.GetError(); e != nil && e.GetCode() == HookCodeUnknownRun {
		// The engine takes unknown_run as no answer, which a registered
		// run's handler cannot give: its error stays a failure.
		return &HookReply{Error: &HookError{Code: HookCodePluginError, Message: e.GetMessage()}}, nil
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

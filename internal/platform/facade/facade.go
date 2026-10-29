// Package facade is the platform facade of ADR 0007 (milestones PF1 to
// PF3): the Go router behind the stock client's platform calls, attributed
// to the invocation that made each one, over the harness's intercepted
// fetch in warm and host-mode processes and over loopback TCP in fresh
// ones, with Windmill's status codes and text/plain error bodies, the 501
// policy for the routes windmill-lite does not serve, the resource and
// variable reads (PF2), and the job registry's lookups, flow user state,
// the object store and dispatch (PF3).
package facade

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// Policy is what becomes of a step that made an unsupported call
// (ADR 0007, WA-41).
type Policy string

const (
	// PolicyFail, the default: a step that settles with any recorded 501
	// fails with UnsupportedPlatformCall, whatever its own outcome.
	PolicyFail Policy = "fail"
	// PolicyObserve leaves the stock clients' behaviour standing (a 501
	// alone does not fail every helper, WA-V5), for oracle comparison.
	PolicyObserve Policy = "observe"
)

// UnsupportedPrefix begins the body of every 501 the facade answers:
// "Not implemented in windmill-lite: <METHOD> <route template>" (WA-41).
const UnsupportedPrefix = "Not implemented in windmill-lite: "

// UnknownToken is the body of the 401 for a call that names no live
// invocation (ADR 0007, transports).
const UnknownToken = "Not authorized: unknown windmill-lite invocation token"

// Caller is the invocation a call is attributed to.
type Caller struct {
	// ID is the invocation's id; Run the run it belongs to.
	ID  string
	Run string
	// Env is the invocation's job environment as the step received it
	// (ADR 0007, job identity), after host changes (H12).
	Env map[string]string
}

// Call is one platform call, attributed to its caller.
type Call struct {
	Caller *Caller
	// Route is the API operation the call matched, and Params its path
	// parameters, unescaped.
	Route  *Route
	Params map[string]string
	Method string
	// Path is the request's path, escaped as sent; Query its query.
	Path    string
	Query   url.Values
	Headers map[string]string
	Body    []byte
}

// Reply is the answer to a call. Status is a final HTTP status.
type Reply struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// Handler answers the calls of one operation. It runs on a goroutine of
// its own and may block; ctx ends when the call's transport gives up.
type Handler func(ctx context.Context, c *Call) Reply

// Record is one call as the facade recorded it: against its invocation,
// or, when it could be attributed to none, as a stray call.
type Record struct {
	// Inv is the invocation the call was attributed to, "" for a stray.
	Inv string
	// Method, Path (escaped, as sent), Query (encoded, as sent) and Body
	// are the request (WA-30/a compares these); Headers its headers, keys
	// in lower case.
	Method  string
	Path    string
	Query   string
	Headers map[string]string
	Body    []byte
	// Route names the matched route ("GET /w/{ws}/jobs_u/get/{id}"), or
	// is "" when none matched.
	Route string
	// Status is the answer's status, 0 while the call is being answered.
	Status int
	// Unsupported marks a call answered 501, and Message is that answer's
	// text, which the 501 policy's error quotes for the first such call:
	// "Not implemented in windmill-lite: <METHOD> <route template>", or a
	// handler's own, such as a refused dispatch's "dispatch target not in
	// bundle: <path>".
	Unsupported bool
	Message     string
}

// name is how a 501 names the call: its route, or, when no route of the
// API matched, its method and path as sent (without /api), so that the 501
// is as loud as any other.
func (r *Record) name() string {
	if r.Route != "" {
		return r.Route
	}
	return r.Method + " " + strings.TrimPrefix(r.Path, "/api")
}

// Config configures a Facade.
type Config struct {
	// Policy is the 501 policy; empty means PolicyFail.
	Policy Policy
	// MaxBody bounds a TCP request's body; zero means 512 MiB, the
	// protocol's largest message (ADR 0004).
	MaxBody int64
	// Recorded, if not nil, receives each invocation's calls when its
	// record is released (Session.Release): after it settled and its
	// outcome was decided.
	Recorded func(inv string, calls []Record)
	// Platform holds the services the facade's routes answer from: the
	// resources, variables and secrets of milestone PF2, and the job
	// registry, flow user state, object store and dispatcher of milestone
	// PF3. The engine that runs the steps takes the same *Services, so
	// that the runs it registers are the ones the facade looks up. nil
	// means empty services of the facade's own: every read is a
	// not-found, the registry knows only the facade's invocations, and no
	// dispatch target is in the bundle.
	Platform *platform.Services
}

// Facade is the platform facade: its routes and handlers, the registry of
// the invocations calls are attributed to, and its two transports. It is
// safe for concurrent use.
type Facade struct {
	policy   Policy
	maxBody  int64
	recorded func(string, []Record)
	platform *platform.Services
	routes   *routeTable
	handlers map[string]Handler
	warm     *reservation
	tcp      *listener

	mu     sync.Mutex
	invs   map[string]*entry
	tokens map[string]*tokenUse
	stray  []*Record
}

// tokenUse is who a token names: the one live registration that carries
// it, or none (e nil) once a second has carried it while the first was
// live, until every registration that carried it has ended.
type tokenUse struct {
	e    *entry
	live int
}

// entry is one registered invocation.
type entry struct {
	caller Caller
	token  string
	calls  []*Record
	ended  bool
	// endJob ends the invocation's job in the job registry.
	endJob func()
	// inflight counts the calls being answered; idle is closed and
	// replaced when it drops to zero.
	inflight int
	idle     chan struct{}
}

// New returns a facade with its two loopback origins: one where nothing
// listens, for warm and host-mode processes, whose fetches the harness
// intercepts, and one that listens, for fresh processes. Close releases
// them.
func New(cfg Config) (*Facade, error) {
	f := &Facade{
		policy:   cfg.Policy,
		maxBody:  cfg.MaxBody,
		recorded: cfg.Recorded,
		platform: cfg.Platform,
		routes:   defaultRoutes,
		handlers: map[string]Handler{},
		invs:     map[string]*entry{},
		tokens:   map[string]*tokenUse{},
	}
	if f.policy == "" {
		f.policy = PolicyFail
	}
	if f.maxBody == 0 {
		f.maxBody = 512 << 20
	}
	if f.platform == nil {
		f.platform = &platform.Services{}
	}
	f.serveJobs()
	f.serveProviders()
	f.serveDispatch()
	f.serveObjects()
	var err error
	if f.warm, err = reserve(); err != nil {
		return nil, err
	}
	if f.tcp, err = listen(f); err != nil {
		f.warm.close()
		return nil, err
	}
	return f, nil
}

// Close releases the facade's origins: the listening one stops serving.
func (f *Facade) Close() error {
	f.warm.close()
	return f.tcp.close()
}

// Policy returns the facade's 501 policy.
func (f *Facade) Policy() Policy { return f.policy }

// WarmOrigin is the origin for warm and host-mode processes: a loopback
// port that the facade holds but where nothing listens, so the kernel
// refuses every connection to it, and the only way to reach the facade is
// a fetch the harness intercepts (ADR 0007, ADR 0001 §5a).
func (f *Facade) WarmOrigin() string { return f.warm.origin }

// FreshOrigin is the origin for fresh processes: a loopback port where the
// facade listens, and attributes each call by the token it carries.
func (f *Facade) FreshOrigin() string { return f.tcp.origin }

// Handle registers the handler of an operation, by its operationId in the
// API's schema. A call to an operation with no handler answers 501.
// Handlers are registered before any process reaches the facade: Handle is
// not safe for concurrent use with calls.
func (f *Facade) Handle(operation string, h Handler) {
	if _, ok := f.routes.byOp[operation]; !ok {
		panic("facade: no operation " + operation)
	}
	f.handlers[operation] = h
}

// Session is one invocation's registration with the facade.
type Session struct {
	f *Facade
	e *entry
}

// Register makes an invocation known to the facade before it runs, so
// that its calls are attributed to it: by its id on the intercepted path,
// and by its token (WM_TOKEN in env) over TCP. A token that two live
// invocations carry names neither, and goes on naming none until every
// registration that carried it has ended; a later one is then its sole
// owner. Executor registers each invocation it runs. The invocation is a
// script job of the job registry (D3) until it ends: its job id is
// WM_JOB_ID (id when unset), its root WM_ROOT_JOB_ID, and the flow scopes
// it runs in become known with it.
func (f *Facade) Register(id, run string, env map[string]string) *Session {
	e := &entry{caller: Caller{ID: id, Run: run, Env: env}, token: env["WM_TOKEN"], idle: make(chan struct{})}
	job := env["WM_JOB_ID"]
	if job == "" {
		job = id
	}
	e.endJob = f.platform.JobRegistry().Begin(platform.Job{ID: job, Kind: platform.JobScript, Root: env["WM_ROOT_JOB_ID"], Path: env["WM_JOB_PATH"], Env: env})
	f.mu.Lock()
	f.invs[id] = e
	if e.token != "" {
		if u := f.tokens[e.token]; u != nil {
			u.e = nil
			u.live++
		} else {
			f.tokens[e.token] = &tokenUse{e: e, live: 1}
		}
	}
	f.mu.Unlock()
	return &Session{f: f, e: e}
}

// End ends the invocation's registration when it has settled: a later call
// with its token, or attributed to it by id, answers 401. Its record stays
// until Release.
func (s *Session) End() {
	f := s.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.e.ended {
		return
	}
	s.e.ended = true
	s.e.endJob()
	if u := f.tokens[s.e.token]; s.e.token != "" && u != nil {
		if u.live--; u.live == 0 {
			delete(f.tokens, s.e.token)
		} else if u.e == s.e {
			u.e = nil
		}
	}
}

// Release drops the invocation's record, after handing it to the
// facade's Recorded callback, once. It ends the registration too.
func (s *Session) Release() {
	s.End()
	f := s.f
	f.mu.Lock()
	if f.invs[s.e.caller.ID] != s.e {
		f.mu.Unlock()
		return
	}
	delete(f.invs, s.e.caller.ID)
	calls := copyRecords(s.e.calls)
	f.mu.Unlock()
	if f.recorded != nil {
		f.recorded(s.e.caller.ID, calls)
	}
}

// Calls returns the calls recorded against the invocation so far, in the
// order the facade accepted them.
func (s *Session) Calls() []Record {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return copyRecords(s.e.calls)
}

// Unsupported waits until no call of the invocation is being answered, or
// ctx ends, and returns the names of its unsupported calls, in order:
// "<METHOD> <route template>" as the 501 named each.
func (s *Session) Unsupported(ctx context.Context) []string {
	names, _ := s.unsupported(ctx)
	return names
}

// unsupported is Unsupported, with the message of the first unsupported
// call.
func (s *Session) unsupported(ctx context.Context) (names []string, message string) {
	f, e := s.f, s.e
	for {
		f.mu.Lock()
		if e.inflight == 0 || ctx.Err() != nil {
			for _, r := range e.calls {
				if r.Unsupported {
					if names == nil {
						message = r.Message
					}
					names = append(names, r.name())
				}
			}
			f.mu.Unlock()
			return names, message
		}
		idle := e.idle
		f.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
		}
	}
}

// Stray returns the latest calls that could be attributed to no
// invocation (an unknown or ended token, an unknown invocation id), at
// most maxStray of them. Each was answered 401.
func (f *Facade) Stray() []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyRecords(f.stray)
}

// maxStray bounds the stray calls a facade keeps.
const maxStray = 256

func copyRecords(rs []*Record) []Record {
	out := make([]Record, len(rs))
	for i, r := range rs {
		out[i] = *r
		out[i].Headers = cloneMap(r.Headers)
		out[i].Body = slices.Clone(r.Body)
	}
	return out
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// request is a call as a transport received it, before attribution.
type request struct {
	method, url string
	headers     map[string]string
	body        []byte
}

// accept attributes and records a call without blocking, and returns the
// function that answers it. byID names the invocation the harness
// attributed an intercepted call to; otherwise the call is attributed by
// its bearer token. The decision that a call is unsupported is made here,
// in the transport's order, so that the 501 policy sees every call that
// arrived before its invocation's outcome. Both transports answer through
// it, so they give a client the same reply: without a body where the
// request's method or the reply's status forbids one (bodyless).
func (f *Facade) accept(req request, byID string, intercepted bool) func(context.Context) Reply {
	serve := f.route(req, byID, intercepted)
	return func(ctx context.Context) Reply {
		rep := serve(ctx)
		if bodyless(req.method, rep.Status) {
			rep.Body = nil
		}
		return rep
	}
}

// bodyless reports whether a reply to method with status carries no body:
// the answer to HEAD, and a null-body status of the Fetch standard (204,
// 205, 304), which a Response cannot be built with a body for.
func bodyless(method string, status int) bool {
	return method == "HEAD" || status == 204 || status == 205 || status == 304
}

// route is accept without the body rule.
func (f *Facade) route(req request, byID string, intercepted bool) func(context.Context) Reply {
	u, err := url.Parse(req.url)
	if err != nil {
		u = &url.URL{Path: req.url}
	}
	rec := &Record{Method: req.method, Path: u.EscapedPath(), Query: u.RawQuery, Headers: cloneMap(req.headers), Body: req.body}
	f.mu.Lock()
	var e *entry
	if intercepted {
		e = f.invs[byID]
	} else if tok, ok := bearer(req.headers["authorization"]); ok && f.tokens[tok] != nil {
		e = f.tokens[tok].e
	}
	if e == nil || e.ended {
		rec.Status = 401
		if f.stray = append(f.stray, rec); len(f.stray) > maxStray {
			f.stray = f.stray[len(f.stray)-maxStray:]
		}
		f.mu.Unlock()
		return fixed(text(401, UnknownToken))
	}
	rec.Inv = e.caller.ID
	e.calls = append(e.calls, rec)
	call := &Call{Caller: &e.caller, Method: req.method, Path: rec.Path, Query: u.Query(), Headers: rec.Headers, Body: req.body}
	api, isAPI := strings.CutPrefix(rec.Path, "/api/")
	var h Handler
	if isAPI {
		if r, params, ok := f.routes.match(req.method, "/"+api); ok {
			call.Route, call.Params, rec.Route = r, params, r.Name()
			h = f.handlers[r.Operation]
		}
	}
	if h == nil {
		rec.Status, rec.Unsupported, rec.Message = 501, true, UnsupportedPrefix+rec.name()
		f.mu.Unlock()
		return fixed(text(501, rec.Message))
	}
	e.inflight++
	f.mu.Unlock()
	return func(ctx context.Context) (rep Reply) {
		// However the handler ends, its answer is the reply both transports
		// send and the record keeps, and the call stops counting as in
		// flight: a handler that panics or answers a status no response can
		// carry gets a 502 in its place, so one bad handler neither fails
		// the harness nor holds its invocation's outcome until the step's
		// context ends.
		defer func() {
			if p := recover(); p != nil {
				rep = Error{Variant: "BadGateway", Message: fmt.Sprintf("windmill-lite: the handler of %s failed: %v", call.Route.Name(), p)}.Reply()
			} else if rep.Status < 200 || rep.Status > 599 {
				rep = Error{Variant: "BadGateway", Message: fmt.Sprintf("windmill-lite: the handler of %s answered status %d", call.Route.Name(), rep.Status)}.Reply()
			}
			f.mu.Lock()
			rec.Status = rep.Status
			rec.Unsupported = rep.Status == 501
			if rec.Unsupported {
				rec.Message = unsupportedMessage(rec, rep)
			}
			if e.inflight--; e.inflight == 0 {
				close(e.idle)
				e.idle = make(chan struct{})
			}
			f.mu.Unlock()
		}()
		return h(ctx, call)
	}
}

// unsupportedMessage is what the 501 policy quotes of a handler's 501: its
// text/plain body, or the route's name when it has none.
func unsupportedMessage(rec *Record, rep Reply) string {
	if strings.HasPrefix(rep.Headers["content-type"], "text/plain") && len(rep.Body) > 0 {
		return string(rep.Body)
	}
	return UnsupportedPrefix + rec.name()
}

func bearer(h string) (string, bool) {
	tok, ok := strings.CutPrefix(h, "Bearer ")
	return strings.TrimSpace(tok), ok && strings.TrimSpace(tok) != ""
}

func fixed(r Reply) func(context.Context) Reply {
	return func(context.Context) Reply { return r }
}

func text(status int, body string) Reply {
	return Reply{Status: status, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte(body)}
}

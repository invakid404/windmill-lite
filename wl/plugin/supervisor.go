package plugin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// The defaults of SupervisorConfig's durations ([ADR 0009], the
// supervisor).
//
// [ADR 0009]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0009-host-adapters.md#the-supervisor
const (
	defaultAdmissionTimeout = 30 * time.Second
	defaultHealthInterval   = time.Second
	defaultHealthTimeout    = time.Second
	defaultDrainTimeout     = time.Minute
	defaultCloseGrace       = 15 * time.Second
	defaultStopTimeout      = 5 * time.Second
)

// The restart backoff is gRPC's connection backoff: the first delay is
// backoffBase, each further delay is backoffMultiplier times the one
// before, at most backoffMax, and each delay but the first is spread by up
// to backoffJitter of itself either way, still at most backoffMax. A
// generation that served for backoffReset or longer starts the delays
// again from backoffBase.
const (
	backoffBase       = time.Second
	backoffMultiplier = 1.6
	backoffJitter     = 0.2
	backoffMax        = 120 * time.Second
	backoffReset      = 60 * time.Second
)

// admissionPoll is how often admission checks health while the engine is
// not yet serving.
const admissionPoll = 100 * time.Millisecond

// ErrNotAdmitted is the error of a launch that admission refused: the
// engine did not answer health SERVING and then a build identity equal to
// the pinned one within the admission timeout.
var ErrNotAdmitted = errors.New("wl/plugin: the engine was not admitted")

// ErrNotServing is the error of a Call whose context ended before a
// generation served the call: while no generation served, or before Call
// was made. It does not start fn, so the host may retry. When a launch had
// failed, the error wraps that launch's error too.
var ErrNotServing = errors.New("wl/plugin: no generation of the engine serves")

// ErrSupervisorStopped is the error of a Call made once the context of
// Supervisor.Run has ended. It does not start fn.
var ErrSupervisorStopped = errors.New("wl/plugin: the supervisor has stopped")

// errAdmissionTimeout ends an admission's context when its timeout is up.
var errAdmissionTimeout = errors.New("the admission timeout is up")

// PinnedIdentity is the build identity of the engine that a Supervisor
// pins: the fields of the host's release record that admission compares
// with the engine's BuildInfo. Every string field is required.
type PinnedIdentity struct {
	// BundleDigest is the digest of the bundle's manifest.
	BundleDigest string
	// EngineVersion is the engine's version.
	EngineVersion string
	// BunVersion is the Bun that built the bundle's scripts.
	BunVersion string
	// ConfigDigest is the SHA-256 of the engine's configuration file, in
	// lowercase hexadecimal.
	ConfigDigest string
	// Labels are the configuration's labels. Nil and empty are the same.
	Labels map[string]string
}

// mismatch returns how info differs from id, or nil when it does not.
func (id *PinnedIdentity) mismatch(info *BuildInfo) error {
	switch {
	case info.GetBundleDigest() != id.BundleDigest:
		return fmt.Errorf("the engine serves the bundle %s, want %s", info.GetBundleDigest(), id.BundleDigest)
	case info.GetEngineVersion() != id.EngineVersion:
		return fmt.Errorf("the engine's version is %s, want %s", info.GetEngineVersion(), id.EngineVersion)
	case info.GetBunVersion() != id.BunVersion:
		return fmt.Errorf("the engine runs Bun %s, want %s", info.GetBunVersion(), id.BunVersion)
	case info.GetConfigDigest() != id.ConfigDigest:
		return fmt.Errorf("the engine's configuration has the digest %s, want %s", info.GetConfigDigest(), id.ConfigDigest)
	case !maps.Equal(info.GetLabels(), id.Labels):
		return fmt.Errorf("the engine has the labels %v, want %v", info.GetLabels(), id.Labels)
	}
	return nil
}

// SupervisorConfig is what NewSupervisor needs. A zero duration means its
// default, except for DrainTimeout, which is nil for its default.
type SupervisorConfig struct {
	// Launch is the engine's launch, which each generation makes with its
	// own ClientConfig. Its Logger also receives the supervisor's log.
	Launch Launch
	// Identity is the pinned build identity, which admission compares with
	// the engine's BuildInfo.
	Identity PinnedIdentity
	// AdmissionTimeout bounds a generation's admission, from the moment
	// go-plugin has connected: health SERVING, then a BuildInfo equal to
	// Identity. Default 30 s.
	AdmissionTimeout time.Duration
	// HealthInterval is how often the supervisor checks the health of the
	// generation that serves. Default 1 s.
	HealthInterval time.Duration
	// HealthTimeout bounds each health check. Default 1 s.
	HealthTimeout time.Duration
	// DrainTimeout is the engine's drain_timeout_ms: how long a withdrawn
	// generation's calls in flight may go on, before CloseGrace. Nil means
	// 1 minute, the engine's default when its configuration has no
	// drain_timeout_ms; a configuration's 0 is a DrainTimeout of 0.
	DrainTimeout *time.Duration
	// CloseGrace is the time after DrainTimeout that the engine has to
	// close its runtime, which cancels the runs left, and answer their
	// calls. Default 15 s.
	CloseGrace time.Duration
	// StopTimeout bounds go-plugin's Kill of a generation. Past it, the
	// supervisor sends the engine's process SIGKILL. Default 5 s.
	StopTimeout time.Duration
}

// Supervisor runs the engine as one go-plugin plugin process at a time, a
// generation, and routes the host's calls to the generation that serves
// ([ADR 0009], the supervisor). It admits a generation once the engine
// answers health SERVING and the pinned build identity, withdraws it from
// new calls for good at the first health check that does not answer
// SERVING, lets its calls in flight end, stops it and launches the next,
// after a backoff. Its methods are safe for concurrent use.
//
// [ADR 0009]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0009-host-adapters.md#the-supervisor
type Supervisor struct {
	cfg SupervisorConfig
	log hclog.Logger
	// clock times the admission, the drain, the stop and the backoff;
	// jitter returns a number in [0, 1) that spreads each backoff. Tests
	// replace both.
	clock   clock
	jitter  func() float64
	running atomic.Bool
	// drainBound is DrainTimeout plus CloseGrace, at most the longest
	// time.Duration.
	drainBound time.Duration

	mu sync.Mutex
	// serving is the generation that takes new calls, or nil.
	serving *generation
	// wake is closed, and replaced, when serving changes or the
	// supervisor stops.
	wake    chan struct{}
	stopped bool
	// runCtx is the context of Run, once Run has been called. Once it has
	// ended, Call and Ready see the supervisor stopped at once.
	runCtx context.Context
	// last is the error of the last launch that failed, until a generation
	// is admitted.
	last error
}

// generation is one launch of the engine.
type generation struct {
	n       int
	client  *goplugin.Client
	cleanup func() error
	proc    *engineProcess
	runner  *Client

	// These fields are guarded by Supervisor.mu. idle is closed once the
	// generation is withdrawn and has no call in flight.
	calls     int
	withdrawn bool
	idle      chan struct{}
}

// clock is the time of a Supervisor's timeouts and backoff.
type clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine once d has passed, unless
	// stop is called first.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// NewSupervisor returns a Supervisor of cfg, which starts the engine once
// Run is called. It refuses a Launch without Path or Config, with a SHA256
// that is not a SHA-256 in hexadecimal or with a negative MaxMessageBytes,
// ReplyMargin or PluginTimeout, as ClientConfig does; an Identity with an
// empty string field; and a negative duration of SupervisorConfig. What
// ClientConfig finds only when it creates a launch's base directory, such
// as a TempDir that does not exist, fails each launch instead, which Run
// records and retries after the backoff. NewSupervisor keeps copies of
// Launch.Env and Identity.Labels.
func NewSupervisor(cfg SupervisorConfig) (*Supervisor, error) {
	if _, err := cfg.Launch.check(); err != nil {
		return nil, err
	}
	id := cfg.Identity
	for _, f := range []struct{ name, v string }{
		{"BundleDigest", id.BundleDigest},
		{"EngineVersion", id.EngineVersion},
		{"BunVersion", id.BunVersion},
		{"ConfigDigest", id.ConfigDigest},
	} {
		if f.v == "" {
			return nil, fmt.Errorf("wl/plugin: Identity.%s is empty", f.name)
		}
	}
	cfg.Identity.Labels = maps.Clone(id.Labels)
	cfg.Launch.Env = slices.Clone(cfg.Launch.Env)
	for _, d := range []struct {
		name string
		v    *time.Duration
		def  time.Duration
	}{
		{"AdmissionTimeout", &cfg.AdmissionTimeout, defaultAdmissionTimeout},
		{"HealthInterval", &cfg.HealthInterval, defaultHealthInterval},
		{"HealthTimeout", &cfg.HealthTimeout, defaultHealthTimeout},
		{"CloseGrace", &cfg.CloseGrace, defaultCloseGrace},
		{"StopTimeout", &cfg.StopTimeout, defaultStopTimeout},
	} {
		switch {
		case *d.v < 0:
			return nil, fmt.Errorf("wl/plugin: negative SupervisorConfig.%s %v", d.name, *d.v)
		case *d.v == 0:
			*d.v = d.def
		}
	}
	drain := defaultDrainTimeout
	if cfg.DrainTimeout != nil {
		if drain = *cfg.DrainTimeout; drain < 0 {
			return nil, fmt.Errorf("wl/plugin: negative SupervisorConfig.DrainTimeout %v", drain)
		}
		cfg.DrainTimeout = &drain
	}
	// The sum saturates: a drain_timeout_ms near the longest duration that
	// the engine takes would wrap it negative.
	bound := time.Duration(math.MaxInt64)
	if drain <= bound-cfg.CloseGrace {
		bound = drain + cfg.CloseGrace
	}
	if cfg.Launch.Logger == nil {
		cfg.Launch.Logger = hclog.New(&hclog.LoggerOptions{Name: "plugin"})
	}
	return &Supervisor{
		cfg:        cfg,
		log:        cfg.Launch.Logger.Named("supervisor"),
		clock:      realClock{},
		jitter:     rand.Float64,
		drainBound: bound,
		wake:       make(chan struct{}),
	}, nil
}

// Run launches generations of the engine until ctx ends, and returns nil
// once the last generation's process has exited. The first launch starts
// at once. After a generation's process has exited, or its launch failed,
// the next launch waits gRPC's connection backoff: 1 s, then 1.6 times
// the delay before, at most 120 s, each but the first spread by up to
// 20 % either way and still at most 120 s. A generation that served for
// 60 s or more makes the next delay 1 s again.
//
// When ctx ends, Run ends a launch or a health check in progress, withdraws
// the generation that serves, lets its calls in flight go on for up to
// DrainTimeout plus CloseGrace, and stops it. Run may be called once.
func (s *Supervisor) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return errors.New("wl/plugin: Supervisor.Run was called twice")
	}
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()
	stopWaking := context.AfterFunc(ctx, s.markStopped)
	defer func() {
		stopWaking()
		s.markStopped()
	}()
	retries := 0
	for n := 1; ctx.Err() == nil; n++ {
		if n > 1 {
			if !s.sleep(ctx, s.backoff(retries)) || ctx.Err() != nil {
				break
			}
			retries++
		}
		if served := s.generation(ctx, n); served >= backoffReset {
			retries = 0
		}
	}
	return nil
}

// Call runs fn with the Client of the generation that serves, and returns
// fn's error. While no generation serves, it waits for one until ctx ends,
// and then returns ErrNotServing without calling fn; a ctx that has ended
// already calls no fn either. Once the context of Run has ended, Call
// returns ErrSupervisorStopped. fn must not keep the Client after it
// returns: the generation's process may be stopped once it is withdrawn
// and its calls have ended.
//
// A call can reach a generation that has begun to drain before a health
// check has withdrawn it. The engine then refuses a run with ErrNotStarted,
// and the host may Call again.
func (s *Supervisor) Call(ctx context.Context, fn func(*Client) error) error {
	for {
		s.mu.Lock()
		if s.stoppedLocked() {
			s.mu.Unlock()
			return ErrSupervisorStopped
		}
		if ctx.Err() != nil {
			err := s.notServingLocked(ctx)
			s.mu.Unlock()
			return err
		}
		if g := s.serving; g != nil {
			g.calls++
			s.mu.Unlock()
			defer s.release(g)
			return fn(g.runner)
		}
		wake := s.wake
		var runDone <-chan struct{}
		if s.runCtx != nil {
			runDone = s.runCtx.Done()
		}
		s.mu.Unlock()
		select {
		case <-wake:
		case <-runDone:
		case <-ctx.Done():
		}
	}
}

// stoppedLocked reports whether the context of Run has ended. The caller
// holds s.mu.
func (s *Supervisor) stoppedLocked() bool {
	return s.stopped || (s.runCtx != nil && s.runCtx.Err() != nil)
}

// notServingLocked is the error of a Call whose ctx has ended. The caller
// holds s.mu.
func (s *Supervisor) notServingLocked(ctx context.Context) error {
	if s.last != nil {
		return fmt.Errorf("%w: %w; the last launch failed: %w", ErrNotServing, context.Cause(ctx), s.last)
	}
	return fmt.Errorf("%w: %w", ErrNotServing, context.Cause(ctx))
}

// Ready reports whether a generation serves: one that admission accepted
// and that is not withdrawn, while the context of Run has not ended. A
// host's own readiness probe can answer it.
func (s *Supervisor) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serving != nil && !s.stoppedLocked()
}

// markStopped makes new calls fail with ErrSupervisorStopped.
func (s *Supervisor) markStopped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		s.stopped = true
		s.signal()
	}
}

// signal wakes the calls that wait. The caller holds s.mu.
func (s *Supervisor) signal() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// release ends a call of g.
func (s *Supervisor) release(g *generation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g.calls--
	if g.calls == 0 && g.withdrawn {
		close(g.idle)
	}
}

// backoff returns the delay before the next launch after retries delays.
// The first delay is not spread, as in gRPC's connection backoff.
func (s *Supervisor) backoff(retries int) time.Duration {
	if retries == 0 {
		return backoffBase
	}
	d := float64(backoffBase)
	for i := 0; i < retries && d < float64(backoffMax); i++ {
		d *= backoffMultiplier
	}
	d = min(d, float64(backoffMax))
	d *= 1 + backoffJitter*(2*s.jitter()-1)
	return time.Duration(min(d, float64(backoffMax)))
}

// sleep waits for d on the supervisor's clock, and reports false when ctx
// ends first.
func (s *Supervisor) sleep(ctx context.Context, d time.Duration) bool {
	fired := make(chan struct{})
	stop := s.clock.AfterFunc(d, func() { close(fired) })
	defer stop()
	select {
	case <-fired:
		return true
	case <-ctx.Done():
		return false
	}
}

// generation runs the nth generation: it launches and admits it, serves
// calls with it until a health check withdraws it or ctx ends, lets its
// calls in flight end and stops it. It returns how long the generation
// served, 0 when it was not admitted, once its process has exited.
func (s *Supervisor) generation(ctx context.Context, n int) time.Duration {
	g, err := s.launch(ctx, n)
	if err == nil {
		err = s.admit(ctx, g)
	}
	if err != nil {
		s.failed(n, err)
		if g != nil {
			s.stop(g)
		}
		return 0
	}
	admitted := s.clock.Now()
	s.mu.Lock()
	s.serving, s.last = g, nil
	s.signal()
	s.mu.Unlock()
	s.log.Info("the engine serves", "generation", n)
	why := s.watch(ctx, g)
	served := s.clock.Now().Sub(admitted)
	s.withdraw(g, why)
	s.drain(g)
	s.stop(g)
	return served
}

// launch starts the nth generation's engine and connects to it. When ctx
// ends first, it aborts the launch: the engine does not start, or gets
// SIGKILL, which ends go-plugin's wait for its handshake. It returns the
// generation also with an error, once the launch has a configuration, so
// that the caller stops it.
func (s *Supervisor) launch(ctx context.Context, n int) (*generation, error) {
	cfg, cleanup, proc, err := clientConfig(s.cfg.Launch, true)
	if err != nil {
		return nil, err
	}
	g := &generation{n: n, client: goplugin.NewClient(cfg), cleanup: cleanup, proc: proc, idle: make(chan struct{})}
	type connection struct {
		rpc goplugin.ClientProtocol
		err error
	}
	connected := make(chan connection, 1)
	go func() {
		rpc, err := g.client.Client()
		connected <- connection{rpc, err}
	}()
	var conn connection
	select {
	case conn = <-connected:
	case <-ctx.Done():
		proc.abort()
		conn = <-connected
		if conn.err == nil {
			conn.err = context.Cause(ctx)
		}
	}
	rpc, err := conn.rpc, conn.err
	if err != nil {
		return g, fmt.Errorf("start the engine: %w", err)
	}
	raw, err := rpc.Dispense(PluginName)
	if err != nil {
		return g, fmt.Errorf("dispense %s: %w", PluginName, err)
	}
	c, ok := raw.(*Client)
	if !ok {
		return g, fmt.Errorf("dispense %s: got %T, want *plugin.Client", PluginName, raw)
	}
	g.runner = c
	s.log.Info("the engine started", "generation", n, "pid", proc.runner().ID())
	return g, nil
}

// admit admits g within the admission timeout: health SERVING, then a
// BuildInfo equal to the pinned identity. Each call has a deadline within
// what is left of the timeout: a health check's is at most HealthTimeout.
func (s *Supervisor) admit(ctx context.Context, g *generation) error {
	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	start := s.clock.Now()
	stop := s.clock.AfterFunc(s.cfg.AdmissionTimeout, func() { cancel(errAdmissionTimeout) })
	defer stop()
	left := func() time.Duration {
		return s.cfg.AdmissionTimeout - s.clock.Now().Sub(start)
	}
	exited := g.proc.exited()
	poll := time.NewTimer(admissionPoll)
	defer poll.Stop()
	for {
		st, err := s.check(actx, g, min(s.cfg.HealthTimeout, left()))
		if err == nil && st == healthpb.HealthCheckResponse_SERVING {
			break
		}
		select {
		case <-actx.Done():
			return fmt.Errorf("%w: health did not answer SERVING (last %v, %v): %w", ErrNotAdmitted, st, err, context.Cause(actx))
		case <-exited:
			return fmt.Errorf("%w: the engine exited before it served", ErrNotAdmitted)
		case <-poll.C:
			poll.Reset(admissionPoll)
		}
	}
	bctx, bcancel := context.WithTimeout(actx, left())
	info, err := g.runner.BuildInfo(bctx)
	bcancel()
	if err != nil {
		if actx.Err() != nil {
			err = context.Cause(actx)
		}
		return fmt.Errorf("%w: GetBuildInfo: %w", ErrNotAdmitted, err)
	}
	if err := s.cfg.Identity.mismatch(info); err != nil {
		return fmt.Errorf("%w: %w", ErrNotAdmitted, err)
	}
	return nil
}

// check returns the health of g's engine, with the deadline d from now.
func (s *Supervisor) check(ctx context.Context, g *generation, d time.Duration) (healthpb.HealthCheckResponse_ServingStatus, error) {
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return g.runner.Health(cctx)
}

// watch checks g's health every HealthInterval, one check at a time, and
// returns why g is withdrawn: the first check that does not answer
// SERVING, the exit of its process, or the end of ctx, which also ends a
// check in progress.
func (s *Supervisor) watch(ctx context.Context, g *generation) string {
	exited := g.proc.exited()
	tick := time.NewTicker(s.cfg.HealthInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "the supervisor stops"
		case <-exited:
			return "the engine exited"
		case <-tick.C:
		}
		st, err := s.check(ctx, g, s.cfg.HealthTimeout)
		switch {
		case ctx.Err() != nil:
			return "the supervisor stops"
		case err != nil:
			return fmt.Sprintf("the health check failed: %v", err)
		case st != healthpb.HealthCheckResponse_SERVING:
			return fmt.Sprintf("health answered %v", st)
		}
	}
}

// withdraw takes g from new calls for good.
func (s *Supervisor) withdraw(g *generation, why string) {
	s.mu.Lock()
	g.withdrawn = true
	if s.serving == g {
		s.serving = nil
		s.signal()
	}
	if g.calls == 0 {
		close(g.idle)
	}
	s.mu.Unlock()
	s.log.Info("the generation is withdrawn", "generation", g.n, "reason", why)
}

// drain waits until g has no call in flight, for up to DrainTimeout plus
// CloseGrace.
func (s *Supervisor) drain(g *generation) {
	fired := make(chan struct{})
	stop := s.clock.AfterFunc(s.drainBound, func() { close(fired) })
	defer stop()
	select {
	case <-g.idle:
	case <-fired:
		s.log.Warn("the generation's calls did not end within the drain timeout and the close grace", "generation", g.n)
	}
}

// stop stops g's engine with go-plugin's Kill, bounded by StopTimeout,
// past which it sends the process SIGKILL through the launch's runner. It
// returns once the process has exited and been reaped, and the launch's
// base directory removed.
func (s *Supervisor) stop(g *generation) {
	killed := make(chan struct{})
	go func() {
		defer close(killed)
		g.client.Kill()
	}()
	fired := make(chan struct{})
	stopTimer := s.clock.AfterFunc(s.cfg.StopTimeout, func() { close(fired) })
	select {
	case <-killed:
	case <-fired:
		s.log.Warn("go-plugin's Kill did not return within the stop timeout: sending SIGKILL", "generation", g.n)
		if r := g.proc.runner(); r != nil {
			if err := r.Kill(context.Background()); err != nil {
				s.log.Error("SIGKILL failed", "generation", g.n, "error", err)
			}
		}
	}
	stopTimer()
	if exited := g.proc.exited(); exited != nil {
		<-exited
	}
	if err := g.cleanup(); err != nil {
		s.log.Warn("removing the launch's base directory failed", "generation", g.n, "error", err)
	}
	s.log.Info("the generation stopped", "generation", g.n)
}

// failed records the error of the nth launch, which failed.
func (s *Supervisor) failed(n int, err error) {
	s.mu.Lock()
	s.last = err
	s.mu.Unlock()
	s.log.Warn("the launch failed", "generation", n, "error", err)
}

package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// Key identifies a pool: one per harness class, runtime pin, dependency set
// and module-state mode (ADR 0004 §Pooling). All bundled Bun scripts share
// one dependency set, so a Bun-only bundle normally needs one pool.
type Key struct {
	Class       string
	Runtime     string
	Deps        string
	ModuleState engine.ModuleState
}

// Defaults of a PoolConfig.
const (
	DefaultLagThreshold   = 250 * time.Millisecond
	DefaultRecycleTimeout = 5 * time.Second
	DefaultRecycleSamples = 2
	defaultAffinity       = 4096
	// freshExitGrace is how long a fresh process may outlive the outcome
	// of its invocation before it is killed: the harness exits right after
	// it reports the outcome, as Windmill's wrapper does after it writes
	// the result (WR-2).
	freshExitGrace = 5 * time.Second
)

// PoolConfig configures a Pool.
type PoolConfig struct {
	Key Key
	// Process is the template for every process of the pool. Its Kind,
	// Runtime and ModuleState default to the Key's.
	Process ProcessConfig
	// Min is how many processes the pool keeps serving; Max bounds them.
	// Zero values mean one each: Bun steps are mostly asynchronous I/O and
	// share one event loop. Processes that are tainted or being recycled
	// do not count toward Max: they are leaving the pool. A fresh pool
	// starts one process per invocation and keeps none, so it uses neither,
	// nor Cap, LagThreshold, RecycleTimeout, MaxLive or Recycle.
	Min, Max int
	// Cap bounds the invocations in flight per process (deviation D15);
	// zero means no cap, the default. Abandoned invocations never count,
	// and neither do host calls (HostCall, Plugin), which a step can wait
	// for, though they keep a leaving process from draining. The pool's
	// user records D15 in the run trace when a cap is set (wl does).
	Cap int
	// LagThreshold is the event-loop lag past which a process counts as
	// busy (a CPU-bound step): new invocations go elsewhere, and the pool
	// starts another process if it is below Max. Zero means
	// DefaultLagThreshold; a negative value turns lag routing off.
	LagThreshold time.Duration
	// RecycleTimeout bounds the drain of a leaving process (tainted,
	// surplus or recycled) before it is killed; zero means
	// DefaultRecycleTimeout. The drain starts once nothing the engine
	// waits for is in flight in the process.
	RecycleTimeout time.Duration
	// MaxLive bounds the processes alive at once, each counted from the
	// start of its spawn until its exit is observed: serving, starting,
	// tainted, recycled, surplus or draining. The pool starts no process
	// while MaxLive are alive. Routing does not change: an invocation goes
	// at once to a serving process that can take it, and waits only when
	// none can and the process it needs cannot start. Zero means no bound;
	// a bound below Max is refused, since the pool could not reach its
	// size.
	MaxLive int
	// Recycle are the triggers that replace a process before it dies
	// (ADR 0004 §Pooling); their zero values turn them off.
	Recycle RecycleTriggers

	// observe, when not nil, is told of the pool's events (poolEvent)
	// under its lock, so it must not call the pool. admitting, when not
	// nil, is called with each process that has started, before the pool
	// takes its lock to admit it. Tests set them.
	observe   func(poolEvent)
	admitting func(*Process)
}

// RecycleTriggers say when a warm pool replaces a process that is still
// serving. A process trips a trigger when its heartbeats report a resident
// set of RSS bytes or more Samples times in a row, or once the engine has
// sent it invocations of more than Modules distinct scripts, whose modules
// it keeps loaded.
//
// A process that trips takes no new invocations, a replacement starts
// beside it outside Max, and it leaves once nothing the engine waits for
// is in flight in it. One process leaves for a trigger at a time: another
// that trips meanwhile keeps serving, and leaves after the first has
// exited. A threshold measures and does not cap: a process can pass it
// between two heartbeats, and while it waits to leave.
type RecycleTriggers struct {
	// RSS is the resident set size, in bytes, that trips the trigger; zero
	// turns it off.
	RSS int64
	// Samples is how many health reports in a row must reach RSS; zero
	// means DefaultRecycleSamples. A health report is a harness's
	// heartbeat ping or its pong to the engine's ping.
	Samples int
	// Modules is how many distinct scripts a process may run, counted from
	// the invocations dispatched to it; the first invocation of one more
	// still runs there, and then the process trips. Invocations picked for
	// it before it trips still run there too. Zero turns it off.
	Modules int
}

// poolEvent is something a pool did, which PoolConfig.observe is told.
type poolEvent struct {
	// kind is "spawn" (a process starts to spawn), "failed" (a spawn
	// failed, and its process, if any, has exited), "exit" (the exit of a
	// member is observed), "drain" (a leaving member starts to drain) or
	// "wait" (an invocation waits for a process).
	kind string
	p    *Process // exit and drain
}

// ErrPoolClosed is returned by Execute after Close.
var ErrPoolClosed = errors.New("harness: pool closed")

// Pool runs invocations on a set of spawned harness processes. It
// implements engine.StepExecutor. A pool whose key is fresh
// (engine.ModuleStateFresh, ADR 0006) starts one process for each
// invocation, which ends with it, and is killed when the engine stops
// waiting; the rules below are a warm pool's.
//
//   - Routing prefers the process the run used last (affinity), then the
//     least-loaded one. A process whose event loop lags past LagThreshold
//     is used only when no other can take the invocation and no new one
//     can be started.
//   - An abandoned invocation (deviation D2) taints its process: it gets no
//     new invocations, returns to service when every abandoned invocation
//     in it has settled, and is drained and replaced once nothing else is
//     in flight in it, so no unrelated step is killed.
//   - A process that dies, or that the watchdog kills, fails its in-flight
//     invocations as escaped and is replaced up to Min.
//   - A process that trips a recycle trigger (PoolConfig.Recycle) takes no
//     new invocations, and is drained and replaced once nothing else is in
//     flight in it.
//   - With MaxLive set, no process starts while that many are alive.
type Pool struct {
	cfg PoolConfig

	mu       sync.Mutex
	bg       sync.WaitGroup // drains of processes that started after Close
	members  []*member
	starting int
	spawning *spawnAttempt
	closed   bool
	changed  chan struct{}
	affinity runMap
	// fresh holds a fresh pool's processes until they exit.
	fresh map[*Process]struct{}
	// plugin is the host plugin declaration of the first process that
	// started with one; a spawn that begins after it is recorded must
	// carry the same declaration. nil until then.
	plugin []byte
	// recycler is the member that leaves for a recycle trigger, until its
	// exit is observed; nil when none does. owed counts the replacements
	// of members that left for a trigger, which start as soon as MaxLive
	// lets them, unless the pool reaches Max first.
	recycler *member
	owed     int
}

type member struct {
	p *Process
	// active counts what the engine waits for in the process: invocations
	// and host calls. steps counts the invocations alone, which the cap
	// bounds.
	active int
	steps  int
	// tainted: it has unsettled abandoned invocations. surplus: its taint
	// cleared while the pool already had Max processes serving without it.
	// Either way it takes no new invocations and leaves once idle.
	tainted  bool
	surplus  bool
	retiring bool
	gone     bool
	// tripped: a recycle trigger tripped, which stays so. recycling: it
	// holds the pool's recycle slot (Pool.recycler), takes no new
	// invocations and leaves once idle.
	tripped   bool
	recycling bool
}

func (m *member) inService() bool {
	return !m.tainted && !m.surplus && !m.retiring && !m.gone && !m.recycling
}

type spawnAttempt struct {
	done chan struct{}
	err  error
	cfg  ProcessConfig // the process's, as the pool held it when it began
}

// NewPool returns a pool; processes start on the first Execute, or on
// Start.
func NewPool(cfg PoolConfig) (*Pool, error) {
	pc := &cfg.Process
	// The key and the process template must agree; either may leave the
	// harness class and the runtime pin to the other.
	for _, f := range [][2]*string{{&pc.Kind, &cfg.Key.Class}, {&pc.Runtime, &cfg.Key.Runtime}} {
		if *f[0] == "" {
			*f[0] = *f[1]
		} else if *f[1] == "" {
			*f[1] = *f[0]
		}
	}
	if pc.Kind != cfg.Key.Class || pc.Runtime != cfg.Key.Runtime {
		return nil, fmt.Errorf("harness: pool key (%q, %q) differs from the process's harness class and runtime (%q, %q)",
			cfg.Key.Class, cfg.Key.Runtime, pc.Kind, pc.Runtime)
	}
	if cfg.Key.ModuleState == "" {
		cfg.Key.ModuleState = engine.ModuleStateWarm
	}
	if pc.ModuleState == "" {
		pc.ModuleState = cfg.Key.ModuleState
	}
	if pc.ModuleState != cfg.Key.ModuleState {
		return nil, fmt.Errorf("harness: pool key module state %q differs from the process's %q", cfg.Key.ModuleState, pc.ModuleState)
	}
	if cfg.Key.ModuleState != engine.ModuleStateWarm && cfg.Key.ModuleState != engine.ModuleStateFresh {
		return nil, fmt.Errorf("harness: unknown module-state mode %q", cfg.Key.ModuleState)
	}
	if err := cfg.CheckSettings(); err != nil {
		return nil, err
	}
	cfg.withDefaults()
	if pc.Diagnostics != nil {
		// Every process of the pool, and the pool itself, write to it.
		pc.Diagnostics = &syncWriter{w: pc.Diagnostics}
	}
	pl := &Pool{cfg: cfg, changed: make(chan struct{}), affinity: runMap{limit: defaultAffinity}, fresh: map[*Process]struct{}{}}
	if !pl.isFresh() {
		pl.cfg.Process.notify = pl.processChanged
		pl.cfg.Process.rssLimit = cfg.Recycle.RSS
		pl.cfg.Process.countScripts = cfg.Recycle.Modules > 0
	}
	return pl, nil
}

// withDefaults gives the zero settings their defaults.
func (cfg *PoolConfig) withDefaults() {
	if cfg.Max == 0 {
		cfg.Max = 1
	}
	if cfg.Min == 0 {
		cfg.Min = 1
	}
	if cfg.LagThreshold == 0 {
		cfg.LagThreshold = DefaultLagThreshold
	}
	if cfg.RecycleTimeout == 0 {
		cfg.RecycleTimeout = DefaultRecycleTimeout
	}
	if cfg.Recycle.Samples == 0 {
		cfg.Recycle.Samples = DefaultRecycleSamples
	}
}

// CheckSettings checks the settings that size a warm pool and recycle its
// processes, after their defaults, as NewPool does: 0 <= Min <= Max,
// Max >= 1, Cap >= 0, MaxLive zero or at least Max, and no negative
// recycle trigger.
func (cfg PoolConfig) CheckSettings() error {
	cfg.withDefaults()
	switch {
	case cfg.Min < 0 || cfg.Max < 1 || cfg.Min > cfg.Max || cfg.Cap < 0:
		return fmt.Errorf("harness: pool needs 0 <= min <= max, max >= 1 and cap >= 0 (min %d, max %d, cap %d)", cfg.Min, cfg.Max, cfg.Cap)
	case cfg.MaxLive < 0:
		return fmt.Errorf("harness: pool's MaxLive %d is negative", cfg.MaxLive)
	case cfg.MaxLive > 0 && cfg.MaxLive < cfg.Max:
		return fmt.Errorf("harness: pool's MaxLive %d is below its Max %d: the pool could not reach its size", cfg.MaxLive, cfg.Max)
	case cfg.Recycle.RSS < 0 || cfg.Recycle.Samples < 0 || cfg.Recycle.Modules < 0:
		return fmt.Errorf("harness: pool's recycle triggers cannot be negative (RSS %d, Samples %d, Modules %d)",
			cfg.Recycle.RSS, cfg.Recycle.Samples, cfg.Recycle.Modules)
	}
	return nil
}

func (pl *Pool) isFresh() bool { return pl.cfg.Key.ModuleState == engine.ModuleStateFresh }

// Start spawns the pool's Min processes and waits until Min serve, those
// that others started included. It returns the error of a start it made
// that failed. A fresh pool starts none ahead.
func (pl *Pool) Start(ctx context.Context) error {
	if pl.isFresh() {
		return nil
	}
	var mine []*spawnAttempt
	for {
		for _, a := range mine {
			select {
			case <-a.done:
				if a.err != nil {
					return a.err
				}
			default:
			}
		}
		pl.mu.Lock()
		if pl.closed || pl.inServiceLocked() >= pl.cfg.Min {
			pl.mu.Unlock()
			return nil
		}
		mine = append(mine, pl.fillLocked()...)
		ch := pl.changed
		pl.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

// Execute implements engine.StepExecutor.
func (pl *Pool) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	if inv.Report == nil {
		inv.Report = engine.NopReporter{}
	}
	if ms := inv.ModuleState; ms != "" && ms != pl.cfg.Key.ModuleState {
		inv.Report.Settled(engine.SettleNotStarted)
		return engine.StepOutcome{}, fmt.Errorf("harness: invocation %q runs in module state %q, the pool in %q", inv.ID, ms, pl.cfg.Key.ModuleState)
	}
	if pl.isFresh() {
		return pl.executeFresh(ctx, inv)
	}
	for {
		m, err := pl.acquire(ctx, inv.Run, true)
		if err != nil {
			inv.Report.Settled(engine.SettleNotStarted)
			return engine.StepOutcome{}, err
		}
		// The slot is freed as soon as the engine stops waiting for the
		// invocation, before its Reporter learns the outcome, or else here.
		var once sync.Once
		release := func(unavailable bool) { once.Do(func() { pl.release(m, unavailable, true) }) }
		out, d, err := m.p.exec(ctx, inv, func() { release(false) })
		unavailable := d == notDispatched && errors.Is(err, errUnavailable)
		release(unavailable)
		if d == notDispatched {
			if unavailable {
				// The process stopped serving between the pick and the
				// send; pick again.
				continue
			}
			inv.Report.Settled(engine.SettleNotStarted)
		}
		return out, err
	}
}

// executeFresh runs inv in a process of its own (ADR 0006): it starts one,
// runs the invocation, and leaves it to exit, which it does right after it
// reports the outcome; a process that outlives the outcome by
// freshExitGrace is killed. When ctx ends first, exec kills the process.
func (pl *Pool) executeFresh(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	pl.mu.Lock()
	if pl.closed {
		pl.mu.Unlock()
		inv.Report.Settled(engine.SettleNotStarted)
		return engine.StepOutcome{}, ErrPoolClosed
	}
	pl.starting++
	pl.mu.Unlock()
	p, err := Spawn(ctx, pl.cfg.Process)
	pl.mu.Lock()
	pl.starting--
	closed := pl.closed
	if err == nil && !closed {
		pl.fresh[p] = struct{}{}
		// Close waits for these, as long as its context lasts.
		pl.bg.Add(1)
	}
	pl.broadcastLocked()
	pl.mu.Unlock()
	if err != nil {
		inv.Report.Settled(engine.SettleNotStarted)
		return engine.StepOutcome{}, err
	}
	if closed {
		// Close may have stopped waiting for this start already.
		p.Kill()
		inv.Report.Settled(engine.SettleNotStarted)
		return engine.StepOutcome{}, ErrPoolClosed
	}
	outcome := make(chan struct{})
	go func() {
		defer pl.bg.Done()
		select {
		case <-p.Done():
		case <-outcome:
			select {
			case <-p.Done():
			case <-time.After(freshExitGrace):
				p.kill("a fresh process outlived its invocation's outcome")
				<-p.Done()
			}
		}
		pl.mu.Lock()
		delete(pl.fresh, p)
		pl.broadcastLocked()
		pl.mu.Unlock()
	}()
	out, d, err := p.exec(ctx, inv, nil)
	switch d {
	case notDispatched:
		p.Kill()
		inv.Report.Settled(engine.SettleNotStarted)
	case completed:
		close(outcome)
	}
	return out, err
}

// acquire picks a process for run's invocation (step) or host call, and
// counts it as active in the process; release ends it.
func (pl *Pool) acquire(ctx context.Context, run string, step bool) (*member, error) {
	var spawnErr error
	for {
		pl.mu.Lock()
		if pl.closed {
			pl.mu.Unlock()
			return nil, ErrPoolClosed
		}
		// The first Execute establishes the minimum, as Start would.
		if spawnErr == nil {
			pl.fillLocked()
		}
		m, lagging := pl.pickLocked(run, step)
		canSpawn := spawnErr == nil && pl.inServiceLocked()+pl.starting < pl.cfg.Max && pl.roomLocked()
		if m != nil && (!lagging || !canSpawn) {
			m.active++
			if step {
				m.steps++
			}
			pl.affinity.set(run, m)
			pl.mu.Unlock()
			return m, nil
		}
		if canSpawn && pl.spawning == nil {
			pl.spawnLocked()
		}
		att, ch := pl.spawning, pl.changed
		pl.observeLocked("wait", nil)
		pl.mu.Unlock()
		if att == nil && spawnErr != nil {
			return nil, spawnErr
		}
		var attDone <-chan struct{}
		if att != nil {
			attDone = att.done
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-ch:
		case <-attDone:
		}
		// spawn closes done before it broadcasts, so a failed start is
		// seen whichever wake-up came first.
		if att != nil {
			select {
			case <-att.done:
				if att.err != nil {
					spawnErr = att.err
				}
			default:
			}
		}
	}
}

// pickLocked returns the best process for an invocation of run (step) or
// a host call of run, and whether it lags. Only serving, untainted
// processes are candidates, and for an invocation only those under the
// cap.
func (pl *Pool) pickLocked(run string, step bool) (*member, bool) {
	usable := func(m *member) bool {
		return m.inService() && (!step || pl.cfg.Cap == 0 || m.steps < pl.cfg.Cap)
	}
	lags := func(m *member) bool { return pl.cfg.LagThreshold > 0 && m.p.Lag() > pl.cfg.LagThreshold }
	if a, ok := pl.affinity.get(run); ok && usable(a) && !lags(a) {
		return a, false
	}
	var best, bestLagging *member
	for _, m := range pl.members {
		switch {
		case !usable(m):
		case lags(m):
			if bestLagging == nil || m.active < bestLagging.active {
				bestLagging = m
			}
		case best == nil || m.active < best.active:
			best = m
		}
	}
	if best != nil {
		return best, false
	}
	return bestLagging, bestLagging != nil
}

func (pl *Pool) inServiceLocked() int {
	n := 0
	for _, m := range pl.members {
		if m.inService() {
			n++
		}
	}
	return n
}

// roomLocked reports whether MaxLive lets another process start: every
// member counts until its exit is observed, and so does every spawn under
// way.
func (pl *Pool) roomLocked() bool {
	return pl.cfg.MaxLive == 0 || len(pl.members)+pl.starting < pl.cfg.MaxLive
}

// fillLocked starts the replacements owed to members that left for a
// recycle trigger, then processes until Min serve or are starting, as far
// as MaxLive lets it, and returns the attempts it made. A closed pool
// starts none.
func (pl *Pool) fillLocked() []*spawnAttempt {
	var atts []*spawnAttempt
	for !pl.closed && pl.owed > 0 && pl.roomLocked() {
		if pl.inServiceLocked()+pl.starting >= pl.cfg.Max {
			// The pool is full without them.
			pl.owed = 0
			break
		}
		atts = append(atts, pl.spawnLocked())
		pl.owed--
	}
	for !pl.closed && pl.inServiceLocked()+pl.starting < pl.cfg.Min && pl.roomLocked() {
		atts = append(atts, pl.spawnLocked())
	}
	return atts
}

func (pl *Pool) observeLocked(kind string, p *Process) {
	if pl.cfg.observe != nil {
		pl.cfg.observe(poolEvent{kind: kind, p: p})
	}
}

func (pl *Pool) spawnLocked() *spawnAttempt {
	pl.observeLocked("spawn", nil)
	att := &spawnAttempt{done: make(chan struct{}), cfg: pl.cfg.Process}
	// A spawn that begins after the pool has recorded a host plugin
	// declaration is held to it; spawns already under way are not.
	if pl.plugin != nil {
		att.cfg.ExpectPlugin = pl.plugin
	}
	pl.starting++
	if pl.spawning == nil {
		pl.spawning = att
	}
	go pl.spawn(att)
	return att
}

func (pl *Pool) spawn(att *spawnAttempt) {
	p, err := Spawn(context.Background(), att.cfg)
	if err == nil && pl.cfg.admitting != nil {
		pl.cfg.admitting(p)
	}
	if err == nil && !pl.declares(p) {
		// It began to start before the pool recorded the declaration, so
		// its handshake could not hold it to it (ExpectPlugin). It still
		// counts as starting until it has exited.
		p.Kill()
		<-p.Done()
		err = errors.New("harness: the process declared another host plugin than the pool's first process with one")
	}
	pl.mu.Lock()
	pl.starting--
	if err == nil && !pl.closed && p.hasExited() {
		// Its exit came before this admission, and found no member to
		// remove: admitted, it would hold a place in the pool for good.
		err = errors.New("harness: the process exited right after its handshake")
	}
	att.err = err
	if err == nil {
		if pl.closed {
			// Close may have stopped waiting for this start already; it waits
			// for this drain as long as its context lasts.
			pl.bg.Add(1)
			go func() {
				defer pl.bg.Done()
				drainWithin(p, pl.cfg.RecycleTimeout)
			}()
		} else {
			// The pool may have reached Max while this process started, by a
			// taint that cleared; then the newcomer is the surplus one,
			// which has no work to lose.
			m := &member{p: p, surplus: pl.inServiceLocked() >= pl.cfg.Max}
			pl.members = append(pl.members, m)
			pl.reviewLocked(m)
		}
	} else {
		// The caller that wanted this process gets err; a replacement
		// nobody waits for is only logged, and the next Execute tries again.
		pl.observeLocked("failed", nil)
		pl.diag().line("windmill-lite: harness pool: starting a process failed: " + err.Error())
	}
	if pl.spawning == att {
		pl.spawning = nil
	}
	close(att.done)
	pl.broadcastLocked()
	pl.mu.Unlock()
}

// declares records the host plugin declaration of the first process that
// started with one, and reports whether p's declaration is the recorded
// one, byte for byte, as a handshake with ExpectPlugin checks it.
func (pl *Pool) declares(p *Process) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	d := p.Hello().Plugin
	if pl.plugin == nil {
		if !d.IsZero() {
			pl.plugin = slices.Clone(d.Producer())
		}
		return true
	}
	return bytes.Equal(d.Producer(), pl.plugin)
}

// release ends an acquire of m: an invocation (step) or a host call. A
// process found not serving is leaving the pool (it is dying or draining),
// so it is not picked again.
func (pl *Pool) release(m *member, unavailable, step bool) {
	pl.mu.Lock()
	m.active--
	if step {
		m.steps--
	}
	if unavailable {
		m.retiring = true
	}
	pl.reviewLocked(m)
	pl.broadcastLocked()
	pl.mu.Unlock()
}

func (pl *Pool) diag() *diagnostics {
	w := pl.cfg.Process.Diagnostics
	if w == nil {
		w = os.Stderr
	}
	return &diagnostics{w: w}
}

// processChanged is every process's notify callback.
func (pl *Pool) processChanged(p *Process) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	for i, m := range pl.members {
		if m.p != p {
			continue
		}
		if p.hasExited() {
			m.gone = true
			pl.members = append(pl.members[:i], pl.members[i+1:]...)
			pl.observeLocked("exit", p)
			if pl.recycler == m {
				// The recycle slot frees at the observed exit; the next
				// process that tripped meanwhile takes it.
				pl.recycler = nil
				for _, o := range pl.members {
					if o.tripped && o.inService() {
						pl.reviewLocked(o)
						break
					}
				}
			}
			pl.fillLocked()
		} else {
			pl.reviewLocked(m)
		}
		pl.broadcastLocked()
		return
	}
}

// reviewLocked applies the taint rules of ADR 0004 §Cancellation item 4,
// keeps a process whose taint cleared from taking the pool past Max, and
// applies the recycle triggers.
func (pl *Pool) reviewLocked(m *member) {
	if m.retiring || m.gone {
		return
	}
	_, abandoned := m.p.Load()
	switch {
	case abandoned > 0:
		m.tainted = true
	case m.tainted:
		// While tainted, m does not count as serving; a process still
		// starting does, since it will be admitted.
		m.surplus = pl.inServiceLocked()+pl.starting >= pl.cfg.Max
		m.tainted = false
	}
	if !m.tripped && pl.trippedLocked(m) {
		m.tripped = true
	}
	if m.tripped && m.inService() && pl.recycler == nil {
		// It leaves for its trigger now, and its replacement starts beside
		// it, also in a pool grown past Min.
		m.recycling = true
		pl.recycler = m
		pl.owed++
		pl.fillLocked()
	}
	if (m.tainted || m.surplus || m.recycling) && m.active == 0 {
		m.retiring = true
		pl.fillLocked()
		pl.observeLocked("drain", m.p)
		go drainWithin(m.p, pl.cfg.RecycleTimeout)
	}
}

// trippedLocked reports whether m has reached a recycle trigger.
func (pl *Pool) trippedLocked(m *member) bool {
	r := pl.cfg.Recycle
	return (r.RSS > 0 && m.p.rssStreak() >= r.Samples) || (r.Modules > 0 && m.p.scriptCount() > r.Modules)
}

func (pl *Pool) broadcastLocked() {
	close(pl.changed)
	pl.changed = make(chan struct{})
}

// Close stops routing, waits for the invocations the engine still waits
// for (until ctx ends), then drains every process; a process that has not
// exited when ctx ends is killed.
func (pl *Pool) Close(ctx context.Context) error {
	pl.mu.Lock()
	pl.closed = true
	pl.broadcastLocked()
	pl.mu.Unlock()
	var err error
	for err == nil {
		pl.mu.Lock()
		busy := pl.starting > 0 || len(pl.fresh) > 0
		for _, m := range pl.members {
			busy = busy || m.active > 0
		}
		ch := pl.changed
		pl.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-ch:
		case <-ctx.Done():
			// Stop waiting; the drains below kill what is left.
			err = context.Cause(ctx)
		}
	}
	pl.mu.Lock()
	members := append([]*member(nil), pl.members...)
	fresh := make([]*Process, 0, len(pl.fresh))
	for p := range pl.fresh {
		fresh = append(fresh, p)
	}
	pl.mu.Unlock()
	var wg sync.WaitGroup
	for _, m := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.p.Drain(ctx)
		}()
	}
	// A fresh process still running when ctx ended is killed: it is its
	// invocation, which the engine no longer waits for.
	for _, p := range fresh {
		p.Kill()
		<-p.Done()
	}
	wg.Wait()
	bgDone := make(chan struct{})
	go func() {
		pl.bg.Wait()
		close(bgDone)
	}()
	select {
	case <-bgDone:
	case <-ctx.Done():
		if err == nil {
			err = context.Cause(ctx)
		}
	}
	return err
}

// ProcessStats is a snapshot of one pool process.
type ProcessStats struct {
	PID       int
	Active    int
	Abandoned int
	Tainted   bool
	Surplus   bool
	Retiring  bool
	Lag       time.Duration
	Health    *Health
}

// Stats returns a snapshot of the pool's processes.
func (pl *Pool) Stats() []ProcessStats {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	out := make([]ProcessStats, 0, len(pl.members))
	for _, m := range pl.members {
		_, ab := m.p.Load()
		out = append(out, ProcessStats{
			PID: m.p.PID(), Active: m.active, Abandoned: ab, Tainted: m.tainted, Surplus: m.surplus, Retiring: m.retiring,
			Lag: m.p.Lag(), Health: m.p.Health(),
		})
	}
	return out
}

// drainWithin drains p, killing it if it has not exited after d.
func drainWithin(p *Process, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	p.Drain(ctx)
}

// runMap remembers the process each run used last, for affinity. It is
// bounded: the oldest runs are forgotten first.
type runMap struct {
	limit int
	m     map[string]*member
	order []string
}

func (r *runMap) set(run string, m *member) {
	if run == "" {
		return
	}
	if r.m == nil {
		r.m = map[string]*member{}
	}
	if _, ok := r.m[run]; !ok {
		r.order = append(r.order, run)
	}
	r.m[run] = m
	for len(r.order) > r.limit {
		delete(r.m, r.order[0])
		r.order = r.order[1:]
	}
}

func (r *runMap) get(run string) (*member, bool) {
	m, ok := r.m[run]
	return m, ok
}

var _ engine.StepExecutor = (*Pool)(nil)
var _ engine.StepExecutor = (*Process)(nil)

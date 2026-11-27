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
	// Cap bounds the invocations in flight per process; zero means no cap,
	// the default. A cap is one of the named deviations of ADR 0001, which
	// the pool's user lists in each run's trace (wl does). Abandoned
	// invocations never count, and neither do host calls (HostCall,
	// Plugin), which a step can wait for, though they keep a leaving
	// process from draining.
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
	// OnStartFailure, when not nil, is called with the error of each
	// process that the pool fails to start, while it is not closed, after
	// the pool has logged it. It is called without the pool's lock, so it
	// may call the pool, and it must not block for long. Start and Execute
	// still return the error to the caller that wanted the process.
	OnStartFailure func(error)

	// observe, when not nil, is told of the pool's events (poolEvent)
	// under its lock, so it must not call the pool. admitting, when not
	// nil, is called with each process that has started, before the pool
	// takes its lock to admit it. Tests set them.
	observe   func(poolEvent)
	admitting func(*Process)
	// now, when not nil, is the clock that times the processes' late host
	// calls (UnsettledRun). Tests set it.
	now func() time.Time
}

// RecycleTriggers say when a warm pool replaces a process that is still
// serving. A process trips a trigger when its heartbeats report a resident
// set of RSS bytes or more Samples times in a row, once the engine has
// sent it invocations of more than Modules distinct scripts, whose modules
// it keeps loaded, when a hook of an ended run has held that run unsettled
// for UnsettledRun, or when its host plugin reports a resource held for
// HeldAge after its run ended. The last two are reviewed at each health
// report.
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
	// it before that one was dispatched still run there too. Zero turns it
	// off.
	Modules int
	// UnsettledRun is how long a host plugin hook that the engine stopped
	// waiting for may still run, holding its run unsettled (ADR 0004 §Host
	// plugins item 2), counted from when the engine stopped waiting, which
	// ends the run. onRunSettled, whose run has settled, does not count.
	// Zero turns it off.
	UnsettledRun time.Duration
	// HeldAge is the age, reported in the process's health by its host
	// plugin (heldAge in the Bun harness), of the oldest resource the
	// plugin still holds after that resource's run ended, that trips the
	// trigger. A plugin that reports nothing never trips it. Zero turns it
	// off.
	HeldAge time.Duration
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

// ErrPoolClosed is returned by Execute after Close, and by a warm pool's
// Start.
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
	// secrets are the processes' secrets (ProcessConfig.Secrets), which
	// the pool masks in its diagnostics and in each error that one of its
	// methods returns; nil when there are none.
	secrets *Secrets

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
	// plugin is the host plugin declaration that a spawn beginning after
	// it is recorded must carry (ExpectPlugin); nil while there is none.
	// A pool without settings records the first declaration a started
	// process carries. A pool with settings (opted) records, as declared,
	// what the first process it admits carries, a declaration or none, and
	// admits only processes that agree with it.
	plugin   []byte
	declared bool
	// opted says that the pool's user set one of its settings (Min, Max,
	// Cap, LagThreshold, RecycleTimeout, MaxLive, Recycle). Only then do
	// the admission checks apply and the pool serialize its diagnostics:
	// a pool without settings behaves as it always has.
	opted bool
	// pins are the pinned runs (PinRun), by run id: the pin of the latest
	// run admitted with each id.
	pins map[string]*Pin
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
	// bounds, and runs counts them by run.
	active int
	steps  int
	runs   map[string]int
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
	// early: its process had exited when the pool admitted it, which only
	// a pool without settings does.
	early bool
}

func (m *member) inService() bool {
	return !m.tainted && !m.surplus && !m.retiring && !m.gone && !m.recycling
}

// pin is a pinned run: the member that served its first invocation or
// host call (nil until then), and whether the run has ended, after which
// the pin still routes its late host calls but no longer holds the member.
// Pin is one admitted run's pin (PinRun), which RunEnded and ForgetRun
// take. A call whose context carries it (WithPin) routes by it, whatever
// later run took the same id; any other call of the run routes by the pin
// that its id names.
type Pin struct {
	pool *Pool
	run  string
	// unpinned marks an unpinned run's routing choice (Unpinned): its
	// calls go through the ordinary pick, never by the pin of its id.
	unpinned bool
	m        *member
	// ended: the run has ended (RunEnded), so the pin no longer holds m.
	ended bool
	// active counts the calls that the pin sent to m and that have not
	// been released; reached is set once one of them reached m. Until one
	// has, a call that m refused before dispatch frees the pin to bind
	// again, when no other is under way.
	active  int
	reached bool
	// refused: m refused a late call after the run ended; later ones then
	// route as any call does.
	refused bool
}

// pinKey carries a Pin in a context (WithPin).
type pinKey struct{}

// ErrPinLive is PinRun's error for an id whose pin a live run holds, such
// as a run that a step dispatched: one id has one live run.
var ErrPinLive = errors.New("harness: a live run already holds the pin of this id")

// WithPin returns ctx carrying p, a run's routing choice: p's pool routes
// a call made with it by p when the call is of p's run, even after another
// run took its id, and through the ordinary pick when p is an unpinned
// run's (Unpinned). Another pool ignores it.
func WithPin(ctx context.Context, p *Pin) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, pinKey{}, p)
}

type spawnAttempt struct {
	done chan struct{}
	err  error
	cfg  ProcessConfig // the process's, as the pool held it when it began
	// owed: it is the replacement of a member that left for a recycle
	// trigger, which is owed again if it fails.
	owed bool
}

// NewPool returns a pool; processes start on the first Execute, or on
// Start.
func NewPool(cfg PoolConfig) (_ *Pool, err error) {
	// The settings' checks quote what they refuse.
	defer func() { err = NewSecrets(cfg.Process.Secrets).MaskErr(err) }()
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
	opted := cfg.opted()
	cfg.withDefaults()
	if opted && pc.Diagnostics != nil {
		// Every process of the pool, and the pool itself, write to it, and
		// the settings let several processes live at once.
		pc.Diagnostics = &syncWriter{w: pc.Diagnostics}
	}
	pl := &Pool{cfg: cfg, secrets: NewSecrets(cfg.Process.Secrets), opted: opted, changed: make(chan struct{}), affinity: runMap{limit: defaultAffinity}, fresh: map[*Process]struct{}{}}
	if !pl.isFresh() {
		pl.cfg.Process.notify = pl.processChanged
		pl.cfg.Process.rssLimit = cfg.Recycle.RSS
		pl.cfg.Process.countScripts = cfg.Recycle.Modules > 0
		pl.cfg.Process.unsettledLimit = cfg.Recycle.UnsettledRun
		pl.cfg.Process.heldAgeLimit = cfg.Recycle.HeldAge
		pl.cfg.Process.now = cfg.now
	}
	return pl, nil
}

// opted reports whether any of the settings that a pool's user chooses is
// set, before the defaults.
func (cfg PoolConfig) opted() bool {
	return cfg.Min != 0 || cfg.Max != 0 || cfg.Cap != 0 || cfg.LagThreshold != 0 || cfg.RecycleTimeout != 0 ||
		cfg.MaxLive != 0 || cfg.Recycle != (RecycleTriggers{})
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
	case cfg.Recycle.RSS < 0 || cfg.Recycle.Samples < 0 || cfg.Recycle.Modules < 0 || cfg.Recycle.UnsettledRun < 0 || cfg.Recycle.HeldAge < 0:
		return fmt.Errorf("harness: pool's recycle triggers cannot be negative (RSS %d, Samples %d, Modules %d, UnsettledRun %s, HeldAge %s)",
			cfg.Recycle.RSS, cfg.Recycle.Samples, cfg.Recycle.Modules, cfg.Recycle.UnsettledRun, cfg.Recycle.HeldAge)
	}
	return nil
}

func (pl *Pool) isFresh() bool { return pl.cfg.Key.ModuleState == engine.ModuleStateFresh }

// Start spawns the pool's Min processes and waits until Min serve, those
// that others started included, and live: a member whose process exited
// before its admission, which a pool without settings admits, is removed
// first, and its replacement counts as a start Start made. It returns the
// error of a start it made that failed, and an error once more than Min
// members have exited that way. A warm pool's Start returns ErrPoolClosed
// once Close has begun; a fresh pool starts none ahead, and its Start
// returns nil.
func (pl *Pool) Start(ctx context.Context) (err error) {
	defer func() { err = pl.secrets.MaskErr(err) }()
	if pl.isFresh() {
		return nil
	}
	var mine []*spawnAttempt
	exits := 0 // members that Start removed because their process exited before their admission
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
		if pl.closed {
			pl.mu.Unlock()
			return ErrPoolClosed
		}
		// The replacements of the members it removes are Start's own: their
		// failures are its errors.
		n, atts := pl.pruneExitedLocked()
		mine = append(mine, atts...)
		if exits += n; exits > pl.cfg.Min {
			pl.mu.Unlock()
			return fmt.Errorf("harness: %d processes exited right after their handshakes while the pool started %d", exits, pl.cfg.Min)
		}
		if pl.inServiceLocked() >= pl.cfg.Min {
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
func (pl *Pool) Execute(ctx context.Context, inv engine.Invocation) (_ engine.StepOutcome, err error) {
	defer func() { err = pl.secrets.MaskErr(err) }()
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
		m, pn, err := pl.acquire(ctx, inv.Run, true)
		if err != nil {
			inv.Report.Settled(engine.SettleNotStarted)
			return engine.StepOutcome{}, err
		}
		// The slot is freed as soon as the engine stops waiting for the
		// invocation, before its Reporter learns the outcome, or else here.
		var once sync.Once
		// The outcome is fixed only for an invocation that was sent.
		release := func(reached, unavailable bool) {
			once.Do(func() { pl.release(m, pn, reached, unavailable, true, inv.Run) })
		}
		out, d, err := m.p.exec(ctx, inv, func() { release(true, false) })
		unavailable := d == notDispatched && errors.Is(err, errUnavailable)
		release(d != notDispatched, unavailable)
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
// counts it as active in the process; release ends it. It returns the
// run's pin when the call went by it, for release.
func (pl *Pool) acquire(ctx context.Context, run string, step bool) (*member, *Pin, error) {
	var spawnErr error
	for {
		pl.mu.Lock()
		if pl.closed {
			pl.mu.Unlock()
			return nil, nil, ErrPoolClosed
		}
		// The first Execute establishes the minimum, as Start would.
		if spawnErr == nil {
			pl.fillLocked()
		}
		// A dispatch that crossed a recycle threshold tells the pool after
		// it is sent; no later pick may miss it.
		pl.applyTriggersLocked()
		pn := pl.pinOfLocked(ctx, run)
		pinned, wait, err := pl.pinnedLocked(pn, run, step)
		if err != nil {
			pl.mu.Unlock()
			return nil, nil, err
		}
		if pinned != nil {
			pl.takeLocked(pinned, run, step)
			pn.active++
			pl.mu.Unlock()
			return pinned, pn, nil
		}
		if wait {
			ch := pl.changed
			pl.observeLocked("wait", nil)
			pl.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, nil, context.Cause(ctx)
			case <-ch:
			}
			continue
		}
		m, lagging := pl.pickLocked(run, step)
		canSpawn := spawnErr == nil && pl.inServiceLocked()+pl.starting < pl.cfg.Max && pl.roomLocked()
		if m != nil && (!lagging || !canSpawn) {
			if pn != nil && !pn.ended && pn.m == nil {
				// The run's first invocation or host call: the pin binds to
				// a serving process, so never to a tainted, recycling or
				// surplus one.
				pn.m = m
			}
			pl.takeLocked(m, run, step)
			if pn == nil || pn.m != m {
				pn = nil
			} else {
				pn.active++
			}
			pl.mu.Unlock()
			return m, pn, nil
		}
		if canSpawn && pl.spawning == nil {
			pl.spawnLocked()
		}
		att, ch := pl.spawning, pl.changed
		pl.observeLocked("wait", nil)
		pl.mu.Unlock()
		if att == nil && spawnErr != nil {
			return nil, nil, spawnErr
		}
		var attDone <-chan struct{}
		if att != nil {
			attDone = att.done
		}
		select {
		case <-ctx.Done():
			return nil, nil, context.Cause(ctx)
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

// takeLocked counts an invocation of run (step) or a host call of run as
// active in m.
func (pl *Pool) takeLocked(m *member, run string, step bool) {
	m.active++
	if step {
		m.steps++
		if run != "" {
			if m.runs == nil {
				m.runs = map[string]int{}
			}
			m.runs[run]++
		}
	}
	pl.affinity.set(run, m)
}

// pinOfLocked returns the pin that run's call routes by: the one its
// context carries (WithPin) when it is run's, or else the one run's id
// names; nil when there is none.
func (pl *Pool) pinOfLocked(ctx context.Context, run string) *Pin {
	if p, ok := ctx.Value(pinKey{}).(*Pin); ok && p.pool == pl && p.run == run {
		if p.unpinned {
			return nil
		}
		return p
	}
	return pl.pins[run]
}

// pinnedLocked routes an invocation of run (step) or a host call of run
// by its pin pn: it returns the member to take, or wait when the call must
// wait for it, or the error that ends it. It returns neither when pn is
// nil or binds to no member yet, or when the call goes through the
// ordinary pick.
//   - The member takes a live run's calls while it lives, tainted,
//     recycling, surplus or lagging included: the one exception to the
//     taint rule.
//   - The cap still bounds its invocations, which wait for a slot.
//   - A member that is dying or draining is waited for until its exit.
//   - Once its exit is observed, a live run's call fails with
//     engine.ErrPinnedProcessLost, which latches the run.
//   - After the run's end, its late calls go to the member while it can
//     answer, leaving or not, without holding it; once it has exited or
//     refused one, they route as any call does.
//   - While no call of the run has reached the member, its leaving says
//     nothing of the run: a call waits for the one under way to say how it
//     went, and once none is, the pin binds afresh.
func (pl *Pool) pinnedLocked(pn *Pin, run string, step bool) (*member, bool, error) {
	if pn == nil || pn.m == nil {
		return nil, false, nil
	}
	m := pn.m
	switch {
	case pn.ended && (m.gone || pn.refused || step):
		return nil, false, nil
	case pn.ended:
		return m, false, nil
	case !pn.reached && (m.gone || m.retiring):
		if pn.active > 0 {
			return nil, true, nil
		}
		pn.m = nil
		return nil, false, nil
	case m.gone:
		return nil, false, fmt.Errorf("harness: run %s: its pinned process has exited: %w", run, engine.ErrPinnedProcessLost)
	case m.retiring:
		return nil, true, nil
	case step && pl.cfg.Cap > 0 && m.steps >= pl.cfg.Cap:
		return nil, true, nil
	}
	return m, false, nil
}

// releasePinLocked records how a call that pn sent to m ended: it reached
// m, or it did not, refused by m (unavailable) or ended before it was
// sent. A pin that no call has reached m by binds afresh once none is
// under way; after the run's end, a refusal sends its later calls through
// the ordinary pick.
func (pl *Pool) releasePinLocked(pn *Pin, m *member, reached, unavailable bool) {
	if pn == nil || pn.m != m {
		return
	}
	pn.active--
	switch {
	case reached:
		pn.reached = true
	case pn.ended:
		if unavailable {
			pn.refused = true
		}
	case !pn.reached && pn.active == 0:
		pn.m = nil
	}
}

// heldLocked reports whether a live pinned run holds m, which keeps it
// from leaving.
func (pl *Pool) heldLocked(m *member) bool {
	for _, pn := range pl.pins {
		if pn.m == m && !pn.ended {
			return true
		}
	}
	return false
}

// PinRun pins a run admitted with id run (ADR 0004 §Pooling) and returns
// its pin: its first invocation or host call goes to a serving process,
// and every later one goes there while that process lives. The process
// does not leave the pool while the run is live, until RunEnded. Call it
// before the run's first invocation. The pin replaces the one of an
// earlier run with the same id, whose calls then route by it only when
// their context carries it (WithPin). A fresh pool has no pins: it
// returns nil, which RunEnded and ForgetRun ignore.
func (pl *Pool) PinRun(run string) (_ *Pin, err error) {
	defer func() { err = pl.secrets.MaskErr(err) }()
	if pl.isFresh() || run == "" {
		return nil, nil
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if p := pl.pins[run]; p != nil && !p.ended {
		return nil, fmt.Errorf("%w: %s", ErrPinLive, run)
	}
	if pl.pins == nil {
		pl.pins = map[string]*Pin{}
	}
	p := &Pin{pool: pl, run: run}
	pl.pins[run] = p
	return p, nil
}

// Unpinned returns an unpinned run's routing choice, for its calls to
// carry (WithPin): they go through the ordinary pick, never by the pin of
// a later run with the same id. A fresh pool returns nil.
func (pl *Pool) Unpinned(run string) *Pin {
	if pl.isFresh() || run == "" {
		return nil
	}
	return &Pin{pool: pl, run: run, unpinned: true}
}

// RunEnded tells the pool that p's run has ended: its pin no longer holds
// its process, which may then leave, and still routes the run's later
// host calls, such as onRunSettled, there while it can answer.
func (pl *Pool) RunEnded(p *Pin) {
	if p == nil || p.unpinned {
		return
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if p.ended {
		return
	}
	p.ended = true
	if p.m != nil && !p.m.gone {
		pl.reviewLocked(p.m)
	}
	pl.broadcastLocked()
}

// ForgetRun drops p, once nothing of its run calls the pool any more (its
// settlement). A later run's pin with the same id stays.
func (pl *Pool) ForgetRun(p *Pin) {
	if p == nil || p.unpinned {
		return
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.pins[p.run] == p {
		delete(pl.pins, p.run)
	}
	if !p.ended {
		p.ended = true
		if p.m != nil && !p.m.gone {
			pl.reviewLocked(p.m)
		}
	}
	pl.broadcastLocked()
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
	if bestLagging == nil && !step && run != "" {
		// A host call of a run with an invocation in flight in a process
		// that is leaving for a recycle trigger: that invocation may be
		// waiting for it.
		for _, m := range pl.members {
			if m.recycling && !m.retiring && !m.gone && m.runs[run] > 0 {
				return m, false
			}
		}
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
		att := pl.spawnLocked()
		att.owed = true
		atts = append(atts, att)
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
	pl.mu.Lock()
	if err == nil && pl.opted && !pl.closed {
		err = pl.admitLocked(p)
	}
	pl.starting--
	if err == nil && !pl.opted && pl.plugin == nil && !p.Hello().Plugin.IsZero() {
		pl.plugin = slices.Clone(p.Hello().Plugin.Producer())
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
			if pl.opted && !pl.declared {
				pl.declared = true
				if d := p.Hello().Plugin; !d.IsZero() {
					pl.plugin = slices.Clone(d.Producer())
				}
			}
			m := &member{p: p, surplus: pl.inServiceLocked() >= pl.cfg.Max, early: p.hasExited()}
			pl.members = append(pl.members, m)
			pl.reviewLocked(m)
		}
	} else {
		// The caller that wanted this process gets err; a replacement
		// nobody waits for is only logged, and the next Execute tries again.
		pl.observeLocked("failed", nil)
		pl.diag().line("windmill-lite: harness pool: starting a process failed: " + err.Error())
		if att.owed && !pl.closed {
			// The next acquire tries it again, not this failure: a command
			// that keeps failing is not started in a loop.
			pl.owed++
		}
	}
	if pl.spawning == att {
		pl.spawning = nil
	}
	report := err != nil && !pl.closed && pl.cfg.OnStartFailure != nil
	close(att.done)
	pl.broadcastLocked()
	pl.mu.Unlock()
	if report {
		pl.cfg.OnStartFailure(err)
	}
}

// admitLocked applies a pool with settings' admission checks to p, a
// process that has started, and returns why it cannot be admitted.
//   - It must not have exited: its exit came before this admission and
//     found no member to remove, so admitted it would hold a place in the
//     pool for good.
//   - Its host plugin declaration, or the absence of one, must be what the
//     first admitted process carried, byte for byte, as ExpectPlugin checks
//     a later spawn's handshake: it may have begun to start before that.
//     One that differs is killed, and counts as starting until it has
//     exited.
func (pl *Pool) admitLocked(p *Process) error {
	if p.hasExited() {
		return errors.New("harness: the process exited right after its handshake")
	}
	d := p.Hello().Plugin
	if !pl.declared || (d.IsZero() == (pl.plugin == nil) && bytes.Equal(d.Producer(), pl.plugin)) {
		return nil
	}
	pl.mu.Unlock()
	p.Kill()
	<-p.Done()
	pl.mu.Lock()
	return errors.New("harness: the process's host plugin declaration differs from the pool's first process's")
}

// release ends an acquire of m: an invocation of run (step) or a host
// call. A process found not serving is leaving the pool (it is dying or
// draining), so it is not picked again.
func (pl *Pool) release(m *member, pn *Pin, reached, unavailable, step bool, run string) {
	pl.mu.Lock()
	m.active--
	pl.releasePinLocked(pn, m, reached, unavailable)
	if step {
		m.steps--
		if run != "" {
			if m.runs[run]--; m.runs[run] <= 0 {
				delete(m.runs, run)
			}
		}
	}
	if unavailable {
		m.retiring = true
	}
	pl.reviewLocked(m)
	pl.broadcastLocked()
	pl.mu.Unlock()
}

// diag returns the pool's own diagnostics, which mask the processes'
// secrets in each line, as a whole (diagnostics.line).
func (pl *Pool) diag() *diagnostics {
	w := pl.cfg.Process.Diagnostics
	if w == nil {
		w = os.Stderr
	}
	return &diagnostics{w: w, secrets: pl.secrets}
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
			pl.removeLocked(i)
		} else {
			pl.reviewLocked(m)
		}
		pl.broadcastLocked()
		return
	}
}

// removeLocked removes the member at i, whose exit is observed, replaces
// it up to Min, and returns the attempts it made.
func (pl *Pool) removeLocked(i int) []*spawnAttempt {
	m := pl.members[i]
	m.gone = true
	pl.members = append(pl.members[:i], pl.members[i+1:]...)
	pl.observeLocked("exit", m.p)
	if pl.recycler == m {
		// The recycle slot frees at the observed exit; the next process
		// that tripped meanwhile takes it.
		pl.recycler = nil
		for _, o := range pl.members {
			if o.tripped && o.inService() {
				pl.reviewLocked(o)
				break
			}
		}
	}
	return pl.fillLocked()
}

// pruneExitedLocked removes the members whose process has exited: those
// whose exit came before their admission, which a pool without settings
// admits, as it always has, and whose exit no notify reports any more, and
// those whose notify has not come yet. It returns how many of the first
// kind it removed, and the attempts it made to replace them all.
func (pl *Pool) pruneExitedLocked() (int, []*spawnAttempt) {
	early, removed := 0, false
	var atts []*spawnAttempt
	for i := 0; i < len(pl.members); {
		if m := pl.members[i]; !m.gone && m.p.hasExited() {
			atts = append(atts, pl.removeLocked(i)...)
			removed = true
			if m.early {
				early++
			}
			continue
		}
		i++
	}
	if removed {
		pl.broadcastLocked()
	}
	return early, atts
}

// applyTriggersLocked applies the recycle triggers to every serving member
// that has reached one and has not been reviewed since.
func (pl *Pool) applyTriggersLocked() {
	if r := pl.cfg.Recycle; r.RSS == 0 && r.Modules == 0 && r.UnsettledRun == 0 && r.HeldAge == 0 {
		return
	}
	for _, m := range pl.members {
		if !m.tripped && m.inService() && pl.trippedLocked(m) {
			pl.reviewLocked(m)
		}
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
		if why := pl.lifetimeTripLocked(m); why != "" {
			pl.diag().line(fmt.Sprintf("windmill-lite: harness pool: process %d leaves the pool: %s", m.p.PID(), why))
		}
	}
	if m.tripped && m.inService() && pl.recycler == nil {
		// It leaves for its trigger now, and its replacement starts beside
		// it, also in a pool grown past Min.
		m.recycling = true
		pl.recycler = m
		pl.owed++
		pl.fillLocked()
	}
	if (m.tainted || m.surplus || m.recycling) && m.active == 0 && !pl.heldLocked(m) {
		m.retiring = true
		pl.fillLocked()
		pl.observeLocked("drain", m.p)
		go drainWithin(m.p, pl.cfg.RecycleTimeout)
	}
}

// lifetimeTripLocked says which of the triggers on what a process holds
// past its runs m has reached, UnsettledRun or HeldAge, for the pool's
// diagnostics line, or "" for neither.
func (pl *Pool) lifetimeTripLocked(m *member) string {
	r := pl.cfg.Recycle
	if d := m.p.unsettledAge(); r.UnsettledRun > 0 && d >= r.UnsettledRun {
		return fmt.Sprintf("a host plugin hook has held its ended run unsettled for %s (the bound is %s)", d.Round(time.Millisecond), r.UnsettledRun)
	}
	if d := m.p.heldAge(); r.HeldAge > 0 && d >= r.HeldAge {
		return fmt.Sprintf("its host plugin reports a resource held %s after its run ended (the bound is %s)", d, r.HeldAge)
	}
	return ""
}

// trippedLocked reports whether m has reached a recycle trigger.
func (pl *Pool) trippedLocked(m *member) bool {
	r := pl.cfg.Recycle
	return (r.RSS > 0 && m.p.rssStreak() >= r.Samples) || (r.Modules > 0 && m.p.scriptCount() > r.Modules) ||
		(r.UnsettledRun > 0 && m.p.unsettledAge() >= r.UnsettledRun) || (r.HeldAge > 0 && m.p.heldAge() >= r.HeldAge)
}

func (pl *Pool) broadcastLocked() {
	close(pl.changed)
	pl.changed = make(chan struct{})
}

// Close stops routing, waits for the invocations the engine still waits
// for (until ctx ends), then drains every process; a process that has not
// exited when ctx ends is killed.
func (pl *Pool) Close(ctx context.Context) (err error) {
	defer func() { err = pl.secrets.MaskErr(err) }()
	pl.mu.Lock()
	pl.closed = true
	pl.broadcastLocked()
	pl.mu.Unlock()
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

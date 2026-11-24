// Package wl is windmill-lite's engine API (ADR 0009): the only exported
// engine package. Open loads a bundle (ADR 0010) into a Runtime, which runs
// its flows and scripts by path (Runtime.Run), reports what it is
// (Runtime.BuildInfo) and drains its harness pools (Runtime.Close). Every
// adapter (go-plugin, plain gRPC, HTTP, the Bun host SDK, the CLI's
// `windmill-lite run`) is a transport over these calls.
package wl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/platform"
	"github.com/invakid404/windmill-lite/internal/platform/facade"
)

// Module states (ADR 0006), for Options.ModuleState.
const (
	// ModuleStateWarm keeps one module instance per script in a pool of
	// long-lived harness processes (deviations D1, D2, D9 to D11).
	ModuleStateWarm = string(engine.ModuleStateWarm)
	// ModuleStateFresh runs each step in a harness process of its own, as
	// Windmill's worker runs each job.
	ModuleStateFresh = string(engine.ModuleStateFresh)
)

// Options configure Open. The zero value of every field but HarnessRuntime
// is a default, so a new field does not break a host; HarnessRuntime is
// needed when the bundle has a step to run, outside host mode. The hooks,
// the per-run providers and the pre-write deadline of a flow run come with
// that run (RunOptions), and every run's hard deadline is its context's.
// Host mode is Host, and the go-plugin adapter, which is not built yet, is
// planned as a runtime over the same calls.
type Options struct {
	// Bun is the Bun executable that harness processes run on; it must be
	// the pinned Bun 1.4.3, which the harness's handshake checks. Empty
	// means "bun" on PATH.
	Bun string
	// HarnessRuntime is the directory of the Bun harness runtime
	// (harnesses/bun/runtime) that the bundle was built for: its tree
	// digest must be the one the manifest records (ADR 0010, drift guard
	// 2). It is needed only when the bundle has a step to run.
	HarnessRuntime string
	// DataDir is the directory the runtime writes into: an archive's
	// extracted bundle, the object store, the harness processes' working
	// directories. Empty means a temporary directory that Close removes.
	DataDir string
	// ModuleState overrides the bundle's module-state mode (ADR 0006):
	// ModuleStateWarm or ModuleStateFresh; empty keeps the manifest's.
	ModuleState string
	// Digest, when set, is the bundle digest the bundle must have.
	Digest string
	// EngineVersion is the runtime's own engine version, which must be the
	// one that built the bundle (ADR 0010); empty means Version().
	EngineVersion string
	// Resources and Variables are the host's platform data (ADR 0007):
	// they replace the bundle's data/ at the same path. Secrets are never
	// in a bundle, so a host passes them here (a Variable with Secret).
	Resources map[string]Resource
	Variables map[string]Variable
	// Results is the result cache that keeps the results of the jobs that
	// run with a cache_ttl (FX-40), which a host can share
	// between runtimes; nil means one in memory for the runtime's runs
	// (D4).
	Results ResultCache
	// ResourceStore keeps the resources that steps write through the job
	// API: the state that the stock client's setState writes (WA-13),
	// which reads find after the bundle's and the host's resources. nil
	// means a new memory store for the runtime, whose entries Close loses
	// and no other runtime sees (deviation D24 of ADR 0001); a host can
	// share one store between its runtimes, a memory store
	// (NewMemoryResourceStore) or its own implementation. Every run of a
	// runtime whose store is a memory store lists D24 in its trace.
	ResourceStore ResourceStore
	// Diagnostics receives the harness processes' output that no step
	// owns, and the engine's diagnostics about them; nil means os.Stderr.
	// With a Pool that is not zero, several processes can write at once,
	// so the runtime serializes its writes, still calling the writer's
	// WriteString when it has one.
	Diagnostics io.Writer
	// ExpressionSlots is the number of evaluation slots of the runtime's
	// expression engine; zero means its default (ADR 0003).
	ExpressionSlots int
	// QuickJSOnly evaluates every expression that the shortcuts and the
	// member paths do not answer in QuickJS, turning off the Go evaluator of
	// templates, arithmetic, comparisons and predicates over JSON values
	// (jsExpr; ADR 0003, engine.Config.QuickJSOnly). The
	// evaluator answers only what QuickJS would answer the same way, so
	// nothing observable changes; this is a safety valve. Off by default.
	QuickJSOnly bool
	// Providers are the named providers of the bundle's host-provided
	// sites (ADR 0002, ADR 0008), by the names the host contract gives
	// them. Open fails with ErrMissingProvider when the contract names one
	// that is not here; a static provided site needs none.
	Providers map[string]Provider
	// Plugin, when set, is the path of a host plugin module (ADR 0008,
	// harnesses/bun/runtime/plugin.ts) that every warm harness process
	// loads: TypeScript hooks and providers for the runtime's runs, which
	// the engine calls over host_call, and around wrappers, which the
	// harness runs around a step in its own process. Open starts the first
	// process to read the plugin's declaration, and fails when the
	// declaration is not usable (a malformed or ambiguous selector, a
	// provider the host contract names that neither Providers nor the
	// plugin has). A fresh runtime takes none: its processes live for one
	// step. In host mode the plugin is the host's.
	Plugin string
	// PluginTimeout bounds each host_call: a hook whose reply does not come
	// within it fails with ErrHostCallTimeout, which latches the run. Zero
	// means DefaultPluginTimeout.
	PluginTimeout time.Duration
	// ReadCache, when true, gives each flow run a read cache (deviation D21
	// of ADR 0001): the run reads each resource and variable from its
	// providers once, at its first use, and answers every later read of it
	// from that answer, its steps' $res: and $var: arguments, flow_env and
	// their reads through the job API alike. Windmill reads them again for
	// each job and each call, so a value that changes during a run reaches
	// its later steps there, and here only the next run. The values are
	// held as stored, so $WM_* strings still resolve for each job, and a
	// variable a step updates is read again. Off by default; the trace of
	// a run with it lists D21.
	ReadCache bool
	// Pool sets the pool of a warm runtime's harness processes (ADR 0004
	// §Pooling): its size, the cap per process, the lag threshold, the
	// recycle triggers, the bound on live processes and a warm start. Its
	// zero value is one process with no cap, which starts on the first
	// step, or in Open to read a host plugin's declaration. A fresh
	// runtime, which starts a process for each step, and host mode, whose
	// one process is the host, refuse a Pool that is not zero.
	Pool PoolOptions
	// ForeignWindmillOrigins are the origins of real Windmill instances
	// that steps call through their own client, such as
	// "https://windmill.example.com" (ADR 0007, async dispatch).
	// windmill-lite's job ids name no job there. So on a step's request
	// to one of these origins under /api/w/{workspace}/jobs/run/, where
	// the stock clients dispatch a script or a flow, the harness's fetch
	// view removes each parent_job and root_job query parameter whose
	// value is one of the invocation's own job ids: its WM_JOB_ID or
	// WM_ROOT_FLOW_JOB_ID as the engine set them, the values that the stock
	// clients send. Every other part of the request stays as it was, a link
	// whose value is neither of the two included. A request
	// to another origin, or one that the fetch view does not see, from a
	// child process or a socket that the step opens, keeps its links.
	// Each value is an http or https origin. Open refuses one with user
	// information, a query, a fragment or a path other than "/", or whose
	// host the harness's URL parser refuses or reads as another (an IPv4
	// address it reads as the WHATWG URL Standard does), naming its
	// position, never the value. A host outside ASCII goes to the harness
	// whole, which maps it as that parser does, and leaves out one the
	// parser refuses, since no request can go to it. Empty, the default,
	// names none. In host mode they apply to the host's steps.
	ForeignWindmillOrigins []string
	// Host, when set, opens the runtime in host mode (ADR 0004 host mode,
	// ADR 0009): its steps run in the host process that started it, which
	// submits the runs. The bundle must then be a directory, which the
	// host loads too; steps run warm (ADR 0006), so ModuleState may only
	// be warm; HarnessRuntime and Bun are not used, since the host is the
	// harness. Open completes the handshake with the host before it
	// returns, and refuses the host, with the reason, when it fails.
	Host *Host
}

// PoolOptions set the pool of a warm runtime's harness processes (ADR 0004
// §Pooling). The zero value of each field is its default.
type PoolOptions struct {
	// Min is how many processes the pool keeps serving, and Max how many
	// may serve; zero means one each. A process that is tainted (an
	// abandoned step of its own still runs), or that leaves for a recycle
	// trigger, does not count toward Max.
	Min, Max int
	// Cap bounds the steps in flight in each process; zero means no cap.
	// A cap is one of the named deviations of ADR 0001: steps that wait
	// on each other can then wait until their timeout. So the trace that
	// Run returns for each flow run lists it, as "D15".
	Cap int
	// LagThreshold is the event-loop lag past which a process counts as
	// busy with a CPU-bound step: new steps go to another process, which
	// the pool starts if it is below Max. Zero means 250 ms; a negative
	// value turns lag routing off.
	LagThreshold time.Duration
	// RecycleTimeout bounds the drain of a process that leaves the pool,
	// after its last step in flight, before it is killed; zero means 5 s.
	RecycleTimeout time.Duration
	// Recycle are the triggers that replace a process that is still
	// serving; zero values turn them off.
	Recycle RecycleTriggers
	// MaxLive bounds the processes alive at once, each counted from its
	// start until its exit: serving, starting, tainted, leaving or
	// draining. Zero means no bound. Open refuses a bound below Max.
	MaxLive int
	// WarmStart makes Open start Min processes, and return once Min
	// serve, their handshakes done, instead of starting them on the first
	// step. A bundle with no step has no pool, so nothing starts.
	WarmStart bool
	// PinRuns pins every run of the runtime, as RunOptions.Pin pins one.
	PinRuns bool
}

// RecycleTriggers say when the pool replaces a process that is still
// serving. A process trips a trigger when its heartbeats report a resident
// set of RSS bytes or more Samples times in a row, or once it has run steps
// of more than Modules distinct scripts, whose modules it keeps loaded.
// It then takes no new steps, a replacement starts beside it outside Max,
// and it leaves once no step is in flight in it. One process leaves for a
// trigger at a time; another that trips meanwhile keeps serving until the
// first has exited. A threshold is sampled and does not cap memory.
type RecycleTriggers struct {
	// RSS is the resident set size, in bytes, that trips the trigger; zero
	// turns it off.
	RSS int64
	// Samples is how many heartbeats in a row must reach RSS; zero means 2.
	Samples int
	// Modules is how many distinct scripts a process may run; the first
	// step of one more still runs there. Zero turns it off.
	Modules int
}

// harnessConfig is the pool's configuration in internal/harness, without
// its key and process template.
func (o PoolOptions) harnessConfig() harness.PoolConfig {
	return harness.PoolConfig{
		Min: o.Min, Max: o.Max, Cap: o.Cap, LagThreshold: o.LagThreshold, RecycleTimeout: o.RecycleTimeout, MaxLive: o.MaxLive,
		Recycle: harness.RecycleTriggers{RSS: o.Recycle.RSS, Samples: o.Recycle.Samples, Modules: o.Recycle.Modules},
	}
}

// Resource is a resource: its resource type, and its value as JSON, whose
// $var: and $res: references are resolved when it is read (WA-12). A host
// provides resources as Resource (Options.Resources, RunPlatform), and a
// ResourceStore keeps the entries steps write as Resource.
type Resource = platform.Resource

// Variable is a variable the host provides. A secret's Value is its
// plaintext, unless Ciphertext says it is Windmill ciphertext under the
// workspace key, which windmill-lite never decrypts (JE-43): reading it
// then fails as Windmill fails a secret it cannot decrypt (WA-11).
type Variable struct {
	Value      string
	Secret     bool
	Ciphertext bool
}

// ResourceStore is the store of the resources that steps write (WA-13): the
// interface every store implements, memory or a host's own, whose contract
// its doc comment states and the contract suite wl/storetest checks.
type ResourceStore = platform.ResourceStore

// MemoryResourceStore is the resource store that keeps its entries in
// memory, the default (deviation D24 of ADR 0001).
type MemoryResourceStore = platform.MemoryResourceStore

// NewMemoryResourceStore returns an empty memory store, which a host can
// pass as Options.ResourceStore to several runtimes so that they share it.
func NewMemoryResourceStore() *MemoryResourceStore { return platform.NewMemoryResourceStore() }

// ResultCache is a store of cached job results (ADR 0007): the engine
// computes the keys, the expiries and the bytes it stores, and a store
// keeps opaque bytes until their expiry, compared with the time a Get is
// given, never its own clock.
type ResultCache = platform.ResultCache

// ErrUnsupported marks a bundle that uses a feature the engine does not
// run; the error names the feature.
var ErrUnsupported = engine.ErrUnsupported

// ErrEngineVersion marks a bundle that another engine version built.
var ErrEngineVersion = errors.New("wl: the bundle was built by another engine version")

// ErrClosed is Run's error once Close has begun.
var ErrClosed = errors.New("wl: the runtime is closed")

// closing is the cause Close cancels the runs in flight with (FX-38), which
// gives them the cause CauseClosing.
var closing = engine.Closing

// Runtime is an opened bundle. It is safe for concurrent use.
type Runtime struct {
	dir       string // the bundle directory
	data      string // the data directory
	ownData   bool
	extracted string // the directory an archive was extracted into, if any
	digest    string
	manifest  *bundle.Manifest
	state     engine.ModuleState
	version   string
	eng       *engine.Engine
	js        *quickjs.Engine
	fac       *facade.Facade
	pool      *harness.Pool
	host      *harness.Process // host mode's connection (Options.Host)
	hostProc  atomic.Pointer[harness.Process]
	exec      *recorder
	prog      *ir.Program
	store     *platform.Store
	services  *platform.Services
	dispatch  *engine.Dispatcher
	readCache bool // Options.ReadCache
	capped    bool // Options.Pool.Cap is set: each flow run's trace lists "D15"
	pinRuns   bool // Options.Pool.PinRuns
	// foreign is Options.ForeignWindmillOrigins as the welcomes carry
	// them (foreignOrigins).
	foreign []string
	// memoryStore says that the runtime's resource store keeps its entries
	// in memory, which its runs' traces list as D24.
	memoryStore bool
	// ownStore is the resource store that Open made, which Close releases;
	// nil when the host gave one, which is the host's to keep.
	ownStore *platform.MemoryResourceStore

	// plugin is the host plugin, once its harness declared it;
	// providers are the Go host's and the plugin's named providers, and
	// goProviders the Go host's alone.
	plugin      atomic.Pointer[plugin]
	providers   atomic.Pointer[map[string]Provider]
	goProviders map[string]Provider

	// mu guards closed: once Close begins, Run admits no run. active counts
	// the runs admitted, which stop's cancellation ends.
	mu         sync.Mutex
	closed     bool
	active     sync.WaitGroup
	stop       context.Context
	cancelStop context.CancelCauseFunc
	closeOnce  sync.Once
	closeErr   error
}

// admit admits a run unless Close has begun; an admitted run calls
// active.Done when it ends.
func (rt *Runtime) admit() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return false
	}
	rt.active.Add(1)
	return true
}

// version is set at link time with
// -ldflags "-X github.com/invakid404/windmill-lite/wl.version=<version>".
var version string

// modulePath is this module's import path.
const modulePath = "github.com/invakid404/windmill-lite"

// Version is the engine's version: the one set at link time, else the
// version of this module in the program's build information (the main
// module's, or a dependency's in a host's program), else "(devel)".
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Path == modulePath && info.Main.Version != "" {
			return info.Main.Version
		}
		for _, d := range info.Deps {
			if d.Path == modulePath && d.Version != "" {
				return d.Version
			}
		}
	}
	return "(devel)"
}

// Open opens a bundle, a directory or a .wlb archive (ADR 0009, ADR 0010):
// it verifies every file against the manifest (and opts.Digest), checks
// that the runtime's engine version built it and, when it has steps, that
// opts.HarnessRuntime is the harness runtime it was built for, reads the
// compiled program and data/, and opens the engine. An archive is
// extracted into the data directory first. Harness processes start on
// the first step (a fresh runtime starts one per step), and a bundle with
// no step starts none. In a warm runtime, Open starts them earlier in two
// cases: a host plugin (Options.Plugin) makes it start the pool's minimum
// and wait for the first, to read the plugin's declaration, and a warm
// start (Options.Pool.WarmStart) makes it wait until the minimum serve.
//
// The runtime owns its platform services (ADR 0007): the bundle's
// resources and variables with the host's, which a step can update through
// the job API (WA-11), the job registry and flow user state, an object
// store under the data directory, and the default dispatcher over the
// bundle's program. The facade serves them to its steps.
func Open(path string, opts Options) (_ *Runtime, err error) {
	rt := &Runtime{version: opts.EngineVersion, readCache: opts.ReadCache, capped: opts.Pool.Cap > 0, pinRuns: opts.Pool.PinRuns}
	if opts.Diagnostics != nil && opts.Pool != (PoolOptions{}) {
		// The harness processes, the pool and the host plugin's calls write
		// to it from goroutines of their own, and the pool settings let
		// several processes live at once. Without them the writer is called
		// as it always has been.
		opts.Diagnostics = &lockedWriter{w: opts.Diagnostics}
	}
	rt.stop, rt.cancelStop = context.WithCancelCause(context.Background())
	if rt.version == "" {
		rt.version = Version()
	}
	attached := false // the handshake with the host was made, or tried
	defer func() {
		if err != nil {
			if opts.Host != nil && opts.Host.Conn != nil && !attached {
				// The host greets an engine that cannot serve it: it is
				// told why.
				refuseHost(opts.Host, rt.version, opts.Diagnostics, err)
			}
			rt.Close()
		}
	}()
	if opts.Host != nil {
		if opts.Host.Conn == nil || opts.Host.Workdir == "" {
			return nil, errors.New("wl: host mode needs Host.Conn and Host.Workdir")
		}
		if opts.ModuleState != "" && opts.ModuleState != ModuleStateWarm {
			return nil, fmt.Errorf("wl: host mode runs warm (ADR 0006), not %s", opts.ModuleState)
		}
		if opts.Pool != (PoolOptions{}) {
			return nil, errors.New("wl: host mode has no harness pool, since the host is its one harness process: Options.Pool must be zero")
		}
	}
	if rt.foreign, err = foreignOrigins(opts.ForeignWindmillOrigins); err != nil {
		return nil, err
	}
	// Harness processes run in directories of their own, so every path
	// they see is absolute.
	if path, err = filepath.Abs(path); err != nil {
		return nil, err
	}
	if opts.DataDir != "" {
		if opts.DataDir, err = filepath.Abs(opts.DataDir); err != nil {
			return nil, err
		}
	}
	if rt.data = opts.DataDir; rt.data == "" {
		if rt.data, err = os.MkdirTemp("", "wl-"); err != nil {
			return nil, err
		}
		rt.ownData = true
	} else if err := os.MkdirAll(rt.data, 0o755); err != nil {
		return nil, err
	}

	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var v *bundle.Verification
	if fi.IsDir() {
		rt.dir = path
		v, err = bundle.Verify(path, bundle.VerifyOptions{Digest: opts.Digest})
	} else if opts.Host != nil {
		return nil, fmt.Errorf("wl: host mode opens a bundle directory, which the host loads too, not the archive %s", path)
	} else {
		// Each runtime extracts into a directory of its own, which Close
		// removes, so a data directory a host keeps (its object store)
		// opens again, however the last open ended.
		if rt.extracted, err = os.MkdirTemp(rt.data, "bundle-"); err != nil {
			return nil, err
		}
		if err := os.Chmod(rt.extracted, 0o755); err != nil {
			return nil, err
		}
		rt.dir = rt.extracted
		v, err = bundle.Extract(path, rt.dir, bundle.VerifyOptions{Digest: opts.Digest})
	}
	if err != nil {
		return nil, err
	}
	rt.digest, rt.manifest = v.Digest, v.Manifest
	if got := rt.manifest.Engine.Version; got != rt.version {
		return nil, fmt.Errorf("%w: engine %s built it, and this runtime is %s", ErrEngineVersion, got, rt.version)
	}
	prog, err := readProgram(filepath.Join(rt.dir, "ir.json"))
	if err != nil {
		return nil, err
	}
	rt.prog = prog
	rt.goProviders = maps.Clone(opts.Providers)
	providers := maps.Clone(opts.Providers)
	rt.providers.Store(&providers)
	switch {
	case opts.Plugin != "" && opts.Host != nil:
		return nil, errors.New("wl: in host mode the host plugin is the host's, not Options.Plugin")
	case opts.Plugin == "" && opts.Host == nil:
		// A plugin's providers count too, once it has declared them.
		if err := checkProviders(prog, opts.Providers); err != nil {
			return nil, err
		}
	}
	rt.state = rt.manifest.ModuleState
	if opts.ModuleState != "" {
		rt.state = engine.ModuleState(opts.ModuleState)
	}
	if opts.Host != nil {
		// Steps run in the host, which keeps their modules (ADR 0006).
		rt.state = engine.ModuleStateWarm
	}
	if rt.state != engine.ModuleStateWarm && rt.state != engine.ModuleStateFresh {
		return nil, fmt.Errorf("wl: module state %q is neither warm nor fresh", rt.state)
	}
	if opts.Plugin != "" && rt.state == engine.ModuleStateFresh {
		return nil, errors.New("wl: a host plugin needs warm harness processes: a fresh process lives for one step (Options.Plugin)")
	}
	if opts.Pool != (PoolOptions{}) {
		if rt.state == engine.ModuleStateFresh {
			return nil, errors.New("wl: a fresh runtime starts a harness process for each step and has no pool to set: Options.Pool must be zero")
		}
		if err := opts.Pool.harnessConfig().CheckSettings(); err != nil {
			return nil, fmt.Errorf("wl: Options.Pool: %w", err)
		}
	}

	store, err := platform.LoadData(os.DirFS(rt.dir))
	if err != nil {
		return nil, err
	}
	for p, r := range opts.Resources {
		if err := store.SetResource(p, platform.Resource{Type: r.Type, Value: r.Value}); err != nil {
			return nil, err
		}
	}
	for p, v := range opts.Variables {
		store.SetVariable(p, platform.Variable{Value: v.Value, IsSecret: v.Secret, Encrypted: v.Ciphertext})
	}
	objects, err := platform.NewDirObjects(filepath.Join(rt.data, "objects"))
	if err != nil {
		return nil, err
	}
	rt.store = store
	resourceStore := opts.ResourceStore
	if resourceStore == nil {
		rt.ownStore = platform.NewMemoryResourceStore()
		resourceStore = rt.ownStore
	}
	_, rt.memoryStore = resourceStore.(*platform.MemoryResourceStore)
	rt.services = &platform.Services{Resources: store, Variables: store, Objects: objects, Results: opts.Results, ResourceStore: resourceStore}
	rt.exec = &recorder{runs: map[string]*callLog{}, invs: map[string]invocation{}}
	if rt.fac, err = facade.New(facade.Config{Platform: rt.services, Answered: rt.exec.answered, Recorded: rt.exec.released}); err != nil {
		return nil, err
	}

	var inner engine.StepExecutor = noHarness{}
	switch {
	case opts.Host != nil:
		inner = hostExec{proc: &rt.hostProc}
	case len(prog.Scripts) > 0:
		if rt.pool, err = rt.harness(opts); err != nil {
			return nil, err
		}
		inner = rt.pool
	}
	rt.exec.inner = rt.fac.Executor(inner)
	if rt.js, err = quickjs.New(quickjs.Options{Slots: opts.ExpressionSlots}); err != nil {
		return nil, err
	}
	if rt.eng, err = engine.New(engine.Config{Program: prog, Bundle: rt.digest, ModuleState: rt.state, SharedCode: rt.sharedCode(), Exec: rt.exec, JS: rt.js, Platform: rt.services, Provide: rt.provide, QuickJSOnly: opts.QuickJSOnly}); err != nil {
		return nil, err
	}
	// The default dispatcher runs the bundle's roots as runs of their own
	// through the facade, so their calls are attributed; it is in the
	// services before any run starts.
	rt.dispatch = engine.NewDispatcher(rt.eng, rt.fac.Executor(inner))
	rt.services.Dispatcher = rt.dispatch
	if opts.Plugin != "" {
		if err := rt.loadPlugin(opts); err != nil {
			return nil, err
		}
	}
	if opts.Pool.WarmStart && rt.pool != nil {
		// Each start is bounded by the handshake's timeout.
		if err := rt.pool.Start(context.Background()); err != nil {
			return nil, fmt.Errorf("wl: the warm start of the harness pool: %w", err)
		}
	}
	if opts.Host != nil {
		attached = true
		if err := rt.attachHost(opts.Host, opts.Diagnostics, opts); err != nil {
			return nil, err
		}
	}
	return rt, nil
}

// harness checks the harness runtime against the manifest and makes the
// pool of Bun harness processes, in the runtime's module state.
func (rt *Runtime) harness(opts Options) (*harness.Pool, error) {
	if opts.HarnessRuntime == "" {
		return nil, errors.New("wl: the bundle has steps to run, and no harness runtime was given (Options.HarnessRuntime)")
	}
	runtimeDir, err := filepath.Abs(opts.HarnessRuntime)
	if err != nil {
		return nil, err
	}
	got, err := bundle.TreeDigest(runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("wl: the harness runtime: %w", err)
	}
	if want := rt.manifest.HarnessRuntimes[string(ir.ClassBun)]; got != want {
		return nil, fmt.Errorf("wl: the harness runtime %s has digest %s, and the bundle was built for %s", runtimeDir, got, want)
	}
	bun := opts.Bun
	if bun == "" {
		bun = "bun"
	} else if strings.ContainsRune(bun, filepath.Separator) {
		if bun, err = filepath.Abs(bun); err != nil {
			return nil, err
		}
	}
	work := filepath.Join(rt.data, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	path, args := harness.BunCommand(bun, filepath.Join(runtimeDir, "main.ts"), rt.dir)
	if opts.Plugin != "" {
		extra, err := pluginArgs(opts.Plugin)
		if err != nil {
			return nil, err
		}
		args = append(args, extra...)
	}
	cfg := opts.Pool.harnessConfig()
	cfg.Key = harness.Key{Class: string(ir.ClassBun), Runtime: harness.PinnedBun, Deps: rt.digest, ModuleState: rt.state}
	cfg.Process = harness.ProcessConfig{
		Path: path, Args: args, WorkRoot: work, Kind: string(ir.ClassBun), Runtime: harness.PinnedBun,
		Bundle: rt.digest, EngineVersion: rt.version,
		Diagnostics: opts.Diagnostics, Facade: rt.PlatformOrigin(), Platform: rt.fac.Platform(),
		ForeignWindmillOrigins: rt.foreign,
	}
	return harness.NewPool(cfg)
}

// readProgram reads ir.json, the compiled program, which must be of the
// IR version this engine reads.
func readProgram(path string) (*ir.Program, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var prog ir.Program
	if err := json.Unmarshal(data, &prog); err != nil {
		return nil, fmt.Errorf("wl: ir.json: %w", err)
	}
	if prog.Version != ir.Version {
		return nil, fmt.Errorf("wl: ir.json is of IR version %d, and this engine reads %d", prog.Version, ir.Version)
	}
	return &prog, nil
}

// lockedWriter serializes the writes to a host's diagnostics writer.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// WriteString passes the text on as io.WriteString does, so the host's
// own WriteString is still the one called.
func (l *lockedWriter) WriteString(s string) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return io.WriteString(l.w, s)
}

// noHarness is the executor of a bundle with no step: nothing reaches it.
type noHarness struct{}

func (noHarness) Execute(_ context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	if inv.Report != nil {
		inv.Report.Settled(engine.SettleNotStarted)
	}
	return engine.StepOutcome{}, errors.New("wl: the bundle has no harness for script " + inv.Script)
}

// BuildInfo is what a runtime is (ADR 0009): the bundle it opened and the
// engine that runs it.
type BuildInfo struct {
	// BundleDigest is the digest of the bundle's manifest (ADR 0010).
	BundleDigest string
	// EngineVersion is the runtime's engine version, which built the bundle.
	EngineVersion string
	// BunVersion is the Bun that built the scripts and that the harness
	// runs on; EsbuildVersion the esbuild of its codebase scripts, if any.
	BunVersion     string
	EsbuildVersion string
	// HarnessRuntimes are, by harness class, the digests of the harness
	// runtimes the bundle was built for.
	HarnessRuntimes map[string]string
	// ModuleState is the mode the runtime runs steps in, the manifest's
	// unless Options overrode it; SharedCode says that its steps run the
	// shared-layout modules of a bundle built with shared code (ADR 0013),
	// in warm mode.
	ModuleState string
	SharedCode  bool
	// Contract is the host contract the bundle was compiled against, as
	// the manifest holds it (JSON).
	Contract json.RawMessage
	// Deviations are the deviations of ADR 0001 in effect for the bundle:
	// those its manifest lists, with the module state's in place of the
	// manifest's mode's when Options overrode it (ADR 0006); those that
	// depend on a run-time setting are each run's trace's.
	Deviations []string
	// NonconformantRuntime says that the bundle was built on a Bun other
	// than the pin.
	NonconformantRuntime bool
}

// BuildInfo reports the bundle the runtime opened and the engine that runs
// it.
func (rt *Runtime) BuildInfo() BuildInfo {
	contract, _ := json.Marshal(rt.manifest.Contract)
	runtimes := make(map[string]string, len(rt.manifest.HarnessRuntimes))
	for k, v := range rt.manifest.HarnessRuntimes {
		runtimes[k] = v
	}
	return BuildInfo{
		BundleDigest:         rt.digest,
		EngineVersion:        rt.version,
		BunVersion:           rt.manifest.Bundlers.Bun,
		EsbuildVersion:       rt.manifest.Bundlers.Esbuild,
		HarnessRuntimes:      runtimes,
		ModuleState:          string(rt.state),
		SharedCode:           rt.sharedCode(),
		Contract:             contract,
		Deviations:           bundle.ModeDeviations(rt.manifest.Deviations, rt.state, rt.manifest.SharedCode),
		NonconformantRuntime: rt.manifest.NonconformantRuntime,
	}
}

// PlatformOrigin is the origin at which the runtime's steps reach its
// platform facade: what a step reads as BASE_URL, and a native step as
// BASE_INTERNAL_URL too (WA-2, WR-45). A warm or host-mode process reaches
// it through its intercepted fetch, at an origin where nothing listens; a
// fresh one over loopback TCP.
func (rt *Runtime) PlatformOrigin() string {
	if rt.state == engine.ModuleStateFresh {
		return rt.fac.FreshOrigin()
	}
	return rt.fac.WarmOrigin()
}

// sharedCode reports whether the runtime's steps run shared-layout modules
// (ADR 0013): the bundle has shared code, and steps run warm.
func (rt *Runtime) sharedCode() bool {
	return rt.manifest.SharedCode && rt.state == engine.ModuleStateWarm
}

// closeTimeout bounds the drain of the runs in flight, the dispatcher's
// runs and the harness pool at Close.
const closeTimeout = 10 * time.Second

// Close stops the runtime: Run admits no more runs, the runs in flight and
// the dispatcher's are canceled (the reason "the runtime is closing", by
// "windmill-lite") and waited for, then the harness pool (a warm process
// is drained), the facade and the expression engine are closed, and the
// directory an archive was extracted into, and the data directory when
// Open made it, are removed. All of it is bounded by 10 s; a run still in
// flight then makes Close return an error. Close is idempotent.
func (rt *Runtime) Close() error {
	rt.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		rt.mu.Lock()
		rt.closed = true
		rt.mu.Unlock()
		rt.cancelStop(closing)
		var errs []error
		if rt.dispatch != nil {
			rt.dispatch.Close(ctx)
		}
		drained := make(chan struct{})
		go func() {
			rt.active.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("wl: runs still in flight %s after Close canceled them", closeTimeout))
		}
		if rt.pool != nil {
			errs = append(errs, rt.pool.Close(ctx))
		}
		if rt.host != nil {
			// The host's connection ends, and with it host mode.
			rt.host.Kill()
			select {
			case <-rt.host.Done():
			case <-ctx.Done():
			}
		}
		if rt.fac != nil {
			errs = append(errs, rt.fac.Close())
		}
		if rt.ownStore != nil {
			// A closed runtime would otherwise keep the entries for as long
			// as the host keeps the runtime. Runs that outlived Close's
			// timeout may still read the store: the release takes the
			// store's lock, and their later reads fail as an outage, never
			// as a missing entry.
			platform.ReleaseMemoryResourceStore(rt.ownStore)
		}
		if rt.js != nil {
			errs = append(errs, rt.js.Close())
		}
		if rt.extracted != "" {
			errs = append(errs, os.RemoveAll(rt.extracted))
		}
		if rt.ownData && rt.data != "" {
			errs = append(errs, os.RemoveAll(rt.data))
		}
		rt.closeErr = errors.Join(errs...)
	})
	return rt.closeErr
}

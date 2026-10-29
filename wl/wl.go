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

// Options configure Open. The zero value of every field is a default, so a
// later milestone adds fields without breaking a host: the hooks and
// per-run providers of F2a and the budgets of F2b come as run options,
// host mode (F3a) is Host, and the go-plugin adapter of F5 is a runtime
// over the same calls.
type Options struct {
	// Bun is the Bun executable that harness processes run on; it must be
	// the pinned Bun 1.3.10, which the harness's handshake checks. Empty
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
	// run with a cache_ttl (FX-40, milestone N4), which a host can share
	// between runtimes; nil means one in memory for the runtime's runs
	// (D4).
	Results ResultCache
	// Diagnostics receives the harness processes' output that no step
	// owns, and the engine's diagnostics about them; nil means os.Stderr.
	Diagnostics io.Writer
	// ExpressionSlots is the number of evaluation slots of the runtime's
	// expression engine; zero means its default (ADR 0003).
	ExpressionSlots int
	// Providers are the named providers of the bundle's host-provided
	// sites (ADR 0002, ADR 0008), by the names the host contract gives
	// them. Open fails with ErrMissingProvider when the contract names one
	// that is not here; a static provided site needs none.
	Providers map[string]Provider
	// Host, when set, opens the runtime in host mode (ADR 0004 host mode,
	// ADR 0009): its steps run in the host process that started it, which
	// submits the runs. The bundle must then be a directory, which the
	// host loads too; steps run warm (ADR 0006), so ModuleState may only
	// be warm; HarnessRuntime and Bun are not used, since the host is the
	// harness. Open completes the handshake with the host before it
	// returns, and refuses the host, with the reason, when it fails.
	Host *Host
}

// Resource is a resource the host provides: its resource type, and its
// value as JSON, whose $var: and $res: references are resolved when it
// is read (WA-12).
type Resource struct {
	Type  string
	Value json.RawMessage
}

// Variable is a variable the host provides. A secret's Value is its
// plaintext, unless Ciphertext says it is Windmill ciphertext under the
// workspace key, which windmill-lite never decrypts (JE-43): reading it
// then fails with secret_unavailable.
type Variable struct {
	Value      string
	Secret     bool
	Ciphertext bool
}

// ResultCache is a store of cached job results (ADR 0007, N4): the engine
// computes the keys, the expiries and the bytes it stores, and a store
// keeps opaque bytes until their expiry, compared with the time a Get is
// given, never its own clock.
type ResultCache = platform.ResultCache

// ErrUnsupported marks a bundle that uses a feature the engine does not
// run yet; the error names the milestone of ADR 0012 that implements it.
var ErrUnsupported = engine.ErrUnsupported

// ErrEngineVersion marks a bundle that another engine version built.
var ErrEngineVersion = errors.New("wl: the bundle was built by another engine version")

// ErrClosed is Run's error once Close has begun.
var ErrClosed = errors.New("wl: the runtime is closed")

// closing is the cause Close cancels the runs in flight with (FX-38).
var closing = &Cancellation{Reason: "the runtime is closing", Canceler: "windmill-lite"}

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
// extracted into the data directory first. Harness processes start
// lazily, on the first step (a fresh runtime starts one per step).
//
// The runtime owns its platform services (ADR 0007): the bundle's
// resources and variables with the host's, which a step can update through
// the job API (WA-11), the job registry and flow user state, an object
// store under the data directory, and the default dispatcher over the
// bundle's program. The facade serves them to its steps.
func Open(path string, opts Options) (_ *Runtime, err error) {
	rt := &Runtime{version: opts.EngineVersion}
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
	if err := checkProviders(prog, opts.Providers); err != nil {
		return nil, err
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
	rt.services = &platform.Services{Resources: store, Variables: store, Objects: objects, Results: opts.Results}
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
	if rt.eng, err = engine.New(engine.Config{Program: prog, Bundle: rt.digest, ModuleState: rt.state, Exec: rt.exec, JS: rt.js, Platform: rt.services, Provide: provide(opts.Providers)}); err != nil {
		return nil, err
	}
	// The default dispatcher runs the bundle's roots as runs of their own
	// (milestone PF3), through the facade, so their calls are attributed;
	// it is in the services before any run starts.
	rt.dispatch = engine.NewDispatcher(rt.eng, rt.fac.Executor(inner))
	rt.services.Dispatcher = rt.dispatch
	if opts.Host != nil {
		attached = true
		if err := rt.attachHost(opts.Host, opts.Diagnostics); err != nil {
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
	// A warm process reaches the facade through its intercepted fetch, at
	// an origin where nothing listens; a fresh one over loopback TCP.
	origin := rt.fac.WarmOrigin()
	if rt.state == engine.ModuleStateFresh {
		origin = rt.fac.FreshOrigin()
	}
	path, args := harness.BunCommand(bun, filepath.Join(runtimeDir, "main.ts"), rt.dir)
	return harness.NewPool(harness.PoolConfig{
		Key: harness.Key{Class: string(ir.ClassBun), Runtime: harness.PinnedBun, Deps: rt.digest, ModuleState: rt.state},
		Process: harness.ProcessConfig{
			Path: path, Args: args, WorkRoot: work, Kind: string(ir.ClassBun), Runtime: harness.PinnedBun,
			Bundle: rt.digest, EngineVersion: rt.version,
			Diagnostics: opts.Diagnostics, Facade: origin, Platform: rt.fac.Platform(),
		},
	})
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
	// unless Options overrode it.
	ModuleState string
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
		Contract:             contract,
		Deviations:           bundle.ModeDeviations(rt.manifest.Deviations, rt.state),
		NonconformantRuntime: rt.manifest.NonconformantRuntime,
	}
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

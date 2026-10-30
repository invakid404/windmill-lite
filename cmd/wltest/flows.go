package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/oracle"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
	"github.com/invakid404/windmill-lite/wl"
)

// flowChecker runs flow fixtures (OR-15, the flow level of ADR 0003) on
// windmill-lite through package wl (milestone F1), in one module state:
// warm, where module state and globalThis survive from one invocation to
// the next (D1), or fresh, one harness process per invocation. For each
// fixture it compiles every run's target flow from the fixture's sync tree
// with the IR compiler (internal/ir), the target as the only root, to sort
// the targets it can run from those it cannot; builds one bundle
// (bundle.Build, ADR 0010) whose contract's roots are the targets it can
// run and the fixture's other roots; opens it with wl.Open, with the
// fixture's secrets and setup as the host's data; and runs the runs, in
// the order of conformance.RunBatches, with Runtime.Run. Each run's trace
// becomes an observation (observeTrace), compared with the run's expect,
// or with its recording when it has none (ADR 0011), and checked against
// its claims.
//
// A run whose flow needs what windmill-lite cannot run yet is reported as
// unsupported, with the reason, and never compared: a feature the
// compiler or the engine refuses (engine.ErrUnsupported names the
// milestone that implements it), another harness, stub mode, setup.yaml
// or the effect sink.
type flowChecker struct {
	bun, repo, mode string
	// sharedCode builds each fixture's bundle with shared code (ADR 0013):
	// warm and host-mode runs then run the scripts' shared-layout modules.
	sharedCode bool
	js         expr.Engine
	// afterRuns, when set, is called in host mode once a fixture's runs
	// have ended, before its host's teardown (tests: hostmode_test.go).
	afterRuns func(*hostRuntime)
}

// runResult is how one run of a fixture went.
type runResult struct {
	fixture, run string
	// unsupported says why the run was not checked; problems lists what
	// failed. A run with neither passed.
	unsupported string
	problems    []string
	diag        string
}

// flowRunTimeout bounds one run; a run that exceeds it is canceled and
// fails. The slowest runs wait for real: a transition the monitor of
// flows that hang between steps cancels at 60 s (JE-2/a), and retry delays
// that add up to 26 s (FX-26/a). Tests replace it.
var flowRunTimeout = 150 * time.Second

// fixtureResult is how one fixture went: the fixture failed to load, or
// its runs' results.
type fixtureResult struct {
	dir  string
	err  error
	runs []runResult
}

// runFixtures loads and runs the fixtures in dirs, a few at a time, and
// returns their results in the order of dirs. An entry is a fixture
// directory or an expression-case file (conformance/exprs), which runs as
// its generated one-step flow fixture, as wltest record lifts it
// (conformance.LiftExprFile).
func (fc *flowChecker) runFixtures(ctx context.Context, dirs []string) []fixtureResult {
	out := make([]fixtureResult, len(dirs))
	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/2))
	var wg sync.WaitGroup
	for i, dir := range dirs {
		out[i].dir = dir
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			f, cleanup, err := loadFixture(dir)
			if err != nil {
				out[i].err = err
				return
			}
			defer cleanup()
			out[i].runs = fc.runFixture(ctx, f)
		}()
	}
	wg.Wait()
	return out
}

// loadFixture loads a fixture directory, or lifts an expression-case file
// into its generated fixture in a temporary directory, which cleanup
// removes.
func loadFixture(path string) (f *conformance.Fixture, cleanup func(), err error) {
	if !conformance.IsExprFile(path) {
		f, err := conformance.LoadFixture(path)
		return f, func() {}, err
	}
	ef, err := conformance.LoadExprFile(path)
	if err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp("", "wltest-exprs-")
	if err != nil {
		return nil, nil, err
	}
	cleanup = func() { os.RemoveAll(tmp) }
	if f, err = conformance.LiftExprFile(ef, filepath.Join(tmp, "lifted")); err != nil {
		cleanup()
		return nil, nil, err
	}
	return f, cleanup, nil
}

// runFixture builds f's bundle and runs every run, in name order within
// the batches of conformance.RunBatches.
func (fc *flowChecker) runFixture(ctx context.Context, f *conformance.Fixture) []runResult {
	results := make([]runResult, len(f.Runs))
	for i, r := range f.Runs {
		results[i] = runResult{fixture: f.Dir, run: r.Name}
	}
	if len(f.Roots) > 0 && (f.Stub || f.Oracle.Effects) {
		// The default dispatcher's runs (milestone PF3) run without a
		// run's hooks, so their jobs would get no WL_EFFECT_SINK, while on
		// the oracle every job gets it.
		for i := range results {
			results[i].unsupported = "a fixture with roots gets no effect sink in its dispatched runs yet (ADR 0011, Effects)"
		}
		return results
	}
	work, err := os.MkdirTemp("", "wltest-flows-")
	if err != nil {
		return failRuns(results, err.Error())
	}
	defer os.RemoveAll(work)

	tree := f.SyncTree()
	if f.Stub {
		// Stub mode (ADR 0011, milestone E4b): the runs run the stubbed
		// copy of the tree that wltest record pushes to the oracle, every
		// script body replaced by a stub that asks the replay service. The
		// copy carries what the codebase entries name outside the tree
		// beside it, as the original has it (oracle.Layout), so its
		// unchanged wmill.yaml reaches the same files.
		root, err := syncfolder.Find(tree)
		if err != nil {
			return failRuns(results, "stub: "+err.Error())
		}
		rep, err := oracle.StubTree(root, filepath.Join(work, "stubbed"))
		if err != nil {
			return failRuns(results, "stub: "+err.Error())
		}
		tree = filepath.Join(work, "stubbed", filepath.FromSlash(rep.Root))
	}
	b, err := fc.build(ctx, f, tree, work)
	if err != nil {
		return failRuns(results, "build: "+err.Error())
	}
	var effects *effectServer
	if f.Stub || f.Oracle.Effects {
		// The fixture's effect sink and replay service (ADR 0011).
		if effects, err = newEffectServer(); err != nil {
			return failRuns(results, "effect sink: "+err.Error())
		}
		defer effects.Close()
	}
	var diag syncBuffer
	var rt *wl.Runtime
	var host *hostRuntime
	if b.out != "" {
		// The runtime owns the fixture's platform services (ADR 0007): the
		// bundle's resources and variables with the host's, its secrets
		// and setup; the facade its steps reach, which attributes their
		// calls; and the default dispatcher (milestone PF3) over the
		// bundle's program, which runs the fixture's other roots. In host
		// mode it serves the fixture's own host (openHost).
		opts := wl.Options{
			Bun: fc.bun, HarnessRuntime: fc.harnessRuntime(), DataDir: filepath.Join(work, "data"), ModuleState: fc.mode,
			Resources: b.resources, Variables: b.variables, Diagnostics: &diag, ExpressionSlots: 4,
			Providers: fixtureProviders(f),
		}
		if fc.mode == ModeHost {
			if host, err = fc.openHost(b.out, work, opts, &diag); err != nil {
				// The host's output, which its close copied, says why.
				results = failRuns(results, "host: "+err.Error())
				for i := range results {
					results[i].diag = diag.String()
				}
				return results
			}
			defer host.close()
			rt = host.rt
		} else {
			if rt, err = wl.Open(b.out, opts); err != nil {
				return failRuns(results, "open: "+err.Error())
			}
			defer rt.Close()
		}
	}

	byName := map[string]int{}
	for i, r := range f.Runs {
		byName[r.Name] = i
	}
	for _, batch := range conformance.RunBatches(f.Runs) {
		var live []conformance.Run
		for _, r := range batch {
			res := &results[byName[r.Name]]
			t := b.targets[r.Target]
			switch {
			case r.IsScript():
				res.unsupported = "a script run (kind: script) has no runner on windmill-lite yet"
			case r.RunsAndWaits():
				res.unsupported = "a run-and-wait response (submit: " + conformance.SubmitRunWait + ") needs the HTTP adapter of milestone F7b"
			case t == nil:
				res.problems = []string{"no target " + r.Target}
			case t.unsupported != "":
				res.unsupported = t.unsupported
			case t.problem != "":
				res.problems = []string{t.problem}
			default:
				live = append(live, r)
			}
		}
		// Runs of one group are concurrent outer runs (OR-15), and one test
		// of them (ADR 0011): a group runs whole or not at all.
		group := batch[0].Group
		if group != "" && len(live) < len(batch) {
			why := groupUnrunnable(group, batch, results, byName)
			for _, r := range live {
				results[byName[r.Name]].unsupported, results[byName[r.Name]].problems = why.unsupported, why.problems
			}
			continue
		}
		var g *runGroup
		if group != "" {
			g = newRunGroup(group, len(live))
		}
		var wg sync.WaitGroup
		for _, r := range live {
			res := &results[byName[r.Name]]
			var gm *groupMember
			if g != nil {
				gm = g.member(r.Name)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				res.problems = fc.runOne(ctx, f, r, rt, b.targets[r.Target], effects, gm)
			}()
		}
		wg.Wait()
		if g != nil {
			names := make([]string, len(live))
			for i, r := range live {
				names[i] = r.Name
			}
			err := g.conclude(names, func(run string) bool { return len(results[byName[run]].problems) > 0 })
			for _, r := range live {
				if res := &results[byName[r.Name]]; err != nil && len(res.problems) == 0 {
					res.problems = []string{err.Error()}
				}
			}
		}
	}
	if host != nil {
		if fc.afterRuns != nil {
			fc.afterRuns(host)
		}
		// One generation serves the fixture: a host lost before its engine
		// closes fails every run it took, whatever those runs observed.
		early := host.exitedEarly()
		host.close()
		if early {
			for i := range results {
				if results[i].unsupported == "" {
					results[i].problems = append(results[i].problems, "the fixture's host was lost before its engine closed")
				}
			}
		}
	}
	if d := diag.String(); d != "" {
		for i := range results {
			if len(results[i].problems) > 0 {
				results[i].diag = d
			}
		}
	}
	return results
}

// moduleState is the module-state mode of the checker's bundles: a host
// runs its steps warm (ADR 0006).
func (fc *flowChecker) moduleState() string {
	if fc.mode == ModeHost {
		return conformance.ModeWarm
	}
	return fc.mode
}

// harnessRuntime is the Bun harness runtime the bundles are built for and
// run on.
func (fc *flowChecker) harnessRuntime() string {
	return filepath.Join(fc.repo, "harnesses", "bun", "runtime")
}

// groupUnrunnable is what becomes of the runs of group that could start
// when another run of it could not (batch is the group's runs): the group
// is unsupported when a run of it is, and fails as a whole when a run of
// it failed before it started (group_member_failed).
func groupUnrunnable(group string, batch []conformance.Run, results []runResult, byName map[string]int) runResult {
	for _, r := range batch {
		if res := results[byName[r.Name]]; res.unsupported != "" {
			return runResult{unsupported: "run " + r.Name + " of group " + group + " is unsupported: " + res.unsupported}
		}
	}
	for _, r := range batch {
		if len(results[byName[r.Name]].problems) > 0 {
			return runResult{problems: []string{`{"error":"group_member_failed","group":` + jsonText(group) + `,"run":` + jsonText(r.Name) + `}`}}
		}
	}
	return runResult{}
}

// jsonText is s as a JSON string, as refusals quote their members.
func jsonText(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

func failRuns(rs []runResult, problem string) []runResult {
	for i := range rs {
		rs[i].problems = []string{problem}
	}
	return rs
}

// fixtureBundle is the bundle of one fixture, what each run's target flow
// needs, and the host's data its runtime is opened with.
type fixtureBundle struct {
	// out is the bundle; empty when no target can run.
	out       string
	targets   map[string]*target
	order     []string // the targets in the order of their first run
	variables map[string]wl.Variable
	resources map[string]wl.Resource
}

// target is one flow that runs of the fixture run.
type target struct {
	// prog is the flow's program, compiled with the flow as its only root.
	prog    *ir.Program
	modules []string // the module ids of the flow, in order
	virtual []bool   // which modules are the generated `<id>-v` step (OF-17)
	failure bool     // the flow has a failure module
	// reached are the scripts the flow reaches, by inventory id.
	reached []string
	// unsupported or problem, when set, is why the flow's runs are not
	// checked, or fail.
	unsupported, problem string
}

// unsupportedKinds are the compile errors of features windmill-lite does
// not run (ADR 0001 §3, §5a): a run that meets one is unsupported, not
// failed.
var unsupportedKinds = map[ir.ErrorKind]bool{ir.ErrUnsupported: true, ir.ErrLanguage: true, ir.ErrPinnedHash: true}

// build compiles each run's target flow alone from the sync tree at tree
// (the fixture's, or its stubbed copy in stub mode), and opens its program
// on the engine, to sort the targets it can run from those it cannot; then it
// builds one bundle below work with bundle.Build (ADR 0010), whose
// contract's roots are the targets it can run and the fixture's other
// roots, and the engine version wl's.
//
// What goes wrong with one target (a compile error, a refused feature, a
// build problem of a script it reaches) is that target's: the bundle is
// built again without it. Only a folder that does not load, or a build
// that fails as a whole, fails the fixture.
func (fc *flowChecker) build(ctx context.Context, f *conformance.Fixture, tree, work string) (*fixtureBundle, error) {
	root, err := syncfolder.Find(tree)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	folder, err := syncfolder.Load(r.FS())
	if err != nil {
		return nil, err
	}
	defer folder.Close()

	b := &fixtureBundle{targets: map[string]*target{}}
	if b.variables, b.resources, err = hostDataOf(folder, f.Setup, f.Oracle.SetupPolicy()); err != nil {
		return nil, err
	}
	provided, err := providedSites(f)
	if err != nil {
		return nil, err
	}
	// items maps how a build problem names a script (bundle.ScriptItem) to
	// its inventory id.
	items := map[string]string{}
	open := func(t *target) {
		if _, err := engine.New(engine.Config{Program: t.prog, JS: fc.js}); errors.Is(err, engine.ErrUnsupported) {
			t.unsupported = "the engine does not run it yet: " + err.Error()
		} else if err != nil {
			t.problem = "engine: " + err.Error()
		}
		if t.prog != nil {
			for id, s := range t.prog.Scripts {
				items[bundle.ScriptItem(s)] = id
			}
		}
	}
	for _, run := range f.Runs {
		if run.IsScript() || b.targets[run.Target] != nil {
			continue
		}
		t := &target{}
		b.targets[run.Target] = t
		b.order = append(b.order, run.Target)
		if fc.compile(folder, run.Target, provided, t) {
			open(t)
		}
	}
	var roots []ir.Root
	if len(f.Roots) > 0 {
		// The bundle's other roots (the targets its steps dispatch, which
		// a host contract lists, ADR 0002), which the default dispatcher
		// runs (milestone PF3); a problem of theirs fails no run, only the
		// dispatches that would reach them.
		t := &target{}
		b.targets[rootsTarget] = t
		flows := folder.Paths(syncfolder.KindFlow)
		for _, p := range f.Roots {
			if slices.Contains(flows, p) {
				roots = append(roots, ir.Root{Flow: p})
			} else {
				roots = append(roots, ir.Root{Script: p})
			}
		}
		if fc.compileRoots(folder, roots, provided, t) {
			open(t)
		}
	}

	for attempt := 0; ; attempt++ {
		contract := ir.Contract{Provided: provided}
		for _, p := range b.order {
			if t := b.targets[p]; t.unsupported == "" && t.problem == "" {
				contract.Roots = append(contract.Roots, ir.Root{Flow: p})
			}
		}
		if t := b.targets[rootsTarget]; t != nil && t.unsupported == "" && t.problem == "" {
			contract.Roots = append(contract.Roots, roots...)
		}
		if len(contract.Roots) == 0 {
			return b, nil
		}
		data, err := json.Marshal(contract)
		if err != nil {
			return nil, err
		}
		out := filepath.Join(work, fmt.Sprintf("bundle-%d", attempt))
		_, err = bundle.Build(ctx, bundle.Config{
			Folder:         root,
			Contract:       data,
			ModuleState:    engine.ModuleState(fc.moduleState()),
			SharedCode:     fc.sharedCode,
			EngineVersion:  wl.Version(),
			Bun:            fc.bun,
			Helper:         filepath.Join(fc.repo, "harnesses", "bun", "build", "main.ts"),
			HarnessRuntime: fc.harnessRuntime(),
			Out:            out,
			Work:           filepath.Join(work, "build"),
		})
		var be *bundle.BuildError
		switch {
		case err == nil:
			b.out = out
			return b, nil
		case !errors.As(err, &be):
			return nil, err
		}
		// A problem of one script is the problem of the targets that reach
		// it, which the next build leaves out.
		blamed := false
		for _, p := range be.Problems {
			id, ok := items[p.Item]
			if !ok {
				return nil, err
			}
			for _, key := range append(slices.Clone(b.order), rootsTarget) {
				if t := b.targets[key]; t != nil && t.problem == "" && t.unsupported == "" && slices.Contains(t.reached, id) {
					t.problem = "build: " + p.Item + ": " + p.Kind + ": " + p.Message
					blamed = true
				}
			}
		}
		if !blamed {
			return nil, err
		}
	}
}

// rootsTarget keys the default dispatcher's program among a fixture's
// targets: no run's target, since a path has no parenthesis.
const rootsTarget = "(roots)"

// compile compiles one target flow, with it as the only root, into t: its
// program, its module ids and the scripts it reaches. A feature the
// compiler or this runner refuses makes t unsupported; a compile error is
// t's problem. It reports whether t has neither.
func (fc *flowChecker) compile(folder *syncfolder.Folder, path string, provided []ir.ProvidedSite, t *target) bool {
	if !fc.compileRoots(folder, []ir.Root{{Flow: path}}, provided, t) {
		return false
	}
	fl := t.prog.Flows[path]
	if fl == nil {
		t.problem = "compile: no flow " + path
		return false
	}
	for _, m := range fl.Modules {
		t.modules = append(t.modules, m.ID)
		t.virtual = append(t.virtual, m.Virtual)
	}
	t.failure = fl.FailureModule != nil
	return true
}

// compileRoots compiles the folder with roots as its contract's roots into
// t: its program and the scripts it reaches. It reports whether t has
// neither a problem nor a reason to be unsupported.
func (fc *flowChecker) compileRoots(folder *syncfolder.Folder, roots []ir.Root, provided []ir.ProvidedSite, t *target) bool {
	// The fixture's provided sites that these roots reach: a mandatory
	// selector that matches no site is a contract error (ADR 0008), so the
	// ones the compile reports unmatched are left out of this compile.
	sites := slices.Clone(provided)
	var prog *ir.Program
	var err error
	for {
		c := &ir.Contract{Roots: roots, Provided: sites}
		if err = c.Validate(); err != nil {
			t.problem = err.Error()
			return false
		}
		prog, err = ir.Compile(folder, c, ir.Options{})
		drop := unmatchedSites(err)
		if len(drop) == 0 {
			break
		}
		var keep []ir.ProvidedSite
		for i, p := range sites {
			if !slices.Contains(drop, i) {
				keep = append(keep, p)
			}
		}
		sites = keep
	}
	if err != nil {
		var ie *ir.Error
		if errors.As(err, &ie) && unsupportedKinds[ie.Kind] {
			t.unsupported = "compile: " + err.Error()
		} else {
			t.problem = "compile: " + err.Error()
		}
		return false
	}
	t.prog = prog
	for _, id := range slices.Sorted(maps.Keys(prog.Scripts)) {
		s := prog.Scripts[id]
		// The Bun harness runs both its boundaries, and the engine invokes
		// each step with its own (C3n); other harnesses are L1's.
		if s.Harness != ir.ClassBun || (s.Boundary != ir.BoundaryBun && s.Boundary != ir.BoundaryNative) {
			t.unsupported = fmt.Sprintf("script %s runs in the %s boundary of the %s harness, which windmill-lite does not have yet", s.Path, s.Boundary, s.Harness)
			return false
		}
		t.reached = append(t.reached, id)
	}
	return true
}

// providedSites reads the fixture's provided sites (Fixture.Provided) as
// the host contract's (ADR 0002), which checks them.
func providedSites(f *conformance.Fixture) ([]ir.ProvidedSite, error) {
	if len(f.Provided) == 0 {
		return nil, nil
	}
	text, err := json.Marshal(map[string]any{"roots": []ir.Root{{Flow: "(fixture)"}}, "provided": f.Provided})
	if err != nil {
		return nil, err
	}
	c, err := ir.ParseContract(text)
	if err != nil {
		return nil, fmt.Errorf("provided: %w", err)
	}
	return c.Provided, nil
}

// unmatchedSites are the provided sites, by their index in the contract,
// that a compile reports matching no site: the contract errors about one
// provided site that name no flow or step.
func unmatchedSites(err error) []int {
	var out []int
	errs := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	}
	for _, e := range errs {
		var ie *ir.Error
		if errors.As(e, &ie) && ie.Kind == ir.ErrContract && ie.Selector >= 0 && ie.Flow == "" && ie.Step == "" {
			out = append(out, ie.Selector)
		}
	}
	return out
}

// fixtureProviders are the fixture's named providers (Fixture.Providers)
// as wl registers them: each answers its sites with its result or its
// error, or declines.
func fixtureProviders(f *conformance.Fixture) map[string]wl.Provider {
	if len(f.Providers) == 0 {
		return nil
	}
	out := make(map[string]wl.Provider, len(f.Providers))
	for name, p := range f.Providers {
		out[name] = func(context.Context, wl.Call) (wl.Outcome, bool, error) {
			switch {
			case p.Decline:
				return wl.Outcome{}, false, nil
			case p.Error != nil:
				return wl.Outcome{Result: p.Error, Failed: true}, true, nil
			}
			return wl.Outcome{Result: p.Result}, true, nil
		}
	}
	return out
}

// hostModuleEntry is a host-provided module in a step or fixture bundle's
// manifest.
type hostModuleEntry struct {
	Key string `json:"key"`
	ID  string `json:"id"`
}

// writeManifest writes the manifest.json of a bundle built for wltest
// check, which the harness reads for its host-provided modules, and
// returns its digest, the bundle digest of the handshake.
func writeManifest(out, format, note string, hosts []hostModuleEntry) (string, error) {
	manifest := struct {
		Format      string            `json:"format"`
		Note        string            `json:"note"`
		HostModules []hostModuleEntry `json:"host_modules"`
	}{Format: format, Note: note, HostModules: hosts}
	if manifest.HostModules == nil {
		manifest.HostModules = []hostModuleEntry{}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), data, 0o644); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// runOne runs one run on the runtime and compares its observation. A run's
// cancel_after_ms cancels it with the reason and the canceler the oracle's
// cancellation names (FX-38). With the fixture's effect server (es, nil
// when the fixture has neither effects nor stub mode), the run's effects
// are observed as the oracle's are: the sink's, then, in stub mode, each
// answered stub call, once the calls agree with the trace
// (conformance.CheckReplay). A run of a group (gm, nil for a run of no
// group) is held at its first module until the group's other runs are in
// flight, and its span, from submission to end, is recorded for the
// group's check. Both go through the run's hooks (wl.RunOptions.Hooks).
func (fc *flowChecker) runOne(ctx context.Context, f *conformance.Fixture, r conformance.Run, rt *wl.Runtime, t *target, es *effectServer, gm *groupMember) []string {
	cctx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	rctx, cancel := context.WithTimeout(cctx, flowRunTimeout)
	defer cancel()
	if r.CancelAfterMS != nil {
		stop := time.AfterFunc(time.Duration(*r.CancelAfterMS)*time.Millisecond, func() {
			cancelRun(&wl.Cancellation{Reason: oracle.CancelAfterReason, Canceler: oracle.CancelAfterCanceler})
		})
		defer stop.Stop()
	}
	id := newUUID()
	opts := wl.RunOptions{ID: id}
	if es != nil || gm != nil {
		var fx *runEffects
		if es != nil {
			fx = es.openRun(id, f.Stub, r.Replay)
		}
		opts.Hooks = runHooks(fx, gm)
	}
	submitted := time.Now()
	res, err := rt.Run(rctx, r.Target, r.ArgsOrEmpty(), opts)
	if gm != nil {
		gm.ended(submitted, time.Now())
	}
	var effects []json.RawMessage
	var calls []conformance.ReplayCall
	if es != nil {
		effects, calls = es.closeRun(id)
	}
	if err != nil {
		return []string{"run: " + err.Error()}
	}
	// A canceled run has its trace too, so a run that timed out leaves no
	// counter files either.
	tr := res.Trace
	defer removeCounters(tr)
	if r.CancelAfterMS == nil && errors.Is(context.Cause(rctx), context.DeadlineExceeded) {
		return []string{fmt.Sprintf("the run did not end within %s", flowRunTimeout)}
	}
	// A run whose calls disagree with its trace fails, and its observation
	// is still compared, so that what made its steps fail is shown too.
	var problems []string
	if f.Stub {
		if err := checkStub(tr, t.prog, r.Target, calls); err != nil {
			problems = append(problems, "stub: "+err.Error())
		}
		for _, c := range calls {
			if c.Effect != nil {
				effects = append(effects, c.Effect)
			}
		}
	}
	// Every job the run's steps ran, superseded attempts' included, which
	// the normaliser replaces with its site's placeholder (OR-14).
	jobs := map[string]conformance.SitePath{}
	for _, rec := range tr.Steps {
		if rec.Job != "" {
			jobs[rec.Job] = rec.Site
		}
	}
	got, generated, err := observeTrace(tr, t.modules, t.virtual, t.failure, jobs, f.Compare, r.Copies, effects)
	if err != nil {
		return append(problems, "observe: "+err.Error())
	}
	problems = append(problems, compareRun(f, r, got, generated)...)
	return append(problems, checkDelays(tr, r.Delays)...)
}

// counterDir is where fixtures keep the files that count a step's attempts
// across retries; tests replace it.
var counterDir = "/tmp"

// removeCounters removes the files a run's steps left in counterDir to
// count their attempts across retries, which the fixtures name
// wl-<fixture>-<job id>-<tag> after a job of the run (its root, a child
// flow, or a step's own job): on Windmill they live in the worker's /tmp,
// but a windmill-lite run would leave them in the host's (N5's follow-up).
// Files named after other runs' jobs are left alone.
func removeCounters(t *engine.Trace) {
	ids := []string{t.RunID}
	for _, c := range t.Children {
		ids = append(ids, c.ID)
	}
	for _, rec := range t.Steps {
		ids = append(ids, rec.Job)
	}
	for _, id := range ids {
		if id == "" || strings.ContainsAny(id, `/\*?[`) {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(counterDir, "wl-*-"+id+"-*"))
		for _, m := range matches {
			os.Remove(m)
		}
	}
}

// checkDelays checks a run's delays on windmill-lite's side (JE-28, FX-23,
// FX-26): each To site's job starts at least AtLeastMS after the From
// site's job ended, by the trace's job times (StepRecord.Start and
// Duration), where the oracle's side reads them from the recording's
// provenance.
func checkDelays(t *engine.Trace, delays []conformance.Delay) []string {
	var problems []string
	for _, d := range delays {
		from, ok1 := siteRecord(t, d.From)
		to, ok2 := siteRecord(t, d.To)
		if !ok1 || !ok2 {
			problems = append(problems, fmt.Sprintf("delay %s to %s: the trace has no job at both sites", d.From, d.To))
			continue
		}
		if gap := to.Start.Sub(from.Start.Add(from.Duration)); gap < time.Duration(d.AtLeastMS)*time.Millisecond {
			problems = append(problems, fmt.Sprintf("delay %s to %s: %v, want at least %d ms", d.From, d.To, gap, d.AtLeastMS))
		}
	}
	return problems
}

// siteRecord is the trace's record of the job at site, a site path's text.
func siteRecord(t *engine.Trace, site string) (engine.StepRecord, bool) {
	p, err := conformance.ParseSitePath(site)
	if err != nil {
		return engine.StepRecord{}, false
	}
	rec, ok := t.Step(p)
	return rec, ok && rec.Ran
}

// compareRun compares a run's observation with its expect, or with its
// recording when it has none (ADR 0011), and checks its claims. A
// recording holds neither carried nor resolved_args, so they are compared
// only with an expect. On the reference's side, the location suffix of an
// internal error is stripped where windmill-lite generated the value
// (ADR 0011, Location suffixes), and so is a parenthesised not-found
// location the run declares (Run.NotFoundLocs) where windmill-lite
// generated the value (FX-50 rule 3).
func compareRun(f *conformance.Fixture, r conformance.Run, got conformance.Observation, generated []string) []string {
	opts := f.Compare.Options()
	var want conformance.Observation
	switch {
	case r.Expect != nil:
		want = *r.Expect
	case r.Recorded != nil:
		want = r.Recorded.Observation
		opts.WithRecording = true
	case len(r.Claims) == 0:
		return []string{"the run has no expect, recording or claims to compare with"}
	}
	var problems []string
	if r.Expect != nil || r.Recorded != nil {
		ref, err := conformance.StripLocations(want, generated)
		if err == nil {
			ref, err = conformance.StripNotFoundLocations(ref, slices.DeleteFunc(slices.Clone(r.NotFoundLocs), func(p string) bool {
				return !slices.ContainsFunc(generated, func(g string) bool { return p == g || strings.HasPrefix(p, g+"/") })
			}))
		}
		if err != nil {
			return []string{"compare: " + err.Error()}
		}
		diffs, err := conformance.Compare(got, ref, opts)
		if err != nil {
			return []string{"compare: " + err.Error()}
		}
		for _, d := range diffs {
			problems = append(problems, d.String())
		}
	}
	lines, err := conformance.CheckClaims(got, r.Claims)
	if err != nil {
		return append(problems, "claims: "+err.Error())
	}
	problems = append(problems, lines...)
	if len(problems) > 0 {
		problems = append(problems, "observed "+compactJSON(got))
	}
	return problems
}

package main

import (
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
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// flowChecker runs flow fixtures (OR-15, the flow level of ADR 0003) on
// windmill-lite, in one configuration (milestone E1). For each fixture it
// compiles every run's target flow from the fixture's sync tree with the
// IR compiler (internal/ir), the target as the only root; builds the Bun
// scripts those flows reach with the Bun build helper into one bundle;
// and runs the runs, in the order of conformance.RunBatches, on the walking
// skeleton's interpreter (engine.Skeleton, milestone W1) over one pool of
// Bun harness processes. That pool is the warm configuration: module state
// and globalThis survive from one invocation to the next (D1). Each run's
// trace becomes an observation (observeTrace), compared with the run's
// expect, or with its recording when it has none (ADR 0011), and checked
// against its claims.
//
// A run whose flow needs what windmill-lite cannot run yet is reported as
// unsupported, with the reason, and never compared: a feature the
// compiler or the skeleton refuses, another harness, stub mode, setup.yaml
// or the effect sink. N1a replaces the skeleton with the engine core, and
// the runs it can then run are checked.
type flowChecker struct {
	bun, repo, mode string
	js              expr.Engine
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
// fails.
const flowRunTimeout = 60 * time.Second

// fixtureResult is how one fixture went: the fixture failed to load, or
// its runs' results.
type fixtureResult struct {
	dir  string
	err  error
	runs []runResult
}

// runFixtures loads and runs the fixtures in dirs, a few at a time, and
// returns their results in the order of dirs.
func (fc *flowChecker) runFixtures(ctx context.Context, dirs []string) []fixtureResult {
	out := make([]fixtureResult, len(dirs))
	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/2))
	var wg sync.WaitGroup
	for i, dir := range dirs {
		out[i].dir = dir
		f, err := conformance.LoadFixture(dir)
		if err != nil {
			out[i].err = err
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i].runs = fc.runFixture(ctx, f)
		}()
	}
	wg.Wait()
	return out
}

// runFixture builds f's bundle and runs every run, in name order within
// the batches of conformance.RunBatches.
func (fc *flowChecker) runFixture(ctx context.Context, f *conformance.Fixture) []runResult {
	results := make([]runResult, len(f.Runs))
	for i, r := range f.Runs {
		results[i] = runResult{fixture: f.Dir, run: r.Name}
	}
	whole := ""
	switch {
	case f.Stub:
		whole = "stub mode on windmill-lite is milestone E4b's"
	case f.Setup != nil:
		whole = "setup.yaml: resources and variables need the platform providers of milestone PF2"
	case f.Oracle.Effects:
		whole = "the effect sink is not injected on windmill-lite yet (ADR 0011, Effects)"
	}
	if whole != "" {
		for i := range results {
			results[i].unsupported = whole
		}
		return results
	}
	work, err := os.MkdirTemp("", "wltest-flows-")
	if err != nil {
		return failRuns(results, err.Error())
	}
	defer os.RemoveAll(work)

	b, err := fc.build(ctx, f, work)
	if err != nil {
		return failRuns(results, "build: "+err.Error())
	}
	var diag syncBuffer
	path, args := harness.BunCommand(fc.bun, filepath.Join(fc.repo, "harnesses", "bun", "runtime", "main.ts"), b.out)
	pool, err := harness.NewPool(harness.PoolConfig{
		Key: harness.Key{Class: "bun", Runtime: harness.PinnedBun, Deps: "wltest-flows", ModuleState: engine.ModuleStateWarm},
		Process: harness.ProcessConfig{
			Path: path, Args: args, WorkRoot: work, Kind: "bun", Runtime: harness.PinnedBun,
			Bundle: b.digest, EngineVersion: "wltest-check", Diagnostics: &diag,
		},
	})
	if err != nil {
		return failRuns(results, "pool: "+err.Error())
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool.Close(cctx)
	}()

	skeletons := map[string]*engine.Skeleton{}
	for i, target := range b.order {
		t := b.targets[target]
		if t.unsupported != "" || t.problem != "" {
			continue
		}
		// One skeleton per flow, so that a flow it refuses leaves the
		// fixture's others to run.
		dir := filepath.Join(work, "flows", fmt.Sprint(i))
		file := filepath.Join(dir, "flows", filepath.FromSlash(target)+".flow.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return failRuns(results, err.Error())
		}
		if err := os.WriteFile(file, t.flowJSON, 0o644); err != nil {
			return failRuns(results, err.Error())
		}
		s, err := engine.NewSkeleton(engine.SkeletonConfig{BundleDir: dir, Bundle: b.digest, Exec: pool, JS: fc.js})
		switch {
		case errors.Is(err, engine.ErrSkeletonUnsupported):
			t.unsupported = "the walking skeleton does not run it: " + err.Error()
		case err != nil:
			t.problem = "skeleton: " + err.Error()
		default:
			skeletons[target] = s
		}
	}

	byName := map[string]int{}
	for i, r := range f.Runs {
		byName[r.Name] = i
	}
	for _, batch := range conformance.RunBatches(f.Runs) {
		var wg sync.WaitGroup
		for _, r := range batch {
			res := &results[byName[r.Name]]
			t := b.targets[r.Target]
			switch {
			case r.IsScript():
				res.unsupported = "a script run (kind: script) has no runner on windmill-lite yet"
				continue
			case t == nil:
				res.problems = []string{"no target " + r.Target}
				continue
			case t.unsupported != "":
				res.unsupported = t.unsupported
				continue
			case t.problem != "":
				res.problems = []string{t.problem}
				continue
			}
			// Runs of one group are concurrent outer runs (OR-15).
			wg.Add(1)
			go func() {
				defer wg.Done()
				res.problems = fc.runOne(ctx, f, r, skeletons[r.Target], pool, t)
			}()
		}
		wg.Wait()
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

func failRuns(rs []runResult, problem string) []runResult {
	for i := range rs {
		rs[i].problems = []string{problem}
	}
	return rs
}

// fixtureBundle is the bundle of one fixture: its Bun scripts, and what
// each run's target flow needs.
type fixtureBundle struct {
	out, digest string
	targets     map[string]*target
	order       []string // the targets in the order of their first run
}

// target is one flow that runs of the fixture run.
type target struct {
	flowJSON []byte
	modules  []string // the module ids of the flow, in order
	// scripts maps the site of each of the flow's leaf modules to its
	// script's inventory id (ADR 0002), which names the script in the
	// bundle. The skeleton names a script by its job path (WR-29), which
	// two distinct scripts can share: an inline step `a` of flow f/x and a
	// path script f/x/a.
	scripts map[conformance.SitePath]string
	// reached are the scripts the flow reaches, and names how a problem
	// names each, by inventory id.
	reached []*ir.Script
	names   map[string]string
	// unsupported or problem, when set, is why the flow's runs are not
	// checked, or fail.
	unsupported, problem string
}

// unsupportedKinds are the compile errors of features windmill-lite does
// not run (ADR 0001 §3, §5a): a run that meets one is unsupported, not
// failed.
var unsupportedKinds = map[ir.ErrorKind]bool{ir.ErrUnsupported: true, ir.ErrLanguage: true, ir.ErrPinnedHash: true}

// build compiles each run's target flow and builds the Bun scripts they
// reach into one bundle below work: scripts/bun/<id>.mjs and
// <id>.meta.json, named by each script's inventory id (ir.Script.ID), as a
// bundle names them (ADR 0010). Codebase scripts are built with esbuild
// (bundle.BuildCodebase, milestone C2b), each with the settings of the
// codebases entry that selects it: scripts/bun/<id>.cjs (or .mjs) and
// <id>.meta.json, with their assets.
// Its manifest lists the host-provided modules, none, which is all the
// harness reads of it besides its digest; the bundle format is F4's.
//
// What goes wrong with one target (a compile error, a refused script, a
// build problem of a script it reaches) is that target's; only a folder
// that does not load, or a build that fails as a whole, fails the fixture.
func (fc *flowChecker) build(ctx context.Context, f *conformance.Fixture, work string) (*fixtureBundle, error) {
	root, err := syncfolder.Find(f.SyncTree())
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

	b := &fixtureBundle{out: filepath.Join(work, "bundle"), targets: map[string]*target{}}
	entries := map[string]bundle.BunScript{}
	codebase := map[string]bundle.CodebaseScript{}
	users := map[string][]string{} // inventory id → the targets that reach it
	for _, run := range f.Runs {
		if run.IsScript() || b.targets[run.Target] != nil {
			continue
		}
		t := &target{scripts: map[conformance.SitePath]string{}}
		b.targets[run.Target] = t
		b.order = append(b.order, run.Target)
		if err := fc.compile(folder, r, run.Target, t); err != nil {
			t.problem = err.Error()
			continue
		}
		if t.unsupported != "" || t.problem != "" {
			continue
		}
		for _, s := range t.reached {
			if s.Bundling == ir.BundlingCodebase {
				// One inventory id is one script, and its file one entry.
				if _, ok := codebase[s.ID]; !ok {
					codebase[s.ID] = bundle.CodebaseScript{ID: s.ID, Path: s.Path, File: s.File,
						Settings: folder.Config.Codebases[s.Codebase.Index].Settings}
				}
				users[s.ID] = append(users[s.ID], run.Target)
				continue
			}
			e := bundle.BunScript{ID: s.ID, Path: s.ImportBase, Lock: lockText(s.Lock)}
			if s.Kind == ir.PathScript {
				e.File = s.File
			} else {
				e.Content = s.Content
			}
			if prev, ok := entries[s.ID]; ok && prev != e {
				// One inventory id is one script in every compile of the
				// folder; two readings of it are the compiler's fault.
				t.problem = "build: script " + s.ID + " compiled to two different entries"
				break
			}
			entries[s.ID] = e
			users[s.ID] = append(users[s.ID], run.Target)
		}
	}

	cfg := bundle.BunConfig{
		Bun:     fc.bun,
		Helper:  filepath.Join(fc.repo, "harnesses", "bun", "build", "main.ts"),
		Folder:  root,
		Out:     b.out,
		Work:    filepath.Join(work, "build"),
		Scripts: folderScripts(folder),
	}
	for _, p := range slices.Sorted(maps.Keys(entries)) {
		cfg.Entries = append(cfg.Entries, entries[p])
	}
	if err := os.MkdirAll(b.out, 0o755); err != nil {
		return nil, err
	}
	// A problem of one script is the problem of the targets that reach it.
	attribute := func(err error) error {
		var be *bundle.BuildError
		switch {
		case errors.As(err, &be):
			for _, p := range be.Problems {
				id, isScript := strings.CutPrefix(p.Item, "script ")
				if !isScript {
					return err
				}
				for _, tp := range users[id] {
					if t := b.targets[tp]; t.problem == "" {
						t.problem = "build: script " + t.names[id] + ": " + p.Kind + ": " + p.Message
					}
				}
			}
		case err != nil:
			return err
		}
		return nil
	}
	if len(cfg.Entries) > 0 {
		if _, err := bundle.BuildBun(ctx, cfg); attribute(err) != nil {
			return nil, err
		}
	}
	if len(codebase) > 0 {
		cc := bundle.CodebaseConfig{Bun: cfg.Bun, Helper: cfg.Helper, Folder: root, Out: b.out, Work: filepath.Join(work, "codebase")}
		for _, id := range slices.Sorted(maps.Keys(codebase)) {
			cc.Entries = append(cc.Entries, codebase[id])
		}
		if _, err := bundle.BuildCodebase(ctx, cc); attribute(err) != nil {
			return nil, err
		}
	}
	if b.digest, err = writeManifest(b.out, "windmill-lite-flow-fixture", "Built by wltest check for one flow fixture; the bundle format is milestone F4's.", nil); err != nil {
		return nil, err
	}
	return b, nil
}

// compile compiles one target flow, with it as the only root, into t: its
// module ids, the inventory id of each leaf's script, the scripts it
// reaches and the flow document the skeleton runs. A feature the compiler
// or this runner refuses makes t unsupported; a compile error is t's
// problem. The error it returns is the target's problem too: its flow
// document did not read after it compiled.
func (fc *flowChecker) compile(folder *syncfolder.Folder, r *os.Root, path string, t *target) error {
	contract, err := json.Marshal(ir.Contract{Roots: []ir.Root{{Flow: path}}})
	if err != nil {
		return err
	}
	c, err := ir.ParseContract(contract)
	if err != nil {
		return err
	}
	prog, err := ir.Compile(folder, c, ir.Options{})
	if err != nil {
		var ie *ir.Error
		if errors.As(err, &ie) && unsupportedKinds[ie.Kind] {
			t.unsupported = "compile: " + err.Error()
		} else {
			t.problem = "compile: " + err.Error()
		}
		return nil
	}
	fl := prog.Flows[path]
	if fl == nil {
		t.problem = "compile: no flow " + path
		return nil
	}
	segs := make([]string, len(fl.Modules))
	for i, m := range fl.Modules {
		segs[i] = m.ID
	}
	t.modules = segs
	for i, seg := range conformance.ModuleSegments(segs) {
		if l := fl.Modules[i].Leaf; l != nil {
			t.scripts[conformance.SitePath{}.Child(seg)] = l.Script
		}
	}
	t.names = map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(prog.Scripts)) {
		s := prog.Scripts[id]
		if s.Harness != ir.ClassBun || s.Boundary != ir.BoundaryBun {
			t.unsupported = fmt.Sprintf("script %s runs in the %s boundary of the %s harness, which is milestone C3n's", s.Path, s.Boundary, s.Harness)
			return nil
		}
		t.reached = append(t.reached, s)
		t.names[id] = s.Path
		if s.Kind != ir.PathScript {
			t.names[id] = fmt.Sprintf("%s (inline step %s of %s)", id, s.Module, s.Flow)
		}
	}
	doc, err := folder.FlowDocument(path)
	if err != nil {
		return fmt.Errorf("flow document: %w", err)
	}
	data, err := r.ReadFile(doc.File)
	if err != nil {
		return fmt.Errorf("flow document: %w", err)
	}
	if t.flowJSON, err = yamlx.ToJSON(data); err != nil {
		return fmt.Errorf("flow document: %w", err)
	}
	return nil
}

// folderScripts maps every TypeScript script of the folder to its file: the
// scripts that workspace imports resolve to (WR-28).
func folderScripts(f *syncfolder.Folder) map[string]string {
	files := map[string]string{}
	for _, p := range f.Paths(syncfolder.KindScript) {
		if s, err := f.Script(p); err == nil && strings.HasSuffix(s.File, ".ts") {
			files[p] = s.File
		}
	}
	return files
}

func lockText(l *string) string {
	if l == nil {
		return ""
	}
	return *l
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

// runOne runs one run on the skeleton and compares its observation.
func (fc *flowChecker) runOne(ctx context.Context, f *conformance.Fixture, r conformance.Run, s *engine.Skeleton, pool *harness.Pool, t *target) []string {
	rctx, cancel := context.WithTimeout(ctx, flowRunTimeout)
	defer cancel()
	if r.CancelAfterMS != nil {
		stop := time.AfterFunc(time.Duration(*r.CancelAfterMS)*time.Millisecond, cancel)
		defer stop.Stop()
	}
	exec := &jobRecorder{exec: pool, scripts: t.scripts, jobs: map[string]conformance.SitePath{}}
	tr, err := s.Run(rctx, engine.SkeletonRun{
		ID:   newUUID(),
		Path: r.Target,
		Args: jsonval.FromProducer(r.ArgsOrEmpty()),
		Exec: exec,
	})
	if err != nil {
		return []string{"run: " + err.Error()}
	}
	if r.CancelAfterMS == nil && errors.Is(context.Cause(rctx), context.DeadlineExceeded) {
		return []string{fmt.Sprintf("the run did not end within %s", flowRunTimeout)}
	}
	got, generated, err := observeTrace(tr, t.modules, exec.ids(), f.Compare)
	if err != nil {
		return []string{"observe: " + err.Error()}
	}
	return compareRun(f, r, got, generated)
}

// compareRun compares a run's observation with its expect, or with its
// recording when it has none (ADR 0011), and checks its claims. A
// recording holds neither carried nor resolved_args, so they are compared
// only with an expect. On the reference's side, the location suffix of an
// internal error is stripped where windmill-lite generated the value
// (ADR 0011, Location suffixes).
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

// jobRecorder is the executor of one run. It records the site of every
// invocation the run makes, by its id, which the normaliser replaces with
// the site's placeholder (OR-14); it names the invocation's script by the
// inventory id of the site's script, as the bundle does, where the
// skeleton names it by its job path, which stays the invocation's
// WM_JOB_PATH; and it passes the invocation on.
type jobRecorder struct {
	exec    engine.StepExecutor
	scripts map[conformance.SitePath]string
	mu      sync.Mutex
	jobs    map[string]conformance.SitePath
}

func (j *jobRecorder) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	id, ok := j.scripts[inv.Site]
	if !ok {
		inv.Report.Settled(engine.SettleNotStarted)
		return engine.StepOutcome{}, fmt.Errorf("wltest: the compiled flow has no script at site %s", inv.Site)
	}
	inv.Script = id
	j.mu.Lock()
	j.jobs[inv.ID] = inv.Site
	j.mu.Unlock()
	return j.exec.Execute(ctx, inv)
}

func (j *jobRecorder) ids() map[string]conformance.SitePath {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make(map[string]conformance.SitePath, len(j.jobs))
	for id, site := range j.jobs {
		out[id] = site
	}
	return out
}

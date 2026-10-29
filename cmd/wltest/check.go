package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/platform"
	"github.com/invakid404/windmill-lite/internal/platform/facade"
)

// check runs step-case files (the step level of ADR 0003) and flow
// fixtures (the flow level) on windmill-lite. For a step file, the file's
// check runs step-case files (the step level of ADR 0003) and flow
// fixtures (the flow level) on windmill-lite. For a step file, the file's
// scripts are built into one bundle, by the Bun build helper or, for
// codebase scripts, by esbuild (bundle.BuildCodebase), each case's runs go
// in order through the protocol, to one harness process of the case's own
// in warm mode, or to one process each in fresh mode, and each outcome is
// mapped to the value Windmill stores (engine.StoreOutcome) and compared
// with the case's expectation (conformance.CompareStep). With Node, each
// run of a CommonJS codebase bundle also runs on Node, as on Windmill, and
// is compared with its node check (D13). For a flow fixture, each run's
// flow runs on the engine (flowChecker) and its observation is compared
// with the run's expect or recording (milestones E1 and N1a). An
// expression-case file runs as the generated one-step flow fixture that
// wltest record lifts it into, compared with its recordings (milestone
// N1a).
func check(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", conformance.ModeWarm, "configuration: warm or fresh (host mode is milestone F3b)")
	bun := fs.String("bun", envOr("WL_BUN", "bun"), "the Bun executable: the pin, "+harness.PinnedBun)
	node := fs.String("node", os.Getenv("WL_NODE"), "the Node executable that runs CommonJS codebase bundles as Windmill does (D13); empty skips that side")
	repo := fs.String("repo", "", "the repository root, which holds harnesses/bun (default: found from the working directory)")
	verbose := fs.Bool("v", false, "print the harness diagnostics of every failed case or run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: wltest check [flags] <step file, fixture, expression file or directory>...")
		return 2
	}
	if *mode != conformance.ModeWarm && *mode != conformance.ModeFresh {
		fmt.Fprintf(stderr, "wltest check: mode %q is not warm or fresh (host mode is milestone F3b)\n", *mode)
		return 2
	}
	for _, a := range fs.Args() {
		if info, err := os.Stat(a); err == nil && !info.IsDir() && !conformance.IsStepFile(a) && !conformance.IsExprFile(a) {
			fmt.Fprintf(stderr, "wltest check: %s is neither a step file (steps/<lang>/<finding>.yaml) nor an expression file (exprs/<finding>.yaml); a flow fixture is named by its directory\n", a)
			return 2
		}
	}
	files, err := conformance.FindStepFiles(fs.Args()...)
	if err != nil {
		fmt.Fprintln(stderr, "wltest check:", err)
		return 1
	}
	dirs, err := findFixtures(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, "wltest check:", err)
		return 1
	}
	exprs, err := conformance.FindExprFiles(fs.Args()...)
	if err != nil {
		fmt.Fprintln(stderr, "wltest check:", err)
		return 1
	}
	dirs = append(dirs, exprs...)
	if len(files)+len(dirs) == 0 {
		fmt.Fprintln(stderr, "wltest check: no step files, flow fixtures or expression files found")
		return 2
	}
	root := *repo
	if root == "" {
		if root, err = repoRoot(); err != nil {
			fmt.Fprintln(stderr, "wltest check:", err)
			return 2
		}
	}
	failed, passed, unsupported := 0, 0, 0
	sc := &stepChecker{bun: *bun, node: *node, repo: root, mode: *mode}
	for _, path := range files {
		f, err := conformance.LoadStepFile(path)
		if err != nil {
			fmt.Fprintln(stdout, "invalid", path, err)
			failed++
			continue
		}
		for _, r := range sc.runFile(ctx, f) {
			if len(r.problems) == 0 {
				passed++
				fmt.Fprintf(stdout, "pass %s %s\n", r.name, path)
				continue
			}
			failed++
			fmt.Fprintf(stdout, "fail %s %s\n", r.name, path)
			for _, p := range r.problems {
				fmt.Fprintf(stdout, "  %s\n", p)
			}
			if *verbose && r.diag != "" {
				fmt.Fprintf(stdout, "  harness diagnostics:\n%s", indent(r.diag, "    "))
			}
		}
	}
	if len(dirs) > 0 {
		js, err := quickjs.New(quickjs.Options{})
		if err != nil {
			fmt.Fprintln(stderr, "wltest check:", err)
			return 1
		}
		defer js.Close()
		fc := &flowChecker{bun: *bun, repo: root, mode: *mode, js: js}
		for _, fr := range fc.runFixtures(ctx, dirs) {
			if fr.err != nil {
				fmt.Fprintln(stdout, "invalid", fr.dir, fr.err)
				failed++
				continue
			}
			for _, r := range fr.runs {
				switch {
				case r.unsupported != "":
					unsupported++
					fmt.Fprintf(stdout, "unsupported %s %s: %s\n", r.fixture, r.run, r.unsupported)
					continue
				case len(r.problems) == 0:
					passed++
					fmt.Fprintf(stdout, "pass %s %s\n", r.fixture, r.run)
				default:
					failed++
					fmt.Fprintf(stdout, "fail %s %s\n", r.fixture, r.run)
					for _, p := range r.problems {
						fmt.Fprintf(stdout, "  %s\n", p)
					}
				}
				if *verbose && len(r.problems) > 0 && r.diag != "" {
					fmt.Fprintf(stdout, "  harness diagnostics:\n%s", indent(r.diag, "    "))
				}
			}
		}
	}
	fmt.Fprintf(stdout, "%d passed, %d failed, %d unsupported (mode %s)\n", passed, failed, unsupported, *mode)
	if failed > 0 {
		return 1
	}
	return 0
}

// findFixtures returns the flow fixtures at or below the arguments, each
// once, sorted.
func findFixtures(args []string) ([]string, error) {
	var dirs []string
	for _, a := range args {
		if info, err := os.Stat(a); err == nil && !info.IsDir() {
			continue
		}
		found, err := conformance.FindFixtures(a)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, found...)
	}
	for i := range dirs {
		dirs[i] = filepath.Clean(dirs[i])
	}
	slices.Sort(dirs)
	return slices.Compact(dirs), nil
}

// stepChecker runs step files in one configuration. node, when set, also
// runs CommonJS codebase bundles on Node (D13).
type stepChecker struct {
	bun, node, repo, mode string
}

// caseResult is how one case went: what failed, if anything, and the
// harness's diagnostics.
type caseResult struct {
	name     string
	problems []string
	diag     string
}

// runTimeout bounds each run's invocation; settleTimeout the wait for its
// settlement, through which its warnings and late reports are observed;
// pendingWindow how long a call that must not settle is observed.
const (
	runTimeout    = 30 * time.Second
	settleTimeout = 10 * time.Second
	pendingWindow = 500 * time.Millisecond
	// lateWindow is how long a warm case's process is still observed after
	// its last run settled, when that run states its late reports: a later
	// run of the case observes the earlier ones' work, and nothing follows
	// the last.
	lateWindow = 200 * time.Millisecond
)

// language returns the language a deploy records for a case's script, and
// the boundary it runs with: bun, or native for bunnative and nativets
// (ADR 0002, WR-40).
func language(c *conformance.StepCase) (openflow.Language, engine.Boundary) {
	lang := ir.DeployedLanguage(openflow.Language(cmp(c.Language, conformance.LangBun)), c.Script)
	if b, _ := ir.BoundaryOf(lang); b == ir.BoundaryNative {
		return lang, engine.BoundaryNative
	}
	return lang, engine.BoundaryBun
}

// runFile builds f's scripts into one bundle and runs every case, a few at
// a time.
func (sc *stepChecker) runFile(ctx context.Context, f *conformance.StepFile) []caseResult {
	results := make([]caseResult, len(f.Cases))
	for i := range f.Cases {
		results[i].name = f.Cases[i].Name()
	}
	work, err := os.MkdirTemp("", "wltest-steps-")
	if err != nil {
		return failAll(results, err.Error())
	}
	defer os.RemoveAll(work)
	out, digest, problems, err := sc.build(ctx, f, work)
	if err != nil {
		return failAll(results, "build: "+err.Error())
	}
	var wrapper string
	if sc.node != "" && hasCommonJS(f) {
		if wrapper, err = sc.nodeWrapper(ctx, work); err != nil {
			return failAll(results, "node: "+err.Error())
		}
	}
	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/2))
	var wg sync.WaitGroup
	for i := range f.Cases {
		c := &f.Cases[i]
		if p, ok := problems[caseID(f, c)]; ok {
			results[i].problems = []string{"build: " + p}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i].problems, results[i].diag = sc.runCase(ctx, c, out, digest, caseID(f, c))
			if wrapper != "" && onNode(c) {
				results[i].problems = append(results[i].problems, sc.runNodeCase(ctx, c, out, caseID(f, c), wrapper, work)...)
			}
		}()
	}
	wg.Wait()
	return results
}

func failAll(rs []caseResult, problem string) []caseResult {
	for i := range rs {
		rs[i].problems = []string{problem}
	}
	return rs
}

// caseID is the inventory id of a case's script in its file's bundle.
func caseID(f *conformance.StepFile, c *conformance.StepCase) string {
	return strings.ToLower(f.Finding) + "/" + c.Name()
}

// build writes f's scripts into a sync folder below work and builds them
// with the Bun build helper into a bundle: scripts/bun/<id>.mjs and
// <id>.meta.json for each case, and its host-provided modules. Its
// manifest lists those modules in registration order, which is all the
// harness reads of it besides its digest; the bundle format is F4's.
// problems holds the build problem of each case's script, by id.
func (sc *stepChecker) build(ctx context.Context, f *conformance.StepFile, work string) (out, digest string, problems map[string]string, err error) {
	folder, out := filepath.Join(work, "folder"), filepath.Join(work, "bundle")
	scripts := map[string]string{}
	write := func(path, src string) error {
		file := path + ".ts"
		scripts[path] = file
		p := filepath.Join(folder, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(src), 0o644)
	}
	cfg := bundle.BunConfig{
		Bun:    sc.bun,
		Helper: filepath.Join(sc.repo, "harnesses", "bun", "build", "main.ts"),
		Folder: folder,
		Out:    out,
		Work:   filepath.Join(work, "build"),
	}
	hosts := map[string]bool{}
	var codebase []bundle.CodebaseScript
	for i := range f.Cases {
		c := &f.Cases[i]
		if err := write(c.ScriptPath(), c.Script); err != nil {
			return "", "", nil, err
		}
		for p, text := range c.Files {
			full := filepath.Join(folder, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return "", "", nil, err
			}
			if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
				return "", "", nil, err
			}
		}
		if c.Codebase != nil {
			codebase = append(codebase, bundle.CodebaseScript{ID: caseID(f, c), Path: c.ScriptPath(), File: c.ScriptPath() + ".ts", Settings: c.Codebase})
		} else {
			lang, _ := language(c)
			cfg.Entries = append(cfg.Entries, bundle.BunScript{ID: caseID(f, c), Path: c.ScriptPath(), File: c.ScriptPath() + ".ts", Language: string(lang)})
		}
		for _, h := range c.HostModules {
			if hosts[h.Path] {
				continue
			}
			hosts[h.Path] = true
			if err := write(h.Path, h.Script); err != nil {
				return "", "", nil, err
			}
			cfg.HostModules = append(cfg.HostModules, bundle.HostModule{ID: h.Path, Key: h.Path + ".ts", Path: h.Path, File: h.Path + ".ts"})
		}
	}
	cfg.Scripts = scripts
	res, err := bundle.BuildBun(ctx, cfg)
	problems = map[string]string{}
	collect := func(err error) error {
		var be *bundle.BuildError
		switch {
		case errors.As(err, &be):
			for _, p := range be.Problems {
				id, isScript := strings.CutPrefix(p.Item, "script ")
				if !isScript {
					return err
				}
				problems[id] = p.Kind + ": " + p.Message
			}
		case err != nil:
			return err
		}
		return nil
	}
	if err := collect(err); err != nil {
		return "", "", nil, err
	}
	// Codebase scripts, with esbuild, once the scan has written the shims
	// of the host-provided modules.
	if len(codebase) > 0 {
		var chosts []bundle.CodebaseHostModule
		for _, h := range cfg.HostModules {
			chosts = append(chosts, bundle.CodebaseHostModule{Key: h.Key, File: h.File,
				Shim: filepath.Join(out, "scripts", "bun", "provided", "shims", filepath.FromSlash(h.ID)+".mjs")})
		}
		_, err := bundle.BuildCodebase(ctx, bundle.CodebaseConfig{
			Bun: cfg.Bun, Helper: cfg.Helper, Folder: folder, Out: out, Work: filepath.Join(work, "codebase"),
			Entries: codebase, HostModules: chosts,
		})
		if err := collect(err); err != nil {
			return "", "", nil, err
		}
	}
	var registered []hostModuleEntry
	for _, id := range res.HostModuleOrder {
		registered = append(registered, hostModuleEntry{Key: res.HostModules[id].Key, ID: id})
	}
	digest, err = writeManifest(out, "windmill-lite-step-cases", "Built by wltest check for one step file; the bundle format is milestone F4's.", registered)
	if err != nil {
		return "", "", nil, err
	}
	return out, digest, problems, nil
}

// runCase runs a case's runs in order. In warm mode they go to one
// harness process of the case's own, so the module and globalThis survive
// from one run to the next (D1); in fresh mode each run has a process of
// its own, which ends with it. The late reports of a run are compared at
// the end of the case, so that one its work makes after it settled, which
// the D9 fence must stop, is seen.
func (sc *stepChecker) runCase(ctx context.Context, c *conformance.StepCase, out, digest, id string) ([]string, string) {
	var diag syncBuffer
	// The case's own platform facade (ADR 0007, milestone PF1), in its
	// default fail mode: a warm process reaches it through the intercepted
	// fetch, at an origin where nothing listens; a fresh one over TCP.
	calls := &callLog{byInv: map[string][]facade.Record{}}
	f, err := facade.New(facade.Config{Recorded: calls.store})
	if err != nil {
		return []string{"facade: " + err.Error()}, ""
	}
	defer f.Close()
	spawn := func() (*harness.Process, error) { return sc.spawn(ctx, out, digest, &diag, f) }
	// end drains a warm process; a fresh one ends by itself, and is killed
	// if it has not within the grace.
	end := func(p *harness.Process) {
		if sc.mode == conformance.ModeFresh {
			select {
			case <-p.Done():
			case <-time.After(5 * time.Second):
				p.Kill()
				<-p.Done()
			}
			return
		}
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Drain(dctx)
	}
	var warm *harness.Process
	if sc.mode == conformance.ModeWarm {
		var err error
		if warm, err = spawn(); err != nil {
			return []string{"spawn: " + err.Error()}, diag.String()
		}
	}
	runID := newUUID()
	var problems []string
	recs := make([]*stepRecorder, len(c.Runs))
	for i := range c.Runs {
		p := warm
		if p == nil {
			var err error
			if p, err = spawn(); err != nil {
				problems = append(problems, fmt.Sprintf("run %d: spawn: %s", i+1, err))
				continue
			}
		}
		var prs []string
		prs, recs[i] = sc.runOne(ctx, f.Executor(p), calls, c, &c.Runs[i], id, runID)
		if warm == nil {
			end(p)
		}
		for _, pr := range prs {
			problems = append(problems, fmt.Sprintf("run %d: %s", i+1, pr))
		}
	}
	if warm != nil {
		if k := c.Runs[len(c.Runs)-1].Check(sc.mode); (k.Settled != "" && k.Settled != conformance.StepPending) || len(k.Late) > 0 {
			time.Sleep(lateWindow)
		}
		end(warm)
	}
	for i := range c.Runs {
		k := c.Runs[i].Check(sc.mode)
		if recs[i] == nil || (k.Settled == "" && len(k.Late) == 0) {
			continue
		}
		for _, pr := range compareLate(recs[i].lates(), k.Late) {
			problems = append(problems, fmt.Sprintf("run %d: %s", i+1, pr))
		}
	}
	return problems, diag.String()
}

// spawn starts a harness process on the bundle in out, in the checker's
// module state, with f's origin for that mode (milestone PF1): a warm
// process intercepts its fetches to an origin where nothing listens, and a
// fresh one reaches the listening origin over TCP. A native step reads the
// same origin as BASE_URL and BASE_INTERNAL_URL (WR-45).
func (sc *stepChecker) spawn(ctx context.Context, out, digest string, diag io.Writer, f *facade.Facade) (*harness.Process, error) {
	origin := f.WarmOrigin()
	if sc.mode == conformance.ModeFresh {
		origin = f.FreshOrigin()
	}
	path, args := harness.BunCommand(sc.bun, filepath.Join(sc.repo, "harnesses", "bun", "runtime", "main.ts"), out)
	return harness.Spawn(ctx, harness.ProcessConfig{
		Path:          path,
		Args:          args,
		Kind:          "bun",
		Runtime:       harness.PinnedBun,
		Bundle:        digest,
		ModuleState:   engine.ModuleState(sc.mode),
		EngineVersion: "wltest-check",
		Diagnostics:   diag,
		Facade:        origin,
		Platform:      f.Platform(),
	})
}

// runOne runs one invocation and checks what it produced up to its
// settlement; it returns the recorder, which keeps receiving the run's
// late reports.
func (sc *stepChecker) runOne(ctx context.Context, p engine.StepExecutor, calls *callLog, c *conformance.StepCase, r *conformance.StepRun, script, runID string) ([]string, *stepRecorder) {
	rec := newStepRecorder()
	args := jsonval.FromProducer([]byte(r.Args))
	entry, _, err := engine.Entrypoint(args)
	if err != nil {
		return []string{err.Error()}, rec
	}
	step := c.ModuleID()
	_, boundary := language(c)
	invID := newUUID()
	inv := engine.Invocation{
		ID:          invID,
		Run:         runID,
		Site:        conformance.SitePath{}.Child(conformance.Module(cmp(step, conformance.StepModule))),
		Script:      script,
		Entry:       entry,
		Args:        args,
		Env:         jobEnv(c, step, invID, runID),
		Boundary:    boundary,
		ModuleState: engine.ModuleState(sc.mode),
	}
	inv.Report = rec
	rctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	// The step's own timeout is the engine's (FX-24): it abandons the
	// invocation (D2), and the step fails with the timeout's text.
	var timeout time.Duration
	if c.Timeout != nil {
		timeout = time.Duration(*c.Timeout) * time.Second
		var stop context.CancelFunc
		rctx, stop = context.WithTimeoutCause(rctx, timeout, engine.CancelTimeout)
		defer stop()
	}
	out, err := p.Execute(rctx, inv)
	var stored engine.Stored
	switch {
	case c.Timeout != nil && errors.Is(err, engine.CancelTimeout):
		stored = engine.Stored{Failed: true, Value: engine.TimeoutError(boundary, step, timeout, false), Origin: engine.OriginEngine}
	case err != nil:
		return []string{"execute: " + err.Error()}, rec
	default:
		stored = engine.StoreOutcome(out, step)
	}
	text, err := stored.Value.Stored()
	if err != nil {
		return []string{"the stored value: " + err.Error()}, rec
	}
	status := conformance.RunSuccess
	if stored.Failed {
		status = conformance.RunFailure
	}
	got := conformance.Observation{Status: status, Result: json.RawMessage(text)}
	problems, err := conformance.CompareStep(c, r, sc.mode, got)
	if err != nil {
		return []string{"compare: " + err.Error()}, rec
	}
	if len(problems) > 0 {
		problems = append(problems, "observed "+compactJSON(got))
	}
	// The run is observed up to its settlement: an outcome fixed early (an
	// intercepted process.exit, D11) comes before the call ends, and its
	// warnings until then belong to it (ADR 0004).
	k := r.Check(sc.mode)
	if k.Settled == conformance.StepPending {
		if kind, ok := rec.waitSettled(pendingWindow); ok {
			problems = append(problems, fmt.Sprintf("settled %s within %s, want pending", kind, pendingWindow))
		}
	} else {
		kind, ok := rec.waitSettled(settleTimeout)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("not settled within %s; a call that must not settle says settled: pending", settleTimeout))
		case k.Settled != "" && string(kind) != k.Settled:
			problems = append(problems, fmt.Sprintf("settled %s, want %s", kind, k.Settled))
		}
	}
	if got, want := rec.warningCodes(), k.Warnings; strings.Join(got, ",") != strings.Join(want, ",") {
		problems = append(problems, fmt.Sprintf("warnings %v, want %v", got, want))
	}
	if k.Left != nil {
		if got := rec.leftBehind(); !maps.Equal(got, k.Left) {
			problems = append(problems, fmt.Sprintf("left behind %v, want %v", got, k.Left))
		}
	}
	if k.Untracked != nil {
		if got := rec.nativeUntracked(); !maps.Equal(got, k.Untracked) {
			problems = append(problems, fmt.Sprintf("untracked %v, want %v", got, k.Untracked))
		}
	}
	if k.Calls != nil {
		// The facade hands over the record once the invocation has settled
		// and its outcome is decided, both of which have happened here.
		if got := calls.describe(invID); !slices.Equal(got, k.Calls) {
			problems = append(problems, fmt.Sprintf("platform calls %q, want %q", got, k.Calls))
		}
	}
	return problems, rec
}

// callLog keeps the platform calls the facade recorded, by invocation.
type callLog struct {
	mu    sync.Mutex
	byInv map[string][]facade.Record
}

func (l *callLog) store(inv string, calls []facade.Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byInv[inv] = calls
}

// describe gives an invocation's calls as a step case states them:
// "<status> <METHOD> <route template>", or the path as sent where no
// route of the API matched.
func (l *callLog) describe(inv string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []string{}
	for _, r := range l.byInv[inv] {
		name := r.Route
		if name == "" {
			name = r.Method + " " + strings.TrimPrefix(r.Path, "/api")
		}
		out = append(out, fmt.Sprintf("%d %s", r.Status, name))
	}
	return out
}

// hasCommonJS reports whether a step file has a case that also runs on
// Node (onNode).
func hasCommonJS(f *conformance.StepFile) bool {
	for i := range f.Cases {
		if onNode(&f.Cases[i]) {
			return true
		}
	}
	return false
}

// onNode reports whether a case also runs on Node, where Windmill runs it
// (D13): a CommonJS codebase script, unless its boundary is native, which
// Windmill gives to the native runtime whatever its bundle's format
// (WR-50/c).
func onNode(c *conformance.StepCase) bool {
	_, b := language(c)
	return c.CommonJS() && b == engine.BoundaryBun
}

// nodeDriver runs one invocation of a CommonJS codebase bundle on Node,
// laid out as Windmill's Node path lays out a job (noderun.mjs).
//
//go:embed noderun.mjs
var nodeDriver []byte

// nodeWrapper writes the harness's wrapper rules
// (harnesses/bun/runtime/wrapper.ts) as a module Node runs, transpiled by
// the pinned Bun, and returns its path.
func (sc *stepChecker) nodeWrapper(ctx context.Context, work string) (string, error) {
	out := filepath.Join(work, "wl-wrapper.mjs")
	cmd := exec.CommandContext(ctx, sc.bun, "build", filepath.Join(sc.repo, "harnesses", "bun", "runtime", "wrapper.ts"),
		"--target=node", "--format=esm", "--outfile="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("transpiling the wrapper rules: %v: %s", err, b)
	}
	return out, nil
}

// runNodeCase runs each run of a CommonJS codebase case on Node, in a job
// directory of its own as Windmill runs each job (WR-1, WR-50), and
// compares what it shows with the run's node check, or its own check
// when the two runtimes agree (D13).
func (sc *stepChecker) runNodeCase(ctx context.Context, c *conformance.StepCase, out, id, wrapper, work string) []string {
	var problems []string
	runID := newUUID()
	for i := range c.Runs {
		r := &c.Runs[i]
		got, err := sc.runNode(ctx, c, r, out, id, wrapper, work, runID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("run %d on node: %v", i+1, err))
			continue
		}
		diffs, err := conformance.CompareStepNode(c, r, sc.mode, got)
		if err != nil {
			problems = append(problems, fmt.Sprintf("run %d on node: compare: %v", i+1, err))
			continue
		}
		for _, d := range diffs {
			problems = append(problems, fmt.Sprintf("run %d on node: %s", i+1, d))
		}
		if len(diffs) > 0 {
			problems = append(problems, fmt.Sprintf("run %d on node: observed %s", i+1, compactJSON(got)))
		}
	}
	return problems
}

// nodeOutcome is noderun.mjs's outcome.json: the outcome as a result frame
// carries it.
type nodeOutcome struct {
	Outcome  string          `json:"outcome"`
	Body     json.RawMessage `json:"body"`
	Tail     string          `json:"tail"`
	ExitCode int             `json:"exit_code"`
}

func (sc *stepChecker) runNode(ctx context.Context, c *conformance.StepCase, r *conformance.StepRun, out, id, wrapper, work, runID string) (conformance.Observation, error) {
	job, err := os.MkdirTemp(work, "node-job-")
	if err != nil {
		return conformance.Observation{}, err
	}
	defer os.RemoveAll(job)
	// The job directory in Windmill's order (WR-50): the placeholder
	// package.json first, then the bundle as main.js and the entry's
	// assets at their paths, as a tar unpacks, so an asset named
	// package.json replaces the placeholder and its type decides how Node
	// loads main.js (c2b review 1, finding 3). The runner's own files come
	// last.
	scripts := filepath.Join(out, "scripts", "bun", filepath.FromSlash(id))
	if err := writeJobFile(job, "package.json", []byte("{}\n")); err != nil {
		return conformance.Observation{}, err
	}
	type jobFile struct{ to, from string }
	files := []jobFile{{"main.js", scripts + ".cjs"}}
	var settings struct {
		Assets []bundle.Asset `json:"assets"`
	}
	json.Unmarshal(c.Codebase, &settings)
	for _, a := range settings.Assets {
		files = append(files, jobFile{a.To, filepath.Join(out, "assets", filepath.FromSlash(a.To))})
	}
	files = append(files, jobFile{"meta.json", scripts + ".meta.json"}, jobFile{"wl-wrapper.mjs", wrapper})
	for _, f := range files {
		b, err := os.ReadFile(f.from)
		if err != nil {
			return conformance.Observation{}, err
		}
		if err := writeJobFile(job, f.to, b); err != nil {
			return conformance.Observation{}, err
		}
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{"noderun.mjs", nodeDriver}, {"args.json", []byte(r.Args)}} {
		if err := writeJobFile(job, f.name, f.data); err != nil {
			return conformance.Observation{}, err
		}
	}
	args := jsonval.FromProducer([]byte(r.Args))
	entry, _, err := engine.Entrypoint(args)
	if err != nil {
		return conformance.Observation{}, err
	}
	step := c.ModuleID()
	env := []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range jobEnv(c, step, newUUID(), runID) {
		env = append(env, k+"="+v)
	}
	rctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, sc.node, "--preserve-symlinks", "noderun.mjs", entry)
	cmd.Dir, cmd.Env = job, env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	var o engine.StepOutcome
	data, readErr := os.ReadFile(filepath.Join(job, "outcome.json"))
	switch {
	case readErr == nil:
		var n nodeOutcome
		if err := json.Unmarshal(data, &n); err != nil {
			return conformance.Observation{}, fmt.Errorf("outcome.json: %v", err)
		}
		o = engine.StepOutcome{Kind: engine.OutcomeKind(n.Outcome), Tail: n.Tail, ExitCode: n.ExitCode}
		if n.Body != nil {
			var text string
			if err := json.Unmarshal(n.Body, &text); err != nil {
				return conformance.Observation{}, fmt.Errorf("outcome.json body: %v", err)
			}
			o.Body = jsonval.FromProducer([]byte(text))
		}
	case runErr == nil:
		// The process exited with 0 before the result was written: an
		// intercepted process.exit(0) on the harness (WR-V2).
		o = engine.StepOutcome{Kind: engine.OutcomeEmpty}
	default:
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return conformance.Observation{}, runErr
		}
		tail := stderr.String()
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		o = engine.StepOutcome{Kind: engine.OutcomeEscaped, Tail: tail, ExitCode: ee.ExitCode()}
	}
	stored := engine.StoreOutcome(o, step)
	text, err := stored.Value.Stored()
	if err != nil {
		return conformance.Observation{}, fmt.Errorf("the stored value: %v", err)
	}
	status := conformance.RunSuccess
	if stored.Failed {
		status = conformance.RunFailure
	}
	return conformance.Observation{Status: status, Result: json.RawMessage(text)}, nil
}

func writeJobFile(job, name string, data []byte) error {
	p := filepath.Join(job, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// jobEnv is the invocation's job variables (WR-22, ADR 0007's job
// identity): those of a flow step, of the flow StepFolder/flow, or of a
// script job when the case runs outside a flow, whose flow variables are
// empty; then the case's own.
func jobEnv(c *conformance.StepCase, step, invID, runID string) map[string]string {
	id := platform.Identity{JobID: invID, RootJobID: runID, StepID: step, JobPath: c.ScriptPath(), Token: platform.NewToken()}
	if step != "" {
		id.FlowJobID, id.RootFlowJobID, id.FlowPath = runID, runID, conformance.StepFolder+"/flow"
	}
	env := id.Env(platform.Host{Workspace: "wl_step"})
	for k, v := range c.Env {
		env[k] = v
	}
	return env
}

func cmp(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

// compareLate compares the late reports an invocation made before it
// settled with those a run expects.
func compareLate(got []engine.Late, want []conformance.StepLate) []string {
	var out []string
	if len(got) != len(want) {
		out = append(out, fmt.Sprintf("%d late reports, want %d: %s", len(got), len(want), describeLate(got)))
		return out
	}
	for i, w := range want {
		g := got[i]
		if string(g.Kind) != w.Kind || g.Final != w.Final {
			out = append(out, fmt.Sprintf("late %d: %s final=%v, want %s final=%v", i+1, g.Kind, g.Final, w.Kind, w.Final))
			continue
		}
		if w.Body == nil {
			continue
		}
		if g.Body.IsZero() {
			out = append(out, fmt.Sprintf("late %d: no body, want %s", i+1, *w.Body))
			continue
		}
		if same, err := conformance.SameJSON(g.Body.Producer(), []byte(*w.Body), false); err != nil || !same {
			out = append(out, fmt.Sprintf("late %d: body %s, want %s", i+1, g.Body.Producer(), *w.Body))
		}
	}
	return out
}

func describeLate(ls []engine.Late) string {
	var parts []string
	for _, l := range ls {
		parts = append(parts, fmt.Sprintf("%s final=%v %s", l.Kind, l.Final, l.Body.Producer()))
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// stepRecorder is a Reporter that keeps what an invocation reports.
type stepRecorder struct {
	mu       sync.Mutex
	late     []engine.Late
	warnings []string
	left     map[string]int
	// untracked is what native_untracked warnings counted, by kind.
	untracked map[string]int
	settled   chan engine.SettleKind
}

func newStepRecorder() *stepRecorder {
	return &stepRecorder{settled: make(chan engine.SettleKind, 1)}
}

func (r *stepRecorder) Late(l engine.Late) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.late = append(r.late, l)
}

func (r *stepRecorder) Warning(w engine.Warning) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, w.Code)
	if w.Code == harness.WarningLeftBehind {
		if r.left == nil {
			r.left = map[string]int{}
		}
		for k, n := range w.Left {
			r.left[k] += n
		}
	}
	if w.Code == harness.WarningNativeUntracked {
		if r.untracked == nil {
			r.untracked = map[string]int{}
		}
		for k, n := range w.Untracked {
			r.untracked[k] += n
		}
	}
}

// leftBehind is what the invocation's left_behind warning counted, by
// kind; nil when it had none.
func (r *stepRecorder) leftBehind() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.left)
}

// nativeUntracked is what the invocation's native_untracked warning
// counted, by kind; nil when it had none.
func (r *stepRecorder) nativeUntracked() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.untracked)
}

func (r *stepRecorder) Settled(k engine.SettleKind) {
	select {
	case r.settled <- k:
	default:
	}
}

func (r *stepRecorder) waitSettled(within time.Duration) (engine.SettleKind, bool) {
	select {
	case k := <-r.settled:
		return k, true
	case <-time.After(within):
		return "", false
	}
}

func (r *stepRecorder) lates() []engine.Late {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.Late(nil), r.late...)
}

func (r *stepRecorder) warningCodes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.warnings...)
}

// syncBuffer is an io.Writer safe for the harness's concurrent output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// compactJSON writes an observation on one line, with its placeholders and
// markers as written: encoding/json escapes <, > and & in strings, and
// compactJSON writes them back.
func compactJSON(o conformance.Observation) string {
	data, err := json.Marshal(o)
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c != '\\' || i+1 >= len(data) {
			b.WriteByte(c)
			continue
		}
		// An escape inside a string: \u003c, \u003e and \u0026 are the
		// HTML escapes; every other escape is copied whole.
		n := 2
		if data[i+1] == 'u' {
			n = 6
		}
		esc := string(data[i:min(i+n, len(data))])
		switch esc {
		case `\u003c`:
			b.WriteByte('<')
		case `\u003e`:
			b.WriteByte('>')
		case `\u0026`:
			b.WriteByte('&')
		default:
			b.WriteString(esc)
		}
		i += n - 1
	}
	return b.String()
}

func indent(s, prefix string) string {
	var b strings.Builder
	for _, l := range strings.SplitAfter(s, "\n") {
		if l != "" {
			b.WriteString(prefix + l)
		}
	}
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func envOr(k, dflt string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return dflt
}

// repoRoot finds the repository root from the working directory: the
// nearest directory up that holds harnesses/bun/runtime/main.ts.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "harnesses", "bun", "runtime", "main.ts")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no repository root (harnesses/bun) above the working directory; pass --repo")
		}
		dir = parent
	}
}

// newUUID returns a random (version 4) UUID: windmill-lite's job ids (D3).
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

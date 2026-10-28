package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// check runs step-case files (the step level of ADR 0003) on windmill-lite:
// each file's scripts are built into one bundle, by the Bun build helper or,
// for codebase scripts, by esbuild (bundle.BuildCodebase), each case's runs
// go in order to a harness process of its own through the protocol, and
// each outcome is mapped to the value Windmill stores (engine.StoreOutcome)
// and compared with the case's expectation (conformance.CompareStep). With
// Node, each run of a CommonJS codebase bundle also runs on Node, as on
// Windmill, and is compared with its node check (D13). Flow fixtures are
// milestone E1's.
func check(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", conformance.ModeWarm, "configuration: warm (fresh is milestone C3b, host mode F3b)")
	bun := fs.String("bun", envOr("WL_BUN", "bun"), "the Bun executable: the pin, "+harness.PinnedBun)
	node := fs.String("node", os.Getenv("WL_NODE"), "the Node executable that runs CommonJS codebase bundles as Windmill does (D13); empty skips that side")
	repo := fs.String("repo", "", "the repository root, which holds harnesses/bun (default: found from the working directory)")
	verbose := fs.Bool("v", false, "print the harness diagnostics of every failed case")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: wltest check [flags] <step file or directory>...")
		return 2
	}
	if *mode != conformance.ModeWarm {
		fmt.Fprintf(stderr, "wltest check: mode %q is not implemented yet (ADR 0012: fresh is C3b, host mode F3b)\n", *mode)
		return 2
	}
	for _, a := range fs.Args() {
		if info, err := os.Stat(a); err == nil && !info.IsDir() && !conformance.IsStepFile(a) {
			fmt.Fprintf(stderr, "wltest check: %s is not a step file (steps/<lang>/<finding>.yaml); flow fixtures are milestone E1's\n", a)
			return 2
		}
	}
	files, err := conformance.FindStepFiles(fs.Args()...)
	if err != nil {
		fmt.Fprintln(stderr, "wltest check:", err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stderr, "wltest check: no step files found; flow fixtures are milestone E1's")
		return 2
	}
	root := *repo
	if root == "" {
		if root, err = repoRoot(); err != nil {
			fmt.Fprintln(stderr, "wltest check:", err)
			return 2
		}
	}
	sc := &stepChecker{bun: *bun, node: *node, repo: root, mode: *mode}
	failed, passed := 0, 0
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
	fmt.Fprintf(stdout, "%d passed, %d failed (mode %s)\n", passed, failed, *mode)
	if failed > 0 {
		return 1
	}
	return 0
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
)

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
			if wrapper != "" && c.CommonJS() {
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
			cfg.Entries = append(cfg.Entries, bundle.BunScript{ID: caseID(f, c), Path: c.ScriptPath(), File: c.ScriptPath() + ".ts"})
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
	type hostModule struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	manifest := struct {
		Format      string       `json:"format"`
		Note        string       `json:"note"`
		HostModules []hostModule `json:"host_modules"`
	}{Format: "windmill-lite-step-cases", Note: "Built by wltest check for one step file; the bundle format is milestone F4's.", HostModules: []hostModule{}}
	for _, id := range res.HostModuleOrder {
		manifest.HostModules = append(manifest.HostModules, hostModule{Key: res.HostModules[id].Key, ID: id})
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", "", nil, err
	}
	b = append(b, '\n')
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), b, 0o644); err != nil {
		return "", "", nil, err
	}
	sum := sha256.Sum256(b)
	return out, "sha256:" + hex.EncodeToString(sum[:]), problems, nil
}

// runCase runs a case's runs in order in one harness process of its own,
// which is the warm configuration: the module and globalThis survive from
// one run to the next (D1).
func (sc *stepChecker) runCase(ctx context.Context, c *conformance.StepCase, out, digest, id string) ([]string, string) {
	var diag syncBuffer
	path, args := harness.BunCommand(sc.bun, filepath.Join(sc.repo, "harnesses", "bun", "runtime", "main.ts"), out)
	p, err := harness.Spawn(ctx, harness.ProcessConfig{
		Path:          path,
		Args:          args,
		Kind:          "bun",
		Runtime:       harness.PinnedBun,
		Bundle:        digest,
		ModuleState:   engine.ModuleStateWarm,
		EngineVersion: "wltest-check",
		Diagnostics:   &diag,
	})
	if err != nil {
		return []string{"spawn: " + err.Error()}, diag.String()
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Drain(dctx)
	}()
	runID := newUUID()
	var problems []string
	for i := range c.Runs {
		for _, pr := range sc.runOne(ctx, p, c, &c.Runs[i], id, runID) {
			problems = append(problems, fmt.Sprintf("run %d: %s", i+1, pr))
		}
	}
	return problems, diag.String()
}

// runOne runs one invocation and checks what it produced.
func (sc *stepChecker) runOne(ctx context.Context, p *harness.Process, c *conformance.StepCase, r *conformance.StepRun, script, runID string) []string {
	args := jsonval.FromProducer([]byte(r.Args))
	entry, _, err := engine.Entrypoint(args)
	if err != nil {
		return []string{err.Error()}
	}
	step := c.ModuleID()
	invID := newUUID()
	inv := engine.Invocation{
		ID:          invID,
		Run:         runID,
		Site:        conformance.SitePath{}.Child(conformance.Module(cmp(step, conformance.StepModule))),
		Script:      script,
		Entry:       entry,
		Args:        args,
		Env:         jobEnv(c, step, invID, runID),
		Boundary:    engine.BoundaryBun,
		ModuleState: engine.ModuleStateWarm,
	}
	rec := newStepRecorder()
	inv.Report = rec
	rctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	out, err := p.Execute(rctx, inv)
	if err != nil {
		return []string{"execute: " + err.Error()}
	}
	stored := engine.StoreOutcome(out, step)
	text, err := stored.Value.Stored()
	if err != nil {
		return []string{"the stored value: " + err.Error()}
	}
	status := conformance.RunSuccess
	if stored.Failed {
		status = conformance.RunFailure
	}
	got := conformance.Observation{Status: status, Result: json.RawMessage(text)}
	problems, err := conformance.CompareStep(c, r, sc.mode, got)
	if err != nil {
		return []string{"compare: " + err.Error()}
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
	if k.Settled != "" || len(k.Late) > 0 {
		problems = append(problems, compareLate(rec.lates(), k.Late)...)
	}
	if got, want := rec.warningCodes(), k.Warnings; strings.Join(got, ",") != strings.Join(want, ",") {
		problems = append(problems, fmt.Sprintf("warnings %v, want %v", got, want))
	}
	return problems
}

// hasCommonJS reports whether a step file has a CommonJS codebase case.
func hasCommonJS(f *conformance.StepFile) bool {
	for i := range f.Cases {
		if f.Cases[i].CommonJS() {
			return true
		}
	}
	return false
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
	scripts := filepath.Join(out, "scripts", "bun", filepath.FromSlash(id))
	files := map[string]string{
		"main.js":        scripts + ".cjs",
		"meta.json":      scripts + ".meta.json",
		"wl-wrapper.mjs": wrapper,
	}
	var settings struct {
		Assets []bundle.Asset `json:"assets"`
	}
	json.Unmarshal(c.Codebase, &settings)
	for _, a := range settings.Assets {
		files[a.To] = filepath.Join(out, "assets", filepath.FromSlash(a.To))
	}
	for to, from := range files {
		b, err := os.ReadFile(from)
		if err != nil {
			return conformance.Observation{}, err
		}
		if err := writeJobFile(job, to, b); err != nil {
			return conformance.Observation{}, err
		}
	}
	for name, b := range map[string][]byte{"package.json": []byte("{}\n"), "noderun.mjs": nodeDriver, "args.json": []byte(r.Args)} {
		if err := writeJobFile(job, name, b); err != nil {
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

// jobEnv is the invocation's job variables (WR-22): the core WM_* ones of
// a flow step, or of a script job when the case runs outside a flow, whose
// flow variables are empty; then the case's own.
func jobEnv(c *conformance.StepCase, step, invID, runID string) map[string]string {
	env := map[string]string{
		"WM_WORKSPACE":        "wl_step",
		"WM_JOB_ID":           invID,
		"WM_ROOT_JOB_ID":      runID,
		"WM_FLOW_JOB_ID":      "",
		"WM_ROOT_FLOW_JOB_ID": "",
		"WM_FLOW_PATH":        "",
		"WM_FLOW_STEP_ID":     step,
		"WM_JOB_PATH":         c.ScriptPath(),
	}
	if step != "" {
		env["WM_FLOW_JOB_ID"] = runID
		env["WM_ROOT_FLOW_JOB_ID"] = runID
		env["WM_FLOW_PATH"] = conformance.StepFolder + "/flow"
	}
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
	settled  chan engine.SettleKind
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

func compactJSON(o conformance.Observation) string {
	b, err := json.Marshal(o)
	if err != nil {
		return err.Error()
	}
	return string(b)
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

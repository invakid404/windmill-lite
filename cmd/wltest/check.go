package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/openflow"
)

// check runs step-case files (the step level of ADR 0003) and flow
// fixtures (the flow level) on windmill-lite. For a step file, the file's
// scripts are built by the Bun build helper into one bundle, each case's
// runs go in order to a harness process of its own through the protocol,
// and each outcome is mapped to the value Windmill stores
// (engine.StoreOutcome) and compared with the case's expectation
// (conformance.CompareStep). For a flow fixture, each run's flow runs on
// the walking skeleton's interpreter (flowChecker) and its observation is
// compared with the run's expect or recording (milestone E1).
func check(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", conformance.ModeWarm, "configuration: warm (fresh is milestone C3b, host mode F3b)")
	bun := fs.String("bun", envOr("WL_BUN", "bun"), "the Bun executable: the pin, "+harness.PinnedBun)
	repo := fs.String("repo", "", "the repository root, which holds harnesses/bun (default: found from the working directory)")
	verbose := fs.Bool("v", false, "print the harness diagnostics of every failed case or run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: wltest check [flags] <step file, fixture or directory>...")
		return 2
	}
	if *mode != conformance.ModeWarm {
		fmt.Fprintf(stderr, "wltest check: mode %q is not implemented yet (ADR 0012: fresh is C3b, host mode F3b)\n", *mode)
		return 2
	}
	for _, a := range fs.Args() {
		if info, err := os.Stat(a); err == nil && !info.IsDir() && !conformance.IsStepFile(a) {
			fmt.Fprintf(stderr, "wltest check: %s is not a step file (steps/<lang>/<finding>.yaml); a flow fixture is named by its directory, and expression cases are checked by go test ./internal/expr\n", a)
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
	if len(files)+len(dirs) == 0 {
		fmt.Fprintln(stderr, "wltest check: no step files or flow fixtures found")
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
	sc := &stepChecker{bun: *bun, repo: root, mode: *mode}
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
				for _, n := range r.notes {
					fmt.Fprintf(stdout, "  note: %s\n", n)
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

// stepChecker runs step files in one configuration.
type stepChecker struct {
	bun, repo, mode string
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

// stepFacade is the platform facade's origin the harness is given, which
// a native step reads as BASE_URL and BASE_INTERNAL_URL (WR-45). Nothing
// listens there, and the checker answers no platform call (a 501).
const stepFacade = "http://127.0.0.1:9"

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
	for i := range f.Cases {
		c := &f.Cases[i]
		if err := write(c.ScriptPath(), c.Script); err != nil {
			return "", "", nil, err
		}
		lang, _ := language(c)
		cfg.Entries = append(cfg.Entries, bundle.BunScript{ID: caseID(f, c), Path: c.ScriptPath(), File: c.ScriptPath() + ".ts", Language: string(lang)})
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
	var be *bundle.BuildError
	switch {
	case errors.As(err, &be):
		for _, p := range be.Problems {
			id, isScript := strings.CutPrefix(p.Item, "script ")
			if !isScript {
				return "", "", nil, err
			}
			problems[id] = p.Kind + ": " + p.Message
		}
	case err != nil:
		return "", "", nil, err
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

// runCase runs a case's runs in order in one harness process of its own,
// which is the warm configuration: the module and globalThis survive from
// one run to the next (D1).
func (sc *stepChecker) runCase(ctx context.Context, c *conformance.StepCase, out, digest, id string) ([]string, string) {
	var diag syncBuffer
	p, err := sc.spawn(ctx, out, digest, &diag)
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

// spawn starts a warm harness process on the bundle in out.
func (sc *stepChecker) spawn(ctx context.Context, out, digest string, diag io.Writer) (*harness.Process, error) {
	path, args := harness.BunCommand(sc.bun, filepath.Join(sc.repo, "harnesses", "bun", "runtime", "main.ts"), out)
	return harness.Spawn(ctx, harness.ProcessConfig{
		Path:          path,
		Args:          args,
		Kind:          "bun",
		Runtime:       harness.PinnedBun,
		Bundle:        digest,
		ModuleState:   engine.ModuleStateWarm,
		EngineVersion: "wltest-check",
		Diagnostics:   diag,
		Facade:        stepFacade,
	})
}

// runOne runs one invocation and checks what it produced.
func (sc *stepChecker) runOne(ctx context.Context, p *harness.Process, c *conformance.StepCase, r *conformance.StepRun, script, runID string) []string {
	args := jsonval.FromProducer([]byte(r.Args))
	entry, _, err := engine.Entrypoint(args)
	if err != nil {
		return []string{err.Error()}
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
		ModuleState: engine.ModuleStateWarm,
	}
	rec := newStepRecorder()
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
		return []string{"execute: " + err.Error()}
	default:
		stored = engine.StoreOutcome(out, step)
	}
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

package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The step level (ADR 0003): conformance/steps/<lang>/<name>.yaml holds the
// step cases of one finding, each one script run through the real harness
// with its arguments and environment, and what Windmill stores for it.
const (
	// StepDirName is the name of the step level's directory.
	StepDirName = "steps"
	// StepDeferredFile lists, per language directory, the catalogue cases
	// of the findings with a step file that are not step cases, with the
	// reason and where each is checked.
	StepDeferredFile = "deferred.yaml"
	// StepFolder is the Windmill folder of the scripts a step file's
	// cases run, unless a case names its own path.
	StepFolder = "f/wl_step"
	// StepModule is the module id a case runs as by default: its errors
	// carry "step_id":"a", as in the catalogue's notation.
	StepModule = "a"
)

// Configurations a step case runs in (ADR 0006): one process per
// invocation, as Windmill runs a step, or one warm process for all of a
// case's runs, the default.
const (
	ModeWarm  = "warm"
	ModeFresh = "fresh"
)

// DefaultStepStack is the stack pointer every failed step's observation
// masks: the wrapper's error object carries the runtime's stack, which is
// never compared as text (the catalogue's <stack>, Q-WR-10). A successful
// result is data, so nothing in it is masked unless the case says so.
const DefaultStepStack = "/result/error/stack"

// StepFile is one conformance/steps/<lang>/<name>.yaml: the step cases of
// one finding, or of a deviation (D11 and the like) that has none.
type StepFile struct {
	// Path is the file's path; Lang the language directory it is in.
	Path string `json:"-"`
	Lang string `json:"-"`
	// Finding is the finding (or deviation) whose cases the file holds.
	Finding string `json:"finding"`
	// Scope is the finding's scope; a case gives its own where it differs.
	Scope string     `json:"scope"`
	Note  string     `json:"note,omitempty"`
	Cases []StepCase `json:"cases"`
}

// StepCase is one step case: one script, and the runs of it that one
// harness process serves in order.
type StepCase struct {
	// Spec is the catalogue case, or the finding for a case beyond the
	// catalogue, which then needs a Variant.
	Spec       string `json:"spec"`
	Variant    string `json:"variant,omitempty"`
	Provenance string `json:"provenance"`
	Scope      string `json:"scope,omitempty"`
	Title      string `json:"title"`
	Note       string `json:"note,omitempty"`
	// Deviation names the deviation of ADR 0001 §5 whose two readings a
	// run's by_mode gives (D1, D11 and the like), or 5a for the
	// restriction of §5a that a warm process reaches the platform only
	// through fetch, where a fresh one uses loopback TCP (milestone PF1).
	Deviation string `json:"deviation,omitempty"`
	// Script is the script's source, a Bun (TypeScript) file.
	Script string `json:"script"`
	// Path is the script's Windmill path; empty means StepFolder/<name>,
	// with the case's name in snake case.
	Path string `json:"path,omitempty"`
	// StepID is the module id the script runs as, which WM_FLOW_STEP_ID
	// carries: nil means StepModule, and "" a script job outside any flow,
	// whose errors carry no step_id (WR-18).
	StepID *string `json:"step_id,omitempty"`
	// Env adds to or replaces the invocation's job variables.
	Env map[string]string `json:"env,omitempty"`
	// HostModules are scripts registered as host-provided modules (D12),
	// which the script imports by their path.
	HostModules []StepHostModule `json:"host_modules,omitempty"`
	// Codebase makes the script a codebase script, which esbuild bundles
	// (milestone C2b): the settings of its codebases entry as wmill.yaml
	// gives them (format, define, inject, loader, banner, external,
	// assets). Its runs of a CommonJS bundle also run on Node (D13).
	Codebase json.RawMessage `json:"codebase,omitempty"`
	// Files are other files of the folder, by path from the sync root,
	// that a codebase build reads: modules the script imports, inject
	// files, asset sources.
	Files   map[string]string `json:"files,omitempty"`
	Compare StepCompare       `json:"compare,omitempty"`
	Runs    []StepRun         `json:"runs"`
}

// CommonJS reports whether the case is a codebase script built as
// CommonJS: a codebases entry whose format is not esm (WR-47), which
// Windmill runs on Node and windmill-lite on Bun (D13).
func (c *StepCase) CommonJS() bool {
	if c.Codebase == nil {
		return false
	}
	var v struct {
		Format string `json:"format"`
	}
	json.Unmarshal(c.Codebase, &v)
	return v.Format != "esm"
}

// StepHostModule is one host-provided module of a case: a script of the
// folder, whose key is its file path (<path>.ts), as a host contract names
// it.
type StepHostModule struct {
	Path   string `json:"path"`
	Script string `json:"script"`
}

// StepCompare selects how a case's observations are compared: Stack and
// Mask as a fixture's compare (OR-14), beyond DefaultStepStack; Unordered
// arrays as multisets; StrictKeyOrder compares object members in order.
type StepCompare struct {
	Stack          []string `json:"stack,omitempty"`
	Mask           []string `json:"mask,omitempty"`
	Unordered      []string `json:"unordered,omitempty"`
	StrictKeyOrder bool     `json:"strict_key_order,omitempty"`
}

// StepRun is one invocation of a case's script. Its observation is the
// step's stored value: status success or failure, and the result or the
// failure value (WR-21). Its check holds in every configuration; a case of
// a deviation gives one per configuration in ByMode instead.
type StepRun struct {
	// Args is the argument object's JSON text, as the script receives it
	// (after storage and $var:/$res: resolution). _ENTRYPOINT_OVERRIDE in
	// it selects the export (WR-12).
	Args string `json:"args"`
	StepCheck
	ByMode map[string]*StepCheck `json:"by_mode,omitempty"`
	// Node is what the same bundle shows on Node, where Windmill runs a
	// CommonJS codebase bundle, when it differs from the run's own check
	// (deviation D13): its expect and claims. A CommonJS codebase case's
	// run without it must show its own check on Node too.
	Node *StepCheck `json:"node,omitempty"`
	Note string     `json:"note,omitempty"`
}

// StepCheck is what a run must show in a configuration.
type StepCheck struct {
	// Expect is the whole observation.
	Expect *Observation `json:"expect,omitempty"`
	// Claims are partial assertions about the observation, for what the
	// catalogue states only in part (a name, a message prefix).
	Claims []Claim `json:"claims,omitempty"`
	// Late lists what the invocation reports after its outcome, in order
	// (an intercepted process.exit, D11, and anything later): all of it up
	// to the end of the case, so a late report of work it left running
	// after it settled counts too (D9). It is compared when Late or Settled
	// is given. Settled is how it settles: completed, not_started or
	// process_exited, or StepPending for a call that must still be running
	// at the end of the observation window.
	Late    []StepLate `json:"late,omitempty"`
	Settled string     `json:"settled,omitempty"`
	// Warnings lists the codes of the warnings the invocation gets while
	// in flight (D9), all of them up to its settlement (or the end of the
	// observation window): no warnings when empty.
	Warnings []string `json:"warnings,omitempty"`
	// Left is what the invocation's left_behind warning counts, by kind
	// (timeout, interval, immediate, fetch, child): the work it still had
	// when it settled, which the settle-time fence revoked (D9). It is
	// compared when given, and needs left_behind in Warnings.
	Left map[string]int `json:"left,omitempty"`
	// Calls lists the platform calls the facade recorded against the
	// invocation, in order, each as "<status> <METHOD> <route template>"
	// with the workspace written {ws} ("501 POST /w/{ws}/jobs/run/p/{path}"),
	// or with the path as sent where no route of the API matched
	// (ADR 0007, milestone PF1). It is compared when given; an empty list
	// says the invocation made none. A call the facade could attribute to
	// no invocation (refused by the kernel, or a token it does not know)
	// is never listed.
	Calls []string `json:"calls,omitempty"`
}

func (k *StepCheck) empty() bool {
	return k.Expect == nil && len(k.Claims) == 0 && len(k.Late) == 0 && k.Settled == "" && len(k.Warnings) == 0 && k.Left == nil && k.Calls == nil
}

// StepPending is the Settled of a call that does not settle: the runner
// observes it for a fixed window, and it must not settle in it.
const StepPending = "pending"

// StepLate is one late report: its kind (result, error or log), whether it
// is the final one that says how the call ended, and its body's JSON text
// when the case states it.
type StepLate struct {
	Kind  string  `json:"kind"`
	Final bool    `json:"final,omitempty"`
	Body  *string `json:"body,omitempty"`
}

// Name returns the case's name: its spec in lower case with / and . as
// hyphens, then its variant, for example wr-6-a or wr-7-rest.
func (c *StepCase) Name() string {
	n := strings.ToLower(strings.NewReplacer("/", "-", ".", "-").Replace(c.Spec))
	if c.Variant != "" {
		n += "-" + c.Variant
	}
	return n
}

// ScriptPath returns the Windmill path of the case's script.
func (c *StepCase) ScriptPath() string {
	if c.Path != "" {
		return c.Path
	}
	return StepFolder + "/" + strings.ReplaceAll(c.Name(), "-", "_")
}

// ModuleID returns the module id the case runs as, "" for a script job.
func (c *StepCase) ModuleID() string {
	if c.StepID == nil {
		return StepModule
	}
	return *c.StepID
}

// Check returns what run r must show in configuration mode.
func (r *StepRun) Check(mode string) *StepCheck {
	if r.ByMode != nil {
		return r.ByMode[mode]
	}
	return &r.StepCheck
}

// LoadStepFile reads and checks a step-case file.
func LoadStepFile(path string) (*StepFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	j, err := YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f := &StepFile{Path: path, Lang: filepath.Base(filepath.Dir(path))}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if stem := strings.TrimSuffix(filepath.Base(path), ".yaml"); stem != strings.ToLower(f.Finding) {
		return nil, fmt.Errorf("%s: a file of finding %s is named %s.yaml", path, f.Finding, strings.ToLower(f.Finding))
	}
	return f, nil
}

func (f *StepFile) check() error {
	if f.Lang != "bun" {
		return fmt.Errorf("language directory %q: only bun has step cases", f.Lang)
	}
	if f.Finding == "" {
		return errors.New("step file names no finding")
	}
	if !validScopes[f.Scope] {
		return fmt.Errorf("scope %q is not core, later, out or EE", f.Scope)
	}
	if len(f.Cases) == 0 {
		return errors.New("step file has no cases")
	}
	seen := make(map[string]bool)
	paths := make(map[string]string)
	for i := range f.Cases {
		c := &f.Cases[i]
		if err := c.check(); err != nil {
			return fmt.Errorf("case %s: %w", c.Name(), err)
		}
		if seen[c.Name()] {
			return fmt.Errorf("case %s is given twice", c.Name())
		}
		seen[c.Name()] = true
		// Every script of a file shares one bundle, so each path names
		// one source.
		for p, src := range c.scripts() {
			if old, ok := paths[p]; ok && old != src {
				return fmt.Errorf("case %s: path %s has another source in an earlier case", c.Name(), p)
			}
			paths[p] = src
		}
	}
	return nil
}

// scripts maps every path the case writes to its source.
func (c *StepCase) scripts() map[string]string {
	m := map[string]string{c.ScriptPath(): c.Script}
	for _, h := range c.HostModules {
		m[h.Path] = h.Script
	}
	for p, text := range c.Files {
		m["file:"+p] = text
	}
	return m
}

func (c *StepCase) check() error {
	if c.Spec == "" || c.Title == "" || strings.TrimSpace(c.Script) == "" {
		return errors.New("a step case needs spec, title and script")
	}
	if c.Variant != "" && !variantRE.MatchString(c.Variant) {
		return fmt.Errorf("variant %q is not lower-case words joined by hyphens", c.Variant)
	}
	if !strings.Contains(c.Spec, "/") && c.Variant == "" {
		return errors.New("it cites a finding, not a catalogue case, so it needs a variant")
	}
	if !validProvenances[c.Provenance] {
		return fmt.Errorf("provenance %q is not source-derived, oracle-observed, disputed or policy", c.Provenance)
	}
	if c.Scope != "" && !validScopes[c.Scope] {
		return fmt.Errorf("scope %q is not core, later, out or EE", c.Scope)
	}
	for _, p := range append([]string{c.ScriptPath()}, hostPaths(c.HostModules)...) {
		if !validStepPath(p) {
			return fmt.Errorf("path %q is not a Windmill path f/<folder>/<name>", p)
		}
	}
	for _, group := range [][]string{c.Compare.Stack, c.Compare.Mask, c.Compare.Unordered} {
		for _, p := range group {
			if _, err := SplitPointer(p); err != nil {
				return err
			}
		}
	}
	if c.Codebase != nil {
		var settings map[string]json.RawMessage
		if err := json.Unmarshal(c.Codebase, &settings); err != nil || settings == nil {
			return errors.New("codebase is the settings of a codebases entry, a mapping")
		}
	}
	for p := range c.Files {
		if c.Codebase == nil {
			return errors.New("files are read by a codebase build, so they need codebase")
		}
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.Contains(p, "\\") {
			return fmt.Errorf("file %q is not a clean path relative to the sync root", p)
		}
	}
	if len(c.Runs) == 0 {
		return errors.New("a step case has no runs")
	}
	byMode := false
	for i := range c.Runs {
		r := &c.Runs[i]
		if err := r.check(); err != nil {
			return fmt.Errorf("run %d: %w", i+1, err)
		}
		if r.Node != nil && !c.CommonJS() {
			return fmt.Errorf("run %d: node states what Node shows of a CommonJS codebase bundle (D13); the case is not one", i+1)
		}
		byMode = byMode || r.ByMode != nil
	}
	if byMode != (c.Deviation != "") {
		return errors.New("by_mode states a deviation's readings, so it needs deviation, and deviation needs by_mode")
	}
	return nil
}

func hostPaths(hs []StepHostModule) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Path)
	}
	return out
}

// validStepPath reports whether p is a Windmill path in a folder:
// f/<folder>/<name>, each segment lower-case letters, digits and _.
func validStepPath(p string) bool {
	segs := strings.Split(p, "/")
	if len(segs) < 3 || segs[0] != "f" {
		return false
	}
	for _, s := range segs[1:] {
		if s == "" {
			return false
		}
		for _, r := range s {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
				return false
			}
		}
	}
	return true
}

func (r *StepRun) check() error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.Args), &obj); err != nil || obj == nil {
		return fmt.Errorf("args %q is not a JSON object text", r.Args)
	}
	if k := r.Node; k != nil {
		if k.Expect == nil && len(k.Claims) == 0 {
			return errors.New("node checks nothing; it gives expect or claims")
		}
		if len(k.Late) > 0 || k.Settled != "" || len(k.Warnings) > 0 {
			return errors.New("node gives only expect and claims: Node runs the bundle once per process, as Windmill does")
		}
		if err := k.check(); err != nil {
			return fmt.Errorf("node: %w", err)
		}
	}
	if r.ByMode == nil {
		if r.StepCheck.empty() {
			return errors.New("a run needs expect, claims, late, settled or warnings, or by_mode")
		}
		return r.StepCheck.check()
	}
	if !r.StepCheck.empty() {
		return errors.New("by_mode replaces the run's own check; give one or the other")
	}
	if len(r.ByMode) != 2 || r.ByMode[ModeWarm] == nil || r.ByMode[ModeFresh] == nil {
		return errors.New("by_mode gives exactly warm and fresh")
	}
	for _, m := range []string{ModeWarm, ModeFresh} {
		k := r.ByMode[m]
		if k.empty() {
			return fmt.Errorf("by_mode %s checks nothing", m)
		}
		if err := k.check(); err != nil {
			return fmt.Errorf("by_mode %s: %w", m, err)
		}
	}
	return nil
}

func (k *StepCheck) check() error {
	if o := k.Expect; o != nil {
		if o.Status != RunSuccess && o.Status != RunFailure {
			return fmt.Errorf("an expected step is a success or a failure, not %q", o.Status)
		}
		if len(o.Steps) > 0 || len(o.Effects) > 0 {
			return errors.New("a step's observation has no steps and no effects")
		}
	}
	for _, c := range k.Claims {
		if err := c.check(); err != nil {
			return err
		}
	}
	for _, l := range k.Late {
		switch l.Kind {
		case "result", "error", "log":
		default:
			return fmt.Errorf("late kind %q is not result, error or log", l.Kind)
		}
		if l.Body != nil && !json.Valid([]byte(*l.Body)) {
			return fmt.Errorf("late body %q is not JSON text", *l.Body)
		}
	}
	switch k.Settled {
	case "", "completed", "not_started", "process_exited", StepPending:
	default:
		return fmt.Errorf("settled %q is not completed, not_started, process_exited or pending", k.Settled)
	}
	if k.Left != nil {
		if !slices.Contains(k.Warnings, StepLeftBehind) {
			return fmt.Errorf("left counts a %s warning, which warnings does not list", StepLeftBehind)
		}
		for kind, n := range k.Left {
			if n < 1 {
				return fmt.Errorf("left %s is %d; a kind left behind counts at least 1", kind, n)
			}
		}
	}
	return nil
}

// StepLeftBehind is the code of the warning that reports what an
// invocation left behind when it settled (D9).
const StepLeftBehind = "left_behind"

// IsStepFile reports whether path is a step-case file: a .yaml file, other
// than deferred.yaml, in a language directory of a directory named steps.
func IsStepFile(path string) bool {
	return filepath.Ext(path) == ".yaml" && filepath.Base(path) != StepDeferredFile &&
		filepath.Base(filepath.Dir(filepath.Dir(path))) == StepDirName
}

// FindStepFiles returns the step-case files at or below roots, each once,
// sorted.
func FindStepFiles(roots ...string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && IsStepFile(p) && !seen[filepath.Clean(p)] {
				seen[filepath.Clean(p)] = true
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// StepDeferred is one catalogue case that is not a step case.
type StepDeferred struct {
	Spec       string `json:"spec"`
	Reason     string `json:"reason"`
	ExpectedIn string `json:"expected_in"`
}

// LoadStepDeferred reads a language directory's deferred.yaml.
func LoadStepDeferred(path string) ([]StepDeferred, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	j, err := YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var f struct {
		Note     string         `json:"note"`
		Deferred []StepDeferred `json:"deferred"`
	}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, d := range f.Deferred {
		if d.Spec == "" || d.Reason == "" || d.ExpectedIn == "" {
			return nil, fmt.Errorf("%s: a deferred case needs spec, reason and expected_in", path)
		}
	}
	return f.Deferred, nil
}

// CompareStep compares a step run's observation with what its check in
// configuration mode expects: each side is masked first, at the case's own
// stack and mask pointers and, when that side is a failure, at
// DefaultStepStack; then the two are compared with the case's options, and
// the check's claims are checked against the masked observation. It
// returns one line per difference or failed claim.
func CompareStep(c *StepCase, r *StepRun, mode string, got Observation) ([]string, error) {
	return CompareStepCheck(c, r.Check(mode), got)
}

// CompareStepNode compares what a run of a CommonJS codebase bundle shows
// on Node with its node check, or, without one, with its own check in
// configuration mode: only the expect and the claims, since Node runs the
// bundle once per process (D13).
func CompareStepNode(c *StepCase, r *StepRun, mode string, got Observation) ([]string, error) {
	k := r.Node
	if k == nil {
		k = &StepCheck{Expect: r.Check(mode).Expect, Claims: r.Check(mode).Claims}
	}
	return CompareStepCheck(c, k, got)
}

// CompareStepCheck compares a step run's observation with a check, as
// CompareStep does.
func CompareStepCheck(c *StepCase, k *StepCheck, got Observation) ([]string, error) {
	mask := func(o Observation) (Observation, error) {
		stack := c.Compare.Stack
		if o.Status == RunFailure {
			stack = append([]string{DefaultStepStack}, stack...)
		}
		return MaskPaths(o, stack, c.Compare.Mask)
	}
	masked, err := mask(got)
	if err != nil {
		return nil, err
	}
	var out []string
	if k.Expect != nil {
		w, err := mask(*k.Expect)
		if err != nil {
			return nil, err
		}
		diffs, err := Compare(masked, w, CompareOptions{Unordered: c.Compare.Unordered, StrictKeyOrder: c.Compare.StrictKeyOrder})
		if err != nil {
			return nil, err
		}
		for _, d := range diffs {
			out = append(out, d.String())
		}
	}
	failed, err := CheckClaims(masked, k.Claims)
	if err != nil {
		return nil, err
	}
	return append(out, failed...), nil
}

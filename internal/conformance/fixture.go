package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The files and directories of a flow fixture (OR-15).
const (
	FixtureFile  = "fixture.yaml"
	SetupFile    = "setup.yaml"
	SyncTreeDir  = "windmill"
	RunsDir      = "runs"
	RecordedDir  = "recorded"
	runExtension = ".yaml"
)

// Fixture is a flow fixture: a directory holding fixture.yaml, a sync tree
// in windmill/, an optional setup.yaml, one runs/<name>.yaml per run and,
// once recorded, recorded/<name>.json (OR-15, ADR 0003).
type Fixture struct {
	// Dir is the fixture directory, or, for a fixture lifted from an
	// expression file (LiftExprFile), that file.
	Dir string `json:"-"`
	// ID names the fixture, conventionally its path under
	// conformance/flows.
	ID string `json:"id"`
	// Spec lists the findings and catalogue cases the fixture encodes.
	Spec []string `json:"spec"`
	// Scope is the scope tag: core, later, out or EE.
	Scope string `json:"scope"`
	// Provenance is the catalogue's provenance tag (ADR 0003).
	Provenance string `json:"provenance"`
	// Languages lists the script languages the fixture uses.
	Languages []string    `json:"languages"`
	Oracle    OracleNeeds `json:"oracle"`
	Compare   CompareSpec `json:"compare"`
	// Stub runs the fixture in stub mode (ADR 0011): every script body of
	// the sync tree is replaced by a stub that keeps main's signature and
	// takes its outcome from the run's replay table.
	Stub bool   `json:"stub,omitempty"`
	Note string `json:"note,omitempty"`

	// Setup is setup.yaml, or nil when the fixture has none.
	Setup *Setup `json:"-"`
	// Runs are the runs, sorted by name.
	Runs []Run `json:"-"`

	// Tree, when set, is the sync tree's directory in place of
	// Dir/windmill: a generated fixture's tree lives outside the
	// repository (LiftExprFile).
	Tree string `json:"-"`
	// RecordedIn, when set, is the directory of the recordings in place of
	// Dir/recorded.
	RecordedIn string `json:"-"`
	// Generated reports that the sync tree was generated, not committed,
	// so the recorder writes its metadata with the CLI before every push
	// and no drift gate applies to it (OR-8).
	Generated bool `json:"-"`
}

// OracleNeeds is what a fixture needs from the oracle (OR-5, OR-16).
type OracleNeeds struct {
	// Workers is the number of worker containers the fixture needs: 1, or
	// 2 for one level of blocking dispatch (OR-5). Zero means 1.
	Workers int `json:"workers,omitempty"`
	// Effects reports that the fixture's scripts post to the effect sink.
	Effects bool `json:"effects,omitempty"`
	// Concurrency reports that the fixture asserts something about
	// concurrency, which needs as many workers as it says (OR-5).
	Concurrency bool `json:"concurrency,omitempty"`
	// Deploy is how the harness deploys the sync tree: DeployCLI (the
	// default) pushes it with the CLI, after generate-metadata when the
	// session regenerates metadata (OR-8); DeployAPI creates its folders,
	// scripts and flows through the REST API exactly as written, so a field
	// the tree leaves out reaches the server absent and the dependency job
	// fills it (CF-6).
	Deploy string `json:"deploy,omitempty"`
	// Setup is when the harness applies setup.yaml (OR-31): SetupAfterPush
	// (the default) once after the push; SetupBeforePush once before the
	// push and never after it, so the push removes what it seeded; or
	// SetupEachRun after the push and its variables again before every
	// run, for a fixture whose runs change them.
	Setup string `json:"setup,omitempty"`
	// Pull reads the deployed tree back with the CLI's sync pull after the
	// deployment gate and keeps each flow's pulled value in the runs'
	// deployment evidence (SY-6, CF-6). It needs Deploy api, so the value
	// pulled is the one the API created from the tree as written.
	Pull bool `json:"pull,omitempty"`
}

// The deployment methods of OracleNeeds.Deploy.
const (
	DeployCLI = "cli"
	DeployAPI = "api"
)

// The setup policies of OracleNeeds.Setup.
const (
	SetupAfterPush  = "after_push"
	SetupBeforePush = "before_push"
	SetupEachRun    = "each_run"
)

// DeploysThroughAPI reports whether the tree is deployed through the API.
func (o OracleNeeds) DeploysThroughAPI() bool { return o.Deploy == DeployAPI }

// SetupPolicy returns Setup, SetupAfterPush when it is empty.
func (o OracleNeeds) SetupPolicy() string {
	if o.Setup == "" {
		return SetupAfterPush
	}
	return o.Setup
}

// NeedWorkers returns the number of workers the fixture needs.
func (o OracleNeeds) NeedWorkers() int {
	if o.Workers == 0 {
		return 1
	}
	return o.Workers
}

// CompareSpec is a fixture's compare options (OR-14 item 8, OR-15). Paths
// are RFC 6901 JSON Pointers into the observation, for example
// "/steps/m:a0/args/fi_keys".
type CompareSpec struct {
	// Mode is "exact" (the default) or "subset".
	Mode string `json:"mode,omitempty"`
	// KeyOrder is "loose" (the default: objects compare without regard to
	// key order, OR-13) or "strict".
	KeyOrder string `json:"key_order,omitempty"`
	// Stack lists strings that are masked as runtime stacks although the
	// normaliser's provenance rule does not reach them (OR-V3 clause c).
	Stack []string `json:"stack,omitempty"`
	// Mask lists values replaced by MarkerMasked: environment-derived
	// values such as WM_BASE_URL, WM_WORKSPACE, identities and tokens.
	// Stack and Mask point into the observation's values; a run-and-wait
	// response's body is text, which they do not reach inside (only the
	// whole body can be masked).
	Mask []string `json:"mask,omitempty"`
	// Unordered lists arrays that compare as multisets (OR-23).
	Unordered []string `json:"unordered,omitempty"`
}

// Options returns the comparator options the spec selects.
func (c CompareSpec) Options() CompareOptions {
	return CompareOptions{
		Subset:         c.Mode == "subset",
		StrictKeyOrder: c.KeyOrder == "strict",
		Unordered:      append([]string(nil), c.Unordered...),
	}
}

// Setup is setup.yaml: workspace items the harness creates after the push,
// because a push removes selected items that are absent from the tree
// (OR-31). Resource types are created first, then variables, then
// resources.
type Setup struct {
	ResourceTypes []SetupResourceType `json:"resource_types,omitempty"`
	Variables     []SetupVariable     `json:"variables,omitempty"`
	Resources     []SetupResource     `json:"resources,omitempty"`
}

// SetupResourceType is a resource type to create.
type SetupResourceType struct {
	Name        string          `json:"name"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Description string          `json:"description,omitempty"`
}

// SetupVariable is a variable to create.
type SetupVariable struct {
	Path        string `json:"path"`
	Value       string `json:"value"`
	IsSecret    bool   `json:"is_secret,omitempty"`
	Description string `json:"description,omitempty"`
}

// SetupResource is a resource to create.
type SetupResource struct {
	Path         string          `json:"path"`
	ResourceType string          `json:"resource_type"`
	Value        json.RawMessage `json:"value"`
	Description  string          `json:"description,omitempty"`
}

// Run is one runs/<name>.yaml.
type Run struct {
	// Name is the file's base name without .yaml.
	Name string `json:"-"`
	// Spec lists the catalogue cases this run encodes, when they are
	// narrower than the fixture's.
	Spec []string `json:"spec,omitempty"`
	// Kind is "flow" (the default) or "script".
	Kind string `json:"kind,omitempty"`
	// Target is the path of the flow or script to run.
	Target string `json:"target"`
	// Args is the argument object; nil means {}.
	Args json.RawMessage `json:"args,omitempty"`
	// CancelAfterMS cancels the run that many milliseconds after it was
	// submitted.
	CancelAfterMS *int `json:"cancel_after_ms,omitempty"`
	// Group names the runs that are submitted together, as concurrent
	// outer runs; a run without a group runs alone (RunBatches).
	Group string `json:"group,omitempty"`
	// Submit is how the run is submitted: empty for the asynchronous
	// submit and poll of OR-10, or SubmitRunWait for the synchronous
	// run-and-wait endpoint, whose HTTP response the observation then
	// holds as well (FX-36, FX-37, OR-10/b). Either way the run is polled
	// to completion and walked.
	Submit string `json:"submit,omitempty"`
	// Replay is the run's replay table, for a fixture in stub mode: the
	// outcome of each attempt at each stubbed site. A stub fixture's run
	// without one answers every call with replay_miss.
	Replay ReplayTable `json:"replay,omitempty"`
	// Expect is the expected normalised observation, or nil.
	Expect *Observation `json:"expect,omitempty"`
	// Claims are what the catalogue says about the observation, where it
	// says less than the whole of it (an error message's prefix, one
	// argument). A test of the recording batch checks them against the
	// committed recording; the recorder does not, so a contradicted claim
	// still leaves its recording to inspect.
	Claims []Claim `json:"claims,omitempty"`
	// Delays are lower bounds on the time between two of the run's jobs,
	// checked against the recording's provenance like Claims.
	Delays []Delay `json:"delays,omitempty"`
	// Derived points to values of the observation that a step copied from
	// an error windmill-lite generates, such as a failure module returning
	// its error's message (FX-15/b): ADR 0011's location rule reaches them
	// as it reaches the errors the trace says windmill-lite generated, by
	// its templates only (StripLocations). The flow's author names them,
	// since no trace can follow a value through a user's script.
	Derived []string `json:"derived,omitempty"`
	// Unit restates the run's expressions at unit level, with the context
	// each evaluation sees (UnitCase).
	Unit []UnitCase `json:"unit,omitempty"`
	Note string     `json:"note,omitempty"`

	// Recorded is recorded/<name>.json, or nil when the run has no
	// recording yet.
	Recorded *Recording `json:"-"`
}

// SubmitRunWait is Run.Submit for the synchronous run-and-wait endpoint,
// POST /w/{ws}/jobs/run_wait_result/f/{path}.
const SubmitRunWait = "run_wait_result"

// IsScript reports whether the run targets a script rather than a flow.
func (r Run) IsScript() bool { return r.Kind == "script" }

// RunsAndWaits reports whether the run is submitted through run-and-wait.
func (r Run) RunsAndWaits() bool { return r.Submit == SubmitRunWait }

// ArgsOrEmpty returns the run's arguments, {} when it has none.
func (r Run) ArgsOrEmpty() json.RawMessage {
	if r.Args == nil {
		return json.RawMessage("{}")
	}
	return r.Args
}

// Recording is recorded/<name>.json: the last oracle observation of a run
// and its provenance, kept apart so that only the observation is compared
// (OR-V5).
type Recording struct {
	Observation Observation     `json:"observation"`
	Provenance  json.RawMessage `json:"provenance"`
}

// WriteRecording writes rec to path as indented JSON (IndentJSON), creating
// its directory.
func WriteRecording(path string, rec Recording) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	out, err := IndentJSON(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// RecordingDir returns the directory of f's recordings: RecordedIn, or
// Dir/recorded.
func (f *Fixture) RecordingDir() string {
	if f.RecordedIn != "" {
		return f.RecordedIn
	}
	return filepath.Join(f.Dir, RecordedDir)
}

// RecordingPath returns the path of the run's recording in f.
func (f *Fixture) RecordingPath(run string) string {
	return filepath.Join(f.RecordingDir(), run+".json")
}

// SyncTree returns the fixture's sync tree directory: Tree, or
// Dir/windmill.
func (f *Fixture) SyncTree() string {
	if f.Tree != "" {
		return f.Tree
	}
	return filepath.Join(f.Dir, SyncTreeDir)
}

// LoadFixture reads the fixture in dir and checks its fields.
func LoadFixture(dir string) (*Fixture, error) {
	f := &Fixture{Dir: dir}
	if err := decodeYAMLFile(filepath.Join(dir, FixtureFile), f); err != nil {
		return nil, err
	}
	if err := f.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, FixtureFile), err)
	}
	if info, err := os.Stat(f.SyncTree()); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s: fixture has no %s/ sync tree", dir, SyncTreeDir)
	}
	setupPath := filepath.Join(dir, SetupFile)
	if _, err := os.Stat(setupPath); err == nil {
		f.Setup = &Setup{}
		if err := decodeYAMLFile(setupPath, f.Setup); err != nil {
			return nil, err
		}
		if err := f.Setup.check(); err != nil {
			return nil, fmt.Errorf("%s: %w", setupPath, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if p := f.Oracle.SetupPolicy(); p != SetupAfterPush && (f.Setup == nil || len(f.Setup.Variables) == 0) {
		return nil, fmt.Errorf("%s: oracle.setup %s needs variables in %s", filepath.Join(dir, FixtureFile), p, SetupFile)
	}
	entries, err := os.ReadDir(filepath.Join(dir, RunsDir))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), runExtension) {
			continue
		}
		r := Run{Name: strings.TrimSuffix(e.Name(), runExtension)}
		p := filepath.Join(dir, RunsDir, e.Name())
		if err := decodeYAMLFile(p, &r); err != nil {
			return nil, err
		}
		if err := r.check(); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if r.Replay != nil && !f.Stub {
			return nil, fmt.Errorf("%s: replay needs stub: true in %s", p, FixtureFile)
		}
		rec, err := readRecording(f.RecordingPath(r.Name))
		if err != nil {
			return nil, err
		}
		r.Recorded = rec
		f.Runs = append(f.Runs, r)
	}
	if len(f.Runs) == 0 {
		return nil, fmt.Errorf("%s: fixture has no runs/*%s", dir, runExtension)
	}
	if f.Oracle.SetupPolicy() == SetupEachRun {
		// Concurrent runs would share the variables between resets.
		for _, r := range f.Runs {
			if r.Group != "" {
				return nil, fmt.Errorf("%s: oracle.setup %s resets before each run, so run %s cannot be in group %s", filepath.Join(dir, FixtureFile), SetupEachRun, r.Name, r.Group)
			}
		}
	}
	sort.Slice(f.Runs, func(i, j int) bool { return f.Runs[i].Name < f.Runs[j].Name })
	return f, nil
}

func readRecording(path string) (*Recording, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec Recording
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &rec, nil
}

// FindFixtures returns every fixture directory at or below the given
// roots, sorted: a directory is a fixture when it holds fixture.yaml.
// Fixture directories are not searched further.
func FindFixtures(roots ...string) ([]string, error) {
	var dirs []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if _, err := os.Stat(filepath.Join(p, FixtureFile)); err == nil {
				dirs = append(dirs, p)
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

func decodeYAMLFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	j, err := YAMLToJSON(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

var (
	validScopes      = map[string]bool{"core": true, "later": true, "out": true, "EE": true}
	validProvenances = map[string]bool{"source-derived": true, "oracle-observed": true, "disputed": true, "policy": true}
)

func (f *Fixture) check() error {
	if f.ID == "" {
		return errors.New("fixture has no id")
	}
	if len(f.Spec) == 0 {
		return errors.New("fixture cites no spec IDs")
	}
	if !validScopes[f.Scope] {
		return fmt.Errorf("scope %q is not core, later, out or EE", f.Scope)
	}
	if !validProvenances[f.Provenance] {
		return fmt.Errorf("provenance %q is not source-derived, oracle-observed, disputed or policy", f.Provenance)
	}
	if w := f.Oracle.Workers; w != 0 && w != 1 && w != 2 {
		return fmt.Errorf("oracle.workers is %d, want 1 or 2", w)
	}
	switch f.Oracle.Deploy {
	case "", DeployCLI, DeployAPI:
	default:
		return fmt.Errorf("oracle.deploy %q is not %s or %s", f.Oracle.Deploy, DeployCLI, DeployAPI)
	}
	if f.Stub && f.Oracle.DeploysThroughAPI() {
		return fmt.Errorf("stub: true needs oracle.deploy %s: stub mode pushes a stubbed tree with the CLI", DeployCLI)
	}
	switch f.Oracle.Setup {
	case "", SetupAfterPush, SetupEachRun:
	case SetupBeforePush:
		if f.Oracle.DeploysThroughAPI() {
			return fmt.Errorf("oracle.setup %s needs oracle.deploy %s: only the CLI's push removes what was seeded", SetupBeforePush, DeployCLI)
		}
	default:
		return fmt.Errorf("oracle.setup %q is not %s, %s or %s", f.Oracle.Setup, SetupAfterPush, SetupBeforePush, SetupEachRun)
	}
	if f.Oracle.Pull && !f.Oracle.DeploysThroughAPI() {
		return fmt.Errorf("oracle.pull needs oracle.deploy %s", DeployAPI)
	}
	switch f.Compare.Mode {
	case "", "exact", "subset":
	default:
		return fmt.Errorf("compare.mode %q is not exact or subset", f.Compare.Mode)
	}
	switch f.Compare.KeyOrder {
	case "", "loose", "strict":
	default:
		return fmt.Errorf("compare.key_order %q is not loose or strict", f.Compare.KeyOrder)
	}
	if f.Compare.Mode == "subset" && f.Compare.KeyOrder == "strict" {
		return errors.New("compare.mode subset cannot be combined with key_order strict: a subset has no member order to compare")
	}
	for _, group := range [][]string{f.Compare.Stack, f.Compare.Mask, f.Compare.Unordered} {
		for _, p := range group {
			if _, err := SplitPointer(p); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Setup) check() error {
	for _, t := range s.ResourceTypes {
		if t.Name == "" {
			return errors.New("resource type has no name")
		}
	}
	for _, v := range s.Variables {
		if v.Path == "" {
			return errors.New("variable has no path")
		}
	}
	for _, r := range s.Resources {
		if r.Path == "" || r.ResourceType == "" || r.Value == nil {
			return errors.New("resource needs path, resource_type and value")
		}
	}
	return nil
}

func (r *Run) check() error {
	if r.Target == "" {
		return errors.New("run has no target")
	}
	switch r.Kind {
	case "", "flow", "script":
	default:
		return fmt.Errorf("kind %q is not flow or script", r.Kind)
	}
	if r.Args != nil {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(r.Args, &obj); err != nil || obj == nil {
			return errors.New("args must be an object")
		}
	}
	if r.CancelAfterMS != nil && *r.CancelAfterMS < 0 {
		return errors.New("cancel_after_ms is negative")
	}
	switch r.Submit {
	case "":
	case SubmitRunWait:
		// One flow, alone and to its end: a canceled or concurrent
		// run-and-wait would observe the cancellation or the other runs.
		if r.IsScript() || r.Group != "" || r.CancelAfterMS != nil || r.Replay != nil {
			return fmt.Errorf("submit %s runs a flow alone: it takes no kind script, group, cancel_after_ms or replay", SubmitRunWait)
		}
	default:
		return fmt.Errorf("submit %q is not %s", r.Submit, SubmitRunWait)
	}
	for _, c := range r.Claims {
		if err := c.check(); err != nil {
			return err
		}
	}
	for _, d := range r.Delays {
		if err := d.check(); err != nil {
			return err
		}
	}
	for _, p := range r.Derived {
		if _, err := SplitPointer(p); err != nil {
			return fmt.Errorf("derived: %w", err)
		}
	}
	for i := range r.Unit {
		if err := r.Unit[i].check(); err != nil {
			return err
		}
	}
	return nil
}

// RunBatches splits runs, sorted by name, into the batches a runner
// submits together: each group's runs, in name order, at the place of the
// group's first run, and every run without a group alone.
func RunBatches(runs []Run) [][]Run {
	var batches [][]Run
	at := map[string]int{}
	for _, r := range runs {
		if r.Group == "" {
			batches = append(batches, []Run{r})
			continue
		}
		if i, ok := at[r.Group]; ok {
			batches[i] = append(batches[i], r)
			continue
		}
		at[r.Group] = len(batches)
		batches = append(batches, []Run{r})
	}
	return batches
}

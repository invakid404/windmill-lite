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
	// Dir is the fixture directory.
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
	Note      string      `json:"note,omitempty"`

	// Setup is setup.yaml, or nil when the fixture has none.
	Setup *Setup `json:"-"`
	// Runs are the runs, sorted by name.
	Runs []Run `json:"-"`
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
	// Expect is the expected normalised observation, or nil.
	Expect *Observation `json:"expect,omitempty"`
	Note   string       `json:"note,omitempty"`

	// Recorded is recorded/<name>.json, or nil when the run has no
	// recording yet.
	Recorded *Recording `json:"-"`
}

// IsScript reports whether the run targets a script rather than a flow.
func (r Run) IsScript() bool { return r.Kind == "script" }

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

// RecordingPath returns the path of the run's recording in f.
func (f *Fixture) RecordingPath(run string) string {
	return filepath.Join(f.Dir, RecordedDir, run+".json")
}

// SyncTree returns the fixture's sync tree directory.
func (f *Fixture) SyncTree() string { return filepath.Join(f.Dir, SyncTreeDir) }

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
	return nil
}

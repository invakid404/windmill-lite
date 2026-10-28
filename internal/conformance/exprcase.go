package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The expression level (ADR 0003): conformance/exprs/<name>.yaml holds the
// context-free expression cases of one finding, and the oracle recordings
// of their generated flows are under conformance/exprs/recorded/<name>/.
const (
	// ExprDirName is the name of the expression level's directory.
	ExprDirName = "exprs"
	// ExprDeferredFile lists the catalogue cases that the recording
	// batches did not encode, with the reason and where each is checked.
	ExprDeferredFile = "deferred.yaml"
	// ExprFolder is the Windmill folder of a generated tree.
	ExprFolder = "f/wl_expr"
	// ExprSink is the script every generated flow's one step runs.
	ExprSink = ExprFolder + "/sink"
	// ExprStep and ExprInput are the generated step's module id and the
	// argument that carries the expression, the catalogue's default
	// fixture (step b, transform x).
	ExprStep  = "b"
	ExprInput = "x"
)

// The sites an expression case may name. Every other site has a context
// (a result before it, a loop, a handler) and is a flow fixture.
const (
	// SiteTransform is a javascript input transform of the flow's first
	// step, the default.
	SiteTransform = "transform"
	// SiteStatic is a static input transform, copied verbatim (JE-40).
	SiteStatic = "static"
	// SiteSkipIf is the skip_if predicate of the flow's first step.
	SiteSkipIf = "skip_if"
)

// ErrContextualCase is the kind of the error that refuses an expression
// case which names a predecessor, a handler or a container: such a case is
// a flow fixture (ADR 0003).
const ErrContextualCase = "contextual_case_needs_flow_fixture"

// ExprCaseError is a refused expression case.
type ExprCaseError struct {
	Kind string
	Case string
	What string
}

func (e *ExprCaseError) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Kind, e.Case, e.What)
}

// ExprFile is one conformance/exprs/<name>.yaml: context-free expression
// cases, each an input transform or the skip_if predicate of a flow's
// first step, whose only bound values are flow_input and flow_env (ADR
// 0003). At module 0, previous_result is the flow input (JE-36 rule 1) and
// the previous id is "" (JE-8), so neither names a predecessor.
type ExprFile struct {
	// Path is the file's path.
	Path string `json:"-"`
	// Finding is the finding whose cases the file holds.
	Finding string `json:"finding"`
	// Scope is the finding's scope; a case gives its own where it differs.
	Scope string `json:"scope"`
	// FlowInput and FlowEnv are the cases' defaults, as JSON texts.
	FlowInput *string `json:"flow_input,omitempty"`
	FlowEnv   *string `json:"flow_env,omitempty"`
	// Slow marks cases that need a generous time allowance (the
	// catalogue's slow tag); the recorder records slow files last.
	Slow bool `json:"slow,omitempty"`
	// Mask lists JSON Pointers into every case's observation whose values
	// are replaced by <masked> (OR-14 item 8): text the oracle writes
	// with timestamps or host names, which no two runs share.
	Mask  []string   `json:"mask,omitempty"`
	Note  string     `json:"note,omitempty"`
	Cases []ExprCase `json:"cases"`
}

// ExprCase is one expression case.
type ExprCase struct {
	// Spec is the catalogue case, or the finding for a case beyond the
	// catalogue, which then needs a Variant.
	Spec string `json:"spec"`
	// Variant names one of several inputs of one catalogue case, in
	// lower-case letters, digits and hyphens.
	Variant    string `json:"variant,omitempty"`
	Provenance string `json:"provenance"`
	Scope      string `json:"scope,omitempty"`
	Title      string `json:"title"`
	// Site is SiteTransform (the default), SiteStatic or SiteSkipIf.
	Site string `json:"site,omitempty"`
	// Expr is the expression text, for a transform or skip_if site.
	Expr *string `json:"expr,omitempty"`
	// Static is the static value's JSON text, for a static site. The CLI
	// reads the flow with JavaScript, so a static number keeps only a
	// double's precision.
	Static *string `json:"static,omitempty"`
	// FlowInput is the run's arguments, the flow input, as a JSON object
	// text: the producer text the submission carries (the job API stores
	// it as jsonb before any expression reads it, JE-44).
	FlowInput *string `json:"flow_input,omitempty"`
	// FlowEnv is the flow value's flow_env, as a JSON object text.
	FlowEnv *string    `json:"flow_env,omitempty"`
	Expect  ExprExpect `json:"expect"`
	Note    string     `json:"note,omitempty"`
}

// ExprExpect is what the catalogue expects of an expression case. Exactly
// one of Value, Error, FlowError and Skipped is given.
type ExprExpect struct {
	// Value is the JSON text of argument x as the job API records it
	// (point O2: jsonb-normalised, before $var:/$res: resolution).
	Value *string `json:"value,omitempty"`
	// KeyOrder is "strict" when Value's member order is part of the
	// expectation; recorded arguments are jsonb, so it is jsonb's order.
	KeyOrder string `json:"key_order,omitempty"`
	// Text is the evaluator's exact output text (point O1), which the
	// oracle cannot observe; it needs Value, which must be its stored
	// form.
	Text *string `json:"text,omitempty"`
	// Error matches the step's error object when the expression fails
	// the step (an input transform, JE-23).
	Error *MessageMatch `json:"error,omitempty"`
	// FlowError matches the run's error object when the expression fails
	// the flow (a predicate at module 0, JE-35 route (b)).
	FlowError *MessageMatch `json:"flow_error,omitempty"`
	// Skipped is whether the skip_if predicate skipped the step.
	Skipped *bool `json:"skipped,omitempty"`
	// Status is the run's status where it is not the one the expectation
	// implies (success for a value or a skip, failure for an error): a
	// run the oracle canceled.
	Status string `json:"status,omitempty"`
}

// contextualKeys are case keys that would give an expression a context
// beyond flow_input and flow_env.
var contextualKeys = []string{"previous", "previous_id", "previous_result", "predecessor", "predecessors", "results", "steps", "failure_module", "handler", "container", "loop", "branch", "retry", "parent"}

var variantRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Name returns the case's run name: its spec in lower case with / and . as
// hyphens, then its variant, for example je-7-a or je-1-math-trig.
func (c *ExprCase) Name() string {
	n := strings.ToLower(strings.NewReplacer("/", "-", ".", "-").Replace(c.Spec))
	if c.Variant != "" {
		n += "-" + c.Variant
	}
	return n
}

// FlowPath returns the path of the case's generated flow.
func (c *ExprCase) FlowPath() string {
	return ExprFolder + "/" + strings.ReplaceAll(c.Name(), "-", "_")
}

// SiteOrDefault returns the case's site, SiteTransform when it gives none.
func (c *ExprCase) SiteOrDefault() string {
	if c.Site == "" {
		return SiteTransform
	}
	return c.Site
}

// LoadExprFile reads and checks an expression-case file.
func LoadExprFile(path string) (*ExprFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	j, err := YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var raw struct {
		Cases []map[string]json.RawMessage `json:"cases"`
	}
	if err := json.Unmarshal(j, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, c := range raw.Cases {
		for _, k := range contextualKeys {
			if _, ok := c[k]; ok {
				var spec string
				_ = json.Unmarshal(c["spec"], &spec)
				if spec == "" {
					spec = fmt.Sprintf("case %d", i)
				}
				return nil, fmt.Errorf("%s: %w", path, &ExprCaseError{Kind: ErrContextualCase, Case: spec, What: "it names " + k + "; write it as a flow fixture"})
			}
		}
	}
	f := &ExprFile{Path: path}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// The recordings are named after the file, so the file is named after
	// the finding.
	if stem := strings.TrimSuffix(filepath.Base(path), ".yaml"); stem != strings.ToLower(f.Finding) {
		return nil, fmt.Errorf("%s: a file of finding %s is named %s.yaml", path, f.Finding, strings.ToLower(f.Finding))
	}
	return f, nil
}

func (f *ExprFile) check() error {
	if f.Finding == "" {
		return errors.New("expression file names no finding")
	}
	if !validScopes[f.Scope] {
		return fmt.Errorf("scope %q is not core, later, out or EE", f.Scope)
	}
	if len(f.Cases) == 0 {
		return errors.New("expression file has no cases")
	}
	for _, t := range []*string{f.FlowInput, f.FlowEnv} {
		if err := checkObjectText(t); err != nil {
			return err
		}
	}
	for _, p := range f.Mask {
		if _, err := SplitPointer(p); err != nil {
			return err
		}
	}
	seen := make(map[string]bool)
	for i := range f.Cases {
		c := &f.Cases[i]
		if err := c.check(); err != nil {
			return err
		}
		if seen[c.Name()] {
			return fmt.Errorf("case %s is given twice", c.Name())
		}
		seen[c.Name()] = true
	}
	return nil
}

func checkObjectText(t *string) error {
	if t == nil {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(*t), &obj); err != nil || obj == nil {
		return fmt.Errorf("%q is not a JSON object text", *t)
	}
	return nil
}

func (c *ExprCase) check() error {
	if c.Spec == "" || c.Title == "" {
		return errors.New("expression case needs spec and title")
	}
	if c.Variant != "" && !variantRE.MatchString(c.Variant) {
		return fmt.Errorf("case %s: variant %q is not lower-case words joined by hyphens", c.Spec, c.Variant)
	}
	if !strings.Contains(c.Spec, "/") && c.Variant == "" {
		return fmt.Errorf("case %s cites a finding, not a catalogue case, so it needs a variant", c.Spec)
	}
	if !validProvenances[c.Provenance] {
		return fmt.Errorf("case %s: provenance %q is not source-derived, oracle-observed, disputed or policy", c.Spec, c.Provenance)
	}
	if c.Scope != "" && !validScopes[c.Scope] {
		return fmt.Errorf("case %s: scope %q is not core, later, out or EE", c.Spec, c.Scope)
	}
	switch c.SiteOrDefault() {
	case SiteTransform, SiteSkipIf:
		if c.Expr == nil || c.Static != nil {
			return fmt.Errorf("case %s: a %s site needs expr and no static", c.Spec, c.SiteOrDefault())
		}
	case SiteStatic:
		if c.Static == nil || c.Expr != nil || !json.Valid([]byte(*c.Static)) {
			return fmt.Errorf("case %s: a static site needs a JSON static value and no expr", c.Spec)
		}
	default:
		return &ExprCaseError{Kind: ErrContextualCase, Case: c.Spec, What: fmt.Sprintf("site %q has a context; write it as a flow fixture", c.Site)}
	}
	for _, t := range []*string{c.FlowInput, c.FlowEnv} {
		if err := checkObjectText(t); err != nil {
			return fmt.Errorf("case %s: %w", c.Spec, err)
		}
	}
	e := c.Expect
	given := 0
	for _, ok := range []bool{e.Value != nil, e.Error != nil, e.FlowError != nil, e.Skipped != nil} {
		if ok {
			given++
		}
	}
	if given != 1 {
		return fmt.Errorf("case %s: expect gives exactly one of value, error, flow_error and skipped", c.Spec)
	}
	if e.Value != nil && !json.Valid([]byte(*e.Value)) {
		return fmt.Errorf("case %s: expect.value is not JSON text", c.Spec)
	}
	if e.Text != nil && (e.Value == nil || !json.Valid([]byte(*e.Text))) {
		return fmt.Errorf("case %s: expect.text needs expect.value and must be JSON text", c.Spec)
	}
	switch e.KeyOrder {
	case "", "loose", "strict":
	default:
		return fmt.Errorf("case %s: key_order %q is not loose or strict", c.Spec, e.KeyOrder)
	}
	switch e.Status {
	case "", string(RunSuccess), string(RunFailure), string(RunCanceled), string(RunSkipped):
	default:
		return fmt.Errorf("case %s: expect.status %q is not a run status", c.Spec, e.Status)
	}
	// A skip_if case's argument x is always static null, and an error of
	// a predicate at module 0 fails the flow, not the step (JE-35 route
	// (b)); a transform's error fails the step, unless the run is canceled
	// before the step exists.
	if c.SiteOrDefault() == SiteSkipIf {
		if e.Value != nil || e.Error != nil {
			return fmt.Errorf("case %s: a skip_if case expects skipped or flow_error", c.Spec)
		}
	} else if e.Skipped != nil || (e.FlowError != nil && e.Status != string(RunCanceled)) {
		return fmt.Errorf("case %s: skipped and flow_error need a skip_if site, or flow_error a canceled run", c.Spec)
	}
	for _, m := range []*MessageMatch{e.Error, e.FlowError} {
		if m != nil {
			if err := m.check(); err != nil {
				return fmt.Errorf("case %s: %w", c.Spec, err)
			}
		}
	}
	return nil
}

// Input returns the case's flow input, its own or the file's, or {}.
func (f *ExprFile) Input(c *ExprCase) string {
	switch {
	case c.FlowInput != nil:
		return *c.FlowInput
	case f.FlowInput != nil:
		return *f.FlowInput
	}
	return "{}"
}

// Env returns the case's flow_env text, its own or the file's, or "".
func (f *ExprFile) Env(c *ExprCase) string {
	switch {
	case c.FlowEnv != nil:
		return *c.FlowEnv
	case f.FlowEnv != nil:
		return *f.FlowEnv
	}
	return ""
}

// CaseScope returns the case's scope, its own or the file's.
func (f *ExprFile) CaseScope(c *ExprCase) string {
	if c.Scope != "" {
		return c.Scope
	}
	return f.Scope
}

// Claims returns what the case's expectation says about the observation
// of its generated flow.
func (c *ExprCase) Claims() []Claim {
	step := "/steps/" + Module(ExprStep).String()
	e := c.Expect
	status := func(s string) Claim {
		if e.Status != "" {
			s = e.Status
		}
		return Claim{At: "/status", Equals: jsonText(s)}
	}
	switch {
	case e.Value != nil:
		return []Claim{
			status("success"),
			{At: step + "/args/" + ExprInput, Equals: json.RawMessage(*e.Value), KeyOrder: e.KeyOrder},
		}
	case e.Skipped != nil && *e.Skipped:
		return []Claim{status("success"), {At: step + "/skipped", Equals: json.RawMessage("true")}}
	case e.Skipped != nil:
		return []Claim{status("success"), {At: step + "/skipped", Absent: true}, {At: step + "/status", Equals: jsonText("success")}}
	case e.Error != nil:
		return append([]Claim{status("failure"), {At: step + "/status", Equals: jsonText("failure")}}, e.Error.Claims(step+"/result/error")...)
	}
	return append([]Claim{status("failure")}, e.FlowError.Claims("/result/error")...)
}

// FindExprFiles returns every expression-case file at or below the given
// roots, sorted and without repeats: a .yaml file in a directory named
// exprs, other than its deferred.yaml. A root may also be such a file. Flow
// fixtures are not searched, so a Windmill folder named exprs in a sync
// tree is not taken for the expression level.
func FindExprFiles(roots ...string) ([]string, error) {
	seen := map[string]bool{}
	var files []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if _, err := os.Stat(filepath.Join(p, FixtureFile)); err == nil {
					return filepath.SkipDir
				}
				return nil
			}
			if IsExprFile(p) && !seen[filepath.Clean(p)] {
				seen[filepath.Clean(p)] = true
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

// IsExprFile reports whether path names an expression-case file: a .yaml
// file in a directory named exprs, other than deferred.yaml.
func IsExprFile(path string) bool {
	return filepath.Base(filepath.Dir(path)) == ExprDirName && strings.HasSuffix(path, ".yaml") && filepath.Base(path) != ExprDeferredFile
}

// LiftExprFile writes the generated sync tree of f's cases into dir, which
// must not exist, and returns the generated fixture (ADR 0011: expression
// cases are lifted into generated one-step flow fixtures for record). Each
// case gets a flow f/wl_expr/<case> whose one module, b, runs the sink
// script f/wl_expr/sink with the case's expression as its input x (or as
// its skip_if, with x static null), and one run with the case's flow input.
// The sink returns null, so the observation holds what the expression
// produced (b's recorded argument, point O2) and not what Bun made of it.
// The tree has no generated metadata: the recorder writes it with the CLI
// before the push. Recordings are read from and written to
// <dir of f>/recorded/<name of f>/<case>.json.
func LiftExprFile(f *ExprFile, dir string) (*Fixture, error) {
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("conformance: %s exists", dir)
	}
	stem := strings.TrimSuffix(filepath.Base(f.Path), ".yaml")
	// The fixture's provenance is its cases' when they agree.
	prov := f.Cases[0].Provenance
	for _, c := range f.Cases {
		if c.Provenance != prov {
			prov = "source-derived"
		}
	}
	fx := &Fixture{
		Dir:        f.Path,
		ID:         ExprDirName + "/" + stem,
		Scope:      f.Scope,
		Provenance: prov,
		Languages:  []string{"bun"},
		Tree:       filepath.Join(dir, SyncTreeDir),
		RecordedIn: filepath.Join(filepath.Dir(f.Path), RecordedDir, stem),
		Generated:  true,
		Compare:    CompareSpec{Mask: append([]string(nil), f.Mask...)},
	}
	files := map[string]string{
		"wmill.yaml": "defaultTs: bun\nincludes: [\"f/**\"]\n",
		ExprSink + ".ts": "// The sink of the generated expression flows: it receives the\n" +
			"// expression's value as x and returns null, so the recorded argument\n" +
			"// is the observation.\n" +
			"export async function main(x: any) {\n  return null;\n}\n",
	}
	for i := range f.Cases {
		c := &f.Cases[i]
		fx.Spec = append(fx.Spec, c.Spec)
		flow, err := c.flowYAML(f)
		if err != nil {
			return nil, err
		}
		files[c.FlowPath()+".flow/flow.yaml"] = flow
		run := Run{Name: c.Name(), Spec: []string{c.Spec}, Target: c.FlowPath(), Args: json.RawMessage(f.Input(c)), Claims: c.Claims(), Note: c.Title}
		rec, err := readRecording(fx.RecordingPath(run.Name))
		if err != nil {
			return nil, err
		}
		run.Recorded = rec
		fx.Runs = append(fx.Runs, run)
	}
	sort.Slice(fx.Runs, func(i, j int) bool { return fx.Runs[i].Name < fx.Runs[j].Name })
	for name, content := range files {
		p := filepath.Join(fx.Tree, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return nil, err
		}
	}
	return fx, nil
}

// flowYAML returns the generated flow.yaml of c, written as JSON, which is
// YAML that the CLI and yamlx read alike.
func (c *ExprCase) flowYAML(f *ExprFile) (string, error) {
	var x any
	switch c.SiteOrDefault() {
	case SiteStatic:
		x = map[string]any{"type": "static", "value": json.RawMessage(*c.Static)}
	case SiteSkipIf:
		x = map[string]any{"type": "static", "value": nil}
	default:
		x = map[string]any{"type": "javascript", "expr": *c.Expr}
	}
	module := map[string]any{
		"id": ExprStep,
		"value": map[string]any{
			"type":             "script",
			"path":             ExprSink,
			"input_transforms": map[string]any{ExprInput: x},
		},
	}
	if c.SiteOrDefault() == SiteSkipIf {
		module["skip_if"] = map[string]any{"expr": *c.Expr}
	}
	value := map[string]any{"modules": []any{module}}
	if env := f.Env(c); env != "" {
		value["flow_env"] = json.RawMessage(env)
	}
	doc := map[string]any{
		"summary": c.Spec + ": " + c.Title,
		"value":   value,
		"schema":  map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "properties": map[string]any{}},
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return "", fmt.Errorf("case %s: %w", c.Spec, err)
	}
	return b.String(), nil
}

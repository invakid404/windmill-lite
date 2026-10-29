package expr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The pipeline level (ADR 0012, B2a): testdata/pipeline/<finding>.json
// holds the cases of the textual pipeline for one finding. A case gives an
// expression at a site and what Compile makes of it, and either restates a
// case that recording batch E3a recorded (recorded), whose context the test
// rebuilds from the recording's case, or gives its own context. With the
// fake engine, a case checks which route answers: a shortcut's exact text,
// or the engine's call (what it binds). The engine's value is B2b's, on the
// real engine.

const (
	pipelineDir     = "testdata/pipeline"
	conformanceRoot = "../../conformance"
)

// gateFindings are the findings of the B2a exit gate: JE-5 to JE-14 (ADR
// 0012), with JE-V6 and CF-V6, whose cases are spellings of JE-8 and JE-9
// (0001 §6 gates CF-V6 on B2a). Every E3a case of these findings has a
// pipeline case.
var gateFindings = []string{"JE-5", "JE-6", "JE-7", "JE-8", "JE-9", "JE-10", "JE-11", "JE-12", "JE-13", "JE-14", "JE-V6", "CF-V6"}

type pipelineFile struct {
	Finding string         `json:"finding"`
	Cases   []pipelineCase `json:"cases"`
}

type pipelineCase struct {
	// Spec is the catalogue case, or the finding with a Variant for a case
	// beyond the catalogue.
	Spec       string `json:"spec"`
	Variant    string `json:"variant,omitempty"`
	Provenance string `json:"provenance"`
	Title      string `json:"title"`
	Note       string `json:"note,omitempty"`
	// Recorded names the E3a case the case restates.
	Recorded *recordedRef `json:"recorded,omitempty"`
	Site     SiteKind     `json:"site"`
	Text     string       `json:"text"`
	// Context is the scope of a case that restates no recording.
	Context *caseContext   `json:"context,omitempty"`
	Expect  pipelineExpect `json:"expect"`
}

// recordedRef names a context-free case (Exprs, the file's stem, and Case,
// the case's name) or a flow fixture's unit case (Fixture, its id, Run, At,
// the site path, and Input).
type recordedRef struct {
	Exprs   string `json:"exprs,omitempty"`
	Case    string `json:"case,omitempty"`
	Fixture string `json:"fixture,omitempty"`
	Run     string `json:"run,omitempty"`
	At      string `json:"at,omitempty"`
	Input   string `json:"input,omitempty"`
}

func (r recordedRef) key() string {
	if r.Exprs != "" {
		return "exprs " + r.Exprs + " " + r.Case
	}
	return "flows " + r.Fixture + " " + r.Run + " " + r.At + " " + r.Input
}

// caseContext is what an evaluation sees: previous_result's text as the
// worker holds it, the flow input as submitted (expressions read the
// worker's text of it, workerFlowInput), the flow env, and the stored texts
// a database read returns.
type caseContext struct {
	PreviousID     string            `json:"previous_id"`
	PreviousResult *string           `json:"previous_result,omitempty"`
	FlowInput      *string           `json:"flow_input,omitempty"`
	FlowEnv        *string           `json:"flow_env,omitempty"`
	Results        map[string]string `json:"results,omitempty"`
}

// pipelineExpect holds what is checked; a field left out is not.
type pipelineExpect struct {
	Trimmed  *string   `json:"trimmed,omitempty"`
	Shortcut *Shortcut `json:"shortcut,omitempty"`
	Mode     Mode      `json:"mode,omitempty"`
	Binds    *[]string `json:"binds,omitempty"`
	// Code is the rewritten code inside the source's wrapper.
	Code *string `json:"code,omitempty"`
	// RewritePanic is the byte offset at which JE-11's rewrite would close
	// a call inside a character, where Windmill's evaluator panics; such a
	// text has no code.
	RewritePanic *int `json:"rewrite_panic,omitempty"`
	// Route is "engine" or the shortcut that answers, by its name in
	// Shortcut's JSON form.
	Route *string `json:"route,omitempty"`
	// Text is a shortcut's exact output.
	Text *string `json:"text,omitempty"`
	// Bound are the host strings of the engine's call, by name, and
	// ResultsLookup whether it binds the results lookup.
	Bound         map[string]string `json:"bound,omitempty"`
	ResultsLookup *bool             `json:"results_lookup,omitempty"`
	// Engine is what the evaluation gives on the real engine, for a case
	// with its own context (TestPipelineCasesOnEngine).
	Engine *engineExpect `json:"engine,omitempty"`
}

// engineExpect is an evaluation's outcome on the real engine: its exact
// text, or its error's exact message or the start of it. In an error's
// message, <n> stands for a decimal number that differs between runs, as
// the catalogue writes the task number of JE-11/e.
type engineExpect struct {
	Text        *string `json:"text,omitempty"`
	Error       *string `json:"error,omitempty"`
	ErrorPrefix *string `json:"error_prefix,omitempty"`
}

// e3aCase is one case that recording batch E3a recorded, as the pipeline
// sees it: the expression at its site, the scope its recorded context
// gives, and what its recorded expectation says of a shortcut's answer.
type e3aCase struct {
	key        string
	spec       string
	provenance string
	site       SiteKind
	text       string
	scope      func(t *testing.T) *Scope
	// check judges a shortcut's answer against the recorded expectation.
	check func(out jsonval.Value) error
	// exprs is the context-free case, or unit the flow fixture's unit case,
	// whose whole expectation the real engine is held to (B2b).
	exprs *conformance.ExprCase
	unit  *conformance.UnitCase
	// recorded are the error messages the oracle recorded where the case's
	// evaluation can fail: its step's, then its run's.
	recorded []string
}

func storedText(t *testing.T, producer string) string {
	t.Helper()
	s, err := jsonval.FromProducer([]byte(producer)).Stored()
	if err != nil {
		t.Fatalf("%s: %v", producer, err)
	}
	return string(s)
}

// workerFlowInput is a flow input as Windmill's worker holds it, given the
// input as submitted (JE-44's table): the flow job's arguments read back
// from jsonb member by member, so a compact object whose members are each
// in jsonb's printed form, here in jsonb's key order (the worker's is a
// hash map's). It is also previous_result at a flow's first module.
func workerFlowInput(t *testing.T, submitted string) string {
	t.Helper()
	ms := objectMembers([]byte(storedText(t, submitted)))
	if ms == nil {
		t.Fatalf("flow input %s is not an object", submitted)
	}
	out := []byte{'{'}
	for i, m := range ms {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendSerdeString(out, m.key)
		out = append(out, ':')
		out = append(out, databaseText(m.value)...)
	}
	return string(append(out, '}'))
}

// siteScope builds the scope a site sees from a context, as JE-22 gives it
// for the sites the cases use: previous_result is P, and so is result except
// at an input transform, which binds params {} instead (JE-23); the flow
// input is the worker's text of it (JE-44); every site but sleep, retry_if
// and skip_expr has a results context, and a stop predicate only when its
// text reads results.
func siteScope(t *testing.T, site SiteKind, text string, c caseContext) *Scope {
	s := &Scope{PreviousID: c.PreviousID, Helpers: &fakeHelpers{}}
	transform := site == SiteInputTransform || site == SiteFailureInputTransform
	if p := c.PreviousResult; p != nil {
		s.Context = append(s.Context, Named{"previous_result", value(*p)})
		if !transform {
			s.Context = append(s.Context, Named{"result", value(*p)})
		}
	}
	if transform {
		s.Context = append(s.Context, Named{"params", value("{}")})
	}
	if c.FlowInput != nil {
		s.FlowInput = value(workerFlowInput(t, *c.FlowInput))
	}
	if c.FlowEnv != nil {
		s.FlowEnv = value(*c.FlowEnv)
	}
	switch site {
	case SiteSleep, SiteRetryIf, SiteSkipExpr:
	case SiteStopAfterIf, SiteStopAfterAllItersIf:
		if strings.Contains(text, "results.") || strings.Contains(text, "results[") || strings.Contains(text, "results?.") {
			s.Results = &fakeResults{stored: c.Results}
		}
	default:
		s.Results = &fakeResults{stored: c.Results}
	}
	return s
}

func exprsSite(c *conformance.ExprCase) SiteKind {
	switch c.SiteOrDefault() {
	case conformance.SiteTransform:
		return SiteInputTransform
	case conformance.SiteSkipIf:
		return SiteSkipIf
	}
	return 0
}

func unitSite(input string) SiteKind {
	switch input {
	case "skip_if":
		return SiteSkipIf
	case "stop_after_if":
		return SiteStopAfterIf
	case "timeout":
		return SiteTimeout
	case "sleep":
		return SiteSleep
	case "iterator":
		return SiteIterator
	}
	return SiteInputTransform
}

// loadE3a returns every expression that recording batch E3a recorded: the
// cases of conformance/exprs (static values aside, which are not
// expressions) and the unit cases of the flow fixtures.
func loadE3a(t *testing.T) map[string]*e3aCase {
	t.Helper()
	out := map[string]*e3aCase{}
	add := func(c *e3aCase) {
		if out[c.key] != nil {
			t.Fatalf("E3a case %s is given twice", c.key)
		}
		out[c.key] = c
	}
	files, err := conformance.FindExprFiles(filepath.Join(conformanceRoot, conformance.ExprDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		f, err := conformance.LoadExprFile(path)
		if err != nil {
			t.Fatal(err)
		}
		stem := strings.TrimSuffix(filepath.Base(path), ".yaml")
		for i := range f.Cases {
			c := &f.Cases[i]
			site := exprsSite(c)
			if site == 0 {
				continue
			}
			in := f.Input(c)
			prev := workerFlowInput(t, in)
			ctx := caseContext{PreviousResult: &prev, FlowInput: &in}
			if env := f.Env(c); env != "" {
				ctx.FlowEnv = &env
			}
			add(&e3aCase{
				key:        recordedRef{Exprs: stem, Case: c.Name()}.key(),
				spec:       c.Spec,
				provenance: c.Provenance,
				site:       site,
				text:       *c.Expr,
				scope:      func(t *testing.T) *Scope { return siteScope(t, site, *c.Expr, ctx) },
				check:      exprsCheck(t, c.Expect),
				exprs:      c,
				recorded:   exprsRecorded(t, path, c),
			})
		}
	}
	dirs, err := conformance.FindFixtures(filepath.Join(conformanceRoot, "flows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		fx, err := conformance.LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range fx.Runs {
			for i := range r.Unit {
				u := &r.Unit[i]
				site := unitSite(u.Input)
				ctx := caseContext{PreviousID: *u.Context.PreviousID, PreviousResult: u.Context.PreviousResult, FlowInput: u.Context.FlowInput, Results: u.Context.Results}
				if ctx.FlowInput == nil {
					// A body's flow input is not the run's arguments
					// (JE-37), so a unit case inside one must give it.
					if at, err := conformance.ParseSitePath(u.Site); err != nil || len(at.Segments()) > 1 {
						t.Fatalf("%s %s %s: a unit case inside a body needs context.flow_input", fx.ID, r.Name, u.Site)
					}
					args := string(r.ArgsOrEmpty())
					ctx.FlowInput = &args
				}
				add(&e3aCase{
					key:        recordedRef{Fixture: fx.ID, Run: r.Name, At: u.Site, Input: u.Input}.key(),
					spec:       u.Spec,
					provenance: fx.Provenance,
					site:       site,
					text:       u.Expr,
					scope:      func(t *testing.T) *Scope { return siteScope(t, site, u.Expr, ctx) },
					check:      unitCheck(u.Expect),
					unit:       u,
					recorded:   recordedMessages(r.Recorded, u.Site),
				})
			}
		}
	}
	return out
}

// exprsRecorded returns the messages recorded for a context-free case,
// from conformance/exprs/recorded/<file>/<case>.json, at its step b.
func exprsRecorded(t *testing.T, path string, c *conformance.ExprCase) []string {
	t.Helper()
	stem := strings.TrimSuffix(filepath.Base(path), ".yaml")
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), conformance.RecordedDir, stem, c.Name()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec conformance.Recording
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return recordedMessages(&rec, "m:b")
}

// recordedMessages returns the error messages of a recording: the one in
// the stored result of the step at site, then the run's own.
func recordedMessages(rec *conformance.Recording, site string) []string {
	if rec == nil {
		return nil
	}
	var out []string
	add := func(result json.RawMessage) {
		var r struct {
			Error struct {
				Message *string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(result, &r) == nil && r.Error.Message != nil {
			out = append(out, *r.Error.Message)
		}
	}
	if at, err := conformance.ParseSitePath(site); err == nil {
		if st, ok := rec.Observation.Steps[at]; ok {
			add(st.Result)
		}
	}
	add(rec.Observation.Result)
	return out
}

// exprsCheck judges a shortcut's answer against a context-free case's
// expectation: the answer's stored text is the recorded argument (point O2)
// and, where given, the answer is the evaluator's text (O1); a skip_if
// answer is the boolean that skipped the step or not, or a non-boolean that
// the strict check turns into the expected flow error (JE-24). A case that
// expects the step to fail never has a shortcut's answer: shortcuts do not
// fail.
func exprsCheck(t *testing.T, e conformance.ExprExpect) func(jsonval.Value) error {
	return func(out jsonval.Value) error {
		got := string(out.Producer())
		switch {
		case e.Value != nil:
			if e.Text != nil && got != *e.Text {
				return fmt.Errorf("the shortcut answers %s, the case's evaluator text is %s", got, *e.Text)
			}
			stored, err := out.Stored()
			if err != nil {
				return err
			}
			if string(stored) != storedText(t, *e.Value) {
				return fmt.Errorf("the shortcut answers %s, stored as %s; the recorded argument is %s", got, stored, *e.Value)
			}
			return nil
		case e.Skipped != nil:
			if want := fmt.Sprint(*e.Skipped); got != want {
				return fmt.Errorf("the shortcut answers %s, the step's skipped is %s", got, want)
			}
			return nil
		case e.FlowError != nil:
			if got == "true" || got == "false" {
				return fmt.Errorf("the shortcut answers %s, but the case expects the flow to fail", got)
			}
			if !mentions(e.FlowError, "Expected a boolean value") {
				return fmt.Errorf("the shortcut answers the non-boolean %s, but the expected flow error is not the strict check's", got)
			}
			return nil
		}
		return fmt.Errorf("the shortcut answers %s, but the case expects the step to fail", got)
	}
}

func mentions(m *conformance.MessageMatch, s string) bool {
	if m.Message != nil && strings.Contains(*m.Message, s) || m.Prefix != nil && strings.Contains(*m.Prefix, s) {
		return true
	}
	return slices.ContainsFunc(m.Contains, func(c string) bool { return strings.Contains(c, s) })
}

// unitCheck judges a shortcut's answer against a unit case's expectation,
// the exact evaluator text (O1).
func unitCheck(e conformance.UnitExpect) func(jsonval.Value) error {
	return func(out jsonval.Value) error {
		got := string(out.Producer())
		if e.Text == nil {
			return fmt.Errorf("the shortcut answers %s, but the case expects an error", got)
		}
		if got != *e.Text {
			return fmt.Errorf("the shortcut answers %q, the case expects %q", got, *e.Text)
		}
		return nil
	}
}

func loadPipelineFiles(t *testing.T) []*pipelineFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(pipelineDir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no pipeline cases in %s: %v", pipelineDir, err)
	}
	var out []*pipelineFile
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f := &pipelineFile{}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(f); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if stem := strings.TrimSuffix(filepath.Base(p), ".json"); stem != strings.ToLower(f.Finding) {
			t.Fatalf("%s: a file of finding %s is named %s.json", p, f.Finding, strings.ToLower(f.Finding))
		}
		out = append(out, f)
	}
	return out
}

func (c *pipelineCase) name() string {
	n := c.Spec
	if c.Variant != "" {
		n += " " + c.Variant
	}
	return n
}

// TestPipelineCases runs every pipeline case: Compile's output, and, on the
// fake engine, the route that answers, a shortcut's text and the engine's
// bindings. A case that restates an E3a case takes its expression, site,
// spec, provenance and context from the recording's case, and a shortcut's
// answer is also judged against the recorded expectation.
func TestPipelineCases(t *testing.T) {
	e3a := loadE3a(t)
	for _, f := range loadPipelineFiles(t) {
		for i := range f.Cases {
			c := &f.Cases[i]
			t.Run(c.name(), func(t *testing.T) {
				if c.Spec != f.Finding && !strings.HasPrefix(c.Spec, f.Finding+"/") {
					t.Fatalf("case %s is in the file of %s", c.Spec, f.Finding)
				}
				if c.Title == "" || !c.Site.Valid() {
					t.Fatal("a case needs a title and a site")
				}
				if !strings.Contains(c.Spec, "/") && c.Variant == "" {
					t.Fatalf("case %s cites a finding, not a catalogue case, so it needs a variant", c.Spec)
				}
				var scope func(t *testing.T) *Scope
				var rec *e3aCase
				switch {
				case c.Recorded != nil && c.Context != nil:
					t.Fatal("a recorded case takes its context from the recording")
				case c.Recorded != nil:
					rec = e3a[c.Recorded.key()]
					if rec == nil {
						t.Fatalf("no E3a case %s", c.Recorded.key())
					}
					if rec.text != c.Text || rec.site != c.Site || rec.spec != c.Spec || rec.provenance != c.Provenance {
						t.Fatalf("the case says %s %q at %v (%s); E3a recorded %s %q at %v (%s)", c.Spec, c.Text, c.Site, c.Provenance, rec.spec, rec.text, rec.site, rec.provenance)
					}
					// A restatement says which route answers, and a
					// shortcut's text, so the gate cannot pass empty.
					if w := c.Expect.Route; w == nil || (*w != "engine" && c.Expect.Text == nil) {
						t.Fatal("a restated case gives its route, and a shortcut's text")
					}
					scope = rec.scope
				case c.Context != nil:
					if c.Provenance != "source-derived" {
						t.Fatalf("a case that restates no recording is source-derived, not %s", c.Provenance)
					}
					scope = func(t *testing.T) *Scope { return siteScope(t, c.Site, c.Text, *c.Context) }
				default:
					if c.Provenance != "source-derived" {
						t.Fatalf("a case that restates no recording is source-derived, not %s", c.Provenance)
					}
					if c.Expect.Route != nil || c.Expect.Text != nil || c.Expect.Bound != nil || c.Expect.ResultsLookup != nil {
						t.Fatal("a case without a context checks Compile only")
					}
				}
				e := Compile(c.Site, c.Text)
				checkCompiled(t, e, c.Expect)
				if scope == nil {
					return
				}
				eng := &fakeEngine{}
				p := NewPipeline(eng)
				out, err := p.Eval(context.Background(), e, scope(t))
				if e.RewritePanic != 0 {
					// The rewrite panics before anything is bound (JE-11).
					if _, called := eng.lastCall(); called || !errors.Is(err, ErrRewritePanic) {
						t.Fatalf("a text whose rewrite panics: %v, engine called %v", err, called)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				_, r := shortcut(context.Background(), e, scope(t))
				call, called := eng.lastCall()
				if called != (r == routeEngine) {
					t.Fatalf("route %q, but the engine was called: %v", r, called)
				}
				got := string(r)
				if r == routeEngine {
					got = "engine"
				}
				if w := c.Expect.Route; w != nil && *w != got {
					t.Errorf("route %s, want %s", got, *w)
				}
				if r != routeEngine {
					if w := c.Expect.Text; w != nil && string(out.Producer()) != *w {
						t.Errorf("text %q, want %q", out.Producer(), *w)
					}
					if rec != nil {
						if err := rec.check(out); err != nil {
							t.Errorf("against the recording: %v", err)
						}
					}
					if c.Expect.Bound != nil || c.Expect.ResultsLookup != nil {
						t.Error("the case expects the engine's bindings, but a shortcut answered")
					}
					return
				}
				if string(out.Producer()) != fakeAnswer {
					t.Errorf("the engine answered %s, the pipeline returned %s", fakeAnswer, out.Producer())
				}
				if call.source != programSource(e) {
					t.Errorf("the engine ran %q, the expression's program is %q", call.source, programSource(e))
				}
				if c.Expect.Text != nil {
					t.Error("the case expects a shortcut's text, but the engine answered")
				}
				if w := c.Expect.Bound; w != nil {
					if g := call.strings(); !maps.Equal(g, w) {
						t.Errorf("bound %v, want %v", g, w)
					}
				}
				if w := c.Expect.ResultsLookup; w != nil && (call.fn(hostResults) != nil) != *w {
					t.Errorf("results lookup bound: %v, want %v", !*w, *w)
				}
				for _, h := range []string{hostVariable, hostResource, hostFlowUserState} {
					if call.fn(h) == nil {
						t.Errorf("helper %s not bound", h)
					}
				}
			})
		}
	}
}

// TestPipelineCasesOnEngine runs the pipeline cases that say what the real
// engine gives (expect.engine), each in its own context, and holds the
// evaluation to it: the cases of the findings whose program the semantics
// reference corrected after B2b (JE-10, JE-15 to JE-17, JE-21, JE-50 to
// JE-52), among them catalogue cases no recording holds yet.
func TestPipelineCasesOnEngine(t *testing.T) {
	p := NewPipeline(realEngine(t, DefaultLimits(), 1))
	n := 0
	for _, f := range loadPipelineFiles(t) {
		for i := range f.Cases {
			c := &f.Cases[i]
			w := c.Expect.Engine
			if w == nil {
				continue
			}
			n++
			t.Run(c.name(), func(t *testing.T) {
				if c.Context == nil {
					t.Fatal("a case run on the engine gives its own context")
				}
				v, err := p.Eval(context.Background(), Compile(c.Site, c.Text), siteScope(t, c.Site, c.Text, *c.Context))
				got := string(v.Producer())
				switch {
				case w.Text != nil:
					if err != nil || got != *w.Text {
						t.Errorf("%s, %v; want %s", got, err, *w.Text)
					}
				case w.Error != nil:
					if err == nil || !numberedMatch(*w.Error, err.Error()) {
						t.Errorf("%s, %q; want the error %q", got, err, *w.Error)
					}
				case w.ErrorPrefix != nil:
					if err == nil || !strings.HasPrefix(err.Error(), *w.ErrorPrefix) {
						t.Errorf("%s, %q; want an error starting %q", got, err, *w.ErrorPrefix)
					}
				default:
					t.Fatal("expect.engine gives no outcome")
				}
			})
		}
	}
	if n < 15 {
		t.Errorf("%d pipeline cases ran on the engine; the cases have gone missing", n)
	}
}

// numberedMatch reports whether got is want with each <n> in want standing
// for a decimal number.
func numberedMatch(want, got string) bool {
	parts := strings.Split(want, "<n>")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`^` + strings.Join(parts, `[0-9]+`) + `$`).MatchString(got)
}

func checkCompiled(t *testing.T, e *Expr, w pipelineExpect) {
	t.Helper()
	if w.Trimmed != nil && e.Trimmed != *w.Trimmed {
		t.Errorf("trimmed %q, want %q", e.Trimmed, *w.Trimmed)
	}
	if w.Shortcut != nil {
		g, _ := json.Marshal(e.Shortcut)
		want, _ := json.Marshal(w.Shortcut)
		if !bytes.Equal(g, want) {
			t.Errorf("shortcut %s, want %s", g, want)
		}
	}
	if w.Mode != 0 && e.Mode != w.Mode {
		t.Errorf("mode %v, want %v", e.Mode, w.Mode)
	}
	if w.Binds != nil && !slices.Equal(e.Binds, *w.Binds) {
		t.Errorf("binds %q, want %q", e.Binds, *w.Binds)
	}
	if w.RewritePanic != nil && e.RewritePanic != *w.RewritePanic {
		t.Errorf("rewrite panic at %d, want %d", e.RewritePanic, *w.RewritePanic)
	}
	if e.RewritePanic != 0 {
		if e.Source != "" || w.Code != nil {
			t.Errorf("a rewrite that panics gives no code: source %q, want code %v", e.Source, w.Code)
		}
		return
	}
	code, ok := unwrap(e.Mode, e.Source)
	if !ok {
		t.Fatalf("source %q is not in the wrapper of %v", e.Source, e.Mode)
	}
	if w.Code != nil && code != *w.Code {
		t.Errorf("code %q, want %q", code, *w.Code)
	}
}

// TestPipelineCoverage holds the exit gate: every case of the gate's
// findings that E3a recorded, as a context-free case or a unit case of a
// flow fixture, is restated by exactly one pipeline case; every catalogue
// case of those findings that an E3a fixture, run or case cites is the spec
// of a restatement, so a run without its unit case cannot drop out of the
// gate; and every gate finding has a restatement.
func TestPipelineCoverage(t *testing.T) {
	e3a := loadE3a(t)
	restated := map[string]string{}
	restatedSpecs := map[string]bool{}
	for _, f := range loadPipelineFiles(t) {
		for _, c := range f.Cases {
			if c.Recorded == nil {
				continue
			}
			k := c.Recorded.key()
			if prev, ok := restated[k]; ok {
				t.Errorf("%s is restated by %s and %s", k, prev, c.name())
			}
			restated[k] = c.name()
			restatedSpecs[c.Spec] = true
		}
	}
	inGate := func(spec string) bool {
		finding, _, _ := strings.Cut(spec, "/")
		return slices.Contains(gateFindings, finding)
	}
	n := 0
	for _, k := range slices.Sorted(maps.Keys(e3a)) {
		c := e3a[k]
		if !inGate(c.spec) {
			continue
		}
		n++
		if _, ok := restated[k]; !ok {
			t.Errorf("E3a case %s (%s) has no pipeline case", k, c.spec)
		}
	}
	cited := map[string]string{}
	dirs, err := conformance.FindFixtures(filepath.Join(conformanceRoot, "flows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		fx, err := conformance.LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, sp := range fx.Spec {
			cited[sp] = fx.ID
		}
		for _, r := range fx.Runs {
			for _, sp := range r.Spec {
				cited[sp] = fx.ID + " " + r.Name
			}
		}
	}
	for _, c := range e3a {
		cited[c.spec] = c.key
	}
	for _, sp := range slices.Sorted(maps.Keys(cited)) {
		if inGate(sp) && strings.Contains(sp, "/") && !restatedSpecs[sp] {
			t.Errorf("%s cites %s, which no pipeline case restates", cited[sp], sp)
		}
	}
	for _, f := range gateFindings {
		if !slices.ContainsFunc(slices.Collect(maps.Keys(restatedSpecs)), func(sp string) bool { return sp == f || strings.HasPrefix(sp, f+"/") }) {
			t.Errorf("no pipeline case restates a case of %s", f)
		}
	}
	t.Logf("%d E3a cases of the gate's findings, %d restated", n, len(restated))
}

// TestRecordedCasesThroughPipeline runs every context-free case and every
// unit case that E3a recorded, of every finding, through the pipeline on
// the fake engine: whenever a shortcut answers, its answer must be what the
// oracle recorded, so no shortcut fires where Windmill ran JavaScript or
// failed. A flow fixture's expressions that no unit case restates are not
// run here.
func TestRecordedCasesThroughPipeline(t *testing.T) {
	e3a := loadE3a(t)
	shortcuts := map[route]int{}
	for _, k := range slices.Sorted(maps.Keys(e3a)) {
		c := e3a[k]
		e := Compile(c.site, c.text)
		eng := &fakeEngine{}
		out, err := NewPipeline(eng).Eval(context.Background(), e, c.scope(t))
		if err != nil {
			t.Errorf("%s: %v", k, err)
			continue
		}
		if _, called := eng.lastCall(); called {
			shortcuts[routeEngine]++
			continue
		}
		_, r := shortcut(context.Background(), e, c.scope(t))
		shortcuts[r]++
		if err := c.check(out); err != nil {
			t.Errorf("%s (%s, %q): %v", k, c.spec, c.text, err)
		}
	}
	t.Logf("%d recorded expressions: %d reach the engine, shortcuts %v", len(e3a), shortcuts[routeEngine], shortcuts)
}

// TestJE8KeyOrderExample keeps the raw-text assertion of ADR 0003's JE-8
// example (review item cx R16). Step p returns {"b":1,"a":2}; the next
// step's results.p is the previous-step shortcut, which answers p's
// producer text byte for byte, not its stored jsonb order. The recorded
// argument cannot tell the two apart, since the job API stores it as jsonb;
// the timeout site's parse error quotes the evaluated text, so the oracle
// shows the producer's order there, and the jsonb text of the database
// route when a step sits in between.
func TestJE8KeyOrderExample(t *testing.T) {
	fx, err := conformance.LoadFixture(filepath.Join(conformanceRoot, "flows", "je", "shortcuts"))
	if err != nil {
		t.Fatal(err)
	}
	runs := map[string]conformance.Run{}
	for _, r := range fx.Runs {
		runs[r.Name] = r
	}
	eval := func(run string) (string, conformance.Run) {
		t.Helper()
		r, ok := runs[run]
		if !ok || len(r.Unit) != 1 || r.Recorded == nil {
			t.Fatalf("run %s: want one unit case and a recording", run)
		}
		u := r.Unit[0]
		site := unitSite(u.Input)
		e := Compile(site, u.Expr)
		s := siteScope(t, site, u.Expr, caseContext{PreviousID: *u.Context.PreviousID, PreviousResult: u.Context.PreviousResult, Results: u.Context.Results})
		eng := &fakeEngine{}
		out, err := NewPipeline(eng).Eval(context.Background(), e, s)
		if err != nil {
			t.Fatal(err)
		}
		if _, called := eng.lastCall(); called {
			t.Fatalf("run %s reached the engine", run)
		}
		return string(out.Producer()), r
	}

	// The input transform: the producer text, whose stored form is the
	// recorded argument, which holds the keys in jsonb's order.
	got, r := eval("je8-order-transform")
	if got != `{"b":1,"a":2}` {
		t.Errorf("results.p after p answers %s, want the producer text {\"b\":1,\"a\":2}", got)
	}
	q, err := conformance.ParseSitePath("m:q")
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(r.Recorded.Observation.Steps[q].Args, &args); err != nil {
		t.Fatal(err)
	}
	var arg bytes.Buffer
	if err := json.Compact(&arg, args["x"]); err != nil {
		t.Fatal(err)
	}
	if arg.String() != `{"a":2,"b":1}` || storedText(t, got) != arg.String() {
		t.Errorf("the recorded argument is %s; want {\"a\":2,\"b\":1}, the stored form of %s", arg.String(), got)
	}

	// The timeout site: the oracle's error quotes the same text, keys in
	// the producer's order.
	got, r = eval("je8-order-timeout")
	if got != `{"b":1,"a":2}` {
		t.Errorf("at the timeout site results.p answers %s", got)
	}
	if msg := recordedMessage(t, r); !strings.Contains(msg, "Value was: "+got) {
		t.Errorf("the oracle's error %q does not quote %s", msg, got)
	}

	// The control: with a step in between, results.p is the JE-9 lookup,
	// which answers jsonb's text output, and the oracle quotes that.
	got, r = eval("je8-order-timeout-db")
	if got != `{"a": 2, "b": 1}` {
		t.Errorf("results.p through the database answers %s, want jsonb's text {\"a\": 2, \"b\": 1}", got)
	}
	if msg := recordedMessage(t, r); !strings.Contains(msg, "Value was: "+got) {
		t.Errorf("the oracle's error %q does not quote %s", msg, got)
	}
}

// recordedMessage returns the message of a run's recorded error result.
func recordedMessage(t *testing.T, r conformance.Run) string {
	t.Helper()
	var res struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Recorded.Observation.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res.Error.Message
}

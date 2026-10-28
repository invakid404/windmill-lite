package oracle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/openflow"
)

// The recording batches (ADR 0012, E3a to E3e) commit oracle recordings of
// the expression cases under conformance/exprs and of the flow fixtures
// under conformance/flows. These tests hold the committed recordings to
// what the cases say.

var exprsDir = filepath.Join("..", "..", "conformance", conformance.ExprDirName)

func loadExprFiles(t *testing.T) []*conformance.ExprFile {
	t.Helper()
	paths, err := conformance.FindExprFiles(exprsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no expression files")
	}
	var files []*conformance.ExprFile
	for _, p := range paths {
		f, err := conformance.LoadExprFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

// TestExprFilesLift lifts every expression file and reads the generated
// tree back as the walker does (yamlx and the effective decoder), so a
// generated flow is the flow its case describes.
func TestExprFilesLift(t *testing.T) {
	for _, f := range loadExprFiles(t) {
		t.Run(filepath.Base(f.Path), func(t *testing.T) {
			fx, err := conformance.LiftExprFile(f, filepath.Join(t.TempDir(), "lifted"))
			if err != nil {
				t.Fatal(err)
			}
			tree, err := LoadSyncTree(fx.SyncTree())
			if err != nil {
				t.Fatal(err)
			}
			if len(tree.Flows) != len(f.Cases) || len(fx.Runs) != len(f.Cases) {
				t.Fatalf("%d flows and %d runs for %d cases", len(tree.Flows), len(fx.Runs), len(f.Cases))
			}
			for i := range f.Cases {
				c := &f.Cases[i]
				flow := tree.Flows[c.FlowPath()]
				if flow == nil {
					t.Fatalf("%s: no flow %s", c.Name(), c.FlowPath())
				}
				mods := flow.Value.Modules
				if len(mods) != 1 || mods[0].ID != conformance.ExprStep {
					t.Fatalf("%s: modules %+v", c.Name(), mods)
				}
				ps, ok := mods[0].Value.(*openflow.PathScript)
				if !ok || ps.Path != conformance.ExprSink {
					t.Fatalf("%s: module value %#v", c.Name(), mods[0].Value)
				}
				x, ok := ps.InputTransforms.Get(conformance.ExprInput)
				if !ok {
					t.Fatalf("%s: no transform %s", c.Name(), conformance.ExprInput)
				}
				switch c.SiteOrDefault() {
				case conformance.SiteTransform:
					if x.Type != openflow.JavaScript || x.Expr != *c.Expr {
						t.Errorf("%s: transform %+v, want javascript %q", c.Name(), x, *c.Expr)
					}
				case conformance.SiteSkipIf:
					if mods[0].SkipIf == nil || mods[0].SkipIf.Expr != *c.Expr {
						t.Errorf("%s: skip_if %+v, want %q", c.Name(), mods[0].SkipIf, *c.Expr)
					}
				case conformance.SiteStatic:
					same, err := conformance.SameJSON(x.Value, []byte(*c.Static), true)
					if err != nil || !same || x.Type != openflow.Static {
						t.Errorf("%s: static %+v, want %s", c.Name(), x, *c.Static)
					}
				}
			}
		})
	}
}

var flowsDir = filepath.Join("..", "..", "conformance", "flows")

// batchFixtures returns the flow fixtures and the lifted expression files,
// with their committed recordings.
func batchFixtures(t *testing.T) []*conformance.Fixture {
	t.Helper()
	dirs, err := conformance.FindFixtures(flowsDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []*conformance.Fixture
	for _, d := range dirs {
		f, err := conformance.LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	for _, ef := range loadExprFiles(t) {
		f, err := conformance.LiftExprFile(ef, filepath.Join(t.TempDir(), "lifted"))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// TestBatchFixturesDecode reads every flow fixture's sync tree as the
// walker does and requires each run's target to be one of its flows, so a
// fixture never reaches the oracle with a flow its walk cannot name.
func TestBatchFixturesDecode(t *testing.T) {
	dirs, err := conformance.FindFixtures(flowsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		f, err := conformance.LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := LoadSyncTree(f.SyncTree())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range f.Runs {
			if !r.IsScript() && tree.flow(r.Target) == nil {
				t.Errorf("%s run %s: target %s is not a flow of the tree", d, r.Name, r.Target)
			}
		}
	}
}

// TestRecordingsSatisfyClaims holds every committed recording to the
// claims and delays of its run: what the catalogue says of the case. A run
// that states claims, delays or unit cases must have a recording, and a
// recorded case is oracle-observed (ADR 0001 section 6, ADR 0003).
func TestRecordingsSatisfyClaims(t *testing.T) {
	for _, f := range batchFixtures(t) {
		for _, r := range f.Runs {
			name := filepath.ToSlash(f.Dir) + " " + r.Name
			if len(r.Claims)+len(r.Delays)+len(r.Unit) == 0 {
				continue
			}
			if r.Recorded == nil {
				t.Errorf("%s: no recording", name)
				continue
			}
			failed, err := conformance.CheckClaims(r.Recorded.Observation, r.Claims)
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			for _, msg := range failed {
				t.Errorf("%s: claim %s", name, msg)
			}
			if len(r.Delays) > 0 {
				var p Provenance
				if err := json.Unmarshal(r.Recorded.Provenance, &p); err != nil {
					t.Errorf("%s: provenance: %v", name, err)
					continue
				}
				for _, d := range r.Delays {
					if err := checkDelay(p.Timings, d); err != nil {
						t.Errorf("%s: %v", name, err)
					}
				}
			}
		}
		if !f.Generated && allRecorded(f) && f.Provenance != "oracle-observed" {
			t.Errorf("%s: every run is recorded, so its provenance is oracle-observed, not %s", f.Dir, f.Provenance)
		}
	}
}

func allRecorded(f *conformance.Fixture) bool {
	for _, r := range f.Runs {
		if r.Recorded == nil {
			return false
		}
	}
	return true
}

func checkDelay(tm RunTimings, d conformance.Delay) error {
	from, ok1 := tm.Jobs[d.From]
	to, ok2 := tm.Jobs[d.To]
	if !ok1 || !ok2 {
		return fmt.Errorf("delay %s to %s: the recording times no such job", d.From, d.To)
	}
	done, err1 := time.Parse(time.RFC3339Nano, from.CompletedAt)
	start, err2 := time.Parse(time.RFC3339Nano, to.StartedAt)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("delay %s to %s: unreadable times %q, %q", d.From, d.To, from.CompletedAt, to.StartedAt)
	}
	if gap := start.Sub(done); gap < time.Duration(d.AtLeastMS)*time.Millisecond {
		return fmt.Errorf("delay %s to %s: %v, want at least %d ms", d.From, d.To, gap, d.AtLeastMS)
	}
	return nil
}

// TestExprCasesAgreeWithRecordings checks each expression case against the
// recording of its generated flow: the case's expectation holds (through
// its claims, above), a recorded case is oracle-observed, and an expected
// evaluator text (O1) has the recorded argument as its stored form.
func TestExprCasesAgreeWithRecordings(t *testing.T) {
	for _, ef := range loadExprFiles(t) {
		f, err := conformance.LiftExprFile(ef, filepath.Join(t.TempDir(), "lifted"))
		if err != nil {
			t.Fatal(err)
		}
		runs := map[string]conformance.Run{}
		for _, r := range f.Runs {
			runs[r.Name] = r
		}
		for i := range ef.Cases {
			c := &ef.Cases[i]
			r := runs[c.Name()]
			name := filepath.Base(ef.Path) + " " + c.Name()
			if r.Recorded == nil {
				t.Errorf("%s: no recording", name)
				continue
			}
			if c.Provenance != "oracle-observed" {
				t.Errorf("%s: recorded, so its provenance is oracle-observed, not %s", name, c.Provenance)
			}
			if c.Expect.Text != nil {
				got, err := conformance.ValueAt(r.Recorded.Observation, "/steps/m:b/args/x")
				if err != nil || got == nil {
					t.Errorf("%s: no recorded argument x", name)
					continue
				}
				if err := sameStored(*c.Expect.Text, got); err != nil {
					t.Errorf("%s: expect.text: %v", name, err)
				}
			}
		}
	}
}

// sameStored reports whether text's jsonb stored form is the recorded
// value, members in order, numbers as written.
func sameStored(text string, recorded []byte) error {
	stored, err := jsonval.FromProducer([]byte(text)).Stored()
	if err != nil {
		return err
	}
	same, err := conformance.SameJSON(stored, recorded, true)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("stored form %s, recorded %s", stored, recorded)
	}
	return nil
}

// TestUnitCasesMatchRecordings ties each unit case of a flow fixture to
// its flow and its recording, so that the expression milestones check the
// pipeline with the context the oracle's run had: the expression is the
// one at the site, the previous id is the module before it, the previous
// result's and the database reads' stored forms are the recorded results,
// and, at an input transform, the expected text's stored form is the
// recorded argument and an expected error is the recorded one.
func TestUnitCasesMatchRecordings(t *testing.T) {
	dirs, err := conformance.FindFixtures(flowsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		f, err := conformance.LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := LoadSyncTree(f.SyncTree())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range f.Runs {
			for _, u := range r.Unit {
				name := filepath.ToSlash(d) + " " + r.Name + " " + u.Spec + " " + u.Site + " " + u.Input
				if err := checkUnit(tree, r, u); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}
		}
	}
}

func checkUnit(tree *SyncTree, r conformance.Run, u conformance.UnitCase) error {
	flow := tree.flow(r.Target)
	if flow == nil {
		return fmt.Errorf("no flow %s", r.Target)
	}
	site, err := conformance.ParseSitePath(u.Site)
	if err != nil {
		return err
	}
	segs := site.Segments()
	if last := segs[len(segs)-1]; last.Kind() != conformance.KindModule {
		// A failure module's previous id is the failed module's (JE-35),
		// which a site path alone does not give.
		return fmt.Errorf("unit cases at %s are not supported: the context of a slot is not checked", site)
	}
	list, idx, err := moduleAt(flow.Value, site)
	if err != nil {
		return err
	}
	m := list[idx]
	expr, transform, err := expressionAt(m, u.Input)
	if err != nil {
		return err
	}
	if expr != u.Expr {
		return fmt.Errorf("the flow's expression is %q, the case's %q", expr, u.Expr)
	}
	prev := ""
	if idx > 0 {
		prev = list[idx-1].ID
	}
	if *u.Context.PreviousID != prev {
		return fmt.Errorf("previous id %q, but the module before the site is %q", *u.Context.PreviousID, prev)
	}
	if r.Recorded == nil {
		return errors.New("no recording")
	}
	obs := r.Recorded.Observation
	parent := segs[:len(segs)-1]
	args, err := jsonval.FromProducer(r.ArgsOrEmpty()).Stored()
	if err != nil {
		return err
	}
	sibling := func(id string) string {
		return conformance.SitePath{}.Child(parent...).Child(conformance.Module(id)).String()
	}
	if t := u.Context.FlowInput; t != nil {
		if len(parent) > 0 {
			return errors.New("flow_input inside a body is the body's input, which a recording does not hold")
		}
		if err := sameStored(*t, args); err != nil {
			return fmt.Errorf("flow_input: %v", err)
		}
	}
	if p := u.Context.PreviousResult; p != nil {
		// At a flow's first module previous_result is its input, which the
		// job API stored as jsonb before any expression read it (JE-44).
		want := []byte(args)
		if prev != "" {
			step, ok := obs.Steps[mustSite(sibling(prev))]
			if !ok {
				return fmt.Errorf("no recorded step %s", sibling(prev))
			}
			want = step.Result
		} else if len(parent) > 0 {
			return errors.New("previous_result at the first module of a body is the body's input, which a recording does not hold")
		}
		if err := sameStored(*p, want); err != nil {
			return fmt.Errorf("previous_result: %v", err)
		}
	}
	for id, text := range u.Context.Results {
		step, ok := obs.Steps[mustSite(sibling(id))]
		if !ok {
			return fmt.Errorf("no recorded step %s", sibling(id))
		}
		if err := sameStored(text, step.Result); err != nil {
			return fmt.Errorf("results.%s: %v", id, err)
		}
	}
	if !transform {
		return nil
	}
	step, ok := obs.Steps[site]
	if !ok {
		return fmt.Errorf("no recorded step %s", site)
	}
	switch {
	case u.Expect.Text != nil:
		got, err := conformance.ValueAt(conformance.Observation{Status: obs.Status, Result: obs.Result, Steps: map[conformance.SitePath]conformance.StepObservation{site: step}},
			"/steps/"+strings.ReplaceAll(site.String(), "/", "~1")+"/args/"+u.Input)
		if err != nil || got == nil {
			return fmt.Errorf("no recorded argument %s", u.Input)
		}
		return sameStored(*u.Expect.Text, got)
	case u.Expect.Error != nil:
		failed, err := conformance.CheckClaims(obs, u.Expect.Error.Claims("/steps/"+strings.ReplaceAll(site.String(), "/", "~1")+"/result/error"))
		if err != nil {
			return err
		}
		if len(failed) > 0 {
			return fmt.Errorf("the recorded error does not match: %s", strings.Join(failed, "; "))
		}
	}
	return nil
}

func mustSite(s string) conformance.SitePath {
	p, err := conformance.ParseSitePath(s)
	if err != nil {
		panic(err)
	}
	return p
}

// moduleAt finds the module a site path names in a flow value, and returns
// its module list and index. A path alternates module segments with the
// iteration or branch segment of the container it enters; s:failure names
// the failure module.
func moduleAt(v *openflow.FlowValue, site conformance.SitePath) ([]openflow.Module, int, error) {
	segs := site.Segments()
	if len(segs) == 1 && segs[0].Kind() == conformance.KindSlot && segs[0].Value() == "failure" && v.FailureModule != nil {
		return []openflow.Module{*v.FailureModule}, 0, nil
	}
	list := v.Modules
	for i := 0; i < len(segs); i += 2 {
		if segs[i].Kind() != conformance.KindModule {
			return nil, 0, fmt.Errorf("site %s: segment %s is not a module", site, segs[i])
		}
		j := -1
		for k := range list {
			if list[k].ID == segs[i].Value() {
				j = k
				break
			}
		}
		if j < 0 {
			return nil, 0, fmt.Errorf("site %s names no module of the flow", site)
		}
		if i == len(segs)-1 {
			return list, j, nil
		}
		next := segs[i+1]
		n, indexed := next.Index()
		switch c := list[j].Value.(type) {
		case *openflow.ForLoop:
			list = c.Modules
		case *openflow.WhileLoop:
			list = c.Modules
		case *openflow.BranchOne:
			switch {
			case next.Kind() == conformance.KindBranch && next.Value() == "default":
				list = c.Default
			case indexed && n < len(c.Branches):
				list = c.Branches[n].Modules
			default:
				return nil, 0, fmt.Errorf("site %s: no branch %s", site, next)
			}
		case *openflow.BranchAll:
			if !indexed || n >= len(c.Branches) {
				return nil, 0, fmt.Errorf("site %s: no branch %s", site, next)
			}
			list = c.Branches[n].Modules
		default:
			return nil, 0, fmt.Errorf("site %s: %s is not a loop or a branch", site, list[j].ID)
		}
	}
	return nil, 0, fmt.Errorf("site %s does not end in a module", site)
}

// expressionAt returns the expression of a module's input, and whether the
// input is an input transform (whose value the job records as an argument).
func expressionAt(m openflow.Module, input string) (string, bool, error) {
	var it *openflow.InputTransform
	switch input {
	case "skip_if":
		if m.SkipIf != nil {
			return m.SkipIf.Expr, false, nil
		}
	case "stop_after_if":
		if m.StopAfterIf != nil {
			return m.StopAfterIf.Expr, false, nil
		}
	case "timeout":
		it = m.Timeout
	case "sleep":
		it = m.Sleep
	default:
		var ts openflow.Transforms
		switch v := m.Value.(type) {
		case *openflow.PathScript:
			ts = v.InputTransforms
		case *openflow.RawScript:
			ts = v.InputTransforms
		case *openflow.PathFlow:
			ts = v.InputTransforms
		}
		if x, ok := ts.Get(input); ok && x.Type == openflow.JavaScript {
			return x.Expr, true, nil
		}
	}
	if it != nil && it.Type == openflow.JavaScript {
		return it.Expr, false, nil
	}
	return "", false, fmt.Errorf("module %s has no javascript %s", m.ID, input)
}

// TestBatchCoverage accounts for every JE case of the conformance
// catalogue and every CF-19 and CF-V6 case (the batch of ADR 0012's E3a):
// each is the spec of an expression case or of a flow fixture's run, or is
// deferred with a reason and the milestone that checks it
// (conformance/exprs/deferred.yaml).
func TestBatchCoverage(t *testing.T) {
	root := repoRoot(t)
	catalogue, err := os.ReadFile(filepath.Join(root, "docs/semantics/conformance.md"))
	if err != nil {
		t.Fatal(err)
	}
	caseLine := regexp.MustCompile(`(?m)^- \*\*((?:JE-V?[0-9]+|CF-19|CF-V6)/[a-z])\*\*`)
	inCatalogue := map[string]bool{}
	var cases []string
	for _, m := range caseLine.FindAllSubmatch(catalogue, -1) {
		cases = append(cases, string(m[1]))
		inCatalogue[string(m[1])] = true
	}
	if len(cases) < 150 {
		t.Fatalf("found only %d JE, CF-19 and CF-V6 cases in the catalogue", len(cases))
	}
	encoded := map[string]bool{}
	for _, f := range loadExprFiles(t) {
		for _, c := range f.Cases {
			encoded[c.Spec] = true
		}
	}
	dirs, err := conformance.FindFixtures(flowsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		f, err := conformance.LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range f.Runs {
			for _, s := range r.Spec {
				encoded[s] = true
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(exprsDir, conformance.ExprDeferredFile))
	if err != nil {
		t.Fatal(err)
	}
	j, err := conformance.YAMLToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Deferred []struct {
			Case, Reason string
			CheckedIn    string `json:"checked_in"`
		} `json:"deferred"`
		Partial []struct {
			Case, Recorded, Remaining string
			CheckedIn                 string `json:"checked_in"`
		} `json:"partial"`
	}
	dec := json.NewDecoder(strings.NewReader(string(j)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil {
		t.Fatal(err)
	}
	deferred := map[string]bool{}
	for _, d := range list.Deferred {
		switch {
		case !inCatalogue[d.Case]:
			t.Errorf("deferred.yaml lists %s, which the catalogue does not have", d.Case)
		case d.Reason == "" || d.CheckedIn == "":
			t.Errorf("%s: a deferred case needs a reason and checked_in", d.Case)
		case encoded[d.Case]:
			t.Errorf("%s is encoded and also deferred", d.Case)
		case deferred[d.Case]:
			t.Errorf("%s is deferred twice", d.Case)
		}
		deferred[d.Case] = true
	}
	for _, p := range list.Partial {
		if !encoded[p.Case] || p.Recorded == "" || p.Remaining == "" || p.CheckedIn == "" {
			t.Errorf("%s: a partial case must be encoded and say what is recorded, what remains and where", p.Case)
		}
	}
	for _, c := range cases {
		if !encoded[c] && !deferred[c] {
			t.Errorf("%s is neither encoded nor listed in conformance/exprs/deferred.yaml", c)
		}
	}
}

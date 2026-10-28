package oracle

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/openflow"
)

const flowsRoot = "../../conformance/flows"

func loadFlowFixtures(t *testing.T) []*conformance.Fixture {
	t.Helper()
	dirs, err := conformance.FindFixtures(flowsRoot)
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
	if len(out) == 0 {
		t.Fatal("no flow fixtures")
	}
	return out
}

// TestFlowFixtureTreesResolve reads every fixture's sync tree as the
// walker does (yamlx, then Windmill's effective decoder) and requires that
// each run's target, and every flow or script a module names by path, is
// in the same tree: the walker names sites from these definitions, so a
// missing one would only show as a failed oracle run.
func TestFlowFixtureTreesResolve(t *testing.T) {
	// spec: OR-15, OR-11
	for _, f := range loadFlowFixtures(t) {
		tree, err := LoadSyncTree(f.SyncTree())
		if err != nil {
			t.Errorf("%s: %v", f.ID, err)
			continue
		}
		hasScript := func(p string) bool {
			_, err := os.Stat(filepath.Join(f.SyncTree(), filepath.FromSlash(p)+".ts"))
			return err == nil
		}
		for _, r := range f.Runs {
			if r.IsScript() && !hasScript(r.Target) || !r.IsScript() && tree.flow(r.Target) == nil {
				t.Errorf("%s run %s: target %s is not in the sync tree", f.ID, r.Name, r.Target)
			}
		}
		for p, fl := range tree.Flows {
			var refs func(ms []openflow.Module)
			check := func(m openflow.Module) {
				switch v := m.Value.(type) {
				case *openflow.PathFlow:
					if tree.flow(v.Path) == nil {
						t.Errorf("%s: flow %s module %s names flow %s, which the tree does not hold", f.ID, p, m.ID, v.Path)
					}
				case *openflow.PathScript:
					if !hasScript(v.Path) {
						t.Errorf("%s: flow %s module %s names script %s, which the tree does not hold", f.ID, p, m.ID, v.Path)
					}
				case *openflow.ForLoop:
					refs(v.Modules)
				case *openflow.WhileLoop:
					refs(v.Modules)
				case *openflow.BranchOne:
					refs(v.Default)
					for _, b := range v.Branches {
						refs(b.Modules)
					}
				case *openflow.BranchAll:
					for _, b := range v.Branches {
						refs(b.Modules)
					}
				}
			}
			refs = func(ms []openflow.Module) {
				for _, m := range ms {
					check(m)
				}
			}
			refs(fl.Value.Modules)
			if fl.Value.FailureModule != nil {
				check(*fl.Value.FailureModule)
			}
		}
	}
}

// TestRecordingsMatchTheirExpect holds every committed recording to its
// run's expect, as wltest record does on the oracle (with the fixture's
// compare options, leaving out carried and resolved_args, which a
// recording cannot hold). It runs offline, so an edited expect or recording
// that no longer agree fail the Go CI, not only the next oracle run.
func TestRecordingsMatchTheirExpect(t *testing.T) {
	// spec: OR-15, OR-19
	checked := 0
	for _, f := range loadFlowFixtures(t) {
		for _, r := range f.Runs {
			if r.Expect == nil || r.Recorded == nil {
				continue
			}
			checked++
			opts := f.Compare.Options()
			opts.WithRecording = true
			diffs, err := conformance.Compare(r.Recorded.Observation, *r.Expect, opts)
			if err != nil {
				t.Errorf("%s run %s: %v", f.ID, r.Name, err)
			}
			for _, d := range diffs {
				t.Errorf("%s run %s: the recording differs from expect: %s", f.ID, r.Name, d)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no recorded run has an expect")
	}
}

// TestRecordedParallelismReleaseOrder checks CF-7/b on its recording:
// with parallelism 1, the iterations of the parallel loop start in index
// order, each after the one before it completed. The times are the job
// times the walker kept in the provenance (they are never compared, OR-17).
// With the reference session's one worker the iterations could not overlap
// in any case, so the order of the start times is the discriminating part.
func TestRecordedParallelismReleaseOrder(t *testing.T) {
	// spec: CF-7/b
	f, err := conformance.LoadFixture(filepath.Join(flowsRoot, "control-flow", "cf-7"))
	if err != nil {
		t.Fatal(err)
	}
	var run *conformance.Run
	for i := range f.Runs {
		if f.Runs[i].Name == "b" {
			run = &f.Runs[i]
		}
	}
	if run == nil || run.Recorded == nil {
		t.Fatal("control-flow/cf-7 has no recorded run b")
	}
	var p Provenance
	if err := json.Unmarshal(run.Recorded.Provenance, &p); err != nil {
		t.Fatal(err)
	}
	at := func(site, field string) time.Time {
		t.Helper()
		j, ok := p.Timings.Jobs[site]
		if !ok {
			t.Fatalf("the provenance has no job at %s", site)
		}
		v := j.StartedAt
		if field == "completed" {
			v = j.CompletedAt
		}
		ts, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			t.Fatalf("%s %s: %v", site, field, err)
		}
		return ts
	}
	for i := 1; i < 4; i++ {
		prev, cur := conformance.SitePath{}.Child(conformance.Module("l")).Child(conformance.Iteration(i-1)), conformance.SitePath{}.Child(conformance.Module("l")).Child(conformance.Iteration(i))
		if !at(cur.String(), "started").After(at(prev.String(), "started")) {
			t.Errorf("iteration %d started before iteration %d", i, i-1)
		}
		if at(cur.String(), "started").Before(at(prev.String(), "completed")) {
			t.Errorf("iteration %d started before iteration %d completed", i, i-1)
		}
	}
}

// batchCoverage is conformance/flows/<area>/coverage.json: the findings
// whose catalogue cases the area's recording batches account for, and the
// cases they leave out.
type batchCoverage struct {
	About    string   `json:"about"`
	Findings []string `json:"findings"`
	Deferred []struct {
		Case      string `json:"case"`
		Milestone string `json:"milestone"`
		Reason    string `json:"reason"`
	} `json:"deferred"`
	Partial []struct {
		Case      string `json:"case"`
		Missing   string `json:"missing"`
		Milestone string `json:"milestone"`
	} `json:"partial"`
}

// TestRecordingBatchCoverage accounts for every catalogue case of the
// findings an area's coverage.json names: each is cited by a run of a
// fixture of that area that has a committed recording, or is deferred with
// a milestone and a reason. A case recorded only in part is cited and also
// listed as partial, with what is missing.
func TestRecordingBatchCoverage(t *testing.T) {
	// spec: OR-15, OR-19
	root := repoRoot(t)
	catalogue, err := os.ReadFile(filepath.Join(root, "docs/semantics/conformance.md"))
	if err != nil {
		t.Fatal(err)
	}
	caseLine := regexp.MustCompile(`(?m)^- \*\*([A-Z]+-V?[0-9]+/[a-z])\*\*`)
	byFinding := map[string][]string{}
	for _, m := range caseLine.FindAllSubmatch(catalogue, -1) {
		c := string(m[1])
		byFinding[c[:strings.IndexByte(c, '/')]] = append(byFinding[c[:strings.IndexByte(c, '/')]], c)
	}
	files, err := filepath.Glob(filepath.Join(flowsRoot, "*", "coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no conformance/flows/<area>/coverage.json")
	}
	fixtures := loadFlowFixtures(t)
	for _, file := range files {
		area := filepath.Base(filepath.Dir(file))
		t.Run(area, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var cov batchCoverage
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&cov); err != nil {
				t.Fatal(err)
			}
			recorded := map[string]bool{}
			for _, f := range fixtures {
				if !strings.HasPrefix(f.ID, area+"/") {
					continue
				}
				for _, r := range f.Runs {
					for _, c := range r.Spec {
						if r.Recorded == nil {
							t.Errorf("%s run %s cites %s but has no recording", f.ID, r.Name, c)
							continue
						}
						recorded[c] = true
					}
				}
			}
			deferred := map[string]bool{}
			for _, d := range cov.Deferred {
				if d.Milestone == "" || d.Reason == "" {
					t.Errorf("%s: a deferred case needs a milestone and a reason", d.Case)
				}
				if recorded[d.Case] {
					t.Errorf("%s is recorded but also deferred", d.Case)
				}
				deferred[d.Case] = true
			}
			for _, p := range cov.Partial {
				if p.Missing == "" || p.Milestone == "" {
					t.Errorf("%s: a partial case needs what is missing and a milestone", p.Case)
				}
				if !recorded[p.Case] {
					t.Errorf("%s is listed as partial but no recorded run cites it", p.Case)
				}
			}
			var want []string
			for _, finding := range cov.Findings {
				cs := byFinding[finding]
				if len(cs) == 0 {
					t.Errorf("finding %s has no catalogue cases", finding)
				}
				want = append(want, cs...)
			}
			sort.Strings(want)
			known := map[string]bool{}
			for _, c := range want {
				known[c] = true
				if !recorded[c] && !deferred[c] {
					t.Errorf("%s is neither recorded by a run of a %s fixture nor deferred", c, area)
				}
			}
			for c := range deferred {
				if !known[c] {
					t.Errorf("%s is deferred but is not a case of the listed findings", c)
				}
			}
		})
	}
}

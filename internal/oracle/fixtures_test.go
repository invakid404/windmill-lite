package oracle

import (
	"encoding/json"
	"os"
	"path/filepath"
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

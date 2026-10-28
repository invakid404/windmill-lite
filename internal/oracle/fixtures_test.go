package oracle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
// with parallelism 1, each iteration's child flow (m:l/i:<n>) starts after
// the previous one completed, so the iterations run in index order. The
// times are the job times the walker kept in the provenance (never
// compared, OR-17). The one worker of the reference session serialises the
// iterations' scripts, not their child flows: CF-7/a, the same loop with
// parallelism 2, records child flows that start before the previous one
// completed, so the predicate holds for b because of parallelism and
// fails for a.
func TestRecordedParallelismReleaseOrder(t *testing.T) {
	// spec: CF-7/b, CF-7/a
	f, err := conformance.LoadFixture(filepath.Join(flowsRoot, "control-flow", "cf-7"))
	if err != nil {
		t.Fatal(err)
	}
	// released reports, for each iteration after the first, whether its
	// child flow started after the previous one started and completed.
	released := func(name string) []bool {
		t.Helper()
		var run *conformance.Run
		for i := range f.Runs {
			if f.Runs[i].Name == name {
				run = &f.Runs[i]
			}
		}
		if run == nil || run.Recorded == nil {
			t.Fatalf("control-flow/cf-7 has no recorded run %s", name)
		}
		var p Provenance
		if err := json.Unmarshal(run.Recorded.Provenance, &p); err != nil {
			t.Fatal(err)
		}
		at := func(i int, completed bool) time.Time {
			t.Helper()
			site := conformance.SitePath{}.Child(conformance.Module("l")).Child(conformance.Iteration(i)).String()
			j, ok := p.Timings.Jobs[site]
			if !ok {
				t.Fatalf("run %s: the provenance has no job at %s", name, site)
			}
			v := j.StartedAt
			if completed {
				v = j.CompletedAt
			}
			ts, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				t.Fatalf("run %s %s: %v", name, site, err)
			}
			return ts
		}
		var out []bool
		for i := 1; i < 4; i++ {
			out = append(out, at(i, false).After(at(i-1, false)) && !at(i, false).Before(at(i-1, true)))
		}
		return out
	}
	for i, ok := range released("b") {
		if !ok {
			t.Errorf("run b: iteration %d started before iteration %d completed", i+1, i)
		}
	}
	overlapped := false
	for _, ok := range released("a") {
		overlapped = overlapped || !ok
	}
	if !overlapped {
		t.Error("run a (parallelism 2) records no iteration that started before the previous one completed, so run b's order shows nothing about parallelism")
	}
}

// TestRecordedDeploymentEvidence checks what the API-deployed fixtures'
// recordings say about their target flows (oracle.deploy api): the
// absent container fields of CF-6/a, CF-15/a and CF-15/b reach the server
// absent and come back filled after the dependency job (CF-6 rule 2), and
// generate-metadata writes the same defaults into flow.yaml; FX-40/d's
// body is pinned to the hash the deployment assigned its script.
func TestRecordedDeploymentEvidence(t *testing.T) {
	// spec: CF-6/a, CF-15/a, CF-15/b, FX-40/d
	const absent = "(absent)"
	type check struct {
		pointer                          string
		submitted, materialized, cliMade string // JSON text, or absent
	}
	loop, branchall := "/modules/0/value", "/modules/1/value"
	for _, c := range []struct {
		fixture, run string
		checks       []check
	}{
		{"control-flow/cf-6", "a", []check{
			{loop + "/skip_failures", absent, "true", "true"},
			{loop + "/parallel", absent, "false", "false"},
		}},
		{"control-flow/cf-15", "a", []check{
			{branchall + "/parallel", absent, "true", "true"},
			{branchall + "/branches/0/skip_failure", absent, "true", "true"},
			{branchall + "/branches/1/skip_failure", "true", "true", "true"},
		}},
		{"control-flow/cf-15", "b", []check{
			{branchall + "/parallel", absent, "true", "true"},
			{branchall + "/branches/1/skip_failure", "false", "false", "false"},
		}},
	} {
		d := recordedDeployment(t, c.fixture, c.run)
		for _, ck := range c.checks {
			for _, v := range []struct {
				name, want string
				doc        json.RawMessage
			}{
				{"submitted", ck.submitted, d.Submitted},
				{"materialized", ck.materialized, d.Materialized},
				{"cli_generated", ck.cliMade, d.CLIGenerated},
			} {
				if got := jsonAt(t, v.doc, ck.pointer, absent); got != v.want {
					t.Errorf("%s %s: %s at %s is %s, want %s", c.fixture, c.run, v.name, ck.pointer, got, v.want)
				}
			}
		}
	}

	hash := "/modules/1/value/modules/0/value/hash"
	for _, run := range []string{"d-hash", "d-hash-script-ttl"} {
		d := recordedDeployment(t, "execution/fx-40-hash", run)
		sub, mat := jsonAt(t, d.Submitted, hash, absent), jsonAt(t, d.Materialized, hash, absent)
		if len(sub) != 18 || sub == `"`+PinToDeployedHash+`"` || mat != sub {
			t.Errorf("fx-40-hash %s: submitted hash %s, materialized %s; want the deployed script's hash in both", run, sub, mat)
		}
		if cli := jsonAt(t, d.CLIGenerated, hash, absent); cli != `"`+PinToDeployedHash+`"` {
			t.Errorf("fx-40-hash %s: generate-metadata's copy pins %s, want the tree's %s", run, cli, PinToDeployedHash)
		}
	}
}

func recordedDeployment(t *testing.T, fixture, run string) FlowDeployment {
	t.Helper()
	f, err := conformance.LoadFixture(filepath.Join(flowsRoot, filepath.FromSlash(fixture)))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Oracle.DeploysThroughAPI() {
		t.Fatalf("%s is not deployed through the API", fixture)
	}
	for _, r := range f.Runs {
		if r.Name != run {
			continue
		}
		if r.Recorded == nil {
			t.Fatalf("%s run %s has no recording", fixture, run)
		}
		var p Provenance
		if err := json.Unmarshal(r.Recorded.Provenance, &p); err != nil {
			t.Fatal(err)
		}
		if p.Deployment == nil || p.Deployment.Method != conformance.DeployAPI {
			t.Fatalf("%s run %s: provenance has no API deployment", fixture, run)
		}
		return *p.Deployment
	}
	t.Fatalf("%s has no run %s", fixture, run)
	return FlowDeployment{}
}

// jsonAt returns the compact JSON text at an RFC 6901 pointer into doc, or
// absent when nothing is there.
func jsonAt(t *testing.T, doc json.RawMessage, pointer, absent string) string {
	t.Helper()
	tokens, err := conformance.SplitPointer(pointer)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			if v, ok = x[tok]; !ok {
				return absent
			}
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(x) {
				return absent
			}
			v = x[i]
		default:
			return absent
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

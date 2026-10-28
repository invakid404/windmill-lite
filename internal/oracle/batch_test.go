package oracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// batchManifest is a recording batch's manifest,
// conformance/flows/batches/<batch>.json: the cases of one ADR 0012
// recording milestone, the oracle runs that record them, and the milestone
// whose exit gate runs each one.
type batchManifest struct {
	Batch string `json:"batch"`
	Row   string `json:"row"`
	About string `json:"about"`
	Scope struct {
		// Findings are findings whose every catalogue case is in the batch.
		Findings []string `json:"findings"`
		// Cases are single catalogue cases in the batch.
		Cases []string `json:"cases"`
	} `json:"scope"`
	Cases []batchCase `json:"cases"`
}

// batchCase is one case of a batch: a catalogue case, or a finding id with
// a variant for a case that encodes the finding's rule beyond its
// catalogue cases.
type batchCase struct {
	Case    string   `json:"case"`
	Variant string   `json:"variant,omitempty"`
	Runs    []string `json:"runs"`
	Gate    string   `json:"gate"`
}

var (
	findingID = regexp.MustCompile(`^[A-Z]+-V?[0-9]+$`)
	caseID    = regexp.MustCompile(`^[A-Z]+-V?[0-9]+/[a-z]$`)
)

// TestRecordingBatches checks every recording batch manifest against
// ADR 0012, the catalogue, the fixtures and their committed recordings:
//
//   - the manifest's row is the batch's row of ADR 0012, and the findings
//     ("FX-6 to FX-10") and single cases ("FX-42/h") the row names are its
//     scope;
//   - every catalogue case of the scope's findings, and every single case,
//     is a case of the manifest, and every manifest case is a catalogue case
//     of the scope or a variant of one of its findings;
//   - every case names its runs, and a gate: a milestone of ADR 0012 that
//     needs the batch merged, directly or through other milestones;
//   - every run exists, cites the case (or, for a variant, its finding) in
//     its own spec, belongs to an oracle-observed fixture, has an expect
//     and a committed recording, and the recording meets the expect under
//     the fixture's compare options. The expect was written from the
//     reference before the oracle ran, so this is the offline proof that
//     the oracle agrees with it, or that it was corrected, as the run's
//     note then says;
//   - every run of a fixture the manifest uses belongs to some case.
func TestRecordingBatches(t *testing.T) {
	root := repoRoot(t)
	catalogue, err := os.ReadFile(filepath.Join(root, "docs/semantics/conformance.md"))
	if err != nil {
		t.Fatal(err)
	}
	inCatalogue := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^- \*\*([A-Z]+-V?[0-9]+/[a-z])\*\*`).FindAllSubmatch(catalogue, -1) {
		inCatalogue[string(m[1])] = true
	}
	design, err := os.ReadFile(filepath.Join(root, "docs/design/0012-layout-and-milestones.md"))
	if err != nil {
		t.Fatal(err)
	}
	plan := readMilestones(design)
	if plan["N1a"] == nil || plan["PF2"] == nil || plan["Id"] != nil {
		t.Fatalf("read %d milestones from ADR 0012", len(plan))
	}

	paths, err := filepath.Glob(filepath.Join(root, "conformance/flows/batches/*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no batch manifests in conformance/flows/batches")
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var m batchManifest
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&m); err != nil {
				t.Fatal(err)
			}
			row := plan[m.Batch]
			if row == nil {
				t.Fatalf("batch %q is not a milestone of ADR 0012", m.Batch)
			}
			if m.Row != row.title {
				t.Errorf("row %q is not ADR 0012's row for %s, %q", m.Row, m.Batch, row.title)
			}
			checkBatchScope(t, m, row.title, inCatalogue)
			checkBatch(t, root, m, inCatalogue, plan)
		})
	}
}

// milestone is one row of ADR 0012's milestone table.
type milestone struct {
	title string   // the Milestone cell, without bold markers
	needs []string // the milestone ids of the Needs merged cell
}

// readMilestones reads ADR 0012's milestone table: a row is a line of six
// cells whose first cell is a milestone id, not the header or the rule.
func readMilestones(design []byte) map[string]*milestone {
	plan := map[string]*milestone{}
	idCell := regexp.MustCompile(`^[A-Z][0-9A-Za-z]*$`)
	for _, line := range strings.Split(string(design), "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 6 {
			continue
		}
		id := strings.TrimSpace(cells[0])
		if id == "Id" || !idCell.MatchString(id) {
			continue
		}
		plan[id] = &milestone{title: strings.ReplaceAll(strings.TrimSpace(cells[1]), "**", ""), needs: strings.Fields(strings.NewReplacer(",", " ", "*", " ").Replace(cells[2]))}
	}
	return plan
}

// needsMerged reports whether milestone id needs batch merged, directly or
// through the milestones it needs.
func needsMerged(plan map[string]*milestone, id, batch string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(m string) bool {
		if seen[m] || plan[m] == nil {
			return false
		}
		seen[m] = true
		for _, n := range plan[m].needs {
			if n == batch || walk(n) {
				return true
			}
		}
		return false
	}
	return walk(id)
}

// checkBatchScope derives the batch's scope from its ADR 0012 row: each
// "X-n to X-m" names the findings X-n to X-m that have catalogue cases, and
// each "X-n/l" one case. When the row names any, the manifest's scope must
// be exactly those.
func checkBatchScope(t *testing.T, m batchManifest, title string, inCatalogue map[string]bool) {
	t.Helper()
	hasCases := func(f string) bool {
		for c := range inCatalogue {
			if strings.HasPrefix(c, f+"/") {
				return true
			}
		}
		return false
	}
	var findings, cases []string
	for _, r := range regexp.MustCompile(`\b([A-Z]+)-([0-9]+) to ([A-Z]+)-([0-9]+)\b`).FindAllStringSubmatch(title, -1) {
		lo, _ := strconv.Atoi(r[2])
		hi, _ := strconv.Atoi(r[4])
		if r[1] != r[3] || lo > hi {
			t.Errorf("row range %q is not one prefix, ascending", r[0])
			continue
		}
		for n := lo; n <= hi; n++ {
			if f := fmt.Sprintf("%s-%d", r[1], n); hasCases(f) {
				findings = append(findings, f)
			}
		}
	}
	cases = regexp.MustCompile(`\b[A-Z]+-V?[0-9]+/[a-z]\b`).FindAllString(title, -1)
	if len(findings) == 0 && len(cases) == 0 {
		return
	}
	sorted := func(v []string) []string { v = slices.Clone(v); sort.Strings(v); return v }
	if got, want := sorted(m.Scope.Findings), sorted(findings); !slices.Equal(got, want) {
		t.Errorf("scope.findings %q, but the row names %q", got, want)
	}
	if got, want := sorted(m.Scope.Cases), sorted(cases); !slices.Equal(got, want) {
		t.Errorf("scope.cases %q, but the row names %q", got, want)
	}
}

func checkBatch(t *testing.T, root string, m batchManifest, inCatalogue map[string]bool, plan map[string]*milestone) {
	t.Helper()
	// The cases the batch must hold.
	want := map[string]bool{}
	scopeFinding := map[string]bool{}
	for _, f := range m.Scope.Findings {
		scopeFinding[f] = true
		n := 0
		for c := range inCatalogue {
			if strings.HasPrefix(c, f+"/") {
				want[c] = true
				n++
			}
		}
		if n == 0 {
			t.Errorf("scope finding %s has no catalogue cases", f)
		}
	}
	for _, c := range m.Scope.Cases {
		if !inCatalogue[c] {
			t.Errorf("scope case %s is not in the catalogue", c)
		}
		want[c] = true
	}

	fixtures := map[string]*conformance.Fixture{}
	load := func(id string) *conformance.Fixture {
		if f, ok := fixtures[id]; ok {
			return f
		}
		f, err := conformance.LoadFixture(filepath.Join(root, "conformance/flows", filepath.FromSlash(id)))
		if err != nil {
			t.Errorf("fixture %s: %v", id, err)
		}
		fixtures[id] = f
		return f
	}
	usedRuns := map[string]bool{}
	have := map[string]bool{}
	for _, c := range m.Cases {
		cite := c.Case
		switch {
		case caseID.MatchString(c.Case):
			if !want[c.Case] {
				t.Errorf("%s is not a catalogue case in the batch's scope", c.Case)
			}
			if c.Variant != "" {
				t.Errorf("%s: a catalogue case has no variant", c.Case)
			}
			if have[c.Case] {
				t.Errorf("%s is listed twice", c.Case)
			}
			have[c.Case] = true
		case findingID.MatchString(c.Case):
			if !scopeFinding[c.Case] && !slices.ContainsFunc(m.Scope.Cases, func(s string) bool { return strings.HasPrefix(s, c.Case+"/") }) {
				t.Errorf("%s is not a finding in the batch's scope", c.Case)
			}
			if c.Variant == "" {
				t.Errorf("%s: a rule case needs a variant", c.Case)
			}
		default:
			t.Errorf("%q is neither a case id nor a finding id", c.Case)
			continue
		}
		if plan[c.Gate] == nil {
			t.Errorf("%s: gate %q is not a milestone of ADR 0012", c.Case, c.Gate)
		} else if !needsMerged(plan, c.Gate, m.Batch) {
			t.Errorf("%s: gate %s does not need %s merged, so it cannot run the recording", c.Case, c.Gate, m.Batch)
		}
		if len(c.Runs) == 0 {
			t.Errorf("%s names no runs", c.Case)
		}
		for _, ref := range c.Runs {
			usedRuns[ref] = true
			f := load(path.Dir(ref))
			if f == nil {
				continue
			}
			checkBatchRun(t, f, path.Base(ref), ref, cite)
		}
	}
	var missing []string
	for c := range want {
		if !have[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	for _, c := range missing {
		t.Errorf("%s is in the batch's scope but not in the manifest", c)
	}
	for id, f := range fixtures {
		if f == nil {
			continue
		}
		for _, r := range f.Runs {
			if !usedRuns[id+"/"+r.Name] {
				t.Errorf("run %s/%s belongs to no case of the batch", id, r.Name)
			}
		}
	}
}

func checkBatchRun(t *testing.T, f *conformance.Fixture, name, ref, cite string) {
	t.Helper()
	var r *conformance.Run
	for i := range f.Runs {
		if f.Runs[i].Name == name {
			r = &f.Runs[i]
		}
	}
	if r == nil {
		t.Errorf("%s: fixture %s has no run %s", ref, f.ID, name)
		return
	}
	// The run's own spec, not its fixture's: a fixture cites all of its
	// cases, so that would let any of its runs stand for any case.
	if !slices.Contains(r.Spec, cite) {
		t.Errorf("%s does not cite %s in its spec %q", ref, cite, r.Spec)
	}
	if f.Provenance != "oracle-observed" {
		t.Errorf("%s: fixture provenance is %q, want oracle-observed", ref, f.Provenance)
	}
	if r.Expect == nil {
		t.Errorf("%s has no expect", ref)
	}
	if r.Recorded == nil {
		t.Errorf("%s has no committed recording", ref)
		return
	}
	if r.Expect == nil {
		return
	}
	opts := f.Compare.Options()
	opts.WithRecording = true
	diffs, err := conformance.Compare(r.Recorded.Observation, *r.Expect, opts)
	if err != nil {
		t.Errorf("%s: %v", ref, err)
	}
	for _, d := range diffs {
		t.Errorf("%s: the recording does not meet the expect: %s", ref, d)
	}
}

package oracle

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestCatalogueCoverage accounts for every oracle case and every LIC-1
// case of the conformance catalogue: each is cited by a test or a data case
// of this milestone, checked live by every oracle session, or deferred to
// the milestone that records or implements it (testdata/coverage.json).
func TestCatalogueCoverage(t *testing.T) {
	root := repoRoot(t)
	catalogue, err := os.ReadFile(filepath.Join(root, "docs/semantics/conformance.md"))
	if err != nil {
		t.Fatal(err)
	}
	caseLine := regexp.MustCompile(`(?m)^- \*\*((?:OR-V?[0-9]+|LIC-1)/[a-z])\*\*`)
	var cases []string
	for _, m := range caseLine.FindAllSubmatch(catalogue, -1) {
		cases = append(cases, string(m[1]))
	}
	if len(cases) < 80 {
		t.Fatalf("found only %d oracle and LIC-1 cases in the catalogue", len(cases))
	}

	// The texts that cite cases: tests and data files of the milestone's
	// packages, and the flow fixtures.
	var cited bytes.Buffer
	for _, dir := range []string{"internal/oracle", "internal/conformance", "conformance/flows"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if filepath.Base(p) == "coverage.json" {
				return nil
			}
			if strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".yaml") {
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				cited.Write(data)
				cited.WriteByte('\n')
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile("testdata/coverage.json")
	if err != nil {
		t.Fatal(err)
	}
	var cov struct {
		About string `json:"about"`
		Live  []struct {
			Case string `json:"case"`
			By   string `json:"by"`
		} `json:"live"`
		Deferred []struct {
			Case      string `json:"case"`
			Milestone string `json:"milestone"`
			Reason    string `json:"reason"`
		} `json:"deferred"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cov); err != nil {
		t.Fatal(err)
	}
	accounted := map[string]string{}
	for _, l := range cov.Live {
		if l.By == "" {
			t.Errorf("%s: live entry says nothing", l.Case)
		}
		accounted[l.Case] = "live"
	}
	for _, d := range cov.Deferred {
		if d.Milestone == "" || d.Reason == "" {
			t.Errorf("%s: deferred entry needs a milestone and a reason", d.Case)
		}
		if _, dup := accounted[d.Case]; dup {
			t.Errorf("%s is listed twice", d.Case)
		}
		accounted[d.Case] = "deferred to " + d.Milestone
	}

	inCatalogue := map[string]bool{}
	var missing []string
	for _, c := range cases {
		inCatalogue[c] = true
		citedHere := regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(c) + `($|[^a-z])`).Match(cited.Bytes())
		how, listed := accounted[c]
		switch {
		case citedHere && listed && how != "live":
			t.Errorf("%s is cited by a case but also %s", c, how)
		case !citedHere && !listed:
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	for _, c := range missing {
		t.Errorf("%s is neither cited by a case nor listed in testdata/coverage.json", c)
	}
	for c := range accounted {
		if !inCatalogue[c] {
			t.Errorf("coverage.json lists %s, which the catalogue does not have", c)
		}
	}
}

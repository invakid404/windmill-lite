package oracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// A batch is a recording batch of ADR 0012 (E3a to E3e): the catalogue
// cases it records on the oracle, the fixtures that record them, and what
// it leaves to a later milestone. testdata/batches/<id>.json holds one.
type batch struct {
	About string `json:"about"`
	Batch string `json:"batch"`
	// Findings name the findings whose catalogue cases the batch records.
	Findings []string `json:"findings"`
	// Cases are further catalogue cases the batch records.
	Cases []string `json:"cases"`
	// Fixtures are the batch's fixture directories, from the repository
	// root.
	Fixtures []string `json:"fixtures"`
	// Deferred cases are not recorded by the batch.
	Deferred []struct {
		Case      string `json:"case"`
		Milestone string `json:"milestone"`
		Reason    string `json:"reason"`
	} `json:"deferred"`
	// Parts are halves of recorded cases that the oracle cannot observe
	// and a later milestone checks.
	Parts []struct {
		Case      string `json:"case"`
		Part      string `json:"part"`
		Milestone string `json:"milestone"`
		Reason    string `json:"reason"`
	} `json:"parts"`
	// Prefixes are the catalogue's prefix-only assertions (<starts "…">),
	// checked against the recorded observation of a run, named
	// <fixture directory>/<run>.
	Prefixes []struct {
		Case    string `json:"case"`
		Run     string `json:"run"`
		Pointer string `json:"pointer"`
		Prefix  string `json:"prefix"`
	} `json:"prefixes"`
	// Contradictions are catalogue claims that the oracle contradicts:
	// the recorded value at a pointer of a run, which the run's
	// expectation holds instead of the claim.
	Contradictions []struct {
		Case      string          `json:"case"`
		Catalogue string          `json:"catalogue"`
		Run       string          `json:"run"`
		Pointer   string          `json:"pointer"`
		Observed  json.RawMessage `json:"observed"`
	} `json:"contradictions"`
}

// TestRecordingBatches holds each recording batch to its row of ADR 0012:
// every catalogue case of the batch is recorded by one of its fixtures or
// deferred with a milestone and a reason; every run of its fixtures has an
// expectation and a recording of the pinned oracle, and the recording
// meets the expectation, so a recording that contradicts the expectation
// derived from the catalogue fails the Go CI and not only the next oracle
// run; the catalogue's prefix-only assertions hold on the recorded text;
// and each catalogue claim the oracle contradicted is listed with the value
// it recorded instead.
func TestRecordingBatches(t *testing.T) {
	// spec: OR-15, OR-19, OR-V5/b
	root := repoRoot(t)
	catalogue, err := os.ReadFile(filepath.Join(root, "docs/semantics/conformance.md"))
	if err != nil {
		t.Fatal(err)
	}
	caseLine := regexp.MustCompile(`(?m)^- \*\*([A-Z]+-V?[0-9]+/[a-z])\*\*`)
	inCatalogue := map[string]bool{}
	for _, m := range caseLine.FindAllSubmatch(catalogue, -1) {
		inCatalogue[string(m[1])] = true
	}

	paths, err := filepath.Glob("testdata/batches/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no batches in testdata/batches")
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			var b batch
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&b); err != nil {
				t.Fatal(err)
			}
			checkBatch(t, root, b, inCatalogue)
		})
	}
}

func checkBatch(t *testing.T, root string, b batch, inCatalogue map[string]bool) {
	// The batch's cases: those of its findings, and the extra ones.
	cases := map[string]bool{}
	for c := range inCatalogue {
		for _, f := range b.Findings {
			if strings.HasPrefix(c, f+"/") {
				cases[c] = true
			}
		}
	}
	for _, f := range b.Findings {
		found := false
		for c := range cases {
			found = found || strings.HasPrefix(c, f+"/")
		}
		if !found {
			t.Errorf("finding %s has no case in the catalogue", f)
		}
	}
	for _, c := range b.Cases {
		if !inCatalogue[c] {
			t.Errorf("extra case %s is not in the catalogue", c)
		}
		cases[c] = true
	}

	// The fixtures, their runs and recordings.
	recordedBy := map[string][]string{}
	observations := map[string]conformance.Observation{}
	for _, dir := range b.Fixtures {
		f, err := conformance.LoadFixture(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			t.Errorf("%s: %v", dir, err)
			continue
		}
		if f.Provenance != "oracle-observed" {
			t.Errorf("%s: provenance %q, want oracle-observed", dir, f.Provenance)
		}
		tree, err := LoadSyncTree(f.SyncTree())
		if err != nil {
			t.Errorf("%s: %v", dir, err)
			continue
		}
		if len(f.Runs) == 0 {
			t.Errorf("%s has no runs", dir)
		}
		for _, r := range f.Runs {
			name := dir + "/" + r.Name
			if !r.IsScript() && tree.flow(r.Target) == nil {
				t.Errorf("%s: target %s is not a flow of the sync tree", name, r.Target)
			}
			for _, c := range r.Spec {
				if strings.Contains(c, "/") && !inCatalogue[c] {
					t.Errorf("%s cites %s, which the catalogue does not have", name, c)
				}
			}
			if r.Expect == nil {
				t.Errorf("%s has no expect", name)
			}
			if r.Recorded == nil {
				t.Errorf("%s has no recording", name)
				continue
			}
			if err := CheckRecordingIdentity(r.Recorded.Provenance); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			observations[name] = r.Recorded.Observation
			if r.Expect != nil {
				opts := f.Compare.Options()
				opts.WithRecording = true
				diffs, err := conformance.Compare(r.Recorded.Observation, *r.Expect, opts)
				if err != nil {
					t.Errorf("%s: %v", name, err)
				}
				for _, d := range diffs {
					t.Errorf("%s: the recording does not meet the expectation: %s", name, d)
				}
			}
			for _, c := range r.Spec {
				recordedBy[c] = append(recordedBy[c], name)
			}
		}
	}

	deferred := map[string]bool{}
	for _, d := range b.Deferred {
		if !cases[d.Case] {
			t.Errorf("deferred %s is not a case of the batch", d.Case)
		}
		if d.Milestone == "" || d.Reason == "" {
			t.Errorf("deferred %s needs a milestone and a reason", d.Case)
		}
		if deferred[d.Case] {
			t.Errorf("%s is deferred twice", d.Case)
		}
		deferred[d.Case] = true
	}
	var missing []string
	for c := range cases {
		switch rec := len(recordedBy[c]) > 0; {
		case rec && deferred[c]:
			t.Errorf("%s is recorded by %v but also deferred", c, recordedBy[c])
		case !rec && !deferred[c]:
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	for _, c := range missing {
		t.Errorf("%s is neither recorded by a run of the batch nor deferred", c)
	}
	for _, p := range b.Parts {
		if len(recordedBy[p.Case]) == 0 {
			t.Errorf("part %q of %s: the case's other part is not recorded", p.Part, p.Case)
		}
		if p.Part == "" || p.Milestone == "" || p.Reason == "" {
			t.Errorf("part of %s needs a part, a milestone and a reason", p.Case)
		}
	}

	for _, p := range b.Prefixes {
		if !cases[p.Case] {
			t.Errorf("prefix for %s, which is not a case of the batch", p.Case)
		}
		obs, ok := observations[p.Run]
		if !ok {
			t.Errorf("prefix for %s: run %s is not a recorded run of the batch", p.Case, p.Run)
			continue
		}
		got, err := pointerString(obs, p.Pointer)
		if err != nil {
			t.Errorf("prefix for %s: %s %s: %v", p.Case, p.Run, p.Pointer, err)
			continue
		}
		if !strings.HasPrefix(got, p.Prefix) {
			t.Errorf("%s: %s at %s is %q, which does not start with %q", p.Case, p.Run, p.Pointer, got, p.Prefix)
		}
	}

	for _, c := range b.Contradictions {
		if !cases[c.Case] {
			t.Errorf("contradiction of %s, which is not a case of the batch", c.Case)
		}
		if c.Catalogue == "" {
			t.Errorf("contradiction of %s needs the catalogue's claim", c.Case)
		}
		obs, ok := observations[c.Run]
		if !ok {
			t.Errorf("contradiction of %s: run %s is not a recorded run of the batch", c.Case, c.Run)
			continue
		}
		got, err := pointerValue(obs, c.Pointer)
		if err != nil {
			t.Errorf("contradiction of %s: %s %s: %v", c.Case, c.Run, c.Pointer, err)
			continue
		}
		var want any
		if err := json.Unmarshal(c.Observed, &want); err != nil {
			t.Errorf("contradiction of %s: observed: %v", c.Case, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("contradiction of %s: %s at %s is %v, not the listed %s", c.Case, c.Run, c.Pointer, got, c.Observed)
		}
	}
}

// pointerValue resolves an RFC 6901 pointer in an observation's JSON form.
func pointerValue(o conformance.Observation, pointer string) (any, error) {
	data, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	tokens, err := conformance.SplitPointer(pointer)
	if err != nil {
		return nil, err
	}
	for _, tok := range tokens {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%q is not under an object", tok)
		}
		if v, ok = m[tok]; !ok {
			return nil, fmt.Errorf("no member %q", tok)
		}
	}
	return v, nil
}

// pointerString resolves an RFC 6901 pointer in an observation's JSON form
// to a string.
func pointerString(o conformance.Observation, pointer string) (string, error) {
	v, err := pointerValue(o, pointer)
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("not a string: %v", v)
	}
	return s, nil
}

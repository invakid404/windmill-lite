package jsonval

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The stored-form cases live in conformance/jsonb as YAML data files, one
// per finding (ADR 0003, the jsonb level). Each case gives a producer text
// and expects its stored text or its rejection. A catalogue case also names
// the form its consumer reads and, where the catalogue expects a key order,
// the top-level keys of that form.

const (
	casesDir      = "../../conformance/jsonb"
	catalogueFile = "../../docs/semantics/conformance.md"
	semanticsDir  = "../../docs/semantics"
)

type caseFile struct {
	Finding string       `yaml:"finding"`
	Note    string       `yaml:"note"`
	Cases   []storedCase `yaml:"cases"`
}

type storedCase struct {
	Spec       string `yaml:"spec"`
	Variant    string `yaml:"variant"`
	Provenance string `yaml:"provenance"`
	Scope      string `yaml:"scope"`
	Title      string `yaml:"title"`
	Note       string `yaml:"note"`
	Read       string `yaml:"read"`
	Given      string `yaml:"given"`
	Expect     struct {
		Stored *string  `yaml:"stored"`
		Keys   []string `yaml:"keys"`
		Reject *struct {
			Kind     string `yaml:"kind"`
			Database string `yaml:"database"`
		} `yaml:"reject"`
	} `yaml:"expect"`
	RestIn string `yaml:"rest_in"`
}

func (c storedCase) name() string {
	if c.Variant != "" {
		return c.Spec + "/" + c.Variant
	}
	return c.Spec
}

type deferredFile struct {
	Note     string `yaml:"note"`
	Deferred []struct {
		Spec       string `yaml:"spec"`
		Reason     string `yaml:"reason"`
		ExpectedIn string `yaml:"expected_in"`
	} `yaml:"deferred"`
}

var (
	rejectKinds = map[string]error{
		"nul":       ErrNUL,
		"surrogate": ErrUnpairedSurrogate,
		"numeric":   ErrNumericOverflow,
		"syntax":    ErrSyntax,
	}
	provenances = []string{"source-derived", "oracle-observed", "disputed", "policy"}
)

func readYAML(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// loadCaseFiles reads every case file and checks its shape.
func loadCaseFiles(t *testing.T) []caseFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(casesDir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var files []caseFile
	seen := map[string]bool{}
	for _, p := range paths {
		if filepath.Base(p) == "deferred.yaml" {
			continue
		}
		var f caseFile
		readYAML(t, p, &f)
		if want := strings.ToLower(f.Finding) + ".yaml"; filepath.Base(p) != want {
			t.Fatalf("%s holds finding %s; name it %s", p, f.Finding, want)
		}
		for _, c := range f.Cases {
			if problem := caseProblem(f.Finding, c); problem != "" {
				t.Fatalf("%s: %s: %s", p, c.name(), problem)
			}
			if seen[c.name()] {
				t.Fatalf("%s: %s appears twice", p, c.name())
			}
			seen[c.name()] = true
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("no case files in %s", casesDir)
	}
	return files
}

func caseProblem(finding string, c storedCase) string {
	catalogueCase := strings.HasPrefix(c.Spec, finding+"/")
	switch {
	case !catalogueCase && c.Spec != finding:
		return "the case does not belong to the file's finding"
	case catalogueCase && c.RestIn == "":
		return "a catalogue case names where its other half is checked (rest_in)"
	case !catalogueCase && c.Variant == "":
		return "a case without a catalogue letter needs a variant"
	case !slices.Contains(provenances, c.Provenance):
		return "unknown provenance " + c.Provenance
	case c.Title == "":
		return "no title"
	case (c.Expect.Stored == nil) == (c.Expect.Reject == nil):
		return "expect exactly one of stored and reject"
	case c.Expect.Reject != nil && rejectKinds[c.Expect.Reject.Kind] == nil:
		return "unknown reject kind " + c.Expect.Reject.Kind
	case c.Read != "" && c.Read != "producer" && c.Read != "stored":
		return "read is producer or stored"
	case c.Expect.Keys != nil && c.Read == "":
		return "keys need read, the form they are the order of"
	case c.Expect.Reject != nil && c.Read != "":
		return "a rejected value has no form to read"
	}
	return ""
}

func TestConformanceStored(t *testing.T) {
	for _, f := range loadCaseFiles(t) {
		for _, c := range f.Cases {
			t.Run(strings.ReplaceAll(c.name(), "/", "_"), func(t *testing.T) {
				runCase(t, c)
			})
		}
	}
}

func runCase(t *testing.T, c storedCase) {
	v := FromProducer([]byte(c.Given))
	stored, err := v.Stored()
	if r := c.Expect.Reject; r != nil {
		var se *StoreError
		if !errors.As(err, &se) {
			t.Fatalf("%s: Stored() = %s, %v; want a %s rejection", c.Title, stored, err, r.Kind)
		}
		if se.Kind != rejectKinds[r.Kind] {
			t.Errorf("rejection %v, want %s", err, r.Kind)
		}
		if r.Database != "" && se.DatabaseMessage() != r.Database {
			t.Errorf("DatabaseMessage() = %q, want %q", se.DatabaseMessage(), r.Database)
		}
		if r.Kind == "nul" && !v.HasNUL() {
			t.Error("HasNUL() is false for a NUL rejection")
		}
	} else {
		if err != nil {
			t.Fatalf("%s: Stored() = %v; want %s", c.Title, err, *c.Expect.Stored)
		}
		if string(stored) != *c.Expect.Stored {
			t.Errorf("%s:\nStored() = %s\n    want   %s", c.Title, stored, *c.Expect.Stored)
		}
	}
	if c.Expect.Keys != nil {
		form := v.Producer()
		if c.Read == "stored" {
			form = stored
		}
		if got := topLevelKeys(t, form); !slices.Equal(got, c.Expect.Keys) {
			t.Errorf("%s keys = %q, want %q", c.Read, got, c.Expect.Keys)
		}
	}
	checkAgainstReference(t, []byte(c.Given))
}

// topLevelKeys returns the keys of the object text in text order,
// duplicates included.
func topLevelKeys(t *testing.T, text []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(text))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("%s is not an object", text)
	}
	keys := []string{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// TestConformanceStoredCatalogue accounts for every catalogue case of each
// finding with a case file: it is a case there, with the provenance and
// scope the catalogue gives it, or it is listed in deferred.yaml with a
// reason and a place. Cases without a catalogue letter must cite a finding
// of the reference.
func TestConformanceStoredCatalogue(t *testing.T) {
	data, err := os.ReadFile(catalogueFile)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct{ provenance, scope string }
	catalogue := map[string]entry{}
	re := regexp.MustCompile(`(?m)^- \*\*([A-Z]+-V?[0-9]+)/([a-z]+)\*\* · ([a-z-]+)(?: · (core|later|out|EE) ·)?`)
	files := loadCaseFiles(t)
	findings := map[string]bool{}
	for _, f := range files {
		findings[f.Finding] = true
	}
	withCases := map[string]bool{}
	for _, m := range re.FindAllSubmatch(data, -1) {
		if finding := string(m[1]); findings[finding] {
			catalogue[finding+"/"+string(m[2])] = entry{string(m[3]), string(m[4])}
			withCases[finding] = true
		}
	}
	for finding := range findings {
		if !withCases[finding] {
			t.Errorf("no catalogue cases found for %s", finding)
		}
	}

	anchors := semanticsAnchors(t)
	covered := map[string]bool{}
	rule := 0
	for _, f := range files {
		for _, c := range f.Cases {
			if c.Spec == f.Finding {
				rule++
				if !anchors[strings.ToLower(c.Spec)] {
					t.Errorf("%s: %s is not a finding of the reference", c.name(), c.Spec)
				}
				continue
			}
			e, ok := catalogue[c.Spec]
			switch {
			case !ok:
				t.Errorf("%s is not a catalogue case", c.Spec)
			case c.Provenance != e.provenance:
				t.Errorf("%s: provenance %q, the catalogue says %q", c.name(), c.Provenance, e.provenance)
			case c.Scope != e.scope:
				t.Errorf("%s: scope %q, the catalogue says %q", c.name(), c.Scope, e.scope)
			}
			covered[c.Spec] = true
		}
	}

	var d deferredFile
	readYAML(t, filepath.Join(casesDir, "deferred.yaml"), &d)
	deferred := map[string]bool{}
	for _, e := range d.Deferred {
		switch _, ok := catalogue[e.Spec]; {
		case !ok:
			t.Errorf("deferred %s is not a catalogue case of a finding here", e.Spec)
		case covered[e.Spec]:
			t.Errorf("%s is both a case and deferred", e.Spec)
		case deferred[e.Spec]:
			t.Errorf("%s is deferred twice", e.Spec)
		case e.Reason == "" || e.ExpectedIn == "":
			t.Errorf("deferred %s needs a reason and a place", e.Spec)
		}
		deferred[e.Spec] = true
	}
	for id := range catalogue {
		if !covered[id] && !deferred[id] {
			t.Errorf("catalogue case %s is neither a case nor deferred", id)
		}
	}
	t.Logf("findings %d; catalogue cases %d: %d here, %d deferred; rule variants %d",
		len(findings), len(catalogue), len(covered), len(deferred), rule)
}

// semanticsAnchors returns the anchor ids of the reference's area files,
// which name every finding.
func semanticsAnchors(t *testing.T) map[string]bool {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(semanticsDir, "*.md"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no reference files in %s (%v)", semanticsDir, err)
	}
	anchors := map[string]bool{}
	re := regexp.MustCompile(`<a id="([a-z0-9-]+)"></a>`)
	for _, p := range paths {
		if filepath.Base(p) == "conformance.md" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllSubmatch(data, -1) {
			anchors[string(m[1])] = true
		}
	}
	return anchors
}

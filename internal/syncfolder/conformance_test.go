package syncfolder

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The sync-folder cases live in conformance/syncfolder as data files, one
// per finding. Each case cites its catalogue case (docs/semantics/
// conformance.md), or only the finding for a case that extends the
// catalogue, and gives a folder as a map of files. The runner writes the
// folder to a temporary directory, opens it from the case's start
// directory, and checks the expectations: what Open returns, the listed
// and filtered items, each named item read with its JSON form addressed by
// JSON Pointer or its error's kind and message substrings, a step's
// target, the warnings, the skipped files, and Check.

const (
	casesDir      = "../../conformance/syncfolder"
	catalogueFile = "../../docs/semantics/conformance.md"
	yamlxCases    = "../yamlx/testdata/sy9.json"
)

type caseFile struct {
	Finding string     `json:"finding"`
	Cases   []loadCase `json:"cases"`
}

type loadCase struct {
	Spec       string `json:"spec"`
	Variant    string `json:"variant"`
	Provenance string `json:"provenance"`
	Scope      string `json:"scope"`
	Part       string `json:"part"`
	Policy     string `json:"policy"`
	Note       string `json:"note"`
	Title      string `json:"title"`
	Given      struct {
		Files    map[string]string `json:"files"`
		HexFiles map[string]string `json:"hex_files"`
		Start    string            `json:"start"`
		Step     *struct {
			Flow   string `json:"flow"`
			Module string `json:"module"`
		} `json:"step"`
	} `json:"given"`
	Expect struct {
		Load        *errExpect                        `json:"load"`
		Root        string                            `json:"root"`
		Paths       map[ItemKind][]string             `json:"paths"`
		Filtered    map[ItemKind][]string             `json:"filtered"`
		Items       map[ItemKind]map[string]itemCheck `json:"items"`
		Target      *itemCheck                        `json:"target"`
		Warnings    []string                          `json:"warnings"`
		NotWarnings []string                          `json:"not_warnings"`
		Skipped     []Skipped                         `json:"skipped"`
		CheckOK     bool                              `json:"check_ok"`
		CheckError  *errExpect                        `json:"check_error"`
	} `json:"expect"`
}

type errExpect struct {
	Kind     ErrorKind `json:"kind"`
	Contains []string  `json:"contains"`
}

type itemCheck struct {
	At     map[string]json.RawMessage `json:"at"`
	Absent []string                   `json:"absent"`
	Error  *errExpect                 `json:"error"`
}

type deferredFile struct {
	Note     string `json:"note"`
	Deferred []struct {
		Spec       string `json:"spec"`
		Reason     string `json:"reason"`
		ExpectedIn string `json:"expected_in"`
	} `json:"deferred"`
}

var specPattern = regexp.MustCompile(`^((?:SY|PA)-[0-9V]+)(/[a-z])?$`)

func readStrict(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func loadCaseFiles(t *testing.T) []caseFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(casesDir, "*-*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no case files in %s (%v)", casesDir, err)
	}
	var files []caseFile
	for _, p := range paths {
		var f caseFile
		readStrict(t, p, &f)
		if want := strings.ToLower(f.Finding) + ".json"; filepath.Base(p) != want {
			t.Fatalf("%s holds finding %s; want the file %s", p, f.Finding, want)
		}
		for _, c := range f.Cases {
			m := specPattern.FindStringSubmatch(c.Spec)
			if m == nil || m[1] != f.Finding {
				t.Fatalf("%s: case %s does not belong to finding %s", p, c.Spec, f.Finding)
			}
			switch c.Provenance {
			case "source-derived", "policy":
			default:
				t.Fatalf("%s: case %s has provenance %q", p, c.Spec, c.Provenance)
			}
		}
		files = append(files, f)
	}
	return files
}

func TestConformanceSyncFolder(t *testing.T) {
	n := 0
	for _, f := range loadCaseFiles(t) {
		for i, c := range f.Cases {
			name := c.Spec
			if c.Variant != "" {
				name += "/" + c.Variant
			}
			if !strings.Contains(c.Spec, "/") {
				name += "/" + strconv.Itoa(i)
			}
			t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) { runCase(t, c) })
			n++
		}
	}
	t.Logf("%d sync-folder cases", n)
}

// writeTree writes a case's files under dir. A `.git` directory at dir
// bounds the upward search for wmill.yaml to the case.
func writeTree(t *testing.T, dir string, c loadCase) {
	t.Helper()
	write := func(name string, data []byte) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range c.Given.Files {
		write(name, []byte(text))
	}
	for name, h := range c.Given.HexFiles {
		data, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		write(name, data)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runCase(t *testing.T, c loadCase) {
	dir := t.TempDir()
	writeTree(t, dir, c)
	f, err := Open(filepath.Join(dir, filepath.FromSlash(c.Given.Start)))
	x := c.Expect
	if x.Load != nil {
		if err == nil {
			f.Close()
			t.Fatalf("%s: Open succeeded; want a %s error", c.Title, x.Load.Kind)
		}
		checkError(t, "Open", err, x.Load)
		return
	}
	if err != nil {
		t.Fatalf("%s: Open: %v", c.Title, err)
	}
	defer f.Close()

	if x.Root != "" {
		rel, err := filepath.Rel(dir, f.Root)
		if err != nil || filepath.ToSlash(rel) != x.Root {
			t.Errorf("root = %s, want %s under the case directory", f.Root, x.Root)
		}
	}
	for kind, want := range x.Paths {
		if got := f.Paths(kind); !slices.Equal(got, want) {
			t.Errorf("Paths(%s) = %q, want %q", kind, got, want)
		}
	}
	for kind, want := range x.Filtered {
		if got := f.Filtered(kind); !slices.Equal(got, want) {
			t.Errorf("Filtered(%s) = %q, want %q", kind, got, want)
		}
	}
	for kind, items := range x.Items {
		for p, want := range items {
			got, err := f.read(kind, p)
			checkItem(t, string(kind)+" "+p, got, err, want)
		}
	}
	if x.Target != nil {
		s := c.Given.Step
		fl, err := f.Flow(s.Flow)
		if err != nil {
			t.Fatalf("flow %s: %v", s.Flow, err)
		}
		found := false
		for m := range fl.Document.Value.AllModules() {
			if m.ID == s.Module {
				got, err := f.Target(s.Flow, m)
				checkItem(t, "target", got, err, *x.Target)
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("flow %s has no module %s", s.Flow, s.Module)
		}
	}
	for _, w := range x.Warnings {
		if !slices.ContainsFunc(f.Warnings, func(s string) bool { return strings.Contains(s, w) }) {
			t.Errorf("no warning contains %q; warnings: %q", w, f.Warnings)
		}
	}
	for _, w := range x.NotWarnings {
		if slices.ContainsFunc(f.Warnings, func(s string) bool { return strings.Contains(s, w) }) {
			t.Errorf("a warning contains %q; warnings: %q", w, f.Warnings)
		}
	}
	for _, want := range x.Skipped {
		if !slices.ContainsFunc(f.Skipped, func(s Skipped) bool { return s.File == want.File && s.Kind == want.Kind }) {
			t.Errorf("%s is not skipped as %s; skipped: %+v", want.File, want.Kind, f.Skipped)
		}
	}
	if x.CheckOK {
		if err := f.Check(); err != nil {
			t.Errorf("Check: %v", err)
		}
	}
	if x.CheckError != nil {
		checkError(t, "Check", f.Check(), x.CheckError)
	}
}

// checkError checks that err, or one of the errors it joins, is an *Error
// of the expected kind whose message holds every substring.
func checkError(t *testing.T, what string, err error, want *errExpect) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error; want %s", what, want.Kind)
		return
	}
	var all []error
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		all = j.Unwrap()
	} else {
		all = []error{err}
	}
	for _, e := range all {
		var se *Error
		if !errors.As(e, &se) {
			t.Errorf("%s: error %T is not an *Error: %v", what, e, e)
			return
		}
		if se.Kind != want.Kind {
			continue
		}
		msg := e.Error()
		for _, s := range want.Contains {
			if !strings.Contains(msg, s) {
				t.Errorf("%s: message %q does not contain %q", what, msg, s)
			}
		}
		return
	}
	t.Errorf("%s: no %s error in %v", what, want.Kind, err)
}

func checkItem(t *testing.T, what string, got any, err error, want itemCheck) {
	t.Helper()
	if want.Error != nil {
		checkError(t, what, err, want.Error)
		return
	}
	if err != nil {
		t.Errorf("%s: %v", what, err)
		return
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeAny(t, encoded)
	for ptr, raw := range want.At {
		v, ok := resolvePointer(doc, ptr)
		if !ok {
			t.Errorf("%s: %s absent in %s", what, ptr, encoded)
			continue
		}
		if w := decodeAny(t, raw); !reflect.DeepEqual(v, w) {
			t.Errorf("%s: %s = %s, want %s", what, ptr, mustJSON(v), raw)
		}
	}
	for _, ptr := range want.Absent {
		if v, ok := resolvePointer(doc, ptr); ok {
			t.Errorf("%s: %s = %s, want absent", what, ptr, mustJSON(v))
		}
	}
}

func decodeAny(t *testing.T, data []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	return v
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func resolvePointer(doc any, ptr string) (any, bool) {
	if ptr == "" {
		return doc, true
	}
	unescape := strings.NewReplacer("~1", "/", "~0", "~")
	for _, part := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		part = unescape.Replace(part)
		switch v := doc.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				return nil, false
			}
			doc = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			doc = v[i]
		default:
			return nil, false
		}
	}
	return doc, true
}

// TestConformanceSyncFolderCatalogue accounts for every SY and PA case of
// the catalogue: each is a case here or is listed in deferred.json with
// the reason, and every cited case exists with the provenance the
// catalogue gives it. The SY-9 cases are deferred to yamlx's case file,
// which must hold them.
func TestConformanceSyncFolderCatalogue(t *testing.T) {
	data, err := os.ReadFile(catalogueFile)
	if err != nil {
		t.Fatal(err)
	}
	catalogue := map[string]string{}
	re := regexp.MustCompile(`(?m)^- \*\*((?:SY|PA)-[0-9V]+/[a-z])\*\* · ([a-z-]+)`)
	for _, m := range re.FindAllSubmatch(data, -1) {
		catalogue[string(m[1])] = string(m[2])
	}
	if len(catalogue) == 0 {
		t.Fatal("no SY or PA cases found in the catalogue")
	}

	covered := map[string]bool{}
	for _, f := range loadCaseFiles(t) {
		for _, c := range f.Cases {
			if !strings.Contains(c.Spec, "/") {
				continue // a case that extends the catalogue
			}
			prov, ok := catalogue[c.Spec]
			if !ok {
				t.Errorf("%s is not a catalogue case", c.Spec)
				continue
			}
			if c.Provenance != prov {
				t.Errorf("%s: provenance %q, catalogue says %q", c.Spec, c.Provenance, prov)
			}
			covered[c.Spec] = true
		}
	}
	var d deferredFile
	readStrict(t, filepath.Join(casesDir, "deferred.json"), &d)
	yamlx, err := os.ReadFile(yamlxCases)
	if err != nil {
		t.Fatal(err)
	}
	deferred := map[string]bool{}
	for _, e := range d.Deferred {
		switch {
		case catalogue[e.Spec] == "":
			t.Errorf("deferred %s is not a catalogue case", e.Spec)
		case covered[e.Spec]:
			t.Errorf("%s is both covered and deferred", e.Spec)
		case deferred[e.Spec]:
			t.Errorf("%s is deferred twice", e.Spec)
		case e.Reason == "" || e.ExpectedIn == "":
			t.Errorf("deferred %s needs a reason and a place", e.Spec)
		case strings.HasPrefix(e.Spec, "SY-9/") && !bytes.Contains(yamlx, []byte(`"spec": "`+e.Spec+`"`)):
			t.Errorf("deferred %s is not in %s", e.Spec, yamlxCases)
		}
		deferred[e.Spec] = true
	}
	for spec := range catalogue {
		if !covered[spec] && !deferred[spec] {
			t.Errorf("catalogue case %s is neither covered nor deferred", spec)
		}
	}
	t.Logf("catalogue SY and PA cases: %d; covered here: %d; deferred: %d", len(catalogue), len(covered), len(deferred))
}

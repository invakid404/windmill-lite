package openflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The OpenFlow decode cases live in conformance/openflow as data files, one
// per finding. Each case cites its catalogue case (docs/semantics/
// conformance.md) and states either the rejection (message substrings, as
// the reference compares them) or, for an accepted value, parts of its
// decoded form addressed by JSON Pointer.

const (
	casesDir      = "../../conformance/openflow"
	catalogueFile = "../../docs/semantics/conformance.md"
)

type caseFile struct {
	Finding string    `json:"finding"`
	Scope   string    `json:"scope"`
	Cases   []docCase `json:"cases"`
}

type docCase struct {
	Spec       string          `json:"spec"`
	Variant    string          `json:"variant"`
	Provenance string          `json:"provenance"`
	Scope      string          `json:"scope"`
	Part       string          `json:"part"`
	Deviation  string          `json:"deviation"`
	Note       string          `json:"note"`
	Title      string          `json:"title"`
	Given      json.RawMessage `json:"given"`
	Expect     struct {
		Accept *struct {
			At      map[string]json.RawMessage `json:"at"`
			Matches map[string]string          `json:"matches"`
			Absent  []string                   `json:"absent"`
		} `json:"accept"`
		Reject *struct {
			Kind        string   `json:"kind"`
			Contains    []string `json:"contains"`
			NotContains []string `json:"not_contains"`
			NotPrefix   string   `json:"not_prefix"`
		} `json:"reject"`
	} `json:"expect"`
}

type deferredFile struct {
	Note     string `json:"note"`
	Deferred []struct {
		Spec       string `json:"spec"`
		Reason     string `json:"reason"`
		ExpectedIn string `json:"expected_in"`
	} `json:"deferred"`
}

var kindNames = map[string]ErrorKind{
	"syntax":     ErrSyntax,
	"flow_value": ErrFlowValue,
	"module":     ErrModule,
	"retry_base": ErrRetryBase,
	"step_path":  ErrStepPath,
	"flow":       ErrFlow,
	"ee_only":    ErrEEOnly,
}

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
	paths, err := filepath.Glob(filepath.Join(casesDir, "of-*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no case files in %s (%v)", casesDir, err)
	}
	var files []caseFile
	for _, p := range paths {
		var f caseFile
		readStrict(t, p, &f)
		for _, c := range f.Cases {
			if !strings.HasPrefix(c.Spec, f.Finding+"/") {
				t.Fatalf("%s: case %s does not belong to finding %s", p, c.Spec, f.Finding)
			}
			if (c.Expect.Accept == nil) == (c.Expect.Reject == nil) {
				t.Fatalf("%s: case %s must expect exactly one of accept and reject", p, c.Spec)
			}
		}
		files = append(files, f)
	}
	return files
}

func TestConformanceOpenFlow(t *testing.T) {
	for _, f := range loadCaseFiles(t) {
		for _, c := range f.Cases {
			name := c.Spec
			if c.Variant != "" {
				name += "/" + c.Variant
			}
			t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
				runCase(t, c)
			})
		}
	}
}

func runCase(t *testing.T, c docCase) {
	v, err := DecodeFlowValue(c.Given)
	if r := c.Expect.Reject; r != nil {
		if err == nil {
			got, _ := v.MarshalJSON()
			t.Fatalf("%s: accepted as %s; want a rejection", c.Title, got)
		}
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("error %T is not an *Error: %v", err, err)
		}
		if r.Kind != "" {
			want, ok := kindNames[r.Kind]
			if !ok {
				t.Fatalf("unknown kind %q in case", r.Kind)
			}
			if e.Kind != want {
				t.Errorf("kind = %d, want %s (%v)", e.Kind, r.Kind, err)
			}
		}
		msg := err.Error()
		for _, s := range r.Contains {
			if !strings.Contains(msg, s) {
				t.Errorf("message %q does not contain %q", msg, s)
			}
		}
		for _, s := range r.NotContains {
			if strings.Contains(msg, s) {
				t.Errorf("message %q contains %q", msg, s)
			}
		}
		if r.NotPrefix != "" && strings.HasPrefix(msg, r.NotPrefix) {
			t.Errorf("message %q starts with %q", msg, r.NotPrefix)
		}
		return
	}
	if err != nil {
		t.Fatalf("%s: rejected: %v", c.Title, err)
	}
	encoded, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeAny(t, encoded)
	a := c.Expect.Accept
	for ptr, wantRaw := range a.At {
		got, ok := resolvePointer(doc, ptr)
		if !ok {
			t.Errorf("%s: absent in %s", ptr, encoded)
			continue
		}
		if want := decodeAny(t, wantRaw); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %s, want %s", ptr, mustJSON(got), wantRaw)
		}
	}
	for ptr, pattern := range a.Matches {
		got, ok := resolvePointer(doc, ptr)
		s, isString := got.(string)
		if !ok || !isString || !regexp.MustCompile(pattern).MatchString(s) {
			t.Errorf("%s = %v, want a string matching %s", ptr, got, pattern)
		}
	}
	for _, ptr := range a.Absent {
		if got, ok := resolvePointer(doc, ptr); ok {
			t.Errorf("%s = %s, want absent", ptr, mustJSON(got))
		}
	}
	assertRoundTrip(t, encoded)
}

// assertRoundTrip checks that the decoded form decodes back to a value with
// the same decoded form.
func assertRoundTrip(t *testing.T, encoded []byte) {
	t.Helper()
	v, err := DecodeFlowValue(encoded)
	if err != nil {
		t.Fatalf("decoded form %s does not decode: %v", encoded, err)
	}
	again, _ := v.MarshalJSON()
	if !bytes.Equal(again, encoded) {
		t.Fatalf("round trip changed the decoded form:\n first %s\nsecond %s", encoded, again)
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

// TestConformanceOpenFlowCatalogue accounts for every OF case of the
// catalogue: each is a decode case here or is listed in deferred.json with
// the reason, and every cited case exists with the provenance the catalogue
// gives it.
func TestConformanceOpenFlowCatalogue(t *testing.T) {
	data, err := os.ReadFile(catalogueFile)
	if err != nil {
		t.Fatal(err)
	}
	catalogue := map[string]string{}
	re := regexp.MustCompile(`(?m)^- \*\*(OF-[0-9V]+/[a-z])\*\* · ([a-z-]+)`)
	for _, m := range re.FindAllSubmatch(data, -1) {
		catalogue[string(m[1])] = string(m[2])
	}
	if len(catalogue) == 0 {
		t.Fatal("no OF cases found in the catalogue")
	}

	covered := map[string]bool{}
	for _, f := range loadCaseFiles(t) {
		for _, c := range f.Cases {
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
		}
		deferred[e.Spec] = true
	}
	for spec := range catalogue {
		if !covered[spec] && !deferred[spec] {
			t.Errorf("catalogue case %s is neither covered nor deferred", spec)
		}
	}
	t.Logf("catalogue OF cases: %d; decode cases: %d; deferred: %d", len(catalogue), len(covered), len(deferred))
}

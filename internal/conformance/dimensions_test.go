package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestObservationDimensionCases(t *testing.T) {
	type variant struct {
		Why   string                     `json:"why"`
		Steps map[string]json.RawMessage `json:"steps"`
	}
	var file struct {
		About    string `json:"about"`
		Distinct []struct {
			caseMeta
			Base     json.RawMessage `json:"base"`
			Variants []variant       `json:"variants"`
		} `json:"distinct"`
		Same []struct {
			caseMeta
			Spellings []json.RawMessage `json:"spellings"`
		} `json:"same"`
		Kept []struct {
			caseMeta
			Observation json.RawMessage `json:"observation"`
			Site        string          `json:"site"`
			Field       string          `json:"field"`
		} `json:"kept"`
		Reject []struct {
			caseMeta
			Observation json.RawMessage `json:"observation"`
		} `json:"reject"`
	}
	data, err := os.ReadFile(filepath.Join("testdata", "observation", "dimensions.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	if len(file.Distinct) == 0 || len(file.Same) == 0 || len(file.Kept) == 0 || len(file.Reject) == 0 {
		t.Fatal("a case group is empty")
	}

	for _, c := range file.Distinct {
		t.Run("distinct/"+c.Name, func(t *testing.T) {
			c.check(t, c.Name)
			base := decodeObservation(t, c.Base)
			for _, v := range c.Variants {
				// The variant is the base with some steps replaced.
				var doc map[string]json.RawMessage
				if err := json.Unmarshal(c.Base, &doc); err != nil {
					t.Fatal(err)
				}
				var steps map[string]json.RawMessage
				if err := json.Unmarshal(doc["steps"], &steps); err != nil {
					t.Fatal(err)
				}
				for k, s := range v.Steps {
					if _, ok := steps[k]; !ok {
						t.Fatalf("%s: variant names site %q that the base lacks", v.Why, k)
					}
					steps[k] = s
				}
				doc["steps"] = mustMarshal(t, steps)
				other := decodeObservation(t, mustMarshal(t, doc))

				// Everything a recording of job rows and the final result holds
				// is the same...
				if other.Status != base.Status || !jsonEqual(other.Result, base.Result) {
					t.Fatalf("%s: the root differs, which the case must not rely on", v.Why)
				}
				for p, s := range base.Steps {
					o := other.Steps[p]
					if o.Status != s.Status || !jsonEqual(o.Args, s.Args) || !jsonEqual(o.Result, s.Result) {
						t.Fatalf("%s: site %s differs in its job status or stored fields", v.Why, p)
					}
				}
				// ...and the observations still differ.
				if observationsEqual(t, base, other) {
					t.Errorf("%s: the observations compare equal", v.Why)
				}
			}
		})
	}

	for _, c := range file.Same {
		t.Run("same/"+c.Name, func(t *testing.T) {
			c.check(t, c.Name)
			first := decodeObservation(t, c.Spellings[0])
			for i, sp := range c.Spellings[1:] {
				if !observationsEqual(t, first, decodeObservation(t, sp)) {
					t.Errorf("spelling %d differs from spelling 0", i+1)
				}
			}
			// The canonical encoding is the same text for every spelling.
			want := mustMarshal(t, first)
			for i, sp := range c.Spellings {
				if got := mustMarshal(t, decodeObservation(t, sp)); !bytes.Equal(got, want) {
					t.Errorf("spelling %d encodes as %s, want %s", i, got, want)
				}
			}
		})
	}

	for _, c := range file.Kept {
		t.Run("kept/"+c.Name, func(t *testing.T) {
			c.check(t, c.Name)
			in := stepField(t, c.Observation, c.Site, c.Field)
			if in == nil {
				t.Fatalf("the case has no %s at %s", c.Field, c.Site)
			}
			// Decoding and encoding again keeps the explicit value, text
			// for text.
			out := stepField(t, mustMarshal(t, decodeObservation(t, c.Observation)), c.Site, c.Field)
			if out == nil {
				t.Fatalf("%s at %s was dropped", c.Field, c.Site)
			}
			var ci, co bytes.Buffer
			if json.Compact(&ci, in) != nil || json.Compact(&co, out) != nil || !bytes.Equal(ci.Bytes(), co.Bytes()) {
				t.Errorf("%s at %s came back as %s, want %s", c.Field, c.Site, out, in)
			}
		})
	}

	for _, c := range file.Reject {
		t.Run("reject/"+c.Name, func(t *testing.T) {
			c.check(t, c.Name)
			var obs Observation
			if err := json.Unmarshal(c.Observation, &obs); err == nil {
				t.Errorf("decoded %s", c.Observation)
			}
		})
	}
}

// stepField returns the raw text of one field of one step of an encoded
// observation, or nil when it is absent.
func stepField(t *testing.T, observation []byte, site, field string) json.RawMessage {
	t.Helper()
	var doc struct {
		Steps map[string]map[string]json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(observation, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Steps[site][field]
}

func TestImpliedModuleStatusOnNonModuleSitesBuiltInGo(t *testing.T) {
	// The same observation, built in Go and decoded from JSON, encodes
	// alike: an implied module status is accepted on any site and dropped.
	attempt := SitePath{}.Child(Module("a"), Attempt(0))
	iteration := SitePath{}.Child(Module("l"), Iteration(0))
	built := Observation{Status: RunSuccess, Result: json.RawMessage(`null`), Steps: map[SitePath]StepObservation{
		attempt:   {Status: StepFailure, Module: ModuleFailure},
		iteration: {Status: StepSuccess, Module: ModuleSuccess},
	}}
	fromGo, err := json.Marshal(built)
	if err != nil {
		t.Fatalf("marshalling the built observation: %v", err)
	}
	decoded := decodeObservation(t, []byte(`{"status":"success","result":null,"steps":{"m:a/r:0":{"status":"failure","module":"failure"},"m:l/i:0":{"status":"success","module":"success"}}}`))
	if fromJSON := mustMarshal(t, decoded); !bytes.Equal(fromGo, fromJSON) {
		t.Errorf("built in Go: %s\ndecoded:     %s", fromGo, fromJSON)
	}
	// The caller's steps are left as they were.
	if built.Steps[attempt].Module != ModuleFailure {
		t.Error("marshalling changed the caller's step")
	}
	// A module status other than the implied one is still rejected there,
	// built or decoded.
	for _, p := range []SitePath{attempt, iteration} {
		bad := Observation{Status: RunSuccess, Result: json.RawMessage(`null`), Steps: map[SitePath]StepObservation{
			p: {Status: StepSuccess, Module: ModuleFailure},
		}}
		if _, err := json.Marshal(bad); err == nil {
			t.Errorf("marshalled a differing module status at %s", p)
		}
	}
}

func decodeObservation(t *testing.T, text []byte) Observation {
	t.Helper()
	var obs Observation
	if err := json.Unmarshal(text, &obs); err != nil {
		t.Fatalf("decoding %s: %v", text, err)
	}
	return obs
}

// observationsEqual compares two observations as a comparator does: by
// their canonical encodings, parsed, with objects compared without regard
// to key order.
func observationsEqual(t *testing.T, a, b Observation) bool {
	t.Helper()
	va, err := decodeJSON(mustMarshal(t, a))
	if err != nil {
		t.Fatal(err)
	}
	vb, err := decodeJSON(mustMarshal(t, b))
	if err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(va, vb)
}

func TestModuleStatusIsImpliedByTheJobStatus(t *testing.T) {
	for s, want := range map[StepStatus]ModuleStatus{
		StepSuccess:  ModuleSuccess,
		StepSkipped:  ModuleSuccess,
		StepFailure:  ModuleFailure,
		StepCanceled: ModuleFailure,
		StepNotRun:   ModuleNotRun,
	} {
		if got := s.ImpliedModule(); got != want {
			t.Errorf("%q.ImpliedModule() = %q, want %q", s, got, want)
		}
		if got := (StepObservation{Status: s}).ModuleStatus(); got != want {
			t.Errorf("ModuleStatus() of a %q step = %q", s, got)
		}
	}
	// spec: FX-V2/a — an explicit module status wins over the implied one.
	if got := (StepObservation{Status: StepSuccess, Module: ModuleFailure}).ModuleStatus(); got != ModuleFailure {
		t.Errorf("ModuleStatus() = %q, want failure", got)
	}
}

// jsonEqual reports whether two JSON texts hold the same value, with
// objects compared without regard to key order and numbers by their text.
func jsonEqual(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va, errA := decodeJSON(a)
	vb, errB := decodeJSON(b)
	if errA != nil || errB != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(va, vb)
}

func decodeJSON(text []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

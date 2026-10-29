package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestNormaliseCases runs testdata/normalise/cases.json.
func TestNormaliseCases(t *testing.T) {
	data, err := os.ReadFile("testdata/normalise/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		About string `json:"about"`
		IDs   map[string]struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"ids"`
		Cases []struct {
			caseMeta
			RuntimeErrors []json.RawMessage `json:"runtime_errors"`
			Raw           json.RawMessage   `json:"raw"`
			Want          json.RawMessage   `json:"want"`
		} `json:"cases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	ids := map[string]SitePath{}
	var pairs []string
	for name, v := range file.IDs {
		p, err := ParseSitePath(v.Path)
		if err != nil {
			t.Fatal(err)
		}
		ids[v.ID] = p
		pairs = append(pairs, "${"+name+"}", v.ID)
	}
	r := strings.NewReplacer(pairs...)
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if len(c.Spec) == 0 || !validProvenances[c.Provenance] {
				t.Fatalf("case needs spec IDs and a provenance (%q)", c.Provenance)
			}
			n := &Normaliser{IDs: ids}
			for _, e := range c.RuntimeErrors {
				n.RuntimeErrors = append(n.RuntimeErrors, json.RawMessage(r.Replace(string(e))))
			}
			got, err := n.Value(json.RawMessage(r.Replace(string(c.Raw))))
			if err != nil {
				t.Fatal(err)
			}
			g, err := IndentJSON(got)
			if err != nil {
				t.Fatal(err)
			}
			w, err := IndentJSON(c.Want)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(g, w) {
				t.Fatalf("got\n%s\nwant\n%s", g, w)
			}
		})
	}
}

// TestMaskPaths checks fixture-declared stack and mask paths (OR-V3 clause
// c, OR-14 item 8, OR-29).
func TestMaskPaths(t *testing.T) {
	var o Observation
	if err := json.Unmarshal([]byte(`{"status":"success","result":{"env":{"WM_WORKSPACE":"wl-1a2b3c-1","WM_BASE_URL":"http://localhost:8000"},"trace":"at x"},"steps":{"m:a":{"status":"success","args":{},"result":{"base_url":"http://localhost:8000","n":1}}}}`), &o); err != nil {
		t.Fatal(err)
	}
	got, err := MaskPaths(o, []string{"/result/trace", "/result/env"}, []string{"/result/env/WM_WORKSPACE", "/steps/m:a/result/base_url", "/steps/m:b/result"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	g, err := IndentJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	w, err := IndentJSON([]byte(`{"status":"success","result":{"env":{"WM_WORKSPACE":"<masked>","WM_BASE_URL":"http://localhost:8000"},"trace":"<stack>"},"steps":{"m:a":{"status":"success","args":{},"result":{"base_url":"<masked>","n":1}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g, w) {
		t.Fatalf("got\n%s\nwant\n%s", g, w)
	}
}

// TestStripLocations is the location-suffix rule of ADR 0011: a generated
// Internal error whose oracle text ends in a Rust location compares equal
// to windmill-lite's text, which has none (D14), while two user errors that
// differ only in such a suffix still differ.
func TestStripLocations(t *testing.T) {
	// spec: OR-14, OR-27
	const job = "0190a0c4-8c11-7b1e-9f3a-0123456789ab"
	ids := map[string]SitePath{job: SitePath{}.Child(Module("a"))}
	internal := func(loc string) string {
		return `{"error":{"name":"InternalErr","message":"Internal: Could not add completed job ` + job +
			`: error returned from database: unsupported Unicode escape sequence` + loc + `"}}`
	}
	user := func(loc string) string {
		return `{"error":{"name":"Error","message":"boom` + loc + `","stack":"s","step_id":"a"}}`
	}
	observe := func(t *testing.T, result string) Observation {
		t.Helper()
		n := &Normaliser{IDs: ids}
		v, err := n.Value(json.RawMessage(result))
		if err != nil {
			t.Fatal(err)
		}
		return Observation{Status: RunFailure, Result: v, Steps: map[SitePath]StepObservation{
			ids[job]: {Status: StepFailure, Args: json.RawMessage(`{}`), Result: v},
		}}
	}
	differences := func(t *testing.T, got, oracle Observation, at []string) []Difference {
		t.Helper()
		stripped, err := StripLocations(oracle, at)
		if err != nil {
			t.Fatal(err)
		}
		diffs, err := Compare(got, stripped, CompareOptions{WithRecording: true})
		if err != nil {
			t.Fatal(err)
		}
		return diffs
	}
	generated := []string{"/result", "/steps/m:a/result"}

	t.Run("generated internal error", func(t *testing.T) {
		got, oracle := observe(t, internal("")), observe(t, internal(" @windmill-worker/src/result_processor.rs:412:17"))
		if !strings.HasSuffix(string(oracle.Result), ` sequence <loc>"}}`) {
			t.Fatalf("the normaliser did not mask the location: %s", oracle.Result)
		}
		if d := differences(t, got, oracle, generated); len(d) != 0 {
			t.Fatalf("differences after stripping: %v", d)
		}
		// Without the rule, the two still differ: the rule is what equates
		// them.
		if d := differences(t, got, oracle, nil); len(d) != 2 {
			t.Fatalf("differences without stripping: %v", d)
		}
		// Only the pointers given are stripped.
		if d := differences(t, got, oracle, []string{"/result"}); len(d) != 1 || d[0].Pointer != "/steps/m:a/result/error/message" {
			t.Fatalf("differences stripping /result only: %v", d)
		}
	})
	t.Run("user errors", func(t *testing.T) {
		got, oracle := observe(t, user(" @a.rs:1:2")), observe(t, user(" @b.rs:3:4"))
		if d := differences(t, got, oracle, generated); len(d) != 2 {
			t.Fatalf("user errors that differ in a location suffix compared equal: %v", d)
		}
	})
	t.Run("a user's literal marker", func(t *testing.T) {
		// The normaliser escapes a literal "<loc>" in user text, so it is
		// never taken for the marker.
		got, oracle := observe(t, internal("")), observe(t, internal(" <loc>"))
		if !strings.HasSuffix(string(oracle.Result), ` sequence <<loc>"}}`) {
			t.Fatalf("the literal was not escaped: %s", oracle.Result)
		}
		if d := differences(t, got, oracle, generated); len(d) != 2 {
			t.Fatalf("an escaped literal was stripped: %v", d)
		}
	})
	t.Run("chaining errors and the stop string", func(t *testing.T) {
		// spec: FX-15/a, FX-15/b, FX-21/a, JE-35/b, FX-50
		const eval = `QuickJS evaluation error: Error: nope is not defined\n    at <anonymous> (eval_script:1:35)\n`
		object := func(name, msg string) string { return `{"error":{"name":"` + name + `","message":"` + msg + `"}}` }
		str := func(msg string) string { return `{"error":"` + msg + `"}` }
		for _, c := range []struct {
			name, wl, oracle string
			equal            bool
		}{
			{"later transition, one suffix", object("InternalError", "Error: "+eval), object("InternalError", "Error: "+eval+" @worker_flow.rs:2451:11"), true},
			{"pickup, two prefixes and two suffixes", object("Anyhow", "Error: Error: "+eval), object("Anyhow", "Error: Error: "+eval+" @worker_flow.rs:2451:11 @worker.rs:3822:13"), true},
			{"pickup, a typed error", object("Anyhow", "Error: Timeout value cannot be negative"), object("Anyhow", "Error: Timeout value cannot be negative @worker.rs:3822:13"), true},
			{"the parenthesised form", object("Anyhow", "Error: not found"), object("Anyhow", "Error: not found (jobs.rs:9074)"), true},
			{"a throwing stop_after_if", str("Error: " + eval), str("Error: " + eval + " @worker_flow.rs:2451:11"), true},
			// One suffix per prefix, no more: a user's own text that ends
			// like a location stays.
			{"only as many as the prefixes", object("Anyhow", "Error: x @u.rs:1:2"), object("Anyhow", "Error: x @u.rs:1:2 @worker.rs:3822:13"), true},
			{"fewer suffixes than prefixes", object("Anyhow", "Error: Error: x"), object("Anyhow", "Error: Error: x @worker.rs:3822:13"), false},
			{"a typed message has no prefix", object("InternalError", "Expected an integer, found: -1"), object("InternalError", "Expected an integer, found: -1 @worker.rs:1:2"), false},
			{"another error name", object("Error", "Error: x"), object("Error", "Error: x @worker.rs:3822:13"), false},
		} {
			got, oracle := observe(t, c.wl), observe(t, c.oracle)
			d := differences(t, got, oracle, generated)
			if (len(d) == 0) != c.equal {
				t.Errorf("%s: differences %v", c.name, d)
			}
			// Only where windmill-lite generated the value.
			if d := differences(t, got, oracle, nil); c.wl != c.oracle && len(d) == 0 {
				t.Errorf("%s: compared equal without the rule", c.name)
			}
		}
	})
}

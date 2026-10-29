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
}

// TestNormaliseText normalises response bodies as text: the spelling is
// kept (spaces, member order), walked job ids become placeholders, a
// runtime error's stack becomes the marker where the body writes it as a
// JSON string, and nothing else is touched: another stack, an unwalked id,
// user text that looks like a marker (escaped, so the result stays
// injective).
func TestNormaliseText(t *testing.T) {
	const job = "10000000-0000-4000-8000-000000000002"
	stack := "Error: z\n    at main (/tmp/windmill/wk/" + job + "/main.ts:2:9)"
	stackJSON, _ := json.Marshal(stack)
	errJSON := `{"error":{"name":"Error","message":"z","stack":` + string(stackJSON) + `,"step_id":"a"}}`
	n := &Normaliser{
		IDs:           map[string]SitePath{job: SitePath{}.Child(Module("a"))},
		RuntimeErrors: []json.RawMessage{json.RawMessage(errJSON)},
	}
	for _, c := range []struct{ name, raw, want string }{
		{"spaced object", `{"a": 2, "b": 1}`, `{"a": 2, "b": 1}`},
		{"unquoted string", `ok`, `ok`},
		{"empty", ``, ``},
		{"job id", `{"job": "` + job + `"}`, `{"job": "<job:m:a>"}`},
		{"stack", `{"error": {"name": "Error", "message": "z", "stack": ` + string(stackJSON) + `, "step_id": "a"}}`, `{"error": {"name": "Error", "message": "z", "stack": "<stack>", "step_id": "a"}}`},
		{"another stack", `{"stack": "Error: y\n    at x"}`, `{"stack": "Error: y\n    at x"}`},
		{"unwalked id", `"20000000-0000-4000-8000-000000000009"`, `"20000000-0000-4000-8000-000000000009"`},
		{"marker-like text", `"<stack>"`, `"<<stack>"`},
	} {
		got, err := n.Text(c.raw)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

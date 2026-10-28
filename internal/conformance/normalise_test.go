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

package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type compareCase struct {
	caseMeta
	Options struct {
		Subset         bool     `json:"subset"`
		StrictKeyOrder bool     `json:"strict_key_order"`
		Unordered      []string `json:"unordered"`
		WithRecording  bool     `json:"with_recording"`
	} `json:"options"`
	Got         Observation `json:"got"`
	Want        Observation `json:"want"`
	Differences []string    `json:"differences"`
}

// TestCompareCases runs testdata/compare/cases.json.
func TestCompareCases(t *testing.T) {
	data, err := os.ReadFile("testdata/compare/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		About string        `json:"about"`
		Cases []compareCase `json:"cases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if len(c.Spec) == 0 {
				t.Fatal("case cites no spec ID")
			}
			if !validProvenances[c.Provenance] {
				t.Fatalf("provenance %q", c.Provenance)
			}
			diffs, err := Compare(c.Got, c.Want, CompareOptions{
				Subset:         c.Options.Subset,
				StrictKeyOrder: c.Options.StrictKeyOrder,
				Unordered:      c.Options.Unordered,
				WithRecording:  c.Options.WithRecording,
			})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(diffs))
			for _, d := range diffs {
				got = append(got, d.String())
			}
			if !reflect.DeepEqual(got, c.Differences) {
				t.Fatalf("differences:\n got  %q\n want %q", got, c.Differences)
			}
		})
	}
}

func TestPointers(t *testing.T) {
	for _, p := range []string{"", "/a", "/steps/m:l~1i:0~1m:x/result", "/a~0b/0", "/"} {
		toks, err := SplitPointer(p)
		if err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		if JoinPointer(toks) != p {
			t.Errorf("%q round-trips to %q", p, JoinPointer(toks))
		}
	}
	toks, _ := SplitPointer("/steps/m:l~1i:0~1m:x")
	if !reflect.DeepEqual(toks, []string{"steps", "m:l/i:0/m:x"}) {
		t.Errorf("tokens %q", toks)
	}
	for _, p := range []string{"a", "/a~2", "/a~"} {
		if _, err := SplitPointer(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestIndentJSONKeepsOrderAndText(t *testing.T) {
	in := `{"z":1.0,"a":["<stack>",{"y":null,"b":true}],"e":{},"f":[]}`
	out, err := IndentJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"z\": 1.0,\n  \"a\": [\n    \"<stack>\",\n    {\n      \"y\": null,\n      \"b\": true\n    }\n  ],\n  \"e\": {},\n  \"f\": []\n}\n"
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

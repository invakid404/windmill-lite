package shapes

import (
	"encoding/json"
	"testing"
)

// TestBriefStructure checks the brief's families against the structure the
// brief states, not only their sizes (fix round 1 of the study's review):
// F4's flat record and its top-level members, the suggested F6 composite
// (ten documents, each with two extraction items of 300 lines and 300
// coordinate entries, a 60 KB dump and a 100 KB markdown string), F7's
// items, and the replicated list. Tiers sized by their structure carry
// less than 1 KiB of padding.
func TestBriefStructure(t *testing.T) {
	tier := func(fam, name string) Tier {
		f, _ := BriefNamed(fam)
		for _, tr := range f.Tiers {
			if tr.Name == name {
				return tr
			}
		}
		t.Fatalf("no tier %s %s", fam, name)
		return Tier{}
	}
	decode := func(tr Tier, v any) {
		t.Helper()
		b := tr.Payload()
		if len(b) != tr.Size {
			t.Fatalf("%d bytes, not %d", len(b), tr.Size)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		tier string
		keys int
	}{{"typical", 24}, {"p90", 29}, {"worst", 29}} {
		var m map[string]any
		decode(tier("F4", c.tier), &m)
		if len(m) != c.keys || m["text"] == nil {
			t.Errorf("F4 %s: %d top-level members, want %d with the text field", c.tier, len(m), c.keys)
		}
	}
	type extraction struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
		Text     string           `json:"text"`
		Markdown string           `json:"markdown"`
		Items    []map[string]any `json:"items"`
	}
	full := func(where string, x extraction) {
		t.Helper()
		if len(x.Data.Items) != 300 || len(x.Items) != 300 || len(x.Data.Items[0]) != 10 || len(x.Text) < 60000 || len(x.Markdown) < 100000 {
			t.Errorf("%s: %d lines of %d keys, %d coordinates, dump %d, markdown %d", where, len(x.Data.Items), len(x.Data.Items[0]), len(x.Items), len(x.Text), len(x.Markdown))
		}
	}
	var docs []struct {
		Record      map[string]any `json:"record"`
		Classes     []any          `json:"classes"`
		Topics      []any          `json:"topics"`
		Entities    []any          `json:"entities"`
		Flags       []any          `json:"flags"`
		Body        string         `json:"body"`
		FullText    string         `json:"full_text"`
		Extractions []extraction   `json:"extractions"`
	}
	decode(tier("F6", "worst"), &docs)
	if len(docs) != 10 {
		t.Errorf("F6 worst: %d documents, want 10", len(docs))
	}
	for i, d := range docs {
		if len(d.Record) != 25 || len(d.Record["text"].(string)) < 30000 || len(d.Body) < 35000 || len(d.FullText) < 150000 || len(d.Extractions) != 2 ||
			d.Classes == nil || d.Topics == nil || d.Entities == nil || d.Flags == nil {
			t.Errorf("F6 worst document %d: %d record keys, body %d, full text %d, %d extractions", i, len(d.Record), len(d.Body), len(d.FullText), len(d.Extractions))
		}
		for _, x := range d.Extractions {
			full("F6 worst extraction", x)
		}
	}
	for _, c := range []struct {
		tier string
		n    int
	}{{"p90", 2}, {"worst", 16}} {
		var xs []extraction
		decode(tier("F7", c.tier), &xs)
		if len(xs) != c.n {
			t.Errorf("F7 %s: %d items, want %d", c.tier, len(xs), c.n)
		}
		for _, x := range xs {
			full("F7 "+c.tier, x)
		}
	}
	var l []string
	decode(tier("L", "worst"), &l)
	if len(l) != 512 || len(l[0]) > 64 {
		t.Errorf("L worst: %d strings, the first %d bytes", len(l), len(l[0]))
	}
	// Padding: a tier sized by its structure cannot be built 1 KiB smaller.
	for _, fam := range Brief {
		for _, tr := range fam.Tiers {
			if tr.Size%kib != 0 || tr.Size < 4*kib {
				continue
			}
			switch fam.Name + "/" + tr.Name {
			case "F6/p90", "F6/worst", "F7/typical", "F7/p90", "F7/worst", "L/typical", "L/worst":
				func() {
					defer func() { _ = recover() }()
					tr.make(tr.Size - kib)
					t.Errorf("%s %s: builds in %d bytes, so its padding exceeds 1 KiB", fam.Name, tr.Name, tr.Size-kib)
				}()
			}
		}
	}
}

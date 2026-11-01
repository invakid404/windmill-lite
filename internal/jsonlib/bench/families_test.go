package bench

import (
	"github.com/invakid404/windmill-lite/internal/jsonlib/shapes"

	"bytes"
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// TestFamilies checks that each family makes one valid JSON object of
// exactly the size asked for, in valid UTF-8 and without \u0000.
func TestFamilies(t *testing.T) {
	for _, f := range shapes.Families {
		for _, n := range []int{1 << 10, 16 << 10, 256 << 10, 1 << 20, 3 << 20} {
			b := shapes.Make(f, n)
			if len(b) != n || !json.Valid(b) || !utf8.Valid(b) || bytes.Contains(b, []byte(`\u0000`)) || b[0] != '{' {
				t.Errorf("%s at %d: %d bytes, valid %v", f, n, len(b), json.Valid(b))
			}
			if !bytes.Equal(b, shapes.Make(f, n)) {
				t.Errorf("%s at %d: not deterministic", f, n)
			}
		}
	}
}

// TestBrief checks the brief's families as TestFamilies checks the
// generic ones.
func TestBrief(t *testing.T) {
	for _, f := range shapes.Brief {
		for _, tier := range f.Tiers {
			b := tier.Payload()
			if len(b) != tier.Size || !json.Valid(b) || !utf8.Valid(b) || bytes.Contains(b, []byte(`\u0000`)) {
				t.Errorf("%s %s: %d bytes of %d, valid %v", f.Name, tier.Name, len(b), tier.Size, json.Valid(b))
			}
			var v any
			json.Unmarshal(b, &v)
			t.Logf("%s %-7s %8d B  depth %d  top %c", f.Name, tier.Name, len(b), depth(v), b[0])
		}
	}
}

func depth(v any) int {
	switch x := v.(type) {
	case map[string]any:
		d := 0
		for _, e := range x {
			d = max(d, depth(e))
		}
		return d + 1
	case []any:
		d := 0
		for _, e := range x {
			d = max(d, depth(e))
		}
		return d + 1
	}
	return 0
}

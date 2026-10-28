package yamlx_test

import (
	"reflect"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// TestQOF6LibraryResolution records the answer to Q-OF-6: the scalar forms
// that go.yaml.in/yaml/v3 resolves differently from the CLI. yamlx parses
// with that library but never uses its resolution, so each row also checks
// that yamlx gives the CLI's value.
func TestQOF6LibraryResolution(t *testing.T) {
	for _, c := range []struct {
		src     string
		library any    // what yaml.Unmarshal into an interface gives
		cli     string // what yamlx gives, the CLI's JSON
	}{
		{"017", 15, "17"},
		{"1_000", 1000, `"1_000"`},
		{"0b11", 3, `"0b11"`},
		{"-0x1F", -31, `"-0x1F"`},
		{"0X1F", 31, `"0X1F"`},
		{"! 017", 15, `"017"`},
		{"9007199254740993", 9007199254740993, "9007199254740992"},
		{"2024-01-01", time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC), `"2024-01-01"`},
		{
			"x: &a {k: 1}\ny:\n  <<: *a\n",
			map[string]any{"x": map[string]any{"k": 1}, "y": map[string]any{"k": 1}},
			`{"x":{"k":1},"y":{"<<":{"k":1}}}`,
		},
	} {
		var v any
		if err := yaml.Unmarshal([]byte(c.src), &v); err != nil {
			t.Fatalf("%q: library: %v", c.src, err)
		}
		if !reflect.DeepEqual(v, c.library) {
			t.Errorf("%q: library gives %#v, the table says %#v", c.src, v, c.library)
		}
		got, err := yamlx.ToJSON([]byte(c.src))
		if err != nil || string(got) != c.cli {
			t.Errorf("%q: yamlx gives %s (%v), want %s", c.src, got, err, c.cli)
		}
	}
}

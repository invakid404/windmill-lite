package bundle

import "testing"

// TestImportBase checks the import base of inline scripts (WR-29); the
// build cases of conformance/build/wr-29.json resolve imports against it.
func TestImportBase(t *testing.T) {
	for _, c := range []struct {
		flow, module, explicit, want string
	}{
		{"f/a/fl", "s", "", "f/a/fl/s"},
		{"f/a/fl", "preprocessor", "", "f/a/fl/preprocessor"},
		{"f/a/fl", "failure", "", "f/a/fl/failure"},
		{"f/a/fl", "s", "f/elsewhere/entry", "f/elsewhere/entry"},
		// Windmill names a step inside a body with the body's segment; the
		// collapse gives it the flow's directory, as at top level.
		{"f/a/fl", "s", "f/a/fl/forloop-0/s", "f/a/fl/flow"},
		{"f/a/fl", "s", "f/a/fl/branchone-2/branchall-0/s", "f/a/fl/flow"},
		{"f/a/fl", "s", "f/a/fl/loop-12/s", "f/a/fl/flow"},
		{"f/a/fl", "s", "f/a/forloopish/s", "f/a/forloopish/s"},
		{"f/a/fl", "s", "f/a/fl/forloop-x/s", "f/a/fl/forloop-x/s"},
	} {
		if got := ImportBase(c.flow, c.module, c.explicit); got != c.want {
			t.Errorf("ImportBase(%q, %q, %q) = %q, want %q", c.flow, c.module, c.explicit, got, c.want)
		}
	}
}

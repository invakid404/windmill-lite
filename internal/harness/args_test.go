package harness

import (
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// WR-5: args.json, and what the script receives, are the arguments without
// _MODULES and _TEMP_SCRIPT_REFS; every other member keeps its raw text
// and its place, underscore keys included.
func TestScriptArgs(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"a":1,"_MODULES":["internal"],"_TEMP_SCRIPT_REFS":{"x":1},"_other":2}`, `{"a":1,"_other":2}`},
		// Raw spelling and order kept, whitespace around a dropped member gone.
		{`{"b":1.50, "_MODULES" : [1], "a":1e2 ,"_TEMP_SCRIPT_REFS":null,"_ENTRYPOINT_OVERRIDE":"main"}`, `{"b":1.50,"a":1e2,"_ENTRYPOINT_OVERRIDE":"main"}`},
		{` { "_MODULES":1 } `, `{}`},
		// A key written with an escape is the same key.
		{`{"_MODULES":1,"a":"\n"}`, `{"a":"\n"}`},
		// Nested keys and string values that look alike stay.
		{`{"a":{"_MODULES":1},"b":"_TEMP_SCRIPT_REFS"}`, `{"a":{"_MODULES":1},"b":"_TEMP_SCRIPT_REFS"}`},
		// Nothing to drop: the text as it is.
		{`{"a" : 1, "b":"\""}`, `{"a" : 1, "b":"\""}`},
		{`{}`, `{}`},
		// Not an object, or not JSON: kept, for the harness to refuse.
		{`[1]`, `[1]`},
		{`{"_MODULES":`, `{"_MODULES":`},
		// An object that does not close with its own brace, or that a
		// value follows, is not valid either, though a dropped key would
		// leave the rest looking whole (PF2 review round 2, P3).
		{`{"_MODULES":1]`, `{"_MODULES":1]`},
		{`{"a":1,"_MODULES":1`, `{"a":1,"_MODULES":1`},
		{`{"_MODULES":1} true`, `{"_MODULES":1} true`},
		{`{"_MODULES":1}{}`, `{"_MODULES":1}{}`},
		{`{"a":1,"_MODULES":1} x`, `{"a":1,"_MODULES":1} x`},
	} {
		if got := string(scriptArgs(jsonval.FromProducer([]byte(c.in))).Producer()); got != c.want {
			t.Errorf("%s: %s, want %s", c.in, got, c.want)
		}
	}
}

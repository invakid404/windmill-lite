package engine

import (
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonlib"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// TestStudyErrorNameCompatibility checks that errorName, which H7's fatal
// error names match against, decodes as encoding/json does whichever
// library the build swaps in (JSON-library study, branch
// study/json-libraries; fix round 1 of its review, P1): a later null field
// leaves the earlier value, and field names match without regard to case
// but not across a dash or an underscore. Strict for builds that claim
// encoding/json's behaviour; logged for the others.
func TestStudyErrorNameCompatibility(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{`{"error":{"name":"Fatal"}}`, "Fatal"},
		{`{"ERROR":{"NAME":"Fatal"}}`, "Fatal"},
		{`{"error":{"name":"Fatal","name":null}}`, "Fatal"},
		{`{"error":{"name":"Fatal"},"error":null}`, "Fatal"},
		{`{"e-rror":{"name":"Fatal"}}`, ""},
		{`{"error":{"n_ame":"Fatal"}}`, ""},
		{`{"error":{"name":7}}`, ""},
	} {
		if got := errorName(jsonval.FromProducer([]byte(c.text))); got != c.want {
			if jsonlib.V1Compatible {
				t.Errorf("errorName(%s) = %q, want %q (%s)", c.text, got, c.want, jsonlib.Name)
			} else {
				t.Logf("DIFF errorName(%s) = %q, encoding/json %q (%s)", c.text, got, c.want, jsonlib.Name)
			}
		}
	}
}

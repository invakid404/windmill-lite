package wl

import (
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonlib"
)

// TestStudyOutcomeReplyCompatibility checks that a plugin's answer to an
// interception or a provider decodes as encoding/json decodes it, as doHeld
// decodes it, whichever library the build swaps in (JSON-library study;
// fix round 1 of its review, P1). Strict for builds that claim
// encoding/json's behaviour; logged for the others.
func TestStudyOutcomeReplyCompatibility(t *testing.T) {
	for _, c := range []struct {
		text            string
		decline, failed bool
	}{
		{`{"decline":true}`, true, false},
		{`{"Decline":true}`, true, false},
		{`{"decline":true,"decline":null}`, true, false},
		{`{"de-cline":true}`, false, false},
		{`{"failed":true,"failed":null}`, false, true},
		{`{"result":{"a":1},"fail_ed":true}`, false, false},
	} {
		var r outcomeReply
		if err := jsonlib.Unmarshal([]byte(c.text), &r); err != nil || r.Decline != c.decline || r.Failed != c.failed {
			if jsonlib.V1Compatible {
				t.Errorf("outcomeReply from %s = decline %v failed %v (%v), want %v %v (%s)", c.text, r.Decline, r.Failed, err, c.decline, c.failed, jsonlib.Name)
			} else {
				t.Logf("DIFF outcomeReply from %s = decline %v failed %v (%v), encoding/json %v %v (%s)", c.text, r.Decline, r.Failed, err, c.decline, c.failed, jsonlib.Name)
			}
		}
	}
}

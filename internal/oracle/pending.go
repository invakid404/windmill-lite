package oracle

import "github.com/invakid404/windmill-lite/internal/conformance"

// PendingVerdict judges a run that the retarget ledger's oracle section
// lists, with entry e (`wltest record --pending`): whether the run fails
// the session, and a note to print below its line. The ledger exists only
// on the retarget branch (cmd/wltest/README.md, "The retarget ledger").
//
// A listed run may be new, changed, expect_mismatch, failed on the oracle
// or unstable: its recording is what its milestone brings. A local failure
// (LocalFailure: a comparison, or a recording, observation or raw capture
// that could not be placed or written) is the session's own, so it fails
// as always: no milestone clears it, and the run's evidence is missing. A
// listed run that is unchanged has its recording, so in check mode its
// entry is stale and fails the session. A refusal fails as always.
func (r RunReport) PendingVerdict(check bool, e conformance.LedgerEntry) (fails bool, note string) {
	if r.LocalFailure {
		return true, "not pending: the run failed in this session, not on the oracle (" + e.Marker() + ")"
	}
	switch r.Outcome {
	case OutcomeNew, OutcomeChanged, OutcomeExpectMismatch, OutcomeFailed, OutcomeUnstable:
		return false, "pending: " + e.Marker()
	case OutcomeUnchanged:
		return check, "stale: the run is unchanged; remove its entry from the retarget ledger (" + e.Marker() + ")"
	}
	return r.Failed(check), ""
}

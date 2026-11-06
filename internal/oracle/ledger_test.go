package oracle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// pendingRecordings returns the retarget ledger's oracle section
// (conformance.LedgerPath): the runs whose recording is pending, which the
// recording tests accept without one. On main there is no ledger, and every
// run needs its recording.
func pendingRecordings(t *testing.T) map[string]conformance.LedgerEntry {
	t.Helper()
	l, err := conformance.LoadLedger(filepath.Join(repoRoot(t), filepath.FromSlash(conformance.LedgerPath)))
	if err != nil {
		t.Fatal(err)
	}
	return l.Oracle
}

// runKey names a run as the ledger does: the fixture directory, or the
// expression file a fixture was lifted from, from the repository root,
// then the run.
func runKey(t *testing.T, f *conformance.Fixture, run string) string {
	t.Helper()
	abs, err := filepath.Abs(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(repoRoot(t), abs)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel) + "/" + run
}

// A batch run whose recording the ledger lists as pending needs none: the
// batch check counts it as recording its cases and passes over the claims
// that read its recording, while the batch's recorded runs are checked as
// always. The fixture is a copy of the smoke fixture without the recording
// of its run fail.
func TestPendingRunsNeedNoRecording(t *testing.T) {
	root := t.TempDir()
	dir := "conformance/flows/oracle/smoke"
	if err := os.CopyFS(filepath.Join(root, dir), os.DirFS(filepath.Join(flowsRoot, "oracle", "smoke"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, dir, "recorded", "fail.json")); err != nil {
		t.Fatal(err)
	}
	b := batch{Batch: "E2", Fixtures: []string{dir}, Cases: []string{"OR-20/b"}}
	b.Texts = append(b.Texts, struct {
		Case     string   `json:"case"`
		Run      string   `json:"run"`
		Pointer  string   `json:"pointer"`
		Contains []string `json:"contains"`
		Excludes []string `json:"excludes"`
	}{Case: "OR-20/b", Run: dir + "/fail", Pointer: "/result/error/message", Contains: []string{"too big"}})
	inCatalogue := map[string]bool{}
	for _, c := range []string{"OR-20/a", "OR-20/b", "OR-12/a", "OR-11/a", "OR-10/c"} {
		inCatalogue[c] = true
	}
	pending := map[string]conformance.LedgerEntry{dir + "/fail": {Milestone: "W5b", Reason: "step errors gain job_id"}}
	checkBatch(t, root, b, inCatalogue, pending)
}

// PendingVerdict on real sessions: a listed run that does not match its
// expect saves its observation in the side directory and does not fail the
// session, nor does a listed run that fails on the oracle; a listed run
// whose observation, recording or raw capture the session cannot place or
// write fails it, since that failure is local and leaves the run's evidence
// missing.
func TestPendingVerdictOnSessions(t *testing.T) {
	e := conformance.LedgerEntry{Milestone: "W5b", Reason: "step errors gain job_id"}
	ctx := context.Background()
	blocked := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name string
		// setup adjusts the rig and the session, and returns the runs it
		// judges.
		setup         func(t *testing.T, rig *sessionRig, s *Session) []string
		outcome, note string
		local, fails  bool
		saved         bool // the run's observation is in the side directory
	}{
		{
			name: "a mismatch saves its observation",
			setup: func(t *testing.T, rig *sessionRig, s *Session) []string {
				rig.oracle.tag = "ho"
				s.MismatchDir = t.TempDir()
				return []string{"ok", "fail"}
			},
			outcome: OutcomeExpectMismatch, note: "pending: " + e.Marker(), saved: true,
		},
		{
			name: "a failure on the oracle",
			setup: func(t *testing.T, rig *sessionRig, s *Session) []string {
				rig.oracle.hang = true
				s.Repeat = 1
				s.RunTimeout, s.minRunTimeout = 50*time.Millisecond, time.Millisecond
				return []string{"ok", "fail", "effects"}
			},
			outcome: OutcomeFailed, note: "pending: " + e.Marker(),
		},
		{
			name: "an observation that cannot be written",
			setup: func(t *testing.T, rig *sessionRig, s *Session) []string {
				rig.oracle.tag = "ho"
				s.MismatchDir = blocked(t)
				return []string{"ok", "fail"}
			},
			outcome: OutcomeFailed, local: true, fails: true, note: "not pending: the run failed in this session",
		},
		{
			name: "a recording that cannot be written",
			setup: func(t *testing.T, rig *sessionRig, s *Session) []string {
				s.OutDir = blocked(t)
				return []string{"ok", "fail", "effects"}
			},
			outcome: OutcomeFailed, local: true, fails: true, note: "not pending: ",
		},
		{
			name: "a recording that --out cannot place",
			setup: func(t *testing.T, rig *sessionRig, s *Session) []string {
				s.OutDir, s.RepoRoot = t.TempDir(), t.TempDir()
				return []string{"ok", "fail", "effects"}
			},
			outcome: OutcomeFailed, local: true, fails: true, note: "not pending: ",
		},
		{
			name: "a raw capture that cannot be written",
			setup: func(t *testing.T, rig *sessionRig, s *Session) []string {
				s.RawDir = blocked(t)
				return []string{"ok", "fail", "effects"}
			},
			outcome: OutcomeFailed, local: true, fails: true, note: "not pending: ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRig(t)
			s := rig.session(20 * GiB)
			s.Check, s.RepoRoot = true, filepath.Dir(rig.fixture)
			names := tc.setup(t, rig, s)
			rep, err := s.Run(ctx, []*conformance.Fixture{rig.load(t)})
			if err != nil {
				t.Fatal(err)
			}
			got := outcomes(rep)
			for _, name := range names {
				r := got[name]
				fails, note := r.PendingVerdict(true, e)
				if r.Outcome != tc.outcome || r.LocalFailure != tc.local || fails != tc.fails || !strings.HasPrefix(note, tc.note) {
					t.Errorf("%s: %+v; verdict %v, %q", name, r, fails, note)
				}
				if !r.Failed(true) {
					t.Errorf("%s: an unlisted %s run does not fail the session", name, r.Outcome)
				}
				if tc.saved {
					if _, err := os.Stat(r.Observation); r.Observation == "" || err != nil {
						t.Errorf("%s: observation %q not saved: %v", name, r.Observation, err)
					}
				}
			}
		})
	}
}

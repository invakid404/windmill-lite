package oracle

import (
	"os"
	"path/filepath"
	"testing"

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

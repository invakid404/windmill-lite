package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
)

// endedRunMessage is the 501 windmill-lite answers a job read of a job of
// a run that has ended with (deviation D22 of ADR 0001).
const endedRunMessage = "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/get/{id}: the job's run has ended, and windmill-lite keeps no record of the jobs of a run that has ended"

// TestEndedRunJobRead is deviation D22's check, on the fixture
// conformance/flows/client-api/job-reads-ended, whose recording holds
// Windmill's answer (WA-19/i): a step dispatches a script as a run of its
// own, waits for it, and reads its job once that run has ended.
// windmill-lite keeps no record of the jobs of a run that has ended, so the
// read answers 501 and the 501 policy fails the step with
// UnsupportedPlatformCall, naming the read and the reason, in fresh and in
// warm mode. The flow suite does not require the run, which differs from
// its recording by that deviation.
func TestEndedRunJobRead(t *testing.T) {
	checkUnservedReads(t, "job-reads-ended", map[string]unserved{
		"ended": {endedRunMessage, []string{"GET /w/{ws}/jobs_u/get/{id}", "GET /w/{ws}/jobs_u/get_args/{id}"}},
	})
}

// largeValueMessage is the 501 windmill-lite answers a job read of a job
// whose arguments or result may take 90000 bytes or more stored with
// (WA-19/o).
const largeValueMessage = "Not implemented in windmill-lite: GET /w/{ws}/jobs_u/get/{id}: the job's arguments or result may take 90000 bytes or more stored, where Windmill's answer depends on PostgreSQL's compression"

// TestLargeValueJobRead checks the fixture
// conformance/flows/client-api/job-reads-large's runs above Windmill's
// replacement size, whose recordings hold Windmill's answers (WA-19/o): a
// value of 92,000 characters that compresses, which Windmill answers
// whole, and one that does not, which it replaces. windmill-lite does not
// reproduce PostgreSQL's compression, so it answers neither: the reads
// answer 501 and the 501 policy fails the step with UnsupportedPlatformCall,
// in fresh and in warm mode. The flow suite requires the fixture's run
// below that size, whole, and not these two.
func TestLargeValueJobRead(t *testing.T) {
	// Step r reads v's job and w's, each answering 501.
	calls := []string{"GET /w/{ws}/jobs_u/get/{id}", "GET /w/{ws}/jobs_u/get/{id}"}
	checkUnservedReads(t, "job-reads-large", map[string]unserved{
		"compressible":   {largeValueMessage, calls},
		"incompressible": {largeValueMessage, calls},
		"whole":          {},
	})
}

// unserved is how a run of a fixture fails on windmill-lite: its step's
// UnsupportedPlatformCall error, with message and the calls it names; the
// zero value is a run that passes.
type unserved struct {
	message string
	calls   []string
}

// checkUnservedReads runs the client-api fixture name in fresh and in warm
// mode and checks that each run fails as want says, or passes.
func checkUnservedReads(t *testing.T, name string, want map[string]unserved) {
	bun := bunOrSkip(t)
	root := repo(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	dir := filepath.Join(flowsDir, "client-api", name)
	for _, mode := range []string{conformance.ModeFresh, conformance.ModeWarm} {
		t.Run(mode, func(t *testing.T) {
			fc := &flowChecker{bun: bun, repo: root, mode: mode, js: js}
			n := 0
			for _, fr := range fc.runFixtures(context.Background(), []string{dir}) {
				if fr.err != nil {
					t.Fatalf("fixture %s does not load: %v", fr.dir, fr.err)
				}
				for _, r := range fr.runs {
					n++
					w, ok := want[r.run]
					switch {
					case !ok:
						t.Errorf("run %s is not expected", r.run)
						continue
					case w.message == "":
						if len(r.problems) > 0 {
							t.Errorf("run %s:\n  %s\nharness diagnostics:\n%s", r.run, strings.Join(r.problems, "\n  "), r.diag)
						}
						continue
					}
					var observed struct {
						Status string `json:"status"`
						Result struct {
							Error struct {
								Name    string   `json:"name"`
								Message string   `json:"message"`
								Calls   []string `json:"calls"`
							} `json:"error"`
						} `json:"result"`
					}
					found := false
					for _, p := range r.problems {
						if text, ok := strings.CutPrefix(p, "observed "); ok {
							found = json.Unmarshal([]byte(text), &observed) == nil
						}
					}
					e := observed.Result.Error
					if !found || observed.Status != "failure" || e.Name != "UnsupportedPlatformCall" || e.Message != w.message || !slices.Equal(e.Calls, w.calls) {
						t.Errorf("run %s: %+v\n  %s\nharness diagnostics:\n%s", r.run, observed, strings.Join(r.problems, "\n  "), r.diag)
					}
				}
			}
			if n != len(want) {
				t.Fatalf("%d runs, want %d", n, len(want))
			}
		})
	}
}

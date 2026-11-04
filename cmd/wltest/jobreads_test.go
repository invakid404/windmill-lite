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
	bun := bunOrSkip(t)
	root := repo(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	dir := filepath.Join(flowsDir, "client-api", "job-reads-ended")
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
					if !found || observed.Status != "failure" || e.Name != "UnsupportedPlatformCall" || e.Message != endedRunMessage ||
						!slices.Equal(e.Calls, []string{"GET /w/{ws}/jobs_u/get/{id}", "GET /w/{ws}/jobs_u/get_args/{id}"}) {
						t.Errorf("run %s: %+v\n  %s\nharness diagnostics:\n%s", r.run, observed, strings.Join(r.problems, "\n  "), r.diag)
					}
				}
			}
			if n != 1 {
				t.Fatalf("%d runs, want 1", n)
			}
		})
	}
}

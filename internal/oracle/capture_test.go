package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// TestReplayRecordedCaptures replays the raw API captures that the oracle
// workflow recorded for the smoke fixture (testdata/captures, from the CI
// run named in each recording's provenance) through the walker and the
// normaliser, offline, and requires the committed recording's observation.
// It also checks the job kinds the walker met against OR-28: a leaf of kind
// script, flowscript, identity or preview has no flow_status, and a flow,
// flownode or flowpreview job has one.
func TestReplayRecordedCaptures(t *testing.T) {
	// spec: OR-28/a, OR-11/a, OR-12/a, OR-20/a, OR-20/b
	paths, err := filepath.Glob("testdata/captures/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no captures in testdata/captures")
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var rc RawCapture
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&rc); err != nil {
				t.Fatal(err)
			}
			f, err := conformance.LoadFixture(filepath.Join("../..", filepath.FromSlash(rc.Fixture)))
			if err != nil {
				t.Fatal(err)
			}
			var run *conformance.Run
			for i := range f.Runs {
				if f.Runs[i].Name == rc.Run {
					run = &f.Runs[i]
				}
			}
			if run == nil || run.Recorded == nil {
				t.Fatalf("fixture %s has no recorded run %s", rc.Fixture, rc.Run)
			}
			tree, err := LoadSyncTree(f.SyncTree())
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer((&Capture{Exchanges: rc.Exchanges}).Handler())
			defer srv.Close()
			w, err := (&Walker{Client: &Client{BaseURL: srv.URL}, Workspace: rc.Workspace, Tree: tree}).Walk(context.Background(), rc.Root, run.Target, run.IsScript())
			if err != nil {
				t.Fatal(err)
			}
			obs, err := Observe(w, rc.Effects, f.Compare)
			if err != nil {
				t.Fatal(err)
			}
			diffs, err := conformance.Compare(obs, run.Recorded.Observation, conformance.CompareOptions{StrictKeyOrder: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range diffs {
				t.Errorf("replay differs from the recording: %s", d)
			}

			kinds := 0
			for _, e := range rc.Exchanges {
				if !strings.Contains(e.URL, "/jobs_u/get/") {
					continue
				}
				var j struct {
					Kind       string          `json:"job_kind"`
					FlowStatus json.RawMessage `json:"flow_status"`
				}
				if err := json.Unmarshal([]byte(e.Body), &j); err != nil {
					t.Fatal(err)
				}
				hasStatus := len(j.FlowStatus) > 0 && string(j.FlowStatus) != "null"
				switch j.Kind {
				case "script", "flowscript", "identity", "preview":
					if hasStatus {
						t.Errorf("leaf of kind %s has a flow_status", j.Kind)
					}
				case "flow", "flownode", "flowpreview":
					if !hasStatus {
						t.Errorf("job of kind %s has no flow_status", j.Kind)
					}
				default:
					t.Errorf("job kind %q is not one OR-28 records", j.Kind)
				}
				kinds++
			}
			if kinds == 0 {
				t.Fatal("the capture holds no job")
			}
		})
	}
}

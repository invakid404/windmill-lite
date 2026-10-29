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
// workflow recorded in CI (testdata/captures: the smoke fixture's and the
// recording batches') through the walker and the normaliser, offline, and
// requires each capture's run to reproduce its committed recording. It also
// checks the job kinds the walker met against OR-28: a leaf of kind script,
// flowscript, identity or preview has no flow_status, and a flow, flownode
// or flowpreview job has one.
func TestReplayRecordedCaptures(t *testing.T) {
	// spec: OR-28/a, OR-11/a, OR-12/a, OR-20/a, OR-20/b, FX-19/d, CF-3/a, CF-12/b, CF-12/e, CF-12/f, CF-13/a, CF-14/c, CF-15/f, FX-40/b, FX-40/d, OR-V9/a, OR-V9/b, OR-V9/c, OR-V9/d, OR-11/b, OR-12/b, OR-16/a, OR-21/a, OR-25/a, OR-26/a, OR-V2/a, OR-V2/d, OR-10/b, FX-37/a, FX-49/a, CF-6/b
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
			obs, err := Observe(w, rc.Effects, rc.HTTP(), f.Compare)
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

// TestCommittedRecordingsNameThePinnedOracle: every recording committed
// under conformance/flows was made on the pinned oracle with the pinned
// loader (OR-V5/b), so a recording of another reference fails the Go CI,
// not only the next oracle run.
func TestCommittedRecordingsNameThePinnedOracle(t *testing.T) {
	// spec: OR-V5/b, OR-15
	dirs, err := conformance.FindFixtures("../../conformance/flows")
	if err != nil {
		t.Fatal(err)
	}
	recordings := 0
	for _, d := range dirs {
		f, err := conformance.LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range f.Runs {
			if r.Recorded == nil {
				continue
			}
			recordings++
			if err := CheckRecordingIdentity(r.Recorded.Provenance); err != nil {
				t.Errorf("%s run %s: %v", d, r.Name, err)
			}
		}
	}
	if recordings == 0 {
		t.Fatal("no committed recordings")
	}
	for _, bad := range []string{
		`{"oracle":{"version":"CE v1.767.0","image":"` + WindmillImage + `","cli":"windmill-cli@1.767.0-beta"}}`,
		`{"oracle":{"version":"CE v1.767.0 ","image":"` + WindmillImage + `","cli":"` + CLIPackage + `"}}`,
		`{"oracle":{"version":"CE v1.767.0","image":"` + WindmillRepo + `:1.767.0","cli":"` + CLIPackage + `"}}`,
		`{"oracle":{"version":null,"image":"` + WindmillImage + `","cli":"` + CLIPackage + `"}}`,
		`not json`,
	} {
		if err := CheckRecordingIdentity(json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

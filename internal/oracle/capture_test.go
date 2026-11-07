package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
// or flowpreview job has one. A capture whose run has no recording, which
// the retarget ledger lists as pending, is skipped with its marker.
func TestReplayRecordedCaptures(t *testing.T) {
	// spec: OR-28/a, OR-11/a, OR-12/a, OR-20/a, OR-20/b, FX-19/d, CF-3/a, CF-12/b, CF-12/e, CF-12/f, CF-13/a, CF-14/c, CF-15/f, FX-40/b, FX-40/d, OR-V9/a, OR-V9/b, OR-V9/c, OR-V9/d, OR-11/b, OR-12/b, OR-16/a, OR-21/a, OR-25/a, OR-26/a, OR-V2/a, OR-V2/d, OR-10/b, FX-37/a, FX-49/a, CF-6/b
	paths, err := filepath.Glob("testdata/captures/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no captures in testdata/captures")
	}
	pending := pendingRecordings(t)
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
			if e, ok := pending[rc.Fixture+"/"+rc.Run]; ok && run != nil && run.Recorded == nil {
				t.Skip(e.Marker())
			}
			if run == nil || run.Recorded == nil {
				t.Fatalf("fixture %s has no recorded run %s", rc.Fixture, rc.Run)
			}
			obs := observeCapture(t, rc, f, run)
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

// observeCapture replays rc, a raw capture of a run of fixture f, through
// the walker and the normaliser, offline.
func observeCapture(t *testing.T, rc RawCapture, f *conformance.Fixture, run *conformance.Run) conformance.Observation {
	t.Helper()
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
	resp, err := rc.HTTP()
	if err != nil {
		t.Fatal(err)
	}
	obs, err := Observe(w, rc.Effects, resp, f.Compare)
	if err != nil {
		t.Fatal(err)
	}
	return obs
}

// TestUnwalkedJobIDsAcrossRepeats replays both executions of each preview
// run of conformance/flows/openflow/of-drivers, as CI captured them on CE
// v1.862.0 (testdata/captures), through the walker and the normaliser. A
// preview's failed step names its job in its error object (FX-12), a job
// of a flow that step d ran through the API and the walk never reaches, so
// the two executions differed in those ids until OR-14 numbered them. Both
// must now give one observation, the ids written <job:?1>, <job:?2> and
// <job:?3> in the sorted order of their members (ba, par, seq), in the
// root's result and in step d's.
func TestUnwalkedJobIDsAcrossRepeats(t *testing.T) {
	// spec: OR-14/c
	f, err := conformance.LoadFixture("../../conformance/flows/openflow/of-drivers")
	if err != nil {
		t.Fatal(err)
	}
	d := conformance.SitePath{}.Child(conformance.Module("d"))
	for _, name := range []string{"preview_absent", "preview_false", "preview_true"} {
		t.Run(name, func(t *testing.T) {
			var run *conformance.Run
			for i := range f.Runs {
				if f.Runs[i].Name == name {
					run = &f.Runs[i]
				}
			}
			if run == nil {
				t.Fatalf("of-drivers has no run %s", name)
			}
			var obs [2]conformance.Observation
			for rep := range obs {
				data, err := os.ReadFile(fmt.Sprintf("testdata/captures/openflow-of-drivers-%s-%d.json", name, rep))
				if err != nil {
					t.Fatal(err)
				}
				var rc RawCapture
				if err := json.Unmarshal(data, &rc); err != nil {
					t.Fatal(err)
				}
				if rc.Run != name || rc.Repeat != rep {
					t.Fatalf("capture of run %s, repeat %d", rc.Run, rc.Repeat)
				}
				obs[rep] = observeCapture(t, rc, f, run)
			}
			diffs, err := conformance.Compare(obs[1], obs[0], conformance.CompareOptions{StrictKeyOrder: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, diff := range diffs {
				t.Errorf("the second execution differs from the first: %s", diff)
			}
			for _, v := range []json.RawMessage{obs[0].Result, obs[0].Steps[d].Result} {
				var got map[string]struct {
					Result []struct {
						Error struct {
							JobID string `json:"job_id"`
						} `json:"error"`
					} `json:"result"`
				}
				if err := json.Unmarshal(v, &got); err != nil {
					t.Fatal(err)
				}
				for i, k := range []string{"ba", "par", "seq"} {
					if r := got[k].Result; len(r) == 0 || r[0].Error.JobID != fmt.Sprintf("<job:?%d>", i+1) {
						t.Errorf("%s's failed step: %+v, want the job id <job:?%d>", k, r, i+1)
					}
				}
			}
		})
	}
}

// TestCommittedRecordingsNameThePinnedOracle: every recording committed
// under conformance/flows, and every recording of an expression case
// under conformance/exprs, was made on the pinned oracle with the pinned
// loader (OR-V5/b), so a recording of another reference fails the Go CI,
// not only the next oracle run.
func TestCommittedRecordingsNameThePinnedOracle(t *testing.T) {
	// spec: OR-V5/b, OR-15
	dirs, err := conformance.FindFixtures("../../conformance/flows")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []*conformance.Fixture
	for _, d := range dirs {
		f, err := conformance.LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, f)
	}
	for _, ef := range loadExprFiles(t) {
		f, err := conformance.LiftExprFile(ef, filepath.Join(t.TempDir(), "lifted"))
		if err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, f)
	}
	flows, exprs := 0, 0
	for i, f := range fixtures {
		for _, r := range f.Runs {
			if r.Recorded == nil {
				continue
			}
			if i < len(dirs) {
				flows++
			} else {
				exprs++
			}
			if err := CheckRecordingIdentity(r.Recorded.Provenance); err != nil {
				t.Errorf("%s run %s: %v", f.Dir, r.Name, err)
			}
		}
	}
	if flows == 0 || exprs == 0 {
		t.Fatalf("committed recordings: %d of flow runs, %d of expression cases; want both", flows, exprs)
	}
	for _, bad := range []string{
		`{"oracle":{"version":"CE v1.862.0","image":"` + WindmillImage + `","cli":"windmill-cli@1.862.0-beta"}}`,
		`{"oracle":{"version":"CE v1.862.0 ","image":"` + WindmillImage + `","cli":"` + CLIPackage + `"}}`,
		`{"oracle":{"version":"CE v1.862.0","image":"` + WindmillRepo + `:1.862.0","cli":"` + CLIPackage + `"}}`,
		`{"oracle":{"version":null,"image":"` + WindmillImage + `","cli":"` + CLIPackage + `"}}`,
		// The previous pin's identity.
		`{"oracle":{"version":"CE v1.767.0","image":"` + WindmillRepo + `:1.767.0@sha256:9239264a7a849deedda87868147e65eb910456039fe9d961901e2e5b143ebd43","cli":"windmill-cli@1.767.0"}}`,
		`not json`,
	} {
		if err := CheckRecordingIdentity(json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

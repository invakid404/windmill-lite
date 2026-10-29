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
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// The stub-mode session tests record the committed stub fixtures against
// fakeEngine, which executes them job by job and makes their stubs' calls.
// They run the whole session path of stub mode: the stubbed push with its
// signature check, the replay service answering while each run is in
// progress (the locator walks unfinished job trees), concurrent outer
// runs, the walk, checkReplay and the comparison with each run's expect.

type stubRig struct {
	docker  *fakeDocker
	engine  *fakeEngine
	fixture string
	cliLog  string
	srvURL  string
}

func newStubRig(t *testing.T, name string) *stubRig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := CopyTree(filepath.Join("../../conformance/flows/stub", name), dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "recorded")); err != nil {
		t.Fatal(err)
	}
	f, err := conformance.LoadFixture(dir)
	if err != nil {
		t.Fatal(err)
	}
	d := newFakeDocker(t)
	e := newFakeEngine(t, filepath.Join(dir, "windmill"), d)
	e.holdGroups(f.Runs)
	srv := httptest.NewServer(e)
	t.Cleanup(func() {
		e.wg.Wait()
		srv.Close()
	})
	return &stubRig{docker: d, engine: e, fixture: dir, cliLog: filepath.Join(t.TempDir(), "cli.log"), srvURL: srv.URL}
}

func (r *stubRig) session() *Session {
	return &Session{
		Docker:       r.docker,
		CLI:          CLI{Command: []string{os.Args[0], "-test.run=^TestFakeCLI$", "--", fakeCLIFlag, r.cliLog, "-"}},
		SinkListen:   "127.0.0.1:0",
		Repeat:       2,
		RunTimeout:   time.Minute,
		Harness:      HarnessInfo{Name: "wltest", Version: "test", Go: "go"},
		baseURL:      r.srvURL,
		pollInterval: time.Millisecond,
		measure:      func() (uint64, error) { return 20 * GiB, nil },
		settle:       time.Millisecond,
	}
}

func (r *stubRig) load(t *testing.T) *conformance.Fixture {
	t.Helper()
	f, err := conformance.LoadFixture(r.fixture)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestSessionRecordsTheStubFixtures records each stub fixture twice per
// run and requires its expect, then checks the recordings on a second
// session. The fake CLI fails a push from a tree whose codebase paths do
// not resolve, so stub/outside, whose entry's relative_path and asset lie
// outside the tree, shows that the pushed copy carries them.
func TestSessionRecordsTheStubFixtures(t *testing.T) {
	for _, name := range []string{"collapsed", "nested", "outside", "retry_loop", "scopes"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newStubRig(t, name)
			ctx := context.Background()
			rep, err := rig.session().Run(ctx, []*conformance.Fixture{rig.load(t)})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rep.Runs {
				if r.Outcome != OutcomeNew || r.Wrote == "" {
					t.Fatalf("run %s: %s %s\n%s", r.Run, r.Outcome, r.Error, strings.Join(r.Differences, "\n"))
				}
			}
			dep := rep.Session.Deployments["stub/"+name]
			if len(dep.Stubbed) == 0 {
				dep = rep.Session.Deployments[rig.fixture]
			}
			if len(dep.Stubbed) == 0 {
				t.Fatalf("the deployment lists no stubbed files: %+v", rep.Session.Deployments)
			}
			if want := map[string]string{"outside": "../shared"}[name]; strings.Join(dep.Outside, " ") != want {
				t.Fatalf("the deployment carried %q, want %q", dep.Outside, want)
			}
			log, _ := os.ReadFile(rig.cliLog)
			if strings.Count(string(log), " generate-metadata --yes") != 1 || strings.Count(string(log), " sync push --yes") != 1 {
				t.Fatalf("CLI calls:\n%s", log)
			}
			s := rig.session()
			s.Check = true
			rep, err = s.Run(ctx, []*conformance.Fixture{rig.load(t)})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rep.Runs {
				if r.Outcome != OutcomeUnchanged {
					t.Fatalf("check of run %s: %s %s\n%s", r.Run, r.Outcome, r.Error, strings.Join(r.Differences, "\n"))
				}
			}
		})
	}
}

// TestSessionStubConcurrentRuns checks the concurrent outer runs of the
// scopes fixture (OR-V6/a): runs r and t share their group in provenance,
// were in flight together, and each recorded only its own calls.
func TestSessionStubConcurrentRuns(t *testing.T) {
	rig := newStubRig(t, "scopes")
	rep, err := rig.session().Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeNew {
			t.Fatalf("run %s: %s %s %q", r.Run, r.Outcome, r.Error, r.Differences)
		}
	}
	f := rig.load(t)
	for _, r := range f.Runs {
		var prov Provenance
		if err := json.Unmarshal(r.Recorded.Provenance, &prov); err != nil {
			t.Fatal(err)
		}
		wantGroup := map[string]string{"alone": "", "r": "rt", "t": "rt"}[r.Name]
		if prov.Group != wantGroup {
			t.Errorf("run %s: group %q", r.Name, prov.Group)
		}
		want := map[string]int{"alone": 3, "r": 3, "t": 2}[r.Name]
		if n := len(r.Recorded.Observation.Effects); n != want {
			t.Errorf("run %s: %d effects, want %d", r.Name, n, want)
		}
		for _, e := range r.Recorded.Observation.Effects {
			var c bytes.Buffer
			if err := json.Compact(&c, e); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(c.Bytes(), []byte(`"root":"<job:>"`)) {
				t.Errorf("run %s has an effect of another run: %s", r.Name, e)
			}
		}
	}
	if kinds := strings.Join(rig.engine.jobKinds(), " "); !strings.Contains(kinds, "flowpreview") {
		t.Fatalf("no parallel iteration ran: %s", kinds)
	}
}

// TestSessionStubRefusals fails a stub run whose table lacks an outcome
// (replay_miss) and one in which a job completed without calling the
// service, as an unstubbed script would (stub_not_called).
func TestSessionStubRefusals(t *testing.T) {
	t.Run("replay_miss", func(t *testing.T) {
		rig := newStubRig(t, "collapsed")
		run := filepath.Join(rig.fixture, "runs", "ok.yaml")
		data, err := os.ReadFile(run)
		if err != nil {
			t.Fatal(err)
		}
		cut := strings.Replace(string(data), `  "m:total": {result: 11}`+"\n", "", 1)
		if cut == string(data) {
			t.Fatal("the run's table changed; update the test")
		}
		if err := os.WriteFile(run, []byte(cut), 0o644); err != nil {
			t.Fatal(err)
		}
		rep, err := rig.session().Run(context.Background(), []*conformance.Fixture{rig.load(t)})
		if err != nil {
			t.Fatal(err)
		}
		if r := rep.Runs[0]; r.Outcome != OutcomeFailed || !strings.Contains(r.Error, `{"error":"replay_miss","site":"m:total","attempt":0}`) {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("stub_not_called", func(t *testing.T) {
		rig := newStubRig(t, "nested")
		rig.engine.skipCall = "write"
		rep, err := rig.session().Run(context.Background(), []*conformance.Fixture{rig.load(t)})
		if err != nil {
			t.Fatal(err)
		}
		if r := rep.Runs[0]; r.Outcome != OutcomeFailed || !strings.Contains(r.Error, `"stub_not_called"`) || !strings.Contains(r.Error, "m:outer/i:0/m:inner/i:0/m:write") {
			t.Fatalf("%+v", r)
		}
	})
}

// TestCheckOverlap requires a group's root jobs to have been in flight
// together, by the oracle's timestamps.
func TestCheckOverlap(t *testing.T) {
	batch := []conformance.Run{{Name: "r"}, {Name: "t"}}
	at := func(from, to string) executed {
		var e executed
		e.prov.Timings.Jobs = map[string]JobTiming{"": {CreatedAt: "2026-10-28T10:00:" + from + "Z", CompletedAt: "2026-10-28T10:00:" + to + "Z"}}
		return e
	}
	if err := checkOverlap(batch, []int{0, 1}, []executed{at("00.1", "00.5"), at("00.2", "00.3")}); err != nil {
		t.Fatal(err)
	}
	if err := checkOverlap(batch, []int{0, 1}, []executed{at("00.1", "00.2"), at("00.3", "00.4")}); err == nil || !strings.Contains(err.Error(), "run t started after run r completed") {
		t.Fatalf("got %v", err)
	}
	if err := checkOverlap(batch, []int{0, 1}, []executed{at("00.1", "00.2"), {}}); err == nil || !strings.Contains(err.Error(), "no root created_at") {
		t.Fatalf("got %v", err)
	}
}

package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// The session options of recording batch E3e, against the fake oracle of
// record_test.go: the run-and-wait submission, the setup policies, skipped
// fixtures, the listen-interval knob and the pull read-back.

// editFixture replaces old with new in a file of the rig's fixture, once.
func editFixture(t *testing.T, rig *sessionRig, name, old, new string) {
	t.Helper()
	p := filepath.Join(rig.fixture, filepath.FromSlash(name))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s lacks %q", name, old)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(data), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFixtureFile(t *testing.T, rig *sessionRig, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(rig.fixture, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSessionRecordsRunAndWait records two smoke runs submitted through
// run-and-wait: each observation holds the response (200 with the result
// on success, 422 with the error on failure) beside the walk, the failed
// step's stack is masked inside the body text, which otherwise keeps its
// spelling, the provenance has the response time, and an expect that
// states the response is met.
func TestSessionRecordsRunAndWait(t *testing.T) {
	// spec: FX-36, FX-37, OR-10, OR-V3
	rig := newRig(t)
	writeFixtureFile(t, rig, "runs/wait_ok.yaml", `target: f/wl_smoke/two_step
args: {x: 3}
submit: run_wait_result
expect:
  status: success
  result: "hi:6"
  steps:
    "m:a": {status: success, args: {x: 3}, result: {doubled: 6, seen: 3}}
    "m:b": {status: success, args: {n: 6, tag: hi}, result: "hi:6"}
  response: {status: 200, content_type: application/json, body: '"hi:6"'}
`)
	writeFixtureFile(t, rig, "runs/wait_fail.yaml", "target: f/wl_smoke/two_step\nargs: {x: 60}\nsubmit: run_wait_result\n")
	rep, err := rig.session(20*GiB).Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeNew {
			t.Errorf("%s: %s %s %v", r.Run, r.Outcome, r.Error, r.Differences)
		}
	}
	f := rig.load(t)
	byName := map[string]conformance.Run{}
	for _, r := range f.Runs {
		byName[r.Name] = r
	}
	ok, fail := byName["wait_ok"].Recorded, byName["wait_fail"].Recorded
	if ok == nil || fail == nil {
		t.Fatal("a run-and-wait run has no recording")
	}
	if got := ok.Observation.Response; got == nil || *got != (conformance.Response{Status: 200, ContentType: "application/json", Body: `"hi:6"`}) {
		t.Errorf("wait_ok response %+v", got)
	}
	want := `{"error":{"name":"Error","message":"too big: 120","stack":"<stack>","step_id":"b"}}`
	if got := fail.Observation.Response; got == nil || got.Status != 422 || got.Body != want {
		t.Errorf("wait_fail response %+v, want 422 with %s", got, want)
	}
	if byName["ok"].Recorded.Observation.Response != nil {
		t.Error("an asynchronous run has a response")
	}
	var p Provenance
	if err := json.Unmarshal(ok.Provenance, &p); err != nil {
		t.Fatal(err)
	}
	if p.Timings.SubmitToResponseMS == nil {
		t.Error("the provenance has no submit_to_response_ms")
	}
}

// TestSessionRunAndWaitRefused fails a run whose run-and-wait submission
// the oracle refused without creating a job, at once and with the
// response, instead of polling a job that does not exist.
func TestSessionRunAndWaitRefused(t *testing.T) {
	// spec: OR-10
	rig := newRig(t)
	rig.oracle.refuseWait = true
	writeFixtureFile(t, rig, "runs/wait_ok.yaml", "target: f/wl_smoke/two_step\nargs: {x: 3}\nsubmit: run_wait_result\n")
	rep, err := rig.session(20*GiB).Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Run == "wait_ok" && (r.Outcome != OutcomeFailed || !strings.Contains(r.Error, "refused run wait_ok without a job: HTTP 400: Bad request: no such flow")) {
			t.Errorf("wait_ok: %s %s", r.Outcome, r.Error)
		}
	}
}

// TestSessionSetupPolicies applies setup.yaml under each policy of
// oracle.setup: after the push (the default), where each variable must
// then be there; before the push, where the push must have removed it
// (OR-31/a, OR-31/b); and after the push and again before every run. A
// variable in the wrong state fails the fixture's runs with a refusal.
func TestSessionSetupPolicies(t *testing.T) {
	// spec: OR-31/a, OR-31/b
	const setup = "variables:\n  - path: f/wl_smoke/v\n    value: seeded\n"
	for _, c := range []struct {
		name, policy string
		keep, lose   bool
		refusal      string
		updates      int
	}{
		{name: "after_push", policy: ""},
		{name: "after_push_lost", policy: "", lose: true, refusal: "setup_not_applied"},
		{name: "before_push", policy: "before_push"},
		{name: "before_push_survives", policy: "before_push", keep: true, refusal: "seed_survived_push"},
		{name: "before_push_lost", policy: "before_push", lose: true, refusal: "setup_not_applied"},
		// Three runs (ok, fail, effects), two repetitions each.
		{name: "each_run", policy: "each_run", updates: 6},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newRig(t)
			rig.oracle.keepVars, rig.oracle.loseVars = c.keep, c.lose
			writeFixtureFile(t, rig, "setup.yaml", setup)
			if c.policy != "" {
				editFixture(t, rig, "fixture.yaml", "oracle: {workers: 1}", "oracle: {workers: 1, setup: "+c.policy+"}")
			}
			rep, err := rig.session(20*GiB).Run(context.Background(), []*conformance.Fixture{rig.load(t)})
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Runs) != len(rig.load(t).Runs) {
				t.Fatalf("%d run reports for %d runs", len(rep.Runs), len(rig.load(t).Runs))
			}
			for _, r := range rep.Runs {
				switch {
				case c.refusal == "" && r.Outcome != OutcomeNew:
					t.Errorf("%s: %s %s", r.Run, r.Outcome, r.Error)
				case c.refusal != "" && (r.Outcome != OutcomeFailed || !strings.Contains(r.Error, c.refusal)):
					t.Errorf("%s: %s %s, want the refusal %s", r.Run, r.Outcome, r.Error, c.refusal)
				}
			}
			if len(rig.oracle.updates) != c.updates {
				t.Errorf("variable updates %v, want %d", rig.oracle.updates, c.updates)
			}
			for _, u := range rig.oracle.updates {
				if !strings.HasSuffix(u, " f/wl_smoke/v=seeded") {
					t.Errorf("update %q", u)
				}
			}
		})
	}
}

// TestSessionSkipsUnmetWorkers skips, under SkipUnmetWorkers, a fixture
// that needs two workers on a one-worker session: its runs are reported
// skipped, nothing is deployed, and nothing fails; without the option the
// same fixture is refused (OR-5/a).
func TestSessionSkipsUnmetWorkers(t *testing.T) {
	// spec: OR-5/a, OR-5/b
	for _, skip := range []bool{true, false} {
		rig := newRig(t)
		editFixture(t, rig, "fixture.yaml", "oracle: {workers: 1}", "oracle: {workers: 2}")
		s := rig.session(20 * GiB)
		s.SkipUnmetWorkers = skip
		rep, err := s.Run(context.Background(), []*conformance.Fixture{rig.load(t)})
		if err != nil {
			t.Fatal(err)
		}
		want := OutcomeRefused
		if skip {
			want = OutcomeSkipped
		}
		if len(rep.Runs) != len(rig.load(t).Runs) {
			t.Fatalf("skip %v: %d run reports for %d runs", skip, len(rep.Runs), len(rig.load(t).Runs))
		}
		for _, r := range rep.Runs {
			if r.Outcome != want || r.Failed(true) == skip {
				t.Errorf("skip %v: %s: %s (failed %v)", skip, r.Run, r.Outcome, r.Failed(true))
			}
		}
		if rig.oracle.deleted != 0 || len(rig.oracle.workspaces) != 0 {
			t.Errorf("skip %v: a workspace was created", skip)
		}
	}
}

// TestSessionSkipsProvidedSites skips a fixture with host-provided sites
// (milestone F2a): windmill-lite replaces them with the host's providers,
// while the oracle would run their real targets, so its runs are reported
// skipped, nothing is deployed, and nothing fails.
func TestSessionSkipsProvidedSites(t *testing.T) {
	rig := newRig(t)
	editFixture(t, rig, "fixture.yaml", "oracle: {workers: 1}",
		"oracle: {workers: 1}\nprovided:\n  - {selector: {flow: f/x/y, module: a}, static: 1, mandatory: true}")
	rep, err := rig.session(20*GiB).Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != len(rig.load(t).Runs) {
		t.Fatalf("%d run reports for %d runs", len(rep.Runs), len(rig.load(t).Runs))
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeSkipped || r.Failed(true) || !strings.Contains(r.Error, "fixture_has_provided_sites") {
			t.Errorf("%s: %s %s", r.Run, r.Outcome, r.Error)
		}
	}
	if rig.oracle.deleted != 0 || len(rig.oracle.workspaces) != 0 {
		t.Error("a workspace was created")
	}
}

// TestSessionListenInterval starts the oracle with the knob of OR-7/a set
// through compose's variable, and records the value in provenance.
func TestSessionListenInterval(t *testing.T) {
	// spec: OR-7/a
	rig := newRig(t)
	s := rig.session(20 * GiB)
	s.ListenInterval = 10
	rep, err := s.Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeNew {
			t.Errorf("%s: %s %s", r.Run, r.Outcome, r.Error)
		}
	}
	if !slices.Contains(rig.docker.upEnv, ListenIntervalVar+"=10") {
		t.Errorf("compose up environment %v", rig.docker.upEnv)
	}
	for _, r := range rig.load(t).Runs {
		var p Provenance
		if err := json.Unmarshal(r.Recorded.Provenance, &p); err != nil {
			t.Fatal(err)
		}
		if p.Env["LISTEN_NEW_EVENTS_INTERVAL_SEC"] != "10" {
			t.Errorf("%s: provenance env %v", r.Name, p.Env)
		}
	}
}

// TestSessionConfiguration runs a session of a changed configuration
// (SessionConfigs): its variables reach the oracle's services through the
// session environment file and every recording's provenance, it records
// only the fixtures that need its configuration, and the reference session
// skips or refuses those, before any workspace exists.
func TestSessionConfiguration(t *testing.T) {
	// spec: OR-4, OR-7
	rig := newRig(t)
	editFixture(t, rig, "fixture.yaml", "oracle: {workers: 1}", "oracle: {workers: 1, session: native}")
	s := rig.session(20 * GiB)
	s.Config = "native"
	rep, err := s.Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeNew {
			t.Errorf("native session: %s: %s %s", r.Run, r.Outcome, r.Error)
		}
	}
	if rig.docker.sessionEnv != "WORKER_TAGS=nativets\n" {
		t.Errorf("native session environment file %q", rig.docker.sessionEnv)
	}
	for _, r := range rig.load(t).Runs {
		var p Provenance
		if err := json.Unmarshal(r.Recorded.Provenance, &p); err != nil {
			t.Fatal(err)
		}
		if p.Env["WORKER_TAGS"] != "nativets" {
			t.Errorf("%s: provenance env %v", r.Name, p.Env)
		}
	}

	// The reference session leaves the fixture to a native session, and a
	// native session leaves a reference fixture alone.
	for _, c := range []struct {
		config, fixture string
		skip            bool
		want            string
		reason          string
	}{
		{"", "oracle: {workers: 1, session: native}", true, OutcomeSkipped, `{"error":"fixture_requires_session","need":"native","have":""}`},
		{"", "oracle: {workers: 1, session: native}", false, OutcomeRefused, `{"error":"fixture_requires_session","need":"native","have":""}`},
		{"native", "oracle: {workers: 1}", true, OutcomeSkipped, `{"error":"fixture_requires_session","need":"","have":"native"}`},
		{"", "oracle: {workers: 1, session: nope}", true, OutcomeRefused, `{"error":"unknown_session","session":"nope"}`},
	} {
		rig := newRig(t)
		editFixture(t, rig, "fixture.yaml", "oracle: {workers: 1}", c.fixture)
		s := rig.session(20 * GiB)
		s.Config, s.SkipUnmetSession = c.config, c.skip
		rep, err := s.Run(context.Background(), []*conformance.Fixture{rig.load(t)})
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Runs) != len(rig.load(t).Runs) {
			t.Fatalf("%+v: %d run reports", c, len(rep.Runs))
		}
		for _, r := range rep.Runs {
			if r.Outcome != c.want || r.Error != c.reason {
				t.Errorf("%+v: %s: %s %s", c, r.Run, r.Outcome, r.Error)
			}
		}
		if rig.oracle.deleted != 0 || len(rig.oracle.workspaces) != 0 {
			t.Errorf("%+v: a workspace was created", c)
		}
		if c.config == "" && rig.docker.sessionEnv != "" {
			t.Errorf("reference session environment file %q", rig.docker.sessionEnv)
		}
	}

	// A configuration SessionConfigs does not name is refused before the
	// oracle starts.
	rig = newRig(t)
	s = rig.session(20 * GiB)
	s.Config = "nope"
	if _, err := s.Run(context.Background(), []*conformance.Fixture{rig.load(t)}); err == nil || err.Error() != `{"error":"unknown_session","session":"nope"}` {
		t.Errorf("unknown configuration: %v", err)
	}
	if rig.docker.called("compose") != 0 {
		t.Error("the oracle was started for an unknown configuration")
	}
}

// TestSessionPullsTheDeployedTree deploys the smoke fixture through the
// API with oracle.pull: after the gate the CLI's sync pull writes the
// workspace back once, and each run's deployment evidence holds the pulled
// flow value (SY-6, CF-6).
func TestSessionPullsTheDeployedTree(t *testing.T) {
	// spec: SY-6/c, CF-6/b
	rig := newRig(t)
	tree := filepath.Join(rig.fixture, "windmill")
	for _, p := range []string{"wmill-lock.yaml", "f/wl_smoke/double.script.lock"} {
		if err := os.Remove(filepath.Join(tree, filepath.FromSlash(p))); err != nil {
			t.Fatal(err)
		}
	}
	writeFixtureFile(t, rig, "windmill/f/wl_smoke/double.script.yaml", "summary: double\n")
	editFixture(t, rig, "fixture.yaml", "oracle: {workers: 1}", "oracle: {workers: 1, deploy: api, pull: true}")
	rep, err := rig.session(20*GiB).Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeNew {
			t.Errorf("%s: %s %s", r.Run, r.Outcome, r.Error)
		}
	}
	if log, _ := os.ReadFile(rig.cliLog); strings.Count(string(log), " sync pull --yes") != 1 {
		t.Errorf("CLI calls:\n%s", log)
	}
	for _, r := range rig.load(t).Runs {
		var p Provenance
		if err := json.Unmarshal(r.Recorded.Provenance, &p); err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if p.Deployment == nil || json.Compact(&b, p.Deployment.Pulled) != nil || b.String() != `{"modules":[{"id":"a"},{"id":"b"}],"pulled":true}` {
			t.Errorf("%s: pulled %s", r.Name, b.String())
		}
	}
}

// TestRunWaitKeepsARedirect returns a run-and-wait redirect as the oracle
// sent it: a 3xx status that response metadata asks for (FX-36) is the
// observation, so it is never followed, and the caller's HTTP client is
// not changed to get there.
func TestRunWaitKeepsARedirect(t *testing.T) {
	// spec: FX-36
	var destination int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/destination" {
			destination++
			_, _ = io.WriteString(w, "destination")
			return
		}
		w.Header().Set("Location", "/destination")
		w.WriteHeader(http.StatusFound)
		_, _ = io.WriteString(w, "original")
	}))
	defer srv.Close()
	own := &http.Client{}
	for _, h := range []*http.Client{nil, own} {
		c := &Client{BaseURL: srv.URL, HTTP: h}
		got, err := c.RunWaitFlow(context.Background(), "ws", "f/x/y", json.RawMessage(`{}`), "00000000-0000-4000-8000-000000000001")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != http.StatusFound || string(got.Body) != "original" {
			t.Errorf("got %d %q, want 302 original", got.Status, got.Body)
		}
	}
	if destination != 0 {
		t.Errorf("the redirect was followed %d times", destination)
	}
	if own.CheckRedirect != nil {
		t.Error("the caller's HTTP client was changed")
	}
	// Other calls still follow redirects.
	got, err := (&Client{BaseURL: srv.URL}).send(context.Background(), http.MethodGet, "/x", nil, nil)
	if err != nil || got.Status != http.StatusOK || string(got.Body) != "destination" {
		t.Errorf("send: %d %q %v", got.Status, got.Body, err)
	}
}

// TestObserveKeepsTheResponse keeps the Content-Type header as sent,
// parameters included, and a body that is not UTF-8 losslessly, as base64,
// through an observation's JSON and a raw capture, whose replay serves the
// same bytes.
func TestObserveKeepsTheResponse(t *testing.T) {
	// spec: FX-36
	w := &Walk{Status: conformance.RunSuccess, Result: json.RawMessage(`null`)}
	ct := "text/plain; charset=iso-8859-1"
	o, err := Observe(w, nil, &HTTPResponse{Status: 200, ContentType: ct, Body: []byte("h\xe9")}, conformance.CompareSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Response.ContentType != ct {
		t.Errorf("content type %q, want %q", o.Response.ContentType, ct)
	}
	raw := []byte{0xff, 0x00, 0xfe}
	o, err = Observe(w, nil, &HTTPResponse{Status: 200, ContentType: "application/octet-stream", Body: raw}, conformance.CompareSpec{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var loaded conformance.Observation
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	got, err := loaded.Response.Bytes()
	if err != nil || !bytes.Equal(got, raw) || loaded.Response.Encoding != conformance.BodyBase64 {
		t.Fatalf("observation round trip: %x %q %v, want %x", got, loaded.Response.Encoding, err, raw)
	}
	other, _ := Observe(w, nil, &HTTPResponse{Status: 200, ContentType: "application/octet-stream", Body: []byte{0xff, 0x01, 0xfe}}, conformance.CompareSpec{})
	if diffs, err := conformance.Compare(other, o, conformance.CompareOptions{}); err != nil || len(diffs) == 0 {
		t.Errorf("two binary bodies compare equal: %v %v", diffs, err)
	}

	// A raw capture keeps the bytes, and its replay serves them.
	b, enc := exchangeBody(raw)
	rc := RawCapture{Response: &Exchange{Status: 200, ContentType: "application/octet-stream", Encoding: enc, Body: b},
		Exchanges: []Exchange{{Method: "GET", URL: "/api/blob", Status: 200, ContentType: "application/octet-stream", Encoding: enc, Body: b}}}
	data, err = json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	var back RawCapture
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	resp, err := back.HTTP()
	if err != nil || !bytes.Equal(resp.Body, raw) {
		t.Fatalf("raw capture round trip: %x %v", resp.Body, err)
	}
	srv := httptest.NewServer((&Capture{Exchanges: back.Exchanges}).Handler())
	defer srv.Close()
	served, err := (&Client{BaseURL: srv.URL}).send(context.Background(), http.MethodGet, "/blob", nil, nil)
	if err != nil || !bytes.Equal(served.Body, raw) {
		t.Errorf("replay served %x %v, want %x", served.Body, err, raw)
	}
}

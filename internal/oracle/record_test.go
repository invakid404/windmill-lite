package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// The session tests record the OR-20 smoke fixture against a fake oracle:
// a fake docker that answers what the session asks, a fake Windmill API
// that runs the two-step flow, and this test binary re-executed as the CLI.

const fakeCLIFlag = "--wltest-fake-cli"

// TestFakeCLI is the fake wmill CLI. It does nothing unless re-executed
// with fakeCLIFlag, a log file and a hang marker ("-" for none): then it
// logs its arguments, prints a version for --version, and, with a marker,
// creates the marker and hangs on sync push.
func TestFakeCLI(t *testing.T) {
	args := os.Args
	for i, a := range args {
		if a != fakeCLIFlag || i+2 >= len(args) {
			continue
		}
		cli := strings.Join(args[i+3:], " ")
		f, err := os.OpenFile(args[i+1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintln(f, cli)
			f.Close()
		}
		if strings.Contains(cli, "--version") {
			fmt.Println("CLI version: " + CLIVersion)
		}
		if marker := args[i+2]; marker != "-" && strings.Contains(cli, "sync push") {
			_ = os.WriteFile(marker, nil, 0o644)
			time.Sleep(time.Minute)
		}
		os.Exit(0)
	}
}

type fakeDocker struct {
	mu        sync.Mutex
	t         *testing.T
	root      string
	present   map[string]bool
	digests   map[string][]string
	leftovers string
	calls     []string
	sinkURL   string
	up        bool
}

func newFakeDocker(t *testing.T) *fakeDocker {
	return &fakeDocker{
		t: t, root: t.TempDir(), present: map[string]bool{},
		digests: map[string][]string{
			WindmillImage: {WindmillRepo + "@" + WindmillDigest},
			PostgresImage: {"postgres@" + PostgresDigest},
		},
	}
}

func (d *fakeDocker) Run(_ context.Context, env []string, args ...string) ([]byte, error) {
	if err := CheckDockerArgs(args); err != nil {
		d.t.Errorf("the session ran a refused docker command: %v", err)
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	line := strings.Join(args, " ")
	d.calls = append(d.calls, line)
	switch {
	case args[0] == "info" && strings.Contains(line, "DockerRootDir"):
		return []byte(d.root + "\n"), nil
	case args[0] == "info":
		return []byte("x86_64\n"), nil
	case args[0] == "ps" || args[0] == "volume" || args[0] == "network":
		return []byte(d.leftovers), nil
	case args[0] == "image" && args[1] == "inspect":
		ref := args[len(args)-1]
		if !d.present[ref] {
			return nil, errors.New("Error: No such image: " + ref)
		}
		info, _ := json.Marshal(map[string]any{"Id": "sha256:feed" + fmt.Sprint(len(ref)), "RepoDigests": d.digests[ref], "Size": 123, "Config": map[string]any{"Labels": map[string]string{"org.opencontainers.image.revision": "6de4ec0f"}}})
		return info, nil
	case args[0] == "image" && args[1] == "rm":
		delete(d.present, args[2])
		return nil, nil
	case args[0] == "pull":
		d.present[args[1]] = true
		return nil, nil
	case args[0] == "compose":
		switch {
		case strings.Contains(line, " up -d"):
			d.up = true
			for _, e := range env {
				if v, ok := strings.CutPrefix(e, "WL_EFFECT_SINK="); ok {
					d.sinkURL = v
				}
			}
		case strings.Contains(line, " down -v"):
			d.up = false
		case strings.Contains(line, " ps -q"):
			return []byte("cid\n"), nil
		}
		return nil, nil
	}
	d.t.Errorf("unexpected docker %s", line)
	return nil, nil
}

func (d *fakeDocker) calledWith(part string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if strings.Contains(c, part) {
			n++
		}
	}
	return n
}

func (d *fakeDocker) called(prefix string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// fakeOracle is a Windmill API that runs the smoke flow: a doubles x, b
// returns "<tag>:<n>" and throws above 100. tag is the value b uses.
type fakeOracle struct {
	mu      sync.Mutex
	t       *testing.T
	version string
	tag     string
	jitter  bool
	extra   bool // a's result gains a member
	hang    bool // roots never complete until canceled
	cancels []string
	docker  *fakeDocker
	jobs    map[string]fakeJob
	n       int
	runs    int
	// workspaces are the live workspaces besides admins; like Community
	// Edition, the fake refuses to create a third (observed by recording
	// batch E3b).
	workspaces map[string]bool
	failDelete bool // deleting a workspace answers 500
	created    []string
	deleted    []string
}

func newFakeOracle(t *testing.T, d *fakeDocker) *fakeOracle {
	return &fakeOracle{t: t, version: OracleVersion, tag: "hi", docker: d, jobs: map[string]fakeJob{}, workspaces: map[string]bool{}}
}

func (o *fakeOracle) newID() string {
	o.n++
	return fmt.Sprintf("10000000-0000-4000-8000-%012d", o.n)
}

func (o *fakeOracle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api")
	write := func(code int, body string) {
		w.WriteHeader(code)
		io.WriteString(w, body)
	}
	switch {
	case p == "/version":
		write(200, o.version)
		return
	case p == "/health/status":
		write(200, `{"status":"healthy","checked_at":"now","database_healthy":true,"workers_alive":1}`)
		return
	case p == "/auth/login":
		write(200, "session-token")
		return
	case p == "/workspaces/create":
		var body struct {
			ID string `json:"id"`
		}
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &body); err != nil || body.ID == "" {
			write(400, "bad workspace")
			return
		}
		if len(o.workspaces) >= 2 {
			write(400, "Bad request: You have reached the maximum number of workspaces (2 outside of default workspace 'admins') without an enterprise license. Archive/delete another workspace to create a new one")
			return
		}
		o.workspaces[body.ID] = true
		o.created = append(o.created, body.ID)
		write(201, "created")
		return
	}
	if r.Header.Get("Authorization") != "Bearer session-token" {
		write(401, "unauthorized")
		return
	}
	if id, ok := strings.CutPrefix(p, "/workspaces/delete/"); ok && r.Method == http.MethodDelete {
		if o.failDelete {
			write(500, "internal error")
			return
		}
		if !o.workspaces[id] {
			write(404, "no such workspace")
			return
		}
		delete(o.workspaces, id)
		o.deleted = append(o.deleted, id)
		write(200, "deleted")
		return
	}
	rest := p[strings.Index(p[3:], "/")+4:] // after /w/{ws}/
	switch {
	case rest == "jobs/queue/count":
		write(200, `{"database_length":0}`)
	case rest == "scripts/list":
		write(200, `[{"path":"f/wl_smoke/double","hash":"5a1f"}]`)
	case rest == "scripts/deployment_status/h/5a1f":
		write(200, `{"lock":"{\n  \"dependencies\": {}\n}\n//bun.lock\n<empty>","lock_error_logs":null,"job_id":null}`)
	case rest == "flows/list":
		write(200, `[{"path":"f/wl_smoke/two_step"}]`)
	case rest == "flows/deployment_status/p/f/wl_smoke/two_step":
		write(200, `{"lock_error_logs":null,"job_id":"20000000-0000-4000-8000-000000000001"}`)
	case rest == "jobs_u/completed/get_result_maybe/20000000-0000-4000-8000-000000000001":
		write(200, `{"completed":true,"success":true,"result":{}}`)
	case rest == "flows/get/f/wl_smoke/two_step":
		write(200, `{"path":"f/wl_smoke/two_step","value":{"modules":[{"id":"a"},{"id":"b"}]}}`)
	case strings.HasPrefix(rest, "jobs/run/f/"):
		o.run(w, r)
	case strings.HasPrefix(rest, "jobs_u/queue/cancel/"):
		id := rest[strings.LastIndex(rest, "/")+1:]
		o.cancels = append(o.cancels, id)
		if j, ok := o.jobs[id]; ok {
			j.Canceled, j.Success = true, false
			o.jobs[id] = j
		}
		write(200, "canceled")
	case strings.HasPrefix(rest, "jobs_u/"):
		id := rest[strings.LastIndex(rest, "/")+1:]
		j, ok := o.jobs[id]
		if !ok {
			write(404, "no job")
			return
		}
		switch {
		case strings.HasPrefix(rest, "jobs_u/get/"):
			job := map[string]any{"type": "CompletedJob", "id": id, "job_kind": j.Kind, "success": j.Success, "is_skipped": false, "canceled": false, "created_at": "2026-10-28T00:00:00Z"}
			if j.FlowStatus != nil {
				job["flow_status"] = j.FlowStatus
			}
			data, _ := json.Marshal(job)
			write(200, string(data))
		case strings.HasPrefix(rest, "jobs_u/get_args/"):
			write(200, string(j.Args))
		default:
			if o.hang && j.Kind == "flow" && !j.Canceled {
				write(200, `{"completed":false,"result":null}`)
				return
			}
			write(200, fmt.Sprintf(`{"completed":true,"started":true,"success":%v,"result":%s}`, j.Success, j.Result))
		}
	default:
		o.t.Errorf("fake oracle: unexpected %s %s", r.Method, r.URL)
		write(404, "unexpected")
	}
}

func (o *fakeOracle) run(w http.ResponseWriter, r *http.Request) {
	o.runs++
	root := r.URL.Query().Get("job_id")
	var args struct {
		X       int  `json:"x"`
		Effects bool `json:"effects"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &args)
	a, b := o.newID(), o.newID()
	n := 2 * args.X
	if o.jitter {
		n += o.runs
	}
	aRes := fmt.Sprintf(`{"doubled":%d,"seen":%d}`, n, args.X)
	if o.extra {
		aRes = fmt.Sprintf(`{"doubled":%d,"seen":%d,"extra":1}`, n, args.X)
	}
	bArgs := fmt.Sprintf(`{"n":%d,"tag":%q}`, n, o.tag)
	success := n <= 100
	bRes := fmt.Sprintf(`"%s:%d"`, o.tag, n)
	bType := "Success"
	if !success {
		bRes = fmt.Sprintf(`{"error":{"name":"Error","message":"too big: %d","stack":"Error: too big: %d\n    at run (/tmp/windmill/wk-x/%s/wrapper.mjs:30:21)","step_id":"b"}}`, n, n, b)
		bType = "Failure"
	}
	fs := fmt.Sprintf(`{"step":2,"modules":[{"type":"Success","id":"a","job":%q},{"type":%q,"id":"b","job":%q}],"failure_module":{"type":"WaitingForPriorSteps","id":"failure"}}`, a, bType, b)
	o.jobs[root] = fakeJob{Kind: "flow", Success: success, FlowStatus: json.RawMessage(fs), Args: body, Result: json.RawMessage(bRes)}
	o.jobs[a] = fakeJob{Kind: "script", Success: true, Args: json.RawMessage(fmt.Sprintf(`{"x":%d}`, args.X)), Result: json.RawMessage(aRes)}
	o.jobs[b] = fakeJob{Kind: "flowscript", Success: success, Args: json.RawMessage(bArgs), Result: json.RawMessage(bRes)}
	if args.Effects {
		u, err := url.Parse(o.docker.sinkURL)
		if err != nil {
			o.t.Fatal(err)
		}
		sink := "http://127.0.0.1:" + u.Port()
		for _, e := range []string{
			fmt.Sprintf(`{"root":%q,"job":%q,"step":"a"}`, root, a),
			`{"root":"99999999-9999-4999-8999-999999999999","job":"x","step":"t"}`,
		} {
			resp, err := http.Post(sink, "application/json", strings.NewReader(e))
			if err == nil {
				resp.Body.Close()
			}
		}
	}
	w.WriteHeader(201)
	io.WriteString(w, root)
}

type sessionRig struct {
	docker  *fakeDocker
	oracle  *fakeOracle
	srv     *httptest.Server
	fixture string
	cliLog  string
}

// newRig copies the smoke fixture into a temporary directory and adds a
// third run whose steps post effects.
func newRig(t *testing.T) *sessionRig {
	t.Helper()
	d := newFakeDocker(t)
	o := newFakeOracle(t, d)
	srv := httptest.NewServer(o)
	t.Cleanup(srv.Close)
	dir := filepath.Join(t.TempDir(), "smoke")
	if err := CopyTree("../../conformance/flows/oracle/smoke", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "recorded")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs", "effects.yaml"), []byte("target: f/wl_smoke/two_step\nargs: {x: 3, effects: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &sessionRig{docker: d, oracle: o, srv: srv, fixture: dir, cliLog: filepath.Join(t.TempDir(), "cli.log")}
}

func (r *sessionRig) session(free uint64) *Session {
	return &Session{
		Docker:       r.docker,
		CLI:          CLI{Command: []string{os.Args[0], "-test.run=^TestFakeCLI$", "--", fakeCLIFlag, r.cliLog, "-"}},
		SinkListen:   "127.0.0.1:0",
		Repeat:       2,
		RunTimeout:   time.Minute,
		Harness:      HarnessInfo{Name: "wltest", Version: "test", Go: "go"},
		baseURL:      r.srv.URL,
		pollInterval: time.Millisecond,
		measure:      func() (uint64, error) { return free, nil },
		settle:       time.Millisecond,
	}
}

func (r *sessionRig) load(t *testing.T) *conformance.Fixture {
	t.Helper()
	f, err := conformance.LoadFixture(r.fixture)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func outcomes(rep *Report) map[string]RunReport {
	m := map[string]RunReport{}
	for _, r := range rep.Runs {
		m[r.Run] = r
	}
	return m
}

// TestSessionRecordsTheSmokeFixture records the smoke fixture, then checks
// the recording on a second session: equal observations with new
// provenance are unchanged (OR-V5/a), a changed observation fails the check
// with a JSON-pointer difference, and a run's expect is enforced.
func TestSessionRecordsTheSmokeFixture(t *testing.T) {
	rig := newRig(t)
	ctx := context.Background()

	// The first session pulls both images, records every run in place.
	rep, err := rig.session(20*GiB).Run(ctx, []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(rep)
	for _, name := range []string{"effects", "fail", "ok"} {
		if got[name].Outcome != OutcomeNew || got[name].Wrote == "" {
			t.Fatalf("run %s: %+v", name, got[name])
		}
	}
	if rig.docker.called("pull ") != 2 || rig.docker.calledWith(" up -d") != 1 || rig.docker.calledWith(" down -v") != 1 || rig.docker.up {
		t.Fatalf("docker calls %q", rig.docker.calls)
	}
	if rep.Session.Images["windmill"].RepoDigests[0] != WindmillRepo+"@"+WindmillDigest || len(rep.Session.Teardown) != 0 {
		t.Fatalf("session report %+v", rep.Session)
	}
	log, _ := os.ReadFile(rig.cliLog)
	if !strings.Contains(string(log), "--version") || !strings.Contains(string(log), "--base-url "+rig.srv.URL+" --token session-token --workspace wl-") || !strings.Contains(string(log), " sync push --yes") {
		t.Fatalf("CLI calls:\n%s", log)
	}

	f := rig.load(t)
	rec := map[string]*conformance.Recording{}
	for _, r := range f.Runs {
		if r.Recorded == nil {
			t.Fatalf("run %s has no recording", r.Name)
		}
		rec[r.Name] = r.Recorded
	}
	// The recordings match the reference's observations: the expect of ok
	// and fail (OR-11/a, OR-12/a) was compared during the session.
	b := rec["fail"].Observation.Steps[conformance.SitePath{}.Child(conformance.Module("b"))]
	var compact bytes.Buffer
	if err := json.Compact(&compact, b.Result); err != nil || compact.String() != `{"error":{"name":"Error","message":"too big: 120","stack":"\u003cstack\u003e","step_id":"b"}}` && compact.String() != `{"error":{"name":"Error","message":"too big: 120","stack":"<stack>","step_id":"b"}}` {
		t.Fatalf("fail run step b: %s", b.Result)
	}
	// Effects are grouped by the outer run and normalised (OR-16, OR-V6).
	effects := rec["effects"].Observation.Effects
	compact.Reset()
	if len(effects) == 1 {
		_ = json.Compact(&compact, effects[0])
	}
	if compact.String() != `{"root":"<job:>","job":"<job:m:a>","step":"a"}` {
		t.Fatalf("effects %s", effects)
	}
	var prov Provenance
	if err := json.Unmarshal(rec["effects"].Provenance, &prov); err != nil {
		t.Fatal(err)
	}
	if prov.Oracle.Version != OracleVersion || prov.Oracle.Image != WindmillImage || prov.Oracle.CLI != CLIPackage || prov.Oracle.CLIVersion != "CLI version: "+CLIVersion ||
		prov.Repeats != 2 || prov.Workers != 1 || prov.ForeignEffects != 1 || prov.Env["LISTEN_NEW_EVENTS_INTERVAL_SEC"] != "1" ||
		!strings.HasPrefix(prov.Env["WL_EFFECT_SINK"], "http://host.docker.internal:") || prov.Timings.Jobs["m:a"].Kind != "script" {
		t.Fatalf("provenance %+v", prov)
	}

	// A second session in check mode: the provenance differs, the
	// observations do not (OR-V5/a).
	s := rig.session(20 * GiB)
	s.Check = true
	rep, err = s.Run(ctx, []*conformance.Fixture{f})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeUnchanged || r.Failed(true) || r.Wrote != "" {
			t.Fatalf("check of an unchanged oracle: %+v", r)
		}
	}
	if rig.docker.called("pull ") != 2 {
		t.Fatal("present images are not pulled again")
	}

	// The oracle now answers differently: ok fails its expect (OR-15/b);
	// effects, which has no expect, changes against its recording.
	rig.oracle.tag = "ho"
	before, err := os.ReadFile(filepath.Join(rig.fixture, "recorded", "effects.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	s = rig.session(20 * GiB)
	s.Check, s.OutDir, s.RepoRoot = true, out, filepath.Dir(rig.fixture)
	rep, err = s.Run(ctx, []*conformance.Fixture{f})
	if err != nil {
		t.Fatal(err)
	}
	got = outcomes(rep)
	if got["ok"].Outcome != OutcomeExpectMismatch || !contains(got["ok"].Differences, `/steps/m:b/args/tag: "ho" != "hi"`) {
		t.Fatalf("ok: %+v", got["ok"])
	}
	if got["effects"].Outcome != OutcomeChanged || !got["effects"].Failed(true) || !contains(got["effects"].Differences, `/result: "ho:6" != "hi:6"`) {
		t.Fatalf("effects: %+v", got["effects"])
	}
	if want := filepath.Join(out, "smoke", "recorded", "effects.json"); got["effects"].Wrote != want {
		t.Fatalf("check mode wrote %q, want %q", got["effects"].Wrote, want)
	}
	if after, err := os.ReadFile(filepath.Join(rig.fixture, "recorded", "effects.json")); err != nil || !bytes.Equal(after, before) {
		t.Fatal("check mode must not touch the fixture's own recording")
	}
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

// TestSessionUnstableObservation is OR-14/a: repeated runs must observe
// alike.
func TestSessionUnstableObservation(t *testing.T) {
	rig := newRig(t)
	rig.oracle.jitter = true
	rep, err := rig.session(20*GiB).Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeUnstable || len(r.Differences) == 0 {
			t.Fatalf("%+v", r)
		}
	}
}

// TestSessionDeletesEachWorkspace: Community Edition keeps at most two
// workspaces besides admins, so a session of three fixtures records all
// three only because each fixture's workspace is deleted before the next
// is created (OR-6). The fake oracle refuses a third workspace as CE does.
func TestSessionDeletesEachWorkspace(t *testing.T) {
	// spec: OR-6
	rig := newRig(t)
	fixtures := []*conformance.Fixture{rig.load(t)}
	for _, name := range []string{"second", "third"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := CopyTree(rig.fixture, dir); err != nil {
			t.Fatal(err)
		}
		f, err := conformance.LoadFixture(dir)
		if err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, f)
	}
	rep, err := rig.session(20*GiB).Run(context.Background(), fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != 9 {
		t.Fatalf("%d runs, want 9: %+v", len(rep.Runs), rep.Runs)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeNew {
			t.Errorf("%s %s: %s %s", r.Fixture, r.Run, r.Outcome, r.Error)
		}
	}
	o := rig.oracle
	if len(o.created) != 3 || !slices.Equal(o.created, o.deleted) || len(o.workspaces) != 0 {
		t.Fatalf("created %q, deleted %q, left %v", o.created, o.deleted, o.workspaces)
	}
}

// TestSessionFailsWhenAWorkspaceCannotBeDeleted: a workspace left behind
// would take one of Community Edition's two places, so the session stops
// with the error after the fixture's runs, and still tears down.
func TestSessionFailsWhenAWorkspaceCannotBeDeleted(t *testing.T) {
	// spec: OR-6
	rig := newRig(t)
	rig.oracle.failDelete = true
	rep, err := rig.session(20*GiB).Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err == nil || !strings.Contains(err.Error(), "deleting workspace wl-") || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("session error %v", err)
	}
	if len(rep.Runs) != 3 {
		t.Fatalf("%d runs, want the fixture's 3 before the error: %+v", len(rep.Runs), rep.Runs)
	}
	if rig.docker.calledWith(" down -v") != 1 || rig.docker.up {
		t.Fatalf("not torn down: docker calls %q", rig.docker.calls)
	}
}

// TestSessionRefusals: the session refuses instead of running what it
// cannot honour, and tears down what it started.
func TestSessionRefusals(t *testing.T) {
	ctx := context.Background()
	code := func(err error) string {
		var r *Refusal
		if errors.As(err, &r) {
			return r.Code
		}
		return fmt.Sprint(err)
	}

	t.Run("OR-1/b: a version mismatch aborts the session", func(t *testing.T) {
		// spec: OR-1/b
		rig := newRig(t)
		rig.oracle.version = "EE v1.767.0"
		_, err := rig.session(20*GiB).Run(ctx, []*conformance.Fixture{rig.load(t)})
		if code(err) != "oracle_version_mismatch" || err.Error() != `{"error":"oracle_version_mismatch","got":"EE v1.767.0"}` || rig.docker.up {
			t.Fatalf("got %v, up %v", err, rig.docker.up)
		}
	})
	t.Run("OR-30/a: no pull without the reserve", func(t *testing.T) {
		// spec: OR-30/a
		rig := newRig(t)
		_, err := rig.session(9*GiB).Run(ctx, []*conformance.Fixture{rig.load(t)})
		if code(err) != "insufficient_disk" || rig.docker.called("pull ") != 0 || rig.docker.called("compose") != 0 {
			t.Fatalf("got %v; calls %q", err, rig.docker.calls)
		}
	})
	t.Run("OR-30/c: no oracle after a tight pull, and the images are removed", func(t *testing.T) {
		// spec: OR-30/c
		rig := newRig(t)
		s := rig.session(0)
		readings := []uint64{20 * GiB, 20 * GiB, 20 * GiB, gibs(4.8), gibs(4.8)}
		var mu sync.Mutex
		s.measure = func() (uint64, error) {
			mu.Lock()
			defer mu.Unlock()
			v := readings[0]
			if len(readings) > 1 {
				readings = readings[1:]
			}
			return v, nil
		}
		_, err := s.Run(ctx, []*conformance.Fixture{rig.load(t)})
		if code(err) != "insufficient_disk_after_pull" || rig.docker.called("image rm") != 2 || rig.docker.called("compose") != 0 {
			t.Fatalf("got %v; calls %q", err, rig.docker.calls)
		}
	})
	t.Run("OR-1: an image without the pinned digest fails the preflight", func(t *testing.T) {
		rig := newRig(t)
		rig.docker.digests[WindmillImage] = []string{WindmillRepo + "@sha256:0e743cd6"}
		_, err := rig.session(20*GiB).Run(ctx, []*conformance.Fixture{rig.load(t)})
		if code(err) != "oracle_image_mismatch" || rig.docker.called("compose") != 0 {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("OR-30: cached images below the floor never start the oracle", func(t *testing.T) {
		rig := newRig(t)
		for _, im := range Images {
			rig.docker.present[im.Ref] = true
		}
		_, err := rig.session(4*GiB).Run(ctx, []*conformance.Fixture{rig.load(t)})
		var r *Refusal
		if !errors.As(err, &r) || r.Error() != `{"error":"disk_floor_reached","phase":"start","free_gib":4.0,"floor_gib":5}` {
			t.Fatalf("got %v", err)
		}
		if rig.docker.called("compose") != 0 || rig.docker.called("pull") != 0 || rig.docker.called("image rm") != 0 {
			t.Fatalf("calls %q", rig.docker.calls)
		}
	})
	t.Run("OR-18: an earlier session that was not torn down", func(t *testing.T) {
		rig := newRig(t)
		rig.docker.leftovers = "abc\n"
		_, err := rig.session(20*GiB).Run(ctx, []*conformance.Fixture{rig.load(t)})
		if err == nil || !strings.Contains(err.Error(), "was not torn down") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("OR-5/a: a fixture that needs two workers on one", func(t *testing.T) {
		// spec: OR-5/a
		rig := newRig(t)
		f := rig.load(t)
		f.Oracle.Workers = 2
		rep, err := rig.session(20*GiB).Run(ctx, []*conformance.Fixture{f})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rep.Runs {
			if r.Outcome != OutcomeRefused || r.Error != `{"error":"fixture_requires_workers","need":2,"have":1}` || !r.Failed(false) {
				t.Fatalf("%+v", r)
			}
		}
		if rig.oracle.runs != 0 || rig.docker.up {
			t.Fatalf("ran %d runs; up %v", rig.oracle.runs, rig.docker.up)
		}
	})
}

// TestSessionSubsetFixtureRecordsExactly: a subset expect accepts an extra
// member, but the recording is compared exactly, so the oracle's change is
// reported (two oracle observations are never compared as a subset).
func TestSessionSubsetFixtureRecordsExactly(t *testing.T) {
	rig := newRig(t)
	ctx := context.Background()
	f := rig.load(t)
	f.Compare.Mode = "subset"
	if _, err := rig.session(20*GiB).Run(ctx, []*conformance.Fixture{f}); err != nil {
		t.Fatal(err)
	}
	f = rig.load(t)
	f.Compare.Mode = "subset"
	rig.oracle.extra = true
	s := rig.session(20 * GiB)
	s.Check = true
	rep, err := s.Run(ctx, []*conformance.Fixture{f})
	if err != nil {
		t.Fatal(err)
	}
	ok := outcomes(rep)["ok"]
	if ok.Outcome != OutcomeChanged || !contains(ok.Differences, `/steps/m:a/result/extra: 1 != (absent)`) {
		t.Fatalf("ok: %+v", ok)
	}
}

// TestSessionCancelsATimedOutRun: a run that does not complete within the
// timeout is canceled, so it cannot hold the worker for the next runs.
func TestSessionCancelsATimedOutRun(t *testing.T) {
	rig := newRig(t)
	rig.oracle.hang = true
	s := rig.session(20 * GiB)
	s.Repeat = 1
	s.RunTimeout, s.minRunTimeout = 50*time.Millisecond, time.Millisecond
	rep, err := s.Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeFailed || !strings.Contains(r.Error, "did not complete (canceled)") {
			t.Fatalf("%+v", r)
		}
	}
	if len(rig.oracle.cancels) != len(rep.Runs) {
		t.Fatalf("canceled %v for %d runs", rig.oracle.cancels, len(rep.Runs))
	}
}

// TestSessionOutDirStaysInside: --out never places a recording outside its
// directory, which would overwrite a fixture's own recording.
func TestSessionOutDirStaysInside(t *testing.T) {
	rig := newRig(t)
	s := rig.session(20 * GiB)
	s.Check, s.OutDir, s.RepoRoot = true, t.TempDir(), t.TempDir()
	rep, err := s.Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeFailed || !strings.Contains(r.Error, "is outside") || r.Wrote != "" {
			t.Fatalf("%+v", r)
		}
	}
}

// TestSessionRefusesARecordingOfAnotherOracle is OR-V5/b: a committed
// recording whose oracle identity does not match the pin is refused before
// anything runs, whatever its observation says.
func TestSessionRefusesARecordingOfAnotherOracle(t *testing.T) {
	// spec: OR-V5/b
	rig := newRig(t)
	ctx := context.Background()
	if _, err := rig.session(20*GiB).Run(ctx, []*conformance.Fixture{rig.load(t)}); err != nil {
		t.Fatal(err)
	}
	for n, c := range []struct {
		name, provenance, field string
	}{
		{"another version", `{"oracle":{"version":"EE v1.766.0","image":"` + WindmillImage + `","cli":"` + CLIPackage + `"}}`, "oracle.version"},
		{"another image", `{"oracle":{"version":"CE v1.767.0","image":"ghcr.io/windmill-labs/windmill-ee:1.767.0@sha256:0e743cd6","cli":"` + CLIPackage + `"}}`, "oracle.image"},
		{"another CLI", `{"oracle":{"version":"CE v1.767.0","image":"` + WindmillImage + `","cli":"windmill-cli@1.766.0"}}`, "oracle.cli"},
		{"no CLI", `{"oracle":{"version":"CE v1.767.0","image":"` + WindmillImage + `"}}`, "oracle.cli"},
		{"no oracle", `{"recorded_at":"2026-10-28T00:00:00Z"}`, "oracle"},
		{"not an object", `[]`, "oracle"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := rig.load(t)
			for i := range f.Runs {
				f.Runs[i].Recorded.Provenance = json.RawMessage(c.provenance)
			}
			before := rig.oracle.runs
			// Check mode for every variant; record mode, which would
			// otherwise rewrite the recording, for the first.
			modes := []bool{true}
			if n == 0 {
				modes = append(modes, false)
			}
			for _, check := range modes {
				s := rig.session(20 * GiB)
				s.Check = check
				rep, err := s.Run(ctx, []*conformance.Fixture{f})
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rep.Runs {
					if r.Outcome != OutcomeRefused || !strings.Contains(r.Error, `"error":"recording_identity_mismatch","field":"`+c.field+`"`) || !r.Failed(check) || r.Wrote != "" {
						t.Fatalf("check %v: %+v", check, r)
					}
				}
			}
			if rig.oracle.runs != before {
				t.Fatalf("a refused recording still ran %d runs", rig.oracle.runs-before)
			}
			if log, _ := os.ReadFile(rig.cliLog); strings.Count(string(log), "sync push") != 1 {
				t.Fatalf("a refused fixture was pushed:\n%s", log)
			}
		})
	}
}

// TestSessionKillsAHungCLIAndTearsDown: when the disk falls below the floor
// during a CLI push that never returns, the CLI is killed, the session
// returns promptly with the refusal, and the stack is torn down (OR-18,
// OR-30 step 7).
func TestSessionKillsAHungCLIAndTearsDown(t *testing.T) {
	rig := newRig(t)
	marker := filepath.Join(t.TempDir(), "pushing")
	s := rig.session(0)
	s.CLI.Command[len(s.CLI.Command)-1] = marker
	s.measure = func() (uint64, error) {
		if _, err := os.Stat(marker); err == nil {
			return 4 * GiB, nil
		}
		return 20 * GiB, nil
	}
	s.runWatch = 20 * time.Millisecond
	start := time.Now()
	_, err := s.Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	elapsed := time.Since(start)
	var r *Refusal
	if !errors.As(err, &r) || r.Code != "disk_floor_reached" {
		t.Fatalf("got %v", err)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("the session took %s to stop", elapsed)
	}
	if rig.docker.calledWith(" down -v") != 1 || rig.docker.up {
		t.Fatalf("calls %q", rig.docker.calls)
	}
}

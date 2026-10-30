package wl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform/facade"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// The tests without Bun open bundles written here by hand: a program of
// identity modules, which runs no step, its data/ and a manifest in
// canonical form. bun_test.go builds real bundles and runs their steps.

// testFlows are synthetic flows, each a flow value as JSON. f/t/which
// returns "host" when the variable u/t/v reads "host", and "bundle"
// otherwise (ADR 0007: a host's variable replaces the bundle's); f/t/sleep
// sleeps 30 s before its step.
var testFlows = map[string]string{
	"f/t/echo":  `{"modules":[{"id":"a","value":{"type":"identity"}}]}`,
	"f/t/dup":   `{"modules":[{"id":"a","value":{"type":"identity"}},{"id":"a","value":{"type":"identity"}}]}`,
	"f/t/sleep": `{"modules":[{"id":"a","value":{"type":"identity"},"sleep":{"type":"static","value":30}}]}`,
	"f/t/which": `{"modules":[{"id":"b","value":{"type":"branchone","branches":[{"expr":"variable(\"u/t/v\") === \"host\"","modules":[` +
		`{"id":"h","value":{"type":"identity"},"mock":{"enabled":true,"return_value":"host"}}]}],` +
		`"default":[{"id":"d","value":{"type":"identity"},"mock":{"enabled":true,"return_value":"bundle"}}]}}]}`,
}

// writeTestBundle writes a bundle of testFlows below a new directory, with
// variables in its data/, built by engine version, and returns it.
func writeTestBundle(t *testing.T, version string, variables map[string]string, edit func(m *bundle.Manifest)) string {
	t.Helper()
	return writeBundleOf(t, testFlows, nil, version, variables, edit)
}

// writeBundleOf is writeTestBundle for the given flows, compiled with the
// host contract (YAML or JSON) when it is not nil, which the manifest then
// records.
func writeBundleOf(t *testing.T, flows map[string]string, contract []byte, version string, variables map[string]string, edit func(m *bundle.Manifest)) string {
	t.Helper()
	fsys := fstest.MapFS{"wmill.yaml": {Data: []byte("defaultTs: bun\n")}}
	for p, v := range flows {
		fsys[p+".flow/flow.yaml"] = &fstest.MapFile{Data: []byte(`{"summary":"t","value":` + v + `}`)}
	}
	folder, err := syncfolder.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	var c *ir.Contract
	if contract != nil {
		if c, err = ir.ParseContract(contract); err != nil {
			t.Fatal(err)
		}
	}
	prog, err := ir.Compile(folder, c, ir.Options{})
	if err != nil {
		t.Fatal(err)
	}
	irJSON, err := prog.MarshalIndent()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"ir.json": irJSON}
	if len(variables) > 0 {
		data := map[string]any{}
		for p, v := range variables {
			data[p] = map[string]any{"value": v, "is_secret": false}
		}
		files["data/variables.json"], _ = json.Marshal(data)
	}
	m := &bundle.Manifest{
		Format:          bundle.FormatVersion,
		Engine:          bundle.Engine{Version: version, ExpressionStackBytes: 1 << 20},
		Bundlers:        bundle.Bundlers{Bun: "1.3.10"},
		ModuleState:     engine.ModuleStateWarm,
		Deviations:      []string{"D1", "D2", "D3", "D9", "D10", "D11", "D14"},
		Contract:        ir.Contract{Roots: []ir.Root{{Flow: "f/t/echo"}, {Flow: "f/t/which"}}},
		HostModules:     []bundle.HostModuleEntry{},
		HarnessRuntimes: map[string]string{"bun": bundle.Digest([]byte("runtime"))},
		Sources:         map[string]string{"wmill.yaml": bundle.Digest([]byte("defaultTs: bun\n"))},
		Files:           map[string]string{},
	}
	dir := t.TempDir()
	for p, data := range files {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, data, 0o644); err != nil {
			t.Fatal(err)
		}
		m.Files[p] = bundle.Digest(data)
	}
	if c != nil {
		m.Contract = prog.Contract
	}
	if edit != nil {
		edit(m)
	}
	manifest, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, bundle.ManifestFile), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Open verifies a bundle and runs its flows; BuildInfo reports the
// manifest and the module state in effect; Close removes the data
// directory Open made, and is idempotent.
func TestOpenAndRun(t *testing.T) {
	dir := writeTestBundle(t, Version(), nil, nil)
	rt, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := rt.Run(context.Background(), "f/t/echo", json.RawMessage(`{"x":1}`), RunOptions{ID: "11111111-1111-4111-8111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusSuccess || string(res.Result) != `{"x":1}` || res.ID != "11111111-1111-4111-8111-111111111111" || res.Trace == nil || res.MayHaveMutated {
		t.Fatalf("result %+v", res)
	}
	if res.Trace.RunID != res.ID || len(res.Trace.Steps) != 1 {
		t.Fatalf("trace %+v", res.Trace)
	}
	bi := rt.BuildInfo()
	digest, _ := os.ReadFile(filepath.Join(dir, bundle.ManifestFile))
	if bi.BundleDigest != bundle.Digest(digest) || bi.EngineVersion != Version() || bi.BunVersion != "1.3.10" || bi.ModuleState != ModuleStateWarm ||
		!slices.Equal(bi.Deviations, []string{"D1", "D2", "D3", "D9", "D10", "D11", "D14"}) || !strings.Contains(string(bi.Contract), "f/t/which") {
		t.Fatalf("build info %+v", bi)
	}
	data := rt.data
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the data directory %s is still there: %v", data, err)
	}

	// A module-state override is in effect, with its mode's deviations
	// (ADR 0006) in place of the manifest's mode's, both ways.
	rt, err = Open(dir, Options{ModuleState: ModuleStateFresh})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if bi := rt.BuildInfo(); bi.ModuleState != ModuleStateFresh || !slices.Equal(bi.Deviations, []string{"D3", "D14"}) {
		t.Fatalf("fresh override: %s %v", bi.ModuleState, bi.Deviations)
	}
	fresh := writeTestBundle(t, Version(), nil, func(m *bundle.Manifest) {
		m.ModuleState, m.Deviations = engine.ModuleStateFresh, []string{"D3", "D8", "D14"}
	})
	rt2, err := Open(fresh, Options{ModuleState: ModuleStateWarm})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()
	if bi := rt2.BuildInfo(); bi.ModuleState != ModuleStateWarm || !slices.Equal(bi.Deviations, []string{"D1", "D2", "D3", "D8", "D9", "D10", "D11", "D14"}) {
		t.Fatalf("warm override: %s %v", bi.ModuleState, bi.Deviations)
	}
}

// A .wlb archive is extracted into a directory of its own under the data
// directory, which Close removes while a given DataDir, with its object
// store, stays: the same DataDir opens again, after a clean Close and after
// an open that failed once the archive was extracted.
func TestOpenArchive(t *testing.T) {
	archiveOf := func(dir string) string {
		archive := filepath.Join(t.TempDir(), "b"+bundle.ArchiveExt)
		if err := bundle.WriteArchiveFile(dir, archive); err != nil {
			t.Fatal(err)
		}
		return archive
	}
	archive := archiveOf(writeTestBundle(t, Version(), nil, nil))
	data := filepath.Join(t.TempDir(), "data")
	extracted := func() []string {
		m, _ := filepath.Glob(filepath.Join(data, "bundle-*"))
		return m
	}
	for i := range 2 {
		rt, err := Open(archive, Options{DataDir: data})
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if len(extracted()) != 1 {
			t.Fatalf("open %d: extracted into %v", i, extracted())
		}
		if res, err := rt.Run(context.Background(), "f/t/echo", nil, RunOptions{}); err != nil || res.Status != StatusSuccess || string(res.Result) != `{}` {
			t.Fatalf("open %d: %+v %v", i, res, err)
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
		if m := extracted(); len(m) != 0 {
			t.Fatalf("open %d: Close left %v", i, m)
		}
		if _, err := os.Stat(filepath.Join(data, "objects")); err != nil {
			t.Fatalf("open %d: the object store: %v", i, err)
		}
	}
	if _, err := Open(archiveOf(writeTestBundle(t, "v9.9.9", nil, nil)), Options{DataDir: data}); !errors.Is(err, ErrEngineVersion) {
		t.Fatalf("another engine version: %v", err)
	}
	if m := extracted(); len(m) != 0 {
		t.Fatalf("a failed open left %v", m)
	}
	rt, err := Open(archive, Options{DataDir: data})
	if err != nil {
		t.Fatalf("open after a failed one: %v", err)
	}
	rt.Close()
}

// The host's variables replace the bundle's data/ (ADR 0007): the bundle's
// u/t/v reads "bundle", and a host's "host".
func TestOpenHostData(t *testing.T) {
	dir := writeTestBundle(t, Version(), map[string]string{"u/t/v": "bundle"}, nil)
	for _, c := range []struct {
		vars map[string]Variable
		want string
	}{
		{nil, `"bundle"`},
		{map[string]Variable{"u/t/v": {Value: "host"}}, `"host"`},
	} {
		rt, err := Open(dir, Options{Variables: c.vars})
		if err != nil {
			t.Fatal(err)
		}
		res, err := rt.Run(context.Background(), "f/t/which", nil, RunOptions{})
		rt.Close()
		if err != nil || res.Status != StatusSuccess || string(res.Result) != c.want {
			t.Errorf("variables %v: %+v %v, want %s", c.vars, res, err, c.want)
		}
	}
}

// Open refuses a bundle another engine version built, one whose files do
// not match its manifest or the pinned digest, an unknown module state, a
// program of another IR version, and a bundle with steps but no harness
// runtime; Run refuses an unknown target and kind.
func TestOpenRefuses(t *testing.T) {
	if _, err := Open(writeTestBundle(t, "v9.9.9", nil, nil), Options{}); !errors.Is(err, ErrEngineVersion) {
		t.Errorf("another engine version: %v", err)
	}
	dir := writeTestBundle(t, Version(), nil, nil)
	if _, err := Open(dir, Options{Digest: "sha256:" + strings.Repeat("0", 64)}); err == nil {
		t.Error("opened a bundle whose digest is not the pinned one")
	}
	if _, err := Open(dir, Options{ModuleState: "tepid"}); err == nil {
		t.Error("opened with an unknown module state")
	}
	tampered := writeTestBundle(t, Version(), nil, nil)
	os.WriteFile(filepath.Join(tampered, "ir.json"), []byte("{}\n"), 0o644)
	if _, err := Open(tampered, Options{}); err == nil {
		t.Error("opened a bundle whose ir.json does not match the manifest")
	}
	old := writeTestBundle(t, Version(), nil, nil)
	ir1 := filepath.Join(old, "ir.json")
	data, _ := os.ReadFile(ir1)
	data = []byte(strings.Replace(string(data), `"version": 2`, `"version": 1`, 1))
	os.WriteFile(ir1, data, 0o644)
	if _, err := readProgram(ir1); err == nil {
		t.Error("read a program of another IR version")
	}

	rt, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.Run(context.Background(), "f/t/none", nil, RunOptions{}); err == nil {
		t.Error("ran an unknown flow")
	}
	if _, err := rt.Run(context.Background(), "f/t/echo", nil, RunOptions{Kind: "job"}); err == nil {
		t.Error("ran an unknown kind")
	}
	if _, err := rt.Run(context.Background(), "f/t/echo", nil, RunOptions{Kind: KindScript}); err == nil {
		t.Error("ran a flow as a script")
	}
	if _, err := rt.Run(context.Background(), "f/t/echo", json.RawMessage(`[1]`), RunOptions{}); err == nil {
		t.Error("ran a flow with arguments that are not an object")
	}
}

// A run's context bounds it: a canceled one ends canceled, with its
// cause's reason and canceler (FX-38).
func TestRunCanceled(t *testing.T) {
	rt, err := Open(writeTestBundle(t, Version(), nil, nil), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(&Cancellation{Reason: "r", Canceler: "u"})
	res, err := rt.Run(ctx, "f/t/echo", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusCanceled || !strings.Contains(string(res.Result), `"Job canceled: r by u"`) {
		t.Fatalf("%+v", res)
	}
}

// Close ends a run in flight: one in a 30 s sleep ends canceled, with the
// runtime's reason, before Close returns; a run after Close is refused.
func TestCloseActiveRun(t *testing.T) {
	rt, err := Open(writeTestBundle(t, Version(), nil, nil), Options{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *Result, 1)
	go func() {
		res, err := rt.Run(context.Background(), "f/t/sleep", nil, RunOptions{})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case res := <-done:
		t.Fatalf("the run did not sleep: %+v", res)
	default:
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res == nil || res.Status != StatusCanceled || !strings.Contains(string(res.Result), `"Job canceled: the runtime is closing by windmill-lite"`) {
			t.Fatalf("%+v", res)
		}
	default:
		t.Fatal("Close returned with the run still in its sleep")
	}
	if _, err := rt.Run(context.Background(), "f/t/echo", nil, RunOptions{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("a run after Close: %v", err)
	}
	if _, err := rt.Run(context.Background(), "f/t/echo", nil, RunOptions{Kind: KindScript}); !errors.Is(err, ErrClosed) {
		t.Fatalf("a script run after Close: %v", err)
	}
}

// Runs that race Close each either run, to success or canceled, or are
// refused with ErrClosed; Close waits for every run it admitted.
func TestCloseRacesRuns(t *testing.T) {
	rt, err := Open(writeTestBundle(t, Version(), nil, nil), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var ran, refused atomic.Int32
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := "f/t/echo"
			if i%2 == 1 {
				path = "f/t/sleep"
			}
			res, err := rt.Run(context.Background(), path, nil, RunOptions{})
			switch {
			case errors.Is(err, ErrClosed):
				refused.Add(1)
			case err != nil:
				t.Error(err)
			case res.Status == StatusSuccess || res.Status == StatusCanceled:
				ran.Add(1)
			default:
				t.Errorf("%+v", res)
			}
		}()
	}
	time.Sleep(5 * time.Millisecond)
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if ran.Load()+refused.Load() != 32 {
		t.Fatalf("%d ran, %d refused", ran.Load(), refused.Load())
	}
}

// stepFunc is a step executor of a function.
type stepFunc func(context.Context, engine.Invocation) (engine.StepOutcome, error)

func (f stepFunc) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	return f(ctx, inv)
}

// A call the step did not await, answered after the facade released the
// step's invocation while the run goes on, still goes to the run's
// platform calls (ADR 0007, In order); the recorder forgets the
// invocation when the run ends, and at release one whose run is not in
// flight.
func TestRecorderAnswerAfterRelease(t *testing.T) {
	x := &recorder{runs: map[string]*callLog{}, invs: map[string]invocation{}}
	calls := x.begin("run")
	answering, answer := make(chan struct{}), make(chan struct{})
	fac, err := facade.New(facade.Config{Recorded: x.released, Answered: func(inv string, c facade.Record) {
		// The answer is decided; its delivery waits until the invocation
		// has been released.
		close(answering)
		<-answer
		x.answered(inv, c)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer fac.Close()
	reply := make(chan harness.PlatformReply, 1)
	x.inner = fac.Executor(stepFunc(func(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
		if inv.ID == "job" {
			// The step fetches without awaiting the answer, and settles.
			serve := fac.Platform()(harness.PlatformRequest{ID: "p", Inv: inv.ID, Method: "GET", URL: fac.WarmOrigin() + "/api/w/ws/jobs_u/get_root_job_id/job"})
			go func() { reply <- serve(context.Background()) }()
			<-answering
		}
		inv.Report.Settled(engine.SettleCompleted)
		return engine.StepOutcome{Kind: engine.OutcomeOK, Body: jsonval.FromProducer([]byte("null"))}, nil
	}))
	if _, err := x.Execute(context.Background(), engine.Invocation{ID: "job", Run: "run", Site: conformance.SitePath{}.Child(conformance.Module("a")),
		Env: map[string]string{"WM_JOB_ID": "job", "WM_ROOT_JOB_ID": "run"}}); err != nil {
		t.Fatal(err)
	}
	close(answer)
	if r := <-reply; r.Status != 200 {
		t.Fatalf("reply %+v", r)
	}
	if got := calls.taken(); len(got) != 1 || got[0].Job != "job" || got[0].Site.String() != "m:a" || got[0].Status != 200 {
		t.Fatalf("calls %+v", got)
	}
	x.end("run")
	x.mu.Lock()
	n := len(x.invs)
	x.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d invocations known after the run ended", n)
	}
	// An invocation of no run in flight, a script run's, is forgotten at
	// release.
	if _, err := x.Execute(context.Background(), engine.Invocation{ID: "script", Run: "script", Env: map[string]string{"WM_JOB_ID": "script", "WM_ROOT_JOB_ID": "script"}}); err != nil {
		t.Fatal(err)
	}
	x.mu.Lock()
	n = len(x.invs)
	x.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d invocations known after a release with no run in flight", n)
	}
}

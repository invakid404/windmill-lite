package wl

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
	"github.com/invakid404/windmill-lite/internal/platform/facade"
)

// storeFiles is a bundle whose flow f/s/write creates the state f/s/st
// with its input's value through the job API, or updates it when it
// exists, and whose flow f/s/read returns it through a $res: argument.
var storeFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/s/write.ts": `export async function main(value: unknown) {
  const base = ` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/resources`" + `;
  const headers = { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + `, "Content-Type": "application/json" };
  const r = await fetch(` + "`${base}/create`" + `, { method: "POST", headers, body: JSON.stringify({ path: "f/s/st", value, resource_type: "state" }) });
  const text = await r.text();
  if (r.status === 400 && text.endsWith("already exists")) {
    const u = await fetch(` + "`${base}/update_value/f/s/st`" + `, { method: "POST", headers, body: JSON.stringify({ value }) });
    if (!u.ok) throw new Error("update: " + u.status + " " + (await u.text()));
    return "updated";
  }
  if (!r.ok) throw new Error("create: " + r.status + " " + text);
  return "created";
}
`,
	"f/s/write.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/s/write",` +
		`"input_transforms":{"value":{"type":"javascript","expr":"flow_input.value"}}}}]}}`,
	"f/s/read.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun",` +
		`"content":"export async function main(x: unknown) { return x }","input_transforms":{"x":{"type":"static","value":"$res:f/s/st"}}}}]}}`,
}

// offlineStore fails every operation, as a store whose server is down.
type offlineStore struct{}

var errOffline = errors.New("store offline")

func (offlineStore) Get(context.Context, string, string) (Resource, bool, error) {
	return Resource{}, false, errOffline
}
func (offlineStore) Create(context.Context, string, string, Resource) (bool, error) {
	return false, errOffline
}
func (offlineStore) UpdateValue(context.Context, string, string, string, json.RawMessage) (bool, error) {
	return false, errOffline
}
func (offlineStore) Delete(context.Context, string, string, string) (bool, error) {
	return false, errOffline
}

// TestResourceStoreOnBun checks Options.ResourceStore on real steps: two
// runtimes given one memory store see each other's writes, through the job
// API and a $res: argument, and keep them in the store; a runtime with the
// default store sees none of them, and its runs list D24 (ADR 0001); a
// failing store fails the step that reaches it, and the run's warnings
// name the call and the store's error.
func TestResourceStoreOnBun(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBunBundle(t, bun, storeFiles)
	open := func(store ResourceStore) *Runtime {
		t.Helper()
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ResourceStore: store})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rt.Close() })
		return rt
	}
	run := func(rt *Runtime, path, args string) *Result {
		t.Helper()
		res, err := rt.Run(context.Background(), path, json.RawMessage(args), RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	shared := NewMemoryResourceStore()
	a, b, own := open(shared), open(shared), open(nil)
	for i, c := range []struct {
		rt         *Runtime
		path, args string
		want       string
	}{
		{a, "f/s/write", `{"value":{"n":1}}`, `"created"`},
		{b, "f/s/read", `{}`, `{"n":1}`},
		{b, "f/s/write", `{"value":{"n":2}}`, `"updated"`},
		{a, "f/s/read", `{}`, `{"n":2}`},
	} {
		res := run(c.rt, c.path, c.args)
		if res.Status != StatusSuccess || string(res.Result) != c.want {
			t.Fatalf("run %d, %s: %s %s, want success %s", i, c.path, res.Status, res.Result, c.want)
		}
		if !slices.Contains(res.Trace.Deviations, "D24") {
			t.Errorf("run %d on a shared memory store: deviations %v, want D24 among them", i, res.Trace.Deviations)
		}
	}
	r, ok, err := shared.Get(context.Background(), platform.DefaultHost.Workspace, "f/s/st")
	if err != nil || !ok || r.Type != "state" || string(r.Value) != `{"n":2}` {
		t.Errorf("the shared store holds %+v %v %v, want the state {\"n\":2}", r, ok, err)
	}
	if res := run(own, "f/s/read", `{}`); res.Status == StatusSuccess || !slices.Contains(res.Trace.Deviations, "D24") {
		t.Errorf("a runtime with its own store read %s %s, deviations %v: want a failure, D24 among them", res.Status, res.Result, res.Trace.Deviations)
	}

	failing := open(offlineStore{})
	res := run(failing, "f/s/write", `{"value":1}`)
	if res.Status != StatusFailure {
		t.Errorf("a write on a failing store: %s %s, want a failure", res.Status, res.Result)
	}
	const code, message = "resource_store_failed", "POST /w/{ws}/resources/create: Internal: windmill-lite: the resource store failed: store offline"
	if !slices.ContainsFunc(res.Warnings, func(w Warning) bool { return w.Code == code && w.Message == message }) {
		t.Errorf("warnings %+v, want %s %q among them", res.Warnings, code, message)
	}
	if slices.Contains(res.Trace.Deviations, "D24") {
		t.Errorf("a run on a store that is not the memory store lists D24: %v", res.Trace.Deviations)
	}
	// A $res: argument the engine resolves, which no platform call makes:
	// the step fails with the store's error, not a missing resource, and
	// the run's warnings name it.
	res = run(failing, "f/s/read", `{}`)
	const engineMessage = "engine read: Internal: windmill-lite: the resource store failed: store offline"
	if res.Status != StatusFailure || !strings.Contains(string(res.Result), "the resource store failed: store offline") || strings.Contains(string(res.Result), "not found") {
		t.Errorf("a $res: argument on a failing store: %s %s, want a failure naming the store's error", res.Status, res.Result)
	}
	if !slices.ContainsFunc(res.Warnings, func(w Warning) bool { return w.Code == code && w.Message == engineMessage }) {
		t.Errorf("warnings %+v, want %s %q among them", res.Warnings, code, engineMessage)
	}
	// A script root warns as a flow does, for its platform call and for an
	// argument the engine resolves; its trace stays nil.
	for _, c := range []struct{ args, message string }{
		{`{"value":1}`, message},
		{`{"value":"$res:f/s/st"}`, engineMessage},
	} {
		res, err := failing.Run(context.Background(), "f/s/write", json.RawMessage(c.args), RunOptions{Kind: KindScript})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusFailure || res.Trace != nil || !slices.ContainsFunc(res.Warnings, func(w Warning) bool { return w.Code == code && w.Message == c.message }) {
			t.Errorf("script root with %s: %s %s, trace %v, warnings %+v; want a failure, no trace and %q", c.args, res.Status, res.Result, res.Trace != nil, res.Warnings, c.message)
		}
	}
}

// TestCloseReleasesItsOwnStore checks that Close releases the memory store
// that Open made, while readers still use it: every read overlapping the
// teardown returns the entry or the store's failure, never a missing entry,
// and every read after it fails. A store that the host gave keeps its
// entries. Run it with -race.
func TestCloseReleasesItsOwnStore(t *testing.T) {
	dir := writeTestBundle(t, Version(), nil, nil)
	own, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	services := own.services
	if err := services.CreateStateResource(ctx, "f/x/kept", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var reads, failures atomic.Int64
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				raw, err := services.RawResource(ctx, "f/x/kept")
				reads.Add(1)
				var pe *platform.Error
				switch {
				case err == nil && string(raw) == "1":
				case errors.As(err, &pe) && pe.StoreFailure:
					failures.Add(1)
				default:
					t.Errorf("a read during Close = %s, %v; want the entry or the store's failure", raw, err)
					return
				}
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	own.Close()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
	if failures.Load() == 0 {
		t.Errorf("no read after Close failed (%d reads): the store was not released", reads.Load())
	}
	if _, _, err := own.ownStore.Get(ctx, platform.DefaultHost.Workspace, "f/x/kept"); !errors.Is(err, platform.ErrStoreReleased) {
		t.Errorf("Get on the released store = %v, want ErrStoreReleased", err)
	}

	given := NewMemoryResourceStore()
	if _, err := given.Create(ctx, "ws", "f/x/kept", Resource{Type: "state", Value: json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(dir, Options{ResourceStore: given})
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()
	if rt.ownStore != nil {
		t.Error("Open marked a store that the host gave as its own")
	}
	if _, ok, err := given.Get(ctx, "ws", "f/x/kept"); err != nil || !ok {
		t.Errorf("Close changed a store that the host gave: %v, %v", ok, err)
	}
}

// TestRunIDInFlight checks that a run whose id a run in flight has is
// refused before it registers anything: the run in flight keeps its call
// log, so a store failure it meets afterwards still warns, and the id is
// free again once that run has returned.
func TestRunIDInFlight(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBunBundle(t, bun, storeFiles)
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ResourceStore: offlineStore{}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	const id = "22222222-2222-4222-8222-222222222222"
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	type outcome struct {
		res *Result
		err error
	}
	first := make(chan outcome, 1)
	go func() {
		res, err := rt.Run(context.Background(), "f/s/read", json.RawMessage(`{}`), RunOptions{ID: id, Hooks: &Hooks{
			OnModuleStart: func(context.Context, RunHandle, Site) error {
				once.Do(func() { close(started) })
				<-release
				return nil
			},
		}})
		first <- outcome{res, err}
	}()
	<-started
	for _, c := range []struct{ path, kind string }{
		{"f/s/none", KindScript}, {"f/s/write", KindScript}, {"f/s/read", KindFlow},
	} {
		if res, err := rt.Run(context.Background(), c.path, json.RawMessage(`{"value":1}`), RunOptions{ID: id, Kind: c.kind}); !errors.Is(err, ErrRunInFlight) {
			t.Errorf("a %s run of %s with the id in flight = %+v, %v; want ErrRunInFlight", c.kind, c.path, res, err)
		}
	}
	close(release)
	o := <-first
	const engineMessage = "engine read: Internal: windmill-lite: the resource store failed: store offline"
	if o.err != nil || o.res.Status != StatusFailure || !slices.ContainsFunc(o.res.Warnings, func(w Warning) bool { return w.Message == engineMessage }) {
		t.Errorf("the run in flight = %+v, %v; want a failure warning %q", o.res, o.err, engineMessage)
	}
	// The id is free again.
	res, err := rt.Run(context.Background(), "f/s/write", json.RawMessage(`{"value":1}`), RunOptions{ID: id, Kind: KindScript})
	if err != nil || res.Status != StatusFailure || len(res.Warnings) == 0 {
		t.Errorf("a run reusing the id after the first ended = %+v, %v; want a failure with its warning", res, err)
	}
}

// TestLateAnswerAcrossIDReuse checks that an answer delivered after its
// run returned does not reach a later run that reuses the id, as a root
// script run again with its id does: the late answer carries its own
// registration's key. The later run's own call still warns.
func TestLateAnswerAcrossIDReuse(t *testing.T) {
	x := &recorder{runs: map[string]*callLog{}, invs: map[string]invocation{}}
	var held atomic.Bool
	arrived, gate, delivered := make(chan struct{}), make(chan struct{}), make(chan struct{})
	fac, err := facade.New(facade.Config{
		Platform: &platform.Services{ResourceStore: offlineStore{}},
		Recorded: x.released,
		Answered: func(inv string, c facade.Record) {
			if held.CompareAndSwap(false, true) {
				// The first run's answer is decided; its delivery waits
				// until the id runs again.
				close(arrived)
				<-gate
				x.answered(inv, c)
				close(delivered)
				return
			}
			x.answered(inv, c)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fac.Close()
	const id = "00000000-0000-4000-8000-0000000000b1"
	// Each run reads its own path, so that a call in a log names the run
	// that made it.
	read := func(inv engine.Invocation, path string) {
		serve := fac.Platform()(harness.PlatformRequest{ID: "p", Inv: inv.ID, Method: "GET", URL: fac.WarmOrigin() + "/api/w/ws/resources/get_value/" + path})
		serve(context.Background())
	}
	run := func(token string, step func(engine.Invocation)) *callLog {
		calls, ok := x.begin(id)
		if !ok {
			t.Fatal("the id is in flight")
		}
		defer x.end(id)
		x.inner = fac.Executor(stepFunc(func(_ context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
			step(inv)
			inv.Report.Settled(engine.SettleCompleted)
			return engine.StepOutcome{Kind: engine.OutcomeOK, Body: jsonval.FromProducer([]byte("null"))}, nil
		}))
		env := map[string]string{"WM_JOB_ID": id, "WM_ROOT_JOB_ID": id, "WM_TOKEN": token, "WM_WORKSPACE": "ws"}
		if _, err := x.Execute(context.Background(), engine.Invocation{ID: id, Run: id, Env: env}); err != nil {
			t.Fatal(err)
		}
		return calls
	}
	// The first run's step reads without awaiting the answer, and ends.
	first := run("token-first", func(inv engine.Invocation) {
		go read(inv, "f/x/first")
		<-arrived
	})
	// The id runs again; its step lets the first run's answer go, checks
	// that its own log is still empty, then makes its own failing read.
	second := run("token-second", func(inv engine.Invocation) {
		close(gate)
		<-delivered
		x.mu.Lock()
		l := x.runs[id]
		x.mu.Unlock()
		if calls, w := l.taken(), l.warned(); len(calls) != 0 || len(w) != 0 {
			t.Errorf("the first run's late answer reached the second run: calls %+v, warnings %+v", calls, w)
		}
		read(inv, "f/x/second")
	})
	if w := first.warned(); len(w) != 0 {
		t.Errorf("the first run, which had returned, got warnings %+v", w)
	}
	calls, w := second.taken(), second.warned()
	if len(calls) != 1 || calls[0].Path != "/api/w/ws/resources/get_value/f/x/second" || len(w) != 1 {
		t.Errorf("the second run's calls %+v and warnings %+v: want its own read of f/x/second and its warning", calls, w)
	}
}

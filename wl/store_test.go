package wl

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/platform"
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
func (offlineStore) UpdateValue(context.Context, string, string, json.RawMessage) (bool, error) {
	return false, errOffline
}
func (offlineStore) Delete(context.Context, string, string) (bool, error) { return false, errOffline }

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
}

// TestCloseReleasesItsOwnStore checks that Close drops the memory store
// that Open made, and keeps a store that the host gave, with its entries.
func TestCloseReleasesItsOwnStore(t *testing.T) {
	dir := writeTestBundle(t, Version(), nil, nil)
	own, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if own.services.ResourceStore == nil {
		t.Fatal("Open made no resource store")
	}
	own.Close()
	if own.services.ResourceStore != nil {
		t.Error("a closed runtime still holds the memory store that Open made")
	}
	given := NewMemoryResourceStore()
	if _, err := given.Create(context.Background(), "ws", "f/x/kept", Resource{Type: "state", Value: json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(dir, Options{ResourceStore: given})
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()
	if rt.services.ResourceStore != given {
		t.Error("Close dropped a store that the host gave")
	}
	if _, ok, _ := given.Get(context.Background(), "ws", "f/x/kept"); !ok {
		t.Error("Close changed a store that the host gave")
	}
}

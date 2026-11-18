package facade

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/platform"
)

const textPlain, textUTF8Type, appJSON = "text/plain", "text/plain; charset=utf-8", "application/json"

type wantReply struct {
	status int
	ctype  string
	body   string
}

// writeFacade is a facade over a memory store, with setup.yaml's resource
// f/wl_state/r of client-api/state as a provider's, and one caller, inv-a,
// of workspace ws.
func writeFacade(t *testing.T, store platform.ResourceStore, answered func(string, Record)) *Facade {
	t.Helper()
	provided := platform.NewStore()
	if err := provided.SetResource("f/wl_state/r", platform.Resource{Type: "wl_state_obj", Value: json.RawMessage(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	f := newFacade(t, Config{Platform: &platform.Services{Resources: provided, ResourceStore: store, Workspace: "ws"}, Answered: answered})
	t.Cleanup(f.Register("inv-a", "run", env("job-a", "run", "tok-a")).End)
	return f
}

func call(f *Facade, method, path string, ctype string, body string) wantReply {
	var headers map[string]string
	if ctype != "" {
		headers = map[string]string{"content-type": ctype}
	}
	var b []byte
	if body != "" || method == "POST" {
		b = []byte(body)
	}
	rep := intercept(f, "inv-a", method, "http://x/api/w/ws/"+path, headers, b)
	return wantReply{rep.Status, rep.Headers["content-type"], string(rep.Body)}
}

// TestStateRoutes plays the requests of client-api/state's run answers
// (WA-13/c to /f) in its order and checks each answer's status, content
// type and body against the recording, the source locations Windmill
// appends to two of them left out (D14).
func TestStateRoutes(t *testing.T) {
	f := writeFacade(t, platform.NewMemoryResourceStore(), nil)
	const j = "application/json"
	p := "f/wl_state/a"
	create := func(body string) wantReply { return call(f, "POST", "resources/create", j, body) }
	update := func(path, body string) wantReply { return call(f, "POST", "resources/update_value/"+path, j, body) }
	updated := wantReply{200, textUTF8Type, "value of resource " + p + " updated"}
	missing := wantReply{404, textPlain, "Not found: Resource " + p + " not found"}
	notJSON := wantReply{415, textUTF8Type, "Expected request with `Content-Type: application/json`"}
	badJSON := wantReply{400, textUTF8Type, "Failed to parse the request body as JSON: EOF while parsing an object at line 1 column 1"}
	for i, c := range []struct {
		name string
		got  func() wantReply
		want wantReply
	}{
		{"exists_missing", func() wantReply { return call(f, "GET", "resources/exists/"+p, "", "") }, wantReply{200, appJSON, "false"}},
		{"get_interpolated_missing", func() wantReply { return call(f, "GET", "resources/get_value_interpolated/"+p, "", "") }, missing},
		{"get_value_missing", func() wantReply { return call(f, "GET", "resources/get_value/"+p, "", "") }, missing},
		{"create", func() wantReply { return create(`{"path":"f/wl_state/a","value":{"n":1},"resource_type":"state"}`) }, wantReply{201, textUTF8Type, "resource " + p + " created"}},
		{"exists", func() wantReply { return call(f, "GET", "resources/exists/"+p, "", "") }, wantReply{200, appJSON, "true"}},
		{"create_taken", func() wantReply { return create(`{"path":"f/wl_state/a","value":{"n":9},"resource_type":"state"}`) }, wantReply{400, textPlain, "Bad request: Resource " + p + " already exists"}},
		{"create_at_other_type", func() wantReply { return create(`{"path":"f/wl_state/r","value":{"n":1},"resource_type":"state"}`) }, wantReply{400, textPlain, "Bad request: Resource f/wl_state/r already exists"}},
		{"other_type_value_after", func() wantReply { return call(f, "GET", "resources/get_value/f/wl_state/r", "", "") }, wantReply{200, appJSON, `{"a":1}`}},
		{"get_interpolated", func() wantReply { return call(f, "GET", "resources/get_value_interpolated/"+p, "", "") }, wantReply{200, appJSON, `{"n":1}`}},
		{"update", func() wantReply { return update(p, `{"value":{"n":2}}`) }, updated},
		{"get_value_updated", func() wantReply { return call(f, "GET", "resources/get_value/"+p, "", "") }, wantReply{200, appJSON, `{"n":2}`}},
		{"update_empty", func() wantReply { return update(p, `{}`) }, updated},
		{"get_value_after_empty", func() wantReply { return call(f, "GET", "resources/get_value/"+p, "", "") }, wantReply{200, appJSON, "null"}},
		{"exists_after_empty", func() wantReply { return call(f, "GET", "resources/exists/"+p, "", "") }, wantReply{200, appJSON, "true"}},
		{"update_null", func() wantReply { return update(p, `{"value":null}`) }, updated},
		{"get_value_after_null", func() wantReply { return call(f, "GET", "resources/get_value/"+p, "", "") }, wantReply{200, appJSON, "null"}},
		{"update_missing", func() wantReply { return update("f/wl_state/none", `{"value":1}`) }, wantReply{404, textPlain, "Not found: Resource f/wl_state/none not found"}},
		{"exists_none", func() wantReply { return call(f, "GET", "resources/exists/f/wl_state/none", "", "") }, wantReply{200, appJSON, "false"}},
		{"delete", func() wantReply { return call(f, "DELETE", "resources/delete/"+p, "", "") }, wantReply{200, textUTF8Type, "resource " + p + " deleted"}},
		{"delete_missing", func() wantReply { return call(f, "DELETE", "resources/delete/"+p, "", "") }, wantReply{404, textPlain, "Not found: Resource not found at name " + p}},
		{"exists_deleted", func() wantReply { return call(f, "GET", "resources/exists/"+p, "", "") }, wantReply{200, appJSON, "false"}},
		{"create_no_value", func() wantReply { return create(`{"path":"f/wl_state/a","resource_type":"state"}`) }, wantReply{201, textUTF8Type, "resource " + p + " created"}},
		{"exists_no_value", func() wantReply { return call(f, "GET", "resources/exists/"+p, "", "") }, wantReply{200, appJSON, "true"}},
		{"get_value_no_value", func() wantReply { return call(f, "GET", "resources/get_value/"+p, "", "") }, wantReply{200, appJSON, "null"}},
		{"delete again", func() wantReply { return call(f, "DELETE", "resources/delete/"+p, "", "") }, wantReply{200, textUTF8Type, "resource " + p + " deleted"}},
		{"create_no_type", func() wantReply { return create(`{"path":"f/wl_state/a","value":1}`) }, wantReply{422, textUTF8Type, "Failed to deserialize the JSON body into the target type: missing field `resource_type` at line 1 column 33"}},
		{"create_no_path", func() wantReply { return create(`{"value":1,"resource_type":"state"}`) }, wantReply{422, textUTF8Type, "Failed to deserialize the JSON body into the target type: missing field `path` at line 1 column 35"}},
		{"create_bad_json", func() wantReply { return create(`{`) }, badJSON},
		{"create_no_content_type", func() wantReply {
			return call(f, "POST", "resources/create", "", `{"path":"f/wl_state/a","value":1,"resource_type":"state"}`)
		}, notJSON},
		{"create_text_plain", func() wantReply {
			return call(f, "POST", "resources/create", "text/plain", `{"path":"f/wl_state/a","value":1,"resource_type":"state"}`)
		}, notJSON},
		{"update_bad_json", func() wantReply { return update(p, `{`) }, badJSON},
		{"update_no_content_type", func() wantReply { return call(f, "POST", "resources/update_value/"+p, "", `{"value":1}`) }, notJSON},
		{"update_text_plain", func() wantReply { return call(f, "POST", "resources/update_value/"+p, "text/plain", `{"value":1}`) }, notJSON},
		{"exists_end", func() wantReply { return call(f, "GET", "resources/exists/"+p, "", "") }, wantReply{200, appJSON, "false"}},
	} {
		if got := c.got(); got != c.want {
			t.Errorf("#%d %s: %d %q %q, want %d %q %q", i, c.name, got.status, got.ctype, got.body, c.want.status, c.want.ctype, c.want.body)
		}
	}
}

// TestStateValuesAndPaths checks the recorded answers of client-api/state's
// runs values and paths (WA-13/h to /j): jsonb's refusals, the stored form
// of each route, interpolation on read, and which paths a create accepts.
func TestStateValuesAndPaths(t *testing.T) {
	f := writeFacade(t, platform.NewMemoryResourceStore(), nil)
	const j = "application/json"
	create := func(body string) wantReply { return call(f, "POST", "resources/create", j, body) }
	get := func(route, p string) wantReply { return call(f, "GET", "resources/"+route+"/"+p, "", "") }
	check := func(name string, got, want wantReply) {
		t.Helper()
		if got != want {
			t.Errorf("%s: %d %q %q, want %d %q %q", name, got.status, got.ctype, got.body, want.status, want.ctype, want.body)
		}
	}
	nul := wantReply{400, textPlain, "SqlErr: error returned from database: unsupported Unicode escape sequence"}
	check("create_nul", create(`{"path":"f/wl_state/nul","value":{"s":"a\u0000b"},"resource_type":"state"}`), nul)
	check("exists_nul", get("exists", "f/wl_state/nul"), wantReply{200, appJSON, "false"})
	check("create_surrogate", create(`{"path":"f/wl_state/nul","value":{"s":"\ud800"},"resource_type":"state"}`), wantReply{400, textPlain, "SqlErr: error returned from database: invalid input syntax for type json"})
	check("exists_surrogate", get("exists", "f/wl_state/nul"), wantReply{200, appJSON, "false"})
	create(`{"path":"f/wl_state/nul","value":{"s":"ok"},"resource_type":"state"}`)
	check("update_nul", call(f, "POST", "resources/update_value/f/wl_state/nul", j, `{"value":{"s":"a\u0000b"}}`), nul)
	check("after_update_nul", get("get_value", "f/wl_state/nul"), wantReply{200, appJSON, `{"s":"ok"}`})

	odd := `{"b":1,"a":1.0,"ab":1e3,"c":{"y":[1.50,"\u0041",-0,1E-2],"x":null},"":true}`
	check("create_stored", create(`{"path":"f/wl_state/stored","value":`+odd+`,"resource_type":"state"}`), wantReply{201, textUTF8Type, "resource f/wl_state/stored created"})
	created := wantReply{200, appJSON, `{"":true,"a":1.0,"b":1,"c":{"x":null,"y":[1.5,"A",0,0.01]},"ab":1000}`}
	check("stored get_value", get("get_value", "f/wl_state/stored"), created)
	check("stored get_interpolated", get("get_value_interpolated", "f/wl_state/stored"), created)
	check("update_stored", call(f, "POST", "resources/update_value/f/wl_state/stored", j, `{"value":`+odd+`}`), wantReply{200, textUTF8Type, "value of resource f/wl_state/stored updated"})
	check("stored_updated get_value", get("get_value", "f/wl_state/stored"), wantReply{200, appJSON, `{"":true,"a":1.0,"b":1,"c":{"x":null,"y":[1.5,"A",0.0,0.01]},"ab":1000.0}`})

	// An exponent at the integer limit is a number like any other, not a
	// handler's panic (a 502).
	check("create_min_exponent", create(`{"path":"f/wl_state/tiny","value":1e-9223372036854775808,"resource_type":"state"}`), wantReply{201, textUTF8Type, "resource f/wl_state/tiny created"})
	check("tiny get_value", get("get_value", "f/wl_state/tiny"), wantReply{200, appJSON, "0.0"})

	invalid := wantReply{400, textPlain, "Bad request: Invalid path: it must be of the form u/<user>/<name>, f/<folder>/<name> or g/<group>/<name>, where every segment contains only alphanumeric characters, '_' or '-'"}
	for path, ok := range map[string]bool{
		"x/wl_state/bad": false, "wl_state_bad": false, "f/wl_state": false, "f/wl_state/slash/": false,
		"f/wl_state/a.b": false, "f/wl_state/a b": false, "f/wl_state_nofolder/x": true, "u/someone/wl_state_own": true,
		"u/wl_state_nobody/x": true, "g/wl_state_group/x": true, "f/wl_state/deep/er/path": true, "f/wl_state/a-b_C9": true,
		"f/wl_state/ü": true,
	} {
		body, _ := json.Marshal(map[string]any{"path": path, "value": map[string]string{"k": "v"}, "resource_type": "state"})
		want := invalid
		if ok {
			want = wantReply{201, textUTF8Type, "resource " + path + " created"}
		}
		check("create at "+path, create(string(body)), want)
	}
}

// TestStateWriteRefusals checks the writes windmill-lite does not make: a
// create of another type or with an option the stock client never sends,
// and an update or delete of an entry of another type, each with the 501
// they answer today; an update or delete of a provider's resource, with
// the restriction's 501 (ADR 0001, section 5a); bodies that are not
// objects; and the framework's 422 on a multi-line body.
func TestStateWriteRefusals(t *testing.T) {
	store := platform.NewMemoryResourceStore()
	f := writeFacade(t, store, nil)
	const j = "application/json"
	createRoute := wantReply{501, textPlain, UnsupportedPrefix + "POST /w/{ws}/resources/create"}
	for name, body := range map[string]string{
		"another type":          `{"path":"f/x/a","value":1,"resource_type":"wl_obj"}`,
		"an unknown type":       `{"path":"f/x/a","value":1,"resource_type":"wl_none"}`,
		"description":           `{"path":"f/x/a","value":1,"resource_type":"state","description":"d"}`,
		"labels":                `{"path":"f/x/a","value":1,"resource_type":"state","labels":["l"]}`,
		"ws_specific":           `{"path":"f/x/a","value":1,"resource_type":"state","ws_specific":true}`,
		"preserve_on_behalf_of": `{"path":"f/x/a","value":1,"resource_type":"state","preserve_on_behalf_of":true}`,
	} {
		if got := call(f, "POST", "resources/create", j, body); got != createRoute {
			t.Errorf("create with %s: %+v, want %+v", name, got, createRoute)
		}
	}
	if got := call(f, "POST", "resources/create?update_if_exists=true", j, `{"path":"f/x/a","value":1,"resource_type":"state"}`); got != createRoute {
		t.Errorf("create with update_if_exists: %+v, want %+v", got, createRoute)
	}
	if _, ok, _ := store.Get(context.Background(), "ws", "f/x/a"); ok {
		t.Error("a refused create stored an entry")
	}

	if _, err := store.Create(context.Background(), "ws", "f/x/obj", platform.Resource{Type: "wl_obj", Value: json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, route, path, body string }{
		{"POST", "POST /w/{ws}/resources/update_value/{path}", "resources/update_value/", `{"value":2}`},
		{"DELETE", "DELETE /w/{ws}/resources/delete/{path}", "resources/delete/", ""},
	} {
		ctype := ""
		if c.body != "" {
			ctype = j
		}
		if got, want := call(f, c.method, c.path+"f/x/obj", ctype, c.body), (wantReply{501, textPlain, UnsupportedPrefix + c.route}); got != want {
			t.Errorf("%s of another type: %+v, want %+v", c.method, got, want)
		}
		want := wantReply{501, textPlain, UnsupportedPrefix + c.route + ": the resource is provided by the host or the bundle, which windmill-lite does not write"}
		if got := call(f, c.method, c.path+"f/wl_state/r", ctype, c.body); got != want {
			t.Errorf("%s of a provider's resource: %+v, want %+v", c.method, got, want)
		}
	}

	multi := "{\n  \"value\": 1,\n  \"resource_type\": \"state\"\n}\n"
	want := wantReply{422, textUTF8Type, "Failed to deserialize the JSON body into the target type: missing field `path` at line 4 column 1"}
	if got := call(f, "POST", "resources/create", j, multi); got != want {
		t.Errorf("a multi-line body: %+v, want %+v", got, want)
	}
	for _, body := range []string{`[1]`, `"x"`, `{"path":1,"resource_type":"state"}`,
		`{"path":null,"value":1,"resource_type":"state"}`, `{"path":"f/x/n","value":1,"resource_type":null}`} {
		if got := call(f, "POST", "resources/create", j, body); got.status != 422 || !strings.HasPrefix(got.body, "Failed to deserialize the JSON body into the target type: ") {
			t.Errorf("create %s: %+v, want a 422", body, got)
		}
	}
	// A null value is a value: it is stored.
	if got := call(f, "POST", "resources/create", j, `{"path":"f/x/nullvalue","value":null,"resource_type":"state"}`); got.status != 201 {
		t.Errorf("create with a null value: %+v, want a 201", got)
	}
	if got := call(f, "POST", "resources/update_value/f/x/obj", j, `[1]`); got.status != 422 {
		t.Errorf("update with an array body: %+v, want a 422", got)
	}
}

// TestStateWritesWithoutAStore checks that services with no resource store
// answer every write 501, as before the store.
func TestStateWritesWithoutAStore(t *testing.T) {
	f := writeFacade(t, nil, nil)
	for _, c := range []struct{ method, path, route string }{
		{"POST", "resources/create", "POST /w/{ws}/resources/create"},
		{"POST", "resources/update_value/f/x/a", "POST /w/{ws}/resources/update_value/{path}"},
		{"DELETE", "resources/delete/f/x/a", "DELETE /w/{ws}/resources/delete/{path}"},
	} {
		if got, want := call(f, c.method, c.path, "application/json", `{}`), (wantReply{501, textPlain, UnsupportedPrefix + c.route}); got != want {
			t.Errorf("%s %s: %+v, want %+v", c.method, c.path, got, want)
		}
	}
}

type brokenStore struct{}

func (brokenStore) Get(context.Context, string, string) (platform.Resource, bool, error) {
	return platform.Resource{}, false, errors.New("i/o timeout")
}
func (brokenStore) Create(context.Context, string, string, platform.Resource) (bool, error) {
	return false, errors.New("i/o timeout")
}
func (brokenStore) UpdateValue(context.Context, string, string, string, json.RawMessage) (bool, error) {
	return false, errors.New("i/o timeout")
}
func (brokenStore) Delete(context.Context, string, string, string) (bool, error) {
	return false, errors.New("i/o timeout")
}

// TestStoreFailureAnswers checks that a failing store answers every read
// and write of a state 500 `Internal: windmill-lite: the resource store
// failed: <err>`, which the stock client's getState throws on, never a
// 404 it would read as a state never written; and that each such call's
// record carries the failure as a warning for the run's trace.
func TestStoreFailureAnswers(t *testing.T) {
	var mu sync.Mutex
	var warned []string
	f := writeFacade(t, brokenStore{}, func(_ string, r Record) {
		mu.Lock()
		defer mu.Unlock()
		if r.Warning != "" {
			warned = append(warned, r.Warning)
		}
	})
	want := wantReply{500, textPlain, "Internal: windmill-lite: the resource store failed: i/o timeout"}
	reqs := []struct{ method, path, body string }{
		{"GET", "resources/get_value_interpolated/f/x/s", ""},
		{"GET", "resources/get_value/f/x/s", ""},
		{"GET", "resources/exists/f/x/s", ""},
		{"POST", "resources/create", `{"path":"f/x/s","value":1,"resource_type":"state"}`},
		{"POST", "resources/update_value/f/x/s", `{"value":1}`},
		{"DELETE", "resources/delete/f/x/s", ""},
	}
	for _, r := range reqs {
		ctype := ""
		if r.body != "" {
			ctype = "application/json"
		}
		if got := call(f, r.method, r.path, ctype, r.body); got != want {
			t.Errorf("%s %s: %+v, want %+v", r.method, r.path, got, want)
		}
	}
	// A provider's resource is read without the store.
	if got := call(f, "GET", "resources/get_value/f/wl_state/r", "", ""); got.status != 200 {
		t.Errorf("a provider's resource with a failing store: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(warned) != len(reqs) || warned[0] != want.body {
		t.Errorf("warnings %q, want %d of %q", warned, len(reqs), want.body)
	}
}

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// The stored form of WA-13/i: state run values of
// conformance/flows/client-api/state sends this text through create and
// through update_value, and reads back the recorded spellings.
const sentNumbers = `{"b":1,"a":1.0,"ab":1e3,"c":{"y":[1.50,"A",-0,1E-2],"x":null},"":true}`

// TestStoredValueSpellsRecordedNumbers checks both routes' number rules on
// every spelling the recordings hold (state values: stored and
// stored_updated).
func TestStoredValueSpellsRecordedNumbers(t *testing.T) {
	for _, c := range []struct {
		create bool
		want   string
	}{
		{true, `{"":true,"a":1.0,"b":1,"c":{"x":null,"y":[1.5,"A",0,0.01]},"ab":1000}`},
		{false, `{"":true,"a":1.0,"b":1,"c":{"x":null,"y":[1.5,"A",0.0,0.01]},"ab":1000.0}`},
	} {
		got, e := storedValue(json.RawMessage(sentNumbers), c.create)
		if e != nil || string(got) != c.want {
			t.Errorf("create %v: storedValue = %s, %v; want %s", c.create, got, e, c.want)
		}
	}
}

// TestRespellNumber checks each recorded number on each route, and the
// numbers the rules spell that no recording holds.
func TestRespellNumber(t *testing.T) {
	for _, c := range []struct{ in, create, update string }{
		// Recorded (state values, counter, pair, answers).
		{"1", "1", "1"},
		{"2", "2", "2"},
		{"1.0", "1.0", "1.0"},
		{"1e3", "1000", "1000.0"},
		{"1.50", "1.5", "1.5"},
		{"-0", "0", "0.0"},
		{"1E-2", "0.01", "0.01"},
		// Not recorded: the rules' consequences.
		{"-7", "-7", "-7"},
		{"1e16", "10000000000000000", "10000000000000000"},
		{"1e15", "1000000000000000", "1000000000000000.0"},
		{"2.5e-7", "0.00000025", "0.00000025"},
		{"12e-1", "1.2", "1.2"},
		{"120e-1", "12", "12.0"},
		{"-0.0", "0.0", "0.0"},
		{"18446744073709551616", "18446744073709551616", "18446744073709552000"},
	} {
		if got := respellNumber(c.in, true); got != c.create {
			t.Errorf("create %s = %s, want %s", c.in, got, c.create)
		}
		if got := respellNumber(c.in, false); got != c.update {
			t.Errorf("update_value %s = %s, want %s", c.in, got, c.update)
		}
	}
}

// TestRespellNumbersLeavesStrings checks that digits inside strings, keys
// and escapes are not numbers.
func TestRespellNumbersLeavesStrings(t *testing.T) {
	in := `{"1e3":"1e3 \"2e2\" \\","n":[1e3,-0]}`
	want := `{"1e3":"1e3 \"2e2\" \\","n":[1000,0]}`
	if got := string(respellNumbers([]byte(in), true)); got != want {
		t.Errorf("respellNumbers = %s, want %s", got, want)
	}
}

// TestValidResourcePath checks every path state paths records (WA-13/j),
// then the cases the chosen rule decides.
func TestValidResourcePath(t *testing.T) {
	for path, want := range map[string]bool{
		// Recorded refusals.
		"x/wl_state/bad": false, "wl_state_bad": false, "f/wl_state": false,
		"f/wl_state/slash/": false, "f/wl_state/a.b": false, "f/wl_state/a b": false,
		// Recorded acceptances.
		"f/wl_state_nofolder/x": true, "u/admin/wl_state_own": true, "u/wl_state_nobody/x": true,
		"g/wl_state_group/x": true, "f/wl_state/deep/er/path": true, "f/wl_state/a-b_C9": true,
		"f/wl_state/ü": true,
		// The chosen rule, not recorded.
		"f/a//b": false, "/f/a/b": false, "f/a/٣": false, "f/a/日本": true, "F/a/b": false, "f/a/b:c": false,
	} {
		if got := validResourcePath(path); got != want {
			t.Errorf("validResourcePath(%q) = %v, want %v", path, got, want)
		}
	}
}

// failingStore fails every operation.
type failingStore struct{}

var errOutage = errors.New("connection refused")

func (failingStore) Get(context.Context, string, string) (Resource, bool, error) {
	return Resource{}, false, errOutage
}
func (failingStore) Create(context.Context, string, string, Resource) (bool, error) {
	return false, errOutage
}
func (failingStore) UpdateValue(context.Context, string, string, json.RawMessage) (bool, error) {
	return false, errOutage
}
func (failingStore) Delete(context.Context, string, string) (bool, error) { return false, errOutage }

func wantError(t *testing.T, what string, err error, status int, body string) {
	t.Helper()
	var pe *Error
	if !errors.As(err, &pe) || pe.HTTPStatus() != status || pe.Body() != body {
		t.Errorf("%s = %v, want %d %q", what, err, status, body)
	}
}

// TestStateWrites checks the write rules over a memory store: the recorded
// answers of create, update_value and delete (WA-13/d to /f), a provider's
// path, an entry of another type, and the stored values.
func TestStateWrites(t *testing.T) {
	ctx := context.Background()
	provided := NewStore()
	if err := provided.SetResource("f/wl_state/r", Resource{Type: "wl_obj", Value: json.RawMessage(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryResourceStore()
	s := &Services{Resources: provided, ResourceStore: store}

	if err := s.CreateStateResource(ctx, "f/wl_state/a", json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatalf("create: %v", err)
	}
	wantError(t, "create at a taken path", s.CreateStateResource(ctx, "f/wl_state/a", json.RawMessage(`{"n":9}`)), 400, "Bad request: Resource f/wl_state/a already exists")
	wantError(t, "create at a provider's path", s.CreateStateResource(ctx, "f/wl_state/r", json.RawMessage(`{"n":1}`)), 400, "Bad request: Resource f/wl_state/r already exists")
	wantError(t, "create at a bad path", s.CreateStateResource(ctx, "x/wl_state/bad", nil), 400, "Bad request: "+invalidPath)
	wantError(t, "create with a NUL", s.CreateStateResource(ctx, "f/wl_state/nul", json.RawMessage(`{"s":"a\u0000b"}`)), 400, "SqlErr: error returned from database: unsupported Unicode escape sequence")
	wantError(t, "create with a lone surrogate", s.CreateStateResource(ctx, "f/wl_state/nul", json.RawMessage(`{"s":"\ud800"}`)), 400, "SqlErr: error returned from database: invalid input syntax for type json")
	if _, ok, _ := store.Get(ctx, DefaultHost.Workspace, "f/wl_state/nul"); ok {
		t.Error("a refused create stored an entry")
	}
	if raw, err := s.RawResource(ctx, "f/wl_state/a"); err != nil || string(raw) != `{"n":1}` {
		t.Errorf("read after create = %s, %v", raw, err)
	}

	if err := s.UpdateStateResource(ctx, "f/wl_state/a", json.RawMessage(`{"n":2}`)); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.UpdateStateResource(ctx, "f/wl_state/a", nil); err != nil {
		t.Fatalf("update without value: %v", err)
	}
	if raw, _ := s.RawResource(ctx, "f/wl_state/a"); string(raw) != "null" {
		t.Errorf("read after an update without value = %s, want null", raw)
	}
	wantError(t, "update of a missing path", s.UpdateStateResource(ctx, "f/wl_state/none", json.RawMessage(`1`)), 404, "Not found: Resource f/wl_state/none not found")
	if ok, _ := s.ResourceExists(ctx, "f/wl_state/none"); ok {
		t.Error("an update of a missing path created it")
	}
	if err := s.UpdateStateResource(ctx, "f/wl_state/r", json.RawMessage(`1`)); !errors.Is(err, ErrProvidedResource) {
		t.Errorf("update of a provider's resource = %v, want ErrProvidedResource", err)
	}
	if err := s.DeleteStateResource(ctx, "f/wl_state/r"); !errors.Is(err, ErrProvidedResource) {
		t.Errorf("delete of a provider's resource = %v, want ErrProvidedResource", err)
	}
	if _, err := store.Create(ctx, DefaultHost.Workspace, "f/wl_state/other", Resource{Type: "wl_obj", Value: json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStateResource(ctx, "f/wl_state/other", json.RawMessage(`2`)); !errors.Is(err, ErrNotStateResource) {
		t.Errorf("update of an entry of another type = %v, want ErrNotStateResource", err)
	}
	if err := s.DeleteStateResource(ctx, "f/wl_state/other"); !errors.Is(err, ErrNotStateResource) {
		t.Errorf("delete of an entry of another type = %v, want ErrNotStateResource", err)
	}

	if err := s.DeleteStateResource(ctx, "f/wl_state/a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	wantError(t, "delete of a missing path", s.DeleteStateResource(ctx, "f/wl_state/a"), 404, "Not found: Resource not found at name f/wl_state/a")
	if err := s.CreateStateResource(ctx, "f/wl_state/a", nil); err != nil {
		t.Fatalf("create without value: %v", err)
	}
	if raw, _ := s.RawResource(ctx, "f/wl_state/a"); string(raw) != "null" {
		t.Errorf("read after a create without value = %s, want null", raw)
	}
}

// TestStoreReadOrder checks that a provider's resource shadows a stored
// entry at the same path, that stored entries are read through every read
// route, and that the read cache (D21) does not keep them.
func TestStoreReadOrder(t *testing.T) {
	ctx := context.Background()
	provided := NewStore()
	if err := provided.SetResource("f/x/p", Resource{Type: "obj", Value: json.RawMessage(`"provided"`)}); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryResourceStore()
	for _, p := range []string{"f/x/p", "f/x/s"} {
		if _, err := store.Create(ctx, DefaultHost.Workspace, p, Resource{Type: StateType, Value: json.RawMessage(`"stored"`)}); err != nil {
			t.Fatal(err)
		}
	}
	cache := NewReadCache(provided, nil)
	s := &Services{Resources: cache, ResourceStore: store}
	for path, want := range map[string]string{"f/x/p": `"provided"`, "f/x/s": `"stored"`} {
		if raw, err := s.RawResource(ctx, path); err != nil || string(raw) != want {
			t.Errorf("get_value %s = %s, %v; want %s", path, raw, err, want)
		}
		if raw, err := s.InterpolatedResource(ctx, path, nil); err != nil || string(raw) != want {
			t.Errorf("get_value_interpolated %s = %s, %v; want %s", path, raw, err, want)
		}
	}
	if _, err := store.UpdateValue(ctx, DefaultHost.Workspace, "f/x/s", json.RawMessage(`"changed"`)); err != nil {
		t.Fatal(err)
	}
	if raw, _ := s.RawResource(ctx, "f/x/s"); string(raw) != `"changed"` {
		t.Errorf("a stored entry read again = %s, want the new value: the store is never cached", raw)
	}
	if _, err := store.Delete(ctx, DefaultHost.Workspace, "f/x/s"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ResourceExists(ctx, "f/x/s"); ok {
		t.Error("exists after a delete in the store is true")
	}
	other := &Services{ResourceStore: store, Workspace: "another"}
	if ok, _ := other.ResourceExists(ctx, "f/x/p"); ok {
		t.Error("services of another workspace read this workspace's entry")
	}
}

// TestStoreFailure checks that a failing store fails every read and write
// with 500 `Internal: windmill-lite: the resource store failed: <err>`,
// marked as a store failure, and is never read as a missing entry.
func TestStoreFailure(t *testing.T) {
	ctx := context.Background()
	s := &Services{ResourceStore: failingStore{}}
	body := "Internal: windmill-lite: the resource store failed: connection refused"
	check := func(what string, err error) {
		t.Helper()
		var pe *Error
		if !errors.As(err, &pe) || pe.HTTPStatus() != 500 || pe.Body() != body || !pe.StoreFailure {
			t.Errorf("%s = %#v, want a store failure, 500 %q", what, err, body)
		}
	}
	_, err := s.RawResource(ctx, "f/x/s")
	check("get_value", err)
	_, err = s.InterpolatedResource(ctx, "f/x/s", nil)
	check("get_value_interpolated", err)
	_, err = s.ResourceExists(ctx, "f/x/s")
	check("exists", err)
	check("create", s.CreateStateResource(ctx, "f/x/s", json.RawMessage(`1`)))
	check("update_value", s.UpdateStateResource(ctx, "f/x/s", json.RawMessage(`1`)))
	check("delete", s.DeleteStateResource(ctx, "f/x/s"))
}

package wl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// The tests without Bun open bundles written here by hand: a program of
// identity modules, which runs no step, its data/ and a manifest in
// canonical form. bun_test.go builds real bundles and runs their steps.

// testFlows are synthetic flows, each a flow value as JSON. f/t/which
// returns "host" when the variable u/t/v reads "host", and "bundle"
// otherwise (ADR 0007: a host's variable replaces the bundle's).
var testFlows = map[string]string{
	"f/t/echo": `{"modules":[{"id":"a","value":{"type":"identity"}}]}`,
	"f/t/which": `{"modules":[{"id":"b","value":{"type":"branchone","branches":[{"expr":"variable(\"u/t/v\") === \"host\"","modules":[` +
		`{"id":"h","value":{"type":"identity"},"mock":{"enabled":true,"return_value":"host"}}]}],` +
		`"default":[{"id":"d","value":{"type":"identity"},"mock":{"enabled":true,"return_value":"bundle"}}]}}]}`,
}

// writeTestBundle writes a bundle of testFlows below a new directory, with
// variables in its data/, built by engine version, and returns it.
func writeTestBundle(t *testing.T, version string, variables map[string]string, edit func(m *bundle.Manifest)) string {
	t.Helper()
	fsys := fstest.MapFS{"wmill.yaml": {Data: []byte("defaultTs: bun\n")}}
	for p, v := range testFlows {
		fsys[p+".flow/flow.yaml"] = &fstest.MapFile{Data: []byte(`{"summary":"t","value":` + v + `}`)}
	}
	folder, err := syncfolder.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	prog, err := ir.Compile(folder, nil, ir.Options{})
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
		Deviations:      []string{"D3", "D14"},
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
		!slices.Equal(bi.Deviations, []string{"D3", "D14"}) || !strings.Contains(string(bi.Contract), "f/t/which") {
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

	// A module-state override is in effect.
	rt, err = Open(dir, Options{ModuleState: ModuleStateFresh})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.BuildInfo().ModuleState != ModuleStateFresh {
		t.Fatalf("module state %s", rt.BuildInfo().ModuleState)
	}
}

// A .wlb archive is extracted into the data directory, which a given
// DataDir keeps after Close.
func TestOpenArchive(t *testing.T) {
	dir := writeTestBundle(t, Version(), nil, nil)
	archive := filepath.Join(t.TempDir(), "b"+bundle.ArchiveExt)
	if err := bundle.WriteArchiveFile(dir, archive); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "data")
	rt, err := Open(archive, Options{DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := rt.Run(context.Background(), "f/t/echo", nil, RunOptions{}); err != nil || res.Status != StatusSuccess || string(res.Result) != `{}` {
		t.Fatalf("%+v %v", res, err)
	}
	rt.Close()
	if _, err := os.Stat(filepath.Join(data, "bundle", "ir.json")); err != nil {
		t.Fatalf("the extracted bundle: %v", err)
	}
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

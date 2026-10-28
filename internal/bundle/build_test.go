package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/ir"
)

// The fixture of the build tests: testdata/fixture holds a synthetic sync
// folder (windmill/) and its host contract (contract.yaml).
const fixtureDir = "testdata/fixture"

// fixtureEngine is the engine version the build tests record.
const fixtureEngine = "v0.0.0-test"

// copyTree copies the directory src to dst.
func copyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// fixtureConfig is a build of a copy of the fixture: in its own directory,
// with its own work and output directories.
func fixtureConfig(t testing.TB, bun string) (Config, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "copy")
	copyTree(t, fixtureDir, dir)
	contract, err := os.ReadFile(filepath.Join(dir, "contract.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Folder:         filepath.Join(dir, "windmill"),
		Contract:       contract,
		EngineVersion:  fixtureEngine,
		Bun:            bun,
		Helper:         repoPath(t, "harnesses/bun/build/main.ts"),
		HarnessRuntime: repoPath(t, "harnesses/bun/runtime"),
		Out:            filepath.Join(t.TempDir(), "bundle"),
		Work:           t.TempDir(),
	}, dir
}

func build(t testing.TB, cfg Config) *Result {
	t.Helper()
	res, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return res
}

// TestBuildFixture builds the fixture and checks the bundle: its files,
// its manifest, and that its modules run.
func TestBuildFixture(t *testing.T) {
	bun := bunPath(t)
	cfg, _ := fixtureConfig(t, bun)
	res := build(t, cfg)
	m := res.Manifest

	inline := ""
	for id, s := range res.Program.Scripts {
		if s.Kind == ir.InlineScript {
			inline = id
		}
	}
	wantFiles := []string{
		"data/resource_types.json", "data/resources.json", "data/variables.json",
		"ir.json",
		"scripts/bun/f/app/report.meta.json", "scripts/bun/f/app/report.mjs",
		"scripts/bun/" + inline + ".meta.json", "scripts/bun/" + inline + ".mjs",
		"scripts/bun/provided/f/shared/ctx.mjs", "scripts/bun/provided/shims/f/shared/ctx.mjs",
	}
	slices.Sort(wantFiles)
	if got := sortedKeys(m.Files); !slices.Equal(got, wantFiles) {
		t.Errorf("files %q, want %q", got, wantFiles)
	}
	// The sources: what the compiler read, the scripts the bundles inline
	// and the host-provided module. Not the folder's other files, and not
	// the secret's.
	wantSources := []string{
		"c_settings.resource-type.yaml", "f/app/format.ts", "f/app/main.flow/flow.yaml",
		"f/app/main.flow/greet.ts", "f/app/region.variable.yaml", "f/app/report.ts",
		"f/app/settings.resource.yaml", "f/shared/ctx.ts", "wmill.yaml",
	}
	if got := sortedKeys(m.Sources); !slices.Equal(got, wantSources) {
		t.Errorf("sources %q, want %q", got, wantSources)
	}
	ctx, err := os.ReadFile(filepath.Join(fixtureDir, "windmill/f/shared/ctx.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Sources["f/shared/ctx.ts"] != Digest(ctx) {
		t.Errorf("source digest of f/shared/ctx.ts is %s, want %s", m.Sources["f/shared/ctx.ts"], Digest(ctx))
	}
	if m.Format != FormatVersion || m.Engine.Version != fixtureEngine || m.Engine.ExpressionStackBytes != 1<<20 {
		t.Errorf("format and engine: %d, %+v", m.Format, m.Engine)
	}
	if m.Bundlers.Bun != harness.PinnedBun || m.NonconformantRuntime {
		t.Errorf("bundlers %+v, nonconformant %v", m.Bundlers, m.NonconformantRuntime)
	}
	if m.ModuleState != engine.ModuleStateWarm {
		t.Errorf("module state %q", m.ModuleState)
	}
	if want := []string{"D1", "D2", "D3", "D8", "D9", "D10", "D11", "D12", "D14", "D16", "D17"}; !slices.Equal(m.Deviations, want) {
		t.Errorf("deviations %q, want %q", m.Deviations, want)
	}
	if want := []HostModuleEntry{{Key: "f/shared/ctx.ts", ID: "f/shared/ctx"}}; !slices.Equal(m.HostModules, want) {
		t.Errorf("host modules %+v, want %+v", m.HostModules, want)
	}
	if want, _ := TreeDigest(cfg.HarnessRuntime); m.HarnessRuntimes["bun"] != want || len(m.HarnessRuntimes) != 1 {
		t.Errorf("harness runtimes %v, want bun %s", m.HarnessRuntimes, want)
	}
	if len(m.Contract.Roots) != 2 || !slices.Equal(m.Contract.HostModules, []string{"f/shared/ctx.ts"}) {
		t.Errorf("contract %+v", m.Contract)
	}

	// The manifest on disk is its canonical form, and its digest is the
	// bundle digest; every digest checks out.
	data, err := os.ReadFile(filepath.Join(cfg.Out, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if canon, _ := m.Canonical(); !bytes.Equal(canon, data) || Digest(data) != res.Digest {
		t.Errorf("manifest.json is not the manifest's canonical form, or its digest is not %s", res.Digest)
	}
	if v, err := Verify(cfg.Out, VerifyOptions{Digest: res.Digest}); err != nil || v.Digest != res.Digest {
		t.Errorf("Verify = %v, %v", v, err)
	}

	// data/ holds the resource, the resource type and the non-secret
	// variable; never the secret.
	vars, err := os.ReadFile(filepath.Join(cfg.Out, "data", "variables.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vars), "eu-synthetic-1") || strings.Contains(string(vars), "f/app/token") ||
		strings.Contains(string(vars), "synthetic-not-a-secret-value") {
		t.Errorf("data/variables.json:\n%s", vars)
	}
	for _, f := range []string{"resources.json", "resource_types.json"} {
		b, err := os.ReadFile(filepath.Join(cfg.Out, "data", f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "c_settings") {
			t.Errorf("data/%s:\n%s", f, b)
		}
	}

	// ir.json is the program.
	irJSON, err := os.ReadFile(filepath.Join(cfg.Out, "ir.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := res.Program.MarshalIndent(); !bytes.Equal(irJSON, want) {
		t.Error("ir.json is not the compiled program")
	}

	// The modules run, sharing the host-provided module's one instance: the
	// inline step stores the name that the report reads.
	scripts := filepath.Join(cfg.Out, "scripts", "bun")
	results := runModules(t, bun, map[string]string{},
		[][2]string{{"f/shared/ctx.ts", filepath.Join(scripts, "provided", "f", "shared", "ctx.mjs")}},
		[]call{
			{Module: filepath.Join(scripts, inline+".mjs"), Export: "main", Args: []any{"fixture"}},
			{Module: filepath.Join(scripts, "f", "app", "report.mjs"), Export: "main", Args: []any{2, "2026-01-01T00:00:00Z"}},
		})
	want := []string{`"[hello, fixture]"`, `{"text":"[fixture]","count":2,"when":"2026-01-01T00:00:00Z"}`}
	for i, r := range results {
		if r.Error != nil {
			t.Errorf("call %d: %s", i, *r.Error)
			continue
		}
		if !jsonEqual(t, r.Result, json.RawMessage(want[i])) {
			t.Errorf("call %d: %s, want %s", i, r.Result, want[i])
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestBuildTwiceGivesOneDigest is F4's exit gate (ADR 0012): two builds of
// one fixture, from copies of it in different directories, with different
// work and output directories, give one bundle digest, the same manifest
// and the same .wlb archive, byte for byte.
func TestBuildTwiceGivesOneDigest(t *testing.T) {
	bun := bunPath(t)
	var digests []string
	var manifests, archives [][]byte
	for range 2 {
		cfg, _ := fixtureConfig(t, bun)
		res := build(t, cfg)
		m, err := os.ReadFile(filepath.Join(cfg.Out, ManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		wlb := filepath.Join(t.TempDir(), "fixture"+ArchiveExt)
		if err := WriteArchiveFile(cfg.Out, wlb); err != nil {
			t.Fatal(err)
		}
		a, err := os.ReadFile(wlb)
		if err != nil {
			t.Fatal(err)
		}
		if v, err := Verify(wlb, VerifyOptions{Release: m}); err != nil || v.Digest != res.Digest {
			t.Fatalf("Verify(archive) = %v, %v", v, err)
		}
		digests, manifests, archives = append(digests, res.Digest), append(manifests, m), append(archives, a)
	}
	if digests[0] != digests[1] {
		t.Errorf("two builds give the digests %s and %s", digests[0], digests[1])
	}
	if !bytes.Equal(manifests[0], manifests[1]) {
		t.Errorf("two builds give different manifests:\n%s\n%s", manifests[0], manifests[1])
	}
	if !bytes.Equal(archives[0], archives[1]) {
		t.Error("two builds give different archives")
	}
	t.Logf("bundle digest %s", digests[0])
}

// TestDigestFollowsInputs checks that every input the manifest names
// changes the bundle digest: a flow, a script the bundles inline, the
// host-provided module, a lock, data, the contract, the module-state mode,
// the engine version and the harness runtime. A file the build does not
// read leaves it alone.
func TestDigestFollowsInputs(t *testing.T) {
	bun := bunPath(t)
	base, _ := fixtureConfig(t, bun)
	want := build(t, base).Digest
	edit := func(rel, old, new string) func(*Config, string) {
		return func(_ *Config, dir string) {
			p := filepath.Join(dir, "windmill", filepath.FromSlash(rel))
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if old == "" {
				b = append(b, new...)
			} else if !bytes.Contains(b, []byte(old)) {
				t.Fatalf("%s does not contain %q", rel, old)
			} else {
				b = bytes.Replace(b, []byte(old), []byte(new), 1)
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write := func(rel, text string) func(*Config, string) {
		return func(_ *Config, dir string) {
			p := filepath.Join(dir, "windmill", filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []struct {
		name    string
		change  func(*Config, string)
		changes bool
	}{
		{"stop_after_if on a step", edit("f/app/main.flow/flow.yaml", "    - id: report\n      value:",
			"    - id: report\n      stop_after_if:\n        expr: result.count > 1\n        skip_if_stopped: false\n      value:"), true},
		{"the inline script", edit("f/app/main.flow/greet.ts", "hello, ", "hi, "), true},
		{"an inlined workspace script", edit("f/app/format.ts", "[${text}]", "<${text}>"), true},
		{"the host-provided module", edit("f/shared/ctx.ts", "", "export const version = 1;\n"), true},
		{"a lock", write("f/app/report.script.lock", "{}\n//bun.lock\n<empty>"), true},
		{"a resource", edit("f/app/settings.resource.yaml", "retries: 3", "retries: 4"), true},
		{"the host-provided module's metadata", write("f/shared/ctx.script.yaml", "summary: the shared context\n"), true},
		// A secret's file is not a source: its digest would let a reader of
		// the manifest test guesses of a plain-text value.
		{"a secret variable", edit("f/app/token.variable.yaml", "synthetic-not", "other-synthetic-not"), false},
		{"the contract", func(c *Config, _ string) { c.Contract = append(c.Contract, "provided: []\n"...) }, false},
		{"the contract's roots", func(c *Config, _ string) {
			c.Contract = []byte("roots:\n  - script: f/app/report\n  - flow: f/app/main\nhost_modules: [f/shared/ctx.ts]\n")
		}, true},
		{"the module-state mode", func(c *Config, _ string) { c.ModuleState = engine.ModuleStateFresh }, true},
		{"the engine version", func(c *Config, _ string) { c.EngineVersion = "v0.0.1-test" }, true},
		{"the harness runtime", func(c *Config, _ string) {
			dir := filepath.Join(t.TempDir(), "runtime")
			copyTree(t, c.HarnessRuntime, dir)
			if err := os.WriteFile(filepath.Join(dir, "extra.ts"), []byte("export {};\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			c.HarnessRuntime = dir
		}, true},
		{"a script nothing reaches", write("f/app/unused.ts", "export async function main() { return 0; }\n"), false},
		{"a file the folder ignores", write("notes.txt", "synthetic notes\n"), false},
	} {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			cfg, dir := fixtureConfig(t, bun)
			c.change(&cfg, dir)
			got := build(t, cfg).Digest
			if (got != want) != c.changes {
				t.Errorf("digest %s, unchanged %s; want a change: %v", got, want, c.changes)
			}
		})
	}
}

// TestBuildOutput checks where a build writes: an output that holds
// anything is refused, an empty directory is replaced, and a failed build
// leaves nothing behind.
func TestBuildOutput(t *testing.T) {
	bun := bunPath(t)
	cfg, dir := fixtureConfig(t, bun)
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Out, "stale"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("a non-empty output: %v", err)
	}
	if err := os.Remove(filepath.Join(cfg.Out, "stale")); err != nil {
		t.Fatal(err)
	}
	build(t, cfg)

	cfg.Out = filepath.Join(t.TempDir(), "nested", "bundle")
	if err := os.WriteFile(filepath.Join(dir, "windmill", "f", "app", "format.ts"), []byte("export const = ;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Build(context.Background(), cfg)
	var be *BuildError
	if !errors.As(err, &be) {
		t.Fatalf("a script that does not parse: %v", err)
	}
	ents, rerr := os.ReadDir(filepath.Dir(cfg.Out))
	if rerr != nil || len(ents) != 0 {
		t.Errorf("a failed build left %v (%v)", ents, rerr)
	}
}

// TestBuildRefusesUnbuilt checks what the build refuses before bundling:
// a codebase script (esbuild bundling is C2b's) and a host-provided module
// that is not a Bun script of the folder (packages need a lock the
// contract cannot give).
func TestBuildRefusesUnbuilt(t *testing.T) {
	bun := bunPath(t)
	for name, c := range map[string]struct {
		files    map[string]string
		contract string
		item     string
		kind     string
		message  string
	}{
		"codebase script": {
			files: map[string]string{
				"wmill.yaml":     "defaultTs: bun\ncodebases:\n  - relative_path: f/cb\n    includes: ['f/cb/**']\n",
				"f/cb/entry.ts":  "export async function main() { return 1; }\n",
				"f/app/plain.ts": "export async function main() { return 2; }\n",
			},
			item: "script f/cb/entry", message: "unsupported: codebase script",
		},
		"host-provided module that does not read": {
			files: map[string]string{
				"wmill.yaml":            "defaultTs: bun\n",
				"f/app/plain.ts":        "export async function main() { return 2; }\n",
				"f/app/ctx.ts":          "export const store = new Map();\n",
				"f/app/ctx.script.yaml": "timeout: soon\n",
			},
			contract: "roots: [{script: f/app/plain}]\nhost_modules: [f/app/ctx.ts]\n",
			item:     "host module f/app/ctx.ts", kind: "load", message: "timeout",
		},
		"host-provided package": {
			files: map[string]string{
				"wmill.yaml":     "defaultTs: bun\n",
				"f/app/plain.ts": "export async function main() { return 2; }\n",
			},
			contract: "roots: [{script: f/app/plain}]\nhost_modules: [fixture-ctx]\n",
			item:     "host module fixture-ctx", message: "host-provided packages are not supported yet",
		},
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			cfg := Config{
				Folder: writeFolder(t, c.files), EngineVersion: fixtureEngine,
				Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), HarnessRuntime: repoPath(t, "harnesses/bun/runtime"),
				Out: filepath.Join(t.TempDir(), "bundle"),
			}
			if c.contract != "" {
				cfg.Contract = []byte(c.contract)
			}
			_, err := Build(context.Background(), cfg)
			var be *BuildError
			if !errors.As(err, &be) || len(be.Problems) != 1 {
				t.Fatalf("Build: %v", err)
			}
			p := be.Problems[0]
			kind := c.kind
			if kind == "" {
				kind = ProblemUnsupportedFeature
			}
			if p.Item != c.item || p.Kind != kind || !strings.Contains(p.Message, c.message) {
				t.Errorf("problem %+v", p)
			}
			if _, err := os.Stat(cfg.Out); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a refused build wrote %s", cfg.Out)
			}
		})
	}
}

// TestBuildCompileErrors checks that what the compiler refuses comes back
// as its own errors, before any bundling.
func TestBuildCompileErrors(t *testing.T) {
	cfg := Config{
		Folder: writeFolder(t, map[string]string{
			"wmill.yaml":                "defaultTs: bun\n",
			"f/app/main.flow/flow.yaml": "summary: x\nvalue:\n  modules:\n    - id: a\n      value:\n        type: script\n        path: f/app/missing\n",
		}),
		EngineVersion: fixtureEngine, Bun: "/nonexistent/bun", Helper: "/nonexistent/main.ts",
		HarnessRuntime: "/nonexistent/runtime", Out: filepath.Join(t.TempDir(), "bundle"),
	}
	_, err := Build(context.Background(), cfg)
	var ie *ir.Error
	if !errors.As(err, &ie) || ie.Kind != ir.ErrLoad {
		t.Fatalf("Build: %v", err)
	}
	if _, err := Build(context.Background(), Config{Folder: cfg.Folder}); err == nil {
		t.Error("a config without an engine version was accepted")
	}
	cfg.ModuleState = "shared"
	if _, err := Build(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "neither warm nor fresh") {
		t.Errorf("module state shared: %v", err)
	}
}

package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// outsideFiles is the synthetic folder of milestone F3h: a codebase entry
// whose relative_path, ../lib, lies outside the sync root (WR-49) holds a
// module with an AsyncLocalStorage store, which a codebase script imports
// from outside the folder (WR-47). Paths are from the directory above the
// sync root, windmill/.
var outsideFiles = map[string]string{
	"lib/ctx.ts": "import { AsyncLocalStorage } from \"node:async_hooks\";\n" +
		"export const store = new AsyncLocalStorage<{ tenant: string }>();\n",
	"windmill/wmill.yaml": "defaultTs: bun\nincludes: [\"f/**\"]\ncodebases:\n" +
		"  - relative_path: ../lib\n    includes: [\"f/cb/**\"]\n",
	"windmill/f/cb/read.ts": "import { store } from \"../../../lib/ctx.ts\";\n" +
		"export async function main() { return store.getStore() ?? null; }\n",
}

// A host contract can list a module outside the folder that a codebase
// script imports, by its path relative to the sync root: the build writes
// its copy and shim as for a module inside it, and the script then reads
// whichever instance is registered under that key: the host's own in host
// mode, whose scope the host entered, or the bundled copy elsewhere.
func TestBuildOutsideHostModule(t *testing.T) {
	bun := bunPath(t)
	dir := writeFolder(t, outsideFiles)
	cfg := Config{
		Folder:         filepath.Join(dir, "windmill"),
		Contract:       []byte("roots:\n  - {script: f/cb/read}\nhost_modules: [../lib/ctx.ts]\n"),
		EngineVersion:  fixtureEngine,
		Bun:            bun,
		Helper:         repoPath(t, "harnesses/bun/build/main.ts"),
		HarnessRuntime: repoPath(t, "harnesses/bun/runtime"),
		Out:            filepath.Join(t.TempDir(), "bundle"),
		Work:           t.TempDir(),
	}
	res := build(t, cfg)
	want := []HostModuleEntry{{Key: "../lib/ctx.ts", ID: "outside/1/lib/ctx.ts"}}
	if !slices.Equal(res.Manifest.HostModules, want) {
		t.Fatalf("manifest host modules %+v, want %+v", res.Manifest.HostModules, want)
	}
	copyFile := filepath.Join(cfg.Out, "scripts", "bun", "provided", "outside", "1", "lib", "ctx.ts.mjs")
	for _, p := range []string{"scripts/bun/provided/outside/1/lib/ctx.ts.mjs", "scripts/bun/provided/shims/outside/1/lib/ctx.ts.mjs"} {
		if _, ok := res.Manifest.Files[p]; !ok {
			t.Errorf("the manifest lists no %s", p)
		}
	}
	if _, err := Verify(cfg.Out, VerifyOptions{Digest: res.Digest}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.Out, "scripts", "bun", "f", "cb", "read.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta ScriptMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(meta.HostModules, []string{"../lib/ctx.ts"}) {
		t.Fatalf("f/cb/read uses host modules %q", meta.HostModules)
	}

	// The host's own instance: the module as the host's program imports
	// it, and a call that enters its scope around the step.
	host := filepath.Join(t.TempDir(), "host.ts")
	if err := os.WriteFile(host, []byte("import { store } from "+jsonString(filepath.Join(dir, "lib", "ctx.ts"))+";\n"+
		"export { store };\n"+
		"export async function inScope(module: string) {\n"+
		"  const step = await import(module);\n"+
		"  return store.run({ tenant: \"t1\" }, () => step.main());\n"+
		"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	step := filepath.Join(cfg.Out, "scripts", "bun", "f", "cb", "read.cjs")
	calls := []call{{Module: host, Export: "inScope", Args: []any{step}}, {Module: step, Export: "main"}}
	got := runModules(t, bun, nil, [][2]string{{"../lib/ctx.ts", host}}, calls)
	for i, w := range []string{`{"tenant":"t1"}`, `null`} {
		if got[i].Error != nil || !jsonEqual(t, got[i].Result, json.RawMessage(w)) {
			t.Errorf("host instance, call %d: %s (error %v), want %s", i, got[i].Result, got[i].Error, w)
		}
	}
	// The bundled copy, as a harness registers it without a host: another
	// instance, which the host's scope does not reach.
	got = runModules(t, bun, nil, [][2]string{{"../lib/ctx.ts", copyFile}}, calls[:1])
	if got[0].Error != nil || !jsonEqual(t, got[0].Result, json.RawMessage(`null`)) {
		t.Errorf("bundled copy: %s (error %v), want null", got[0].Result, got[0].Error)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestOutsideID(t *testing.T) {
	for rel, want := range map[string]string{
		"../lib/ctx.ts":          "outside/1/lib/ctx.ts",
		"../../shared/a/b.ts":    "outside/2/shared/a/b.ts",
		"../f/cb/same-as-inside": "outside/1/f/cb/same-as-inside",
	} {
		if got := OutsideID(rel); got != want {
			t.Errorf("OutsideID(%q) = %q, want %q", rel, got, want)
		}
	}
}

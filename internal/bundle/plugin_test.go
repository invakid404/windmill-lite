package bundle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pluginFolder is a folder whose host contract names a host plugin,
// f/app/plugin.ts, which imports the host-provided module f/app/ctx.ts, as
// the step f/app/plain does.
func pluginFolder(pluginSource string) map[string]string {
	return map[string]string{
		"wmill.yaml":      "defaultTs: bun\n",
		"f/app/ctx.ts":    "export const store = new Map<string, string>();\n",
		"f/app/plain.ts":  "import { store } from \"./ctx.ts\";\nexport async function main() { return store.get(\"plugin\") ?? null; }\n",
		"f/app/plugin.ts": pluginSource,
	}
}

const pluginContract = "roots: [{script: f/app/plain}]\nhost_modules: [f/app/ctx.ts]\nplugin: f/app/plugin.ts\n"

func pluginConfig(t *testing.T, bun string, files map[string]string) Config {
	t.Helper()
	return Config{
		Folder: writeFolder(t, files), Contract: []byte(pluginContract), EngineVersion: fixtureEngine,
		Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), HarnessRuntime: repoPath(t, "harnesses/bun/runtime"),
		Out: filepath.Join(t.TempDir(), "bundle"), Work: t.TempDir(),
	}
}

// TestBundleDigestCoversPlugin builds a bundle whose host contract names a
// plugin. The manifest records it and lists its bundle,
// plugins/f/app/plugin.mjs, among the files whose digests the bundle
// digest covers, and its source among the sources. A change to the
// plugin's source changes the bundle digest, and Verify refuses a bundle
// whose plugin was changed after the build. (That the plugin and the steps
// share the host-provided module is wl's TestBundledPluginSharesHostModule.)
func TestBundleDigestCoversPlugin(t *testing.T) {
	bun := bunPath(t)
	src := "import { store } from \"./ctx.ts\";\nexport default { onRunEnd: async () => { store.set(\"plugin\", \"one\"); return []; } };\n"
	cfg := pluginConfig(t, bun, pluginFolder(src))
	res := build(t, cfg)
	m := res.Manifest
	const file = "plugins/f/app/plugin.mjs"
	if m.Plugin == nil || m.Plugin.Key != "f/app/plugin.ts" || m.Plugin.File != file {
		t.Fatalf("manifest plugin %+v", m.Plugin)
	}
	if m.Files[file] == "" {
		t.Errorf("the manifest's files have no digest of %s: %v", file, m.Files)
	}
	if m.Sources["f/app/plugin.ts"] == "" {
		t.Errorf("the manifest's sources have no digest of f/app/plugin.ts: %v", m.Sources)
	}
	bundled, err := os.ReadFile(filepath.Join(cfg.Out, file))
	if err != nil {
		t.Fatal(err)
	}

	// Another plugin source, another bundle digest.
	cfg2 := pluginConfig(t, bun, pluginFolder(strings.Replace(src, `"one"`, `"two"`, 1)))
	res2 := build(t, cfg2)
	if res2.Manifest.Files[file] == m.Files[file] || res2.Manifest.Sources["f/app/plugin.ts"] == m.Sources["f/app/plugin.ts"] || res2.Digest == res.Digest {
		t.Errorf("a changed plugin kept its digests: file %s, source %s, bundle %s", res2.Manifest.Files[file], res2.Manifest.Sources["f/app/plugin.ts"], res2.Digest)
	}

	// A plugin changed after the build fails verification.
	if _, err := Verify(cfg.Out, VerifyOptions{Digest: res.Digest}); err != nil {
		t.Fatalf("Verify of the built bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Out, file), append(bundled, []byte("\n// changed\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(cfg.Out, VerifyOptions{Digest: res.Digest}); err == nil {
		t.Error("Verify accepted a bundle whose plugin changed after the build")
	}
}

// TestBuildPluginProblems: the build refuses a plugin that is not the file
// of a Bun script of the folder, naming it.
func TestBuildPluginProblems(t *testing.T) {
	bun := bunPath(t)
	files := pluginFolder("export default {};\n")
	cfg := pluginConfig(t, bun, files)
	cfg.Contract = []byte("roots: [{script: f/app/plain}]\nplugin: f/app/missing.ts\n")
	_, err := Build(context.Background(), cfg)
	var be *BuildError
	if !errors.As(err, &be) || len(be.Problems) != 1 {
		t.Fatalf("Build: %v", err)
	}
	if p := be.Problems[0]; p.Item != "plugin f/app/missing.ts" || p.Kind != ProblemUnsupportedFeature || !strings.Contains(p.Message, "not the file of a Bun script of the folder") {
		t.Errorf("problem %+v", p)
	}
}

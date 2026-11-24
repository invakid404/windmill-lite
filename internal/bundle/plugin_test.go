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

// TestBuildPluginProblems: the build refuses, naming it, a plugin that is
// not the file of a Bun script of the folder: no script at all, a Deno
// script, a //native script, each with a usable plugin export. It also
// refuses a plugin that imports a listed codebase script, whose shared copy
// esbuild builds, since the plugin's bundle would inline a second instance.
func TestBuildPluginProblems(t *testing.T) {
	bun := bunPath(t)
	const usable = "export default { onRunEnd: async () => [] };\n"
	for name, c := range map[string]struct {
		files    map[string]string
		contract string
		item     string
		message  string
	}{
		"no script": {
			files:    pluginFolder(usable),
			contract: "roots: [{script: f/app/plain}]\nplugin: f/app/missing.ts\n",
			item:     "plugin f/app/missing.ts", message: "not the file of a Bun script of the folder",
		},
		"deno script": {
			files:    withFile(pluginFolder(usable), "f/app/hooks.deno.ts", usable),
			contract: "roots: [{script: f/app/plain}]\nplugin: f/app/hooks.deno.ts\n",
			item:     "plugin f/app/hooks.deno.ts", message: "it is a deno script",
		},
		"native script": {
			files:    withFile(pluginFolder(usable), "f/app/hooks.ts", "//native\n"+usable),
			contract: "roots: [{script: f/app/plain}]\nplugin: f/app/hooks.ts\n",
			item:     "plugin f/app/hooks.ts", message: "it is a bunnative script",
		},
		"listed codebase module": {
			files: map[string]string{
				"wmill.yaml":      "defaultTs: bun\nincludes: [\"f/**\"]\ncodebases:\n  - relative_path: f/cb\n    includes: [\"f/cb/**\"]\n",
				"f/cb/ctx.ts":     "export const store = { tenant: null };\n",
				"f/cb/step.ts":    "import { store } from \"./ctx.ts\";\nexport function main() { return store.tenant; }\n",
				"f/app/plugin.ts": "import { store } from \"../cb/ctx.ts\";\nexport default { onRunEnd: async () => { store.tenant = null; return []; } };\n",
			},
			contract: "roots: [{script: f/cb/step}]\nhost_modules: [f/cb/ctx.ts]\nplugin: f/app/plugin.ts\n",
			item:     "plugin f/app/plugin.ts", message: "imports the host-provided module f/cb/ctx.ts, a codebase script",
		},
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			cfg := pluginConfig(t, bun, c.files)
			cfg.Contract = []byte(c.contract)
			_, err := Build(context.Background(), cfg)
			var be *BuildError
			if !errors.As(err, &be) || len(be.Problems) != 1 {
				t.Fatalf("Build: %v", err)
			}
			if p := be.Problems[0]; p.Item != c.item || p.Kind != ProblemUnsupportedFeature || !strings.Contains(p.Message, c.message) {
				t.Errorf("problem %+v", p)
			}
		})
	}
}

// TestVerifyRefusesPluginReference: a manifest whose plugin entry names a
// file outside the bundle, a module of plugins/ that it does not list, or
// a listed file outside plugins/ fails verification, so no runtime loads
// an unverified module as the plugin.
func TestVerifyRefusesPluginReference(t *testing.T) {
	bun := bunPath(t)
	cfg := pluginConfig(t, bun, pluginFolder("export default { onRunEnd: async () => [] };\n"))
	res := build(t, cfg)
	if _, err := Verify(cfg.Out, VerifyOptions{}); err != nil {
		t.Fatalf("Verify of the built bundle: %v", err)
	}
	for _, file := range []string{"../evil.mjs", "plugins/f/app/other.mjs", "scripts/bun/f/app/plain.mjs"} {
		m := *res.Manifest
		m.Plugin = &PluginEntry{Key: "f/app/plugin.ts", File: file}
		data, err := m.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Out, ManifestFile), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(cfg.Out, VerifyOptions{}); err == nil || !strings.Contains(err.Error(), "host plugin") {
			t.Errorf("Verify with the plugin at %s: %v, want a refusal of the host plugin", file, err)
		}
	}
}

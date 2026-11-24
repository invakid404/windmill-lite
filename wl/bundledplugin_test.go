package wl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
)

// bundledPluginFiles is a folder whose host contract names a host plugin,
// f/app/plugin.ts. Its around wrapper of the flow f/app/flow's step a puts
// a value for the run into the host-provided module f/app/ctx.ts, and the
// step, the script f/app/step, reads its run's value from the same module.
var bundledPluginFiles = map[string]string{
	"wmill.yaml":   "defaultTs: bun\n",
	"f/app/ctx.ts": "export const byRun = new Map<string, string>();\n",
	"f/app/step.ts": `import { byRun } from "./ctx.ts";
export async function main(tag: string) {
  await new Promise((r) => setTimeout(r, 10));
  return { tag, seen: byRun.get(process.env.WM_FLOW_JOB_ID ?? "") ?? null };
}
`,
	"f/app/plugin.ts": `import { byRun } from "./ctx.ts";
export default {
  around: [
    {
      selector: { flow: "f/app/flow", module: "a" },
      fn: async (c: { run: string; args: Record<string, unknown> }, next: () => Promise<unknown>) => {
        byRun.set(c.run, "around:" + String(c.args.tag));
        await next();
      },
    },
  ],
};
`,
	"f/app/flow.flow/flow.yaml": `{"summary":"p","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/app/step",` +
		`"input_transforms":{"tag":{"type":"javascript","expr":"flow_input.tag"}}}}]}}`,
}

const bundledPluginContract = "roots: [{flow: f/app/flow}]\nhost_modules: [f/app/ctx.ts]\nplugin: f/app/plugin.ts\n"

// buildContractBundle builds a real bundle of files with the host contract
// contract.
func buildContractBundle(t *testing.T, bun string, files map[string]string, contract string) (out, runtimeDir string) {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	for p, s := range files {
		f := filepath.Join(folder, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runtimeDir = filepath.Join(repo, "harnesses", "bun", "runtime")
	out = filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder: folder, Contract: []byte(contract), EngineVersion: Version(), Bun: bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: runtimeDir, Out: out, Work: filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	return out, runtimeDir
}

// TestBundledPluginSharesHostModule opens a bundle whose host contract
// names a plugin, with no Options.Plugin: the runtime loads the bundle's
// plugin, and its around wrapper and the step share the host-provided
// module's instance in each process. In 16 concurrent runs, each step reads
// the value that the wrapper put into the module for its own run; a plugin
// with a copy of its own of the module would leave the step's empty.
func TestBundledPluginSharesHostModule(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildContractBundle(t, bun, bundledPluginFiles, bundledPluginContract)
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	const n = 16
	type result struct {
		Tag  string
		Seen *string
	}
	got := make([]result, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			args, _ := json.Marshal(map[string]string{"tag": fmt.Sprint("run-", i)})
			res, err := rt.Run(context.Background(), "f/app/flow", args, RunOptions{})
			if err == nil && res.Status != StatusSuccess {
				err = fmt.Errorf("status %s: %s", res.Status, res.Result)
			}
			if err == nil {
				err = json.Unmarshal(res.Result, &got[i])
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	ok := 0
	for i := range n {
		want := fmt.Sprint("around:run-", i)
		switch {
		case errs[i] != nil:
			t.Errorf("run %d: %v", i, errs[i])
		case got[i].Seen == nil || *got[i].Seen != want:
			t.Errorf("run %d: the step read %v from the host-provided module, want %q", i, got[i].Seen, want)
		default:
			ok++
		}
	}
	t.Logf("%d of %d runs: the step read its own run's value, which the bundled plugin's around wrapper set", ok, n)
}

// TestOpenRefusesTwoPlugins: a bundle with its own host plugin does not
// open with Options.Plugin set too, naming the bundle's plugin, nor in the
// fresh module state, whose processes live for one step.
func TestOpenRefusesTwoPlugins(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildContractBundle(t, bun, bundledPluginFiles, bundledPluginContract)
	other := filepath.Join(t.TempDir(), "other.ts")
	if err := os.WriteFile(other, []byte("export default {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: other}); err == nil || !strings.Contains(err.Error(), "its own host plugin, f/app/plugin.ts") {
		if rt != nil {
			rt.Close()
		}
		t.Errorf("Open with Options.Plugin and the bundle's plugin: %v", err)
	}
	if rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: ModuleStateFresh}); err == nil || !strings.Contains(err.Error(), "needs warm harness processes") {
		if rt != nil {
			rt.Close()
		}
		t.Errorf("Open of the bundle's plugin in fresh mode: %v", err)
	}
}

// fetchWrapPlugin wraps the global fetch when it loads: every request that
// goes through the wrapper carries the header x-plugin-wrap.
const fetchWrapPlugin = `const inner = globalThis.fetch;
globalThis.fetch = Object.assign(
  (input: RequestInfo | URL, init?: RequestInit) => {
    const req = new Request(input, init);
    req.headers.set("x-plugin-wrap", "1");
    return inner(req);
  },
  inner,
);
export default {};
`

// TestPluginFetchWrapperOrder: a harness process loads a host plugin after
// the runtime has installed its fetch view and the native realms have
// taken the web APIs. A fetch wrapper that the plugin installs at load
// therefore reaches an ordinary step, which reads globalThis.fetch on each
// call, and not a native step, whose realm took fetch before the plugin
// loaded. So for the bundle's own plugin and for one that Options.Plugin
// names alike.
func TestPluginFetchWrapperOrder(t *testing.T) {
	bun := bunOrSkip(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("x-plugin-wrap"))
	}))
	defer srv.Close()
	files := map[string]string{
		"wmill.yaml":      "defaultTs: bun\n",
		"f/app/plain.ts":  "export async function main(url: string) { return await (await fetch(url)).text(); }\n",
		"f/app/native.ts": "//native\nexport async function main(url: string) { return await (await fetch(url)).text(); }\n",
		"f/app/plugin.ts": fetchWrapPlugin,
	}
	for _, s := range []string{"plain", "native"} {
		files["f/app/"+s+"_flow.flow/flow.yaml"] = `{"summary":"f","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/app/` + s +
			`","input_transforms":{"url":{"type":"javascript","expr":"flow_input.url"}}}}]}}`
	}
	const roots = "roots: [{flow: f/app/plain_flow}, {flow: f/app/native_flow}]\n"
	optionPlugin := filepath.Join(t.TempDir(), "plugin.ts")
	if err := os.WriteFile(optionPlugin, []byte(fetchWrapPlugin), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		name, contract, plugin string
	}{
		{"bundled", roots + "plugin: f/app/plugin.ts\n", ""},
		{"Options.Plugin", roots, optionPlugin},
	} {
		t.Run(strings.ReplaceAll(v.name, " ", "_"), func(t *testing.T) {
			out, runtimeDir := buildContractBundle(t, bun, files, v.contract)
			rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: v.plugin})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			args, _ := json.Marshal(map[string]string{"url": srv.URL})
			for _, c := range []struct{ script, want string }{
				{"f/app/plain", `"1"`},
				{"f/app/native", `""`},
			} {
				res, err := rt.Run(context.Background(), c.script+"_flow", args, RunOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if res.Status != StatusSuccess || string(res.Result) != c.want {
					t.Errorf("%s: %s %s, want the header x-plugin-wrap %s", c.script, res.Status, res.Result, c.want)
				}
			}
		})
	}
}

// TestBundledPluginWithoutSteps: a bundle whose only code is its plugin,
// with a flow of no script step, still starts harness processes for the
// plugin, whose onRunEnd adds a warning to the run.
func TestBundledPluginWithoutSteps(t *testing.T) {
	bun := bunOrSkip(t)
	files := map[string]string{
		"wmill.yaml":                "defaultTs: bun\n",
		"f/app/plugin.ts":           "export default { onRunEnd: async () => [{ code: \"plugin_ran\", message: \"no step\" }] };\n",
		"f/app/flow.flow/flow.yaml": `{"summary":"i","value":{"modules":[{"id":"a","value":{"type":"identity"}}]}}`,
	}
	out, runtimeDir := buildContractBundle(t, bun, files, "roots: [{flow: f/app/flow}]\nplugin: f/app/plugin.ts\n")
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	res, err := rt.Run(context.Background(), "f/app/flow", json.RawMessage(`{}`), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusSuccess || !slices.ContainsFunc(res.Warnings, func(w Warning) bool { return w.Code == "plugin_ran" }) {
		t.Fatalf("%s %+v: want success with the plugin's warning plugin_ran", res.Status, res.Warnings)
	}
}

// TestOpenRefusesUnverifiedPlugin: Open refuses a bundle whose manifest
// names, as its plugin, a module outside the bundle, which verification
// never checked, and that module never loads: it would write a marker file
// when it did.
func TestOpenRefusesUnverifiedPlugin(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildContractBundle(t, bun, bundledPluginFiles, bundledPluginContract)
	marker := filepath.Join(t.TempDir(), "evil-loaded")
	evil := fmt.Sprintf("require(\"node:fs\").writeFileSync(%q, \"loaded\");\nexport default {};\n", marker)
	if err := os.WriteFile(filepath.Join(filepath.Dir(out), "evil.mjs"), []byte(evil), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, bundle.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var m bundle.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m.Plugin = &bundle.PluginEntry{Key: "f/app/plugin.ts", File: "../evil.mjs"}
	if data, err = m.Canonical(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, bundle.ManifestFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir})
	if err == nil {
		rt.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "host plugin") {
		t.Errorf("Open: %v, want a refusal of the manifest's host plugin", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the module outside the bundle loaded as the plugin")
	}
}

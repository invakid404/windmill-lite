package bundle

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// manyModulesFolder writes a folder whose codebase scripts each import a
// package of n modules, module i importing modules 2i+1 and 2i+2, and a
// module of the folder that a host contract can list (f/lib/ctx.ts).
func manyModulesFolder(t testing.TB, n, scripts int) string {
	t.Helper()
	files := map[string]string{
		"node_modules/bigdep/package.json": `{"name":"bigdep","version":"1.0.0","main":"m0.js","type":"module"}`,
		"f/lib/ctx.ts":                     "export const store = { tenant: null };\n",
	}
	for i := range n {
		text := ""
		sum := fmt.Sprint(i)
		for _, j := range []int{2*i + 1, 2*i + 2} {
			if j < n {
				text += fmt.Sprintf("import { v%d } from \"./m%d.js\";\n", j, j)
				sum += fmt.Sprintf(" + v%d", j)
			}
		}
		files[fmt.Sprintf("node_modules/bigdep/m%d.js", i)] = text + fmt.Sprintf("export const v%d = %s;\n", i, sum)
	}
	for k := range scripts {
		files[fmt.Sprintf("f/cb/s%d.ts", k)] = fmt.Sprintf("import { v0 } from \"bigdep\";\nimport { store } from \"../lib/ctx.ts\";\n"+
			"export function main() { return [v0 + %d, store.tenant]; }\n", k)
	}
	return writeFolder(t, files)
}

// bundleScripts bundles the folder's scripts with esbuild as BuildCodebase
// does, with f/lib/ctx.ts listed as a host-provided module when listed is
// set, and returns how long it took.
func bundleScripts(t testing.TB, folder string, scripts int, listed bool) time.Duration {
	t.Helper()
	cfg := &CodebaseConfig{Folder: folder}
	shims := map[string]string{}
	if listed {
		shim, err := Shim("f/lib/ctx.ts", []string{"store"})
		if err != nil {
			t.Fatal(err)
		}
		cfg.HostModules = []CodebaseHostModule{{Key: "f/lib/ctx.ts", File: "f/lib/ctx.ts", Shim: filepath.Join(folder, "unused.mjs")}}
		shims["f/lib/ctx.ts"] = string(shim)
	}
	s, err := ParseCodebaseSettings(nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for k := range scripts {
		p := fmt.Sprintf("f/cb/s%d", k)
		_, _, used, _, prob := cfg.esbuild(CodebaseScript{ID: p, Path: p, File: p + ".ts"}, s, EnvBinding, shims, "")
		if prob != nil {
			t.Fatalf("%s: %s: %s", p, prob.Kind, prob.Message)
		}
		if listed != (len(used) == 1) {
			t.Fatalf("%s: listed %v, used %q", p, listed, used)
		}
	}
	return time.Since(start)
}

// Listing a host-provided module leaves a codebase build's time as it was
// (milestone F3h). Mapping imports of the listed module to its shim once
// resolved every import a second time, outside esbuild's directory cache,
// which made a build that imports a package of many modules 16 to 44 times
// slower; the shim now replaces the module's file when esbuild loads it.
func TestCodebaseHostModulesKeepBuildTime(t *testing.T) {
	const n, scripts = 400, 6
	folder := manyModulesFolder(t, n, scripts)
	fastest := func(listed bool) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range 3 {
			best = min(best, bundleScripts(t, folder, scripts, listed))
		}
		return best
	}
	plain, listed := fastest(false), fastest(true)
	t.Logf("%d scripts importing %d modules: %v without a host-provided module, %v with one", scripts, n, plain, listed)
	if listed > 3*plain+300*time.Millisecond {
		t.Fatalf("listing a host-provided module made the build %.1f times slower (%v against %v)",
			float64(listed)/float64(plain), listed, plain)
	}
}

func BenchmarkCodebaseHostModules(b *testing.B) {
	const n, scripts = 400, 6
	folder := manyModulesFolder(b, n, scripts)
	for _, listed := range []bool{false, true} {
		b.Run(fmt.Sprintf("listed=%v", listed), func(b *testing.B) {
			for b.Loop() {
				bundleScripts(b, folder, scripts, listed)
			}
		})
	}
}

// sfxPackage is a package marked sideEffects:false whose value.js sets an
// unread global as it loads, and whose register.js only sets one.
func sfxPackage(name, global string) map[string]string {
	return map[string]string{
		"package.json": `{"name":"` + name + `","version":"1.0.0","main":"index.js","type":"module","sideEffects":false}`,
		"index.js":     "export { value } from \"./value.js\";\n",
		"value.js":     "globalThis." + global + " = (globalThis." + global + " ?? 0) + 1;\nexport const value = \"sfx\";\n",
		"register.js":  "globalThis.wlRegistered = true;\n",
	}
}

// Listing host-provided modules keeps the load effects of the modules a
// step bundle still inlines, those of modules marked sideEffects:false
// included (milestone F3h): esbuild and the Bun build resolve and prune
// every import the same way whether or not a module is listed, and only a
// listed module's file is replaced, by its shim. The one difference is
// D12's own: every listed module loads once per process when it is
// registered, so a listed module that a step imports but never uses, which
// sideEffects:false prunes from the plain bundle, still runs its load
// effects (step g).
func TestHostModulesKeepLoadEffects(t *testing.T) {
	bun := bunPath(t)
	reg := newRegistry(t, map[string]map[string]string{
		"sfx@1.0.0":  sfxPackage("sfx", "wlBunSfx"),
		"sfxh@1.0.0": sfxPackage("sfxh", "wlH"),
	})
	files := map[string]string{
		"windmill/wmill.yaml": "defaultTs: bun\nincludes: [\"f/**\"]\ncodebases:\n" +
			"  - relative_path: ../lib\n    includes: [\"f/cb/**\"]\n",
		// Outside the folder, in a directory marked sideEffects:false.
		"lib/package.json": `{"sideEffects":false}`,
		"lib/ctx.ts":       "import { value } from \"./m.ts\";\nexport const tag = \"c\" + value;\n",
		"lib/m.ts":         "globalThis.wlC = (globalThis.wlC ?? 0) + 1;\nexport const value = 1;\n",
		"lib/reg.ts":       "import \"./effect.ts\";\nexport const tag = \"d\";\n",
		"lib/effect.ts":    "globalThis.wlD = true;\n",
		"lib/plain.ts":     "globalThis.wlE = (globalThis.wlE ?? 0) + 1;\nexport const value = \"e\";\n",
		"lib/flag.ts":      "globalThis.wlF = (globalThis.wlF ?? 0) + 1;\nexport const flag = \"f\";\n",
		"lib/unused.ts":    "globalThis.wlG = (globalThis.wlG ?? 0) + 1;\nexport const flag = \"g\";\n",
		// Codebase scripts: a used value of the package, a bare import of
		// its register.js, which sideEffects:false prunes, and modules
		// outside the folder, listed or not.
		"windmill/f/cb/a.ts": "import { value } from \"sfx\";\nexport function main() { return [value, globalThis.wlA ?? null]; }\n",
		"windmill/f/cb/b.ts": "import \"sfx/register.js\";\nexport function main() { return globalThis.wlRegistered ?? null; }\n",
		"windmill/f/cb/c.ts": "import { tag } from \"../../../lib/ctx.ts\";\nexport function main() { return [tag, globalThis.wlC ?? null]; }\n",
		"windmill/f/cb/d.ts": "import { tag } from \"../../../lib/reg.ts\";\nexport function main() { return [tag, globalThis.wlD ?? null]; }\n",
		"windmill/f/cb/e.ts": "import { value } from \"../../../lib/plain.ts\";\nexport function main() { return [value, globalThis.wlE ?? null]; }\n",
		"windmill/f/cb/f.ts": "import { flag } from \"../../../lib/flag.ts\";\nexport function main() { return [flag, globalThis.wlF ?? null]; }\n",
		"windmill/f/cb/g.ts": "import { flag } from \"../../../lib/unused.ts\";\nexport function main() { return globalThis.wlG ?? null; }\n",
		// A module of the folder that uses a package only in a function:
		// esbuild inlines it, with the folder's copy of the package, and
		// when it is listed the Bun build makes its copy, with the lock's.
		"windmill/f/lib/sfxuser.ts":          "import { value } from \"sfxh\";\nexport function tag() { return value; }\n",
		"windmill/f/lib/sfxuser.script.lock": reg.lock(t, bun, map[string]string{"sfxh": "1.0.0"}),
		"windmill/f/cb/h.ts": "import { tag } from \"../lib/sfxuser.ts\";\n" +
			"export function main() { return [typeof tag, globalThis.wlH ?? null]; }\n",
		// A Bun script: the package from its lock, beside a listed module
		// of the folder.
		"windmill/f/lib/x.ts":          "export const x = \"x\";\n",
		"windmill/f/bun/a.ts":          "import { value } from \"sfx\";\nimport { x } from \"../lib/x.ts\";\nexport function main() { return [value, x, globalThis.wlBunSfx ?? null]; }\n",
		"windmill/f/bun/a.script.lock": reg.lock(t, bun, map[string]string{"sfx": "1.0.0"}),
	}
	for p, text := range sfxPackage("sfx", "wlA") {
		files["windmill/node_modules/sfx/"+p] = text
	}
	for p, text := range sfxPackage("sfxh", "wlH") {
		files["windmill/node_modules/sfxh/"+p] = text
	}
	dir := writeFolder(t, files)
	steps := []string{"f/bun/a", "f/cb/a", "f/cb/b", "f/cb/c", "f/cb/d", "f/cb/e", "f/cb/f", "f/cb/g", "f/cb/h"}
	roots := "roots:\n"
	for _, s := range steps {
		roots += "  - {script: " + s + "}\n"
	}
	run := func(contract string) map[string]string {
		cfg := Config{
			Folder: filepath.Join(dir, "windmill"), Contract: []byte(contract), EngineVersion: fixtureEngine,
			Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), HarnessRuntime: repoPath(t, "harnesses/bun/runtime"),
			Out: filepath.Join(t.TempDir(), "bundle"), Work: t.TempDir(), Env: reg.env,
		}
		res := build(t, cfg)
		// Every listed module is registered, as a harness registers them,
		// and each step runs in a process of its own.
		register := [][2]string{}
		for _, h := range res.Manifest.HostModules {
			register = append(register, [2]string{h.Key, filepath.Join(cfg.Out, "scripts", "bun", "provided", filepath.FromSlash(h.ID)+".mjs")})
		}
		got := map[string]string{}
		for _, s := range steps {
			r := runModules(t, bun, nil, register, []call{{Module: builtModuleOf(t, cfg.Out, s), Export: "main"}})[0]
			if r.Error != nil {
				b, _ := os.ReadFile(builtModuleOf(t, cfg.Out, s))
				t.Fatalf("%s threw %s\n%s", s, *r.Error, b)
			}
			got[s] = string(r.Result)
		}
		return got
	}
	plain := run(roots)
	listed := run(roots + "host_modules: [f/lib/x.ts, f/lib/sfxuser.ts, ../lib/ctx.ts, ../lib/reg.ts, ../lib/flag.ts, ../lib/unused.ts]\n")
	want := map[string]string{
		"f/bun/a": `["sfx","x",1]`, "f/cb/a": `["sfx",1]`, "f/cb/b": `null`, "f/cb/c": `["c1",1]`,
		"f/cb/d": `["d",null]`, "f/cb/e": `["e",1]`, "f/cb/f": `["f",1]`, "f/cb/g": `null`,
		"f/cb/h": `["function",1]`,
	}
	if !maps.Equal(plain, want) {
		t.Errorf("without host-provided modules: %v, want %v", plain, want)
	}
	want["f/cb/g"] = `1`
	if !maps.Equal(listed, want) {
		for _, s := range slices.Sorted(maps.Keys(want)) {
			if listed[s] != want[s] {
				t.Errorf("with host-provided modules, %s returned %s, want %s (without: %s)", s, listed[s], want[s], plain[s])
			}
		}
	}
}

// builtModuleOf is the module file of a script in a bundle: a codebase
// script's .cjs, or the .mjs of a Bun script.
func builtModuleOf(t testing.TB, out, id string) string {
	t.Helper()
	base := filepath.Join(out, "scripts", "bun", filepath.FromSlash(id))
	if _, err := os.Stat(base + ".cjs"); err == nil {
		return base + ".cjs"
	}
	return base + ".mjs"
}

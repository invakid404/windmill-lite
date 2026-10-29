package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// spells decides whether a codebase bundle's inputs spell the env
// rewrite's binding, as written or through an identifier's Unicode
// escapes, as the Bun helper's spells does.
func TestSpells(t *testing.T) {
	const name = "__windmill_lite_Bun"
	for _, c := range []struct {
		text string
		want bool
	}{
		{"const __windmill_lite_Bun = 1;", true},
		{"const __windmill_lite_B\\u0075n = 1;", true},
		{"const __windmill_lite_B\\u{75}n = 1;", true},
		{"const __windmill_lite_B\\u{0000075}n = 1;", true},
		{"const \\u005f_windmill_lite_Bun = 1;", true},
		{"const __windmill_lite_Bu = 1; // __windmill_lite_Bun in a comment", true},
		{"const __windmill_lite_Bunny = 1;", true},
		{"const __windmill_lite_B = 1; Bun.env.X", false},
		{"const s = '\\u{110000}__windmill_lite_B';", false},
		{"const s = '\\u00zz';", false},
	} {
		if got := spells(c.text, name); got != c.want {
			t.Errorf("spells(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// esbuild resolves the imports of different modules concurrently, so the
// plugin's callbacks that map host-provided modules to their shims run at
// once (c2b review 1, finding 1). Many modules of one codebase bundle each
// import a listed folder module and a listed package: the build records
// both, every import reads the registered instance, and under -race no
// write is unsynchronised.
func TestBuildCodebaseConcurrentHostImports(t *testing.T) {
	bun := bunPath(t)
	const n = 64
	files := map[string]string{
		"f/shared/ctx.ts":                        "export const tag = \"inlined copy\";\n",
		"node_modules/fixture-host/package.json": `{"name":"fixture-host","version":"1.0.0","main":"index.js","type":"module"}`,
		"node_modules/fixture-host/index.js":     "export const pkgTag = \"inlined package\";\n",
	}
	var entry strings.Builder
	for i := range n {
		files[fmt.Sprintf("f/cb/m%d.ts", i)] = fmt.Sprintf(
			"import { tag } from \"../shared/ctx\";\nimport { pkgTag } from \"fixture-host\";\nexport const v%d = () => tag + \" \" + pkgTag;\n", i)
		fmt.Fprintf(&entry, "import { v%d } from \"./m%d\";\n", i, i)
	}
	entry.WriteString("export function main() {\n  return [")
	for i := range n {
		fmt.Fprintf(&entry, "v%d(), ", i)
	}
	entry.WriteString("];\n}\n")
	files["f/cb/entry.ts"] = entry.String()
	folder := writeFolder(t, files)
	shims := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(shims, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctxShim, err := Shim("f/shared/ctx.ts", []string{"tag"})
	if err != nil {
		t.Fatal(err)
	}
	pkgShim, err := Shim("fixture-host", []string{"pkgTag"})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	res, err := BuildCodebase(context.Background(), CodebaseConfig{
		Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), Folder: folder, Out: out, Work: t.TempDir(),
		Entries: []CodebaseScript{{ID: "f/cb/entry", Path: "f/cb/entry", File: "f/cb/entry.ts", Settings: json.RawMessage(`{}`)}},
		HostModules: []CodebaseHostModule{
			{Key: "f/shared/ctx.ts", File: "f/shared/ctx.ts", Shim: write("ctx.mjs", ctxShim)},
			{Key: "fixture-host", Package: "fixture-host", Shim: write("pkg.mjs", pkgShim)},
		},
	})
	if err != nil {
		t.Fatalf("BuildCodebase: %v", err)
	}
	if got := res.Scripts["f/cb/entry"].HostModules; !slices.Equal(got, []string{"f/shared/ctx.ts", "fixture-host"}) {
		t.Fatalf("host modules %q", got)
	}
	registered := t.TempDir()
	ctxCopy := filepath.Join(registered, "ctx.mjs")
	pkgCopy := filepath.Join(registered, "pkg.mjs")
	if err := os.WriteFile(ctxCopy, []byte("export const tag = \"registered\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pkgCopy, []byte("export const pkgTag = \"registered package\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runModules(t, bun, nil, [][2]string{{"f/shared/ctx.ts", ctxCopy}, {"fixture-host", pkgCopy}},
		[]call{{Module: filepath.Join(out, "scripts", "bun", "f", "cb", "entry.cjs"), Export: "main"}})
	if got[0].Error != nil {
		t.Fatalf("main threw %s", *got[0].Error)
	}
	var values []string
	if err := json.Unmarshal(got[0].Result, &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != n {
		t.Fatalf("%d values, want %d", len(values), n)
	}
	for i, v := range values {
		if v != "registered registered package" {
			t.Fatalf("value %d is %q: an import did not resolve to its shim", i, v)
		}
	}
}

// One codebase script that fails does not keep the others from their
// outputs (c2b review 3, finding 5): an entry whose module and assets
// build gets its metadata and assets, so a caller can run it, while the
// failed entries are reported. An asset that collides with one an earlier
// entry took is the later entry's problem; paths that would not extract to
// one tree are the bundle's, and then nothing is written.
func TestBuildCodebasePartialSuccess(t *testing.T) {
	bun := bunPath(t)
	build := func(t *testing.T, files map[string]string, entries []CodebaseScript) (*CodebaseResult, *BuildError, string) {
		t.Helper()
		out := t.TempDir()
		res, err := BuildCodebase(context.Background(), CodebaseConfig{
			Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), Folder: writeFolder(t, files), Out: out, Work: t.TempDir(),
			Entries: entries,
		})
		var be *BuildError
		if err != nil && !errors.As(err, &be) {
			t.Fatalf("BuildCodebase: %v", err)
		}
		return res, be, out
	}
	entry := func(id, settings string) CodebaseScript {
		return CodebaseScript{ID: id, Path: id, File: id + ".ts", Settings: json.RawMessage(settings)}
	}
	exists := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}

	t.Run("independent entries", func(t *testing.T) {
		res, be, out := build(t, map[string]string{
			"f/cb/good.ts":    "import { readFileSync } from \"node:fs\";\nexport function main() { return [42, readFileSync(\"data/a.txt\", \"utf8\")]; }\n",
			"f/cb/bad.ts":     "import { x } from \"./missing\";\nexport function main() { return x; }\n",
			"f/cb/clash.ts":   "export function main() { return 2; }\n",
			"f/cb/a.txt":      "good asset",
			"f/cb/other.txt":  "other content",
			"f/cb/shared.txt": "good asset",
		}, []CodebaseScript{
			entry("f/cb/bad", `{}`),
			entry("f/cb/good", `{"assets":[{"from":"f/cb/a.txt","to":"data/a.txt"}]}`),
			entry("f/cb/clash", `{"assets":[{"from":"f/cb/shared.txt","to":"data/same.txt"},{"from":"f/cb/other.txt","to":"data/a.txt"}]}`),
		})
		if be == nil {
			t.Fatal("no problems reported")
		}
		got := map[string]string{}
		for _, p := range be.Problems {
			got[p.Item] = p.Kind
		}
		if want := map[string]string{"script f/cb/bad": ProblemBundleFailed, "script f/cb/clash": ProblemAssetCollision}; !reflect.DeepEqual(got, want) {
			t.Fatalf("problems %v, want %v", be.Problems, want)
		}
		scripts := filepath.Join(out, "scripts", "bun", "f", "cb")
		if !exists(filepath.Join(scripts, "good.cjs")) || !exists(filepath.Join(scripts, "good.meta.json")) {
			t.Fatal("the entry that built has no module or no metadata")
		}
		for _, f := range []string{"bad.meta.json", "clash.meta.json"} {
			if exists(filepath.Join(scripts, f)) {
				t.Errorf("a failed entry has %s", f)
			}
		}
		// The colliding entry takes none of its assets, not even the one
		// that does not collide.
		if b, err := os.ReadFile(filepath.Join(out, "assets", "data", "a.txt")); err != nil || string(b) != "good asset" {
			t.Fatalf("the good entry's asset: %q, %v", b, err)
		}
		if exists(filepath.Join(out, "assets", "data", "same.txt")) || len(res.Assets) != 1 {
			t.Errorf("assets %v: the colliding entry's were written", res.Assets)
		}
		// The entry that built runs, reading its asset where a harness
		// extracts it, in the working directory.
		cwd := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cwd, "data"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cwd, "data", "a.txt"), []byte("good asset"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := runModulesIn(t, bun, cwd, []call{{Module: filepath.Join(scripts, "good.cjs"), Export: "main"}})
		if r[0].Error != nil || !jsonEqual(t, r[0].Result, json.RawMessage(`[42,"good asset"]`)) {
			t.Fatalf("the good entry ran: %s %v", r[0].Result, r[0].Error)
		}
	})

	t.Run("paths that do not form one tree", func(t *testing.T) {
		res, be, out := build(t, map[string]string{
			"f/cb/one.ts": "export function main() { return 1; }\n",
			"f/cb/two.ts": "export function main() { return 2; }\n",
			"f/cb/x.txt":  "file",
			"f/cb/y.txt":  "below",
		}, []CodebaseScript{
			entry("f/cb/one", `{"assets":[{"from":"f/cb/x.txt","to":"data"}]}`),
			entry("f/cb/two", `{"assets":[{"from":"f/cb/y.txt","to":"data/y.txt"}]}`),
		})
		if be == nil || len(be.Problems) != 1 || be.Problems[0].Item != "assets" || be.Problems[0].Kind != ProblemAssetCollision {
			t.Fatalf("problems %v; want the bundle's asset collision", be)
		}
		if exists(filepath.Join(out, "assets")) || exists(filepath.Join(out, "scripts", "bun", "f", "cb", "one.meta.json")) || len(res.Assets) != 0 {
			t.Fatal("assets or metadata were written for a bundle whose assets cannot extract")
		}
	})
}

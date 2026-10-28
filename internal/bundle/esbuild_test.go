package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

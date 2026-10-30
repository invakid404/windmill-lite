package bundle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestBuiltins checks the names of the modules the runtime has built in, as
// the build helper lists them from its Bun (the kept op): Node's modules
// with their subpaths and Bun's own are listed, and a specifier names one
// when it is a listed name, alone or after node:.
func TestBuiltins(t *testing.T) {
	bun := bunPath(t)
	work := t.TempDir()
	raw := filepath.Join(work, "c.raw.cjs")
	if err := os.WriteFile(raw, []byte("// a.js\nvar a = () => import(\"node:async_hooks\");\n// b.js\nvar b = (s) => import(s);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bc := &BunConfig{Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), Folder: work, Work: work, Out: work}
	kept, b, err := keptImports(context.Background(), bc, map[string]string{"c": raw})
	if err != nil {
		t.Fatal(err)
	}
	if got := kept["c"]; len(got) != 1 || !got["b.js"] {
		t.Errorf("kept imports %v, want only b.js's", got)
	}
	for _, spec := range []string{"async_hooks", "node:async_hooks", "fs/promises", "node:fs/promises", "bun", "bun:sqlite"} {
		if !b.has(spec) {
			t.Errorf("%q is not a built-in module", spec)
		}
	}
	for _, spec := range []string{"node:nope", "bun:nope", "left-pad", "./fs", "async_hooks/nope"} {
		if b.has(spec) {
			t.Errorf("%q is a built-in module", spec)
		}
	}
}

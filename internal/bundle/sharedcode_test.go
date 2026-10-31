package bundle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestBuiltins checks the specifiers that name a module the runtime has
// built in, as the build helper lists them from its Bun (the kept op).
// Node's modules with their subpaths name one alone or after node:. Bun's
// own name one only alone: Bun rejects node:bun, node:bun:sqlite, node:ws
// and node:undici, so an import() of one is kept like an external one.
func TestBuiltins(t *testing.T) {
	bun := bunPath(t)
	work := t.TempDir()
	raw := filepath.Join(work, "c.raw.cjs")
	if err := os.WriteFile(raw, []byte("// a.js\nvar a = () => import(\"node:async_hooks\");\n// b.js\nvar b = (s) => import(s);\n// c.js\nvar c = () => import(\"node:ws\");\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bc := &BunConfig{Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), Folder: work, Work: work, Out: work}
	kept, b, err := keptImports(context.Background(), bc, map[string]string{"c": raw})
	if err != nil {
		t.Fatal(err)
	}
	if got := kept["c"]; len(got) != 2 || !got["b.js"] || !got["c.js"] {
		t.Errorf("kept imports %v, want b.js's and c.js's", got)
	}
	for _, spec := range []string{"async_hooks", "node:async_hooks", "fs/promises", "node:fs/promises", "bun", "bun:sqlite", "ws", "undici"} {
		if !b.has(spec) {
			t.Errorf("%q is not a built-in module", spec)
		}
	}
	for _, spec := range []string{"node:nope", "bun:nope", "left-pad", "./fs", "async_hooks/nope", "node:bun", "node:bun:sqlite", "node:ws", "node:undici", "node:test"} {
		if b.has(spec) {
			t.Errorf("%q is a built-in module", spec)
		}
	}
}

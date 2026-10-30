package wl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
)

// A bundle whose host contract lists modules outside the sync folder
// (milestone F3h) runs on the Bun harness without a host: the harness
// registers each module's bundled copy, by the key the contract gives it,
// so a codebase script that imports it from outside the folder finds no
// host scope, and two step bundles share the one copy.
func TestRunOutsideHostModulesOnBun(t *testing.T) {
	bun := bunOrSkip(t)
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for p, s := range map[string]string{
		"lib/ctx.ts": "import { AsyncLocalStorage } from \"node:async_hooks\";\n" +
			"export const store = new AsyncLocalStorage<{ tenant: string }>();\n",
		"lib/count.ts": "let n = 0;\nexport function bump() { n++; return n; }\n",
		"windmill/wmill.yaml": "defaultTs: bun\nincludes: [\"f/**\"]\ncodebases:\n" +
			"  - relative_path: ../lib\n    includes: [\"f/cb/**\"]\n",
		"windmill/f/cb/read.ts": "import { store } from \"../../../lib/ctx.ts\";\n" +
			"export async function main() { return store.getStore() ?? null; }\n",
		"windmill/f/cb/bump.ts":  "import { bump } from \"../../../lib/count.ts\";\nexport function main() { return bump(); }\n",
		"windmill/f/cb/bump2.ts": "import { bump } from \"../../../lib/count\";\nexport function main() { return bump(); }\n",
		"windmill/f/t/twice.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
			`{"id":"a","value":{"type":"script","path":"f/cb/bump","input_transforms":{}}},` +
			`{"id":"b","value":{"type":"script","path":"f/cb/bump2","input_transforms":{}}}]}}`,
	} {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runtimeDir := filepath.Join(repo, "harnesses", "bun", "runtime")
	out := filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder:         filepath.Join(dir, "windmill"),
		Contract:       []byte("roots:\n  - script: f/cb/read\n  - flow: f/t/twice\nhost_modules: [../lib/ctx.ts, ../lib/count.ts]\n"),
		EngineVersion:  Version(),
		Bun:            bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: runtimeDir,
		Out:            out,
		Work:           filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	res, err := rt.Run(context.Background(), "f/cb/read", json.RawMessage(`{}`), RunOptions{Kind: KindScript})
	if err != nil || res.Status != StatusSuccess || string(res.Result) != `null` {
		t.Fatalf("f/cb/read: %+v %v", res, err)
	}
	// Had each step bundled its own copy of count.ts, both would return 1.
	res, err = rt.Run(context.Background(), "f/t/twice", json.RawMessage(`{}`), RunOptions{})
	if err != nil || res.Status != StatusSuccess || string(res.Result) != `2` {
		t.Fatalf("f/t/twice: %+v %v", res, err)
	}
}

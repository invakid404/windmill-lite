package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/wl"
)

// TestBuiltinImportsShareTheClosure: with shared code, a dependency's
// import() of a module the runtime has built in costs no script its shared
// layout, as a static import of one does not ([ADR 0013], decision item 5).
// The fixture testdata/builtin-imports has four Bun scripts: two reach a
// dependency that imports node:async_hooks by import(), and two one that
// imports it statically. Built with shared code, all four name one closure
// and the build warns no shared_code_fallback. The fixture's flow of the
// four then passes in warm, fresh and host mode, and in warm and host mode
// from a bundle built with shared code: every step returns its
// AsyncLocalStorage's store, 1.
//
// [ADR 0013]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0013-shared-module-code.md#decision
func TestBuiltinImportsShareTheClosure(t *testing.T) {
	bun := bunOrSkip(t)
	root := repo(t)
	dir := filepath.Join("testdata", "builtin-imports")

	out := filepath.Join(t.TempDir(), "bundle")
	res, err := bundle.Build(context.Background(), bundle.Config{
		Folder:         filepath.Join(dir, "windmill"),
		Contract:       []byte(`{"roots": [{"flow": "f/wl_builtin/all"}]}`),
		SharedCode:     true,
		EngineVersion:  wl.Version(),
		Bun:            bun,
		Helper:         filepath.Join(root, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: filepath.Join(root, "harnesses", "bun", "runtime"),
		Out:            out,
		Work:           out + ".work",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, bundle.WarningSharedCodeFallback) {
			t.Errorf("build warning: %s", w)
		}
	}
	closure := ""
	for _, s := range []string{"dynamic_a", "dynamic_b", "static_a", "static_b"} {
		data, err := os.ReadFile(filepath.Join(out, "scripts", "bun", "f", "wl_builtin", s+".meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m bundle.ScriptMeta
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		switch {
		case m.Shared == nil || m.Shared.Closure == "":
			t.Errorf("script f/wl_builtin/%s has no closure: %+v", s, m.Shared)
		case closure == "":
			closure = m.Shared.Closure
		case m.Shared.Closure != closure:
			t.Errorf("script f/wl_builtin/%s names closure %s, want %s", s, m.Shared.Closure, closure)
		}
	}

	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	for _, c := range []struct {
		mode   string
		shared bool
	}{{conformance.ModeWarm, false}, {conformance.ModeFresh, false}, {ModeHost, false}, {conformance.ModeWarm, true}, {ModeHost, true}} {
		name := c.mode
		if c.shared {
			name += "-shared-code"
		}
		t.Run(name, func(t *testing.T) {
			fc := &flowChecker{bun: bun, repo: root, mode: c.mode, sharedCode: c.shared, js: js}
			frs := fc.runFixtures(context.Background(), []string{dir})
			if len(frs) != 1 || frs[0].err != nil || len(frs[0].runs) != 1 {
				t.Fatalf("fixture results %+v", frs)
			}
			r := frs[0].runs[0]
			switch {
			case r.unsupported != "":
				t.Fatalf("run %s is unsupported: %s", r.run, r.unsupported)
			case len(r.problems) > 0:
				t.Fatalf("run %s failed:\n  %s\nharness diagnostics:\n%s", r.run, strings.Join(r.problems, "\n  "), r.diag)
			}
		})
	}
}

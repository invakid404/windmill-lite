package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/oracle"
)

// TestCheckBuildsTheTreeRecordPushes: wltest check builds the copy of a
// fixture's sync tree that wltest record pushes (oracle.CopyLayout), not
// the tree in place. The path-alias fixture's codebase script imports a
// module through a tsconfig.json path alias. With the tsconfig.json beside
// the sync tree, the copy leaves it out: the alias does not resolve, so the
// run fails its build, as the pinned CLI, which bundles a codebase script
// with esbuild ([WR-47]), fails to push that copy to the oracle. With the
// same alias in a tsconfig.json at the root of the sync tree, the copy
// carries it, and the run passes.
//
// [WR-47]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/languages.md#wr-47
func TestCheckBuildsTheTreeRecordPushes(t *testing.T) {
	bun := bunOrSkip(t)
	root := repo(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()

	beside := filepath.Join("testdata", "path-alias")
	inTree := filepath.Join(t.TempDir(), "path-alias")
	if err := oracle.CopyTree(beside, inTree); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(inTree, "tsconfig.json")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, inTree, map[string]string{
		"windmill/tsconfig.json": `{"compilerOptions": {"baseUrl": ".", "paths": {"@wl_alias/*": ["shared/wl_alias/*"]}}}` + "\n",
	})

	for _, c := range []struct {
		name, dir string
		resolves  bool
	}{{"tsconfig beside the tree", beside, false}, {"tsconfig in the tree", inTree, true}} {
		t.Run(c.name, func(t *testing.T) {
			// What record pushes: the copy holds a tsconfig.json only when
			// the tree does.
			f, err := conformance.LoadFixture(c.dir)
			if err != nil {
				t.Fatal(err)
			}
			pushed := filepath.Join(t.TempDir(), "push")
			l, err := oracle.CopyLayout(f.SyncTree(), pushed)
			if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(pushed, filepath.FromSlash(l.Root), "tsconfig.json"))
			if (err == nil) != c.resolves {
				t.Fatalf("the pushed copy's tsconfig.json: %v, want present %v", err, c.resolves)
			}

			fc := &flowChecker{bun: bun, repo: root, mode: conformance.ModeWarm, js: js}
			frs := fc.runFixtures(context.Background(), []string{c.dir})
			if len(frs) != 1 || frs[0].err != nil || len(frs[0].runs) != 1 {
				t.Fatalf("fixture results %+v", frs)
			}
			r := frs[0].runs[0]
			switch {
			case r.unsupported != "":
				t.Fatalf("run %s is unsupported: %s", r.run, r.unsupported)
			case c.resolves && len(r.problems) > 0:
				t.Fatalf("run %s failed:\n  %s\nharness diagnostics:\n%s", r.run, strings.Join(r.problems, "\n  "), r.diag)
			case !c.resolves && (len(r.problems) == 0 || !strings.Contains(strings.Join(r.problems, "\n"), "@wl_alias/greeting")):
				t.Fatalf("run %s: problems %q, want the build's failure to resolve @wl_alias/greeting", r.run, r.problems)
			}
		})
	}
}

package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/wl"
)

// TestESModulePackagesShareTheClosure: with shared code, a codebase script
// that imports a package whose entry is an ES module keeps its shared
// layout, and each of its imports reads the package's instance in the
// script's instance of the closure, as its per-script bundle reads the
// package it inlines ([ADR 0013], decision item 3). The fixture
// testdata/esm-packages has codebase scripts of a CommonJS entry and of a
// format esm entry. They take named and namespace imports of packages with
// an .mjs entry, with "type": "module" and a .js entry, and with an .mjs
// entry that re-exports its members; of a CommonJS package; and of folder
// files whose names end .mjs and .mts. checkSharedFixture checks the build
// and runs the fixture's flows in every mode.
//
// [ADR 0013]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0013-shared-module-code.md#decision
func TestESModulePackagesShareTheClosure(t *testing.T) {
	bun := bunOrSkip(t)
	src := filepath.Join("testdata", "esm-packages")
	checkSharedFixture(t, bun, src, os.DirFS(filepath.Join(src, "packages")))
}

// TestWindmillClientSharesTheClosure is TestESModulePackagesShareTheClosure
// for the stock windmill-client 1.767.0, whose import condition names an ES
// module entry (dist/index.mjs) that re-exports its services. The scripts of
// testdata/esm-client read JobService.getJob through a named and a
// namespace import. WL_WINDMILL_CLIENT names a directory whose node_modules
// holds the client, as for the harness's Q-WA-4 test; CI fetches it by its
// npm integrity hash.
func TestWindmillClientSharesTheClosure(t *testing.T) {
	bun := bunOrSkip(t)
	client := os.Getenv("WL_WINDMILL_CLIENT")
	if client == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_WINDMILL_CLIENT is not; it names a directory whose node_modules holds windmill-client@1.767.0")
		}
		t.Skip("WL_WINDMILL_CLIENT is not set")
	}
	modules := filepath.Join(client, "node_modules")
	pkg, err := os.ReadFile(filepath.Join(modules, "windmill-client", "package.json"))
	if err != nil || !strings.Contains(string(pkg), `"version": "1.767.0"`) {
		t.Fatalf("WL_WINDMILL_CLIENT must hold windmill-client 1.767.0: %v", err)
	}
	checkSharedFixture(t, bun, filepath.Join("testdata", "esm-client"), os.DirFS(modules))
}

// checkSharedFixture copies the flow fixture at src, installs packages as
// its sync tree's node_modules, and checks it under shared code:
//
//   - The build warns no shared_code_fallback, and every script names a
//     closure: one for the scripts of each codebase entry's directory, and
//     another for each entry.
//   - Loading a script's shared-layout module, with a stand-in closure
//     registry (testdata/shared-stub-exports.mjs), assigns module.exports
//     once in a CommonJS module, at the script's own export point, which
//     then holds its main, and never in an ES module.
//   - Every run of the fixture passes in warm, fresh and host mode, and in
//     warm and host mode from a bundle built with shared code. Fresh mode
//     runs the per-script bundles, so every mode gives what the per-script
//     bundles give.
func checkSharedFixture(t *testing.T, bun, src string, packages fs.FS) {
	t.Helper()
	root := repo(t)
	dir := filepath.Join(t.TempDir(), filepath.Base(src))
	for _, p := range []string{"fixture.yaml", "runs", conformance.SyncTreeDir} {
		var err error
		if p == "fixture.yaml" {
			err = copyFile(filepath.Join(src, p), filepath.Join(dir, p))
		} else {
			err = os.CopyFS(filepath.Join(dir, p), os.DirFS(filepath.Join(src, p)))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	tree := filepath.Join(dir, conformance.SyncTreeDir)
	if err := os.CopyFS(filepath.Join(tree, "node_modules"), packages); err != nil {
		t.Fatal(err)
	}
	f, err := conformance.LoadFixture(dir)
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	for _, r := range f.Runs {
		roots = append(roots, `{"flow": "`+r.Target+`"}`)
	}

	out := filepath.Join(t.TempDir(), "bundle")
	res, err := bundle.Build(context.Background(), bundle.Config{
		Folder:         tree,
		Contract:       []byte(`{"roots": [` + strings.Join(roots, ", ") + `]}`),
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

	scripts := filepath.Join(out, "scripts", "bun")
	closures := map[string]string{} // by the script's directory
	var stubs []string
	err = filepath.WalkDir(scripts, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch {
		case strings.HasSuffix(p, ".shared.cjs"), strings.HasSuffix(p, ".shared.mjs"):
			stubs = append(stubs, p)
		case strings.HasSuffix(p, ".meta.json"):
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			var m bundle.ScriptMeta
			if err := json.Unmarshal(data, &m); err != nil {
				return err
			}
			rel, err := filepath.Rel(scripts, p)
			if err != nil {
				return err
			}
			id := filepath.ToSlash(strings.TrimSuffix(rel, ".meta.json"))
			group := path.Dir(id)
			switch {
			case m.Shared == nil || m.Shared.Closure == "":
				t.Errorf("script %s has no closure: %+v", id, m.Shared)
			case closures[group] == "":
				closures[group] = m.Shared.Closure
			case closures[group] != m.Shared.Closure:
				t.Errorf("script %s names closure %s, another script of %s %s", id, m.Shared.Closure, group, closures[group])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids := slices.Sorted(maps.Values(closures)); len(slices.Compact(ids)) != len(closures) {
		t.Errorf("two codebase entries share a closure: %v", closures)
	}
	checkStubExports(t, bun, root, stubs)

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
			if len(frs) != 1 || frs[0].err != nil || len(frs[0].runs) != len(f.Runs) {
				t.Fatalf("fixture results %+v", frs)
			}
			for _, r := range frs[0].runs {
				switch {
				case r.unsupported != "":
					t.Errorf("run %s is unsupported: %s", r.run, r.unsupported)
				case len(r.problems) > 0:
					t.Errorf("run %s failed:\n  %s\nharness diagnostics:\n%s", r.run, strings.Join(r.problems, "\n  "), r.diag)
				}
			}
		})
	}
}

// checkStubExports loads each shared-layout module with
// testdata/shared-stub-exports.mjs: a CommonJS module assigns
// module.exports once, to exports holding its main, and an ES module never
// assigns it and exports its main.
func checkStubExports(t *testing.T, bun, root string, stubs []string) {
	t.Helper()
	if len(stubs) == 0 {
		t.Fatal("the bundle has no shared-layout modules")
	}
	cmd := exec.Command(bun, "--no-install", filepath.Join(root, "cmd", "wltest", "testdata", "shared-stub-exports.mjs"))
	cmd.Args = append(cmd.Args, stubs...)
	cmd.Stderr = os.Stderr
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("shared-stub-exports.mjs: %v", err)
	}
	var got map[string]struct {
		Assigned int    `json:"assigned"`
		Main     string `json:"main"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("shared-stub-exports.mjs printed %q: %v", data, err)
	}
	for _, s := range stubs {
		g, want := got[s], 0
		if strings.HasSuffix(s, ".cjs") {
			want = 1
		}
		if g.Error != "" || g.Assigned != want || g.Main != "function" {
			t.Errorf("%s: loading it assigns module.exports %d times (want %d), main is %q, error %q", filepath.Base(s), g.Assigned, want, g.Main, g.Error)
		}
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

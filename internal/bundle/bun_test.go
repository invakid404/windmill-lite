package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reproFolder is a fixture with a dependency, workspace imports, an inline
// script and a host-provided module.
var reproFolder = map[string]string{
	"f/a/main.ts":     "import { tag } from \"fixture-pkg@1.2.3\";\nimport { h } from \"./lib\";\nimport { store } from \"/f/shared/ctx\";\ntype Ts = Date;\nexport async function main(when: Ts, n = 1) { return [tag, h(), store, Bun.env.X, when, n]; }\n",
	"f/a/lib.ts":      "export const h = () => \"lib\";\n",
	"f/shared/ctx.ts": "import { tag } from \"fixture-pkg\";\nexport const store = { tag };\n",
}

func reproConfig(t *testing.T, bun string, reg *registry, lock string) BunConfig {
	return BunConfig{
		Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"),
		Folder: writeFolder(t, reproFolder), Out: t.TempDir(), Work: t.TempDir(),
		Scripts: scriptsOf(reproFolder), Env: reg.env,
		Entries: []BunScript{
			{ID: "main", Path: "f/a/main", File: "f/a/main.ts", Lock: lock},
			{ID: "inline", Path: ImportBase("f/a/fl", "s", ""), Content: "import { h } from \"../lib\";\nexport const main = () => h();\n"},
		},
		HostModules: []HostModule{{ID: "ctx", Key: "f/shared/ctx.ts", Path: "f/shared/ctx", File: "f/shared/ctx.ts", Lock: lock}},
	}
}

// TestBuildIsReproducible builds one fixture twice, from different folder,
// work and output directories, and compares every file written.
func TestBuildIsReproducible(t *testing.T) {
	bun := bunPath(t)
	reg := newRegistry(t, map[string]map[string]string{"fixture-pkg@1.2.3": {"index.js": "export const tag = \"t\";\n"}})
	lock := reg.lock(t, bun, map[string]string{"fixture-pkg": "1.2.3"})
	var trees []map[string]string
	for range 2 {
		cfg := reproConfig(t, bun, reg, lock)
		res, err := BuildBun(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if res.BunVersion != "1.3.10" || res.NonconformantRuntime {
			t.Errorf("runtime %s, nonconformant %v", res.BunVersion, res.NonconformantRuntime)
		}
		tree := map[string]string{}
		err = filepath.WalkDir(cfg.Out, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			rel, _ := filepath.Rel(cfg.Out, p)
			tree[filepath.ToSlash(rel)] = string(b)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, tree)
	}
	want := []string{
		"scripts/bun/inline.meta.json", "scripts/bun/inline.mjs",
		"scripts/bun/main.meta.json", "scripts/bun/main.mjs",
		"scripts/bun/provided/ctx.mjs", "scripts/bun/provided/shims/ctx.mjs",
	}
	for _, p := range want {
		if trees[0][p] == "" {
			t.Errorf("%s was not written", p)
		}
	}
	if len(trees[0]) != len(want) {
		t.Errorf("wrote %d files, want %d", len(trees[0]), len(want))
	}
	for p, a := range trees[0] {
		if b := trees[1][p]; a != b {
			t.Errorf("%s differs between the two builds:\n%s\n---\n%s", p, a, b)
		}
		for _, leak := range []string{os.TempDir(), "/private/", "/var/folders"} {
			if strings.Contains(a, leak) {
				t.Errorf("%s names a build directory (%s)", p, leak)
			}
		}
	}
}

// TestMetaFile reads a written <id>.meta.json back.
func TestMetaFile(t *testing.T) {
	bun := bunPath(t)
	reg := newRegistry(t, map[string]map[string]string{"fixture-pkg@1.2.3": {"index.js": "export const tag = \"t\";\n"}})
	cfg := reproConfig(t, bun, reg, reg.lock(t, bun, map[string]string{"fixture-pkg": "1.2.3"}))
	if _, err := BuildBun(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.Out, "scripts", "bun", "main.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m ScriptMeta
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Path != "f/a/main" || m.Target != "bun" {
		t.Errorf("path %q target %q", m.Path, m.Target)
	}
	sig := m.Signatures["main"]
	if len(sig.Params) != 2 || sig.Params[0] != (Param{"when", true}) || sig.Params[1] != (Param{"n", false}) {
		t.Errorf("signature of main: %+v", sig)
	}
	if strings.Join(m.Imports, ",") != "f/a/lib" || strings.Join(m.HostModules, ",") != "f/shared/ctx.ts" || strings.Join(m.Packages, ",") != "fixture-pkg" {
		t.Errorf("imports %q host modules %q packages %q", m.Imports, m.HostModules, m.Packages)
	}
	var metafile struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(m.Metafile, &metafile); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"entry/main.ts", "src/f/a/lib.ts", "host/ctx.mjs", "node_modules/fixture-pkg/index.js"} {
		if metafile.Inputs[in] == nil {
			t.Errorf("the metafile has no input %s: %s", in, m.Metafile)
		}
	}
}

func TestBuildLocks(t *testing.T) {
	bun := bunPath(t)
	run := func(lock string) (*BunResult, error) {
		files := map[string]string{"f/a/main.ts": "export function main() { return 1; }\n"}
		return BuildBun(context.Background(), BunConfig{
			Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"),
			Folder: writeFolder(t, files), Out: t.TempDir(), Scripts: scriptsOf(files),
			Entries: []BunScript{{ID: "main", Path: "f/a/main", File: "f/a/main.ts", Lock: lock}},
		})
	}
	// WR-25: a lock part of <empty> means no dependencies; nothing installs.
	if _, err := run("{\"dependencies\":{}}\n//bun.lock\n<empty>"); err != nil {
		t.Errorf("<empty> lock: %v", err)
	}
	// A lock without its separator is invalid, with Windmill's message.
	_, err := run("{\"dependencies\":{}}")
	var be *BuildError
	if !errors.As(err, &be) || be.Problems[0].Kind != ProblemInvalidLock ||
		!strings.Contains(be.Problems[0].Message, "Invalid requirements, expected to find //bun.lock split pattern in reqs. Found: |") {
		t.Errorf("lock without a separator: %v", err)
	}
	// A lock that does not match its package.json fails the frozen install.
	_, err = run("{\"dependencies\":{\"left-pad\":\"1.0.0\"}}\n//bun.lock\n{\"lockfileVersion\":1,\"workspaces\":{\"\":{}},\"packages\":{}}")
	if !errors.As(err, &be) || be.Problems[0].Kind != ProblemInstallFailed {
		t.Errorf("mismatched lock: %v", err)
	}
}

// TestBuildRefusesAnotherBun checks the pin (ADR 0010 step 2): the helper
// refuses a request that names another Bun, and BuildBun asks for the pin.
func TestBuildRefusesAnotherBun(t *testing.T) {
	bun := bunPath(t)
	work := t.TempDir()
	cfg := BunConfig{Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), Work: work}
	req := helperRequest{Version: 1, Op: "build", RequireBun: "0.0.0", Folder: work, Work: work, Out: work,
		Scripts: map[string]string{}, Deps: map[string]string{}, HostModules: []helperHostModule{}, Entries: []helperEntry{}}
	_, err := runHelper(context.Background(), &cfg, &req)
	var he *HelperError
	if !errors.As(err, &he) || he.Kind != "runtime_mismatch" || !strings.Contains(he.Message, "Bun 0.0.0") {
		t.Errorf("another Bun: %v", err)
	}
	var r BunResult
	r.setRuntime("1.4.2")
	if !r.NonconformantRuntime {
		t.Error("a build on Bun 1.4.2 is not marked nonconformant")
	}
}

func TestBunConfigValidation(t *testing.T) {
	base := BunConfig{Bun: "bun", Helper: "main.ts", Folder: ".", Out: "out"}
	for name, mod := range map[string]func(*BunConfig){
		"no bun": func(c *BunConfig) { c.Bun = "" },
		"bad id": func(c *BunConfig) { c.Entries = []BunScript{{ID: "../x", Path: "f/x", File: "f/x.ts"}} },
		"repeated id": func(c *BunConfig) {
			c.Entries = []BunScript{{ID: "a", Path: "f/a", File: "f/a.ts"}, {ID: "a", Path: "f/b", File: "f/b.ts"}}
		},
		"file and inline": func(c *BunConfig) { c.Entries = []BunScript{{ID: "a", Path: "f/a", File: "f/a.ts", Content: "x"}} },
		"no path":         func(c *BunConfig) { c.Entries = []BunScript{{ID: "a", File: "f/a.ts"}} },
		"module neither":  func(c *BunConfig) { c.HostModules = []HostModule{{ID: "m", Key: "k"}} },
		"module both": func(c *BunConfig) {
			c.HostModules = []HostModule{{ID: "m", Key: "k", Package: "p", Path: "f/m", File: "f/m.ts"}}
		},
		"case-folded id": func(c *BunConfig) {
			c.Entries = []BunScript{{ID: "a", Path: "f/a", File: "f/a.ts"}, {ID: "A", Path: "f/b", File: "f/b.ts"}}
		},
		"path, no file":    func(c *BunConfig) { c.HostModules = []HostModule{{ID: "m", Key: "k", Path: "f/m"}} },
		"package and path": func(c *BunConfig) { c.HostModules = []HostModule{{ID: "m", Key: "k", Package: "p", Path: "f/m"}} },
		"file not the script": func(c *BunConfig) {
			c.Scripts = map[string]string{"f/m": "f/m.ts"}
			c.HostModules = []HostModule{{ID: "m", Key: "k", Path: "f/m", File: "f/other.ts"}}
		},
		"repeated key": func(c *BunConfig) {
			c.HostModules = []HostModule{{ID: "m", Key: "k", Package: "p"}, {ID: "n", Key: "k", Package: "q"}}
		},
	} {
		c := base
		mod(&c)
		if err := c.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := base.validate(); err != nil {
		t.Errorf("the base config: %v", err)
	}
}

// TestBuildIgnoresNodeEnv checks that the builder's NODE_ENV, which
// Bun.build inlines, does not reach a bundle.
func TestBuildIgnoresNodeEnv(t *testing.T) {
	bun := bunPath(t)
	files := map[string]string{"f/a/main.ts": "export function main() { return process.env.NODE_ENV; }\n"}
	out := t.TempDir()
	_, err := BuildBun(context.Background(), BunConfig{
		Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"),
		Folder: writeFolder(t, files), Out: out, Scripts: scriptsOf(files),
		Env:     append(os.Environ(), "NODE_ENV=staging-from-the-builder"),
		Entries: []BunScript{{ID: "main", Path: "f/a/main", File: "f/a/main.ts"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "scripts", "bun", "main.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "staging-from-the-builder") || !strings.Contains(string(b), `"development"`) {
		t.Errorf("the bundle depends on the builder's NODE_ENV:\n%s", b)
	}
}

// TestBuildRefusesAncestorPackages builds in a work directory below a
// node_modules holding a package no lock installed: the bundle must not
// inline it.
func TestBuildRefusesAncestorPackages(t *testing.T) {
	bun := bunPath(t)
	project := t.TempDir()
	pkg := filepath.Join(project, "node_modules", "stray-pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"stray-pkg","version":"1.0.0","main":"index.js"}`), 0o644)
	os.WriteFile(filepath.Join(pkg, "index.js"), []byte("export const stray = 1;\n"), 0o644)
	work := filepath.Join(project, "work")
	files := map[string]string{"f/a/main.ts": "import { stray } from \"stray-pkg\";\nexport function main() { return stray; }\n"}
	_, err := BuildBun(context.Background(), BunConfig{
		Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"),
		Folder: writeFolder(t, files), Out: t.TempDir(), Work: work, Scripts: scriptsOf(files),
		Entries: []BunScript{{ID: "main", Path: "f/a/main", File: "f/a/main.ts"}},
	})
	var be *BuildError
	if !errors.As(err, &be) || be.Problems[0].Kind != ProblemBundleFailed || !strings.Contains(be.Problems[0].Message, "outside its dependency set") {
		t.Errorf("a package from an ancestor node_modules: %v", err)
	}
}

// TestHelperRefusesPathsOutsideTheFolder sends the helper a script file
// that leaves the folder, which BuildBun's callers never should.
func TestHelperRefusesPathsOutsideTheFolder(t *testing.T) {
	bun := bunPath(t)
	work := t.TempDir()
	cfg := BunConfig{Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), Work: work}
	for _, file := range []string{"../escape.ts", "/etc/hosts"} {
		req := helperRequest{Version: 1, Op: "build", Folder: work, Work: work, Out: work,
			Scripts: map[string]string{}, Deps: map[string]string{}, HostModules: []helperHostModule{},
			Entries: []helperEntry{{ID: "x", Path: "f/x", File: file}}}
		resp, err := runHelper(context.Background(), &cfg, &req)
		if err != nil {
			t.Fatal(err)
		}
		if e := resp.Entries["x"].Error; e == nil || e.Kind != "invalid_request" || !strings.Contains(e.Message, "leaves the folder") {
			t.Errorf("%s: %+v", file, e)
		}
	}
	req := helperRequest{Version: 1, Op: "build", Folder: work, Work: work, Out: work,
		Scripts: map[string]string{}, Deps: map[string]string{"bad key!": "x"}, HostModules: []helperHostModule{}, Entries: []helperEntry{}}
	_, err := runHelper(context.Background(), &cfg, &req)
	var he *HelperError
	if !errors.As(err, &he) || he.Kind != "invalid_request" {
		t.Errorf("a bad dependency-set key: %v", err)
	}
}

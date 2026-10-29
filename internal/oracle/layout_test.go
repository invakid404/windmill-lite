package oracle

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// writeFiles writes files, by slash path below dir, and returns dir.
func writeFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for p, s := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// outsideBase is a checkout in which the sync tree is top/repo/windmill,
// with directories beside it and above it.
var outsideBase = map[string]string{
	"top/repo/windmill/f/cb/a.ts":          okScript,
	"top/repo/windmill/f/cb/lib/x.ts":      "export const x = 1;\n",
	"top/repo/shared/lib/label.ts":         "export const label = 'l';\n",
	"top/repo/shared/lib/assets/child.mjs": "export const child = 1;\n",
	"top/repo/shared-x/a.txt":              "a\n",
	"top/repo/assets/b.txt":                "b\n",
	"top/inject/shim.ts":                   "export const shim = 1;\n",
	"top/repo/fixture.yaml":                "id: x\n",
}

// TestPlanLayout lays a copy out from the paths the codebase entries name:
// paths inside the tree, absolute or missing ones are not carried, a path
// below another is carried with it, a spelling that leaves the tree and
// comes back in is inside it, and the copied tree sits below as many of
// its parents' names as the furthest path climbs.
func TestPlanLayout(t *testing.T) {
	abs := t.TempDir()
	cases := []struct {
		name, codebases string
		root            string
		outside         []string
	}{
		{"no codebases", "", ".", nil},
		{"inside", "  - relative_path: f/cb\n    assets: [{from: f/cb/lib/x.ts, to: x.ts}]\n", ".", nil},
		{"come back in", "  - relative_path: ../windmill/f/cb\n    inject: [./f/../f/cb/a.ts]\n", ".", nil},
		{"relative_path and its asset", "  - relative_path: ../shared\n    assets: [{from: ../shared/lib/assets/child.mjs, to: child.mjs}]\n", "windmill", []string{"../shared"}},
		{"asset alone", "  - relative_path: f/cb\n    assets: [{from: ../assets/b.txt, to: b.txt}]\n", "windmill", []string{"../assets/b.txt"}},
		{"a name that starts like another", "  - relative_path: ../shared\n    assets: [{from: ../shared-x/a.txt, to: a.txt}, {from: ../shared/lib/label.ts, to: l.ts}]\n", "windmill", []string{"../shared", "../shared-x/a.txt"}},
		{"twice", "  - relative_path: ../shared\n  - relative_path: ../shared/\n", "windmill", []string{"../shared"}},
		{"two levels up", "  - relative_path: ../shared\n    inject: [../../inject/shim.ts]\n", "repo/windmill", []string{"../../inject/shim.ts", "../shared"}},
		{"the tree's parent", "  - relative_path: ..\n    assets: [{from: ../shared/lib/label.ts, to: l.ts}]\n", "windmill", []string{".."}},
		{"climbs past the tree's parent", "  - relative_path: ..\n    inject: [../../inject/shim.ts]\n", "repo/windmill", []string{"..", "../../inject/shim.ts"}},
		{"through a file", "  - relative_path: ../fixture.yaml/x\n", ".", nil},
		{"missing and absolute", "  - relative_path: ../nowhere\n    assets: [{from: " + filepath.ToSlash(filepath.Join(abs, "x")) + ", to: x}]\n", ".", nil},
		{"wrong types", "  - relative_path: 3\n    assets: ../shared\n    inject: ../shared\n  - x\n", ".", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"top/repo/windmill/wmill.yaml": "defaultTs: bun\n"}
			if c.codebases != "" {
				files["top/repo/windmill/wmill.yaml"] += "codebases:\n" + c.codebases
			}
			for p, s := range outsideBase {
				files[p] = s
			}
			dir := writeFiles(t, t.TempDir(), files)
			l, err := PlanLayout(filepath.Join(dir, "top/repo/windmill"))
			if err != nil {
				t.Fatal(err)
			}
			if l.Root != c.root || strings.Join(l.Outside, " ") != strings.Join(c.outside, " ") {
				t.Fatalf("got root %q outside %q, want %q %q", l.Root, l.Outside, c.root, c.outside)
			}
		})
	}
	// Through a symbolic link to the tree, ".." climbs from the tree, as
	// it does from the CLI's working directory.
	dir := writeFiles(t, t.TempDir(), outsideBase)
	if err := os.WriteFile(filepath.Join(dir, "top/repo/windmill/wmill.yaml"), []byte("codebases:\n  - relative_path: ../shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(dir, "top/repo/windmill"), link); err != nil {
		t.Fatal(err)
	}
	if l, err := PlanLayout(link); err != nil || l.Root != "windmill" || strings.Join(l.Outside, " ") != "../shared" {
		t.Fatalf("through a link: got %+v %v", l, err)
	}
	if l, err := CopyLayout(link, filepath.Join(t.TempDir(), "copy")); err != nil || strings.Join(l.Outside, " ") != "../shared" {
		t.Fatalf("copy through a link: got %+v %v", l, err)
	}
	// A tree without wmill.yaml, or whose wmill.yaml does not parse, names
	// no paths.
	for _, files := range []map[string]string{{"f/a.ts": okScript}, {"wmill.yaml": "codebases: [\n"}} {
		l, err := PlanLayout(writeFiles(t, t.TempDir(), files))
		if err != nil || l.Root != "." || l.Outside != nil {
			t.Fatalf("%v: got %+v %v", files, l, err)
		}
	}
}

// TestCopyLayout copies a tree whose codebase entries reach outside it:
// wmill.yaml is unchanged, every path it names reaches from the copied
// tree a copy of what it reaches from the original, the copy holds nothing
// else from outside, and a carried directory that holds the tree leaves
// the tree in the copy as it is.
func TestCopyLayout(t *testing.T) {
	wmill := "defaultTs: bun\ncodebases:\n  - relative_path: ../shared\n    includes: [\"f/cb/**\"]\n    assets: [{from: ../shared/lib/assets/child.mjs, to: child.mjs}, {from: ../assets/b.txt, to: b.txt}]\n    inject: [../../inject/shim.ts]\n"
	files := map[string]string{"top/repo/windmill/wmill.yaml": wmill}
	for p, s := range outsideBase {
		files[p] = s
	}
	dir := writeFiles(t, t.TempDir(), files)
	src := filepath.Join(dir, "top/repo/windmill")
	dst := filepath.Join(t.TempDir(), "copy")
	l, err := CopyLayout(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if l.Root != "repo/windmill" || strings.Join(l.Outside, " ") != "../../inject/shim.ts ../assets/b.txt ../shared" {
		t.Fatalf("layout %+v", l)
	}
	tree := filepath.Join(dst, "repo/windmill")
	if got, _ := os.ReadFile(filepath.Join(tree, "wmill.yaml")); string(got) != wmill {
		t.Fatalf("wmill.yaml changed:\n%s", got)
	}
	doc, err := yamlx.Parse([]byte(wmill))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range codebasePaths(doc) {
		from, to := filepath.Join(src, filepath.FromSlash(p)), filepath.Join(tree, filepath.FromSlash(p))
		if info, err := os.Stat(from); err != nil || info.IsDir() {
			if got, want := listFiles(t, to), listFiles(t, from); err != nil || got != want {
				t.Errorf("%s holds:\n%s\nwant\n%s (%v)", p, got, want, err)
			}
			continue
		}
		want, _ := os.ReadFile(from)
		if got, err := os.ReadFile(to); err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %q %v, want %q", p, got, err, want)
		}
	}
	want := strings.Join([]string{
		"f inject/shim.ts",
		"f repo/assets/b.txt",
		"f repo/shared/lib/assets/child.mjs",
		"f repo/shared/lib/label.ts",
		"f repo/windmill/f/cb/a.ts",
		"f repo/windmill/f/cb/lib/x.ts",
		"f repo/windmill/wmill.yaml",
	}, "\n")
	if got := listFiles(t, dst); got != want {
		t.Fatalf("the copy holds:\n%s\nwant\n%s", got, want)
	}

	// relative_path .. holds the tree: the copied tree is the tree, with
	// the rest of its parent beside it.
	files["top/repo/windmill/wmill.yaml"] = "defaultTs: bun\ncodebases:\n  - relative_path: ..\n"
	dir = writeFiles(t, t.TempDir(), files)
	src = filepath.Join(dir, "top/repo/windmill")
	dst = filepath.Join(t.TempDir(), "copy")
	if l, err = CopyLayout(src, dst); err != nil || l.Root != "windmill" {
		t.Fatalf("got %+v %v", l, err)
	}
	if got, want := listFiles(t, filepath.Join(dst, "windmill")), listFiles(t, src); got != want {
		t.Fatalf("the copied tree holds:\n%s\nwant\n%s", got, want)
	}
	if got, want := listFiles(t, dst), listFiles(t, filepath.Join(dir, "top/repo")); got != want {
		t.Fatalf("the copy holds:\n%s\nwant\n%s", got, want)
	}
}

// TestCopyLayoutRefuses refuses an output that exists or lies inside a
// path the copy carries, which it would walk into, before writing
// anything, and removes what it made when the copy fails.
func TestCopyLayoutRefuses(t *testing.T) {
	files := map[string]string{"top/repo/windmill/wmill.yaml": "codebases:\n  - relative_path: ../shared\n"}
	for p, s := range outsideBase {
		files[p] = s
	}
	dir := writeFiles(t, t.TempDir(), files)
	src := filepath.Join(dir, "top/repo/windmill")
	before := listFiles(t, dir)
	for dst, msg := range map[string]string{
		filepath.Join(dir, "top/repo/shared"):          "already exists",
		filepath.Join(dir, "top/repo/shared/lib/copy"): "is inside the carried path",
		filepath.Join(src, "copy"):                     "is inside the sync tree",
	} {
		if _, err := CopyLayout(src, dst); err == nil || !strings.Contains(err.Error(), msg) {
			t.Fatalf("%s: got %v, want %q", dst, err, msg)
		}
		if _, err := StubTree(src, dst); err == nil || !strings.Contains(err.Error(), msg) {
			t.Fatalf("StubTree %s: got %v, want %q", dst, err, msg)
		}
		if after := listFiles(t, dir); after != before {
			t.Fatalf("%s: the checkout changed:\n%s\nwas\n%s", dst, after, before)
		}
	}

	// A symbolic link in a carried directory fails the copy, as one in the
	// tree does; the copy and the parents it made are removed.
	if err := os.Symlink("label.ts", filepath.Join(dir, "top/repo/shared/lib/link.ts")); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(t.TempDir(), "new")
	if _, err := CopyLayout(src, filepath.Join(parent, "a", "out")); err == nil || !strings.Contains(err.Error(), "not a regular file or directory") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(parent); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a failed copy left %s: %v", parent, err)
	}
}

// TestStubTreeCarriesTheOutsidePaths stubs the stub/outside fixture,
// whose codebase entry's relative_path and asset lie outside its tree: the
// stubbed tree is at Root with the shared directory beside it unchanged,
// so the unchanged wmill.yaml reaches the asset from the stubbed tree.
func TestStubTreeCarriesTheOutsidePaths(t *testing.T) {
	src := "../../conformance/flows/stub/outside/windmill"
	dst := filepath.Join(t.TempDir(), "stubbed")
	rep, err := StubTree(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Root != "windmill" || strings.Join(rep.Outside, " ") != "../shared" || strings.Join(rep.Files, " ") != "f/wl_stub_out/tag.ts" {
		t.Fatalf("report %+v", rep)
	}
	if got, want := listFiles(t, filepath.Join(dst, "shared")), listFiles(t, filepath.Join(src, "../shared")); got != want {
		t.Fatalf("the carried directory holds:\n%s\nwant\n%s", got, want)
	}
	tree := filepath.Join(dst, "windmill")
	for _, p := range []string{"wmill.yaml", "../shared/lib/assets/child.mjs", "../shared/lib/label.ts"} {
		want, _ := os.ReadFile(filepath.Join(src, p))
		if got, err := os.ReadFile(filepath.Join(tree, p)); err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %q %v", p, got, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(tree, "f/wl_stub_out/tag.ts")); !bytes.Contains(got, []byte("__wlReplay(Array.from(arguments))")) || bytes.Contains(got, []byte("shared/lib")) {
		t.Fatalf("not a stub:\n%s", got)
	}
}

// listFiles lists the regular files below dir, "f <slash path>", sorted.
func listFiles(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, "f "+filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

// codebasePathsResolve is the fake CLI's check of its working directory,
// a sync tree's root: the CLI digests each codebase entry's relative_path
// and reads its assets' from and inject files from there (WR-47, WR-49),
// so a path that does not resolve fails the push.
func codebasePathsResolve() error {
	data, err := os.ReadFile("wmill.yaml")
	if err != nil {
		return nil
	}
	doc, err := yamlx.Parse(data)
	if err != nil {
		return nil
	}
	for _, p := range codebasePaths(doc) {
		if _, err := os.Stat(filepath.FromSlash(p)); err != nil {
			return err
		}
	}
	return nil
}

// TestSessionPushesACopyWithTheOutsidePaths records the smoke fixture
// with a codebase entry whose relative_path and asset lie outside its
// tree: the session pushes from a copy that carries them, which the fake
// CLI requires as the real one does, and the deployment lists them.
// Without the shared directory beside the tree, the push fails.
func TestSessionPushesACopyWithTheOutsidePaths(t *testing.T) {
	for _, carried := range []bool{true, false} {
		rig := newRig(t)
		wmill := filepath.Join(rig.fixture, "windmill", "wmill.yaml")
		data, err := os.ReadFile(wmill)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, "codebases:\n  - relative_path: ../shared\n    includes: [\"f/wl_cb/**\"]\n    assets: [{from: ../shared/lib/a.txt, to: a.txt}]\n"...)
		if err := os.WriteFile(wmill, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if carried {
			writeFiles(t, rig.fixture, map[string]string{"shared/lib/a.txt": "a\n"})
		}
		rep, err := rig.session(20*GiB).Run(context.Background(), []*conformance.Fixture{rig.load(t)})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rep.Runs {
			switch {
			case carried && r.Outcome != OutcomeNew:
				t.Fatalf("run %s: %s %s", r.Run, r.Outcome, r.Error)
			case !carried && (r.Outcome != OutcomeFailed || !strings.Contains(r.Error, "fake CLI: stat ../shared")):
				t.Fatalf("without the shared directory, run %s: %s %s", r.Run, r.Outcome, r.Error)
			}
		}
		if !carried {
			continue
		}
		for name, dep := range rep.Session.Deployments {
			if strings.Join(dep.Outside, " ") != "../shared" {
				t.Fatalf("deployment %s carried %q", name, dep.Outside)
			}
		}
	}
}

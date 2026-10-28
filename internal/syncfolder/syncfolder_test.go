package syncfolder

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/invakid404/windmill-lite/internal/openflow"
)

const mainSrc = "export async function main() {\n  return 1;\n}\n"

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func kindOf(err error) ErrorKind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

func mapFolder(t *testing.T, files map[string]string) *Folder {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, text := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(text)}
	}
	f, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// spec: SY-1
func TestFind(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".git/HEAD":           "",
		"wmill.yaml":          "defaultTs: bun\n",
		"f/a/b/x.ts":          mainSrc,
		"nested/wmill.yaml/x": "a directory named wmill.yaml is not the marker\n",
		"sub/.git":            "gitdir: elsewhere\n",
	})
	for _, c := range []struct {
		start, root string
		kind        ErrorKind
	}{
		{".", ".", ""},
		{"f/a/b", ".", ""},
		{"nested", ".", ""},
		{"sub", "", ErrNoConfig}, // a .git file is a git root too
	} {
		root, err := Find(filepath.Join(dir, c.start))
		if c.kind != "" {
			if kindOf(err) != c.kind {
				t.Errorf("Find(%s): %v, want %s", c.start, err, c.kind)
			}
			continue
		}
		if err != nil || root != filepath.Join(dir, c.root) {
			t.Errorf("Find(%s) = %s, %v; want %s", c.start, root, err, c.root)
		}
	}
}

func TestLoadFromFS(t *testing.T) {
	f := mapFolder(t, map[string]string{"wmill.yaml": "defaultTs: deno\n", "f/a/x.ts": mainSrc})
	defer f.Close()
	if f.Root != "" {
		t.Errorf("Root = %q, want empty for Load", f.Root)
	}
	s, err := f.Script("f/a/x")
	if err != nil || s.Language != openflow.LangDeno {
		t.Fatalf("Script = %+v, %v", s, err)
	}
	if _, err := Load(fstest.MapFS{}); kindOf(err) != ErrNoConfig {
		t.Errorf("Load without wmill.yaml: %v", err)
	}
}

func TestConfigErrors(t *testing.T) {
	for _, c := range []struct {
		cfg, contains string
		kind          ErrorKind
	}{
		{"includes: [f/**, 3]\n", "includes entry must be a string, not the number 3", ErrConfig},
		{"skipScripts: 'yes'\n", "skipScripts must be a boolean", ErrConfig},
		{"codebases: {a: 1}\n", "codebases must be a list", ErrConfig},
		{"codebases: [1]\n", "codebases[0] must be a mapping", ErrConfig},
		{"codebases: [{relative_path: 1}]\n", "relative_path must be a string", ErrConfig},
		{"codebases: [{includes: ['f/{a}']}]\n", "includes: unsupported glob", ErrGlob},
		{"workspaces: [a]\n", "workspaces must be a mapping", ErrConfig},
		{"a: [\n", "yaml_syntax", ErrYAML},
		{"a: 1\na: 2\n", "wmill.yaml:2", ErrYAML},
	} {
		_, err := Load(fstest.MapFS{"wmill.yaml": {Data: []byte(c.cfg)}})
		if kindOf(err) != c.kind || !strings.Contains(err.Error(), c.contains) {
			t.Errorf("%q: %v, want %s containing %q", c.cfg, err, c.kind, c.contains)
		}
	}
	f := mapFolder(t, map[string]string{"wmill.yaml": "defaultTs: bun\noverrides: {}\nincludes: ~\n"})
	if f.Config.Includes != nil {
		t.Errorf("includes: ~ is %q, want absent", f.Config.Includes)
	}
}

// spec: SY-1, SY-7 (windmill-lite policy: reads stay inside the root)
func TestSymlinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "root")
	writeFiles(t, base, map[string]string{
		"root/wmill.yaml":         "defaultTs: bun\n",
		"root/shared/x.ts":        mainSrc,
		"root/f/real/y.ts":        mainSrc,
		"root/f/x.flow/flow.yaml": "value:\n  modules:\n    - id: a\n      value: {type: rawscript, language: bun, content: '!inline a.ts', input_transforms: {}}\n",
		"outside/secret.ts":       "export const secret = 1;\n",
		"outside/dir/z.ts":        mainSrc,
	})
	symlink(t, "../../shared/x.ts", filepath.Join(dir, "f/a/x.ts"))
	symlink(t, filepath.Join(base, "outside/secret.ts"), filepath.Join(dir, "f/a/leak.ts"))
	symlink(t, filepath.Join(base, "outside/secret.ts"), filepath.Join(dir, "f/x.flow/a.ts"))
	symlink(t, "real", filepath.Join(dir, "f/linked"))
	symlink(t, filepath.Join(base, "outside/dir"), filepath.Join(dir, "f/out"))

	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if s, err := f.Script("f/a/x"); err != nil || s.Content != mainSrc {
		t.Errorf("a file link inside the root: %v", err)
	}
	if _, err := f.Script("f/a/leak"); kindOf(err) != ErrIO {
		t.Errorf("a file link outside the root: %v, want an io error", err)
	}
	if _, err := f.Flow("f/x"); kindOf(err) != ErrIO {
		t.Errorf("an inline file linked outside the root: %v, want an io error", err)
	}
	if got := f.Paths(KindScript); !slices.Equal(got, []string{"f/a/leak", "f/a/x", "f/real/y"}) {
		t.Errorf("Paths = %q: directory links must not be followed", got)
	}
	for _, w := range []string{"f/linked is a symbolic link to a directory", "f/out is a symbolic link that cannot be followed", "f/a/leak.ts is a symbolic link that cannot be followed"} {
		if !slices.ContainsFunc(f.Warnings, func(s string) bool { return strings.Contains(s, w) }) {
			t.Errorf("no warning %q: %q", w, f.Warnings)
		}
	}
}

func TestErrorText(t *testing.T) {
	e := &Error{Kind: ErrFlow, Step: "flow f/x step 'a'", Item: "flow f/y", File: "f/y.flow/flow.yaml", Line: 3, Column: 7, Msg: "boom"}
	if got, want := e.Error(), "flow f/x step 'a': flow f/y: f/y.flow/flow.yaml:3:7: boom"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	inner := errors.New("inner")
	if !errors.Is(&Error{Err: inner}, inner) {
		t.Error("Unwrap does not reach the wrapped error")
	}
	f := mapFolder(t, map[string]string{"wmill.yaml": "", "f/x.flow/flow.yaml": "value: {modules: [{id: a, value: {type: rawscript, language: cobol, content: x}}]}\n"})
	_, err := f.Flow("f/x")
	var oe *openflow.Error
	if !errors.As(err, &oe) || !strings.Contains(err.Error(), "flow f/x: f/x.flow/flow.yaml: ") {
		t.Errorf("a decode error: %v", err)
	}
}

func TestCheckJoinsErrors(t *testing.T) {
	f := mapFolder(t, map[string]string{
		"wmill.yaml":          "defaultTs: bun\n",
		"f/a/ok.ts":           mainSrc,
		"f/a/bad.sql":         "select 1;\n",
		"f/a/r.resource.yaml": "value: [\n",
		"f/x.flow/flow.yaml":  "value: {modules: []}\n",
	})
	err := f.Check()
	var kinds []ErrorKind
	for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
		kinds = append(kinds, kindOf(e))
	}
	if !slices.Equal(kinds, []ErrorKind{ErrLanguage, ErrYAML}) {
		t.Errorf("Check: kinds %q from %v", kinds, err)
	}
}

func TestTargetOfOtherSteps(t *testing.T) {
	f := mapFolder(t, map[string]string{"wmill.yaml": "", "f/x.flow/flow.yaml": "value: {modules: [{id: a, value: {type: identity}}]}\n"})
	fl, err := f.Flow("f/x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Target("f/x", &fl.Document.Value.Modules[0]); err == nil || !strings.Contains(err.Error(), "identity step has no by-path target") {
		t.Errorf("Target of an identity step: %v", err)
	}
}

// spec: SY-V1
func TestLockDirectiveWhitespace(t *testing.T) {
	for _, c := range []struct {
		lock string
		name string
		ok   bool
	}{
		{"!inline a.lock", "a.lock", true},
		{"\ufeff!inline a.lock", "a.lock", true}, // JavaScript's trimStart removes U+FEFF
		{"\u00a0!inline a.lock", "a.lock", true},
		{"\u3000!inline a.lock", "a.lock", true},
		{"\u0085!inline a.lock", "", false}, // and keeps U+0085
		{"\t!inline a.lock", "a.lock", true},
		{" !inline a.lock", "!inline", true},
		{"  !inline a.lock", "", true},
		{"!inline\ta.lock", "", false},
		{"!inline", "", false},
		{"lock text", "", false},
	} {
		name, ok := inlineLockFile(c.lock)
		if name != c.name || ok != c.ok {
			t.Errorf("inlineLockFile(%q) = %q, %v; want %q, %v", c.lock, name, ok, c.name, c.ok)
		}
	}
}

func TestDataDuplicatesAndJSONMode(t *testing.T) {
	f := mapFolder(t, map[string]string{
		"wmill.yaml":               "",
		"f/a/r.resource.yaml":      "value: 1\n",
		"f/a/r.resource.json":      "{\"value\": 1}\n",
		"f/a/j.variable.json":      "{\"value\": \"v\"}\n",
		"t.resource-type.json":     "{}\n",
		"f/a/r.prod.resource.yaml": "value: 2\n",
	})
	for _, c := range []struct {
		kind ItemKind
		path string
		want ErrorKind
	}{
		{KindResource, "f/a/r", ErrDuplicate},
		{KindVariable, "f/a/j", ErrUnsupported},
		{KindResourceType, "t", ErrUnsupported},
		{KindVariable, "f/a/none", ErrNotInFolder},
	} {
		if _, err := f.read(c.kind, c.path); kindOf(err) != c.want {
			t.Errorf("%s %s: %v, want %s", c.kind, c.path, err, c.want)
		}
	}
	// Without a declared workspace, f/a/r.prod is an ordinary resource.
	if r, err := f.Resource("f/a/r.prod"); err != nil || string(r.Value) != "2" {
		t.Errorf("f/a/r.prod: %v", err)
	}
}

// Reads go to the files each time, and may run concurrently.
func TestConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"wmill.yaml": "defaultTs: bun\n"}
	for _, n := range []string{"a", "b", "c", "d"} {
		files["f/s/"+n+".ts"] = mainSrc
		files["f/"+n+".flow/flow.yaml"] = "value:\n  modules:\n    - id: m\n      value: {type: rawscript, language: bun, content: '!inline m.ts', input_transforms: {}}\n"
		files["f/"+n+".flow/m.ts"] = "// " + n + "\n"
	}
	writeFiles(t, dir, files)
	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, n := range []string{"a", "b", "c", "d"} {
				if _, err := f.Script("f/s/" + n); err != nil {
					t.Error(err)
				}
				fl, err := f.Flow("f/" + n)
				if err != nil {
					t.Error(err)
					continue
				}
				if got := fl.Document.Value.Modules[0].Value.(*openflow.RawScript).Content; got != "// "+n+"\n" {
					t.Errorf("flow f/%s content %q", n, got)
				}
			}
			if err := f.Check(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	writeFiles(t, dir, map[string]string{"f/s/a.ts": "// changed\n" + mainSrc})
	if s, err := f.Script("f/s/a"); err != nil || !strings.HasPrefix(s.Content, "// changed") {
		t.Errorf("a read after a change: %v", err)
	}
}

func TestProblemsAreWarnings(t *testing.T) {
	f := mapFolder(t, map[string]string{"wmill.yaml": "defaultTs: bun\n", "f/a/x.ts": mainSrc, "f/a/x.py": "x\n", "f/my.d/y.ts": mainSrc})
	for _, w := range []string{"script f/a/x: f/a/x.py: 2 files define this script", "script f/my: f/my.d/y.ts: the CLI maps this file to f/my"} {
		if !slices.ContainsFunc(f.Warnings, func(s string) bool { return strings.Contains(s, w) }) {
			t.Errorf("no warning %q in %q", w, f.Warnings)
		}
	}
}

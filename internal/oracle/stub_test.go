package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// TestStubTreeOfTheFixtures stubs the sync tree of every stub fixture:
// each Bun script and inline script is replaced, nothing else changes, and
// no stub still names the systems the originals call (the .invalid hosts).
func TestStubTreeOfTheFixtures(t *testing.T) {
	want := map[string][]string{
		"collapsed":  {"f/wl_stub/collapsed.flow/book.ts", "f/wl_stub/total.ts"},
		"nested":     {"f/wl_stub/nested.flow/read.ts", "f/wl_stub/nested.flow/write.ts"},
		"retry_loop": {"f/wl_stub/retry_loop.flow/call.ts"},
		"scopes":     {"f/wl_stub/members.flow/lookup.ts", "f/wl_stub/notify.flow/post.ts"},
	}
	for name, files := range want {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join("../../conformance/flows/stub", name, "windmill")
			dst := filepath.Join(t.TempDir(), "stubbed")
			rep, err := StubTree(src, dst)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(rep.Files, " ") != strings.Join(files, " ") {
				t.Fatalf("stubbed %q, want %q", rep.Files, files)
			}
			stubbed := map[string]bool{}
			for _, f := range files {
				stubbed[f] = true
			}
			err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(src, p)
				orig, _ := os.ReadFile(p)
				got, err := os.ReadFile(filepath.Join(dst, rel))
				if err != nil {
					return err
				}
				switch {
				case stubbed[filepath.ToSlash(rel)]:
					if !bytes.Contains(got, []byte("__wlReplay(Array.from(arguments))")) || bytes.Contains(got, []byte(".invalid")) {
						t.Errorf("%s is not a stub:\n%s", rel, got)
					}
				case !bytes.Equal(orig, got):
					t.Errorf("%s changed", rel)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// writeStubTree writes a sync tree of the given files below a new directory.
func writeStubTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["wmill.yaml"] = "defaultTs: bun\nincludes: [\"f/**\"]\n"
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

const okScript = "export async function main(x: number) { return x }\n"

func flowYAML(modules string) string {
	return "summary: s\nvalue:\n  modules:\n" + modules + "schema: {type: object, properties: {}}\n"
}

// TestStubTreeRefuses refuses every tree in which an executed script would
// keep its real body, naming the file or step and the reason.
func TestStubTreeRefuses(t *testing.T) {
	rawscript := func(id, content, lang string) string {
		return "    - id: " + id + "\n      value:\n        type: rawscript\n        content: " + content + "\n        language: " + lang + "\n        input_transforms: {}\n"
	}
	cases := []struct {
		name   string
		files  map[string]string
		reason string
	}{
		{"a Python script", map[string]string{"f/x/a.py": "def main(x):\n    return x\n"}, "language is python3"},
		{"a native Bun script", map[string]string{"f/x/a.ts": "//native\n" + okScript}, "language is bunnative"},
		{"a native inline rawscript", map[string]string{"f/x/w.flow/flow.yaml": flowYAML(rawscript("a", "'!inline a.ts'", "bun")), "f/x/w.flow/a.ts": "//native\n" + okScript}, "native annotation"},
		{"an arrow-function main", map[string]string{"f/x/a.ts": "export const main = async (x: number) => x\n"}, "no exported function declaration main"},
		{"a rawscript written in flow.yaml", map[string]string{"f/x/w.flow/flow.yaml": flowYAML(rawscript("a", "'export async function main() {}'", "bun"))}, "written in flow.yaml"},
		{"an inline Python rawscript", map[string]string{"f/x/w.flow/flow.yaml": flowYAML(rawscript("a", "'!inline a.py'", "python3")), "f/x/w.flow/a.py": "def main():\n  pass\n"}, "language is python3"},
		{"an inline rawscript that cannot be stubbed", map[string]string{"f/x/w.flow/flow.yaml": flowYAML(rawscript("a", "'!inline a.ts'", "bun")), "f/x/w.flow/a.ts": "export default function main() {}\n"}, "default export"},
		{"a hub script step", map[string]string{"f/x/w.flow/flow.yaml": flowYAML("    - id: h\n      value:\n        type: script\n        path: hub/1/x\n        input_transforms: {}\n")}, "hub path"},
		{"a script step outside the tree", map[string]string{"f/x/w.flow/flow.yaml": flowYAML("    - id: s\n      value:\n        type: script\n        path: f/elsewhere/s\n        input_transforms: {}\n")}, "f/elsewhere/s"},
		{"a subflow outside the tree", map[string]string{"f/x/w.flow/flow.yaml": flowYAML("    - id: s\n      value:\n        type: flow\n        path: f/elsewhere/w\n        input_transforms: {}\n")}, "f/elsewhere/w"},
		{"a script step pinned by hash", map[string]string{"f/x/a.ts": okScript, "f/x/w.flow/flow.yaml": flowYAML("    - id: s\n      value:\n        type: script\n        path: f/x/a\n        hash: '0000000000000001'\n        input_transforms: {}\n")}, "pinned by hash"},
		{"a flowscript step", map[string]string{"f/x/w.flow/flow.yaml": flowYAML("    - id: s\n      value:\n        type: flowscript\n        id: 42\n        input_transforms: {}\n")}, "flowscript step"},
		{"a retried loop", map[string]string{"f/x/w.flow/flow.yaml": flowYAML("    - id: l\n      value:\n        type: forloopflow\n        iterator: {type: static, value: [1]}\n        modules: []\n      retry: {constant: {attempts: 1, seconds: 0}}\n")}, "a retry on a subflow, loop or branch step"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := writeStubTree(t, c.files)
			dst := filepath.Join(t.TempDir(), "out")
			_, err := StubTree(src, dst)
			var r *Refusal
			if !errors.As(err, &r) || r.Code != "stub_unsupported" || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("got %v, want stub_unsupported with %q", err, c.reason)
			}
			if _, err := os.Stat(dst); err == nil {
				t.Fatal("a refused tree is not written")
			}
		})
	}
	src := writeStubTree(t, map[string]string{"f/x/a.ts": okScript})
	if _, err := StubTree(src, src); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("an existing output: %v", err)
	}
	if _, err := StubTree(filepath.Join(src, "f"), filepath.Join(t.TempDir(), "o")); err == nil || !strings.Contains(err.Error(), "not a sync tree root") {
		t.Fatalf("not a root: %v", err)
	}
}

// TestCheckStubSignatures passes when generate-metadata gave the stub the
// original's schema and refuses when it did not.
func TestCheckStubSignatures(t *testing.T) {
	meta := "summary: ''\ndescription: ''\nkind: script\nschema:\n  type: object\n  properties:\n    x: {type: number}\n  required: [x]\n"
	orig := writeStubTree(t, map[string]string{"f/x/a.ts": okScript, "f/x/a.script.yaml": meta})
	stubbed := filepath.Join(t.TempDir(), "stubbed")
	if _, err := StubTree(orig, stubbed); err != nil {
		t.Fatal(err)
	}
	if err := CheckStubSignatures(orig, stubbed); err != nil {
		t.Fatal(err)
	}
	// Member order is not a difference.
	reordered := "summary: ''\ndescription: ''\nkind: script\nschema:\n  required: [x]\n  properties:\n    x: {type: number}\n  type: object\n"
	if err := os.WriteFile(filepath.Join(stubbed, "f/x/a.script.yaml"), []byte(reordered), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckStubSignatures(orig, stubbed); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(meta, "required: [x]", "required: []", 1)
	if err := os.WriteFile(filepath.Join(stubbed, "f/x/a.script.yaml"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CheckStubSignatures(orig, stubbed)
	var r *Refusal
	if !errors.As(err, &r) || r.Code != "stub_signature_changed" || !strings.Contains(err.Error(), `"script":"f/x/a"`) {
		t.Fatalf("got %v", err)
	}
}

// locateFunc is a conformance.Locator from a function.
type locateFunc func(root, job string) (conformance.SitePath, error)

func (f locateFunc) Locate(_ context.Context, root, job string) (conformance.SitePath, error) {
	return f(root, job)
}

// TestStubRunsOnBun runs the stub of the collapsed fixture's book.ts on
// the pinned Bun against the real replay service, the way Windmill's
// wrapper calls main (positional arguments, the Date already hydrated):
// the stub sends the job identities from the environment and the
// arguments it received, throws an error outcome as an Error with its
// name, message and own properties, and returns a result outcome as the
// table's value, members in order.
func TestStubRunsOnBun(t *testing.T) {
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set; it names the pinned Bun " + BunVersion)
	}
	tree := filepath.Join(t.TempDir(), "stubbed")
	if _, err := StubTree("../../conformance/flows/stub/collapsed/windmill", tree); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stub, err := os.ReadFile(filepath.Join(tree, "f/wl_stub/collapsed.flow/book.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.ts"), stub, 0o644); err != nil {
		t.Fatal(err)
	}
	driver := `import { main } from "./main.ts";
try {
  const r = await main(new Date("2026-03-02T09:00:00Z"), 0);
  console.log(JSON.stringify({ result: r }));
} catch (e) {
  // The own properties the Bun wrapper keeps under extra, plus name and
  // message (WR-18).
  const skip = ["stack", "line", "column", "sourceURL", "originalLine", "originalColumn"];
  const own = {};
  for (const k of Object.getOwnPropertyNames(e)) if (!skip.includes(k)) own[k] = e[k];
  console.log(JSON.stringify({ error: { isError: e instanceof Error, name: e.name, own } }));
}
`
	if err := os.WriteFile(filepath.Join(dir, "driver.ts"), []byte(driver), 0o644); err != nil {
		t.Fatal(err)
	}
	site := conformance.SitePath{}.Child(conformance.Module("each"), conformance.Iteration(0), conformance.Module("book"))
	var tbl conformance.ReplayTable
	if err := json.Unmarshal([]byte(`{"m:each/i:0/m:book": [{"error": {"name": "Busy", "message": "calendar busy", "code": 7}}, {"result": {"slot": 4, "at": "x"}}]}`), &tbl); err != nil {
		t.Fatal(err)
	}
	var svc conformance.ReplayService
	sink := &Sink{Replay: &svc}
	srv := httptest.NewServer(sink)
	defer srv.Close()
	const root = "00000000-0000-4000-8000-0000000000aa"
	svc.Open(root, tbl, locateFunc(func(r, job string) (conformance.SitePath, error) { return site, nil }))
	run := func(job string) string {
		t.Helper()
		cmd := exec.Command(bun, "run", "driver.ts")
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "WL_EFFECT_SINK=" + srv.URL,
			"WM_ROOT_JOB_ID=" + root, "WM_ROOT_FLOW_JOB_ID=" + root, "WM_FLOW_JOB_ID=" + root, "WM_JOB_ID=" + job, "WM_FLOW_STEP_ID=each"}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("bun: %v\n%s", err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	if got := run("job-0"); got != `{"error":{"isError":true,"name":"Busy","own":{"message":"calendar busy","name":"Busy","code":7}}}` {
		t.Fatalf("attempt 0: %s", got)
	}
	if got := run("job-1"); got != `{"result":{"slot":4,"at":"x"}}` {
		t.Fatalf("attempt 1: %s", got)
	}
	// A run that is not open is refused, and the stub throws the refusal.
	if got := run2(t, bun, dir, srv.URL); !strings.Contains(got, "wltest replay refused the call (HTTP 409)") || !strings.Contains(got, "replay_foreign_root") {
		t.Fatalf("foreign root: %s", got)
	}
	calls := svc.Close(root)
	if len(calls) != 2 {
		t.Fatalf("%d calls", len(calls))
	}
	for i, c := range calls {
		want := fmt.Sprintf(`{"site":"m:each/i:0/m:book","attempt":%d,"step":"each","job":"job-%d","flow":"%s","root_flow":"%s","root":"%s","args":["2026-03-02T09:00:00.000Z",0]}`, i, i, root, root, root)
		if string(c.Effect) != want {
			t.Errorf("effect %d: %s\nwant      %s", i, c.Effect, want)
		}
	}
}

func run2(t *testing.T, bun, dir, sink string) string {
	t.Helper()
	cmd := exec.Command(bun, "run", "driver.ts")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "WL_EFFECT_SINK=" + sink, "WM_ROOT_JOB_ID=00000000-0000-4000-8000-0000000000ff", "WM_JOB_ID=j"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bun: %v", err)
	}
	return string(out)
}

// TestStubTreeKeepsItsOutputOutsideTheTree refuses an output directory
// inside the tree it copies, also through a symbolic link in its parents,
// before writing anything, and removes the output and the parents it made
// when the copy fails.
func TestStubTreeKeepsItsOutputOutsideTheTree(t *testing.T) {
	src := writeStubTree(t, map[string]string{"f/x/a.ts": okScript})
	before := listTree(t, src)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}
	for _, dst := range []string{
		filepath.Join(src, "stubbed"),
		filepath.Join(src, "f", "x", "deeper", "stubbed"),
		filepath.Join(link, "stubbed"),
	} {
		if _, err := StubTree(src, dst); err == nil || !strings.Contains(err.Error(), "is inside the sync tree") {
			t.Fatalf("%s: got %v", dst, err)
		}
		if after := listTree(t, src); after != before {
			t.Fatalf("%s: the tree changed:\n%s\nwas\n%s", dst, after, before)
		}
	}
	// A sibling whose name starts like the tree's is outside it.
	if _, err := StubTree(src, src+"-stubbed"); err != nil {
		t.Fatal(err)
	}

	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	bad := writeStubTree(t, map[string]string{"f/x/a.ts": okScript, "f/x/notes.txt": "n"})
	if err := os.Chmod(filepath.Join(bad, "f/x/notes.txt"), 0); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(t.TempDir(), "new")
	if _, err := StubTree(bad, filepath.Join(parent, "a", "out")); err == nil {
		t.Fatal("a copy of an unreadable file succeeded")
	}
	if _, err := os.Lstat(parent); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a failed copy left %s: %v", parent, err)
	}
}

func listTree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b.WriteString(rel + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestStubIsolationOnBun runs stubs on the pinned Bun with fetch replaced
// by a recorder: the declared main is never called, so a parameter default
// that would call a service or name a dropped binding never runs, and a
// parameter named Array cannot reach the stub's own code. The only request
// is the replay call, which carries the arguments as main received them.
// Each original runs through the same driver first, to show the recorder
// sees what a default does.
func TestStubIsolationOnBun(t *testing.T) {
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set; it names the pinned Bun " + BunVersion)
	}
	const replay = "http://replay.invalid"
	for _, c := range []struct {
		name, src, args string
		original, stub  string
	}{
		{
			name:     "a ternary default that calls fetch",
			src:      `export async function main(x = (true ? fetch : null)("https://synthetic.invalid", {method: "POST"})) { return "original" }`,
			args:     `[]`,
			original: `{"calls":["https://synthetic.invalid"],"args":[],"result":"original"}`,
			stub:     `{"calls":["http://replay.invalid/replay"],"args":[[]],"result":7}`,
		},
		{
			name:     "a ternary default that names a dropped binding",
			src:      "const LIMIT = 5;\nexport async function main(x = true ? LIMIT : 0) { return x }",
			args:     `[]`,
			original: `{"calls":[],"args":[],"result":5}`,
			stub:     `{"calls":["http://replay.invalid/replay"],"args":[[]],"result":7}`,
		},
		{
			name:     "a parameter named Array",
			src:      `export async function main(Array: string) { return Array }`,
			args:     `["value"]`,
			original: `{"calls":[],"args":[],"result":"value"}`,
			stub:     `{"calls":["http://replay.invalid/replay"],"args":[["value"]],"result":7}`,
		},
		{
			name:     "a destructured binding named Array",
			src:      `export async function main({ Array }: { Array: string }) { return Array }`,
			args:     `[{"Array":"v"}]`,
			original: `{"calls":[],"args":[],"result":"v"}`,
			stub:     `{"calls":["http://replay.invalid/replay"],"args":[[{"Array":"v"}]],"result":7}`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub, err := StubSource(c.src)
			if err != nil {
				t.Fatal(err)
			}
			for _, run := range []struct{ label, source, want string }{{"original", c.src, c.original}, {"stub", stub, c.stub}} {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "main.ts"), []byte(run.source), 0o644); err != nil {
					t.Fatal(err)
				}
				driver := `const calls = [];
globalThis.fetch = async (url, init) => {
  calls.push({ url: String(url), body: init && init.body ? JSON.parse(init.body) : null });
  return new Response('{"result":7}', { status: 200 });
};
process.env.WL_EFFECT_SINK = "` + replay + `";
const m = await import("./main.ts");
let out;
try { out = { result: await m.main(...` + c.args + `) }; } catch (e) { out = { error: String(e) }; }
console.log(JSON.stringify({ calls: calls.map((x) => x.url), args: calls.filter((x) => x.body).map((x) => x.body.args ?? null), ...out }));
`
				if err := os.WriteFile(filepath.Join(dir, "driver.ts"), []byte(driver), 0o644); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(bun, "run", "driver.ts")
				cmd.Dir = dir
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("%s: bun: %v\n%s", run.label, err, stderr.String())
				}
				if got := strings.TrimSpace(string(out)); got != run.want {
					t.Errorf("%s: %s\nwant %s", run.label, got, run.want)
				}
			}
		})
	}
}

// TestStubTreeRefusesACaseAlias spells the output's parent with another
// letter case than the tree. On a case-insensitive file system (macOS by
// default) the two spellings are one directory, so the output is inside
// the tree and is refused before anything is written; on a case-sensitive
// one they are two directories, and the stub is written.
func TestStubTreeRefusesACaseAlias(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "SourceTree")
	for p, c := range map[string]string{"wmill.yaml": "defaultTs: bun\nincludes: [\"f/**\"]\n", "f/x/a.ts": okScript} {
		full := filepath.Join(src, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(base, "sourcetree")
	a, errA := os.Stat(src)
	b, errB := os.Stat(alias)
	sameDir := errA == nil && errB == nil && os.SameFile(a, b)
	before := listTree(t, src)
	_, err := StubTree(src, filepath.Join(alias, "out"))
	if after := listTree(t, src); after != before {
		t.Fatalf("the tree changed:\n%s\nwas\n%s", after, before)
	}
	if sameDir {
		if err == nil || !strings.Contains(err.Error(), "is inside the sync tree") {
			t.Fatalf("a case alias of the tree: got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("a distinct directory on a case-sensitive file system: %v", err)
	}
	t.Log("case-sensitive file system: the alias is a separate directory")
}

// TestExportListsOnBun checks the scanner's reading of export lists
// against the pinned Bun: a script is stubbed exactly when importing it
// gives a callable main, so a type-only export entry (which exports
// nothing at run time) is refused as Windmill would fail it (WR-7), and
// the stub of every accepted one exports a callable main too.
func TestExportListsOnBun(t *testing.T) {
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set; it names the pinned Bun " + BunVersion)
	}
	typeofMain := func(t *testing.T, src string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.ts"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "driver.ts"), []byte(`const m = await import("./main.ts"); console.log(typeof m.main);`), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bun, "run", "driver.ts")
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("bun: %v\n%s", err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	for _, src := range []string{
		"function main(x: string) { return x }\nexport { main };",
		"function main(x: string) { return x }; export { type main };",
		"function main(x: string) { return x }\nexport type { main };",
		"function main(x: string) { return x }\nexport { type main as main };",
		"type Foo = string;\nfunction main(x: Foo) { return x }\nexport { type Foo, main };",
		"function main(x: string) { return x }\nconst run = (x: string) => x;\nexport { run as main };",
		"export async function main(x: string) { return x }",
	} {
		got := typeofMain(t, src)
		stub, err := StubSource(src)
		switch {
		case got == "function" && err != nil:
			t.Errorf("Bun exports a callable main, the scanner refuses: %v\n%s", err, src)
		case got != "function" && err == nil:
			t.Errorf("Bun exports main as %s, the scanner accepts it\n%s", got, src)
		case err == nil:
			if s := typeofMain(t, stub); s != "function" {
				t.Errorf("the stub exports main as %s\n%s", s, src)
			}
		}
	}
}

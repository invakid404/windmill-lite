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

// writeTree writes a sync tree of the given files below a new directory.
func writeTree(t *testing.T, files map[string]string) string {
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := writeTree(t, c.files)
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
	src := writeTree(t, map[string]string{"f/x/a.ts": okScript})
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
	orig := writeTree(t, map[string]string{"f/x/a.ts": okScript, "f/x/a.script.yaml": meta})
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

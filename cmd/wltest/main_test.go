package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsage(t *testing.T) {
	for _, c := range []struct {
		args []string
		code int
		out  string
	}{
		{nil, 2, "usage: wltest record"},
		{[]string{"frobnicate"}, 2, "usage: wltest record"},
		{[]string{"help"}, 0, "usage: wltest record"},
		{[]string{"check", t.TempDir()}, 2, "no step files, flow fixtures or expression files found"},
		{[]string{"check", "main.go"}, 2, "is neither a step file"},
		{[]string{"check", "../../conformance/exprs/deferred.yaml"}, 2, "is neither a step file"},
		{[]string{"check"}, 2, "usage: wltest check"},
		{[]string{"check", "--mode", "host", "x"}, 2, `mode "host" is not warm or fresh (host mode is milestone F3b)`},
		{[]string{"diff", "x"}, 2, "wltest diff is not implemented yet"},
		{[]string{"stub", "x"}, 2, "usage: wltest record"},
		{[]string{"stub", "--out", t.TempDir() + "/o"}, 2, "usage: wltest record"},
		{[]string{"stub", "--out", t.TempDir() + "/o", t.TempDir()}, 1, "not a sync tree root"},
		{[]string{"record"}, 2, "usage: wltest record"},
		{[]string{"record", "--workers", "3", "x"}, 2, "usage: wltest record"},
		{[]string{"record", "--no-such-flag", "x"}, 2, "flag provided but not defined"},
		{[]string{"record", t.TempDir()}, 1, "no fixtures"},
		{[]string{"record", "main.go"}, 2, "neither a fixture directory nor an expression file"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), c.args, &stdout, &stderr)
		if code != c.code || !strings.Contains(stdout.String()+stderr.String(), c.out) {
			t.Errorf("%q: exit %d, output %q; want exit %d with %q", c.args, code, stdout.String()+stderr.String(), c.code, c.out)
		}
	}
}

func TestHarnessInfo(t *testing.T) {
	h := harnessInfo()
	if h.Name != "wltest" || !strings.HasPrefix(h.Go, "go") {
		t.Fatalf("%+v", h)
	}
}

// TestStub writes the stubbed copy of a fixture's sync tree and lists the
// replaced sources; a tree it cannot stub is refused with the reason as
// JSON on standard output.
func TestStub(t *testing.T) {
	out := filepath.Join(t.TempDir(), "stubbed")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"stub", "--out", out, "../../conformance/flows/stub/scopes/windmill"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stdout.String() != "stubbed f/wl_stub/members.flow/lookup.ts\nstubbed f/wl_stub/notify.flow/post.ts\n" || !strings.Contains(stderr.String(), "generate-metadata") {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(out, "f/wl_stub/notify.flow/post.ts"))
	if err != nil || !strings.Contains(string(data), "async function main(to: string, team: string) {}") || !strings.Contains(string(data), "export { __wlMain as main };") {
		t.Fatalf("stub %s %v", data, err)
	}
	// An output inside the tree is refused before anything is written.
	if code := run(context.Background(), []string{"stub", "--out", "../../conformance/flows/stub/scopes/windmill/out", "../../conformance/flows/stub/scopes/windmill"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "is inside the sync tree") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if _, err := os.Stat("../../conformance/flows/stub/scopes/windmill/out"); err == nil {
		t.Fatal("the refused output was written")
	}

	// A tree whose codebase entry names paths outside it is stubbed below
	// the output, with those paths beside it (oracle.Layout), and the
	// unchanged wmill.yaml reaches them from the stubbed tree.
	out = filepath.Join(t.TempDir(), "stubbed")
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"stub", "--out", out, "../../conformance/flows/stub/outside/windmill"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stdout.String() != "stubbed f/wl_stub_out/tag.ts\ncarried ../shared\ntree windmill\n" || !strings.Contains(stderr.String(), "generate-metadata in "+filepath.Join(out, "windmill")+" before") {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
	for _, p := range []string{"wmill.yaml", "../shared/lib/assets/child.mjs", "../shared/lib/label.ts"} {
		want, _ := os.ReadFile(filepath.Join("../../conformance/flows/stub/outside/windmill", p))
		if got, err := os.ReadFile(filepath.Join(out, "windmill", p)); err != nil || string(got) != string(want) {
			t.Fatalf("%s: %q %v", p, got, err)
		}
	}
	// A detour through a directory outside the tree is made, empty.
	base := t.TempDir()
	for p, s := range map[string]string{
		"w/wmill.yaml":   "defaultTs: bun\ncodebases:\n  - relative_path: ../detour/../shared\n",
		"w/f/x/a.ts":     "export async function main(x: number) { return x }\n",
		"shared/a.txt":   "a\n",
		"detour/old.txt": "o\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(base, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out = filepath.Join(t.TempDir(), "stubbed")
	stdout.Reset()
	if code := run(context.Background(), []string{"stub", "--out", out, filepath.Join(base, "w")}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stdout.String() != "stubbed f/x/a.ts\ncarried ../shared\nmade ../detour\ntree w\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
	if got, err := os.ReadFile(out + "/w/../detour/../shared/a.txt"); err != nil || string(got) != "a\n" {
		t.Fatalf("the spelled path from the stubbed tree: %q %v", got, err)
	}

	tree := t.TempDir()
	for p, s := range map[string]string{"wmill.yaml": "defaultTs: bun\n", "f/x/a.py": "def main():\n  pass\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(tree, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stdout.Reset()
	if code := run(context.Background(), []string{"stub", "--out", filepath.Join(t.TempDir(), "o"), tree}, &stdout, &stderr); code != 1 || !strings.HasPrefix(stdout.String(), `{"error":"stub_unsupported","file":"f/x/a.py"`) {
		t.Fatalf("exit %d, stdout %q", code, stdout.String())
	}
}

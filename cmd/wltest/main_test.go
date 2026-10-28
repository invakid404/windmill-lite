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
		{[]string{"check", "conformance/flows"}, 2, "wltest check is not implemented yet"},
		{[]string{"diff", "x"}, 2, "wltest diff is not implemented yet"},
		{[]string{"stub", "x"}, 2, "usage: wltest record"},
		{[]string{"stub", "--out", t.TempDir() + "/o"}, 2, "usage: wltest record"},
		{[]string{"stub", "--out", t.TempDir() + "/o", t.TempDir()}, 1, "not a sync tree root"},
		{[]string{"record"}, 2, "usage: wltest record"},
		{[]string{"record", "--workers", "3", "x"}, 2, "usage: wltest record"},
		{[]string{"record", "--no-such-flag", "x"}, 2, "flag provided but not defined"},
		{[]string{"record", t.TempDir()}, 1, "no fixtures"},
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
	if err != nil || !strings.Contains(string(data), "export async function main(to: string, team: string) {") {
		t.Fatalf("stub %s %v", data, err)
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

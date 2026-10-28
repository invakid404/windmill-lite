package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPrintsVersion(t *testing.T) {
	for _, args := range [][]string{nil, {"version"}, {"--version"}, {"-v"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%q) exit code = %d, want 0", args, code)
		}
		if got := stdout.String(); !strings.HasPrefix(got, "windmill-lite ") || !strings.HasSuffix(got, "\n") {
			t.Errorf("run(%q) stdout = %q, want \"windmill-lite <version>\\n\"", args, got)
		}
		if stderr.Len() != 0 {
			t.Errorf("run(%q) stderr = %q, want empty", args, stderr.String())
		}
	}
}

func TestRunUsesLinkTimeVersion(t *testing.T) {
	old := version
	version = "v1.2.3"
	t.Cleanup(func() { version = old })

	var stdout, stderr bytes.Buffer
	run(nil, &stdout, &stderr)
	if got, want := stdout.String(), "windmill-lite v1.2.3\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestRunRejectsUnknownArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"serve"}, &stdout, &stderr); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("stderr = %q, want a usage line", stderr.String())
	}
}

func TestSelfcheck(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"selfcheck"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "expression engine ok, 3 probes") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if code := run([]string{"selfcheck", "extra"}, &stdout, &stderr); code != 2 {
		t.Errorf("selfcheck with an argument: exit code %d", code)
	}
}

// bunForTest is the pinned Bun named by WL_BUN; the build tests skip
// without it unless WL_REQUIRE_BUN is set, as in CI.
func bunForTest(t *testing.T) string {
	t.Helper()
	p := os.Getenv("WL_BUN")
	if p == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set")
	}
	return p
}

// TestBuildAndVerify runs build and verify as the command line does: the
// fixture of internal/bundle built twice, as a directory and as an
// archive, gives one digest, which verify confirms; a changed file fails
// verification with status 1.
func TestBuildAndVerify(t *testing.T) {
	bun := bunForTest(t)
	fixture := filepath.Join("..", "..", "internal", "bundle", "testdata", "fixture")
	helper := filepath.Join("..", "..", "harnesses", "bun", "build", "main.ts")
	dir := t.TempDir()
	buildTo := func(out string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := run([]string{"build", filepath.Join(fixture, "windmill"), "-o", out,
			"-contract", filepath.Join(fixture, "contract.yaml"), "-bun", bun, "-helper", helper}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("build exit code %d: %s", code, stderr.String())
		}
		return strings.TrimSpace(stdout.String())
	}
	a := buildTo(filepath.Join(dir, "a"))
	b := buildTo(filepath.Join(dir, "b.wlb"))
	if !strings.HasPrefix(a, "sha256:") || a != b {
		t.Fatalf("digests %q and %q", a, b)
	}
	for _, artifact := range []string{filepath.Join(dir, "a"), filepath.Join(dir, "b.wlb")} {
		var stdout, stderr bytes.Buffer
		code := run([]string{"verify", artifact, "-digest", a, "-manifest", filepath.Join(dir, "a", "manifest.json")}, &stdout, &stderr)
		if code != 0 || strings.TrimSpace(stdout.String()) != a {
			t.Errorf("verify %s: exit code %d, stdout %q, stderr %q", artifact, code, stdout.String(), stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "ir.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"verify", filepath.Join(dir, "a")}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "ir.json: digest") {
		t.Errorf("verify of a changed bundle: exit code %d, stderr %q", code, stderr.String())
	}
	// An existing archive is not overwritten.
	if code := run([]string{"build", filepath.Join(fixture, "windmill"), "-o", filepath.Join(dir, "b.wlb"),
		"-bun", bun, "-helper", helper}, &stdout, &stderr); code != 1 {
		t.Errorf("build over an existing archive: exit code %d", code)
	}
}

func TestCommandUsage(t *testing.T) {
	for _, args := range [][]string{
		{"build"}, {"build", "a", "b", "-o", "x"}, {"build", "a"}, {"build", "a", "-o", "x", "-nope"},
		{"verify"}, {"verify", "a", "b"},
	} {
		var stdout, stderr bytes.Buffer
		t.Setenv("WL_BUILD_HELPER", "main.ts")
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("run(%q) exit code = %d, want 2 (%s)", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	t.Setenv("WL_BUILD_HELPER", "")
	if code := run([]string{"build", "a", "-o", "x"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "WL_BUILD_HELPER") {
		t.Errorf("build without a helper: exit code %d, stderr %q", code, stderr.String())
	}
}

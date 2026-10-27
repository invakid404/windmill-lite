package main

import (
	"bytes"
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

package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// This file builds every marker from markerPrefix: the guard reads a
// marker in any test source, so a literal one here would be a real marker,
// which fails the guard.

// gitTree writes files into a new Git repository and returns its root. With
// tracked the files are staged, as a checkout's are in the index; without
// it they stay untracked. The guard reads both, leaving out what an ignore
// rule excludes.
func gitTree(t *testing.T, files map[string]string, tracked bool) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, files)
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if tracked {
		if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
			t.Fatalf("git add: %v: %s", err, out)
		}
	}
	return dir
}

// TestLedgerGuard is the guard's exit gate: `wltest ledger` fails while
// the ledger file exists, with an entry or empty, while a marker remains
// in a Go test, a bun test or any other file, or while a test source holds
// a malformed marker, and passes without them. Prose about the marker form
// outside a test source passes.
func TestLedgerGuard(t *testing.T) {
	m := func(milestone, reason string) string {
		return markerPrefix + " " + milestone + " " + reason
	}
	for _, tc := range []struct {
		name        string
		files       map[string]string
		code        int
		out, errOut []string
	}{
		{
			name: "nothing pending",
			files: map[string]string{
				"README.md": "A marker is `" + markerPrefix + " <milestone> <reason>`.\n",
				"x/ok.go":   "package x\n\nconst p = \"" + markerPrefix + "\"\n",
			},
			out: []string{"retarget ledger " + ledgerPath + ": absent", "markers: 0"},
		},
		{
			name:   "a ledger entry",
			files:  map[string]string{ledgerPath: `{"about": "a", "flow_suite": {"conformance/flows/oracle/probes/env": {"milestone": "W8c", "reason": "the image's Bun"}}, "step_suite": {}, "oracle": {}}`},
			code:   1,
			out:    []string{"retarget ledger " + ledgerPath + ": present"},
			errOut: []string{ledgerPath + " exists: delete the retarget ledger"},
		},
		{
			name:   "an empty ledger",
			files:  map[string]string{ledgerPath: `{"about": "a", "flow_suite": {}, "step_suite": {}, "oracle": {}}`},
			code:   1,
			out:    []string{": present", "markers: 0"},
			errOut: []string{"exists: delete the retarget ledger"},
		},
		{
			name: "a Go test's marker",
			files: map[string]string{"internal/x/x_test.go": "package x\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tt.Skip(\"" +
				m("W4c", `Bun 1.4.3's process.env refuses accessors, so the \"view\" breaks`) + "\")\n}\n"},
			code:   1,
			out:    []string{"markers: 1", "  internal/x/x_test.go:6: " + m("W4c", `Bun 1.4.3's process.env refuses accessors, so the \"view\" breaks`)},
			errOut: []string{"1 retarget markers remain: remove every one"},
		},
		{
			name:   "a bun test's marker",
			files:  map[string]string{"harnesses/bun/test/native.test.ts": "test.skip('" + m("W4f", "a BYOB request now refuses respond(0)") + ": events reach the realm', () => {});\n"},
			code:   1,
			out:    []string{"  harnesses/bun/test/native.test.ts:1: " + m("W4f", "a BYOB request now refuses respond(0): events reach the realm")},
			errOut: []string{"1 retarget markers remain"},
		},
		{
			name:   "a marker in another file",
			files:  map[string]string{"conformance/steps/bun/x.yaml": "note: " + m("W4d", "the text moved") + "\n"},
			code:   1,
			out:    []string{"  conformance/steps/bun/x.yaml:1: " + m("W4d", "the text moved")},
			errOut: []string{"1 retarget markers remain"},
		},
		{
			name:   "a malformed marker in a test",
			files:  map[string]string{"x/x_test.go": "package x\n// " + markerPrefix + " w4c lower case\n"},
			code:   1,
			out:    []string{"markers: 0"},
			errOut: []string{"x/x_test.go:2: ", `milestone "w4c" is not a milestone id`},
		},
		{
			name:   "a malformed marker in a bun test",
			files:  map[string]string{"harnesses/bun/test/x.test.ts": "test.skip('" + markerPrefix + " w4c r', () => {});\n"},
			code:   1,
			out:    []string{"markers: 0"},
			errOut: []string{"harnesses/bun/test/x.test.ts:1: ", `milestone "w4c" is not a milestone id`},
		},
		{
			name:  "an ignored file",
			files: map[string]string{".gitignore": "/out/\n", "out/x_test.go": "// " + m("W4c", "r") + "\n"},
			out:   []string{"markers: 0"},
		},
	} {
		for _, tracked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tracked=%v", tc.name, tracked), func(t *testing.T) {
				dir := gitTree(t, tc.files, tracked)
				var stdout, stderr bytes.Buffer
				code := run(context.Background(), []string{"ledger", "--repo", dir}, &stdout, &stderr)
				if code != tc.code {
					t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.code, stdout.String(), stderr.String())
				}
				for _, want := range tc.out {
					if !strings.Contains(stdout.String(), want) {
						t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
					}
				}
				if tc.code == 1 {
					for _, want := range tc.errOut {
						if !strings.Contains(stderr.String(), want) {
							t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
						}
					}
				} else if stderr.Len() > 0 {
					t.Errorf("stderr %q", stderr.String())
				}
			})
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"ledger", "x"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: wltest ledger") {
		t.Errorf("an argument: exit %d, %q", code, stderr.String())
	}
	if code := run(context.Background(), []string{"ledger", "--base", "main"}, &stdout, &stderr); code != 2 {
		t.Errorf("the retired --base flag: exit %d, %q", code, stderr.String())
	}
	if code := run(context.Background(), []string{"ledger", "--repo", t.TempDir()}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "listing the files of") {
		t.Errorf("not a repository: exit %d, %q", code, stderr.String())
	}
}

// parseMarker reads the milestone and the reason of a marker, and refuses
// any other form.
func TestParseMarker(t *testing.T) {
	for _, tc := range []struct{ text, milestone, reason string }{
		{markerPrefix + " W4c Bun 1.4.3's process.env refuses accessors", "W4c", "Bun 1.4.3's process.env refuses accessors"},
		{markerPrefix + " W10a r", "W10a", "r"},
		{markerPrefix + " X1 the bound fails under load", "X1", "the bound fails under load"},
		{markerPrefix + " W9 r: with a colon", "W9", "r: with a colon"},
	} {
		m, r, err := parseMarker(tc.text)
		if err != nil || m != tc.milestone || r != tc.reason {
			t.Errorf("parseMarker(%q) = %q, %q, %v", tc.text, m, r, err)
		}
	}
	for _, text := range []string{
		markerPrefix + "W4c r",
		markerPrefix + "  W4c r",
		markerPrefix + " w4c r",
		markerPrefix + " W4c",
		markerPrefix + " W4c ",
		markerPrefix + " W4c  r",
		markerPrefix + " <milestone> <reason>",
		markerPrefix + " W4cc r",
		"pending: W4c r",
		" " + markerPrefix + " W4c r",
	} {
		if m, r, err := parseMarker(text); err == nil {
			t.Errorf("parseMarker(%q) = %q, %q, want an error", text, m, r)
		}
	}
}

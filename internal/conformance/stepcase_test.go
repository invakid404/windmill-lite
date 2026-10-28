package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every step file of conformance/steps loads.
func TestLoadStepFiles(t *testing.T) {
	files, err := FindStepFiles("../../conformance/steps")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no step files")
	}
	for _, p := range files {
		if _, err := LoadStepFile(p); err != nil {
			t.Error(err)
		}
	}
	if _, err := LoadStepDeferred("../../conformance/steps/bun/deferred.yaml"); err != nil {
		t.Error(err)
	}
}

func writeStepFile(t *testing.T, name, text string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), StepDirName, "bun")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const stepCaseHead = `finding: WR-6
scope: core
cases:
  - spec: WR-6/a
    provenance: source-derived
    title: t
    script: |
      export function main() { return 1; }
`

func TestLoadStepFileRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, file, text, want string
	}{
		{"named after another finding", "wr-7.yaml", stepCaseHead + "    runs: [{args: '{}', expect: {status: success, result: 1}}]\n", "is named wr-6.yaml"},
		{"unknown field", "wr-6.yaml", stepCaseHead + "    runs: [{args: '{}', expect: {status: success, result: 1}, extra: 1}]\n", "unknown field"},
		{"args not an object", "wr-6.yaml", stepCaseHead + "    runs: [{args: '[1]', expect: {status: success, result: 1}}]\n", "not a JSON object text"},
		{"a run that checks nothing", "wr-6.yaml", stepCaseHead + "    runs: [{args: '{}'}]\n", "a run needs"},
		{"no runs", "wr-6.yaml", stepCaseHead, "has no runs"},
		{"a status no step has", "wr-6.yaml", stepCaseHead + "    runs: [{args: '{}', expect: {status: canceled, result: null}}]\n", "a success or a failure"},
		{"by_mode without a deviation", "wr-6.yaml", stepCaseHead + "    runs: [{args: '{}', by_mode: {warm: {expect: {status: success, result: 1}}, fresh: {expect: {status: success, result: 1}}}}]\n", "needs deviation"},
		{"by_mode without fresh", "wr-6.yaml", stepCaseHead + "    deviation: D1\n    runs: [{args: '{}', by_mode: {warm: {expect: {status: success, result: 1}}}}]\n", "exactly warm and fresh"},
		{"by_mode and a check", "wr-6.yaml", stepCaseHead + "    deviation: D1\n    runs: [{args: '{}', expect: {status: success, result: 1}, by_mode: {warm: {expect: {status: success, result: 1}}, fresh: {expect: {status: success, result: 1}}}}]\n", "one or the other"},
		{"a bad late kind", "wr-6.yaml", stepCaseHead + "    runs: [{args: '{}', late: [{kind: warning}]}]\n", "late kind"},
		{"a bad path", "wr-6.yaml", stepCaseHead + "    path: u/x/y\n    runs: [{args: '{}', expect: {status: success, result: 1}}]\n", "not a Windmill path"},
		{"a finding cited without a variant", "wr-6.yaml", strings.Replace(stepCaseHead, "spec: WR-6/a", "spec: WR-6", 1) + "    runs: [{args: '{}', expect: {status: success, result: 1}}]\n", "needs a variant"},
		{"two sources for one path", "wr-6.yaml", stepCaseHead + "    path: f/x/s\n    runs: [{args: '{}', expect: {status: success, result: 1}}]\n" +
			"  - spec: WR-6/b\n    provenance: source-derived\n    title: t\n    path: f/x/s\n    script: |\n      export function main() { return 2; }\n    runs: [{args: '{}', expect: {status: success, result: 2}}]\n", "another source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadStepFile(writeStepFile(t, tc.file, tc.text))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
	// Outside steps/bun a file is not a step file at all.
	if IsStepFile(filepath.Join("conformance", "steps", "bun", StepDeferredFile)) || !IsStepFile(filepath.Join("x", "steps", "bun", "wr-6.yaml")) || IsStepFile(filepath.Join("x", "exprs", "wr-6.yaml")) {
		t.Fatal("IsStepFile")
	}
}

// CompareStep masks the stack, compares the rest, checks claims, and uses
// the check of the mode asked for.
func TestCompareStep(t *testing.T) {
	f, err := LoadStepFile(writeStepFile(t, "d1.yaml", `finding: D1
scope: core
cases:
  - spec: D1
    variant: x
    provenance: policy
    title: t
    deviation: D1
    script: |
      export function main() { return 1; }
    runs:
      - args: '{}'
        by_mode:
          warm:
            expect: {status: failure, result: {error: {message: m, stack: "<stack>"}}}
            claims: [{at: /result/error/message, prefix: m}]
          fresh: {expect: {status: success, result: 1}}
`))
	if err != nil {
		t.Fatal(err)
	}
	c := &f.Cases[0]
	got := Observation{Status: RunFailure, Result: json.RawMessage(`{"error":{"message":"m","stack":"Error: m\n    at main"}}`)}
	if diffs, err := CompareStep(c, &c.Runs[0], ModeWarm, got); err != nil || len(diffs) != 0 {
		t.Fatalf("warm: %v, %v", diffs, err)
	}
	diffs, err := CompareStep(c, &c.Runs[0], ModeFresh, got)
	if err != nil || len(diffs) == 0 {
		t.Fatalf("fresh expects a success: %v, %v", diffs, err)
	}
	got.Result = json.RawMessage(`{"error":{"message":"n","stack":"s"}}`)
	diffs, err = CompareStep(c, &c.Runs[0], ModeWarm, got)
	if err != nil || len(diffs) != 2 {
		t.Fatalf("a different message is a difference and a failed claim: %v, %v", diffs, err)
	}
}

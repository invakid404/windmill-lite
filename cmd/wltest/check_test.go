package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

const (
	stepsDir      = "../../conformance/steps"
	catalogueFile = "../../docs/semantics/conformance.md"
)

// bunOrSkip returns the pinned Bun from WL_BUN, or skips the test; with
// WL_REQUIRE_BUN set, as CI sets it, a missing Bun fails it instead.
func bunOrSkip(t *testing.T) string {
	t.Helper()
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set")
	}
	return bun
}

func repo(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestStepSuite is the exit gate of milestones C3a and C3b: every step
// case of conformance/steps passes in warm mode and in fresh mode, through
// the real Bun harness on the pinned Bun, each script built by the Bun
// build helper.
func TestStepSuite(t *testing.T) {
	bun := bunOrSkip(t)
	files, err := conformance.FindStepFiles(stepsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no step files")
	}
	var loaded []*conformance.StepFile
	n := 0
	for _, path := range files {
		f, err := conformance.LoadStepFile(path)
		if err != nil {
			t.Fatal(err)
		}
		loaded = append(loaded, f)
		n += len(f.Cases)
	}
	for _, mode := range []string{conformance.ModeWarm, conformance.ModeFresh} {
		t.Run(mode, func(t *testing.T) {
			sc := &stepChecker{bun: bun, repo: repo(t), mode: mode}
			for _, f := range loaded {
				t.Run(f.Finding, func(t *testing.T) {
					t.Parallel()
					for _, r := range sc.runFile(context.Background(), f) {
						if len(r.problems) > 0 {
							t.Errorf("%s:\n  %s\nharness diagnostics:\n%s", r.name, strings.Join(r.problems, "\n  "), r.diag)
						}
					}
				})
			}
		})
	}
	t.Logf("step suite: %d cases in %d files, modes warm and fresh", n, len(files))
}

// The checker fails a case whose expectation does not hold: a result, a
// claim, a late report, a warning, a count of what was left behind and a
// late report after settlement that differ are each reported, in both
// modes.
func TestStepCheckerReportsDifferences(t *testing.T) {
	bun := bunOrSkip(t)
	dir := filepath.Join(t.TempDir(), "steps", "bun")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wr-6.yaml")
	if err := os.WriteFile(path, []byte(`finding: WR-6
scope: core
cases:
  - spec: WR-6/a
    provenance: source-derived
    title: a wrong result
    script: |
      export function main(a: number) { return a; }
    runs:
      - args: '{"a":1}'
        expect: {status: success, result: 2}
  - spec: WR-6/b
    provenance: source-derived
    title: a wrong claim, late report and warning
    script: |
      export function main() { try { process.exit(3); } catch {} return "after"; }
    runs:
      - args: '{}'
        claims: [{at: /result/error/exit_code, equals: 4}]
        late: [{kind: result, final: true, body: '"before"'}]
        warnings: [unattributed_error]
  - spec: WR-6/d
    provenance: source-derived
    title: a warning that arrives after the outcome was fixed, before the call ends, is not missed
    script: |
      export async function main() {
        try { process.exit(3); } catch {}
        setTimeout(() => Promise.reject(new Error("detached")), 10);
        await Bun.sleep(100);
        return "after";
      }
    runs:
      - args: '{}'
        claims: [{at: /result/error/exit_code, equals: 3}]
  - spec: WR-6/e
    provenance: source-derived
    title: a successful result's error.stack is data, compared as any field
    script: |
      export function main() { return { error: { stack: "actual data" } }; }
    runs:
      - args: '{}'
        expect: {status: success, result: {error: {stack: "expected data"}}}
  - spec: WR-6/f
    provenance: source-derived
    title: a wrong count of what was left behind
    script: |
      export function main() { setTimeout(() => {}, 1000); setInterval(() => {}, 1000); return 1; }
    runs:
      - args: '{}'
        warnings: [left_behind]
        left: {timeout: 2}
  - spec: WR-6/g
    provenance: source-derived
    title: a late report that arrives after settlement is not missed
    script: |
      export async function main(wait: boolean) {
        if (wait) { await Bun.sleep(100); return 2; }
        Bun.sleep(20).then(() => process.exit(9));
        return 1;
      }
    runs:
      - args: '{}'
        expect: {status: success, result: 1}
        settled: completed
      - args: '{"wait":true}'
        expect: {status: success, result: 2}
  - spec: WR-6/c
    provenance: source-derived
    title: a case that holds
    script: |
      export function main(a: number) { return a; }
    runs:
      - args: '{"a":1}'
        expect: {status: success, result: 1}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{conformance.ModeWarm, conformance.ModeFresh} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"check", "--mode", mode, "--bun", bun, "--repo", repo(t), path}, &stdout, &stderr)
		out := stdout.String()
		if code != 1 {
			t.Fatalf("%s: exit %d, want 1\n%s%s", mode, code, out, stderr.String())
		}
		wants := []string{
			"fail wr-6-a ",
			"run 1: /result: 1 != 2",
			"fail wr-6-b ",
			"run 1: /result/error/exit_code: 3 (",
			"fail wr-6-e ",
			"run 1: /result/error/stack: \"actual data\" != \"expected data\"",
			"fail wr-6-f ",
			"run 1: left behind map[interval:1 timeout:1], want map[timeout:2]",
			"pass wr-6-c ",
		}
		if mode == conformance.ModeWarm {
			// In fresh mode the exits end the process: no late report or
			// warning can follow them.
			wants = append(wants,
				"run 1: late 1: body \"after\", want \"before\"",
				"run 1: warnings [], want [unattributed_error]",
				"fail wr-6-d ",
				"run 1: warnings [unattributed_error], want []",
				"fail wr-6-g ",
				"run 1: 1 late reports, want 0: [error final=false",
				"1 passed, 6 failed (mode warm)",
			)
		} else {
			wants = append(wants, "pass wr-6-d ", "pass wr-6-g ", "3 passed, 4 failed (mode fresh)")
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", mode, want, out)
			}
		}
	}
}

var catalogueEntry = regexp.MustCompile(`(?m)^- \*\*((?:WR|WA|FX)-[0-9V]+/[a-z])\*\* · ([a-z-]+)`)

// Every catalogue case of a finding with a step file is a step case or
// deferred, and a step case that cites a catalogue case keeps its
// provenance.
func TestStepCatalogue(t *testing.T) {
	data, err := os.ReadFile(catalogueFile)
	if err != nil {
		t.Fatal(err)
	}
	catalogue := map[string]string{}
	for _, m := range catalogueEntry.FindAllSubmatch(data, -1) {
		catalogue[string(m[1])] = string(m[2])
	}
	files, err := conformance.FindStepFiles(stepsDir)
	if err != nil {
		t.Fatal(err)
	}
	findings, covered := map[string]bool{}, map[string]bool{}
	for _, path := range files {
		f, err := conformance.LoadStepFile(path)
		if err != nil {
			t.Fatal(err)
		}
		findings[f.Finding] = true
		for _, c := range f.Cases {
			if !strings.Contains(c.Spec, "/") {
				continue // a case that extends the catalogue, or a deviation's
			}
			prov, ok := catalogue[c.Spec]
			if !ok {
				t.Errorf("%s is not a catalogue case", c.Spec)
				continue
			}
			if c.Provenance != prov {
				t.Errorf("%s: provenance %q, catalogue says %q", c.Spec, c.Provenance, prov)
			}
			covered[c.Spec] = true
		}
	}
	deferred, err := conformance.LoadStepDeferred(filepath.Join(stepsDir, "bun", conformance.StepDeferredFile))
	if err != nil {
		t.Fatal(err)
	}
	isDeferred := map[string]bool{}
	for _, d := range deferred {
		switch {
		case catalogue[d.Spec] == "":
			t.Errorf("deferred %s is not a catalogue case", d.Spec)
		case covered[d.Spec]:
			t.Errorf("%s is both a step case and deferred", d.Spec)
		case isDeferred[d.Spec]:
			t.Errorf("%s is deferred twice", d.Spec)
		}
		isDeferred[d.Spec] = true
	}
	n := 0
	for spec := range catalogue {
		finding, _, _ := strings.Cut(spec, "/")
		if !findings[finding] {
			continue
		}
		n++
		if !covered[spec] && !isDeferred[spec] {
			t.Errorf("catalogue case %s is neither a step case nor deferred", spec)
		}
	}
	t.Logf("catalogue cases of the findings with a step file: %d; step cases: %d; deferred: %d", n, len(covered), len(isDeferred))
}

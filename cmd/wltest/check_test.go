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

// nodeOrSkip returns the Node of WL_NODE, which runs CommonJS codebase
// bundles as Windmill does (D13), or "" when it is not set; with
// WL_REQUIRE_NODE set, as CI sets it, a missing Node fails the test.
func nodeOrSkip(t *testing.T) string {
	t.Helper()
	node := os.Getenv("WL_NODE")
	if node == "" && os.Getenv("WL_REQUIRE_NODE") != "" {
		t.Fatal("WL_REQUIRE_NODE is set but WL_NODE is not")
	}
	return node
}

func repo(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestStepSuite is milestone C3a's exit gate: every step case of
// conformance/steps passes in warm mode, through the real Bun harness on
// the pinned Bun, each script built by the Bun build helper or, for a
// codebase script, by esbuild. With WL_NODE it is also milestone C2b's D13
// gate: each CommonJS codebase bundle runs on Node too, as on Windmill,
// and shows its node check there.
func TestStepSuite(t *testing.T) {
	bun := bunOrSkip(t)
	node := nodeOrSkip(t)
	if node == "" {
		t.Log("WL_NODE is not set: CommonJS codebase bundles run on the Bun harness only (D13's Node side is skipped)")
	}
	files, err := conformance.FindStepFiles(stepsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no step files")
	}
	sc := &stepChecker{bun: bun, node: node, repo: repo(t), mode: conformance.ModeWarm}
	n := 0
	for _, path := range files {
		f, err := conformance.LoadStepFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(f.Finding, func(t *testing.T) {
			t.Parallel()
			for _, r := range sc.runFile(context.Background(), f) {
				if len(r.problems) > 0 {
					t.Errorf("%s:\n  %s\nharness diagnostics:\n%s", r.name, strings.Join(r.problems, "\n  "), r.diag)
				}
			}
		})
		n += len(f.Cases)
	}
	t.Logf("step suite: %d cases in %d files, mode warm", n, len(files))
}

// The checker fails a case whose expectation does not hold: a result, a
// claim, a late report and a warning that differ are each reported.
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
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "--bun", bun, "--repo", repo(t), path}, &stdout, &stderr)
	out := stdout.String()
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out, stderr.String())
	}
	for _, want := range []string{
		"fail wr-6-a ",
		"run 1: /result: 1 != 2",
		"fail wr-6-b ",
		"run 1: /result/error/exit_code: 3 (",
		"run 1: late 1: body \"after\", want \"before\"",
		"run 1: warnings [], want [unattributed_error]",
		"fail wr-6-d ",
		"run 1: warnings [unattributed_error], want []",
		"fail wr-6-e ",
		"run 1: /result/error/stack: \"actual data\" != \"expected data\"",
		"pass wr-6-c ",
		"1 passed, 4 failed, 0 unsupported (mode warm)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The Node side of a CommonJS codebase case (D13) is compared too: a run
// whose node check, or own check, does not hold on Node fails, and a case
// that holds on both passes.
func TestStepCheckerReportsNodeDifferences(t *testing.T) {
	bun := bunOrSkip(t)
	node := nodeOrSkip(t)
	if node == "" {
		t.Skip("WL_NODE is not set")
	}
	dir := filepath.Join(t.TempDir(), "steps", "bun")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "d13.yaml")
	if err := os.WriteFile(path, []byte(`finding: D13
scope: core
cases:
  - spec: D13
    variant: wrong-node
    provenance: policy
    title: a wrong node check
    codebase: {relative_path: f/wl_step}
    script: |
      export function main() { return typeof (globalThis as any).Bun; }
    runs:
      - args: '{}'
        expect: {status: success, result: object}
        node: {expect: {status: success, result: object}}
  - spec: D13
    variant: unstated-difference
    provenance: policy
    title: a difference no node check states
    codebase: {relative_path: f/wl_step}
    script: |
      export function main() { return typeof process.versions.bun; }
    runs:
      - args: '{}'
        expect: {status: success, result: string}
  - spec: D13
    variant: agree
    provenance: policy
    title: a case that holds on both
    codebase: {relative_path: f/wl_step}
    script: |
      export function main(a: number) { return a * 2; }
    runs:
      - args: '{"a":2}'
        expect: {status: success, result: 4}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "--bun", bun, "--node", node, "--repo", repo(t), path}, &stdout, &stderr)
	out := stdout.String()
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out, stderr.String())
	}
	for _, want := range []string{
		"fail d13-wrong-node ",
		`run 1 on node: /result: "undefined" != "object"`,
		"fail d13-unstated-difference ",
		`run 1 on node: /result: "undefined" != "string"`,
		"pass d13-agree ",
		"1 passed, 2 failed, 0 unsupported (mode warm)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// A codebase case whose script fails to build fails alone (c2b review 3,
// finding 5): the codebase build writes the metadata and assets of every
// script that built, so another case of the file, which reads its asset in
// the harness's working directory, still runs and passes.
func TestStepCheckerIsolatesCodebaseFailures(t *testing.T) {
	bun := bunOrSkip(t)
	dir := filepath.Join(t.TempDir(), "steps", "bun")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wr-49.yaml")
	if err := os.WriteFile(path, []byte(`finding: WR-49
scope: core
cases:
  - spec: WR-49
    variant: does-not-build
    provenance: policy
    title: a codebase script that imports a missing module
    codebase: {relative_path: f/wl_step, format: esm}
    script: |
      import { x } from "./missing";
      export function main() { return x; }
    runs:
      - args: '{}'
        expect: {status: success, result: 0}
  - spec: WR-49
    variant: builds
    provenance: policy
    title: a codebase script that reads its asset
    codebase:
      relative_path: f/wl_step
      format: esm
      assets: [{from: f/wl_step/data/a.txt, to: data/a.txt}]
    files:
      f/wl_step/data/a.txt: asset
    script: |
      import { readFileSync } from "node:fs";
      export function main() { return readFileSync("data/a.txt", "utf8"); }
    runs:
      - args: '{}'
        expect: {status: success, result: asset}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "--bun", bun, "--repo", repo(t), path}, &stdout, &stderr)
	out := stdout.String()
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out, stderr.String())
	}
	for _, want := range []string{
		"fail wr-49-does-not-build ",
		"build: bundle_failed: ",
		"pass wr-49-builds ",
		"1 passed, 1 failed, 0 unsupported (mode warm)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
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

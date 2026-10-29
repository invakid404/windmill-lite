package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/platform/facade"
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

// TestStepSuite is the exit gate of milestones C3a and C3b: every step
// case of conformance/steps passes in warm mode and in fresh mode, through
// the real Bun harness on the pinned Bun, each script built by the Bun
// build helper or, for a codebase script, by esbuild. With WL_NODE it is
// also milestone C2b's D13 gate: each CommonJS codebase bundle runs on
// Node too, as on Windmill, and shows its node check there.
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
			sc := &stepChecker{bun: bun, node: node, repo: repo(t), mode: mode}
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
// claim, a late report, a warning, a count of what was left behind or of
// what the native boundary handed over untracked, and a late report after
// settlement that differ are each reported, in both modes.
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
  - spec: WR-6/h
    provenance: source-derived
    title: a wrong count of what the native boundary handed over untracked
    language: nativets
    script: |
      export function main() { return typeof performance.timing; }
    runs:
      - args: '{}'
        expect: {status: success, result: object}
        warnings: [native_untracked]
        untracked: {PerformanceTiming: 2}
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
			"fail wr-6-h ",
			"run 1: untracked map[PerformanceTiming:1], want map[PerformanceTiming:2]",
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
				"1 passed, 7 failed, 0 unsupported (mode warm)",
			)
		} else {
			wants = append(wants, "pass wr-6-d ", "pass wr-6-g ", "3 passed, 5 failed, 0 unsupported (mode fresh)")
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", mode, want, out)
			}
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

// A native invocation's rejection that nobody handles, from a promise a web
// API returned (a method of its own or inherited, a continuation, a stream
// iterator's next, an object an iterator handed over), fails that
// invocation (WR-43/d) and no other, and an
// iteration error the script catches fails none: native and Bun
// invocations in flight beside them in the same process succeed with no
// warning (c3n review, finding 1; round 2, findings 1 and 4).
func TestNativeRejectionIsItsInvocationsOwn(t *testing.T) {
	bun := bunOrSkip(t)
	dir := filepath.Join(t.TempDir(), "steps", "bun")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wr-43.yaml")
	if err := os.WriteFile(path, []byte(`finding: WR-43
scope: core
cases:
  - spec: WR-43
    variant: rejects
    provenance: source-derived
    title: a web API's rejection nobody handles
    language: nativets
    script: |
      export async function main() {
        new Response("invalid json").json();
        crypto.subtle.digest("bad-algorithm", new Uint8Array());
        await new Promise(r => setTimeout(r, 300));
        return "success";
      }
    runs:
      - args: '{}'
        claims:
          - {at: /status, equals: failure}
          - {at: /result/error/name, equals: SyntaxError}
  - spec: WR-43
    variant: continuation-rejects
    provenance: source-derived
    title: a rejection of a continuation of an inherited web API method's promise
    script: |
      //native
      export async function main() {
        new File(["x"], "x").text().then(() => { throw new Error("file rejection"); });
        await new Promise(r => setTimeout(r, 300));
        return "success";
      }
    runs:
      - args: '{}'
        claims:
          - {at: /status, equals: failure}
          - {at: /result/error/message, equals: file rejection}
  - spec: WR-43
    variant: iterator-rejects
    provenance: source-derived
    title: a stream iterator's next nobody awaits
    language: nativets
    script: |
      export async function main() {
        new ReadableStream({ start(c) { c.error(new Error("iterator rejection")); } })[Symbol.asyncIterator]().next();
        await new Promise(r => setTimeout(r, 300));
        return "success";
      }
    runs:
      - args: '{}'
        claims:
          - {at: /status, equals: failure}
          - {at: /result/error/message, equals: iterator rejection}
  - spec: WR-43
    variant: channel-rejects
    provenance: source-derived
    title: a rejection of a continuation of a file FormData's iterator handed over
    language: nativets
    script: |
      export async function main() {
        const form = new FormData();
        form.append("file", new Blob(["abc"]), "a.txt");
        (form.values().next().value as Blob).text().then(() => { throw new Error("channel rejection"); });
        await new Promise(r => setTimeout(r, 300));
        return "success";
      }
    runs:
      - args: '{}'
        claims:
          - {at: /status, equals: failure}
          - {at: /result/error/message, equals: channel rejection}
  - spec: WR-43
    variant: iterator-caught
    provenance: source-derived
    title: a stream iterator's error the script catches
    script: |
      //native
      export async function main() {
        try { for await (const _ of new ReadableStream({ start(c) { c.error(new Error("handled")); } })) {} } catch {}
        await new Promise(r => setTimeout(r, 300));
        return "success";
      }
    runs:
      - args: '{}'
        expect: {status: success, result: success}
  - spec: WR-43
    variant: healthy-native
    provenance: source-derived
    title: a native invocation beside it
    language: nativets
    script: |
      export async function main() { await new Promise(r => setTimeout(r, 600)); return "ok"; }
    runs:
      - args: '{}'
        expect: {status: success, result: ok}
  - spec: WR-43
    variant: healthy-bun
    provenance: source-derived
    title: a Bun invocation beside it
    script: |
      export async function main() { await new Promise(r => setTimeout(r, 600)); return "ok"; }
    runs:
      - args: '{}'
        expect: {status: success, result: ok}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := conformance.LoadStepFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sc := &stepChecker{bun: bun, repo: repo(t), mode: conformance.ModeWarm}
	ctx := context.Background()
	out, digest, problems, err := sc.build(ctx, f, t.TempDir())
	if err != nil || len(problems) > 0 {
		t.Fatalf("build: %v %v", err, problems)
	}
	var diag syncBuffer
	calls := &callLog{byInv: map[string][]facade.Record{}}
	fac, err := facade.New(facade.Config{Recorded: calls.store})
	if err != nil {
		t.Fatal(err)
	}
	defer fac.Close()
	p, err := sc.spawn(ctx, out, digest, &diag, fac)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Drain(dctx)
	}()
	got := make([][]string, len(f.Cases))
	var wg sync.WaitGroup
	for i := range f.Cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &f.Cases[i]
			got[i], _ = sc.runOne(ctx, fac.Executor(p), calls, c, &c.Runs[0], caseID(f, c), newUUID())
		}()
	}
	wg.Wait()
	for i, problems := range got {
		if len(problems) > 0 {
			t.Errorf("%s:\n  %s", f.Cases[i].Name(), strings.Join(problems, "\n  "))
		}
	}
	if t.Failed() {
		t.Logf("harness diagnostics:\n%s", diag.String())
	}
}

// A flow's native steps run through the real build and harness with their
// boundary and the native environment: a nativets step without a //native
// line, one with it, and a bun step with it are each native (no Bun) and
// read BASE_URL and BASE_INTERNAL_URL as the fixture's facade origin, a
// loopback one (milestone PF1), and only the nativets ones declare the job variables as bare names
// (WR-40's note, SC1; WR-45/a; c3n review r3, finding 6).
func TestNativeFlowSteps(t *testing.T) {
	bun := bunOrSkip(t)
	dir := filepath.Join(t.TempDir(), "flows", "languages", "native-steps")
	script := func(lang, header string) map[string]any {
		return map[string]any{
			"type":             "rawscript",
			"language":         lang,
			"content":          header + "export function main() { return [typeof Bun, /^http:\\/\\/127\\.0\\.0\\.1:[0-9]+$/.test(process.env.BASE_URL), process.env.BASE_URL === process.env.BASE_INTERNAL_URL, typeof BASE_URL, typeof WM_WORKSPACE]; }",
			"input_transforms": map[string]any{},
		}
	}
	flow := map[string]any{"summary": "native steps", "value": map[string]any{"modules": []any{
		map[string]any{"id": "nativets", "value": script("nativets", "")},
		map[string]any{"id": "nativetsheader", "value": script("nativets", "//native\n")},
		map[string]any{"id": "bunheader", "value": script("bun", "//native\n")},
	}}}
	nativets := []any{"undefined", true, true, "string", "string"}
	bunnative := []any{"undefined", true, true, "string", "undefined"}
	step := func(result []any) map[string]any {
		return map[string]any{"status": "success", "args": map[string]any{}, "result": result}
	}
	files := map[string]any{
		"fixture.yaml": map[string]any{"id": "languages/native-steps", "spec": []string{"WR-40", "WR-45"}, "scope": "core",
			"provenance": "source-derived", "languages": []string{"nativets", "bun"}, "oracle": map[string]any{"workers": 1}},
		"runs/run.yaml": map[string]any{"spec": []string{"WR-40", "WR-45"}, "target": "f/wl_native/steps", "expect": map[string]any{
			"status": "success", "result": bunnative,
			"steps": map[string]any{"m:nativets": step(nativets), "m:nativetsheader": step(nativets), "m:bunheader": step(bunnative)},
		}},
		"windmill/f/wl_native/steps.flow/flow.yaml": flow,
	}
	for name, v := range files {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "windmill", "wmill.yaml"), []byte("defaultTs: bun\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "--bun", bun, "--repo", repo(t), "-v", dir}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "1 passed, 0 failed, 0 unsupported") {
		t.Fatalf("exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	t.Log(strings.TrimSpace(stdout.String()))
}

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

// TestStepSuite is milestone C3a's exit gate: every step case of
// conformance/steps passes in warm mode, through the real Bun harness on
// the pinned Bun, each script built by the Bun build helper.
func TestStepSuite(t *testing.T) {
	bun := bunOrSkip(t)
	files, err := conformance.FindStepFiles(stepsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no step files")
	}
	sc := &stepChecker{bun: bun, repo: repo(t), mode: conformance.ModeWarm}
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
	p, err := sc.spawn(ctx, out, digest, &diag)
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
			got[i] = sc.runOne(ctx, p, c, &c.Runs[0], caseID(f, c), newUUID())
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
// read BASE_URL and BASE_INTERNAL_URL as the facade origin the step cases
// get, and only the nativets ones declare the job variables as bare names
// (WR-40's note, SC1; WR-45/a; c3n review r3, finding 6).
func TestNativeFlowSteps(t *testing.T) {
	bun := bunOrSkip(t)
	dir := filepath.Join(t.TempDir(), "flows", "languages", "native-steps")
	script := func(lang, header string) map[string]any {
		return map[string]any{
			"type":             "rawscript",
			"language":         lang,
			"content":          header + "export function main() { return [typeof Bun, process.env.BASE_URL, process.env.BASE_URL === process.env.BASE_INTERNAL_URL, typeof BASE_URL, typeof WM_WORKSPACE]; }",
			"input_transforms": map[string]any{},
		}
	}
	flow := map[string]any{"summary": "native steps", "value": map[string]any{"modules": []any{
		map[string]any{"id": "nativets", "value": script("nativets", "")},
		map[string]any{"id": "nativetsheader", "value": script("nativets", "//native\n")},
		map[string]any{"id": "bunheader", "value": script("bun", "//native\n")},
	}}}
	nativets := []any{"undefined", stepFacade, true, "string", "string"}
	bunnative := []any{"undefined", stepFacade, true, "string", "undefined"}
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

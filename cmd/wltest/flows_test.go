package main

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

const (
	flowsDir    = "../../conformance/flows"
	suiteFile   = "testdata/flowsuite.json"
	batchesGlob = "../../internal/oracle/testdata/batches/*.json"
)

// flowSuite is testdata/flowsuite.json: the runs the flow suite requires.
type flowSuite struct {
	About       string            `json:"about"`
	Implemented []string          `json:"implemented"`
	Runs        map[string]string `json:"runs"`
}

// requiredRuns reads the suite and the recording batches' gates, and
// returns every required run (<fixture directory>/<run>, from the
// repository root) with the milestone that requires it.
func requiredRuns(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(suiteFile)
	if err != nil {
		t.Fatal(err)
	}
	var s flowSuite
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.Implemented) == 0 || len(s.Runs) == 0 {
		t.Fatalf("%s lists no implemented milestone or no run", suiteFile)
	}
	req := map[string]string{}
	for run, m := range s.Runs {
		if !slices.Contains(s.Implemented, m) {
			t.Errorf("%s: run %s is required since %s, which implemented does not list", suiteFile, run, m)
		}
		req[run] = m
	}
	batches, err := filepath.Glob(batchesGlob)
	if err != nil || len(batches) == 0 {
		t.Fatalf("no recording batches (%v)", err)
	}
	for _, p := range batches {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var b struct {
			Gates map[string]string `json:"gates"`
		}
		if err := json.Unmarshal(data, &b); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for run, m := range b.Gates {
			if slices.Contains(s.Implemented, m) {
				req[run] = m
			}
		}
	}
	return req
}

// TestFlowSuite is milestone E1's exit gate: every run the flow suite
// requires passes in warm mode, on the walking skeleton and the real Bun
// harness on the pinned Bun, each compared with its expect or recording.
// Every other fixture under conformance/flows also runs: each must load,
// and its runs are reported as passing, failing or unsupported, with the
// reason.
func TestFlowSuite(t *testing.T) {
	bun := bunOrSkip(t)
	req := requiredRuns(t)
	root := repo(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	fc := &flowChecker{bun: bun, repo: root, mode: conformance.ModeWarm, js: js}
	dirs, err := findFixtures([]string{flowsDir})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		res  runResult
		seen bool
	}
	got := map[string]outcome{}
	tally := map[string]int{}
	for _, fr := range fc.runFixtures(context.Background(), dirs) {
		if fr.err != nil {
			t.Errorf("fixture %s does not load: %v", fr.dir, fr.err)
			continue
		}
		rel, err := filepath.Rel(root, mustAbs(t, fr.dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range fr.runs {
			key := filepath.ToSlash(rel) + "/" + r.run
			got[key] = outcome{res: r, seen: true}
			switch {
			case r.unsupported != "":
				tally["unsupported"]++
			case len(r.problems) > 0:
				tally["failed"]++
			default:
				tally["passed"]++
			}
		}
	}
	var extra []string
	for key, o := range got {
		if _, ok := req[key]; !ok && o.res.unsupported == "" && len(o.res.problems) == 0 {
			extra = append(extra, key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(req)) {
		o := got[key]
		switch {
		case !o.seen:
			t.Errorf("required run %s (%s) is not a run of a fixture under %s", key, req[key], flowsDir)
		case o.res.unsupported != "":
			t.Errorf("required run %s (%s) is unsupported: %s", key, req[key], o.res.unsupported)
		case len(o.res.problems) > 0:
			t.Errorf("required run %s (%s) failed:\n  %s\nharness diagnostics:\n%s", key, req[key], strings.Join(o.res.problems, "\n  "), o.res.diag)
		}
	}
	slices.Sort(extra)
	t.Logf("flow suite, mode warm: %d fixtures, %d runs: %d passed, %d failed, %d unsupported; %d required, all passed unless reported above",
		len(dirs), len(got), tally["passed"], tally["failed"], tally["unsupported"], len(req))
	if len(extra) > 0 {
		t.Logf("runs that pass but are not required (add them to %s): %s", suiteFile, strings.Join(extra, ", "))
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// writeTree writes files below dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, s := range files {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The flow checker fails a run whose observation differs from its expect,
// its recording or a claim, names the job placeholders it compared, and
// reports a run the skeleton cannot run as unsupported, not failed.
func TestFlowCheckerReportsDifferences(t *testing.T) {
	bun := bunOrSkip(t)
	dir := filepath.Join(t.TempDir(), "flows", "e1", "checker")
	flow := func(modules string) string {
		return "summary: t\nvalue:\n  modules:\n" + modules + "schema:\n  type: object\n  properties: {}\n  required: []\n"
	}
	inline := func(id string) string {
		return "    - id: " + id + "\n      value:\n        type: rawscript\n        content: '!inline " + id + ".ts'\n        input_transforms: {}\n        language: bun\n"
	}
	writeTree(t, dir, map[string]string{
		"fixture.yaml":                       "id: e1/checker\nspec: [OR-15]\nscope: core\nprovenance: policy\nlanguages: [bun]\n",
		"windmill/wmill.yaml":                "defaultTs: bun\nincludes: [\"f/**\"]\n",
		"windmill/f/t/one.flow/flow.yaml":    flow(inline("a")),
		"windmill/f/t/one.flow/a.ts":         "export async function main() { return 1; }\n",
		"windmill/f/t/job.flow/flow.yaml":    flow(inline("a")),
		"windmill/f/t/job.flow/a.ts":         "export async function main() { return process.env.WM_JOB_ID; }\n",
		"windmill/f/t/two.flow/flow.yaml":    flow(inline("a") + inline("b")),
		"windmill/f/t/two.flow/a.ts":         "export async function main() { throw new Error(\"no\"); }\n",
		"windmill/f/t/two.flow/b.ts":         "export async function main() { return 2; }\n",
		"windmill/f/t/branch.flow/flow.yaml": flow("    - id: o\n      value:\n        type: branchone\n        branches: []\n        default: []\n"),
		"runs/a_wrong_result.yaml":           "target: f/t/one\nexpect:\n  status: success\n  result: 2\n  steps:\n    \"m:a\": {status: success, args: {}, result: 2}\n",
		"runs/b_wrong_job.yaml":              "target: f/t/job\nexpect:\n  status: success\n  result: \"<job:m:b>\"\n  steps:\n    \"m:a\": {status: success, args: {}, result: \"<job:m:b>\"}\n",
		"runs/c_wrong_recording.yaml":        "target: f/t/two\n",
		"runs/d_wrong_claim.yaml":            "target: f/t/two\nclaims:\n  - {at: /steps/m:b/status, equals: success}\n",
		"runs/e_unsupported.yaml":            "target: f/t/branch\nexpect: {status: success, result: {}}\n",
		"runs/f_holds.yaml":                  "target: f/t/two\nexpect:\n  status: failure\n  result: {error: {name: Error, message: \"no\", stack: \"<stack>\", step_id: a}}\n  steps:\n    \"m:a\": {status: failure, args: {}, result: {error: {name: Error, message: \"no\", stack: \"<stack>\", step_id: a}}}\n    \"m:b\": {status: not_run}\n",
		// The recording says b ran; the skeleton's run stops at a.
		"recorded/c_wrong_recording.json": `{"observation":{"status":"failure","result":{"error":{"name":"Error","message":"no","stack":"<stack>","step_id":"a"}},"steps":{"m:a":{"status":"failure","args":{},"result":{"error":{"name":"Error","message":"no","stack":"<stack>","step_id":"a"}}},"m:b":{"status":"success","args":{},"result":2}}},"provenance":{}}`,
	})
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "--bun", bun, "--repo", repo(t), filepath.Dir(dir)}, &stdout, &stderr)
	out := stdout.String()
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out, stderr.String())
	}
	for _, want := range []string{
		"fail " + dir + " a_wrong_result\n  /result: 1 != 2\n  /steps/m:a/result: 1 != 2\n",
		"fail " + dir + " b_wrong_job\n  /result: \"<job:m:a>\" != \"<job:m:b>\"\n",
		"fail " + dir + " c_wrong_recording\n  /steps/m:b/args: (absent) != {}\n  /steps/m:b/ran: false != true\n  /steps/m:b/result: (absent) != 2\n  /steps/m:b/status: \"not_run\" != \"success\"\n",
		"fail " + dir + " d_wrong_claim\n  /steps/m:b/status: \"not_run\" (want \"success\")\n",
		`observed {"status":"success","result":"<job:m:a>",`,
		"unsupported " + dir + " e_unsupported: the walking skeleton does not run it: engine: flow f/t/branch: module \"o\": branchone modules",
		"pass " + dir + " f_holds\n",
		"1 passed, 4 failed, 1 unsupported (mode warm)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// observeTrace builds the observation the oracle's walker would: stored
// values in their stored form, the module after a failure not run, job ids
// as placeholders, the failed leaf's stack masked wherever its value is
// carried, and the texts of carried and resolved_args kept where they
// differ from the stored ones.
func TestObserveTrace(t *testing.T) {
	const (
		runID = "11111111-1111-4111-8111-111111111111"
		jobA  = "22222222-2222-4222-8222-222222222222"
		jobB  = "33333333-3333-4333-8333-333333333333"
	)
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	site := func(id string) conformance.SitePath { return conformance.SitePath{}.Child(conformance.Module(id)) }
	failure := `{"error":{"name":"Error","message":"no","stack":"Error: no\n    at main (a.ts:1:1)","step_id":"b"}}`
	tr := &engine.Trace{
		RunID: runID, Status: conformance.RunFailure, Result: v(failure), ResultOrigin: engine.OriginUser,
		Steps: []engine.StepRecord{
			{
				Site: site("a"), Status: conformance.StepSuccess, Module: conformance.ModuleSuccess, Ran: true,
				RecordedArgs: v(`{"zz":1,"a":"` + runID + `"}`), ResolvedArgs: v(`{"zz":1,"a":"` + runID + `"}`),
				Result: v(`{"job":"` + jobA + `","bb":1.0}`), Carried: v(`{"job":"` + jobA + `","bb":1.0}`),
			},
			{
				Site: site("b"), Status: conformance.StepFailure, Module: conformance.ModuleFailure, Ran: true,
				RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: v(failure), Carried: v(failure), ResultOrigin: engine.OriginUser,
			},
		},
	}
	got, generated, err := observeTrace(tr, []string{"a", "b", "c"}, map[string]conformance.SitePath{jobA: site("a"), jobB: site("b")}, conformance.CompareSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != 0 {
		t.Fatalf("generated %v, want none", generated)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	g, err := conformance.IndentJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	// Stored values come in jsonb's member order, shorter keys first.
	masked := `{"error":{"name":"Error","message":"no","stack":"<stack>","step_id":"b"}}`
	stored := `{"error":{"name":"Error","stack":"<stack>","message":"no","step_id":"b"}}`
	want, err := conformance.IndentJSON([]byte(`{"status":"failure","result":` + stored + `,"steps":{` +
		`"m:a":{"status":"success","args":{"a":"<job:>","zz":1},"resolved_args":{"zz":1,"a":"<job:>"},"result":{"bb":1.0,"job":"<job:m:a>"},"carried":{"job":"<job:m:a>","bb":1.0}},` +
		`"m:b":{"status":"failure","args":{},"result":` + stored + `,"carried":` + masked + `},` +
		`"m:c":{"status":"not_run","ran":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g, want) {
		t.Fatalf("got\n%s\nwant\n%s", g, want)
	}
	// Against what a recording holds, which has no carried or resolved
	// arguments, the kept texts differ only in member order: a loose
	// comparison ignores it and a strict one does not (OR-13, ADR 0011).
	var expect conformance.Observation
	if err := json.Unmarshal([]byte(`{"status":"failure","result":`+stored+`,"steps":{"m:a":{"status":"success","args":{"a":"<job:>","zz":1},"result":{"bb":1.0,"job":"<job:m:a>"}},"m:b":{"status":"failure","args":{},"result":`+stored+`},"m:c":{"status":"not_run"}}}`), &expect); err != nil {
		t.Fatal(err)
	}
	if d, err := conformance.Compare(got, expect, conformance.CompareOptions{}); err != nil || len(d) != 0 {
		t.Fatalf("loose: %v %v", d, err)
	}
	d, err := conformance.Compare(got, expect, conformance.CompareOptions{StrictKeyOrder: true})
	if err != nil {
		t.Fatal(err)
	}
	var ptrs []string
	for _, x := range d {
		ptrs = append(ptrs, x.Pointer)
	}
	if !slices.Equal(ptrs, []string{"/steps/m:a/carried", "/steps/m:a/resolved_args", "/steps/m:b/carried/error"}) {
		t.Fatalf("strict: %v", d)
	}

	// A generated error is named for the location rule, and a run with no
	// result observes null.
	tr = &engine.Trace{RunID: runID, Status: conformance.RunFailure, ResultOrigin: engine.OriginEngine, Steps: []engine.StepRecord{
		{Site: site("a"), Status: conformance.StepFailure, Module: conformance.ModuleFailure, Ran: true, ResultOrigin: engine.OriginHarness,
			RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: v(`{"error":{"name":"InternalErr","message":"m"}}`), Carried: v(`{"error":{"name":"InternalErr","message":"m"}}`)},
	}}
	got, generated, err = observeTrace(tr, []string{"a"}, nil, conformance.CompareSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(generated, []string{"/steps/m:a/result", "/result"}) || string(got.Result) != "null" {
		t.Fatalf("generated %v, result %s", generated, got.Result)
	}
}

// compareRun compares with the expect when the run has one, carried and
// resolved_args included, and otherwise with the recording, which has
// neither; it strips the oracle's location suffix only where windmill-lite
// generated the value (ADR 0011).
func TestCompareRun(t *testing.T) {
	obs := func(s string) conformance.Observation {
		t.Helper()
		var o conformance.Observation
		if err := json.Unmarshal([]byte(s), &o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	f := &conformance.Fixture{}
	internal := func(loc string) string {
		return `{"error":{"name":"InternalErr","message":"Internal: Could not add completed job <job:m:a>: error returned from database: unsupported Unicode escape sequence` + loc + `"}}`
	}
	step := func(result, carried string) string {
		s := `{"status":"failure","result":` + result + `,"steps":{"m:a":{"status":"failure","args":{},"result":` + result
		if carried != "" {
			s += `,"carried":` + carried
		}
		return s + `}}}`
	}
	got := obs(step(internal(""), ""))
	withLoc := obs(step(internal(" <loc>"), ""))
	for _, c := range []struct {
		name      string
		run       conformance.Run
		got       conformance.Observation
		generated []string
		problems  int
	}{
		{"generated value, expect", conformance.Run{Expect: &withLoc}, got, []string{"/result", "/steps/m:a/result"}, 0},
		{"generated value, recording", conformance.Run{Recorded: &conformance.Recording{Observation: withLoc}}, got, []string{"/result", "/steps/m:a/result"}, 0},
		{"user value", conformance.Run{Expect: &withLoc}, got, nil, 2},
		{"carried, expect", conformance.Run{Expect: &got}, obs(step(internal(""), `{"other":1}`)), nil, 2},
		{"carried, recording", conformance.Run{Recorded: &conformance.Recording{Observation: got}}, obs(step(internal(""), `{"other":1}`)), nil, 0},
		{"nothing to compare", conformance.Run{}, got, nil, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Every failed comparison ends with the observation itself.
			var lines []string
			for _, p := range compareRun(f, c.run, c.got, c.generated) {
				if !strings.HasPrefix(p, "observed ") {
					lines = append(lines, p)
				}
			}
			if len(lines) != c.problems {
				t.Fatalf("problems %q, want %d", lines, c.problems)
			}
		})
	}
}

// compactJSON writes back what encoding/json escapes for HTML and nothing
// else: a user's backslash before "u003c" stays an escaped backslash.
func TestCompactJSON(t *testing.T) {
	o := conformance.Observation{Status: conformance.RunSuccess, Result: json.RawMessage(`["<job:m:a>","a&b",">","\\u003c","\"q\"","\u00e9"]`)}
	const want = `{"status":"success","result":["<job:m:a>","a&b",">","\\u003c","\"q\"","\u00e9"]}`
	if got := compactJSON(o); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

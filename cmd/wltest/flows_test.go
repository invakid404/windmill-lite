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
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

const (
	flowsDir    = "../../conformance/flows"
	exprsDir    = "../../conformance/exprs"
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

// TestFlowSuite is the exit gate of milestones E1, N1a and F1, and in host
// mode part of F3b's: every run the flow suite requires passes in warm and
// in fresh mode, through wl on the engine and the real Bun harness on the
// pinned Bun, and in host mode, on a Bun host that embeds the host SDK and
// runs the steps in its own process (hostmode.go), each compared with its
// expect or recording. Every other fixture under
// conformance/flows, and every expression file under conformance/exprs as
// its generated one-step flow fixture, also runs: each must load, and its
// runs are reported as passing, failing or unsupported, with the reason.
func TestFlowSuite(t *testing.T) {
	bun := bunOrSkip(t)
	req := requiredRuns(t)
	root := repo(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	dirs, err := findFixtures([]string{flowsDir})
	if err != nil {
		t.Fatal(err)
	}
	exprs, err := conformance.FindExprFiles(exprsDir)
	if err != nil || len(exprs) == 0 {
		t.Fatalf("no expression files under %s (%v)", exprsDir, err)
	}
	dirs = append(dirs, exprs...)
	for _, mode := range []string{conformance.ModeWarm, conformance.ModeFresh, ModeHost} {
		t.Run(mode, func(t *testing.T) {
			fc := &flowChecker{bun: bun, repo: root, mode: mode, js: js}
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
					t.Errorf("required run %s (%s) is not a run of a fixture under %s or of an expression file under %s", key, req[key], flowsDir, exprsDir)
				case o.res.unsupported != "":
					t.Errorf("required run %s (%s) is unsupported: %s", key, req[key], o.res.unsupported)
				case len(o.res.problems) > 0:
					t.Errorf("required run %s (%s) failed:\n  %s\nharness diagnostics:\n%s", key, req[key], strings.Join(o.res.problems, "\n  "), o.res.diag)
				}
			}
			slices.Sort(extra)
			t.Logf("flow suite, mode %s: %d fixtures, %d runs: %d passed, %d failed, %d unsupported; %d required, all passed unless reported above",
				mode, len(dirs), len(got), tally["passed"], tally["failed"], tally["unsupported"], len(req))
			if len(extra) > 0 {
				t.Logf("runs that pass but are not required (add them to %s): %s", suiteFile, strings.Join(extra, ", "))
			}
		})
	}
}

// Milestone N4's gate (ADR 0012): the E3d FX-40 cases, the fixture
// conformance/flows/execution/fx-40, pass on windmill-lite in fresh and in
// warm mode, since the result cache lives in the engine, not in a harness
// process (FX-40, D4). Every run of the fixture runs and passes in both.
func TestCacheFixturesFreshAndWarm(t *testing.T) {
	bun := bunOrSkip(t)
	root := repo(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	dir := filepath.Join(flowsDir, "execution", "fx-40")
	for _, mode := range []string{conformance.ModeFresh, conformance.ModeWarm} {
		t.Run(mode, func(t *testing.T) {
			fc := &flowChecker{bun: bun, repo: root, mode: mode, js: js}
			n := 0
			for _, fr := range fc.runFixtures(context.Background(), []string{dir}) {
				if fr.err != nil {
					t.Fatalf("fixture %s does not load: %v", fr.dir, fr.err)
				}
				for _, r := range fr.runs {
					n++
					switch {
					case r.unsupported != "":
						t.Errorf("run %s is unsupported: %s", r.run, r.unsupported)
					case len(r.problems) > 0:
						t.Errorf("run %s failed:\n  %s\nharness diagnostics:\n%s", r.run, strings.Join(r.problems, "\n  "), r.diag)
					}
				}
			}
			if n != 7 {
				t.Fatalf("%d runs, want FX-40's 7", n)
			}
		})
	}
}

// Milestone N5's gate (ADR 0012): the control-flow/subflow-retry runs, a
// retry on a flow step recorded on the oracle, and a retry on a while loop
// and a branchone, match their recordings on windmill-lite in fresh and in
// warm mode. Every run of the fixture runs and passes in both.
func TestSubflowRetryFixturesFreshAndWarm(t *testing.T) {
	bun := bunOrSkip(t)
	root := repo(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	dir := filepath.Join(flowsDir, "control-flow", "subflow-retry")
	for _, mode := range []string{conformance.ModeFresh, conformance.ModeWarm} {
		t.Run(mode, func(t *testing.T) {
			fc := &flowChecker{bun: bun, repo: root, mode: mode, js: js}
			n := 0
			for _, fr := range fc.runFixtures(context.Background(), []string{dir}) {
				if fr.err != nil {
					t.Fatalf("fixture %s does not load: %v", fr.dir, fr.err)
				}
				for _, r := range fr.runs {
					n++
					switch {
					case r.unsupported != "":
						t.Errorf("run %s is unsupported: %s", r.run, r.unsupported)
					case len(r.problems) > 0:
						t.Errorf("run %s failed:\n  %s\nharness diagnostics:\n%s", r.run, strings.Join(r.problems, "\n  "), r.diag)
					}
				}
			}
			if n != 11 {
				t.Fatalf("%d runs, want the fixture's 11", n)
			}
		})
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
// reports a run the engine cannot run yet as unsupported, not failed. Two
// scripts that share a job path both run, each seeing that path, and a
// script that does not build fails only the runs that reach it.
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
		"fixture.yaml":                      "id: e1/checker\nspec: [OR-15]\nscope: core\nprovenance: policy\nlanguages: [bun]\n",
		"windmill/wmill.yaml":               "defaultTs: bun\nincludes: [\"f/**\"]\n",
		"windmill/f/t/one.flow/flow.yaml":   flow(inline("a")),
		"windmill/f/t/one.flow/a.ts":        "export async function main() { return 1; }\n",
		"windmill/f/t/job.flow/flow.yaml":   flow(inline("a")),
		"windmill/f/t/job.flow/a.ts":        "export async function main() { return process.env.WM_JOB_ID; }\n",
		"windmill/f/t/two.flow/flow.yaml":   flow(inline("a") + inline("b")),
		"windmill/f/t/two.flow/a.ts":        "export async function main() { throw new Error(\"no\"); }\n",
		"windmill/f/t/two.flow/b.ts":        "export async function main() { return 2; }\n",
		"windmill/f/t/cache.flow/flow.yaml": flow(inline("a") + "      cache_ttl: 60\n      cache_ignore_s3_path: true\n"),
		"windmill/f/t/cache.flow/a.ts":      "export async function main() { return 1; }\n",
		// Inline step a of f/t/same has the job path f/t/same/a, which is
		// also the path of the script that step b runs (WR-29): two scripts,
		// one job path.
		"windmill/f/t/same.flow/flow.yaml":   flow(inline("a") + "    - id: b\n      value:\n        type: script\n        input_transforms: {}\n        path: f/t/same/a\n"),
		"windmill/f/t/same.flow/a.ts":        "export async function main() { return [1, process.env.WM_JOB_PATH]; }\n",
		"windmill/f/t/same/a.ts":             "export async function main() { return [2, process.env.WM_JOB_PATH]; }\n",
		"windmill/f/t/same/a.script.yaml":    "summary: ''\ndescription: ''\nkind: script\nschema:\n  type: object\n  properties: {}\n  required: []\n",
		"windmill/f/t/broken.flow/flow.yaml": flow(inline("a")),
		"windmill/f/t/broken.flow/a.ts":      "export async function main( { return 1; }\n",
		"runs/a_wrong_result.yaml":           "target: f/t/one\nexpect:\n  status: success\n  result: 2\n  steps:\n    \"m:a\": {status: success, args: {}, result: 2}\n",
		"runs/b_wrong_job.yaml":              "target: f/t/job\nexpect:\n  status: success\n  result: \"<job:m:b>\"\n  steps:\n    \"m:a\": {status: success, args: {}, result: \"<job:m:b>\"}\n",
		"runs/c_wrong_recording.yaml":        "target: f/t/two\n",
		"runs/d_wrong_claim.yaml":            "target: f/t/two\nclaims:\n  - {at: /steps/m:b/status, equals: success}\n",
		"runs/e_unsupported.yaml":            "target: f/t/cache\nexpect: {status: success, result: 1}\n",
		"runs/g_same_job_path.yaml":          "target: f/t/same\nexpect:\n  status: success\n  result: [2, f/t/same/a]\n  steps:\n    \"m:a\": {status: success, args: {}, result: [1, f/t/same/a]}\n    \"m:b\": {status: success, args: {}, result: [2, f/t/same/a]}\n",
		"runs/h_build_problem.yaml":          "target: f/t/broken\nexpect: {status: success, result: 1}\n",
		"runs/f_holds.yaml":                  "target: f/t/two\nexpect:\n  status: failure\n  result: {error: {name: Error, message: \"no\", stack: \"<stack>\", step_id: a}}\n  steps:\n    \"m:a\": {status: failure, args: {}, result: {error: {name: Error, message: \"no\", stack: \"<stack>\", step_id: a}}}\n    \"m:b\": {status: not_run}\n",
		// The recording says b ran; the engine's run stops at a.
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
		"unsupported " + dir + " e_unsupported: the engine does not run it yet: engine: flow f/t/cache: module \"a\": cache_ignore_s3_path (later)",
		"pass " + dir + " f_holds\n",
		"pass " + dir + " g_same_job_path\n",
		// A script that does not build fails only the runs of the flows
		// that reach it.
		"fail " + dir + " h_build_problem\n  build: f/t/broken step 'a' (script inline-",
		"): parse_failed: ",
		"2 passed, 5 failed, 1 unsupported (mode warm)",
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
	got, generated, err := observeTrace(tr, []string{"a", "b", "c"}, nil, false, map[string]conformance.SitePath{jobA: site("a"), jobB: site("b")}, conformance.CompareSpec{}, nil, nil)
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

	// A generated error, and the carried value that is the same value, are
	// named for the location rule, and a run with no result observes null.
	tr = &engine.Trace{RunID: runID, Status: conformance.RunFailure, ResultOrigin: engine.OriginEngine, Steps: []engine.StepRecord{
		{Site: site("a"), Status: conformance.StepFailure, Module: conformance.ModuleFailure, Ran: true, ResultOrigin: engine.OriginHarness,
			RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: v(`{"error":{"name":"InternalErr","message":"m"}}`), Carried: v(`{"error":{"name":"InternalErr","message":"m"}}`)},
	}}
	got, generated, err = observeTrace(tr, []string{"a"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(generated, []string{"/steps/m:a/result", "/steps/m:a/carried", "/result"}) || string(got.Result) != "null" {
		t.Fatalf("generated %v, result %s", generated, got.Result)
	}
}

// The walker's sites of retries (FX-27, FX-29, OR-11): a failed attempt
// at <module>/r:<k>, met again at <module>/r:<k> of every module whose
// status lists it, with the placeholder of its own site; the virtual
// <id>-v step of a final sleep left out (OF-17, OR-V7).
func TestObserveTraceRetries(t *testing.T) {
	const (
		runID = "11111111-1111-4111-8111-111111111111"
		jobA0 = "22222222-2222-4222-8222-222222222222"
		jobA  = "33333333-3333-4333-8333-333333333333"
		jobB  = "44444444-4444-4444-8444-444444444444"
	)
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	site := func(id string) conformance.SitePath { return conformance.SitePath{}.Child(conformance.Module(id)) }
	a0 := site("a").Child(conformance.Attempt(0))
	fail := func(job string) jsonval.Value {
		return v(`{"error":{"name":"E","message":"` + job + `","step_id":"a"}}`)
	}
	tr := &engine.Trace{
		RunID: runID, Status: conformance.RunFailure, Result: fail(jobB), ResultOrigin: engine.OriginUser,
		Steps: []engine.StepRecord{
			{Site: site("a"), Attempt: 1, Status: conformance.StepFailure, Module: conformance.ModuleFailure, Ran: true, Job: jobA,
				RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: fail(jobA), Carried: fail(jobA), ResultOrigin: engine.OriginUser,
				FailedRetries: []conformance.SitePath{a0}},
			{Site: a0, Status: conformance.StepFailure, Ran: true, Job: jobA0,
				RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: fail(jobA0), ResultOrigin: engine.OriginUser},
			{Site: site("b"), Status: conformance.StepFailure, Module: conformance.ModuleFailure, Ran: true, Job: jobB,
				RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: fail(jobB), Carried: fail(jobB), ResultOrigin: engine.OriginUser,
				FailedRetries: []conformance.SitePath{a0}},
			{Site: site("b-v"), Status: conformance.StepSuccess, Module: conformance.ModuleSuccess, Ran: true,
				RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: v(`{}`), Carried: v(`{}`)},
		},
	}
	// The executor saw every invocation at its module's site; the records
	// name the attempt's job by its own.
	jobs := map[string]conformance.SitePath{jobA0: site("a"), jobA: site("a"), jobB: site("b")}
	got, _, err := observeTrace(tr, []string{"a", "b", "b-v"}, []bool{false, false, true}, false, jobs, conformance.CompareSpec{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var want conformance.Observation
	if err := json.Unmarshal([]byte(`{"status":"failure","result":{"error":{"name":"E","message":"<job:m:b>","step_id":"a"}},"steps":{`+
		`"m:a/r:0":{"status":"failure","args":{},"result":{"error":{"name":"E","message":"<job:m:a/r:0>","step_id":"a"}}},`+
		`"m:a":{"status":"failure","args":{},"result":{"error":{"name":"E","message":"<job:m:a>","step_id":"a"}}},`+
		`"m:b/r:0":{"status":"failure","args":{},"result":{"error":{"name":"E","message":"<job:m:a/r:0>","step_id":"a"}}},`+
		`"m:b":{"status":"failure","args":{},"result":{"error":{"name":"E","message":"<job:m:b>","step_id":"a"}}}}}`), &want); err != nil {
		t.Fatal(err)
	}
	if d, err := conformance.Compare(got, want, conformance.CompareOptions{}); err != nil || len(d) != 0 {
		t.Fatalf("%v %v\nobserved %s", d, err, compactJSON(got))
	}

	// A listed attempt with no record is the engine's fault, reported.
	tr.Steps = tr.Steps[:1]
	if _, _, err := observeTrace(tr, []string{"a"}, nil, false, nil, conformance.CompareSpec{}, nil, nil); err == nil {
		t.Fatal("a listed attempt with no record was observed")
	}
}

// checkDelays reads the trace's job times: the To site's job starts at
// least the delay after the From site's job ended, and a site with no job
// is reported.
func TestCheckDelays(t *testing.T) {
	at := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	site := func(id string) conformance.SitePath { return conformance.SitePath{}.Child(conformance.Module(id)) }
	tr := &engine.Trace{Steps: []engine.StepRecord{
		{Site: site("a"), Ran: true, Start: at, Duration: 100 * time.Millisecond},
		{Site: site("b"), Ran: true, Start: at.Add(1600 * time.Millisecond), Duration: time.Millisecond},
		{Site: site("c")},
	}}
	if p := checkDelays(tr, []conformance.Delay{{From: "m:a", To: "m:b", AtLeastMS: 1500}}); len(p) != 0 {
		t.Fatalf("1.5 s: %v", p)
	}
	if p := checkDelays(tr, []conformance.Delay{{From: "m:a", To: "m:b", AtLeastMS: 1501}}); len(p) != 1 || !strings.Contains(p[0], "want at least 1501 ms") {
		t.Fatalf("1.501 s: %v", p)
	}
	if p := checkDelays(tr, []conformance.Delay{{From: "m:a", To: "m:c", AtLeastMS: 1}, {From: "m:z", To: "m:b", AtLeastMS: 1}}); len(p) != 2 {
		t.Fatalf("no job: %v", p)
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
	notFound := func(loc string) string {
		return `{"error":{"name":"ExecutionErr","message":"Error during isolated evaluation of expression ` + "`[results.br]`" +
			`:\nQuickJS evaluation error: Error: Failed to fetch result for step 'br': Not found: Completed Job not found at name x` + loc + `\n"}}`
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
		// FX-50 rule 3: a declared not-found location goes where
		// windmill-lite generated the value, and nowhere else.
		{"declared not-found location", conformance.Run{Recorded: &conformance.Recording{Observation: obs(step(notFound(" (jobs.rs:9074)"), ""))}, NotFoundLocs: []string{"/result", "/steps/m:a/result"}},
			obs(step(notFound(""), "")), []string{"/result", "/steps/m:a/result"}, 0},
		{"undeclared not-found location", conformance.Run{Recorded: &conformance.Recording{Observation: obs(step(notFound(" (jobs.rs:9074)"), ""))}, NotFoundLocs: []string{"/result"}},
			obs(step(notFound(""), "")), []string{"/result", "/steps/m:a/result"}, 1},
		{"declared at a user value", conformance.Run{Recorded: &conformance.Recording{Observation: obs(step(notFound(" (user.rs:123)"), ""))}, NotFoundLocs: []string{"/result", "/steps/m:a/result"}},
			obs(step(notFound(""), "")), nil, 2},
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

// CF-22, FX-29, OR-11: a retried loop's failed attempt is its failed
// child flow, walked at m:l/r:0 with the child's modules below it; after a
// tolerated failure the next module lists that attempt, and the walker
// walks the attempt's flow again below m:d/r:0.
func TestObserveContainerAttempts(t *testing.T) {
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	// Each iteration's child fails at its pickup: its first module's
	// iterator is no array.
	inner := &ir.Module{ID: "inner", Kind: ir.KindForLoop, ForLoop: &ir.ForLoop{
		Iterator: ir.Transform{Type: ir.Javascript, Expr: expr.Compile(expr.SiteIterator, "({})")},
		Modules:  []*ir.Module{{ID: "u", Kind: ir.KindIdentity}},
	}}
	loop := &ir.Module{ID: "l", Kind: ir.KindForLoop, ContinueOnError: true,
		Retry:   &ir.Retry{Constant: &ir.ConstantRetry{Attempts: 1}},
		ForLoop: &ir.ForLoop{Iterator: ir.Transform{Type: ir.Static, Value: json.RawMessage(`[1]`)}, Modules: []*ir.Module{inner, {ID: "x", Kind: ir.KindIdentity}}},
	}
	e, err := engine.New(engine.Config{JS: js, Program: &ir.Program{Flows: map[string]*ir.Flow{"f/t/main": {Path: "f/t/main", Modules: []*ir.Module{loop, {ID: "d", Kind: ir.KindIdentity}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := e.Run(context.Background(), engine.RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := observeTrace(tr, []string{"l", "d"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for site, status := range map[string]conformance.StepStatus{
		"m:l": conformance.StepFailure, "m:l/i:0/m:inner": conformance.StepNotRun, "m:l/i:0/m:x": conformance.StepNotRun,
		"m:l/r:0": conformance.StepFailure, "m:l/r:0/m:inner": conformance.StepNotRun, "m:l/r:0/m:x": conformance.StepNotRun,
		"m:d":     conformance.StepSuccess,
		"m:d/r:0": conformance.StepFailure, "m:d/r:0/m:inner": conformance.StepNotRun, "m:d/r:0/m:x": conformance.StepNotRun,
	} {
		p, _ := conformance.ParseSitePath(site)
		if st, ok := got.Steps[p]; !ok || st.Status != status {
			t.Errorf("%s: %+v (present %v), want %s", site, st, ok, status)
		}
	}
	if len(got.Steps) != 10 {
		t.Fatalf("observed %s", compactJSON(got))
	}
	if r, d := got.Steps[conformance.SitePath{}.Child(conformance.Module("l"), conformance.Attempt(0))], got.Steps[conformance.SitePath{}.Child(conformance.Module("d"), conformance.Attempt(0))]; string(r.Args) != `{"iter":{"index":0,"value":1}}` || string(d.Result) != string(r.Result) {
		t.Fatalf("m:l/r:0 %+v, m:d/r:0 %+v", r, d)
	}
}

// CF-22, FX-29, OR-11: when a later module lists a retried container's
// failed attempt, the walker walks the attempt's child flow again below
// it, each module with the attempts its own status lists: a body module
// that lists an earlier body module's attempt (FX-29) shows it below the
// copy too.
func TestObserveNestedAttemptCopies(t *testing.T) {
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	// A branchone whose predicate throws fails in place (CF-14).
	fail := func(id string) *ir.Module {
		return &ir.Module{ID: id, Kind: ir.KindBranchOne, BranchOne: &ir.BranchOne{Branches: []ir.OneBranch{{Expr: expr.Compile(expr.SiteBranchOne, "missing.x"), Modules: []*ir.Module{{ID: "unused", Kind: ir.KindIdentity}}}}}}
	}
	a := fail("a")
	a.ContinueOnError, a.Retry = true, &ir.Retry{Constant: &ir.ConstantRetry{Attempts: 1}}
	loop := &ir.Module{ID: "l", Kind: ir.KindForLoop, ContinueOnError: true, Retry: &ir.Retry{Constant: &ir.ConstantRetry{Attempts: 1}},
		ForLoop: &ir.ForLoop{Iterator: ir.Transform{Type: ir.Static, Value: json.RawMessage(`[1]`)}, Modules: []*ir.Module{a, {ID: "b", Kind: ir.KindIdentity}, fail("c")}}}
	e, err := engine.New(engine.Config{JS: js, Program: &ir.Program{Flows: map[string]*ir.Flow{"f/t/main": {Path: "f/t/main", Modules: []*ir.Module{loop, {ID: "d", Kind: ir.KindIdentity}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := e.Run(context.Background(), engine.RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := observeTrace(tr, []string{"l", "d"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"m:l/i:0", "m:l/r:0", "m:d/r:0"} {
		for _, s := range []string{"/m:a", "/m:a/r:0", "/m:b", "/m:b/r:0", "/m:c"} {
			p, _ := conformance.ParseSitePath(prefix + s)
			if _, ok := got.Steps[p]; !ok {
				t.Errorf("no site %s%s in %s", prefix, s, compactJSON(got))
			}
		}
	}
	if len(got.Steps) != 2+1+5+1+5+5 {
		t.Fatalf("%d sites: %s", len(got.Steps), compactJSON(got))
	}
}

// A child flow's modules are sites below its iteration's, a module it
// has no record of is not_run, and its id is its iteration's site (OR-11);
// a module with no result shows no carried (FX-15 context 2); a carried
// value the trace says the engine produced is generated even when it
// differs from the result (FX-V2).
func TestObserveChildFlows(t *testing.T) {
	const child = "44444444-4444-4444-8444-444444444444"
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	p := func(s string) conformance.SitePath {
		site, err := conformance.ParseSitePath(s)
		if err != nil {
			t.Fatal(err)
		}
		return site
	}
	stop := `{"error":{"name":"EarlyStopError","message":"halt"}}`
	tr := &engine.Trace{
		RunID: "root", Status: conformance.RunFailure, Result: v(`[` + stop + `]`),
		Children: []engine.ChildFlow{{Site: p("m:l/i:0"), ID: child, Modules: []string{"b", "c"}}},
		Steps: []engine.StepRecord{
			{Site: p("m:l"), Status: conformance.StepFailure, Module: conformance.ModuleFailure, Ran: true, Result: v(`[` + stop + `]`), Carried: v(`[` + stop + `]`)},
			{Site: p("m:l/i:0/m:b"), Status: conformance.StepSuccess, Module: conformance.ModuleFailure, Ran: true, RecordedArgs: v(`{"f":"` + child + `"}`), ResolvedArgs: v(`{"f":"` + child + `"}`),
				Result: v(`1`), Carried: v(stop), CarriedOrigin: engine.OriginEngine},
			{Site: p("m:p"), Status: conformance.StepFailure, Module: conformance.ModuleFailure, Carried: v(`{"error":{"name":"Anyhow","message":"Error: x"}}`), CarriedOrigin: engine.OriginEngine},
		},
	}
	got, generated, err := observeTrace(tr, []string{"l", "p", "n"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(generated, []string{"/steps/m:l~1i:0~1m:b/carried"}) {
		t.Fatalf("generated %v", generated)
	}
	var want conformance.Observation
	if err := json.Unmarshal([]byte(`{"status":"failure","result":[`+stop+`],"steps":{`+
		`"m:l":{"status":"failure","result":[`+stop+`]},`+
		`"m:l/i:0/m:b":{"status":"success","module":"failure","args":{"f":"<job:m:l/i:0>"},"result":1,"carried":`+stop+`},`+
		`"m:l/i:0/m:c":{"status":"not_run"},`+
		`"m:p":{"status":"failure","ran":false},`+
		`"m:n":{"status":"not_run"}}}`), &want); err != nil {
		t.Fatal(err)
	}
	if d, err := conformance.Compare(got, want, conformance.CompareOptions{}); err != nil || len(d) != 0 {
		t.Fatalf("differences %v %v\ngot %s", d, err, compactJSON(got))
	}
}

// Inside a loop's array the location rule reaches exactly the errors the
// trace points to as generated (StepRecord.ResultNested), on the loop's
// site and on the run's result; a user's error of the same shape keeps
// its difference (ADR 0011).
func TestObserveNestedGenerated(t *testing.T) {
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	site := conformance.SitePath{}.Child(conformance.Module("l"))
	arr := `[{"error":{"name":"Anyhow","message":"Error: x"}},{"error":{"name":"Anyhow","message":"Error: y"}}]`
	trace := func(nested []string) *engine.Trace {
		return &engine.Trace{RunID: "root", Status: conformance.RunFailure, Result: v(arr), ResultNested: nested, Steps: []engine.StepRecord{{
			Site: site, Status: conformance.StepFailure, Module: conformance.ModuleFailure, Ran: true,
			Result: v(arr), Carried: v(arr), ResultNested: nested, CarriedNested: nested,
		}}}
	}
	// The oracle's side: both entries end in a pickup location.
	located := `[{"error":{"name":"Anyhow","message":"Error: x @worker.rs:3822:13"}},{"error":{"name":"Anyhow","message":"Error: y @worker.rs:3822:13"}}]`
	var expect conformance.Observation
	if err := json.Unmarshal([]byte(`{"status":"failure","result":`+located+`,"steps":{"m:l":{"status":"failure","result":`+located+`}}}`), &expect); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		nested, differs []string
	}{
		{[]string{"/0", "/1"}, nil},
		{[]string{"/1"}, []string{"/result/0/error/message", "/steps/m:l/result/0/error/message"}},
		{nil, []string{"/result/0/error/message", "/result/1/error/message", "/steps/m:l/result/0/error/message", "/steps/m:l/result/1/error/message"}},
	} {
		got, generated, err := observeTrace(trace(c.nested), []string{"l"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var differs []string
		for _, p := range compareRun(&conformance.Fixture{}, conformance.Run{Expect: &expect}, got, generated) {
			if !strings.HasPrefix(p, "observed ") {
				ptr, _, _ := strings.Cut(p, ": ")
				differs = append(differs, ptr)
			}
		}
		slices.Sort(differs)
		if !slices.Equal(differs, c.differs) {
			t.Fatalf("nested %v: differences at %q, want %q (generated %v)", c.nested, differs, c.differs, generated)
		}
	}
}

// An error stop that includes a loop's array (CF-13): run end to end on
// the engine, the location rule reaches the engine's error inside the
// embedded array, on the run's result and on the loop's site, and leaves
// a user's lookalike beside it, an Anyhow-shaped value a mocked step
// returns, with its difference (ADR 0011).
func TestObserveIncludedResult(t *testing.T) {
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	lookalike := `{"error":{"name":"Anyhow","message":"Error: lookalike"}}`
	stop, include := "stop", true
	// Iteration 0's inner loop fails its child's pickup with the engine's
	// Anyhow; iteration 1's returns [lookalike].
	inner := &ir.Module{ID: "inner", Kind: ir.KindForLoop, ForLoop: &ir.ForLoop{
		Iterator: ir.Transform{Type: ir.Javascript, Expr: expr.Compile(expr.SiteIterator, "flow_input.iter.value === 0 ? ({}) : [0]")},
		Modules:  []*ir.Module{{ID: "u", Kind: ir.KindIdentity, Mock: &ir.Mock{Enabled: true, ReturnValue: json.RawMessage(lookalike)}}},
	}}
	outer := &ir.Module{ID: "l", Kind: ir.KindForLoop,
		StopAfterAllItersIf: &ir.Stop{Expr: expr.Compile(expr.SiteStopAfterAllItersIf, "true"), ErrorMessage: &stop, ErrorIncludeResult: &include},
		ForLoop:             &ir.ForLoop{Iterator: ir.Transform{Type: ir.Static, Value: json.RawMessage(`[0,1]`)}, SkipFailures: true, Modules: []*ir.Module{inner}},
	}
	e, err := engine.New(engine.Config{JS: js, Program: &ir.Program{Flows: map[string]*ir.Flow{"f/t/main": {Path: "f/t/main", Modules: []*ir.Module{outer}}}}})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := e.Run(context.Background(), engine.RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, generated, err := observeTrace(tr, []string{"l"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The oracle's side: the same observation, every Anyhow message with a
	// pickup location.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var locate func(any)
	locate = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if v["name"] == "Anyhow" {
				if m, ok := v["message"].(string); ok {
					v["message"] = m + " @worker.rs:3822:13"
				}
			}
			for _, c := range v {
				locate(c)
			}
		case []any:
			for _, c := range v {
				locate(c)
			}
		}
	}
	locate(tree)
	raw, _ = json.Marshal(tree)
	var want conformance.Observation
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	var differs []string
	for _, p := range compareRun(&conformance.Fixture{}, conformance.Run{Expect: &want}, got, generated) {
		if !strings.HasPrefix(p, "observed ") {
			ptr, _, _ := strings.Cut(p, ": ")
			differs = append(differs, ptr)
		}
	}
	for _, p := range []string{"/result/error/result/0/error/message", "/steps/m:l/result/0/error/message", "/steps/m:l/carried/error/result/0/error/message"} {
		if slices.Contains(differs, p) {
			t.Fatalf("the engine's error at %s keeps its location (generated %v)", p, generated)
		}
	}
	for _, p := range []string{"/result/error/result/1/0/error/message", "/steps/m:l/result/1/0/error/message", "/steps/m:l/carried/error/result/1/0/error/message"} {
		if !slices.Contains(differs, p) {
			t.Fatalf("the lookalike at %s was stripped: differences %v", p, differs)
		}
	}
}

// The location rule reaches a generated error wherever the observation
// keeps it: its stored result and its carried value, which keeps the
// producer's member order (ADR 0011). It never reaches a user's error, nor
// a carried value that is not the generated result and has no generated
// origin of its own (TestObserveChildFlows has one that does).
func TestCarriedLocation(t *testing.T) {
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	const msg = "Internal: Could not add completed job x: error returned from database: unsupported Unicode escape sequence"
	produced := `{"error":{"message":"` + msg + `","name":"InternalErr"}}`
	other := `{"error":{"message":"` + msg + ` here","name":"InternalErr"}}`
	trace := func(origin engine.Origin, carried string) *engine.Trace {
		return &engine.Trace{RunID: "root", Status: conformance.RunFailure, Result: v(produced), ResultOrigin: origin, Steps: []engine.StepRecord{{
			Site: conformance.SitePath{}.Child(conformance.Module("a")), Status: conformance.StepFailure, Module: conformance.ModuleFailure, Ran: true,
			RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: v(produced), Carried: v(carried), ResultOrigin: origin,
		}}}
	}
	// The oracle's side: the stored result in jsonb's member order, and an
	// explicit carried value in the producer's, both with the location.
	expect := func(carried string) *conformance.Observation {
		stored := `{"error":{"name":"InternalErr","message":"` + msg + ` <loc>"}}`
		var o conformance.Observation
		if err := json.Unmarshal([]byte(`{"status":"failure","result":`+stored+`,"steps":{"m:a":{"status":"failure","args":{},"result":`+stored+`,"carried":`+carried+`}}}`), &o); err != nil {
			t.Fatal(err)
		}
		return &o
	}
	withLoc := func(m string) string { return `{"error":{"message":"` + m + ` <loc>","name":"InternalErr"}}` }
	for _, c := range []struct {
		name    string
		trace   *engine.Trace
		expect  *conformance.Observation
		strict  bool
		differs []string
	}{
		{"generated, carried in producer order", trace(engine.OriginEngine, produced), expect(withLoc(msg)), false, nil},
		// Under strict key order both forms are checked exactly, and a
		// carried value written in the stored order differs.
		{"generated, strict key order", trace(engine.OriginHarness, produced), expect(withLoc(msg)), true, nil},
		{"generated, strict, carried in stored order", trace(engine.OriginHarness, produced), expect(`{"error":{"name":"InternalErr","message":"` + msg + ` <loc>"}}`), true, []string{"/steps/m:a/carried/error"}},
		{"user error", trace(engine.OriginUser, produced), expect(withLoc(msg)), false, []string{"/result/error/message", "/steps/m:a/carried/error/message", "/steps/m:a/result/error/message"}},
		{"carried is another value", trace(engine.OriginEngine, other), expect(withLoc(msg + " here")), false, []string{"/steps/m:a/carried/error/message"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, generated, err := observeTrace(c.trace, []string{"a"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			f := &conformance.Fixture{}
			if c.strict {
				f.Compare.KeyOrder = "strict"
			}
			var differs []string
			for _, p := range compareRun(f, conformance.Run{Expect: c.expect}, got, generated) {
				if !strings.HasPrefix(p, "observed ") {
					ptr, _, _ := strings.Cut(p, ": ")
					differs = append(differs, ptr)
				}
			}
			slices.Sort(differs)
			if !slices.Equal(differs, c.differs) {
				t.Fatalf("differences at %q, want %q", differs, c.differs)
			}
		})
	}
}

// N1b: a flow with a failure module has the site s:failure, not_run when
// it never ran, as the oracle's walker reads the flow's failure_module
// entry. A carried value the engine set apart from the job's result (an
// error stop's, FX-21's string, a chaining error with no job) has its own
// origin, so the location rule reaches it: FX-21/a's string loses its
// <loc> there, while the job's user result next to it keeps its text.
func TestObserveFailureSlotAndCarriedOrigin(t *testing.T) {
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	const text = `Error: QuickJS evaluation error: Error: cannot read property 'x' of null\n    at <anonymous> (eval_script:1:35)\n`
	str := `{"error":"` + text + `"}`
	tr := &engine.Trace{RunID: "root", Status: conformance.RunFailure, Result: v(str), ResultOrigin: engine.OriginEngine, Steps: []engine.StepRecord{{
		Site: conformance.SitePath{}.Child(conformance.Module("a")), Status: conformance.StepSuccess, Module: conformance.ModuleFailure, Ran: true,
		RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: v(`1`), Carried: v(str), CarriedOrigin: engine.OriginEngine,
	}}}
	got, generated, err := observeTrace(tr, []string{"a", "b"}, nil, true, nil, conformance.CompareSpec{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f := got.Steps[conformance.SitePath{}.Child(conformance.FailureSlot())]; f.Status != conformance.StepNotRun {
		t.Fatalf("s:failure %+v", f)
	}
	if want := []string{"/steps/m:a/carried", "/result"}; !slices.Equal(generated, want) {
		t.Fatalf("generated %q, want %q", generated, want)
	}
	withLoc := `{"error":"` + text + ` @worker_flow.rs:2451:11"}`
	var expect conformance.Observation
	if err := json.Unmarshal([]byte(`{"status":"failure","result":`+withLoc+`,"steps":{"m:a":{"status":"success","module":"failure","args":{},"result":1,"carried":`+withLoc+`},"m:b":{"status":"not_run"},"s:failure":{"status":"not_run"}}}`), &expect); err != nil {
		t.Fatal(err)
	}
	if p := compareRun(&conformance.Fixture{}, conformance.Run{Expect: &expect}, got, generated); len(p) != 0 {
		t.Fatalf("problems %q", p)
	}
	// Without a failure module there is no s:failure site.
	got, _, err = observeTrace(tr, []string{"a", "b"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
	if _, ok := got.Steps[conformance.SitePath{}.Child(conformance.FailureSlot())]; err != nil || ok {
		t.Fatalf("s:failure without a failure module (%v)", err)
	}
}

// ADR 0011: a copy of an error windmill-lite generated is generated only
// where the run declares it (copies), and only when it equals the error of
// the site it names, its error member or its message: a failure module
// that echoes a pickup error the observation does not show, or its message
// (FX-15/b, JE-35/d), loses the reference's suffix at the declared copies.
// Equality alone never counts: the failure module's own error, an
// undeclared copy, a copy declared against a site that generated nothing,
// and one whose value differs keep the reference's text.
func TestObserveDeclaredCopies(t *testing.T) {
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	const errObj = `{"name":"Anyhow","message":"Error: flow not found"}`
	const own = `{"error":{"name":"Anyhow","message":"Error: mine"}}`
	result := `{"copy":{"error":` + errObj + `},"m":"Error: flow not found","own":` + own + `,"undeclared":"Error: flow not found"}`
	tr := &engine.Trace{RunID: "root", Status: conformance.RunFailure, Result: v(result), Steps: []engine.StepRecord{
		{Site: conformance.SitePath{}.Child(conformance.Module("p")), Status: conformance.StepFailure, Module: conformance.ModuleFailure,
			Carried: v(`{"error":` + errObj + `}`), CarriedOrigin: engine.OriginEngine},
		{Site: conformance.SitePath{}.Child(conformance.FailureSlot()), Status: conformance.StepSuccess, Module: conformance.ModuleSuccess, Ran: true,
			RecordedArgs: v(`{"e":` + errObj + `}`), ResolvedArgs: v(`{"e":` + errObj + `}`), Result: v(result)},
	}}
	copies := []conformance.Copy{
		{At: "/result/copy", Of: "m:p"}, {At: "/result/m", Of: "m:p"}, {At: "/steps/s:failure/args/e", Of: "m:p"},
		{At: "/steps/s:failure/result/copy", Of: "m:p"}, {At: "/steps/s:failure/result/m", Of: "m:p"},
		// Declared against a site that generated nothing, and with a value
		// that differs from the site's error: neither counts.
		{At: "/steps/s:failure/result/undeclared", Of: "s:failure"}, {At: "/result/own", Of: "m:p"},
	}
	got, generated, err := observeTrace(tr, []string{"p"}, nil, true, nil, conformance.CompareSpec{}, copies, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/result/copy", "/result/m", "/steps/s:failure/args/e", "/steps/s:failure/result/copy", "/steps/s:failure/result/m"}
	if !slices.Equal(generated, want) {
		t.Fatalf("generated %q, want %q", generated, want)
	}
	if _, undeclared, err := observeTrace(tr, []string{"p"}, nil, true, nil, conformance.CompareSpec{}, nil, nil); err != nil || len(undeclared) != 0 {
		t.Fatalf("generated with no declaration %q (%v)", undeclared, err)
	}
	reference := func(text string) conformance.Run {
		var expect conformance.Observation
		if err := json.Unmarshal([]byte(text), &expect); err != nil {
			t.Fatal(err)
		}
		return conformance.Run{Expect: &expect}
	}
	text := compactJSON(got)
	withLoc := strings.ReplaceAll(text, `"Error: flow not found"`, `"Error: flow not found @jobs.rs:1:2"`)
	withLoc = strings.ReplaceAll(withLoc, `"message":"Error: flow not found"`, `"message":"Error: flow not found @jobs.rs:1:2"`)
	var differs []string
	for _, p := range compareRun(&conformance.Fixture{}, reference(withLoc), got, generated) {
		if !strings.HasPrefix(p, "observed ") {
			differs = append(differs, p)
		}
	}
	// The undeclared copies, in the flow's result and in the failure
	// module's, keep the reference's location: only they differ.
	if len(differs) != 2 || !strings.HasPrefix(differs[0], "/result/undeclared") || !strings.HasPrefix(differs[1], "/steps/s:failure/result/undeclared") {
		t.Fatalf("differences %q", differs)
	}
}

// ADR 0001 §2, ADR 0011: a user's own message that happens to equal an
// error windmill-lite generates later in the run is not generated, so a
// location in the reference's text at it still differs.
func TestObserveUserValueEqualToAGeneratedError(t *testing.T) {
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	const msg = "Error: Timeout value cannot be negative"
	tr := &engine.Trace{RunID: "root", Status: conformance.RunFailure, Result: v(`{"error":{"name":"Anyhow","message":"` + msg + `"}}`), ResultOrigin: engine.OriginEngine,
		Steps: []engine.StepRecord{
			{Site: conformance.SitePath{}.Child(conformance.Module("a")), Status: conformance.StepSuccess, Module: conformance.ModuleSuccess, Ran: true,
				RecordedArgs: v(`{}`), ResolvedArgs: v(`{}`), Result: v(`"` + msg + `"`), Carried: v(`"` + msg + `"`)},
			{Site: conformance.SitePath{}.Child(conformance.Module("b")), Status: conformance.StepFailure, Module: conformance.ModuleFailure,
				Carried: v(`{"error":{"name":"Anyhow","message":"` + msg + `"}}`), CarriedOrigin: engine.OriginEngine},
		}}
	got, generated, err := observeTrace(tr, []string{"a", "b"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(generated, []string{"/result"}) {
		t.Fatalf("generated %q", generated)
	}
	var expect conformance.Observation
	if err := json.Unmarshal([]byte(strings.Replace(compactJSON(got), `"result":"`+msg+`"`, `"result":"`+msg+` @user.rs:1:2"`, 1)), &expect); err != nil {
		t.Fatal(err)
	}
	if p := compareRun(&conformance.Fixture{}, conformance.Run{Expect: &expect}, got, generated); len(p) == 0 || !strings.HasPrefix(p[0], "/steps/m:a/result") {
		t.Fatalf("problems %q", p)
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

// A codebase script that fails to build fails only the flows that reach it
// (c2b review 3, finding 5): the codebase build writes the metadata and
// assets of every script that built, so an unrelated flow whose codebase
// script reads its asset still runs and passes. A script whose asset
// collides with one an earlier script took fails its own flow.
func TestFlowCheckerIsolatesCodebaseFailures(t *testing.T) {
	bun := bunOrSkip(t)
	dir := filepath.Join(t.TempDir(), "flows", "c2b", "isolation")
	flow := func(script string) string {
		return "summary: t\nvalue:\n  modules:\n    - id: a\n      value:\n        type: script\n        path: f/t/" + script +
			"\n        input_transforms: {}\nschema: {type: object, properties: {}}\n"
	}
	meta := "summary: ''\ndescription: ''\nkind: script\nschema: {type: object, properties: {}}\n"
	writeTree(t, dir, map[string]string{
		"fixture.yaml": "id: c2b/isolation\nspec: [WR-47]\nscope: core\nprovenance: policy\nlanguages: [bun]\n",
		"windmill/wmill.yaml": "defaultTs: bun\ncodebases:\n" +
			"  - relative_path: f/t\n    includes: [\"f/t/good.ts\", \"f/t/bad.ts\"]\n    assets: [{from: f/t/a.txt, to: data/a.txt}]\n" +
			"  - relative_path: f/t\n    includes: [\"f/t/other.ts\"]\n    assets: [{from: f/t/another.txt, to: data/a.txt}]\n",
		"windmill/f/t/a.txt":                    "asset",
		"windmill/f/t/another.txt":              "another asset",
		"windmill/f/t/good.ts":                  "import { readFileSync } from \"node:fs\";\nexport function main() { return readFileSync(\"data/a.txt\", \"utf8\"); }\n",
		"windmill/f/t/bad.ts":                   "import { x } from \"./missing\";\nexport function main() { return x; }\n",
		"windmill/f/t/other.ts":                 "export function main() { return 2; }\n",
		"windmill/f/t/good.script.yaml":         meta,
		"windmill/f/t/bad.script.yaml":          meta,
		"windmill/f/t/other.script.yaml":        meta,
		"windmill/f/t/goodflow.flow/flow.yaml":  flow("good"),
		"windmill/f/t/badflow.flow/flow.yaml":   flow("bad"),
		"windmill/f/t/otherflow.flow/flow.yaml": flow("other"),
		"runs/bad.yaml":                         "target: f/t/badflow\nexpect: {status: success, result: 0}\n",
		"runs/other.yaml":                       "target: f/t/otherflow\nexpect:\n  status: success\n  result: 2\n  steps:\n    \"m:a\": {status: success, args: {}, result: 2}\n",
		"runs/good.yaml":                        "target: f/t/goodflow\nexpect:\n  status: success\n  result: asset\n  steps:\n    \"m:a\": {status: success, args: {}, result: asset}\n",
	})
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "--bun", bun, "--repo", repo(t), dir}, &stdout, &stderr)
	out := stdout.String()
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out, stderr.String())
	}
	for _, want := range []string{
		"pass " + dir + " good\n",
		"fail " + dir + " bad\n",
		"build: script f/t/bad: bundle_failed: ",
		"fail " + dir + " other\n",
		"build: script f/t/other: asset_collision: ",
		"1 passed, 2 failed, 0 unsupported (mode warm)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// A run that exceeds its deadline is reported as such, and the files its
// steps left to count their attempts are still removed, while another
// run's stay (FU8 review).
func TestTimedOutRunRemovesCounters(t *testing.T) {
	bun := bunOrSkip(t)
	counters := t.TempDir()
	oldDir, oldTimeout := counterDir, flowRunTimeout
	counterDir, flowRunTimeout = counters, 2*time.Second
	t.Cleanup(func() { counterDir, flowRunTimeout = oldDir, oldTimeout })
	other := "wl-t-11111111-1111-4111-8111-111111111111-a"
	if err := os.WriteFile(filepath.Join(counters, other), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "flows", "fu8", "timeout")
	writeTree(t, dir, map[string]string{
		"fixture.yaml":        "id: fu8/timeout\nspec: [OR-15]\nscope: core\nprovenance: policy\nlanguages: [bun]\n",
		"windmill/wmill.yaml": "defaultTs: bun\nincludes: [\"f/**\"]\n",
		"windmill/f/t/hang.flow/flow.yaml": "summary: t\nvalue:\n  modules:\n    - id: a\n      value:\n        type: rawscript\n        content: '!inline a.ts'\n" +
			"        input_transforms:\n          dir: {type: static, value: '" + counters + "'}\n        language: bun\n" +
			"schema:\n  type: object\n  properties: {}\n  required: []\n",
		// The step counts its attempt, then outlasts the run's deadline.
		"windmill/f/t/hang.flow/a.ts": "import { writeFileSync } from \"node:fs\";\n" +
			"export async function main(dir: string) {\n" +
			"  writeFileSync(dir + \"/wl-t-\" + process.env.WM_JOB_ID + \"-a\", \"1\");\n" +
			"  await new Promise((r) => setTimeout(r, 60000));\n}\n",
		"runs/hang.yaml": "target: f/t/hang\nexpect: {status: success, result: 1}\n",
	})
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "--bun", bun, "--repo", repo(t), filepath.Dir(dir)}, &stdout, &stderr)
	out := stdout.String()
	if want := "fail " + dir + " hang\n  the run did not end within 2s\n"; code != 1 || !strings.Contains(out, want) {
		t.Fatalf("exit %d, want 1 and %q\n%s%s", code, want, out, stderr.String())
	}
	var left []string
	ents, _ := os.ReadDir(counters)
	for _, e := range ents {
		left = append(left, e.Name())
	}
	if want := []string{other}; !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
}

// The files a run's steps leave to count their attempts are removed after
// the run, those named after its root, its child flows and its steps' jobs,
// and no other run's (N5's follow-up: they piled up in the host's /tmp).
func TestRemoveCounters(t *testing.T) {
	dir := t.TempDir()
	old := counterDir
	counterDir = dir
	t.Cleanup(func() { counterDir = old })
	names := []string{"wl-n5-run1-a", "wl-ni-child1-b", "wl-ni-job1-args", "wl-n5-other-a", "wl-n5-run1", "unrelated"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removeCounters(&engine.Trace{RunID: "run1", Children: []engine.ChildFlow{{ID: "child1"}}, Steps: []engine.StepRecord{{Job: "job1"}, {}}})
	var left []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		left = append(left, e.Name())
	}
	if want := []string{"unrelated", "wl-n5-other-a", "wl-n5-run1"}; !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
}

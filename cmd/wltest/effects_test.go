package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/ir"
)

const stubDir = "../../conformance/flows/stub"

// TestStubCasesMatchTheOracle is the exit gate of milestone E4b: the stub
// acceptance cases of ADR 0011 (a retry inside a loop, a collapsed body,
// nested loops, a named subflow and a parallel loop under one outer run,
// two concurrent outer runs), and E4b's retry inside a loop on codebase
// scripts, run on windmill-lite, stubbed, with the replay service
// answering from each run's table, and every run matches both its expect
// and the oracle's recording of it, effects included: where each stub
// call resolved, its attempt, the job, flow, root flow and root ids the
// step saw, and the arguments it received. They run in fresh mode, one
// harness process per invocation as on Windmill, and in warm mode.
// TestFlowSuite requires the same runs against their expects, in warm.
func TestStubCasesMatchTheOracle(t *testing.T) {
	bun := bunOrSkip(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer js.Close()
	dirs, err := findFixtures([]string{stubDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) < 5 {
		t.Fatalf("%d stub fixtures under %s, want the four of E4a and E4b's codebase fixture", len(dirs), stubDir)
	}
	runs := 0
	for _, mode := range []string{conformance.ModeFresh, conformance.ModeWarm} {
		fc := &flowChecker{bun: bun, repo: repo(t), mode: mode, js: js}
		for _, dir := range dirs {
			for _, against := range []string{"expect", "recording"} {
				f, err := conformance.LoadFixture(dir)
				if err != nil {
					t.Fatal(err)
				}
				if !f.Stub || !f.Oracle.Effects {
					t.Errorf("%s: not a stub fixture with effects", dir)
					continue
				}
				recorded := true
				for i, r := range f.Runs {
					if r.Recorded == nil || len(r.Recorded.Observation.Effects) == 0 {
						t.Errorf("%s/%s: no recording with effects", dir, r.Name)
						recorded = false
					}
					if against == "recording" {
						f.Runs[i].Expect = nil
					}
				}
				if !recorded {
					continue
				}
				for _, res := range fc.runFixture(context.Background(), f) {
					switch {
					case res.unsupported != "":
						t.Errorf("%s/%s against its %s, %s: unsupported: %s", dir, res.run, against, mode, res.unsupported)
					case len(res.problems) > 0:
						t.Errorf("%s/%s against its %s, %s:\n  %s\n%s", dir, res.run, against, mode, strings.Join(res.problems, "\n  "), res.diag)
					}
					if against == "expect" && mode == conformance.ModeWarm {
						runs++
					}
				}
			}
		}
	}
	t.Logf("stub acceptance cases: %d runs of %d fixtures match their expects and the oracle's recordings, in fresh and in warm", runs, len(dirs))
}

// Stub mode fails a run whose table lacks an attempt the run makes
// (replay_miss), shows an effect that differs from the expected one, needs
// no call from a mocked or skipped script module, which runs an identity
// job (FX-16, FX-18), fails a run whose script job launched and failed
// before its invocation (FX-14) as never having called, fails a group
// whose other run failed (group_member_failed), and fails a fixture whose
// tree cannot be stubbed; the effect sink records a run's own effects and
// refuses another root's (OR-16/a, OR-16/c).
func TestStubModeAndEffects(t *testing.T) {
	bun := bunOrSkip(t)
	base := filepath.Join(t.TempDir(), "flows")
	flow := "summary: t\nvalue:\n  modules:\n    - id: loop\n      value:\n        type: forloopflow\n        modules:\n          - id: call\n            value:\n              type: rawscript\n              content: '!inline call.ts'\n              input_transforms:\n                x: {type: javascript, expr: flow_input.iter.value}\n              language: bun\n            retry: {constant: {attempts: 1, seconds: 0}}\n        iterator: {type: javascript, expr: flow_input.xs}\n        parallel: false\n        skip_failures: false\nschema:\n  type: object\n  properties: {}\n  required: []\n"
	effect := func(id string) string {
		return "    - id: " + id + "\n      value:\n        type: rawscript\n        content: '!inline " + id + ".ts'\n        input_transforms: {}\n        language: bun\n"
	}
	call := `{site: "m:loop/i:0/m:call", step: call, flow: "<job:m:loop/i:0>", root_flow: "<job:>", root: "<job:>", args: [p]`
	stub := filepath.Join(base, "stub")
	writeTree(t, stub, map[string]string{
		"fixture.yaml":                     "id: t/stub\nspec: [OR-11]\nscope: core\nprovenance: policy\nlanguages: [bun]\noracle: {effects: true}\nstub: true\n",
		"windmill/wmill.yaml":              "defaultTs: bun\nincludes: [\"f/**\"]\n",
		"windmill/f/t/loop.flow/flow.yaml": flow,
		// The original body is never run: its result would differ.
		"windmill/f/t/loop.flow/call.ts": "export async function main(x: string) { return 'original ' + x; }\n",
		"runs/a_holds.yaml": "target: f/t/loop\nargs: {xs: [p]}\nreplay:\n  \"m:loop/i:0/m:call\": [{error: {message: busy}}, {result: P}]\n" +
			"expect:\n  status: success\n  result: [P]\n  steps:\n    \"m:loop\": {status: success, result: [P]}\n" +
			"    \"m:loop/i:0/m:call/r:0\": {status: failure, args: {x: p}, result: {error: {name: Error, message: busy, stack: \"<stack>\", step_id: call}}}\n" +
			"    \"m:loop/i:0/m:call\": {status: success, args: {x: p}, result: P}\n" +
			"  effects:\n    - " + call + `, attempt: 0, job: "<job:m:loop/i:0/m:call/r:0>"}` + "\n    - " + call + `, attempt: 1, job: "<job:m:loop/i:0/m:call>"}` + "\n",
		"runs/b_miss.yaml": "target: f/t/loop\nargs: {xs: [p]}\nreplay:\n  \"m:loop/i:0/m:call\": {error: {message: busy}}\nexpect: {status: failure}\n",
		// m is mocked and s skipped: identity jobs, whose scripts never run.
		"windmill/f/t/ident.flow/flow.yaml": "summary: t\nvalue:\n  modules:\n" + effect("m") + "      mock: {enabled: true, return_value: {v: 1}}\n" + effect("s") + "      skip_if: {expr: 'true'}\n" + effect("c") + "schema:\n  type: object\n  properties: {}\n  required: []\n",
		"windmill/f/t/ident.flow/m.ts":      "export async function main() { return 'original'; }\n",
		"windmill/f/t/ident.flow/s.ts":      "export async function main() { return 'original'; }\n",
		"windmill/f/t/ident.flow/c.ts":      "export async function main() { return 'original'; }\n",
		"runs/d_identity.yaml":              "target: f/t/ident\nreplay:\n  \"m:c\": {result: C}\nclaims:\n  - {at: /status, equals: success}\n  - {at: /result, equals: C}\n  - {at: /steps/m:m/result, equals: {v: 1}}\n  - {at: /steps/m:s/skipped, equals: true}\n  - {at: /effects, length: 1}\n",
		// x's input transform throws: its job launches and fails without
		// running (FX-14), so its stub never calls.
		"windmill/f/t/pre.flow/flow.yaml": "summary: t\nvalue:\n  modules:\n    - id: x\n      value:\n        type: rawscript\n        content: '!inline x.ts'\n        input_transforms:\n          v: {type: javascript, expr: null.v}\n        language: bun\nschema:\n  type: object\n  properties: {}\n  required: []\n",
		"windmill/f/t/pre.flow/x.ts":      "export async function main(v: unknown) { return v; }\n",
		"runs/e_pre_run.yaml":             "target: f/t/pre\nreplay:\n  \"m:x\": {result: 1}\nclaims:\n  - {at: /status, equals: failure}\n",
		// Two concurrent outer runs; the second's table misses its call, so
		// the group fails together.
		"runs/g_pair_a.yaml": "target: f/t/loop\nargs: {xs: [p]}\ngroup: pair\nreplay:\n  \"m:loop/i:0/m:call\": {result: P}\nclaims:\n  - {at: /status, equals: success}\n",
		"runs/g_pair_b.yaml": "target: f/t/loop\nargs: {xs: [q]}\ngroup: pair\nreplay:\n  \"m:loop/i:0/m:call\": [{error: {message: busy}}]\nclaims:\n  - {at: /status, equals: failure}\n",
		// The second call is the module's last attempt, attempt 1.
		"runs/c_wrong_effect.yaml": "target: f/t/loop\nargs: {xs: [p]}\nreplay:\n  \"m:loop/i:0/m:call\": [{error: {message: busy}}, {result: P}]\n" +
			"expect:\n  status: success\n  result: [P]\n  steps:\n    \"m:loop\": {status: success, result: [P]}\n" +
			"    \"m:loop/i:0/m:call/r:0\": {status: failure, args: {x: p}, result: {error: {name: Error, message: busy, stack: \"<stack>\", step_id: call}}}\n" +
			"    \"m:loop/i:0/m:call\": {status: success, args: {x: p}, result: P}\n" +
			"  effects:\n    - " + call + `, attempt: 0, job: "<job:m:loop/i:0/m:call/r:0>"}` + "\n    - " + call + `, attempt: 0, job: "<job:m:loop/i:0/m:call>"}` + "\n",
	})
	refused := filepath.Join(base, "refused")
	writeTree(t, refused, map[string]string{
		"fixture.yaml":                    "id: t/refused\nspec: [OR-11]\nscope: core\nprovenance: policy\nlanguages: [bun]\noracle: {effects: true}\nstub: true\n",
		"windmill/wmill.yaml":             "defaultTs: bun\nincludes: [\"f/**\"]\n",
		"windmill/f/t/one.flow/flow.yaml": "summary: t\nvalue:\n  modules:\n" + effect("a") + "schema:\n  type: object\n  properties: {}\n  required: []\n",
		// An arrow main has no declaration that binds its arguments (WR-7).
		"windmill/f/t/one.flow/a.ts": "export const main = async () => 1;\n",
		"runs/a.yaml":                "target: f/t/one\nreplay:\n  \"m:a\": {result: 1}\nexpect: {status: success, result: 1}\n",
	})
	sink := filepath.Join(base, "sink")
	post := func(root string) string {
		return "export async function main() {\n  const env = process.env;\n  const res = await fetch(env.WL_EFFECT_SINK!, { method: 'POST', body: JSON.stringify({ root: " + root + ", job: env.WM_JOB_ID, step: env.WM_FLOW_STEP_ID }) });\n  return res.status;\n}\n"
	}
	writeTree(t, sink, map[string]string{
		"fixture.yaml":                    "id: t/sink\nspec: [OR-16]\nscope: core\nprovenance: policy\nlanguages: [bun]\noracle: {effects: true}\n",
		"windmill/wmill.yaml":             "defaultTs: bun\nincludes: [\"f/**\"]\n",
		"windmill/f/t/two.flow/flow.yaml": "summary: t\nvalue:\n  modules:\n" + effect("a") + effect("b") + "schema:\n  type: object\n  properties: {}\n  required: []\n",
		"windmill/f/t/two.flow/a.ts":      post("env.WM_ROOT_JOB_ID"),
		"windmill/f/t/two.flow/b.ts":      post("'elsewhere'"),
		"runs/a.yaml": "target: f/t/two\nexpect:\n  status: success\n  result: 409\n  steps:\n    \"m:a\": {status: success, args: {}, result: 200}\n    \"m:b\": {status: success, args: {}, result: 409}\n" +
			"  effects:\n    - {root: \"<job:>\", job: \"<job:m:a>\", step: a}\n",
	})
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"check", "--bun", bun, "--repo", repo(t), base}, &stdout, &stderr)
	out := stdout.String()
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out, stderr.String())
	}
	for _, want := range []string{
		"pass " + stub + " a_holds\n",
		"fail " + stub + " b_miss\n  stub: {\"error\":\"replay_miss\",\"site\":\"m:loop/i:0/m:call\",\"attempt\":1}\n",
		"fail " + stub + " c_wrong_effect\n  /effects/1/attempt: 1 != 0\n",
		"pass " + stub + " d_identity\n",
		"fail " + stub + " e_pre_run\n  stub: {\"error\":\"stub_not_called\",\"sites\":[\"m:x\"]}\n",
		"fail " + stub + " g_pair_a\n  {\"error\":\"group_member_failed\",\"group\":\"pair\",\"run\":\"g_pair_b\"}\n",
		"fail " + stub + " g_pair_b\n  stub: {\"error\":\"replay_miss\",\"site\":\"m:loop/i:0/m:call\",\"attempt\":1}\n",
		"fail " + refused + " a\n  stub: {\"error\":\"stub_unsupported\"",
		"pass " + sink + " a\n",
		"3 passed, 6 failed, 0 unsupported (mode warm)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The run's hooks give every invocation the sink's URL and locate a job
// at the site it launched at; a job the run did not launch is unresolved.
func TestRunEffectsLocates(t *testing.T) {
	site, err := conformance.ParseSitePath("m:loop/i:1/m:call")
	if err != nil {
		t.Fatal(err)
	}
	h := &runEffects{url: "http://127.0.0.1:1", sites: map[string]conformance.SitePath{}}
	env := map[string]string{"WM_JOB_ID": "j1", "WM_ROOT_JOB_ID": "R"}
	if err := h.InvocationEnv(context.Background(), nil, engine.Site{Path: site}, env); err != nil {
		t.Fatal(err)
	}
	if env[effectSinkVar] != "http://127.0.0.1:1" || env["WM_JOB_ID"] != "j1" {
		t.Errorf("env %v", env)
	}
	if got, err := h.Locate(context.Background(), "R", "j1"); err != nil || got != site {
		t.Errorf("Locate(j1) = %v, %v; want %v", got, err, site)
	}
	if _, err := h.Locate(context.Background(), "R", "j2"); err == nil || !strings.Contains(err.Error(), "launched no job j2") {
		t.Errorf("Locate(j2): %v", err)
	}
}

// One fixture's effect server keeps its open runs apart: a run's effects
// and its stub calls are its own, answered from its own table, and a root
// that is no open run is refused (OR-16/c, OR-V6).
func TestEffectServerKeepsRunsApart(t *testing.T) {
	s, err := newEffectServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := conformance.ParseSitePath("m:a")
	var table conformance.ReplayTable
	if err := json.Unmarshal([]byte(`{"m:a": {"result": 7}}`), &table); err != nil {
		t.Fatal(err)
	}
	a := s.openRun("A", true, table)
	s.openRun("B", false, nil)
	if err := a.InvocationEnv(context.Background(), nil, engine.Site{Path: site}, map[string]string{"WM_JOB_ID": "ja"}); err != nil {
		t.Fatal(err)
	}
	send := func(path, body string) (int, string) {
		t.Helper()
		res, err := http.Post(s.url+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := send(conformance.ReplayPath, `{"root":"A","root_flow":"A","flow":"A","job":"ja","step":"a","args":[1]}`); code != 200 || body != `{"result":7}` {
		t.Errorf("replay: %d %s", code, body)
	}
	if code, _ := send(conformance.ReplayPath, `{"root":"B","job":"jb","args":[]}`); code != http.StatusConflict {
		t.Errorf("a stub call of a run not in stub mode: %d, want 409", code)
	}
	if code, _ := send("/", `{"root":"B","job":"jb","step":"b"}`); code != 200 {
		t.Errorf("B's effect: %d", code)
	}
	if code, _ := send("/", `{"root":"C","job":"jc","step":"c"}`); code != http.StatusConflict {
		t.Errorf("a foreign effect: %d, want 409", code)
	}
	effects, calls := s.closeRun("A")
	if len(effects) != 0 || len(calls) != 1 || calls[0].Site != site || calls[0].Attempt != 0 ||
		string(calls[0].Effect) != `{"site":"m:a","attempt":0,"step":"a","job":"ja","flow":"A","root_flow":"A","root":"A","args":[1]}` {
		t.Errorf("A: effects %s, calls %+v", effects, calls)
	}
	effects, calls = s.closeRun("B")
	if len(effects) != 1 || string(effects[0]) != `{"root":"B","job":"jb","step":"b"}` || calls != nil {
		t.Errorf("B: effects %s, calls %+v", effects, calls)
	}
}

// checkStub reads the trace as the oracle reads its walk: a failed attempt
// is walked at its r: site, a subflow step's job is a flow's, not a
// script's, a superseded record's job is not walked, and a record that ran
// with no job is a script job that never called only when its module runs
// a script and the job failed before its invocation, never when it is an
// identity job (an identity module, a mocked or skipped script module).
func TestCheckStub(t *testing.T) {
	p := func(s string) conformance.SitePath {
		site, err := conformance.ParseSitePath(s)
		if err != nil {
			t.Fatal(err)
		}
		return site
	}
	leaf := func(id string) *ir.Module {
		return &ir.Module{ID: id, Kind: ir.KindRawScript, Leaf: &ir.Leaf{Script: "inline-" + id}}
	}
	mocked := leaf("mocked")
	mocked.Mock = &ir.Mock{Enabled: true}
	prog := &ir.Program{Flows: map[string]*ir.Flow{
		"f/t/main": {Path: "f/t/main", Modules: []*ir.Module{
			{ID: "who", Kind: ir.KindSubflow, Subflow: &ir.Subflow{Path: "f/t/sub"}},
			leaf("a"), leaf("t"), mocked, leaf("skipped"), leaf("old"),
			{ID: "id", Kind: ir.KindIdentity},
		}, FailureModule: leaf("failure")},
		"f/t/sub": {Path: "f/t/sub", Modules: []*ir.Module{leaf("lookup")}},
	}}
	ok, fail := conformance.StepSuccess, conformance.StepFailure
	tr := &engine.Trace{
		RunID:    "R",
		Children: []engine.ChildFlow{{ID: "S", Site: p("m:who")}},
		Steps: []engine.StepRecord{
			{Site: p("m:who"), Job: "S", Ran: true, Status: ok},
			{Site: p("m:who/m:lookup"), Job: "l", Ran: true, Status: ok},
			{Site: p("m:a/r:0"), Job: "a0", Ran: true, Status: fail},
			{Site: p("m:a"), Job: "a1", Ran: true, Status: ok, FailedRetries: []conformance.SitePath{p("m:a/r:0")}},
			{Site: p("m:old"), Job: "old", Ran: true, Status: ok, Superseded: true},
			// Identity jobs have no invocation and run no script: an
			// identity module's, a mocked module's (FX-18), even one its
			// stop_after_if failed (FX-21), a skipped module's (FX-16), and
			// the failure module's when its skip_if held, which the trace
			// does not flag skipped (FX-32).
			{Site: p("m:id"), Ran: true, Status: ok},
			{Site: p("m:mocked"), Ran: true, Status: fail},
			{Site: p("m:skipped"), Ran: true, Status: ok, Skipped: true},
			{Site: p("s:failure"), Ran: true, Status: ok},
		},
	}
	call := func(job, site string, attempt int) conformance.ReplayCall {
		return conformance.ReplayCall{Job: job, Site: p(site), Attempt: attempt}
	}
	good := []conformance.ReplayCall{call("l", "m:who/m:lookup", 0), call("a0", "m:a", 0), call("a1", "m:a", 1)}
	if err := checkStub(tr, prog, "f/t/main", good); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		calls []conformance.ReplayCall
		want  string
	}{
		{"a leaf that never called", good[1:], `{"error":"stub_not_called","sites":["m:who/m:lookup"]}`},
		{"the last attempt counted as the first", []conformance.ReplayCall{good[0], good[1], call("a1", "m:a", 0)}, `"error":"replay_site_mismatch","job":"a1"`},
		{"a superseded job", append(good[:3:3], call("old", "m:old", 0)), `"job":"old","resolved":"m:old","attempt":0,"walked":"(not walked)"`},
	} {
		if err := checkStub(tr, prog, "f/t/main", c.calls); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}

	// A script leaf whose first attempt launched but failed before any
	// invocation (an input transform, FX-14) has no job id: as on the
	// oracle's walk, that job is at r:0, so the one call is the module's
	// last attempt, not its first, and the launched job never called.
	pre := &engine.Trace{RunID: "R", Steps: []engine.StepRecord{
		{Site: p("m:t/r:0"), Ran: true, Status: fail},
		{Site: p("m:t"), Job: "t1", Ran: true, Status: ok, FailedRetries: []conformance.SitePath{p("m:t/r:0")}},
	}}
	if err := checkStub(pre, prog, "f/t/main", []conformance.ReplayCall{call("t1", "m:t", 0)}); err == nil || !strings.Contains(err.Error(), `"job":"t1","resolved":"m:t","attempt":0,"walked":"m:t"`) {
		t.Errorf("a call at a module whose failed attempt had no invocation: %v", err)
	}
	if err := checkStub(pre, prog, "f/t/main", []conformance.ReplayCall{call("t1", "m:t", 1)}); err == nil || err.Error() != `{"error":"stub_not_called","sites":["m:t/r:0"]}` {
		t.Errorf("an attempt that launched with no invocation: %v", err)
	}
}

// moduleAt finds the module a site names through loop bodies, branches,
// subflows by path, repeated ids and the failure slot of the flow that
// holds it, and nothing for a site the program does not have.
func TestModuleAt(t *testing.T) {
	m := func(id string) *ir.Module {
		return &ir.Module{ID: id, Kind: ir.KindRawScript, Leaf: &ir.Leaf{Script: id}}
	}
	inner, dup0, dup1, b0, bd, all1, w, fail, subFail, lookup := m("inner"), m("d"), m("d"), m("b0"), m("bd"), m("all1"), m("w"), m("failure"), m("subfail"), m("lookup")
	prog := &ir.Program{Flows: map[string]*ir.Flow{
		"f/main": {Path: "f/main", FailureModule: fail, Modules: []*ir.Module{
			{ID: "loop", Kind: ir.KindForLoop, ForLoop: &ir.ForLoop{Modules: []*ir.Module{inner}}},
			dup0, dup1,
			{ID: "one", Kind: ir.KindBranchOne, BranchOne: &ir.BranchOne{Branches: []ir.OneBranch{{Modules: []*ir.Module{b0}}}, Default: []*ir.Module{bd}}},
			{ID: "all", Kind: ir.KindBranchAll, BranchAll: &ir.BranchAll{Branches: []ir.AllBranch{{}, {Modules: []*ir.Module{all1}}}}},
			{ID: "while", Kind: ir.KindWhileLoop, WhileLoop: &ir.WhileLoop{Modules: []*ir.Module{w}}},
			{ID: "who", Kind: ir.KindSubflow, Subflow: &ir.Subflow{Path: "f/sub"}},
		}},
		"f/sub": {Path: "f/sub", FailureModule: subFail, Modules: []*ir.Module{lookup}},
	}}
	for site, want := range map[string]*ir.Module{
		"m:loop/i:3/m:inner":     inner,
		"m:loop/i:3/m:inner/r:1": inner,
		"m:d~0":                  dup0,
		"m:d~1":                  dup1,
		"m:one/b:0/m:b0":         b0,
		"m:one/b:default/m:bd":   bd,
		"m:all/b:1/m:all1":       all1,
		"m:while/i:0/m:w":        w,
		"m:who/m:lookup":         lookup,
		"m:who/s:failure":        subFail,
		"s:failure":              fail,
		"m:loop/i:0/s:failure":   fail,
		"m:d":                    nil,
		"m:loop/m:inner":         nil,
		"m:one/b:1/m:b0":         nil,
		"s:preprocessor":         nil,
		"m:nope":                 nil,
	} {
		p, err := conformance.ParseSitePath(site)
		if err != nil {
			t.Fatal(err)
		}
		if got := moduleAt(prog, "f/main", p); got != want {
			t.Errorf("moduleAt(%s) = %v, want %v", site, got, want)
		}
	}
}

// A group's runs are held at their first module until all of them are in
// flight, so concurrent runs overlap by construction. Runs that ran one
// after the other do not overlap and fail the group, and so does a failed
// run; a run that starts no module holds up no other run.
func TestRunGroup(t *testing.T) {
	ctx := context.Background()
	names := []string{"r", "t"}
	none := func(string) bool { return false }

	g := newRunGroup("rt", 2)
	r := g.member("r")
	released := make(chan struct{})
	go func() {
		from := time.Now()
		r.inFlight(ctx)
		close(released)
		r.inFlight(ctx) // a later module start does not wait
		r.ended(from, time.Now())
	}()
	select {
	case <-released:
		t.Fatal("run r went on before run t was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	tm := g.member("t")
	from := time.Now()
	tm.inFlight(ctx)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("run r was not released once run t was in flight")
	}
	tm.ended(from, time.Now())
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		n := len(g.spans)
		g.mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := g.conclude(names, none); err != nil {
		t.Errorf("concurrent runs: %v", err)
	}
	if err := g.conclude(names, func(run string) bool { return run == "t" }); err == nil || err.Error() != `{"error":"group_member_failed","group":"rt","run":"t"}` {
		t.Errorf("a failed run: %v", err)
	}

	// One run after the other: r waits out the group's wait, ends, and only
	// then is t submitted.
	g = newRunGroup("rt", 2)
	g.wait = 10 * time.Millisecond
	r, tm = g.member("r"), g.member("t")
	from = time.Now()
	r.inFlight(ctx)
	r.ended(from, time.Now())
	from = time.Now()
	tm.inFlight(ctx)
	tm.ended(from, time.Now())
	if err := g.conclude(names, none); err == nil || err.Error() != `{"error":"group_not_concurrent","group":"rt","reason":"run t started after run r ended"}` {
		t.Errorf("runs one after the other: %v", err)
	}

	// A run that ends without starting a module counts as in flight then.
	g = newRunGroup("rt", 2)
	g.member("r").ended(time.Now(), time.Now())
	done := make(chan struct{})
	go func() { g.member("t").inFlight(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run t waited for a run that had ended")
	}
}

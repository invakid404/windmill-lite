package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The branch tests (milestone N3b) run synthetic flows on the real
// QuickJS-ng engine, with steps answered in Go; the E3d, E3a and E3b branch
// fixtures of conformance/flows run the same branches on the real Bun
// harness (cmd/wltest, TestFlowSuite).

// branch writes one branch of a branchone (with a predicate) or of a
// branchall (with a skip_failure).
func branch(field string, body ...string) string {
	return fmt.Sprintf(`{%s,"modules":[%s]}`, field, strings.Join(body, ","))
}

// when is a branchone branch's predicate field.
func when(expr string) string {
	b, _ := json.Marshal(expr)
	return `"expr":` + string(b)
}

// skipFailure is a branchall branch's skip_failure field.
func skipFailure(skip bool) string { return fmt.Sprintf(`"skip_failure":%v`, skip) }

// branchOneOf writes a branchone module.
func branchOneOf(id string, branches []string, def []string, envelope string) string {
	if envelope != "" {
		envelope = "," + envelope
	}
	return fmt.Sprintf(`{"id":%q,"value":{"type":"branchone","branches":[%s],"default":[%s]}%s}`, id, strings.Join(branches, ","), strings.Join(def, ","), envelope)
}

// branchAllOf writes a branchall module.
func branchAllOf(id string, branches []string, parallel bool, envelope string) string {
	if envelope != "" {
		envelope = "," + envelope
	}
	return fmt.Sprintf(`{"id":%q,"value":{"type":"branchall","branches":[%s],"parallel":%v}%s}`, id, strings.Join(branches, ","), parallel, envelope)
}

// value is a module that returns v, a JSON text, with envelope fields.
func value(id, v, envelope string) string { return mod(id, "value", inputs("v", static(v)), envelope) }

// CF-14, JE-31, CF-9, JE-36, JE-37, CF-19, CF-24, WA-6, WA-V1: the
// predicates are evaluated in order with the step's scope, and the first
// that holds is chosen, so a later one that would throw never runs; an
// empty predicate never holds. The chosen branch runs as a child flow
// whose input, its first step's previous_result, is the flow's input; its
// steps write into the root's results scope, so they are visible after the
// branchone, whose result is the branch's. The branch's steps see the
// child's job id and path, and the root's innermost scope.
func TestBranchOneChoice(t *testing.T) {
	s := &loopSteps{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		value("a", `{"n":5}`, ""),
		branchOneOf("br", []string{
			branch(when(""), value("e", `"empty"`, "")),
			branch(when("results.a.n > 10"), value("c1", `"big"`, "")),
			branch(when("previous_result.n > 3"),
				mod("b", "echo", inputs("prev", js("previous_result"), "a", js("results.a")), ""),
				inlineOf("c", inputs("v", static(`"mid"`)))),
			branch(when("undefined.x"), value("c3", `"never"`, "")),
		}, []string{value("d", `"small"`, "")}, ""),
		mod("n", "echo", inputs("b", js("results.b"), "br", js("results.br"), "p", js("previous_result")), ""),
	)}, s)
	tr := runIt(t, e, `{"k":1}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"b":{"prev":{"k":1},"a":{"n":5}},"br":{"v":"mid"},"p":{"v":"mid"}}`)
	br := at(t, tr, "m:br")
	if !br.Ran || br.Status != conformance.StepSuccess || br.BranchChosen == nil || br.BranchChosen.Type != "branch" || *br.BranchChosen.Branch != 2 {
		t.Fatalf("the branchone %+v, chosen %+v", br, br.BranchChosen)
	}
	sameJSON(t, "its result", br.Result, `{"v":"mid"}`)
	if len(tr.Children) != 1 || tr.Children[0].Site.String() != "m:br/b:2" || !slices.Equal(tr.Children[0].Modules, []string{"b", "c"}) {
		t.Fatalf("children %+v", tr.Children)
	}
	b, c := s.inv(t, "m:br/b:2/m:b"), s.inv(t, "m:br/b:2/m:c")
	if b.Env["WM_FLOW_JOB_ID"] != tr.Children[0].ID || b.Env["WM_ROOT_FLOW_JOB_ID"] != tr.RunID || b.Env["WM_FLOW_PATH"] != "f/t/main/branchone-3" {
		t.Fatalf("a branch step's environment %v", b.Env)
	}
	if c.Env["WM_JOB_PATH"] != "f/t/main/branchone-3/c" || c.Env["WM_FLOW_STEP_ID"] != "c" {
		t.Fatalf("an inline branch step's environment %v", c.Env)
	}

	// No predicate holds: the default runs, at <flow>/branchone-0.
	s = &loopSteps{}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(
		branchOneOf("br", []string{branch(when("false"), value("x", `1`, ""))}, []string{mod("d", "echo", inputs("p", js("previous_result")), "")}, ""),
	)}, s)
	tr = runIt(t, e, `{"k":1}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `{"p":{"k":1}}`)
	if br := at(t, tr, "m:br"); br.BranchChosen == nil || br.BranchChosen.Type != "default" || br.BranchChosen.Branch != nil {
		t.Fatalf("chosen %+v", br.BranchChosen)
	}
	if d := s.inv(t, "m:br/b:default/m:d"); d.Env["WM_FLOW_PATH"] != "f/t/main/branchone-0" {
		t.Fatalf("the default's environment %v", d.Env)
	}
}

// CF-14, CF-23, JE-31: a predicate that throws stops the scan and fails
// the branchone in place, as an ordinary step failure named Anyhow,
// wherever the module is, with no job; continue_on_error then applies.
func TestBranchOnePredicateErrors(t *testing.T) {
	br := branchOneOf("br", []string{branch(when("nope"), value("x", "0", "")), branch(when("true"), value("y", "1", ""))}, nil, "")
	for _, first := range []bool{true, false} {
		mods := []string{br}
		if !first {
			mods = []string{value("a", "1", ""), br}
		}
		e := mustOpen(t, map[string]string{"f/t/main": flowOf(mods...)}, &loopSteps{})
		tr := runIt(t, e, `{}`, nil)
		wantStatus(t, tr, conformance.RunFailure, "")
		name, msg := errorOf(tr.Result)
		if name != "Anyhow" || !strings.HasPrefix(msg, "Error: QuickJS evaluation error: Error: nope is not defined") || tr.ResultOrigin != OriginEngine {
			t.Fatalf("first %v: the result %s, origin %q", first, tr.Result, tr.ResultOrigin)
		}
		rec := at(t, tr, "m:br")
		if rec.Ran || rec.Status != conformance.StepFailure || rec.Module != conformance.ModuleFailure || !rec.Result.IsZero() || rec.CarriedOrigin != OriginEngine || len(tr.Children) != 0 {
			t.Fatalf("first %v: the branchone %+v", first, rec)
		}
	}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		value("a", "1", ""),
		branchOneOf("br", []string{branch(when("nope"), value("x", "0", ""))}, nil, `"continue_on_error":true`),
		mod("n", "echo", inputs("p", js("previous_result.error.name")), ""),
	)}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `{"p":"Anyhow"}`)
}

// CF-14, OR-25, CF-19, Q-CF-4: an empty chosen branch runs no child: the
// branchone succeeds with a nil job and passes the previous_result handed
// to it through, the JSON string "{}" at the first module. Directly after
// it results.br is that value from memory; later a direct read of it is
// null, while an engine read fails to fetch its nil job. Its stop_after_if
// is evaluated with the value passed through.
func TestBranchOneEmptyBranch(t *testing.T) {
	empty := branchOneOf("br", []string{branch(when("false"), value("s", "1", ""))}, nil, "")
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		value("a", "42", ""),
		empty,
		mod("c", "echo", inputs("x", js("[results.br]")), ""),
		mod("d", "echo", inputs("x", js("results.br")), ""),
		mod("e", "echo", inputs("x", js("[results.br]")), ""),
	)}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "")
	br := at(t, tr, "m:br")
	if br.Ran || br.Status != conformance.StepSuccess || !br.Result.IsZero() || len(tr.Children) != 0 || br.BranchChosen.Type != "default" {
		t.Fatalf("the branchone %+v", br)
	}
	sameJSON(t, "what it carries", br.Carried, `42`)
	sameJSON(t, "c's arguments", at(t, tr, "m:c").RecordedArgs, `{"x":[42]}`)
	sameJSON(t, "d's arguments", at(t, tr, "m:d").RecordedArgs, `{"x":null}`)
	if _, msg := errorOf(at(t, tr, "m:e").Result); !strings.Contains(msg, "Failed to fetch result for step 'br': Not found: Completed Job not found at name 00000000-0000-0000-0000-000000000000") {
		t.Fatalf("e's failure: %s", msg)
	}

	e = mustOpen(t, map[string]string{"f/t/main": flowOf(empty)}, &loopSteps{})
	wantStatus(t, runIt(t, e, `{"k":1}`, nil), conformance.RunSuccess, `"{}"`)
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(value("a", "42", ""),
		branchOneOf("br", nil, nil, `"stop_after_if":{"expr":"result === 42","skip_if_stopped":true}`), value("n", "0", ""))}, &loopSteps{})
	tr = runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSkipped, `42`)
	noRecord(t, tr, "m:n")
}

// CF-12, FX-20, CF-14: a clean stop in the chosen branch stops the flow
// right after the branchone, with the same skipped flag; an error stop in
// it is an error stop of the branchone, which fails the flow even under
// continue_on_error; the branchone's own stop_after_if is evaluated only
// after its branch succeeded, as an ordinary stop.
func TestBranchOneStops(t *testing.T) {
	stopping := func(stop string) string {
		return value("x", "1", `"stop_after_if":{"expr":"true",`+stop+`"skip_if_stopped":false}`)
	}
	after := value("n", `"after"`, "")
	never := `"stop_after_if":{"expr":"nope","skip_if_stopped":false}`
	for _, c := range []struct {
		name    string
		flow    string
		status  conformance.RunStatus
		result  string
		br      conformance.StepStatus
		skipped bool
	}{
		{"clean stop", flowOf(branchOneOf("br", nil, []string{stopping("")}, never), after), conformance.RunSuccess, `1`, conformance.StepSuccess, false},
		{"skipped stop", flowOf(branchOneOf("br", nil, []string{value("x", "1", `"stop_after_if":{"expr":"true","skip_if_stopped":true}`)}, never), after), conformance.RunSkipped, `1`, conformance.StepSkipped, true},
		{"error stop", flowOf(branchOneOf("br", nil, []string{stopping(`"error_message":"E",`)}, `"continue_on_error":true`), after), conformance.RunFailure, `{"error":{"name":"EarlyStopError","message":"E"}}`, conformance.StepFailure, false},
		{"its own stop", flowOf(branchOneOf("br", nil, []string{value("x", "1", "")}, `"stop_after_if":{"expr":"result === 1","error_message":"own","skip_if_stopped":false}`), after), conformance.RunFailure, `{"error":{"name":"EarlyStopError","message":"own"}}`, conformance.StepSuccess, false},
		{"a failed branch", flowOf(branchOneOf("br", nil, []string{mod("x", "fail", "", "")}, never), after), conformance.RunFailure, `{"error":{"message":"boom","name":"Error","step_id":"x"}}`, conformance.StepFailure, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, &loopSteps{})
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, c.status, c.result)
			noRecord(t, tr, "m:n")
			if br := at(t, tr, "m:br"); br.Status != c.br || br.Skipped != c.skipped {
				t.Fatalf("the branchone %+v", br)
			}
		})
	}
	// The branch's stop propagates one level up: in a sequential loop's
	// body, it breaks the loop, and the flow goes on (CF-12).
	inner := branchOneOf("br", nil, []string{stopping("")}, "")
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		loopOf("l", []string{inner, value("y", "2", "")}, `"iterator":{"type":"static","value":[0,1]},"parallel":false,"skip_failures":false`, ""),
		after,
	)}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, `"after"`)
	sameJSON(t, "the loop's result", at(t, tr, "m:l").Result, `[1]`)
	noRecord(t, tr, "m:l/i:0/m:y")
}

// CF-15, CF-6, CF-12, CF-19, CF-24, WA-V1, Q-18 (decisions): a parallel
// branchall starts every branch at once and collects their results in
// branch order; there is no fail-fast, and a failure fails it, with the
// full array, only when its branch's skip_failure is false; a clean stop
// ends only its branch, and its own stop_after_if is not evaluated. Its
// branches write into the root's results scope and stay in its innermost
// scope; an empty branch returns its input.
func TestBranchAllParallel(t *testing.T) {
	g := newGated()
	g.hold = "/b:"
	held := func(k int) string {
		return branch(skipFailure(false), value(fmt.Sprintf("b%d", k), fmt.Sprintf(`"r%d"`, k), ""))
	}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(branchAllOf("ba", []string{held(0), held(1), held(2)}, true, `"stop_after_if":{"expr":"nope","skip_if_stopped":false}`))}, g)
	out := runAsync(context.Background(), e, `{}`, nil)
	seen := []string{g.next(t), g.next(t), g.next(t)}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"m:ba/b:0/m:b0", "m:ba/b:1/m:b1", "m:ba/b:2/m:b2"}) {
		t.Fatalf("started %v", seen)
	}
	for _, site := range []string{"m:ba/b:2/m:b2", "m:ba/b:0/m:b0", "m:ba/b:1/m:b1"} {
		g.done(site)
	}
	tr := <-out
	wantStatus(t, tr, conformance.RunSuccess, `["r0","r1","r2"]`)
	if len(tr.Children) != 3 || g.peak != 3 || tr.Children[1].Site.String() != "m:ba/b:1" {
		t.Fatalf("children %+v, peak %d", tr.Children, g.peak)
	}

	for _, skip := range []bool{true, false} {
		s := &loopSteps{}
		e := mustOpen(t, map[string]string{"f/t/main": flowOf(
			branchAllOf("ba", []string{
				branch(skipFailure(false), mod("x", "echo", inputs("p", js("previous_result")), "")),
				branch(skipFailure(skip), mod("y", "fail", "", "")),
				branch(skipFailure(false), value("z", "3", `"stop_after_if":{"expr":"true","skip_if_stopped":true}`)),
				branch(skipFailure(false)),
			}, true, ""),
			mod("n", "echo", inputs("x", js("results.x"), "z", js("results.z")), ""),
		)}, s)
		tr := runIt(t, e, `{"k":1}`, nil)
		arr := `[{"p":{"k":1}},{"error":{"message":"boom","name":"Error","step_id":"y"}},3,{"k":1}]`
		if !skip {
			wantStatus(t, tr, conformance.RunFailure, arr)
			noRecord(t, tr, "m:n")
			continue
		}
		wantStatus(t, tr, conformance.RunSuccess, `{"x":{"p":{"k":1}},"z":3}`)
		sameJSON(t, "the branchall's result", at(t, tr, "m:ba").Result, arr)
		if x := s.inv(t, "m:ba/b:0/m:x"); x.Env["WM_FLOW_PATH"] != "f/t/main/branchall-0" || x.Env["WM_ROOT_FLOW_JOB_ID"] != tr.RunID || x.Env["WM_FLOW_JOB_ID"] != tr.Children[0].ID {
			t.Fatalf("a branch step's environment %v", x.Env)
		}
	}
}

// CF-15, CF-12, CF-13, CF-29, CF-V2, JE-27, FX-20: a sequential branchall
// runs its branches in order and goes past a failed one only when its
// skip_failure is true. A stop in a branch, clean or error, stops the flow
// right after the branchall with the results so far: a clean one keeps
// its skipped flag, and an error one takes the branch's skip_failure as
// the status; its stop_after_all_iters_if is then not evaluated, but it is
// after a failure. A branchall with no branches evaluates its
// stop_after_if as an ordinary stop with [], never its
// stop_after_all_iters_if.
func TestBranchAllSequential(t *testing.T) {
	stopping := func(stop string) string {
		return value("b0", "7", `"stop_after_if":{"expr":"true",`+stop+`"skip_if_stopped":false}`)
	}
	b1 := branch(skipFailure(true), value("b1", "9", ""))
	never := `"stop_after_all_iters_if":{"expr":"nope","skip_if_stopped":false}`
	after := value("n", `"after"`, "")
	earlyStop := `[{"error":{"name":"EarlyStopError","message":"halt"}}]`
	for _, c := range []struct {
		name   string
		flow   string
		status conformance.RunStatus
		result string
		ba     conformance.StepStatus
		n      bool
	}{
		{"a failure ends it", flowOf(branchAllOf("ba", []string{branch(skipFailure(false), mod("b0", "fail", "", "")), b1}, false, ""), after), conformance.RunFailure, `[{"error":{"message":"boom","name":"Error","step_id":"b0"}}]`, conformance.StepFailure, false},
		{"skip_failure goes on", flowOf(branchAllOf("ba", []string{branch(skipFailure(true), mod("b0", "fail", "", "")), b1}, false, ""), after), conformance.RunSuccess, `"after"`, conformance.StepSuccess, true},
		{"an empty branch", flowOf(branchAllOf("ba", []string{branch(skipFailure(false)), b1}, false, "")), conformance.RunSuccess, `[{"k":1},9]`, conformance.StepSuccess, false},
		{"a clean stop", flowOf(branchAllOf("ba", []string{branch(skipFailure(false), stopping("")), b1}, false, never), after), conformance.RunSuccess, `[7]`, conformance.StepSuccess, false},
		{"a skipped stop", flowOf(branchAllOf("ba", []string{branch(skipFailure(false), value("b0", "7", `"stop_after_if":{"expr":"true","skip_if_stopped":true}`)), b1}, false, never), after), conformance.RunSkipped, `[7]`, conformance.StepSuccess, false},
		{"an error stop, skip_failure", flowOf(branchAllOf("ba", []string{branch(skipFailure(true), stopping(`"error_message":"halt",`)), b1}, false, never), after), conformance.RunSuccess, earlyStop, conformance.StepSuccess, false},
		{"an error stop", flowOf(branchAllOf("ba", []string{branch(skipFailure(false), stopping(`"error_message":"halt",`)), b1}, false, never), after), conformance.RunFailure, earlyStop, conformance.StepFailure, false},
		{"all iters, skipped", flowOf(branchAllOf("ba", []string{branch(skipFailure(true), value("b0", "7", "")), b1}, false, `"stop_after_all_iters_if":{"expr":"result.length === 2","skip_if_stopped":true}`), after), conformance.RunSkipped, `[7,9]`, conformance.StepSuccess, false},
		{"all iters after a failure", flowOf(branchAllOf("ba", []string{branch(skipFailure(false), mod("b0", "fail", "", "")), b1}, false, `"stop_after_all_iters_if":{"expr":"true","error_message":"all","skip_if_stopped":false}`), after), conformance.RunFailure, `{"error":{"name":"EarlyStopError","message":"all"}}`, conformance.StepFailure, false},
		{"all iters throws", flowOf(branchAllOf("ba", []string{b1}, false, never), after), conformance.RunFailure, "", conformance.StepFailure, false},
		{"no branches", flowOf(branchAllOf("ba", nil, false, `"stop_after_if":{"expr":"result.length === 0","skip_if_stopped":false},`+never), after), conformance.RunSuccess, `[]`, conformance.StepSuccess, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, &loopSteps{})
			tr := runIt(t, e, `{"k":1}`, nil)
			wantStatus(t, tr, c.status, c.result)
			if ba := at(t, tr, "m:ba"); ba.Status != c.ba {
				t.Fatalf("the branchall %+v", ba)
			}
			if _, ran := tr.Step(conformance.SitePath{}.Child(conformance.Module("n"))); ran != c.n {
				t.Fatalf("n ran: %v", ran)
			}
			if c.name == "all iters throws" {
				if _, msg := errorOf(tr.Result); !strings.HasPrefix(msg, "Error evaluating stop_after_all_iters_if expression `nope`: Error: QuickJS evaluation error: Error: nope is not defined") {
					t.Fatalf("the error stop %s", tr.Result)
				}
			}
		})
	}
	// A skipped stop flags the branchall skipped; branch 1 never runs.
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(branchAllOf("ba", []string{branch(skipFailure(false), value("b0", "7", `"stop_after_if":{"expr":"true","skip_if_stopped":true}`)), b1}, false, ""))}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	if ba := at(t, tr, "m:ba"); !ba.Skipped || len(tr.Children) != 1 {
		t.Fatalf("the branchall %+v, children %+v", ba, tr.Children)
	}
	noRecord(t, tr, "m:ba/b:1/m:b1")
}

// CF-7, CF-23: more than one branch of a parallel branchall in a
// same_worker flow fails its start, a chaining error routed by where it is
// raised; one branch, or a sequential branchall, runs.
func TestBranchAllSameWorker(t *testing.T) {
	two := []string{branch(skipFailure(false), value("x", "1", "")), branch(skipFailure(false), value("y", "2", ""))}
	sameWorker := func(mods ...string) string {
		return strings.TrimSuffix(flowOf(mods...), "}") + `,"same_worker":true}`
	}
	const msg = "Bad request: Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker"
	for _, c := range []struct {
		name, flow, err string
	}{
		{"at the pickup", sameWorker(branchAllOf("ba", two, true, "")), `{"error":{"message":"Error: ` + msg + `","name":"Anyhow"}}`},
		{"later", sameWorker(value("a", "0", ""), branchAllOf("ba", two, true, "")), `{"error":{"message":"` + msg + `","name":"InternalError"}}`},
		{"one branch", sameWorker(branchAllOf("ba", two[:1], true, "")), ""},
		{"sequential", sameWorker(branchAllOf("ba", two, false, "")), ""},
	} {
		e := mustOpen(t, map[string]string{"f/t/main": c.flow}, &loopSteps{})
		tr := runIt(t, e, `{}`, nil)
		if c.err == "" {
			wantStatus(t, tr, conformance.RunSuccess, "")
			continue
		}
		wantStatus(t, tr, conformance.RunFailure, c.err)
		if len(tr.Children) != 0 {
			t.Fatalf("%s: children %+v", c.name, tr.Children)
		}
	}
}

// ADR 0008 in branches: H6 observes the container and each branch module;
// a body module's halt-after halts while the run would start more (the
// next branch of a sequential branchall, or what follows the container)
// and changes nothing otherwise; a latch in a parallel branch lets the
// others finish and ends the run latched; a cancellation abandons the
// running branch and starts no other.
func TestBranchHostSignals(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(branchOneOf("br", []string{branch(when("true"), value("x", "1", ""))}, nil, ""))}, &loopSteps{})
	h := &recordingHooks{}
	wantStatus(t, runIt(t, e, `{}`, h), conformance.RunSuccess, `1`)
	if want := []string{"start m:br", "start m:br/b:0/m:x", "end m:br/b:0/m:x success", "end m:br success"}; !slices.Equal(h.calls, want) {
		t.Fatalf("calls %q\nwant %q", h.calls, want)
	}

	two := func(parallel bool) string {
		return branchAllOf("ba", []string{branch(skipFailure(false), value("x", "1", "")), branch(skipFailure(false), value("y", "2", ""))}, parallel, "")
	}
	after := value("n", "0", "")
	for _, c := range []struct {
		name, flow, at string
		status         conformance.RunStatus
		invocations    int
	}{
		{"sequential, a branch follows", flowOf(two(false)), "m:ba/b:0/m:x", conformance.RunHalted, 1},
		{"sequential, the last branch", flowOf(two(false)), "m:ba/b:1/m:y", conformance.RunSuccess, 2},
		{"sequential, then a module", flowOf(two(false), after), "m:ba/b:1/m:y", conformance.RunHalted, 2},
		{"parallel, then a module", flowOf(two(true), after), "m:ba/b:0/m:x", conformance.RunHalted, -1},
		{"parallel, nothing after", flowOf(two(true)), "m:ba/b:0/m:x", conformance.RunSuccess, 2},
		{"parallel, a failure ends the run", flowOf(branchAllOf("ba", []string{branch(skipFailure(false), mod("x", "fail", "", "")), branch(skipFailure(false), value("y", "2", ""))}, true, ""), after), "m:ba/b:0/m:x", conformance.RunFailure, 2},
		{"branchone, then a module", flowOf(branchOneOf("br", nil, []string{value("x", "1", "")}, ""), after), "m:br/b:default/m:x", conformance.RunHalted, 1},
		{"branchone, a stop ends the flow", flowOf(branchOneOf("br", nil, []string{value("x", "1", `"stop_after_if":{"expr":"true","skip_if_stopped":false}`)}, ""), after), "m:br/b:default/m:x", conformance.RunSuccess, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &loopSteps{}
			e := mustOpen(t, map[string]string{"f/t/main": c.flow}, s)
			tr := runIt(t, e, `{}`, haltAt{after: c.at})
			if tr.Status != c.status || c.invocations >= 0 && len(s.invs) != c.invocations {
				t.Fatalf("status %s, invocations %d, result %s", tr.Status, len(s.invs), tr.Result)
			}
		})
	}

	// A latch in one parallel branch: the other runs to its end.
	g := newGated()
	g.hold = "/b:"
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(branchAllOf("ba", []string{branch(skipFailure(false), value("b", "1", "")), branch(skipFailure(false), value("c", "2", ""))}, true, ""), after)}, g)
	out := runAsync(context.Background(), e, `{}`, latchAfter{})
	first := []string{g.next(t), g.next(t)}
	slices.Sort(first)
	g.done("m:ba/b:0/m:b")
	g.done("m:ba/b:1/m:c")
	tr := <-out
	if tr.Status != conformance.RunLatched || at(t, tr, "m:ba/b:1/m:c").Status != conformance.StepSuccess {
		t.Fatalf("latched: status %s", tr.Status)
	}
	noRecord(t, tr, "m:n")

	// A cancellation in a sequential branchall starts no further branch.
	g = newGated()
	g.hold = "/b:"
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(two(false))}, g)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out = runAsync(ctx, e, `{}`, nil)
	g.next(t)
	cancel()
	tr = <-out
	if tr.Status != conformance.RunCanceled || len(g.started) != 1 || at(t, tr, "m:ba").Status != conformance.StepCanceled {
		t.Fatalf("canceled: status %s, started %v", tr.Status, g.started)
	}
}

// ADR 0011, D14: errors the engine generates in a branch keep their
// provenance: inside a branchall's array as pointers into it, and as the
// origin of a branchone's result; a user's error has none.
func TestBranchProvenance(t *testing.T) {
	bad := loopOf("l", []string{value("x", "1", "")}, `"iterator":{"type":"static","value":{}},"parallel":false,"skip_failures":false`, "")
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		branchAllOf("ba", []string{branch(skipFailure(true), mod("u", "fail", inputs("name", static(`"Anyhow"`)), "")), branch(skipFailure(true), bad)}, true, ""),
	)}, &loopSteps{})
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, "")
	if ba := at(t, tr, "m:ba"); !slices.Equal(ba.ResultNested, []string{"/1"}) || !slices.Equal(tr.ResultNested, []string{"/1"}) {
		t.Fatalf("the branchall %+v, the run's %v", ba, tr.ResultNested)
	}
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(branchOneOf("br", nil, []string{bad}, ""))}, &loopSteps{})
	tr = runIt(t, e, `{}`, nil)
	if br := at(t, tr, "m:br"); br.ResultOrigin != OriginEngine || tr.ResultOrigin != OriginEngine {
		t.Fatalf("the branchone %+v, the run's origin %q", br, tr.ResultOrigin)
	}
}

// Parallel branches share the engine with concurrent runs (run with
// -race).
func TestBranchConcurrentRuns(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		branchAllOf("ba", []string{
			branch(skipFailure(false), value("x", "1", ""), value("y", "2", "")),
			branch(skipFailure(false), branchOneOf("br", []string{branch(when("flow_input.k > 0"), value("z", "3", ""))}, nil, "")),
		}, true, ""),
		mod("n", "echo", inputs("y", js("results.y"), "z", js("results.z")), ""),
	)}, &loopSteps{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{"k":1}`))})
			switch {
			case err != nil:
				errs <- err
			case tr.Status != conformance.RunSuccess:
				errs <- errors.New(string(tr.Status))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

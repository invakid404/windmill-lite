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
	"testing/fstest"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// The engine's tests compile synthetic sync folders with the IR compiler
// and run them on the real QuickJS-ng engine, with steps answered in Go:
// path scripts under f/t, named by their inventory ids, which are their
// paths. The flow fixtures of conformance/flows run the same engine on the
// real Bun harness (cmd/wltest, TestFlowSuite).

// scripts answers the test folders' path scripts:
//
//   - f/t/echo returns its arguments;
//   - f/t/value returns its argument v;
//   - f/t/obj returns {"zz":1,"b":2,"aaa":3}, in that key order;
//   - f/t/fail throws an Error named by its argument name, "Error" by
//     default;
//   - f/t/zero, whose metadata asks for a timeout of 0 s, is not run here;
//   - f/t/flaky fails once per site and run, then returns its arguments
//     (retry_test.go's flaky executor).
type scripts struct {
	mu   sync.Mutex
	invs []Invocation
}

func (s *scripts) Execute(_ context.Context, inv Invocation) (StepOutcome, error) {
	s.mu.Lock()
	s.invs = append(s.invs, inv)
	s.mu.Unlock()
	defer inv.Report.Settled(SettleCompleted)
	var args map[string]json.RawMessage
	if err := json.Unmarshal(inv.Args.Producer(), &args); err != nil {
		return StepOutcome{}, err
	}
	ok := func(text string) (StepOutcome, error) {
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte(text))}, nil
	}
	switch inv.Script {
	case "f/t/echo":
		return StepOutcome{Kind: OutcomeOK, Body: inv.Args}, nil
	case "f/t/value":
		if v, has := args["v"]; has {
			return ok(string(v))
		}
		return ok("null")
	case "f/t/obj":
		return ok(`{"zz":1,"b":2,"aaa":3}`)
	case "f/t/fail":
		name := `"Error"`
		if n, has := args["name"]; has {
			name = string(n)
		}
		return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"message":"boom","name":` + name + `,"step_id":"` + inv.Env["WM_FLOW_STEP_ID"] + `"}`))}, nil
	}
	return StepOutcome{}, errors.New("no script " + inv.Script)
}

func (s *scripts) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, inv := range s.invs {
		out = append(out, inv.Site.String())
	}
	return out
}

// testFolder is a sync folder with the f/t scripts and the given flows,
// each a flow value as JSON.
func testFolder(flows map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{"wmill.yaml": {Data: []byte("defaultTs: bun\n")}}
	for _, name := range []string{"echo", "value", "obj", "fail", "zero", "flaky"} {
		fsys["f/t/"+name+".ts"] = &fstest.MapFile{Data: []byte("export async function main(...a: any[]) { return null }\n")}
	}
	// f/t/zero's own metadata asks for a timeout of 0 s (FX-24).
	fsys["f/t/zero.script.yaml"] = &fstest.MapFile{Data: []byte("summary: ''\ndescription: ''\nkind: script\ntimeout: 0\nschema:\n  type: object\n  properties: {}\n  required: []\n")}
	for path, value := range flows {
		fsys[path+".flow/flow.yaml"] = &fstest.MapFile{Data: []byte(`{"summary":"t","value":` + value + `}`)}
	}
	return fsys
}

// The QuickJS-ng engine the tests share.
var (
	jsOnce sync.Once
	jsEng  *quickjs.Engine
	jsErr  error
)

// newTestJS starts the QuickJS-ng engine the tests share.
func newTestJS() (*quickjs.Engine, error) { return quickjs.New(quickjs.Options{Slots: 2}) }

// openEngine compiles the flows, every one a root, and opens them.
func openEngine(t *testing.T, flows map[string]string, exec StepExecutor) (*Engine, error) {
	t.Helper()
	jsOnce.Do(func() { jsEng, jsErr = newTestJS() })
	if jsErr != nil {
		t.Fatal(jsErr)
	}
	folder, err := syncfolder.Load(testFolder(flows))
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	prog, err := ir.Compile(folder, nil, ir.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Program: prog, Bundle: "sha256:test", Exec: exec, JS: jsEng})
}

func mustOpen(t *testing.T, flows map[string]string, exec StepExecutor) *Engine {
	t.Helper()
	e, err := openEngine(t, flows, exec)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// runIt runs the flow f/t/main with args, hooks and a fixed seed.
func runIt(t *testing.T, e *Engine, args string, hooks Hooks) *Trace {
	t.Helper()
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(args)), Hooks: hooks, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// mod writes a module that runs the script f/t/<script>, with input
// transforms and envelope fields, as JSON.
func mod(id, script, inputs, envelope string) string {
	if inputs == "" {
		inputs = "{}"
	}
	if envelope != "" {
		envelope = "," + envelope
	}
	return fmt.Sprintf(`{"id":%q,"value":{"type":"script","path":"f/t/%s","input_transforms":%s}%s}`, id, script, inputs, envelope)
}

func flowOf(mods ...string) string { return `{"modules":[` + strings.Join(mods, ",") + `]}` }

func js(text string) string {
	b, _ := json.Marshal(map[string]string{"type": "javascript", "expr": text})
	return string(b)
}

func static(v string) string { return `{"type":"static","value":` + v + `}` }

func inputs(kv ...string) string {
	var parts []string
	for i := 0; i < len(kv); i += 2 {
		parts = append(parts, fmt.Sprintf("%q:%s", kv[i], kv[i+1]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func wantStatus(t *testing.T, tr *Trace, st conformance.RunStatus, result string) {
	t.Helper()
	if tr.Status != st {
		t.Fatalf("status %s, want %s (result %s, latched %v)", tr.Status, st, tr.Result, tr.Latched)
	}
	if result != "" {
		sameJSON(t, "the run's result", tr.Result, result)
	}
}

// FX-7, JE-23, JE-40, FX-6, FX-10: input transforms, static values in
// their stored form, params, the results routes from memory and from
// storage, and an unknown id.
func TestEngineTransformsAndResults(t *testing.T) {
	s := &scripts{}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		mod("a", "obj", inputs("n", static(`1e3`), "flow", js("flow_input"), "prev", js("previous_result"), "params", js("params")), ""),
		mod("b", "echo", inputs(
			"memory", js("Object.keys(results.a)"),
			"path", js("results.a.zz"),
			"storage", js("Object.keys(await results[flow_input.t])"),
			"memory_by_name", js("previous_result && Object.keys(await results[flow_input.t])"),
			"whole", js("results.a"),
			"unknown", js("results.zzz"),
			"unknown_engine", js("(await results.zzz) ?? 'none'"),
		), ""),
	)}, s)
	tr := runIt(t, e, `{"t":"a","x":1}`, nil)
	wantStatus(t, tr, conformance.RunSuccess, "")
	a := step(t, tr, "a")
	// A static value is stored as jsonb (ADR 0005 step 3); the first
	// step's previous_result is the flow input (FX-6).
	sameJSON(t, "a's arguments", a.RecordedArgs, `{"n":1000,"flow":{"t":"a","x":1},"prev":{"t":"a","x":1},"params":{}}`)
	b := step(t, tr, "b")
	sameJSON(t, "b's arguments", b.RecordedArgs, `{"memory":["zz","b","aaa"],"path":1,"storage":["b","zz","aaa"],"memory_by_name":["zz","b","aaa"],"whole":{"zz":1,"b":2,"aaa":3},"unknown":null,"unknown_engine":"none"}`)
	if got := s.calls(); !slices.Equal(got, []string{"m:a", "m:b"}) {
		t.Fatalf("calls %v", got)
	}
}

// argsSeen records the arguments H2 sees, before the storage boundary.
type argsSeen struct {
	NoHooks
	args map[string]string
}

func (h *argsSeen) RewriteArgs(_ context.Context, _ Run, s Site, args jsonval.Value) (jsonval.Value, error) {
	h.args[s.Module] = string(args.Producer())
	return args, nil
}

// JE-44's table of texts: the flow input is a compact object whose
// members are in jsonb's printed form, a space after each : and , inside
// them. A shortcut answers with those texts verbatim (JE-6, JE-7), which
// the storage boundary then normalises (FX-42).
func TestEngineFlowInputText(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", inputs("o", js("flow_input.o"), "p", js("previous_result")), ""))}, &scripts{})
	h := &argsSeen{args: map[string]string{}}
	tr := runIt(t, e, `{"o":{"b":[1,2e0],"a":1}}`, h)
	wantStatus(t, tr, conformance.RunSuccess, `{"o":{"a":1,"b":[1,2]},"p":{"o":{"a":1,"b":[1,2]}}}`)
	ms, _ := members([]byte(h.args["a"]))
	got := map[string]string{}
	for _, m := range ms {
		got[m.key] = string(m.value)
	}
	if got["o"] != `{"a": 1, "b": [1, 2]}` || got["p"] != `{"o":{"a": 1, "b": [1, 2]}}` {
		t.Fatalf("the shortcuts' texts: %v", got)
	}
	if a := step(t, tr, "a"); string(a.RecordedArgs.Producer()) != `{"o":{"a":1,"b":[1,2]},"p":{"o":{"a":1,"b":[1,2]}}}` && string(a.RecordedArgs.Producer()) != `{"p":{"o":{"a":1,"b":[1,2]}},"o":{"a":1,"b":[1,2]}}` {
		t.Fatalf("the stored arguments: %s", a.RecordedArgs)
	}
}

// ADR 0001 §2, ADR 0005 step 2: the top-level order of the flow input and
// of every argument map is shuffled by the run's seed, the same for one
// seed and not the same for every seed.
func TestEngineShufflesUnspecifiedOrders(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		mod("a", "echo", inputs("a", static("1"), "b", static("2"), "c", static("3"), "d", static("4"), "e", static("5"), "keys", js("Object.keys(flow_input)")), ""),
	)}, &scripts{})
	order := func(seed uint64) string {
		tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{"p":1,"q":2,"r":3,"s":4,"u":5}`)), Seed: seed})
		if err != nil {
			t.Fatal(err)
		}
		ms, _ := members(step(t, tr, "a").RecordedArgs.Producer())
		var keys []string
		for _, m := range ms {
			keys = append(keys, m.key)
		}
		return strings.Join(keys, ",") + " " + string(step(t, tr, "a").Result.Producer())
	}
	seen := map[string]bool{}
	for seed := uint64(1); seed <= 8; seed++ {
		o := order(seed)
		if again := order(seed); again != o {
			t.Fatalf("seed %d gave %s, then %s", seed, o, again)
		}
		seen[o] = true
	}
	if len(seen) < 2 {
		t.Fatalf("eight seeds gave one order: %v", seen)
	}
}

// FX-16, FX-17, FX-18: identity, skip_if and mock all run an identity job
// whose result is its arguments' previous_result; a skipped or mocked
// module never launches, and its transforms are not evaluated.
func TestEngineIdentitySkipAndMock(t *testing.T) {
	boom := inputs("x", js("null.boom"))
	for _, c := range []struct {
		name, module, args string
		// skipped, the record's arguments and result, and the flow's
		// result; launched says whether b ran.
		skipped            bool
		recArgs, recResult string
		launched           bool
	}{
		{name: "skip", module: mod("b", "fail", boom, `"skip_if":{"expr":"previous_result.v === 1"}`), skipped: true, recArgs: `{"previous_result":{"v":1}}`, recResult: `{"v":1}`},
		{name: "no skip", module: mod("b", "value", inputs("v", static("2")), `"skip_if":{"expr":"previous_result.v === 9"}`), recArgs: `{"v":2}`, recResult: `2`, launched: true},
		{name: "mock", module: mod("b", "fail", boom, `"mock":{"enabled":true,"return_value":{"m":1}}`), recArgs: `{"previous_result":{"m":1}}`, recResult: `{"m":1}`},
		{name: "mock without value", module: mod("b", "fail", boom, `"mock":{"enabled":true}`), recArgs: `{"previous_result":null}`, recResult: `null`},
		// An identity with any skip_if is classified skipped, even when
		// the predicate says no (FX-18).
		{name: "mock and skip_if false", module: mod("b", "fail", boom, `"mock":{"enabled":true,"return_value":7},"skip_if":{"expr":"false"}`), skipped: true, recArgs: `{"previous_result":7}`, recResult: `7`},
		{name: "mock disabled", module: mod("b", "value", inputs("v", static("3")), `"mock":{"enabled":false,"return_value":7}`), recArgs: `{"v":3}`, recResult: `3`, launched: true},
		{name: "identity", module: `{"id":"b","value":{"type":"identity"}}`, recArgs: `{"previous_result":{"v":1}}`, recResult: `{"v":1}`},
		// pass_flow_input_directly: the arguments are the flow input, so
		// an identity returns its previous_result field, or {} (FX-16/a).
		{name: "identity, direct input", module: `{"id":"b","pass_flow_input_directly":true,"value":{"type":"identity"}}`, recArgs: `{"in":1}`, recResult: `{}`},
		{name: "leaf, direct input", module: mod("b", "echo", boom, `"pass_flow_input_directly":true`), recArgs: `{"in":1}`, recResult: `{"in":1}`, launched: true},
		{name: "skip, direct input", module: mod("b", "fail", boom, `"pass_flow_input_directly":true,"skip_if":{"expr":"true"}`), skipped: true, recArgs: `{"in":1}`, recResult: `{}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", inputs("v", static("1")), ""), c.module)}, s)
			tr := runIt(t, e, `{"in":1}`, nil)
			wantStatus(t, tr, conformance.RunSuccess, c.recResult)
			b := step(t, tr, "b")
			if b.Skipped != c.skipped || !b.Ran || b.Status != conformance.StepSuccess || b.Module != conformance.ModuleSuccess {
				t.Fatalf("b %+v", b)
			}
			sameJSON(t, "b's arguments", b.RecordedArgs, c.recArgs)
			sameJSON(t, "b's result", b.Result, c.recResult)
			if launched := len(s.calls()) == 2; launched != c.launched {
				t.Fatalf("b launched: %v, want %v", launched, c.launched)
			}
		})
	}
}

// FX-19, FX-21, FX-V2, JE-26: stop_after_if after a success, with the
// module's own result as result and previous_result: the clean stops, the
// error stop and its message, and a throwing predicate.
func TestEngineStopAfterIf(t *testing.T) {
	for _, c := range []struct {
		name, stop string
		status     conformance.RunStatus
		result     string
		module     conformance.ModuleStatus
		skipped    bool
	}{
		{"no stop", `{"expr":"result === 9","skip_if_stopped":false}`, conformance.RunSuccess, `"c"`, conformance.ModuleSuccess, false},
		{"clean", `{"expr":"result === 3 && previous_result === 3","skip_if_stopped":false}`, conformance.RunSuccess, `3`, conformance.ModuleSuccess, false},
		{"skipped", `{"expr":"result > 2","skip_if_stopped":true,"error_message":"ignored"}`, conformance.RunSkipped, `3`, conformance.ModuleSuccess, true},
		{"error", `{"expr":"result > 2","skip_if_stopped":false,"error_message":"too big"}`, conformance.RunFailure, `{"error":{"name":"EarlyStopError","message":"too big"}}`, conformance.ModuleFailure, false},
		{"empty message", `{"expr":" result > 2 ","skip_if_stopped":false,"error_message":"","error_include_result":true}`, conformance.RunFailure, `{"error":{"name":"EarlyStopError","message":"stop after if:  result > 2 ","result":3}}`, conformance.ModuleFailure, false},
		{"throws", `{"expr":"null.x","skip_if_stopped":false}`, conformance.RunFailure,
			`{"error":"Error: QuickJS evaluation error: Error: cannot read property 'x' of null\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:44)\n"}`, conformance.ModuleFailure, false},
		{"not a boolean", `{"expr":"false ||\nresults.b\n","skip_if_stopped":false}`, conformance.RunFailure, `{"error":"Expected a boolean value, found: \"3\""}`, conformance.ModuleFailure, false},
		// results is bound only when the text holds results., results[ or
		// results?.; flow_env only when it holds flow_env.
		{"no results", `{"expr":"typeof results === 'undefined' && typeof flow_input === 'object'","skip_if_stopped":false}`, conformance.RunSuccess, `3`, conformance.ModuleSuccess, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(
				mod("b", "value", inputs("v", static("3")), `"stop_after_if":`+c.stop),
				mod("c", "value", inputs("v", static(`"c"`)), ""),
			)}, s)
			tr := runIt(t, e, `{}`, nil)
			wantStatus(t, tr, c.status, c.result)
			b := step(t, tr, "b")
			// The job succeeded and keeps its result whatever the stop made
			// of the module (FX-V2).
			if b.Status != conformance.StepSuccess || b.Module != c.module || b.Skipped != c.skipped || string(b.Result.Producer()) != "3" {
				t.Fatalf("b %+v", b)
			}
			sameJSON(t, "b's carried value", b.Carried, map[bool]string{true: c.result, false: "3"}[c.module == conformance.ModuleFailure])
			if ran := len(s.calls()) == 2; ran != (c.name == "no stop") {
				t.Fatalf("c ran: %v", ran)
			}
		})
	}
}

// FX-15, JE-24, JE-35: a skip_if that throws or is not a boolean is a
// chaining error: at the first module, the root's first pickup, a failure
// of that module named Anyhow; later, the flow fails at once with an
// InternalError and the module never starts. windmill-lite writes no Rust
// location (D14).
func TestEngineSkipIfChainingErrors(t *testing.T) {
	const thrown = `QuickJS evaluation error: Error: nope is not defined\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:42)\n`
	for _, c := range []struct {
		name, flow, result string
		at                 string
		status             conformance.StepStatus
	}{
		{"pickup", flowOf(mod("a", "value", "", `"skip_if":{"expr":"nope"}`)), `{"error":{"name":"Anyhow","message":"Error: Error: ` + thrown + `"}}`, "a", conformance.StepFailure},
		{"pickup, not a boolean", flowOf(mod("a", "value", "", `"skip_if":{"expr":"/;/.test('a;b')"}`)), `{"error":{"name":"Anyhow","message":"Error: Expected a boolean value, found: \"null\""}}`, "a", conformance.StepFailure},
		{"later", flowOf(mod("a", "value", "", ""), mod("b", "value", "", `"skip_if":{"expr":"nope"}`)), `{"error":{"name":"InternalError","message":"Error: ` + thrown + `"}}`, "b", conformance.StepNotRun},
		{"later, not a boolean", flowOf(mod("a", "value", inputs("v", static("7")), ""), mod("b", "value", "", `"skip_if":{"expr":"true ||\nresults.a\n"}`)), `{"error":{"name":"InternalError","message":"Expected a boolean value, found: \"7\""}}`, "b", conformance.StepNotRun},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := runIt(t, mustOpen(t, map[string]string{"f/t/main": c.flow}, &scripts{}), `{}`, nil)
			wantStatus(t, tr, conformance.RunFailure, c.result)
			if tr.ResultOrigin != OriginEngine {
				t.Fatalf("origin %q", tr.ResultOrigin)
			}
			if r := step(t, tr, c.at); r.Ran || r.Status != c.status {
				t.Fatalf("%s %+v", c.at, r)
			}
		})
	}
}

// FX-14, WR-12, FX-48: a failing transform launches the step with empty
// arguments and fails it without running; an entrypoint override selects
// the export, and an invalid one fails the job before it runs; an argument
// jsonb refuses fails the push, a chaining error.
func TestEngineArgumentFailures(t *testing.T) {
	s := &scripts{}
	e := mustOpen(t, map[string]string{
		"f/t/main":  flowOf(mod("a", "echo", inputs("x", js(" null.x ")), "")),
		"f/t/entry": flowOf(mod("a", "echo", inputs("_ENTRYPOINT_OVERRIDE", static(`"other"`)), ""), mod("b", "echo", inputs("_ENTRYPOINT_OVERRIDE", static(`"no-way"`)), "")),
		"f/t/nul":   flowOf(mod("a", "echo", inputs("x", static(`"a\u0000b"`)), "")),
		"f/t/nul2":  flowOf(mod("a", "echo", "", ""), mod("b", "echo", inputs("x", js(`"a\u0000b"`)), "")),
	}, s)
	tr := runIt(t, e, `{}`, nil)
	wantStatus(t, tr, conformance.RunFailure, "{\"error\":{\"name\":\"ExecutionErr\",\"message\":\"Error during isolated evaluation of expression ` null.x `:\\nQuickJS evaluation error: Error: cannot read property 'x' of null\\n    at <anonymous> (eval_script:1:1)\\n    at <eval> (eval_script:1:35)\\n\"}}")
	if a := step(t, tr, "a"); !a.Ran || string(a.RecordedArgs.Producer()) != "{}" || a.ResultOrigin != OriginEngine || len(s.calls()) != 0 {
		t.Fatalf("a %+v, calls %v", a, s.calls())
	}

	run := func(path string) *Trace {
		tr, err := e.Run(context.Background(), RunRequest{Path: path, Args: jsonval.FromProducer([]byte(`{}`))})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	tr = run("f/t/entry")
	wantStatus(t, tr, conformance.RunFailure, `{"error":{"name":"ExecutionErr","message":"Invalid entrypoint override: \"no-way\""}}`)
	if inv := s.invs[0]; inv.Entry != "other" || len(s.invs) != 1 {
		t.Fatalf("invocations %+v", s.invs)
	}
	wantStatus(t, run("f/t/nul"), conformance.RunFailure, `{"error":{"name":"Anyhow","message":"Error: error returned from database: unsupported Unicode escape sequence"}}`)
	wantStatus(t, run("f/t/nul2"), conformance.RunFailure, `{"error":{"name":"InternalError","message":"error returned from database: unsupported Unicode escape sequence"}}`)
}

// FX-3: a flow with no modules returns its input. OF-2: a module id that
// occurs twice gets two sites. JE-9's direct lookup reads the node-id map,
// where the module that finished last wins (JE-19); the engine path reads
// the first module with that id that has a job (JE-18 step 2), a reading:
// the reference does not say which of two status entries it finds.
func TestEngineEmptyFlowAndRepeatedIDs(t *testing.T) {
	e := mustOpen(t, map[string]string{
		"f/t/main": `{"modules":[]}`,
		"f/t/rep":  flowOf(mod("a", "value", inputs("v", static("1")), ""), mod("a", "value", inputs("v", static("2")), ""), mod("m", "value", inputs("v", static("0")), ""), mod("c", "echo", inputs("direct", js("results.a"), "engine", js("(await results.a)"), "prev", js("previous_result")), "")),
	}, &scripts{})
	wantStatus(t, runIt(t, e, `{"b":1,"a":2}`, nil), conformance.RunSuccess, `{"a":2,"b":1}`)
	tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/rep", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, tr, conformance.RunSuccess, `{"direct":2,"engine":1,"prev":0}`)
	var sites []string
	for _, st := range tr.Steps {
		sites = append(sites, st.Site.String())
	}
	if !slices.Equal(sites, []string{"m:a~0", "m:a~1", "m:m", "m:c"}) {
		t.Fatalf("sites %v", sites)
	}
}

// recordingHooks records the hook calls and answers some of them.
type recordingHooks struct {
	NoHooks
	mu        sync.Mutex
	calls     []string
	rewrite   map[string]string // module → arguments H2 returns
	writes    map[string]bool   // write-capable modules (H3)
	veto      bool              // BeforeFirstWrite vetoes
	fatal     string            // an error name that latches (H7)
	haltBefor string
	haltAfter string
	env       map[string]string // set in every invocation (H12)
}

func (h *recordingHooks) log(f string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, fmt.Sprintf(f, args...))
}

func (h *recordingHooks) RewriteArgs(_ context.Context, _ Run, s Site, args jsonval.Value) (jsonval.Value, error) {
	if v, ok := h.rewrite[s.Module]; ok {
		return jsonval.FromProducer([]byte(v)), nil
	}
	return args, nil
}

func (h *recordingHooks) WriteCapable(s Site) bool { return h.writes[s.Module] }

func (h *recordingHooks) BeforeFirstWrite(_ context.Context, _ Run, s Site) error {
	h.log("first write %s", s.Module)
	if h.veto {
		return errors.New("no writes")
	}
	return nil
}

func (h *recordingHooks) FatalErrorName(name string) bool { return name == h.fatal }
func (h *recordingHooks) HaltBefore(s Site) bool          { return s.Module == h.haltBefor }
func (h *recordingHooks) HaltAfter(s Site) bool           { return s.Module == h.haltAfter }

func (h *recordingHooks) OnModuleStart(_ context.Context, _ Run, s Site) error {
	h.log("start %s", s.Path)
	return nil
}

func (h *recordingHooks) OnModuleEnd(_ context.Context, _ Run, s Site, rec StepRecord) error {
	h.log("end %s %s", s.Path, rec.Module)
	return nil
}

func (h *recordingHooks) InvocationEnv(_ context.Context, _ Run, _ Site, env map[string]string) error {
	for k, v := range h.env {
		env[k] = v
	}
	return nil
}

// ADR 0008: H2 rewrites a launching module's arguments and the trace keeps
// both; H3 asks once, before the first write-capable launch, and a veto
// latches; H6 observes every module; H7 latches a fatal error name; H8
// halts before or after a module, never after the last; H12 changes an
// invocation's environment.
func TestEngineHooks(t *testing.T) {
	flow := flowOf(mod("a", "echo", inputs("x", static("1")), ""), mod("b", "echo", inputs("x", static("2")), ""), mod("c", "fail", inputs("name", static(`"Fatal"`)), ""))
	open := func() (*Engine, *scripts) {
		s := &scripts{}
		return mustOpen(t, map[string]string{"f/t/main": flow}, s), s
	}

	e, s := open()
	h := &recordingHooks{rewrite: map[string]string{"b": `{"x":20}`}, writes: map[string]bool{"a": true, "b": true}, env: map[string]string{"HOST": "yes"}}
	tr := runIt(t, e, `{}`, h)
	wantStatus(t, tr, conformance.RunFailure, `{"error":{"message":"boom","name":"Fatal","step_id":"c"}}`)
	b := step(t, tr, "b")
	if !slices.Equal(b.Hooks, []string{"H2"}) || string(b.ArgsBeforeRewrite.Producer()) != `{"x":2}` || string(b.RecordedArgs.Producer()) != `{"x":20}` || string(s.invs[1].Args.Producer()) != `{"x":20}` {
		t.Fatalf("H2: b %+v, invocation args %s", b, s.invs[1].Args)
	}
	if !tr.MayHaveMutated || s.invs[0].Env["HOST"] != "yes" {
		t.Fatalf("H3 or H12: mutated %v, env %v", tr.MayHaveMutated, s.invs[0].Env)
	}
	if want := []string{"start m:a", "first write a", "end m:a success", "start m:b", "end m:b success", "start m:c", "end m:c failure"}; !slices.Equal(h.calls, want) {
		t.Fatalf("calls %q\nwant %q", h.calls, want)
	}

	e, s = open()
	tr = runIt(t, e, `{}`, &recordingHooks{writes: map[string]bool{"b": true}, veto: true})
	if tr.Status != conformance.RunLatched || tr.Latched == nil || tr.Latched.Error() != "no writes" || tr.MayHaveMutated || len(s.invs) != 1 {
		t.Fatalf("a vetoed write: status %s, latched %v, mutated %v, invocations %d", tr.Status, tr.Latched, tr.MayHaveMutated, len(s.invs))
	}
	if b := step(t, tr, "b"); b.Ran || b.Status != conformance.StepNotRun {
		t.Fatalf("b %+v", b)
	}

	e, _ = open()
	tr = runIt(t, e, `{}`, &recordingHooks{fatal: "Fatal"})
	var fe *FatalError
	if tr.Status != conformance.RunLatched || !errors.As(tr.Latched, &fe) || fe.Name != "Fatal" || !slices.Equal(tr.Deviations, []string{"H7"}) || !slices.Equal(step(t, tr, "c").Hooks, []string{"H7"}) {
		t.Fatalf("H7: status %s, latched %v, deviations %v", tr.Status, tr.Latched, tr.Deviations)
	}

	e, s = open()
	tr = runIt(t, e, `{}`, &recordingHooks{haltBefor: "b"})
	if _, ok := tr.Step(conformance.SitePath{}.Child(conformance.Module("b"))); tr.Status != conformance.RunHalted || ok || len(s.invs) != 1 || !tr.Result.IsZero() {
		t.Fatalf("haltBefore: status %s, steps %+v", tr.Status, tr.Steps)
	}
	e, s = open()
	tr = runIt(t, e, `{}`, &recordingHooks{haltAfter: "a"})
	if tr.Status != conformance.RunHalted || len(s.invs) != 1 || !slices.Equal(step(t, tr, "a").Hooks, []string{"H8"}) {
		t.Fatalf("haltAfter: status %s, steps %+v", tr.Status, tr.Steps)
	}
	// A completion that ends the run keeps Windmill's status.
	e, _ = open()
	wantStatus(t, runIt(t, e, `{}`, &recordingHooks{haltAfter: "c"}), conformance.RunFailure, "")

	// H2 sees a direct-input step's arguments too, and an identity
	// module's: it launches its own identity job.
	e = mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "echo", "", `"pass_flow_input_directly":true`), `{"id":"b","value":{"type":"identity"}}`)}, &scripts{})
	tr = runIt(t, e, `{"in":1}`, &recordingHooks{rewrite: map[string]string{"a": `{"r":1}`, "b": `{"previous_result":"rewritten"}`}})
	wantStatus(t, tr, conformance.RunSuccess, `"rewritten"`)
	if a := step(t, tr, "a"); !slices.Equal(a.Hooks, []string{"H2"}) || string(a.ArgsBeforeRewrite.Producer()) != `{"in":1}` {
		t.Fatalf("a %+v", a)
	}
	if b := step(t, tr, "b"); !slices.Equal(b.Hooks, []string{"H2"}) || string(b.ArgsBeforeRewrite.Producer()) != `{"previous_result":{"r":1}}` {
		t.Fatalf("b %+v", b)
	}
}

// interceptAll answers H1 for every module it is asked about with its
// module id, and counts the questions.
type interceptAll struct {
	NoHooks
	mu    sync.Mutex
	asked []string
}

func (h *interceptAll) Intercept(_ context.Context, _ Run, s Site, _ jsonval.Value) (StepOutcome, bool, error) {
	h.mu.Lock()
	h.asked = append(h.asked, s.Module)
	h.mu.Unlock()
	return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer(jsonString(s.Module))}, true, nil
}

func (h *interceptAll) RewriteArgs(_ context.Context, _ Run, s Site, args jsonval.Value) (jsonval.Value, error) {
	h.mu.Lock()
	h.asked = append(h.asked, "H2 "+s.Module)
	h.mu.Unlock()
	return args, nil
}

// FX-16, FX-17, FX-18, ADR 0008: an identity module launches its own
// identity job, so H2 and H1 see it like any launching module, its stop
// applying to what H1 answers; a module that skip_if or mock answered is
// replaced by an identity job that neither sees. The identity job of a
// module with any skip_if is classified skipped, even when the predicate
// says no, so its stop_after_if is not evaluated, whether it runs or H1
// answers for it.
func TestEngineIdentityJobs(t *testing.T) {
	stop := `"stop_after_if":{"expr":"true","skip_if_stopped":false}`
	for _, c := range []struct {
		name, module string
		hooks        Hooks
		// skipped and result are module a's; next says whether b ran.
		skipped bool
		result  string
		next    bool
		asked   []string
	}{
		{name: "identity, skip_if false", module: `{"id":"a","value":{"type":"identity"},"skip_if":{"expr":"false"},` + stop + `}`, skipped: true, result: `{"previous_result":7}`, next: true},
		{name: "identity, skip_if false, direct input", module: `{"id":"a","value":{"type":"identity"},"skip_if":{"expr":"false"},"pass_flow_input_directly":true,` + stop + `}`, skipped: true, result: `7`, next: true},
		{name: "identity, stop", module: `{"id":"a","value":{"type":"identity"},` + stop + `}`, result: `{"previous_result":7}`},
		{name: "identity, intercepted", module: `{"id":"a","value":{"type":"identity"},` + stop + `}`, hooks: &interceptAll{}, result: `"a"`, asked: []string{"H2 a", "a"}},
		{name: "identity, skip_if false, intercepted", module: `{"id":"a","value":{"type":"identity"},"skip_if":{"expr":"false"},` + stop + `}`, hooks: &interceptAll{}, skipped: true, result: `"a"`, next: true, asked: []string{"H2 a", "a", "H2 b", "b"}},
		{name: "skipped leaf, not intercepted", module: mod("a", "value", "", `"skip_if":{"expr":"true"},`+stop), hooks: &interceptAll{}, skipped: true, result: `{"previous_result":7}`, next: true, asked: []string{"H2 b", "b"}},
		{name: "mocked leaf, not intercepted", module: mod("a", "value", "", `"mock":{"enabled":true,"return_value":3},`+stop), hooks: &interceptAll{}, result: `3`, asked: []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(c.module, mod("b", "value", inputs("v", static(`"b"`)), ""))}, s)
			tr := runIt(t, e, `{"previous_result":7}`, c.hooks)
			a := step(t, tr, "a")
			if a.Skipped != c.skipped || a.Status != conformance.StepSuccess || a.Module != conformance.ModuleSuccess {
				t.Fatalf("a %+v", a)
			}
			sameJSON(t, "a's result", a.Result, c.result)
			if _, ran := tr.Step(conformance.SitePath{}.Child(conformance.Module("b"))); ran != c.next {
				t.Fatalf("b ran: %v, want %v (status %s, result %s)", ran, c.next, tr.Status, tr.Result)
			}
			if h, ok := c.hooks.(*interceptAll); ok && !slices.Equal(h.asked, c.asked) && !(len(h.asked) == 0 && len(c.asked) == 0) {
				t.Fatalf("hooks asked %q, want %q", h.asked, c.asked)
			}
		})
	}
	// A throwing skip_if on an identity module still fails the flow.
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("z", "value", "", ""), `{"id":"a","value":{"type":"identity"},"skip_if":{"expr":"null.x"}}`)}, &scripts{})
	if tr := runIt(t, e, `{}`, nil); tr.Status != conformance.RunFailure || errorName(tr.Result) != "InternalError" {
		t.Fatalf("status %s, result %s", tr.Status, tr.Result)
	}
}

// ADR 0005 step 3, FX-48, FX-51: the storage boundary checks every
// argument's key as well as its value, after H2; a key jsonb refuses
// fails the push before the step runs. A key jsonb accepts is stored in
// its escaping, and of two keys that store alike the last value stays.
func TestEngineArgumentKeys(t *testing.T) {
	for _, c := range []struct {
		name, args, result string
		calls              int
	}{
		{"NUL key", `{"\u0000":1,"v":7}`, `{"error":{"name":"Anyhow","message":"Error: error returned from database: unsupported Unicode escape sequence"}}`, 0},
		{"unpaired surrogate key", `{"\ud800":1,"v":7}`, `{"error":{"name":"Anyhow","message":"Error: error returned from database: invalid input syntax for type json"}}`, 0},
		{"unusual key", `{"\u00e9\n":1,"v":7}`, `7`, 1},
		{"duplicate keys", `{"v":1,"\u0076":7}`, `7`, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(mod("a", "value", "", ""))}, s)
			tr := runIt(t, e, `{}`, &recordingHooks{rewrite: map[string]string{"a": c.args}})
			sameJSON(t, "the result", tr.Result, c.result)
			if len(s.calls()) != c.calls {
				t.Fatalf("calls %v", s.calls())
			}
			if c.calls == 1 {
				if args := string(s.invs[0].Args.Producer()); c.name == "unusual key" && !strings.Contains(args, `"é\n":1`) || c.name == "duplicate keys" && args != `{"v":7}` {
					t.Fatalf("the stored arguments: %s", args)
				}
			}
		})
	}
}

// waits answers no invocation: it waits for the invocation's context, as
// a step that hangs.
type waits struct{}

func (waits) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	defer inv.Report.Settled(SettleNotStarted)
	<-ctx.Done()
	return StepOutcome{}, context.Cause(ctx)
}

// FX-24, FX-25, WR-37: a leaf's timeout is the module's, else its path
// script's own timeout metadata, else the instance maximum; a request
// strictly below the maximum is job-specific, zero included, which leaves
// the step no time; one at or above it is instance-wide. An identity job
// is never timed.
func TestEngineTimeouts(t *testing.T) {
	const limit = `execution error:\njob process terminated due to timeout after exceeding `
	for _, c := range []struct {
		name, module, result string
	}{
		{"none", mod("a", "value", "", ""), limit + "instance-wide job duration limit"},
		{"null", mod("a", "value", "", `"timeout":{"type":"static","value":null}`), limit + "instance-wide job duration limit"},
		{"zero", mod("a", "value", "", `"timeout":{"type":"static","value":0}`), limit + "job-specific duration limit"},
		{"at the maximum", mod("a", "value", "", `"timeout":{"type":"static","value":1}`), limit + "instance-wide job duration limit"},
		{"script metadata", mod("a", "zero", "", ""), limit + "job-specific duration limit"},
		{"module over metadata", mod("a", "zero", "", `"timeout":{"type":"static","value":5}`), limit + "instance-wide job duration limit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := mustOpen(t, map[string]string{"f/t/main": flowOf(c.module)}, waits{})
			e.cap = 20 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))})
			if err != nil {
				t.Fatal(err)
			}
			wantStatus(t, tr, conformance.RunFailure, `{"error":{"message":"`+c.result+`","name":"ExecutionErr","step_id":"a"}}`)
			if a := step(t, tr, "a"); !a.Abandoned {
				t.Fatalf("a %+v", a)
			}
		})
	}
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(`{"id":"a","value":{"type":"identity"},"timeout":{"type":"static","value":0}}`)}, waits{})
	wantStatus(t, runIt(t, e, `{"x":1}`, nil), conformance.RunSuccess, `{"x":1}`)
}

// The engine refuses at open what later milestones implement, naming the
// milestone, never midway through a run.
func TestEngineRefusesWhatItDoesNotRun(t *testing.T) {
	for name, value := range map[string]string{
		"failure module loop":           `{"modules":[],"failure_module":{"id":"f","value":{"type":"forloopflow","iterator":{"type":"static","value":[]},"skip_failures":true,"modules":[]}}}`,
		"loop and failure module":       `{"modules":[{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[` + mod("a", "echo", "", "") + `]}}],"failure_module":{"id":"f","value":{"type":"script","path":"f/t/echo"}}}`,
		"ai sleep":                      flowOf(mod("a", "echo", "", `"sleep":{"type":"ai"}`)),
		"branch and failure module":     `{"modules":[{"id":"b","value":{"type":"branchone","branches":[],"default":[]}}],"failure_module":{"id":"f","value":{"type":"script","path":"f/t/echo"}}}`,
		"while loop and failure module": `{"modules":[{"id":"w","value":{"type":"whileloopflow","skip_failures":true,"modules":[]}}],"failure_module":{"id":"f","value":{"type":"script","path":"f/t/echo"}}}`,
		"subflow":                       flowOf(`{"id":"s","value":{"type":"flow","path":"f/t/sub","input_transforms":{}}}`),
		"cache_ttl":                     flowOf(mod("a", "echo", "", `"cache_ttl":60`)),
	} {
		_, err := openEngine(t, map[string]string{"f/t/main": value, "f/t/sub": `{"modules":[]}`}, &scripts{})
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// What milestones N2 and NI run opens: retries, sleeps, javascript and
	// negative timeouts (whose failure is a chaining error at run time),
	// a static timeout of null, a failure module's retry, a loop's static
	// timeout and a loop's body's, and all of them in and on containers.
	for name, value := range map[string]string{
		"failure module retry": `{"modules":[],"failure_module":{"id":"f","value":{"type":"script","path":"f/t/echo"},"retry":{"constant":{"attempts":1,"seconds":0}}}}`,
		"retry":                flowOf(mod("a", "echo", "", `"retry":{"constant":{"attempts":1,"seconds":1},"retry_if":{"expr":"true"}}`)),
		"sleep":                flowOf(mod("a", "echo", "", `"sleep":{"type":"static","value":1}`)),
		"js timeout":           flowOf(mod("a", "echo", "", `"timeout":{"type":"javascript","expr":"1"}`)),
		"negative timeout":     flowOf(mod("a", "echo", "", `"timeout":{"type":"static","value":-1}`)),
		"null timeout":         flowOf(mod("a", "echo", "", `"retry":{"constant":{"attempts":0,"seconds":1}},"timeout":{"type":"static","value":null}`)),
		"loop timeouts":        flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[` + mod("a", "echo", "", `"timeout":{"type":"static","value":5}`) + `]},"timeout":{"type":"static","value":3}}`),
		"a sleep after a loop": flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[]}}`, mod("a", "echo", "", `"sleep":{"type":"static","value":1}`)),
		// N2's waits and retries in or on a container, or delaying one
		// (milestone NI), and a squashed loop, which fails as CE fails it
		// (CF-25).
		"loop body retry":            flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[` + mod("a", "echo", "", `"retry":{"constant":{"attempts":1,"seconds":1}}`) + `]}}`),
		"loop body sleep":            flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[` + mod("a", "echo", "", `"sleep":{"type":"static","value":1}`) + `]}}`),
		"loop body js timeout":       flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[` + mod("a", "echo", "", `"timeout":{"type":"javascript","expr":"1"}`) + `]}}`),
		"loop body negative":         flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[` + mod("a", "echo", "", `"timeout":{"type":"static","value":-1}`) + `]}}`),
		"retry on a loop":            flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[]},"retry":{"constant":{"attempts":1,"seconds":1}}}`),
		"sleep on a loop":            flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[]},"sleep":{"type":"static","value":1}}`),
		"a loop's js timeout":        flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[]},"timeout":{"type":"javascript","expr":"1"}}`),
		"a sleep before a loop":      flowOf(mod("a", "echo", "", `"sleep":{"type":"static","value":1}`), `{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[]}}`),
		"branch body sleep":          flowOf(`{"id":"b","value":{"type":"branchall","branches":[{"modules":[` + mod("a", "echo", "", `"sleep":{"type":"static","value":1}`) + `]}]}}`),
		"while body retry":           flowOf(`{"id":"w","value":{"type":"whileloopflow","skip_failures":true,"modules":[` + mod("a", "echo", "", `"retry":{"constant":{"attempts":1,"seconds":1}}`) + `]}}`),
		"retry on a while loop":      flowOf(`{"id":"w","value":{"type":"whileloopflow","skip_failures":true,"modules":[]},"retry":{"constant":{"attempts":1,"seconds":1}}}`),
		"sleep on a branchone":       flowOf(`{"id":"b","value":{"type":"branchone","branches":[],"default":[]},"sleep":{"type":"static","value":1}}`),
		"a branchall's js timeout":   flowOf(`{"id":"b","value":{"type":"branchall","branches":[]},"timeout":{"type":"javascript","expr":"1"}}`),
		"a sleep before a branchone": flowOf(mod("a", "echo", "", `"sleep":{"type":"static","value":1}`), `{"id":"b","value":{"type":"branchone","branches":[],"default":[]}}`),
		"squashed while loop":        flowOf(`{"id":"l","value":{"type":"whileloopflow","skip_failures":true,"squash":true,"modules":[]}}`),
		"squashed loop":              flowOf(`{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"squash":true,"modules":[]}}`),
	} {
		if _, err := openEngine(t, map[string]string{"f/t/main": value}, &scripts{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// stop_after_all_iters_if runs only on an active loop or branchall
	// (CF-13, FX-2): on a step or a branchone it is never evaluated.
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		mod("a", "value", inputs("v", static("1")), `"stop_after_all_iters_if":{"expr":"nope","skip_if_stopped":false}`),
		`{"id":"b","value":{"type":"branchone","branches":[],"default":[`+mod("c", "value", inputs("v", static("2")), "")+`]},"stop_after_all_iters_if":{"expr":"nope","skip_if_stopped":false}}`,
	)}, &scripts{})
	wantStatus(t, runIt(t, e, `{}`, nil), conformance.RunSuccess, `2`)
}

// One engine serves concurrent runs (run with -race).
func TestEngineConcurrentRuns(t *testing.T) {
	e := mustOpen(t, map[string]string{"f/t/main": flowOf(
		mod("a", "value", inputs("v", js("flow_input.n")), `"skip_if":{"expr":"flow_input.n < 0"}`),
		mod("b", "value", inputs("v", js("results.a * 2")), `"stop_after_if":{"expr":"result > 1000","skip_if_stopped":true}`),
	)}, &scripts{})
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for n := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr, err := e.Run(context.Background(), RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(fmt.Sprintf(`{"n":%d}`, n)))})
			if err == nil && (tr.Status != conformance.RunSuccess || string(tr.Result.Producer()) != fmt.Sprint(2*n)) {
				err = fmt.Errorf("run %d: status %s, result %s", n, tr.Status, tr.Result)
			}
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

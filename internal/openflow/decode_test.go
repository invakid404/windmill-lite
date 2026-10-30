package openflow

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func decodeOK(t *testing.T, in string) *FlowValue {
	t.Helper()
	v, err := DecodeFlowValue([]byte(in))
	if err != nil {
		t.Fatalf("DecodeFlowValue(%s): %v", in, err)
	}
	return v
}

func decodeErr(t *testing.T, in string) *Error {
	t.Helper()
	_, err := DecodeFlowValue([]byte(in))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("DecodeFlowValue(%s) = %v, want an *Error", in, err)
	}
	return e
}

// identityWith is a one-module flow whose envelope carries extra members.
func identityWith(members string) string {
	return `{"modules":[{"id":"a","value":{"type":"identity"}` + members + `}]}`
}

// valueWith is a one-module flow with the given module value.
func valueWith(value string) string {
	return `{"modules":[{"id":"a","value":` + value + `}]}`
}

type rejectCase struct {
	name, in string
	kind     ErrorKind
	contains string
}

func checkRejects(t *testing.T, cases []rejectCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := decodeErr(t, c.in)
			if e.Kind != c.kind || !strings.Contains(e.Error(), c.contains) {
				t.Fatalf("got kind %d %q; want kind %d containing %q", e.Kind, e.Error(), c.kind, c.contains)
			}
		})
	}
}

// Integers are classified as serde_json does and range-checked per field.
func TestIntegerFields(t *testing.T) {
	accept := []string{
		identityWith(`,"priority":32767`),
		identityWith(`,"priority":-32768`),
		identityWith(`,"cache_ttl":0`),
		identityWith(`,"cache_ttl":4294967295`),
		identityWith(`,"retry":{"exponential":{"attempts":1,"seconds":65535,"random_factor":-128}}`),
		identityWith(`,"retry":{"exponential":{"attempts":1,"seconds":1,"random_factor":127}}`),
		identityWith(`,"timeout":-2147483648`),
		identityWith(`,"debouncing":{"debounce_delay_s":9223372036854775807}`),
	}
	for _, in := range accept {
		decodeOK(t, in)
	}
	checkRejects(t, []rejectCase{
		{"i16 above", identityWith(`,"priority":32768`), ErrFlowValue, "invalid value: integer `32768`, expected i16"},
		{"i16 below", identityWith(`,"priority":-32769`), ErrFlowValue, "expected i16"},
		{"u32 negative", identityWith(`,"cache_ttl":-1`), ErrFlowValue, "invalid value: integer `-1`, expected u32"},
		{"u32 above", identityWith(`,"cache_ttl":4294967296`), ErrFlowValue, "expected u32"},
		{"i8 above", identityWith(`,"retry":{"exponential":{"random_factor":128}}`), ErrFlowValue, "expected i8"},
		{"fraction", identityWith(`,"cache_ttl":30.0`), ErrFlowValue, "invalid type: floating point `30.0`, expected u32"},
		{"exponent", identityWith(`,"cache_ttl":3e1`), ErrFlowValue, "floating point"},
		{"negative zero", identityWith(`,"timeout":-0`), ErrFlowValue, "floating point `-0`"},
		{"beyond u64", identityWith(`,"cache_ttl":18446744073709551616`), ErrFlowValue, "floating point"},
		{"string", identityWith(`,"cache_ttl":"30"`), ErrFlowValue, `invalid type: string "30", expected u32`},
		{"i32 in a value", valueWith(`{"type":"rawscript","content":"","language":"bun","concurrent_limit":2147483648}`), ErrModule, "expected i32"},
	})
}

// Optional fields treat null as absent; fields with a plain default reject
// null (OF-V1).
func TestNullAndAbsence(t *testing.T) {
	v := decodeOK(t, identityWith(`,"summary":null,"retry":null,"sleep":null,"mock":null,"continue_on_error":null,"timeout":null,"skip_if":null,"suspend":null,"debouncing":null`))
	m := v.Modules[0]
	if m.Summary != nil || m.Retry != nil || m.Sleep != nil || m.Mock != nil || m.ContinueOnError != nil || m.Timeout != nil || m.SkipIf != nil || m.Suspend != nil || m.Debouncing != nil {
		t.Fatalf("null optional fields were not absent: %+v", m)
	}

	// In a module value every recognised field is optional, so null is
	// absent and each kind checks its required fields afterwards.
	v = decodeOK(t, valueWith(`{"type":"script","path":"f/x","input_transforms":null,"hash":null,"lock":null}`))
	if s := v.Modules[0].Value.(*PathScript); s.InputTransforms == nil || len(s.InputTransforms) != 0 || s.Hash != nil {
		t.Fatalf("got %+v", s)
	}
	v = decodeOK(t, valueWith(`{"type":"whileloopflow","modules":[],"skip_failures":null,"parallel":null}`))
	if w := v.Modules[0].Value.(*WhileLoop); w.SkipFailures {
		t.Fatal("while-loop skip_failures: null must take its false default")
	}
	v = decodeOK(t, valueWith(`{"type":"branchall","branches":[],"parallel":null}`))
	if b := v.Modules[0].Value.(*BranchAll); !b.Parallel {
		t.Fatal("branchall parallel: null must take its true default")
	}

	checkRejects(t, []rejectCase{
		{"module id", `{"modules":[{"id":null,"value":{"type":"identity"}}]}`, ErrFlowValue, "invalid type: null, expected a string"},
		{"same_worker", `{"modules":[],"same_worker":null}`, ErrFlowValue, "invalid type: null, expected a boolean"},
		{"preserve_step_tags", `{"modules":[],"preserve_step_tags":null}`, ErrFlowValue, "expected a boolean"},
		{"branch expr", valueWith(`{"type":"branchone","branches":[{"expr":null,"modules":[]}],"default":[]}`), ErrModule, "expected a string"},
		{"branch parallel", valueWith(`{"type":"branchall","branches":[{"modules":[],"parallel":null}]}`), ErrModule, "expected a boolean"},
		{"retry attempts", identityWith(`,"retry":{"constant":{"attempts":null}}`), ErrFlowValue, "invalid type: null, expected u32"},
		{"mock enabled", identityWith(`,"mock":{"enabled":null}`), ErrFlowValue, "expected a boolean"},
		{"value type", valueWith(`{"type":null}`), ErrModule, "missing field `type`"},
		{"loop modules", valueWith(`{"type":"forloopflow","iterator":{"type":"static","value":[]},"modules":null}`), ErrModule, "missing field `modules`"},
		{"rawscript language", valueWith(`{"type":"rawscript","content":"","language":null}`), ErrModule, "missing field `language`"},
		{"transform type", identityWith(`,"sleep":{"type":null}`), ErrFlowValue, "invalid type: null, expected a string"},
	})
}

func TestRequiredFields(t *testing.T) {
	checkRejects(t, []rejectCase{
		{"flow modules", `{}`, ErrFlowValue, "missing field `modules`"},
		{"envelope value", `{"modules":[{"id":"a"}]}`, ErrFlowValue, "missing field `value`"},
		{"stop expr", identityWith(`,"stop_after_if":{"skip_if_stopped":true}`), ErrFlowValue, "missing field `expr`"},
		{"skip_if expr", identityWith(`,"skip_if":{}`), ErrFlowValue, "missing field `expr`"},
		{"retry_if expr", identityWith(`,"retry":{"retry_if":{}}`), ErrFlowValue, "missing field `expr`"},
		{"mock enabled", identityWith(`,"mock":{"return_value":1}`), ErrFlowValue, "missing field `enabled`"},
		{"transform type", identityWith(`,"sleep":{"value":1}`), ErrFlowValue, "missing field `type`"},
		{"value type", valueWith(`{}`), ErrModule, "missing field `type`"},
		{"rawscript content", valueWith(`{"type":"rawscript","language":"bun"}`), ErrModule, "missing field `content`"},
		{"script path", valueWith(`{"type":"script"}`), ErrModule, "missing field `path`"},
		{"flow path", valueWith(`{"type":"flow"}`), ErrModule, "missing field `path`"},
		{"loop iterator", valueWith(`{"type":"forloopflow","modules":[]}`), ErrModule, "missing field `iterator`"},
		{"while modules", valueWith(`{"type":"whileloopflow"}`), ErrModule, "missing field `modules`"},
		{"branchone default", valueWith(`{"type":"branchone","branches":[]}`), ErrModule, "missing field `default`"},
		{"branchall branches", valueWith(`{"type":"branchall"}`), ErrModule, "missing field `branches`"},
		{"branch modules", valueWith(`{"type":"branchall","branches":[{}]}`), ErrModule, "missing field `modules`"},
		{"aiagent tools", valueWith(`{"type":"aiagent"}`), ErrModule, "missing field `tools`"},
		{"value not an object", valueWith(`5`), ErrModule, "invalid type: integer `5`"},
		{"flow not an object", `[]`, ErrFlowValue, "invalid type: sequence, expected struct FlowValue"},
	})
}

func TestDuplicateFields(t *testing.T) {
	checkRejects(t, []rejectCase{
		{"flow field", `{"modules":[],"modules":[]}`, ErrFlowValue, "duplicate field `modules`"},
		{"envelope field", `{"modules":[{"id":"a","id":"b","value":{"type":"identity"}}]}`, ErrFlowValue, "duplicate field `id`"},
		{"alias and name", valueWith(`{"type":"script","path":"f/x","input_transforms":{},"input_transform":{}}`), ErrModule, "duplicate field `input_transforms`"},
		{"debounce alias", `{"modules":[],"debounce_key":"a","custom_debounce_key":"b"}`, ErrFlowValue, "duplicate field `debounce_key`"},
		{"transform type", identityWith(`,"sleep":{"type":"static","type":"static"}`), ErrFlowValue, "duplicate field `type`"},
		{"value type", valueWith(`{"type":"identity","type":"identity"}`), ErrModule, "duplicate field `type`"},
	})

	// An unknown name may repeat, and a repeated map key keeps its first
	// position and its last value.
	v := decodeOK(t, `{"modules":[{"id":"a","x":1,"x":2,"value":{"type":"script","path":"f/x","input_transforms":{"p":{"type":"static","value":1},"q":{"type":"static","value":2},"p":{"type":"static","value":3}}}}],"flow_env":{"e":1,"f":2,"e":3}}`)
	ts := v.Modules[0].Value.(*PathScript).InputTransforms
	if len(ts) != 2 || ts[0].Name != "p" || string(ts[0].Transform.Value) != "3" || ts[1].Name != "q" {
		t.Fatalf("transforms = %+v", ts)
	}
	if env := v.FlowEnv; len(env) != 2 || env[0].Name != "e" || string(env[0].Value) != "3" {
		t.Fatalf("flow_env = %+v", env)
	}
}

func TestNamesAreCaseSensitive(t *testing.T) {
	checkRejects(t, []rejectCase{
		{"field", `{"Modules":[]}`, ErrFlowValue, "missing field `modules`"},
		{"kind", valueWith(`{"type":"Identity"}`), ErrModule, "unknown variant `Identity`"},
		{"language", valueWith(`{"type":"rawscript","content":"","language":"Bun"}`), ErrModule, "unknown variant `Bun`"},
		{"transform type", identityWith(`,"sleep":{"type":"Static"}`), ErrFlowValue, "got value: Static for field `type`"},
	})
}

// Recognised fields are type-checked on every kind, even where the kind
// drops them (OF-V1).
func TestCrossKindTypeChecks(t *testing.T) {
	checkRejects(t, []rejectCase{
		{"hash on identity", valueWith(`{"type":"identity","hash":"1"}`), ErrModule, "Odd number of digits"},
		{"parallelism on rawscript", valueWith(`{"type":"rawscript","content":"","language":"bun","parallelism":70000}`), ErrModule, "expected u16"},
		{"iterator on script", valueWith(`{"type":"script","path":"f/x","iterator":{"type":"jq"}}`), ErrModule, "got value: jq"},
		{"branches on identity", valueWith(`{"type":"identity","branches":[{}]}`), ErrModule, "missing field `modules`"},
		{"modules on identity", valueWith(`{"type":"identity","modules":[{"id":"b"}]}`), ErrModule, "missing field `value`"},
		{"tools on identity", valueWith(`{"type":"identity","tools":[{"value":{}}]}`), ErrModule, "missing field `id`"},
		{"assets on script", valueWith(`{"type":"script","path":"f/x","assets":{}}`), ErrModule, "expected a sequence"},
		{"aiagent parallel", valueWith(`{"type":"aiagent","tools":[],"parallel":1}`), ErrModule, "expected a boolean"},
	})
	// The body envelopes of a non-container are decoded but never traversed,
	// so their values are not checked.
	decodeOK(t, valueWith(`{"type":"identity","modules":[{"id":"b","value":{"type":"nonsense"}}]}`))
}

func TestInputTransforms(t *testing.T) {
	v := decodeOK(t, valueWith(`{"type":"script","path":"f/x","input_transforms":{
		"s":{"type":"static","value": { "b" : [1.0, 1e2], "a": "\u00e9" } ,"expr":"unused"},
		"j":{"expr":"flow_input.x","type":"javascript","value":{"unused":[1]}},
		"n":{"type":"javascript","expr":null},
		"ai":{"type":"ai","value":1,"expr":"unused"}}}`))
	ts := v.Modules[0].Value.(*PathScript).InputTransforms
	// Static values are kept verbatim (JE-40). Every recognised member is
	// type-checked, and the type then selects the payload.
	if s, _ := ts.Get("s"); s.Type != Static || s.Expr != "" || string(s.Value) != `{ "b" : [1.0, 1e2], "a": "\u00e9" }` {
		t.Errorf("static = %+v (%s)", s, s.Value)
	}
	if j, _ := ts.Get("j"); j.Type != JavaScript || j.Expr != "flow_input.x" || j.Value != nil {
		t.Errorf("javascript = %+v", j)
	}
	if n, _ := ts.Get("n"); n.Type != JavaScript || n.Expr != "" {
		t.Errorf("javascript with null expr = %+v", n)
	}
	if a, _ := ts.Get("ai"); a.Type != AI || a.Value != nil || a.Expr != "" {
		t.Errorf("ai = %+v", a)
	}
	checkRejects(t, []rejectCase{
		// OF-10: recognised but unused members are still type-checked.
		{"unused expr under static, envelope slot", identityWith(`,"sleep":{"type":"static","value":1,"expr":5}`), ErrFlowValue, "Invalid flow value: invalid type: integer `5`, expected a string"},
		{"unused expr under ai, envelope slot", identityWith(`,"sleep":{"type":"ai","expr":5}`), ErrFlowValue, "Invalid flow value: invalid type: integer `5`, expected a string"},
		{"unused expr under static, step input", valueWith(`{"type":"script","path":"f/x","input_transforms":{"x":{"type":"static","expr":false}}}`), ErrModule, "Module 'a': invalid type: boolean `false`, expected a string"},
		{"unused expr under static, iterator", valueWith(`{"type":"forloopflow","iterator":{"type":"static","value":[],"expr":[]},"modules":[]}`), ErrModule, "Module 'a': invalid type: sequence, expected a string"},
		{"unused expr, approval groups", identityWith(`,"suspend":{"user_groups_required":{"type":"static","expr":1}}`), ErrFlowValue, "expected a string"},
		{"expr not a string", valueWith(`{"type":"script","path":"f/x","input_transforms":{"x":{"type":"javascript","expr":1}}}`), ErrModule, "expected a string"},
		{"duplicate unused value", identityWith(`,"sleep":{"type":"javascript","expr":"1","value":1,"value":2}`), ErrFlowValue, "duplicate field `value`"},
		{"duplicate unused expr", identityWith(`,"sleep":{"type":"static","expr":"a","expr":"b"}`), ErrFlowValue, "duplicate field `expr`"},
		{"member checked before type", identityWith(`,"sleep":{"type":"jq","expr":5}`), ErrFlowValue, "invalid type: integer `5`, expected a string"},
		{"not an object", valueWith(`{"type":"script","path":"f/x","input_transforms":{"x":5}}`), ErrModule, "invalid type: integer `5`"},
		{"map not an object", valueWith(`{"type":"script","path":"f/x","input_transforms":[]}`), ErrModule, "expected a map"},
	})
}

// Decoded values own their bytes: no value shares storage with another
// value, with a later decode, or with the caller's input.
func TestDecodedValuesOwnTheirBytes(t *testing.T) {
	in := `{"modules":[{"id":"a","value":{"type":"identity"},"sleep":{"type":"static"},"timeout":{"type":"static"}}],"flow_env":{"x":123,"y":4}}`
	input := []byte(in)
	first, err := DecodeFlowValue(input)
	if err != nil {
		t.Fatal(err)
	}
	m := &first.Modules[0]
	// Defaults are not shared (OF-10/a, OF-V2/e).
	copy(m.Sleep.Value, "true")
	if string(m.Timeout.Value) != "null" {
		t.Errorf("timeout changed to %s with the sleep's default", m.Timeout.Value)
	}
	if again := decodeOK(t, in); string(again.Modules[0].Sleep.Value) != "null" {
		t.Errorf("a later decode returned %s for a default null", again.Modules[0].Sleep.Value)
	}
	// Raw values are copies, so overwriting the input changes nothing.
	at := strings.Index(string(input), "123")
	copy(input[at:], "456")
	if got := string(first.FlowEnv[0].Value); got != "123" {
		t.Errorf("flow_env value follows the caller's buffer: %s", got)
	}
	// Each copy's capacity ends at its length.
	x := first.FlowEnv[0].Value
	_ = append(x, '9')
	if got := string(first.FlowEnv[1].Value); got != "4" {
		t.Errorf("appending to one value overwrote another: %s", got)
	}
	for _, e := range first.FlowEnv {
		if cap(e.Value) != len(e.Value) {
			t.Errorf("%s: capacity %d, length %d", e.Name, cap(e.Value), len(e.Value))
		}
	}
}

// TestAIParallelism decodes an ai parallelism, which the parallelism slot
// accepts and evaluates to 0 (CF-7/e), while the timeout slot refuses the
// ai type (TestRestrictedTransforms).
func TestAIParallelism(t *testing.T) {
	// spec: CF-7/e
	v := decodeOK(t, `{"modules":[{"id":"l","value":{"type":"forloopflow","iterator":{"type":"javascript","expr":"[1]"},"modules":[],"parallel":true,"parallelism":{"type":"ai"}}}]}`)
	l, ok := v.Modules[0].Value.(*ForLoop)
	if !ok || l.Parallelism == nil || l.Parallelism.Type != AI {
		t.Fatalf("parallelism = %+v", v.Modules[0].Value)
	}
}

func TestRestrictedTransforms(t *testing.T) {
	v := decodeOK(t, identityWith(`,"timeout":{"type":"javascript","expr":null,"value":null}`))
	if tt := v.Modules[0].Timeout; tt.Type != JavaScript || tt.Expr != "" {
		t.Fatalf("timeout = %+v", tt)
	}
	checkRejects(t, []rejectCase{
		{"expr checked under static", identityWith(`,"timeout":{"type":"static","value":1,"expr":2}`), ErrFlowValue, "expected a string"},
		{"value checked under javascript", identityWith(`,"timeout":{"type":"javascript","expr":"1","value":1.5}`), ErrFlowValue, "expected i32"},
		{"ai is not allowed", identityWith(`,"timeout":{"type":"ai"}`), ErrFlowValue, "got value: ai for field `type`"},
		{"missing type", identityWith(`,"timeout":{"value":1}`), ErrFlowValue, "missing field `type`"},
		{"bare string", identityWith(`,"timeout":"30"`), ErrFlowValue, `invalid type: string "30", expected i32 or an input transform object`},
		{"bare float", identityWith(`,"timeout":1.5`), ErrFlowValue, "floating point"},
	})
}

// The outer decode covers every top-level envelope before any module value
// is decoded (OF-2), so an envelope failure in a later module wins over a
// value failure in an earlier one.
func TestStageOrder(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"outer before values",
			`{"modules":[{"id":"a","value":{"type":"foo"}},{"id":"b","value":{"type":"identity"},"sleep":5}]}`,
			"Invalid flow value: invalid type: integer `5`"},
		{"main list in order",
			`{"modules":[{"id":"a","value":{"type":"foo"}},{"id":"b","value":{"type":"bar"}}]}`,
			"Module 'a':"},
		{"body after its container",
			`{"modules":[{"id":"l","value":{"type":"forloopflow","iterator":{"type":"static","value":[]},"modules":[{"id":"b","value":{"type":"foo"}}]}},{"id":"c","value":{"type":"bar"}}]}`,
			"Module 'b':"},
		{"body envelope only when the container is reached",
			`{"modules":[{"id":"a","value":{"type":"foo"}},{"id":"l","value":{"type":"whileloopflow","modules":[{"id":"b"}]}}]}`,
			"Module 'a':"},
		{"main list before failure module",
			`{"modules":[{"id":"a","value":{"type":"foo"}}],"failure_module":{"id":"f","value":{"type":"bar"}}}`,
			"Module 'a':"},
		{"failure before preprocessor",
			`{"modules":[],"failure_module":{"id":"f","value":{"type":"foo"}},"preprocessor_module":{"id":"p","value":{"type":"bar"}}}`,
			"Module 'f':"},
		{"preprocessor checked",
			`{"modules":[],"preprocessor_module":{"id":"p","value":{"type":"script","path":"x"}}}`,
			"step 'p' references 'x'"},
		{"branches before default",
			`{"modules":[{"id":"o","value":{"type":"branchone","branches":[{"modules":[{"id":"b","value":{"type":"foo"}}]}],"default":[{"id":"d","value":{"type":"bar"}}]}}]}`,
			"Module 'b':"},
		{"value before retry",
			`{"modules":[{"id":"a","value":{"type":"foo"},"retry":{"exponential":{"attempts":1}}}]}`,
			"Module 'a': unknown variant"},
		{"retry before path",
			`{"modules":[{"id":"a","value":{"type":"script","path":"x"},"retry":{"exponential":{"attempts":1}}}]}`,
			"Module 'a': Exponential backoff base"},
		{"container checks before its body",
			`{"modules":[{"id":"l","value":{"type":"whileloopflow","modules":[{"id":"b","value":{"type":"foo"}}]},"retry":{"exponential":{"attempts":1}}}]}`,
			"Module 'l': Exponential backoff base"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if e := decodeErr(t, c.in); !strings.Contains(e.Error(), c.want) {
				t.Fatalf("got %q, want it to contain %q", e.Error(), c.want)
			}
		})
	}
}

func TestErrorPointers(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"modules":[{"id":"a","value":{"type":"identity"},"priority":"x"}]}`, "/modules/0/priority"},
		{`{"modules":[{"id":"l","value":{"type":"whileloopflow","modules":[{"id":"b","value":{"type":"identity","hash":5}}]}}]}`, "/modules/0/value/modules/0/value/hash"},
		{`{"modules":[{"id":"a","value":{"type":"script","path":"x"}}]}`, "/modules/0/value/path"},
		{`{"modules":[],"failure_module":{"id":"f","value":{"type":"identity"},"retry":{"exponential":{"attempts":1}}}}`, "/failure_module/retry/exponential"},
	} {
		if e := decodeErr(t, c.in); e.Pointer != c.want {
			t.Errorf("pointer = %q, want %q (%v)", e.Pointer, c.want, e)
		}
	}
}

func TestServerChecks(t *testing.T) {
	for _, path := range []string{"u/x/s", "f/x/s", "g/x/s", "hub/123/s", ""} {
		decodeOK(t, valueWith(`{"type":"script","path":"`+path+`"}`))
		decodeOK(t, valueWith(`{"type":"flow","path":"`+path+`"}`))
	}
	for _, path := range []string{"f", "x/f/s", "./f/s", "/f/s", "F/x/s", "hub"} {
		e := decodeErr(t, valueWith(`{"type":"flow","path":"`+path+`"}`))
		if e.Kind != ErrStepPath || e.Module != "a" || !strings.Contains(e.Error(), "'"+path+"'") {
			t.Errorf("path %q: %v", path, e)
		}
	}
	// A zero base is rejected only with attempts.
	decodeOK(t, identityWith(`,"retry":{"exponential":{"attempts":0,"seconds":0}}`))
	decodeOK(t, identityWith(`,"retry":{"constant":{"attempts":3,"seconds":0}}`))
}

func TestRetry(t *testing.T) {
	v := decodeOK(t, identityWith(`,"retry":{"constant":{"attempts":4294967295},"exponential":{"attempts":1,"seconds":2},"retry_if":{"expr":"true"}}`))
	r := v.Modules[0].Retry
	if r.MaxRetries() != ^uint32(0) {
		t.Errorf("MaxRetries = %d, want saturation", r.MaxRetries())
	}
	if x := r.Exponential; x.Multiplier != 1 || x.Seconds != 2 || x.RandomFactor != nil {
		t.Errorf("exponential = %+v", x)
	}
	if r.RetryIf.Expr != "true" {
		t.Errorf("retry_if = %+v", r.RetryIf)
	}
	if got := (&Retry{Constant: &ConstantRetry{Attempts: 2}, Exponential: &ExponentialRetry{Attempts: 3}}).MaxRetries(); got != 5 {
		t.Errorf("MaxRetries = %d, want 5", got)
	}
	if got := (&Retry{}).MaxRetries(); got != 0 {
		t.Errorf("MaxRetries = %d, want 0", got)
	}
}

func TestKinds(t *testing.T) {
	v := decodeOK(t, `{"modules":[
		{"id":"r","value":{"type":"rawscript","content":"c","language":"python3","input_transform":{"x":{"type":"static","value":1}},"lock":"","path":"f/x/r","tag":"t","is_trigger":false,"assets":[{"path":"p","kind":"resource"}],"custom_concurrency_key":"k","concurrent_limit":2,"concurrency_time_window_s":60}},
		{"id":"s","value":{"type":"script","path":"f/x/s","hash":"FFFFFFFFFFFFFFFF","tag_override":"t","pass_flow_input_directly":true}},
		{"id":"f","value":{"type":"flow","path":"f/x/f"}},
		{"id":"l","value":{"type":"forloopflow","iterator":{"type":"javascript","expr":"[1]"},"modules":[],"squash":true}},
		{"id":"w","value":{"type":"whileloopflow","modules":[],"skip_failures":true}},
		{"id":"o","value":{"type":"branchone","branches":[{"summary":"s","expr":"true","modules":[]}],"default":[]}},
		{"id":"b","value":{"type":"branchall","branches":[{"modules":[],"skip_failure":false,"parallel":false}],"parallel":false}},
		{"id":"i","value":{"type":"identity","flow":true}},
		{"id":"g","value":{"type":"aiagent","tools":[
			{"id":"t1","value":{"tool_type":"flowmodule","type":"script","path":"f/x/s"}},
			{"id":"t2","value":{"type":"identity"}},
			{"id":"t3","value":{"tool_type":"mcp","resource_path":"u/x/mcp","include_tools":["a"]}},
			{"id":"t4","value":{"tool_type":"websearch"}}],"omit_output_from_conversation":true}},
		{"id":"x","value":{"type":"flowscript","id":42}}]}`)
	want := []Kind{KindRawScript, KindScript, KindFlow, KindForLoop, KindWhileLoop, KindBranchOne, KindBranchAll, KindIdentity, KindAIAgent, KindFlowScript}
	for i, k := range want {
		if got := v.Modules[i].Value.Kind(); got != k {
			t.Errorf("module %d: kind %s, want %s", i, got, k)
		}
	}
	r := v.Modules[0].Value.(*RawScript)
	if r.Language != LangPython3 || *r.Lock != "" || *r.Path != "f/x/r" || len(r.Assets) != 1 || *r.ConcurrentLimit != 2 || len(r.InputTransforms) != 1 {
		t.Errorf("rawscript = %+v", r)
	}
	if s := v.Modules[1].Value.(*PathScript); s.Hash.String() != "ffffffffffffffff" || int64(*s.Hash) != -1 || !*s.PassFlowInputDirectly {
		t.Errorf("script = %+v", s)
	}
	if l := v.Modules[3].Value.(*ForLoop); l.Parallel || !l.EffectiveSkipFailures() || l.SkipFailures != nil || !*l.Squash {
		t.Errorf("for-loop = %+v", l)
	}
	if b := v.Modules[6].Value.(*BranchAll); b.Parallel || b.Branches[0].EffectiveSkipFailure() || b.Branches[0].Parallel {
		t.Errorf("branchall = %+v", b)
	}
	if o := v.Modules[5].Value.(*BranchOne); o.Branches[0].SkipFailure != nil || !o.Branches[0].EffectiveSkipFailure() || !o.Branches[0].Parallel {
		t.Errorf("branchone = %+v", o)
	}
	g := v.Modules[8].Value.(*AIAgent)
	if !g.OmitOutputFromConversation || len(g.Tools) != 4 {
		t.Fatalf("aiagent = %+v", g)
	}
	if mt := g.Tools[0].Value.(*ModuleTool); mt.Value.Kind() != KindScript {
		t.Errorf("tool 1 = %+v", mt.Value)
	}
	if mt := g.Tools[1].Value.(*ModuleTool); mt.Value.Kind() != KindIdentity {
		t.Errorf("legacy tool = %+v", mt.Value)
	}
	if mcp := g.Tools[2].Value.(*MCPTool); mcp.ResourcePath != "u/x/mcp" || len(mcp.IncludeTools) != 1 || mcp.ExcludeTools != nil {
		t.Errorf("mcp tool = %+v", mcp)
	}
	if _, ok := g.Tools[3].Value.(*WebsearchTool); !ok {
		t.Errorf("websearch tool = %+v", g.Tools[3].Value)
	}
	if fs := v.Modules[9].Value.(*FlowScript); string(fs.Raw) != `{"type":"flowscript","id":42}` {
		t.Errorf("flowscript = %s", fs.Raw)
	}

	checkRejects(t, []rejectCase{
		{"tool module value", valueWith(`{"type":"aiagent","tools":[{"id":"t","value":{"tool_type":"flowmodule","type":"foo"}}]}`), ErrModule, "Module 't': unknown variant `foo`"},
		{"tool path", valueWith(`{"type":"aiagent","tools":[{"id":"t","value":{"type":"script","path":"/x"}}]}`), ErrStepPath, "step 't' references '/x'"},
		{"tool type", valueWith(`{"type":"aiagent","tools":[{"id":"t","value":{"tool_type":"x"}}]}`), ErrModule, "unknown variant `x`, expected one of `flowmodule`, `mcp`, `websearch`"},
		{"mcp path", valueWith(`{"type":"aiagent","tools":[{"id":"t","value":{"tool_type":"mcp"}}]}`), ErrModule, "missing field `resource_path`"},
	})
}

func TestLanguages(t *testing.T) {
	if len(Languages) != 24 {
		t.Fatalf("%d languages, OF-13 lists 24", len(Languages))
	}
	for _, l := range Languages {
		v := decodeOK(t, valueWith(fmt.Sprintf(`{"type":"rawscript","content":"","language":%q}`, l)))
		if got := v.Modules[0].Value.(*RawScript).Language; got != l {
			t.Errorf("language %s decoded as %s", l, got)
		}
	}
}

func TestParseScriptHash(t *testing.T) {
	for _, c := range []struct{ in, want, err string }{
		{"0000000000000001", "0000000000000001", ""},
		{"0000000000000001ff", "0000000000000001", ""},
		{"00000000000000AB", "00000000000000ab", ""},
		{"8000000000000000", "8000000000000000", ""},
		{"1", "", "Odd number of digits"},
		{"000000000000000g", "", "Invalid character 'g' at position 15"},
		{"00000001", "", "hex string did not decode to an u64: 00000001"},
		{"", "", "hex string did not decode to an u64: "},
	} {
		h, err := ParseScriptHash(c.in)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("ParseScriptHash(%q) error %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || h.String() != c.want {
			t.Errorf("ParseScriptHash(%q) = %s, %v; want %s", c.in, h, err, c.want)
		}
	}
}

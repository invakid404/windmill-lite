package openflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const nestedFlow = `{
	"modules": [
		{"id": "a", "value": {"type": "identity"}},
		{"id": "l", "value": {"type": "forloopflow", "iterator": {"type": "static", "value": []}, "modules": [
			{"id": "l1", "value": {"type": "whileloopflow", "modules": [{"id": "w1", "value": {"type": "identity"}}]}},
			{"id": "l2", "value": {"type": "identity"}}]}},
		{"id": "o", "value": {"type": "branchone", "default": [{"id": "d1", "value": {"type": "identity"}}],
			"branches": [{"modules": [{"id": "o1", "value": {"type": "identity"}}]}, {"modules": [{"id": "o2", "value": {"type": "identity"}}]}]}},
		{"id": "b", "value": {"type": "branchall", "branches": [{"modules": [{"id": "b1", "value": {"type": "identity"}}]}, {"modules": [{"id": "b2", "value": {"type": "identity"}}]}]}},
		{"id": "g", "value": {"type": "aiagent", "tools": [{"id": "t", "value": {"type": "forloopflow", "iterator": {"type": "static", "value": []}, "modules": [{"id": "t1", "value": {"type": "identity"}}]}}]}}
	],
	"preprocessor_module": {"id": "p", "value": {"type": "identity"}},
	"failure_module": {"id": "f", "value": {"type": "identity"}}
}`

// The traversal order of OF-2 and SY-7.
func TestAllModulesOrder(t *testing.T) {
	v := decodeOK(t, nestedFlow)
	var ids []string
	for m := range v.AllModules() {
		ids = append(ids, m.ID)
	}
	if got, want := strings.Join(ids, " "), "a l l1 w1 l2 o o1 o2 d1 b b1 b2 g t1 f p"; got != want {
		t.Fatalf("order %q, want %q", got, want)
	}
	n := 0
	for range v.AllModules() {
		if n++; n == 3 {
			break
		}
	}
	if n != 3 {
		t.Fatalf("early stop visited %d", n)
	}
}

// Deviation D6: a module without an id gets `wl` and a hash of its position.
func TestDerivedIDs(t *testing.T) {
	in := `{"modules":[{"value":{"type":"identity"}},{"value":{"type":"whileloopflow","modules":[{"value":{"type":"identity"}}]}},{"id":"","value":{"type":"identity"}}],"failure_module":{"value":{"type":"identity"}}}`
	v := decodeOK(t, in)
	ids := map[string]bool{}
	pattern := regexp.MustCompile(`^wl[0-9a-f]{8}$`)
	for _, m := range []*Module{&v.Modules[0], &v.Modules[1], &v.Modules[1].Value.(*WhileLoop).Modules[0], v.FailureModule} {
		if !m.IDDerived || !pattern.MatchString(m.ID) {
			t.Errorf("module %+v: want a derived id", m)
		}
		ids[m.ID] = true
	}
	if len(ids) != 4 {
		t.Errorf("derived ids collide: %v", ids)
	}
	if m := v.Modules[2]; m.IDDerived || m.ID != "" {
		t.Errorf("an explicit empty id must be kept: %+v", m)
	}
	if got := v.Modules[0].ID; got != derivedID("/modules/0") {
		t.Errorf("id %s is not the hash of /modules/0", got)
	}
	if again := decodeOK(t, in); again.Modules[1].Value.(*WhileLoop).Modules[0].ID != v.Modules[1].Value.(*WhileLoop).Modules[0].ID {
		t.Error("derived ids are not deterministic")
	}
	// Positions are relative to the flow value, so a flow document derives
	// the same ids.
	f, err := DecodeFlow([]byte(`{"summary":"s","value":` + in + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Value.Modules[0].ID != v.Modules[0].ID || f.Value.FailureModule.ID != v.FailureModule.ID {
		t.Error("DecodeFlow derived different ids")
	}
	// The derived id names the module in errors.
	e := decodeErr(t, `{"modules":[{"value":{"type":"foo"}}]}`)
	if want := "Module '" + derivedID("/modules/0") + "':"; !strings.HasPrefix(e.Error(), want) {
		t.Errorf("got %q, want prefix %q", e, want)
	}
}

func TestDecodeFlow(t *testing.T) {
	f, err := DecodeFlow([]byte(`{
		"summary": "s", "description": "d", "timeout": 60,
		"schema": {"type": "object", "properties": {"x": {"default": 5}}},
		"value": {"modules": []},
		"on_behalf_of_email": "someone@example.invalid",
		"tag": 1, "dedicated_worker": "ignored", "ws_error_handler_muted": false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if *f.Summary != "s" || *f.Description != "d" || *f.Timeout != 60 || f.Value == nil || *f.OnBehalfOfEmail != "someone@example.invalid" {
		t.Fatalf("flow = %+v", f)
	}
	if string(f.Schema) != `{"type": "object", "properties": {"x": {"default": 5}}}` {
		t.Errorf("schema = %s, want it verbatim", f.Schema)
	}
	encoded, _ := f.MarshalJSON()
	if !json.Valid(encoded) {
		t.Fatalf("invalid JSON %s", encoded)
	}
	again, err := DecodeFlow(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if reencoded, _ := again.MarshalJSON(); !bytes.Equal(reencoded, encoded) {
		t.Errorf("round trip changed %s to %s", encoded, reencoded)
	}

	for _, c := range []struct {
		in, want string
		kind     ErrorKind
	}{
		{`{"summary":"s"}`, "invalid flow: missing field `value`", ErrFlow},
		{`{"value":{"modules":[]},"timeout":"60"}`, `invalid flow: /timeout: invalid type: string "60", expected i32`, ErrFlow},
		{`{"value":{"modules":[{"id":"a","value":{"type":"foo"}}]}}`, "Module 'a': unknown variant `foo`", ErrModule},
		{`{"value":{"modules":[{"id":"a"}]}}`, "Invalid flow value: missing field `value`", ErrFlowValue},
		{`{"value":{"modules":[]},"ws_error_handler_muted":true}`, "ws_error_handler_muted: true is an EE setting", ErrEEOnly},
		{`{"value":`, "invalid JSON", ErrSyntax},
	} {
		_, err := DecodeFlow([]byte(c.in))
		var e *Error
		if !errors.As(err, &e) || e.Kind != c.kind || !strings.Contains(e.Error(), c.want) {
			t.Errorf("DecodeFlow(%s) = %v, want kind %d containing %q", c.in, err, c.kind, c.want)
		}
	}
	if _, err := DecodeFlow([]byte(`{"value":{"modules":[{"id":"a","value":{"type":"foo"}}]}}`)); err.(*Error).Pointer != "/value/modules/0/value/type" {
		t.Errorf("pointer %q is not relative to the document", err.(*Error).Pointer)
	}
}

const richFlow = `{
	"modules": [
		{"id": "r", "summary": "raw", "value": {"type": "rawscript", "content": "export async function main(x: string) {\n\treturn \"\\u0000\" + x\n}", "language": "bun",
			"input_transforms": {"x": {"type": "static", "value": {"z": [1.50, -0, 1e400], "a": "é\u00e9"}}, "y": {"type": "javascript", "expr": "results.p"}}, "lock": "!inline r.lock", "assets": [{"kind": "resource", "path": "u/x/y"}]},
			"stop_after_if": {"expr": "result", "skip_if_stopped": false, "error_message": "stopped"},
			"stop_after_all_iters_if": {"expr": "true", "skip_if_stopped": true, "error_include_result": true},
			"skip_if": {"expr": "false"}, "sleep": {"type": "javascript", "expr": "1"}, "timeout": 5,
			"retry": {"constant": {"attempts": 1, "seconds": 2}, "exponential": {"attempts": 2, "multiplier": 3, "seconds": 4, "random_factor": 50}, "retry_if": {"expr": "true"}},
			"continue_on_error": true, "mock": {"enabled": false, "return_value": {"m": 1}},
			"suspend": {"required_events": 1, "timeout": 60, "resume_form": {"schema": {}}, "user_auth_required": true, "user_groups_required": {"type": "static", "value": ["g"]}, "self_approval_disabled": false, "hide_cancel": true, "continue_on_disapprove_timeout": false},
			"cache_ttl": 10, "cache_ignore_s3_path": true, "priority": -1, "delete_after_use": true, "delete_after_secs": 30, "apply_preprocessor": false, "pass_flow_input_directly": true,
			"debouncing": {"debounce_key": "k", "debounce_delay_s": 1, "max_total_debouncing_time": 2, "max_total_debounces_amount": 3, "debounce_args_to_accumulate": ["a"]}},
		{"id": "s", "value": {"type": "script", "path": "hub/1/x", "hash": "0123456789ABCDEF00", "tag_override": "t", "is_trigger": true, "pass_flow_input_directly": false}},
		{"id": "l", "value": {"type": "forloopflow", "iterator": {"type": "static", "value": [1, 2]}, "skip_failures": false, "parallel": true, "parallelism": {"type": "javascript", "expr": "2"}, "squash": false, "modules": [{"id": "l1", "value": {"type": "flow", "path": "f/x/sub"}}]}},
		{"id": "g", "value": {"type": "aiagent", "tag": "ai", "tools": [{"id": "t", "summary": "s", "description": "d", "value": {"tool_type": "mcp", "resource_path": "u/x/m", "exclude_tools": []}}, {"id": "u", "value": {"type": "flowscript", "id": 1}}, {"id": "w", "value": {"tool_type": "websearch"}}]}}
	],
	"failure_module": {"id": "failure", "value": {"type": "flowscript", "id": 7, "extra": [1]}},
	"same_worker": true, "preserve_step_tags": true, "concurrent_limit": 1, "concurrency_key": "c", "concurrency_time_window_s": 2,
	"custom_debounce_key": "d", "debounce_delay_s": 3, "debounce_args_to_accumulate": [], "skip_expr": "false", "cache_ttl": 4, "cache_ignore_s3_path": false,
	"early_return": "r", "priority": 5, "chat_input_enabled": false, "flow_env": {"V": "$var:u/x/v", "N": 1.0}, "delete_after_use": false, "delete_after_secs": 6
}`

func TestDecodedFormRoundTrip(t *testing.T) {
	v := decodeOK(t, richFlow)
	encoded, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("invalid JSON: %s", encoded)
	}
	assertRoundTrip(t, encoded)

	// Verbatim values stay verbatim through the decoded form.
	for _, want := range []string{
		`"value":{"z": [1.50, -0, 1e400], "a": "é\u00e9"}`,
		`"N":1.0`,
		`"value":{"type": "flowscript", "id": 7, "extra": [1]}`,
		`"value":{"type": "flowscript", "id": 1}`,
		`"hash":"0123456789abcdef"`,
	} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Errorf("decoded form lacks %s:\n%s", want, encoded)
		}
	}
	if bytes.Contains(encoded, []byte("delete_after_use")) {
		t.Errorf("decoded form keeps the dropped delete_after_use")
	}
}

func TestDecodedFormEscapesStrings(t *testing.T) {
	v := decodeOK(t, valueWith(`{"type":"rawscript","content":"\u0001\"\\\n\r\t/<>&é😀","language":"bun"}`))
	encoded, _ := v.MarshalJSON()
	if !bytes.Contains(encoded, []byte(`"content":"\u0001\"\\\n\r\t/<>&é😀"`)) {
		t.Fatalf("encoded %s", encoded)
	}
	assertRoundTrip(t, encoded)
}

func FuzzDecodeFlowValue(f *testing.F) {
	f.Add([]byte(richFlow))
	f.Add([]byte(nestedFlow))
	paths, _ := filepath.Glob(filepath.Join(casesDir, "of-*.json"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		var cf struct {
			Cases []struct {
				Given json.RawMessage `json:"given"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(data, &cf); err != nil {
			f.Fatal(err)
		}
		for _, c := range cf.Cases {
			f.Add([]byte(c.Given))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := DecodeFlowValue(data)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("error %T is not an *Error", err)
			}
			return
		}
		encoded, err := v.MarshalJSON()
		if err != nil || !json.Valid(encoded) {
			t.Fatalf("decoded form %s: %v", encoded, err)
		}
		assertRoundTrip(t, encoded)
	})
}

// The decoded form is a projection: re-decoding it keeps the decoded form
// but not the presence of defaulted fields.
func TestDecodedFormIsAProjection(t *testing.T) {
	v := decodeOK(t, `{"modules":[{"value":{"type":"forloopflow","iterator":{"type":"static","value":[]},"modules":[]}},{"id":"b","value":{"type":"branchall","branches":[{"modules":[]}]}}]}`)
	loop, branch := v.Modules[0].Value.(*ForLoop), v.Modules[1].Value.(*BranchAll).Branches[0]
	if loop.SkipFailures != nil || branch.SkipFailure != nil || !v.Modules[0].IDDerived {
		t.Fatal("the decoded model must record absence")
	}
	encoded, _ := v.MarshalJSON()
	assertRoundTrip(t, encoded)
	again := decodeOK(t, string(encoded))
	loop, branch = again.Modules[0].Value.(*ForLoop), again.Modules[1].Value.(*BranchAll).Branches[0]
	if loop.SkipFailures == nil || !*loop.SkipFailures || branch.SkipFailure == nil || !*branch.SkipFailure || again.Modules[0].IDDerived {
		t.Fatal("the decoded form writes defaults and ids out; re-decoding sees them as written")
	}
}

package ir

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseContract(t *testing.T) {
	yaml := `
roots:
  - flow: f/a/main
  - script: f/a/dispatched
provided:
  - selector: {flow: f/a/main, module: permit, occurrence: 1}
    provider: permitCheck
    mandatory: true
  - selector: {script: f/a/audit}
    static: {ok: true, n: 017}
    mandatory: true
host_modules: [f/a/scope.ts]
`
	one := 1
	want := &Contract{
		Roots: []Root{{Flow: "f/a/main"}, {Script: "f/a/dispatched"}},
		Provided: []ProvidedSite{
			{Selector: Selector{Flow: "f/a/main", Module: new("permit"), Occurrence: &one}, Provider: "permitCheck", Mandatory: true},
			// The static value is the JSON the sync folder's YAML dialect
			// gives: 017 is 17 (SY-9).
			{Selector: Selector{Script: "f/a/audit"}, Static: json.RawMessage(`{"ok":true,"n":17}`), Mandatory: true},
		},
		HostModules: []string{"f/a/scope.ts"},
	}
	got, err := ParseContract([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseContract = %+v\nwant %+v", got, want)
	}
	// JSON is YAML too.
	js, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseContract(js)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, want) {
		t.Errorf("ParseContract(%s) = %+v", js, back)
	}
	// A static null is a value, not an absent provider.
	c, err := ParseContract([]byte("roots: [{flow: f/a}]\nprovided:\n  - selector: {script: f/s}\n    static: null\n    mandatory: true\n"))
	if err != nil || string(c.Provided[0].Static) != "null" {
		t.Errorf("static null: %+v, %v", c, err)
	}
}

func TestParseContractErrors(t *testing.T) {
	site := "  - selector: {flow: f/a, module: m}\n    provider: p\n    mandatory: true\n"
	for _, c := range []struct{ src, want string }{
		{"", "expected an object"},
		{"roots: []\n", "at least one root"},
		{"roots: [{flow: f/a, script: f/b}]\n", "exactly one of flow and script"},
		{"roots: [{}]\n", "exactly one of flow and script"},
		{"roots: [{flow: f/a}, {flow: f/a}]\n", "listed twice"},
		{"roots: [{Flow: f/a}]\n", `unknown key "Flow"`},
		{"roots: [{flow: f/a}]\nextra: 1\n", `unknown key "extra"`},
		{"roots: {flow: f/a}\n", "roots: expected a list"},
		{"roots: [{flow: 1}]\n", "roots[0].flow: expected a string"},
		{"roots: [{flow: f/a}]\nprovided:\n  - provider: p\n    mandatory: true\n", "missing selector"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a}\n    provider: p\n    mandatory: true\n", "give `flow` and `module`, or `script`"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {script: f/s, module: m}\n    provider: p\n    mandatory: true\n", "a script selector has only `script`"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m}\n    provider: p\n    static: 1\n    mandatory: true\n", "exactly one of static and provider"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m}\n    mandatory: true\n", "exactly one of static and provider"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m}\n    provider: ''\n    mandatory: true\n", "empty name"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m}\n    provider: p\n", "must be mandatory"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m}\n    provider: p\n    mandatory: false\n", "must be mandatory"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m}\n    provider: p\n    mandatory: 'yes'\n", "expected a boolean"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m, occurrence: -1}\n    provider: p\n    mandatory: true\n", "non-negative integer"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m, occurrence: 1.5}\n    provider: p\n    mandatory: true\n", "non-negative integer"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m, occurrence: '1'}\n    provider: p\n    mandatory: true\n", "non-negative integer"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m, iterations: [0]}\n    provider: p\n    mandatory: true\n", "cannot narrow it"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m, branches: [default]}\n    provider: p\n    mandatory: true\n", "cannot narrow it"},
		{"roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: m, branches: [other]}\n    provider: p\n    mandatory: true\n", `expected an index or "default"`},
		{"roots: [{flow: f/a}]\nprovided:\n" + site + "host_modules: [a, a]\n", "listed twice"},
		{"roots: [{flow: f/a}]\nhost_modules: ['']\n", "empty path"},
		{"roots: [{flow: f/a}]\nroots: [{flow: f/b}]\n", "yaml"},
		{"roots: [{flow: f/a}\n", "yaml"},
	} {
		_, err := ParseContract([]byte(c.src))
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrContract || !strings.Contains(err.Error(), c.want) {
			t.Errorf("ParseContract(%q) = %v; want an %s error containing %q", c.src, err, ErrContract, c.want)
		}
	}
}

// TestSelectorFields checks the narrowing fields a selector can carry for
// optional hooks (0008), which a provided site refuses.
func TestSelectorFields(t *testing.T) {
	s, err := parseSelector(json.RawMessage(`{"flow":"f/a","module":"m","iterations":[0,2],"branches":["default",1]}`), "s")
	if err != nil {
		t.Fatal(err)
	}
	want := Selector{Flow: "f/a", Module: new("m"), Iterations: []int{0, 2}, Branches: []string{"default", "1"}}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("parseSelector = %+v, want %+v", s, want)
	}
	if err := s.validate("s"); err != nil {
		t.Errorf("validate: %v", err)
	}
	if got := (Selector{Flow: "f/a", Module: new("m"), Occurrence: new(int)}).String(); got != `{flow: f/a, module: "m", occurrence: 0}` {
		t.Errorf("String = %s", got)
	}
}

// TestEmptyModuleID checks that a selector can name the id "" (OF-2 allows
// any id) and that a missing module field is still refused.
func TestEmptyModuleID(t *testing.T) {
	c, err := ParseContract([]byte("roots: [{flow: f/a}]\nprovided:\n  - selector: {flow: f/a, module: '', occurrence: 1}\n    provider: p\n    mandatory: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Provided[0].Selector.Module; m == nil || *m != "" {
		t.Fatalf("module = %v, want the empty id", m)
	}
	if got := c.Provided[0].Selector.String(); got != `{flow: f/a, module: "", occurrence: 1}` {
		t.Errorf("String = %s", got)
	}
	// The empty id survives the contract's JSON form, in both directions.
	js, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), `"module":""`) {
		t.Errorf("JSON form lost the empty id: %s", js)
	}
	back, err := ParseContract(js)
	if err != nil || !reflect.DeepEqual(back, c) {
		t.Errorf("ParseContract(%s) = %+v, %v", js, back, err)
	}
	var decoded Contract
	if err := json.Unmarshal(js, &decoded); err != nil || !reflect.DeepEqual(&decoded, c) {
		t.Errorf("json.Unmarshal(%s) = %+v, %v", js, decoded, err)
	}
	// A Go-built selector without a module is refused, as a file's is.
	bad := &Contract{Roots: []Root{{Flow: "f/a"}}, Provided: []ProvidedSite{{Selector: Selector{Flow: "f/a"}, Provider: "p", Mandatory: true}}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "give `flow` and `module`") {
		t.Errorf("Validate without a module = %v", err)
	}
}

// TestStaticMustBeJSON checks that a Go-built contract's static value is
// checked as a parsed one is: valid JSON text, null included.
func TestStaticMustBeJSON(t *testing.T) {
	site := func(static json.RawMessage) *Contract {
		return &Contract{Roots: []Root{{Flow: "f/a"}}, Provided: []ProvidedSite{
			{Selector: Selector{Script: "f/s"}, Static: static, Mandatory: true}}}
	}
	for _, bad := range []json.RawMessage{json.RawMessage("invalid"), {}, json.RawMessage(`{"a":`)} {
		err := site(bad).Validate()
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrContract || !strings.Contains(err.Error(), "provided[0].static: not a JSON value") {
			t.Errorf("Validate(static %q) = %v", bad, err)
		}
	}
	for _, good := range []json.RawMessage{json.RawMessage("null"), json.RawMessage(` {"a": [1]} `)} {
		if err := site(good).Validate(); err != nil {
			t.Errorf("Validate(static %q) = %v", good, err)
		}
	}
}

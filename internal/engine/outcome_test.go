package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

func text(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }

// The outcome table of ADR 0004: each harness outcome and the value
// Windmill stores for it.
func TestStoreOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		out    StepOutcome
		step   string
		failed bool
		want   string
		origin Origin
	}{
		{"ok keeps the producer text", StepOutcome{Kind: OutcomeOK, Body: text(`{"b":1,"a":2}`)}, "a", false, `{"b":1,"a":2}`, OriginNone},
		{"error is wrapped (WR-21)", StepOutcome{Kind: OutcomeError, Body: text(`{"message":"boom","name":"Error","step_id":"a"}`)}, "a",
			true, `{"error":{"message":"boom","name":"Error","step_id":"a"}}`, OriginUser},
		{"a pre-shaped ManualFailure stays as it is (WR-V5)", StepOutcome{Kind: OutcomeError, Body: text(`{"error":{"name":"ManualFailure"},"k":1}`)}, "a",
			true, `{"error":{"name":"ManualFailure"},"k":1}`, OriginUser},
		{"escaped quotes the tail (WR-20)", StepOutcome{Kind: OutcomeEscaped, Tail: "x\nlast\n", ExitCode: 1}, "a",
			true, `{"error":{"message":"exit code for \"bun run\": 1, last log lines:\nx\nlast","name":"ExecutionErr","step_id":"a","exit_code":1}}`, OriginHarness},
		{"escaped outside a flow has no step_id", StepOutcome{Kind: OutcomeEscaped, ExitCode: 3}, "",
			true, `{"error":{"message":"exit code for \"bun run\": 3, last log lines:\n","name":"ExecutionErr","exit_code":3}}`, OriginHarness},
		{"a signal is the worker's execution error (WR-4)", StepOutcome{Kind: OutcomeEscaped, Signal: "SIGKILL", ExitCode: -1}, "a",
			true, `{"error":{"message":"execution error:\nprocess terminated by signal: SIGKILL","name":"ExecutionErr","step_id":"a"}}`, OriginHarness},
		{"empty is InternalErr with no step_id (WR-V2)", StepOutcome{Kind: OutcomeEmpty}, "a",
			true, `{"error":{"message":"Internal: Result of job is invalid json (empty)","name":"InternalErr"}}`, OriginHarness},
		{"an error the harness generated keeps its origin (WR-43, D14)", StepOutcome{Kind: OutcomeError, Body: text(`{"message":"execution error:\nProxy is an EE feature","name":"ExecutionErr","step_id":"a"}`), Origin: OriginHarness}, "a",
			true, `{"error":{"message":"execution error:\nProxy is an EE feature","name":"ExecutionErr","step_id":"a"}}`, OriginHarness},
		{"ok with a string wm_failure fails (WR-17, FX-13)", StepOutcome{Kind: OutcomeOK, Body: text(`{"wm_failure":"bad input","x":1}`)}, "a",
			true, `{"wm_failure":"bad input","x":1,"error":{"name":"ManualFailure","message":"bad input"}}`, OriginUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := StoreOutcome(tc.out, tc.step)
			if s.Failed != tc.failed || string(s.Value.Producer()) != tc.want || s.Origin != tc.origin {
				t.Fatalf("got failed=%v %s (%s); want failed=%v %s (%s)", s.Failed, s.Value.Producer(), s.Origin, tc.failed, tc.want, tc.origin)
			}
		})
	}
}

// WR-17, FX-13: a successful result that marks itself failed with a
// string wm_failure becomes a pre-shaped ManualFailure, its other members
// kept; a null marker marks nothing, and a marker of the wrong type
// disables both.
func TestManualFailure(t *testing.T) {
	for result, want := range map[string]string{
		`{"wm_failure":"bad input","x":1}`:          `{"wm_failure":"bad input","x":1,"error":{"name":"ManualFailure","message":"bad input"}}`,
		`{"x":1,"error":"old","wm_failure":"n<o>"}`: `{"x":1,"error":{"name":"ManualFailure","message":"n<o>"},"wm_failure":"n<o>"}`,
		`{"wm_failure":"x","wm_labels":["a"]}`:      `{"wm_failure":"x","wm_labels":["a"],"error":{"name":"ManualFailure","message":"x"}}`,
		`{"wm_failure":"x","wm_labels":null}`:       `{"wm_failure":"x","wm_labels":null,"error":{"name":"ManualFailure","message":"x"}}`,
		`{"wm_failure":true}`:                       "",
		`{"wm_failure":"x","wm_labels":"a"}`:        "",
		`{"wm_failure":"x","wm_labels":[1]}`:        "",
		`{"wm_failure":"x","wm_labels":[null]}`:     "",
		`{"wm_failure":"x","wm_labels":["a",null]}`: "",
		`{"wm_failure":"x","wm_labels":[]}`:         `{"wm_failure":"x","wm_labels":[],"error":{"name":"ManualFailure","message":"x"}}`,
		`{"wm_failure":null,"x":1}`:                 "",
		`{"x":1}`:                                   "",
		`["wm_failure"]`:                            "",
		`"wm_failure"`:                              "",
		// Only an object carries the markers: an array that would fill
		// the record by position (wm_labels, then wm_failure) marks
		// nothing (FX-13 at 1.862.0).
		`[[], "boom"]`:   "",
		`[null, "boom"]`: "",
		// Keys spelled with escapes are the same keys.
		`{"\u0077m_failure":"esc"}`:                      `{"\u0077m_failure":"esc","error":{"name":"ManualFailure","message":"esc"}}`,
		`{"wm\u005ffailure":"esc","wm_labels":[1]}`:      "",
		`{"wm_failur\u0065":"e","wm\u005flabels":["a"]}`: `{"wm_failur\u0065":"e","wm\u005flabels":["a"],"error":{"name":"ManualFailure","message":"e"}}`,
		// Look-alike values and nested keys are not markers.
		`{"a":"wm_failure","b":{"wm_failure":"x"}}`: "",
		`{"a":"line\nnext","b":"q\"x\\"}`:           "",
	} {
		v, failed := ManualFailure([]byte(result))
		if failed != (want != "") || failed && string(v.Producer()) != want {
			t.Errorf("%s: failed %v, %s; want %s", result, failed, v.Producer(), want)
		}
	}
}

func TestFailureValue(t *testing.T) {
	for raw, want := range map[string]string{
		`{"message":"m"}`: `{"error":{"message":"m"}}`,
		` "text" `:        `{"error":"text"}`,
		`{"error":{"name":"ManualFailure","message":"x"},"wm_failure":"x"}`: `{"error":{"name":"ManualFailure","message":"x"},"wm_failure":"x"}`,
		// Only a top-level error object named ManualFailure bypasses.
		`{"error":{"name":"Other"}}`:                   `{"error":{"error":{"name":"Other"}}}`,
		`{"error":"ManualFailure"}`:                    `{"error":{"error":"ManualFailure"}}`,
		`{"extra":{"error":{"name":"ManualFailure"}}}`: `{"error":{"extra":{"error":{"name":"ManualFailure"}}}}`,
		`[{"error":{"name":"ManualFailure"}}]`:         `{"error":[{"error":{"name":"ManualFailure"}}]}`,
		// Exact keys only: a key spelt in another case is another key.
		`{"error":{"NAME":"ManualFailure"},"k":1}`:                `{"error":{"error":{"NAME":"ManualFailure"},"k":1}}`,
		`{"error":{"name":"Other","Name":"ManualFailure"}}`:       `{"error":{"error":{"name":"Other","Name":"ManualFailure"}}}`,
		`{"ERROR":{"name":"ManualFailure"}}`:                      `{"error":{"ERROR":{"name":"ManualFailure"}}}`,
		`{"error":{"Name":"Other","name":"ManualFailure"},"k":1}`: `{"error":{"Name":"Other","name":"ManualFailure"},"k":1}`,
		`{"error":{"name":["ManualFailure"]}}`:                    `{"error":{"error":{"name":["ManualFailure"]}}}`,
		`not json`:                                                `{"error":{"message":"Non serializable error: not json"}}`,
	} {
		if got := string(FailureValue([]byte(raw)).Producer()); got != want {
			t.Errorf("FailureValue(%s) = %s, want %s", raw, got, want)
		}
	}
}

// WR-20's tail: the last 600 characters, what follows the last marker,
// ANSI escapes removed, trimmed.
func TestLogTail(t *testing.T) {
	long := strings.Repeat("a", 700) + "end"
	for log, want := range map[string]string{
		"  plain\n":                                    "plain",
		"preamble\nCODE EXECUTION ---\nmine\n":         "mine",
		"x CODE EXECUTION --- y CODE EXECUTION --- z":  "z",
		"\x1b[31mred\x1b[0m and \x1b[1;32mgreen\x1b[m": "red and green",
		long: strings.Repeat("a", 597) + "end",
		"":   "",
	} {
		if got := LogTail(log); got != want {
			t.Errorf("LogTail(%q) = %q, want %q", log, got, want)
		}
	}
	// Characters, not bytes.
	if got := LogTail(strings.Repeat("é", 601)); got != strings.Repeat("é", 600) {
		t.Errorf("multi-byte tail has %d characters", len([]rune(got)))
	}
}

// The entrypoint an invocation calls (ADR 0004, entrypoints; WR-12).
func TestEntrypoint(t *testing.T) {
	for _, tc := range []struct {
		args     string
		entry    string
		override bool
		invalid  bool
	}{
		{`{"a":1}`, "main", false, false},
		{`{}`, "main", false, false},
		{`{"_ENTRYPOINT_OVERRIDE":"preprocessor","a":1}`, "preprocessor", true, false},
		{`{"_ENTRYPOINT_OVERRIDE":"_alt9"}`, "_alt9", true, false},
		{`{"_ENTRYPOINT_OVERRIDE":"main"}`, "main", true, false},
		{`{"_ENTRYPOINT_OVERRIDE":"9lives"}`, "", true, true},
		{`{"_ENTRYPOINT_OVERRIDE":"a-b"}`, "", true, true},
		{`{"_ENTRYPOINT_OVERRIDE":""}`, "", true, true},
		{`{"_ENTRYPOINT_OVERRIDE":7}`, "", true, true},
		{`{"_ENTRYPOINT_OVERRIDE":null}`, "", true, true},
		// A key spelled with escapes is the same key; of duplicates the
		// last counts; nested keys and values that look alike do not.
		{`{"_ENTRYPOINT\u005fOVERRIDE":"alt"}`, "alt", true, false},
		{`{"_ENTRYPOINT_OVERRIDE":"a","b":1,"_ENTRYPOINT_OVERRIDE":"b2"}`, "b2", true, false},
		{`{"_ENTRYPOINT_OVERRIDE":"a","_ENTRYPOINT_OVERRIDE":7}`, "", true, true},
		{`{"a":{"_ENTRYPOINT_OVERRIDE":"x"},"b":"_ENTRYPOINT_OVERRIDE"}`, "main", false, false},
		{` {"_ENTRYPOINT_OVERRIDE" : "sp"} `, "sp", true, false},
	} {
		entry, override, err := Entrypoint(text(tc.args))
		if entry != tc.entry || override != tc.override || errors.Is(err, ErrInvalidEntrypoint) != tc.invalid {
			t.Errorf("Entrypoint(%s) = %q, %v, %v", tc.args, entry, override, err)
		}
		if tc.invalid && !strings.HasPrefix(err.Error(), "Invalid entrypoint override") {
			t.Errorf("Entrypoint(%s): %v", tc.args, err)
		}
	}
	for _, args := range []string{`[1]`, `null`, `{"a":1} x`, `{"a":1}{}`, `{"a":1} 2`, `{"a":`, ``} {
		if _, _, err := Entrypoint(text(args)); err == nil || errors.Is(err, ErrInvalidEntrypoint) {
			t.Errorf("arguments %q that are not an object: %v", args, err)
		}
	}
}

// storedEntrypoint answers as Entrypoint does on every stored argument
// object, whose keys spell the override's name only
// as itself: escaped spellings, look-alike keys and values, and keys that
// store alike included.
func TestStoredEntrypoint(t *testing.T) {
	for _, args := range []string{
		`{"a":1}`, `{}`, `{"_ENTRYPOINT_OVERRIDE":"pre"}`, `{"_ENTRYPOINT\u005fOVERRIDE":"pre"}`,
		`{"\u005fENTRYPOINT_OVERRIDE":"x9","a":"\n"}`, `{"_ENTRYPOINT_OVERRIDE":"bad-name"}`,
		`{"a":"_ENTRYPOINT_OVERRIDE"}`, `{"a":{"_ENTRYPOINT_OVERRIDE":"x"}}`, `{"_ENTRYPOINT_OVERRIDEx":"y"}`,
		`{"_ENTRYPOINT_OVERRIDE":"a","_ENTRYPOINT\u005fOVERRIDE":"b"}`, `{"_entrypoint_override":"x"}`,
	} {
		stored, err := storeMembers(text(args))
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		ge, go_, gerr := storedEntrypoint(stored)
		we, wo, werr := Entrypoint(stored)
		if ge != we || go_ != wo || (gerr == nil) != (werr == nil) || gerr != nil && gerr.Error() != werr.Error() {
			t.Errorf("%s (stored %s): %q %v %v; Entrypoint %q %v %v", args, stored.Producer(), ge, go_, gerr, we, wo, werr)
		}
	}
}

// A step that its timeout ended fails with its boundary's text: FX-25's
// for Bun, naming the process phase and the limit that applied, in whole
// seconds, rounded down; WR-44's for native, the duration in whole
// seconds, rounded down, whichever limit applied.
func TestTimeoutError(t *testing.T) {
	for _, tc := range []struct {
		b     Boundary
		step  string
		d     time.Duration
		atMax bool
		want  string
	}{
		{BoundaryBun, "a", time.Second, false, "{\"error\":{\"message\":\"execution error:\\njob process terminated due to timeout: `bun run` exceeded the 1s custom timeout set on the script or flow step\",\"name\":\"ExecutionErr\",\"step_id\":\"a\"}}"},
		{BoundaryBun, "a", 2900 * time.Millisecond, false, "{\"error\":{\"message\":\"execution error:\\njob process terminated due to timeout: `bun run` exceeded the 2s custom timeout set on the script or flow step\",\"name\":\"ExecutionErr\",\"step_id\":\"a\"}}"},
		{BoundaryBun, "", 604800 * time.Second, true, "{\"error\":{\"message\":\"execution error:\\njob process terminated due to timeout: `bun run` exceeded the 604800s instance maximum job duration (TIMEOUT env variable of the worker), which no custom or default timeout can exceed\",\"name\":\"ExecutionErr\"}}"},
		{BoundaryNative, "a", time.Second, false, `{"error":{"message":"execution error:\nQuery timeout after (>1s)","name":"ExecutionErr","step_id":"a"}}`},
		{BoundaryNative, "a", 2900 * time.Millisecond, false, `{"error":{"message":"execution error:\nQuery timeout after (>2s)","name":"ExecutionErr","step_id":"a"}}`},
		{BoundaryNative, "a", 604800 * time.Second, true, `{"error":{"message":"execution error:\nQuery timeout after (>604800s)","name":"ExecutionErr","step_id":"a"}}`},
	} {
		if got := string(TimeoutError(tc.b, tc.step, tc.d, tc.atMax).Producer()); got != tc.want {
			t.Errorf("TimeoutError(%s, %q, %s, %v) = %s, want %s", tc.b, tc.step, tc.d, tc.atMax, got, tc.want)
		}
	}
}

// Entrypoint's walk answers as the decode into a map of members it
// replaced, on texts of every kind: escaped and
// duplicate keys, trailing text, invalid UTF-8, deep nesting.
func TestEntrypointWalk(t *testing.T) {
	ref := func(args []byte) (string, bool, error) {
		var top map[string]json.RawMessage
		if err := json.Unmarshal(args, &top); err != nil || top == nil {
			return "", false, fmt.Errorf("engine: the arguments are not a JSON object")
		}
		raw, ok := top[EntrypointOverride]
		if !ok {
			return "main", false, nil
		}
		var name string
		if json.Unmarshal(raw, &name) != nil || !entrypointName.MatchString(name) {
			return "", true, fmt.Errorf("%w %s: must match %s (letters, digits and underscores, not starting with a digit)", ErrInvalidEntrypoint, raw, entrypointName)
		}
		return name, true, nil
	}
	for _, args := range []string{
		`{"_ENTRYPOINT_OVERRIDE":"p"}`, `{"_ENTRYPOINT\u005fOVERRIDE":"p"}`, `{"_ENTRYPOINT_OVERRIDE":"a","_ENTRYPOINT_OVERRIDE":"b"}`,
		`{"_ENTRYPOINT_OVERRIDE":"a","_ENTRYPOINT_OVERRIDE":null}`, `{"_ENTRYPOINT_OVERRIDE": "x" , "y":[1,{"z":2}]}`,
		`{"_ENTRYPOINT_OVERRIDE":"\u0061"}`, `{"_ENTRYPOINT_OVERRIDE":"a\u0000"}`, "{\"_ENTRYPOINT_OVERRIDE\xff\":\"q\"}",
		"{\"_ENTRYPOINT_OVERRIDE\":\"a\xff\"}", `{"a":1} x`, `{"a":1}{}`, `{"a":1} 1e400`, `{"a":1}   `, `{"a":1,}`,
		`{"a":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`, `null`, `[]`, `"s"`, ``, `{`,
	} {
		ge, go_, gerr := Entrypoint(text(args))
		we, wo, werr := ref([]byte(args))
		if ge != we || go_ != wo || (gerr == nil) != (werr == nil) || gerr != nil && gerr.Error() != werr.Error() {
			t.Errorf("%.80q: %q %v %v; the decode %q %v %v", args, ge, go_, gerr, we, wo, werr)
		}
	}
}

// ManualFailure's pre-check skips only results its walk
// leaves successes: on every probe it answers as the walk does.
func TestManualFailureSkip(t *testing.T) {
	for _, r := range []string{
		`{"wm_failure":"x"}`, `{"\u0077m_failure":"x"}`, `{"wm\u005ffailure":"x"}`, `{"w\u006d_failure":"x","wm_labels":null}`,
		`{"wm_labels":["a"]}`, `{"wm\u005flabels":["a"]}`, `{"a":"\n","b":"\"","c":"\\u0077m_failure"}`,
		`{"a":"wm_failure"}`, `{"a":{"wm_failure":"x"}}`, `["wm_failure"]`, `{"x":1}`, `{}`, `{"wm_failure":"x"} 1`,
		`{"wm_failure":"x"} y`, `{"wm_failure":"x",}`, `{"\\u0077m_failure":"x"}`, `{"wm_failure":"\u00e9"}`,
		`[{"wm_failure":"x"}]`, ` [{"\u0077m_failure":"x"}]`, `"wm_failure"`, ``, `  `, "\u00a0{\"wm_failure\":\"nbsp\"}",
		` {"wm_failure":"spaced"} `, "\n\t{\"wm_failure\":\"x\"}",
	} {
		gv, gok := ManualFailure([]byte(r))
		wv, wok := manualFailure([]byte(r))
		if gok != wok || string(gv.Producer()) != string(wv.Producer()) {
			t.Errorf("%s: %v %s; the walk %v %s", r, gok, gv.Producer(), wok, wv.Producer())
		}
	}
}

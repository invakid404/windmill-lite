package engine

import (
	"errors"
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
		`{"wm_failure":null,"x":1}`:                 "",
		`{"x":1}`:                                   "",
		`["wm_failure"]`:                            "",
		`"wm_failure"`:                              "",
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
	} {
		entry, override, err := Entrypoint(text(tc.args))
		if entry != tc.entry || override != tc.override || errors.Is(err, ErrInvalidEntrypoint) != tc.invalid {
			t.Errorf("Entrypoint(%s) = %q, %v, %v", tc.args, entry, override, err)
		}
		if tc.invalid && !strings.HasPrefix(err.Error(), "Invalid entrypoint override") {
			t.Errorf("Entrypoint(%s): %v", tc.args, err)
		}
	}
	if _, _, err := Entrypoint(text(`[1]`)); err == nil || errors.Is(err, ErrInvalidEntrypoint) {
		t.Errorf("arguments that are not an object: %v", err)
	}
}

// A step that its timeout ended fails with its boundary's text: WR-37's
// for Bun, naming the limit that applied; WR-44's for native, the
// duration in whole seconds, rounded down, whichever limit applied.
func TestTimeoutError(t *testing.T) {
	for _, tc := range []struct {
		b            Boundary
		step         string
		d            time.Duration
		instanceWide bool
		want         string
	}{
		{BoundaryBun, "a", time.Second, false, `{"error":{"message":"execution error:\njob process terminated due to timeout after exceeding job-specific duration limit","name":"ExecutionErr","step_id":"a"}}`},
		{BoundaryBun, "", time.Hour, true, `{"error":{"message":"execution error:\njob process terminated due to timeout after exceeding instance-wide job duration limit","name":"ExecutionErr"}}`},
		{BoundaryNative, "a", time.Second, false, `{"error":{"message":"execution error:\nQuery timeout after (>1s)","name":"ExecutionErr","step_id":"a"}}`},
		{BoundaryNative, "a", 2900 * time.Millisecond, false, `{"error":{"message":"execution error:\nQuery timeout after (>2s)","name":"ExecutionErr","step_id":"a"}}`},
		{BoundaryNative, "a", 604800 * time.Second, true, `{"error":{"message":"execution error:\nQuery timeout after (>604800s)","name":"ExecutionErr","step_id":"a"}}`},
	} {
		if got := string(TimeoutError(tc.b, tc.step, tc.d, tc.instanceWide).Producer()); got != tc.want {
			t.Errorf("TimeoutError(%s, %q, %s, %v) = %s, want %s", tc.b, tc.step, tc.d, tc.instanceWide, got, tc.want)
		}
	}
}

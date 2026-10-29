package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Stored is a leaf's outcome as Windmill stores it (ADR 0004, the outcome
// table): the job's result, or its failure value, which is also what the
// flow sees of the failed step (WR-21).
type Stored struct {
	// Failed is set when the job failed; Value is then its failure value.
	Failed bool
	// Value is the producer text of the result or of the failure value.
	Value jsonval.Value
	// Origin is who produced a failure value: the step's code (OriginUser,
	// for the wrapper's error object) or the harness (OriginHarness, for
	// the errors windmill-lite generates in place of the worker's).
	Origin Origin
}

// TailLength is how much of a job's log an escaped failure quotes: its
// last 600 characters (WR-20).
const TailLength = 600

// codeExecutionMarker ends the worker's preamble in a job's log; WR-20's
// tail keeps only the text after the last one.
const codeExecutionMarker = "CODE EXECUTION ---"

// ansiEscape matches the ANSI escape sequences WR-20 removes from a tail:
// CSI sequences and two-character escapes.
var ansiEscape = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|[@-Z\\\\-_])")

// StoreOutcome maps a Bun step's outcome to the value Windmill stores for
// it (WR-21 and WR-V5 for errors). stepID is the flow step's module id, or
// "" for a script job, and is added to the errors windmill-lite generates
// where the worker adds it; the wrapper's own error object already carries
// the step id it read from WM_FLOW_STEP_ID (WR-18).
//
//   - OutcomeOK: the result text, as the harness serialised it (WR-13),
//     unless it marks itself failed with a string wm_failure (WR-17, FX-13):
//     then it fails, kept as it is with a ManualFailure error added
//     (ManualFailure).
//   - OutcomeError: the wrapper's error object, wrapped as {"error": …}
//     unless it is already a pre-shaped ManualFailure (WR-21, WR-V5).
//   - OutcomeEscaped, by a signal: the worker's
//     `execution error:\nprocess terminated by signal: <signal>` (WR-4,
//     WR-37); by an exit: the log-tail error of WR-20, with exit_code.
//   - OutcomeEmpty: the completion's InternalErr, with no step id even for
//     a flow step (WR-V2), and no Rust location suffix (D14).
func StoreOutcome(out StepOutcome, stepID string) Stored {
	switch out.Kind {
	case OutcomeOK:
		if v, ok := ManualFailure(out.Body.Producer()); ok {
			return Stored{Failed: true, Value: v, Origin: OriginUser}
		}
		return Stored{Value: out.Body}
	case OutcomeError:
		return Stored{Failed: true, Value: FailureValue(out.Body.Producer()), Origin: OriginUser}
	case OutcomeEscaped:
		if out.Signal != "" {
			return Stored{Failed: true, Value: workerError(stepID, "execution error:\nprocess terminated by signal: "+out.Signal), Origin: OriginHarness}
		}
		e := struct {
			Message  string `json:"message"`
			Name     string `json:"name"`
			StepID   string `json:"step_id,omitempty"`
			ExitCode int    `json:"exit_code"`
		}{fmt.Sprintf("exit code for \"bun run\": %d, last log lines:\n%s", out.ExitCode, LogTail(out.Tail)), "ExecutionErr", stepID, out.ExitCode}
		return Stored{Failed: true, Value: wrapped(e), Origin: OriginHarness}
	case OutcomeEmpty:
		return Stored{Failed: true, Value: wrapped(struct {
			Message string `json:"message"`
			Name    string `json:"name"`
		}{"Internal: Result of job is invalid json (empty)", "InternalErr"}), Origin: OriginHarness}
	}
	return Stored{Failed: true, Value: workerError(stepID, fmt.Sprintf("execution error:\nwindmill-lite: unknown step outcome %q", out.Kind)), Origin: OriginHarness}
}

// FailureValue is how a failed job's raw error value is stored and passed on
// (WR-21): a JSON object whose top-level "error" is an object named
// ManualFailure is kept as it is, with its sibling fields (WR-V5); any other
// JSON value is wrapped as {"error": value}; text that is not JSON becomes
// {"error":{"message":"Non serializable error: <text>"}}.
func FailureValue(raw []byte) jsonval.Value {
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) {
		return wrapped(struct {
			Message string `json:"message"`
		}{"Non serializable error: " + string(raw)})
	}
	// The marker is the exact key path error.name: maps, not structs, since
	// encoding/json matches struct fields without regard to case.
	var top, inner map[string]json.RawMessage
	var name string
	if json.Unmarshal(raw, &top) == nil && top != nil &&
		json.Unmarshal(top["error"], &inner) == nil && inner != nil &&
		json.Unmarshal(inner["name"], &name) == nil && name == "ManualFailure" {
		return jsonval.FromProducer(append([]byte(nil), raw...))
	}
	return jsonval.FromProducer(append(append([]byte(`{"error":`), raw...), '}'))
}

// ManualFailure is the failure a successful result turns into when it marks
// itself failed (WR-17, FX-13): a JSON object whose top-level wm_failure is
// a string becomes that object with a top-level error member
// {"name":"ManualFailure","message":<wm_failure>} put in, in place of an
// error member it had or else last, its other members kept. It is then a
// pre-shaped failure, stored and passed on as it is (WR-21, WR-V5). A
// wm_failure that is null or absent marks nothing, and a marker of the wrong
// type (a wm_failure that is not a string, a wm_labels that is not an array
// of strings or null) disables both markers, so the result stays a
// success. ok is false when the result stays one.
func ManualFailure(result []byte) (jsonval.Value, bool) {
	ms, ok := members(bytes.TrimSpace(result))
	if !ok {
		return jsonval.Value{}, false
	}
	var message *string
	for _, m := range ms {
		v := bytes.TrimSpace(m.value)
		switch m.key {
		case "wm_failure":
			var s *string
			if json.Unmarshal(v, &s) != nil {
				return jsonval.Value{}, false
			}
			message = s
		case "wm_labels":
			var labels []string
			if json.Unmarshal(v, &labels) != nil {
				return jsonval.Value{}, false
			}
		}
	}
	if message == nil {
		return jsonval.Value{}, false
	}
	e := wrapped(struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}{"ManualFailure", *message})
	ems, _ := members(e.Producer())
	out, put := make([]rawMember, 0, len(ms)+1), false
	for _, m := range ms {
		if m.key == "error" {
			if put {
				continue
			}
			m.value, put = ems[0].value, true
		}
		out = append(out, m)
	}
	if !put {
		out = append(out, ems[0])
	}
	return jsonval.FromProducer(writeMembers(out)), true
}

// LogTail is the tail an escaped failure quotes (WR-20): the last
// TailLength characters of the log text, only what follows the last
// `CODE EXECUTION ---` marker, with ANSI escapes removed and surrounding
// space trimmed.
func LogTail(log string) string {
	if r := []rune(log); len(r) > TailLength {
		log = string(r[len(r)-TailLength:])
	}
	if i := strings.LastIndex(log, codeExecutionMarker); i >= 0 {
		log = log[i+len(codeExecutionMarker):]
	}
	return strings.TrimSpace(ansiEscape.ReplaceAllString(log, ""))
}

// TimeoutError is the stored value of a step that its timeout d ended,
// with the step abandoned (FX-24, D2). A Bun step's text is WR-37's, which
// names the job-specific limit unless the duration was the instance's
// maximum; a native step's is WR-44's, `Query timeout after (>Ns)` with the
// duration in whole seconds, rounded down, whichever limit applied.
func TimeoutError(b Boundary, stepID string, d time.Duration, instanceWide bool) jsonval.Value {
	if b == BoundaryNative {
		return workerError(stepID, fmt.Sprintf("execution error:\nQuery timeout after (>%ds)", int64(d/time.Second)))
	}
	limit := "job-specific duration limit"
	if instanceWide {
		limit = "instance-wide job duration limit"
	}
	return workerError(stepID, "execution error:\njob process terminated due to timeout after exceeding "+limit)
}

// workerError is a failure the worker raises around a job, wrapped:
// {"message", "name": "ExecutionErr", "step_id"} with step_id only for a
// flow step (FX-12, WR-37).
func workerError(stepID, message string) jsonval.Value {
	return wrapped(struct {
		Message string `json:"message"`
		Name    string `json:"name"`
		StepID  string `json:"step_id,omitempty"`
	}{message, "ExecutionErr", stepID})
}

// wrapped returns {"error": e} (WR-21). Its fields are strings and
// numbers, so encoding cannot fail; strings are encoded without HTML
// escaping, as Windmill's server writes them.
func wrapped(e any) jsonval.Value {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		panic(err)
	}
	return jsonval.FromProducer(append(append([]byte(`{"error":`), bytes.TrimSpace(b.Bytes())...), '}'))
}

// EntrypointOverride is the argument that names the export a step calls
// instead of main (WR-12).
const EntrypointOverride = "_ENTRYPOINT_OVERRIDE"

// ErrInvalidEntrypoint is the kind of the error Entrypoint returns for an
// override that is not a valid export name.
var ErrInvalidEntrypoint = errors.New("Invalid entrypoint override")

var entrypointName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Entrypoint returns the export a step's invocation calls (ADR 0004,
// entrypoints): the value of _ENTRYPOINT_OVERRIDE when the argument object
// carries one, which the flow preprocessor step always does (WR-12), and
// main otherwise. override reports whether the arguments carry one, which
// is what selects the WR-V1 fallback in the harness. An override that is
// not a string matching ^[A-Za-z_][A-Za-z0-9_]*$ is refused before any
// wrapper runs, with an error wrapping ErrInvalidEntrypoint, whose text
// begins as Windmill's does.
func Entrypoint(args jsonval.Value) (entry string, override bool, err error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(args.Producer(), &top); err != nil || top == nil {
		return "", false, fmt.Errorf("engine: the arguments are not a JSON object")
	}
	raw, ok := top[EntrypointOverride]
	if !ok {
		return "main", false, nil
	}
	var name string
	if json.Unmarshal(raw, &name) != nil || !entrypointName.MatchString(name) {
		return "", true, fmt.Errorf("%w: %s", ErrInvalidEntrypoint, raw)
	}
	return name, true, nil
}

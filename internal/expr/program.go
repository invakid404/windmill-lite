package expr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// The program an evaluation runs on the engine (milestone B2b), around an
// expression's Source. An evaluation runs these scripts on one fresh
// context, in order, each a global script named eval_script (JE-21):
//
//  1. preludeContext: it parses each context value that the evaluation
//     binds as a host string into its JavaScript value, in place, so that
//     it stays a property of globalThis (JE-10, JE-16), and defines the
//     helpers variable, resource and flow_user_state over the host
//     functions, which it removes from globalThis (JE-20).
//  2. preludeResults, when the evaluation binds the results lookup: the
//     lexical results proxy of JE-12 and JE-18.
//  3. preludeError, when the text mentions error: the lexical error of
//     JE-17.
//  4. The program: Source, then the serialisation of JE-15 on its last
//     line.
//
// The scripts' text is observable, because a stack frame in an error
// message gives a position in it (JE-21). The layout reproduces the frames
// recording batch E3a recorded: the user's code at line 1 after the
// wrapper's opening (Source), JSON.stringify called from line 1, column 64
// for a three-character expression, the helper variable throwing from
// line 7, column 50, and a failed results read throwing from
// __throwOrParse at line 5, column 54, called from __getResult at line 11,
// column 59. A change to these texts must keep those positions; the tests
// compare E3a's recorded messages byte for byte.

// serialise follows Source on its last line: the value becomes
// JSON.stringify(value ?? null), and a value that JSON.stringify gives no
// text for (a function, a symbol) becomes null (JE-15). A rejection passes
// through to the evaluation unchanged.
const serialise = `.then((v) => JSON.stringify(v ?? null) ?? "null")`

// programSource is the program the engine compiles for e.
func programSource(e *Expr) string { return e.Source + serialise }

// preludeContext parses the bound context values and defines the helpers.
// Each helper awaits its host function and parses the JSON text it
// answers; a host function's rejection is thrown again from the helper's
// own frame, as the Error of JE-20 with the API's text. The intrinsics the
// helpers call later (JSON.parse, Promise.prototype.then, Reflect.apply)
// are taken before the user's code runs, so its replacements do not reach
// them; Error, which the helpers construct, is looked up when they throw.
var preludeContext = `{
  const parse = JSON.parse, hasOwn = Object.hasOwn, apply = Reflect.apply, then = Promise.prototype.then;
  for (const name of ` + mustJSON(hostStringNames) + `) if (hasOwn(globalThis, name)) globalThis[name] = parse(globalThis[name]);
  const [getVariable, getResource, getFlowUserState] = ` + mustJSON([]string{hostVariable, hostResource, hostFlowUserState}) + `.map((name) => { const fn = globalThis[name]; delete globalThis[name]; return fn; });
  const settle = (promise) => apply(then, promise, [undefined, (failure) => [failure.message]]);
  const variable = async (path) => { const outcome = await settle(getVariable(path));
    if (typeof outcome !== "string") { throw new Error(outcome[0]); } return parse(outcome); };
  const resource = async (path) => { const outcome = await settle(getResource(path));
    if (typeof outcome !== "string") { throw new Error(outcome[0]); } return parse(outcome); };
  const flow_user_state = async (key) => { const outcome = await settle(getFlowUserState(key));
    if (typeof outcome !== "string") { throw new Error(outcome[0]); } return parse(outcome); };
  Object.assign(globalThis, { variable, resource, flow_user_state });
}
`

// preludeResults declares results: a proxy whose string keys read a step's
// result through the host lookup and resolve to its value, and whose symbol
// keys give undefined (JE-18). Its target is an empty object, so that
// JSON.stringify(results) gives {} (JE-12). A failed read rejects with the
// read's text as an Error thrown from __throwOrParse.
const preludeResults = `const results = (() => {
  const parse = JSON.parse, apply = Reflect.apply, then = Promise.prototype.then, lookupResult = globalThis.` + hostResults + `;
  delete globalThis.` + hostResults + `;
  function __throwOrParse(readOutcome) {
    if (typeof readOutcome !== "string") { throw new Error(readOutcome[0]); }
    return parse(readOutcome);
  }
  const settleLookup = (promise) => apply(then, promise, [undefined, (failure) => [failure.message]]);
  // A read answers the step's result as JSON text, or fails with the read's text.
  async function __getResult(key) {
    return __throwOrParse(await settleLookup(lookupResult(key)));
  }
  return new Proxy({}, { get: (_, key) => (typeof key === "symbol" ? undefined : __getResult(key)) });
})();
`

// preludeError declares error, derived from previous_result by JE-17's
// decision list.
const preludeError = `const error = (() => {
  const p = globalThis.previous_result;
  if (p != null && p.error) return p.error;
  if (Array.isArray(p)) {
    const found = p.filter((item) => typeof item === "object" && item !== null && "error" in item);
    if (found.length === 1) return found[0].error;
    if (found.length === 0) return { name: "MultipleErrors", message: "Could not parse errors", errors: p };
    const entry = (item, i) => "[" + (item.error?.step_id ?? i) + "] " + (item.error?.message ?? item.error?.name);
    return { name: "MultipleErrors", message: found.map(entry).join("; "), errors: p };
  }
  if (p) return { name: "UnknownError", message: "Could not parse the error", error: p };
  return { name: "UnknownError", message: "No error found" };
})();
`

// hostStringNames are the names an evaluation can bind as host strings:
// the context values, WM_SCHEDULED_FOR, flow_input and flow_env (JE-10).
var hostStringNames = func() []string {
	var out []string
	for _, n := range bindable {
		if n != "results" && n != "error" {
			out = append(out, n)
		}
	}
	return out
}()

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// EvalError is an evaluation that failed, with Windmill's text for the
// failure as its message. Each site wraps that text in its own (JE-23 to
// JE-35).
type EvalError struct {
	// Message is the evaluator's error text:
	//   - QuickJS evaluation error: Error: <message>, then a newline and the
	//     engine's stack when it has one, for a thrown Error (JE-21);
	//   - QuickJS evaluation error: Exception generated by quickjs: <value>
	//     and a newline, for another thrown value (JE-21);
	//   - the memory-limit message of JE-3, for running out of memory or a
	//     thrown null or undefined;
	//   - the time-limit message of JE-2, after the last attempt;
	//   - serde_json's recursion limit message, for a context value nested
	//     too deeply (JE-49).
	Message string
	// Err is the cause: the engine's *Exception, ErrOutOfMemory,
	// ErrAttemptTimeout, ErrTooDeep, ErrNotString or ErrNotJSON.
	Err error
}

func (e *EvalError) Error() string { return e.Message }

func (e *EvalError) Unwrap() error { return e.Err }

// ErrOutOfMemory is the cause of an evaluation that ran out of memory, or
// threw null or undefined, which Windmill cannot tell apart (JE-3).
var ErrOutOfMemory = errors.New("expr: evaluation ran out of memory")

// ErrTooDeep is the cause of an evaluation whose bound context value is
// nested deeper than 127 levels (JE-49).
var ErrTooDeep = errors.New("expr: context value nested too deeply")

// ErrNotJSON is the cause of an evaluation whose program completed with a
// string that is not one JSON value. The serialisation calls the context's
// JSON.stringify and Promise.prototype.then as they are when it runs, as
// Windmill's does, so the user's code can replace them.
var ErrNotJSON = errors.New("expr: program did not complete with JSON text")

// maxDepth is the deepest nesting of arrays and objects a bound context
// value may have: serde_json's default recursion limit (JE-49).
const maxDepth = 127

// windmillError returns Windmill's error for the engine's err in an
// evaluation of e, or err itself when it is not the evaluation's failure
// (the context ended, or the engine closed).
func windmillError(e *Expr, l Limits, err error) error {
	var ex *Exception
	switch {
	case errors.As(err, &ex):
		switch {
		case ex.Thrown == ThrownNull || ex.Thrown == ThrownUndefined,
			ex.Thrown == ThrownError && ex.Name == "InternalError" && ex.Message == "out of memory":
			return &EvalError{Message: memoryMessage(l), Err: ErrOutOfMemory}
		case ex.Thrown == ThrownError:
			msg := "QuickJS evaluation error: Error: " + ex.Message
			if ex.Stack != "" {
				msg += "\n" + ex.Stack
			}
			return &EvalError{Message: msg, Err: ex}
		default:
			return &EvalError{Message: "QuickJS evaluation error: Exception generated by quickjs: " + ex.Text + "\n", Err: ex}
		}
	case errors.Is(err, ErrAttemptTimeout):
		return &EvalError{Message: timeoutMessage(e, l), Err: ErrAttemptTimeout}
	case errors.Is(err, ErrNotString):
		return &EvalError{Message: "QuickJS evaluation error: the serialised value is not a string", Err: ErrNotString}
	case errors.Is(err, ErrNotJSON):
		return &EvalError{Message: "QuickJS evaluation error: the serialised value is not JSON", Err: ErrNotJSON}
	}
	return err
}

// memoryMessage is JE-3's text.
func memoryMessage(l Limits) string {
	return fmt.Sprintf("The expression evaluation exceeded the memory limit of %d MB. Reduce the amount of data handled in the expression, or raise the cap via the QUICKJS_MEMORY_LIMIT_MB environment variable.", l.MemoryMB)
}

// timeoutMessage is JE-2's text, which quotes the trimmed text before any
// rewriting (for a predicate, wrapped as Boolean(...)).
func timeoutMessage(e *Expr, l Limits) string {
	return fmt.Sprintf("The expression evaluation `%s` took too long to execute (>%dms)", e.Trimmed, l.Attempt.Milliseconds())
}

// retryable reports whether an evaluation that failed with err is tried
// again: Windmill retries any error whose text contains "took too long",
// its own time-limit message or not (JE-2).
func retryable(err error) bool {
	var ee *EvalError
	return errors.As(err, &ee) && strings.Contains(ee.Message, "took too long")
}

// checkDepth returns JE-49's error for the first bound value nested deeper
// than maxDepth: flow_input as a whole, re-serialised compactly with its
// outer object as the first level; then each other context value's text
// as bound; then each member of flow_env on its own, re-serialised
// compactly. Such a value fails before any JavaScript runs, with
// serde_json's text and the line and column of the bracket that goes past
// the limit, counted in the text that is checked.
func checkDepth(in Input) error {
	var input, env []byte
	var values [][]byte
	for _, hs := range in.Strings {
		switch hs.Name {
		case "flow_input":
			input = hs.Text
		case "flow_env":
			env = hs.Text
		default:
			values = append(values, hs.Text)
		}
	}
	check := func(t []byte, reserialised bool) error {
		if !tooDeep(t) {
			return nil
		}
		if reserialised {
			t = compactText(t)
		}
		line, col := depthPosition(t)
		return &EvalError{Message: fmt.Sprintf("recursion limit exceeded at line %d column %d", line, col), Err: ErrTooDeep}
	}
	if input != nil {
		if err := check(input, true); err != nil {
			return err
		}
	}
	for _, t := range values {
		if err := check(t, false); err != nil {
			return err
		}
	}
	if env != nil && tooDeep(env) {
		members := topMembers(env)
		for _, k := range slices.Sorted(maps.Keys(members)) {
			if err := check(members[k], true); err != nil {
				return err
			}
		}
	}
	return nil
}

// compactText returns t without the white space outside its strings, its
// escapes and numbers as written. It scans bytes, so no depth limits it.
func compactText(t []byte) []byte {
	out := make([]byte, 0, len(t))
	for i := 0; i < len(t); i++ {
		switch c := t[i]; c {
		case ' ', '\t', '\n', '\r':
		case '"':
			start := i
			for i++; i < len(t) && t[i] != '"'; i++ {
				if t[i] == '\\' {
					i++
				}
			}
			out = append(out, t[start:min(i+1, len(t))]...)
		default:
			out = append(out, c)
		}
	}
	return out
}

// topMembers returns the members of the JSON object text obj by decoded
// key, each value's text verbatim, the last of duplicate keys kept; it is
// empty when obj is not an object. It scans bytes, so no depth limits it.
func topMembers(obj []byte) map[string][]byte {
	out := map[string][]byte{}
	s := scanner{b: obj}
	s.space()
	if !s.peek('{') {
		return out
	}
	for s.i++; ; {
		s.space()
		ks, ke, ok := s.string()
		if !ok {
			return out
		}
		s.space()
		if !s.peek(':') {
			return out
		}
		s.i++
		s.space()
		vs, ve, ok := s.value()
		if !ok {
			return out
		}
		var key string
		if json.Unmarshal(obj[ks:ke], &key) == nil {
			out[key] = obj[vs:ve]
		}
		s.space()
		if !s.peek(',') {
			return out
		}
		s.i++
	}
}

// tooDeep reports whether the JSON text t nests arrays and objects deeper
// than maxDepth. A text with no more brackets than that cannot, which the
// common case shows without a scan.
func tooDeep(t []byte) bool {
	if bytes.Count(t, []byte("["))+bytes.Count(t, []byte("{")) <= maxDepth {
		return false
	}
	_, col := depthPosition(t)
	return col > 0
}

// depthPosition returns the line and column (1-based, in bytes, as
// serde_json counts them) of the first opening bracket of t outside a
// string that goes past maxDepth, or 0, 0 when none does.
func depthPosition(t []byte) (line, col int) {
	depth, line, start := 0, 1, 0
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '"':
			// Skip the string, escapes included.
			for i++; i < len(t) && t[i] != '"'; i++ {
				if t[i] == '\\' {
					i++
				}
			}
		case '[', '{':
			depth++
			if depth > maxDepth {
				return line, i - start + 1
			}
		case ']', '}':
			depth--
		case '\n':
			line, start = line+1, i+1
		}
	}
	return 0, 0
}

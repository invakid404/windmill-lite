package expr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The program an evaluation runs on the engine (milestone B2b), around an
// expression's Source. An evaluation runs these scripts on one fresh
// context, in order, each a strict-mode global script named eval_script
// (JE-21, JE-50):
//
//  1. preludeContext: it converts each context value that the evaluation
//     binds as a host string into its JavaScript value, as Windmill's host
//     rebuilds it, in place, so that it stays a property of globalThis
//     (JE-10, JE-16), and defines the helpers variable, resource and
//     flow_user_state over the host functions, which it removes from
//     globalThis (JE-20).
//  2. preludeResults, when the evaluation binds the results lookup: the
//     lexical results proxy of JE-12 and JE-18, a constant.
//  3. preludeError, when the text mentions error: the lexical error of
//     JE-17, a let binding (JE-10).
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
// through to the evaluation unchanged. JSON.stringify is looked up when the
// reaction runs, so an expression can replace it; the pipeline reads what
// the reaction fulfils with as Windmill reads it back (Pipeline.evaluate).
const serialise = `.then((v) => JSON.stringify(v ?? null) ?? "null")`

// programSource is the program the engine compiles for e.
func programSource(e *Expr) string { return e.Source + serialise }

// preludeContext converts the bound context values and defines the
// helpers.
//
// A context value is rebuilt from its parsed text by ordinary property
// assignment, key by key, as Windmill's host rebuilds it (JE-16). That
// differs from JSON.parse's own properties only for a key named
// __proto__, which goes through the inherited setter: an object or null
// becomes the prototype, and any other value is dropped. The prelude
// rebuilds the values that hostRebuild names, which bind finds can hold
// such a key (mayHoldProto), and keeps JSON.parse's value for the rest.
//
// Each helper awaits its host function and parses the JSON text it
// answers; a host function's rejection is thrown again from the helper's
// own frame, as the Error of JE-20 with the API's text. The intrinsics the
// helpers call later (JSON.parse, Promise.prototype.then, Reflect.apply)
// are taken before the user's code runs, so its replacements do not reach
// them; Error, which the helpers construct, is looked up when they throw.
// The conversion functions come after the helpers, so that the helpers'
// lines are the ones E3a's stacks show.
var preludeContext = `{
  const parse = JSON.parse, hasOwn = Object.hasOwn, apply = Reflect.apply, then = Promise.prototype.then, rebuilt = hasOwn(globalThis, "` + hostRebuild + `") ? parse(globalThis.` + hostRebuild + `) : [];
  delete globalThis.` + hostRebuild + `; for (const name of ` + mustJSON(hostStringNames) + `) if (hasOwn(globalThis, name)) globalThis[name] = convert(name, globalThis[name]);
  const [getVariable, getResource, getFlowUserState] = ` + mustJSON([]string{hostVariable, hostResource, hostFlowUserState}) + `.map((name) => { const fn = globalThis[name]; delete globalThis[name]; return fn; });
  const settle = (promise) => apply(then, promise, [undefined, (failure) => [failure.message]]);
  const variable = async (path) => { const outcome = await settle(getVariable(path));
    if (typeof outcome !== "string") { throw new Error(outcome[0]); } return parse(outcome); };
  const resource = async (path) => { const outcome = await settle(getResource(path));
    if (typeof outcome !== "string") { throw new Error(outcome[0]); } return parse(outcome); };
  const flow_user_state = async (key) => { const outcome = await settle(getFlowUserState(key));
    if (typeof outcome !== "string") { throw new Error(outcome[0]); } return parse(outcome); };
  Object.assign(globalThis, { variable, resource, flow_user_state });
  function convert(name, text) {
    const value = parse(text);
    return rebuilt.includes(name) ? rebuild(value) : value;
  }
  function rebuild(v) {
    if (typeof v !== "object" || v === null) return v;
    if (Array.isArray(v)) { for (let i = 0; i < v.length; i++) v[i] = rebuild(v[i]); return v; }
    const o = {};
    for (const k of Object.keys(v)) o[k] = rebuild(v[k]);
    return o;
  }
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
// decision list. It is a let binding, which the expression may assign; an
// entry's tag and text fall back on falsy values (JE-10, JE-17).
const preludeError = `let error = (() => {
  const p = globalThis.previous_result;
  if (p != null && p.error) return p.error;
  if (Array.isArray(p)) {
    const found = p.filter((item) => typeof item === "object" && item !== null && "error" in item);
    if (found.length === 1) return found[0].error;
    if (found.length === 0) return { name: "MultipleErrors", message: "Could not parse errors", errors: p };
    const entry = (item, i) => "[" + (item.error?.step_id || i) + "] " + (item.error?.message || item.error?.name);
    return { name: "MultipleErrors", message: found.map(entry).join("; "), errors: p };
  }
  if (p) return { name: "UnknownError", message: "Could not parse the error", error: p };
  return { name: "UnknownError", message: "No error found" };
})();
`

// mayHoldProto reports whether the JSON text t can hold a key named
// __proto__: one holds __proto literally unless it is written with \u
// escapes.
func mayHoldProto(t []byte) bool {
	return bytes.Contains(t, []byte("__proto")) || bytes.Contains(t, []byte(`\u`))
}

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
	//   - QuickJS evaluation error: Error:, then a space and the message
	//     when the Error has one, then a newline and the stack when it has
	//     one, for a thrown Error (JE-21); a message or stack that is not
	//     UTF-8 counts as none (JE-52);
	//   - QuickJS evaluation error: Exception generated by quickjs: <debug
	//     form> and a newline, for another thrown value (JE-21);
	//   - the memory-limit message of JE-3, for running out of memory or a
	//     thrown null or undefined;
	//   - the time-limit message of JE-2, after the last attempt;
	//   - serde_json's recursion limit message, for a context value nested
	//     too deeply (JE-49);
	//   - windmill-lite's own text for a call rewrite that Windmill's
	//     evaluator panics on (JE-11).
	Message string
	// Err is the cause: the engine's *Exception, ErrOutOfMemory,
	// ErrAttemptTimeout, ErrTooDeep or ErrRewritePanic.
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

// ErrRewritePanic is the cause of an evaluation whose text has a helper
// call that the rewrite of JE-11 would close inside a multi-byte
// character, where Windmill's evaluator panics (Expr.RewritePanic).
var ErrRewritePanic = errors.New("expr: the helper-call rewrite panics on this text")

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
			// rquickjs's display of an exception: the message and the stack
			// each when it reads one, and it reads only UTF-8 (JE-52).
			msg := "QuickJS evaluation error: Error:"
			if ex.HasMessage && utf8.ValidString(ex.Message) {
				msg += " " + ex.Message
			}
			if ex.HasStack && utf8.ValidString(ex.Stack) {
				msg += "\n" + ex.Stack
			}
			return &EvalError{Message: msg, Err: ex}
		default:
			return &EvalError{Message: "QuickJS evaluation error: Exception generated by quickjs: " + debugForm(ex.Value) + "\n", Err: ex}
		}
	case errors.Is(err, ErrAttemptTimeout):
		return &EvalError{Message: timeoutMessage(e, l), Err: ErrAttemptTimeout}
	}
	return err
}

// rewritePanicMessage is windmill-lite's text for an evaluation that
// reaches the engine with a text whose call rewrite Windmill's evaluator
// panics on (JE-11); what the panic does to Windmill's job is not traced
// (Q-JE-13).
func rewritePanicMessage(e *Expr) string {
	return fmt.Sprintf("Expression evaluation failed: the rewrite of its helper calls inserts a parenthesis at byte %d of `%s`, inside a character, where Windmill's evaluator panics", e.RewritePanic, e.Trimmed)
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

// checkDepth returns JE-49's error for the first value of s that the
// evaluation binds and that nests arrays and objects deeper than maxDepth.
// Windmill converts the bound values one at a time, in this order:
//
//  1. the context values the text names, in the scope's order (a hash map's
//     on Windmill, so any order is one it can take);
//  2. previous_result, when only the text's error or a spelling of the
//     previous id bound it (JE-10);
//  3. flow_input;
//  4. each member of flow_env, in its text's order (a hash map's again).
//
// Such a value fails before any JavaScript runs, with serde_json's text and
// the line and column of the bracket that goes past the limit, counted in
// the text serde_json reads: a context value's and a flow_env member's own
// text as the scope holds it, and for flow_input the object written out
// again compactly, its members' texts copied in, so the position counts
// the keys before the deep member.
func checkDepth(e *Expr, s *Scope, in Input) error {
	bound := map[string][]byte{}
	for _, hs := range in.Strings {
		bound[hs.Name] = hs.Text
	}
	check := func(t []byte, read func([]byte) []byte) error {
		if !tooDeep(t) {
			return nil
		}
		line, col := depthPosition(read(t))
		return &EvalError{Message: fmt.Sprintf("recursion limit exceeded at line %d column %d", line, col), Err: ErrTooDeep}
	}
	held := func(t []byte) []byte { return t }
	done := map[string]bool{}
	for _, n := range s.Context {
		t, ok := bound[n.Name]
		if !ok || done[n.Name] || !strings.Contains(e.Trimmed, n.Name) {
			continue
		}
		done[n.Name] = true
		if err := check(t, held); err != nil {
			return err
		}
	}
	if t, ok := bound["previous_result"]; ok && !done["previous_result"] {
		if err := check(t, held); err != nil {
			return err
		}
	}
	if t, ok := bound["flow_input"]; ok {
		if err := check(t, rewrittenObject); err != nil {
			return err
		}
	}
	if t, ok := bound["flow_env"]; ok && tooDeep(t) {
		for _, m := range objectMembers(t) {
			if err := check(m.value, held); err != nil {
				return err
			}
		}
	}
	return nil
}

// objectMember is one member of a JSON object: its decoded key and its
// value's text verbatim.
type objectMember struct {
	key   string
	value []byte
}

// objectMembers returns the members of the JSON object text obj in their
// order; of duplicate keys the first's place and the last's value are
// kept, as an insertion-ordered map keeps them. It is nil when obj is not
// an object, and the members read up to an error when the object is
// malformed. It scans bytes, so no depth limits it.
func objectMembers(obj []byte) []objectMember {
	s := scanner{b: obj}
	s.space()
	if !s.peek('{') {
		return nil
	}
	out := []objectMember{}
	at := map[string]int{}
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
			if i, seen := at[key]; seen {
				out[i].value = obj[vs:ve:ve]
			} else {
				at[key] = len(out)
				out = append(out, objectMember{key, obj[vs:ve:ve]})
			}
		}
		s.space()
		if !s.peek(',') {
			return out
		}
		s.i++
	}
}

// rewrittenObject is the JSON object text obj as Windmill's evaluator
// writes the flow input out again before it parses it (JE-49): compactly,
// {"<key>":<value>,...}, each key escaped as serde_json escapes strings and
// each value's text copied in as held. Text that is not an object is
// returned as it is.
func rewrittenObject(obj []byte) []byte {
	ms := objectMembers(obj)
	if ms == nil {
		return obj
	}
	out := make([]byte, 0, len(obj))
	out = append(out, '{')
	for i, m := range ms {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendSerdeString(out, m.key)
		out = append(out, ':')
		out = append(out, m.value...)
	}
	return append(out, '}')
}

// appendSerdeString appends s as a JSON string as serde_json writes one: a
// quotation mark and a backslash escaped, the control characters as \b,
// \t, \n, \f, \r or \u00xx, and every other character as it is.
func appendSerdeString(out []byte, s string) []byte {
	const hex = "0123456789abcdef"
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			out = append(out, '\\', c)
		case c == '\b':
			out = append(out, '\\', 'b')
		case c == '\t':
			out = append(out, '\\', 't')
		case c == '\n':
			out = append(out, '\\', 'n')
		case c == '\f':
			out = append(out, '\\', 'f')
		case c == '\r':
			out = append(out, '\\', 'r')
		case c < 0x20:
			out = append(out, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
		default:
			out = append(out, c)
		}
	}
	return append(out, '"')
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

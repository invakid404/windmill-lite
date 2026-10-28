package expr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// ErrNotCompiled reports an Expr that Compile did not fill: one that holds
// only its site and text, as internal/ir writes it.
var ErrNotCompiled = errors.New("expr: expression is not compiled")

// The host functions an evaluation binds (Engine, Input). Each answers with
// JSON text, which the program parses: the results lookup of JE-18 and the
// helpers of JE-20. The program gives the user's code its own names for
// them (B2b).
const (
	hostResults       = "__wl_results"
	hostVariable      = "__wl_variable"
	hostResource      = "__wl_resource"
	hostFlowUserState = "__wl_flow_user_state"
)

var jsonNull = []byte("null")

// Pipeline is the Evaluator: it answers an expression with the shortcuts of
// JE-6 to JE-9 when one fires, and otherwise evaluates its source on the
// Engine, with the scope's values bound as JE-10 says (ADR 0003). It is
// safe for concurrent use.
//
// The engine's result text is returned as is, and its errors are returned
// unwrapped: the program around the source (the binding prelude and the
// serialisation of JE-15) and Windmill's error texts are milestone B2b's.
type Pipeline struct {
	engine Engine

	mu       sync.Mutex
	programs map[string]compiled
}

// compiled is the Engine's answer for one source: its program, or the
// exception that the source raised when it was compiled.
type compiled struct {
	program Program
	err     error
}

// NewPipeline returns a Pipeline that evaluates on engine.
func NewPipeline(engine Engine) *Pipeline {
	return &Pipeline{engine: engine, programs: map[string]compiled{}}
}

// Prepare compiles the sources of exprs on the engine, as a bundle opens
// (ADR 0003). A source that does not compile is recorded, and Eval raises
// its error each time the expression reaches the engine, as Windmill
// reports it; Prepare itself never fails. Programs are kept by source text
// for the Pipeline's life, so a Pipeline serves one bundle's expressions.
func (p *Pipeline) Prepare(exprs ...*Expr) {
	for _, e := range exprs {
		if e != nil && e.Mode != 0 {
			p.program(e.Source)
		}
	}
}

// program returns the compiled program of source, compiling it on first
// use. Two callers that meet a new source at once may both compile it; the
// programs are equivalent and the later one is kept. A compile error other
// than an *Exception (a closed engine, say) is not kept, so a later call
// tries again.
func (p *Pipeline) program(source string) (Program, error) {
	p.mu.Lock()
	c, ok := p.programs[source]
	p.mu.Unlock()
	if ok {
		return c.program, c.err
	}
	prog, err := p.engine.Compile(source)
	var ex *Exception
	if err == nil || errors.As(err, &ex) {
		p.mu.Lock()
		p.programs[source] = compiled{prog, err}
		p.mu.Unlock()
	}
	return prog, err
}

// Eval evaluates a compiled expression in a scope. It returns the producer
// text of the value: a shortcut's text, or the engine's.
//
// The shortcuts are tried in the order JE-6, JE-7, JE-8, JE-9, and the
// first that fires answers without the engine:
//
//   - JE-6: the text is a context name the scope holds; its value verbatim.
//   - JE-7: the text names a key that the flow input (or the flow env)
//     has; the member's text verbatim.
//   - JE-8: the whole text is one of the four spellings of the scope's
//     previous id, and the scope has a results context and
//     previous_result; previous_result verbatim.
//   - JE-9: a line of the text is a results path that does not decline,
//     and the scope has a results context; the path applied to the step's
//     stored result as #> applies it, in jsonb's text output, or null when
//     anything is missing. JE-9 needs an authenticated client too, but
//     every site with a results context has one (JE-22), so the results
//     context alone decides.
//
// A context that ends while a shortcut reads the results context (JE-9) is
// reported as its cause, not as the lookup's null.
func (p *Pipeline) Eval(ctx context.Context, e *Expr, s *Scope) (jsonval.Value, error) {
	if err := context.Cause(ctx); err != nil {
		return jsonval.Value{}, err
	}
	if e == nil || e.Mode == 0 {
		return jsonval.Value{}, ErrNotCompiled
	}
	if s == nil {
		s = &Scope{}
	}
	if v, r := shortcut(ctx, e, s); r != routeEngine {
		if err := context.Cause(ctx); err != nil {
			return jsonval.Value{}, err
		}
		return v, nil
	}
	prog, err := p.program(e.Source)
	if err != nil {
		return jsonval.Value{}, err
	}
	out, err := p.engine.Eval(ctx, prog, bind(e, s))
	if err != nil {
		return jsonval.Value{}, err
	}
	return jsonval.FromProducer(out), nil
}

// route is what answered an evaluation: one of the shortcuts, named as its
// Shortcut field is in JSON, or the engine.
type route string

const (
	routeEngine      route = ""
	routeContextName route = "context_name"
	routeFlowKey     route = "flow_key"
	routePreviousID  route = "previous_id"
	routeResultsPath route = "results_path"
)

// shortcut answers e from s when one of JE-6 to JE-9 fires, and says which.
func shortcut(ctx context.Context, e *Expr, s *Scope) (jsonval.Value, route) {
	sc := e.Shortcut
	if sc.ContextName != "" {
		if v, ok := s.context(sc.ContextName); ok {
			return v, routeContextName
		}
	}
	if k := sc.FlowKey; k != nil {
		obj := s.FlowInput
		if k.Env {
			obj = s.FlowEnv
		}
		if !obj.IsZero() {
			if m, ok := member(obj.Producer(), k.Key); ok {
				return jsonval.FromProducer(m), routeFlowKey
			}
		}
	}
	if sc.PreviousID != nil && isSpelling(e.Trimmed, s.PreviousID) && s.Results != nil {
		if v, ok := s.context("previous_result"); ok {
			return v, routePreviousID
		}
	}
	if rp := sc.ResultsPath; rp != nil && !rp.Declines && s.Results != nil {
		return jsonval.FromProducer(lookupPath(ctx, s.Results, rp)), routeResultsPath
	}
	return jsonval.Value{}, routeEngine
}

// lookupPath is JE-9's lenient lookup: the step's stored result, with the
// optional index and the segments applied, in jsonb's text output. A
// missing step, an unusable stored text, a missing key, a type mismatch and
// a null intermediate all give null.
func lookupPath(ctx context.Context, r Results, rp *ResultsPath) []byte {
	v, ok := r.Stored(ctx, rp.Step)
	if !ok || v.IsZero() {
		return jsonNull
	}
	stored, err := v.Stored()
	if err != nil {
		return jsonNull
	}
	path := rp.Segments
	if rp.Index != "" {
		path = append([]string{rp.Index}, path...)
	}
	sub, ok := walk(stored, path)
	if !ok {
		return jsonNull
	}
	return databaseText(sub)
}

// context returns the scope's context value name; the first of repeated
// names counts.
func (s *Scope) context(name string) (jsonval.Value, bool) {
	for _, n := range s.Context {
		if n.Name == name && !n.Value.IsZero() {
			return n.Value, true
		}
	}
	return jsonval.Value{}, false
}

// bind returns what an evaluation of e in s binds (JE-10):
//
//   - a host string for each context name the text mentions and the scope
//     holds, and for previous_result also when the text mentions error
//     (JE-17) or spells the previous id (JE-8);
//   - flow_input, when mentioned: the flow input, or null at a site without
//     one; flow_env, when mentioned: the flow env, or {};
//   - the results lookup, when the text mentions results and the scope has
//     a results context;
//   - the helpers, always.
//
// The error helper's value is derived from previous_result by the program
// (B2b), so error binds no host value of its own.
func bind(e *Expr, s *Scope) Input {
	names := e.Binds
	if !slices.Contains(names, "previous_result") && spellsPrevious(e.Trimmed, s.PreviousID) {
		names = append(slices.Clone(names), "previous_result")
		slices.Sort(names)
	}
	b := &binding{scope: s}
	var in Input
	for _, n := range names {
		switch n {
		case "flow_input":
			in.Strings = append(in.Strings, HostString{n, textOr(s.FlowInput, jsonNull)})
		case "flow_env":
			in.Strings = append(in.Strings, HostString{n, textOr(s.FlowEnv, []byte("{}"))})
		case "results":
			if s.Results != nil {
				in.Funcs = append(in.Funcs, HostFunc{hostResults, b.results})
			}
		case "error":
		default:
			if v, ok := s.context(n); ok {
				in.Strings = append(in.Strings, HostString{n, v.Producer()})
				if n == "previous_result" {
					b.previous, b.memory = v, true
				}
			}
		}
	}
	in.Funcs = append(in.Funcs,
		HostFunc{hostVariable, b.variable},
		HostFunc{hostResource, b.resource},
		HostFunc{hostFlowUserState, b.flowUserState},
	)
	return in
}

func textOr(v jsonval.Value, def []byte) []byte {
	if v.IsZero() {
		return def
	}
	return v.Producer()
}

// binding answers the host functions of one evaluation.
type binding struct {
	scope *Scope
	// previous is previous_result, and memory is whether the evaluation
	// binds it.
	previous jsonval.Value
	memory   bool
}

// results answers results.<id> on the engine path (JE-18): the previous
// id from memory when previous_result is bound (step 1, the reading 0001
// §6 records for FX-V6), and otherwise the results context's lookup, a
// database read, in stored text (steps 2 and 3). The lookup's error is the
// read's failure, with its message.
func (b *binding) results(ctx context.Context, id string) ([]byte, error) {
	if b.memory && id == b.scope.PreviousID {
		return b.previous.Producer(), nil
	}
	v, err := b.scope.Results.Lookup(ctx, id)
	if err != nil {
		return nil, err
	}
	if v.IsZero() {
		return jsonNull, nil
	}
	stored, err := v.Stored()
	if err != nil {
		return nil, fmt.Errorf("expr: the result of %q has no stored form: %w", id, err)
	}
	return stored, nil
}

// variable answers variable(path) (JE-20): the value, a string, as JSON
// text.
func (b *binding) variable(ctx context.Context, path string) ([]byte, error) {
	h := b.scope.Helpers
	if h == nil {
		return nil, errors.New("variable() is not available without an authenticated client")
	}
	v, err := h.Variable(ctx, path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// resource answers resource(path) (JE-20).
func (b *binding) resource(ctx context.Context, path string) ([]byte, error) {
	h := b.scope.Helpers
	if h == nil {
		return nil, errors.New("resource() is not available without an authenticated client")
	}
	v, err := h.Resource(ctx, path)
	if err != nil {
		return nil, err
	}
	return textOr(v, jsonNull), nil
}

// flowUserState answers flow_user_state(key) (JE-20), which needs a results
// context as well as a client.
func (b *binding) flowUserState(ctx context.Context, key string) ([]byte, error) {
	h := b.scope.Helpers
	switch {
	case h == nil:
		return nil, errors.New("flow_user_state() is not available without an authenticated client")
	case b.scope.Results == nil:
		return nil, errors.New("flow_user_state() is not available in this context")
	}
	v, err := h.FlowUserState(ctx, key)
	if err != nil {
		return nil, err
	}
	return textOr(v, jsonNull), nil
}

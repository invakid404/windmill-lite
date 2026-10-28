package expr

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// fakeEngine is the Engine the pipeline's tests run on (ADR 0012, B2a): it
// records what it compiles and the inputs of each evaluation, and answers
// with eval when set, or with fakeAnswer.
type fakeEngine struct {
	mu       sync.Mutex
	compiled []string
	calls    []fakeCall
	// compile, when set, answers Compile instead of a program.
	compile func(source string) error
	// eval, when set, answers Eval.
	eval func(ctx context.Context, source string, in Input) ([]byte, error)
}

// fakeAnswer is what the fake engine answers by default, a text no
// shortcut gives.
const fakeAnswer = `"from the fake engine"`

type fakeCall struct {
	source string
	in     Input
}

type fakeProgram struct{ source string }

func (f *fakeEngine) Compile(source string) (Program, error) {
	f.mu.Lock()
	f.compiled = append(f.compiled, source)
	f.mu.Unlock()
	if f.compile != nil {
		if err := f.compile(source); err != nil {
			return nil, err
		}
	}
	return fakeProgram{source}, nil
}

func (f *fakeEngine) Eval(ctx context.Context, p Program, in Input) ([]byte, error) {
	fp, ok := p.(fakeProgram)
	if !ok {
		return nil, errors.New("fake engine: not its program")
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{fp.source, in})
	f.mu.Unlock()
	if f.eval != nil {
		return f.eval(ctx, fp.source, in)
	}
	return []byte(fakeAnswer), nil
}

func (f *fakeEngine) Limits() Limits { return DefaultLimits() }

// isPrelude reports whether source is one of the pipeline's preludes.
func isPrelude(source string) bool {
	return source == preludeContext || source == preludeResults || source == preludeError
}

// programs returns the sources compiled that are not preludes.
func (f *fakeEngine) programs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.compiled {
		if !isPrelude(s) {
			out = append(out, s)
		}
	}
	return out
}

// lastCall returns the last evaluation, and false when there was none.
func (f *fakeEngine) lastCall() (fakeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// strings returns the host strings of an evaluation by name.
func (c fakeCall) strings() map[string]string {
	out := map[string]string{}
	for _, s := range c.in.Strings {
		out[s.Name] = string(s.Text)
	}
	return out
}

// fn returns the host function name, or nil.
func (c fakeCall) fn(name string) func(context.Context, string) ([]byte, error) {
	for _, f := range c.in.Funcs {
		if f.Name == name {
			return f.Call
		}
	}
	return nil
}

// fakeResults answers results lookups from stored texts by module id, as a
// database read returns them, and records the ids it is asked for.
type fakeResults struct {
	mu     sync.Mutex
	stored map[string]string
	// fail, when set, fails Lookup for its ids with its messages.
	fail  map[string]string
	asked []string
}

func (r *fakeResults) Lookup(_ context.Context, id string) (jsonval.Value, error) {
	r.mu.Lock()
	r.asked = append(r.asked, "lookup:"+id)
	r.mu.Unlock()
	if msg, ok := r.fail[id]; ok {
		return jsonval.Value{}, errors.New(msg)
	}
	if t, ok := r.stored[id]; ok {
		return jsonval.FromProducer([]byte(t)), nil
	}
	return jsonval.FromProducer([]byte("null")), nil
}

func (r *fakeResults) Stored(_ context.Context, id string) (jsonval.Value, bool) {
	r.mu.Lock()
	r.asked = append(r.asked, "stored:"+id)
	r.mu.Unlock()
	t, ok := r.stored[id]
	if !ok {
		return jsonval.Value{}, false
	}
	return jsonval.FromProducer([]byte(t)), true
}

// fakeHelpers answers the helpers of JE-20 from tables.
type fakeHelpers struct {
	variables map[string]string
	resources map[string]string
	state     map[string]string
}

func (h *fakeHelpers) Variable(_ context.Context, path string) (string, error) {
	if v, ok := h.variables[path]; ok {
		return v, nil
	}
	return "", errors.New("Variable not found: " + path)
}

func (h *fakeHelpers) Resource(_ context.Context, path string) (jsonval.Value, error) {
	if v, ok := h.resources[path]; ok {
		return jsonval.FromProducer([]byte(v)), nil
	}
	return jsonval.Value{}, errors.New("Resource not found: " + path)
}

func (h *fakeHelpers) FlowUserState(_ context.Context, key string) (jsonval.Value, error) {
	if v, ok := h.state[key]; ok {
		return jsonval.FromProducer([]byte(v)), nil
	}
	return jsonval.FromProducer([]byte("null")), nil
}

// unwrap returns the rewritten code inside a source that wrap built, and
// false for any other source.
func unwrap(m Mode, source string) (string, bool) {
	open, close := wrapOpen+wrapReturn, wrapCloseExpr
	if m == ModeStatement {
		open, close = wrapOpen, wrapCloseStatement
	}
	code, ok := strings.CutPrefix(source, open)
	if !ok {
		return "", false
	}
	return strings.CutSuffix(code, close)
}

func value(t string) jsonval.Value { return jsonval.FromProducer([]byte(t)) }

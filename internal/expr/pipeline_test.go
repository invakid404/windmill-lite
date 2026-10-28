package expr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

var _ Evaluator = (*Pipeline)(nil)

func TestEvalChecksContextFirst(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("run canceled")
	cancel(cause)
	eng := &fakeEngine{}
	_, err := NewPipeline(eng).Eval(ctx, Compile(SiteInputTransform, "previous_result"), &Scope{Context: []Named{{"previous_result", value("1")}}})
	if !errors.Is(err, cause) {
		t.Fatalf("Eval on a canceled context: %v, want the cause", err)
	}
	if len(eng.compiled)+len(eng.calls) != 0 {
		t.Error("the engine was used")
	}
}

func TestEvalNeedsACompiledExpr(t *testing.T) {
	for _, e := range []*Expr{{Site: SiteInputTransform, Text: "1"}, nil} {
		_, err := NewPipeline(&fakeEngine{}).Eval(context.Background(), e, nil)
		if !errors.Is(err, ErrNotCompiled) {
			t.Fatalf("Eval of %v: %v", e, err)
		}
	}
}

// cancellingResults ends the run's context while JE-9 reads it.
type cancellingResults struct {
	fakeResults
	cancel context.CancelCauseFunc
	cause  error
}

func (r *cancellingResults) Stored(context.Context, string) (jsonval.Value, bool) {
	r.cancel(r.cause)
	return jsonval.Value{}, false
}

// TestShortcutCanceled: a context that ends during JE-9's read is the
// evaluation's error, not the lookup's null, which would otherwise be
// recorded as a value.
func TestShortcutCanceled(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	r := &cancellingResults{cancel: cancel, cause: errors.New("run canceled")}
	v, err := NewPipeline(&fakeEngine{}).Eval(ctx, Compile(SiteInputTransform, "results.a.b"), &Scope{PreviousID: "m", Results: r})
	if !errors.Is(err, r.cause) || !v.IsZero() {
		t.Fatalf("Eval: %s, %v; want the cause", v, err)
	}
}

// TestCompileErrorRaisedAtEval follows ADR 0003: a program that does not
// compile is recorded when the bundle opens and its error is raised, in
// Windmill's text (JE-21), each time the expression reaches the engine; it
// is compiled once.
func TestCompileErrorRaisedAtEval(t *testing.T) {
	syntax := &Exception{Thrown: ThrownError, Name: "SyntaxError", Message: "unexpected token", Text: "SyntaxError: unexpected token", Stack: "    at eval_script:1:7\n"}
	eng := &fakeEngine{compile: func(src string) error {
		if isPrelude(src) {
			return nil
		}
		return syntax
	}}
	p := NewPipeline(eng)
	e := Compile(SiteInputTransform, "flow_input.n // the count")
	p.Prepare(e, nil, &Expr{})
	for range 2 {
		_, err := p.Eval(context.Background(), e, &Scope{FlowInput: value(`{"n":5}`)})
		var ee *EvalError
		if !errors.As(err, &ee) || ee.Err != syntax || ee.Message != "QuickJS evaluation error: Error: unexpected token\n    at eval_script:1:7\n" {
			t.Fatalf("Eval: %q, want the compile exception in JE-21's text", err)
		}
	}
	if n := len(eng.programs()); n != 1 || len(eng.calls) != 0 {
		t.Errorf("compiled %d times and evaluated %d times; want 1 and 0", n, len(eng.calls))
	}
	// A shortcut still answers: the engine's error is raised only when the
	// expression reaches it.
	v, err := p.Eval(context.Background(), e, &Scope{FlowInput: value(`{"n // the count":5}`)})
	if err != nil || string(v.Producer()) != "5" {
		t.Errorf("the shortcut gave %s, %v", v, err)
	}
}

// TestCompileFailureNotKept: a compile error that is not the source's own
// (a closed engine) is not recorded, so a later evaluation compiles again.
// A prelude that does not compile is reported as such.
func TestCompileFailureNotKept(t *testing.T) {
	closed := errors.New("engine closed")
	fail := func(string) bool { return true }
	eng := &fakeEngine{compile: func(src string) error {
		if fail(src) {
			return closed
		}
		return nil
	}}
	p := NewPipeline(eng)
	e := Compile(SiteInputTransform, "1 + 1")
	if _, err := p.Eval(context.Background(), e, nil); !errors.Is(err, closed) || !strings.Contains(err.Error(), "prelude") {
		t.Fatalf("Eval: %v", err)
	}
	fail = func(src string) bool { return !isPrelude(src) }
	if _, err := p.Eval(context.Background(), e, nil); !errors.Is(err, closed) {
		t.Fatalf("Eval: %v", err)
	}
	fail = func(string) bool { return false }
	if v, err := p.Eval(context.Background(), e, nil); err != nil || string(v.Producer()) != fakeAnswer {
		t.Fatalf("Eval after the engine recovered: %s, %v", v, err)
	}
	if n := len(eng.programs()); n != 2 {
		t.Errorf("compiled %d times, want 2", n)
	}
}

func TestPrepareCompilesEachSourceOnce(t *testing.T) {
	eng := &fakeEngine{}
	p := NewPipeline(eng)
	a, b := Compile(SiteInputTransform, "1 + 1"), Compile(SiteInputTransform, " 1 + 1 ")
	p.Prepare(a, b)
	for _, e := range []*Expr{a, b, a} {
		if _, err := p.Eval(context.Background(), e, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(eng.programs()); n != 1 || len(eng.calls) != 3 || len(eng.compiled) != 4 {
		t.Errorf("compiled %d programs and %d sources, evaluated %d times; want 1, 4 (with the three preludes) and 3", n, len(eng.compiled), len(eng.calls))
	}
}

// TestEngineErrorsBecomeWindmillTexts: the engine's exceptions and limits
// become Windmill's texts, with the engine's error as the cause (JE-2,
// JE-3, JE-21); a context that ends is not the evaluation's failure, and
// passes through.
func TestEngineErrorsBecomeWindmillTexts(t *testing.T) {
	ended := errors.New("run canceled")
	memory := "The expression evaluation exceeded the memory limit of 128 MB. Reduce the amount of data handled in the expression, or raise the cap via the QUICKJS_MEMORY_LIMIT_MB environment variable."
	for _, tc := range []struct {
		err   error
		cause error
		text  string
	}{
		{&Exception{Thrown: ThrownNull, Text: "null"}, ErrOutOfMemory, memory},
		{&Exception{Thrown: ThrownUndefined, Text: "undefined"}, ErrOutOfMemory, memory},
		{&Exception{Thrown: ThrownError, Name: "InternalError", Message: "out of memory"}, ErrOutOfMemory, memory},
		{&Exception{Thrown: ThrownError, Name: "Error", Message: "out of memory", Stack: "    at <eval> (eval_script:1:28)\n"}, nil, "QuickJS evaluation error: Error: out of memory\n    at <eval> (eval_script:1:28)\n"},
		{&Exception{Thrown: ThrownError, Name: "TypeError", Message: "not a function"}, nil, "QuickJS evaluation error: Error: not a function"},
		{&Exception{Thrown: ThrownOther, Text: "x"}, nil, "QuickJS evaluation error: Exception generated by quickjs: x\n"},
		{ErrAttemptTimeout, ErrAttemptTimeout, "The expression evaluation `x` took too long to execute (>20000ms)"},
		{ErrNotString, ErrNotString, "QuickJS evaluation error: the serialised value is not a string"},
	} {
		eng := &fakeEngine{eval: func(context.Context, string, Input) ([]byte, error) { return nil, tc.err }}
		p := NewPipeline(eng)
		setRetry(p, 3, 0)
		_, err := p.Eval(context.Background(), Compile(SiteInputTransform, " x "), nil)
		var ee *EvalError
		if !errors.As(err, &ee) || ee.Message != tc.text {
			t.Errorf("%v: Eval: %q, want %q", tc.err, err, tc.text)
			continue
		}
		cause := tc.cause
		if cause == nil {
			cause = tc.err
		}
		if !errors.Is(err, cause) {
			t.Errorf("%v: the cause is %v, want %v", tc.err, ee.Err, cause)
		}
	}
	eng := &fakeEngine{eval: func(context.Context, string, Input) ([]byte, error) { return nil, ended }}
	if _, err := NewPipeline(eng).Eval(context.Background(), Compile(SiteInputTransform, "x"), nil); err != ended {
		t.Errorf("Eval: %v, want the context's cause unwrapped", err)
	}
	// A completion that is not one JSON value fails, once.
	eng = &fakeEngine{eval: func(context.Context, string, Input) ([]byte, error) { return []byte(`1, "x": 2`), nil }}
	_, err := NewPipeline(eng).Eval(context.Background(), Compile(SiteInputTransform, "x"), nil)
	if !errors.Is(err, ErrNotJSON) || err.Error() != "QuickJS evaluation error: the serialised value is not JSON" || len(eng.calls) != 1 {
		t.Errorf("Eval: %v after %d attempts", err, len(eng.calls))
	}
}

// TestTimeoutRetried follows JE-2: an evaluation whose error text contains
// "took too long" is tried three times in all, with a pause between
// attempts, whether it is the time-limit message or the user's own; any
// other error is not tried again.
func TestTimeoutRetried(t *testing.T) {
	user := &Exception{Thrown: ThrownError, Name: "Error", Message: "the query took too long"}
	for _, tc := range []struct {
		err   error
		tries int
	}{
		{ErrAttemptTimeout, 3},
		{user, 3},
		{&Exception{Thrown: ThrownError, Name: "Error", Message: "too long"}, 1},
		{&Exception{Thrown: ThrownNull}, 1},
	} {
		eng := &fakeEngine{eval: func(context.Context, string, Input) ([]byte, error) { return nil, tc.err }}
		p := NewPipeline(eng)
		setRetry(p, 3, time.Millisecond)
		_, err := p.Eval(context.Background(), Compile(SiteSkipIf, "x"), nil)
		if len(eng.calls) != tc.tries || err == nil {
			t.Errorf("%v: %d attempts, %v; want %d", tc.err, len(eng.calls), err, tc.tries)
		}
		if tc.err == ErrAttemptTimeout && err.Error() != "The expression evaluation `Boolean(x)` took too long to execute (>20000ms)" {
			t.Errorf("the predicate's message quotes its wrapped text: %q", err)
		}
	}
	// The pause ends early when the run does.
	ctx, cancel := context.WithCancelCause(context.Background())
	eng := &fakeEngine{eval: func(context.Context, string, Input) ([]byte, error) {
		cancel(errors.New("run canceled"))
		return nil, ErrAttemptTimeout
	}}
	p := NewPipeline(eng)
	setRetry(p, 3, time.Hour)
	if _, err := p.Eval(ctx, Compile(SiteInputTransform, "x"), nil); err == nil || err.Error() != "run canceled" || len(eng.calls) != 1 {
		t.Errorf("Eval: %v after %d attempts, want the cause after 1", err, len(eng.calls))
	}
}

// TestResultsLookupFromMemory covers the host side of JE-18's proxy: the
// previous id answers previous_result's text from memory when
// previous_result is bound (by its name, a spelling of the previous id or
// error: the reading 0001 §6 records for FX-V6, Q-FX-15), and otherwise,
// like any other id, the results context's database read, in stored text.
func TestResultsLookupFromMemory(t *testing.T) {
	producer := `{"b":1,"a":2}`
	for _, tc := range []struct {
		text   string
		memory bool
	}{
		{"(results.a)", true},                   // spec: JE-18 rule 1, JE-8 spelling
		{"Object.keys(results?.a)", true},       // spec: JE-10, a spelling inside the text
		{`results["a"].b`, true},                // spec: JE-10, the bracket spelling
		{"previous_result && results[k]", true}, // spec: FX-V6, previous_result named
		{"error && results[k]", true},           // spec: FX-V6, error binds previous_result
		{"results[k]", false},                   // spec: FX-V6, a bare computed read
		{"(results.ab)", true},                  // spec: JE-10, a substring is a spelling of a
	} {
		results := &fakeResults{stored: map[string]string{"a": `{"b": 1, "a": 2}`}}
		s := &Scope{PreviousID: "a", Context: []Named{{"previous_result", value(producer)}}, Results: results}
		var got string
		eng := &fakeEngine{eval: func(ctx context.Context, _ string, in Input) ([]byte, error) {
			lookup := fakeCall{in: in}.fn(hostResults)
			if lookup == nil {
				return nil, errors.New("no results lookup bound")
			}
			out, err := lookup(ctx, "a")
			got = string(out)
			return []byte("null"), err
		}}
		if _, err := NewPipeline(eng).Eval(context.Background(), Compile(SiteInputTransform, tc.text), s); err != nil {
			t.Fatalf("%q: %v", tc.text, err)
		}
		want, asked := producer, 0
		if !tc.memory {
			want, asked = `{"a":2,"b":1}`, 1
		}
		if got != want || len(results.asked) != asked {
			t.Errorf("%q: the lookup of the previous id answered %s after %d reads; want %s after %d", tc.text, got, len(results.asked), want, asked)
		}
	}
}

// TestResultsLookupErrors: a failed read rejects with the read's message
// (JE-18 rule 2), and an unknown id is the results context's to answer.
func TestResultsLookupErrors(t *testing.T) {
	results := &fakeResults{fail: map[string]string{"br": "Failed to fetch result for step 'br': Not found"}}
	s := &Scope{PreviousID: "m", Context: []Named{{"previous_result", value("0")}}, Results: results}
	var answers []string
	eng := &fakeEngine{eval: func(ctx context.Context, _ string, in Input) ([]byte, error) {
		lookup := fakeCall{in: in}.fn(hostResults)
		for _, id := range []string{"br", "nope"} {
			out, err := lookup(ctx, id)
			answers = append(answers, fmt.Sprintf("%s|%v", out, err))
		}
		return []byte("null"), nil
	}}
	if _, err := NewPipeline(eng).Eval(context.Background(), Compile(SiteInputTransform, "results.br.x + results.nope"), s); err != nil {
		t.Fatal(err)
	}
	want := []string{"|Failed to fetch result for step 'br': Not found", "null|<nil>"}
	if strings.Join(answers, "\n") != strings.Join(want, "\n") {
		t.Errorf("answers %q, want %q", answers, want)
	}
}

// TestHelpers covers JE-20's host side: each helper answers JSON text, an
// API error keeps its text, and without a client, or for flow_user_state
// without a results context, they reject with the "not available" texts.
func TestHelpers(t *testing.T) {
	helpers := &fakeHelpers{
		variables: map[string]string{"u/x/v": "s3cr\"t"},
		resources: map[string]string{"u/x/r": `{"host":"db"}`},
		state:     map[string]string{"k": "[1]"},
	}
	call := func(s *Scope, name, arg string) string {
		var answer string
		eng := &fakeEngine{eval: func(ctx context.Context, _ string, in Input) ([]byte, error) {
			out, err := fakeCall{in: in}.fn(name)(ctx, arg)
			answer = fmt.Sprintf("%s|%v", out, err)
			return []byte("null"), nil
		}}
		if _, err := NewPipeline(eng).Eval(context.Background(), Compile(SiteInputTransform, "variable(x)"), s); err != nil {
			t.Fatal(err)
		}
		return answer
	}
	full := &Scope{Results: &fakeResults{}, Helpers: helpers}
	for _, tc := range []struct {
		scope     *Scope
		name, arg string
		want      string
	}{
		{full, hostVariable, "u/x/v", `"s3cr\"t"|<nil>`},
		{full, hostVariable, "u/x/missing", "|Variable not found: u/x/missing"},
		{full, hostResource, "u/x/r", `{"host":"db"}|<nil>`},
		{full, hostResource, "u/x/missing", "|Resource not found: u/x/missing"},
		{full, hostFlowUserState, "k", "[1]|<nil>"},
		{full, hostFlowUserState, "unset", "null|<nil>"},
		{&Scope{Results: &fakeResults{}}, hostVariable, "u/x/v", "|variable() is not available without an authenticated client"},
		{&Scope{Results: &fakeResults{}}, hostResource, "u/x/r", "|resource() is not available without an authenticated client"},
		{&Scope{}, hostFlowUserState, "k", "|flow_user_state() is not available without an authenticated client"},
		{&Scope{Helpers: helpers}, hostFlowUserState, "k", "|flow_user_state() is not available in this context"},
	} {
		if got := call(tc.scope, tc.name, tc.arg); got != tc.want {
			t.Errorf("%s(%q): %s, want %s", tc.name, tc.arg, got, tc.want)
		}
	}
}

// TestConcurrentEval runs one Pipeline from many goroutines, shortcuts and
// the engine path mixed, for the race detector.
func TestConcurrentEval(t *testing.T) {
	eng := &fakeEngine{}
	p := NewPipeline(eng)
	s := &Scope{
		PreviousID: "a",
		Context:    []Named{{"previous_result", value(`{"n":1}`)}},
		FlowInput:  value(`{"k":"v"}`),
		Results:    &fakeResults{stored: map[string]string{"b": `{"c": [1, 2]}`}},
	}
	texts := []string{"results.a", "results.b.c", "flow_input.k", "previous_result", "1 + 1", "(results.a)"}
	exprs := make([]*Expr, len(texts))
	for i, text := range texts {
		exprs[i] = Compile(SiteInputTransform, text)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				e := exprs[(g+i)%len(exprs)]
				if _, err := p.Eval(context.Background(), e, s); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if n := len(eng.compiled); n > 2*8 {
		t.Errorf("compiled %d times for 2 engine sources", n)
	}
}

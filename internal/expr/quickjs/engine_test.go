package quickjs

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
)

// newEngine starts an engine that the test closes, and fails the test if
// any of its runtimes leaked a heap block.
func newEngine(t testing.TB, opts Options) *Engine {
	t.Helper()
	e := newLeakyEngine(t, opts)
	t.Cleanup(func() {
		if n := e.Stats().LeakedBlocks; n != 0 {
			t.Errorf("runtimes leaked %d heap blocks", n)
		}
	})
	return e
}

// newLeakyEngine is newEngine for tests that make QuickJS-ng leak.
func newLeakyEngine(t testing.TB, opts Options) *Engine {
	t.Helper()
	e, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func compile(t testing.TB, e *Engine, source string) expr.Program {
	t.Helper()
	p, err := e.Compile(source)
	if err != nil {
		t.Fatalf("Compile(%q): %v", source, err)
	}
	return p
}

func run(t testing.TB, e *Engine, source string, in expr.Input) (string, error) {
	t.Helper()
	out, err := e.Eval(context.Background(), compile(t, e, source), in)
	return string(out), err
}

func mustRun(t testing.TB, e *Engine, source string, in expr.Input) string {
	t.Helper()
	out, err := run(t, e, source, in)
	if err != nil {
		t.Fatalf("Eval(%q): %v", source, err)
	}
	return out
}

func shortLimits(attempt time.Duration) expr.Limits {
	l := expr.DefaultLimits()
	l.Attempt = attempt
	return l
}

func TestEvalReturnsTheCompletionString(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	for _, tc := range []struct{ source, want string }{
		{`"a" + "b"`, "ab"},
		{`(async () => "x")()`, "x"},
		{`Promise.resolve("p")`, "p"},
		{`"é😀\u2028"`, "é😀\u2028"},
		{`""`, ""},
	} {
		if got := mustRun(t, e, tc.source, expr.Input{}); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestEvalRejectsANonStringCompletion(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	for _, source := range []string{`42`, `(async () => 42)()`, `undefined`, `null`, `({})`} {
		if _, err := run(t, e, source, expr.Input{}); !errors.Is(err, expr.ErrNotString) {
			t.Errorf("%s: got %v, want ErrNotString", source, err)
		}
	}
}

func TestEvalRunsEveryQueuedJob(t *testing.T) {
	// JE-51: the completion value is read only once the queue is empty, so
	// a job queued before the result settled still runs, and one that never
	// ends keeps the evaluation going until the attempt limit.
	e := newEngine(t, Options{Limits: shortLimits(200 * time.Millisecond), Slots: 1})
	got := mustRun(t, e, `(async () => { const seen = []; Promise.resolve().then(() => queueMicrotask(() => seen.push("late"))); await null; return seen; })().then((seen) => new Promise((resolve) => queueMicrotask(() => resolve(seen.join(",") || "none"))))`, expr.Input{})
	if got != "late" {
		t.Errorf("got %q, want the late job to have run", got)
	}
	start := time.Now()
	_, err := run(t, e, `(async () => { Promise.resolve().then(() => { for (;;) {} }); return "done"; })()`, expr.Input{})
	if !errors.Is(err, expr.ErrAttemptTimeout) || time.Since(start) < 200*time.Millisecond {
		t.Fatalf("got %v after %v; the queued job should run until the limit", err, time.Since(start))
	}
}

func TestHostStringsAreGlobalStrings(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	text := `{"k":"é😀","n":-0,"big":9007199254740993}`
	in := expr.Input{Strings: []expr.HostString{{Name: "flow_input", Text: []byte(text)}, {Name: "empty", Text: nil}}}
	got := mustRun(t, e, `JSON.stringify([typeof flow_input, flow_input, empty, Object.getOwnPropertyDescriptor(globalThis, "flow_input")])`, in)
	quoted, _ := json.Marshal(text)
	want := `["string",` + string(quoted) + `,"",{"value":` + string(quoted) + `,"writable":true,"enumerable":true,"configurable":true}]`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// The program parses the text itself (ADR 0003): JSON.parse sees -0.
	if got := mustRun(t, e, `String(Object.is(JSON.parse(flow_input).n, -0))`, in); got != "true" {
		t.Fatalf("-0 did not survive: %s", got)
	}
}

func hostFunc(name string, call func(ctx context.Context, arg string) ([]byte, error)) expr.HostFunc {
	return expr.HostFunc{Name: name, Call: call}
}

func TestHostFuncResolvesWithText(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	var mu sync.Mutex
	var args []string
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("lookup", func(ctx context.Context, arg string) ([]byte, error) {
		mu.Lock()
		args = append(args, arg)
		mu.Unlock()
		return []byte(`{"id":"` + arg + `"}`), nil
	})}}
	got := mustRun(t, e, `(async () => {
		const p = lookup("a");
		const t = await p;
		return JSON.stringify([p instanceof Promise, typeof t, t, await lookup(1), await lookup(), lookup.name]);
	})()`, in)
	want := `[true,"string","{\"id\":\"a\"}","{\"id\":\"1\"}","{\"id\":\"undefined\"}","lookup"]`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if !slices.Equal(args, []string{"a", "1", "undefined"}) {
		t.Fatalf("args %q", args)
	}
}

func TestHostFuncRejectsWithAnError(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("lookup", func(context.Context, string) ([]byte, error) {
		return nil, errors.New("no such step: a")
	})}}
	got := mustRun(t, e, `(async () => {
		const Original = Error;
		globalThis.Error = function () { return { fake: true }; };
		try { await lookup("a"); return "resolved"; } catch (e) {
			return JSON.stringify([Object.getPrototypeOf(e) === Original.prototype, e.message, e.stack, Object.keys(e), e.fake]);
		}
	})()`, in)
	// The Error has no frames to show: its stack is empty.
	if want := `[true,"no such step: a","",[],null]`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	_, err := run(t, e, `lookup("a")`, in)
	var ex *expr.Exception
	if !errors.As(err, &ex) || ex.Thrown != expr.ThrownError || ex.Name != "Error" || ex.Message != "no such step: a" || ex.Text != "Error: no such step: a" {
		t.Fatalf("uncaught rejection: %#v", err)
	}
}

func TestHostCallsRunConcurrently(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	var started sync.WaitGroup
	started.Add(3)
	all := make(chan struct{})
	go func() { started.Wait(); close(all) }()
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("lookup", func(ctx context.Context, arg string) ([]byte, error) {
		started.Done()
		select {
		case <-all:
			return []byte(arg), nil
		case <-time.After(5 * time.Second):
			return nil, errors.New("calls were not concurrent")
		}
	})}}
	got := mustRun(t, e, `(async () => (await Promise.all([lookup("a"), lookup("b"), lookup("c")])).join(""))()`, in)
	if got != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestHostResultSettlesAfterQueuedJobs(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("lookup", func(context.Context, string) ([]byte, error) {
		return []byte("v"), nil
	})}}
	got := mustRun(t, e, `(async () => {
		const order = [];
		const p = lookup("a").then(() => order.push("host"));
		Promise.resolve().then(() => order.push("job 1")).then(() => order.push("job 2"));
		await p;
		return order.join(",");
	})()`, in)
	if got != "job 1,job 2,host" {
		t.Fatalf("order %q", got)
	}
}

func TestHostCallSeesTheAttemptEnd(t *testing.T) {
	e := newEngine(t, Options{Limits: shortLimits(200 * time.Millisecond), Slots: 1})
	causes := make(chan error, 1)
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("lookup", func(ctx context.Context, _ string) ([]byte, error) {
		<-ctx.Done()
		causes <- context.Cause(ctx)
		return nil, ctx.Err()
	})}}
	start := time.Now()
	_, err := run(t, e, `lookup("a")`, in)
	if !errors.Is(err, expr.ErrAttemptTimeout) || time.Since(start) < 200*time.Millisecond {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
	if cause := <-causes; !errors.Is(cause, expr.ErrAttemptTimeout) {
		t.Fatalf("host call's context ended with %v", cause)
	}
}

func TestExceptions(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	for _, tc := range []struct {
		source string
		want   expr.Exception
	}{
		{`(() => { throw new TypeError("bad") })()`, expr.Exception{Thrown: expr.ThrownError, Name: "TypeError", Message: "bad", HasMessage: true, HasStack: true, Text: "TypeError: bad"}},
		// The quickjs-ng texts JE-21 and JE-15 name.
		{`nope`, expr.Exception{Thrown: expr.ThrownError, Name: "ReferenceError", Message: "nope is not defined", HasMessage: true, HasStack: true, Text: "ReferenceError: nope is not defined"}},
		{`null.x`, expr.Exception{Thrown: expr.ThrownError, Name: "TypeError", Message: "cannot read property 'x' of null", HasMessage: true, HasStack: true, Text: "TypeError: cannot read property 'x' of null"}},
		{`undefined.x`, expr.Exception{Thrown: expr.ThrownError, Name: "TypeError", Message: "cannot read property 'x' of undefined", HasMessage: true, HasStack: true, Text: "TypeError: cannot read property 'x' of undefined"}},
		{`JSON.stringify(10n)`, expr.Exception{Thrown: expr.ThrownError, Name: "TypeError", Message: "BigInt are forbidden in JSON.stringify", HasMessage: true, HasStack: true, Text: "TypeError: BigInt are forbidden in JSON.stringify"}},
		{`Promise.reject(new RangeError("r"))`, expr.Exception{Thrown: expr.ThrownError, Name: "RangeError", Message: "r", HasMessage: true, HasStack: true, Text: "RangeError: r"}},
		{`(() => { class E extends Error { constructor() { super("m"); this.name = "Custom"; } } throw new E(); })()`, expr.Exception{Thrown: expr.ThrownError, Name: "Custom", Message: "m", HasMessage: true, HasStack: true, Text: "Custom: m"}},
		// The message and the stack are read by conversion (JE-21): a value
		// converts, null or undefined is none, and so is one whose
		// conversion throws.
		{`(() => { const e = new Error("m"); e.message = 5; e.stack = null; throw e })()`, expr.Exception{Thrown: expr.ThrownError, Name: "Error", Message: "5", HasMessage: true, Text: "Error: 5"}},
		{`(() => { const e = new Error("m"); e.message = undefined; e.stack = 7; throw e })()`, expr.Exception{Thrown: expr.ThrownError, Name: "Error", Stack: "7", HasStack: true, Text: "Error"}},
		{`(() => { const e = new Error("m"); e.message = Symbol(); throw e })()`, expr.Exception{Thrown: expr.ThrownError, Name: "Error", HasStack: true, Text: "Error"}},
		// Another value: its kind as rquickjs classifies it, and its payload
		// (JE-21). An object's address changes, so it is checked apart.
		{`(() => { throw "str" })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindString, String: "str"}, Text: "str"}},
		{`(() => { throw "\ud83d" })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindString, String: "\xed\xa0\xbd"}, Text: "\xed\xa0\xbd"}},
		{`(() => { throw {a: 1} })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindObject}}},
		{`(() => { throw 42 })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindInt, Int: 42}, Text: "42"}},
		{`(() => { throw 4.5 })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindFloat, Float: 4.5}, Text: "4.5"}},
		{`(() => { throw false })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindBool}, Text: "false"}},
		{`(() => { throw 10n })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindBigInt}, Text: "10"}},
		{`(() => { throw [1] })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindArray}}},
		{`(() => { throw class C {} })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindConstructor}}},
		{`(() => { throw () => 1 })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindFunction}}},
		{`(() => { throw Promise.resolve() })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindPromise}}},
		{`(() => { throw new Proxy({}, {}) })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindProxy}}},
		{`(() => { throw {message: "m", name: "Error"} })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindObject}}},
		// Nothing converts an object or a symbol: its toString would be the
		// program's code (review round 1, R1-1).
		{`(() => { throw {toString() { throw new Error("called") }} })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindObject}}},
		{`Promise.reject(null)`, expr.Exception{Thrown: expr.ThrownNull, Text: "null"}},
		{`(() => { throw undefined })()`, expr.Exception{Thrown: expr.ThrownUndefined, Text: "undefined"}},
		// A symbol, and an Error whose message getter throws, are still
		// reported, with what could be read. An Error's text is its name and
		// message as Error.prototype.toString joins them, from those reads;
		// its name comes from data properties only, so a name getter is not
		// called and the name is then "Error".
		{`(() => { throw Symbol("s") })()`, expr.Exception{Thrown: expr.ThrownOther, Value: expr.ThrownValue{Kind: expr.KindSymbol}}},
		{`(() => { const e = new Error("x"); Object.defineProperty(e, "message", { get() { throw new Error("boom"); } }); throw e; })()`, expr.Exception{Thrown: expr.ThrownError, Name: "Error", HasStack: true, Text: "Error"}},
		{`(() => { const e = new TypeError("x"); Object.defineProperty(e, "name", { get() { throw new Error("called"); } }); throw e; })()`, expr.Exception{Thrown: expr.ThrownError, Message: "x", HasMessage: true, HasStack: true, Text: "Error: x"}},
	} {
		_, err := run(t, e, tc.source, expr.Input{})
		var ex *expr.Exception
		if !errors.As(err, &ex) {
			t.Errorf("%s: got %v, want an exception", tc.source, err)
			continue
		}
		stack := ex.Stack
		if ex.HasStack && tc.want.Stack == "" {
			ex.Stack = ""
		}
		switch ex.Value.Kind {
		case expr.KindSymbol, expr.KindArray, expr.KindConstructor, expr.KindFunction, expr.KindPromise, expr.KindProxy, expr.KindObject:
			if ex.Value.Address == 0 {
				t.Errorf("%s: no address for kind %d", tc.source, ex.Value.Kind)
			}
			ex.Value.Address = 0
		}
		if *ex != tc.want {
			t.Errorf("%s:\n got  %#v\n want %#v", tc.source, *ex, tc.want)
		}
		if tc.want.Thrown == expr.ThrownError && tc.want.HasStack && tc.want.Stack == "" && !strings.Contains(stack, "eval_script") {
			t.Errorf("%s: stack %q does not name eval_script (JE-21)", tc.source, stack)
		}
	}
}

func TestCompileErrorsAreExceptions(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	_, err := e.Compile(`(`)
	var ex *expr.Exception
	if !errors.As(err, &ex) || ex.Thrown != expr.ThrownError || ex.Name != "SyntaxError" || ex.Message == "" {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(ex.Stack, "eval_script") {
		t.Errorf("stack %q does not name eval_script", ex.Stack)
	}
	ex.Message = "changed by the caller"
	_, err2 := e.Compile(`(`)
	var ex2 *expr.Exception
	if !errors.As(err2, &ex2) || ex2 == ex || ex2.Message == "changed by the caller" {
		t.Fatalf("the cached exception is shared: %#v", err2)
	}
	if n := e.Stats().Compiles; n != 1 {
		t.Fatalf("compiled %d times, want 1 (cached)", n)
	}
}

func TestCompileCache(t *testing.T) {
	t.Run("hit", func(t *testing.T) {
		e := newEngine(t, Options{Slots: 1})
		if compile(t, e, `"a"`) != compile(t, e, `"a"`) {
			t.Fatal("the same source compiled to two programs")
		}
		if n := e.Stats().Compiles; n != 1 {
			t.Fatalf("compiled %d times", n)
		}
	})
	t.Run("off", func(t *testing.T) {
		e := newEngine(t, Options{Slots: 1, CacheEntries: -1})
		p1, p2 := compile(t, e, `"a"`), compile(t, e, `"a"`)
		if p1 == p2 || e.Stats().Compiles != 2 {
			t.Fatalf("cache not off: %d compiles", e.Stats().Compiles)
		}
		for _, p := range []expr.Program{p1, p2} {
			if out, err := e.Eval(context.Background(), p, expr.Input{}); err != nil || string(out) != "a" {
				t.Fatalf("Eval = %s, %v", out, err)
			}
		}
	})
	t.Run("evicts least recently used", func(t *testing.T) {
		e := newEngine(t, Options{Slots: 1, CacheEntries: 2})
		a := compile(t, e, `"a"`)
		compile(t, e, `"b"`)
		compile(t, e, `"a"`) // a is now the most recent
		compile(t, e, `"c"`) // evicts b
		if compile(t, e, `"a"`) != a {
			t.Fatal("a was evicted")
		}
		if n := e.Stats().Compiles; n != 3 {
			t.Fatalf("compiled %d times, want 3", n)
		}
		compile(t, e, `"b"`)
		if n := e.Stats().Compiles; n != 4 {
			t.Fatalf("b was not evicted: %d compiles", n)
		}
	})
}

func TestFreshRuntimePerEvaluation(t *testing.T) {
	// JE-1: nothing survives from one evaluation to the next.
	e := newEngine(t, Options{Slots: 1})
	in := expr.Input{Strings: []expr.HostString{{Name: "first", Text: []byte("1")}}}
	mustRun(t, e, `globalThis.leak = 1; Array.prototype.leak = 2; Object.prototype.leak = 3; JSON.stringify = () => "patched"; "ok"`, in)
	got := mustRun(t, e, `[typeof leak, typeof [].leak, typeof ({}).leak, typeof first, JSON.stringify([1])].join(",")`, expr.Input{})
	if got != "undefined,undefined,undefined,undefined,[1]" {
		t.Fatalf("state survived: %s", got)
	}
}

func TestPerformanceClockStartsWithTheEvaluation(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	p := compile(t, e, `String(performance.now() < 100)`)
	time.Sleep(300 * time.Millisecond) // the prepared runtime idles
	if out, err := e.Eval(context.Background(), p, expr.Input{}); err != nil || string(out) != "true" {
		t.Fatalf("the clock started when the runtime was prepared: %s, %v", out, err)
	}
}

func TestGlobalSurface(t *testing.T) {
	// The names quickjs-ng's full context defines, pinned so that a change
	// of engine or of context setup is deliberate. JE-1 lists what Windmill
	// lacks and has; whether it also has queueMicrotask, performance and
	// DOMException is Q-JE-2's to settle on the oracle.
	e := newEngine(t, Options{Slots: 1})
	got := mustRun(t, e, `JSON.stringify(Object.getOwnPropertyNames(globalThis).sort())`, expr.Input{})
	data, err := os.ReadFile("testdata/globals.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	if got != string(wantJSON) {
		t.Fatalf("global surface changed:\n got  %s\n want %s", got, wantJSON)
	}
}

func TestContextEndsTheEvaluation(t *testing.T) {
	e := newEngine(t, Options{Limits: shortLimits(10 * time.Second), Slots: 1})
	p := compile(t, e, `(() => { for (;;) {} })()`)

	t.Run("before", func(t *testing.T) {
		before := e.Stats().Evaluations
		ctx, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("run canceled")
		cancel(cause)
		if _, err := e.Eval(ctx, p, expr.Input{}); !errors.Is(err, cause) {
			t.Fatalf("got %v", err)
		}
		if e.Stats().Evaluations != before {
			t.Fatal("a canceled evaluation reached a slot")
		}
	})
	t.Run("during", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("run canceled")
		time.AfterFunc(100*time.Millisecond, func() { cancel(cause) })
		start := time.Now()
		if _, err := e.Eval(ctx, p, expr.Input{}); !errors.Is(err, cause) {
			t.Fatalf("got %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("interrupt took %v", d)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, err := e.Eval(ctx, p, expr.Input{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	})
	if s := e.Stats(); s.Canceled != 2 || s.Timeouts != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestInterruptedSlotPreparesAFreshRuntime(t *testing.T) {
	// D8: the attempt limit interrupts the program, and the slot goes on
	// with a new runtime.
	e := newEngine(t, Options{Limits: shortLimits(200 * time.Millisecond), Slots: 1})
	start := time.Now()
	_, err := run(t, e, `globalThis.left = 1; (() => { for (;;) {} })()`, expr.Input{})
	if !errors.Is(err, expr.ErrAttemptTimeout) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d < 200*time.Millisecond || d > 5*time.Second {
		t.Fatalf("interrupted after %v", d)
	}
	for range 3 {
		if got := mustRun(t, e, `typeof left`, expr.Input{}); got != "undefined" {
			t.Fatalf("the interrupted runtime was reused: %s", got)
		}
	}
	if s := e.Stats(); s.Timeouts != 1 || s.Canceled != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestForeignPrograms(t *testing.T) {
	a := newEngine(t, Options{Slots: 1})
	b := newEngine(t, Options{Slots: 1})
	p := compile(t, a, `"a"`)
	for _, prog := range []expr.Program{p, nil, "not a program", (*program)(nil)} {
		if _, err := b.Eval(context.Background(), prog, expr.Input{}); !errors.Is(err, errForeign) {
			t.Errorf("Eval(%#v): got %v", prog, err)
		}
	}
}

// TestPreludes covers the preludes of expr.Input: they run in order, after
// the bindings and before the program, on the program's context, so their
// top-level lexical declarations are global bindings the program sees but
// globalThis does not hold (JE-10). A prelude that throws ends the
// evaluation before the program runs, and a prelude must be the engine's
// own.
func TestPreludes(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	in := expr.Input{
		Strings: []expr.HostString{{Name: "flow_input", Text: []byte(`{"n":2}`)}},
		Preludes: []expr.Program{
			compile(t, e, `globalThis.flow_input = JSON.parse(flow_input); const order = ["first"];`),
			compile(t, e, `order.push("second"); let doubled = flow_input.n * 2;`),
		},
	}
	got := mustRun(t, e, `JSON.stringify([order, doubled, typeof globalThis.order, typeof globalThis.doubled, flow_input])`, in)
	if want := `[["first","second"],4,"undefined","undefined",{"n":2}]`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}

	ran := false
	in = expr.Input{
		Funcs:    []expr.HostFunc{hostFunc("mark", func(context.Context, string) ([]byte, error) { ran = true; return []byte("x"), nil })},
		Preludes: []expr.Program{compile(t, e, `throw new TypeError("in the prelude")`)},
	}
	_, err := run(t, e, `mark("")`, in)
	var ex *expr.Exception
	if !errors.As(err, &ex) || ex.Name != "TypeError" || ex.Message != "in the prelude" || ran {
		t.Fatalf("a throwing prelude: %v (the program ran: %v)", err, ran)
	}

	other := newEngine(t, Options{Slots: 1})
	in = expr.Input{Preludes: []expr.Program{compile(t, other, `1`)}}
	if _, err := run(t, e, `"x"`, in); !errors.Is(err, errForeign) {
		t.Fatalf("a foreign prelude: %v", err)
	}
}

// TestCLibrary checks the C library's name against the binary: on Linux,
// a binary with a glibc program interpreter is a glibc build, and one with
// none is the static musl build.
func TestCLibrary(t *testing.T) {
	got := CLibrary()
	t.Logf("the engine's C library: %s", got)
	switch runtime.GOOS {
	case "darwin":
		if got != "apple" {
			t.Errorf("CLibrary() = %q on macOS", got)
		}
	case "linux":
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		f, err := elf.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		want := "musl"
		for _, p := range f.Progs {
			if p.Type == elf.PT_INTERP {
				interp, err := io.ReadAll(p.Open())
				if err != nil {
					t.Fatal(err)
				}
				if want = "other"; strings.Contains(string(interp), "ld-linux") {
					want = "glibc"
				}
			}
		}
		if got != want {
			t.Errorf("CLibrary() = %q, but the binary's interpreter says %s", got, want)
		}
	}
}

func TestClose(t *testing.T) {
	e, err := New(Options{Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("block", func(context.Context, string) ([]byte, error) {
		close(entered)
		<-release
		return []byte("released"), nil
	})}}
	p := compile(t, e, `block("x")`)
	q := compile(t, e, `"queued"`)

	running := make(chan string, 1)
	go func() {
		out, err := e.Eval(context.Background(), p, in)
		running <- fmt.Sprintf("%s %v", out, err)
	}()
	<-entered
	waiting := make(chan error, 1)
	go func() {
		_, err := e.Eval(context.Background(), q, expr.Input{})
		waiting <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the second caller queue
	closed := make(chan struct{})
	go func() { e.Close(); close(closed) }()

	if err := <-waiting; !errors.Is(err, ErrClosed) {
		t.Fatalf("waiting caller got %v", err)
	}
	select {
	case <-closed:
		t.Fatal("Close returned while an evaluation was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if got := <-running; got != "released <nil>" {
		t.Fatalf("running evaluation got %s", got)
	}
	<-closed
	if _, err := e.Eval(context.Background(), q, expr.Input{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Eval after Close: %v", err)
	}
	if _, err := e.Compile(`"new"`); !errors.Is(err, ErrClosed) {
		t.Fatalf("Compile after Close: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWaitingCallersAreServedInArrivalOrder(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	release := make(chan struct{})
	entered := make(chan struct{})
	var mu sync.Mutex
	var order []string
	in := expr.Input{Funcs: []expr.HostFunc{
		hostFunc("block", func(context.Context, string) ([]byte, error) {
			close(entered)
			<-release
			return []byte("b"), nil
		}),
		hostFunc("mark", func(_ context.Context, arg string) ([]byte, error) {
			mu.Lock()
			order = append(order, arg)
			mu.Unlock()
			return []byte(arg), nil
		}),
	}}
	blocker := compile(t, e, `block("x")`)
	marker := compile(t, e, `mark(who)`)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.Eval(context.Background(), blocker, in)
	}()
	<-entered
	waitedBefore := e.Stats().Waited
	callers := []string{"1", "2", "3", "4"}
	for _, who := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := in
			in.Strings = []expr.HostString{{Name: "who", Text: []byte(who)}}
			if out, err := e.Eval(context.Background(), marker, in); err != nil || string(out) != who {
				t.Errorf("caller %s: %s, %v", who, out, err)
			}
		}()
		time.Sleep(30 * time.Millisecond) // the caller queues before the next starts
	}
	close(release)
	wg.Wait()
	if !slices.Equal(order, callers) {
		t.Fatalf("served in order %v, want %v", order, callers)
	}
	if s := e.Stats(); s.Waited-waitedBefore != uint64(len(callers)) || s.WaitTime <= 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestOptions(t *testing.T) {
	e := newEngine(t, Options{})
	if got, want := e.Stats().Slots, min(runtime.GOMAXPROCS(0), 8); got != want {
		t.Errorf("default slots %d, want %d", got, want)
	}
	if e.Limits() != expr.DefaultLimits() {
		t.Errorf("default limits %+v", e.Limits())
	}
	if _, err := New(Options{Slots: -1}); err == nil {
		t.Error("negative slot count accepted")
	}
	if _, err := New(Options{Limits: expr.Limits{MemoryMB: -1, Attempt: time.Second, StackBytes: 1}}); err == nil {
		t.Error("invalid limits accepted")
	}
	if got := threadStack(expr.DefaultLimits()); got != 8<<20 {
		t.Errorf("thread stack %d for the default limits", got)
	}
	if got := threadStack(expr.Limits{StackBytes: 16 << 20}); got != 20<<20 {
		t.Errorf("thread stack %d for a 16 MiB limit", got)
	}
}

func TestOverflowDepthAtTheDefaultStack(t *testing.T) {
	// JE-48 records depth 600 fitting on the oracle and depth 1 000 000
	// overflowing, and depth 1 000 overflowing there too. Where this engine
	// overflows depends on the compiler and the CPU, so it is logged for
	// each platform; it is above 1 000 on every platform measured (D17).
	e := newEngine(t, Options{Slots: 1})
	got := mustRun(t, e, `(() => { let d = 0; const f = n => { d = n; return n === 0 ? 0 : 1 + f(n - 1); }; const g = n => { try { f(n); return -1; } catch (e) { return e.name === "RangeError" ? n - d : -2; } }; return String(g(1000000)); })()`, expr.Input{})
	var depth int
	fmt.Sscan(got, &depth)
	t.Logf("%s/%s: a 1 MiB stack overflows at recursion depth %d", runtime.GOOS, runtime.GOARCH, depth)
	if depth <= 600 || depth >= 1000000 {
		t.Fatalf("overflow depth %s is outside JE-48's bracket", got)
	}
	if depth <= 1000 {
		t.Logf("depth 1 000 overflows here, as on the oracle: D17 does not apply to this build")
	}
}

func TestLargeStackLimitStaysInsideTheThread(t *testing.T) {
	// A slot thread always has room beyond the engine's limit, so a deep
	// recursion meets the engine's RangeError, never the end of the thread.
	l := expr.DefaultLimits()
	l.StackBytes = 6 << 20
	e := newEngine(t, Options{Limits: l, Slots: 1})
	got := mustRun(t, e, `(() => { let d = 0; const f = n => { d = n; return 1 + f(n + 1); }; try { f(0); } catch (e) { return e.name + ":" + (d > 4000); } })()`, expr.Input{})
	if got != "RangeError:true" {
		t.Fatalf("got %s", got)
	}
}

func TestConcurrentEvaluations(t *testing.T) {
	e := newEngine(t, Options{Slots: 4})
	p := compile(t, e, `(async () => { const v = JSON.parse(input); const r = JSON.parse(await lookup(String(v.n))); return JSON.stringify({n: v.n, double: r.double}); })()`)
	in := func(n int) expr.Input {
		return expr.Input{
			Strings: []expr.HostString{{Name: "input", Text: fmt.Appendf(nil, `{"n":%d}`, n)}},
			Funcs: []expr.HostFunc{hostFunc("lookup", func(_ context.Context, arg string) ([]byte, error) {
				var k int
				fmt.Sscan(arg, &k)
				return fmt.Appendf(nil, `{"double":%d}`, 2*k), nil
			})},
		}
	}
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 25 {
				n := g*1000 + i
				out, err := e.Eval(context.Background(), p, in(n))
				if want := fmt.Sprintf(`{"n":%d,"double":%d}`, n, 2*n); err != nil || string(out) != want {
					t.Errorf("got %s, %v; want %s", out, err, want)
					return
				}
			}
		}()
	}
	wg.Wait()
	if s := e.Stats(); s.Evaluations != 16*25 {
		t.Fatalf("stats %+v", s)
	}
}

func TestVendoredSourcesAreUnmodified(t *testing.T) {
	for _, dir := range []string{"quickjs-ng", "mimalloc"} {
		t.Run(dir, func(t *testing.T) { checkNotice(t, dir) })
	}
}

// checkNotice checks that dir/NOTICE lists every file under dir, with its
// SHA-256.
func checkNotice(t *testing.T, dir string) {
	notice, err := os.ReadFile(dir + "/NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{"NOTICE": true}
	for _, line := range strings.Split(string(notice), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		data, err := os.ReadFile(dir + "/" + fields[1])
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != fields[0] {
			t.Errorf("%s: SHA-256 %s, NOTICE lists %s", fields[1], got, fields[0])
		}
		listed[fields[1]] = true
	}
	err = fs.WalkDir(os.DirFS(dir), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !listed[p] {
			t.Errorf("%s/%s is not listed in NOTICE", dir, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) < 10 {
		t.Fatalf("NOTICE lists only %d files", len(listed))
	}
}

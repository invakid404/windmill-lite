package quickjs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
)

// Programs can run their own code inside the engine's steps and can push it
// toward its limits; these tests keep the process alive and the heap cap
// whole when they do.

func TestSettlingSurvivesProgramCodeThatCallsTheHost(t *testing.T) {
	// Rejecting a call builds an Error, which runs the program's
	// Error.prepareStackTrace; that code calls the host again and grows the
	// table of calls while the rejection is being settled.
	e := newEngine(t, Options{Slots: 1})
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("f", func(_ context.Context, arg string) ([]byte, error) {
		if arg == "fail" {
			return nil, errors.New("no")
		}
		return []byte(arg), nil
	})}}
	got := mustRun(t, e, `(async () => {
		const extra = [];
		let entered = false;
		Error.prepareStackTrace = () => {
			if (!entered) { entered = true; for (let i = 0; i < 20; i++) extra.push(f("x")); }
			return "custom";
		};
		const first = [f("a"), f("b"), f("c"), f("d")];
		try { await f("fail"); return "resolved"; } catch (e) {
			return JSON.stringify([e.message, e.stack, (await Promise.all(first)).join(""), (await Promise.all(extra)).length]);
		}
	})()`, in)
	if want := `["no","custom","abcd",20]`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestEngineLeaksAreReclaimed(t *testing.T) {
	// QuickJS-ng does not free the object this program leaves behind; the
	// engine frees it with the runtime's other blocks, and counts it.
	e := newLeakyEngine(t, Options{Slots: 1})
	for range 3 {
		if got := mustRun(t, e, `Error.stackTraceLimit = {}; Error.stackTraceLimit = 10; "x"`, expr.Input{}); got != "x" {
			t.Fatalf("got %q", got)
		}
	}
	if n := e.Stats().LeakedBlocks; n == 0 {
		t.Fatal("the leak was not seen")
	}
	if got := mustRun(t, e, `"next"`, expr.Input{}); got != "next" {
		t.Fatalf("the slot did not recover: %q", got)
	}
}

func TestHostCallArgumentsCountAgainstTheCap(t *testing.T) {
	// JE-3: the cap covers what the program makes the engine hold, the
	// copies of host-call arguments included.
	l := expr.DefaultLimits()
	l.MemoryMB = 32
	e := newEngine(t, Options{Limits: l, Slots: 1})
	var calls atomic.Int32
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("f", func(context.Context, string) ([]byte, error) {
		calls.Add(1)
		return nil, nil
	})}}
	_, err := run(t, e, `(() => { const s = "x".repeat(8 << 20); for (let i = 0; i < 100; i++) f(s); return "done"; })()`, in)
	var ex *expr.Exception
	if !errors.As(err, &ex) || !outOfMemory(ex) {
		t.Fatalf("got %v, want out of memory", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d calls ran", n)
	}
}

func TestHostCallsInFlightAreBounded(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	var mu sync.Mutex
	running, peak := 0, 0
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("f", func(_ context.Context, arg string) ([]byte, error) {
		mu.Lock()
		running++
		peak = max(peak, running)
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return []byte(arg), nil
	})}}
	got := mustRun(t, e, `(async () => {
		const ps = [];
		for (let i = 0; i < 200; i++) ps.push(f(String(i)));
		const r = await Promise.all(ps);
		return String(r.every((v, i) => v === String(i)) && r.length);
	})()`, in)
	if got != "200" {
		t.Fatalf("got %s", got)
	}
	if peak > maxCallsInFlight || peak < 2 {
		t.Fatalf("%d calls ran at once, want 2 to %d", peak, maxCallsInFlight)
	}
}

func TestBindingNamedLikeAGlobalKeepsItsValue(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	in := expr.Input{Strings: []expr.HostString{{Name: "performance", Text: []byte("bound")}}}
	if got := mustRun(t, e, `typeof performance + ":" + performance`, in); got != "string:bound" {
		t.Fatalf("got %s", got)
	}
}

func TestManyHostFunctions(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	var in expr.Input
	for i := range 40000 {
		in.Funcs = append(in.Funcs, hostFunc(fmt.Sprintf("f%d", i), func(_ context.Context, arg string) ([]byte, error) {
			return fmt.Appendf(nil, "%d:%s", i, arg), nil
		}))
	}
	got := mustRun(t, e, `(async () => (await f0("a")) + "," + (await f32768("b")) + "," + (await f39999("c")))()`, in)
	if got != "0:a,32768:b,39999:c" {
		t.Fatalf("got %s", got)
	}
}

func TestHostArgumentWhoseToStringThrows(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	var calls atomic.Int32
	in := expr.Input{Funcs: []expr.HostFunc{hostFunc("f", func(context.Context, string) ([]byte, error) {
		calls.Add(1)
		return nil, nil
	})}}
	got := mustRun(t, e, `(() => {
		const e = new Error("m");
		e.toString = () => { throw new Error("no string form"); };
		try { f(e); return "called"; } catch (x) { return x.message; }
	})()`, in)
	if got != "no string form" || calls.Load() != 0 {
		t.Fatalf("got %q after %d calls", got, calls.Load())
	}
}

func TestHostFuncWithoutCall(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	p := compile(t, e, `"x"`)
	_, err := e.Eval(context.Background(), p, expr.Input{Funcs: []expr.HostFunc{{Name: "f"}}})
	if !errors.Is(err, errNilCall) || !strings.Contains(err.Error(), `"f"`) {
		t.Fatalf("got %v", err)
	}
}

func TestUnpairedSurrogatesComeBackAsWTF8(t *testing.T) {
	// QuickJS-ng converts a string with an unpaired surrogate to three bytes
	// for it (WTF-8). JSON.stringify, which the pipeline's programs end
	// with, escapes such surrogates, so its text is UTF-8.
	e := newEngine(t, Options{Slots: 1})
	if got := mustRun(t, e, `"a\uD800b"`, expr.Input{}); got != "a\xed\xa0\x80b" {
		t.Fatalf("got % x", got)
	}
	if got := mustRun(t, e, `JSON.stringify("a\uD800b")`, expr.Input{}); got != `"a\ud800b"` {
		t.Fatalf("got %s", got)
	}
}

func TestAThrowingJobFailsTheEvaluation(t *testing.T) {
	// A job that throws (here a queueMicrotask callback) fails the
	// evaluation with its exception, even though the result would settle.
	// Whether Windmill's job loop does the same is an open question.
	e := newEngine(t, Options{Slots: 1})
	_, err := run(t, e, `(async () => { queueMicrotask(() => { throw new TypeError("job"); }); await null; await null; return "done"; })()`, expr.Input{})
	var ex *expr.Exception
	if !errors.As(err, &ex) || ex.Name != "TypeError" || ex.Message != "job" {
		t.Fatalf("got %v", err)
	}
}

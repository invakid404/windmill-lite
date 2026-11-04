package quickjs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
)

// Inline host functions (expr.HostFunc.Inline) are called on
// the slot's thread when the program's calls are dispatched, and their
// answers settle in call order.

func inlineFunc(name string, call func(ctx context.Context, arg string) ([]byte, error)) expr.HostFunc {
	return expr.HostFunc{Name: name, Call: call, Inline: true}
}

func TestInlineHostCallsSettleInCallOrder(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	var calls atomic.Int32
	in := expr.Input{Funcs: []expr.HostFunc{inlineFunc("f", func(_ context.Context, arg string) ([]byte, error) {
		calls.Add(1)
		return []byte(arg), nil
	})}}
	got := mustRun(t, e, `(async () => {
		const order = [], ps = [];
		for (let i = 0; i < 200; i++) ps.push(f(String(i)).then(v => order.push(v)));
		await Promise.all(ps);
		return order.join(",");
	})()`, in)
	want := ""
	for i := range 200 {
		if i > 0 {
			want += ","
		}
		want += fmt.Sprint(i)
	}
	if got != want {
		t.Fatalf("settled in the order %s", got)
	}
	if n := calls.Load(); n != 200 {
		t.Fatalf("%d calls", n)
	}
}

// TestInlineHostCallsAreBounded: answers of inline calls not yet settled
// count against maxCallsInFlight with the running calls, so the engine
// holds at most that many at once however many the program queues.
func TestInlineHostCallsAreBounded(t *testing.T) {
	e := newEngine(t, Options{Slots: 1})
	in := expr.Input{Funcs: []expr.HostFunc{
		inlineFunc("f", func(_ context.Context, arg string) ([]byte, error) { return []byte(arg), nil }),
		hostFunc("g", func(_ context.Context, arg string) ([]byte, error) {
			time.Sleep(time.Millisecond)
			return []byte(arg), nil
		}),
	}}
	got := mustRun(t, e, `(async () => {
		const ps = [];
		for (let i = 0; i < 2000; i++) ps.push(i % 10 === 0 ? g(String(i)) : f(String(i)));
		const r = await Promise.all(ps);
		return String(r.every((v, i) => v === String(i)) && r.length);
	})()`, in)
	if got != "2000" {
		t.Fatalf("got %s", got)
	}
	if peak := e.stats.heldPeak.Load(); peak != maxCallsInFlight {
		t.Fatalf("the engine held %d calls at once, want the bound, %d", peak, maxCallsInFlight)
	}
}

// TestInlineHostCallsMatchGoroutineCalls runs programs with their host
// functions inline and not: answers, rejections, calls from a settlement,
// a call that throws in the program, and calls mixed with a function
// that is not inline give the same outcome either way.
func TestInlineHostCallsMatchGoroutineCalls(t *testing.T) {
	e := newEngine(t, Options{Slots: 2})
	programs := []string{
		`(async () => (await f("a")) + (await f("b")))()`,
		`(async () => { try { await f("fail") } catch (err) { return err instanceof Error && err.message } })()`,
		`f("fail").then(() => "resolved", err => "rejected: " + err.message)`,
		`(async () => { const a = await f("x"); const b = await f(a + "y"); return b + (await g("z")); })()`,
		`(async () => (await Promise.all([f("1"), g("2"), f("3"), g("4")])).join("-"))()`,
		`(async () => { const r = await Promise.allSettled([f("ok"), f("fail"), g("fail")]); return r.map(x => x.status).join(","); })()`,
		`(async () => { let s = ""; for (let i = 0; i < 100; i++) s += await f(String(i % 10)); return s; })()`,
		`Promise.race([f("first"), g("second")]).then(v => typeof v)`,
	}
	funcs := func(inline bool) expr.Input {
		call := func(_ context.Context, arg string) ([]byte, error) {
			if arg == "fail" {
				return nil, errors.New("synthetic failure")
			}
			return []byte(arg), nil
		}
		return expr.Input{Funcs: []expr.HostFunc{{Name: "f", Call: call, Inline: inline}, hostFunc("g", call)}}
	}
	var wg sync.WaitGroup
	for _, src := range programs {
		p := compile(t, e, src)
		for range 4 {
			wg.Go(func() {
				var outs [2]string
				for i, inline := range []bool{false, true} {
					out, err := e.Eval(context.Background(), p, funcs(inline))
					outs[i] = string(out)
					if err != nil {
						outs[i] = "error: " + err.Error()
					}
				}
				if outs[0] != outs[1] {
					t.Errorf("%s: %q with goroutines, %q inline", src, outs[0], outs[1])
				}
			})
		}
	}
	wg.Wait()
}

// TestInlineHostCallKeepsTheAttemptLimit: an inline call runs on the slot's
// thread, where nothing can interrupt it, so one that broke its promise
// not to block still ends the attempt at the limit's error once it returns.
func TestInlineHostCallKeepsTheAttemptLimit(t *testing.T) {
	e := newEngine(t, Options{Limits: shortLimits(200 * time.Millisecond), Slots: 1})
	in := expr.Input{Funcs: []expr.HostFunc{inlineFunc("f", func(context.Context, string) ([]byte, error) {
		time.Sleep(400 * time.Millisecond)
		return []byte("late"), nil
	})}}
	if _, err := run(t, e, `f("x")`, in); !errors.Is(err, expr.ErrAttemptTimeout) {
		t.Fatalf("got %v, want the attempt limit's error", err)
	}
	if got := mustRun(t, e, `"next"`, expr.Input{}); got != "next" {
		t.Fatalf("the slot did not recover: %q", got)
	}
}

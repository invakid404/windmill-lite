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

// The evaluation limits jsExpr keeps: wherever it
// answers, the engine, run with the same limits, answers the same text, so
// it never answers what the engine would fail with the heap cap (JE-3), the
// stack limit (JE-48) or the attempt limit (JE-2).

// limitCase evaluates text over scope s both ways, with limits l, and
// reports whether jsExpr answered and whether the engine failed.
func limitCase(t *testing.T, p *Pipeline, l Limits, text string, s func() *Scope) (answered, engineFailed bool) {
	t.Helper()
	ctx := context.Background()
	e := Compile(SiteInputTransform, text)
	x := parseJSExpr(e)
	if x == nil {
		return false, false
	}
	sc := s()
	_, memoryReads := sc.Results.(MemoryResults)
	got, ok := x.eval(ctx, sc, jsLimits{limits: l, bound: bind(e, sc).Strings, memoryReads: memoryReads})
	want, err := p.evaluate(ctx, e, bind(e, s()))
	if ok && err != nil {
		t.Errorf("%.80s: jsExpr answered %.80s, the engine failed: %v", text, got, err)
	}
	if ok && err == nil && string(got) != string(want) {
		t.Errorf("%.80s: jsExpr answered %.80s, the engine %.80s", text, got, want)
	}
	return ok, err != nil
}

// TestJSExprKeepsTheHeapCap runs strings and parsed values around small
// caps, with the shapes that cost QuickJS the most per byte of JSON text,
// and checks that jsExpr answers only what fits.
func TestJSExprKeepsTheHeapCap(t *testing.T) {
	values := map[string]func(n int) string{
		"string":     func(n int) string { return `"` + strings.Repeat("x", n) + `"` },
		"wide":       func(n int) string { return `"` + strings.Repeat("é", n/2) + `"` },
		"empties":    func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat("[],", n/3), ",") + "]" },
		"objects":    func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat("{},", n/3), ",") + "]" },
		"oneKey":     func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat(`{"a":0},`, n/8), ",") + "]" },
		"numbers":    func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat("0,", n/2), ",") + "]" },
		"shortStrs":  func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat(`"a",`, n/4), ",") + "]" },
		"distinctKs": func(n int) string { return distinctKeys(n) },
	}
	texts := []string{
		"flow_input.v.length + 1",
		"`${flow_input.v.length}:${flow_input.k}`",
		"flow_input.s + flow_input.s",
		"flow_input.s + flow_input.s + flow_input.s + flow_input.s",
		"`${flow_input.s}${flow_input.s}${flow_input.s}`",
		"results.st.v.length * 2",
		"Boolean(results.st.v)",
	}
	answered, failed := 0, 0
	for _, capMB := range []int{2, 4, 8} {
		l := DefaultLimits()
		l.MemoryMB = capMB
		// The engine runs out of the cap on strings with non-ASCII
		// characters here, where QuickJS-ng leaks (realLeakyEngine).
		p := NewPipeline(realLeakyEngine(t, l, 1))
		for name, mk := range values {
			for _, size := range []int{1 << 10, 4 << 10, 16 << 10, 32 << 10, 64 << 10, 128 << 10, 256 << 10, 512 << 10, 1 << 20, 2 << 20} {
				v := mk(size)
				s := `"` + strings.Repeat("s", size) + `"`
				fi := fmt.Sprintf(`{"k":"key","v":%s,"s":%s}`, v, s)
				st := fmt.Sprintf(`{"v":%s}`, v)
				scope := func() *Scope {
					return &Scope{PreviousID: "other", FlowInput: jsonval.FromProducer([]byte(fi)), Results: memoryResults{&fakeResults{stored: map[string]string{"st": st}}}}
				}
				for _, text := range texts {
					a, f := limitCase(t, p, l, text, scope)
					if a {
						answered++
					}
					if f {
						failed++
					}
					if t.Failed() {
						t.Fatalf("cap %d MiB, %s of %d bytes", capMB, name, size)
					}
				}
			}
		}
	}
	t.Logf("jsExpr answered %d cases; the engine failed %d (the cap reached)", answered, failed)
	if answered == 0 || failed == 0 {
		t.Fatalf("the cases do not reach both sides of the cap: %d answered, %d failed", answered, failed)
	}
}

// distinctKeys is an object of n bytes or so with distinct short keys.
func distinctKeys(n int) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; b.Len() < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":0`, i)
	}
	b.WriteByte('}')
	return b.String()
}

// TestJSExprKeepsTheStackLimit nests expressions up to and past jsExpr's
// depth limit: where it answers, the engine answers the same.
func TestJSExprKeepsTheStackLimit(t *testing.T) {
	p := NewPipeline(realEngine(t, DefaultLimits(), 1))
	scope := func() *Scope { return &Scope{FlowInput: jsonval.FromProducer([]byte(`{"a":1,"s":"x"}`))} }
	answered, declined := 0, 0
	for _, depth := range []int{1, 8, 16, 24, 30, 31, 32, 33, 40, 64, 500, 5000} {
		for _, text := range []string{
			strings.Repeat("(", depth) + "flow_input.a + 1" + strings.Repeat(")", depth),
			strings.Repeat("!", depth) + "flow_input.a",
			strings.Repeat("-", depth/2) + strings.Repeat("!", depth-depth/2) + "flow_input.a",
			strings.Repeat("`${", depth) + "flow_input.s" + strings.Repeat("}`", depth),
			strings.Repeat("flow_input.a ? ", depth) + "1" + strings.Repeat(" : 2", depth),
			strings.Repeat("Boolean(", depth) + "flow_input.a" + strings.Repeat(")", depth),
		} {
			a, _ := limitCase(t, p, DefaultLimits(), text, scope)
			if a {
				answered++
				if depth > jsMaxDepth {
					t.Errorf("depth %d answered: %.60s", depth, text)
				}
			} else {
				declined++
			}
		}
	}
	t.Logf("%d answered, %d declined", answered, declined)
}

// slowMemoryResults answers after a delay but says it reads memory: a
// broken promise jsExpr must not stretch past the attempt limit either way.
type slowLookup struct{ d time.Duration }

func (s slowLookup) Lookup(ctx context.Context, _ string) (jsonval.Value, error) {
	select {
	case <-time.After(s.d):
		return jsonval.FromProducer([]byte("1")), nil
	case <-ctx.Done():
		return jsonval.Value{}, ctx.Err()
	}
}

func (slowLookup) Stored(context.Context, string) (jsonval.Value, bool) {
	return jsonval.Value{}, false
}

// TestJSExprKeepsTheAttemptLimit: two lookups of
// 35 ms each under a 50 ms attempt limit time the evaluation out, with
// jsExpr on or off; and under a limit below jsMinAttempt jsExpr declines
// everything.
func TestJSExprKeepsTheAttemptLimit(t *testing.T) {
	l := DefaultLimits()
	l.Attempt = 50 * time.Millisecond
	p := NewPipeline(realEngine(t, l, 1))
	setRetry(p, 1, 0)
	e := Compile(SiteInputTransform, "results.a + results.b")
	s := &Scope{PreviousID: "other", Results: slowLookup{35 * time.Millisecond}}
	for _, on := range []bool{true, false} {
		p.paths = on
		_, err := p.Eval(context.Background(), e, s)
		if !errors.Is(err, ErrAttemptTimeout) {
			t.Fatalf("jsExpr %t: %v, want the attempt limit's error", on, err)
		}
	}
	if _, ok := parseJSExpr(Compile(SiteInputTransform, "flow_input.a + 1")).eval(context.Background(), &Scope{FlowInput: jsonval.FromProducer([]byte(`{"a":1}`))}, jsLimits{limits: l}); ok {
		t.Fatal("jsExpr answered under a 50 ms attempt limit")
	}
}

// TestJSExprIsolation evaluates the same expressions concurrently over
// shared scopes: each answer depends on its own inputs only (run it with
// -race).
func TestJSExprIsolation(t *testing.T) {
	x := parseJSExpr(Compile(SiteInputTransform, "`${flow_input.a}:${results.st.n + 1}`"))
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			s := &Scope{PreviousID: "other", FlowInput: jsonval.FromProducer(fmt.Appendf(nil, `{"a":%d}`, i)), Results: memoryResults{&fakeResults{stored: map[string]string{"st": fmt.Sprintf(`{"n":%d}`, i)}}}}
			for range 100 {
				out, ok := x.eval(context.Background(), s, jsLimits{limits: DefaultLimits(), memoryReads: true})
				if !ok || string(out) != fmt.Sprintf(`"%d:%d"`, i, i+1) {
					t.Errorf("%d: %s, %t", i, out, ok)
					return
				}
			}
		})
	}
	wg.Wait()
}

// TestJSExprWorstCaseTime times jsExpr and the engine on the largest
// inputs the walker takes (a root just under the member paths' 1 MiB cap read to its
// end, a string of that size measured and joined three times, ≈ 0.9 MiB
// strings joined): jsExpr answers or, past jsMaxWork, declines, within
// milliseconds either way. jsExpr's own work is bounded, so it stays far
// under 1 % of the attempt limit, and the engine's evaluation of the same
// expressions far under jsMinAttempt: neither can meet JE-2's limit where
// jsExpr answers. It logs the times.
func TestJSExprWorstCaseTime(t *testing.T) {
	l := DefaultLimits()
	p := NewPipeline(realEngine(t, l, 1))
	// The member paths' walker reads roots up to 1 MiB (maxPathText); the budget then
	// bounds the strings built.
	big := maxPathText - 64
	arr := "[" + strings.TrimSuffix(strings.Repeat("123456,", big/7), ",") + "]"
	obj := `{"a":` + arr + `,"z":{"n":41}}`
	str := `"` + strings.Repeat("y", big) + `"`
	half := `"` + strings.Repeat("z", 900<<10) + `"`
	cases := []struct{ text, fi string }{
		{"flow_input.o.z.n + 1", `{"o":` + obj + `}`},
		{"flow_input.s.length * 2", `{"s":` + str + `}`},
		{"`${flow_input.s}${flow_input.s}${flow_input.s}`.length", `{"s":` + str + `}`},
		{"`${flow_input.h}${flow_input.h}`.length", `{"h":` + half + `}`},
		{"flow_input.h + flow_input.h", `{"h":` + half + `}`},
	}
	for _, c := range cases {
		e := Compile(SiteInputTransform, c.text)
		x := parseJSExpr(e)
		if x == nil {
			t.Logf("%s: not in jsExpr's grammar", c.text)
			continue
		}
		s := &Scope{FlowInput: jsonval.FromProducer([]byte(c.fi))}
		t0 := time.Now()
		out, ok := x.eval(context.Background(), s, jsLimits{limits: l, bound: bind(e, s).Strings, memoryReads: true})
		dj := time.Since(t0)
		t0 = time.Now()
		want, err := p.evaluate(context.Background(), e, bind(e, s))
		de := time.Since(t0)
		t.Logf("%-45s jsExpr %v in %v; engine in %v (err %v)", c.text, ok, dj, de, err)
		if ok && (err != nil || string(out) != string(want)) {
			t.Errorf("%s: jsExpr %.60s, engine %.60s %v", c.text, out, want, err)
		}
		if ok && dj > l.Attempt/100 {
			t.Errorf("%s: jsExpr took %v", c.text, dj)
		}
		if ok && de > jsMinAttempt/10 {
			t.Errorf("%s: the engine took %v, near jsMinAttempt", c.text, de)
		}
	}
}

// TestJSExprBoundsItsOwnWork: an expression that
// reads a large value many times. jsExpr's own work is charged before it is
// done (jsMaxWork) and its clock is checked at every node, so it answers or
// declines within a hundredth of the attempt limit plus one bounded
// operation, never after doing the work; where it answers, the engine
// answers the same. The first case is the review's: a 900 KiB string read
// 700 times, which took 1.5 s in Go before declining.
func TestJSExprBoundsItsOwnWork(t *testing.T) {
	l := DefaultLimits()
	l.Attempt = 5 * time.Second
	p := NewPipeline(realEngine(t, l, 1))
	setRetry(p, 1, 0)
	s900 := `"` + strings.Repeat("y", 900<<10) + `"`
	var keys, wide strings.Builder
	for i := range 300 {
		if i > 0 {
			keys.WriteString(" + ")
			wide.WriteByte(',')
		}
		fmt.Fprintf(&keys, "flow_input.o.k%d.length", i)
		fmt.Fprintf(&wide, `"k%d":"%s"`, i, strings.Repeat("z", 3300))
	}
	cases := []struct{ name, text, fi string }{
		{"one read 700 times", strings.Repeat("flow_input.s.length + ", 700) + "1", `{"s":` + s900 + `}`},
		{"300 distinct reads of a 1 MiB root", keys.String(), `{"o":{` + wide.String() + `}}`},
		{"a large string compared 300 times", strings.Repeat("(flow_input.s === flow_input.s) + ", 300) + "0", `{"s":` + s900 + `}`},
		{"a template that grows", "`" + strings.Repeat("${flow_input.s}", 40) + "`.length", `{"s":` + s900 + `}`},
	}
	for _, c := range cases {
		e := Compile(SiteInputTransform, c.text)
		x := parseJSExpr(e)
		if x == nil {
			t.Fatalf("%s: not in jsExpr's grammar (%d bytes)", c.name, len(c.text))
		}
		s := &Scope{FlowInput: jsonval.FromProducer([]byte(c.fi))}
		t0 := time.Now()
		out, ok := x.eval(context.Background(), s, jsLimits{limits: l, bound: bind(e, s).Strings, memoryReads: true})
		dj := time.Since(t0)
		t0 = time.Now()
		v, err := p.Eval(context.Background(), e, s)
		dp := time.Since(t0)
		p.paths = false
		t0 = time.Now()
		want, werr := p.Eval(context.Background(), e, s)
		de := time.Since(t0)
		p.paths = true
		t.Logf("%-36s jsExpr %-5v in %8v; pipeline in %8v; engine alone in %8v", c.name, ok, dj, dp, de)
		if bound := l.Attempt/100 + 50*time.Millisecond; dj > bound {
			t.Errorf("%s: jsExpr took %v, over %v", c.name, dj, bound)
		}
		if ok && string(out) != string(want.Producer()) {
			t.Errorf("%s: jsExpr %.60s, engine %.60s", c.name, out, want.Producer())
		}
		if fmt.Sprint(err) != fmt.Sprint(werr) || string(v.Producer()) != string(want.Producer()) {
			t.Errorf("%s: pipeline %.60s %v, engine %.60s %v", c.name, v.Producer(), err, want.Producer(), werr)
		}
	}
}

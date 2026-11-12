package quickjs

// TEMPORARY diagnostic (removed before review): measures the deepest
// recursion that fits under several stack limits and derives the C stack
// one JavaScript call takes. Run with WL_DIAG_STACK=1.

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
)

var diagShapes = []struct{ name, src string }{
	{"je48-arrow", `String((() => { const f = n => n === 0 ? 0 : 1 + f(n - 1); return f(K); })())`},
	{"selfcheck-named", `String((function f(n) { return n === 0 ? 0 : 1 + f(n - 1); })(K))`},
	{"je48a-two-arg", `String(((g, n) => g(g, n))((g, n) => n === 0 ? 0 : 1 + g(g, n - 1), K))`},
	{"tracker", `(() => { let d = 0; const f = n => { d = n; return n === 0 ? 0 : 1 + f(n - 1); }; return String(f(K)); })()`},
}

func diagFits(t *testing.T, e *Engine, src string, k int) bool {
	p, err := e.Compile(strings.ReplaceAll(src, "K", fmt.Sprint(k)))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, err = e.Eval(context.Background(), p, expr.Input{})
	if err == nil {
		return true
	}
	if !strings.Contains(err.Error(), "Maximum call stack size exceeded") {
		t.Fatalf("depth %d: %v", k, err)
	}
	return false
}

// diagDepth is the largest k for which the shape fits.
func diagDepth(t *testing.T, e *Engine, src string) int {
	lo, hi := 0, 1
	for diagFits(t, e, src, hi) {
		lo, hi = hi, hi*2
	}
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if diagFits(t, e, src, mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

func TestDiagStack(t *testing.T) {
	if os.Getenv("WL_DIAG_STACK") != "1" {
		t.Skip("WL_DIAG_STACK not set")
	}
	limits := []int{1 << 20, 2 << 20, 4 << 20}
	depth := map[int]map[string]int{}
	for _, lim := range limits {
		l := expr.DefaultLimits()
		l.StackBytes = lim
		e := newEngine(t, Options{Limits: l, Slots: 1, CacheEntries: -1})
		depth[lim] = map[string]int{}
		for _, s := range diagShapes {
			depth[lim][s.name] = diagDepth(t, e, s.src)
		}
		e.Close()
	}
	label := os.Getenv("WL_DIAG_LABEL")
	for _, s := range diagShapes {
		d1, d2, d4 := depth[1<<20][s.name], depth[2<<20][s.name], depth[4<<20][s.name]
		slope := float64(3<<20) / float64(d4-d1)
		base := float64(1<<20) - float64(d1)*slope
		t.Logf("DIAG %s %s/%s %-16s depth@1MiB=%d depth@2MiB=%d depth@4MiB=%d bytes/call=%.1f entry=%.0f",
			label, runtime.GOOS, runtime.GOARCH, s.name, d1, d2, d4, slope, base)
	}
}

var diagWorkloads = []struct{ name, src string }{
	{"fib", `(() => { const fib = n => n < 2 ? n : fib(n - 1) + fib(n - 2); return String(fib(25)); })()`},
	{"loop", `(() => { let s = 0; const o = {a: 1, b: 2}; for (let i = 0; i < 400000; i++) { s += o.a * i + (i & 7); o.b = s % 13; } return String(s); })()`},
	{"json", `(() => { const a = []; for (let i = 0; i < 2000; i++) a.push({id: i, name: "n" + i, tags: ["x", "y"]}); let t = 0; for (let k = 0; k < 10; k++) t += JSON.parse(JSON.stringify(a)).length; return String(t); })()`},
	{"array", `(() => { const a = Array.from({length: 30000}, (_, i) => (i * 7919) % 10007); return String(a.map(x => x * 2).filter(x => x % 3).sort((x, y) => x - y).reduce((s, x) => s + x, 0)); })()`},
	{"string", `(() => { let s = ""; for (let i = 0; i < 20000; i++) s += String.fromCharCode(97 + i % 26); let n = 0; for (let i = 0; i < 200; i++) n += s.split("a").length + s.indexOf("zz") + s.replace(/b/g, "c").length; return String(n); })()`},
}

func TestDiagPerf(t *testing.T) {
	if os.Getenv("WL_DIAG_STACK") != "1" {
		t.Skip("WL_DIAG_STACK not set")
	}
	e := newEngine(t, Options{Slots: 1})
	label := os.Getenv("WL_DIAG_LABEL")
	var total time.Duration
	for _, w := range diagWorkloads {
		p := compile(t, e, w.src)
		best := time.Duration(1 << 62)
		for range 15 {
			start := time.Now()
			if _, err := e.Eval(context.Background(), p, expr.Input{}); err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
			best = min(best, time.Since(start))
		}
		total += best
		t.Logf("PERF %s %s/%s %-8s best=%v", label, runtime.GOOS, runtime.GOARCH, w.name, best)
	}
	t.Logf("PERF %s %s/%s %-8s best=%v", label, runtime.GOOS, runtime.GOARCH, "total", total)
}

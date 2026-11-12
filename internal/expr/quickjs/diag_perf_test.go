package quickjs

// TEMPORARY diagnostic (removed before review): interpreter speed, to cost
// the alternative of pinning the inliner. Run with WL_DIAG_PERF=1.

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
)

var diagWorkloads = []struct{ name, src string }{
	{"fib", `(() => { const fib = n => n < 2 ? n : fib(n - 1) + fib(n - 2); return String(fib(25)); })()`},
	{"loop", `(() => { let s = 0; const o = {a: 1, b: 2}; for (let i = 0; i < 400000; i++) { s += o.a * i + (i & 7); o.b = s % 13; } return String(s); })()`},
	{"json", `(() => { const a = []; for (let i = 0; i < 2000; i++) a.push({id: i, name: "n" + i, tags: ["x", "y"]}); let t = 0; for (let k = 0; k < 10; k++) t += JSON.parse(JSON.stringify(a)).length; return String(t); })()`},
	{"array", `(() => { const a = Array.from({length: 30000}, (_, i) => (i * 7919) % 10007); return String(a.map(x => x * 2).filter(x => x % 3).sort((x, y) => x - y).reduce((s, x) => s + x, 0)); })()`},
	{"string", `(() => { let s = ""; for (let i = 0; i < 20000; i++) s += String.fromCharCode(97 + i % 26); let n = 0; for (let i = 0; i < 200; i++) n += s.split("a").length + s.indexOf("zz") + s.replace(/b/g, "c").length; return String(n); })()`},
}

func TestDiagPerf(t *testing.T) {
	if os.Getenv("WL_DIAG_PERF") != "1" {
		t.Skip("WL_DIAG_PERF not set")
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

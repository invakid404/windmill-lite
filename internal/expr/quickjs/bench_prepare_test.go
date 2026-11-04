package quickjs

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
)

// BenchmarkEvalCPU measures what one evaluation costs the process in CPU,
// the slot's preparation of the next fresh runtime and the release of the
// used one included (both off the caller's path), for a short program that
// reads one bound string, one evaluation at a time on a pool of one or four
// slots. It reports cpu-ns/op (getrusage) beside the wall time, in the
// default mode, which destroys each runtime's heap whole, and in leak check,
// which frees it block by block (Options.LeakCheck).
func BenchmarkEvalCPU(b *testing.B) {
	for _, mode := range []string{"destroy", "leakcheck"} {
		for _, slots := range []int{1, 4} {
			b.Run("mode="+mode+"/slots="+map[int]string{1: "1", 4: "4"}[slots], func(b *testing.B) {
				e := newEngine(b, Options{Slots: slots, LeakCheck: mode == "leakcheck"})
				p := compile(b, e, `JSON.stringify(JSON.parse(v).a + 1)`)
				in := expr.Input{Strings: []expr.HostString{{Name: "v", Text: []byte(`{"a":41,"b":[1,2,3],"c":"text"}`)}}}
				ctx := context.Background()
				// Let the pool prepare its runtimes first.
				time.Sleep(50 * time.Millisecond)
				c0 := cpuNow()
				b.ResetTimer()
				for range b.N {
					if _, err := e.Eval(ctx, p, in); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				// The last releases and preparations finish off the clock.
				time.Sleep(50 * time.Millisecond)
				b.ReportMetric(float64(cpuNow()-c0)/float64(b.N), "cpu-ns/op")
			})
		}
	}
}

func cpuNow() time.Duration {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

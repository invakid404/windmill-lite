package quickjs

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
)

// TestPoolUnderLoad measures the native pool under the loads of ADR 0003's
// pool table and budget gate, and prints the latencies. It asserts nothing
// about them: the budget gate is B2b's. It runs only with WL_QUICKJS_LOAD=1;
// WL_QUICKJS_LOAD_SECONDS sets the length of each row (default 2).
//
// The program stands in for an engine-evaluated predicate: it binds a
// synthetic results value of the row's size and a small flow input as host
// strings, parses both, and serialises the predicate's value.
func TestPoolUnderLoad(t *testing.T) {
	if os.Getenv("WL_QUICKJS_LOAD") != "1" {
		t.Skip("set WL_QUICKJS_LOAD=1 to measure the pool under load")
	}
	seconds := 2.0
	if s := os.Getenv("WL_QUICKJS_LOAD_SECONDS"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		seconds = v
	}
	duration := time.Duration(seconds * float64(time.Second))

	e := newEngine(t, Options{})
	p := compile(t, e, `(async () => {
		const flow_input = JSON.parse(wl_flow_input);
		const results = { fetch_order: JSON.parse(wl_result_fetch_order) };
		return JSON.stringify(Boolean(results.fetch_order.archived ? null : flow_input.order.ref));
	})()`)
	small := []byte(`{"order":{"ref":"ORD-7"},"note":"synthetic"}`)

	type shape struct {
		name    string
		callers int
		pause   time.Duration
		burst   bool
	}
	shapes := []shape{
		{"1 caller, 2 ms between evaluations", 1, 2 * time.Millisecond, false},
		{"8 callers, 0.5 ms between evaluations", 8, 500 * time.Microsecond, false},
		{"8 callers, back to back", 8, 0, false},
		{"16 callers, back to back", 16, 0, false},
		{"burst of 64 at once", 64, 0, true},
	}
	var report strings.Builder
	fmt.Fprintf(&report, "native pool: %d slots, %v per row\n\n", e.Stats().Slots, duration)
	fmt.Fprintf(&report, "| Load | Bound value | Evaluations | p50 | p95 | p99 | CPU per evaluation |\n|---|---|---|---|---|---|---|\n")
	for _, size := range []int{4 << 10, 64 << 10, 256 << 10} {
		in := expr.Input{Strings: []expr.HostString{
			{Name: "wl_flow_input", Text: small},
			{Name: "wl_result_fetch_order", Text: syntheticOrder(size)},
		}}
		// Warm the pool and check the program.
		if out, err := e.Eval(context.Background(), p, in); err != nil || string(out) != "true" {
			t.Fatalf("Eval = %s, %v", out, err)
		}
		for _, sh := range shapes {
			lat, cpu := measure(t, e, p, in, sh.callers, sh.pause, sh.burst, duration)
			fmt.Fprintf(&report, "| %s | %s | %d | %s | %s | %s | %s |\n", sh.name, sizeName(size), len(lat),
				pct(lat, 50), pct(lat, 95), pct(lat, 99), round(cpu))
		}
	}
	fmt.Fprintf(&report, "\nwaits for a prepared slot: %d, total %v\n", e.Stats().Waited, round(e.Stats().WaitTime))
	fmt.Print(report.String())
	t.Log("\n" + report.String())
}

func measure(t *testing.T, e *Engine, p expr.Program, in expr.Input, callers int, pause time.Duration, burst bool, d time.Duration) ([]time.Duration, time.Duration) {
	t.Helper()
	var mu sync.Mutex
	var lat []time.Duration
	record := func(start time.Time) {
		el := time.Since(start)
		mu.Lock()
		lat = append(lat, el)
		mu.Unlock()
	}
	cpu0 := cpuTime()
	deadline := time.Now().Add(d)
	var wg sync.WaitGroup
	if burst {
		for time.Now().Before(deadline) {
			// A burst's latency counts from when it is issued, not from
			// when each goroutine first runs.
			start := time.Now()
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := e.Eval(context.Background(), p, in); err != nil {
						t.Error(err)
					}
					record(start)
				}()
			}
			wg.Wait()
			time.Sleep(20 * time.Millisecond)
		}
	} else {
		for range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(deadline) {
					start := time.Now()
					if _, err := e.Eval(context.Background(), p, in); err != nil {
						t.Error(err)
						return
					}
					record(start)
					if pause > 0 {
						time.Sleep(pause)
					}
				}
			}()
		}
		wg.Wait()
	}
	cpu := cpuTime() - cpu0
	if len(lat) == 0 {
		t.Fatal("no evaluations")
	}
	return lat, cpu / time.Duration(len(lat))
}

// syntheticOrder is an invented order object of about size bytes: a
// reference, a flag and line items of about 1 KB each.
func syntheticOrder(size int) []byte {
	var b strings.Builder
	b.WriteString(`{"ref":"ORD-7","archived":false,"items":[`)
	for i := 0; b.Len() < size-200; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"line":%d,"sku":"SKU-%05d","qty":%d,"price":%d.%02d,"note":"%s"}`, i, i, i%7+1, 10+i%90, i%100, strings.Repeat("synthetic line item text ", 37))
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func pct(lat []time.Duration, p int) string {
	s := slices.Clone(lat)
	slices.Sort(s)
	i := (len(s)*p + 99) / 100
	return round(s[max(min(i, len(s))-1, 0)])
}

func round(d time.Duration) string {
	switch {
	case d >= 10*time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	case d >= time.Millisecond:
		return d.Round(10 * time.Microsecond).String()
	}
	return d.Round(time.Microsecond).String()
}

func sizeName(n int) string { return fmt.Sprintf("%d KB", n>>10) }

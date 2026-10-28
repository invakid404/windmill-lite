package expr

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

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// budgetBounds are the budget gate of ADR 0003: the p95 and p99 latency of
// an engine-evaluated predicate over a results value of each size, under
// eight concurrent callers that each issue their next evaluation 0.5 ms
// after the previous one returns.
var budgetBounds = []struct {
	size     int
	p95, p99 time.Duration
}{
	{4 << 10, 500 * time.Microsecond, 1500 * time.Microsecond},
	{64 << 10, 2 * time.Millisecond, 6 * time.Millisecond},
	{256 << 10, 6 * time.Millisecond, 20 * time.Millisecond},
}

// TestBudgetGate measures ADR 0003's budget gate through the whole pipeline
// on the real engine with the default pool: the skip_if predicate
// results.fetch_order.archived ? null : flow_input.order.ref, whose results
// read is a database read of a synthetic order of the row's size (the
// previous step is another), so the evaluation binds the lookup and a small
// flow input and runs the preludes, the program and the serialisation.
//
// It runs only with WL_EXPR_BUDGET set: "gate" fails a paced row past its
// bounds, which holds on the benchmark machine only; "report" prints the
// table, as CI does from its shared runners and from the static musl
// engine (review item cl R3-2). Back-to-back load, and the paced load with
// fetch_order as the previous step (its value then bound twice, as
// previous_result and from memory), are reported beside the gate.
// WL_EXPR_BUDGET_SECONDS sets the length of each row (default 3).
func TestBudgetGate(t *testing.T) {
	mode := os.Getenv("WL_EXPR_BUDGET")
	if mode != "gate" && mode != "report" {
		t.Skip("set WL_EXPR_BUDGET=gate or =report to measure the budget gate")
	}
	seconds := 3.0
	if s := os.Getenv("WL_EXPR_BUDGET_SECONDS"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		seconds = v
	}
	duration := time.Duration(seconds * float64(time.Second))

	p := NewPipeline(realEngine(t, DefaultLimits(), 0))
	e := Compile(SiteSkipIf, "results.fetch_order.archived ? null : flow_input.order.ref")
	if e.Shortcut.ResultsPath != nil {
		t.Fatal("the predicate must reach the engine")
	}
	p.Prepare(e)
	input := value(`{"order":{"ref":"ORD-7"},"note":"synthetic"}`)

	type shape struct {
		name     string
		pause    time.Duration
		previous bool
		gated    bool
	}
	shapes := []shape{
		{"8 callers, 0.5 ms between evaluations", 500 * time.Microsecond, false, true},
		{"8 callers, back to back", 0, false, false},
		{"8 callers, 0.5 ms between, fetch_order the previous step", 500 * time.Microsecond, true, false},
	}
	var report strings.Builder
	fmt.Fprintf(&report, "budget gate (ADR 0003) through the pipeline: %s engine, default pool, %v per row\n\n", testCLibrary, duration)
	fmt.Fprintf(&report, "| Load | Results value | Evaluations | p50 | p95 | p99 | CPU per evaluation | Gate |\n|---|---|---|---|---|---|---|---|\n")
	for _, b := range budgetBounds {
		order := jsonval.FromProducer(syntheticOrder(b.size))
		if _, err := order.Stored(); err != nil {
			t.Fatal(err)
		}
		for _, sh := range shapes {
			s := &Scope{PreviousID: "check_stock", FlowInput: input, Results: budgetResults{order}}
			if sh.previous {
				s.PreviousID = "fetch_order"
				s.Context = []Named{{"previous_result", order}, {"result", order}}
			}
			if out, err := p.Eval(context.Background(), e, s); err != nil || string(out.Producer()) != "true" {
				t.Fatalf("the predicate gives %s, %v", out.Producer(), err)
			}
			lat, cpu := measureBudget(t, p, e, s, 8, sh.pause, duration)
			p95, p99 := percentile(lat, 95), percentile(lat, 99)
			verdict := "reported"
			if sh.gated {
				verdict = fmt.Sprintf("p95 ≤ %v, p99 ≤ %v: ", b.p95, b.p99)
				if p95 <= b.p95 && p99 <= b.p99 {
					verdict += "met"
				} else {
					verdict += "missed"
					if mode == "gate" {
						t.Errorf("%s at %d KB: p95 %v, p99 %v; the gate is p95 ≤ %v, p99 ≤ %v", sh.name, b.size>>10, p95, p99, b.p95, b.p99)
					}
				}
			}
			fmt.Fprintf(&report, "| %s | %d KB | %d | %s | %s | %s | %s | %s |\n", sh.name, b.size>>10, len(lat),
				roundDuration(percentile(lat, 50)), roundDuration(p95), roundDuration(p99), roundDuration(cpu), verdict)
		}
	}
	fmt.Print(report.String())
	t.Log("\n" + report.String())
}

// budgetResults reads every step as the order, from its stored text.
type budgetResults struct{ order jsonval.Value }

func (r budgetResults) Lookup(context.Context, string) (jsonval.Value, error) { return r.order, nil }

func (r budgetResults) Stored(context.Context, string) (jsonval.Value, bool) { return r.order, true }

// measureBudget runs callers goroutines that each evaluate e in s, pause,
// and repeat until d has passed, and returns every latency and the
// process's CPU time per evaluation.
func measureBudget(t *testing.T, p *Pipeline, e *Expr, s *Scope, callers int, pause, d time.Duration) ([]time.Duration, time.Duration) {
	t.Helper()
	var mu sync.Mutex
	var lat []time.Duration
	cpu0 := cpuTime()
	deadline := time.Now().Add(d)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				start := time.Now()
				if _, err := p.Eval(context.Background(), e, s); err != nil {
					t.Error(err)
					return
				}
				el := time.Since(start)
				mu.Lock()
				lat = append(lat, el)
				mu.Unlock()
				if pause > 0 {
					time.Sleep(pause)
				}
			}
		})
	}
	wg.Wait()
	if len(lat) == 0 {
		t.Fatal("no evaluations")
	}
	return lat, (cpuTime() - cpu0) / time.Duration(len(lat))
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

func percentile(lat []time.Duration, p int) time.Duration {
	s := slices.Clone(lat)
	slices.Sort(s)
	i := (len(s)*p + 99) / 100
	return s[max(min(i, len(s))-1, 0)]
}

func roundDuration(d time.Duration) string {
	switch {
	case d >= 10*time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	case d >= time.Millisecond:
		return d.Round(10 * time.Microsecond).String()
	}
	return d.Round(time.Microsecond).String()
}

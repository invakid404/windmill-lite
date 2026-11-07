package expr

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// TestJSExprConcurrentPipeline evaluates expressions from 16 goroutines on
// one pipeline, each with a scope of its own, as a runtime's runs do: texts
// that jsExpr answers, texts it declines after reading a step's result
// (the engine then reuses the read, its lookup answered inline), texts it
// does not parse, and failing reads. Each outcome must be what a pipeline
// with jsExpr off gives for the same scope. Under -race it also checks the
// parse cache, the read memo and the inline lookups for data races.
func TestJSExprConcurrentPipeline(t *testing.T) {
	texts := []string{
		"`${flow_input.a}:${results.st.n + 1}`",
		"results.st.n * 2 + flow_input.a",
		"results.st.n > flow_input.a ? results.st.tag : flow_input.a",
		"results.st.n + results.st.list",
		"results.st.list.map(x => x + flow_input.a)",
		"results.gone.n + 1",
		"`${results.st.tag}`.toUpperCase()",
		"flow_input.a === 3 && results.st.n === 4",
	}
	scope := func(i int) *Scope {
		return &Scope{
			PreviousID: "other",
			FlowInput:  jsonval.FromProducer(fmt.Appendf(nil, `{"a":%d}`, i)),
			Results:    memoryResults{&fakeResults{stored: map[string]string{"st": fmt.Sprintf(`{"n":%d,"tag":"t%d","list":[1,%d]}`, i+1, i, i)}}},
		}
	}
	eval := func(p *Pipeline, text string, i int) string {
		v, err := p.Eval(context.Background(), Compile(SiteInputTransform, text), scope(i))
		if err != nil {
			return "error: " + err.Error()
		}
		return string(v.Producer())
	}
	eng := realEngine(t, DefaultLimits(), 4)
	ref := NewPipeline(eng)
	ref.SetQuickJSOnly(true)
	want := map[[2]int]string{}
	for i := range 16 {
		for j, text := range texts {
			want[[2]int{i, j}] = eval(ref, text, i)
		}
	}
	p := NewPipeline(eng)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			for round := range 5 {
				for j, text := range texts {
					if got := eval(p, text, i); got != want[[2]int{i, j}] {
						t.Errorf("goroutine %d, round %d, %s: %s, want %s", i, round, text, got, want[[2]int{i, j}])
					}
				}
			}
		})
	}
	wg.Wait()
}

// TestInlineLookupsLetHelpersSettle runs a statement that races a helper
// call against an endless loop of results lookups, through the pipeline on
// the real engine: with a lookup that reads memory (answered inline on the
// engine's thread) as with one that does not (on goroutines), the helper's
// answer wins the race well inside the attempt limit. The alias keeps the
// helper call concurrent with the loop instead of awaited where it is
// written, and the loop's read has a computed key, so that no prefetch
// answers it and each read is a lookup (JE-12).
func TestInlineLookupsLetHelpersSettle(t *testing.T) {
	l := DefaultLimits()
	l.Attempt = 5 * time.Second
	p := NewPipeline(realEngine(t, l, 1))
	setRetry(p, 1, 0)
	e := Compile(SiteInputTransform, `const get = variable, k = "a"; return Promise.race([get("u/done"), (async () => { for (;;) await results[k]; })()])`)
	for _, memory := range []bool{false, true} {
		r := &fakeResults{stored: map[string]string{"a": "1"}}
		s := &Scope{PreviousID: "other", Helpers: &fakeHelpers{variables: map[string]string{"u/done": "done"}}, Results: r}
		if memory {
			s.Results = memoryResults{r}
		}
		start := time.Now()
		v, err := p.Eval(context.Background(), e, s)
		if err != nil {
			t.Fatalf("memory %t: %v after %v", memory, err, time.Since(start))
		}
		if got := string(v.Producer()); got != `"done"` {
			t.Fatalf("memory %t: %s", memory, got)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("memory %t: the helper's answer settled after %v", memory, d)
		}
	}
}

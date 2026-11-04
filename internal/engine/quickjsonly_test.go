package engine

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
)

// countingJS is the tests' QuickJS-ng engine, counting the evaluations that
// reach it. It keeps the engine's ExactMemberPaths, so the pipeline's Go
// evaluator may answer instead.
type countingJS struct {
	*quickjs.Engine
	n atomic.Int64
}

func (c *countingJS) Eval(ctx context.Context, p expr.Program, in expr.Input) ([]byte, error) {
	c.n.Add(1)
	return c.Engine.Eval(ctx, p, in)
}

// TestQuickJSOnlyReachesThePipeline runs a flow whose template, arithmetic
// and skip_if the pipeline's Go evaluator answers (jsExpr), reading
// the previous step's result through the run's frame results: with the
// evaluator on, none of them reaches QuickJS-ng; with Config.QuickJSOnly,
// every one does, and the run's outcome is the same. So the switch that
// CI's QuickJS-only part and embedders use does turn the evaluator off.
func TestQuickJSOnlyReachesThePipeline(t *testing.T) {
	flows := map[string]string{"f/t/main": flowOf(
		mod("a", "value", inputs("v", static(`{"n":2}`)), ""),
		mod("b", "echo", inputs("t", js("`${flow_input.x}-${results.a.n}`"), "s", js("results.a.n * 3 + flow_input.y")), `"skip_if":{"expr":"flow_input.y > 100 && results.a.n === 2"}`),
	)}
	prog := compileFlows(t, flows)
	results := map[bool]*Trace{}
	for _, only := range []bool{false, true} {
		c := &countingJS{Engine: jsEng}
		e, err := New(Config{Program: prog, Bundle: "sha256:test", Exec: &scripts{}, JS: c, QuickJSOnly: only})
		if err != nil {
			t.Fatal(err)
		}
		tr := runIt(t, e, `{"x":"id","y":1}`, nil)
		wantStatus(t, tr, conformance.RunSuccess, `{"t":"id-2","s":7}`)
		results[only] = tr
		n := c.n.Load()
		if !only && n != 0 {
			t.Errorf("with the Go evaluator on, %d evaluations reached QuickJS-ng", n)
		}
		if only && n != 3 {
			t.Errorf("with QuickJSOnly, %d evaluations reached QuickJS-ng, want 3", n)
		}
	}
	if string(results[false].Result.Producer()) != string(results[true].Result.Producer()) {
		t.Errorf("the result with the evaluator %s, without %s", results[false].Result.Producer(), results[true].Result.Producer())
	}
}

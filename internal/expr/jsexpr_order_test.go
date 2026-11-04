package expr

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// orderLog records the reads and helper calls of evaluations, in order.
type orderLog struct {
	mu      sync.Mutex
	entries []string
	stored  map[string]string
}

func (l *orderLog) add(s string) {
	l.mu.Lock()
	l.entries = append(l.entries, s)
	l.mu.Unlock()
}

func (l *orderLog) Lookup(_ context.Context, id string) (jsonval.Value, error) {
	l.add("lookup:" + id)
	if t, ok := l.stored[id]; ok {
		return jsonval.FromProducer([]byte(t)), nil
	}
	return jsonval.Value{}, errors.New("Failed to fetch result for step '" + id + "'")
}

func (l *orderLog) Stored(_ context.Context, id string) (jsonval.Value, bool) {
	l.add("stored:" + id)
	t, ok := l.stored[id]
	return jsonval.FromProducer([]byte(t)), ok
}

func (l *orderLog) Variable(_ context.Context, path string) (string, error) {
	l.add("variable:" + path)
	if path == "u/missing" {
		return "", errors.New("Variable not found: " + path)
	}
	return path, nil
}

func (l *orderLog) Resource(_ context.Context, path string) (jsonval.Value, error) {
	l.add("resource:" + path)
	return jsonval.FromProducer([]byte(`{}`)), nil
}

func (l *orderLog) FlowUserState(context.Context, string) (jsonval.Value, error) {
	return jsonval.FromProducer([]byte("null")), nil
}

// memoryLog is orderLog declared to read memory, so that jsExpr reads
// results through it.
type memoryLog struct{ *orderLog }

func (memoryLog) MemoryLookup() {}

// TestJSExprKeepsReadOrder evaluates sequences of expressions one at a time
// on the real engine, as a step's inputs are, with jsExpr on and with it
// off: the reads of results, the helper calls and the outcomes are the same
// and come in the same order, a read jsExpr made before it declined is not
// made again by the engine, and nothing after a failing expression runs.
func TestJSExprKeepsReadOrder(t *testing.T) {
	cases := []struct {
		name  string
		texts []string
	}{
		{"helpers around a lookup", []string{`variable("u/a")`, "results.a.n + 1", `variable("u/b")`}},
		{"helpers around a JE-9 path", []string{`variable("u/a")`, "results.a.n", `variable("u/b")`}},
		{"lookups between helpers", []string{`variable("u/a")`, "`${results.a.n}`", `resource("u/r")`, "results.b", `variable("u/c")`}},
		{"a failure before a lookup", []string{`variable("u/missing")`, "results.a + 1", `variable("u/b")`}},
		{"a failing lookup after a good one", []string{"results.a.n + results.gone.n", `variable("u/b")`}},
		{"short-circuited reads", []string{"results.a.n > 0 ? results.b[0] : results.gone.n", "results.a.n < 0 && results.gone.n", "results.a.n || results.gone.n"}},
		{"a decline after a read", []string{"results.a.n + results.b", "results.a.n + flow_input.x.y"}},
		{"a pure failure after helpers", []string{`variable("u/a")`, "flow_input.x.y.z", `variable("u/c")`}},
	}
	eng := realEngine(t, DefaultLimits(), 2)
	stored := map[string]string{"a": `{"n":1}`, "b": `[1,2]`}
	for _, c := range cases {
		for _, memory := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/memory=%t", c.name, memory), func(t *testing.T) {
				run := func(js bool) ([]string, []string) {
					l := &orderLog{stored: stored}
					p := NewPipeline(eng)
					p.SetQuickJSOnly(!js)
					var outs []string
					for _, text := range c.texts {
						s := &Scope{PreviousID: "other", FlowInput: jsonval.FromProducer([]byte(`{"x":null}`)), Helpers: l, Results: l}
						if memory {
							s.Results = memoryLog{l}
						}
						v, err := p.Eval(context.Background(), Compile(SiteInputTransform, text), s)
						if err != nil {
							outs = append(outs, "error: "+err.Error())
							break
						}
						outs = append(outs, string(v.Producer()))
					}
					return l.entries, outs
				}
				onLog, onOuts := run(true)
				offLog, offOuts := run(false)
				if !slices.Equal(onLog, offLog) {
					t.Errorf("reads and calls\n jsExpr on  %v\n jsExpr off %v", onLog, offLog)
				}
				if !slices.Equal(onOuts, offOuts) {
					t.Errorf("outcomes\n jsExpr on  %v\n jsExpr off %v", onOuts, offOuts)
				}
			})
		}
	}
}

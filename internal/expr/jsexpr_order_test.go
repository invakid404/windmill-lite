package expr

import (
	"context"
	"errors"
	"slices"
	"strings"
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
// off, over a lookup that reads memory (which jsExpr reads through, and the
// engine calls inline, on its own thread) and over one that does not (which
// the engine calls on goroutines): in all four, each expression reads the
// same results, the steps its text names (JE-12), and the helper calls and
// the outcomes are the same and come in the same order; a read jsExpr made
// before it declined is not made again by the engine, and nothing after a
// failing expression runs. The prefetch resolves an expression's steps at
// once, so their reads are compared as a set: on goroutines they finish in
// any order.
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
		{"a failed read not reached", []string{"true ? results.a.n : results.gone.n", `variable("u/b")`}},
		{"a decline after a read", []string{"results.a.n + results.b", "results.a.n + flow_input.x.y"}},
		{"a pure failure after helpers", []string{`variable("u/a")`, "flow_input.x.y.z", `variable("u/c")`}},
	}
	eng := realEngine(t, DefaultLimits(), 2)
	stored := map[string]string{"a": `{"n":1}`, "b": `[1,2]`}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := func(js, memory bool) ([]string, []string) {
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
					l.add("--")
					if err != nil {
						outs = append(outs, "error: "+err.Error())
						break
					}
					outs = append(outs, string(v.Producer()))
				}
				return prefetchSets(l.entries), outs
			}
			offLog, offOuts := run(false, false)
			for _, v := range [][2]bool{{false, true}, {true, false}, {true, true}} {
				log, outs := run(v[0], v[1])
				if !slices.Equal(log, offLog) {
					t.Errorf("reads and calls\n jsExpr %t, memory %t: %v\n jsExpr off, no memory: %v", v[0], v[1], log, offLog)
				}
				if !slices.Equal(outs, offOuts) {
					t.Errorf("outcomes\n jsExpr %t, memory %t: %v\n jsExpr off, no memory: %v", v[0], v[1], outs, offOuts)
				}
			}
		})
	}
}

// prefetchSets sorts the leading lookups of each expression's entries,
// which "--" ends: the reads of the steps its text names, which the
// program's prefetch resolves at once, before any helper call.
func prefetchSets(entries []string) []string {
	out := slices.Clone(entries)
	leading, start := true, 0
	for i := 0; i <= len(out); i++ {
		if i < len(out) && leading && strings.HasPrefix(out[i], "lookup:") {
			continue
		}
		if leading {
			slices.Sort(out[start:i])
			leading = false
		}
		if i < len(out) && out[i] == "--" {
			leading, start = true, i+1
		}
	}
	return out
}

// TestBindInlinesMemoryLookups: the engine may answer the results lookup on
// its own thread (HostFunc.Inline) exactly when it reads memory
// (MemoryResults), directly or through jsExpr's read memo; the helpers,
// which may wait on a provider, never.
func TestBindInlinesMemoryLookups(t *testing.T) {
	e := Compile(SiteInputTransform, "`${results.a.n}:${variable(\"u/a\")}`")
	plain := &fakeResults{stored: map[string]string{"a": `{"n":1}`}}
	for _, c := range []struct {
		name    string
		results Results
		inline  bool
	}{
		{"a lookup of its own", plain, false},
		{"a memory lookup", memoryResults{plain}, true},
		{"a memory lookup through the memo", &memoResults{Results: memoryResults{plain}}, true},
		{"a lookup of its own through the memo", &memoResults{Results: plain}, false},
	} {
		in := bind(e, &Scope{Results: c.results})
		found := false
		for _, f := range in.Funcs {
			if f.Name == hostResults {
				found = true
				if f.Inline != c.inline {
					t.Errorf("%s: the results lookup is inline: %t, want %t", c.name, f.Inline, c.inline)
				}
			} else if f.Inline {
				t.Errorf("%s: %s is inline", c.name, f.Name)
			}
		}
		if !found {
			t.Errorf("%s: no results lookup bound", c.name)
		}
	}
}

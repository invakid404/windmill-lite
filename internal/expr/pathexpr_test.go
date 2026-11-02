package expr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// TestParsePathExpr holds parsePathExpr to its grammar: member paths of
// one root, null tests and objects of member paths, and nothing that runs
// code, compares two values that could be objects, mixes roots, or that a
// rewrite would change.
func TestParsePathExpr(t *testing.T) {
	for text, want := range map[string]string{
		"flow_input.a":      "single flow_input 0",
		"results.r.subject": "single results.r 1",
		"results.r == null ? null : results.r.id":          "cond results.r 2",
		"results.r != null ? results.r.id : undefined":     "cond results.r 2",
		`results.r === undefined ? null : results["r"].x`:  "cond results.r 2",
		"results?.r !== null":                              "test results.r 1",
		"previous_result.a==null?previous_result.b:null":   "cond previous_result 0",
		"flow_input.v == null ? undefined : flow_input.v":  "cond flow_input 0",
		"({ id: results.r.id, text: results.r?.text })":    "object results.r 2",
		`({ "a b": flow_input.x, 'c': flow_input["y"], })`: "object flow_input 0",
		"( { a : flow_input . x } )":                       "",
		"results.r == null ? null : results.s.id":          "", // two roots
		"results.r === results.r":                          "", // identity
		"results.r.x = 1":                                  "",
		"results.r.items.map((i) => i.id)":                 "",
		"results.r == null ? null : results.r.id()":        "",
		"results.r == null ? 1 : results.r.id":             "",
		"results.r == null?.x ? 1 : 2":                     "",
		"results.r == null ?.5 : 2":                        "",
		"({ __proto__: results.r.a })":                     "",
		`({ "__proto__": results.r.a })`:                   "",
		`({ "results.r": results.r.a })`:                   "", // a rewrite in the key
		"({ a: results.r.a, b: flow_input.b })":            "",
		"({})":                                             "",
		"({ a })":                                          "",
		"results.r.a // c":                                 "",
		"results.r == null ? null : results.r.id; 1":       "",
		"results.r==nullish":                               "",
		"flow_input.a == null && flow_input.b":             "",
		"variable(x) == null":                              "",
		"error.message == null ? 1 : 2":                    "",
	} {
		x := parsePathExpr(Compile(SiteInputTransform, text))
		got := ""
		if x != nil {
			root := x.root
			if root == "" {
				root = "results." + x.step
			}
			got = fmt.Sprintf("%s %s %d", map[pathExprKind]string{pathSingle: "single", pathTest: "test", pathCond: "cond", pathObject: "object"}[x.kind], root, x.reads)
		}
		if got != want {
			t.Errorf("%s: %q, want %q", text, got, want)
		}
	}
}

// TestSharedRead holds the shared read (lever J3, item 1) to the reads it
// replaces: on the real engine, a text that reads one step's result
// through the lookup more than once gives the same answer, or the same
// error, with one lookup as with one per read. Texts that could tell one
// read from two keep a read each, as JE-18 rule 2 has it: two reads give
// two objects, and a write through one does not show through the other;
// the previous step's result, read from memory, is one object (rule 1).
func TestSharedRead(t *testing.T) {
	ctx := context.Background()
	p := NewPipeline(realEngine(t, DefaultLimits(), 0))
	stored := map[string]string{
		"r": `{"id":"m1","subject":"s","text":"long text","n":null,"a":[1,{"b":2}],"__proto__":{"x":1},"0":"zero"}`,
		"z": `null`,
		"p": `{"id":"prev"}`,
	}
	scope := func(r *fakeResults) *Scope {
		return &Scope{PreviousID: "p", Context: []Named{{Name: "previous_result", Value: jsonval.FromProducer([]byte(stored["p"]))}}, Results: r}
	}
	lookups := func(r *fakeResults, id string) int { return strings.Count(strings.Join(r.asked, " "), "lookup:"+id) }
	for _, text := range []string{
		"results.r == null ? null : results.r.id",
		"results.r != null ? results.r.a : null",
		"results.r === null ? null : results.r.missing",
		"results.z == null ? null : results.z.id",
		"results.z !== undefined ? results.z : null",
		"results.r.n === null ? results.r.subject : results.r.text",
		"({ id: results.r.id, subject: results.r.subject, text: results.r.text })",
		"({ b: results.r.a[1].b, p: results.r.__proto__, z: results.r[\"0\"], m: results.r.missing })",
		"results.z == null ? null : results.z.x.y",
		"results.r.n == null ? results.r.n.x : undefined",
		"results.f == null ? null : results.f.id",
	} {
		t.Run(text, func(t *testing.T) {
			e := Compile(SiteInputTransform, text)
			if id := parsePathExpr(e).sharedRead(); id == "" {
				t.Fatalf("no shared read")
			}
			eval := func(shared bool) (string, int) {
				r := &fakeResults{stored: stored, fail: map[string]string{"f": "Failed to fetch result for step 'f': synthetic"}}
				in := bind(e, scope(r))
				if shared {
					in.Strings = append(in.Strings, HostString{hostSharedRead, []byte(parsePathExpr(e).sharedRead())})
				}
				out, err := p.evaluate(ctx, e, in)
				if err != nil {
					var ee *EvalError
					if !errors.As(err, &ee) {
						t.Fatal(err)
					}
					return "error: " + ee.Message, lookups(r, parsePathExpr(e).step)
				}
				return string(out), lookups(r, parsePathExpr(e).step)
			}
			got, n := eval(true)
			want, wn := eval(false)
			if got != want {
				t.Fatalf("shared: %s; a read each: %s", got, want)
			}
			if n != 1 || wn < n {
				t.Fatalf("%d lookups shared, %d without", n, wn)
			}
			// Through the pipeline, which binds the shared read itself.
			r := &fakeResults{stored: stored, fail: map[string]string{"f": "Failed to fetch result for step 'f': synthetic"}}
			out, err := p.Eval(ctx, e, scope(r))
			pg := string(out.Producer())
			if err != nil {
				pg = "error: " + err.(*EvalError).Message
			}
			if pg != want || lookups(r, parsePathExpr(e).step) != 1 {
				t.Fatalf("the pipeline: %s with %d lookups; want %s with one", pg, lookups(r, parsePathExpr(e).step), want)
			}
		})
	}
	// What a shared read could change, and so does not share.
	for text, want := range map[string]string{
		"results.r === results.r":                  "false",
		"(results.r.id = 1, results.r.id)":         `"m1"`,
		"results.p === results.p":                  "true",
		"(results.p.id = 1, results.p.id)":         "1",
		"[results.r, results.r].length":            "2",
		"results.r.a !== null ? results.r.a : 1":   "[1,{\"b\":2}]",
		"Object.keys(results.r).concat(results.r)": "",
	} {
		r := &fakeResults{stored: stored}
		e := Compile(SiteInputTransform, text)
		out, err := p.Eval(ctx, e, scope(r))
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		if want != "" && string(out.Producer()) != want {
			t.Errorf("%s = %s, want %s", text, out.Producer(), want)
		}
		if id := parsePathExpr(e).sharedRead(); id != "" && strings.Contains(text, "===") {
			t.Errorf("%s shares the read of %s", text, id)
		}
	}
}

package expr

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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

// TestSharedRead holds the shared read to the reads it
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

// TestPathExprsMatchEngine is the differential test of the null tests and
// objects of member paths that the pipeline answers itself, as
// TestMemberPathsMatchEngine is for single paths: generated JSON values,
// and generated tests (==, !=, ===, !== against null and
// undefined, alone or choosing between paths, null and undefined) and
// object literals of paths over them, each read by pathExpr and by the
// expression's program on the real engine. Wherever pathExpr answers, the
// engine must give the same text and no error. The values and paths are
// TestMemberPathsMatchEngine's: undefined against null, missing keys, null
// on the way, arrays and their lengths, keys Object.prototype has, and
// repeated, integer-like and quoted keys in the literals.
func TestPathExprsMatchEngine(t *testing.T) {
	p := NewPipeline(realEngine(t, DefaultLimits(), 0))
	rng := rand.New(rand.NewPCG(3, 5))
	const n = 6000
	count := map[pathResult]int{}
	kinds := map[pathExprKind]int{}
	for range n {
		c, text := genFormCase(rng)
		r, kind := c.checkForm(t, p, text)
		count[r]++
		if r == pathAnswered {
			kinds[kind]++
		}
		if t.Failed() {
			return
		}
	}
	t.Logf("%d cases: %v; answered by kind %v", n, count, kinds)
	if count[pathAnswered] < n/3 || count[pathDeclinedEngineOK] == 0 || count[pathDeclinedEngineErr] == 0 || kinds[pathTest] == 0 || kinds[pathCond] == 0 || kinds[pathObject] == 0 {
		t.Fatalf("the cases do not cover answers, declines and engine errors enough: %v, %v", count, kinds)
	}
}

// genFormCase makes a case and a test, a conditional or an object literal
// of paths over its value.
func genFormCase(rng *rand.Rand) (pathCase, string) {
	c, v := genPathBase(rng)
	sp := func() string { return []string{"", " ", " ", "  ", "\n"}[rng.IntN(5)] }
	path := func() string {
		root := c.root
		if root == "results" {
			root = []string{"results.st", "results?.st", `results["st"]`}[rng.IntN(3)]
		}
		return root + genPath(rng, v)
	}
	lit := func() string { return []string{"null", "undefined"}[rng.IntN(2)] }
	term := func() string {
		if rng.IntN(3) == 0 {
			return lit()
		}
		return path()
	}
	op := []string{"==", "!=", "===", "!=="}[rng.IntN(4)]
	switch rng.IntN(3) {
	case 0:
		return c, path() + sp() + op + sp() + lit()
	case 1:
		return c, path() + sp() + op + sp() + lit() + sp() + "?" + sp() + term() + sp() + ":" + sp() + term()
	}
	keys := []string{"a", "b", "id", "n", "0", "1", "10", "007", "x y", "length", "toString", "ü", "a"}
	var b strings.Builder
	b.WriteString("({" + sp())
	for i := range 1 + rng.IntN(4) {
		if i > 0 {
			b.WriteString("," + sp())
		}
		k := keys[rng.IntN(len(keys))]
		switch {
		case isIdent(k) && rng.IntN(2) == 0:
			b.WriteString(k)
		case rng.IntN(2) == 0:
			b.WriteString(`"` + k + `"`)
		default:
			b.WriteString("'" + k + "'")
		}
		b.WriteString(sp() + ":" + sp() + path())
	}
	if rng.IntN(5) == 0 {
		b.WriteString(",")
	}
	b.WriteString(sp() + "})")
	return c, b.String()
}

// checkForm runs a form both ways and fails t when pathExpr answers what
// the engine does not.
func (c pathCase) checkForm(t *testing.T, p *Pipeline, text string) (pathResult, pathExprKind) {
	t.Helper()
	ctx := context.Background()
	e := Compile(SiteInputTransform, text)
	x := parsePathExpr(e)
	if x == nil {
		return pathNotPath, 0
	}
	s := c.scope()
	in := bind(e, s)
	if err := checkDepth(e, s, in); err != nil {
		t.Fatalf("%s over %s: %v", text, c.text, err)
	}
	got, ok := x.eval(in)
	want, err := p.evaluate(ctx, e, bind(e, c.scope()))
	switch {
	case !ok && err != nil:
		return pathDeclinedEngineErr, x.kind
	case !ok:
		return pathDeclinedEngineOK, x.kind
	case err != nil:
		t.Errorf("%s over %s (%+v): pathExpr answered %s, the engine failed: %v", text, c.text, c, got, err)
	case string(got) != string(want):
		t.Errorf("%s over %s (%+v): pathExpr answered %s, the engine %s", text, c.text, c, got, want)
	}
	return pathAnswered, x.kind
}

// skipChecked ends every checked value where skip, which checks it, ends:
// on generated values with escaped quotes, runs of backslashes and
// brackets inside strings, nested containers and numbers.
func TestSkipChecked(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 4))
	texts := []string{`"a\"b"`, `"a\\"`, `"a\\\"b\\\\"`, `{"k":"}]\"[{","n":[1,{"x":"]"}]}`, `[]`, `{}`, `-1.5e+7`, `true`, `null`, `"""`}
	for range 3000 {
		var b strings.Builder
		genJSON(rng, &b, 0)
		texts = append(texts, b.String())
	}
	for _, text := range texts {
		for _, tail := range []string{"", ",1", "] ", "}"} {
			s := []byte(text + tail)
			want := &pathScanner{s: s}
			if !want.skip(0) {
				t.Fatalf("not a checked value: %s", text)
			}
			got := &pathScanner{s: s}
			got.skipChecked()
			if got.i != want.i {
				t.Fatalf("%s%s: skipChecked ends at %d, skip at %d", text, tail, got.i, want.i)
			}
		}
	}
}

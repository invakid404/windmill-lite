package expr

import (
	"context"
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// TestParseJSExpr holds parseJSExpr to its grammar.
func TestParseJSExpr(t *testing.T) {
	for text, want := range map[string]bool{
		"`${flow_input.tenant}:${results.s1.id}`":               true,
		"results.s2.n + 1":                                      true,
		"results.s1.ok === true && flow_input.mode !== \"dry\"": true,
		"results.s3.n < 0":                                      true,
		"flow_input.mode === 'full' ? results.a.x : null":       true,
		"(flow_input.a + flow_input.b).length":                  true,
		"!flow_input.x || -flow_input.y * 2 % 3 / 4 - 1 >= .5":  true,
		"results.r.text.length":                                 true,
		"flow_input.a == 1":                                     false,
		"flow_input.a = 1":                                      false,
		"flow_input.a++":                                        false,
		"flow_input.a --flow_input.b":                           false,
		"[flow_input.region, results.s2.id].join(\"/\")":        false,
		"results.p.html.includes(\"<title>\")":                  false,
		"flow_input.a // c":                                     false,
		"flow_input.a ?? 1":                                     false,
		"flow_input.a\n+ 1":                                     false,
		"variable(\"x\") + 1":                                   false,
		"0x10 + 1":                                              false,
		"`a\\u0041`":                                            false,
		"flow_input.__proto__":                                  false,
		"flow_input?.[0]":                                       false,
		"Math.max(1, 2)":                                        false, // a call
		"Math.PI + 1":                                           true,  // parses; eval declines the unbound root
		"error.message + 1":                                     false,
	} {
		if got := parseJSExpr(Compile(SiteInputTransform, text)) != nil; got != want {
			t.Errorf("%q: parsed %t, want %t", text, got, want)
		}
	}
}

// TestJSExprsMatchEngine is the differential test of jsExpr against the
// real engine: generated expressions of its grammar over generated JSON
// values bound as flow_input, previous_result, a step's result read from
// memory (the previous id) and one read through the lookup, sometimes
// failing. Wherever jsExpr answers, the engine must give the same text; it
// may decline anything.
func TestJSExprsMatchEngine(t *testing.T) {
	p := NewPipeline(realEngine(t, DefaultLimits(), 0))
	n := 6000
	if testing.Short() {
		n = 1500
	}
	if v, err := strconv.Atoi(os.Getenv("WL_JSEXPR_CASES")); err == nil && v > 0 {
		n = v
	}
	seed := uint64(7)
	if v, err := strconv.Atoi(os.Getenv("WL_JSEXPR_SEED")); err == nil {
		seed = uint64(v)
	}
	rng := rand.New(rand.NewPCG(seed, 11))
	count := map[pathResult]int{}
	for range n {
		c := genJSCase(rng)
		count[c.check(t, p)]++
		if t.Failed() {
			return
		}
	}
	t.Logf("%d cases: %v", n, count)
	if count[pathAnswered] < n/10 || count[pathDeclinedEngineOK] == 0 || count[pathDeclinedEngineErr] == 0 {
		t.Fatalf("the cases do not cover answers and declines enough: %v", count)
	}
}

type jsCase struct {
	text                string
	flowInput, prev, st string
	failSt              bool
}

func (c jsCase) scope() *Scope {
	r := &fakeResults{stored: map[string]string{"st": c.st, "pv": `{"from":"the lookup"}`}}
	if c.failSt {
		r.fail = map[string]string{"st": "Failed to fetch result for step 'st': synthetic"}
	}
	return &Scope{
		PreviousID: "pv",
		FlowInput:  jsonval.FromProducer([]byte(c.flowInput)),
		Context:    []Named{{Name: "previous_result", Value: jsonval.FromProducer([]byte(c.prev))}},
		Results:    memoryResults{r},
	}
}

// memoryResults is fakeResults declared to read memory (MemoryResults), as
// the engine's frame results are, so that jsExpr reads through its lookup.
type memoryResults struct{ *fakeResults }

func (memoryResults) MemoryLookup() {}

func (c jsCase) check(t *testing.T, p *Pipeline) pathResult {
	t.Helper()
	ctx := context.Background()
	e := Compile(SiteInputTransform, c.text)
	x := parseJSExpr(e)
	if x == nil {
		return pathNotPath
	}
	s := c.scope()
	if err := checkDepth(e, s, bind(e, s)); err != nil {
		return pathNotPath
	}
	got, ok := x.eval(ctx, s, jsLimits{limits: DefaultLimits(), bound: bind(e, s).Strings, memoryReads: true})
	want, err := p.evaluate(ctx, e, bind(e, c.scope()))
	switch {
	case !ok && err != nil:
		return pathDeclinedEngineErr
	case !ok:
		return pathDeclinedEngineOK
	case err != nil:
		t.Errorf("%s (flow_input %s, previous_result %s, st %s, fail %t): jsExpr answered %s, the engine failed: %v", c.text, c.flowInput, c.prev, c.st, c.failSt, got, err)
	case string(got) != string(want):
		t.Errorf("%s (flow_input %s, previous_result %s, st %s, fail %t): jsExpr answered %s, the engine %s", c.text, c.flowInput, c.prev, c.st, c.failSt, got, want)
	}
	return pathAnswered
}

// twinComponent finds a path's components after its root: .k, ?.k or
// ["k"] (group 2: the identifier; group 3: the quoted key).
var twinComponent = regexp.MustCompile(`(\?\.|\.)([A-Za-z_$][A-Za-z0-9_$]*)|\["([^"\\]*)"\]`)

// genJSCase makes three values and an expression over them.
func genJSCase(rng *rand.Rand) jsCase {
	vals := make([]*genValue, 3)
	texts := make([]string, 3)
	for i := range vals {
		vals[i] = genPathValue(rng, 0, rng.IntN(8) == 0)
		texts[i] = vals[i].write(rng)
	}
	c := jsCase{flowInput: texts[0], prev: texts[1], st: texts[2], failSt: rng.IntN(10) == 0}
	sp := func() string { return []string{"", " ", " ", "  "}[rng.IntN(4)] }
	path := func() string {
		switch rng.IntN(6) {
		case 0, 1:
			return "flow_input" + genPath(rng, vals[0])
		case 2:
			return "previous_result" + genPath(rng, vals[1])
		case 3:
			return "results.pv" + genPath(rng, vals[1])
		case 4:
			return "results.st" + genPath(rng, vals[2])
		}
		return []string{"results.zz", "results.zz.a", "undefined", "results[\"st\"]" + genPath(rng, vals[2])}[rng.IntN(4)]
	}
	// twin respells one component of a path in another form (.k, ?.k,
	// ["k"], ["?k"]): two reads of one expression that differ only there
	// (the read memo's key must tell them apart).
	twin := func(p string) string {
		ms := twinComponent.FindAllStringSubmatchIndex(p, -1)
		if len(ms) == 0 {
			return p
		}
		m := ms[rng.IntN(len(ms))]
		key := ""
		if m[4] >= 0 {
			key = p[m[4]:m[5]]
		} else {
			key = p[m[6]:m[7]]
		}
		forms := []string{"?." + key, "." + key, `["?` + key + `"]`, `["` + key + `"]`}
		if !isIdent(key) {
			forms = []string{`["?` + key + `"]`, `["` + key + `"]`}
		}
		return p[:m[0]] + forms[rng.IntN(len(forms))] + p[m[1]:]
	}
	lit := func() string {
		return []string{"0", "1", "-1", "2.5", "1e21", "0.1", "100", "true", "false", "null", "undefined", `""`, `"a"`, `'b'`, `"ü😀"`, `"10"`, `"\n"`, `"\\"`}[rng.IntN(18)]
	}
	var term func(d int) string
	var expr func(d int) string
	term = func(d int) string {
		switch k := rng.IntN(10); {
		case k < 5 || d > 2:
			if rng.IntN(4) == 0 {
				return lit()
			}
			if rng.IntN(4) == 0 {
				p := path()
				return "(" + p + sp() + []string{"+", "===", "||"}[rng.IntN(3)] + sp() + twin(p) + ")"
			}
			return path()
		case k < 7:
			var b strings.Builder
			b.WriteString("`")
			for i := range 1 + rng.IntN(3) {
				b.WriteString([]string{"", "a", ":", " x ", "ü"}[rng.IntN(5)])
				if i < 3 {
					b.WriteString("${" + expr(d+1) + "}")
				}
			}
			b.WriteString("`")
			return b.String()
		case k < 8:
			return "(" + expr(d+1) + ")" + []string{"", ".length"}[rng.IntN(2)]
		case k < 9:
			if rng.IntN(2) == 0 {
				return "Boolean(" + expr(d+1) + ")"
			}
			return "!" + term(d+1)
		}
		return "-" + term(d+1)
	}
	ops := []string{"+", "+", "-", "*", "/", "%", "<", "<=", ">", ">=", "===", "!==", "&&", "||"}
	expr = func(d int) string {
		switch k := rng.IntN(10); {
		case k < 3 || d > 2:
			return term(d)
		case k < 9:
			return term(d) + sp() + ops[rng.IntN(len(ops))] + sp() + term(d)
		}
		return term(d) + sp() + "?" + sp() + term(d) + sp() + ":" + sp() + term(d)
	}
	c.text = expr(0)
	return c
}

// TestJSExprReadMemoKeys: reads that differ only in
// whether a component is optional, or in a key that spells like another
// component (a leading "?", dots, brackets, quotes), must not share the
// read memo. Each case runs both ways on the real engine: where jsExpr
// answers, the engine answers the same; where the engine fails, jsExpr
// declines.
func TestJSExprReadMemoKeys(t *testing.T) {
	p := NewPipeline(realEngine(t, DefaultLimits(), 1))
	values := []string{
		`{"a":{"b":1,"?b":2,"?":3,"?.b":4,".":5,"a.b":6,"]":7,"[":8,"b?":9,"?\"b":10}}`,
		`{"a":{"?b":2}}`,
		`{"a":{"b":1}}`,
		`{"a":null}`,
		`{"a":{}}`,
		`{}`,
	}
	reads := []string{
		`flow_input.a?.b`, `flow_input.a.b`, `flow_input.a["?b"]`, `flow_input.a['?b']`, `flow_input.a["?"]`,
		`flow_input.a["?.b"]`, `flow_input.a["."]`, `flow_input.a["a.b"]`, `flow_input.a["]"]`, `flow_input.a["["]`,
		`flow_input.a["b?"]`, `flow_input.a['?"b']`, `flow_input?.a?.b`, `flow_input.a?.["?b"]`,
	}
	answered, declined := 0, 0
	for _, v := range values {
		for _, r1 := range reads {
			for _, r2 := range reads {
				for _, text := range []string{r1 + " + " + r2, "`${" + r1 + "}|${" + r2 + "}`", r1 + " === " + r2} {
					c := jsCase{text: text, flowInput: v, prev: "null", st: "null"}
					switch c.check(t, p) {
					case pathAnswered:
						answered++
					case pathDeclinedEngineOK, pathDeclinedEngineErr:
						declined++
					}
					if t.Failed() {
						t.Fatalf("flow_input %s", v)
					}
				}
			}
		}
	}
	t.Logf("%d answered by jsExpr, %d declined", answered, declined)
	if answered < 500 {
		t.Fatalf("too few answered cases: %d", answered)
	}
}

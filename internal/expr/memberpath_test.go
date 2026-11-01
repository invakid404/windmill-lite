package expr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// TestMemberPathsMatchEngine is the differential test of the pipeline's
// member paths: generated JSON values and generated paths over them, read
// both by memberPath and by the expression's program on the real engine.
// Wherever memberPath answers, the engine must give the same text and no
// error. The values hold repeated keys, integer-like keys, keys that
// Object.prototype has, escapes, surrogate pairs and unpaired surrogates,
// numbers at the edges of a double, and text that is not JSON; the paths
// hold missing members, indexes past the end, length, optional chaining on
// null and on undefined, quoted keys, and reads past a primitive.
func TestMemberPathsMatchEngine(t *testing.T) {
	p := NewPipeline(realEngine(t, DefaultLimits(), 0))
	if !p.paths {
		t.Fatal("the real engine does not say it evaluates member paths exactly")
	}
	ctx := context.Background()

	t.Run("Object.prototype", func(t *testing.T) {
		e := Compile(SiteInputTransform, "Object.getOwnPropertyNames(Object.prototype).sort()")
		out, err := p.Eval(ctx, e, &Scope{})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		if err := json.Unmarshal(out.Producer(), &names); err != nil {
			t.Fatal(err)
		}
		if want := slices.Sorted(slices.Values(objectProtoNames)); !slices.Equal(names, want) {
			t.Fatalf("the engine's Object.prototype has %v, memberPath declines %v", names, want)
		}
	})

	t.Run("numbers", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(7, 11))
		for batch := range 40 {
			var b strings.Builder
			b.WriteByte('[')
			for i := range 100 {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(genNumber(rng))
			}
			b.WriteByte(']')
			c := pathCase{root: "flow_input", text: b.String()}
			if r := c.check(t, p); r != pathAnswered {
				t.Fatalf("batch %d: %s", batch, r)
			}
		}
	})

	t.Run("generated", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(1, 2))
		const n = 6000
		count := map[pathResult]int{}
		for range n {
			c := genPathCase(rng)
			count[c.check(t, p)]++
			if t.Failed() {
				return
			}
		}
		t.Logf("%d cases: %v", n, count)
		if count[pathAnswered] < n/2 || count[pathDeclinedEngineOK] == 0 || count[pathDeclinedEngineErr] == 0 {
			t.Fatalf("the cases do not cover answers, declines and engine errors enough: %v", count)
		}
	})
}

// pathCase is one generated case: a path text over a root, the root's
// JSON text, and how the scope holds it.
type pathCase struct {
	// root is flow_input, flow_env, previous_result, or results for
	// results.<id>.
	root string
	// path is the text after the root; for results, after results.st,
	// results?.st or results["st"], whichever spell is.
	path, spell string
	text        string
	// memory makes st the previous id, so that results.st reads
	// previous_result; unbound leaves the root out of the scope; fail
	// fails the results lookup.
	memory, unbound, fail bool
}

type pathResult string

const (
	pathAnswered          pathResult = "answered"
	pathDeclinedEngineOK  pathResult = "declined, engine answered"
	pathDeclinedEngineErr pathResult = "declined, engine failed"
	pathNotPath           pathResult = "not a member path"
)

func (c pathCase) expr() string {
	if c.root != "results" {
		return c.root + c.path
	}
	return c.spell + c.path
}

func (c pathCase) scope() *Scope {
	s := &Scope{PreviousID: "other"}
	v := jsonval.FromProducer([]byte(c.text))
	switch {
	case c.unbound:
	case c.root == "flow_input":
		s.FlowInput = v
	case c.root == "flow_env":
		s.FlowEnv = v
	case c.root == "previous_result":
		s.Context = []Named{{Name: "previous_result", Value: v}}
	case c.memory:
		s.PreviousID = "st"
		s.Context = []Named{{Name: "previous_result", Value: v}}
		s.Results = &fakeResults{stored: map[string]string{"st": `{"from":"the lookup"}`}}
	default:
		r := &fakeResults{stored: map[string]string{"st": c.text}}
		if c.fail {
			r.fail = map[string]string{"st": "Failed to fetch result for step 'st': synthetic"}
		}
		s.Results = r
	}
	return s
}

// check runs the case both ways and fails t when memberPath answers what
// the engine does not.
func (c pathCase) check(t *testing.T, p *Pipeline) pathResult {
	t.Helper()
	ctx := context.Background()
	e := Compile(SiteInputTransform, c.expr())
	mp := parseMemberPath(e)
	if mp == nil {
		return pathNotPath
	}
	s := c.scope()
	in := bind(e, s)
	if err := checkDepth(e, s, in); err != nil {
		t.Fatalf("%s over %s: %v", c.expr(), c.text, err)
	}
	got, ok := mp.eval(in)
	want, err := p.evaluate(ctx, e, bind(e, c.scope()))
	switch {
	case !ok && err != nil:
		return pathDeclinedEngineErr
	case !ok:
		return pathDeclinedEngineOK
	case err != nil:
		t.Errorf("%s over %s (%+v): memberPath answered %s, the engine failed: %v", c.expr(), c.text, c, got, err)
	case string(got) != string(want):
		t.Errorf("%s over %s (%+v): memberPath answered %s, the engine %s", c.expr(), c.text, c, got, want)
	}
	return pathAnswered
}

// genPathCase makes a case: a root, a value, and a path that mostly
// follows the value's members and sometimes leaves them.
func genPathCase(rng *rand.Rand) pathCase {
	c := pathCase{}
	switch rng.IntN(6) {
	case 0:
		c.root = "flow_input"
	case 1:
		c.root = "flow_env"
	case 2:
		c.root = "previous_result"
	default:
		c.root = "results"
		c.spell = []string{"results.st", "results?.st", `results["st"]`}[rng.IntN(3)]
		// A results.<id> read is answered only from memory, the previous
		// result; a read through the lookup stays the engine's, so a few
		// cases check that it is declined.
		c.memory = rng.IntN(5) > 0
		c.fail = !c.memory && rng.IntN(4) == 0
	}
	c.unbound = c.root == "previous_result" && rng.IntN(30) == 0
	// A context value with a \u escape is rebuilt (JE-16), which memberPath
	// leaves to the engine, so most context values do without them.
	escapes := c.root == "results" && !c.memory || rng.IntN(10) == 0
	var v *genValue
	if rng.IntN(25) == 0 {
		c.text = notJSON[rng.IntN(len(notJSON))]
	} else {
		v = genPathValue(rng, 0, escapes)
		c.text = v.write(rng)
	}
	c.path = genPath(rng, v)
	return c
}

// notJSON are texts JSON.parse rejects, or reads differently from a
// careless parser.
var notJSON = []string{
	``, ` `, `{"a":1,}`, `[1,2`, `{'a':1}`, `NaN`, `01`, `1.`, `.5`, `-`, `+1`, `"\x"`, "\ufeff{}", `{"a":1} x`,
	`{"a" 1}`, `[1 2]`, `tru`, `nul`, `"tab	in string"`, `{"a":1}{}`, `"\u12"`, `1e`, `0x10`, `Infinity`, `[,]`,
}

// genValue is a generated JSON value, kept with the spellings its text
// uses, so that paths can follow it.
type genValue struct {
	kind  byte // o, a, s, n (number), l (literal)
	text  string
	keys  []genKey
	elems []*genValue
}

// genKey is a member's key as the text spells it and as JavaScript reads it.
type genKey struct {
	spelled, key string
	val          *genValue
}

// genKeys are the keys members get: plain keys, integer-like keys, keys
// Object.prototype has, and keys a path can only quote.
var genKeys = []string{
	"a", "b", "id", "items", "name", "data", "n", "x_1", "$k", "0", "1", "2", "10", "007", "-1", "1.5", "4294967294", "4294967295",
	"2147483647", "2147483648", "length", "constructor", "toString", "hasOwnProperty", "valueOf", "__proto__", "", "with space",
	"ü", "😀", "quo\"te", "back\\slash", "tab\t", "a-b",
	// Helper-call texts, which JE-11 rewrites inside quoted keys too.
	"variable(x)", "resource(x)", "flow_user_state(x)", "(await resource(x))", "resource(",
}

func genPathValue(rng *rand.Rand, depth int, escapes bool) *genValue {
	k := rng.IntN(10)
	if depth == 0 && rng.IntN(10) > 0 {
		k = rng.IntN(6) // mostly an object or an array at the root
	}
	if depth >= 4 {
		k = 6 + rng.IntN(4)
	}
	switch {
	case k < 3:
		v := &genValue{kind: 'o'}
		for range rng.IntN(5) {
			var key string
			if len(v.keys) > 0 && rng.IntN(6) == 0 {
				key = v.keys[rng.IntN(len(v.keys))].key // repeated
			} else {
				key = genKeys[rng.IntN(len(genKeys))]
			}
			if key == "__proto__" && !escapes {
				key = "proto"
			}
			v.keys = append(v.keys, genKey{spelled: spellString(rng, key, escapes), key: key, val: genPathValue(rng, depth+1, escapes)})
		}
		return v
	case k < 6:
		v := &genValue{kind: 'a'}
		for range rng.IntN(5) {
			v.elems = append(v.elems, genPathValue(rng, depth+1, escapes))
		}
		return v
	case k < 7:
		return &genValue{kind: 'n', text: genNumber(rng)}
	case k < 9:
		return &genValue{kind: 's', text: genString(rng, escapes)}
	}
	return &genValue{kind: 'l', text: []string{"true", "false", "null"}[rng.IntN(3)]}
}

// genNumber spells a JSON number: edge cases and random doubles.
func genNumber(rng *rand.Rand) string {
	edges := []string{
		"0", "-0", "1", "-1", "42", "3.14", "1e3", "1E-7", "1e21", "1e20", "123456789012345678901", "1e-6", "1e-7",
		"0.000001", "0.0000001", "123e-20", "1.7976931348623157e308", "1e400", "-1e400", "5e-324", "2e-324", "1e-400",
		"9007199254740993", "100", "1.0", "1.50", "-0.0", "0.1", "0.30000000000000004", "1e+2", "2.5e-5", "12345678.9",
	}
	switch rng.IntN(4) {
	case 0:
		return edges[rng.IntN(len(edges))]
	case 1:
		return strconv.FormatInt(rng.Int64N(1<<62)-1<<61, 10)
	case 2:
		f := math.Float64frombits(rng.Uint64())
		if math.IsNaN(f) || math.IsInf(f, 0) {
			f = 1.5
		}
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return strconv.FormatFloat(rng.NormFloat64()*math.Pow(10, float64(rng.IntN(50)-25)), []byte{'e', 'f', 'g'}[rng.IntN(3)], rng.IntN(20)-1, 64)
}

// genString spells a JSON string value.
func genString(rng *rand.Rand, escapes bool) string {
	plain := []string{"plain", "", "é", "\u2028", "😀", "line", "a b", "quote", "x/y", "tab"}
	esc := []string{`line\nbreak`, `\"q\"`, `\\`, `é`, `\u0001`, `\/`, `😀`, `\ud800`, `\udc00x`, `\u0000`, `\b\f\r\t`, `\u001f`, `\u007f`}
	s := plain[rng.IntN(len(plain))]
	if escapes && rng.IntN(2) == 0 {
		s += esc[rng.IntN(len(esc))]
	} else if rng.IntN(4) == 0 {
		s += []string{`\n`, `\"`, `\\`, `\t`, `\/`}[rng.IntN(5)]
	}
	return `"` + s + `"`
}

// spellString spells key as a JSON string, sometimes with escapes that
// mean the same characters.
func spellString(rng *rand.Rand, key string, escapes bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range key {
		switch {
		case r == '"' || r == '\\':
			b.WriteString(`\` + string(r))
		case r == '\t':
			b.WriteString(`\t`)
		case escapes && r < 0x10000 && rng.IntN(5) == 0:
			fmt.Fprintf(&b, `\u%04x`, r)
		case escapes && r >= 0x10000 && rng.IntN(2) == 0:
			hi, lo := (r-0x10000)>>10+0xD800, (r-0x10000)&0x3FF+0xDC00
			fmt.Fprintf(&b, `\u%04X\u%04x`, hi, lo)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// write spells v as JSON text, with whitespace between some tokens.
func (v *genValue) write(rng *rand.Rand) string {
	var b strings.Builder
	v.writeTo(&b, rng)
	return b.String()
}

func (v *genValue) writeTo(b *strings.Builder, rng *rand.Rand) {
	ws := func() {
		if rng.IntN(8) == 0 {
			b.WriteString([]string{" ", "\n", "\t", "\r\n  "}[rng.IntN(4)])
		}
	}
	switch v.kind {
	case 'o':
		b.WriteByte('{')
		for i, k := range v.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			ws()
			b.WriteString(k.spelled)
			ws()
			b.WriteByte(':')
			ws()
			k.val.writeTo(b, rng)
			ws()
		}
		b.WriteByte('}')
	case 'a':
		b.WriteByte('[')
		for i, e := range v.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			ws()
			e.writeTo(b, rng)
			ws()
		}
		b.WriteByte(']')
	default:
		b.WriteString(v.text)
	}
}

// genPath makes a path that follows v's members for a few steps, and
// sometimes reads what v does not have.
func genPath(rng *rand.Rand, v *genValue) string {
	var b strings.Builder
	left := false // the path has left v's members
	for range rng.IntN(6) {
		// A read past a primitive or past the value's members mostly ends
		// the path, and is optional more often.
		if (left || v != nil && v.kind != 'o' && v.kind != 'a') && rng.IntN(3) > 0 {
			break
		}
		opt := rng.IntN(4) == 0 || left && rng.IntN(2) == 0
		var key string
		switch {
		case v != nil && v.kind == 'o' && len(v.keys) > 0 && rng.IntN(8) > 0:
			// The last repeat of a key is the value JSON.parse keeps.
			k := v.keys[rng.IntN(len(v.keys))]
			key = k.key
			for _, o := range v.keys {
				if o.key == key {
					k = o
				}
			}
			v = k.val
		case v != nil && v.kind == 'a' && rng.IntN(8) > 0:
			i := rng.IntN(len(v.elems) + 2)
			key = strconv.Itoa(i)
			if i < len(v.elems) {
				v = v.elems[i]
			} else {
				v, left = nil, true
			}
		default:
			key = []string{"missing", "length", "toString", "constructor", "__proto__", "0", "9", "hasOwnProperty", "a", "4294967295"}[rng.IntN(10)]
			v, left = nil, true
		}
		b.WriteString(spellStep(rng, key, opt))
	}
	return b.String()
}

// spellStep writes one read of key: .key or ?.key for an identifier,
// [n] for an index, ["key"] or ['key'] otherwise or at random.
func spellStep(rng *rand.Rand, key string, opt bool) string {
	dot, open := ".", "["
	if opt {
		dot, open = "?.", "?.["
	}
	switch {
	case isIdent(key) && rng.IntN(4) > 0:
		return dot + key
	case canonicalIndex(key) && rng.IntN(2) == 0:
		return open + key + "]"
	case !strings.ContainsAny(key, `"\`+"\t"):
		return open + `"` + key + `"]`
	case !strings.ContainsAny(key, `'\`+"\t"):
		return open + `'` + key + `']`
	}
	return dot + "missing"
}

// TestMemberPathPipeline holds the pipeline to its use of member paths: on
// an engine that evaluates them exactly, a member path over a bound value
// or the previous result does not reach the engine, while one that needs
// the results lookup does, and the lookup is read once, by the engine; on
// any other engine, every such path reaches it.
func TestMemberPathPipeline(t *testing.T) {
	ctx := context.Background()
	real := realEngine(t, DefaultLimits(), 0)
	scope := func(r *fakeResults) *Scope {
		return &Scope{
			PreviousID: "b",
			Context:    []Named{{Name: "previous_result", Value: jsonval.FromProducer([]byte(`{"items":[{"id":7}]}`))}},
			FlowInput:  jsonval.FromProducer([]byte(`{"cfg":{"region":"eu-1"}}`)),
			Results:    r,
		}
	}
	cases := []struct {
		text, want string
		engine     bool
	}{
		{"flow_input.cfg.region", `"eu-1"`, false},
		{"previous_result.items[0]?.id", `7`, false},
		{`results["a"].n`, `1`, true},
		{"results.a?.missing?.x", `null`, true},
		{"results.b.items.length", `1`, false},
		{`results["b"]?.items[0].id`, `7`, false},
		{"flow_input.cfg.toString", `null`, true},
		{"flow_input.cfg.region.length", `4`, true},
		{"flow_input.missing.x", "", true},
		{"results.f?.n", "", true},
	}
	for _, exact := range []bool{true, false} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("exact=%t/%s", exact, c.text), func(t *testing.T) {
				counting := &countingEngine{Engine: real}
				var eng Engine = counting
				if exact {
					eng = exactEngine{counting}
				}
				r := &fakeResults{
					stored: map[string]string{"a": `{"n":1}`, "b": `{"from":"the lookup"}`},
					fail:   map[string]string{"f": "Failed to fetch result for step 'f': synthetic"},
				}
				out, err := NewPipeline(eng).Eval(ctx, Compile(SiteInputTransform, c.text), scope(r))
				got := string(out.Producer())
				if err != nil {
					got = err.Error()
				}
				if c.want != "" && got != c.want {
					t.Errorf("%s, want %s", got, c.want)
				}
				if c.want == "" && err == nil {
					t.Errorf("%s, want an error", got)
				}
				if n := counting.n.Load(); (n > 0) != (c.engine || !exact) || n > 1 {
					t.Errorf("%d engine evaluations", n)
				}
				for _, id := range []string{"a", "f"} {
					if n := strings.Count(strings.Join(r.asked, " "), "lookup:"+id); n > 1 {
						t.Errorf("results.%s read %d times: %v", id, n, r.asked)
					}
				}
				if slices.Contains(r.asked, "lookup:b") {
					t.Errorf("the previous id was read from the database: %v", r.asked)
				}
			})
		}
	}
	// The failed read gives the engine's error either way.
	var msgs []string
	for _, exact := range []bool{true, false} {
		var eng Engine = &countingEngine{Engine: real}
		if exact {
			eng = exactEngine{eng.(*countingEngine)}
		}
		_, err := NewPipeline(eng).Eval(ctx, Compile(SiteInputTransform, "results.f?.n"), scope(&fakeResults{fail: map[string]string{"f": "synthetic failure"}}))
		var ee *EvalError
		if !errors.As(err, &ee) {
			t.Fatalf("results.f?.n: %v", err)
		}
		msgs = append(msgs, ee.Message)
	}
	if msgs[0] != msgs[1] {
		t.Fatalf("the failed read's error with the shortcut %q, without %q", msgs[0], msgs[1])
	}
}

// A text that JE-11 rewrites is the engine's, whichever helper it spells
// and wherever: in a quoted key, in a results id, rewritten into another
// key or into a syntax error. The pipeline gives what it gives with member
// paths off.
func TestMemberPathHelperCallTexts(t *testing.T) {
	ctx := context.Background()
	p := NewPipeline(realEngine(t, DefaultLimits(), 0))
	prev := `{"variable(x)":1,"(await variable(x))":2,"resource(x)":3,"(await resource(x))":4,"flow_user_state(x)":5,"(await flow_user_state(x))":6,"a":{"resource(x)":7}}`
	for _, text := range []string{
		`previous_result["variable(x)"]`, `previous_result["resource(x)"]`, `previous_result["flow_user_state(x)"]`,
		`previous_result?.['resource(x)']`, `previous_result.a["resource(x)"]`, `flow_input["variable(x)"]`,
		`previous_result["resource("]`, `results["resource(x)"].a`, `results["variable(x)"]?.a`,
	} {
		t.Run(text, func(t *testing.T) {
			e := Compile(SiteInputTransform, text)
			if parseMemberPath(e) != nil {
				t.Fatalf("a text JE-11 rewrites to %q is taken as a member path", e.Source)
			}
			for _, prevID := range []string{"b", "resource(x)", "variable(x)"} {
				s := &Scope{
					PreviousID: prevID,
					Context:    []Named{{Name: "previous_result", Value: jsonval.FromProducer([]byte(prev))}},
					FlowInput:  jsonval.FromProducer([]byte(prev)),
					Results:    &fakeResults{stored: map[string]string{"resource(x)": prev, "variable(x)": prev}},
				}
				p.paths = true
				on, onErr := p.Eval(ctx, e, s)
				p.paths = false
				off, offErr := p.Eval(ctx, e, s)
				p.paths = true
				if string(on.Producer()) != string(off.Producer()) || fmt.Sprint(onErr) != fmt.Sprint(offErr) {
					t.Fatalf("previous id %q: %s, %v with member paths; %s, %v without", prevID, on.Producer(), onErr, off.Producer(), offErr)
				}
			}
		})
	}
}

// A results.<id> read through the lookup stays under the evaluation's
// attempt limit and retries (JE-2): a lookup slower than the limit fails
// the evaluation with the time-limit error, member paths on or off.
func TestMemberPathLookupKeepsTheAttemptLimit(t *testing.T) {
	l := DefaultLimits()
	l.Attempt = 20 * time.Millisecond
	p := NewPipeline(realEngine(t, l, 1))
	setRetry(p, 1, 0)
	e := Compile(SiteInputTransform, "results.st?.n")
	s := &Scope{PreviousID: "other", Results: slowResults{}}
	for _, paths := range []bool{true, false} {
		p.paths = paths
		_, err := p.Eval(context.Background(), e, s)
		if !errors.Is(err, ErrAttemptTimeout) {
			t.Fatalf("member paths %t: %v, want the attempt limit's error", paths, err)
		}
	}
}

// slowResults answers every lookup after 100 ms, or when its context ends.
type slowResults struct{}

func (slowResults) Stored(context.Context, string) (jsonval.Value, bool) {
	return jsonval.Value{}, false
}

func (slowResults) Lookup(ctx context.Context, _ string) (jsonval.Value, error) {
	select {
	case <-time.After(100 * time.Millisecond):
		return jsonval.FromProducer([]byte(`{"n":7}`)), nil
	case <-ctx.Done():
		return jsonval.Value{}, context.Cause(ctx)
	}
}

// exactEngine is a countingEngine that says it evaluates member paths
// exactly.
type exactEngine struct{ *countingEngine }

func (exactEngine) ExactMemberPaths() {}

package expr

import (
	"context"
	"encoding/json"
	"flag"
	"math/rand/v2"
	"sync/atomic"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// fuzzSites are the sites a fuzzed case may take (a predicate site wraps
// its text as Boolean(…), JE-24).
var fuzzSites = []SiteKind{SiteInputTransform, SiteFailureInputTransform, SiteIterator, SiteBranchOne, SiteSkipIf, SiteStopAfterIf, SiteStopAfterAllItersIf}

// FuzzJSExprMatchesEngine checks that every answer jsExpr gives is the
// engine's: for a text it parses, at a site, over a flow input, a previous
// result and step results (a JSON object of id to value), whatever it
// answers QuickJS answers too, and where QuickJS fails jsExpr declines. Its
// seed corpus is the differential generator's cases (TestJSExprsMatchEngine)
// and every expression of the conformance suite with its scope (the cases
// of conformance/exprs and the flow fixtures' unit cases), so `go test`
// runs them all as a test, and
//
//	go test -run '^$' -fuzz FuzzJSExprMatchesEngine ./internal/expr
//
// explores further.
func FuzzJSExprMatchesEngine(f *testing.F) {
	rng := rand.New(rand.NewPCG(5, 11))
	seeds := 0
	for range 400 {
		c := genJSCase(rng)
		f.Add(uint8(SiteInputTransform), c.text, c.flowInput, c.prev, `{"st":`+c.st+`}`)
		seeds++
	}
	for _, c := range loadE3a(f) {
		s := c.scope(f)
		prev := "null"
		if v, ok := s.context("previous_result"); ok {
			prev = string(v.Producer())
		}
		res := "{}"
		if r, ok := s.Results.(*fakeResults); ok && len(r.stored) > 0 {
			m := map[string]json.RawMessage{}
			for id, v := range r.stored {
				if json.Valid([]byte(v)) {
					m[id] = json.RawMessage(v)
				}
			}
			b, _ := json.Marshal(m)
			res = string(b)
		}
		f.Add(uint8(c.site), c.text, string(textOr(s.FlowInput, jsonNull)), prev, res)
		seeds++
	}
	// Run as a test, the seeds must reach jsExpr's answers, not only its
	// declines, or the comparison would pass empty: at least a tenth of them,
	// as TestJSExprsMatchEngine asks of its cases.
	var answered atomic.Int64
	if fz := flag.Lookup("test.fuzz"); fz == nil || fz.Value.String() == "" {
		f.Cleanup(func() {
			f.Logf("jsExpr answered %d of %d seeds", answered.Load(), seeds)
			if answered.Load() < int64(seeds)/10 {
				f.Errorf("jsExpr answered only %d of %d seeds", answered.Load(), seeds)
			}
		})
	}
	p := NewPipeline(realEngine(f, DefaultLimits(), 0))
	f.Fuzz(func(t *testing.T, site uint8, text, flowInput, prev, results string) {
		e := Compile(fuzzSites[int(site)%len(fuzzSites)], text)
		x := parseJSExpr(e)
		if x == nil {
			return
		}
		// A scope's values are JSON texts (jsonval.Value), as the engine
		// binds them; the fuzzer's other strings are not cases.
		if !json.Valid([]byte(flowInput)) || !json.Valid([]byte(prev)) {
			return
		}
		stored := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(results), &stored); err != nil {
			return
		}
		scope := func() *Scope {
			r := &fakeResults{stored: map[string]string{}}
			for id, v := range stored {
				r.stored[id] = string(v)
			}
			s := &Scope{
				PreviousID: "pv",
				FlowInput:  jsonval.FromProducer([]byte(flowInput)),
				Context:    []Named{{Name: "previous_result", Value: jsonval.FromProducer([]byte(prev))}},
				Results:    memoryResults{r},
			}
			if e.Site != SiteInputTransform && e.Site != SiteFailureInputTransform {
				s.Context = append(s.Context, Named{Name: "result", Value: jsonval.FromProducer([]byte(prev))})
			}
			return s
		}
		s := scope()
		if err := checkDepth(e, s, bind(e, s)); err != nil {
			return
		}
		ctx := context.Background()
		got, ok := x.eval(ctx, s, jsLimits{limits: DefaultLimits(), bound: bind(e, s).Strings, memoryReads: true})
		if !ok {
			return
		}
		answered.Add(1)
		want, err := p.evaluate(ctx, e, bind(e, scope()))
		if err != nil {
			t.Fatalf("%q at %v (flow_input %.200s, previous_result %.200s, results %.200s): jsExpr answered %s, the engine failed: %v", text, e.Site, flowInput, prev, results, got, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%q at %v (flow_input %.200s, previous_result %.200s, results %.200s): jsExpr answered %s, the engine %s", text, e.Site, flowInput, prev, results, got, want)
		}
	})
}

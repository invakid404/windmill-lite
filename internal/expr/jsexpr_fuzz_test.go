package expr

import (
	"context"
	"encoding/json"
	"flag"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// fuzzSite decodes a fuzzed case's site: a byte that is a SiteKind is that
// site, so every seed replays at the site it was recorded for (a predicate
// site wraps its text as Boolean(…), JE-24); any other byte maps onto one of
// the sites, so the fuzzer reaches them all.
func fuzzSite(b uint8) SiteKind {
	if k := SiteKind(b); k.Valid() {
		return k
	}
	return SiteKind(int(b)%(len(siteNames)-1) + 1)
}

// fuzzScope is the scope a site sees, as siteScope gives it for the
// conformance cases: previous_result, and result except at an input
// transform, unless prev is empty (a case with no previous result); params
// {} at an input transform (JE-23); the results lookup,
// here one that reads memory, except at sleep, retry_if and skip_expr, and
// at a stop predicate only when its text reads results.
func fuzzScope(site SiteKind, text, flowInput, prev string, stored map[string]json.RawMessage) *Scope {
	s := &Scope{PreviousID: "pv", FlowInput: jsonval.FromProducer([]byte(flowInput))}
	transform := site == SiteInputTransform || site == SiteFailureInputTransform
	if prev != "" {
		s.Context = append(s.Context, Named{Name: "previous_result", Value: jsonval.FromProducer([]byte(prev))})
		if !transform {
			s.Context = append(s.Context, Named{Name: "result", Value: jsonval.FromProducer([]byte(prev))})
		}
	}
	if transform {
		s.Context = append(s.Context, Named{Name: "params", Value: jsonval.FromProducer([]byte("{}"))})
	}
	r := &fakeResults{stored: map[string]string{}}
	for id, v := range stored {
		r.stored[id] = string(v)
	}
	switch site {
	case SiteSleep, SiteRetryIf, SiteSkipExpr:
	case SiteStopAfterIf, SiteStopAfterAllItersIf:
		if strings.Contains(text, "results.") || strings.Contains(text, "results[") || strings.Contains(text, "results?.") {
			s.Results = memoryResults{r}
		}
	default:
		s.Results = memoryResults{r}
	}
	return s
}

// FuzzJSExprMatchesEngine checks that every answer jsExpr gives is the
// engine's: for a text it parses, at any site (fuzzSite), over a flow
// input, a previous result (empty: none) and step results (a JSON object of
// id to value) in the site's scope (fuzzScope), whatever it
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
		prev := ""
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
		e := Compile(fuzzSite(site), text)
		x := parseJSExpr(e)
		if x == nil {
			return
		}
		// A scope's values are JSON texts (jsonval.Value), as the engine
		// binds them; the fuzzer's other strings are not cases.
		if !json.Valid([]byte(flowInput)) || prev != "" && !json.Valid([]byte(prev)) {
			return
		}
		stored := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(results), &stored); err != nil {
			return
		}
		scope := func() *Scope { return fuzzScope(e.Site, text, flowInput, prev, stored) }
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

// TestFuzzSeedsKeepTheirSites checks the fuzz test's site encoding: every
// site decodes to itself, any other byte to some site, and every
// conformance seed replays at its recorded site, with its text wrapped as
// that site wraps it and with the results lookup and context names that
// siteScope gives the recorded case.
func TestFuzzSeedsKeepTheirSites(t *testing.T) {
	for k := SiteKind(1); k.Valid(); k++ {
		if got := fuzzSite(uint8(k)); got != k {
			t.Errorf("site %v decodes as %v", k, got)
		}
	}
	for _, b := range []uint8{0, uint8(len(siteNames)), 200, 255} {
		if !fuzzSite(b).Valid() {
			t.Errorf("byte %d decodes as %v", b, fuzzSite(b))
		}
	}
	names := func(s *Scope) []string {
		var out []string
		for _, n := range s.Context {
			out = append(out, n.Name)
		}
		return out
	}
	sites := map[SiteKind]int{}
	for _, c := range loadE3a(t) {
		k := fuzzSite(uint8(c.site))
		if k != c.site {
			t.Fatalf("%s: recorded at %v, replayed at %v", c.key, c.site, k)
		}
		sites[k]++
		e := Compile(k, c.text)
		if wrapped := strings.HasPrefix(e.Trimmed, "Boolean(") && !strings.HasPrefix(strings.TrimSpace(c.text), "Boolean("); wrapped != k.Predicate() {
			t.Errorf("%s at %v: wrapped as a predicate %t, want %t", c.key, k, wrapped, k.Predicate())
		}
		want := c.scope(t)
		prev := ""
		if _, ok := want.context("previous_result"); ok {
			prev = "null"
		}
		got := fuzzScope(k, c.text, "{}", prev, nil)
		if (want.Results == nil) != (got.Results == nil) {
			t.Errorf("%s at %v: results lookup %t, the recorded case's %t", c.key, k, got.Results != nil, want.Results != nil)
		}
		if w, g := strings.Join(names(want), ","), strings.Join(names(got), ","); w != g {
			t.Errorf("%s at %v: context %s, the recorded case's %s", c.key, k, g, w)
		}
	}
	t.Logf("conformance seeds by site: %v", sites)
}

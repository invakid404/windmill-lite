package expr

import (
	"context"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// BenchmarkEvalMemberPath evaluates member paths over JSON values, the
// expressions that read a context value's or a step's member, which the
// shortcuts of JE-6 to JE-9 do not answer: a nested flow input key, an
// index into the previous result, and an optional member of the previous
// step's result read as results.<id>. It is the per-expression cost the
// engine pays at an input transform, on the real engine.
func BenchmarkEvalMemberPath(b *testing.B) {
	p := NewPipeline(realEngine(b, DefaultLimits(), 0))
	s := &Scope{
		PreviousID: "b",
		Context:    []Named{{Name: "previous_result", Value: jsonval.FromProducer([]byte(`{"n":2,"data":{"items":[{"id":7,"name":"item"}],"pad":"x"}}`))}},
		FlowInput:  jsonval.FromProducer([]byte(`{"tenant":"tenant-a","cfg":{"region":"eu-1","tier":2}}`)),
		Results:    &fakeResults{stored: map[string]string{"a": `{"n":1}`}},
	}
	for _, text := range []string{"flow_input.cfg.region", "previous_result.data.items[0].id", "results.b?.n"} {
		e := Compile(SiteInputTransform, text)
		p.Prepare(e)
		want, err := p.Eval(context.Background(), e, s)
		if err != nil {
			b.Fatalf("%s: %v", text, err)
		}
		b.Run(text, func(b *testing.B) {
			for b.Loop() {
				v, err := p.Eval(context.Background(), e, s)
				if err != nil || string(v.Producer()) != string(want.Producer()) {
					b.Fatalf("%s: %s, %v", text, v.Producer(), err)
				}
			}
		})
	}
}

// BenchmarkEvalJSExpr evaluates the expressions jsExpr answers (milestone
// PS4) through Eval, with it on and with it off (QuickJS): a template over
// the flow input and a step's result read through a memory lookup,
// arithmetic, and a skip_if predicate.
func BenchmarkEvalJSExpr(b *testing.B) {
	eng := realEngine(b, DefaultLimits(), 0)
	s := &Scope{
		PreviousID: "b",
		Context:    []Named{{Name: "previous_result", Value: jsonval.FromProducer([]byte(`{"n":2,"data":{"items":[{"id":7,"name":"item"}],"pad":"x"}}`))}},
		FlowInput:  jsonval.FromProducer([]byte(`{"tenant":"tenant-a","cfg":{"region":"eu-1","tier":2}}`)),
		Results:    memoryResults{&fakeResults{stored: map[string]string{"a": `{"n":1}`}}},
	}
	cases := []struct {
		site SiteKind
		text string
	}{
		{SiteInputTransform, "`${flow_input.tenant}/${results.a.n}`"},
		{SiteInputTransform, "previous_result.n * flow_input.cfg.tier + 1"},
		{SiteSkipIf, `flow_input.cfg.region === "eu-1" && results.a.n > 0`},
	}
	for _, js := range []bool{true, false} {
		p := NewPipeline(eng)
		p.SetQuickJSOnly(!js)
		for _, c := range cases {
			e := Compile(c.site, c.text)
			p.Prepare(e)
			want, err := p.Eval(context.Background(), e, s)
			if err != nil {
				b.Fatalf("%s: %v", c.text, err)
			}
			name := "quickjs/" + c.text
			if js {
				name = "jsexpr/" + c.text
			}
			b.Run(name, func(b *testing.B) {
				for b.Loop() {
					v, err := p.Eval(context.Background(), e, s)
					if err != nil || string(v.Producer()) != string(want.Producer()) {
						b.Fatalf("%s: %s, %v", c.text, v.Producer(), err)
					}
				}
			})
		}
	}
}

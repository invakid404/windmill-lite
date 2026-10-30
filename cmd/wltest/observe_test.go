package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// TestResolvedArgsKeyOrder: the engine shuffles the top-level keys of a
// step's arguments in each run, because Windmill leaves their order
// unspecified ([ADR 0001] §2). The observation lists the resolved
// arguments' top-level members in jsonb's key order, whatever order the
// trace holds: a trace with each of the 24 orders of four arguments
// observes the same resolved_args text, shorter keys first, with each
// value's text kept, a nested object's member order and a number's
// spelling included.
//
// [ADR 0001]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0001-equivalence-contract.md
func TestResolvedArgsKeyOrder(t *testing.T) {
	members := []string{`"zz":{"y":1,"x":2}`, `"b":1.50e3`, `"aaa":"resolved"`, `"a":[3,1]`}
	const want = `{"a":[3,1],"b":1.50e3,"zz":{"y":1,"x":2},"aaa":"resolved"}`
	site := conformance.SitePath{}.Child(conformance.Module("s"))
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	n := 0
	for _, order := range permutations(len(members)) {
		var ms []string
		for _, i := range order {
			ms = append(ms, members[i])
		}
		resolved := "{" + strings.Join(ms, ",") + "}"
		recorded := strings.Replace(resolved, `"resolved"`, `"$var:f/x/v"`, 1)
		tr := &engine.Trace{RunID: "11111111-1111-4111-8111-111111111111", Status: conformance.RunSuccess, Result: v(`{}`), Steps: []engine.StepRecord{{
			Site: site, Status: conformance.StepSuccess, Module: conformance.ModuleSuccess, Ran: true,
			RecordedArgs: v(recorded), ResolvedArgs: v(resolved), Result: v(`{}`), Carried: v(`{}`),
		}}}
		got, _, err := observeTrace(tr, []string{"s"}, nil, false, nil, conformance.CompareSpec{}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if g := string(got.Steps[site].ResolvedArgs); g != want {
			t.Fatalf("resolved arguments %s observed as %s, want %s", resolved, g, want)
		}
		n++
	}
	if n != 24 {
		t.Fatalf("%d orders, want 24", n)
	}
}

// TestKeyOrdered: an object's top-level members come in jsonb's key order
// with their texts kept, white space between members included; a key is
// ordered by its decoded bytes; anything but one JSON object is returned
// as it is.
func TestKeyOrdered(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"bb":1,"a":2}`, `{"a":2,"bb":1}`},
		{"{ \"bb\" : 1 ,\n \"a\":{\"d\":0,\"c\":1}}", "{\"a\":{\"d\":0,\"c\":1},\"bb\" : 1}"},
		// é decodes to two bytes, so it sorts after the one-byte z.
		{`{"é":1,"z":2}`, `{"z":2,"é":1}`},
		{`{}`, `{}`},
		{`[2,1]`, `[2,1]`},
		{`"text"`, `"text"`},
		{`{"a":1} {"b":2}`, `{"a":1} {"b":2}`},
		{`{"a":`, `{"a":`},
	} {
		if got := string(keyOrdered(json.RawMessage(c.in))); got != c.want {
			t.Errorf("keyOrdered(%s) = %s, want %s", c.in, got, c.want)
		}
	}
	if keyOrdered(nil) != nil {
		t.Error("keyOrdered(nil) is not nil")
	}
}

// permutations returns every order of 0..n-1.
func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, p := range permutations(n - 1) {
		for i := 0; i <= len(p); i++ {
			q := append(append(append([]int{}, p[:i]...), n-1), p[i:]...)
			out = append(out, q)
		}
	}
	return out
}

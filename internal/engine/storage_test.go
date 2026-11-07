package engine

import (
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// storeMembers' held values: a member whose text is its
// held value's producer text takes that value's stored text; any other
// member is canonicalised from its bytes; the stored arguments and the
// errors are the same either way.
func TestStoreMembersHeld(t *testing.T) {
	for _, c := range []struct {
		args string
		held []string
	}{
		{`{"a":{"b":1.50e1,"a":[1,2]},"n":7}`, []string{`{"b":1.50e1,"a":[1,2]}`, ""}},
		{`{"p":{"z":"é\/","a":-0.0}}`, []string{`{"z":"é\/","a":-0.0}`}},
		// A held value whose bytes differ from the member's is not used.
		{`{"p":{"a":1}}`, []string{`{"a": 1}`}},
		// Fewer held values than members, and a duplicate key.
		{`{"a":1,"a":{"x":2},"b":3}`, []string{"1"}},
		// A NUL is removed from a key and from a value alike (FX-48).
		{`{"\u0000":1}`, []string{"1"}},
		{`{"a":"\u0000"}`, []string{`"\u0000"`}},
		// A value jsonb refuses fails the push with the same text.
		{`{"a":1e999999999999}`, []string{`1e999999999999`}},
		{`{"a":"\ud800"}`, []string{`"\ud800"`}},
	} {
		var held []jsonval.Value
		for _, h := range c.held {
			if h == "" {
				held = append(held, jsonval.Value{})
				continue
			}
			held = append(held, jsonval.FromProducer([]byte(h)))
		}
		args := jsonval.FromProducer([]byte(c.args))
		got, gerr := storeMembers(args, held...)
		want, werr := storeMembers(args)
		if (gerr == nil) != (werr == nil) || gerr != nil && gerr.Error() != werr.Error() || string(got.Producer()) != string(want.Producer()) {
			t.Errorf("%s: %s, %v; without held values %s, %v", c.args, got.Producer(), gerr, want.Producer(), werr)
		}
	}
	// The held value's stored text is the one taken: a value printed from
	// a stored text keeps it (jsonval.FromPrinted), here a marker that
	// shows which text was used.
	marked := jsonval.FromPrinted([]byte(`{"a": 1}`), []byte(`{"a":"held"}`))
	got, err := storeMembers(jsonval.FromProducer([]byte(`{"p":{"a": 1}}`)), marked)
	if err != nil || string(got.Producer()) != `{"p":{"a":"held"}}` {
		t.Fatalf("the held stored text was not taken: %s, %v", got.Producer(), err)
	}
	got, err = storeMembers(jsonval.FromProducer([]byte(`{"p":{"a":1}}`)), marked)
	if err != nil || string(got.Producer()) != `{"p":{"a":1}}` {
		t.Fatalf("a held value with other bytes was taken: %s, %v", got.Producer(), err)
	}
}

// FX-48 cases 2 and 3: the push removes every NUL escape from a job's
// arguments, names and values at any depth, and of names the removal makes
// equal keeps the value of the name that held no NUL, else of the smallest
// original name; equal names that held none keep the last, as jsonb does.
func TestArgsWithoutNUL(t *testing.T) {
	for _, c := range []struct{ args, stored string }{
		{`{"s":"x\u0000y"}`, `{"s":"xy"}`},
		{`{"k\u0000":1}`, `{"k":1}`},
		{`{"a":{"b\u0000":["\u0000c"]}}`, `{"a":{"b":["c"]}}`},
		{`{"k\u0000":1,"k":2}`, `{"k":2}`},
		{`{"k":1,"k\u0000":2}`, `{"k":1}`},
		{`{"\u0000k":1,"k\u0000":2}`, `{"k":1}`},
		{`{"k\u0000":1,"\u0000k":2}`, `{"k":2}`},
		{`{"a":"\\u0000"}`, `{"a":"\\u0000"}`},
		{`{"a":1,"a":2}`, `{"a":2}`},
		// A name given more than once keeps its last value, whatever its
		// spelling, before the rule for distinct names applies.
		{`{"k\u0000":1,"k\u0000":2}`, `{"k":2}`},
		{`{"k\u0000":1,"\u006b\u0000":2}`, `{"k":2}`},
		{`{"\u0000k":1,"k\u0000":2,"\u0000k":3}`, `{"k":3}`},
		{`{"k\u0000":1,"\u0000k":2,"k\u0000":3}`, `{"k":2}`},
		{`{"k":1,"k\u0000":2,"k":3}`, `{"k":3}`},
		{`{"k\u0000":1,"k":2,"k\u0000":3}`, `{"k":2}`},
	} {
		got, err := storeMembers(jsonval.FromProducer([]byte(c.args)))
		if err != nil || string(got.Producer()) != c.stored {
			t.Errorf("storeMembers(%s) = %s, %v; want %s", c.args, got.Producer(), err, c.stored)
		}
		stored, err := argsWithoutNUL(jsonval.FromProducer([]byte(c.args))).Stored()
		if err != nil || string(stored) != c.stored {
			t.Errorf("argsWithoutNUL(%s) stores %s, %v; want %s", c.args, stored, err, c.stored)
		}
	}
	// Arguments with no NUL are returned as they are; text that is not
	// exactly one object loses its NULs and nothing else, so what checks
	// its syntax later sees the same text as without them.
	for _, c := range []struct{ args, want string }{
		{`{"b":1,"a":2}`, `{"b":1,"a":2}`},
		{`{"k":"x\u0000y"}!`, `{"k":"xy"}!`},
		{`{"k":"x\u0000y"} {}`, `{"k":"xy"} {}`},
		{`["\u0000"]`, `[""]`},
	} {
		if got := argsWithoutNUL(jsonval.FromProducer([]byte(c.args))); string(got.Producer()) != c.want {
			t.Errorf("argsWithoutNUL(%s) = %s, want %s", c.args, got, c.want)
		}
	}
}

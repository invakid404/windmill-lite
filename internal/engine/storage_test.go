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
		// A key jsonb refuses still fails the push.
		{`{"\u0000":1}`, []string{"1"}},
		// A value jsonb refuses fails it with the same text.
		{`{"a":"\u0000"}`, []string{`"\u0000"`}},
		{`{"a":1e999999999999}`, []string{`1e999999999999`}},
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

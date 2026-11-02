package wl

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// TestPayloadSplice holds the host_call payloads written by splicing
// stored texts (lever J4) to json.Marshal's: the plugin's JSON.parse reads
// the same values from both, and the bytes differ only in the escapes
// encoding/json adds inside the stored texts (<, >, &, U+2028, U+2029).
// A value whose producer text jsonb refuses is written as encoding/json
// writes it, its errors included.
func TestPayloadSplice(t *testing.T) {
	v := func(s string) jsonval.Value { return jsonval.FromProducer([]byte(s)) }
	texts := []string{
		"{\"html\":\"<b>a & b</b>\",\"sep\":\"a\u2028b\u2029c\",\"esc\":\"\\u00e9\\n\\t\\\"\\\\\\/\",\"n\":[1.50,-0,1e21,12345678901234567890],\"z\":null}",
		`[{"b":2,"a":1,"a":3},"<script>",true,false,{}]`,
		`"plain"`, `0.0`, "{\"k\":\"\\u0001\\u001f\"}", `{"long":"` + strings.Repeat("x<y>&", 5000) + `"}`,
	}
	site, err := conformance.ParseSitePath("m:a")
	if err != nil {
		t.Fatal(err)
	}
	rec := func(s string) StepRecord {
		return StepRecord{
			Site: site, Attempt: 1, Status: conformance.StepStatus("success"), Ran: true,
			Hooks: []string{"H2"}, Job: "j&1", RecordedArgs: v(`{"p":` + s + `}`), ResolvedArgs: v(`{"p":` + s + `}`),
			Result: v(s), Carried: v(s), Start: time.Date(2026, 11, 2, 1, 2, 3, 4, time.UTC), Duration: 1500 * time.Microsecond,
			Late: []engine.Late{{Kind: engine.LateKind("log"), Body: v(s)}},
		}
	}
	var payloads []map[string]any
	for _, s := range texts {
		payloads = append(payloads,
			map[string]any{"record": recordJSON(rec(s)), "site": siteJSON(engine.Site{Path: site, Flow: "f/<x>"})},
			map[string]any{"index": 2, "args": Call{Args: storedText(v(`{"p":` + s + `}`)), args: v(`{"p":` + s + `}`)}.payloadArgs()},
			map[string]any{"trace": traceJSON(&Trace{RunID: "r", Status: conformance.RunStatus("success"), Result: v(s), Steps: []StepRecord{rec(s), rec(`1`)},
				Warnings: []engine.Warning{{Code: "c<", Message: "m "}}})},
		)
	}
	// A producer text with no stored text: a NUL escape that jsonb refuses,
	// kept spaced as written, and text that is not JSON at all.
	payloads = append(payloads,
		map[string]any{"record": recordJSON(rec("{\"a\": \"\\u0000<\"}"))},
		map[string]any{"args": json.RawMessage(`{ "a" : 1 }`)},
	)
	unescape := strings.NewReplacer("\\u003c", "<", "\\u003e", ">", "\\u0026", "&", "\\u2028", "\u2028", "\\u2029", "\u2029")
	for i, p := range payloads {
		got, gerr := marshalPayload(p)
		want, werr := json.Marshal(p)
		if gerr != nil || werr != nil {
			t.Fatalf("payload %d: %v; json.Marshal %v", i, gerr, werr)
		}
		var gv, wv any
		if err := json.Unmarshal(got, &gv); err != nil {
			t.Fatalf("payload %d is not JSON: %v\n%.300s", i, err, got)
		}
		if err := json.Unmarshal(want, &wv); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gv, wv) {
			t.Errorf("payload %d parses differently:\n%.300s\n%.300s", i, got, want)
		}
		if g, w := unescape.Replace(string(got)), unescape.Replace(string(want)); g != w {
			at := 0
			for at < len(g) && at < len(w) && g[at] == w[at] {
				at++
			}
			t.Errorf("payload %d differs beyond the escapes at byte %d:\n%q\n%q", i, at, g[max(0, at-40):min(len(g), at+60)], w[max(0, at-40):min(len(w), at+60)])
		}
	}
	// The stored texts are spliced: the HTML of a stored text stays raw.
	got, err := marshalPayload(map[string]any{"result": payloadText(v(`"<b>"`))})
	if err != nil || string(got) != `{"result":"<b>"}` {
		t.Fatalf("the stored text was not spliced: %s, %v", got, err)
	}
	// Text that is not JSON fails as json.Marshal fails.
	bad := map[string]any{"args": json.RawMessage(`{"a":1} x`), "site": map[string]any{"path": "p"}}
	_, gerr := marshalPayload(bad)
	_, werr := json.Marshal(bad)
	if gerr == nil || werr == nil || gerr.Error() != werr.Error() {
		t.Fatalf("invalid text: %v; json.Marshal %v", gerr, werr)
	}
	var se *json.MarshalerError
	if !errors.As(gerr, &se) {
		t.Fatalf("the error is not json.Marshal's: %T", gerr)
	}
	if !bytes.Equal([]byte(spliced(nil).must()), []byte("null")) {
		t.Fatal("a nil spliced text does not marshal as null")
	}
}

func (s spliced) must() []byte {
	b, _ := s.MarshalJSON()
	return b
}

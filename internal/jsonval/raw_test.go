package jsonval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// j spells a probe's bytes: @u is a backslash-u escape's start, @L and @P
// are raw U+2028 and U+2029, @FF is the byte 0xff (invalid UTF-8), @01 the
// byte 0x01 and @BOM a UTF-8 byte order mark.
func j(s string) []byte {
	return []byte(strings.NewReplacer(
		"@u", "\\u", "@L", string(rune(0x2028)), "@P", string(rune(0x2029)),
		"@FF", "\xff", "@01", "\x01", "@BOM", "\xef\xbb\xbf",
	).Replace(s))
}

// equivalenceProbes are the inputs of TestEquivalence: the JSON-library
// study's exactness and equivalence probes (numbers beyond float64,
// -0, HTML, U+2028 and U+2029, invalid UTF-8, unpaired surrogates, control
// characters, duplicate keys, key order, trailing text, a byte order
// mark, deep nesting, the struct-decoding combinations), and what may
// follow an object: the end, a token, a number beyond float64 and text
// that cannot start one.
var equivalenceProbes = []string{
	`{"n":12345678901234567890}`, `{"n":9007199254740993}`, `{"n":-0}`, `{"n":-0.0}`, `{"n":1E400}`,
	`{"n":1e21}`, `{"n":1.5e-7}`, `{"n":1.50}`, `{"n":123456789012345678901234567890.123456789}`,
	`{"n":NaN}`, `{"n":Infinity}`, `{"n":01}`, `{"s":"<b>&amp;</b>"}`, `{"s":"a@Lb@Pc"}`,
	`{"s":"a@u2028b@u2029c"}`, `{"s":"a@FFb"}`, `{"@FF":1}`, `{"a@FF@u0041":1}`, `{"@ud800":1}`, `{"@ud800x":2}`,
	`{"@udc00@ud800":3}`, `{"@ud83d@ude00":4}`, `{"@u0000":5}`, `{"a@u00e9":6}`, "{\"\xed\xa0\x80\":7}",
	`{"s":"@ud800x"}`, `{"s":"@u00e9\/@u0041\t"}`, `{"s":"a@01b"}`, `{"@01":1}`,
	`{"a":1,"a":2}`, `{"a":1,"b":2,"a":3}`, `{"a":1,"a":null}`, `{"b":1,"a":2,"aa":3,"B":4}`,
	` { "a" : [ 1 , 2 ] , "b" : { } } `, "\t{\n\"a\"\r:\n1\n}\n", `{"a":1}`, `{}`, `{ }`,
	`{"a":1} x`, `{"a":1}{"b":2}`, `{"a":1} 2`, `{"a":1}2`, `{"a":1} 1e400`, `{"a":1} -`, `{"a":1} "x`,
	`{"a":1} "x"`, `{"a":1} }`, `{"a":1} ]`, `{"a":1} ,`, `{"a":1} [1,`, `{"a":1} tru`, `{"a":1} true`,
	`{"a":1} null`, `{"a":1} 01`, `{"a":1} "@FF"`, `{"a":1} "a@01"`, `{"a":1}` + "\x00",
	`{"a":1,}`, `{"a" 1}`, `{"a":}`, `{1:2}`, `{"a":1`, `{"a"`, `{`, `}`, ``, `   `, `null`, `"str"`, `7`, `true`,
	`[1,2]`, `[1,null,{"a":1},"x",[]]`, `[ 1 , [ 2 ] ]`, `[]`, `[1,2,]`, `[1] x`, `[1] 2`, `@BOM{}`, `@BOM[]`,
	strings.Repeat("[", 5000) + strings.Repeat("]", 5000),
	strings.Repeat("[", 10001) + strings.Repeat("]", 10001),
	`{"a":` + strings.Repeat("[", 9999) + strings.Repeat("]", 9999) + `}`,
	`{"a":` + strings.Repeat("[", 10000) + strings.Repeat("]", 10000) + `}`,
	`{"ERROR":{"NAME":"Fatal"},"Decline":true}`, `{"error":{"name":7}}`,
	`{"error":{"name":"Fatal","name":null}}`, `{"error":{"name":"Fatal"},"error":null}`,
	`{"e-rror":{"name":"Fatal"}}`, `{"error":{"n_ame":"Fatal"}}`, `{"Error":{"Name":"Fatal"}}`,
	`{"decline":true,"decline":null}`, `{"de-cline":true}`, `{"failed":true,"failed":null}`,
	`{"decline":false,"result":{"a":1},"failed":true}`, `{"result":null,"failed":"yes"}`,
	`{"args":{"x":1},"args":null}`, `{"found":true,"type":"t","value":{"k":[1,2]}}`,
	`{"found":true,"value":"v","secret":null}`, `{"env":{"A":"1","B":null}}`, `{"env":{"A":"1","a":"2"}}`,
	`{"warnings":[{"code":"c","message":"m"},null]}`, `{"wm_failure":"boom","wm_labels":["a",null]}`,
	`{"_MODULES":{"a":1},"x":"y\"z","_TEMP_SCRIPT_REFS":[],"_ENTRYPOINT_OVERRIDE":"main"}`,
	`{"_MOD@u0055LES":1,"x":2}`, `{"t":"result","id":"j1","body":{"a":[1,2,{"b":"c"}]}}`,
}

// TestEquivalence holds the raw-text reads to the encoding/json code they
// replace, on every probe, error texts included, and fails on any
// difference: RawObject and RawArray to json.Unmarshal into a
// map[string]json.RawMessage and a []json.RawMessage (harness.Decode,
// engine.Entrypoint, wl's intercept map; engine.elements), and Members to
// the two token walks it replaces, engine.members's and harness.scriptArgs's
// (decoded keys, key and value texts, and what each did with the text that
// follows the object).
func TestEquivalence(t *testing.T) {
	for _, p := range equivalenceProbes {
		in := j(p)
		name := p
		if len(name) > 60 {
			name = name[:60] + "…"
		}
		gm, gerr := RawObject(in)
		var wm map[string]json.RawMessage
		werr := json.Unmarshal(in, &wm)
		if !sameErr(gerr, werr) || gerr == nil && !reflect.DeepEqual(gm, wm) {
			t.Errorf("RawObject(%q) = %v, %v; encoding/json %v, %v", name, gm, gerr, wm, werr)
		}
		ga, gerr := RawArray(in)
		var wa []json.RawMessage
		werr = json.Unmarshal(in, &wa)
		if !sameErr(gerr, werr) || gerr == nil && !reflect.DeepEqual(ga, wa) {
			t.Errorf("RawArray(%q) = %q, %v; encoding/json %q, %v", name, ga, gerr, wa, werr)
		}
		var got []string
		rest, ok := Members(in, func(k string, ks, ke, vs, ve int) {
			got = append(got, fmt.Sprintf("%q|%q|%q", k, in[ks:ke], in[vs:ve]))
		})
		want, wok, after := tokenWalk(in)
		// engine.members refuses an object a token follows; scriptArgs
		// keeps only an object the end of the text follows.
		engineOK, wantEngineOK := ok && rest != RestValue, wok && after != nil
		argsOK, wantArgsOK := ok && rest == RestNone, wok && errors.Is(after, io.EOF)
		if engineOK != wantEngineOK || argsOK != wantArgsOK {
			t.Errorf("Members(%q): engine walk %v, scriptArgs walk %v; encoding/json %v, %v (rest %d, then %v)", name, engineOK, argsOK, wantEngineOK, wantArgsOK, rest, after)
		}
		// A walk that fails may have reported members before it failed;
		// the callers discard them, so only a walk that succeeds must match.
		if ok && wok && !reflect.DeepEqual(got, want) {
			t.Errorf("Members(%q) = %q; encoding/json %q", name, got, want)
		}
	}
	t.Logf("%d probes", len(equivalenceProbes))
}

// sameErr reports whether both fail with the same text, or both succeed.
func sameErr(g, w error) bool {
	if (g == nil) != (w == nil) {
		return false
	}
	return g == nil || g.Error() == w.Error()
}

// tokenWalk is the walk engine.members and harness.scriptArgs made with
// encoding/json before the raw-text reads: each member's decoded key, its
// key's text (the text from the end of the previous value to the key's
// end, less whitespace and the comma) and its value's text, whether the
// text starts with one object, and the error of the token read after it
// (nil when a token follows).
func tokenWalk(obj []byte) (ms []string, ok bool, after error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false, nil
	}
	for dec.More() {
		start := dec.InputOffset()
		t, err := dec.Token()
		key, isKey := t.(string)
		if err != nil || !isKey {
			return nil, false, nil
		}
		keyEnd := dec.InputOffset()
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false, nil
		}
		raw := bytes.TrimSpace(bytes.TrimLeft(bytes.TrimSpace(obj[start:keyEnd]), ","))
		ms = append(ms, fmt.Sprintf("%q|%q|%q", key, raw, v))
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, false, nil
	}
	_, err := dec.Token()
	return ms, true, err
}

// TestMembersInPlace checks that Members reads its text in place: the
// offsets it reports index the text, and a walk over a large object
// allocates far less than the text.
func TestMembersInPlace(t *testing.T) {
	big := []byte(`{"a":"` + strings.Repeat("x", 1<<20) + `","b":[1,2,3],"c":{"d":null}}`)
	allocs := testing.AllocsPerRun(5, func() {
		if _, ok := Members(big, func(string, int, int, int, int) {}); !ok {
			t.Fatal("not an object")
		}
	})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	Members(big, func(string, int, int, int, int) {})
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; allocs > 20 || n > 64<<10 {
		t.Fatalf("Members allocated %v times, %d bytes, for a %d-byte object", allocs, n, len(big))
	}
}

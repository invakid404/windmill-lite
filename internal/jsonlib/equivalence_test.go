package jsonlib_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonlib"
	"github.com/invakid404/windmill-lite/internal/jsonlib/std"
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

// equivalenceProbes are the inputs of TestEquivalence: the exactness
// probes of internal/jsonlib/bench, and the struct-decoding combinations
// the JC1 review found (a duplicate field whose later value is null, and
// field names with a dash or an underscore).
var equivalenceProbes = []string{
	`{"n":12345678901234567890}`, `{"n":9007199254740993}`, `{"n":-0}`, `{"n":-0.0}`, `{"n":1E400}`,
	`{"n":1e21}`, `{"n":1.5e-7}`, `{"n":1.50}`, `{"n":123456789012345678901234567890.123456789}`,
	`{"n":NaN}`, `{"n":Infinity}`, `{"n":01}`, `{"s":"<b>&amp;</b>"}`, `{"s":"a@Lb@Pc"}`,
	`{"s":"a@u2028b@u2029c"}`, `{"s":"a@FFb"}`, `{"@FF":1}`, `{"s":"@ud800x"}`, `{"s":"@u00e9\/@u0041\t"}`,
	`{"s":"a@01b"}`, `{"a":1,"a":2}`, `{"a":1,"b":2,"a":3}`, `{"a":1,"a":null}`, `{"b":1,"a":2,"aa":3,"B":4}`,
	` { "a" : [ 1 , 2 ] , "b" : { } } `, `{"a":1} x`, `{"a":1}{"b":2}`, `{"a":1,}`, ``, `null`, `"str"`,
	`[1,2]`, `[1,null,{"a":1},"x",[]]`, `[1,2,]`, `[1] x`, `@BOM{}`,
	strings.Repeat("[", 5000) + strings.Repeat("]", 5000),
	strings.Repeat("[", 10001) + strings.Repeat("]", 10001),
	`{"ERROR":{"NAME":"Fatal"},"Decline":true}`, `{"error":{"name":7}}`,
	// The review's counterexamples.
	`{"error":{"name":"Fatal","name":null}}`, `{"error":{"name":"Fatal"},"error":null}`,
	`{"e-rror":{"name":"Fatal"}}`, `{"error":{"n_ame":"Fatal"}}`, `{"Error":{"Name":"Fatal"}}`,
	`{"decline":true,"decline":null}`, `{"de-cline":true}`, `{"failed":true,"failed":null}`,
	`{"decline":false,"result":{"a":1},"failed":true}`, `{"result":null,"failed":"yes"}`,
	`{"args":{"x":1},"args":null}`, `{"found":true,"type":"t","value":{"k":[1,2]}}`,
	`{"found":true,"value":"v","secret":null}`, `{"env":{"A":"1","B":null}}`, `{"env":{"A":"1","a":"2"}}`,
	`{"warnings":[{"code":"c","message":"m"},null]}`, `{"wm_failure":"boom","wm_labels":["a",null]}`,
}

// decodeTargets are the Go values the routed sites decode into, by the
// site that does it.
var decodeTargets = []struct {
	site string
	new  func() any
}{
	{"engine.errorName", func() any {
		return &struct {
			Error struct {
				Name string `json:"name"`
			} `json:"error"`
		}{}
	}},
	{"wl outcomeReply", func() any {
		return &struct {
			Decline bool            `json:"decline"`
			Result  json.RawMessage `json:"result"`
			Failed  bool            `json:"failed"`
		}{}
	}},
	{"wl rewrite_args answer", func() any {
		return &struct {
			Args json.RawMessage `json:"args"`
		}{}
	}},
	{"wl resource answer", func() any {
		return &struct {
			Found bool            `json:"found"`
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		}{}
	}},
	{"wl variable answer", func() any {
		return &struct {
			Found  bool   `json:"found"`
			Value  string `json:"value"`
			Secret bool   `json:"secret"`
		}{}
	}},
	{"wl invocation_env answer", func() any {
		return &struct {
			Env map[string]string `json:"env"`
		}{}
	}},
	{"wl on_run_end answer", func() any {
		return &struct {
			Warnings []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"warnings"`
		}{}
	}},
	{"map of raw members", func() any { return &map[string]json.RawMessage{} }},
	{"raw elements", func() any { return &[]json.RawMessage{} }},
	{"string", func() any { return new(string) }},
	{"*string (wm_failure)", func() any { return new(*string) }},
	{"map of strings (env)", func() any { return &map[string]string{} }},
}

// TestEquivalence compares what the selected build does at every routed
// site with encoding/json, on every probe: Valid, RawObject, RawArray,
// Members, and Unmarshal into each Go value a routed site decodes into. A
// build that claims encoding/json's behaviour (jsonlib.V1Compatible) must
// match it exactly, error texts included, since two sites pass a decode
// error's text on (a protocol error, the host's refusal of an intercept);
// for the others the differences are logged, error texts left out.
func TestEquivalence(t *testing.T) {
	diffs := 0
	report := func(format string, args ...any) {
		diffs++
		if jsonlib.V1Compatible {
			t.Errorf(format, args...)
		} else {
			t.Logf("DIFF "+format, args...)
		}
	}
	// sameErr: whether both fail or both succeed and, for a build that
	// claims encoding/json's behaviour, with the same text.
	sameErr := func(g, w error) bool {
		if (g == nil) != (w == nil) {
			return false
		}
		return g == nil || !jsonlib.V1Compatible || g.Error() == w.Error()
	}
	for _, p := range equivalenceProbes {
		in := j(p)
		name := p
		if len(name) > 60 {
			name = name[:60] + "…"
		}
		if got, want := jsonlib.Valid(in), std.Valid(in); got != want {
			report("Valid(%q) = %v, encoding/json %v", name, got, want)
		}
		gm, gerr := jsonlib.RawObject(in)
		wm, werr := std.RawObject(in)
		if !sameErr(gerr, werr) || gerr == nil && !rawMapsEqual(gm, wm) {
			report("RawObject(%q) = %s, %v; encoding/json %s, %v", name, showMap(gm), gerr, showMap(wm), werr)
		}
		ga, gerr := jsonlib.RawArray(in)
		wa, werr := std.RawArray(in)
		if !sameErr(gerr, werr) || gerr == nil && !rawSlicesEqual(ga, wa) {
			report("RawArray(%q) = %q, %v; encoding/json %q, %v", name, ga, gerr, wa, werr)
		}
		var gms, wms []string
		gok := jsonlib.Members(in, func(k string, ks, ke, vs, ve int) {
			gms = append(gms, fmt.Sprintf("%q|%q|%q", k, in[ks:ke], in[vs:ve]))
		})
		wok := std.Members(in, func(k string, ks, ke, vs, ve int) {
			wms = append(wms, fmt.Sprintf("%q|%q|%q", k, in[ks:ke], in[vs:ve]))
		})
		// A walk that fails may have reported members before it failed; the
		// callers discard them, so only a walk that succeeds must match.
		if gok != wok || gok && !reflect.DeepEqual(gms, wms) {
			report("Members(%q) = %v %v; encoding/json %v %v", name, gok, gms, wok, wms)
		}
		for _, target := range decodeTargets {
			g, w := target.new(), target.new()
			gerr, werr := jsonlib.Unmarshal(in, g), std.Unmarshal(in, w)
			// Whether it fails must match; what a failed decode left behind
			// is never read by a routed site, so values are compared when
			// both succeed.
			if !sameErr(gerr, werr) || gerr == nil && !reflect.DeepEqual(g, w) {
				report("Unmarshal(%q) into %s = %+v, %v; encoding/json %+v, %v", name, target.site, reflect.ValueOf(g).Elem(), gerr, reflect.ValueOf(w).Elem(), werr)
			}
		}
	}
	t.Logf("%s: %d differences from encoding/json over %d probes", jsonlib.Name, diffs, len(equivalenceProbes))
}

func rawMapsEqual(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) || (a == nil) != (b == nil) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || string(v) != string(w) {
			return false
		}
	}
	return true
}

func rawSlicesEqual(a, b []json.RawMessage) bool {
	if len(a) != len(b) || (a == nil) != (b == nil) {
		return false
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			return false
		}
	}
	return true
}

func showMap(m map[string]json.RawMessage) string {
	var b strings.Builder
	for k, v := range m {
		fmt.Fprintf(&b, "%q=%q ", k, v)
	}
	return b.String()
}
